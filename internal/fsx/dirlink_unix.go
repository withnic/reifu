//go:build !windows

package fsx

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func createJunction(string, string) error { return syscall.ENOTSUP }
func isJunction(string) bool              { return false }

func isDirReparsePoint(info os.FileInfo, _ string) bool {
	return info.Mode()&os.ModeSymlink != 0
}

func junctionUnsupported(err error) bool { return errors.Is(err, syscall.ENOTSUP) }

func removeOrdinaryDir(path string) error { return os.RemoveAll(path) }

func moveNewDirEntry(src, dst string) error {
	// The project lock serializes participating writers across the check and rename.
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("move directory entry to %q: %w", dst, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(src, dst)
}
