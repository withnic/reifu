package syncer

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/model"
)

type skillAction struct {
	source, target string
	remove         bool
}

type skillPlan struct {
	reports  []model.LinkReport
	actions  []skillAction
	managed  map[string][]string
	warnings []string
	issues   bool
}

func skillOK(mode model.LinkMode, report model.LinkReport) bool {
	if mode == model.LinkModeCopy {
		return report.Status == model.LinkStatusCopy
	}
	return report.Status == model.LinkStatusOK
}

// InspectSkills checks skills without changing the project.
func InspectSkills(root string, cfg model.ReifuConfig) ([]model.LinkReport, bool, error) {
	state, err := readState(root)
	if err != nil {
		return nil, false, err
	}
	plan, err := planSkills(root, cfg, state, Options{}, true, false)
	return plan.reports, plan.issues, err
}

// SyncSkillLinks updates only skill links and, when track is true, an existing
// state snapshot.
// The caller holds the project lock when applying changes.
func SyncSkillLinks(root string, cfg model.ReifuConfig, options Options, track bool) (Result, error) {
	var result Result
	var state *model.ReifuState
	if track {
		var err error
		state, err = readState(root)
		if err != nil {
			return result, err
		}
	}
	plan, err := planSkills(root, cfg, state, options, false, false)
	if err != nil {
		return result, err
	}
	plan.describe(&result)
	if err := writeWarnings(options.Warnings, result.Warnings); err != nil || options.DryRun {
		return result, err
	}
	if err := plan.apply(root, cfg, options.Force); err != nil {
		return result, err
	}
	if state != nil && !maps.EqualFunc(state.ManagedSkills, plan.managed, slices.Equal[[]string]) {
		state.ManagedSkills = plan.managed
		state.LastSyncedAt = time.Now().UTC()
		data, err := encodeState(*state)
		if err != nil {
			return result, err
		}
		if err := fsx.WriteFileAtomic(root, StatePath, data); err != nil {
			return result, err
		}
	}
	return result, nil
}

func planSkills(root string, cfg model.ReifuConfig, state *model.ReifuState, options Options, inspect, respectCreate bool) (skillPlan, error) {
	var plan skillPlan
	if state != nil {
		plan.managed = maps.Clone(state.ManagedSkills)
	}
	targets := cfg.EffectiveSkillsTargets(cfg.Agents.Targets)
	if !cfg.SkillsEnabled() || len(targets) == 0 {
		return plan, nil
	}
	source := cfg.EffectiveSkillsSource()
	names, err := fsx.DiscoverSkills(root, source)
	if err != nil || len(names) == 0 && len(plan.managed) == 0 {
		return plan, err
	}
	var activeDirs []string
	for _, dir := range targets {
		dir = path.Clean(strings.ReplaceAll(dir, "\\", "/"))
		previous := plan.managed[dir]
		if len(names) == 0 && len(previous) == 0 {
			continue
		}
		if respectCreate && !options.Create && !cfg.Agents.CreateMissing && path.Dir(dir) != "." {
			parent := path.Dir(dir)
			if err := fsx.ValidateWithinRoot(root, parent); err != nil {
				return plan, err
			}
			info, err := os.Stat(filepath.Join(root, filepath.FromSlash(parent)))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return plan, fmt.Errorf("stat %s: %w", parent, err)
			}
			if !info.IsDir() {
				return plan, fmt.Errorf("%s is not a directory", parent)
			}
		}
		activeDirs = append(activeDirs, dir)
		next := slices.Clone(names)
		for _, name := range names {
			target := path.Join(dir, name)
			if err := plan.addSkill(root, path.Join(strings.ReplaceAll(source, "\\", "/"), name), target, cfg.Links.Mode, options.Force, inspect); err != nil {
				return plan, err
			}
		}
		for _, name := range previous {
			if slices.Contains(names, name) {
				continue
			}
			if name == "" || strings.HasPrefix(name, ".") || strings.Contains(name, "..") ||
				strings.ContainsAny(name, "/\\:") || strings.ContainsFunc(name, unicode.IsControl) ||
				strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
				return plan, fmt.Errorf("invalid managed skill name %q in %s", name, StatePath)
			}
			target := path.Join(dir, name)
			// On case-insensitive filesystems a case-only rename can refer
			// to the same destination entry as the newly discovered name.
			i := slices.IndexFunc(names, func(current string) bool { return strings.EqualFold(current, name) })
			if i >= 0 {
				alias, err := fsx.SameSkillEntry(root, target, path.Join(dir, names[i]))
				if err != nil {
					return plan, err
				}
				if alias {
					continue
				}
			}
			if slices.Contains(next, name) {
				continue
			}
			retained, err := plan.addCleanup(root, target, options.Force, inspect)
			if err != nil {
				return plan, err
			}
			if retained {
				next = append(next, name)
			}
		}
		if len(next) == 0 {
			delete(plan.managed, dir)
		} else {
			if plan.managed == nil {
				plan.managed = make(map[string][]string)
			}
			slices.Sort(next)
			plan.managed[dir] = next
		}
	}
	if !inspect && len(activeDirs) != 0 {
		if err := fsx.ValidateSkillTargetDirs(root, source, activeDirs); err != nil {
			return plan, err
		}
		for _, action := range plan.actions {
			if action.remove {
				if err := fsx.ValidateDirLinkRemoval(root, action.target, source); err != nil {
					return plan, err
				}
			}
		}
	}
	if len(plan.managed) == 0 {
		plan.managed = nil
	}
	return plan, nil
}

