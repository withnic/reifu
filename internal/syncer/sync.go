package syncer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/withnic/reifu/internal/compiler"
	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/model"
)

const MasterPath = ".reifu.jsonc"
const StatePath = ".reifu.state.json"
const CodexRulesPath = ".codex/rules/reifu.rules"

var targetPaths = map[model.AgentName]string{
	model.AgentClaude: ".claude/settings.json",
	model.AgentCodex:  ".codex/config.toml",
	model.AgentGemini: ".gemini/settings.json",
}

type Options struct {
	Import, Force, Create, NoHooks, DryRun bool
	TargetOS                               string
	Warnings                               io.Writer
}

type Result struct {
	StateMissing   bool
	Targets        map[model.AgentName]model.TargetReport
	Skills         []model.LinkReport
	HasSkillIssues bool
	ChangedPaths   []string
	Warnings       []string
}

type projectSnapshot struct {
	config     model.ReifuConfig
	master     []byte
	state      *model.ReifuState
	files      map[model.AgentName][]byte
	codexRules []byte
	result     Result
}

type codexRulesChange struct {
	write  bool
	remove bool
	data   []byte
}

// Inspect reports target drift and skill integrity without writing files.
func Inspect(root string) (Result, error) {
	snapshot, err := readProject(root, false, nil)
	if err != nil {
		return snapshot.result, err
	}
	// Like target settings, skills under a missing agent directory are skipped
	// (sync would not create them), so they are not reported as issues.
	plan, err := planSkills(root, snapshot.config, snapshot.state, Options{}, true, true)
	snapshot.result.Skills = plan.reports
	snapshot.result.HasSkillIssues = plan.issues
	return snapshot.result, err
}

// Sync preflights all merges, then writes changed files and the state snapshot
// under the project lock. Dry runs use the same preflight without writes.
func Sync(root string, options Options) (result Result, err error) {
	if options.DryRun {
		return sync(root, options, nil)
	}
	err = fsx.WithFileLock(root, func() error {
		result, err = sync(root, options, nil)
		return err
	})
	return result, err
}

// SyncInit compiles the proposed master during init. The caller holds the
// project lock, except when previewing with DryRun.
func SyncInit(root string, options Options, master []byte) (Result, error) {
	return sync(root, options, master)
}

// PlanNewProject previews compilation for a project directory that does not exist yet.
func PlanNewProject(cfg model.ReifuConfig, options Options, master []byte) (Result, error) {
	result := Result{StateMissing: true, Targets: make(map[model.AgentName]model.TargetReport)}
	for _, agent := range cfg.Agents.Targets {
		result.Targets[agent] = model.TargetReport{Path: targetPaths[agent], Status: model.StatusStateMissing}
	}
	snapshot := projectSnapshot{config: cfg, master: master}
	_, _, _, _, err := snapshot.plan(options, skillPlan{}, &result)
	return result, err
}

func sync(root string, options Options, proposed []byte) (result Result, err error) {
	run := func() error {
		snapshot, err := readProject(root, options.Create, proposed)
		if err != nil {
			return err
		}
		result = snapshot.result
		skills, err := planSkills(root, snapshot.config, snapshot.state, options, false, true)
		if err != nil {
			return err
		}
		result.Skills = skills.reports
		master, targets, rulesChange, state, err := snapshot.plan(options, skills, &result)
		if wErr := writeWarnings(options.Warnings, result.Warnings); wErr != nil {
			return wErr
		}
		if err != nil {
			return err
		}
		if options.DryRun || state == nil {
			return nil
		}
		if err := skills.apply(root, snapshot.config, options.Force); err != nil {
			return err
		}
		// Write the master before distributing any imported rules. Publish the
		// snapshot last so incomplete writes remain detectable on retry.
		if !bytes.Equal(master, snapshot.master) {
			if err := fsx.WriteFileAtomic(root, MasterPath, master); err != nil {
				return err
			}
		}
		for _, agent := range snapshot.config.Agents.Targets {
			if data, changed := targets[agent]; changed {
				target := targetPaths[agent]
				if err := fsx.EnsureDirWithinRoot(root, path.Dir(target)); err != nil {
					return err
				}
				if err := fsx.WriteFileAtomic(root, target, data); err != nil {
					return err
				}
			}
		}
		if rulesChange.write {
			if err := fsx.EnsureDirWithinRoot(root, path.Dir(CodexRulesPath)); err != nil {
				return err
			}
			if err := fsx.WriteFileAtomic(root, CodexRulesPath, rulesChange.data); err != nil {
				return err
			}
		} else if rulesChange.remove {
			if err := fsx.ValidateWithinRoot(root, CodexRulesPath); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(CodexRulesPath))); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove %s: %w", CodexRulesPath, err)
			}
		}
		return fsx.WriteFileAtomic(root, StatePath, state)
	}
	err = run()
	return result, err
}

