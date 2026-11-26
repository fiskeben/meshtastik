// Command ircmesh serves a Meshtastic node as an IRC server: the node's
// channels are IRC channels, other nodes are IRC users, and you are the
// node. It reaches the node through the meshtastik daemon, and runs one
// itself when none is running.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"meshtastik/internal/daemon"
	"meshtastik/internal/device"
	"meshtastik/internal/devices"
	"meshtastik/internal/ircmesh"
	"meshtastik/internal/nodeconn"
	"meshtastik/internal/transport"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const (
	daemonIdleTimeout = 30 * time.Minute // of a daemon ircmesh runs, as meshtastik's
	daemonStartWait   = 5 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop() // a second signal kills the process
	}()
	d := defaultDeps()
	err := run(ctx, os.Args[1:], d)
	stop()
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(d.Stderr, "error:", err)
		os.Exit(1)
	}
}

// deps carries run's side-effecting dependencies.
type deps struct {
	Dial        func(nodeconn.Flags) (transport.Dialer, error) // a daemon ircmesh runs dials nodes with this
	Getenv      func(string) string
	Stderr      io.Writer
	Socket      string // the daemon's socket
	DevicesFile string
	StateDir    string           // where memberships are saved
	Ready       func(a net.Addr) // when set, called once IRC is being served on a
}

func defaultDeps() deps {
	d := deps{Dial: nodeconn.Dial, Getenv: os.Getenv, Stderr: os.Stderr, Socket: daemon.SocketPath(), DevicesFile: devices.Path()}
	if dir, err := os.UserConfigDir(); err == nil {
		d.StateDir = filepath.Join(dir, "ircmesh")
	}
	return d
}

// needsPassword reports whether clients from other machines can reach
// addr: anything but a loopback address or localhost.
func needsPassword(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true
	}
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func run(ctx context.Context, args []string, d deps) error {
	fs := flag.NewFlagSet("ircmesh", flag.ContinueOnError)
	fs.SetOutput(d.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: ircmesh [flags]\n\nServe a Meshtastic node as an IRC server. Connect an IRC client to --listen.\n\nFlags:")
		fs.PrintDefaults()
	}
	listen := fs.String("listen", "127.0.0.1:6667", "address to serve IRC on")
	tlsCert := fs.String("tls-cert", "", "serve TLS with this certificate (PEM; needs --tls-key)")
	tlsKey := fs.String("tls-key", "", "private key for --tls-cert (PEM)")
	passFile := fs.String("password-file", "", "file holding the password clients must send (also MESHTASTIK_IRC_PASSWORD)")
	active := fs.Duration("active", 24*time.Hour, "how long a node stays in a channel without being heard there")
	var sel nodeconn.Selection
	fs.StringVar(&sel.Device, "device", "", "named device to serve (see meshtastik device list)")
	fs.StringVar(&sel.Flags.Port, "port", "", "serial port of the node")
	fs.StringVar(&sel.Flags.Host, "host", "", "host name or address of the node (TCP)")
	fs.StringVar(&sel.Flags.BLE, "ble", "", "Bluetooth name or address of the node")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *showVersion {
		fmt.Fprintln(d.Stderr, "ircmesh", version)
		return nil
	}
	n := 0
	for _, v := range []string{sel.Device, sel.Flags.Port, sel.Flags.Host, sel.Flags.BLE} {
		if v != "" {
			n++
		}
	}
	if n > 1 {
		return errors.New("only one of --device, --port, --host and --ble may be given")
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("--tls-cert and --tls-key go together")
	}
	password := strings.TrimSpace(d.Getenv("MESHTASTIK_IRC_PASSWORD"))
	if *passFile != "" {
		b, err := os.ReadFile(*passFile)
		if err != nil {
			return err
		}
		password = strings.TrimSpace(string(b))
	}
	if password == "" && needsPassword(*listen) {
		return fmt.Errorf("listening on %s needs a password: set MESHTASTIK_IRC_PASSWORD or --password-file", *listen)
	}
	sel.Getenv, sel.DevicesFile = d.Getenv, d.DevicesFile
	r, err := nodeconn.Resolve(sel)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	var hosted sync.WaitGroup
	defer func() {
		cancel() // stops a daemon this process runs
		hosted.Wait()
	}()
	dd := nodeconn.DaemonDial{
		Socket:  d.Socket,
		Version: version,
		Start: func(_ context.Context, sock string) error {
			return hostDaemon(ctx, sock, d, &hosted)
		},
		OnVersion: func(v string) {
			fmt.Fprintf(d.Stderr, "note: the daemon runs meshtastik %s, this is ircmesh %s\n", v, version)
		},
	}
	timeout := nodeconn.HandshakeTimeout(nodeconn.KindOf(r.Flags))
	open := func(ctx context.Context, opts device.Options) (ircmesh.Mesh, error) {
		dial, err := dd.Dialer(r.Flags, true, timeout)
		if err != nil {
			return nil, r.Wrap(err)
		}
		opts.HandshakeTimeout = timeout
		c, err := device.Open(ctx, dial, opts)
		if err != nil {
			return nil, r.Wrap(err)
		}
		return c, nil
	}

	var ln net.Listener
	if *tlsCert != "" {
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			return err
		}
		ln, err = tls.Listen("tcp", *listen, &tls.Config{Certificates: []tls.Certificate{cert}})
		if err != nil {
			return err
		}
	} else if ln, err = net.Listen("tcp", *listen); err != nil {
		return err
	}
	node := r.String()
	if node == "" {
		node = "the serial port found"
	}
	fmt.Fprintf(d.Stderr, "ircmesh %s: connecting to %s\n", version, node)
	srv := ircmesh.New(ircmesh.Config{Version: version, Open: open, Password: password, Active: *active,
		StateDir: d.StateDir, Log: d.Stderr, OnReady: func() {
			fmt.Fprintf(d.Stderr, "connected to the node; IRC clients can connect to %s\n", ln.Addr())
			if d.Ready != nil {
				d.Ready(ln.Addr())
			}
		}})
	return srv.Run(ctx, ln)
}

// hostDaemon serves the daemon on sock in this process until ctx ends, so
// meshtastik commands can use it too. A daemon that another process
// started first is fine.
func hostDaemon(ctx context.Context, sock string, d deps, wg *sync.WaitGroup) error {
	ln, err := daemon.Listen(sock)
	if errors.Is(err, daemon.ErrAlreadyRunning) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sock, err)
	}
	srv := &daemon.Server{
		Version:     version, // same release as meshtastik, so its commands see no mismatch
		Dial:        func(t daemon.Target) (transport.Dialer, error) { return d.Dial(nodeconn.FlagsFor(t)) },
		IdleTimeout: daemonIdleTimeout,
		Log:         d.Stderr,
		Socket:      sock,
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Serve(ctx, ln); err != nil {
			fmt.Fprintln(d.Stderr, "daemon:", err)
		}
	}()
	return daemon.WaitRunning(ctx, sock, daemonStartWait)
}
