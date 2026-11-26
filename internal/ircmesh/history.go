package ircmesh

import (
	"maps"
	"slices"
	"sort"
	"strconv"
	"time"
)

const (
	// historyPerKey is the default of Config.History.
	historyPerKey = 100
	// replayMax bounds what a client gets replayed on connect, well below
	// sendQueue.
	replayMax = 2000
)

// histKey is a channel index (Peer 0) or a DM peer (Channel -1).
type histKey struct {
	Channel int
	Peer    uint32
}

// histEntry is one line of text sent on the mesh or from IRC. It holds the
// sender's node number rather than its nick, so it reads with the nicks
// nodes have when it is replayed.
type histEntry struct {
	ID   string    // the msgid tag
	At   time.Time // UTC, whole milliseconds, unique and increasing
	Key  histKey
	From uint32
	Text string // as sent to IRC clients, a CTCP ACTION for reactions
}

// history keeps the last perKey lines of every channel and DM peer, for
// clients that connect later: replayed to those without draft/chathistory,
// fetched with CHATHISTORY by the others.
type history struct {
	perKey int // 0 or less: keep nothing
	prefix string
	seq    uint64
	last   time.Time
	byKey  map[histKey][]histEntry
}

// newHistory keeps perKey lines per key, naming them prefix plus a counter.
func newHistory(perKey int, prefix string) *history {
	return &history{perKey: perKey, prefix: prefix, byKey: map[histKey][]histEntry{}}
}

// add records a line received at at and returns it. Its time is at to the
// millisecond the server-time tag has, moved past the last line's so that
// a timestamp names one line.
func (h *history) add(k histKey, from uint32, text string, at time.Time) histEntry {
	at = at.UTC().Truncate(time.Millisecond)
	if !at.After(h.last) {
		at = h.last.Add(time.Millisecond)
	}
	h.last = at
	h.seq++
	e := histEntry{ID: h.prefix + strconv.FormatUint(h.seq, 10), At: at, Key: k, From: from, Text: text}
	if h.perKey <= 0 {
		return e
	}
	es := append(h.byKey[k], e)
	if len(es) > h.perKey {
		es = slices.Delete(es, 0, len(es)-h.perKey)
	}
	h.byKey[k] = es
	return e
}

// entries is the lines of k, oldest first.
func (h *history) entries(k histKey) []histEntry { return h.byKey[k] }

// keys is every key with lines.
func (h *history) keys() []histKey { return slices.Collect(maps.Keys(h.byKey)) }

// all is every line, oldest first.
func (h *history) all() []histEntry {
	out := []histEntry{}
	for _, es := range h.byKey {
		out = append(out, es...)
	}
	slices.SortFunc(out, func(a, b histEntry) int { return a.At.Compare(b.At) })
	return out
}

// find looks a line up by its msgid.
func (h *history) find(id string) (histEntry, bool) {
	for _, es := range h.byKey {
		for _, e := range es {
			if e.ID == id {
				return e, true
			}
		}
	}
	return histEntry{}, false
}

// dropChannel forgets the lines of a channel index.
func (h *history) dropChannel(ch int) { delete(h.byKey, histKey{Channel: ch}) }

// The pick functions select from es (oldest first) as the CHATHISTORY
// subcommands do, oldest first. References are exclusive.

// idxAfter is the index of the first line after t.
func idxAfter(es []histEntry, t time.Time) int {
	return sort.Search(len(es), func(i int) bool { return es[i].At.After(t) })
}

// idxFrom is the index of the first line at or after t.
func idxFrom(es []histEntry, t time.Time) int {
	return sort.Search(len(es), func(i int) bool { return !es[i].At.Before(t) })
}

func lastN(es []histEntry, n int) []histEntry  { return es[max(0, len(es)-n):] }
func firstN(es []histEntry, n int) []histEntry { return es[:min(n, len(es))] }

// pickLatest is the newest limit lines after t (the zero time: all lines).
func pickLatest(es []histEntry, t time.Time, limit int) []histEntry {
	return lastN(es[idxAfter(es, t):], limit)
}

// pickBefore is the limit lines just before t.
func pickBefore(es []histEntry, t time.Time, limit int) []histEntry {
	return lastN(es[:idxFrom(es, t)], limit)
}

// pickAfter is the limit lines just after t.
func pickAfter(es []histEntry, t time.Time, limit int) []histEntry {
	return firstN(es[idxAfter(es, t):], limit)
}

// pickAround is limit lines centred on t.
func pickAround(es []histEntry, t time.Time, limit int) []histEntry {
	start := idxFrom(es, t) - limit/2
	start = max(0, min(start, len(es)-limit))
	return firstN(es[start:], limit)
}

// pickBetween is the limit lines between a and b nearest a, which may be
// after b.
func pickBetween(es []histEntry, a, b time.Time, limit int) []histEntry {
	if a.After(b) {
		return lastN(es[idxAfter(es, b):idxFrom(es, a)], limit)
	}
	return firstN(es[idxAfter(es, a):idxFrom(es, b)], limit)
}
