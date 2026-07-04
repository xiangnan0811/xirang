# 策略 node_ids 查询瘦身

## Goal

Reduce unnecessary database payload and handler work in policy list/detail
responses by loading only policy-node association IDs where the API only needs
`node_ids`, while keeping the response contract unchanged.

## Requirements

- Scope is limited to policy list/detail/create/update response assembly and
  directly related tests.
- `GET /api/v1/policies` and `GET /api/v1/policies/:id` must continue returning
  the same policy fields, including `node_ids`, hook visibility rules, and
  `latest_drill`.
- Replace full `Preload("Nodes")` on policy list/detail read paths when the
  handler only needs node IDs. The handler should query `policy_nodes` for the
  relevant policy IDs and build a `policy_id -> []nodeID` map.
- Preserve ownership filtering semantics: operators can see policies associated
  with any node they own, and unauthorized policy detail access remains denied.
- Preserve hook secrecy: non-admin responses must not expose `pre_hook` or
  `post_hook`; admin responses must keep existing behavior.
- Preserve create/update behavior and association persistence. This slice should
  not change policy validation, task synchronization, drill triggering, template
  cloning, or the policy service API unless strictly required by the response
  mapping change.
- Add focused backend tests proving `node_ids` are returned for list and detail
  without relying on serialized `model.Node` fields.
- Do not add migrations, frontend changes, or API shape changes in this child.

## Acceptance Criteria

- [ ] Policy list response includes the correct `node_ids` for policies with
      zero, one, and multiple associated nodes.
- [ ] Policy detail response includes the correct `node_ids`.
- [ ] Existing hook visibility behavior remains covered for admin and non-admin
      responses.
- [ ] Existing latest drill summary behavior remains unchanged.
- [ ] Implementation removes the TODO about wasteful full `Nodes` preload from
      policy list response assembly.
- [ ] The relevant backend tests pass with `cd backend && go test ./internal/api/handlers`.
- [ ] Broader backend verification passes with `cd backend && go test ./...`.

## Notes

- Evidence from audit:
  - `policy_handler.go` contains a TODO noting that `Preload("Nodes")` is
    wasteful because `buildPolicyResponse` only uses node IDs.
  - Existing tests cover policy hook visibility and latest drill summary, but
    not list/detail `node_ids` mapping directly.
  - Other policy consumers, such as task retention and drill execution, still
    legitimately need full node records and are out of scope for this child.
