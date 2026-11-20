package device_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/configpath"
	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

var fastMesh = device.Options{MeshTimeout: 100 * time.Millisecond, ReconnectDelay: 10 * time.Millisecond, ReconnectTimeout: 2 * time.Second}

func remoteNode(t *testing.T, o device.Options, setup func(*fakedevice.Device, *fakedevice.Remote)) (*device.Remote, *fakedevice.Device, *fakedevice.Remote) {
	t.Helper()
	c, d, r := withRemoteOpts(t, o, setup)
	n, err := c.Remote(ctxT(t), hill)
	if err != nil {
		t.Fatal(err)
	}
	return n, d, r
}

func loraChange(t *testing.T, n *device.Remote, hop uint32) device.Change {
	t.Helper()
	st, err := n.Fetch(ctxT(t), device.Want{Config: []string{"lora"}})
	if err != nil {
		t.Fatal(err)
	}
	l := proto.Clone(st.Config.GetLora()).(*pb.Config_LoRaConfig)
	l.HopLimit = hop
	return device.Change{Config: &pb.Config{PayloadVariant: &pb.Config_Lora{Lora: l}}}
}

func count(ms []*pb.AdminMessage, name string) int {
	n := 0
	for _, m := range ms {
		if fakedevice.OneofName(m) == name {
			n++
		}
	}
	return n
}

func TestRemoteApplyVerified(t *testing.T) {
	n, d, r := remoteNode(t, fastMesh, nil)
	if got, err := n.Apply(ctxT(t), []device.Change{loraChange(t, n, 5)}); err != nil || got != 1 {
		t.Fatal(got, err)
	}
	ms := d.RemoteAdmin(hill)
	if r.Config.GetLora().GetHopLimit() != 5 || count(ms, "begin_edit_settings") != 1 || count(ms, "commit_edit_settings") != 1 {
		t.Fatal(r.Config.GetLora(), ms)
	}
	if got, err := n.Apply(ctxT(t), []device.Change{loraChange(t, n, 5)}); err != nil || got != 0 {
		t.Fatal("no-op change applied:", got, err)
	}
}

func TestRemoteApplyRetriesLostAck(t *testing.T) {
	n, d, r := remoteNode(t, fastMesh, nil)
	ch := loraChange(t, n, 6)
	// The set is applied but its ack is lost: it is sent again, and since
	// time passed the edit is reopened and replayed (begin, set) as well.
	d.Update(func() { r.DropAcks, r.DropAcksFor = 1, "set_" })
	if got, err := n.Apply(ctxT(t), []device.Change{ch}); err != nil || got != 1 {
		t.Fatal(got, err)
	}
	ms := d.RemoteAdmin(hill)
	if r.Config.GetLora().GetHopLimit() != 6 || count(ms, "begin_edit_settings") != 2 || count(ms, "set_config") != 3 {
		t.Fatal(r.Config.GetLora(), count(ms, "begin_edit_settings"), count(ms, "set_config"))
	}
}

func TestRemoteApplyWaitsOutReboot(t *testing.T) {
	n, d, r := remoteNode(t, fastMesh, func(_ *fakedevice.Device, r *fakedevice.Remote) {
		r.RebootOnCommit, r.RebootFor = true, 400*time.Millisecond
	})
	start := time.Now()
	if got, err := n.Apply(ctxT(t), []device.Change{loraChange(t, n, 4)}); err != nil || got != 1 {
		t.Fatal(got, err)
	}
	_ = d
	if time.Since(start) < 400*time.Millisecond || r.Config.GetLora().GetHopLimit() != 4 {
		t.Fatal("verified before the node came back")
	}
}

func TestRemoteApplyRebootNeverReturns(t *testing.T) {
	o := fastMesh
	o.ReconnectTimeout = 300 * time.Millisecond
	n, _, _ := remoteNode(t, o, func(_ *fakedevice.Device, r *fakedevice.Remote) {
		r.RebootOnCommit, r.RebootFor = true, time.Hour
	})
	_, err := n.Apply(ctxT(t), []device.Change{loraChange(t, n, 4)})
	if err == nil || !strings.Contains(err.Error(), "committed but could not be verified") {
		t.Fatal(err)
	}
}

func TestRemoteApplyMismatch(t *testing.T) {
	n, _, _ := remoteNode(t, fastMesh, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.IgnoreFields = []string{"lora.hop_limit"} })
	_, err := n.Apply(ctxT(t), []device.Change{loraChange(t, n, 2)})
	var ve *device.VerifyError
	if !errors.As(err, &ve) || ve.Mismatches[0].Path != "lora.hop_limit" {
		t.Fatal(err)
	}
}

