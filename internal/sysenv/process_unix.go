//go:build linux || darwin

package sysenv

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// KillTreeOnCancel starts the command in its own process group and makes
// context cancellation kill the whole group, including agent subprocesses.
func KillTreeOnCancel(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
