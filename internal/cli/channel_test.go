package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/channelurl"
	"meshtastik/internal/fakedevice"
)

func chans() [8]*pb.Channel {
	var c [8]*pb.Channel
	for i := range c {
		c[i] = &pb.Channel{Index: int32(i), Role: pb.Channel_DISABLED}
	}
	c[0] = &pb.Channel{Index: 0, Role: pb.Channel_PRIMARY, Settings: &pb.ChannelSettings{Psk: []byte{1}}}
	c[1] = &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "admin", Psk: bytes.Repeat([]byte{9}, 32)}}
	return c
}

func TestChannelAdd(t *testing.T) {
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "channel", "add", "admin"); err != nil {
		t.Fatal(err)
	}
	if c := d.Channels[1]; c.Role != pb.Channel_SECONDARY || c.Settings.Name != "admin" || len(c.Settings.Psk) != 32 {
		t.Fatal(c)
	}
}

func TestChannelAddNameByteLimit(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "channel", "add", "🦊🦊🦊"); err == nil || !strings.Contains(err.Error(), "11 bytes") {
		t.Fatal(err)
	}
}

func TestChannelAddFull(t *testing.T) {
	d := fakedevice.New()
	for i := range d.Channels {
		d.Channels[i].Role = pb.Channel_SECONDARY
		if i == 0 {
			d.Channels[i].Role = pb.Channel_PRIMARY
		}
	}
	if _, _, err := runCLI(t, d, "", "channel", "add", "x"); err == nil || !strings.Contains(err.Error(), "no free channel slot") {
		t.Fatal(err)
	}
}

func TestChannelAddBadPSK(t *testing.T) {
	_, _, err := runCLI(t, fakedevice.New(), "", "channel", "add", "x", "--psk", "base64:AAAA")
	if err == nil || !strings.Contains(err.Error(), "16") {
		t.Fatal(err)
	}
	d := fakedevice.New()
	k := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
	if _, _, err := runCLI(t, d, "", "channel", "add", "x", "--psk", "base64:"+k); err != nil || len(d.Channels[1].Settings.Psk) != 16 {
		t.Fatal(err)
	}
}

func TestChannelSetPSKKeywords(t *testing.T) {
	d := fakedevice.New()
	d.Channels = chans()
	for kw, n := range map[string]int{"none": 0, "default": 1, "random": 32} {
		if _, _, err := runCLI(t, d, "", "channel", "set", "1", "psk", kw); err != nil || len(d.Channels[1].Settings.Psk) != n {
			t.Fatal(kw, err)
		}
	}
}

func TestChannelSetNameAndLimit(t *testing.T) {
	d := fakedevice.New()
	d.Channels = chans()
	if _, _, err := runCLI(t, d, "", "channel", "set", "1", "name", "club"); err != nil || d.Channels[1].Settings.Name != "club" {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, d, "", "channel", "set", "1", "name", "abcdefghijkl"); err == nil || !strings.Contains(err.Error(), "11 bytes") {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, d, "", "channel", "set", "1", "module_settings.position_precision", "13"); err != nil || d.Channels[1].Settings.ModuleSettings.PositionPrecision != 13 {
		t.Fatal(err)
	}
}

func TestChannelDelShiftsDown(t *testing.T) {
	d := fakedevice.New()
	d.Channels = chans()
	d.Channels[2] = &pb.Channel{Index: 2, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "two"}}
	if _, _, err := runCLI(t, d, "", "channel", "del", "1"); err != nil {
		t.Fatal(err)
	}
	if d.Channels[1].Settings.Name != "two" || d.Channels[2].Role != pb.Channel_DISABLED || fakedevice.CountBegin(d.Received()) != 1 {
		t.Fatal(d.Channels)
	}
}

func TestChannelDelPrimaryRefused(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "channel", "del", "0"); err == nil || !strings.Contains(err.Error(), "primary") {
		t.Fatal(err)
	}
}

func TestChannelListHidesPSK(t *testing.T) {
	d := fakedevice.New()
	d.Channels = chans()
	out, _, _ := runCLI(t, d, "", "channel", "list")
	if !strings.Contains(out, "random (32 bytes)") || !strings.Contains(out, "default") || strings.Contains(out, base64.StdEncoding.EncodeToString(d.Channels[1].Settings.Psk)) {
		t.Fatal(out)
	}
}

func TestChannelListJSON(t *testing.T) {
	d := fakedevice.New()
	d.Channels = chans()
	out, _, err := runCLI(t, d, "", "channel", "list", "--json")
	var rows []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 2 {
		t.Fatal(out, err)
	}
	if rows[1]["name"] != "admin" || !strings.HasPrefix(rows[1]["psk"].(string), "base64:") || rows[1]["role"] != "SECONDARY" {
		t.Fatal(rows)
	}
}

func TestChannelURLPrint(t *testing.T) {
	d := fakedevice.New()
	d.Channels = chans()
	out, _, err := runCLI(t, d, "", "channel", "url")
	if err != nil || !strings.HasPrefix(out, channelurl.Prefix) || strings.Count(out, "\n") < 3 {
		t.Fatal(out, err)
	}
	out, _, _ = runCLI(t, d, "", "channel", "url", "--no-qr")
	if strings.Count(out, "\n") != 1 {
		t.Fatal(out)
	}
	out, _, _ = runCLI(t, d, "", "channel", "url", "--json")
	if !strings.HasPrefix(out, "{") || strings.Contains(out, "▀") {
		t.Fatal(out)
	}
}

func TestChannelURLSet(t *testing.T) {
	src := chans()
	src[1].Settings.Name = "club"
	u, _ := channelurl.Encode(src, &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_US, UsePreset: true, TxEnabled: true})
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "y\n", "channel", "url", "--set", u); err != nil {
		t.Fatal(err)
	}
	if d.Channels[1].Settings.Name != "club" || d.Config.Lora.Region != pb.Config_LoRaConfig_US {
		t.Fatal()
	}
}

func TestChannelURLSetNeedsConfirm(t *testing.T) {
	u, _ := channelurl.Encode(chans(), &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_US})
	d := fakedevice.New()
	if _, _, err := runCLI(t, d, "", "channel", "url", "--set", u); err == nil || d.Config.Lora.Region != pb.Config_LoRaConfig_EU_868 {
		t.Fatal(err)
	}
}

func TestChannelURLSetRefusesAddURL(t *testing.T) {
	u, _ := channelurl.Encode(chans(), &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_US})
	for _, add := range []string{strings.Replace(u, "/e/#", "/e/?add=true#", 1), u + "?add=true"} {
		d := fakedevice.New()
		_, _, err := runCLI(t, d, "", "--yes", "channel", "url", "--set", add)
		if err == nil || err.Error() != "add-style channel URLs are not supported yet; use channel add" {
			t.Fatal(add, err)
		}
		if fakedevice.CountBegin(d.Received()) != 0 || d.Dials() != 0 {
			t.Fatal("touched the node")
		}
	}
}
