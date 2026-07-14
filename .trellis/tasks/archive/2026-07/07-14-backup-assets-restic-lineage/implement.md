# Restic Exact Lineage And Recovery-Point Publication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use `superpowers:executing-plans` in the current Codex inline workflow. Before product edits, load `trellis-before-dev` and `superpowers:test-driven-development`. Do not dispatch implement/check sub-agents. Track every execution step by changing its checkbox only after the stated command produces the stated result.

**Status:** Child 3 PRD and complete focused design were approved by the user on 2026-07-14 with no other objections. The user subsequently approved this focused plan and explicitly requested implementation on the same date; `task.py start` completed and Child 3 is `in_progress`. Product changes now remain constrained to this plan; commits, push, and PR remain deferred to their stated workflow gates.

**Goal:** Bind every asset-managed Restic backup run to the exact full snapshot created by its proven exit-zero command, asynchronously publish a fenced and fully manifested native `RecoveryPoint`, and prevent every legacy Restic surface from crossing Task lineage.

**Architecture:** A shared backup-asset runtime owns one generation-fenced Restic command-admission barrier, the existing Child 2 Provider transport, a Provider-neutral publication coordinator, and a durable publication worker. Task Manager retains TaskRun transfer truth; the coordinator alone owns RecoveryPoint/manifest/lease/audit transactions; `verifying` rows are the durable queue; legacy commands run only through a post-admission lineage session. No publication path guesses `latest`, scans by time, deletes Provider data, or waits for the manifest before completing the TaskRun.

**Tech Stack:** Go 1.26, GORM, SQLite/PostgreSQL paired `golang-migrate` SQL, Restic v0.19.1 JSON/JSONL, bounded SSH command streams, zerolog, Gin, existing settings/audit/lease services, `go test`, `golangci-lint`, repository `make check`.

---

## 0. Review Gate, Baseline, And Execution Rules

- Branch: `codex/backup-assets-restic-lineage`.
- Required base: `origin/main@e1a8f24c3c8b8b71581cedc148c5f32482c8ac0b` before implementation begins. If `origin/main` advances after plan approval, stop and rebase the branch, re-read the affected code/specs, and amend this plan before product edits.
- Existing dirty parent/Child 3 planning artifacts are reviewed user work. Preserve them byte-for-byte except for approved planning-consistency changes and normal Trellis lifecycle metadata.
- The user explicitly approved this file and asked to start implementation on 2026-07-14; the task is `in_progress`.
- Inline mode skips `implement.jsonl`/`check.jsonl` curation. Phase 2 loads task artifacts, research, and specs through `trellis-before-dev`.
- Apply the TDD iron law to every behavior change: add one focused failing test, run it and confirm the expected failure, add the smallest production change, rerun the focused test and affected package, then refactor only while green.
- Do not create commits during Tasks 1–10. Keep logical commit boundaries recorded below, run every final gate, then create the reviewed work commits together in workflow Phase 3.4. This user-approved ordering overrides the writing-plan skill's usual per-task commit cadence.
- Never run `restic forget`, `prune`, `delete`, `restore`, `init`, repository repair, or any Provider cleanup from publication, reconciliation, rollback, or tests. Legacy exact snapshot restore remains an existing guarded surface; the new publication Provider adapter never exposes it.

### Activation steps — execute only after explicit plan/start approval

- [x] **Step 0.1: Reconfirm branch, base, dirty-path ownership, and planning artifacts.**

```bash
git fetch origin --prune
git status --short --branch
git rev-parse HEAD
git rev-parse origin/main
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-14-backup-assets-restic-lineage
```

Expected: branch is `codex/backup-assets-restic-lineage`; the only pre-implementation changes are the reviewed parent/Child 3 planning files; both revisions are `e1a8f24c3c8b8b71581cedc148c5f32482c8ac0b`; both validations exit 0.

- [x] **Step 0.2: Activate only Child 3.**

```bash
python3 ./.trellis/scripts/task.py start 07-14-backup-assets-restic-lineage
python3 ./.trellis/scripts/get_context.py
```

Expected: Child 3 status becomes `in_progress`; the parent remains `planning`; no source or migration file changes.

- [x] **Step 0.3: Load implementation guidance before editing.**

Load `trellis-before-dev`, then read this task's `prd.md`, `design.md`, `implement.md`, `research/restic-lineage-evidence.md`, `.trellis/spec/backend/index.md`, the backend guides referenced below, and `backend/internal/api/handlers/AGENTS.md`. Load `superpowers:executing-plans` and `superpowers:test-driven-development` before the first red test.

Expected: the implementation session records that the following specs govern the work:

```text
.trellis/spec/backend/directory-structure.md
.trellis/spec/backend/database-guidelines.md
.trellis/spec/backend/error-handling.md
.trellis/spec/backend/quality-guidelines.md
.trellis/spec/backend/logging-guidelines.md
.trellis/spec/backend/deployment-runtime.md
.trellis/spec/guides/branch-workflow-guidelines.md
.trellis/spec/guides/code-reuse-thinking-guide.md
.trellis/spec/guides/cross-layer-thinking-guide.md
.trellis/spec/guides/documentation-truth-guide.md
```

- [x] **Step 0.4: Create only the three reviewed package/fixture directories that do not yet exist.**

```bash
mkdir -p \
  backend/internal/backupasset/publication \
  backend/internal/backupasset/runtime \
  backend/internal/backupasset/provider/testdata/restic
test -d backend/internal/backupasset/publication
test -d backend/internal/backupasset/runtime
test -d backend/internal/backupasset/provider/testdata/restic
git status --short --untracked-files=all
```

Expected: all directory checks pass; empty directories create no Git diff, and every subsequent Create path has an existing parent. No source, fixture, or migration file exists yet.

## 1. Locked Type And Signature Ledger

These names and signatures are the single source of truth for all later tasks. If compilation reveals a necessary signature change, update every definition, fake, call site, test, and this ledger in the same red-green cycle before continuing.

### 1.1 Domain publication values

Create `backend/internal/backupasset/publication.go` with these public shapes. JSON codecs in that file must reject unknown fields, trailing data, non-UTC persisted times, invalid code pairings, and unsafe/raw evidence.

```go
type ProviderCompletionClass string

const (
	CompletionKnownExitZero ProviderCompletionClass = "known_exit_zero"
	CompletionKnownNonzero  ProviderCompletionClass = "known_nonzero"
	CompletionOutcomeUnknown ProviderCompletionClass = "outcome_unknown"
)

type PublicationFailureCode string

const (
	FailurePublicationPreconditionMissing PublicationFailureCode = "publication_precondition_missing"
	FailurePublicationInProgress          PublicationFailureCode = "publication_in_progress"
	FailurePublicationSessionAbandoned    PublicationFailureCode = "publication_session_abandoned"
	FailureEvidenceMissingSummary         PublicationFailureCode = "evidence_missing_summary"
	FailureEvidenceMalformedStream        PublicationFailureCode = "evidence_malformed_stream"
	FailureEvidenceDuplicateSummary       PublicationFailureCode = "evidence_duplicate_summary"
	FailureEvidenceNonFinalSummary        PublicationFailureCode = "evidence_non_final_summary"
	FailureEvidenceInvalidNativeID        PublicationFailureCode = "evidence_invalid_native_id"
	FailureProviderNonzeroExit            PublicationFailureCode = "provider_nonzero_exit"
	FailureProviderTimeout                PublicationFailureCode = "provider_timeout"
	FailureProviderCanceled               PublicationFailureCode = "provider_canceled"
	FailureProviderResourceLimit          PublicationFailureCode = "provider_resource_limit"
	FailureProviderOutcomeUnknown         PublicationFailureCode = "provider_outcome_unknown"
	FailureProviderCompletionUnproven     PublicationFailureCode = "provider_completion_unproven"
	FailureProviderSnapshotRewritten      PublicationFailureCode = "provider_snapshot_rewritten"
	FailureRepositoryIdentityDrift        PublicationFailureCode = "repository_identity_drift"
	FailureRunTagMissing                  PublicationFailureCode = "run_tag_missing"
	FailureAmbiguousRunTags               PublicationFailureCode = "ambiguous_run_tags"
	FailureNativePointConflict            PublicationFailureCode = "native_point_conflict"
	FailureManifestPartial                PublicationFailureCode = "manifest_partial"
	FailureManifestUnavailable            PublicationFailureCode = "manifest_unavailable"
	FailureLeaseFenceLost                 PublicationFailureCode = "lease_fence_lost"
	FailurePublicationDeadlineExceeded    PublicationFailureCode = "publication_deadline_exceeded"
	FailureSnapshotMissingAtDeadline      PublicationFailureCode = "snapshot_missing_at_deadline"
	FailureLegacyFallbackBlocked          PublicationFailureCode = "legacy_fallback_blocked"
	FailureLegacyOperationBlocked         PublicationFailureCode = "legacy_operation_blocked"
)

type PublicationOutcomeCode string

const PublicationOutcomeSuccess PublicationOutcomeCode = "success"

func PublicationOutcomeFromFailure(PublicationFailureCode) (PublicationOutcomeCode, error)

type PublicationLineageV1 struct {
	Version                  int       `json:"version"`
	TaskRepositoryLinkID     string    `json:"task_repository_link_id"`
	TaskID                   uint      `json:"task_id"`
	TaskRunID                uint      `json:"task_run_id"`
	Trigger                  string    `json:"trigger"`
	ChainRunIDPresent        bool      `json:"chain_run_id_present"`
	ChainRunIDDigest         string    `json:"chain_run_id_digest,omitempty"`
	PublicationMode          string    `json:"publication_mode"`
	PointCodecVersion        int       `json:"point_codec_version"`
	TagCodecVersion          int       `json:"tag_codec_version"`
	StartedAt                time.Time `json:"started_at"`
	PreparedAt               time.Time `json:"prepared_at"`
	PointDeadlineAt          time.Time `json:"point_deadline_at"`
}

type PublicationConsistencyV1 struct {
	Version                    int                     `json:"version"`
	PublicationRevision        uint64                  `json:"publication_revision"`
	AttemptCount               uint64                  `json:"attempt_count"`
	Completion                 ProviderCompletionClass `json:"completion,omitempty"`
	Code                       PublicationFailureCode  `json:"code,omitempty"`
	CaptureStartedAt           *time.Time              `json:"capture_started_at,omitempty"`
	CaptureFinishedAt          *time.Time              `json:"capture_finished_at,omitempty"`
	FilesProcessed             uint64                  `json:"files_processed,omitempty"`
	LogicalBytes               uint64                  `json:"logical_bytes,omitempty"`
	Provider                   ProviderKind            `json:"provider,omitempty"`
	RepositoryIdentityDigest   string                  `json:"repository_identity_digest,omitempty"`
	RequestedTagDigest         string                  `json:"requested_tag_digest,omitempty"`
	ProviderCommitDigest       string                  `json:"provider_commit_digest,omitempty"`
	AdapterRevision            string                  `json:"adapter_revision,omitempty"`
	CapabilityRevision         int                     `json:"capability_revision,omitempty"`
	FirstMissingObservedAt     *time.Time              `json:"first_missing_observed_at,omitempty"`
	MissingGraceReportedAt     *time.Time              `json:"missing_grace_reported_at,omitempty"`
	LastAttemptAt              *time.Time              `json:"last_attempt_at,omitempty"`
	QuarantineObservationDigest string                `json:"quarantine_observation_digest,omitempty"`
}

type PublicationConfig struct {
	ReconcileInterval      time.Duration
	ReconcileBatchSize     int
	WorkerConcurrency      int
	MissingGrace           time.Duration
	BackupStreamMaxBytes   int64
	ManifestTimeout        time.Duration
	ManifestMaxBytes       int64
	ManifestMaxEntries     int64
	ManifestMaxRecordBytes int
	ManifestMaxDepth       int
}

type PublicationAuditContext struct {
	Actor         AuditActor
	CorrelationID string
}

type CanonicalSHA256 struct {
	// unexported hash/error state
}

func NewCanonicalSHA256() *CanonicalSHA256
func (writer *CanonicalSHA256) String(string)
func (writer *CanonicalSHA256) Uint8(uint8)
func (writer *CanonicalSHA256) Uint32(uint32)
func (writer *CanonicalSHA256) Uint64(uint64)
func (writer *CanonicalSHA256) Int64(int64)
func (writer *CanonicalSHA256) HexDigest() (string, error)
```

`CanonicalSHA256.String` writes an unsigned 32-bit big-endian length plus exact bytes and latches an error when the length exceeds `math.MaxUint32`; integer methods use big-endian fixed width, and `HexDigest` rejects reuse after finalization. Also add `PublicationNativeSnapshot TaskPublicationMode = "native_snapshot"` and `LeaseHolderPointPublication LeaseHolderType = "point_publication"`. `MapPublicationMode(ProviderRestic, mode)` must accept only `PublicationNativeSnapshot`; the old `native_object_versions` placeholder remains valid only for Rclone.

### 1.2 Provider evidence and manifest ports

Replace the reserved empty publication types in `backend/internal/backupasset/provider/contracts.go` with these exact contracts:

```go
type PublicationAttempt struct {
	Provider             backupasset.ProviderKind
	RepositoryID         string
	RepositoryIdentity   string `json:"-"`
	TaskRepositoryLinkID string
	RecoveryPointID      string
	TaskID                uint
	TaskRunID             uint
	RequiredTags          [2]string `json:"-"`
	PointDeadlineAt       time.Time
	CapabilityRevision    int
	AdapterRevision       string
	Audit                 backupasset.PublicationAuditContext `json:"-"`
	Access                AccessBinding          `json:"-"`
	Fence                 backupasset.LeaseFence `json:"-"`
}

type ProviderCommitEvidence struct {
	Provider             backupasset.ProviderKind
	RepositoryIdentity   string `json:"-"`
	NativePointID        string `json:"-"`
	CaptureStartedAt     time.Time
	CaptureFinishedAt    time.Time
	FilesProcessed       uint64
	LogicalBytes         uint64
}

type ResticBackupInput struct {
	Source   string   `json:"-"`
	Excludes []string `json:"-"`
}

type ResticBackupProgress struct {
	ObservedAt     time.Time
	Percent        int
	ThroughputMbps float64
	FilesDone      uint64
}

type ResticBackupResult struct {
	ExitCode       int
	Completion     backupasset.ProviderCompletionClass
	ProviderCommit *ProviderCommitEvidence
	EvidenceCode   backupasset.PublicationFailureCode
}

const UnknownProviderExitCode = -1

type ResticStoredSummary struct {
	BackupStartedAt  time.Time
	BackupFinishedAt time.Time
	FilesProcessed   uint64
	LogicalBytes     uint64
}

type ResticSnapshotObservation struct {
	RepositoryIdentity string   `json:"-"`
	NativePointID      string   `json:"-"`
	SnapshotTime       time.Time
	Tags               []string `json:"-"`
	OriginalPresent    bool
	Original           *string  `json:"-"`
	Summary            *ResticStoredSummary
}

type ManifestLimits struct {
	Timeout        time.Duration
	MaxBytes       int64
	MaxEntries     int64
	MaxRecordBytes int
	MaxDepth       int
}

type ResticManifestFidelity struct {
	Version     int       `json:"version"`
	Profile     string    `json:"profile"`
	Included    [7]string `json:"included"`
	CommitBound [3]string `json:"commit_bound"`
	NotExposed  [7]string `json:"not_exposed"`
}

func ResticManifestFidelityV1() ResticManifestFidelity {
	return ResticManifestFidelity{
		Version: 1,
		Profile: "restic_ls_json_v1",
		Included: [7]string{"path_name", "native_type", "regular_file_size", "mode", "uid_gid", "mtime_atime_ctime", "inode"},
		CommitBound: [3]string{"repository_identity", "full_snapshot_id", "required_tags"},
		NotExposed: [7]string{"link_target", "xattrs", "generic_attributes", "device_link_counts", "content_blob_ids", "subtree_ids", "acl_security_descriptors"},
	}
}

type ManifestEvidence struct {
	DigestAlgorithm   string
	Digest            string
	Generator         string
	GeneratorVersion  string
	Completeness      backupasset.ManifestCompleteness
	EntryCount        int64
	LogicalBytes      int64
	Fidelity          ResticManifestFidelity
	HeaderCapturedAt  time.Time
	ObservedTagDigest string
	FailureCode       backupasset.PublicationFailureCode
}

type ResticPublisher interface {
	Backup(context.Context, PublicationAttempt, ResticBackupInput, func(ResticBackupProgress)) (ResticBackupResult, error)
	LookupAttempt(context.Context, PublicationAttempt) ([]ResticSnapshotObservation, error)
}

type ManifestBuilder interface {
	BuildManifest(context.Context, PublicationAttempt, ProviderCommitEvidence, ManifestLimits) (ManifestEvidence, error)
}

type CommandCompletion struct {
	ExitCode       int
	ExitCodeKnown  bool
	Stderr         []byte `json:"-"`
	StderrTruncated bool
}

type CommandExecution interface {
	io.Reader
	Join() (CommandCompletion, error)
	Cancel() error
}

type CommandStreamTransport interface {
	OpenExecution(context.Context, CommandInvocation, OperationLimits, int64) (CommandExecution, error)
}

type PublicationConfigSource func() (backupasset.PublicationConfig, error)

func NewResticAdapterWithPublication(
	transport CommandTransport,
	streamTransport CommandStreamTransport,
	cursors *CursorCodec,
	limitsSource OperationLimitsSource,
	publicationConfigSource PublicationConfigSource,
	maxPageSize int,
	now func() time.Time,
) (*ResticAdapter, error)
```

The existing `NewResticAdapter` and `NewResticAdapterWithLimitsSource` signatures remain read-only-compatible and leave the optional publication fields nil; only `NewResticAdapterWithPublication` validates and installs the streaming transport/config source. `provider.Registration` gains `ResticPublisher ResticPublisher` and `ManifestBuilder ManifestBuilder`; the Registry accessors are `ResticPublisher(kind backupasset.ProviderKind) (ResticPublisher, error)` and `ManifestBuilder(kind backupasset.ProviderKind) (ManifestBuilder, error)`. Only the Restic registration is populated in Child 3.

### 1.3 Evidence executor

Create `backend/internal/task/executor/evidence.go`:

```go
type EvidenceExecutionRequest struct {
	Task      model.Task
	TaskRunID uint
	Attempt   provider.PublicationAttempt
}

type EvidenceExecutionResult struct {
	ExitCode       int
	Completion     backupasset.ProviderCompletionClass
	ProviderCommit *provider.ProviderCommitEvidence
	EvidenceCode   backupasset.PublicationFailureCode
}

type EvidenceExecutor interface {
	Executor
	RunWithEvidence(context.Context, EvidenceExecutionRequest, LogFunc, ProgressFunc) (EvidenceExecutionResult, error)
}
```

The Provider and executor result fields intentionally match one-for-one. `ResticExecutor.RunWithEvidence` performs only Task-config mapping, invokes `provider.ResticPublisher.Backup`, maps progress, and returns after the command stream has naturally drained and joined.

### 1.4 Provider-neutral publication session

Create `backend/internal/backupasset/publication/contracts.go`:

```go
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
}

type Deferral struct {
	Completion backupasset.ProviderCompletionClass
	Code       backupasset.PublicationFailureCode
}

type Outcome struct {
	RepositoryID          string
	RecoveryPointID       string
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
	Attempt() *provider.PublicationAttempt
	Context() context.Context
	Cancel(error) error
	Abandon(error) error
	CompleteCompatibility(context.Context) error
	RecordProviderCommit(context.Context, provider.ProviderCommitEvidence) (Outcome, error)
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
	ReconcileInterruptedRuns(context.Context, int) (unresolved bool, err error)
}
```

### 1.5 Restic command admission and lineage session

Add to the same publication contracts package:

```go
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
	OperationEvidenceBackup        ResticOperation = "evidence_backup"
	OperationManifest              ResticOperation = "manifest"
	OperationReconcile             ResticOperation = "reconcile"
)

type AdmissionMode string

const (
	AdmissionPristineLegacy AdmissionMode = "pristine_legacy"
	AdmissionManaged        AdmissionMode = "managed"
	AdmissionRollbackSafe   AdmissionMode = "rollback_safe"
)

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
	ListEntries(context.Context, string /* fullNativeID */, provider.EntryLocator, provider.PageRequest) (provider.EntryPage, error)
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

type ReconcileMatchClass string

const (
	ReconcileMatchZero      ReconcileMatchClass = "zero"
	ReconcileMatchExact     ReconcileMatchClass = "exact"
	ReconcileMatchMultiple  ReconcileMatchClass = "multiple"
	ReconcileMatchRewritten ReconcileMatchClass = "rewritten"
	ReconcileMatchConflict  ReconcileMatchClass = "conflict"
	ReconcileMatchTransient ReconcileMatchClass = "transient"
)

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

type LegacyBlock struct {
	TaskID    uint
	TaskRunID *uint
	Operation ResticOperation
	Audit     backupasset.PublicationAuditContext
}

type LegacyBlockRecorder interface {
	RecordLegacyBlock(context.Context, LegacyBlock) error
}

func NewSystemLegacyBlockAuditContext(uint, *uint, ResticOperation) (backupasset.PublicationAuditContext, error)

type PrometheusMetrics struct {
	attempts         *prometheus.CounterVec
	outcomes         *prometheus.CounterVec
	backlogCount     *prometheus.GaugeVec
	backlogOldestAge *prometheus.GaugeVec
	reconcileMatches *prometheus.CounterVec
	fenceLost        *prometheus.CounterVec
	manifestDuration *prometheus.HistogramVec
	manifestEntries  *prometheus.HistogramVec
	manifestBytes    *prometheus.HistogramVec
	legacyBlocked    *prometheus.CounterVec
	auditFailures    *prometheus.CounterVec
}

func NewPrometheusMetrics(prometheus.Registerer) (*PrometheusMetrics, error)
```

All concrete sessions are exactly-once closers. They acquire admission before reading the feature setting, Task link, or managed-history latch, snapshot both mode and generation in the token, and hold that token until Provider close/join and synchronous response projection complete. Only `AdmissionPristineLegacy` may authorize compatibility; `AdmissionRollbackSafe` and `AdmissionManaged` are non-overridable safety floors even if a later DB/history read appears empty.

## 2. Exact File Ownership

### 2.1 Create during implementation

```text
backend/internal/database/migrations/sqlite/000063_backup_asset_publication_contract.up.sql
backend/internal/database/migrations/sqlite/000063_backup_asset_publication_contract.down.sql
backend/internal/database/migrations/postgres/000063_backup_asset_publication_contract.up.sql
backend/internal/database/migrations/postgres/000063_backup_asset_publication_contract.down.sql
backend/internal/backupasset/publication.go
backend/internal/backupasset/publication_test.go
backend/internal/backupasset/canonical.go
backend/internal/backupasset/canonical_test.go
backend/internal/sshutil/lifecycle.go
backend/internal/sshutil/lifecycle_test.go
backend/internal/backupasset/publication/contracts.go
backend/internal/backupasset/publication/contracts_test.go
backend/internal/backupasset/publication/metrics.go
backend/internal/backupasset/publication/metrics_test.go
backend/internal/backupasset/provider/restic_publication.go
backend/internal/backupasset/provider/restic_publication_test.go
backend/internal/backupasset/provider/restic_manifest.go
backend/internal/backupasset/provider/restic_manifest_test.go
backend/internal/backupasset/provider/publication_boundary_test.go
backend/internal/backupasset/provider/testdata/restic/backup-success.ndjson
backend/internal/backupasset/provider/testdata/restic/backup-missing-summary.ndjson
backend/internal/backupasset/provider/testdata/restic/backup-malformed.ndjson
backend/internal/backupasset/provider/testdata/restic/backup-truncated.ndjson
backend/internal/backupasset/provider/testdata/restic/snapshots-exact.json
backend/internal/backupasset/provider/testdata/restic/snapshots-rewritten.json
backend/internal/backupasset/provider/testdata/restic/manifest-valid.ndjson
backend/internal/backupasset/provider/testdata/restic/manifest-depth-edge.ndjson
backend/internal/backupasset/provider/testdata/restic/manifest-rewritten.ndjson
backend/internal/backupasset/repository/managed_history.go
backend/internal/backupasset/repository/managed_history_test.go
backend/internal/backupasset/repository/lineage_guard.go
backend/internal/backupasset/repository/lineage_guard_test.go
backend/internal/backupasset/repository/publication_identity.go
backend/internal/backupasset/repository/publication_identity_test.go
backend/internal/backupasset/repository/publication_execution.go
backend/internal/backupasset/repository/publication_execution_test.go
backend/internal/backupasset/repository/publication_commit_test.go
backend/internal/backupasset/repository/publication_integration_test.go
backend/internal/backupasset/repository/publication_reconcile.go
backend/internal/backupasset/repository/publication_reconcile_test.go
backend/internal/backupasset/runtime/admission.go
backend/internal/backupasset/runtime/admission_test.go
backend/internal/backupasset/runtime/controller.go
backend/internal/backupasset/runtime/admission_controller_test.go
backend/internal/backupasset/runtime/runtime.go
backend/internal/backupasset/runtime/runtime_test.go
backend/internal/backupasset/runtime/publication_worker.go
backend/internal/backupasset/runtime/publication_worker_test.go
backend/internal/task/executor/evidence.go
backend/internal/task/executor/evidence_test.go
backend/internal/task/publication_runner.go
backend/internal/task/publication_runner_test.go
backend/internal/task/publication_interrupted.go
backend/internal/task/publication_interrupted_test.go
backend/cmd/server/main_test.go
backend/internal/api/handlers/settings_transition_test.go
backend/internal/api/handlers/snapshot_handler_test.go
backend/internal/api/handlers/snapshot_diff_handler_test.go
```

`publication_execution.go` intentionally owns the session lifecycle and its
fenced commit methods together; `publication_commit_test.go` remains the
separate focused transaction test file. This keeps the private execution
fence/context fields and their terminal methods in one source boundary without
changing the reviewed public contracts.

### 2.2 Modify during implementation

```text
backend/internal/database/backup_asset_migrations_integration_test.go
.github/workflows/ci.yml
backend/internal/backupasset/domain.go
backend/internal/backupasset/domain_test.go
backend/internal/backupasset/errors.go
backend/internal/backupasset/service.go
backend/internal/backupasset/service_test.go
backend/internal/backupasset/audit_action.go
backend/internal/backupasset/audit_action_test.go
backend/internal/backupasset/lease.go
backend/internal/backupasset/lease_test.go
backend/internal/backupasset/provider/contracts.go
backend/internal/backupasset/provider/registry.go
backend/internal/backupasset/provider/registry_test.go
backend/internal/backupasset/provider/runner.go
backend/internal/backupasset/provider/runner_test.go
backend/internal/backupasset/provider/restic.go
backend/internal/backupasset/repository/service.go
backend/internal/backupasset/repository/testutil_test.go
backend/internal/backupasset/repository/query_test.go
backend/internal/backupasset/repository/connect.go
backend/internal/backupasset/repository/connect_test.go
backend/internal/sshutil/command_runner.go
backend/internal/sshutil/command_runner_test.go
backend/internal/sshutil/node_dialer.go
backend/internal/sshutil/node_dialer_test.go
backend/internal/settings/service.go
backend/internal/settings/service_test.go
backend/internal/task/executor/executor.go
backend/internal/task/executor/restic_executor.go
backend/internal/task/executor/restic_executor_test.go
backend/internal/task/manager.go
backend/internal/task/manager_test.go
backend/internal/task/runner.go
backend/internal/task/retention.go
backend/internal/task/retention_test.go
backend/internal/snapshot/indexer.go
backend/internal/snapshot/indexer_test.go
backend/internal/anomaly/snapshot_diff.go
backend/internal/anomaly/snapshot_diff_test.go
backend/internal/api/handlers/snapshot_handler.go
backend/internal/api/handlers/snapshot_search_handler.go
backend/internal/api/handlers/snapshot_search_handler_test.go
backend/internal/api/handlers/snapshot_diff_handler.go
backend/internal/api/handlers/settings_handler.go
backend/internal/api/handlers/config_handler.go
backend/internal/api/handlers/config_handler_test.go
backend/internal/api/handlers/step_up_test.go
backend/internal/api/handlers/credential_access_grant_test.go
backend/internal/api/router.go
backend/internal/api/router_test.go
backend/cmd/server/main.go
backend/README_backend.md
.env.deploy
backend/.env.production.example
docs/env-vars.md
docs/admin/backup-recovery.md
.trellis/spec/backend/database-guidelines.md
.trellis/spec/backend/directory-structure.md
.trellis/spec/backend/quality-guidelines.md
```

No frontend source, Swagger-generated file, public asset route, Catalog implementation, Provider deletion adapter, or migration outside `000063` belongs in this child. If implementation needs another production path, stop and amend the reviewed plan before editing it.

## 3. Task 1 — Schema A And Truthful Domain Enums

**Files:**

- Create the four paired `000063_backup_asset_publication_contract` SQL files listed in Section 2.1.
- Modify `backend/internal/database/backup_asset_migrations_integration_test.go`.
- Modify `.github/workflows/ci.yml` so the required PostgreSQL parity job executes both migration 062 and 063 suites.
- Modify `backend/internal/backupasset/domain.go` and `backend/internal/backupasset/domain_test.go`.
- Modify `backend/internal/backupasset/lease.go`.
- Modify `backend/internal/backupasset/repository/connect.go`, `backend/internal/backupasset/repository/connect_test.go`, and `backend/internal/backupasset/repository/query_test.go`.

- [x] **Step 1.1: Add the failing domain and connect tests.**

Add these cases with the existing table-driven style:

```go
func TestPublicationModeMappingRequiresNativeSnapshotForRestic(t *testing.T) {
	mode, semantics, state, err := MapPublicationMode(ProviderRestic, PublicationNativeSnapshot)
	if err != nil {
		t.Fatalf("map native Restic publication: %v", err)
	}
	if mode != VersionNativeSnapshot || semantics != PointNativeSnapshot || state != RecoveryPointPreparing {
		t.Fatalf("unexpected Restic mapping: %s %s %s", mode, semantics, state)
	}
	if _, _, _, err := MapPublicationMode(ProviderRestic, PublicationNativeObjectVersions); err == nil {
		t.Fatal("Restic accepted the Rclone native-object-version mode")
	}
}

func TestConnectPersistsNativeSnapshotModeForRestic(t *testing.T) {
	db := newRepositoryTestDB(t)
	taskEntity := seedTask(t, db, "restic", "/backup/repository", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, provider.NativeResticIdentityPrefix+strings.Repeat("a", 64))}
	service := newRepositoryServiceForTest(t, db, backupasset.ProviderRestic, prober)
	if _, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatalf("connect Restic repository: %v", err)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
		t.Fatalf("publication mode = %q", link.PublicationMode)
	}
}

func TestPublicationLeaseHolderIsValid(t *testing.T) {
	if !validLeaseHolderTypes[LeaseHolderPointPublication] {
		t.Fatal("point_publication lease holder is not valid")
	}
}
```

