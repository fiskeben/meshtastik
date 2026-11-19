package fakedevice_test

import (
	"context"
	"errors"
	"testing"

	"meshtastik/internal/fakedevice"
	"meshtastik/internal/transport"
)

func TestRecvAfterCloseReturnsErrClosed(t *testing.T) {
	d := fakedevice.New()
	conn, err := d.Dialer()(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a done ctx must not win over a closed link
	for range 100 {
		if _, err := conn.Recv(ctx); !errors.Is(err, transport.ErrClosed) {
			t.Fatal(err)
		}
	}
}
