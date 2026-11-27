package ircmesh

import (
	"strings"
	"testing"
	"time"
)

func TestEchoMessage(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.registerCaps("echo-message message-tags")
	c.send("PRIVMSG #Ryfylke :hei")
	c.expect(`^@msgid=\S+ :Ricco!ab2c9b0a@mesh PRIVMSG #Ryfylke :?hei$`)
}

func TestEchoMessageDirect(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.registerCaps("echo-message")
	c.send("PRIVMSG as02 :psst")
	c.expect(`^:Ricco!ab2c9b0a@mesh PRIVMSG AS02 :?psst$`)
}

func TestNothingEchoedWhenNotSent(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.registerCaps("echo-message")
	c.send("PRIVMSG nobody :x")
	c.expect(` 401 `)
	c.send("NOTICE #LongFast :x")
	c.expectNone(`PRIVMSG|NOTICE #LongFast`, 300*time.Millisecond)
}

func TestLabeledEcho(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.registerCaps("echo-message labeled-response")
	c.send("@label=a1 PRIVMSG #Ryfylke :hei")
	c.expect(`^@label=a1 :Ricco!ab2c9b0a@mesh PRIVMSG #Ryfylke :?hei$`)
}

func TestLabeledError(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.registerCaps("labeled-response")
	c.send("@label=b2 PRIVMSG nobody :x")
	c.expect(`^@label=b2 :ircmesh 401 Ricco nobody `)
}

func TestLabeledNoReplyIsAcked(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.registerCaps("labeled-response")
	c.send("@label=c3 NOTICE #LongFast :x")
	c.expect(`^@label=c3 :ircmesh ACK$`)
}

func TestLabeledResponseBatched(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.registerCaps("batch echo-message labeled-response")
	c.send("@label=d4 PRIVMSG #LongFast :%s", strings.TrimSpace(strings.Repeat("word ", 90)))
	ref := c.expect(`^@label=d4 :ircmesh BATCH \+(\S+) labeled-response$`)[1]
	for range 3 {
		c.expect(`^@batch=` + ref + ` :Ricco!ab2c9b0a@mesh PRIVMSG #LongFast :word`)
	}
	c.expect(`^:ircmesh BATCH -` + ref + `$`)
}

func TestLabeledQuit(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.registerCaps("labeled-response")
	c.send("@label=e5 QUIT")
	c.expect(`^@label=e5 ERROR :Closing link`)
	if !c.closed() {
		t.Fatal("connection not closed")
	}
}

func TestLabelIgnoredWithoutCap(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.registerCaps("echo-message")
	c.send("@label=f6 PRIVMSG #Ryfylke :hei")
	c.expect(`^:Ricco!ab2c9b0a@mesh PRIVMSG #Ryfylke :?hei$`)
}
