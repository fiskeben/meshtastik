package fakedevice

import (
	"bytes"
	"strings"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"meshtastik/internal/configpath"
)

// passkey is the admin session passkey the fake hands out.
var passkey = []byte{0xfa, 0x6e, 0x5e, 0x55, 0x10, 0x4c, 0x0d, 0xe7}

// editTx is the edit transaction state of one link.
type editTx struct {
	open    bool
	pending []*pb.AdminMessage // sets buffered until commit
}

// Received returns copies of every admin message the device has received.
func (d *Device) Received() []*pb.AdminMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*pb.AdminMessage, len(d.received))
	for i, m := range d.received {
		out[i] = proto.Clone(m).(*pb.AdminMessage)
	}
	return out
}

// LastAdmin returns a copy of the last admin message received, or nil.
func (d *Device) LastAdmin() *pb.AdminMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.received) == 0 {
		return nil
	}
	return proto.Clone(d.received[len(d.received)-1]).(*pb.AdminMessage)
}

// CountBegin counts begin_edit_settings messages in ms.
func CountBegin(ms []*pb.AdminMessage) int {
	n := 0
	for _, m := range ms {
		if m.GetBeginEditSettings() {
			n++
		}
	}
	return n
}

// HasCommit reports whether ms contains a commit_edit_settings message.
func HasCommit(ms []*pb.AdminMessage) bool {
	for _, m := range ms {
		if m.GetCommitEditSettings() {
			return true
		}
	}
	return false
}

// oneofName returns the name of the populated payload_variant field of m.
func oneofName(m proto.Message) string {
	r := m.ProtoReflect()
	if f := r.WhichOneof(r.Descriptor().Oneofs().ByName("payload_variant")); f != nil {
		return string(f.Name())
	}
	return ""
}

// handleAdmin handles admin message m carried by packet p; false closes the link.
func (d *Device) handleAdmin(l *link, tx *editTx, p *pb.MeshPacket, m *pb.AdminMessage) bool {
	d.mu.Lock()
	onAdmin, dropAfter, requirePasskey := d.OnAdmin, d.DropAfter, d.RequirePasskey
	d.mu.Unlock()
	if onAdmin != nil {
		onAdmin(m)
	}
	isGet := strings.HasPrefix(oneofName(m), "get_")
	if !requirePasskey || isGet || bytes.Equal(m.SessionPasskey, passkey) {
		resp, reboot := d.admin(tx, m)
		if resp != nil && !d.reply(l, p, resp) {
			return false
		}
		if reboot {
			l.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Rebooted{Rebooted: true}})
			return false
		}
	}
	return dropAfter == nil || !dropAfter(m)
}

// admin performs m and returns the response to send, if any, and whether the
// node reboots now. Reboot, shutdown and factory reset are only recorded.
func (d *Device) admin(tx *editTx, m *pb.AdminMessage) (*pb.AdminMessage, bool) {
	switch v := m.PayloadVariant.(type) {
	case *pb.AdminMessage_GetConfigRequest:
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetConfigResponse{GetConfigResponse: d.configSection(v.GetConfigRequest)}}, false
	case *pb.AdminMessage_GetChannelRequest:
		ch := &pb.Channel{}
		d.mu.Lock()
		if i := int(v.GetChannelRequest) - 1; i >= 0 && i < len(d.Channels) && d.Channels[i] != nil {
			ch = proto.Clone(d.Channels[i]).(*pb.Channel)
		}
		d.mu.Unlock()
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetChannelResponse{GetChannelResponse: ch}}, false
	case *pb.AdminMessage_BeginEditSettings:
		tx.open, tx.pending = true, nil
	case *pb.AdminMessage_CommitEditSettings:
		for _, s := range tx.pending {
			d.applySet(s)
		}
		tx.open, tx.pending = false, nil
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.RebootOnCommit {
			d.rebooted = true
			return nil, true
		}
	case *pb.AdminMessage_SetConfig, *pb.AdminMessage_SetModuleConfig, *pb.AdminMessage_SetChannel,
		*pb.AdminMessage_SetOwner, *pb.AdminMessage_SetFixedPosition:
		if tx.open {
			tx.pending = append(tx.pending, m)
		} else {
			d.applySet(m)
		}
	}
	return nil, false
}

// reply sends resp as the answer to request packet req.
func (d *Device) reply(l *link, req *pb.MeshPacket, resp *pb.AdminMessage) bool {
	resp.SessionPasskey = passkey
	payload, err := proto.Marshal(resp)
	if err != nil {
		panic(err)
	}
	d.mu.Lock()
	num := d.NodeNum
	d.mu.Unlock()
	pkt := &pb.MeshPacket{From: num, To: num, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{
		Portnum: pb.PortNum_ADMIN_APP, Payload: payload, RequestId: req.GetId(),
	}}}
	return l.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: pkt}})
}

// configSection answers get_config_request for type t.
func (d *Device) configSection(t pb.AdminMessage_ConfigType) *pb.Config {
	if t == pb.AdminMessage_SESSIONKEY_CONFIG {
		return &pb.Config{PayloadVariant: &pb.Config_Sessionkey{Sessionkey: &pb.Config_SessionkeyConfig{}}}
	}
	name := strings.ToLower(strings.TrimSuffix(t.String(), "_CONFIG")) // LORA_CONFIG → lora
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range configpath.ConfigSections(d.Config) {
		if oneofName(c) == name {
			return c
		}
	}
	return &pb.Config{}
}

// applySet applies one set_* message to the device, minus IgnoreFields.
func (d *Device) applySet(m *pb.AdminMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch v := m.PayloadVariant.(type) {
	case *pb.AdminMessage_SetConfig:
		c := proto.Clone(v.SetConfig).(*pb.Config)
		d.keepIgnored(c.ProtoReflect(), d.Config.ProtoReflect())
		configpath.MergeConfig(d.Config, c)
	case *pb.AdminMessage_SetModuleConfig:
		c := proto.Clone(v.SetModuleConfig).(*pb.ModuleConfig)
		d.keepIgnored(c.ProtoReflect(), d.ModuleConfig.ProtoReflect())
		configpath.MergeModuleConfig(d.ModuleConfig, c)
	case *pb.AdminMessage_SetChannel:
		if i := v.SetChannel.GetIndex(); i >= 0 && int(i) < len(d.Channels) {
			d.Channels[i] = proto.Clone(v.SetChannel).(*pb.Channel)
		}
	case *pb.AdminMessage_SetOwner:
		u := proto.Clone(d.Owner).(*pb.User)
		u.LongName, u.ShortName = v.SetOwner.GetLongName(), v.SetOwner.GetShortName()
		d.Owner = u
	case *pb.AdminMessage_SetFixedPosition:
		if d.Config.Position == nil {
			d.Config.Position = &pb.Config_PositionConfig{}
		}
		d.Config.Position.FixedPosition = true
	}
}

// keepIgnored resets each IgnoreFields path in the section carried by
// wrapper to its current value in local, so the write is silently dropped.
func (d *Device) keepIgnored(wrapper, local protoreflect.Message) {
	for _, path := range d.IgnoreFields {
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
