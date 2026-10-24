//go:build unix

package local

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts a child process into a process group of its own so
// that the whole tree can be signalled at once.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalProcessGroup sends a signal to every process of a group.
//
// The group is addressed with a negative process identifier, which is what
// makes the signal reach the children of the process as well.
func signalProcessGroup(pid int, signal os.Signal) error {
	number, ok := signal.(syscall.Signal)
	if !ok {
		return fmt.Errorf("local: unsupported signal %v", signal)
	}
	return syscall.Kill(-pid, number)
}