func readProject(root string, create bool, proposed []byte) (projectSnapshot, error) {
	var snapshot projectSnapshot
	master, err := readOptional(root, MasterPath)
	if err != nil {
		return snapshot, err
	}
	if proposed != nil {
		master = proposed
	}
	if master == nil {
		return snapshot, fmt.Errorf("read %s: %w", MasterPath, os.ErrNotExist)
	}
	snapshot.master = master
	if err := jsonc.Unmarshal(master, &snapshot.config); err != nil {
		return snapshot, fmt.Errorf("parse %s: %w", MasterPath, err)
	}
	snapshot.config.ApplyDefaults()
	if err := snapshot.config.Validate(); err != nil {
		return snapshot, fmt.Errorf("validate %s: %w", MasterPath, err)
	}
	snapshot.state, err = readState(root)
	if err != nil {
		return snapshot, err
	}
	snapshot.result = Result{StateMissing: snapshot.state == nil, Targets: make(map[model.AgentName]model.TargetReport)}
	snapshot.files = make(map[model.AgentName][]byte)
	masterHash := SHA256(master)
	directories := make(map[string]os.FileInfo)
	for _, agent := range snapshot.config.Agents.Targets {
		target := targetPaths[agent]
		dir := path.Dir(target)
		if err := fsx.ValidateWithinRoot(root, dir); err != nil {
			return snapshot, err
		}
		info, err := os.Stat(filepath.Join(root, dir))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return snapshot, fmt.Errorf("stat %s: %w", dir, err)
		}
		if info != nil && !info.IsDir() {
			return snapshot, fmt.Errorf("%s is not a directory", dir)
		}
		if info != nil {
			for other, otherDir := range directories {
				if path.Base(other) == path.Base(target) && os.SameFile(info, otherDir) {
					return snapshot, fmt.Errorf("%s and %s resolve to the same settings path", other, target)
				}
			}
			directories[target] = info
			data, err := readOptional(root, target)
			if err != nil {
				return snapshot, err
			}
			snapshot.files[agent] = data
			if agent == model.AgentCodex {
				rulesData, err := readOptional(root, CodexRulesPath)
				if err != nil {
					return snapshot, err
				}
				snapshot.codexRules = rulesData
			}
		}
		status := Detect(snapshot.state, agent, masterHash, snapshot.files[agent], info != nil, create || snapshot.config.Agents.CreateMissing)
		if agent == model.AgentCodex && (status == model.StatusInSync || status == model.StatusMasterUpdated) && snapshot.state != nil {
			if prev, tracked := snapshot.state.Targets[agent]; tracked && prev.RulesPath != "" {
				rulesChanged := snapshot.codexRules == nil || SHA256(snapshot.codexRules) != prev.RulesFileSHA256
				if rulesChanged {
					if status == model.StatusMasterUpdated {
						status = model.StatusConflict
					} else {
						status = model.StatusDrifted
					}
				}
			}
		}
		snapshot.result.Targets[agent] = model.TargetReport{Path: target, Status: status}
	}
	return snapshot, nil
}

func readState(root string) (*model.ReifuState, error) {
	data, err := readOptional(root, StatePath)
	if err != nil || data == nil {
		return nil, err
	}
	var state model.ReifuState
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", StatePath, err)
	}
	if state.Version != model.CurrentSchemaVersion {
		return nil, fmt.Errorf("unsupported %s version %d", StatePath, state.Version)
	}
	return &state, nil
}

func readOptional(root, target string) ([]byte, error) {
	if err := fsx.ValidateWithinRoot(root, target); err != nil {
		return nil, err
	}
	file := filepath.Join(root, filepath.FromSlash(target))
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", file, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return data, nil
}

