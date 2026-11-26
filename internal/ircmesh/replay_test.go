package ircmesh

import (
	"strconv"
	"testing"
	"time"

	"meshtastik/internal/irc"
)

func entry(at time.Time, text string) replayEntry {
	return replayEntry{At: at, Msg: irc.Message{Command: "PRIVMSG", Params: []string{"#c", text}}}
}

func TestReplayCapsPerKey(t *testing.T) {
	r := newReplay(100)
	t0 := time.Now()
	for i := 1; i <= 101; i++ {
		r.add(replayKey{Channel: 0}, entry(t0.Add(time.Duration(i)*time.Second), strconv.Itoa(i)))
	}
	got := r.take()
	if len(got) != 100 || got[0].Msg.Params[1] != "2" || got[99].Msg.Params[1] != "101" {
		t.Fatal(len(got), got[0].Msg)
	}
}

func TestReplayMergesByTime(t *testing.T) {
	r := newReplay(100)
	t0 := time.Now()
	r.add(replayKey{Channel: 0}, entry(t0, "a"))
	r.add(replayKey{Channel: -1, Peer: 7}, entry(t0.Add(time.Second), "b"))
	r.add(replayKey{Channel: 0}, entry(t0.Add(2*time.Second), "c"))
	got := r.take()
	if len(got) != 3 || got[0].Msg.Params[1] != "a" || got[1].Msg.Params[1] != "b" || got[2].Msg.Params[1] != "c" {
		t.Fatal(got)
	}
	if len(r.take()) != 0 {
		t.Fatal("take did not clear")
	}
}

func TestReplayDropChannel(t *testing.T) {
	r := newReplay(100)
	r.add(replayKey{Channel: 4}, entry(time.Now(), "a"))
	r.add(replayKey{Channel: -1, Peer: 7}, entry(time.Now(), "b"))
	r.dropChannel(4)
	if got := r.take(); len(got) != 1 || got[0].Msg.Params[1] != "b" {
		t.Fatal(got)
	}
}
