package ircmesh

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"meshtastik/internal/irc"
)

const (
	sendQueue       = 4096 // lines queued for a client before it is dropped
	registerTimeout = 60 * time.Second
	pingAfter       = 90 * time.Second // of client silence, then as long again for an answer
	writeTimeout    = 30 * time.Second
	maxLine         = 8191 + 512 // tags plus an RFC 1459 line
)

// client is one IRC connection. Its fields after conn are guarded by s.mu.
type client struct {
	s      *Server
	conn   net.Conn
	out    chan string // lines to write; "" closes the connection
	done   chan struct{}
	once   sync.Once
	heard  atomic.Int64 // unix nanos of the last line read
	pinged atomic.Bool

	nick, pass  string // as sent during registration
	gotUser     bool
	capPending  bool // CAP LS or REQ seen, END not yet
	registered  bool
	serverTime  bool
	parted      map[int]bool // channel indexes this client has left
	overflowing bool
}

func (c *client) close() {
	c.once.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}

// target is the nick numerics are addressed to: * until registered.
func (c *client) target() string {
	if c.registered {
		return c.s.selfNick()
	}
	return "*"
}

// queue sends m to the client without blocking. A client too slow to keep
// up is dropped. The caller holds s.mu.
func (c *client) queue(m irc.Message) { c.queueLine(m.String()) }

func (c *client) queueLine(l string) {
	if c.overflowing {
		return
	}
	select {
	case <-c.done:
	case c.out <- l:
	default:
		c.overflowing = true
		c.s.logf("%s: not keeping up; disconnecting", c.conn.RemoteAddr())
		go func() {
			c.conn.SetWriteDeadline(time.Now().Add(time.Second))
			fmt.Fprint(c.conn, "ERROR :SendQ exceeded\r\n")
			c.close()
		}()
	}
}

// hangUp sends ERROR and closes the connection once it is written. The
// caller holds s.mu.
func (c *client) hangUp(reason string) {
	c.queue(irc.Message{Command: "ERROR", Params: []string{reason}})
	c.queueLine("")
}

// serveClient runs one IRC connection until it closes or ctx ends.
func (s *Server) serveClient(ctx context.Context, conn net.Conn) {
	c := &client{s: s, conn: conn, out: make(chan string, sendQueue), done: make(chan struct{}), parted: map[int]bool{}}
	c.heard.Store(time.Now().UnixNano())
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		c.close()
	}()
	go c.writeLoop()
	go c.watchdog()

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 1024), maxLine)
	for sc.Scan() {
		c.heard.Store(time.Now().UnixNano())
		c.pinged.Store(false)
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		m, err := irc.Parse(line)
		if err != nil {
			continue
		}
		s.mu.Lock()
		s.handle(c, m)
		s.mu.Unlock()
	}
}

func (c *client) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case l := <-c.out:
			if l == "" {
				c.close()
				return
			}
			c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, err := c.conn.Write([]byte(l + "\r\n")); err != nil {
				c.close()
				return
			}
		}
	}
}

// watchdog drops clients that don't register in time, and pings silent
// ones, dropping those that don't answer.
func (c *client) watchdog() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	start := time.Now()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		c.s.mu.Lock()
		silent := time.Since(time.Unix(0, c.heard.Load()))
		switch {
		case !c.registered && time.Since(start) > registerTimeout:
			c.hangUp("Registration timeout")
		case silent > 2*pingAfter && c.pinged.Load():
			c.hangUp("Ping timeout")
		case silent > pingAfter && !c.pinged.Load():
			c.pinged.Store(true)
			c.queue(irc.Message{Command: "PING", Params: []string{serverName}})
		}
		c.s.mu.Unlock()
	}
}

// handle acts on one message from c. The caller holds s.mu.
func (s *Server) handle(c *client, m irc.Message) {
	switch m.Command {
	case "CAP":
		s.handleCap(c, m)
		return
	case "PASS":
		if c.registered {
			c.queue(s.numeric(c, "462", "You may not reregister"))
		} else if len(m.Params) > 0 {
			c.pass = m.Params[0]
		}
		return
	case "NICK":
		if c.registered {
			c.queue(s.numeric(c, "484", "nicks follow the node's names; rename the node with meshtastik"))
			return
		}
		if len(m.Params) == 0 || m.Params[0] == "" {
			c.queue(s.numeric(c, "431", "No nickname given"))
			return
		}
		c.nick = m.Params[0]
		s.tryRegister(c)
		return
	case "USER":
		if c.registered {
			c.queue(s.numeric(c, "462", "You may not reregister"))
			return
		}
		if len(m.Params) < 4 {
			c.queue(s.numeric(c, "461", "USER", "Not enough parameters"))
			return
		}
		c.gotUser = true
		s.tryRegister(c)
		return
	case "PING":
		token := serverName
		if len(m.Params) > 0 {
			token = m.Params[0]
		}
		c.queue(irc.Message{Prefix: serverName, Command: "PONG", Params: []string{serverName, token}})
		return
	case "PONG":
		return
	case "QUIT":
		c.hangUp("Closing link (quit)")
		return
	}
	if !c.registered {
		c.queue(s.numeric(c, "451", "You have not registered"))
		return
	}
	if h, ok := commands[m.Command]; ok {
		h(s, c, m)
		return
	}
	c.queue(s.numeric(c, "421", m.Command, "Unknown command"))
}

