package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

// ResticOperation is the complete, bounded ledger of commands that must hold
// a generation-admission token before touching credentials, SSH, or Provider
// handles. New Restic paths must add a value here before they can be admitted.
type ResticOperation string

const (
	OperationLegacyBackup          ResticOperation = "legacy_backup"
	OperationLegacySnapshotList    ResticOperation = "legacy_snapshot_list"
	OperationLegacySnapshotFiles   ResticOperation = "legacy_snapshot_files"
	OperationLegacyIndex           ResticOperation = "legacy_index"
	OperationLegacySearch          ResticOperation = "legacy_search"
	OperationLegacyDiff            ResticOperation = "legacy_diff"
	OperationLegacySnapshotRestore ResticOperation = "legacy_snapshot_restore"
	OperationLegacyRestoreLatest   ResticOperation = "legacy_restore_latest"
	OperationLegacyAnomaly         ResticOperation = "legacy_anomaly"
	OperationLegacyRetention       ResticOperation = "legacy_retention"
	OperationLegacyIntegrity       ResticOperation = "legacy_integrity"
	OperationEvidenceBackup        ResticOperation = "evidence_backup"
	OperationManifest              ResticOperation = "manifest"
	OperationReconcile             ResticOperation = "reconcile"
	OperationManagedRsyncPointRead ResticOperation = "managed_rsync_point_read"
)

var validResticOperations = map[ResticOperation]struct{}{
	OperationLegacyBackup:          {},
	OperationLegacySnapshotList:    {},
	OperationLegacySnapshotFiles:   {},
	OperationLegacyIndex:           {},
	OperationLegacySearch:          {},
	OperationLegacyDiff:            {},
	OperationLegacySnapshotRestore: {},
	OperationLegacyRestoreLatest:   {},
	OperationLegacyAnomaly:         {},
	OperationLegacyRetention:       {},
	OperationLegacyIntegrity:       {},
	OperationEvidenceBackup:        {},
	OperationManifest:              {},
	OperationReconcile:             {},
	OperationManagedRsyncPointRead: {},
}

func ValidateResticOperation(value ResticOperation) error {
	if _, ok := validResticOperations[value]; !ok {
		return fmt.Errorf("%w: unknown Restic operation", backupasset.ErrInvalidState)
	}
	return nil
}

type AdmissionMode string

const (
	AdmissionPristineLegacy AdmissionMode = "pristine_legacy"
	AdmissionManaged        AdmissionMode = "managed"
	AdmissionRollbackSafe   AdmissionMode = "rollback_safe"
)

func ValidateAdmissionMode(value AdmissionMode) error {
	switch value {
	case AdmissionPristineLegacy, AdmissionManaged, AdmissionRollbackSafe:
		return nil
	default:
		return fmt.Errorf("%w: unknown admission mode", backupasset.ErrInvalidState)
	}
}

type AdmissionToken interface {
	Generation() uint64
	Mode() AdmissionMode
	Operation() ResticOperation
	Close() error
}

type Admission interface {
	Acquire(context.Context, ResticOperation) (AdmissionToken, error)
}

type FeatureTransitioner interface {
	TransitionFeature(context.Context, bool, func() error) error
	PrepareApplicationDowngrade(context.Context, func() error) error
	PrepareSchemaDown(context.Context, func() error) error
}

type LineageMode string

const (
	LineageCompatibility LineageMode = "compatibility"
	LineageExact         LineageMode = "exact"
)

type CommittedPoint struct {
	RecoveryPointID string
	FullNativeID    string `json:"-"`
	CapturedAt      time.Time
}

type LineageSession interface {
	Mode() LineageMode
	RepositoryID() string
	LinkTag() string
	CommittedPoints() []CommittedPoint
	ResolveNativeID(string) (string, error)
	CurrentAndPrevious(currentFullNativeID string) (CommittedPoint, *CommittedPoint, error)
	ListEntries(context.Context, string, provider.EntryLocator, provider.PageRequest) (provider.EntryPage, error)
	Close() error
}

