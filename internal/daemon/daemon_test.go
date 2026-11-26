package daemon_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/configpath"
	"meshtastik/internal/daemon"
	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
	"meshtastik/internal/transport"
)

var target = daemon.Target{Kind: "serial", Addr: "/dev/fake"}

// socketPath is short enough for the 104-byte unix socket limit on macOS.
func socketPath(t *testing.T) string {
	dir, err := os.MkdirTemp("", "mtd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

type harness struct {
	sock  string
	srv   *daemon.Server
	dials atomic.Int32 // dials of the daemon to the node
	done  chan error
	stop  context.CancelFunc
}

func start(t *testing.T, d *fakedevice.Device, idle time.Duration) *harness {
	t.Helper()
	h := &harness{sock: socketPath(t), done: make(chan error, 1)}
	h.srv = &daemon.Server{
		Version:     "test",
		IdleTimeout: idle,
		Dial: func(daemon.Target) (transport.Dialer, error) {
			dial := d.Dialer()
			return func(ctx context.Context) (transport.Conn, error) { h.dials.Add(1); return dial(ctx) }, nil
		},
		DeviceOptions: func(o *device.Options) { o.ReconnectDelay = 10 * time.Millisecond },
	}
	ln, err := daemon.Listen(h.sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.stop = cancel
	go func() { h.done <- h.srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	return h
}

func (h *harness) dialer(backlog bool) transport.Dialer {
	return func(ctx context.Context) (transport.Conn, error) {
		conn, _, err := daemon.Connect(ctx, h.sock, daemon.Hello{Version: "test", Target: target, Backlog: backlog})
		return conn, err
	}
}

func (h *harness) open(t *testing.T, backlog bool) *device.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := device.Open(ctx, h.dialer(backlog), device.Options{RebootGrace: 50 * time.Millisecond, ReconnectDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func text(s string) *pb.MeshPacket {
	return &pb.MeshPacket{From: 42, To: device.Broadcast, Id: rand.Uint32() | 1,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(s)}}}
}

// next returns the text of the next packet c receives.
func next(t *testing.T, c *device.Client) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var got string
	err := c.Listen(ctx, func(m *pb.FromRadio) error {
		got = string(m.GetPacket().GetDecoded().GetPayload())
		return errors.New("got one")
	})
	if got == "" {
		t.Fatalf("no packet: %v", err)
	}
	return got
}

func TestClientsShareOneNodeConnection(t *testing.T) {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{LongName: "Car"}}}
	h := start(t, d, 0)
	a := h.open(t, false)
	b := h.open(t, false)
	if h.dials.Load() != 1 || d.Dials() != 1 {
		t.Fatal("daemon dials", h.dials.Load(), d.Dials())
	}
	st := b.State()
	if st.Self().GetUser().GetLongName() != "Test Node" || st.Nodes[42].GetUser().GetLongName() != "Car" ||
		st.Config.GetLora().GetRegion() != pb.Config_LoRaConfig_EU_868 || st.Channels[0].GetRole() != pb.Channel_PRIMARY ||
		st.Metadata.GetFirmwareVersion() != d.FirmwareVersion || a.Kind() != transport.KindSerial {
		t.Fatalf("%+v", st)
	}
	d.Inject(text("to everyone"))
	if next(t, a) != "to everyone" || next(t, b) != "to everyone" {
		t.Fatal()
	}
}

func TestSendGoesThroughTheDaemon(t *testing.T) {
	d := fakedevice.New()
	d.AckTexts = true
	h := start(t, d, 0)
	c := h.open(t, false)
	if err := c.SendText(context.Background(), "hi", 42, 0, true); err != nil {
		t.Fatal(err)
	}
	if texts := d.Texts(); len(texts) != 1 || string(texts[0].GetDecoded().GetPayload()) != "hi" {
		t.Fatal(texts)
	}
	if d.Heartbeats() != 0 {
		t.Fatal("client heartbeat reached the node")
	}
}

// configChange is the change that sets config path to raw.
func configChange(t *testing.T, st *device.State, path, raw string) device.Change {
	t.Helper()
	p, err := configpath.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := proto.Clone(st.Config).(*pb.LocalConfig)
	if err := configpath.Set(cfg, st.ModuleConfig, p, raw); err != nil {
		t.Fatal(err)
	}
	cs, _ := configpath.Changes(st.Config, cfg, st.ModuleConfig, st.ModuleConfig)
	if len(cs) != 1 {
		t.Fatal(cs)
	}
	return device.Change{Config: cs[0]}
}

func TestApplyIsVerifiedThroughTheDaemon(t *testing.T) {
	d := fakedevice.New()
	h := start(t, d, 0)
	c := h.open(t, false)
	change := configChange(t, c.State(), "lora.hop_limit", "5")
	if n, err := c.Apply(context.Background(), []device.Change{change}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	// A later client sees the new value from the daemon's re-read state.
	if got := h.open(t, false).State().Config.GetLora().GetHopLimit(); got != 5 {
		t.Fatal(got)
	}
}

func TestApplyRebootThroughTheDaemon(t *testing.T) {
	d := fakedevice.New()
	d.RebootOnCommit = true
	h := start(t, d, 0)
	c := h.open(t, false)
	change := configChange(t, c.State(), "lora.region", "US")
	if n, err := c.Apply(context.Background(), []device.Change{change}); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if h.dials.Load() != 2 {
		t.Fatal("daemon dials", h.dials.Load())
	}
}

func TestBacklogGoesToTheNextListener(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{text("queued on the node")}
	h := start(t, d, 0)
	other := h.open(t, false) // e.g. info: must not eat the backlog
	other.Close()
	l := h.open(t, true)
	if got := next(t, l); got != "queued on the node" {
		t.Fatal(got)
	}
	l.Close()
	d.AckTexts = true // its ack must not be kept for the next listener
	if err := h.open(t, false).SendText(context.Background(), "hi", 42, 0, true); err != nil {
		t.Fatal(err)
	}
	d.Inject(text("while nobody listened"))
	time.Sleep(50 * time.Millisecond)
	if got := next(t, h.open(t, true)); got != "while nobody listened" {
		t.Fatal(got)
	}
}

func TestNodeDropReconnectsClients(t *testing.T) {
	d := fakedevice.New()
	h := start(t, d, 0)
	c := h.open(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond)
		d.DropLink()
		for d.Dials() < 2 {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		d.Inject(text("after"))
	}()
	var got []string
	err := c.Listen(ctx, func(m *pb.FromRadio) error {
		got = append(got, string(m.GetPacket().GetDecoded().GetPayload()))
		cancel()
		return nil
	})
	if err != nil || !slices.Equal(got, []string{"after"}) {
		t.Fatal(got, err)
	}
}

func TestConnectErrorReachesTheClient(t *testing.T) {
	d := fakedevice.New()
	d.RefuseDials = true
	h := start(t, d, 0)
	_, _, err := daemon.Connect(context.Background(), h.sock, daemon.Hello{Target: target})
	if err == nil || err.Error() != "fakedevice: dial refused" {
		t.Fatal(err)
	}
}

func TestStatusAndStop(t *testing.T) {
	d := fakedevice.New()
	h := start(t, d, 0)
	h.open(t, false)
	r, err := daemon.Request(context.Background(), h.sock, daemon.Hello{Op: daemon.OpStatus})
	if err != nil || r.Status == nil || len(r.Status.Links) != 1 {
		t.Fatal(r, err)
	}
	if l := r.Status.Links[0]; l.Target != target || l.State != "connected" || l.Clients != 1 || l.Node != "Test Node (TEST)" {
		t.Fatalf("%+v", l)
	}
	if _, err := daemon.Request(context.Background(), h.sock, daemon.Hello{Op: daemon.OpStop}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not stop")
	}
	h.done <- nil // for Cleanup
	if _, err := daemon.Request(context.Background(), h.sock, daemon.Hello{Op: daemon.OpStatus}); !errors.Is(err, daemon.ErrNotRunning) {
		t.Fatal(err)
	}
}

func TestIdleTimeoutDisconnectsAndExits(t *testing.T) {
	d := fakedevice.New()
	h := start(t, d, 200*time.Millisecond)
	c := h.open(t, false)
	c.Close()
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not exit when idle")
	}
	h.done <- nil
}

func TestListenRefusesALiveDaemonAndReplacesAStaleSocket(t *testing.T) {
	h := start(t, fakedevice.New(), 0)
	if _, err := daemon.Listen(h.sock); !errors.Is(err, daemon.ErrAlreadyRunning) {
		t.Fatal(err)
	}
	stale := socketPath(t)
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := daemon.Listen(stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

func TestProtocolMismatchIsRefused(t *testing.T) {
	h := start(t, fakedevice.New(), 0)
	r, err := daemon.Request(context.Background(), h.sock, daemon.Hello{Op: daemon.OpStatus})
	if err != nil || r.Proto != daemon.Proto || r.Version != "test" {
		t.Fatal(r, err)
	}
}

func TestRemoteAdminDoesNotStaleTheCache(t *testing.T) {
	d := fakedevice.New()
	r := fakedevice.NewRemote(0x99, "Hill", "HIL1")
	r.Config.Security.AdminKey = [][]byte{d.PublicKey}
	d.Remotes = map[uint32]*fakedevice.Remote{0x99: r}
	h := start(t, d, 0)
	c := h.open(t, false)
	if d.WantConfigs() != 1 {
		t.Fatal("daemon handshakes", d.WantConfigs())
	}
	if _, err := c.Request(context.Background(), 0x99, pb.PortNum_ADMIN_APP,
		&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerRequest{GetOwnerRequest: true}}, device.RequestOptions{PKI: true}); err != nil {
		t.Fatal(err)
	}
	h.open(t, false)
	if d.WantConfigs() != 1 {
		t.Fatal("remote admin made the daemon re-read the node:", d.WantConfigs())
	}
	ch := configChange(t, c.State(), "lora.hop_limit", "5") // local admin still does
	if _, err := c.Apply(context.Background(), []device.Change{ch}); err != nil {
		t.Fatal(err)
	}
	if d.WantConfigs() < 2 {
		t.Fatal("local admin did not make the daemon re-read the node")
	}
}

// textWithin returns the next text packet c receives within wait, or nil.
func textWithin(c *device.Client, wait time.Duration) *pb.MeshPacket {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var got *pb.MeshPacket
	c.Listen(ctx, func(m *pb.FromRadio) error {
		if p := m.GetPacket(); p.GetDecoded().GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP {
			got = p
			return errors.New("got one")
		}
		return nil
	})
	return got
}

func TestTextFromOneClientReachesTheOthers(t *testing.T) {
	d := fakedevice.New()
	h := start(t, d, 0)
	a := h.open(t, false)
	b := h.open(t, false)
	if err := a.SendText(context.Background(), "hi", device.Broadcast, 0, false); err != nil {
		t.Fatal(err)
	}
	p := textWithin(b, 3*time.Second)
	if string(p.GetDecoded().GetPayload()) != "hi" || p.GetFrom() != d.NodeNum || p.GetRxTime() != 0 {
		t.Fatalf("%v", p)
	}
}

func TestSenderDoesNotGetItsOwnTextBack(t *testing.T) {
	h := start(t, fakedevice.New(), 0)
	a := h.open(t, false)
	h.open(t, false)
	if err := a.SendText(context.Background(), "hi", device.Broadcast, 0, false); err != nil {
		t.Fatal(err)
	}
	if p := textWithin(a, 300*time.Millisecond); p != nil {
		t.Fatalf("sender got %v", p)
	}
}

func TestEchoesSkipTheBacklog(t *testing.T) {
	h := start(t, fakedevice.New(), 0)
	a := h.open(t, false)
	if err := a.SendText(context.Background(), "hi", device.Broadcast, 0, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if p := textWithin(h.open(t, true), 300*time.Millisecond); p != nil {
		t.Fatalf("backlog had %v", p)
	}
}
