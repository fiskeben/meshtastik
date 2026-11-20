package fakedevice

import (
	"bytes"
	"crypto/rand"
	"slices"
	"strings"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"meshtastik/internal/configpath"
)

// Remote is a node reached through the fake's radio. Set fields before the
// first Dial; they are read under the device lock.
type Remote struct {
	Num             uint32
	Owner           *pb.User // Owner.PublicKey is what the fake's node list knows of it
	FirmwareVersion string
	Config          *pb.LocalConfig
	ModuleConfig    *pb.LocalModuleConfig
	Channels        [8]*pb.Channel
	Route           []uint32      // traceroute hops towards it (intermediate nodes)
	RouteBack       []uint32      // traceroute hops back
	Position        *pb.Position  // nil: no position to share (requests get NO_RESPONSE)
	Telemetry       *pb.Telemetry // answered for device metrics requests
	// EnvironmentSensor, PowerSensor: without them, environment and power
	// telemetry requests get NO_RESPONSE, as on a node lacking the sensor.
	EnvironmentSensor, PowerSensor bool

	Unreachable bool // drop everything addressed to it
	// FailWith, with Unreachable: the local node answers this routing
	// error. MAX_RETRANSMIT and TIMEOUT need want_ack (only acked packets
	// are retransmitted); other packets then vanish.
	FailWith pb.Routing_Error
	// DropFirst loses this many packets on the way to it. As with real
	// firmware, a lost want_ack packet ends in a local MAX_RETRANSMIT after
	// the radio's retries; other packets vanish.
	DropFirst int
	// DropAcks: handle, but lose this many acks. Firmware acks only packets
	// no module answered (an answer is the ack). DropAcksFor, when set,
	// limits this to admin messages whose type starts with it ("set_").
	DropAcks    int
	DropAcksFor string
	// RotatePasskeyOnce: after the first response, silently invalidate the
	// issued passkey once, as when it expires.
	RotatePasskeyOnce bool
	Decoys            bool     // before each answer, emit one with a wrong request id and one from another node
	IgnoreFields      []string // config paths whose new value is silently dropped when applied
	RebootOnCommit    bool     // after commit: unreachable for RebootFor (want_ack packets get a local MAX_RETRANSMIT)
	RebootFor         time.Duration
	Latency           time.Duration // before each answer

	passkey   []byte
	rotated   bool
	open      bool               // edit transaction open
	pending   []*pb.AdminMessage // sets held until commit
	downUntil time.Time
	requests  []*pb.MeshPacket
	adminMsgs []*pb.AdminMessage
}

// NewRemote returns a remote node with the fake's default configuration
// and a random public key.
func NewRemote(num uint32, name, short string) *Remote {
	d := New()
	key := make([]byte, 32)
	rand.Read(key)
	r := &Remote{Num: num, FirmwareVersion: d.FirmwareVersion, Config: d.Config, ModuleConfig: d.ModuleConfig,
		Channels: d.Channels, Position: &pb.Position{LatitudeI: proto.Int32(596000000), LongitudeI: proto.Int32(107000000)},
		Telemetry: &pb.Telemetry{Variant: &pb.Telemetry_DeviceMetrics{DeviceMetrics: &pb.DeviceMetrics{BatteryLevel: proto.Uint32(87)}}},
		Owner:     &pb.User{LongName: name, ShortName: short, HwModel: pb.HardwareModel_RAK4631, PublicKey: key}}
	r.Config.Security.PublicKey = key
	return r
}

// RemoteAdmin returns copies of the admin messages that reached remote num.
func (d *Device) RemoteAdmin(num uint32) []*pb.AdminMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*pb.AdminMessage
	if r := d.Remotes[num]; r != nil {
		for _, m := range r.adminMsgs {
			out = append(out, proto.Clone(m).(*pb.AdminMessage))
		}
	}
	return out
}

