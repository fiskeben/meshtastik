package cli

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"meshtastik/internal/daemon"
	"meshtastik/internal/transport"
)

// daemonStartWait is how long to wait for a started daemon to answer.
const daemonStartWait = 5 * time.Second

// defaultIdleTimeout is how long the daemon keeps an unused node connection.
const defaultIdleTimeout = 30 * time.Minute

// daemonSocket is the socket the daemon commands use.
func (a *app) daemonSocket() string {
	if a.DaemonSocket != "" {
		return a.DaemonSocket
	}
	return daemon.SocketPath()
}

// flagsFor is the connection flag that selects t.
func flagsFor(t daemon.Target) ConnFlags {
	switch t.Kind {
	case transport.KindTCP.String():
		return ConnFlags{Host: t.Addr}
	case transport.KindBLE.String():
		return ConnFlags{BLE: t.Addr}
	}
	return ConnFlags{Port: t.Addr}
}

// daemonDialer connects through the daemon, starting it if none answers.
// Serial auto-detection runs here, so the daemon gets an explicit target.
func (a *app) daemonDialer(conn ConnFlags, backlog bool, timeout time.Duration) (transport.Dialer, error) {
	kind, addr, err := chooseConn(conn, transport.DetectSerial)
	if err != nil {
		return nil, err
	}
	h := daemon.Hello{Version: version, Target: daemon.Target{Kind: kind.String(), Addr: addr},
		Timeout: daemon.Duration(timeout), Backlog: backlog}
	sock := a.DaemonSocket
	return func(ctx context.Context) (transport.Conn, error) {
		conn, r, err := daemon.Connect(ctx, sock, h)
		if errors.Is(err, daemon.ErrNotRunning) && a.StartDaemon != nil {
			if err := a.startDaemon(ctx, sock, "daemon", "run"); err != nil {
				return nil, err
			}
			conn, r, err = daemon.Connect(ctx, sock, h)
		}
		if err != nil {
			return nil, err
		}
		if r.Version != version && !a.versionNoted {
			a.versionNoted = true
			fmt.Fprintf(a.Stderr, "note: the daemon runs meshtastik %s, this is %s; run 'meshtastik daemon restart' to update it\n", r.Version, version)
		}
		return conn, nil
	}, nil
}

// startDaemon starts a daemon on sock with args and waits until it answers.
func (a *app) startDaemon(ctx context.Context, sock string, args ...string) error {
	if a.StartDaemon == nil {
		return errors.New("starting the daemon is not available here")
	}
	if err := a.StartDaemon(sock, args...); err != nil {
		return err
	}
	return daemon.WaitRunning(ctx, sock, daemonStartWait)
}

func newDaemonCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the background daemon that holds node connections",
		Long: "Commands connect to nodes through a background daemon, which they start when it isn't running.\n" +
			"The daemon keeps each node connection open between commands, lets several commands use a node at once,\n" +
			"and keeps messages that arrive between listen runs. Use --no-daemon (or MESHTASTIK_NO_DAEMON=1) to connect directly.",
	}
	cmd.AddCommand(newDaemonRunCmd(a), newDaemonStartCmd(a), newDaemonStopCmd(a), newDaemonRestartCmd(a), newDaemonStatusCmd(a))
	return cmd
}

func idleFlag(cmd *cobra.Command, idle *time.Duration) {
	cmd.Flags().DurationVar(idle, "idle-timeout", defaultIdleTimeout, "disconnect from a node unused this long, and exit when nothing is left (0 = never)")
}

func newDaemonRunCmd(a *app) *cobra.Command {
	var idle time.Duration
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sock := a.daemonSocket()
			ln, err := daemon.Listen(sock)
			if errors.Is(err, daemon.ErrAlreadyRunning) {
				fmt.Fprintf(a.Stderr, "a daemon is already running on %s\n", sock)
				return nil
			}
			if err != nil {
				return fmt.Errorf("listen on %s: %w", sock, err)
			}
			srv := &daemon.Server{
				Version:       version,
				Dial:          func(t daemon.Target) (transport.Dialer, error) { return a.Dial(flagsFor(t)) },
				IdleTimeout:   idle,
				Log:           a.Stderr,
				DeviceOptions: a.DeviceOptions,
				Socket:        sock,
				LogFile:       daemon.LogPath(sock),
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return srv.Serve(ctx, ln)
		},
	}
	idleFlag(cmd, &idle)
	return cmd
}

func (a *app) daemonStatus(ctx context.Context) (*daemon.Status, error) {
	r, err := daemon.Request(ctx, a.daemonSocket(), daemon.Hello{Version: version, Op: daemon.OpStatus})
	if err != nil {
		return nil, err
	}
	return r.Status, nil
}

func (a *app) spawnDaemon(ctx context.Context, idle time.Duration) error {
	args := []string{"daemon", "run"}
	if idle != defaultIdleTimeout {
		args = append(args, "--idle-timeout", idle.String())
	}
	return a.startDaemon(ctx, a.daemonSocket(), args...)
}

