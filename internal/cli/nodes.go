package cli

import (
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
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			st := c.State()
			nums := make([]uint32, len(args))
			for i, q := range args {
				if nums[i], err = resolveNode(st, q); err != nil {
					return err
				}
			}
			if op == device.RemoveNode && len(nums) > 1 {
				if err := confirmOrAbort(cmd.Context(), a, fmt.Sprintf("Remove %d nodes from the node list?", len(nums))); err != nil {
					return err
				}
			}
			for _, num := range nums {
				ref := nodeRef(st, num)
				if err := c.EditNodeDB(cmd.Context(), op, num); err != nil {
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
			if err := confirmOrAbort(cmd.Context(), a, "Reset the node list? The node reboots."); err != nil {
				return err
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			if err := c.ResetNodeDB(cmd.Context(), keep); err != nil {
				return err
			}
			_, err = fmt.Fprintln(a.Stdout, "Node list reset; the node rebooted.")
			return err
		},
	}
	cmd.Flags().BoolVar(&keep, "keep-favorites", false, "keep favourite nodes")
	return cmd
}
