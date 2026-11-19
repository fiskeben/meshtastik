package main

import (
	"fmt"
	"os"

	"meshtastik/internal/cli"
)

func main() {
	d := cli.DefaultDeps()
	if err := cli.NewRoot(d).Execute(); err != nil {
		fmt.Fprintln(d.Stderr, "error:", err)
		os.Exit(1)
	}
}
