# Design: private-network HTTP content delivery

## 1. Architecture and ownership

This task extends the existing content-delivery transport boundary rather than
adding a second delivery path:

```text
Admin Backups control
  -> existing Admin Settings API
  -> settings.Service dynamic Foundation value
  -> backupasset.ContentConfig
  -> shared BackupContentSchemePolicy
       -> asset preview/download ticket issue
       -> Export artifact ticket issue
       -> BackupArchiveHandler archive-member ticket issue
       -> Recovery result ticket issue
       -> asset-content GET/HEAD serve
  -> existing broker/export ledgers, RBAC, proofs, budgets, renderer headers
```

Nginx remains the bundled internal HTTP proxy. The feature does not add TLS,
certificates, listeners, or an external reverse proxy.

## 2. Setting contract

Add one registry definition:

| Field | Value |
|---|---|
| key | `backup_assets.content_allow_insecure_private_network` |
| env | `BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK` |
| type | bool |
| default | `false` |
| category | `backup_assets` |
| restart | false |

Add `AllowInsecurePrivateNetwork bool` to `backupasset.ContentConfig` and the
handler config projection. Include the key in the Foundation content snapshot,
prospective parser, exact override snapshot/restore, registry parity tests, and
runtime transition fixtures. Do not combine it with or rename
`AllowInsecureLoopback`; both compatibility paths remain explicit.

DB > env > default precedence and the existing Foundation mutation transition
remain authoritative. The Admin UI writes only the exact new key. Existing
settings audit records the key, actor, source, and changed boolean without the
value.

The settings mutation uses the existing production-composed Foundation
transition, not a panel-local cache. Its prospective snapshot must feed both
ticket issuance and Serve without restart. A transition error restores the exact
prior override presence/value and the prior runtime snapshot before returning an
error; no successful audit is emitted. An integration fixture covers DB > env >
default, enable, disable, and induced apply/restore failure boundaries.

## 3. Shared transport policy

Replace the current “HTTPS or direct localhost” decision with a closed evaluation
that returns the effective scheme, effective client address, and whether the
cookie must be Secure. The implementation may use a small options struct to
avoid positional boolean drift:

```go
type BackupContentTransportOptions struct {
    AllowInsecureLoopback       bool
    AllowInsecurePrivateNetwork bool
}
```

### 3.1 Effective scheme

1. Any `Forwarded` header rejects the request.
2. Direct TLS with no XFP is `https`.
3. No TLS and no XFP is direct `http`.
4. XFP is accepted only when the immediate remote peer is trusted and the header
   is exactly one trimmed value equal to `http` or `https`.
5. Duplicate, comma-compound, empty, case-varied, or unknown XFP rejects. Direct
   TLS plus contradictory forwarded `http` rejects rather than downgrading.

HTTPS returns `Secure=true` and does not require a private client classification,
but duplicate, malformed, or untrusted forwarding evidence still fails closed.

### 3.2 Effective client address for HTTP

- With neither XFP nor XFF, parse the fully direct `RemoteAddr` host exactly. An
  HTTP request carrying XFP but no XFF has no provable client and fails closed.
- With XFF, require a trusted immediate peer and one syntactically valid chain.
  Require exactly one header field, at most 1024 raw bytes, and at most 16 hops.
  Parse every comma-separated hop as an exact IP with no zone or empty item.
  Append the immediate remote hop, peel trusted hops from the right, and select
  the nearest remaining untrusted hop. If no such hop exists, fail closed.
- Ignore all farther-left hops once the nearest untrusted hop is selected. This
  prevents an untrusted client from prepending an RFC1918 value.
- Normalize IPv4-mapped IPv6 before classification.

The allowed address set is exact: `netip.Addr.IsPrivate()` or
`netip.Addr.IsLoopback()`. Explicit negative tests cover public, unspecified,
multicast, link-local, and `100.64.0.0/10` CGNAT addresses.

### 3.3 Decision matrix

| Scheme/evidence | New setting | Client | Result |
|---|---:|---|---|
| direct TLS or trusted XFP `https` | either | any | allow, Secure cookie |
| direct strict localhost legacy exception | false | loopback + localhost Host | preserve old non-Secure behavior only when old key is true |
| direct/trusted XFP `http` | true | RFC1918/ULA/loopback | allow, non-Secure cookie |
| direct/trusted XFP `http` | false | any | 503 secure transport required |
| direct/trusted XFP `http` | true | public/invalid | 503 secure transport required |
| untrusted or ambiguous forwarding evidence | either | any | deny fail-closed |

