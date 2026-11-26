package ircmesh

import (
	"context"
	"testing"
	"time"

	"meshtastik/internal/device"
)

func TestLinkDownAndUpNotices(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.DropLink()
	c.expect(`^:ircmesh NOTICE Ricco :mesh link down \(.+\); reconnecting$`)
	c.expect(`^:ircmesh NOTICE Ricco :mesh link back up$`)
}

func TestSendWhileDownRefused(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.Update(func() { h.d.RefuseDials = true })
	h.d.DropLink()
	c.expect(`mesh link down`)
	c.send("PRIVMSG #LongFast :x")
	c.expect(`NOTICE Ricco :mesh link is down; message not sent$`)
	h.d.Update(func() { h.d.RefuseDials = false })
	c.expect(`mesh link back up`)
	time.Sleep(200 * time.Millisecond)
	if got := h.d.Texts(); len(got) != 0 {
		t.Fatal("sent after the link came back:", got)
	}
}

func TestMembersKeptWhileDown(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.Update(func() { h.d.RefuseDials = true })
	h.d.DropLink()
	c.expect(`mesh link down`)
	c.send("NAMES #Ryfylke")
	c.expect(` 353 Ricco = #Ryfylke :Ricco AS02$`)
	c.expectNone(` QUIT `, 100*time.Millisecond)
}

func TestReopenAfterListenGivesUp(t *testing.T) {
	d := newFake()
	h := newHarness(t, d, func(c *Config) {
		open := c.Open
		c.Open = func(ctx context.Context, opts device.Options) (Mesh, error) {
			opts.ReconnectTimeout = 100 * time.Millisecond
			return open(ctx, opts)
		}
	})
	c := h.dial(t)
	c.register("foo")
	d.Update(func() { d.RefuseDials = true })
	d.DropLink()
	c.expect(`mesh link down`)
	time.Sleep(300 * time.Millisecond)
	d.Update(func() { d.RefuseDials = false })
	c.expect(`mesh link back up`)
	c.send("PRIVMSG #LongFast :after")
	if p := texts(t, d, 1)[0]; payload(p) != "after" {
		t.Fatal(payload(p))
	}
}

func TestChannelRenameOnNode(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.Update(func() { h.d.Channels[4].Settings.Name = "Fjord" })
	h.d.DropLink() // the re-handshake reads the new channels, as a refresh does
	c.expect(`^:Ricco!ab2c9b0a@mesh PART #Ryfylke :channel removed on node$`)
	c.expect(`^:Ricco!ab2c9b0a@mesh JOIN :?#Fjord$`)
	c.expect(` 353 Ricco = #Fjord :Ricco AS02$`)
}

func TestChannelRemovedOnNode(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	h.d.Update(func() { h.d.Channels[4].Role = 0 }) // DISABLED
	h.d.DropLink()
	c.expect(`^:Ricco!ab2c9b0a@mesh PART #Ryfylke :channel removed on node$`)
	c.expect(`mesh link back up`)
	c.send("JOIN #Ryfylke")
	c.expect(` 403 Ricco #Ryfylke `)
}
