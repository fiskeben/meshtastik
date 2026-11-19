package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// ctxT returns a context with a 2s timeout, cancelled at test cleanup.
func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestStreamConnRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	c := NewStreamConn(a, KindTCP, io.Discard)
	defer c.Close()
	go func() {
		p, _ := proto.Marshal(&pb.FromRadio{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: 42}})
		b.Write([]byte("\x00\xffINFO | boot\n")) // garbage before first frame
		WriteFrame(b, p)
	}()
	m, err := c.Recv(ctxT(t))
	if err != nil || m.GetConfigCompleteId() != 42 {
		t.Fatalf("%v %v", m, err)
	}

	go c.Send(ctxT(t), &pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: 7}})
	p, err := NewFrameReader(b, io.Discard).Next()
	var tr pb.ToRadio
	if err != nil || proto.Unmarshal(p, &tr) != nil || tr.GetWantConfigId() != 7 {
		t.Fatal(err)
	}
}

func TestStreamConnRecvAfterPeerClose(t *testing.T) {
	a, b := net.Pipe()
	c := NewStreamConn(a, KindTCP, io.Discard)
	b.Close()
	if _, err := c.Recv(ctxT(t)); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestStreamConnRecvHonoursContext(t *testing.T) {
	a, _ := net.Pipe()
	c := NewStreamConn(a, KindTCP, io.Discard)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Recv(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestKindString(t *testing.T) {
	if KindSerial.String() != "serial" || KindTCP.String() != "tcp" || KindBLE.String() != "ble" {
		t.Fatal("bad Kind strings")
	}
}
