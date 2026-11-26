package ircmesh

import (
	"context"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
)

// agingInterval is how often memberships are aged and saved.
const agingInterval = time.Minute

// meshOptions are the device options the server opens the node with.
func (s *Server) meshOptions() device.Options {
	return device.Options{}
}

// startMesh starts consuming the node connection m, and aging memberships.
func (s *Server) startMesh(ctx context.Context, m Mesh) {
	q := make(chan sendJob, sendQueueLen)
	s.mu.Lock()
	s.sendQ = q
	s.mu.Unlock()
	s.wg.Add(3)
	go func() {
		defer s.wg.Done()
		s.sender(ctx, m, q)
	}()
	go func() {
		defer s.wg.Done()
		err := m.Listen(ctx, func(fr *pb.FromRadio) error {
			if p := fr.GetPacket(); p != nil {
				s.mu.Lock()
				s.packet(p)
				s.mu.Unlock()
			}
			return nil
		})
		if err != nil {
			s.logf("listening to the node: %v", err)
		}
	}()
	go func() {
		defer s.wg.Done()
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
	}()
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
