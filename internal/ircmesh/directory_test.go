package ircmesh

import (
	"slices"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

const selfNum = 0xab2c9b0a

func TestDirectoryCollision(t *testing.T) {
	d := newDirectory(selfNum, false)
	d.set(0x11110001, &pb.User{ShortName: "AB"})
	d.set(0x22220002, &pb.User{ShortName: "AB"})
	if d.nick(0x11110001) != "AB" || d.nick(0x22220002) != "AB-0002" {
		t.Fatal(d.nick(0x11110001), d.nick(0x22220002))
	}
	got := d.set(0x22220002, &pb.User{ShortName: "CD"})
	if want := []nickChange{{0x22220002, "AB-0002", "CD"}}; !slices.Equal(got, want) {
		t.Fatal(got)
	}
	if got := d.set(0x22220002, &pb.User{ShortName: "CD", LongName: "new long name"}); len(got) != 0 {
		t.Fatal("same nick reported a change", got)
	}
}

func TestDirectoryLongSuffix(t *testing.T) {
	d := newDirectory(selfNum, false)
	d.set(0x1, &pb.User{ShortName: "Abcdefghijklmnop"})
	d.set(0x2, &pb.User{ShortName: "Abcdefghijklmnop"})
	if n := d.nick(0x2); n != "Abcdefghijk-0002" {
		t.Fatal(n)
	}
	d.set(0x30002, &pb.User{ShortName: "Abcdefghijklmnop"})
	if n := d.nick(0x30002); n != "Abcdefg-00030002" {
		t.Fatal(n)
	}
}

func TestDirectorySelfWins(t *testing.T) {
	d := newDirectory(selfNum, false)
	d.set(0x33, &pb.User{ShortName: "🥸", LongName: "Ricco"})
	got := d.set(selfNum, &pb.User{ShortName: "🥸", LongName: "Ricco"})
	if d.nick(selfNum) != "Ricco" || d.nick(0x33) != "Ricco-0033" {
		t.Fatal(d.nick(selfNum), d.nick(0x33))
	}
	if !slices.Contains(got, nickChange{0x33, "Ricco", "Ricco-0033"}) {
		t.Fatal(got)
	}
}

func TestDirectoryLookupFolds(t *testing.T) {
	d := newDirectory(selfNum, false)
	d.set(0x1bf0411d, &pb.User{ShortName: "AS02"})
	if n, ok := d.lookup("as02"); !ok || n != 0x1bf0411d {
		t.Fatal(n, ok)
	}
	if _, ok := d.lookup("nobody"); ok {
		t.Fatal("found nobody")
	}
	if m := d.mask(0x1bf0411d); m != "AS02!1bf0411d@mesh" {
		t.Fatal(m)
	}
}

func TestDirectoryUnknownNode(t *testing.T) {
	d := newDirectory(selfNum, false)
	if n := d.nick(0x1bf0411d); n != "n411d" {
		t.Fatal(n)
	}
	if got := d.set(0x1bf0411d, &pb.User{ShortName: "AS02"}); !slices.Equal(got, []nickChange{{0x1bf0411d, "n411d", "AS02"}}) {
		t.Fatal(got)
	}
	if d.user(0x1bf0411d).GetShortName() != "AS02" {
		t.Fatal(d.user(0x1bf0411d))
	}
}

func TestDirectoryLongNickCollision(t *testing.T) {
	d := newDirectory(selfNum, true)
	name := "Stavanger Harbour Repeater Station North"
	d.set(0x11110001, &pb.User{ShortName: "SH1", LongName: name})
	d.set(0x22220002, &pb.User{ShortName: "SH2", LongName: name + " 2"})
	if d.nick(0x11110001) != "Stavanger_Harbour_Repeater_Stati" || d.nick(0x22220002) != "Stavanger_Harbour_Repeater_-0002" {
		t.Fatal(d.nick(0x11110001), d.nick(0x22220002))
	}
}
