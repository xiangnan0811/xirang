# P4 AppCredential profile hook local resolver seam

## Goal

Extend the P4 local-only credential provider/resolver pattern to AppCredential materialization used by app-aware profile hook rendering, so current policy create/update and AppCredential cascade paths resolve decrypted app credential config through one backend-internal seam while preserving existing API, UI, storage, and hook rendering behavior.

## Requirements

* Add a backend-internal local resolver seam for AppCredential profile hook materialization with provider identity fixed to `local`.
* Keep existing encrypted `model.AppCredential.Config` as the only source of truth for this slice.
* Cover current AppCredential config materialization consumers discovered in research: policy create profile rendering, policy update profile rendering, AppCredential update password-preservation parsing, and AppCredential update cascade re-rendering.
* Preserve existing behavior:
  * app credential API responses continue to omit raw password material;
  * blank password on credential update preserves the existing stored password when one exists;
  * generated pre/post hooks remain behaviorally equivalent;
  * user-provided hook overrides continue to win over generated profile hooks;
  * policy API shape and persisted policy hook fields remain unchanged;
  * app profile existence checks and missing credential errors remain additive to existing handler gates.
* Keep all operation-layer gates additive: app credential RBAC, policy RBAC/ownership, step-up/grants where already applied, SSH key scope checks, hook execution purpose checks, and runtime credential audit must not be bypassed.
* Add only safe resolver metadata: provider/kind/source labels are allowed; raw app credential config, passwords, rendered hook text, commands, output, endpoints, hostnames, paths, and imported/exported payloads are forbidden.
* Keep the change backend-only and avoid migrations, env vars, deployment changes, public API changes, frontend changes, external providers, or config import/export semantic changes.

## Acceptance Criteria

* [ ] A shared AppCredential profile materialization resolver exists and returns resolved config plus safe metadata with `provider=local`.
* [ ] Policy create and update no longer directly unmarshal `AppCredential.Config` for profile rendering.
* [ ] AppCredential update and cascade re-rendering no longer use local duplicate config parsing for old/new profile config materialization.
* [ ] Blank password update preservation, cascade re-rendering, and user hook override behavior remain equivalent.
* [ ] App credential responses still omit raw password material, and storage remains encrypted through model hooks.
* [ ] Invalid AppCredential config errors do not include raw config, password material, rendered hook text, commands, host-sensitive values, or paths.
* [ ] Tests prove safe metadata and that resolver errors/JSON output do not expose raw app password or decrypted config.
* [ ] Targeted backend tests and full backend tests pass before commit.

## Definition of Done

* Backend implementation is minimal and focused on the resolver seam.
* Tests are added/updated for resolver metadata, invalid JSON safety, storage/response safety, policy rendering adoption, and AppCredential cascade behavior.
* `go test ./... -count=1` passes from `backend/` using a writable temp directory if needed.
* Trellis task is started, archived after commit, journal recorded, PR created/merged, CI green, release automation completed, GitHub release published, Docker publish completed, and local main synced clean.

## Technical Approach

Add the seam in `backend/internal/profile` because current profile rendering already owns AppCredential-to-hook semantics and both handlers can import it without a cycle. The seam should parse a decrypted `model.AppCredential.Config` string into an in-memory map, return a small safe metadata struct, and expose helper methods for resolving by AppCredential ID and parsing raw config strings used during AppCredential update before save. Existing handler-local JSON parsing will be replaced at call sites while preserving handler HTTP mapping and generated hook output.

## Decision (ADR-lite)

**Context**: P4-1/P4-2 established a conservative local SSH provider seam, and P4-3 centralized restic repository password access. AppCredential config remains the next local encrypted credential class with direct materialization in policy/profile hook flows.

**Decision**: Implement a backend-only `local` AppCredential profile materialization resolver using existing encrypted `model.AppCredential.Config`; cover current policy create/update and AppCredential update/cascade consumers in this slice.

**Consequences**: This centralizes local materialization and creates a future external-provider insertion point, but deliberately does not remove current rendered hook persistence or change hook execution semantics.

## Out of Scope

* Vault, KMS, Boundary, Teleport, SSH CA, dynamic secrets, provider health/lease/fallback/registry semantics.
* DB migrations, provider tables, provider reference columns, new env vars, deployment changes, public API/frontend changes.
* Changing generated hook command text, escaping behavior, execution behavior, or persisted policy hook fields.
* Removing app passwords from rendered hooks or replacing hook execution with password files/stdin/agents.
* Enforcing AppCredential type/profile compatibility unless code inspection proves current backend behavior already does so.
* Adding new credential audit events for profile rendering.
* Config import/export provider-reference semantics.
* Integration/notification secret resolver seam, rclone secret handling, terminal/session recording, command approval, WebAuthn/passkeys/device trust.

## Research References

* [`research/remaining-p4-options.md`](research/remaining-p4-options.md) — ranks remaining P4 work and recommends AppCredential profile hook materialization as the next local-only seam.
* [`research/profile-hook-app-credential-flow.md`](research/profile-hook-app-credential-flow.md) — maps AppCredential storage, profile rendering, persisted policy hooks, runtime hook execution, leakage notes, and existing tests.
* [`research/remaining-secret-materialization.md`](research/remaining-secret-materialization.md) — summarizes remaining non-SSH/non-restic local secret materialization candidates and why this slice stays narrower.

## Technical Notes

* `AppCredential.Config` is encrypted/decrypted by model hooks; do not manually encrypt/decrypt in handlers.
* Normal AppCredential API responses use sanitized config and must continue to omit password material.
* Policy responses currently include persisted hook fields; this slice preserves that API behavior and does not claim to eliminate rendered hook exposure.
* Profile-generated hooks are currently rendered separately from manual hook validation; this slice preserves that behavior.
* Safe metadata must avoid denied keys/values such as password, credential, config, command, output, content, payload, endpoint, hostname, and path-shaped operational data.
