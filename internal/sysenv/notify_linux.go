//go:build linux

package sysenv

import (
	"context"
	"io"
	"os/exec"
	"time"
)

func notifyPlatform(n Notification, tty io.Writer) {
	if isTerminal(tty) {
		_, _ = io.WriteString(tty, "\a")
	}
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	args := []string{"--", n.Title, n.Message}
	if !n.Success {
		args = append([]string{"--urgency=critical"}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, path, args...).Run()
}
