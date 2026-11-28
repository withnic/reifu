package fsx

import (
	"errors"
	"fmt"
	"os"
	"time"
)

const lockTimeout = 5 * time.Second
const lockPollInterval = 20 * time.Millisecond

var ErrLockTimeout = errors.New("project lock timed out")

// WithFileLock serializes writers to a project. Expired files are reclaimed
// only after acquiring an OS lock on the same file, so a live writer retains
// the lock even when its operation runs for more than five seconds.
func WithFileLock(root string, run func() error) error {
	return withFileLock(root, lockTimeout, run)
}

func withFileLock(root string, timeout time.Duration, run func() error) error {
	return withNamedFileLock(root, ".reifu.lock", timeout, run)
}

// WithNamedFileLock serializes writers using a project-relative lock path.
// The parent directory must already exist.
func WithNamedFileLock(root, target string, run func() error) error {
	return withNamedFileLock(root, target, lockTimeout, run)
}

func withNamedFileLock(root, target string, timeout time.Duration, run func() error) error {
	path, err := withinRoot(root, target)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for {
		file, err := openLock(path, true)
		if err == nil {
			acquired, err := runWithLock(file, run)
			if acquired || err != nil {
				return err
			}
		} else if os.IsExist(err) || sharingViolation(err) {
			if !os.IsExist(err) {
				if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
					return fmt.Errorf("acquire project lock %q: %w", path, err)
				}
			}
			if err := reclaimLock(path); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("acquire project lock %q: %w", path, err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("acquire %q: %w", path, ErrLockTimeout)
		}
		time.Sleep(min(remaining, lockPollInterval))
	}
}

func runWithLock(file *os.File, run func() error) (acquired bool, err error) {
	path := file.Name()
	defer func() {
		if acquired {
			err = errors.Join(err, removeTemp(path))
		}
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close project lock %q: %w", path, closeErr))
		}
	}()
	acquired, err = tryLock(file)
	if err != nil {
		return false, fmt.Errorf("lock %q: %w", path, err)
	}
	if !acquired {
		return false, nil
	}
	current, err := lockIsCurrent(file)
	if !current || err != nil {
		return false, err
	}
	if _, err := fmt.Fprintf(file, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return true, fmt.Errorf("write lock metadata %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return true, fmt.Errorf("sync lock metadata %q: %w", path, err)
	}
	return true, run()
}

func reclaimLock(path string) (err error) {
	entry, err := os.Lstat(path)
	if os.IsNotExist(err) || sharingViolation(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect lock %q: %w", path, err)
	}
	if !entry.Mode().IsRegular() {
		return fmt.Errorf("lock %q is not a regular file", path)
	}
	if time.Since(entry.ModTime()) < lockTimeout {
		return nil
	}
	file, err := openLock(path, false)
	if os.IsNotExist(err) || sharingViolation(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open stale lock %q: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close stale lock %q: %w", path, closeErr))
		}
	}()
	locked, err := tryLock(file)
	if err != nil {
		return fmt.Errorf("check stale lock %q: %w", path, err)
	}
	if !locked {
		return nil
	}
	current, err := lockIsCurrent(file)
	if !current || err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat stale lock %q: %w", path, err)
	}
	if time.Since(info.ModTime()) >= lockTimeout {
		return removeTemp(path)
	}
	return nil
}

func lockIsCurrent(file *os.File) (bool, error) {
	path := file.Name()
	entry, err := os.Lstat(path)
	if os.IsNotExist(err) || sharingViolation(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect current lock %q: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		return false, fmt.Errorf("stat open lock %q: %w", path, err)
	}
	return entry.Mode().IsRegular() && os.SameFile(entry, info), nil
}
