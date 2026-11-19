package device_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

func lora(hop uint32) device.Change {
	return device.Change{Config: &pb.Config{PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_EU_868, HopLimit: hop, TxEnabled: true, UsePreset: true}}}}
}

func hasBegin(r []*pb.AdminMessage) bool { return fakedevice.CountBegin(r) > 0 }

// openA opens with a short reboot grace unless o sets one: the fake reboots
// (or not) immediately after commit.
func openA(t *testing.T, d *fakedevice.Device, o device.Options) (*device.Client, error) {
	t.Helper()
	if o.RebootGrace == 0 {
		o.RebootGrace = 50 * time.Millisecond
	}
	return open(t, d, o)
}

func TestApplySetsAndVerifies(t *testing.T) {
	d := fakedevice.New()
	c, _ := openA(t, d, device.Options{})
	n, err := c.Apply(ctxT(t), []device.Change{lora(5)})
	if err != nil || n != 1 || c.State().Config.Lora.HopLimit != 5 {
		t.Fatal(n, err)
	}
	r := d.Received()
	if !hasBegin(r) || !fakedevice.HasCommit(r) {
		t.Fatal("no edit transaction")
	}
}

func TestApplyUsesSessionPasskey(t *testing.T) {
	d := fakedevice.New()
	d.RequirePasskey = true
	c, _ := openA(t, d, device.Options{})
	if _, err := c.Apply(ctxT(t), []device.Change{lora(5)}); err != nil {
		t.Fatal(err)
	}
	for _, m := range d.Received() {
		if m.GetSetConfig() != nil && len(m.SessionPasskey) == 0 {
			t.Fatal("set without passkey")
		}
	}
}

func TestApplyReportsMismatch(t *testing.T) {
	d := fakedevice.New()
	d.IgnoreFields = []string{"lora.hop_limit"}
	c, _ := openA(t, d, device.Options{})
	_, err := c.Apply(ctxT(t), []device.Change{lora(5)})
	var ve *device.VerifyError
	if !errors.As(err, &ve) || len(ve.Mismatches) != 1 || ve.Mismatches[0] != (device.Mismatch{Path: "lora.hop_limit", Want: "5", Got: "3"}) {
		t.Fatal(err)
	}
	if want := "not applied by the node:\n  lora.hop_limit: want 5, got 3"; err.Error() != want {
		t.Fatalf("%q", err.Error())
	}
}

func TestApplyNoChangesSkips(t *testing.T) {
	d := fakedevice.New()
	c, _ := openA(t, d, device.Options{})
	n, err := c.Apply(ctxT(t), []device.Change{lora(3)})
	if err != nil || n != 0 || hasBegin(d.Received()) {
		t.Fatal(n, err)
	}
}

func TestApplyReconnectsAfterReboot(t *testing.T) {
	d := fakedevice.New()
	d.RebootOnCommit = true
	c, _ := openA(t, d, device.Options{})
	if _, err := c.Apply(ctxT(t), []device.Change{lora(5)}); err != nil || d.Dials() != 2 {
		t.Fatal(err, d.Dials())
	}
	if c.State().Config.Lora.HopLimit != 5 {
		t.Fatal("state not refreshed after reconnect")
	}
}

func TestApplyReconnectRestartsHeartbeat(t *testing.T) {
	d := fakedevice.New()
	d.RebootOnCommit = true
	c, _ := openA(t, d, device.Options{HeartbeatInterval: 20 * time.Millisecond})
	if _, err := c.Apply(ctxT(t), []device.Change{lora(5)}); err != nil {
		t.Fatal(err)
	}
	before := d.Heartbeats()
	time.Sleep(80 * time.Millisecond)
	if d.Heartbeats()-before < 2 {
		t.Fatal(before, d.Heartbeats())
	}
}

func TestApplyReconnectTimeout(t *testing.T) {
	d := fakedevice.New()
	d.RebootOnCommit = true
	d.RefuseDialsAfterReboot = true
	c, _ := openA(t, d, device.Options{ReconnectTimeout: 300 * time.Millisecond})
	if _, err := c.Apply(ctxT(t), []device.Change{lora(5)}); err == nil || !strings.Contains(err.Error(), "did not come back") {
		t.Fatal(err)
	}
}

func TestApplyPartialEditOnDrop(t *testing.T) {
	d := fakedevice.New()
	d.DropAfter = func(m *pb.AdminMessage) bool { return m.GetSetConfig() != nil }
	c, _ := openA(t, d, device.Options{})
	bt := device.Change{Config: &pb.Config{PayloadVariant: &pb.Config_Bluetooth{Bluetooth: &pb.Config_BluetoothConfig{Enabled: false}}}}
	_, err := c.Apply(ctxT(t), []device.Change{lora(5), bt})
	if !errors.Is(err, device.ErrPartialEdit) {
		t.Fatal(err)
	}
}

func TestApplyPartialEditOnCancel(t *testing.T) {
	d := fakedevice.New()
	c, _ := openA(t, d, device.Options{})
	ctx, cancel := context.WithCancel(ctxT(t))
	d.OnAdmin = func(m *pb.AdminMessage) {
		if m.GetBeginEditSettings() {
			cancel()
			time.Sleep(20 * time.Millisecond)
		}
	}
	if _, err := c.Apply(ctx, []device.Change{lora(5)}); !errors.Is(err, device.ErrPartialEdit) {
		t.Fatal(err)
	}
}

