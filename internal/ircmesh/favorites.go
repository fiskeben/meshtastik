package ircmesh

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"meshtastik/internal/device"
	"meshtastik/internal/irc"
)

// Favourite nodes (is_favorite in the node DB) are voiced (+v) in every
// channel they are in, and are the members of &Favorites, a channel that
// exists only in ircmesh. /mode +v and -v, and /invite and /kick on
// &Favorites, change the flag on the node.

const (
	// favChan is the channel index of &Favorites, past the node's own
	// channels (0-7).
	favChan = 8
	favName = "&Favorites"

	editQueueLen = 16          // favourite changes that may wait for the node
	editTimeout  = time.Minute // for one change, which re-reads the node list
)

func init() {
	commands["INVITE"] = (*Server).cmdInvite
	commands["KICK"] = (*Server).cmdKick
}

// channels is the channel indexes clients join: the node's enabled
// channels, ascending, then &Favorites. The caller holds s.mu.
func (s *Server) channels() []int { return append(s.indexes(), favChan) }

// chanName is channel index i's name. The caller holds s.mu.
func (s *Server) chanName(i int) string {
	if i == favChan {
		return favName
	}
	return s.names[i]
}

// findChannel finds a channel clients can join by name, ignoring case: one
// of the node's or &Favorites. The caller holds s.mu.
func (s *Server) findChannel(name string) (int, bool) {
	if irc.Fold(name) == irc.Fold(favName) {
		return favChan, true
	}
	return s.channelIndex(name)
}

// isChannel reports whether target names a channel rather than a nick.
func isChannel(target string) bool {
	return strings.HasPrefix(target, "#") || strings.HasPrefix(target, "&")
}

// chanMembers is the nodes in channel index i, sorted: those heard there
// lately, or the favourites for &Favorites. The caller holds s.mu.
func (s *Server) chanMembers(i int) []uint32 {
	if i == favChan {
		return slices.Sorted(maps.Keys(s.favorites))
	}
	return s.members.members(i)
}

// nodeChannels is the channel indexes num is in, ascending. The caller
// holds s.mu.
func (s *Server) nodeChannels(num uint32) []int {
	chans := s.members.channels(num)
	if s.favorites[num] {
		chans = append(chans, favChan)
	}
	return chans
}

// chanTopic is channel index i's topic. The caller holds s.mu.
func (s *Server) chanTopic(i int) string {
	if i == favChan {
		return s.favTopic()
	}
	return channelTopic(s.chans[i], s.lora)
}

// favTopic is &Favorites' topic: how many favourites there are and how many
// of them were heard lately. The caller holds s.mu.
func (s *Server) favTopic() string {
	heard := 0
	for num := range s.favorites {
		if t := s.lastHeard(num); !t.IsZero() && s.cfg.Now().Sub(t) <= s.cfg.Active {
			heard++
		}
	}
	return fmt.Sprintf("favourite nodes · %d on the node · %d heard in the last %s · /invite or /kick to change",
		len(s.favorites), heard, s.activeText())
}

// chanModes is channel index i's modes, for 324: &Favorites is moderated,
// as nobody can talk there.
func chanModes(i int) string {
	if i == favChan {
		return "+mnt"
	}
	return "+nt"
}

// prefix is num's membership prefix in any channel it is in: + for a
// favourite. The caller holds s.mu.
func (s *Server) prefix(num uint32) string {
	if s.favorites[num] {
		return "+"
	}
	return ""
}

// channelMode handles MODE on channel index i with modes: a ban list query,
// or +v and -v, which favourite a node or stop it being one. Other modes
// can't be changed. The caller holds s.mu.
func (s *Server) channelMode(c *client, i int, modes string, args []string) {
	name := s.chanName(i)
	if strings.Trim(modes, "+") == "b" {
		c.queue(s.numeric(c, "368", name, "End of channel ban list"))
		return
	}
	type voice struct {
		add  bool
		nick string
	}
	var changes []voice
	add := true
	for _, r := range modes {
		switch r {
		case '+', '-':
			add = r == '+'
		case 'v':
			if len(args) == 0 {
				c.queue(s.numeric(c, "461", "MODE", "Not enough parameters"))
				return
			}
			changes = append(changes, voice{add, args[0]})
			args = args[1:]
		default:
			if i == favChan {
				c.queue(s.numeric(c, "482", name, "Only +v and -v (favourite nodes) can be changed here"))
			} else {
				c.queue(s.numeric(c, "482", name, "Channel modes follow the node and can't be changed"))
			}
			return
		}
	}
	for _, v := range changes {
		num, ok := s.dir.lookup(v.nick)
		if !ok {
			c.queue(s.numeric(c, "401", v.nick, "No such nick/channel"))
			continue
		}
		op := device.Unfavorite
		if v.add {
			op = device.Favorite
		}
		if v.add != s.favorites[num] {
			s.editFavorite(c, op, num)
		}
	}
}

// cmdInvite favourites a node: INVITE <nick> &Favorites.
func (s *Server) cmdInvite(c *client, m irc.Message) {
	if !s.needParams(c, m, 2) {
		return
	}
	nick, target := m.Params[0], m.Params[1]
	i, ok := s.findChannel(target)
	switch {
	case !ok:
		c.queue(s.numeric(c, "403", target, "No such channel"))
		return
	case i != favChan:
		c.queue(s.numeric(c, "482", s.chanName(i), "Nodes join the node's channels by being heard there"))
		return
	}
	num, ok := s.dir.lookup(nick)
	switch {
	case !ok:
		c.queue(s.numeric(c, "401", nick, "No such nick/channel"))
	case s.favorites[num]:
		c.queue(s.numeric(c, "443", s.dir.nick(num), favName, "is already a favourite"))
	default:
		s.editFavorite(c, device.Favorite, num)
	}
}

