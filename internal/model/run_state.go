package model

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

type NotifyMode string

const (
	NotifyOff   NotifyMode = "off"
	NotifyEnd   NotifyMode = "end"
	NotifyPhase NotifyMode = "phase"
)

func (m NotifyMode) Validate() error {
	switch m {
	case "", NotifyOff, NotifyEnd, NotifyPhase:
		return nil
	default:
		return invalid("runner.notify", fmt.Errorf("invalid notification mode %q (want off, end or phase)", m))
	}
}

const PromptPlaceholder = "{prompt}"

type RunnerConfig struct {
	Implementer string              `json:"implementer,omitempty"`
	Reviewer    string              `json:"reviewer,omitempty"`
	Agents      map[string][]string `json:"agents,omitempty"`
	Notify      NotifyMode          `json:"notify,omitempty"`
}

func (c RunnerConfig) Validate() error {
	for _, role := range []struct{ field, name string }{
		{"runner.implementer", c.Implementer}, {"runner.reviewer", c.Reviewer},
	} {
		if role.name != "" && !presetPattern.MatchString(role.name) {
			return invalid(role.field, fmt.Errorf("invalid agent name %q", role.name))
		}
	}
	for name, argv := range c.Agents {
		field := fmt.Sprintf("runner.agents[%q]", name)
		if !presetPattern.MatchString(name) {
			return invalid(field, fmt.Errorf("invalid agent name %q", name))
		}
		if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
			return invalid(field, fmt.Errorf("must start with a command"))
		}
		if strings.Contains(argv[0], PromptPlaceholder) {
			return invalid(fmt.Sprintf("%s[0]", field), fmt.Errorf("executable must not contain %s", PromptPlaceholder))
		}
		for i, arg := range argv {
			if strings.ContainsFunc(arg, unicode.IsControl) {
				return invalid(fmt.Sprintf("%s[%d]", field, i), fmt.Errorf("contains a control character"))
			}
		}
	}
	return c.Notify.Validate()
}

func ValidateRunID(id string) error {
	if !presetPattern.MatchString(id) {
		return invalid("run_id", fmt.Errorf("invalid run ID %q", id))
	}
	return nil
}

type PhaseStatus string

const (
	PhasePending      PhaseStatus = "pending"
	PhaseImplementing PhaseStatus = "implementing"
	PhaseImplemented  PhaseStatus = "implemented"
	PhaseReviewing    PhaseStatus = "reviewing"
	PhaseCompleted    PhaseStatus = "completed"
	PhaseFailed       PhaseStatus = "failed"
	PhaseBlocked      PhaseStatus = "blocked"
)

func (s PhaseStatus) Terminal() bool {
	return s == PhaseCompleted || s == PhaseFailed || s == PhaseBlocked
}

type RunState struct {
	Version     int              `json:"version"`
	RunID       string           `json:"run_id"`
	StartedAt   time.Time        `json:"started_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Implementer string           `json:"implementer"`
	Reviewer    string           `json:"reviewer"`
	NoReview    bool             `json:"no_review"`
	NoCommit    bool             `json:"no_commit"`
	Notify      NotifyMode       `json:"notify,omitempty"`
	RunDir      string           `json:"run_dir"`
	Phases      []PhaseRunRecord `json:"phases"`
}

type AgentCommand struct {
	Name     string   `json:"name"`
	Argv     []string `json:"argv"`
	IsCustom bool     `json:"is_custom"`
}

type RunPlan struct {
	Implementer AgentCommand  `json:"implementer"`
	Reviewer    *AgentCommand `json:"reviewer,omitempty"`
	State       RunState      `json:"state"`
}

type RunEventType string

const (
	EventRunPlan        RunEventType = "run_plan"
	EventRunStarted     RunEventType = "run_started"
	EventPhaseStarted   RunEventType = "phase_started"
	EventStepStarted    RunEventType = "step_started"
	EventLog            RunEventType = "log"
	EventHeartbeat      RunEventType = "heartbeat"
	EventCommit         RunEventType = "commit"
	EventStepCompleted  RunEventType = "step_completed"
	EventPhaseCompleted RunEventType = "phase_completed"
	EventPhaseFailed    RunEventType = "phase_failed"
	EventRunCompleted   RunEventType = "run_completed"
	EventRunFailed      RunEventType = "run_failed"
)

type RunEvent struct {
	SchemaVersion int          `json:"schema_version"`
	Type          RunEventType `json:"type"`
	Timestamp     time.Time    `json:"timestamp"`
	RunID         string       `json:"run_id,omitempty"`
	Phase         string       `json:"phase,omitempty"`
	PhaseIndex    int          `json:"phase_index,omitempty"`
	PhaseTotal    int          `json:"phase_total,omitempty"`
	Step          string       `json:"step,omitempty"`
	Agent         string       `json:"agent,omitempty"`
	Commit        string       `json:"commit,omitempty"`
	Path          string       `json:"path,omitempty"`
	Text          string       `json:"text,omitempty"`
	ElapsedMS     int64        `json:"elapsed_ms"`
	Status        PhaseStatus  `json:"status,omitempty"`
	Error         string       `json:"error,omitempty"`
	Warning       string       `json:"warning,omitempty"`
	Plan          *RunPlan     `json:"plan,omitempty"`
	State         *RunState    `json:"state,omitempty"`
}

type PhaseRunRecord struct {
	BaseCommit      string      `json:"base_commit,omitempty"`
	ReviewBase      string      `json:"review_base,omitempty"`
	CommitBase      *string     `json:"commit_base,omitempty"`
	ImplementAgent  string      `json:"implement_agent,omitempty"`
	File            string      `json:"file"`
	Name            string      `json:"name"`
	Status          PhaseStatus `json:"status"`
	ImplementCommit string      `json:"implement_commit,omitempty"`
	ReviewCommit    string      `json:"review_commit,omitempty"`
	ImplementLog    string      `json:"implement_log"`
	ReviewReport    string      `json:"review_report"`
	DurationMS      int64       `json:"duration_ms"`
	ImplementDiff   string      `json:"implement_diff,omitempty"`
	ResumeStep      string      `json:"resume_step,omitempty"`
	Error           string      `json:"error,omitempty"`
}

type RunSummary struct {
	RunID     string      `json:"run_id"`
	StartedAt time.Time   `json:"started_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	Status    PhaseStatus `json:"status"`
	Completed int         `json:"completed"`
	Total     int         `json:"total"`
}

func (r RunState) Summary() RunSummary {
	summary := RunSummary{RunID: r.RunID, StartedAt: r.StartedAt, UpdatedAt: r.UpdatedAt, Status: PhasePending, Total: len(r.Phases)}
	for _, phase := range r.Phases {
		if phase.Status == PhaseCompleted {
			summary.Completed++
		} else if summary.Status == PhasePending {
			summary.Status = phase.Status
		}
	}
	if summary.Total > 0 && summary.Completed == summary.Total {
		summary.Status = PhaseCompleted
	}
	return summary
}
