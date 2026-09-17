# Task reconciliation — 2026-09-17

## Current authorized continuation

Backup acceptance and its parent were archived in #540 after the user's real-data
and UI confirmations. This existing task now owns the three bounded changes in
[design.md](design.md) and [implement.md](implement.md): API middleware error
envelopes, panel RAF lifetime, and Hub queue-overflow warning volume. Current
inspection confirms all three remain relevant; no new task or protocol redesign
is required. Implementation and independent verification are complete; see
[verification](research/verification-2026-09-17.md). Earlier delivered work remains
delivered. Archive this existing task with the scoped fixes and deliver through
required PR/post-merge CI; local completion is not a NAS upgrade or release.

The runtime logging disposition is module-scoped: repair the demonstrated
per-dropped-event warning flood and touched Hub debug logging, while retaining
ordinary terminal/reporting/SSH/migration diagnostics and intentional pre-init
config logging. This is not a claim that every historical log.Printf was removed.
The previously documented WebSocket/KDF exclusions remain design dispositions,
not unfinished implementation within the current authorized repair scope.

## Historical reconciliation — 2026-09-16

This disposition supersedes stale current-state, delivery and execution instructions in this task; old evidence remains historical. User authorized evidence-based archival and consolidation on 2026-09-16.

Retain independent P3 backlog: API middleware envelope, panel-editor RAF cleanup, and bounded runtime logging. Keep current WebSocket protocol; defer KDF salt migration pending a concrete threat/compatibility design.

This task remains open; assessment does not authorize a production change or imply new implementation has run.

Full inventory and evidence: `.trellis/workspace/weibo/task-reconciliation-2026-09-16.md`. No product code, production configuration or Provider data was changed by this reconciliation. No new task was created.

## Current bounded backlog

This backlog is independent of the archived audit wrapper. The six P1/P2 slices
were delivered in PR #367; retaining the wrapper adds no execution ownership.

1. **API middleware error envelope — worth doing.** Current
   `backend/internal/middleware/auth.go:37-91`, `rbac.go:104-117` and
   `ownership.go:31-105` emit legacy `{error}`, while
   `web/src/lib/api/core.ts:271-278` extracts `{code,message}`. Enumerate only
   `/api/v1` JSON errors; keep metrics, streams, content delivery and WebSocket
   upgrade protocols explicit. Acceptance must cover status codes, 401/403/400/
   500/429 responses, Retry-After, safe messages and frontend extraction.
2. **Panel editor RAF cleanup — worth doing as a small change.**
   `web/src/pages/dashboards/panel-editor-dialog.tsx:144-151` stores a second RAF
   ID on the React state setter via an `unknown` cast. Use effect-local ownership
   or a ref, preserve the two-frame layout wait and check close/reopen/unmount
   cancellation. Do not redesign dashboard rendering.
3. **Runtime structured logging — opportunistic, by module.** Remaining examples
   exist in terminal handlers, reporting, ws/hub, sshutil and migrator. Preserve
   diagnostics and redaction. `config/config.go` intentionally runs before logger
   initialization and is not subject to a mechanical zero-log.Printf target.

## Decisions not to pursue now

- **Generic WebSocket protocol rewrite:** retain the current bounded first-frame
  protocol. `ws/hub.go:258-306`, terminal first-frame handling, and
  `api/handlers/realtime_auth.go:23-60` already cover connection/frame/time bounds,
  purpose, durable revocation, role and token version. Existing hub/terminal tests
  cover oversize frames and revocation. This is a design disposition, not proof
  that every historical B-P2-8 wording was implemented. Reopen only for a concrete
  defect, measurable operational requirement or missing security contract.
- **Argon2 per-deployment salt:** the parent had this deferred item even though
  the old P3 PRD omitted it. `secure/crypto.go:27,86-92` retains the fixed KDF salt
  for passphrase inputs; sufficiently long base64 keys follow the direct-key
  path. No migration is performed or claimed complete. Reconsider only with a
  threat model and versioned ciphertext/key recovery/migration design; silently
  changing the salt would break existing data access.

Source inspection only in this session; no product tests or fixes were run.
Remaining implementation should be scoped and validated per slice, not reopened
as a broad audit.

## Previous metadata note (historical)

Partial only: CAPTCHA rate limiting, structured bootstrap logging, Service Worker controllerchange, automation any cleanup, and CSP tightening are done. Middleware envelope sweep, panel-editor RAF ref, WebSocket auth evaluation, and remaining log cleanup stay deferred; do not archive this child.
