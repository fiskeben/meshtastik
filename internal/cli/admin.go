package cli

import (
	"fmt"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"github.com/spf13/cobra"

	"meshtastik/internal/device"
)

const (
	maxLongName  = 39
	maxShortName = 4
	powerDelay   = 5 // seconds
)

func newOwnerCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "owner", Short: "Change the node's owner names"}
	var short string
	set := &cobra.Command{
		Use:   "set <long-name> [--short X]",
		Short: "Set the long name, and optionally the short name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			long := args[0]
			if len(long) > maxLongName {
				return fmt.Errorf("long name is %d bytes; the limit is %d bytes", len(long), maxLongName)
			}
			shortSet := cmd.Flags().Changed("short")
			if shortSet && len(short) > maxShortName {
				return fmt.Errorf("short name is %d bytes; the limit is %d bytes", len(short), maxShortName)
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			if !shortSet {
				short = c.State().Self().GetUser().GetShortName()
			}
			return applyChanges(cmd, a, c, []device.Change{{Owner: &pb.User{LongName: long, ShortName: short}}})
		},
	}
	set.Flags().StringVar(&short, "short", "", "short name (up to 4 bytes); kept unchanged if omitted")
	cmd.AddCommand(set)
	return cmd
}

func newRebootCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "reboot",
		Short: "Reboot the node in 5 seconds",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			if err := c.Reboot(cmd.Context(), powerDelay); err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.Stdout, "Rebooting in %d seconds.\n", powerDelay)
			return err
		},
	}
}

func newShutdownCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "shutdown",
		Short: "Power the node off in 5 seconds",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmOrAbort(a, "Shut the node down? It stays off until powered on again."); err != nil {
				return err
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			if err := c.Shutdown(cmd.Context(), powerDelay); err != nil {
				return err
			}
			_, err = fmt.Fprintf(a.Stdout, "Shutting down in %d seconds.\n", powerDelay)
			return err
		},
	}
}

func newFactoryResetCmd(a *app) *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "factory-reset [--full]",
		Short: "Erase the node's configuration (--full also erases device state)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			prompt := "Factory reset the node? This erases its configuration."
			if full {
				prompt = "Fully factory reset the node? This erases its configuration and device state (including the node database and keys)."
			}
			if err := confirmOrAbort(a, prompt); err != nil {
				return err
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			if err := c.FactoryReset(cmd.Context(), full); err != nil {
				return err
			}
			_, err = fmt.Fprintln(a.Stdout, "Factory reset sent; the node will reboot.")
			return err
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "also erase device state")
	return cmd
}
