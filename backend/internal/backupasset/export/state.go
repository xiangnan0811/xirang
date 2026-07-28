package export

import "time"

type ExecutionState string

const (
	ExecutionQueued          ExecutionState = "queued"
	ExecutionRunning         ExecutionState = "running"
	ExecutionRetryWait       ExecutionState = "retry_wait"
	ExecutionSealing         ExecutionState = "sealing"
	ExecutionReady           ExecutionState = "ready"
	ExecutionCancelRequested ExecutionState = "cancel_requested"
	ExecutionFailed          ExecutionState = "failed"
	ExecutionSourceExpired   ExecutionState = "source_expired"
	ExecutionCanceled        ExecutionState = "canceled"
	ExecutionExpiring        ExecutionState = "expiring"
	ExecutionExpired         ExecutionState = "expired"
)

type CleanupState string

const (
	CleanupNone        CleanupState = "none"
	CleanupRevoking    CleanupState = "revoking"
	CleanupPurging     CleanupState = "purging"
	CleanupPurged      CleanupState = "purged"
	CleanupPurgeFailed CleanupState = "purge_failed"
)

type ItemState string

const (
	ItemPending ItemState = "pending"
	ItemRead    ItemState = "read"
	ItemPacked  ItemState = "packed"
	ItemSkipped ItemState = "skipped"
	ItemFailed  ItemState = "failed"
)

type AttemptState string

const (
	AttemptActive     AttemptState = "active"
	AttemptSealing    AttemptState = "sealing"
	AttemptSealed     AttemptState = "sealed"
	AttemptFailed     AttemptState = "failed"
	AttemptCanceled   AttemptState = "canceled"
	AttemptSuperseded AttemptState = "superseded"
)

type SourceDeadline struct {
	AbsoluteDeadline time.Time
	RetentionUntil   *time.Time
}

func ValidateExecutionTransition(from, to ExecutionState) error {
	if !validExecutionStates[from] || !validExecutionStates[to] {
		return ErrInvalidTransition
	}
	if from == to || executionTransitions[[2]ExecutionState{from, to}] {
		return nil
	}
	return ErrInvalidTransition
}

func ValidateCleanupTransition(from, to CleanupState) error {
	if !validCleanupStates[from] || !validCleanupStates[to] {
		return ErrInvalidTransition
	}
	if from == to || cleanupTransitions[[2]CleanupState{from, to}] {
		return nil
	}
	return ErrInvalidTransition
}

func ValidateItemTransition(from, to ItemState) error {
	if !validItemStates[from] || !validItemStates[to] {
		return ErrInvalidTransition
	}
	if from == to || itemTransitions[[2]ItemState{from, to}] {
		return nil
	}
	return ErrInvalidTransition
}

func ValidateItemReset(from ItemState, fenceValidated bool) error {
	if !validItemStates[from] || !fenceValidated {
		return ErrInvalidTransition
	}
	return nil
}

func ComputeExecutionDeadline(
	createdAt time.Time,
	maxDuration time.Duration,
	safeStartWindow time.Duration,
	sources []SourceDeadline,
) (time.Time, error) {
	createdAt = createdAt.UTC()
	if createdAt.IsZero() || maxDuration <= 0 || safeStartWindow <= 0 || len(sources) == 0 {
		return time.Time{}, ErrDeadlineUnsafe
	}
	capAt := createdAt.Add(maxDuration)
	for _, source := range sources {
		deadline := source.AbsoluteDeadline.UTC()
		if deadline.IsZero() {
			return time.Time{}, ErrDeadlineUnsafe
		}
		if deadline.Before(capAt) {
			capAt = deadline
		}
		if source.RetentionUntil != nil {
			retention := source.RetentionUntil.UTC()
			if retention.IsZero() {
				return time.Time{}, ErrDeadlineUnsafe
			}
			if retention.Before(capAt) {
				capAt = retention
			}
		}
	}
	if !capAt.After(createdAt.Add(safeStartWindow)) {
		return time.Time{}, ErrDeadlineUnsafe
	}
	return capAt, nil
}

