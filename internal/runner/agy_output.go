package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/withnic/reifu/internal/model"
)

type agyOutputMode int

const (
	agyOutputText agyOutputMode = iota
	agyOutputJSON
	agyOutputStreamJSON
)

func detectAgyOutputMode(a AgentCommand) agyOutputMode {
	if a.Name != "agy" {
		return agyOutputText
	}
	for i := 1; i < len(a.Argv); i++ {
		arg := a.Argv[i]
		var format string
		if after, ok := strings.CutPrefix(arg, "--output-format="); ok {
			format = after
		} else if arg == "--output-format" && i+1 < len(a.Argv) {
			format = a.Argv[i+1]
			i++
		} else {
			continue
		}
		switch format {
		case "stream-json":
			return agyOutputStreamJSON
		case "json":
			return agyOutputJSON
		}
	}
	return agyOutputText
}

type agyStreamEvent struct {
	Event      string         `json:"event"`
	StepUpdate *agyStepUpdate `json:"step_update,omitempty"`
	Result     *agyResult     `json:"result,omitempty"`
}

type agyStepUpdate struct {
	StepIndex int          `json:"step_index"`
	State     string       `json:"state"`
	StepType  string       `json:"step_type"`
	ToolName  string       `json:"tool_name,omitempty"`
	TextDelta string       `json:"text_delta,omitempty"`
	ToolInfo  *agyToolInfo `json:"tool_info,omitempty"`
}

type agyToolInfo struct {
	Name       string            `json:"name,omitempty"`
	Parameters map[string]any    `json:"parameters,omitempty"`
	Output     string            `json:"output,omitempty"`
	Error      *agyToolErrorInfo `json:"error,omitempty"`
}

type agyToolErrorInfo struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

type agyResult struct {
	ConversationID string            `json:"conversation_id,omitempty"`
	Status         string            `json:"status"`
	Response       string            `json:"response"`
	Error          string            `json:"error,omitempty"`
	DeniedActions  []agyDeniedAction `json:"denied_actions,omitempty"`
}

type agyDeniedAction struct {
	Action      string `json:"action"`
	DisplayName string `json:"display_name,omitempty"`
}

type agyOutputOutcome struct {
	Commands              []model.CommandRecord
	DeniedCommands        []string
	AllowCommandSuggests  []string
	Err                   error
	HasStructuredResponse bool
}

// agyOutputParser consumes stdout from `agy --output-format stream-json` or
// `--output-format json`, writes human-readable progress and response text to
// `out`, and records tool/command outcomes for post-run verification.
type agyOutputParser struct {
	mode             agyOutputMode
	step             string
	agent            string
	out              io.Writer
	buf              bytes.Buffer
	pendingTools     map[int]string
	completedSteps   map[int]bool
	commands         []model.CommandRecord
	result           *agyResult
	streamedText     bool
	endedWithNewline bool
	malformedLines   int
	writeErr         error
}

func newAgyOutputParser(mode agyOutputMode, step, agent string, out io.Writer) *agyOutputParser {
	return &agyOutputParser{
		mode:             mode,
		step:             step,
		agent:            agent,
		out:              out,
		pendingTools:     make(map[int]string),
		completedSteps:   make(map[int]bool),
		endedWithNewline: true,
	}
}

func (p *agyOutputParser) Write(data []byte) (int, error) {
	if p.mode == agyOutputJSON {
		p.buf.Write(data)
		return len(data), nil
	}
	p.buf.Write(data)
	for {
		raw := p.buf.Bytes()
		idx := bytes.IndexByte(raw, '\n')
		if idx < 0 {
			break
		}
		line := string(bytes.TrimRight(raw[:idx], "\r"))
		p.buf.Next(idx + 1)
		p.processStreamLine(line)
	}
	return len(data), nil
}

func (p *agyOutputParser) finish() agyOutputOutcome {
	if p.mode == agyOutputJSON {
		p.processJSONEnvelope(p.buf.Bytes())
	} else if p.buf.Len() > 0 {
		line := string(bytes.TrimRight(p.buf.Bytes(), "\r\n"))
		p.buf.Reset()
		p.processStreamLine(line)
	}
	return p.evaluate()
}

func (p *agyOutputParser) emitText(text string) {
	if text == "" || p.writeErr != nil {
		return
	}
	if _, err := io.WriteString(p.out, text); err != nil {
		p.writeErr = err
		return
	}
	p.endedWithNewline = strings.HasSuffix(text, "\n")
}

