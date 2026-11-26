package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
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
