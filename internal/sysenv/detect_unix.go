//go:build linux || darwin

package sysenv

import (
	"os"
	"path/filepath"
)

func ResolvePath(path string) (string, error) { return filepath.EvalSymlinks(path) }

func IsPathLink(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }

func StripExtendedPrefix(path string) string { return path }
