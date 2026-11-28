package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type SchemaVersion int

const CurrentSchemaVersion SchemaVersion = 1

type LinkMode string

const (
	LinkModeAuto     LinkMode = "auto"
	LinkModeHard     LinkMode = "hard"
	LinkModeSymlink  LinkMode = "symlink"
	LinkModeCopy     LinkMode = "copy"
	LinkModeJunction LinkMode = "junction"
)

type AgentName string

const (
	AgentClaude AgentName = "claude"
	AgentCodex  AgentName = "codex"
	AgentGemini AgentName = "gemini"
)

type SandboxMode string

const (
	SandboxWorkspaceWrite   SandboxMode = "workspace-write"
	SandboxReadOnly         SandboxMode = "read-only"
	SandboxDangerFullAccess SandboxMode = "danger-full-access"
)

type ApprovalPolicy string

const (
	ApprovalNever     ApprovalPolicy = "never"
	ApprovalOnRequest ApprovalPolicy = "on-request"
	ApprovalUntrusted ApprovalPolicy = "untrusted"
)

type ReifuConfig struct {
	Runner      RunnerConfig     `json:"runner,omitempty"`
	Version     SchemaVersion    `json:"version"`
	Project     ProjectMeta      `json:"project"`
	Agents      AgentsConfig     `json:"agents"`
	Links       LinksConfig      `json:"links"`
	Skills      SkillsConfig     `json:"skills,omitempty"`
	Permissions PermissionsModel `json:"permissions"`
	Sandbox     SandboxModel     `json:"sandbox"`
	Hooks       HooksModel       `json:"hooks"`
	Workflow    WorkflowModel    `json:"workflow,omitempty"`
}

// MarshalJSON omits optional sections when unset and preserves empty skill targets.
func (c ReifuConfig) MarshalJSON() ([]byte, error) {
	type config ReifuConfig
	var runner *RunnerConfig
	if c.Runner.Implementer != "" || c.Runner.Reviewer != "" || len(c.Runner.Agents) != 0 {
		runner = &c.Runner
	}
	type skillsJSON struct {
		Enabled *bool     `json:"enabled,omitempty"`
		Source  string    `json:"source,omitempty"`
		Targets *[]string `json:"targets,omitempty"`
	}
	var skills *skillsJSON
	if c.Skills.Enabled != nil || c.Skills.Source != "" || c.Skills.Targets != nil {
		skills = &skillsJSON{Enabled: c.Skills.Enabled, Source: c.Skills.Source}
		if c.Skills.Targets != nil {
			skills.Targets = &c.Skills.Targets
		}
	}
	return json.Marshal(struct {
		config
		Runner *RunnerConfig `json:"runner,omitempty"`
		Skills *skillsJSON   `json:"skills,omitempty"`
	}{config: config(c), Runner: runner, Skills: skills})
}

type ProjectMeta struct {
	Name   string `json:"name"`
	Preset string `json:"preset"`
}
type AgentsConfig struct {
	Targets       []AgentName `json:"targets"`
	CreateMissing bool        `json:"create_missing"`
}
type LinksConfig struct {
	Mode    LinkMode `json:"mode"`
	Targets []string `json:"targets"`
}
type SkillsConfig struct {
	Enabled *bool    `json:"enabled,omitempty"`
	Source  string   `json:"source,omitempty"`
	Targets []string `json:"targets,omitempty"`
}

func (c ReifuConfig) SkillsEnabled() bool {
	return c.Skills.Enabled == nil || *c.Skills.Enabled
}

func (c ReifuConfig) EffectiveSkillsSource() string {
	if c.Skills.Source == "" {
		return ".agents/skills"
	}
	return c.Skills.Source
}

func (c ReifuConfig) EffectiveSkillsTargets(agents []AgentName) []string {
	if c.Skills.Targets != nil {
		return c.Skills.Targets
	}
	for _, agent := range agents {
		if agent == AgentClaude {
			return []string{".claude/skills"}
		}
	}
	return nil
}

type PermissionsModel struct {
	AllowCommands       []string `json:"allow_commands"`
	StrictAllowCommands []string `json:"strict_allow_commands,omitempty"`
	DenyCommands        []string `json:"deny_commands"`
	DenyPaths           []string `json:"deny_paths"`
}
type SandboxModel struct {
	Mode           SandboxMode    `json:"mode"`
	ApprovalPolicy ApprovalPolicy `json:"approval_policy"`
	NetworkAccess  bool           `json:"network_access"`
	GeminiSandbox  bool           `json:"gemini_sandbox,omitempty"`
}
type HooksModel struct {
	VerifyCommand        string `json:"verify_command"`
	VerifyCommandWindows string `json:"verify_command_windows"`
	VerifyCommandUnix    string `json:"verify_command_unix"`
}
type WorkflowModel struct {
	Stack        string        `json:"stack,omitempty"`
	Linter       string        `json:"linter,omitempty"`
	SetupNote    string        `json:"setup_note,omitempty"`
	RequireFiles []string      `json:"require_files,omitempty"`
	Commands     []CommandStep `json:"commands,omitempty"`
	VerifySteps  [][]string    `json:"verify_steps,omitempty"`
	Gitignore    []string      `json:"gitignore,omitempty"`
}
type CommandStep struct {
	Step string `json:"step"`
	Run  string `json:"run"`
}

