package fsx

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/withnic/reifu/internal/model"
)

// DiscoverSkills returns sorted names of direct, ordinary skill directories.
func DiscoverSkills(root, sourceDir string) ([]string, error) {
	src, _, err := dirLinkSource(root, sourceDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, fmt.Errorf("read skills directory %q: %w", src, err)
	}
	var names []string
	seen := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\\") ||
			strings.Contains(name, "..") || strings.ContainsFunc(name, unicode.IsControl) {
			continue
		}
		dir := filepath.Join(src, name)
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect skill %q: %w", dir, err)
		}
		if !info.IsDir() || isDirReparsePoint(info, dir) {
			continue
		}
		skill := filepath.Join(dir, "SKILL.md")
		info, err = os.Lstat(skill)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect skill file %q: %w", skill, err)
		}
		if !info.Mode().IsRegular() || isDirReparsePoint(info, skill) {
			continue
		}
		key := strings.ToLower(name)
		if previous, ok := seen[key]; ok {
			return nil, fmt.Errorf("skill names %q and %q collide in %q", previous, name, src)
		}
		seen[key] = name
		names = append(names, name)
	}
	return names, nil
}

func dirLinkSource(root, source string) (string, os.FileInfo, error) {
	src, err := withinRoot(root, source)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return src, nil, fmt.Errorf("inspect directory source %q: %w", src, err)
	}
	if !info.IsDir() || isDirReparsePoint(info, src) {
		return "", nil, fmt.Errorf("directory source %q must be an ordinary directory", src)
	}
	return src, info, nil
}

// dirLinkDestination validates the relative path and resolves its parent,
// leaving the final entry untouched so broken and external links can be removed.
func dirLinkDestination(root, target string) (string, error) {
	canonical, path, err := projectPath(root, target)
	if err != nil {
		return "", err
	}
	resolved, err := resolveWithin(canonical, filepath.Dir(path), 0)
	if err != nil {
		return "", fmt.Errorf("validate directory link parent for %q: %w", target, err)
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

// CheckDirLink classifies a directory link or copy without following an
// out-of-project target.
func CheckDirLink(root, source, target string) (model.LinkReport, error) {
	report := model.LinkReport{Target: target}
	src, srcInfo, err := dirLinkSource(root, source)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	dst, err := dirLinkDestination(root, target)
	if err != nil {
		return report, err
	}
	if srcInfo != nil {
		if err := validateSkillTree(root, src); err != nil {
			return report, err
		}
	}
	return checkDirLink(root, src, srcInfo, dst, report)
}

func checkDirLink(root, src string, srcInfo os.FileInfo, dst string, report model.LinkReport) (model.LinkReport, error) {
	report.Status = model.LinkStatusMissing
	entry, err := os.Lstat(dst)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, fmt.Errorf("inspect directory link %q: %w", dst, err)
	}
	report.Status = model.LinkStatusDifferent

	if isDirReparsePoint(entry, dst) {
		canonical, err := canonicalRoot(root)
		if err != nil {
			return report, err
		}
		if _, err := resolveWithin(canonical, dst, 0); err != nil {
			return report, err
		}
		report.Mode = model.LinkModeSymlink
		if isJunction(dst) {
			report.Mode = model.LinkModeJunction
		}
		info, err := os.Stat(dst)
		if errors.Is(err, os.ErrNotExist) {
			report.Status = model.LinkStatusBroken
			return report, nil
		}
		if err != nil {
			return report, fmt.Errorf("stat directory link %q: %w", dst, err)
		}
		if srcInfo != nil && os.SameFile(srcInfo, info) {
			report.Status = model.LinkStatusOK
		}
		return report, nil
	}

	if srcInfo != nil && entry.IsDir() && !os.SameFile(srcInfo, entry) {
		equal, err := sameDirContents(root, src, dst)
		if err != nil {
			return report, err
		}
		if equal {
			report.Status, report.Mode = model.LinkStatusCopy, model.LinkModeCopy
		}
	}
	return report, nil
}

// EnsureDirLink stages the new entry before replacing an existing destination.
// Callers coordinate writes with the project lock.
func EnsureDirLink(root, source, target string, mode model.LinkMode, force bool) (model.LinkMode, error) {
	return ensureDirLink(root, source, target, mode, force, os.Symlink, createJunction)
}

func ensureDirLink(root, source, target string, mode model.LinkMode, force bool, symlink, junction func(string, string) error) (model.LinkMode, error) {
	if mode == "" {
		mode = model.LinkModeAuto
	}
	switch mode {
	case model.LinkModeAuto, model.LinkModeHard, model.LinkModeSymlink, model.LinkModeCopy:
	default:
		return "", fmt.Errorf("invalid directory link mode %q", mode)
	}
	src, srcInfo, err := dirLinkSource(root, source)
	if err != nil {
		return "", err
	}
	dst, err := dirLinkDestination(root, target)
	if err != nil {
		return "", err
	}
	if err := ValidateDistinctPaths([]string{src}, []string{dst}); err != nil {
		return "", err
	}
	if err := validateSkillTree(root, src); err != nil {
		return "", err
	}
	entry, err := os.Lstat(dst)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect directory link %q: %w", dst, err)
	}
	if exists {
		// Compare only when the entry's kind can satisfy the requested mode.
		linked := isDirReparsePoint(entry, dst)
		if (linked && mode != model.LinkModeCopy) || (entry.IsDir() && !linked && mode == model.LinkModeCopy) {
			report, err := checkDirLink(root, src, srcInfo, dst, model.LinkReport{Target: target})
			if err == nil && (report.Status == model.LinkStatusOK &&
				(mode == model.LinkModeAuto || mode == model.LinkModeHard || mode == report.Mode) ||
				report.Status == model.LinkStatusCopy && mode == model.LinkModeCopy) {
				return report.Mode, nil
			}
			// A link can be safely replaced even if its destination is inaccessible.
			if err != nil && !linked {
				return "", err
			}
		}
		if !force {
			return "", fmt.Errorf("directory link destination %q: %w", dst, os.ErrExist)
		}
	}

	canonical, err := canonicalRoot(root)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(dst)
	if parent != canonical {
		rel, err := filepath.Rel(canonical, parent)
		if err != nil {
			return "", err
		}
		if err := EnsureDirWithinRoot(canonical, rel); err != nil {
			return "", err
		}
	}
	return stageDirLink(root, src, dst, mode, exists, symlink, junction)
}

