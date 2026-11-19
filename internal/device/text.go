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

// SendText sends text to node to on channel. A direct message (to !=
// Broadcast) asks for an ack; with waitAck SendText waits for the mesh's
// routing reply, returning ErrNoAck when ctx's deadline passes first.
func (c *Client) SendText(ctx context.Context, text string, to uint32, channel uint32, waitAck bool) error {
	if n := len([]byte(text)); n > int(pb.Constants_DATA_PAYLOAD_LEN) {
		return fmt.Errorf("text is %d bytes; the limit is %d", n, int(pb.Constants_DATA_PAYLOAD_LEN))
	}
	id := randomID()
	direct := to != Broadcast
	var responses chan *pb.MeshPacket
	if waitAck && direct {
		responses = c.addWaiter(id)
		defer c.removeWaiter(id)
	}
	l := c.current()
	pkt := &pb.MeshPacket{
		To:      to,
		Channel: channel,
		Id:      id,
		WantAck: direct,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{
			Portnum: pb.PortNum_TEXT_MESSAGE_APP,
			Payload: []byte(text),
		}},
	}
	if err := c.send(ctx, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: pkt}}); err != nil {
		return c.ackErr(ctx, err)
	}
	if responses == nil {
		return nil
	}
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
			return nil
		case <-l.readerEnd:
			return fmt.Errorf("send text: %w", l.readErr)
		case <-ctx.Done():
			return c.ackErr(ctx, ctx.Err())
		}
	}
}

// ackErr maps a deadline expiry to ErrNoAck.
func (c *Client) ackErr(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrNoAck
	}
	return err
}
