# Backup Asset Provider Readers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. This Trellis task is configured for inline execution; do not dispatch implementation or check sub-agents. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a feature-gated, read-only Repository/Provider boundary for existing Rsync, Restic, and Rclone backup Tasks, with stable identity, encrypted access binding, bounded exact-locator reads, lineage-safe APIs, and no Provider mutation.

**Architecture:** Root `backupasset` remains the domain package. Concrete narrow ports/adapters live under `backupasset/provider`; transaction, ownership, and audit orchestration live under `backupasset/repository`; operation-coupled filesystem access lives under `fileaccess`; purpose-aware SSH dial and bounded remote command execution live under `sshutil`. Restic uses native repository identity, while legacy Rsync/Rclone use encrypted-salt task-scoped endpoint identities.

**Tech Stack:** Go 1.26, Gin, GORM, SQLite/PostgreSQL-compatible SQL behavior, `golang.org/x/crypto/ssh`, `github.com/pkg/sftp`, Go 1.26 `os.Root`, Linux `golang.org/x/sys/unix.Openat2`, Restic/Rclone executable fakes, Swagger (`swag`).

**Status:** Implementation, contract audit, spec sync, and final local verification complete; Phase 3.4 commit pending.

---

## 0. Execution Contract

- [x] The user reviewed `prd.md`, `design.md`, and this plan and explicitly authorized `task.py start` on 2026-07-13; the Child task is `in_progress` while the parent remains `planning`.
- The main session performs implementation and verification directly. Inline mode forbids implement/check sub-agents.
- No `implement.jsonl` or `check.jsonl` curation is required in inline mode.
- Work stays on `codex/backup-assets-provider-readers`, based on released `main@8317fe6e04ea9ec1480460074ce9fe99ae8a7626`.
- Do not create nested Child 2a/2b tasks. Provider ports, safe access, SSH transport, and Repository transactions are one security boundary; merging any one without the others would leave an unsafe or unusable seam.
- Do not make intermediate Git commits. Follow the user's corrected ordering exactly: implement and verify everything, perform one reviewed Phase 3.4 work commit, run `trellis-finish-work` on the same branch so archive and journal commits follow, then push and create one PR.
- Do not create or modify schema migrations, frontend files, public navigation, Provider data, Task execution commands, or the enabled default.

## 1. Reviewed File Manifest

### Create

- `backend/internal/backupasset/provider/contracts.go` — narrow ports, internal locators, observations, pages, read snapshots, limits.
- `backend/internal/backupasset/provider/errors.go` — typed Provider operation codes and sentinel wrapping.
- `backend/internal/backupasset/provider/registry.go` — capability-specific registration and lookup.
- `backend/internal/backupasset/provider/cursor.go` — signed opaque scoped cursor codec.
- `backend/internal/backupasset/provider/identity.go` — native/scoped identity and config fingerprint derivation.
- `backend/internal/backupasset/provider/runner.go` — Provider-facing bounded command runner interface and allowlisted command specs.
- `backend/internal/backupasset/provider/rsync.go` — legacy mutable tree adapter.
- `backend/internal/backupasset/provider/restic.go` — strict Restic read adapter.
- `backend/internal/backupasset/provider/rclone.go` — strict Rclone read adapter.
- `backend/internal/backupasset/provider/testutil_test.go` — deterministic clocks/keys/fake command and tree transports.
- `backend/internal/backupasset/provider/contracts_test.go`
- `backend/internal/backupasset/provider/registry_test.go`
- `backend/internal/backupasset/provider/cursor_test.go`
- `backend/internal/backupasset/provider/identity_test.go`
- `backend/internal/backupasset/provider/rsync_test.go`
- `backend/internal/backupasset/provider/restic_test.go`
- `backend/internal/backupasset/provider/rclone_test.go`
- `backend/internal/backupasset/repository/service.go` — feature gate and application orchestration.
- `backend/internal/backupasset/repository/binding.go` — Task-derived versioned encrypted binding construction/validation.
- `backend/internal/backupasset/repository/connect.go` — probe-first identity/idempotency/link/binding transaction.
- `backend/internal/backupasset/repository/query.go` — lineage-scoped list/detail and sanitized view DTOs.
- `backend/internal/backupasset/repository/reconcile.go` — observation/disconnect state transactions.
- `backend/internal/backupasset/repository/audit.go` — typed asset-audit adapter and correlation context.
- `backend/internal/backupasset/repository/errors.go` — safe application error classification.
- `backend/internal/backupasset/repository/testutil_test.go`
- `backend/internal/backupasset/repository/binding_test.go`
- `backend/internal/backupasset/repository/connect_test.go`
- `backend/internal/backupasset/repository/query_test.go`
- `backend/internal/backupasset/repository/reconcile_test.go`
- `backend/internal/fileaccess/contracts.go` — roots, locators, policies, entries, pages, handles, backend interfaces.
- `backend/internal/fileaccess/path.go` — strict/legacy canonicalization and root containment helpers.
- `backend/internal/fileaccess/local.go` — `os.Root` local list/lstat/open and bounded page selection.
- `backend/internal/fileaccess/local_linux.go` — strict `openat2` regular-file/directory open.
- `backend/internal/fileaccess/local_other.go` — strict Provider capability failure on unsupported platforms.
- `backend/internal/fileaccess/sftp.go` — pre/open/post checked SFTP stat/open and injected bounded enumeration.
- `backend/internal/fileaccess/contracts_test.go`
- `backend/internal/fileaccess/path_test.go`
- `backend/internal/fileaccess/local_test.go`
- `backend/internal/fileaccess/sftp_test.go`
- `backend/internal/sshutil/node_dialer.go` — DB-aware purpose-scoped Node dial and credential audit context.
- `backend/internal/sshutil/command_runner.go` — bounded SSH argv serialization, secret stdin, stream/result lifecycle.
- `backend/internal/sshutil/node_dialer_test.go`
- `backend/internal/sshutil/command_runner_test.go`
- `backend/internal/api/handlers/backup_repository_handler.go`
- `backend/internal/api/handlers/backup_repository_handler_test.go`
- `backend/internal/api/backup_asset_rbac_test.go`

### Modify

- `backend/go.mod`, `backend/go.sum` — promote the already-present `golang.org/x/sys` dependency to direct use if `go mod tidy` requires it.
- `backend/internal/backupasset/domain.go`, `domain_test.go` — safe Provider reason codes and validation.
- `backend/internal/backupasset/service.go`, `service_test.go` — repository-aware RecoveryPoint DTO mapping and Provider runtime settings.
- `backend/internal/settings/service.go`, `service_test.go` — three Provider limit settings.
- `backend/internal/sshutil/scope.go`, `scope_test.go` — three Repository purposes.
- `backend/internal/sshutil/ssh_auth.go`, `ssh_auth_test.go` — cancellable SSH handshake.
- `backend/internal/task/executor/ssh_connect.go`, `ssh_connect_test.go` — compatibility wrapper over the lower dialer.
- `backend/internal/api/handlers/file_handler.go`, `file_handler_validate_test.go` — shared operation-coupled file access.
- `backend/internal/api/handlers/response.go`, `response_test.go`, `response_helper_usage_test.go` — named typed capability response helper and new handler coverage.
- `backend/internal/api/router.go`, `router_test.go` — construct/wire the service and register five secured routes.
- `backend/internal/api/docs/docs.go` — generated Swagger route contracts.
- `backend/README_backend.md` — current maintainer route reference for the five feature-gated Repository APIs.
- `docs/env-vars.md` — Provider limit settings and read location.
- `.trellis/spec/backend/directory-structure.md` — new package boundaries.
- `.trellis/spec/backend/error-handling.md` — typed backup Provider response mapping.
- `.trellis/spec/backend/quality-guidelines.md` — executable Provider read/safe-open contract.
- This task's `task.json`, `prd.md`, `design.md`, and `implement.md` as Trellis records require.

