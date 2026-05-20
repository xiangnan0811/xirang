# P1 Delivery Evidence

## Scope Reviewed

- Active acceptance review PRD: `.trellis/tasks/05-20-p1-overall-security-review-before-p2/prd.md` — review goal and scope require P1/P1b/P1c/P1d release/CI evidence through `v0.42.0` (`prd.md:5`, `prd.md:19-36`, `prd.md:50`).
- P1 archived task: `.trellis/tasks/archive/2026-05/05-19-security-p1-least-privilege-audit/prd.md` — SSH key least-privilege metadata/enforcement, credential-use audit, security risk summary, tests, and follow-up roadmap (`prd.md:28-44`, `prd.md:111-113`).
- P1b archived task: `.trellis/tasks/archive/2026-05/05-19-security-p1b-credential-audit-extended-surfaces/prd.md` — extended audit coverage for SFTP/file, Docker volume, config export, diagnostics, migration preflight, and bounded background/system events (`prd.md:15-34`, `prd.md:75-81`).
- P1c archived task: `.trellis/tasks/archive/2026-05/05-19-security-p1c-credential-audit-ui-export/prd.md` — admin-only credential audit list/export APIs and frontend review UI with output-time sanitization (`prd.md:9-33`, `prd.md:61-67`).
- P1d archived task: `.trellis/tasks/archive/2026-05/05-19-security-p1d-step-up-auth-high-risk/prd.md` — TOTP-backed step-up proof and backend gates for high-risk operations (`prd.md:9-43`, `prd.md:45-56`).
- Release tags reviewed: `v0.39.0`, `v0.40.0`, `v0.41.0`, `v0.42.0`.
- Evidence sources: local git history/tags, GitHub releases/PRs/Actions via `gh`, workflow definitions, and current code/docs/tests at the `v0.42.0` line of development. No raw logs, secrets, terminal streams, or host-sensitive samples are included.

## Release and PR Evidence

