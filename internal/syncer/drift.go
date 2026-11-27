package syncer

import (
	"crypto/sha256"
	"fmt"

	"github.com/withnic/reifu/internal/model"
)

// SHA256 hashes the exact file bytes used for drift detection.
func SHA256(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// Detect classifies a target against the last successfully applied snapshot.
// A nil current file means absent. An empty but existing file is non-nil.
func Detect(state *model.ReifuState, agent model.AgentName, masterHash string, current []byte, directoryExists, create bool) model.TargetStatus {
	if !directoryExists && !create {
		return model.StatusSkipped
	}
	if state == nil {
		return model.StatusStateMissing
	}
	previous, tracked := state.Targets[agent]
	if !tracked {
		return model.StatusStateMissing
	}
	targetChanged := previous.Exists != (current != nil) ||
		current != nil && SHA256(current) != previous.FileSHA256
	masterChanged := masterHash != state.MasterSHA256
	switch {
	case targetChanged && masterChanged:
		return model.StatusConflict
	case targetChanged:
		return model.StatusDrifted
	case masterChanged:
		return model.StatusMasterUpdated
	default:
		return model.StatusInSync
	}
}
