# Backend handler response helper cleanup

## Goal

Reduce backend handler response drift by converting a focused set of direct
`c.JSON` response writes to shared response helpers while preserving status
codes, response envelopes, headers, and existing client-visible behavior.

## Requirements

- Convert direct `c.JSON` usage in `backend/internal/api/handlers/auth_handler.go`,
  `node_handler.go`, and `policy_handler.go` to helpers in
  `backend/internal/api/handlers/response.go`.
- Preserve the login lock response status `423`, `Retry-After` header, message,
  and `data.retry_after` payload.
- Preserve auth service-unavailable envelopes for onboarding/logout when their
  backing DB/JWT service is missing.
- Preserve the disabled node exec response payload, including
  `data.error_code`.
- Preserve the policy update warning response: HTTP 200, standard envelope,
  warning text in `message`, and policy data in `data`.
- Do not change middleware responses, WebSocket upgrade responses, raw file
  exports, tests that intentionally use Gin directly, or specialized
  step-up/credential-grant helpers in this slice.

## Acceptance Criteria

- [ ] A focused backend test fails before implementation when selected handler
      files still contain direct `c.JSON(` calls, and passes after conversion.
- [ ] Existing behavior tests for auth, node exec, and policy update warning
      continue to pass.
- [ ] `rg -n "\\bc\\.JSON\\(" backend/internal/api/handlers/auth_handler.go
      backend/internal/api/handlers/node_handler.go
      backend/internal/api/handlers/policy_handler.go` returns no matches.
- [ ] `cd backend && go test ./internal/api/handlers` passes.
- [ ] `cd backend && go test ./...` and `cd backend && go build ./...` pass.
- [ ] `git diff --check` passes.

## Notes

- Parent task requirement: reduce backend ad hoc `c.JSON` responses in focused
  slices, preserving documented exceptions.
- Candidate scan showed many remaining `c.JSON` calls in middleware, tests,
  WebSocket handlers, and raw export paths; those are out of scope for this
  child task.
