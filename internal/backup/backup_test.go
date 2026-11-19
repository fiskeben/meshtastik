package backup

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/channelurl"
	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func fixture(t *testing.T) []byte {
	raw, err := os.ReadFile("testdata/python-export.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var chs [8]*pb.Channel
	chs[0] = &pb.Channel{Role: pb.Channel_PRIMARY, Settings: &pb.ChannelSettings{Name: "x", Psk: []byte{1}}}
	u, err := channelurl.Encode(chs, &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_EU_868})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(raw, []byte("{{URL}}"), []byte(u))
}

func TestReadPythonFixture(t *testing.T) {
	d, err := Read(fixture(t))
	if err != nil || d.Owner != "Base Station" || d.OwnerShort != "BASE" || d.Location.Lat != 59.9139 || !slices.Contains(d.Unsupported, "canned_messages") {
		t.Fatal(d, err)
	}
}

func TestMergeConfigPresenceSemantics(t *testing.T) {
	d, _ := Read(fixture(t))
	base := &pb.LocalConfig{Bluetooth: &pb.Config_BluetoothConfig{Enabled: true}, Lora: &pb.Config_LoRaConfig{TxPower: 27}, Security: &pb.Config_SecurityConfig{PrivateKey: []byte{1}}}
	got, skipped, err := d.MergeConfig(base, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bluetooth.Enabled {
		t.Error("explicit false not applied")
	}
	if got.Lora.TxPower != 27 || got.Lora.HopLimit != 5 {
		t.Error("absent key overwrote base, or present key missed")
	}
	if !bytes.Equal(got.Security.PrivateKey, []byte{1}) || len(got.Security.AdminKey) != 1 {
		t.Error("key handling")
	}
	if !slices.Contains(skipped, "security.private_key") || !slices.Contains(skipped, "security.public_key") {
		t.Error(skipped)
	}
	got, _, _ = d.MergeConfig(base, true)
	if len(got.Security.PrivateKey) != 32 {
		t.Error("--keys not honoured")
	}
}

func TestSkippedOnlyWhenPresent(t *testing.T) {
	d, _ := Read([]byte("config: {security: {serialEnabled: true, privateKey: ''}}"))
	_, skipped, err := d.MergeConfig(&pb.LocalConfig{}, false)
	if err != nil || len(skipped) != 0 {
		t.Fatal(skipped, err)
	}
}

func TestMergeAcceptsSnakeAndCamel(t *testing.T) {
	for _, y := range []string{"config: {lora: {hop_limit: 6}}", "config: {lora: {hopLimit: 6}}"} {
		d, _ := Read([]byte(y))
		got, _, err := d.MergeConfig(&pb.LocalConfig{}, false)
		if err != nil || got.Lora.HopLimit != 6 {
			t.Fatal(y, err)
		}
	}
}

func TestMergeUnknownNamesError(t *testing.T) {
	for y, name := range map[string]string{"config: {lroa: {region: EU_868}}": "lroa", "config: {lora: {regoin: EU_868}}": "regoin"} {
		d, _ := Read([]byte(y))
		if _, _, err := d.MergeConfig(&pb.LocalConfig{}, false); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatal(y, err)
		}
	}
}

func TestReadUnknownTopLevelKey(t *testing.T) {
	if _, err := Read([]byte("bogus: 1")); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatal(err)
	}
}

func TestMergeModuleConfig(t *testing.T) {
	d, _ := Read(fixture(t))
	got, err := d.MergeModuleConfig(&pb.LocalModuleConfig{})
	if err != nil || got.Telemetry.DeviceUpdateInterval != 900 || !got.Mqtt.EncryptionEnabled {
		t.Fatal(got, err)
	}
}

func TestExportRoundTrip(t *testing.T) {
	c, err := device.Open(ctxT(t), fakedevice.New().Dialer(), device.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st := c.State()
	out, err := Export(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# start of Meshtastic configure yaml", "owner_short: TEST", "module_config:", "external_notification:", "hopLimit:"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q", want)
		}
	}
	d, err := Read(out)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := d.MergeConfig(&pb.LocalConfig{}, true)
	if err != nil {
		t.Fatal(err)
	}
	mod, err := d.MergeModuleConfig(&pb.LocalModuleConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(cfg, st.Config) || !proto.Equal(mod, st.ModuleConfig) {
		t.Fatal("round trip lost data")
	}
}
