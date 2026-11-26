package ircmesh

import (
	"slices"
	"time"

	"meshtastik/internal/irc"
)

// replayPerKey is how many messages are kept per channel and per DM peer.
const replayPerKey = 100

// replayKey is a channel index (Peer 0) or a DM peer (Channel -1).
type replayKey struct {
	Channel int
	Peer    uint32
}

// replayEntry is a message that arrived while no IRC client was attached.
type replayEntry struct {
	At  time.Time
	Msg irc.Message
	DM  bool // addressed to the self nick, which may change before replay
}

// replay keeps messages for the next IRC client, as a bouncer does.
type replay struct {
	perKey int
	byKey  map[replayKey][]replayEntry
}

func newReplay(perKey int) *replay {
	return &replay{perKey: perKey, byKey: map[replayKey][]replayEntry{}}
}

func (r *replay) add(k replayKey, e replayEntry) {
	es := append(r.byKey[k], e)
	if len(es) > r.perKey {
		es = slices.Delete(es, 0, len(es)-r.perKey)
	}
	r.byKey[k] = es
}

// take returns every kept message, oldest first, and forgets them.
func (r *replay) take() []replayEntry {
	var out []replayEntry
	for _, es := range r.byKey {
		out = append(out, es...)
	}
	slices.SortStableFunc(out, func(a, b replayEntry) int { return a.At.Compare(b.At) })
	clear(r.byKey)
	return out
}

// dropChannel forgets the messages of a channel index.
func (r *replay) dropChannel(ch int) {
	delete(r.byKey, replayKey{Channel: ch})
}
