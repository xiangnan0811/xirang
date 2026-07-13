# Backup Asset Domain Foundation Implementation Plan

> **For the implementing Codex session:** REQUIRED SUB-SKILLS: load trellis-before-dev, superpowers:test-driven-development, and superpowers:executing-plans. This repository is in Codex inline mode; do not dispatch implement/check sub-agents. Execute the checkboxes in order.

**Status:** Focused package approved on 2026-07-13; task activated and implementation in progress.

**Goal:** Add the disabled-by-default domain, schema, key, lease, authorization, step-up, audit, and migration-parity foundation required by every later backup-asset child, without exposing an asset route or touching Provider bytes.

**Architecture:** backend/internal/backupasset owns pure domain contracts and persistence services; model owns GORM records; secure owns only domain-key wrapping primitives; auth owns the StepUpAction allowlist. Additive 000062 migrations provide identical SQLite/PostgreSQL shape, while the existing bundled frontend migrates every step-up caller to action-keyed proof issuance/storage.

**Tech stack:** Go 1.26.5, Gin, GORM, golang-migrate, SQLite/PostgreSQL, AES-GCM/HMAC-SHA-256, zerolog, React 18, TypeScript strict, Vitest, GitHub Actions.

---

## 0. Start and scope gate

- [x] User reviews prd.md, design.md, and this implement.md and explicitly approves implementation.
- [x] Confirm task status remains planning before approval:

    python3 ./.trellis/scripts/get_context.py

  Expected: current task is 07-13-backup-assets-domain-foundation with Status: planning.

- [x] After approval only, run:

    python3 ./.trellis/scripts/task.py start backup-assets-domain-foundation

  Expected: status becomes in_progress. Do not start the planning parent.

- [x] Load pre-development context before the first product edit:

    python3 ./.trellis/scripts/get_context.py --mode phase --step 2.1 --platform codex

- [x] Reconfirm clean dedicated branch and migration reservation:

    git status --short --branch
    git merge-base --is-ancestor 4ea4c2b HEAD
    find backend/internal/database/migrations -maxdepth 2 -type f -name '000062_*' -print

  Expected: branch codex/backup-assets-domain-foundation, no unrelated changes, ancestor check exits 0, and no pre-existing 000062 file outside this reviewed work.

- [x] Freeze the live step-up caller manifest before edits:

    rg -l 'RequireStepUp|EnforceStepUp|enforceStepUpForContext|validateStepUpProof|GenerateStepUpToken|requestStepUpProof|ensureStepUpProof|useStepUpAction|readStepUpProof|saveStepUpProof|clearStepUpProof' backend web/src | sort

  Expected: reconcile every result with Section 1. If a new caller exists, stop and amend this plan before implementation.

## 1. Exact file manifest

Only the following product/process files may change. If implementation discovers another required file, amend focused design/plan and obtain review before staging it.

### 1.1 Create — backend domain and models

- backend/internal/model/backup_asset.go
- backend/internal/model/backup_asset_catalog.go
- backend/internal/model/backup_asset_audit.go
- backend/internal/model/backup_asset_lease.go
- backend/internal/backupasset/domain.go
- backend/internal/backupasset/errors.go
- backend/internal/backupasset/service.go
- backend/internal/backupasset/keyring.go
- backend/internal/backupasset/lease.go
- backend/internal/backupasset/authorization.go
- backend/internal/backupasset/audit_action.go
- backend/internal/backupasset/audit.go
- backend/internal/backupasset/domain_test.go
- backend/internal/backupasset/service_test.go
- backend/internal/backupasset/keyring_test.go
- backend/internal/backupasset/lease_test.go
- backend/internal/backupasset/authorization_test.go
- backend/internal/backupasset/audit_action_test.go
- backend/internal/backupasset/audit_test.go
- backend/internal/secure/keyring.go
- backend/internal/secure/keyring_test.go
- backend/internal/auth/step_up_action.go
- backend/internal/auth/step_up_action_test.go

### 1.2 Create — migrations and migration parity

- backend/internal/database/migrations/sqlite/000062_backup_asset_foundation.up.sql
- backend/internal/database/migrations/sqlite/000062_backup_asset_foundation.down.sql
- backend/internal/database/migrations/postgres/000062_backup_asset_foundation.up.sql
- backend/internal/database/migrations/postgres/000062_backup_asset_foundation.down.sql
- backend/internal/database/backup_asset_migrations_integration_test.go

### 1.3 Modify — backend