var presetPattern = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$")

func ValidatePresetName(name string) error {
	if !presetPattern.MatchString(name) {
		return invalid("project.preset", fmt.Errorf("invalid preset name %q", name))
	}
	return nil
}

// ApplyDefaults fills fields whose omitted values have schema defaults.
func (c *ReifuConfig) ApplyDefaults() {
	if c.Agents.Targets == nil {
		c.Agents.Targets = []AgentName{AgentClaude, AgentCodex, AgentGemini}
	}
	if c.Links.Mode == "" {
		c.Links.Mode = LinkModeAuto
	}
	if c.Sandbox.Mode == "" {
		c.Sandbox.Mode = SandboxWorkspaceWrite
	}
	if c.Sandbox.ApprovalPolicy == "" {
		c.Sandbox.ApprovalPolicy = ApprovalNever
	}
}

// Validate checks the configuration without modifying it.
func (c ReifuConfig) Validate() error {
	c.ApplyDefaults()
	if err := c.Runner.Validate(); err != nil {
		return err
	}
	if c.Version != CurrentSchemaVersion {
		return invalid("version", fmt.Errorf("unsupported schema version %d", c.Version))
	}
	if err := ValidatePresetName(c.Project.Preset); err != nil {
		return err
	}
	if err := c.validateSkills(); err != nil {
		return err
	}
	seenLinks := make(map[string]struct{}, len(c.Links.Targets))
	for i, target := range c.Links.Targets {
		if !validLinkTarget(target) {
			return invalid(fmt.Sprintf("links.targets[%d]", i), fmt.Errorf("invalid relative target %q", target))
		}
		norm := normalizeLinkTargetKey(target)
		if _, dup := seenLinks[norm]; dup {
			return invalid(fmt.Sprintf("links.targets[%d]", i), fmt.Errorf("duplicate link target %q", target))
		}
		seenLinks[norm] = struct{}{}
	}
	seenAgents := make(map[AgentName]struct{}, len(c.Agents.Targets))
	for i, agent := range c.Agents.Targets {
		switch agent {
		case AgentClaude, AgentCodex, AgentGemini:
		default:
			return invalid(fmt.Sprintf("agents.targets[%d]", i), fmt.Errorf("unknown agent %q", agent))
		}
		if _, dup := seenAgents[agent]; dup {
			return invalid(fmt.Sprintf("agents.targets[%d]", i), fmt.Errorf("duplicate agent %q", agent))
		}
		seenAgents[agent] = struct{}{}
	}
	switch c.Links.Mode {
	case LinkModeAuto, LinkModeHard, LinkModeSymlink, LinkModeCopy:
	default:
		return invalid("links.mode", fmt.Errorf("unknown mode %q", c.Links.Mode))
	}
	switch c.Sandbox.Mode {
	case SandboxWorkspaceWrite, SandboxReadOnly, SandboxDangerFullAccess:
	default:
		return invalid("sandbox.mode", fmt.Errorf("unknown mode %q", c.Sandbox.Mode))
	}
	switch c.Sandbox.ApprovalPolicy {
	case ApprovalNever, ApprovalOnRequest, ApprovalUntrusted:
	default:
		return invalid("sandbox.approval_policy", fmt.Errorf("unknown policy %q", c.Sandbox.ApprovalPolicy))
	}
	if c.Sandbox.Mode == SandboxDangerFullAccess && c.Sandbox.ApprovalPolicy == ApprovalNever {
		return invalid("sandbox.approval_policy", fmt.Errorf("%q requires an approval policy other than %q", c.Sandbox.Mode, ApprovalNever))
	}
	fields := []struct {
		name   string
		values []string
		array  bool
	}{
		{"permissions.allow_commands", c.Permissions.AllowCommands, true},
		{"permissions.strict_allow_commands", c.Permissions.StrictAllowCommands, true},
		{"permissions.deny_commands", c.Permissions.DenyCommands, true},
		{"permissions.deny_paths", c.Permissions.DenyPaths, true},
		{"hooks.verify_command", []string{c.Hooks.VerifyCommand}, false},
		{"hooks.verify_command_windows", []string{c.Hooks.VerifyCommandWindows}, false},
		{"hooks.verify_command_unix", []string{c.Hooks.VerifyCommandUnix}, false},
		{"project.name", []string{c.Project.Name}, false},
		{"workflow.stack", []string{c.Workflow.Stack}, false},
		{"workflow.linter", []string{c.Workflow.Linter}, false},
		{"workflow.setup_note", []string{c.Workflow.SetupNote}, false},
		{"workflow.gitignore", c.Workflow.Gitignore, true},
	}
	for _, field := range fields {
		for i, value := range field.values {
			if field.array && strings.TrimSpace(value) == "" {
				return invalid(fmt.Sprintf("%s[%d]", field.name, i), fmt.Errorf("must not be empty"))
			}
			if strings.ContainsFunc(value, unicode.IsControl) {
				name := field.name
				if field.array {
					name = fmt.Sprintf("%s[%d]", name, i)
				}
				return invalid(name, fmt.Errorf("contains a control character"))
			}
		}
	}
	for i, req := range c.Workflow.RequireFiles {
		if !validRelativePath(req) {
			return invalid(fmt.Sprintf("workflow.require_files[%d]", i), fmt.Errorf("invalid relative path %q", req))
		}
	}
	for i, cmd := range c.Workflow.Commands {
		if strings.TrimSpace(cmd.Step) == "" || strings.ContainsFunc(cmd.Step, unicode.IsControl) {
			return invalid(fmt.Sprintf("workflow.commands[%d].step", i), fmt.Errorf("invalid command step"))
		}
		if strings.TrimSpace(cmd.Run) == "" || strings.ContainsFunc(cmd.Run, unicode.IsControl) {
			return invalid(fmt.Sprintf("workflow.commands[%d].run", i), fmt.Errorf("invalid command run"))
		}
	}
	for i, step := range c.Workflow.VerifySteps {
		if len(step) == 0 || strings.TrimSpace(step[0]) == "" {
			return invalid(fmt.Sprintf("workflow.verify_steps[%d]", i), fmt.Errorf("verify step must start with a command"))
		}
		for j, arg := range step {
			if strings.ContainsFunc(arg, unicode.IsControl) {
				return invalid(fmt.Sprintf("workflow.verify_steps[%d][%d]", i, j), fmt.Errorf("contains a control character"))
			}
		}
	}
	return nil
}

