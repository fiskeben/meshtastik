// Package device is the core client: it performs the Meshtastic want_config
// handshake over a transport.Conn and keeps an in-memory State.
package device

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
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
	HandshakeTimeout  time.Duration // default 15s; also bounds each admin request
	ReconnectTimeout  time.Duration // default 90s
	HeartbeatInterval time.Duration // default 5m
	RebootGrace       time.Duration // default 3s: wait after a commit for the node to reboot
	Log               io.Writer     // default io.Discard

	// Listen's handling of dropped links (see Listen).
	StableLink     time.Duration // default 60s: a link that lived shorter counts as short-lived when it drops
	ReconnectDelay time.Duration // default 1s: wait before redialling after a drop; doubles while attempts fail or links are short-lived
	// OnReconnect, when set, is called by Listen after a dropped link and
	// after each failed reconnect attempt, with the cause and the wait
	// before redialling.
	OnReconnect func(cause error, wait time.Duration)
	// OnReconnected, when set, is called by Listen once a reconnect after a
	// dropped link has succeeded.
	OnReconnected func()
	// OnMessage, when set, is called on the reader goroutine with each
	// received message that is neither a packet nor part of the handshake
	// (rebooted, queue status, client notifications, …). It must not block.
	OnMessage func(*pb.FromRadio)
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
	if o.RebootGrace <= 0 {
		o.RebootGrace = 3 * time.Second
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.StableLink <= 0 {
		o.StableLink = 60 * time.Second
	}
	if o.ReconnectDelay <= 0 {
		o.ReconnectDelay = time.Second
	}
}

// Redial backoff after a reboot: starts here, doubles, capped.
const (
	reconnectBackoff    = 500 * time.Millisecond
	maxReconnectBackoff = 5 * time.Second
)

var (
	errClientClosed = errors.New("client closed")
	// errDroppedInHandshake: the link closed before config_complete_id, as
	// when another client takes the node's only TCP connection.
	errDroppedInHandshake = errors.New("node closed the connection")
	errRebooted           = errors.New("node rebooted during handshake")
	errHandshakeTimeout   = errors.New("no response to handshake")
)

// link is one physical connection with its own reader and heartbeat
// goroutines. A reconnect replaces the whole link.
type link struct {
	conn   transport.Conn
	since  time.Time // when the link was attached
	sendMu sync.Mutex

	readerEnd chan struct{} // closed when the reader goroutine exits
	readErr   error         // set before readerEnd closes

	rebooted chan struct{} // holds one pending "node rebooted" event; receiving consumes it

	mu       sync.Mutex    // guards nonce and complete
	nonce    uint32        // of the handshake in progress
	complete chan struct{} // closed when its config_complete_id arrives

	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// send serialises writes to the connection.
func (l *link) send(ctx context.Context, m *pb.ToRadio) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	return l.conn.Send(ctx, m)
}

// startHandshake arms a fresh nonce and returns it with its completion signal.
func (l *link) startHandshake() (uint32, chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nonce = newNonce()
	l.complete = make(chan struct{})
	return l.nonce, l.complete
}

func (l *link) configComplete(id uint32) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.complete == nil || id != l.nonce {
		return
	}
	select {
	case <-l.complete:
	default:
		close(l.complete)
	}
}

func (l *link) markRebooted() {
	select {
	case l.rebooted <- struct{}{}:
	default: // one pending event is enough
	}
}

// close stops the goroutines and closes the connection; safe to repeat.
func (l *link) close() error {
	l.closeOnce.Do(func() {
		l.cancel()
		l.closeErr = l.conn.Close()
		l.wg.Wait()
	})
	return l.closeErr
}

// Client is a handshaken connection to a local node.
type Client struct {
	dial transport.Dialer // kept for reconnecting after a reboot
	opts Options

	connMu sync.Mutex // guards cur and closed
	cur    *link
	closed bool

	mu      sync.Mutex // guards state and passkey
	state   *State
	passkey []byte // admin session passkey, refreshed from admin responses

	wmu     sync.Mutex
	waiters map[uint32]chan *pb.MeshPacket // by request id

	packets packetQueue // every MeshPacket received, across reconnects, in its FromRadio

	applyMu   sync.Mutex // serialises Apply and Listen's reconnects
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
		dial:    dial,
		opts:    opts,
		state:   newState(),
		waiters: map[uint32]chan *pb.MeshPacket{},
		packets: packetQueue{wake: make(chan struct{}, 1)},
	}
	l, err := c.attach(conn)
	if err != nil {
		return nil, err
	}
	if err := c.handshake(ctx, l, opts.HandshakeTimeout, true); err != nil {
		c.Close()
		if errors.Is(err, errDroppedInHandshake) && conn.Kind() == transport.KindTCP {
			err = fmt.Errorf("the node closed the connection during the handshake — another client (e.g. Home Assistant "+
				"or the phone app) connected over WiFi has probably taken the node's only TCP connection; "+
				"stop it or connect over serial/BLE (%w)", err)
		}
		return nil, err
	}
	if v := c.State().Metadata.GetFirmwareVersion(); !supported(v) {
		c.Close()
		if v == "" {
			v = "unknown firmware version"
		}
		return nil, fmt.Errorf("%w: %s (need 2.5 or newer)", ErrUnsupportedFirmware, v)
	}
	return c, nil
}

