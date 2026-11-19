package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain lets a test re-run this binary as the real meshtastik process.
func TestMain(m *testing.M) {
	if args, ok := os.LookupEnv("MESHTASTIK_RUN_MAIN"); ok {
		os.Args = append([]string{"meshtastik"}, strings.Fields(args)...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestInterruptCancelsTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs SIGINT")
	}
	// A node that accepts the connection but never answers the handshake.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			go io.Copy(io.Discard, c)
		}
	}()
	var stderr bytes.Buffer
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "MESHTASTIK_RUN_MAIN=--host "+ln.Addr().String()+" info")
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("never connected")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("want exit code 1 after interrupt, got %v (stderr %q)", err, stderr.String())
	}
	if !strings.HasPrefix(stderr.String(), "error: ") || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("stderr %q", stderr.String())
	}
}
