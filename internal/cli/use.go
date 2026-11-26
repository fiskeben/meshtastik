package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"meshtastik/internal/devices"
	"meshtastik/internal/nodeconn"
)

func (a *app) getenv(name string) string {
	if a.Getenv == nil {
		return ""
	}
	return a.Getenv(name)
}

func (a *app) loadDevices() (*devices.File, error) { return devices.Load(a.DevicesFile) }

// resolveConn picks the connection: the flags, else the named device, else
// the environment, else the current device (see nodeconn.Resolve).
func (a *app) resolveConn() (nodeconn.Resolved, error) {
	return nodeconn.Resolve(nodeconn.Selection{Flags: a.conn, Device: a.device, Getenv: a.getenv, DevicesFile: a.DevicesFile})
}

// changesAnnotation marks commands that change the node or send from it:
// "yes", or the name of the flag that makes them do so.
const changesAnnotation = "meshtastik/changes"

// changes marks cmd with changesAnnotation and returns it.
func changes(cmd *cobra.Command, when string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[changesAnnotation] = when
	return cmd
}

// changesNode reports whether cmd, as invoked, changes the node.
func changesNode(cmd *cobra.Command) bool {
	switch v := cmd.Annotations[changesAnnotation]; v {
	case "":
		return false
	case "yes":
		return true
	default:
		return cmd.Flags().Changed(v)
	}
}

func (a *app) completeDevices(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	f, err := a.loadDevices()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	return f.Names(), cobra.ShellCompDirectiveNoFileComp
}

type useJSON struct {
	Device string `json:"device,omitempty"`
	Target string `json:"target,omitempty"`
	From   string `json:"from,omitempty"`
}

func newUseCmd(a *app) *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "use [name | --port P | --host H | --ble B | --clear]",
		Short: "Choose the node commands connect to when none is given",
		Long: "Choose the node commands connect to when none of --port, --host, --ble or --device is given:\n" +
			"a named device (see 'meshtastik device'), or a target given with --port, --host or --ble.\n" +
			"Without arguments, show which node commands connect to and why.\n\n" +
			"The connection flags win over MESHTASTIK_DEVICE, MESHTASTIK_PORT, MESHTASTIK_HOST or MESHTASTIK_BLE,\n" +
			"which win over the current device. With none of them, commands look for a single serial port.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: a.completeDevices,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && a.device != "" {
				return errors.New("give the device name once")
			}
			name := a.device
			if len(args) == 1 {
				name = args[0]
			}
			n := 0
			for _, given := range []bool{name != "", a.conn != (ConnFlags{}), clear} {
				if given {
					n++
				}
			}
			if n > 1 {
				return errors.New("give one of a device name, --port, --host, --ble or --clear")
			}
			if n == 0 {
				return a.showCurrent()
			}
			f, err := a.loadDevices()
			if err != nil {
				return err
			}
			switch {
			case clear:
				f.Current = nil
			case name != "":
				if _, err := f.Lookup(name); err != nil {
					return err
				}
				f.Current = &devices.Current{Device: name}
			default:
				f.Current = &devices.Current{Target: devices.Target(a.conn)}
			}
			if err := devices.Save(a.DevicesFile, f); err != nil {
				return err
			}
			if f.Current == nil {
				fmt.Fprintln(a.Stdout, "No current device; commands look for a serial port.")
			} else {
				r := nodeconn.Resolved{Flags: ConnFlags(f.Current.Target), Name: name}
				if name != "" {
					r.Flags = ConnFlags(f.Devices[name])
				}
				fmt.Fprintf(a.Stdout, "Commands now connect to %s.\n", r)
			}
			for _, v := range nodeconn.ConnEnv {
				if a.getenv(v) != "" {
					fmt.Fprintf(a.Stderr, "note: %s is set, and wins over the current device in this shell\n", v)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "forget the current device")
	return cmd
}

