package channelurl

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
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

func TestURLRoundTrip(t *testing.T) {
	lora := &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_EU_868, UsePreset: true}
	u, err := Encode(chans(), lora)
	if err != nil || !strings.HasPrefix(u, Prefix) || strings.Contains(u, "=") {
		t.Fatal(u, err)
	}
	set, err := Decode(u)
	if err != nil || len(set.Settings) != 2 || set.Settings[1].Name != "admin" || set.LoraConfig.Region != pb.Config_LoRaConfig_EU_868 {
		t.Fatal(set, err)
	}
}

func TestDecodeVariants(t *testing.T) {
	raw, _ := proto.Marshal(&pb.ChannelSet{Settings: []*pb.ChannelSettings{{Psk: []byte{1}}}})
	for _, u := range []string{
		Prefix + base64.RawURLEncoding.EncodeToString(raw),
		"https://meshtastic.org/e/?add=true#" + base64.RawURLEncoding.EncodeToString(raw),
		Prefix + base64.StdEncoding.EncodeToString(raw),
		Prefix + base64.RawURLEncoding.EncodeToString(raw) + "?add=true",
	} {
		if _, err := Decode(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, bad := range []string{"https://meshtastic.org/e/", Prefix + "!!!", Prefix} {
		if _, err := Decode(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestToChannels(t *testing.T) {
	c := ToChannels(&pb.ChannelSet{Settings: []*pb.ChannelSettings{{}, {Name: "b"}}})
	if c[0].Role != pb.Channel_PRIMARY || c[1].Role != pb.Channel_SECONDARY || c[7].Role != pb.Channel_DISABLED || c[7].Index != 7 {
		t.Fatal(c)
	}
}

func TestAddOnly(t *testing.T) {
	for url, want := range map[string]bool{
		"https://meshtastic.org/e/#CgMSAQE":              false,
		"https://meshtastic.org/e/?add=true#CgMSAQE":     true,
		"https://meshtastic.org/e/#CgMSAQE?add=true":     true,
		"https://meshtastic.org/e/#CgMSAQE?add=TRUE":     true,
		"https://meshtastic.org/e/?add=false#CgMSAQE":    false,
		"https://meshtastic.org/e/?other=1#CgMSAQE?x=on": false,
	} {
		if got := AddOnly(url); got != want {
			t.Errorf("%s: %v", url, got)
		}
	}
}