// attach starts the reader and heartbeat on conn and makes it the current link.
func (c *Client) attach(conn transport.Conn) (*link, error) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &link{conn: conn, since: time.Now(), readerEnd: make(chan struct{}), rebooted: make(chan struct{}, 1), cancel: cancel}
	l.wg.Add(2)
	go c.readLoop(ctx, l)
	go c.heartbeatLoop(ctx, l)
	c.connMu.Lock()
	if c.closed {
		c.connMu.Unlock()
		l.close()
		return nil, errClientClosed
	}
	c.cur = l
	c.connMu.Unlock()
	return l, nil
}

func (c *Client) current() *link {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.cur
}

// randomID returns a random uint32 that is non-zero and not in skip.
func randomID(skip ...uint32) uint32 {
	var b [4]byte
	for {
		rand.Read(b[:])
		if n := binary.BigEndian.Uint32(b[:]); n != 0 && !slices.Contains(skip, n) {
			return n
		}
	}
}

// newNonce avoids the special want_config nonces: 69420 asks for config
// only, 69421 for the node DB only.
func newNonce() uint32 { return randomID(69420, 69421) }

// handshake runs want_config on l with a fresh nonce and waits for
// completion. If the node reports a reboot meanwhile, the request was lost:
// with resendOnReboot it is sent again with a fresh nonce and a fresh
// timeout, otherwise handshake returns errRebooted.
func (c *Client) handshake(ctx context.Context, l *link, timeout time.Duration, resendOnReboot bool) error {
	for {
		err := c.handshakeOnce(ctx, l, timeout)
		if !errors.Is(err, errRebooted) || !resendOnReboot {
			return err
		}
		fmt.Fprintln(c.opts.Log, "node rebooted during handshake; asking again")
	}
}

func (c *Client) handshakeOnce(ctx context.Context, l *link, timeout time.Duration) error {
	nonce, complete := l.startHandshake()
	req := &pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: nonce}}
	if err := l.send(ctx, req); err != nil {
		return fmt.Errorf("start handshake: %w", err)
	}
	const hint = "is another program connected to the node?"
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-complete:
		return nil
	case <-l.rebooted:
		return errRebooted
	case <-l.readerEnd:
		return fmt.Errorf("handshake: %w: %w", errDroppedInHandshake, l.readErr)
	case <-timer.C:
		return fmt.Errorf("%w after %s on %s — %s", errHandshakeTimeout, timeout, l.conn.Kind(), hint)
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w on %s (%w) — %s", errHandshakeTimeout, l.conn.Kind(), ctx.Err(), hint)
		}
		return ctx.Err()
	}
}

// reconnectReason says why reconnect runs; it shapes the error messages.
type reconnectReason int

const (
	afterReboot reconnectReason = iota // the node rebooted (Apply)
	afterDrop                          // the link dropped (Listen)
)

func (r reconnectReason) String() string {
	if r == afterDrop {
		return "after the connection dropped"
	}
	return "after reboot"
}

// contestedError is returned once the connection has dropped drops times
// during or soon after reconnecting.
func contestedError(drops int, within time.Duration) error {
	return fmt.Errorf("connection dropped %d times within %s, each time during or soon after reconnecting — "+
		"another client (Home Assistant, the phone app) is probably using the node's connection; "+
		"stop it or connect over serial/BLE", drops, within)
}

// reconnect replaces the current link after a reboot or a dropped link: it
// redials with backoff and re-runs the handshake until ReconnectTimeout.
// After a drop the backoff starts at Options.ReconnectDelay, each failed
// attempt is reported through Options.OnReconnect, and shortDropLimit
// attempts that the node closes mid-handshake end it with contestedError.
func (c *Client) reconnect(ctx context.Context, after reconnectReason) error {
	if l := c.current(); l != nil {
		l.close()
	}
	c.setPasskey(nil) // the node makes a new session key on boot
	rctx, cancel := context.WithTimeout(ctx, c.opts.ReconnectTimeout)
	defer cancel()
	delay := reconnectBackoff
	if after == afterDrop {
		delay = c.opts.ReconnectDelay
	}
	dropped := 0 // attempts the node closed mid-handshake
	for {
		err := c.redial(rctx)
		if err == nil || errors.Is(err, errClientClosed) {
			return err
		}
		if after == afterDrop {
			if errors.Is(err, errDroppedInHandshake) && rctx.Err() == nil {
				if dropped++; dropped >= shortDropLimit {
					return contestedError(dropped, c.opts.ReconnectTimeout)
				}
			}
			if rctx.Err() == nil && c.opts.OnReconnect != nil {
				c.opts.OnReconnect(fmt.Errorf("reconnect failed: %w", err), delay)
			}
		}
		t := time.NewTimer(delay)
		select {
		case <-rctx.Done():
			t.Stop()
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for the node %s: %w", after, ctx.Err())
			}
			if after == afterReboot && errors.Is(err, errHandshakeTimeout) {
				err = errHandshakeTimeout // the "another program" hint does not apply mid-reboot
			}
			return fmt.Errorf("node did not come back %s within %s: %v", after, c.opts.ReconnectTimeout, err)
		case <-t.C:
		}
		delay = min(delay*2, maxReconnectBackoff)
	}
}

