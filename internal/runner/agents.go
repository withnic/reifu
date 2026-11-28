package runner

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/withnic/reifu/internal/model"
)

type AgentCommand = model.AgentCommand

func ResolveAgent(cfg model.ReifuConfig, name string) (AgentCommand, error) {
	cfg.ApplyDefaults()
	if err := cfg.Runner.Validate(); err != nil {
		return AgentCommand{}, err
	}
	normalized := name
	if name == "antigravity" {
		normalized = "agy"
	}
	for _, key := range []string{name, normalized} {
		if argv, ok := cfg.Runner.Agents[key]; ok {
			return AgentCommand{Name: normalized, Argv: slices.Clone(argv), IsCustom: true}, nil
		}
	}
	var argv []string
	switch normalized {
	case "claude":
		argv = []string{"claude", "-p"}
	case "codex":
		argv = []string{"codex", "exec", "-s", string(cfg.Sandbox.Mode), "-"}
	case "gemini":
		argv = []string{"gemini", "-p", ""}
	case "agy":
		argv = []string{"agy", "--mode", "accept-edits", "--output-format", "stream-json", "--print=" + model.PromptPlaceholder}
	default:
		return AgentCommand{}, fmt.Errorf("unknown runner agent %q", name)
	}
	return AgentCommand{Name: normalized, Argv: argv}, nil
}

const (
	// maxInlineCommandUTF16Len is the threshold (in UTF-16 code units of the
	// Windows CreateProcessW command line) above which argument-mode prompts are
	// spilled to a file inside .reifu/runs/<run-id>/ instead of passed inline.
	// Keeping this well below Windows' 32,767-unit hard cap also stays safely
	// under Linux's 128 KiB MAX_ARG_STRLEN per argument.
	maxInlineCommandUTF16Len  = 24000
	maxWindowsCommandUTF16Len = 32767
)

func withAgySettings(a AgentCommand, settingsRel string) AgentCommand {
	if settingsRel == "" || len(a.Argv) == 0 {
		return a
	}
	argv := make([]string, 0, len(a.Argv)+2)
	if len(a.Argv) >= 2 && strings.Contains(a.Argv[len(a.Argv)-1], model.PromptPlaceholder) {
		argv = append(argv, a.Argv[:len(a.Argv)-1]...)
		argv = append(argv, "--settings", settingsRel, a.Argv[len(a.Argv)-1])
	} else {
		argv = append(argv, a.Argv...)
		argv = append(argv, "--settings", settingsRel)
	}
	a.Argv = argv
	return a
}

func usesPromptArg(argv []string) bool {
	for _, arg := range argv[1:] {
		if strings.Contains(arg, model.PromptPlaceholder) {
			return true
		}
	}
	return false
}

func expandPromptArgs(args []string, prompt string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = strings.ReplaceAll(arg, model.PromptPlaceholder, prompt)
	}
	return out
}

// commandLineUTF16Len returns the UTF-16 code unit length of the Windows
// CreateProcessW command line (including escaping, spaces, and trailing NUL).
func commandLineUTF16Len(executable string, args []string) int {
	total := utf16Len(escapeWindowsArg(executable))
	for _, arg := range args {
		total += 1 + utf16Len(escapeWindowsArg(arg))
	}
	return total + 1 // trailing NUL terminator
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func escapeWindowsArg(s string) string {
	if len(s) == 0 {
		return `""`
	}
	hasSpace := false
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\v' {
			hasSpace = true
			break
		}
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	if hasSpace {
		b.WriteByte('"')
	}
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			slashes++
		case '"':
			for range slashes*2 + 1 {
				b.WriteByte('\\')
			}
			slashes = 0
			b.WriteByte('"')
		default:
			for range slashes {
				b.WriteByte('\\')
			}
			slashes = 0
			b.WriteByte(s[i])
		}
	}
	if hasSpace {
		for range slashes * 2 {
			b.WriteByte('\\')
		}
		b.WriteByte('"')
	} else {
		for range slashes {
			b.WriteByte('\\')
		}
	}
	return b.String()
}

// lookPath resolves bare names via PATH and relative paths against the
// project root, which is also the agent's working directory.
func lookPath(root string, a AgentCommand) (string, error) {
	name := a.Argv[0]
	if strings.ContainsAny(name, `/\`) && !filepath.IsAbs(name) {
		name = filepath.Join(root, filepath.FromSlash(name))
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("runner agent %q executable %q: %w", a.Name, a.Argv[0], err)
	}
	return filepath.Abs(path)
}
