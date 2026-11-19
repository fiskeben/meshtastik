package cli

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/fakedevice"
)

func text(from uint32, to uint32, s string) *pb.MeshPacket {
	return &pb.MeshPacket{From: from, To: to, Id: rand.Uint32() | 1,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(s)}}}
}

func onChannel(p *pb.MeshPacket, ch uint32) *pb.MeshPacket { p.Channel = ch; return p }

func port(from uint32, pn pb.PortNum) *pb.MeshPacket {
	return &pb.MeshPacket{From: from, To: 0xffffffff, Id: rand.Uint32() | 1,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pn, Payload: []byte{1, 2}}}}
}

func TestListenHuman(t *testing.T) {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{LongName: "Car", ShortName: "CAR1"}}}
	d.Backlog = []*pb.MeshPacket{text(42, 0xffffffff, "yes, 20 min out"), text(42, d.NodeNum, "for you")}
	out, errOut, err := runCLI(t, d, "", "listen", "--count", "2")
	if err != nil || !strings.Contains(out, "Car (CAR1)") || !strings.Contains(out, "→ all") || !strings.Contains(out, "→ me") || !strings.Contains(out, ": yes, 20 min out") {
		t.Fatal(out, err)
	}
	if lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n"); len(lines) != 2 || !strings.Contains(lines[0], "  ch0  ") {
		t.Fatalf("%q", out)
	}
	if !strings.Contains(errOut, "Listening on Test Node (TEST)") || strings.Contains(out, "Listening") {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestListenUnknownNodeFallsBackToID(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{text(42, 7, "hi")}
	out, _, err := runCLI(t, d, "", "listen", "--count", "1")
	if err != nil || !strings.Contains(out, "!0000002a") || !strings.Contains(out, "→ !00000007") {
		t.Fatal(out, err)
	}
}

func TestListenJSON(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{text(42, 0xffffffff, "hello")}
	out, _, err := runCLI(t, d, "", "listen", "--json", "--count", "1")
	if err != nil || strings.Count(out, "\n") != 1 {
		t.Fatalf("%q %v", out, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if m["from"] != "!0000002a" || m["to"] != "broadcast" || m["port"] != "TEXT_MESSAGE_APP" || m["text"] != "hello" {
		t.Fatal(m)
	}
	if _, ok := m["encrypted"]; ok {
		t.Fatal("encrypted present", m)
	}
	if _, err := time.Parse(time.RFC3339, m["time"].(string)); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"from_name", "channel", "snr", "rssi"} {
		if _, ok := m[k]; !ok {
			t.Fatal("missing", k, m)
		}
	}
}

func TestListenChannelFilter(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{onChannel(text(42, 0xffffffff, "a"), 0), onChannel(text(42, 0xffffffff, "b"), 1)}
	out, _, err := runCLI(t, d, "", "listen", "--channel", "1", "--count", "1")
	if err != nil || !strings.Contains(out, ": b") || strings.Contains(out, ": a") || !strings.Contains(out, "ch1") {
		t.Fatal(out, err)
	}
	if _, _, err := runCLI(t, d, "", "listen", "--channel", "8"); err == nil {
		t.Fatal("channel 8 accepted")
	}
}

func TestListenAllShowsOtherPorts(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{port(42, pb.PortNum_POSITION_APP), text(42, 0xffffffff, "hi")}
	out, _, err := runCLI(t, d, "", "listen", "--count", "1")
	if err != nil || strings.Contains(out, "POSITION_APP") || !strings.Contains(out, ": hi") {
		t.Fatal(out, err)
	}
	d = fakedevice.New()
	d.Backlog = []*pb.MeshPacket{port(42, pb.PortNum_POSITION_APP), text(42, 0xffffffff, "hi")}
	out, _, err = runCLI(t, d, "", "listen", "--all", "--count", "2")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if err != nil || len(lines) != 2 || !strings.HasSuffix(lines[0], ": [POSITION_APP]") || !strings.HasSuffix(lines[1], ": hi") {
		t.Fatal(out, err)
	}
}

func TestListenAllShowsEncrypted(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{{From: 42, To: 0xffffffff, Id: 9, PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{0xde, 0xad}}}, text(42, 0xffffffff, "hi")}
	out, _, err := runCLI(t, d, "", "listen", "--count", "1")
	if err != nil || strings.Contains(out, "encrypted") || !strings.Contains(out, ": hi") {
		t.Fatal(out, err)
	}
	d = fakedevice.New()
	d.Backlog = []*pb.MeshPacket{{From: 42, To: 0xffffffff, Id: 9, PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{0xde, 0xad}}}}
	out, _, err = runCLI(t, d, "", "listen", "--all", "--count", "1")
	if err != nil || !strings.HasSuffix(out, ": <encrypted>\n") || strings.Contains(out, "dead") {
		t.Fatal(out, err)
	}
	d = fakedevice.New() // the backlog goes out on the first dial only
	d.Backlog = []*pb.MeshPacket{{From: 42, To: 0xffffffff, Id: 9, PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{0xde, 0xad}}}}
	out, _, err = runCLI(t, d, "", "listen", "--all", "--json", "--count", "1")
	var m map[string]any
	if err != nil || json.Unmarshal([]byte(out), &m) != nil || m["encrypted"] != true {
		t.Fatal(out, err)
	}
	if _, ok := m["port"]; ok {
		t.Fatal("port present for an encrypted packet", m)
	}
}

