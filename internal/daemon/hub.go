package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/configpath"
	"meshtastik/internal/device"
	"meshtastik/internal/transport"
)

const (
	// backlogSize is how many packets a hub keeps for the next backlog client.
	backlogSize = 256
	// maxStateAge is how old the cached node state may get before a client
	// handshake re-reads it from the node.
	maxStateAge = 10 * time.Minute
	// sessionQueue is how many messages may wait for a slow client before
	// the daemon disconnects it.
	sessionQueue = 1024
)

var errHubClosed = errors.New("daemon closed the connection to the node")

// hub is one node connection shared by the sessions attached to it.
type hub struct {
	s      *Server
	target Target
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when run returns

	refreshMu sync.Mutex // one state re-read at a time

	mu       sync.Mutex
	c        *device.Client // set once connected
	state    string         // connecting, connected, reconnecting
	up       chan struct{}  // closed when connected or failed; replaced on a drop
	err      error          // why the hub failed; set before up closes for good
	sessions map[*session]struct{}
	backlogN int // attached sessions that take the backlog
	backlog  []*pb.FromRadio
	stale    bool      // the node's state may have changed since readAt
	readAt   time.Time // when the node's state was last read
	since    time.Time // when state last changed
	lastUsed time.Time // when the last session detached
}

func newHub(s *Server, t Target) *hub {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	return &hub{s: s, target: t, ctx: ctx, cancel: cancel, done: make(chan struct{}),
		state: "connecting", up: make(chan struct{}), sessions: map[*session]struct{}{}, since: now, lastUsed: now}
}

// run opens the node connection and relays received packets until the
// connection fails for good or the hub is closed.
func (h *hub) run(timeout time.Duration) {
	defer close(h.done)
	defer h.s.removeHub(h)
	dial, err := h.s.Dial(h.target)
	if err != nil {
		h.fail(err)
		return
	}
	opts := device.Options{
		HandshakeTimeout: timeout,
		Log:              h.s.Log,
		OnReconnect:      h.dropped,
		OnReconnected:    h.reconnected,
		OnMessage:        h.message,
	}
	if h.s.DeviceOptions != nil {
		h.s.DeviceOptions(&opts)
	}
	c, err := device.Open(h.ctx, dial, opts)
	if err != nil {
		if h.ctx.Err() != nil {
			err = errHubClosed
		}
		h.fail(err)
		return
	}
	defer c.Close()
	st := c.State()
	h.mu.Lock()
	h.c = c
	h.setState("connected")
	h.readAt = time.Now()
	close(h.up)
	h.mu.Unlock()
	h.s.logf("%s: connected to %s (%s)", h.target, nodeLabel(st, st.MyInfo.GetMyNodeNum()), c.Kind())
	err = c.Listen(h.ctx, h.packet)
	if err == nil {
		err = errHubClosed
	}
	h.fail(err)
}

// setState records a new state; h.mu must be held.
func (h *hub) setState(s string) {
	h.state = s
	h.since = time.Now()
}

// fail ends the hub with err: waiting clients get it and sessions close.
func (h *hub) fail(err error) {
	h.mu.Lock()
	h.err = err
	select {
	case <-h.up:
	default:
		close(h.up)
	}
	sessions := h.takeSessions()
	h.mu.Unlock()
	h.s.logf("%s: %v", h.target, err)
	for _, s := range sessions {
		s.close()
	}
}

// takeSessions detaches every session; h.mu must be held.
func (h *hub) takeSessions() []*session {
	out := make([]*session, 0, len(h.sessions))
	for s := range h.sessions {
		out = append(out, s)
	}
	clear(h.sessions)
	h.backlogN = 0
	h.lastUsed = time.Now()
	return out
}

// close shuts the node connection and waits for run to return.
func (h *hub) close() {
	h.cancel()
	<-h.done
}

// dropped is called by Listen when the node connection drops and after each
// failed reconnect: sessions are closed so clients run their own reconnect
// logic, and new clients wait until the node is back.
func (h *hub) dropped(cause error, wait time.Duration) {
	h.mu.Lock()
	if h.state != "connected" {
		h.mu.Unlock()
		h.s.logf("%s: %v; retrying in %s", h.target, cause, wait)
		return
	}
	h.setState("reconnecting")
	h.up = make(chan struct{})
	sessions := h.takeSessions()
	h.mu.Unlock()
	h.s.logf("%s: %v; reconnecting in %s", h.target, cause, wait)
	for _, s := range sessions {
		s.close()
	}
}

