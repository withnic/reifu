package fsx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DirectoryAncestor locates the nearest existing directory for an absolute path.
func DirectoryAncestor(path string) (string, error) {
	for {
		info, err := os.Stat(path)
		if err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("%q is not a directory", path)
			}
			return path, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect directory %q: %w", path, err)
		}
		if _, linkErr := os.Lstat(path); !errors.Is(linkErr, os.ErrNotExist) {
			if linkErr == nil {
				return "", fmt.Errorf("resolve directory %q: %w", path, err)
			}
			return "", fmt.Errorf("inspect directory entry %q: %w", path, linkErr)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("inspect directory %q: %w", path, err)
		}
		path = parent
	}
}

// EnsureRoot creates an explicitly selected output root if it does not exist.
func EnsureRoot(root string) error {
	parent, err := DirectoryAncestor(root)
	if err != nil || parent == root {
		return err
	}
	rel, err := filepath.Rel(parent, root)
	if err != nil {
		return fmt.Errorf("locate output directory %q: %w", root, err)
	}
	return EnsureDirWithinRoot(parent, rel)
}
