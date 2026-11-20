package device_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

const hill = 0x99

// withRemote opens a client on a fake that can reach remote node hill.
func withRemote(t *testing.T, setup func(*fakedevice.Device, *fakedevice.Remote)) (*device.Client, *fakedevice.Device, *fakedevice.Remote) {
	t.Helper()
	return withRemoteOpts(t, device.Options{MeshTimeout: 200 * time.Millisecond}, setup)
}

// withRemoteOpts is withRemote with client options.
func withRemoteOpts(t *testing.T, o device.Options, setup func(*fakedevice.Device, *fakedevice.Remote)) (*device.Client, *fakedevice.Device, *fakedevice.Remote) {
	t.Helper()
	d := fakedevice.New()
	r := fakedevice.NewRemote(hill, "Hill", "HIL1")
	r.AdminKeys = [][]byte{d.PublicKey}
	d.Remotes = map[uint32]*fakedevice.Remote{hill: r}
	if setup != nil {
		setup(d, r)
	}
	c, err := openA(t, d, o)
	if err != nil {
		t.Fatal(err)
	}
	return c, d, r
}

func TestRequestReturnsTheAnswer(t *testing.T) {
	c, _, r := withRemote(t, nil)
	p, err := c.Request(ctxT(t), hill, pb.PortNum_POSITION_APP, &pb.Position{}, device.RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var pos pb.Position
	if err := proto.Unmarshal(p.GetDecoded().GetPayload(), &pos); err != nil || pos.GetLatitudeI() != r.Position.GetLatitudeI() {
		t.Fatal(&pos, err)
	}
}

func TestRequestRoutingError(t *testing.T) {
	c, _, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) {
		r.Unreachable, r.FailWith = true, pb.Routing_MAX_RETRANSMIT
	})
	_, err := c.Request(ctxT(t), hill, pb.PortNum_POSITION_APP, &pb.Position{}, device.RequestOptions{})
	var re *device.RoutingError
	if !errors.As(err, &re) || re.Reason != pb.Routing_MAX_RETRANSMIT || !strings.Contains(err.Error(), "no route to !00000099") {
		t.Fatal(err)
	}
}

func TestRequestTimesOut(t *testing.T) {
	c, _, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Unreachable = true })
	start := time.Now()
	_, err := c.Request(ctxT(t), hill, pb.PortNum_POSITION_APP, &pb.Position{}, device.RequestOptions{Timeout: 100 * time.Millisecond})
	if !errors.Is(err, device.ErrNoAnswer) || !strings.Contains(err.Error(), "!00000099") || time.Since(start) > time.Second {
		t.Fatal(err)
	}
}

func TestRequestIgnoresOtherAnswers(t *testing.T) {
	c, _, _ := withRemote(t, func(_ *fakedevice.Device, r *fakedevice.Remote) { r.Decoys = true })
	p, err := c.Request(ctxT(t), hill, pb.PortNum_POSITION_APP, &pb.Position{}, device.RequestOptions{})
	if err != nil || p.GetFrom() != hill {
		t.Fatal(p, err)
	}
}

func TestRequestRefusesSelf(t *testing.T) {
	c, d, _ := withRemote(t, nil)
	if _, err := c.Request(ctxT(t), d.NodeNum, pb.PortNum_POSITION_APP, &pb.Position{}, device.RequestOptions{}); err == nil {
		t.Fatal("asked the connected node itself")
	}
}

func TestRequestSendsOptions(t *testing.T) {
	c, d, _ := withRemote(t, nil)
	if _, err := c.Request(ctxT(t), hill, pb.PortNum_POSITION_APP, &pb.Position{},
		device.RequestOptions{Channel: 2, HopLimit: 5, PKI: true, WantAck: true}); err != nil {
		t.Fatal(err)
	}
	p := d.RemoteRequests(hill)[0]
	if p.GetChannel() != 2 || p.GetHopLimit() != 5 || !p.GetPkiEncrypted() || !p.GetWantAck() || !p.GetDecoded().GetWantResponse() {
		t.Fatal(p)
	}
}

func TestRoutingErrorMessages(t *testing.T) {
	for reason, want := range map[pb.Routing_Error]string{
		pb.Routing_ADMIN_PUBLIC_KEY_UNAUTHORIZED: "admin-key add",
		pb.Routing_PKI_UNKNOWN_PUBKEY:            "no public key for !00000099",
		pb.Routing_PKI_SEND_FAIL_PUBLIC_KEY:      "no public key for !00000099",
		pb.Routing_NO_ROUTE:                      "no route to !00000099",
		pb.Routing_TIMEOUT:                       "no route to !00000099",
		pb.Routing_NO_RESPONSE:                   "no route to !00000099",
		pb.Routing_RATE_LIMIT_EXCEEDED:           "rate",
	} {
		if got := (&device.RoutingError{Dest: hill, Reason: reason}).Error(); !strings.Contains(got, want) {
			t.Errorf("%s: %q lacks %q", reason, got, want)
		}
	}
}
