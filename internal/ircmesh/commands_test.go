package ircmesh

import (
	"strconv"
	"strings"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

func TestJoinUnknownChannel(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("JOIN #nope")
	c.expect(` 403 Ricco #nope `)
}

func TestPartAndRejoin(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PART #Ryfylke :bye")
	c.expect(`^:Ricco!ab2c9b0a@mesh PART #Ryfylke`)
	c.send("PART #Ryfylke")
	c.expect(` 442 Ricco #Ryfylke `)
	c.send("JOIN #ryfylke")
	c.expect(`^:Ricco!ab2c9b0a@mesh JOIN :?#Ryfylke$`)
	c.expect(` 366 Ricco #Ryfylke `)
	c.send("JOIN #Ryfylke")
	c.expectNone(` JOIN `, 300*time.Millisecond)
	c.send("JOIN 0")
	c.expect(` PART #LongFast`)
	c.expect(` PART #Ryfylke`)
}

func TestNamesFollowNodeChannel(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("NAMES #Ryfylke")
	c.expect(` 353 Ricco = #Ryfylke :Ricco AS02$`)
	c.send("NAMES #LongFast")
	c.expect(` 353 Ricco = #LongFast :?Ricco$`)
}

func TestWho(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("WHO #Ryfylke")
	c.expect(` 352 Ricco #Ryfylke ab2c9b0a mesh ircmesh Ricco H :0 Ricco$`)
	c.expect(` 352 Ricco #Ryfylke 1bf0411d mesh ircmesh AS02 H :0 Arve Tak$`)
	c.expect(` 315 Ricco #Ryfylke `)
	c.send("WHO OLD")
	c.expect(` 352 Ricco \* 00000005 mesh ircmesh OLD H :0 Old node$`)
	c.expect(` 315 Ricco OLD `)
}

func TestWhoisOfflineNode(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("WHOIS old")
	c.expect(` 311 Ricco OLD 00000005 mesh \* :Old node$`)
	c.expect(` 312 Ricco OLD ircmesh :last heard 1d16h ago$`)
	idle := c.expect(` 317 Ricco OLD (\d+) `)
	if n, _ := strconv.Atoi(idle[1]); n < 144000 {
		t.Fatal("idle", n)
	}
	c.expect(` 318 Ricco OLD `)
	c.send("WHOIS AS02")
	c.expect(` 311 Ricco AS02 1bf0411d mesh \* :Arve Tak$`)
	c.expect(` 319 Ricco AS02 :?#Ryfylke$`)
	c.expect(` 318 Ricco AS02 `)
}

func TestWhoisUnknown(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("WHOIS nobody")
	c.expect(` 401 Ricco nobody `)
	c.expect(` 318 Ricco nobody `)
}

func TestNickRefused(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("NICK bar")
	c.expect(` 484 Ricco :nicks follow the node's names`)
	c.send("USER a b c d")
	c.expect(` 462 Ricco `)
}

func TestListAndTopic(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("LIST")
	c.expect(` 321 Ricco `)
	c.expect(` 322 Ricco #LongFast 1 :channel 0 · primary`)
	c.expect(` 322 Ricco #Ryfylke 2 :channel 4 · secondary`)
	c.expect(` 323 Ricco `)
	c.send("TOPIC #Ryfylke")
	c.expect(` 332 Ricco #Ryfylke :channel 4 · secondary`)
	c.send("TOPIC #Ryfylke :x")
	c.expect(` 482 Ricco #Ryfylke `)
}

func TestModeReplies(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("MODE #LongFast")
	c.expect(` 324 Ricco #LongFast :?\+nt$`)
	c.send("MODE #LongFast b")
	c.expect(` 368 Ricco #LongFast `)
	c.send("MODE #LongFast +m")
	c.expect(` 482 Ricco #LongFast `)
	c.send("MODE Ricco")
	c.expect(` 221 Ricco :?\+i$`)
	c.send("MODE Ricco +i")
	c.expectNone(` 501 `, 200*time.Millisecond)
	c.send("MODE Ricco +x")
	c.expect(` 501 Ricco `)
	c.send("MODE AS02")
	c.expect(` 502 Ricco `)
}

func TestUserhostIsonAway(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.register("foo")
	c.send("USERHOST AS02 nobody")
	c.expect(` 302 Ricco :?AS02=\+1bf0411d@mesh$`)
	c.send("ISON AS02 OLD nobody Ricco")
	c.expect(` 303 Ricco :AS02 Ricco$`)
	c.send("AWAY :lunch")
	c.expect(` 306 Ricco `)
	c.send("AWAY")
	c.expect(` 305 Ricco `)
}

func TestLongNameCannotInjectLines(t *testing.T) {
	d := newFake()
	d.ExtraNodes = []*pb.NodeInfo{{Num: arveNum, Channel: 4, LastHeard: uint32(time.Now().Unix()),
		User: &pb.User{ShortName: "EVL", LongName: "x\r\n:evil!e@mesh PRIVMSG #Ryfylke :pwn"}}}
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.register("foo")
	c.send("WHO #Ryfylke")
	c.expect(` 352 Ricco #Ryfylke 1bf0411d mesh ircmesh EVL H :0 x:evil!e@mesh PRIVMSG #Ryfylke :pwn$`)
	c.send("WHOIS EVL")
	c.expect(` 311 Ricco EVL `)
	c.expect(` 318 `)
	for _, l := range c.seen {
		if strings.HasPrefix(l, ":evil") {
			t.Fatalf("injected line %q", l)
		}
	}
}