- backend/internal/model/task.go
- backend/internal/settings/service.go
- backend/internal/settings/service_test.go
- backend/internal/middleware/rbac.go
- backend/internal/middleware/rbac_test.go
- backend/internal/auth/jwt.go
- backend/internal/auth/jwt_test.go
- backend/internal/api/handlers/auth_handler.go
- backend/internal/api/handlers/auth_handler_test.go
- backend/internal/api/handlers/step_up.go
- backend/internal/api/handlers/step_up_test.go
- backend/internal/api/handlers/credential_access_grant.go
- backend/internal/api/handlers/credential_access_grant_test.go
- backend/internal/api/handlers/batch_handler.go
- backend/internal/api/handlers/batch_handler_test.go
- backend/internal/api/handlers/task_handler.go
- backend/internal/api/handlers/task_handler_test.go
- backend/internal/api/handlers/terminal_handler.go
- backend/internal/api/handlers/terminal_handler_test.go
- backend/internal/api/router.go
- backend/internal/api/router_test.go
- .github/workflows/ci.yml

### 1.4 Create/modify — frontend

- web/src/lib/api/totp-api.ts
- web/src/lib/api/totp-api.test.ts
- web/src/lib/api/client.test.ts
- web/src/lib/step-up-storage.ts
- web/src/lib/step-up-storage.test.ts
- web/src/hooks/use-step-up-action.ts
- web/src/hooks/use-step-up-action.test.tsx
- web/src/hooks/use-console-task-operations.ts
- web/src/hooks/use-console-data.test.tsx
- web/src/context/auth-context-provider.tsx
- web/src/context/auth-context.shared.ts
- web/src/context/auth-context.test.tsx
- web/src/components/batch-command-dialog.tsx
- web/src/components/batch-command-dialog.test.tsx
- web/src/components/config-export-import.tsx
- web/src/components/config-export-import.test.tsx
- web/src/components/restore-confirm-dialog.tsx
- web/src/components/restore-confirm-dialog.test.tsx
- web/src/components/snapshot-browser.tsx
- web/src/components/snapshot-browser.test.tsx
- web/src/components/ssh-key-export-dialog.tsx
- web/src/components/ssh-key-export-dialog.test.tsx
- web/src/components/web-terminal.tsx
- web/src/components/web-terminal.test.tsx
- web/src/pages/tasks-page.tsx
- web/src/pages/tasks-page.test.tsx
- web/src/pages/notifications/alert-center.tsx
- web/src/pages/notifications-page.test.tsx

web/src/lib/api/core.ts requires no behavior edit: its existing clearStepUpProof() call intentionally becomes “clear all action proofs” through the new optional-action storage API. A compile/test failure proving otherwise requires a reviewed manifest amendment.

### 1.5 Modify — configuration/docs

- backend/.env.example
- backend/.env.production.example
- .env.deploy
- docs/env-vars.md

### 1.6 Trellis planning/lifecycle files

- .trellis/tasks/07-13-backup-assets-domain-foundation/prd.md
- .trellis/tasks/07-13-backup-assets-domain-foundation/design.md
- .trellis/tasks/07-13-backup-assets-domain-foundation/implement.md
- .trellis/tasks/07-13-backup-assets-domain-foundation/task.json

implement.jsonl and check.jsonl remain the seed example in inline mode and are not curated.

## 2. Domain contract — tests first

**Files:** backend/internal/backupasset/domain_test.go, service_test.go; then domain.go, errors.go, service.go.

- [ ] Write table-driven tests before implementation:

  - TestRepositoryStateTransitions covers every state pair and idempotent no-op;
  - TestImmutableRecoveryPointTransitions covers every state pair;
  - TestMutableHeadLifecyclePreservesStableID;
  - TestMutableHeadRetirementAndPhysicalPurgeAreDistinct;
  - TestRecoveryPointProfileRejectsCrossEnumDrift;
  - TestPublicationModeMapping;
  - TestAssetRefRequiresRecoveryPointAndEntry;
  - TestCommandCapabilityRequiresArtifactContract;
  - TestSanitizedDTOExcludesSecretsAndLocators;
  - TestOpaqueIDFormatAndEntropySourceFailure.

- [ ] Run the red test:

    cd backend
    go test ./internal/backupasset -run 'Test(RepositoryStateTransitions|ImmutableRecoveryPointTransitions|MutableHead|RecoveryPointProfile|PublicationModeMapping|AssetRef|CommandCapability|SanitizedDTO|OpaqueID)' -count=1

  Expected: FAIL because domain symbols do not exist; no test may pass by testing a local fake.

