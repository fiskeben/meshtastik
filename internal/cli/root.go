// Package cli defines the meshtastik command tree.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"sigs.k8s.io/yaml"

	"meshtastik/internal/daemon"
	"meshtastik/internal/device"
	"meshtastik/internal/transport"
)

// version is set at build time via -ldflags "-X meshtastik/internal/cli.version=...".
var version = "dev"

// ConnFlags holds the mutually exclusive connection selectors.
type ConnFlags struct {
	Port, Host, BLE string
}

// Deps carries the side-effecting dependencies of the commands.
type Deps struct {
	Dial    func(ConnFlags) (transport.Dialer, error)
	Stdout  io.Writer
	Stderr  io.Writer
	Confirm func(prompt string) bool
	// DeviceOptions, when set, adjusts the client options chosen by connect.
	DeviceOptions func(*device.Options)
	// DaemonSocket, when set, is the socket of the daemon that commands
	// connect through; empty connects directly with Dial.
	DaemonSocket string
	// StartDaemon starts this program in the background with args (such as
	// "daemon", "run") to serve sock.
	StartDaemon func(sock string, args ...string) error
}

// DefaultDeps returns dependencies wired to the real process environment.
// Commands go through the daemon unless MESHTASTIK_NO_DAEMON is set.
func DefaultDeps() Deps {
	d := Deps{
		Dial:        defaultDial,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Confirm:     terminalConfirm,
		StartDaemon: daemon.Spawn,
	}
	if os.Getenv("MESHTASTIK_NO_DAEMON") == "" {
		d.DaemonSocket = daemon.SocketPath()
	}
	return d
}

// chooseConn picks the transport and its target from the flags. Without any
// flag it auto-detects a single serial device.
func chooseConn(f ConnFlags, detect func() ([]transport.SerialCandidate, error)) (transport.Kind, string, error) {
	switch {
	case f.Port != "":
		return transport.KindSerial, f.Port, nil
	case f.Host != "":
		return transport.KindTCP, f.Host, nil
	case f.BLE != "":
		return transport.KindBLE, f.BLE, nil
	}
	cands, err := detect()
	if err != nil {
		return 0, "", fmt.Errorf("detect serial devices: %w", err)
	}
	switch len(cands) {
	case 0:
		return 0, "", errors.New("no Meshtastic device found; use --port, --host or --ble")
	case 1:
		return transport.KindSerial, cands[0].Path, nil
	}
	var b strings.Builder
	for _, c := range cands {
		fmt.Fprintf(&b, "\n  %s", c.Path)
		if c.Product != "" {
			fmt.Fprintf(&b, " (%s)", c.Product)
		}
	}
	return 0, "", fmt.Errorf("multiple serial devices found, choose one with --port:%s", b.String())
}

// defaultDial resolves the flags to a Dialer that re-opens the same target,
// so the client can reconnect after the node reboots.
func defaultDial(f ConnFlags) (transport.Dialer, error) {
	kind, target, err := chooseConn(f, transport.DetectSerial)
	if err != nil {
		return nil, err
	}
	switch kind {
	case transport.KindTCP:
		return func(ctx context.Context) (transport.Conn, error) { return transport.DialTCP(ctx, target, nil) }, nil
	case transport.KindSerial:
		return func(ctx context.Context) (transport.Conn, error) { return transport.OpenSerial(ctx, target, nil) }, nil
	}
	return func(ctx context.Context) (transport.Conn, error) { return transport.DialBLE(ctx, target, nil) }, nil
}

// defaultHandshakeTimeout is how long to wait for the node's config dump.
func defaultHandshakeTimeout(k transport.Kind) time.Duration {
	if k == transport.KindBLE {
		return 30 * time.Second
	}
	return 15 * time.Second
}

// kindOf is the transport kind the flags select (serial when auto-detecting).
func kindOf(f ConnFlags) transport.Kind {
	switch {
	case f.BLE != "":
		return transport.KindBLE
	case f.Host != "":
		return transport.KindTCP
	}
	return transport.KindSerial
}

// app is the state shared by all subcommands: dependencies and global flags.
type app struct {
	Deps
	conn     ConnFlags
	asJSON   bool
	yes      bool
	debug    bool
	noDaemon bool
	dest     string
	timeout  time.Duration

	versionNoted bool // the daemon version mismatch note was printed
}

// connect is the one place commands obtain a client. The caller must Close it.
func (a *app) connect(cmd *cobra.Command) (*device.Client, error) {
	return a.connectWith(cmd, false, nil)
}

