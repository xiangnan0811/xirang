# Rclone 版本化恢复点 Implementation Plan

> **Planning status (2026-07-16):** 完整计划与自检已完成并获独立批准；用户随后明确授权 `task.py start` 与完整实施。命令尚未执行，任务仍为 `planning`，因为当前 workflow-state 要求本轮 stay in planning；成功启动前不得修改产品或执行 Git 发布动作。

> **For agentic workers:** REQUIRED SUB-SKILL: 仅在获得 `task.py start` 的单独授权后，由 inline 主会话使用 `superpowers:executing-plans` 与 `superpowers:test-driven-development` 逐任务执行。本 Child 明确禁止 implement/check sub-agent。

**Goal:** 为既有 Rclone Task 增加可移植 unique-prefix 与经认证 AWS S3 native-object-version 两种可验证恢复点发布，同时保留 pristine legacy mutable 行为、精确证据和安全回滚边界。

**Architecture:** 在 Child 3/4 的 provider-tagged publication coordinator、lease/fence、worker 和 managed-history latch 上增加单一 `RclonePublicationStrategy`。Portable adapter 使用 bound config、attempt-qualified prefix、canonical manifest 与 marker-last 协议；native adapter 使用 STS-only temporary session、official AWS S3/KMS SDK、双完整 `ListObjectVersions` 观察和 exact VersionId 读证据。两者共享 V3 encrypted binding、Task activation、ProviderCommit、reconciliation、safe API 和前端状态，不创建第二套 publication state machine。

**Tech Stack:** Go 1.26、GORM、SQLite/PostgreSQL 既有 `000062`--`000064` schema、Rclone v1.74.4、SSH/SFTP、AWS SDK for Go v2、Gin、React 18、TypeScript strict、Vite/Vitest、Tailwind/Radix、i18next。

---

## 0. Authority, Gates, And Baseline

- 当前任务状态必须保持 `planning`。完整 `design.md` 与本 `implement.md` 均已于 2026-07-16 独立批准，单独的 `task.py start`/实施授权也已取得；但 start 命令尚未执行，当前 workflow-state 仍要求 stay in planning。
- 在用户批准本计划后，仍须另一条明确授权才能运行 `python3 ./.trellis/scripts/task.py start ...`。在该授权前不得修改 backend/web 产品文件、migration、spec、公开文档、生成 Swagger、提交、push 或创建 PR。
- 分支固定为 `codex/backup-assets-rclone-versioning`，基线固定为 `main@3825c0aa3eb66c865e33c72dc69ec47658e8c1eb`。当前仅有父 task child link 与 Child 5 规划/研究工件；全部视为用户已审阅工作，不得丢弃或重复创建。
- Inline mode 跳过 `implement.jsonl`/`check.jsonl` curation；Phase 2 通过 `trellis-before-dev` 读取本任务工件和实际 specs。实现、检查和 review 都由主会话直接完成。
- Child 5 不创建 migration version，不占用父计划 `000065`--`000071`。只允许在未发布 `000064_backup_asset_rsync_publication_contract.down.sql` 中补 Rclone managed-link guard，并补相应双数据库测试。
- 不在 Tasks 1--11 中间提交。所有功能与文档通过最终门后，Phase 3.4 创建一个工作提交；随后同分支运行 `trellis-finish-work` 形成归档/journal 自动提交。该项目流程覆盖 writing-plans 的逐任务 commit 默认建议。
- 父计划早期 Child 5 摘要中的 native exact-delete 测试已被批准设计替代：产品 runtime 不调用 `DeleteObjectVersion`，不清除 prefix/version/delete marker。只有 build-tagged live-test admin cleanup 可在专属 fixture prefix 外部清理测试数据。

### Activation steps - only after the separate start authorization

- [ ] **Step 0.1: Reconfirm branch, base, dirty ownership, and migration reservation.**

```bash
git fetch origin --prune
git status --short --branch
git rev-parse HEAD
git rev-parse origin/main
if find backend/internal/database/migrations -type f -print | rg -q '/00006[5-9]|/00007[01]'; then echo 'unexpected reserved migration'; exit 1; fi
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-12-backup-data-explorer-design
python3 ./.trellis/scripts/task.py validate .trellis/tasks/07-15-backup-assets-rclone-versioning
```

Expected: current branch is `codex/backup-assets-rclone-versioning`; `HEAD` and `origin/main` still match the reviewed base or the branch is safely rebased and affected planning paths are re-reviewed; only expected planning files are dirty; no unplanned migration consumes `000065`--`000071`; both validations exit 0.

- [ ] **Step 0.2: Activate only Child 5.**

```bash
python3 ./.trellis/scripts/task.py start .trellis/tasks/07-15-backup-assets-rclone-versioning
python3 ./.trellis/scripts/get_context.py
```

Expected: Child 5 becomes `in_progress`; parent remains `planning`; no product or migration file changes.

- [ ] **Step 0.3: Load implementation guidance before the first code edit.**

Load `trellis-before-dev`, `superpowers:executing-plans`, and `superpowers:test-driven-development`; read `prd.md`, `design.md`, this file, both research files, `.trellis/spec/backend/index.md`, `.trellis/spec/frontend/index.md`, `.trellis/spec/guides/index.md`, every referenced backend/frontend guide, `backend/internal/api/handlers/AGENTS.md`, and `web/src/pages/AGENTS.md`.

Expected: the active checklist records the exact current specs and the TDD red-green-refactor rule before product edits.

- [ ] **Step 0.4: Record a green pre-change baseline.**

```bash
go -C backend test ./internal/backupasset/... ./internal/task/... ./internal/api/... ./internal/database -count=1
npm --prefix web run check
git diff --check
```

Expected: all baseline suites pass. Any pre-existing failure is diagnosed before Child 5 code is written.

## 1. Locked Contracts, Files, And Dependency Order

### 1.1 Domain and Task wire contracts

Create `backend/internal/backupasset/rclone_versioning.go`. These closed values are the single source of truth for repository, API and frontend mappings:

