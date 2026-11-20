package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"meshtastik/internal/devices"
)

// connEnv are the environment variables that choose a connection.
var connEnv = []string{"MESHTASTIK_DEVICE", "MESHTASTIK_PORT", "MESHTASTIK_HOST", "MESHTASTIK_BLE"}

// fromCurrent is resolvedConn.from for the device saved with `use`.
const fromCurrent = "the current device"

// resolvedConn is the connection a command uses.
type resolvedConn struct {
	conn ConnFlags
	name string // the named device, if one was chosen
	from string // why, when not chosen by a flag: fromCurrent or a variable
}

func (r resolvedConn) String() string {
	t := devices.Target(r.conn).String()
	if r.name != "" {
		return r.name + " (" + t + ")"
	}
	return t
}

// wrap adds to a connection error which node was tried and why, when the
// command line did not say.
func (r resolvedConn) wrap(err error) error {
	if r.from == "" {
		return err
	}
	hint := "pass --port, --host, --ble or --device to use another node"
	if r.from == fromCurrent {
		hint = "change it with 'meshtastik use', or " + hint
	}
	return fmt.Errorf("%s, %s: %w\n%s", r, r.from, err, hint)
}

func (a *app) getenv(name string) string {
	if a.Getenv == nil {
		return ""
	}
	return a.Getenv(name)
}

func (a *app) loadDevices() (*devices.File, error) { return devices.Load(a.DevicesFile) }

// resolveConn picks the connection: the flags, else the environment, else
// the current device. With none of these the result is empty, and serial
// auto-detection picks the port.
func (a *app) resolveConn() (resolvedConn, error) {
	if a.conn != (ConnFlags{}) {
		return resolvedConn{conn: a.conn}, nil
	}
	named := func(name, from string) (resolvedConn, error) {
		f, err := a.loadDevices()
		if err != nil {
			return resolvedConn{}, err
		}
		t, err := f.Lookup(name)
		if err != nil {
			if from != "" {
				err = fmt.Errorf("%s: %w", from, err)
			}
			return resolvedConn{}, err
		}
		return resolvedConn{conn: ConnFlags(t), name: name, from: from}, nil
	}
	if a.device != "" {
		return named(a.device, "")
	}
	var set []string
	for _, v := range connEnv {
		if a.getenv(v) != "" {
			set = append(set, v)
		}
	}
	switch {
	case len(set) > 1:
		return resolvedConn{}, fmt.Errorf("only one of %s may be set; %s are", strings.Join(connEnv, ", "), strings.Join(set, " and "))
	case len(set) == 1:
		v := a.getenv(set[0])
		var c ConnFlags
		switch set[0] {
		case "MESHTASTIK_DEVICE":
			return named(v, "from MESHTASTIK_DEVICE")
		case "MESHTASTIK_PORT":
			c.Port = v
		case "MESHTASTIK_HOST":
			c.Host = v
		case "MESHTASTIK_BLE":
			c.BLE = v
		}
		return resolvedConn{conn: c, from: "from " + set[0]}, nil
	}
	f, err := a.loadDevices()
	if err != nil {
		return resolvedConn{}, err
	}
	switch cur := f.Current; {
	case cur == nil:
		return resolvedConn{}, nil
	case cur.Device != "":
		return named(cur.Device, fromCurrent)
	}
	return resolvedConn{conn: ConnFlags(f.Current.Target), from: fromCurrent}, nil
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
				r := resolvedConn{conn: ConnFlags(f.Current.Target), name: name}
				if name != "" {
					r.conn = ConnFlags(f.Devices[name])
				}
				fmt.Fprintf(a.Stdout, "Commands now connect to %s.\n", r)
			}
			for _, v := range connEnv {
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
		v := useJSON{Device: r.name, From: strings.TrimPrefix(r.from, "from ")}
		if r.conn != (ConnFlags{}) {
			v.Target = devices.Target(r.conn).String()
		}
		return printJSON(a.Stdout, v)
	}
	if r.conn == (ConnFlags{}) {
		fmt.Fprintln(a.Stdout, "No current device; commands look for a serial port.\n"+
			"Choose one with 'meshtastik use <name>' or 'meshtastik use --host <host>' (or --port, --ble).")
		return nil
	}
	if r.from == "" {
		r.from = "given on the command line"
	}
	fmt.Fprintf(a.Stdout, "%s — %s\n", r, r.from)
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
