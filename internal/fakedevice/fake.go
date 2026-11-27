// Package fakedevice is an in-memory Meshtastic node for tests. It speaks the
// want_config handshake over an in-memory transport.Conn.
package fakedevice

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"meshtastik/internal/configpath"
	"meshtastik/internal/transport"
)

// Device is a fake node. Set fields before the first Dial; they are read under
// the device lock when a handshake or admin message is answered.
type Device struct {
	NodeNum         uint32
	FirmwareVersion string
	Kind            transport.Kind
	Owner           *pb.User
	Position        *pb.Position // own position, reported in the self NodeInfo
	Config          *pb.LocalConfig
	ModuleConfig    *pb.LocalModuleConfig
	Channels        [8]*pb.Channel
	ExtraNodes      []*pb.NodeInfo
	// Remotes are nodes reached through the radio (see Remote); each is
	// listed in the handshake with its public key.
	Remotes map[uint32]*Remote
	// PublicKey is the node's own public key, also reported as
	// security.public_key; remotes check it against their admin keys.
	PublicKey       []byte
	Silent          bool // never answer want_config
	StaleCompleteID bool // send one config_complete_id with a wrong nonce first
	AckTexts        bool // answer a direct text message with a ROUTING_APP ack from the destination
	// ImplicitAckTexts: answer a text message sent with want_ack with a NONE
	// ROUTING_APP reply from the local node, as firmware does when it hears
	// a neighbour rebroadcast the packet.
	ImplicitAckTexts bool
	AckError         pb.Routing_Error // error_reason of the AckTexts reply (default NONE)

	RequirePasskey bool // ignore non-get admin messages without the session passkey
	IgnoreNodeOps  bool // accept node list admin messages (favorite, ignore, remove) without applying them
	// RefuseSending: the node does not send packets to a remote on these
	// ports, answering with a client notification carrying this message
	// instead (as firmware rate limits do).
	RefuseSending map[pb.PortNum]string
	// RebootDelay: a node list reset reboots this long after the request
	// instead of at once (firmware waits ~7 s).
	RebootDelay time.Duration
	// SilentReboot: that reboot neither reports rebooted nor closes the link,
	// which just stops answering, as over TCP.
	SilentReboot bool
	IgnoreFields []string // config paths whose new value is silently dropped when applied
	// IgnoreFixedPositionCoords: set_fixed_position turns position.fixed_position
	// on but keeps the old coordinates.
	IgnoreFixedPositionCoords bool
	RebootOnCommit            bool // after commit: apply, send rebooted, close the link
	// HandshakeReboots: after a commit, answer this many want_config
	// requests (on any link) with rebooted instead, keeping the link open.
	HandshakeReboots int
	// HangOnCommit: after commit, the link stays open but never answers again.
	HangOnCommit bool
	// RefuseDialsAfterReboot fails every Dial after a commit that rebooted
	// the node (RebootOnCommit, HandshakeReboots or HangOnCommit).
	RefuseDialsAfterReboot bool
	// RefuseDials fails every Dial while set.
	RefuseDials bool
	// DropAfterHandshake, when non-zero, closes each link this long after it
	// answers want_config, like another client kicking this one off.
	DropAfterHandshake time.Duration
	// DropHandshakesFrom, when non-zero, closes the link instead of answering
	// want_config on dial number DropHandshakesFrom (1-based) and later ones,
	// like another client taking the connection mid-handshake.
	DropHandshakesFrom int
	// Backlog is emitted right after config_complete_id on the first
	// handshake of the first dial, as a node hands over queued packets.
	Backlog []*pb.MeshPacket
	// DropAfter closes the link on receiving a matching admin message,
	// without handling it.
	DropAfter func(*pb.AdminMessage) bool
	// OnAdmin is called with each admin message before it is handled, on the
	// device goroutine. It must not wait on client sends: the device reads
	// nothing more until it returns.
	OnAdmin func(*pb.AdminMessage)

	mu               sync.Mutex
	dials            int
	heartbeats       int
	received         []*pb.AdminMessage
	texts            []*pb.MeshPacket
	rebooted         bool
	handshakeReboots int   // pending HandshakeReboots
	cur              *link // link of the latest successful Dial
	backlogSent      bool
	wantConfigs      int // want_config requests answered
}

