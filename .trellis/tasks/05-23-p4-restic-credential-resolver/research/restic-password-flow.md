# Research: restic repository_password flow

- **Query**: restic repository_password 当前在 Xirang 中的所有消费路径、secret 流向、适合 P4-3 local resolver seam 的最小切入点、必须覆盖和可以 deferred 的路径
- **Scope**: internal
- **Date**: 2026-05-23

## Findings

### Files Found

| File Path | Description |
|---|---|
| `web/src/components/task-create-dialog.tsx` | Restic task draft state, existing config parser, and create/update payload builder for `repository_password`. |
| `web/src/components/task-create-dialog.advanced.tsx` | Password input UI for restic repository password. |
| `web/src/lib/api/tasks-api.ts` | Frontend API client sends `executor_config`; mapper can accept `executor_config` if backend returns it. |
| `backend/internal/api/handlers/task_handler.go` | Task create/update ingress for `executor_config`; validates only JSON and preserves blank secret values on same-executor update. |
| `backend/internal/model/models.go` | `Task.ExecutorConfig` is `json:"-"` and encrypted/decrypted by GORM hooks. |
| `backend/internal/task/executor/restic_executor.go` | Primary restic consumer: backup, restore, snapshot list, snapshot file list, and snapshot file restore all parse `repository_password` and build `RESTIC_PASSWORD=...`. |
| `backend/internal/task/retention.go` | Periodic restic `forget --prune` path extracts `repository_password` with a package-local helper and builds its own env prefix. |
| `backend/internal/task/integrity_checker.go` | Periodic restic `check` path reuses task package `extractResticPassword` and builds its own env prefix. |
| `backend/internal/task/verifier/verifier.go` | Post-backup restic verification extracts `repository_password` with a verifier-local helper and runs `restic check`. |
| `backend/internal/snapshot/indexer.go` | Snapshot search indexing uses `ResticExecutor.ListSnapshots` and also parses `repository_password` locally for `restic find --json --long`. |
| `backend/internal/api/handlers/snapshot_handler.go` | Snapshot browse/restore API calls `ResticExecutor.ListSnapshots`, `ListFiles`, and `RestoreFiles`. |
| `backend/internal/api/handlers/snapshot_search_handler.go` | Snapshot search API triggers `snapshot.EnsureIndexed`, which eventually consumes `repository_password`. |
| `backend/internal/api/handlers/snapshot_diff_handler.go` | Manual/API snapshot diff path parses `repository_password` locally and builds `RESTIC_PASSWORD=...` for `restic diff`. |
| `backend/internal/anomaly/snapshot_diff.go` | Async post-backup anomaly diff reloads task, extracts `repository_password`, and runs `restic snapshots` / `restic diff`. |
| `backend/internal/task/runner.go` | Runtime orchestration for backup, post-backup verify, async anomaly diff, and restore; restore verification skips restic/rclone. |
| `backend/internal/task/manager.go` | Task restore creates restore-task copy retaining original `ExecutorConfig`; restic restore reads repository password from that copy. |
| `backend/internal/api/handlers/config_handler.go` | Config export/import can include or restore decrypted `executor_config` only when secrets are explicitly included. |
| `backend/internal/api/handlers/credential_access_grant.go` | Grant routes gate snapshot restore/task restore eligibility; they do not resolve `repository_password` themselves. |
| `backend/internal/sshutil/credential_provider.go` | Existing local provider seam for SSH credentials; relevant precedent for a P4-3 restic local resolver seam. |
| `backend/internal/sshutil/scope.go` | Existing purpose constants and `ResolvedCredential` shape used by SSH provider/audit code. |
| `backend/internal/credentialaudit/audit.go` | Credential audit sanitizer drops sensitive metadata keys/values, including `password`, `secret`, `credential`, `config`, `output`, `command`, and `payload`. |
| `.trellis/spec/backend/database-guidelines.md` | Spec: sensitive model fields are encrypted through hooks; `credential_audit_events` must not store raw credentials or decrypted executor config. |
| `.trellis/spec/backend/quality-guidelines.md` | Spec: do not return executor configs without sanitizing; security-sensitive code needs denial tests. |
| `backend/internal/task/executor/restic_executor_test.go` | Existing tests cover restic config parsing and env prefix behavior. |
| `backend/internal/task/retention_test.go` | Existing tests cover task package `extractResticPassword`. |
| `backend/internal/snapshot/indexer_test.go` | Existing tests cover indexer config parsing and env prefix behavior. |
| `backend/internal/anomaly/snapshot_diff_test.go` | Existing tests cover anomaly diff password extraction/env prefix. |
| `backend/internal/api/handlers/task_handler_test.go` | Existing tests assert task create/update responses do not expose `executor_config` or restic password. |
| `backend/internal/api/handlers/batch_handler_test.go` | Existing tests assert batch details do not expose `executor_config` or restic password. |

