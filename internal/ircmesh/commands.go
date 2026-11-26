package ircmesh

import (
	"fmt"
	"strings"
	"time"

	"meshtastik/internal/irc"
)

func init() {
	commands["JOIN"] = (*Server).cmdJoin
	commands["PART"] = (*Server).cmdPart
	commands["NAMES"] = (*Server).cmdNames
	commands["LIST"] = (*Server).cmdList
	commands["TOPIC"] = (*Server).cmdTopic
	commands["WHO"] = (*Server).cmdWho
	commands["WHOIS"] = (*Server).cmdWhois
	commands["MODE"] = (*Server).cmdMode
	commands["USERHOST"] = (*Server).cmdUserhost
	commands["ISON"] = (*Server).cmdIson
	commands["AWAY"] = (*Server).cmdAway
}

// param is m's i'th parameter, or "".
func param(m irc.Message, i int) string {
	if i < len(m.Params) {
		return m.Params[i]
	}
	return ""
}

func (s *Server) needParams(c *client, m irc.Message, n int) bool {
	if len(m.Params) < n {
		c.queue(s.numeric(c, "461", m.Command, "Not enough parameters"))
		return false
	}
	return true
}

func (s *Server) cmdJoin(c *client, m irc.Message) {
	if !s.needParams(c, m, 1) {
		return
	}
	if m.Params[0] == "0" {
		for _, i := range s.indexes() {
			if !c.parted[i] {
				s.part(c, i, "")
			}
		}
		return
	}
	for name := range strings.SplitSeq(m.Params[0], ",") {
		i, ok := s.channelIndex(name)
		switch {
		case !ok:
			c.queue(s.numeric(c, "403", name, "No such channel"))
		case c.parted[i]:
			delete(c.parted, i)
			s.joinBurst(c, i)
		}
	}
}

func (s *Server) part(c *client, i int, reason string) {
	c.parted[i] = true
	p := []string{s.names[i]}
	if reason != "" {
		p = append(p, reason)
	}
	c.queue(irc.Message{Prefix: s.dir.mask(s.self), Command: "PART", Params: p})
}

func (s *Server) cmdPart(c *client, m irc.Message) {
	if !s.needParams(c, m, 1) {
		return
	}
	for name := range strings.SplitSeq(m.Params[0], ",") {
		i, ok := s.channelIndex(name)
		switch {
		case !ok:
			c.queue(s.numeric(c, "403", name, "No such channel"))
		case c.parted[i]:
			c.queue(s.numeric(c, "442", s.names[i], "You're not on that channel"))
		default:
			s.part(c, i, param(m, 1))
		}
	}
}

func (s *Server) cmdNames(c *client, m irc.Message) {
	if len(m.Params) == 0 {
		for _, i := range s.indexes() {
			if !c.parted[i] {
				s.sendNames(c, i)
			}
		}
		return
	}
	for name := range strings.SplitSeq(m.Params[0], ",") {
		if i, ok := s.channelIndex(name); ok {
			s.sendNames(c, i)
		} else {
			c.queue(s.numeric(c, "366", name, "End of /NAMES list."))
		}
	}
}

func (s *Server) cmdList(c *client, m irc.Message) {
	c.queue(s.numeric(c, "321", "Channel", "Users  Name"))
	for _, i := range s.indexes() {
		n := 1 + len(s.members.members(i))
		c.queue(s.numeric(c, "322", s.names[i], fmt.Sprint(n), channelTopic(s.chans[i], s.lora)))
	}
	c.queue(s.numeric(c, "323", "End of /LIST"))
}

func (s *Server) cmdTopic(c *client, m irc.Message) {
	if !s.needParams(c, m, 1) {
		return
	}
	i, ok := s.channelIndex(m.Params[0])
	switch {
	case !ok:
		c.queue(s.numeric(c, "403", m.Params[0], "No such channel"))
	case len(m.Params) > 1:
		c.queue(s.numeric(c, "482", s.names[i], "The topic describes the channel on the node and can't be set"))
	default:
		c.queue(s.numeric(c, "332", s.names[i], channelTopic(s.chans[i], s.lora)))
	}
}

// whoLine is a 352 reply for node num seen in channel ch ("*" for none).
func (s *Server) whoLine(c *client, ch string, num uint32) irc.Message {
	return s.numeric(c, "352", ch, fmt.Sprintf("%08x", num), "mesh", serverName, s.dir.nick(num), "H",
		"0 "+s.dir.user(num).GetLongName())
}

