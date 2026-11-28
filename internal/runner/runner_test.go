package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/sysenv"
)

func TestHelperAgentProcess(t *testing.T) {
	if os.Getenv("REIFU_HELPER_AGENT") != "1" {
		return
	}
	if os.Getenv("REIFU_HELPER_MODE") == "grandchild" {
		put(t, ".", "grandchild-started.txt", "started\n")
		time.Sleep(1500 * time.Millisecond)
		put(t, ".", "orphan.txt", "survived the timeout\n")
		os.Exit(0)
	}
	if os.Getenv("REIFU_HELPER_MODE") == "tree" {
		child := exec.Command(os.Args[0], "-test.run=^TestHelperAgentProcess$", "--", "grandchild")
		child.Env = append(os.Environ(), "REIFU_HELPER_MODE=grandchild")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	role := os.Args[len(os.Args)-1]
	text := string(input)
	phase := strings.TrimSpace(text)
	if role == "review" {
		phase = strings.TrimPrefix(strings.SplitN(text, "\n", 2)[0], "# Phase ")
		if !strings.Contains(text, "Implementation commit:") || !strings.Contains(text, "review instructions") {
			t.Fatal("missing review prompt context")
		}
	}
	if os.Getenv("REIFU_HELPER_MODE") == "timeout" {
		time.Sleep(10 * time.Second)
	}
	if os.Getenv("REIFU_HELPER_MODE") == "block" {
		put(t, ".", blockedReport, "blocked\n")
		os.Exit(0)
	}
	fmt.Fprintf(os.Stdout, "%s %s\r\n", role, phase)
	fmt.Fprint(os.Stderr, "stderr output\r\n")
	if os.Getenv("REIFU_HELPER_MODE") == "heartbeat" {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintln(os.Stdout, "after heartbeat")
	}
	if os.Getenv("REIFU_HELPER_MODE") == "follow" {
		limit := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat("proceed"); err == nil {
				break
			}
			if time.Now().After(limit) {
				t.Fatal("follower did not observe agent output")
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprintln(os.Stdout, "final output")
	}
	if role == "review" && os.Getenv("REIFU_HELPER_MODE") == "fail-review" {
		os.Exit(3)
	}
	put(t, ".", phase+"-"+role+".txt", role+" "+phase+"\n")
	if os.Getenv("REIFU_HELPER_MODE") == "self-commit" {
		gitOK(t, ".", "add", phase+"-"+role+".txt")
		gitOK(t, ".", "commit", "-m", "agent "+role+" "+phase)
	}
	os.Exit(0)
}

func put(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, withGit bool) (Options, *bytes.Buffer) {
	t.Helper()
	t.Setenv("REIFU_HELPER_AGENT", "1")
	t.Setenv("REIFU_HELPER_MODE", "")
	root := t.TempDir()
	put(t, root, "prompts/phases/02_beta.md", "02_beta\n")
	put(t, root, "prompts/phases/01_alpha.md", "01_alpha\n")
	put(t, root, reviewPrompt, "review instructions\n")
	if withGit {
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "absent-git-config"))
		gitOK(t, root, "init")
		gitOK(t, root, "config", "user.name", "Runner Test")
		gitOK(t, root, "config", "user.email", "runner@example.invalid")
		gitOK(t, root, "config", "core.autocrlf", "false")
		put(t, root, ".gitignore", "")
		gitOK(t, root, "add", ".")
		gitOK(t, root, "commit", "-m", "initial")
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	return Options{Root: root, Timeout: 5 * time.Second, Progress: &progress, NoCommit: !withGit,
		Config: model.ReifuConfig{Runner: model.RunnerConfig{
			Implementer: "builder", Reviewer: "critic", Agents: map[string][]string{
				"builder": {exe, "-test.run=^TestHelperAgentProcess$", "--", "implement"},
				"critic":  {exe, "-test.run=^TestHelperAgentProcess$", "--", "review"},
			},
		}},
	}, &progress
}

func gitOK(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), root, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func requireContains(t *testing.T, body string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(body, part) {
			t.Errorf("%q does not contain %q", body, part)
		}
	}
}

