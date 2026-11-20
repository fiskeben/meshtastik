package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"meshtastik/internal/device"
	"meshtastik/internal/devices"
	"meshtastik/internal/fakedevice"
	"meshtastik/internal/transport"
)

// devEnv runs commands with a devices file, an environment, and a record of
// what each command dialled.
type devEnv struct {
	t       *testing.T
	d       *fakedevice.Device
	file    string
	env     map[string]string
	dialed  []ConnFlags
	prompts []string
	dialErr error
}

func newDevEnv(t *testing.T) *devEnv {
	return &devEnv{t: t, d: fakedevice.New(), file: filepath.Join(t.TempDir(), "devices.yaml"), env: map[string]string{}}
}

func (e *devEnv) run(args ...string) (stdout, stderr string, err error) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	root := NewRoot(Deps{
		Dial: func(f ConnFlags) (transport.Dialer, error) {
			e.dialed = append(e.dialed, f)
			if e.dialErr != nil {
				return nil, e.dialErr
			}
			return e.d.Dialer(), nil
		},
		Stdout:        &out,
		Stderr:        &errOut,
		Confirm:       func(p string) bool { e.prompts = append(e.prompts, p); return false },
		DeviceOptions: func(o *device.Options) { o.RebootGrace, o.ReconnectDelay = 50*time.Millisecond, 10*time.Millisecond },
		DevicesFile:   e.file,
		Getenv:        func(k string) string { return e.env[k] },
	})
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func (e *devEnv) mustRun(args ...string) (stdout, stderr string) {
	e.t.Helper()
	out, errOut, err := e.run(args...)
	if err != nil {
		e.t.Fatalf("%v: %v\n%s", args, err, errOut)
	}
	return out, errOut
}

// lastDial is the connection the last command dialled.
func (e *devEnv) lastDial() ConnFlags {
	e.t.Helper()
	if len(e.dialed) == 0 {
		e.t.Fatal("nothing dialled")
	}
	return e.dialed[len(e.dialed)-1]
}

func TestDeviceAddListRemove(t *testing.T) {
	e := newDevEnv(t)
	if out, _ := e.mustRun("device", "list"); !strings.Contains(out, "No named devices") {
		t.Fatal(out)
	}
	if out, _ := e.mustRun("device", "add", "home", "--host", "10.0.0.2"); out != "home is tcp:10.0.0.2.\n" {
		t.Fatal(out)
	}
	e.mustRun("device", "add", "hill", "--ble", "Hill_1234")
	if out, _ := e.mustRun("device", "add", "home", "--host", "10.0.0.3"); out != "home is now tcp:10.0.0.3 (was tcp:10.0.0.2).\n" {
		t.Fatal(out)
	}
	e.mustRun("use", "home")
	out, _ := e.mustRun("device", "list")
	if !strings.Contains(out, "*  home  tcp:10.0.0.3") || !strings.Contains(out, "   hill  ble:Hill_1234") {
		t.Fatalf("%q", out)
	}
	out, _ = e.mustRun("device", "list", "--json")
	var rows []deviceJSON
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 2 || rows[1] != (deviceJSON{"home", "tcp:10.0.0.3", true}) {
		t.Fatal(out, err)
	}
	if out, _ = e.mustRun("device", "remove", "home"); !strings.Contains(out, "It was the current device") {
		t.Fatal(out)
	}
	f, _ := devices.Load(e.file)
	if f.Current != nil || len(f.Devices) != 1 {
		t.Fatalf("%+v", f)
	}
	if _, _, err := e.run("device", "remove", "home"); err == nil || !strings.Contains(err.Error(), "[hill]") {
		t.Fatal(err)
	}
	if len(e.dialed) != 0 {
		t.Fatal("device commands dialled", e.dialed)
	}
}

func TestDeviceAddRefuses(t *testing.T) {
	e := newDevEnv(t)
	for _, args := range [][]string{
		{"device", "add", "home"},
		{"device", "add", "-x", "--host", "h"},
		{"device", "add", "a b", "--host", "h"},
		{"device", "add", "home", "--host", "h", "--ble", "b"},
		{"device", "add", "home", "--host", ""},
	} {
		if _, _, err := e.run(args...); err == nil {
			t.Error(args, "accepted")
		}
	}
}

func TestUseNamedDevice(t *testing.T) {
	e := newDevEnv(t)
	e.mustRun("device", "add", "home", "--host", "10.0.0.2")
	if out, _ := e.mustRun("use", "home"); out != "Commands now connect to home (tcp:10.0.0.2).\n" {
		t.Fatal(out)
	}
	if out, _ := e.mustRun("use"); out != "home (tcp:10.0.0.2) — the current device\n" {
		t.Fatal(out)
	}

	// Reading commands use it quietly; changing ones say which node.
	_, errOut := e.mustRun("info")
	if e.lastDial() != (ConnFlags{Host: "10.0.0.2"}) || errOut != "" {
		t.Fatal(e.lastDial(), errOut)
	}
	_, errOut = e.mustRun("owner", "set", "Renamed")
	if errOut != "using home (tcp:10.0.0.2) — the current device\n" || e.d.Owner.LongName != "Renamed" {
		t.Fatal(errOut)
	}

	if _, _, err := e.run("use", "attic"); err == nil || !strings.Contains(err.Error(), `no device named "attic"`) {
		t.Fatal(err)
	}
}

