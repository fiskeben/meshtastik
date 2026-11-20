package device

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// ErrNoAnswer is returned (wrapped) when a request over the mesh gets no
// answer in time.
var ErrNoAnswer = errors.New("no answer")

// RoutingError is a routing reply other than NONE to a packet for Dest.
type RoutingError struct {
	From   uint32 // the node that sent the reply
	Dest   uint32 // where the packet was going
	Reason pb.Routing_Error
}

func (e *RoutingError) Error() string {
	dest := fmt.Sprintf("!%08x", e.Dest)
	switch e.Reason {
	case pb.Routing_ADMIN_PUBLIC_KEY_UNAUTHORIZED:
		return fmt.Sprintf("%s does not list this node's public key in security.admin_key; on %s run: "+
			"meshtastik admin-key add <this node's public key, shown by meshtastik info>", dest, dest)
	case pb.Routing_NOT_AUTHORIZED:
		return fmt.Sprintf("%s refused: not authorized (remote admin needs firmware 2.5 or newer and this node's "+
			"public key in its security.admin_key)", dest)
	case pb.Routing_PKI_UNKNOWN_PUBKEY, pb.Routing_PKI_SEND_FAIL_PUBLIC_KEY:
		return fmt.Sprintf("this node has no public key for %s yet; wait until it has heard %s's node info", dest, dest)
	case pb.Routing_NO_ROUTE, pb.Routing_MAX_RETRANSMIT, pb.Routing_NO_RESPONSE, pb.Routing_TIMEOUT:
		return fmt.Sprintf("no route to %s (%s)", dest, e.Reason)
	case pb.Routing_ADMIN_BAD_SESSION_KEY:
		return fmt.Sprintf("%s rejected the admin session key", dest)
	case pb.Routing_RATE_LIMIT_EXCEEDED:
		return fmt.Sprintf("%s is rate-limiting requests; try again in a while", dest)
	}
	return fmt.Sprintf("delivery to %s failed: %s", dest, e.Reason)
}

// routingReason decodes the error reason of a ROUTING_APP packet.
func routingReason(p *pb.MeshPacket) (pb.Routing_Error, error) {
	r := &pb.Routing{}
	if err := proto.Unmarshal(p.GetDecoded().GetPayload(), r); err != nil {
		return 0, fmt.Errorf("decode routing reply: %w", err)
	}
	return r.GetErrorReason(), nil
}

// RequestOptions tunes Request; zero values select the defaults.
type RequestOptions struct {
	Channel  uint32
	HopLimit uint32 // 0: the node's default
	PKI      bool   // pki_encrypted
	WantAck  bool
	Timeout  time.Duration // 0: Options.MeshTimeout
}

// Request sends payload to node to on port with want_response and returns
// the first packet from to that answers it on the same port. A routing
// error from any node ends it; routing NONE replies (acks, implicit acks)
// are skipped. Without an answer it fails with ErrNoAnswer after the timeout.
func (c *Client) Request(ctx context.Context, to uint32, port pb.PortNum, payload proto.Message, o RequestOptions) (*pb.MeshPacket, error) {
	if to == 0 || to == Broadcast {
		return nil, fmt.Errorf("!%08x is not a node to ask", to)
	}
	if to == c.State().MyInfo.GetMyNodeNum() {
		return nil, errors.New("cannot ask the connected node itself over the mesh")
	}
	b, err := proto.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	id := randomID()
	responses := c.addWaiter(id)
	defer c.removeWaiter(id)
	l := c.current()
	pkt := &pb.MeshPacket{
		To:           to,
		Id:           id,
		Channel:      o.Channel,
		HopLimit:     o.HopLimit,
		WantAck:      o.WantAck,
		PkiEncrypted: o.PKI,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{
			Portnum:      port,
			Payload:      b,
			WantResponse: true,
		}},
	}
	if err := c.send(ctx, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: pkt}}); err != nil {
		return nil, err
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = c.opts.MeshTimeout
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		select {
		case p := <-responses:
			if p.GetDecoded().GetPortnum() == pb.PortNum_ROUTING_APP {
				reason, err := routingReason(p)
				if err != nil {
					return nil, err
				}
				if reason != pb.Routing_NONE {
					return nil, &RoutingError{From: p.GetFrom(), Dest: to, Reason: reason}
				}
				continue
			}
			if p.GetFrom() != to || p.GetDecoded().GetPortnum() != port {
				continue
			}
			return p, nil
		case <-l.readerEnd:
			return nil, fmt.Errorf("request: %w", l.readErr)
		case <-t.C:
			return nil, fmt.Errorf("%w from !%08x after %s", ErrNoAnswer, to, timeout)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