func TestResolveAgent(t *testing.T) {
	cfg := model.ReifuConfig{}
	for name, want := range map[string][]string{
		"claude": {"claude", "-p"}, "codex": {"codex", "exec", "-s", "workspace-write", "-"},
		"gemini": {"gemini", "-p", ""}, "agy": {"agy", "--print", "--mode", "auto"},
		"antigravity": {"agy", "--print", "--mode", "auto"},
	} {
		a, err := ResolveAgent(cfg, name)
		if err != nil || a.IsCustom || !reflect.DeepEqual(a.Argv, want) || a.Name != want[0] {
			t.Errorf("%s = %+v (%v)", name, a, err)
		}
	}
	cfg.Runner.Agents = map[string][]string{"antigravity": {"alias"}, "agy": {"override"}, "claude": {"custom", "-p"}}
	for _, tc := range []struct{ name, exe string }{{"antigravity", "alias"}, {"agy", "override"}, {"claude", "custom"}} {
		a, err := ResolveAgent(cfg, tc.name)
		if err != nil || !a.IsCustom || a.Argv[0] != tc.exe {
			t.Errorf("%s = %+v (%v)", tc.name, a, err)
		}
	}
	root := t.TempDir()
	put(t, root, "bin/agent.exe", "")
	if err := os.Chmod(filepath.Join(root, "bin", "agent.exe"), 0755); err != nil {
		t.Fatal(err)
	}
	if got, err := lookPath(root, AgentCommand{Name: "local", Argv: []string{"bin/agent.exe"}}); err != nil || got != filepath.Join(root, "bin", "agent.exe") {
		t.Errorf("relative agent path resolved to %q (%v), want it under the project root", got, err)
	}
	delete(cfg.Runner.Agents, "antigravity")
	a, err := ResolveAgent(cfg, "antigravity")
	if err != nil || !a.IsCustom || a.Argv[0] != "override" || a.Name != "agy" {
		t.Fatalf("alias fallback: %+v (%v)", a, err)
	}
}

func TestRunFullLoopAndHistory(t *testing.T) {
	opts, progress := fixture(t, true)
	put(t, opts.Root, ".reifu.state.json", "{}\n")
	put(t, opts.Root, ".REIFU.TMP.spare", "temporary\n")
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, progress)
	}
	state := *result.State
	if summary := state.Summary(); summary.Status != model.PhaseCompleted || summary.Completed != 2 {
		t.Fatalf("summary: %+v", summary)
	}
	requireContains(t, progress.String(), "warning: executing custom agent", "starting 01_alpha", "completed 02_beta", "stderr output")
	if got := gitOK(t, opts.Root, "log", "-5", "--format=%s"); strings.ReplaceAll(got, "\r", "") != "fix(review): address review for 02_beta\nfeat(phase): complete 02_beta\nfix(review): address review for 01_alpha\nfeat(phase): complete 01_alpha\ninitial" {
		t.Fatalf("commit order: %s", got)
	}
	tracked := gitOK(t, opts.Root, "ls-files")
	if strings.Contains(strings.ToLower(tracked), ".reifu") || strings.Contains(tracked, blockedReport) {
		t.Fatalf("runtime files committed: %s", tracked)
	}
	for _, phase := range state.Phases {
		log, err := ReadPhaseLog(opts.Root, state, phase.Name+".md")
		if err != nil || bytes.ContainsRune(log, '\r') {
			t.Fatalf("log = %q (%v)", log, err)
		}
		requireContains(t, string(log), "implement "+phase.Name, "stderr output")
		report, err := ReadReviews(opts.Root, state, phase.Name)
		if err != nil || bytes.ContainsRune(report, '\r') || phase.ImplementCommit == "" || phase.ReviewCommit == "" {
			t.Fatalf("report = %q, phase = %+v (%v)", report, phase, err)
		}
		requireContains(t, string(report), phase.File, phase.ImplementCommit, phase.Name+"-implement.txt", phase.Name+"-review.txt", "review "+phase.Name)
	}
	reports, err := ReadReviews(opts.Root, state, "")
	if err != nil {
		t.Fatal(err)
	}
	requireContains(t, string(reports), "review 01_alpha", "review 02_beta")
	summary, err := readText(opts.Root, state.RunDir+"/summary.md")
	if err != nil {
		t.Fatal(err)
	}
	requireContains(t, string(summary), "01_alpha_implement.log", "02_beta_review.md", "completed")
	opts.Phase = "02_beta"
	second, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := ListRuns(opts.Root)
	if err != nil || len(runs) != 2 || runs[0].RunID != second.State.RunID || runs[1].RunID != state.RunID {
		t.Fatalf("history: %+v (%v)", runs, err)
	}
	if err := os.Remove(filepath.Join(opts.Root, checkpoint)); err != nil {
		t.Fatal(err)
	}
	latest, err := LoadRun(opts.Root, "")
	if err != nil || latest.RunID != second.State.RunID {
		t.Fatalf("latest: %+v (%v)", latest, err)
	}
	if sha, err := CommitAll(context.Background(), opts.Root, "empty"); err != nil || sha != "" {
		t.Fatalf("empty commit: %q (%v)", sha, err)
	}
	gitOK(t, opts.Root, "add", ".reifu.state.json")
	if _, err := CommitAll(context.Background(), opts.Root, "forbidden"); err == nil {
		t.Fatal("committed a prestaged runtime file")
	}
}

