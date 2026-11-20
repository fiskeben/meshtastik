package devices

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil || f.Current != nil || len(f.Devices) != 0 {
		t.Fatal(f, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "devices.yaml")
	in := &File{
		Current: &Current{Device: "home"},
		Devices: map[string]Target{"home": {Host: "10.0.0.2"}, "hill": {BLE: "Hill_1234"}},
	}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "device: home") || !strings.Contains(string(b), "host: 10.0.0.2") {
		t.Fatalf("file:\n%s", b)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Current.Device != "home" || out.Devices["hill"].BLE != "Hill_1234" || out.Devices["home"].Host != "10.0.0.2" {
		t.Fatalf("%+v", out)
	}

	in.Current = &Current{Target: Target{Port: "/dev/cu.a"}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	if out, err = Load(path); err != nil || out.Current.Port != "/dev/cu.a" || out.Current.Device != "" {
		t.Fatalf("%+v %v", out.Current, err)
	}
}

func TestLoadRefusesBadFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"unknown": "devices:\n  home:\n    hots: 10.0.0.2\n",
		"both":    "current:\n  device: home\n  host: 10.0.0.2\n",
	} {
		path := filepath.Join(dir, name+".yaml")
		os.WriteFile(path, []byte(body), 0o600)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLookup(t *testing.T) {
	f := &File{}
	if _, err := f.Lookup("home"); err == nil || !strings.Contains(err.Error(), "device add") {
		t.Fatal(err)
	}
	f.Devices = map[string]Target{"home": {Host: "h"}, "attic": {Port: "p"}}
	if _, err := f.Lookup("x"); err == nil || !strings.Contains(err.Error(), "[attic home]") {
		t.Fatal(err)
	}
	if tg, err := f.Lookup("home"); err != nil || tg.Host != "h" {
		t.Fatal(tg, err)
	}
}

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"home", "Hill-2", "a.b_c"} {
		if err := CheckName(ok); err != nil {
			t.Error(ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "a b", "tcp:x", "a/b"} {
		if CheckName(bad) == nil {
			t.Error(bad)
		}
	}
}

func TestTarget(t *testing.T) {
	if (Target{Host: "h"}).String() != "tcp:h" || (Target{Port: "p"}).String() != "serial:p" || (Target{BLE: "b"}).String() != "ble:b" {
		t.Fatal()
	}
	if (Target{}).Validate() == nil || (Target{Host: "h", BLE: "b"}).Validate() == nil || (Target{Host: "h"}).Validate() != nil {
		t.Fatal()
	}
}
