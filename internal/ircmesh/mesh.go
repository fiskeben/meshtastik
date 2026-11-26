package ircmesh

import (
	"context"

	"meshtastik/internal/device"
)

// meshOptions are the device options the server opens the node with.
func (s *Server) meshOptions() device.Options {
	return device.Options{}
}

// startMesh starts consuming the node connection m.
func (s *Server) startMesh(ctx context.Context, m Mesh) {}

// shutdown closes the node connection once every client is gone.
func (s *Server) shutdown() {
	s.mu.Lock()
	m := s.mesh
	s.mesh = nil
	s.mu.Unlock()
	if m != nil {
		m.Close()
	}
}
