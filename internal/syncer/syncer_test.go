package syncer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/model"
)

func TestDetectTransitions(t *testing.T) {
	agent := model.AgentClaude
	original := []byte("original")
	state := &model.ReifuState{MasterSHA256: "master", Targets: map[model.AgentName]model.TargetState{
		agent: {Exists: true, FileSHA256: SHA256(original)},
	}}
	for _, tc := range []struct {
		want        model.TargetStatus
		state       *model.ReifuState
		master      string
		current     []byte
		dir, create bool
	}{
		{model.StatusSkipped, nil, "master", nil, false, false},
		{model.StatusStateMissing, nil, "master", nil, false, true},
		{model.StatusStateMissing, nil, "master", original, true, false},
		{model.StatusInSync, state, "master", original, true, false},
		{model.StatusMasterUpdated, state, "new", original, true, false},
		{model.StatusDrifted, state, "master", nil, true, false},
		{model.StatusDrifted, state, "master", []byte{}, true, false},
		{model.StatusConflict, state, "new", []byte("external"), true, false},
	} {
		if got := Detect(tc.state, agent, tc.master, tc.current, tc.dir, tc.create); got != tc.want {
			t.Errorf("got %s, want %s", got, tc.want)
		}
	}
}

const masterFixture = `{
  // top comment
  "version": 1,
  "project": {"preset":"minimal"},
  "permissions": {
    "allow_commands": [
      "go test *", // preserve inline
      /* preserve block */
    ],
    "deny_commands": ["git push *", "blocked *"]
  },
  "hooks": {"verify_command":"verify-common", "verify_command_windows":"verify-windows"},
}
`

