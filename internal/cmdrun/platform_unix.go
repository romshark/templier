//go:build !windows

package cmdrun

import (
	"os"
	"os/exec"
)

// SetProcessGroup is a noop on unix-like systems where the interrupt signal
// can be delivered to any process directly, no matter its process group.
func SetProcessGroup(cmd *exec.Cmd) {}

// StopProcess asks p to terminate and reports whether it was asked to shut
// down gracefully (true) or had to be terminated forcefully (false).
//
// graceful is always true here because the interrupt signal can always be
// delivered on unix-like systems. Only the Windows implementation ever needs
// to fall back to terminating the process.
func StopProcess(p *os.Process) (graceful bool, err error) {
	return true, p.Signal(os.Interrupt)
}

// shellCommand returns the shell executable and the arguments
// necessary to execute script with it.
func shellCommand(script string) (name string, args []string) {
	return "sh", []string{"-c", script}
}