### Code Patterns

#### Secret source and API ingress

Frontend restic task editing stores the secret in local draft state (`resticPassword`) and serializes it into `executor_config` as `repository_password`:

- `web/src/components/task-create-dialog.tsx:17-30` defines `TaskDraft.resticPassword`, `resticExcludePatterns`, and `resticAppendOnly`.
- `web/src/components/task-create-dialog.tsx:51-63` parses existing restic config as `{ repository_password, exclude_patterns, append_only }`.
- `web/src/components/task-create-dialog.tsx:152-162` serializes restic config:
  - `repository_password: draft.resticPassword.trim()`
  - `exclude_patterns: excludePatterns`
  - `append_only: draft.resticAppendOnly || undefined`
- `web/src/components/task-create-dialog.advanced.tsx:146-160` renders `<Input type="password">` bound to `draft.resticPassword`.
- `web/src/lib/api/tasks-api.ts:172-188` sends `executor_config` on create; `web/src/lib/api/tasks-api.ts:192-208` sends it on update.

Backend task handler accepts this field directly:

- `backend/internal/api/handlers/task_handler.go:56-66` defines `taskRequest.ExecutorConfig string json:"executor_config"`.
- `backend/internal/api/handlers/task_handler.go:257-268` assigns `ExecutorConfig: req.ExecutorConfig` when creating a `model.Task`.
- `backend/internal/api/handlers/task_handler.go:347` merges update config through `mergeTaskExecutorConfigForUpdate`.
- `backend/internal/api/handlers/task_handler.go:935-993` preserves previous secret values when a same-executor update sends blank values for keys matching `password`, `secret`, `token`, `api_key`, or `access_key`.
- `backend/internal/api/handlers/task_handler.go:1127-1131` validates only that non-empty `executor_config` is valid JSON; there is no restic-specific schema validation at ingress.

#### Storage boundary

`Task.ExecutorConfig` is the persistence boundary for restic `repository_password`:

- `backend/internal/model/models.go:296-308` defines `Task.ExecutorConfig string gorm:"type:text" json:"-"`.
- `backend/internal/model/models.go:326-347` encrypts `Task.ExecutorConfig` in `BeforeSave` and decrypts it in `AfterFind` via `secure.EncryptIfNeeded` / `secure.DecryptIfNeeded`.
- Because the model field is `json:"-"`, normal task responses do not emit `executor_config`; handler tests cover this redaction (`backend/internal/api/handlers/task_handler_test.go`, `backend/internal/api/handlers/batch_handler_test.go`).

Config export/import is an explicit exception path:

- `backend/internal/api/handlers/config_handler.go:205-207` includes `executor_config` only when `includeSecrets` is true.
- `backend/internal/api/handlers/config_handler.go:533-543` reads imported `executor_config` into `taskRequest`.
- `backend/internal/api/handlers/config_handler.go:575-582` overwrites an existing task's `ExecutorConfig` from import data.

#### Runtime consumption: central executor path

`backend/internal/task/executor/restic_executor.go` is the central consumer and already provides the most concentrated seam:

- `backend/internal/task/executor/restic_executor.go:18-23` defines `ResticConfig.RepositoryPassword json:"repository_password,omitempty"`.
- `backend/internal/task/executor/restic_executor.go:383-391` parses raw `task.ExecutorConfig` into `ResticConfig`.
- `backend/internal/task/executor/restic_executor.go:395-410` builds the command prefix:
  - empty password -> `RESTIC_PASSWORD=''`
  - non-empty password -> `RESTIC_PASSWORD=` + `ShellEscape(password)`
  - sudo path -> `sudo env <envPrefix> <resticBin>`

Executor methods consuming this config:

- Backup: `ResticExecutor.Run` parses config at `restic_executor.go:49`, builds command prefix at `restic_executor.go:68`, and uses it for `snapshots`, `init`, `cat config`, and `backup` commands at `restic_executor.go:71-109`.
- Task restore: `ResticExecutor.RunRestore` parses config at `restic_executor.go:134`, builds command prefix at `restic_executor.go:146`, and uses it for `restore latest` at `restic_executor.go:148-149`.
- Snapshot list: `ResticExecutor.ListSnapshots` parses config at `restic_executor.go:288`, builds command prefix at `restic_executor.go:299`, and runs `snapshots -r ... --json` at `restic_executor.go:300`.
- Snapshot file list: `ResticExecutor.ListFiles` parses config at `restic_executor.go:316`, builds command prefix at `restic_executor.go:327`, and runs `ls ... -r ... --json` at `restic_executor.go:332`.
- Snapshot file restore: `ResticExecutor.RestoreFiles` parses config at `restic_executor.go:359`, builds command prefix at `restic_executor.go:370`, and runs `restore <snapshot> -r ... --target ...` at `restic_executor.go:375`.

