package cli

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/device"
)

// maxAdminKeys is how many admin keys a node holds (security.admin_key).
const maxAdminKeys = 3

// keyLen is the length of a public key.
const keyLen = 32

// parseKeyRef turns "base64:<32 bytes>", plain base64 of 32 bytes, or a node
// reference (id, number or name) into a public key.
func parseKeyRef(st *device.State, ref string) ([]byte, error) {
	enc, prefixed := strings.CutPrefix(ref, "base64:")
	if !prefixed && len(ref) == base64.StdEncoding.EncodedLen(keyLen) { // no name is that long
		if _, err := base64.StdEncoding.DecodeString(ref); err == nil {
			enc, prefixed = ref, true
		}
	}
	if prefixed {
		b, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("invalid base64: %w", err)
		}
		if len(b) != keyLen {
			return nil, fmt.Errorf("key must be %d bytes, got %d", keyLen, len(b))
		}
		return b, nil
	}
	num, err := resolveNode(st, ref)
	if err != nil {
		return nil, err
	}
	k := st.Nodes[num].GetUser().GetPublicKey()
	if len(k) != keyLen {
		return nil, fmt.Errorf("%s has no known public key", nodeID(num))
	}
	return k, nil
}

// keyOwner is the node in st whose public key is k, if any.
func keyOwner(st *device.State, k []byte) (uint32, bool) {
	for num, n := range st.Nodes {
		if bytes.Equal(n.GetUser().GetPublicKey(), k) {
			return num, true
		}
	}
	return 0, false
}

// securityChange is the change that sets the node's admin keys to keys.
func securityChange(st *device.State, keys [][]byte) device.Change {
	sec := proto.Clone(st.Config.GetSecurity()).(*pb.Config_SecurityConfig)
	if sec == nil {
		sec = &pb.Config_SecurityConfig{}
	}
	sec.AdminKey = keys
	return device.Change{Config: &pb.Config{PayloadVariant: &pb.Config_Security{Security: sec}}}
}

func newAdminKeyCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin-key",
		Short: "Manage the public keys allowed to administer the node remotely",
		Long: "Manage security.admin_key: the public keys (at most 3) of the nodes allowed to administer this node\n" +
			"over the mesh. A key is base64:<32 bytes>, or a node whose public key the node list knows.\n" +
			"meshtastik info shows a node's own public key.",
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List the admin keys, naming the node each belongs to when known",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State()
			keys := st.Config.GetSecurity().GetAdminKey()
			if a.asJSON {
				type row struct {
					Index int     `json:"index"`
					Key   string  `json:"key"`
					Node  *string `json:"node"`
				}
				rows := make([]row, len(keys))
				for i, k := range keys {
					rows[i] = row{Index: i, Key: keyText(k)}
					if num, ok := keyOwner(st, k); ok {
						id := nodeID(num)
						rows[i].Node = &id
					}
				}
				return printJSON(a.Stdout, rows)
			}
			if len(keys) == 0 {
				_, err := fmt.Fprintln(a.Stdout, "no admin keys")
				return err
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "#\tKEY\tNODE")
			for i, k := range keys {
				node := "-"
				if num, ok := keyOwner(st, k); ok {
					node = nodeRef(st, num)
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\n", i, keyText(k), node)
			}
			return tw.Flush()
		},
	}
	add := &cobra.Command{
		Use:   "add <key|node>",
		Short: "Allow a key to administer the node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State()
			k, err := parseKeyRef(st, args[0])
			if err != nil {
				return err
			}
			keys := st.Config.GetSecurity().GetAdminKey()
			for _, have := range keys {
				if bytes.Equal(have, k) {
					_, err := fmt.Fprintln(a.Stdout, "already listed")
					return err
				}
			}
			if len(keys) >= maxAdminKeys {
				return fmt.Errorf("the node holds at most %d admin keys; remove one first", maxAdminKeys)
			}
			return applyChanges(cmd, a, c, []device.Change{securityChange(st, append(append([][]byte{}, keys...), k))})
		},
	}
	remove := &cobra.Command{
		Use:   "remove <key|node|index>",
		Short: "Stop a key from administering the node",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State()
			keys := st.Config.GetSecurity().GetAdminKey()
			drop := -1
			if i, err := strconv.Atoi(args[0]); err == nil && len(args[0]) == 1 {
				if i < 0 || i >= len(keys) {
					return fmt.Errorf("no admin key %d (the node has %d)", i, len(keys))
				}
				drop = i
			} else {
				k, err := parseKeyRef(st, args[0])
				if err != nil {
					return err
				}
				for i, have := range keys {
					if bytes.Equal(have, k) {
						drop = i
					}
				}
				if drop < 0 {
					return errors.New("that key is not one of the node's admin keys")
				}
			}
			rest := append(append([][]byte{}, keys[:drop]...), keys[drop+1:]...)
			return applyChanges(cmd, a, c, []device.Change{securityChange(st, rest)})
		},
	}
	cmd.AddCommand(list, add, remove)
	return cmd
}
