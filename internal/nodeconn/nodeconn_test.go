package nodeconn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"meshtastik/internal/daemon"
	"meshtastik/internal/device"
	"meshtastik/internal/devices"
	"meshtastik/internal/fakedevice"
	"meshtastik/internal/transport"
)

func TestChoose(t *testing.T) {
	one := func() ([]transport.SerialCandidate, error) {
		return []transport.SerialCandidate{{Path: "/dev/cu.a"}}, nil
	}
	two := func() ([]transport.SerialCandidate, error) {
		return []transport.SerialCandidate{{Path: "/dev/cu.a"}, {Path: "/dev/cu.b"}}, nil
	}
	none := func() ([]transport.SerialCandidate, error) { return nil, nil }
	if k, p, err := Choose(Flags{}, one); err != nil || k != transport.KindSerial || p != "/dev/cu.a" {
		t.Fatal(k, p, err)
	}
	if k, p, _ := Choose(Flags{Host: "10.0.0.2"}, none); k != transport.KindTCP || p != "10.0.0.2" {
		t.Fatal(k, p)
	}
	if _, _, err := Choose(Flags{}, none); err == nil || !strings.Contains(err.Error(), "no Meshtastic device found") {
		t.Fatal(err)
	}
	if _, _, err := Choose(Flags{}, two); err == nil || !strings.Contains(err.Error(), "/dev/cu.b") {
		t.Fatal(err)
	}
}

func TestHandshakeTimeoutPerKind(t *testing.T) {
	if HandshakeTimeout(transport.KindBLE) != 30*time.Second || HandshakeTimeout(transport.KindSerial) != 15*time.Second {
		t.Fatal()
	}
}

func TestFlagsForAndKindOf(t *testing.T) {
	if f := FlagsFor(daemon.Target{Kind: "tcp", Addr: "h"}); f != (Flags{Host: "h"}) || KindOf(f) != transport.KindTCP {
		t.Fatal(f)
	}
	if f := FlagsFor(daemon.Target{Kind: "serial", Addr: "/dev/x"}); f != (Flags{Port: "/dev/x"}) || KindOf(f) != transport.KindSerial {
		t.Fatal(f)
	}
}

func TestResolveOrder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "devices.yaml")
	if err := devices.Save(file, &devices.File{
		Current: &devices.Current{Device: "home"},
		Devices: map[string]devices.Target{"home": {Host: "10.0.0.1"}, "car": {Port: "/dev/car"}},
	}); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	sel := Selection{DevicesFile: file, Getenv: func(k string) string { return env[k] }}

	r, err := Resolve(sel)
	if err != nil || r.Flags != (Flags{Host: "10.0.0.1"}) || r.Name != "home" || r.From != FromCurrent {
		t.Fatal(r, err)
	}
	env["MESHTASTIK_DEVICE"] = "car"
	if r, err = Resolve(sel); err != nil || r.Flags != (Flags{Port: "/dev/car"}) || r.From != "from MESHTASTIK_DEVICE" {
		t.Fatal(r, err)
	}
	sel.Flags = Flags{BLE: "x"}
	if r, err = Resolve(sel); err != nil || r.Flags != (Flags{BLE: "x"}) || r.From != "" {
		t.Fatal(r, err)
	}
	sel.Flags = Flags{}
	env["MESHTASTIK_HOST"] = "h"
	if _, err = Resolve(sel); err == nil || !strings.Contains(err.Error(), "MESHTASTIK_DEVICE and MESHTASTIK_HOST") {
		t.Fatal(err)
	}
	if s := (Resolved{Flags: Flags{Host: "h"}, Name: "home"}).String(); s != "home (tcp:h)" {
		t.Fatal(s)
	}
}

// socketPath is short enough for the 104-byte unix socket limit on macOS.
func socketPath(t *testing.T) string {
	dir, err := os.MkdirTemp("", "nc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

// serve runs a daemon with version v on sock until the test ends.
func serve(t *testing.T, sock, v string, d *fakedevice.Device) error {
	ln, err := daemon.Listen(sock)
	if err != nil {
		return err
	}
	srv := &daemon.Server{Version: v, Dial: func(daemon.Target) (transport.Dialer, error) { return d.Dialer(), nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return daemon.WaitRunning(ctx, sock, 5*time.Second)
}

func TestDaemonDialStartsWhenNotRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := fakedevice.New()
	starts := 0
	dd := DaemonDial{Socket: socketPath(t), Version: "v", Start: func(ctx context.Context, sock string) error {
		starts++
		return serve(t, sock, "v", d)
	}}
	dial, err := dd.Dialer(Flags{Host: "fake"}, false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c, err := device.Open(ctx, dial, device.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if starts != 1 || c.State().Self().GetUser().GetLongName() != "Test Node" {
		t.Fatal(starts)
	}
}

func TestDaemonDialReportsVersion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sock := socketPath(t)
	if err := serve(t, sock, "x", fakedevice.New()); err != nil {
		t.Fatal(err)
	}
	var got string
	dd := DaemonDial{Socket: sock, Version: "v", OnVersion: func(v string) { got = v }}
	dial, err := dd.Dialer(Flags{Host: "fake"}, false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c, err := device.Open(ctx, dial, device.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got != "x" {
		t.Fatalf("OnVersion(%q)", got)
	}
}
