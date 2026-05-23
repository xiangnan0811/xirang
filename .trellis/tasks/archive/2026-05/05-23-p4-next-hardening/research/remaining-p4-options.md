# Research: Remaining P4 credential/security hardening options

- **Query**: Research remaining P4 credential/security hardening options after completed slices P4-1 local credential provider foundation, P4-2 executor SSH local-provider adoption, and P4-3 restic repository access resolver. Select the next smallest executable P4 slice that is local-only/backend-first, preserves API/deployment/UI behavior, and avoids external Vault/KMS/SSH CA/session recording/command approval. Summarize completed vs remaining P4, candidate slices, recommended next slice, risks, and must-cover files/tests.
- **Scope**: internal
- **Date**: 2026-05-23

## Findings

### Files Found

| File Path | Description |
|---|---|
| `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md` | Roadmap source separating P3 control-plane grants from P4 architecture-level work. |
| `.trellis/tasks/archive/2026-05/05-22-p4-credential-broker-foundation/prd.md` | P4-1 PRD: local-only SSH credential provider seam, no migrations/API/deployment changes, external providers out of scope. |
| `.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/prd.md` | P4-2 PRD: executor SSH and rsync key materialization use the local provider seam; restic and app credential seams explicitly deferred. |
| `.trellis/tasks/archive/2026-05/05-23-p4-restic-credential-resolver/prd.md` | P4-3 PRD: local resolver seam for restic `repository_password` across all current consumers. |
| `.trellis/tasks/archive/2026-05/05-22-p4-next-hardening-slice/research/remaining-p4-roadmap.md` | Prior remaining-P4 ranking after P4-1; listed AppCredential profile hook rendering and restic repository password as follow-up seams. |
| `.trellis/tasks/archive/2026-05/05-23-p4-restic-credential-resolver/research/restic-password-flow.md` | Full map of restic password consumers and must-cover paths before P4-3. |
| `.trellis/tasks/archive/2026-05/05-23-p4-restic-credential-resolver/research/p4-provider-precedent.md` | Reusable local-provider constraints from P4-1/P4-2 and restic-specific acceptance guidance. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend security contracts for SSH scope, credential audit, grant ordering, and sensitive-data exclusions. |
| `.trellis/spec/backend/database-guidelines.md` | Model-hook encryption/decryption contract and credential audit persistence constraints. |
| `.trellis/spec/backend/logging-guidelines.md` | Logging exclusions for decrypted secrets, executor config, command output, endpoints, and unsafe audit metadata. |
| `.trellis/spec/backend/error-handling.md` | Client-facing error exclusions for SQL/encryption details, private keys, tokens, command output, exported payloads, and stack-like details. |
| `.trellis/spec/backend/deployment-runtime.md` | Deployment contract keeping official self-hosted runtime free of new external credential services. |
| `backend/internal/sshutil/credential_provider.go` | Current P4-1 local SSH credential provider implementation. |
| `backend/internal/sshutil/ssh_auth.go` | Provider-backed public SSH auth/key helpers and managed-key `LastUsedAt` update helper. |
| `backend/internal/task/executor/ssh_connect.go` | P4-2 executor SSH dial path now delegates auth construction to `sshutil.BuildSSHAuthForPurpose`. |
| `backend/internal/task/executor/executor.go` | P4-2 rsync key resolver now delegates key content resolution to `sshutil.ResolveKeyContentForPurpose`. |
| `backend/internal/task/executor/restic_repository_access.go` | P4-3 restic repository access resolver with `provider=local` and safe metadata. |
| `backend/internal/task/executor/restic_executor.go` | Central restic backup/restore/snapshot methods now use restic repository access resolver. |
| `backend/internal/task/retention.go` | Restic retention path now uses `ResolveResticRepositoryAccessOrEmpty` and shared env prefix builder. |
| `backend/internal/task/integrity_checker.go` | Restic integrity check path now uses shared restic repository access resolver. |
| `backend/internal/task/verifier/verifier.go` | Restic post-backup verifier now uses shared restic repository access resolver. |
| `backend/internal/snapshot/indexer.go` | Snapshot indexer restic find path now uses shared restic repository access resolver. |
| `backend/internal/api/handlers/snapshot_diff_handler.go` | Manual/API restic snapshot diff now uses shared restic repository access resolver. |
| `backend/internal/anomaly/snapshot_diff.go` | Async anomaly snapshot diff now uses shared restic repository access resolver. |
| `backend/internal/api/handlers/policy_handler.go` | Policy create/update still directly load `AppCredential.Config` and render hooks. |
| `backend/internal/api/handlers/app_credential_handler.go` | AppCredential update/cascade path still directly unmarshals old/new config maps for hook re-rendering. |
| `backend/internal/profile/profile.go` | Built-in app-aware hook templates and `RenderHooks`; templates can intentionally include app passwords in generated hook commands. |
| `backend/internal/model/models.go` | `AppCredential`, `Integration`, `Task.ExecutorConfig`, and other sensitive fields with model-hook encryption/decryption. |
| `backend/internal/alerting/dispatcher.go` | Notification dispatch consumes decrypted `Integration.Endpoint`, `Secret`, and `ProxyURL` directly. |
| `backend/internal/api/handlers/integration_handler.go` | Integration create/update/test paths write and consume endpoint/secret/proxy values with masking for responses. |
| `backend/internal/api/handlers/node_handler.go` | Node test connection uses provider-backed auth but still manually dials with `ssh.Dial`. |
| `backend/internal/sshutil/probe.go` | Probe helper uses provider-backed auth but still manually dials with `ssh.Dial`. |
| `backend/internal/task/executor/restic_executor_test.go` | Current restic resolver tests for safe metadata, JSON secrecy, invalid config safety, and env-prefix equivalence. |
| `backend/internal/task/executor/ssh_connect_test.go` | P4-2 executor auth metadata and audit tests. |
| `backend/internal/sshutil/credential_provider_test.go` | P4-1 local-provider equivalence and metadata safety tests. |
| `backend/internal/api/handlers/app_credential_handler_test.go` | Existing AppCredential tests for encryption, response sanitization, password preservation, and profile list. |
| `backend/internal/api/handlers/integration_app_aware_test.go` | Existing app-aware integration tests proving rendered policy hooks currently include app credential passwords by design. |
| `backend/internal/profile/profile_test.go` | Existing profile rendering tests that assert password-containing hook command behavior. |

