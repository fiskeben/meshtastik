package ircmesh

import (
	"fmt"
	"strings"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/device"
	"meshtastik/internal/irc"
)

// serverTimeFormat is the IRCv3 server-time tag format.
const serverTimeFormat = "2006-01-02T15:04:05.000Z"

// packet takes one packet from the node: names, membership, then text. The
// caller holds s.mu.
func (s *Server) packet(p *pb.MeshPacket) {
	from := p.GetFrom()
	d := p.GetDecoded()
	if d.GetPortnum() == pb.PortNum_NODEINFO_APP && from != s.self {
		u := &pb.User{}
		if proto.Unmarshal(d.GetPayload(), u) == nil {
			s.nickChanges(s.dir.set(from, u))
		}
	}
	if ch, ok := counts(p, s.self); ok {
		if _, known := s.names[ch]; known && s.heard(from, ch) {
			s.toChannel(ch, irc.Message{Prefix: s.dir.mask(from), Command: "JOIN", Params: []string{s.names[ch]}})
		}
	}
	if d.GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP {
		s.text(p)
	}
}

// text delivers a text packet, or keeps it for replay when no client is
// attached. The caller holds s.mu.
func (s *Server) text(p *pb.MeshPacket) {
	from, to := p.GetFrom(), p.GetTo()
	ch := int(p.GetChannel())
	var target string
	var key replayKey
	dm := false
	switch {
	case to == device.Broadcast:
		name, ok := s.names[ch]
		if !ok {
			return
		}
		target, key = name, replayKey{Channel: ch}
	case from == s.self: // sent by another daemon client
		target, key = s.dir.nick(to), replayKey{Channel: -1, Peer: to}
	case to == s.self:
		target, key, dm = s.selfNick(), replayKey{Channel: -1, Peer: from}, true
	default:
		return
	}
	lines, action := renderText(p.GetDecoded(), s.recent)
	s.recent.add(p.GetId(), strings.Join(meshLines(p.GetDecoded().GetPayload()), " "))
	clients := s.registered()
	for _, l := range lines {
		if action {
			l = irc.CTCP("ACTION", l)
		}
		m := irc.Message{Prefix: s.dir.mask(from), Command: "PRIVMSG", Params: []string{target, l}}
		if len(clients) == 0 {
			s.replay.add(key, replayEntry{At: s.cfg.Now(), Msg: m, DM: dm})
			continue
		}
		for _, c := range clients {
			if key.Channel < 0 || !c.parted[ch] {
				c.queue(m)
			}
		}
	}
}

// toChannel sends m to the registered clients in channel index ch. The
// caller holds s.mu.
func (s *Server) toChannel(ch int, m irc.Message) {
	for _, c := range s.registered() {
		if !c.parted[ch] {
			c.queue(m)
		}
	}
}

// toAll sends m to every registered client. The caller holds s.mu.
func (s *Server) toAll(m irc.Message) {
	for _, c := range s.registered() {
		c.queue(m)
	}
}

// nickChanges tells clients about renamed nodes. The caller holds s.mu.
func (s *Server) nickChanges(changes []nickChange) {
	for _, ch := range changes {
		if ch.Old != "" {
			s.toAll(irc.Message{Prefix: fmt.Sprintf("%s!%08x@mesh", ch.Old, ch.Num), Command: "NICK", Params: []string{ch.New}})
		}
	}
}

// sendReplay hands c the messages kept while no client was attached.
func (s *Server) sendReplay(c *client) {
	for _, e := range s.replay.take() {
		m := e.Msg
		m.Params = append([]string(nil), m.Params...)
		if e.DM {
			m.Params[0] = s.selfNick()
		}
		if c.serverTime {
			m.Tags = map[string]string{"time": e.At.UTC().Format(serverTimeFormat)}
		}
		c.queue(m)
	}
}

// activeText is the --active duration as a person would write it.
func (s *Server) activeText() string {
	if a := s.cfg.Active; a%time.Hour == 0 {
		return fmt.Sprintf("%dh", a/time.Hour)
	}
	return s.cfg.Active.String()
}

// heard records hearing num on ch now and reports whether it joined ch.
// Renewed times are saved too, so a restart doesn't age out active nodes.
// The caller holds s.mu.
func (s *Server) heard(num uint32, ch int) bool {
	s.dirty = true
	return s.members.heard(num, ch, s.cfg.Now())
}

// age ends the memberships not renewed within the active window, and saves
// the memberships when they changed.
func (s *Server) age() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.up {
		return // nobody leaves because the server can't hear them
	}
	quit := map[uint32]bool{}
	for _, ch := range s.members.expire(s.cfg.Now()) {
		s.dirty = true
		mask := s.dir.mask(ch.Node)
		switch {
		case ch.Last && !quit[ch.Node]:
			quit[ch.Node] = true
			s.toAll(irc.Message{Prefix: mask, Command: "QUIT", Params: []string{"timed out"}})
		case !ch.Last:
			if name, ok := s.names[ch.Channel]; ok {
				s.toChannel(ch.Channel, irc.Message{Prefix: mask, Command: "PART",
					Params: []string{name, "not heard on this channel for " + s.activeText()}})
			}
		}
	}
	s.save()
}

// save writes the memberships if they changed. The caller holds s.mu.
func (s *Server) save() {
	if !s.dirty || s.statePath == "" {
		return
	}
	if err := s.members.save(s.statePath); err != nil {
		s.logf("saving %s: %v", s.statePath, err)
		return
	}
	s.dirty = false
}
