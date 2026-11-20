package device

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/fakedevice"
)

func openFake(t *testing.T, d *fakedevice.Device) *Client {
	t.Helper()
	c, err := Open(context.Background(), d.Dialer(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestSendTextAck(t *testing.T) {
	d := fakedevice.New()
	d.AckTexts = true
	c := openFake(t, d)
	if err := c.SendText(context.Background(), "hi", 42, 1, true); err != nil {
		t.Fatal(err)
	}
	p := d.Texts()[0]
	if p.To != 42 || p.Channel != 1 || !p.WantAck || p.Id == 0 {
		t.Errorf("packet %v", p)
	}
}

func TestSendTextNoAck(t *testing.T) {
	c := openFake(t, fakedevice.New())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.SendText(ctx, "hi", 42, 0, true); !errors.Is(err, ErrNoAck) {
		t.Fatal(err)
	}
}

func TestSendTextBroadcastNoWait(t *testing.T) {
	d := fakedevice.New()
	c := openFake(t, d)
	if err := c.SendText(context.Background(), "hi", Broadcast, 0, true); err != nil {
		t.Fatal(err)
	}
	if p := d.Texts()[0]; p.WantAck {
		t.Error("broadcast asked for ack")
	}
}

func TestSendTextTooLong(t *testing.T) {
	c := openFake(t, fakedevice.New())
	err := c.SendText(context.Background(), strings.Repeat("é", 117), Broadcast, 0, false) // 234 bytes
	if err == nil || !strings.Contains(err.Error(), "233") {
		t.Fatal(err)
	}
}

func sendAsync(c *Client, ctx context.Context) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- c.SendText(ctx, "hi", 42, 0, true) }()
	return ch
}

func TestSendTextImplicitOnly(t *testing.T) {
	d := fakedevice.New()
	d.ImplicitAckTexts = true
	c := openFake(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := c.SendText(ctx, "hi", 42, 0, true)
	if !errors.Is(err, ErrNoAck) || !errors.Is(err, ErrImplicitAck) || !strings.Contains(err.Error(), "implicit") {
		t.Fatal(err)
	}
}

func TestSendTextImplicitThenAck(t *testing.T) {
	d := fakedevice.New()
	d.ImplicitAckTexts, d.AckTexts = true, true
	c := openFake(t, d)
	if err := c.SendText(context.Background(), "hi", 42, 0, true); err != nil {
		t.Fatal(err)
	}
}

func TestSendTextDeliveryFailed(t *testing.T) {
	d := fakedevice.New()
	d.AckTexts, d.AckError = true, pb.Routing_NO_RESPONSE
	c := openFake(t, d)
	err := c.SendText(context.Background(), "hi", 42, 0, true)
	if err == nil || !strings.Contains(err.Error(), "delivery failed: NO_RESPONSE") {
		t.Fatal(err)
	}
}

func TestSendTextIgnoresOtherRequestID(t *testing.T) {
	c := openFake(t, fakedevice.New())
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	res := sendAsync(c, ctx)
	ack := &pb.MeshPacket{From: 42, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ROUTING_APP}}}
	var id uint32
	for id == 0 {
		c.wmu.Lock()
		for k := range c.waiters {
			id = k
		}
		c.wmu.Unlock()
		time.Sleep(time.Millisecond)
	}
	c.deliver(id+1, ack)
	if err := <-res; !errors.Is(err, ErrNoAck) {
		t.Fatal(err)
	}
}

func TestSendTextRejectsSelfAndZero(t *testing.T) {
	d := fakedevice.New()
	c := openFake(t, d)
	for _, to := range []uint32{0, d.NodeNum} {
		if err := c.SendText(context.Background(), "hi", to, 0, true); err == nil {
			t.Errorf("to %x accepted", to)
		}
	}
}

func TestSendTextBroadcastCarriesHopLimit(t *testing.T) {
	d := fakedevice.New()
	d.Config.Lora.HopLimit = 4
	c := openFake(t, d)
	if err := c.SendText(context.Background(), "hello all", Broadcast, 0, false); err != nil {
		t.Fatal(err)
	}
	if p := d.Texts()[0]; p.GetHopLimit() != 4 {
		t.Fatalf("broadcast hop limit %d: firmware only fills it in for want_ack packets", p.GetHopLimit())
	}
}
