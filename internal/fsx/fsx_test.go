package fsx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/withnic/reifu/internal/model"
)

func TestPathBoundary(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFixture(t, outside, "outside.md", "outside")
	for _, name := range []string{"../escape", "sub/../file", "sub\\..\\file", "sub/.. /file", outside, "C:relative", "\\\\server\\share", "file:stream", "", "."} {
		if err := ValidateWithinRoot(root, name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	if err := ValidateWithinRoot(root, "new/sub/file"); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(root, "external")
	makeDirectoryLink(t, outside, dirLink)
	if err := ValidateWithinRoot(root, "external/outside.md"); err == nil {
		t.Fatal("accepted external symlink")
	}
	if err := WriteFileAtomic(root, "external/new", []byte("unsafe")); err == nil {
		t.Fatal("wrote through external symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatalf("external path was touched: %v", err)
	}
	writeFixture(t, root, "AGENTS.md", "agents")
	if _, err := EnsureLink(root, "AGENTS.md", "external/outside.md", model.LinkModeHard, true); err == nil {
		t.Fatal("linked through external symlink")
	}
	if _, err := EnsureLink(root, "external/outside.md", "alias.md", model.LinkModeHard, true); err == nil {
		t.Fatal("used external symlink as source")
	}
	assertFile(t, outside, "outside.md", "outside")
	dir := filepath.Join(root, "nested")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	makeDirectoryLink(t, dir, filepath.Join(root, "local"))
	if err := WriteFileAtomic(root, "local/new.txt", []byte("inside")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, dir, "new.txt", "inside")
	if err := EnsureDirWithinRoot(root, ".claude/hooks"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDirWithinRoot(root, ".claude/hooks"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDirWithinRoot(root, "external/sub"); err == nil {
		t.Fatal("created directory through external symlink")
	}
	if err := EnsureDirWithinRoot(root, "AGENTS.md/sub"); err == nil {
		t.Fatal("accepted file component as directory")
	}

	// Absolute link targeting an aliased path to root (like macOS /var -> /private/var
	// or Windows 8.3 short paths) must resolve cleanly within canonical root.
	aliasBase := t.TempDir()
	realRoot := filepath.Join(aliasBase, "real")
	aliasRoot := filepath.Join(aliasBase, "alias")
	if err := os.MkdirAll(filepath.Join(realRoot, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	makeDirectoryLink(t, realRoot, aliasRoot)
	makeDirectoryLink(t, filepath.Join(aliasRoot, "nested"), filepath.Join(realRoot, "via-alias"))
	if err := WriteFileAtomic(aliasRoot, "via-alias/ok.txt", []byte("canonical")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(realRoot, "nested"), "ok.txt", "canonical")
}

func TestWriteFileAtomic(t *testing.T) {
	root := t.TempDir()
	if err := WriteFileAtomic(root, "name.txt", []byte("\xef\xbb\xbf日本語\r\nsecond\rthird")); err != nil {
		t.Fatal(err)
	}
	assertFile(t, root, "name.txt", "日本語\nsecond\nthird")
	path := filepath.Join(root, "name.txt")
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(root, "name.txt", []byte("updated")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode changed to %o", info.Mode().Perm())
	}
	if err := WriteFileAtomic(root, "name.txt", []byte{0xff}); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	assertFile(t, root, "name.txt", "updated")
	dir := filepath.Join(root, "dir")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	staged, err := stageFile(root, strings.NewReader("new"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := publish(staged, dir, true); err == nil {
		t.Fatal("replaced directory with regular file")
	}
	assertNoTemps(t, root)
}

func TestEnsureLink(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "AGENTS.md", "canonical")
	writeFixture(t, root, "CLAUDE.md", "edited")
	if _, err := EnsureLink(root, "AGENTS.md", "CLAUDE.md", model.LinkModeHard, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected refusal to replace: %v", err)
	}
	assertFile(t, root, "CLAUDE.md", "edited")
	for i := 0; i < 2; i++ {
		mode, err := EnsureLink(root, "AGENTS.md", "CLAUDE.md", model.LinkModeHard, i == 0)
		if err != nil || mode != model.LinkModeHard {
			t.Fatalf("hard link: %q (%v)", mode, err)
		}
	}
	src, err := os.Stat(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Stat(filepath.Join(root, "CLAUDE.md"))
	if err != nil || !os.SameFile(src, dst) {
		t.Fatalf("not a hard link: %v", err)
	}
	rep, err := CheckLink(root, "AGENTS.md", "CLAUDE.md")
	if err != nil || rep.Status != model.LinkStatusOK || rep.Mode != model.LinkModeHard {
		t.Fatalf("check hard link: %+v (%v)", rep, err)
	}
	// Explicit mode conversion from hard link to copy must require force and replace the link.
	if _, err := EnsureLink(root, "AGENTS.md", "CLAUDE.md", model.LinkModeCopy, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected mode mismatch refusal: %v", err)
	}
	if mode, err := EnsureLink(root, "AGENTS.md", "CLAUDE.md", model.LinkModeCopy, true); err != nil || mode != model.LinkModeCopy {
		t.Fatalf("convert hard link to copy: %q (%v)", mode, err)
	}
	rep, err = CheckLink(root, "AGENTS.md", "CLAUDE.md")
	if err != nil || rep.Status != model.LinkStatusCopy || rep.Mode != model.LinkModeCopy {
		t.Fatalf("check copy link: %+v (%v)", rep, err)
	}
	if _, err := EnsureLink(root, "AGENTS.md", "AGENTS.md", model.LinkModeHard, true); err == nil {
		t.Fatal("accepted linking file to itself")
	}
	assertNoTemps(t, root)
}

func TestCheckLinkStates(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "AGENTS.md", "canonical\n")
	rep, err := CheckLink(root, "AGENTS.md", "MISSING.md")
	if err != nil || rep.Status != model.LinkStatusMissing {
		t.Fatalf("missing link: %+v (%v)", rep, err)
	}
	writeFixture(t, root, "DIFF.md", "other\n")
	rep, err = CheckLink(root, "AGENTS.md", "DIFF.md")
	if err != nil || rep.Status != model.LinkStatusDifferent {
		t.Fatalf("different link: %+v (%v)", rep, err)
	}
	writeFixture(t, root, "COPY.md", "canonical\n")
	rep, err = CheckLink(root, "AGENTS.md", "COPY.md")
	if err != nil || rep.Status != model.LinkStatusCopy || rep.Mode != model.LinkModeCopy {
		t.Fatalf("copy link: %+v (%v)", rep, err)
	}
	if err := WithFileLock(filepath.Join(root, "nonexistent"), func() error { return nil }); err == nil {
		t.Fatal("lock succeeded on missing root")
	}
}

func TestEnsureLinkFallback(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "AGENTS.md", "canonical")
	writeFixture(t, root, "GEMINI.md", "existing")
	var attempts []string
	hard := func(_, path string) error {
		attempts = append(attempts, "hard")
		return &os.LinkError{Op: "link", New: path, Err: syscall.EXDEV}
	}
	symlink := func(rel, path string) error {
		attempts = append(attempts, "symlink")
		if rel != "AGENTS.md" {
			t.Errorf("relative link %q", rel)
		}
		return &os.LinkError{Op: "symlink", New: path, Err: os.ErrPermission}
	}
	mode, err := ensureLink(root, "AGENTS.md", "GEMINI.md", model.LinkModeAuto, true, hard, symlink)
	if err != nil || mode != model.LinkModeCopy || strings.Join(attempts, ",") != "hard,symlink" {
		t.Fatalf("fallback: %q %v %v", mode, err, attempts)
	}
	assertFile(t, root, "GEMINI.md", "canonical")
	mode, err = EnsureLink(root, "AGENTS.md", "GEMINI.md", model.LinkModeCopy, false)
	if err != nil || mode != model.LinkModeCopy {
		t.Fatalf("copy idempotency: %q %v", mode, err)
	}
	assertNoTemps(t, root)
}

func TestCopyPublication(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "AGENTS.md", "original\r\n")
	mode, err := EnsureLink(root, "AGENTS.md", "copy.md", model.LinkModeCopy, false)
	if err != nil || mode != model.LinkModeCopy {
		t.Fatalf("new copy: %q (%v)", mode, err)
	}
	assertFile(t, root, "copy.md", "original\n")

	staged, err := stageFile(root, strings.NewReader("late copy"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err := publish(staged, filepath.Join(root, "copy.md"), false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing copy to be protected: %v", err)
	}
	assertFile(t, root, "copy.md", "original\n")
	assertNoTemps(t, root)
}

func TestForceLinkFailurePreservesDestination(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "AGENTS.md", "canonical")
	writeFixture(t, root, "GEMINI.md", "existing")
	failedHardLink := func(_, _ string) error { return syscall.EXDEV }
	_, err := ensureLink(root, "AGENTS.md", "GEMINI.md", model.LinkModeHard, true, failedHardLink, os.Symlink)
	if !errors.Is(err, syscall.EXDEV) {
		t.Fatalf("expected forced hard link to fail: %v", err)
	}
	assertFile(t, root, "GEMINI.md", "existing")
	assertNoTemps(t, root)
}

func TestFileLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".reifu.lock")
	writeFixture(t, root, ".reifu.lock", "expired")
	expired := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, expired, expired); err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("callback result")
	err := WithFileLock(root, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		metadata := strings.Fields(string(data))
		if len(metadata) != 2 || metadata[0] != fmt.Sprint(os.Getpid()) {
			return fmt.Errorf("unexpected lock metadata %q", data)
		}
		if parsed, err := time.Parse(time.RFC3339Nano, metadata[1]); err != nil || !strings.HasSuffix(parsed.Format(time.RFC3339Nano), "Z") {
			return fmt.Errorf("unexpected lock timestamp %q", metadata[1])
		}
		if err := os.Chtimes(path, expired, expired); err != nil {
			return err
		}
		err = withFileLock(root, 80*time.Millisecond, func() error {
			t.Error("contender ran while stale-looking lock was active")
			return nil
		})
		if !errors.Is(err, ErrLockTimeout) {
			return fmt.Errorf("expected lock timeout: %v", err)
		}
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("lock callback result: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("lock not cleaned up: %v", err)
	}
	if err := WithFileLock(root, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func writeFixture(t *testing.T, root, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || string(data) != want {
		t.Fatalf("%s: got %q (%v), want %q", name, data, err, want)
	}
}

func assertNoTemps(t *testing.T, root string) {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(root, ".reifu.tmp.*"))
	if err != nil || len(names) != 0 {
		t.Fatalf("temporary files: %v (%v)", names, err)
	}
}

func makeDirectoryLink(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return
	}
	if runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(1314)) {
		t.Fatal(err)
	}
	// Creating a junction does not require the Windows symlink privilege.
	// Both exercise the filesystem boundary with real redirections.
	cmd := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v (%s)", err, output)
	}
}
