# Small-team P5 security roadmap research

## Question

How should the remaining P5 roadmap change now that Xirang targets personal users and small teams rather than enterprise security organizations?

## Findings

### Existing P5 foundation

Xirang already has an admin-only, read-only Settings security-risk summary that surfaces bounded advisory cards. Recent P5 work added:

* `privileged_users_without_totp` — report-only privileged strong-auth posture.
* `ssh_host_key_trust_posture` — report-only global SSH host-key trust posture.
* `audit_log_integrity_posture` — report-only audit hash-chain integrity posture.

These remain valuable for personal/small-team users because they require no new deployment infrastructure and help operators notice risky local setup choices.

### What should move out of the default queue

The previous P5 candidate list included several enterprise-oriented controls. They remain security-relevant, but they are poor default fits for Xirang's current product scope:

* Enterprise policy UI and exception governance.
* Device trust governance.
* Command approval/inspection engine.
* Terminal/session recording platform.
* Full Vault/KMS/secret broker rollout.
* SSH CA issuance/rotation/revocation platform.

Common issue: each introduces new operational concepts, storage/privacy decisions, deployment dependencies, or workflow redesign that is disproportionate for personal and small-team self-hosting.

### Best-fit next slice

The best next slice is deployment secret posture:

* It uses existing runtime/config state.
* It fits the current Settings security-risk summary surface.
* It benefits personal/small-team deployments directly.
* It can be implemented as report-only without enforcing new requirements.
* It avoids exposing raw secret values by returning only generic findings.

Relevant existing code:

* `backend/internal/config/config.go` already classifies weak `JWT_SECRET` and `DATA_ENCRYPTION_KEY` outside development.
* `backend/internal/secure/crypto.go` has a default development encryption key that is only acceptable in development.
* `backend/internal/api/handlers/settings_handler.go` already aggregates security-risk cards and includes `weak_security_defaults`.

## Recommendation

Re-rank P5 toward small-team operational safety. Implement deployment secret posture next, then consider backup/restore safety posture, risk-summary readability, and dangerous-default cleanup. Keep enterprise-grade architecture items deferred unless explicitly requested later.
