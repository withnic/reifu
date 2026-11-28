//go:build !windows && !darwin && !linux

package sysenv

import "io"

func notifyPlatform(_ Notification, tty io.Writer) {
	if isTerminal(tty) {
		_, _ = io.WriteString(tty, "\a")
	}
}