func (s *Server) cmdWho(c *client, m irc.Message) {
	mask := param(m, 0)
	if i, ok := s.channelIndex(mask); ok {
		c.queue(s.whoLine(c, s.names[i], s.self))
		for _, num := range s.members.members(i) {
			c.queue(s.whoLine(c, s.names[i], num))
		}
	} else if num, ok := s.dir.lookup(mask); ok {
		c.queue(s.whoLine(c, "*", num))
	}
	c.queue(s.numeric(c, "315", mask, "End of /WHO list."))
}

// ago describes how long ago t was, briefly.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute ago"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm ago", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh ago", int(d.Hours())/24, int(d.Hours())%24)
}

func (s *Server) cmdWhois(c *client, m irc.Message) {
	if !s.needParams(c, m, 1) {
		return
	}
	nick := m.Params[len(m.Params)-1] // WHOIS [server] nick
	num, ok := s.dir.lookup(nick)
	if !ok {
		c.queue(s.numeric(c, "401", nick, "No such nick/channel"))
		c.queue(s.numeric(c, "318", nick, "End of /WHOIS list."))
		return
	}
	nick = s.dir.nick(num)
	c.queue(s.numeric(c, "311", nick, fmt.Sprintf("%08x", num), "mesh", "*", s.dir.user(num).GetLongName()))
	var chans []string
	if num == s.self {
		c.queue(s.numeric(c, "312", nick, serverName, "this node"))
		for _, i := range s.indexes() {
			chans = append(chans, s.names[i])
		}
	} else {
		info := "never heard"
		last := s.lastHeard(num)
		if !last.IsZero() {
			info = "last heard " + ago(s.cfg.Now().Sub(last))
		}
		c.queue(s.numeric(c, "312", nick, serverName, info))
		for _, i := range s.members.channels(num) {
			chans = append(chans, s.names[i])
		}
		if !last.IsZero() {
			idle := int64(s.cfg.Now().Sub(last).Seconds())
			c.queue(s.numeric(c, "317", nick, fmt.Sprint(idle), "seconds idle"))
		}
	}
	if len(chans) > 0 {
		c.queue(s.numeric(c, "319", nick, strings.Join(chans, " ")))
	}
	c.queue(s.numeric(c, "318", nick, "End of /WHOIS list."))
}

func (s *Server) cmdMode(c *client, m irc.Message) {
	if !s.needParams(c, m, 1) {
		return
	}
	target := m.Params[0]
	if strings.HasPrefix(target, "#") {
		i, ok := s.channelIndex(target)
		switch {
		case !ok:
			c.queue(s.numeric(c, "403", target, "No such channel"))
		case len(m.Params) == 1:
			c.queue(s.numeric(c, "324", s.names[i], "+nt"))
		case strings.Trim(m.Params[1], "+") == "b":
			c.queue(s.numeric(c, "368", s.names[i], "End of channel ban list"))
		default:
			c.queue(s.numeric(c, "482", s.names[i], "Channel modes follow the node and can't be changed"))
		}
		return
	}
	if irc.Fold(target) != irc.Fold(s.selfNick()) {
		c.queue(s.numeric(c, "502", "Can't change mode for other users"))
		return
	}
	switch param(m, 1) {
	case "":
		c.queue(s.numeric(c, "221", "+i"))
	case "+i", "-i", "i":
	default:
		c.queue(s.numeric(c, "501", "Unknown MODE flag"))
	}
}

func (s *Server) cmdUserhost(c *client, m irc.Message) {
	var out []string
	for _, nick := range m.Params {
		if num, ok := s.dir.lookup(nick); ok {
			out = append(out, fmt.Sprintf("%s=+%08x@mesh", s.dir.nick(num), num))
		}
	}
	c.queue(s.numeric(c, "302", strings.Join(out, " ")))
}

func (s *Server) cmdIson(c *client, m irc.Message) {
	var out []string
	for _, p := range m.Params {
		for nick := range strings.FieldsSeq(p) {
			num, ok := s.dir.lookup(nick)
			if ok && (num == s.self || len(s.members.channels(num)) > 0) {
				out = append(out, s.dir.nick(num))
			}
		}
	}
	c.queue(s.numeric(c, "303", strings.Join(out, " ")))
}

func (s *Server) cmdAway(c *client, m irc.Message) {
	if param(m, 0) == "" {
		c.queue(s.numeric(c, "305", "You are no longer marked as being away"))
	} else {
		c.queue(s.numeric(c, "306", "You have been marked as being away"))
	}
}