// connectWith is connect with adjust, when set, applied to the client options
// before Deps.DeviceOptions. backlog asks the daemon for the packets that
// arrived while no listener was attached.
func (a *app) connectWith(cmd *cobra.Command, backlog bool, adjust func(*device.Options)) (*device.Client, error) {
	for _, name := range []string{"port", "host", "ble"} {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed && f.Value.String() == "" {
			return nil, fmt.Errorf("--%s must not be empty", name)
		}
	}
	opts := device.Options{HandshakeTimeout: a.timeout, MeshTimeout: a.timeout}
	if opts.HandshakeTimeout == 0 {
		opts.HandshakeTimeout = defaultHandshakeTimeout(kindOf(a.conn))
	}
	var dial transport.Dialer
	var err error
	if a.DaemonSocket != "" && !a.noDaemon {
		dial, err = a.daemonDialer(backlog, opts.HandshakeTimeout)
	} else {
		dial, err = a.Dial(a.conn)
	}
	if err != nil {
		return nil, err
	}
	if a.debug {
		opts.Log = a.Stderr
	}
	if a.dest != "" && isTerminal(a.Stderr) {
		opts.OnProgress = func(part string) { fmt.Fprintf(a.Stderr, "reading %s…\n", part) }
	}
	if adjust != nil {
		adjust(&opts)
	}
	if a.DeviceOptions != nil {
		a.DeviceOptions(&opts)
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return device.Open(ctx, dial, opts)
}

// protoOutput renders messages for display: proto field names, and zero
// values shown rather than omitted (region UNSET, enabled false).
var protoOutput = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

// printRaw writes the JSON raw as YAML, or as indented JSON when asJSON is set.
func printRaw(w io.Writer, raw []byte, asJSON bool) error {
	if asJSON {
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "", "  "); err != nil {
			return err
		}
		buf.WriteByte('\n')
		_, err := buf.WriteTo(w)
		return err
	}
	y, err := yaml.JSONToYAML(raw)
	if err != nil {
		return err
	}
	_, err = w.Write(y)
	return err
}

// printJSON writes v as indented JSON.
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// terminalConfirm asks on stderr and reads from stdin. It refuses (returns
// false) when stdin is not a terminal.
func terminalConfirm(prompt string) bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// NewRoot builds the root command.
func NewRoot(d Deps) *cobra.Command {
	a := &app{Deps: d}
	root := &cobra.Command{
		Use:           "meshtastik",
		Short:         "Administer a local Meshtastic node",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			for _, s := range []string{a.conn.Port, a.conn.Host, a.conn.BLE} {
				if s != "" {
					n++
				}
			}
			if n > 1 {
				return errors.New("only one of --port, --host, --ble may be given")
			}
			return a.checkDest(cmd)
		},
	}
	if d.Stdout != nil {
		root.SetOut(d.Stdout)
	}
	if d.Stderr != nil {
		root.SetErr(d.Stderr)
	}
	pf := root.PersistentFlags()
	pf.StringVar(&a.conn.Port, "port", "", "serial port path")
	pf.StringVar(&a.conn.Host, "host", "", "TCP host[:port] (default port 4403)")
	pf.StringVar(&a.conn.BLE, "ble", "", "BLE device name or address")
	pf.BoolVar(&a.asJSON, "json", false, "output JSON")
	pf.BoolVar(&a.yes, "yes", false, "skip confirmations")
	pf.BoolVar(&a.debug, "debug", false, "debug logging")
	pf.StringVar(&a.dest, "dest", "", "administer this node over the mesh instead of the connected one: a !hex id, number or name")
	pf.BoolVar(&a.noDaemon, "no-daemon", false, "connect to the node directly instead of through the background daemon (also MESHTASTIK_NO_DAEMON=1)")
	pf.DurationVar(&a.timeout, "timeout", 0, "timeout that bounds the handshake and each request, not the whole operation (0 = per-transport default; 30s per answer over the mesh)")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "meshtastik %s\n", version)
		},
	})
	root.AddCommand(newInfoCmd(a), newNodesCmd(a), newConfigCmd(a), newOwnerCmd(a),
		newRebootCmd(a), newShutdownCmd(a), newFactoryResetCmd(a), newChannelCmd(a), newBLECmd(a), newSendCmd(a), newListenCmd(a), newDaemonCmd(a),
		newTracerouteCmd(a), newRequestCmd(a), newAdminKeyCmd(a))
	return root
}
