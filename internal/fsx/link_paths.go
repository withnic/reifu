package fsx

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/withnic/reifu/internal/sysenv"
)

// ValidateDistinctPaths rejects aliases and file-versus-directory collisions
// between link destinations and files the caller needs to keep.
func ValidateDistinctPaths(protected, targets []string) error {
	key := func(file string) string {
		return strings.ToLower(path.Clean(strings.ReplaceAll(file, "\\", "/")))
	}
	seen := append([]string(nil), protected...)
	for _, target := range targets {
		name := key(target)
		for _, previous := range seen {
			other := key(previous)
			if name == other || strings.HasPrefix(name, other+"/") || strings.HasPrefix(other, name+"/") {
				return fmt.Errorf("link destination %q conflicts with %q", target, previous)
			}
		}
		seen = append(seen, target)
	}
	return nil
}

// ValidateLinkPaths checks link destinations before a multi-file operation.
func ValidateLinkPaths(root string, protected, targets []string) error {
	if err := ValidateDistinctPaths(protected, targets); err != nil {
		return err
	}
	var guards, destinations []string
	for _, name := range protected {
		// An inaccessible or out-of-root protected path cannot alias a
		// validated destination. Its literal name is checked above.
		if file, err := withinRoot(root, name); err == nil {
			guards = append(guards, file)
		}
	}
	for _, target := range targets {
		file, err := withinRoot(root, target)
		if err != nil {
			return err
		}
		destinations = append(destinations, file)
		info, err := os.Lstat(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect link destination %q: %w", file, err)
		}
		if !info.Mode().IsRegular() && !sysenv.IsPathLink(info) {
			return fmt.Errorf("link destination %q is neither a file nor a symlink", file)
		}
	}
	return ValidateDistinctPaths(guards, destinations)
}
