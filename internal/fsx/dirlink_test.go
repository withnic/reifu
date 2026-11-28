package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/withnic/reifu/internal/model"
)

const skillSource = ".agents/skills/例"
const skillTarget = ".claude/skills/例"

func putSkillFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

func requireSkillFile(t *testing.T, root, name, content string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil || string(got) != content {
		t.Fatalf("%s = %q, %v, want %q", name, got, err, content)
	}
}

func installTestDirLink(t *testing.T, target, source string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if err := createJunction(target, source); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Symlink(source, target); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSkills(t *testing.T) {
	root := t.TempDir()
	source := ".agents/skills"
	if got, err := DiscoverSkills(root, source); err != nil || got != nil {
		t.Fatalf("missing source: %v, %v", got, err)
	}
	for _, path := range []string{"z/SKILL.md", "a/SKILL.md", ".hidden/SKILL.md", "none/README.md", "not-directory"} {
		putSkillFile(t, root, source+"/"+path, "")
	}
	outside := t.TempDir()
	putSkillFile(t, outside, "SKILL.md", "")
	installTestDirLink(t, filepath.Join(root, source, "external"), outside)
	installTestDirLink(t, filepath.Join(root, "external"), outside)
	got, err := DiscoverSkills(root, source)
	if err != nil || !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatalf("discover: %v, %v", got, err)
	}
	for _, path := range []string{"external", "external/missing", source + "/not-directory"} {
		if _, err := DiscoverSkills(root, path); err == nil {
			t.Fatalf("accepted source %q", path)
		}
	}
}

