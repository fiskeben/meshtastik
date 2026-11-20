package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/fakedevice"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func b64(k []byte) string { return "base64:" + base64.StdEncoding.EncodeToString(k) }

// withKeyedCar is a fake whose node list has Car with public key 0x0c….
func withKeyedCar() *fakedevice.Device {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{LongName: "Car", ShortName: "CAR1", PublicKey: key(0x0c)}}}
	return d
}

func adminKeys(d *fakedevice.Device) [][]byte { return d.Config.GetSecurity().GetAdminKey() }

func TestAdminKeyAddByNode(t *testing.T) {
	d := withKeyedCar()
	out, _, err := runCLI(t, d, "", "admin-key", "add", "CAR1")
	if err != nil || !strings.Contains(out, "verified") || len(adminKeys(d)) != 1 || !bytes.Equal(adminKeys(d)[0], key(0x0c)) {
		t.Fatal(out, err, adminKeys(d))
	}
}

func TestAdminKeyAddBase64(t *testing.T) {
	d := withKeyedCar()
	if _, _, err := runCLI(t, d, "", "admin-key", "add", b64(key(1))); err != nil {
		t.Fatal(err)
	}
	plain := base64.StdEncoding.EncodeToString(key(2)) // without the prefix
	if _, _, err := runCLI(t, d, "", "admin-key", "add", plain); err != nil {
		t.Fatal(err)
	}
	if ks := adminKeys(d); len(ks) != 2 || !bytes.Equal(ks[0], key(1)) || !bytes.Equal(ks[1], key(2)) {
		t.Fatal(ks)
	}
}

func TestAdminKeyRejectsShortKey(t *testing.T) {
	d := withKeyedCar()
	_, _, err := runCLI(t, d, "", "admin-key", "add", "base64:"+base64.StdEncoding.EncodeToString([]byte{1, 2, 3}))
	if err == nil || !strings.Contains(err.Error(), "key must be 32 bytes, got 3") || len(d.Received()) != 0 {
		t.Fatal(err)
	}
}

func TestAdminKeyNodeWithoutKey(t *testing.T) {
	d := withKeyedCar()
	d.ExtraNodes[0].User.PublicKey = nil
	if _, _, err := runCLI(t, d, "", "admin-key", "add", "CAR1"); err == nil || !strings.Contains(err.Error(), "!0000002a has no known public key") {
		t.Fatal(err)
	}
}

func TestAdminKeyFourthRefused(t *testing.T) {
	d := withKeyedCar()
	d.Config.Security.AdminKey = [][]byte{key(1), key(2), key(3)}
	if _, _, err := runCLI(t, d, "", "admin-key", "add", "CAR1"); err == nil || !strings.Contains(err.Error(), "at most 3 admin keys") {
		t.Fatal(err)
	}
}

func TestAdminKeyAddTwiceIsNoop(t *testing.T) {
	d := withKeyedCar()
	d.Config.Security.AdminKey = [][]byte{key(0x0c)}
	out, _, err := runCLI(t, d, "", "admin-key", "add", "CAR1")
	if err != nil || !strings.Contains(out, "already listed") || len(d.Received()) != 0 {
		t.Fatal(out, err)
	}
}

func TestAdminKeyRemoveByIndexAndNode(t *testing.T) {
	d := withKeyedCar()
	d.Config.Security.AdminKey = [][]byte{key(1), key(0x0c), key(3)}
	if _, _, err := runCLI(t, d, "", "admin-key", "remove", "0"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, d, "", "admin-key", "remove", "CAR1"); err != nil {
		t.Fatal(err)
	}
	if ks := adminKeys(d); len(ks) != 1 || !bytes.Equal(ks[0], key(3)) {
		t.Fatal(ks)
	}
	if _, _, err := runCLI(t, d, "", "admin-key", "remove", "CAR1"); err == nil || !strings.Contains(err.Error(), "not one of the node's admin keys") {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, d, "", "admin-key", "remove", "2"); err == nil {
		t.Fatal("removed index 2 of a 1-key list")
	}
}

func TestAdminKeyListNamesNodes(t *testing.T) {
	d := withKeyedCar()
	out, _, err := runCLI(t, d, "", "admin-key", "list")
	if err != nil || out != "no admin keys\n" {
		t.Fatalf("%q %v", out, err)
	}
	d.Config.Security.AdminKey = [][]byte{key(0x0c), key(1)}
	out, _, err = runCLI(t, d, "", "admin-key", "list")
	if err != nil || !strings.Contains(row(out, b64(key(0x0c))), "!0000002a (Car)") || !strings.HasSuffix(strings.TrimSpace(row(out, b64(key(1)))), "-") {
		t.Fatalf("%q %v", out, err)
	}
	out, _, err = runCLI(t, d, "", "--json", "admin-key", "list")
	var rows []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 2 || rows[0]["node"] != "!0000002a" || rows[1]["node"] != nil || rows[1]["index"] != float64(1) {
		t.Fatal(out, err)
	}
}

func TestAdminKeyPlainBase64WrongLength(t *testing.T) {
	d := withKeyedCar()
	plain := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 33)) // 44 characters, 33 bytes
	if _, _, err := runCLI(t, d, "", "admin-key", "add", plain); err == nil || !strings.Contains(err.Error(), "key must be 32 bytes, got 33") {
		t.Fatal(err)
	}
}
