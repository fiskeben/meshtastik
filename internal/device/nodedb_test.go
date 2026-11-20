package device_test

import (
	"errors"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

func withCar() *fakedevice.Device {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{LongName: "Car", ShortName: "CAR1"}}}
	return d
}

func TestFavoriteAndIgnoreAreVerified(t *testing.T) {
	d := withCar()
	c, _ := openA(t, d, device.Options{})
	steps := []struct {
		op        device.NodeOp
		fav, ign  bool
		sentField string
	}{
		{device.Favorite, true, false, "set_favorite_node"},
		{device.Ignore, true, true, "set_ignored_node"},
		{device.Unignore, true, false, "remove_ignored_node"},
		{device.Unfavorite, false, false, "remove_favorite_node"},
	}
	for _, s := range steps {
		if err := c.EditNodeDB(ctxT(t), s.op, 42); err != nil {
			t.Fatal(s.op, err)
		}
		n := c.State().Nodes[42]
		if n.GetIsFavorite() != s.fav || n.GetIsIgnored() != s.ign {
			t.Fatalf("after %s: favorite %v ignored %v", s.op, n.GetIsFavorite(), n.GetIsIgnored())
		}
		if got := fakedevice.OneofName(d.LastAdmin()); got != s.sentField {
			t.Fatalf("after %s: node got %s", s.op, got)
		}
	}
}

func TestRemoveNodeIsVerified(t *testing.T) {
	d := withCar()
	c, _ := openA(t, d, device.Options{})
	if err := c.EditNodeDB(ctxT(t), device.RemoveNode, 42); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.State().Nodes[42]; ok {
		t.Fatal("node 42 still listed")
	}
	if d.LastAdmin().GetRemoveByNodenum() != 42 {
		t.Fatal(d.LastAdmin())
	}
}

func TestEditNodeDBRefusesSelf(t *testing.T) {
	d := withCar()
	c, _ := openA(t, d, device.Options{})
	before := len(d.Received())
	if err := c.EditNodeDB(ctxT(t), device.Ignore, d.NodeNum); err == nil {
		t.Fatal("ignored the connected node")
	}
	if len(d.Received()) != before {
		t.Fatal("sent something")
	}
}

func TestEditNodeDBNotApplied(t *testing.T) {
	d := withCar()
	d.IgnoreNodeOps = true
	c, _ := openA(t, d, device.Options{})
	err := c.EditNodeDB(ctxT(t), device.Favorite, 42)
	var ve *device.VerifyError
	if !errors.As(err, &ve) || len(ve.Mismatches) != 1 || ve.Mismatches[0].Path != "node !0000002a favorite" {
		t.Fatal(err)
	}
}

func TestResetNodeDB(t *testing.T) {
	d := withCar()
	d.ExtraNodes = append(d.ExtraNodes, &pb.NodeInfo{Num: 43, IsFavorite: true})
	c, _ := openA(t, d, device.Options{})
	if err := c.ResetNodeDB(ctxT(t), true); err != nil {
		t.Fatal(err)
	}
	if !d.LastAdmin().GetNodedbReset() || d.Dials() != 2 {
		t.Fatal(d.LastAdmin(), d.Dials())
	}
	nodes := c.State().Nodes
	if _, ok := nodes[42]; ok || nodes[43] == nil || nodes[d.NodeNum] == nil || len(nodes) != 2 {
		t.Fatal(nodes)
	}
}

func TestResetNodeDBWaitsForDelayedReboot(t *testing.T) {
	d := withCar()
	d.RebootDelay = 300 * time.Millisecond // firmware reboots ~7 s after a reset
	c, _ := openA(t, d, device.Options{})  // RebootGrace 50ms: shorter than the delay
	if err := c.ResetNodeDB(ctxT(t), false); err != nil {
		t.Fatal(err)
	}
	if d.Dials() != 2 {
		t.Fatalf("returned before the node rebooted (dials %d)", d.Dials())
	}
}

func TestResetNodeDBOverTCP(t *testing.T) {
	d := withCar()
	// Over TCP a rebooting node neither says so nor closes the socket.
	d.RebootDelay, d.SilentReboot = 100*time.Millisecond, true
	c, _ := openA(t, d, device.Options{RebootWait: 300 * time.Millisecond})
	if err := c.ResetNodeDB(ctxT(t), false); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.State().Nodes[42]; ok || d.Dials() != 2 {
		t.Fatal("not reconnected after the silent reboot", d.Dials())
	}
}