func stageDirLink(root, src, dst string, mode model.LinkMode, replace bool, symlink, junction func(string, string) error) (used model.LinkMode, err error) {
	parent := filepath.Dir(dst)
	staged, err := os.MkdirTemp(parent, ".reifu.tmp.*")
	if err != nil {
		return "", fmt.Errorf("stage directory link %q: %w", dst, err)
	}
	defer func() { err = errors.Join(err, removeDirEntry(staged)) }()
	used = model.LinkModeCopy
	var symErr, junctionErr error
	if mode != model.LinkModeCopy {
		if err := removeTemp(staged); err != nil {
			return "", err
		}
		rel, err := filepath.Rel(parent, src)
		if err != nil {
			return "", fmt.Errorf("relative symlink from %q to %q: %w", dst, src, err)
		}
		symErr = retrySharing(func() error { return symlink(rel, staged) })
		switch {
		case symErr == nil:
			used = model.LinkModeSymlink
		case mode == model.LinkModeSymlink || !canFallback(symErr):
			return "", fmt.Errorf("symlink %q to %q: %w", dst, src, symErr)
		default:
			junctionErr = junction(staged, src)
			if junctionErr == nil {
				used = model.LinkModeJunction
			} else if !canFallback(junctionErr) && !junctionUnsupported(junctionErr) {
				return "", fmt.Errorf("junction %q to %q: %w", dst, src, junctionErr)
			} else if err := os.Mkdir(staged, 0700); err != nil {
				return "", fmt.Errorf("stage directory copy %q: %w", dst, err)
			}
		}
	}
	if used == model.LinkModeCopy {
		if err := copySkillTree(root, src, staged); err != nil {
			return "", fmt.Errorf("copy directory %q to %q: %w", src, dst, errors.Join(err, symErr, junctionErr))
		}
	}
	if err := publishDirLink(staged, dst, replace); err != nil {
		return "", err
	}
	if used == model.LinkModeCopy && mode != model.LinkModeCopy {
		fmt.Fprintf(os.Stderr, "reifu: directory links unavailable for %q, using a copy\n", dst)
	}
	return used, nil
}

func publishDirLink(staged, dst string, replace bool) (err error) {
	if !replace {
		return moveNewDirEntry(staged, dst)
	}
	backup, err := os.MkdirTemp(filepath.Dir(dst), ".reifu.tmp.*")
	if err != nil {
		return fmt.Errorf("reserve backup for %q: %w", dst, err)
	}
	defer func() { err = errors.Join(err, removeTemp(backup)) }()
	old := filepath.Join(backup, "previous")
	if err := moveNewDirEntry(dst, old); err != nil {
		return err
	}
	if err := moveNewDirEntry(staged, dst); err != nil {
		return errors.Join(err, moveNewDirEntry(old, dst))
	}
	return removeDirEntry(old)
}