func TestUseUnnamedTargetAndClear(t *testing.T) {
	e := newDevEnv(t)
	if out, _ := e.mustRun("use"); !strings.Contains(out, "No current device") {
		t.Fatal(out)
	}
	if out, _ := e.mustRun("use", "--port", "/dev/cu.a"); out != "Commands now connect to serial:/dev/cu.a.\n" {
		t.Fatal(out)
	}
	e.mustRun("nodes")
	if e.lastDial() != (ConnFlags{Port: "/dev/cu.a"}) {
		t.Fatal(e.lastDial())
	}
	out, _ := e.mustRun("use", "--json")
	if strings.TrimSpace(out) != `{
  "target": "serial:/dev/cu.a",
  "from": "the current device"
}` {
		t.Fatal(out)
	}
	if out, _ := e.mustRun("device", "list"); !strings.Contains(out, "The current device is serial:/dev/cu.a, which has no name.") {
		t.Fatal(out)
	}
	e.mustRun("use", "--clear")
	e.mustRun("nodes")
	if e.lastDial() != (ConnFlags{}) {
		t.Fatal("cleared device still used:", e.lastDial())
	}
	for _, args := range [][]string{{"use", "home", "--host", "h"}, {"use", "--clear", "--host", "h"}, {"use", "home", "--device", "home"}} {
		if _, _, err := e.run(args...); err == nil {
			t.Error(args, "accepted")
		}
	}
}

func TestConnPrecedence(t *testing.T) {
	e := newDevEnv(t)
	e.mustRun("device", "add", "home", "--host", "10.0.0.2")
	e.mustRun("device", "add", "attic", "--port", "/dev/cu.attic")
	e.mustRun("use", "home")

	e.env["MESHTASTIK_DEVICE"] = "attic"
	_, errOut := e.mustRun("send", "hi")
	if e.lastDial() != (ConnFlags{Port: "/dev/cu.attic"}) || errOut != "using attic (serial:/dev/cu.attic) — from MESHTASTIK_DEVICE\n" {
		t.Fatal(e.lastDial(), errOut)
	}
	if out, _ := e.mustRun("use"); out != "attic (serial:/dev/cu.attic) — from MESHTASTIK_DEVICE\n" {
		t.Fatal(out)
	}
	if _, errOut := e.mustRun("use", "home"); !strings.Contains(errOut, "MESHTASTIK_DEVICE is set") {
		t.Fatal(errOut)
	}

	e.env["MESHTASTIK_HOST"] = "10.9.9.9"
	if _, _, err := e.run("nodes"); err == nil || !strings.Contains(err.Error(), "MESHTASTIK_DEVICE and MESHTASTIK_HOST are") {
		t.Fatal(err)
	}
	delete(e.env, "MESHTASTIK_DEVICE")
	e.mustRun("nodes")
	if e.lastDial() != (ConnFlags{Host: "10.9.9.9"}) {
		t.Fatal(e.lastDial())
	}

	// Flags win over everything, and are not announced.
	_, errOut = e.mustRun("--device", "attic", "send", "hi")
	if e.lastDial() != (ConnFlags{Port: "/dev/cu.attic"}) || errOut != "" {
		t.Fatal(e.lastDial(), errOut)
	}
	e.mustRun("--ble", "Node_1", "nodes")
	if e.lastDial() != (ConnFlags{BLE: "Node_1"}) {
		t.Fatal(e.lastDial())
	}
	if _, _, err := e.run("--device", "attic", "--host", "h", "nodes"); err == nil || !strings.Contains(err.Error(), "only one of") {
		t.Fatal(err)
	}

	e.env = map[string]string{"MESHTASTIK_DEVICE": "nope"}
	if _, _, err := e.run("nodes"); err == nil || !strings.Contains(err.Error(), `from MESHTASTIK_DEVICE: no device named "nope"`) {
		t.Fatal(err)
	}
}

func TestCurrentDeviceUnreachableSaysWhy(t *testing.T) {
	e := newDevEnv(t)
	e.mustRun("device", "add", "home", "--host", "10.0.0.2")
	e.mustRun("use", "home")
	e.dialErr = errors.New("connection refused")
	_, _, err := e.run("nodes")
	if err == nil || !strings.Contains(err.Error(), "home (tcp:10.0.0.2), the current device: connection refused") ||
		!strings.Contains(err.Error(), "meshtastik use") {
		t.Fatal(err)
	}
	if len(e.dialed) != 1 {
		t.Fatal("fell back to another connection:", e.dialed)
	}
	if _, _, err = e.run("--host", "10.0.0.5", "nodes"); err == nil || err.Error() != "connection refused" {
		t.Fatal(err)
	}
}

func TestConfirmNamesTheNode(t *testing.T) {
	e := newDevEnv(t)
	if _, _, err := e.run("factory-reset"); err == nil {
		t.Fatal("factory reset without confirmation")
	}
	if len(e.prompts) != 1 || !strings.HasPrefix(e.prompts[0], "!1234abcd (Test Node): Factory reset the node?") {
		t.Fatal(e.prompts)
	}
}

func TestChangesNode(t *testing.T) {
	var set string
	cmd := changes(&cobra.Command{Use: "url", RunE: func(*cobra.Command, []string) error { return nil }}, "set")
	cmd.Flags().StringVar(&set, "set", "", "")
	if changesNode(cmd) {
		t.Fatal("changes without --set")
	}
	cmd.Flags().Set("set", "x")
	if !changesNode(cmd) || !changesNode(changes(&cobra.Command{}, "yes")) || changesNode(&cobra.Command{}) {
		t.Fatal()
	}
}