- [x] **Step 1.2: Run the domain red test.**

```bash
go -C backend test ./internal/backupasset ./internal/backupasset/repository -run 'TestPublicationModeMappingRequiresNativeSnapshotForRestic|TestPublicationLeaseHolderIsValid|TestConnectPersistsNativeSnapshotModeForRestic' -count=1
```

Expected: FAIL to compile because `PublicationNativeSnapshot` is undefined. A syntax/import failure is not the expected red result and must be corrected before continuing.

- [x] **Step 1.3: Add migration 063 failing integration tests.**

Extend the harness without replacing the version-62 tests. Add `backupAssetPublicationVersion = 63`, migration helpers that migrate explicitly to 62 or 63, and two engine parents that call the same contract helper:

```go
func TestBackupAssetMigration063SQLite(t *testing.T) {
	runBackupAssetMigration063Contract(t, newSQLiteMigrationFixture(t))
}

func TestBackupAssetMigration063Postgres(t *testing.T) {
	runBackupAssetMigration063Contract(t, newRequiredPostgresMigrationFixture(t))
}

func runBackupAssetMigration063Contract(t *testing.T, fixture migrationFixture) {
	t.Run("ApplyDown", fixture.testApplyDown)
	t.Run("ConvertsOnlyResticLinks", fixture.testConvertsOnlyResticLinks)
	t.Run("UniqueProducingRunAcrossSemantics", fixture.testUniqueProducingRunAcrossSemantics)
	t.Run("UniqueNativeSourcePerRepository", fixture.testUniqueNativeSourcePerRepository)
	t.Run("DownRejectsActivePublicationLease", fixture.testDownRejectsActivePublicationLease)
	t.Run("DownRejectsEveryNativePointStateAndNullableLineage", fixture.testDownRejectsEveryNativePointStateAndNullableLineage)
	t.Run("RejectedDownLeavesVersionSchemaAndDataUnchanged", fixture.testRejectedDownLeavesVersionSchemaAndDataUnchanged)
	t.Run("UTCAndModelParity", fixture.testUTCAndModelParity)
}
```

Use all immutable states, not a sample:

```go
states := []string{
	"preparing", "verifying", "committed", "degraded", "expiring",
	"expired", "failed", "purge_blocked",
}
```

The uniqueness fixture must insert one `native_snapshot` point with a non-null run, then prove a second `mutable_head`, `xirang_manifest`, or `native_snapshot` point with that same non-null run fails. A separate mutable head with `producing_task_run_id IS NULL` must remain legal. The source fixture must prove the same non-empty fingerprint conflicts only within the same Repository and `native_snapshot` semantics.

- [x] **Step 1.4: Run the migration red test.**

```bash
go -C backend test ./internal/database -run 'TestBackupAssetMigration063' -count=1
```

Expected: FAIL with a migration-to-version-63 error because no `000063` files exist. With no `TEST_POSTGRES_DSN`, the PostgreSQL case may report SKIP; SQLite must fail.

- [x] **Step 1.5: Verify the current required CI job is red for migration 063 coverage.**

```bash
rg -Fq 'TestBackupAssetMigration0(62|63)Postgres' .github/workflows/ci.yml
```

Expected: FAIL with exit 1 because the current job is hard-coded to `TestBackupAssetMigration062Postgres` and would falsely pass without exercising 063.

- [x] **Step 1.6: Implement the enum and connect correction.**

Add the constants and make the Restic mapping explicit:

```go
const PublicationNativeSnapshot TaskPublicationMode = "native_snapshot"
const LeaseHolderPointPublication LeaseHolderType = "point_publication"

func MapPublicationMode(provider ProviderKind, mode TaskPublicationMode) (VersionMode, PointVersionSemantics, RecoveryPointState, error) {
	if provider == ProviderCommand {
		return "", "", "", fmt.Errorf("%w: command task has no artifact contract", ErrCapabilityUnavailable)
	}
	if provider == ProviderRestic {
		if mode != PublicationNativeSnapshot {
			return "", "", "", fmt.Errorf("%w: Restic requires native_snapshot publication", ErrInvalidState)
		}
		return VersionNativeSnapshot, PointNativeSnapshot, RecoveryPointPreparing, nil
	}

	var version VersionMode
	switch {
	case provider == ProviderRsync && mode == PublicationLegacyMutable,
		provider == ProviderRclone && mode == PublicationLegacyMutable:
		return VersionMutableHead, PointMutableHead, RecoveryPointObserved, nil
	case provider == ProviderRsync && mode == PublicationVersionedHardlink:
		version = VersionHardlinkTree
	case provider == ProviderRsync && mode == PublicationVersionedFullCopy:
		version = VersionFullCopyTree
	case provider == ProviderRclone && mode == PublicationVersionedPrefix:
		version = VersionVersionedPrefix
	case provider == ProviderRclone && mode == PublicationNativeObjectVersions:
		version = VersionNativeObjectVersions
	case provider == ProviderVerifiedImport:
		version = versionModeForPublication(mode)
		if version == "" || version == VersionMutableHead {
			return "", "", "", fmt.Errorf("%w: unsupported verified import mode", ErrInvalidState)
		}
		return version, PointImportedBaseline, RecoveryPointPreparing, nil
	default:
		return "", "", "", fmt.Errorf("%w: unsupported provider/publication combination", ErrInvalidState)
	}
	return version, PointXirangManifest, RecoveryPointPreparing, nil
}
```

In `ensureTaskLink`, select `PublicationNativeSnapshot` for Restic. Add the new lease-holder constant to `validLeaseHolderTypes`, and update existing Restic-only test fixtures so they persist `PublicationNativeSnapshot` rather than the Rclone-only mode.

- [x] **Step 1.7: Implement paired migration 063 and extend the required PostgreSQL CI gate.**

PostgreSQL up must use the existing generated constraint names and this order inside one transaction:

```sql
BEGIN;

ALTER TABLE task_repository_links
    DROP CONSTRAINT task_repository_links_publication_mode_check;
ALTER TABLE task_repository_links
    ADD CONSTRAINT task_repository_links_publication_mode_check
    CHECK (publication_mode IN ('legacy_mutable', 'versioned_hardlink', 'versioned_full_copy', 'versioned_prefix', 'native_object_versions', 'native_snapshot'));

UPDATE task_repository_links AS link
SET publication_mode = 'native_snapshot'
FROM backup_repositories AS repository
WHERE repository.id = link.repository_id
  AND repository.provider_kind = 'restic'
  AND link.publication_mode = 'native_object_versions';

ALTER TABLE recovery_point_leases
    DROP CONSTRAINT recovery_point_leases_holder_type_check;
ALTER TABLE recovery_point_leases
    ADD CONSTRAINT recovery_point_leases_holder_type_check
    CHECK (holder_type IN ('rsync_parent', 'catalog_build', 'content_session', 'processing_job', 'export_job', 'recovery_job', 'point_publication'));

CREATE UNIQUE INDEX idx_recovery_points_producing_task_run_unique
    ON recovery_points(producing_task_run_id)
    WHERE producing_task_run_id IS NOT NULL;
CREATE UNIQUE INDEX idx_recovery_points_native_source_unique
    ON recovery_points(repository_id, source_fingerprint)
    WHERE semantics = 'native_snapshot' AND source_fingerprint <> '';

COMMIT;
```

The PostgreSQL down migration is one atomic `BEGIN` → guard → reversal → `COMMIT` unit and must abort before changing schema when either predicate is true:

```sql
BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM recovery_point_leases
        WHERE holder_type = 'point_publication' AND status = 'active'
    ) THEN
        RAISE EXCEPTION '000063 down blocked: active point publication lease';
    END IF;
    IF EXISTS (
        SELECT 1 FROM recovery_points WHERE semantics = 'native_snapshot'
    ) THEN
        RAISE EXCEPTION '000063 down blocked: managed Restic history exists';
    END IF;
END $$;

-- Drop the two indexes, map only Restic native_snapshot links back to
-- native_object_versions, and restore both original checks here.

COMMIT;
```

After the guard, drop the two indexes, map only Restic `native_snapshot` links back to `native_object_versions`, and restore the two original checks before `COMMIT`. Do not delete or rewrite RecoveryPoints. Both engine contract parents snapshot migration version, checks/indexes, and representative rows before every rejected down, then assert all three are byte/structurally unchanged afterward.

SQLite up/down each execute as one migration transaction and rebuild only `task_repository_links` and `recovery_point_leases`, explicitly preserving every column and foreign key. Use these complete column orders for `INSERT ... SELECT`:

```text
task_repository_links:
id, task_id, repository_id, task_name_snapshot, node_id_snapshot,
node_name_snapshot, publication_mode, encrypted_legacy_locator,
linked_at, unlinked_at, created_at, updated_at

recovery_point_leases:
id, recovery_point_id, holder_type, owner_id, attempt_id, fence_token,
status, lease_expires_at, absolute_deadline, last_heartbeat_at,
released_at, created_at, updated_at
```

Recreate `idx_task_repository_links_active_task`, `idx_task_repository_links_repository_id`, `idx_recovery_point_leases_active_owner_slot`, `idx_recovery_point_leases_recovery_status_expiry`, and `idx_recovery_point_leases_absolute_deadline` after each table rename. Add the same two RecoveryPoint unique indexes as PostgreSQL. The SQLite down precondition uses a temporary checked guard so it aborts rather than silently proceeding:

```sql
CREATE TEMP TABLE backup_asset_063_down_guard (
    allowed INTEGER NOT NULL CHECK (allowed = 1)
);
INSERT INTO backup_asset_063_down_guard(allowed)
SELECT CASE WHEN
    EXISTS (SELECT 1 FROM recovery_point_leases WHERE holder_type = 'point_publication' AND status = 'active')
    OR EXISTS (SELECT 1 FROM recovery_points WHERE semantics = 'native_snapshot')
THEN 0 ELSE 1 END;
DROP TABLE backup_asset_063_down_guard;
```

End SQLite up and down fixtures with `PRAGMA foreign_key_check` assertions in Go. Do not add timestamp defaults or time-zone expressions.

Change the `PostgreSQL Migration Parity` workflow command to this exact regex so both versions run under `REQUIRE_POSTGRES_MIGRATION_TEST=1`:

```yaml
- name: Run PostgreSQL migration parity
  run: go test ./internal/database -run 'TestBackupAssetMigration0(62|63)Postgres' -count=1
```

- [x] **Step 1.8: Run the green schema/domain/CI-contract suites.**

```bash
go -C backend test ./internal/backupasset ./internal/backupasset/repository -run 'PublicationMode|PublicationLeaseHolder|Connect.*Restic' -count=1
go -C backend test ./internal/database -run 'TestBackupAssetMigration06(2|3)' -count=1
bash scripts/check-migration-utc-safety.sh
rg -Fq 'TestBackupAssetMigration0(62|63)Postgres' .github/workflows/ci.yml
```

Expected: PASS; PostgreSQL is SKIP only when `TEST_POSTGRES_DSN` is absent; UTC scan reports zero violations.

- [x] **Step 1.9: Record the future Phase 3.4 boundary without committing.**

```bash
git diff --check -- .github/workflows/ci.yml backend/internal/database backend/internal/backupasset/domain.go backend/internal/backupasset/domain_test.go backend/internal/backupasset/lease.go backend/internal/backupasset/repository/connect.go backend/internal/backupasset/repository/connect_test.go backend/internal/backupasset/repository/query_test.go
```

Expected: exit 0. Migration/domain/CI paths belong to Phase 3.4 commit `feat: repair backup asset publication schema`. The final `connect.go`/`connect_test.go` files join `feat: publish fenced Restic recovery points`, because Task 5 later updates their Repository constructor calls to the final Admission/History/Metrics contract and that commit must remain independently buildable.

## 4. Task 2 — Publication Domain, Settings, Audit, And Transactional Lease Primitives

**Files:**

- Create `backend/internal/backupasset/publication.go`, `publication_test.go`, `canonical.go`, and `canonical_test.go`.
- Create `backend/internal/sshutil/lifecycle.go` and `lifecycle_test.go` for the neutral command-join safety bound required by settings before the stream implementation exists.
- Modify `backend/internal/backupasset/errors.go`, `service.go`, `service_test.go`, `audit_action.go`, `audit_action_test.go`, `lease.go`, and `lease_test.go`.
- Modify `backend/internal/settings/service.go` and `backend/internal/settings/service_test.go`.

- [x] **Step 2.1: Write failing publication-codec and pairing tests.**

Add tests that round-trip UTC lineage/consistency values, reject trailing/unknown JSON, reject raw locator/native ID/tag fields, and validate the only legal deferral pairings:

```go
func TestPublicationDeferralValidation(t *testing.T) {
	tests := []struct {
		name string
		value ProviderCompletionClass
		code PublicationFailureCode
		valid bool
	}{
		{"exit zero evidence defect", CompletionKnownExitZero, FailureEvidenceMissingSummary, true},
		{"unknown timeout", CompletionOutcomeUnknown, FailureProviderTimeout, true},
		{"unknown cancellation", CompletionOutcomeUnknown, FailureProviderCanceled, true},
		{"unknown resource limit", CompletionOutcomeUnknown, FailureProviderResourceLimit, true},
		{"unknown command lifecycle", CompletionOutcomeUnknown, FailureProviderOutcomeUnknown, true},
		{"abandoned joined command", CompletionOutcomeUnknown, FailurePublicationSessionAbandoned, true},
		{"nonzero cannot defer", CompletionKnownNonzero, FailureProviderNonzeroExit, false},
		{"exit zero cannot use timeout", CompletionKnownExitZero, FailureProviderTimeout, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePublicationDeferral(test.value, test.code)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
}
```

The codec test must use distinct UTC `StartedAt`, `PreparedAt`, and `PointDeadlineAt` values including `2026-07-14T03:04:05.123456789Z`, verify nanosecond preservation and `StartedAt <= PreparedAt < PointDeadlineAt`, and assert the encoded bytes contain none of `repository`, `snapshot`, `xirang.link`, or `xirang.point` test secrets. Add `TestPublicationAuditContextRequiresSafeUserOrSystemIdentityAndCorrelation` and canonical-writer vectors that prove uint widths/order, UTF-8 byte preservation, `math.MaxUint32` length rejection, and one-shot finalization.

- [x] **Step 2.2: Run the publication-domain red test.**

```bash
go -C backend test ./internal/backupasset -run 'Publication|Canonical' -count=1
```

Expected: FAIL to compile because the publication types/codecs, audit-context validator, and canonical writer are undefined.

- [x] **Step 2.3: Write failing settings and audit tests.**

Extend the registry contract table with these exact definitions:

```go
"backup_assets.publication_reconcile_interval": {"BACKUP_ASSETS_PUBLICATION_RECONCILE_INTERVAL", "5m", TypeDuration, "", "", "30s", "24h"},
"backup_assets.publication_reconcile_batch_size": {"BACKUP_ASSETS_PUBLICATION_RECONCILE_BATCH_SIZE", "100", TypeInt, "1", "1000", "", ""},
"backup_assets.publication_worker_concurrency": {"BACKUP_ASSETS_PUBLICATION_WORKER_CONCURRENCY", "2", TypeInt, "1", "32", "", ""},
"backup_assets.publication_missing_grace": {"BACKUP_ASSETS_PUBLICATION_MISSING_GRACE", "30m", TypeDuration, "", "", "1m", "24h"},
"backup_assets.publication_stream_max_bytes": {"BACKUP_ASSETS_PUBLICATION_STREAM_MAX_BYTES", "268435456", TypeInt, "1048576", "1073741824", "", ""},
"backup_assets.manifest_timeout": {"BACKUP_ASSETS_MANIFEST_TIMEOUT", "2h", TypeDuration, "", "", "1m", "24h"},
"backup_assets.manifest_max_bytes": {"BACKUP_ASSETS_MANIFEST_MAX_BYTES", "4294967296", TypeInt, "1048576", "17179869184", "", ""},
"backup_assets.manifest_max_entries": {"BACKUP_ASSETS_MANIFEST_MAX_ENTRIES", "10000000", TypeInt, "1", "100000000", "", ""},
"backup_assets.manifest_max_record_bytes": {"BACKUP_ASSETS_MANIFEST_MAX_RECORD_BYTES", "1048576", TypeInt, "4096", "4194304", "", ""},
"backup_assets.manifest_max_depth": {"BACKUP_ASSETS_MANIFEST_MAX_DEPTH", "4096", TypeInt, "1", "65536", "", ""},
```

Add cross-setting cases proving:

```text
lease_heartbeat < lease_duration
sshutil.CommandExecutionJoinTimeout < (lease_duration - lease_heartbeat)
publication_missing_grace >= lease_duration
publication_missing_grace < lease_absolute_deadline
manifest_timeout < lease_absolute_deadline
manifest_max_record_bytes <= manifest_max_bytes
publication_worker_concurrency <= provider_max_concurrency is not required;
both positive bounded gates apply independently
```

Add `TestFoundationConfigGettersUseFullEffectiveLeaseAndPublicationValues`, `TestValidateBackupAssetEffectiveUpdateCombinesExplicitCurrentAndRequestOverrides`, `TestValidateBackupAssetEffectiveUpdateDoesNotMutateInputsOrReadAgain`, and `TestWithBackupAssetMutationSerializesCallbacksOverFreshSnapshots`. Seed one lease value in DB, one publication value through the environment fallback, and one request override; call every Foundation config getter and prove validation evaluates that complete effective combination rather than silently filling omitted keys from code defaults. The purity test changes the DB/environment after obtaining `current` and proves validation still uses exactly the two supplied maps. The serialization test blocks the first callback, starts a second, proves it cannot observe/commit until the first persistence callback finishes, then proves the second receives a newly resolved snapshot rather than the first copy; mutating a callback's map must not corrupt any service-owned state.

Create `backend/internal/sshutil/lifecycle_test.go` with `TestCommandExecutionJoinTimeoutIsTenSeconds`, which asserts the exported neutral constant is exactly `10*time.Second`; settings boundary tests below consume the same symbol rather than copying a duration.

Use exact boundary cases: `lease_duration=70s, lease_heartbeat=60s` is invalid because the remaining 10 seconds is not greater than `sshutil.CommandExecutionJoinTimeout`; `71s/60s` is valid. This bound governs executor cleanup, stream cancellation/join, and worker shutdown joins rather than introducing an eleventh dynamic setting.

Add audit registry assertions for exactly these six actions:

```text
recovery_point_publication_prepare
recovery_point_publication_verify
recovery_point_publication_commit
recovery_point_publication_fail
recovery_point_publication_reconcile
restic_legacy_operation_blocked
```

For each publication action, construct one user actor and one system actor event and assert the stored safe field set is exactly `stage`, `status`, `code`, and `correlation_id`, with opaque Repository/point IDs plus Task/TaskRun IDs and bounded item/byte counts in typed columns. Add `AuditFieldOperation`; only `restic_legacy_operation_blocked` may include it, and its value must be one validated `ResticOperation`, never an arbitrary label. Inject full native ID, tags, path, locator, Repository identity, source/excludes, and raw stderr into rejected input fields and prove none reaches `FieldsJSON` or logs.

- [x] **Step 2.4: Run the settings/audit red tests.**

```bash
go -C backend test ./internal/sshutil ./internal/settings ./internal/backupasset -run 'CommandExecutionJoinTimeout|BackupAsset.*(Config|EffectiveUpdate|Mutation)|Publication.*Audit|AuditAction' -count=1
```

Expected: FAIL because the neutral join constant, ten registry entries, explicit-snapshot/serialized Foundation validation, six audit actions, and `AuditFieldOperation` are absent.

- [x] **Step 2.5: Write failing fixed-deadline and same-transaction fence tests.**

Extend `AcquireLeaseRequest` in the test's wished-for API and add:

```go
func TestLeaseAcquireTxUsesSuppliedPointDeadline(t *testing.T)
func TestLeaseValidateAndReleaseTxShareCallerTransaction(t *testing.T)
func TestLeaseFreshStageCannotMovePointDeadline(t *testing.T)
func TestLeaseTakeoverTxRotatesFenceAndPreservesDeadline(t *testing.T)
```

The first test must acquire with `AbsoluteDeadline = 2026-07-15T00:00:00Z`, release, advance the fake clock, acquire a fresh stage for the same point with the same supplied deadline, and assert equality. The transaction test must roll back after `ReleaseTx` and prove the lease remains active, then commit the same operation and prove it is released.

- [x] **Step 2.6: Run the lease red test.**

```bash
go -C backend test ./internal/backupasset -run 'TestLease(AcquireTx|ValidateAndReleaseTx|FreshStage|TakeoverTx)' -count=1
```

Expected: FAIL to compile because `AbsoluteDeadline`, `AcquireTx`, `ValidateFenceTx`, `ReleaseTx`, and `TakeoverTx` do not exist.

- [x] **Step 2.7: Implement the domain codecs, configuration, and audit registry.**

Implement the ledger types and these exact APIs:

```go
func ValidateProviderCompletionClass(ProviderCompletionClass) error
func ValidatePublicationFailureCode(PublicationFailureCode) error
func ValidatePublicationOutcomeCode(PublicationOutcomeCode) error
func ValidatePublicationDeferral(ProviderCompletionClass, PublicationFailureCode) error
func ValidatePublicationAuditContext(PublicationAuditContext) error
func NewSystemPublicationAuditContext(string) (PublicationAuditContext, error)
func EncodePublicationLineage(PublicationLineageV1) (string, error)
func DecodePublicationLineage(string) (PublicationLineageV1, error)
func EncodePublicationConsistency(PublicationConsistencyV1) (string, error)
func DecodePublicationConsistency(string) (PublicationConsistencyV1, error)
func (service *FoundationService) PublicationConfig() (PublicationConfig, error)

// backend/internal/settings
func (service *Service) ValidateBackupAssetEffectiveUpdate(current, overrides map[string]string) error
func (service *Service) WithBackupAssetMutation(context.Context, func(current map[string]string) error) error
func (service *Service) GetFallback(key string) (string, error)
func IsBackupAssetFoundationSetting(key string) bool
```

Use `json.Decoder.DisallowUnknownFields`, require a second decode to return `io.EOF`, normalize all accepted times to UTC, and require all digest fields to be empty or 64 lowercase hex. Add sentinels `ErrPublicationInProgress` and `ErrPublicationUnconfirmed` to `errors.go`; wrap them with `%w`.

Implement the locked `CanonicalSHA256` in `canonical.go`; Provider manifest hashing and Repository commit hashing must both use it. Add `ErrPublicationSessionAbandoned` as the only pre/out-of-band local cleanup cause and keep `ErrPublicationUnconfirmed` for an indeterminate commit response that cannot yet be classified as committed or rolled back. `ValidatePublicationAuditContext` accepts either a bounded non-zero user actor or exactly `{UserID:0, Username:"system", Role:"system"}`, plus a non-empty at-most-64-character correlation matching `^[A-Za-z0-9._:-]+$`.

Create `sshutil/lifecycle.go` first with `const CommandExecutionJoinTimeout = 10 * time.Second`; it contains no stream implementation or upper-layer import. Add the ten settings to the one registry and to `ValidateBackupAssetFoundationConfig`. Parse integer sizes with `ParseInt(..., 10, 64)` and checked conversion to `int` only after bounds validation. Refactor `FoundationService` around one unexported `effectiveFoundationValues()` that reads **every** original + new backup-asset foundation key through `SettingsReader`, validates that full map once, and returns it. `LeaseConfig`, `ProviderConfig`, audit config, and `PublicationConfig` all parse their fields from that shared full effective map; none calls the cross-setting validator with a partial map that would silently substitute code defaults. `IsBackupAssetFoundationSetting` is backed by the same exact key set as `ValidateBackupAssetFoundationConfig`, including all existing foundation keys and the ten additions; handlers never reproduce a prefix/key list. Add a dedicated `backupAssetMutationMu` to `settings.Service`; do not reuse the cache `RWMutex`. `WithBackupAssetMutation` locks it, resolves a fresh error-returning DB→environment→default snapshot of every foundation key, validates that snapshot, passes an immutable-by-contract copy to the callback, and holds the lock until the callback's admission transition and settings transaction finish. `ValidateBackupAssetEffectiveUpdate(current, overrides)` is a pure two-map operation: copy `current`, overlay only validated foundation keys from `overrides`, validate the complete copy, mutate neither input, perform no DB/environment/cache read, and return no hidden snapshot. Every callback must derive its target enabled value and persistence plan from exactly the supplied `current` copy. This serialization prevents two individually valid requests from committing an invalid combination or desynchronizing the enabled value from admission generation. Enforce the exact command-join margin with `sshutil.CommandExecutionJoinTimeout` in the settings package (never import `backupasset` back into `settings`, which would create a cycle); Provider, Repository, runtime, and Task callers use that neutral lower-layer constant directly. Keep `backup_assets.enabled` default `false`.

Add these audit constants and append them once to `AuditActions`:

```go
AuditActionRecoveryPointPublicationPrepare   AuditAction = "recovery_point_publication_prepare"
AuditActionRecoveryPointPublicationVerify    AuditAction = "recovery_point_publication_verify"
AuditActionRecoveryPointPublicationCommit    AuditAction = "recovery_point_publication_commit"
AuditActionRecoveryPointPublicationFail      AuditAction = "recovery_point_publication_fail"
AuditActionRecoveryPointPublicationReconcile AuditAction = "recovery_point_publication_reconcile"
AuditActionResticLegacyOperationBlocked      AuditAction = "restic_legacy_operation_blocked"
AuditFieldOperation                         AuditField  = "operation"
```

- [x] **Step 2.8: Implement transaction-aware lease methods without weakening current callers.**

Use this exact request extension and wrappers:

```go
type AcquireLeaseRequest struct {
	RecoveryPointID string
	HolderType      LeaseHolderType
	OwnerID         string
	AbsoluteDeadline time.Time
}

func (service *LeaseService) Acquire(ctx context.Context, request AcquireLeaseRequest) (Lease, error) {
	var lease Lease
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		lease, err = service.AcquireTx(ctx, tx, request)
		return err
	})
	return lease, err
}

func (service *LeaseService) AcquireTx(context.Context, *gorm.DB, AcquireLeaseRequest) (Lease, error)
func (service *LeaseService) ValidateFenceTx(context.Context, *gorm.DB, LeaseFence) error
func (service *LeaseService) ReleaseTx(context.Context, *gorm.DB, LeaseFence) error
func (service *LeaseService) TakeoverTx(context.Context, *gorm.DB, TakeoverLeaseRequest) (Lease, error)
```

Rules:

- zero `AbsoluteDeadline` retains existing callers' `now + config.AbsoluteDeadline` behavior;
- non-zero deadline is normalized to UTC, must be after `now`, and must not exceed `now + maxLeaseAbsoluteDeadline`;
- `AcquireTx`, `ValidateFenceTx`, and `ReleaseTx` use only the supplied transaction handle;
- publication callers lock RecoveryPoint first, then call lease methods; lease methods never lock a RecoveryPoint;
- takeover rotates attempt/token, preserves the stored absolute deadline, and fails after it;
- `Renew` caps expiry at the stored deadline and never rewrites it.

- [x] **Step 2.9: Run green domain/settings/audit/lease suites.**

```bash
go -C backend test ./internal/sshutil ./internal/settings ./internal/backupasset -run 'CommandExecutionJoinTimeout|Publication|BackupAsset.*(Config|EffectiveUpdate|Mutation)|AuditAction|Lease' -count=1
go -C backend test ./internal/backupasset -run 'Lease' -race -count=10
```

Expected: PASS with no race report, raw evidence in serialized values, duplicate audit action, or moved deadline.

- [x] **Step 2.10: Record the future Phase 3.4 boundary without committing.**

```bash
git diff --check -- backend/internal/backupasset backend/internal/settings backend/internal/sshutil/lifecycle.go backend/internal/sshutil/lifecycle_test.go
```

Expected: exit 0. These paths join Phase 3.4 commit `feat: repair backup asset publication schema` except Provider/repository/runtime files added in later tasks.

## 5. Task 3 — Joined Command Completion, Exact Backup Evidence, And Stored Summary Lookup

**Files:**

- Modify `backend/internal/sshutil/command_runner.go`, `command_runner_test.go`, `node_dialer.go`, and `node_dialer_test.go`.
- Consume the already-green `backend/internal/sshutil/lifecycle.go` join bound from Task 2; do not redeclare it in `command_runner.go`.
- Modify `backend/internal/backupasset/provider/contracts.go`, `contracts_test.go`, `runner.go`, `runner_test.go`, `registry.go`, `registry_test.go`, `restic.go`, and `restic_test.go`.
- Create `backend/internal/backupasset/provider/restic_publication.go`, `restic_publication_test.go`, and the six backup/snapshot fixture files from Section 2.1.

- [x] **Step 3.1: Add failing joined-completion tests at the SSH boundary.**

Define the wished-for API in the tests:

```go
type CommandCompletion struct {
	ExitCode        int
	ExitCodeKnown   bool
	Stderr          []byte `json:"-"`
	StderrTruncated bool
}

type CommandExecutionStream interface {
	io.Reader
	Join() (CommandCompletion, error)
	Cancel() error
}

func (runner *CommandRunner) OpenExecution(context.Context, CommandSpec) (CommandExecutionStream, error)
```

Add these cases with `fakeCommandSession`:

```go
func TestCommandRunnerExecutionJoinsExitZeroAfterNaturalEOF(t *testing.T)
func TestCommandRunnerExecutionReturnsExactNonzeroExit(t *testing.T)
func TestCommandRunnerExecutionRejectsJoinBeforeEOF(t *testing.T)
func TestCommandRunnerExecutionClassifiesTimeoutCancelOutputAndWaitUncertainty(t *testing.T)
func TestCommandRunnerExecutionClassifiesStdoutStderrReadStdinWriteAndCloseUncertainty(t *testing.T)
func TestCommandRunnerExecutionKeepsStdoutAndStderrSeparateAndBounded(t *testing.T)
func TestCommandRunnerExecutionCancelAndJoinAreIdempotent(t *testing.T)
```

