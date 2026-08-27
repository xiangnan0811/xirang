# Implementation plan

## Preconditions

- Work only in `/home/murray/code/xirang/.worktrees/backup-assets-preview-authorization-ui`
  on `codex/backup-assets-preview-authorization-ui`.
- Reconcile the already merged/released branch with current `origin/main` without
  discarding this task's planning files. Do not write the main worktree.
- Load every file in `implement.jsonl` plus this PRD/design before coding.
- Keep production node-log collectors at zero.

## 1. Genuine RED and policy contract

- Add the smallest non-behavioral config/test plumbing needed to express the new
  boolean without changing transport decisions.
- First behavioral product action: add and run the hermetic official-template
  Nginx runtime flow described in the design. POST a probe ticket, retain its
  cookie, then perform exact-route GET/HEAD with Host, inbound XFP `https`,
  spoof-canary XFF, Range, and If-Range. It must fail because the released exact
  route neither appends XFF nor sanitizes the spoofed scheme to actual `http`.
  Record this real Nginx-hop RED before editing production behavior.
- Then run a production-shaped ticket-issue handler RED with immediate remote
  `127.0.0.1`, exact trusted `X-Forwarded-Proto: http`, XFF whose nearest
  untrusted hop is private, and the new opt-in true. Assert issuance and a non-
  Secure strict cookie. It must fail under the released policy with the current
  secure-transport 503 before policy implementation.
- Add RED matrix cases for public/spoofed/malformed evidence and serve-time
  private enforcement before making them GREEN.

## 2. Dynamic setting and config propagation

- Register `backup_assets.content_allow_insecure_private_network` with default
  false and add it to the content Foundation key set.
- Parse/project it through `backupasset.ContentConfig`, handler/export config
  sources, prospective transition, atomic snapshots, rollback fixtures, registry
  parity, and all static test maps.
- Preserve `content_allow_insecure_loopback` as an independent legacy field.
- Verify true/false/env/default validation and the production-composed dynamic
  transition. Induce apply and restoration failures and prove exact DB override
  presence/value plus runtime snapshot rollback and value-free audit behavior.

## 3. Shared transport policy

- Refactor `BackupContentSchemePolicy` to evaluate exact scheme and effective
  client from one trusted-proxy policy.
- Implement the approved exact XFP/XFF parsing, right-to-left trusted-hop peel,
  closed private/loopback allowlist, forwarded-HTTP-without-XFF denial, and other
  fail-closed negatives.
- Keep direct strict localhost compatibility separate.
- Apply the policy to normal/recovery/export issuance, the explicit
  `BackupArchiveHandler` archive-member delegate, and content Serve. Turning the
  setting off must immediately deny HTTP Serve while HTTPS stays available.
- Add the closed, parameter-free `secure_transport_required` 503 response only
  for JSON ticket transport denials; keep raw Serve status-only and all other 503
  products unchanged.
- Keep response/log details generic.

## 4. All-in-one Nginx

- Make the exact and shaped-fallback content routes overwrite XFP with `$scheme`,
  remove their client-derived effective-proto map dependency, and add only XFF to
  the exact route for serve-time classification; do not add X-Real-IP.
- Extend `scripts/check-asset-content-nginx.sh` and its mutation self-test to
  require the new forwarding and continue forbidding identity/resource fields in
  content logs.
- Add `scripts/check-asset-content-nginx-runtime.sh`: render the official template
  in the repository-configured Nginx image, add an in-container probe listener
  on port 3000, and execute the POST-cookie-GET-HEAD flow. Assert exact forwarded
  headers, cookie/byte behavior, inbound-XFP sanitization to actual `http`, and
  canary-free dedicated logs; always remove temporary containers, networks, and
  files on success or failure.
- Change `StructuredLogger` so content-shaped requests omit `client_ip` entirely
  for every method and malformed suffix while retaining the safe shaped route,
  method, status, latency, and request ID. Add exact/malformed/trailing/query/
  POST/OPTIONS canary capture plus mutation/source checks; leave non-content
  request logging unchanged.
- Beyond the approved XFP sanitization, do not alter public port, TLS ownership,
  route order, Range, buffering, cache, gzip, timeout, or Host behavior.

## 5. Admin Backups control

- Add a focused typed API module that reads the existing Settings response,
  validates the exact bool/source product, and writes only the new key.
- Add `PrivateNetworkContentTransportPanel` and mount it in Backups Overview for
  Admin only.
- Reuse Switch, AlertDialog, InlineAlert, Card, Button, and i18n resources.
- Test load/default/env/db, enabled warning, confirmation/cancel, exact PUT,
  disable, failure rollback, pending-write deduplication, saved live region,
  stable hash target/focus, Admin-only visibility, keyboard/axe behavior, and
  absence of value persistence or direct fetch. Permit only the exact static
  hash read/navigation required by the approved guidance target.
- Keep generic System `CATEGORY_ORDER` unchanged.

## 6. Transport-error UX across delivery surfaces

- Add a bounded parser for the exact
  `{reason:{code:"secure_transport_required",params:{}}}` error product. Keep it
  separate from `CatalogCapabilityCode`; do not infer it from localized messages.
- Add the distinct non-retryable error to native preview/original download and
  the Recovery, Export, and Archive state/classifier contracts.
- Render role-aware guidance on each surface: Admin gets an accessible link to
  `/app/backups/overview#backup-assets-content-transport`; Operator gets HTTPS-or-
  contact-Admin guidance without a settings action. Keep other 503 behavior
  unchanged.
