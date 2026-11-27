package syncer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
