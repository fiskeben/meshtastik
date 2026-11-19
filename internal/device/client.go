// Package device is the core client: it performs the Meshtastic want_config
// handshake over a transport.Conn and keeps an in-memory State.
package device

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/configpath"
	"meshtastik/internal/transport"
)

// State is what the node reported during the handshake, kept current afterwards.
type State struct {
	MyInfo       *pb.MyNodeInfo
	Metadata     *pb.DeviceMetadata
	Nodes        map[uint32]*pb.NodeInfo
	Config       *pb.LocalConfig
	ModuleConfig *pb.LocalModuleConfig
	Channels     [8]*pb.Channel
}

// Self returns the NodeInfo of the connected node, or nil if unknown.
func (s *State) Self() *pb.NodeInfo {
	if s.MyInfo == nil {
		return nil
	}
	return s.Nodes[s.MyInfo.MyNodeNum]
}

func newState() *State {
	return &State{Nodes: map[uint32]*pb.NodeInfo{}, Config: &pb.LocalConfig{}, ModuleConfig: &pb.LocalModuleConfig{}}
}

func (s *State) clone() *State {
	out := &State{
		Nodes:        make(map[uint32]*pb.NodeInfo, len(s.Nodes)),
		Config:       proto.Clone(s.Config).(*pb.LocalConfig),
		ModuleConfig: proto.Clone(s.ModuleConfig).(*pb.LocalModuleConfig),
	}
	if s.MyInfo != nil {
		out.MyInfo = proto.Clone(s.MyInfo).(*pb.MyNodeInfo)
	}
	if s.Metadata != nil {
		out.Metadata = proto.Clone(s.Metadata).(*pb.DeviceMetadata)
	}
	for k, n := range s.Nodes {
		out.Nodes[k] = proto.Clone(n).(*pb.NodeInfo)
	}
	for i, c := range s.Channels {
		if c != nil {
			out.Channels[i] = proto.Clone(c).(*pb.Channel)
		}
	}
	return out
}

// Options tunes a Client; zero values select the defaults.
type Options struct {
	HandshakeTimeout  time.Duration // default 15s
	ReconnectTimeout  time.Duration // default 90s
	HeartbeatInterval time.Duration // default 5m
	Log               io.Writer     // default io.Discard
}

func (o *Options) defaults() {
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = 15 * time.Second
	}
	if o.ReconnectTimeout <= 0 {
		o.ReconnectTimeout = 90 * time.Second
	}
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = 5 * time.Minute
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
}

// Client is a handshaken connection to a local node.
type Client struct {
	dial transport.Dialer // kept for reconnecting after a reboot
	opts Options

	conn   transport.Conn
	sendMu sync.Mutex

	mu    sync.Mutex // guards state
	state *State

	nonce     uint32
	complete  chan struct{} // closed when the handshake's config_complete_id arrives
	readerEnd chan struct{} // closed when the reader goroutine exits
	readErr   error         // set before readerEnd closes

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// Open dials the node and performs the want_config handshake.
func Open(ctx context.Context, dial transport.Dialer, opts Options) (*Client, error) {
	opts.defaults()
	conn, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	c := &Client{
		dial:      dial,
		opts:      opts,
		conn:      conn,
		state:     newState(),
		nonce:     newNonce(),
		complete:  make(chan struct{}),
		readerEnd: make(chan struct{}),
	}
	rctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.wg.Add(1)
	go c.readLoop(rctx)

	if err := c.handshake(ctx); err != nil {
		c.Close()
		return nil, err
	}
	if v := c.State().Metadata.GetFirmwareVersion(); !supported(v) {
		c.Close()
		if v == "" {
			v = "unknown firmware version"
		}
		return nil, fmt.Errorf("%w: %s (need 2.5 or newer)", ErrUnsupportedFirmware, v)
	}
	c.wg.Add(1)
	go c.heartbeatLoop(rctx)
	return c, nil
}

func newNonce() uint32 {
	var b [4]byte
	for {
		if _, err := rand.Read(b[:]); err == nil {
			if n := binary.BigEndian.Uint32(b[:]); n != 0 {
				return n
			}
		}
	}
}

func (c *Client) handshake(ctx context.Context) error {
	req := &pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: c.nonce}}
	if err := c.send(ctx, req); err != nil {
		return fmt.Errorf("start handshake: %w", err)
	}
	timer := time.NewTimer(c.opts.HandshakeTimeout)
	defer timer.Stop()
	select {
	case <-c.complete:
		return nil
	case <-c.readerEnd:
		return fmt.Errorf("handshake: %w", c.readErr)
	case <-timer.C:
		return fmt.Errorf("no response to handshake after %s on %s — is another program connected to the node?", c.opts.HandshakeTimeout, c.conn.Kind())
	case <-ctx.Done():
		return ctx.Err()
	}
}

