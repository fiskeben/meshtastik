package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"meshtastik/internal/device"
)

// meshTarget connects and resolves the node argument of a mesh request,
// telling the user on stderr that the answer can take a while.
func meshTarget(cmd *cobra.Command, a *app, q string) (*device.Client, *device.State, uint32, error) {
	c, err := a.connect(cmd)
	if err != nil {
		return nil, nil, 0, err
	}
	st := c.State()
	to, err := resolveNode(st, q)
	if err != nil {
		c.Close()
		return nil, nil, 0, err
	}
	fmt.Fprintf(a.Stderr, "asking %s — this can take a while\n", nodeRef(st, to))
	return c, st, to, nil
}

// hopName is a hop's name for route output: "me" for this node.
func hopName(st *device.State, num uint32) string {
	if num == st.MyInfo.GetMyNodeNum() {
		return "me"
	}
	return nodeName(st, num)
}

// snrText formats an SNR in quarter dB steps as "6.25", "5.0".
func snrText(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func hopsText(st *device.State, hops []device.Hop) string {
	parts := make([]string, len(hops))
	for i, h := range hops {
		parts[i] = hopName(st, h.Num)
		switch {
		case i == 0:
		case h.SNR == nil:
			parts[i] += " ?"
		default:
			parts[i] += " " + snrText(*h.SNR) + " dB"
		}
	}
	return strings.Join(parts, " → ")
}

// writeRoute prints a traced route, one line per direction.
func writeRoute(w io.Writer, st *device.State, r *device.Route) {
	fmt.Fprintf(w, "towards:  %s\n", hopsText(st, r.Towards))
	if r.Back == nil {
		fmt.Fprintln(w, "back:     not reported")
		return
	}
	fmt.Fprintf(w, "back:     %s\n", hopsText(st, r.Back))
}

type hopJSON struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	SNR  *float64 `json:"snr"`
}

func hopsJSON(st *device.State, hops []device.Hop) []hopJSON {
	if hops == nil {
		return nil
	}
	out := make([]hopJSON, len(hops))
	for i, h := range hops {
		out[i] = hopJSON{ID: nodeID(h.Num), Name: hopName(st, h.Num), SNR: h.SNR}
	}
	return out
}

func newTracerouteCmd(a *app) *cobra.Command {
	var hopLimit uint32
	cmd := &cobra.Command{
		Use:   "traceroute <node>",
		Short: "Trace the route to a node and back, with the SNR of each hop",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("hop-limit") && (hopLimit < 1 || hopLimit > 7) {
				return fmt.Errorf("--hop-limit %d out of range 1-7", hopLimit)
			}
			c, st, to, err := meshTarget(cmd, a, args[0])
			if err != nil {
				return err
			}
			defer c.Close()
			if !cmd.Flags().Changed("hop-limit") {
				if hopLimit = st.Config.GetLora().GetHopLimit(); hopLimit == 0 {
					hopLimit = 3
				}
			}
			r, err := c.Traceroute(cmd.Context(), to, hopLimit)
			if err != nil {
				return err
			}
			st = c.State() // names learnt meanwhile
			if a.asJSON {
				return printJSON(a.Stdout, map[string]any{"towards": hopsJSON(st, r.Towards), "back": hopsJSON(st, r.Back)})
			}
			writeRoute(a.Stdout, st, r)
			return nil
		},
	}
	cmd.Flags().Uint32Var(&hopLimit, "hop-limit", 0, "how many hops the trace may take, 1-7 (default: the node's lora.hop_limit)")
	return cmd
}

// printProto prints m as JSON with proto field names, or with lines from
// text when not --json.
func printProto(a *app, m proto.Message, text func(io.Writer)) error {
	if a.asJSON {
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
		if err != nil {
			return err
		}
		return printRaw(a.Stdout, raw, true)
	}
	text(a.Stdout)
	return nil
}

func newRequestCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "request", Short: "Ask another node for its position or telemetry"}
	position := &cobra.Command{
		Use:   "position <node>",
		Short: "Ask a node for its position",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, to, err := meshTarget(cmd, a, args[0])
			if err != nil {
				return err
			}
			defer c.Close()
			p, err := c.RequestPosition(cmd.Context(), to)
			if err != nil {
				return err
			}
			return printProto(a, p, func(w io.Writer) {
				if !p.HasLatitudeI() && !p.HasLongitudeI() {
					fmt.Fprintln(w, "the node did not report a position")
					return
				}
				fmt.Fprintf(w, "latitude:   %.7f\n", float64(p.GetLatitudeI())*1e-7)
				fmt.Fprintf(w, "longitude:  %.7f\n", float64(p.GetLongitudeI())*1e-7)
				if p.HasAltitude() {
					fmt.Fprintf(w, "altitude:   %d m\n", p.GetAltitude())
				}
				if t := p.GetTime(); t != 0 {
					fmt.Fprintf(w, "time:       %s\n", time.Unix(int64(t), 0).Format(time.RFC3339))
				}
				if b := p.GetPrecisionBits(); b != 0 {
					fmt.Fprintf(w, "precision:  %d bits\n", b)
				}
			})
		},
	}
	var kind string
	telemetry := &cobra.Command{
		Use:   "telemetry <node> [--type device|environment|power|local-stats]",
		Short: "Ask a node for its telemetry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := device.ParseTelemetryKind(kind)
			if err != nil {
				return err
			}
			c, _, to, err := meshTarget(cmd, a, args[0])
			if err != nil {
				return err
			}
			defer c.Close()
			t, err := c.RequestTelemetry(cmd.Context(), to, k)
			if err != nil {
				return err
			}
			return printProto(a, t, func(w io.Writer) { writeTelemetry(w, t) })
		},
	}
	telemetry.Flags().StringVar(&kind, "type", "device", "device, environment, power or local-stats")
	cmd.AddCommand(position, telemetry)
	return cmd
}

// writeTelemetry prints the set fields of t's variant as "name: value".
func writeTelemetry(w io.Writer, t *pb.Telemetry) {
	r := t.ProtoReflect()
	f := r.WhichOneof(r.Descriptor().Oneofs().ByName("variant"))
	if f == nil {
		fmt.Fprintln(w, "the node sent no telemetry")
		return
	}
	n := 0
	r.Get(f).Message().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Enum() != nil {
			fmt.Fprintf(w, "%s: %s\n", fd.Name(), fd.Enum().Values().ByNumber(v.Enum()).Name())
		} else {
			fmt.Fprintf(w, "%s: %v\n", fd.Name(), v.Interface())
		}
		n++
		return true
	})
	if n == 0 {
		fmt.Fprintf(w, "no %s reported\n", f.Name())
	}
}
