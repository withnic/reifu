package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/sysenv"
)

func markdownLink(name string) string {
	return url.PathEscape(name)
}

func resumeStep(p model.PhaseRunRecord) string {
	if p.ResumeStep != "" {
		return p.ResumeStep
	}
	switch p.Status {
	case model.PhaseImplemented, model.PhaseReviewing:
		return reviewStep
	default:
		return implementStep
	}
}

func (e *execution) executePhase(ctx context.Context, i int, p *model.PhaseRunRecord) error {
	step := resumeStep(*p)
	switch step {
	case implementStep, implementCommitStep, reviewStep, reviewCommitStep:
	default:
		return fmt.Errorf("invalid resume step %q for %q", step, p.Name)
	}
	start := time.Now()
	event := e.phaseEvent(model.EventPhaseStarted, i, p)
	event.Step = step
	e.emit(event)
	if err := e.runPhase(ctx, i, p, step, start); err != nil {
		p.DurationMS += time.Since(start).Milliseconds()
		return e.fail(i, p, err)
	}
	p.DurationMS += time.Since(start).Milliseconds()
	p.Status, p.ResumeStep, p.Error = model.PhaseCompleted, "", ""
	if err := e.save(); err != nil {
		return err
	}
	event = e.phaseEvent(model.EventPhaseCompleted, i, p)
	event.Status, event.ElapsedMS = p.Status, p.DurationMS
	e.emit(event)
	return nil
}

func (e *execution) transition(p *model.PhaseRunRecord, status model.PhaseStatus, step string) error {
	p.Status, p.ResumeStep, p.Error = status, step, ""
	return e.save()
}

func (e *execution) runPhase(ctx context.Context, i int, p *model.PhaseRunRecord, step string, started time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if step == implementStep {
		p.ImplementAgent = e.implementer.Name
		if e.gitRepo && p.BaseCommit == "" {
			head, err := currentHead(ctx, e.Root)
			if err != nil {
				return err
			}
			p.BaseCommit = head
		}
		if err := e.transition(p, model.PhaseImplementing, implementStep); err != nil {
			return err
		}
		if err := e.implement(ctx, i, p); err != nil {
			return err
		}
		step = implementCommitStep
		if err := e.beforeCommit(ctx, p, model.PhaseImplementing, step); err != nil {
			return err
		}
	}
	if step == implementCommitStep {
		if !e.run.NoCommit {
			sha, err := e.commit(ctx, p, "feat(phase): complete "+p.Name)
			if err != nil {
				return err
			}
			if sha == "" {
				sha, err = e.headIfAdvanced(ctx, p.BaseCommit)
				if err != nil {
					return err
				}
			}
			p.ImplementCommit = sha
			p.CommitBase = nil
		}
		e.banner(i, p, implementStep, e.implementer, "completed", p.ImplementCommit, p.ImplementLog, time.Since(started))
		step = reviewStep
		if err := e.transition(p, model.PhaseImplemented, step); err != nil {
			return err
		}
		e.stepCompleted(i, p, implementStep, e.implementer.Name, p.ImplementCommit, p.ImplementLog, time.Since(started))
	}
	if e.run.NoReview {
		return nil
	}
	reviewStarted := time.Now()
	if step == reviewStep {
		if e.gitRepo && p.ReviewBase == "" {
			head, err := currentHead(ctx, e.Root)
			if err != nil {
				return err
			}
			p.ReviewBase = head
		}
		if err := e.transition(p, model.PhaseReviewing, reviewStep); err != nil {
			return err
		}
		if err := e.review(ctx, i, p); err != nil {
			return err
		}
		if err := e.beforeCommit(ctx, p, model.PhaseReviewing, reviewCommitStep); err != nil {
			return err
		}
	}
	if !e.run.NoCommit {
		sha, err := e.commit(ctx, p, "fix(review): address review for "+p.Name)
		if err != nil {
			return err
		}
		if sha == "" {
			sha, err = e.headIfAdvanced(ctx, p.ReviewBase)
			if err != nil {
				return err
			}
		}
		p.ReviewCommit = sha
		p.CommitBase = nil
	}
	e.banner(i, p, reviewStep, e.reviewer, "completed", p.ReviewCommit, p.ReviewReport, time.Since(reviewStarted))
	e.stepCompleted(i, p, reviewStep, e.reviewer.Name, p.ReviewCommit, p.ReviewReport, time.Since(reviewStarted))
	return nil
}

