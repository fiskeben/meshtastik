package device_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

func hopNums(hs []device.Hop) []uint32 {
	var out []uint32
	for _, h := range hs {
		out = append(out, h.Num)
	}
	return out
}

func snrs(hs []device.Hop) []any {
	var out []any
	for _, h := range hs {
		if h.SNR == nil {
			out = append(out, nil)
		} else {
			out = append(out, *h.SNR)
		}
	}
	return out
}

func eq[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTracerouteBothWays(t *testing.T) {
	c, d, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Route, r.RouteBack = []uint32{7}, []uint32{8} })
	rt, err := c.Traceroute(ctxT(t), hill, 4)
	if err != nil {
		t.Fatal(err)
	}
	me := d.NodeNum
	if !eq(hopNums(rt.Towards), []uint32{me, 7, hill}) || !eq(snrs(rt.Towards), []any{nil, 10.0, 9.0}) {
		t.Fatal(hopNums(rt.Towards), snrs(rt.Towards))
	}
	if !eq(hopNums(rt.Back), []uint32{hill, 8, me}) || !eq(snrs(rt.Back), []any{nil, 10.0, 9.0}) {
		t.Fatal(hopNums(rt.Back), snrs(rt.Back))
	}
	if p := d.RemoteRequests(hill)[0]; p.GetHopLimit() != 4 || p.GetDecoded().GetPortnum() != pb.PortNum_TRACEROUTE_APP {
		t.Fatal(p)
	}
}

func TestTracerouteDirect(t *testing.T) {
	c, d, _ := withRemote(t, nil)
	rt, err := c.Traceroute(ctxT(t), hill, 3)
	if err != nil || !eq(hopNums(rt.Towards), []uint32{d.NodeNum, hill}) || !eq(snrs(rt.Towards), []any{nil, 10.0}) {
		t.Fatal(rt, err)
	}
}

func TestRouteFromDiscovery(t *testing.T) {
	const me, to = 1, 2
	// -128 is an unknown SNR; a short snr_towards list makes every SNR
	// unknown; a short snr_back list means the way back is not reported.
	rt := device.RouteFrom(me, to, &pb.RouteDiscovery{Route: []uint32{7}, SnrTowards: []int32{-128, 22}, RouteBack: []uint32{8}, SnrBack: []int32{4}}, 3)
	if !eq(snrs(rt.Towards), []any{nil, nil, 5.5}) || rt.Back != nil {
		t.Fatal(snrs(rt.Towards), rt.Back)
	}
	if rt := device.RouteFrom(me, to, &pb.RouteDiscovery{Route: []uint32{7}, SnrTowards: []int32{8}}, 3); !eq(snrs(rt.Towards), []any{nil, nil, nil}) {
		t.Fatal(snrs(rt.Towards))
	}
	// Without hop_start on the answer the way back is not reported.
	if rt := device.RouteFrom(me, to, &pb.RouteDiscovery{SnrTowards: []int32{8}, SnrBack: []int32{8}}, 0); rt.Back != nil {
		t.Fatal(rt.Back)
	}
}

func TestRequestTelemetryKinds(t *testing.T) {
	c, d, r := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.EnvironmentSensor, r.PowerSensor = true, true })
	for i, kind := range []string{"device", "environment", "power", "local-stats"} {
		k, err := device.ParseTelemetryKind(kind)
		if err != nil {
			t.Fatal(err)
		}
		tm, err := c.RequestTelemetry(ctxT(t), hill, k)
		if err != nil {
			t.Fatal(kind, err)
		}
		var sent pb.Telemetry
		if err := proto.Unmarshal(d.RemoteRequests(hill)[i].GetDecoded().GetPayload(), &sent); err != nil {
			t.Fatal(err)
		}
		want := map[string]bool{
			"device":      sent.GetDeviceMetrics() != nil && tm.GetDeviceMetrics().GetBatteryLevel() == r.Telemetry.GetDeviceMetrics().GetBatteryLevel(),
			"environment": sent.GetEnvironmentMetrics() != nil && tm.GetEnvironmentMetrics() != nil,
			"power":       sent.GetPowerMetrics() != nil && tm.GetPowerMetrics() != nil,
			"local-stats": sent.GetLocalStats() != nil && tm.GetLocalStats() != nil,
		}
		if !want[kind] {
			t.Fatalf("%s: sent %v, got %v", kind, &sent, tm)
		}
	}
	if _, err := device.ParseTelemetryKind("weather"); err == nil {
		t.Fatal("accepted weather")
	}
}

func TestRequestPosition(t *testing.T) {
	c, _, r := withRemote(t, nil)
	pos, err := c.RequestPosition(ctxT(t), hill)
	if err != nil || pos.GetLatitudeI() != r.Position.GetLatitudeI() || pos.GetLongitudeI() != r.Position.GetLongitudeI() {
		t.Fatal(pos, err)
	}
}

func TestPositionAndTelemetryWaitTwiceMeshTimeout(t *testing.T) {
	c, _, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Unreachable = true }) // MeshTimeout 200ms
	start := time.Now()
	if _, err := c.RequestPosition(ctxT(t), hill); err == nil || time.Since(start) < 400*time.Millisecond {
		t.Fatal("position gave up after", time.Since(start), err)
	}
	start = time.Now()
	if _, err := c.RequestTelemetry(ctxT(t), hill, device.DeviceMetrics); err == nil || time.Since(start) < 400*time.Millisecond {
		t.Fatal("telemetry gave up after", time.Since(start), err)
	}
}

func TestRequestTelemetryWithoutSensor(t *testing.T) {
	c, _, _ := withRemote(t, nil)
	_, err := c.RequestTelemetry(ctxT(t), hill, device.EnvironmentMetrics)
	var re *device.RoutingError
	if !errors.As(err, &re) || re.Reason != pb.Routing_NO_RESPONSE || !strings.Contains(err.Error(), "!00000099 received the request but did not answer it") {
		t.Fatal(err)
	}
}

func TestRequestPositionWithoutPosition(t *testing.T) {
	c, _, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Position = nil })
	if _, err := c.RequestPosition(ctxT(t), hill); err == nil || !strings.Contains(err.Error(), "did not answer it") {
		t.Fatal(err)
	}
}
