# P2 Security Product Pattern Research

## Comparable Patterns

### 1. Time-bound elevation / JIT approval

Comparable products commonly add a request/approval layer after baseline identity controls. Teleport Access Requests are a representative pattern: users obtain elevated permissions by approval from one or more users in the cluster, configuration is role-based, and granted duration is bounded by request/session TTLs. This exists to reduce standing privileges without forcing every fleet to rework SSH host authentication immediately.

Typical shape:

- Request includes actor, target resource or role, reason, requested duration, and expiry.
- Approval can require a separate reviewer in multi-admin deployments, but self-hosted/small deployments often need a break-glass or self-approval mode to avoid deadlock.
- The grant gates use of existing credentials; it does not itself change stored credentials.
- Audit records request, approval/denial, grant use, and expiry.

Useful references:

- Teleport Access Requests: https://goteleport.com/docs/identity-governance/access-requests/access-request-configuration/ — role-based elevated access approval and bounded elevated duration.
- HashiCorp Boundary/Azure JIT workflow: https://www.hashicorp.com/en/blog/just-in-time-approval-workflow-with-boundary-and-azure — example of an approval workflow layered over access brokerage.

### 2. Ephemeral SSH identity / SSH certificates / external CA

Mature access products move from long-lived SSH keys toward short-lived identities. Teleport describes short-lived certificates as the core of its authentication model: users/services present valid CA-issued certificates for mutual SSH/TLS, certificates are tied to identity, expire automatically, and make actions traceable. Vault's SSH secrets engine supports signed SSH certificates and one-time SSH passwords. AWS EC2 Instance Connect uses a lighter pattern where an authorized API call pushes a temporary SSH public key that remains available briefly.

This exists to shrink the value of any one leaked key and shift revocation from manual cleanup to short TTLs. It usually requires remote host trust changes: sshd must trust a CA, an agent/proxy path must be introduced, or the platform must push temporary public keys.

Useful references:

- Teleport authentication architecture: https://goteleport.com/docs/reference/architecture/authentication/ — short-lived SSH certificates and identity-bound traceability.
- Teleport OpenSSH agentless mode: https://goteleport.com/docs/enroll-resources/server-access/openssh/openssh-agentless/ — existing OpenSSH servers trust Teleport's proxy/CA so RBAC and audit can be enforced at the proxy.
- Vault SSH secrets engine: https://developer.hashicorp.com/vault/docs/secrets/ssh — signed SSH certificates and one-time SSH password modes.
- Vault signed SSH certificates: https://developer.hashicorp.com/vault/docs/secrets/ssh/signed-ssh-certificates — role-based signing with TTLs.
- AWS EC2 Instance Connect: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-connect-methods.html — temporary public key delivery authorized by IAM and available briefly.

### 3. External secret broker / Vault/KMS integration

Secrets-manager integration is often staged after an app already has RBAC/audit and after customers ask to keep credentials outside the app database. Vault centralizes secret issuance/storage/audit; KMS usually wraps or unwraps encryption keys rather than serving as an SSH credential broker. Vault SSH OTP mode also shows a hard compatibility requirement: the remote host must contact Vault via a helper during login, so availability and TLS trust become part of the SSH login path.

This exists to reduce how much secret material the ops app persists and to align with organizations that already operate Vault/KMS. It is not a low-friction default for small self-hosted installs because auth bootstrap, provider configuration, lease renewal, fallback behavior, and outage handling all become product-critical.

Useful references:

- Vault SSH secrets engine: https://developer.hashicorp.com/vault/docs/secrets/ssh — brokered SSH credentials via signed certificates or one-time passwords.
- Vault SSH OTP: https://developer.hashicorp.com/vault/docs/secrets/ssh/one-time-ssh-passwords — one-time password flow, audit lease correlation, and host-side helper dependency.

### 4. Terminal/session recording and supervision

Session recording is usually added for accountability and compliance after the product already brokers interactive access. Teleport records SSH PTY output as what the user saw, while noting limitations: users can hide commands through encoding, scripts, or terminal settings, and enhanced recording requires deeper host-level support. Boundary stores recordings through external object storage such as S3/MinIO and can convert an SSH channel into asciicast for playback, but documentation calls out replay limitations and large-file-transfer risks.

This exists to provide evidence after interactive access, not to remove secrets. It can increase secret exposure if terminal output contains tokens, passwords, file contents, or customer data. It also only covers sessions routed through the product; background tasks and direct SSH remain unrecorded unless the product becomes a full SSH proxy.

