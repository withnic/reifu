package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/runner"
)

func (c CLI) logs(args []string) error {
	f := c.flags("logs", "[dir]")
	list := f.Bool("list", false, "list past runs, newest first")
	runID := f.String("run", "", "select a run by ID (default latest)")
	phase := f.String("phase", "", "select a phase by name")
	review := f.Bool("review", false, "show review reports")
	var follow bool
	f.BoolVar(&follow, "f", false, "follow the implementation log")
	f.BoolVar(&follow, "follow", false, "follow the implementation log")
	asJSON := f.Bool("json", false, "emit run history or state as JSON")
	root, err := c.projectRoot(f, "logs", args)
	if err != nil {
		return err
	}
	if err := c.rejectHomeDir(root); err != nil {
		return err
	}
	if follow && *asJSON {
		return errors.New("--follow cannot be combined with --json")
	}
	if follow && (*review || *list) {
		return errors.New("--follow cannot be combined with --review or --list")
	}
	// JSON always describes the whole run, so phase/review selectors would be
	// silently ignored (and an unknown phase would still exit 0).
	if *asJSON && (*phase != "" || *review) {
		return errors.New("--json cannot be combined with --phase or --review")
	}
	if *list && (*runID != "" || *phase != "" || *review) {
		return errors.New("--list cannot be combined with --run, --phase or --review")
	}
	if *runID != "" {
		if err := model.ValidateRunID(*runID); err != nil {
			return err
		}
	}
	if *list {
		return c.listRuns(root, *asJSON)
	}
	run, err := runner.LoadRun(root, *runID)
	if err != nil {
		return err
	}
	switch {
	case *asJSON:
		return writeJSON(c.Stdout, run)
	case follow:
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := runner.FollowLog(ctx, root, run, *phase, 0, c.Stdout)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case *review || *phase != "":
		read := runner.ReadPhaseLog
		if *review {
			read = runner.ReadReviews
		}
		data, err := read(root, run, *phase)
		if err != nil {
			return err
		}
		if len(data) == 0 && *review {
			if run.NoReview {
				_, err = fmt.Fprintln(c.Stdout, "No review reports (run was executed with --no-review).")
			} else {
				_, err = fmt.Fprintln(c.Stdout, "No review reports yet.")
			}
			return err
		}
		_, err = c.Stdout.Write(data)
		return err
	default:
		return c.printRunState(run)
	}
}

func (c CLI) listRuns(root string, asJSON bool) error {
	runs, err := runner.ListRuns(root)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(c.Stdout, runs)
	}
	if len(runs) == 0 {
		_, err := fmt.Fprintln(c.Stdout, "No run history. Start a run with reifu run.")
		return err
	}
	table := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "RUN ID\tSTARTED (UTC)\tSTATUS\tCOMPLETED")
	for _, run := range runs {
		fmt.Fprintf(table, "%s\t%s\t%s\t%d/%d\n", run.RunID, run.StartedAt.UTC().Format(time.RFC3339), run.Status, run.Completed, run.Total)
	}
	return table.Flush()
}

func (c CLI) printRunState(run model.RunState) error {
	summary := run.Summary()
	header := fmt.Sprintf("Run %s: %s (%d/%d completed)", run.RunID, summary.Status, summary.Completed, summary.Total)
	for i, phase := range run.Phases {
		if phase.Status != model.PhaseCompleted {
			header = fmt.Sprintf("Run %s: %s [%d/%d: %s] (%d/%d completed)", run.RunID, summary.Status, i+1, summary.Total, phase.Name, summary.Completed, summary.Total)
			break
		}
	}
	if _, err := fmt.Fprintln(c.Stdout, header); err != nil {
		return err
	}
	table := tabwriter.NewWriter(c.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "PHASE\tSTATUS\tIMPLEMENT COMMIT\tREVIEW COMMIT\tDURATION\tLOG\tREVIEW")
	for _, phase := range run.Phases {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%dms\t%s\t%s\n",
			phase.Name, phase.Status, phase.ImplementCommit, phase.ReviewCommit, phase.DurationMS, phase.ImplementLog, phase.ReviewReport)
	}
	return table.Flush()
}
