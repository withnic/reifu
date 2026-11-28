package compiler

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/withnic/reifu/internal/model"
)

func TestJSONThreeWayMerge(t *testing.T) {
	for _, tc := range []struct {
		name, initial, tool, event, section, allow, deny string
		compile                                          func([]byte, model.ReifuConfig, model.TargetState, string, bool) ([]byte, model.TargetState, error)
	}{
		{"claude", `{"permissions":{"allow":["Bash(old)","Bash(manual)"],"deny":["Bash(block-old)","Read(old-path)","Edit(old-path)","Write(old-path)","Read(manual-path)"],"ask":["Bash(ask)"]},"custom":9007199254740993,"hooks":{"Stop":[{"matcher":"keep","hooks":[{"type":"command","command":"old-hook"},{"type":"command","command":"manual-hook"}]}],"Start":[]}}`, "Bash", "Stop", "permissions", "allow", "deny", CompileClaude},
		{"gemini", `{"tools":{"allowed":["run_shell_command(old)","run_shell_command(manual)"],"exclude":["run_shell_command(block-old)","Read(old-path)"]},"custom":9007199254740993,"sandbox":true,"context":{"extra":true},"hooks":{"AfterAgent":[{"matcher":"keep","hooks":[{"type":"command","command":"old-hook"},{"type":"command","command":"manual-hook"}]}],"Start":[]}}`, "run_shell_command", "AfterAgent", "tools", "allowed", "exclude", CompileGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := model.TargetState{
				AppliedAllowCommands: []string{"old"}, AppliedDenyCommands: []string{"block-old"},
				AppliedDenyPaths: []string{"old-path"}, AppliedVerifyCommand: "old-hook",
			}
			cfg := model.ReifuConfig{
				Permissions: model.PermissionsModel{AllowCommands: []string{"new", "new"}, DenyCommands: []string{"block-new"}, DenyPaths: []string{"new-path"}},
				Hooks:       model.HooksModel{VerifyCommand: "common", VerifyCommandWindows: "windows-hook", VerifyCommandUnix: "unix-hook"},
			}
			out, applied, err := tc.compile([]byte(tc.initial), cfg, previous, "windows", false)
			if err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{tc.tool + "(new)", tc.tool + "(manual)", tc.tool + "(block-new)", "manual-hook", "windows-hook", "9007199254740993", "Start", "matcher"} {
				if !bytes.Contains(out, []byte(expected)) {
					t.Errorf("missing %s in %s", expected, out)
				}
			}
			for _, removed := range []string{tc.tool + "(old)", tc.tool + "(block-old)", "old-hook"} {
				if bytes.Contains(out, []byte(removed)) {
					t.Errorf("retained %s in %s", removed, out)
				}
			}
			if bytes.Count(out, []byte(tc.tool+"(new)")) != 1 || applied.AppliedVerifyCommand != "windows-hook" {
				t.Fatalf("unexpected applied settings: %s", out)
			}
			if tc.name == "claude" {
				for _, verb := range []string{"Read", "Edit"} {
					if !bytes.Contains(out, []byte(verb+"(new-path)")) || bytes.Contains(out, []byte(verb+"(old-path)")) {
						t.Errorf("incorrect %s rules: %s", verb, out)
					}
				}
				if bytes.Contains(out, []byte("Write(")) {
					t.Errorf("unexpected Write() rule in claude settings: %s", out)
				}
				if !bytes.Contains(out, []byte("Bash(ask)")) || !bytes.Contains(out, []byte("Read(manual-path)")) {
					t.Fatal(string(out))
				}
			} else if !bytes.Contains(out, []byte("AGENTS.md")) || !bytes.Contains(out, []byte("Read(old-path)")) ||
				!bytes.Contains(out, []byte(`"sandbox": true`)) || !bytes.Contains(out, []byte(`"extra": true`)) ||
				bytes.Contains(out, []byte("new-path")) || len(applied.AppliedDenyPaths) != 0 {
				t.Fatal(string(out))
			}
			without, removed, err := tc.compile(out, cfg, applied, "unix", true)
			if err != nil || removed.AppliedVerifyCommand != "" || bytes.Contains(without, []byte("windows-hook")) || !bytes.Contains(without, []byte("manual-hook")) {
				t.Fatalf("no-hooks: %s, %v", without, err)
			}
			added, _, err := tc.compile(without, cfg, removed, "unix", false)
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Hooks map[string][]struct{ Hooks []struct{ Command string } }
			}
			if err := json.Unmarshal(added, &parsed); err != nil {
				t.Fatal(err)
			}
			if groups := parsed.Hooks[tc.event]; len(groups) != 2 || groups[0].Hooks[0].Command != "unix-hook" {
				t.Fatalf("expected new group before manual hook: %s", added)
			}
		})
	}
}