func TestRunEvents(t *testing.T) {
	opts, progress := fixture(t, true)
	opts.HeartbeatInterval = 10 * time.Millisecond
	t.Setenv("REIFU_HELPER_MODE", "heartbeat")
	var events []model.RunEvent
	opts.EventSink = func(event model.RunEvent) { events = append(events, event) }
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.RunEventType{model.EventRunStarted}
	for range 2 {
		want = append(want, model.EventPhaseStarted)
		for range 2 {
			want = append(want, model.EventStepStarted, model.EventCommit, model.EventStepCompleted)
		}
		want = append(want, model.EventPhaseCompleted)
	}
	want = append(want, model.EventRunCompleted)
	var got []model.RunEventType
	var logs strings.Builder
	var heartbeats, phaseIndex int
	var step string
	for _, event := range events {
		if event.SchemaVersion != 1 || event.Timestamp.IsZero() || event.Timestamp.Location() != time.UTC || event.RunID != result.State.RunID {
			t.Fatalf("event metadata: %+v", event)
		}
		if event.Type == model.EventPhaseStarted {
			phaseIndex++
		}
		if event.Phase != "" && (event.PhaseIndex != phaseIndex || event.PhaseTotal != 2 || event.Phase != result.State.Phases[phaseIndex-1].Name) {
			t.Fatalf("phase metadata: %+v", event)
		}
		switch event.Type {
		case model.EventStepStarted:
			step, heartbeats = event.Step, 0
			logs.Reset()
			if event.Warning == "" || event.Agent == "" || event.Path == "" {
				t.Fatalf("step metadata: %+v", event)
			}
		case model.EventLog:
			if event.Step != step || event.Agent == "" || event.Text == "" || strings.ContainsRune(event.Text, '\r') {
				t.Fatalf("log event: %+v", event)
			}
			logs.WriteString(event.Text)
			continue
		case model.EventHeartbeat:
			if event.Step != step || event.Agent == "" || event.ElapsedMS < 10 {
				t.Fatalf("heartbeat event: %+v", event)
			}
			heartbeats++
			continue
		case model.EventCommit:
			if event.Step != step || event.Commit == "" || heartbeats == 0 {
				t.Fatalf("commit after %d heartbeats: %+v", heartbeats, event)
			}
			if actual := logs.String(); actual != step+" "+event.Phase+"\nstderr output\nafter heartbeat\n" &&
				actual != "stderr output\n"+step+" "+event.Phase+"\nafter heartbeat\n" {
				t.Fatalf("unexpected step output %q", actual)
			}
		case model.EventStepCompleted:
			if event.Step != step || event.Agent == "" || event.Path == "" || event.Commit == "" {
				t.Fatalf("step completion: %+v", event)
			}
			step = ""
		}
		got = append(got, event.Type)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events: got %v, want %v", got, want)
	}
	first, last := events[0].State, events[len(events)-1].State
	if first == nil || last == nil || last.Summary().Completed != 2 {
		t.Fatalf("run snapshots: start=%+v end=%+v", first, last)
	}
	for _, phase := range first.Phases {
		if phase.Status != model.PhasePending || phase.ImplementCommit != "" || phase.ReviewCommit != "" {
			t.Fatalf("start snapshot was modified: %+v", phase)
		}
	}
	if strings.Contains(progress.String(), "stderr output") || strings.Contains(progress.String(), "... running") {
		t.Fatalf("duplicated progress: %s", progress)
	}
}