// send serialises writes to the connection.
func (c *Client) send(ctx context.Context, m *pb.ToRadio) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.conn.Send(ctx, m)
}

// readLoop is the only goroutine that reads from the connection.
func (c *Client) readLoop(ctx context.Context) {
	defer c.wg.Done()
	defer close(c.readerEnd)
	for {
		m, err := c.conn.Recv(ctx)
		if err != nil {
			c.readErr = err
			return
		}
		c.dispatch(m)
	}
}

// dispatch routes one FromRadio message by payload variant.
func (c *Client) dispatch(m *pb.FromRadio) {
	switch v := m.PayloadVariant.(type) {
	case *pb.FromRadio_MyInfo:
		c.update(func(s *State) { s.MyInfo = v.MyInfo })
	case *pb.FromRadio_Metadata:
		c.update(func(s *State) { s.Metadata = v.Metadata })
	case *pb.FromRadio_NodeInfo:
		c.update(func(s *State) { s.Nodes[v.NodeInfo.Num] = v.NodeInfo })
	case *pb.FromRadio_Config:
		c.update(func(s *State) { configpath.MergeConfig(s.Config, v.Config) })
	case *pb.FromRadio_ModuleConfig:
		c.update(func(s *State) { configpath.MergeModuleConfig(s.ModuleConfig, v.ModuleConfig) })
	case *pb.FromRadio_Channel:
		if i := v.Channel.GetIndex(); i >= 0 && int(i) < len(State{}.Channels) {
			c.update(func(s *State) { s.Channels[i] = v.Channel })
		}
	case *pb.FromRadio_ConfigCompleteId:
		if v.ConfigCompleteId == c.nonce {
			select {
			case <-c.complete:
			default:
				close(c.complete)
			}
		}
	case *pb.FromRadio_Packet:
		c.update(func(s *State) {
			if n, ok := s.Nodes[v.Packet.GetFrom()]; ok {
				n.LastHeard = v.Packet.GetRxTime()
				n.Snr = v.Packet.GetRxSnr()
			}
		})
	default:
		fmt.Fprintf(c.opts.Log, "ignoring %T\n", m.PayloadVariant)
	}
}

func (c *Client) update(f func(*State)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c.state)
}

func (c *Client) heartbeatLoop(ctx context.Context) {
	defer c.wg.Done()
	t := time.NewTicker(c.opts.HeartbeatInterval)
	defer t.Stop()
	hb := &pb.ToRadio{PayloadVariant: &pb.ToRadio_Heartbeat{Heartbeat: &pb.Heartbeat{}}}
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.readerEnd:
			return
		case <-t.C:
			if err := c.send(ctx, hb); err != nil {
				fmt.Fprintf(c.opts.Log, "heartbeat: %v\n", err)
			}
		}
	}
}

// State returns a deep copy of the current node state.
func (c *Client) State() *State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.clone()
}

// Kind reports the transport kind of the connection.
func (c *Client) Kind() transport.Kind { return c.conn.Kind() }

// Close shuts the connection down and waits for the background goroutines.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.cancel()
		err = c.conn.Close()
		c.wg.Wait()
	})
	return err
}