### Completed vs Remaining P4

| Area | Status | Evidence |
|---|---|---|
| P4-1 local SSH credential provider foundation | Completed. Current `sshutil` exposes `CredentialProviderLocal = "local"`, a `CredentialProvider` interface, `LocalCredentialProvider`, and `DefaultCredentialProvider()`. | `backend/internal/sshutil/credential_provider.go:13-24`; recent commit evidence includes `3df0a29 fix(security): add local credential provider seam`. |
| P4-1 managed/inline/password SSH behavior | Completed for shared helpers. Local provider resolves preloaded/DB-loaded managed keys, inline node private keys, and node passwords with safe metadata and managed-key scope validation. | `backend/internal/sshutil/credential_provider.go:26-94`; tests in `backend/internal/sshutil/credential_provider_test.go:16-255`. |
| P4-2 executor SSH local-provider adoption | Completed. Executor auth construction now normalizes auth type and delegates to `sshutil.BuildSSHAuthForPurpose`; runtime audit metadata includes `provider` when present. | `backend/internal/task/executor/ssh_connect.go:66-93`, `:106-147`; recent commit evidence includes `ca37eda fix(security): route executor ssh through local provider`. |
| P4-2 rsync key materialization | Completed. Rsync private-key resolution now calls `sshutil.ResolveKeyContentForPurpose(node, nil, purpose)` and keeps rsync audit metadata safe. | `backend/internal/task/executor/executor.go:522-560`. |
| P4-3 restic repository access resolver | Completed. A local resolver parses restic config, returns password material in memory plus safe `provider/kind/source` metadata, and maps invalid JSON to a sanitized sentinel error. | `backend/internal/task/executor/restic_repository_access.go:9-14`, `:16-29`, `:39-64`, `:67-80`; recent commit evidence includes `9ec737c fix(security): centralize restic repository access`. |
| P4-3 restic consumer adoption | Completed across discovered current consumers. Restic executor, retention, integrity check, verifier, indexer, manual diff, and anomaly diff all call the shared resolver/env-prefix helpers. | `backend/internal/task/executor/restic_executor.go:49-68`, `:134-146`, `:288-300`, `:316-332`, `:359-375`; `backend/internal/task/retention.go:138-149`; `backend/internal/task/integrity_checker.go:70-75`; `backend/internal/task/verifier/verifier.go:488-490`; `backend/internal/snapshot/indexer.go:179-186`; `backend/internal/api/handlers/snapshot_diff_handler.go:101-123`; `backend/internal/anomaly/snapshot_diff.go:297-344`. |
| External Vault/KMS/provider references | Remaining, but not a fit for this next local-only slice. The original P4 roadmap lists provider health, leases, fallback, and import/export semantics as part of external secret broker work. | `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`; P4-1 out-of-scope excludes provider tables, health, leases, fallback, SDKs, migrations, env vars, and deployment changes (`05-22-p4-credential-broker-foundation/prd.md:33-40`). |
| SSH certificates / external CA | Remaining, but deferred. It requires host trust rollout, principals, TTL, revocation, signing, and CA lifecycle. | `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`. |
| Terminal/session recording | Remaining, but deferred. It would introduce a sensitive evidence store containing terminal streams/output, which current audit/grant contracts explicitly exclude. | `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`; `.trellis/spec/backend/quality-guidelines.md:397-405`. |
| Command approval/inspection | Remaining, but deferred. It requires command parsing/policy semantics while current audit/grant metadata forbids full command text/output. | `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`; `.trellis/spec/backend/quality-guidelines.md:397-401`, `:467-473`. |
| Advanced step-up / WebAuthn / device trust / policy UI | Remaining, but not directly enabled by the local credential provider seam and not backend-only. | `.trellis/tasks/archive/2026-05/05-21-plan-p3-p4-credential-security-hardening/prd.md:23-29`, `:69-77`. |
| Local provider seam expansion beyond SSH/restic | Remaining and best fit for next small P4 work. Remaining sensitive local materialization includes AppCredential profile hook rendering and notification integration endpoint/secret/proxy dispatch. | AppCredential direct config use in `backend/internal/api/handlers/policy_handler.go:257-274`, `:583-598`, and `backend/internal/api/handlers/app_credential_handler.go:262-295`, `:306-340`; integration direct dispatch in `backend/internal/alerting/dispatcher.go:568-586`. |