// commands are the handlers for registered clients, by command.
var commands = map[string]func(s *Server, c *client, m irc.Message){}

// handleCap negotiates capabilities; only server-time is offered.
func (s *Server) handleCap(c *client, m irc.Message) {
	sub := ""
	if len(m.Params) > 0 {
		sub = strings.ToUpper(m.Params[0])
	}
	reply := func(params ...string) {
		c.queue(irc.Message{Prefix: serverName, Command: "CAP", Params: append([]string{c.target()}, params...)})
	}
	switch sub {
	case "LS":
		if !c.registered {
			c.capPending = true
		}
		reply("LS", "server-time")
	case "LIST":
		if c.serverTime {
			reply("LIST", "server-time")
		} else {
			reply("LIST", "")
		}
	case "REQ":
		if !c.registered {
			c.capPending = true
		}
		var req string
		if len(m.Params) > 1 {
			req = m.Params[1]
		}
		on, ok := c.serverTime, true
		for _, cp := range strings.Fields(req) {
			switch cp {
			case "server-time":
				on = true
			case "-server-time":
				on = false
			default:
				ok = false
			}
		}
		if !ok || req == "" {
			reply("NAK", req)
			return
		}
		c.serverTime = on
		reply("ACK", req)
	case "END":
		c.capPending = false
		s.tryRegister(c)
	default:
		c.queue(s.numeric(c, "410", sub, "Invalid CAP command"))
	}
}

// tryRegister completes registration once NICK, USER and capability
// negotiation are done.
func (s *Server) tryRegister(c *client) {
	if c.registered || c.nick == "" || !c.gotUser || c.capPending {
		return
	}
	if s.cfg.Password != "" && c.pass != s.cfg.Password {
		c.queue(s.numeric(c, "464", "Password incorrect"))
		c.hangUp("Closing link (bad password)")
		return
	}
	c.registered = true
	s.welcome(c)
}

// welcome sends the registration burst, renames the client to the node's
// nick and joins it to every channel.
func (s *Server) welcome(c *client) {
	nick := s.selfNick()
	n := func(code string, params ...string) { c.queue(s.numeric(c, code, params...)) }
	n("001", "Welcome to the mesh, "+nick)
	n("002", fmt.Sprintf("Your host is %s, running version %s", serverName, s.cfg.Version))
	n("003", "This server was created "+s.started.Format(time.RFC1123))
	n("004", serverName, s.cfg.Version, "i", "nt")
	n("005", "CHANTYPES=#", fmt.Sprintf("NICKLEN=%d", nickLen), "CHANNELLEN=32", "CASEMAPPING=ascii",
		"NETWORK=mesh", "are supported by this server")
	n("375", "- "+serverName+" Message of the day -")
	n("372", fmt.Sprintf("- node %s (!%08x)", s.dir.realname(s.self), s.self))
	if s.firmware != "" {
		n("372", "- firmware "+s.firmware)
	}
	n("376", "End of /MOTD command.")
	if irc.Fold(c.nick) != irc.Fold(nick) {
		c.queue(irc.Message{Prefix: fmt.Sprintf("%s!%08x@mesh", c.nick, s.self), Command: "NICK", Params: []string{nick}})
	}
	for _, i := range s.indexes() {
		s.joinBurst(c, i)
	}
	s.sendReplay(c)
}

// joinBurst joins c to channel index i: JOIN, topic and names.
func (s *Server) joinBurst(c *client, i int) {
	name := s.names[i]
	c.queue(irc.Message{Prefix: s.dir.mask(s.self), Command: "JOIN", Params: []string{name}})
	c.queue(s.numeric(c, "332", name, channelTopic(s.chans[i], s.lora)))
	s.sendNames(c, i)
}

// sendNames sends the members of channel index i: the IRC user, then the
// nodes heard there.
func (s *Server) sendNames(c *client, i int) {
	name := s.names[i]
	nicks := []string{s.selfNick()}
	for _, num := range s.members.members(i) {
		nicks = append(nicks, s.dir.nick(num))
	}
	var line []string
	size := 0
	for _, n := range nicks {
		if size+len(n) > 400 && len(line) > 0 {
			c.queue(s.numeric(c, "353", "=", name, strings.Join(line, " ")))
			line, size = nil, 0
		}
		line = append(line, n)
		size += len(n) + 1
	}
	c.queue(s.numeric(c, "353", "=", name, strings.Join(line, " ")))
	c.queue(s.numeric(c, "366", name, "End of /NAMES list."))
}
