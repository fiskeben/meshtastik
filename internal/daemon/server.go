package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"time"

	"meshtastik/internal/device"
	"meshtastik/internal/transport"
)

// helloTimeout bounds how long a new connection may take to send its hello.
const helloTimeout = 10 * time.Second

// Server is the daemon: it accepts clients on a listener and holds one node
// connection (hub) per target, shared by every client of that target.
type Server struct {
	Version string
	// Dial returns the dialer for a target's node.
	Dial func(Target) (transport.Dialer, error)
	// IdleTimeout closes a node connection that has had no clients this
	// long, and ends Serve once there have been no connections and no
	// clients this long; 0 never does either.
	IdleTimeout time.Duration
	Log         io.Writer // default io.Discard
	// DeviceOptions, when set, adjusts the options of each node connection.
	DeviceOptions func(*device.Options)
	Socket        string // reported by status
	LogFile       string // reported by status

	mu         sync.Mutex
	hubs       map[Target]*hub
	conns      int       // open client connections
	lastActive time.Time // when conns last dropped to 0, or a hub closed
	started    time.Time
	idleExited bool
	stopOnce   sync.Once
	stop       chan struct{} // closed by the stop op
	wg         sync.WaitGroup
}

func (s *Server) logf(format string, args ...any) {
	fmt.Fprintf(s.Log, "%s "+format+"\n", append([]any{time.Now().Format(time.DateTime)}, args...)...)
}

// Serve accepts clients on ln until ctx ends, a client sends stop, or the
// idle timeout passes. It closes ln and every node connection on return.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.Log == nil {
		s.Log = io.Discard
	}
	s.mu.Lock()
	s.hubs = map[Target]*hub{}
	s.started = time.Now()
	s.lastActive = s.started
	s.stop = make(chan struct{})
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.logf("meshtastik daemon %s listening on %s (pid %d)", s.Version, ln.Addr(), os.Getpid())

	go func() {
		defer ln.Close()
		tick := time.NewTicker(s.tick())
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				s.logf("stop requested")
				return
			case <-tick.C:
				if s.idle() {
					s.logf("idle for %s; exiting", s.IdleTimeout)
					return
				}
			}
		}
	}()

	var err error
	for {
		conn, aerr := ln.Accept()
		if aerr != nil {
			if ctx.Err() == nil && !s.stopped() && !s.idleExit() {
				err = aerr
			}
			break
		}
		s.mu.Lock()
		s.conns++
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(ctx, conn)
			s.mu.Lock()
			if s.conns--; s.conns == 0 {
				s.lastActive = time.Now()
			}
			s.mu.Unlock()
		}()
	}
	cancel()
	s.mu.Lock()
	hubs := make([]*hub, 0, len(s.hubs))
	for _, h := range s.hubs {
		hubs = append(hubs, h)
	}
	s.mu.Unlock()
	for _, h := range hubs {
		h.close()
	}
	s.wg.Wait()
	return err
}

// tick is how often idleness is checked.
func (s *Server) tick() time.Duration {
	if s.IdleTimeout <= 0 {
		return time.Hour
	}
	return min(max(s.IdleTimeout/10, 10*time.Millisecond), 5*time.Second)
}

// idle closes node connections that have been unused for the idle timeout
// and reports whether the daemon itself has been.
func (s *Server) idle() bool {
	if s.IdleTimeout <= 0 {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	var unused []*hub
	for _, h := range s.hubs {
		if h.unusedSince(now) > s.IdleTimeout {
			unused = append(unused, h)
		}
	}
	s.mu.Unlock()
	for _, h := range unused {
		s.logf("%s: no clients for %s; disconnecting", h.target, s.IdleTimeout)
		h.close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hubs) == 0 && s.conns == 0 && now.Sub(s.lastActive) > s.IdleTimeout {
		s.idleExited = true
		return true
	}
	return false
}

func (s *Server) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *Server) idleExit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idleExited
}