```go
const RcloneTaskConfigSchemaVersion = 1

type RcloneTaskConfigV1 struct {
	Version          int                 `json:"version"`
	PublicationMode  TaskPublicationMode `json:"publication_mode"`
	BandwidthLimit   string              `json:"bandwidth_limit,omitempty"`
	Transfers        int                 `json:"transfers,omitempty"`
}

type RcloneVersioningMigrationChoice string
const (
	RcloneImportedBaseline RcloneVersioningMigrationChoice = "imported_baseline"
	RcloneFirstNewPoint     RcloneVersioningMigrationChoice = "first_new_point"
)

type RcloneVersioningState string
const (
	RcloneStateLegacy                  RcloneVersioningState = "legacy"
	RcloneStatePreflightRequired       RcloneVersioningState = "preflight_required"
	RcloneStateCredentialSetupRequired RcloneVersioningState = "credential_setup_required"
	RcloneStateCapabilitySettling      RcloneVersioningState = "capability_settling"
	RcloneStateReady                   RcloneVersioningState = "ready"
	RcloneStatePreparing               RcloneVersioningState = "preparing"
	RcloneStateVerifying               RcloneVersioningState = "verifying"
	RcloneStateCommitted               RcloneVersioningState = "committed"
	RcloneStateDegraded                RcloneVersioningState = "degraded"
	RcloneStateAtRisk                  RcloneVersioningState = "at_risk"
	RcloneStateFailed                  RcloneVersioningState = "failed"
	RcloneStateBlocked                 RcloneVersioningState = "blocked"
	RcloneStateRollbackPrepared        RcloneVersioningState = "rollback_prepared"
)

type RcloneEncryptionProfile string
const (
	RcloneEncryptionNone   RcloneEncryptionProfile = "none"
	RcloneEncryptionSSES3  RcloneEncryptionProfile = "sse_s3"
	RcloneEncryptionSSEKMS RcloneEncryptionProfile = "sse_kms_cmk"
)

type RcloneKMSKeyStatus string
const (
	RcloneKMSNotApplicable RcloneKMSKeyStatus = "not_applicable"
	RcloneKMSReady         RcloneKMSKeyStatus = "ready"
	RcloneKMSDegraded      RcloneKMSKeyStatus = "degraded"
	RcloneKMSAtRisk        RcloneKMSKeyStatus = "at_risk"
	RcloneKMSBlocked       RcloneKMSKeyStatus = "blocked"
)

type RcloneRollbackCapability string
const (
	RcloneRollbackCleanAvailable  RcloneRollbackCapability = "clean_available"
	RcloneRollbackPreparationOnly RcloneRollbackCapability = "preparation_only"
	RcloneRollbackPrepared        RcloneRollbackCapability = "prepared"
)

type RcloneVersioningReasonCode string
const (
	RcloneReasonLegacy                  RcloneVersioningReasonCode = "legacy"
	RcloneReasonPreflightRequired       RcloneVersioningReasonCode = "preflight_required"
	RcloneReasonReady                   RcloneVersioningReasonCode = "ready"
	RcloneReasonCredentialSetupRequired RcloneVersioningReasonCode = "credential_setup_required"
	RcloneReasonCapabilitySettling      RcloneVersioningReasonCode = "capability_settling"
	RcloneReasonPreflightExpired        RcloneVersioningReasonCode = "preflight_expired"
	RcloneReasonTaskRevisionChanged     RcloneVersioningReasonCode = "task_revision_changed"
	RcloneReasonBindingRevisionChanged  RcloneVersioningReasonCode = "binding_revision_changed"
	RcloneReasonPreflightMismatch       RcloneVersioningReasonCode = "preflight_mismatch"
	RcloneReasonFeatureDisabled         RcloneVersioningReasonCode = "feature_disabled"
	RcloneReasonUnsupportedProfile      RcloneVersioningReasonCode = "unsupported_profile"
	RcloneReasonRepositoryOffline       RcloneVersioningReasonCode = "repository_offline"
	RcloneReasonProviderUnavailable     RcloneVersioningReasonCode = "provider_unavailable"
	RcloneReasonProviderTimeout         RcloneVersioningReasonCode = "provider_timeout"
	RcloneReasonProviderResourceLimit   RcloneVersioningReasonCode = "provider_resource_limit"
	RcloneReasonSessionTooShort         RcloneVersioningReasonCode = "session_too_short"
	RcloneReasonVersioningDisabled      RcloneVersioningReasonCode = "versioning_disabled"
	RcloneReasonLifecycleConflict       RcloneVersioningReasonCode = "lifecycle_conflict"
	RcloneReasonEncryptionUnsupported   RcloneVersioningReasonCode = "encryption_unsupported"
	RcloneReasonKMSKeyUnavailable       RcloneVersioningReasonCode = "kms_key_unavailable"
	RcloneReasonKMSPermissionDenied     RcloneVersioningReasonCode = "kms_permission_denied"
	RcloneReasonKMSKeyRingLimit         RcloneVersioningReasonCode = "kms_key_ring_limit"
	RcloneReasonIdentityMismatch        RcloneVersioningReasonCode = "identity_mismatch"
	RcloneReasonCredentialInvalid       RcloneVersioningReasonCode = "credential_invalid"
	RcloneReasonVerificationCostLimit   RcloneVersioningReasonCode = "verification_cost_limit"
	RcloneReasonSourceDrift             RcloneVersioningReasonCode = "source_drift"
	RcloneReasonExternalWriterDetected  RcloneVersioningReasonCode = "external_writer_detected"
	RcloneReasonUnexpectedVersion       RcloneVersioningReasonCode = "unexpected_version"
	RcloneReasonManifestMismatch        RcloneVersioningReasonCode = "manifest_mismatch"
	RcloneReasonMarkerMismatch          RcloneVersioningReasonCode = "marker_mismatch"
	RcloneReasonAdmissionBlocked        RcloneVersioningReasonCode = "admission_blocked"
	RcloneReasonOutcomeUnknown          RcloneVersioningReasonCode = "outcome_unknown"
	RcloneReasonRollbackPrepared        RcloneVersioningReasonCode = "rollback_prepared"
)

type RcloneConsistencyClass string
const (
	RcloneConsistencyNotEvaluated         RcloneConsistencyClass = "not_evaluated"
	RcloneConsistencyObservationallyStable RcloneConsistencyClass = "observationally_stable"
	RcloneConsistencyProviderStrong        RcloneConsistencyClass = "provider_strong"
)

type RcloneHashFidelity string
const (
	RcloneHashNotEvaluated           RcloneHashFidelity = "not_evaluated"
	RcloneHashProviderStrongChecksum RcloneHashFidelity = "provider_strong_checksum"
	RcloneHashDownloadVerifiedBytes  RcloneHashFidelity = "download_verified_bytes"
)

type RcloneCostClass string
const (
	RcloneCostNotEvaluated RcloneCostClass = "not_evaluated"
	RcloneCostNone         RcloneCostClass = "none"
	RcloneCostLow          RcloneCostClass = "low"
	RcloneCostModerate     RcloneCostClass = "moderate"
	RcloneCostHigh         RcloneCostClass = "high"
)

type RclonePublicationSummary struct {
	Mode                   TaskPublicationMode          `json:"mode"`
	State                  RcloneVersioningState        `json:"state"`
	ReasonCode             RcloneVersioningReasonCode   `json:"reason_code"`
	TaskRevision           string                       `json:"task_revision"`
	BindingRevision        string                       `json:"binding_revision"`
	CapabilityRevision     string                       `json:"capability_revision"`
	ConsistencyClass       RcloneConsistencyClass       `json:"consistency_class"`
	HashFidelity           RcloneHashFidelity           `json:"hash_fidelity"`
	EstimatedReadBytes     string                       `json:"estimated_read_bytes"`
	APICostClass           RcloneCostClass              `json:"api_cost_class"`
	StorageCostClass       RcloneCostClass              `json:"storage_cost_class"`
	EgressCostClass        RcloneCostClass              `json:"egress_cost_class"`
	CredentialExpiresAt    *time.Time                   `json:"credential_expires_at,omitempty"`
	EncryptionProfile      RcloneEncryptionProfile      `json:"encryption_profile"`
	KMSKeyStatus           RcloneKMSKeyStatus            `json:"kms_key_status"`
	KMSReadKeyCount        uint32                        `json:"kms_read_key_count"`
	RollbackLocatorPresent bool                          `json:"rollback_locator_present"`
	RollbackCapability     RcloneRollbackCapability     `json:"rollback_capability"`
}
```

The five aggregate classes above are closed API values, not provider names. `not_evaluated` is the only pre-preflight value; it never claims successful verification. Task, binding and capability revisions plus byte estimates use canonical unsigned decimal strings, including canonical `"0"` where the fact is not yet applicable. Unknown mode/state/reason/consistency/hash/cost/encryption/KMS wire values project the enclosing summary to `blocked + unsupported_profile`; unknown rollback projects to `preparation_only`. No unknown value may project to legacy, `none`, SSE-S3 or clean rollback.

### 1.2 Provider tagged contracts

Extend `backend/internal/backupasset/provider/contracts.go` without changing the existing Restic/Rsync wire shapes:

```go
type TaggedPublicationAttempt struct {
	Provider  backupasset.ProviderKind
	Version   int
	Restic    *ResticAttemptV1
	RsyncTree *RsyncTreeAttemptV1
	Rclone    *RcloneAttemptV1
}

type RcloneNativeProfileCode string
const RcloneNativeAWSS3GeneralPurposeV1 RcloneNativeProfileCode = "aws_s3_general_purpose_v1"

type RcloneNativeEncryptionProfileCode string
const (
	RcloneNativeSSES3V1  RcloneNativeEncryptionProfileCode = "sse_s3_v1"
	RcloneNativeSSEKMSV1 RcloneNativeEncryptionProfileCode = "sse_kms_cmk_v1"
)

type RcloneAttemptV1 struct {
	SchemaVersion             int
	LayoutVersion             int
	MinimumRuntimeRevision    int
	Provider                  backupasset.ProviderKind
	RepositoryID              string
	TaskRepositoryLinkID      string
	RecoveryPointID           string
	AttemptID                 string
	TaskID                    uint
	TaskRunID                 uint
	Trigger                    string
	PublicationMode           backupasset.TaskPublicationMode
	ImportedBaseline          bool
	CaptureStartedAt          time.Time
	PreparedAt                time.Time
	PointDeadlineAt           time.Time
	ExpectedTaskRevision      uint64
	BindingRevision           uint64
	ConfigRevision            uint64
	ConfigDigest              string
	CapabilityRevision        uint64
	CredentialRevision        uint64
	PreflightID               string
	PreflightRevision         uint64
	PreflightDigest           string
	ManifestSchemaRevision    uint64
	ManifestLimitsRevision    uint64
	ManifestLimitsDigest      string
	RepositoryIdentityDigest  string
	ManagedRootIdentityDigest string
	ChildFenceDigest          string
	LegacyOriginEvidenceDigest string
	Portable                  *RclonePortableAttemptV1
	Native                    *RcloneNativeAttemptV1
}

type RclonePortableAttemptV1 struct {
	AttemptComponent         string
	DataComponent            string
	ControlComponent         string
	AttemptMarkerDigest      string
	ExpectedConsistencyClass string
	ExpectedHashFidelity     string
	CopyDest                 *RclonePortableCopyDestV1
}

type RclonePortableCopyDestV1 struct {
	ParentRecoveryPointID    string
	ParentAttemptID          string
	ParentDataComponent      string
	ParentCommitDigest       string
	ParentManifestDigest     string
	ParentCapabilityRevision uint64
}

type RcloneNativeAttemptV1 struct {
	ProfileCode                  RcloneNativeProfileCode
	RegionIdentityDigest         string
	BucketIdentityDigest         string
	ManagedPrefixIdentityDigest  string
	RoleSessionIdentityDigest    string
	SessionExpiresAt             time.Time
	VersioningDigest             string
	LifecycleDigest              string
	CapabilityStableObservedAt   time.Time
	EncryptionProfile            RcloneNativeEncryptionProfileCode
	BucketEncryptionDigest       string
	ActiveKeyDigest              string
	RetainedReadKeySetDigest     string
	KMSCapabilityRevision        uint64
	B0VersionGraphDigest         string
	StartMarkerIdentityDigest    string
	CanaryIdentityDigest         string
}

type ProviderCommit struct {
	Provider  backupasset.ProviderKind
	Version   int
	Restic    *ResticCommitV1
	RsyncTree *RsyncTreeCommitV1
	Rclone    *RcloneCommitV1
}

type RcloneCommitV1 struct {
	SchemaVersion              int
	LayoutVersion              int
	MinimumRuntimeRevision     int
	RepositoryID               string
	TaskRepositoryLinkID       string
	RecoveryPointID            string
	AttemptID                  string
	PublicationMode            backupasset.TaskPublicationMode
	PointDeadlineAt            time.Time
	ProviderCommittedAt        time.Time
	ManifestIndexDigest        string
	ManifestChunkDigests       []string
	ManifestEntryCount         uint64
	LogicalBytes               uint64
	SourceObservationDigest    string
	DestinationObservationDigest string
	ContentProofDigest         string
	FidelityEvidenceDigest     string
	CostEvidenceDigest         string
	CapabilityEvidenceDigest   string
	ChildFenceDigest           string
	Portable                   *RclonePortableCommitV1
	Native                     *RcloneNativeCommitV1
}

type RclonePortableCommitV1 struct {
	AttemptIdentityDigest      string
	ControlIdentityDigest      string
	DataIdentityDigest         string
	AttemptMarkerDigest        string
	ParentRecoveryPointID      string
	ParentCommitDigest         string
	ParentManifestDigest       string
	CommitComponent            string
	CommitPayloadDigest        string
	CommitAuthenticationDigest string
	ConsistencyEvidenceDigest  string
	HashEvidenceDigest         string
	DownloadVerifiedBytes      uint64
}

type RcloneNativeCommitV1 struct {
	CommitKey                  string `json:"-"`
	CommitVersionID            string `json:"-"`
	CommitContentDigest        string
	ManifestControlGraphDigest string
	PointViewDigest            string
	MutationLedgerDigest       string
	B0VersionGraphDigest       string
	B1VersionGraphDigest       string
	ExactReadProofDigest       string
	VersioningDigest           string
	LifecycleDigest            string
	BucketEncryptionDigest     string
	EncryptionEvidenceDigest   string
	ActiveKeyDigest            string
	RetainedReadKeySetDigest   string
	RoleSessionIdentityDigest  string
	CapabilityRevision         uint64
	CredentialRevision         uint64
	KMSCapabilityRevision      uint64
	SessionExpiresAt           time.Time
}
```