### Explicitly forbidden from the diff

- `backend/internal/database/migrations/**`
- `web/**`
- `deploy/**`
- existing Restic/Rsync/Rclone backup, restore, retention, delete, init, or publication behavior except the thin SSH compatibility wrapper
- feature defaults or public README claims

## 2. Activation And Pre-Development Gate

- [ ] **Step 1: Reconfirm the reviewed branch and planning-only diff**

Run:

```bash
git status --short --branch
git rev-parse HEAD
git rev-parse origin/main
python3 ./.trellis/scripts/task.py validate 07-13-backup-assets-provider-readers
```

Expected: current branch is `codex/backup-assets-provider-readers`; `HEAD` and the recorded base are `8317fe6e04ea9ec1480460074ce9fe99ae8a7626`; only the parent link and Child 2 planning artifacts are dirty; task validation succeeds.

- [ ] **Step 2: Activate only after explicit user implementation approval**

Run:

```bash
python3 ./.trellis/scripts/task.py start 07-13-backup-assets-provider-readers
python3 ./.trellis/scripts/task.py current --source
```

Expected: task status changes from `planning` to `in_progress`; the active task resolves to `.trellis/tasks/07-13-backup-assets-provider-readers`.

- [ ] **Step 3: Load implementation rules**

Invoke `trellis-before-dev` for backend domain, filesystem/SSH, API handler, database transaction, logging/error, and cross-layer guide scopes. Read every selected spec completely before editing product code.

- [ ] **Step 4: Establish a green baseline**

Run:

```bash
cd backend && go test ./internal/backupasset ./internal/sshutil ./internal/task/executor ./internal/api/handlers ./internal/api -count=1
```

Expected: all selected baseline packages pass before red tests are added.

## 3. Foundation Contract Corrections And Runtime Settings

**Files:** modify `backend/internal/backupasset/{domain,service}.go`, their tests, `backend/internal/settings/service.go`, its test, and `docs/env-vars.md`.

- [ ] **Step 1: Write failing capability, DTO, and setting tests**

Add tests with these exact behaviors:

```go
func TestProviderCapabilityReasonCodesAreValidated(t *testing.T) {
	codes := []CapabilityCode{
		CapabilityRepositoryIdentityUnavailable,
		CapabilityProviderProtocolIncompatible,
		CapabilityProviderOperationTimeout,
		CapabilityProviderResourceLimit,
	}
	for _, code := range codes {
		if err := ValidateCapabilityReason(CapabilityReason{Code: code}); err != nil {
			t.Fatalf("code %q rejected: %v", code, err)
		}
	}
}

func TestToRecoveryPointDTOUsesOwningRepositoryVersionMode(t *testing.T) {
	record, repository := validRecoveryPointAndRepositoryForTest(VersionVersionedPrefix, PointXirangManifest)
	if _, err := ToRecoveryPointDTO(record, VersionMode(repository.VersionMode)); err != nil {
		t.Fatalf("versioned-prefix recovery point rejected: %v", err)
	}
	if _, err := ToRecoveryPointDTO(record, VersionHardlinkTree); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("mismatched repository version got %v", err)
	}
}

func TestFoundationServiceProviderConfig(t *testing.T) {
	service := NewFoundationService(mapSettingsReader{
		"backup_assets.provider_operation_timeout": "3m",
		"backup_assets.provider_max_concurrency": "6",
		"backup_assets.provider_metadata_limit_bytes": "8388608",
	})
	config, err := service.ProviderConfig()
	if err != nil || config.OperationTimeout != 3*time.Minute || config.MaxConcurrency != 6 || config.MetadataLimitBytes != 8<<20 {
		t.Fatalf("ProviderConfig=%+v err=%v", config, err)
	}
}
```

Add `TestTaskCapabilitiesDoNotClaimUnprovenRange`: Restic, Rclone, and Rsync task-level baseline capability sets must have `OpenRange=false`; only a concrete Provider observation may enable Range for its active binding.

Extend the settings registry exact-definition test for:

```text
backup_assets.provider_operation_timeout / BACKUP_ASSETS_PROVIDER_OPERATION_TIMEOUT / 2m / 5s..30m
backup_assets.provider_max_concurrency / BACKUP_ASSETS_PROVIDER_MAX_CONCURRENCY / 4 / 1..32
backup_assets.provider_metadata_limit_bytes / BACKUP_ASSETS_PROVIDER_METADATA_LIMIT_BYTES / 16777216 / 65536..67108864
```

- [ ] **Step 2: Run the focused tests and verify red state**

Run:

```bash
cd backend && go test ./internal/backupasset ./internal/settings -run 'Test(ProviderCapabilityReasonCodes|ToRecoveryPointDTOUses|FoundationServiceProviderConfig|BackupAssetFoundation)' -count=1
```

Expected: compile/test failure because the four codes, two-argument mapper, three settings, and `ProviderConfig` do not exist; the new Range assertion also fails against the current theoretical defaults.

- [ ] **Step 3: Implement the frozen contract**

Add the exact typed config and reason values:

```go
const (
	CapabilityRepositoryIdentityUnavailable CapabilityCode = "repository_identity_unavailable"
	CapabilityProviderProtocolIncompatible  CapabilityCode = "provider_protocol_incompatible"
	CapabilityProviderOperationTimeout       CapabilityCode = "provider_operation_timeout"
	CapabilityProviderResourceLimit          CapabilityCode = "provider_resource_limit"
)

type ProviderConfig struct {
	OperationTimeout   time.Duration
	MaxConcurrency     int
	MetadataLimitBytes int64
}
```

Change the DTO boundary to `func ToRecoveryPointDTO(record model.RecoveryPoint, repositoryVersion VersionMode) (RecoveryPointDTO, error)` and validate the complete `RecoveryPointProfile` with the supplied owning mode. Remove `inferredVersionMode`; no caller may guess mode from semantics. Change task-level baseline mapping so Restic/Rclone/Rsync never claim Range before a concrete active-binding probe; effective Repository observations are the only source that may enable it.

Implement `FoundationService.ProviderConfig()` by reading all three keys, calling the central setting validators, parsing duration/integers, and wrapping invalid values with `ErrInvalidState`. Add the three registry definitions to `ValidateBackupAssetFoundationConfig`'s required-key validation and env-var documentation while keeping `backup_assets.enabled=false`.

- [ ] **Step 4: Run focused tests**

Run:

```bash
cd backend && gofmt -w internal/backupasset/domain.go internal/backupasset/domain_test.go internal/backupasset/service.go internal/backupasset/service_test.go internal/settings/service.go internal/settings/service_test.go && go test ./internal/backupasset ./internal/settings -count=1
```

Expected: both packages pass; no existing Child 1 state/DTO/settings test regresses.

## 4. Provider Core Ports, Identity, Registry, And Cursor

**Files:** create the Provider core files and tests listed in the manifest.

- [ ] **Step 1: Write failing port and registry tests**

Define compile-time assertions and behavior tests around these public shapes:

```go
var _ RepositoryProber = (*fakeProvider)(nil)
var _ PointLister = (*fakeProvider)(nil)
var _ EntryLister = (*fakeProvider)(nil)
var _ EntryStatter = (*fakeProvider)(nil)
var _ SequentialReader = (*fakeProvider)(nil)

func TestRegistryReturnsTypedMissingCapability(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.RangeReader(backupasset.ProviderRestic)
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("missing Range error=%v", err)
	}
}

func TestRegistryRejectsDuplicateRegistration(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(backupasset.ProviderRestic, Registration{Prober: &fakeProvider{}}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("duplicate registration=%v", err)
	}
}
```

