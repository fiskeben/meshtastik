package fakedevice_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/fakedevice"
	"meshtastik/internal/transport"
)

const remoteNum = 0x99

// rig is a fake with one remote node and a handshaken raw connection.
type rig struct {
	t    *testing.T
	d    *fakedevice.Device
	r    *fakedevice.Remote
	conn transport.Conn
	id   uint32
}

func newRig(t *testing.T, setup func(*fakedevice.Device, *fakedevice.Remote)) *rig {
	d := fakedevice.New()
	r := fakedevice.NewRemote(remoteNum, "Hill", "HIL1")
	r.Config.Security.AdminKey = [][]byte{d.PublicKey}
	d.Remotes = map[uint32]*fakedevice.Remote{remoteNum: r}
	if setup != nil {
		setup(d, r)
	}
	conn, err := d.Dialer()(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	g := &rig{t: t, d: d, r: r, conn: conn, id: 100}
	if err := conn.Send(context.Background(), &pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: 7}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range g.recv(2 * time.Second) {
		if m.GetNodeInfo().GetNum() == remoteNum && !bytes.Equal(m.GetNodeInfo().GetUser().GetPublicKey(), r.Owner.GetPublicKey()) {
			t.Fatal("remote node info lacks its public key")
		}
		if m.GetConfigCompleteId() == 7 {
			return g
		}
	}
	t.Fatal("no handshake")
	return nil
}

// recv collects messages until none arrives for wait.
func (g *rig) recv(wait time.Duration) []*pb.FromRadio {
	var out []*pb.FromRadio
	for {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		m, err := g.conn.Recv(ctx)
		cancel()
		if err != nil {
			return out
		}
		out = append(out, m)
		if m.GetConfigCompleteId() == 7 {
			return out
		}
	}
}

// send sends a packet to the remote and returns the packets that come back.
func (g *rig) send(port pb.PortNum, payload proto.Message, pki, wantAck, wantResponse bool) []*pb.MeshPacket {
	g.t.Helper()
	b, err := proto.Marshal(payload)
	if err != nil {
		g.t.Fatal(err)
	}
	g.id++
	p := &pb.MeshPacket{To: remoteNum, Id: g.id, WantAck: wantAck, PkiEncrypted: pki,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: port, Payload: b, WantResponse: wantResponse}}}
	if err := g.conn.Send(context.Background(), &pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: p}}); err != nil {
		g.t.Fatal(err)
	}
	var out []*pb.MeshPacket
	for _, m := range g.recv(150 * time.Millisecond) {
		if p := m.GetPacket(); p != nil {
			out = append(out, p)
		}
	}
	return out
}

func (g *rig) admin(m *pb.AdminMessage) []*pb.MeshPacket {
	return g.send(pb.PortNum_ADMIN_APP, m, true, true, true)
}

func routing(t *testing.T, p *pb.MeshPacket) pb.Routing_Error {
	t.Helper()
	if p.GetDecoded().GetPortnum() != pb.PortNum_ROUTING_APP {
		t.Fatalf("not a routing packet: %v", p)
	}
	var r pb.Routing
	if err := proto.Unmarshal(p.GetDecoded().GetPayload(), &r); err != nil {
		t.Fatal(err)
	}
	return r.GetErrorReason()
}

func adminOf(t *testing.T, p *pb.MeshPacket) *pb.AdminMessage {
	t.Helper()
	var m pb.AdminMessage
	if p.GetDecoded().GetPortnum() != pb.PortNum_ADMIN_APP || proto.Unmarshal(p.GetDecoded().GetPayload(), &m) != nil {
		t.Fatalf("not an admin packet: %v", p)
	}
	return &m
}

var getOwner = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerRequest{GetOwnerRequest: true}}

func hopLimit(n uint32) *pb.AdminMessage {
	return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{
		PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{HopLimit: n, Region: pb.Config_LoRaConfig_EU_868, UsePreset: true, TxEnabled: true}}}}}
}

func TestRemoteAnswersTraceroute(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Route, r.RouteBack = []uint32{7}, []uint32{8} })
	ps := g.send(pb.PortNum_TRACEROUTE_APP, &pb.RouteDiscovery{}, false, false, true)
	if len(ps) != 1 || ps[0].GetFrom() != remoteNum || ps[0].GetDecoded().GetRequestId() != g.id || ps[0].GetHopStart() == 0 {
		t.Fatal(ps)
	}
	var rd pb.RouteDiscovery
	if err := proto.Unmarshal(ps[0].GetDecoded().GetPayload(), &rd); err != nil {
		t.Fatal(err)
	}
	if len(rd.Route) != 1 || rd.Route[0] != 7 || len(rd.SnrTowards) != 2 || rd.RouteBack[0] != 8 || len(rd.SnrBack) != 2 {
		t.Fatalf("%v", &rd)
	}
}