func TestListenEmptyPacketIsNotEncrypted(t *testing.T) {
	d := fakedevice.New()
	d.Backlog = []*pb.MeshPacket{{From: 42, To: 0xffffffff, Id: 9}}
	out, _, err := runCLI(t, d, "", "listen", "--all", "--count", "1")
	if err != nil || strings.Contains(out, "encrypted") || !strings.HasSuffix(out, ": [UNKNOWN_APP]\n") {
		t.Fatal(out, err)
	}
}

func TestListenJSONEscapesDELAndC1(t *testing.T) {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{LongName: "Ev\u009bil", ShortName: "EV"}}}
	d.Backlog = []*pb.MeshPacket{text(42, 0xffffffff, "a\x7fb\u0085c\x1b")}
	out, _, err := runCLI(t, d, "", "listen", "--json", "--count", "1")
	var m map[string]any
	if err != nil || strings.ContainsAny(out, "\x7f\u0085\u009b\x1b") || json.Unmarshal([]byte(out), &m) != nil ||
		m["text"] != "a\x7fb\u0085c\x1b" || m["from_name"] != "Ev\u009bil" || !strings.Contains(out, `\u009b`) {
		t.Fatalf("%q %v", out, err)
	}
}

func TestListenReportsReconnect(t *testing.T) {
	d := fakedevice.New()
	go func() {
		for d.Dials() < 1 {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		d.DropLink()
		for d.Dials() < 2 {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		d.Inject(text(42, 0xffffffff, "back"))
	}()
	out, errOut, err := runCLI(t, d, "", "listen", "--count", "1")
	if err != nil || !strings.Contains(out, ": back") || !strings.Contains(errOut, "connection lost (") || !strings.Contains(errOut, "reconnecting in 10ms") {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
}

func TestListenHidesOwnResponsesByDefault(t *testing.T) {
	dev := func() *fakedevice.Device {
		d := fakedevice.New()
		ack := port(d.NodeNum, pb.PortNum_ROUTING_APP)
		ack.GetDecoded().RequestId = 1234
		reply := text(42, d.NodeNum, "a reply")
		reply.GetDecoded().RequestId = 99
		d.Backlog = []*pb.MeshPacket{ack, reply, text(42, 0xffffffff, "hi")}
		return d
	}
	out, _, err := runCLI(t, dev(), "", "listen", "--count", "1")
	if err != nil || strings.Contains(out, "ROUTING_APP") || strings.Contains(out, "a reply") || !strings.Contains(out, ": hi") {
		t.Fatal(out, err)
	}
	out, _, err = runCLI(t, dev(), "", "listen", "--all", "--count", "1")
	if err != nil || !strings.Contains(out, "[ROUTING_APP]") {
		t.Fatal(out, err)
	}
}

func TestListenSanitisesText(t *testing.T) {
	d := fakedevice.New()
	d.ExtraNodes = []*pb.NodeInfo{{Num: 42, User: &pb.User{LongName: "Ev\x1bil", ShortName: "EV"}}}
	d.Backlog = []*pb.MeshPacket{text(42, 0xffffffff, "hi\x1b[31mred\xff\ttab\nline\u009b")}
	out, _, err := runCLI(t, d, "", "listen", "--count", "1")
	if err != nil || strings.ContainsAny(out, "\x1b\u009b") || !strings.Contains(out, `\x1b[31mred`) || !strings.Contains(out, "\uFFFD") ||
		!strings.Contains(out, "\ttab") || !strings.Contains(out, `\x0aline`) || strings.Count(out, "\n") != 1 || !strings.Contains(out, `Ev\x1bil`) {
		t.Fatalf("%q %v", out, err)
	}
}

func TestListenCtrlCExitsZero(t *testing.T) {
	d := fakedevice.New()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	out, _, err := runCLICtx(t, ctx, d, func(string) bool { return false }, "listen")
	if err != nil || out != "" {
		t.Fatal(out, err)
	}
}

func TestListenCountValidated(t *testing.T) {
	if _, _, err := runCLI(t, fakedevice.New(), "", "listen", "--count", "0"); err == nil {
		t.Fatal("count 0 accepted")
	}
}