All fields above receive explicit lower-snake-case tags in the private strict wire structs. `CommitKey` and `CommitVersionID`, like raw Remote locators, are encoded only inside the repository's encrypted locator/evidence record; generic JSON, API, logs, audit and metrics never marshal them. The portable/native pointers are a closed exactly-one union selected by `PublicationMode`; absent, duplicate, explicit-null, unknown, trailing or cross-variant data is rejected. `ManifestResult`, `PublicationPrepareRequest`, `PreparedPublication`, `PublicationReconcileRequest` and `PublicationReconcileResult` gain exactly one equally closed Rclone branch, while the existing Restic/Rsync wire bytes remain unchanged.

### 1.3 File ownership

| Area | Create | Modify |
| --- | --- | --- |
| Domain/settings | `backupasset/rclone_versioning.go`, tests; `task/rclone_versioning.go`, tests | `backupasset/publication.go`, `backupasset/service.go`, `settings/service.go` and tests |
| Command/staging | `provider/rclone_staged_payload.go`, `provider/rclone_staged_payload_test.go` | `provider/runner.go`, `runner_test.go`, `sshutil` only if an existing SFTP lifecycle primitive cannot satisfy typed cleanup |
| Portable | `provider/rclone_publication_contract.go`, `rclone_bound_config.go`, `rclone_manifest.go`, `rclone_portable.go` and tests/fixtures | `provider/contracts.go`, `registry.go`, `rclone.go`, boundary tests |
| Binding/workflow | `repository/rclone_binding.go`, `rclone_versioning.go` and tests | `repository/binding.go`, `managed_history.go`, service/lineage/query tests, paired `000064` down files and migration integration test |
| AWS native | `provider/rclone_native.go`, `rclone_native_aws_sdk.go`, tests and build-tagged live test | `backend/go.mod`, `backend/go.sum` |
| Publication runtime | `repository/rclone_publication_execution.go`, `rclone_publication_reconcile.go`, `rclone_health.go`; executor/runtime health files and tests | shared publication/reconcile/runtime/task factory/runner/guards |
| API | `handlers/task_rclone_versioning_handler.go` and test | task handler, router/RBAC/config import tests, generated `docs.go`, audit actions |
| Frontend | `components/task-rclone-versioning-dialog.tsx` and test | domain types, tasks API/tests, task page fragments/tests, i18n/a11y |
| Docs/gates | build-tagged conformance and temporary fixtures only | `docs/admin/backup-recovery.md`, `docs/env-vars.md`; specs only through Phase 3.3 `trellis-update-spec` |

Implementation order is fixed: domain/settings -> typed transport -> portable -> V3/safety -> AWS admission -> native graph -> coordinator/runtime -> workflow -> API -> frontend -> conformance/full delivery.

## 2. Task 1: Freeze Domain, Settings, And Tagged Rclone Types

**Files:**

- Create: `backend/internal/backupasset/rclone_versioning.go`
- Create: `backend/internal/backupasset/rclone_versioning_test.go`
- Create: `backend/internal/task/rclone_versioning.go`
- Create: `backend/internal/task/rclone_versioning_test.go`
- Create: `backend/internal/backupasset/provider/rclone_publication_contract.go`
- Create: `backend/internal/backupasset/provider/rclone_publication_contract_test.go`
- Modify: `backend/internal/backupasset/provider/contracts.go`
- Modify: `backend/internal/backupasset/provider/contracts_test.go`
- Modify: `backend/internal/backupasset/publication.go`
- Modify: `backend/internal/backupasset/publication_test.go`
- Modify: `backend/internal/backupasset/service.go`
- Modify: `backend/internal/settings/service.go`
- Modify: `backend/internal/settings/service_test.go`

- [ ] **Step 1: Add failing closed-codec and safe-summary tests.**

Cover empty/legacy Rclone config, old `{bandwidth_limit,transfers}` compatibility, exact V1 managed round trip, invalid bandwidth/transfer bounds, unknown/duplicate/null/trailing fields, generic create/update rejection of managed mode, all safe state/reason/profile/status/rollback enums and unknown-to-blocked projection. Add tagged attempt/commit/manifest/reconcile tests proving Restic/Rsync bytes remain compatible and every Rclone cross-variant is rejected.

- [ ] **Step 2: Run the red domain tests.**

```bash
go -C backend test ./internal/backupasset ./internal/task ./internal/backupasset/provider -run 'Rclone|Tagged|PublicationFailure' -count=1
```

Expected: failures identify missing Rclone config/summary types and missing tagged Rclone branch; existing Restic/Rsync cases remain green.

- [ ] **Step 3: Implement the locked types and internal failure mapping.**

Implement the section 1 ledgers with strict duplicate/null/trailing checks. Extend internal publication failures only with the Rclone facts required for durable reconciliation (`source_drift`, `external_writer_detected`, `unexpected_version`, `marker_mismatch`, `manifest_mismatch`); public API reasons remain the separate closed union. Never add `map[string]any`, `json.RawMessage`, arbitrary extension fields or SDK types to provider-neutral contracts.

- [ ] **Step 4: Register bounded dynamic settings.**

Add these exact registry keys and validate cross-field invariants in `ValidateBackupAssetEffectiveUpdate`:

```text
backup_assets.rclone_preflight_ttl                  30m       [16m,24h]
backup_assets.rclone_portable_deadline              24h       [5m,168h]
backup_assets.rclone_native_deadline                45m       [5m,55m]
backup_assets.rclone_bound_config_max_bytes         65536     [1024,65536]
backup_assets.rclone_control_payload_max_bytes      8388608   [65536,67108864]
backup_assets.rclone_full_verify_max_bytes          1099511627776 [1048576,17592186044416]
backup_assets.rclone_manifest_chunk_max_bytes       8388608   [65536,67108864]
backup_assets.rclone_low_level_retries              3         [1,10]
backup_assets.rclone_staging_orphan_age             24h       [1h,168h]
backup_assets.rclone_staging_scan_limit             256       [1,4096]
backup_assets.rclone_kms_read_key_max_count         8         [1,32]
backup_assets.rclone_health_interval                15m       [1m,24h]
backup_assets.rclone_health_batch_size              100       [1,1000]
backup_assets.rclone_aws_sdk_max_attempts            3         [1,10]
```

`rclone_preflight_ttl` must exceed the fixed 15-minute AWS settle observation plus join margin; native deadline must remain below the STS role-chain 3600-second ceiling with margin; config max must not exceed `sshutil` SecretStdin maximum; manifest chunk must not exceed staged payload max.

- [ ] **Step 5: Format and run focused tests.**

```bash
gofmt -w backend/internal/backupasset/rclone_versioning.go backend/internal/task/rclone_versioning.go backend/internal/backupasset/provider/rclone_publication_contract.go backend/internal/backupasset/provider/contracts.go backend/internal/backupasset/publication.go backend/internal/backupasset/service.go backend/internal/settings/service.go
go -C backend test ./internal/settings ./internal/backupasset ./internal/task ./internal/backupasset/provider -run 'Rclone|Tagged|PublicationFailure|BackupAsset.*Setting' -count=1
```

Expected: all focused tests pass and legacy Restic/Rsync tagged fixtures remain byte-compatible.

## 3. Task 2: Add Typed SFTP Staging And Strict Rclone Commands

**Files:**

- Create: `backend/internal/backupasset/provider/rclone_staged_payload.go`
- Create: `backend/internal/backupasset/provider/rclone_staged_payload_test.go`
- Modify: `backend/internal/backupasset/provider/runner.go`
- Modify: `backend/internal/backupasset/provider/runner_test.go`
- Modify: `backend/internal/backupasset/provider/rclone.go`
- Modify: `backend/internal/backupasset/provider/rclone_test.go`

- [ ] **Step 1: Write failing transport lifecycle and command allowlist tests.**

Use a fake SFTP backend plus a `net.Pipe`/`sftp.NewServer` integration fixture. Cover verified per-user home root, 0700 owner directory, opaque attempt directory, authenticated ownership marker, O_EXCL 0600 payload, byte/digest/stat verification, symlink/root replacement, duplicate name, partial write, close failure, cancel, active-handle protection, bounded aged-orphan scan and exact-owner cleanup. Add table tests for every approved Rclone operation and reject user flags, shell fragments, dynamic config source, `--retries` other than `1`, `--backup-dir`, `--dest-after`, `--copy-links`, `--skip-links`, destructive portable sync/delete/purge and raw staged paths in caller args.

- [ ] **Step 2: Run the command/staging tests red.**

```bash
go -C backend test ./internal/backupasset/provider -run 'Rclone.*(Staged|Command|Invocation|Transport)' -count=1
```

Expected: tests fail because staged payload references and publication operations are not registered.

