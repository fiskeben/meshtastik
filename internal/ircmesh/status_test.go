package ircmesh

import (
	"regexp"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// reaction matches the TAGMSG that marks message id with emoji. It refers
// to id with both +reply, which the react spec pairs it with, and the older
// +draft/reply.
func reaction(emoji, id, rest string) string {
	id = regexp.QuoteMeta(id)
	return `^@\+draft/react=` + emoji + `;\+draft/reply=` + id + `;\+reply=` + id + rest
}

func TestDeliveredDMGetsCheckMark(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts = true
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.registerCaps("echo-message message-tags")
	c.send("PRIVMSG as02 :psst")
	id := c.expect(`^@msgid=(\S+) :Ricco!ab2c9b0a@mesh PRIVMSG AS02 :?psst$`)[1]
	c.expect(reaction("✅", id, ` :AS02!1bf0411d@mesh TAGMSG Ricco$`))
}

func TestUndeliveredDMGetsCrossAndNotice(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts, d.AckError = true, pb.Routing_NO_RESPONSE
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.registerCaps("echo-message message-tags")
	c.send("PRIVMSG as02 :are you there")
	id := c.expect(`^@msgid=(\S+) :Ricco!ab2c9b0a@mesh PRIVMSG AS02 `)[1]
	c.expect(`NOTICE Ricco :message to AS02 not delivered`)
	c.expect(reaction("❌", id, ` :Ricco!ab2c9b0a@mesh TAGMSG AS02$`))
}

func TestUnackedDMGetsCross(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.registerCaps("echo-message message-tags")
	c.send("PRIVMSG as02 :hello?")
	id := c.expect(`^@msgid=(\S+) :Ricco!ab2c9b0a@mesh PRIVMSG AS02 `)[1]
	c.expect(reaction("❌", id, ` :Ricco!ab2c9b0a@mesh TAGMSG AS02$`))
}

func TestNoReactionsWithoutMessageTags(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts = true
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG as02 :psst")
	texts(t, h.d, 1)
	c.expectNone(`TAGMSG`, 300*time.Millisecond)
}

func TestReactionReachesOtherClients(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts = true
	h := newHarness(t, d, nil)
	a := h.dial(t)
	a.register("foo")
	b := h.dial(t)
	b.registerCaps("message-tags")
	a.send("PRIVMSG as02 :psst")
	id := b.expect(`^@msgid=(\S+) :Ricco!ab2c9b0a@mesh PRIVMSG AS02 :?psst$`)[1]
	b.expect(reaction("✅", id, ` :AS02!1bf0411d@mesh TAGMSG Ricco$`))
}

func TestEveryPartGetsItsReaction(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts = true
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.registerCaps("echo-message message-tags")
	c.send("PRIVMSG as02 :%s", strings.TrimSpace(strings.Repeat("word ", 60)))
	first := c.expect(`^@msgid=(\S+) :Ricco!ab2c9b0a@mesh PRIVMSG AS02 :word`)[1]
	second := c.expect(`^@msgid=(\S+) :Ricco!ab2c9b0a@mesh PRIVMSG AS02 :word`)[1]
	c.expect(reaction("✅", first, ` `))
	c.expect(reaction("✅", second, ` `))
}

func TestChannelMessagesGetNoReaction(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.registerCaps("echo-message message-tags")
	c.send("PRIVMSG #Ryfylke :hei")
	texts(t, h.d, 1)
	c.expectNone(`TAGMSG`, 500*time.Millisecond)
}

func TestReactionReplayed(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts = true
	h := newHarness(t, d, nil)
	a := h.dial(t)
	a.registerCaps("echo-message message-tags")
	a.send("PRIVMSG as02 :psst")
	id := a.expect(`^@msgid=(\S+) `)[1]
	a.expect(`TAGMSG`)

	b := h.dial(t)
	b.registerCaps("message-tags server-time")
	b.expect(`msgid=` + regexp.QuoteMeta(id) + `;time=\S+ :Ricco!ab2c9b0a@mesh PRIVMSG AS02 :?psst$`)
	b.expect(reaction("✅", id, `;time=\S+ :AS02!1bf0411d@mesh TAGMSG Ricco$`))

	plain := h.dial(t)
	plain.register("bar")
	plain.expect(`PRIVMSG AS02 :?psst$`)
	plain.expectNone(`TAGMSG`, 300*time.Millisecond)
}

func TestReactionInChathistory(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts = true
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.registerCaps("echo-message " + allCaps)
	c.send("PRIVMSG as02 :psst")
	id := c.expect(`msgid=(\S+);time=\S+ :Ricco!ab2c9b0a@mesh PRIVMSG`)[1]
	c.expect(`TAGMSG`)
	c.send("CHATHISTORY LATEST as02 * 10")
	ref := c.expect(`BATCH \+(\S+) chathistory AS02$`)[1]
	c.expect(`^@batch=` + ref + `;msgid=` + regexp.QuoteMeta(id) + `;time=\S+ :Ricco!ab2c9b0a@mesh PRIVMSG AS02 :?psst$`)
	c.expect(reaction("✅", id, `;batch=`+ref+`;time=\S+ :AS02!1bf0411d@mesh TAGMSG Ricco$`))
	c.expect(`^:ircmesh BATCH -` + ref + `$`)
}

func TestReactionWithHistoryOff(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts = true
	h := newHarness(t, d, func(c *Config) { c.History = -1 })
	c := h.dial(t)
	c.registerCaps("echo-message message-tags")
	c.send("PRIVMSG as02 :psst")
	id := c.expect(`^@msgid=(\S+) `)[1]
	c.expect(reaction("✅", id, ` :AS02!1bf0411d@mesh TAGMSG Ricco$`))
}
