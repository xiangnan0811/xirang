# Approved remediation design

## Ownership
Identity owns auth service, JWT/MFA handlers and user model. Runtime owns task runner/manager and policy/automation control files. Monitoring owns uptime, monitor model/handler and monitor-only frontend. Client owns frontend core/auth/retry hooks, excluding monitor frontend. Main owns integration, main.go/router.go, bootstrap.go shared integration, generated API docs, database migration-version metadata, batch handler/model, SSH/CI and final verification. No concurrent edits to shared files; request integration from main.

## Contracts
Identity: explicit encrypted writes, compare current security version and enrollment binding at commit; pending JTI unique durable consumption and recovery-code CAS in same transaction. Normal recovery login must not increment user TokenVersion. Last-admin mutations serialize across database connections, not a Go mutex. Preserve SQLite and PostgreSQL.
Runtime: legal state pairs rather than equal statuses (failed run/retrying task is valid). Terminal transaction must be owned by current run and compare expected states. Never put transfer in transaction. Persist run-scoped effect intent, dedupe downstream execution; recover abandoned ordinary runs without overwriting live or managed drill/publication ownership. Policy changes persist first, scheduler updates only after commit. Skip-next means every applicable task's next cron execution; manual behavior remains distinct. Recheck durable controls at executor admission.
Monitoring: responses contain http_header_names and http_headers_configured, never values/ciphertext. Omitted http_headers retains; explicit {} clears; nonempty object replaces. UI serializer must omit unchanged fields. Backfill plaintext using existing application encryption conventions, fail closed if incomplete. Earliest due/bounded workers, target non-overlap, cancellation and stale revision checks. Existing last_checked_at may avoid needless scheduling schema.
Client: session generation/request identity at final 401 handling; authenticated fallback to other origin forbidden. Production fallback off; developer fallback only DEV plus explicit config and unauthenticated request. Retry never resolves alerts.
Batch: all creates in one transaction, durable endpoint idempotency scoped by actor/key/request fingerprint, postcommit dispatch and queryable failures. No generic cross-project idempotency framework.
SSH: net.JoinHostPort everywhere node host+port forms network address; preserve known-host verification. Consistent first-host trust default false.
Release: resolve ref exactly once; require CI success for exact SHA before public image promotion; all architecture builds and attestation use frozen SHA; retain digest scans.

## Migrations and integration
Reserved migration slots: identity 000078, runtime 000079, monitoring 000080 only if required, batch/main 000081. Workers report unused slots; main renumbers to a contiguous sequence before integration. Both engines need up/down parity and forward-only safety for used durable security/effect data. Workers must not edit shared latest-version constants or migrator glue; report requirements to main.

## Compatibility and rollback
Migrate all repo consumers at once; do not retain aliases, permissive bypasses or plaintext fallback writes. Never re-expose secrets on downgrade. Preserve existing backupasset schema/ownership and production data. External secrets/CI availability are verified, not invented.
