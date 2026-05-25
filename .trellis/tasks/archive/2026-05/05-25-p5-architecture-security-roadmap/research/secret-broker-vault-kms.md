# Research: Secret broker / Vault / KMS

- **Query**: Audit external Vault/KMS/secret broker architecture candidates for Xirang after P4 residual hardening, then identify the smallest behavior-compatible first slice.
- **Scope**: internal architecture mapping plus comparable deployment patterns.
- **Date**: 2026-05-25

## Scope

This research covers architecture-level secret custody options for credentials currently encrypted in the local database: managed SSH keys, node passwords/private keys, integration secrets, app credential configs, task executor config, TOTP secrets, recovery codes, and sensitive export/import payloads. The goal is not to roll out a new required external service in the first P5 slice; it is to rank candidates and find a safe enabling step.

## Current repo constraints

- Sensitive fields are encrypted/decrypted through model hooks and bootstrap migration support rather than manual caller-side handling.
- Production already requires a strong `DATA_ENCRYPTION_KEY`; v1 encrypted data can be migrated to v2 on startup.
- SSH credential resolution is already centralized behind `sshutil.CredentialProvider`, `LocalCredentialProvider`, and purpose-scoped helpers that return metadata such as provider/source/kind without exposing raw secret material.
- SSH key purpose/node/tag constraints and operation-bound credential grants are already in place for high-risk paths.
- Config export/import can include secret material only behind admin, step-up, and JIT credential-grant gates; default export omits secrets.
- The deployment target remains a single-repo Docker Compose setup using SQLite or PostgreSQL with no required Vault/KMS sidecar or cloud account.
- Audit/log specs forbid raw secret material, executor config, endpoint/proxy values, command output, file contents, diagnostic output, and exported/imported payloads.

## Comparable patterns

### HashiCorp Vault transit/KV

Vault can centralize encryption/decryption, lease policy, audit, and secret lifecycle. A full rollout would require service provisioning, auth method configuration, token/role lifecycle, availability planning, and operator documentation. It has high security value for organizations already running Vault, but it is too large as a required first slice for Xirang’s current Docker deployment.

### Cloud KMS envelope encryption

Cloud KMS can wrap data-encryption keys while keeping encrypted values in the application database. It reduces key custody risk for cloud deployments but introduces provider-specific configuration, IAM, network dependency, availability concerns, and backup/restore coupling. It is a good future provider target, not a compatible default.

### External secret broker / sidecar

A sidecar or broker can vend short-lived credentials to task executors and terminal/session paths. This aligns with operation-bound grants but changes runtime assumptions: executor startup, failure modes, token exchange, local development, and health monitoring all become part of the platform contract.

### App-local key-provider abstraction

A provider interface can preserve the current local encrypted DB behavior while creating an explicit seam for future Vault/KMS/broker providers. Xirang already has a partial seam in SSH credential resolution, but encrypted model hooks still directly depend on local app encryption. Generalizing all field encryption in one step would be risky; a narrow inventory/provider-readiness slice is safer.

## Candidate ranking

| Candidate | Security impact | Compatibility | Operational cost | Blast radius | Assessment |
|---|---|---|---|---|---|
| Full Vault transit/KV rollout | High | Low as required default | High | High | Roadmap follow-up only; too much new infrastructure for first P5 slice. |
| Cloud KMS envelope encryption | High | Medium for cloud users, low for local users | Medium/High | High | Future optional provider; not first because it adds provider/IAM/deploy coupling. |
| Secret broker for executor credentials | High | Medium/Low | High | High | Future direction after provider contracts and policy language mature. |
| Credential provider inventory/readiness metadata | Medium | High | Low | Low/Medium | Useful enabling work, but lower immediate value than strong-auth posture and still easy to over-abstract. |
| No secret-custody code in first slice | Medium via roadmap clarity | Highest | Lowest | Lowest | Recommended for this task if another lower-blast-radius P5 slice is available. |

## Recommended first slice

Do not start P5 with required Vault/KMS/secret broker integration. The first implementation slice should remain behavior-compatible and local-deployable. If this candidate family must contribute to the first slice, record roadmap-ready provider boundaries and avoid code changes beyond documenting the current local provider constraints in the PRD.

The better first executable P5 slice is outside this family: a report-only strong-auth posture signal in the existing Settings security-risk summary. It prepares enterprise policy language without requiring secret-custody infrastructure, new deployment dependencies, or sensitive runtime data movement.

## Follow-up roadmap

1. Define a local-compatible secret provider contract that preserves current encrypted DB semantics.
2. Add optional provider health/readiness metadata that never includes raw secret material.
3. Support cloud KMS wrapping for the application encryption key or selected secret classes.
4. Support Vault transit/KV as an optional provider for deployments that already operate Vault.
5. Consider short-lived executor credential brokering only after grant/audit and provider contracts are stable.

## Validation notes

- Any future implementation must prove default local Docker behavior remains unchanged.
- Tests should assert no raw private keys, passwords, tokens, executor config, endpoints, exported/imported payloads, or provider diagnostic details are returned, logged, audited, or stored in UI state.
- Dependency/version choices for Vault or KMS clients must be verified from authoritative releases before commit.