func TestRemoteFixedPositionCoordinatesUnverified(t *testing.T) {
	n, _, r := remoteNode(t, fastMesh, nil)
	pos := &pb.Position{LatitudeI: proto.Int32(1), LongitudeI: proto.Int32(2)}
	got, err := n.Apply(ctxT(t), []device.Change{{FixedPosition: pos}})
	if got != 1 || !errors.Is(err, device.ErrCoordinatesUnverified) || !r.Config.GetPosition().GetFixedPosition() {
		t.Fatal(got, err)
	}
}

func TestRemoteSecurityWriteNeedsFirmware28(t *testing.T) {
	n, d, _ := remoteNode(t, fastMesh, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.FirmwareVersion = "2.7.15.567b8ea" })
	st, err := n.Fetch(ctxT(t), device.Want{Config: []string{"security"}})
	if err != nil {
		t.Fatal(err)
	}
	sec := proto.Clone(st.Config.GetSecurity()).(*pb.Config_SecurityConfig)
	sec.AdminKey = append(sec.AdminKey, make([]byte, 32))
	_, err = n.Apply(ctxT(t), []device.Change{{Config: &pb.Config{PayloadVariant: &pb.Config_Security{Security: sec}}}})
	if err == nil || !strings.Contains(err.Error(), "older than 2.8") || count(d.RemoteAdmin(hill), "begin_edit_settings") != 0 {
		t.Fatal(err)
	}
}

func TestRemoteRebootSendsAcked(t *testing.T) {
	n, d, _ := remoteNode(t, fastMesh, nil)
	if err := n.Reboot(ctxT(t), 5); err != nil {
		t.Fatal(err)
	}
	ps := d.RemoteRequests(hill)
	last := ps[len(ps)-1]
	ms := d.RemoteAdmin(hill)
	if !last.GetWantAck() || ms[len(ms)-1].GetRebootSeconds() != 5 {
		t.Fatal(last, ms[len(ms)-1])
	}
}

func TestRemoteEditNodeDBNotVerifiable(t *testing.T) {
	n, d, _ := remoteNode(t, fastMesh, nil)
	if err := n.EditNodeDB(ctxT(t), device.Favorite, 42); !errors.Is(err, device.ErrNotVerifiable) {
		t.Fatal(err)
	}
	ms := d.RemoteAdmin(hill)
	if ms[len(ms)-1].GetSetFavoriteNode() != 42 {
		t.Fatal(ms[len(ms)-1])
	}
	if err := n.EditNodeDB(ctxT(t), device.RemoveNode, hill); err == nil || errors.Is(err, device.ErrNotVerifiable) {
		t.Fatal("removed the node from itself:", err)
	}
}

func TestRemoteApplyRefusesKeys(t *testing.T) {
	n, d, _ := remoteNode(t, fastMesh, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.FirmwareVersion = "2.8.2.d5f1619" })
	st, err := n.Fetch(ctxT(t), device.Want{Config: []string{"security"}})
	if err != nil {
		t.Fatal(err)
	}
	sec := proto.Clone(st.Config.GetSecurity()).(*pb.Config_SecurityConfig)
	sec.PrivateKey = make([]byte, 32)
	_, err = n.Apply(ctxT(t), []device.Change{{Config: &pb.Config{PayloadVariant: &pb.Config_Security{Security: sec}}}})
	if err == nil || !strings.Contains(err.Error(), "private key must not be sent over the air") || count(d.RemoteAdmin(hill), "begin_edit_settings") != 0 {
		t.Fatal(err)
	}
	sec = proto.Clone(st.Config.GetSecurity()).(*pb.Config_SecurityConfig)
	sec.PublicKey = make([]byte, 32)
	if _, err = n.Apply(ctxT(t), []device.Change{{Config: &pb.Config{PayloadVariant: &pb.Config_Security{Security: sec}}}}); err == nil || !strings.Contains(err.Error(), "public_key") {
		t.Fatal(err)
	}
}

func TestRemoteEditNodeDBRefusesTheConnectedNode(t *testing.T) {
	n, d, _ := remoteNode(t, fastMesh, nil)
	before := len(d.RemoteAdmin(hill))
	for _, op := range []device.NodeOp{device.Ignore, device.RemoveNode} {
		if err := n.EditNodeDB(ctxT(t), op, d.NodeNum); err == nil || errors.Is(err, device.ErrNotVerifiable) || !strings.Contains(err.Error(), "remote admin") {
			t.Fatal(op, err)
		}
	}
	if len(d.RemoteAdmin(hill)) != before {
		t.Fatal("sent something")
	}
	if err := n.EditNodeDB(ctxT(t), device.Favorite, d.NodeNum); !errors.Is(err, device.ErrNotVerifiable) { // harmless
		t.Fatal(err)
	}
}

