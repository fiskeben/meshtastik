package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/daemon"
	"meshtastik/internal/fakedevice"
	"meshtastik/internal/transport"
)

// TestMain lets a test re-run this binary as the real meshtastik process,
// with the arguments in MESHTASTIK_RUN_MAIN, or with its own when it was
// started without test flags (as the daemon is spawned).
func TestMain(m *testing.M) {
	if args, ok := os.LookupEnv("MESHTASTIK_RUN_MAIN"); ok {
		if len(os.Args) < 2 || strings.HasPrefix(os.Args[1], "-test.") {
			os.Args = append([]string{"meshtastik"}, strings.Fields(args)...)
		}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestInterruptCancelsTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs SIGINT")
	}
	// A node that accepts the connection but never answers the handshake.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// Accept returns once the kernel completes the TCP handshake, which can be
	// before the client's dial returns; wait for its first bytes so the
	// interrupt lands in the node handshake, not in the dial.
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				if _, err := c.Read(make([]byte, 1)); err == nil {
					accepted <- struct{}{}
				}
				io.Copy(io.Discard, c)
			}()
		}
	}()
	var stderr bytes.Buffer
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "MESHTASTIK_RUN_MAIN=--host "+ln.Addr().String()+" info", "MESHTASTIK_NO_DAEMON=1")
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("never connected")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("want exit code 1 after interrupt, got %v (stderr %q)", err, stderr.String())
	}
	if !strings.HasPrefix(stderr.String(), "error: ") || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

// serveFake serves d as a node's TCP API on a local port.
func serveFake(t *testing.T, d *fakedevice.Device) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			fc, err := d.Dialer()(context.Background())
			if err != nil {
				nc.Close()
				continue
			}
			go func() { // node to TCP
				defer nc.Close()
				for {
					m, err := fc.Recv(context.Background())
					if err != nil {
						return
					}
					b, _ := proto.Marshal(m)
					if transport.WriteFrame(nc, b) != nil {
						return
					}
				}
			}()
			go func() { // TCP to node
				defer fc.Close()
				fr := transport.NewFrameReader(nc, nil)
				for {
					p, err := fr.Next()
					if err != nil {
						return
					}
					var m pb.ToRadio
					if proto.Unmarshal(p, &m) != nil || fc.Send(context.Background(), &m) != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestCommandsStartAndShareTheDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets in temp dirs")
	}
	dir, err := os.MkdirTemp("", "mte2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		daemon.Request(ctx, sock, daemon.Hello{Op: daemon.OpStop})
	})
	d := fakedevice.New()
	addr := serveFake(t, d)
	run := func(args string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "MESHTASTIK_RUN_MAIN="+args, "MESHTASTIK_SOCKET="+sock)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v\nstdout %s\nstderr %s\nlog %s", args, err, stdout.String(), stderr.String(), readLog(sock))
		}
		return stdout.String()
	}
	if out := run("daemon status"); !strings.Contains(out, "not running") {
		t.Fatal(out)
	}
	if out := run("--host " + addr + " info"); !strings.Contains(out, "Test Node") {
		t.Fatal(out)
	}
	if out := run("--host " + addr + " nodes"); !strings.Contains(out, "Test Node") {
		t.Fatal(out)
	}
	if d.Dials() != 1 {
		t.Fatalf("node dialled %d times, want once (log %s)", d.Dials(), readLog(sock))
	}
	if out := run("daemon status"); !strings.Contains(out, "tcp:"+addr) || !strings.Contains(out, "connected") {
		t.Fatal(out)
	}
	if out := run("daemon stop"); !strings.Contains(out, "daemon stopped") {
		t.Fatal(out)
	}
	if out := run("--no-daemon --host " + addr + " info"); !strings.Contains(out, "Test Node") {
		t.Fatal(out)
	}
	if out := run("daemon status"); !strings.Contains(out, "not running") {
		t.Fatal("--no-daemon started the daemon:", out)
	}
}

func readLog(sock string) string {
	b, _ := os.ReadFile(daemon.LogPath(sock))
	return string(b)
}
