package cli

import (
	"bytes"
	"context"
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
	return runCLICtx(t, context.Background(), d, func(string) bool { return strings.HasPrefix(stdin, "y") }, args...)
}

// runCLICtx is runCLI with a context (as main passes one) and a Confirm func.
func runCLICtx(t *testing.T, ctx context.Context, d *fakedevice.Device, confirm func(string) bool, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(Deps{
		Dial:          func(ConnFlags) (transport.Dialer, error) { return d.Dialer(), nil },
		Stdout:        &out,
		Stderr:        &errOut,
		Confirm:       confirm,
		DeviceOptions: func(o *device.Options) { o.RebootGrace, o.ReconnectDelay = 50*time.Millisecond, 10*time.Millisecond },
	})
	root.SetArgs(args)
	err = root.ExecuteContext(ctx)
	return out.String(), errOut.String(), err
}
