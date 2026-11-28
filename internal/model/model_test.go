package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestConfigValidation(t *testing.T) {
	base := ReifuConfig{Version: 1, Project: ProjectMeta{Name: "Example", Preset: "go-cli"}}
	checks := []struct {
		field  string
		mutate func(*ReifuConfig)
	}{
		{"version", func(c *ReifuConfig) { c.Version = 2 }},
		{"project.preset", func(c *ReifuConfig) { c.Project.Preset = "../bad" }},
		{"agents.targets[0]", func(c *ReifuConfig) { c.Agents.Targets = []AgentName{"other"} }},
		{"links.mode", func(c *ReifuConfig) { c.Links.Mode = "invalid" }},
		{"sandbox.mode", func(c *ReifuConfig) { c.Sandbox.Mode = "invalid" }},
		{"sandbox.approval_policy", func(c *ReifuConfig) { c.Sandbox.ApprovalPolicy = "invalid" }},
		{"sandbox.approval_policy", func(c *ReifuConfig) { c.Sandbox.Mode = SandboxDangerFullAccess }},
		{"sandbox.approval_policy", func(c *ReifuConfig) {
			c.Sandbox = SandboxModel{Mode: SandboxDangerFullAccess, ApprovalPolicy: ApprovalNever}
		}},
		{"permissions.allow_commands[0]", func(c *ReifuConfig) { c.Permissions.AllowCommands = []string{"go\nbuild"} }},
		{"permissions.allow_commands[0]", func(c *ReifuConfig) { c.Permissions.AllowCommands = []string{"  "} }},
		{"permissions.deny_commands[0]", func(c *ReifuConfig) { c.Permissions.DenyCommands = []string{"bad\rcommand"} }},
		{"permissions.deny_commands[0]", func(c *ReifuConfig) { c.Permissions.DenyCommands = []string{""} }},
		{"permissions.deny_paths[0]", func(c *ReifuConfig) { c.Permissions.DenyPaths = []string{"a\x00b"} }},
		{"permissions.deny_paths[0]", func(c *ReifuConfig) { c.Permissions.DenyPaths = []string{"\t"} }},
		{"agents.targets[1]", func(c *ReifuConfig) { c.Agents.Targets = []AgentName{AgentClaude, AgentClaude} }},
		{"links.targets[1]", func(c *ReifuConfig) { c.Links.Targets = []string{"CLAUDE.md", "./claude.md"} }},
		{"hooks.verify_command", func(c *ReifuConfig) { c.Hooks.VerifyCommand = "a\tb" }},
		{"hooks.verify_command_windows", func(c *ReifuConfig) { c.Hooks.VerifyCommandWindows = "a\x7fb" }},
		{"hooks.verify_command_unix", func(c *ReifuConfig) { c.Hooks.VerifyCommandUnix = "a\nb" }},
		{"project.name", func(c *ReifuConfig) { c.Project.Name = "a\x1bb" }},
		{"workflow.require_files[0]", func(c *ReifuConfig) { c.Workflow.RequireFiles = []string{"../go.mod"} }},
		{"workflow.commands[0].step", func(c *ReifuConfig) { c.Workflow.Commands = []CommandStep{{Step: "", Run: "go test"}} }},
		{"workflow.commands[0].run", func(c *ReifuConfig) { c.Workflow.Commands = []CommandStep{{Step: "Test", Run: "go\ntest"}} }},
		{"workflow.verify_steps[0]", func(c *ReifuConfig) { c.Workflow.VerifySteps = [][]string{{}} }},
		{"workflow.verify_steps[0][1]", func(c *ReifuConfig) { c.Workflow.VerifySteps = [][]string{{"go", "test\n"}} }},
		{"workflow.gitignore[0]", func(c *ReifuConfig) { c.Workflow.Gitignore = []string{"bad\nline"} }},
	}
	for _, check := range checks {
		t.Run(check.field, func(t *testing.T) {
			c := base
			check.mutate(&c)
			err := c.Validate()
			var failure *ExitError
			if !errors.As(err, &failure) || failure.Field != check.field || ExitCodeOf(err) != ExitGeneral {
				t.Fatalf("got %v, want validation error for %s", err, check.field)
			}
		})
	}
	for _, path := range []string{"", ".", "../out", "nested/../out", "/abs", "\\abs", "C:abs", "C:\\abs", "\\\\host\\share", "AGENTS.md", ".\\agents.MD", "AGENTS.md ", "nested/.. /out", "CON", "NUL.md", "sub/com1.txt", "lpt9"} {
		c := base
		c.Links.Targets = []string{path}
		if err := c.Validate(); err == nil {
			t.Errorf("accepted target %q", path)
		}
	}
	base.Links.Targets = []string{"nested/CLAUDE.md", "./GEMINI.md", "agent..md"}
	base.Agents.Targets = []AgentName{AgentCodex}
	base.Sandbox = SandboxModel{Mode: SandboxDangerFullAccess, ApprovalPolicy: ApprovalOnRequest}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	var defaults ReifuConfig
	defaults.ApplyDefaults()
	if defaults.Links.Mode != LinkModeAuto || defaults.Sandbox.Mode != SandboxWorkspaceWrite || defaults.Sandbox.ApprovalPolicy != ApprovalNever || len(defaults.Agents.Targets) != 3 {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
}

func TestSkillsDefaultsAndJSON(t *testing.T) {
	var config ReifuConfig
	if !config.SkillsEnabled() || config.EffectiveSkillsSource() != ".agents/skills" {
		t.Fatal("unexpected default skills settings")
	}
	if targets := config.EffectiveSkillsTargets([]AgentName{AgentClaude}); len(targets) != 1 || targets[0] != ".claude/skills" {
		t.Fatalf("Claude defaults = %v", targets)
	}
	if targets := config.EffectiveSkillsTargets([]AgentName{AgentCodex}); targets != nil {
		t.Fatalf("non-Claude defaults = %v", targets)
	}
	for _, value := range []any{config, ReifuState{}, StatusReport{}} {
		data, err := json.Marshal(value)
		if err != nil || strings.Contains(string(data), "skills") {
			t.Fatalf("optional field in %s: %v", data, err)
		}
	}
	if err := json.Unmarshal([]byte(`{"skills":{"enabled":false,"source":"custom","targets":[]}}`), &config); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(config)
	if err != nil || !strings.Contains(string(data), `"targets":[]`) {
		t.Fatalf("explicit empty targets lost: %s, %v", data, err)
	}
	targets := config.EffectiveSkillsTargets([]AgentName{AgentClaude})
	if config.SkillsEnabled() || config.EffectiveSkillsSource() != "custom" || targets == nil || len(targets) != 0 {
		t.Fatal("explicit skills settings were not respected")
	}
}

func TestSkillsValidation(t *testing.T) {
	base := ReifuConfig{Version: 1, Project: ProjectMeta{Preset: "minimal"}}
	for _, path := range []string{"/abs", "C:\\abs", "..", "part/../other", ".", "name\npart", "NUL.md"} {
		for _, skills := range []SkillsConfig{{Source: path}, {Targets: []string{path}}} {
			config := base
			config.Skills = skills
			if err := config.Validate(); err == nil {
				t.Errorf("accepted invalid path %q in %+v", path, skills)
			}
		}
	}
	for _, targets := range [][]string{
		{".AGENTS\\skills"}, {".agents"}, {".agents/skills/sub"}, {"out/skills", "./OUT\\skills"},
		{".claude/skills", ".claude/skills/nested"}, {"out/a/b", "OUT/a"},
	} {
		config := base
		config.Skills.Targets = targets
		if err := config.Validate(); err == nil {
			t.Errorf("accepted overlapping or duplicate target %v", targets)
		}
	}
	base.Skills.Targets = []string{".agents/skills-other"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	base.Links.Mode = LinkModeJunction
	if err := base.Validate(); err == nil {
		t.Fatal("accepted junction as a links.mode input")
	}
}

func TestPresetBoundary(t *testing.T) {
	for _, name := range []string{"A", "go-cli", "Custom_1", strings.Repeat("a", 64)} {
		if err := ValidatePresetName(name); err != nil {
			t.Errorf("rejected %q: %v", name, err)
		}
	}
	for _, name := range []string{"", "_first", "x.y", "x/y", "x\\y", "x\n", strings.Repeat("a", 65)} {
		if err := ValidatePresetName(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestStateAndExitCodes(t *testing.T) {
	var state ReifuState
	input := `{"version":1,"last_synced_at":"2026-11-26T14:00:00Z","master_sha256":"master","managed_commands":["go test"],"targets":{"claude":{"path":".claude/settings.json","exists":true,"file_sha256":"file","applied_allow_commands":["go test"],"applied_deny_commands":["bad"],"applied_deny_paths":["secret"],"applied_verify_command":"go vet"}}}`
	if err := json.Unmarshal([]byte(input), &state); err != nil {
		t.Fatal(err)
	}
	output, err := json.Marshal(state)
	if err != nil || string(output) != input {
		t.Fatalf("state round trip: %s (%v)", output, err)
	}
	cause := errors.New("cause")
	for _, code := range []ExitCode{ExitGeneral, ExitDrift, ExitLinkIntegrity} {
		err := fmt.Errorf("outer: %w", &ExitError{Code: code, Err: cause})
		if ExitCodeOf(err) != code || !errors.Is(err, cause) {
			t.Fatalf("unexpected wrapped error %v", err)
		}
	}
	if ExitCodeOf(nil) != ExitOK || ExitCodeOf(cause) != ExitGeneral {
		t.Fatal("incorrect exit code for nil or unstructured error")
	}
	if (&ExitError{Code: ExitGeneral, Field: "field"}).Error() != "field" {
		t.Fatal("nil wrapped error panicked or returned unexpected string")
	}
	report := StatusReport{
		Root:    "/repo",
		Env:     "windows",
		Links:   []LinkReport{{Target: "CLAUDE.md", Status: LinkStatusOK, Mode: LinkModeHard}},
		Targets: map[AgentName]TargetReport{AgentClaude: {Path: ".claude/settings.json", Status: StatusInSync}},
	}
	if raw, err := json.Marshal(report); err != nil || !strings.Contains(string(raw), `"IN_SYNC"`) {
		t.Fatalf("status report marshal: %s (%v)", raw, err)
	}
}

func TestRunnerConfigAndRunID(t *testing.T) {
	cfg := ReifuConfig{Version: 1, Project: ProjectMeta{Preset: "go-cli"}}
	data, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(data), `"runner"`) || cfg.Validate() != nil {
		t.Fatalf("legacy config: %s (%v)", data, err)
	}
	cfg.Runner = RunnerConfig{Implementer: "test-1", Reviewer: "Codex", Agents: map[string][]string{"test-1": {"test", ""}}}
	data, err = json.Marshal(cfg)
	var decoded ReifuConfig
	if err != nil || json.Unmarshal(data, &decoded) != nil || decoded.Runner.Implementer != "test-1" || decoded.Validate() != nil {
		t.Fatalf("runner config: %s (%v)", data, err)
	}
	for _, bad := range []RunnerConfig{
		{Implementer: "../bad"}, {Reviewer: "-bad"}, {Agents: map[string][]string{"bad/name": {"x"}}},
		{Agents: map[string][]string{"empty": {}}}, {Agents: map[string][]string{"blank": {"  "}}},
		{Agents: map[string][]string{"control": {"x", "a\rb", "\x00"}}}, {Agents: map[string][]string{"line": {"x", "\n"}}},
	} {
		cfg.Runner = bad
		if err := cfg.Validate(); err == nil {
			t.Errorf("accepted %#v", bad)
		}
	}
	for _, id := range []string{"", "../runs", "..\\runs", "/tmp", "C:run", "a\n", strings.Repeat("a", 65)} {
		if err := ValidateRunID(id); err == nil {
			t.Errorf("accepted run ID %q", id)
		}
	}
	for _, id := range []string{"20261128T120000Z", "A_-0", strings.Repeat("a", 64)} {
		if err := ValidateRunID(id); err != nil {
			t.Error(err)
		}
	}
}