// New returns a Device with the default identity and configuration.
func New() *Device {
	d := &Device{
		NodeNum:         0x1234abcd,
		FirmwareVersion: "2.7.15.567b8ea",
		Kind:            transport.KindSerial,
		Owner:           &pb.User{LongName: "Test Node", ShortName: "TEST", HwModel: pb.HardwareModel_HELTEC_V3},
		Config:          &pb.LocalConfig{},
		ModuleConfig:    &pb.LocalModuleConfig{},
	}
	fillSections(d.Config.ProtoReflect())
	fillSections(d.ModuleConfig.ProtoReflect())
	d.Config.Lora.Region = pb.Config_LoRaConfig_EU_868
	d.Config.Lora.HopLimit = 3
	d.Config.Lora.TxEnabled = true
	d.Config.Lora.UsePreset = true
	d.Config.Bluetooth.Enabled = true
	d.Config.Security.SerialEnabled = true
	d.PublicKey = make([]byte, 32)
	rand.Read(d.PublicKey)
	d.Config.Security.PublicKey = d.PublicKey
	d.Owner.PublicKey = d.PublicKey                 // the node lists itself with its key
	d.Config.Security.PrivateKey = make([]byte, 32) // a real node holds both halves
	rand.Read(d.Config.Security.PrivateKey)
	for i := range d.Channels {
		ch := &pb.Channel{Index: int32(i), Role: pb.Channel_DISABLED}
		if i == 0 {
			ch.Role = pb.Channel_PRIMARY
			ch.Settings = &pb.ChannelSettings{Psk: []byte{1}}
		}
		d.Channels[i] = ch
	}
	return d
}

// fillSections makes every message-typed field of m non-nil.
func fillSections(m protoreflect.Message) {
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		if f := fields.Get(i); f.Kind() == protoreflect.MessageKind {
			m.Mutable(f)
		}
	}
}

// Dials reports how many times the Dialer has been used.
func (d *Device) Dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials
}

// Heartbeats reports how many heartbeat messages the device has received.
func (d *Device) Heartbeats() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.heartbeats
}

// Dialer returns a transport.Dialer; each call yields a fresh in-memory link
// to this device.
func (d *Device) Dialer() transport.Dialer {
	return func(ctx context.Context) (transport.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.dials++
		refuse := d.RefuseDials || d.rebooted && d.RefuseDialsAfterReboot
		kind := d.Kind
		var l *link
		if !refuse {
			l = newLink()
			l.dial = d.dials
			d.cur = l
		}
		d.mu.Unlock()
		if refuse {
			return nil, errors.New("fakedevice: dial refused")
		}
		go d.serve(l)
		return &clientConn{l: l, kind: kind}, nil
	}
}

// envelope carries a ToRadio to the device; seen closes once the device has
// recorded it, so Send returning means Received/Heartbeats include it.
type envelope struct {
	m    *pb.ToRadio
	seen chan struct{}
}

// link is the shared state of one in-memory connection.
type link struct {
	toDevice chan envelope
	toClient chan *pb.FromRadio
	done     chan struct{}
	once     sync.Once
	dial     int         // number of the Dial that made it, from 1
	dead     atomic.Bool // the node rebooted under it without closing it (TCP)
}

func newLink() *link {
	return &link{toDevice: make(chan envelope), toClient: make(chan *pb.FromRadio), done: make(chan struct{})}
}

// clientConn is the client-facing end of a link.
type clientConn struct {
	l    *link
	kind transport.Kind
}

