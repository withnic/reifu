package runner

import (
	"context"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/withnic/reifu/internal/model"
)

// InspectStatus reports available runner information without requiring a runnable project.
func InspectStatus(ctx context.Context, root string, cfg model.ReifuConfig) model.RunnerStatus {
	status := model.RunnerStatus{Phases: []string{}}
	if path, err := projectPath(root, blockedReport); err == nil {
		_, err := os.Lstat(path)
		status.Blocked = err == nil
	}

	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if repo, err := IsGitRepo(gitCtx, root); err == nil && repo {
		status.GitRepo = true
		dirty, err := StatusPorcelain(gitCtx, root)
		status.GitDirty = err == nil && dirty != ""
	}
	cancel()

	if path, err := projectPath(root, phasesDir); err == nil {
		entries, _ := os.ReadDir(path)
		seen := make(map[string]bool)
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			phase, err := phaseRecord("", entry.Name())
			if err != nil {
				continue
			}
			key := strings.ToLower(phase.Name)
			if seen[key] {
				continue
			}
			if err := requireFile(root, phase.File); err != nil {
				continue
			}
			seen[key] = true
			status.Phases = append(status.Phases, phase.Name)
		}
		slices.Sort(status.Phases)
	}

	names := []string{"agy", "claude", "codex", "gemini"}
	names = slices.AppendSeq(names, maps.Keys(cfg.Runner.Agents))
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		agent := model.AgentStatus{Name: name}
		if command, err := ResolveAgent(cfg, name); err == nil {
			agent.IsCustom = command.IsCustom
			if executable, err := lookPath(root, command); err == nil {
				agent.Executable = executable
				agent.Available = true
			}
		}
		status.Agents = append(status.Agents, agent)
	}
	if run, err := LoadRun(root, ""); err == nil {
		summary := run.Summary()
		status.LatestRun = &summary
	}
	return status
}
