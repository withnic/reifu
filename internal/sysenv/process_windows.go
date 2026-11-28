package sysenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// KillTreeOnCancel makes context cancellation terminate the whole process
// tree. Agent CLIs are often .cmd shims, so killing only cmd.exe would leave
// the real agent running and editing the worktree.
func KillTreeOnCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		systemRoot := os.Getenv("SystemRoot")
		if systemRoot == "" {
			systemRoot = `C:\Windows`
		}
		taskkill := filepath.Join(systemRoot, "System32", "taskkill.exe")
		kill := exec.Command(taskkill, "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
