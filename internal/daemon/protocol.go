// Package daemon holds node connections in a background process and lets
// commands use them over a local socket. The socket carries the node's own
// framed ToRadio/FromRadio stream after a one-line JSON hello, so a
// device.Client works over it unchanged.
package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"meshtastik/internal/transport"
)

// Proto is the wire protocol version; a daemon refuses other versions.
const Proto = 1

// Hello ops.
const (
	OpConnect = "connect"
	OpStatus  = "status"
	OpStop    = "stop"
)

// Target names a node connection: the transport and its --port, --host or
// --ble value.
type Target struct {
	Kind string `json:"kind"` // serial, tcp or ble
	Addr string `json:"addr"`
}

func (t Target) String() string { return t.Kind + ":" + t.Addr }

// TransportKind is the transport.Kind of t.Kind.
func (t Target) TransportKind() (transport.Kind, error) {
	for _, k := range []transport.Kind{transport.KindSerial, transport.KindTCP, transport.KindBLE} {
		if k.String() == t.Kind {
			return k, nil
		}
	}
	return 0, fmt.Errorf("unknown transport %q", t.Kind)
}

// Hello is the first line a client sends.
type Hello struct {
	Proto   int    `json:"proto"`
	Version string `json:"version"`
	Op      string `json:"op"`
	Target  Target `json:"target,omitzero"`
	// Timeout bounds the handshake when the daemon opens the connection
	// (0: the daemon's per-transport default).
	Timeout Duration `json:"timeout,omitzero"`
	// Backlog asks for the packets that arrived while no backlog client
	// was attached, as listen does.
	Backlog bool `json:"backlog,omitempty"`
}

// Reply is the daemon's one-line answer to a Hello.
type Reply struct {
	OK      bool    `json:"ok,omitempty"`
	Error   string  `json:"error,omitempty"`
	Proto   int     `json:"proto"`
	Version string  `json:"version"`
	Kind    string  `json:"kind,omitempty"` // transport of the node connection (connect)
	Status  *Status `json:"status,omitempty"`
}

// Status describes a running daemon.
type Status struct {
	PID         int          `json:"pid"`
	Version     string       `json:"version"`
	Started     time.Time    `json:"started"`
	Socket      string       `json:"socket"`
	Log         string       `json:"log"`
	IdleTimeout Duration     `json:"idle_timeout"`
	Links       []LinkStatus `json:"links"`
}

// LinkStatus describes one node connection held by the daemon.
type LinkStatus struct {
	Target   Target    `json:"target"`
	State    string    `json:"state"` // connecting, connected, reconnecting
	Node     string    `json:"node,omitempty"`
	NodeID   string    `json:"node_id,omitempty"`
	Clients  int       `json:"clients"`
	Since    time.Time `json:"since"`
	Backlog  int       `json:"backlog"`
	LastUsed time.Time `json:"last_used"`
}

// Duration is a time.Duration that encodes as a string such as "15s".
type Duration time.Duration

func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	*d = Duration(v)
	return err
}

// SocketPath is where the daemon listens: $MESHTASTIK_SOCKET, else
// $XDG_RUNTIME_DIR/meshtastik.sock, else meshtastik-<uid>.sock in the
// temporary directory.
func SocketPath() string {
	if p := os.Getenv("MESHTASTIK_SOCKET"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "meshtastik.sock")
	}
	user := fmt.Sprint(os.Getuid())
	if os.Getuid() < 0 { // Windows
		user = os.Getenv("USERNAME")
	}
	return filepath.Join(os.TempDir(), "meshtastik-"+user+".sock")
}

// LogPath is the daemon's log file for socket path sock.
func LogPath(sock string) string { return strings.TrimSuffix(sock, ".sock") + ".log" }
