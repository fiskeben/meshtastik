package ircmesh

import (
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

func TestChannelTextFromMember(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.Inject(rx(arveNum, 4, "hei"))
	c.expect(`^:AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?hei$`)
}

func TestTextFromNewChannelJoinsFirst(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.Inject(rx(arveNum, 0, "hi"))
	c.expect(`^:AS02!1bf0411d@mesh JOIN :?#LongFast$`)
	c.expect(`^:AS02!1bf0411d@mesh PRIVMSG #LongFast :?hi$`)
	c.send("NAMES #Ryfylke")
	c.expect(` 353 Ricco = #Ryfylke :Ricco AS02$`)
}

func TestUnknownSenderGetsFallbackNick(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.Inject(rx(0x77778888, 0, "who am i"))
	c.expect(`^:n8888!77778888@mesh JOIN :?#LongFast$`)
	c.expect(`^:n8888!77778888@mesh PRIVMSG #LongFast :who am i$`)
}

func TestDirectMessage(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	p := rx(arveNum, 0, "psst")
	p.To = selfNum
	h.d.Inject(p)
	c.expect(`^:AS02!1bf0411d@mesh PRIVMSG Ricco :?psst$`)
	c.send("NAMES #LongFast")
	c.expect(` 353 Ricco = #LongFast :?Ricco$`)
}

func TestEchoFromDaemonClient(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	p := rx(selfNum, 4, "sent from cli")
	p.RxTime = nil
	h.d.Inject(p)
	c.expect(`^:Ricco!ab2c9b0a@mesh PRIVMSG #Ryfylke :sent from cli$`)
	dm := rx(selfNum, 0, "dm from cli")
	dm.RxTime, dm.To = nil, arveNum
	h.d.Inject(dm)
	c.expect(`^:Ricco!ab2c9b0a@mesh PRIVMSG AS02 :dm from cli$`)
}

func TestMultilineAndReaction(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	orig := rx(arveNum, 4, "see you at the hut tomorrow morning ok")
	h.d.Inject(orig)
	c.expect(`PRIVMSG #Ryfylke :see you`)
	two := rx(arveNum, 4, "line one\nline two")
	h.d.Inject(two)
	c.expect(`PRIVMSG #Ryfylke :line one$`)
	c.expect(`PRIVMSG #Ryfylke :line two$`)
	react := rx(arveNum, 4, "👍")
	react.GetDecoded().Emoji, react.GetDecoded().ReplyId = 1, orig.Id
	h.d.Inject(react)
	c.expect("^:AS02!1bf0411d@mesh PRIVMSG #Ryfylke :\x01ACTION reacted 👍 to \"see you at the hut tomorrow mo…\"\x01$")
}

func TestRenameSendsNick(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	b, _ := proto.Marshal(&pb.User{ShortName: "AT", LongName: "Arve Tak"})
	p := rx(arveNum, 4, "")
	p.GetDecoded().Portnum, p.GetDecoded().Payload = pb.PortNum_NODEINFO_APP, b
	h.d.Inject(p)
	c.expect(`^:AS02!1bf0411d@mesh NICK :?AT$`)
}

func TestPartStopsDelivery(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PART #Ryfylke")
	c.expect(` PART #Ryfylke`)
	h.d.Inject(rx(arveNum, 4, "hidden"))
	c.expectNone(`hidden`, 300*time.Millisecond)
	c.send("JOIN #Ryfylke")
	c.expect(` 366 Ricco #Ryfylke `)
	h.d.Inject(rx(arveNum, 4, "shown"))
	c.expect(`PRIVMSG #Ryfylke :?shown$`)
}

func TestAgingPartsAndQuits(t *testing.T) {
	clk := &clock{}
	h := newHarness(t, withNodes(newFake()), func(c *Config) { c.Now = clk.now })
	c := h.dial(t)
	c.register("foo")
	h.d.Inject(rx(arveNum, 0, "hi"))
	c.expect(`PRIVMSG #LongFast :?hi$`)
	clk.advance(23*time.Hour + 30*time.Minute)
	h.srv.age()
	c.expect(`^:AS02!1bf0411d@mesh PART #Ryfylke :not heard on this channel for 24h$`)
	c.expectNone(` QUIT `, 100*time.Millisecond)
	clk.advance(time.Hour)
	h.srv.age()
	c.expect(`^:AS02!1bf0411d@mesh QUIT :timed out$`)
}

func TestReplayWhileDetached(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	h.d.Inject(rx(arveNum, 4, "one"))
	p := rx(arveNum, 0, "two")
	p.To = selfNum
	h.d.Inject(p)
	settle()
	c := h.dial(t)
	c.send("CAP LS 302")
	c.send("CAP REQ :server-time")
	c.send("NICK foo")
	c.send("USER u 0 * :r")
	c.send("CAP END")
	c.expect(` 366 Ricco #Ryfylke `)
	c.expect(`^@time=\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z :AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?one$`)
	c.expect(`^@time=\S+ :AS02!1bf0411d@mesh PRIVMSG Ricco :?two$`)
	d := h.dial(t)
	d.register("bar")
	d.expectNone(`PRIVMSG`, 300*time.Millisecond)
}

func TestReplayWithoutServerTime(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	h.d.Inject(rx(arveNum, 4, "one"))
	settle()
	c := h.dial(t)
	c.register("foo")
	c.expect(`^:AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?one$`)
}

func TestStalledClientIsDropped(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	a := h.dial(t)
	a.register("foo") // and never reads again
	b := h.dial(t)
	b.register("bar")
	body := strings.Repeat("x", 190)
	const n = 15000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range n {
			text := body
			if i == n-1 {
				text = "the last one"
			}
			h.d.Inject(rx(0x42, 0, text))
			if i%10 == 0 {
				time.Sleep(time.Millisecond) // as fast as a reader keeps up with
			}
		}
	}()
	b.expect(`:the last one$`)
	<-done
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.srv.mu.Lock()
		left := len(h.srv.clients)
		h.srv.mu.Unlock()
		if left == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d clients left", left)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMembershipPersists(t *testing.T) {
	d := withNodes(newFake())
	dir := t.TempDir()
	h := startHarness(t, d, testConfig(d, dir))
	c := h.dial(t)
	c.register("foo")
	d.Inject(rx(arveNum, 0, "hi"))
	c.expect(`PRIVMSG #LongFast :?hi$`)
	h.close()
	h2 := startHarness(t, d, testConfig(d, dir))
	c2 := h2.dial(t)
	c2.register("foo")
	c2.send("NAMES #LongFast")
	c2.expect(` 353 Ricco = #LongFast :Ricco AS02$`)
}
