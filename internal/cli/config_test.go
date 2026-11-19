package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/fakedevice"
	"meshtastik/internal/transport"
)

func TestConfigGet(t *testing.T) {
	d := fakedevice.New()
	if out, _, _ := runCLI(t, d, "", "config", "get", "lora.region"); out != "EU_868\n" {
		t.Fatalf("%q", out)
	}
	if out, _, _ := runCLI(t, d, "", "config", "get", "lora"); !strings.Contains(out, "region: EU_868") {
		t.Fatal(out)
	}
	out, _, _ := runCLI(t, d, "", "config", "get", "--json")
	var m map[string]any
	if json.Unmarshal([]byte(out), &m) != nil || m["config"] == nil || m["module_config"] == nil {
		t.Fatal(out)
	}
}

func TestConfigSetMultiplePairs(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "config", "set", "lora.hop_limit", "5", "lora.tx_power", "20"); err != nil {
		t.Fatal(err)
	}
	if d.Config.Lora.HopLimit != 5 || d.Config.Lora.TxPower != 20 || fakedevice.CountBegin(d.Received()) != 1 {
		t.Fatal()
	}
}

func TestConfigSetOddArgs(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "config", "set", "lora.hop_limit"); err == nil {
		t.Fatal()
	}
}

func TestConfigSetLockoutNeedsConfirm(t *testing.T) {
	d := fakedevice.New()
	d.Kind = transport.KindBLE
	if _, _, err := runCLI(t, d, "n\n", "config", "set", "bluetooth.enabled", "false"); err == nil || !d.Config.Bluetooth.Enabled {
		t.Fatal("applied without confirm")
	}
	if _, _, err := runCLI(t, d, "", "--yes", "config", "set", "bluetooth.enabled", "false"); err != nil || d.Config.Bluetooth.Enabled {
		t.Fatal(err)
	}
}

func TestConfigSetMismatchFails(t *testing.T) {
	d := fakedevice.New()
	d.IgnoreFields = []string{"lora.hop_limit"}
	_, _, err := runCLI(t, d, "", "config", "set", "lora.hop_limit", "5")
	if err == nil || !strings.Contains(err.Error(), "lora.hop_limit") {
		t.Fatal(err)
	}
}

func TestConfigGetUsesProtoNames(t *testing.T) {
	d := fakedevice.New()
	if out, _, _ := runCLI(t, d, "", "config", "get", "lora", "--json"); !strings.Contains(out, `"hop_limit"`) || strings.Contains(out, "hopLimit") {
		t.Fatal(out)
	}
	if out, _, _ := runCLI(t, d, "", "config", "get", "lora"); !strings.Contains(out, "hop_limit:") {
		t.Fatal(out)
	}
	if out, _, _ := runCLI(t, d, "", "config", "get", "--json"); !strings.Contains(out, `"hop_limit"`) {
		t.Fatal(out)
	}
}

func TestConfigSetNegativeAfterDashDash(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "config", "set", "--", "lora.tx_power", "-5"); err != nil || d.Config.Lora.TxPower != -5 {
		t.Fatal(err)
	}
}

func TestExportFileMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, fakedevice.New(), "", "config", "export", "-o", p); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	got, _ := os.ReadFile(p)
	if fi.Mode().Perm() != 0o600 || !strings.HasPrefix(string(got), "# start of Meshtastic configure yaml") {
		t.Fatal(fi.Mode(), string(got))
	}
}

func TestExportFailureLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "node.yaml")
	if err := os.Mkdir(p, 0o755); err != nil { // rename over a directory fails
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, fakedevice.New(), "", "config", "export", "-o", p); err == nil {
		t.Fatal("expected error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
}

func TestExportImportRoundTripIsNoOp(t *testing.T) {
	d := fakedevice.New()
	d.Config.Position.FixedPosition = true
	p := filepath.Join(t.TempDir(), "rt.yaml")
	if _, _, err := runCLI(t, d, "", "config", "export", "-o", p); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runCLI(t, d, "", "config", "import", p, "--keys")
	if err != nil || out != "No changes needed.\n" || fakedevice.CountBegin(d.Received()) != 0 {
		t.Fatal(err, out, stderr, d.Received())
	}
}

func TestExportStdout(t *testing.T) {
	out, _, err := runCLI(t, fakedevice.New(), "", "config", "export")
	if err != nil || !strings.HasPrefix(out, "# start of Meshtastic configure yaml\n") {
		t.Fatal(err, out)
	}
}

func TestImportClonesWithoutIdentity(t *testing.T) {
	a := fakedevice.New()
	a.Config.Lora.HopLimit = 6
	a.Config.Security.PrivateKey = bytes.Repeat([]byte{1}, 32)
	p := filepath.Join(t.TempDir(), "a.yaml")
	if _, _, err := runCLI(t, a, "", "config", "export", "-o", p); err != nil {
		t.Fatal(err)
	}
	b := fakedevice.New()
	b.Config.Security.PrivateKey = bytes.Repeat([]byte{2}, 32)
	_, stderr, err := runCLI(t, b, "", "config", "import", p)
	if err != nil || b.Config.Lora.HopLimit != 6 || b.Config.Security.PrivateKey[0] != 2 || !strings.Contains(stderr, "skipped: security.private_key (pass --keys to apply)") {
		t.Fatal(err, stderr)
	}
	if _, _, err := runCLI(t, b, "", "config", "import", p, "--keys"); err != nil || b.Config.Security.PrivateKey[0] != 1 {
		t.Fatal(err)
	}
}

func TestImportOwnerChannelsUnsupported(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.yaml")
	y := "owner: New Name\nowner_short: NEW\nringtone: x\nconfig: {lora: {hopLimit: 4}}\n"
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	d := fakedevice.New()
	_, stderr, err := runCLI(t, d, "", "config", "import", p)
	if err != nil || d.Owner.LongName != "New Name" || d.Owner.ShortName != "NEW" || d.Config.Lora.HopLimit != 4 ||
		!strings.Contains(stderr, "not supported, ignored: ringtone") {
		t.Fatal(err, stderr, d.Owner)
	}
}

func TestImportLocationAndChannelURL(t *testing.T) {
	src := fakedevice.New()
	src.Channels[1] = &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "club", Psk: []byte{9}}}
	u, _, err := runCLI(t, src, "", "channel", "url", "--no-qr")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "x.yaml")
	y := "channel_url: '" + strings.TrimSpace(u) + "'\nlocation: {lat: 59.9139, lon: 10.7522, alt: 23}\n"
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	d := fakedevice.New()
	if _, stderr, err := runCLI(t, d, "", "config", "import", p); err != nil {
		t.Fatal(err, stderr)
	}
	if d.Channels[1].GetSettings().GetName() != "club" {
		t.Fatal("channel not imported")
	}
	out, _, _ := runCLI(t, d, "", "config", "get", "position.fixed_position")
	if out != "true\n" {
		t.Fatal(out)
	}
	var pos *pb.Position
	for _, m := range d.Received() {
		if p := m.GetSetFixedPosition(); p != nil {
			pos = p
		}
	}
	if pos == nil || pos.GetLatitudeI() != 599139000 || pos.GetLongitudeI() != 107522000 || pos.GetAltitude() != 23 {
		t.Fatal(pos)
	}
}