### Code Patterns

#### 1. Current local-provider pattern is conservative and internal-only

The repeated P4 pattern is now established:

- Provider identity is fixed to `local` for current slices (`backend/internal/sshutil/credential_provider.go:13`; `backend/internal/task/executor/restic_repository_access.go:39-45`).
- Existing encrypted local model fields remain the source of truth; P4-1/P4-2/P4-3 did not add provider tables, migrations, env vars, public API fields, frontend behavior, or deployment requirements.
- Result metadata is safe and small. SSH uses `ResolvedCredential{Kind, Source, Provider, KeyID}` (`backend/internal/sshutil/ssh_auth.go:75-80`); restic access exposes only `provider`, `kind`, and `source` through `SafeMetadata()` (`backend/internal/task/executor/restic_repository_access.go:48-54`).
- Storage stays behind GORM hooks. The database spec says sensitive fields are encrypted/decrypted through model hooks and `secure/crypto.go`, and handlers must not manually encrypt/decrypt sensitive fields (`.trellis/spec/backend/database-guidelines.md:14-17`, `:105-111`).

#### 2. AppCredential profile hook rendering is now the smallest remaining local-only credential materialization seam

`AppCredential.Config` is explicitly secret-bearing and encrypted by model hooks:

- `model.AppCredential` stores full JSON config including password and hides `Config` from JSON responses (`backend/internal/model/models.go:152-160`).
- `BeforeSave` encrypts `Config`, and `AfterFind` decrypts it (`backend/internal/model/models.go:166-185`).
- Normal API responses use `SanitizedConfig()` to delete `password` (`backend/internal/model/models.go:188-196`; `backend/internal/api/handlers/app_credential_handler.go:58-78`).

Current direct materialization paths:

- Policy create loads `model.AppCredential`, unmarshals decrypted `cred.Config`, and passes the map to `profile.RenderHooks` (`backend/internal/api/handlers/policy_handler.go:257-274`).
- Policy update repeats the same load/unmarshal/render flow (`backend/internal/api/handlers/policy_handler.go:583-598`).
- AppCredential update unmarshals old and new config maps directly, then cascade re-renders policy hooks with `profile.RenderHooks` (`backend/internal/api/handlers/app_credential_handler.go:262-295`, `:306-340`).

Important no-behavior-change constraint:

