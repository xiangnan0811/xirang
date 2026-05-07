# Fix app credential profile permission 403

## Goal

Fix the permission regression where opening the application credential creation
dialog after login fails with `GET /api/v1/app-credentials/profiles` returning
403 Forbidden. Users who are allowed to create application credentials must be
able to load the built-in profile schema and complete the create form.

## What I already know

- The reported browser error is:
  `GET http://192.168.100.247:19927/api/v1/app-credentials/profiles 403 (Forbidden)`.
- The frontend opens the credential editor from `CredentialsPage`; the dialog
  calls `createCredentialsApi().listProfiles(token)` when it opens.
- Backend routes require:
  - `GET /app-credentials/profiles` -> `app_credentials:read`
  - `GET /app-credentials` and `GET /app-credentials/:id` -> `app_credentials:read`
  - create/update/delete -> `app_credentials:write`
- `backend/internal/middleware/rbac.go` currently defines no
  `app_credentials:read` or `app_credentials:write` permission for any role.
  That makes all roles fail app credential RBAC checks, including admin.
- `ListProfiles` returns built-in profile schema only. It does not return saved
  credential records or stored credential secrets.
- Credential list/detail responses are sanitized, but they are still sensitive
  operational data and should not be made visible to roles unless the role is
  intended to manage credentials.

## Assumptions

- Application credentials are an admin-managed secret surface.
- Admin should have both `app_credentials:read` and `app_credentials:write`.
- Operator/viewer should not gain credential list/detail/create/update/delete
  access in this fix unless an existing documented product contract clearly says
  otherwise.
- The immediate reported bug is caused by the missing RBAC permissions rather
  than by token transport or frontend base URL configuration.

## Requirements

- Add the missing app credential RBAC permissions to the role matrix so admin
  can load app credential profiles and manage credentials.
- Keep credential entity read/write access limited; do not broaden access to
  saved credential records or credential mutations for operator/viewer unless
  explicitly justified by an existing contract.
- Preserve the existing route structure and response shapes.
- Add or update backend tests so the permission regression is covered through
  the same RBAC middleware used by the router, not only handler-only tests.
- If frontend navigation currently exposes the credentials page to roles that
  cannot use it, align the UI visibility with the backend permission contract
  so users do not hit avoidable 403 errors from normal navigation.
- Do not expose plaintext passwords or hook templates through profile/schema
  responses.

## Acceptance Criteria

- [ ] An admin-authenticated request to
  `GET /api/v1/app-credentials/profiles` returns 200.
- [ ] An admin-authenticated request to credential list/create/update/delete
  routes is authorized by RBAC.
- [ ] Operator/viewer access to saved credential records and credential
  mutations remains forbidden unless product docs already define otherwise.
- [ ] Normal frontend navigation does not present an unusable credentials
  management entry to roles that cannot access the credential management API.
- [ ] Backend regression tests cover the RBAC permission matrix for app
  credential profile/list/write paths.
- [ ] Backend lint/typecheck/tests relevant to the change pass.
- [ ] Frontend lint/typecheck/tests relevant to any UI visibility change pass.

## Definition of Done

- Tests added/updated for the affected RBAC behavior.
- Lint/typecheck pass for touched packages.
- Any docs/spec updates required by a newly clarified permission contract are
  made.
- The fix is committed on a dedicated branch and ready for PR/CI.

## Out of Scope

- Redesigning app credential CRUD or storage.
- Changing encryption or credential sanitization logic.
- Adding fine-grained per-user credential ownership.
- Changing deployment host/base URL behavior.

## Technical Notes

- Likely backend files:
  - `backend/internal/middleware/rbac.go`
  - `backend/internal/api/router.go`
  - app credential handler and/or RBAC tests
- Likely frontend files if UI visibility needs alignment:
  - `web/src/components/layout/navigation.ts`
  - navigation or credentials page tests if present/needed
- Relevant docs/specs:
  - `.trellis/spec/backend/index.md`
  - `.trellis/spec/backend/error-handling.md`
  - `.trellis/spec/backend/quality-guidelines.md`
  - `.trellis/spec/frontend/index.md`
  - `.trellis/spec/frontend/component-guidelines.md`
  - `.trellis/spec/frontend/quality-guidelines.md`
  - `.trellis/spec/frontend/type-safety.md`
