package configpath

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestSectionsMatchOneofs(t *testing.T) {
	check := func(local, wrapper protoreflect.MessageDescriptor) {
		for i := 0; i < local.Fields().Len(); i++ {
			f := local.Fields().Get(i)
			if f.Kind() != protoreflect.MessageKind {
				continue
			}
			w := wrapper.Fields().ByName(f.Name())
			if w == nil || w.ContainingOneof() == nil || w.Message().FullName() != f.Message().FullName() {
				t.Errorf("%s.%s has no matching oneof in %s", local.Name(), f.Name(), wrapper.Name())
			}
		}
	}
	check((&pb.LocalConfig{}).ProtoReflect().Descriptor(), (&pb.Config{}).ProtoReflect().Descriptor())
	check((&pb.LocalModuleConfig{}).ProtoReflect().Descriptor(), (&pb.ModuleConfig{}).ProtoReflect().Descriptor())
	s := Sections()
	for _, want := range []string{"device", "lora", "bluetooth", "security", "mqtt", "serial", "telemetry", "neighbor_info"} {
		if !slices.Contains(s, want) {
			t.Errorf("missing section %s", want)
		}
	}
	if len(s) != len(slices.Compact(slices.Clone(s))) {
		t.Error("config and module section names collide")
	}
}

func TestParse(t *testing.T) {
	ok := map[string]Path{
		"lora.region": {Section: "lora", Field: "region"}, "lora.hopLimit": {Section: "lora", Field: "hop_limit"},
		"mqtt.enabled": {Module: true, Section: "mqtt", Field: "enabled"}, "lora": {Section: "lora"},
		"network.ipv4_config.ip": {Section: "network", Field: "ipv4_config.ip"},
	}
	for in, want := range ok {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for in, hint := range map[string]string{"lroa.region": "lora", "lora.regoin": "region", "lora.region.x": "region"} {
		if _, err := Parse(in); err == nil || !strings.Contains(err.Error(), hint) {
			t.Errorf("%s: %v", in, err)
		}
	}
}

func TestSetValues(t *testing.T) {
	cfg, mod := &pb.LocalConfig{}, &pb.LocalModuleConfig{}
	set := func(path, raw string) error {
		p, err := Parse(path)
		if err != nil {
			return err
		}
		return Set(cfg, mod, p, raw)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(set("lora.region", "eu_868"))
	if cfg.Lora.Region != pb.Config_LoRaConfig_EU_868 {
		t.Fatal("enum")
	}
	for _, v := range []string{"false", "no", "off", "0"} {
		must(set("bluetooth.enabled", "true"))
		must(set("bluetooth.enabled", v))
		if cfg.Bluetooth.Enabled {
			t.Fatal(v)
		}
	}
	must(set("security.admin_key", "base64:AAEC,base64:AwQF"))
	if len(cfg.Security.AdminKey) != 2 || !bytes.Equal(cfg.Security.AdminKey[1], []byte{3, 4, 5}) {
		t.Fatal("bytes list")
	}
	must(set("mqtt.enabled", "yes"))
	if !mod.Mqtt.Enabled {
		t.Fatal("module")
	}
	must(set("network.ipv4_config.ip", "42"))
	if cfg.Network.Ipv4Config.Ip != 42 {
		t.Fatal("nested")
	}
	for path, raw := range map[string]string{"lora.region": "mars", "bluetooth.enabled": "maybe", "lora.hop_limit": "-1", "lora": "x"} {
		if err := set(path, raw); err == nil {
			t.Errorf("%s=%s accepted", path, raw)
		}
	}
	if err := set("lora.hop_limit", "4294967296"); err == nil {
		t.Error("uint32 overflow accepted")
	}
	if err := set("lora.region", "mars"); !strings.Contains(err.Error(), "EU_868") {
		t.Errorf("no options listed: %v", err)
	}
}

func TestFormatField(t *testing.T) {
	cfg := &pb.LocalConfig{Lora: &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_EU_868, TxEnabled: true}}
	m := cfg.Lora.ProtoReflect()
	for f, want := range map[string]string{"region": "EU_868", "tx_enabled": "true", "hop_limit": "0"} {
		if got, _ := FormatField(m, f); got != want {
			t.Errorf("%s=%q", f, got)
		}
	}
	sec := (&pb.Config_SecurityConfig{AdminKey: [][]byte{{0, 1, 2}, {3, 4, 5}}}).ProtoReflect()
	if got, _ := FormatField(sec, "admin_key"); got != "base64:AAEC,base64:AwQF" {
		t.Errorf("bytes list %q", got)
	}
}

func TestSectionRoundTrip(t *testing.T) {
	l := &pb.LocalConfig{Lora: &pb.Config_LoRaConfig{HopLimit: 3}, Bluetooth: &pb.Config_BluetoothConfig{Enabled: true}}
	got := &pb.LocalConfig{}
	for _, c := range ConfigSections(l) {
		MergeConfig(got, c)
	}
	if !proto.Equal(l, got) {
		t.Fatal(got)
	}
	ml := &pb.LocalModuleConfig{Mqtt: &pb.ModuleConfig_MQTTConfig{Enabled: true}}
	mgot := &pb.LocalModuleConfig{}
	for _, c := range ModuleSections(ml) {
		MergeModuleConfig(mgot, c)
	}
	if !proto.Equal(ml, mgot) {
		t.Fatal(mgot)
	}
}

func TestChangesOnlyChangedSections(t *testing.T) {
	before := &pb.LocalConfig{Lora: &pb.Config_LoRaConfig{HopLimit: 3}, Device: &pb.Config_DeviceConfig{}}
	after := proto.Clone(before).(*pb.LocalConfig)
	after.Lora.HopLimit = 5
	cfgs, mods := Changes(before, after, &pb.LocalModuleConfig{}, &pb.LocalModuleConfig{})
	if len(cfgs) != 1 || cfgs[0].GetLora().GetHopLimit() != 5 || len(mods) != 0 {
		t.Fatal(cfgs, mods)
	}
}

func TestFieldDiffs(t *testing.T) {
	want := (&pb.Config_LoRaConfig{HopLimit: 5}).ProtoReflect()
	got := (&pb.Config_LoRaConfig{HopLimit: 3}).ProtoReflect()
	d := FieldDiffs("lora", want, got)
	if len(d) != 1 || d[0] != (FieldDiff{"lora.hop_limit", "5", "3"}) {
		t.Fatal(d)
	}
	nw := (&pb.Config_NetworkConfig{Ipv4Config: &pb.Config_NetworkConfig_IpV4Config{Ip: 1}}).ProtoReflect()
	ng := (&pb.Config_NetworkConfig{}).ProtoReflect()
	d = FieldDiffs("network", nw, ng)
	if len(d) != 1 || d[0].Path != "network.ipv4_config.ip" {
		t.Fatal(d)
	}
	sw := (&pb.Config_SecurityConfig{AdminKey: [][]byte{{1}}}).ProtoReflect()
	sg := (&pb.Config_SecurityConfig{}).ProtoReflect()
	d = FieldDiffs("security", sw, sg)
	if len(d) != 1 || d[0].Path != "security.admin_key" || d[0].Want != "base64:AQ==" {
		t.Fatal(d)
	}
}