- Built-in hook templates can intentionally include app passwords in generated hook commands, e.g. MySQL `-p'{{.password}}'`, PostgreSQL `PGPASSWORD='{{.password}}'`, MongoDB `--password '{{.password}}'`, Redis `-a '{{.password}}'`, and Docker variants (`backend/internal/profile/profile.go:55-65`, `:75-85`, `:95-125`).
- Existing tests assert this behavior, including `profile_test.go` password-containing hook assertions (`backend/internal/profile/profile_test.go:56-90`, `:111-126`, `:139-160`, `:167-184`) and integration test text saying the rendered hook contains the password by design (`backend/internal/api/handlers/integration_app_aware_test.go:131-133`).

This means the next seam should wrap current materialization, not change rendered hook semantics.

#### 3. Integration notification secrets are a viable later local-only seam but broader than AppCredential hooks

`Integration` is another encrypted local credential source:

- `model.Integration` stores `Endpoint`, `Secret`, and `ProxyURL`, with hooks encrypting/decrypting all three (`backend/internal/model/models.go:198-258`).
- Integration create/update paths accept and validate endpoint/secret/proxy fields (`backend/internal/api/handlers/integration_handler.go:258-340`, `:357-454`).
- Test delivery loads a decrypted integration and calls `alerting.SendProbe(item)` (`backend/internal/api/handlers/integration_handler.go:612-625`).
- Alert delivery consumes decrypted endpoint/secret/proxy directly: `getHTTPClient(channel.ProxyURL)` and `s.Send(client, channel.Endpoint, channel.Secret, body)` (`backend/internal/alerting/dispatcher.go:568-586`).

This is backend-only and local-only, but it crosses more sender/channel code and network behavior than AppCredential hook rendering. It should remain a follow-up candidate after the smaller AppCredential seam.

#### 4. Raw SSH dial cleanup remains lower-value after P4-1/P4-2

Two raw `ssh.Dial` call sites still exist, but both already use provider-backed auth before dialing:

- Node test connection builds auth through `sshutil.BuildSSHAuthWithKeyForPurpose(..., PurposeNodeTest)` and then manually calls `ssh.Dial` (`backend/internal/api/handlers/node_handler.go:674-747`).
- `sshutil.ProbeNodeForPurpose` builds auth through `BuildSSHAuthForPurpose` and then manually calls `ssh.Dial` (`backend/internal/sshutil/probe.go:27-49`).

This is a consistency cleanup rather than a new credential-provider seam. It is smaller than AppCredential in code volume, but lower-value for P4 credential architecture because credential materialization already goes through the provider.

### Candidate Executable Slices

| Rank | Candidate slice | Boundary | Why it fits / does not fit now | Must-cover files/tests |
|---:|---|---|---|---|
| 1 | **AppCredential local resolver seam for profile hook rendering** | Add a backend-internal local resolver/helper for `AppCredential.Config` materialization used by policy create/update and app-credential cascade hook re-rendering. Keep hook output and responses unchanged. | Best next fit: local-only, backend-only, no migration/env/API/UI/deployment change; directly extends P4 provider pattern to the next credential class previously identified after SSH/restic; implementation is localized to policy/app-credential/profile code. | `backend/internal/api/handlers/policy_handler.go`, `backend/internal/api/handlers/app_credential_handler.go`, possible new internal resolver file/package, `backend/internal/profile/profile.go`, `backend/internal/model/models.go`; tests in `app_credential_handler_test.go`, `integration_app_aware_test.go`, `profile_test.go`, and targeted new resolver tests. |
| 2 | **Integration local resolver seam for notification dispatch/test** | Wrap decrypted `Integration.Endpoint`, `Secret`, and `ProxyURL` materialization for alert send/test behind a local resolver that returns in-memory send material plus safe provider/source labels. | Viable but broader: touches notification dispatch, many sender types, proxy HTTP client behavior, endpoint masking, and test-send error sanitization. It is still local-only/backend-only, but has more delivery behavior risk than AppCredential rendering. | `backend/internal/alerting/dispatcher.go`, `backend/internal/api/handlers/integration_handler.go`, `backend/internal/model/models.go`, integration handler/dispatcher tests. |
| 3 | **Node/probe raw SSH dial consolidation** | Route node test and `sshutil.ProbeNodeForPurpose` through `sshutil.DialSSH` or a shared provider/dial wrapper while keeping current timeout/status/audit behavior. | Very small and backend-only, but lower P4 value because auth already uses the local provider. Mainly improves SSH dial consistency rather than reducing remaining direct credential materialization. | `backend/internal/api/handlers/node_handler.go`, `backend/internal/sshutil/probe.go`, node/probe tests and credential audit tests. |
| 4 | **External provider-reference semantics design** | Planning-only ADR/research for provider references, fallback, health, import/export, and outage behavior before Vault/KMS. | Important before any external provider, but not the requested smallest backend implementation slice and explicitly outside local-only code constraints. | Archived P4 PRDs/research and backend specs; no code by default. |
| Deferred | Vault/KMS, SSH CA, terminal/session recording, command approval, WebAuthn/device trust | Architecture-level P4 work. | These all violate the requested next-slice constraints because they require external services, deployment/product decisions, UI/policy semantics, or sensitive evidence storage. | Not applicable for this next slice. |

