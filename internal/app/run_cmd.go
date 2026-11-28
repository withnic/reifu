package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/runner"
	"github.com/withnic/reifu/internal/syncer"
	"github.com/withnic/reifu/internal/sysenv"
)

type notifyFlag model.NotifyMode

func (n *notifyFlag) IsBoolFlag() bool { return true }

func (n *notifyFlag) String() string { return string(*n) }

func (n *notifyFlag) Set(value string) error {
	switch value {
	case "true":
		value = string(model.NotifyEnd)
	case "false":
		value = string(model.NotifyOff)
	case "":
		return fmt.Errorf("notification mode must be off, end or phase")
	}
	mode := model.NotifyMode(value)
	if err := mode.Validate(); err != nil {
		return err
	}
	*n = notifyFlag(mode)
	return nil
}

func (c CLI) runPhases(args []string) (runErr error) {
	f := c.flags("run", "[dir]")
	opts := runner.Options{Progress: c.Stderr, Diagnostics: c.Stderr}
	f.StringVar(&opts.Implementer, "implementer", "", "implementation agent (defaults to runner.implementer)")
	f.StringVar(&opts.Reviewer, "reviewer", "", "review agent (defaults to runner.reviewer or the implementer)")
	f.StringVar(&opts.Phase, "phase", "", "execute one named phase")
	f.StringVar(&opts.From, "from", "", "execute from the named phase onward")
	f.BoolVar(&opts.NoReview, "no-review", false, "skip review")
	f.BoolVar(&opts.NoCommit, "no-commit", false, "skip automatic Git commits")
	f.BoolVar(&opts.AllowDirty, "allow-dirty", false, "allow starting with uncommitted changes")
	f.BoolVar(&opts.Resume, "resume", false, "resume the latest run from its checkpoint")
	f.BoolVar(&opts.SkipCompleted, "skip-completed", false, "start a new run and carry over completed phases from the latest checkpoint")
	f.DurationVar(&opts.Timeout, "timeout", runner.DefaultTimeout, "timeout per agent step (0 for unlimited)")
	f.BoolVar(&opts.DryRun, "dry-run", false, "show the plan without running agents or writing files")
	f.Var((*notifyFlag)(&opts.Notify), "notify", "notify on completion or stop (off, end, phase; bare flag means end)")
	asJSON := f.Bool("json", false, "emit the plan or final run state as JSON")
	streamJSON := f.Bool("stream-json", false, "emit realtime run events as NDJSON")
	cancelOnStdinEOF := f.Bool("cancel-on-stdin-eof", false, "cancel the run when stdin closes")
	root, err := c.projectRoot(f, "run", args)
	if err != nil {
		return err
	}
	if *streamJSON && *asJSON {
		return errors.New("--stream-json and --json are mutually exclusive")
	}
	var result runner.Result
	if *streamJSON {
		encoder := json.NewEncoder(c.Stdout)
		encoder.SetEscapeHTML(false)
		var mu sync.Mutex
		var terminal bool
		var encodeErr error
		opts.EventSink = func(event model.RunEvent) {
			mu.Lock()
			defer mu.Unlock()
			switch event.Type {
			case model.EventRunPlan, model.EventRunCompleted, model.EventRunFailed:
				terminal = true
			}
			if encodeErr == nil {
				if err := encoder.Encode(event); err != nil {
					encodeErr = fmt.Errorf("write run event: %w", err)
				}
			}
		}
		opts.Progress = io.Discard
		opts.Notifier = func(n sysenv.Notification) { sysenv.Notify(n, c.Stderr) }
		defer func() {
			if runErr != nil && !terminal {
				status := model.PhaseFailed
				if model.ExitCodeOf(runErr) == model.ExitDrift {
					status = model.PhaseBlocked
				}
				event := model.RunEvent{
					SchemaVersion: 1, Type: model.EventRunFailed, Timestamp: time.Now().UTC(),
					Status: status, Error: runErr.Error(), State: result.State,
				}
				if result.State != nil {
					event.RunID = result.State.RunID
				}
				opts.EventSink(event)
			}
			runErr = errors.Join(runErr, encodeErr)
		}()
	}
	if err := c.rejectHomeDir(root); err != nil {
		return err
	}
	opts.Root = root
	data, err := readRegular(root, syncer.MasterPath)
	if err != nil {
		return err
	}
	if data != nil {
		if err := jsonc.Unmarshal(data, &opts.Config); err != nil {
			return fmt.Errorf("parse %s: %w", syncer.MasterPath, err)
		}
		opts.Config.ApplyDefaults()
		if err := opts.Config.Validate(); err != nil {
			return fmt.Errorf("validate %s: %w", syncer.MasterPath, err)
		}
	}
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()
	if *cancelOnStdinEOF && !opts.DryRun && c.Stdin != nil {
		go func() {
			var buf [256]byte
			for ctx.Err() == nil {
				if _, err := c.Stdin.Read(buf[:]); err != nil {
					cancel()
					return
				}
			}
		}()
	}
	result, err = runner.Run(ctx, opts)
	switch {
	case *streamJSON:
		return err
	case result.Plan != nil && *asJSON:
		return errors.Join(err, writeJSON(c.Stdout, result.Plan))
	case result.State != nil && *asJSON:
		return errors.Join(err, writeJSON(c.Stdout, result.State))
	case result.Plan != nil:
		plan := result.Plan
		if plan.Implementer.Name != "" {
			if _, err := fmt.Fprintf(c.Stdout, "Implementer %s: %q\n", plan.Implementer.Name, plan.Implementer.Argv); err != nil {
				return err
			}
		}
		if plan.Reviewer != nil {
			if _, err := fmt.Fprintf(c.Stdout, "Reviewer %s: %q\n", plan.Reviewer.Name, plan.Reviewer.Argv); err != nil {
				return err
			}
		}
		return c.printRunState(plan.State)
	case result.State != nil:
		return errors.Join(err, c.printRunState(*result.State))
	}
	return err
}