func (c *clientConn) Send(ctx context.Context, m *pb.ToRadio) error {
	select {
	case <-c.l.done:
		return fmt.Errorf("fakedevice send: %w", transport.ErrClosed)
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	e := envelope{m: proto.Clone(m).(*pb.ToRadio), seen: make(chan struct{})}
	select {
	case c.l.toDevice <- e:
	case <-c.l.done:
		return fmt.Errorf("fakedevice send: %w", transport.ErrClosed)
	case <-ctx.Done():
		return ctx.Err()
	}
	// Delivered; the device records it without blocking.
	select {
	case <-e.seen:
	case <-c.l.done:
	}
	return nil
}

func (c *clientConn) Recv(ctx context.Context) (*pb.FromRadio, error) {
	select {
	case <-c.l.done:
		return nil, fmt.Errorf("fakedevice recv: %w", transport.ErrClosed)
	default:
	}
	select {
	case m := <-c.l.toClient:
		return m, nil
	case <-c.l.done:
		return nil, fmt.Errorf("fakedevice recv: %w", transport.ErrClosed)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *clientConn) Kind() transport.Kind { return c.kind }

func (c *clientConn) Close() error { return c.l.close() }

func (l *link) close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

// emit sends a FromRadio from the device side; false when the link is gone.
func (l *link) emit(m *pb.FromRadio) bool {
	select {
	case l.toClient <- m:
		return true
	case <-l.done:
		return false
	}
}

// next reads a ToRadio on the device side; ok is false when the link is gone.
func (l *link) next() (e envelope, ok bool) {
	select {
	case e := <-l.toDevice:
		return e, true
	case <-l.done:
		return envelope{}, false
	}
}

func (d *Device) serve(l *link) {
	defer l.close()
	var st linkState
	for {
		e, ok := l.next()
		if !ok {
			return
		}
		adm := d.record(e.m)
		close(e.seen)
		if st.hung || l.dead.Load() {
			continue
		}
		switch v := e.m.PayloadVariant.(type) {
		case *pb.ToRadio_WantConfigId:
			d.mu.Lock()
			drop := d.DropHandshakesFrom > 0 && l.dial >= d.DropHandshakesFrom
			silent := d.Silent
			reboot := !drop && !silent && d.handshakeReboots > 0
			if reboot {
				d.handshakeReboots--
			}
			d.mu.Unlock()
			if drop {
				return // closes the link
			}
			if silent {
				continue
			}
			if reboot {
				if !l.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Rebooted{Rebooted: true}}) {
					return
				}
				continue
			}
			if !d.handshake(l, v.WantConfigId) {
				return
			}
			d.mu.Lock()
			after := d.DropAfterHandshake
			d.mu.Unlock()
			if after > 0 {
				time.AfterFunc(after, func() { l.close() })
			}
		case *pb.ToRadio_Packet:
			d.mu.Lock()
			remote := d.isRemote(v.Packet)
			d.mu.Unlock()
			if remote {
				d.mu.Lock()
				msg, refused := d.RefuseSending[v.Packet.GetDecoded().GetPortnum()]
				d.mu.Unlock()
				if refused {
					note := &pb.FromRadio{PayloadVariant: &pb.FromRadio_ClientNotification{ClientNotification: &pb.ClientNotification{
						ReplyId: proto.Uint32(v.Packet.GetId()), Level: pb.LogRecord_WARNING, Message: msg}}}
					if !l.emit(note) {
						return
					}
					continue
				}
				d.toRemote(l, v.Packet)
				continue
			}
			if adm != nil && !d.handleAdmin(l, &st, v.Packet, adm) {
				return
			}
			if v.Packet.GetDecoded().GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP && !d.ackText(l, v.Packet) {
				return
			}
		}
	}
}

// record counts heartbeats and stores admin messages; it returns the decoded
// admin message, or nil if m is not one.
func (d *Device) record(m *pb.ToRadio) *pb.AdminMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	if m.GetHeartbeat() != nil {
		d.heartbeats++
		return nil
	}
	if m.GetPacket() != nil && d.isRemote(m.GetPacket()) {
		return nil // recorded by toRemote
	}
	data := m.GetPacket().GetDecoded()
	if data.GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP {
		d.texts = append(d.texts, proto.Clone(m.GetPacket()).(*pb.MeshPacket))
		return nil
	}
	if data.GetPortnum() != pb.PortNum_ADMIN_APP {
		return nil
	}
	adm := &pb.AdminMessage{}
	if err := proto.Unmarshal(data.GetPayload(), adm); err != nil {
		return nil
	}
	d.received = append(d.received, proto.Clone(adm).(*pb.AdminMessage))
	return adm
}

