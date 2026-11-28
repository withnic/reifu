package sysenv

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSanitizeNotificationText(t *testing.T) {
	const input = " \x00日本語\r\n\t  \x1b]9;example\a \x7f\u009b\u009d\u009c  end "
	const want = "日本語 ]9;example end"
	if got := sanitizeNotificationText(input); got != want {
		t.Fatalf("sanitizeNotificationText = %q, want %q", got, want)
	}
}

func TestNotifySuppressed(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		dir := t.TempDir()
		marker := filepath.Join(dir, "called")
		t.Setenv("PATH", dir)
		t.Setenv("REIFU_TEST_NOTIFY_MARKER", marker)
		program := "notify-send"
		if runtime.GOOS == "darwin" {
			program = "osascript"
		}
		if err := os.WriteFile(filepath.Join(dir, program), []byte("#!/bin/sh\n: > \"$REIFU_TEST_NOTIFY_MARKER\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"CI", "REIFU_NO_NOTIFY"} {
			t.Run(key, func(t *testing.T) {
				t.Setenv("CI", "")
				t.Setenv("REIFU_NO_NOTIFY", "")
				t.Setenv(key, "1")
				Notify(Notification{Title: "test", Message: "suppressed"}, nil)
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("notification helper was invoked or marker could not be checked: %v", err)
				}
			})
		}
	}
	for _, key := range []string{"CI", "REIFU_NO_NOTIFY"} {
		t.Run(key+"_output", func(t *testing.T) {
			t.Setenv("CI", "")
			t.Setenv("REIFU_NO_NOTIFY", "")
			t.Setenv(key, "1")
			var out bytes.Buffer
			Notify(Notification{Title: "test", Message: "suppressed"}, &out)
			if out.Len() != 0 {
				t.Fatalf("unexpected notification output: %q", &out)
			}
		})
	}
}
