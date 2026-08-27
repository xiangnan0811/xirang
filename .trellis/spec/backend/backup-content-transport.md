# Backup Content Transport

## Scenario: Transport Evidence And Private-LAN HTTP

### 1. Scope / Trigger

- Trigger: changing backup-content transport settings, ticket Cookie security,
  trusted-proxy evidence, or any preview/download/export/archive/recovery
  delivery entry point.
- This scenario governs the shared handler policy. Nginx header ownership and
  content-log redaction remain separately mandatory in `deployment-runtime.md`
  and `logging-guidelines.md`.

### 2. Signatures

- Dynamic key:
  `backup_assets.content_allow_insecure_private_network`.
- Environment override:
  `BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK`.
- Runtime projection:
  `backupasset.ContentConfig.AllowInsecurePrivateNetwork bool`.
- Handler policy:
  `BackupContentSchemePolicy.SecureCookie(*http.Request, BackupContentTransportOptions) (bool, error)`.
- Closed JSON denial:
  HTTP 503 with
  `data.reason = {"code":"secure_transport_required","params":{}}`.

### 3. Contracts

- The new key is a Foundation bool with DB > env > default precedence and a
  code default of `false`. PUT, DELETE/reset, config import, prospective
  transition, persistence failure, and compensation use the existing exact
  Foundation snapshot/rollback contract. Audit records action, key, change fact,
  and actor; successful PUT also records `source=db`. Neither PUT nor reset
  records the setting value.
- `content_allow_insecure_loopback` remains an independent compatibility key.
  It permits only direct HTTP from loopback to a localhost Host with no
  forwarding headers. Do not broaden it through the private-network option.
- Direct TLS is allowed with a Secure Cookie. HTTP is allowed by the new key
  only when the effective client is RFC1918, IPv6 ULA, or loopback. IPv4-mapped
  addresses are unmapped before classification. CGNAT, link-local, public,
  unspecified, multicast, zoned, or invalid addresses fail closed.
- Reject `Forwarded`. XFP must be exactly one raw value of at most 16 bytes that
  resolves case-sensitively to `http` or `https`, from a trusted immediate peer.
  Any HTTP hop carrying XFP also requires exactly one XFF field of at most 1,024
  bytes and 16 comma-separated hops. Parse every XFF hop as an exact IP, append
  the immediate peer, then peel trusted hops right-to-left; the nearest
  untrusted address is the client. Duplicate, compound, empty, malformed,
  over-limit, or all-trusted chains fail closed.
- Ticket issue and every content GET/HEAD reload the current Content config and
  apply the same policy. Turning the key off therefore blocks further HTTP reads
  made with previously issued non-Secure tickets. Broker RBAC, step-up, origin,
  purpose, renderer, grant, lease, byte, request, and rate limits stay final and
  unchanged.
- The shared transport denial applies to native preview, original download,
  Export artifact, archive-member delivery, and Recovery result. Raw content
  GET/HEAD transport denial returns only status; JSON ticket routes use the
  closed reason above.

### 4. Validation & Error Matrix

| Evidence/configuration | Result |
|---|---|
| Direct TLS, any client | Allow; issue Secure Cookie. |
| Direct strict localhost, old key true | Preserve legacy non-Secure Cookie behavior. |
| HTTP, new key true, effective RFC1918/ULA/loopback client | Allow; issue non-Secure HttpOnly SameSite=Strict exact-Path Cookie. |
| HTTP, new key false or non-private client | 503 `secure_transport_required`; do not call the ticket service. |
| HTTP XFP without valid XFF, or untrusted/ambiguous forwarding | Fail closed with the same transport denial. |
| Setting disabled after ticket issue | Subsequent content GET/HEAD returns status-only 503 before service read. |
| Valid transport but RBAC/purpose/origin/budget fails | Preserve the existing authoritative denial; transport never grants access. |

### 5. Good/Base/Bad Cases

- Good: the bundled proxy supplies actual `$scheme` plus an appended XFF chain;
  an Admin-enabled RFC1918 client receives a bounded non-Secure ticket and the
  Serve path rechecks the same client evidence.
- Base: the key remains false, HTTPS and strict legacy localhost retain their
  existing behavior, and private HTTP receives the closed 503 product.
- Bad: trust a leftmost XFF value, accept client-supplied XFP from an untrusted
  peer, classify CGNAT as private, skip Serve-time config reload, or change
  RBAC/ticket limits to make HTTP work.

### 6. Tests Required

- Table-test direct/proxied TLS and HTTP; RFC1918, ULA, loopback, IPv4-mapped,
  CGNAT, link-local, public, unspecified, multicast, zoned, and invalid clients;
  duplicate/compound/over-limit XFP/XFF; spoofed leftmost hops; and all-trusted
  chains.
- Exercise normal preview/download, Recovery, Export, and archive-member ticket
  routes. Assert exact reason shape, Cookie flags/path, no service call on deny,
  and unchanged RBAC/step-up/purpose behavior.
- Toggle the dynamic setting and prove exact DB/env/default/reset projection,
  value-free audit, prospective runtime config, failure rollback, and immediate
  Serve-time rejection of an old non-Secure ticket.
- Run focused selectors repeatedly and under `-race`, then full backend and
  repository gates. Keep the official Nginx runtime probe as separate evidence.

### 7. Wrong vs Correct

Wrong:

```go
if request.Header.Get("X-Forwarded-Proto") == "https" || remoteIP.IsPrivate() {
    return true, nil // trusts client input and bypasses dynamic Serve recheck
}
```

Correct:

```go
config, err := contentConfigSource(ctx) // current Foundation snapshot
if err != nil {
    return content.ErrContentUnavailable
}
secure, err := schemePolicy.SecureCookie(request, config.transportOptions())
// Existing RBAC, purpose, origin, ticket, grant, and budget checks still apply.
```
