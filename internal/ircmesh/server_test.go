package ircmesh

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestRegistration(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.send("NICK foo")
	c.send("USER u 0 * :r")
	c.expect(`^:ircmesh 001 Ricco :`)
	c.expect(`^:ircmesh 002 Ricco `)
	c.expect(`^:ircmesh 003 Ricco `)
	c.expect(`^:ircmesh 004 Ricco ircmesh test `)
	c.expect(`^:ircmesh 005 Ricco .*NICKLEN=16.*CASEMAPPING=ascii`)
	c.expect(`^:ircmesh 375 Ricco `)
	c.expect(`^:ircmesh 372 Ricco :- node Ricco \(!ab2c9b0a\)`)
	c.expect(`^:ircmesh 376 Ricco `)
	c.expect(`^:foo!\S+ NICK :?Ricco$`)
}

func TestRegistrationWithTheRightNickHasNoNickChange(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.send("NICK ricco")
	c.send("USER u 0 * :r")
	c.expect(` 376 `)
	c.expectNone(` NICK `, 300*time.Millisecond)
}

func TestCapServerTime(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.send("CAP LS 302")
	c.expect(`^:ircmesh CAP \* LS :batch draft/chathistory message-tags server-time$`)
	c.send("NICK foo")
	c.send("USER u 0 * :r")
	c.expectNone(` 001 `, 300*time.Millisecond)
	c.send("CAP REQ :sasl")
	c.expect(`^:ircmesh CAP \* NAK :?sasl$`)
	c.send("CAP REQ :server-time")
	c.expect(`^:ircmesh CAP \* ACK :?server-time$`)
	c.send("CAP END")
	c.expect(` 001 Ricco `)
}

func TestPassword(t *testing.T) {
	h := newHarness(t, newFake(), func(c *Config) { c.Password = "pw" })
	for _, pass := range []string{"", "wrong"} {
		c := h.dial(t)
		if pass != "" {
			c.send("PASS %s", pass)
		}
		c.send("NICK foo")
		c.send("USER u 0 * :r")
		c.expect(` 464 `)
		c.expect(`^ERROR `)
		if !c.closed() {
			t.Fatalf("PASS %q: still open", pass)
		}
	}
	c := h.dial(t)
	c.send("PASS pw")
	c.register("foo")
}

func TestAutoJoin(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.send("NICK foo")
	c.send("USER u 0 * :r")
	c.expect(`^:Ricco!ab2c9b0a@mesh JOIN :?#LongFast$`)
	c.expect(` 332 Ricco #LongFast :channel 0 · primary · LongFast · default key$`)
	c.expect(` 353 Ricco = #LongFast :?Ricco$`)
	c.expect(` 366 Ricco #LongFast `)
	c.expect(`^:Ricco!ab2c9b0a@mesh JOIN :?#Ryfylke$`)
	c.expect(` 332 Ricco #Ryfylke :channel 4 · secondary · LongFast · custom key$`)
	c.expect(` 353 Ricco = #Ryfylke :Ricco AS02$`)
	c.expect(` 366 Ricco #Ryfylke `)
}

func TestLongNicks(t *testing.T) {
	d := withNodes(newFake())
	d.Owner.ShortName, d.Owner.LongName = "RF", "Ricco F"
	h := newHarness(t, d, func(c *Config) { c.LongNicks = true })
	c := h.dial(t)
	c.send("NICK foo")
	c.send("USER u 0 * :r")
	c.expect(` 005 Ricco_F .*NICKLEN=32 `)
	c.expect(` 353 Ricco_F = #Ryfylke :Ricco_F Arve_Tak$`)
}

func TestShortNicksByDefault(t *testing.T) {
	d := withNodes(newFake())
	d.Owner.ShortName, d.Owner.LongName = "RF", "Ricco F"
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.send("NICK foo")
	c.send("USER u 0 * :r")
	c.expect(` 005 RF .*NICKLEN=16 `)
	c.expect(` 353 RF = #Ryfylke :RF AS02$`)
}

func TestPingPong(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PING :x")
	c.expect(`^:ircmesh PONG ircmesh :?x$`)
}

func TestUnknownCommand(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("FOO bar")
	c.expect(` 421 Ricco FOO `)
}

func TestCommandBeforeRegistration(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.send("JOIN #LongFast")
	c.expect(` 451 \* `)
}

func TestQuit(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("QUIT :bye")
	c.expect(`^ERROR `)
	if !c.closed() {
		t.Fatal("still open")
	}
}

func TestFirstOpenFailureIsFatal(t *testing.T) {
	d := newFake()
	d.RefuseDials = true
	cfg := testConfig(d, t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := New(cfg).Run(ctx, ln); err == nil || ctx.Err() != nil {
		t.Fatal("Run returned", err)
	}
	if _, err := net.Dial("tcp", ln.Addr().String()); err == nil {
		t.Fatal("listener still accepting")
	}
}

func TestOnReadyAfterConnecting(t *testing.T) {
	ready := make(chan bool, 1)
	h := newHarness(t, newFake(), func(c *Config) {
		c.OnReady = func() { ready <- true }
	})
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("OnReady not called")
	}
	h.dial(t).register("foo")
}