- [ ] Implement defined string types, constant registries, validators, crypto/rand opaque IDs, capability reason allowlist and sentinels exactly as design.md §§3–4.

  Required exported signatures:

    func NewOpaqueID() (string, error)
    func ValidateOpaqueID(string) error
    func ValidateAssetRef(AssetRef) error
    func ValidateRepositoryTransition(from, to RepositoryStatus) error
    func ValidateRecoveryPointTransition(profile RecoveryPointProfile, to RecoveryPointState) error
    func ValidateRecoveryPointProfile(RecoveryPointProfile) error
    func MapPublicationMode(ProviderKind, TaskPublicationMode) (VersionMode, PointVersionSemantics, RecoveryPointState, error)
    func CapabilitiesForTask(TaskArtifactContract) CapabilitySet

- [ ] Add explicit RepositoryDTO/RecoveryPointDTO converters. Never return model structs from service.go.

- [ ] Re-run the focused domain tests.

  Expected: PASS, including exhaustive invalid-pair assertions and raw JSON negative checks.

## 3. Schema, models and dual-engine migration harness

**Files:** four model files, task.go, four migration files, migration integration test, CI workflow.

- [ ] Write migration integration tests first:

  - TestBackupAssetMigration062SQLiteApplyDown;
  - TestBackupAssetMigration062PostgresApplyDown;
  - TestBackupAssetMigration062ForeignKeysSetNull;
  - TestBackupAssetMigration062MutableHeadAndActiveGenerationUniqueness;
  - TestBackupAssetMigration062UTCAndModelParity.

  The test helper creates a disposable SQLite file and a disposable PostgreSQL database, applies 000001..000062 through embedded golang-migrate, inspects schema/FKs/indexes/CHECKs, steps down exactly once to 000061, and verifies all Child 1 tables plus tasks.archived_at disappear.

- [ ] Run the SQLite red test before writing 000062:

    cd backend
    go test ./internal/database -run 'TestBackupAssetMigration062SQLite' -count=1

  Expected: FAIL because migration version 62/tables are absent.

- [ ] Implement the four SQL files exactly as design.md §5. Keep engine versions/names in lockstep. Do not use AutoMigrate.

- [ ] Add GORM structs for every SQL column and TableName methods where GORM pluralization would differ. Secret/locator/wrapped/fence fields use json:"-".

- [ ] Add Task.ArchivedAt *time.Time with json:"archived_at,omitempty". Do not change Task.Delete or retention behavior.

- [ ] Add model hook encryption only for access config and Provider/rollback/entry commit locators. Hooks use secure.EncryptIfNeeded/DecryptIfNeeded; API code still uses sanitized DTOs.

- [ ] Add dedicated PostgreSQL CI job:

  - service image postgres:18-alpine;
  - database xirang_test;
  - password literal FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY;
  - TEST_POSTGRES_DSN set to that service;
  - REQUIRE_POSTGRES_MIGRATION_TEST=1;
  - run only the migration integration suite after go mod download.

  Test behavior: missing TEST_POSTGRES_DSN may skip only on a normal local run; when REQUIRE_POSTGRES_MIGRATION_TEST=1 it must Fatal.

- [ ] Run SQLite tests again and run the UTC lint:

    go test ./internal/database -run 'TestBackupAssetMigration062SQLite' -count=1
    cd ../..
    bash scripts/check-migration-utc-safety.sh

  Expected: PASS.

## 4. Domain-key wrapping and versioned keyring

**Files:** secure/keyring.go/test; backupasset/keyring.go/test; model/backup_asset.go.

- [ ] Write secure primitive tests first:

  - TestWrapDomainKeyRoundTripWithAAD;
  - TestWrappedDomainKeyRejectsWrongDomainVersionAndTamper;
  - TestUnwrapDomainKeyAcceptsLegacyV2KEK;
  - TestWrappedEnvelopeContainsNoPlaintextOrKEK.

- [ ] Write persistent keyring tests first:

  - TestEnsureRequiredDomainsCreatesIndependentRandomKeys;
  - TestStableDomainsAllowRewrapButRejectRotate;
  - TestCursorRotationKeepsBoundedVerifyOnlyOverlap;
  - TestAuditFingerprintVersionIsExplicit;
  - TestRewrapPreservesDomainVersionAndPlaintext;
  - TestMissingOrLostKeyFailsClosedWithoutRegeneration;
  - TestConcurrentEnsureCreatesOneActiveKeyPerDomain.

