package cli

import (
	"strings"
	"testing"

	"meshtastik/internal/fakedevice"
)

func TestOwnerSet(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "owner", "set", "Base Station", "--short", "BASE"); err != nil || d.Owner.ShortName != "BASE" {
		t.Fatal(err)
	}
}

func TestOwnerSetByteLimits(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "owner", "set", "Fox", "--short", "🦊"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, d, "", "owner", "set", "Fox", "--short", "🦊🦊"); err == nil || !strings.Contains(err.Error(), "4 bytes") {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, d, "", "owner", "set", strings.Repeat("x", 40)); err == nil || !strings.Contains(err.Error(), "39 bytes") {
		t.Fatal(err)
	}
}

func TestOwnerSetKeepsShort(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "owner", "set", "Renamed"); err != nil || d.Owner.ShortName != "TEST" || d.Owner.LongName != "Renamed" {
		t.Fatal(err, d.Owner)
	}
}

func TestReboot(t *testing.T) {
	d := fakedevice.New()
	out, _, err := runCLI(t, d, "", "reboot")
	if err != nil || out != "Reboot requested (node restarts in ~5 seconds).\n" || d.LastAdmin().GetRebootSeconds() != 5 {
		t.Fatal(out, err)
	}
}

func TestShutdownConfirm(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "n\n", "shutdown"); err == nil || d.LastAdmin().GetShutdownSeconds() != 0 {
		t.Fatal("shutdown without confirm")
	}
	out, _, err := runCLI(t, d, "", "--yes", "shutdown")
	if err != nil || out != "Shutdown requested (node powers off in ~5 seconds).\n" || d.LastAdmin().GetShutdownSeconds() != 5 {
		t.Fatal(out, err)
	}
}

func TestFactoryResetConfirm(t *testing.T) {
	d := fakedevice.New()
	runCLI(t, d, "n\n", "factory-reset")
	if d.LastAdmin().GetFactoryResetConfig() != 0 {
		t.Fatal("reset without confirm")
	}
	runCLI(t, d, "y\n", "factory-reset", "--full")
	if d.LastAdmin().GetFactoryResetDevice() != 1 {
		t.Fatal("full reset not sent")
	}
}

func TestOwnerSetKeepsOtherFields(t *testing.T) {
	d := fakedevice.New()
	d.Owner.IsLicensed = true
	if _, _, err := runCLI(t, d, "", "owner", "set", "Renamed"); err != nil || !d.Owner.IsLicensed {
		t.Fatal(err, d.Owner)
	}
}

func TestOwnerSetRejectsEmpty(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "owner", "set", ""); err == nil {
		t.Fatal("empty long name accepted")
	}
	if _, _, err := runCLI(t, d, "", "owner", "set", "X", "--short", ""); err == nil {
		t.Fatal("empty short name accepted")
	}
}