// RemoteRequests returns copies of every packet addressed to remote num.
func (d *Device) RemoteRequests(num uint32) []*pb.MeshPacket {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*pb.MeshPacket
	if r := d.Remotes[num]; r != nil {
		for _, p := range r.requests {
			out = append(out, proto.Clone(p).(*pb.MeshPacket))
		}
	}
	return out
}

// isRemote reports whether p is addressed to a remote node; d.mu must be held.
func (d *Device) isRemote(p *pb.MeshPacket) bool {
	_, ok := d.Remotes[p.GetTo()]
	return ok && p.GetTo() != d.NodeNum
}

func packet(from, to, requestID uint32, port pb.PortNum, m proto.Message) *pb.FromRadio {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	return &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{From: from, To: to,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: port, Payload: b, RequestId: requestID}}}}}
}

func routingReply(from, to, requestID uint32, reason pb.Routing_Error) *pb.FromRadio {
	return packet(from, to, requestID, pb.PortNum_ROUTING_APP, &pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: reason}})
}

// toRemote handles a packet addressed to a remote node: it records it and
// emits the remote's answers on l after the remote's latency.
func (d *Device) toRemote(l *link, p *pb.MeshPacket) {
	d.mu.Lock()
	r := d.Remotes[p.GetTo()]
	r.requests = append(r.requests, proto.Clone(p).(*pb.MeshPacket))
	out, latency := d.remoteAnswers(r, p), r.Latency
	d.mu.Unlock()
	if len(out) == 0 {
		return
	}
	go func() {
		time.Sleep(latency)
		for _, m := range out {
			if !l.emit(m) {
				return
			}
		}
	}()
}

// retransmitted reports whether reason is what the local node reports when
// its own retransmissions of a want_ack packet went unanswered.
func retransmitted(reason pb.Routing_Error) bool {
	return reason == pb.Routing_MAX_RETRANSMIT || reason == pb.Routing_TIMEOUT
}

