package ircmesh

import (
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/device"
	"meshtastik/internal/fakedevice"
)

// texts waits until the node has received n text packets and returns them.
func texts(t *testing.T, d *fakedevice.Device, n int) []*pb.MeshPacket {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := d.Texts(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("node got %d texts, want %d", len(d.Texts()), n)
	return nil
}

func noTexts(t *testing.T, d *fakedevice.Device) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	if got := d.Texts(); len(got) != 0 {
		t.Fatalf("node got %v", got)
	}
}

func payload(p *pb.MeshPacket) string { return string(p.GetDecoded().GetPayload()) }

func TestChannelSend(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG #Ryfylke :hei")
	p := texts(t, h.d, 1)[0]
	if p.GetChannel() != 4 || p.GetTo() != device.Broadcast || payload(p) != "hei" {
		t.Fatal(p)
	}
}

func TestMentionSentToMesh(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	a := h.dial(t)
	a.register("foo")
	b := h.dial(t)
	b.register("bar")
	a.send("PRIVMSG #Ryfylke :AS02: hi")
	if p := texts(t, h.d, 1)[0]; payload(p) != "@!1bf0411d hi" {
		t.Fatal(payload(p))
	}
	b.expect(`^:Ricco!ab2c9b0a@mesh PRIVMSG #Ryfylke :AS02: hi$`)
}

func TestDirectSend(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts = true
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG as02 :psst")
	p := texts(t, h.d, 1)[0]
	if p.GetTo() != arveNum || payload(p) != "psst" || !p.GetWantAck() {
		t.Fatal(p)
	}
	c.expectNone(`NOTICE`, 300*time.Millisecond)
}

func TestUnknownTargets(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG #nope :x")
	c.expect(` 403 Ricco #nope `)
	c.send("PRIVMSG nobody :x")
	c.expect(` 401 Ricco nobody `)
	c.send("PRIVMSG Ricco :x")
	c.expect(`NOTICE Ricco :.*your own node`)
	noTexts(t, h.d)
}

func TestFormattingStripped(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG #LongFast :\x02hi\x02 \x0304there")
	if p := texts(t, h.d, 1)[0]; payload(p) != "hi there" {
		t.Fatal(payload(p))
	}
}

func TestActionSent(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG #LongFast :\x01ACTION waves\x01")
	if p := texts(t, h.d, 1)[0]; payload(p) != "* Ricco waves" {
		t.Fatal(payload(p))
	}
}

func TestCTCPNotTransmitted(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG AS02 :\x01VERSION\x01")
	c.expect("NOTICE Ricco :\x01VERSION ircmesh test\x01")
	c.send("PRIVMSG AS02 :\x01PING 123\x01")
	noTexts(t, h.d)
}

func TestLongMessageSplitPaced(t *testing.T) {
	h := newHarness(t, newFake(), func(c *Config) { c.SendInterval = 100 * time.Millisecond })
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG #LongFast :%s", strings.TrimSpace(strings.Repeat("word ", 90)))
	var at []time.Time
	deadline := time.Now().Add(3 * time.Second)
	for len(at) < 3 && time.Now().Before(deadline) {
		if n := len(h.d.Texts()); n > len(at) {
			at = append(at, time.Now())
		}
		time.Sleep(time.Millisecond)
	}
	got := h.d.Texts()
	if len(got) != 3 || !strings.HasPrefix(payload(got[0]), "word") {
		t.Fatal(len(got))
	}
	for i := 1; i < len(at); i++ {
		if gap := at[i].Sub(at[i-1]); gap < 80*time.Millisecond {
			t.Fatalf("packets %d and %d %s apart", i-1, i, gap)
		}
	}
}

func TestTooLongRefused(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG #LongFast :%s", strings.TrimSpace(strings.Repeat("xy ", 234)))
	c.expect(`NOTICE Ricco :message too long for the mesh \(701 bytes, at most 3 packets of 200\) — not sent$`)
	noTexts(t, h.d)
}

func TestDMAckFailureNotice(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts, d.AckError = true, pb.Routing_NO_RESPONSE
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG AS02 :are you there")
	c.expect(`^:ircmesh NOTICE Ricco :message to AS02 not delivered \(.*NO_RESPONSE.*\): "are you there"$`)
}

func TestDMNoAckNotice(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG AS02 :hello?")
	c.expect(`NOTICE Ricco :message to AS02 not delivered \(.*no acknowledgement.*\)`)
}

func TestEchoToOtherClients(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	a := h.dial(t)
	a.register("foo")
	b := h.dial(t)
	b.register("bar")
	a.send("PRIVMSG #Ryfylke :hei")
	b.expect(`^:Ricco!ab2c9b0a@mesh PRIVMSG #Ryfylke :?hei$`)
	a.expectNone(`PRIVMSG`, 300*time.Millisecond)
}

func TestLatin1Client(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG #LongFast :caf\xe9")
	if p := texts(t, h.d, 1)[0]; payload(p) != "café" {
		t.Fatal(payload(p))
	}
}

func TestNoticeNotTransmitted(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("NOTICE #LongFast :x")
	c.expectNone(` 421 `, 200*time.Millisecond)
	noTexts(t, h.d)
}

func TestReplyToOwnMessageIsQuoted(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG #Ryfylke :dinner at six")
	sent := texts(t, h.d, 1)[0]
	reply := rx(arveNum, 4, "ok")
	reply.GetDecoded().ReplyId = sent.GetId()
	h.d.Inject(reply)
	c.expect(`PRIVMSG #Ryfylke :↪ "dinner at six" ok$`)
}
