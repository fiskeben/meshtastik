package ircmesh

import (
	"fmt"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/irc"
)

// nickChange is a node whose nick changed; Old is empty for a new node.
type nickChange struct {
	Num      uint32
	Old, New string
}

type dirEntry struct {
	user *pb.User
	base string // the nick it asks for
	nick string // the nick it has
}

// directory names every node the server has heard of. Nicks are sticky: a
// node keeps its nick until its names change, and a node whose nick is taken
// gets a suffix from its node number. The connected node never does.
type directory struct {
	self   uint32
	long   bool // nicks are long names rather than short names
	nodes  map[uint32]*dirEntry
	byNick map[string]uint32 // folded nick → node
}

func newDirectory(self uint32, long bool) *directory {
	return &directory{self: self, long: long, nodes: map[uint32]*dirEntry{}, byNick: map[string]uint32{}}
}

// set records a node's names and returns the nicks that changed as a result.
func (d *directory) set(num uint32, u *pb.User) []nickChange {
	e := d.nodes[num]
	if e == nil {
		e = &dirEntry{}
		d.nodes[num] = e
	}
	if u != nil || e.user == nil {
		e.user = u
	}
	base := baseNick(e.user, num, d.long)
	if e.nick != "" && base == e.base {
		return nil
	}
	e.base = base
	var out []nickChange
	if num == d.self {
		if holder, ok := d.byNick[irc.Fold(base)]; ok && holder != num {
			// the connected node keeps its own name; the other node moves
			h := d.nodes[holder]
			d.byNick[irc.Fold(base)] = num // reserve it before moving the holder
			old := h.nick
			h.nick = d.free(holder, h.base)
			d.byNick[irc.Fold(h.nick)] = holder
			out = append(out, nickChange{holder, old, h.nick})
		}
	}
	old := e.nick
	if old != "" {
		delete(d.byNick, irc.Fold(old))
	}
	e.nick = d.free(num, base)
	d.byNick[irc.Fold(e.nick)] = num
	if e.nick != old {
		out = append(out, nickChange{num, old, e.nick})
	}
	return out
}

// free is base if no other node holds it, else base with the last 4 hex
// digits of num, else with all 8.
func (d *directory) free(num uint32, base string) string {
	limit := nickLimit(d.long)
	for _, n := range []string{base, cut(base, limit-5) + "-" + hex4(num), cut(base, limit-9) + fmt.Sprintf("-%08x", num)} {
		if holder, ok := d.byNick[irc.Fold(n)]; !ok || holder == num {
			return n
		}
	}
	return fmt.Sprintf("n%08x", num)
}

// nick is the node's nick, naming an unknown node by its number.
func (d *directory) nick(num uint32) string {
	if e := d.nodes[num]; e != nil {
		return e.nick
	}
	d.set(num, nil)
	return d.nodes[num].nick
}

// lookup finds the node with a nick, ignoring case.
func (d *directory) lookup(nick string) (uint32, bool) {
	num, ok := d.byNick[irc.Fold(nick)]
	return num, ok
}

// user is the node's names, or nil if unknown.
func (d *directory) user(num uint32) *pb.User {
	if e := d.nodes[num]; e != nil {
		return e.user
	}
	return nil
}

// realname is the node's long name without control characters, for
// realname fields.
func (d *directory) realname(num uint32) string {
	return dropControls(d.user(num).GetLongName())
}

// mask is the node's hostmask: nick!<node number in hex>@mesh.
func (d *directory) mask(num uint32) string {
	return fmt.Sprintf("%s!%08x@mesh", d.nick(num), num)
}
