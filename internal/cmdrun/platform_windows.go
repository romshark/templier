//go:build windows

package cmdrun

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// SetProcessGroup makes cmd run in its own process group so that a console
// control event can be delivered to it without also hitting Templiér itself.
// It must be called before starting cmd for StopProcess to be able to stop it gracefully.
func SetProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// StopProcess asks p to terminate and reports whether it was asked to shut
// down gracefully (true) or had to be terminated forcefully (false).
//
// Go's os.Process.Signal(os.Interrupt) always fails on Windows with
// "not supported by windows", hence a console control event is used instead.
// The Go runtime of the target process translates it into os.Interrupt,
// so its regular shutdown logic applies.
//
// This requires p to have been started with SetProcessGroup, since the event
// addresses a process group, and it requires Templiér to own a console.
// Neither can be relied upon, so p is terminated if the event can't be sent.
//
// Templiér owns a console because it is a console application.
// Should it ever be built for the GUI subsystem it would have to
// allocate one explicitly (AllocConsole), otherwise every stop
// silently falls back to terminating.
func StopProcess(p *os.Process) (graceful bool, err error) {
	// The process group ID of a process started with CREATE_NEW_PROCESS_GROUP
	// is its process ID.
	err = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid))
	if err == nil {
		return true, nil
	}
	return false, p.Kill()
}

// shellCommand returns the shell executable and the arguments
// necessary to execute script with it.
//
// Windows provides no POSIX shell, the command interpreter is used instead.
func shellCommand(script string) (name string, args []string) {
	return "cmd", []string{"/C", script}
}
