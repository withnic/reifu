package runner

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/withnic/reifu/internal/compiler"
	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/sysenv"
)

const (
	phasesDir           = "prompts/phases"
	reviewPrompt        = "prompts/review.md"
	blockedReport       = "BLOCKED_REPORT.md"
	implementStep       = "implement"
	reviewStep          = "review"
	implementCommitStep = "implement_commit"
	reviewCommitStep    = "review_commit"
)

// DefaultTimeout is the CLI default. A zero Options.Timeout is unlimited.
const DefaultTimeout = 30 * time.Minute

type Options struct {
	Root                                           string
	Config                                         model.ReifuConfig
	Implementer, Reviewer                          string
	Phase, From                                    string
	Resume, NoReview, NoCommit, AllowDirty, DryRun bool
	SkipCompleted                                  bool
	Timeout, HeartbeatInterval                     time.Duration
	Progress                                       io.Writer
	Diagnostics                                    io.Writer
	Notify                                         model.NotifyMode
	Notifier                                       func(sysenv.Notification)
	EventSink                                      func(model.RunEvent)
}

type Plan = model.RunPlan

type Result struct {
	Plan  *Plan           `json:"plan,omitempty"`
	State *model.RunState `json:"state,omitempty"`
}

type execution struct {
	Options
	run                       model.RunState
	implementer, reviewer     AgentCommand
	implementPath, reviewPath string
	agySettingsRel            string
	agySettingsData           []byte
	gitRepo                   bool
	eventMu                   sync.Mutex
}

func (e *execution) emit(ev model.RunEvent) {
	if e.EventSink == nil {
		return
	}
	e.eventMu.Lock()
	defer e.eventMu.Unlock()
	ev.SchemaVersion = 1
	ev.Timestamp = time.Now().UTC()
	if ev.RunID == "" {
		ev.RunID = e.run.RunID
	}
	e.EventSink(ev)
}

func (e *execution) snapshot() *model.RunState {
	state := e.run
	state.Phases = slices.Clone(state.Phases)
	for i := range state.Phases {
		if base := state.Phases[i].CommitBase; base != nil {
			value := *base
			state.Phases[i].CommitBase = &value
		}
		state.Phases[i].DeniedCommands = slices.Clone(state.Phases[i].DeniedCommands)
	}
	return &state
}

func (e *execution) phaseEvent(kind model.RunEventType, i int, phase *model.PhaseRunRecord) model.RunEvent {
	return model.RunEvent{Type: kind, Phase: phase.Name, PhaseIndex: i + 1, PhaseTotal: len(e.run.Phases)}
}

func Run(ctx context.Context, opts Options) (Result, error) {
	var selections int
	for _, selected := range []bool{opts.Phase != "", opts.From != "", opts.Resume, opts.SkipCompleted} {
		if selected {
			selections++
		}
	}
	if selections > 1 {
		return Result{}, fmt.Errorf("--phase, --from, --resume and --skip-completed are mutually exclusive")
	}
	if opts.Resume && (opts.NoReview || opts.NoCommit) {
		return Result{}, fmt.Errorf("resume cannot be combined with --no-review or --no-commit")
	}
	if opts.Timeout < 0 {
		return Result{}, fmt.Errorf("timeout must be nonnegative")
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return Result{}, err
	}
	opts.Root = root
	home, err := os.UserHomeDir()
	if err != nil {
		return Result{}, fmt.Errorf("locate home directory: %w", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return Result{}, err
	}
	if !rootInfo.IsDir() {
		return Result{}, fmt.Errorf("project root %q is not a directory", root)
	}
	if homeInfo, err := os.Stat(home); err == nil && os.SameFile(rootInfo, homeInfo) {
		return Result{}, fmt.Errorf("refusing to run in home directory %q", root)
	}
	if opts.Progress == nil {
		opts.Progress = os.Stderr
	}
	if opts.Diagnostics == nil {
		opts.Diagnostics = opts.Progress
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 30 * time.Second
	}
	if err := checkBlocked(root); err != nil {
		return Result{}, err
	}
	e := execution{Options: opts}
	if opts.DryRun {
		if err := e.prepare(); err != nil {
			return Result{}, err
		}
		plan := &Plan{Implementer: e.implementer, State: e.run}
		if e.reviewer.Name != "" {
			plan.Reviewer = &e.reviewer
		}
		e.emit(model.RunEvent{Type: model.EventRunPlan, Plan: plan})
		return Result{Plan: plan}, nil
	}
	if err := fsx.EnsureDirWithinRoot(root, ".reifu"); err != nil {
		return Result{}, err
	}
	var result Result
	err = fsx.WithNamedFileLock(root, ".reifu/run.lock", func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkBlocked(root); err != nil {
			return err
		}
		if err := e.prepare(); err != nil {
			return err
		}
		if err := e.preflight(ctx); err != nil {
			return err
		}
		result.State = &e.run
		return e.execute(ctx)
	})
	return result, err
}

