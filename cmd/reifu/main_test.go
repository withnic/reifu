package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/withnic/reifu/internal/app"
	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/template"
)

type cliTest struct {
	t                               *testing.T
	root, presets, workDir, homeDir string
}

func (s cliTest) run(want model.ExitCode, args ...string) (string, string) {
	s.t.Helper()
	var stdout, stderr bytes.Buffer
	cli := app.CLI{Stdout: &stdout, Stderr: &stderr, Presets: template.Presets{Dir: s.presets}, WorkDir: s.workDir, HomeDir: s.homeDir}
	if got := cli.Run(args); got != want {
		s.t.Fatalf("reifu %q exited %d, want %d\nstdout: %s\nstderr: %s", args, got, want, &stdout, &stderr)
	}
	return stdout.String(), stderr.String()
}

func (s cliTest) read(name string) string {
	s.t.Helper()
	data, err := os.ReadFile(filepath.Join(s.root, name))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(data)
}

func (s cliTest) write(name, contents string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.root, name), []byte(contents), 0644); err != nil {
		s.t.Fatal(err)
	}
}

func (s cliTest) remove(name string) {
	s.t.Helper()
	if err := os.Remove(filepath.Join(s.root, name)); err != nil {
		s.t.Fatal(err)
	}
}

func contains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("missing %q in %q", want, got)
	}
}

func missing(t *testing.T, file string) {
	t.Helper()
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("%s should be absent, stat error: %v", file, err)
	}
}