Test Command and an unknown Provider as unsupported, nil ports as rejected registration, and getters returning only the requested narrow interface.

- [ ] **Step 2: Write failing identity tests**

Cover native Restic identity and scoped HMAC behavior:

```go
func TestScopedEndpointIdentityIsTaskBoundAndDomainSeparated(t *testing.T) {
	salt := bytes.Repeat([]byte{0x42}, IdentitySaltBytes)
	doc := ScopedIdentityDocument{Provider: backupasset.ProviderRsync, TaskID: 7, NodeID: 9, EndpointFacts: []string{"root-fact"}}
	identity, err := DeriveScopedIdentity(salt, doc)
	if err != nil {
		t.Fatal(err)
	}
	otherTask := doc
	otherTask.TaskID = 8
	other, _ := DeriveScopedIdentity(salt, otherTask)
	fingerprint, _ := DeriveConfigFingerprint(salt, []byte("canonical-binding"))
	if identity == other || strings.TrimPrefix(identity, ScopedIdentityPrefix) == fingerprint {
		t.Fatal("identity must be task-scoped and config-domain-separated")
	}
}
```

Also assert canonical document ordering, duplicate/empty fact rejection, 32-byte salt enforcement, Restic native ID validation, and that raw endpoint facts never appear in identity/fingerprint strings.

- [ ] **Step 3: Write failing cursor tests**

Use deterministic cursor keys and clock. Assert valid round-trip, key version, expiry, repository/point/parent/capability/source scope, tamper rejection, and raw path/name/remote absence from the encoded token.

The cursor payload type must be exactly:

```go
type CursorScope struct {
	Provider           backupasset.ProviderKind
	RepositoryID       string
	PointScopeDigest   string
	ParentScopeDigest  string
	CapabilityRevision int
	SourceRevision     string
	LastItemDigest     string
	Direction          string
}
```

- [ ] **Step 4: Run tests and verify red state**

Run:

```bash
cd backend && go test ./internal/backupasset/provider -run 'Test(Registry|ScopedEndpointIdentity|NativeRepositoryIdentity|Cursor)' -count=1
```

Expected: compile failure because the package and types do not exist.

- [ ] **Step 5: Implement core contracts and registry**

Create exact narrow interfaces from `design.md` plus:

```go
type PageRequest struct {
	Limit  int
	Cursor string
}

type OperationLimits struct {
	Timeout           time.Duration
	MaxMetadataBytes  int64
	MaxStderrBytes    int64
	MaxRecordBytes    int
	MaxItems          int
}

type Registration struct {
	Prober           RepositoryProber
	PointLister      PointLister
	EntryLister      EntryLister
	EntryStatter     EntryStatter
	SequentialReader SequentialReader
	RangeReader      RangeReader
}

type ReadRequest struct {
	MaxBytes int64
}
```

The registry validates known Provider kind, non-nil prober, duplicate registration, and one getter per port. Define future mutation interfaces but omit them from `Registration` so Child 2 cannot reach them.

- [ ] **Step 6: Implement identities and cursor codec**

Use `crypto/rand` for 32-byte salts and domain-separated HMAC-SHA-256 labels `repository-identity:v1` and `repository-config:v1`. Native Restic identity is `restic-native:v1:<64-lower-hex-id>`; scoped identity is `<provider>-task-endpoint:v1:<64-lower-hex-hmac>`.

Use the Child 1 Cursor Signing key for HMAC authentication. Tokens serialize only `CursorScope`, key version, issued/expiry times; reject unknown fields and trailing JSON. Clamp page limit to the service-provided maximum before calling an adapter.

- [ ] **Step 7: Run the complete Provider core tests**

Run:

```bash
cd backend && gofmt -w internal/backupasset/provider && go test ./internal/backupasset/provider -run 'Test(Registry|ScopedEndpointIdentity|NativeRepositoryIdentity|Cursor|Contracts)' -count=1
```

Expected: all core tests pass; `go list -deps ./internal/backupasset/provider` contains no handler or task/executor package.

## 5. Purpose-Aware SSH Dial And Bounded Command Runner

**Files:** create `sshutil/{node_dialer,command_runner}.go` and tests; modify scope/auth and executor wrapper files.

- [ ] **Step 1: Write failing SSH purpose tests**

Extend `scope_test.go` to assert all three new values normalize and remain independent:

```go
purposes := []string{PurposeRepositoryProbe, PurposeRepositoryList, PurposeRepositoryRead}
for _, allowed := range purposes {
	key := model.SSHKey{AllowedPurposes: allowed}
	for _, requested := range purposes {
		err := ValidateSSHKeyScope(key, model.Node{ID: 1}, requested)
		if (allowed == requested) != (err == nil) {
			t.Fatalf("allowed=%s requested=%s err=%v", allowed, requested, err)
		}
	}
}
```

Retain disabled/expired/node/tag and empty-scope compatibility assertions.

- [ ] **Step 2: Write failing NodeDialer tests**

Use injected auth/host-key/network functions. Assert:

- DB-backed managed key resolution works without preloading `Node.SSHKey`;
- wrong purpose is blocked before network dial and before `last_used_at` mutation;
- password/private-key inline credentials keep exact safe kind/source;
- host-key and dial failures write the exact sanitized credential action for the operation: `repository.probe`, `repository.list`, or `repository.read`, with correlation ID;
- no password/private key/host/path appears in returned error or audit metadata;
- successful and failed handshakes close the raw connection on context cancellation.

- [ ] **Step 3: Write failing bounded runner tests**

Create fake SSH sessions and assert:

```go
func TestCommandRunnerRejectsRawShellAndLimitsOutput(t *testing.T) {
	runner := newFakeBoundedRunner(t)
	_, err := runner.Run(context.Background(), CommandSpec{Binary: "sh -c", Args: []string{"whoami"}})
	if !errors.Is(err, ErrUnsafeCommandSpec) {
		t.Fatalf("raw shell accepted: %v", err)
	}
	runner.stdout = bytes.Repeat([]byte("x"), 1025)
	_, err = runner.Run(context.Background(), CommandSpec{Binary: "find", Args: []string{"/safe"}, MaxStdoutBytes: 1024})
	if !errors.Is(err, ErrCommandOutputLimit) {
		t.Fatalf("oversize output=%v", err)
	}
}
```

Also cover per-operand quoting, leading-dash/space/newline operands, separate stderr cap, oversized single record, secret stdin one-write/close/redaction, context timeout, TERM-ignore forced session close, stream close, and goroutine completion.

- [ ] **Step 4: Run tests and verify red state**

Run:

```bash
cd backend && go test ./internal/sshutil ./internal/task/executor -run 'Test(RepositoryPurpose|NodeDialer|CommandRunner|DialSSHHandshake)' -count=1
```

Expected: compile/test failure for new purposes, dialer, runner, and handshake cancellation.

- [ ] **Step 5: Implement scope and cancellable handshake**

Add exact constants:

```go
const (
	PurposeRepositoryProbe = "repository_probe"
	PurposeRepositoryList  = "repository_list"
	PurposeRepositoryRead  = "repository_read"
)
```

During `DialSSH`, connect context cancellation to raw connection closure across `ssh.NewClientConn`; set a deadline from context/timeout before handshake and clear it on success. Return only sanitized stage-classified errors.

- [ ] **Step 6: Implement NodeDialer and executor compatibility wrapper**

`NodeDialer.Dial` must accept `*gorm.DB`, `model.Node`, purpose, and this safe audit context:

```go
type DialAuditContext struct {
	Action        string
	CorrelationID string
	UserID        uint
	Username      string
	Role          string
	TaskID        *uint
}
```