func (p *agyOutputParser) emitLine(line string) {
	if line == "" {
		return
	}
	if !p.endedWithNewline {
		p.emitText("\n")
	}
	p.emitText(line + "\n")
}

func (p *agyOutputParser) processJSONEnvelope(raw []byte) {
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf}))
	if len(trimmed) == 0 {
		return
	}
	var res agyResult
	if err := json.Unmarshal(trimmed, &res); err != nil || (res.Status == "" && res.Response == "" && res.Error == "" && len(res.DeniedActions) == 0 && res.ConversationID == "") {
		p.malformedLines++
		p.emitLine(string(trimmed))
		return
	}
	p.result = &res
	if res.Response != "" {
		p.emitText(res.Response)
		if !strings.HasSuffix(res.Response, "\n") {
			p.emitText("\n")
		}
		p.streamedText = true
	} else if res.Error != "" {
		p.emitLine(res.Error)
	}
}

func (p *agyOutputParser) processStreamLine(line string) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
	if trimmed == "" {
		return
	}
	var ev agyStreamEvent
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil || ev.Event == "" {
		p.malformedLines++
		p.emitLine(trimmed)
		return
	}
	switch ev.Event {
	case "init":
		// Metadata only; keep human logs concise.
	case "step_update":
		if ev.StepUpdate != nil {
			p.handleStepUpdate(ev.StepUpdate)
		}
	case "result":
		if ev.Result != nil {
			res := *ev.Result
			p.result = &res
			if !p.streamedText && res.Response != "" {
				if !p.endedWithNewline {
					p.emitText("\n")
				}
				p.emitText(res.Response)
				if !strings.HasSuffix(res.Response, "\n") {
					p.emitText("\n")
				}
				p.streamedText = true
			} else if res.Response == "" && res.Error != "" {
				p.emitLine(res.Error)
			}
		}
	default:
		// Ignore unrecognized future event types gracefully.
	}
}

func (p *agyOutputParser) handleStepUpdate(u *agyStepUpdate) {
	if u.StepType == "agent_response" && u.TextDelta != "" {
		p.emitText(u.TextDelta)
		p.streamedText = true
		return
	}
	if u.StepType != "tool" {
		return
	}
	tool := u.ToolName
	if tool == "" && u.ToolInfo != nil {
		tool = u.ToolInfo.Name
	}
	cmdLine := extractCommandLine(u.ToolInfo)
	switch strings.ToUpper(u.State) {
	case "ACTIVE":
		if cmdLine != "" {
			p.pendingTools[u.StepIndex] = cmdLine
		}
	case "DONE":
		if p.completedSteps[u.StepIndex] {
			return
		}
		p.completedSteps[u.StepIndex] = true
		if cmdLine == "" {
			cmdLine = p.pendingTools[u.StepIndex]
		}
		delete(p.pendingTools, u.StepIndex)
		if isCommandTool(tool) || cmdLine != "" {
			p.commands = append(p.commands, model.CommandRecord{
				Step:    p.step,
				Agent:   p.agent,
				Tool:    firstSet(tool, "run_command"),
				Command: cmdLine,
				Status:  model.CommandAllowed,
			})
			p.emitLine(fmt.Sprintf("[tool] %s: %s (ok)", firstSet(tool, "run_command"), cmdLine))
		} else if tool != "" {
			p.emitLine(fmt.Sprintf("[tool] %s (ok)", tool))
		}
	case "ERROR":
		if p.completedSteps[u.StepIndex] {
			return
		}
		p.completedSteps[u.StepIndex] = true
		if cmdLine == "" {
			cmdLine = p.pendingTools[u.StepIndex]
		}
		delete(p.pendingTools, u.StepIndex)
		errMsg := ""
		if u.ToolInfo != nil && u.ToolInfo.Error != nil {
			errMsg = u.ToolInfo.Error.Message
		}
		denied, deniedBy := classifyPermissionDenial(errMsg)
		if isCommandTool(tool) || cmdLine != "" || denied {
			rec := model.CommandRecord{
				Step:    p.step,
				Agent:   p.agent,
				Tool:    firstSet(tool, "run_command"),
				Command: cmdLine,
				Status:  model.CommandError,
				Error:   firstLine(errMsg),
			}
			if denied {
				rec.Status = model.CommandDenied
				rec.DeniedBy = deniedBy
			}
			p.commands = append(p.commands, rec)
			label := firstSet(cmdLine, rec.Tool)
			if denied {
				p.emitLine(fmt.Sprintf("[tool] %s: %s (denied: %s)", rec.Tool, label, deniedBy))
			} else {
				p.emitLine(fmt.Sprintf("[tool] %s: %s (error)", rec.Tool, label))
			}
		} else if tool != "" {
			p.emitLine(fmt.Sprintf("[tool] %s (error)", tool))
		}
	}
}