func TestRunFailureEvents(t *testing.T) {
	for mode, status := range map[string]model.PhaseStatus{"fail-review": model.PhaseFailed, "block": model.PhaseBlocked, "timeout": model.PhaseFailed} {
		t.Run(mode, func(t *testing.T) {
			opts, _ := fixture(t, false)
			t.Setenv("REIFU_HELPER_MODE", mode)
			if mode == "timeout" {
				opts.Timeout = 300 * time.Millisecond
			}
			var events []model.RunEvent
			opts.EventSink = func(event model.RunEvent) { events = append(events, event) }
			if _, err := Run(context.Background(), opts); err == nil {
				t.Fatal("expected run failure")
			}
			if len(events) < 2 {
				t.Fatalf("too few events: %v", events)
			}
			phase, run := events[len(events)-2], events[len(events)-1]
			if phase.Type != model.EventPhaseFailed || phase.Status != status || phase.Error == "" || phase.PhaseIndex != 1 || phase.PhaseTotal != 2 ||
				run.Type != model.EventRunFailed || run.Status != status || run.Error == "" || run.State == nil || run.State.Summary().Status != status {
				t.Fatalf("terminal events: phase=%+v run=%+v", phase, run)
			}
			if mode == "timeout" && !strings.Contains(run.Error, context.DeadlineExceeded.Error()) {
				t.Fatalf("timeout error not reported: %q", run.Error)
			}
		})
	}
}

func TestRunPlanEvent(t *testing.T) {
	opts, _ := fixture(t, false)
	opts.DryRun = true
	var events []model.RunEvent
	opts.EventSink = func(event model.RunEvent) { events = append(events, event) }
	result, err := Run(context.Background(), opts)
	if err != nil || len(events) != 1 || events[0].Type != model.EventRunPlan || events[0].Plan == nil ||
		!reflect.DeepEqual(result.Plan, events[0].Plan) || events[0].SchemaVersion != 1 {
		t.Fatalf("plan=%+v events=%+v err=%v", result.Plan, events, err)
	}
	for _, name := range []string{".reifu", "01_alpha-implement.txt", "01_alpha-review.txt"} {
		if _, err := os.Lstat(filepath.Join(opts.Root, name)); !os.IsNotExist(err) {
			t.Fatalf("dry run created %s or stat failed: %v", name, err)
		}
	}
}

