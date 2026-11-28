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
			if s.favorites[from] {
				s.toChannel(ch, irc.Message{Prefix: serverName, Command: "MODE", Params: []string{s.names[ch], "+v", s.dir.nick(from)}})
			}
		}
	}
	if d.GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP {
		s.text(p)
	}
}

// text records a text packet in the history and delivers it. The caller
// holds s.mu.
func (s *Server) text(p *pb.MeshPacket) {
	from, to := p.GetFrom(), p.GetTo()
	ch := int(p.GetChannel())
	var key histKey
	switch {
	case to == device.Broadcast:
		if _, ok := s.names[ch]; !ok {
			return
		}
		key = histKey{Channel: ch}
	case from == s.self: // sent by another daemon client
		key = histKey{Channel: -1, Peer: to}
	case to == s.self:
		key = histKey{Channel: -1, Peer: from}
	default:
		return
	}
	lines, action := renderText(p.GetDecoded(), s.recent)
	s.recent.add(p.GetId(), strings.Join(meshLines(p.GetDecoded().GetPayload()), " "))
	for _, l := range lines {
		if action {
			l = irc.CTCP("ACTION", l)
		}
		e := s.history.add(key, from, l, s.cfg.Now())
		for _, c := range s.registered() {
			if key.Channel < 0 || !c.parted[ch] {
				s.deliver(c, e, "")
			}
		}
	}
}

// histMessage is e as c sees it: from the sender's current nick, to the
// channel or to the other end of the DM, with the tags c asked for and the
// batch reference unless empty. ok is false when e's channel is gone. The
// caller holds s.mu.
func (s *Server) histMessage(c *client, e histEntry, batch string) (m irc.Message, ok bool) {
	target, ok := s.entryTarget(e.Key, e.From)
	if !ok {
		return irc.Message{}, false
	}
	m = irc.Message{Prefix: s.dir.mask(e.From), Command: "PRIVMSG", Params: []string{target, e.Text}}
	tags := map[string]string{}
	if c.caps[capServerTime] {
		tags["time"] = e.At.Format(serverTimeFormat)
	}
	if c.caps[capMessageTags] {
		tags["msgid"] = e.ID
	}
	if batch != "" {
		tags["batch"] = batch
	}
	if len(tags) > 0 {
		m.Tags = tags
	}
	return m, true
}

// markMessage is e's mark as c sees it: a TAGMSG reacting to e. ok is false
// when e has no mark, c doesn't take tags or e's channel is gone. The caller
// holds s.mu.
func (s *Server) markMessage(c *client, e histEntry, batch string) (m irc.Message, ok bool) {
	if e.Mark == nil || !c.caps[capMessageTags] {
		return irc.Message{}, false
	}
	target, ok := s.entryTarget(e.Key, e.Mark.From)
	if !ok {
		return irc.Message{}, false
	}
	// +reply is what the react spec pairs +draft/react with; some clients
	// still read only the older +draft/reply.
	tags := map[string]string{"+reply": e.ID, "+draft/reply": e.ID, "+draft/react": e.Mark.Emoji}
	if c.caps[capServerTime] {
		tags["time"] = e.Mark.At.Format(serverTimeFormat)
	}
	if batch != "" {
		tags["batch"] = batch
	}
	return irc.Message{Tags: tags, Prefix: s.dir.mask(e.Mark.From), Command: "TAGMSG", Params: []string{target}}, true
}

// entryTarget is where a message of k from node from goes: the channel, or
// the other end of the DM. ok is false when the channel is gone. The caller
// holds s.mu.
func (s *Server) entryTarget(k histKey, from uint32) (target string, ok bool) {
	switch {
	case k.Channel >= 0:
		target, ok = s.names[k.Channel]
		return target, ok
	case from == s.self:
		return s.dir.nick(k.Peer), true
	default:
		return s.selfNick(), true
	}
}

// deliver queues e for c, followed by its mark. The caller holds s.mu.
func (s *Server) deliver(c *client, e histEntry, batch string) {
	if m, ok := s.histMessage(c, e, batch); ok {
		c.queue(m)
	}
	if m, ok := s.markMessage(c, e, batch); ok {
		c.queue(m)
	}
}

// setMark records emoji, reacted by from, as the delivery status of line id
// of k and shows it to the clients. The caller holds s.mu.
func (s *Server) setMark(k histKey, id, emoji string, from uint32) {
	m := mark{Emoji: emoji, From: from, At: s.cfg.Now().UTC().Truncate(time.Millisecond)}
	s.history.setMark(k, id, m)
	e := histEntry{ID: id, Key: k, Mark: &m}
	for _, c := range s.registered() {
		if tm, ok := s.markMessage(c, e, ""); ok {
			c.queue(tm)
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

// sendHistory replays the history to c, oldest first, unless c fetches it
// with CHATHISTORY. The caller holds s.mu.
func (s *Server) sendHistory(c *client) {
	if c.caps[capChathistory] {
		return
	}
	for _, e := range lastN(s.history.all(), replayMax) {
		s.deliver(c, e, "")
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
		// a favourite stays in &Favorites, so it only leaves the channel
		gone := ch.Last && !s.favorites[ch.Node]
		switch {
		case gone && !quit[ch.Node]:
			quit[ch.Node] = true
			s.toAll(irc.Message{Prefix: mask, Command: "QUIT", Params: []string{"timed out"}})
		case !gone:
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
