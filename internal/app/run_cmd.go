package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/runner"
	"github.com/withnic/reifu/internal/syncer"
)

func (c CLI) runPhases(args []string) error {
	f := c.flags("run", "[dir]")
	opts := runner.Options{Progress: c.Stderr}
	f.StringVar(&opts.Implementer, "implementer", "", "implementation agent (defaults to runner.implementer)")
	f.StringVar(&opts.Reviewer, "reviewer", "", "review agent (defaults to runner.reviewer or the implementer)")
	f.StringVar(&opts.Phase, "phase", "", "execute one named phase")
	f.StringVar(&opts.From, "from", "", "execute from the named phase onward")
	f.BoolVar(&opts.NoReview, "no-review", false, "skip review")
	f.BoolVar(&opts.NoCommit, "no-commit", false, "skip automatic Git commits")
	f.BoolVar(&opts.AllowDirty, "allow-dirty", false, "allow starting with uncommitted changes")
	f.BoolVar(&opts.Resume, "resume", false, "resume the latest run from its checkpoint")
	f.DurationVar(&opts.Timeout, "timeout", runner.DefaultTimeout, "timeout per agent step (0 for unlimited)")
	f.BoolVar(&opts.DryRun, "dry-run", false, "show the plan without running agents or writing files")
	asJSON := f.Bool("json", false, "emit the plan or final run state as JSON")
	root, err := c.projectRoot(f, "run", args)
	if err != nil {
		return err
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := runner.Run(ctx, opts)
	switch {
	case result.Plan != nil && *asJSON:
		return errors.Join(err, json.NewEncoder(c.Stdout).Encode(result.Plan))
	case result.State != nil && *asJSON:
		return errors.Join(err, json.NewEncoder(c.Stdout).Encode(result.State))
	case result.Plan != nil:
		plan := result.Plan
		if _, err := fmt.Fprintf(c.Stdout, "Implementer %s: %q\n", plan.Implementer.Name, plan.Implementer.Argv); err != nil {
			return err
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
