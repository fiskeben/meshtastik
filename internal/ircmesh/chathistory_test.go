package ircmesh

import (
	"regexp"
	"testing"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"google.golang.org/protobuf/proto"
)

// registerCaps signs on as foo after requesting caps, and waits for the end
// of the welcome burst.
func (c *ircClient) registerCaps(caps string) {
	c.t.Helper()
	c.send("CAP LS 302")
	c.send("CAP REQ :%s", caps)
	c.expect(`^:ircmesh CAP \* ACK `)
	c.send("NICK foo")
	c.send("USER u 0 * :r")
	c.send("CAP END")
	c.expect(` 366 Ricco #Ryfylke `)
}

const allCaps = "batch draft/chathistory message-tags server-time"

func TestHistoryReplayedToEveryClient(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	h.d.Inject(rx(arveNum, 4, "one"))
	p := rx(arveNum, 0, "two")
	p.To = selfNum
	h.d.Inject(p)
	settle()
	c := h.dial(t)
	c.registerCaps("server-time")
	c.expect(`^@time=\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z :AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?one$`)
	c.expect(`^@time=\S+ :AS02!1bf0411d@mesh PRIVMSG Ricco :?two$`)
	h.d.Inject(rx(arveNum, 4, "three")) // while c is attached
	c.expect(`PRIVMSG #Ryfylke :?three$`)
	d := h.dial(t)
	d.register("bar")
	d.expect(`^:AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?one$`)
	d.expect(`^:AS02!1bf0411d@mesh PRIVMSG Ricco :?two$`)
	d.expect(`^:AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?three$`)
}

func TestReplayWithoutServerTime(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	h.d.Inject(rx(arveNum, 4, "one"))
	settle()
	c := h.dial(t)
	c.register("foo")
	c.expect(`^:AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?one$`)
}

func TestOwnMessagesReplayed(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts = true
	h := newHarness(t, d, nil)
	c := h.dial(t)
	c.register("foo")
	c.send("PRIVMSG #Ryfylke :hello all")
	c.send("PRIVMSG AS02 :hello you")
	texts(t, h.d, 2)
	e := h.dial(t)
	e.register("bar")
	e.expect(`^:Ricco!ab2c9b0a@mesh PRIVMSG #Ryfylke :hello all$`)
	e.expect(`^:Ricco!ab2c9b0a@mesh PRIVMSG AS02 :hello you$`)
}

func TestReplayUsesCurrentNick(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	h.d.Inject(rx(arveNum, 4, "before the rename"))
	b, _ := proto.Marshal(&pb.User{ShortName: "AT", LongName: "Arve Tak"})
	p := rx(arveNum, 4, "")
	p.GetDecoded().Portnum, p.GetDecoded().Payload = pb.PortNum_NODEINFO_APP, b
	h.d.Inject(p)
	settle()
	c := h.dial(t)
	c.register("foo")
	c.expect(`^:AT!1bf0411d@mesh PRIVMSG #Ryfylke :before the rename$`)
}

func TestHistoryOff(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), func(c *Config) { c.History = -1 })
	h.d.Inject(rx(arveNum, 4, "one"))
	settle()
	c := h.dial(t)
	c.register("foo")
	c.expectNone(`PRIVMSG`, 300*time.Millisecond)
}

func TestHistorySizeConfigurable(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), func(c *Config) { c.History = 2 })
	for _, s := range []string{"one", "two", "three"} {
		h.d.Inject(rx(arveNum, 4, s))
	}
	settle()
	c := h.dial(t)
	c.register("foo")
	c.expect(`PRIVMSG #Ryfylke :?two$`)
	c.expect(`PRIVMSG #Ryfylke :?three$`)
	if regexpMatch(`:?one$`, c.seen) {
		t.Fatal("kept more than 2:", c.seen)
	}
}

func TestChathistoryClientGetsNoReplay(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	h.d.Inject(rx(arveNum, 4, "one"))
	settle()
	c := h.dial(t)
	c.registerCaps(allCaps)
	c.expectNone(`PRIVMSG`, 300*time.Millisecond)
}

func TestIsupportAdvertisesChathistory(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.send("NICK foo")
	c.send("USER u 0 * :r")
	c.expect(`^:ircmesh 005 Ricco .*CHATHISTORY=100 .*MSGREFTYPES=msgid,timestamp`)
}

