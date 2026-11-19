package transport

import (
	"context"
	"errors"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// Kind identifies the physical transport behind a Conn.
type Kind int

const (
	KindSerial Kind = iota
	KindTCP
	KindBLE
)

func (k Kind) String() string {
	switch k {
	case KindSerial:
		return "serial"
	case KindTCP:
		return "tcp"
	case KindBLE:
		return "ble"
	}
	return "unknown"
}

// ErrClosed is returned (wrapped) by Recv once the link is gone.
var ErrClosed = errors.New("connection closed")

// Conn is a message-level link to a node.
type Conn interface {
	Send(ctx context.Context, m *pb.ToRadio) error
	Recv(ctx context.Context) (*pb.FromRadio, error) // ErrClosed (wrapped) once the link is gone
	Kind() Kind
	Close() error
}

// Dialer opens a Conn.
type Dialer func(ctx context.Context) (Conn, error)
