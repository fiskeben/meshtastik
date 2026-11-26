// Package cli defines the meshtastik command tree.
package cli

import (
	"bufio"
	"bytes"
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
	"meshtastik/internal/devices"
	"meshtastik/internal/nodeconn"
	"meshtastik/internal/transport"
)

// version is set at build time via -ldflags "-X meshtastik/internal/cli.version=...".
var version = "dev"

// ConnFlags holds the mutually exclusive connection selectors.
type ConnFlags = nodeconn.Flags

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
	// DevicesFile holds the named devices and the current device; empty
	// means there are none and none can be saved.
	DevicesFile string
	// Getenv reads the environment variables that choose a connection
	// (MESHTASTIK_DEVICE and the like); nil reads none.
	Getenv func(string) string
}

// DefaultDeps returns dependencies wired to the real process environment.
// Commands go through the daemon unless MESHTASTIK_NO_DAEMON is set.
func DefaultDeps() Deps {
	d := Deps{
		Dial:        nodeconn.Dial,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Confirm:     terminalConfirm,
		StartDaemon: daemon.Spawn,
		DevicesFile: devices.Path(),
		Getenv:      os.Getenv,
	}
	if os.Getenv("MESHTASTIK_NO_DAEMON") == "" {
		d.DaemonSocket = daemon.SocketPath()
	}
	return d
}

// app is the state shared by all subcommands: dependencies and global flags.
type app struct {
	Deps
	conn     ConnFlags
	device   string
	asJSON   bool
	yes      bool
	debug    bool
	noDaemon bool
	dest     string
	timeout  time.Duration

	versionNoted bool   // the daemon version mismatch note was printed
	acting       string // the node admin commands act on, for prompts
}

// connect is the one place commands obtain a client. The caller must Close it.
func (a *app) connect(cmd *cobra.Command) (*device.Client, error) {
	return a.connectWith(cmd, false, nil)
}

// connectWith is connect with adjust, when set, applied to the client options
// before Deps.DeviceOptions. backlog asks the daemon for the packets that
// arrived while no listener was attached.
func (a *app) connectWith(cmd *cobra.Command, backlog bool, adjust func(*device.Options)) (*device.Client, error) {
	r, err := a.resolveConn()
	if err != nil {
		return nil, err
	}
	if r.From != "" && changesNode(cmd) {
		fmt.Fprintf(a.Stderr, "using %s — %s\n", r, r.From)
	}
	opts := device.Options{HandshakeTimeout: a.timeout, MeshTimeout: a.timeout}
	if opts.HandshakeTimeout == 0 {
		opts.HandshakeTimeout = nodeconn.HandshakeTimeout(nodeconn.KindOf(r.Flags))
	}
	var dial transport.Dialer
	if a.DaemonSocket != "" && !a.noDaemon {
		dial, err = a.daemonDialer(r.Flags, backlog, opts.HandshakeTimeout)
	} else {
		dial, err = a.Dial(r.Flags)
	}
	if err != nil {
		return nil, r.Wrap(err)
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
	ctx := cmdContext(cmd)
	c, err := device.Open(ctx, dial, opts)
	if err != nil && ctx.Err() == nil {
		err = r.Wrap(err)
	}
	return c, err
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
			for _, name := range []string{"port", "host", "ble", "device"} {
				f := cmd.Flags().Lookup(name)
				if f == nil || !f.Changed {
					continue
				}
				if f.Value.String() == "" {
					return fmt.Errorf("--%s must not be empty", name)
				}
				n++
			}
			if n > 1 {
				return errors.New("only one of --port, --host, --ble, --device may be given")
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
	pf.StringVar(&a.device, "device", "", "connect to this named device (see 'meshtastik device'; also MESHTASTIK_DEVICE)")
	root.RegisterFlagCompletionFunc("device", a.completeDevices)
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
		newTracerouteCmd(a), newRequestCmd(a), newAdminKeyCmd(a), newUseCmd(a), newDeviceCmd(a))
	return root
}
