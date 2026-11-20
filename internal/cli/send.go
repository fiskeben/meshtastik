package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"meshtastik/internal/device"
)

// errNoNameMatch means no node has the queried name.
var errNoNameMatch = errors.New("no name matches")

// resolveNode turns a --to value into a node number: "!" and 8 hex digits,
// a decimal number, or a unique short or long name (case-insensitive). A
// number that is not a listed node but is a node's name means that node.
func resolveNode(s *device.State, q string) (uint32, error) {
	if h, ok := strings.CutPrefix(q, "!"); ok && len(h) == 8 {
		if n, err := strconv.ParseUint(h, 16, 32); err == nil {
			return uint32(n), nil
		}
	}
	if n, err := strconv.ParseUint(q, 10, 32); err == nil {
		if _, listed := s.Nodes[uint32(n)]; listed {
			return uint32(n), nil
		}
		if num, err := resolveName(s, q); !errors.Is(err, errNoNameMatch) {
			return num, err
		}
		return uint32(n), nil
	}
	num, err := resolveName(s, q)
	if errors.Is(err, errNoNameMatch) {
		return 0, fmt.Errorf("no node matches %q; use a node id (!1234abcd), number or name", q)
	}
	return num, err
}

// resolveName finds the node whose short name, or else long name, is q.
func resolveName(s *device.State, q string) (uint32, error) {
	for _, short := range []bool{true, false} {
		var hits []uint32
		for num, n := range s.Nodes {
			name := n.GetUser().GetLongName()
			if short {
				name = n.GetUser().GetShortName()
			}
			if name != "" && strings.EqualFold(name, q) {
				hits = append(hits, num)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		}
		names := make([]string, len(hits))
		for i, num := range hits {
			names[i] = s.Nodes[num].GetUser().GetShortName()
			if names[i] == "" {
				names[i] = nodeID(num)
			}
		}
		sort.Strings(names)
		return 0, fmt.Errorf("%q is ambiguous: matches %s", q, strings.Join(names, ", "))
	}
	return 0, errNoNameMatch
}

func newSendCmd(a *app) *cobra.Command {
	var to string
	var channel int
	var ackTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "send <text>",
		Short: "Send a text message to the mesh",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ackTimeout <= 0 {
				return errors.New("--ack-timeout must be greater than 0")
			}
			if channel < 0 || channel > 7 {
				return fmt.Errorf("channel %d out of range 0-7", channel)
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			dest := uint32(device.Broadcast)
			label := ""
			if to != "" {
				st := c.State()
				if dest, err = resolveNode(st, to); err != nil {
					return err
				}
				label = nodeID(dest)
				if s := st.Nodes[dest].GetUser().GetShortName(); s != "" {
					label = s
				}
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			direct := dest != device.Broadcast
			if direct {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, ackTimeout)
				defer cancel()
			}
			if err := c.SendText(ctx, args[0], dest, uint32(channel), direct); err != nil {
				if errors.Is(err, device.ErrNoAck) {
					msg := fmt.Sprintf("no acknowledgement from %s within %s", label, ackTimeout)
					if errors.Is(err, device.ErrImplicitAck) {
						msg += " (the message was relayed by the mesh, but delivery is unconfirmed)"
					}
					return errors.New(msg)
				}
				return err
			}
			if direct {
				fmt.Fprintf(a.Stdout, "Acknowledged by %s.\n", label)
			} else {
				fmt.Fprintln(a.Stdout, "Sent.")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&to, "to", "", "destination node: !hex id, number, or name (default: broadcast)")
	f.IntVar(&channel, "channel", 0, "channel index 0-7")
	f.DurationVar(&ackTimeout, "ack-timeout", 30*time.Second, "how long to wait for a direct message's acknowledgement")
	return changes(cmd, "yes")
}