Useful references:

- Teleport session recording: https://goteleport.com/docs/reference/architecture/session-recording/ — PTY output capture, limits, and security caveats.
- Boundary session recording: https://developer.hashicorp.com/boundary/docs/session-recording — external storage buckets and SSH-channel playback format.

## Trade-off Matrix

| Candidate | User value | Implementation risk | Compatibility | Secret-safety impact |
|---|---|---|---|---|
| Approval / JIT grants | High for high-risk operations: reduces standing access while preserving existing credential storage and SSH topology. Fits products that already have RBAC, MFA/step-up, and audit. | Medium. Requires new grant/request state, enforcement at credential-use boundaries, UI for pending/active grants, expiry, and audit. Avoids remote host changes. | High if introduced as opt-in or scoped to existing high-risk routes first. Single-admin deployments need self-approval/break-glass with reason + TOTP so they are not locked out. | Medium-high. Does not remove stored secrets, but reduces when and by whom they can be used; adds bounded intent around each use. |
| SSH certificates / external CA | High for mature fleets: removes reliance on long-lived SSH keys and makes access identity-bound and short-lived. | High. Requires CA model, certificate signing, principal mapping, host trust rollout, TTL/revocation semantics, and likely proxy/agent support. | Low-medium. Existing nodes must reconfigure sshd or trust a proxy/CA; unmanaged/heterogeneous hosts will lag. | High when complete: leaked certs expire quickly and private CA key can be isolated. Partial rollout leaves old key paths active. |
| Vault / KMS integration | Medium-high for organizations already running Vault/KMS. Lets the app reference external secrets or externalize key wrapping/issuance. | Medium-high. Requires provider auth, health checks, lease/TTL handling, fallback, audit correlation, token storage, and UX for misconfiguration/outage. | Medium. Must preserve local encrypted storage for small installs; Vault SSH OTP/cert modes may require host-side helpers or CA trust. | High only if secret material is not persisted/cached unsafely. KMS alone protects encryption keys but does not eliminate app-side SSH private key use. |
| Terminal session recording | Medium. Strong accountability for interactive terminal sessions and useful incident evidence. | Medium-high. Requires stream capture, storage backend, retention, playback, privacy notices, access controls, redaction expectations, and large-output handling. | Medium-low. Covers only Xirang web terminal unless Xirang becomes a full SSH proxy. Does not cover scheduled tasks, batch commands, file browser, or direct SSH. | Mixed. Improves forensic evidence but can create a new sensitive data store containing terminal output. Recording must be opt-in/retained carefully. |

## Recommendation

Recommended P2 MVP: **operation-bound JIT credential-use grants for existing high-risk Xirang operations**.

Why this slice is pragmatic:

- It builds directly on P1 assets: JWT/RBAC, TOTP step-up, credential-use audit, SSH key purpose/node/tag scope, and admin security-risk UI.
- It reduces blast radius without requiring remote host changes, Vault deployment, SSH CA rollout, or terminal transcript storage.
- It can be enforced at the same server-side boundaries already used for step-up and credential audit.
- It can stay compatible by starting with narrow high-risk operations and explicit opt-in settings rather than silently blocking all existing automation.

Suggested MVP behavior:

1. Introduce a time-boxed `credential access grant` concept tied to user, purpose/action, optional resource IDs, reason, status, expiry, and optional approver.
2. Bind grants to specific high-risk operation/resource combinations rather than accepting one generic recent TOTP proof for any protected action.
3. Enforce first on interactive/high-blast-radius paths that already have P1 step-up or admin gates: terminal open, SSH key export, config export with secrets, restore/snapshot restore, and manual task/command trigger paths where credentials are used.
4. Support two modes:
   - single-admin/self-hosted: TOTP + reason issues a short self-grant and writes audit evidence;
   - multi-admin: optional reviewer approval before grant activation.
5. Add bounded audit rows for request, approval/denial, grant use, expiry, and blocked use. Do not store command text, terminal streams, private key material, exported payloads, or raw credential metadata.
6. Surface active/pending grants and recent blocked attempts in an admin view or the existing security area, but keep remediation explicit and non-automatic.

Phased follow-ups:

