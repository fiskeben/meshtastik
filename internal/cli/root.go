// Package cli defines the meshtastik command tree.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// version is set at build time via -ldflags "-X meshtastik/internal/cli.version=...".
var version = "dev"

// ConnFlags holds the mutually exclusive connection selectors.
type ConnFlags struct {
	Port, Host, BLE string
}

// Deps carries the side-effecting dependencies of the commands.
type Deps struct {
	// Dial's return type becomes transport.Dialer in Task 3.
	Dial    func(ConnFlags) (any, error)
	Stdout  io.Writer
	Stderr  io.Writer
	Confirm func(prompt string) bool
}

// DefaultDeps returns dependencies wired to the real process environment.
func DefaultDeps() Deps {
	return Deps{
		Dial: func(ConnFlags) (any, error) {
			return nil, errors.New("no transport available yet")
		},
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Confirm: terminalConfirm,
	}
}

// terminalConfirm asks on stderr and reads from stdin. It refuses (returns
// false) when stdin is not a terminal.
func terminalConfirm(prompt string) bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// NewRoot builds the root command.
func NewRoot(d Deps) *cobra.Command {
	var (
		conn    ConnFlags
		asJSON  bool
		yes     bool
		debug   bool
		timeout time.Duration
	)
	root := &cobra.Command{
		Use:           "meshtastik",
		Short:         "Administer a local Meshtastic node",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			for _, s := range []string{conn.Port, conn.Host, conn.BLE} {
				if s != "" {
					n++
				}
			}
			if n > 1 {
				return errors.New("only one of --port, --host, --ble may be given")
			}
			return nil
		},
	}
	if d.Stdout != nil {
		root.SetOut(d.Stdout)
	}
	if d.Stderr != nil {
		root.SetErr(d.Stderr)
	}
	pf := root.PersistentFlags()
	pf.StringVar(&conn.Port, "port", "", "serial port path")
	pf.StringVar(&conn.Host, "host", "", "TCP host[:port] (default port 4403)")
	pf.StringVar(&conn.BLE, "ble", "", "BLE device name or address")
	pf.BoolVar(&asJSON, "json", false, "output JSON")
	pf.BoolVar(&yes, "yes", false, "skip confirmations")
	pf.BoolVar(&debug, "debug", false, "debug logging")
	pf.DurationVar(&timeout, "timeout", 0, "operation timeout (0 = per-transport default)")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "meshtastik %s\n", version)
		},
	})
	return root
}