func checkBlocked(root string) error {
	path, err := projectPath(root, blockedReport)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return &model.ExitError{Code: model.ExitDrift, Err: fmt.Errorf("resolve %s before continuing", path)}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func phaseRecord(dir, name string) (model.PhaseRunRecord, error) {
	stem := strings.TrimSuffix(name, ".md")
	if stem == "" || !strings.HasSuffix(name, ".md") || !utf8.ValidString(name) ||
		strings.ContainsAny(name, "/\\:") || strings.ContainsFunc(name, unicode.IsControl) {
		return model.PhaseRunRecord{}, fmt.Errorf("invalid phase file name %q", name)
	}
	return model.PhaseRunRecord{
		Name: stem, File: phasesDir + "/" + name, Status: model.PhasePending,
		ImplementLog: dir + "/" + stem + "_implement.log", ReviewReport: dir + "/" + stem + "_review.md",
	}, nil
}

func discover(root, dir string) ([]model.PhaseRunRecord, error) {
	path, err := projectPath(root, phasesDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path) // ReadDir sorts by filename.
	if err != nil {
		return nil, err
	}
	var phases []model.PhaseRunRecord
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		phase, err := phaseRecord(dir, entry.Name())
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(phase.Name)
		if seen[key] {
			return nil, fmt.Errorf("phase log names collide for %q", phase.Name)
		}
		seen[key] = true
		if err := requireFile(root, phase.File); err != nil {
			return nil, err
		}
		phases = append(phases, phase)
	}
	if len(phases) == 0 {
		return nil, fmt.Errorf("no Markdown phases in %q", path)
	}
	return phases, nil
}

func requireFile(root, rel string) error {
	file, err := openRead(root, rel)
	if err != nil {
		return err
	}
	return file.Close()
}

