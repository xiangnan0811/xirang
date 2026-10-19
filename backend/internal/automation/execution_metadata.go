package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"

	"gorm.io/gorm"
)

// SafeExecutionMetadataVersion identifies the bounded, allowlisted Details object.
const SafeExecutionMetadataVersion = 1

const (
	ExecutionStageUnknown              = "unknown"
	ExecutionStageActionValidation     = "action_validation"
	ExecutionStageActionExecution      = "action_execution"
	ExecutionStagePolicyUpdated        = "policy_updated"
	ExecutionStageTaskTriggerAccepted  = "task_trigger_accepted"
	ExecutionStageTaskRunReserved      = "task_run_reserved"
	ExecutionStageNotificationRecorded = "notification_recorded"
)

const (
	ReasonInvalidTarget                    = "INVALID_TARGET"
	ReasonExecutorUnavailable              = "EXECUTOR_UNAVAILABLE"
	ReasonTransactionalExecutorUnavailable = "TRANSACTIONAL_EXECUTOR_UNAVAILABLE"
	ReasonPolicyControllerUnavailable      = "POLICY_CONTROLLER_UNAVAILABLE"
	ReasonContextCanceled                  = "CONTEXT_CANCELED"
	ReasonDeadlineExceeded                 = "DEADLINE_EXCEEDED"
	ReasonTargetNotFound                   = "TARGET_NOT_FOUND"
	ReasonActionFailed                     = "ACTION_FAILED"
	ReasonUnsupportedAction                = "UNSUPPORTED_ACTION"
)

// SafeExecutionMetadata contains only producer-known boundaries and historical
// IDs. It is not an execution timeline or a snapshot of current resource state.
// Only this nested object is bounded to 1024 bytes; private Details are not.
type SafeExecutionMetadata struct {
	Version         int    `json:"version"`
	Stage           string `json:"stage"`
	ReasonCode      string `json:"reason_code,omitempty"`
	SourceNodeID    uint   `json:"source_node_id,omitempty"`
	SourcePolicyID  uint   `json:"source_policy_id,omitempty"`
	SourceTaskID    uint   `json:"source_task_id,omitempty"`
	SourceTaskRunID uint   `json:"source_task_run_id,omitempty"`
	TargetPolicyID  uint   `json:"target_policy_id,omitempty"`
}

