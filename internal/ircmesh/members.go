package ircmesh

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
)

// counts reports the channel a packet shows its sender on. Only packets the
// node decoded with a channel key say that (rx_time is not required: a
// node without a clock leaves it 0):
// undecoded packets carry a channel hash, PKI and other direct messages say
// nothing about the channels a node listens on.
func counts(p *pb.MeshPacket, self uint32) (ch int, ok bool) {
	if p.GetDecoded() == nil || p.GetPkiEncrypted() || p.GetFrom() == self || p.GetTo() != device.Broadcast || p.GetChannel() >= 8 {
		return 0, false
	}
	return int(p.GetChannel()), true
}

type memberKey struct {
	node uint32
	ch   int
}

// membership is which nodes have been heard on which channels lately. Each
// (node, channel) pair ages out on its own after active.
type membership struct {
	active  time.Duration
	heardAt map[memberKey]time.Time
}

// change is a membership that ended; Last means the node has none left.
type change struct {
	Node    uint32
	Channel int
	Last    bool
}

func newMembership(active time.Duration) *membership {
	return &membership{active: active, heardAt: map[memberKey]time.Time{}}
}

// heard records hearing num on ch at and reports whether that made it a
// member of ch.
func (m *membership) heard(num uint32, ch int, at time.Time) (joined bool) {
	k := memberKey{num, ch}
	old, ok := m.heardAt[k]
	if !ok || at.After(old) {
		m.heardAt[k] = at
	}
	return !ok
}

// expire ends the memberships not renewed within active of now.
func (m *membership) expire(now time.Time) []change {
	var out []change
	for k, at := range m.heardAt {
		if now.Sub(at) > m.active {
			delete(m.heardAt, k)
			out = append(out, change{Node: k.node, Channel: k.ch})
		}
	}
	slices.SortFunc(out, func(a, b change) int {
		return cmp.Or(cmp.Compare(a.Node, b.Node), cmp.Compare(a.Channel, b.Channel))
	})
	for i := range out {
		out[i].Last = len(m.channels(out[i].Node)) == 0
	}
	return out
}

// members is the nodes in ch, sorted.
func (m *membership) members(ch int) []uint32 {
	var out []uint32
	for k := range m.heardAt {
		if k.ch == ch {
			out = append(out, k.node)
		}
	}
	slices.Sort(out)
	return out
}

// channels is the channels num is a member of, sorted.
func (m *membership) channels(num uint32) []int {
	var out []int
	for k := range m.heardAt {
		if k.node == num {
			out = append(out, k.ch)
		}
	}
	slices.Sort(out)
	return out
}

// lastHeard is when num was last heard on any channel (zero if never).
func (m *membership) lastHeard(num uint32) time.Time {
	var last time.Time
	for k, at := range m.heardAt {
		if k.node == num && at.After(last) {
			last = at
		}
	}
	return last
}

// seed adds the one membership the node DB records for each node: the
// channel it was last heard on, at that time, if within active of now.
func (m *membership) seed(nodes map[uint32]*pb.NodeInfo, self uint32, now time.Time) {
	for _, num := range slices.Sorted(maps.Keys(nodes)) {
		n := nodes[num]
		if num == self || n.GetLastHeard() == 0 || n.GetChannel() >= 8 {
			continue
		}
		if at := time.Unix(int64(n.GetLastHeard()), 0); now.Sub(at) <= m.active {
			m.heard(num, int(n.GetChannel()), at)
		}
	}
}

// shift moves every membership's time forward by d, so time the server
// couldn't hear doesn't count against anyone.
func (m *membership) shift(d time.Duration) {
	for k, at := range m.heardAt {
		m.heardAt[k] = at.Add(d)
	}
}

// dropChannel ends every membership of ch, without reporting them.
func (m *membership) dropChannel(ch int) {
	for k := range m.heardAt {
		if k.ch == ch {
			delete(m.heardAt, k)
		}
	}
}

type membersFile struct {
	Version int            `json:"version"`
	Members []memberRecord `json:"members"`
}

type memberRecord struct {
	Node    uint32    `json:"node"`
	Channel int       `json:"channel"`
	Heard   time.Time `json:"heard"`
}

// load merges the memberships saved at path, keeping the newer time of a
// pair known to both. A missing file is no memberships.
func (m *membership) load(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f membersFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	for _, r := range f.Members {
		if r.Channel >= 0 && r.Channel < 8 {
			m.heard(r.Node, r.Channel, r.Heard)
		}
	}
	return nil
}

// save writes the memberships to path, replacing it atomically.
func (m *membership) save(path string) error {
	f := membersFile{Version: 1, Members: []memberRecord{}}
	for k, at := range m.heardAt {
		f.Members = append(f.Members, memberRecord{Node: k.node, Channel: k.ch, Heard: at.UTC()})
	}
	slices.SortFunc(f.Members, func(a, b memberRecord) int {
		return cmp.Or(cmp.Compare(a.Node, b.Node), cmp.Compare(a.Channel, b.Channel))
	})
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".members-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path) // CreateTemp makes it 0600
}
