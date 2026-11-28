package runner

import (
	"testing"

	"github.com/withnic/reifu/internal/model"
)

func TestValidateRunStateCarriedArtifacts(t *testing.T) {
	const (
		current = "20261129T000000Z-0123456789abcdef"
		earlier = "20261128T000000Z-fedcba9876543210"
	)
	curDir, oldDir := runsDir+"/"+current, runsDir+"/"+earlier
	record := func(status model.PhaseStatus, dir, stem string) model.PhaseRunRecord {
		return model.PhaseRunRecord{
			Name: "01_first", File: phasesDir + "/01_first.md", Status: status,
			ImplementLog: dir + "/" + stem + "_implement.log", ReviewReport: dir + "/" + stem + "_review.md",
		}
	}
	cases := []struct {
		name  string
		phase model.PhaseRunRecord
		ok    bool
	}{
		{"pending in current run", record(model.PhasePending, curDir, "01_first"), true},
		{"completed in current run", record(model.PhaseCompleted, curDir, "01_first"), true},
		{"completed from earlier run", record(model.PhaseCompleted, oldDir, "01_first"), true},
		{"completed from earlier run after case rename", record(model.PhaseCompleted, oldDir, "01_FIRST"), true},
		{"pending from earlier run", record(model.PhasePending, oldDir, "01_first"), false},
		{"failed from earlier run", record(model.PhaseFailed, oldDir, "01_first"), false},
		{"completed with traversal run ID", record(model.PhaseCompleted, runsDir+"/..", "01_first"), false},
		{"completed outside runs dir", record(model.PhaseCompleted, ".reifu/other", "01_first"), false},
		{"completed with nested directory", record(model.PhaseCompleted, oldDir+"/x", "01_first"), false},
		{"completed with other phase name", record(model.PhaseCompleted, oldDir, "02_second"), false},
		{"completed with mismatched review run", func() model.PhaseRunRecord {
			p := record(model.PhaseCompleted, oldDir, "01_first")
			p.ReviewReport = curDir + "/01_first_review.md"
			return p
		}(), false},
		{"completed with commands log from earlier run", func() model.PhaseRunRecord {
			p := record(model.PhaseCompleted, oldDir, "01_first")
			p.CommandsLog = oldDir + "/01_first_commands.json"
			return p
		}(), true},
		{"completed with mismatched commands log", func() model.PhaseRunRecord {
			p := record(model.PhaseCompleted, oldDir, "01_first")
			p.CommandsLog = curDir + "/01_first_commands.json"
			return p
		}(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := model.RunState{Version: 1, RunID: current, RunDir: curDir, Phases: []model.PhaseRunRecord{tc.phase}}
			if err := validateRunState(run); (err == nil) != tc.ok {
				t.Fatalf("validateRunState(%+v) = %v, want ok=%v", tc.phase, err, tc.ok)
			}
		})
	}
}