// ReadSafeExecutionMetadata validates the nested safe_execution object without
// exposing private Details or inferring metadata from legacy top-level fields.
// A readable v1 object retains its version even if its stage/reason is unknown;
// associations are independently filtered by their event/action contracts.
func ReadSafeExecutionMetadata(details, eventType, actionType, result string) *SafeExecutionMetadata {
	var envelope struct {
		SafeExecution json.RawMessage `json:"safe_execution"`
	}
	if err := json.Unmarshal([]byte(details), &envelope); err != nil {
		return nil
	}
	raw := bytes.TrimSpace(envelope.SafeExecution)
	if len(raw) == 0 || len(raw) > 1024 || raw[0] != '{' {
		return nil
	}
	var fields struct {
		Version         json.RawMessage `json:"version"`
		Stage           json.RawMessage `json:"stage"`
		ReasonCode      json.RawMessage `json:"reason_code"`
		SourceNodeID    json.RawMessage `json:"source_node_id"`
		SourcePolicyID  json.RawMessage `json:"source_policy_id"`
		SourceTaskID    json.RawMessage `json:"source_task_id"`
		SourceTaskRunID json.RawMessage `json:"source_task_run_id"`
		TargetPolicyID  json.RawMessage `json:"target_policy_id"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil || string(fields.Version) != "1" {
		return nil
	}
	meta := &SafeExecutionMetadata{Version: SafeExecutionMetadataVersion, Stage: ExecutionStageUnknown}
	var stage, reason string
	stageValid := safeExecutionString(fields.Stage, &stage)
	reasonValid := len(fields.ReasonCode) == 0 || safeExecutionString(fields.ReasonCode, &reason)
	if stageValid && reasonValid && validSafeExecutionBoundary(stage, reason, actionType, result) {
		meta.Stage, meta.ReasonCode = stage, reason
	}

	switch eventType {
	case EventBackupSucceeded, EventBackupFailed:
		meta.SourceNodeID = safeExecutionID(fields.SourceNodeID)
		meta.SourcePolicyID = safeExecutionID(fields.SourcePolicyID)
		taskID, runID := safeExecutionID(fields.SourceTaskID), safeExecutionID(fields.SourceTaskRunID)
		if taskID != 0 && runID != 0 {
			meta.SourceTaskID, meta.SourceTaskRunID = taskID, runID
		}
	case EventAnomalyDetected:
		meta.SourceNodeID = safeExecutionID(fields.SourceNodeID)
	case EventDrillFailed:
		meta.SourcePolicyID = safeExecutionID(fields.SourcePolicyID)
	}
	if result == ResultSuccess && (actionType == ActionPausePolicy || actionType == ActionDisablePolicy) {
		meta.TargetPolicyID = safeExecutionID(fields.TargetPolicyID)
	}
	return meta
}

func safeExecutionString(raw json.RawMessage, out *string) bool {
	return len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, out) == nil
}

// safeExecutionID accepts only positive JSON decimal integers that remain exact
// in browser numbers. RawMessage avoids rounding fractions or oversized values.
func safeExecutionID(raw json.RawMessage) uint {
	if len(raw) == 0 || raw[0] < '1' || raw[0] > '9' {
		return 0
	}
	value, err := json.Number(raw).Int64()
	if err != nil || value <= 0 || value > 9007199254740991 || uint64(value) > uint64(^uint(0)) {
		return 0
	}
	return uint(value)
}

func validSafeExecutionBoundary(stage, reason, actionType, result string) bool {
	policyAction := actionType == ActionPausePolicy || actionType == ActionDisablePolicy
	if result == ResultSuccess && reason == "" {
		switch stage {
		case ExecutionStagePolicyUpdated:
			return policyAction
		case ExecutionStageTaskTriggerAccepted, ExecutionStageTaskRunReserved:
			return actionType == ActionTriggerTask
		case ExecutionStageNotificationRecorded:
			return actionType == ActionSendNotification
		}
	}
	if result != ResultError {
		return false
	}
	switch stage {
	case ExecutionStageActionValidation:
		switch reason {
		case ReasonInvalidTarget, ReasonExecutorUnavailable, ReasonTransactionalExecutorUnavailable, ReasonPolicyControllerUnavailable:
			return true
		case ReasonUnsupportedAction:
			return !ValidActionTypes[actionType]
		}
	case ExecutionStageActionExecution:
		if !policyAction && actionType != ActionTriggerTask {
			return false
		}
		switch reason {
		case ReasonContextCanceled, ReasonDeadlineExceeded, ReasonTargetNotFound, ReasonActionFailed:
			return true
		}
	}
	return false
}

// newSafeExecutionMetadata captures only associations the event producer knows.
// In particular, a drill's isolated run ID is not an exact Task/Run association.
func newSafeExecutionMetadata(event Event) *SafeExecutionMetadata {
	meta := &SafeExecutionMetadata{Version: SafeExecutionMetadataVersion}
	switch event.Type {
	case EventBackupSucceeded, EventBackupFailed:
		meta.SourceNodeID = safeExecutionContextID(event.Context["node_id"])
		meta.SourcePolicyID = safeExecutionContextID(event.Context["policy_id"])
		taskID := safeExecutionContextID(event.Context["task_id"])
		runID := safeExecutionContextID(event.Context["task_run_id"])
		if taskID != 0 && runID != 0 {
			meta.SourceTaskID, meta.SourceTaskRunID = taskID, runID
		}
	case EventAnomalyDetected:
		meta.SourceNodeID = safeExecutionContextID(event.Context["node_id"])
	case EventDrillFailed:
		meta.SourcePolicyID = safeExecutionContextID(event.Context["policy_id"])
	}
	return meta
}

func safeExecutionContextID(value interface{}) uint {
	var id uint64
	switch v := value.(type) {
	case uint:
		id = uint64(v)
	case uint64:
		id = v
	case int:
		if v <= 0 {
			return 0
		}
		id = uint64(v)
	case int64:
		if v <= 0 {
			return 0
		}
		id = uint64(v)
	case float64:
		if v <= 0 || v > 9007199254740991 || math.IsNaN(v) || math.Trunc(v) != v {
			return 0
		}
		id = uint64(v)
	case json.Number:
		raw := json.RawMessage(v)
		if !json.Valid(raw) {
			return 0
		}
		return safeExecutionID(raw)
	default:
		return 0
	}
	if id == 0 || id > 9007199254740991 || id > uint64(^uint(0)) {
		return 0
	}
	return uint(id)
}

// safeExecutionConfigTarget does not decide whether an action may execute.
// It only proves that its rendered, strictly integral target matches the ID
// already admitted by the legacy parser and passed to the controller.
func safeExecutionConfigTarget(config, key string, actual uint) uint {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(config), &fields); err != nil {
		return 0
	}
	raw := fields[key]
	id := safeExecutionID(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) != nil || value == "" {
			return 0
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return 0
			}
		}
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0
		}
		id = safeExecutionContextID(n)
	}
	if id != actual {
		return 0
	}
	return id
}

func setSafeExecutionValidation(meta *SafeExecutionMetadata, reason string) {
	meta.Stage, meta.ReasonCode = ExecutionStageActionValidation, reason
}

func setSafeExecutionFailure(meta *SafeExecutionMetadata, err error) {
	meta.Stage = ExecutionStageActionExecution
	switch {
	case errors.Is(err, context.Canceled):
		meta.ReasonCode = ReasonContextCanceled
	case errors.Is(err, context.DeadlineExceeded):
		meta.ReasonCode = ReasonDeadlineExceeded
	case errors.Is(err, gorm.ErrRecordNotFound):
		meta.ReasonCode = ReasonTargetNotFound
	default:
		meta.ReasonCode = ReasonActionFailed
	}
}
