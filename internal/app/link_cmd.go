package app

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/model"
	"github.com/withnic/reifu/internal/syncer"
)

const linkSource = "AGENTS.md"

var protectedLinkPaths = []string{
	linkSource, syncer.MasterPath, syncer.StatePath, ".gitignore", ".reifu.lock",
	".claude/settings.json", ".codex/config.toml", ".gemini/settings.json",
}

func projectConfig(root string, required bool) (model.ReifuConfig, error) {
	var cfg model.ReifuConfig
	data, err := readRegular(root, syncer.MasterPath)
	if err != nil {
		return cfg, err
	}
	if data == nil {
		if required {
			return cfg, fmt.Errorf("read %s: %w", syncer.MasterPath, os.ErrNotExist)
		}
		cfg.ApplyDefaults()
		cfg.Links.Targets = []string{"CLAUDE.md", "GEMINI.md"}
		return cfg, nil
	}
	if err := jsonc.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", syncer.MasterPath, err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("validate %s: %w", syncer.MasterPath, err)
	}
	return cfg, nil
}

func (c CLI) link(args []string) error {
	f := c.flags("link", "[dir]")
	mode := f.String("mode", "", "link mode: auto, hard, symlink, copy")
	check := f.Bool("check", false, "check the configured links")
	asJSON := f.Bool("json", false, "emit a JSON link report")
	force := f.Bool("force", false, "replace differing destinations")
	root, err := c.projectRoot(f, "link", args)
	if err != nil {
		return err
	}
	if !*check {
		if err := c.rejectHomeDir(root); err != nil {
			return err
		}
	}
	run := func() error {
		cfg, err := projectConfig(root, false)
		if err != nil {
			return err
		}
		if *mode != "" {
			cfg.Links.Mode = model.LinkMode(*mode)
		}
		links := cfg.Links
		switch links.Mode {
		case model.LinkModeAuto, model.LinkModeHard, model.LinkModeSymlink, model.LinkModeCopy:
		default:
			return fmt.Errorf("invalid link mode %q", links.Mode)
		}
		reports, err := inspectLinks(root, links)
		if err != nil {
			return err
		}
		if *check {
			failed := false
			for _, report := range reports {
				if !*asJSON {
					if err := c.printLink(report); err != nil {
						return err
					}
				}
				failed = failed || !linkOK(links.Mode, report)
			}
			skills, issues, err := syncer.InspectSkills(root, cfg)
			if err != nil {
				return err
			}
			if *asJSON {
				if err := writeJSON(c.Stdout, model.LinkCommandReport{
					Root: root, Mode: links.Mode, Check: true, HasIssues: failed || issues,
					Links: reports, Skills: append([]model.LinkReport{}, skills...),
				}); err != nil {
					return err
				}
			} else {
				for _, report := range skills {
					if err := c.printLink(report); err != nil {
						return err
					}
				}
			}
			if failed || issues {
				return &model.ExitError{Code: model.ExitLinkIntegrity, Err: errors.New("link check failed")}
			}
			return nil
		}
		source, err := readRegular(root, linkSource)
		if err != nil {
			return err
		}
		if source == nil {
			return fmt.Errorf("read %s: %w", linkSource, os.ErrNotExist)
		}
		if err := fsx.ValidateLinkPaths(root, protectedLinkPaths, links.Targets); err != nil {
			return err
		}
		if err := preflightLinks(links, reports, *force); err != nil {
			return err
		}
		if _, err := syncer.SyncSkillLinks(root, cfg, syncer.Options{Force: *force, Warnings: c.Stderr}, true); err != nil {
			return err
		}
		created, err := c.createLinks(root, links, *force, !*asJSON)
		if err != nil || !*asJSON {
			return err
		}
		skills, issues, err := syncer.InspectSkills(root, cfg)
		if err != nil {
			return err
		}
		return writeJSON(c.Stdout, model.LinkCommandReport{
			Root: root, Mode: links.Mode, HasIssues: issues,
			Links: created, Skills: append([]model.LinkReport{}, skills...),
		})
	}
	if *check {
		return run()
	}
	return fsx.WithFileLock(root, run)
}

func inspectLinks(root string, links model.LinksConfig) ([]model.LinkReport, error) {
	reports := make([]model.LinkReport, 0, len(links.Targets))
	for _, target := range links.Targets {
		report, err := fsx.CheckLink(root, linkSource, target)
		if err != nil {
			return nil, err
		}
		if (report.Status == model.LinkStatusOK || report.Status == model.LinkStatusCopy) &&
			links.Mode != model.LinkModeAuto && report.Mode != links.Mode {
			report.Status = model.LinkStatusDifferent
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// linkOK considers an independent copy valid only when copy mode is explicitly configured.
// Under auto/hard/symlink modes, a detached copy (such as right after git clone) fails integrity checks.
func linkOK(mode model.LinkMode, report model.LinkReport) bool {
	if mode == model.LinkModeCopy {
		return report.Status == model.LinkStatusCopy
	}
	return report.Status == model.LinkStatusOK
}

func (c CLI) printLink(report model.LinkReport) error {
	mode := ""
	if report.Mode != "" {
		mode = " (" + string(report.Mode) + ")"
	}
	_, err := fmt.Fprintf(c.Stdout, "%s  %s%s\n", report.Target, report.Status, mode)
	return err
}

func preflightLinks(links model.LinksConfig, reports []model.LinkReport, force bool) error {
	for _, report := range reports {
		if report.Status == model.LinkStatusMissing || force {
			continue
		}
		if linkOK(links.Mode, report) {
			continue
		}
		return fmt.Errorf("link destination %q: %w (use --force)", report.Target, os.ErrExist)
	}
	return nil
}

func (c CLI) createLinks(root string, links model.LinksConfig, force, print bool) ([]model.LinkReport, error) {
	reports := make([]model.LinkReport, 0, len(links.Targets))
	for _, target := range links.Targets {
		parent := path.Dir(strings.ReplaceAll(target, "\\", "/"))
		if parent != "." {
			if err := fsx.EnsureDirWithinRoot(root, parent); err != nil {
				return nil, err
			}
		}
		mode, err := fsx.EnsureLink(root, linkSource, target, links.Mode, force)
		if err != nil {
			return nil, err
		}
		status := model.LinkStatusOK
		if mode == model.LinkModeCopy {
			status = model.LinkStatusCopy
		}
		report := model.LinkReport{Target: target, Status: status, Mode: mode}
		reports = append(reports, report)
		if print {
			if err := c.printLink(report); err != nil {
				return nil, err
			}
		}
	}
	return reports, nil
}
