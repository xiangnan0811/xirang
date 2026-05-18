# Research: PAM / bastion / zero-trust SSH patterns for Xirang security baseline hardening

- **Query**: Research comparable PAM/bastion/zero-trust SSH tools and patterns for Xirang security baseline hardening. Focus on JumpServer, Teleport, Boundary, Warpgate/Bastillion where relevant. Summarize common conventions: SSH key minimization, short-lived credentials/certificates, session audit/recording, RBAC/JIT access, approval/step-up. Map these patterns onto Xirang's current layered-hardening boundary (not a full bastion rewrite).
- **Scope**: mixed
- **Date**: 2026-05-18

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go` | Core security-sensitive models: `User`, `SSHKey`, `Node`, `AuditLog`, `LoginFailure`; secret-stripping helpers and GORM encryption hooks. |
| `backend/internal/secure/crypto.go` | `DATA_ENCRYPTION_KEY`-backed AES-GCM encryption helpers with v2 Argon2id-derived key path and legacy v1 decrypt support. |
| `backend/internal/middleware/auth.go` | Bearer JWT middleware, rejects `2fa_pending` tokens, checks `token_version`, and stores user/role/token in context. |
| `backend/internal/middleware/rbac.go` | Role-to-permission map for `admin`, `operator`, and `viewer`; route-level RBAC helper. |
| `backend/internal/middleware/ownership.go` | Object-level node/task ownership checks for operators plus owner node ID lookup for realtime filtering. |
| `backend/internal/middleware/audit.go` | Hash-chained audit log middleware and exported audit writer for non-HTTP paths such as WebSockets. |
| `backend/internal/middleware/rate_limit.go` | Login and API rate-limit middleware. |
| `backend/internal/auth/jwt.go` | Normal JWT generation, 5-minute `2fa_pending` token generation, token revocation, token IDs. |
| `backend/internal/auth/service.go` | Login lockout integration, timing-pad dummy password check, TOTP-gated login flow, password/role updates that bump `token_version`. |
| `backend/internal/api/router.go` | Central `/api/v1` route registration with auth, audit, RBAC, ownership, admin-only routes, and WebSocket routes outside HTTP auth. |
| `backend/internal/api/handlers/realtime_auth.go` | WebSocket token validation mirroring HTTP auth, including `2fa_pending` rejection, `token_version`, role, and permission checks. |
| `backend/internal/api/handlers/ws_handler.go` | Log WebSocket authorization bridge; maps operator ownership into realtime access scope. |
| `backend/internal/ws/hub.go` | WebSocket origin checks, auth-first protocol, task access filtering, backfill filtering, max client handling. |
| `backend/internal/api/handlers/terminal_handler.go` | WebSocket SSH terminal; admin-only auth, session cap, 30-minute timeout, open/close/failure audit events, SSH dial path. |
| `backend/internal/api/handlers/ssh_key_handler.go` | SSH key input validation, sanitized API responses with derived public key/fingerprint, public-key-only exports. |
| `backend/internal/sshutil/ssh_auth.go` | SSH credential resolution, key/password auth construction, host-key callback and known_hosts behavior, SSH dial timeout. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend security contracts for sanitized responses, correct Auth/RBAC/ownership middleware, secret sanitizer, and denial-case tests. |
| `.trellis/spec/backend/deployment-runtime.md` | Runtime boundary notes: public HTTP entry, external TLS termination, production secret requirements. |

### Code Patterns

#### Current layered-hardening boundary in Xirang

Xirang is not currently structured as a full PAM/bastion/zero-trust SSH gateway. Its current boundary is layered around authenticated API access, role/permission checks, object ownership, encrypted stored secrets, sanitized responses, auditability, WebSocket protocol auth, and SSH client-side hardening for managed operations.

Key boundary layers found:

1. **Authentication and token validity**
   - HTTP routes under `secured` use `AuthMiddleware`, `AuditLogger`, API rate limiting, and body-size limiting before resource routes (`backend/internal/api/router.go:124-128`).
   - `AuthMiddleware` parses `Authorization: Bearer`, rejects `Purpose == "2fa_pending"`, checks `token_version` against DB, and sets user identity/role context (`backend/internal/middleware/auth.go:20-64`).
   - Realtime/WebSocket auth mirrors this: `authorizeRealtimeToken` parses JWT, rejects `2fa_pending`, checks `token_version`, and enforces role/permission requirements (`backend/internal/api/handlers/realtime_auth.go:18-48`).

2. **RBAC**
   - The built-in role model is `admin`, `operator`, `viewer`; `rolePermissions` grants narrower permissions to operators and read-oriented permissions to viewers (`backend/internal/middleware/rbac.go:8-78`).
   - Routes consistently apply `middleware.RBAC("...")` or `middleware.RequireRole("admin")`, e.g. users are `users:manage`, SSH keys are `ssh_keys:*`, terminal uses admin-only protocol auth, restore/config/settings/system routes are admin-only (`backend/internal/api/router.go:142-229`, `backend/internal/api/router.go:282-345`).

3. **Object-level ownership**
   - Operator access is constrained by `node_owners`: `OwnershipNodeCheck` allows admin/viewer through but requires operator to own the route node ID (`backend/internal/middleware/ownership.go:19-58`).
   - `OwnershipTaskCheck` verifies a task's `node_id` through `node_owners` before operator task access (`backend/internal/middleware/ownership.go:70-110`).
   - WebSocket logs map operator-owned nodes into `AccessScope.AllowedNodeIDs` (`backend/internal/api/handlers/ws_handler.go:35-56`) and the hub filters task events by role/ownership (`backend/internal/ws/hub.go:335-384`).

4. **Secret minimization and encryption at rest**
   - `SSHKey.PrivateKey` has `json:"-"`; comments state handlers use sanitized response structs and the model tag is defense-in-depth (`backend/internal/model/models.go:26-38`).
   - `Node.Sanitized()` clears `Password`, `PrivateKey`, and nested `SSHKey.PrivateKey` before API responses (`backend/internal/model/models.go:40-50`).
   - `SSHKey`, `Node`, and `User` hooks encrypt/decrypt private keys, passwords, TOTP secrets, and recovery codes (`backend/internal/model/models.go:573-669`).
   - `secure.EncryptIfNeeded`/`DecryptIfNeeded` preserve already-encrypted values and use `DATA_ENCRYPTION_KEY`; production without the key is rejected in non-development env (`backend/internal/secure/crypto.go:42-69`, `backend/internal/secure/crypto.go:118-173`).
   - SSH key API responses return derived public key/fingerprint and never the private key (`backend/internal/api/handlers/ssh_key_handler.go:42-73`, `backend/internal/api/handlers/ssh_key_handler.go:96-117`). Public exports derive `authorized_keys`/CSV public keys rather than exporting private key material (`backend/internal/api/handlers/ssh_key_handler.go:486-580`).

5. **Audit trail**
   - Non-GET/HEAD/OPTIONS secured HTTP routes are audit logged after request handling (`backend/internal/middleware/audit.go:20-55`).
   - Audit logs are hash-chained by storing `PrevHash` from the prior row and computing `EntryHash` in a transaction (`backend/internal/middleware/audit.go:58-91`); model fields are `UserID`, `Username`, `Role`, `Method`, `Path`, `StatusCode`, `ClientIP`, `UserAgent`, hashes, timestamp (`backend/internal/model/models.go:400-413`).
   - Audit listing/export supports filters by username, role, method, path, status, user, and time (`backend/internal/api/handlers/audit_handler.go:25-83`, `backend/internal/api/handlers/audit_handler.go:141-176`).
   - Terminal WebSocket writes explicit audit rows for auth failure, missing node, SSH auth/init/dial/PTY/shell failures, open, and close (`backend/internal/api/handlers/terminal_handler.go:104-130`, `backend/internal/api/handlers/terminal_handler.go:184-230`, `backend/internal/api/handlers/terminal_handler.go:249-290`, `backend/internal/api/handlers/terminal_handler.go:318-348`, `backend/internal/api/handlers/terminal_handler.go:372-383`).

6. **Step-up / login hardening currently present**
   - Login checks persistent lockout state before password verification, does a dummy bcrypt comparison for nonexistent users to reduce timing enumeration, registers failures/successes, and returns a 2FA-pending login token for TOTP-enabled users (`backend/internal/auth/service.go:79-115`).
   - `Generate2FAPendingToken` creates a 5-minute token with `Purpose: "2fa_pending"` (`backend/internal/auth/jwt.go:67-90`).
   - Password or role changes increment `token_version`, invalidating older tokens (`backend/internal/auth/service.go:164-195`).
   - Login rate limit rejects excessive attempts with HTTP 429 (`backend/internal/middleware/rate_limit.go:94-107`).

7. **Realtime and terminal boundary**
   - WebSocket routes are outside `secured` because browser WebSocket cannot set custom headers; first protocol message carries auth and runs RBAC (`backend/internal/api/router.go:354-357`).
   - The log hub enforces max clients, origin checks, first-message auth within 5 seconds, per-task access checks, and backfill filtering (`backend/internal/ws/hub.go:164-281`).
   - Terminal sessions are admin-only, max 10, pre-reserved to avoid TOCTOU session-count races, and have a 30-minute context timeout (`backend/internal/api/handlers/terminal_handler.go:25-28`, `backend/internal/api/handlers/terminal_handler.go:62-83`, `backend/internal/api/handlers/terminal_handler.go:142-149`, `backend/internal/api/handlers/terminal_handler.go:184-185`, `backend/internal/api/handlers/terminal_handler.go:249-253`).

8. **SSH client-side hardening**
   - `BuildSSHAuth` resolves node-bound reusable SSH keys or node-local private keys, validates/prepares keys, and parses them into Go SSH signers; password auth is also supported (`backend/internal/sshutil/ssh_auth.go:23-87`).
   - `ResolveSSHHostKeyCallback` defaults `SSH_STRICT_HOST_KEY_CHECKING` to true, uses known_hosts, can auto-accept unknown hosts when configured, and logs when strict checking is disabled (`backend/internal/sshutil/ssh_auth.go:123-173`).
   - `DialSSH` uses a 5-second SSH client timeout (`backend/internal/sshutil/ssh_auth.go:175-197`).

#### Mapping common PAM/bastion conventions onto Xirang's current boundary

| Common convention | Comparable-tool pattern | Xirang current boundary mapping |
|---|---|---|
| SSH key minimization | Bastillion emphasizes public-key distribution, profile-based key removal, and discouraging private-key sharing; Teleport scans authorized keys/private keys to find bypass paths; Boundary models credentials as session-bound resources. | Xirang stores SSH secrets centrally for managed operations but minimizes exposure: private keys are encrypted by hooks, `json:"-"`, sanitized from node/SSH-key responses, and exports derive public keys only. Current boundary is response/at-rest minimization, not elimination of stored SSH private keys. |
| Short-lived credentials/certificates | Teleport roles include certificate TTL (`max_session_ttl`) and Access Requests grant temporary credentials; Boundary sessions have expiration and connection limits; Boundary credentials may include SSH certificates. | Xirang has short-lived API/auth artifacts (`2fa_pending` 5-minute token, JWT TTL, revocation, `token_version`) but no SSH certificate authority or per-SSH-session short-lived credential model found. This maps to control-plane session hardening rather than data-plane SSH cert issuance. |
| Session audit/recording | JumpServer positions itself around pre-authorization, in-session monitoring, post-audit; Teleport and Warpgate provide session recording/replay; Warpgate also advertises command-level audit; Boundary has session-recording domain/docs. | Xirang has API/WS audit rows, hash chaining, terminal open/close/failure audit, and task logs. No command-level terminal recording/replay implementation was found. The current boundary records control actions and operational task logs, not full bastion session playback. |
| RBAC / least privilege | Boundary is least-privilege, allow-only RBAC; Teleport roles use allow/deny and default-deny semantics; Bastillion maps users/LDAP roles to profiles; JumpServer has user/asset/account/authorization modules. | Xirang has fixed roles plus permission keys and object ownership. Operators can be constrained to owned nodes/tasks; admin-only gates remain for restore/settings/config/system and terminal. This is static RBAC + ownership, not dynamic resource/role access requests. |
| JIT access and approvals | Teleport Access Requests provide Role/Resource Access Requests, dual authorization, no self-approval, temporary elevated privileges; Warpgate docs expose “tickets” but subpage fetch failed; JumpServer includes authorization/work-order areas in docs navigation. | No approval/work-order/JIT access request model was found in Xirang. The closest current boundary is role assignment, ownership assignment, TOTP login, and admin-only route gating. |
| Step-up authentication | Teleport supports per-session MFA and MFA for admin actions; Warpgate and Bastillion include native 2FA/TOTP; JumpServer docs list multiple authentication settings. | Xirang supports TOTP 2FA at login, rejects pending 2FA tokens from HTTP/WS APIs, rate-limits login, locks failed login attempts, and invalidates tokens after sensitive account changes. No per-action/per-session step-up gate was found. |
| Layered deployment boundary | Bastion products often terminate/proxy sessions themselves. Teleport notes recording proxy mode decrypts traffic and performs RBAC/audit/recording but is less secure than recording at node because proxy sees decrypted data. | Xirang's deployment spec keeps public entry HTTP on port 10761 with external TLS termination. Current hardening boundary is app/API/WS plus SSH client validation, not a network-level transparent bastion proxy. |

### External References

- [JumpServer documentation](https://docs.jumpserver.org/) — Describes JumpServer as an open-source bastion/PAM-style “4A” security audit system that manages/logs assets and supports “事前授权、事中监察、事后审计”; docs navigation includes account management, authorization, access control, session audit, command audit, file transfer, job audit, and work-order audit.
- [Teleport Just-in-Time Access Requests](https://goteleport.com/docs/identity-governance/access-requests/) — Access Requests cover Role and Resource requests, temporary elevated credentials, dual authorization, no self-approval, and least-privilege JIT access.
- [Teleport Access Lists](https://goteleport.com/docs/identity-governance/access-lists/) — Long-term access is tied to roles/traits with regular audit and control, integrating into Teleport RBAC.
- [Teleport Role Reference](https://goteleport.com/docs/reference/access-controls/roles/) — Roles define RBAC allow/deny rules; “nothing is allowed by default”; options include SSH certificate TTL (`max_session_ttl`), session recording policy, enhanced recording, per-session MFA, access-request review permissions, and moderated-session policies.
- [Teleport Session Recording architecture](https://goteleport.com/docs/reference/architecture/session-recording/) — Default SSH node recording is preferred because client-to-node SSH is encrypted end-to-end; recording proxy mode decrypts, performs RBAC checks, records sessions, emits audit events, and has a larger proxy trust boundary.
- [Teleport encrypted session recordings](https://goteleport.com/docs/enroll-resources/server-access/guides/encrypted-session-recordings/) — Covers encrypted recordings and key availability/replay constraints.
- [Teleport per-session MFA](https://goteleport.com/docs/zero-trust-access/authentication/per-session-mfa/) and [MFA for administrative actions](https://goteleport.com/docs/zero-trust-access/authentication/mfa-for-admin-actions/) — Step-up / repeated verification patterns for sessions and admin operations.
- [Teleport SSH keys scan](https://goteleport.com/docs/identity-security/integrations/ssh-keys-scan/) — Scans authorized keys and private-key fingerprints to detect SSH access paths that could bypass Teleport; notes the private key itself is not sent to the Teleport cluster.
- [HashiCorp Boundary docs](https://developer.hashicorp.com/boundary/docs) — Boundary is positioned around least-privilege access to critical systems; permissions are composable, RBAC, and allow-only; connections are protected with TLS and data at rest is secured.
- [Boundary credentials domain model](https://developer.hashicorp.com/boundary/docs/concepts/domain-model/credentials) — Credentials bind an identity to permissions/capabilities on a host for a session; supported types include username/password, SSH private key, SSH certificate, and JSON.
- [Boundary sessions domain model](https://developer.hashicorp.com/boundary/docs/concepts/domain-model/sessions) — Sessions are user/host connection sets with credential scope, expiration time, connection limits, and termination on expiry, cancellation, or associated resource deletion.
- [Boundary roles domain model](https://developer.hashicorp.com/boundary/docs/concepts/domain-model/roles) — Roles are collections of permissions assigned to principals (users/groups/managed groups) in scopes.
- [Boundary session recording docs](https://developer.hashicorp.com/boundary/docs/session-recording) and [session-recordings domain model](https://developer.hashicorp.com/boundary/docs/domain-model/session-recordings) — Session recording is a first-class Boundary documentation area.
- [Warpgate README](https://raw.githubusercontent.com/warp-tech/warpgate/master/README.md) — Warpgate is a transparent SSH/HTTPS/Kubernetes/MySQL/PostgreSQL bastion; assigns users to specific targets, records every session for live view/replay, has native TOTP/OIDC, command-level audit, full session recording, and no custom client requirement.
- [Warpgate docs sitemap](https://warpgate.null.page/sitemap.xml) — Public docs include roles, tickets, auth, OTP, and SSH target pages; individual `docs.warpgate.dev` fetches failed in this environment with an SSL EOF error.
- [Bastillion README](https://raw.githubusercontent.com/bastillion-io/Bastillion/master/README.md) — Bastillion is a web-based SSH console and key-management tool with 2FA, SSH public key management/distribution, secure web shells, command sharing, LDAP role-to-profile mapping, and opt-in internal audit.
- [Bastillion Features](https://www.bastillion.io/features.html) — Emphasizes preventing SSH key sprawl, public-key distribution via profiles, strong passphrases, disabling administrative keys for rotation, 2FA, profile-based system access, LDAP role mapping, and session auditing.
- [Bastillion Whitepaper](https://www.bastillion.io/docs/using/whitepaper) — Frames centralized SSH control as a way to tie sessions back to actual users, reduce scattered system-local audit gaps, and mitigate private-key/authorized_keys risks.

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — Security-sensitive backend work must avoid returning nodes/SSH keys/integrations/executor configs without sanitizing sensitive fields, add routes with correct Auth/RBAC/ownership, keep sensitive data encrypted via hooks, sanitize command-output-like evidence, and test SSH auth/path/encryption/RBAC/ownership denial cases (`.trellis/spec/backend/quality-guidelines.md:16-31`, `.trellis/spec/backend/quality-guidelines.md:34-55`, `.trellis/spec/backend/quality-guidelines.md:58-73`).
- `.trellis/spec/backend/deployment-runtime.md` — Production runtime contract requires filled production secrets and keeps TLS/HTTPS termination external to user-managed infrastructure, matching a layered app boundary rather than an embedded full bastion ingress (`.trellis/spec/backend/deployment-runtime.md:21-32`, `.trellis/spec/backend/deployment-runtime.md:33-42`).
- `.trellis/spec/guides/documentation-truth-guide.md` — External facts should be verified when claimed and public docs should avoid process/archive material; relevant if research outputs become product/maintainer docs later (`.trellis/spec/guides/documentation-truth-guide.md:20-30`, `.trellis/spec/guides/documentation-truth-guide.md:40-48`).

## Caveats / Not Found

- No Xirang data model or route for SSH certificate authority, short-lived SSH certificates, per-SSH-session credential minting, or credential broker/injector was found in the searched backend paths.
- No Xirang approval workflow, JIT access request, reviewer/approver role, work-order, or dual-authorization model was found in the searched backend/frontend/spec paths.
- No full terminal session recording/replay or command-level interactive terminal audit implementation was found. Current terminal audit covers open/close and failure milestones; task execution logs cover Xirang-managed tasks, not arbitrary interactive terminal commands.
- Warpgate README and sitemap were reachable; individual `docs.warpgate.dev` pages for roles/tickets/auth/OTP/SSH returned SSL EOF errors during this research run.
- External product docs may drift; URLs and wording were checked on 2026-05-18.