- [ ] Run red tests:

    cd backend
    go test ./internal/secure ./internal/backupasset -run 'Test(Wrap|Wrapped|Unwrap|EnsureRequiredDomains|StableDomains|CursorRotation|AuditFingerprint|Rewrap|MissingOrLost|ConcurrentEnsure)' -count=1

  Expected: FAIL because wrap/keyring APIs are absent.

- [ ] Implement secure AES-256-GCM envelope with AAD domain/version/algorithm, random nonce and KEK fingerprint. Use current DATA_ENCRYPTION_KEY first and DATA_ENCRYPTION_LEGACY_KEY as the previous v2 wrapping KEK. Do not alter enc:v1/enc:v2 field formats.

- [ ] Implement backupasset Keyring with Ensure, Active, ByVersion, Rotate, RewrapAll and MarkLost. Entry Identity and Recovery Cleanup Ownership reject Rotate; Cursor Signing and Audit Fingerprint use active/verify_only versions.

- [ ] Re-run the key tests.

  Expected: PASS; four domains have four different plaintext keys and KEK rewrap changes only envelope metadata.

## 5. RecoveryPointLease and fencing

**Files:** model/backup_asset_lease.go; backupasset/lease.go/test.

- [ ] Write tests first:

  - TestLeaseAcquireRenewRelease;
  - TestLeaseRejectsDuplicateActiveOwnerSlot;
  - TestLeaseTakeoverReplacesAttemptAndFence;
  - TestLeaseOldFenceCannotPublishAfterTakeover;
  - TestLeaseRenewCannotCrossAbsoluteDeadline;
  - TestLeaseTakeoverCannotExtendAbsoluteDeadline;
  - TestLeaseReconcileExpiredAfterRestart;
  - TestLeaseConcurrentTakeoverHasSingleWinner.

- [ ] Run red tests:

    cd backend
    go test ./internal/backupasset -run 'TestLease' -count=1

  Expected: FAIL because LeaseService does not exist.

- [ ] Implement:

    Acquire(ctx, AcquireLeaseRequest) (Lease, error)
    Renew(ctx, LeaseFence) (Lease, error)
    Release(ctx, LeaseFence) error
    Takeover(ctx, TakeoverLeaseRequest) (Lease, error)
    ValidateFence(ctx, LeaseFence) error
    ReconcileExpired(ctx) (int64, error)

  Enforce settings, 168h hard cap, conditional RowsAffected checks and injected UTC clock. No process-local lock is the source of truth.

- [ ] Re-run on SQLite and PostgreSQL through the same service test table where feasible.

  Expected: PASS; exactly one takeover wins and every stale fence fails.

## 6. Permission/settings foundation

**Files:** authorization.go/test, rbac.go/test, settings/service.go/test, environment docs/examples, service.go.

- [ ] Write authorization tests:

  - admin has all seven permissions;
  - operator has list/preview only;
  - viewer/unknown/empty has none;
  - manage does not imply purge;
  - download does not imply recover;
  - recovery result requires recover + exact owner + recovery.result_download, not download.

- [ ] Write settings tests:

  - all eleven definitions exist with exact default/env/type/min/max;
  - backup_assets.enabled resolves false with no DB/env override;
  - MaxDuration init and Validate reject malformed/too-large duration;
  - lease heartbeat must be lower than lease duration in foundation config validation;
  - no new setting is Sensitive or RequiresRestart;
  - registry uniqueness replaces reliance on a bare count-only assertion.

- [ ] Run red tests:

    cd backend
    go test ./internal/backupasset ./internal/middleware ./internal/settings -run 'Test(Authorization|RecoveryResult|BackupAssetPermissions|BackupAssetSettings|MaxDuration|Registry)' -count=1

  Expected: FAIL for missing permissions/settings.

- [ ] Implement permission constants in backupasset/authorization.go and reference them from middleware role maps.

- [ ] Extend SettingDef with MaxDuration, validate it at init and request validation, add the eleven settings, and keep enabled=false.

- [ ] Update backend/.env.example, backend/.env.production.example, .env.deploy and docs/env-vars.md with exact variables/defaults. .env.deploy and production example explicitly set BACKUP_ASSETS_ENABLED=false. Update DATA_ENCRYPTION_LEGACY_KEY wording to include domain-key v2 rewrap.

- [ ] Re-run tests and documentation freshness:

    cd backend && go test ./internal/backupasset ./internal/middleware ./internal/settings -run 'Test(Authorization|RecoveryResult|BackupAssetPermissions|BackupAssetSettings|MaxDuration|Registry)' -count=1
    cd ..
    bash scripts/check-doc-freshness.sh

  Expected: PASS.

## 7. Backend purpose-bound step-up