- [ ] **Step 3: Implement a typed second input channel.**

Add these provider-owned shapes; `StagedPayloadRef` fields stay unexported so only a successful transport can construct one:

```go
type StagedPayloadRequest struct {
	AttemptID string
	Name      string
	Payload   []byte `json:"-"`
	MaxBytes  int64
}

type StagedPayloadTransport interface {
	Stage(context.Context, RemoteCommandAccess, StagedPayloadRequest) (StagedPayloadRef, error)
	Cleanup(context.Context, RemoteCommandAccess, StagedPayloadRef) error
	CleanupAged(context.Context, RemoteCommandAccess, time.Duration, int) error
}
```

Production staging dials the same node identity through `sshutil.NodeDialer`, creates an SFTP client, resolves the SSH user's home, and confines all paths below `.xirang/rclone-publication`. It never accepts a root/path from Task, API or binding. Cleanup removes only the exact attempt-owned files after marker verification; it never scans outside that root or recursively removes unknown entries.

- [ ] **Step 4: Extend exact command construction.**

Register separate operations for `version`, recursive `lsjson`, `copy`, native `sync`, `check --download`, `copyto`, `cat`, `backend features`, and exact stat. `CommandInvocation` carries typed private source/destination/staged references; `commandSpec` inserts them after validation and never logs them. Every managed command receives bound config through `--config /dev/stdin`, `--retries 1`, bounded `--low-level-retries`, an absolute context deadline, `--links`, and the operation-specific fixed flags. Portable data uses `copy`, not `sync`, so an unexpected object is preserved as evidence and caught by set verification. Native current-head mutation alone uses `sync` and records its delete-marker effects. The managed `version` probe accepts exactly v1.74.4; pristine legacy execution retains its existing compatibility behavior and does not inherit this new rejection.

- [ ] **Step 5: Run transport, security and race tests.**

```bash
gofmt -w backend/internal/backupasset/provider/rclone_staged_payload.go backend/internal/backupasset/provider/runner.go backend/internal/backupasset/provider/rclone.go
go -C backend test ./internal/backupasset/provider ./internal/sshutil -run 'Rclone|CommandRunner|UnsafeInvocation' -count=1
go -C backend test -race ./internal/backupasset/provider -run 'Rclone.*(Staged|Transport|Command)' -count=1
```

Expected: command snapshots and lifecycle tests pass with no path/config/payload disclosure and no race report.

## 4. Task 3: Implement Portable Unique-Prefix Publication

**Files:**

- Create: `backend/internal/backupasset/provider/rclone_manifest.go`
- Create: `backend/internal/backupasset/provider/rclone_manifest_test.go`
- Create: `backend/internal/backupasset/provider/rclone_bound_config.go`
- Create: `backend/internal/backupasset/provider/rclone_bound_config_test.go`
- Create: `backend/internal/backupasset/provider/rclone_portable.go`
- Create: `backend/internal/backupasset/provider/rclone_portable_test.go`
- Create: `backend/internal/backupasset/provider/testdata/rclone/v1.74.4-config-providers.json`
- Create: `backend/internal/backupasset/provider/testdata/rclone/bound-config/s3.conf`
- Create: `backend/internal/backupasset/provider/testdata/rclone/bound-config/azure-blob.conf`
- Create: `backend/internal/backupasset/provider/testdata/rclone/bound-config/gcs.conf`
- Create: `backend/internal/backupasset/provider/testdata/rclone/bound-config/b2.conf`
- Create: `backend/internal/backupasset/provider/testdata/rclone/bound-config/sftp.conf`
- Create: `backend/internal/backupasset/provider/testdata/rclone/bound-config/webdav.conf`
- Create: `backend/internal/backupasset/provider/testdata/rclone/bound-config/swift.conf`
- Create: `backend/internal/backupasset/provider/testdata/rclone/bound-config/ftp.conf`
- Create: `backend/internal/backupasset/provider/testdata/rclone/bound-config/crypt-closure.conf`
- Create: `backend/internal/backupasset/provider/testdata/rclone/lsjson-tree.json`
- Create: `backend/internal/backupasset/provider/testdata/rclone/lsjson-empty.json`
- Create: `backend/internal/backupasset/provider/testdata/rclone/lsjson-symlink.json`
- Create: `backend/internal/backupasset/provider/testdata/rclone/lsjson-weak-hash.json`
- Create: `backend/internal/backupasset/provider/testdata/rclone/lsjson-collision.json`
- Modify: `backend/internal/backupasset/provider/rclone_publication_contract.go`
- Modify: `backend/internal/backupasset/provider/rclone.go`
- Modify: `backend/internal/backupasset/provider/registry.go`
- Modify: `backend/internal/backupasset/provider/registry_test.go`
- Modify: `backend/internal/backupasset/provider/publication_boundary_test.go`

- [ ] **Step 1: Add the red v1.74.4 provider-schema and bound-config matrix.**

Check in the reviewed JSON emitted by exact Rclone v1.74.4 `config providers` and pin its SHA-256 in the test. A single exhaustive table must classify every schema backend name exactly once as `literal_self_contained`, `closure_wrapper`, or `unsupported` with a stable reason code. Set equality between the provider fixture and classification table is mandatory: duplicates, missing entries, extra entries, aliases without an explicit target, and any backend absent from the pinned fixture fail the test. Literal profiles accept only exact in-bundle values and reject environment/default chains, metadata/instance identity, credential/token commands, external helper/file/keyring, interactive login and identity-changing refresh. Closure profiles recursively resolve every referenced Remote, reject cycles and dangling/unused stanzas, and apply the same rules to every dependency. Unknown backend, option or dependency fails closed before a provider command.

The positive fixtures must cover S3, Azure Blob, Google Cloud Storage, B2, SFTP, WebDAV, Swift and FTP literal profiles plus a `crypt` dependency closure. Negative tables cover dynamic sources, duplicate section/key, extra stanza, cycle, ambiguous Remote, provider-schema drift and each stable unsupported reason. Fixture credentials and endpoints are syntactically valid fake values and never become live identities.

- [ ] **Step 2: Add red manifest/path/fidelity fixtures.**

Cover canonical ordering/chunking, empty source, manifest-only empty directories, `--create-empty-src-dirs` capable round trip, symlink target bytes, literal `.rclonelink` collision, hardlink/metadata fidelity, special-file rejection, invalid UTF-8, normalization/encoded-rune collision, oversized item/depth/record/spool/output and source mutation. Verify strong shared checksum success and weak/no/ambiguous hash full-byte source-destination comparison; metadata-only evidence must never produce a commit.

- [ ] **Step 3: Add red publication/crash/cost tests.**

Assert exact mutation order `attempt.json -> data -> manifest chunks -> manifest-index.json -> commit.json`, commit-last readback, CSPRNG attempt IDs, visible collision quarantine, eventual absence not labeled storage no-clobber, D1/D2 stable full listings, source S0/S1 stability, fresh outer retry attempt, `--copy-dest` exact-parent lease eligibility and manual-copy fallback. Cover partial data/manifest, marker outcome unknown, marker/DB crash, lease loss, takeover and deadline; production never deletes an orphan prefix.

- [ ] **Step 4: Run portable tests red.**

```bash
go -C backend test ./internal/backupasset/provider -run 'Rclone.*(BoundConfig|ProviderSchema|Manifest|Portable|Path|Fidelity|Retry|Reconcile)' -count=1
```

Expected: failures identify the missing exhaustive config classifier, portable strategy and canonical marker implementation.

- [ ] **Step 5: Implement strict bound-config closure validation.**

Use a bounded strict parser for the Rclone INI grammar so duplicate sections/keys and trailing malformed data cannot be normalized away. Bind the exact bytes, target Remote, complete dependency closure, backend classification revision and keyed digest. The checked-in v1.74.4 provider fixture is the admission source of truth; runtime schema discovery cannot widen it. Every command uses those exact bytes through secret stdin, a sanitized environment and the pinned target Remote. Rotation creates a new revision and forces preflight; it never mutates an in-flight attempt.

- [ ] **Step 6: Implement canonical manifest and marker-last publisher.**

Use streaming JSON array decode, bounded spool/external sort, checked counts and SHA-256 canonical framing. Generate at least 128 bits of CSPRNG attempt entropy and persist it before Remote mutation. Upload control objects only through typed staged `copyto`; read each back fully. For weak/no-hash use exact `rclone check --download` and record verified bytes. `copy-dest` is cost-only and holds an exact parent `point_publication` read lease; any high-level retry allocates a new attempt. The final `commit.json` is authenticated, minimum-runtime/versioned, and the last Remote mutation.

- [ ] **Step 7: Add exact portable point reopening.**

Extend the Rclone adapter with a managed committed-prefix lane that accepts only a committed point's encrypted V3 locator and exact attempt marker/manifest. Legacy `mutable_head` retains the existing task-scoped reader. Managed readers never accept current/latest/root fallback, a V1 node-default binding or a preparing/verifying point.

- [ ] **Step 8: Run focused and actual-Rclone conformance.**

```bash
gofmt -w backend/internal/backupasset/provider/rclone_bound_config.go backend/internal/backupasset/provider/rclone_manifest.go backend/internal/backupasset/provider/rclone_portable.go backend/internal/backupasset/provider/rclone.go
go -C backend test ./internal/backupasset/provider -run 'Rclone.*(BoundConfig|ProviderSchema|Manifest|Portable|Path|Fidelity|Retry|Reconcile|Reader)' -count=1
RCLONE_TEST_BINARY=/absolute/path/to/rclone-v1.74.4 go -C backend test ./internal/backupasset/provider -run 'RcloneV1744(ProviderSchema|BoundConfig|Portable)Conformance' -count=1 -v
```

Expected: focused tests pass; the conformance test proves the checked-in provider schema exactly matches the pinned binary and verifies bound configs, local-backend bytes, symlinks, empty directories and command flags. A missing/wrong binary may skip ordinary development tests but is not acceptable at the final gate.

