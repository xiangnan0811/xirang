# Current-state evidence

## Production trigger

- The v0.50.10 production UI now reaches the exact delivery-ticket POST over
  LAN HTTP, proving the earlier frontend authorization defect is fixed.
- The server returns HTTP 503 `需要安全传输` before issuing a ticket. The runtime
  remains healthy, the Catalog/Search lineage is ready, and node-log collectors
  remain disabled.

## Root cause

1. `BackupContentSchemePolicy.effectiveScheme` accepts direct HTTP only when no
   forwarded scheme exists. When XFP is present it accepts only exact `https`
   from a trusted peer.
2. The all-in-one Nginx always supplies `X-Forwarded-Proto` to generic API and
   content requests, so `content_allow_insecure_loopback=true` cannot activate
   its direct-localhost exception in the packaged deployment.
3. The same policy is shared by asset preview/download, Recovery result ticket,
   and Export ticket issuance. Therefore a preview-only UI change cannot make
   the NAS product complete.
4. Content serving repeats effective-origin validation. Accepting issuance alone
   would still reject an HTTP content GET because trusted forwarded `http` is not
   a valid effective scheme today.
5. Serve-time private-client enforcement needs the original client chain, but
   the exact Nginx content route does not currently forward XFF.
6. The generic ticket route safely overwrites XFP with `$scheme`, but the exact
   content route maps client-supplied exact XFP. A client holding an existing
   non-Secure ticket could therefore send XFP `https` on HTTP GET/HEAD and bypass
   immediate setting-off read revocation unless the route is sanitized.

## Existing reusable boundaries

- `TRUSTED_PROXIES` is validated at startup and injected into both Gin and the
  backup-content scheme policy. Its default trusts only the all-in-one loopback
  proxy.
- `settings.Service` already owns dynamic Foundation keys, bool validation,
  DB > env > default resolution, atomic mutation, runtime transition, exact
  rollback, and value-free audit facts.
- The generic Settings API is Admin-only and already returns definitions plus
  resolved source. The System settings page deliberately filters out
  `backup_assets`, so a dedicated Backups panel can reuse the API without
  exposing every internal Foundation control in the generic UI.
- `Switch`, `AlertDialog`, `InlineAlert`, typed API factories, and the Backups
  Overview Admin panel pattern already exist.
- The exact content Nginx route already uses a redacted access-log format and a
  mutation-tested checker.
- The application structured logger shapes opaque content paths but currently
  emits `client_ip`; adding XFF to the exact route therefore also requires
  content-shaped application events to omit that field entirely.
- Ticket 503 responses are currently collapsed into generic unavailability by
  the preview, Recovery, Export, and Archive frontend paths. Transport guidance
  requires a closed machine-readable error reason rather than localized-message
  parsing.

## Selected design

- Add a new default-false private-network setting; keep the old loopback key
  unchanged.
- Reuse one policy for exact scheme plus effective client resolution at issuance
  and serve.
- Allow HTTP only when the new setting is true and the effective client is
  RFC1918, IPv6 ULA, or loopback.
- Preserve all authorization and ticket controls; only the Cookie `Secure` bit
  follows the accepted transport scheme.
- Add an Admin-only Backups Overview control with explicit enable warning.
- Add XFF forwarding to the exact content route while keeping content logs free
  of identity and resource material.

## Rejected approaches

- Preview-only HTTP: rejected by the user because download/export/result
  delivery would remain unusable on the primary NAS deployment.
- Arbitrary HTTP: rejected because accidental public exposure would silently
  extend the opt-out beyond the user's LAN requirement.
- Implicit LAN auto-detection with no setting: rejected because the risk decision
  would no longer be explicit or auditable.
- Broadening `content_allow_insecure_loopback`: rejected because its name and
  compatibility contract promise a strict development-only localhost exception.
