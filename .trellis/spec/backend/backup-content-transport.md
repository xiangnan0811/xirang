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

---

## Scenario: Safe-Preview Bounded Prefix Reads

### 1. Scope / Trigger

- Trigger: changing `safe_preview_v1`, Broker classification, sequential source
  reads, Restic/Rclone/Rsync adapters, SSH command streams, or preview source
  error mapping.
- This scenario governs the bounded prefix used to resolve a safe-preview intent
  and the later exact-product Serve read. It does not broaden AssetRef authority,
  RBAC, step-up, lease, ticket, origin, or transport policy.

### 2. Signatures

- The client sends intent only: `action=preview` plus
  `preview_intent=safe_preview_v1`; renderer/profile are forbidden in that form.
- The server resolves and persists an exact closed renderer/profile product.
  Faithful generic text resolves to `plain_text/text_v2`; only true binary falls
  back to the bounded hex product.
- A sequential handle may additionally implement the internal capability
  `ClosePrefix() error`. It means that the consumer intentionally finished after
  a valid bounded prefix, not that arbitrary close errors may be ignored.
- Closed source-stage failures expose only safe codes for open, read, changed,
  timeout, or capability failure, with empty parameters and request correlation.
  Provider commands, locators, paths, tokens, proof, and bytes never cross the
  API or audit boundary.

### 3. Contracts

- Issue performs exactly one bounded source open/read for classification. Serve
  performs its own exactly one authorized bounded/exact read; classification,
  selection, grant preparation, and auditing must not reopen the source.
- Restic and Rclone command-backed sequential adapters forward intentional
  prefix close through every wrapper down to the command stream. Rsync retains
  its ordinary strict tree-handle close when it has no such capability.
- `ClosePrefix` may suppress only the wait error caused by terminating a still
  running command after a successfully consumed prefix. It must preserve a
  command failure that completed before intentional termination, and preserve
  read, cancel, timeout, byte-limit, background, invariant, and ordinary-close
  failures.
- The safe-preview classifier consumes only the bounded in-memory prefix. It
  accepts strict UTF-8 and approved, well-formed UTF-16 text, keeps active markup
  inert, and selects native media only from closed signatures/capabilities.
  Provider MIME cannot upgrade ambiguous bytes.
- Core source failures use direct, localized source guidance. Worker-enhancement
  copy is reserved for actual derived ZIP/Office/OCR states and must not mask a
  failed core text read.
- Success grants, descriptors, Serve validation, and success audit contain only
  the resolved exact product. Pre-resolution failures contain the closed intent
  and safe reason only.

### 4. Validation & Error Matrix

| Condition | Result |
|---|---|
| Valid generic text prefix, command still running | Resolve `plain_text/text_v2`; intentional prefix close suppresses only its induced wait error. |
| Command already failed before prefix close | Preserve the command/source failure; issue no grant. |
| Prefix read canceled, timed out, exceeds bounds, or violates an invariant | Preserve the authoritative typed failure; do not call intentional-success cleanup. |
| Generic binary bytes | Resolve bounded hex with exact `truncated` fact. |
| Signature-proven supported media with required capability | Resolve the matching native exact product. |
| Ambiguous/deceptive media or missing native capability | Fail with the closed renderer/capability product; do not trust MIME or downgrade authorization. |
| Core source open/read/change failure | Return localized core guidance and safe correlation; never a generic Worker hint or raw provider detail. |

### 5. Good/Base/Bad Cases

- Good: an actual command-backed adapter yields a valid text prefix, Issue
  resolves one exact text grant, intentional prefix close joins safely, and Serve
  returns matching bytes under the same policy.
- Base: a strict local-tree adapter uses ordinary close and the same Broker
  contract without implementing `ClosePrefix`.
- Bad: ignore every close/wait error; infer text/native solely from MIME; reopen
  during classification; persist `safe_preview_v1` in the grant; expose the
  provider error; or tell the user to deploy a Worker when the core read failed.

### 6. Tests Required

- Unit-test command streams where termination causes the wait error and where a
  failure completes naturally before prefix close. Cover ordinary close, limit,
  cancellation, timeout, and session cleanup separately.
- Compose actual Restic, Rsync, and Rclone adapters through Issue, resolved grant,
  and Serve. Assert one source open/read per stage, exact product/truncation, and
  no ordinary aborted-close promotion for command-backed prefix success.
- Include one live handler vertical slice through the real Repository Service,
  an actual adapter, Broker, persisted grant, and Serve; fake-only Broker tests
  are insufficient production evidence.
- Test the strict request union, closed error envelope, audit/privacy canaries,
  repeated focused selectors, race detector, and full backend/frontend gates.

### 7. Wrong vs Correct

Wrong:

```go
prefix, err := io.ReadAll(source)
closeErr := source.Close()
return prefix, errors.Join(err, closeErr) // expected early command stop becomes 503
```

Correct:

```go
prefix, complete, err := readBoundedPrefix(source)
if err != nil {
    return nil, authoritativeClose(source, err)
}
if !complete {
    if closer, ok := source.(interface{ ClosePrefix() error }); ok {
        return prefix, closer.ClosePrefix() // suppress induced wait only
    }
}
return prefix, source.Close()
```
