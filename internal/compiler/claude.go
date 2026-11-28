package compiler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/withnic/reifu/internal/model"
)

// ResolveVerifyCommand prefers a nonempty OS override over the common command.
// targetOS accepts either "windows"/"unix" or a sysenv.Env string ("windows", "darwin", "linux", "wsl2-ext4", "wsl2-ntfs").
func ResolveVerifyCommand(hooks model.HooksModel, targetOS string, noHooks bool) string {
	switch {
	case noHooks:
		return ""
	case targetOS == "windows" && hooks.VerifyCommandWindows != "":
		return hooks.VerifyCommandWindows
	case isUnixEnv(targetOS) && hooks.VerifyCommandUnix != "":
		return hooks.VerifyCommandUnix
	default:
		return hooks.VerifyCommand
	}
}

func isUnixEnv(targetOS string) bool {
	return targetOS == "unix" || targetOS == "darwin" || targetOS == "linux" || strings.HasPrefix(targetOS, "wsl2")
}

// OSDenyRules returns OS-appropriate system, credential, and agent-config protection deny rules for Claude Code.
// These are injected at compile time so .reifu.jsonc stays OS-neutral across teams.
func OSDenyRules(targetOS string) []string {
	if targetOS == "" {
		return nil
	}
	var rules []string
	// Claude Code file-permission rules match Edit(...) for both Edit and Write tools;
	// Write(...) never matches and triggers startup warnings.
	for _, sharedCfg := range []string{".claude/settings.json", ".codex/config.toml", ".gemini/settings.json"} {
		rules = append(rules, "Edit("+sharedCfg+")")
	}
	for _, localCfg := range []string{".claude/settings.local.json", ".gemini/settings.local.json"} {
		for _, verb := range []string{"Read", "Edit"} {
			rules = append(rules, verb+"("+localCfg+")")
		}
	}
	for _, secretDir := range []string{"~/.ssh/**", "~/.aws/**", "~/.gnupg/**"} {
		for _, verb := range []string{"Read", "Edit"} {
			rules = append(rules, verb+"("+secretDir+")")
		}
	}
	var sysPaths []string
	switch targetOS {
	case "windows":
		sysPaths = []string{"C:/Windows/**", "C:/Program Files/**", "C:/Program Files (x86)/**"}
	case "darwin":
		sysPaths = []string{"/System/**", "/Library/**", "/etc/**"}
	case "wsl2-ext4", "wsl2-ntfs":
		sysPaths = []string{"/etc/**", "/usr/**", "/bin/**", "/sbin/**", "/mnt/c/Windows/**", "/mnt/c/Program Files/**"}
	default:
		if isUnixEnv(targetOS) {
			sysPaths = []string{"/etc/**", "/usr/**", "/bin/**", "/sbin/**"}
		}
	}
	for _, p := range sysPaths {
		rules = append(rules, "Edit("+p+")")
	}
	return rules
}

// ClaudeSandboxEnabled reports whether Claude Code's native sandbox should be
// enabled for the given config and target environment (macOS/Linux/WSL2 only).
func ClaudeSandboxEnabled(cfg model.ReifuConfig, targetOS string) bool {
	return isUnixEnv(targetOS) && cfg.Sandbox.Mode != model.SandboxDangerFullAccess
}

// GeminiSandboxEnabled reports whether Gemini CLI's tools.sandbox should be
// enabled (opt-in via sandbox.gemini_sandbox on non-Windows environments).
func GeminiSandboxEnabled(cfg model.ReifuConfig, targetOS string) bool {
	return cfg.Sandbox.GeminiSandbox && isUnixEnv(targetOS) && cfg.Sandbox.Mode != model.SandboxDangerFullAccess
}

