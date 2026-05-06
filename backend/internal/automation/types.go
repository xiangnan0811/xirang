package automation

// Event types
const (
	EventAnomalyDetected = "anomaly_detected"
	EventBackupFailed    = "backup_failed"
	EventBackupSucceeded = "backup_succeeded"
	EventDrillFailed     = "drill_failed"
	EventNodeOffline     = "node_offline"
	EventNodeDiskHigh    = "node_disk_high"
)

// Action types
const (
	ActionPausePolicy      = "pause_policy"
	ActionDisablePolicy    = "disable_policy"
	ActionTriggerTask      = "trigger_task"
	ActionSendNotification = "send_notification"
)

// Result values for AutomationRuleLog.Result
const (
	ResultSuccess = "success"
	ResultError   = "error"
)

// ValidEventTypes is the set of all known event types.
var ValidEventTypes = map[string]bool{
	EventAnomalyDetected: true,
	EventBackupFailed:    true,
	EventBackupSucceeded: true,
	EventDrillFailed:     true,
	EventNodeOffline:     true,
	EventNodeDiskHigh:    true,
}

// ValidActionTypes is the set of all known action types.
var ValidActionTypes = map[string]bool{
	ActionPausePolicy:      true,
	ActionDisablePolicy:    true,
	ActionTriggerTask:      true,
	ActionSendNotification: true,
}

// Event is the context passed to the dispatcher when an event occurs.
type Event struct {
	Type    string                 // event_type
	Context map[string]interface{} // event-specific data (policy_id, task_id, node_id, severity, metric, etc.)
}