func writeFixture(t *testing.T, root, name, data string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func readFixture(t *testing.T, root, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSyncBaselineConflictAndImport(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, MasterPath, masterFixture)
	claude := targetPaths[model.AgentClaude]
	gemini := targetPaths[model.AgentGemini]
	writeFixture(t, root, claude, `{"custom":true,"permissions":{"allow":["Bash(local only)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"manual"}]}]}}`)
	var warnings bytes.Buffer
	opts := Options{TargetOS: "windows", Warnings: &warnings}
	result, err := Sync(root, opts)
	if err != nil || !result.StateMissing || result.Targets[model.AgentGemini].Status != model.StatusSkipped ||
		!strings.Contains(warnings.String(), "verify-windows") {
		t.Fatalf("baseline: %+v, warnings %q, err %v", result, warnings.String(), err)
	}
	if data := readFixture(t, root, claude); !bytes.Contains(data, []byte("manual")) || !bytes.Contains(data, []byte("custom")) {
		t.Fatal(string(data))
	}
	opts.Create = true
	if _, err := Sync(root, opts); err != nil {
		t.Fatal(err)
	}
	master := strings.Replace(masterFixture, "go test *", "go vet *", 1)
	writeFixture(t, root, MasterPath, master)
	writeFixture(t, root, claude, `{"custom":true,"permissions":{"allow":["Bash(go test *)","Bash(go build *)","Bash(local only)","Read(local)"]}}`)
	writeFixture(t, root, gemini, `{"tools":{"allowed":["run_shell_command(go build *)","run_shell_command(go list *)"]}}`)
	before := readFixture(t, root, StatePath)
	report, err := Sync(root, opts)
	if model.ExitCodeOf(err) != model.ExitDrift || report.Targets[model.AgentClaude].Status != model.StatusConflict {
		t.Fatalf("expected conflict: %+v, %v", report, err)
	}
	if !bytes.Equal(before, readFixture(t, root, StatePath)) || string(readFixture(t, root, MasterPath)) != master {
		t.Fatal("conflict changed the master or state")
	}
	opts.Import = true
	opts.DryRun = true
	if plan, err := Sync(root, opts); err != nil || !slices.Contains(plan.ChangedPaths, MasterPath) ||
		string(readFixture(t, root, MasterPath)) != master {
		t.Fatalf("dry run: %+v, %v", plan, err)
	}
	opts.DryRun = false
	if _, err := Sync(root, opts); err != nil {
		t.Fatal(err)
	}
	data := readFixture(t, root, MasterPath)
	for _, comment := range []string{"// top comment", "// preserve inline", "/* preserve block */"} {
		if !bytes.Contains(data, []byte(comment)) {
			t.Fatalf("lost %q: %s", comment, data)
		}
	}
	var cfg model.ReifuConfig
	if err := jsonc.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Permissions.AllowCommands, []string{"go vet *", "go build *", "go list *"}) {
		t.Fatalf("wrong imported commands: %v", cfg.Permissions.AllowCommands)
	}
	for _, agent := range []model.AgentName{model.AgentClaude, model.AgentGemini} {
		data := readFixture(t, root, targetPaths[agent])
		if !bytes.Contains(data, []byte("go list *")) || bytes.Contains(data, []byte("go test *")) {
			t.Fatalf("wrong target rules in %s: %s", agent, data)
		}
		if bytes.Contains(data, []byte("local only")) != (agent == model.AgentClaude) {
			t.Fatalf("local rule was lost or distributed in %s: %s", agent, data)
		}
	}
	result, err = Inspect(root)
	for _, target := range result.Targets {
		if target.Status != model.StatusInSync {
			t.Errorf("expected in sync: %+v", target)
		}
	}
	if err != nil || len(result.Targets) != 3 {
		t.Fatalf("inspect: %+v, %v", result, err)
	}
	result, err = Sync(root, opts)
	if err != nil || len(result.ChangedPaths) != 0 {
		t.Fatalf("expected no-op: %+v, %v", result, err)
	}
	opts.NoHooks = true
	warnings.Reset()
	if _, err := Sync(root, opts); err != nil || warnings.Len() != 0 || bytes.Contains(readFixture(t, root, gemini), []byte("verify-windows")) {
		t.Fatalf("no-hooks: warnings=%s err=%v", &warnings, err)
	}
	opts.NoHooks = false
	writeFixture(t, root, MasterPath, strings.Replace(string(data), "verify-windows", "verify-updated", 1))
	if _, err := Sync(root, opts); err != nil || !strings.Contains(warnings.String(), "verify-updated") ||
		!bytes.Contains(readFixture(t, root, claude), []byte("verify-updated")) {
		t.Fatalf("updated hook: warnings=%s err=%v", &warnings, err)
	}
}

func TestSyncPreflightsAllTargets(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, MasterPath, masterFixture)
	claude, gemini := targetPaths[model.AgentClaude], targetPaths[model.AgentGemini]
	writeFixture(t, root, claude, "{}")
	writeFixture(t, root, gemini, "{")
	if _, err := Sync(root, Options{}); model.ExitCodeOf(err) != model.ExitGeneral {
		t.Fatalf("expected compile error: %v", err)
	}
	if string(readFixture(t, root, claude)) != "{}" {
		t.Fatal("an earlier target was written before the compile error")
	}
	if _, err := os.Stat(filepath.Join(root, StatePath)); !os.IsNotExist(err) {
		t.Fatalf("unexpected state after failed compilation: %v", err)
	}
}

