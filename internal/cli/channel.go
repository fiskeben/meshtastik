package cli

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/channelurl"
	"meshtastik/internal/configpath"
	"meshtastik/internal/device"
)

// maxChannelName is the firmware's limit for a channel name, in bytes.
const maxChannelName = 11

func newChannelCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "channel", Short: "Manage channels and channel URLs"}
	cmd.AddCommand(newChannelListCmd(a), newChannelAddCmd(a), newChannelSetCmd(a),
		newChannelDelCmd(a), newChannelURLCmd(a))
	return cmd
}

func checkChannelName(name string) error {
	if len(name) > maxChannelName {
		return fmt.Errorf("channel name %q is %d bytes; the limit is %d bytes", name, len(name), maxChannelName)
	}
	return nil
}

// parsePSK accepts none, default, random or base64:<key> (0, 1, 16 or 32 bytes).
func parsePSK(s string) ([]byte, error) {
	switch s {
	case "none":
		return nil, nil
	case "default":
		return []byte{1}, nil
	case "random":
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		return k, nil
	}
	enc, ok := strings.CutPrefix(s, "base64:")
	if !ok {
		return nil, fmt.Errorf("psk %q: use none, default, random or base64:<key>", s)
	}
	k, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, fmt.Errorf("psk: invalid base64: %w", err)
	}
	switch len(k) {
	case 0, 1, 16, 32:
		return k, nil
	}
	return nil, fmt.Errorf("psk is %d bytes; allowed lengths are 0, 1, 16 or 32 bytes", len(k))
}

func pskSummary(psk []byte) string {
	switch len(psk) {
	case 0:
		return "none"
	case 1:
		return "default"
	}
	return fmt.Sprintf("random (%d bytes)", len(psk))
}

func parseIndex(s string) (int, error) {
	i, err := strconv.Atoi(s)
	if err != nil || i < 0 || i > 7 {
		return 0, fmt.Errorf("channel index %q: expected a number 0-7", s)
	}
	return i, nil
}

func newChannelListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the node's channels",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State()
			if a.asJSON {
				rows := []map[string]any{}
				for _, ch := range st.Channels {
					if ch.GetRole() == pb.Channel_DISABLED {
						continue
					}
					s := ch.GetSettings()
					rows = append(rows, map[string]any{
						"index": ch.GetIndex(), "role": ch.GetRole().String(), "name": s.GetName(),
						"psk":            "base64:" + base64.StdEncoding.EncodeToString(s.GetPsk()),
						"uplink_enabled": s.GetUplinkEnabled(), "downlink_enabled": s.GetDownlinkEnabled(),
					})
				}
				return printJSON(a.Stdout, rows)
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "INDEX\tROLE\tNAME\tPSK\tUPLINK\tDOWNLINK")
			for _, ch := range st.Channels {
				if ch.GetRole() == pb.Channel_DISABLED {
					continue
				}
				s := ch.GetSettings()
				name := s.GetName()
				if name == "" {
					name = "(default)"
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%t\t%t\n", ch.GetIndex(), ch.GetRole(), name,
					pskSummary(s.GetPsk()), s.GetUplinkEnabled(), s.GetDownlinkEnabled())
			}
			return tw.Flush()
		},
	}
}

func newChannelAddCmd(a *app) *cobra.Command {
	var psk string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a secondary channel in the first free slot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkChannelName(args[0]); err != nil {
				return err
			}
			key, err := parsePSK(psk)
			if err != nil {
				return err
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			for i, ch := range c.State().Channels {
				if ch.GetRole() == pb.Channel_DISABLED {
					return applyChanges(cmd, a, c, []device.Change{{Channel: &pb.Channel{
						Index: int32(i), Role: pb.Channel_SECONDARY,
						Settings: &pb.ChannelSettings{Name: args[0], Psk: key},
					}}})
				}
			}
			return errors.New("no free channel slot; delete one first (channel del <index>)")
		},
	}
	cmd.Flags().StringVar(&psk, "psk", "random", "random, none, default or base64:<key> (0, 1, 16 or 32 bytes)")
	return cmd
}

func newChannelSetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "set <index> <field> <value>",
		Short:   "Set a channel settings field (e.g. name, psk, uplink_enabled, module_settings.position_precision)",
		Example: "  meshtastik channel set 1 name club\n  meshtastik channel set 1 psk random",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := parseIndex(args[0])
			if err != nil {
				return err
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			cur := c.State().Channels[idx]
			if cur.GetRole() == pb.Channel_DISABLED {
				return fmt.Errorf("channel %d is not in use; create it with channel add", idx)
			}
			ch := proto.Clone(cur).(*pb.Channel)
			if ch.Settings == nil {
				ch.Settings = &pb.ChannelSettings{}
			}
			if args[1] == "psk" {
				if ch.Settings.Psk, err = parsePSK(args[2]); err != nil {
					return err
				}
			} else if err := configpath.SetField(ch.Settings.ProtoReflect(), args[1], args[2]); err != nil {
				return fmt.Errorf("%s: %w", args[1], err)
			}
			if err := checkChannelName(ch.Settings.Name); err != nil {
				return err
			}
			return applyChanges(cmd, a, c, []device.Change{{Channel: ch}})
		},
	}
}

func newChannelDelCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "del <index>",
		Short: "Delete a secondary channel; later channels shift down",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := parseIndex(args[0])
			if err != nil {
				return err
			}
			if idx == 0 {
				return errors.New("cannot delete the primary channel (index 0)")
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			cur := c.State().Channels
			if cur[idx].GetRole() == pb.Channel_DISABLED {
				return fmt.Errorf("channel %d is not in use", idx)
			}
			var changes []device.Change
			for i := idx; i < len(cur); i++ {
				next := &pb.Channel{Index: int32(i), Role: pb.Channel_DISABLED}
				if i+1 < len(cur) && cur[i+1].GetRole() != pb.Channel_DISABLED {
					next = proto.Clone(cur[i+1]).(*pb.Channel)
					next.Index = int32(i)
				}
				changes = append(changes, device.Change{Channel: next})
			}
			return applyChanges(cmd, a, c, changes)
		},
	}
}

// sameChannel reports whether two channels carry the same role and settings.
func sameChannel(x, y *pb.Channel) bool {
	sx, sy := x.GetSettings(), y.GetSettings()
	if sx == nil {
		sx = &pb.ChannelSettings{}
	}
	if sy == nil {
		sy = &pb.ChannelSettings{}
	}
	return x.GetRole() == y.GetRole() && proto.Equal(sx, sy)
}

// channelSetChanges lays set out over all 8 slots and returns a Change for
// every slot that differs from cur.
func channelSetChanges(cur [8]*pb.Channel, set *pb.ChannelSet) []device.Change {
	var changes []device.Change
	for i, ch := range channelurl.ToChannels(set) {
		if !sameChannel(ch, cur[i]) {
			changes = append(changes, device.Change{Channel: ch})
		}
	}
	return changes
}

func newChannelURLCmd(a *app) *cobra.Command {
	var set string
	var noQR bool
	cmd := &cobra.Command{
		Use:   "url",
		Short: "Show the channel URL and QR code, or replace all channels from a URL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var newSet *pb.ChannelSet
			if cmd.Flags().Changed("set") {
				var err error
				if newSet, err = channelurl.Decode(set); err != nil {
					return err
				}
				if len(newSet.Settings) > 8 {
					return fmt.Errorf("URL has %d channels; a node holds at most 8", len(newSet.Settings))
				}
				for _, s := range newSet.Settings {
					if err := checkChannelName(s.GetName()); err != nil {
						return err
					}
				}
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State()
			if newSet == nil {
				u, err := channelurl.Encode(st.Channels, st.Config.GetLora())
				if err != nil {
					return err
				}
				if a.asJSON {
					return printJSON(a.Stdout, map[string]string{"url": u})
				}
				fmt.Fprintln(a.Stdout, u)
				if !noQR {
					qrterminal.GenerateHalfBlock(u, qrterminal.L, a.Stdout)
				}
				return nil
			}
			if err := confirmOrAbort(a, "Replace all channels and LoRa settings from this URL?"); err != nil {
				return err
			}
			changes := channelSetChanges(st.Channels, newSet)
			if newSet.LoraConfig != nil {
				changes = append(changes, device.Change{Config: &pb.Config{
					PayloadVariant: &pb.Config_Lora{Lora: newSet.LoraConfig},
				}})
			}
			return applyChanges(cmd, a, c, changes)
		},
	}
	cmd.Flags().StringVar(&set, "set", "", "replace all channels and LoRa settings from this URL")
	cmd.Flags().BoolVar(&noQR, "no-qr", false, "do not print the QR code")
	return cmd
}
