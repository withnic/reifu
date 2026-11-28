package model

import "time"

type TargetStatus string

const (
	StatusStateMissing  TargetStatus = "STATE_MISSING"
	StatusInSync        TargetStatus = "IN_SYNC"
	StatusMasterUpdated TargetStatus = "MASTER_UPDATED"
	StatusDrifted       TargetStatus = "DRIFTED"
	StatusConflict      TargetStatus = "CONFLICT"
	StatusSkipped       TargetStatus = "SKIPPED"
)

type LinkStatus string

const (
	LinkStatusOK        LinkStatus = "OK"
	LinkStatusMissing   LinkStatus = "MISSING"
	LinkStatusDifferent LinkStatus = "DIFFERENT"
	LinkStatusBroken    LinkStatus = "BROKEN"
	LinkStatusCopy      LinkStatus = "COPY"
)

type ReifuState struct {
	Version         SchemaVersion             `json:"version"`
	LastSyncedAt    time.Time                 `json:"last_synced_at"`
	MasterSHA256    string                    `json:"master_sha256"`
	ManagedCommands []string                  `json:"managed_commands"`
	ManagedSkills   map[string][]string       `json:"managed_skills,omitempty"`
	Targets         map[AgentName]TargetState `json:"targets"`
}

type TargetState struct {
	Path                 string   `json:"path"`
	Exists               bool     `json:"exists"`
	FileSHA256           string   `json:"file_sha256"`
	AppliedAllowCommands []string `json:"applied_allow_commands"`
	AppliedDenyCommands  []string `json:"applied_deny_commands"`
	AppliedDenyPaths     []string `json:"applied_deny_paths"`
	AppliedVerifyCommand string   `json:"applied_verify_command"`
	// A non-nil snapshot distinguishes new external rules from existing local ones.
	ObservedAllowCommands *[]string  `json:"observed_allow_commands,omitempty"`
	AppliedOSDenyRules    []string   `json:"applied_os_deny_rules,omitempty"`
	AppliedSandbox        *bool      `json:"applied_sandbox,omitempty"`
	AppliedVerifySteps    [][]string `json:"applied_verify_steps,omitempty"`
	RulesPath             string     `json:"rules_path,omitempty"`
	RulesFileSHA256       string     `json:"rules_file_sha256,omitempty"`
}

// StatusReport matches the JSON schema for `reifu status --json`.
type StatusReport struct {
	Root         string                     `json:"root"`
	Env          string                     `json:"env"`
	StateMissing bool                       `json:"state_missing"`
	HasIssues    bool                       `json:"has_issues"`
	Links        []LinkReport               `json:"links"`
	Skills       []LinkReport               `json:"skills,omitempty"`
	Targets      map[AgentName]TargetReport `json:"targets"`
	Runner       *RunnerStatus              `json:"runner,omitempty"`
}

type SyncReport struct {
	Root         string                     `json:"root"`
	DryRun       bool                       `json:"dry_run"`
	StateMissing bool                       `json:"state_missing"`
	ChangedPaths []string                   `json:"changed_paths"`
	Warnings     []string                   `json:"warnings"`
	Targets      map[AgentName]TargetReport `json:"targets"`
	Skills       []LinkReport               `json:"skills"`
}

type LinkCommandReport struct {
	Root      string       `json:"root"`
	Mode      LinkMode     `json:"mode"`
	Check     bool         `json:"check"`
	HasIssues bool         `json:"has_issues"`
	Links     []LinkReport `json:"links"`
	Skills    []LinkReport `json:"skills"`
}

type SetupReport struct {
	Command      string       `json:"command"`
	Root         string       `json:"root"`
	Preset       string       `json:"preset"`
	DryRun       bool         `json:"dry_run"`
	Files        []string     `json:"files"`
	Links        []LinkReport `json:"links"`
	ChangedPaths []string     `json:"changed_paths"`
	Warnings     []string     `json:"warnings"`
}

type RunnerStatus struct {
	Blocked   bool          `json:"blocked"`
	GitRepo   bool          `json:"git_repo"`
	GitDirty  bool          `json:"git_dirty"`
	Phases    []string      `json:"phases"`
	Agents    []AgentStatus `json:"agents"`
	LatestRun *RunSummary   `json:"latest_run,omitempty"`
}

type AgentStatus struct {
	Name       string `json:"name"`
	Available  bool   `json:"available"`
	Executable string `json:"executable,omitempty"`
	IsCustom   bool   `json:"is_custom"`
}

type LinkReport struct {
	Target string     `json:"target"`
	Status LinkStatus `json:"status"`
	Mode   LinkMode   `json:"mode,omitempty"`
}

type TargetReport struct {
	Path   string       `json:"path"`
	Status TargetStatus `json:"status"`
}