It calls `BuildSSHAuthForPurpose(node, db, purpose)`, known-host resolution, cancellable dial, and `credentialaudit.Write`. It stores only stage, latency, correlation ID, safe credential metadata, Node/Task IDs, and outcome.

Make `executor.DialSSHForNodePurpose` delegate to the same low-level mechanics while retaining its signature, task action, context-propagated audit, messages, and existing tests.

- [ ] **Step 7: Implement the bounded command runner**

Accept only a validated binary basename/absolute configured binary, separately supplied args, fixed safe environment labels, limits, and optional `SecretStdin`. Serialize remote operands through one shell-quote function; never accept a command template. Implement buffered metadata and streaming result modes with shared semaphore, cancellation, output accounting, bounded termination, and `Wait/Close` error propagation.

- [ ] **Step 8: Run focused and regression tests**

Run:

```bash
cd backend && gofmt -w internal/sshutil internal/task/executor/ssh_connect.go internal/task/executor/ssh_connect_test.go && go test ./internal/sshutil ./internal/task/executor -count=1
```

Expected: all SSH/executor tests pass; existing Task credential actions remain `task.credential.use`.

## 6. Local Operation-Coupled File Access

**Files:** create `fileaccess/contracts.go`, `path.go`, `local*.go`, and tests; update `backend/go.mod` only if direct `x/sys` use changes module classification.

- [ ] **Step 1: Write strict/legacy path tests**

Table-test empty, NUL, absolute, Windows-volume, `.`, any `..` segment, duplicate separator, invalid UTF-8, root `/`, trailing slash, `/backup` versus `/backup-evil`, root symlink, internal symlink, dangling symlink, and escape symlink.

Use exact policies:

```go
var ProviderPolicy = Policy{Input: StrictRelativeLocator, Symlinks: NeverFollow, OpenTypes: RegularFilesOnly}
var LegacyPolicy = Policy{Input: LegacyAbsoluteOrRelative, Symlinks: FollowOnlyWithinRoot, OpenTypes: RegularFilesOnly}
```

- [ ] **Step 2: Write local race/type/page tests**

Add deterministic hooks around resolve/open/post-check. Assert:

- missing candidate replaced by escape symlink is rejected;
- parent replaced between resolve/open is rejected or remains bound beneath original root;
- root rename keeps the opened `os.Root` bound to the original directory;
- FIFO, socket, and device are typed `special` and `OpenRegular` rejects them;
- symlink can be listed but strict policy cannot traverse/open it;
- list selection returns the lexicographically next `limit+1` entries using bounded memory and an opaque last-item digest;
- cancellation closes handles;
- Range reads return exact bytes only for regular files and source metadata mismatch fails.

- [ ] **Step 3: Run tests and verify red state**

Run:

```bash
cd backend && go test ./internal/fileaccess -run 'Test(Path|Local|Strict|Legacy)' -count=1
```

Expected: compile failure because `fileaccess` does not exist.

- [ ] **Step 4: Implement contracts and path normalization**

Define `Root`, `Locator`, `Policy`, `Entry`, `EntryPage`, `ContentStat`, `ByteRange`, `ReadHandle`, and `Tree`. `ReadHandle.Close()` must return final invariant/cancellation errors. Root collection remains a caller responsibility.

- [ ] **Step 5: Implement contained local operations**

The local root wrapper holds `os.OpenRoot` for the legacy contained policy. On Linux it separately opens the canonical root as read-only `O_PATH|O_DIRECTORY|O_CLOEXEC` and uses that dirfd with `unix.Openat2`, `RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_SYMLINKS`, read-only flags, and handle-based `fstat` for strict Provider operations. Do not attempt to access `os.Root`'s unexported descriptor. Non-Linux strict Provider methods return a typed unsupported error; legacy contained access continues through `os.Root`.

List scans under a byte/item/time cap and keeps only the next page candidates rather than accumulating the full directory. Every stat/open result is derived from the opened handle, not a prevalidated string.

- [ ] **Step 6: Format, tidy, and run tests**

Run:

```bash
cd backend && gofmt -w internal/fileaccess && go mod tidy && go test ./internal/fileaccess -count=1 && go test ./internal/backupasset ./internal/sshutil -count=1
```

Expected: fileaccess and affected dependency packages pass; no unrelated module dependency appears.

## 7. SFTP Safe Access And Legacy File Handler Migration

**Files:** create `fileaccess/sftp.go` and test; modify file handler and validation tests.

- [ ] **Step 1: Write failing SFTP operation tests**

Use fake SFTP and directory-enumerator interfaces. Cover:

- canonical root and strict relative path;
- static internal/escape symlinks under both policies;
- pre-open and post-open path/type/source changes;
- regular/symlink/special typing;
- exact Range via `ReadAt`/`Seek` only after probe;
- enumerator byte/item/time overflow;
- unsupported strict enumerator with no fallback to `ReadDirContext`;
- cancel closing file, SFTP client, session, and SSH client.

- [ ] **Step 2: Write failing legacy handler compatibility tests**

Retain every existing path validation assertion and add:

```go
func TestFileHandlerUsesSharedFileAccessAndFailsOnRootQueryError(t *testing.T) {
	handler, db := newFileHandlerHarness(t)
	installTaskRootQueryFailure(t, db)
	response := performNodeFileList(t, handler, 1, "/allowed")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", response.Code)
	}
}
```

Also assert Provider policy never sees `FILE_BROWSER_ALLOW_ALL`, absolute/default legacy paths remain accepted, root-internal symlink remains allowed, special-file preview is rejected, list caps apply during enumeration, and existing `file_browser.list|preview` credential audit remains secret-free.

- [ ] **Step 3: Run tests and verify red state**

Run:

```bash
cd backend && go test ./internal/fileaccess ./internal/api/handlers -run 'Test(SFTP|FileHandler|ValidateNodePath|ValidateLocalPath)' -count=1
```

Expected: new tests fail because SFTP operation coupling and handler injection do not exist.

- [ ] **Step 4: Implement SFTP Tree**

`SFTPTree` accepts an SFTP stat/open backend plus a bounded directory enumerator. Strict Provider enumeration uses the common remote runner with fixed read-only `find <absolute-directory> -mindepth 1 -maxdepth 1 -print0`; it reads under the Provider metadata limit, computes safe relative candidates, and performs SFTP `Lstat` only for the selected page. If the server lacks the proven enumerator, strict list is unavailable.

Perform root/path `RealPath` and `Lstat` before opening, open the handle, then repeat containment/type/source checks. Return the opened handle only after the second check. Document in code that verified SSH server behavior is trusted.

- [ ] **Step 5: Migrate the handler**

Replace `validateNodePath`/`validateLocalPath` check-then-use calls with `fileaccess.Tree.List/Lstat/OpenRegular`. Move DB root lookup into a checked handler helper that returns `Node.BasePath` plus Task source roots. Keep the development bypass in the handler adapter only. Do not change response envelopes, size limits, or audit action names.

- [ ] **Step 6: Run focused and full handler tests**

Run:

```bash
cd backend && gofmt -w internal/fileaccess internal/api/handlers/file_handler.go internal/api/handlers/file_handler_validate_test.go && go test ./internal/fileaccess ./internal/api/handlers -count=1
```

Expected: all new safe-access and existing handler tests pass.

## 8. Restic Read Adapter

**Files:** create `provider/restic.go`, `restic_test.go`, and extend Provider test utilities.

- [ ] **Step 1: Write the executable-fake contract tests**

Use the Go helper-process pattern: launch the current test binary with `GO_WANT_RESTIC_PROVIDER_HELPER=1`; the helper records argv, reads secret stdin, and emits selected fixtures. Cover:

- `version` and `cat config` with valid 64-hex repository ID/version;
- invalid/missing/trailing config fields and identity mismatch;
- shared-repository snapshots with full IDs, duplicate times, tags/hosts/paths;
- short/prefix/latest locator rejection before execution;
- strict streaming snapshot array and `ls --json` header/entry records;
- malformed, unknown, truncated, oversized, or trailing records failing without partial success;
- exact Unicode/newline/leading-dash path operand serialization;
- binary `dump` stream and close/wait behavior;
- timeout, cancellation, stdout/stderr/record caps;
- password observed only on stdin and absent from argv/result/error/audit;
- command history containing no `latest`, `init`, `backup`, `restore`, `forget`, `prune`, `unlock`, or delete command.

- [ ] **Step 2: Run tests and verify red state**

Run:

```bash
cd backend && go test ./internal/backupasset/provider -run 'TestRestic' -count=1
```

Expected: failure because the adapter does not exist.

- [ ] **Step 3: Implement probe and strict parsers**

Construct only server-owned Restic commands. Use `--password-file /dev/stdin`, send the bounded password once through `SecretStdin`, close stdin, and reject runtimes that cannot prove this path. Parse `cat config` with `json.Decoder`, `DisallowUnknownFields` only for the owned config envelope where compatible, explicit required fields, and trailing-token rejection.

Stream snapshot arrays and NDJSON through byte/record/item budgets. Validate full snapshot IDs and known record types. Never use existing executor `ListSnapshots/ListFiles/RestoreFiles`.

- [ ] **Step 4: Implement point/entry/stat/sequential ports**

Return native points and entries with internal `json:"-"` locators, stable opaque digests, exact metadata, next cursor, and safe fidelity. `OpenSequential` rejects a non-positive `ReadRequest.MaxBytes`, starts `restic dump` for one full snapshot/path, enforces the requested streaming ceiling without buffering content, and returns a handle whose `Close` releases concurrency, joins the remote command, and reports limit/cancellation/protocol failure. Do not implement `RangeReader`.

- [ ] **Step 5: Run Restic and negative reachability tests**

Run:

```bash
cd backend && gofmt -w internal/backupasset/provider/restic.go internal/backupasset/provider/restic_test.go && go test ./internal/backupasset/provider -run 'TestRestic' -count=1 && rg -n 'latest|init|backup|restore|forget|prune|unlock' internal/backupasset/provider/restic.go
```

Expected: Restic tests pass; source search matches only explicit deny/allowlist validation or comments/tests, not executable mutation specs.

## 9. Rsync Mutable Tree Adapter

**Files:** create `provider/rsync.go`, `rsync_test.go`.

- [ ] **Step 1: Write failing Rsync adapter tests**

Cover local and SFTP-backed roots:

- task-scoped identity salt stability and different Task isolation;
- local/remote canonical root facts and unavailable root;
- exactly one mutable native point;
- bounded list/stat and sequential read with exact positive `ReadRequest.MaxBytes` enforcement;
- local and SFTP Range enabled only after transport probe proves exact seek/read-at;
- source fingerprint/capability revision mismatch before operation;
- root/entry change during operation returning `CapabilityMutableSourceChanged`;
- symlink/special open denial;
- cancellation/limit/offline mapping;
- a spy proving no call to Rsync executor, mkdir, write-check, restore, or shell mutation.

- [ ] **Step 2: Run tests and verify red state**

Run:

```bash
cd backend && go test ./internal/backupasset/provider -run 'TestRsync' -count=1
```

Expected: failure because the Rsync adapter does not exist.

- [ ] **Step 3: Implement probe and the mutable point**

Probe only through `fileaccess`. Produce `task_scoped_endpoint` identity from the supplied salt/document, capability revision, transport facts, physical availability, and an explicitly weak mutable source fingerprint. Return one native point whose locator binds Repository ID and expected source fingerprint; never synthesize history.

- [ ] **Step 4: Implement read ports**

Delegate list/lstat/open to strict Provider `fileaccess` policy. Wrap handles so final source/type checks are honored. The adapter statically implements `RangeReader`, but the binding's effective capability remains false until its concrete transport probe passes; `OpenRange` rejects a false binding capability before I/O. Restic remains the Provider with no registered Range port.

- [ ] **Step 5: Run Rsync/fileaccess tests**

Run:

```bash
cd backend && gofmt -w internal/backupasset/provider/rsync.go internal/backupasset/provider/rsync_test.go && go test ./internal/backupasset/provider ./internal/fileaccess -run 'Test(Rsync|Local|SFTP)' -count=1
```

Expected: all selected tests pass and no Provider byte was created or changed.

## 10. Rclone Mutable Remote Adapter

**Files:** create `provider/rclone.go`, `rclone_test.go`.

- [ ] **Step 1: Write executable-fake Rclone tests**

Use a helper process and fake remote facts. Cover:

- task-scoped identity from Node/remote/backend/root facts without raw remote leakage;
- `version` and read-only backend fact probe;
- `lsjson --max-depth 1`, `lsjson --stat`, and `cat` for empty/file/directory/object results, including exact positive `ReadRequest.MaxBytes` enforcement;
- missing/weak hashes and mtime precision retained as weak fidelity;
- valid Unicode/leading-dash/shell-meta remote paths passed as one quoted operand;
- Range default false, exact offset/count proof enabling it, incorrect/ignored/unsupported semantics keeping it false;
- object/source change between pre/post observation;
- timeout/cancel/output/item/protocol errors with no partial success;
- command history containing no `sync`, `copy`, `move`, `delete`, `purge`, `mkdir`, `rmdir`, `cleanup`, or mutating backend command.

- [ ] **Step 2: Run tests and verify red state**

Run:

```bash
cd backend && go test ./internal/backupasset/provider -run 'TestRclone' -count=1
```

Expected: failure because the Rclone adapter does not exist.

- [ ] **Step 3: Implement probe/list/stat/sequential read**

Build bound remote operands exclusively from encrypted runtime access. Strictly parse bounded `lsjson` arrays and safe backend facts. Return one mutable point, typed entries, weak/strong metadata facts, and `cat` stream handles. Never copy raw Rclone errors or output into public errors/logs.

- [ ] **Step 4: Implement runtime Range proof**

Select at most one bounded regular object candidate from a read-only list. Compare a small sequential slice with exact positive `--offset/--count` reads under the operation timeout and byte cap. Enable Range only on exact equality and stable pre/post stat; empty/unprovable/slow/error cases remain sequential-only without failing the whole repository probe.

- [ ] **Step 5: Run Rclone and command-deny tests**

Run:

```bash
cd backend && gofmt -w internal/backupasset/provider/rclone.go internal/backupasset/provider/rclone_test.go && go test ./internal/backupasset/provider -run 'TestRclone' -count=1 && rg -n 'sync|copy|move|delete|purge|mkdir|rmdir|cleanup' internal/backupasset/provider/rclone.go
```

Expected: tests pass; source matches are denylist/validation text only, not executable mutation specs.

## 11. Repository Binding And Probe-First Connect Service

**Files:** create repository service/binding/connect/error/audit files and tests.

- [ ] **Step 1: Write versioned binding tests**

Assert Task mapping for Rsync/Restic/Rclone, rejection of Command/unknown type, required Task/Node/root/config fields, JSON schema/trailing-data rejection, scoped salt persistence, native identity behavior, and public JSON exclusion. Persist/reload through GORM and assert encrypted-at-rest values begin with the secure envelope while loaded values reconstruct runtime access.

Test that plaintext secret fields are never written through `Updates(map[string]any)` and that config fingerprints contain no raw path/remote/password.

- [ ] **Step 2: Write connect service tests before implementation**

