package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
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
	if err := e.runPhase(ctx, i, p, step, start); err != nil {
		p.DurationMS += time.Since(start).Milliseconds()
		return e.fail(p, err)
	}
	p.DurationMS += time.Since(start).Milliseconds()
	p.Status, p.ResumeStep, p.Error = model.PhaseCompleted, "", ""
	return e.save()
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
	return nil
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
	if err == nil {
		err = e.agent(ctx, i, p, implementStep, e.implementer, e.implementPath, p.ImplementLog, prompt, log)
	}
	if err := errors.Join(checkBlocked(e.Root), err, log.Close()); err != nil {
		return err
	}
	p.ImplementDiff, err = e.stat(ctx, p.BaseCommit)
	return err
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
	agentErr := e.agent(ctx, i, p, reviewStep, e.reviewer, e.reviewPath, p.ReviewReport, input, &output)
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

func (e *execution) agent(ctx context.Context, i int, p *model.PhaseRunRecord, step string, a AgentCommand, executable, path string, prompt []byte, log io.Writer) error {
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}
	if a.IsCustom {
		fmt.Fprintf(e.Progress, "warning: executing custom agent %q: %q\n", a.Name, a.Argv)
	}
	e.banner(i, p, step, a, "starting", "", path, 0)
	start := time.Now()
	stream := &agentStream{out: io.MultiWriter(log, e.Progress), last: start, first: true}
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
					fmt.Fprintf(e.Progress, "[%s]   ... running [%d/%d] %s (%s: %s, elapsed %s)\n",
						now.Format("15:04:05"), i+1, len(e.run.Phases), p.Name, step, a.Name, now.Sub(start).Round(time.Millisecond))
					stream.last = now
					idle = 0
				}
				stream.mu.Unlock()
				timer.Reset(e.HeartbeatInterval - idle)
			}
		}
	}()
	cmd := exec.CommandContext(ctx, executable, a.Argv[1:]...)
	cmd.Dir = e.Root
	cmd.Stdin = bytes.NewReader(bytes.TrimPrefix(prompt, []byte{0xef, 0xbb, 0xbf}))
	cmd.Stdout, cmd.Stderr = stream, stream
	cmd.WaitDelay = 2 * time.Second
	sysenv.KillTreeOnCancel(cmd)
	err := cmd.Run()
	close(done)
	<-heartbeatDone
	if err := errors.Join(err, ctx.Err(), stream.flush()); err != nil {
		return fmt.Errorf("%s with %q in %q: %w", step, a.Name, e.Root, err)
	}
	return nil
}

// agentStream normalizes text across pipe reads, including split UTF-8
// sequences and CRLF pairs. The same writer receives both child streams.
type agentStream struct {
	mu      sync.Mutex
	out     io.Writer
	last    time.Time
	pending []byte
	first   bool
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
	_, err := io.WriteString(s.out, text.String())
	return err
}
