package sysenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// StripExtendedPrefix removes Windows \\?\ and \??\ prefixes from resolved paths.
func StripExtendedPrefix(path string) string {
	if rest, ok := strings.CutPrefix(path, `\\?\UNC\`); ok {
		return `\\` + rest
	}
	if rest, ok := strings.CutPrefix(path, `\??\UNC\`); ok {
		return `\\` + rest
	}
	if rest, ok := strings.CutPrefix(path, `\\?\`); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(path, `\??\`); ok {
		return rest
	}
	return path
}

// ResolvePath resolves an existing path. Some Windows sandboxes permit
// inspecting directory entries but deny the FindFirstFile calls EvalSymlinks
// uses to normalize the spelling of every ancestor.
func ResolvePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if !errors.Is(err, os.ErrPermission) {
		return resolved, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return walkExisting(absolute, 0)
}

func walkExisting(path string, hops int) (string, error) {
	if hops > 40 {
		return "", fmt.Errorf("too many reparse points at %q", path)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path, nil
	}
	resolved, err := walkExisting(parent, hops)
	if err != nil {
		return "", err
	}
	current := filepath.Join(resolved, filepath.Base(path))
	info, err := os.Lstat(current)
	if err != nil {
		return "", err
	}
	if !IsPathLink(info) {
		return current, nil
	}
	target, err := os.Readlink(current)
	if err != nil {
		return "", err
	}
	target = StripExtendedPrefix(target)
	if !filepath.IsAbs(target) {
		if filepath.VolumeName(target) != "" || (len(target) > 0 && os.IsPathSeparator(target[0])) {
			return "", fmt.Errorf("unsupported reparse target %q", target)
		}
		target = filepath.Join(resolved, target)
	}
	return walkExisting(filepath.Clean(target), hops+1)
}

func IsPathLink(info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