- Test the exact reason, malformed/extra-param fallbacks, Admin action, Operator
  message, no Viewer privilege inference, and no identifier/client leakage for
  preview, original download, Export artifact, archive member, and Recovery
  result ticket errors.

## 7. Documentation

- Update `docs/deployment.md` and `docs/env-vars.md` for secure default, Admin UI,
  private-address definition, cleartext risk, env fallback, trusted-proxy caveat,
  and rollback.
- Do not add NAS vendor reverse-proxy instructions or claim built-in TLS.

## 8. Verification gates

Run focused selectors after each slice, then the final gates:

```bash
cd backend && go test ./internal/api/handlers -run 'BackupContent.*(Transport|Scheme|Private|Cookie|Serve)|BackupAssetExport.*(Transport|Private)|BackupArchive.*(Transport|Private|DeliveryTicket)' -count=1
cd backend && go test ./internal/api/handlers ./internal/settings ./internal/backupasset ./internal/backupasset/runtime -run 'PrivateNetwork|Settings.*Content|Content.*Private|FoundationTransition|RuntimeConfigAwareContent|Registry.*Backup' -count=1
cd backend && go test ./internal/api -run 'BackupContent|Export.*RBAC|Recovery.*Result' -count=1
cd backend && go test -race ./internal/api/handlers ./internal/middleware ./internal/settings ./internal/backupasset ./internal/backupasset/runtime -run 'PrivateNetwork|Backup(Content|AssetExport|Archive).*(Transport|Private|Serve|DeliveryTicket)|StructuredLogger.*Content|Settings.*Content|FoundationTransition|RuntimeConfigAwareContent|Content.*Private' -count=1
```

Repeat the focused normal selectors at least three consecutive times, then:

```bash
cd backend && go test ./...
cd backend && go build ./...
cd web && env -u NODE_ENV npx vitest run src/features/backup-assets/private-network-content-transport-panel.test.tsx src/lib/api/backup-content-transport-api.test.ts src/lib/api/backup-assets-error.test.ts src/features/backup-assets/asset-preview.test.tsx src/features/backup-assets/use-backup-recovery.test.tsx src/features/backup-assets/use-backup-asset-export.test.tsx src/features/backup-assets/use-backup-archive.test.tsx src/pages/backups-page.test.tsx
cd web && env -u NODE_ENV npm run check
./scripts/check-asset-content-nginx.sh
./scripts/check-asset-content-nginx.test.sh
./scripts/check-asset-content-nginx-runtime.sh
bash scripts/check-doc-freshness.sh
make check
git diff --check
```

If final filenames differ, use the exact focused files introduced by the
implementation and record them in evidence.

## 9. Privacy, compatibility, and diff checks

- Source-scan modified web code for direct `fetch`, `localStorage`,
  `sessionStorage`, history/location/router persistence, `any`, and
  `unknown as` bypass.
- Scan modified backend/deploy/docs code and test responses/log fixtures for raw
  XFF/client chains, tokens, proofs, Cookie values, tickets, Provider locators,
  backup paths, asset names/content, commands/output, or setting values.
- Capture application and Nginx content-route logs for every method and malformed
  shape and prove no `client_ip`, XFF, X-Real-IP, URI, query, cookie, request
  line, referrer, or user-agent value is emitted. Prove the structured logger
  still records the approved safe fields.
- Prove `CATEGORY_ORDER`, Catalog DTO/permissions, RBAC matrices, ticket/grant
  schema, migrations, Provider code, and node-log collector settings are
  unchanged.
- Record `git diff --stat`, `git diff --check`, and exact intended paths.

## 10. Independent check and delivery

- Dispatch a fresh `trellis-check` agent after all local gates. Resolve every
  Critical/Important finding on the same branch and rerun affected plus full
  gates.
- Main session commits, pushes, opens PR, monitors required CI, and squash-merges
  only when green.
- Monitor Release Please, release PR, GitHub Release, Docker publication, and
  post-release workflows. Fix failures on the same branch/task.

## 11. Production acceptance

- Use the existing guarded NAS pattern: read-only exact preflight, verified DB
  backup, exact Compose target/snapshot, timed pull/up/health, failure stop and
  rollback, schema/runtime/repository/Catalog/Search/task/collector/log checks.
- Commands must obey the established NAS constraints: root in
  `/volume2/docker/xirang`; external `19927`, internal `10761`; no `test`, `[`,
  `[[`, `cd`, `su`, or `sudo`; no printed secrets or content identifiers.
- Admin explicitly enables private-network HTTP in Backups Overview. Confirm the
  setting only by safe key/state output.
- Re-run Load Preview over LAN HTTP and accept only when the ticket succeeds and
  actual preview content renders. Do not request screenshots containing asset
  names or content.
- Keep collectors at zero until that point. Then hand control back to the approved
  node-log P1 workflow.

## Risky files and rollback points

- `backup_content_handler.go`: scheme/origin/client parsing and raw Serve status.
- `backup_asset_export_handler.go`: shared policy parity.
- `settings/service.go` and `backupasset/service.go`: Foundation snapshot and
  transition completeness.
- Nginx exact content location/checker: address evidence without log leakage.
- Structured logger: content-shaped client identity suppression without changing
  normal request telemetry.
- Admin toggle: explicit confirmation and exact-key mutation.
- Delivery error surfaces: exact safe transport reason and role-aware guidance
  without turning generic 503s into false settings advice.

Rollback the setting first. Code/image rollback is secondary and must preserve
the verified DB backup and Compose snapshot.
