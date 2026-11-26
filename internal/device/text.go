package device

import (
	"context"
	"errors"
	"fmt"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// Broadcast is the destination of a message for every node.
const Broadcast = 0xffffffff

// ErrNoAck means a direct message was sent but no acknowledgement arrived
// before the context deadline.
var ErrNoAck = errors.New("no acknowledgement")

// ErrImplicitAck accompanies ErrNoAck when the local node reported hearing a
// neighbour rebroadcast the message (an "implicit ack") but the destination
// never confirmed delivery.
var ErrImplicitAck = errors.New("relayed by the mesh (implicit ack) but not confirmed")

// SendText sends text to node to on channel. A direct message (to !=
// Broadcast) asks for an ack; with waitAck SendText waits for the mesh's
// routing reply, returning ErrNoAck when ctx's deadline passes first.
func (c *Client) SendText(ctx context.Context, text string, to uint32, channel uint32, waitAck bool) error {
	return c.SendTextID(ctx, randomID(), text, to, channel, waitAck)
}

// NewPacketID returns a random, non-zero packet id for SendTextID.
func NewPacketID() uint32 { return randomID() }

// SendTextID is SendText with the packet id chosen by the caller, so it can
// recognise replies and reactions to the message.
func (c *Client) SendTextID(ctx context.Context, id uint32, text string, to uint32, channel uint32, waitAck bool) error {
	if id == 0 {
		return errors.New("0 is not a valid packet id")
	}
	if n := len([]byte(text)); n > int(pb.Constants_DATA_PAYLOAD_LEN) {
		return fmt.Errorf("text is %d bytes; the limit is %d", n, int(pb.Constants_DATA_PAYLOAD_LEN))
	}
	if to == 0 {
		return errors.New("0 is not a valid node")
	}
	me := c.State().MyInfo.GetMyNodeNum()
	if to == me {
		return errors.New("cannot send a message to the connected node itself")
	}
	direct := to != Broadcast
	var responses chan *pb.MeshPacket
	if waitAck && direct {
		responses = c.addWaiter(id)
		defer c.removeWaiter(id)
	}
	l := c.current()
	pkt := &pb.MeshPacket{
		To:       to,
		Channel:  channel,
		Id:       id,
		WantAck:  direct,
		HopLimit: c.hopLimit(),
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{
			Portnum: pb.PortNum_TEXT_MESSAGE_APP,
			Payload: []byte(text),
		}},
	}
	if err := c.send(ctx, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: pkt}}); err != nil {
		if responses != nil {
			err = ackErr(ctx, err)
		}
		return err
	}
	if responses == nil {
		return nil
	}
	implicit := false
	for {
		select {
		case p := <-responses:
			data := p.GetDecoded()
			if data.GetPortnum() != pb.PortNum_ROUTING_APP {
				continue
			}
			r := &pb.Routing{}
			if err := proto.Unmarshal(data.GetPayload(), r); err != nil {
				return fmt.Errorf("decode routing reply: %w", err)
			}
			if r.GetErrorReason() != pb.Routing_NONE {
				return fmt.Errorf("delivery failed: %s", r.GetErrorReason())
			}
			if p.GetFrom() == to {
				return nil
			}
			// A NONE reply from anyone else, normally the local node, only says
			// a neighbour rebroadcast the packet; keep waiting for the real ack.
			implicit = true
		case <-l.readerEnd:
			return fmt.Errorf("send text: %w", l.readErr)
		case <-ctx.Done():
			err := ackErr(ctx, ctx.Err())
			if implicit && errors.Is(err, ErrNoAck) {
				err = fmt.Errorf("%w: %w", err, ErrImplicitAck)
			}
			return err
		}
	}
}

// ackErr maps a deadline expiry to ErrNoAck.
func ackErr(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrNoAck
	}
	return err
}