**Files:** auth step_up_action/jwt files, auth/step_up/credential grant/batch/task/terminal handlers and tests, router files.

- [ ] Add red tests before changing signatures:

  - TestStepUpActionAllowlistContainsExactContract;
  - TestGenerateStepUpTokenRejectsUnknownAction;
  - TestStepUpRequestRequiresKnownAction;
  - TestStepUpProofRejectsMissingLegacyGenericAction;
  - TestStepUpProofPairwiseCrossPurposeRejection;
  - TestTerminalAcceptsOnlyTerminalOpenProof;
  - TestEveryStepUpRouteDeclaresExpectedAction;
  - focused task/batch/grant tests for their exact action.

- [ ] Run red tests:

    cd backend
    go test ./internal/auth ./internal/api/... -run 'Test.*(StepUpAction|StepUpRequest|StepUpProof|CrossPurpose|Terminal.*Purpose|EveryStepUpRoute)' -count=1

  Expected: FAIL because Claims and issuance have no step_up_action.

- [ ] Add the exact 17-action auth registry from design.md §9. GenerateStepUpToken requires a valid StepUpAction and embeds it in Claims.

- [ ] Change POST /auth/step-up request to code + step_up_action. Audit issuance using the requested safe action; unknown/missing action returns standard bad-request envelope.

- [ ] Change RequireStepUp, RequireStepUpIf, EnforceStepUp, enforceStepUpForContext and validateStepUpProof so the expected StepUpAction is explicit and exact.

- [ ] Migrate every backend caller from the frozen manifest. Keep credential grant Purpose values unchanged; StepUpAction and credential purpose must be separately visible in code/tests.

- [ ] Pin terminal WebSocket validation to terminal.open before node/credential load.

- [ ] Run all auth/API focused tests.

  Expected: PASS; an N×N matrix accepts exactly 17 diagonal pairs and rejects all 272 off-diagonal pairs, plus missing/unknown/generic proofs.

## 8. Frontend purpose propagation and storage isolation

**Files:** every frontend file in Sections 1.4.

- [ ] Write/adjust tests first:

  - totp API sends step_up_action;
  - storage isolates all action keys, expires one without clearing others, clears one/all, and deletes legacy generic keys without migration;
  - Auth context reuses only the same action and sends the pending action;
  - useStepUpAction forwards its declared action and clears only that action after repeated 403;
  - each nine-caller mapping in design.md §9.3 is asserted by its component/hook test;
  - logout, TOTP disable and HTTP 401 clear all proofs.

- [ ] Run the red frontend tests:

    cd web
    npm run test -- --run src/lib/api/totp-api.test.ts src/lib/step-up-storage.test.ts src/context/auth-context.test.tsx src/hooks/use-step-up-action.test.tsx

  Expected: FAIL because APIs/storage are still generic.

- [ ] Export StepUpAction constants/type from totp-api.ts and require action in requestStepUpProof.

- [ ] Replace two legacy storage keys with one versioned action map. Never promote the old generic proof into any action.

- [ ] Change AuthContext to ensureStepUpProof(action, options) and clearStepUpProof(action?). Pending dialog state stores the exact action used on submit.

- [ ] Change useStepUpAction(action, options) and every caller:

  - ssh_key.export;
  - terminal.open;
  - config.import/config.export;
  - snapshot.restore;
  - task.restore_trigger;
  - task.manual_trigger;
  - task.batch_trigger;
  - batch_command.create.

- [ ] Run all affected tests, then the full frontend gate:

    npm run check

  Expected: typecheck, lint, Vitest coverage and Vite build PASS; no zero-argument ensureStepUpProof/useStepUpAction production caller remains.

## 9. Typed asset audit and segmented checkpoints

**Files:** model/backup_asset_audit.go; backupasset/audit_action.go/test and audit.go/test.

- [ ] Write registry/sanitizer tests first:

  - TestAuditActionRegistryMatchesDesignContract;
  - TestAuditRejectsUnknownActionAndField;
  - TestAuditSanitizerDropsForbiddenKeysAndValues;
  - TestAuditFingerprintUsesIndependentKeyedDomainAndVersion;
  - TestAuditRecordNeverContainsRawPathNameQuerySnippetContentTicketCookieCredentialOrLocator.

- [ ] Write chain tests first:

  - TestAuditWriterMaintainsEntryChain;
  - TestAuditWriterClosesSegmentByCountAndAge;
  - TestAuditCheckpointLinksAdjacentSegments;
  - TestAuditDetailPurgeRetainsVerifiableCheckpoint;
  - TestAuditVerifierDetectsEntryAndCheckpointTamper;
  - TestAuditConcurrentWritersProduceUniqueSequence;
  - TestRangeSummaryIsBounded.