func (s projectSnapshot) plan(options Options, skills skillPlan, result *Result) ([]byte, map[model.AgentName][]byte, codexRulesChange, []byte, error) {
	var sources []importSource
	var divergent []string
	for _, agent := range s.config.Agents.Targets {
		report := result.Targets[agent]
		if report.Status == model.StatusDrifted || report.Status == model.StatusConflict {
			divergent = append(divergent, fmt.Sprintf("%s (%s)", report.Path, report.Status))
			sources = append(sources, importSource{agent, report.Path, s.files[agent]})
		}
	}
	if len(divergent) != 0 && !options.Import && !options.Force {
		return nil, nil, codexRulesChange{}, nil, &model.ExitError{Code: model.ExitDrift, Err: fmt.Errorf("%s changed externally; use --import or --force", strings.Join(divergent, ", "))}
	}
	master := s.master
	cfg := s.config
	if options.Import && len(sources) != 0 {
		var importWarnings []string
		var err error
		master, importWarnings, err = importCommands(master, cfg, s.state, sources, options.Force)
		result.Warnings = append(result.Warnings, importWarnings...)
		if err != nil {
			return nil, nil, codexRulesChange{}, nil, err
		}
		if !bytes.Equal(master, s.master) {
			master = normalizedText(master)
			cfg = model.ReifuConfig{}
			if err := jsonc.Unmarshal(master, &cfg); err != nil {
				return nil, nil, codexRulesChange{}, nil, fmt.Errorf("parse imported %s: %w", MasterPath, err)
			}
			cfg.ApplyDefaults()
			if err := cfg.Validate(); err != nil {
				return nil, nil, codexRulesChange{}, nil, fmt.Errorf("validate imported %s: %w", MasterPath, err)
			}
			result.ChangedPaths = append(result.ChangedPaths, MasterPath)
		}
	}

	next := model.ReifuState{
		Version:         model.CurrentSchemaVersion,
		MasterSHA256:    SHA256(master),
		ManagedCommands: slices.Clone(cfg.Permissions.AllowCommands),
		Targets:         make(map[model.AgentName]model.TargetState),
		ManagedSkills:   skills.managed,
	}
	targets := make(map[model.AgentName][]byte)
	var rulesChange codexRulesChange
	for _, agent := range cfg.Agents.Targets {
		report := result.Targets[agent]
		if report.Status == model.StatusSkipped {
			continue
		}
		var previous model.TargetState
		if s.state != nil {
			previous = s.state.Targets[agent]
		}
		command := ""
		if agent != model.AgentCodex {
			command = compiler.ResolveVerifyCommand(cfg.Hooks, options.TargetOS, options.NoHooks)
		}
		var codexRulesOut []byte
		var hasCodexRules bool
		if agent == model.AgentCodex {
			var err error
			codexRulesOut, hasCodexRules, err = compiler.CompileCodexRules(cfg)
			if err != nil {
				return nil, nil, codexRulesChange{}, nil, fmt.Errorf("compile %s: %w", CodexRulesPath, err)
			}
		}
		codexRulesUnchanged := agent != model.AgentCodex ||
			(hasCodexRules && previous.RulesPath == CodexRulesPath && previous.RulesFileSHA256 == SHA256(codexRulesOut) && bytes.Equal(s.codexRules, codexRulesOut)) ||
			(!hasCodexRules && previous.RulesPath == "" && s.codexRules == nil)
		if report.Status == model.StatusInSync && next.MasterSHA256 == s.state.MasterSHA256 &&
			command == previous.AppliedVerifyCommand &&
			targetEnvUnchanged(agent, cfg, previous, options.TargetOS) &&
			codexRulesUnchanged {
			if err := captureAllowRules(agent, report.Path, s.files[agent], &previous); err != nil {
				return nil, nil, codexRulesChange{}, nil, err
			}
			next.Targets[agent] = previous
			continue
		}
		existing := s.files[agent]
		var out []byte
		var applied model.TargetState
		var err error
		switch agent {
		case model.AgentClaude:
			out, applied, err = compiler.CompileClaude(existing, cfg, previous, options.TargetOS, options.NoHooks)
		case model.AgentGemini:
			out, applied, err = compiler.CompileGemini(existing, cfg, previous, options.TargetOS, options.NoHooks)
		case model.AgentCodex:
			out, err = compiler.CompileCodex(existing, cfg)
			if err == nil && hasCodexRules {
				applied.AppliedDenyCommands = slices.Clone(cfg.Permissions.DenyCommands)
				applied.RulesPath = CodexRulesPath
				applied.RulesFileSHA256 = SHA256(codexRulesOut)
				if !bytes.Equal(codexRulesOut, s.codexRules) {
					rulesChange = codexRulesChange{write: true, data: codexRulesOut}
					result.ChangedPaths = append(result.ChangedPaths, CodexRulesPath)
				}
			} else if err == nil && !hasCodexRules && previous.RulesPath != "" && s.codexRules != nil {
				rulesChange = codexRulesChange{remove: true}
				result.ChangedPaths = append(result.ChangedPaths, CodexRulesPath)
			}
		}
		if err != nil {
			return nil, nil, codexRulesChange{}, nil, fmt.Errorf("compile %s: %w", report.Path, err)
		}
		if !utf8.Valid(out) {
			return nil, nil, codexRulesChange{}, nil, fmt.Errorf("compile %s: invalid UTF-8", report.Path)
		}
		applied.Path = report.Path
		applied.Exists = true
		applied.FileSHA256 = SHA256(out)
		if command == "reifu hook verify" {
			applied.AppliedVerifySteps = cloneSteps(cfg.Workflow.VerifySteps)
		}
		if err := captureAllowRules(agent, report.Path, out, &applied); err != nil {
			return nil, nil, codexRulesChange{}, nil, err
		}
		next.Targets[agent] = applied
		if command != "" && (command != previous.AppliedVerifyCommand ||
			(command == "reifu hook verify" && !reflect.DeepEqual(applied.AppliedVerifySteps, previous.AppliedVerifySteps))) {
			detail := command
			if command == "reifu hook verify" && len(applied.AppliedVerifySteps) > 0 {
				var parts []string
				for _, step := range applied.AppliedVerifySteps {
					parts = append(parts, strings.Join(step, " "))
				}
				detail = fmt.Sprintf("%s [%s]", command, strings.Join(parts, " && "))
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("warning: applying verification hook to %s: %s", report.Path, detail))
		}
		if !bytes.Equal(out, existing) {
			targets[agent] = out
			result.ChangedPaths = append(result.ChangedPaths, report.Path)
		}
	}
	skills.describe(result)
	if s.state != nil && len(result.ChangedPaths) == 0 &&
		next.MasterSHA256 == s.state.MasterSHA256 && reflect.DeepEqual(next.Targets, s.state.Targets) &&
		maps.EqualFunc(next.ManagedSkills, s.state.ManagedSkills, slices.Equal[[]string]) {
		return master, targets, codexRulesChange{}, nil, nil
	}
	next.LastSyncedAt = time.Now().UTC()
	state, err := encodeState(next)
	if err != nil {
		return nil, nil, codexRulesChange{}, nil, err
	}
	result.ChangedPaths = append(result.ChangedPaths, StatePath)
	return master, targets, rulesChange, state, nil
}

