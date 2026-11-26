package ircmesh

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func textsOf(es []histEntry) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Text)
	}
	return out
}

func TestHistoryCapsPerKey(t *testing.T) {
	h := newHistory(100, "x")
	for i := 1; i <= 101; i++ {
		h.add(histKey{Channel: 0}, 7, strconv.Itoa(i), t0.Add(time.Duration(i)*time.Second))
	}
	got := h.entries(histKey{Channel: 0})
	if len(got) != 100 || got[0].Text != "2" || got[99].Text != "101" {
		t.Fatal(len(got), got[0])
	}
}

func TestHistoryKeepsEverythingAndForgetsNothingOnRead(t *testing.T) {
	h := newHistory(100, "x")
	h.add(histKey{Channel: 0}, 7, "a", t0)
	h.all()
	if got := textsOf(h.all()); !slices.Equal(got, []string{"a"}) {
		t.Fatal(got)
	}
}

func TestHistoryTimesAreUniqueAndIncreasing(t *testing.T) {
	h := newHistory(100, "x")
	a := h.add(histKey{Channel: 0}, 7, "a", t0.Add(1500*time.Microsecond))
	b := h.add(histKey{Channel: -1, Peer: 9}, 7, "b", t0) // the clock went back
	if !a.At.Equal(t0.Add(time.Millisecond)) || !b.At.Equal(a.At.Add(time.Millisecond)) {
		t.Fatal(a.At, b.At)
	}
	if a.ID == b.ID || a.ID == "" {
		t.Fatal(a.ID, b.ID)
	}
	if a.At.Location() != time.UTC {
		t.Fatal(a.At.Location())
	}
}

func TestHistoryAllMergesByTime(t *testing.T) {
	h := newHistory(100, "x")
	h.add(histKey{Channel: 0}, 7, "a", t0)
	h.add(histKey{Channel: -1, Peer: 7}, 7, "b", t0.Add(time.Second))
	h.add(histKey{Channel: 0}, 7, "c", t0.Add(2*time.Second))
	if got := textsOf(h.all()); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatal(got)
	}
}

func TestHistoryFind(t *testing.T) {
	h := newHistory(100, "x")
	h.add(histKey{Channel: 0}, 7, "a", t0)
	b := h.add(histKey{Channel: 1}, 7, "b", t0)
	if got, ok := h.find(b.ID); !ok || got.Text != "b" {
		t.Fatal(got, ok)
	}
	if _, ok := h.find("nope"); ok {
		t.Fatal("found an unknown id")
	}
}

func TestHistoryDropChannel(t *testing.T) {
	h := newHistory(100, "x")
	h.add(histKey{Channel: 4}, 7, "a", t0)
	h.add(histKey{Channel: -1, Peer: 7}, 7, "b", t0)
	h.dropChannel(4)
	if got := textsOf(h.all()); !slices.Equal(got, []string{"b"}) {
		t.Fatal(got)
	}
}

func TestHistoryDisabledKeepsNothingButNamesMessages(t *testing.T) {
	h := newHistory(-1, "x")
	e := h.add(histKey{Channel: 0}, 7, "a", t0)
	if len(h.all()) != 0 || e.ID == "" || e.At.IsZero() {
		t.Fatal(h.all(), e)
	}
}

func TestPick(t *testing.T) {
	h := newHistory(100, "x")
	for i := range 5 { // texts 0..4 at t0+0s..t0+4s
		h.add(histKey{Channel: 0}, 7, strconv.Itoa(i), t0.Add(time.Duration(i)*time.Second))
	}
	es := h.entries(histKey{Channel: 0})
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Second) }
	for _, tc := range []struct {
		name string
		got  []histEntry
		want []string
	}{
		{"latest all", pickLatest(es, time.Time{}, 3), []string{"2", "3", "4"}},
		{"latest after ref, exclusive", pickLatest(es, at(2), 10), []string{"3", "4"}},
		{"latest after ref, limited", pickLatest(es, at(0), 2), []string{"3", "4"}},
		{"before, exclusive", pickBefore(es, at(3), 10), []string{"0", "1", "2"}},
		{"before, the nearest", pickBefore(es, at(3), 2), []string{"1", "2"}},
		{"after, exclusive", pickAfter(es, at(1), 10), []string{"2", "3", "4"}},
		{"after, the nearest", pickAfter(es, at(1), 2), []string{"2", "3"}},
		{"around", pickAround(es, at(2), 3), []string{"1", "2", "3"}},
		{"around the start", pickAround(es, at(0), 3), []string{"0", "1", "2"}},
		{"between forwards", pickBetween(es, at(0), at(4), 2), []string{"1", "2"}},
		{"between backwards", pickBetween(es, at(4), at(0), 2), []string{"2", "3"}},
		{"between, exclusive", pickBetween(es, at(1), at(3), 10), []string{"2"}},
		{"nothing", pickBefore(es, at(0), 10), []string{}},
	} {
		if got := textsOf(tc.got); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