func (e *execution) prepare() error {
	if e.Resume {
		run, err := loadState(e.Root, checkpoint)
		if err != nil {
			return err
		}
		e.run = run
	} else {
		now := time.Now().UTC()

		// A timestamp provides readable ordering and a random suffix avoids collisions.
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return fmt.Errorf("generate run ID: %w", err)
		}
		id := fmt.Sprintf("%s-%x", now.Format("20060102T150405Z"), nonce)
		e.run = model.RunState{Version: 1, RunID: id, RunDir: runsDir + "/" + id, StartedAt: now, UpdatedAt: now,
			NoReview: e.NoReview, NoCommit: e.NoCommit}
		phases, err := discover(e.Root, e.run.RunDir)
		if err != nil {
			return err
		}
		e.run.Phases = phases
		if e.SkipCompleted {
			e.carryCompleted()
		}
		if e.Phase != "" || e.From != "" {
			i, err := phaseIndex(e.run, firstSet(e.Phase, e.From))
			if err != nil {
				return err
			}
			if e.Phase != "" {
				e.run.Phases = phases[i : i+1]
			} else {
				e.run.Phases = phases[i:]
			}
		}
	}
	e.run.Notify = model.NotifyMode(firstSet(string(e.Notify), string(e.run.Notify), string(e.Config.Runner.Notify), string(model.NotifyOff)))
	if err := e.run.Notify.Validate(); err != nil {
		return err
	}
	e.run.Implementer = firstSet(e.Implementer, e.run.Implementer, e.Config.Runner.Implementer)
	e.run.Reviewer = firstSet(e.Reviewer, e.run.Reviewer, e.Config.Runner.Reviewer, e.run.Implementer)
	if e.SkipCompleted && e.run.Summary().Status == model.PhaseCompleted {
		fmt.Fprintln(e.Diagnostics, "実行するフェーズがない")
		return nil
	}
	if e.run.Implementer == "" {
		return fmt.Errorf("missing implementer agent; specify --implementer or set runner.implementer in .reifu.jsonc")
	}
	var err error
	e.implementer, err = ResolveAgent(e.Config, e.run.Implementer)
	if err != nil {
		return err
	}
	if !e.run.NoReview {
		e.reviewer, err = ResolveAgent(e.Config, e.run.Reviewer)
		if err != nil {
			return err
		}
		if err := requireFile(e.Root, reviewPrompt); err != nil {
			return err
		}
	}
	needsAgyImpl := !e.implementer.IsCustom && e.implementer.Name == "agy"
	needsAgyRev := !e.run.NoReview && !e.reviewer.IsCustom && e.reviewer.Name == "agy"
	if needsAgyImpl || needsAgyRev {
		data, ok, err := compiler.CompileAgySettings(e.Config)
		if err != nil {
			return err
		}
		if ok {
			e.agySettingsRel = e.run.RunDir + "/agy-settings.json"
			e.agySettingsData = data
			if needsAgyImpl {
				e.implementer = withAgySettings(e.implementer, e.agySettingsRel)
			}
			if needsAgyRev {
				e.reviewer = withAgySettings(e.reviewer, e.agySettingsRel)
			}
		}
	}
	pathsToCheck := []string{checkpoint, e.run.RunDir + "/run.json", e.run.RunDir + "/summary.md", ".reifu/run.lock"}
	if e.agySettingsRel != "" {
		pathsToCheck = append(pathsToCheck, e.agySettingsRel)
	}
	for _, rel := range pathsToCheck {
		if _, err := projectPath(e.Root, rel); err != nil {
			return err
		}
	}
	for _, phase := range e.run.Phases {
		if e.SkipCompleted && phase.Status == model.PhaseCompleted {
			continue
		}
		for _, rel := range []string{phase.File, phase.ImplementLog, phase.ReviewReport, e.run.RunDir + "/" + phase.Name + "_commands.json"} {
			if _, err := projectPath(e.Root, rel); err != nil {
				return err
			}
		}
	}
	return nil
}

func completedPhases(run model.RunState) map[string]model.PhaseRunRecord {
	completed := make(map[string]model.PhaseRunRecord)
	for _, phase := range run.Phases {
		if phase.Status == model.PhaseCompleted {
			completed[strings.ToLower(phase.Name)] = phase
		}
	}
	return completed
}

func (e *execution) carryCompleted() {
	previous, err := loadState(e.Root, checkpoint)
	if err != nil {
		fmt.Fprintf(e.Diagnostics, "warning: could not read %s; starting all phases: %v\n", checkpoint, err)
		return
	}
	completed := completedPhases(previous)
	for i := range e.run.Phases {
		phase := &e.run.Phases[i]
		if prior, ok := completed[strings.ToLower(phase.Name)]; ok {
			phase.Status = prior.Status
			phase.ImplementCommit = prior.ImplementCommit
			phase.ReviewCommit = prior.ReviewCommit
			phase.ImplementLog = prior.ImplementLog
			phase.ReviewReport = prior.ReviewReport
			phase.CommandsLog = prior.CommandsLog
			phase.DurationMS = prior.DurationMS
		}
	}
}