Use a test error implementing `ExitStatus() int` for codes 3 and 17. Inject stdout/stderr read errors, secret-stdin write/close errors, stdout/stderr close errors, remote connection close errors, and a generic wait error independently; each is outcome uncertainty and returns no raw bytes in its error. Assert that an ordinary wait error has `ExitCodeKnown=false`, that early `Join` cancels, that bounded stderr truncation sets `StderrTruncated=true` while its reader continues draining to natural EOF, that a hard stdout limit cancels, that stderr bytes never appear in stdout reads, and that the runner semaphore is released only after `Join` or `Cancel` completes.

- [x] **Step 3.2: Run the SSH completion red test.**

```bash
go -C backend test ./internal/sshutil -run 'TestCommandRunnerExecution' -count=1
```

Expected: FAIL to compile because `OpenExecution`, `CommandCompletion`, and `CommandExecutionStream` do not exist; the Task 2 lifecycle constant remains green.

- [x] **Step 3.3: Implement the joined stream without changing existing `Run`/`Open` contracts.**

Add the two exact public types and `OpenExecution` method above, consuming the existing `CommandExecutionJoinTimeout`. Implement a new internal execution stream with these terminal rules:

```go
switch {
case parentContext.Err() != nil:
	return CommandCompletion{}, fmt.Errorf("command canceled: %w", parentContext.Err())
case runContext.Err() == context.DeadlineExceeded:
	return CommandCompletion{}, ErrCommandTimeout
case totalOutputLimitErr != nil:
	return CommandCompletion{}, ErrCommandOutputLimit
case stdoutReadErr != nil || stderrReadErr != nil || stdinWriteErr != nil ||
	stdinCloseErr != nil || stdoutCloseErr != nil || stderrCloseErr != nil || connectionCloseErr != nil:
	return CommandCompletion{}, ErrCommandFailed
case waitErr == nil:
	return CommandCompletion{ExitCode: 0, ExitCodeKnown: true, Stderr: stderrBytes, StderrTruncated: stderrTruncated}, nil
case errors.As(waitErr, &exitStatusError):
	return CommandCompletion{ExitCode: exitStatusError.ExitStatus(), ExitCodeKnown: true, Stderr: stderrBytes, StderrTruncated: stderrTruncated}, nil
default:
	return CommandCompletion{}, ErrCommandFailed
}
```

`Join` requires natural stdout EOF, waits for stderr/stdin/wait/connection close exactly once, and never returns raw stderr through an error. `Cancel` signals/terminates, joins all goroutines within `CommandExecutionJoinTimeout`, zeroes the copied secret buffer through the existing writer, releases the semaphore, and returns a sanitized lifecycle error. Existing `Open` behavior and its tests remain green.

- [x] **Step 3.4: Add failing Provider operation/transport tests.**

Add these command enums and interface in the test's desired API:

```go
const (
	CommandPurposePublish  CommandPurpose = "publish"
	CommandPurposeManifest CommandPurpose = "manifest"
	OperationResticBackup CommandOperation = "restic_backup"
	OperationResticSnapshotsByTags CommandOperation = "restic_snapshots_by_tags"
	OperationResticManifest CommandOperation = "restic_manifest"
)

type CommandStreamTransport interface {
	OpenExecution(context.Context, CommandInvocation, OperationLimits, int64) (CommandExecution, error)
}

type CommandCompletion struct {
	ExitCode        int
	ExitCodeKnown   bool
	Stderr          []byte `json:"-"`
	StderrTruncated bool
}

type CommandExecution interface {
	io.Reader
	Join() (CommandCompletion, error)
	Cancel() error
}
```

The bridge copies all four `sshutil.CommandCompletion` fields explicitly; it is not an undefined cross-package alias. Tests must assert:

- `restic_backup` accepts only `backup --json`, exactly two canonical generated tags, bounded repeated `--exclude value`, `--`, and one absolute source;
- `restic_snapshots_by_tags` accepts exactly one comma-separated canonical link+point AND filter;
- `restic_manifest` accepts only `ls --json --recursive -- full-64-id /`;
- Publish maps to existing `sshutil.PurposeTaskBackup`; lookup/manifest map to `PurposeRepositoryList`;
- `sshutil.DialAuditContext` gains `TaskRunID *uint`; publish writes one `task.credential.use` event with `PurposeTaskBackup`, while lookup/manifest write `repository.list`, and each carries the same actor, Task/TaskRun IDs, and correlation ID as its typed asset audit;
- `forget`, `prune`, `delete`, `restore`, `init`, `latest`, short IDs, extra flags, comma-bearing tags, NUL, and non-absolute sources are rejected before transport.

- [x] **Step 3.5: Run the Provider operation red tests.**

```bash
go -C backend test ./internal/backupasset/provider -run 'TestRunner.*(Publication|Backup|Manifest|Tags)|TestSSHCommandTransport.*Execution' -count=1
```

Expected: FAIL because the operations, purposes, streaming transport, and allowlist cases are absent.

- [x] **Step 3.6: Add real-shaped backup and stored-summary fixtures.**

`backup-success.ndjson` must contain one status row with a path-bearing `current_files`, one unknown future progress row, and this final newline-terminated summary:

```json
{"message_type":"summary","files_new":2,"files_changed":1,"files_unmodified":4,"dirs_new":1,"dirs_changed":0,"dirs_unmodified":2,"data_blobs":4,"tree_blobs":3,"data_added":8192,"data_added_packed":4096,"total_files_processed":7,"total_bytes_processed":16384,"total_duration":1.25,"snapshot_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","backup_start":"2026-07-14T11:00:00+08:00","backup_end":"2026-07-14T11:00:02.123456789+08:00","dry_run":false}
```

`backup-missing-summary.ndjson` ends after valid progress. `backup-malformed.ndjson` starts with malformed within-limit JSON and ends with a syntactically valid summary so the test can prove the parser still drains. `backup-truncated.ndjson` ends in a partial summary without a newline.

`snapshots-exact.json` is an array with one full ID, exact two tags, absent `original`, and a stored summary whose `backup_start`, `backup_end`, `total_files_processed`, and `total_bytes_processed` match the successful fixture. `snapshots-rewritten.json` contains three objects covering add-tag, set-same-tags, and add-then-remove-tag rewrites; every object has `original` set and a changed full ID.

- [x] **Step 3.7: Write failing parser and backup-result tests.**

Add table-driven tests with these exact cases:

```text
valid final summary -> known_exit_zero + full commit, UTC-normalized times
valid `Z` and `+08:00` timestamps -> identical UTC nanoseconds
unknown pre-summary message/fields -> accepted
missing/blank/non-string message_type -> evidence_malformed_stream after drain
status valid counters/ranges -> clamped safe progress; negative/overflow/wrong numeric type -> evidence_malformed_stream
verbose_status documented actions -> accepted; unknown/blank action or invalid counters -> evidence_malformed_stream
status/verbose_status/error item/path fields -> never logged or serialized
one over-record-limit row that remains within total limit -> bounded discard, evidence_malformed_stream, then natural drain/join
missing summary -> known_exit_zero + evidence_missing_summary
duplicate summary -> known_exit_zero + evidence_duplicate_summary
row after summary -> known_exit_zero + evidence_non_final_summary
malformed JSON -> known_exit_zero + evidence_malformed_stream after natural drain
truncated/no final newline -> known_exit_zero + evidence_malformed_stream
dry_run=true, uppercase/short/missing ID -> known_exit_zero + evidence_invalid_native_id
exit 3 or 17 with valid summary -> known_nonzero, no commit, exact exit code
stderr flood -> bounded StderrTruncated=true, no raw stderr exposure, and otherwise the joined exit/stdout result remains authoritative
timeout/cancel/stdout total limit/read/write/close/wait uncertainty -> outcome_unknown, no commit, ExitCode=-1
```

Add lookup tests proving one exact observation parses the persisted `Snapshot.Summary`; unknown snapshot fields are accepted; missing/invalid summary stays typed but cannot produce commit evidence; raw tag multiplicity plus `OriginalPresent`/`Original` preserve the distinction among absent, explicit null, empty string, and non-empty rewrite ID. Only absent or explicit null is eligible for automatic attribution. Assert `UnknownProviderExitCode == -1` and that every `CompletionOutcomeUnknown` result uses it while no known result may use it.

- [x] **Step 3.8: Run the Restic parser red test.**

```bash
go -C backend test ./internal/backupasset/provider -run 'TestRestic(Backup|Lookup|Publication|StoredSummary)' -count=1
```

Expected: FAIL because `ResticAdapter.Backup`, `LookupAttempt`, exact tag codec, and parser state are missing.

- [x] **Step 3.9: Implement the Provider stream bridge and strict allowlist.**

Add `CommandStreamTransport` and `CommandExecution` without changing `CommandTransport`. `SSHCommandTransport.OpenExecution` acquires the existing dynamic concurrency gate, creates the same safe `CommandSpec`, wraps `sshutil.OpenExecution`, and retains the SSH connection/gate until Provider `Join` or `Cancel` returns.

Validate backup operands structurally rather than by string concatenation:

```go
func validResticBackupArguments(args []string) bool {
	if len(args) < 10 || !equalArguments(args[:4], "--password-file", "/dev/stdin", "backup", "--json") {
		return false
	}
	index := 4
	for tag := 0; tag < 2; tag++ {
		if index+1 >= len(args) || args[index] != "--tag" || !validGeneratedResticTag(args[index+1]) {
			return false
		}
		index += 2
	}
	for index+2 < len(args) && args[index] == "--exclude" {
		if !validExclude(args[index+1]) {
			return false
		}
		index += 2
	}
	return index+2 == len(args) && args[index] == "--" && validAbsoluteSource(args[index+1])
}
```

Require exactly one link tag and one point tag in canonical order. The lookup invocation joins them as one comma-separated AND operand. No public caller supplies tags.

Extend `ResticAdapter` with `streamTransport CommandStreamTransport` and `publicationConfigSource PublicationConfigSource`. Preserve both existing read-only constructors exactly; implement the locked `NewResticAdapterWithPublication` constructor and require `transport` plus `streamTransport` to be the same concrete `*SSHCommandTransport` in production (tests may inject one fake implementing both interfaces). Resolve `FoundationService.PublicationConfig` on every **backup** call for `BackupStreamMaxBytes`; `BuildManifest` never reloads that source and uses only the validated `ManifestLimits` snapshot supplied by the fenced worker, so one scan cannot mix two dynamic configurations.

For backup, derive a fresh `OperationLimits` from the existing Provider limits but replace its timeout with `attempt.PointDeadlineAt.Sub(now) - sshutil.CommandExecutionJoinTimeout`; reject a non-positive remainder before transport. Pass `PublicationConfig.BackupStreamMaxBytes` as `OpenExecution`'s hard stdout total. Lookup uses the existing Provider metadata total. `SSHCommandTransport.OpenExecution` maps all completion fields, retains the shared dynamic gate/SSH closer through join, and maps publish to `PurposeTaskBackup`.

Extend `sshutil.DialAuditContext` with `TaskRunID *uint` and add `PurposeTaskBackup -> "task.credential.use"` to the NodeDialer action mapping. Before every publication invocation, copy `attempt.Audit` into the private `RemoteCommandAccess.Audit`, set both Task IDs, and reject a mismatched pre-populated audit context. The same correlation ID is therefore written once by NodeDialer for credential use and once by the coordinator's typed asset audit; no secret metadata is duplicated.

- [x] **Step 3.10: Implement Restic backup parsing and stored-summary lookup.**

In `restic_publication.go`:

- derive tags with fixed regexes `^xirang\.link\.v1\.[0-9a-f]{32}$` and `^xirang\.point\.v1\.[0-9a-f]{32}$`;
- validate `PublicationAttempt` completely before command construction;
- resolve the dynamic publication config, use its backup total limit, and reserve `sshutil.CommandExecutionJoinTimeout` before the immutable point deadline;
- use `bufio.Reader.ReadSlice('\n')` with explicit record and total counters rather than `bufio.Scanner`;
- on a semantic defect, remember the first stable evidence code and bounded-drain every later stdout record to EOF;
- require one final nonblank summary, `dry_run=false`, a lowercase 64-hex ID, legal RFC3339/RFC3339Nano offsets, non-zero times, and end not before start;
- call `Join` after EOF and let joined completion override parser state for nonzero/unknown outcomes;
- return `UnknownProviderExitCode` for every unknown completion, the exact joined exit for known results, and never combine a parser defect with an unknown/nonzero completion;
- emit progress containing only percent, throughput, safe file count, and observation time;
- never format `PublicationAttempt`, invocation, stdout, stderr, source, excludes, tags, identity, or native ID.

Implement lookup with `[]string{"snapshots", "--json", "--tag", attempt.RequiredTags[0] + "," + attempt.RequiredTags[1]}` and a strict JSON array decoder. Accept unknown fields, retain the raw tag slice, and use a small custom field decoder to preserve `original` presence separately from its nullable value. Require full IDs/non-zero snapshot times and parse stored summaries with checked counts/times. Do not choose among multiple rows; present-empty and present-nonempty `original` both fail strict attribution, while absent or explicit null is eligible.

Register the same `ResticAdapter` as `ResticPublisher` and later `ManifestBuilder`. Add Registry getters that return a typed capability error for a missing optional port.

- [x] **Step 3.11: Run green stream/parser/registry suites and a redaction scan.**

```bash
go -C backend test ./internal/sshutil ./internal/backupasset/provider -run 'CommandRunner|Runner|Registry|Restic' -count=1
go -C backend test ./internal/sshutil ./internal/backupasset/provider -run 'Execution|Backup|Lookup' -race -count=10
! rg -n 'FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY|/private/source|stderr payload' backend/internal/backupasset/provider/restic_publication.go backend/internal/backupasset/provider/runner.go
```

Expected: both Go commands PASS with no race; the final scan exits 0 because production code contains no fixture secret/path/output literal. The fixture intentionally retains fake path-bearing fields so parser tests can prove they never escape.

- [x] **Step 3.12: Record the future Phase 3.4 boundary without committing.**

```bash
git diff --check -- backend/internal/sshutil backend/internal/backupasset/provider
```

Expected: exit 0. These paths belong to Phase 3.4 commit `feat: capture exact Restic backup evidence`.

## 6. Task 4 — Exact Streaming Manifest And Minimum Provider Verification

**Files:**

- Create `backend/internal/backupasset/provider/restic_manifest.go`, `restic_manifest_test.go`, and the three manifest fixtures from Section 2.1.
- Modify `backend/internal/backupasset/provider/contracts.go`, `contracts_test.go`, `registry.go`, `registry_test.go`, `restic.go`, and `restic_test.go` only where the locked ManifestBuilder port requires it.

- [x] **Step 4.1: Add exact manifest fixtures.**

`manifest-valid.ndjson` contains one exact header and nodes for an empty directory, a regular file, a symlink, and every accepted special type (`dev`, `chardev`, `fifo`, `socket`, `irregular`). It uses only fake names and legal Restic fields.

`manifest-depth-edge.ndjson` contains this traversal order, which is valid depth-first but not globally path-sorted:

```text
/a
/a/child
/a/child/file
/a-
```

`manifest-rewritten.ndjson` contains a header with a changed full ID, the exact requested tags, and non-empty `original`; no node from this fixture may reach the canonical hasher.

All fixtures end with a newline. Header `time` is `2026-07-14T03:00:00Z`; the exact ID is 64 lowercase `a`; tags use 32 lowercase `b` for link and 32 lowercase `c` for point.

- [x] **Step 4.2: Write failing header/traversal/parser tests.**

Add these tests:

```go
func TestResticManifestRequiresExactIdentityIDTagsOriginalAndTime(t *testing.T)
func TestResticManifestAcceptsDepthFirstSiblingOrdering(t *testing.T)
func TestResticManifestRejectsDuplicateNoncanonicalAndReenteredPaths(t *testing.T)
func TestResticManifestRejectsUnknownRecordAndNodeTypes(t *testing.T)
func TestResticManifestKnownHeaderAndNodeRecordsTolerateUnknownFields(t *testing.T)
func TestResticManifestChecksOptionalNumericAndTimeRanges(t *testing.T)
func TestResticManifestCountsOnlyRegularFileLogicalBytes(t *testing.T)
func TestResticManifestRejectsHeaderBeforeCanonicalNodeEncoding(t *testing.T)
func TestResticManifestFidelityV1DeclaresExactIncludedCommitBoundAndNotExposedFields(t *testing.T)
```

The rewrite test must iterate add-tag, set-same-tags, and add-then-remove fixtures and expect `FailureProviderSnapshotRewritten`, even when the raw tag multiset currently equals the two required markers.

- [x] **Step 4.3: Run the manifest parser red test.**

```bash
go -C backend test ./internal/backupasset/provider -run 'TestResticManifest' -count=1
```

Expected: FAIL to compile because `BuildManifest`, `ManifestLimits`, `ManifestEvidence`, and the internal manifest-node decoder are absent.

- [x] **Step 4.4: Write failing canonical digest and incomplete-result tests.**

Add these cases:

```go
func TestResticManifestCompleteDigestIsChunkAndJSONFieldOrderIndependent(t *testing.T)
func TestResticManifestPreservesUTF8WithoutNormalizationOrCaseFolding(t *testing.T)
func TestResticManifestPartialDomainCannotCollideWithShorterComplete(t *testing.T)
func TestResticManifestUnavailableHasEmptyDigestAndZeroCounts(t *testing.T)
func TestResticManifestLimitsCancellationTruncationAndNonzeroCloseNeverComplete(t *testing.T)
func TestResticManifestReprobesRepositoryIdentityBeforeAndAfterEnumeration(t *testing.T)
```

For partial collision, hash the same accepted one-node prefix in two ways: complete with its complete trailer, and partial with `FailureProviderResourceLimit` plus its partial terminator. Assert different digests. Repeat the partial with `FailureEvidenceMalformedStream` and assert a third digest.

- [x] **Step 4.5: Run the manifest digest red test.**

```bash
go -C backend test ./internal/backupasset/provider -run 'TestResticManifest(Complete|Preserves|Partial|Unavailable|Limits|Reprobes)' -count=1
```

Expected: FAIL because the manifest canonicalizer and dedicated limits are missing.

- [x] **Step 4.6: Implement strict header and node decoding.**

Use one bounded `bufio.Reader` over `CommandExecution`. Accept `message_type` or legacy `struct_type` only when present values agree. Require one leading header before nodes and no second header. Known snapshot-header and node records ignore unknown object fields for Restic forward compatibility; unknown record kinds or native node types still fail completeness.

Validate every accepted node with this sequence:

```go
type manifestNode struct {
	Path       string
	Name       string
	NativeType string
	Size       *uint64
	Mode       *uint64
	UID        *uint64
	GID        *uint64
	ModTime    *time.Time
	AccessTime *time.Time
	ChangeTime *time.Time
	Inode      *uint64
}

func (state *walkState) accept(node manifestNode) error {
	canonical, parent, err := validateManifestPathAndName(node.Path, node.Name)
	if err != nil {
		return err
	}
	for len(state.frames) > 1 && state.frames[len(state.frames)-1].path != parent {
		state.frames = state.frames[:len(state.frames)-1]
	}
	if len(state.frames) == 0 || state.frames[len(state.frames)-1].path != parent {
		return errManifestTraversal
	}
	frame := &state.frames[len(state.frames)-1]
	if frame.lastChild != "" && node.Name <= frame.lastChild {
		return errManifestTraversal
	}
	frame.lastChild = node.Name
	if node.NativeType == "dir" {
		if len(state.frames) >= state.maxDepth {
			return errManifestDepth
		}
		state.frames = append(state.frames, walkFrame{path: canonical})
	}
	return nil
}
```

Initialize the frame stack with root `/`. Strictly increasing sibling names plus the inability to recover a popped parent reject duplicate/re-entered paths without a global seen set, keeping memory bounded by current record bytes and directory depth. Enforce `MaxEntries`, `MaxDepth`, `MaxRecordBytes`, and total bytes before allocations grow. Preserve exact UTF-8 bytes after slash canonicality validation; do not normalize Unicode or fold case.

Require the header's full ID, exact raw tag multiset, absent/null `original`, and snapshot time equal `ProviderCommitEvidence.CaptureStartedAt`. Decode header presence explicitly: absent or present-null is eligible, while present-empty or present-nonempty `original` returns `FailureProviderSnapshotRewritten`. Any extra/missing/duplicate tag does the same.

- [x] **Step 4.7: Implement the canonical byte stream.**

Use the shared `backupasset.CanonicalSHA256`. Strings use unsigned 32-bit big-endian lengths. Encode all eight optional fields with one `uint8` field-presence bitmap (`size=bit0`, `mode=bit1`, `uid=bit2`, `gid=bit3`, `mtime=bit4`, `atime=bit5`, `ctime=bit6`, `inode=bit7`), then emit only present values in that fixed order as signed/unsigned 64-bit big-endian values. The complete order is fixed by the approved design:

```text
string "xirang.restic.manifest.complete.v1"
string "restic"
string full_snapshot_id
uint32 tag_codec_version (exactly 1)
string "restic_depth_first_name_v1"
for each node:
  string canonical_path
  string native_type
  uint8 field_presence_bitmap
  present size, mode, uid, gid, mtime_ns, atime_ns, ctime_ns, inode in bit order
uint64 entry_count
uint64 regular_file_logical_bytes
```

The partial stream changes only the first domain to `xirang.restic.manifest.partial.v1` and replaces the two complete count fields with:

```text
string "partial_terminator"
string stable_failure_code
uint64 accepted_entry_count
uint64 accepted_regular_file_logical_bytes
```

It never emits the complete trailer. `unavailable` always has empty digest and zero counts. Convert final counts to `int64` only after checked `<= math.MaxInt64` validation.

- [x] **Step 4.8: Implement identity reprobe, stream completion, fidelity, and legacy-list isolation.**

Before opening `restic ls`, call the existing Restic probe and compare raw native identity, adapter revision, and capability revision with the attempt. After natural EOF and successful exit-zero `Join`, probe again and require the same values. A drift produces no complete manifest.

Validate the caller-supplied `ManifestLimits` once and use that immutable value for the whole build; do not consult `publicationConfigSource` inside `BuildManifest`. Wrap the entire probe → command → post-probe sequence in `context.WithTimeout(ctx, limits.Timeout)`. Pass `limits.MaxBytes` as the hard stream total, enforce `MaxRecordBytes`, `MaxEntries`, and `MaxDepth` in the parser, and reserve `sshutil.CommandExecutionJoinTimeout` for cancel/join. Timeout, caller cancellation, truncation, and transport/close uncertainty return typed non-complete evidence; no path ignores `ManifestLimits.Timeout`.

Return exactly:

```go
ManifestEvidence{
	DigestAlgorithm: "sha256",
	Generator: "xirang-restic-ls",
	GeneratorVersion: "1",
	Completeness: backupasset.ManifestComplete,
	Fidelity: ResticManifestFidelityV1(),
	EntryCount: checkedEntryCount,
	LogicalBytes: checkedLogicalBytes,
	HeaderCapturedAt: header.Time.UTC(),
	ObservedTagDigest: digestExactTags(header.Tags),
}
```

Call `ResticManifestFidelityV1()` to obtain an immutable-by-copy value, validate exact equality, then encode it with `json.Marshal`; persist the identical JSON in both the manifest revision and committed RecoveryPoint, and reject unknown/trailing fidelity JSON on read-back. The declared arrays are exactly those in the locked ledger; there is no exported mutable global, and an omitted or extra included/commit-bound/not-exposed claim fails the test. Legacy indexing does not call `ManifestBuilder`; Task 9 uses the bounded Provider `EntryLister` behind `LineageSession.ListEntries`, avoiding a publication fence/manifest side effect.

- [x] **Step 4.9: Run green manifest and Provider suites.**

```bash
go -C backend test ./internal/backupasset/provider -run 'TestRestic(Manifest|Probe|Runner|Registry)' -count=1
go -C backend test ./internal/backupasset/provider -run 'TestResticManifest' -race -count=10
```

Expected: PASS with no race. Every non-complete result is inactive by type and cannot report a complete digest/count projection.

- [x] **Step 4.10: Record the future Phase 3.4 boundary without committing.**

```bash
git diff --check -- backend/internal/backupasset/provider
```

Expected: exit 0. Manifest paths join Phase 3.4 commit `feat: capture exact Restic backup evidence`.

## 7. Task 5 — Generation Admission, Managed-History Latch, And Exact Lineage Guard

**Files:**

- Create `backend/internal/backupasset/publication/contracts.go`, `contracts_test.go`, `metrics.go`, and `metrics_test.go` with the ledger contracts and bounded-label Prometheus sink.
- Create `backend/internal/backupasset/runtime/admission.go` and `admission_test.go`.
- Create `backend/internal/backupasset/repository/managed_history.go`, `managed_history_test.go`, `lineage_guard.go`, and `lineage_guard_test.go`.
- Modify `backend/internal/backupasset/repository/service.go`, `testutil_test.go`, and `query_test.go` to inject the shared admission controller, lower managed-history resolver, metrics, and optional future tombstone source without creating schema.

- [x] **Step 5.1: Write failing admission token and drain tests.**

Add one table containing every `ResticOperation` from the ledger and run the same concurrency proof for each:

```go
func TestAdmissionTransitionDrainsEveryResticOperation(t *testing.T) {
	for _, operation := range allResticOperations {
		t.Run(string(operation), func(t *testing.T) {
			barrier := newTestAdmission(t, publication.AdmissionPristineLegacy)
			token, err := barrier.Acquire(context.Background(), operation)
			if err != nil {
				t.Fatal(err)
			}
			persisted := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- barrier.transition(context.Background(), publication.AdmissionManaged, func() error {
					close(persisted)
					return nil
				})
			}()
			select {
			case <-persisted:
				t.Fatal("transition persisted before admitted operation drained")
			case <-time.After(25 * time.Millisecond):
			}
			if err := token.Close(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
```

Also add:

```go
func TestAdmissionFailedDrainPreservesPriorModeAndGeneration(t *testing.T)
func TestAdmissionPersistFailureReopensPriorGeneration(t *testing.T)
func TestAdmissionTokenCloseIsIdempotentAndCannotUnderflow(t *testing.T)
func TestAdmissionTokenSnapshotsModeAndGenerationAcrossTransition(t *testing.T)
func TestAdmissionStopRejectsNewTokensAndWaitsForCurrentTokens(t *testing.T)
func TestAdmissionDoesNotUpgradeAnOperationTokenIntoTransition(t *testing.T)
func TestPublicationMetricsExposeOnlyFrozenBoundedLabels(t *testing.T)
func TestPublicationMetricsEncodeSuccessAsSuccessNeverUnknown(t *testing.T)
func TestPublicationMetricsMapUnknownTypedValuesToUnknownWithoutRawLabels(t *testing.T)
```

- [x] **Step 5.2: Run the admission red test.**

```bash
go -C backend test ./internal/backupasset/publication ./internal/backupasset/runtime -run 'TestAdmission|TestPublicationMetrics' -count=1
```

Expected: FAIL because the publication metrics sink, runtime package, and admission barrier do not exist.

- [x] **Step 5.3: Implement the barrier state machine.**

Use one mutex-protected state:

```go
type admissionBarrier struct {
	mu           sync.Mutex
	mode         publication.AdmissionMode
	generation   uint64
	active       int
	transitioning bool
	stopping     bool
	changed      chan struct{}
}
```

`Acquire` waits context-responsively while `transitioning`, rejects `stopping`, snapshots mode/generation/operation, increments active, and returns an exactly-once token. `Mode()` and `Generation()` are immutable for that token. A transition sets `transitioning=true`, prevents new tokens, waits for `active==0` without holding a DB/settings transaction, invokes the supplied persistence callback, and then either increments generation/installs the target mode or restores the prior mode/generation. Notify waiters by closing and replacing `changed`; do not use sleeps in production.

Implement `NewPrometheusMetrics` against the supplied registerer (production passes `prometheus.DefaultRegisterer`; tests pass a fresh registry) with these metric names and only the listed labels:

```text
xirang_backup_asset_publication_attempts_total{provider,stage}
xirang_backup_asset_publication_outcomes_total{provider,stage,code}
xirang_backup_asset_publication_backlog_count{state}
xirang_backup_asset_publication_backlog_oldest_age_seconds{state}
xirang_backup_asset_publication_reconcile_matches_total{class}
xirang_backup_asset_publication_fence_lost_total{stage}
xirang_backup_asset_publication_manifest_duration_seconds{completeness,limit_class}
xirang_backup_asset_publication_manifest_entries{completeness}
xirang_backup_asset_publication_manifest_bytes{completeness}
xirang_backup_asset_legacy_operation_blocked_total{operation}
xirang_backup_asset_publication_audit_failures_total{stage}
```

Validate every typed value against the ledger and map an invalid cast to literal `unknown`; `PublicationOutcomeSuccess` must label a successful record/commit as `success`, while `PublicationOutcomeFromFailure` preserves only a validated stable failure code. Never expose Task, path, host, Repository/native IDs, tags, correlation IDs, or error strings as labels. Tests gather the custom registry and compare its complete descriptor/label set.

Implement `publication.NewSystemLegacyBlockAuditContext` beside the typed operation ledger. It validates `ResticOperation`, then derives `legblk-` plus the first 16 SHA-256 bytes over `xirang.legacy-block.correlation.v1\x00`, base-10 Task ID, optional base-10 TaskRun ID, and the operation, each NUL-delimited; it returns a root `backupasset.PublicationAuditContext` and never imports publication types into the root domain package.

- [x] **Step 5.4: Write failing managed-history tests.**

Seed RecoveryPoints for every immutable lifecycle state with `semantics=native_snapshot`, then set both producing FKs null. Add:

```go
func TestManagedHistoryLatchIgnoresStateAndNullableLineage(t *testing.T)
func TestManagedHistoryLatchIsRepositoryScopedWhenBindingIsExact(t *testing.T)
func TestManagedHistoryLatchTreatsFutureTombstoneAsPermanent(t *testing.T)
func TestManagedHistoryLatchDoesNotTripFromMigrationOnlyOrMutableHead(t *testing.T)
```

The future-tombstone case uses an injected interface, not a table:

```go
type ManagedHistoryTombstoneSource interface {
	HasRepositoryManagedHistory(context.Context, string) (bool, error)
	HasInstallationManagedHistory(context.Context) (bool, error)
}

type ManagedHistoryResolverDependencies struct {
	DB         *gorm.DB
	Tombstones ManagedHistoryTombstoneSource
}

func NewManagedHistoryResolver(ManagedHistoryResolverDependencies) (*ManagedHistoryResolver, error)
func (resolver *ManagedHistoryResolver) HasRepositoryManagedHistory(context.Context, string) (bool, error)
func (resolver *ManagedHistoryResolver) HasInstallationManagedHistory(context.Context) (bool, error)
func (resolver *ManagedHistoryResolver) HasActivePublicationLease(context.Context) (bool, error)
```

Its fake returns true after all RecoveryPoint rows are absent, proving schema down/application rollback cannot infer safety only from current points. Child 14 must later implement this port from its retained tombstones.

- [x] **Step 5.5: Run the managed-history red test.**

```bash
go -C backend test ./internal/backupasset/repository -run 'TestManagedHistory' -count=1
```

Expected: FAIL because managed-history queries and the tombstone port are absent.

- [x] **Step 5.6: Write failing lineage-session tests.**

Add this complete decision matrix:

```text
flag false + no native point/tombstone -> compatibility, no link required
flag true + exact active native_snapshot link -> exact
flag true + no/mismatched link -> legacy_fallback_blocked before Provider I/O
flag false + repository managed history -> exact rollback-safe
flag false + installation history + unlinked/stale/ambiguous Restic Task -> fail closed
flag false + exact binding to a different pristine Repository -> compatibility
token mode rollback_safe + active-lease-only safety + no point/tombstone -> never compatibility; backup blocks and reads use exact/empty or fail closed
token mode managed + contradictory false hint/read -> close/retry or fail closed, never compatibility
```

Then prove exact sessions:

```go
func TestLineageExactUsesImmutableLineageAfterProducingFKsSetNull(t *testing.T)
func TestLineageExactRejectsLiveFKConflictWithImmutableLineage(t *testing.T)
func TestLineageRollbackSafeTokenWithActiveLeaseOnlyNeverUsesLegacyRead(t *testing.T)
```

- return only current Task's `committed` full IDs;
- continue returning a committed point after both nullable producing FKs are cleared, using its strict immutable `PublicationLineageV1.TaskID/TaskRunID/TaskRepositoryLinkID`; reject a non-null FK that conflicts with the immutable lineage;
- exclude another Task, manual, preparing, verifying, failed, expired, and rewritten/quarantine points;
- resolve 4–64 hex prefixes only within that committed set and reject zero/multiple matches;
- order current/previous by `captured_at DESC, recovery_point_id DESC`;
- produce the canonical link tag from the active link ID;
- keep the admission token open through caller response work;
- re-read the setting and latch after admission, so a pre-token compatibility decision cannot survive a generation transition.

- [x] **Step 5.7: Run the lineage red test.**

```bash
go -C backend test ./internal/backupasset/repository -run 'TestLineage' -count=1
```

Expected: FAIL because `publication.LineageGuard`, managed safety resolution, locator decoding, and prefix resolution are not implemented.

- [x] **Step 5.8: Implement permanent managed-history resolution.**

Current Child 3 persistence uses these cross-engine-safe predicates only:

```sql
SELECT COUNT(*) FROM recovery_points
WHERE semantics = 'native_snapshot';

SELECT COUNT(*) FROM recovery_points
WHERE repository_id = ? AND semantics = 'native_snapshot';
```

Do not filter by state or nullable producing FKs. `ManagedHistoryResolver` is a lower DB-backed query object with no admission, PublicationService, or runtime dependency; OR point results with its optional tombstone source and query active `point_publication` leases directly. Absence of a tombstone source means false for Child 3, not permission to ignore existing points. Propagate DB/source errors and fail closed. Runtime constructs this resolver before both AdmissionController and Repository Service and shares the same instance, eliminating a controller↔repository construction cycle.

- [x] **Step 5.9: Implement exact lineage sessions.**

`LineageGuard.Begin` must execute in this order:

1. acquire the requested admission token and snapshot its `Mode()`/generation;
2. load the current Restic Task;
3. re-read `backup_assets.enabled`;
4. load active TaskRepositoryLink and Repository when present;
5. resolve Repository- and installation-level managed history;
6. choose compatibility/exact/fail-closed from the tested matrix, with token mode as a safety floor: only `AdmissionPristineLegacy` may choose compatibility; rollback-safe chooses an exact session for a proven binding even when the current history query is empty, otherwise fails closed;
7. for exact mode, load committed native points for the bound Repository, strictly decode `PublicationLineageV1`, require its Task ID and active link ID to match, require any non-null producing Task/TaskRun FK to equal the immutable lineage, decrypt/strictly decode locators, and sort deterministically;
8. return a session that owns the token, or close the token on every error.

Use a versioned locator document:

```go
type resticPointLocatorV1 struct {
	Version       int    `json:"version"`
	Provider      string `json:"provider"`
	FullSnapshotID string `json:"full_snapshot_id"`
}
```

`repository.Service` implements the guard directly—there is no second lineage service or constructor:

```go
var _ publication.LineageGuard = (*Service)(nil)
```

Decode the locator with unknown-field/trailing-data rejection and require a full lowercase 64-hex ID. Never return it from JSON, audit, log, or public error.

- [x] **Step 5.10: Write and run the failing feature/down/downgrade transition tests required by Schema A.**

```go
func TestPrepareSchemaDownRejectsEveryActiveOperation(t *testing.T)
func TestPrepareSchemaDownRejectsActivePublicationLease(t *testing.T)
func TestPrepareSchemaDownRejectsRetainedTombstone(t *testing.T)
func TestPrepareSchemaDownInvokesMigrationOnlyAfterCleanDrain(t *testing.T)
func TestPrepareApplicationDowngradeRejectsHistoryLeaseAndEveryActiveOperation(t *testing.T)
func TestTransitionDisableRechecksHistoryAndLeaseAfterDrain(t *testing.T)
func TestPrepareSchemaDownRechecksTombstoneAfterDrain(t *testing.T)
func TestAdmissionInitializeUsesEnvironmentFallbackAndRollbackSafeHistory(t *testing.T)
func TestAdmissionInitializeActiveLeaseSelectsRollbackSafe(t *testing.T)
func TestAdmissionInitializeHistoryFailureLeavesControllerUninitialized(t *testing.T)
func TestAdmissionInitializeLeaseQueryFailureLeavesControllerUninitialized(t *testing.T)
func TestAdmissionCurrentModeRequiresSuccessfulInitialize(t *testing.T)
```

The first reuses every operation constant. The active-lease test has no point/tombstone history visible through the history query, inserts only an active `point_publication` lease fixture, and proves the application preflight rejects before its callback; the SQL down guard remains a second independent defense. The retained-history test injects a tombstone-only latch. The two post-drain race tests hold one admitted operation, begin the transition, make a native point/lease or tombstone visible while the drain is blocked, release the operation, and prove the controller re-reads safety state while admission is still exclusive. Disable with an active lease selects `AdmissionRollbackSafe`, never `AdmissionPristineLegacy`; a lease-query error aborts the transition and preserves the previous generation/value. Initialization with an active lease installs rollback-safe when the effective flag is false, while any history/lease query error returns a typed readiness error and leaves the controller uninitialized. In every rejection case, a callback counter remains zero.

```bash
go -C backend test ./internal/backupasset/runtime -run 'Test(PrepareSchemaDown|PrepareApplicationDowngrade|TransitionDisable|AdmissionInitialize)' -count=1
```

Expected: FAIL because `AdmissionController`, post-drain safety revalidation, environment-derived initialization, and down/downgrade callbacks do not exist.

- [x] **Step 5.11: Implement feature/schema transition policy over the barrier.**

Create an admission controller that owns the barrier and a `ManagedHistoryTombstoneSource`/repository resolver:

```go
func (controller *AdmissionController) TransitionFeature(ctx context.Context, enabled bool, persist func() error) error
func (controller *AdmissionController) PrepareApplicationDowngrade(ctx context.Context, downgrade func() error) error
func (controller *AdmissionController) PrepareSchemaDown(ctx context.Context, down func() error) error
func (controller *AdmissionController) Initialize(ctx context.Context) error
func (controller *AdmissionController) CurrentMode() (publication.AdmissionMode, error)

type AdmissionControllerDependencies struct {
	Foundation *backupasset.FoundationService
	History    *repository.ManagedHistoryResolver
}

func NewAdmissionController(AdmissionControllerDependencies) (*AdmissionController, error)
func (controller *AdmissionController) Acquire(context.Context, publication.ResticOperation) (publication.AdmissionToken, error)

var _ publication.Admission = (*AdmissionController)(nil)
var _ publication.FeatureTransitioner = (*AdmissionController)(nil)
```

Construct the controller from Foundation settings plus the shared lower `ManagedHistoryResolver`. `Initialize` reads the effective DB→environment→default feature value, resolves history and active publication leases, and installs `managed`, `rollback_safe`, or `pristine_legacy`; false plus either managed history or an active lease is always rollback-safe. A history/lease query error leaves the controller uninitialized and fails readiness. `CurrentMode` reads the mutex-protected installed mode and returns a typed not-initialized error before successful initialization; Runtime uses it only after `Initialize` to choose whether managed TaskRun readiness is required. Runtime—not the controller—owns the later worker/stale-TaskRun readiness pass, avoiding a Manager/worker construction cycle. For enable, target `AdmissionManaged`. For disable, target `AdmissionRollbackSafe` when managed history or an active lease exists, otherwise `AdmissionPristineLegacy`; a safety-query error aborts and preserves the previous state. Application downgrade and schema down both exclusively drain every command and reject any point/tombstone history **or active `point_publication` lease** before their callback. Schema SQL independently rejects active publication leases and native points as defense in depth.

Every disable, application downgrade, and schema-down operation closes admission and drains all tokens first, then—while the barrier is still exclusive—re-reads native-point history, retained tombstones, and active publication leases immediately before choosing the target mode or invoking the callback. History or an active lease that appears during the drain forces `AdmissionRollbackSafe` for disable and rejects application/schema downgrade; any safety-query error fails closed. No decision may use a pre-drain safety snapshot. A canceled/expired drain or readiness/persist failure never invokes the callback and preserves the prior generation/value.

Extend the existing Repository constructor exactly once; `repository.Service` uses these fields for `LineageGuard` decisions and guard-failure metrics, while `PublicationService` keeps its separate transaction-focused dependency struct:

```go
type Dependencies struct {
	DB         *gorm.DB
	Foundation *backupasset.FoundationService
	Registry   *provider.Registry
	Keyring    *backupasset.Keyring
	Now        func() time.Time
	Audit      AssetAuditSink
	Admission  publication.Admission
	History    *ManagedHistoryResolver
	Metrics    publication.Metrics
}
```

Production requires all three; focused tests may inject no-op typed fakes, never nil permissive fallbacks.

- [x] **Step 5.12: Run green admission/history/lineage suites.**

```bash
go -C backend test ./internal/backupasset/publication ./internal/backupasset/runtime ./internal/backupasset/repository -run 'Admission|ManagedHistory|Lineage|SchemaDown|ApplicationDowngrade|Metrics' -count=1
go -C backend test ./internal/backupasset/runtime ./internal/backupasset/repository -run 'Admission|Lineage' -race -count=20
```

Expected: PASS with no race, token leak, early transition callback, or legacy fallback after latch.

- [x] **Step 5.13: Record the future Phase 3.4 boundary without committing.**

```bash
git diff --check -- backend/internal/backupasset/publication backend/internal/backupasset/runtime backend/internal/backupasset/repository
```

Expected: exit 0. These files join Phase 3.4 commit `feat: publish fenced Restic recovery points`.

## 8. Task 6 — Deterministic Prepare, Evidence Record, Fencing, And Transfer/Publication Separation

**Files:**

- Create `backend/internal/backupasset/repository/publication_identity.go`, `publication_identity_test.go`, `publication_execution.go`, `publication_execution_test.go`, and `publication_commit_test.go`.
- Modify `backend/internal/backupasset/repository/service.go`, `audit.go`, `binding.go`, `binding_test.go`, `connect.go`, `connect_test.go`, and `testutil_test.go`.
- Modify `backend/internal/backupasset/lease.go` only if a red coordinator test exposes a missing transactional primitive already specified in Task 2.

- [ ] **Step 6.1: Write failing deterministic identity/tag tests.**

Add exact vectors:

```go
func TestDeriveRecoveryPointIDIsStableAndRunScoped(t *testing.T) {
	linkID := "0123456789abcdef0123456789abcdef"
	first, err := deriveRecoveryPointID(linkID, 42)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deriveRecoveryPointID(linkID, 42)
	if err != nil || first != second || len(first) != 32 {
		t.Fatalf("unstable point id: %q %q %v", first, second, err)
	}
	third, err := deriveRecoveryPointID(linkID, 43)
	if err != nil || third == first {
		t.Fatalf("run-scoped id not unique: %q %v", third, err)
	}
}
```

The implementation vector is SHA-256 over exact bytes:

```text
xirang.recovery-point.task-run.v1\x00
0123456789abcdef0123456789abcdef\x00
42
```

and the point ID is lowercase hex of the first 16 digest bytes. Tag tests require exactly:

```text
xirang.link.v1.0123456789abcdef0123456789abcdef
xirang.point.v1.f8d8a903c42c38398811387e4c201a28c
```

The point-tag value above is the exact expected vector for link ID `0123456789abcdef0123456789abcdef` and TaskRun ID `42`; other runs use the same prefix plus their computed 32-character lowercase point ID.

They reject comma, whitespace, uppercase, names, raw TaskRun text, and wrong prefixes.

- [ ] **Step 6.2: Run the identity red test.**

```bash
go -C backend test ./internal/backupasset/repository -run 'TestDeriveRecoveryPointID|TestPublicationTags' -count=1
```

Expected: FAIL because deterministic identity/tag helpers are absent.

- [ ] **Step 6.3: Write failing `Prepare` matrix tests.**

Create `PublicationService` through this constructor:

```go
type PublicationDependencies struct {
	DB          *gorm.DB
	Foundation  *backupasset.FoundationService
	Registry    *provider.Registry
	Lease       *backupasset.LeaseService
	Admission   publication.Admission
	Metrics     publication.Metrics
	Audit       AssetAuditSink
	History     *ManagedHistoryResolver
	Now         func() time.Time
	TryWake     func(string) bool
}

func NewPublicationService(PublicationDependencies) (*PublicationService, error)
```

Add tests:

```go
func TestPreparePristineDisabledReturnsSideEffectFreeCompatibilitySession(t *testing.T)
func TestPrepareEnabledRequiresExactActiveResticBindingBeforeMutation(t *testing.T)
func TestPrepareCreatesOneDeterministicPointAndExecutionLease(t *testing.T)
func TestPrepareCopiesImmutableTaskRunAndLinkLineage(t *testing.T)
func TestPrepareCopiesDatabaseTaskSnapshotInsteadOfCallerFields(t *testing.T)
func TestPrepareDuplicateSameRunNeverReturnsAnotherFenceOrReplaysBackup(t *testing.T)
func TestPrepareDifferentRetryRunCreatesDifferentPointAndTags(t *testing.T)
func TestPrepareImmutableConflictRollsBackWithoutSecondPoint(t *testing.T)
func TestPrepareProbeIdentityDriftFailsBeforePointAndProviderMutation(t *testing.T)
func TestPrepareLeaseRenewFailureCancelsExecutionContext(t *testing.T)
func TestPreparePropagatesOneSafeAuditContextIntoAssetAndCredentialAccess(t *testing.T)
func TestPrepareRacesFeatureTransitionAndUsesActualLegacyOrEvidenceOperationToken(t *testing.T)
func TestPrepareDisabledManagedHistoryBlocksLegacyBackupBeforeExecutorOrProvider(t *testing.T)
func TestPrepareDisabledUnlinkedTaskWithInstallationHistoryFailsClosed(t *testing.T)
func TestPrepareRollbackSafeTokenWithActiveLeaseOnlyBlocksLegacyExecutor(t *testing.T)
func TestExecutionCancelJoinsHeartbeatButRetainsAdmissionUntilTerminalChoice(t *testing.T)
func TestExecutionAbandonClosesAdmissionAndNeverMutatesPoint(t *testing.T)
```

The compatibility test records calls to prober, publisher, audit, lease, metrics, and DB row counts and requires all zero except the side-effect-free latch query and admission token. The evidence test requires a valid `Run.Audit`, live `native_snapshot` link, exact Repository identity probe, TaskRun row locked/owned by Task, one `preparing` point, and one active `point_publication` lease with the lineage deadline. Its returned attempt must contain the same actor/correlation and private `DialAuditContext` with both Task IDs.

- [ ] **Step 6.4: Run the `Prepare` red test.**

```bash
go -C backend test ./internal/backupasset/repository -run 'TestPrepare' -count=1
```

Expected: FAIL because `PublicationService`, coordinator `Prepare`, sessions, and deterministic point allocation do not exist.

- [ ] **Step 6.5: Implement identity, binding resolution, and `Prepare`.**

Create/load the point under this lock order:

```text
TaskRun -> RecoveryPoint -> active RecoveryPointLease
```

The evidence path is:

```go
// Read enabled only as a routing hint: legacy_backup when false, evidence_backup when true.
token, err := service.admission.Acquire(ctx, hintedOperation)
// Re-read enabled/latch after token. Treat token.Mode() as the non-overridable floor.
// If the hint and admitted generation disagree,
// close the token and retry routing under ctx; never authorize from the hint.
// False plus applicable Repository history returns legacy_fallback_blocked.
// False plus installation history and an unlinked/stale/ambiguous Restic Task also fails closed.
// AdmissionRollbackSafe never returns compatibility even when a later history/lease read is empty.
// No rollback-safe backup reaches credentials, SSH, executor, or Provider.
// Resolve the active Task link and repository-scoped encrypted binding.
// Verify the retained binding origin is still valid, then derive command
// access from the current linked Task's Node/config and require its live probe
// to match the retained native Repository identity.
// Probe exact native Repository identity outside the DB transaction.
// Load leaseConfig, err := service.foundation.LeaseConfig() before the transaction and return on err.
// Begin transaction; lock TaskRun; derive point/tags and
// pointDeadlineAt := preparedAt.Add(leaseConfig.AbsoluteDeadline);
// create/load point with that exact UTC deadline in PublicationLineageV1;
// acquire LeaseHolderPointPublication with the same point deadline; commit.
// Start heartbeat and return the session only after commit.
```

Persist a `RecoveryPoint` with:

```go
model.RecoveryPoint{
	ID: pointID, RepositoryID: repository.ID,
	ProducingTaskID: &taskID, ProducingTaskRunID: &runID,
	ProducingTaskNameSnapshot: taskEntity.Name,
	ProducingNodeIDSnapshot: taskEntity.NodeID,
	ProducingNodeNameSnapshot: taskEntity.Node.Name,
	LineageJSON: encodedLineage,
	Semantics: string(backupasset.PointNativeSnapshot),
	State: string(backupasset.RecoveryPointPreparing),
	ManifestDigestAlgorithm: "sha256",
	ConsistencyJSON: encodedEmptyConsistency,
	FidelityJSON: "{}",
	CapabilityRevision: repository.CapabilityRevision,
	CapabilitiesJSON: repository.CapabilitiesJSON,
	ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
	PhysicalAvailability: string(backupasset.PhysicalUnknown),
	HoldState: string(backupasset.HoldNone),
	CreatedAt: preparedAt, UpdatedAt: preparedAt,
}
```

Compare every immutable field on conflict. A live lease returns `ErrPublicationInProgress`; an expired/abandoned point is reconciliation-owned and never returns another execution fence. The stable lease owner is `point-publication`, scoped by point/holder in the existing unique slot.

The session heartbeat uses the configured lease heartbeat and cancels its derived context on the first renew error. `Cancel` closes/joins only the derived context and heartbeat, retaining admission until a terminal DB choice; `Abandon` calls the same cancellation then closes admission exactly once without a DB choice. `Cancel`/`Abandon` are mode-agnostic local lifecycle methods. Compatibility's only normal terminal method is `CompleteCompatibility`; evidence alone permits `RecordProviderCommit`, `Defer`, pre-command `Reject`, and post-command `Fail`. Every returned session is eventually completed, deferred, rejected, failed, or abandoned.

More exactly, `Cancel(cause) error` accepts only `ErrPublicationSessionAbandoned`, `ErrPublicationUnconfirmed`, a joined Provider cancellation, or a wrapped context cause; it cancels the derived context and waits for the heartbeat goroutine but keeps the admission token. `Abandon(cause) error` then closes that token exactly once. Neither method releases/changes the active DB lease, writes `Defer`, or chooses a point state; an abandoned lease short-expires for a new fence. The prepare audit is written before the caller may start Provider mutation. If it fails, increment `ObserveAuditFailure(StageExecution)`, emit a safe structured alert, call `Abandon`, and return without Provider I/O.

- [ ] **Step 6.6: Write failing commit/defer/fail and stale-fence tests.**

Add:

```go
func TestRecordProviderCommitAdvancesOnlyPreparingToVerifying(t *testing.T)
func TestRecordProviderCommitPersistsEncryptedLocatorAndSafeDigestEnvelope(t *testing.T)
func TestRecordProviderCommitIsIdempotentOnlyForByteEquivalentEvidence(t *testing.T)
func TestRecordProviderCommitConflictCannotClaimAnotherRunOrNativePoint(t *testing.T)
func TestRecordProviderCommitReleasesExecutionLeaseAndWakeNeverBlocks(t *testing.T)
func TestRecordProviderCommitLostResponseRetriesSameOperationWithoutDowngrade(t *testing.T)
func TestPublicationDeferPersistsOnlyCompletionAndSafeCode(t *testing.T)
func TestPublicationRejectAllowsOnlyPreCommandPreconditionFailure(t *testing.T)
func TestPublicationFailRejectsUnknownCodesAndNeverOverwritesCommitted(t *testing.T)
func TestPublicationMutationRejectsStaleFenceInsideSameTransaction(t *testing.T)
func TestPublicationFailureNeverChangesTaskRunTransferFields(t *testing.T)
func TestPublicationStateAuditsContainOnlyActorOpaqueIDsSafeCountsCodeAndCorrelation(t *testing.T)
func TestPublicationAuditFailureNeverRollsBackProviderTruthOrLeaksRawFacts(t *testing.T)
func TestRecordLegacyBlockWritesTypedAuditAndMetricWithoutRawFacts(t *testing.T)
```

At-rest tests must query raw SQL, assert `encrypted_provider_locator` starts with the active encryption envelope rather than containing the full ID, and assert `lineage_json`, `consistency_json`, audit rows, and logs contain no full ID, raw identity, or tags.

- [ ] **Step 6.7: Run the commit-state red tests.**

```bash
go -C backend test ./internal/backupasset/repository -run 'Test(RecordProviderCommit|PublicationDefer|PublicationReject|PublicationFail|PublicationMutation|PublicationFailure)' -count=1
```

Expected: FAIL because record/defer/fail transaction owners and idempotent evidence comparison are absent.

- [ ] **Step 6.8: Implement canonical commit/fingerprint helpers.**

The source fingerprint is exactly:

```go
func resticSourceFingerprint(identity, fullID string) string {
	sum := sha256.Sum256([]byte("xirang.restic.native-point.v1\x00" + identity + "\x00" + fullID))
	return hex.EncodeToString(sum[:])
}
```

The commit digest is SHA-256 over a length-delimited envelope in this exact order:

```text
xirang.restic.provider-commit.v1
provider kind
raw repository identity
full native ID
UTC start nanoseconds
UTC end nanoseconds
files processed
logical bytes
requested-tag digest
adapter revision
capability revision
```

Use `backupasset.CanonicalSHA256` from Task 2 rather than a Provider-local or Repository-local encoder. Validate identity/attempt/evidence equality and checked counts before hashing.

- [ ] **Step 6.9: Implement `RecordProviderCommit` in one fenced transaction.**

Use `context.WithTimeout(context.WithoutCancel(caller), sshutil.CommandExecutionJoinTimeout)`. First lock/read the point and perform the read-only replay branch: if state is already `verifying|committed`, decrypt and require byte-equivalent locator, source fingerprint, summary, requested-tag, adapter/capability, and commit digests; return the current safe outcome without requiring the released execution fence. A mismatch is `ErrConflict`. Only a still-`preparing` point enters the fenced mutation transaction:

1. lock the point;
2. call `ValidateFenceTx` on the same transaction;
3. decode/compare lineage, state, link, run, Repository, deadline, requested tags, adapter/capability revisions, and evidence identity;
4. compute source fingerprint and rely on both database unique indexes;
5. save the versioned encrypted locator with `tx.Save(&point)` so model hooks run;
6. persist only safe summary facts/digests in `PublicationConsistencyV1`;
7. validate `preparing -> verifying` through the domain state machine;
8. release the execution fence with `ReleaseTx`;
9. commit, increment typed outcome metrics, call `_ = service.tryWake(point.ID)`, and write the transition audit with `Run.Audit`; `false` only means the bounded queue was full/stopping, because the durable `verifying` row remains recoverable.

When the DB driver reports an indeterminate commit and the bounded read-back can prove neither byte-equivalent `verifying|committed` nor definite still-`preparing` rollback under the same fence, return an error wrapping `ErrPublicationUnconfirmed`. Manager branches only with `errors.Is`, retries this exact method/value, and never calls `Defer`. Audit writes occur only when the state-transition update affected one row, so replay creates no duplicate. A post-commit audit failure increments the audit-failure metric and emits a safe alert but never rolls back or weakens the Provider fact.

- [ ] **Step 6.10: Implement typed `Defer`, pre-command `Reject`, and post-command `Fail`.**

`Defer` validates the completion/code pairing from Task 2, writes only completion, safe code, revision/attempt/time, and leaves the point `preparing` or `verifying`. Invalid stdout bytes are never persisted. Transient defer leaves an active lease to short-expire after heartbeat stops. `Reject` accepts only `publication_precondition_missing` while the Manager has not invoked a Provider method; it moves `preparing -> failed`, releases the fence, and the test requires zero Publisher/executor calls. `Fail` accepts only the joined-command terminal allowlist, validates/revalidates the current fence in the point transaction, moves only `preparing|verifying -> failed`, and releases the lease. Replays with identical terminal facts return nil without a duplicate audit event; neither `Reject` nor `Fail` returns a row.

`CompleteCompatibility`, a confirmed/read-back `RecordProviderCommit`, `Defer`, `Reject`, and `Fail` each cancel/join the heartbeat then close admission exactly once after their synchronous DB/audit work. An unconfirmed commit deliberately does neither so Manager can retry the same operation before choosing `Abandon`.

`PublicationService` also implements the narrow legacy block recorder used by Task Manager:

```go
var _ publication.LegacyBlockRecorder = (*PublicationService)(nil)
```

`RecordLegacyBlock` validates Task/optional run identity, operation, and audit context; increments `ObserveLegacyBlocked(operation)` and writes exactly one `restic_legacy_operation_blocked` event with stable `legacy_operation_blocked`, stage `execution`, and correlation. An audit failure increments `ObserveAuditFailure(StageExecution)` and returns a sanitized error to the caller, but the caller still blocks the legacy operation and never reaches credentials/SSH.


- [ ] **Step 6.11: Run green prepare/commit/fencing suites.**

```bash
go -C backend test ./internal/backupasset/repository -run 'Identity|Tags|Prepare|RecordProviderCommit|Publication' -count=1
go -C backend test ./internal/backupasset/repository -run 'Prepare|RecordProviderCommit|PublicationMutation' -race -count=20
```

Expected: PASS with no duplicate point/claim, replayed backup, stale-fence mutation, raw evidence, blocked wake, or race.

- [ ] **Step 6.12: Record the future Phase 3.4 boundary without committing.**

```bash
git diff --check -- backend/internal/backupasset/repository backend/internal/backupasset/lease.go
```

Expected: exit 0. These files join Phase 3.4 commit `feat: publish fenced Restic recovery points`.

## 9. Task 7 — Asynchronous Manifest Worker, Stored-Summary Recovery, Deadlines, And Restart Reconciliation

**Files:**

- Create `backend/internal/backupasset/repository/publication_reconcile.go`, `publication_reconcile_test.go`, and `publication_integration_test.go`.
- Create `backend/internal/backupasset/runtime/publication_worker.go` and `publication_worker_test.go`.
- Modify `backend/internal/backupasset/repository/publication_execution.go` only to share its internal canonical record/manifest transaction helpers.
- Modify `backend/internal/backupasset/provider/restic_publication.go` and tests only if stored-summary lookup lacks a case required by reconciliation.

- [ ] **Step 7.1: Write failing verifying-point manifest commit tests.**

Add:

```go
func TestProcessVerifyingPointUsesFreshFenceAndOriginalDeadline(t *testing.T)
func TestProcessVerifyingPointReconstructsAndDigestChecksCommitEvidence(t *testing.T)
func TestProcessVerifyingPointBuildsManifestOutsideTransaction(t *testing.T)
func TestProcessVerifyingPointCommitsOnlyActiveCompleteManifest(t *testing.T)
func TestProcessVerifyingPointPersistsInactivePartialOrUnavailableDiagnostic(t *testing.T)
func TestProcessVerifyingPointRejectsLateManifestAfterTakeover(t *testing.T)
func TestProcessVerifyingPointRejectsIdentityTagIDTimeAndCapabilityDrift(t *testing.T)
func TestProcessVerifyingPointDetectsTagRewriteWhenCommittedIDDisappears(t *testing.T)
func TestProcessVerifyingPointOutcomeSelectsImmutableSameTaskPreviousCommittedPoint(t *testing.T)
func TestProcessVerifyingPointManifestAdmissionDrainsOnlyAfterCommandJoinAndCommit(t *testing.T)
func TestProcessVerifyingPointFailureClassificationMatrix(t *testing.T)
```

Use a blocking ManifestBuilder to assert another DB connection can read the point while enumeration is blocked, proving no long transaction is open. Rotate the lease before releasing the fake builder and require the late result to return `ErrLeaseFenceLost` with zero active manifest and unchanged point state.

- [ ] **Step 7.2: Run the verifying-point red tests.**

```bash
go -C backend test ./internal/backupasset/repository -run 'TestProcessVerifyingPoint' -count=1
```

Expected: FAIL because `ProcessPoint`, manifest-stage acquire/takeover, and the manifest commit transaction do not exist.

- [ ] **Step 7.3: Implement manifest-stage claim and commit.**