## 5. Task 4: Add V3 Binding, Managed-History Guard, And Rollback Floors

**Files:**

- Create: `backend/internal/backupasset/repository/rclone_binding.go`
- Create: `backend/internal/backupasset/repository/rclone_binding_test.go`
- Modify: `backend/internal/backupasset/repository/binding.go`
- Modify: `backend/internal/backupasset/repository/binding_test.go`
- Modify: `backend/internal/backupasset/repository/managed_history.go`
- Modify: `backend/internal/backupasset/repository/managed_history_test.go`
- Modify: `backend/internal/backupasset/repository/lineage_guard.go`
- Modify: `backend/internal/backupasset/repository/lineage_guard_test.go`
- Modify: `backend/internal/database/migrations/sqlite/000064_backup_asset_rsync_publication_contract.down.sql`
- Modify: `backend/internal/database/migrations/postgres/000064_backup_asset_rsync_publication_contract.down.sql`
- Modify: `backend/internal/database/backup_asset_migrations_integration_test.go`

- [ ] **Step 1: Add red V1/V2/V3 closed-union tests.**

Prove V1 legacy Rclone node-default and V2 managed Rsync decode unchanged. Cover V3 portable exact config bytes/dependency closure/revision/fingerprint, V3 native bootstrap/encryption cross-fields, unknown/duplicate/null/trailing fields, wrong task/node/repository/link/mode, config/native field mixing, dynamic credential source, secret redaction and old-reader V3 rejection.

- [ ] **Step 2: Define encrypted V3 as one strict portable/native document.**

`managedRcloneBindingDocumentV3` stores exact repository/link/task/node identities, publication/layout/minimum-runtime revisions, binding/config/credential/capability revisions, encrypted legacy V1 and Task policy snapshots, rollback facts, preflight evidence and a closed variant. Portable stores exact managed Remote/root and self-contained config bytes. Native stores official region/bucket/prefix, role/external ID, workload/static-STS bootstrap union, selected `sse_s3_v1 | sse_kms_cmk_v1`, active full KMS key ARN and bounded retained read-key ring. The model hook encrypts the whole document; raw values never enter DTO/log/audit.

- [ ] **Step 3: Add clean/preparation rollback guard tests.**

Test zero-reservation clean rollback, concurrent prepare conflict, any-state Rclone point blocking clean rollback, failed/no-commit point preserving evidence, imported-baseline activation consuming the window, rollback preparation restoring only the encrypted legacy locator while Task remains paused, and older-runtime backstop via empty `Task.RsyncTarget` plus minimum-runtime marker.

- [ ] **Step 4: Repair only the existing 000064 down guard.**

Add `versioned_prefix` and `native_object_versions` to both active-link guard lists before any DDL. Extend migration integration tests to prove either Rclone link blocks down and leaves schema/data unchanged, while clean rollback that removed the only link and has zero managed points/latches/leases still permits down. Do not edit either `000064` up file and do not create `000065`.

- [ ] **Step 5: Extend managed-history resolution.**

Query active managed Rclone links in repository and installation history. Keep `xirang_manifest/imported_baseline` any-state point semantics as permanent blockers after first reservation. Ensure legacy fallback accepts only exact pristine V1 + mutable link + mutable repository; `node_default` cannot become V3 or a managed fallback.

- [ ] **Step 6: Run binding and dual-engine migration tests.**

```bash
gofmt -w backend/internal/backupasset/repository/rclone_binding.go backend/internal/backupasset/repository/binding.go backend/internal/backupasset/repository/managed_history.go backend/internal/backupasset/repository/lineage_guard.go
go -C backend test ./internal/backupasset/repository ./internal/database -run 'Rclone.*(Binding|History|Rollback)|Migration064' -count=1
```

Expected: SQLite passes; PostgreSQL runs when `TEST_POSTGRES_DSN` is configured and otherwise explicitly skips only that engine; no migration file above `000064` appears.

## 6. Task 5: Implement AWS STS, S3/KMS Admission, And SDK Boundary

**Files:**

- Create: `backend/internal/backupasset/provider/rclone_native.go`
- Create: `backend/internal/backupasset/provider/rclone_native_test.go`
- Create: `backend/internal/backupasset/provider/rclone_native_aws_sdk.go`
- Create: `backend/internal/backupasset/provider/rclone_native_aws_sdk_test.go`
- Create: `backend/internal/backupasset/provider/testdata/rclone/aws/list-object-versions-page-1.xml`
- Create: `backend/internal/backupasset/provider/testdata/rclone/aws/list-object-versions-page-2.xml`
- Create: `backend/internal/backupasset/provider/testdata/rclone/aws/get-bucket-encryption.xml`
- Create: `backend/internal/backupasset/provider/testdata/rclone/aws/get-bucket-lifecycle.xml`
- Modify: `backend/go.mod`
- Modify: `backend/go.sum`

- [ ] **Step 1: Add red narrow-interface and admission tests.**

Define provider-owned `STSAssumer`, `BootstrapDenyProbe`, `S3Native`, `KMSKeyInspector` and client-factory interfaces using only internal structs. Cover official regional endpoint/DNS addressing/general-purpose bucket, custom/path-style/directory/access-point rejection, correct/missing/fresh-wrong external ID, base-principal negative probe, same-account owner, `s3:ResourceAccount`, `ExpectedBucketOwner`, role-chain 3600-second limit, session expiry margin, refresh failure and no direct-static S3 factory.

- [ ] **Step 2: Add encryption/lifecycle red tests.**

Cover `GetBucketEncryption` AES256/aws:kms/aws:kms:dsse, `BlockedEncryptionTypes`, bucket-key absent/false/true, explicit Rclone headers, exact-version Head evidence, customer/symmetric/encrypt-decrypt/enabled/AWS_KMS/same-account-region `DescribeKey`, unsupported AWS-managed/alias/cross-account/custom/external/SSE-C/DSSE/unknown, active GenerateDataKey+Decrypt, retained decrypt-only, `kms:ViaService`, bucket-key-specific encryption context and 2048-character/PackedPolicySize ring limits. Cover versioning enabled and fixed 15-minute digest stability; lifecycle canonical filters/actions, destructive/delete-marker/offline transition rejection and allowed STANDARD_IA/ONEZONE_IA/GLACIER_IR.

- [ ] **Step 3: Add the pinned official SDK modules.**

```bash
go -C backend get github.com/aws/aws-sdk-go-v2@v1.42.1
go -C backend get github.com/aws/aws-sdk-go-v2/config@v1.32.30
go -C backend get github.com/aws/aws-sdk-go-v2/credentials@v1.19.29
go -C backend get github.com/aws/aws-sdk-go-v2/service/sts@v1.44.1
go -C backend get github.com/aws/aws-sdk-go-v2/service/s3@v1.105.1
go -C backend get github.com/aws/aws-sdk-go-v2/service/kms@v1.54.1
```

Expected: only required AWS v2 modules and their transitives are added; Go version remains compatible with repository Go 1.26.

- [ ] **Step 4: Implement the production SDK adapters.**

Load bootstrap only from SDK workload/default chain or encrypted static key. Bootstrap creates STS plus the isolated deny-probe client only; every functional S3/KMS call uses one assumed-role temporary session frozen for the attempt. Generate a bounded exact-resource inline session policy, reject overflow/PackedPolicySize, set official endpoint/region and `ExpectedBucketOwner`, cap SDK retries, close every body and expose only sanitized typed causes. Generate the node Rclone S3 config from the same temporary credentials and explicit encryption profile; never persist session credentials.

- [ ] **Step 5: Run protocol, dependency and security tests.**

```bash
gofmt -w backend/internal/backupasset/provider/rclone_native.go backend/internal/backupasset/provider/rclone_native_aws_sdk.go
go -C backend test ./internal/backupasset/provider -run 'RcloneNative.*(STS|Admission|Lifecycle|Encryption|KMS|SDK|Policy)' -count=1
go -C backend mod tidy
go -C backend list -m all
(cd backend && govulncheck ./internal/backupasset/provider/... ./internal/backupasset/repository/...)
```

Expected: protocol fixtures pass, no SDK type crosses provider package APIs, and vulnerability scan reports no reachable known vulnerability.

## 7. Task 6: Implement Native Version Graph, Exact Proof, And Health

**Files:**

- Create: `backend/internal/backupasset/provider/rclone_native_versions.go`
- Create: `backend/internal/backupasset/provider/rclone_native_versions_test.go`
- Create: `backend/internal/backupasset/provider/rclone_path_codec.go`
- Create: `backend/internal/backupasset/provider/rclone_path_codec_test.go`
- Create: `backend/internal/backupasset/provider/rclone_native_aws_live_test.go`
- Modify: `backend/internal/backupasset/provider/rclone_publication_contract.go`
- Modify: `backend/internal/backupasset/provider/rclone_native.go`
- Modify: `backend/internal/backupasset/provider/publication_boundary_test.go`

- [ ] **Step 1: Add red path-codec and version-graph fixtures.**

Implement parity tests for Rclone v1.74.4 `InvalidUtf8|Slash|Dot` encoding with ordinary, Unicode, dot, slash and encoded-rune cases; reject invalid UTF-8, non-bijective decode and collisions. The production codec is the smallest fixed implementation derived from the Rclone v1.74.4 MIT-licensed encoder, with upstream version, commit, source path and attribution retained in its file header; no broader Rclone source is copied. Golden cases plus the real pinned v1.74.4 binary must prove encode/decode parity. Add double-full-list B0/B1 fixtures with both KeyMarker and VersionIdMarker, interleaved versions/delete markers, zero-byte objects, unchanged/new/overwrite/delete/multiple-rewrite, cross-page drift, B0 permanent removal and unknown external version.

