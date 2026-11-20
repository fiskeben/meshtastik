package device

import (
	"context"
	"errors"
	"fmt"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// meshAttempts is how many times an admin message to another node is sent
// before giving up when no answer comes back.
const meshAttempts = 3

// checkRemoteKey fails, without sending, when the connected node has no
// public key for node to: it could not encrypt the admin message.
func (c *Client) checkRemoteKey(to uint32) error {
	c.mu.Lock()
	key := c.state.Nodes[to].GetUser().GetPublicKey()
	c.mu.Unlock()
	if len(key) == 0 {
		return &RoutingError{Dest: to, Reason: pb.Routing_PKI_UNKNOWN_PUBKEY}
	}
	return nil
}

// stamped is a copy of m carrying node to's session passkey.
func (c *Client) stamped(to uint32, m *pb.AdminMessage) *pb.AdminMessage {
	m = proto.Clone(m).(*pb.AdminMessage)
	m.SessionPasskey = c.sessionPasskey(to)
	return m
}

// lostOnTheWay reports whether err means the packet or its answer was lost:
// no answer in time, or the connected node giving up on its own
// retransmissions of a want_ack packet.
func lostOnTheWay(err error) bool {
	var re *RoutingError
	return errors.Is(err, ErrNoAnswer) ||
		errors.As(err, &re) && (re.Reason == pb.Routing_MAX_RETRANSMIT || re.Reason == pb.Routing_TIMEOUT)
}

// meshRetry runs try up to meshAttempts times while its packet is lost on
// the way. When node to rejects the session passkey, it fetches a new one
// and tries once more.
func (c *Client) meshRetry(ctx context.Context, to uint32, try func() error) error {
	refreshed := false
	var err error
	for attempt := 0; attempt < meshAttempts; {
		err = try()
		var re *RoutingError
		switch {
		case err == nil:
			return nil
		case lostOnTheWay(err):
			attempt++
		case errors.As(err, &re) && re.Reason == pb.Routing_ADMIN_BAD_SESSION_KEY && !refreshed:
			refreshed = true
			c.setPasskey(to, nil)
			if err := c.ensurePasskeyFor(ctx, to); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return fmt.Errorf("%w (tried %d times)", err, meshAttempts)
}

// remoteAdminRequest sends m to node to over the mesh, PKI-encrypted with
// want_ack, and returns its admin response.
func (c *Client) remoteAdminRequest(ctx context.Context, to uint32, m *pb.AdminMessage) (*pb.AdminMessage, error) {
	if err := c.checkRemoteKey(to); err != nil {
		return nil, err
	}
	var resp *pb.AdminMessage
	err := c.meshRetry(ctx, to, func() error {
		p, err := c.Request(ctx, to, pb.PortNum_ADMIN_APP, c.stamped(to, m), RequestOptions{PKI: true, WantAck: true})
		if err != nil {
			return err
		}
		r := &pb.AdminMessage{}
		if err := proto.Unmarshal(p.GetDecoded().GetPayload(), r); err != nil {
			return fmt.Errorf("decode admin response: %w", err)
		}
		if len(r.SessionPasskey) > 0 {
			c.setPasskey(to, r.SessionPasskey)
		}
		resp = r
		return nil
	})
	return resp, err
}

// remotePost sends m to node to over the mesh, PKI-encrypted, and waits for
// to's acknowledgement.
func (c *Client) remotePost(ctx context.Context, to uint32, m *pb.AdminMessage) error {
	if err := c.checkRemoteKey(to); err != nil {
		return err
	}
	if err := c.ensurePasskeyFor(ctx, to); err != nil {
		return err
	}
	return c.meshRetry(ctx, to, func() error {
		return c.sendAcked(ctx, to, pb.PortNum_ADMIN_APP, c.stamped(to, m), RequestOptions{PKI: true})
	})
}
