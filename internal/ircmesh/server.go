package ircmesh

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"path/filepath"
	"slices"
	"sync"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
	"meshtastik/internal/irc"
)

// serverName is the server's name in prefixes and numerics.
const serverName = "ircmesh"

// Mesh is the node connection the server uses; *device.Client is one.
type Mesh interface {
	State() *device.State
	Listen(ctx context.Context, fn func(*pb.FromRadio) error) error
	SendTextID(ctx context.Context, id uint32, text string, to, channel uint32, waitAck bool) error
	Refresh(ctx context.Context) error
	Close() error
}

// Config configures a Server. Zero durations take the defaults noted.
type Config struct {
	Version string
	// Open connects to the node with opts, which carry the server's
	// reconnect and message callbacks.
	Open     func(ctx context.Context, opts device.Options) (Mesh, error)
	Password string        // the PASS clients must send; empty: none
	Active   time.Duration // default 24h: how long a node stays in a channel unheard
	StateDir string        // where memberships are saved; empty: not saved
	Log      io.Writer     // default io.Discard
	Now      func() time.Time
	// SendInterval (default 2s) is the least time between packets sent;
	// DMTimeout (default 30s) how long a direct message waits for its ack.
	SendInterval, DMTimeout time.Duration
	// OnReady, when set, is called once the node is connected and clients
	// are about to be accepted.
	OnReady func()
}

func (c *Config) defaults() {
	if c.Active == 0 {
		c.Active = 24 * time.Hour
	}
	if c.Log == nil {
		c.Log = io.Discard
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.SendInterval == 0 {
		c.SendInterval = 2 * time.Second
	}
	if c.DMTimeout == 0 {
		c.DMTimeout = 30 * time.Second
	}
}

// Server is the IRC server. Its state is guarded by mu, which is never held
// across mesh calls or socket writes.
type Server struct {
	cfg     Config
	started time.Time

	mu        sync.Mutex
	mesh      Mesh
	self      uint32
	firmware  string
	dir       *directory
	members   *membership
	chans     [8]*pb.Channel
	lora      *pb.Config_LoRaConfig
	names     map[int]string       // channel index → #name
	dbHeard   map[uint32]time.Time // last_heard from the node DB
	replay    *replay
	recent    *recent
	clients   map[*client]struct{}
	statePath string
	dirty     bool          // memberships changed since last saved
	sendQ     chan sendJob  // to the current sender; nil while the link is down
	up        bool          // the node connection is working
	refreshC  chan struct{} // asks the refresher to re-read the node's state
	wg        sync.WaitGroup
}

// New returns a server for cfg.
func New(cfg Config) *Server {
	cfg.defaults()
	return &Server{cfg: cfg, names: map[int]string{}, dbHeard: map[uint32]time.Time{}, refreshC: make(chan struct{}, 1), replay: newReplay(replayPerKey),
		recent: newRecent(1000), clients: map[*client]struct{}{}}
}

func (s *Server) logf(format string, args ...any) {
	fmt.Fprintf(s.cfg.Log, "%s "+format+"\n", append([]any{time.Now().Format(time.DateTime)}, args...)...)
}

// Run connects to the node, then serves IRC clients on ln until ctx ends. A
// failure to connect the first time is returned. Run closes ln, every
// client and the node connection before it returns.
func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	s.started = s.cfg.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m, err := s.cfg.Open(ctx, s.meshOptions())
	if err != nil {
		ln.Close()
		return err
	}
	st := m.State()
	s.mu.Lock()
	s.mesh, s.up = m, true
	s.applyState(st)
	s.mu.Unlock()
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.superviseMesh(ctx, m)
	}()
	go func() {
		defer s.wg.Done()
		s.ager(ctx)
	}()

	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	if s.cfg.OnReady != nil {
		s.cfg.OnReady()
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			break
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveClient(ctx, conn)
		}()
	}
	cancel()
	s.mu.Lock()
	for c := range s.clients {
		c.close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.shutdown()
	return nil
}

// applyState takes the node's identity, names, channels and the nodes it
// knows from st, and returns the nicks that changed. The caller holds s.mu.
func (s *Server) applyState(st *device.State) []nickChange {
	first := s.dir == nil
	if first {
		s.self = st.MyInfo.GetMyNodeNum()
		s.dir = newDirectory(s.self)
		s.members = newMembership(s.cfg.Active)
		if s.cfg.StateDir != "" {
			s.statePath = filepath.Join(s.cfg.StateDir, fmt.Sprintf("members-%08x.json", s.self))
			if err := s.members.load(s.statePath); err != nil {
				s.logf("reading %s: %v", s.statePath, err)
			}
		}
	}
	s.firmware = st.Metadata.GetFirmwareVersion()
	changes := s.dir.set(s.self, st.Self().GetUser())
	for _, num := range slices.Sorted(maps.Keys(st.Nodes)) {
		if num != s.self {
			changes = append(changes, s.dir.set(num, st.Nodes[num].GetUser())...)
		}
		if lh := st.Nodes[num].GetLastHeard(); lh != 0 {
			s.dbHeard[num] = time.Unix(int64(lh), 0)
		}
	}
	s.chans = st.Channels
	s.lora = st.Config.GetLora()
	s.names = channelNames(s.chans, s.lora)
	if first {
		s.members.seed(st.Nodes, s.self, s.cfg.Now())
	}
	return changes
}

// shutdown saves the memberships and closes the node connection once every
// client is gone.
func (s *Server) shutdown() {
	s.mu.Lock()
	s.save()
	m := s.mesh
	s.mesh = nil
	s.mu.Unlock()
	if m != nil {
		m.Close()
	}
}

// indexes is the enabled channel indexes, ascending. The caller holds s.mu.
func (s *Server) indexes() []int {
	return slices.Sorted(maps.Keys(s.names))
}

// channelIndex finds a channel by name, ignoring case. The caller holds s.mu.
func (s *Server) channelIndex(name string) (int, bool) {
	for i, n := range s.names {
		if irc.Fold(n) == irc.Fold(name) {
			return i, true
		}
	}
	return 0, false
}

// lastHeard is when num was last heard, by the server or by the node DB
// (zero if never). The caller holds s.mu.
func (s *Server) lastHeard(num uint32) time.Time {
	t := s.members.lastHeard(num)
	if db := s.dbHeard[num]; db.After(t) {
		t = db
	}
	return t
}

// selfNick is the IRC user's nick. The caller holds s.mu.
func (s *Server) selfNick() string { return s.dir.nick(s.self) }

// registered is the clients past registration. The caller holds s.mu.
func (s *Server) registered() []*client {
	var out []*client
	for c := range s.clients {
		if c.registered {
			out = append(out, c)
		}
	}
	return out
}

// numeric is a numeric reply to c.
func (s *Server) numeric(c *client, code string, params ...string) irc.Message {
	return irc.Message{Prefix: serverName, Command: code, Params: append([]string{c.target()}, params...)}
}
