package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/fakedevice"
)

func TestInfoHuman(t *testing.T) {
	out, _, err := runCLI(t, fakedevice.New(), "", "info")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Test Node", "TEST", "!1234abcd", "2.7.15", "EU_868"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestInfoJSON(t *testing.T) {
	out, _, _ := runCLI(t, fakedevice.New(), "", "info", "--json")
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil || m["id"] != "!1234abcd" || m["region"] != "EU_868" {
		t.Fatal(out, err)
	}
	if m["channels"] != float64(1) || m["battery_level"] != nil {
		t.Fatal(out)
	}
}

func TestNodesSortedByLastHeard(t *testing.T) {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{
		{Num: 1, LastHeard: 100, User: &pb.User{LongName: "old"}},
		{Num: 2, LastHeard: 200, User: &pb.User{LongName: "new"}},
	}
	out, _, _ := runCLI(t, d, "", "nodes")
	if strings.Index(out, "new") > strings.Index(out, "old") {
		t.Fatal(out)
	}
	out, _, _ = runCLI(t, d, "", "nodes", "--json")
	var arr []map[string]any
	if json.Unmarshal([]byte(out), &arr) != nil || len(arr) != 3 {
		t.Fatal(out)
	}
	if arr[0]["long_name"] != "new" || arr[0]["battery_level"] != nil {
		t.Fatal(out)
	}
}

func TestEmptyConnFlagRejected(t *testing.T) {
	_, _, err := runCLI(t, fakedevice.New(), "", "info", "--host", "")
	if err == nil || !strings.Contains(err.Error(), "--host") {
		t.Fatal(err)
	}
}

func TestInfoShowsPublicKey(t *testing.T) {
	d := fakedevice.New()
	key := bytes.Repeat([]byte{0xab}, 32)
	d.Config.Security.PublicKey = key
	want := "base64:" + base64.StdEncoding.EncodeToString(key)
	out, _, err := runCLI(t, d, "", "info")
	if err != nil || !regexp.MustCompile(`(?m)^Public key:\s+`+regexp.QuoteMeta(want)+`$`).MatchString(out) {
		t.Fatal(out, err)
	}
	out, _, err = runCLI(t, d, "", "--json", "info")
	var m map[string]any
	if err != nil || json.Unmarshal([]byte(out), &m) != nil || m["public_key"] != want {
		t.Fatal(out, err)
	}
	d.Config.Security.PublicKey = nil
	out, _, _ = runCLI(t, d, "", "info")
	if !regexp.MustCompile(`(?m)^Public key:\s+-$`).MatchString(out) {
		t.Fatal(out)
	}
}
