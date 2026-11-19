package transport

import (
	"context"
	"fmt"
	"io"
	"sync"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

type streamConn struct {
	rwc  io.ReadWriteCloser
	kind Kind
	log  io.Writer

	wmu sync.Mutex

	msgs    chan *pb.FromRadio
	done    chan struct{} // closed by Close to release the reader goroutine
	readErr error         // set before msgs is closed
	once    sync.Once
}

// NewStreamConn runs the stream framing protocol over rwc. log receives
// non-frame bytes and undecodable frames; nil discards them.
func NewStreamConn(rwc io.ReadWriteCloser, kind Kind, log io.Writer) Conn {
	if log == nil {
		log = io.Discard
	}
	c := &streamConn{
		rwc: rwc, kind: kind, log: log,
		msgs: make(chan *pb.FromRadio, 64),
		done: make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *streamConn) readLoop() {
	defer close(c.msgs)
	fr := NewFrameReader(c.rwc, c.log)
	for {
		p, err := fr.Next()
		if err != nil {
			c.readErr = err
			return
		}
		var m pb.FromRadio
		if err := proto.Unmarshal(p, &m); err != nil {
			fmt.Fprintf(c.log, "skipping undecodable frame (%d bytes): %v\n", len(p), err)
			continue
		}
		select {
		case c.msgs <- &m:
		case <-c.done:
			c.readErr = io.ErrClosedPipe
			return
		}
	}
}

func (c *streamConn) Send(ctx context.Context, m *pb.ToRadio) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return WriteFrame(c.rwc, p)
}

func (c *streamConn) Recv(ctx context.Context) (*pb.FromRadio, error) {
	select {
	case m, ok := <-c.msgs:
		if !ok {
			return nil, fmt.Errorf("%w: %v", ErrClosed, c.readErr)
		}
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *streamConn) Kind() Kind { return c.kind }

func (c *streamConn) Close() error {
	var err error
	c.once.Do(func() {
		close(c.done)
		err = c.rwc.Close()
	})
	return err
}
