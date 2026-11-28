package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/withnic/reifu/internal/fsx"
	"github.com/withnic/reifu/internal/model"
)

const (
	runsDir    = ".reifu/runs"
	checkpoint = ".reifu.run.json"
)

func projectPath(root, rel string) (string, error) {
	if err := fsx.ValidateWithinRoot(root, rel); err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}

func openRead(root, rel string) (*os.File, error) {
	path, err := projectPath(root, rel)
	if err != nil {
		return nil, err
	}
	return fsx.OpenRegular(path)
}

func readText(root, rel string) ([]byte, error) {
	file, err := openRead(root, rel)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	return data, errors.Join(err, file.Close())
}

func loadState(root, rel string) (model.RunState, error) {
	var run model.RunState
	data, err := readText(root, rel)
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal(data, &run); err != nil {
		return run, fmt.Errorf("decode %q: %w", rel, err)
	}
	if err := validateRunState(run); err != nil {
		return run, fmt.Errorf("validate %q: %w", rel, err)
	}
	if rel != checkpoint && rel != run.RunDir+"/run.json" {
		return run, fmt.Errorf("run ID does not match %q", rel)
	}
	return run, nil
}

func validateRunState(run model.RunState) error {
	if err := model.ValidateRunID(run.RunID); err != nil {
		return err
	}
	if run.Version != 1 || len(run.Phases) == 0 || run.RunDir != runsDir+"/"+run.RunID {
		return fmt.Errorf("invalid metadata for run %q", run.RunID)
	}
	seen := make(map[string]bool, len(run.Phases))
	for _, phase := range run.Phases {
		expected, err := phaseRecord(run.RunDir, phase.Name+".md")
		key := strings.ToLower(phase.Name)
		if err != nil || seen[key] || phase.File != expected.File || !validPhaseArtifacts(run.RunDir, phase) {
			return fmt.Errorf("invalid phase %q in run %q", phase.Name, run.RunID)
		}
		seen[key] = true
		switch phase.Status {
		case model.PhasePending, model.PhaseImplementing, model.PhaseImplemented, model.PhaseReviewing, model.PhaseCompleted, model.PhaseFailed, model.PhaseBlocked:
		default:
			return fmt.Errorf("invalid phase status %q in run %q", phase.Status, run.RunID)
		}
	}
	return nil
}

func validPhaseArtifacts(runDir string, phase model.PhaseRunRecord) bool {
	dir, name := runDir, phase.Name
	if phase.Status == model.PhaseCompleted {
		// Completed phases may refer to one earlier run, including after a case-only rename.
		rel, ok := strings.CutPrefix(phase.ImplementLog, runsDir+"/")
		id, file, hasFile := strings.Cut(rel, "/")
		stem, hasSuffix := strings.CutSuffix(file, "_implement.log")
		if !ok || !hasFile || !hasSuffix || model.ValidateRunID(id) != nil ||
			strings.ToLower(stem) != strings.ToLower(name) || strings.ContainsAny(stem, "/\\") {
			return false
		}
		dir, name = runsDir+"/"+id, stem
	}
	return phase.ImplementLog == dir+"/"+name+"_implement.log" &&
		phase.ReviewReport == dir+"/"+name+"_review.md" &&
		(phase.CommandsLog == "" || phase.CommandsLog == dir+"/"+name+"_commands.json")
}
func ListRuns(root string) ([]model.RunSummary, error) {
	summaries := []model.RunSummary{}
	path, err := projectPath(root, runsDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return summaries, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if model.ValidateRunID(entry.Name()) != nil {
			continue
		}
		run, err := LoadRun(root, entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue // The runner may still be creating its initial snapshot.
		}
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, run.Summary())
	}
	slices.SortFunc(summaries, func(a, b model.RunSummary) int {
		if order := b.StartedAt.Compare(a.StartedAt); order != 0 {
			return order
		}
		return strings.Compare(b.RunID, a.RunID)
	})
	return summaries, nil
}

func LoadRun(root, runID string) (model.RunState, error) {
	if runID != "" {
		if err := model.ValidateRunID(runID); err != nil {
			return model.RunState{}, err
		}
		return loadState(root, runsDir+"/"+runID+"/run.json")
	}
	run, err := loadState(root, checkpoint)
	if !errors.Is(err, os.ErrNotExist) {
		return run, err
	}
	runs, err := ListRuns(root)
	if err != nil {
		return run, err
	}
	if len(runs) == 0 {
		return run, fmt.Errorf("no run history in %q; start a run with reifu run: %w", root, os.ErrNotExist)
	}
	return LoadRun(root, runs[0].RunID)
}