`ProcessPoint` may read only point ID/state to select an operation. For `verifying`, acquire `OperationManifest` before reading the feature flag, latch, binding, locator, or lease; re-read the point/state after admission and hold that token through command cancel/join, fenced commit/audit, and safe outcome construction. The claim transaction then locks point before lease, skips a live lease, takes over an expired lease or acquires a fresh one with the immutable lineage deadline, increments `PublicationConsistencyV1.PublicationRevision` and `AttemptCount`, writes `LastAttemptAt`, and updates `updated_at` before Provider I/O. A state change between the initial hint and post-admission read closes the token without Provider I/O.

Outside the transaction:

1. start lease heartbeat;
2. decrypt/decode the exact locator;
3. load active binding and re-probe raw Repository identity;
4. reconstruct the original commit envelope from locator, live matching identity, persisted safe summary, requested tags, adapter revision, and capability revision;
5. require its digest to equal `provider_commit_digest`;
6. invoke `ManifestBuilder.BuildManifest` with the current attempt/fence and the freshly loaded fixed limits, including `ManifestTimeout`;
7. stop/join on cancellation or fence loss.

For a complete result, one transaction locks point then lease, revalidates the current fence/deadline/state/consistency blob/evidence digest/source fingerprint/manifest header, inserts the next manifest revision, marks it active, stores encrypted commit evidence plus observed-tag attestation, copies digest/count/bytes/fidelity and header snapshot time to the point, moves `verifying -> committed`, sets UTC `committed_at`, and releases the lease. In that same consistent view, select the immediately preceding committed point for the same strict immutable Task/link lineage, ordered by `captured_at DESC, recovery_point_id DESC`, excluding the current point and applying the same FK-null fallback/live-FK conflict rejection as `LineageGuard`; return its full ID only in-memory in `Outcome.PreviousNativePointID`. Cross-Task, manual, uncommitted, failed, and lineage-conflicting points are never predecessors.

Use this encrypted evidence document:

```go
type manifestCommitEvidenceV1 struct {
	Version            int                      `json:"version"`
	Provider           backupasset.ProviderKind `json:"provider"`
	RepositoryIdentity string                   `json:"repository_identity"`
	NativePointID      string                   `json:"native_point_id"`
	CaptureStartedAt   time.Time                `json:"capture_started_at"`
	CaptureFinishedAt  time.Time                `json:"capture_finished_at"`
	FilesProcessed     uint64                   `json:"files_processed"`
	LogicalBytes       uint64                   `json:"logical_bytes"`
	ObservedTags       [2]string                `json:"observed_tags"`
	ObservedTagDigest  string                   `json:"observed_tag_digest"`
}
```

Do not embed `provider.ProviderCommitEvidence` here because its raw identity/ID fields deliberately carry `json:"-"`. Build this private document explicitly, validate it, and pass the complete plaintext only to the model encryption setter; the model hook encrypts the entire JSON before storage. Never map-update `encrypted_commit_evidence`. Partial/unavailable revisions use a deterministic opaque ID derived from point ID plus lease attempt, remain inactive, and never project counts/digest to RecoveryPoint.

Freeze this manifest-stage result matrix in `TestProcessVerifyingPointFailureClassificationMatrix`:

```text
complete + exact identity/ID/tags/time/fidelity -> committed, active manifest, release lease
timeout/offline/caller cancel/shutdown/truncated EOF/transport close uncertainty -> verifying, inactive partial|unavailable diagnostic, leave lease to short-expire
configured byte/entry/record/depth limit -> failed/provider_resource_limit, inactive diagnostic, release lease
unsupported record/node/protocol -> failed/manifest_unavailable, inactive diagnostic, release lease
tag/original/ID/time rewrite -> failed/provider_snapshot_rewritten, inactive diagnostic, release lease
exact committed ID not found + exact-marker lookup returns changed ID/original/tag drift -> quarantine observation, failed/provider_snapshot_rewritten, never replace locator/fingerprint
exact committed ID not found + exact-marker lookup returns zero rows -> transient verifying until fixed deadline
Repository identity/capability drift -> failed/repository_identity_drift, inactive diagnostic, release lease
stale fence/takeover -> no row change, ErrLeaseFenceLost
```

Every transition writes the matching typed verify/commit/fail audit with the operation audit context and records attempt/outcome/fence/manifest metrics. The repository returns only `Outcome`; it never invokes `CommitObserver` or `InterruptedRunReporter`.

If exact-ID manifest lookup reports native not-found, use the already-held `OperationManifest` admission and current publication fence to perform exactly one `ResticPublisher.LookupAttempt` with the two exact markers; do not acquire a nested reconciliation token. A changed ID, non-empty `original`, or tag drift is persisted only as encrypted quarantine evidence and fails `provider_snapshot_rewritten`. Zero matches remains transient until the immutable deadline. The rewritten/current Provider ID can never replace the original recorded locator or source fingerprint.

For restart/worker operations, derive the system audit correlation as `"pubw-" + hex(first16(SHA256("xirang.publication.worker-correlation.v1\x00" + pointID + "\x00" + base10(publicationRevision) + "\x00" + operation)))`, validate it, and copy it into both the asset event and private credential `DialAuditContext`. For point `0123456789abcdef0123456789abcdef`, revision 1, freeze `manifest -> pubw-26cb76b68d127a7892139f481e89815d` and `reconcile -> pubw-4bbff93d4eea56d84da7f14175aa9de4`.

- [ ] **Step 7.4: Write failing preparing reconciliation tests.**

Add the complete matrix:

```go
func TestReconcilePreparingZeroMatchKeepsStableMissingOriginUntilDeadline(t *testing.T)
func TestReconcilePreparingMissingGraceEmitsBoundedSafeAuditAndMetric(t *testing.T)
func TestReconcilePreparingKnownExitZeroRebuildsFromValidStoredSummary(t *testing.T)
func TestReconcilePreparingNeverUsesRejectedStdoutAsEvidence(t *testing.T)
func TestReconcilePreparingOutcomeUnknownOrMarkerAbsentQuarantinesCompletionUnproven(t *testing.T)
func TestReconcilePreparingKnownNonzeroNeverPublishesSnapshot(t *testing.T)
func TestReconcilePreparingRewriteFailsWithoutClaimingChangedNativeID(t *testing.T)
func TestReconcilePreparingMissingInvalidOrDriftedStoredSummaryFailsClosed(t *testing.T)
func TestReconcilePreparingMultipleMatchesAndNativeConflictFailClosed(t *testing.T)
func TestReconcilePreparingTransientProviderFailureRemainsPending(t *testing.T)
func TestReconcileNeverUsesLatestPrefixTimeDifferenceOrLegacyIndex(t *testing.T)
func TestReconcilePreparingAdmissionDrainsOnlyAfterLookupJoinAndStateCommit(t *testing.T)
func TestPublicationSharedRepositoryConcurrentTasksRetriesAndManualSnapshotsNeverCrossClaim(t *testing.T)
```

The valid recovery test begins with only `CompletionKnownExitZero` and an evidence defect code in consistency, returns one exact unmodified snapshot with a valid stored summary, and expects a canonical commit record followed by `verifying`. It must not preload the rejected stdout summary anywhere. The marker-absent test models a process death after remote exit but before durable outcome and requires `failed/provider_completion_unproven` plus encrypted quarantine locator/fingerprint.

The shared-Repository integration test seeds two linked Tasks, concurrent TaskRuns, an automatic retry with a new TaskRun ID, one manual untagged snapshot, and one other-Task tagged snapshot. Prepare/record runs concurrently, then reconcile exact-tag fixtures in adversarial order. Assert each run has its deterministic point and only its own full ID/fingerprint; the retry is distinct; manual/untagged and cross-Task snapshots remain unclaimed; the two database unique indexes reject any attempted relabel.

- [ ] **Step 7.5: Run the preparing reconciliation red tests.**

```bash
go -C backend test ./internal/backupasset/repository -run 'TestReconcilePreparing|TestReconcileNever' -count=1
```

Expected: FAIL because exact-tag reconciliation, completion-marker policy, quarantine, and deadline logic are absent.

- [ ] **Step 7.6: Implement preparing-point reconciliation.**

For a `preparing` point, acquire `OperationReconcile` before reading feature/latch/binding/lease/coordination facts, re-read the state after admission, then acquire/take over the same `point_publication` owner slot. Hold the admission token until `LookupAttempt` naturally drains/joins and the fenced state/audit transaction finishes. Call only `ResticPublisher.LookupAttempt` with the two generated markers; never nest an `OperationManifest` token while holding reconciliation admission.

Apply this exact order:

```text
0 rows -> set first_missing_observed_at once; after missing grace emit one deduplicated typed reconcile audit plus bounded backlog/outcome metrics, then remain preparing
>1 rows -> failed/ambiguous_run_tags
1 row with original, non-exact tag multiset, ID/time rewrite -> encrypted quarantine + failed/provider_snapshot_rewritten
1 row + known_exit_zero + valid stored summary -> canonical commit record under reconciliation fence -> verifying
1 row + outcome_unknown, known_nonzero, or no completion marker -> encrypted quarantine + failed/provider_completion_unproven
identity/native claimant conflict -> failed closed
transient timeout/offline/cancel -> remain preparing; lease short-expires
```

Stored-summary validation requires non-zero ordered times, full ID, exact two-tag multiset, absent `original`, snapshot time equal summary backup start, checked counts, and agreement with any persisted coordination facts. Construct a fresh `ProviderCommitEvidence` only from that typed snapshot object. Never consult TaskRun finish time, old SnapshotFileIndex, repository ordering, prefix, `latest`, or a before/after set.

The missing-grace event uses `recovery_point_publication_reconcile` and the deterministic worker correlation. Its audit/metric projection contains only opaque Repository/point IDs, stable code, bounded age/count, match class, and stage; it contains no locator, full native ID, tag, Repository identity, path, raw output, or error text. The immutable `first_missing_observed_at` records the origin, while optional UTC `missing_grace_reported_at` is set in the same fenced consistency transaction exactly once before the audit/metric projection; this survives restart and makes replay deduplicated.

- [ ] **Step 7.7: Write failing deadline/fairness/batch tests.**

Add:

```go
func TestExpireAtDeadlineRequiresElapsedDeadlineNoLiveLeaseAndExactRevision(t *testing.T)
func TestFreshLeaseStageAndRestartNeverExtendPointDeadline(t *testing.T)
func TestListCandidatesKeysetRotatesPastLiveAndPoisonPoints(t *testing.T)
func TestProcessPointCrashBeforeDeferStillAdvancesDurableAttempt(t *testing.T)
func TestListCandidatesProcessesOnlyResticPreparingOrVerifyingPoints(t *testing.T)
func TestPublicationBackoffUsesDurableAttemptAndLastAttemptWithExactCap(t *testing.T)
func TestPublicationBackoffNeverHidesElapsedDeadline(t *testing.T)
func TestHasUnresolvedPublicationIncludesLiveLeaseBackoffAndBeyondScanCeiling(t *testing.T)
```

`ExpireAtDeadline` must fail its CAS after any consistency revision changes. It may choose only `snapshot_missing_at_deadline` for a never-observed zero match or `publication_deadline_exceeded` for every other reconcilable failure.

- [ ] **Step 7.8: Run the deadline/fairness/batch red tests.**

```bash
go -C backend test ./internal/backupasset/repository -run 'Test(ExpireAtDeadline|FreshLeaseStage|ListCandidates|ProcessPointCrash|PublicationBackoff|HasUnresolvedPublication)' -count=1
```

Expected: FAIL because deadline CAS, durable backoff eligibility, bounded keyset selection, pre-I/O attempt rotation, and the unfiltered readiness query are absent.

- [ ] **Step 7.9: Implement `ExpireAtDeadline` and bounded keyset reconciliation.**

`ExpireAtDeadline` is the only no-live-fence state mutation. It locks point and newest lease, requires the immutable deadline elapsed, requires no active/live lease, matches the exact prior state and serialized consistency/revision in its update predicate, and moves only `preparing|verifying -> failed`. Since acquire/renew reject the elapsed absolute deadline, an old callback cannot become valid later.

`ListCandidates(ctx, limit)` keyset-pages by `updated_at ASC, id ASC`, overfetches enough rows to pass live leases/backoff-ineligible points, stops at a bounded scan ceiling of `max(limit*10, limit)`, and returns at most `limit` opaque point IDs. It never performs Provider I/O; `ProcessPoint` performs the post-admission claim that updates attempt/revision/timestamp before Provider work.

`HasUnresolvedPublication(ctx)` is a separate unfiltered readiness query, not an alias for `ListCandidates`. It returns true for every Child-3 Restic `native_snapshot` point in `preparing|verifying`, including live-lease, backoff-ineligible, deadline-eligible, and beyond-keyset-scan rows; it has no batch/scan ceiling and performs no Provider I/O or mutation. It decodes the versioned lineage/consistency needed to prove Restic ownership and returns an error on an invalid codec/query rather than reporting ready. Terminal states and non-Restic semantics do not count.

Derive bounded retry eligibility only from existing durable fields:

```go
func publicationBackoff(attempt uint64) time.Duration {
	if attempt <= 1 {
		return 30 * time.Second
	}
	delay := 30 * time.Second * time.Duration(uint64(1)<<min(attempt-1, 5))
	return min(delay, 15*time.Minute)
}
```

The exact sequence is `30s, 1m, 2m, 4m, 8m, 15m, 15m...`. For a transient/zero-match pending point, eligibility is `max(last_attempt_at + publicationBackoff(attempt_count), active_lease_expires_at)`; if the immutable point deadline is earlier, eligibility is the deadline so `ExpireAtDeadline` is never hidden. Deterministic terminal codes do not back off because their state leaves the candidate set. Dynamic reconciliation interval controls polling but never rewrites these durable facts.

- [ ] **Step 7.10: Write failing worker lifecycle tests.**

Construct the worker with:

```go
type PublicationWorkerDependencies struct {
	Foundation *backupasset.FoundationService
	Reconciler publication.Reconciler
	Observer   publication.CommitObserver
	Reporter   publication.InterruptedRunReporter
	Metrics    publication.Metrics
	Now        func() time.Time
}

func NewPublicationWorker(PublicationWorkerDependencies) (*PublicationWorker, error)
func (worker *PublicationWorker) StartupPass(context.Context) error
func (worker *PublicationWorker) TryWake(string) bool
func (worker *PublicationWorker) Run(context.Context)
func (worker *PublicationWorker) Shutdown(context.Context) error
```

Add:

```go
func TestPublicationWorkerStartupPassRunsBeforePeriodicLoop(t *testing.T)
func TestPublicationWorkerWakeIsNonblockingAndDurableStateRecoversLostWake(t *testing.T)
func TestPublicationWorkerHonorsDynamicBatchConcurrencyAndInterval(t *testing.T)
func TestPublicationWorkerFeatureDisableStopsNewClaimsAndKeepsDurableRows(t *testing.T)
func TestPublicationWorkerShutdownRejectsWakeCancelsAndJoinsActiveWork(t *testing.T)
func TestPublicationWorkerRestartResumesVerifyingAndPreparingPoints(t *testing.T)
func TestPublicationWorkerReporterFailureDoesNotRollBackPublication(t *testing.T)
func TestPublicationWorkerObserverIsAtMostOnceAndOnlyAfterCommittedOutcome(t *testing.T)
func TestPublicationWorkerSemaphoreBoundsWakeAndCandidateProcessPointTogether(t *testing.T)
```

- [ ] **Step 7.11: Run the worker red test.**

```bash
go -C backend test ./internal/backupasset/runtime -run 'TestPublicationWorker' -count=1
```

Expected: FAIL because the worker does not exist.

- [ ] **Step 7.12: Implement the bounded worker.**

Use a bounded buffered wake channel sized to the dynamic maximum batch (`1000`) and a runtime-owned stop channel. `TryWake` validates opaque point ID, checks stopping under a mutex/atomic flag, and performs a non-blocking send. A full channel returns false; it never blocks a TaskRun.

`StartupPass` calls `ListCandidates(batchSize)`, submits every returned ID through one worker-owned dynamic semaphore, calls `ProcessPoint` per ID, and waits for that bounded set. The semaphore re-reads `PublicationConfig.WorkerConcurrency` on every acquire under a mutex/changed-channel gate, so both increases and decreases apply without replacing a live channel or oversubscribing. `Run` re-reads the remaining config each cycle; wake IDs and periodic candidate IDs share that same gate and de-duplicate only concurrently active opaque IDs. When the feature is false, it accepts no new publication claims; rows remain durable for re-enable. `Shutdown` marks stopping, cancels active contexts, and waits on a WaitGroup within the caller deadline (which must allow `sshutil.CommandExecutionJoinTimeout`); it never closes a producer-visible channel.

After each non-empty safe outcome, call `InterruptedRunReporter`; reporter errors are structured warnings only. A committed outcome calls `CommitObserver` at most once in the current worker execution after `ProcessPoint` returns committed and before reporter; repository code never calls it. The callback is explicitly best-effort: a process crash after DB commit and before callback may yield zero calls, and no outbox/durable exactly-once claim is added. Both callbacks cannot roll back publication. Refresh backlog count/oldest-age metrics after each periodic candidate pass and observe attempts/outcomes in `ProcessPoint`.

- [ ] **Step 7.13: Run green reconcile/worker/race suites.**

```bash
go -C backend test ./internal/backupasset/repository ./internal/backupasset/runtime -run 'ProcessVerifying|Reconcile|ListCandidates|HasUnresolvedPublication|ProcessPoint|PublicationBackoff|ExpireAtDeadline|PublicationWorker|SharedRepository' -count=1
go -C backend test ./internal/backupasset/repository ./internal/backupasset/runtime -run 'Reconcile|ListCandidates|HasUnresolvedPublication|ProcessPoint|PublicationBackoff|PublicationWorker' -race -count=20
```

Expected: PASS with no deadline extension, lost durable work, stale-fence commit, worker leak, wake panic, or race.

- [ ] **Step 7.14: Record the future Phase 3.4 boundary without committing.**

```bash
git diff --check -- backend/internal/backupasset/repository backend/internal/backupasset/runtime
```

Expected: exit 0. These files join Phase 3.4 commit `feat: publish fenced Restic recovery points`.

## 10. Task 8 — Evidence Executor And Task Manager Integration

**Files:**

- Create `backend/internal/task/executor/evidence.go`, `evidence_test.go`, `backend/internal/task/publication_runner.go`, and `publication_runner_test.go`.
- Modify `backend/internal/task/executor/executor.go`, `restic_executor.go`, and `restic_executor_test.go`.
- Modify `backend/internal/task/manager.go`, `manager_test.go`, `runner.go`, and `runner_timeout_test.go`.

- [ ] **Step 8.1: Write failing compile-time evidence/factory tests.**

Add:

```go
func TestFactoryInjectsResticPublisherOnlyIntoEvidenceLane(t *testing.T)
func TestResticExecutorImplementsEvidenceExecutor(t *testing.T)
func TestResticEvidenceExecutorMapsTaskConfigWithoutLegacyShellInitOrPasswordFile(t *testing.T)
func TestResticEvidenceExecutorRejectsTaskAttemptProviderAndRunIdentityMismatch(t *testing.T)
func TestResticEvidenceConfigIgnoresLegacyAccessSecretsAndExtractsOnlyBoundedExcludes(t *testing.T)
func TestNonEvidenceExecutorsRetainCurrentContract(t *testing.T)
```

Use these constructors:

```go
func NewFactory(rsyncBinary string) Factory
func NewFactoryWithResticPublisher(rsyncBinary string, publisher provider.ResticPublisher) Factory
```

`NewFactory` must preserve every existing test. The injected factory resolves one Restic executor implementing `EvidenceExecutor`; Rsync/Rclone/Command remain only `Executor` in Child 3.

- [ ] **Step 8.2: Run the evidence/factory red test.**

```bash
go -C backend test ./internal/task/executor -run 'Test(FactoryInjects|ResticExecutorImplements|ResticEvidence|NonEvidence)' -count=1
```

Expected: FAIL because the evidence interface, injected factory constructor, and `RunWithEvidence` are absent.

- [ ] **Step 8.3: Implement the evidence adapter.**

Add a `publisher provider.ResticPublisher` field to `ResticExecutor`. Keep legacy `Run`/restore/list/files methods unchanged for pristine compatibility. Implement:

```go
func (executor *ResticExecutor) RunWithEvidence(
	ctx context.Context,
	request EvidenceExecutionRequest,
	logf LogFunc,
	progressf ProgressFunc,
) (EvidenceExecutionResult, error) {
	if executor == nil || executor.publisher == nil || request.TaskRunID == 0 ||
		request.Task.ID == 0 || request.Task.ID != request.Attempt.TaskID ||
		request.TaskRunID != request.Attempt.TaskRunID ||
		request.Attempt.Provider != backupasset.ProviderRestic || request.Task.ExecutorType != "restic" {
		return EvidenceExecutionResult{}, fmt.Errorf("%w: Restic evidence executor unavailable", backupasset.ErrInvalidState)
	}
	config, err := parseResticEvidenceConfig(request.Task.ExecutorConfig)
	if err != nil {
		return EvidenceExecutionResult{}, fmt.Errorf("parse Restic evidence config: %w", err)
	}
	result, runErr := executor.publisher.Backup(ctx, request.Attempt, provider.ResticBackupInput{
		Source: strings.TrimSpace(request.Task.RsyncSource),
		Excludes: append([]string(nil), config.ExcludePatterns...),
	}, func(progress provider.ResticBackupProgress) {
		if progressf != nil {
			progressf(ProgressSample{ObservedAt: progress.ObservedAt, Percent: progress.Percent, ThroughputMbps: progress.ThroughputMbps})
		}
	})
	return EvidenceExecutionResult{
		ExitCode: result.ExitCode, Completion: result.Completion,
		ProviderCommit: result.ProviderCommit, EvidenceCode: result.EvidenceCode,
	}, runErr
}
```

Define `resticEvidenceConfig` with only `ExcludePatterns []string` and freeze `maxResticEvidenceConfigBytes=1<<20`, `maxResticEvidenceExcludes=256`, and `maxResticEvidenceExcludeBytes=4096`. Reject an oversized config before decoding; use a bounded `json.Decoder` without `DisallowUnknownFields`, so legacy `repository_password` and `append_only` values are skipped and never assigned to a Go field. Validate exclude count/per-value UTF-8 byte length/NUL and reject trailing JSON. Do not read Task Repository locator/password into the new command; the validated attempt binding owns them. Do not call legacy `parseResticConfigWithRepositoryAccess`, `which`, `snapshots`, `cat`, `init`, remote password-file helpers, or `streamSSHCommand`. Log only stable start/finish/evidence-code messages.

- [ ] **Step 8.4: Write failing Manager publication-session tests.**

Create a fake coordinator/session and add:

```go
func TestProviderRunnerPreservesNonEvidenceExecutorFlow(t *testing.T)
func TestProviderRunnerCompatibilitySessionClosesAfterLegacyJoin(t *testing.T)
func TestProviderRunnerEvidenceUsesExactTaskRunAttempt(t *testing.T)
func TestProviderRunnerExitZeroEvidenceDefectPreservesTransferSuccessAndDefersPublication(t *testing.T)
func TestProviderRunnerValidCommitRecordsAndReturnsBeforeManifest(t *testing.T)
func TestProviderRunnerUnknownOutcomeSuppressesTransferRetry(t *testing.T)
func TestProviderRunnerUnknownOutcomeMapsStableLifecycleCodes(t *testing.T)
func TestProviderRunnerKnownNonzeroKeepsBoundedRetrySemantics(t *testing.T)
func TestProviderRunnerUnconfirmedCommitRetriesOnlyByteEquivalentRecord(t *testing.T)
func TestProviderRunnerEarlyReturnCancelsSessionAndCannotLeakAdmissionOrHeartbeat(t *testing.T)
func TestProviderRunnerPassesExecutionContextToCompatibilityAndEvidenceCommands(t *testing.T)
func TestProviderRunnerUnconfirmedCommitClosesLocalSessionWithoutDeferral(t *testing.T)
func TestTaskRunSuccessAndPublicationFailureRemainIndependentFacts(t *testing.T)
```

The non-blocking proof gives `RecordProviderCommit` an outcome in `verifying`, makes the fake manifest worker block forever, and requires TaskRun finalization/post-hook/policy flow to finish without waiting.

- [ ] **Step 8.5: Run the Manager publication red tests.**

```bash
go -C backend test ./internal/task -run 'TestProviderRunner|TestTaskRunSuccessAndPublicationFailure' -count=1
```

Expected: FAIL because Manager has no coordinator and directly invokes `Executor.Run`.

- [ ] **Step 8.6: Extract one publication-aware Provider execution helper.**

Create this internal result:

```go
type providerRunResult struct {
	ExitCode      int
	Err           error
	SuppressRetry bool
	Managed       bool
	WarningCode   backupasset.PublicationFailureCode
}
```

Add `publicationCoordinator publication.Coordinator` to Manager and:

```go
func (manager *Manager) SetPublicationCoordinator(coordinator publication.Coordinator)
func (manager *Manager) executeProvider(
	ctx context.Context,
	taskEntity model.Task,
	runID uint,
	reason string,
	chainRunID string,
	logf executor.LogFunc,
	progressf executor.ProgressFunc,
) providerRunResult
```

Call it at the current `exec.Run` site after successful pre-hook and immediately before Provider mutation. Build `publication.Run.Audit` from the existing credential-audit runtime actor/correlation when both validate; otherwise use the exact system actor and `"pub-" + hex(first16(SHA256("xirang.publication.correlation.v1\x00" + base10(TaskRunID))))`. Freeze vectors `1 -> pub-6981ff9cfeb6f62fbddbfcdfc2eb88eb` and `42 -> pub-0abe4a8d60bda4a3d63f63b882d876c1`; never use Task name/path/chain text. Keep all existing post-hook, policy verification, TaskRun finalization, automation, downstream, and alert behavior after the helper returns.

Execution matrix:

```text
nil session/non-Restic -> current Executor.Run
compatibility -> current Executor.Run, then CompleteCompatibility after join
evidence + missing EvidenceExecutor -> Reject(publication_precondition_missing), no Provider call
known_exit_zero + valid commit -> idempotent RecordProviderCommit, transfer success
known_exit_zero + evidence code -> Defer(known_exit_zero, code), transfer success + safe warning
known_nonzero -> Fail(provider_nonzero_exit), return transfer failure with retry allowed
outcome_unknown -> Defer(outcome_unknown, stable code), return transfer failure with retry suppressed
```

Validate result consistency before any coordinator call: `known_exit_zero` requires exit 0, `known_nonzero` requires exit >0, and `outcome_unknown` requires `provider.UnknownProviderExitCode`; every mismatch is a precondition failure and never becomes a weaker deferral.

Map a joined `outcome_unknown` outer error to the deferral code only with `errors.Is`: `context.DeadlineExceeded` or `sshutil.ErrCommandTimeout` → `FailureProviderTimeout`; `context.Canceled` → `FailureProviderCanceled`; `sshutil.ErrCommandOutputLimit` → `FailureProviderResourceLimit`; generic stdout/stderr read, secret write/close, stream close, connection close, wait uncertainty, or `sshutil.ErrCommandFailed` → `FailureProviderOutcomeUnknown`. The early cleanup path alone uses `FailurePublicationSessionAbandoned`. The Manager table test supplies every error class and asserts the exact `Defer` value and suppressed retry.

Immediately after `Prepare`, use `execution.Context()`—not the original Task context—for both legacy `Executor.Run` and `EvidenceExecutor.RunWithEvidence`, so renew/fence cancellation reaches and joins the real command. Install a `context.WithTimeout(context.WithoutCancel(ctx), sshutil.CommandExecutionJoinTimeout)` cleanup defer. It tracks compatibility completion and the evidence choice among record/defer/reject/fail. A returned compatibility command always closes through `CompleteCompatibility`, whether it succeeded or failed. If evidence unwinds while unresolved, call `Cancel(ErrPublicationSessionAbandoned)` and check its sanitized error, then idempotent `Defer{CompletionOutcomeUnknown, FailurePublicationSessionAbandoned}` only if Provider execution began and its joined outcome is actually unknown. A type/precondition failure proven to occur before Provider invocation uses `Reject`, never `Fail` or an unknown-outcome deferral.

For an error matching `ErrPublicationUnconfirmed`, retry only `RecordProviderCommit` with the same value until the cleanup deadline. If still unresolved, call `Abandon(ErrPublicationUnconfirmed)` to close context/heartbeat/admission while leaving the DB lease to expire, then return transfer success plus a safe publication warning. Do not call `Defer`, create a weaker completion marker, release/mutate the uncertain fence, or rerun transfer.

- [ ] **Step 8.7: Modify retry and anomaly dispatch decisions.**

In the existing failure branch, force `shouldRetry=false` when `providerRunResult.SuppressRetry` is true. Keep known non-zero policy retries unchanged. Never put a publication-only warning into TaskRun `last_error`; write one sanitized TaskLog line containing only point ID/stable code.

The existing immediate repository-wide anomaly call remains only when `providerRunResult.Managed == false`. Managed publication anomaly dispatch moves to the committed observer in Task 9, after exact current/previous point selection.

- [ ] **Step 8.8: Write failing interrupted TaskRun reconciliation tests.**

Add:

```go
func TestReportInterruptedPublicationMarksOnlyStaleExactRunWarningOrFailed(t *testing.T)
func TestReportInterruptedPublicationIgnoresPreparingAndEmptyOutcomes(t *testing.T)
func TestReportInterruptedPublicationNeverOverwritesTerminalOrNewerRun(t *testing.T)
func TestReportInterruptedPublicationUpdatesTaskOnlyWithPrecisionSafeCAS(t *testing.T)
func TestReportInterruptedPublicationNeverDispatchesAutomationDownstreamOrRetry(t *testing.T)
func TestReportInterruptedPublicationFastWorkerDuringPostHookSkipsLiveCurrentProcessRun(t *testing.T)
func TestReconcileInterruptedRunsClassifiesTerminalPointAndLeavesPreparingOrMissingUnresolved(t *testing.T)
func TestReconcileInterruptedRunsDetectsUnfilteredBatchRemainder(t *testing.T)
func TestReconcileInterruptedRunsQueriesOnlyTaskOwnedResticRuns(t *testing.T)
```

Freeze these Task-local codes and mapping:

```go
const (
	taskRunCodeInterruptedAfterProviderCommit  = "process_interrupted_after_provider_commit"
	taskRunCodeInterruptedBeforeProviderCommit = "process_interrupted_before_provider_commit"
)
```

