package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/fakedevice"
)

// hill returns the fake's remote node Hill.
func hill(d *fakedevice.Device) *fakedevice.Remote { return d.Remotes[hillNum] }

func TestDestConfigSetAndGet(t *testing.T) {
	d := withHill(nil)
	out, errOut, err := runCLI(t, d, "", "--dest", "HIL1", "config", "set", "lora.hop_limit", "5")
	if err != nil || !strings.Contains(out, "Applied 1 change(s); verified.") || !strings.Contains(errOut, "!00000099 (Hill)") {
		t.Fatal(out, errOut, err)
	}
	if hill(d).Config.GetLora().GetHopLimit() != 5 || d.Config.GetLora().GetHopLimit() == 5 {
		t.Fatal("wrong node changed")
	}
	out, _, err = runCLI(t, d, "", "--dest", "HIL1", "config", "get", "lora.hop_limit")
	if err != nil || out != "5\n" {
		t.Fatalf("%q %v", out, err)
	}
	for _, m := range d.RemoteAdmin(hillNum) {
		if c := m.GetGetConfigRequest(); m.GetGetConfigRequest() != pb.AdminMessage_LORA_CONFIG && fakedevice.OneofName(m) == "get_config_request" &&
			c != pb.AdminMessage_SESSIONKEY_CONFIG {
			t.Fatal("read more than the lora section:", m)
		}
	}
}

func TestDestInfo(t *testing.T) {
	d := withHill(nil)
	out, _, err := runCLI(t, d, "", "--dest", "!00000099", "info")
	if err != nil || !strings.Contains(out, "Hill") || !strings.Contains(out, "!00000099") || !strings.Contains(out, "RAK4631") ||
		!strings.Contains(out, keyText(hill(d).Owner.GetPublicKey())) {
		t.Fatal(out, err)
	}
	out, _, err = runCLI(t, d, "", "--json", "--dest", "HIL1", "info")
	var m map[string]any
	if err != nil || json.Unmarshal([]byte(out), &m) != nil || m["remote"] != true || m["id"] != "!00000099" {
		t.Fatal(out, err)
	}
}

func TestDestOwnerSet(t *testing.T) {
	d := withHill(nil)
	if _, _, err := runCLI(t, d, "", "--dest", "HIL1", "owner", "set", "Hilltop"); err != nil {
		t.Fatal(err)
	}
	if o := hill(d).Owner; o.GetLongName() != "Hilltop" || o.GetShortName() != "HIL1" {
		t.Fatal(o)
	}
}

func TestDestChannelList(t *testing.T) {
	d := withHill(func(r *fakedevice.Remote) {
		r.Channels[1] = &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "ridge"}}
	})
	out, _, err := runCLI(t, d, "", "--dest", "HIL1", "channel", "list")
	if err != nil || !strings.Contains(out, "ridge") {
		t.Fatal(out, err)
	}
}

func TestDestExportHasNoPrivateKey(t *testing.T) {
	d := withHill(func(r *fakedevice.Remote) { r.Config.Security.PrivateKey = bytes.Repeat([]byte{5}, 32) })
	out, errOut, err := runCLI(t, d, "", "--dest", "HIL1", "config", "export")
	if err != nil || !strings.Contains(out, "owner: Hill") || strings.Contains(out, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32))) ||
		!strings.Contains(errOut, "a remote node never sends its private key; the backup has none") {
		t.Fatal(out, errOut, err)
	}
}

func TestDestImportKeysRefused(t *testing.T) {
	_, _, err := runCLI(t, withHill(nil), "", "--dest", "HIL1", "config", "import", "--keys", "nonexistent.yaml")
	if err == nil || !strings.Contains(err.Error(), "--keys cannot be used with --dest") {
		t.Fatal(err)
	}
}

func TestDestUnauthorizedError(t *testing.T) {
	d := withHill(func(r *fakedevice.Remote) { r.Config.Security.AdminKey = nil })
	_, _, err := runCLI(t, d, "", "--dest", "HIL1", "info")
	if err == nil || !strings.Contains(err.Error(), "admin-key add") {
		t.Fatal(err)
	}
}

