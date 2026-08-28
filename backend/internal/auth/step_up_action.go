package auth

import (
	"fmt"
	"time"
)

type StepUpAction string

const (
	StepUpActionSSHKeyExport           StepUpAction = "ssh_key.export"
	StepUpActionTerminalOpen           StepUpAction = "terminal.open"
	StepUpActionConfigImport           StepUpAction = "config.import"
	StepUpActionConfigExport           StepUpAction = "config.export"
	StepUpActionSnapshotRestore        StepUpAction = "snapshot.restore"
	StepUpActionTaskRestoreTrigger     StepUpAction = "task.restore_trigger"
	StepUpActionTaskManualTrigger      StepUpAction = "task.manual_trigger"
	StepUpActionTaskBatchTrigger       StepUpAction = "task.batch_trigger"
	StepUpActionBatchCommandCreate     StepUpAction = "batch_command.create"
	StepUpActionAssetSecretReveal      StepUpAction = "asset.secret_reveal"
	StepUpActionAssetDownload          StepUpAction = "asset.download"
	StepUpActionAssetExportCreate      StepUpAction = "asset.export_create"
	StepUpActionAssetExportDownload    StepUpAction = "asset.export_download"
	StepUpActionAssetRecover           StepUpAction = "asset.recover"
	StepUpActionRecoveryResultDownload StepUpAction = "recovery.result_download"
	StepUpActionRecoveryResultRetain   StepUpAction = "recovery.result_retain"
	StepUpActionRepositoryPurge        StepUpAction = "repository.purge"
	StepUpActionRetentionHoldRelease   StepUpAction = "retention.hold_release"
)

var stepUpActions = []StepUpAction{
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
	StepUpActionRetentionHoldRelease,
}

var validStepUpActions = func() map[StepUpAction]bool {
	result := make(map[StepUpAction]bool, len(stepUpActions))
	for _, action := range stepUpActions {
		result[action] = true
	}
	return result
}()

func AllStepUpActions() []StepUpAction {
	result := make([]StepUpAction, len(stepUpActions))
	copy(result, stepUpActions)
	return result
}

func IsValidStepUpAction(action StepUpAction) bool {
	return validStepUpActions[action]
}

func StepUpProofTTLForAction(action StepUpAction) time.Duration {
	if !IsValidStepUpAction(action) {
		return 0
	}
	if action == StepUpActionAssetSecretReveal {
		return 45 * time.Minute
	}
	return StepUpProofTTL
}

func ParseStepUpAction(value string) (StepUpAction, error) {
	action := StepUpAction(value)
	if !IsValidStepUpAction(action) {
		return "", fmt.Errorf("unknown step-up action")
	}
	return action, nil
}
