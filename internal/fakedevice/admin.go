package fakedevice

import (
	"bytes"
	"slices"
	"strings"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// passkey is the admin session passkey the fake hands out.
var passkey = []byte{0xfa, 0x6e, 0x5e, 0x55, 0x10, 0x4c, 0x0d, 0xe7}

// linkState is the admin state of one link.
type linkState struct {
	open    bool               // edit transaction in progress
	pending []*pb.AdminMessage // sets buffered until commit
	hung    bool               // HangOnCommit: read everything, answer nothing
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

// OneofName returns the name of the populated payload_variant field of m,
// such as "set_config".
func OneofName(m proto.Message) string { return oneofName(m) }

// oneofName returns the name of the populated payload_variant field of m.
func oneofName(m proto.Message) string {
	r := m.ProtoReflect()
	if f := r.WhichOneof(r.Descriptor().Oneofs().ByName("payload_variant")); f != nil {
		return string(f.Name())
	}
	return ""
}

// handleAdmin handles admin message m carried by packet p; false closes the link.
func (d *Device) handleAdmin(l *link, st *linkState, p *pb.MeshPacket, m *pb.AdminMessage) bool {
	d.mu.Lock()
	onAdmin, dropAfter, requirePasskey := d.OnAdmin, d.DropAfter, d.RequirePasskey
	d.mu.Unlock()
	if onAdmin != nil {
		onAdmin(m)
	}
	if dropAfter != nil && dropAfter(m) {
		return false
	}
	isGet := strings.HasPrefix(oneofName(m), "get_")
	if !requirePasskey || isGet || bytes.Equal(m.SessionPasskey, passkey) {
		resp, reboot := d.admin(st, m)
		if resp != nil && !d.reply(l, p, resp) {
			return false
		}
		d.mu.Lock()
		delay := d.RebootDelay
		d.mu.Unlock()
		if _, reset := m.PayloadVariant.(*pb.AdminMessage_NodedbReset); reboot && reset && delay > 0 {
			time.AfterFunc(delay, func() {
				l.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Rebooted{Rebooted: true}})
				l.close()
			})
			return true
		}
		if reboot {
			l.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Rebooted{Rebooted: true}})
			return false
		}
	}
	return true
}

// admin performs m and returns the response to send, if any, and whether the
// node reboots now. Reboot, shutdown and factory reset are only recorded.
func (d *Device) admin(st *linkState, m *pb.AdminMessage) (*pb.AdminMessage, bool) {
	switch v := m.PayloadVariant.(type) {
	case *pb.AdminMessage_GetConfigRequest:
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetConfigResponse{GetConfigResponse: d.configSection(v.GetConfigRequest)}}, false
	case *pb.AdminMessage_GetChannelRequest:
		d.mu.Lock()
		ch := d.local().channel(v.GetChannelRequest)
		d.mu.Unlock()
		return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetChannelResponse{GetChannelResponse: ch}}, false
	case *pb.AdminMessage_BeginEditSettings:
		st.open, st.pending = true, nil
	case *pb.AdminMessage_CommitEditSettings:
		for _, s := range st.pending {
			d.applySet(s)
		}
		st.open, st.pending = false, nil
		d.mu.Lock()
		defer d.mu.Unlock()
		d.handshakeReboots = d.HandshakeReboots
		if d.RebootOnCommit || d.HangOnCommit || d.HandshakeReboots > 0 {
			d.rebooted = true
		}
		st.hung = d.HangOnCommit
		return nil, d.RebootOnCommit
	case *pb.AdminMessage_SetFavoriteNode, *pb.AdminMessage_RemoveFavoriteNode, *pb.AdminMessage_SetIgnoredNode,
		*pb.AdminMessage_RemoveIgnoredNode, *pb.AdminMessage_RemoveByNodenum:
		d.editNodes(m)
	case *pb.AdminMessage_NodedbReset:
		d.mu.Lock()
		defer d.mu.Unlock()
		kept := d.ExtraNodes[:0]
		for _, n := range d.ExtraNodes {
			if v.NodedbReset && n.GetIsFavorite() {
				kept = append(kept, n)
			}
		}
		d.ExtraNodes = kept
		d.rebooted = true
		return nil, true
	case *pb.AdminMessage_SetConfig, *pb.AdminMessage_SetModuleConfig, *pb.AdminMessage_SetChannel,
		*pb.AdminMessage_SetOwner, *pb.AdminMessage_SetFixedPosition:
		if st.open {
			st.pending = append(st.pending, m)
		} else {
			d.applySet(m)
		}
	}
	return nil, false
}

// editNodes applies a node list admin message to ExtraNodes, unless
// IgnoreNodeOps is set.
func (d *Device) editNodes(m *pb.AdminMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.IgnoreNodeOps {
		return
	}
	find := func(num uint32) *pb.NodeInfo {
		for _, n := range d.ExtraNodes {
			if n.GetNum() == num {
				return n
			}
		}
		return nil
	}
	switch v := m.PayloadVariant.(type) {
	case *pb.AdminMessage_SetFavoriteNode:
		if n := find(v.SetFavoriteNode); n != nil {
			n.IsFavorite = true
		}
	case *pb.AdminMessage_RemoveFavoriteNode:
		if n := find(v.RemoveFavoriteNode); n != nil {
			n.IsFavorite = false
		}
	case *pb.AdminMessage_SetIgnoredNode: // firmware 2.8 adds a node it has not heard yet
		if n := find(v.SetIgnoredNode); n != nil {
			n.IsIgnored = true
		} else if v.SetIgnoredNode != d.NodeNum {
			d.ExtraNodes = append(d.ExtraNodes, &pb.NodeInfo{Num: v.SetIgnoredNode, IsIgnored: true})
		}
	case *pb.AdminMessage_RemoveIgnoredNode:
		if n := find(v.RemoveIgnoredNode); n != nil {
			n.IsIgnored = false
		}
	case *pb.AdminMessage_RemoveByNodenum:
		d.ExtraNodes = slices.DeleteFunc(d.ExtraNodes, func(n *pb.NodeInfo) bool { return n.GetNum() == v.RemoveByNodenum })
	}
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

// local is the device's own configuration as a nodeConfig; d.mu must be held.
func (d *Device) local() nodeConfig {
	return nodeConfig{cfg: d.Config, mod: d.ModuleConfig, channels: &d.Channels, owner: &d.Owner, position: &d.Position,
		ignoreFields: d.IgnoreFields, ignoreCoords: d.IgnoreFixedPositionCoords}
}

// configSection answers get_config_request for type t.
func (d *Device) configSection(t pb.AdminMessage_ConfigType) *pb.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.local().section(t)
}

// applySet applies one set_* message to the device, minus IgnoreFields.
func (d *Device) applySet(m *pb.AdminMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.local().apply(m)
}