- [ ] Run red tests:

    cd backend
    go test ./internal/backupasset -run 'TestAudit' -count=1

  Expected: FAIL because action registry/writer do not exist.

- [ ] Implement the exact action and field registries. NewEvent accepts only typed action/fields.

- [ ] Implement HMAC path/query fingerprint through Audit Fingerprint domain key; store key version and digest only.

- [ ] Implement transactional segment writer, count/age closure, checkpoint hash, verifier and detail-purge transition. Use logger.Module("backup_asset_audit") with only safe action/correlation/error-category fields.

- [ ] Re-run audit tests.

  Expected: PASS; tampering is detected and purged-detail checkpoints retain continuity without raw asset data.

## 10. Dual-engine and focused integration gate

- [ ] Start a disposable local PostgreSQL when Docker is available:

    docker run --rm --detach --name xirang-child1-postgres -e POSTGRES_PASSWORD=FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY -e POSTGRES_DB=xirang_test -p 55432:5432 postgres:18-alpine
    until docker exec xirang-child1-postgres pg_isready -U postgres -d xirang_test; do sleep 1; done

- [ ] Run required PostgreSQL migration parity:

    cd backend
    TEST_POSTGRES_DSN='postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:55432/xirang_test?sslmode=disable' REQUIRE_POSTGRES_MIGRATION_TEST=1 go test ./internal/database -run 'TestBackupAssetMigration062Postgres' -count=1

  Expected: PASS; test creates/drops its own disposable child database or schema and leaves the service database clean.

- [ ] Run focused backend integration:

    go test ./internal/backupasset ./internal/model ./internal/secure ./internal/settings ./internal/middleware ./internal/database ./internal/auth ./internal/api/... -run 'BackupAsset|DomainKey|RecoveryPointLease|StepUp|Purpose|Permission|Audit' -count=1

  Expected: PASS.

- [ ] Stop the local PostgreSQL:

    docker rm -f xirang-child1-postgres

- [ ] If Docker is unavailable, record that local limitation, but do not waive the dedicated required PostgreSQL CI job. CI must still run with REQUIRE_POSTGRES_MIGRATION_TEST=1.

## 11. Full validation and negative-scope gate

- [ ] Format changed Go files:

    cd backend
    gofmt -w internal/model/backup_asset.go internal/model/backup_asset_catalog.go internal/model/backup_asset_audit.go internal/model/backup_asset_lease.go internal/backupasset/*.go internal/secure/keyring.go internal/auth/step_up_action.go internal/auth/jwt.go internal/api/handlers/auth_handler.go internal/api/handlers/step_up.go internal/api/handlers/credential_access_grant.go internal/api/handlers/batch_handler.go internal/api/handlers/task_handler.go internal/api/handlers/terminal_handler.go internal/api/router.go internal/settings/service.go internal/middleware/rbac.go
    cd ..

- [ ] Run complete gates:

    make check
    bash scripts/check-migration-utc-safety.sh
    bash scripts/check-doc-freshness.sh
    git diff --check

  Expected: all PASS.

- [ ] Run caller and forbidden-scope scans:

    rg -n 'ensureStepUpProof\(\)|useStepUpAction\(\)' web/src --glob '!**/*.test.*'
    rg -n 'GenerateStepUpToken\([^,]+\)' backend
    rg -n 'backup_assets\.enabled[^\n]*true|BACKUP_ASSETS_ENABLED=true' backend web .env.deploy --glob '!**/*_test.go' --glob '!**/*.test.*'
    git diff -- backend/internal/api/router.go

  Expected: all three production-source searches return no matches. Tests may explicitly exercise a true override. Router diff changes only existing step-up middleware arguments/tests; it adds no backup repository/asset/content/export/recovery route.

- [ ] Verify no Provider implementation/mutation entered the diff:

    git diff --name-only | rg 'backend/internal/task/executor|backend/internal/backupasset/provider|snapshot_handler|file_handler|retention'

  Expected: no matches.

- [ ] Verify exact changed-file manifest:

    git status --short
    git diff --name-only

  Expected: every path is listed in Section 1 or is an expected Trellis lifecycle move; no unrelated user file is staged.

## 12. Requirement-to-proof matrix

