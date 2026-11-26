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
