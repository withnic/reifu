package fsx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"
)

// WriteFileAtomic writes UTF-8 text at a project-relative path, preserving
// existing permissions and replacing the destination entry after syncing.
func WriteFileAtomic(root, target string, data []byte) error {
	return writeFileAtomic(root, target, data, true)
}

// WriteNewFileAtomic publishes a file only if its destination is still absent.
func WriteNewFileAtomic(root, target string, data []byte) error {
	return writeFileAtomic(root, target, data, false)
}

func writeFileAtomic(root, target string, data []byte, replace bool) error {
	path, err := withinRoot(root, target)
	if err != nil {
		return err
	}
	data, err = normalizeText(path, data)
	if err != nil {
		return err
	}
	mode := os.FileMode(0644)
	info, err := os.Lstat(path)
	if err == nil {
		if !replace {
			return fmt.Errorf("write %q: %w", path, os.ErrExist)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("write %q: target is not a regular file", path)
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("lstat %q: %w", path, err)
	}
	staged, err := stageFile(filepath.Dir(path), bytes.NewReader(data), mode)
	if err != nil {
		return err
	}
	return publish(staged, path, replace)
}

func normalizeText(path string, data []byte) ([]byte, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("normalize %q: input is not valid UTF-8", path)
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(data, []byte("\r"), []byte("\n")), nil
}

func stageFile(dir string, data io.Reader, mode os.FileMode) (path string, err error) {
	file, err := os.CreateTemp(dir, fmt.Sprintf(".reifu.tmp.%d.*", os.Getpid()))
	if err != nil {
		return "", fmt.Errorf("create temporary file in %q: %w", dir, err)
	}
	name := file.Name()
	defer func() {
		if err != nil {
			err = errors.Join(err, removeTemp(name))
		}
	}()
	_, writeErr := io.Copy(file, data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if writeErr == nil {
		writeErr = file.Chmod(mode)
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return "", fmt.Errorf("prepare temporary file %q: %w", name, err)
	}
	return name, nil
}

func publish(staged, target string, replace bool) (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, removeTemp(staged))
		}
	}()
	if !replace {
		if err := publishNew(staged, target); err != nil {
			return fmt.Errorf("publish %q: %w", target, err)
		}
		return nil
	}
	if err := retrySharing(func() error { return os.Rename(staged, target) }); err != nil {
		return fmt.Errorf("publish %q: %w", target, err)
	}
	return nil
}

func removeTemp(path string) error {
	err := retrySharing(func() error { return os.Remove(path) })
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove temporary file %q: %w", path, err)
	}
	return nil
}

// OpenRegular opens a regular file for reading. On Windows it retries the transient
// sharing violation or access denial seen while a writer atomically replaces the file.
func OpenRegular(path string) (*os.File, error) {
	var file *os.File
	err := retrySharing(func() error {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%q must be a regular file", path)
		}
		file, err = os.Open(path)
		return err
	})
	return file, err
}

func retrySharing(run func() error) error {
	for i := 0; ; i++ {
		err := run()
		if i == 5 || !sharingViolation(err) {
			return err
		}
		time.Sleep(20 * time.Millisecond << i)
	}
}