// remoteAnswers works out what comes back for p; d.mu must be held.
func (d *Device) remoteAnswers(r *Remote, p *pb.MeshPacket) []*pb.FromRadio {
	me, id, data := d.NodeNum, p.GetId(), p.GetDecoded()
	lost := func() []*pb.FromRadio { // the packet never got there
		if p.GetWantAck() {
			return []*pb.FromRadio{routingReply(me, me, id, pb.Routing_MAX_RETRANSMIT)}
		}
		return nil
	}
	switch {
	case r.Unreachable:
		if r.FailWith == pb.Routing_NONE || retransmitted(r.FailWith) && !p.GetWantAck() {
			return nil
		}
		return []*pb.FromRadio{routingReply(me, me, id, r.FailWith)}
	case time.Now().Before(r.downUntil):
		return lost()
	case r.DropFirst > 0:
		r.DropFirst--
		return lost()
	}
	var out []*pb.FromRadio
	answered := false
	answer := func(port pb.PortNum, m proto.Message) {
		if r.Decoys {
			out = append(out, packet(r.Num, me, id+1, port, m), packet(r.Num+1, me, id, port, m))
		}
		a := packet(r.Num, me, id, port, m)
		a.GetPacket().PkiEncrypted = p.GetPkiEncrypted()
		if port == pb.PortNum_TRACEROUTE_APP {
			a.GetPacket().HopStart = 3
		}
		out = append(out, a)
		answered = true
	}
	refuse := func(from uint32, reason pb.Routing_Error) []*pb.FromRadio {
		return []*pb.FromRadio{routingReply(from, me, id, reason)}
	}
	adminType := ""
	switch data.GetPortnum() {
	case pb.PortNum_ADMIN_APP:
		var m pb.AdminMessage
		if err := proto.Unmarshal(data.GetPayload(), &m); err != nil {
			return nil
		}
		r.adminMsgs = append(r.adminMsgs, proto.Clone(&m).(*pb.AdminMessage))
		adminType = oneofName(&m)
		switch {
		case len(r.Owner.GetPublicKey()) == 0:
			return refuse(me, pb.Routing_PKI_UNKNOWN_PUBKEY)
		case !p.GetPkiEncrypted():
			return refuse(r.Num, pb.Routing_NOT_AUTHORIZED)
		case !slices.ContainsFunc(r.Config.GetSecurity().GetAdminKey(), func(k []byte) bool { return bytes.Equal(k, d.PublicKey) }):
			return refuse(r.Num, pb.Routing_ADMIN_PUBLIC_KEY_UNAUTHORIZED)
		case !strings.HasPrefix(adminType, "get_") && (r.passkey == nil || !bytes.Equal(m.SessionPasskey, r.passkey)):
			return refuse(r.Num, pb.Routing_ADMIN_BAD_SESSION_KEY)
		}
		if resp := d.remoteAdmin(r, &m); resp != nil {
			if r.passkey == nil {
				r.passkey = randomPasskey()
			}
			resp.SessionPasskey = r.passkey
			answer(pb.PortNum_ADMIN_APP, resp)
			if r.RotatePasskeyOnce && !r.rotated {
				r.rotated = true
				r.passkey = randomPasskey()
			}
		}
	case pb.PortNum_TRACEROUTE_APP:
		rd := &pb.RouteDiscovery{Route: r.Route, RouteBack: r.RouteBack}
		for i := range len(r.Route) + 1 {
			rd.SnrTowards = append(rd.SnrTowards, int32(4*(10-i)))
		}
		for i := range len(r.RouteBack) + 1 {
			rd.SnrBack = append(rd.SnrBack, int32(4*(10-i)))
		}
		answer(pb.PortNum_TRACEROUTE_APP, rd)
	case pb.PortNum_POSITION_APP:
		if data.GetWantResponse() && r.Position != nil {
			answer(pb.PortNum_POSITION_APP, r.Position)
		}
	case pb.PortNum_TELEMETRY_APP:
		var req pb.Telemetry
		if err := proto.Unmarshal(data.GetPayload(), &req); err != nil {
			return nil
		}
		if !data.GetWantResponse() {
			break
		}
		resp := &pb.Telemetry{Time: uint32(time.Now().Unix())}
		switch req.Variant.(type) {
		case *pb.Telemetry_EnvironmentMetrics:
			if !r.EnvironmentSensor {
				break
			}
			resp.Variant = &pb.Telemetry_EnvironmentMetrics{EnvironmentMetrics: &pb.EnvironmentMetrics{Temperature: proto.Float32(21.5)}}
		case *pb.Telemetry_PowerMetrics:
			if !r.PowerSensor {
				break
			}
			resp.Variant = &pb.Telemetry_PowerMetrics{PowerMetrics: &pb.PowerMetrics{Ch1Voltage: proto.Float32(4.1)}}
		case *pb.Telemetry_LocalStats:
			resp.Variant = &pb.Telemetry_LocalStats{LocalStats: &pb.LocalStats{}}
		default:
			resp.Variant = r.Telemetry.GetVariant()
		}
		if resp.Variant != nil {
			answer(pb.PortNum_TELEMETRY_APP, resp)
		}
	}
	// The target NAKs a request no module answered.
	if !answered && data.GetWantResponse() && data.GetPortnum() != pb.PortNum_ADMIN_APP {
		return refuse(r.Num, pb.Routing_NO_RESPONSE)
	}
	if !answered && p.GetWantAck() { // an answer is the ack
		if r.DropAcks > 0 && strings.HasPrefix(adminType, r.DropAcksFor) {
			r.DropAcks--
		} else {
			out = append(out, routingReply(r.Num, me, id, pb.Routing_NONE))
		}
	}
	return out
}

func randomPasskey() []byte {
	b := make([]byte, 8)
	rand.Read(b)
	return b
}

