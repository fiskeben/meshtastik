package cli

import (
	"errors"
	"fmt"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"meshtastik/internal/device"
)

const (
	maxLongName  = 39
	maxShortName = 4
	powerDelay   = 5 // seconds
)

func checkLongName(s string) error {
	if s == "" {
		return errors.New("long name must not be empty")
	}
	if len(s) > maxLongName {
		return fmt.Errorf("long name is %d bytes; the limit is %d bytes", len(s), maxLongName)
	}
	return nil
}

func checkShortName(s string) error {
	if s == "" {
		return errors.New("short name must not be empty")
	}
	if len(s) > maxShortName {
		return fmt.Errorf("short name is %d bytes; the limit is %d bytes", len(s), maxShortName)
	}
	return nil
}

func newOwnerCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "owner", Short: "Change the node's owner names"}
	var short string
	set := &cobra.Command{
		Use:   "set <long-name> [--short X]",
		Short: "Set the long name, and optionally the short name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			long := args[0]
			if err := checkLongName(long); err != nil {
				return err
			}
			shortSet := cmd.Flags().Changed("short")
			if shortSet {
				if err := checkShortName(short); err != nil {
					return err
				}
			}
			c, err := a.connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			self := c.State().Self().GetUser()
			if self == nil && !shortSet {
				return errors.New("the node did not report its current owner; pass --short")
			}
			u := &pb.User{}
			if self != nil {
				u = proto.Clone(self).(*pb.User)
			}
			u.LongName = long
			if shortSet {
				u.ShortName = short
			}
			return applyChanges(cmd, a, c, []device.Change{{Owner: u}})
		},
	}
	set.Flags().StringVar(&short, "short", "", "short name (up to 4 bytes); kept unchanged if omitted")
	cmd.AddCommand(set)
	return cmd
}

func newRebootCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "reboot",
		Short: "Ask the node to reboot (it restarts in ~5 seconds)",
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
			_, err = fmt.Fprintf(a.Stdout, "Reboot requested (node restarts in ~%d seconds).\n", powerDelay)
			return err
		},
	}
}

func newShutdownCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "shutdown",
		Short: "Ask the node to power off (it shuts down in ~5 seconds)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmOrAbort(cmd.Context(), a, "Shut the node down? It stays off until powered on again."); err != nil {
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
			_, err = fmt.Fprintf(a.Stdout, "Shutdown requested (node powers off in ~%d seconds).\n", powerDelay)
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
			if err := confirmOrAbort(cmd.Context(), a, prompt); err != nil {
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
