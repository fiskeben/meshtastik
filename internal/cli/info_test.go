package cli

import (
	"encoding/json"
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