- [ ] **Step 2: Add red exact-content and control-commit tests.**

Require every live entry to bind exact VersionId, size, content proof, actual encryption and KMS key digest. Cover current/latest/synthetic-name rejection, marker 404/405 semantics, exact full/range reads, short read, decrypt/key mismatch, source drift and weak-hash full download. Commit control objects must have their own exact VersionIds; multiple same-key commit versions or deadline/fence ambiguity quarantines.

- [ ] **Step 3: Run native graph tests red.**

```bash
go -C backend test ./internal/backupasset/provider -run 'RcloneNative.*(Codec|Version|Graph|Marker|Exact|Ledger)' -count=1
```

Expected: failures identify the missing graph/ledger implementation rather than falling back to current object state.

- [ ] **Step 4: Implement B0/B1 capture and authenticated commit.**

Take two complete identical B0 enumerations before mutation, write an attempt start marker, run native `sync`, join all handles, write an end marker, then take two complete identical B1 enumerations. Build a tagged `object_version | delete_marker` graph and a complete attempt mutation ledger; every B1-B0 mutation must be explained by the attempt and every missing B0 version quarantines. Use only the attributed, revision-pinned codec proven against the actual v1.74.4 binary for logical/physical mapping. Upload manifest/control and commit through explicit SSE profile, capture exact control VersionIds, then exact-read all evidence before returning `RcloneCommitV1`.

- [ ] **Step 5: Implement exact native reader and key health facts.**

Native readers accept only committed manifests and exact VersionId. Periodic health re-runs versioning/lifecycle/encryption/retained-key checks, probes at least one referenced version per key and classifies temporary KMS/provider outage as availability while disabled/pending-deletion/missing/permission/decrypt mismatch marks related points at-risk. It never rewrites historical key digests or falls back to current/SSE-S3/portable.

- [ ] **Step 6: Run native focused and codec conformance tests.**

```bash
gofmt -w backend/internal/backupasset/provider/rclone_native_versions.go backend/internal/backupasset/provider/rclone_path_codec.go backend/internal/backupasset/provider/rclone_native.go
go -C backend test ./internal/backupasset/provider -run 'RcloneNative|RcloneV1744PathCodec' -count=1
go -C backend test -race ./internal/backupasset/provider -run 'RcloneNative.*(Graph|Exact|Health)' -count=1
RCLONE_TEST_BINARY=/absolute/path/to/rclone-v1.74.4 go -C backend test ./internal/backupasset/provider -run 'RcloneV1744PathCodecConformance' -count=1 -v
```

Expected: all protocol/codec tests pass with no race and no production `DeleteObjectVersion` call.

## 8. Task 7: Integrate Repository, Task Execution, Reconciliation, And Runtime Health

**Files:**

- Create: `backend/internal/backupasset/repository/rclone_publication_execution.go`
- Create: `backend/internal/backupasset/repository/rclone_publication_execution_test.go`
- Create: `backend/internal/backupasset/repository/rclone_publication_reconcile.go`
- Create: `backend/internal/backupasset/repository/rclone_publication_reconcile_test.go`
- Create: `backend/internal/backupasset/repository/rclone_health.go`
- Create: `backend/internal/backupasset/repository/rclone_health_test.go`
- Create: `backend/internal/task/executor/rclone_publication_executor.go`
- Create: `backend/internal/task/executor/rclone_publication_executor_test.go`
- Create: `backend/internal/backupasset/runtime/rclone_health_worker.go`
- Create: `backend/internal/backupasset/runtime/rclone_health_worker_test.go`
- Modify: `backend/internal/backupasset/repository/publication_execution.go`
- Modify: `backend/internal/backupasset/repository/publication_reconcile.go`
- Modify: `backend/internal/backupasset/runtime/runtime.go`
- Modify: `backend/internal/backupasset/runtime/runtime_test.go`
- Modify: `backend/internal/backupasset/runtime/publication_worker.go`
- Modify: `backend/internal/backupasset/runtime/publication_worker_test.go`
- Modify: `backend/internal/task/executor/evidence.go`
- Modify: `backend/internal/task/executor/executor.go`
- Modify: `backend/internal/task/executor/evidence_test.go`
- Modify: `backend/internal/task/publication_runner.go`
- Modify: `backend/internal/task/publication_runner_test.go`
- Modify: `backend/internal/task/publication_interrupted.go`
- Modify: `backend/internal/task/publication_interrupted_test.go`
- Modify: `backend/internal/task/verifier/verifier.go`
- Modify: `backend/internal/task/verifier/verifier_test.go`
- Modify: `backend/internal/task/retention.go`
- Modify: `backend/internal/task/retention_test.go`
- Modify: `backend/internal/task/integrity_checker.go`
- Create: `backend/internal/task/integrity_checker_test.go`
- Modify: `backend/internal/task/manager.go`
- Modify: `backend/internal/task/manager_test.go`
- Modify: `backend/cmd/server/main.go`

- [ ] **Step 1: Add red three-fact, lease and single-writer tests.**

Cover transfer success while DB preparing, provider commit to verifying, worker verifying to committed, transfer success plus publication failure, known nonzero transfer, unknown outcome, retry with same point/new attempt and TaskRun truth never rewritten. Portable parent optimization holds an owner-isolated read lease. Native prepare row-locks Repository/link and rejects unresolved same-physical-identity writers. All effective deadlines are immutable minima; renewal cannot extend them.

- [ ] **Step 2: Add red crash/restart/shutdown tests.**

Cover no marker, partial prefix, exact marker with DB preparing, multiple attempts/commit versions, stale fence, takeover/deadline marker, DB verifying missing evidence, committed provider offline/permanently missing, interrupted TaskRun and startup ordering. Cancellation must stop new calls, cancel contexts, drain streams/bodies/spool/heartbeat, join, re-lock/fence, persist typed outcome and only then release leases.

- [ ] **Step 3: Implement Rclone publication sessions.**

Create/retrieve the stable RecoveryPoint and fresh attempt in a fenced transaction, persist strict tagged evidence in encrypted locator, acquire child and optional parent leases, and expose a process-local `RclonePublicationInput` from the validated V3 binding. `RclonePublicationExecutor` derives only Task source, delegates managed work to the strategy and keeps legacy Run/RunRestore exact. Extend factory and runtime composition with a third strategy; Rclone enters publication runner only for managed mode.

- [ ] **Step 4: Implement commit, reconciliation and worker dispatch.**

Record only matching point/attempt/link/binding/capability/fence/deadline evidence, write durable latches at first exact provider commit, move to verifying and wake the shared worker. Reconciliation reopens only the persisted exact attempt/VersionId; it never scans newest prefixes or current objects. Generalize candidate dispatch and interrupted-run reporting without changing Restic/Rsync behavior.

- [ ] **Step 5: Add native health worker and startup gate.**

Run one bounded Rclone native health pass after publication reconciliation and before schedules open; unresolved or at-risk binding facts block only affected native writers. Periodically scan a bounded committed native batch using dynamic health settings. Shutdown stops admission, joins publication and health workers, then stops the shared barrier.

- [ ] **Step 6: Block legacy verifier/retention/integrity lanes.**

Modify `backend/internal/task/verifier/verifier.go`, `backend/internal/task/retention.go`, `backend/internal/task/integrity_checker.go` and tests so active managed Rclone link/V3 binding/any-state managed point routes through the lineage guard and never reads empty/legacy `Task.RsyncTarget`. Pristine legacy Rclone behavior remains unchanged.

- [ ] **Step 7: Run focused and race suites.**

```bash
gofmt -w backend/internal/backupasset/repository/rclone_publication_execution.go backend/internal/backupasset/repository/rclone_publication_reconcile.go backend/internal/backupasset/repository/rclone_health.go backend/internal/task/executor/rclone_publication_executor.go backend/internal/backupasset/runtime/rclone_health_worker.go
go -C backend test ./internal/backupasset/repository ./internal/backupasset/runtime ./internal/task/executor ./internal/task -run 'Rclone.*(Publication|Reconcile|Health|Managed|Interrupted)|PublicationRunner' -count=1
go -C backend test -race ./internal/backupasset/repository ./internal/backupasset/runtime ./internal/task -run 'Rclone|Publication|Admission|Interrupted' -count=1
```

Expected: all providers remain green; no race, late commit, mutable fallback or TaskRun/publication conflation occurs.

## 9. Task 8: Implement Setup, Preflight, Activation, Baseline, And Rollback Workflow

**Files:**

- Create: `backend/internal/backupasset/repository/rclone_versioning.go`
- Create: `backend/internal/backupasset/repository/rclone_versioning_test.go`
- Modify: `backend/internal/backupasset/repository/service.go`
- Modify: `backend/internal/task/rclone_versioning.go`
- Modify: `backend/internal/task/rclone_versioning_test.go`
- Modify: `backend/internal/task/service.go`
- Modify: `backend/internal/task/manager.go`
- Modify: `backend/internal/task/manager_test.go`

- [ ] **Step 1: Add red setup-store and secret-boundary tests.**

Cover short-lived task/revision-bound setup IDs, one-time consumption, stale/duplicate use, portable exact config bytes, dependency closure and dynamic source rejection; native external ID creation, workload/static union, static key cross-fields, write-only KMS full ARN and no response/log/audit echo. Rotation must pause, close admission, drain commands/reads/leases, CAS expected binding revision and force preflight.

- [ ] **Step 2: Add red preflight and activation tests.**

Portable preflight first proves the managed node binary is exactly Rclone v1.74.4, then uses the exact bound config to prove list/read/write/check/copy/cost without user data. Native uses that same exact node version for its Rclone data plane and executes all STS/S3/KMS admission and settle gates. Any managed version mismatch returns `unsupported_profile` before mutation; pristine legacy Rclone continues its existing compatibility path. Activation requires exact Task/binding/capability revisions, non-overlapping legacy/managed locators and fresh ownership; it installs V3/link/mode and atomically clears `Task.RsyncTarget`. Generic create/update/import cannot set managed mode or restore target.

