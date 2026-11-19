package device

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

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

// DroppedPackets reports how many received packets have been discarded
// since Open because the buffer was full, normally because nobody was
// listening yet.
func (c *Client) DroppedPackets() int {
	c.packets.mu.Lock()
	defer c.packets.mu.Unlock()
	return c.packets.dropped
}

// shortDropLimit is how many drops of short-lived links (see
// Options.StableLink) within ReconnectTimeout make Listen give up.
const shortDropLimit = 3

// Listen calls fn for each buffered packet (oldest first), then for each new
// packet, until ctx ends (returns nil) or fn returns an error (returned as
// is). Packets are buffered from the start of the first handshake, so the
// backlog the node sends right after connecting is not lost. Run at most one
// Listen at a time per Client.
//
// If the link drops, Listen waits (Options.ReconnectDelay, doubling up to 5s
// while links keep dropping soon after connecting), calls
// Options.OnReconnect, and reconnects (redial and handshake, with the same
// backoff and ReconnectTimeout as Apply); a reconnect failure is returned.
// A node allows one TCP client at a time, so another client that reconnects
// whenever it is kicked off would make the two take turns forever: after
// shortDropLimit drops of links younger than Options.StableLink within
// ReconnectTimeout, Listen gives up with an error saying so.
func (c *Client) Listen(ctx context.Context, fn func(*pb.MeshPacket) error) error {
	var shortDrops []time.Time // recent drops of short-lived links
	var delay time.Duration    // wait before the next redial
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
			continue
		case <-l.readerEnd:
		}
		if c.isClosed() {
			return errClientClosed
		}
		if c.current() != l {
			continue // Apply has already replaced the link
		}
		now := time.Now()
		if now.Sub(l.since) >= c.opts.StableLink {
			shortDrops, delay = nil, c.opts.ReconnectDelay
		} else {
			shortDrops = slices.DeleteFunc(shortDrops, func(t time.Time) bool { return now.Sub(t) > c.opts.ReconnectTimeout })
			shortDrops = append(shortDrops, now)
			if len(shortDrops) >= shortDropLimit {
				return fmt.Errorf("connection dropped %d times within %s, each time soon after reconnecting — "+
					"another client (Home Assistant, the phone app) is probably using the node's connection; "+
					"stop it or connect over serial/BLE", len(shortDrops), c.opts.ReconnectTimeout)
			}
			if delay == 0 {
				delay = c.opts.ReconnectDelay
			} else {
				delay = min(delay*2, maxReconnectBackoff)
			}
		}
		if c.opts.OnReconnect != nil {
			c.opts.OnReconnect(l.readErr, delay)
		} else {
			fmt.Fprintf(c.opts.Log, "connection lost (%v); reconnecting in %s\n", l.readErr, delay)
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		if err := c.relink(ctx, l); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func (c *Client) isClosed() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.closed
}

// relink replaces dead link l, unless something else (Apply) already has.
func (c *Client) relink(ctx context.Context, l *link) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	if c.isClosed() {
		return errClientClosed
	}
	if c.current() != l {
		return nil
	}
	return c.reconnect(ctx, afterDrop)
}