| PRD requirement | Owning steps | Required evidence |
|---|---|---|
| Repository/RP/AssetRef/state/capability | 2 | exhaustive state pair tests, mapping tests, sanitized JSON negatives |
| Paired 000062 and lineage | 3, 10 | SQLite/PostgreSQL apply/down, FK/index/CHECK/UTC parity |
| Four key domains and rewrap/loss | 4 | AAD/tamper/separation/stability/rotation/rewrap tests |
| RecoveryPointLease/fence | 5 | acquire/renew/release/takeover/deadline/restart/concurrency tests |
| Purpose-bound step-up | 7–8 | 17×17 backend matrix, terminal exact action, nine frontend callers |
| Dedicated RBAC/recovery result | 6 | role matrix and non-implication tests |
| Asset audit | 9 | registry/redaction/fingerprint/segment/checkpoint/tamper tests |
| Settings/feature off | 6, 11 | registry/default/env/docs tests and negative enabled scan |
| No route/Provider/UI | 11 | router diff and forbidden-path diff scan |

## 13. Commit, PR and post-merge gate

- [ ] Run verification-before-completion and trellis-check only after every prior gate passes.
- [ ] Stage exact paths from Section 1, never whole backend/web directories:

    git add .trellis/tasks/07-13-backup-assets-domain-foundation/prd.md .trellis/tasks/07-13-backup-assets-domain-foundation/design.md .trellis/tasks/07-13-backup-assets-domain-foundation/implement.md .trellis/tasks/07-13-backup-assets-domain-foundation/task.json
    git add backend/internal/model/backup_asset.go backend/internal/model/backup_asset_catalog.go backend/internal/model/backup_asset_audit.go backend/internal/model/backup_asset_lease.go backend/internal/model/task.go
    git add backend/internal/backupasset/domain.go backend/internal/backupasset/errors.go backend/internal/backupasset/service.go backend/internal/backupasset/keyring.go backend/internal/backupasset/lease.go backend/internal/backupasset/authorization.go backend/internal/backupasset/audit_action.go backend/internal/backupasset/audit.go
    git add backend/internal/backupasset/domain_test.go backend/internal/backupasset/service_test.go backend/internal/backupasset/keyring_test.go backend/internal/backupasset/lease_test.go backend/internal/backupasset/authorization_test.go backend/internal/backupasset/audit_action_test.go backend/internal/backupasset/audit_test.go
    git add backend/internal/secure/keyring.go backend/internal/secure/keyring_test.go backend/internal/auth/step_up_action.go backend/internal/auth/step_up_action_test.go backend/internal/auth/jwt.go backend/internal/auth/jwt_test.go
    git add backend/internal/database/backup_asset_migrations_integration_test.go backend/internal/database/migrations/sqlite/000062_backup_asset_foundation.up.sql backend/internal/database/migrations/sqlite/000062_backup_asset_foundation.down.sql backend/internal/database/migrations/postgres/000062_backup_asset_foundation.up.sql backend/internal/database/migrations/postgres/000062_backup_asset_foundation.down.sql
    git add backend/internal/settings/service.go backend/internal/settings/service_test.go backend/internal/middleware/rbac.go backend/internal/middleware/rbac_test.go
    git add backend/internal/api/handlers/auth_handler.go backend/internal/api/handlers/auth_handler_test.go backend/internal/api/handlers/step_up.go backend/internal/api/handlers/step_up_test.go backend/internal/api/handlers/credential_access_grant.go backend/internal/api/handlers/credential_access_grant_test.go backend/internal/api/handlers/batch_handler.go backend/internal/api/handlers/batch_handler_test.go backend/internal/api/handlers/task_handler.go backend/internal/api/handlers/task_handler_test.go backend/internal/api/handlers/terminal_handler.go backend/internal/api/handlers/terminal_handler_test.go backend/internal/api/router.go backend/internal/api/router_test.go
    git add web/src/lib/api/totp-api.ts web/src/lib/api/totp-api.test.ts web/src/lib/api/client.test.ts web/src/lib/step-up-storage.ts web/src/lib/step-up-storage.test.ts web/src/hooks/use-step-up-action.ts web/src/hooks/use-step-up-action.test.tsx web/src/hooks/use-console-task-operations.ts web/src/hooks/use-console-data.test.tsx web/src/context/auth-context-provider.tsx web/src/context/auth-context.shared.ts web/src/context/auth-context.test.tsx
    git add web/src/components/batch-command-dialog.tsx web/src/components/batch-command-dialog.test.tsx web/src/components/config-export-import.tsx web/src/components/config-export-import.test.tsx web/src/components/restore-confirm-dialog.tsx web/src/components/restore-confirm-dialog.test.tsx web/src/components/snapshot-browser.tsx web/src/components/snapshot-browser.test.tsx web/src/components/ssh-key-export-dialog.tsx web/src/components/ssh-key-export-dialog.test.tsx web/src/components/web-terminal.tsx web/src/components/web-terminal.test.tsx web/src/pages/tasks-page.tsx web/src/pages/tasks-page.test.tsx web/src/pages/notifications/alert-center.tsx web/src/pages/notifications-page.test.tsx
    git add backend/.env.example backend/.env.production.example .env.deploy docs/env-vars.md .github/workflows/ci.yml