func phaseIndex(run model.RunState, name string) (int, error) {
	for i, phase := range run.Phases {
		if name == phase.Name || name == phase.Name+".md" {
			return i, nil
		}
	}
	return 0, fmt.Errorf("unknown phase %q", name)
}

func latestPhase(run model.RunState) int {
	for i := len(run.Phases) - 1; i >= 0; i-- {
		if run.Phases[i].Status != model.PhasePending {
			return i
		}
	}
	return 0
}

func ReadPhaseLog(root string, run model.RunState, phaseName string) ([]byte, error) {
	if err := validateRunState(run); err != nil {
		return nil, err
	}
	i := latestPhase(run)
	if phaseName != "" {
		var err error
		i, err = phaseIndex(run, phaseName)
		if err != nil {
			return nil, err
		}
	}
	return readText(root, run.Phases[i].ImplementLog)
}

func ReadPhaseCommands(root string, run model.RunState, phaseName string) (model.PhaseCommandsLog, error) {
	if err := validateRunState(run); err != nil {
		return model.PhaseCommandsLog{}, err
	}
	i := latestPhase(run)
	if phaseName != "" {
		var err error
		i, err = phaseIndex(run, phaseName)
		if err != nil {
			return model.PhaseCommandsLog{}, err
		}
	}
	rel := run.Phases[i].CommandsLog
	if rel == "" {
		rel = run.RunDir + "/" + run.Phases[i].Name + "_commands.json"
	}
	data, err := readText(root, rel)
	if err != nil {
		return model.PhaseCommandsLog{}, err
	}
	var log model.PhaseCommandsLog
	if err := json.Unmarshal(data, &log); err != nil {
		return model.PhaseCommandsLog{}, fmt.Errorf("decode %q: %w", rel, err)
	}
	return log, nil
}

func ReadReviews(root string, run model.RunState, phaseName string) ([]byte, error) {
	if err := validateRunState(run); err != nil {
		return nil, err
	}
	if phaseName != "" {
		i, err := phaseIndex(run, phaseName)
		if err != nil {
			return nil, err
		}
		return readText(root, run.Phases[i].ReviewReport)
	}
	var reports []byte
	for _, phase := range run.Phases {
		body, err := readText(root, phase.ReviewReport)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(reports) > 0 {
			reports = append(reports, '\n')
		}
		reports = append(reports, body...)
	}
	return reports, nil
}

func readLogTail(root, rel string, offset int64) ([]byte, error) {
	file, err := openRead(root, rel)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	data, err := io.ReadAll(file)
	return data, errors.Join(err, file.Close())
}

func FollowLog(ctx context.Context, root string, run model.RunState, phaseName string, pollInterval time.Duration, out io.Writer) error {
	if err := model.ValidateRunID(run.RunID); err != nil {
		return err
	}
	if pollInterval <= 0 {
		pollInterval = 200 * time.Millisecond
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	offsets := make(map[string]int64)
	first := -1
	for {
		current, err := LoadRun(root, run.RunID)
		if errors.Is(err, os.ErrNotExist) && first == -1 && len(run.Phases) > 0 {
			// The latest checkpoint can be visible before its initial snapshot.
			current, err = loadState(root, checkpoint)
			if err == nil && current.RunID != run.RunID {
				return fmt.Errorf("snapshot for run %q is missing", run.RunID)
			}
		}
		if err != nil {
			return err
		}
		last := latestPhase(current)
		if phaseName != "" {
			last, err = phaseIndex(current, phaseName)
			if err != nil {
				return err
			}
			first = last
		} else if first == -1 {
			first = last
		}
		for _, phase := range current.Phases[first : last+1] {
			path := phase.ImplementLog
			data, err := readLogTail(root, path, offsets[path])
			if err != nil {
				return err
			}
			if len(data) > 0 {
				n, err := out.Write(data)
				if err != nil {
					return err
				}
				if n != len(data) {
					return io.ErrShortWrite
				}
				offsets[path] += int64(n)
			}
		}
		if current.Summary().Status.Terminal() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
