package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"meshtastik/internal/configpath"
	"meshtastik/internal/device"
)

// destAnnotation marks commands that accept --dest.
const destAnnotation = "meshtastik/dest"

// supportsDest marks cmds as accepting --dest and returns the first.
func supportsDest(cmds ...*cobra.Command) *cobra.Command {
	for _, c := range cmds {
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		c.Annotations[destAnnotation] = "yes"
	}
	return cmds[0]
}

// checkDest refuses --dest on commands that do not support it.
func (a *app) checkDest(cmd *cobra.Command) error {
	if a.dest != "" && cmd.Annotations[destAnnotation] != "yes" {
		return fmt.Errorf("--dest is not supported by %s", cmd.CommandPath())
	}
	return nil
}

// node returns what admin commands act on: the connected node, or the
// --dest node, which is first asked for its metadata. --dest naming the
// connected node means the connected node.
func (a *app) node(cmd *cobra.Command, c *device.Client) (device.Node, error) {
	st := c.State()
	a.acting = nodeRef(st, st.MyInfo.GetMyNodeNum())
	if a.dest == "" {
		return device.Local{Client: c}, nil
	}
	num, err := resolveNode(st, a.dest)
	if err != nil {
		return nil, fmt.Errorf("--dest: %w", err)
	}
	if num == st.MyInfo.GetMyNodeNum() {
		return device.Local{Client: c}, nil
	}
	a.acting = nodeRef(st, num)
	fmt.Fprintf(a.Stderr, "reaching %s over the mesh — this can take a while\n", a.acting)
	return c.Remote(cmdContext(cmd), num)
}

// target connects and returns the client (the caller closes it), the node
// to act on, and its state as w asks for.
func (a *app) target(cmd *cobra.Command, w device.Want) (*device.Client, device.Node, *device.State, error) {
	c, err := a.connect(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	n, err := a.node(cmd, c)
	if err == nil {
		var st *device.State
		if st, err = n.Fetch(cmdContext(cmd), w); err == nil {
			return c, n, st, nil
		}
	}
	c.Close()
	return nil, nil, nil, err
}

// sectionWant is the Want for one config or module section.
func sectionWant(name string) device.Want {
	for _, s := range configpath.ConfigSectionNames() {
		if s == name {
			return device.Want{Config: []string{name}}
		}
	}
	return device.Want{Modules: []string{name}}
}

// allWant reads everything a backup or a full config listing needs.
var allWant = device.Want{Owner: true, Channels: true, Config: device.AllSections, Modules: device.AllSections}

// isTerminal reports whether w is a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
