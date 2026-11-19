// Package fakedevice is an in-memory Meshtastic node for tests. It speaks the
// want_config handshake over an in-memory transport.Conn.
package fakedevice

import (
	"context"
	"fmt"
	"sync"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"meshtastik/internal/configpath"
	"meshtastik/internal/transport"
)

// Device is a fake node. Set fields before the first Dial; the fields are read
// when a handshake is answered.
type Device struct {
	NodeNum         uint32
	FirmwareVersion string
	Kind            transport.Kind
	Owner           *pb.User
	Config          *pb.LocalConfig
	ModuleConfig    *pb.LocalModuleConfig
	Channels        [8]*pb.Channel
	ExtraNodes      []*pb.NodeInfo
	Silent          bool // never answer want_config
	StaleCompleteID bool // send one config_complete_id with a wrong nonce first

	mu         sync.Mutex
	dials      int
	heartbeats int
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
		d.mu.Unlock()
		l := newLink()
		go d.serve(l)
		return &clientConn{l: l, kind: d.Kind}, nil
	}
}

// link is the shared state of one in-memory connection.
type link struct {
	toDevice chan *pb.ToRadio
	toClient chan *pb.FromRadio
	done     chan struct{}
	once     sync.Once
}

func newLink() *link {
	return &link{toDevice: make(chan *pb.ToRadio), toClient: make(chan *pb.FromRadio), done: make(chan struct{})}
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
	select {
	case c.l.toDevice <- proto.Clone(m).(*pb.ToRadio):
		return nil
	case <-c.l.done:
		return fmt.Errorf("fakedevice send: %w", transport.ErrClosed)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *clientConn) Recv(ctx context.Context) (*pb.FromRadio, error) {
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

// next reads a ToRadio on the device side; nil when the link is gone.
func (l *link) next() *pb.ToRadio {
	select {
	case m := <-l.toDevice:
		return m
	case <-l.done:
		return nil
	}
}

func (d *Device) serve(l *link) {
	defer l.close()
	for {
		m := l.next()
		if m == nil {
			return
		}
		switch v := m.PayloadVariant.(type) {
		case *pb.ToRadio_WantConfigId:
			if d.Silent {
				continue
			}
			if !d.handshake(l, v.WantConfigId) {
				return
			}
		case *pb.ToRadio_Heartbeat:
			d.mu.Lock()
			d.heartbeats++
			d.mu.Unlock()
		}
	}
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
		&pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: &pb.NodeInfo{Num: d.NodeNum, User: d.Owner}}},
	)
	for _, n := range d.ExtraNodes {
		msgs = append(msgs, &pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: n}})
	}
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