An `Outcome` with `ProviderCommitRecorded=true` and state `verifying|committed|failed` marks the stale exact run `warning` with `taskRunCodeInterruptedAfterProviderCommit`; a terminal failed outcome with `ProviderCommitRecorded=false` marks it `failed` with `taskRunCodeInterruptedBeforeProviderCommit`; `preparing`, empty, or otherwise nonterminal outcomes do not update TaskRun. Before any CAS, Manager checks its existing current-process `pendingRuns` registry by `Outcome.TaskID`; presence means the original run/post-hook is still live, so reporter returns without mutation. The race test keeps the registry entry while a fast worker reports committed, proves the live run remains untouched, then deletes the entry to model restart recovery and proves the same outcome becomes reportable. Task aggregate update requires current Task status `running`, `last_run_at` equal the interrupted run's normalized `started_at`, and no newer active TaskRun. `ReconcileInterruptedRuns(ctx, limit)` is Manager-owned: keyset-query at most `limit` Task-owned Restic runs still in `pending|running|retrying`, skip current-process registry entries, load their RecoveryPoint by exact `producing_task_run_id`, strictly decode its lineage/consistency, and reconstruct only the safe `Outcome` fields. `ProviderCommitRecorded` comes from the durable typed commit marker/locator/manifest facts, never from point state alone. Apply the same safe mapping to terminal/verifying outcomes, then run one unfiltered existence query and return `unresolved=true` for any remaining stale run. A live registry entry, preparing point, missing point, invalid lineage/outcome, query error, or rows beyond the batch can never report ready; non-Restic and terminal TaskRuns are outside this port.

- [ ] **Step 8.9: Run the interrupted-publication red tests.**

```bash
go -C backend test ./internal/task -run '^Test(Report|Reconcile)Interrupted' -count=1
```

Expected: FAIL because Manager does not implement the reporter/readiness ports, exact interruption codes, unfiltered stale-run query, or precision-safe CAS mapping.

- [ ] **Step 8.10: Implement `publication.InterruptedRunReporter`.**

Add compile-time assertion:

```go
var _ publication.InterruptedRunReporter = (*Manager)(nil)
var _ publication.InterruptedRunReadiness = (*Manager)(nil)
```

Use the frozen state/`ProviderCommitRecorded` mapping and active-registry check, then conditional `UPDATE` statements with exact run ID/status predicates. The aggregate Task update uses `NOT EXISTS` for a newer `pending|running|retrying` TaskRun. If zero rows match, leave Task untouched and emit a safe structured warning. Implement the bounded keyset scan plus final unfiltered existence query exactly as specified above; reuse the reporter's internal CAS helper rather than recursively calling a public interface. The final existence query includes skipped current-process entries, so readiness cannot pass while a run is live. Do not call automation, downstream, alert success, or retry scheduling from either method.

- [ ] **Step 8.11: Run green executor/Manager regression and race suites.**

```bash
go -C backend test ./internal/task/executor ./internal/task -run 'Restic|Evidence|ProviderRunner|InterruptedPublication|RunnerTimeout|Manager' -count=1
go -C backend test ./internal/task -run 'ProviderRunner|InterruptedPublication' -race -count=20
```

Expected: PASS; existing non-evidence hooks/progress/retry tests remain green; no TaskRun waits for manifest; unknown outcomes schedule no transfer retry.

- [ ] **Step 8.12: Record the future Phase 3.4 boundary without committing.**

```bash
git diff --check -- backend/internal/task backend/internal/task/executor
```

Expected: exit 0. These files join Phase 3.4 commit `feat: publish fenced Restic recovery points`.

## 11. Task 9 — Legacy List/Files/Search/Diff/Restore/Anomaly/Retention Isolation

**Files:**

- Modify `backend/internal/task/executor/restic_executor.go` and `restic_executor_test.go` for a canonical link-tag listing method only; do not change the evidence path.
- Modify `backend/internal/api/handlers/snapshot_handler.go`, `snapshot_search_handler.go`, `snapshot_search_handler_test.go`, and `snapshot_diff_handler.go`.
- Modify `backend/internal/api/handlers/step_up_test.go` and `credential_access_grant_test.go` so their existing `NewSnapshotHandler` call sites pass narrow guard/Restic fakes and continue proving the unchanged authorization gates.
- Create `backend/internal/api/handlers/snapshot_handler_test.go` and `snapshot_diff_handler_test.go`.
- Modify `backend/internal/snapshot/indexer.go` and `indexer_test.go`.
- Modify `backend/internal/anomaly/snapshot_diff.go` and `snapshot_diff_test.go`.
- Modify `backend/internal/task/manager.go`, `manager_test.go`, `runner.go`, `retention.go`, and `retention_test.go`.
- Modify `backend/internal/backupasset/repository/lineage_guard.go` and tests to implement the locked `ListEntries` port.

- [ ] **Step 9.1: Write failing snapshot list/files/restore guard tests.**

Refactor handlers to depend on narrow fakes:

```go
type LegacyResticSnapshots interface {
	ListSnapshots(context.Context, model.Task) ([]executor.ResticSnapshot, error)
	ListSnapshotsByLinkTag(context.Context, model.Task, string) ([]executor.ResticSnapshot, error)
	ListFiles(context.Context, model.Task, string, string) ([]executor.ResticEntry, error)
	RestoreFiles(context.Context, model.Task, string, []string, string) error
}

func NewSnapshotHandler(db *gorm.DB, guard publication.LineageGuard, restic LegacyResticSnapshots) *SnapshotHandler
```

Add:

```go
func TestSnapshotListPristineCompatibilityPreservesLegacyResults(t *testing.T)
func TestSnapshotListExactUsesLinkTagAndIntersectsCommittedTaskPoints(t *testing.T)
func TestSnapshotListNeverReturnsOtherTaskManualOrUncommittedSnapshot(t *testing.T)
func TestSnapshotFilesResolvesShortPrefixOnlyInsideTaskCommittedSet(t *testing.T)
func TestSnapshotFilesRejectsAmbiguousCrossTaskAndUnknownPrefixBeforeProvider(t *testing.T)
func TestSnapshotRestoreUsesResolvedFullIDAndHoldsAdmissionThroughJoinAndResponse(t *testing.T)
func TestSnapshotHandlersRollbackSafeDisabledKeepsExactGuard(t *testing.T)
```

The list fake returns committed IDs plus another Task/manual ID; the response may contain only the committed intersection. The restore fake blocks; while blocked, a feature transition callback must not run. Release restore, let the handler write its response, and only then may transition complete.

- [ ] **Step 9.2: Run snapshot-handler red tests.**

```bash
go -C backend test ./internal/api/handlers -run 'TestSnapshot(List|Files|Restore|Handlers)' -count=1
```

Expected: FAIL because constructors have no guard/service dependencies and handlers pass raw prefixes/repository-wide lists directly.

- [ ] **Step 9.3: Implement guarded list/files/restore.**

For every handler, call `guard.Begin` before a safety decision, defer `session.Close` until after the response helper returns, and close immediately on bind/load errors.

Use the exact operation at each real call site: list=`OperationLegacySnapshotList`, files=`OperationLegacySnapshotFiles`, snapshot restore=`OperationLegacySnapshotRestore`, diff=`OperationLegacyDiff`, index build=`OperationLegacyIndex`, search=`OperationLegacySearch`, anomaly=`OperationLegacyAnomaly`, task restore-latest=`OperationLegacyRestoreLatest`, and retention=`OperationLegacyRetention`. The Task 6 prepare race test proves pristine backup uses `OperationLegacyBackup` and managed backup uses `OperationEvidenceBackup`; Task 7 proves manifest/reconcile. Thus every ledger operation has both the generic barrier proof and a concrete blocking call-site proof.

List behavior:

```text
compatibility -> existing ListSnapshots
exact -> ListSnapshotsByLinkTag(session.LinkTag()), then intersect by full native ID with session.CommittedPoints()
```

`ListSnapshotsByLinkTag` accepts only `^xirang\.link\.v1\.[0-9a-f]{32}$` and passes one separately escaped `--tag` operand; it never accepts a client tag. Files/restore call `ResolveNativeID`, then pass the resulting full lowercase ID. Keep current response envelopes, RBAC/ownership middleware, step-up, grant, audit, and target-path validation unchanged.

- [ ] **Step 9.4: Write failing diff tests and freeze the exact runner port.**

Use:

```go
type SnapshotDiffRunner interface {
	RunSnapshotDiff(context.Context, model.Task, string, string) (string, error)
}

func NewSnapshotDiffHandler(db *gorm.DB, guard publication.LineageGuard, runner SnapshotDiffRunner) *SnapshotDiffHandler
```

Add tests for exact same-Task prefix resolution, cross-Task/ambiguous rejection before SSH, rollback-safe disabled behavior, and token drain through command join/response. Compatibility may retain current IDs only in pristine mode.

- [ ] **Step 9.4a: Run the snapshot-diff red tests.**

```bash
go -C backend test ./internal/api/handlers -run 'TestSnapshotDiff' -count=1
```

Expected: FAIL because raw query IDs still reach the command path and the injected guard/runner constructor is absent.

- [ ] **Step 9.4b: Implement exact diff resolution.**

Move SSH command execution behind the injected runner and preserve the existing parser/output DTO. The handler flow is exact:

```go
session, err := handler.guard.Begin(c.Request.Context(), taskEntity.ID, publication.OperationLegacyDiff)
if err != nil { /* safe response; no runner call */ }
defer session.Close() // after response projection
left, err := session.ResolveNativeID(requestedLeft)
if err != nil { /* safe response; no runner call */ }
right, err := session.ResolveNativeID(requestedRight)
if err != nil { /* safe response; no runner call */ }
output, err := handler.runner.RunSnapshotDiff(c.Request.Context(), taskEntity, left, right)
```

Compatibility mode passes current IDs only in a pristine session; exact/rollback-safe mode reaches the runner only with two full committed same-Task IDs.

- [ ] **Step 9.4c: Run the snapshot-diff green tests.**

```bash
go -C backend test ./internal/api/handlers -run 'TestSnapshotDiff' -count=1
```

Expected: PASS; cross-Task/ambiguous inputs make zero runner calls, and the admission token closes after command join and response projection.

- [ ] **Step 9.5: Write failing index/search coverage tests.**

Replace package-global index entry points with an injectable service while retaining read-only compatibility wrappers only where existing callers require them:

```go
type Indexer struct {
	db         *gorm.DB
	guard      publication.LineageGuard
	foundation *backupasset.FoundationService
}

func NewIndexer(*gorm.DB, publication.LineageGuard, *backupasset.FoundationService) *Indexer
func (indexer *Indexer) EnsureIndexed(context.Context, uint, publication.LineageSession) (bool, error)
func (indexer *Indexer) Build(context.Context, model.Task) error
func (indexer *Indexer) Status(context.Context, uint, publication.LineageSession) (indexed, total int, building bool, err error)
```

Add:

```go
func TestIndexerExactModeEnumeratesOnlyCommittedTaskPoints(t *testing.T)
func TestIndexerExactModeUsesBoundedProviderEntryListerNotLegacyRepositoryScan(t *testing.T)
func TestIndexerExactModeReplacesContaminatedRowsOutsideAllowedSet(t *testing.T)
func TestEnsureIndexedRequiresCoverageForEveryExpectedCommittedPoint(t *testing.T)
func TestEnsureIndexedRejectsPartialRowsWithoutCompletionMarker(t *testing.T)
func TestEnsureIndexedRepresentsEmptySnapshotWithCompletionMarker(t *testing.T)
func TestSnapshotSearchNeverReadsPartialRowsAfterCrashOrRestart(t *testing.T)
func TestIndexerPristineCompatibilityRetainsCurrentBehavior(t *testing.T)
func TestIndexerAdmissionLivesUntilEveryProviderPageCloses(t *testing.T)
func TestSnapshotSearchFiltersHistoricalContaminationAndHoldsSearchAdmission(t *testing.T)
```

Exact index traversal uses `LineageSession.ListEntries(ctx, fullNativeID, parent, page)` page-by-page with an explicit directory stack, per-page limit `200`, total entry limit freshly loaded from `FoundationService.PublicationConfig().ManifestMaxEntries`, context cancellation, and full IDs from `CommittedPoints`. It never calls legacy `ListSnapshots`/`ListFiles`, `restic find`, or treats the publication manifest as legacy-index coverage.

- [ ] **Step 9.6: Run the index/search red tests.**

```bash
go -C backend test ./internal/snapshot ./internal/api/handlers -run 'Test(Indexer|EnsureIndexed|SnapshotSearch)' -count=1
```

Expected: FAIL because the current indexer scans every repository snapshot and search considers any historical row sufficient.

- [ ] **Step 9.7: Implement exact index/search and pristine compatibility.**

Use these schema-free legacy-cache sentinels (empty is impossible for a validated absolute Provider path):

```go
const exactIndexCompleteMarkerPath = ""
const exactIndexCompleteMarkerMtime = "xirang-index-complete-v1"
```

In exact mode, `Build` begins its own `OperationLegacyIndex` session, removes the marker and all prior rows for one allowed full ID before traversal, then streams validated rows in bounded batches. A crash/error may leave partial rows but no marker. Only after every page/handle closes successfully does it insert one marker row `(task_id, full_id, path="", size=checked_entry_count, mtime=exactIndexCompleteMarkerMtime)`. An empty snapshot therefore has exactly the marker. After every currently allowed snapshot has a marker, delete rows for disallowed IDs; never delete the previous allowed set after an incomplete traversal.

`EnsureIndexed` and `Status` receive the handler's already-admitted lineage session and count only exact marker rows for `CommittedPoints`; they never start a nested admission. When incomplete, `EnsureIndexed` copies no raw full IDs into a goroutine and non-blockingly schedules `Build(task)` by Task ID; that goroutine independently acquires a fresh `OperationLegacyIndex` session and re-resolves lineage after the search response can close. Existing partial rows cannot make readiness true.

Construct search exactly as:

```go
func NewSnapshotSearchHandler(db *gorm.DB, guard publication.LineageGuard, indexer *snapshot.Indexer) *SnapshotSearchHandler
```

It begins `OperationLegacySearch`, passes that session to `EnsureIndexed`, and in exact mode queries only allowed full IDs with `path <> ''` plus an `EXISTS` completion-marker row for the same `(task_id,snapshot_id)`. It retains the current response status/shape in compatibility mode. A missing/incomplete exact point produces no leaked counts or paths. The search token remains open through DB query and response projection.

- [ ] **Step 9.7a: Run the index/search green tests.**

```bash
go -C backend test ./internal/snapshot ./internal/api/handlers -run 'Test(Indexer|EnsureIndexed|SnapshotSearch)' -count=1
```

Expected: PASS; partial/crashed rows without the exact marker never appear, empty snapshots are complete by marker, and no exact-mode repository-wide scan occurs.

- [ ] **Step 9.8: Write failing exact anomaly tests.**

Add:

```go
func TestManagedAnomalyUsesExactCurrentAndPreviousCommittedTaskPoints(t *testing.T)
func TestManagedAnomalySkipsWhenNoPreviousCommittedPoint(t *testing.T)
func TestManagedAnomalyNeverRunsRepositoryLatestTwo(t *testing.T)
func TestManagedAnomalyAdmissionDrainsAfterCommandJoin(t *testing.T)
func TestManagerObserveCommittedDispatchesExactAnomalyBestEffort(t *testing.T)
```

Introduce:

```go
type SnapshotDiffCommandRunner interface {
	RunSnapshotDiff(context.Context, model.Task, string, string) (string, error)
}

func AnalyzeSnapshotDiffExact(
	context.Context, *gorm.DB, model.Task, uint, string, string,
	publication.LineageGuard, SnapshotDiffCommandRunner,
) ([]Finding, error)

type ExactAnomalyFunc func(context.Context, model.Task, uint, string, string) ([]anomaly.Finding, error)
func (manager *Manager) SetExactAnomalyAnalyzer(ExactAnomalyFunc)
```

The analyzer begins one `OperationLegacyAnomaly` lineage session and calls `CurrentAndPrevious(currentFullID)` to re-derive the specified current point's immediate same-Task/link predecessor under the current committed view. It requires that predecessor to equal the observer-supplied previous ID, then resolves both full IDs again before command execution; a concurrent lineage change or mismatch skips/fails closed before SSH. The observer receives current/previous full IDs only in memory and never logs them.

- [ ] **Step 9.9: Run the anomaly red tests.**

```bash
go -C backend test ./internal/anomaly ./internal/task -run 'TestManagedAnomaly|TestManagerObserveCommitted' -count=1
```

Expected: FAIL because current code runs `snapshots --latest 2` and Manager has no committed observer.

- [ ] **Step 9.9a: Implement the committed observer and exact anomaly diff.**

Keep the old `AnalyzeSnapshotDiff` path only for a pristine compatibility session; managed/rollback-safe calls reject repository-wide selection. Add:

```go
var _ publication.CommitObserver = (*Manager)(nil)
```

Add `RunSnapshotDiff` to `ResticExecutor`; it requires two full lowercase IDs and contains the existing legacy diff command/join implementation. Both handler and anomaly packages parse its returned bounded text behind their local typed DTOs. `Manager.ObserveCommitted` loads the exact Task, returns when no predecessor exists, calls its injected `ExactAnomalyFunc` under a bounded context, and raises findings best-effort. It never mutates RecoveryPoint or TaskRun state.

- [ ] **Step 9.9b: Run the anomaly green tests.**

```bash
go -C backend test ./internal/anomaly ./internal/task -run 'TestManagedAnomaly|TestManagerObserveCommitted' -count=1
```

Expected: PASS; managed execution uses only revalidated exact IDs and no code path invokes repository `--latest 2`.

- [ ] **Step 9.10: Write failing restore-latest and retention guard tests.**

Add:

```go
func TestManagedResticRestoreLatestBlockedBeforeCredentialAndSSH(t *testing.T)
func TestPristineResticRestoreLatestRetainsCompatibility(t *testing.T)
func TestManagedResticRetentionBlocksForgetPruneBeforeCredentialAndSSH(t *testing.T)
func TestRollbackSafeDisabledRetentionRemainsBlocked(t *testing.T)
func TestPristineResticRetentionRetainsCompatibility(t *testing.T)
func TestResticRetentionAdmissionDrainsThroughCommandAndConnectionClose(t *testing.T)
func TestManagedRestoreAndRetentionRecordTypedLegacyBlockAuditAndMetric(t *testing.T)
```

Instrument credential resolution, SSH dial, command construction, and runner calls; every managed blocked case requires all counters zero. The compatibility blocking fake proves the token survives until command and SSH close.

- [ ] **Step 9.11: Run the retention/restore red tests.**

```bash
go -C backend test ./internal/task -run 'Test(ManagedRestic|PristineRestic|RollbackSafeDisabled|ResticRetentionAdmission)' -count=1
```

Expected: FAIL because managed calls reach legacy `restore latest` or `forget --prune` and no typed legacy-block recorder is wired.

- [ ] **Step 9.11a: Implement restore-latest and retention guards.**

Add `lineageGuard publication.LineageGuard` and `legacyBlockRecorder publication.LegacyBlockRecorder` to Manager, with `SetLineageGuard` and `SetLegacyBlockRecorder`. `runRestoreTask` begins `OperationLegacyRestoreLatest` before executor/credential access; exact mode derives a system legacy-block audit context, calls the recorder best-effort, and returns stable `legacy_operation_blocked` regardless of audit success. `TriggerRestore` may perform an early user-facing preflight, but the run path always reacquires/rechecks after admission. `enforceResticRetention` begins `OperationLegacyRetention`; exact mode calls the same recorder and writes only a stable warning before returning prior to SSH. The recorder owns the typed audit and `ObserveLegacyBlocked` metric, so Manager does not reach into a concrete metric/audit sink. Exact-session resolution failures in handlers/index/diff record the corresponding bounded operation metric inside `repository.Service`; HTTP audit middleware retains user audit ownership. No metric/audit label contains Task/path/native facts. Pristine compatibility retains current behavior.

- [ ] **Step 9.11b: Run the retention/restore green tests.**

```bash
go -C backend test ./internal/task -run 'Test(ManagedRestic|PristineRestic|RollbackSafeDisabled|ResticRetentionAdmission)' -count=1
```

Expected: PASS; managed/rollback-safe paths record only typed safe facts and make zero credential, SSH, command-construction, or destructive runner calls.

- [ ] **Step 9.12: Run the complete legacy isolation and command-drain matrix.**

```bash
go -C backend test ./internal/api/handlers ./internal/snapshot ./internal/anomaly ./internal/task -run 'Snapshot|Indexer|Lineage|Anomaly|Retention|Restore' -count=1
go -C backend test ./internal/api/handlers ./internal/snapshot ./internal/anomaly ./internal/task -run 'Admission|Snapshot|Anomaly|Retention|Restore' -race -count=10
```

Expected: PASS. Every listed Restic command surface is either pristine-compatible or exact/fail-closed after post-admission recheck; no shared-repository snapshot crosses Tasks.

- [ ] **Step 9.13: Add and run forbidden-boundary source tests.**

`backend/internal/backupasset/provider/publication_boundary_test.go` must parse source/imports and prove:

- Provider imports neither API handlers, Task Manager, task executor, Repository, nor runtime;
- publication/manifest/reconcile Provider files contain no registered mutation operation for forget/prune/delete/restore/init;
- Task Manager contains no Restic JSON/tag/locator/manifest parser;
- legacy index is not referenced by repository publication or manifest code;
- no new publication file constructs a shell command string.

Run:

```bash
go -C backend test ./internal/backupasset/provider -run 'TestPublicationSourceBoundaries' -count=1
```

Expected: PASS.

- [ ] **Step 9.14: Record the future Phase 3.4 boundary without committing.**

```bash
git diff --check -- backend/internal/api/handlers backend/internal/snapshot backend/internal/anomaly backend/internal/task
```

Expected: exit 0. These paths join Phase 3.4 commit `fix: isolate legacy Restic lineage`.

## 12. Task 10 — Shared Runtime, Feature Transitions, Startup/Shutdown, Documentation, And Wiring

**Files:**

- Create `backend/internal/backupasset/runtime/runtime.go` and `runtime_test.go`.
- Create `backend/cmd/server/main_test.go` for lifecycle/source-order regression coverage.
- Modify `backend/internal/backupasset/runtime/publication_worker.go` where runtime ownership requires it.
- Modify `backend/internal/api/router.go`, `router_test.go`, and `backup_asset_rbac_test.go`.
- Modify `backend/internal/api/handlers/settings_handler.go`, `settings_handler_test.go`, `config_handler.go`, and `config_handler_test.go`.
- Modify `backend/cmd/server/main.go`.
- Modify `.env.deploy`, `backend/.env.production.example`, `docs/env-vars.md`, and `docs/admin/backup-recovery.md`.
- Modify the three Trellis backend specs listed in Section 2.2 with the durable conventions established by this child.

- [ ] **Step 10.1: Write failing single-runtime composition tests.**

Create the runtime with:

```go
type Dependencies struct {
	DB              *gorm.DB
	Settings        *settings.Service
	Now             func() time.Time
	Transport       provider.CommandTransport
	StreamTransport provider.CommandStreamTransport
	ToolBinaries    provider.ToolBinaries
	Metrics         publication.Metrics
	Tombstones      repository.ManagedHistoryTombstoneSource
}

func New(Dependencies) (*Runtime, error)
func (runtime *Runtime) FoundationService() *backupasset.FoundationService
func (runtime *Runtime) RepositoryService() *repository.Service
func (runtime *Runtime) PublicationCoordinator() publication.Coordinator
func (runtime *Runtime) PublicationReconciler() publication.Reconciler
func (runtime *Runtime) ResticPublisher() provider.ResticPublisher
func (runtime *Runtime) ManifestBuilder() provider.ManifestBuilder
func (runtime *Runtime) LineageGuard() publication.LineageGuard
func (runtime *Runtime) LegacyBlockRecorder() publication.LegacyBlockRecorder
func (runtime *Runtime) FeatureTransitioner() publication.FeatureTransitioner
func (runtime *Runtime) SetCommitObserver(publication.CommitObserver) error
func (runtime *Runtime) SetInterruptedRunReporter(publication.InterruptedRunReporter) error
func (runtime *Runtime) SetInterruptedRunReadiness(publication.InterruptedRunReadiness) error
func (runtime *Runtime) StartupPass(context.Context) error
func (runtime *Runtime) StopAccepting()
func (runtime *Runtime) Run(context.Context)
func (runtime *Runtime) Shutdown(context.Context) error
```

Add:

```go
func TestRuntimeSharesOneTransportGateRegistryAndResticAdapter(t *testing.T)
func TestRuntimeExposesOneRepositoryPublicationLineageAndWorkerGraph(t *testing.T)
func TestRuntimeResticRegistrationIncludesReadPublisherAndManifestPorts(t *testing.T)
func TestRuntimeProductionConstructionRequiresDBSettingsAndMatchedOrInternalTransport(t *testing.T)
func TestRuntimeRouterNeverConstructsFallbackProviderRuntime(t *testing.T)
func TestRuntimeForwardsManagedHistoryTombstoneSourceToSharedResolver(t *testing.T)
func TestRuntimeRejectsLateObserverReporterAndReadinessWiring(t *testing.T)
```

The shared-object test compares fake transport call counters/pointers after a read probe, evidence backup, and manifest call. The tombstone test injects a source returning true with no point rows, initializes the false feature, and proves the shared Admission/Repository graph resolves rollback-safe rather than pristine. The Router source-boundary test requires the old `newBackupRepositoryHandler` composition helper to be absent.

- [ ] **Step 10.2: Run runtime composition red tests.**

```bash
go -C backend test ./internal/backupasset/runtime ./internal/api -run 'TestRuntime' -count=1
```

Expected: FAIL because `runtime.Runtime` does not exist and Router still constructs a private Provider graph.

- [ ] **Step 10.3: Implement the shared composition root.**

`Runtime.New` constructs exactly once, in order:

1. Foundation service and validated Provider/lease/publication configs;
2. Keyring, cursor codec, audit writer/sink;
3. Node dialer and one SSH command transport when test transports are not injected;
4. Rsync, Restic, Rclone adapters using the same transport/concurrency gate;
5. Registry registrations, including Restic publisher/manifest ports;
6. one Prometheus metrics sink (use injected sink or construct against the default registerer);
7. one lower DB-backed `ManagedHistoryResolver`, forwarding `Dependencies.Tombstones` exactly once and including the active-publication-lease query;
8. admission controller in an unready state using that resolver; `StartupPass` later initializes the DB→environment→default generation;
9. Repository read/PublicationService graph using the same resolver and admission; `repository.Service` is the sole `LineageGuard` implementation;
10. PublicationWorker with non-blocking wake callback.

Construct Restic through `NewResticAdapterWithPublication`, passing the same concrete transport, `FoundationService.PublicationConfig`, and existing Provider limits source. Runtime requires DB and Settings. It accepts either both transport facets as the same injected object or neither, in which case it constructs one internal `*provider.SSHCommandTransport`; exactly-one or mismatched injections fail. Resolve the PublicationService/worker cycle with a closure over a local worker pointer: before assignment it returns false, assign exactly once before `New` returns, and never mutate it afterward; no goroutine or caller can observe the nil phase. The independently constructed history resolver removes any AdmissionController↔Repository cycle. Do not expose secrets or concrete transport internals from public methods.

`Runtime.SetCommitObserver`, `SetInterruptedRunReporter`, and `SetInterruptedRunReadiness` update callback/readiness dependencies only before `StartupPass`; calling any after startup begins returns a stable configuration error rather than racing or missing startup outcomes. Production injects the same Manager as both interrupted-run ports; no type assertion or runtime-owned TaskRun query is allowed.

`Runtime.StartupPass` first calls `AdmissionController.Initialize` so the effective DB/environment/default flag, active-lease safety, and permanent history latch pass readiness with no admitted commands, then obtains the installed mode through `CurrentMode`. Only then may it call the worker's bounded `StartupPass`. After that pass it calls `Reconciler.HasUnresolvedPublication`, which is unfiltered by lease/backoff/batch/scan ceiling. In managed or rollback-safe mode it also calls Manager's `InterruptedRunReadiness.ReconcileInterruptedRuns(batchSize)`; pristine mode skips that managed-run port so an old compatibility TaskRun cannot break the approved disabled path. Readiness succeeds only when the unfiltered publication query is false and the Manager port returns `unresolved=false`. A live-lease/backoff/transient/preparing point, more-than-batch point/run set, missing-point stale managed run, or any query/codec error returns a typed not-ready error; Runtime stays unready, Router remains unavailable, and schedules remain unloaded. Runtime never queries TaskRun tables itself.

- [ ] **Step 10.4: Write failing settings/config transition tests.**

Add a narrow optional dependency to both handlers:

```go
func (handler *SettingsHandler) WithBackupAssetTransitioner(publication.FeatureTransitioner) *SettingsHandler
func (handler *ConfigHandler) WithBackupAssetTransitioner(publication.FeatureTransitioner) *ConfigHandler
```

Add:

```go
func TestSettingsEnableDrainsBeforeDatabaseValueChanges(t *testing.T)
func TestSettingsDisableChoosesRollbackSafeModeWhenHistoryExists(t *testing.T)
func TestSettingsFailedDrainOrPersistKeepsOldValueAndGeneration(t *testing.T)
func TestSettingsDeleteEnabledOverrideTransitionsToEnvironmentOrDefault(t *testing.T)
func TestSettingsDeleteNonEnabledFoundationOverrideValidatesFallbackCombination(t *testing.T)
func TestSettingsApparentNoOpEnabledWriteStillUsesExclusiveTransition(t *testing.T)
func TestConcurrentFoundationSettingsCannotCommitInvalidCombination(t *testing.T)
func TestConcurrentEnabledTrueFalseCannotDesynchronizeGeneration(t *testing.T)
func TestConfigImportPreflightsFeatureTransitionBeforeOpeningImportTransaction(t *testing.T)
func TestConfigImportTransitionFailureRollsBackAllImportedSettings(t *testing.T)
func TestConfigImportAndSettingsBatchSerializeFoundationMutation(t *testing.T)
```

Add to settings service:

```go
func (service *Service) GetFallback(key string) (string, error)
```

It returns validated environment value when present, otherwise the registry default, without reading/writing the DB.

Use blocking transition/transaction fakes plus two goroutines for the concurrency cases. The invalid-combination test starts from a valid full snapshot and races two individually valid batches whose combined final lease/publication values would violate a cross-setting constraint; exactly one may commit and the final effective snapshot must validate. The enabled race alternates explicit `true`/`false` requests and requires the final DB value, admission mode, and observed generation to describe the same serialized request. The apparent no-op test writes the currently effective enabled value and still proves exclusive drain occurs before persistence. The import/batch test blocks the import callback and proves the batch cannot validate against or persist over a stale pre-import snapshot. Delete a non-enabled coupled key whose environment/default fallback conflicts with another DB override and require rejection with the DB row and generation unchanged.