func TestImportRejectsDangerousCommandsWithoutWriting(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, MasterPath, masterFixture)
	claude := targetPaths[model.AgentClaude]
	writeFixture(t, root, claude, "{}")
	if _, err := Sync(root, Options{NoHooks: true}); err != nil {
		t.Fatal(err)
	}
	before := readFixture(t, root, StatePath)
	for _, command := range []string{"", `""`, "*", "sh", "bash *", "zsh *", "pwsh *", "powershell *", "cmd *",
		"rm -rf *", "rm -rf*", "git push *", "git push*", "git -C repo push", "git -c user.name=x reset --hard",
		"git reset --hard*", "curl *", "wget *", "sudo go test *", "env bash *", "iex *", "(go test)", "echo ${SECRET}",
		"blocked arg", "bad\ncommand"} {
		t.Run(command, func(t *testing.T) {
			wrapped, err := json.Marshal("Bash(" + command + ")")
			if err != nil {
				t.Fatal(err)
			}
			external := `{"permissions":{"allow":["Bash(go list *)",` + string(wrapped) + `]}}`
			writeFixture(t, root, claude, external)
			var warnings bytes.Buffer
			_, err = Sync(root, Options{Import: true, NoHooks: true, Warnings: &warnings})
			if model.ExitCodeOf(err) != model.ExitDrift ||
				warnings.Len() == 0 ||
				string(readFixture(t, root, MasterPath)) != masterFixture ||
				string(readFixture(t, root, claude)) != external ||
				!bytes.Equal(before, readFixture(t, root, StatePath)) {
				t.Fatalf("unsafe import was not blocked atomically: warnings=%q err=%v", warnings.String(), err)
			}
		})
	}
	// Control characters remain rejected even with --force.
	writeFixture(t, root, claude, `{"permissions":{"allow":["Bash(bad\ncommand)"]}}`)
	if _, err := Sync(root, Options{Import: true, Force: true, NoHooks: true}); model.ExitCodeOf(err) != model.ExitDrift {
		t.Fatalf("expected control char rejected even with --force: %v", err)
	}
	// Dangerous/conflicting rules can be explicitly allowed with --force.
	writeFixture(t, root, claude, `{"permissions":{"allow":["Bash(curl *)"]}}`)
	if _, err := Sync(root, Options{Import: true, Force: true, NoHooks: true}); err != nil {
		t.Fatalf("expected --force import to succeed: %v", err)
	}
	if !bytes.Contains(readFixture(t, root, MasterPath), []byte(`"curl *"`)) {
		t.Fatal("forced import did not update master")
	}
}