func TestDiscoverSkillsCaseCollision(t *testing.T) {
	root := t.TempDir()
	putSkillFile(t, root, "skills/a/SKILL.md", "")
	err := os.Mkdir(filepath.Join(root, "skills", "A"), 0755)
	if errors.Is(err, os.ErrExist) {
		// A case-insensitive volume represents both spellings as one entry.
		if got, err := DiscoverSkills(root, "skills"); err != nil || !reflect.DeepEqual(got, []string{"a"}) {
			t.Fatalf("discover: %v, %v", got, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	putSkillFile(t, root, "skills/A/SKILL.md", "")
	if _, err := DiscoverSkills(root, "skills"); err == nil {
		t.Fatal("accepted case-colliding names")
	}
}

func TestDirLinkAutoAndSafeRemoval(t *testing.T) {
	verifyDirLinkLifecycle(t, EnsureDirLink)
	if runtime.GOOS == "windows" {
		t.Run("native junction", func(t *testing.T) {
			verifyDirLinkLifecycle(t, func(root, source, target string, mode model.LinkMode, force bool) (model.LinkMode, error) {
				deny := func(string, string) error { return os.ErrPermission }
				got, err := ensureDirLink(root, source, target, mode, force, deny, createJunction)
				if err == nil && mode != model.LinkModeCopy && got != model.LinkModeJunction {
					t.Fatalf("native junction mode = %s", got)
				}
				return got, err
			})
		})
	}
}

func verifyDirLinkLifecycle(t *testing.T, ensure func(string, string, string, model.LinkMode, bool) (model.LinkMode, error)) {
	t.Helper()
	root := t.TempDir()
	putSkillFile(t, root, skillSource+"/SKILL.md", "skill")
	putSkillFile(t, root, skillSource+"/scripts/run", "script")

	mode, err := ensure(root, skillSource, skillTarget, model.LinkModeAuto, false)
	if err != nil || (mode != model.LinkModeSymlink && mode != model.LinkModeJunction) {
		t.Fatalf("auto mode = %s, %v", mode, err)
	}
	requireDirReport(t, root, skillSource, skillTarget, model.LinkStatusOK, mode)
	if got, err := ensure(root, skillSource, skillTarget, model.LinkModeHard, false); err != nil || got != mode {
		t.Fatalf("second ensure = %s, %v", got, err)
	}
	if got, err := ensure(root, skillSource, skillTarget, model.LinkModeCopy, true); err != nil || got != model.LinkModeCopy {
		t.Fatalf("replace link with copy = %s, %v", got, err)
	}
	requireSkillFile(t, root, skillSource+"/SKILL.md", "skill")
	requireSkillFile(t, root, skillSource+"/scripts/run", "script")
	if got, err := ensure(root, skillSource, skillTarget, model.LinkModeAuto, true); err != nil || got != mode {
		t.Fatalf("replace copy with link = %s, %v", got, err)
	}
	if err := RemoveDirLink(root, skillTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, skillTarget)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target remains: %v", err)
	}
	if err := RemoveDirLink(root, skillSource); err == nil {
		t.Fatal("removed a directory inside the default source")
	}
	requireSkillFile(t, root, skillSource+"/SKILL.md", "skill")
	requireSkillFile(t, root, skillSource+"/scripts/run", "script")
}

func requireDirReport(t *testing.T, root, source, target string, status model.LinkStatus, mode model.LinkMode) {
	t.Helper()
	report, err := CheckDirLink(root, source, target)
	if err != nil || report != (model.LinkReport{Target: target, Status: status, Mode: mode}) {
		t.Fatalf("report = %+v, %v, want %s/%s", report, err, status, mode)
	}
}

func TestDirLinkCopyAndForce(t *testing.T) {
	root := t.TempDir()
	const payload = "\x00\xff\r\n"
	putSkillFile(t, root, skillSource+"/SKILL.md", "skill")
	putSkillFile(t, root, skillSource+"/scripts/run", payload)
	requireDirReport(t, root, skillSource, skillTarget, model.LinkStatusMissing, "")
	putSkillFile(t, root, skillTarget+"/custom", "keep")
	if _, err := EnsureDirLink(root, skillSource, skillTarget, model.LinkModeCopy, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected non-destructive collision, got %v", err)
	}
	requireSkillFile(t, root, skillTarget+"/custom", "keep")
	for i := 0; i < 2; i++ {
		if mode, err := EnsureDirLink(root, skillSource, skillTarget, model.LinkModeCopy, i == 0); err != nil || mode != model.LinkModeCopy {
			t.Fatalf("copy = %s, %v", mode, err)
		}
	}
	requireSkillFile(t, root, skillTarget+"/scripts/run", payload)
	src, err := os.Stat(filepath.Join(root, skillSource, "scripts", "run"))
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.Stat(filepath.Join(root, skillTarget, "scripts", "run"))
	if err != nil || src.Mode().Perm() != dst.Mode().Perm() || os.SameFile(src, dst) {
		t.Fatalf("copy file info = %v, %v, source = %v", dst, err, src)
	}
	requireDirReport(t, root, skillSource, skillTarget, model.LinkStatusCopy, model.LinkModeCopy)
	putSkillFile(t, root, skillTarget+"/scripts/run", "changed")
	requireDirReport(t, root, skillSource, skillTarget, model.LinkStatusDifferent, "")
	if _, err := EnsureDirLink(root, skillSource, skillTarget, model.LinkModeCopy, true); err != nil {
		t.Fatal(err)
	}
	requireDirReport(t, root, skillSource, skillTarget, model.LinkStatusCopy, model.LinkModeCopy)
}

func TestDirLinkFallback(t *testing.T) {
	deny := func(string, string) error { return os.ErrPermission }
	for _, test := range []struct {
		name     string
		junction func(string, string) error
		mode     model.LinkMode
	}{
		{"junction", func(link, _ string) error { return os.Mkdir(link, 0755) }, model.LinkModeJunction},
		{"copy", deny, model.LinkModeCopy},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			putSkillFile(t, root, skillSource+"/SKILL.md", "skill")
			mode, err := ensureDirLink(root, skillSource, skillTarget, model.LinkModeAuto, false, deny, test.junction)
			if err != nil || mode != test.mode {
				t.Fatalf("fallback = %s, %v, want %s", mode, err, test.mode)
			}
			if test.mode == model.LinkModeCopy {
				requireSkillFile(t, root, skillTarget+"/SKILL.md", "skill")
			}
			putSkillFile(t, root, skillTarget+"/preserved", "original")
			if _, err := ensureDirLink(root, skillSource, skillTarget, model.LinkModeSymlink, true, deny, test.junction); !errors.Is(err, os.ErrPermission) {
				t.Fatalf("explicit symlink failure = %v", err)
			}
			requireSkillFile(t, root, skillTarget+"/preserved", "original")
		})
	}
}

func TestDirLinkRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	putSkillFile(t, root, skillSource+"/SKILL.md", "skill")
	putSkillFile(t, outside, "scripts/run", "external")
	installTestDirLink(t, filepath.Join(root, skillSource, "escape"), outside)
	putSkillFile(t, root, skillTarget+"/SKILL.md", "skill")
	for _, mode := range []model.LinkMode{model.LinkModeAuto, model.LinkModeCopy} {
		if _, err := EnsureDirLink(root, skillSource, skillTarget, mode, true); err == nil {
			t.Fatalf("%s accepted external source entry", mode)
		}
	}
	if _, err := CheckDirLink(root, skillSource, skillTarget); err == nil {
		t.Fatal("check accepted external source entry")
	}
	requireSkillFile(t, root, skillTarget+"/SKILL.md", "skill")
	requireSkillFile(t, outside, "scripts/run", "external")

	installTestDirLink(t, filepath.Join(root, "outside"), outside)
	if _, err := CheckDirLink(root, skillTarget, "outside"); err == nil {
		t.Fatal("check accepted external target link")
	}
	if err := RemoveDirLink(root, "outside/scripts"); err == nil {
		t.Fatal("removed through external parent")
	}
	if err := RemoveDirLink(root, "outside"); err != nil {
		t.Fatal(err)
	}
	putSkillFile(t, root, "obsolete/readme", "remove")
	installTestDirLink(t, filepath.Join(root, "obsolete", "outside"), outside)
	if err := RemoveDirLink(root, "obsolete"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "obsolete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete target remains: %v", err)
	}
	requireSkillFile(t, outside, "scripts/run", "external")
}

