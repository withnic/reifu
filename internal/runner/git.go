package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var runtimePaths = []string{".reifu", ".reifu.run.json", ".reifu.state.json", ".reifu.lock", ".reifu.tmp.*", "BLOCKED_REPORT.md"}

func sourcePaths() []string {
	paths := []string{"--", "."}
	for _, path := range runtimePaths {
		paths = append(paths, ":!"+path, ":(exclude,icase)"+path)
	}
	return paths
}

func git(ctx context.Context, root string, args ...string) (string, error) {
	return gitWithStdin(ctx, root, nil, args...)
}

func gitWithStdin(ctx context.Context, root string, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %q in %q: %w %s", args, root, errors.Join(err, ctx.Err()), strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// stageChanges stages unstaged modifications, deletions, and untracked files
// outside .gitignore and runtimePaths without passing negative pathspecs to
// `git add`, which fails when excluded paths are also matched by .gitignore.
func stageChanges(ctx context.Context, root string) error {
	args := append([]string{"status", "--porcelain", "-z", "--untracked-files=all"}, sourcePaths()...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git %q in %q: %w %s", args, root, errors.Join(err, ctx.Err()), strings.TrimSpace(stderr.String()))
	}
	paths := parseUnstagedPorcelainZ(out)
	if len(paths) == 0 {
		return nil
	}
	var spec bytes.Buffer
	for _, p := range paths {
		spec.WriteString(p)
		spec.WriteByte(0)
	}
	_, err = gitWithStdin(ctx, root, spec.Bytes(), "add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
}

func parseUnstagedPorcelainZ(out []byte) []string {
	var paths []string
	for len(out) > 0 {
		nul := bytes.IndexByte(out, 0)
		if nul < 0 {
			break
		}
		entry := out[:nul]
		out = out[nul+1:]
		if len(entry) < 4 {
			continue
		}
		x, y := entry[0], entry[1]
		path1 := string(entry[3:])
		var path2 string
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			if nextNul := bytes.IndexByte(out, 0); nextNul >= 0 {
				path2 = string(out[:nextNul])
				out = out[nextNul+1:]
			}
		}
		if y == ' ' {
			continue
		}
		paths = append(paths, path1)
		if path2 != "" {
			paths = append(paths, path2)
		}
	}
	return paths
}

func IsGitRepo(ctx context.Context, root string) (bool, error) {
	out, err := git(ctx, root, "rev-parse", "--is-inside-work-tree")
	return out == "true", err
}

func StatusPorcelain(ctx context.Context, root string) (string, error) {
	return git(ctx, root, append([]string{"status", "--porcelain", "--untracked-files=all"}, sourcePaths()...)...)
}

// currentHead returns an empty commit for an unborn worktree.
func currentHead(ctx context.Context, root string) (string, error) {
	head, err := git(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD")
	var exit *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	return head, err
}

func HeadShortSHA(ctx context.Context, root string) (string, error) {
	return git(ctx, root, "rev-parse", "--short", "HEAD")
}

func DiffStat(ctx context.Context, root, baseCommit string) (string, error) {
	args := []string{"diff", "--stat"}
	if baseCommit != "" {
		args = append(args, "--end-of-options", baseCommit)
	}
	return git(ctx, root, append(args, sourcePaths()...)...)
}

func worktreeStat(ctx context.Context, root, base string) (string, error) {
	stat, err := DiffStat(ctx, root, base)
	if err != nil {
		return "", err
	}
	if base == "" {
		staged, err := git(ctx, root, append([]string{"diff", "--cached", "--stat"}, sourcePaths()...)...)
		if err != nil {
			return "", err
		}
		stat = strings.TrimSpace(staged + "\n" + stat)
	}
	untracked, err := git(ctx, root, append([]string{"ls-files", "--others", "--exclude-standard"}, sourcePaths()...)...)
	if untracked != "" {
		stat = strings.TrimSpace(stat + "\nUntracked files:\n" + untracked)
	}
	return stat, err
}

func hasDiff(ctx context.Context, root string, args ...string) (bool, error) {
	_, err := git(ctx, root, args...)
	var exit *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

func requireWorktreeRoot(ctx context.Context, root string) error {
	prefix, err := git(ctx, root, "rev-parse", "--show-prefix")
	if err != nil {
		return err
	}
	if prefix != "" {
		return fmt.Errorf("%q must be the root of the Git worktree for automatic commits", root)
	}
	return nil
}

func CommitAll(ctx context.Context, root, message string) (string, error) {
	if err := requireWorktreeRoot(ctx, root); err != nil {
		return "", err
	}
	// Refuse pre-staged runtime files instead of modifying the caller's index.
	args := []string{"diff", "--cached", "--quiet", "--"}
	for _, path := range runtimePaths {
		args = append(args, ":(icase)"+path)
	}
	dirty, err := hasDiff(ctx, root, args...)
	if err != nil {
		return "", err
	}
	if dirty {
		return "", fmt.Errorf("runtime files are already staged in %q; unstage them before committing", root)
	}
	if err := stageChanges(ctx, root); err != nil {
		return "", err
	}
	dirty, err = hasDiff(ctx, root, "diff", "--cached", "--quiet")
	if err != nil || !dirty {
		return "", err
	}
	if _, err := git(ctx, root, "commit", "-m", message); err != nil {
		return "", err
	}
	return HeadShortSHA(ctx, root)
}
