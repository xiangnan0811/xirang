# Design: Policy node_ids Query Slimming

## Boundary

This child stays inside `backend/internal/api/handlers/policy_handler.go` and
its colocated tests. It changes response assembly for policy HTTP reads, not the
policy model, database schema, scheduler, task manager, drill runner, frontend,
or policy service contract.

## Current Behavior

Policy list and detail handlers call `Preload("Nodes")` and then
`buildPolicyResponse` derives `node_ids` by iterating over `p.Nodes`. That loads
full node rows, including fields the response intentionally never serializes,
even though the handler only needs IDs.

## Target Behavior

The handler should load policies without full node preloads, collect the policy
IDs in the result set, query `policy_nodes` once for those IDs, and build a
`map[uint][]uint`. The response builder should accept explicit node IDs for
list/detail paths. Empty associations return an empty array, matching current
behavior when `p.Nodes` is empty.

Create and update paths can continue reloading associations through their
existing path if changing them would increase risk. If they are touched, they
must preserve the same response fields and association persistence.

## Compatibility

The API response shape remains unchanged. `node_ids` stays a JSON array of
numeric IDs. Hook visibility remains role-gated by `policyResponseIncludesHooks`.
Operator filtering still uses the existing `policy_nodes` subquery. Latest drill
summary loading remains keyed by policy ID.

## Error Handling

Association loading errors are internal server errors because the policy rows
were already selected successfully and the join table query is part of response
assembly. Empty policy lists should short-circuit without an extra join-table
query.

## Tests

Add handler tests that create policies and policy-node rows directly, call list
and detail endpoints, and assert `node_ids` values. Keep existing hook and
latest-drill tests passing to guard response behavior.

## Rollback

The rollback is local: restore `Preload("Nodes")` in list/detail and revert the
response builder signature. No schema or data migration is involved.