func TestResumeReview(t *testing.T) {
	opts, progress := fixture(t, true)
	opts.Phase = "01_alpha"
	t.Setenv("REIFU_HELPER_MODE", "fail-review")
	first, err := Run(context.Background(), opts)
	if model.ExitCodeOf(err) != model.ExitDrift || first.State == nil || first.State.Phases[0].ResumeStep != reviewStep || first.State.Phases[0].Status != model.PhaseFailed {
		t.Fatalf("failed review: %+v (%v)", first.State, err)
	}
	put(t, opts.Root, "unfinished-review.txt", "left by review\n")
	t.Setenv("REIFU_HELPER_MODE", "")
	progress.Reset()
	opts.Phase, opts.Resume = "", true
	if _, err := Run(context.Background(), opts); model.ExitCodeOf(err) != model.ExitGeneral || !strings.Contains(err.Error(), "--resume --allow-dirty") {
		t.Fatalf("dirty resume: %v", err)
	}
	for _, flag := range []string{"no-review", "no-commit"} {
		bad := opts
		if flag == "no-review" {
			bad.NoReview = true
		} else {
			bad.NoCommit = true
		}
		if _, err := Run(context.Background(), bad); model.ExitCodeOf(err) != model.ExitGeneral || !strings.Contains(err.Error(), "resume cannot be combined") {
			t.Fatalf("resume with %s: %v", flag, err)
		}
	}
	opts.AllowDirty = true
	opts.Config.Runner.Agents["builder"] = []string{"missing-implementer-on-review-resume"}
	second, err := Run(context.Background(), opts)
	if err != nil || second.State.RunID != first.State.RunID || second.State.Summary().Status != model.PhaseCompleted ||
		second.State.Phases[0].ImplementCommit != first.State.Phases[0].ImplementCommit || strings.Contains(progress.String(), "starting 01_alpha (implement:") {
		t.Fatalf("resume: %+v (%v)\n%s", second.State, err, progress)
	}
}