func firstSet(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (e *execution) preflight(ctx context.Context) error {
	var err error
	if e.run.Summary().Status == model.PhaseCompleted {
		return nil
	}
	var needsImplementer, needsReviewer bool
	for _, phase := range e.run.Phases {
		if phase.Status == model.PhaseCompleted {
			continue
		}
		step := resumeStep(phase)
		needsImplementer = needsImplementer || step == implementStep
		needsReviewer = needsReviewer || (!e.run.NoReview && step != reviewCommitStep)
	}
	if needsImplementer {
		e.implementPath, err = lookPath(e.Root, e.implementer)
		if err != nil {
			return err
		}
	}
	if needsReviewer {
		e.reviewPath, err = lookPath(e.Root, e.reviewer)
		if err != nil {
			return err
		}
	}
	e.gitRepo, err = IsGitRepo(ctx, e.Root)
	if e.run.NoCommit {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if !e.gitRepo {
		return fmt.Errorf("%q is not a Git worktree", e.Root)
	}
	if err := requireWorktreeRoot(ctx, e.Root); err != nil {
		return err
	}
	if !e.AllowDirty {
		status, err := StatusPorcelain(ctx, e.Root)
		if err != nil {
			return err
		}
		if status != "" {
			if e.Resume {
				return fmt.Errorf("Git worktree has uncommitted changes; inspect them and use --resume --allow-dirty to continue")
			}
			return fmt.Errorf("Git worktree has uncommitted changes; commit or stash them, or use --allow-dirty to continue")
		}
	}
	return nil
}

func (e *execution) save() error {
	e.run.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(e.run, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := fsx.WriteFileAtomic(e.Root, checkpoint, data); err != nil {
		return err
	}
	return fsx.WriteFileAtomic(e.Root, e.run.RunDir+"/run.json", data)
}

func (e *execution) execute(ctx context.Context) (runErr error) {
	project := firstSet(e.Config.Project.Name, filepath.Base(e.Root))
	var phaseName string
	if e.run.Notify == model.NotifyEnd || e.run.Notify == model.NotifyPhase {
		defer func() {
			summary := e.run.Summary()
			if runErr == nil {
				e.notify(fmt.Sprintf("%s: completed (%d/%d)", project, summary.Completed, summary.Total), true)
				return
			}
			status := "failed"
			switch {
			case summary.Status == model.PhaseBlocked:
				status = "blocked"
			case errors.Is(runErr, context.DeadlineExceeded):
				status = "timed out"
			case errors.Is(runErr, context.Canceled):
				status = "canceled"
			}
			if phaseName != "" {
				status += " at " + phaseName
			}
			e.notify(fmt.Sprintf("%s: %s (%d/%d completed)", project, status, summary.Completed, summary.Total), false)
		}()
	}
	if err := fsx.EnsureDirWithinRoot(e.Root, e.run.RunDir); err != nil {
		return err
	}
	if e.agySettingsRel != "" {
		if err := fsx.WriteFileAtomic(e.Root, e.agySettingsRel, e.agySettingsData); err != nil {
			return err
		}
	}
	if err := e.save(); err != nil {
		return err
	}
	if e.EventSink != nil {
		e.emit(model.RunEvent{Type: model.EventRunStarted, State: e.snapshot()})
		defer func() {
			event := model.RunEvent{Type: model.EventRunCompleted, Status: model.PhaseCompleted, State: e.snapshot()}
			if runErr != nil {
				event.Type, event.Status, event.Error = model.EventRunFailed, model.PhaseFailed, runErr.Error()
				if e.run.Summary().Status == model.PhaseBlocked {
					event.Status = model.PhaseBlocked
				}
				event.DeniedCommands = e.collectDeniedCommands()
			}
			e.emit(event)
		}()
	}
	if e.SkipCompleted && e.run.Summary().Status == model.PhaseCompleted {
		e.summary()
	}
	for i := range e.run.Phases {
		phase := &e.run.Phases[i]
		if phase.Status == model.PhaseCompleted {
			continue
		}
		phaseName = phase.Name
		if err := e.executePhase(ctx, i, phase); err != nil {
			return err
		}
		e.summary()
		if e.run.Notify == model.NotifyPhase {
			summary := e.run.Summary()
			if summary.Completed < summary.Total {
				e.notify(fmt.Sprintf("%s: completed %s (%d/%d)", project, phaseName, summary.Completed, summary.Total), true)
			}
		}
	}
	return nil
}

func (e *execution) notify(message string, success bool) {
	n := sysenv.Notification{Title: "reifu run", Message: message, Success: success}
	if e.Notifier != nil {
		e.Notifier(n)
		return
	}
	sysenv.Notify(n, e.Progress)
}

func (e *execution) summary() {
	var body strings.Builder
	fmt.Fprintf(&body, "# Run %s\n\n| Phase | Status | Implementation | Review | Duration (ms) | Reports |\n| --- | --- | --- | --- | ---: | --- |\n", e.run.RunID)
	var deniedLines []string
	for _, p := range e.run.Phases {
		review := ""
		if !e.run.NoReview {
			review = fmt.Sprintf(" [review](%s)", summaryLink(e.run.RunDir, p.ReviewReport))
		}
		commands := ""
		if p.CommandsLog != "" {
			commands = fmt.Sprintf(" [commands](%s)", summaryLink(e.run.RunDir, p.CommandsLog))
		}
		fmt.Fprintf(&body, "| %s | %s | %s | %s | %d | [log](%s)%s%s |\n",
			strings.ReplaceAll(p.Name, "|", "\\|"), p.Status, p.ImplementCommit, p.ReviewCommit, p.DurationMS,
			summaryLink(e.run.RunDir, p.ImplementLog), review, commands)
		if len(p.DeniedCommands) > 0 {
			quoted := make([]string, len(p.DeniedCommands))
			for i, cmd := range p.DeniedCommands {
				quoted[i] = fmt.Sprintf("`%s`", strings.ReplaceAll(cmd, "`", "'"))
			}
			deniedLines = append(deniedLines, fmt.Sprintf("- %s: %s", p.Name, strings.Join(quoted, ", ")))
		}
	}
	if len(deniedLines) > 0 {
		body.WriteString("\n## Denied commands\n\n")
		body.WriteString("Candidates to review for `permissions.allow_commands` in `.reifu.jsonc`:\n\n")
		for _, line := range deniedLines {
			body.WriteString(line + "\n")
		}
	}
	if err := fsx.WriteFileAtomic(e.Root, e.run.RunDir+"/summary.md", []byte(body.String())); err != nil {
		fmt.Fprintf(e.Progress, "warning: write run summary: %v\n", err)
	}
}

func summaryLink(runDir, rel string) string {
	dir, file := filepath.ToSlash(filepath.Dir(rel)), filepath.Base(rel)
	if dir == runDir {
		return markdownLink(file)
	}
	return "../" + markdownLink(filepath.Base(dir)) + "/" + markdownLink(file)
}

func (e *execution) collectDeniedCommands() []string {
	var denied []string
	for _, p := range e.run.Phases {
		for _, cmd := range p.DeniedCommands {
			if !slices.Contains(denied, cmd) {
				denied = append(denied, cmd)
			}
		}
	}
	return denied
}

func (e *execution) fail(i int, phase *model.PhaseRunRecord, err error) error {
	phase.Status = model.PhaseFailed
	if model.ExitCodeOf(err) == model.ExitDrift {
		phase.Status = model.PhaseBlocked
	}
	phase.Error = err.Error()
	saveErr := e.save()
	e.summary()
	event := e.phaseEvent(model.EventPhaseFailed, i, phase)
	event.Status, event.Error, event.ElapsedMS = phase.Status, phase.Error, phase.DurationMS
	event.DeniedCommands = slices.Clone(phase.DeniedCommands)
	e.emit(event)
	return &model.ExitError{Code: model.ExitDrift, Field: phase.Name, Err: errors.Join(err, saveErr)}
}
