# Quality Guidelines

> Code quality standards for backend development.

---

## Overview

Backend changes should match the existing Go/Gin/GORM style and keep the
security-sensitive server-management domain conservative. Run `gofmt` on edited
Go files. The standard backend gate is `cd backend && go test ./... && go build
./...`; repository CI also has a conservative `golangci-lint` configuration in
`backend/.golangci.yml`.

---

## Forbidden Patterns

- New ad hoc JSON response shapes in handlers. Use the helpers in
  `backend/internal/api/handlers/response.go`.
- Returning nodes, SSH keys, integrations, or executor configs without
  sanitizing sensitive fields.
- Adding routes under `/api/v1` without the correct `AuthMiddleware`, RBAC, and
  ownership middleware unless the route is intentionally public auth/captcha.
- Ignoring database, SSH, file-system, encryption, JSON marshal, or migration
  errors.
- Adding a setting outside `settings.Service`'s registry or reading a dynamic
  setting directly from the environment when an existing registry key exists.
- Adding SQLite-only or PostgreSQL-only schema changes.
- Introducing new dependencies for small helpers that the standard library or
  existing packages already cover.

---

## Required Patterns

- Return API data through `respondOK`, `respondCreated`, `respondMessage`,
  `respondPaginated`, or the error helpers.
- Keep sensitive data encrypted via model hooks and strip secrets from response
  structs. Example: `model.Node.Sanitized()`.
- Keep encryption-key rotation docs and implementation in lockstep:
  `DATA_ENCRYPTION_KEY` is the primary v2 write key, and
  `DATA_ENCRYPTION_LEGACY_KEY` must be honored by
  `backend/internal/secure/crypto.go` when documented for v1 decrypt/migration.
- Validate IDs with shared helpers such as `parseID` and validate user input
  before writes. Keep validation close to the owning handler/service.
- For cross-resource or multi-row writes, use GORM transactions.
- Use `logger.Module` for new structured backend logs.
- Keep docs in sync when changing API routes, models, env vars, migrations, or
  release/deploy behavior. `CONTRIBUTING.md` lists the current doc-sync rules.
- Prefer existing domain services and helpers before adding new abstractions.

---

## Testing Requirements

- Add or update package tests for behavior changes. The repo already has broad
  `*_test.go` coverage under `backend/internal/api/handlers/`,
  `backend/internal/task/`, `backend/internal/alerting/`,
  `backend/internal/dashboards/`, and related packages.
- Handler changes should verify status code and response envelope when feasible.
  See `backend/internal/api/handlers/response_test.go`.
- Database logic should cover both empty-result and error paths. Migration or
  schema compatibility fixes should include focused tests when they are not
  trivially verified by startup.
- Security-sensitive code such as SSH auth, path validation, encryption, RBAC,
  and ownership filtering requires explicit tests for denial cases.
- Before merging backend work, run at least `cd backend && go test ./...`; for
  broader changes also run `cd backend && go build ./...` and `make lint-backend`
  when `golangci-lint` is available.

### Scenario: RBAC route permission keys

#### 1. Scope / Trigger

- Trigger: adding or changing any `middleware.RBAC("<permission>")` route under
  `/api/v1`, or making a frontend navigation item depend on that route.
- Applies to backend route registration, `rolePermissions`, and frontend
  surfaces that link to the protected feature.

#### 2. Signatures

- Route signature: `secured.<METHOD>("<path>", middleware.RBAC("<permission>"), handler)`.
- Permission matrix signature:
  `rolePermissions["<role>"]["<permission>"] = true`.
- Test signature: use a router that includes `AuthMiddleware` and `RBAC`, not a
  handler-only Gin route, when the behavior being changed is authorization.

#### 3. Contracts

- Every permission string used by a route must be granted to at least one
  intended role in `backend/internal/middleware/rbac.go`.
- Sensitive management surfaces such as saved credentials, system settings,
  recovery, and secret-bearing config should fail closed. Do not grant
  operator/viewer access unless the product contract explicitly says so.
- Frontend navigation must not expose a normal path to roles that the backend
  will always reject for that feature.

#### 4. Validation & Error Matrix