These executor calls are reached from:

- `backend/internal/task/runner.go:417-421` for post-backup verifier dispatch after `ResticExecutor.Run` succeeds.
- `backend/internal/task/runner.go:747-754` for task restore through `RestoreExecutor.RunRestore`.
- `backend/internal/api/handlers/snapshot_handler.go:76-78` for snapshot listing.
- `backend/internal/api/handlers/snapshot_handler.go:125-127` for snapshot file listing.
- `backend/internal/api/handlers/snapshot_handler.go:219-225` for snapshot file restore.
- `backend/internal/snapshot/indexer.go:46` and `snapshot/indexer.go:134` indirectly for listing snapshots before indexing.

#### Runtime consumption: duplicated direct consumers outside ResticExecutor

Several code paths parse `repository_password` independently and build their own `RESTIC_PASSWORD` prefix:

1. Retention cleanup
   - `backend/internal/task/retention.go:139-147` conditionally extracts `repository_password` from `task.ExecutorConfig`.
   - `backend/internal/task/retention.go:150-153` builds `RESTIC_PASSWORD=''` or `RESTIC_PASSWORD=<escaped>`.
   - `backend/internal/task/retention.go:162-163` runs `restic forget -r ... --prune`.
   - `backend/internal/task/retention.go:238-247` defines package-local `extractResticPassword`.

2. Periodic integrity check
   - `backend/internal/task/integrity_checker.go:70-75` extracts the password from `task.ExecutorConfig` using the task package helper.
   - `backend/internal/task/integrity_checker.go:77-84` builds the env prefix and runs `restic check -r ... --json`.

3. Post-backup verifier
   - `backend/internal/task/verifier/verifier.go:474-501` runs `restic check` during backup verification.
   - `backend/internal/task/verifier/verifier.go:488-491` extracts `repository_password` and builds `RESTIC_PASSWORD=` + `shellQuote(password)`.
   - `backend/internal/task/verifier/verifier.go:531-543` defines verifier-local `extractResticPassword`.
   - `backend/internal/task/verifier/verifier.go` restore verification path skips restic/rclone when `isRestore == true`, so restore verification does not consume the restic password.

4. Snapshot search index build
   - `backend/internal/snapshot/indexer.go:251-265` defines and parses `indexConfig.RepositoryPassword`.
   - `backend/internal/snapshot/indexer.go:276-282` builds `RESTIC_PASSWORD` prefix.
   - `backend/internal/snapshot/indexer.go` also uses `ResticExecutor.ListSnapshots`, but the actual per-snapshot `restic find --json --long --path=/ ... -r ...` path has its own parser/env builder.

5. Manual/API snapshot diff
   - `backend/internal/api/handlers/snapshot_diff_handler.go:102-104` parses restic diff config from `task.ExecutorConfig`.
   - `backend/internal/api/handlers/snapshot_diff_handler.go:118-124` builds `RESTIC_PASSWORD` env prefix and runs `restic diff`.
   - `backend/internal/api/handlers/snapshot_diff_handler.go:276-295` defines the handler-local parser/env-prefix helper.

6. Async anomaly snapshot diff
   - `backend/internal/anomaly/snapshot_diff.go:312-321` reloads full task and extracts `repository_password`.
   - `backend/internal/anomaly/snapshot_diff.go:336-367` runs `restic snapshots` and `restic diff` with that env prefix.
   - `backend/internal/anomaly/snapshot_diff.go:267-288` defines anomaly-local parser/env-prefix helpers.
   - `backend/internal/task/runner.go:506-514` triggers this asynchronously after successful restic backup when anomaly sink and `PolicyID` are present.

#### Grant and audit boundaries

Grant routes gate restore actions but do not themselves resolve `repository_password`:

- `backend/internal/api/handlers/credential_access_grant.go:260-299` creates snapshot restore grants only for restic tasks.
- `backend/internal/api/handlers/credential_access_grant.go:302-330` creates task restore grants.
- `backend/internal/api/handlers/credential_access_grant.go:437-464` allows task restore grants for `rsync`, `restic`, and `rclone` tasks with at least one successful run.

