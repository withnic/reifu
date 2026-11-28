//go:build windows

package sysenv

import (
	"io"
	"syscall"
)

func notifyPlatform(n Notification, tty io.Writer) {
	beep := syscall.NewLazyDLL("user32.dll").NewProc("MessageBeep")
	if beep.Find() == nil {
		const (
			mbIconHand     = 0x10
			mbIconAsterisk = 0x40
		)
		sound := uintptr(mbIconHand)
		if n.Success {
			sound = mbIconAsterisk
		}
		_, _, _ = beep.Call(sound)
	}
	if isTerminal(tty) {
		_, _ = io.WriteString(tty, "\x1b]9;"+n.Title+": "+n.Message+"\a")
	}
}
