package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/daemon"
	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
	"meshtastik/internal/nodeconn"
	"meshtastik/internal/transport"
)

// socketPath is short enough for the 104-byte unix socket limit on macOS.
func socketPath(t *testing.T) string {
	dir, err := os.MkdirTemp("", "irm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

type env struct {
	t    *testing.T
	d    *fakedevice.Device
	deps deps
	vars map[string]string
	addr chan net.Addr
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, d: fakedevice.New(), vars: map[string]string{}, addr: make(chan net.Addr, 1)}
	e.deps = deps{
		Dial:        func(nodeconn.Flags) (transport.Dialer, error) { return e.d.Dialer(), nil },
		Getenv:      func(k string) string { return e.vars[k] },
		Stderr:      io.Discard,
		Socket:      socketPath(t),
		DevicesFile: filepath.Join(t.TempDir(), "devices.yaml"),
		StateDir:    t.TempDir(),
		Ready:       func(a net.Addr) { e.addr <- a },
	}
	return e
}

// start runs ircmesh with args until the test ends and returns the address
// it serves IRC on.
func (e *env) start(args ...string) net.Addr {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, args, e.deps) }()
	e.t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			e.t.Error("ircmesh did not stop")
		}
	})
	select {
	case a := <-e.addr:
		return a
	case err := <-done:
		e.t.Fatal("ircmesh exited:", err)
	case <-time.After(5 * time.Second):
		e.t.Fatal("ircmesh never served")
	}
	return nil
}

// expect reads lines from r until one matches re.
func expect(t *testing.T, conn net.Conn, r *bufio.Reader, re string) {
	t.Helper()
	rx := regexp.MustCompile(re)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var seen []string
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("no %q (%v); got:\n%s", re, err, strings.Join(seen, ""))
		}
		seen = append(seen, l)
		if rx.MatchString(l) {
			return
		}
	}
}

func register(t *testing.T, conn net.Conn) {
	t.Helper()
	fmt.Fprint(conn, "NICK foo\r\nUSER u 0 * :r\r\n")
	expect(t, conn, bufio.NewReader(conn), ` 376 `)
}

func dialIRC(t *testing.T, a net.Addr) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", a.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func status(t *testing.T, sock string) *daemon.Status {
	t.Helper()
	r, err := daemon.Request(context.Background(), sock, daemon.Hello{Op: daemon.OpStatus})
	if err != nil {
		t.Fatal(err)
	}
	return r.Status
}