func TestPlanAndStops(t *testing.T) {
	t.Run("dry-run and filtering", func(t *testing.T) {
		opts, _ := fixture(t, false)
		opts.Config.Runner.Agents["builder"] = []string{"missing-phase-test-executable"}
		opts.DryRun, opts.NoReview, opts.From = true, true, "02_beta"
		res, err := Run(context.Background(), opts)
		if err != nil || len(res.Plan.State.Phases) != 1 || res.Plan.State.Phases[0].Name != "02_beta" ||
			res.Plan.Implementer.Argv[0] != "missing-phase-test-executable" || res.Plan.Reviewer != nil {
			t.Fatalf("dry run: %+v (%v)", res, err)
		}
		if _, err := os.Stat(filepath.Join(opts.Root, ".reifu")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dry-run wrote runtime directory: %v", err)
		}
		opts.Phase = "01_alpha"
		if _, err := Run(context.Background(), opts); err == nil {
			t.Fatal("accepted conflicting selectors")
		}
		opts.Phase, opts.DryRun = "", false
		if _, err := Run(context.Background(), opts); model.ExitCodeOf(err) != model.ExitGeneral {
			t.Fatalf("missing executable: %v", err)
		}
	})
	t.Run("no review and no commit", func(t *testing.T) {
		opts, progress := fixture(t, false)
		opts.NoReview, opts.From = true, "02_beta.md"
		if err := os.Remove(filepath.Join(opts.Root, reviewPrompt)); err != nil {
			t.Fatal(err)
		}
		res, err := Run(context.Background(), opts)
		if err != nil || len(res.State.Phases) != 1 || res.State.Phases[0].Name != "02_beta" ||
			res.State.Phases[0].ImplementCommit != "" || strings.Contains(progress.String(), "review:") {
			t.Fatalf("no review: %+v (%v)\n%s", res.State, err, progress)
		}
	})
	t.Run("blocked", func(t *testing.T) {
		opts, _ := fixture(t, false)
		t.Setenv("REIFU_HELPER_MODE", "block")
		res, err := Run(context.Background(), opts)
		if model.ExitCodeOf(err) != model.ExitDrift || res.State.Phases[0].Status != model.PhaseBlocked ||
			res.State.Phases[1].Status != model.PhasePending {
			t.Fatalf("blocked: %+v (%v)", res.State, err)
		}
		if next, err := Run(context.Background(), opts); model.ExitCodeOf(err) != model.ExitDrift || next.State != nil {
			t.Fatalf("existing report: %+v (%v)", next, err)
		}
	})
	t.Run("timeout and heartbeat", func(t *testing.T) {
		opts, progress := fixture(t, false)
		t.Setenv("REIFU_HELPER_MODE", "timeout")
		opts.Timeout, opts.HeartbeatInterval = 300*time.Millisecond, 20*time.Millisecond
		res, err := Run(context.Background(), opts)
		if model.ExitCodeOf(err) != model.ExitDrift || !errors.Is(err, context.DeadlineExceeded) || res.State.Phases[0].Status != model.PhaseFailed {
			t.Fatalf("timeout: %+v (%v)", res.State, err)
		}
		requireContains(t, progress.String(), "... running [1/2] 01_alpha", "elapsed")
	})
	t.Run("timeout kills the agent process tree", func(t *testing.T) {
		opts, _ := fixture(t, false)
		t.Setenv("REIFU_HELPER_MODE", "tree")
		opts.Timeout = 700 * time.Millisecond
		if _, err := Run(context.Background(), opts); model.ExitCodeOf(err) != model.ExitDrift || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout: %v", err)
		}
		if _, err := os.Stat(filepath.Join(opts.Root, "grandchild-started.txt")); err != nil {
			t.Fatalf("grandchild did not start before the timeout: %v", err)
		}
		time.Sleep(2 * time.Second)
		if _, err := os.Stat(filepath.Join(opts.Root, "orphan.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("agent subprocess kept running after the timeout: %v", err)
		}
	})
	t.Run("missing implementer error", func(t *testing.T) {
		opts, _ := fixture(t, false)
		opts.Config.Runner.Implementer = ""
		opts.Implementer = ""
		opts.DryRun = true
		if _, err := Run(context.Background(), opts); model.ExitCodeOf(err) != model.ExitGeneral || !strings.Contains(err.Error(), "missing implementer agent") {
			t.Fatalf("missing implementer: %v", err)
		}
	})
	t.Run("agent self-commit preserves commit SHA and diff stat", func(t *testing.T) {
		opts, progress := fixture(t, true)
		opts.Phase = "01_alpha"
		t.Setenv("REIFU_HELPER_MODE", "self-commit")
		res, err := Run(context.Background(), opts)
		if err != nil {
			t.Fatalf("self-commit run: %v\n%s", err, progress)
		}
		p := res.State.Phases[0]
		if p.ImplementCommit == "" || p.ReviewCommit == "" || p.ImplementCommit == p.ReviewCommit {
			t.Fatalf("expected distinct agent commits recorded: %+v", p)
		}
		report, err := ReadReviews(opts.Root, *res.State, p.Name)
		if err != nil {
			t.Fatal(err)
		}
		requireContains(t, string(report), p.ImplementCommit, "01_alpha-implement.txt", "01_alpha-review.txt")
	})
	t.Run("commit succeeds when runtime files are listed in .gitignore", func(t *testing.T) {
		opts, progress := fixture(t, true)
		put(t, opts.Root, ".gitignore", ".reifu/\n.reifu.run.json\n.reifu.state.json\n.reifu.lock\n.reifu.tmp.*\nBLOCKED_REPORT.md\n")
		gitOK(t, opts.Root, "add", ".gitignore")
		gitOK(t, opts.Root, "commit", "-m", "ignore runtime files")
		put(t, opts.Root, ".reifu.state.json", "{}\n")
		opts.Phase = "01_alpha"
		res, err := Run(context.Background(), opts)
		if err != nil {
			t.Fatalf("run with .gitignore runtime entries: %v\n%s", err, progress)
		}
		if res.State.Summary().Status != model.PhaseCompleted || res.State.Phases[0].ImplementCommit == "" || res.State.Phases[0].ReviewCommit == "" {
			t.Fatalf("unexpected state: %+v", res.State)
		}
		tracked := gitOK(t, opts.Root, "ls-files")
		if strings.Contains(strings.ToLower(tracked), ".reifu") || strings.Contains(tracked, blockedReport) {
			t.Fatalf("runtime files committed: %s", tracked)
		}
	})
	t.Run("dirty worktree", func(t *testing.T) {
		opts, _ := fixture(t, true)
		opts.NoReview, opts.Phase = true, "01_alpha"
		put(t, opts.Root, "dirty.txt", "change")
		if _, err := Run(context.Background(), opts); model.ExitCodeOf(err) != model.ExitGeneral {
			t.Fatalf("dirty tree: %v", err)
		}
		opts.AllowDirty = true
		if _, err := Run(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	})
}

type followWriter struct {
	bytes.Buffer
	root string
}

func (w *followWriter) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	if err == nil && strings.Contains(w.String(), "stderr output") {
		err = os.WriteFile(filepath.Join(w.root, "proceed"), nil, 0644)
	}
	return n, err
}