SSH credential auditing is separate from restic password use:

- Restic execution opens SSH connections through `executor.DialSSHForNodePurpose`, using purposes such as `task_backup`, `task_restore`, `snapshot`, `snapshot_diff`, `integrity_check`, and `retention`.
- `backend/internal/task/executor/ssh_connect.go` writes `task.credential.use` audit events for SSH credentials, not for restic repository password resolution.

Audit constraints for any future restic resolver/audit seam:

- `backend/internal/credentialaudit/audit.go:263-280` drops metadata keys or values containing `private`, `password`, `token`, `secret`, `credential`, `config`, `output`, `stream`, `command`, `content`, `payload`, `bearer`, or `authorization:`.
- `.trellis/spec/backend/database-guidelines.md:85-89` states `credential_audit_events` must not store raw credentials, decrypted executor config, terminal streams, command output, or file contents.
- `.trellis/spec/backend/quality-guidelines.md:21-22` forbids returning executor configs without sanitizing sensitive fields.

#### Existing local resolver precedent

The existing SSH provider seam is the closest precedent:

- `backend/internal/sshutil/credential_provider.go:13-24` defines `CredentialProvider`, `LocalCredentialProvider`, and `DefaultCredentialProvider()`.
- `backend/internal/sshutil/credential_provider.go:26-60` resolves SSH key content from preloaded `Node.SSHKey`, DB lookup by `SSHKeyID`, or legacy `node.private_key`, returning both secret content and a safe `ResolvedCredential`.
- `backend/internal/sshutil/credential_provider.go:62-94` builds auth methods and returns `ResolvedCredential` metadata without exposing secret material.
- `backend/internal/sshutil/scope.go:13-33` defines existing purpose labels including `task_backup`, `task_restore`, `snapshot`, `snapshot_diff`, `integrity_check`, and `retention`.
- `backend/internal/sshutil/scope.go:57-64` defines `ResolvedCredential{Kind, Source, Provider, KeyID}`.

There is no equivalent restic repository password resolver found in backend models, migrations, settings, or secure packages. The search for restic credential-resource models/migrations found no backend `restic_repo` resource; the only `restic_repo` reference found is a frontend display label in `web/src/pages/credentials-page.tsx:77-100`. The existing `AppCredential` resource currently accepts only DB/Docker credential types in `backend/internal/api/handlers/app_credential_handler.go:15-24`, stores config encrypted via `model.AppCredential` hooks at `models.go:152-195`, and is referenced by policies, not by restic executor config.

### Secret Flow

1. User enters restic repository password in `TaskEditorDialog` password input.
2. Frontend serializes it into `executor_config` JSON as `repository_password`.
3. Task create/update API receives raw JSON in `taskRequest.ExecutorConfig`.
4. `model.Task.BeforeSave` encrypts the whole `ExecutorConfig` string before DB persistence.
5. `model.Task.AfterFind` decrypts the whole `ExecutorConfig` string when the task is loaded.
6. Restic execution paths parse the decrypted JSON and extract `repository_password`.
7. Remote commands inject it into the shell command as `RESTIC_PASSWORD=...`, normally escaped with `executor.ShellEscape` or local `shellQuote` helpers.
8. Normal API responses omit `executor_config`; config export with `includeSecrets=true` can include it.

### Suitable P4-3 Local Resolver Seam: Minimum Cut Points

A minimal local resolver seam can be placed around the restic config parsing and password-to-command-env boundary because all current consumers need the same two outputs:

- a resolved repository password string for runtime command construction; and
- safe metadata/source information if audit or future credential-source distinction is needed.

The smallest high-leverage cut point is the centralized `ResticExecutor` config/env path:

- `parseResticConfig` at `backend/internal/task/executor/restic_executor.go:383-391`
- `buildCommandPrefix` / `buildResticEnvPrefix` at `backend/internal/task/executor/restic_executor.go:395-410`

That cut point covers the largest set of paths immediately:

- restic backup
- task restore
- snapshot listing
- snapshot file listing
- snapshot file restore
- any caller routed through `ResticExecutor.ListSnapshots`, including part of snapshot indexing

However, this alone is not full coverage because several packages bypass `ResticExecutor` and parse `repository_password` directly. To cover all current consumers, the seam must also be callable from:

- `backend/internal/task/retention.go`
- `backend/internal/task/integrity_checker.go`
- `backend/internal/task/verifier/verifier.go`
- `backend/internal/snapshot/indexer.go`
- `backend/internal/api/handlers/snapshot_diff_handler.go`
- `backend/internal/anomaly/snapshot_diff.go`

