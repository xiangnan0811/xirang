# Backend Handler Response Helper Cleanup Design

## Scope and Boundaries

This task targets three regular REST handler files where direct `c.JSON` calls
can be replaced without changing public behavior:

- `auth_handler.go`
- `node_handler.go`
- `policy_handler.go`

The task does not touch middleware, WebSocket upgrade paths, CSV/text exports,
or test-only Gin handlers. Those paths have different response contracts and
should be handled in separate slices.

## Helper Shape

Existing response helpers already cover common success/error envelopes. This
task adds only small missing variants in `response.go` when needed:

- HTTP 423 with data for login lock responses.
- HTTP 403 with data for disabled node exec responses.
- HTTP 200 with custom message and data for policy warning responses.

Existing helpers such as `respondServiceUnavailable` should be reused before
adding new helper variants.

## Compatibility

The conversion is intentionally envelope-preserving. HTTP status code, envelope
`code`, `message`, `data`, and required headers remain unchanged. Frontend API
request unwrapping should see the same data as before.

Rollback is straightforward: restore the direct `c.JSON` blocks in the three
handler files and remove any helper variant that no longer has callers.
