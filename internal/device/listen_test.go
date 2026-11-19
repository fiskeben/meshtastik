package device_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

func text(from uint32, to uint32, s string) *pb.MeshPacket {
	return &pb.MeshPacket{From: from, To: to, Id: rand.Uint32() | 1,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(s)}}}
}

func TestListenDeliversBacklogThenLive(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{text(42, device.Broadcast, "old")}
	c, _ := open(t, d, device.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var got []string
	go func() { time.Sleep(50 * time.Millisecond); d.Inject(text(42, device.Broadcast, "new")) }()
	err := c.Listen(ctx, func(p *pb.MeshPacket) error {
		got = append(got, string(p.GetDecoded().GetPayload()))
		if len(got) == 2 {
			cancel()
		}
		return nil
	})
	if err != nil || !slices.Equal(got, []string{"old", "new"}) {
		t.Fatal(got, err)
	}
}

func TestListenBufferBounded(t *testing.T) {
	d := fakedevice.New()
	for i := range 300 {
		d.Backlog = append(d.Backlog, text(42, device.Broadcast, fmt.Sprint(i)))
	}
	c, _ := open(t, d, device.Options{})
	for deadline := time.Now().Add(2 * time.Second); c.DroppedPackets() < 44 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var first string
	n := 0
	err := c.Listen(ctx, func(p *pb.MeshPacket) error {
		if n == 0 {
			first = string(p.GetDecoded().GetPayload())
		}
		n++
		if n == 256 {
			cancel()
		}
		return nil
	})
	if err != nil || first != "44" || c.DroppedPackets() != 44 {
		t.Fatal(first, c.DroppedPackets(), err)
	}
}

func TestListenReturnsCallbackError(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{text(42, device.Broadcast, "old")}
	c, _ := open(t, d, device.Options{})
	want := fmt.Errorf("stop")
	if err := c.Listen(ctxT(t), func(*pb.MeshPacket) error { return want }); err != want {
		t.Fatal(err)
	}
}

func TestListenReconnectsAfterDrop(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{text(42, device.Broadcast, "old")} // first dial only
	var waits []time.Duration
	c, _ := open(t, d, device.Options{ReconnectDelay: 10 * time.Millisecond,
		OnReconnect: func(_ error, wait time.Duration) { waits = append(waits, wait) }})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond)
		d.DropLink()
		for d.Dials() < 2 {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		d.Inject(text(42, device.Broadcast, "after"))
	}()
	var got []string
	err := c.Listen(ctx, func(p *pb.MeshPacket) error {
		got = append(got, string(p.GetDecoded().GetPayload()))
		if got[len(got)-1] == "after" {
			cancel()
		}
		return nil
	})
	if err != nil || !slices.Equal(got, []string{"old", "after"}) || !slices.Equal(waits, []time.Duration{10 * time.Millisecond}) {
		t.Fatal(got, err, waits)
	}
}

func TestListenGivesUpWhenNodeGone(t *testing.T) {
	d := fakedevice.New()
	c, _ := open(t, d, device.Options{ReconnectTimeout: 300 * time.Millisecond, ReconnectDelay: 10 * time.Millisecond})
	d.RefuseDials = true
	go func() { time.Sleep(50 * time.Millisecond); d.DropLink() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Listen(ctx, func(*pb.MeshPacket) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "did not come back") || ctx.Err() != nil {
		t.Fatal(err)
	}
}

func TestListenKeepsHandshakeHintAfterDrop(t *testing.T) {
	d := fakedevice.New()
	c, _ := open(t, d, device.Options{HandshakeTimeout: 100 * time.Millisecond, ReconnectTimeout: 300 * time.Millisecond, ReconnectDelay: 10 * time.Millisecond})
	d.Silent = true
	go func() { time.Sleep(50 * time.Millisecond); d.DropLink() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Listen(ctx, func(*pb.MeshPacket) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "did not come back after the connection dropped") || !strings.Contains(err.Error(), "another program") {
		t.Fatal(err)
	}
}

func TestListenGivesUpWhenKickedRepeatedly(t *testing.T) {
	d := fakedevice.New()
	d.DropAfterHandshake = 30 * time.Millisecond // every link, including the first
	var waits []time.Duration
	c, _ := open(t, d, device.Options{StableLink: time.Second, ReconnectDelay: 10 * time.Millisecond,
		OnReconnect: func(_ error, wait time.Duration) { waits = append(waits, wait) }})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Listen(ctx, func(*pb.MeshPacket) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "another client") || ctx.Err() != nil || d.Dials() > 4 {
		t.Fatal(err, d.Dials())
	}
	if !slices.Equal(waits, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}) {
		t.Fatal(waits)
	}
}

func TestListenStableLinkResetsDropCount(t *testing.T) {
	d := fakedevice.New()
	var waits []time.Duration
	c, _ := open(t, d, device.Options{StableLink: 50 * time.Millisecond, ReconnectDelay: 10 * time.Millisecond,
		OnReconnect: func(_ error, wait time.Duration) { waits = append(waits, wait) }})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		for i := range 4 { // each link outlives StableLink before it drops
			for d.Dials() < i+1 {
				time.Sleep(5 * time.Millisecond)
			}
			time.Sleep(100 * time.Millisecond)
			d.DropLink()
		}
		for d.Dials() < 5 {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		d.Inject(text(42, device.Broadcast, "still here"))
	}()
	err := c.Listen(ctx, func(*pb.MeshPacket) error { cancel(); return nil })
	if err != nil || len(waits) != 4 || slices.ContainsFunc(waits, func(w time.Duration) bool { return w != 10*time.Millisecond }) {
		t.Fatal(err, waits)
	}
}

func TestListenGivesUpWhenKickedDuringHandshake(t *testing.T) {
	d := fakedevice.New()
	d.DropHandshakesFrom = 2 // every redial is closed mid-handshake
	var causes []string
	var waits []time.Duration
	c, _ := open(t, d, device.Options{ReconnectDelay: 10 * time.Millisecond,
		OnReconnect: func(cause error, wait time.Duration) {
			causes, waits = append(causes, cause.Error()), append(waits, wait)
		}})
	go func() { time.Sleep(50 * time.Millisecond); d.DropLink() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	err := c.Listen(ctx, func(*pb.MeshPacket) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "another client") || time.Since(start) > time.Second || d.Dials() > 4 {
		t.Fatal(err, time.Since(start), d.Dials())
	}
	if len(causes) != 3 || !strings.HasPrefix(causes[0], "connection lost: ") ||
		!strings.HasPrefix(causes[1], "reconnect failed: handshake: node closed the connection") ||
		!slices.Equal(waits, []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond}) {
		t.Fatal(causes, waits)
	}
}