func (e *execution) stepCompleted(i int, p *model.PhaseRunRecord, step, agent, sha, path string, elapsed time.Duration) {
	if sha != "" {
		event := e.phaseEvent(model.EventCommit, i, p)
		event.Step, event.Commit = step, sha
		e.emit(event)
	}
	event := e.phaseEvent(model.EventStepCompleted, i, p)
	event.Step, event.Agent, event.Commit, event.Path, event.ElapsedMS = step, agent, sha, path, elapsed.Milliseconds()
	e.emit(event)
}

func (e *execution) beforeCommit(ctx context.Context, p *model.PhaseRunRecord, status model.PhaseStatus, step string) error {
	if !e.run.NoCommit {
		head, err := currentHead(ctx, e.Root)
		if err != nil {
			return err
		}
		p.CommitBase = &head
	}
	return e.transition(p, status, step)
}

func (e *execution) commit(ctx context.Context, p *model.PhaseRunRecord, message string) (string, error) {
	if p.CommitBase != nil {
		head, err := currentHead(ctx, e.Root)
		if err != nil {
			return "", err
		}
		if head != *p.CommitBase {
			// A prior command may have committed before its result was persisted.
			// Only reuse HEAD when its parent and subject match this checkpoint.
			if head == "" {
				return "", fmt.Errorf("Git HEAD disappeared while committing %q", p.Name)
			}
			details, err := git(ctx, e.Root, "show", "-s", "--format=%P%x00%s", "HEAD")
			if err != nil {
				return "", err
			}
			if details != *p.CommitBase+"\x00"+message {
				return "", fmt.Errorf("Git HEAD changed since the commit checkpoint for %q", p.Name)
			}
			return HeadShortSHA(ctx, e.Root)
		}
	}
	return CommitAll(ctx, e.Root, message)
}

const emptyGitTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func (e *execution) headIfAdvanced(ctx context.Context, base string) (string, error) {
	if !e.gitRepo {
		return "", nil
	}
	head, err := currentHead(ctx, e.Root)
	if err != nil || head == "" || head == base {
		return "", err
	}
	return HeadShortSHA(ctx, e.Root)
}

