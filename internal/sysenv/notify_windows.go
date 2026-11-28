//go:build windows

package sysenv

import (
	"io"
	"os"
	"syscall"
)

const enableVirtualTerminalProcessing = 0x0004

var (
	messageBeep    = syscall.NewLazyDLL("user32.dll").NewProc("MessageBeep")
	setConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")
)

func notifyPlatform(n Notification, tty io.Writer) {
	if messageBeep.Find() == nil {
		const (
			mbIconHand     = 0x10
			mbIconAsterisk = 0x40
		)
		sound := uintptr(mbIconHand)
		if n.Success {
			sound = mbIconAsterisk
		}
		_, _, _ = messageBeep.Call(sound)
	}
	if isTerminal(tty) {
		writeOSC9(tty.(*os.File), n.Title+": "+n.Message)
	}
}

// writeOSC9 writes an OSC 9 notification only when the console interprets
// virtual terminal sequences, so legacy consoles never print raw escapes.
func writeOSC9(f *os.File, text string) {
	handle := syscall.Handle(f.Fd())
	var mode uint32
	if syscall.GetConsoleMode(handle, &mode) != nil {
		return
	}
	if mode&enableVirtualTerminalProcessing == 0 {
		if setConsoleMode.Find() != nil {
			return
		}
		if r, _, _ := setConsoleMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminalProcessing)); r == 0 {
			return
		}
		defer func() { _, _, _ = setConsoleMode.Call(uintptr(handle), uintptr(mode)) }()
	}
	_, _ = io.WriteString(f, "\x1b]9;"+text+"\a")
}