func TestResolveVerifyCommandFallback(t *testing.T) {
	hooks := model.HooksModel{VerifyCommand: "common", VerifyCommandUnix: "unix"}
	if got := ResolveVerifyCommand(hooks, "windows", false); got != "common" {
		t.Fatal(got)
	}
	if got := ResolveVerifyCommand(hooks, "unix", true); got != "" {
		t.Fatal(got)
	}
}

func TestJSONBaselineDoesNotDuplicateExistingHook(t *testing.T) {
	initial := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"verify-cmd"}]}]}}`
	cfg := model.ReifuConfig{Hooks: model.HooksModel{VerifyCommand: "verify-cmd"}}
	out, applied, err := CompileClaude([]byte(initial), cfg, model.TargetState{}, "unix", false)
	if err != nil {
		t.Fatal(err)
	}
	if applied.AppliedVerifyCommand != "verify-cmd" || bytes.Count(out, []byte("verify-cmd")) != 1 {
		t.Fatalf("duplicate hook on baseline sync: %s", out)
	}
	// Removing the only hook cleans up the empty hooks object.
	cleared, _, err := CompileClaude(out, model.ReifuConfig{}, applied, "unix", false)
	if err != nil || bytes.Contains(cleared, []byte(`"hooks"`)) {
		t.Fatalf("expected empty hooks object removed: %s (%v)", cleared, err)
	}
	// Shell operators like && and 1>&2 must not be HTML-escaped to \u0026 / \u003e.
	shellCmd := "gofmt -s -w . && go vet ./... 1>&2 || exit 2"
	shellOut, _, err := CompileClaude(nil, model.ReifuConfig{Hooks: model.HooksModel{VerifyCommand: shellCmd}}, model.TargetState{}, "unix", false)
	if err != nil || !bytes.Contains(shellOut, []byte(shellCmd)) || bytes.Contains(shellOut, []byte(`\u0026`)) {
		t.Fatalf("expected unescaped shell command in JSON: %s (%v)", shellOut, err)
	}
}

func TestOSSpecificDenyAndSandbox(t *testing.T) {
	cfg := model.ReifuConfig{
		Sandbox: model.SandboxModel{
			Mode:          model.SandboxWorkspaceWrite,
			GeminiSandbox: true,
		},
	}
	// Windows: C:/Windows/** deny present, Claude native sandbox omitted.
	winOut, winState, err := CompileClaude(nil, cfg, model.TargetState{}, "windows", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(winOut, []byte("Edit(C:/Windows/**)")) || !bytes.Contains(winOut, []byte("Read(~/.ssh/**)")) || bytes.Contains(winOut, []byte(`"sandbox"`)) {
		t.Fatalf("unexpected windows claude settings:\n%s", winOut)
	}
	for _, expected := range []string{
		"Edit(.claude/settings.json)",
		"Edit(.codex/config.toml)",
		"Edit(.gemini/settings.json)",
		"Read(.claude/settings.local.json)", "Edit(.claude/settings.local.json)",
		"Read(.gemini/settings.local.json)", "Edit(.gemini/settings.local.json)",
	} {
		if !bytes.Contains(winOut, []byte(expected)) {
			t.Errorf("missing agent config guard %s in:\n%s", expected, winOut)
		}
	}
	if bytes.Contains(winOut, []byte("Write(")) {
		t.Errorf("unexpected Write() deny rule in windows claude settings:\n%s", winOut)
	}
	if bytes.Contains(winOut, []byte("Read(.claude/settings.json)")) {
		t.Errorf("shared settings.json should stay readable:\n%s", winOut)
	}
	// Migrating from legacy settings containing Write(...) rules removes them cleanly
	// even when previous state is empty (e.g. first sync after git clone), rewrites
	// standalone Write(<path>) rules to Edit(<path>), deduplicates against existing
	// Edit(<path>) rules regardless of order, and leaves bare "Write" intact.
	legacySettings := `{"permissions":{"deny":["Write(custom/only-write.txt)","Write(custom/dup-first.txt)","Edit(custom/dup-first.txt)","Edit(custom/dup-second.txt)","Write(custom/dup-second.txt)","Write","Read(.env)","Edit(.env)","Write(.env)","Edit(.claude/settings.json)","Write(.claude/settings.json)","Edit(C:/Windows/**)","Write(C:/Windows/**)","Edit(manual-only)"]}}`
	migrateCfg := model.ReifuConfig{
		Permissions: model.PermissionsModel{DenyPaths: []string{".env"}},
		Sandbox:     cfg.Sandbox,
	}
	for _, tc := range []struct {
		name  string
		state model.TargetState
	}{
		{"with-previous-state", model.TargetState{
			AppliedDenyPaths:   []string{".env"},
			AppliedOSDenyRules: []string{"Edit(.claude/settings.json)", "Write(.claude/settings.json)", "Edit(C:/Windows/**)", "Write(C:/Windows/**)"},
		}},
		{"empty-previous-state", model.TargetState{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			migratedOut, _, err := CompileClaude([]byte(legacySettings), migrateCfg, tc.state, "windows", false)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(migratedOut, []byte("Write(")) {
				t.Fatalf("legacy Write() rules were not cleaned up:\n%s", migratedOut)
			}
			for _, keep := range []string{
				"Edit(custom/only-write.txt)",
				"Edit(custom/dup-first.txt)",
				"Edit(custom/dup-second.txt)",
				`"Write"`,
				"Read(.env)", "Edit(.env)",
				"Edit(.claude/settings.json)", "Edit(C:/Windows/**)",
				"Edit(manual-only)",
			} {
				if !bytes.Contains(migratedOut, []byte(keep)) {
					t.Errorf("expected %s to remain after migration:\n%s", keep, migratedOut)
				}
			}
			for _, unique := range []string{"Edit(custom/dup-first.txt)", "Edit(custom/dup-second.txt)", "Edit(.env)", "Edit(.claude/settings.json)"} {
				if n := bytes.Count(migratedOut, []byte(`"`+unique+`"`)); n != 1 {
					t.Errorf("expected exactly 1 occurrence of %s, got %d:\n%s", unique, n, migratedOut)
				}
			}
		})
	}
	// Switching from Windows to macOS replaces Windows system paths with macOS ones and enables Claude sandbox.
	macOut, macState, err := CompileClaude(winOut, cfg, winState, "darwin", false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(macOut, []byte("C:/Windows/**")) || !bytes.Contains(macOut, []byte("Edit(/System/**)")) || !bytes.Contains(macOut, []byte(`"enabled": true`)) {
		t.Fatalf("unexpected darwin claude settings:\n%s", macOut)
	}
	// Switching to danger-full-access removes managed sandbox.enabled cleanly.
	dangerCfg := model.ReifuConfig{Sandbox: model.SandboxModel{Mode: model.SandboxDangerFullAccess, ApprovalPolicy: model.ApprovalOnRequest}}
	dangerOut, _, err := CompileClaude(macOut, dangerCfg, macState, "darwin", false)
	if err != nil || bytes.Contains(dangerOut, []byte(`"sandbox"`)) {
		t.Fatalf("expected sandbox removed under danger-full-access:\n%s (%v)", dangerOut, err)
	}
	// WSL2 includes /mnt/c/Windows/** deny.
	wslOut, _, err := CompileClaude(nil, cfg, model.TargetState{}, "wsl2-ext4", false)
	if err != nil || !bytes.Contains(wslOut, []byte("Edit(/mnt/c/Windows/**)")) || !bytes.Contains(wslOut, []byte(`"enabled": true`)) {
		t.Fatalf("unexpected wsl2 claude settings:\n%s (%v)", wslOut, err)
	}
	// Gemini opt-in sandbox: disabled on Windows, enabled on darwin.
	gemWin, _, err := CompileGemini(nil, cfg, model.TargetState{}, "windows", false)
	if err != nil || bytes.Contains(gemWin, []byte(`"sandbox": true`)) {
		t.Fatalf("gemini sandbox should stay off on windows:\n%s (%v)", gemWin, err)
	}
	gemMac, _, err := CompileGemini(nil, cfg, model.TargetState{}, "darwin", false)
	if err != nil || !bytes.Contains(gemMac, []byte(`"sandbox": true`)) {
		t.Fatalf("gemini opt-in sandbox expected on darwin:\n%s (%v)", gemMac, err)
	}
}

func TestCodexPreservesTOML(t *testing.T) {
	input := "# root\napproval_policy  = 'untrusted'  # policy\n" +
		"message = \"\"\"\n[sandbox_workspace_write]\nnetwork_access = true\n\"\"\"\n" +
		"multiline = [\n  \"literal # hash\",\n]\n" +
		"['sandbox_workspace_write'] # section\n  \"network_access\" = true # net\nother = 'keep'\n" +
		"[[mcp_servers.custom]]\napproval_policy = \"keep\" # other table\n"
	cfg := model.ReifuConfig{}
	out, err := CompileCodex([]byte(input), cfg)
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.NewReplacer(
		"'untrusted'", "\"never\"",
		"['sandbox_workspace_write']", "sandbox_mode = \"workspace-write\"\n['sandbox_workspace_write']",
		"true # net", "false # net",
	).Replace(input)
	if string(out) != expected {
		t.Fatalf("got:\n%s\nwant:\n%s", out, expected)
	}
	again, err := CompileCodex(out, cfg)
	if err != nil || !bytes.Equal(again, out) {
		t.Fatalf("not idempotent: %s, %v", again, err)
	}
}

func TestCodexRejectsInjection(t *testing.T) {
	for _, sandbox := range []model.SandboxModel{
		{Mode: "workspace-write\"\n[unexpected]"},
		{ApprovalPolicy: "never\r\nnetwork_access=true"},
		{Mode: model.SandboxDangerFullAccess, ApprovalPolicy: model.ApprovalNever},
	} {
		if out, err := CompileCodex(nil, model.ReifuConfig{Sandbox: sandbox}); err == nil || out != nil {
			t.Fatalf("accepted invalid sandbox %+v", sandbox)
		}
	}
}

func TestCodexDottedWorkspace(t *testing.T) {
	input := "sandbox_workspace_write.writable_roots = [] # keep\n[unrelated]\nnetwork_access = true\n"
	out, err := CompileCodex([]byte(input), model.ReifuConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "sandbox_workspace_write.network_access = false\n[unrelated]\nnetwork_access = true") ||
		!strings.Contains(string(out), "sandbox_workspace_write.writable_roots = [] # keep") {
		t.Fatal(string(out))
	}
	again, err := CompileCodex(out, model.ReifuConfig{})
	if err != nil || !bytes.Equal(again, out) {
		t.Fatalf("not idempotent: %s, %v", again, err)
	}
}

func TestCodexNewAndInsertIntoExistingWorkspace(t *testing.T) {
	cfg := model.ReifuConfig{
		Sandbox: model.SandboxModel{
			Mode:           model.SandboxReadOnly,
			ApprovalPolicy: model.ApprovalOnRequest,
			NetworkAccess:  true,
		},
	}
	created, err := CompileCodex(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantNew := "approval_policy = \"on-request\"\nsandbox_mode = \"read-only\"\n\n[sandbox_workspace_write]\nnetwork_access = true\n"
	if string(created) != wantNew {
		t.Fatalf("new codex:\n%s\nwant:\n%s", created, wantNew)
	}

	existing := "[sandbox_workspace_write]\nwritable_roots = []\n\n[other]\nkey = 1\n"
	updated, err := CompileCodex([]byte(existing), cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantUpdated := "approval_policy = \"on-request\"\nsandbox_mode = \"read-only\"\n[sandbox_workspace_write]\nwritable_roots = []\n\nnetwork_access = true\n[other]\nkey = 1\n"
	if string(updated) != wantUpdated {
		t.Fatalf("updated codex:\n%s\nwant:\n%s", updated, wantUpdated)
	}
}
