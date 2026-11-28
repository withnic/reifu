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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/model"
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
	Timeout, HeartbeatInterval                     time.Duration
	Progress                                       io.Writer
}

type Plan struct {
	Implementer AgentCommand   `json:"implementer"`
	Reviewer    *AgentCommand  `json:"reviewer,omitempty"`
	State       model.RunState `json:"state"`
}

type Result struct {
	Plan  *Plan           `json:"plan,omitempty"`
	State *model.RunState `json:"state,omitempty"`
}

type execution struct {
	Options
	run                       model.RunState
	implementer, reviewer     AgentCommand
	implementPath, reviewPath string
	gitRepo                   bool
}

func Run(ctx context.Context, opts Options) (Result, error) {
	if opts.Phase != "" && opts.From != "" || opts.Resume && (opts.Phase != "" || opts.From != "") {
		return Result{}, fmt.Errorf("phase, from and resume are mutually exclusive")
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
		if !e.run.NoReview {
			plan.Reviewer = &e.reviewer
		}
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
	e.run.Implementer = firstSet(e.Implementer, e.run.Implementer, e.Config.Runner.Implementer)
	e.run.Reviewer = firstSet(e.Reviewer, e.run.Reviewer, e.Config.Runner.Reviewer, e.run.Implementer)
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
	for _, rel := range []string{checkpoint, e.run.RunDir + "/run.json", e.run.RunDir + "/summary.md", ".reifu/run.lock"} {
		if _, err := projectPath(e.Root, rel); err != nil {
			return err
		}
	}
	for _, phase := range e.run.Phases {
		for _, rel := range []string{phase.File, phase.ImplementLog, phase.ReviewReport} {
			if _, err := projectPath(e.Root, rel); err != nil {
				return err
			}
		}
	}
	return nil
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
		e.implementPath, err = e.implementer.lookPath(e.Root)
		if err != nil {
			return err
		}
	}
	if needsReviewer {
		e.reviewPath, err = e.reviewer.lookPath(e.Root)
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
			return fmt.Errorf("Git worktree has uncommitted changes; use --allow-dirty to continue")
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

func (e *execution) execute(ctx context.Context) error {
	if err := fsx.EnsureDirWithinRoot(e.Root, e.run.RunDir); err != nil {
		return err
	}
	if err := e.save(); err != nil {
		return err
	}
	for i := range e.run.Phases {
		phase := &e.run.Phases[i]
		if phase.Status == model.PhaseCompleted {
			continue
		}
		if err := e.executePhase(ctx, i, phase); err != nil {
			return err
		}
		e.summary()
	}
	return nil
}

func (e *execution) summary() {
	var body strings.Builder
	fmt.Fprintf(&body, "# Run %s\n\n| Phase | Status | Implementation | Review | Duration (ms) | Reports |\n| --- | --- | --- | --- | ---: | --- |\n", e.run.RunID)
	for _, p := range e.run.Phases {
		review := ""
		if !e.run.NoReview {
			review = fmt.Sprintf(" [review](%s)", markdownLink(filepath.Base(p.ReviewReport)))
		}
		fmt.Fprintf(&body, "| %s | %s | %s | %s | %d | [log](%s)%s |\n",
			strings.ReplaceAll(p.Name, "|", "\\|"), p.Status, p.ImplementCommit, p.ReviewCommit, p.DurationMS,
			markdownLink(filepath.Base(p.ImplementLog)), review)
	}
	if err := fsx.WriteFileAtomic(e.Root, e.run.RunDir+"/summary.md", []byte(body.String())); err != nil {
		fmt.Fprintf(e.Progress, "warning: write run summary: %v\n", err)
	}
}

func (e *execution) fail(phase *model.PhaseRunRecord, err error) error {
	phase.Status = model.PhaseFailed
	if model.ExitCodeOf(err) == model.ExitDrift {
		phase.Status = model.PhaseBlocked
	}
	phase.Error = err.Error()
	saveErr := e.save()
	e.summary()
	return &model.ExitError{Code: model.ExitDrift, Field: phase.Name, Err: errors.Join(err, saveErr)}
}