func (c ReifuConfig) validateSkills() error {
	source := c.EffectiveSkillsSource()
	if !validRelativePath(source) {
		return invalid("skills.source", fmt.Errorf("invalid relative path %q", source))
	}
	source = normalizeLinkTargetKey(source)
	targets := c.EffectiveSkillsTargets(c.Agents.Targets)
	seen := make(map[string]struct{}, len(targets))
	for i, target := range targets {
		field := fmt.Sprintf("skills.targets[%d]", i)
		if !validRelativePath(target) {
			return invalid(field, fmt.Errorf("invalid relative path %q", target))
		}
		key := normalizeLinkTargetKey(target)
		if _, dup := seen[key]; dup {
			return invalid(field, fmt.Errorf("duplicate skills target %q", target))
		}
		// A nested target could be reached through a skill link in its
		// parent target and write into the source tree.
		for other := range seen {
			if strings.HasPrefix(key, other+"/") || strings.HasPrefix(other, key+"/") {
				return invalid(field, fmt.Errorf("skills target %q is nested with another target", target))
			}
		}
		if key == source || strings.HasPrefix(key, source+"/") || strings.HasPrefix(source, key+"/") {
			return invalid(field, fmt.Errorf("skills target %q overlaps source %q", target, c.EffectiveSkillsSource()))
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Target paths use the same relative-path constraints on every platform.
func validLinkTarget(target string) bool {
	if !validRelativePath(target) {
		return false
	}
	return normalizeLinkTargetKey(target) != "agents.md"
}

func validRelativePath(target string) bool {
	path := strings.ReplaceAll(target, "\\", "/")
	if strings.HasPrefix(path, "/") || strings.Contains(path, ":") || strings.ContainsFunc(path, unicode.IsControl) {
		return false
	}
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	var components []string
	for _, part := range parts {
		if part == "." {
			continue
		}
		if part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || isWindowsReservedName(part) {
			return false
		}
		components = append(components, part)
	}
	return len(components) != 0
}

func normalizeLinkTargetKey(target string) string {
	path := strings.ReplaceAll(target, "\\", "/")
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	var components []string
	for _, part := range parts {
		if part == "." {
			continue
		}
		components = append(components, strings.ToLower(part))
	}
	return strings.Join(components, "/")
}

func isWindowsReservedName(part string) bool {
	base := part
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}
