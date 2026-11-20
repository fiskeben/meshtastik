package device_test

import (
	"errors"
	"slices"
	"sync"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

var (
	_ device.Node = device.Local{}
	_ device.Node = (*device.Remote)(nil)
)

func TestRemoteFetchOnlyWhatIsWanted(t *testing.T) {
	c, d, r := withRemote(t, nil)
	n, err := c.Remote(ctxT(t), hill)
	if err != nil {
		t.Fatal(err)
	}
	before := len(d.RemoteAdmin(hill))
	st, err := n.Fetch(ctxT(t), device.Want{Config: []string{"lora"}})
	if err != nil {
		t.Fatal(err)
	}
	got := d.RemoteAdmin(hill)[before:]
	if len(got) != 1 || got[0].GetGetConfigRequest() != pb.AdminMessage_LORA_CONFIG {
		t.Fatal(got)
	}
	if st.Config.GetLora().GetRegion() != r.Config.GetLora().GetRegion() || st.MyInfo.GetMyNodeNum() != hill || st.Self() == nil ||
		st.Metadata.GetFirmwareVersion() != r.FirmwareVersion || !n.IsRemote() || n.Num() != hill {
		t.Fatal(st)
	}
}

func TestRemoteFetchAll(t *testing.T) {
	var mu sync.Mutex
	var parts []string
	d := fakedevice.New()
	r := fakedevice.NewRemote(hill, "Hill", "HIL1")
	r.AdminKeys = [][]byte{d.PublicKey}
	r.Config.Lora.HopLimit = 6
	r.ModuleConfig.Mqtt.Enabled = true
	r.Channels[1] = &pb.Channel{Index: 1, Role: pb.Channel_SECONDARY, Settings: &pb.ChannelSettings{Name: "ops"}}
	d.Remotes = map[uint32]*fakedevice.Remote{hill: r}
	c, err := openA(t, d, device.Options{OnProgress: func(p string) { mu.Lock(); parts = append(parts, p); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	n, err := c.Remote(ctxT(t), hill)
	if err != nil {
		t.Fatal(err)
	}
	st, err := n.Fetch(ctxT(t), device.Want{Owner: true, Channels: true, Config: device.AllSections, Modules: device.AllSections})
	if err != nil {
		t.Fatal(err)
	}
	sec := proto.Clone(r.Config.Security).(*pb.Config_SecurityConfig)
	sec.PrivateKey = nil // never sent over the air
	wantCfg := proto.Clone(r.Config).(*pb.LocalConfig)
	wantCfg.Security = sec
	if !proto.Equal(st.Config, wantCfg) || !proto.Equal(st.ModuleConfig, r.ModuleConfig) {
		t.Fatal("config differs:", st.Config, st.ModuleConfig)
	}
	if st.Channels[1].GetSettings().GetName() != "ops" || st.Self().GetUser().GetLongName() != "Hill" {
		t.Fatal(st.Channels, st.Self())
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(parts, "lora") || !slices.Contains(parts, "mqtt") || !slices.Contains(parts, "channel 1") || !slices.Contains(parts, "owner") {
		t.Fatal(parts)
	}
}

func TestRemoteFirmwareTooOld(t *testing.T) {
	c, _, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.FirmwareVersion = "2.4.3" })
	if _, err := c.Remote(ctxT(t), hill); !errors.Is(err, device.ErrUnsupportedFirmware) {
		t.Fatal(err)
	}
}

func TestRemoteUnreachable(t *testing.T) {
	c, _, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Unreachable = true })
	if _, err := c.Remote(ctxT(t), hill); !errors.Is(err, device.ErrNoAnswer) {
		t.Fatal(err)
	}
}

func TestEverySectionHasAnEnum(t *testing.T) {
	for _, s := range device.ConfigSectionsWithoutEnum() {
		t.Error("no get_config enum for", s)
	}
}

func TestLocalFetchIsTheState(t *testing.T) {
	c, d, _ := withRemote(t, nil)
	l := device.Local{Client: c}
	st, err := l.Fetch(ctxT(t), device.Want{Config: []string{"lora"}})
	if err != nil || st.Self().GetUser().GetLongName() != "Test Node" || l.IsRemote() || l.Num() != d.NodeNum {
		t.Fatal(st, err)
	}
}
