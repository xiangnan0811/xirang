package model

import "time"

const (
	TaskCronOccurrenceStateQueued     = "queued"
	TaskCronOccurrenceStateDispatched = "dispatched"
	TaskCronOccurrenceStateSkipped    = "skipped"
	TaskCronOccurrenceStateCanceled   = "canceled"
)

// TaskCronOccurrence is the durable intent for one scheduler activation. A
// queued row has no executor slot yet; TaskRun is created only after policy
// admission succeeds. This keeps pending TaskRun quota semantics unchanged
// while ensuring a busy/quota gate cannot erase an occurrence.
type TaskCronOccurrence struct {
	ID                 uint       `gorm:"primaryKey" json:"-"`
	TaskID             uint       `gorm:"not null;index:idx_task_cron_occurrences_task_state,priority:1;uniqueIndex:idx_task_cron_occurrences_task_scheduled,priority:1" json:"task_id"`
	ScheduledAt        time.Time  `gorm:"column:scheduled_at;not null;uniqueIndex:idx_task_cron_occurrences_task_scheduled,priority:2" json:"scheduled_at"`
	State              string     `gorm:"size:16;not null;default:queued;index:idx_task_cron_occurrences_task_state,priority:2" json:"state"`
	TaskRunID          *uint      `gorm:"column:task_run_id;index" json:"-"`
	DispatchOwnerID    string     `gorm:"column:dispatch_owner_id;size:64;not null;default:''" json:"-"`
	DispatchLeaseUntil *time.Time `gorm:"column:dispatch_lease_until;index" json:"-"`
	Reason             string     `gorm:"type:text;not null;default:''" json:"reason,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}