func (h *hub) reconnected() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.setState("connected")
	h.readAt, h.stale = time.Now(), false
	close(h.up)
	h.s.logf("%s: reconnected", h.target)
}

// waitUp waits until the node connection is up and returns its transport.
func (h *hub) waitUp(ctx context.Context) (transport.Kind, error) {
	for {
		h.mu.Lock()
		err, up, c := h.err, h.up, h.c
		connected := h.state == "connected"
		h.mu.Unlock()
		if err != nil {
			return 0, err
		}
		if connected {
			return c.Kind(), nil
		}
		select {
		case <-up:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// attach adds s unless the hub is not connected (any more).
func (h *hub) attach(s *session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil || h.state != "connected" {
		return false
	}
	h.sessions[s] = struct{}{}
	if s.wantBacklog {
		h.backlogN++
	}
	return true
}

func (h *hub) detach(s *session) {
	h.mu.Lock()
	if _, ok := h.sessions[s]; ok {
		delete(h.sessions, s)
		if s.wantBacklog {
			h.backlogN--
		}
		h.lastUsed = time.Now()
	}
	h.mu.Unlock()
	s.close()
}

// unusedSince is how long the hub has had no sessions (0 while it has some).
func (h *hub) unusedSince(now time.Time) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sessions) > 0 {
		return 0
	}
	return now.Sub(h.lastUsed)
}

// packet fans a received packet out to every session and, while no backlog
// session is attached, keeps it for the next one. Replies to a client's
// request (admin responses, acks) are not kept.
func (h *hub) packet(m *pb.FromRadio) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.backlogN == 0 && m.GetPacket().GetDecoded().GetRequestId() == 0 {
		if len(h.backlog) == backlogSize {
			h.backlog = slices.Delete(h.backlog, 0, 1)
		}
		h.backlog = append(h.backlog, m)
	}
	for s := range h.sessions {
		s.offer(m)
	}
	return nil
}

// message relays a non-packet message from the node to every session.
func (h *hub) message(m *pb.FromRadio) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m.GetRebooted() {
		h.stale = true
	}
	for s := range h.sessions {
		s.offer(m)
	}
}

// takeBacklog returns and clears the kept packets.
func (h *hub) takeBacklog() []*pb.FromRadio {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.backlog
	h.backlog = nil
	return b
}

func (h *hub) markStale() {
	h.mu.Lock()
	h.stale = true
	h.mu.Unlock()
}

// freshState returns the node's state, re-reading it first when it may be
// out of date.
func (h *hub) freshState(ctx context.Context) (*device.State, error) {
	h.refreshMu.Lock()
	defer h.refreshMu.Unlock()
	h.mu.Lock()
	c := h.c
	refresh := h.stale || time.Since(h.readAt) > maxStateAge
	h.stale = false
	h.mu.Unlock()
	if refresh {
		if err := c.Refresh(ctx); err != nil {
			h.markStale()
			return nil, fmt.Errorf("re-read node state: %w", err)
		}
		h.mu.Lock()
		h.readAt = time.Now()
		h.mu.Unlock()
	}
	return c.State(), nil
}

func (h *hub) status() LinkStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	ls := LinkStatus{Target: h.target, State: h.state, Clients: len(h.sessions), Since: h.since,
		Backlog: len(h.backlog), LastUsed: h.lastUsed}
	if h.c != nil {
		st := h.c.State()
		num := st.MyInfo.GetMyNodeNum()
		ls.Node, ls.NodeID = nodeLabel(st, num), fmt.Sprintf("!%08x", num)
	}
	return ls
}

func nodeLabel(st *device.State, num uint32) string {
	u := st.Nodes[num].GetUser()
	switch {
	case u.GetLongName() != "" && u.GetShortName() != "":
		return u.GetLongName() + " (" + u.GetShortName() + ")"
	case u.GetLongName() != "":
		return u.GetLongName()
	case u.GetShortName() != "":
		return u.GetShortName()
	}
	return fmt.Sprintf("!%08x", num)
}