func TestRemoteFixedPositionIsNeverANoop(t *testing.T) {
	n, d, _ := remoteNode(t, fastMesh, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Config.Position.FixedPosition = true })
	pos := &pb.Position{LatitudeI: proto.Int32(5), LongitudeI: proto.Int32(6)}
	got, err := n.Apply(ctxT(t), []device.Change{{FixedPosition: pos}})
	ms := d.RemoteAdmin(hill)
	if got != 1 || !errors.Is(err, device.ErrCoordinatesUnverified) || count(ms, "set_fixed_position") != 1 {
		t.Fatal(got, err, count(ms, "set_fixed_position"))
	}
}

func TestRemoteCommitUnacknowledged(t *testing.T) {
	n, d, r := remoteNode(t, fastMesh, nil)
	ch := loraChange(t, n, 5)
	d.Update(func() { r.DropAcks, r.DropAcksFor = 3, "commit_" }) // every attempt's ack lost; the commit itself landed
	_, err := n.Apply(ctxT(t), []device.Change{ch})
	if !errors.Is(err, device.ErrCommitUnconfirmed) || errors.Is(err, device.ErrPartialEdit) || !strings.Contains(err.Error(), "config get") {
		t.Fatal(err)
	}
	if r.Config.GetLora().GetHopLimit() != 5 {
		t.Fatal("the commit did land", r.Config.GetLora())
	}
}

func TestRemoteSettleReadsManyPartsPastTheDeadline(t *testing.T) {
	o := fastMesh
	o.MeshTimeout, o.ReconnectTimeout = 500*time.Millisecond, 150*time.Millisecond
	n, _, _ := remoteNode(t, o, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Latency = 40 * time.Millisecond })
	st, err := n.Fetch(ctxT(t), device.Want{Config: []string{"lora", "device", "position", "bluetooth", "display"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := proto.Clone(st.Config).(*pb.LocalConfig)
	cfg.Lora.HopLimit, cfg.Device.LedHeartbeatDisabled, cfg.Position.GpsUpdateInterval = 6, true, 120
	cfg.Bluetooth.FixedPin, cfg.Display.ScreenOnSecs = 123456, 30
	cs, _ := configpath.Changes(st.Config, cfg, st.ModuleConfig, st.ModuleConfig)
	var changes []device.Change
	for _, c := range cs {
		changes = append(changes, device.Change{Config: c})
	}
	// Reading five sections back takes ~200 ms, past the 150 ms reboot deadline.
	if got, err := n.Apply(ctxT(t), changes); err != nil || got != 5 {
		t.Fatal(got, err)
	}
}

func TestRemoteEditTransactionSurvivesALostSet(t *testing.T) {
	n, d, r := remoteNode(t, fastMesh, func(_ *fakedevice.Device, r *fakedevice.Remote) {
		r.EditIdle, r.LossDelay = 60*time.Millisecond, 100*time.Millisecond
	})
	st, err := n.Fetch(ctxT(t), device.Want{Config: []string{"lora", "device"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := proto.Clone(st.Config).(*pb.LocalConfig)
	cfg.Lora.HopLimit, cfg.Device.LedHeartbeatDisabled = 6, true
	cs, _ := configpath.Changes(st.Config, cfg, st.ModuleConfig, st.ModuleConfig)
	var changes []device.Change
	for _, c := range cs {
		changes = append(changes, device.Change{Config: c})
	}
	// The first set is lost; by the time its retry arrives the transaction
	// has been idle past EditIdle and the node has closed it.
	d.Update(func() { r.DropFirst, r.DropFirstFor = 1, "set_" })
	if got, err := n.Apply(ctxT(t), changes); err != nil || got != 2 {
		t.Fatal(got, err)
	}
	ms := d.RemoteAdmin(hill)
	if count(ms, "begin_edit_settings") != 2 || count(ms, "set_config") != 3 || count(ms, "commit_edit_settings") != 1 {
		t.Fatalf("begin %d set %d commit %d: the edit was not reopened and replayed",
			count(ms, "begin_edit_settings"), count(ms, "set_config"), count(ms, "commit_edit_settings"))
	}
}
