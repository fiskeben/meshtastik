package ircmesh

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

func TestSplitText(t *testing.T) {
	if parts, err := splitText(strings.Repeat("a", 150), maxTextBytes, maxParts); err != nil || len(parts) != 1 {
		t.Fatal(parts, err)
	}
	words := strings.Repeat("word ", 90) // 450 bytes
	parts, err := splitText(words, maxTextBytes, maxParts)
	if err != nil || len(parts) != 3 {
		t.Fatal(len(parts), err)
	}
	for _, p := range parts {
		if len(p) > maxTextBytes || strings.HasPrefix(p, " ") || strings.HasSuffix(p, " ") {
			t.Fatalf("part %q", p)
		}
	}
	if strings.Join(parts, " ") != strings.TrimSpace(words) {
		t.Fatal("words lost")
	}
	accents := strings.Repeat("é", 125) // 250 bytes, no spaces
	parts, err = splitText(accents, maxTextBytes, maxParts)
	if err != nil || len(parts) != 2 || !utf8.ValidString(parts[0]) || !utf8.ValidString(parts[1]) || parts[0]+parts[1] != accents {
		t.Fatal(parts, err)
	}
	_, err = splitText(strings.Repeat("x ", 350), maxTextBytes, maxParts)
	if !errors.Is(err, errTooLong) || !strings.Contains(err.Error(), "700 bytes") {
		t.Fatal(err)
	}
}

// mentionDir has AS02, Ricco (self) and Riccos_WisMesh (a long name with
// spaces, as the nicks use).
func mentionDir() *directory {
	d := newDirectory(selfNum, false)
	d.set(arveNum, &pb.User{ShortName: "AS02", LongName: "Arve Tak"})
	d.set(selfNum, &pb.User{ShortName: "🥸", LongName: "Ricco"})
	d.set(0x2e3ab140, &pb.User{ShortName: "🛰", LongName: "Riccos WisMesh"})
	return d
}

func TestResolveMentions(t *testing.T) {
	d := mentionDir()
	for _, tc := range []struct{ in, want string }{
		{"@!1bf0411d hi", "AS02: hi"},
		{"hey @!1bf0411d, ok", "hey AS02:, ok"},
		{"@!1BF0411D hi", "AS02: hi"},
		{"@!2e3ab140 hi", "Riccos_WisMesh: hi"},
		{"@!ab2c9b0a hi", "Ricco: hi"},
		{"@!1bf0411d", "AS02:"},
		{"@!deadbeef hi", "nbeef: hi"}, // unknown nodes get their fallback nick
		{"@!1bf0411da hi", "@!1bf0411da hi"},
		{"@!1bf0411 hi", "@!1bf0411 hi"},
		{"no mentions", "no mentions"},
		{"😀 @!1bf0411d", "😀 AS02:"},
	} {
		if got := resolveMentions(tc.in, d); got != tc.want {
			t.Errorf("resolveMentions(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEncodeMentions(t *testing.T) {
	d := mentionDir()
	for _, tc := range []struct{ in, want string }{
		{"AS02: hi", "@!1bf0411d hi"},
		{"hey as02: hi", "hey @!1bf0411d hi"},
		{"hey, (AS02: hi)", "hey, (@!1bf0411d hi)"},
		{"AS02: hi Ricco: there", "@!1bf0411d hi @!ab2c9b0a there"},
		{"Riccos_WisMesh: hi", "@!2e3ab140 hi"},
		{"AS02:\thi", "@!1bf0411d\thi"},
		{"AS02:", "@!1bf0411d"},
		{"AS02:hi", "AS02:hi"},
		{"nobody: hi", "nobody: hi"},
		{"@AS02: hi", "@AS02: hi"},
		{"http://example.com", "http://example.com"},
	} {
		if got := encodeMentions(tc.in, d); got != tc.want {
			t.Errorf("encodeMentions(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQuote(t *testing.T) {
	if got := quote(strings.Repeat("ø", 40)); got != strings.Repeat("ø", 30)+"…" {
		t.Fatal(got)
	}
	if got := quote("short text"); got != "short text" {
		t.Fatal(got)
	}
}

func TestRenderSplitsLines(t *testing.T) {
	lines, action := renderText(&pb.Data{Payload: []byte("a\r\n\nb\x00c\xff")}, newRecent(10))
	if action || !slices.Equal(lines, []string{"a", "bc�"}) {
		t.Fatalf("%q %v", lines, action)
	}
}

const hut = "see you at the hut tomorrow morning ok"

func TestRenderReaction(t *testing.T) {
	r := newRecent(10)
	r.add(7, hut)
	lines, action := renderText(&pb.Data{Payload: []byte("👍"), Emoji: 1, ReplyId: 7}, r)
	if !action || !slices.Equal(lines, []string{`reacted 👍 to "see you at the hut tomorrow mo…"`}) {
		t.Fatalf("%q %v", lines, action)
	}
	lines, _ = renderText(&pb.Data{Payload: []byte("👍"), Emoji: 1, ReplyId: 8}, r)
	if !slices.Equal(lines, []string{"reacted 👍 to a message"}) {
		t.Fatalf("%q", lines)
	}
}

func TestRenderReply(t *testing.T) {
	r := newRecent(10)
	r.add(7, hut)
	lines, action := renderText(&pb.Data{Payload: []byte("yes"), ReplyId: 7}, r)
	if action || !slices.Equal(lines, []string{`↪ "see you at the hut tomorrow mo…" yes`}) {
		t.Fatalf("%q %v", lines, action)
	}
}

func TestRecentForgetsOldest(t *testing.T) {
	r := newRecent(2)
	r.add(1, "a")
	r.add(2, "b")
	r.add(3, "c")
	if _, ok := r.get(1); ok {
		t.Fatal("kept the oldest")
	}
	if s, ok := r.get(3); !ok || s != "c" {
		t.Fatal(s, ok)
	}
}
