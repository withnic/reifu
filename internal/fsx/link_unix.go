//go:build linux || darwin

package fsx

import (
	"errors"
	"os"
	"syscall"
)

func sharingViolation(error) bool { return false }

func canFallback(err error) bool {
	return errors.Is(err, syscall.EXDEV) || errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.ENOSYS) || errors.Is(err, os.ErrPermission)
}

func publishNew(staged, target string) error {
	if err := os.Link(staged, target); err == nil {
		return removeTemp(staged)
	} else if !canFallback(err) {
		return err
	}
	// On filesystems without hard links, the project lock serializes
	// participating writers across the existence check and rename.
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		if err == nil {
			return os.ErrExist
		}
		return err
	}
	return os.Rename(staged, target)
}

func openLock(path string, create bool) (*os.File, error) {
	flags := os.O_RDWR | syscall.O_NOFOLLOW
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	return os.OpenFile(path, flags, 0600)
}

func tryLock(file *os.File) (bool, error) {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}
