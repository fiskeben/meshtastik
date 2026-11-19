package transport

import (
	"errors"
	"io"
	"sync"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

type fakeLink struct {
	mu      sync.Mutex
	queue   [][]byte
	written [][]byte
	readErr error
}

func (f *fakeLink) WriteToRadio(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, p)
	return nil
}

func (f *fakeLink) ReadFromRadio(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return 0, f.readErr
	}
	if len(f.queue) == 0 {
		return 0, nil
	}
	n := copy(b, f.queue[0])
	f.queue = f.queue[1:]
	return n, nil
}

func fr(id uint32) []byte {
	p, _ := proto.Marshal(&pb.FromRadio{Id: id})
	return p
}

func TestBLEDrainReadsUntilEmpty(t *testing.T) {
	l := &fakeLink{queue: [][]byte{fr(1), fr(2), fr(3)}}
	c := newBLEConn(l, io.Discard)
	defer c.Close()
	c.notify()
	for want := uint32(1); want <= 3; want++ {
		m, err := c.Recv(ctxT(t))
		if err != nil || m.Id != want {
			t.Fatal(m, err)
		}
	}
}

func TestBLESendWritesRawProtoAndDrains(t *testing.T) {
	l := &fakeLink{}
	c := newBLEConn(l, io.Discard)
	defer c.Close()
	l.queue = [][]byte{fr(9)}
	if err := c.Send(ctxT(t), &pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: 1}}); err != nil {
		t.Fatal(err)
	}
	if len(l.written) != 1 || l.written[0][0] == 0x94 {
		t.Fatal("must write unframed protobuf")
	}
	if m, _ := c.Recv(ctxT(t)); m.Id != 9 {
		t.Fatal("no drain after write")
	}
}

func TestBLEReadErrorClosesConn(t *testing.T) {
	l := &fakeLink{readErr: errors.New("link lost")}
	c := newBLEConn(l, io.Discard)
	defer c.Close()
	c.notify()
	if _, err := c.Recv(ctxT(t)); !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
}

func TestBLECloseUnblocksRecvAndIsIdempotent(t *testing.T) {
	c := newBLEConn(&fakeLink{}, io.Discard)
	errc := make(chan error, 1)
	go func() { _, err := c.Recv(ctxT(t)); errc <- err }()
	c.Close()
	if err := <-errc; !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctxT(t), &pb.ToRadio{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("send after close: %v", err)
	}
}

func TestBLESkipsUndecodable(t *testing.T) {
	l := &fakeLink{queue: [][]byte{{0xff, 0xff, 0xff}, fr(4)}}
	c := newBLEConn(l, io.Discard)
	defer c.Close()
	c.notify()
	if m, err := c.Recv(ctxT(t)); err != nil || m.Id != 4 {
		t.Fatal(m, err)
	}
}

func TestMatchDevice(t *testing.T) {
	d := BLEDevice{Name: "Meshtastic_ab12", Address: "AA:BB:CC:DD:EE:FF"}
	if !matchDevice(d, "meshtastic_ab12") || !matchDevice(d, "aa:bb:cc:dd:ee:ff") || matchDevice(d, "Meshtastic") {
		t.Fatal()
	}
}