// CompileClaude merges the desired configuration into a Claude settings document.
func CompileClaude(existing []byte, cfg model.ReifuConfig, previous model.TargetState, targetOS string, noHooks bool) ([]byte, model.TargetState, error) {
	sandboxOn := ClaudeSandboxEnabled(cfg, targetOS)
	applied := model.TargetState{
		AppliedAllowCommands: slices.Clone(cfg.Permissions.AllowCommands),
		AppliedDenyCommands:  slices.Clone(cfg.Permissions.DenyCommands),
		AppliedDenyPaths:     slices.Clone(cfg.Permissions.DenyPaths),
		AppliedVerifyCommand: ResolveVerifyCommand(cfg.Hooks, targetOS, noHooks),
		AppliedOSDenyRules:   OSDenyRules(targetOS),
		AppliedSandbox:       &sandboxOn,
	}
	return compileJSON(existing, previous, applied, claudeSettings)
}

type jsonSettings struct {
	section, allow, deny, tool, event string
	context, claudeSandbox            bool
	geminiSandbox                     bool
	normalizeWriteDeny                bool
}

var claudeSettings = jsonSettings{section: "permissions", allow: "allow", deny: "deny", tool: "Bash", event: "Stop", claudeSandbox: true, normalizeWriteDeny: true}

func compileJSON(existing []byte, previous, applied model.TargetState, format jsonSettings) ([]byte, model.TargetState, error) {
	doc := make(map[string]any)
	if existing != nil {
		existing = bytes.TrimPrefix(existing, []byte("\xef\xbb\xbf"))
		if !json.Valid(existing) {
			return nil, model.TargetState{}, fmt.Errorf("invalid settings JSON")
		}
		decoder := json.NewDecoder(bytes.NewReader(existing))
		decoder.UseNumber()
		if err := decoder.Decode(&doc); err != nil {
			return nil, model.TargetState{}, fmt.Errorf("decode settings: %w", err)
		}
		if doc == nil {
			return nil, model.TargetState{}, fmt.Errorf("settings must be an object")
		}
	}
	section, err := settingsObject(doc, format.section)
	if err != nil {
		return nil, model.TargetState{}, err
	}
	err = mergeRules(section, format.allow, wrapRules(format.tool, previous.AppliedAllowCommands), wrapRules(format.tool, applied.AppliedAllowCommands), nil)
	if err != nil {
		return nil, model.TargetState{}, err
	}
	deny := func(state model.TargetState, includeLegacyWrite bool) []string {
		rules := wrapRules(format.tool, state.AppliedDenyCommands)
		verbs := []string{"Read", "Edit"}
		if includeLegacyWrite {
			verbs = append(verbs, "Write")
		}
		for _, tool := range verbs {
			rules = append(rules, wrapRules(tool, state.AppliedDenyPaths)...)
		}
		rules = append(rules, state.AppliedOSDenyRules...)
		return rules
	}
	var normalizeDeny func(string) string
	if format.normalizeWriteDeny {
		normalizeDeny = normalizeClaudeWriteRule
	}
	if err := mergeRules(section, format.deny, deny(previous, true), deny(applied, false), normalizeDeny); err != nil {
		return nil, model.TargetState{}, err
	}
	if format.context {
		context, err := settingsObject(doc, "context")
		if err != nil {
			return nil, model.TargetState{}, err
		}
		context["fileName"] = "AGENTS.md"
	}
	if format.claudeSandbox {
		if err := mergeClaudeSandbox(doc, boolValue(previous.AppliedSandbox), boolValue(applied.AppliedSandbox)); err != nil {
			return nil, model.TargetState{}, err
		}
	}
	if format.geminiSandbox {
		mergeGeminiSandbox(section, boolValue(previous.AppliedSandbox), boolValue(applied.AppliedSandbox))
	}
	if err := mergeHook(doc, format.event, previous.AppliedVerifyCommand, applied.AppliedVerifyCommand); err != nil {
		return nil, model.TargetState{}, err
	}
	out, err := marshalJSON(doc)
	if err != nil {
		return nil, model.TargetState{}, fmt.Errorf("encode settings: %w", err)
	}
	return out, applied, nil
}

func marshalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func settingsObject(doc map[string]any, key string) (map[string]any, error) {
	value, exists := doc[key]
	if !exists {
		object := make(map[string]any)
		doc[key] = object
		return object, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return object, nil
}

func wrapRules(tool string, patterns []string) []string {
	rules := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		rules = append(rules, tool+"("+pattern+")")
	}
	return rules
}

