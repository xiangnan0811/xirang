# Backup assets private network HTTP content delivery

## Goal

Make Xirang's complete backup-asset content-delivery experience usable on its
primary self-hosted deployment shape: a NAS accessed directly over a private
LAN. HTTPS remains the secure default, while an Admin can explicitly allow
private-network HTTP without weakening role authorization, step-up, ticket,
same-origin, byte/request budget, or audit boundaries.

## Background and Confirmed Facts

- The official all-in-one image intentionally exposes HTTP and delegates TLS
  termination to user-managed infrastructure
  (`.trellis/spec/backend/deployment-runtime.md:22-27`). The target production
  installation is a UGREEN NAS reached directly over LAN HTTP; introducing an
  external reverse proxy is not part of this task.
- The released preview-authorization fix now exposes and invokes Load Preview
  for the real list-only Catalog shape, but production ticket issuance returns
  HTTP 503 `需要安全传输` over LAN HTTP.
- `BackupContentSchemePolicy.SecureCookie` currently accepts direct TLS or exact
  trusted-proxy `X-Forwarded-Proto: https`. Its only HTTP exception is a strict
  direct localhost development case
  (`backend/internal/api/handlers/backup_content_handler.go:57-92`).
- The same secure-cookie policy gates normal preview/download tickets, Recovery
  result downloads, and Export delivery tickets
  (`backup_content_handler.go:243`, `backup_content_handler.go:338`, and
  `backup_asset_export_handler.go:377`). The content gateway additionally
  validates the browser's effective origin before serving bytes
  (`backup_content_handler.go:397-431`).
- The all-in-one generic API route overwrites XFP with its actual `$scheme` and
  includes XFF. The exact content route currently preserves exact client-supplied
  XFP through a map and omits XFF (`deploy/nginx/templates/default.conf.template:
  53-105`). Thus ticket spoofing is already blocked, but a holder of an existing
  non-Secure ticket could spoof `X-Forwarded-Proto: https` on GET/HEAD and bypass
  immediate HTTP-read revocation unless the exact route is sanitized.
- The settings registry already supports dynamic Admin-only updates, exact
  validation, runtime transition, and value-free audit records. The generic
  System settings UI intentionally omits the `backup_assets` category, so this
  product control belongs in Backups rather than exposing all internal
  Foundation settings.
- The user explicitly rejected a preview-only exception because it would leave
  download, export, and Recovery result delivery unusable on the primary NAS
  deployment. The approved product choice is one private-network HTTP option for
  the whole existing content-delivery boundary.

## Requirements

- **R1 — Secure default.** Add
  `backup_assets.content_allow_insecure_private_network` /
  `BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK` as a dynamic boolean
  Foundation setting with CodeDefault `false`. HTTPS behavior remains unchanged
  when the setting is false or unavailable.
- **R2 — Explicit Admin control.** Add an Admin-only control under Backups →
  Overview. Enabling the setting requires an explicit warning confirmation;
  disabling it restores HTTPS-only behavior. The control must show loading,
  saved, error, enabled-warning, and inherited source states without exposing
  other backup settings. It has a stable, focusable hash target for transport-
  error guidance and permits at most one mutation while a save is pending.
- **R3 — Complete delivery scope.** When R1 is enabled and the request is from a
  private address, HTTP must work for every existing use of the shared content
  delivery mechanism: native preview, original asset download, Export artifact,
  `BackupArchiveHandler` archive-member delivery, and Recovery result download.
  Existing feature, role, Catalog, capability, classification, proof, ownership,
  and job-state rules still decide whether each operation is otherwise available.
- **R4 — Closed private-address product.** Private HTTP accepts only IPv4 RFC1918
  addresses, IPv6 unique-local addresses, and loopback. Public, unspecified,
  multicast, link-local, carrier-grade-NAT, malformed, zone-qualified, or
  unresolvable client evidence remains denied. This is an intentional closed
  allowlist, not a generic `IsGlobalUnicast` guess.