func (e *execution) stat(ctx context.Context, base string) (string, error) {
	if !e.gitRepo {
		return "", nil
	}
	head, err := currentHead(ctx, e.Root)
	if err != nil {
		return "", err
	}
	if base != "" {
		return worktreeStat(ctx, e.Root, base)
	}
	if head != "" {
		return worktreeStat(ctx, e.Root, emptyGitTree)
	}
	return worktreeStat(ctx, e.Root, "")
}
func (e *execution) implement(ctx context.Context, i int, p *model.PhaseRunRecord) error {
	prompt, err := readText(e.Root, p.File)
	if err != nil {
		return err
	}
	path, err := projectPath(e.Root, p.ImplementLog)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("append log %q: target must be a regular file", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	log, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(log, "[%s] %s (%s)\n", time.Now().UTC().Format(time.RFC3339), p.File, e.implementer.Name)
	var hasOutput bool
	if err == nil {
		hasOutput, err = e.agent(ctx, i, p, implementStep, e.implementer, e.implementPath, p.ImplementLog, prompt, log)
	}
	if err := errors.Join(checkBlocked(e.Root), err, log.Close()); err != nil {
		return err
	}
	p.ImplementDiff, err = e.stat(ctx, p.BaseCommit)
	if err != nil {
		return err
	}
	if e.gitRepo && strings.TrimSpace(p.ImplementDiff) == "" && !hasOutput {
		advanced, headErr := e.headIfAdvanced(ctx, p.BaseCommit)
		if headErr != nil {
			return headErr
		}
		if advanced == "" {
			return fmt.Errorf("%s with %q in %q: agent exited 0 with no repository changes and no output", implementStep, e.implementer.Name, e.Root)
		}
	}
	return nil
}

func (e *execution) review(ctx context.Context, i int, p *model.PhaseRunRecord) error {
	prompt, err := readText(e.Root, reviewPrompt)
	if err != nil {
		return err
	}
	metadata := fmt.Sprintf("# Phase %s\n\nFile: %s\n\nImplementer: %s\n\nImplementation commit: %s\n\n## Implementation diff stat\n\n%s\n",
		p.Name, p.File, firstSet(p.ImplementAgent, e.run.Implementer), firstSet(p.ImplementCommit, "(none)"), p.ImplementDiff)
	input := append([]byte(metadata+"\n---\n\n"), prompt...)
	var output bytes.Buffer
	_, agentErr := e.agent(ctx, i, p, reviewStep, e.reviewer, e.reviewPath, p.ReviewReport, input, &output)
	blockedErr := checkBlocked(e.Root)
	// A step timeout does not cancel the parent context. On interruption preserve
	// the output even if Git can no longer provide a diff.
	stat, statErr := e.stat(ctx, p.ReviewBase)
	var report strings.Builder
	fmt.Fprintf(&report, "%s\n## Reviewer output (%s)\n\n%s\n\n## Review diff stat\n\n%s\n",
		metadata, e.reviewer.Name, output.String(), stat)
	if err := errors.Join(blockedErr, agentErr, statErr); err != nil {
		fmt.Fprintf(&report, "\n## Step error\n\n%s\n", err)
	}
	saveErr := fsx.WriteFileAtomic(e.Root, p.ReviewReport, []byte(report.String()))
	return errors.Join(blockedErr, agentErr, statErr, saveErr)
}

func (e *execution) banner(i int, p *model.PhaseRunRecord, step string, a AgentCommand, event, sha, path string, elapsed time.Duration) {
	fmt.Fprintf(e.Progress, "[%s] [%d/%d] %s %s (%s: %s, commit %s, elapsed %s) %s\n",
		time.Now().Format("15:04:05"), i+1, len(e.run.Phases), event, p.Name, step, a.Name, firstSet(sha, "-"),
		elapsed.Round(time.Millisecond), path)
}

func (e *execution) agent(ctx context.Context, i int, p *model.PhaseRunRecord, step string, a AgentCommand, executable, path string, prompt []byte, log io.Writer) (bool, error) {
	prompt = bytes.TrimPrefix(prompt, []byte{0xef, 0xbb, 0xbf})
	var cmdArgs []string
	var stdin io.Reader
	if usesPromptArg(a.Argv) {
		var err error
		cmdArgs, err = e.buildPromptArgs(executable, a.Argv[1:], p.Name, step, prompt)
		if err != nil {
			return false, fmt.Errorf("%s with %q in %q: %w", step, a.Name, e.Root, err)
		}
	} else {
		cmdArgs = a.Argv[1:]
		stdin = bytes.NewReader(prompt)
	}
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}
	event := e.phaseEvent(model.EventStepStarted, i, p)
	event.Step, event.Agent, event.Path = step, a.Name, path
	if a.IsCustom {
		event.Warning = fmt.Sprintf("executing custom agent %q: %q", a.Name, a.Argv)
		fmt.Fprintf(e.Progress, "warning: %s\n", event.Warning)
	}
	e.banner(i, p, step, a, "starting", "", path, 0)
	e.emit(event)
	start := time.Now()
	stream := &agentStream{out: log, last: start, first: true}
	if e.EventSink == nil {
		stream.out = io.MultiWriter(log, e.Progress)
	} else {
		stream.onText = func(text string) {
			event := e.phaseEvent(model.EventLog, i, p)
			event.Step, event.Agent, event.Text = step, a.Name, text
			e.emit(event)
		}
	}
	done := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		timer := time.NewTimer(e.HeartbeatInterval)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-timer.C:
				stream.mu.Lock()
				now := time.Now()
				idle := now.Sub(stream.last)
				if idle >= e.HeartbeatInterval {
					if e.EventSink == nil {
						fmt.Fprintf(e.Progress, "[%s]   ... running [%d/%d] %s (%s: %s, elapsed %s)\n",
							now.Format("15:04:05"), i+1, len(e.run.Phases), p.Name, step, a.Name, now.Sub(start).Round(time.Millisecond))
					} else {
						event := e.phaseEvent(model.EventHeartbeat, i, p)
						event.Step, event.Agent, event.ElapsedMS = step, a.Name, now.Sub(start).Milliseconds()
						e.emit(event)
					}
					stream.last = now
					idle = 0
				}
				stream.mu.Unlock()
				timer.Reset(e.HeartbeatInterval - idle)
			}
		}
	}()
	cmd := exec.CommandContext(ctx, executable, cmdArgs...)
	cmd.Dir = e.Root
	cmd.Stdin = stdin
	var parser *agyOutputParser
	if agyMode := detectAgyOutputMode(a); agyMode != agyOutputText {
		parser = newAgyOutputParser(agyMode, step, a.Name, stream)
		cmd.Stdout = parser
		cmd.Stderr = stream
	} else {
		cmd.Stdout, cmd.Stderr = stream, stream
	}
	cmd.WaitDelay = 2 * time.Second
	sysenv.KillTreeOnCancel(cmd)
	runErr := cmd.Run()
	close(done)
	<-heartbeatDone
	var parseErr, recordErr error
	if parser != nil {
		outcome := parser.finish()
		recordErr = e.recordPhaseCommands(p, step, outcome.Commands)
		if runErr == nil && ctx.Err() == nil {
			parseErr = outcome.Err
		}
	}
	flushErr := stream.flush()
	hasOutput := stream.hasOutput()
	if err := errors.Join(runErr, ctx.Err(), parseErr, recordErr, flushErr); err != nil {
		return hasOutput, fmt.Errorf("%s with %q in %q: %w", step, a.Name, e.Root, err)
	}
	return hasOutput, nil
}

