//go:build !windows

package daemon

import "syscall"

// detached starts the daemon in a session of its own, so it outlives the
// command's terminal.
func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
