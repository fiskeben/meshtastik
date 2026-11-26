package ircmesh

import (
	"reflect"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

func TestBaseNick(t *testing.T) {
	cases := []struct {
		u    *pb.User
		num  uint32
		want string
	}{
		{&pb.User{ShortName: "AS02", LongName: "Arve Tak"}, 1, "AS02"},
		{&pb.User{ShortName: "🥸", LongName: "Ricco"}, 1, "Ricco"},
		{&pb.User{ShortName: "🥸", LongName: "Dyngelandsvegen Base"}, 1, "Dyngelandsvegen_"},
		{&pb.User{ShortName: "🥸", LongName: "🙂"}, 0xab2c9b0a, "n9b0a"},
		{nil, 0x1bf0411d, "n411d"},
		{&pb.User{ShortName: "a b"}, 1, "ab"},
		{&pb.User{ShortName: "[x]"}, 1, "[x]"},
	}
	for _, c := range cases {
		if got := baseNick(c.u, c.num); got != c.want {
			t.Errorf("baseNick(%v) = %q, want %q", c.u, got, c.want)
		}
	}
}

func TestBaseNickLeadingDigit(t *testing.T) {
	if got := baseNick(&pb.User{ShortName: "1c70"}, 1); got != "_1c70" {
		t.Errorf("got %q", got)
	}
	if got := baseNick(&pb.User{ShortName: "-ab"}, 1); got != "_-ab" {
		t.Errorf("got %q", got)
	}
	long := baseNick(&pb.User{ShortName: "🥸", LongName: "1234567890123456789"}, 1)
	if long != "_123456789012345" {
		t.Errorf("got %q", long)
	}
}

func lora(preset pb.Config_LoRaConfig_ModemPreset) *pb.Config_LoRaConfig {
	return &pb.Config_LoRaConfig{UsePreset: true, ModemPreset: preset}
}

func channels(names map[int]string) [8]*pb.Channel {
	var chs [8]*pb.Channel
	for i := range chs {
		chs[i] = &pb.Channel{Index: int32(i), Role: pb.Channel_DISABLED}
	}
	for i, n := range names {
		role := pb.Channel_SECONDARY
		if i == 0 {
			role = pb.Channel_PRIMARY
		}
		chs[i] = &pb.Channel{Index: int32(i), Role: role, Settings: &pb.ChannelSettings{Name: n, Psk: []byte{1}}}
	}
	return chs
}

func TestChannelNames(t *testing.T) {
	l := lora(pb.Config_LoRaConfig_LONG_FAST)
	if got := channelNames(channels(map[int]string{0: ""}), l); !reflect.DeepEqual(got, map[int]string{0: "#LongFast"}) {
		t.Error(got)
	}
	if got := channelNames(channels(map[int]string{0: "", 4: "Ryfylke"}), l); !reflect.DeepEqual(got, map[int]string{0: "#LongFast", 4: "#Ryfylke"}) {
		t.Error(got)
	}
	if got := channelNames(channels(map[int]string{0: "", 2: "a b", 5: "AB"}), l); !reflect.DeepEqual(got, map[int]string{0: "#LongFast", 2: "#ab", 5: "#AB-5"}) {
		t.Error(got)
	}
	if got := channelNames(channels(map[int]string{0: "", 3: ""}), lora(pb.Config_LoRaConfig_MEDIUM_SLOW)); !reflect.DeepEqual(got, map[int]string{0: "#MediumSlow", 3: "#ch3"}) {
		t.Error(got)
	}
	if got := presetName(&pb.Config_LoRaConfig{UsePreset: false}); got != "Custom" {
		t.Error(got)
	}
}

func TestChannelTopic(t *testing.T) {
	l := lora(pb.Config_LoRaConfig_LONG_FAST)
	custom := &pb.Channel{Index: 4, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "Ryfylke", Psk: make([]byte, 32)}}
	if got := channelTopic(custom, l); got != "channel 4 · secondary · LongFast · custom key" {
		t.Error(got)
	}
	cases := map[string][]byte{"default key": {1}, "simple key": {3}, "no encryption": nil}
	for want, psk := range cases {
		ch := &pb.Channel{Index: 0, Role: pb.Channel_PRIMARY, Settings: &pb.ChannelSettings{Psk: psk}}
		if got := channelTopic(ch, l); got != "channel 0 · primary · LongFast · "+want {
			t.Error(got)
		}
	}
}