// remoteAdmin performs m on r and returns the response, if any; d.mu must
// be held.
func (d *Device) remoteAdmin(r *Remote, m *pb.AdminMessage) *pb.AdminMessage {
	nc := r.config()
	switch v := m.PayloadVariant.(type) {
	case *pb.AdminMessage_GetOwnerRequest:
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerResponse{GetOwnerResponse: proto.Clone(r.Owner).(*pb.User)}}
	case *pb.AdminMessage_GetDeviceMetadataRequest:
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetDeviceMetadataResponse{GetDeviceMetadataResponse: &pb.DeviceMetadata{
			FirmwareVersion: r.FirmwareVersion, HwModel: r.Owner.GetHwModel()}}}
	case *pb.AdminMessage_GetConfigRequest:
		c := nc.section(v.GetConfigRequest)
		if sec := c.GetSecurity(); sec != nil { // never sent over the air
			sec.PrivateKey = nil
		}
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetConfigResponse{GetConfigResponse: c}}
	case *pb.AdminMessage_GetModuleConfigRequest:
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetModuleConfigResponse{GetModuleConfigResponse: nc.moduleSection(v.GetModuleConfigRequest)}}
	case *pb.AdminMessage_GetChannelRequest:
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetChannelResponse{GetChannelResponse: nc.channel(v.GetChannelRequest)}}
	case *pb.AdminMessage_BeginEditSettings:
		r.open, r.pending = true, nil
	case *pb.AdminMessage_CommitEditSettings:
		for _, s := range r.pending {
			nc.apply(s)
		}
		r.open, r.pending = false, nil
		if r.RebootOnCommit {
			r.downUntil = time.Now().Add(r.RebootFor)
			r.passkey = nil
		}
	case *pb.AdminMessage_SetConfig, *pb.AdminMessage_SetModuleConfig, *pb.AdminMessage_SetChannel,
		*pb.AdminMessage_SetOwner, *pb.AdminMessage_SetFixedPosition:
		if r.open {
			r.pending = append(r.pending, proto.Clone(m).(*pb.AdminMessage))
		} else {
			nc.apply(m)
		}
	}
	return nil
}

func (r *Remote) config() nodeConfig {
	return nodeConfig{cfg: r.Config, mod: r.ModuleConfig, channels: &r.Channels, owner: &r.Owner, position: &r.Position,
		ignoreFields: r.IgnoreFields}
}

// nodeConfig is the configuration a fake node answers from and applies
// set_* messages to. Callers hold the device lock.
type nodeConfig struct {
	cfg          *pb.LocalConfig
	mod          *pb.LocalModuleConfig
	channels     *[8]*pb.Channel
	owner        **pb.User
	position     **pb.Position
	ignoreFields []string
	ignoreCoords bool
}

// section answers get_config_request for type t.
func (nc nodeConfig) section(t pb.AdminMessage_ConfigType) *pb.Config {
	if t == pb.AdminMessage_SESSIONKEY_CONFIG {
		return &pb.Config{PayloadVariant: &pb.Config_Sessionkey{Sessionkey: &pb.Config_SessionkeyConfig{}}}
	}
	name := strings.ToLower(strings.TrimSuffix(t.String(), "_CONFIG")) // LORA_CONFIG → lora
	for _, c := range configpath.ConfigSections(nc.cfg) {
		if oneofName(c) == name {
			return proto.Clone(c).(*pb.Config)
		}
	}
	return &pb.Config{}
}