func TestHostsDaemonWhenNoneRuns(t *testing.T) {
	e := newEnv(t)
	a := e.start("--listen", "127.0.0.1:0", "--host", "fake")
	register(t, dialIRC(t, a))
	ctx := context.Background()
	c, err := device.Open(ctx, func(ctx context.Context) (transport.Conn, error) {
		conn, _, err := daemon.Connect(ctx, e.deps.Socket, daemon.Hello{Target: daemon.Target{Kind: "tcp", Addr: "fake"}})
		return conn, err
	}, device.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st := status(t, e.deps.Socket)
	if len(st.Links) != 1 || st.Links[0].Clients != 2 || e.d.Dials() != 1 || st.Version != version {
		t.Fatalf("%+v, %d dials", st.Links, e.d.Dials())
	}
}

func TestUsesRunningDaemon(t *testing.T) {
	e := newEnv(t)
	ln, err := daemon.Listen(e.deps.Socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &daemon.Server{Version: "other", Dial: func(daemon.Target) (transport.Dialer, error) { return e.d.Dialer(), nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	e.deps.Dial = func(nodeconn.Flags) (transport.Dialer, error) {
		t.Error("ircmesh dialled the node itself")
		return e.d.Dialer(), nil
	}
	a := e.start("--listen", "127.0.0.1:0", "--host", "fake")
	register(t, dialIRC(t, a))
	if st := status(t, e.deps.Socket); len(st.Links) != 1 || st.Links[0].Clients != 1 || st.Version != "other" {
		t.Fatalf("%+v", st)
	}
}

func TestRefusesPublicListenWithoutPassword(t *testing.T) {
	e := newEnv(t)
	err := run(context.Background(), []string{"--listen", "0.0.0.0:0", "--host", "fake"}, e.deps)
	if err == nil || !strings.Contains(err.Error(), "listening on 0.0.0.0:0 needs a password: set MESHTASTIK_IRC_PASSWORD or --password-file") {
		t.Fatal(err)
	}
	if e.d.Dials() != 0 {
		t.Fatal("dialled the node")
	}
	e.vars["MESHTASTIK_IRC_PASSWORD"] = "pw"
	e.start("--listen", "0.0.0.0:0", "--host", "fake")
}

func TestLoopbackNeedsNoPassword(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		if needsPassword(addr) {
			t.Error(addr)
		}
	}
	for _, addr := range []string{":6667", "0.0.0.0:6667", "[::]:6667", "192.168.1.2:6667", "example.com:6667"} {
		if !needsPassword(addr) {
			t.Error(addr)
		}
	}
}

func TestPasswordFile(t *testing.T) {
	e := newEnv(t)
	pw := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(pw, []byte("secret\n"), 0o600)
	a := e.start("--listen", "127.0.0.1:0", "--host", "fake", "--password-file", pw)
	conn := dialIRC(t, a)
	fmt.Fprint(conn, "PASS secret\r\nNICK foo\r\nUSER u 0 * :r\r\n")
	expect(t, conn, bufio.NewReader(conn), ` 001 `)
}

func TestTLSFlagsTogether(t *testing.T) {
	e := newEnv(t)
	err := run(context.Background(), []string{"--tls-cert", "x.pem", "--host", "fake"}, e.deps)
	if err == nil || !strings.Contains(err.Error(), "--tls-cert and --tls-key go together") {
		t.Fatal(err)
	}
}

func TestOneNodeSelector(t *testing.T) {
	e := newEnv(t)
	err := run(context.Background(), []string{"--host", "a", "--port", "/dev/x"}, e.deps)
	if err == nil || !strings.Contains(err.Error(), "only one of") {
		t.Fatal(err)
	}
}

func writeCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return certFile, keyFile
}

func TestTLS(t *testing.T) {
	e := newEnv(t)
	cert, key := writeCert(t)
	a := e.start("--listen", "127.0.0.1:0", "--host", "fake", "--tls-cert", cert, "--tls-key", key)
	conn, err := tls.Dial("tcp", a.String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	register(t, conn)
}

func TestFirstConnectFailureExits(t *testing.T) {
	e := newEnv(t)
	e.d.RefuseDials = true
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), []string{"--listen", "127.0.0.1:0", "--host", "fake"}, e.deps)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("no error")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run hung after failing to connect")
	}
}

func TestHistoryFlag(t *testing.T) {
	for _, tc := range []struct {
		flag   string
		replay bool
	}{{"5", true}, {"0", false}} {
		t.Run(tc.flag, func(t *testing.T) {
			e := newEnv(t)
			a := e.start("--listen", "127.0.0.1:0", "--host", "fake", "--history", tc.flag)
			e.d.Inject(&pb.MeshPacket{From: 0x1234, To: device.Broadcast, Id: device.NewPacketID(),
				PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("kept?")}}})
			time.Sleep(200 * time.Millisecond)
			conn := dialIRC(t, a)
			fmt.Fprint(conn, "NICK foo\r\nUSER u 0 * :r\r\n")
			r := bufio.NewReader(conn)
			conn.SetReadDeadline(time.Now().Add(time.Second))
			got := false
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					break
				}
				got = got || strings.Contains(l, "PRIVMSG #LongFast") && strings.HasSuffix(strings.TrimSpace(l), "kept?")
			}
			if got != tc.replay {
				t.Fatalf("replayed: %v, want %v", got, tc.replay)
			}
		})
	}
}

func TestHistoryFlagNotNegative(t *testing.T) {
	e := newEnv(t)
	err := run(context.Background(), []string{"--host", "fake", "--history", "-1"}, e.deps)
	if err == nil || !strings.Contains(err.Error(), "--history") {
		t.Fatal(err)
	}
}