func TestDirLinkForceKeepsOtherLinkDestination(t *testing.T) {
	root := t.TempDir()
	putSkillFile(t, root, skillSource+"/SKILL.md", "skill")
	putSkillFile(t, root, "other/SKILL.md", "other")
	putSkillFile(t, root, "other/scripts/run", "other-script")
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(filepath.FromSlash(skillTarget))), 0755); err != nil {
		t.Fatal(err)
	}
	installTestDirLink(t, filepath.Join(root, filepath.FromSlash(skillTarget)), filepath.Join(root, "other"))
	report, err := CheckDirLink(root, skillSource, skillTarget)
	if err != nil || report.Status != model.LinkStatusDifferent {
		t.Fatalf("link to another directory = %+v, %v", report, err)
	}
	if _, err := EnsureDirLink(root, skillSource, skillTarget, model.LinkModeAuto, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected non-destructive collision, got %v", err)
	}
	for _, mode := range []model.LinkMode{model.LinkModeCopy, model.LinkModeAuto} {
		if _, err := EnsureDirLink(root, skillSource, skillTarget, mode, true); err != nil {
			t.Fatalf("%s force: %v", mode, err)
		}
		requireSkillFile(t, root, "other/SKILL.md", "other")
		requireSkillFile(t, root, "other/scripts/run", "other-script")
	}
	requireSkillFile(t, root, skillTarget+"/SKILL.md", "skill")
}

func TestRemoveDirLinkKeepsSources(t *testing.T) {
	root := t.TempDir()
	putSkillFile(t, root, skillSource+"/SKILL.md", "skill")
	putSkillFile(t, root, skillSource+"/scripts/run", "script")
	// An ordinary target directory holding a link back into the source:
	// only the link entry may be removed.
	putSkillFile(t, root, "stale/readme", "remove")
	installTestDirLink(t, filepath.Join(root, "stale", "into-source"), filepath.Join(root, filepath.FromSlash(skillSource)))
	if err := RemoveDirLink(root, "stale"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "stale")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale target remains: %v", err)
	}
	requireSkillFile(t, root, skillSource+"/SKILL.md", "skill")
	requireSkillFile(t, root, skillSource+"/scripts/run", "script")

	// A custom source is protected when the caller passes it.
	putSkillFile(t, root, "custom/skills/a/SKILL.md", "custom")
	for _, target := range []string{"custom/skills/a", "custom", "CUSTOM/skills"} {
		if err := RemoveDirLink(root, target, "custom/skills"); err == nil {
			t.Fatalf("removed %q overlapping a protected source", target)
		}
	}
	requireSkillFile(t, root, "custom/skills/a/SKILL.md", "custom")

	// A protected source reached through an in-project link is resolved.
	installTestDirLink(t, filepath.Join(root, "alias"), filepath.Join(root, "custom"))
	if err := RemoveDirLink(root, "custom/skills/a", "alias/skills"); err == nil {
		t.Fatal("removed a directory inside a linked protected source")
	}
	requireSkillFile(t, root, "custom/skills/a/SKILL.md", "custom")
}

func TestDiscoverSkillsSkipsUnusableNames(t *testing.T) {
	root := t.TempDir()
	names := []string{"ok", "a..b"}
	if runtime.GOOS != "windows" {
		// Windows cannot create these names, so only Unix exercises them.
		names = append(names, "a:b", "dot.", "space ")
	}
	for _, name := range names {
		putSkillFile(t, root, "skills/"+name+"/SKILL.md", "")
	}
	got, err := DiscoverSkills(root, "skills")
	if err != nil || !reflect.DeepEqual(got, []string{"ok"}) {
		t.Fatalf("discover: %v, %v", got, err)
	}
}

func TestDirLinkBroken(t *testing.T) {
	root := t.TempDir()
	mode := model.LinkModeSymlink
	if runtime.GOOS == "windows" {
		mode = model.LinkModeJunction
	}
	installTestDirLink(t, filepath.Join(root, "broken"), filepath.Join(root, "absent"))
	requireDirReport(t, root, "absent", "broken", model.LinkStatusBroken, mode)
	if err := RemoveDirLink(root, "broken"); err != nil {
		t.Fatal(err)
	}
}
