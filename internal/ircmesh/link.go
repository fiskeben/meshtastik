package ircmesh

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
	"meshtastik/internal/irc"
)

const (
	agingInterval = time.Minute      // how often memberships are aged and saved
	refreshEvery  = 10 * time.Minute // how often the node's state is re-read
	reopenFirst   = time.Second      // first wait before reopening a lost node connection
	reopenMax     = time.Minute
)

// meshOptions are the device options the server opens the node with: its
// reconnects show as link notices, and a reboot re-reads the node's state.
func (s *Server) meshOptions() device.Options {
	return device.Options{
		OnReconnect: func(cause error, _ time.Duration) {
			s.mu.Lock()
			s.linkDown(cause)
			s.mu.Unlock()
		},
		OnReconnected: func() {
			s.mu.Lock()
			m := s.mesh
			s.mu.Unlock()
			if m == nil {
				return
			}
			st := m.State()
			s.mu.Lock()
			s.syncState(st)
			s.linkUp()
			s.mu.Unlock()
		},
		OnMessage: func(fr *pb.FromRadio) {
			if fr.GetRebooted() {
				s.requestRefresh()
			}
		},
	}
}

// requestRefresh asks the refresher to re-read the node's state soon.
func (s *Server) requestRefresh() {
	select {
	case s.refreshC <- struct{}{}:
	default:
	}
}

// linkDown marks the node connection lost. The caller holds s.mu.
func (s *Server) linkDown(cause error) {
	if !s.up {
		return
	}
	s.up = false
	s.toAll(s.notice(s.selfNick(), fmt.Sprintf("mesh link down (%v); reconnecting", cause)))
}

// linkUp marks the node connection back. The caller holds s.mu.
func (s *Server) linkUp() {
	if s.up {
		return
	}
	s.up = true
	s.toAll(s.notice(s.selfNick(), "mesh link back up"))
}

// superviseMesh runs the node connection m until ctx ends: it relays
// packets and sends messages, and when the connection fails for good it
// reopens it with backoff.
func (s *Server) superviseMesh(ctx context.Context, m Mesh) {
	for {
		mctx, cancel := context.WithCancel(ctx)
		q := make(chan sendJob, sendQueueLen)
		s.mu.Lock()
		s.mesh, s.sendQ = m, q
		s.mu.Unlock()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.sender(mctx, m, q)
		}()
		go func() {
			defer wg.Done()
			s.refresher(mctx, m)
		}()
		err := m.Listen(mctx, func(fr *pb.FromRadio) error {
			if p := fr.GetPacket(); p != nil {
				s.mu.Lock()
				s.packet(p)
				s.mu.Unlock()
			}
			return nil
		})
		cancel()
		wg.Wait()
		s.mu.Lock()
		s.sendQ = nil
		s.dropQueued(q)
		if ctx.Err() != nil {
			s.mu.Unlock()
			return // shutdown closes the connection
		}
		s.logf("node connection lost: %v", err)
		s.linkDown(err)
		s.mesh = nil
		s.mu.Unlock()
		m.Close()
		if m = s.reopen(ctx); m == nil {
			return
		}
		st := m.State()
		s.mu.Lock()
		s.syncState(st)
		s.linkUp()
		s.mu.Unlock()
	}
}

// reopen opens the node connection again, waiting longer after each
// failure, until it succeeds or ctx ends (nil).
func (s *Server) reopen(ctx context.Context) Mesh {
	delay := reopenFirst
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		m, err := s.cfg.Open(ctx, s.meshOptions())
		if err == nil {
			return m
		}
		s.logf("reopening the node connection: %v", err)
		delay = min(2*delay, reopenMax)
	}
}

// dropQueued refuses the messages still waiting for the radio. The caller
// holds s.mu.
func (s *Server) dropQueued(q chan sendJob) {
	for {
		select {
		case <-q:
			s.toAll(s.notice(s.selfNick(), "mesh link is down; message not sent"))
		default:
			return
		}
	}
}

// refresher re-reads the node's state every refreshEvery and on request.
func (s *Server) refresher(ctx context.Context, m Mesh) {
	t := time.NewTicker(refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.refreshC:
		}
		if err := m.Refresh(ctx); err != nil {
			if ctx.Err() == nil {
				s.logf("re-reading the node's state: %v", err)
			}
			continue
		}
		st := m.State()
		s.mu.Lock()
		s.syncState(st)
		s.mu.Unlock()
	}
}

// ager ages memberships until ctx ends.
func (s *Server) ager(ctx context.Context) {
	t := time.NewTicker(agingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.age()
		}
	}
}

// syncState takes a fresh copy of the node's state and tells clients what
// changed: renamed nodes, and channels removed, added or renamed. The
// caller holds s.mu.
func (s *Server) syncState(st *device.State) {
	old := maps.Clone(s.names)
	s.nickChanges(s.applyState(st))
	selfMask := s.dir.mask(s.self)
	for _, i := range slices.Sorted(maps.Keys(old)) {
		name := old[i]
		if s.names[i] == name {
			continue
		}
		for _, c := range s.registered() {
			if !c.parted[i] {
				c.queue(irc.Message{Prefix: selfMask, Command: "PART", Params: []string{name, "channel removed on node"}})
			}
		}
		if _, still := s.names[i]; !still {
			s.members.dropChannel(i)
			s.replay.dropChannel(i)
			s.dirty = true
			for _, c := range s.registered() {
				delete(c.parted, i)
			}
		}
	}
	for _, i := range s.indexes() {
		if old[i] == s.names[i] {
			continue
		}
		for _, c := range s.registered() {
			if !c.parted[i] {
				s.joinBurst(c, i)
			}
		}
	}
}