- **P2 follow-up A:** policy-based grant requirements, e.g. require grants only for broad-scope keys, sudo/root nodes, production-tagged nodes, export-with-secrets, or terminal access.
- **P2 follow-up B:** external secret reference prototype for Vault/KMS users, limited to one provider shape and local fallback, with no full dynamic SSH CA rollout in the first pass.
- **P2 follow-up C:** terminal session metadata and optional recording for the web terminal only, with retention/access controls and clear warnings that replay files may contain sensitive output.
- **P3/P4:** SSH certificate/external CA integration after the app can express purpose/resource grants and after operators can opt nodes into CA trust safely.

Explicitly out of scope for the first P2 slice:

- Implementing a built-in SSH CA or requiring all nodes to trust a new CA.
- Replacing existing stored SSH keys/passwords or migrating all nodes away from current auth modes.
- Full Vault provider matrix, lease renewal engine, or host-side Vault OTP helper rollout.
- Recording scheduled task output, batch command streams, file browser payloads, SCP/SFTP transfer contents, or arbitrary terminal transcripts by default.
- Command-level approval, command parsing, command allow/deny policy, or shell-content inspection.
- SIEM/ticketing/chat-ops integrations, reviewer routing rules, or enterprise workflow builders.
- Automatic remote key rotation or remote account provisioning.

## Constraints for Xirang

Current repo/product constraints that shape the MVP:

- Xirang stores centralized SSH credentials and already has managed SSH key scope metadata: `model.SSHKey` includes `disabled`, `expires_at`, `allowed_purposes`, `allowed_node_ids`, `allowed_node_tags`, and `last_used_at` (`backend/internal/model/models.go:27-43`).
- Scope enforcement already happens before managed private key use: `ValidateSSHKeyScope` checks purpose, node ID, and tags; `ValidateSSHKeyPurpose` denies disabled/expired keys and disallowed purposes (`backend/internal/sshutil/scope.go:128-153`). Empty scope remains compatibility-broad (`backend/internal/sshutil/scope.go:189-193`; spec contract in `.trellis/spec/backend/quality-guidelines.md:244-250`).
- Purpose-aware SSH helpers already exist (`BuildSSHAuthForPurpose`, `BuildSSHAuthWithKeyForPurpose`) and return a safe `ResolvedCredential` label without exposing private key material (`backend/internal/sshutil/ssh_auth.go:76-123`). JIT enforcement can reuse the same purpose/resource boundary.
- P1 step-up is a short-lived JWT proof (`auth.StepUpProofTTL = 5 * time.Minute`) with `PurposeStepUp` (`backend/internal/auth/jwt.go:19-23`, `backend/internal/auth/jwt.go:98-124`). Current protected routes require the proof for SSH key export, task trigger/restore, snapshot restore, config export with secrets, and terminal open (`backend/internal/api/router.go:224-228`, `backend/internal/api/router.go:281-287`, `backend/internal/api/router.go:307-320`; `backend/internal/api/handlers/terminal_handler.go:213-226`). A P2 grant should bind intent to operation/resource more specifically than a reusable generic step-up proof.
- Credential audit is already a domain event table with safe labels and bounded metadata (`backend/internal/model/models.go:421-443`; `backend/internal/credentialaudit/audit.go:144-177`). The spec forbids raw passwords, private keys, TOTP/JWT/recovery codes, terminal input/output, command output, file contents, exported config payloads, and other secret-bearing material in credential audit (`.trellis/spec/backend/quality-guidelines.md:395-405`).
- The terminal path currently proxies WebSocket to SSH and audits open/failure/close, but it does not persist terminal streams (`backend/internal/api/handlers/terminal_handler.go:491-503`, `backend/internal/api/handlers/terminal_handler.go:535-548`, `backend/internal/api/handlers/terminal_handler.go:552-608`). Full recording would create a new sensitive evidence store and should not be the first blast-radius control.
- Security risk summary is advisory-only and admin-only; current risk codes include broad/reused/stale SSH keys, disabled/expired in-use keys, recent credential operations, root users, sudo nodes, and weak defaults (`.trellis/spec/backend/quality-guidelines.md:293-347`; `backend/internal/api/handlers/settings_handler.go:201-245`). JIT can use these signals later to decide where grants are required, but the summary itself should remain read-only.
- Inline node password/private-key credentials are explicitly not controlled by managed SSH key scope metadata (`.trellis/spec/backend/quality-guidelines.md:248-250`). A JIT gate at credential-use boundaries covers both managed keys and inline node credentials better than adding only more SSHKey fields.
- The project supports SQLite and PostgreSQL migrations and small self-hosted deployments. Any P2 schema should be simple, paired for both databases, and not depend on external services for baseline operation.
