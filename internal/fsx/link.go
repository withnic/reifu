package fsx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/sysenv"
)

// EnsureLink creates a project-relative link and returns the mode in use.
// Callers coordinate multi-file operations with WithFileLock.
func EnsureLink(root, source, target string, mode model.LinkMode, force bool) (model.LinkMode, error) {
	return ensureLink(root, source, target, mode, force, os.Link, os.Symlink)
}

// CheckLink inspects whether target refers to the same underlying file (or identical copy) as source.
func CheckLink(root, source, target string) (model.LinkReport, error) {
	report := model.LinkReport{Target: target}
	src, err := withinRoot(root, source)
	if err != nil {
		return report, err
	}
	dst, err := withinRoot(root, target)
	if err != nil {
		return report, err
	}
	dstEntry, err := os.Lstat(dst)
	if os.IsNotExist(err) {
		report.Status = model.LinkStatusMissing
		return report, nil
	}
	if err != nil {
		return report, fmt.Errorf("inspect link destination %q: %w", dst, err)
	}
	dstInfo, statErr := os.Stat(dst)
	if statErr != nil {
		if os.IsNotExist(statErr) && dstEntry.Mode()&os.ModeSymlink != 0 {
			report.Status = model.LinkStatusBroken
			report.Mode = model.LinkModeSymlink
			return report, nil
		}
		return report, fmt.Errorf("stat link destination %q: %w", dst, statErr)
	}
	srcInfo, err := os.Stat(src)
	if os.IsNotExist(err) {
		report.Status = model.LinkStatusDifferent
		return report, nil
	}
	if err != nil {
		return report, fmt.Errorf("stat link source %q: %w", src, err)
	}
	if !srcInfo.Mode().IsRegular() {
		return report, fmt.Errorf("link source %q is not a regular file", src)
	}
	if os.SameFile(srcInfo, dstInfo) {
		report.Status = model.LinkStatusOK
		if dstEntry.Mode()&os.ModeSymlink != 0 {
			report.Mode = model.LinkModeSymlink
		} else {
			report.Mode = model.LinkModeHard
		}
		return report, nil
	}
	if dstEntry.Mode().IsRegular() {
		equal, err := sameContents(src, dst)
		if err != nil {
			return report, err
		}
		if equal {
			report.Status = model.LinkStatusCopy
			report.Mode = model.LinkModeCopy
			return report, nil
		}
	}
	report.Status = model.LinkStatusDifferent
	return report, nil
}

