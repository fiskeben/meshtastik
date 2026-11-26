package ircmesh

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

const (
	maxTextBytes = 200 // text per packet, as the apps allow
	maxParts     = 3   // packets per IRC message
	quoteRunes   = 30  // how much of a message a reaction or reply quotes
)

var errTooLong = errors.New("message too long for the mesh")

// splitText splits s into pieces of at most max bytes, at the last space
// that fits or else between two runes, refusing to make more than parts.
func splitText(s string, max, parts int) ([]string, error) {
	n := len(s)
	s = strings.TrimSpace(s)
	var out []string
	for len(s) > max {
		i := max
		for i > 0 && !utf8.RuneStart(s[i]) {
			i--
		}
		if sp := strings.LastIndexByte(s[:i+1], ' '); sp > 0 {
			i = sp
		}
		out = append(out, strings.TrimSpace(s[:i]))
		s = strings.TrimSpace(s[i:])
	}
	if s != "" {
		out = append(out, s)
	}
	if len(out) > parts {
		return nil, fmt.Errorf("%w (%d bytes, at most %d packets of %d)", errTooLong, n, parts, max)
	}
	return out, nil
}

// dropControls removes control characters (C0 and DEL), which could break
// or disguise IRC lines.
func dropControls(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// meshLines is a text payload as valid UTF-8 lines without control
// characters: \x01 would make it CTCP, CR and NUL could break IRC lines.
// Blank lines are dropped.
func meshLines(payload []byte) []string {
	var out []string
	for l := range strings.SplitSeq(strings.ToValidUTF8(string(payload), "\uFFFD"), "\n") {
		if l = dropControls(l); strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// quote is the start of a message for a reaction or reply.
func quote(s string) string {
	s = dropControls(s)
	r := []rune(s)
	if len(r) <= quoteRunes {
		return s
	}
	return string(r[:quoteRunes]) + "…"
}

// recent remembers the text of the last messages by packet id, for quoting
// in reactions and replies.
type recent struct {
	ids  []uint32 // ring, oldest at next
	next int
	text map[uint32]string
}

func newRecent(n int) *recent {
	return &recent{ids: make([]uint32, n), text: map[uint32]string{}}
}

func (r *recent) add(id uint32, text string) {
	if id == 0 {
		return
	}
	if old := r.ids[r.next]; old != 0 {
		delete(r.text, old)
	}
	r.ids[r.next] = id
	r.next = (r.next + 1) % len(r.ids)
	r.text[id] = text
}

func (r *recent) get(id uint32) (string, bool) {
	s, ok := r.text[id]
	return s, ok
}

// renderText turns a text packet into IRC message bodies, one per line of
// text; action means they are CTCP ACTIONs (a reaction).
func renderText(d *pb.Data, r *recent) (lines []string, action bool) {
	lines = meshLines(d.GetPayload())
	reply := d.GetReplyId()
	if reply == 0 {
		return lines, false
	}
	orig, known := r.get(reply)
	if d.GetEmoji() != 0 {
		target := "a message"
		if known {
			target = `"` + quote(orig) + `"`
		}
		return []string{"reacted " + strings.Join(lines, " ") + " to " + target}, true
	}
	if known && len(lines) > 0 {
		lines[0] = `↪ "` + quote(orig) + `" ` + lines[0]
	}
	return lines, false
}