func newDaemonStartCmd(a *app) *cobra.Command {
	var idle time.Duration
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the background unless it is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			if st, err := a.daemonStatus(ctx); err == nil {
				fmt.Fprintf(a.Stdout, "daemon already running (pid %d)\n", st.PID)
				return nil
			}
			if err := a.spawnDaemon(ctx, idle); err != nil {
				return err
			}
			st, err := a.daemonStatus(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "daemon started (pid %d), log %s\n", st.PID, st.Log)
			return nil
		},
	}
	idleFlag(cmd, &idle)
	return cmd
}

// stopDaemon asks the daemon to exit and waits until it no longer answers.
// It reports whether one was running.
func (a *app) stopDaemon(ctx context.Context) (bool, error) {
	sock := a.daemonSocket()
	_, err := daemon.Request(ctx, sock, daemon.Hello{Version: version, Op: daemon.OpStop})
	if errors.Is(err, daemon.ErrNotRunning) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	deadline := time.Now().Add(daemonStartWait)
	for {
		if _, err := a.daemonStatus(ctx); errors.Is(err, daemon.ErrNotRunning) {
			return true, nil
		}
		if time.Now().After(deadline) {
			return true, fmt.Errorf("daemon still answering %s after stop", daemonStartWait)
		}
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func newDaemonStopCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon, closing its node connections",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			was, err := a.stopDaemon(cmdContext(cmd))
			if err != nil {
				return err
			}
			if was {
				fmt.Fprintln(a.Stdout, "daemon stopped")
			} else {
				fmt.Fprintln(a.Stdout, "daemon not running")
			}
			return nil
		},
	}
}

func newDaemonRestartCmd(a *app) *cobra.Command {
	var idle time.Duration
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Stop the daemon if it is running, then start it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			if _, err := a.stopDaemon(ctx); err != nil {
				return err
			}
			if err := a.spawnDaemon(ctx, idle); err != nil {
				return err
			}
			st, err := a.daemonStatus(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "daemon started (pid %d), log %s\n", st.PID, st.Log)
			return nil
		},
	}
	idleFlag(cmd, &idle)
	return cmd
}

type daemonStatusJSON struct {
	Running bool `json:"running"`
	*daemon.Status
	Links []linkJSON `json:"links,omitzero"` // wins over Status.Links
}

// linkJSON is a node connection with the names it has in the devices file.
type linkJSON struct {
	Device string `json:"device,omitempty"`
	daemon.LinkStatus
}

// deviceNames maps each target in the devices file to its names, such as
// "tcp:10.0.0.2" to "home". It is empty when the file cannot be read.
func (a *app) deviceNames() map[string]string {
	names := map[string]string{}
	f, err := a.loadDevices()
	if err != nil {
		fmt.Fprintln(a.Stderr, "note: device names not shown:", err)
		return names
	}
	for _, n := range f.Names() {
		t := f.Devices[n].String()
		if names[t] != "" {
			names[t] += ","
		}
		names[t] += n
	}
	return names
}

func newDaemonStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the daemon runs and which node connections it holds",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.daemonStatus(cmdContext(cmd))
			if errors.Is(err, daemon.ErrNotRunning) {
				if a.asJSON {
					return printJSON(a.Stdout, daemonStatusJSON{})
				}
				fmt.Fprintln(a.Stdout, "daemon not running")
				return nil
			}
			if err != nil {
				return err
			}
			names := a.deviceNames()
			if a.asJSON {
				links := make([]linkJSON, 0, len(st.Links))
				for _, l := range st.Links {
					links = append(links, linkJSON{Device: names[l.Target.String()], LinkStatus: l})
				}
				return printJSON(a.Stdout, daemonStatusJSON{Running: true, Status: st, Links: links})
			}
			now := time.Now()
			idle := "never"
			if st.IdleTimeout > 0 {
				idle = time.Duration(st.IdleTimeout).String()
			}
			fmt.Fprintf(a.Stdout, "daemon %s, pid %d, up %s\n", st.Version, st.PID, ago(now, st.Started))
			fmt.Fprintf(a.Stdout, "socket        %s\nlog           %s\nidle timeout  %s\n", st.Socket, st.Log, idle)
			if len(st.Links) == 0 {
				fmt.Fprintln(a.Stdout, "no node connections")
				return nil
			}
			fmt.Fprintln(a.Stdout)
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "DEVICE\tTARGET\tSTATE\tNODE\tCLIENTS\tSINCE")
			for _, l := range st.Links {
				node := orDash(l.Node)
				if l.NodeID != "" && l.Node != l.NodeID {
					node += " " + l.NodeID
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s ago\n", orDash(names[l.Target.String()]), l.Target, l.State, sanitise(node), l.Clients, ago(now, l.Since))
			}
			return tw.Flush()
		},
	}
}

// ago is now-t rounded to the second.
func ago(now, t time.Time) string { return now.Sub(t).Round(time.Second).String() }

func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