// showCurrent prints the node commands connect to and why.
func (a *app) showCurrent() error {
	r, err := a.resolveConn()
	if err != nil {
		return err
	}
	if a.asJSON {
		v := useJSON{Device: r.Name, From: strings.TrimPrefix(r.From, "from ")}
		if r.Flags != (ConnFlags{}) {
			v.Target = devices.Target(r.Flags).String()
		}
		return printJSON(a.Stdout, v)
	}
	if r.Flags == (ConnFlags{}) {
		fmt.Fprintln(a.Stdout, "No current device; commands look for a serial port.\n"+
			"Choose one with 'meshtastik use <name>' or 'meshtastik use --host <host>' (or --port, --ble).")
		return nil
	}
	if r.From == "" {
		r.From = "given on the command line"
	}
	fmt.Fprintf(a.Stdout, "%s — %s\n", r, r.From)
	return nil
}

func newDeviceCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "device",
		Short: "Name the nodes you connect to, for --device and 'meshtastik use'",
	}
	cmd.AddCommand(newDeviceAddCmd(a), newDeviceListCmd(a), newDeviceRemoveCmd(a))
	return cmd
}

func newDeviceAddCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> (--port P | --host H | --ble B)",
		Short: "Name a node connection; an existing name is replaced",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := devices.CheckName(name); err != nil {
				return err
			}
			if a.device != "" {
				return errors.New("--device names an existing device; give the node with --port, --host or --ble")
			}
			t := devices.Target(a.conn)
			if t.IsZero() {
				return errors.New("give the node with --port, --host or --ble")
			}
			f, err := a.loadDevices()
			if err != nil {
				return err
			}
			old, existed := f.Devices[name]
			if f.Devices == nil {
				f.Devices = map[string]devices.Target{}
			}
			f.Devices[name] = t
			if err := devices.Save(a.DevicesFile, f); err != nil {
				return err
			}
			if existed && old != t {
				fmt.Fprintf(a.Stdout, "%s is now %s (was %s).\n", name, t, old)
			} else {
				fmt.Fprintf(a.Stdout, "%s is %s.\n", name, t)
			}
			return nil
		},
	}
}

type deviceJSON struct {
	Name    string `json:"name"`
	Target  string `json:"target"`
	Current bool   `json:"current"`
}

func newDeviceListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the named devices; * marks the current one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := a.loadDevices()
			if err != nil {
				return err
			}
			cur := ""
			if f.Current != nil {
				cur = f.Current.Device
			}
			rows := make([]deviceJSON, 0, len(f.Devices))
			for _, n := range f.Names() {
				rows = append(rows, deviceJSON{Name: n, Target: f.Devices[n].String(), Current: n == cur})
			}
			if a.asJSON {
				return printJSON(a.Stdout, rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(a.Stdout, "No named devices; add one with 'meshtastik device add <name> --host <host>' (or --port, --ble).")
			} else {
				tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "\tNAME\tTARGET")
				for _, r := range rows {
					mark := ""
					if r.Current {
						mark = "*"
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\n", mark, r.Name, r.Target)
				}
				if err := tw.Flush(); err != nil {
					return err
				}
			}
			if f.Current != nil && cur == "" {
				fmt.Fprintf(a.Stdout, "The current device is %s, which has no name.\n", f.Current.Target)
			}
			return nil
		},
	}
}

func newDeviceRemoveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:               "remove <name>",
		Short:             "Forget a named device",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.completeDevices,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			f, err := a.loadDevices()
			if err != nil {
				return err
			}
			if _, err := f.Lookup(name); err != nil {
				return err
			}
			delete(f.Devices, name)
			wasCurrent := f.Current != nil && f.Current.Device == name
			if wasCurrent {
				f.Current = nil
			}
			if err := devices.Save(a.DevicesFile, f); err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "Removed %s.\n", name)
			if wasCurrent {
				fmt.Fprintln(a.Stdout, "It was the current device; commands look for a serial port until you run 'meshtastik use' again.")
			}
			return nil
		},
	}
}
