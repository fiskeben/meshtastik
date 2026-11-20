package device

import (
	"context"
	"fmt"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// unknownSNR marks a hop whose SNR was not measured.
const unknownSNR = -128

// Hop is one node on a traced route with the SNR (dB) at which it heard the
// previous hop; nil when unknown or for the route's first node.
type Hop struct {
	Num uint32
	SNR *float64
}

// Route is a traced route: Towards runs from this node to the target, Back
// from the target to this node (nil when the target did not report it).
type Route struct {
	Towards []Hop
	Back    []Hop
}

// hops builds from → route… → to; snr has one entry per hop after from, or
// is ignored.
func hops(from uint32, route []uint32, to uint32, snr []int32) []Hop {
	out := []Hop{{Num: from}}
	nums := append(append([]uint32{}, route...), to)
	valid := len(snr) == len(nums)
	for i, n := range nums {
		h := Hop{Num: n}
		if valid && snr[i] != unknownSNR {
			v := float64(snr[i]) / 4
			h.SNR = &v
		}
		out = append(out, h)
	}
	return out
}

// RouteFrom assembles the route in a traceroute answer from node to to node
// me; hopStart is the answer packet's hop_start (0: the way back is not
// reported).
func RouteFrom(me, to uint32, rd *pb.RouteDiscovery, hopStart uint32) *Route {
	r := &Route{Towards: hops(me, rd.GetRoute(), to, rd.GetSnrTowards())}
	if hopStart != 0 && len(rd.GetSnrBack()) == len(rd.GetRouteBack())+1 {
		r.Back = hops(to, rd.GetRouteBack(), me, rd.GetSnrBack())
	}
	return r
}

// Traceroute traces the route to node to and back, within hopLimit hops.
func (c *Client) Traceroute(ctx context.Context, to uint32, hopLimit uint32) (*Route, error) {
	p, err := c.Request(ctx, to, pb.PortNum_TRACEROUTE_APP, &pb.RouteDiscovery{},
		RequestOptions{HopLimit: hopLimit, Timeout: 2 * c.opts.MeshTimeout})
	if err != nil {
		return nil, err
	}
	var rd pb.RouteDiscovery
	if err := proto.Unmarshal(p.GetDecoded().GetPayload(), &rd); err != nil {
		return nil, fmt.Errorf("decode traceroute answer: %w", err)
	}
	return RouteFrom(c.State().MyInfo.GetMyNodeNum(), to, &rd, p.GetHopStart()), nil
}

// TelemetryKind selects the telemetry a node is asked for.
type TelemetryKind int

const (
	DeviceMetrics TelemetryKind = iota
	EnvironmentMetrics
	PowerMetrics
	LocalStats
)

var telemetryKinds = []string{"device", "environment", "power", "local-stats"}

func (k TelemetryKind) String() string { return telemetryKinds[k] }

// ParseTelemetryKind parses "device", "environment", "power" or "local-stats".
func ParseTelemetryKind(s string) (TelemetryKind, error) {
	for i, name := range telemetryKinds {
		if s == name {
			return TelemetryKind(i), nil
		}
	}
	return 0, fmt.Errorf("unknown telemetry type %q (want device, environment, power or local-stats)", s)
}

// RequestTelemetry asks node to for its telemetry of the given kind.
func (c *Client) RequestTelemetry(ctx context.Context, to uint32, kind TelemetryKind) (*pb.Telemetry, error) {
	req := &pb.Telemetry{}
	switch kind {
	case DeviceMetrics:
		req.Variant = &pb.Telemetry_DeviceMetrics{DeviceMetrics: &pb.DeviceMetrics{}}
	case EnvironmentMetrics:
		req.Variant = &pb.Telemetry_EnvironmentMetrics{EnvironmentMetrics: &pb.EnvironmentMetrics{}}
	case PowerMetrics:
		req.Variant = &pb.Telemetry_PowerMetrics{PowerMetrics: &pb.PowerMetrics{}}
	case LocalStats:
		req.Variant = &pb.Telemetry_LocalStats{LocalStats: &pb.LocalStats{}}
	}
	p, err := c.Request(ctx, to, pb.PortNum_TELEMETRY_APP, req, RequestOptions{})
	if err != nil {
		return nil, err
	}
	var t pb.Telemetry
	if err := proto.Unmarshal(p.GetDecoded().GetPayload(), &t); err != nil {
		return nil, fmt.Errorf("decode telemetry: %w", err)
	}
	return &t, nil
}

// RequestPosition asks node to for its position.
func (c *Client) RequestPosition(ctx context.Context, to uint32) (*pb.Position, error) {
	p, err := c.Request(ctx, to, pb.PortNum_POSITION_APP, &pb.Position{}, RequestOptions{})
	if err != nil {
		return nil, err
	}
	var pos pb.Position
	if err := proto.Unmarshal(p.GetDecoded().GetPayload(), &pos); err != nil {
		return nil, fmt.Errorf("decode position: %w", err)
	}
	return &pos, nil
}
