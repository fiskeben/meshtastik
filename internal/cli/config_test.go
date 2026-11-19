package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"meshtastik/internal/fakedevice"
	"meshtastik/internal/transport"
)

func TestConfigGet(t *testing.T) {
	d := fakedevice.New()
	if out, _, _ := runCLI(t, d, "", "config", "get", "lora.region"); out != "EU_868\n" {
		t.Fatalf("%q", out)
	}
	if out, _, _ := runCLI(t, d, "", "config", "get", "lora"); !strings.Contains(out, "region: EU_868") {
		t.Fatal(out)
	}
	out, _, _ := runCLI(t, d, "", "config", "get", "--json")
	var m map[string]any
	if json.Unmarshal([]byte(out), &m) != nil || m["config"] == nil || m["module_config"] == nil {
		t.Fatal(out)
	}
}

func TestConfigSetMultiplePairs(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "config", "set", "lora.hop_limit", "5", "lora.tx_power", "20"); err != nil {
		t.Fatal(err)
	}
	if d.Config.Lora.HopLimit != 5 || d.Config.Lora.TxPower != 20 || fakedevice.CountBegin(d.Received()) != 1 {
		t.Fatal()
	}
}

func TestConfigSetOddArgs(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "config", "set", "lora.hop_limit"); err == nil {
		t.Fatal()
	}
}

func TestConfigSetLockoutNeedsConfirm(t *testing.T) {
	d := fakedevice.New()
	d.Kind = transport.KindBLE
	if _, _, err := runCLI(t, d, "n\n", "config", "set", "bluetooth.enabled", "false"); err == nil || !d.Config.Bluetooth.Enabled {
		t.Fatal("applied without confirm")
	}
	if _, _, err := runCLI(t, d, "", "--yes", "config", "set", "bluetooth.enabled", "false"); err != nil || d.Config.Bluetooth.Enabled {
		t.Fatal(err)
	}
}

func TestConfigSetMismatchFails(t *testing.T) {
	d := fakedevice.New()
	d.IgnoreFields = []string{"lora.hop_limit"}
	_, _, err := runCLI(t, d, "", "config", "set", "lora.hop_limit", "5")
	if err == nil || !strings.Contains(err.Error(), "lora.hop_limit") {
		t.Fatal(err)
	}
}

func TestConfigGetUsesProtoNames(t *testing.T) {
	d := fakedevice.New()
	if out, _, _ := runCLI(t, d, "", "config", "get", "lora", "--json"); !strings.Contains(out, `"hop_limit"`) || strings.Contains(out, "hopLimit") {
		t.Fatal(out)
	}
	if out, _, _ := runCLI(t, d, "", "config", "get", "lora"); !strings.Contains(out, "hop_limit:") {
		t.Fatal(out)
	}
	if out, _, _ := runCLI(t, d, "", "config", "get", "--json"); !strings.Contains(out, `"hop_limit"`) {
		t.Fatal(out)
	}
}

func TestConfigSetNegativeAfterDashDash(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "config", "set", "--", "lora.tx_power", "-5"); err != nil || d.Config.Lora.TxPower != -5 {
		t.Fatal(err)
	}
}
