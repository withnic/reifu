package syncer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/withnic/reifu/internal/jsonc"
	"github.com/withnic/reifu/internal/model"
)

type importSource struct {
	agent model.AgentName
	path  string
	data  []byte
}

func importCommands(master []byte, cfg model.ReifuConfig, previous *model.ReifuState, sources []importSource, force bool) ([]byte, []string, error) {
	var additions []string
	var rejected []string
	seen := make(map[string]bool)
	for _, cmd := range cfg.Permissions.AllowCommands {
		seen[cmd] = true
	}
	if previous != nil {
		for _, cmd := range previous.ManagedCommands {
			seen[cmd] = true
		}
	}
	for _, source := range sources {
		patterns, err := extractCommands(source)
		if err != nil {
			return nil, nil, fmt.Errorf("read allow rules from %s: %w", source.path, err)
		}
		baseline := model.TargetState{}
		if previous != nil {
			baseline = previous.Targets[source.agent]
		}
		known := baseline.AppliedAllowCommands
		if observed := baseline.ObservedAllowCommands; observed != nil {
			known = *observed
		}
		for _, pattern := range patterns {
			if seen[pattern] || slices.Contains(known, pattern) {
				continue
			}
			seen[pattern] = true
			// Schema-invalid values cannot be written even with --force.
			if strings.TrimSpace(pattern) == "" || strings.ContainsFunc(pattern, unicode.IsControl) ||
				!force && (dangerousCommand(pattern) || conflictsWithDeny(pattern, cfg.Permissions.DenyCommands)) {
				rejected = append(rejected, fmt.Sprintf("%s: %q", source.path, pattern))
				continue
			}
			additions = append(additions, pattern)
		}
	}
	if len(rejected) > 0 {
		warnings := make([]string, len(rejected))
		for i, item := range rejected {
			warnings[i] = fmt.Sprintf("warning: refused unsafe or conflicting allow rule from %s", item)
		}
		return nil, warnings, &model.ExitError{Code: model.ExitDrift, Err: fmt.Errorf("refused unsafe or conflicting allow rules: %s", strings.Join(rejected, ", "))}
	}
	if len(additions) == 0 {
		return master, nil, nil
	}
	updated, err := jsonc.AppendAllowCommands(master, additions)
	return updated, nil, err
}

func extractCommands(source importSource) ([]string, error) {
	var section, key, prefix string
	switch source.agent {
	case model.AgentClaude:
		section, key, prefix = "permissions", "allow", "Bash("
	case model.AgentGemini:
		section, key, prefix = "tools", "allowed", "run_shell_command("
	default:
		return nil, nil
	}
	if source.data == nil {
		return nil, nil
	}
	// Decode only the managed section. Other top-level values can be scalars.
	var root map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimPrefix(source.data, []byte("\xef\xbb\xbf")), &root); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if value, ok := root[section]; ok {
		if err := json.Unmarshal(value, &fields); err != nil {
			return nil, err
		}
	}
	var rules []any
	if value, ok := fields[key]; ok {
		if err := json.Unmarshal(value, &rules); err != nil {
			return nil, err
		}
	}
	var patterns []string
	for _, rule := range rules {
		value, ok := rule.(string)
		if ok && strings.HasPrefix(value, prefix) && strings.HasSuffix(value, ")") {
			patterns = append(patterns, value[len(prefix):len(value)-1])
		}
	}
	return patterns, nil
}

func canonicalCommand(command string) string {
	return strings.Join(strings.Fields(strings.ToLower(command)), " ")
}

func conflictsWithDeny(command string, denied []string) bool {
	command = canonicalCommand(command)
	for _, deny := range denied {
		deny = canonicalCommand(deny)
		if command == deny || matchesWildcard(deny, command) || matchesWildcard(command, deny) {
			return true
		}
	}
	return false
}

func matchesWildcard(pattern, value string) bool {
	if prefix, ok := strings.CutSuffix(pattern, " *"); ok && value == prefix {
		return true
	}
	expression := regexp.QuoteMeta(pattern)
	expression = strings.NewReplacer("\\*", ".*", "\\?", ".").Replace(expression)
	matched, _ := regexp.MatchString("^"+expression+"$", value)
	return matched
}

func dangerousCommand(command string) bool {
	if strings.ContainsAny(command, ";&|<>\x60()") || strings.Contains(command, "$(") || strings.Contains(command, "${") {
		return true
	}
	normalized := canonicalCommand(command)
	fields := strings.Fields(normalized)
	if len(fields) == 0 {
		return true
	}
	executable := strings.Trim(fields[0], "\"'")
	executable = strings.ReplaceAll(executable, "\\", "/")
	executable = executable[strings.LastIndex(executable, "/")+1:]
	executable = strings.TrimSuffix(executable, ".exe")
	if executable == "" || strings.ContainsAny(executable, "*?") {
		return true
	}
	switch executable {
	case "sh", "bash", "zsh", "fish", "dash", "ksh", "pwsh", "powershell", "cmd",
		"curl", "wget", "iex", "iwr", "invoke-expression", "invoke-webrequest",
		"wscript", "cscript", "mshta",
		"sudo", "doas", "env", "xargs", "eval", "exec", "nohup", "command":
		return true
	case "git":
		sub, args := gitSubcommand(fields[1:])
		if sub == "" {
			return false
		}
		if sub == "push" || strings.HasPrefix(sub, "push*") || strings.ContainsAny(sub, "*?") {
			return true
		}
		if sub == "reset" {
			return slices.ContainsFunc(args, func(arg string) bool {
				return strings.HasPrefix(arg, "--hard") || arg == "*"
			})
		}
	case "rm":
		var recursive, force bool
		for _, option := range fields[1:] {
			if strings.HasPrefix(option, "--") {
				recursive = recursive || strings.HasPrefix(option, "--recursive")
				force = force || strings.HasPrefix(option, "--force")
			} else if strings.HasPrefix(option, "-") {
				recursive = recursive || strings.Contains(option, "r")
				force = force || strings.Contains(option, "f")
			}
		}
		return recursive && force
	}
	return false
}

// gitSubcommand skips leading git global flags (such as -C <dir> or -c k=v)
// so wrapper flags cannot bypass subcommand checks.
func gitSubcommand(args []string) (string, []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			return arg, args[i+1:]
		}
		switch arg {
		case "-c", "--config-env", "--exec-path", "--git-dir", "--work-tree", "--namespace", "--super-prefix":
			i++
		}
	}
	return "", nil
}