Cover exact sequence and side effects:

- feature disabled invokes zero DB/provider/audit dependencies;
- probe failure writes no Repository/binding/link/RP;
- first Rsync/Rclone connect creates Repository, active binding, active link, and one observed mutable head;
- retry reuses the same IDs;
- same Task/different identity returns `ErrConflict` and preserves the old rows;
- Restic Tasks with the same native ID reuse one Repository but create separate links and no RecoveryPoint;
- Rsync/Rclone different Tasks do not merge even when endpoint facts match;
- existing active binding remains unless `replace_access=true` and the supplied Repository identity matches;
- replacement revokes exactly one old binding and creates one active binding in one transaction;
- uniqueness races reload/revalidate the winner;
- GORM insert/update failure rolls back every row.

The disabled test spy must fail the test if any `Task`, keyring, audit, or Provider method is touched.

- [ ] **Step 3: Run tests and verify red state**

Run:

```bash
cd backend && go test ./internal/backupasset/repository -run 'Test(Binding|Connect)' -count=1
```

Expected: compile failure because the repository package does not exist.

- [ ] **Step 4: Implement Service dependencies and binding codec**

Use explicit dependencies:

```go
type Dependencies struct {
	DB         *gorm.DB
	Foundation *backupasset.FoundationService
	Registry   *provider.Registry
	Keyring    *backupasset.Keyring
	Now        func() time.Time
	Audit      AssetAuditSink
}
```

`Service.ensureEnabled()` is the first line of every public method. Binding JSON is version `1`, uses strict decoding, and never implements `json.Marshaler` for public use.

- [ ] **Step 5: Implement probe-first connect**

Perform Task/Node/existing-link resolution, ephemeral access construction, and Provider probe outside the write transaction. Inside one GORM transaction, implement exact identity resolution, link/binding constraints, Child 1 state validation, mutable singleton creation/update, and safe timestamps. Classify SQLite/PostgreSQL unique errors only for the known identity/active-binding/active-link/mutable-head indexes, then reload and revalidate.

- [ ] **Step 6: Implement correlated audit**

Create one request correlation context consumed by Provider NodeDialer credential events and typed asset events. Connect uses `AuditActionRepositoryConnect`; safe fields are stage/status/code/correlation/repository/task IDs only. Audit failure logs one bounded warning and leaves the already-decided primary result unchanged.

- [ ] **Step 7: Run connect and Child 1 regressions**

Run:

```bash
cd backend && gofmt -w internal/backupasset/repository && go test ./internal/backupasset/repository ./internal/backupasset -run 'Test(Binding|Connect|RepositoryState|MutableHead|SanitizedDTO)' -count=1
```

Expected: tests pass; a database query confirms no extra tables/migrations were introduced.

## 12. Repository Visibility, Reconcile, And Disconnect

**Files:** create repository query/reconcile tests and implementations.

- [ ] **Step 1: Write lineage visibility tests**

Seed Admin, Viewer, Operators with owned/unowned Nodes, a shared Restic Repository with mixed links/points, task-deleted lineage, and an unattributed point. Assert:

- Admin sees all safe Repository/link metadata;
- Operator list includes only repositories with at least one non-null lineage joined through a live Task to an owned current Node;
- Operator detail returns only owned safe lineage summaries and no cross-lineage counts/evidence;
- owning one link does not reveal another Task's point IDs, names, counts, capability evidence, or existence;
- unowned and missing opaque IDs both return `ErrNotFound`;
- deleted/unattributed lineage is Admin-only;
- DB/unknown-role resolution fails closed, never unscoped.

- [ ] **Step 2: Write reconcile/disconnect tests**

Cover:

- successful mutable reconcile keeps the RecoveryPoint ID/state and updates observation/fingerprint/capability revision atomically;
- unchanged capabilities keep revision; changed effective capabilities increment it;
- failed probe preserves last successful fingerprint/observed time and only changes Repository/physical availability plus safe reason;
- identity mismatch never updates the Repository;
- Restic reconcile updates Repository facts but creates no point;
- disconnect revokes binding, sets Repository disconnected, leaves observed point offline with prior fingerprint/time, and preserves links/Catalog/provider locator/bytes;
- reconnect requires same identity and retained salt;
- disabled mode has zero side effects.

- [ ] **Step 3: Run tests and verify red state**

Run:

```bash
cd backend && go test ./internal/backupasset/repository -run 'Test(Query|Visibility|Reconcile|Disconnect)' -count=1
```

Expected: tests fail because query/reconcile methods are not implemented.

- [ ] **Step 4: Implement list/detail visibility queries**

Define `VisibilityScope{Role string; UserID uint}`. Query `node_owners` directly inside the service, never middleware. Admin paths are explicit; Operator paths join non-null active `task_repository_links.task_id` and non-null `recovery_points.producing_task_id` through live Tasks to their current Nodes before projection. Snapshot node IDs/names are never authorization inputs. Unknown/Viewer roles return `ErrForbidden` even if called outside the router.

Use cursor pagination on stable `(created_at,id)` Repository tuples. Operator authorization joins non-null active links and non-null `producing_task_id` values to live Tasks and their current owned Nodes; `node_id_snapshot` is display/history only. Build `RepositoryView` from `backupasset.RepositoryDTO` plus only authorized `LineageSummary` values and safe access status. Never serialize Repository identity or binding config.

- [ ] **Step 5: Implement reconcile/disconnect transactions**

Probe outside the transaction, compare exact identity, compute effective capability revision, and apply Repository/mutable fields in one transaction. On failure, execute the bounded failure-state transaction without clearing the last successful observation. Disconnect updates only binding status/revoked time, Repository state, and mutable availability; it never deletes.

- [ ] **Step 6: Run repository package tests repeatedly**

Run:

```bash
cd backend && gofmt -w internal/backupasset/repository && go test ./internal/backupasset/repository -count=5
```

Expected: all tests pass five times, including uniqueness/concurrency/state assertions.

## 13. HTTP Handler, RBAC, Typed Errors, Router Wiring, And Swagger

**Files:** create handler/RBAC tests and handler; modify response/router/docs files.

- [ ] **Step 1: Write handler unit tests**

Use an injected service interface and assert exact request/response behavior for:

- connect JSON with only `task_id`, optional opaque `repository_id`, labels, and `replace_access`;
- rejecting provider/path/remote/config/password/command fields via strict request decoding;
- list page limit/cursor and detail opaque ID;
- reconcile/disconnect empty-body enforcement;
- standard envelopes and localized messages;
- typed 400/404/409/501/503 mapping with safe `CapabilityReason`/correlation ID;
- generic 500 excluding raw DB/crypto/SSH/Provider error text;
- each method writing the correct typed asset action/stage;
- handler containing no GORM/provider command logic.

- [ ] **Step 2: Write full-router auth/RBAC tests**

Register the real middleware chain and assert all five routes:

| Caller | list/detail | connect/reconcile/disconnect |
|---|---|---|
| no token | 401 | 401 |
| Viewer | 403 | 403 |
| Operator with owned lineage | handler result | 403 |
| Operator without lineage | 404 | 403 |
| Admin | handler result | handler result |
| unknown/missing role | 403 | 403 |

Assert RBAC runs before feature gate/service invocation, route templates are unique, and no `/backup-repositories/probe` route exists.

- [ ] **Step 3: Run tests and verify red state**

Run:

```bash
cd backend && go test ./internal/api/handlers ./internal/api -run 'TestBackupRepositor|TestBackupAssetRBAC' -count=1
```

Expected: compile/test failure because handler/routes/helpers do not exist.

- [ ] **Step 4: Implement named response mapping**

