//go:build windows

package cmd

import "os"

// processAlive reports whether pid names a live process. On Windows
// os.FindProcess opens the process by id and fails when none exists, which is
// enough to tell a running gateway from a stale pid record.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = proc.Release()
	return true
}

// signalTerminate stops the gateway. Windows has no SIGTERM to deliver to
// another process, so this is an immediate stop; the gateway's state is
// committed per write, so nothing is lost.
func signalTerminate(proc *os.Process) error {
	return proc.Kill()
}