- [ ] **Step 3: Add red baseline/rollback tests.**

`first_new_point` creates no point during activation and keeps clean rollback until first prepare. `imported_baseline` activation transaction atomically creates the migration TaskRun and preparing point/attempt reservation; failure rolls back the whole activation and Provider mutation starts only after commit. Test portable physical copy, native source encryption inventory/destination re-encryption, failure pause, zero-reservation clean rollback, first-reservation race and evidence-preserving preparation.

- [ ] **Step 4: Implement bounded in-memory setup/preflight stores and service methods.**

Expose typed repository methods for portable/native setup creation and binding consumption, preflight, activation, clean rollback, rollback preparation and safe summary. Stores are process-local, expiring and keyed by opaque CSPRNG IDs; only encrypted V3 holds durable secrets. Audit actions use safe mode/result/revision/correlation values only.

- [ ] **Step 5: Implement strict Task config and old-runtime backstop.**

Parse empty/old Rclone config as legacy; managed V1 is activation-only and preserves bandwidth/transfers. `RcloneExecutor` rejects a managed mode explicitly before command construction. Activation clears target; generic Task update/import cannot repopulate it; rollback preparation may reconnect the encrypted legacy locator only while Task remains paused and the V3/link blocker remains active.

- [ ] **Step 6: Run service/workflow tests.**

```bash
gofmt -w backend/internal/backupasset/repository/rclone_versioning.go backend/internal/task/rclone_versioning.go backend/internal/task/service.go
go -C backend test ./internal/backupasset/repository ./internal/task -run 'RcloneVersioning|Rclone.*(Setup|Preflight|Activation|Baseline|Rollback|Config|Summary)' -count=1
```

Expected: all workflows pass; no raw Remote/config/AWS identity/secret appears in safe results.

## 10. Task 9: Add Authenticated API, Safe Task Projection, And Swagger

**Files:**

- Create: `backend/internal/api/handlers/task_rclone_versioning_handler.go`
- Create: `backend/internal/api/handlers/task_rclone_versioning_handler_test.go`
- Modify: `backend/internal/api/handlers/task_handler.go`
- Modify: `backend/internal/api/handlers/task_handler_test.go`
- Modify: `backend/internal/api/handlers/config_handler.go`
- Modify: `backend/internal/api/handlers/config_handler_test.go`
- Modify: `backend/internal/api/router.go`
- Modify: `backend/internal/api/router_test.go`
- Modify: `backend/internal/api/backup_asset_rbac_test.go`
- Modify: `backend/internal/backupasset/audit_action.go`
- Modify: `backend/internal/backupasset/audit_action_test.go`
- Modify: `backend/internal/api/docs/docs.go` through `make swag-init`

- [ ] **Step 1: Add red handler and full-router tests for all eight endpoints.**

```text
POST /api/v1/tasks/:id/rclone-versioning/portable-binding-setups
PUT  /api/v1/tasks/:id/rclone-versioning/portable-binding
POST /api/v1/tasks/:id/rclone-versioning/native-binding-setups
PUT  /api/v1/tasks/:id/rclone-versioning/native-binding
POST /api/v1/tasks/:id/rclone-versioning/preflights
POST /api/v1/tasks/:id/rclone-versioning/activate
POST /api/v1/tasks/:id/rclone-versioning/clean-rollbacks
POST /api/v1/tasks/:id/rclone-versioning/rollback-preparations
```

Test no auth, Viewer/Operator, missing/unknown role, Admin success, ownership/no-existence leak, feature disabled, bad ID, duplicate/null/unknown/trailing fields, oversized config, stale revisions/setup/preflight, unsupported profile and deterministic 400/403/404/409/501/503 mapping.

- [ ] **Step 2: Implement narrow handler/service interfaces.**

Use response helpers and the existing authenticated `/api/v1` group with Admin plus `PermissionBackupRepositoriesManage` and Task ownership. Request DTOs are closed; portable config and AWS secrets appear only in PUT request memory. External ID appears only in native setup response; ordinary Task responses expose `rclone_publication` safe summary. Unexpected errors use generic 500 and never `err.Error()`.

- [ ] **Step 3: Make config import and ordinary Task APIs fail closed.**

Foreign managed Rclone config imports paused/disconnected with no V3 secret, target or preflight and requires a fresh local setup. Export never includes V3 or setup/preflight data. Ordinary create/update cannot choose managed mode, mutate binding, change KMS key or restore a managed target.

- [ ] **Step 4: Register sanitized audit actions and regenerate docs.**

Register setup/set/rotate, preflight, activate, clean-rollback and rollback-preparation actions with stable safe reasons. Run Swagger generation from annotations; generated docs must contain safe request/response shapes but no binding document, static secret, raw ARN/bucket/prefix/Remote or VersionId.

- [ ] **Step 5: Run API/RBAC/redaction suites.**

```bash
make swag-init
gofmt -w backend/internal/api/handlers/task_rclone_versioning_handler.go backend/internal/api/handlers/task_handler.go backend/internal/api/router.go backend/internal/backupasset/audit_action.go
go -C backend test ./internal/api/handlers ./internal/api ./internal/backupasset -run 'RcloneVersioning|RclonePublication|BackupAsset.*RBAC|AuditAction|Config.*Managed' -count=1
```

Expected: every route is authenticated, Admin/ownership-gated and redacted; Swagger tests remain fresh.

## 11. Task 10: Add Typed Frontend Mapping And Rclone Versioning Dialog

**Files:**

- Create: `web/src/components/task-rclone-versioning-dialog.tsx`
- Create: `web/src/components/task-rclone-versioning-dialog.test.tsx`
- Modify: `web/src/types/domain.ts`
- Modify: `web/src/lib/api/tasks-api.ts`
- Modify: `web/src/lib/api/tasks-api.test.ts`
- Modify: `web/src/components/task-create-dialog.basics.tsx`
- Modify: `web/src/components/task-create-dialog.test.tsx`
- Modify: `web/src/pages/tasks-page.utils.ts`
- Modify: `web/src/pages/tasks-page.grid.tsx`
- Modify: `web/src/pages/tasks-page.table.tsx`
- Modify: `web/src/pages/tasks-page.dialogs.tsx`
- Modify: `web/src/pages/tasks-page.tsx`
- Modify: `web/src/pages/tasks-page.test.tsx`
- Modify: `web/src/pages/__tests__/tasks-page.a11y.test.tsx`
- Modify: `web/src/i18n/locales/zh.ts`
- Modify: `web/src/i18n/locales/en.ts`

- [ ] **Step 1: Add red raw-mapper and API wrapper tests.**

Define exact camelCase unions matching section 1.1. Test snake_case mapping, decimal strings, invalid arrays/timestamps/numbers, internal `_v1` profile never leaking, unknown raw mode/state/reason/encryption/KMS status -> `blocked + unsupported_profile`, unknown rollback -> `preparation_only`, all eight request serializers and API error projection. Assert JSON-stringified domain data contains no config, Remote, bucket/prefix, ARN, external ID, VersionId or digest.

- [ ] **Step 2: Implement private raw types and central-client methods.**

Keep raw request/response types private to `tasks-api.ts`; export only domain unions and typed methods. Components never read `executorConfig` or call `fetch`. Secret form values stay in component-local state, never URL/storage/shared draft, and are cleared after success or failure.

- [ ] **Step 3: Add the Admin Rclone versioning dialog.**

Use existing Dialog, Tabs/segmented control, Input, Select, Badge, Alert and Button primitives. Portable is recommended. Implement portable/native write-only setup, preflight settle/expiry, cost/fidelity/session/lifecycle/encryption/KMS/rollback status, first-new versus baseline confirmation, activation, clean rollback and preparation. Disable activation on missing setup, settling, expiry or revision drift. Never show a node-default option, risk override, KMS key ARN or provider raw error.

- [ ] **Step 4: Wire task page and legacy warnings.**

Show a manage action only for Admin Rclone tasks with a safe summary; keep Rsync dialog/types separate. Task editor displays managed mode/state read-only and prevents ordinary target/config editing. Transfer and publication statuses remain separate. Add compact badges rather than nested cards and preserve current table/grid responsive dimensions.

- [ ] **Step 5: Add i18n, keyboard and accessibility coverage.**

Maintain zh/en key parity for every state/reason/action. Test DialogTitle, labels, description, focus-visible, segmented keyboard semantics, secret clearing, portaled dialog `runAxe`, no overlap at narrow task-page viewport and no untranslated fallback key.

- [ ] **Step 6: Run the full frontend gate.**

```bash
npm --prefix web run check
```

Expected: typecheck, lint, Vitest coverage and Vite build pass with no direct fetch, raw snake_case component access, `any`, unsafe cast or accessibility regression.

## 12. Task 11: Live Conformance, Cross-Layer Gates, Docs, And Delivery

**Files:**

- Modify: `docs/admin/backup-recovery.md`
- Modify: `docs/env-vars.md`
- Inspect/update through `trellis-update-spec`: relevant `.trellis/spec/backend/`, `.trellis/spec/frontend/`, `.trellis/spec/guides/`
- Verify: `.github/workflows/ci.yml`, `scripts/check-doc-freshness.sh`, `scripts/check-migration-utc-safety.sh`

- [ ] **Step 1: Run focused cross-layer and dual-database suites.**

```bash
go -C backend test ./internal/backupasset/... ./internal/task/... ./internal/api/... ./internal/database ./internal/settings -count=1
go -C backend test -race ./internal/backupasset/... ./internal/task/... -count=1
TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" go -C backend test ./internal/database ./internal/backupasset/repository -run 'Migration064|Rclone' -count=1
npm --prefix web run check
```