type LineageGuard interface {
	Begin(context.Context, uint, ResticOperation) (LineageSession, error)
}

type PublicationStage string

const (
	StageExecution      PublicationStage = "execution"
	StageManifest       PublicationStage = "manifest"
	StageReconciliation PublicationStage = "reconciliation"
)

func ValidatePublicationStage(value PublicationStage) error {
	switch value {
	case StageExecution, StageManifest, StageReconciliation:
		return nil
	default:
		return fmt.Errorf("%w: unknown publication stage", backupasset.ErrInvalidState)
	}
}

type ReconcileMatchClass string

const (
	ReconcileMatchZero      ReconcileMatchClass = "zero"
	ReconcileMatchExact     ReconcileMatchClass = "exact"
	ReconcileMatchMultiple  ReconcileMatchClass = "multiple"
	ReconcileMatchRewritten ReconcileMatchClass = "rewritten"
	ReconcileMatchConflict  ReconcileMatchClass = "conflict"
	ReconcileMatchTransient ReconcileMatchClass = "transient"
)

func ValidateReconcileMatchClass(value ReconcileMatchClass) error {
	switch value {
	case ReconcileMatchZero, ReconcileMatchExact, ReconcileMatchMultiple, ReconcileMatchRewritten, ReconcileMatchConflict, ReconcileMatchTransient:
		return nil
	default:
		return fmt.Errorf("%w: unknown reconciliation match class", backupasset.ErrInvalidState)
	}
}

type ManifestLimitClass string

const (
	ManifestLimitNone     ManifestLimitClass = "none"
	ManifestLimitTimeout  ManifestLimitClass = "timeout"
	ManifestLimitBytes    ManifestLimitClass = "bytes"
	ManifestLimitEntries  ManifestLimitClass = "entries"
	ManifestLimitRecord   ManifestLimitClass = "record"
	ManifestLimitDepth    ManifestLimitClass = "depth"
	ManifestLimitProtocol ManifestLimitClass = "protocol"
)

func ValidateManifestLimitClass(value ManifestLimitClass) error {
	switch value {
	case ManifestLimitNone, ManifestLimitTimeout, ManifestLimitBytes, ManifestLimitEntries, ManifestLimitRecord, ManifestLimitDepth, ManifestLimitProtocol:
		return nil
	default:
		return fmt.Errorf("%w: unknown manifest limit class", backupasset.ErrInvalidState)
	}
}

type Metrics interface {
	ObserveAttempt(backupasset.ProviderKind, PublicationStage)
	ObserveOutcome(backupasset.ProviderKind, PublicationStage, backupasset.PublicationOutcomeCode)
	SetBacklog(backupasset.RecoveryPointState, int, time.Duration)
	ObserveReconcileMatch(ReconcileMatchClass)
	ObserveFenceLoss(PublicationStage)
	ObserveManifest(time.Duration, int64, int64, backupasset.ManifestCompleteness, ManifestLimitClass)
	ObserveLegacyBlocked(ResticOperation)
	ObserveAuditFailure(PublicationStage)
}

type ExecutionMode string

const (
	ModeCompatibility ExecutionMode = "compatibility"
	ModeEvidence      ExecutionMode = "evidence"
)

type Run struct {
	Task       model.Task
	TaskRunID  uint
	Trigger    string
	ChainRunID string
	StartedAt  time.Time
	Audit      backupasset.PublicationAuditContext
	// ImportedBaseline is an internal migration-only signal. It is never
	// populated from an API request and makes the prepared Rsync point use
	// imported_baseline semantics with full-copy mechanics.
	ImportedBaseline bool
}

type Deferral struct {
	Completion backupasset.ProviderCompletionClass
	Code       backupasset.PublicationFailureCode
}

type Outcome struct {
	RepositoryID           string
	RecoveryPointID        string
	TaskID                 uint
	TaskRunID              uint
	State                  backupasset.RecoveryPointState
	NativePointID          string `json:"-"`
	PreviousNativePointID  string `json:"-"`
	CapturedAt             time.Time
	ProviderCommitRecorded bool
	Code                   backupasset.PublicationFailureCode
}

