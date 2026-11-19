package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"github.com/spf13/cobra"

	"meshtastik/internal/device"
)

// errCountReached ends Listen once --count messages have been printed.
var errCountReached = errors.New("count reached")

// sanitise makes untrusted mesh text safe for a terminal: invalid UTF-8
// becomes U+FFFD, control characters other than tab are escaped (\xNN for
// C0 and DEL, \u00NN for C1), so text can never move the cursor, change
// colours or break the one-message-per-line output.
func sanitise(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r >= 0x80 && r <= 0x9f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nodeName is "Long (SHORT)", whichever of the two is set, or the node id.
func nodeName(s *device.State, num uint32) string {
	u := s.Nodes[num].GetUser()
	long, short := u.GetLongName(), u.GetShortName()
	switch {
	case long != "" && short != "":
		return sanitise(long + " (" + short + ")")
	case long != "":
		return sanitise(long)
	case short != "":
		return sanitise(short)
	}
	return nodeID(num)
}

// listenFilter decides which packets listen prints.
type listenFilter struct {
	all     bool
	channel int // -1: every channel
}

// shows reports whether p is printed: by default only text messages that are
// not replies to a request, with --all every packet.
func (f listenFilter) shows(p *pb.MeshPacket) bool {
	if f.channel >= 0 && p.GetChannel() != uint32(f.channel) {
		return false
	}
	if f.all {
		return true
	}
	d := p.GetDecoded()
	return d.GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP && d.GetRequestId() == 0
}

// packetTime is when p was received: its rx_time if set, else now.
func packetTime(p *pb.MeshPacket, now time.Time) time.Time {
	if t := p.GetRxTime(); t != 0 {
		return time.Unix(int64(t), 0)
	}
	return now
}

type listenJSON struct {
	Time      string  `json:"time"`
	From      string  `json:"from"`
	FromName  string  `json:"from_name"`
	To        string  `json:"to"`
	Channel   uint32  `json:"channel"`
	Port      string  `json:"port,omitempty"`
	Text      *string `json:"text,omitempty"`
	SNR       float32 `json:"snr"`
	RSSI      int32   `json:"rssi"`
	Encrypted bool    `json:"encrypted,omitempty"`
}

// encrypted reports whether p arrived undecoded (the node lacked its key).
func encrypted(p *pb.MeshPacket) bool {
	_, ok := p.GetPayloadVariant().(*pb.MeshPacket_Encrypted)
	return ok
}

func writeListenJSON(w io.Writer, s *device.State, p *pb.MeshPacket, now time.Time) error {
	d := p.GetDecoded()
	m := listenJSON{
		Time:      packetTime(p, now).UTC().Format(time.RFC3339),
		From:      nodeID(p.GetFrom()),
		FromName:  s.Nodes[p.GetFrom()].GetUser().GetLongName(),
		To:        nodeID(p.GetTo()),
		Channel:   p.GetChannel(),
		SNR:       p.GetRxSnr(),
		RSSI:      p.GetRxRssi(),
		Encrypted: encrypted(p),
	}
	if !m.Encrypted {
		m.Port = d.GetPortnum().String()
	}
	if p.GetTo() == device.Broadcast {
		m.To = "broadcast"
	}
	if d.GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP {
		t := strings.ToValidUTF8(string(d.GetPayload()), "\uFFFD")
		m.Text = &t
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil { // escapes C0 control characters
		return err
	}
	_, err := w.Write(escapeJSONControls(buf.Bytes()))
	return err
}

func isDELOrC1(r rune) bool { return r == 0x7f || r >= 0x80 && r <= 0x9f }

// escapeJSONControls replaces DEL and C1 control characters, which
// encoding/json leaves as they are, with \u escapes. In JSON output they can
// only occur inside strings, where the escape stands for the same character.
func escapeJSONControls(b []byte) []byte {
	if !bytes.ContainsFunc(b, isDELOrC1) {
		return b
	}
	var out bytes.Buffer
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if isDELOrC1(r) {
			fmt.Fprintf(&out, `\u%04x`, r)
		} else {
			out.Write(b[:n])
		}
		b = b[n:]
	}
	return out.Bytes()
}

// humanWriter prints one line per packet, padding the sender and recipient
// to the widest seen so far so that columns line up.
type humanWriter struct {
	w          io.Writer
	fromW, toW int
	maxPad     int
}

func (h *humanWriter) write(s *device.State, p *pb.MeshPacket, now time.Time) error {
	from := nodeName(s, p.GetFrom())
	var to string
	switch p.GetTo() {
	case device.Broadcast:
		to = "all"
	case s.MyInfo.GetMyNodeNum():
		to = "me"
	default:
		to = nodeName(s, p.GetTo())
	}
	var body string
	switch d := p.GetDecoded(); {
	case encrypted(p):
		body = "<encrypted>"
	case d.GetPortnum() == pb.PortNum_TEXT_MESSAGE_APP:
		body = sanitise(string(d.GetPayload()))
	default:
		body = "[" + d.GetPortnum().String() + "]"
	}
	h.fromW = min(max(h.fromW, utf8.RuneCountInString(from)), h.maxPad)
	h.toW = min(max(h.toW, utf8.RuneCountInString(to)), h.maxPad)
	_, err := fmt.Fprintf(h.w, "%s  ch%d  %s  → %s  : %s\n", packetTime(p, now).Format("15:04:05"),
		p.GetChannel(), pad(from, h.fromW), pad(to, h.toW), body)
	return err
}

// pad right-pads s with spaces to n runes.
func pad(s string, n int) string {
	if k := n - utf8.RuneCountInString(s); k > 0 {
		return s + strings.Repeat(" ", k)
	}
	return s
}

func newListenCmd(a *app) *cobra.Command {
	var channel, count int
	var all bool
	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Print received messages: the node's backlog, then new ones until Ctrl-C",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f := listenFilter{all: all, channel: -1}
			if cmd.Flags().Changed("channel") {
				if channel < 0 || channel > 7 {
					return fmt.Errorf("channel %d out of range 0-7", channel)
				}
				f.channel = channel
			}
			if cmd.Flags().Changed("count") && count < 1 {
				return errors.New("--count must be at least 1")
			}
			c, err := a.connectWith(cmd, func(o *device.Options) {
				o.OnReconnect = func(cause error, wait time.Duration) {
					fmt.Fprintf(a.Stderr, "connection lost (%v); reconnecting in %s\n", cause, wait)
				}
			})
			if err != nil {
				return err
			}
			defer c.Close()
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			st := c.State()
			fmt.Fprintf(a.Stderr, "Listening on %s — Ctrl-C to stop\n", nodeName(st, st.MyInfo.GetMyNodeNum()))
			hw := &humanWriter{w: a.Stdout, maxPad: 32}
			printed, dropped := 0, 0
			err = c.Listen(ctx, func(p *pb.MeshPacket) error {
				if n := c.DroppedPackets(); n > dropped {
					fmt.Fprintf(a.Stderr, "note: %d older packets were dropped (buffer full)\n", n-dropped)
					dropped = n
				}
				if !f.shows(p) {
					return nil
				}
				st := c.State() // names as currently known
				var err error
				if a.asJSON {
					err = writeListenJSON(a.Stdout, st, p, time.Now())
				} else {
					err = hw.write(st, p, time.Now())
				}
				if err != nil {
					return err
				}
				printed++
				if count > 0 && printed >= count {
					return errCountReached
				}
				return nil
			})
			if errors.Is(err, errCountReached) {
				return nil
			}
			return err
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&channel, "channel", 0, "only show messages on this channel index 0-7 (default: all channels)")
	fl.BoolVar(&all, "all", false, "also show other packet types (by port name), replies and encrypted packets")
	fl.IntVar(&count, "count", 0, "exit after printing this many messages (default: run until Ctrl-C)")
	return cmd
}
