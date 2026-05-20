# P1 Overall Security Review Before P2

## Conclusion

**P1 is accepted. P2 may start.**

No **Blocker before P2** was found in the reviewed scope. The released P1 slices (`v0.39.0` through `v0.42.0`) are coherent against the approved P1/P1b/P1c/P1d scope, and the reviewed backend, frontend, docs, tests, CI, release, and Docker publishing evidence support proceeding to P2.

## Evidence Reviewed

- Delivery/release evidence: [`research/p1-delivery-evidence.md`](research/p1-delivery-evidence.md)
- Cross-layer security review: [`research/p1-cross-layer-security-review.md`](research/p1-cross-layer-security-review.md)
- Active review PRD: [`prd.md`](prd.md)
- Archived P1 tasks:
  - `.trellis/tasks/archive/2026-05/05-19-security-p1-least-privilege-audit/`
  - `.trellis/tasks/archive/2026-05/05-19-security-p1b-credential-audit-extended-surfaces/`
  - `.trellis/tasks/archive/2026-05/05-19-security-p1c-credential-audit-ui-export/`
  - `.trellis/tasks/archive/2026-05/05-19-security-p1d-step-up-auth-high-risk/`
- Release tags: `v0.39.0`, `v0.40.0`, `v0.41.0`, `v0.42.0`

Sensitive samples were intentionally excluded from this report and the research files. Evidence is recorded as file paths, route names, action names, release identifiers, and bounded safe metadata categories only.

## Coverage Matrix

| P1 security goal | Backend evidence | Frontend/UI evidence | Tests/docs/release evidence | Result |
|---|---|---|---|---|
| SSH key least-privilege metadata and enforcement | `SSHKey` scope fields exist; managed key use goes through scope validation before private-key use; disabled/expired/purpose/node/tag checks are enforced at credential resolution. | SSH key API/frontend mapper expose scope metadata and broad-scope state without private key material. | Scope, SSH auth, SSH key handler, mapper tests; `v0.39.0` release evidence. | Covered |
| Existing unscoped key compatibility and broad-scope risk visibility | Empty purpose/node/tag scope remains permissive; broad-scope detection feeds Settings risk summary. | Settings/security summary and SSH key UI expose risk state without secret fields. | Scope/risk-summary tests; backend docs. | Covered |
| Credential-use audit coverage across high-risk and extended surfaces | Core and P1b surfaces write dedicated credential audit events with bounded metadata, including export, terminal, task, snapshot, config, file, Docker, diagnostics, preflight, probe, and node-log paths. | Credential audit UI/API action mapping includes P1/P1b/P1d action namespaces. | P1/P1b tests and `v0.39.0`/`v0.40.0` release evidence. | Covered |
| Admin-only credential audit list/export UI/API | Backend routes are `RequireRole("admin")`; list/export use bounded filters/sorts/limits and output-time sanitizer. | Route/nav/page hide or redirect non-admin access; frontend mapper re-sanitizes metadata before rendering/export handling. | Backend handler tests, frontend API/page/nav tests, `v0.41.0` release evidence. | Covered |
| Step-up enforcement for selected high-risk operations | `X-Xirang-Step-Up` proof is validated for purpose/user/role/token-version/TOTP-enabled state; high-risk REST routes and terminal WS enforce proof server-side. | API core attaches proof header; shared retry hook handles `STEP_UP_REQUIRED`; terminal WS sends proof in protocol auth; proof is session-scoped. | Backend step-up/terminal tests, frontend proof/storage/API/component tests, `v0.42.0` release evidence. | Covered |
| Settings/security risk summary alignment | Risk summary is admin-only and includes broad SSH key risk plus P1/P1b/P1d high-risk audit actions. | Credential audit action labels and Settings consumption align with backend action names. | Settings tests, backend route docs, release evidence. | Covered |
| Secret-safety invariants | Credential audit write path bounds metadata and denies sensitive markers; list/export re-sanitizes legacy records; model response paths omit private key/password fields. | Step-up proof stored only in `sessionStorage` and cleared on expiry/logout/401/retry failure; credential audit UI renders sanitized metadata. | Credential audit sanitizer tests, docs for sensitive-field handling, review report contains no raw secret/output samples. | Covered |
| Release and CI evidence | Feature/release PRs and tags exist for every slice. | N/A | GitHub releases, final CI status rollups, main/tag CI, and Docker publish evidence recorded for all reviewed tags. | Covered |