func TestFollowLog(t *testing.T) {
	opts, _ := fixture(t, false)
	t.Setenv("REIFU_HELPER_MODE", "follow")
	opts.NoReview, opts.Phase, opts.Timeout = true, "01_alpha", 10*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := Run(ctx, opts); finished <- err }()
	var state model.RunState
	for {
		run, err := LoadRun(opts.Root, "")
		if err == nil {
			state = run
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case err := <-finished:
			t.Fatalf("run ended before follow: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	out := &followWriter{root: opts.Root}
	if err := FollowLog(ctx, opts.Root, state, "01_alpha", 10*time.Millisecond, out); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	requireContains(t, out.String(), "implement 01_alpha", "stderr output", "final output")
	if strings.Count(out.String(), "final output") != 1 || strings.Count(out.String(), "stderr output") != 1 {
		t.Fatalf("duplicate output: %q", out)
	}
}

func directoryLink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(1314)) {
			t.Fatal(err)
		}
		output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", path, target).CombinedOutput()
		if err != nil {
			t.Fatalf("create junction: %v (%s)", err, output)
		}
	}
}

func TestReadAndRunPathBoundary(t *testing.T) {
	opts, _ := fixture(t, false)
	for _, id := range []string{"../escape", "..\\escape", "x/y", "x\x00"} {
		if _, err := LoadRun(opts.Root, id); err == nil {
			t.Fatalf("accepted run ID %q", id)
		}
	}
	outside := t.TempDir()
	put(t, outside, "outside.md", "outside")
	directoryLink(t, outside, filepath.Join(opts.Root, ".reifu"))
	opts.DryRun = true
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("accepted escaped output path")
	}
	if _, err := ListRuns(opts.Root); err == nil {
		t.Fatal("listed escaped history path")
	}
	if err := os.Remove(filepath.Join(opts.Root, ".reifu")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(opts.Root, "prompts", "phases"), filepath.Join(opts.Root, "saved")); err != nil {
		t.Fatal(err)
	}
	directoryLink(t, outside, filepath.Join(opts.Root, "prompts", "phases"))
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("read phases outside the project")
	}
}

