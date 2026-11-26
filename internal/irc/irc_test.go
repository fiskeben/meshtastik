package irc

import (
	"reflect"
	"strings"
	"testing"
)

var parseCases = []struct {
	line string
	want Message
}{
	{`@time=x;a=b\:c :n!u@h PRIVMSG #c :hello world`, Message{
		Tags: map[string]string{"time": "x", "a": "b;c"}, Prefix: "n!u@h", Command: "PRIVMSG", Params: []string{"#c", "hello world"}}},
	{"ping x", Message{Command: "PING", Params: []string{"x"}}},
	{"NICK foo", Message{Command: "NICK", Params: []string{"foo"}}},
	{"USER u 0 * :Real Name", Message{Command: "USER", Params: []string{"u", "0", "*", "Real Name"}}},
	{"PRIVMSG #c :", Message{Command: "PRIVMSG", Params: []string{"#c", ""}}},
	{"PRIVMSG #c ::-)", Message{Command: "PRIVMSG", Params: []string{"#c", ":-)"}}},
}

func TestParse(t *testing.T) {
	for _, c := range parseCases {
		got, err := Parse(c.line)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) = %#v, %v; want %#v", c.line, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "   ", ":prefix-only", "@a=b"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q): no error", bad)
		}
	}
}

func TestStringRoundTrip(t *testing.T) {
	for _, c := range parseCases {
		got, err := Parse(c.want.String())
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("round trip of %#v: %q gives %#v, %v", c.want, c.want.String(), got, err)
		}
	}
	if s := (Message{Command: "PRIVMSG", Params: []string{"#c", ""}}).String(); s != "PRIVMSG #c :" {
		t.Errorf("got %q", s)
	}
	if s := (Message{Prefix: "ircmesh", Command: "001", Params: []string{"Ricco", "Welcome"}}).String(); s != ":ircmesh 001 Ricco Welcome" {
		t.Errorf("got %q", s)
	}
}

func TestStripFormatting(t *testing.T) {
	if got := StripFormatting("\x02bold\x02 \x0304,12red\x03 \x04ff0000hex \x1ditalic\x0f"); got != "bold red hex italic" {
		t.Errorf("got %q", got)
	}
	if got := StripFormatting("\x0312 is twelve"); got != " is twelve" {
		t.Errorf("got %q", got)
	}
}

func TestCTCP(t *testing.T) {
	if c, a, ok := ParseCTCP("\x01ACTION waves\x01"); !ok || c != "ACTION" || a != "waves" {
		t.Error(c, a, ok)
	}
	if c, a, ok := ParseCTCP("\x01VERSION"); !ok || c != "VERSION" || a != "" {
		t.Error(c, a, ok)
	}
	if _, _, ok := ParseCTCP("plain"); ok {
		t.Error("plain is not CTCP")
	}
	if s := CTCP("ACTION", "waves"); s != "\x01ACTION waves\x01" {
		t.Errorf("got %q", s)
	}
	if s := CTCP("VERSION", ""); s != "\x01VERSION\x01" {
		t.Errorf("got %q", s)
	}
}

func TestToUTF8Latin1(t *testing.T) {
	if got := ToUTF8("caf\xe9"); got != "café" {
		t.Errorf("got %q", got)
	}
	if got := ToUTF8("café 🥸"); got != "café 🥸" {
		t.Errorf("got %q", got)
	}
}

func TestFold(t *testing.T) {
	if got := Fold("Ab[]Ø"); got != "ab[]Ø" {
		t.Errorf("got %q", got)
	}
}

func TestStringNeverBreaksLines(t *testing.T) {
	m := Message{Prefix: "a\r\nb", Command: "PRIVMSG", Params: []string{"x y", ":z", "", "end\r\nNOTICE x :evil\x00"}}
	s := m.String()
	if strings.ContainsAny(s, "\r\n\x00") {
		t.Fatalf("%q", s)
	}
	got, err := Parse(s)
	if err != nil || len(got.Params) != 4 || got.Command != "PRIVMSG" {
		t.Fatalf("%q parses as %#v, %v", s, got, err)
	}
}
