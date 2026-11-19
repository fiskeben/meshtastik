package cli

import (
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

func TestResolveNode(t *testing.T) {
	s := &device.State{Nodes: map[uint32]*pb.NodeInfo{
		0x1234abcd: {Num: 0x1234abcd, User: &pb.User{LongName: "Base Station", ShortName: "BASE"}},
		7:          {Num: 7, User: &pb.User{LongName: "Car", ShortName: "CAR1"}}, 8: {Num: 8, User: &pb.User{LongName: "Car", ShortName: "CAR2"}},
	}}
	for q, want := range map[string]uint32{"!1234abcd": 0x1234abcd, "305441741": 0x1234abcd, "base": 0x1234abcd, "base station": 0x1234abcd, "car2": 8} {
		if got, err := resolveNode(s, q); err != nil || got != want {
			t.Errorf("%s: %x %v", q, got, err)
		}
	}
	if _, err := resolveNode(s, "Car"); err == nil || !strings.Contains(err.Error(), "CAR1") {
		t.Error("ambiguous not reported")
	}
	if _, err := resolveNode(s, "nobody"); err == nil {
		t.Error("unknown accepted")
	}
}

func TestSendBroadcast(t *testing.T) {
	d := fakedevice.New()
	out, _, err := runCLI(t, d, "", "send", "hello")
	p := d.Texts()[0]
	if err != nil || p.To != device.Broadcast || string(p.GetDecoded().Payload) != "hello" || !strings.Contains(out, "Sent") {
		t.Fatal(out, err)
	}
}

func TestSendDirectAck(t *testing.T) {
	d := fakedevice.New()
	d.AckTexts = true
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{LongName: "Peer", ShortName: "PEER"}}}
	out, _, err := runCLI(t, d, "", "send", "hi", "--to", "PEER")
	if err != nil || !strings.Contains(out, "Acknowledged by PEER") || !d.Texts()[0].WantAck {
		t.Fatal(out, err)
	}
}

func TestSendDirectNoAck(t *testing.T) {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{ShortName: "PEER"}}}
	_, _, err := runCLI(t, d, "", "send", "hi", "--to", "PEER", "--ack-timeout", "100ms")
	if err == nil || !strings.Contains(err.Error(), "no acknowledgement") {
		t.Fatal(err)
	}
}

func TestSendTooLong(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "send", strings.Repeat("x", 234)); err == nil || !strings.Contains(err.Error(), "233") {
		t.Fatal(err)
	}
}

func TestSendBadChannel(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "send", "x", "--channel", "8"); err == nil || !strings.Contains(err.Error(), "0-7") {
		t.Fatal(err)
	}
}
