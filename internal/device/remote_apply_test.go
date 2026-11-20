package device_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

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
	d.Update(func() { r.DropAcks, r.DropAcksFor = 1, "set_" }) // the set is applied but its ack is lost: sent again
	if got, err := n.Apply(ctxT(t), []device.Change{ch}); err != nil || got != 1 {
		t.Fatal(got, err)
	}
	ms := d.RemoteAdmin(hill)
	if r.Config.GetLora().GetHopLimit() != 6 || count(ms, "begin_edit_settings") != 1 || count(ms, "set_config") != 2 {
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