func TestRemoteAnswersOwnerWithPasskey(t *testing.T) {
	g := newRig(t, nil)
	ps := g.admin(getOwner) // the answer is the ack: firmware sends no second one
	if len(ps) != 1 {
		t.Fatal(ps)
	}
	m := adminOf(t, ps[0])
	if m.GetGetOwnerResponse().GetLongName() != "Hill" || len(m.SessionPasskey) != 8 || !ps[0].GetPkiEncrypted() ||
		ps[0].GetFrom() != remoteNum || ps[0].GetTo() != g.d.NodeNum || ps[0].GetDecoded().GetRequestId() != g.id {
		t.Fatal(ps[0], m)
	}
}

func TestRemoteRefusesUnknownAdminKey(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Config.Security.AdminKey = nil })
	ps := g.admin(getOwner)
	if len(ps) != 1 || routing(t, ps[0]) != pb.Routing_ADMIN_PUBLIC_KEY_UNAUTHORIZED || ps[0].GetFrom() != remoteNum {
		t.Fatal(ps)
	}
	if ps := g.send(pb.PortNum_ADMIN_APP, getOwner, false, true, true); len(ps) != 1 || routing(t, ps[0]) != pb.Routing_NOT_AUTHORIZED {
		t.Fatal("unencrypted admin:", ps)
	}
}

func TestRemoteWithoutPublicKeyFailsLocally(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Owner.PublicKey = nil })
	ps := g.admin(getOwner)
	if len(ps) != 1 || routing(t, ps[0]) != pb.Routing_PKI_UNKNOWN_PUBKEY || ps[0].GetFrom() != g.d.NodeNum {
		t.Fatal(ps)
	}
}

func TestRemoteNeedsPasskeyForSets(t *testing.T) {
	g := newRig(t, nil)
	ps := g.admin(hopLimit(5))
	if len(ps) != 1 || routing(t, ps[0]) != pb.Routing_ADMIN_BAD_SESSION_KEY {
		t.Fatal(ps)
	}
	key := adminOf(t, g.admin(getOwner)[0]).SessionPasskey
	set := hopLimit(5)
	set.SessionPasskey = key
	if ps := g.admin(set); len(ps) != 1 || routing(t, ps[0]) != pb.Routing_NONE {
		t.Fatal(ps)
	}
	if g.r.Config.GetLora().GetHopLimit() != 5 || len(g.d.RemoteAdmin(remoteNum)) != 3 || len(g.d.Received()) != 0 {
		t.Fatal(g.r.Config.GetLora(), len(g.d.RemoteAdmin(remoteNum)), len(g.d.Received()))
	}
}

func TestRemoteAcksWantAck(t *testing.T) {
	g := newRig(t, nil)
	ps := g.send(pb.PortNum_TEXT_MESSAGE_APP, &pb.Data{}, false, true, false)
	if len(ps) != 1 || routing(t, ps[0]) != pb.Routing_NONE || ps[0].GetFrom() != remoteNum || ps[0].GetDecoded().GetRequestId() != g.id {
		t.Fatal(ps)
	}
}

func TestRemoteUnreachableSaysNothing(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Unreachable = true })
	if ps := g.admin(getOwner); len(ps) != 0 {
		t.Fatal(ps)
	}
	if len(g.d.RemoteRequests(remoteNum)) != 1 {
		t.Fatal("request not recorded")
	}
}

func TestRemoteFailWith(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) {
		r.Unreachable, r.FailWith = true, pb.Routing_MAX_RETRANSMIT
	})
	ps := g.admin(getOwner)
	if len(ps) != 1 || routing(t, ps[0]) != pb.Routing_MAX_RETRANSMIT || ps[0].GetFrom() != g.d.NodeNum {
		t.Fatal(ps)
	}
	// Retransmission needs want_ack: without it nothing comes back.
	if ps := g.send(pb.PortNum_POSITION_APP, &pb.Position{}, false, false, true); len(ps) != 0 {
		t.Fatal(ps)
	}
}