func TestLiveMessagesCarryMsgidAndTime(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.registerCaps("message-tags server-time")
	h.d.Inject(rx(arveNum, 4, "live"))
	c.expect(`^@msgid=\S+;time=\S+Z :AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?live$`)
}

func TestChathistoryLatest(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	for _, s := range []string{"one", "two", "three"} {
		h.d.Inject(rx(arveNum, 4, s))
	}
	settle()
	c := h.dial(t)
	c.registerCaps(allCaps)
	c.send("CHATHISTORY LATEST #ryfylke * 2")
	id := c.expect(`^:ircmesh BATCH \+(\S+) chathistory #Ryfylke$`)[1]
	c.expect(`^@batch=` + id + `;msgid=\S+;time=\S+ :AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?two$`)
	c.expect(`^@batch=` + id + `;msgid=\S+;time=\S+ :AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?three$`)
	c.expect(`^:ircmesh BATCH -` + id + `$`)
	if regexpMatch(`:?one$`, c.seen) {
		t.Fatal("limit not applied:", c.seen)
	}
}

func TestChathistoryBeforeMsgid(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	for _, s := range []string{"one", "two", "three"} {
		h.d.Inject(rx(arveNum, 4, s))
	}
	settle()
	c := h.dial(t)
	c.registerCaps(allCaps)
	c.send("CHATHISTORY LATEST #Ryfylke * 1")
	msgid := c.expect(`msgid=(\S+);time=\S+ \S+ PRIVMSG #Ryfylke :?three$`)[1]
	c.expect(`BATCH -`)
	c.send("CHATHISTORY BEFORE #Ryfylke msgid=%s 10", msgid)
	c.expect(`BATCH \+\S+ chathistory #Ryfylke$`)
	c.expect(`PRIVMSG #Ryfylke :?one$`)
	c.expect(`PRIVMSG #Ryfylke :?two$`)
	c.expect(`BATCH -`)
}

func TestChathistoryAfterTimestamp(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	h.d.Inject(rx(arveNum, 4, "old"))
	settle()
	c := h.dial(t)
	c.registerCaps(allCaps)
	ts := time.Now().UTC().Format(serverTimeFormat)
	time.Sleep(5 * time.Millisecond)
	h.d.Inject(rx(arveNum, 4, "new"))
	c.expect(`PRIVMSG #Ryfylke :?new$`)
	c.send("CHATHISTORY AFTER #Ryfylke timestamp=%s 10", ts)
	c.expect(`BATCH \+\S+ chathistory #Ryfylke$`)
	c.expect(`PRIVMSG #Ryfylke :?new$`)
	c.expect(`BATCH -`)
	if regexpMatch(`PRIVMSG #Ryfylke :?old$`, c.seen) {
		t.Fatal("got the older message:", c.seen)
	}
}

func TestChathistoryDirectMessages(t *testing.T) {
	d := withNodes(newFake())
	d.AckTexts = true
	h := newHarness(t, d, nil)
	p := rx(arveNum, 0, "psst")
	p.To = selfNum
	h.d.Inject(p)
	settle()
	c := h.dial(t)
	c.registerCaps(allCaps)
	c.send("PRIVMSG AS02 :yes?")
	texts(t, h.d, 1)
	c.send("CHATHISTORY LATEST as02 * 10")
	c.expect(`BATCH \+\S+ chathistory AS02$`)
	c.expect(` :AS02!1bf0411d@mesh PRIVMSG Ricco :?psst$`)
	c.expect(` :Ricco!ab2c9b0a@mesh PRIVMSG AS02 :?yes\?$`)
	c.expect(`BATCH -`)
}

func TestChathistoryEmptyBatch(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.registerCaps(allCaps)
	c.send("CHATHISTORY LATEST #Ryfylke * 10")
	id := c.expect(`BATCH \+(\S+) chathistory #Ryfylke$`)[1]
	c.expect(`^:ircmesh BATCH -` + id + `$`)
	c.send("CHATHISTORY BEFORE #Ryfylke msgid=unknown 10")
	id = c.expect(`BATCH \+(\S+) chathistory #Ryfylke$`)[1]
	c.expect(`^:ircmesh BATCH -` + id + `$`)
}

