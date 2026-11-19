package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"meshtastik/internal/cli"
)

func main() {
	// Ctrl-C cancels the command's context, so an interrupted edit reports
	// what state the node was left in. A second Ctrl-C kills the process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	d := cli.DefaultDeps()
	err := cli.NewRoot(d).ExecuteContext(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(d.Stderr, "error:", err)
		os.Exit(1)
	}
}
