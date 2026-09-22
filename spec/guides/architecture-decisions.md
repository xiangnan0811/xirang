# Product and compatibility decisions

## Product scope

Xirang targets personal operators and small teams. Prefer low deployment burden,
compatibility and bounded changes. Preserve advisory security-posture cards and
lightweight confirmations for destructive actions. Enterprise policy/device-trust
governance, full Vault/KMS rollout, SSH CA, command approval engines and a
session-recording platform are outside the default direction unless explicitly
requested. This does not weaken security controls or remove supported integrations.

The backup asset browser is an operations capability, not a general-purpose
writable file-sharing or synchronization product. Repository and RecoveryPoint
identity, rather than TaskRun identity or directory timestamps, establish
produced-asset provenance. Detailed current contracts remain in the layer specs;
historical proposals do not override shipped behavior or later contracts.

## Deferred compatibility changes

Keep the current WebSocket protocol. Generic protocol redesign and KDF salt
migration were not implemented by the completed quality-debt work. Reconsider
KDF migration only with a concrete threat/compatibility design; neither deferred
topic is an automatically active task. Broad elimination of legacy Printf calls
is not a goal by itself: preserve necessary diagnostics and configuration logging
that intentionally precedes logger initialization.

## Historical acceptance boundaries

Production acceptance is scoped and dated. Repository tests, release/CI/registry
checks and user-reported host observations are separate evidence. Old incomplete
checklists or reconciliation next-action fields do not reopen completed work.

Node-log acceptance on 2026-09-16 covered the one journal-only node the user
needed; enabling other collectors was explicitly not outstanding. Backup-assets
functional acceptance and its parent closed on 2026-09-17. Preserve the accepted
collector configuration rather than reviving the older collectors-must-be-zero
instruction. These are historical observations, not proof of current deployment
state, a full server-log audit or production fault injection.

## Decision provenance

These immutable records explain the retained decisions; read them only when the
rationale matters. Their old workflow instructions and intermediate status are
historical, not development prerequisites.

- [Small-team scope](https://github.com/xiangnan0811/xirang/blob/aab8d1e188249a49e2050b8febfdad2b5b10ea1e/.trellis/tasks/archive/2026-05/05-25-p5-small-team-security-roadmap-adjustment/prd.md#L5)
- [Asset identity and product scope](https://github.com/xiangnan0811/xirang/blob/aab8d1e188249a49e2050b8febfdad2b5b10ea1e/.trellis/tasks/archive/2026-09/07-12-backup-data-explorer-design/design.md#L918)
- [Bounded quality-debt decisions](https://github.com/xiangnan0811/xirang/blob/aab8d1e188249a49e2050b8febfdad2b5b10ea1e/.trellis/tasks/archive/2026-09/07-11-07-11-p3-quality-debt/design.md#L11)
- [Node-log acceptance scope](https://github.com/xiangnan0811/xirang/blob/aab8d1e188249a49e2050b8febfdad2b5b10ea1e/.trellis/tasks/archive/2026-09/08-23-node-logs-collector-stall/research/production-acceptance-2026-09-16.md#L3)
- [Final backup-assets closure](https://github.com/xiangnan0811/xirang/blob/aab8d1e188249a49e2050b8febfdad2b5b10ea1e/.trellis/tasks/archive/2026-09/07-12-backup-data-explorer-design/task.json#L45)