func TestSyncBaselineAfterCloneDoesNotDuplicateHook(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, MasterPath, masterFixture)
	claude := targetPaths[model.AgentClaude]
	writeFixture(t, root, claude, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"verify-windows"}]}]}}`)
	var warnings bytes.Buffer
	if _, err := Sync(root, Options{TargetOS: "windows", Warnings: &warnings}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warnings.String(), "verify-windows") {
		t.Fatalf("expected hook warning on baseline sync: %q", warnings.String())
	}
	if got := readFixture(t, root, claude); bytes.Count(got, []byte("verify-windows")) != 1 {
		t.Fatalf("baseline sync duplicated hook: %s", got)
	}
}

func TestSyncSkills(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, MasterPath, masterFixture)
	run := func(options Options) Result {
		t.Helper()
		result, err := Sync(root, options)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	absent := func(name string) {
		t.Helper()
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should be absent: %v", name, err)
		}
	}
	run(Options{})
	if got := run(Options{}); len(got.ChangedPaths) != 0 || len(got.Warnings) != 0 {
		t.Fatalf("no skills: %+v", got)
	}
	absent(".agents/skills")
	absent(".claude")
	baseline := readFixture(t, root, StatePath)
	const source = ".agents/skills/my-skill"
	const dest = ".claude/skills/my-skill"
	const manual = ".claude/skills/claude-only/SKILL.md"
	writeFixture(t, root, source+"/SKILL.md", "# My skill\n")
	if got := run(Options{}); len(got.ChangedPaths) != 0 {
		t.Fatalf("missing agent directory: %+v", got)
	}
	if got := run(Options{Create: true, DryRun: true}); !slices.Contains(got.ChangedPaths, dest) {
		t.Fatalf("missing skill in preview: %+v", got)
	}
	absent(".claude")
	if !bytes.Equal(baseline, readFixture(t, root, StatePath)) {
		t.Fatal("preview changed the state")
	}

	writeFixture(t, root, manual, "# Claude only\n")
	if got := run(Options{}); !slices.Contains(got.ChangedPaths, dest) {
		t.Fatalf("missing skill in changes: %+v", got)
	}
	if got := readFixture(t, root, dest+"/SKILL.md"); string(got) != "# My skill\n" {
		t.Fatal(string(got))
	}
	state, err := readState(root)
	if err != nil || !slices.Equal(state.ManagedSkills[".claude/skills"], []string{"my-skill"}) {
		t.Fatalf("managed skills: %+v, %v", state, err)
	}
	if got := run(Options{}); len(got.ChangedPaths) != 0 {
		t.Fatalf("second sync: %+v", got)
	}
	for _, file := range []string{source + "/SKILL.md", source, ".agents/skills"} {
		if err := os.Remove(filepath.Join(root, file)); err != nil {
			t.Fatal(err)
		}
	}
	if got := run(Options{DryRun: true}); !slices.Contains(got.ChangedPaths, dest) {
		t.Fatalf("missing cleanup in preview: %+v", got)
	}
	if _, err := os.Lstat(filepath.Join(root, dest)); err != nil {
		t.Fatal(err)
	}
	if got := run(Options{}); !slices.Contains(got.ChangedPaths, dest) {
		t.Fatalf("missing cleanup in changes: %+v", got)
	}
	absent(dest)
	if got := readFixture(t, root, manual); string(got) != "# Claude only\n" {
		t.Fatal(string(got))
	}
	state, err = readState(root)
	if err != nil || len(state.ManagedSkills) != 0 {
		t.Fatalf("managed skills after cleanup: %+v, %v", state, err)
	}
}

func TestSyncSkillsDisabledAndForceCleanup(t *testing.T) {
	root := t.TempDir()
	const config = `{"version":1,"project":{"preset":"minimal"},"links":{"mode":"copy"},"skills":{"enabled":%s,"source":"shared"}}`
	writeFixture(t, root, MasterPath, fmt.Sprintf(config, "false"))
	writeFixture(t, root, "shared/example/SKILL.md", "# Skill\n")
	run := func(options Options) Result {
		t.Helper()
		result, err := Sync(root, options)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	const dest = ".claude/skills/example"
	run(Options{Create: true})
	if _, err := os.Lstat(filepath.Join(root, dest)); !os.IsNotExist(err) {
		t.Fatalf("disabled skills created destination: %v", err)
	}
	writeFixture(t, root, MasterPath, fmt.Sprintf(config, "true"))
	run(Options{})
	if err := os.Remove(filepath.Join(root, "shared/example/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	var warnings bytes.Buffer
	run(Options{Warnings: &warnings})
	if !strings.Contains(warnings.String(), dest) {
		t.Fatalf("missing retained skill warning: %s", &warnings)
	}
	readFixture(t, root, dest+"/SKILL.md")
	if got := run(Options{Force: true}); !slices.Contains(got.ChangedPaths, dest) {
		t.Fatalf("missing forced cleanup: %+v", got)
	}
	if _, err := os.Lstat(filepath.Join(root, dest)); !os.IsNotExist(err) {
		t.Fatalf("forced cleanup left destination: %v", err)
	}
}

func TestSyncSkillsProtectsCustomSourceFromAliasedTargets(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, MasterPath, `{"version":1,"project":{"preset":"minimal"},"skills":{"source":"shared","targets":["alias"]}}`)
	writeFixture(t, root, "shared/example/SKILL.md", "# Skill\n")
	if mode, err := fsx.EnsureDirLink(root, "shared", "alias", model.LinkModeAuto, false); err != nil || mode == model.LinkModeCopy {
		t.Fatalf("alias fixture: %s, %v", mode, err)
	}
	if _, err := Sync(root, Options{Force: true}); err == nil {
		t.Fatal("sync accepted a target alias of the custom source")
	}
	if got := readFixture(t, root, "shared/example/SKILL.md"); string(got) != "# Skill\n" {
		t.Fatal(string(got))
	}
}