// RemoveDirLink removes a destination entry. Callers must also guard any
// configured custom source against overlap before removing managed targets.
func RemoveDirLink(root, target string) error {
	dst, err := dirLinkDestination(root, target)
	if err != nil {
		return err
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return err
	}
	source := model.ReifuConfig{}.EffectiveSkillsSource()
	if err := ValidateDistinctPaths([]string{filepath.Join(canonical, filepath.FromSlash(source))}, []string{dst}); err != nil {
		return err
	}
	if resolved, err := dirLinkDestination(canonical, source); err == nil {
		if err := ValidateDistinctPaths([]string{resolved}, []string{dst}); err != nil {
			return err
		}
	}
	return removeDirEntry(dst)
}

func removeDirEntry(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect directory entry %q: %w", path, err)
	}
	if info.IsDir() && !isDirReparsePoint(info, path) {
		if err := retrySharing(func() error { return removeOrdinaryDir(path) }); err != nil {
			return fmt.Errorf("remove directory %q: %w", path, err)
		}
		return nil
	}
	return removeTemp(path)
}

var errNonRegularSkillEntry = errors.New("skill tree contains a link or special file")

func validateSkillTree(root, src string) error {
	return walkSkillTree(root, src, func(string, os.FileInfo) error { return nil })
}

func walkSkillTree(root, tree string, visit func(string, os.FileInfo) error) error {
	var canonical string
	if root != "" {
		var err error
		canonical, err = canonicalRoot(root)
		if err != nil {
			return err
		}
	}
	return filepath.WalkDir(tree, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		linked := isDirReparsePoint(info, path)
		if linked && canonical != "" {
			if _, err := resolveWithin(canonical, path, 0); err != nil {
				return fmt.Errorf("validate skill entry %q: %w", path, err)
			}
		}
		if err := visit(path, info); err != nil {
			return err
		}
		if linked && entry.IsDir() {
			return fs.SkipDir
		}
		return nil
	})
}

func sameDirContents(root, src, dst string) (bool, error) {
	index := func(guardRoot, tree string) (map[string]string, error) {
		files := make(map[string]string)
		err := walkSkillTree(guardRoot, tree, func(path string, info os.FileInfo) error {
			if isDirReparsePoint(info, path) || (!info.IsDir() && !info.Mode().IsRegular()) {
				return fmt.Errorf("%q: %w", path, errNonRegularSkillEntry)
			}
			if info.Mode().IsRegular() {
				rel, err := filepath.Rel(tree, path)
				if err != nil {
					return err
				}
				files[rel] = path
			}
			return nil
		})
		return files, err
	}
	a, err := index(root, src)
	if err != nil {
		return false, err
	}
	b, err := index("", dst)
	if errors.Is(err, errNonRegularSkillEntry) {
		return false, nil
	}
	if err != nil || len(a) != len(b) {
		return false, err
	}
	for rel, from := range a {
		to, ok := b[rel]
		if !ok {
			return false, nil
		}
		left, err := readLinkFile(from)
		if err != nil {
			return false, err
		}
		right, err := readLinkFile(to)
		if err != nil || !bytes.Equal(left, right) {
			return false, err
		}
	}
	return true, nil
}

func copySkillTree(root, src, dst string) error {
	type directory struct {
		path string
		mode os.FileMode
	}
	var dirs []directory
	err := walkSkillTree(root, src, func(path string, info os.FileInfo) error {
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		switch {
		case isDirReparsePoint(info, path):
			return fmt.Errorf("%q: %w", path, errNonRegularSkillEntry)
		case info.IsDir():
			if rel != "." {
				if err := os.Mkdir(out, 0700); err != nil {
					return err
				}
			}
			dirs = append(dirs, directory{out, info.Mode().Perm()})
			return nil
		case info.Mode().IsRegular():
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			staged, stageErr := stageFile(filepath.Dir(out), in, info.Mode().Perm())
			closeErr := in.Close()
			if stageErr != nil {
				return errors.Join(stageErr, closeErr)
			}
			return errors.Join(publish(staged, out, false), closeErr)
		default:
			return fmt.Errorf("%q: %w", path, errNonRegularSkillEntry)
		}
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].mode); err != nil {
			return err
		}
	}
	return nil
}