Add one named typed capability-error helper that still emits the project `Response` envelope, accepts only status 501/503 plus a validated `backupasset.CapabilityReason` and correlation ID, and rejects any other status in tests. Continue using `respondConflict`, `respondNotFound`, and `respondInternalError` for their existing cases. Add response helper usage scanning for `backup_repository_handler.go`.

- [ ] **Step 5: Implement thin handler**

Define a service interface with `Connect`, `List`, `Detail`, `Reconcile`, and `Disconnect`. Each handler performs strict body/query/opaque-ID parsing, constructs actor/visibility/correlation context from Gin, calls one service method, maps one result, and returns. Swagger annotations describe only sanitized DTOs and the actual feature-gated routes.

- [ ] **Step 6: Wire the default service and routes**

In `api.NewRouter`, create one shared FoundationService, Keyring, purpose-aware NodeDialer, bounded command runner/semaphore, strict file trees, Provider adapters/registry, Repository service, and handler. Construction must not call `EnsureRequiredDomains`, decrypt data, dial, execute commands, or write DB while the feature is disabled; key/audit initialization occurs lazily after `ensureEnabled`.

Register the five routes under `secured` with exact Child 1 permissions. Do not add generic ownership middleware; the service performs shared-lineage filtering.

- [ ] **Step 7: Generate and verify Swagger**

Run:

```bash
make swag-init
git status --short backend/internal/api/docs backend/docs
```

Expected: tracked `backend/internal/api/docs/docs.go` changes; ignored local Swagger JSON/YAML may regenerate but must not be force-added. The five routes are present and `/backup-repositories/probe` is absent.

- [ ] **Step 8: Run handler/router tests**

Run:

```bash
cd backend && gofmt -w internal/api/handlers/backup_repository_handler.go internal/api/handlers/backup_repository_handler_test.go internal/api/handlers/response.go internal/api/handlers/response_test.go internal/api/router.go internal/api/router_test.go internal/api/backup_asset_rbac_test.go && go test ./internal/api/handlers ./internal/api -count=1
```

Expected: all handler/router tests pass.

## 14. Cross-Layer Security And Compatibility Verification

- [ ] **Step 1: Add cross-layer source-boundary tests**

Add tests/scans that fail if:

- Provider imports `api/handlers` or `task/executor`;
- repository service switches on executor type outside Task-to-binding mapping;
- handler imports GORM or contains Provider commands;
- Provider source includes executable mutation subcommands;
- Provider/DTO JSON exposes `EncryptedConfig`, identity salt, repository identity, path/remote/locator, argv, output, or credentials;
- disabled service touches DB/keyring/dialer/runner/audit;
- fileaccess queries DB or reads `FILE_BROWSER_ALLOW_ALL`;
- a migration/frontend/deploy path appears in the diff.

- [ ] **Step 2: Run secret and mutation negative scans**

Run:

```bash
rg -n 'repository_password|private_key|password|token|secret|provider_locator|executor_config|CombinedOutput' backend/internal/backupasset/provider backend/internal/backupasset/repository backend/internal/api/handlers/backup_repository_handler.go
rg -n 'init|backup|restore|sync|copy|move|delete|purge|forget|prune|unlock|mkdir|rmdir|cleanup' backend/internal/backupasset/provider
git diff --name-only | rg '^(backend/internal/database/migrations/|web/|deploy/)' && exit 1 || true
```

Expected: secret terms occur only in private binding/secret-stdin types and negative tests, never DTO/log/error/audit paths; mutation terms occur only in deny assertions/comments/tests; forbidden changed-path scan is empty.

- [ ] **Step 3: Run focused race/cancel/resource suites**

Run:

```bash
cd backend && go test ./internal/fileaccess ./internal/sshutil ./internal/backupasset/provider ./internal/backupasset/repository -run 'Test.*(Race|Cancel|Timeout|Limit|SourceChanged|Cursor|Identity)' -count=10
```

Expected: all selected tests pass ten times with no hang or goroutine leak.

- [ ] **Step 4: Run complete backend tests and build**

Run:

```bash
cd backend && go test ./... -count=1 && go build ./...
```

Expected: all backend packages pass and build.

- [ ] **Step 5: Run repository-wide quality gates**

Run:

```bash
make lint-backend
make check
git diff --check
```

Expected: backend lint, backend/frontend tests, builds, docs checks, migration checks, and diff whitespace checks all pass. Frontend is unchanged but full `make check` proves compatibility.

- [ ] **Step 6: Verify Swagger/doc/migration truth**

Run:

```bash
rg -n 'backup-repositories' backend/internal/api/docs/docs.go
rg -n 'BACKUP_ASSETS_PROVIDER_(OPERATION_TIMEOUT|MAX_CONCURRENCY|METADATA_LIMIT_BYTES)' docs/env-vars.md
git diff --name-only -- backend/internal/database/migrations
git diff --name-only -- web
```

Expected: five intended route blocks and three settings exist; migration and frontend outputs are empty.

## 15. Trellis Check, Spec Update, And Phase 3.4 Work Commit

- [x] **Step 1: Run `trellis-check` inline**

Load the `trellis-check` skill in the main session. Verify spec compliance, exact PRD/design coverage, tests, data flow, reuse, authorization, secret boundaries, UTC behavior, docs freshness, and no context drift. Fix findings on the same branch and rerun every affected gate.

- [x] **Step 2: Perform the implementation-plan self-review**

Map every PRD acceptance checkbox to a passing test/command. Search for planning placeholders and signature drift:

```bash
rg -n 'TBD|TODO|implement later|fill in|appropriate error|similar to' .trellis/tasks/07-13-backup-assets-provider-readers/{prd,design,implement}.md | rg -v 'rg -n'
rg -n 'type (RepositoryProber|PointLister|EntryLister|EntryStatter|SequentialReader|RangeReader)|func \(.*\) (Connect|List|Detail|Reconcile|Disconnect)' backend/internal/backupasset
```

Expected: placeholder scan is empty except quoted negative-review wording; type/method search shows one consistent definition/call chain.

- [x] **Step 3: Update executable project specs**

Load `trellis-update-spec` and record only durable contracts:

- package dependency direction in `.trellis/spec/backend/directory-structure.md`;
- Provider typed errors/HTTP mapping in `.trellis/spec/backend/error-handling.md`;
- operation-coupled file access, bounded runner, exact Provider locators, task-scoped mutable identity, and required tests in `.trellis/spec/backend/quality-guidelines.md`.

Do not copy the entire feature plan into specs; record reusable rules and signatures.

- [x] **Step 4: Re-run final evidence after all fixes/spec edits**

Run:

```bash
git status --short
git diff --check
cd backend && go test ./... -count=1 && go build ./...
cd .. && make check
```

Expected: every command succeeds on the final uncommitted tree.

- [ ] **Step 5: Draft and verify the single Phase 3.4 commit manifest**

Run:

```bash
git diff --name-only
git diff --stat
git status --short
```

Compare every path with Section 1. Stop for user direction if an unrecognized user-owned path appears. Stage exact reviewed files only; never use directory-wide `git add backend`, `git add .`, or wildcards.

- [ ] **Step 6: Stage exact files and inspect the index**

Run one explicit `git add <exact path list>` containing only the reviewed manifest, then:

```bash
git diff --cached --name-only
git diff --cached --check
git status --short
```

Expected: every intended product/spec/task file is staged, no forbidden/unrelated file is staged, and cached diff check passes.

- [ ] **Step 7: Create the Phase 3.4 work commit**

Run:

```bash
git commit -m "feat: add backup repository read adapters"
git status --short
```

Expected: commit succeeds and the worktree contains no uncommitted task code/spec/planning changes.

## 16. Same-Branch Trellis Finish, PR, CI, Merge, And Post-Merge

