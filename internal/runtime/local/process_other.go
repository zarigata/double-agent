//go:build !unix

package local

import (
	"errors"
	"os"
	"os/exec"
)

// setProcessGroup is a no-op on platforms without process groups.
func setProcessGroup(cmd *exec.Cmd) {}

// signalProcessGroup reports that process groups are not available.
func signalProcessGroup(pid int, signal os.Signal) error {
	return errors.New("local: process groups are not supported on this platform")
}
