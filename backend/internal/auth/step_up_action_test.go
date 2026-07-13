package auth

import (
	"reflect"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestStepUpActionAllowlistContainsExactContract(t *testing.T) {
	want := []StepUpAction{
		StepUpActionSSHKeyExport,
		StepUpActionTerminalOpen,
		StepUpActionConfigImport,
		StepUpActionConfigExport,
		StepUpActionSnapshotRestore,
		StepUpActionTaskRestoreTrigger,
		StepUpActionTaskManualTrigger,
		StepUpActionTaskBatchTrigger,
		StepUpActionBatchCommandCreate,
		StepUpActionAssetSecretReveal,
		StepUpActionAssetDownload,
		StepUpActionAssetExportCreate,
		StepUpActionAssetExportDownload,
		StepUpActionAssetRecover,
		StepUpActionRecoveryResultDownload,
		StepUpActionRecoveryResultRetain,
		StepUpActionRepositoryPurge,
	}
	if !reflect.DeepEqual(AllStepUpActions(), want) {
		t.Fatalf("step-up action registry mismatch\n got: %v\nwant: %v", AllStepUpActions(), want)
	}
	seen := make(map[StepUpAction]bool, len(want))
	for _, action := range want {
		if seen[action] || !IsValidStepUpAction(action) {
			t.Fatalf("invalid or duplicate registered step-up action %q", action)
		}
		seen[action] = true
	}
	for _, invalid := range []StepUpAction{"", "step_up", "terminal", "task_command", "future.action"} {
		if IsValidStepUpAction(invalid) {
			t.Fatalf("unknown step-up action %q accepted", invalid)
		}
	}
}

func TestGenerateStepUpTokenRejectsUnknownAction(t *testing.T) {
	manager := NewJWTManager("FAKE_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	user := model.User{ID: 9, Username: "alice", Role: "admin", TokenVersion: 3}
	for _, action := range []StepUpAction{"", "future.action"} {
		if _, _, err := manager.GenerateStepUpToken(user, action); err == nil {
			t.Fatalf("GenerateStepUpToken accepted action %q", action)
		}
	}
}
