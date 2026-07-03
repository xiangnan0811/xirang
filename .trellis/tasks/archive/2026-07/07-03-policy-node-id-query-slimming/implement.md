# Implementation Plan: Policy node_ids Query Slimming

## Checklist

1. Add focused regression tests in
   `backend/internal/api/handlers/policy_handler_test.go`:
   - list returns `node_ids` for policies with no nodes, one node, and multiple
     nodes;
   - detail returns `node_ids` for a policy with multiple nodes.
2. Run the focused handler tests and confirm the new tests fail or cover the
   current behavior before changing implementation.
3. In `policy_handler.go`, add a small helper that accepts a context and policy
   IDs, queries `policy_nodes`, and returns `map[uint][]uint`.
4. Update `List` to remove `Preload("Nodes")`, call the helper after policies
   are loaded, and pass each policy's node IDs into the response builder.
5. Update `Get` to avoid full node preload for response assembly while keeping
   ownership authorization correct. If the current ownership helper requires
   loaded `Nodes`, adjust locally with a minimal node-ID lookup or keep preload
   only where authorization truly needs it.
6. Update `buildPolicyResponse` to receive explicit node IDs or a small response
   options value. Keep all existing response fields and hook gating.
7. Re-run focused tests:
   `cd backend && go test ./internal/api/handlers`
8. Re-run backend gate:
   `cd backend && go test ./...`
9. Inspect `git diff` for unrelated churn, accidental response shape changes,
   or missed TODO cleanup.

## Validation Commands

```bash
cd backend && go test ./internal/api/handlers
cd backend && go test ./...
```

## Risk Points

- `authorizePolicyOwnership` may rely on `p.Nodes`; verify before removing
  detail preload.
- Policy create/update response paths also call `buildPolicyResponse`; changing
  its signature must update them without changing create/update behavior.
- Do not alter task scheduling, policy-node persistence, drill trigger audit
  metadata, or template clone behavior in this slice.

## Rollback Point

After tests are added but before implementation, the change can be abandoned by
reverting only the test additions. After implementation, rollback is limited to
`policy_handler.go` and the new tests.
