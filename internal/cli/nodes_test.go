package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/fakedevice"
)

func carAndBus() *fakedevice.Device {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{
		{Num: 42, User: &pb.User{LongName: "Car", ShortName: "CAR1"}},
		{Num: 43, User: &pb.User{LongName: "Bus", ShortName: "BUS2"}},
	}
	return d
}

// row returns the line of a nodes table that mentions name.
func row(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, name) {
			return l
		}
	}
	return ""
}

func TestNodesFavoriteShowsInList(t *testing.T) {
	d := carAndBus()
	out, _, err := runCLI(t, d, "", "nodes", "favorite", "CAR1")
	if err != nil || !strings.Contains(out, "favorite !0000002a (Car): done") {
		t.Fatal(out, err)
	}
	out, _, err = runCLI(t, d, "", "nodes")
	if err != nil || !strings.Contains(out, "FLAGS") || !strings.HasSuffix(strings.TrimSpace(row(out, "Car")), "fav") ||
		!strings.HasSuffix(strings.TrimSpace(row(out, "Bus")), "-") {
		t.Fatal(out, err)
	}
	out, _, err = runCLI(t, d, "", "--json", "nodes")
	var rows []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil {
		t.Fatal(out, err)
	}
	for _, r := range rows {
		if r["id"] == "!0000002a" && (r["is_favorite"] != true || r["is_ignored"] != false) {
			t.Fatal(r)
		}
	}
	if _, _, err := runCLI(t, d, "", "nodes", "ignore", "CAR1"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = runCLI(t, d, "", "nodes")
	if !strings.HasSuffix(strings.TrimSpace(row(out, "Car")), "fav,ignored") {
		t.Fatal(out)
	}
}

func TestNodesRemoveSeveralAsks(t *testing.T) {
	d := carAndBus()
	if _, _, err := runCLI(t, d, "n", "nodes", "remove", "CAR1", "BUS2"); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatal(err)
	}
	if len(d.Received()) != 0 {
		t.Fatal("sent before confirming")
	}
	out, _, err := runCLI(t, d, "y", "nodes", "remove", "CAR1", "BUS2")
	if err != nil || strings.Count(out, ": done") != 2 {
		t.Fatal(out, err)
	}
	out, _, _ = runCLI(t, d, "", "nodes")
	if strings.Contains(out, "Car") || strings.Contains(out, "Bus") {
		t.Fatal(out)
	}
}

func TestNodesUnknownNodeSendsNothing(t *testing.T) {
	d := carAndBus()
	if _, _, err := runCLI(t, d, "y", "nodes", "remove", "CAR1", "NOPE"); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatal(err)
	}
	if len(d.Received()) != 0 {
		t.Fatal("sent before resolving every node")
	}
}

func TestNodesResetAsksAndReboots(t *testing.T) {
	d := carAndBus()
	if _, _, err := runCLI(t, d, "n", "nodes", "reset"); err == nil {
		t.Fatal("reset without confirmation")
	}
	out, _, err := runCLI(t, d, "y", "nodes", "reset", "--keep-favorites")
	if err != nil || !strings.Contains(out, "reboot") || !d.LastAdmin().GetNodedbReset() || d.Dials() != 2 {
		t.Fatal(out, err, d.Dials())
	}
}

func TestNodesThroughDaemon(t *testing.T) {
	d := carAndBus()
	td := newTestDaemon(t, d)
	if _, _, err := td.run(t, context.Background(), "nodes", "favorite", "CAR1"); err != nil {
		t.Fatal(err)
	}
	out, _, err := td.run(t, context.Background(), "nodes")
	if err != nil || !strings.HasSuffix(strings.TrimSpace(row(out, "Car")), "fav") {
		t.Fatal(out, err)
	}
}