| Slice | Release tag | Main commits / PRs | CI / release evidence | Confidence |
|---|---|---|---|---|
| P1 — least-privilege SSH key audit | `v0.39.0` (`d8e3c45`, 2026-05-19) | Feature merge commit `ecea8b6` / [PR #193](https://github.com/xiangnan0811/xirang/pull/193) `feat(security): add SSH key least-privilege audit`; release PR [#194](https://github.com/xiangnan0811/xirang/pull/194). GitHub release: [v0.39.0](https://github.com/xiangnan0811/xirang/releases/tag/v0.39.0) targets `d8e3c45` and release body links `#193` + `ecea8b6`. | PR #193 and #194 status rollups show successful CI jobs for backend, frontend, doc freshness, migration UTC safety, and GitGuardian. Main/tag CI runs succeeded (`26098762330`, `26098942593`, `26098955802`). Docker publish workflow [26098955926](https://github.com/xiangnan0811/xirang/actions/runs/26098955926) succeeded with `Build & Scan (linux/amd64)`, `Build & Scan (linux/arm64)`, `Scan image for vulnerabilities`, `Publish manifest`, and `Attest build provenance` jobs/steps. | High |
| P1b — extended credential audit surfaces | `v0.40.0` (`d3c176d`, 2026-05-20 local tag date; GitHub release published 2026-05-19 UTC) | Feature merge commit `e92618b` / [PR #195](https://github.com/xiangnan0811/xirang/pull/195) `feat(security): extend credential audit coverage`; release PR [#196](https://github.com/xiangnan0811/xirang/pull/196). GitHub release: [v0.40.0](https://github.com/xiangnan0811/xirang/releases/tag/v0.40.0) targets `d3c176d` and release body links `#195` + `e92618b`. | Final PR #195 and #196 status rollups show successful CI jobs for backend, frontend, doc freshness, migration UTC safety, and GitGuardian. Main/tag CI runs succeeded (`26113206612`, `26113393446`, `26113402385`). Docker publish workflow [26113401872](https://github.com/xiangnan0811/xirang/actions/runs/26113401872) succeeded with both platform build/scan jobs, publish manifest, and provenance. One earlier P1b PR run failed before a later refresh/fix; final merge/release evidence is successful. | High |
| P1c — credential audit review UI/export | `v0.41.0` (`413b4d4`, 2026-05-20) | Feature merge commit `5d257bf` / [PR #197](https://github.com/xiangnan0811/xirang/pull/197) `feat(security): add credential audit review UI`; release PR [#198](https://github.com/xiangnan0811/xirang/pull/198). GitHub release: [v0.41.0](https://github.com/xiangnan0811/xirang/releases/tag/v0.41.0) targets `413b4d4` and release body links feature commit `5d257bf`. | PR #197 and #198 status rollups show successful CI jobs for backend, frontend, doc freshness, migration UTC safety, and GitGuardian. Main/tag CI runs succeeded (`26137042912`, `26137307998`, `26137313747`). Docker publish workflow [26137313792](https://github.com/xiangnan0811/xirang/actions/runs/26137313792) succeeded with both platform build/scan jobs, publish manifest, and provenance. | High |
| P1d — step-up auth high-risk operations | `v0.42.0` (`d4f925e`, 2026-05-20) | Feature merge commit `55faa35` / [PR #199](https://github.com/xiangnan0811/xirang/pull/199) `feat(security): add step-up auth for high-risk operations`; release PR [#200](https://github.com/xiangnan0811/xirang/pull/200). GitHub release: [v0.42.0](https://github.com/xiangnan0811/xirang/releases/tag/v0.42.0) targets `d4f925e` and release body links feature commit `55faa35`. | PR #199 and #200 status rollups show successful CI jobs for backend, frontend, doc freshness, migration UTC safety, and GitGuardian. Main/tag CI runs succeeded (`26149434770`, `26149611768`, `26149619569`). Docker publish workflow [26149619131](https://github.com/xiangnan0811/xirang/actions/runs/26149619131) succeeded with both platform build/scan jobs, publish manifest, and provenance. | High |

## Acceptance Evidence by Slice

### P1 — SSH key least privilege + core credential-use audit

**Approved goals**

- Add SSH key disabled/expiry/purpose/node/tag scope metadata, enforce it at backend use boundaries, preserve compatibility for existing unscoped keys, add dedicated credential-use audit events, and surface security-risk signals (`.trellis/tasks/archive/2026-05/05-19-security-p1-least-privilege-audit/prd.md:28-44`).
- Follow-up roadmap explicitly split P1b/P1c/P1d from the core P1 slice (`prd.md:111-113`).

**Implemented surfaces**

- Data model includes `SSHKey.Disabled`, `ExpiresAt`, `AllowedPurposes`, `AllowedNodeIDs`, `AllowedNodeTags`, and `LastUsedAt` while keeping `PrivateKey` hidden from JSON (`backend/internal/model/models.go:27-41`).
- Dedicated credential audit model stores actor/context/action/purpose/credential/outcome/metadata fields and documents that it must not contain raw secrets, terminal streams, command output, or executor config (`backend/internal/model/models.go:421-443`).
- Paired SQLite/PostgreSQL migration `000059_ssh_key_scope_credential_audit` adds the SSH key scope fields with permissive defaults and creates/indexes `credential_audit_events` (`backend/internal/database/migrations/sqlite/000059_ssh_key_scope_credential_audit.up.sql:1-43`; `backend/internal/database/migrations/postgres/000059_ssh_key_scope_credential_audit.up.sql:3-45`).
- Scope enforcement helper rejects disabled/expired/out-of-purpose/out-of-node/out-of-tag keys, while empty purpose/node/tag scope remains broad/permissive (`backend/internal/sshutil/scope.go:128-157`).
- SSH key API response and frontend mapper carry least-privilege metadata without private-key exposure (`backend/internal/api/handlers/ssh_key_handler.go:34-47`, `backend/internal/api/handlers/ssh_key_handler.go:102-103`; `web/src/lib/api/ssh-keys-api.ts:11-15`, `web/src/lib/api/ssh-keys-api.ts:48-65`).
- SSH key test/export paths write credential audit evidence and export validates purpose scope (`backend/internal/api/handlers/ssh_key_handler.go:458`, `backend/internal/api/handlers/ssh_key_handler.go:563`, `backend/internal/api/handlers/ssh_key_handler.go:668-716`).
- Settings security risk summary aggregates reused/broad/disabled/expired/stale SSH key and high-risk credential-audit signals (`backend/internal/api/handlers/settings_handler.go:206-242`, `backend/internal/api/handlers/settings_handler.go:284-459`, `backend/internal/api/handlers/settings_handler.go:468-494`).

**Key tests / checks / docs evidence**

- Scope tests cover empty broad scope and disabled/expired/purpose/node/tag denial (`backend/internal/sshutil/scope_test.go:11`, `backend/internal/sshutil/scope_test.go:20`).
- Credential audit tests cover timestamping, error redaction, metadata sanitization, and bounded fields (`backend/internal/credentialaudit/audit_test.go:15`, `audit_test.go:51`, `audit_test.go:86`).
- SSH key handler tests cover scope field preservation/clearing and non-admin export visibility (`backend/internal/api/handlers/ssh_key_handler_test.go:179`, `ssh_key_handler_test.go:197`, `ssh_key_handler_test.go:256-298`).
- Settings risk summary tests cover reused, broad, disabled-in-use, expired-in-use, and stale key signals (`backend/internal/api/handlers/settings_handler_test.go:191-207`).
- Frontend SSH key mapper tests cover disabled/expiry/purpose/node/tag fields (`web/src/lib/api/ssh-keys-api.test.ts:29-33`, `ssh-keys-api.test.ts:128-132`).
- Backend docs list step-up and credential audit routes after later P1d/P1c additions (`backend/README_backend.md:43`, `backend/README_backend.md:98`, `backend/README_backend.md:259-260`).

**Gaps / unknowns in this evidence pass**

- This pass verified delivery/release traceability and key source surfaces, not a full line-by-line acceptance review of every P1 enforcement call site.

### P1b — extended credential audit coverage

**Approved goals**

- Add explicit credential-use audit to SFTP/file reads, Docker volume discovery, config export, node doctor diagnostics, migration preflight, and bounded background/system SSH surfaces while avoiding raw file content, command output, diagnostic evidence, exported payloads, or secret material (`.trellis/tasks/archive/2026-05/05-19-security-p1b-credential-audit-extended-surfaces/prd.md:15-34`).
- Decision selected handler-driven explicit events plus bounded background audit, not exhaustive per-dial telemetry (`prd.md:75-81`).

**Implemented surfaces**

- File list/preview writes credential audit events at list/preview/dial/validation/read stages; helper centralizes safe metadata (`backend/internal/api/handlers/file_handler.go:96-129`, `file_handler.go:181-250`, `file_handler.go:333-352`).
- Docker volume listing writes credential audit events for dial/list/success outcomes without persisting remote output or volume names (`backend/internal/api/handlers/docker_handler.go:65-115`).
- Config export writes credential audit events for normal export and `include_secrets=true` paths without persisting exported payloads (`backend/internal/api/handlers/config_handler.go:65`, `config_handler.go:228`).
- Node doctor diagnostics write safe credential audit evidence (`backend/internal/api/handlers/node_doctor_handler.go:88-92`, `node_doctor_handler.go:176-206`).
- Migration preflight resolves node credential context and writes preflight audit events (`backend/internal/api/handlers/node_migrate_preflight_handler.go:246`, `node_migrate_preflight_handler.go:377-413`).
- Background/system SSH evidence exists for node logs and probe/metrics failures with bounded/system-actor behavior (`backend/internal/nodelogs/ssh_runner.go:33-88`; `backend/internal/probe/prober.go:152-154`, `prober.go:262-309`, `prober.go:325-382`).
- Credential audit writer sanitizes actor/action/error/metadata fields, caps metadata entries, and drops forbidden key/value markers including private/password/token/secret/credential/config/output/stream/command/content/payload (`backend/internal/credentialaudit/audit.go:144-174`, `audit.go:208-276`, `audit.go:291-292`).

**Key tests / checks / docs evidence**

- File browser audit test verifies path/preview content are not persisted (`backend/internal/api/handlers/file_handler_validate_test.go:328`).
- Docker audit test verifies remote output/volume names are not persisted (`backend/internal/api/handlers/docker_handler_test.go:31`).
- Config export tests cover default secret omission, blocked secret export audit, admin secret export audit without payload, and import/export preservation of SSH key scope metadata (`backend/internal/api/handlers/config_handler_test.go:143`, `config_handler_test.go:251`, `config_handler_test.go:290`, `config_handler_test.go:349`).
- Node doctor and migration preflight tests cover safe diagnostic audit and outcome classification (`backend/internal/api/handlers/node_doctor_handler_test.go:127`, `node_doctor_handler_test.go:183`, `node_doctor_handler_test.go:284`; `backend/internal/api/handlers/node_handler_test.go:259`, `node_handler_test.go:321`).
- Probe tests cover bounded failure tracking and audit threshold behavior (`backend/internal/probe/prober_test.go:116`, `prober_test.go:134`).
- Settings risk summary extends high-risk credential audit action aggregation (`backend/internal/api/handlers/settings_handler.go:450-459`, `settings_handler.go:468-494`).

**Gaps / unknowns in this evidence pass**

- Background audit was intentionally bounded rather than exhaustive per SSH dial per the approved P1b decision; this is an accepted scope boundary, not a missing implementation item in the reviewed PRD.

### P1c — credential audit list/export UI/API

**Approved goals**

- Add admin-only backend list/export routes, safe DTOs, pagination/sort/filtering, CSV export, output-time metadata/error sanitization, frontend mappers, admin-only UI, navigation hiding for non-admin users, and tests (`.trellis/tasks/archive/2026-05/05-19-security-p1c-credential-audit-ui-export/prd.md:9-33`).

**Implemented surfaces**

- Router registers admin-only backend routes: `GET /credential-audit-events` and `GET /credential-audit-events/export` (`backend/internal/api/router.go:260-263`).
- List handler builds filtered query, applies pagination/sort, and returns safe mapped DTOs (`backend/internal/api/handlers/credential_audit_handler.go:138-155`).
- Export handler uses the same filter semantics, bounded limit, UTF-8 BOM, CSV headers, and safe DTO mapping (`backend/internal/api/handlers/credential_audit_handler.go:179-213`).
- Frontend API module and client include credential audit API composition (`web/src/lib/api/client.ts:4`; `web/src/lib/api/credential-audit-api.ts`).
- Frontend route lazy-loads `/app/credential-audit` (`web/src/router-pages.tsx:32-33`; `web/src/router.tsx:103-104`).
- Navigation includes an admin-only Credential Audit entry (`web/src/components/layout/navigation.ts:118-119`).
- i18n includes credential audit labels/errors in English/Chinese (`web/src/i18n/locales/en.ts:95`, `en.ts:1369-1408`; `web/src/i18n/locales/zh.ts:1369-1371`).

**Key tests / checks / docs evidence**

- Backend handler tests cover filters/pagination/sort, legacy metadata/error sanitization, and CSV export safety (`backend/internal/api/handlers/credential_audit_handler_test.go:19`, `credential_audit_handler_test.go:93`, `credential_audit_handler_test.go:177`).
- Frontend mapper tests cover snake_case normalization, unknown values, redaction of unsafe error/metadata, metadata parsing, and filter serialization (`web/src/lib/api/credential-audit-api.test.ts:5`, `credential-audit-api.test.ts:51`, `credential-audit-api.test.ts:63`, `credential-audit-api.test.ts:76`, `credential-audit-api.test.ts:109`).
- Frontend page tests cover filtering, pagination, safe detail metadata, CSV export success/403, and non-admin access behavior (`web/src/pages/credential-audit-page.test.tsx:117`, `credential-audit-page.test.tsx:165`, `credential-audit-page.test.tsx:198`, `credential-audit-page.test.tsx:223`, `credential-audit-page.test.tsx:261`, `credential-audit-page.test.tsx:277`).
- Navigation test confirms credential audit nav is admin-only (`web/src/components/layout/navigation.test.ts:17-20`).
- Backend docs list credential audit list/export routes (`backend/README_backend.md:259-260`).

**Gaps / unknowns in this evidence pass**

- GitHub release notes for `v0.41.0` link the feature commit but do not include PR #197 in the body; PR evidence is still discoverable through GitHub PR search and status rollups.

### P1d — TOTP-backed step-up for high-risk operations

**Approved goals**

- Add short-lived step-up JWT proof flow based on TOTP/JWT, backend verification endpoint, backend gates for selected high-risk operations, additive RBAC/ownership semantics, machine-readable step-up-required response, frontend prompt/retry/proof attachment, and credential audit evidence without sensitive values (`.trellis/tasks/archive/2026-05/05-19-security-p1d-step-up-auth-high-risk/prd.md:9-43`).

**Implemented surfaces**

- Auth route registers `POST /auth/step-up` (`backend/internal/api/router.go:130-138`; handler at `backend/internal/api/handlers/auth_handler.go:384-419`).
- Step-up helper defines dedicated header `X-Xirang-Step-Up`, error code `STEP_UP_REQUIRED`, 300-second proof TTL, proof validation against purpose/user/role/token version/TOTP-enabled state, sanitized 403 envelope, and bounded credential-audit evidence (`backend/internal/api/handlers/step_up.go:17-23`, `step_up.go:58-75`, `step_up.go:77-111`, `step_up.go:114-123`, `step_up.go:125-153`).
- Router/middleware gates high-risk REST routes: SSH key export, task trigger, task restore, snapshot restore, and secret-including config export (`backend/internal/api/router.go:228`, `router.go:281-287`, `router.go:307-320`).
- Batch trigger and batch command creation enforce step-up inside handlers where per-request predicates/operation context are needed (`backend/internal/api/handlers/task_handler.go:717`; `backend/internal/api/handlers/batch_handler.go:113`).
- Terminal WebSocket route is outside the secured group and enforces proof in the first protocol auth message before opening SSH (`backend/internal/api/router.go:365`; `backend/internal/api/handlers/terminal_handler.go:56`, `terminal_handler.go:214-226`).
- Frontend API attaches the step-up header when provided and recognizes `STEP_UP_REQUIRED` without treating it as session expiry (`web/src/lib/api/core.ts:58-59`, `core.ts:173`).
- Frontend proof storage is session-scoped and clears expired proof (`web/src/lib/step-up-storage.ts:40-63`).
- Frontend shared retry hook requests proof and retries the original action on step-up-required responses (`web/src/hooks/use-step-up-action.ts:8-25`).
- Frontend TOTP API requests proof via `/auth/step-up` with code-only payload (`web/src/lib/api/totp-api.ts:61-62`).

**Key tests / checks / docs evidence**

- Backend step-up tests cover proof issuance, disabled/invalid TOTP, missing/invalid/expired/wrong-user/token-version mismatch, purpose-scoped token rejection as primary auth, RBAC/ownership preservation, secret-only config export gating, terminal WebSocket proof gating, and snapshot restore audit evidence (`backend/internal/api/handlers/step_up_test.go:184`, `step_up_test.go:231`, `step_up_test.go:275`, `step_up_test.go:337`, `step_up_test.go:362`, `step_up_test.go:392`, `step_up_test.go:419`, `step_up_test.go:481`).
- Frontend tests cover step-up proof header on high-risk task/batch requests, code-only proof request, `STEP_UP_REQUIRED` handling without login redirect, session storage expiry/clear, prompt/retry hook, terminal WS proof, and direct SSH key export proof attachment (`web/src/lib/api/client.test.ts:130`, `client.test.ts:148`, `client.test.ts:165`, `client.test.ts:223`; `web/src/lib/step-up-storage.test.ts:10`, `step-up-storage.test.ts:20`, `step-up-storage.test.ts:29`; `web/src/hooks/use-step-up-action.test.tsx:42`, `use-step-up-action.test.tsx:57`; `web/src/components/web-terminal.test.tsx:96`, `web-terminal.test.tsx:114`; `web/src/components/ssh-key-export-dialog.test.tsx:26`, `ssh-key-export-dialog.test.tsx:39`).
- Backend docs list high-risk step-up surfaces including SSH key export, task triggers/restores, batch commands, snapshot restore, sensitive config export, and terminal WebSocket (`backend/README_backend.md:43`, `README_backend.md:98`, `README_backend.md:128-134`, `README_backend.md:143`, `README_backend.md:280`, `README_backend.md:294`, `README_backend.md:314`).

**Gaps / unknowns in this evidence pass**

- GitHub release notes for `v0.42.0` link the feature commit but do not include PR #199 in the body; PR evidence is still discoverable through GitHub PR search and status rollups.

## CI / Release Evidence

### Workflow definitions in repository

- CI workflow `.github/workflows/ci.yml` runs on `push` and `pull_request` (`ci.yml:3-5`) with:
  - Backend job: Go setup, golangci-lint, `go test -coverprofile=coverage.out ./...`, `go build ./...`, and `govulncheck ./...` (`ci.yml:28-64`).
  - Frontend job: Node setup, `npm ci`, `npm audit --audit-level=moderate`, `npm run check`, and bundle budget check (`ci.yml:75-103`).
  - Doc freshness job: `scripts/check-doc-freshness.sh` and self-test (`ci.yml:114-127`).
  - Migration UTC safety job: migration linter and self-test (`ci.yml:129-140`).
- Release Please workflow `.github/workflows/release-please.yml` runs on pushes to `main` and prepares release PRs using `release-please-action` with repository config/manifest (`release-please.yml:3-27`).
- Publish Docker Images workflow `.github/workflows/publish-images.yml` runs on GitHub release publication and manual dispatch (`publish-images.yml:3-16`), builds `docker.io/linnea7171/xirang` for `linux/amd64` and `linux/arm64` (`publish-images.yml:27-44`), scans each digest with Trivy for HIGH/CRITICAL vulnerabilities (`publish-images.yml:126-139`), creates/pushes a manifest (`publish-images.yml:234-277`), and attests provenance (`publish-images.yml:291-296`).

### GitHub releases and publish evidence

- GitHub releases exist and are not draft/prerelease for all four tags:
  - [v0.39.0](https://github.com/xiangnan0811/xirang/releases/tag/v0.39.0), published `2026-05-19T13:03:10Z`, target `d8e3c45`.
  - [v0.40.0](https://github.com/xiangnan0811/xirang/releases/tag/v0.40.0), published `2026-05-19T17:18:45Z`, target `d3c176d`.
  - [v0.41.0](https://github.com/xiangnan0811/xirang/releases/tag/v0.41.0), published `2026-05-20T02:21:16Z`, target `413b4d4`.
  - [v0.42.0](https://github.com/xiangnan0811/xirang/releases/tag/v0.42.0), published `2026-05-20T08:02:22Z`, target `d4f925e`; marked Latest in the release list.
- Docker publish workflow succeeded for every release tag reviewed:
  - [26098955926](https://github.com/xiangnan0811/xirang/actions/runs/26098955926) for `v0.39.0`.
  - [26113401872](https://github.com/xiangnan0811/xirang/actions/runs/26113401872) for `v0.40.0`.
  - [26137313792](https://github.com/xiangnan0811/xirang/actions/runs/26137313792) for `v0.41.0`.
  - [26149619131](https://github.com/xiangnan0811/xirang/actions/runs/26149619131) for `v0.42.0`.
- Each reviewed Docker publish run includes successful `Build & Scan (linux/amd64)`, `Build & Scan (linux/arm64)`, and `Publish manifest` jobs. Within those jobs, the `Scan image for vulnerabilities`, `Create and push manifest`, and `Attest build provenance` steps completed successfully.

### PR / merge CI evidence

- Feature PRs merged to `main` with successful final CI/status rollups:
  - P1: [PR #193](https://github.com/xiangnan0811/xirang/pull/193), merged `2026-05-19T12:59:36Z`, merge commit `ecea8b6`.
  - P1b: [PR #195](https://github.com/xiangnan0811/xirang/pull/195), merged `2026-05-19T17:15:02Z`, merge commit `e92618b`.
  - P1c: [PR #197](https://github.com/xiangnan0811/xirang/pull/197), merged `2026-05-20T02:12:41Z`, merge commit `5d257bf`.
  - P1d: [PR #199](https://github.com/xiangnan0811/xirang/pull/199), merged `2026-05-20T07:58:33Z`, merge commit `55faa35`.
- Release PRs merged to `main` with successful final CI/status rollups:
  - [PR #194](https://github.com/xiangnan0811/xirang/pull/194) for `v0.39.0`.
  - [PR #196](https://github.com/xiangnan0811/xirang/pull/196) for `v0.40.0`.
  - [PR #198](https://github.com/xiangnan0811/xirang/pull/198) for `v0.41.0`.
  - [PR #200](https://github.com/xiangnan0811/xirang/pull/200) for `v0.42.0`.
- Main/tag CI runs after merge/release succeeded for all reviewed tags. The evidence pass saw successful CI for feature main pushes, release main pushes, and tag pushes; no final merge/release CI failure remains open in the reviewed evidence.

## Findings

### Blocker before P2

- No delivery/release-evidence blocker found in this research scope. All four P1 release tags have GitHub releases, merged feature/release PR evidence where discoverable, successful final CI status rollups, successful main/tag CI runs, and successful Docker publish workflows. This is not a substitute for the main acceptance review's full code-level go/no-go decision.

### Non-blocking follow-up

- Release-note traceability differs by tag: `v0.39.0` and `v0.40.0` release bodies link both feature PR and commit, while `v0.41.0` and `v0.42.0` release bodies link feature commits but not PR #197/#199. PRs are still discoverable and linked in this evidence file, so this does not block P2.

### Observations

- The P1 roadmap was delivered as four sequential security slices: P1 in `v0.39.0`, P1b in `v0.40.0`, P1c in `v0.41.0`, and P1d in `v0.42.0`, matching the active review PRD's known release mapping (`.trellis/tasks/05-20-p1-overall-security-review-before-p2/prd.md:11-15`, `prd.md:88-92`).
- The CI definition provides backend lint/test/build/vulnerability scanning, frontend audit/check/build-budget coverage, doc freshness checks, and migration UTC safety checks. GitHub status rollups also show GitGuardian secret scanning checks on reviewed PRs.
- Docker release automation published all four reviewed tags through the same release-triggered workflow, with per-platform build/scans, manifest publishing, and provenance attestation.
- P1b had an earlier failing PR CI run before a later refresh/fix; the final PR, main, release, and Docker publish evidence for `v0.40.0` are all successful.
- Source-level implementation references exist for every major accepted surface: SSH key least-privilege metadata/enforcement, credential audit storage/sanitization, P1b extended audit surfaces, P1c list/export UI/API, P1d REST/WebSocket step-up gates, frontend proof handling, tests, and route documentation.
