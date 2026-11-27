package ircmesh

import (
	"errors"
	"fmt"
	"strconv"
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

// isHex reports whether b is a hexadecimal digit.
func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

// resolveMentions turns Meshtastic mention tokens, @! and the 8 hex digits
// of a node number, into the node's nick and a colon, as IRC clients expect
// when someone addresses them.
func resolveMentions(text string, d *directory) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '@' && i+10 <= len(text) && text[i+1] == '!' && hexDigits(text[i+2:i+10]) &&
			(i+10 == len(text) || !isHex(text[i+10])) {
			num, _ := strconv.ParseUint(text[i+2:i+10], 16, 32)
			b.WriteString(d.nick(uint32(num)))
			b.WriteByte(':')
			i += 10
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

// hexDigits reports whether s is all hexadecimal digits.
func hexDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}

// encodeMentions turns IRC mentions, a node's nick followed by a colon and
// whitespace or the end, into Meshtastic mention tokens, @! and the 8 hex
// digits of its node number. A nick preceded by @ is left alone.
func encodeMentions(text string, d *directory) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		if !nickRune(rune(text[i])) {
			b.WriteByte(text[i])
			i++
			continue
		}
		j := i
		for j < len(text) && nickRune(rune(text[j])) {
			j++
		}
		mention := j < len(text) && text[j] == ':' &&
			(j+1 == len(text) || text[j+1] == ' ' || text[j+1] == '\t')
		if mention && (i == 0 || text[i-1] != '@') {
			if num, ok := d.lookup(text[i:j]); ok {
				fmt.Fprintf(&b, "@!%08x", num)
				i = j + 1 // past the colon
				continue
			}
		}
		b.WriteString(text[i:j])
		i = j
	}
	return b.String()
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
