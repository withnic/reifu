package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/withnic/reifu/internal/app"
	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/runner"
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

func TestPhaseAgentProcess(t *testing.T) {
	if os.Getenv("REIFU_CLI_TEST_AGENT") != "1" {
		return
	}
	if os.Getenv("REIFU_CLI_TEST_AGY_DENIED") == "1" {
		fmt.Fprintln(os.Stdout, `{"event":"init","conversation_id":"cli-1","init":{"cwd":"."}}`)
		fmt.Fprintln(os.Stdout, `{"event":"step_update","step_update":{"step_index":1,"state":"ERROR","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"go test ./..."},"error":{"type":"TOOL_ERROR","message":"permission check failed for unsandboxed \"go test ./...\": user denied permission to run command:\ngo test ./..."}}}}`)
		fmt.Fprintln(os.Stdout, `{"event":"result","result":{"conversation_id":"cli-1","status":"SUCCESS","response":"","num_turns":1,"denied_actions":[{"action":"command","display_name":"Bash"}]}}`)
		os.Exit(0)
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	role := os.Args[len(os.Args)-2]
	phase := "01_first"
	if strings.Contains(string(prompt), "02_second") {
		phase = "02_second"
	}
	if os.Getenv("REIFU_CLI_TEST_WAIT") == "1" {
		fmt.Fprintln(os.Stdout, "<agent-ready>")
		time.Sleep(20 * time.Second)
	}
	if err := os.WriteFile(role+"-"+phase+".txt", []byte("agent change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("mock %s %s\n", role, phase)
	if role == "review" && os.Getenv("REIFU_CLI_TEST_FAIL_REVIEW") == "1" {
		os.Exit(7)
	}
	if os.Getenv("REIFU_CLI_TEST_SELF_COMMIT") == "1" {
		cmd := exec.Command("git", "add", "--", role+"-"+phase+".txt")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git add: %s: %v", out, err)
		}
		cmd = exec.Command("git", "commit", "-m", role+" "+phase)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %s: %v", out, err)
		}
	}
	os.Exit(0)
}

func phaseCLI(t *testing.T) cliTest {
	t.Helper()
	t.Setenv("REIFU_CLI_TEST_AGENT", "1")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	s := cliTest{t: t, root: root, workDir: root}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(model.ReifuConfig{Version: 1, Project: model.ProjectMeta{Preset: "minimal"}, Runner: model.RunnerConfig{
		Implementer: "builder", Reviewer: "checker",
		Agents: map[string][]string{
			"builder": {exe, "-test.run=^TestPhaseAgentProcess$", "--", "implement", "agent"},
			"checker": {exe, "-test.run=^TestPhaseAgentProcess$", "--", "review", "agent"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s.write(".reifu.jsonc", string(cfg)+"\n")
	if err := os.MkdirAll(filepath.Join(root, "prompts", "phases"), 0755); err != nil {
		t.Fatal(err)
	}
	s.write("prompts/phases/02_second.md", "Implement 02_second\n")
	s.write("prompts/phases/01_first.md", "Implement 01_first\n")
	s.write("prompts/review.md", "Review this phase\n")
	s.git("init", "-q")
	s.git("config", "user.name", "Reifu Test")
	s.git("config", "user.email", "reifu@example.invalid")
	s.git("add", ".")
	s.git("commit", "-qm", "initial")
	return s
}

func (s cliTest) git(args ...string) string {
	s.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = s.root
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("git %q: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func parseCLIJSON(t *testing.T, out string, value any) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(out))
	if err := decoder.Decode(value); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("extra output after JSON in %q: %v", out, err)
	}
}

func parseRunEvents(t *testing.T, stdout string) []model.RunEvent {
	t.Helper()
	if !strings.HasSuffix(stdout, "\n") || strings.ContainsRune(stdout, '\r') {
		t.Fatalf("expected LF-terminated NDJSON, got %q", stdout)
	}
	var events []model.RunEvent
	for _, line := range strings.Split(strings.TrimSuffix(stdout, "\n"), "\n") {
		var event model.RunEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Type == "" || event.SchemaVersion != 1 || event.Timestamp.IsZero() {
			t.Fatalf("invalid NDJSON event %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func TestRunStreamJSONCLI(t *testing.T) {
	t.Run("exclusive", func(t *testing.T) {
		s := phaseCLI(t)
		stdout, stderr := s.run(model.ExitGeneral, "run", "--stream-json", "--json")
		if stdout != "" {
			t.Fatalf("unexpected stdout: %q", stdout)
		}
		contains(t, stderr, "mutually exclusive")
	})
	t.Run("plan", func(t *testing.T) {
		s := phaseCLI(t)
		stdout, stderr := s.run(model.ExitOK, "run", "--stream-json", "--dry-run")
		events := parseRunEvents(t, stdout)
		if len(events) != 1 || events[0].Type != model.EventRunPlan || events[0].Plan == nil || stderr != "" {
			t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
		}
		missing(t, filepath.Join(s.root, ".reifu"))
	})
	t.Run("immediate EOF without opt-in", func(t *testing.T) {
		s := phaseCLI(t)
		var stdout, stderr bytes.Buffer
		cli := app.CLI{WorkDir: s.root, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}
		if code := cli.Run([]string{"run", "--stream-json", "--no-review", "--no-commit"}); code != model.ExitOK || stderr.Len() != 0 {
			t.Fatalf("exit=%d stderr=%q", code, &stderr)
		}
		events := parseRunEvents(t, stdout.String())
		if events[0].Type != model.EventRunStarted || events[len(events)-1].Type != model.EventRunCompleted {
			t.Fatalf("unexpected event boundaries: %s", &stdout)
		}
		var logs strings.Builder
		for _, event := range events {
			if event.Type == model.EventLog {
				logs.WriteString(event.Text)
			}
		}
		contains(t, logs.String(), "mock implement 01_first\n")
		contains(t, logs.String(), "mock implement 02_second\n")
	})
	t.Run("preflight", func(t *testing.T) {
		s := phaseCLI(t)
		s.remove(".reifu.jsonc")
		stdout, _ := s.run(model.ExitGeneral, "run", "--stream-json")
		events := parseRunEvents(t, stdout)
		if len(events) != 1 || events[0].Type != model.EventRunFailed || events[0].Status != model.PhaseFailed || events[0].Error == "" {
			t.Fatalf("stdout=%q", stdout)
		}
		s.write("BLOCKED_REPORT.md", "blocked\n")
		stdout, _ = s.run(model.ExitDrift, "run", "--stream-json")
		events = parseRunEvents(t, stdout)
		if len(events) != 1 || events[0].Type != model.EventRunFailed || events[0].Status != model.PhaseBlocked {
			t.Fatalf("stdout=%q", stdout)
		}
	})
}

func TestRunCancelOnStdinEOFCLI(t *testing.T) {
	s := phaseCLI(t)
	t.Setenv("REIFU_CLI_TEST_WAIT", "1")
	stdin, input := io.Pipe()
	defer stdin.Close()
	defer input.Close()
	output, stdout := io.Pipe()
	defer output.Close()
	var stderr bytes.Buffer
	cli := app.CLI{WorkDir: s.root, Stdin: stdin, Stdout: stdout, Stderr: &stderr}
	done := make(chan model.ExitCode, 1)
	go func() {
		done <- cli.Run([]string{"run", "--stream-json", "--cancel-on-stdin-eof", "--no-review", "--no-commit", "--timeout=10s"})
		stdout.Close()
	}()
	var all strings.Builder
	var ready bool
	scanner := bufio.NewScanner(output)
	for scanner.Scan() {
		line := scanner.Text()
		all.WriteString(line + "\n")
		if strings.Contains(line, "<agent-ready>") {
			ready = true
			if err := input.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != model.ExitDrift || !ready {
		t.Fatalf("exit=%d ready=%v stderr=%q", code, ready, &stderr)
	}
	events := parseRunEvents(t, all.String())
	if len(events) < 2 || events[len(events)-2].Type != model.EventPhaseFailed {
		t.Fatalf("missing phase failure: %s", all.String())
	}
	var terminals int
	for _, event := range events {
		if event.Type == model.EventRunFailed || event.Type == model.EventRunCompleted {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("expected exactly one terminal event, got %d: %s", terminals, all.String())
	}
	last := events[len(events)-1]
	if last.Type != model.EventRunFailed || last.Status != model.PhaseFailed || last.State == nil {
		t.Fatalf("terminal event: %+v", last)
	}
	contains(t, last.Error, "context canceled")
	saved, err := runner.LoadRun(s.root, "")
	if err != nil || saved.RunID != last.RunID || saved.Phases[0].Status != model.PhaseFailed || saved.Phases[0].ResumeStep != "implement" {
		t.Fatalf("checkpoint=%+v err=%v", saved, err)
	}
	missing(t, filepath.Join(s.root, "implement-01_first.txt"))
}

func TestPhaseRunAndLogsCLI(t *testing.T) {
	s := phaseCLI(t)
	head := s.git("rev-parse", "HEAD")
	out, stderr := s.run(0, "run", "--dry-run", "--json")
	var plan runner.Plan
	parseCLIJSON(t, out, &plan)
	if len(plan.State.Phases) != 2 || plan.State.Phases[0].Name != "01_first" ||
		plan.Implementer.Name != "builder" || plan.Reviewer == nil || plan.Reviewer.Name != "checker" || stderr != "" {
		t.Fatalf("unexpected plan: %+v, stderr: %q", plan, stderr)
	}
	if gotHead, status := s.git("rev-parse", "HEAD"), s.git("status", "--porcelain"); gotHead != head || status != "" {
		t.Fatalf("dry run changed the Git worktree or HEAD: head %q (want %q), status %q", gotHead, head, status)
	}
	missing(t, filepath.Join(s.root, ".reifu"))
	missing(t, filepath.Join(s.root, ".reifu.run.json"))
	out, _ = s.run(0, "run", "--dry-run", "--json", "--from", "02_second", "--no-review", "--no-commit", "--timeout", "0")
	plan = runner.Plan{}
	parseCLIJSON(t, out, &plan)
	if len(plan.State.Phases) != 1 || plan.State.Phases[0].Name != "02_second" || plan.Reviewer != nil || !plan.State.NoCommit {
		t.Fatalf("unexpected selected plan: %+v", plan)
	}

	s.write("dirty.txt", "preexisting change\n")
	_, stderr = s.run(1, "run")
	contains(t, stderr, "uncommitted changes")
	missing(t, filepath.Join(s.root, ".reifu.run.json"))
	out, stderr = s.run(0, "run", "--allow-dirty", "--json", "--implementer", "builder", "--reviewer", "checker", s.root)
	var state model.RunState
	parseCLIJSON(t, out, &state)
	if state.Summary().Completed != 2 {
		t.Fatalf("unexpected state: %+v", state)
	}
	for _, phase := range state.Phases {
		if phase.ImplementCommit == "" || phase.ReviewCommit == "" {
			t.Fatalf("missing commits: %+v", phase)
		}
		contains(t, stderr, "mock implement "+phase.Name)
		contains(t, stderr, "mock review "+phase.Name)
	}
	contains(t, stderr, "warning: executing custom agent")
	contains(t, stderr, "[2/2]")
	want := "feat(phase): complete 01_first\nfix(review): address review for 01_first\nfeat(phase): complete 02_second\nfix(review): address review for 02_second"
	if got := s.git("log", "--reverse", "--format=%s", head+"..HEAD"); got != want {
		t.Fatalf("unexpected commits: %q", got)
	}
	if got := s.git("log", "--all", "--format=%H", "--", ".reifu", ".reifu.run.json"); got != "" {
		t.Fatalf("runtime files committed: %s", got)
	}
	out, _ = s.run(model.ExitOK, "status", "--json")
	var status model.StatusReport
	parseCLIJSON(t, out, &status)
	if status.Runner == nil || status.Runner.LatestRun == nil || *status.Runner.LatestRun != state.Summary() {
		t.Fatalf("status did not report latest run: %+v", status.Runner)
	}
	checkpoint := s.read(".reifu.run.json")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{}, "completed (2/2 completed)"},
		{[]string{"--list"}, "COMPLETED"},
		{[]string{"--review"}, "mock review 02_second"},
		{[]string{"--run", state.RunID, "--review", "--phase", "01_first"}, "mock review 01_first"},
		{[]string{"--phase", "01_first"}, "mock implement 01_first"},
		{[]string{"-f"}, "mock implement 02_second"},
		{[]string{"--follow", "--phase", "01_first"}, "mock implement 01_first"},
	} {
		out, stderr = s.run(0, append([]string{"logs"}, tc.args...)...)
		contains(t, out, tc.want)
		if stderr != "" {
			t.Fatalf("logs %v wrote stderr: %q", tc.args, stderr)
		}
	}
	var summaries []model.RunSummary
	out, _ = s.run(0, "logs", "--list", "--json")
	parseCLIJSON(t, out, &summaries)
	if len(summaries) != 1 || summaries[0] != state.Summary() {
		t.Fatalf("unexpected history: %+v", summaries)
	}
	out, _ = s.run(0, "logs", "--json")
	var saved model.RunState
	parseCLIJSON(t, out, &saved)
	if saved.RunID != state.RunID || saved.Summary() != state.Summary() {
		t.Fatalf("unexpected logs state: %+v", saved)
	}
	if s.read(".reifu.run.json") != checkpoint {
		t.Fatal("logs changed checkpoint")
	}
	missing(t, filepath.Join(s.root, ".reifu", "run.lock"))

	// Non-JSON run prints a final summary to stdout; logs --review explains --no-review runs.
	out, stderr = s.run(0, "run", "--phase", "01_first", "--no-review", "--no-commit")
	contains(t, out, "completed (1/1 completed)")
	contains(t, out, "01_first")
	contains(t, stderr, "mock implement 01_first")
	out, stderr = s.run(0, "logs", "--review")
	contains(t, out, "No review reports (run was executed with --no-review).")
	if stderr != "" {
		t.Fatalf("logs --review wrote stderr: %q", stderr)
	}
}

func TestPhaseResumeCLI(t *testing.T) {
	s := phaseCLI(t)
	t.Setenv("REIFU_CLI_TEST_FAIL_REVIEW", "1")
	out, _ := s.run(2, "run", "--json")
	var failed model.RunState
	parseCLIJSON(t, out, &failed)
	if failed.Phases[0].Status != model.PhaseFailed || failed.Phases[0].ResumeStep != "review" ||
		failed.Phases[1].Status != model.PhasePending {
		t.Fatalf("unexpected stopped state: %+v", failed)
	}
	for _, flag := range []string{"--no-review", "--no-commit"} {
		_, stderr := s.run(1, "run", "--resume", flag)
		contains(t, stderr, "resume cannot be combined")
	}
	_, stderr := s.run(1, "run", "--resume")
	contains(t, stderr, "--resume --allow-dirty")
	t.Setenv("REIFU_CLI_TEST_FAIL_REVIEW", "0")
	t.Setenv("REIFU_CLI_TEST_SELF_COMMIT", "1")
	out, _ = s.run(0, "run", "--resume", "--allow-dirty", "--json")
	var resumed model.RunState
	parseCLIJSON(t, out, &resumed)
	if resumed.RunID != failed.RunID || resumed.Summary().Completed != 2 ||
		resumed.Phases[0].ImplementCommit != failed.Phases[0].ImplementCommit ||
		resumed.Phases[1].BaseCommit == "" || resumed.Phases[1].ReviewBase == "" ||
		resumed.Phases[1].ImplementCommit == "" || resumed.Phases[1].ReviewCommit == "" {
		t.Fatalf("unexpected resumed state: %+v", resumed)
	}
	contains(t, resumed.Phases[1].ImplementDiff, "implement-02_second.txt")
	contains(t, s.read(resumed.Phases[1].ReviewReport), "review-02_second.txt")
	if got := s.git("rev-list", "--count", "HEAD"); got != "5" {
		t.Fatalf("unexpected commit count: %s", got)
	}
}

func TestPhaseCLIGuards(t *testing.T) {
	s := phaseCLI(t)
	s.write(".reifu.jsonc", "{\"version\":1,\"project\":{\"preset\":\"minimal\"}}\n")
	_, stderr := s.run(1, "run", "--dry-run")
	contains(t, stderr, "missing implementer")
	out, _ := s.run(0, "run", "--dry-run", "--phase", "01_first.md", "--implementer", "agy")
	contains(t, out, "pending [1/1: 01_first] (0/1 completed)")
	contains(t, out, "01_first")
	if strings.Contains(out, "02_second") {
		t.Fatalf("dry run selected an extra phase: %s", out)
	}
	for _, args := range [][]string{
		{"--phase", "01_first", "--from", "02_second"},
		{"--resume", "--phase", "01_first"},
		{"--resume", "--from", "01_first"},
	} {
		_, stderr := s.run(1, append([]string{"run", "--dry-run", "--implementer", "agy"}, args...)...)
		contains(t, stderr, "mutually exclusive")
	}
	_, stderr = s.run(1, "run", "--dry-run", "--implementer", "agy", "--phase", "03_missing")
	contains(t, stderr, "unknown phase")
	s.write("BLOCKED_REPORT.md", "blocked\n")
	out, stderr = s.run(2, "run", "--json", "--implementer", "agy")
	contains(t, stderr, "BLOCKED_REPORT.md")
	if out != "" {
		t.Fatalf("blocked run wrote stdout: %q", out)
	}
	missing(t, filepath.Join(s.root, ".reifu"))
	if err := os.Remove(filepath.Join(s.root, "BLOCKED_REPORT.md")); err != nil {
		t.Fatal(err)
	}
	_, stderr = s.run(1, "logs", "--run", "../escape")
	contains(t, stderr, "invalid run ID")
	for _, alias := range []string{"-f", "--follow"} {
		_, stderr := s.run(1, "logs", alias, "--json")
		contains(t, stderr, "--follow cannot be combined with --json")
		_, stderr = s.run(1, "logs", alias, "--review")
		contains(t, stderr, "--follow cannot be combined with --review or --list")
	}
	for _, args := range [][]string{{"--phase", "01_first"}, {"--phase", "99_missing"}, {"--review"}} {
		out, stderr := s.run(1, append([]string{"logs", "--json"}, args...)...)
		contains(t, stderr, "--json cannot be combined with --phase or --review")
		if out != "" {
			t.Fatalf("logs --json %v wrote stdout: %q", args, out)
		}
	}
	for _, args := range [][]string{{"--run", "20261128T120000Z"}, {"--phase", "01_first"}, {"--review"}} {
		out, stderr := s.run(1, append([]string{"logs", "--list"}, args...)...)
		contains(t, stderr, "--list cannot be combined")
		if out != "" {
			t.Fatalf("logs --list %v wrote stdout: %q", args, out)
		}
	}
	_, stderr = s.run(1, "logs")
	contains(t, stderr, "no run history")
	contains(t, stderr, "start a run with reifu run")
	out, _ = s.run(0, "logs", "--list", "--json")
	if out != "[]\n" {
		t.Fatalf("unexpected empty history: %q", out)
	}
	s.homeDir = s.root
	for _, name := range []string{"run", "logs"} {
		_, stderr := s.run(1, name)
		contains(t, stderr, "user home directory")
		out, _ := s.run(0, "help", name)
		contains(t, out, "Usage: reifu "+name+" [options] [dir]")
	}
}

func TestCompanionSetupJSON(t *testing.T) {
	for _, command := range []string{"new", "init"} {
		t.Run(command, func(t *testing.T) {
			tmp := t.TempDir()
			root := filepath.Join(tmp, "project")
			s := cliTest{t: t, root: root, presets: filepath.Join(tmp, "presets")}
			if command == "init" {
				if err := os.Mkdir(root, 0755); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{command, root, "--preset", "go-cli", "--link-mode", "hard", "--json"}
			var reports [2]model.SetupReport
			for i, dryRun := range []bool{true, false} {
				call := slices.Clone(args)
				if dryRun {
					call = append(call, "--dry-run")
				}
				out, stderr := s.run(model.ExitOK, call...)
				if stderr != "" {
					t.Fatalf("unexpected setup stderr: %q", stderr)
				}
				report := &reports[i]
				parseCLIJSON(t, out, report)
				if report.Command != command || report.Root != root || report.Preset != "go-cli" || report.DryRun != dryRun ||
					!slices.Contains(report.Files, "AGENTS.md") || !slices.Contains(report.ChangedPaths, ".reifu.state.json") ||
					len(report.Warnings) == 0 || len(report.Links) != 2 {
					t.Fatalf("unexpected setup report: %+v", report)
				}
				for _, link := range report.Links {
					if dryRun && link.Status != model.LinkStatusMissing || !dryRun && (link.Status != model.LinkStatusOK || link.Mode != model.LinkModeHard) {
						t.Fatalf("unexpected setup link: %+v", link)
					}
				}
				if dryRun {
					missing(t, filepath.Join(root, ".reifu.jsonc"))
					missing(t, filepath.Join(root, ".reifu.lock"))
					if command == "new" {
						missing(t, root)
					}
				}
			}
			if !slices.Equal(reports[0].Files, reports[1].Files) || !slices.Equal(reports[0].ChangedPaths, reports[1].ChangedPaths) {
				t.Fatalf("dry-run plan differs from applied setup: %+v / %+v", reports[0], reports[1])
			}
		})
	}
}

func TestCompanionCommandsJSON(t *testing.T) {
	t.Setenv("PATH", "")
	tmp := t.TempDir()
	root := filepath.Join(tmp, "project")
	s := cliTest{t: t, root: root, presets: filepath.Join(tmp, "presets")}

	out, _ := s.run(model.ExitOK, "preset", "list", "--json")
	var presets []template.Preset
	parseCLIJSON(t, out, &presets)
	if !slices.Contains(presets, template.Preset{Name: "go-cli", Source: "builtin"}) {
		t.Fatalf("missing builtin preset: %+v", presets)
	}

	out, _ = s.run(model.ExitOK, "status", "--json", tmp)
	var uninitialized model.StatusReport
	parseCLIJSON(t, out, &uninitialized)
	if !uninitialized.StateMissing || uninitialized.Runner == nil {
		t.Fatalf("unexpected uninitialized status: %+v", uninitialized)
	}

	s.run(model.ExitOK, "new", root, "--preset", "go-cli", "--link-mode", "hard", "--no-settings")
	skillDir := filepath.Join(root, ".agents", "skills", "example")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	s.write(".agents/skills/example/SKILL.md", "# Example\n")
	for _, dryRun := range []bool{true, false} {
		args := []string{"sync", root, "--create", "--json"}
		if dryRun {
			args = append(args, "--dry-run")
		}
		out, stderr := s.run(model.ExitOK, args...)
		var report model.SyncReport
		parseCLIJSON(t, out, &report)
		if stderr != "" || report.Root != root || report.DryRun != dryRun || !report.StateMissing ||
			!slices.Contains(report.ChangedPaths, ".reifu.state.json") || len(report.Warnings) == 0 ||
			len(report.Targets) != 3 || len(report.Skills) != 1 {
			t.Fatalf("unexpected sync report: %+v, stderr %q", report, stderr)
		}
		if dryRun {
			missing(t, filepath.Join(root, ".reifu.state.json"))
			missing(t, filepath.Join(root, ".claude", "skills", "example"))
		}
	}
	out, _ = s.run(model.ExitOK, "sync", root, "--json")
	var unchanged model.SyncReport
	parseCLIJSON(t, out, &unchanged)
	if unchanged.ChangedPaths == nil || len(unchanged.ChangedPaths) != 0 || unchanged.Warnings == nil {
		t.Fatalf("unexpected no-op sync: %+v", unchanged)
	}

	s.remove("CLAUDE.md")
	out, _ = s.run(model.ExitLinkIntegrity, "link", root, "--check", "--json")
	var damaged model.LinkCommandReport
	parseCLIJSON(t, out, &damaged)
	if !damaged.HasIssues || !damaged.Check || len(damaged.Links) != 2 || damaged.Links[0].Status != model.LinkStatusMissing {
		t.Fatalf("unexpected damaged link report: %+v", damaged)
	}
	for _, check := range []bool{false, true} {
		args := []string{"link", root, "--json"}
		if check {
			args = append(args, "--check")
		}
		out, _ := s.run(model.ExitOK, args...)
		var report model.LinkCommandReport
		parseCLIJSON(t, out, &report)
		if report.Root != root || report.Check != check || report.HasIssues || report.Mode != model.LinkModeHard ||
			len(report.Links) != 2 || report.Links[0].Status != model.LinkStatusOK ||
			len(report.Skills) != 1 || report.Skills[0].Status != model.LinkStatusOK {
			t.Fatalf("unexpected link report: %+v", report)
		}
	}

	s.write("BLOCKED_REPORT.md", "test runner block\n")
	out, _ = s.run(model.ExitOK, "status", root, "--json")
	var status model.StatusReport
	parseCLIJSON(t, out, &status)
	runnerStatus := status.Runner
	if status.HasIssues || status.StateMissing || status.Env == "" || status.Root != root || len(status.Links) != 2 ||
		len(status.Targets) != 3 || len(status.Skills) != 1 || runnerStatus == nil || !runnerStatus.Blocked ||
		runnerStatus.GitRepo || runnerStatus.GitDirty || runnerStatus.Phases == nil || len(runnerStatus.Phases) != 0 ||
		len(runnerStatus.Agents) != 4 || runnerStatus.LatestRun != nil {
		t.Fatalf("unexpected status: %+v, runner %+v", status, runnerStatus)
	}
	for _, agent := range runnerStatus.Agents {
		if agent.Available || agent.Executable != "" {
			t.Fatalf("unexpected agent with empty PATH: %+v", agent)
		}
	}
	quietOut, quietErr := s.run(model.ExitOK, "status", root, "--quiet")
	if quietOut != "" || quietErr != "" {
		t.Fatalf("quiet status wrote output: %q / %q", quietOut, quietErr)
	}
	s.write(".claude/settings.json", "{}\n")
	s.run(model.ExitDrift, "status", root, "--quiet")
}

func TestProjectWorkflow(t *testing.T) {
	tmp := t.TempDir()
	s := cliTest{t: t, root: filepath.Join(tmp, "project"), presets: filepath.Join(tmp, "presets")}
	_, warnings := s.run(0, "init", s.root, "--name", "phase-3", "--preset", "go-cli", "--link-mode", "hard")
	contains(t, warnings, "applying verification hook")
	contains(t, s.read("AGENTS.md"), "go test ./...")
	contains(t, s.read(".gitattributes"), "* text=auto eol=lf")
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
	s.run(0, "init", s.root, "--preset", "go-cli", "--strict", "--name", `Name with "quotes"`, "--no-hooks")
	var config model.ReifuConfig
	if err := jsonc.Unmarshal([]byte(s.read(master)), &config); err != nil {
		t.Fatal(err)
	}
	if config.Project.Name != `Name with "quotes"` {
		t.Fatalf("project name changed: %q", config.Project.Name)
	}
	contains(t, s.read(".claude/settings.json"), "Bash(go list *)")
	if strings.Contains(s.read(".claude/settings.json"), "Bash(*)") {
		t.Fatalf("--strict should not allow Bash(*): %s", s.read(".claude/settings.json"))
	}
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
	contains(t, s.read(".gitattributes"), "* text=auto eol=lf")

	// Existing .gitattributes is preserved on init without --force, and replaced with --force.
	preserveRoot := filepath.Join(tmp, "preserve-attrs")
	if err := os.MkdirAll(filepath.Join(preserveRoot, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	p := cliTest{t: t, root: preserveRoot, presets: filepath.Join(tmp, "presets")}
	p.write(".gitattributes", "* text=auto\n")
	p.run(0, "init", preserveRoot, "--preset", "minimal", "--no-docs", "--no-settings", "--link-mode", "copy")
	if got := p.read(".gitattributes"); got != "* text=auto\n" {
		t.Fatalf("existing .gitattributes was overwritten: %q", got)
	}
	p.run(0, "init", preserveRoot, "--preset", "minimal", "--no-docs", "--no-settings", "--link-mode", "copy", "--force")
	contains(t, p.read(".gitattributes"), "* text=auto eol=lf")

	// Default uses allow_commands: ["*"] + strengthened deny_commands; --strict uses curated allowlist.
	defaultRoot := filepath.Join(tmp, "default-perms")
	d := cliTest{t: t, root: defaultRoot, presets: filepath.Join(tmp, "presets")}
	d.run(0, "new", defaultRoot, "--preset", "go-cli", "--link-mode", "copy", "--no-hooks")
	contains(t, d.read(".claude/settings.json"), "Bash(*)")
	contains(t, d.read(".claude/settings.json"), "Bash(Remove-Item*-Recurse*)")
	contains(t, d.read(".gemini/settings.json"), "run_shell_command(*)")
	contains(t, d.read(".gemini/settings.json"), "run_shell_command(rd /s*)")
	contains(t, d.read(".codex/rules/reifu.rules"), `pattern = ["git", "push"]`)
	contains(t, d.read(".codex/rules/reifu.rules"), `decision = "forbidden"`)

	strictRoot := filepath.Join(tmp, "strict-perms")
	st := cliTest{t: t, root: strictRoot, presets: filepath.Join(tmp, "presets")}
	st.run(0, "new", strictRoot, "--preset", "go-cli", "--strict", "--link-mode", "copy", "--no-hooks")
	contains(t, st.read(".claude/settings.json"), "Bash(go test *)")
	contains(t, st.read(".claude/settings.json"), "Bash(Remove-Item*-Recurse*)")
	if strings.Contains(st.read(".claude/settings.json"), "Bash(*)") || strings.Contains(st.read(".gemini/settings.json"), "run_shell_command(*)") {
		t.Fatalf("--strict leaked wildcard allow rule:\nclaude=%s\ngemini=%s", st.read(".claude/settings.json"), st.read(".gemini/settings.json"))
	}
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

func TestSkillsCLI(t *testing.T) {
	root := t.TempDir()
	s := cliTest{t: t, root: root, workDir: root, homeDir: t.TempDir(), presets: t.TempDir()}
	s.run(model.ExitOK, "init", root)
	missing(t, filepath.Join(root, ".agents/skills"))
	missing(t, filepath.Join(root, ".claude/skills"))
	out, _ := s.run(model.ExitOK, "status", "--json")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["skills"]; exists {
		t.Fatalf("unexpected skills field: %s", out)
	}

	const source = ".agents/skills/deploy-check"
	const target = ".claude/skills/deploy-check"
	const manual = ".claude/skills/local-only/SKILL.md"
	put := func(name, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0755); err != nil {
			t.Fatal(err)
		}
		s.write(name, data)
	}
	put(source+"/SKILL.md", "# Deploy check\n")
	put(source+"/scripts/check.sh", "#!/bin/sh\n")
	put(manual, "# Claude only\n")
	baseline := s.read(".reifu.state.json")
	out, _ = s.run(model.ExitOK, "sync", "--dry-run")
	contains(t, out, "Would update "+target)
	missing(t, filepath.Join(root, target))
	if got := s.read(".reifu.state.json"); got != baseline {
		t.Fatalf("dry run changed state: %s", got)
	}
	out, _ = s.run(model.ExitOK, "sync")
	contains(t, out, "Updated "+target)
	if got := s.read(target + "/SKILL.md"); got != "# Deploy check\n" {
		t.Fatal(got)
	}
	if got := s.read(target + "/scripts/check.sh"); got != "#!/bin/sh\n" {
		t.Fatal(got)
	}
	out, _ = s.run(model.ExitOK, "status", "--json")
	var report model.StatusReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Skills) != 1 || report.Skills[0].Target != target || report.Skills[0].Status != model.LinkStatusOK ||
		(report.Skills[0].Mode != model.LinkModeSymlink && report.Skills[0].Mode != model.LinkModeJunction) {
		t.Fatalf("unexpected skill reports: %+v", report.Skills)
	}
	out, _ = s.run(model.ExitOK, "status")
	contains(t, out, target+"  OK (")
	s.run(model.ExitOK, "status", "--quiet")
	s.run(model.ExitOK, "link", "--check")

	s.remove(target)
	put(target+"/SKILL.md", "# Deploy check\n")
	put(target+"/scripts/check.sh", "#!/bin/sh\n")
	out, _ = s.run(model.ExitLinkIntegrity, "link", "--check")
	contains(t, out, target+"  COPY (copy)")
	s.run(model.ExitDrift, "status", "--quiet")
	s.run(model.ExitGeneral, "link")
	if got := s.read(target + "/scripts/check.sh"); got != "#!/bin/sh\n" {
		t.Fatal(got)
	}
	s.run(model.ExitOK, "link", "--force")
	s.run(model.ExitOK, "link", "--check")
	s.run(model.ExitOK, "status", "--quiet")

	s.remove(source + "/scripts/check.sh")
	s.remove(source + "/scripts")
	s.remove(source + "/SKILL.md")
	s.remove(source)
	out, _ = s.run(model.ExitOK, "sync")
	contains(t, out, "Updated "+target)
	missing(t, filepath.Join(root, target))
	if got := s.read(manual); got != "# Claude only\n" {
		t.Fatal(got)
	}
}

func TestInitAndLinkSkills(t *testing.T) {
	root := t.TempDir()
	s := cliTest{t: t, root: root, workDir: root, homeDir: t.TempDir(), presets: t.TempDir()}
	const source = ".agents/skills/example"
	const target = ".claude/skills/example"
	if err := os.MkdirAll(filepath.Join(root, source), 0755); err != nil {
		t.Fatal(err)
	}
	s.write(source+"/SKILL.md", "# Example\n")
	s.run(model.ExitOK, "init", root, "--dry-run")
	missing(t, filepath.Join(root, target))
	s.run(model.ExitOK, "init", root)
	if got := s.read(target + "/SKILL.md"); got != "# Example\n" {
		t.Fatal(got)
	}

	s.remove(target)
	s.remove(".claude/skills")
	s.remove(".claude/settings.json")
	s.remove(".claude")
	s.run(model.ExitOK, "link")
	if got := s.read(target + "/SKILL.md"); got != "# Example\n" {
		t.Fatal(got)
	}
	s.remove(source + "/SKILL.md")
	s.remove(source)
	s.run(model.ExitOK, "link")
	missing(t, filepath.Join(root, target))
}

func TestSkillsExternalDestination(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "project")
	s := cliTest{t: t, root: root, workDir: outer, homeDir: t.TempDir(), presets: t.TempDir()}
	const source = ".agents/skills/example"
	const target = ".claude/skills/example"
	for _, dir := range []string{filepath.Join(root, source), filepath.Join(outer, "outside")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	s.write(source+"/SKILL.md", "# Example\n")
	s.write("../outside/SKILL.md", "# Outside\n")
	s.run(model.ExitOK, "init", root)
	s.remove(target)
	if mode, err := fsx.EnsureDirLink(outer, "outside", "project/"+target, model.LinkModeAuto, false); err != nil || mode == model.LinkModeCopy {
		t.Fatalf("external link fixture: %s, %v", mode, err)
	}
	out, _ := s.run(model.ExitLinkIntegrity, "link", root, "--check")
	contains(t, out, target+"  DIFFERENT")
	out, _ = s.run(model.ExitDrift, "status", root, "--quiet", "--json")
	contains(t, out, `"status":"DIFFERENT"`)
	s.run(model.ExitOK, "link", root, "--force")
	s.run(model.ExitOK, "link", root, "--check")
	if got := s.read("../outside/SKILL.md"); got != "# Outside\n" {
		t.Fatal(got)
	}
}

func TestRunNotifyCLI(t *testing.T) {
	t.Setenv("REIFU_NO_NOTIFY", "1")
	s := phaseCLI(t)
	for _, tc := range []struct {
		flag string
		mode model.NotifyMode
	}{
		{"--notify", model.NotifyEnd},
		{"--notify=end", model.NotifyEnd},
		{"--notify=true", model.NotifyEnd},
		{"--notify=off", model.NotifyOff},
		{"--notify=false", model.NotifyOff},
	} {
		out, stderr := s.run(0, "run", tc.flag, "--dry-run", "--json")
		var plan runner.Plan
		parseCLIJSON(t, out, &plan)
		if plan.State.Notify != tc.mode || stderr != "" {
			t.Fatalf("%s: notify = %q, want %q, stderr = %q", tc.flag, plan.State.Notify, tc.mode, stderr)
		}
	}
	out, _ := s.run(0, "run", "--notify=phase", "--phase", "01_first", "--no-review", "--no-commit", "--json")
	var state model.RunState
	parseCLIJSON(t, out, &state)
	if state.Notify != model.NotifyPhase || state.Summary().Status != model.PhaseCompleted {
		t.Fatalf("unexpected state: %+v", state)
	}
	_, stderr := s.run(1, "run", "--notify=invalid")
	contains(t, stderr, "invalid notification mode")
}

func TestAgyDeniedCommandsCLI(t *testing.T) {
	s := phaseCLI(t)
	t.Setenv("REIFU_CLI_TEST_AGY_DENIED", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(model.ReifuConfig{
		Version: 1,
		Project: model.ProjectMeta{Preset: "minimal"},
		Runner: model.RunnerConfig{
			Implementer: "agy",
			Agents: map[string][]string{
				"agy": {exe, "-test.run=^TestPhaseAgentProcess$", "--", "implement", "--output-format", "stream-json", "--print={prompt}"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.write(".reifu.jsonc", string(cfg)+"\n")
	s.git("add", ".reifu.jsonc")
	s.git("commit", "-qm", "configure agy mock")

	stdout, stderr := s.run(model.ExitDrift, "run", "--phase", "01_first", "--no-review", "--stream-json")
	contains(t, stderr, "go test ./...")
	contains(t, stderr, "permissions.allow_commands")
	events := parseRunEvents(t, stdout)
	last := events[len(events)-1]
	if last.Type != model.EventRunFailed || !slices.Equal(last.DeniedCommands, []string{"go test ./..."}) {
		t.Fatalf("run_failed event = %+v", last)
	}

	logsOut, _ := s.run(model.ExitOK, "logs")
	contains(t, logsOut, "Denied commands")
	contains(t, logsOut, `"go test ./..."`)

	logsJSON, _ := s.run(model.ExitOK, "logs", "--json")
	var state model.RunState
	parseCLIJSON(t, logsJSON, &state)
	if len(state.Phases) != 1 || state.Phases[0].CommandsLog == "" || !slices.Equal(state.Phases[0].DeniedCommands, []string{"go test ./..."}) {
		t.Fatalf("logs --json state = %+v", state)
	}
}