func (c *Client) redial(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	l, err := c.attach(conn)
	if err != nil {
		return err
	}
	if err := c.handshake(ctx, l, c.opts.HandshakeTimeout, true); err != nil {
		l.close()
		return err
	}
	return nil
}

// send writes to the current link.
func (c *Client) send(ctx context.Context, m *pb.ToRadio) error {
	return c.current().send(ctx, m)
}

// Send writes m to the node as it is, for callers that relay messages built
// elsewhere.
func (c *Client) Send(ctx context.Context, m *pb.ToRadio) error { return c.send(ctx, m) }

// Refresh re-reads the node's state by running the handshake again on the
// current connection.
func (c *Client) Refresh(ctx context.Context) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	return c.handshake(ctx, c.current(), c.opts.HandshakeTimeout, true)
}

// readLoop is the only goroutine that reads from l's connection.
func (c *Client) readLoop(ctx context.Context, l *link) {
	defer l.wg.Done()
	defer close(l.readerEnd)
	for {
		m, err := l.conn.Recv(ctx)
		if err != nil {
			l.readErr = err
			return
		}
		c.dispatch(l, m)
	}
}

// dispatch routes one FromRadio message from l by payload variant.
func (c *Client) dispatch(l *link, m *pb.FromRadio) {
	switch v := m.PayloadVariant.(type) {
	case *pb.FromRadio_MyInfo:
		c.update(func(s *State) { s.MyInfo = v.MyInfo })
	case *pb.FromRadio_Metadata:
		c.update(func(s *State) { s.Metadata = v.Metadata })
	case *pb.FromRadio_NodeInfo:
		if n := v.NodeInfo; n != nil {
			c.update(func(s *State) { s.Nodes[n.GetNum()] = n })
		}
	case *pb.FromRadio_Config:
		c.update(func(s *State) { configpath.MergeConfig(s.Config, v.Config) })
	case *pb.FromRadio_ModuleConfig:
		c.update(func(s *State) { configpath.MergeModuleConfig(s.ModuleConfig, v.ModuleConfig) })
	case *pb.FromRadio_Channel:
		if i := v.Channel.GetIndex(); i >= 0 && int(i) < len(State{}.Channels) {
			c.update(func(s *State) { s.Channels[i] = v.Channel })
		}
	case *pb.FromRadio_ConfigCompleteId:
		l.configComplete(v.ConfigCompleteId)
	case *pb.FromRadio_Rebooted:
		if v.Rebooted {
			l.markRebooted()
		}
		if c.opts.OnMessage != nil {
			c.opts.OnMessage(m)
		}
	case *pb.FromRadio_Packet:
		p := v.Packet
		if p == nil {
			return
		}
		if id := p.GetDecoded().GetRequestId(); id != 0 {
			c.deliver(id, p)
		}
		if p.GetRxTime() != 0 { // received over the radio
			c.update(func(s *State) {
				if n, ok := s.Nodes[p.GetFrom()]; ok {
					n.LastHeard = p.GetRxTime()
					n.Snr = p.GetRxSnr()
				}
			})
		}
		c.packets.push(m)
	default:
		if c.opts.OnMessage != nil {
			c.opts.OnMessage(m)
		} else {
			fmt.Fprintf(c.opts.Log, "ignoring %T\n", m.PayloadVariant)
		}
	}
}

func (c *Client) update(f func(*State)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c.state)
}

// addWaiter registers interest in packets answering request id.
func (c *Client) addWaiter(id uint32) chan *pb.MeshPacket {
	ch := make(chan *pb.MeshPacket, 4)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.waiters[id] = ch
	return ch
}

func (c *Client) removeWaiter(id uint32) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	delete(c.waiters, id)
}

// deliver hands p to the waiter for request id, if any, without blocking.
func (c *Client) deliver(id uint32, p *pb.MeshPacket) {
	c.wmu.Lock()
	ch := c.waiters[id]
	c.wmu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- p:
	default:
		fmt.Fprintf(c.opts.Log, "dropping extra response to request %d\n", id)
	}
}

func (c *Client) heartbeatLoop(ctx context.Context, l *link) {
	defer l.wg.Done()
	t := time.NewTicker(c.opts.HeartbeatInterval)
	defer t.Stop()
	hb := &pb.ToRadio{PayloadVariant: &pb.ToRadio_Heartbeat{Heartbeat: &pb.Heartbeat{}}}
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.readerEnd:
			return
		case <-t.C:
			if err := l.send(ctx, hb); err != nil {
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
func (c *Client) Kind() transport.Kind { return c.current().conn.Kind() }

// Close shuts the connection down and waits for the background goroutines.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.connMu.Lock()
		c.closed = true
		l := c.cur
		c.connMu.Unlock()
		if l != nil {
			err = l.close()
		}
	})
	return err
}