A seam that returns only the password string would match current behavior; a seam shaped more like the SSH provider precedent would also return safe source/provider data while keeping raw password out of audit metadata.

### Must Cover Paths

Current must-cover paths for `repository_password` resolution are all places that execute restic against an encrypted repository:

1. `ResticExecutor.Run` backup: repo detection, init, append-only config check, backup.
2. `ResticExecutor.RunRestore` task restore.
3. `ResticExecutor.ListSnapshots` snapshot browsing and the initial indexing snapshot list.
4. `ResticExecutor.ListFiles` snapshot file browse.
5. `ResticExecutor.RestoreFiles` snapshot file restore.
6. `Manager.enforceResticRetention` retention cleanup.
7. `Manager.checkResticIntegrity` periodic integrity check.
8. `verifier.VerifyRestic` post-backup verification.
9. `snapshot.indexSnapshot` / indexer restic find path.
10. `SnapshotDiffHandler.Compare` manual/API snapshot diff.
11. `anomaly.AnalyzeSnapshotDiff` async post-backup diff and anomaly detection.
12. Task create/update/import boundaries that preserve/store `executor_config`, because the current local source of truth is `Task.ExecutorConfig.repository_password`.

### Can Be Deferred Paths

Paths that are related but do not currently require a restic password resolver for this P4 seam:

- Policy sync in `backend/internal/policy/sync.go:91-105`: creates policy-managed tasks as `ExecutorType: "rsync"`, not restic.
- Restore drill in `backend/internal/task/drill.go`: cross-node transfer is currently disabled, and the active sync restore code shown does not consume restic `repository_password`.
- Credential access grant creation/use: grants authorize restore actions but do not currently resolve the restic repository password.
- `AppCredential` / credentials page `restic_repo` display label: backend accepted credential types are DB/Docker types only; no backend restic repository credential model or profile was found.
- Frontend typed `restic_repo` labels can remain display-only unless a backend restic credential resource is introduced.
- Restore verification for restic/rclone: current verifier skips restic/rclone when `isRestore == true`.

### Related Specs

- `.trellis/spec/backend/database-guidelines.md` — sensitive fields must be encrypted through model hooks; `credential_audit_events` must not store raw credentials, decrypted executor config, terminal streams, command output, or file contents.
- `.trellis/spec/backend/quality-guidelines.md` — handlers must not expose executor configs without sanitization; security-sensitive changes require explicit denial tests.
- `.trellis/spec/guides/cross-layer-thinking-guide.md` — use source → transform → store → retrieve → transform → display mapping for cross-layer secret flow analysis.

### Tests and Existing Coverage

Existing tests already exercise some current restic password behavior:

- `backend/internal/task/executor/restic_executor_test.go` covers `parseResticConfig`, `buildCommandPrefix`, `RESTIC_PASSWORD`, append-only, and restic binary behavior.
- `backend/internal/task/retention_test.go` covers task package `extractResticPassword`.
- `backend/internal/snapshot/indexer_test.go` covers `parseResticIndexConfig` and `buildIndexEnvPrefix`.
- `backend/internal/anomaly/snapshot_diff_test.go` covers anomaly diff password extraction and env prefix.
- `backend/internal/api/handlers/task_handler_test.go` covers no `executor_config`/password exposure in task create/update responses.
- `backend/internal/api/handlers/batch_handler_test.go` covers no `executor_config`/password exposure in batch details.

Paths where tests should be checked/updated if resolver behavior changes:

- Restic executor backup/restore/snapshot methods.
- Retention and integrity checker restic paths.
- Verifier restic check path.
- Snapshot indexer `restic find` path.
- Snapshot diff handler path.
- Anomaly diff path.
- Task update blank-secret preservation for `repository_password`.
- Config export/import include-secrets behavior.

## Caveats / Not Found

- No backend restic repository credential resource, restic credential model, or migration was found. Current restic repository password source is `Task.ExecutorConfig.repository_password`.
- `restic_repo` was found only as a frontend credentials-page display label; backend `AppCredential` accepted types do not include `restic_repo`.
- Multiple direct consumers build `RESTIC_PASSWORD=...` independently. Covering only `ResticExecutor` would leave retention, integrity check, verifier, snapshot indexer, snapshot diff handler, and anomaly diff using the old path.
- Some restic command errors include command output in error/log paths. The credential audit sanitizer has explicit output redaction rules, but task logs and regular logs should be reviewed separately if command construction changes.