// normalizeClaudeWriteRule rewrites Claude Code path-scoped Write(<path>) rules
// to Edit(<path>), because Claude Code's file permission checks only match Edit(...)
// for both Edit and Write tools. Bare "Write" tool rules are left unchanged.
func normalizeClaudeWriteRule(rule string) string {
	if strings.HasPrefix(rule, "Write(") && strings.HasSuffix(rule, ")") && len(rule) > len("Write()") {
		return "Edit" + rule[len("Write"):]
	}
	return rule
}

func mergeRules(object map[string]any, key string, previous, desired []string, normalize func(string) string) error {
	current := []any{}
	if value, exists := object[key]; exists {
		var ok bool
		current, ok = value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", key)
		}
	}
	seen := make(map[string]bool)
	merged := make([]any, 0, len(current)+len(desired))
	for _, value := range current {
		raw, ok := value.(string)
		if ok {
			rule := raw
			if normalize != nil {
				rule = normalize(raw)
			}
			wasManaged := slices.Contains(previous, raw) || slices.Contains(previous, rule)
			if seen[rule] || (wasManaged && !slices.Contains(desired, rule)) {
				continue
			}
			seen[rule] = true
			value = rule
		}
		merged = append(merged, value)
	}
	for _, rule := range desired {
		if !seen[rule] {
			merged = append(merged, rule)
			seen[rule] = true
		}
	}
	object[key] = merged
	return nil
}

func mergeHook(doc map[string]any, event, previous, desired string) error {
	if previous == "" && desired == "" {
		return nil
	}
	hadHooks := false
	if _, exists := doc["hooks"]; exists {
		hadHooks = true
	}
	hooks, err := settingsObject(doc, "hooks")
	if err != nil {
		return err
	}
	var groups []any
	if value, exists := hooks[event]; exists {
		var ok bool
		groups, ok = value.([]any)
		if !ok {
			return fmt.Errorf("hooks.%s must be an array", event)
		}
	}
	for i, value := range groups {
		group, _ := value.(map[string]any)
		entries, _ := group["hooks"].([]any)
		for j, value := range entries {
			entry, _ := value.(map[string]any)
			if previous == "" || entry["type"] != "command" || entry["command"] != previous {
				continue
			}
			switch {
			case desired != "":
				entry["command"] = desired
			case len(entries) == 1:
				groups = slices.Delete(groups, i, i+1)
			default:
				group["hooks"] = slices.Delete(entries, j, j+1)
			}
			if len(groups) == 0 {
				delete(hooks, event)
				if len(hooks) == 0 {
					delete(doc, "hooks")
				}
			} else {
				hooks[event] = groups
			}
			return nil
		}
	}
	if desired != "" {
		if hasHookCommand(groups, desired) {
			return nil
		}
		group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": desired}}}
		hooks[event] = append([]any{group}, groups...)
		return nil
	}
	if !hadHooks && len(hooks) == 0 {
		delete(doc, "hooks")
	}
	return nil
}

func hasHookCommand(groups []any, command string) bool {
	for _, value := range groups {
		group, _ := value.(map[string]any)
		entries, _ := group["hooks"].([]any)
		for _, value := range entries {
			entry, _ := value.(map[string]any)
			if entry["type"] == "command" && entry["command"] == command {
				return true
			}
		}
	}
	return false
}

func boolValue(ptr *bool) bool {
	return ptr != nil && *ptr
}

func mergeClaudeSandbox(doc map[string]any, previous, desired bool) error {
	if !previous && !desired {
		return nil
	}
	if desired {
		sandbox, err := settingsObject(doc, "sandbox")
		if err != nil {
			return err
		}
		sandbox["enabled"] = true
		return nil
	}
	if value, exists := doc["sandbox"]; exists {
		sandbox, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("sandbox must be an object")
		}
		delete(sandbox, "enabled")
		if len(sandbox) == 0 {
			delete(doc, "sandbox")
		}
	}
	return nil
}

func mergeGeminiSandbox(tools map[string]any, previous, desired bool) {
	if !previous && !desired {
		return
	}
	if desired {
		tools["sandbox"] = true
		return
	}
	delete(tools, "sandbox")
}