// handshake answers want_config_id in firmware order.
func (d *Device) handshake(l *link, nonce uint32) bool {
	d.mu.Lock()
	var msgs []*pb.FromRadio
	if d.StaleCompleteID {
		msgs = append(msgs, &pb.FromRadio{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: nonce + 1}})
	}
	msgs = append(msgs,
		&pb.FromRadio{PayloadVariant: &pb.FromRadio_MyInfo{MyInfo: &pb.MyNodeInfo{MyNodeNum: d.NodeNum}}},
		&pb.FromRadio{PayloadVariant: &pb.FromRadio_Metadata{Metadata: &pb.DeviceMetadata{FirmwareVersion: d.FirmwareVersion}}},
		&pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: &pb.NodeInfo{Num: d.NodeNum, User: d.Owner, Position: d.Position}}},
	)
	for _, n := range d.ExtraNodes {
		msgs = append(msgs, &pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: n}})
	}
	for _, num := range slices.Sorted(maps.Keys(d.Remotes)) {
		r := d.Remotes[num]
		msgs = append(msgs, &pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: &pb.NodeInfo{Num: num, User: r.Owner}}})
	}
	d.wantConfigs++
	for _, c := range configpath.ConfigSections(d.Config) {
		msgs = append(msgs, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Config{Config: c}})
	}
	for _, c := range configpath.ModuleSections(d.ModuleConfig) {
		msgs = append(msgs, &pb.FromRadio{PayloadVariant: &pb.FromRadio_ModuleConfig{ModuleConfig: c}})
	}
	for _, ch := range d.Channels {
		msgs = append(msgs, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Channel{Channel: ch}})
	}
	msgs = append(msgs, &pb.FromRadio{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: nonce}})
	if l.dial == 1 && !d.backlogSent {
		d.backlogSent = true
		for _, p := range d.Backlog {
			msgs = append(msgs, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: p}})
		}
	}
	// Clone under the lock so tests can mutate the fields afterwards.
	for i, m := range msgs {
		msgs[i] = proto.Clone(m).(*pb.FromRadio)
	}
	d.mu.Unlock()
	for _, m := range msgs {
		if !l.emit(m) {
			return false
		}
	}
	return true
}

// Update runs f under the device lock, for changing fields (including a
// Remote's) while links are open.
func (d *Device) Update(f func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f()
}

// WantConfigs reports how many want_config requests the device has answered.
func (d *Device) WantConfigs() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.wantConfigs
}

// Inject emits p as a received packet on the current link; a no-op when
// there is no open link.
func (d *Device) Inject(p *pb.MeshPacket) {
	d.mu.Lock()
	l := d.cur
	m := &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: proto.Clone(p).(*pb.MeshPacket)}}
	d.mu.Unlock()
	if l != nil {
		l.emit(m)
	}
}

// DropLink closes the current link from the device side, as when the node
// kicks the client or the cable is pulled.
func (d *Device) DropLink() {
	d.mu.Lock()
	l := d.cur
	d.mu.Unlock()
	if l != nil {
		l.close()
	}
}

// Texts returns copies of every text message packet the device has received.
func (d *Device) Texts() []*pb.MeshPacket {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*pb.MeshPacket, len(d.texts))
	for i, p := range d.texts {
		out[i] = proto.Clone(p).(*pb.MeshPacket)
	}
	return out
}

// ackText answers a text packet as configured by ImplicitAckTexts (a NONE
// ROUTING_APP reply from the local node for a packet sent with want_ack, as
// firmware does when it hears a neighbour rebroadcast it) and AckTexts (a
// direct message's ack from the destination); false when the link is gone.
func (d *Device) ackText(l *link, p *pb.MeshPacket) bool {
	d.mu.Lock()
	ack, implicit, reason, num := d.AckTexts, d.ImplicitAckTexts, d.AckError, d.NodeNum
	d.mu.Unlock()
	reply := func(from uint32, reason pb.Routing_Error) bool {
		payload, err := proto.Marshal(&pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: reason}})
		if err != nil {
			panic(err)
		}
		pkt := &pb.MeshPacket{From: from, To: num, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{
			Portnum: pb.PortNum_ROUTING_APP, Payload: payload, RequestId: p.GetId(),
		}}}
		return l.emit(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: pkt}})
	}
	if implicit && p.GetWantAck() && !reply(num, pb.Routing_NONE) {
		return false
	}
	if p.GetTo() == 0xffffffff {
		return true
	}
	return !ack || reply(p.GetTo(), reason)
}
