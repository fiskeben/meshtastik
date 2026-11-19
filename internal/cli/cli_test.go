package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
	"meshtastik/internal/transport"
)

// runCLI executes the command tree against a fake device. stdin only feeds
// Deps.Confirm: a prompt is confirmed when stdin starts with "y".
func runCLI(t *testing.T, d *fakedevice.Device, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(Deps{
		Dial:          func(ConnFlags) (transport.Dialer, error) { return d.Dialer(), nil },
		Stdout:        &out,
		Stderr:        &errOut,
		Confirm:       func(string) bool { return strings.HasPrefix(stdin, "y") },
		DeviceOptions: func(o *device.Options) { o.RebootGrace = 50 * time.Millisecond },
	})
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}
