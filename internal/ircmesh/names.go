// Package ircmesh serves a Meshtastic node as an IRC server: the node's
// channels are IRC channels, other nodes are IRC users, and the IRC user is
// the node itself.
package ircmesh

import (
	"cmp"
	"fmt"
	"strings"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"

	"meshtastik/internal/irc"
)

// The longest nick, advertised as NICKLEN: 16 when nicks are short names,
// 32 when they are long names (up to 39 bytes on the node).
const (
	nickLen     = 16
	longNickLen = 32
)

// nickLimit is the longest nick when nicks are long names or not.
func nickLimit(long bool) int {
	if long {
		return longNickLen
	}
	return nickLen
}

// hex4 is the last four hex digits of a node number.
func hex4(num uint32) string { return fmt.Sprintf("%04x", num&0xffff) }

// keep returns s with only the runes ok accepts.
func keep(s string, ok func(rune) bool) string {
	return strings.Map(func(r rune) rune {
		if ok(r) {
			return r
		}
		return -1
	}, s)
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func nickRune(r rune) bool { return isAlnum(r) || strings.ContainsRune("[]\\`_^{|}-", r) }

// cut shortens an ASCII string to n bytes.
func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// baseNick is the nick a node asks for: its short name, else its long name
// (spaces as _), or the other way round with long, else n and the last 4
// hex digits of its number, each stripped to what IRC allows.
func baseNick(u *pb.User, num uint32, long bool) string {
	short := keep(u.GetShortName(), nickRune)
	longName := keep(strings.ReplaceAll(u.GetLongName(), " ", "_"), nickRune)
	n := cmp.Or(short, longName)
	if long {
		n = cmp.Or(longName, short)
	}
	if n == "" {
		return "n" + hex4(num)
	}
	if c := n[0]; (c >= '0' && c <= '9') || c == '-' {
		n = "_" + n // a nick may not start with a digit or -
	}
	return cut(n, nickLimit(long))
}

// presetName is the modem preset as the apps show it (LONG_FAST: LongFast),
// or Custom when the node doesn't use a preset.
func presetName(lora *pb.Config_LoRaConfig) string {
	if !lora.GetUsePreset() {
		return "Custom"
	}
	var b strings.Builder
	for w := range strings.SplitSeq(lora.GetModemPreset().String(), "_") {
		if w != "" {
			b.WriteString(w[:1] + strings.ToLower(w[1:]))
		}
	}
	return b.String()
}

func enabled(ch *pb.Channel) bool {
	r := ch.GetRole()
	return r == pb.Channel_PRIMARY || r == pb.Channel_SECONDARY
}

// channelNames maps each enabled channel index to its IRC channel name.
func channelNames(chs [8]*pb.Channel, lora *pb.Config_LoRaConfig) map[int]string {
	out := map[int]string{}
	taken := map[string]bool{}
	for i, ch := range chs {
		if !enabled(ch) {
			continue
		}
		n := keep(ch.GetSettings().GetName(), func(r rune) bool { return isAlnum(r) || strings.ContainsRune("-_.", r) })
		switch {
		case n != "":
		case ch.GetRole() == pb.Channel_PRIMARY:
			n = presetName(lora)
		default:
			n = fmt.Sprintf("ch%d", i)
		}
		if taken[irc.Fold(n)] {
			n = fmt.Sprintf("%s-%d", n, i)
		}
		taken[irc.Fold(n)] = true
		out[i] = "#" + n
	}
	return out
}

// channelTopic describes a channel for TOPIC and LIST.
func channelTopic(ch *pb.Channel, lora *pb.Config_LoRaConfig) string {
	role := "secondary"
	if ch.GetRole() == pb.Channel_PRIMARY {
		role = "primary"
	}
	var key string
	switch psk := ch.GetSettings().GetPsk(); {
	case len(psk) == 0:
		key = "no encryption"
	case len(psk) == 1 && psk[0] == 1:
		key = "default key"
	case len(psk) == 1:
		key = "simple key"
	default:
		key = "custom key"
	}
	return fmt.Sprintf("channel %d · %s · %s · %s", ch.GetIndex(), role, presetName(lora), key)
}
