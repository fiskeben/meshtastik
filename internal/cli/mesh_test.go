package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

const hillNum = 0x99

// withHill is a fake that reaches remote node Hill through relay node 7.
func withHill(setup func(*fakedevice.Remote)) *fakedevice.Device {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 7, User: &pb.User{LongName: "Relay", ShortName: "RLY1"}}}
	r := fakedevice.NewRemote(hillNum, "Hill", "HIL1")
	r.AdminKeys = [][]byte{d.PublicKey}
	r.Route, r.RouteBack = []uint32{7}, []uint32{7}
	if setup != nil {
		setup(r)
	}
	d.Remotes = map[uint32]*fakedevice.Remote{hillNum: r}
	return d
}

func TestTracerouteOutput(t *testing.T) {
	out, errOut, err := runCLI(t, withHill(nil), "", "traceroute", "HIL1")
	want := "towards:  me → Relay (RLY1) 10.0 dB → Hill (HIL1) 9.0 dB\n" +
		"back:     Hill (HIL1) → Relay (RLY1) 10.0 dB → me 9.0 dB\n"
	if err != nil || out != want || !strings.Contains(errOut, "asking !00000099 (Hill) — this can take a while") {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
}

func TestTracerouteHopLimit(t *testing.T) {
	d := withHill(nil)
	if _, _, err := runCLI(t, d, "", "traceroute", "HIL1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, d, "", "traceroute", "--hop-limit", "6", "HIL1"); err != nil {
		t.Fatal(err)
	}
	ps := d.RemoteRequests(hillNum)
	if ps[0].GetHopLimit() != 3 || ps[1].GetHopLimit() != 6 { // default: the node's lora.hop_limit
		t.Fatal(ps[0].GetHopLimit(), ps[1].GetHopLimit())
	}
	if _, _, err := runCLI(t, d, "", "traceroute", "--hop-limit", "8", "HIL1"); err == nil {
		t.Fatal("hop limit 8 accepted")
	}
}

func TestTracerouteJSON(t *testing.T) {
	out, _, err := runCLI(t, withHill(nil), "", "--json", "traceroute", "HIL1")
	var m struct {
		Towards []struct {
			ID   string   `json:"id"`
			Name string   `json:"name"`
			SNR  *float64 `json:"snr"`
		} `json:"towards"`
		Back []any `json:"back"`
	}
	if err != nil || json.Unmarshal([]byte(out), &m) != nil {
		t.Fatal(out, err)
	}
	if len(m.Towards) != 3 || m.Towards[0].SNR != nil || m.Towards[1].Name != "Relay (RLY1)" || *m.Towards[2].SNR != 9 ||
		m.Towards[2].ID != "!00000099" || len(m.Back) != 3 {
		t.Fatal(out)
	}
}

func TestRouteTextUnknownSNRAndNoBack(t *testing.T) {
	st := &device.State{MyInfo: &pb.MyNodeInfo{MyNodeNum: 1}, Nodes: map[uint32]*pb.NodeInfo{}}
	var b strings.Builder
	writeRoute(&b, st, &device.Route{Towards: []device.Hop{{Num: 1}, {Num: 2}}})
	if b.String() != "towards:  me → !00000002 ?\nback:     not reported\n" {
		t.Fatalf("%q", b.String())
	}
}

func TestTracerouteNoRoute(t *testing.T) {
	d := withHill(func(r *fakedevice.Remote) { r.Unreachable, r.FailWith = true, pb.Routing_MAX_RETRANSMIT })
	if _, _, err := runCLI(t, d, "", "traceroute", "HIL1"); err == nil || !strings.Contains(err.Error(), "no route to !00000099") {
		t.Fatal(err)
	}
}

func TestRequestPositionOutput(t *testing.T) {
	out, _, err := runCLI(t, withHill(nil), "", "request", "position", "HIL1")
	if err != nil || !strings.Contains(out, "latitude:   59.6000000\n") || !strings.Contains(out, "longitude:  10.7000000\n") {
		t.Fatalf("%q %v", out, err)
	}
	out, _, err = runCLI(t, withHill(nil), "", "--json", "request", "position", "HIL1")
	var m map[string]any
	if err != nil || json.Unmarshal([]byte(out), &m) != nil || m["latitude_i"] != float64(596000000) {
		t.Fatal(out, err)
	}
}

func TestRequestTelemetryOutput(t *testing.T) {
	out, _, err := runCLI(t, withHill(nil), "", "request", "telemetry", "HIL1")
	if err != nil || !strings.Contains(out, "battery_level: 87\n") {
		t.Fatalf("%q %v", out, err)
	}
	d := withHill(nil)
	if _, _, err := runCLI(t, d, "", "request", "telemetry", "--type", "environment", "HIL1"); err != nil {
		t.Fatal(err)
	}
	var sent pb.Telemetry
	if err := proto.Unmarshal(d.RemoteRequests(hillNum)[0].GetDecoded().GetPayload(), &sent); err != nil || sent.GetEnvironmentMetrics() == nil {
		t.Fatal(&sent, err)
	}
	if _, _, err := runCLI(t, d, "", "request", "telemetry", "--type", "weather", "HIL1"); err == nil {
		t.Fatal("accepted weather")
	}
}

func TestTracerouteThroughDaemon(t *testing.T) {
	td := newTestDaemon(t, withHill(nil))
	out, _, err := td.run(t, context.Background(), "traceroute", "HIL1")
	if err != nil || !strings.HasPrefix(out, "towards:  me → Relay (RLY1)") {
		t.Fatal(out, err)
	}
}