// moduleSections maps module config types whose name differs from the
// LocalModuleConfig field.
var moduleSections = map[pb.AdminMessage_ModuleConfigType]string{
	pb.AdminMessage_EXTNOTIF_CONFIG:          "external_notification",
	pb.AdminMessage_STOREFORWARD_CONFIG:      "store_forward",
	pb.AdminMessage_RANGETEST_CONFIG:         "range_test",
	pb.AdminMessage_CANNEDMSG_CONFIG:         "canned_message",
	pb.AdminMessage_REMOTEHARDWARE_CONFIG:    "remote_hardware",
	pb.AdminMessage_NEIGHBORINFO_CONFIG:      "neighbor_info",
	pb.AdminMessage_AMBIENTLIGHTING_CONFIG:   "ambient_lighting",
	pb.AdminMessage_DETECTIONSENSOR_CONFIG:   "detection_sensor",
	pb.AdminMessage_TRAFFICMANAGEMENT_CONFIG: "traffic_management",
	pb.AdminMessage_MESHBEACON_CONFIG:        "mesh_beacon",
}

// moduleSection answers get_module_config_request for type t.
func (nc nodeConfig) moduleSection(t pb.AdminMessage_ModuleConfigType) *pb.ModuleConfig {
	name, ok := moduleSections[t]
	if !ok {
		name = strings.ToLower(strings.TrimSuffix(t.String(), "_CONFIG"))
	}
	for _, c := range configpath.ModuleSections(nc.mod) {
		if oneofName(c) == name {
			return proto.Clone(c).(*pb.ModuleConfig)
		}
	}
	return &pb.ModuleConfig{}
}

// channel answers get_channel_request (index + 1).
func (nc nodeConfig) channel(req uint32) *pb.Channel {
	if i := int(req) - 1; i >= 0 && i < len(nc.channels) && nc.channels[i] != nil {
		return proto.Clone(nc.channels[i]).(*pb.Channel)
	}
	return &pb.Channel{}
}

// apply applies one set_* message, minus ignoreFields.
func (nc nodeConfig) apply(m *pb.AdminMessage) {
	switch v := m.PayloadVariant.(type) {
	case *pb.AdminMessage_SetConfig:
		c := proto.Clone(v.SetConfig).(*pb.Config)
		nc.keepIgnored(c.ProtoReflect(), nc.cfg.ProtoReflect())
		configpath.MergeConfig(nc.cfg, c)
	case *pb.AdminMessage_SetModuleConfig:
		c := proto.Clone(v.SetModuleConfig).(*pb.ModuleConfig)
		nc.keepIgnored(c.ProtoReflect(), nc.mod.ProtoReflect())
		configpath.MergeModuleConfig(nc.mod, c)
	case *pb.AdminMessage_SetChannel:
		if i := v.SetChannel.GetIndex(); i >= 0 && int(i) < len(nc.channels) {
			nc.channels[i] = proto.Clone(v.SetChannel).(*pb.Channel)
		}
	case *pb.AdminMessage_SetOwner:
		*nc.owner = proto.Clone(v.SetOwner).(*pb.User)
	case *pb.AdminMessage_SetFixedPosition:
		if nc.cfg.Position == nil {
			nc.cfg.Position = &pb.Config_PositionConfig{}
		}
		nc.cfg.Position.FixedPosition = true
		if !nc.ignoreCoords {
			*nc.position = proto.Clone(v.SetFixedPosition).(*pb.Position)
		}
	}
}

// keepIgnored resets each ignoreFields path in the section carried by
// wrapper to its current value in local, so the write is silently dropped.
func (nc nodeConfig) keepIgnored(wrapper, local protoreflect.Message) {
	for _, path := range nc.ignoreFields {
		p, err := configpath.Parse(path)
		if err != nil || p.Field == "" {
			continue
		}
		name := protoreflect.Name(p.Section)
		wf, lf := wrapper.Descriptor().Fields().ByName(name), local.Descriptor().Fields().ByName(name)
		if wf == nil || lf == nil || !wrapper.Has(wf) {
			continue
		}
		old, err := configpath.FormatField(local.Get(lf).Message(), p.Field)
		if err != nil {
			continue
		}
		if err := configpath.SetField(wrapper.Mutable(wf).Message(), p.Field, old); err != nil {
			panic(err)
		}
	}
}