## Impact Radius Review

- **L1 — local report/task artifacts**: The acceptance review deliverables are limited to the active Trellis task directory and its `research/` evidence files.
- **L2 — backend security boundaries**: Reviewed route, handler, middleware, SSH scope, credential audit, Settings risk summary, and WebSocket auth boundaries that enforce P1 controls.
- **L3 — frontend/API boundaries**: Reviewed frontend API wrappers, direct download flows, step-up retry/proof handling, terminal WebSocket protocol auth, credential audit UI, navigation, and Settings consumption.
- **L4 — release/CI/documentation evidence**: Reviewed P1 release tags, feature/release PRs, CI/status rollups, Docker publish workflows, changelog/docs references, and targeted test evidence.
- **L5 — external/runtime production systems**: Out of scope for this acceptance review; no live infrastructure, destructive testing, external pentest, or production data inspection was performed.

## High-risk Operation Review

| Operation | Backend gate / bypass resistance | Frontend/direct-flow behavior | Audit and secret-safety result | Result |
|---|---|---|---|---|
| SSH key export `GET /ssh-keys/export` | Auth + audit + rate/body limit + `ssh_keys:read` RBAC + route-level step-up. | Direct-download helper sends bearer token and step-up header; `STEP_UP_REQUIRED` flows through retry logic. | Audit stores bounded counts/format/outcome, not key material. | Pass |
| Sensitive config export `GET /config/export?include_secrets=true` | Admin-only route plus conditional step-up for `include_secrets=true`; handler has defensive admin check. | API wrapper supports proof; default UI export path does not request secrets. | Audit records sensitive-export boolean/counts, not payload. | Pass |
| Terminal WebSocket `GET /ws/terminal` | WS primary token is protocol-authenticated; purpose-scoped JWTs are rejected; admin and step-up proof are required before SSH access. | Terminal obtains proof before opening WS and sends it in first auth message. | Audit records session/stage/duration/source metadata, not terminal stream. | Pass |
| Task manual trigger `POST /tasks/:id/trigger` | `tasks:trigger` RBAC + ownership check + route-level step-up before handler. | API wrapper and console operation hook pass proof via retry flow. | Audit stores bounded task/run/credential metadata. | Pass |
| Task batch trigger `POST /tasks/batch-trigger` | `tasks:write` RBAC; handler filters ownership; step-up runs before any eligible task execution. | API wrapper supports proof and exposed UI paths use step-up flow. | Executed/partial paths write bounded counts. All-blocked/no-op path noted as observation. | Pass |
| Task restore `POST /tasks/:id/restore` | Admin-only + route-level step-up. | API wrapper passes proof. | Audit records booleans/counts/IDs, not restore target path. | Pass |
| Snapshot restore `POST /tasks/:id/snapshots/:sid/restore` | Admin-only + route-level step-up. | API wrapper passes proof. | Audit truncates snapshot ID and stores include count/target-set boolean, not paths. | Pass |
| Batch command `POST /batch-commands` | `tasks:write` RBAC + ownership filtering + handler-level step-up before creation. | API wrapper passes proof. | Audit stores counts/retain flag, not command text/output. | Pass |
| Credential audit list/export | Backend admin-only list/export routes. | Non-admin UI redirect; CSV export uses backend-safe DTO path. | Backend and frontend both sanitize metadata/error fields. | Pass |
| Settings risk summary | Backend admin-only route. | Settings page consumes summary only. | Summary aggregates categories and counts, not raw metadata. | Pass |

