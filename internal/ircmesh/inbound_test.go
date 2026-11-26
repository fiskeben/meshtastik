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

	// b keeps reading throughout, as a healthy client does.
	gotLast := make(chan struct{})
	go func() {
		for {
			l, ok := b.line(30 * time.Second)
			if !ok {
				return
			}
			if strings.HasSuffix(l, ":the last one") {
				close(gotLast)
				return
			}
		}
	}()

	clients := func() int {
		h.srv.mu.Lock()
		defer h.srv.mu.Unlock()
		return len(h.srv.clients)
	}
	// How much a client that stopped reading absorbs before the server's
	// writes block depends on the OS's socket buffers: under 2 MB on macOS
	// loopback, over 4 MB on Linux. So send until a is dropped rather than
	// a fixed amount, up to far more than any OS buffers.
	body := strings.Repeat("x", 190)
	const maxLines = 100_000 // ~23 MB
	for i := 0; ; i++ {
		if i%100 == 0 && clients() == 1 {
			break
		}
		if i == maxLines {
			t.Fatalf("stalled client still connected after %d lines", i)
		}
		h.d.Inject(rx(0x42, 0, body))
		if i%10 == 0 {
			time.Sleep(time.Millisecond) // as fast as a reader keeps up with
		}
	}

	h.d.Inject(rx(0x42, 0, "the last one"))
	select {
	case <-gotLast:
	case <-time.After(10 * time.Second):
		t.Fatal("the reading client missed the last line, or was dropped instead")
	}
	if !a.closed() {
		t.Error("the stalled client's connection is still open")
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

func TestReplyQuoteCannotInjectLines(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	orig := rx(arveNum, 4, "hi\r\nNOTICE Ricco :injected")
	h.d.Inject(orig)
	reply := rx(arveNum, 4, "ok")
	reply.GetDecoded().ReplyId = orig.Id
	h.d.Inject(reply)
	react := rx(arveNum, 4, "👍")
	react.GetDecoded().Emoji, react.GetDecoded().ReplyId = 1, orig.Id
	h.d.Inject(react)
	c.expect(`PRIVMSG #Ryfylke :↪ "hi NOTICE Ricco :injected" ok$`)
	c.expect(`reacted 👍 to "hi NOTICE Ricco :injected"`)
	for _, l := range c.seen {
		if strings.HasPrefix(l, "NOTICE") {
			t.Fatalf("injected line %q", l)
		}
	}
}

func TestMeshCTCPIsDefused(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.Inject(rx(arveNum, 4, "\x01DCC SEND evil 1 2 3\x01"))
	c.expect("PRIVMSG #Ryfylke :DCC SEND evil 1 2 3$")
}

func TestTextWithoutRxTimeStillJoins(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	p := rx(arveNum, 0, "no clock")
	p.RxTime = nil
	h.d.Inject(p)
	c.expect(`^:AS02!1bf0411d@mesh JOIN :?#LongFast$`)
	c.expect(`PRIVMSG #LongFast :no clock$`)
}

func TestRenewedMembershipIsSaved(t *testing.T) {
	d := withNodes(newFake())
	dir := t.TempDir()
	clk := &clock{}
	cfg := testConfig(d, dir)
	cfg.Now = clk.now
	run := func(text string) {
		h := startHarness(t, d, cfg)
		c := h.dial(t)
		c.register("foo")
		d.Inject(rx(arveNum, 0, text))
		c.expect(`PRIVMSG #LongFast :?` + text + `$`)
		h.close()
	}
	run("first") // joins #LongFast; saved
	clk.advance(20 * time.Hour)
	run("again")                // renews the membership only
	clk.advance(10 * time.Hour) // 30 h after the first, 10 h after the last
	h := startHarness(t, d, cfg)
	h.srv.age()
	c := h.dial(t)
	c.register("foo")
	c.send("NAMES #LongFast")
	c.expect(` 353 Ricco = #LongFast :Ricco AS02$`)
}

func TestMembersSurviveOutage(t *testing.T) {
	clk := &clock{}
	h := newHarness(t, withNodes(newFake()), func(c *Config) { c.Now = clk.now })
	c := h.dial(t)
	c.register("foo")
	h.d.Inject(rx(arveNum, 0, "hi"))
	c.expect(`PRIVMSG #LongFast :?hi$`)
	h.d.Update(func() { h.d.RefuseDials = true })
	h.d.DropLink()
	c.expect(`mesh link down`)
	clk.advance(30 * time.Hour)
	h.srv.age()
	h.d.Update(func() { h.d.RefuseDials = false })
	c.expect(`mesh link back up`)
	h.srv.age()
	c.expectNone(` (QUIT|PART) `, 200*time.Millisecond)
	c.send("NAMES #LongFast")
	c.expect(` 353 Ricco = #LongFast :Ricco AS02$`)
}