// handshakeMessages is what a node sends for want_config, built from st:
// my_info, metadata, node infos (own first), config and module config
// sections, channels, then config_complete_id.
func handshakeMessages(st *device.State, nonce uint32) []*pb.FromRadio {
	var out []*pb.FromRadio
	add := func(m *pb.FromRadio) { out = append(out, m) }
	add(&pb.FromRadio{PayloadVariant: &pb.FromRadio_MyInfo{MyInfo: st.MyInfo}})
	if st.Metadata != nil {
		add(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Metadata{Metadata: st.Metadata}})
	}
	self := st.MyInfo.GetMyNodeNum()
	nums := make([]uint32, 0, len(st.Nodes))
	for n := range st.Nodes {
		nums = append(nums, n)
	}
	slices.SortFunc(nums, func(a, b uint32) int {
		switch {
		case a == b:
			return 0
		case a == self:
			return -1
		case b == self:
			return 1
		case a < b:
			return -1
		}
		return 1
	})
	for _, n := range nums {
		add(&pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: st.Nodes[n]}})
	}
	for _, c := range configpath.ConfigSections(st.Config) {
		add(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Config{Config: c}})
	}
	for _, c := range configpath.ModuleSections(st.ModuleConfig) {
		add(&pb.FromRadio{PayloadVariant: &pb.FromRadio_ModuleConfig{ModuleConfig: c}})
	}
	for _, ch := range st.Channels {
		if ch != nil {
			add(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Channel{Channel: ch}})
		}
	}
	add(&pb.FromRadio{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: nonce}})
	return out
}

// session is one client attached to a hub.
type session struct {
	h           *hub
	conn        net.Conn
	wantBacklog bool
	out         chan *pb.FromRadio
	done        chan struct{}
	once        sync.Once
	gotBacklog  bool
}

func newSession(h *hub, conn net.Conn, wantBacklog bool) *session {
	return &session{h: h, conn: conn, wantBacklog: wantBacklog,
		out: make(chan *pb.FromRadio, sessionQueue), done: make(chan struct{})}
}

func (s *session) close() {
	s.once.Do(func() {
		close(s.done)
		s.conn.Close()
	})
}

// offer queues m without blocking; a client too slow to keep up is
// disconnected.
func (s *session) offer(m *pb.FromRadio) {
	select {
	case s.out <- m:
	case <-s.done:
	default:
		s.h.s.logf("%s: client not keeping up; disconnecting it", s.h.target)
		go s.close()
	}
}

// send queues m, waiting for room.
func (s *session) send(m *pb.FromRadio) bool {
	select {
	case s.out <- m:
		return true
	case <-s.done:
		return false
	}
}

// run relays between the client and the node until either side closes.
func (s *session) run(ctx context.Context, br *bufio.Reader) {
	defer s.close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
			s.close()
		}
	}()
	go s.writeLoop()
	fr := transport.NewFrameReader(br, nil)
	for {
		p, err := fr.Next()
		if err != nil {
			return
		}
		var m pb.ToRadio
		if err := proto.Unmarshal(p, &m); err != nil {
			s.h.s.logf("%s: skipping undecodable frame from a client: %v", s.h.target, err)
			continue
		}
		if !s.handle(ctx, &m) {
			return
		}
	}
}

func (s *session) writeLoop() {
	for {
		select {
		case <-s.done:
			return
		case m := <-s.out:
			b, err := proto.Marshal(m)
			if err == nil {
				err = transport.WriteFrame(s.conn, b)
			}
			if err != nil {
				s.close()
				return
			}
		}
	}
}

// handle acts on one message from the client; false ends the session.
func (s *session) handle(ctx context.Context, m *pb.ToRadio) bool {
	switch v := m.PayloadVariant.(type) {
	case *pb.ToRadio_WantConfigId:
		st, err := s.h.freshState(ctx)
		if err != nil {
			s.h.s.logf("%s: %v", s.h.target, err)
			return false // the client sees a dropped link and reconnects
		}
		for _, r := range handshakeMessages(st, v.WantConfigId) {
			if !s.send(r) {
				return false
			}
		}
		if s.wantBacklog && !s.gotBacklog {
			s.gotBacklog = true
			for _, r := range s.h.takeBacklog() {
				if !s.send(r) {
					return false
				}
			}
		}
		return true
	case *pb.ToRadio_Heartbeat, *pb.ToRadio_Disconnect:
		return true // the hub keeps its own connection alive
	case *pb.ToRadio_Packet:
		if v.Packet.GetDecoded().GetPortnum() == pb.PortNum_ADMIN_APP {
			s.h.markStale()
		}
	}
	s.h.mu.Lock()
	c := s.h.c
	s.h.mu.Unlock()
	if err := c.Send(ctx, m); err != nil {
		s.h.s.logf("%s: relaying to the node: %v", s.h.target, err)
		return false
	}
	return true
}