func ensureLink(root, source, target string, mode model.LinkMode, force bool, hard, symlink func(string, string) error) (model.LinkMode, error) {
	if mode == "" {
		mode = model.LinkModeAuto
	}
	switch mode {
	case model.LinkModeAuto, model.LinkModeHard, model.LinkModeSymlink, model.LinkModeCopy:
	default:
		return "", fmt.Errorf("invalid link mode %q", mode)
	}
	src, err := withinRoot(root, source)
	if err != nil {
		return "", err
	}
	dst, err := withinRoot(root, target)
	if err != nil {
		return "", err
	}
	src, err = sysenv.ResolvePath(src)
	if err != nil {
		return "", fmt.Errorf("resolve link source %q: %w", source, err)
	}
	if filepath.Clean(src) == filepath.Clean(dst) {
		return "", fmt.Errorf("link source and target refer to the same path %q", target)
	}
	parent := filepath.Dir(dst)
	srcInfo, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("stat link source %q: %w", src, err)
	}
	if !srcInfo.Mode().IsRegular() {
		return "", fmt.Errorf("link source %q is not a regular file", src)
	}
	dstEntry, err := os.Lstat(dst)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect link destination %q: %w", dst, err)
	}
	if exists {
		dstInfo, err := os.Stat(dst)
		if err == nil && os.SameFile(srcInfo, dstInfo) {
			existingMode := model.LinkModeHard
			if dstEntry.Mode()&os.ModeSymlink != 0 {
				existingMode = model.LinkModeSymlink
			}
			if mode == model.LinkModeAuto || mode == existingMode {
				return existingMode, nil
			}
		}
		if err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("stat link destination %q: %w", dst, err)
		}
		if !dstEntry.Mode().IsRegular() && dstEntry.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("link destination %q is neither a file nor a symlink", dst)
		}
		if dstEntry.Mode().IsRegular() && mode == model.LinkModeCopy && (err != nil || !os.SameFile(srcInfo, dstInfo)) {
			equal, err := sameContents(src, dst)
			if err != nil {
				return "", err
			}
			if equal {
				return model.LinkModeCopy, nil
			}
		}
		if !force {
			return "", fmt.Errorf("link destination %q: %w", dst, os.ErrExist)
		}
	}

	var hardErr, symErr error
	if mode == model.LinkModeHard || mode == model.LinkModeAuto {
		var createErr error
		hardErr, createErr = createLink(dst, exists, func(path string) error {
			return retrySharing(func() error { return hard(src, path) })
		})
		if hardErr == nil {
			return model.LinkModeHard, nil
		}
		if mode == model.LinkModeHard || !canFallback(createErr) {
			return "", fmt.Errorf("hard-link %q to %q: %w", dst, src, hardErr)
		}
		if env, err := sysenv.Detect(parent); err == nil && env == sysenv.EnvWSL2DrvFS {
			fmt.Fprintf(os.Stderr, "reifu: hard link unavailable for %q on WSL2 DrvFS, trying a symlink or copy\n", target)
		}
	}
	if mode == model.LinkModeSymlink || mode == model.LinkModeAuto {
		rel, err := filepath.Rel(parent, src)
		if err != nil {
			return "", fmt.Errorf("relative symlink from %q to %q: %w", dst, src, err)
		}
		var createErr error
		symErr, createErr = createLink(dst, exists, func(path string) error { return symlink(rel, path) })
		if symErr == nil {
			return model.LinkModeSymlink, nil
		}
		if mode == model.LinkModeSymlink || !canFallback(createErr) {
			return "", fmt.Errorf("symlink %q to %q: %w", dst, src, symErr)
		}
	}
	data, err := readCopySource(src)
	if err != nil {
		return "", err
	}
	staged, err := stageFile(parent, bytes.NewReader(data), srcInfo.Mode().Perm())
	if err != nil {
		return "", fmt.Errorf("copy %q to %q: %w", src, dst, err)
	}
	if err := publish(staged, dst, exists); err != nil {
		return "", fmt.Errorf("copy %q to %q: %w", src, dst, errors.Join(hardErr, symErr, err))
	}
	return model.LinkModeCopy, nil
}

// Stage replacements in the same directory so a failed link leaves the
// original destination intact.
func createLink(dst string, replace bool, create func(string) error) (err, createErr error) {
	if !replace {
		err := create(dst)
		return err, err
	}
	temp, err := os.CreateTemp(filepath.Dir(dst), fmt.Sprintf(".reifu.tmp.%d.*", os.Getpid()))
	if err != nil {
		return fmt.Errorf("reserve temporary path for %q: %w", dst, err), nil
	}
	name := temp.Name()
	if err := errors.Join(temp.Close(), removeTemp(name)); err != nil {
		return fmt.Errorf("release temporary path %q: %w", name, err), nil
	}
	if err := create(name); err != nil {
		return err, err
	}
	return publish(name, dst, true), nil
}

func readLinkFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read link file %q: %w", path, err)
	}
	return data, nil
}

func sameContents(src, dst string) (bool, error) {
	a, err := readCopySource(src)
	if err != nil {
		return false, err
	}
	b, err := readLinkFile(dst)
	return bytes.Equal(a, b), err
}

func readCopySource(path string) ([]byte, error) {
	data, err := readLinkFile(path)
	if err != nil {
		return nil, err
	}
	return normalizeText(path, data)
}
