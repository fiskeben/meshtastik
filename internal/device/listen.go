package device

import (
	"context"
	"fmt"
	"sync"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// packetBuffer is how many received packets are held for Listen; when it is
// full the oldest packet is dropped.
const packetBuffer = 256

// packetQueue is a bounded FIFO of received packets. push never blocks, so
// the reader goroutine is never held up by a slow or absent listener.
type packetQueue struct {
	mu      sync.Mutex
	buf     [packetBuffer]*pb.MeshPacket
	head, n int
	dropped int
	wake    chan struct{} // holds one pending wake-up for Listen
}

func (q *packetQueue) push(p *pb.MeshPacket) {
	q.mu.Lock()
	if q.n == len(q.buf) {
		q.buf[q.head] = nil
		q.head = (q.head + 1) % len(q.buf)
		q.n--
		q.dropped++
	}
	q.buf[(q.head+q.n)%len(q.buf)] = p
	q.n++
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default: // a wake-up is already pending (or nobody can listen)
	}
}

// pop removes and returns the oldest packet, or nil when empty.
func (q *packetQueue) pop() *pb.MeshPacket {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.n == 0 {
		return nil
	}
	p := q.buf[q.head]
	q.buf[q.head] = nil
	q.head = (q.head + 1) % len(q.buf)
	q.n--
	return p
}

// DroppedPackets reports how many received packets were discarded because
// the buffer was full, normally because nobody was listening yet.
func (c *Client) DroppedPackets() int {
	c.packets.mu.Lock()
	defer c.packets.mu.Unlock()
	return c.packets.dropped
}

// Listen calls fn for each buffered packet (oldest first), then for each new
// packet, until ctx ends (returns nil) or fn returns an error (returned as
// is). Packets are buffered from the start of the first handshake, so the
// backlog the node sends right after connecting is not lost.
//
// If the link drops, Listen reconnects (redial and handshake, with the same
// backoff and ReconnectTimeout as Apply) and continues; a reconnect failure
// is returned.
func (c *Client) Listen(ctx context.Context, fn func(*pb.MeshPacket) error) error {
	for {
		l := c.current()
		for {
			if ctx.Err() != nil {
				return nil
			}
			p := c.packets.pop()
			if p == nil {
				break
			}
			if err := fn(p); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.packets.wake:
		case <-l.readerEnd:
			if err := c.relink(ctx, l); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

// relink replaces dead link l, unless something else (Apply) already has.
func (c *Client) relink(ctx context.Context, l *link) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.connMu.Lock()
	cur, closed := c.cur, c.closed
	c.connMu.Unlock()
	if closed {
		return errClientClosed
	}
	if cur != l {
		return nil
	}
	fmt.Fprintf(c.opts.Log, "connection lost (%v); reconnecting\n", l.readErr)
	return c.reconnect(ctx, "after the connection dropped")
}
