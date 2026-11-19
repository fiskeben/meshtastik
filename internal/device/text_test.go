package device

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
