# Research: SSH trust / session recording / command controls

- **Query**: Audit SSH CA, host trust, session recording, and command approval/inspection candidates for Xirang after P4 residual hardening, then identify the smallest behavior-compatible first slice.
- **Scope**: internal architecture mapping plus comparable SSH control patterns.
- **Date**: 2026-05-25

## Scope

This research covers SSH trust and command/session control directions: SSH CA, host-key trust/pinning, terminal/session recording, command approval/inspection, and privileged command workflows. The goal is to rank candidates without expanding the first P5 slice into a risky platform rewrite.

## Current repo constraints

- SSH credential use is centralized through purpose-aware helpers and managed-key scope checks.
- Host-key trust is centralized in the SSH auth utility and supports strict known-hosts behavior, auto-accept, and insecure mode depending on configuration.
- WebSocket terminal access already requires realtime JWT auth, admin role, step-up proof, active terminal credential grant, purpose-scoped SSH auth, session limits, timeout, host-key callback, and audit events.
- Manual task trigger and batch command paths already require RBAC/ownership where applicable, step-up, JIT credential grants, command length checks, dangerous-command blocking, purpose-scoped SSH auth, and credential audit.
- Audit is split between hash-chained general audit logs and credential-specific audit events; credential audit sanitization explicitly avoids raw secrets, terminal streams, command output, command text, executor config, and remote evidence.
- Task logs, terminal streams, node logs, file browser, Docker volume discovery, probes, hooks, and restore/integrity checks all create sensitive remote-data surfaces; first P5 work must not start storing raw streams or command outputs as policy evidence.
- Docker Compose/local deployment should keep working without a CA authority, object store, parser service, or approval workflow service.

## Comparable patterns

### SSH CA and certificate lifecycle

An SSH CA can reduce long-lived key distribution and support certificate principals, short TTLs, and revocation. A complete rollout needs CA custody, host/user cert issuance, trusted-user CA installation on nodes, renewal/revocation, principal mapping, failure handling, and migration from current keys. This is high-impact but high-blast-radius.

### Host-key trust inventory / pinning

Known-hosts enforcement and host-key state visibility reduce MITM risk without changing user authentication. Xirang already has strict host-key configuration and node Doctor-style diagnostics, so a future slice could expose sanitized host-trust posture without recording host-sensitive values. This is a plausible enabling slice but still needs careful redaction.

### Terminal/session recording

Recording sessions can improve forensics and compliance, but raw terminal streams are sensitive and explicitly forbidden for first-slice storage/logging. Full recording needs retention, encryption, object storage, privacy controls, playback authorization, redaction, and export governance. It should not be first.

### Command approval / inspection

Approval workflows can prevent risky commands before execution. Xirang already blocks some dangerous command patterns and gates high-risk operations through step-up and grants. A complete command approval engine would need parser semantics, policy model, approval UI, reviewer lifecycle, race handling, and audit. It also risks storing raw command text as policy data, which conflicts with current constraints.

### Sudo / privileged command workflow hardening

Existing nodes include sudo behavior and command executors can wrap commands. A richer privileged-operation policy could be valuable later, but it depends on command policy semantics and node/user trust boundaries.

## Candidate ranking

| Candidate | Security impact | Compatibility | Operational cost | Blast radius | Assessment |
|---|---|---|---|---|---|
| Full SSH CA rollout | High | Low without node-side migration | High | High | Roadmap follow-up; not first. |
| Host-key trust posture/inventory | Medium/High | Medium/High | Low/Medium | Medium | Plausible later enabling slice if sanitized and advisory. |
| Terminal/session recording | High for forensics | Low for privacy/storage compatibility | High | High | Not first; raw stream storage is out of scope. |
| Command approval/inspection engine | High | Medium/Low | Medium/High | High | Not first; command text/policy storage risks and workflow redesign. |
| Privileged command policy matrix | Medium/High | Medium/Low | Medium | High | Later after policy model exists. |
| No SSH-control code in first slice | Medium via roadmap clarity | Highest | Lowest | Lowest | Recommended for this task if a safer architecture-enabling slice is available. |

## Recommended first slice

Do not start P5 with SSH CA, session recording, command approval, or command parser work. They are valuable roadmap directions but each changes deployment, node runtime, sensitive storage, or operator workflow.

A future SSH-control enabling slice could be a sanitized host-key trust posture item, because host-key policy is already centralized and node Doctor already has diagnostics precedent. However, for the first P5 implementation, the strongest safety/value tradeoff is the report-only strong-auth posture slice in Settings security-risk summary: it reuses existing advisory UI, existing user/TOTP state, and does not require recording commands, streams, host-sensitive values, or remote evidence.

## Follow-up roadmap

1. Add sanitized host-key trust posture/inventory after defining redaction boundaries and tests.
2. Model optional SSH CA provider/readiness without requiring node-side CA rollout.
3. Design command policy language separately from raw command storage; prefer action/resource/purpose metadata over command text.
4. Consider command approval workflow only after policy, reviewer lifecycle, and audit redaction are specified.
5. Consider terminal/session recording only with encryption, retention, object storage, playback authorization, privacy controls, and redaction strategy.

## Validation notes

- Any future SSH-control slice must preserve existing terminal/task/batch command workflows unless the PRD deliberately records a breaking MVP change.
- Tests must prove no raw terminal streams, command text/output, file contents, Docker output, diagnostic evidence, hostnames, paths, endpoints, or credentials are returned/logged/audited/stored in UI state.
- Host-key posture should report counts/classes and safe identifiers only, not raw known_hosts lines or target-sensitive values.
