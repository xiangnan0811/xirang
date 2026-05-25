# Research: P5 Settings Audit Posture

- **Query**: Determine the safest next P5 report-only Settings security-risk summary slice around audit/security-event posture.
- **Scope**: backend audit log, credential audit, Settings risk summary, frontend mapper/rendering.
- **Date**: 2026-05-25

## Findings

### Files inspected

| File Path | Finding |
|---|---|
| `backend/internal/middleware/audit.go` | Secured non-GET/HEAD/OPTIONS HTTP routes are written to `audit_logs`; each row gets a hash-chain `prev_hash`/`entry_hash` in a serialized transaction. |
| `backend/internal/model/models.go` | `AuditLog` has user/role/method/path/status/client IP/user agent plus `PrevHash`/`EntryHash`; `CredentialAuditEvent` stores domain-specific high-risk credential-use evidence. |
| `backend/internal/api/handlers/audit_handler.go` | Audit list/export intentionally returns raw audit rows to authorized audit readers; not suitable as examples for Settings risk cards. |
| `backend/internal/credentialaudit/audit.go` | Credential audit writer sanitizes metadata/errors and bounds fields before persistence. |
| `backend/internal/api/handlers/credential_audit_handler.go` | Credential audit read/export re-sanitizes legacy rows and strips risky metadata/error values. |
| `backend/internal/api/handlers/settings_handler.go` | Existing security-risk summary supports bounded count/example posture items and already reports recent high-risk credential operations. |
| `web/src/lib/api/settings-api.ts` | Security risk codes are normalized through a closed union; new code must be added explicitly. |
| `web/src/pages/settings-page.system.test.tsx` | Settings/System renders risk cards without remediation links or mutation actions. |
| `web/src/i18n/locales/{zh,en}.ts` | Risk-code i18n titles/descriptions are localized under Settings System. |

### Existing signals available

- `audit_logs` can be counted without exposing raw actors or paths.
- Hash-chain posture can be derived from aggregate checks:
  - audit rows exist but some rows have blank `entry_hash`;
  - non-first rows have blank `prev_hash`;
  - row `prev_hash` does not match previous row `entry_hash` when ordered by ID;
  - the first row may legitimately have blank `prev_hash`.
- `credential_audit_events` already supports high-risk operation counts in `recent_credential_operations`; this task should avoid duplicating that item.
- No retention/config field for audit log retention was found in the inspected code path, so MVP should not claim retention posture.

### Sanitization constraints

Settings risk summary must not return raw usernames, role-bearing account examples, IPs, user agents, paths, request payloads, endpoints, command text/output, terminal streams, file contents, raw SQL, or host-sensitive strings for this item.

### Recommended MVP

Add a dedicated `audit_log_integrity_posture` Settings security-risk summary item that reports only aggregate audit-log hash-chain integrity posture:

- `info`, count `0` when no audit rows exist or the existing chain has no detected issue.
- `critical` with bounded generic examples when missing entry hashes or broken previous-hash links are detected.
- Examples are generic Chinese strings such as `审计日志存在缺失的完整性哈希` and `审计日志哈希链存在断点`, not row identifiers or raw audit fields.

This is behavior-compatible and schema-free. It surfaces an architecture-security posture signal without adding SIEM, enforcement, session recording, command approval, or external logging.

### Validation suggestions

- Backend test should create safe and broken `AuditLog` rows and assert the new risk item count/examples/severity.
- Backend test should assert response body does not contain seeded raw audit username, IP, user agent, or path.
- Frontend mapper/i18n/SystemTab tests should include `audit_log_integrity_posture`.
- Run `git diff --check`, focused backend handlers tests, full backend tests/build, focused frontend tests, and full frontend check.

## Caveats

- This MVP does not verify cryptographic correctness by recomputing `entry_hash` for every row because the hash payload includes stored raw actor/path/IP/user-agent fields. A future deeper check could stay server-side and still report only aggregate counts, but may be more expensive and should be planned separately.
- This MVP does not introduce retention policy checks because no audit-retention configuration was found in the inspected code path.