### Recommended Next Slice

**Recommend: AppCredential local resolver seam for profile hook rendering.**

Recommended scope:

1. Add a small backend-internal local resolver/helper for AppCredential profile materialization.
   - Provider/source remains local-only.
   - Existing encrypted `model.AppCredential.Config` remains the only source of truth.
   - The resolver returns an in-memory config map for immediate `profile.RenderHooks` use plus safe non-secret metadata if needed.
2. Replace direct `db.First(&cred, id)` + `json.Unmarshal([]byte(cred.Config), &configMap)` in policy create/update with the resolver.
3. Replace direct old/new config map unmarshalling in AppCredential update/cascade with the same parsing/resolution helper where it preserves current behavior.
4. Preserve existing hook output exactly, including existing password-containing hook commands where templates currently render them.
5. Do not add migrations, provider tables, provider references, env vars, deployment docs, frontend/API fields, grant policy changes, external providers, SSH CA, recording, or command approval.

Why this is the smallest executable P4 slice now:

- P4-1/P4-2/P4-3 already covered SSH credentials and restic repository passwords; AppCredential config is the next remaining local encrypted credential class with direct materialization at runtime/policy boundaries.
- The active direct uses are concentrated in two handlers and one profile renderer (`policy_handler.go`, `app_credential_handler.go`, `profile.go`).
- The slice can be backend-only and behavior-preserving: no route shape, response DTO, frontend, migration, or deployment changes are required.
- Archived P4 research already listed AppCredential config loading for policy hook rendering as the next-smallest seam after SSH (`05-22-p4-credential-broker-foundation/research/broker-foundation-patterns.md:175-178`), and restic has since been completed.

Suggested out-of-scope for the slice:

- Enforcing AppCredential type/profile compatibility if not already enforced by current code.
- Changing generated hook command text, escaping behavior, storage of rendered hooks, or task execution behavior.
- Removing password material from rendered hooks or replacing hook execution with environment files/secret agents.
- Adding credential audit events for app profile rendering unless a separate product decision defines safe fields; current sanitizer drops keys/values containing `credential`, `config`, `password`, `secret`, `command`, `output`, `content`, or `payload` (`.trellis/spec/backend/quality-guidelines.md:398-401`).
- Config import/export provider-reference semantics.
- External Vault/KMS/secret manager references.

### Must-Cover Files and Tests for Recommended Slice

Implementation anchors:

| File | Required coverage |
|---|---|
| `backend/internal/api/handlers/policy_handler.go` | Replace policy create/update direct AppCredential load/unmarshal paths at `:257-274` and `:583-598` with the resolver; preserve existing bad-request/internal-error behavior where applicable. |
| `backend/internal/api/handlers/app_credential_handler.go` | Replace or centralize old/new config map parsing and cascade rendering paths at `:262-295` and `:306-340`; preserve password-retention update semantics. |
| `backend/internal/profile/profile.go` | Usually no behavior change; only use if the resolver is placed near profile rendering. Existing templates and `RenderHooks` output must remain equivalent. |
| `backend/internal/model/models.go` | Reference-only unless adding tiny metadata types elsewhere; keep `AppCredential.Config` hook behavior and `SanitizedConfig()` response behavior unchanged. |
| New resolver file/package if added | Should be backend-internal, local-only, no external clients, no env vars, no migrations, no API DTOs. Suggested concepts: resolve by AppCredential ID and parse a provided raw JSON string for the AppCredential update path. |

Tests to add/update:

