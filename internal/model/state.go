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
}

// StatusReport matches the JSON schema for `reifu status --json`.
type StatusReport struct {
	Root         string                     `json:"root"`
	Env          string                     `json:"env"`
	StateMissing bool                       `json:"state_missing"`
	HasIssues    bool                       `json:"has_issues"`
	Links        []LinkReport               `json:"links"`
	Targets      map[AgentName]TargetReport `json:"targets"`
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
