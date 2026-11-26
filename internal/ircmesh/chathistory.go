package ircmesh

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"meshtastik/internal/irc"
)

// chathistoryMax is the most lines one CHATHISTORY command returns.
const chathistoryMax = 100

func init() {
	commands["CHATHISTORY"] = (*Server).cmdChathistory
	commands["TAGMSG"] = func(*Server, *client, irc.Message) {} // typing notifications and the like: nowhere to send them
}

// cmdChathistory answers the IRCv3 draft/chathistory command from the
// history: CHATHISTORY LATEST|BEFORE|AFTER|AROUND <target> <ref> <limit>,
// BETWEEN <target> <ref> <ref> <limit> and TARGETS <ts> <ts> <limit>.
func (s *Server) cmdChathistory(c *client, m irc.Message) {
	sub := strings.ToUpper(param(m, 0))
	fail := func(code string, params ...string) {
		c.queue(irc.Message{Prefix: serverName, Command: "FAIL", Params: append([]string{"CHATHISTORY", code}, params...)})
	}
	refs := 1
	switch sub {
	case "LATEST", "BEFORE", "AFTER", "AROUND":
	case "BETWEEN", "TARGETS":
		refs = 2
	case "":
		fail("INVALID_PARAMS", "*", "Insufficient parameters")
		return
	default:
		fail("INVALID_PARAMS", sub, "Unknown command")
		return
	}
	args := m.Params[1:]
	if sub == "TARGETS" {
		args = append([]string{""}, args...) // no target
	}
	if len(args) < 2+refs {
		fail("INVALID_PARAMS", sub, "Insufficient parameters")
		return
	}
	limitArg := args[1+refs]
	limit, err := strconv.Atoi(limitArg)
	if err != nil || limit <= 0 {
		fail("INVALID_PARAMS", sub, limitArg, "Invalid limit")
		return
	}
	limit = min(limit, chathistoryMax)

	if sub == "TARGETS" {
		var ts [2]time.Time
		for i, a := range args[1:3] {
			v, ok := strings.CutPrefix(a, "timestamp=")
			t, err := time.Parse(time.RFC3339Nano, v)
			if !ok || err != nil {
				fail("INVALID_PARAMS", sub, a, "Invalid timestamp")
				return
			}
			ts[i] = t
		}
		s.chathistoryTargets(c, ts[0], ts[1], limit)
		return
	}

	key, name, ok := s.histTarget(args[0])
	if !ok {
		fail("INVALID_TARGET", sub, args[0], "Messages could not be retrieved")
		return
	}
	var ts [2]time.Time
	known := true
	for i, a := range args[1 : 1+refs] {
		if sub == "LATEST" && a == "*" {
			continue // the zero time: everything
		}
		t, found, valid := s.refTime(a)
		if !valid {
			fail("INVALID_PARAMS", sub, a, "Invalid message reference")
			return
		}
		ts[i], known = t, known && found
	}
	var picked []histEntry
	if known { // an unknown msgid selects nothing
		es := s.history.entries(key)
		switch sub {
		case "LATEST":
			picked = pickLatest(es, ts[0], limit)
		case "BEFORE":
			picked = pickBefore(es, ts[0], limit)
		case "AFTER":
			picked = pickAfter(es, ts[0], limit)
		case "AROUND":
			picked = pickAround(es, ts[0], limit)
		case "BETWEEN":
			picked = pickBetween(es, ts[0], ts[1], limit)
		}
	}
	s.batch(c, []string{"chathistory", name}, func(ref string) {
		for _, e := range picked {
			s.deliver(c, e, ref)
		}
	})
}

// histTarget is the history key and canonical name of a CHATHISTORY
// target: a channel, or a node other than the user's own.
func (s *Server) histTarget(target string) (histKey, string, bool) {
	if strings.HasPrefix(target, "#") {
		i, ok := s.channelIndex(target)
		return histKey{Channel: i}, s.names[i], ok
	}
	num, ok := s.dir.lookup(target)
	if !ok || num == s.self {
		return histKey{}, "", false
	}
	return histKey{Channel: -1, Peer: num}, s.dir.nick(num), true
}

// refTime is the time a message reference (timestamp=… or msgid=…) names.
// found is false for a msgid the history doesn't hold; valid is false for
// anything else than the two.
func (s *Server) refTime(ref string) (t time.Time, found, valid bool) {
	if v, ok := strings.CutPrefix(ref, "timestamp="); ok {
		t, err := time.Parse(time.RFC3339Nano, v)
		return t, true, err == nil
	}
	if id, ok := strings.CutPrefix(ref, "msgid="); ok && id != "" {
		e, found := s.history.find(id)
		return e.At, found, true
	}
	return time.Time{}, false, false
}

// chathistoryTargets lists the channels and DM peers whose latest line is
// between a and b, oldest first.
func (s *Server) chathistoryTargets(c *client, a, b time.Time, limit int) {
	type target struct {
		name string
		at   time.Time
	}
	lo, hi := a, b
	if lo.After(hi) {
		lo, hi = hi, lo
	}
	var ts []target
	for _, k := range s.history.keys() {
		es := s.history.entries(k)
		last := es[len(es)-1]
		if !last.At.After(lo) || !last.At.Before(hi) {
			continue
		}
		var name string
		if k.Channel >= 0 {
			n, ok := s.names[k.Channel]
			if !ok {
				continue
			}
			name = n
		} else {
			name = s.dir.nick(k.Peer)
		}
		ts = append(ts, target{name, last.At})
	}
	slices.SortFunc(ts, func(x, y target) int { return x.at.Compare(y.at) })
	if a.After(b) {
		ts = ts[max(0, len(ts)-limit):]
	} else {
		ts = ts[:min(limit, len(ts))]
	}
	s.batch(c, []string{"draft/chathistory-targets"}, func(ref string) {
		for _, t := range ts {
			m := irc.Message{Prefix: serverName, Command: "CHATHISTORY",
				Params: []string{"TARGETS", t.name, t.at.Format(serverTimeFormat)}}
			if ref != "" {
				m.Tags = map[string]string{"batch": ref}
			}
			c.queue(m)
		}
	})
}

// batch wraps what body queues for c in a batch of the given type and
// parameters, when c has the batch capability. body gets the batch
// reference, or "" without one. The caller holds s.mu.
func (s *Server) batch(c *client, typeAndParams []string, body func(ref string)) {
	if !c.caps[capBatch] {
		body("")
		return
	}
	s.batches++
	ref := strconv.FormatUint(s.batches, 36)
	c.queue(irc.Message{Prefix: serverName, Command: "BATCH", Params: append([]string{"+" + ref}, typeAndParams...)})
	body(ref)
	c.queue(irc.Message{Prefix: serverName, Command: "BATCH", Params: []string{"-" + ref}})
}
