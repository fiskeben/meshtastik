package device_test

import (
	"errors"
	"strings"
	"testing"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

var getOwner = &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerRequest{GetOwnerRequest: true}}

func setHopLimit(n uint32) *pb.AdminMessage {
	return &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{
		PayloadVariant: &pb.Config_Lora{Lora: &pb.Config_LoRaConfig{HopLimit: n, Region: pb.Config_LoRaConfig_EU_868, UsePreset: true, TxEnabled: true}}}}}
}

func TestRemoteAdminRequestGetsOwner(t *testing.T) {
	c, d, _ := withRemote(t, nil)
	resp, err := device.AdminRequestTo(c, ctxT(t), hill, getOwner)
	if err != nil || resp.GetGetOwnerResponse().GetLongName() != "Hill" {
		t.Fatal(resp, err)
	}
	p := d.RemoteRequests(hill)[0]
	if !p.GetPkiEncrypted() || !p.GetWantAck() || !p.GetDecoded().GetWantResponse() || len(d.Received()) != 0 {
		t.Fatal(p)
	}
}

func TestRemoteUnauthorizedNamesTheFix(t *testing.T) {
	c, _, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Config.Security.AdminKey = nil })
	_, err := device.AdminRequestTo(c, ctxT(t), hill, getOwner)
	var re *device.RoutingError
	if !errors.As(err, &re) || re.Reason != pb.Routing_ADMIN_PUBLIC_KEY_UNAUTHORIZED || !strings.Contains(err.Error(), "admin-key add") {
		t.Fatal(err)
	}
}

func TestRemoteUnknownPublicKeyPrecheck(t *testing.T) {
	c, d, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Owner.PublicKey = nil })
	_, err := device.AdminRequestTo(c, ctxT(t), hill, getOwner)
	if err == nil || !strings.Contains(err.Error(), "no public key for !00000099") || len(d.RemoteRequests(hill)) != 0 {
		t.Fatal(err, d.RemoteRequests(hill))
	}
}

func TestRemoteBadSessionKeyRefreshes(t *testing.T) {
	c, _, r := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.RotatePasskeyOnce = true })
	if _, err := device.AdminRequestTo(c, ctxT(t), hill, getOwner); err != nil { // holds a key that is then invalidated
		t.Fatal(err)
	}
	if err := device.PostTo(c, ctxT(t), hill, setHopLimit(5)); err != nil {
		t.Fatal(err)
	}
	if r.Config.GetLora().GetHopLimit() != 5 {
		t.Fatal(r.Config.GetLora())
	}
}

func TestRemoteRequestRetries(t *testing.T) {
	c, d, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.DropFirst = 2 })
	if _, err := device.AdminRequestTo(c, ctxT(t), hill, getOwner); err != nil {
		t.Fatal(err)
	}
	if n := len(d.RemoteRequests(hill)); n != 3 {
		t.Fatal("attempts", n)
	}
}

func TestRemoteRequestGivesUp(t *testing.T) {
	c, d, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Unreachable = true })
	_, err := device.AdminRequestTo(c, ctxT(t), hill, getOwner)
	if !errors.Is(err, device.ErrNoAnswer) || len(d.RemoteRequests(hill)) != 3 {
		t.Fatal(err, len(d.RemoteRequests(hill)))
	}
}

func TestRemotePostRetriesLostAck(t *testing.T) {
	c, d, r := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.DropAcks = 1 }) // the get is answered, so this is the set's ack
	if err := device.PostTo(c, ctxT(t), hill, setHopLimit(6)); err != nil {
		t.Fatal(err)
	}
	if r.Config.GetLora().GetHopLimit() != 6 || len(d.RemoteAdmin(hill)) != 3 { // get, set, set again
		t.Fatal(r.Config.GetLora(), len(d.RemoteAdmin(hill)))
	}
}

func TestLocalAdminUnchanged(t *testing.T) {
	c, d, _ := withRemote(t, nil)
	if _, err := device.AdminRequestTo(c, ctxT(t), d.NodeNum, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetConfigRequest{GetConfigRequest: pb.AdminMessage_LORA_CONFIG}}); err != nil {
		t.Fatal(err)
	}
	if len(d.Received()) != 1 || len(d.RemoteRequests(hill)) != 0 {
		t.Fatal(d.Received())
	}
}
