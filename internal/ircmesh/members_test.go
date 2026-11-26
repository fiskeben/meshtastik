package ircmesh

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/device"
)

func pkt(from, to, ch uint32, rx bool) *pb.MeshPacket {
	p := &pb.MeshPacket{From: from, To: to, Channel: ch,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("x")}}}
	if rx {
		p.RxTime = proto.Uint32(1700000000)
	}
	return p
}

func TestCountsOnlyDecodedBroadcasts(t *testing.T) {
	if ch, ok := counts(pkt(7, device.Broadcast, 4, true), selfNum); !ok || ch != 4 {
		t.Fatal(ch, ok)
	}
	encrypted := &pb.MeshPacket{From: 7, To: device.Broadcast, Channel: 0x8b, RxTime: proto.Uint32(1),
		PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1, 2}}}
	pki := pkt(7, device.Broadcast, 0, true)
	pki.PkiEncrypted = true
	bad := map[string]*pb.MeshPacket{
		"encrypted": encrypted,
		"pki":       pki,
		"to self":   pkt(7, selfNum, 0, true),
		"from self": pkt(selfNum, device.Broadcast, 0, true),
		"index 8+":  pkt(7, device.Broadcast, 8, true),
	}
	for name, p := range bad {
		if _, ok := counts(p, selfNum); ok {
			t.Errorf("%s counted", name)
		}
	}
}

func TestCountsWithoutRxTime(t *testing.T) {
	if ch, ok := counts(pkt(7, device.Broadcast, 2, false), selfNum); !ok || ch != 2 {
		t.Fatal("a node without a clock never counts", ch, ok)
	}
}

func TestHeardJoinsOnce(t *testing.T) {
	m := newMembership(24 * time.Hour)
	now := time.Now()
	if !m.heard(7, 4, now) || m.heard(7, 4, now.Add(time.Minute)) {
		t.Fatal("joined twice or never")
	}
	if !slices.Equal(m.members(4), []uint32{7}) || !slices.Equal(m.channels(7), []int{4}) {
		t.Fatal(m.members(4), m.channels(7))
	}
	if !m.lastHeard(7).Equal(now.Add(time.Minute)) {
		t.Fatal(m.lastHeard(7))
	}
}

func TestExpirePerChannel(t *testing.T) {
	m := newMembership(24 * time.Hour)
	t0 := time.Now()
	m.heard(7, 0, t0)
	m.heard(7, 4, t0.Add(20*time.Hour))
	if got := m.expire(t0.Add(25 * time.Hour)); !slices.Equal(got, []change{{7, 0, false}}) {
		t.Fatal(got)
	}
	if got := m.expire(t0.Add(45 * time.Hour)); !slices.Equal(got, []change{{7, 4, true}}) {
		t.Fatal(got)
	}
	if len(m.channels(7)) != 0 {
		t.Fatal(m.channels(7))
	}
}

func TestSeed(t *testing.T) {
	m := newMembership(24 * time.Hour)
	now := time.Now()
	m.seed(map[uint32]*pb.NodeInfo{
		7:       {Num: 7, Channel: 4, LastHeard: uint32(now.Add(-time.Hour).Unix())},
		8:       {Num: 8, Channel: 0, LastHeard: uint32(now.Add(-30 * time.Hour).Unix())},
		9:       {Num: 9, LastHeard: uint32(now.Add(-time.Hour).Unix())},
		selfNum: {Num: selfNum, LastHeard: uint32(now.Unix())},
	}, selfNum, now)
	if !slices.Equal(m.members(4), []uint32{7}) || !slices.Equal(m.members(0), []uint32{9}) {
		t.Fatal(m.members(4), m.members(0))
	}
	m.heard(7, 4, now)
	m.seed(map[uint32]*pb.NodeInfo{7: {Num: 7, Channel: 4, LastHeard: uint32(now.Add(-time.Hour).Unix())}}, selfNum, now)
	if !m.lastHeard(7).Equal(now) {
		t.Fatal("seed moved a time backwards")
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ircmesh", "members-ab2c9b0a.json")
	m := newMembership(24 * time.Hour)
	now := time.Now().Truncate(time.Second)
	m.heard(7, 0, now)
	m.heard(7, 4, now)
	m.heard(8, 4, now.Add(-time.Hour))
	if err := m.save(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode(), err)
	}
	n := newMembership(24 * time.Hour)
	if err := n.load(path); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(n.members(4), []uint32{7, 8}) || !slices.Equal(n.channels(7), []int{0, 4}) || !n.lastHeard(7).Equal(now) {
		t.Fatal(n.members(4), n.channels(7), n.lastHeard(7))
	}
	if err := newMembership(time.Hour).load(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Fatal("missing file:", err)
	}
}

func TestDropChannel(t *testing.T) {
	m := newMembership(24 * time.Hour)
	m.heard(7, 4, time.Now())
	m.dropChannel(4)
	if len(m.members(4)) != 0 {
		t.Fatal(m.members(4))
	}
}