Expected: all suites pass; required PostgreSQL run is not replaced by a skip in the completion record.

- [ ] **Step 2: Run pinned real-Rclone portable conformance.**

```bash
RCLONE_TEST_BINARY=/absolute/path/to/rclone-v1.74.4 go -C backend test ./internal/backupasset/provider -run 'RcloneV1744(ProviderSchema|BoundConfig|Portable|PathCodec)Conformance' -count=1 -v
```

Expected: the exact v1.74.4 binary passes local-backend command, symlink, empty-directory, weak-hash full-byte, marker-last and path-codec cases. Record its version and checksum; never substitute an unpinned binary.

- [ ] **Step 3: Retain and review official AWS live conformance for both encryption subprofiles.**

Use a preconfigured dedicated official general-purpose S3 bucket with versioning enabled for more than 15 minutes, no matching destructive/offline lifecycle, a same-account AssumeRole trust requiring the configured external ID, and two pre-created customer-managed keys. Key A is the initial key retained for decrypt-only reads; key B is the active key after rotation. The bootstrap identity and separately authorized cleanup-admin profile must be distinct. Do not create/modify/disable/schedule-delete keys or lifecycle in the test.

```bash
XIRANG_AWS_LIVE_REGION="$XIRANG_AWS_LIVE_REGION" \
XIRANG_AWS_LIVE_BUCKET="$XIRANG_AWS_LIVE_BUCKET" \
XIRANG_AWS_LIVE_PREFIX="$XIRANG_AWS_LIVE_PREFIX" \
XIRANG_AWS_LIVE_ROLE_ARN="$XIRANG_AWS_LIVE_ROLE_ARN" \
XIRANG_AWS_LIVE_EXTERNAL_ID="$XIRANG_AWS_LIVE_EXTERNAL_ID" \
XIRANG_AWS_LIVE_KMS_KEY_ARN_A="$XIRANG_AWS_LIVE_KMS_KEY_ARN_A" \
XIRANG_AWS_LIVE_KMS_KEY_ARN_B="$XIRANG_AWS_LIVE_KMS_KEY_ARN_B" \
XIRANG_AWS_LIVE_ADMIN_PROFILE="$XIRANG_AWS_LIVE_ADMIN_PROFILE" \
XIRANG_AWS_LIVE_RCLONE_BINARY="$XIRANG_AWS_LIVE_RCLONE_BINARY" \
go -C backend test -tags=aws_live ./internal/backupasset/provider -run 'RcloneAWSLiveConformance' -count=1 -v -timeout=90m
```

When a fixture is supplied, expected live evidence is SSE-S3 and SSE-KMS single/multipart, dual-marker pages, overwrite/delete marker, exact Head/full/range, wrong external ID, owner/session, key rotation and retained decrypt-only reads. Cleanup uses only the separately authorized test-admin fixture against the exact randomized test prefix.

2026-07-17 owner evidence decision: no dedicated AWS fixture will be created for Child 5. The suite must still compile, its logic and official-wire coverage must be reviewed, and a discovery invocation without fixture must explicitly list the missing variables and skip. This owner-approved exception permits delivery after all offline/SDK/protocol gates pass, but the completion record must say live `not_executed`; a skip is not a pass, MinIO/LocalStack is not substitute evidence, and this release must not be described as live-certified.

- [ ] **Step 4: Update documentation truth.**

Document Admin setup, portable default, full-byte cost, STS trust/external ID, official-S3-only native matrix, encryption/KMS key-ring operations, lifecycle fail-closed behavior, clean rollback window and evidence-preserving preparation. State that native is backend-versioned but not WORM, no Provider deletion is implemented, `backup_assets.enabled` stays false, node-default is legacy-only and Azure/GCS/S3-compatible native is unsupported. Add all new `BACKUP_ASSETS_RCLONE_*` environment mappings to `docs/env-vars.md`.

- [ ] **Step 5: Run full repository, dependency, documentation and image gates.**

```bash
go -C backend mod tidy
(cd backend && govulncheck ./...)
npm --prefix web audit --audit-level=moderate
npm --prefix web run build
node web/scripts/check-bundle-budget.mjs
bash scripts/check-doc-freshness.sh
bash scripts/check-migration-utc-safety.sh
make check
make docker-build
git diff --check
rg -n 'DeleteObjectVersion|--backup-dir|--dest-after|node_default' backend/internal/backupasset/provider/rclone_* backend/internal/backupasset/repository/rclone_* docs/admin/backup-recovery.md
if find backend/internal/database/migrations -type f -print | rg -q '/00006[5-9]|/00007[01]'; then echo 'unexpected reserved migration'; exit 1; fi
```

Expected: every gate passes; any `DeleteObjectVersion` occurrence is confined to negative assertions/build-tagged test cleanup, forbidden Rclone options are confined to rejection tests/docs, `node_default` is confined to legacy/rejection paths, and Child 5 created no new migration.

- [ ] **Step 6: Perform Phase 3.3 knowledge capture and Phase 3.4 work commit.**

Use `trellis-update-spec` only for reusable, codebase-backed contracts learned during implementation; otherwise record a no-change decision. After PostgreSQL, real Rclone, offline official-wire/SDK/protocol tests, the reviewed AWS live-suite logic, its explicit missing-fixture skip, and all remaining required gates are green, stage only reviewed Child 5 functional/planning/docs changes and create one work commit. Record the 2026-07-17 owner evidence exception and live `not_executed` state; do not record an AWS live pass:

```bash
git add backend web docs
git add .github/workflows/ci.yml
git add .trellis/spec .trellis/tasks/07-12-backup-data-explorer-design
git add .trellis/tasks/07-15-backup-assets-rclone-versioning
git diff --cached --check
git commit -m "feat: add rclone versioned recovery points"
```

Expected: no unrelated user changes, secrets, local runtime directories, live fixture identities or test credentials are staged.

- [ ] **Step 7: Finish and integrate on the same branch.**

Invoke `trellis-finish-work` only after the work commit; let it archive Child 5 and create the journal/archive commit. Push the same branch, create one PR containing both commits, monitor every required CI job, fix failures on this branch, merge only when required checks pass, monitor Release Please and any resulting GitHub Release/Publish Docker Images/Sync Docker Hub Description workflows, explicitly record when no release is expected, then sync local `main` to `origin/main`.

## 13. Plan Self-Review

| Design section | Frozen contract | Planned tasks and gate |
| --- | --- | --- |
| 0 | authority, evidence and no-start boundary | Section 0; Task 11 evidence gates |
| 1 | goals, non-goals and exact terminology | Sections 0--1; Task 11 scope scan |
| 2 | single typed strategy and three independent facts | Tasks 1 and 7; cross-provider tests |
| 3 | closed attempt/commit and linearization | Task 1 ledger; Tasks 3, 6 and 7 codec/replay tests |
| 4 | attempt-qualified portable layout, staged payload and marker-last | Tasks 2 and 3; real v1.74.4 conformance |
| 5 | exhaustive bound config, full-byte floor, cost, copy-dest and reconciliation | Tasks 2, 3, 8 and 11; provider-schema set-equality gate |
| 6 | official AWS profile, V3 binding, STS, lifecycle and encryption | Tasks 4 and 5; protocol fixtures, SDK tests and reviewed opt-in live suite |
| 7 | serialized B0/B1 graph, exact VersionId proof and control commit | Task 6; graph/codec tests and reviewed opt-in live sequence |
| 8 | shared orchestration, immutable deadline, crash and shutdown order | Task 7; race/restart suites |
| 9 | activation, baseline, clean/preparation rollback and 000064 safety | Tasks 4 and 8; dual-database tests |
| 10 | safe API, eight privileged endpoints and typed UI | Tasks 1, 9 and 10; router/mapper/a11y gates |
| 11 | secret isolation, stable errors, resource ceilings and observability | Tasks 1--11; redaction/limit/security scans |
| 12 | provider/repository/task/API/frontend dependency direction | Section 1.3 and Tasks 1--10; boundary tests |
| 13 | unit, fixture, race, dual-DB, frontend, Rclone and opt-in AWS live matrix | Every task's focused gate plus Task 11 and the recorded owner evidence exception |
| 14 | disabled-by-default rollout, compatibility, docs and one-PR delivery | Tasks 8--11; final integration sequence |
| 15 | complete design/plan approval and separate start authorization | Sections 0 and 14 |

Self-review completed:

- Every design section 0--15 maps to an implementation task and an executable verification gate.
- Portable and native share the same binding/coordinator/API surface but retain separate provider adapters and conformance; neither can silently fall back to the other or to legacy.
- No new migration, public Catalog/content/restore/retention/purge UI, storage WORM claim, Azure/GCS/S3-compatible native profile or production exact-version deletion is planned.
- Type names and internal/public encryption mappings are consistent: private `sse_s3_v1 | sse_kms_cmk_v1` maps only to public `sse_s3 | sse_kms_cmk`; unknown maps to blocked/unsupported.
- No unresolved planning markers, deferred-work phrases, generic error handling or unscoped test instructions remain before plan approval.
- AWS live suite逻辑和official-wire fixtures是完成要求；按2026-07-17 owner证据例外，真实AWS执行状态记录为not_executed而不阻塞Child交付。该例外不是live pass或mock替代，破坏性key-state仍只由official wire fixtures验证。

## 14. Next Authorization Gates

This plan and the separate start authorization are complete, but the command has not run and the task remains `planning` under the active workflow-state.

1. Full `implement.md` approval is complete.
2. Separate `task.py start` and implementation authorization is complete.
3. Once the workflow-state permits the phase transition, the next action is:

```bash
python3 ./.trellis/scripts/task.py start .trellis/tasks/07-15-backup-assets-rclone-versioning
```

No product edit may occur before that command succeeds. After it succeeds, execute the already approved inline implementation and delivery plan without implement/check sub-agents.