- [ ] **Step 10.5: Run the transition red tests.**

```bash
go -C backend test ./internal/api/handlers -run 'Test(Settings|ConfigImport|Concurrent).*(Transition|Delete|Enable|Disable|Failed|Foundation|Generation|Serialize)' -count=1
```

Expected: FAIL because foundation writes/import/delete bypass the mutation mutex and admission transitions, delete validates only one key, and concurrent requests can use stale effective snapshots.

- [ ] **Step 10.6: Implement transition-wrapped settings persistence.**

For `BatchUpdate`, validate every requested value first. If no key satisfies `settings.IsBackupAssetFoundationSetting`, retain the existing atomic transaction path. Otherwise define a named `foundationMutation(current map[string]string) error` closure and pass it to `WithBackupAssetMutation(c.Request.Context(), foundationMutation)`: extract the request's foundation subset, call `ValidateBackupAssetEffectiveUpdate(current, foundationOverlay)`, derive the target enabled bool from a copy of `current` plus that exact overlay, and build one callback that opens the existing atomic DB transaction and applies **all** requested settings. If the request contains `backup_assets.enabled`, always pass that persistence callback to `TransitionFeature` even when the target bool equals `current["backup_assets.enabled"]`; otherwise invoke it directly. The mutation mutex remains held through exclusive drain and transaction completion, and the transaction never opens before the drain callback. No handler performs a second effective-settings read inside the callback.

For deletion, use `IsBackupAssetFoundationSetting` rather than a special case for enabled. A foundation-key delete executes inside `WithBackupAssetMutation`, calls `GetFallback(key)`, validates `{key: fallback}` against the callback's `current` snapshot, derives the target value from that same overlay, and only then deletes the DB override. Deleting `backup_assets.enabled` always uses `TransitionFeature`, including an apparent no-op fallback; deleting another foundation key persists directly under the mutation lock. Invalid fallback combinations and lookup/transition/persist failures leave the DB row, cache-visible value, and generation unchanged. Non-foundation deletes retain current behavior.

For config import, normalize and individually validate the final system-setting write plan before opening the import transaction; reject duplicate setting keys instead of making them order-dependent, and apply the selected conflict policy before constructing the overlay. If the actual plan contains any foundation key, execute the **entire** existing nodes/keys/policies/tasks/settings import transaction inside `WithBackupAssetMutation`; validate the foundation overlay against its supplied `current` map and derive target enabled from that exact copy. If the plan contains `backup_assets.enabled`, always wrap the entire import transaction in `TransitionFeature` regardless of apparent equality; otherwise invoke it directly under the mutation lock. An import without foundation mutations retains the existing transaction path. A missing transitioner rejects any plan containing enabled, while unrelated settings retain their current behavior. Transition/persist failure returns the standard safe response and rolls back all imported entities/settings plus the admission state. Add a source/call-site assertion that production `BatchUpdate`, config import, and foundation delete are the only foundation mutation entry points and all three call `WithBackupAssetMutation`; no direct `Update`, `UpdateWithTx`, or `Delete` call may bypass it for a foundation key.

- [ ] **Step 10.7: Write failing Router/bootstrap/startup ordering tests.**

Change API dependencies to include:

```go
BackupAssets *backupruntime.Runtime
```

Add tests/source assertions proving:

```text
main constructs backupasset/runtime before executor Factory and Task Manager
Factory receives `runtime.ResticPublisher()`
Manager receives coordinator and lineage guard
runtime receives Manager as commit observer, interrupted-run reporter, and interrupted-run readiness owner
StartupPass completes before Manager.LoadSchedules
Router receives the same runtime and never constructs Provider transports/adapters
all existing Router tests explicitly pass nil/disabled dependencies safely or a test runtime
```

Freeze the API wiring fields:

```go
type Dependencies struct {
	AppContext           context.Context
	DB                   *gorm.DB
	AuthService          *auth.Service
	JWTManager           *auth.JWTManager
	TaskManager          *task.Manager
	Hub                  *ws.Hub
	AllowedOrigins       []string
	LoginRateLimit       int
	LoginRateWindow      time.Duration
	SettingsService      *settings.Service
	RetryWorker          *alerting.RetryWorker
	AlertDispatcher      *alerting.Dispatcher
	MetricsToken         string
	MetricsRateLimit     int
	MetricsRateWindow    time.Duration
	TrustedProxies       []string
	BackupAssets          *backupruntime.Runtime
	LegacyResticSnapshots handlers.LegacyResticSnapshots
	SnapshotDiffRunner    handlers.SnapshotDiffRunner
	SnapshotIndexer       *snapshot.Indexer
}
```

Router constructs snapshot/list/diff/search/settings/config handlers only from those injected ports plus `BackupAssets.LineageGuard/FeatureTransitioner`; nil test dependencies return stable fail-closed handlers and never construct a Provider transport, adapter, or executor.

- [ ] **Step 10.8: Run bootstrap/Router red tests.**

```bash
go -C backend test ./internal/api ./cmd/server -run 'Runtime|Router|Startup|BackupAsset' -count=1
```

Expected: FAIL until main/Router use the shared runtime and all constructors compile.

- [ ] **Step 10.9: Wire runtime, Manager, handlers, and immediate reconciliation.**

In `main.go` after settings construction and before executor factory:

```go
assetRuntime, err := backupruntime.New(backupruntime.Dependencies{
	DB: db, Settings: settingsSvc,
	ToolBinaries: provider.ToolBinaries{
		Restic: util.GetEnvOrDefault("RESTIC_BINARY", "restic"),
		Rclone: util.GetEnvOrDefault("RCLONE_BINARY", "rclone"),
	},
})
if err != nil {
	log.Fatal().Err(err).Msg("构建备份资产运行时失败")
}

executorFactory := executor.NewFactoryWithResticPublisher(cfg.RsyncBinary, assetRuntime.ResticPublisher())
taskManager := task.NewManager(db, executorFactory, hub, cronScheduler, settingsSvc, alertDispatcher, cfg.TaskTrafficRetentionDays, cfg.TaskRunRetentionDays)
taskManager.SetPublicationCoordinator(assetRuntime.PublicationCoordinator())
taskManager.SetLineageGuard(assetRuntime.LineageGuard())
taskManager.SetLegacyBlockRecorder(assetRuntime.LegacyBlockRecorder())
if err := assetRuntime.SetCommitObserver(taskManager); err != nil {
	log.Fatal().Err(err).Msg("配置备份资产提交观察器失败")
}
if err := assetRuntime.SetInterruptedRunReporter(taskManager); err != nil {
	log.Fatal().Err(err).Msg("配置备份资产中断对账失败")
}
if err := assetRuntime.SetInterruptedRunReadiness(taskManager); err != nil {
	log.Fatal().Err(err).Msg("配置备份资产中断就绪检查失败")
}
legacyRestic, ok := executorFactory.Resolve("restic").(*executor.ResticExecutor)
if !ok {
	log.Fatal().Msg("Restic legacy adapter type mismatch")
}
snapshotIndexer := snapshot.NewIndexer(db, assetRuntime.LineageGuard(), assetRuntime.FoundationService())
taskManager.SetExactAnomalyAnalyzer(func(ctx context.Context, taskEntity model.Task, runID uint, currentID, previousID string) ([]anomaly.Finding, error) {
	return anomaly.AnalyzeSnapshotDiffExact(ctx, db, taskEntity, runID, currentID, previousID, assetRuntime.LineageGuard(), legacyRestic)
})
if err := assetRuntime.StartupPass(context.Background()); err != nil {
	log.Fatal().Err(err).Msg("备份资产启动对账失败")
}
if err := taskManager.LoadSchedules(context.Background()); err != nil {
	log.Fatal().Err(err).Msg("加载定时任务失败")
}
```

Pass `BackupAssets: assetRuntime`, `LegacyResticSnapshots: legacyRestic`, `SnapshotDiffRunner: legacyRestic`, and `SnapshotIndexer: snapshotIndexer` to Router. The same ResticExecutor instance serves the Manager evidence/compatibility factory and the explicitly guarded legacy handler interfaces; it never constructs the new Provider runtime. Construct backup Repository, snapshot, diff, search/index, settings, and config handlers from these runtime/services/ports. When Router tests pass nil runtime, handlers fail closed without constructing a second transport; production main always supplies all four values.

- [ ] **Step 10.10: Write failing shutdown/restart race tests.**

Add:

```go
func TestMainShutdownOrderStopsTriggersBeforeRuntimeAndDB(t *testing.T)
func TestRuntimeShutdownDuringBackupManifestReadAnomalyRetentionJoinsEveryCommand(t *testing.T)
func TestRuntimeShutdownWakeRaceNeverPanicsOrBlocks(t *testing.T)
func TestRuntimeRestartReconcilesStaleRunsBeforeSchedules(t *testing.T)
func TestRuntimeStartupRefusesReadinessWhenCandidatesExceedBoundedPass(t *testing.T)
func TestRuntimeStartupRefusesTransientZeroMatchStaleRun(t *testing.T)
func TestRuntimeStartupRefusesLiveLeaseBackoffAndBeyondScanRowsOmittedByCandidates(t *testing.T)
func TestRuntimeStartupRefusesUnresolvedTaskRunAndReadinessQueryFailure(t *testing.T)
func TestRuntimeStartupPristineCompatibilitySkipsManagedRunReadiness(t *testing.T)
func TestRuntimeShutdownLeavesVerifyingAndDeferredRowsRestartable(t *testing.T)
```

The first is a `backend/cmd/server` lifecycle/source-order test because Runtime does not own Manager or cron. Runtime tests use blocking admission/worker fakes for each operation class; main tests assert DB closes only after all handles join. An invalid current fence is left to expire rather than force-mutated; no wake send races a channel close.

- [ ] **Step 10.11: Run the shutdown/restart/startup-readiness red tests.**

```bash
go -C backend test ./internal/backupasset/runtime ./cmd/server -run 'Test(MainShutdown|Runtime(Shutdown|Restart|Startup))' -count=1
```

Expected: FAIL because the lifecycle ordering, bounded join, wake-safe shutdown, stale-run readiness refusal, and server source-order seam are absent.

- [ ] **Step 10.12: Implement production shutdown order.**

Use this exact order in `main.go`:

```text
receive signal
stop cron scheduler and Task Manager new triggers
runtime.StopAccepting (new Restic admissions and worker claims)
HTTP server.Shutdown (stop endpoints and response producers)
reverse-shutdown workers so retention/anomaly/Task Manager join before asset runtime
asset runtime cancels/joins manifest/reconcile work and drains admission
cleanup legacy SSH key temp directory only after every command has joined
cancel WebSocket hub context
```

Place asset runtime before Task Manager and every Restic-using background worker in the startup `workers` slice so LIFO shutdown closes consumers before the runtime. `Runtime.Shutdown` never deletes Provider data or terminalizes a point with a live/possibly live fence; it releases only a fence it still validates, otherwise it lets the lease expire for restart takeover.

- [ ] **Step 10.13: Run green runtime/transition/bootstrap/shutdown suites.**

```bash
go -C backend test ./internal/backupasset/runtime ./internal/api ./internal/api/handlers ./internal/task ./cmd/server -run 'Runtime|Transition|Startup|Shutdown|Router|BackupAsset' -count=1
go -C backend test ./internal/backupasset/runtime ./internal/task -run 'Shutdown|Wake|Admission|PublicationWorker' -race -count=20
```

Expected: PASS with one runtime/transport, startup reconciliation before schedules, no setting race, no leaked command/worker, and no wake panic.

- [ ] **Step 10.14: Update operator configuration and rollback documentation.**

Add all ten Task 2 environment keys with their exact defaults/bounds to `.env.deploy`, `backend/.env.production.example`, and `docs/env-vars.md`. Update `backend/README_backend.md` with the single `backupasset/runtime` composition root, injected Router ports, and no new public route; this is required by doc-freshness for `router.go`. Update `docs/admin/backup-recovery.md` to state:

- feature default remains false;
- enabling/disabling drains all Restic backup/read/restore/anomaly/retention/publication commands;
- pristine disabled installations retain legacy behavior;
- any managed native point/tombstone permanently activates rollback-safe guards;
- after use, retain migration 063 and use a Child-3-compatible binary with the flag false;
- pre-Child3 application downgrade and schema down both require the explicit exclusive preflight, reject any active publication lease, and are rejected permanently after managed history/tombstones exist;
- no rollback runs forget/prune/delete or removes native snapshots.

Do not document public asset navigation/API or GA enablement.

- [ ] **Step 10.15: Update durable Trellis backend conventions.**

Apply these exact facts:

- `database-guidelines.md`: current backup-asset baseline includes paired 062/063, every later version stays paired, and down after managed history fails closed rather than deleting data;
- `directory-structure.md`: `backupasset/runtime` is the single composition root, `backupasset/publication` holds neutral ports, Provider cannot import Task/API/runtime;
- `quality-guidelines.md`: add a Restic exact-publication scenario covering full ID/final summary, transfer/publication separation, exact tags/`original`, complete manifest, transaction fence, admission drain, managed latch, no destructive rollback, and required tests.

- [ ] **Step 10.16: Run docs/spec and focused integration gates.**

```bash
set -euo pipefail
doc_changed_files="$({ git diff --name-only HEAD; git ls-files --others --exclude-standard; } | LC_ALL=C sort -u)"
doc_freshness_output="$(DOC_FRESHNESS_CHANGED_FILES="$doc_changed_files" bash scripts/check-doc-freshness.sh)"
printf '%s\n' "$doc_freshness_output"
case "$doc_freshness_output" in *"⚠️"*) exit 1 ;; esac
bash scripts/check-migration-utc-safety.sh
go -C backend test ./internal/database ./internal/backupasset/... ./internal/task/... ./internal/snapshot ./internal/anomaly ./internal/api/... -run 'Migration063|Restic|Publication|Lineage|Snapshot|Retention|Runtime' -count=1
go -C backend build -o /tmp/xirang-child3-server ./cmd/server
rm -f /tmp/xirang-child3-server
test ! -e /tmp/xirang-child3-server
git diff --check
```

Expected: all commands exit 0; PostgreSQL migration tests may skip only in this focused local run when no DSN is configured; build succeeds; docs report no stale claim warning attributable to this change.

- [ ] **Step 10.17: Record the future Phase 3.4 boundary without committing.**

```bash
git diff --check -- backend/cmd/server backend/internal/api backend/internal/api/handlers backend/internal/backupasset/runtime backend/README_backend.md .env.deploy backend/.env.production.example docs .trellis/spec/backend
```

Expected: exit 0. Runtime/wiring paths join `feat: publish fenced Restic recovery points`; legacy paths and their operator/spec truth updates join `fix: isolate legacy Restic lineage`.

## 13. Risk And Rollback Checkpoints

| Checkpoint | Failure signal | Required response | Data rollback rule |
|---|---|---|---|
| Before migration 063 | another migration has consumed 063–070 or engines differ | stop, renumber 063 and every later parent reservation atomically, re-review plan | no schema change applied |
| Migration up | duplicate producing run/native source, table rebuild/FK/check mismatch | abort migration; inspect existing DB; do not merge/delete rows | Provider data untouched; restore DB backup only if engine reports dirty/partial migration |
| Concurrent foundation settings mutation | two batches/import/delete validate different stale snapshots, or enabled value diverges from admission generation | serialize every foundation mutation through `WithBackupAssetMutation`; reject the losing invalid overlay and rerun from its fresh snapshot | preserve the last committed full-effective combination and matching generation; never partially apply an import |
| Feature transition | admission drain timeout, setting transaction failure | abort transition and reopen prior generation/value | do not start tagged backup or cancel a proven old command to force enable |
| Publication audit failure | typed audit write fails before or after Provider I/O | increment bounded audit-failure metric and emit safe structured alert; before Provider I/O abandon without invoking Provider, after a durable Provider/state fact preserve and reconcile that truth rather than rolling it back | never expose raw evidence or invent a weaker success/failure; a missing post-fact audit remains explicitly observable instead of erasing the fact |
| Prepare | missing link, identity drift, immutable point mismatch, live lease | fail before Provider backup; a live-lease duplicate returns `publication_in_progress`, while an expired/abandoned existing point remains exclusively reconciliation-owned | delete no point or snapshot; transaction rollback only |
| Backup completion unknown | timeout/cancel/output/read/close/wait uncertainty | suppress transfer retry; persist typed unknown deferral only after join; reconcile exact tags | never rerun an ambiguous backup automatically |
| Exit zero evidence defect | missing/malformed/duplicate/non-final summary | keep transfer truth successful; persist `known_exit_zero`; rebuild only from valid stored summary | never store rejected stdout as evidence |
| Commit response unconfirmed | DB response lost/transient | retry/read back byte-identical `RecordProviderCommit` until cleanup deadline | do not downgrade to deferral or rerun transfer |
| Manifest/reconcile fence loss | renew failure, takeover, stale callback | cancel/join Provider work; discard result; new fence retries | no stale state/manifest write |
| Manifest incomplete | partial/unavailable/limit/protocol/identity/tag drift | persist inactive diagnostic when trustworthy; keep pending for transient or fail typed deterministic error | never activate partial or copy its counts to point |
| Fixed deadline | elapsed immutable deadline and no live lease | CAS exact revision to typed failed state | never move deadline or delete Provider snapshot |
| Feature disable after history | latch true | enter rollback-safe mode; stop new publication; keep exact guards | retain 063, points, manifests, audit, tags, tombstones |
| Legacy index crash/partial batch | rows exist without the exact completion marker | treat the snapshot as unindexed, return no partial search truth, and rebuild after a fresh lineage/admission check | retain the previous fully marked allowed set until the replacement marker commits; delete no Provider data |
| Schema down | active operation/lease, any native point, or tombstone | reject before down callback/SQL mutation | after any use, application rollback only |
| Shutdown | command/worker misses bounded join | keep durable preparing/verifying state, let invalid fence expire, report safe warning | close DB only after handles join; no terminal guess |
| Required CI follow-up after archive | a required check fails after the five work commits/archive/journal are pushed | reproduce red, fix on the same branch, rerun the full gate, append one fix commit and one workspace-only journal commit, then push/watch again | never amend/rewrite the reviewed commits or create a second archive/PR |

## 14. Requirement And Design Coverage Matrix

| Approved requirement/design section | Owning TDD tasks | Required proof |
|---|---:|---|
| Schema A: native_snapshot mode, point_publication holder, two unique defenses | 1–2 | dual-engine 063 apply/down, conversion, check/index/FK/UTC tests |
| PostgreSQL CI executes migration 063 rather than only 062 | 1, 15, 18 | workflow regex assertion plus required real-PostgreSQL 063 suite with no skip |
| Parent migration reservation 063–070 | 1, final audit | paired 063 exists; 064–070 remain consistently reserved/free |
| Exact full ID from final Restic summary | 3 | real JSONL parser fixtures; full lowercase ID; final-summary and exit precedence tests |
| No latest/prefix/time/difference attribution | 3, 7, 9 | allowlist/source-boundary tests and reconciliation negative tests |
| Exact two tags plus absent original; rewrite isolation | 3–4, 7 | add/set-same/add-remove rewrite fixtures fail `provider_snapshot_rewritten` |
| Stored Snapshot.Summary reconstruction only after durable exit zero | 3, 7 | exact lookup + known-exit-zero recovery; marker/missing/invalid summary cases fail closed |
| Provider-neutral evidence executor/coordinator seam | 6, 8 | compile-time ports, non-evidence regression, Restic adapter-only mapping |
| Transfer result independent from publication result | 6, 8 | TaskRun success/evidence defect/publication failure matrix and no transfer retry |
| RecordProviderCommit stops at verifying; TaskRun does not wait | 6, 8 | blocking worker test and verifying durable wake-loss test |
| Complete exact manifest and honest fidelity | 4, 7 | canonical digest/count/fidelity/header/identity tests and active-complete-only transaction |
| Frozen Restic fidelity profile | 4, 7 | immutable-copy getter, exact included/commit-bound/not-exposed arrays, identical manifest/point JSON, unknown/extra claim rejection |
| Partial/unavailable cannot become trusted | 4, 7 | disjoint domain/terminator, inactive diagnostic, no point projection |
| One immutable point-wide deadline across stages | 2, 6–7 | supplied-deadline lease tests, takeover/restart/deadline CAS tests |
| In-transaction fence validation and lock order | 2, 6–7 | rollback/stale fence/late manifest tests; point-before-lease transaction code |
| Async bounded worker, wake loss, fairness, lease-expiry retry spacing, restart | 7 | startup/periodic/keyset/poison/lost-wake/shutdown/race tests |
| Bounded durable backoff and scan fairness | 7 | persisted attempt/last-attempt timing, capped backoff, keyset overfetch/scan ceiling, poison-row rotation, no busy loop |
| Actual manifest/reconcile command admission | 5, 7 | blocking concrete `OperationManifest` and `OperationReconcile` calls prove token-before-read and hold-through-join/audit; no nested admission |
| Crash: snapshot exists before DB commit | 7 | marker-absent/outcome-unknown quarantine; no auto-publish |
| Crash: DB preparing/verifying without usable snapshot/manifest | 7 | zero/multiple/offline/deadline/verifying rebuild tests |
| Managed-history permanent latch and admission-mode floor | 5, 6, 9–10 | every state/FK-null/tombstone, active-lease-only rollback-safe token, disabled backup/read zero legacy I/O, unlinked global-history tests |
| FK-null immutable-lineage fallback and live-FK conflict rejection | 5–7, 9 | copied lineage survives Task/TaskRun deletion; live FK disagreement rejects guard, predecessor selection, and reconciliation |
| All-command generation admission drain | 5, 9–10 | every operation constant plus concrete handler/index/anomaly/retention/shutdown races |
| Legacy list/files/search/diff/snapshot restore exact Task lineage | 5, 9 | committed-set/prefix/filter/contamination tests |
| Legacy index never feeds publication/Catalog truth | 9 | source-boundary, exact EntryLister traversal, coverage tests |
| Legacy exact-index completion marker | 9 | crash/partial rows lack marker and remain invisible; empty snapshot marker; previous marked set retained until full replacement |
| Anomaly exact current/previous same Task | 9 | committed observer and no-predecessor/no-latest tests |
| Restore latest and untagged retention fail closed | 9 | zero credential/SSH/runner calls in managed/rollback-safe modes |
| Pristine feature-disabled compatibility and zero asset side effects | 5–6, 8–10 | compatibility sessions and existing handler/runner regression suites |
| Shared runtime, one transport/gate/Registry | 10 | identity/call-counter and Router source-boundary tests |
| Runtime forwards retained tombstone source once | 5, 10 | tombstone-only false startup resolves rollback-safe through the same Admission/Repository resolver |
| Shared-Repository multi-Task isolation | 3–7, 9 | integrated concurrent/retry/manual/foreign snapshot fixture proves exact tags/full IDs and no cross-Task point/index/read/anomaly ownership |
| Settings serialization and enable-generation race safety | 2, 5, 10 | explicit immutable current+overlay validation; concurrent batch/import/delete; enabled true/false/no-op drain; final DB/mode/generation agreement |
| Application downgrade/schema-down/startup preflight | 1, 5, 10 | every command drains; history/active lease reject both downs; false+lease initializes rollback-safe; query error stays unready |
| Interrupted-run callback is restart-only, never live-run mutation | 7–8, 10 | fast worker/post-hook registry race skips CAS; restart without registry entry reconciles; final unfiltered stale-run query gates readiness |
| Shutdown/reconciliation ordering and unfiltered startup readiness | 7–8, 10 | blocking operation matrix, wake race, live-lease/backoff/beyond-scan point query, Manager-owned stale-run batch/final query, pristine compatibility, schedules-after-ready tests |
| Typed audit, metrics, error/log redaction, and credential safety | 2–10 | action registry, audit-failure/legacy-block metrics, bounded labels, at-rest/source scans, safe fields/codes, fake secret naming |
| No frontend/public asset API/default enable/destructive Provider mutation | all, final audit | changed-path manifest, route/source scan, default setting assertion |
| Correct work commit/Trellis archive/PR/CI/post-merge flow | 16–18 | git log order, PR required checks, workflow monitoring, synced main |

## 15. Final Verification Gate — Before Phase 3.4

- [x] **Step 15.0: Load the required final-check skills.**

Load `trellis-check` and `superpowers:verification-before-completion`, read their current instructions completely, and record any additional project-specific command they require before running this section. Do not claim completion or enter Phase 3.4 from remembered results.

Expected: the execution session records both skills as loaded and every result below is produced freshly from the final working tree.

- [x] **Step 15.1: Verify planned files only and inspect the complete diff.**

```bash
set -euo pipefail
git status --short
git status --porcelain=v1 -uall
git diff --name-status
git diff --stat
git diff --check
untracked_check_output="$({
  while IFS= read -r -d '' file; do
    set +e
    file_output="$(git diff --no-index --check /dev/null "$file" 2>&1)"
    file_status=$?
    set -e
    if [ "$file_status" -gt 1 ]; then
      printf '%s\n' "$file_output"
      exit "$file_status"
    fi
    printf '%s' "$file_output"
  done < <(git ls-files -z --others --exclude-standard)
})" || exit 1
test -z "$untracked_check_output"
```

Expected: every tracked and untracked path is listed in Section 2 or is a Trellis lifecycle file already owned by the parent/Child 3; no frontend source, migration other than 063, generated Swagger, Provider deletion code, or unrelated user file appears; both tracked and untracked whitespace checks exit 0.

- [x] **Step 15.2: Run focused exact-lineage suites.**

```bash
go -C backend test ./internal/sshutil -run 'CommandRunner.*Execution' -count=1
go -C backend test ./internal/backupasset/... -run 'Restic|Publication|Manifest|Admission|Lineage|Lease|Runtime|Reconcile' -count=1
go -C backend test ./internal/task/... ./internal/snapshot ./internal/anomaly ./internal/api/... -run 'Restic|Publication|Lineage|Snapshot|Retention|Runtime|Transition|Shutdown' -count=1
```

Expected: all packages PASS with zero failure/panic; tests outside a package regex may report `[no tests to run]` but the package must compile.

- [x] **Step 15.3: Run mandatory race/repetition suites.**

```bash
go -C backend test -race ./internal/sshutil ./internal/backupasset/... ./internal/task/... -run 'Execution|Publication|Manifest|Admission|Lineage|Reconcile|Shutdown|Interrupted' -count=10
```

Expected: PASS with no `DATA RACE`, timeout, goroutine leak assertion, flaky fence, or wake panic.

- [x] **Step 15.4: Run SQLite and real PostgreSQL migration 063.**

Start an isolated PostgreSQL 18 fixture only when `TEST_POSTGRES_DSN` is not already provided:

```bash
set -euo pipefail
PG063_CONTAINER=""
cleanup_pg063() {
  if [ -n "$PG063_CONTAINER" ]; then
    docker rm -f "$PG063_CONTAINER" >/dev/null 2>&1 || true
    PG063_CONTAINER=""
  fi
}
trap cleanup_pg063 EXIT INT TERM
if [ -z "${TEST_POSTGRES_DSN:-}" ]; then
  PG063_CONTAINER="xirang-pg063-$$"
  docker run --rm -d --name "$PG063_CONTAINER" \
    -e POSTGRES_PASSWORD=FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY \
    -e POSTGRES_DB=xirang_test -p 127.0.0.1::5432 postgres:18-alpine
  for attempt in $(seq 1 30); do
    docker exec "$PG063_CONTAINER" pg_isready -U postgres -d xirang_test >/dev/null 2>&1 && break
    if [ "$attempt" -eq 30 ]; then docker logs "$PG063_CONTAINER"; docker stop "$PG063_CONTAINER"; exit 1; fi
    sleep 1
  done
  PG063_PORT=$(docker port "$PG063_CONTAINER" 5432/tcp | awk -F: 'NR==1 {print $NF}')
  TEST_POSTGRES_DSN="postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:${PG063_PORT}/xirang_test?sslmode=disable"
fi
pg063_status=0
REQUIRE_POSTGRES_MIGRATION_TEST=1 TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" \
  go -C backend test ./internal/database -run 'TestBackupAssetMigration063' -count=1 || pg063_status=$?
cleanup_pg063
trap - EXIT INT TERM
test "$pg063_status" -eq 0
```

Expected: SQLite and PostgreSQL 063 apply/down/conversion/unique/down-rejection/UTC tests PASS; no SKIP. If Docker is unavailable, provide a real `TEST_POSTGRES_DSN`; skipping is not acceptable for this gate.

When Docker is available but its bridge driver cannot create the required veth pair, use the same disposable `postgres:18-alpine` fixture with `--network host` after proving that port `5432` has no listener. Keep the required-mode test unchanged and use `TEST_POSTGRES_DSN='postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:5432/xirang_test?sslmode=disable'`; remove the named container after the test. This is only a test-environment transport fallback, not permission to skip the real PostgreSQL gate.

- [x] **Step 15.5: Verify migration parity, UTC safety, and reservation.**

```bash
set -euo pipefail
for engine in sqlite postgres; do
  test -f "backend/internal/database/migrations/${engine}/000063_backup_asset_publication_contract.up.sql"
  test -f "backend/internal/database/migrations/${engine}/000063_backup_asset_publication_contract.down.sql"
done
test "$(find backend/internal/database/migrations/sqlite -maxdepth 1 -type f \( -name '00006[4-9]_*.sql' -o -name '000070_*.sql' \) | wc -l)" -eq 0
test "$(find backend/internal/database/migrations/postgres -maxdepth 1 -type f \( -name '00006[4-9]_*.sql' -o -name '000070_*.sql' \) | wc -l)" -eq 0
bash scripts/check-migration-utc-safety.sh
while IFS=: read -r version slug; do
  rg -q "${version}_${slug}" .trellis/tasks/07-12-backup-data-explorer-design/implement.md
done <<'RESERVATIONS'
000064:backup_asset_search
000065:backup_asset_content
000066:backup_asset_processing
000067:backup_asset_export
000068:backup_asset_recovery
000069:backup_asset_lifecycle
000070:backup_asset_ga
RESERVATIONS
while IFS=: read -r stale_version slug; do
  if rg -q "${stale_version}_${slug}" .trellis/tasks/07-12-backup-data-explorer-design/implement.md; then
    exit 1
  else
    test "$?" -eq 1
  fi
done <<'STALE_RESERVATIONS'
000063:backup_asset_search
000064:backup_asset_content
000065:backup_asset_processing
000066:backup_asset_export
000067:backup_asset_recovery
000068:backup_asset_lifecycle
000069:backup_asset_ga
STALE_RESERVATIONS
rg -q '000062 → … → 000070' .trellis/tasks/07-12-backup-data-explorer-design/implement.md
rg -q '000063_backup_asset_publication_contract' .trellis/tasks/07-14-backup-assets-restic-lineage/implement.md
rg -Fq 'TestBackupAssetMigration0(62|63)Postgres' .github/workflows/ci.yml
```

