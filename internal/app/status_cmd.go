package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/runner"
	"github.com/withnic/reifu/internal/syncer"
	"github.com/withnic/reifu/internal/sysenv"
)

func (c CLI) status(args []string) error {
	f := c.flags("status", "[dir]")
	quiet := f.Bool("quiet", false, "signal issues with exit code 2")
	asJSON := f.Bool("json", false, "emit a JSON status report")
	root, err := c.projectRoot(f, "status", args)
	if err != nil {
		return err
	}
	env, err := sysenv.Detect(root)
	if err != nil {
		return err
	}
	cfg, err := projectConfig(root, !*asJSON || *quiet)
	if err != nil {
		return err
	}
	// The JSON API can inspect runner information before reifu is initialized.
	linked := []model.LinkReport{}
	state := syncer.Result{StateMissing: true, Targets: map[model.AgentName]model.TargetReport{}}
	if cfg.Version != 0 {
		linked, err = inspectLinks(root, cfg.Links)
		if err != nil {
			return err
		}
		state, err = syncer.Inspect(root)
		if err != nil {
			return err
		}
	}
	report := model.StatusReport{
		Root: root, Env: string(env), Links: linked, Skills: state.Skills, Targets: state.Targets,
		StateMissing: state.StateMissing, HasIssues: state.StateMissing || state.HasSkillIssues,
	}
	for _, link := range report.Links {
		report.HasIssues = report.HasIssues || !linkOK(cfg.Links.Mode, link)
	}
	for _, target := range report.Targets {
		report.HasIssues = report.HasIssues || (target.Status != model.StatusSkipped && target.Status != model.StatusInSync)
	}
	if *asJSON {
		runnerStatus := runner.InspectStatus(context.Background(), root, cfg)
		report.Runner = &runnerStatus
		if err := writeJSON(c.Stdout, report); err != nil {
			return err
		}
	} else if !*quiet {
		if err := c.printStatus(report); err != nil {
			return err
		}
	}
	if *quiet && report.HasIssues {
		return &model.ExitError{Code: model.ExitDrift}
	}
	return nil
}

func (c CLI) printStatus(report model.StatusReport) error {
	if _, err := fmt.Fprintf(c.Stdout, "%s (%s)\n", report.Root, report.Env); err != nil {
		return err
	}
	if report.StateMissing {
		if _, err := fmt.Fprintln(c.Stdout, "STATE_MISSING: run reifu sync to create the baseline."); err != nil {
			return err
		}
	}
	for _, link := range report.Links {
		if err := c.printLink(link); err != nil {
			return err
		}
	}
	for _, skill := range report.Skills {
		if err := c.printLink(skill); err != nil {
			return err
		}
	}
	agents := make([]model.AgentName, 0, len(report.Targets))
	for agent := range report.Targets {
		agents = append(agents, agent)
	}
	slices.Sort(agents)
	for _, agent := range agents {
		target := report.Targets[agent]
		if _, err := fmt.Fprintf(c.Stdout, "%s  %s  %s\n", agent, target.Status, target.Path); err != nil {
			return err
		}
	}
	return nil
}