type Coordinator interface {
	Prepare(context.Context, Run) (Execution, error)
}

type Execution interface {
	Mode() ExecutionMode
	Attempt() *provider.TaggedPublicationAttempt
	Context() context.Context
	Cancel(error) error
	Abandon(error) error
	CompleteCompatibility(context.Context) error
	RecordProviderCommit(context.Context, provider.ProviderCommit) (Outcome, error)
	Defer(context.Context, Deferral) error
	Reject(context.Context, backupasset.PublicationFailureCode) error
	Fail(context.Context, backupasset.PublicationFailureCode) error
}

type Reconciler interface {
	ListCandidates(context.Context, int) ([]string, error)
	ProcessPoint(context.Context, string) (Outcome, error)
	HasUnresolvedPublication(context.Context) (bool, error)
}

type CommitObserver interface {
	ObserveCommitted(context.Context, Outcome)
}

type InterruptedRunReporter interface {
	ReportInterruptedPublication(context.Context, Outcome) error
}

type InterruptedRunReadiness interface {
	ReconcileInterruptedRuns(context.Context, int) (bool, error)
}

type LegacyBlock struct {
	TaskID    uint
	TaskRunID *uint
	Operation ResticOperation
	Audit     backupasset.PublicationAuditContext
}

type LegacyBlockRecorder interface {
	RecordLegacyBlock(context.Context, LegacyBlock) error
}

// NewSystemLegacyBlockAuditContext derives a bounded opaque correlation ID for
// the one audit event that is allowed to carry a validated legacy operation.
func NewSystemLegacyBlockAuditContext(taskID uint, taskRunID *uint, operation ResticOperation) (backupasset.PublicationAuditContext, error) {
	if taskID == 0 || ValidateResticOperation(operation) != nil {
		return backupasset.PublicationAuditContext{}, fmt.Errorf("%w: invalid legacy block audit input", backupasset.ErrInvalidState)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("xirang.legacy-block.correlation.v1\x00"))
	_, _ = hash.Write([]byte(strconv.FormatUint(uint64(taskID), 10)))
	_, _ = hash.Write([]byte{'\x00'})
	if taskRunID != nil {
		if *taskRunID == 0 {
			return backupasset.PublicationAuditContext{}, fmt.Errorf("%w: invalid legacy block TaskRun ID", backupasset.ErrInvalidState)
		}
		_, _ = hash.Write([]byte(strconv.FormatUint(uint64(*taskRunID), 10)))
	}
	_, _ = hash.Write([]byte{'\x00'})
	_, _ = hash.Write([]byte(operation))
	sum := hash.Sum(nil)
	context, err := backupasset.NewSystemPublicationAuditContext("legblk-" + hex.EncodeToString(sum[:16]))
	if err != nil {
		return backupasset.PublicationAuditContext{}, err
	}
	return context, nil
}

// NoopMetrics is intentionally explicit: tests and future optional callers
// must inject a typed no-op rather than rely on a nil permissive fallback.
type NoopMetrics struct{}

func (NoopMetrics) ObserveAttempt(backupasset.ProviderKind, PublicationStage) {}
func (NoopMetrics) ObserveOutcome(backupasset.ProviderKind, PublicationStage, backupasset.PublicationOutcomeCode) {
}
func (NoopMetrics) SetBacklog(backupasset.RecoveryPointState, int, time.Duration) {}
func (NoopMetrics) ObserveReconcileMatch(ReconcileMatchClass)                     {}
func (NoopMetrics) ObserveFenceLoss(PublicationStage)                             {}
func (NoopMetrics) ObserveManifest(time.Duration, int64, int64, backupasset.ManifestCompleteness, ManifestLimitClass) {
}
func (NoopMetrics) ObserveLegacyBlocked(ResticOperation) {}
func (NoopMetrics) ObserveAuditFailure(PublicationStage) {}

var _ Metrics = NoopMetrics{}