func (e *execution) recordPhaseCommands(p *model.PhaseRunRecord, step string, records []model.CommandRecord) error {
	rel := e.run.RunDir + "/" + p.Name + "_commands.json"
	var merged []model.CommandRecord
	if existing, err := readText(e.Root, rel); err == nil {
		var prev model.PhaseCommandsLog
		if json.Unmarshal(existing, &prev) == nil {
			for _, rec := range prev.Commands {
				if rec.Step != step {
					merged = append(merged, rec)
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read command log %q: %w", rel, err)
	}
	merged = append(merged, records...)
	var denied []string
	for _, rec := range merged {
		if rec.Status == model.CommandDenied && rec.DeniedBy == "headless_auto_deny" {
			label := firstSet(rec.Command, rec.Tool)
			if label != "" && !slices.Contains(denied, label) {
				denied = append(denied, label)
			}
		}
	}
	p.DeniedCommands = denied
	if len(merged) == 0 && p.CommandsLog == "" {
		return nil
	}
	if merged == nil {
		merged = []model.CommandRecord{}
	}
	doc := model.PhaseCommandsLog{
		Version:  1,
		Phase:    p.Name,
		Commands: merged,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encode command log %q: %w", rel, err)
	}
	if err := fsx.WriteFileAtomic(e.Root, rel, buf.Bytes()); err != nil {
		return fmt.Errorf("write command log %q: %w", rel, err)
	}
	p.CommandsLog = rel
	return nil
}

func (e *execution) buildPromptArgs(executable string, templateArgs []string, phaseName, step string, prompt []byte) ([]string, error) {
	if bytes.IndexByte(prompt, 0) >= 0 {
		return nil, fmt.Errorf("prompt contains NUL byte and cannot be passed as an argument")
	}
	normalized := bytes.ReplaceAll(prompt, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	args := expandPromptArgs(templateArgs, string(normalized))
	if commandLineUTF16Len(executable, args) <= maxInlineCommandUTF16Len {
		return args, nil
	}
	promptRel := fmt.Sprintf("%s/%s_%s_prompt.md", e.run.RunDir, phaseName, step)
	if err := fsx.WriteFileAtomic(e.Root, promptRel, normalized); err != nil {
		return nil, fmt.Errorf("write spilled prompt %q: %w", promptRel, err)
	}
	pointer := fmt.Sprintf("Read the full instructions from %s and follow them completely.", promptRel)
	args = expandPromptArgs(templateArgs, pointer)
	if n := commandLineUTF16Len(executable, args); n > maxWindowsCommandUTF16Len {
		return nil, fmt.Errorf("command line length (%d UTF-16 code units) exceeds the %d-unit Windows limit even after spilling prompt to %q", n, maxWindowsCommandUTF16Len, promptRel)
	}
	return args, nil
}

// agentStream normalizes text across pipe reads, including split UTF-8
// sequences and CRLF pairs. The same writer receives both child streams.
type agentStream struct {
	mu          sync.Mutex
	out         io.Writer
	last        time.Time
	pending     []byte
	first       bool
	hasNonSpace bool
	onText      func(string)
}

func (s *agentStream) hasOutput() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasNonSpace
}

func (s *agentStream) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = time.Now()
	s.pending = append(s.pending, data...)
	if err := s.writeText(false); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (s *agentStream) flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeText(true)
}

func (s *agentStream) writeText(final bool) error {
	remaining := s.pending
	var text strings.Builder
	for len(remaining) > 0 {
		if !final && (!utf8.FullRune(remaining) || (remaining[0] == '\r' && len(remaining) == 1)) {
			break
		}
		r, n := utf8.DecodeRune(remaining)
		remaining = remaining[n:]
		if s.first {
			s.first = false
			if r == '\ufeff' {
				continue
			}
		}
		if r == '\r' {
			r = '\n'
			if len(remaining) > 0 && remaining[0] == '\n' {
				remaining = remaining[1:]
			}
		}
		text.WriteRune(r)
	}
	s.pending = append(s.pending[:0], remaining...)
	if text.Len() == 0 {
		return nil
	}
	outText := text.String()
	if strings.TrimSpace(outText) != "" {
		s.hasNonSpace = true
	}
	_, err := io.WriteString(s.out, outText)
	if s.onText != nil {
		s.onText(outText)
	}
	return err
}