func TestApplyOwnerAndChannel(t *testing.T) {
	d := fakedevice.New()
	c, _ := openA(t, d, device.Options{})
	ch := &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "admin", Psk: bytes.Repeat([]byte{7}, 32)}}
	_, err := c.Apply(ctxT(t), []device.Change{{Owner: &pb.User{LongName: "Base", ShortName: "BASE"}}, {Channel: ch}})
	if err != nil || c.State().Self().User.ShortName != "BASE" || c.State().Channels[1].Settings.Name != "admin" {
		t.Fatal(err)
	}
}

func TestApplyFixedPosition(t *testing.T) {
	d := fakedevice.New()
	c, _ := openA(t, d, device.Options{})
	pos := &pb.Position{LatitudeI: new(int32(599_000_000)), LongitudeI: new(int32(107_000_000))}
	n, err := c.Apply(ctxT(t), []device.Change{{FixedPosition: pos}})
	if err != nil || n != 1 || !c.State().Config.Position.FixedPosition {
		t.Fatal(n, err)
	}
	if got := d.LastAdmin(); !fakedevice.HasCommit([]*pb.AdminMessage{got}) {
		t.Fatal(got)
	}
}

func TestApplyRejectsAmbiguousChange(t *testing.T) {
	d := fakedevice.New()
	c, _ := openA(t, d, device.Options{})
	both := lora(5)
	both.Owner = &pb.User{LongName: "x"}
	if _, err := c.Apply(ctxT(t), []device.Change{both}); err == nil || hasBegin(d.Received()) {
		t.Fatal(err)
	}
}

func TestAdminRequestReturnsResponse(t *testing.T) {
	d := fakedevice.New()
	c, _ := openA(t, d, device.Options{})
	resp, err := c.AdminRequest(ctxT(t), &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetChannelRequest{GetChannelRequest: 1}})
	if err != nil || resp.GetGetChannelResponse().GetRole() != pb.Channel_PRIMARY || len(resp.SessionPasskey) == 0 {
		t.Fatal(resp, err)
	}
}

func TestFactoryResetSendsRightField(t *testing.T) {
	for _, full := range []bool{false, true} {
		d := fakedevice.New()
		d.RequirePasskey = true
		c, _ := openA(t, d, device.Options{})
		if err := c.FactoryReset(ctxT(t), full); err != nil {
			t.Fatal(err)
		}
		m := d.LastAdmin()
		if len(m.SessionPasskey) == 0 {
			t.Fatal("factory reset without passkey")
		}
		if full && m.GetFactoryResetDevice() != 1 || !full && m.GetFactoryResetConfig() != 1 {
			t.Fatal(full, m)
		}
	}
}

func TestRebootAndShutdown(t *testing.T) {
	d := fakedevice.New()
	c, _ := openA(t, d, device.Options{})
	if err := c.Reboot(ctxT(t), 5); err != nil || d.LastAdmin().GetRebootSeconds() != 5 {
		t.Fatal(err, d.LastAdmin())
	}
	if err := c.Shutdown(ctxT(t), 7); err != nil || d.LastAdmin().GetShutdownSeconds() != 7 {
		t.Fatal(err, d.LastAdmin())
	}
}

func TestApplyRebootDuringReHandshake(t *testing.T) {
	for _, reboots := range []int{1, 2} { // 2: the redial's first want_config is lost to the reboot too
		d := fakedevice.New()
		d.HandshakeReboots = reboots
		c, _ := openA(t, d, device.Options{HandshakeTimeout: 5 * time.Second})
		start := time.Now()
		if _, err := c.Apply(ctxT(t), []device.Change{lora(5)}); err != nil || d.Dials() != 2 {
			t.Fatal(reboots, err, d.Dials())
		}
		if el := time.Since(start); el > time.Second {
			t.Fatal(reboots, "waited for the handshake timeout:", el)
		}
		if c.State().Config.Lora.HopLimit != 5 {
			t.Fatal(reboots, "not verified")
		}
	}
}

func TestApplySilentAfterCommitReconnects(t *testing.T) {
	d := fakedevice.New()
	d.HangOnCommit = true
	c, _ := openA(t, d, device.Options{HandshakeTimeout: 200 * time.Millisecond})
	if _, err := c.Apply(ctxT(t), []device.Change{lora(5)}); err != nil || d.Dials() != 2 {
		t.Fatal(err, d.Dials())
	}
}

func TestApplySilentAfterCommitDidNotComeBack(t *testing.T) {
	d := fakedevice.New()
	d.HangOnCommit = true
	d.RefuseDialsAfterReboot = true
	c, _ := openA(t, d, device.Options{HandshakeTimeout: 200 * time.Millisecond, ReconnectTimeout: 300 * time.Millisecond})
	_, err := c.Apply(ctxT(t), []device.Change{lora(5)})
	if err == nil || !strings.Contains(err.Error(), "did not come back") || strings.Contains(err.Error(), "another program") {
		t.Fatal(err)
	}
}

func TestApplyFailureBeforeBeginIsNotPartial(t *testing.T) {
	d := fakedevice.New()
	d.DropAfter = func(m *pb.AdminMessage) bool { return m.GetGetConfigRequest() == pb.AdminMessage_SESSIONKEY_CONFIG }
	c, _ := openA(t, d, device.Options{})
	_, err := c.Apply(ctxT(t), []device.Change{lora(5)})
	if err == nil || errors.Is(err, device.ErrPartialEdit) || hasBegin(d.Received()) {
		t.Fatal(err)
	}
}