func TestRemoteDropFirst(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.DropFirst = 2 })
	// A lost want_ack packet ends in the local node giving up; others vanish.
	if ps := g.admin(getOwner); len(ps) != 1 || routing(t, ps[0]) != pb.Routing_MAX_RETRANSMIT || ps[0].GetFrom() != g.d.NodeNum {
		t.Fatal(ps)
	}
	if ps := g.send(pb.PortNum_POSITION_APP, &pb.Position{}, false, false, true); len(ps) != 0 {
		t.Fatal(ps)
	}
	if ps := g.admin(getOwner); len(ps) != 1 || adminOf(t, ps[0]).GetGetOwnerResponse() == nil {
		t.Fatal(ps)
	}
}

func TestRemoteDropAcksStillApplies(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.DropAcks, r.DropAcksFor = 1, "set_" })
	key := adminOf(t, g.admin(getOwner)[0]).SessionPasskey
	begin := &pb.AdminMessage{SessionPasskey: key, PayloadVariant: &pb.AdminMessage_BeginEditSettings{BeginEditSettings: true}}
	if ps := g.admin(begin); len(ps) != 1 || routing(t, ps[0]) != pb.Routing_NONE { // not a set_: acked
		t.Fatal(ps)
	}
	set := hopLimit(6)
	set.SessionPasskey = key
	if ps := g.admin(set); len(ps) != 0 { // the set's ack is lost
		t.Fatal(ps)
	}
	if ps := g.admin(set); len(ps) != 1 || routing(t, ps[0]) != pb.Routing_NONE {
		t.Fatal(ps)
	}
}

func TestRemoteRotatePasskeyOnce(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.RotatePasskeyOnce = true })
	set := hopLimit(5)
	set.SessionPasskey = adminOf(t, g.admin(getOwner)[0]).SessionPasskey
	if ps := g.admin(set); len(ps) != 1 || routing(t, ps[0]) != pb.Routing_ADMIN_BAD_SESSION_KEY {
		t.Fatal(ps)
	}
	set.SessionPasskey = adminOf(t, g.admin(getOwner)[0]).SessionPasskey
	if ps := g.admin(set); len(ps) != 1 || routing(t, ps[0]) != pb.Routing_NONE {
		t.Fatal(ps)
	}
}

func TestRemoteDecoys(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Decoys = true })
	ps := g.send(pb.PortNum_POSITION_APP, &pb.Position{}, false, false, true)
	if len(ps) != 3 || ps[0].GetDecoded().GetRequestId() == g.id || ps[1].GetFrom() == remoteNum ||
		ps[2].GetFrom() != remoteNum || ps[2].GetDecoded().GetRequestId() != g.id {
		t.Fatal(ps)
	}
}

func TestRemoteBlanksPrivateKey(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) {
		r.Config.Security.PrivateKey = bytes.Repeat([]byte{1}, 32)
	})
	ps := g.admin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetConfigRequest{GetConfigRequest: pb.AdminMessage_SECURITY_CONFIG}})
	sec := adminOf(t, ps[0]).GetGetConfigResponse().GetSecurity()
	if sec == nil || len(sec.PrivateKey) != 0 || len(sec.PublicKey) != 32 {
		t.Fatal(sec)
	}
}

func TestRemoteModuleConfigAndChannel(t *testing.T) {
	g := newRig(t, nil)
	ps := g.admin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetModuleConfigRequest{GetModuleConfigRequest: pb.AdminMessage_EXTNOTIF_CONFIG}})
	if adminOf(t, ps[0]).GetGetModuleConfigResponse().GetExternalNotification() == nil {
		t.Fatal(ps[0])
	}
	ps = g.admin(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetChannelRequest{GetChannelRequest: 1}})
	if adminOf(t, ps[0]).GetGetChannelResponse().GetRole() != pb.Channel_PRIMARY {
		t.Fatal(ps[0])
	}
}

func TestRemoteRebootsAfterCommit(t *testing.T) {
	g := newRig(t, func(_ *fakedevice.Device, r *fakedevice.Remote) {
		r.RebootOnCommit, r.RebootFor = true, 300*time.Millisecond
	})
	key := adminOf(t, g.admin(getOwner)[0]).SessionPasskey
	commit := &pb.AdminMessage{SessionPasskey: key, PayloadVariant: &pb.AdminMessage_CommitEditSettings{CommitEditSettings: true}}
	g.admin(commit)
	if ps := g.admin(getOwner); len(ps) != 1 || routing(t, ps[0]) != pb.Routing_MAX_RETRANSMIT {
		t.Fatal("answered while rebooting", ps)
	}
	time.Sleep(300 * time.Millisecond)
	if ps := g.admin(getOwner); len(ps) != 1 || adminOf(t, ps[0]).GetGetOwnerResponse() == nil {
		t.Fatal("not back after reboot", ps)
	}
}