func TestChathistoryWithoutBatch(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	h.d.Inject(rx(arveNum, 4, "one"))
	settle()
	c := h.dial(t)
	c.registerCaps("draft/chathistory")
	c.send("CHATHISTORY LATEST #Ryfylke * 10")
	c.expect(`^:AS02!1bf0411d@mesh PRIVMSG #Ryfylke :?one$`)
	if regexpMatch(`BATCH`, c.seen) {
		t.Fatal("batch sent to a client without the batch cap:", c.seen)
	}
}

func TestChathistoryErrors(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	c := h.dial(t)
	c.registerCaps(allCaps)
	for _, tc := range []struct{ send, want string }{
		{"CHATHISTORY FOO #Ryfylke * 10", `^:ircmesh FAIL CHATHISTORY INVALID_PARAMS FOO :Unknown command$`},
		{"CHATHISTORY LATEST #Ryfylke *", `^:ircmesh FAIL CHATHISTORY INVALID_PARAMS LATEST :Insufficient parameters$`},
		{"CHATHISTORY", `^:ircmesh FAIL CHATHISTORY INVALID_PARAMS \* :Insufficient parameters$`},
		{"CHATHISTORY LATEST #nope * 10", `^:ircmesh FAIL CHATHISTORY INVALID_TARGET LATEST #nope :Messages could not be retrieved$`},
		{"CHATHISTORY LATEST nobody * 10", `^:ircmesh FAIL CHATHISTORY INVALID_TARGET LATEST nobody :Messages could not be retrieved$`},
		{"CHATHISTORY LATEST Ricco * 10", `^:ircmesh FAIL CHATHISTORY INVALID_TARGET LATEST Ricco :Messages could not be retrieved$`},
		{"CHATHISTORY LATEST #Ryfylke * 0", `^:ircmesh FAIL CHATHISTORY INVALID_PARAMS LATEST 0 :Invalid limit$`},
		{"CHATHISTORY BEFORE #Ryfylke * 10", `^:ircmesh FAIL CHATHISTORY INVALID_PARAMS BEFORE \* :Invalid message reference$`},
		{"CHATHISTORY BEFORE #Ryfylke timestamp=yesterday 10", `^:ircmesh FAIL CHATHISTORY INVALID_PARAMS BEFORE timestamp=yesterday :Invalid message reference$`},
		{"CHATHISTORY TARGETS msgid=a msgid=b 10", `^:ircmesh FAIL CHATHISTORY INVALID_PARAMS TARGETS msgid=a :Invalid timestamp$`},
	} {
		c.send("%s", tc.send)
		c.expect(tc.want)
	}
}

func TestChathistoryTargets(t *testing.T) {
	h := newHarness(t, withNodes(newFake()), nil)
	from := time.Now().UTC().Add(-time.Second).Format(serverTimeFormat)
	h.d.Inject(rx(arveNum, 4, "in the channel"))
	settle()
	p := rx(arveNum, 0, "in private")
	p.To = selfNum
	h.d.Inject(p)
	settle()
	to := time.Now().UTC().Add(time.Second).Format(serverTimeFormat)
	c := h.dial(t)
	c.registerCaps(allCaps)
	c.send("CHATHISTORY TARGETS timestamp=%s timestamp=%s 10", from, to)
	id := c.expect(`^:ircmesh BATCH \+(\S+) draft/chathistory-targets$`)[1]
	c.expect(`^@batch=` + id + ` :ircmesh CHATHISTORY TARGETS #Ryfylke \S+Z$`)
	c.expect(`^@batch=` + id + ` :ircmesh CHATHISTORY TARGETS AS02 \S+Z$`)
	c.expect(`^:ircmesh BATCH -` + id + `$`)
}

func TestTagmsgIgnored(t *testing.T) {
	h := newHarness(t, newFake(), nil)
	c := h.dial(t)
	c.registerCaps("message-tags")
	c.send("@+typing=active TAGMSG #Ryfylke")
	c.expectNone(` 421 `, 300*time.Millisecond)
}

// regexpMatch reports whether any of lines matches re.
func regexpMatch(re string, lines []string) bool {
	rx := regexp.MustCompile(re)
	for _, l := range lines {
		if rx.MatchString(l) {
			return true
		}
	}
	return false
}