## RBAC / Ownership / Step-up Composition

- Primary REST auth rejects purpose-scoped JWTs, so `2fa_pending` and `step_up` proofs cannot be reused as normal bearer tokens.
- Secured REST routes apply primary auth, generic audit middleware, API rate limiting, and body-size limits before route-specific RBAC/role/ownership/step-up gates.
- Representative admin case: admin-only routes still require step-up for selected high-risk operations; sensitive config export also has handler-level admin defense.
- Representative operator case: operator-permitted task/node operations remain constrained by ownership checks before step-up on per-object flows.
- Representative viewer case: viewer lacks write/trigger permissions and backend gates reject high-risk operations regardless of frontend visibility.
- WebSocket terminal auth composes as protocol primary auth + purpose-token rejection + admin check + step-up proof before SSH credential use.
- Direct downloads and exports cannot bypass backend enforcement because server routes enforce RBAC/admin/step-up independently of frontend behavior.

## Release and CI Evidence

| Slice | Release | Feature PR | Release PR | Release / CI result |
|---|---|---|---|---|
| P1 least-privilege SSH key audit | `v0.39.0` | #193 | #194 | GitHub release exists; final CI/status rollups succeeded; Docker publish succeeded. |
| P1b extended credential audit surfaces | `v0.40.0` | #195 | #196 | GitHub release exists; final CI/status rollups succeeded; Docker publish succeeded. |
| P1c credential audit UI/export | `v0.41.0` | #197 | #198 | GitHub release exists; final CI/status rollups succeeded; Docker publish succeeded. |
| P1d step-up auth high-risk operations | `v0.42.0` | #199 | #200 | GitHub release exists; final CI/status rollups succeeded; Docker publish succeeded; `v0.42.0` is latest. |

CI/release evidence includes backend lint/test/build/vulnerability workflows, frontend audit/check/build workflows, doc freshness, migration UTC safety, GitGuardian checks, tag/main CI, and release-triggered Docker multi-platform build/scan/manifest/provenance workflows.

## Findings

### Blocker before P2

None found.

### Non-blocking follow-up

None required before P2.

### Observations

1. `POST /tasks/batch-trigger` returns without step-up and without a `task.batch_trigger` credential audit event when every requested task is missing or filtered out before execution. This does not bypass execution controls because no task runs; successful and partial execution paths enforce step-up and write bounded audit counts. A future hardening task may add sanitized attempted-action telemetry for this no-op path if desired.
2. Release-note traceability differs by tag: `v0.39.0` and `v0.40.0` release bodies link both feature PR and commit, while `v0.41.0` and `v0.42.0` link feature commits but not PR #197/#199 directly. The PRs are still discoverable and recorded in this review, so this does not block P2.

## Acceptance Criteria Status

- [x] Coverage matrix maps P1/P1b/P1c/P1d requirements to code, tests, docs, release evidence, or justified observations.
- [x] High-risk operations were checked for backend enforcement, direct API/download bypass resistance, frontend proof/retry behavior where applicable, and audit evidence.
- [x] RBAC/ownership/step-up interactions were reviewed for representative admin/operator/viewer and owner/non-owner cases.
- [x] Credential audit metadata and response/export/UI rendering paths were reviewed for forbidden sensitive fields and unbounded raw evidence.
- [x] Release and CI evidence for `v0.39.0` through `v0.42.0` was recorded.
- [x] Findings were classified as blocker, non-blocking follow-up, or observation with concrete surfaces.
- [x] Final conclusion states P1 is accepted and P2 may start.

## Final Go / No-go Decision

**Go for P2.**

P1/P1b/P1c/P1d are accepted as a completed phase. The review found no blocking defect in least-privilege SSH key enforcement, credential-use audit coverage, credential audit UI/export safety, step-up enforcement, RBAC/ownership composition, direct download/WebSocket bypass resistance, release/CI evidence, or secret-safe metadata handling.