func (s *Server) reply(conn net.Conn, r Reply) error {
	r.Proto, r.Version = Proto, s.Version
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	conn.SetWriteDeadline(time.Now().Add(helloTimeout))
	_, err = conn.Write(append(b, '\n'))
	conn.SetWriteDeadline(time.Time{})
	return err
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(helloTimeout))
	line, err := br.ReadBytes('\n')
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		return
	}
	var h Hello
	if err := json.Unmarshal(line, &h); err != nil {
		s.reply(conn, Reply{Error: fmt.Sprintf("bad hello: %v", err)})
		return
	}
	if h.Proto != Proto {
		s.reply(conn, Reply{Error: fmt.Sprintf("daemon speaks protocol %d, client %d — run 'meshtastik daemon restart'", Proto, h.Proto)})
		return
	}
	switch h.Op {
	case OpStatus:
		st := s.status()
		s.reply(conn, Reply{OK: true, Status: &st})
	case OpStop:
		s.reply(conn, Reply{OK: true})
		s.stopOnce.Do(func() { close(s.stop) })
	case OpConnect:
		s.connect(ctx, conn, br, h)
	default:
		s.reply(conn, Reply{Error: fmt.Sprintf("unknown op %q", h.Op)})
	}
}

// connect attaches conn to the node connection for h.Target, opening it
// first if needed, and relays until either side closes.
func (s *Server) connect(ctx context.Context, conn net.Conn, br *bufio.Reader, h Hello) {
	if _, err := h.Target.TransportKind(); err != nil || h.Target.Addr == "" {
		s.reply(conn, Reply{Error: fmt.Sprintf("bad target %q", h.Target)})
		return
	}
	// Notice the client hanging up while the node connection is opened. The
	// peek leaves any byte it sees in br for the session.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	gone := make(chan struct{})
	go func() {
		defer close(gone)
		if _, err := br.Peek(1); err != nil {
			cancel()
		}
	}()
	abort := func() {
		conn.Close()
		<-gone
	}
	var hb *hub
	var kind transport.Kind
	var err error
	for {
		hb = s.hub(h.Target, time.Duration(h.Timeout))
		kind, err = hb.waitUp(wctx)
		if !errors.Is(err, errHubClosed) || wctx.Err() != nil {
			break // a hub closed for idleness while we waited is replaced
		}
	}
	if err != nil {
		if wctx.Err() == nil { // not when the client hung up or the daemon is stopping
			s.reply(conn, Reply{Error: err.Error()})
		}
		abort()
		return
	}
	sess := newSession(hb, conn, h.Backlog)
	if !hb.attach(sess) {
		s.reply(conn, Reply{Error: "connection to the node was lost; try again"})
		abort()
		return
	}
	defer hb.detach(sess)
	if err := s.reply(conn, Reply{OK: true, Kind: kind.String()}); err != nil {
		abort()
		return
	}
	select {
	case <-gone: // the client has sent its first frame, or hung up
	case <-wctx.Done(): // the daemon is stopping
		abort()
		return
	case <-sess.done: // the node connection dropped
		abort()
		return
	}
	if wctx.Err() != nil { // the client hung up
		return
	}
	sess.run(ctx, br)
}

// hub returns the node connection for t, starting one if there is none.
func (s *Server) hub(t Target, timeout time.Duration) *hub {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h := s.hubs[t]; h != nil {
		return h
	}
	h := newHub(s, t)
	s.hubs[t] = h
	s.logf("%s: connecting", t)
	go h.run(timeout)
	return h
}

// removeHub forgets h once it has closed.
func (s *Server) removeHub(h *hub) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hubs[h.target] == h {
		delete(s.hubs, h.target)
		s.lastActive = time.Now()
	}
}

func (s *Server) status() Status {
	s.mu.Lock()
	hubs := make([]*hub, 0, len(s.hubs))
	for _, h := range s.hubs {
		hubs = append(hubs, h)
	}
	st := Status{PID: os.Getpid(), Version: s.Version, Started: s.started, Socket: s.Socket, Log: s.LogFile,
		IdleTimeout: Duration(s.IdleTimeout), Links: []LinkStatus{}}
	s.mu.Unlock()
	for _, h := range hubs {
		st.Links = append(st.Links, h.status())
	}
	slices.SortFunc(st.Links, func(a, b LinkStatus) int {
		switch {
		case a.Target.String() < b.Target.String():
			return -1
		case a.Target.String() > b.Target.String():
			return 1
		}
		return 0
	})
	return st
}
