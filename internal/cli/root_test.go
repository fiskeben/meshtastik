package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"meshtastik/internal/transport"
)

func TestConnFlagsMutuallyExclusive(t *testing.T) {
	var out, errOut bytes.Buffer
	root := NewRoot(Deps{Stdout: &out, Stderr: &errOut})
	root.SetArgs([]string{"--port", "/dev/x", "--host", "1.2.3.4", "version"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "only one of --port, --host, --ble") {
		t.Fatalf("want exclusivity error, got %v", err)
	}
}

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	root := NewRoot(Deps{Stdout: &out, Stderr: io.Discard})
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "meshtastik ") {
		t.Fatalf("got %q", out.String())
	}
}

func TestChooseConn(t *testing.T) {
	one := func() ([]transport.SerialCandidate, error) {
		return []transport.SerialCandidate{{Path: "/dev/cu.a"}}, nil
	}
	two := func() ([]transport.SerialCandidate, error) {
		return []transport.SerialCandidate{{Path: "/dev/cu.a"}, {Path: "/dev/cu.b"}}, nil
	}
	none := func() ([]transport.SerialCandidate, error) { return nil, nil }
	if k, p, err := chooseConn(ConnFlags{}, one); err != nil || k != transport.KindSerial || p != "/dev/cu.a" {
		t.Fatal(k, p, err)
	}
	if k, p, _ := chooseConn(ConnFlags{Host: "10.0.0.2"}, none); k != transport.KindTCP || p != "10.0.0.2" {
		t.Fatal(k, p)
	}
	if _, _, err := chooseConn(ConnFlags{}, none); err == nil || !strings.Contains(err.Error(), "no Meshtastic device found") {
		t.Fatal(err)
	}
	if _, _, err := chooseConn(ConnFlags{}, two); err == nil || !strings.Contains(err.Error(), "/dev/cu.b") {
		t.Fatal(err)
	}
}

func TestDefaultHandshakeTimeoutPerKind(t *testing.T) {
	if defaultHandshakeTimeout(transport.KindBLE) != 30*time.Second || defaultHandshakeTimeout(transport.KindSerial) != 15*time.Second {
		t.Fatal()
	}
}

func TestTimeoutHelpSaysWhatItBounds(t *testing.T) {
	var out bytes.Buffer
	root := NewRoot(Deps{Stdout: &out, Stderr: io.Discard})
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "bounds the handshake and each request, not the whole operation") {
		t.Fatal(out.String())
	}
}