// cmdKick stops nodes being favourites: KICK &Favorites <nick>[,<nick>].
func (s *Server) cmdKick(c *client, m irc.Message) {
	if !s.needParams(c, m, 2) {
		return
	}
	i, ok := s.findChannel(m.Params[0])
	switch {
	case !ok:
		c.queue(s.numeric(c, "403", m.Params[0], "No such channel"))
		return
	case i != favChan:
		c.queue(s.numeric(c, "482", s.chanName(i), "Nodes leave the node's channels when they aren't heard there"))
		return
	}
	for nick := range strings.SplitSeq(m.Params[1], ",") {
		num, ok := s.dir.lookup(nick)
		switch {
		case !ok:
			c.queue(s.numeric(c, "401", nick, "No such nick/channel"))
		case !s.favorites[num]:
			c.queue(s.numeric(c, "441", s.dir.nick(num), favName, "isn't a favourite"))
		default:
			s.editFavorite(c, device.Unfavorite, num)
		}
	}
}

// nodeEdit is a favourite change on its way to the node.
type nodeEdit struct {
	c   *client // who asked, told if it fails
	op  device.NodeOp
	num uint32
}

// describe says what e does, for notices.
func (e nodeEdit) describe(nick string) string {
	if e.op == device.Favorite {
		return "make " + nick + " a favourite"
	}
	return "remove " + nick + " from the favourites"
}

// editFavorite queues op (Favorite or Unfavorite) on num for the node. The
// caller holds s.mu.
func (s *Server) editFavorite(c *client, op device.NodeOp, num uint32) {
	e := nodeEdit{c: c, op: op, num: num}
	fail := func(why string) {
		c.queue(s.notice(s.selfNick(), fmt.Sprintf("can't %s: %s", e.describe(s.dir.nick(num)), why)))
	}
	switch {
	case num == s.self:
		fail("it's your own node")
		return
	case s.editQ == nil || !s.up:
		fail("the mesh link is down")
		return
	}
	select {
	case s.editQ <- e:
	default:
		fail("too many changes are waiting for the node")
	}
}

// editor makes the favourite changes on q one at a time, until ctx ends.
// The admin call goes to the node, so s.mu isn't held during it.
func (s *Server) editor(ctx context.Context, m Mesh, q <-chan nodeEdit) {
	for {
		var e nodeEdit
		select {
		case <-ctx.Done():
			return
		case e = <-q:
		}
		ectx, cancel := context.WithTimeout(ctx, editTimeout)
		err := m.EditNodeDB(ectx, e.op, e.num)
		cancel()
		if ctx.Err() != nil {
			s.mu.Lock()
			e.c.queue(s.notice(s.selfNick(), fmt.Sprintf("the mesh link went down; check whether it did %s", e.describe(s.dir.nick(e.num)))))
			s.mu.Unlock()
			return
		}
		st := m.State()
		s.mu.Lock()
		if err != nil {
			e.c.queue(s.notice(s.selfNick(), fmt.Sprintf("can't %s: %v", e.describe(s.dir.nick(e.num)), err)))
		}
		s.syncState(st, s.dir.mask(s.self))
		s.mu.Unlock()
	}
}

// dropEdits refuses the favourite changes still waiting for the node. The
// caller holds s.mu.
func (s *Server) dropEdits(q chan nodeEdit) {
	for {
		select {
		case e := <-q:
			e.c.queue(s.notice(s.selfNick(), fmt.Sprintf("can't %s: the mesh link is down", e.describe(s.dir.nick(e.num)))))
		default:
			return
		}
	}
}

// favoriteChanges tells clients which nodes became favourites, or stopped
// being ones, since old: voice in the channels they are in, and joining or
// leaving &Favorites. by is the mask of whoever changed them, or "" when
// the change was seen on the node. The caller holds s.mu.
func (s *Server) favoriteChanges(old map[uint32]bool, by string) {
	src := cmp.Or(by, serverName)
	var added, removed []uint32
	for _, num := range slices.Sorted(maps.Keys(s.favorites)) {
		if !old[num] {
			added = append(added, num)
		}
	}
	for _, num := range slices.Sorted(maps.Keys(old)) {
		if !s.favorites[num] {
			removed = append(removed, num)
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return
	}
	voice := func(num uint32, mode string) {
		for _, i := range s.members.channels(num) {
			if name, ok := s.names[i]; ok {
				s.toChannel(i, irc.Message{Prefix: src, Command: "MODE", Params: []string{name, mode, s.dir.nick(num)}})
			}
		}
	}
	for _, num := range removed {
		const why = "no longer a favourite"
		if by != "" {
			s.toChannel(favChan, irc.Message{Prefix: by, Command: "KICK", Params: []string{favName, s.dir.nick(num), why}})
		} else {
			s.toChannel(favChan, irc.Message{Prefix: s.dir.mask(num), Command: "PART", Params: []string{favName, why}})
		}
		voice(num, "-v")
	}
	for _, num := range added {
		s.toChannel(favChan, irc.Message{Prefix: s.dir.mask(num), Command: "JOIN", Params: []string{favName}})
		s.toChannel(favChan, irc.Message{Prefix: src, Command: "MODE", Params: []string{favName, "+v", s.dir.nick(num)}})
		voice(num, "+v")
	}
	s.toChannel(favChan, irc.Message{Prefix: serverName, Command: "TOPIC", Params: []string{favName, s.favTopic()}})
}