Expected: paired 063 exists, no unapproved 064–070 file exists, UTC scan passes, and all planning references preserve the approved reservation.

- [x] **Step 15.6: Run backend and repository-wide gates.**

```bash
set -euo pipefail
make backend-test
make lint-backend
make backend-build
env -u NODE_ENV make check
rm -f backend/xirang-server
test ! -e backend/xirang-server
git status --short --untracked-files=all
```

Expected: every gate exits 0; `env -u NODE_ENV make check` proves backend lint/test/build plus frontend lint/test/build regressions without inheriting the shell's production-mode test disablement. The scoped cleanup removes only Make's generated `backend/xirang-server`, and the final status contains no build artifact or unexpected path.

- [x] **Step 15.7: Run documentation, security, and dependency-direction checks against the working diff.**

```bash
set -euo pipefail
doc_changed_files="$({ git diff --name-only HEAD; git ls-files --others --exclude-standard; } | LC_ALL=C sort -u)"
doc_freshness_output="$(DOC_FRESHNESS_CHANGED_FILES="$doc_changed_files" bash scripts/check-doc-freshness.sh)"
printf '%s\n' "$doc_freshness_output"
case "$doc_freshness_output" in *"⚠️"*) exit 1 ;; esac
go -C backend test ./internal/backupasset/provider -run 'TestPublicationSourceBoundaries' -count=1
provider_dependencies="$(go -C backend list -deps ./internal/backupasset/provider)" || exit 1
if printf '%s\n' "$provider_dependencies" | rg -q 'xirang/backend/internal/(api|task/executor|backupasset/repository|backupasset/runtime)'; then
  exit 1
else
  test "$?" -eq 1
fi
assert_no_match() {
  if rg -n "$@"; then
    return 1
  else
    test "$?" -eq 1
  fi
}
assert_no_match 'restic[^\n]*(forget|prune|delete|restore|init)|--latest|latest 2' \
  backend/internal/backupasset/provider/restic_publication.go \
  backend/internal/backupasset/provider/restic_manifest.go \
  backend/internal/backupasset/repository/publication_*.go
assert_no_match 'fmt\.(Print|Printf|Println)|log\.(Print|Printf|Println)' \
  backend/internal/backupasset/provider/restic_publication.go \
  backend/internal/backupasset/provider/restic_manifest.go \
  backend/internal/backupasset/repository/publication_*.go \
  backend/internal/backupasset/runtime/*.go
```

Expected: doc freshness passes; boundary test passes; each negated scan exits 0; Provider dependency list contains no forbidden layer.

- [x] **Step 15.8: Revalidate task artifacts and review the final task state.**

```bash
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-14-backup-assets-restic-lineage
python3 ./.trellis/scripts/get_context.py --mode phase --step 3.3 --platform codex
git diff --check
```

Expected: both task validations exit 0; Child 3 remains `in_progress`; spec update step is satisfied by Task 10's exact spec changes; final whitespace check passes.

## 16. Phase 3.4 Work Commit Plan

Do not execute this section until every Step 15 command is freshly green. First run `git status --porcelain` and `git log --oneline -5`, classify every dirty path, and present this exact logical grouping plus any unexpected paths to the user for the workflow's one-shot confirmation. Never use `git add .`, a directory-wide add, wildcard staging, amend, or push here.

- [ ] **Step 16.1: Commit the reviewed parent/Child 3 planning package.**

```bash
git add \
  .trellis/tasks/07-12-backup-data-explorer-design/prd.md \
  .trellis/tasks/07-12-backup-data-explorer-design/design.md \
  .trellis/tasks/07-12-backup-data-explorer-design/implement.md \
  .trellis/tasks/07-12-backup-data-explorer-design/task.json \
  .trellis/tasks/07-14-backup-assets-restic-lineage/task.json \
  .trellis/tasks/07-14-backup-assets-restic-lineage/prd.md \
  .trellis/tasks/07-14-backup-assets-restic-lineage/design.md \
  .trellis/tasks/07-14-backup-assets-restic-lineage/implement.md \
  .trellis/tasks/07-14-backup-assets-restic-lineage/implement.jsonl \
  .trellis/tasks/07-14-backup-assets-restic-lineage/check.jsonl \
  .trellis/tasks/07-14-backup-assets-restic-lineage/research/restic-lineage-evidence.md
git diff --cached --name-only
git diff --cached --check
git commit -m "docs: finalize Restic lineage implementation plan"
```

Expected: staged list contains exactly those changed planning paths; commit succeeds.

- [ ] **Step 16.2: Commit Schema A and root publication contracts.**

```bash
git add \
  backend/internal/database/migrations/sqlite/000063_backup_asset_publication_contract.up.sql \
  backend/internal/database/migrations/sqlite/000063_backup_asset_publication_contract.down.sql \
  backend/internal/database/migrations/postgres/000063_backup_asset_publication_contract.up.sql \
  backend/internal/database/migrations/postgres/000063_backup_asset_publication_contract.down.sql \
  backend/internal/database/backup_asset_migrations_integration_test.go \
  .github/workflows/ci.yml \
  backend/internal/backupasset/domain.go backend/internal/backupasset/domain_test.go \
  backend/internal/backupasset/errors.go \
  backend/internal/backupasset/publication.go backend/internal/backupasset/publication_test.go \
  backend/internal/backupasset/canonical.go backend/internal/backupasset/canonical_test.go \
  backend/internal/backupasset/service.go backend/internal/backupasset/service_test.go \
  backend/internal/backupasset/audit_action.go backend/internal/backupasset/audit_action_test.go \
  backend/internal/backupasset/lease.go backend/internal/backupasset/lease_test.go \
  backend/internal/settings/service.go backend/internal/settings/service_test.go \
  backend/internal/sshutil/lifecycle.go backend/internal/sshutil/lifecycle_test.go
git diff --cached --name-only
git diff --cached --check
git commit -m "feat: repair backup asset publication schema"
```

- [ ] **Step 16.3: Commit exact Restic command evidence and manifests.**

```bash
git add \
  backend/internal/sshutil/command_runner.go backend/internal/sshutil/command_runner_test.go \
  backend/internal/sshutil/node_dialer.go backend/internal/sshutil/node_dialer_test.go \
  backend/internal/backupasset/provider/contracts.go \
  backend/internal/backupasset/provider/registry.go backend/internal/backupasset/provider/registry_test.go \
  backend/internal/backupasset/provider/runner.go backend/internal/backupasset/provider/runner_test.go \
  backend/internal/backupasset/provider/restic.go \
  backend/internal/backupasset/provider/restic_publication.go backend/internal/backupasset/provider/restic_publication_test.go \
  backend/internal/backupasset/provider/restic_manifest.go backend/internal/backupasset/provider/restic_manifest_test.go \
  backend/internal/backupasset/provider/publication_boundary_test.go \
  backend/internal/backupasset/provider/testdata/restic/backup-success.ndjson \
  backend/internal/backupasset/provider/testdata/restic/backup-missing-summary.ndjson \
  backend/internal/backupasset/provider/testdata/restic/backup-malformed.ndjson \
  backend/internal/backupasset/provider/testdata/restic/backup-truncated.ndjson \
  backend/internal/backupasset/provider/testdata/restic/snapshots-exact.json \
  backend/internal/backupasset/provider/testdata/restic/snapshots-rewritten.json \
  backend/internal/backupasset/provider/testdata/restic/manifest-valid.ndjson \
  backend/internal/backupasset/provider/testdata/restic/manifest-depth-edge.ndjson \
  backend/internal/backupasset/provider/testdata/restic/manifest-rewritten.ndjson
git diff --cached --name-only
git diff --cached --check
git commit -m "feat: capture exact Restic backup evidence"
```

- [ ] **Step 16.4: Commit coordinator, worker, runtime, and Task integration.**

```bash
git add \
  backend/internal/backupasset/publication/contracts.go backend/internal/backupasset/publication/contracts_test.go \
  backend/internal/backupasset/publication/metrics.go backend/internal/backupasset/publication/metrics_test.go \
  backend/internal/backupasset/repository/service.go backend/internal/backupasset/repository/testutil_test.go backend/internal/backupasset/repository/query_test.go \
  backend/internal/backupasset/repository/connect.go backend/internal/backupasset/repository/connect_test.go \
  backend/internal/backupasset/repository/managed_history.go backend/internal/backupasset/repository/managed_history_test.go \
  backend/internal/backupasset/repository/lineage_guard.go backend/internal/backupasset/repository/lineage_guard_test.go \
  backend/internal/backupasset/repository/publication_identity.go backend/internal/backupasset/repository/publication_identity_test.go \
  backend/internal/backupasset/repository/publication_execution.go backend/internal/backupasset/repository/publication_execution_test.go \
  backend/internal/backupasset/repository/publication_commit_test.go \
  backend/internal/backupasset/repository/publication_reconcile.go backend/internal/backupasset/repository/publication_reconcile_test.go \
  backend/internal/backupasset/repository/publication_integration_test.go \
  backend/internal/backupasset/runtime/admission.go backend/internal/backupasset/runtime/admission_test.go \
  backend/internal/backupasset/runtime/controller.go backend/internal/backupasset/runtime/admission_controller_test.go \
  backend/internal/backupasset/runtime/runtime.go backend/internal/backupasset/runtime/runtime_test.go \
  backend/internal/backupasset/runtime/publication_worker.go backend/internal/backupasset/runtime/publication_worker_test.go \
  backend/internal/task/executor/evidence.go backend/internal/task/executor/evidence_test.go \
  backend/internal/task/executor/executor.go \
  backend/internal/task/executor/restic_executor.go backend/internal/task/executor/restic_executor_test.go \
  backend/internal/task/publication_runner.go backend/internal/task/publication_runner_test.go \
  backend/internal/task/publication_interrupted.go backend/internal/task/publication_interrupted_test.go \
  backend/internal/task/manager.go backend/internal/task/manager_test.go \
  backend/internal/task/runner.go
git diff --cached --name-only
git diff --cached --check
git commit -m "feat: publish fenced Restic recovery points"
```

- [ ] **Step 16.5: Commit legacy isolation, bootstrap, docs, and specs.**

```bash
git add \
  backend/internal/task/retention.go backend/internal/task/retention_test.go \
  backend/internal/snapshot/indexer.go backend/internal/snapshot/indexer_test.go \
  backend/internal/anomaly/snapshot_diff.go backend/internal/anomaly/snapshot_diff_test.go \
  backend/internal/api/handlers/snapshot_handler.go backend/internal/api/handlers/snapshot_handler_test.go \
  backend/internal/api/handlers/snapshot_search_handler.go backend/internal/api/handlers/snapshot_search_handler_test.go \
  backend/internal/api/handlers/snapshot_diff_handler.go backend/internal/api/handlers/snapshot_diff_handler_test.go \
  backend/internal/api/handlers/settings_handler.go backend/internal/api/handlers/settings_transition_test.go \
  backend/internal/api/handlers/config_handler.go backend/internal/api/handlers/config_handler_test.go \
  backend/internal/api/handlers/step_up_test.go backend/internal/api/handlers/credential_access_grant_test.go \
  backend/internal/api/router.go backend/internal/api/router_test.go \
  backend/cmd/server/main.go backend/cmd/server/main_test.go \
  backend/README_backend.md .env.deploy backend/.env.production.example docs/env-vars.md docs/admin/backup-recovery.md \
  .trellis/spec/backend/database-guidelines.md \
  .trellis/spec/backend/directory-structure.md \
  .trellis/spec/backend/quality-guidelines.md
git diff --cached --name-only
git diff --cached --check
git commit -m "fix: isolate legacy Restic lineage"
```

Expected after all work commits:

```bash
git status --short
git log --oneline -5
```

Only Trellis task/workspace lifecycle paths may become dirty in the next section; work commits appear in the planned order. If an expected file is unchanged, `git add` is harmless; if an unexpected path exists, stop rather than include it.

## 17. Same-Branch Trellis Finish-Work

Load `trellis-finish-work` locally after work commits; do not push or create a PR first.

- [ ] **Step 17.1: Survey and prove no current-task product work is uncommitted.**

```bash
python3 ./.trellis/scripts/get_context.py --mode record
git status --porcelain
```

Expected: no uncommitted product/docs/spec path from Sections 2/16. Unrelated parallel work, if any, is reported and excluded. If current-task code remains, return to Phase 3.4.

- [ ] **Step 17.2: Archive Child 3 on the same work branch.**

```bash
set -euo pipefail
archive_before="$(git rev-parse HEAD)"
python3 ./.trellis/scripts/task.py archive 07-14-backup-assets-restic-lineage
archive_after="$(git rev-parse HEAD)"
test "$archive_after" != "$archive_before"
test "$(git show -s --format=%s "$archive_after")" = "chore(task): archive 07-14-backup-assets-restic-lineage"
test ! -e .trellis/tasks/07-14-backup-assets-restic-lineage
test -f .trellis/tasks/archive/2026-07/07-14-backup-assets-restic-lineage/task.json
test "$(python3 -c 'import json; print(json.load(open(".trellis/tasks/archive/2026-07/07-14-backup-assets-restic-lineage/task.json"))["status"])')" = "completed"
archive_unexpected="$(git show --format= --name-only "$archive_after" | sed '/^$/d' | awk '$0 !~ /^\.trellis\/tasks\/(07-14-backup-assets-restic-lineage\/|archive\/2026-07\/07-14-backup-assets-restic-lineage\/)/ { print }')"
test -z "$archive_unexpected"
```

Expected: Child 3 moves under `.trellis/tasks/archive/2026-07/`, status becomes completed, parent linkage remains valid, and exactly one path-scoped `chore(task): archive 07-14-backup-assets-restic-lineage` commit advances HEAD. Do not archive the planning parent.

- [ ] **Step 17.3: Record the session using only the five verified work-commit hashes.**

Collect exactly one hash for each reviewed Section 16 commit subject, reject a missing or duplicate subject, exclude the archive hash by construction, and pass the generated CSV directly:

```bash
set -euo pipefail
work_commits="$({
  git log --reverse --format='%H%x09%s' e1a8f24c3c8b8b71581cedc148c5f32482c8ac0b..HEAD
} | awk -F '\t' '
BEGIN {
  expected["docs: finalize Restic lineage implementation plan"] = 1
  expected["feat: repair backup asset publication schema"] = 1
  expected["feat: capture exact Restic backup evidence"] = 1
  expected["feat: publish fenced Restic recovery points"] = 1
  expected["fix: isolate legacy Restic lineage"] = 1
}
$2 in expected {
  count[$2]++
  hashes[++found] = $1
}
END {
  if (found != 5) exit 1
  for (subject in expected) {
    if (count[subject] != 1) exit 1
  }
  for (index = 1; index <= found; index++) print hashes[index]
}')" || exit 1
test "$(printf '%s\n' "$work_commits" | awk '/^[0-9a-f]{40}$/ { valid++ } END { print valid + 0 }')" = "5"
test "$(printf '%s\n' "$work_commits" | sort -u | wc -l | tr -d ' ')" = "5"
work_commit_csv="$(printf '%s\n' "$work_commits" | paste -sd, -)"
journal_before="$(git rev-parse HEAD)"
python3 ./.trellis/scripts/add_session.py \
  --title "Restic exact lineage and recovery-point publication" \
  --commit "$work_commit_csv" \
  --summary "Implemented exact Restic run evidence, fenced asynchronous RecoveryPoint publication, reconciliation, managed-history admission, and legacy lineage guards; all local gates passed."
journal_after="$(git rev-parse HEAD)"
test "$journal_after" != "$journal_before"
test "$(git show -s --format=%s "$journal_after")" = "chore: record journal"
test -z "$(git show --format= --name-only "$journal_after" | sed '/^$/d' | awk '$0 !~ /^\.trellis\/workspace\// { print }')"
test -z "$(git status --porcelain)"
```

Expected: both count checks pass, `work_commit_csv` contains five distinct full hashes in Section 16 order, `add_session.py` advances HEAD with one workspace-only `chore: record journal` commit, and the tree is clean. Final log order is work commits → archive commit → journal commit.

- [ ] **Step 17.4: Revalidate clean same-branch state.**

```bash
git status --short --branch
git log --oneline -8
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-12-backup-data-explorer-design
```

Expected: clean branch, archive and journal commits present after all work commits, parent validates, and no second PR is needed for Trellis bookkeeping.

## 18. Push, Pull Request, Required CI, Merge, And Post-Merge

- [ ] **Step 18.1: Push the complete work+archive+journal branch and create one PR.**

```bash
git push -u origin codex/backup-assets-restic-lineage
gh pr create \
  --base main \
  --head codex/backup-assets-restic-lineage \
  --title "feat: publish exact Restic recovery points" \
  --body "Implements Child 3 exact Restic lineage, async fenced publication, reconciliation, managed-history admission, and legacy safety guards. Includes the Child 3 Trellis archive and journal commits on the same branch."
```

Expected: one PR targets `main`; it contains product changes and Trellis archive/journal together.

- [ ] **Step 18.2: Monitor every required check and fix failures on this branch.**

```bash
set -euo pipefail
gh pr checks --watch --fail-fast=false
PR_NUMBER="$(gh pr view --json number --jq '.number')"
PR_HEAD_SHA="$(gh pr view "$PR_NUMBER" --json headRefOid --jq '.headRefOid')"
test "$(printf '%s' "$PR_HEAD_SHA" | awk '/^[0-9a-f]{40}$/ { print "valid" }')" = "valid"
test "$PR_HEAD_SHA" = "$(git rev-parse HEAD)"
check_rollup="$(gh pr view "$PR_NUMBER" --json statusCheckRollup --jq '.statusCheckRollup[] | [(.name // .context), (.status // ""), (.conclusion // .state // "")] | @tsv')"
required_count=0
while IFS= read -r required_check; do
  required_count=$((required_count + 1))
  result="$(printf '%s\n' "$check_rollup" | awk -F '\t' -v required="$required_check" '
    $1 == required {
      count++
      status = tolower($2)
      conclusion = tolower($3)
      terminal = (status == "" || status == "completed")
      if (terminal && conclusion == "success") passing++
      if (terminal && (conclusion == "success" || conclusion == "skipped")) acceptable++
    }
    END {
      if (count >= 1 && passing >= 1 && acceptable == count) print "pass"
      else print "fail"
    }
  ')"
  test "$result" = "pass"
done <<'REQUIRED_CHECKS'
PR Title
Backend Test & Build
PostgreSQL Migration Parity
Frontend Test & Build
Docker Build
Doc Freshness Check
Migration UTC Safety
REQUIRED_CHECKS
test "$required_count" -eq 7
```

Expected: each of the seven required names—`PR Title`, `Backend Test & Build`, `PostgreSQL Migration Parity`, `Frontend Test & Build`, `Docker Build`, `Doc Freshness Check`, and `Migration UTC Safety`—has at least one terminal `success`. Because CI runs on both push and pull request, additional same-name rollup entries are allowed only when terminal `success` or `skipped`; pending, canceled, or failed duplicates reject the gate. The verified PR head equals local HEAD. Any failure returns to its owning TDD task, adds a reproducing red test when behavior is wrong, and reruns every Step 15 gate. Stage only with the corresponding exact Section 16.2–16.5 path list (after presenting unexpected paths for the workflow confirmation), then create `git commit -m "fix: address Restic publication CI failure"`; never amend a published commit.

Because Child 3 is already archived, do not archive it again and do not rewrite the original five-hash journal. For each post-push fix commit, immediately record a separate same-branch journal entry and push both commits:

```bash
set -euo pipefail
CI_FIX_HASH="$(git rev-parse HEAD)"
test "$(git show -s --format=%s "$CI_FIX_HASH")" = "fix: address Restic publication CI failure"
python3 ./.trellis/scripts/get_context.py --mode record
test -z "$(git status --porcelain | awk '$2 !~ /^\.trellis\/workspace\// { print }')"
python3 ./.trellis/scripts/add_session.py \
  --title "Restic publication CI follow-up" \
  --commit "$CI_FIX_HASH" \
  --summary "Fixed a required CI failure on the existing Restic lineage branch and reran the complete local gate."
CI_JOURNAL_HASH="$(git rev-parse HEAD)"
test "$CI_JOURNAL_HASH" != "$CI_FIX_HASH"
test "$(git show -s --format=%s "$CI_JOURNAL_HASH")" = "chore: record journal"
test -z "$(git show --format= --name-only "$CI_JOURNAL_HASH" | sed '/^$/d' | awk '$0 !~ /^\.trellis\/workspace\// { print }')"
test -z "$(git status --porcelain)"
git push origin codex/backup-assets-restic-lineage
gh pr checks --watch --fail-fast=false
```

Expected: the fix work commit is followed by its journal commit in the same PR; there is still one PR and one Child 3 archive. Repeat this exact red→fix→full-gate→work-commit→journal→push→watch cycle until every required check succeeds or a real external blocker is recorded.

- [ ] **Step 18.3: Merge only when all required checks are successful.**

```bash
set -euo pipefail
PR_NUMBER="$(gh pr view --json number --jq '.number')"
PR_HEAD_SHA="$(gh pr view "$PR_NUMBER" --json headRefOid --jq '.headRefOid')"
test "$PR_HEAD_SHA" = "$(git rev-parse HEAD)"
check_rollup="$(gh pr view "$PR_NUMBER" --json statusCheckRollup --jq '.statusCheckRollup[] | [(.name // .context), (.status // ""), (.conclusion // .state // "")] | @tsv')"
required_count=0
while IFS= read -r required_check; do
  required_count=$((required_count + 1))
  result="$(printf '%s\n' "$check_rollup" | awk -F '\t' -v required="$required_check" '
    $1 == required {
      count++
      status = tolower($2)
      conclusion = tolower($3)
      terminal = (status == "" || status == "completed")
      if (terminal && conclusion == "success") passing++
      if (terminal && (conclusion == "success" || conclusion == "skipped")) acceptable++
    }
    END {
      if (count >= 1 && passing >= 1 && acceptable == count) print "pass"
      else print "fail"
    }
  ')"
  test "$result" = "pass"
done <<'REQUIRED_CHECKS'
PR Title
Backend Test & Build
PostgreSQL Migration Parity
Frontend Test & Build
Docker Build
Doc Freshness Check
Migration UTC Safety
REQUIRED_CHECKS
test "$required_count" -eq 7
test "$(gh pr view "$PR_NUMBER" --json state --jq '.state')" = "OPEN"
test "$(gh pr view "$PR_NUMBER" --json mergeable --jq '.mergeable')" = "MERGEABLE"
gh pr merge "$PR_NUMBER" --squash --delete-branch --match-head-commit "$PR_HEAD_SHA"
test "$(gh pr view "$PR_NUMBER" --json state --jq '.state')" = "MERGED"
```

Expected: the same seven exact checks are freshly re-proven successful against the unchanged local/PR head; same-name push/PR duplicates are terminal success/skipped only. `--match-head-commit` prevents a later push from racing the merge, and squash merge completes. Do not merge while any required name is missing or lacks success, or while any matching entry is pending, canceled, or failing.

- [ ] **Step 18.4: Monitor post-merge automation and record expected release behavior.**

```bash
set -euo pipefail
PR_NUMBER="$(gh pr list --state merged --head codex/backup-assets-restic-lineage --limit 1 --json number --jq '.[0].number')"
test -n "$PR_NUMBER"
MERGED_SHA="$(gh pr view "$PR_NUMBER" --json mergeCommit --jq '.mergeCommit.oid')"
test "$(printf '%s' "$MERGED_SHA" | awk '/^[0-9a-f]{40}$/ { print "valid" }')" = "valid"
RELEASE_PLEASE_RUN=""
for attempt in $(seq 1 24); do
  RELEASE_PLEASE_RUN="$(gh run list --workflow release-please.yml --commit "$MERGED_SHA" --event push --limit 1 --json databaseId --jq '.[0].databaseId')"
  [ -n "$RELEASE_PLEASE_RUN" ] && break
  sleep 5
done
test -n "$RELEASE_PLEASE_RUN"
gh run watch "$RELEASE_PLEASE_RUN" --exit-status
wait_optional_workflow() {
  workflow="$1"
  run_ids=""
  for attempt in $(seq 1 24); do
    run_ids="$(gh run list --workflow "$workflow" --commit "$MERGED_SHA" --limit 20 --json databaseId --jq '.[].databaseId')"
    [ -n "$run_ids" ] && break
    sleep 5
  done
  if [ -z "$run_ids" ]; then
    printf '%s\tabsent-after-discovery-window\n' "$workflow"
    return 0
  fi
  for run_id in $run_ids; do
    gh run watch "$run_id" --exit-status
  done
  final_runs="$(gh run list --workflow "$workflow" --commit "$MERGED_SHA" --limit 20 --json databaseId,status,conclusion --jq '.[] | [.databaseId, .status, (.conclusion // "")] | @tsv')"
  test -n "$final_runs"
  test "$(printf '%s\n' "$final_runs" | awk -F '\t' '
    { count++; if (tolower($2) == "completed" && tolower($3) == "success") passing++ }
    END { if (count > 0 && count == passing) print "pass"; else print "fail" }
  ')" = "pass"
  printf '%s\tpresent-and-successful\n' "$workflow"
}
wait_optional_workflow publish-images.yml
wait_optional_workflow dockerhub-description.yml
gh run list --workflow release-please.yml --commit "$MERGED_SHA" --limit 10
gh run list --workflow publish-images.yml --commit "$MERGED_SHA" --limit 10
gh run list --workflow dockerhub-description.yml --commit "$MERGED_SHA" --limit 10
```

Expected: the exact merge SHA has a `Release Please` run and `gh run watch --exit-status` proves it completes successfully. Each conditional workflow is polled for a full two-minute discovery window before being recorded as absent; if discovered, every exact-SHA run must reach terminal success. A normal feature squash updates/creates the release PR but does not itself publish a stable GitHub Release, so `Publish Docker Images` is normally recorded absent. `Sync Docker Hub Description` is normally absent because root `README.md` and its workflow file are unchanged.

- [ ] **Step 18.5: Sync local main to the merged origin and prove equality.**

```bash
git switch main
git pull --ff-only origin main
git fetch origin --prune
test "$(git rev-parse main)" = "$(git rev-parse origin/main)"
git status --short --branch
```

Expected: local `main` equals `origin/main`, worktree is clean, and no new child branch starts until separately authorized.

## 19. Planning Self-Review Record

Executed on 2026-07-14 at `2026-07-14T14:20:24+08:00` against branch `codex/backup-assets-restic-lineage`, with local HEAD and `origin/main` both `e1a8f24c3c8b8b71581cedc148c5f32482c8ac0b`.

| Audit | Fresh command/evidence | Result |
|---|---|---|
| Approved spec coverage | Section 14 anchor script plus independent cross-artifact contract review over PRD/design/Sections 1–18 | 9/9 required groups present, 39 explicit matrix rows, no approval-blocking omission |
| Placeholder/completeness | `rg` over the enumerated incomplete-marker, deferred-fill/similarity, angle-placeholder, omitted-body, and cross-step-shorthand patterns | 0 matches |
| Type/signature/TDD consistency | independent final ledger/call-site review plus ordered red→failure→minimum implementation→green review | no blocking inconsistency; readiness, mode floor, current-run protection, tombstone injection, settings serialization, and observer ownership all have exact ports and red tests |
| Bash command syntax | extract every `bash` fence and run `bash -n` | 87/87 pass |
| File existence | Section 2 parser with `Path.exists()` | 54/54 Create paths absent; 68/68 Modify paths present |
| Commit ownership | parse Section 16 `git add` lists and compare with Section 2 | 122/122 implementation paths owned exactly once; no missing, duplicate, or extra product path |
| Migration reservation | exact required/stale `rg` loops against the parent plan | 063 publication; 064 search; 065 content; 066 processing; 067 export; 068 recovery; 069 lifecycle; 070 GA; no stale pre-shift mapping |
| Command/tool availability | Make-target, script-file, and `command -v` checks | `backend-test`, `lint-backend`, `backend-build`, `check`; all referenced Trellis/project scripts; Bash/Python/Git/Go/Make/rg/Awk/Sed/core shell/GitHub CLI/Docker/npm present |
| Tracked and untracked whitespace | `git diff --check` plus `git diff --no-index --check /dev/null "$file"` for every untracked file | pass |
| Documentation freshness | tracked diff plus untracked-file union passed through `scripts/check-doc-freshness.sh` | `✅ 文档新鲜度检查通过`; no `⚠️` output |
| Trellis validation | `task.py validate` for parent and Child 3 | both report `✓ All validations passed`; both JSONL files validate with 0 entries |
| Scope/state | changed-path assertion, `git status --short --branch`, task JSON status check | only parent/Child 3 planning artifacts are dirty; no backend/frontend/product/migration path; parent and Child 3 both remain `planning` |

After writing this record, the placeholder scan, all 87 Bash syntax checks, 122-path ownership audit, tracked/untracked whitespace checks, doc freshness, parent/child validation, scope assertion, and branch/base/status checks are rerun once more from the final planning tree. The 2026-07-14 lifecycle record above is an approved consistency update: the user approved this plan, explicitly requested Phase 2, and `task.py start` set the task to `in_progress`. Product code and migration creation are now authorized only under the stated TDD steps; commit, push, and PR remain deferred to the stated finish gates.

### Post-Implementation Commit-Plan Consistency Update

Executed on 2026-07-14 at `2026-07-14T22:49:20+08:00` after the final
implementation audit. Section 2 now owns exactly 118 current product/docs/spec
paths, while Section 16 assigns all 129 current changed paths exactly once:
the 118 implementation paths plus 11 reviewed parent/Child 3 planning paths.
The audit found zero unassigned, unchanged, duplicate, or nonexistent staging
paths. It removed obsolete no-op entries and the nonexistent
`publication_commit.go`, and placed the actual runtime controller,
interrupted-run, and settings-transition files in their existing logical
commits. The five approved work-commit subjects and their ordering are
unchanged.