- [ ] Inspect staged paths/content:

    git diff --cached --name-only
    git diff --cached --check

  Expected: exact manifest only and no whitespace errors.

- [ ] Commit:

    git commit -m "feat: add backup asset domain foundation"

- [ ] Push the branch, open a PR to main, and monitor every required CI job. The PostgreSQL migration job is required, not allowed to skip or remain missing.
- [ ] Fix failures on this branch, push, and continue monitoring until all required checks pass.
- [ ] After squash merge, monitor Release Please and applicable post-merge automation. This foundation is feature-disabled; record explicitly whether a formal GitHub Release/Docker publish was expected and what occurred.
- [ ] Fast-forward local main to origin/main before creating Child 2. Child 2 must depend on the merged Child 1 commit, not this unmerged branch.

## 14. Rollback

- Before any later child writes foundation rows: revert the application commit and run 000062 down on disposable/confirmed-unused databases only.
- After any Repository/RecoveryPoint/domain key/audit row exists: set backup_assets.enabled=false, keep additive schema, revert application behavior through a forward fix, and do not run down.
- Step-up rollback requires reverting backend and bundled frontend together. Never reintroduce a backend compatibility path accepting action-less proof while the new code is active.
- KEK rewrap rollback keeps the old KEK available until every wrapped domain key is proven readable under the restored/current KEK.
- Child 1 has no Provider mutation, so rollback must not delete or rewrite Provider bytes.

## 15. Plan self-review

- [x] Every PRD acceptance criterion maps to an implementation step and command.
- [x] Every persistent entity has an exact migration/model owner and delete rule.
- [x] Every step-up production caller found in current code has an exact action and test owner.
- [x] SQLite and PostgreSQL both apply and down through the real migrator.
- [x] No route, Provider mutation, UI enablement or legacy-data inference is included.
- [x] File staging is exact; no directory-wide backend/web add is used.
- [x] Inline mode is preserved; implementation/check sub-agents are not dispatched.
- [x] User approval preceded task activation and every product edit.

## 16. Verification record (2026-07-13)

- Focused backend integration passed with PostgreSQL required mode enabled across backupasset/model/secure/settings/middleware/database/auth/API packages.
- PostgreSQL 18 migration parity passed through the real migrator using a disposable host-network container. The test now proves apply/down, all 28 named indexes, eight partial-unique definitions, CHECK contracts, three Task/TaskRun `ON DELETE SET NULL` links, 11 model/schema column sets, UTC session/instant behavior, and absence of DB-side timestamp defaults.
- Focused frontend step-up coverage passed: 13 files / 74 tests plus TypeScript typecheck. The complete frontend gate in `make check` passed 128 files / 551 tests, lint, typecheck, coverage, and Vite production build.
- `env -u NODE_ENV TEST_POSTGRES_DSN=... REQUIRE_POSTGRES_MIGRATION_TEST=1 make check` passed backend lint/tests/build and the complete frontend gate.
- `scripts/check-migration-utc-safety.sh`, `scripts/check-doc-freshness.sh`, and `git diff --check` passed. The doc-freshness script emitted its expected router/README reminder; the inspected router diff only replaces existing step-up middleware arguments and adds no route, so `README_backend.md` does not require an out-of-manifest edit.
- Negative scans found no zero-argument frontend proof caller, legacy backend token issuance, production feature enablement, public backup-asset route, Provider implementation/mutation path, or changed path outside the exact manifest.
- Review hardening added red/green regressions for strict wrapped-key envelope EOF, raw Provider-value non-disclosure, unique-only lease conflict classification, and rejection of raw step-up JWTs in typed asset audit IDs.
- Spec-sync judgment: no `.trellis/spec/` update. Shared migration, error, logging, type-safety, branch, and cross-layer conventions were followed without changing them; the new contracts are feature-specific and are fully captured by this focused design, executable tests, and the parent program package. Adding a shared spec file would duplicate those contracts and violate the approved exact manifest.
- Remaining gates: exact staging, commit, push/PR, required CI, then maintainer-authorized merge/post-merge automation and local `main` synchronization before Child 2.
