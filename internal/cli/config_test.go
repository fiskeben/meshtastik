package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
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
	a.Config.Security.PublicKey = bytes.Repeat([]byte{3}, 32) // a real node always has both
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
	y := "channel_url: '" + strings.TrimSpace(u) + "'\nlocation: {lat: 59.9139, lon: 10.7522, alt: 23}\n" +
		"config: {position: {fixedPosition: true}}\n"
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

func writeBackup(t *testing.T, y string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "b.yaml")
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigGetRedactsPrivateKey(t *testing.T) {
	d := fakedevice.New()
	d.Config.Security.PrivateKey = bytes.Repeat([]byte{0xab}, 32)
	secret := base64.StdEncoding.EncodeToString(d.Config.Security.PrivateKey)
	for _, args := range [][]string{{"config", "get"}, {"config", "get", "--json"}, {"config", "get", "security"}, {"config", "get", "security", "--json"}} {
		out, _, err := runCLI(t, d, "", args...)
		if err != nil || strings.Contains(out, secret) || !strings.Contains(out, "<redacted; use config export>") {
			t.Error(args, err, out)
		}
	}
	if out, _, err := runCLI(t, d, "", "config", "get", "security.private_key"); err != nil || out != "base64:"+secret+"\n" {
		t.Fatal(err, out)
	}
}

func TestConfigGetShowsZeroValues(t *testing.T) {
	d := fakedevice.New()
	d.Config.Bluetooth.Enabled = false
	if out, _, err := runCLI(t, d, "", "config", "get", "bluetooth"); err != nil || !strings.Contains(out, "enabled: false") {
		t.Fatal(err, out)
	}
	if out, _, err := runCLI(t, d, "", "config", "get", "--json"); err != nil || !strings.Contains(out, `"fixed_position": false`) {
		t.Fatal(err, out)
	}
}

func TestImportOwnerKeepsOtherUserFields(t *testing.T) {
	d := fakedevice.New()
	d.Owner.IsLicensed = true
	if _, stderr, err := runCLI(t, d, "", "config", "import", writeBackup(t, "owner: New Name\n")); err != nil {
		t.Fatal(err, stderr)
	}
	if d.Owner.LongName != "New Name" || d.Owner.ShortName != "TEST" || !d.Owner.IsLicensed || d.Owner.HwModel != pb.HardwareModel_HELTEC_V3 {
		t.Fatal(d.Owner)
	}
}

func TestImportOwnerLengthsCheckedBeforeConnecting(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "config", "import", writeBackup(t, "owner: "+strings.Repeat("x", 40)+"\n")); err == nil || !strings.Contains(err.Error(), "39 bytes") {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, d, "", "config", "import", writeBackup(t, "owner_short: ABCDE\n")); err == nil || !strings.Contains(err.Error(), "4 bytes") {
		t.Fatal(err)
	}
	if d.Dials() != 0 {
		t.Fatal("connected before validating", d.Dials())
	}
}

func TestImportKeysRefusesInvalidPrivateKey(t *testing.T) {
	pub := "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	cases := []struct{ priv, reason string }{
		{"base64:", "skipped: security.private_key (empty in file; would make the node generate a new identity)"},
		{"base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 16)), "skipped: security.private_key (16 bytes in file, not 32; would make the node generate a new identity)"},
	}
	for _, c := range cases {
		d := fakedevice.New()
		d.Config.Security.PrivateKey = bytes.Repeat([]byte{2}, 32)
		d.Config.Security.PublicKey = bytes.Repeat([]byte{3}, 32)
		y := "config: {security: {privateKey: '" + c.priv + "', publicKey: '" + pub + "'}, lora: {hopLimit: 6}}\n"
		_, stderr, err := runCLI(t, d, "", "config", "import", "--keys", writeBackup(t, y))
		if err != nil || d.Config.Lora.HopLimit != 6 {
			t.Fatal(err, stderr)
		}
		if !bytes.Equal(d.Config.Security.PrivateKey, bytes.Repeat([]byte{2}, 32)) || !bytes.Equal(d.Config.Security.PublicKey, bytes.Repeat([]byte{3}, 32)) {
			t.Fatal("keys applied", d.Config.Security)
		}
		if !strings.Contains(stderr, c.reason) || !strings.Contains(stderr, "skipped: security.public_key (") {
			t.Fatal(stderr)
		}
	}
}

func TestImportIgnoresLocationWithoutFixedPosition(t *testing.T) {
	d := fakedevice.New()
	y := "location: {lat: 59.9139, lon: 10.7522, alt: 23}\nconfig: {position: {fixedPosition: false}}\n"
	_, stderr, err := runCLI(t, d, "", "config", "import", writeBackup(t, y))
	if err != nil || !strings.Contains(stderr, "note: location ignored (position.fixed_position is false in this backup)") {
		t.Fatal(err, stderr)
	}
	for _, m := range d.Received() {
		if m.GetSetFixedPosition() != nil {
			t.Fatal("fixed position set")
		}
	}
}

func TestConfigSetRefusedOnManagedNode(t *testing.T) {
	d := fakedevice.New()
	d.Config.Security.IsManaged = true
	_, _, err := runCLI(t, d, "", "--yes", "config", "set", "lora.hop_limit", "5")
	if err == nil || err.Error() != "node is managed (security.is_managed); local configuration changes are ignored by the firmware" {
		t.Fatal(err)
	}
	if fakedevice.CountBegin(d.Received()) != 0 {
		t.Fatal("began an edit")
	}
	if _, _, err := runCLI(t, d, "", "--yes", "config", "set", "security.is_managed", "false"); err != nil || d.Config.Security.IsManaged {
		t.Fatal(err)
	}
}

func TestInterruptBeforeCommitReportsPartialEdit(t *testing.T) {
	d := fakedevice.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.OnAdmin = func(m *pb.AdminMessage) {
		if m.GetBeginEditSettings() {
			cancel()
			time.Sleep(20 * time.Millisecond)
		}
	}
	_, _, err := runCLICtx(t, ctx, d, nil, "config", "set", "lora.hop_limit", "5")
	if !errors.Is(err, device.ErrPartialEdit) {
		t.Fatal(err)
	}
}

func TestInterruptAtConfirmationAborts(t *testing.T) {
	d := fakedevice.New()
	d.Kind = transport.KindBLE
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	block := make(chan struct{})
	defer close(block)
	confirm := func(string) bool {
		cancel()
		<-block // the user never answers
		return true
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := runCLICtx(t, ctx, d, confirm, "config", "set", "bluetooth.enabled", "false")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "interrupted; nothing was changed") || !d.Config.Bluetooth.Enabled {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation prompt ignored the interrupt")
	}
}
