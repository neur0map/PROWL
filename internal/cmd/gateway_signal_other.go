//go:build !windows

package cmd

import (
	"errors"
	"os"
	"syscall"
)

// processAlive reports whether pid names a live process this user may signal.
// Signal 0 is delivered to no one: it only exercises the kernel's existence
// and permission checks, so it is the standard liveness probe on Unix.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but belongs to another user; still alive.
	return errors.Is(err, syscall.EPERM)
}

// signalTerminate asks the gateway to shut down gracefully. The foreground
// gateway installs a SIGTERM handler that drains in-flight requests before
// exiting, so this is a clean stop rather than a kill.
func signalTerminate(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}