func TestProjectWorkflow(t *testing.T) {
	tmp := t.TempDir()
	s := cliTest{t: t, root: filepath.Join(tmp, "project"), presets: filepath.Join(tmp, "presets")}
	_, warnings := s.run(0, "init", s.root, "--name", "phase-3", "--preset", "go-cli", "--link-mode", "hard")
	contains(t, warnings, "applying verification hook")
	contains(t, s.read("AGENTS.md"), "go test ./...")
	contains(t, s.read("prompts/phase0.md"), "{{ .Name }}")
	for _, entry := range []string{".reifu.state.json", ".reifu.lock", ".reifu.tmp.*"} {
		contains(t, s.read(".gitignore"), entry)
	}

	out, _ := s.run(0, "status", s.root)
	contains(t, out, "IN_SYNC")
	out, stderr := s.run(0, "status", "--json", s.root)
	if stderr != "" {
		t.Fatalf("unexpected status stderr: %s", stderr)
	}
	decoder := json.NewDecoder(strings.NewReader(out))
	var report model.StatusReport
	if err := decoder.Decode(&report); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("status produced extra data: %v", err)
	}
	if report.Root != s.root || report.Env == "" || report.StateMissing || report.HasIssues ||
		len(report.Links) != 2 || len(report.Targets) != 3 {
		t.Fatalf("unexpected initial status: %+v", report)
	}
	for _, link := range report.Links {
		if link.Status != model.LinkStatusOK || link.Mode != model.LinkModeHard {
			t.Errorf("unexpected link report: %+v", link)
		}
	}
	for _, target := range report.Targets {
		if target.Path == "" || target.Status != model.StatusInSync {
			t.Errorf("unexpected target report: %+v", target)
		}
	}
	out, stderr = s.run(0, "status", s.root, "--quiet")
	if out != "" || stderr != "" {
		t.Fatalf("quiet status wrote output: %q / %q", out, stderr)
	}

	addAllow := func(rule string) {
		t.Helper()
		raw := s.read(".claude/settings.json")
		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &settings); err != nil {
			t.Fatal(err)
		}
		var permissions map[string]json.RawMessage
		if err := json.Unmarshal(settings["permissions"], &permissions); err != nil {
			t.Fatal(err)
		}
		var allow []string
		if err := json.Unmarshal(permissions["allow"], &allow); err != nil {
			t.Fatal(err)
		}
		permissions["allow"], _ = json.Marshal(append(allow, rule))
		settings["permissions"], _ = json.Marshal(permissions)
		data, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		s.write(".claude/settings.json", string(data)+"\n")
	}

	const master = ".reifu.jsonc"
	addAllow("Bash(go get *)")
	originalMaster := s.read(master)
	s.run(2, "status", s.root, "--quiet")
	_, stderr = s.run(2, "sync", s.root)
	contains(t, stderr, "DRIFTED")
	if s.read(master) != originalMaster {
		t.Fatal("drift guard changed the master")
	}

	codex := s.read(".codex/config.toml") + "\n# local Codex preference\n"
	s.write(".codex/config.toml", codex)
	editedMaster := strings.Replace(originalMaster, "\"workspace-write\"", "\"read-only\"", 1)
	s.write(master, editedMaster)
	s.run(0, "sync", s.root, "--import", "--dry-run")
	if s.read(master) != editedMaster {
		t.Fatal("dry run rewrote the master")
	}
	s.run(0, "sync", "--import", s.root)
	contains(t, s.read(master), "// Add project commands here")
	contains(t, s.read(master), "\"go get *\"")
	contains(t, s.read(".gemini/settings.json"), "run_shell_command(go get *)")
	contains(t, s.read(".codex/config.toml"), "sandbox_mode = \"read-only\"")
	contains(t, s.read(".codex/config.toml"), "# local Codex preference")

	s.remove(".reifu.state.json")
	out, _ = s.run(0, "status", s.root)
	contains(t, out, "STATE_MISSING")
	s.run(2, "status", s.root, "--quiet")
	s.run(0, "sync", s.root)
	contains(t, s.read(".reifu.state.json"), "go get *")
	s.run(0, "status", s.root, "--quiet")

	s.remove("CLAUDE.md")
	s.write("CLAUDE.md", s.read("AGENTS.md"))
	s.run(3, "link", "--check", s.root)
	s.run(2, "status", "--quiet", s.root)
	s.run(1, "link", s.root)
	s.run(0, "link", s.root, "--force")
	s.run(0, "link", s.root, "--check")
	s.run(0, "status", s.root, "--quiet")

	addAllow("Bash(git push *)")
	before := s.read(master)
	_, stderr = s.run(2, "sync", s.root, "--import")
	contains(t, stderr, "git push")
	if s.read(master) != before {
		t.Fatal("guarded rule changed the master")
	}

	out, _ = s.run(0, "preset", "list")
	for _, name := range []string{"go-cli", "minimal", "rust-cli", "ts-electron", "ts-web"} {
		contains(t, out, name+"\tbuiltin")
	}
	s.run(0, "preset", "export", "go-cli")
	s.run(1, "preset", "export", "go-cli")
	override, err := os.ReadFile(filepath.Join(s.presets, "go-cli.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	override = bytes.Replace(override, []byte("go run *"), []byte("go list *"), 1)
	if err := os.WriteFile(filepath.Join(s.presets, "go-cli.jsonc"), override, 0644); err != nil {
		t.Fatal(err)
	}
	out, _ = s.run(0, "preset", "list")
	contains(t, out, "go-cli\tcustom (overrides builtin)")
	exported := filepath.Join(tmp, "export")
	s.run(0, "preset", "export", "--out", exported, "go-cli", "--force")
	got, err := os.ReadFile(filepath.Join(exported, "go-cli.jsonc"))
	if err != nil || !bytes.Equal(got, override) {
		t.Fatalf("exported override mismatch: %v\n%s", err, got)
	}
	s.root = filepath.Join(tmp, "override-project")
	s.run(0, "init", s.root, "--preset", "go-cli", "--name", `Name with "quotes"`, "--no-hooks")
	var config model.ReifuConfig
	if err := jsonc.Unmarshal([]byte(s.read(master)), &config); err != nil {
		t.Fatal(err)
	}
	if config.Project.Name != `Name with "quotes"` {
		t.Fatalf("project name changed: %q", config.Project.Name)
	}
	contains(t, s.read(".claude/settings.json"), "Bash(go list *)")
	s.run(0, "status", "--quiet", s.root)
}

func TestInitOptions(t *testing.T) {
	tmp := t.TempDir()
	s := cliTest{t: t, root: filepath.Join(tmp, "nested", "project"), presets: filepath.Join(tmp, "presets")}
	s.run(0, "init", s.root, "--dry-run")
	missing(t, filepath.Dir(s.root))
	s.run(1, "init", s.root, "--agents", "unknown")
	s.run(1, "init", s.root, "--preset", "../go-cli")
	missing(t, s.root)
	s.run(0, "init", s.root, "--preset", "ts-web", "--agents", "claude", "--no-docs", "--no-settings", "--link-mode", "copy")
	for _, filename := range []string{"prompts", "docs", ".claude", ".codex", ".gemini", ".reifu.state.json", ".reifu.lock"} {
		missing(t, filepath.Join(s.root, filename))
	}
	contains(t, s.read("AGENTS.md"), "npm run build")
	var cfg model.ReifuConfig
	if err := jsonc.Unmarshal([]byte(s.read(".reifu.jsonc")), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Agents.Targets) != 1 || cfg.Agents.Targets[0] != model.AgentClaude {
		t.Fatalf("unexpected agents: %v", cfg.Agents.Targets)
	}
	s.run(0, "link", s.root, "--check")
	s.run(0, "sync", s.root, "--create", "--no-hooks")
	if strings.Contains(s.read(".claude/settings.json"), "\"hooks\"") {
		t.Fatal("verification hook was generated")
	}
	s.run(0, "status", "--quiet", s.root)
}

func TestAutoLinkCloneAndCLIGuards(t *testing.T) {
	tmp := t.TempDir()
	s := cliTest{t: t, root: filepath.Join(tmp, "auto-project"), presets: filepath.Join(tmp, "presets")}

	// Help outputs to stdout and exits 0.
	out, stderr := s.run(0, "--help")
	contains(t, out, "Usage: reifu")
	if stderr != "" {
		t.Fatalf("unexpected help stderr: %q", stderr)
	}
	out, stderr = s.run(0, "status", "--help")
	contains(t, out, "Usage: reifu status")
	if stderr != "" {
		t.Fatalf("unexpected subcommand help stderr: %q", stderr)
	}

	// Parse errors must never pollute stdout (critical for --json callers).
	out, stderr = s.run(1, "status", "--json", "--unknown-flag")
	if out != "" || stderr == "" {
		t.Fatalf("expected clean stdout on flag error, got stdout=%q stderr=%q", out, stderr)
	}

	// Preset export rejects path traversal.
	s.run(1, "preset", "export", "../../evil")

	// Default auto link mode: detached copy (git clone simulation) must fail link --check (Exit 3) and status --quiet (Exit 2).
	s.run(0, "init", s.root, "--preset", "go-cli", "--no-hooks")
	s.remove("CLAUDE.md")
	s.write("CLAUDE.md", s.read("AGENTS.md"))
	s.run(3, "link", "--check", s.root)
	s.run(2, "status", "--quiet", s.root)
	s.run(1, "link", s.root)
	s.run(0, "link", "--force", s.root)
	s.run(0, "link", "--check", s.root)
	s.run(0, "status", "--quiet", s.root)

	// Re-running init protects existing boilerplate unless --force is given.
	s.write("AGENTS.md", "custom agents\n")
	s.run(1, "init", s.root, "--preset", "go-cli", "--no-hooks")
	s.run(0, "init", s.root, "--preset", "go-cli", "--no-hooks", "--force")

	// sync --force overwrites external drift with the master config.
	s.write(".claude/settings.json", `{"permissions":{"allow":["Bash(untracked *)"]}}`)
	s.run(2, "sync", s.root, "--no-hooks")
	s.run(0, "sync", s.root, "--force", "--no-hooks")
	s.run(0, "status", "--quiet", s.root)
}

func TestInitImplicitDirGuard(t *testing.T) {
	tmp := t.TempDir()
	parent := filepath.Join(tmp, "projects")
	if err := os.MkdirAll(filepath.Join(parent, "other-repo"), 0755); err != nil {
		t.Fatal(err)
	}

	s := cliTest{t: t, root: parent, presets: filepath.Join(tmp, "presets"), workDir: parent}
	// Non-empty directory without .git or .reifu.jsonc is refused when [dir] is omitted.
	_, stderr := s.run(1, "init", "--name", "sample-app", "--preset", "go-cli", "--link-mode", "copy", "--no-hooks")
	contains(t, stderr, `reifu new sample-app`)
	missing(t, filepath.Join(parent, ".reifu.jsonc"))

	// reifu new requires a directory argument and refuses existing directories.
	s.run(1, "new", "--preset", "go-cli")
	s.run(0, "new", "sample-app", "--preset", "go-cli", "--link-mode", "copy", "--no-hooks")
	contains(t, s.read(filepath.Join("sample-app", "AGENTS.md")), "# AGENTS.md — sample-app")
	_, stderr = s.run(1, "new", "sample-app", "--preset", "go-cli", "--link-mode", "copy", "--no-hooks")
	contains(t, stderr, "already exists")

	// Home directory is unconditionally blocked even with --force.
	fakeHome := filepath.Join(tmp, "home")
	if err := os.MkdirAll(fakeHome, 0755); err != nil {
		t.Fatal(err)
	}
	homeTest := cliTest{t: t, root: fakeHome, presets: filepath.Join(tmp, "presets"), workDir: fakeHome, homeDir: fakeHome}
	for _, cmdArgs := range [][]string{
		{"init", "--force", "--link-mode", "copy", "--no-hooks"},
		{"init", fakeHome, "--force", "--link-mode", "copy", "--no-hooks"},
		{"new", fakeHome},
		{"sync", fakeHome},
		{"link", fakeHome, "--force"},
	} {
		_, stderr = homeTest.run(1, cmdArgs...)
		contains(t, stderr, "home directory")
	}
	missing(t, filepath.Join(fakeHome, ".reifu.jsonc"))

	// Empty directory allows implicit init without [dir].
	empty := filepath.Join(tmp, "empty-app")
	if err := os.MkdirAll(empty, 0755); err != nil {
		t.Fatal(err)
	}
	s.root, s.workDir = empty, empty
	s.run(0, "init", "--preset", "minimal", "--link-mode", "copy", "--no-hooks")
	contains(t, s.read("AGENTS.md"), "# AGENTS.md — empty-app")

	// Non-empty directory with .git allows implicit init without [dir].
	gitRepo := filepath.Join(tmp, "git-app")
	if err := os.MkdirAll(filepath.Join(gitRepo, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(gitRepo, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	s.root, s.workDir = gitRepo, gitRepo
	s.run(0, "init", "--preset", "minimal", "--link-mode", "copy", "--no-hooks")
	contains(t, s.read("AGENTS.md"), "# AGENTS.md — git-app")
}

func TestHookVerify(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "hook-app")
	presets := filepath.Join(tmp, "presets")
	s := cliTest{t: t, root: root, presets: presets}
	s.run(0, "new", root, "--preset", "go-cli", "--link-mode", "copy")

	// Missing go.mod -> skips cleanly with exit 0 and empty stdout.
	out, stderr := s.run(0, "hook", "verify", root)
	if out != "" || stderr != "" {
		t.Fatalf("expected silent skip without go.mod, got stdout=%q stderr=%q", out, stderr)
	}

	// Custom verify_steps using `go version` succeeds with empty stdout.
	s.write("go.mod", "module example.com/hookapp\n\ngo 1.23\n")
	masterPass := `{
  "version": 1,
  "project": {"name": "hook-app", "preset": "go-cli"},
  "workflow": {
    "require_files": ["go.mod"],
    "verify_steps": [["go", "version"]]
  }
}
`
	s.write(".reifu.jsonc", masterPass)
	out, stderr = s.run(0, "hook", "verify", root)
	if out != "" || !strings.Contains(stderr, "go version") {
		t.Fatalf("expected stdout empty and stderr containing go version, got stdout=%q stderr=%q", out, stderr)
	}

	// Failing step exits 2 with empty stdout.
	masterFail := strings.Replace(masterPass, `["go", "version"]`, `["go", "vet", "./does-not-exist"]`, 1)
	s.write(".reifu.jsonc", masterFail)
	out, stderr = s.run(2, "hook", "verify", root)
	if out != "" || stderr == "" {
		t.Fatalf("expected exit 2 with empty stdout, got stdout=%q stderr=%q", out, stderr)
	}

	// stop_hook_active: true skips failing step to prevent infinite loops.
	var stdoutBuf, stderrBuf bytes.Buffer
	cli := app.CLI{
		Stdin:   strings.NewReader(`{"stop_hook_active":true}`),
		Stdout:  &stdoutBuf,
		Stderr:  &stderrBuf,
		Presets: template.Presets{Dir: presets},
	}
	if code := cli.Run([]string{"hook", "verify", root}); code != model.ExitOK {
		t.Fatalf("expected exit 0 when stop_hook_active=true, got %d (%s)", code, stderrBuf.String())
	}

	// Open, idle stdin pipe must not hang hook verify.
	pr, pw := io.Pipe()
	defer pw.Close()
	cliIdle := app.CLI{
		Stdin:   pr,
		Stdout:  &stdoutBuf,
		Stderr:  &stderrBuf,
		Presets: template.Presets{Dir: presets},
	}
	if code := cliIdle.Run([]string{"hook", "verify", root}); code != model.ExitDrift {
		t.Fatalf("expected exit 2 on failing step with idle stdin pipe, got %d", code)
	}

	// BLOCKED_REPORT.md also skips failing step.
	s.write("BLOCKED_REPORT.md", "blocked\n")
	s.run(0, "hook", "verify", root)
	s.remove("BLOCKED_REPORT.md")

	// Node preset skips undefined npm scripts in package.json.
	tsRoot := filepath.Join(tmp, "ts-app")
	s.root = tsRoot
	s.run(0, "new", tsRoot, "--preset", "ts-web", "--link-mode", "copy")
	s.write("package.json", `{"name":"ts-app","scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`)
	out, _ = s.run(0, "hook", "verify", tsRoot)
	if out != "" {
		t.Fatalf("expected clean stdout on ts-web skip, got %q", out)
	}
}

func TestHelpAndSpecImport(t *testing.T) {
	tmp := t.TempDir()
	presets := filepath.Join(tmp, "presets")
	s := cliTest{t: t, root: tmp, presets: presets, workDir: tmp}

	// Global help lists all subcommands; `help <cmd>` prints subcommand options.
	out, stderr := s.run(0, "help")
	if stderr != "" {
		t.Fatalf("unexpected help stderr: %q", stderr)
	}
	for _, cmd := range []string{"new <dir>", "init [dir]", "link [dir]", "sync [dir]", "status [dir]", "preset", "hook", "version"} {
		contains(t, out, cmd)
	}
	out, stderr = s.run(0, "help", "new")
	if stderr != "" {
		t.Fatalf("unexpected subcommand help stderr: %q", stderr)
	}
	contains(t, out, "-spec")
	s.run(1, "help", "nonexistent")

	// Import existing spec file via --spec.
	specFile := filepath.Join(tmp, "my-spec.md")
	if err := os.WriteFile(specFile, []byte("# My Spec\r\nRequirements here.\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// --spec cannot be combined with --no-docs, and missing/dir spec paths are rejected.
	s.run(1, "new", "bad-combo", "--spec", specFile, "--no-docs", "--link-mode", "copy", "--no-hooks")
	s.run(1, "new", "bad-missing", "--spec", filepath.Join(tmp, "missing.md"), "--link-mode", "copy", "--no-hooks")
	s.run(1, "new", "bad-dir", "--spec", tmp, "--link-mode", "copy", "--no-hooks")

	s.root = filepath.Join(tmp, "spec-app")
	s.run(0, "new", "spec-app", "--preset", "go-cli", "--spec", "my-spec.md", "--link-mode", "copy", "--no-hooks")
	if got := s.read("docs/specs/000_source_spec.md"); got != "# My Spec\nRequirements here.\n" {
		t.Fatalf("unexpected imported spec content: %q", got)
	}
	contains(t, s.read("prompts/phase0.md"), "docs/specs/000_source_spec.md")
	contains(t, s.read("prompts/phase0.md"), "prompts/phases/")
	contains(t, s.read("prompts/README.md"), "prompts/phases/*.md")
	contains(t, s.read("prompts/add_feature.md"), "docs/specs/NNN_")
	contains(t, s.read("prompts/review.md"), "docs/design/")
	contains(t, s.read("docs/design/02_data_modeling.md"), "データベース")

	// Omitting --preset defaults to minimal (language-neutral).
	s.root = filepath.Join(tmp, "default-app")
	s.run(0, "new", "default-app", "--link-mode", "copy")
	var defaultCfg model.ReifuConfig
	if err := jsonc.Unmarshal([]byte(s.read(".reifu.jsonc")), &defaultCfg); err != nil {
		t.Fatal(err)
	}
	if defaultCfg.Project.Preset != "minimal" {
		t.Fatalf("expected default preset minimal, got %q", defaultCfg.Project.Preset)
	}
}

func TestVersionCommand(t *testing.T) {
	s := cliTest{t: t}
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		out, stderr := s.run(0, args...)
		if stderr != "" {
			t.Fatalf("unexpected stderr for %v: %q", args, stderr)
		}
		contains(t, out, "reifu ")
	}

	out, stderr := s.run(0, "version", "--json")
	if stderr != "" {
		t.Fatalf("unexpected json stderr: %q", stderr)
	}
	var info app.VersionInfo
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("invalid version json %q: %v", out, err)
	}
	if info.Version == "" || info.Commit == "" || info.GoVersion == "" || info.OS == "" || info.Arch == "" {
		t.Fatalf("incomplete version info: %+v", info)
	}

	out, stderr = s.run(0, "help", "version")
	if stderr != "" {
		t.Fatalf("unexpected help version stderr: %q", stderr)
	}
	contains(t, out, "-json")

	s.run(1, "version", "extra")
}
