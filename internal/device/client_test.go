package device_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func open(t *testing.T, d *fakedevice.Device, o device.Options) (*device.Client, error) {
	t.Helper()
	c, err := device.Open(ctxT(t), d.Dialer(), o)
	if err == nil {
		t.Cleanup(func() { c.Close() })
	}
	return c, err
}

func TestOpenHandshake(t *testing.T) {
	d := fakedevice.New()
	c, err := open(t, d, device.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := c.State()
	if s.MyInfo.MyNodeNum != d.NodeNum || !proto.Equal(s.Config, d.Config) || !proto.Equal(s.ModuleConfig, d.ModuleConfig) {
		t.Fatal("state mismatch")
	}
	if s.Channels[0].Role != pb.Channel_PRIMARY || s.Self().User.LongName != "Test Node" {
		t.Fatal(s.Self())
	}
}

func TestOpenIgnoresStaleConfigComplete(t *testing.T) {
	d := fakedevice.New()
	d.StaleCompleteID = true
	c, err := open(t, d, device.Options{})
	if err != nil || c.State().Config.Lora == nil {
		t.Fatal(err)
	}
}

func TestOpenUnsupportedFirmware(t *testing.T) {
	d := fakedevice.New()
	d.FirmwareVersion = "2.3.2.abc1234"
	_, err := open(t, d, device.Options{})
	if !errors.Is(err, device.ErrUnsupportedFirmware) || !strings.Contains(err.Error(), "2.3.2") {
		t.Fatal(err)
	}
}

func TestOpenHandshakeTimeout(t *testing.T) {
	d := fakedevice.New()
	d.Silent = true
	_, err := open(t, d, device.Options{HandshakeTimeout: 100 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "no response") {
		t.Fatal(err)
	}
}

func TestOpenLargeNodeDB(t *testing.T) {
	d := fakedevice.New()
	for i := range 300 {
		d.ExtraNodes = append(d.ExtraNodes, &pb.NodeInfo{Num: uint32(i + 1), User: &pb.User{LongName: fmt.Sprint("n", i)}})
	}
	c, err := open(t, d, device.Options{})
	if err != nil || len(c.State().Nodes) != 301 {
		t.Fatal(err)
	}
}

func TestHeartbeat(t *testing.T) {
	d := fakedevice.New()
	if _, err := open(t, d, device.Options{HeartbeatInterval: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if d.Heartbeats() < 2 {
		t.Fatal(d.Heartbeats())
	}
}