func (p *skillPlan) addSkill(root, source, target string, mode model.LinkMode, force, inspect bool) error {
	var report model.LinkReport
	var err error
	if inspect {
		report, err = fsx.CheckDirLink(root, source, target)
	} else {
		report, err = fsx.CheckDirLinkForUpdate(root, source, target, mode)
	}
	if err != nil && !errors.Is(err, fsx.ErrDirLinkDestination) {
		return err
	}
	p.reports = append(p.reports, report)
	ok := skillOK(mode, report)
	p.issues = p.issues || !ok
	if inspect || ok {
		return nil
	}
	if report.Status != model.LinkStatusMissing && !force {
		return fmt.Errorf("directory link destination %q: %w (use --force)", target, errors.Join(os.ErrExist, err))
	}
	if mode == model.LinkModeCopy {
		if err := fsx.ValidateSkillCopy(root, source); err != nil {
			return err
		}
	}
	p.actions = append(p.actions, skillAction{source: source, target: target})
	return nil
}

func (p *skillPlan) addCleanup(root, target string, force, inspect bool) (bool, error) {
	if inspect {
		report, err := fsx.CheckRemovedSkill(root, target)
		if err != nil && !errors.Is(err, fsx.ErrDirLinkDestination) {
			return false, err
		}
		if report.Status != model.LinkStatusMissing {
			p.reports = append(p.reports, report)
			p.issues = true
		}
		return false, nil
	}
	exists, linked, err := fsx.InspectDirEntry(root, target)
	if err != nil || !exists {
		return false, err
	}
	if !linked && !force {
		p.warnings = append(p.warnings, fmt.Sprintf("warning: retaining removed skill %q (use --force to remove it)", target))
		return true, nil
	}
	p.actions = append(p.actions, skillAction{target: target, remove: true})
	return false, nil
}

func (p skillPlan) describe(result *Result) {
	for _, action := range p.actions {
		result.ChangedPaths = append(result.ChangedPaths, action.target)
	}
	result.Warnings = append(result.Warnings, p.warnings...)
}

func (p skillPlan) apply(root string, cfg model.ReifuConfig, force bool) error {
	for _, action := range p.actions {
		if action.remove {
			if err := fsx.RemoveDirLink(root, action.target, cfg.EffectiveSkillsSource()); err != nil {
				return err
			}
		} else {
			if err := fsx.ValidateSkillDestinations(root, cfg.EffectiveSkillsSource(), []string{action.target}); err != nil {
				return err
			}
			if _, err := fsx.EnsureDirLink(root, action.source, action.target, cfg.Links.Mode, force); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeWarnings(out io.Writer, warnings []string) error {
	if out != nil {
		for _, warning := range warnings {
			if _, err := fmt.Fprintln(out, warning); err != nil {
				return fmt.Errorf("write sync warning: %w", err)
			}
		}
	}
	return nil
}