## 4. Enforcement points

All current callers use the same policy and current dynamic config:

- `BackupContentHandler.Issue`: preview and original asset download.
- `BackupContentHandler.IssueRecoveryResult`: Recovery result download.
- `BackupAssetExportHandler.secureCookie`: Export artifact tickets.
- `BackupArchiveHandler.DeliveryTicket`: archive-member tickets, through the
  shared `BackupAssetExportHandler.secureCookie` policy delegate.
- `BackupContentHandler.Serve`: validate the current scheme and private client
  again before broker/export ledger lookup and byte service.

Serve loads the current content transport config. An unavailable or invalid
config fails closed with the existing raw content-route 503 class. Turning the
setting off therefore stops new HTTP tickets and further HTTP reads immediately;
HTTPS tickets continue through the unchanged path.

The Browser origin contract remains exact scheme + Host equality. After exact
trusted `http` becomes a valid effective scheme, an HTTP Origin can pass only if
it matches the current request Host exactly. Fetch Metadata, no-Authorization,
no-query, exact path/method, and security headers remain unchanged.

## 5. Nginx boundary

The exact `/api/v1/asset-content/<opaque>` location changes its scheme authority
and adds only the client chain needed by the backend:

```nginx
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
```

Generic API already overwrites XFP with `$scheme` and sends both X-Real-IP and
XFF. The exact content route must likewise use only its actual `$scheme`; the
client-derived effective-proto map is removed so an inbound XFP `https` cannot
turn an HTTP content read into backend HTTPS evidence. The exact route does not
need X-Real-IP and must not add it. It keeps `$http_host`, Range/If-Range
forwarding, buffering/cache/temp/gzip suppression, finite timeouts, route order,
and the redacted content access log. The shaped fallback also uses `$scheme`.
Checker and mutation tests must reject any content-route dependency on
`$http_x_forwarded_proto` and prove client identity does not enter the log format.

The application `StructuredLogger` must also recognize
`IsBackupContentShapedPath` and omit the `client_ip` field entirely for every
method, including malformed suffixes, trailing slash, query, POST, and OPTIONS.
It retains only the safe shaped route, method, status, latency, and request ID;
it must not substitute the proxy-loopback or effective LAN address. Non-content
request logging remains unchanged. Middleware capture tests use canary remote and
forwarded values and a mutation/source check must fail if content-shaped events
regain `client_ip` or any forwarding header value.

Static template validation alone is insufficient. Add a hermetic Docker runtime
gate that renders the official template and starts it with a second in-container
Nginx probe server on port 3000. Drive a generic POST that returns a test ticket
cookie, then cookie-authenticated GET and HEAD through the exact content route.
The probe response must prove Host-with-port, exact XFP `http`, appended XFF,
Range, and If-Range forwarding; the client must prove cookie pass-through, GET
bytes, and empty HEAD body. Inspect the dedicated content log with canary URI,
cookie, and address values and prove only its approved fields occur. This runtime
test also sends inbound XFP `https` and requires the probe to observe actual
`http`. It is the first proxy behavioral RED because the released exact route has
no XFF and accepts the spoofed scheme; it complements, rather than replaces, real
handler policy tests.

## 6. Admin UX

Add a focused `PrivateNetworkContentTransportPanel` on Backups → Overview next
to the existing Admin readiness surface. It is mounted only for an authenticated
Admin.

- Load the existing Settings response through a typed API wrapper and select
  only the exact new key/value/source.
- Give the panel a stable `backup-assets-content-transport` target and a
  programmatically focusable semantic heading. Arrival at the matching static
  hash focuses that heading after load without persisting any state or value.
- Display an accessible loading status, the secure default, and the effective
  source (`db`, `env`, `default`).
- Turning off -> on opens an accessible `AlertDialog` explaining that preview,
  download, Export delivery, archive-member delivery, and Recovery result bytes
  will travel unencrypted on the LAN. Cancel writes nothing. Confirm writes
  `{key: "true"}`.
