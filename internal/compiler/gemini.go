package compiler

import (
	"slices"

	"github.com/withnic/reifu/internal/model"
)

var geminiSettings = jsonSettings{section: "tools", allow: "allowed", deny: "exclude", tool: "run_shell_command", event: "AfterAgent", context: true, geminiSandbox: true}

// CompileGemini merges the desired configuration into a Gemini settings document.
func CompileGemini(existing []byte, cfg model.ReifuConfig, previous model.TargetState, targetOS string, noHooks bool) ([]byte, model.TargetState, error) {
	sandboxOn := GeminiSandboxEnabled(cfg, targetOS)
	applied := model.TargetState{
		AppliedAllowCommands: slices.Clone(cfg.Permissions.AllowCommands),
		AppliedDenyCommands:  slices.Clone(cfg.Permissions.DenyCommands),
		AppliedVerifyCommand: ResolveVerifyCommand(cfg.Hooks, targetOS, noHooks),
		AppliedSandbox:       &sandboxOn,
	}
	previous.AppliedDenyPaths = nil
	previous.AppliedOSDenyRules = nil
	return compileJSON(existing, previous, applied, geminiSettings)
}
