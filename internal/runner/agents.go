package runner

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/withnic/reifu/internal/model"
)

type AgentCommand struct {
	Name     string   `json:"name"`
	Argv     []string `json:"argv"`
	IsCustom bool     `json:"is_custom"`
}

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
		argv = []string{"agy", "--print", "--mode", "auto"}
	default:
		return AgentCommand{}, fmt.Errorf("unknown runner agent %q", name)
	}
	return AgentCommand{Name: normalized, Argv: argv}, nil
}

// lookPath resolves bare names via PATH and relative paths against the
// project root, which is also the agent's working directory.
func (a AgentCommand) lookPath(root string) (string, error) {
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