- **R5 — Trusted-proxy integrity.** Direct requests use `RemoteAddr`. Forwarded
  scheme or client evidence is honored only when the immediate peer is in the
  configured `TRUSTED_PROXIES` set. Accept only one exact effective scheme token
  (`http` or `https`); reject `Forwarded`, duplicate/compound scheme evidence,
  malformed address chains, and a forwarded chain with no provable client hop.
  For HTTP, any XFP requires one valid XFF chain with a provable client; only a
  fully direct request with neither header may classify `RemoteAddr` itself.
  Strip trusted hops from right to left and use the nearest untrusted hop so
  attacker-supplied leftmost XFF values cannot turn a public client private. The
  official all-in-one Nginx must overwrite XFP with its own `$scheme`; client-
  supplied XFP must never become backend scheme authority on content routes.
- **R6 — Issue, serve, and logging enforcement.** Apply the same shared transport
  policy at ticket issuance and content serving. The exact all-in-one Nginx
  content route must forward only the client address chain needed for the
  decision. Both its redacted access log and the application structured logger
  must omit client identity as well as URI, cookie, and request-line details for
  every content-shaped path regardless of method.
- **R7 — Security invariants.** HTTP tickets use a non-`Secure` cookie only after
  R1–R6 succeed. Preserve host-only cookie scope, `HttpOnly`,
  `SameSite=Strict`, exact Path, short expiry, same-origin and Fetch Metadata
  checks, no-CORS headers, server RBAC, step-up, ownership, revocation,
  request/byte/concurrency budgets, renderer policy, and redacted logs/audits.
- **R8 — Compatibility.** Preserve the existing strict
  `content_allow_insecure_loopback` development behavior and all HTTPS flows.
  Do not reinterpret the old key as LAN-wide permission. No database migration,
  Catalog DTO, Provider locator, ticket/grant schema, or role-permission change
  is allowed.
- **R9 — Closed transport error and discoverability.** JSON ticket endpoints
  denied only by transport return HTTP 503 with the machine-readable, parameter-
  free reason `secure_transport_required`; raw content GET/HEAD remains status-
  only. Frontend handling must distinguish this from generic unavailability for
  preview/original download, Export artifact, archive-member, and Recovery result
  delivery. Admin receives an accessible action to the exact Backups Overview
  control; Operator receives safe HTTPS-or-contact-Admin guidance. No UI may parse
  localized server text or expose request/client details. Documentation covers
  the default, private-address boundary, cleartext risk, UI control, environment
  fallback, and immediate rollback.
- **R10 — Delivery and production acceptance.** Use TDD with a production-shaped
  HTTP-through-all-in-one-proxy RED, focused/repeated/full gates, independent
  Trellis check, PR/CI/merge/release/Docker monitoring, guarded NAS upgrade, and
  a real content-preview acceptance. Node-log P1 and collectors stay at zero
  until that preview succeeds.

## Acceptance Criteria

- [ ] **AC1 (R1, R8):** The compatibility matrix proves: default/new=false
  rejects HTTP; old=true/new=false allows only direct loopback plus exact
  localhost Host; that old path still rejects proxied HTTP and non-loopback;
  new=true allows private/loopback HTTP; and every combination preserves direct
  TLS and exact trusted-proxy HTTPS with `Secure` cookies.
- [ ] **AC2 (R1, R2):** A production-composed Foundation transition test proves
  DB > env > default resolution, true/false changes without restart, immediate
  issue/Serve visibility, and exact DB/runtime restoration after an induced
  transition failure. Audit evidence remains value-free.
- [ ] **AC3 (R1, R3–R7):** With the new setting true, a production-shaped request
  from the all-in-one loopback proxy carrying exact `X-Forwarded-Proto: http`
  and a private client hop issues a non-`Secure`, `HttpOnly`, Strict, exact-Path
  ticket and the matching same-origin GET/HEAD content request can serve bytes.
- [ ] **AC4 (R3):** Automated selectors prove AC3 for normal preview/download,
  Export artifact delivery, `BackupArchiveHandler` archive-member delivery, and
  Recovery result download without changing their existing RBAC, proof,
  ownership, or capability decisions.