func isCommandTool(name string) bool {
	switch strings.ToLower(name) {
	case "run_command", "bash", "shell", "command":
		return true
	default:
		return false
	}
}

func extractCommandLine(info *agyToolInfo) string {
	if info == nil || len(info.Parameters) == 0 {
		return ""
	}
	for _, key := range []string{"CommandLine", "command_line", "command", "cmd"} {
		if value, ok := info.Parameters[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func classifyPermissionDenial(errMsg string) (bool, string) {
	if errMsg == "" {
		return false, ""
	}
	lower := strings.ToLower(errMsg)
	if strings.Contains(lower, "matches user-configured deny rule") {
		return true, "deny_rule"
	}
	if strings.Contains(lower, "permission check failed") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "user denied permission") ||
		strings.Contains(lower, "auto-denied") {
		return true, "headless_auto_deny"
	}
	return false, ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}

func (p *agyOutputParser) evaluate() agyOutputOutcome {
	outcome := agyOutputOutcome{
		Commands: slices.Clone(p.commands),
	}
	if p.writeErr != nil {
		outcome.Err = p.writeErr
		return outcome
	}
	if p.result == nil {
		if p.malformedLines > 0 {
			outcome.Err = fmt.Errorf("agy output did not contain a valid terminal result (%d malformed line(s))", p.malformedLines)
		} else {
			outcome.Err = fmt.Errorf("agy output ended without a terminal result event")
		}
		return outcome
	}

	// If result.denied_actions reports denials that were not captured in
	// step_update (for example under --output-format json or non-command tools),
	// record fallback entries using the available action/tool names.
	hasAutoDeniedCommand := false
	for _, rec := range outcome.Commands {
		if rec.Status == model.CommandDenied && rec.DeniedBy == "headless_auto_deny" {
			hasAutoDeniedCommand = true
			break
		}
	}
	for _, da := range p.result.DeniedActions {
		if da.Action == "command" && hasAutoDeniedCommand {
			continue
		}
		toolName := firstSet(da.DisplayName, da.Action, "unknown")
		outcome.Commands = append(outcome.Commands, model.CommandRecord{
			Step:     p.step,
			Agent:    p.agent,
			Tool:     toolName,
			Status:   model.CommandDenied,
			DeniedBy: "headless_auto_deny",
			Error:    fmt.Sprintf("headless mode auto-denied %q permission", da.Action),
		})
	}

	for _, rec := range outcome.Commands {
		if rec.Status != model.CommandDenied {
			continue
		}
		label := firstSet(rec.Command, rec.Tool)
		if label != "" && !slices.Contains(outcome.DeniedCommands, label) {
			outcome.DeniedCommands = append(outcome.DeniedCommands, label)
		}
		if rec.DeniedBy == "headless_auto_deny" && label != "" && !slices.Contains(outcome.AllowCommandSuggests, label) {
			outcome.AllowCommandSuggests = append(outcome.AllowCommandSuggests, label)
		}
	}

	outcome.HasStructuredResponse = strings.TrimSpace(p.result.Response) != ""

	switch {
	case p.result.Status != "SUCCESS":
		detail := firstSet(firstLine(p.result.Error), p.result.Status, "unknown status")
		outcome.Err = fmt.Errorf("agy reported status %q: %s", p.result.Status, detail)
	case len(p.result.DeniedActions) > 0 || len(outcome.AllowCommandSuggests) > 0:
		candidates := outcome.AllowCommandSuggests
		if len(candidates) == 0 {
			for _, da := range p.result.DeniedActions {
				candidates = append(candidates, firstSet(da.DisplayName, da.Action))
			}
		}
		quoted := make([]string, len(candidates))
		for i, c := range candidates {
			quoted[i] = fmt.Sprintf("%q", c)
		}
		outcome.Err = fmt.Errorf("agy auto-denied required permission(s): %s; consider adding needed patterns to permissions.allow_commands in .reifu.jsonc and run reifu run --resume", strings.Join(quoted, ", "))
	case !outcome.HasStructuredResponse:
		outcome.Err = fmt.Errorf("agy exited with status SUCCESS but produced an empty response")
	}
	return outcome
}
