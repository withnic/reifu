package sysenv

import (
	"io"
	"os"
	"strings"
	"unicode"
)

type Notification struct {
	Title   string
	Message string
	Success bool
}

// Notify delivers a best-effort notification and writes terminal signals only to tty.
func Notify(n Notification, tty io.Writer) {
	if os.Getenv("CI") != "" || os.Getenv("REIFU_NO_NOTIFY") != "" {
		return
	}
	n.Title = sanitizeNotificationText(n.Title)
	n.Message = sanitizeNotificationText(n.Message)
	notifyPlatform(n, tty)
}

func sanitizeNotificationText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok || file == nil {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