- [ ] **Step 1: Run `trellis-finish-work` locally before push**

Load and execute the `trellis-finish-work` skill on `codex/backup-assets-provider-readers`. It must:

1. verify the Phase 3.4 work commit and clean code tree;
2. archive `07-13-backup-assets-provider-readers`, producing `chore(task): archive 07-13-backup-assets-provider-readers`;
3. record the session with the Phase 3.4 work commit hash, producing `chore: record journal`.

Expected log order: work commit -> archive commit -> journal commit. Do not amend or create a separate archive PR.

- [ ] **Step 2: Verify archive/parent metadata and clean state**

Run:

```bash
git log -3 --oneline
git status --short --branch
python3 ./.trellis/scripts/task.py list-archive | rg 'backup-assets-provider-readers'
```

Expected: three-stage commit order is correct, Child 2 archive exists with `status=completed`, parent remains planning with Child 2 linked, and the tree is clean.

- [ ] **Step 3: Push the one finished branch**

Run:

```bash
git push -u origin codex/backup-assets-provider-readers
```

Expected: push succeeds with work + archive + journal commits on the same branch.

- [ ] **Step 4: Create one ready PR**

Use Trellis/GitHub CLI with title:

```text
feat: add backup repository read adapters
```

The PR body must summarize identity classes, no-migration/no-Provider-write boundary, five feature-gated routes, safe-path/SSH hardening, exact gates run, rollback, and the corrected same-branch Trellis finish sequence.

- [ ] **Step 5: Monitor and fix every required PR check**

Watch GitGuardian, PR title, backend, frontend, Docker, PostgreSQL migration parity, UTC/migration safety, and documentation freshness until all required checks are successful. Diagnose failures on the same branch, fix, rerun local affected/full gates, create a normal follow-up work commit, push, and continue monitoring. Do not merge with pending, missing, canceled, or failing required checks.

- [ ] **Step 6: Squash merge and monitor main automation**

After all checks are green, squash merge. Monitor post-merge main CI and Release Please. Because the PR title is `feat:`, expect Release Please to create/update the next minor release PR. Unless a formal GitHub Release is actually published, record that Docker image publication and Docker Hub description sync were not expected. README is unchanged, so description sync is not expected.

- [ ] **Step 7: Synchronize and clean branches before the next child**

Run:

```bash
git fetch --prune origin
git switch main
git pull --ff-only origin main
git status --short --branch
```

Verify the merged `main` tree contains the Child 2 result. Before deleting the local work branch, compare its tree to the merged main tree; delete only after equality is proven. Delete a remaining remote feature branch if GitHub did not auto-delete it. Final expected state: local `main` equals `origin/main`, clean tree, no open Child 2 PR, no obsolete Child 2 branch, parent task still planning, and no next child has started prematurely.

## 17. Requirement Coverage Matrix

| Requirement | Implementation tasks | Primary evidence |
|---|---|---|
| Narrow ports/registry/Command unsupported | 4 | Provider core compile/behavior tests |
| Stable identity and encrypted binding | 4, 11 | identity/binding/connect tests and at-rest assertions |
| Bounded exact list/stat/open/cursor | 4, 6–10 | cursor/fileaccess/adapter suites |
| Safe local/SFTP containment and race handling | 6, 7 | injected race, symlink, special-file, cancel tests |
| Restic full ID/read-only commands | 8 | executable fake and negative command history |
| Rclone honest Range/read-only commands | 10 | capability probe and negative command history |
| Rsync mutable tree without write-check | 9 | tree adapter spy and mutable tests |
| SSH purposes, cancellation, audit | 5 | scope/dialer/runner tests |
| Feature gate, API, RBAC, ownership | 11–13 | zero-side-effect, full-router, mixed-lineage tests |
| Stable mutable-head observation | 12 | success/failure/disconnect/reconnect tests |
| Sanitized DTO/error/log/audit | 3–5, 11–14 | serialization and negative secret scans |
| No migration/UI/Provider mutation | 14 | changed-path and command reachability scans |
| Swagger/settings/docs | 3, 13, 14 | generated docs and env-var checks |
| Correct commit/archive/PR lifecycle | 15–16 | Git log, archive, PR CI, post-merge state |

## 18. Planning Review Gate

Before running Task 2 activation, present the final planning package to the user:

- `.trellis/tasks/07-13-backup-assets-provider-readers/prd.md`
- `.trellis/tasks/07-13-backup-assets-provider-readers/design.md`
- `.trellis/tasks/07-13-backup-assets-provider-readers/implement.md`

Ask one question only: whether the user approves starting implementation with `task.py start`. An approval to plan or choose architecture is not implementation approval.

## 19. Plan Self-Review

- [x] Every functional PRD acceptance criterion maps to a passing test or command in Sections 3–14 and the Requirement Coverage Matrix.
- [x] The implementation keeps domain, Provider, repository service, file access, SSH transport, and HTTP responsibilities in their reviewed dependency direction.
- [x] Review hardening added red/green regressions for dynamic settings, strict SFTP source checks, changed-list cursors, retained/shared Restic binding drift, stream lifecycle, oversized request bodies, nil service handling, and internal observation JSON non-disclosure.
- [x] The reviewed manifest contains every changed path. `backend/README_backend.md` was added after doc-freshness review proved that five new routes require maintainer-route documentation.
- [x] No schema migration, frontend/public navigation, deploy change, Provider mutation, Task command change, or feature-default enablement is present.
- [x] Inline mode was preserved; no implementation/check sub-agent or intermediate Git commit was used.
- [x] All product, test, documentation, spec, and active-task changes remain together for the single Phase 3.4 work commit.

## 20. Verification Record (2026-07-14)

- Focused tests passed for `backupasset`, `settings`, `fileaccess`, `sshutil`, `task/executor`, all three Provider adapters, repository service, API handlers, and the full API router/RBAC package.
- Cancel/timeout/resource/source-change/cursor/identity suites passed ten consecutive runs. `go test -race` passed for `backupasset`, `fileaccess`, `sshutil`, `backupasset/provider`, and `backupasset/repository`.
- `make lint-backend` reported `0 issues`. A fresh `cd backend && go test ./... -count=1 && go build ./...` passed after the final production-code change.
- A fresh `env -u NODE_ENV make check` passed backend lint/tests/build and frontend lint, 128 test files / 551 tests with coverage, TypeScript build, and Vite production build.
- Project-pinned `swag` v1.16.6 regenerated tracked `backend/internal/api/docs/docs.go`; it contains exactly the five intended Repository routes and no standalone probe route. The global `swag` binary was absent, so generation used `go run github.com/swaggo/swag/cmd/swag@v1.16.6`; dependency-parser warnings were non-fatal and generation exited successfully.
- `scripts/check-doc-freshness.sh`, `scripts/check-migration-utc-safety.sh`, `git diff --check`, and `task.py validate 07-13-backup-assets-provider-readers` passed. The backend route reference and env-var documentation now match production wiring.
- Dependency and negative-reachability checks found no Provider import of handlers or `task/executor`, no repository executor branching outside `binding.go`, no direct shell/`CombinedOutput` path, and no changed migration, frontend, or deploy file.
- The Provider command allowlist contains only Restic `version/config/snapshots/ls/dump`, Rclone `version/features/lsjson/cat`, and read-only remote enumeration. Secret/locator fields are internal or `json:"-"`; public Repository DTOs, capability errors, logs, and typed audits expose only validated safe fields.
- PostgreSQL migration parity needs no local schema run because this child changes no migration or schema file; the required CI job remains mandatory before merge.
- Phase 3.4 staging/commit, same-branch `trellis-finish-work`, push, PR CI, merge, and post-merge automation remain intentionally pending and must execute in that order.
