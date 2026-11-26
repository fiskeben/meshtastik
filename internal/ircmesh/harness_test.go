package ircmesh

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

const (
	arveNum = 0x1bf0411d
	oldNum  = 0x5
)

// newFake is the user's node: 🥸/Ricco with a private channel 4, Ryfylke.
func newFake() *fakedevice.Device {
	d := fakedevice.New()
	d.NodeNum = selfNum
	d.Owner = &pb.User{ShortName: "🥸", LongName: "Ricco", PublicKey: d.PublicKey}
	d.Channels[4] = &pb.Channel{Index: 4, Role: pb.Channel_SECONDARY,
		Settings: &pb.ChannelSettings{Name: "Ryfylke", Psk: make([]byte, 32)}}
	return d
}

// withNodes adds Arve Tak (AS02, heard an hour ago on channel 4) and OLD
// (heard 40 hours ago) to the node DB.
func withNodes(d *fakedevice.Device) *fakedevice.Device {
	now := time.Now()
	d.ExtraNodes = []*pb.NodeInfo{
		{Num: arveNum, Channel: 4, LastHeard: uint32(now.Add(-time.Hour).Unix()), User: &pb.User{ShortName: "AS02", LongName: "Arve Tak"}},
		{Num: oldNum, LastHeard: uint32(now.Add(-40 * time.Hour).Unix()), User: &pb.User{ShortName: "OLD", LongName: "Old node"}},
	}
	return d
}

type harness struct {
	t    *testing.T
	d    *fakedevice.Device
	srv  *Server
	addr string
	done chan error
	stop context.CancelFunc
	once sync.Once
}

func testConfig(d *fakedevice.Device, dir string) Config {
	return Config{
		Version: "test",
		Open: func(ctx context.Context, opts device.Options) (Mesh, error) {
			opts.ReconnectDelay = 10 * time.Millisecond
			c, err := device.Open(ctx, d.Dialer(), opts)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		StateDir:     dir,
		SendInterval: 20 * time.Millisecond,
		DMTimeout:    300 * time.Millisecond,
	}
}

func newHarness(t *testing.T, d *fakedevice.Device, adjust func(*Config)) *harness {
	t.Helper()
	cfg := testConfig(d, t.TempDir())
	if adjust != nil {
		adjust(&cfg)
	}
	return startHarness(t, d, cfg)
}

func startHarness(t *testing.T, d *fakedevice.Device, cfg Config) *harness {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, d: d, srv: New(cfg), addr: ln.Addr().String(), done: make(chan error, 1), stop: cancel}
	go func() { h.done <- h.srv.Run(ctx, ln) }()
	t.Cleanup(h.close)
	h.waitOpen()
	return h
}

// waitOpen waits until the server has connected to the node and listens.
func (h *harness) waitOpen() {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.srv.mu.Lock()
		up := h.srv.mesh != nil
		h.srv.mu.Unlock()
		if up {
			settle()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatal("server never connected to the node")
}

// close stops the server and waits for Run to return.
func (h *harness) close() {
	h.once.Do(func() {
		h.stop()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			h.t.Error("server did not stop")
		}
	})
}

// clock is a controllable Config.Now: the real time plus an offset.
type clock struct{ offset atomic.Int64 }

func (c *clock) now() time.Time          { return time.Now().Add(time.Duration(c.offset.Load())) }
func (c *clock) advance(d time.Duration) { c.offset.Add(int64(d)) }

// settle waits for injected packets to be handled.
func settle() { time.Sleep(100 * time.Millisecond) }

type ircClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	seen []string
}

func (h *harness) dial(t *testing.T) *ircClient {
	t.Helper()
	conn, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &ircClient{t: t, conn: conn, r: bufio.NewReader(conn)}
}

func (c *ircClient) send(format string, args ...any) {
	c.t.Helper()
	if _, err := fmt.Fprintf(c.conn, format+"\r\n", args...); err != nil {
		c.t.Fatal(err)
	}
}

// line reads one line within d; ok is false on timeout or close.
func (c *ircClient) line(d time.Duration) (string, bool) {
	c.conn.SetReadDeadline(time.Now().Add(d))
	l, err := c.r.ReadString('\n')
	if err != nil {
		return "", false
	}
	l = strings.TrimRight(l, "\r\n")
	c.seen = append(c.seen, l)
	return l, true
}

// expect reads lines until one matches re and returns its submatches.
func (c *ircClient) expect(re string) []string {
	c.t.Helper()
	rx := regexp.MustCompile(re)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		l, ok := c.line(time.Until(deadline))
		if !ok {
			break
		}
		if m := rx.FindStringSubmatch(l); m != nil {
			return m
		}
	}
	c.t.Fatalf("no line matching %q; got:\n%s", re, strings.Join(c.seen, "\n"))
	return nil
}

// expectNone fails if a line matching re arrives within d.
func (c *ircClient) expectNone(re string, d time.Duration) {
	c.t.Helper()
	rx := regexp.MustCompile(re)
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		l, ok := c.line(time.Until(deadline))
		if !ok {
			return
		}
		if rx.MatchString(l) {
			c.t.Fatalf("unexpected %q", l)
		}
	}
}

// closed reports whether the server closes the connection within 3 s.
func (c *ircClient) closed() bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(deadline)
		if _, err := c.r.ReadString('\n'); err != nil {
			return !isTimeout(err)
		}
	}
	return false
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

// register signs on as nick and waits for the end of the welcome burst
// (the last channel's end of NAMES).
func (c *ircClient) register(nick string) {
	c.t.Helper()
	c.send("NICK %s", nick)
	c.send("USER u 0 * :real name")
	c.expect(` 376 `)
	c.expect(` 366 \S+ #Ryfylke `)
}

// rx is a text packet received over the radio on channel ch.
func rx(from uint32, ch uint32, text string) *pb.MeshPacket {
	return &pb.MeshPacket{From: from, To: device.Broadcast, Channel: ch, Id: device.NewPacketID(), RxTime: proto.Uint32(uint32(time.Now().Unix())),
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(text)}}}
}