func TestResumeAfterCommitBeforeCheckpoint(t *testing.T) {
	opts, progress := fixture(t, true)
	opts.NoReview, opts.Phase = true, "01_alpha"
	e := execution{Options: opts}
	if err := e.prepare(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := gitOK(t, opts.Root, "rev-parse", "HEAD")
	phase := &e.run.Phases[0]
	phase.Status, phase.ResumeStep, phase.CommitBase = model.PhaseImplementing, implementCommitStep, &base
	put(t, opts.Root, phase.ImplementLog, "completed agent\n")
	put(t, opts.Root, "generated.txt", "generated\n")
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	sha, err := CommitAll(ctx, opts.Root, "feat(phase): complete "+phase.Name)
	if err != nil || sha == "" {
		t.Fatalf("commit: %q (%v)", sha, err)
	}
	opts.Resume, opts.Phase, opts.NoReview = true, "", false
	opts.Config.Runner.Agents["builder"] = []string{"missing-implementer-after-commit"}
	result, err := Run(ctx, opts)
	if err != nil || result.State.Phases[0].ImplementCommit != sha || result.State.Phases[0].Status != model.PhaseCompleted {
		t.Fatalf("resume committed step: %+v (%v)\n%s", result.State, err, progress)
	}
	if head := gitOK(t, opts.Root, "rev-parse", "--short", "HEAD"); head != sha {
		t.Fatalf("resume changed HEAD to %q", head)
	}
}

func TestRunNotifications(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mode         model.NotifyMode
		agentMode    string
		phase        string
		dryRun       bool
		wantMessages []string
		wantFailure  bool
	}{
		{name: "default"},
		{name: "end", mode: model.NotifyEnd, wantMessages: []string{"completed (2/2)"}},
		{name: "failure", mode: model.NotifyEnd, agentMode: "fail-review", wantMessages: []string{"failed at 01_alpha (0/2 completed)"}, wantFailure: true},
		{name: "blocked", mode: model.NotifyEnd, agentMode: "block", wantMessages: []string{"blocked at 01_alpha (0/2 completed)"}, wantFailure: true},
		{name: "timeout", mode: model.NotifyEnd, agentMode: "timeout", wantMessages: []string{"timed out at 01_alpha (0/2 completed)"}, wantFailure: true},
		{name: "phase", mode: model.NotifyPhase, wantMessages: []string{"completed 01_alpha (1/2)", "completed (2/2)"}},
		{name: "phase-single", mode: model.NotifyPhase, phase: "01_alpha", wantMessages: []string{"completed (1/1)"}},
		{name: "dry-run", mode: model.NotifyPhase, dryRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _ := fixture(t, false)
			t.Setenv("REIFU_HELPER_MODE", tc.agentMode)
			if tc.agentMode == "timeout" {
				opts.Timeout = 100 * time.Millisecond
			}
			opts.Config.Project.Name = "notification project"
			opts.Config.Runner.Notify = tc.mode
			opts.DryRun = tc.dryRun
			opts.Phase = tc.phase
			var got []sysenv.Notification
			opts.Notifier = func(n sysenv.Notification) { got = append(got, n) }
			_, err := Run(context.Background(), opts)
			if (err != nil) != tc.wantFailure {
				t.Fatalf("Run error = %v, want failure %t", err, tc.wantFailure)
			}
			var want []sysenv.Notification
			for _, message := range tc.wantMessages {
				want = append(want, sysenv.Notification{Title: "reifu run", Message: "notification project: " + message, Success: !tc.wantFailure})
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("notifications = %+v, want %+v", got, want)
			}
		})
	}
}

func TestResumeNotifications(t *testing.T) {
	for _, override := range []model.NotifyMode{"", model.NotifyOff} {
		t.Run(string(override), func(t *testing.T) {
			opts, _ := fixture(t, false)
			opts.Notify = model.NotifyPhase
			opts.Notifier = func(sysenv.Notification) {}
			t.Setenv("REIFU_HELPER_MODE", "fail-review")
			result, err := Run(context.Background(), opts)
			if err == nil || result.State == nil || result.State.Notify != model.NotifyPhase {
				t.Fatalf("initial run = %+v, %v", result, err)
			}

			t.Setenv("REIFU_HELPER_MODE", "")
			opts.Resume, opts.NoCommit = true, false
			opts.Notify = override
			opts.Config.Runner.Notify = model.NotifyEnd
			var got []sysenv.Notification
			opts.Notifier = func(n sysenv.Notification) { got = append(got, n) }
			result, err = Run(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			mode, wantCount := model.NotifyPhase, 2
			if override == model.NotifyOff {
				mode, wantCount = model.NotifyOff, 0
			}
			if result.State.Notify != mode || len(got) != wantCount {
				t.Fatalf("resumed mode = %q, notifications = %+v, want %q and %d notices", result.State.Notify, got, mode, wantCount)
			}
			for _, n := range got {
				if !n.Success {
					t.Fatalf("unexpected failure notification: %+v", n)
				}
			}
		})
	}
}

func TestRunNotificationOnCancellation(t *testing.T) {
	opts, _ := fixture(t, false)
	opts.Notify = model.NotifyPhase
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []sysenv.Notification
	opts.Notifier = func(n sysenv.Notification) {
		got = append(got, n)
		cancel()
	}
	if _, err := Run(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want cancellation", err)
	}
	if len(got) != 2 || !got[0].Success || got[1].Success ||
		!strings.Contains(got[1].Message, "canceled at 02_beta (1/2 completed)") {
		t.Fatalf("unexpected notifications: %+v", got)
	}
}
