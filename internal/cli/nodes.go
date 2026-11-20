package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"meshtastik/internal/device"
)

// nodeRef is "!0000002a (Car)", or the bare id when the node has no name.
func nodeRef(st *device.State, num uint32) string {
	u := st.Nodes[num].GetUser()
	name := u.GetLongName()
	if name == "" {
		name = u.GetShortName()
	}
	if name == "" {
		return nodeID(num)
	}
	return nodeID(num) + " (" + sanitise(name) + ")"
}

func newNodesOpCmd(a *app, op device.NodeOp, short string) *cobra.Command {
	return &cobra.Command{
		Use:   op.String() + " <node>...",
		Short: short,
		Long:  short + ".\n\nA node is a !hex id, a number, or a unique short or long name.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, n, _, err := a.target(cmd, device.Want{})
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State() // nodes are named from the connected node's list, even for --dest
			nums := make([]uint32, len(args))
			for i, q := range args {
				if nums[i], err = resolveNode(st, q); err != nil {
					return err
				}
				// The firmware silently does nothing for a node it does not
				// list; only ignore (2.8 and newer) adds the entry. A remote
				// node's list cannot be read.
				if _, listed := st.Nodes[nums[i]]; !listed && op != device.Ignore && !n.IsRemote() {
					return fmt.Errorf("%s is not in the node list", nodeID(nums[i]))
				}
			}
			if op == device.RemoveNode && len(nums) > 1 {
				if err := confirmOrAbort(cmd.Context(), a, fmt.Sprintf("Remove %d nodes from the node list?", len(nums))); err != nil {
					return err
				}
			}
			for _, num := range nums {
				ref := nodeRef(st, num)
				err := n.EditNodeDB(cmd.Context(), op, num)
				if errors.Is(err, device.ErrNotVerifiable) {
					fmt.Fprintf(a.Stdout, "%s %s: sent (a remote node list cannot be read back)\n", op, ref)
					continue
				}
				if err != nil {
					var ve *device.VerifyError
					if _, listed := st.Nodes[num]; !listed && errors.As(err, &ve) {
						return fmt.Errorf("%s %s: %w (older firmware can only ignore nodes it already lists)", op, ref, err)
					}
					return fmt.Errorf("%s %s: %w", op, ref, err)
				}
				fmt.Fprintf(a.Stdout, "%s %s: done\n", op, ref)
			}
			return nil
		},
	}
}

func newNodesResetCmd(a *app) *cobra.Command {
	var keep bool
	cmd := &cobra.Command{
		Use:   "reset [--keep-favorites]",
		Short: "Clear the node list; the node reboots",
		Long: "Clear the node list; the node reboots.\n\n" +
			"Nodes in the ROUTER, ROUTER_LATE or CLIENT_BASE role always keep their favourites.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, n, _, err := a.target(cmd, device.Want{})
			if err != nil {
				return err
			}
			defer c.Close()
			prompt := "Reset the node list? The node reboots."
			if n.IsRemote() && !keep {
				// The node favourites the node that administers it, so only a
				// reset that keeps favourites keeps this node's key.
				prompt += " Without --keep-favorites it forgets this node's public key: remote admin stops working until it hears this node again."
			}
			if err := confirmOrAbort(cmd.Context(), a, prompt); err != nil {
				return err
			}
			err = n.ResetNodeDB(cmd.Context(), keep)
			if errors.Is(err, device.ErrNotVerifiable) {
				_, err = fmt.Fprintln(a.Stdout, "Node list reset sent; the node reboots (a remote node list cannot be read back).")
				return err
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(a.Stdout, "Node list reset; the node rebooted.")
			return err
		},
	}
	cmd.Flags().BoolVar(&keep, "keep-favorites", false, "keep favourite nodes")
	return cmd
}
