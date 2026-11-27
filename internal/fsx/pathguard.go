package fsx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/withnic/reifu/internal/sysenv"
)

// ValidateWithinRoot validates a project-relative path, including existing
// symlink components. The target and its parent directories may be absent.
func ValidateWithinRoot(root, target string) error {
	_, err := withinRoot(root, target)
	return err
}

// EnsureDirWithinRoot safely creates a project-relative directory hierarchy,
// rejecting path traversal and symlinks that escape the project root.
func EnsureDirWithinRoot(root, relDir string) error {
	path, err := withinRoot(root, relDir)
	if err != nil {
		return err
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(canonical, path)
	if err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("directory %q escapes project root %q", relDir, canonical)
	}
	current := canonical
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0755); err != nil && !os.IsExist(err) {
				return fmt.Errorf("create directory %q: %w", current, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect directory %q: %w", current, err)
		}
		if sysenv.IsPathLink(info) {
			resolved, err := resolveWithin(canonical, current, 0)
			if err != nil {
				return fmt.Errorf("validate directory symlink %q: %w", current, err)
			}
			targetInfo, err := os.Stat(resolved)
			if err != nil {
				return fmt.Errorf("stat directory symlink %q: %w", current, err)
			}
			if !targetInfo.IsDir() {
				return fmt.Errorf("path component %q is not a directory", current)
			}
			current = resolved
			continue
		}
		if !info.IsDir() {
			return fmt.Errorf("path component %q is not a directory", current)
		}
	}
	return nil
}

func canonicalRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("absolute project root %q: %w", root, err)
	}
	canonical, err := sysenv.ResolvePath(abs)
	if err != nil {
		return "", fmt.Errorf("resolve project root %q: %w", abs, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("stat project root %q: %w", canonical, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project root %q is not a directory", canonical)
	}
	return canonical, nil
}

func withinRoot(root, target string) (string, error) {
	portable := strings.ReplaceAll(target, "\\", "/")
	parts := strings.Split(portable, "/")
	if portable == "" || strings.HasPrefix(portable, "/") || strings.Contains(portable, ":") || strings.ContainsFunc(portable, unicode.IsControl) {
		return "", fmt.Errorf("invalid project-relative path %q", target)
	}
	for _, part := range parts {
		if part == ".." || (part != "." && (strings.HasSuffix(part, ".") || strings.HasSuffix(part, " "))) {
			return "", fmt.Errorf("invalid path component %q in %q", part, target)
		}
	}
	relative := filepath.FromSlash(portable)
	if !filepath.IsLocal(relative) || filepath.Clean(relative) == "." {
		return "", fmt.Errorf("invalid project-relative path %q", target)
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(canonical, relative)
	if _, err := resolveWithin(canonical, path, 0); err != nil {
		return "", fmt.Errorf("validate target %q: %w", target, err)
	}
	parent, err := resolveWithin(canonical, filepath.Dir(path), 0)
	if err != nil {
		return "", fmt.Errorf("validate target parent %q: %w", target, err)
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func resolveWithin(root, path string, depth int) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("compare %q with root %q: %w", path, root, err)
	}
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path %q escapes project root %q", path, root)
	}
	if depth > 40 {
		return "", fmt.Errorf("too many symlink traversals at %q", path)
	}
	if rel == "." {
		return root, nil
	}
	current := root
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return filepath.Join(current, filepath.Join(parts[i+1:]...)), nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect %q: %w", current, err)
		}
		if sysenv.IsPathLink(info) {
			destination, err := os.Readlink(current)
			if err != nil {
				return "", fmt.Errorf("read symlink %q: %w", current, err)
			}
			destination = sysenv.StripExtendedPrefix(destination)
			absolute := filepath.IsAbs(destination)
			if !absolute && (filepath.VolumeName(destination) != "" || (len(destination) > 0 && os.IsPathSeparator(destination[0]))) {
				return "", fmt.Errorf("unsupported link destination %q at %q", destination, current)
			}
			// A leading .. in a relative link is interpreted from the
			// already resolved parent. Reject .. after any other component,
			// where cleaning it could hide a symlink traversal.
			seenName := absolute
			for _, component := range strings.FieldsFunc(destination, func(r rune) bool {
				return r < 128 && os.IsPathSeparator(byte(r))
			}) {
				switch component {
				case ".":
				case "..":
					if seenName {
						return "", fmt.Errorf("ambiguous parent traversal in link %q", current)
					}
				default:
					if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
						return "", fmt.Errorf("ambiguous component in link %q", current)
					}
					seenName = true
				}
			}
			if !absolute {
				destination = filepath.Join(filepath.Dir(current), destination)
			} else {
				resolvedDest, err := canonicalizeAbsoluteDestination(root, destination)
				if err != nil {
					return "", fmt.Errorf("resolve link destination %q at %q: %w", destination, current, err)
				}
				destination = resolvedDest
			}
			current, err = resolveWithin(root, destination, depth+1)
			if err != nil {
				return "", err
			}
		} else if i < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("path component %q is not a directory", current)
		}
	}
	return current, nil
}

func canonicalizeAbsoluteDestination(root, path string) (string, error) {
	cleaned := filepath.Clean(path)
	cur := cleaned
	var tail []string
	for {
		resolved, err := sysenv.ResolvePath(cur)
		if err == nil {
			parts := make([]string, 0, 1+len(tail))
			parts = append(parts, alignVolumeCase(root, resolved))
			for i := len(tail) - 1; i >= 0; i-- {
				parts = append(parts, tail[i])
			}
			return filepath.Join(parts...), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return alignVolumeCase(root, cleaned), nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func alignVolumeCase(root, path string) string {
	rootVol := filepath.VolumeName(root)
	pathVol := filepath.VolumeName(path)
	if rootVol != "" && strings.EqualFold(rootVol, pathVol) {
		return rootVol + path[len(pathVol):]
	}
	return path
}
