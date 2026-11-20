package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	d = carAndBus() // the declined run connected too
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

func TestNodesOpRefusesUnlistedNode(t *testing.T) {
	for _, op := range []string{"remove", "favorite", "unfavorite", "unignore"} {
		d := carAndBus()
		_, _, err := runCLI(t, d, "y", "nodes", op, "!deadbeef")
		if err == nil || !strings.Contains(err.Error(), "!deadbeef is not in the node list") {
			t.Fatal(op, err)
		}
		if len(d.Received()) != 0 {
			t.Fatal(op, "sent something")
		}
	}
}

func TestNodesIgnoreUnlistedNode(t *testing.T) {
	d := carAndBus() // firmware 2.8 adds an ignored node it has not heard yet
	out, _, err := runCLI(t, d, "", "nodes", "ignore", "!deadbeef")
	if err != nil || !strings.Contains(out, "ignore !deadbeef: done") {
		t.Fatal(out, err)
	}
	d = carAndBus()
	d.IgnoreNodeOps = true // older firmware ignores only nodes it lists
	_, _, err = runCLI(t, d, "", "nodes", "ignore", "!deadbeef")
	if err == nil || !strings.Contains(err.Error(), "older firmware can only ignore nodes it already lists") {
		t.Fatal(err)
	}
}

func TestResolveNodePrefersNameOverUnlistedNumber(t *testing.T) {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 99, User: &pb.User{LongName: "Base", ShortName: "0042"}}, {Num: 7, User: &pb.User{ShortName: "X"}}}
	out, _, err := runCLI(t, d, "", "nodes", "favorite", "0042")
	if err != nil || !strings.Contains(out, "!00000063") {
		t.Fatal(out, err)
	}
	out, _, err = runCLI(t, d, "", "nodes", "favorite", "7") // a listed number still wins
	if err != nil || !strings.Contains(out, "!00000007") {
		t.Fatal(out, err)
	}
}

func TestNodesResetThroughDaemon(t *testing.T) {
	d := carAndBus()
	d.RebootDelay = 200 * time.Millisecond
	td := newTestDaemon(t, d)
	out, _, err := td.run(t, context.Background(), "--yes", "nodes", "reset")
	if err != nil || !strings.Contains(out, "rebooted") || d.Dials() != 2 {
		t.Fatal(out, err, d.Dials())
	}
	out, _, err = td.run(t, context.Background(), "nodes")
	if err != nil || strings.Contains(out, "Car") || !strings.Contains(out, "Test Node") {
		t.Fatal(out, err)
	}
}
