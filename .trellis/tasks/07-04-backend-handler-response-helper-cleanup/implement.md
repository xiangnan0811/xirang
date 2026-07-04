# Backend Handler Response Helper Cleanup Implementation Plan

> Inline Trellis execution: the main Codex session implements and checks this
> plan directly. Use TDD for behavior changes and keep commits focused.

**Goal:** Convert selected backend REST handlers away from direct `c.JSON`
responses while preserving response contracts.

**Architecture:** Add focused helper variants in `response.go` only where an
existing helper cannot express the current envelope. Use a source-level test to
lock the selected handler files to helper usage, then rely on existing behavior
tests to catch response drift.

**Tech Stack:** Go 1.26, Gin handler tests, repository response helpers.

---

### Task 1: RED Static Contract Test

**Files:**
- Create: `backend/internal/api/handlers/response_helper_usage_test.go`

- [ ] Add a test that reads `auth_handler.go`, `node_handler.go`, and
      `policy_handler.go` and fails if any contains direct `c.JSON(`.
- [ ] Run
      `cd backend && go test ./internal/api/handlers -run TestSelectedHandlersAvoidDirectJSONResponses`
      and verify it fails because those files still have direct `c.JSON` calls.

### Task 2: Response Helper Variants

**Files:**
- Modify: `backend/internal/api/handlers/response.go`

- [ ] Reuse `respondServiceUnavailable` for service-unavailable auth paths.
- [ ] Add helper variants only for response envelopes that carry both a custom
      message and data:
      - `respondLocked(c, msg, data)`
      - `respondForbiddenData(c, msg, data)`
      - `respondOKWithMessage(c, msg, data)`

### Task 3: Convert Selected Handlers

**Files:**
- Modify: `backend/internal/api/handlers/auth_handler.go`
- Modify: `backend/internal/api/handlers/node_handler.go`
- Modify: `backend/internal/api/handlers/policy_handler.go`

- [ ] Convert login lock, onboarding service unavailable, and logout service
      unavailable responses to helpers without changing headers or envelope.
- [ ] Convert disabled node exec response to a helper while preserving
      `data.error_code`.
- [ ] Convert policy target-path warning response to a helper while preserving
      the warning message and policy data.
- [ ] Run the focused static contract test and verify it passes.

### Task 4: Verification

- [ ] Run `rg -n "\\bc\\.JSON\\(" backend/internal/api/handlers/auth_handler.go backend/internal/api/handlers/node_handler.go backend/internal/api/handlers/policy_handler.go`.
- [ ] Run `cd backend && go test ./internal/api/handlers`.
- [ ] Run `cd backend && go test ./...`.
- [ ] Run `cd backend && go build ./...`.
- [ ] Run `git diff --check`.
- [ ] Run Trellis check, update specs only if new durable knowledge appears,
      commit, archive, and record journal progress.
