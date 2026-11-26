// Package nodeconn chooses which node to connect to and how: the flags, the
// environment and the named devices pick a target, and DaemonDial reaches it
// through the daemon. Both meshtastik and ircmesh use it.
package nodeconn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"meshtastik/internal/daemon"
	"meshtastik/internal/devices"
	"meshtastik/internal/transport"
)

// Flags holds the mutually exclusive connection selectors.
type Flags struct {
	Port, Host, BLE string
}

// Choose picks the transport and its target from the flags. Without any
// flag it auto-detects a single serial device.
func Choose(f Flags, detect func() ([]transport.SerialCandidate, error)) (transport.Kind, string, error) {
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

// Dial resolves the flags to a Dialer that re-opens the same target, so the
// client can reconnect after the node reboots.
func Dial(f Flags) (transport.Dialer, error) {
	kind, target, err := Choose(f, transport.DetectSerial)
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

// HandshakeTimeout is how long to wait for the node's config dump.
func HandshakeTimeout(k transport.Kind) time.Duration {
	if k == transport.KindBLE {
		return 30 * time.Second
	}
	return 15 * time.Second
}

// KindOf is the transport kind the flags select (serial when auto-detecting).
func KindOf(f Flags) transport.Kind {
	switch {
	case f.BLE != "":
		return transport.KindBLE
	case f.Host != "":
		return transport.KindTCP
	}
	return transport.KindSerial
}

// FlagsFor is the connection flag that selects t.
func FlagsFor(t daemon.Target) Flags {
	switch t.Kind {
	case transport.KindTCP.String():
		return Flags{Host: t.Addr}
	case transport.KindBLE.String():
		return Flags{BLE: t.Addr}
	}
	return Flags{Port: t.Addr}
}

// ConnEnv are the environment variables that choose a connection.
var ConnEnv = []string{"MESHTASTIK_DEVICE", "MESHTASTIK_PORT", "MESHTASTIK_HOST", "MESHTASTIK_BLE"}

// FromCurrent is Resolved.From for the device saved with `meshtastik use`.
const FromCurrent = "the current device"

// Selection is what picks a connection: the flags, a named device, the
// environment and the devices file.
type Selection struct {
	Flags       Flags
	Device      string              // --device
	Getenv      func(string) string // nil reads no environment
	DevicesFile string
}

// Resolved is the connection chosen by Resolve.
type Resolved struct {
	Flags Flags
	Name  string // the named device, if one was chosen
	From  string // why, when not chosen by a flag: FromCurrent or a variable
}

func (r Resolved) String() string {
	t := devices.Target(r.Flags).String()
	if r.Name != "" {
		return r.Name + " (" + t + ")"
	}
	return t
}

// Wrap adds to a connection error which node was tried and why, when the
// command line did not say.
func (r Resolved) Wrap(err error) error {
	if r.From == "" {
		return err
	}
	hint := "pass --port, --host, --ble or --device to use another node"
	if r.From == FromCurrent {
		hint = "change it with 'meshtastik use', or " + hint
	}
	return fmt.Errorf("%s, %s: %w\n%s", r, r.From, err, hint)
}

// Resolve picks the connection: the flags, else the named device, else the
// environment, else the current device. With none of these the result is
// empty, and serial auto-detection picks the port.
func Resolve(s Selection) (Resolved, error) {
	if s.Flags != (Flags{}) {
		return Resolved{Flags: s.Flags}, nil
	}
	getenv := func(name string) string {
		if s.Getenv == nil {
			return ""
		}
		return s.Getenv(name)
	}
	named := func(name, from string) (Resolved, error) {
		f, err := devices.Load(s.DevicesFile)
		if err != nil {
			return Resolved{}, err
		}
		t, err := f.Lookup(name)
		if err != nil {
			if from != "" {
				err = fmt.Errorf("%s: %w", from, err)
			}
			return Resolved{}, err
		}
		return Resolved{Flags: Flags(t), Name: name, From: from}, nil
	}
	if s.Device != "" {
		return named(s.Device, "")
	}
	var set []string
	for _, v := range ConnEnv {
		if getenv(v) != "" {
			set = append(set, v)
		}
	}
	switch {
	case len(set) > 1:
		return Resolved{}, fmt.Errorf("only one of %s may be set; %s are", strings.Join(ConnEnv, ", "), strings.Join(set, " and "))
	case len(set) == 1:
		v := getenv(set[0])
		var c Flags
		switch set[0] {
		case "MESHTASTIK_DEVICE":
			return named(v, "from MESHTASTIK_DEVICE")
		case "MESHTASTIK_PORT":
			c.Port = v
		case "MESHTASTIK_HOST":
			c.Host = v
		case "MESHTASTIK_BLE":
			c.BLE = v
		}
		return Resolved{Flags: c, From: "from " + set[0]}, nil
	}
	f, err := devices.Load(s.DevicesFile)
	if err != nil {
		return Resolved{}, err
	}
	switch cur := f.Current; {
	case cur == nil:
		return Resolved{}, nil
	case cur.Device != "":
		return named(cur.Device, FromCurrent)
	}
	return Resolved{Flags: Flags(f.Current.Target), From: FromCurrent}, nil
}

// DaemonDial connects to nodes through the daemon on Socket.
type DaemonDial struct {
	Socket  string
	Version string // sent in the hello; compared with the daemon's
	// Start, when set, starts a daemon on sock when none answers and
	// returns once it does.
	Start func(ctx context.Context, sock string) error
	// OnVersion, when set, is called when the daemon runs another version.
	OnVersion func(daemonVersion string)
}

// Dialer connects to the node f selects through the daemon, starting it if
// none answers. Serial auto-detection runs here, so the daemon gets an
// explicit target.
func (d DaemonDial) Dialer(f Flags, backlog bool, timeout time.Duration) (transport.Dialer, error) {
	kind, addr, err := Choose(f, transport.DetectSerial)
	if err != nil {
		return nil, err
	}
	h := daemon.Hello{Version: d.Version, Target: daemon.Target{Kind: kind.String(), Addr: addr},
		Timeout: daemon.Duration(timeout), Backlog: backlog}
	return func(ctx context.Context) (transport.Conn, error) {
		conn, r, err := daemon.Connect(ctx, d.Socket, h)
		if errors.Is(err, daemon.ErrNotRunning) && d.Start != nil {
			if err := d.Start(ctx, d.Socket); err != nil {
				return nil, err
			}
			conn, r, err = daemon.Connect(ctx, d.Socket, h)
		}
		if err != nil {
			return nil, err
		}
		if r.Version != d.Version && d.OnVersion != nil {
			d.OnVersion(r.Version)
		}
		return conn, nil
	}, nil
}