func encodeState(state model.ReifuState) ([]byte, error) {
	var stateBuf bytes.Buffer
	enc := json.NewEncoder(&stateBuf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(state); err != nil {
		return nil, fmt.Errorf("encode %s: %w", StatePath, err)
	}
	return stateBuf.Bytes(), nil
}

func captureAllowRules(agent model.AgentName, target string, data []byte, state *model.TargetState) error {
	if agent == model.AgentCodex || state.ObservedAllowCommands != nil || data == nil {
		return nil
	}
	commands, err := extractCommands(importSource{agent, target, data})
	if err != nil {
		return fmt.Errorf("snapshot allow rules in %s: %w", target, err)
	}
	if commands == nil {
		commands = []string{}
	}
	state.ObservedAllowCommands = &commands
	return nil
}

func normalizedText(data []byte) []byte {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
}

func targetEnvUnchanged(agent model.AgentName, cfg model.ReifuConfig, previous model.TargetState, targetOS string) bool {
	prevSandbox := previous.AppliedSandbox != nil && *previous.AppliedSandbox
	switch agent {
	case model.AgentClaude:
		return slices.Equal(previous.AppliedOSDenyRules, compiler.OSDenyRules(targetOS)) &&
			prevSandbox == compiler.ClaudeSandboxEnabled(cfg, targetOS)
	case model.AgentGemini:
		return prevSandbox == compiler.GeminiSandboxEnabled(cfg, targetOS)
	default:
		return true
	}
}

func cloneSteps(steps [][]string) [][]string {
	if len(steps) == 0 {
		return nil
	}
	out := make([][]string, len(steps))
	for i, step := range steps {
		out[i] = slices.Clone(step)
	}
	return out
}
