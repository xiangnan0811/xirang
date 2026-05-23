# P4 Integration notification local resolver seam

## Goal

Reduce the remaining least-privilege blast radius around notification integration secrets by centralizing API response sanitization and safe sender-error handling for `Integration` data, without changing storage, delivery behavior, deployment, or frontend contracts.

## What I already know

- Previous P4 slices added local provider/resolver seams for SSH credentials, executor SSH usage, Restic repository passwords, and AppCredential profile hook access.
- `model.Integration` encrypts `Endpoint`, `Secret`, and `ProxyURL` via GORM hooks, so normal handler/dispatcher reads materialize plaintext values.
- Current API handlers return `model.Integration` directly after only Telegram-specific endpoint masking; other tokenized webhook URLs and proxy URL credentials can still be serialized back to callers.
- Alert dispatch/retry paths mostly persist sanitized errors, but a few test/log/response paths can still use raw sender errors.

## Requirements

- Add a backend-local Integration response DTO/sanitizer used by List/Get/Create/Update/Patch/Test responses.
- Preserve existing request fields and storage semantics; `Endpoint`, `Secret`, and `ProxyURL` continue to be encrypted by model hooks.
- Preserve existing delivery behavior; dispatchers/senders must still receive the real endpoint/proxy/secret values.
- Mask endpoint URLs for all integration types, not only Telegram, removing path/query/fragment/userinfo details that can contain tokens or host-sensitive routing.
- Mask proxy URLs so credentials, path, query, and fragment are not returned to API clients.
- Preserve `has_secret` behavior and do not serialize `Secret`.
- Replace remaining raw sender-error API/log surfaces with `util.SanitizeError` or equivalent safe messages.
- Add regression tests proving API responses and test-send failures do not expose endpoint tokens, proxy credentials, host-sensitive URL pieces, or secret strings.

## Acceptance Criteria

- [ ] Integration List/Get/Create/Update/Patch/Test responses use sanitized DTOs and never return raw endpoint token/path/query/userinfo/proxy credentials.
- [ ] Integration dispatch and retry behavior is unchanged for real sends.
- [ ] Alert delivery persisted errors and handler test responses use sanitized error text.
- [ ] Regression tests cover non-Telegram webhook endpoint masking and proxy URL masking.
- [ ] Regression tests cover failed test-send error sanitization.
- [ ] Backend targeted tests pass for integration handler and alerting packages.
- [ ] Full backend test suite passes before PR.

## Definition of Done

- Trellis task archived and session journal recorded.
- Work committed on a non-main branch.
- PR created, CI green, merged.
- Release Please / GitHub release / Docker publish complete.
- Local main synced clean.

## Out of Scope

- External Vault/KMS/secret-manager provider references.
- Schema migrations or environment/config changes.
- Changing notification delivery payloads, endpoint validation, sender transport behavior, or retry policy.
- Frontend form redesign or new API fields.
- Retrofitting historical alert delivery rows beyond safe handling on new writes/responses.
- Removing encrypted Integration fields from the model or changing GORM hook encryption.

## Technical Approach

Create a handler-local response DTO and sanitizer that copies public Integration fields while replacing sensitive URL-bearing fields with stable masked representations. Keep dispatcher and sender call paths on `model.Integration` so delivery semantics do not change. For sender/test error surfaces, sanitize error text at the boundary before logging/responding/persisting.

## Technical Notes

- Candidate call sites: `backend/internal/api/handlers/integration_handler.go` List/Get/Create/Update/Patch/Test.
- Dispatch call sites: `backend/internal/alerting/dispatcher.go`, `backend/internal/alerting/retry.go`, `backend/internal/alerting/sender.go`.
- Sensitive-field model hooks remain authoritative in `backend/internal/model/models.go`.
- Relevant specs: `.trellis/spec/backend/logging-guidelines.md`, `.trellis/spec/backend/error-handling.md`, `.trellis/spec/backend/database-guidelines.md`.
