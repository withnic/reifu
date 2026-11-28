//go:build darwin

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
	path, err := exec.LookPath("osascript")
	if err != nil {
		return
	}
	const script = `on run argv
	display notification (item 1 of argv) with title (item 2 of argv) sound name (item 3 of argv)
end run`
	sound := "Basso"
	if n.Success {
		sound = "Glass"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, path, "-e", script, "--", n.Message, n.Title, sound).Run()
}