- Missing/expired token -> 401 from auth middleware.
- Known role without the route permission -> 403 `权限不足`.
- Unknown or missing role -> 403 `权限不足`.
- Intended role with the route permission -> handler status code.

#### 5. Good/Base/Bad Cases

- Good: a new `app_credentials:read` route is paired with admin permission,
  full-router admin/non-admin tests, and admin-only frontend navigation.
- Base: a new route reuses an existing permission whose role coverage already
  matches the feature.
- Bad: a route references a new permission key that is absent from
  `rolePermissions`, causing every authenticated role to receive 403.

#### 6. Tests Required

- Assert at least one intended role receives the handler response through the
  full router.
- Assert roles that should not access the feature receive 403 through the same
  middleware stack.
- For sensitive data routes, include a denial case for saved records or
  mutations, not just the public/schema endpoint.

#### 7. Wrong vs Correct

Wrong:

```go
secured.GET("/app-credentials/profiles", middleware.RBAC("app_credentials:read"), h.ListProfiles)
// rolePermissions has no app_credentials:read entry, so all roles fail.
```

Correct:

```go
secured.GET("/app-credentials/profiles", middleware.RBAC("app_credentials:read"), h.ListProfiles)

var rolePermissions = map[string]map[string]bool{
	"admin": {
		"app_credentials:read": true,
	},
}
```

### Test fixture credential naming

Test fixtures that simulate secrets/credentials (passwords, tokens, keys,
bearer tokens, base64-encoded auth headers, webhook URLs with embedded tokens)
must be named so that **both human reviewers and automated secret scanners**
(GitGuardian, gitleaks, ggshield, trufflehog) immediately recognize them as
non-real test data.

**Rule**: use the prefix `FAKE_` and the suffix `_FOR_TEST_ONLY` for any
literal string that resembles a secret. Compute matching base64 / hex / hash
values from the same fake string when needed (don't reuse pre-computed values
from external examples or Stack Overflow snippets).

**Why**: GitGuardian and similar ML-based scanners flag entropy-rich strings
even in `*_test.go` / `*.test.tsx` files. False positives block PR merges and
require either ggshield ignore comments (per-scanner syntax, fragile) or
admin override (loses signal). Naming the fixture obviously fake is the only
robust solution. Examples that have triggered scanners on this repo:

| Forbidden in fixtures | Why scanners flag | Replacement |
|---|---|---|
| `hunter2` | XKCD password meme; on every scanner's blocklist | `FAKE_PASSWORD_FOR_TEST_ONLY` |
| `secret-metrics-token` | Hyphen pattern + the word "secret" looks like real token | `FAKE_METRICS_TOKEN_FOR_TEST_ONLY` |
| `c2VjcmV0LW1ldHJpY3MtdG9rZW4=` | base64 of the above; same problem | recompute from new fake string |
| `SECRETXYZ` / `ABCD-1234-EFGH` | Looks like a real API key by entropy | `FAKE_TOKEN_FOR_TEST_ONLY` |
| `replace-with-strong-random-secret` (in `.env*.example`) | Was OK historically but newer scanners may flag | Prefer `<set-strong-random-token>` style |

**How to apply**:

- Before committing any test fixture or `.env*.example` placeholder that
  contains a secret-shaped string, mentally check: would a stranger reading
  this think it's a real secret? If yes, rename.
- For base64-encoded auth headers (`Authorization: Basic ...`), encode the
  fake plaintext fresh: `echo -n "FAKE_FOO_FOR_TEST_ONLY" | base64`.
- The `pre-commit` hook does not currently lint fixture names. Reviewers and
  the AI assistant share responsibility for catching these in PR review.

---

## Code Review Checklist

- Are route middleware, RBAC permissions, and ownership checks correct?
- Are API responses still using the unified envelope and existing helpers?
- Are secrets encrypted at rest and removed from response payloads?
- Are SQLite and PostgreSQL migrations paired and reversible?
- Are all DB/SSH/file/encryption errors checked and mapped safely?
- Are background workers and goroutines cancelable or shutdown-aware when the
  surrounding package requires it?
- Are docs updated for API, model, migration, env var, or deployment changes?
- Did the change reuse existing packages/helpers instead of duplicating local
  logic?