func TestDestRiskAsks(t *testing.T) {
	d := withHill(nil)
	_, errOut, err := runCLI(t, d, "n", "--dest", "HIL1", "config", "set", "lora.region", "US")
	if err == nil || !strings.Contains(errOut, "lora.region") || hill(d).Config.GetLora().GetRegion() != pb.Config_LoRaConfig_EU_868 {
		t.Fatal(errOut, err)
	}
	if _, _, err := runCLI(t, d, "y", "--dest", "HIL1", "config", "set", "lora.region", "US"); err != nil || hill(d).Config.GetLora().GetRegion() != pb.Config_LoRaConfig_US {
		t.Fatal(err)
	}
}

func TestDestSelfIsLocal(t *testing.T) {
	d := withHill(nil)
	out, _, err := runCLI(t, d, "", "--dest", "TEST", "config", "set", "lora.hop_limit", "6")
	if err != nil || !strings.Contains(out, "verified") || d.Config.GetLora().GetHopLimit() != 6 || len(d.RemoteRequests(hillNum)) != 0 {
		t.Fatal(out, err)
	}
}

func TestDestRefusedBySend(t *testing.T) {
	for _, args := range [][]string{{"send", "hi"}, {"listen"}, {"nodes"}, {"traceroute", "HIL1"}} {
		_, _, err := runCLI(t, withHill(nil), "", append([]string{"--dest", "HIL1"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "--dest is not supported by meshtastik "+args[0]) {
			t.Fatal(args, err)
		}
	}
}

func TestDestAdminKeyAdd(t *testing.T) {
	d := withHill(func(r *fakedevice.Remote) { r.FirmwareVersion = "2.8.2.d5f1619" }) // older firmware refuses remote security writes
	d.ExtraNodes = append(d.ExtraNodes, &pb.NodeInfo{Num: 42, User: &pb.User{LongName: "Car", ShortName: "CAR1", PublicKey: key(0x0c)}})
	if _, _, err := runCLI(t, d, "", "--dest", "HIL1", "admin-key", "add", "CAR1"); err != nil {
		t.Fatal(err)
	}
	if ks := hill(d).Config.GetSecurity().GetAdminKey(); len(ks) != 2 || !bytes.Equal(ks[1], key(0x0c)) {
		t.Fatal(ks)
	}
	out, _, err := runCLI(t, d, "", "--dest", "HIL1", "admin-key", "list")
	if err != nil || !strings.Contains(out, "!0000002a (Car)") || !strings.Contains(out, "Test Node") {
		t.Fatal(out, err)
	}
	// Removing the key that lets this node in asks first.
	if _, _, err := runCLI(t, d, "n", "--dest", "HIL1", "admin-key", "remove", "0"); err == nil {
		t.Fatal("removed this node's own admin key without asking")
	}
}

func TestDestNodesFavoriteIsSentNotVerified(t *testing.T) {
	d := withHill(nil)
	out, _, err := runCLI(t, d, "", "--dest", "HIL1", "nodes", "favorite", "RLY1")
	if err != nil || !strings.Contains(out, "favorite !00000007 (Relay): sent (a remote node list cannot be read back)") {
		t.Fatal(out, err)
	}
	ms := d.RemoteAdmin(hillNum)
	if ms[len(ms)-1].GetSetFavoriteNode() != 7 {
		t.Fatal(ms[len(ms)-1])
	}
}

func TestDestShutdownWarnsRemote(t *testing.T) {
	var prompt string
	d := withHill(nil)
	_, _, err := runCLICtx(t, context.Background(), d, func(p string) bool { prompt = p; return false }, "--dest", "HIL1", "shutdown")
	if err == nil || !strings.Contains(prompt, "cannot be powered on remotely") {
		t.Fatal(prompt, err)
	}
}

func TestDestThroughDaemon(t *testing.T) {
	d := withHill(nil)
	td := newTestDaemon(t, d)
	if _, _, err := td.run(t, context.Background(), "--dest", "HIL1", "config", "set", "lora.hop_limit", "4"); err != nil {
		t.Fatal(err)
	}
	if hill(d).Config.GetLora().GetHopLimit() != 4 {
		t.Fatal(hill(d).Config.GetLora())
	}
}
