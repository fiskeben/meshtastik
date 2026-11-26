// Package irc holds the IRC wire format (RFC 1459/2812 messages with IRCv3
// tags) and text helpers shared by IRC servers: case mapping, formatting
// codes, CTCP and charset repair.
package irc

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// Message is one IRC protocol line.
type Message struct {
	Tags    map[string]string // IRCv3 message tags; nil when none
	Prefix  string            // source, without the leading ':'
	Command string            // upper case
	Params  []string
}

var tagUnescape = strings.NewReplacer(`\:`, ";", `\s`, " ", `\\`, `\`, `\r`, "\r", `\n`, "\n")
var tagEscape = strings.NewReplacer(";", `\:`, " ", `\s`, `\`, `\\`, "\r", `\r`, "\n", `\n`)

// Parse parses one line, without its CRLF.
func Parse(line string) (Message, error) {
	var m Message
	line = strings.TrimRight(line, "\r\n")
	rest := strings.TrimLeft(line, " ")
	if strings.HasPrefix(rest, "@") {
		var tags string
		tags, rest, _ = strings.Cut(rest[1:], " ")
		m.Tags = map[string]string{}
		for kv := range strings.SplitSeq(tags, ";") {
			if kv == "" {
				continue
			}
			k, v, _ := strings.Cut(kv, "=")
			m.Tags[k] = tagUnescape.Replace(v)
		}
		rest = strings.TrimLeft(rest, " ")
	}
	if strings.HasPrefix(rest, ":") {
		m.Prefix, rest, _ = strings.Cut(rest[1:], " ")
		rest = strings.TrimLeft(rest, " ")
	}
	m.Command, rest, _ = strings.Cut(rest, " ")
	if m.Command == "" {
		return Message{}, errors.New("no command")
	}
	m.Command = strings.ToUpper(m.Command)
	for {
		rest = strings.TrimLeft(rest, " ")
		if rest == "" {
			break
		}
		if rest[0] == ':' {
			m.Params = append(m.Params, rest[1:])
			break
		}
		var p string
		p, rest, _ = strings.Cut(rest, " ")
		m.Params = append(m.Params, p)
	}
	return m, nil
}

// String formats m as a line without CRLF.
func (m Message) String() string {
	var b strings.Builder
	if len(m.Tags) > 0 {
		keys := make([]string, 0, len(m.Tags))
		for k := range m.Tags {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b.WriteByte('@')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(';')
			}
			b.WriteString(k)
			if v := m.Tags[k]; v != "" {
				b.WriteByte('=')
				b.WriteString(tagEscape.Replace(v))
			}
		}
		b.WriteByte(' ')
	}
	if m.Prefix != "" {
		b.WriteByte(':')
		b.WriteString(m.Prefix)
		b.WriteByte(' ')
	}
	b.WriteString(m.Command)
	for i, p := range m.Params {
		b.WriteByte(' ')
		if i == len(m.Params)-1 && (p == "" || strings.Contains(p, " ") || p[0] == ':') {
			b.WriteByte(':')
		}
		b.WriteString(p)
	}
	return b.String()
}

// Fold lower-cases s with the ascii case mapping (A–Z only).
func Fold(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// StripFormatting removes mIRC formatting codes: bold, italics, underline,
// strikethrough, monospace, reverse, reset, and colours (\x03 with up to two
// digits and an optional comma with two more, \x04 with hex colours).
func StripFormatting(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; c {
		case 0x02, 0x1d, 0x1f, 0x1e, 0x11, 0x16, 0x0f:
			i++
		case 0x03:
			i = skipColour(s, i+1, 2, isDigit)
		case 0x04:
			i = skipColour(s, i+1, 6, isHex)
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// skipColour skips a colour argument starting at i: up to n characters
// matching ok, then optionally a comma and up to n more (the comma only if
// a character follows it).
func skipColour(s string, i, n int, ok func(byte) bool) int {
	j := i
	for j < len(s) && j-i < n && ok(s[j]) {
		j++
	}
	if j > i && j+1 < len(s) && s[j] == ',' && ok(s[j+1]) {
		k := j + 1
		for k < len(s) && k-(j+1) < n && ok(s[k]) {
			k++
		}
		j = k
	}
	return j
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// ParseCTCP splits a CTCP message (\x01CMD arg\x01, the closing \x01
// optional) into its command and argument.
func ParseCTCP(text string) (cmd, arg string, ok bool) {
	if len(text) < 2 || text[0] != 0x01 {
		return "", "", false
	}
	body := strings.TrimSuffix(text[1:], "\x01")
	cmd, arg, _ = strings.Cut(body, " ")
	return strings.ToUpper(cmd), arg, cmd != ""
}

// CTCP formats a CTCP message.
func CTCP(cmd, arg string) string {
	if arg == "" {
		return "\x01" + cmd + "\x01"
	}
	return "\x01" + cmd + " " + arg + "\x01"
}

// ToUTF8 returns s unchanged when it is valid UTF-8, and otherwise reads
// each byte as Latin-1, the usual charset of clients that don't send UTF-8.
func ToUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	r := make([]rune, len(s))
	for i := 0; i < len(s); i++ {
		r[i] = rune(s[i])
	}
	return string(r)
}
