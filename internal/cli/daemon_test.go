package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/daemon"
	"meshtastik/internal/device"
	"meshtastik/internal/devices"
	"meshtastik/internal/fakedevice"
	"meshtastik/internal/transport"
)

// testDaemon is an in-process daemon serving a fake device.
type testDaemon struct {
	sock    string
	d       *fakedevice.Device
	started int    // calls to StartDaemon
	devices string // Deps.DevicesFile
	cancel  context.CancelFunc
	done    chan struct{}
}

func newTestDaemon(t *testing.T, d *fakedevice.Device) *testDaemon {
	dir, err := os.MkdirTemp("", "mtc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	td := &testDaemon{sock: filepath.Join(dir, "d.sock"), d: d}
	t.Cleanup(td.stop)
	return td
}

// start runs the daemon in-process, as `daemon run` would.
func (td *testDaemon) start(t *testing.T) {
	ln, err := daemon.Listen(td.sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &daemon.Server{
		Version:       version,
		Dial:          func(daemon.Target) (transport.Dialer, error) { return td.d.Dialer(), nil },
		DeviceOptions: func(o *device.Options) { o.ReconnectDelay = 10 * time.Millisecond },
		Socket:        td.sock,
	}
	ctx, cancel := context.WithCancel(context.Background())
	td.cancel, td.done = cancel, make(chan struct{})
	go func() { defer close(td.done); srv.Serve(ctx, ln) }()
}

// waitNoClients waits until the daemon has noticed every command hang up.
func (td *testDaemon) waitNoClients(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		r, err := daemon.Request(context.Background(), td.sock, daemon.Hello{Op: daemon.OpStatus})
		if err == nil && (len(r.Status.Links) == 0 || r.Status.Links[0].Clients == 0) {
			return
		}
	}
	t.Fatal("daemon still has clients")
}

func (td *testDaemon) stop() {
	if td.cancel != nil {
		td.cancel()
		<-td.done
		td.cancel = nil
	}
}

// run executes the command tree with commands going through the daemon; a
// missing daemon is started in-process.
func (td *testDaemon) run(t *testing.T, ctx context.Context, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(Deps{
		Dial: func(ConnFlags) (transport.Dialer, error) {
			t.Error("dialled the node directly")
			return td.d.Dialer(), nil
		},
		Stdout:        &out,
		Stderr:        &errOut,
		Confirm:       func(string) bool { return true },
		DeviceOptions: func(o *device.Options) { o.RebootGrace, o.ReconnectDelay = 50*time.Millisecond, 10*time.Millisecond },
		DaemonSocket:  td.sock,
		DevicesFile:   td.devices,
		StartDaemon: func(sock string, args ...string) error {
			td.started++
			td.start(t)
			return nil
		},
	})
	root.SetArgs(append([]string{"--port", "/dev/fake"}, args...))
	err = root.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}

func TestCommandStartsTheDaemonAndReusesIt(t *testing.T) {
	d := fakedevice.New()
	td := newTestDaemon(t, d)
	for range 2 {
		out, _, err := td.run(t, context.Background(), "info")
		if err != nil || !strings.Contains(out, "Test Node") {
			t.Fatal(out, err)
		}
	}
	if td.started != 1 || d.Dials() != 1 {
		t.Fatal("started", td.started, "node dials", d.Dials())
	}
}

func TestConfigSetThroughTheDaemon(t *testing.T) {
	d := fakedevice.New()
	td := newTestDaemon(t, d)
	if _, _, err := td.run(t, context.Background(), "config", "set", "lora.hop_limit", "5"); err != nil {
		t.Fatal(err)
	}
	out, _, err := td.run(t, context.Background(), "config", "get", "lora.hop_limit")
	if err != nil || !strings.Contains(out, "5") {
		t.Fatal(out, err)
	}
}

func TestListenThroughTheDaemonGetsWhatArrivedMeanwhile(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{text(42, 0xffffffff, "queued")}
	td := newTestDaemon(t, d)
	if _, _, err := td.run(t, context.Background(), "info"); err != nil { // must not eat the backlog
		t.Fatal(err)
	}
	out, _, err := td.run(t, context.Background(), "listen")
	if err != nil || !strings.Contains(out, ": queued") {
		t.Fatal(out, err)
	}
	td.waitNoClients(t) // a packet that arrives while listen is still attached goes to it
	d.Inject(text(42, 0xffffffff, "between runs"))
	time.Sleep(50 * time.Millisecond)
	out, _, err = td.run(t, context.Background(), "listen", "--json", "--raw")
	var m map[string]any
	if err != nil || json.Unmarshal([]byte(out), &m) != nil || m["text"] != "between runs" || m["raw"] == nil {
		t.Fatal(out, err)
	}
}

func TestListenAndSendAtTheSameTime(t *testing.T) {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{LongName: "Car", ShortName: "CAR1"}}}
	d.AckTexts = true
	td := newTestDaemon(t, d)
	td.start(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		out string
		err error
	}
	res := make(chan result, 1)
	go func() {
		out, _, err := td.run(t, ctx, "listen", "-f", "--count", "1")
		res <- result{out, err}
	}()
	time.Sleep(200 * time.Millisecond) // let listen attach
	if _, _, err := td.run(t, context.Background(), "send", "--to", "CAR1", "on my way"); err != nil {
		t.Fatal(err)
	}
	d.Inject(text(42, 0xffffffff, "see you"))
	select {
	case r := <-res:
		if r.err != nil || !strings.Contains(r.out, ": see you") {
			t.Fatal(r.out, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listen got nothing")
	}
	if texts := d.Texts(); len(texts) != 1 || d.Dials() != 1 {
		t.Fatal(len(texts), d.Dials())
	}
}

func TestDaemonStatusAndStopCommands(t *testing.T) {
	td := newTestDaemon(t, fakedevice.New())
	out, _, err := td.run(t, context.Background(), "daemon", "status")
	if err != nil || out != "daemon not running\n" {
		t.Fatalf("%q %v", out, err)
	}
	out, _, err = td.run(t, context.Background(), "--json", "daemon", "status")
	if err != nil || !strings.Contains(out, `"running": false`) {
		t.Fatalf("%q %v", out, err)
	}
	if _, _, err := td.run(t, context.Background(), "info"); err != nil {
		t.Fatal(err)
	}
	out, _, err = td.run(t, context.Background(), "daemon", "status")
	if err != nil || !strings.Contains(out, "serial:/dev/fake") || !strings.Contains(out, "connected") || !strings.Contains(out, "Test Node (TEST) !1234abcd") {
		t.Fatalf("%q %v", out, err)
	}
	out, _, err = td.run(t, context.Background(), "daemon", "stop")
	if err != nil || out != "daemon stopped\n" {
		t.Fatalf("%q %v", out, err)
	}
	out, _, err = td.run(t, context.Background(), "daemon", "stop")
	if err != nil || out != "daemon not running\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestDaemonStatusNamesDevices(t *testing.T) {
	td := newTestDaemon(t, fakedevice.New())
	td.devices = filepath.Join(t.TempDir(), "devices.yaml")
	if err := devices.Save(td.devices, &devices.File{Devices: map[string]devices.Target{
		"desk": {Port: "/dev/fake"}, "bench": {Port: "/dev/fake"}, "home": {Host: "10.0.0.2"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := td.run(t, context.Background(), "info"); err != nil {
		t.Fatal(err)
	}
	out, _, err := td.run(t, context.Background(), "daemon", "status")
	if err != nil || !strings.Contains(out, "DEVICE      TARGET") || !strings.Contains(out, "bench,desk  serial:/dev/fake") {
		t.Fatalf("%q %v", out, err)
	}
	out, _, err = td.run(t, context.Background(), "--json", "daemon", "status")
	var st struct {
		Links []struct {
			Device string        `json:"device"`
			Target daemon.Target `json:"target"`
		} `json:"links"`
	}
	if err != nil || json.Unmarshal([]byte(out), &st) != nil || len(st.Links) != 1 ||
		st.Links[0].Device != "bench,desk" || st.Links[0].Target.Addr != "/dev/fake" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestDaemonVersionMismatchNotes(t *testing.T) {
	td := newTestDaemon(t, fakedevice.New())
	td.start(t)
	old := version
	version = "9.9.9"
	defer func() { version = old }()
	_, errOut, err := td.run(t, context.Background(), "info")
	if err != nil || strings.Count(errOut, "daemon restart") != 1 {
		t.Fatalf("%q %v", errOut, err)
	}
}

func TestNoDaemonConnectsDirectly(t *testing.T) {
	d := fakedevice.New()
	var out bytes.Buffer
	root := NewRoot(Deps{
		Dial:         func(ConnFlags) (transport.Dialer, error) { return d.Dialer(), nil },
		Stdout:       &out,
		Stderr:       &bytes.Buffer{},
		DaemonSocket: "/nonexistent/d.sock",
		StartDaemon:  func(string, ...string) error { t.Error("started the daemon"); return nil },
	})
	root.SetArgs([]string{"--no-daemon", "info"})
	if err := root.Execute(); err != nil || !strings.Contains(out.String(), "Test Node") {
		t.Fatal(out.String(), err)
	}
}