func ComputeReadyExpiry(readyAt time.Time, readyTTL time.Duration, sources []SourceDeadline) (time.Time, error) {
	readyAt = readyAt.UTC()
	if readyAt.IsZero() || readyTTL <= 0 || len(sources) == 0 {
		return time.Time{}, ErrDeadlineUnsafe
	}
	expiresAt := readyAt.Add(readyTTL)
	for _, source := range sources {
		deadline := source.AbsoluteDeadline.UTC()
		if deadline.IsZero() {
			return time.Time{}, ErrDeadlineUnsafe
		}
		if deadline.Before(expiresAt) {
			expiresAt = deadline
		}
		if source.RetentionUntil != nil && source.RetentionUntil.UTC().Before(expiresAt) {
			expiresAt = source.RetentionUntil.UTC()
		}
	}
	if !expiresAt.After(readyAt) {
		return time.Time{}, ErrDeadlineUnsafe
	}
	return expiresAt, nil
}

var validExecutionStates = map[ExecutionState]bool{
	ExecutionQueued: true, ExecutionRunning: true, ExecutionRetryWait: true,
	ExecutionSealing: true, ExecutionReady: true, ExecutionCancelRequested: true,
	ExecutionFailed: true, ExecutionSourceExpired: true, ExecutionCanceled: true,
	ExecutionExpiring: true, ExecutionExpired: true,
}

var executionTransitions = map[[2]ExecutionState]bool{
	{ExecutionQueued, ExecutionRunning}: true, {ExecutionQueued, ExecutionCancelRequested}: true,
	{ExecutionQueued, ExecutionFailed}: true, {ExecutionQueued, ExecutionSourceExpired}: true,
	{ExecutionRunning, ExecutionRetryWait}: true, {ExecutionRunning, ExecutionSealing}: true,
	{ExecutionRunning, ExecutionCancelRequested}: true, {ExecutionRunning, ExecutionFailed}: true,
	{ExecutionRunning, ExecutionSourceExpired}: true, {ExecutionRetryWait, ExecutionRunning}: true,
	{ExecutionRetryWait, ExecutionCancelRequested}: true, {ExecutionRetryWait, ExecutionFailed}: true,
	{ExecutionRetryWait, ExecutionSourceExpired}: true, {ExecutionSealing, ExecutionReady}: true,
	{ExecutionSealing, ExecutionRetryWait}:       true,
	{ExecutionSealing, ExecutionCancelRequested}: true, {ExecutionSealing, ExecutionFailed}: true,
	{ExecutionSealing, ExecutionSourceExpired}: true, {ExecutionReady, ExecutionCancelRequested}: true,
	{ExecutionReady, ExecutionExpiring}: true, {ExecutionCancelRequested, ExecutionCanceled}: true,
	{ExecutionExpiring, ExecutionExpired}: true,
}

var validCleanupStates = map[CleanupState]bool{
	CleanupNone: true, CleanupRevoking: true, CleanupPurging: true,
	CleanupPurged: true, CleanupPurgeFailed: true,
}

var cleanupTransitions = map[[2]CleanupState]bool{
	{CleanupNone, CleanupRevoking}: true, {CleanupRevoking, CleanupPurging}: true,
	{CleanupRevoking, CleanupPurgeFailed}: true, {CleanupPurging, CleanupPurged}: true,
	{CleanupPurging, CleanupPurgeFailed}: true, {CleanupPurgeFailed, CleanupPurging}: true,
}

var validItemStates = map[ItemState]bool{
	ItemPending: true, ItemRead: true, ItemPacked: true, ItemSkipped: true, ItemFailed: true,
}

var itemTransitions = map[[2]ItemState]bool{
	{ItemPending, ItemRead}: true, {ItemPending, ItemSkipped}: true, {ItemPending, ItemFailed}: true,
	{ItemRead, ItemPacked}: true, {ItemRead, ItemSkipped}: true, {ItemRead, ItemFailed}: true,
}
