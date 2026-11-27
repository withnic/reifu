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

func projectLinks(root string, required bool) (model.LinksConfig, error) {
	var cfg model.ReifuConfig
	data, err := readRegular(root, syncer.MasterPath)
	if err != nil {
		return cfg.Links, err
	}
	if data == nil {
		if required {
			return cfg.Links, fmt.Errorf("read %s: %w", syncer.MasterPath, os.ErrNotExist)
		}
		return model.LinksConfig{Mode: model.LinkModeAuto, Targets: []string{"CLAUDE.md", "GEMINI.md"}}, nil
	}
	if err := jsonc.Unmarshal(data, &cfg); err != nil {
		return cfg.Links, fmt.Errorf("parse %s: %w", syncer.MasterPath, err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return cfg.Links, fmt.Errorf("validate %s: %w", syncer.MasterPath, err)
	}
	return cfg.Links, nil
}

func (c CLI) link(args []string) error {
	f := c.flags("link", "[dir]")
	mode := f.String("mode", "", "link mode: auto, hard, symlink, copy")
	check := f.Bool("check", false, "check the configured links")
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
		links, err := projectLinks(root, false)
		if err != nil {
			return err
		}
		if *mode != "" {
			links.Mode = model.LinkMode(*mode)
		}
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
				if err := c.printLink(report); err != nil {
					return err
				}
				failed = failed || !linkOK(links.Mode, report)
			}
			if failed {
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
		return c.createLinks(root, links, *force)
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

func (c CLI) createLinks(root string, links model.LinksConfig, force bool) error {
	for _, target := range links.Targets {
		parent := path.Dir(strings.ReplaceAll(target, "\\", "/"))
		if parent != "." {
			if err := fsx.EnsureDirWithinRoot(root, parent); err != nil {
				return err
			}
		}
		mode, err := fsx.EnsureLink(root, linkSource, target, links.Mode, force)
		if err != nil {
			return err
		}
		status := model.LinkStatusOK
		if mode == model.LinkModeCopy {
			status = model.LinkStatusCopy
		}
		if err := c.printLink(model.LinkReport{Target: target, Status: status, Mode: mode}); err != nil {
			return err
		}
	}
	return nil
}
