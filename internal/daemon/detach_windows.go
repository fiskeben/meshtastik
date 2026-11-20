package daemon

import "syscall"

// detached starts the daemon without a console, so it outlives the
// command's window.
func detached() *syscall.SysProcAttr {
	const detachedProcess = 0x00000008
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess, HideWindow: true}
}
