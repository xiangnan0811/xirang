package cronbackup

import (
	"context"
	"errors"
	"time"
)

const (
	StateDirectoryEnv = "CRON_DB_BACKUP_STATE_DIR"
	MaxAgeEnv         = "CRON_DB_BACKUP_MAX_AGE_HOURS"

	maxStateBytes   = 16 * 1024
	maxReceiptBytes = 256
)

// Config is the on-disk cron backup state configuration. StateDirectory must
// be an absolute, private directory when it is non-empty. Engine is one of
// sqlite or postgres. MaxAge is the freshness threshold used by observations.
type Config struct {
	StateDirectory string
	Engine         string
	MaxAge         time.Duration
}

// JobStatus is the safe, externally consumable status of a cron backup job.
type JobStatus string

const (
	JobStatusNotConfigured        JobStatus = "not_configured"
	JobStatusInvalidConfiguration JobStatus = "invalid_configuration"
	JobStatusNotInitialized       JobStatus = "not_initialized"
	JobStatusStateUnavailable     JobStatus = "state_unavailable"
	JobStatusStateInvalid         JobStatus = "state_invalid"
	JobStatusClockAnomaly         JobStatus = "clock_anomaly"
	JobStatusNeverRun             JobStatus = "never_run"
	JobStatusRunning              JobStatus = "running"
	JobStatusOverdueRunning       JobStatus = "overdue_running"
	JobStatusInterrupted          JobStatus = "interrupted"
	JobStatusFailed               JobStatus = "failed"
	JobStatusSuccess              JobStatus = "success"
	JobStatusStale                JobStatus = "stale"
)

type AttemptResult string

const (
	AttemptRunning     AttemptResult = "running"
	AttemptSuccess     AttemptResult = "success"
	AttemptFailed      AttemptResult = "failed"
	AttemptInterrupted AttemptResult = "interrupted"
)

const (
	FailureBackupFailed       = "backup_failed"
	FailureBackupStartFailed  = "backup_start_failed"
	FailureResultInvalid      = "result_invalid"
	FailureProcessInterrupted = "process_interrupted"
)

// AttemptObservation contains only safe attempt evidence. It deliberately
// contains no command, path, DSN, output, or process error text.
type AttemptObservation struct {
	RunID        string        `json:"run_id"`
	StartedAt    time.Time     `json:"started_at"`
	Result       AttemptResult `json:"result"`
	FinishedAt   *time.Time    `json:"finished_at,omitempty"`
	DetectedAt   *time.Time    `json:"detected_at,omitempty"`
	FailureCode  string        `json:"failure_code,omitempty"`
	ArtifactName string        `json:"artifact_name,omitempty"`
}

// SuccessObservation is the safe projection of the most recent successful
// attempt.
type SuccessObservation struct {
	RunID        string    `json:"run_id"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	ArtifactName string    `json:"artifact_name"`
}

// JobObservation is safe to expose to an API or UI. Source identity,
// revision, lock paths, and raw filesystem errors are intentionally absent.
type JobObservation struct {
	Evidence      string              `json:"evidence"`
	Status        JobStatus           `json:"status"`
	CheckedAt     time.Time           `json:"checked_at"`
	MaxAgeSeconds int64               `json:"max_age_seconds"`
	LatestAttempt *AttemptObservation `json:"latest_attempt,omitempty"`
	LastSuccess   *SuccessObservation `json:"last_success,omitempty"`
}

// Observation combines the safe job projection with metadata needed by the
// alert worker. Metadata is private and can only be obtained through bounded
// accessors, so marshaling Observation cannot accidentally disclose it.
type Observation struct {
	Job JobObservation

	sourceID      string
	revision      int64
	initializedAt time.Time
	statePresent  bool
	stateValid    bool
	runLockHeld   bool
}

func (o Observation) SourceID() string             { return o.sourceID }
func (o Observation) Revision() int64              { return o.revision }
func (o Observation) InitializedAt() time.Time     { return o.initializedAt }
func (o Observation) StatePresent() bool           { return o.statePresent }
func (o Observation) StateValid() bool             { return o.stateValid }
func (o Observation) RunLockHeld() bool            { return o.runLockHeld }
func (o Observation) HasUsableState() bool         { return o.statePresent && o.stateValid }
func (o Observation) IsStateLockUnavailable() bool { return o.Job.Status == JobStatusStateUnavailable }

var (
	ErrNotConfigured        = errors.New("cronbackup: not configured")
	ErrInvalidConfiguration = errors.New("cronbackup: invalid configuration")
	ErrNotInitialized       = errors.New("cronbackup: not initialized")
	ErrStateUnavailable     = errors.New("cronbackup: state unavailable")
	ErrStateInvalid         = errors.New("cronbackup: state invalid")
	ErrAlreadyRunning       = errors.New("cronbackup: already running")
	ErrStatePublishFailed   = errors.New("cronbackup: state_publish_failed")
	ErrClockAnomaly         = errors.New("cronbackup: clock anomaly")
	ErrBackupFailed         = errors.New("cronbackup: backup_failed")
	ErrBackupStartFailed    = errors.New("cronbackup: backup_start_failed")
	ErrResultInvalid        = errors.New("cronbackup: result_invalid")
	ErrProcessInterrupted   = errors.New("cronbackup: process_interrupted")
)

// WithLockedObservation invokes callback while state.lock is held. The state
// file and run.lock are freshly observed before callback; the clock is sampled
// only after the state file has been read while the lock is held. A callback
// may safely open a database transaction and commit its decision without a
// state-file writer interleaving. Failure to acquire state.lock returns
// ErrStateUnavailable and never invokes callback. Invalid state is instead
// delivered as an Observation with Job.Status=state_invalid, allowing the
// alert worker to distinguish the two cases.
func WithLockedObservation(ctx context.Context, cfg Config, clock func() time.Time, callback func(Observation) error) error {
	return withLockedObservation(ctx, cfg, clock, callback, stateLockWorkerTimeout)
}

const (
	stateLockReadTimeout   = 100 * time.Millisecond
	stateLockWorkerTimeout = time.Second
)