- Turning on -> off writes `{key: "false"}` and immediately restores HTTPS-only
  behavior.
- Disable mutation controls while one save is pending so rapid clicks cannot
  issue duplicate PUTs. Announce a successful save through a polite live region.
- While enabled, keep a visible warning that names the same complete delivery
  scope. On mutation failure, retain the prior confirmed value and show an
  accessible alert; focus remains predictable.
- Operator/Viewer receive no toggle. Their existing feature UIs work when an
  Admin has enabled the setting and their normal RBAC/capability permits the
  operation.
- Keep `CATEGORY_ORDER` unchanged; do not expose the entire `backup_assets`
  registry in System settings.

Use existing Card, Switch, AlertDialog, InlineAlert, Button, i18n, and API request
primitives. The Switch and dialog have exact labels/descriptions and pass
keyboard plus axe coverage. No raw setting snake_case or direct fetch belongs in
the component.

## 7. Errors, privacy, and audit

- Add a transport-specific response helper used only when ticket issuance is
  denied by the shared scheme/client policy. It returns the standard 503 envelope
  with a safe localized message and the closed product below; it is not a Catalog
  capability and must not be added to `CatalogCapabilityCode`:

  ```json
  {"data":{"reason":{"code":"secure_transport_required","params":{}}}}
  ```

  The typed response contains no client, header, Host, setting source/value,
  ticket, or asset identifier. Other 503 causes retain `data: null` or their
  existing error product.
- The bounded frontend error parser accepts only the exact code and empty params.
  Native preview/original download map it to a non-retryable
  `secure_transport_required` UI error. Recovery, Export, and Archive state
  unions/classifiers gain the same distinct error instead of collapsing it to
  `unavailable`.
- Each delivery surface renders safe role-aware guidance. Admin gets an
  accessible link to
  `/app/backups/overview#backup-assets-content-transport`; Operator is told to
  use HTTPS or ask an Admin and receives no settings action. Unrelated 503s keep
  their current generic retry/unavailable UX. Tests cover normal preview and
  original download, Export artifact, archive-member, and Recovery result ticket
  failures without parsing message text.
- Content GET/HEAD retains raw redacted status responses and no body detail.
- Settings audit remains value-free. Do not add client chains to structured logs,
  credential audit, content audit, or Nginx access/error logs.
- No token, step-up proof, cookie secret, delivery URL, client evidence, asset
  name/path/content, Provider locator, or setting response is stored in browser
  storage, query strings, route state, or analytics.

## 8. Compatibility and migration

- No schema migration. `system_settings` already stores registered overrides.
- Default false exactly preserves released behavior.
- Existing env/DB users of `content_allow_insecure_loopback` keep the strict
  direct-localhost exception; the packaged LAN path uses only the new key.
- Direct TLS and a proxy that connects directly to the backend from an explicitly
  trusted peer retain their ticket/cookie contract. The all-in-one HTTP listener
  now reports only its actual scheme; external reverse-proxy integration remains
  outside this task.
- RBAC permissions, success DTOs, ticket/grant ledgers, delivery IDs, Catalog
  permissions, Provider ports, renderer profiles, and step-up actions are
  unchanged. The only API-envelope addition is the safe transport error reason
  above.

## 9. Rollout and rollback

1. Merge only after focused/repeated/race/full backend, full web, Nginx static/
   mutation/runtime, docs, privacy, and independent check gates pass.
2. Monitor Release Please, GitHub Release, Docker multi-arch publish, and Docker
   Hub description automation as applicable.
3. On NAS, keep collectors at zero; perform read-only image/digest/runtime/DB/
   repository/Catalog/Search/task preflight; create and verify a DB backup; update
   the exact Compose image; wait for internal/external health and schema checks.
4. Admin enables the option in Backups Overview. Verify only the safe setting
   state and aggregate health facts; do not print secrets or content identifiers.
5. Run real preview acceptance over LAN HTTP. Automated release gates cover
   content operations unavailable in the current production Catalog/capability
   fixture.
6. Immediate feature rollback is the UI switch to false (or delete the DB
   override to restore env/default). Image rollback uses the retained Compose
   snapshot and prior verified image only if application rollback is required.

Node-log P1 starts only after real preview content is visible.