| Test area | Must assert |
|---|---|
| Resolver unit tests | Encrypted `AppCredential.Config` loaded through GORM hooks resolves to the same config map; invalid JSON returns a sanitized error that does not include raw config or password; result metadata contains only safe local labels and no raw password/config. |
| Policy create/update tests | Existing app-profile hook rendering remains equivalent for generated pre/post hooks; missing credential still fails; user-supplied pre/post hooks still override generated hooks; invalid app profile behavior unchanged. |
| AppCredential update/cascade tests | Blank password update preserves prior password; cascade re-renders policies whose hooks match the old generated hooks; user-edited hooks remain unchanged; responses still omit `config.password`. |
| Existing profile tests | `profile.RenderHooks` behavior stays unchanged, including currently expected password-containing commands. |
| Response/storage safety tests | `AppCredential` API responses still omit raw password; `Config` remains encrypted at rest through hooks; no new API fields appear. |
| Negative safety tests | Resolver/loggable/auditable metadata and errors do not contain raw app password, decrypted config JSON, generated command text, hostnames/endpoints, or secret-shaped values. |
| Verification | Targeted backend tests for handlers/profile/resolver, then `go test ./... -count=1` from `backend/`. Frontend check is not required if no frontend files change. |

### Risks

1. **Rendered hook password behavior is intentional today.** Existing templates and tests expect app passwords to appear in generated hook commands. A provider seam must wrap materialization without silently removing/changing those commands.
2. **Rendered hooks are stored on `Policy`.** Centralizing config resolution does not remove the existing persistence of generated hook strings; this slice should not claim to eliminate that exposure.
3. **Metadata naming can collide with sanitizer rules.** Credential-audit metadata keys/values containing `credential`, `config`, `password`, `secret`, `command`, `output`, `content`, or `payload` are dropped by contract. If metadata is introduced, it must be tested against these rules or kept out of audit paths.
4. **GORM hooks mutate sensitive fields on save.** `BeforeSave` can replace plaintext config with encrypted text in the struct; AppCredential update currently derives old/new config maps before saving. Resolver code should not assume a post-save struct still contains plaintext.
5. **Do not add type/profile validation as a side effect.** `PolicyHandler` currently checks that the app profile exists, then renders with the selected credential config; changing compatibility checks would be a behavior change outside the provider-seam slice.
6. **Do not broaden logs/errors.** Existing logging specs forbid decrypted model-hook values, executor config, command output, endpoints/proxies, and unsafe credential metadata (`.trellis/spec/backend/logging-guidelines.md:68-81`). Resolver errors should be generic and safe.
7. **Integration secrets remain separate.** Completing the AppCredential seam would not cover notification endpoints/secrets/proxy URLs; those remain a later local-only candidate.

### External References

No external references were used. This was an internal codebase, archived Trellis, and spec research task.

### Related Specs

- `.trellis/spec/backend/database-guidelines.md:14-17` — sensitive fields are encrypted/decrypted through model hooks.
- `.trellis/spec/backend/database-guidelines.md:85-91` — credential audit rows must not contain raw credentials, decrypted executor config, terminal streams, command output, or file contents.
- `.trellis/spec/backend/database-guidelines.md:105-111` — do not expose raw secret-bearing model values and do not manually encrypt/decrypt sensitive fields in handlers.
- `.trellis/spec/backend/quality-guidelines.md:224-255` — purpose-aware SSH helper contract remains relevant for any adjacent SSH behavior.
- `.trellis/spec/backend/quality-guidelines.md:380-405` — credential-use audit safety contract and forbidden metadata categories.
- `.trellis/spec/backend/quality-guidelines.md:451-520` — credential access grants remain additive and must run before protected credential resolution where applicable.
- `.trellis/spec/backend/logging-guidelines.md:68-81` — logging exclusions for decrypted secrets, executor config, command output, endpoints/proxies, and unsafe audit metadata.
- `.trellis/spec/backend/error-handling.md:68-74` — client-facing errors must not expose sensitive internals or payloads.
- `.trellis/spec/backend/deployment-runtime.md:22-32` — official deployment should not gain external credential services/configuration for this local-only slice.

## Caveats / Not Found

- No tests were run; this is static research only.
- No existing AppCredential provider/resolver seam was found. Current code directly loads/unmarshals `AppCredential.Config` for policy hook rendering and cascade updates.
- No external Vault/KMS/provider-reference implementation, SSH CA, session-recording storage, or command-approval subsystem was found in current code.
- Restic repository access appears centralized in the current branch after P4-3; no remaining direct `extractResticPassword` helper was found in the inspected current code paths.
- Notification integration endpoint/secret/proxy materialization remains direct and is a likely follow-up after AppCredential if the project continues local-only provider seam expansion.
