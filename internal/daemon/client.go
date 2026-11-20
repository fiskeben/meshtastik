package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"meshtastik/internal/transport"
)

// ErrNotRunning is returned (wrapped) when no daemon answers on the socket.
var ErrNotRunning = errors.New("daemon not running")

// ErrAlreadyRunning is returned by Listen when a live daemon owns the socket.
var ErrAlreadyRunning = errors.New("daemon already running")

func dial(ctx context.Context, sock string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sock)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	return conn, nil
}

// exchange sends h on conn and reads the reply. It gives up when ctx ends.
func exchange(ctx context.Context, conn net.Conn, br *bufio.Reader, h Hello) (Reply, error) {
	h.Proto = Proto
	b, err := json.Marshal(h)
	if err != nil {
		return Reply{}, err
	}
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return Reply{}, ctxErr(ctx, fmt.Errorf("talk to daemon: %w", err))
	}
	line, err := br.ReadBytes('\n')
	if err != nil {
		return Reply{}, ctxErr(ctx, fmt.Errorf("talk to daemon: %w", err))
	}
	if !stop() {
		return Reply{}, ctx.Err()
	}
	var r Reply
	if err := json.Unmarshal(line, &r); err != nil {
		return Reply{}, fmt.Errorf("bad reply from daemon: %w", err)
	}
	if !r.OK {
		return r, errors.New(r.Error)
	}
	return r, nil
}

func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Request sends a status or stop hello and returns the reply.
func Request(ctx context.Context, sock string, h Hello) (Reply, error) {
	conn, err := dial(ctx, sock)
	if err != nil {
		return Reply{}, err
	}
	defer conn.Close()
	return exchange(ctx, conn, bufio.NewReader(conn), h)
}

// Connect attaches to the daemon's connection to h.Target (h.Op is set to
// connect) and returns it as a transport.Conn of the node's transport kind.
func Connect(ctx context.Context, sock string, h Hello) (transport.Conn, Reply, error) {
	h.Op = OpConnect
	conn, err := dial(ctx, sock)
	if err != nil {
		return nil, Reply{}, err
	}
	br := bufio.NewReader(conn)
	r, err := exchange(ctx, conn, br, h)
	if err != nil {
		conn.Close()
		return nil, r, err
	}
	kind, err := Target{Kind: r.Kind}.TransportKind()
	if err != nil {
		conn.Close()
		return nil, r, fmt.Errorf("daemon: %w", err)
	}
	return transport.NewStreamConn(readerConn{br, conn}, kind, nil), r, nil
}

// readerConn reads through the buffered reader that holds what followed the
// reply line.
type readerConn struct {
	r io.Reader
	net.Conn
}

func (c readerConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// Listen binds the daemon socket. A socket file left by a daemon that died
// is replaced; one a live daemon answers on gives ErrAlreadyRunning.
func Listen(sock string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if conn, derr := dial(ctx, sock); derr == nil {
			conn.Close()
			return nil, ErrAlreadyRunning
		}
		if rerr := os.Remove(sock); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return nil, err
		}
		if ln, err = net.Listen("unix", sock); err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Spawn starts this executable with args as a detached background process
// that uses socket sock, its output appended to the daemon log. The process
// gets MESHTASTIK_NO_DAEMON=1, so it never starts a daemon itself.
func Spawn(sock string, args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(LogPath(sock), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "MESHTASTIK_SOCKET="+sock, "MESHTASTIK_NO_DAEMON=1")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	return cmd.Process.Release()
}

// WaitRunning dials sock until a daemon answers or timeout passes.
func WaitRunning(ctx context.Context, sock string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		r, err := Request(ctx, sock, Hello{Op: OpStatus})
		if err == nil || !errors.Is(err, ErrNotRunning) && r.Proto != 0 {
			return nil // answering, even if refusing our protocol
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon did not start within %s (see %s): %w", timeout, LogPath(sock), err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