- [ ] **AC5 (R4, R5):** Public IPv4/IPv6, CGNAT, link-local, malformed/multiple or
  over-limit XFP/XFF, untrusted proxy headers, `Forwarded`, an all-trusted/no-
  client chain, forwarded HTTP without XFF, spoofed leftmost private XFF, and
  client-supplied XFP `https` through the all-in-one HTTP listener are denied/
  sanitized; RFC1918, IPv6 ULA, and loopback positives are covered.
- [ ] **AC6 (R2):** Only Admin sees the Backups Overview control. Tests cover
  loading, inherited source, saved live region, persistent enabled warning,
  labelled Switch/dialog, cancel/no-write, exact PUT, disable, failure rollback,
  pending-write deduplication, axe/keyboard behavior, and focus at the stable
  `backup-assets-content-transport` hash target. Operator/Viewer render no control.
- [ ] **AC7 (R6, R7):** A hermetic runtime test sends POST ticket, cookie, GET,
  HEAD, Host, XFP, XFF, Range, and If-Range through the rendered official Nginx
  template to an in-container probe and proves exact-route forwarding and byte/
  cookie behavior. A client-supplied `X-Forwarded-Proto: https` must reach the
  probe as actual `http`. Static/mutation checks and application middleware canary tests
  prove every exact or malformed content-shaped method omits URI/query/cookie/
  request-line/referrer/user-agent/XFF/X-Real-IP/`client_ip` while retaining the
  approved safe fields.
- [ ] **AC8 (R7–R9):** Tests and source/privacy scans prove no token, proof,
  delivery secret, Provider locator, backup path, asset name/content, raw client
  chain, or setting value enters responses, logs, audit details, browser storage,
  URLs, or router state.
- [ ] **AC9 (R9):** Every in-scope content-delivery JSON ticket endpoint emits the
  exact safe 503 reason
  `{reason:{code:"secure_transport_required",params:{}}}` on transport denial.
  Bounded frontend parsers map only that exact reason; Admin guidance links to
  `/app/backups/overview#backup-assets-content-transport`, Operator guidance has
  no settings action, and unrelated 503 responses retain their existing generic
  behavior across preview/download, Export, archive-member, and Recovery result
  surfaces.
- [ ] **AC10 (R9):** Deployment/env documentation accurately describes HTTPS as
  default, the explicit private-LAN HTTP option, its cleartext risk, and how to
  turn it off; doc freshness and diff checks pass.
- [ ] **AC11 (R10):** Focused backend policy/handler/export/archive/settings/logging
  tests pass at least three consecutive repetitions, focused race tests pass, the
  full backend suite/build and full `web` check pass, Nginx static/mutation/runtime
  gates and repository checks pass, and independent Trellis check reports no
  unresolved Critical or Important finding.
- [ ] **AC12 (R10):** PR and required CI pass, release/Docker publication finish,
  and guarded production upgrade preserves healthy runtime, schema 72 clean,
  repository/Catalog/Search readiness, task quiescence, zero critical log
  matches, and collectors `0`.
- [ ] **AC13 (R10):** In production, Admin explicitly enables private-network
  HTTP, the previously failing LAN preview ticket succeeds, and actual preview
  content renders. Only then may the approved node-log P1 start.

## Out of Scope

- Bundled TLS, certificate lifecycle, a new external reverse proxy, NAS vendor
  proxy instructions, or changing the official HTTP entrypoint.
- Allowing arbitrary/public HTTP, auto-enabling LAN HTTP, or inferring trust from
  Host/DNS names alone.
- New preview/download/export/recovery capability, broader RBAC, changed step-up
  actions, Catalog permission rewriting, Provider mutation, ticket/grant schema,
  or database migration.
- Persisting client addresses, asset paths/names/content, tickets, proofs, or
  delivery URLs in UI storage, settings audit values, or logs.

## Technical Notes

- The toggle is a long-lived NAS deployment choice, not a one-time test bypass.
- A capability can still be unavailable for its existing independent reason.
  “Complete delivery scope” means transport no longer removes an otherwise
  authorized content feature on private HTTP; it does not fabricate missing
  Catalog permissions or Provider ports.
- No blocking product, scope, UX, compatibility, or risk question remains.
