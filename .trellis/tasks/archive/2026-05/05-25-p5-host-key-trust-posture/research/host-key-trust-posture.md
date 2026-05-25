# Research: SSH host-key trust posture patterns

- **Query**: Research SSH host-key trust posture/inventory patterns for operations platforms. Identify 2-4 comparable patterns (global strict checking, TOFU/known_hosts inventory, mismatch alerting, SSH CA), explain common conventions, then map them to Xirang constraints: compatibility-first, no enforcement in MVP, no raw hostnames/fingerprints/host-sensitive strings in UI/logs/docs, reuse Settings security-risk summary where possible.
- **Scope**: mixed
- **Date**: 2026-05-25

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/sshutil/ssh_auth.go` | Central Go SSH host-key callback resolution: strict mode switch, known_hosts loading, first-seen append path, SSH dial helper. |
| `backend/internal/task/executor/executor.go` | rsync executor builds OpenSSH CLI options for `StrictHostKeyChecking` and `UserKnownHostsFile`. |
| `backend/internal/api/handlers/node_doctor_handler.go` | Fleet Doctor classifies `known_hosts`/host-key outcomes into structured checks and sanitized evidence. |
| `backend/internal/api/handlers/settings_handler.go` | Settings security-risk summary already reports weak SSH host-key defaults under `weak_security_defaults`. |
| `web/src/lib/api/settings-api.ts` | Frontend mapper and supported risk-code union for Settings risk summary. |
| `web/src/pages/settings-page.system.tsx` | Settings system tab renders risk summary cards as advisory text/examples. |
| `backend/.env.example` | Development/example SSH host-key defaults: strict checking off; auto-accept enabled. |
| `backend/.env.production.example` | Production example enables strict host-key checking while leaving first-seen auto-accept enabled. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend contract for Settings risk summary: admin-only, read-only, advisory-only, sanitized bounded examples. |
| `.trellis/spec/frontend/type-safety.md` | Frontend contract for mapping/rendering Settings risk summary without adding host/credential details or actions. |
| `.trellis/spec/backend/error-handling.md` | Node Doctor contract: known_hosts/auth/network/handshake failures become structured checks, not 500s; evidence must be sanitized. |
| `.trellis/spec/backend/logging-guidelines.md` | Logging contract includes security-relevant SSH host-key warnings but excludes secrets. |

### Comparable Patterns

#### 1. Global strict host-key checking

Common convention:

- A platform-level switch controls whether SSH client connections verify server host keys.
- OpenSSH models this with `StrictHostKeyChecking`: `yes` refuses unknown and changed host keys; `accept-new` accepts previously unseen host keys but refuses changed keys; `no`/`off` is compatibility-oriented and less protective; `ask` is interactive and usually unsuitable for server-side background jobs.
- Operations platforms usually expose this as an environment/deployment posture rather than per-command free-form input, because tasks, probes, terminals, file browsing, and diagnostics all need consistent behavior.

Xirang mapping:

- Central Go SSH paths use `SSH_STRICT_HOST_KEY_CHECKING`, defaulting to `true` in code, and return `ssh.InsecureIgnoreHostKey()` when disabled (`backend/internal/sshutil/ssh_auth.go:83-92`).
- rsync CLI execution mirrors the same posture through OpenSSH options: strict mode uses `StrictHostKeyChecking=yes` or `accept-new` plus `UserKnownHostsFile`; non-strict mode emits `StrictHostKeyChecking=no` (`backend/internal/task/executor/executor.go:151-173`).
- Existing examples are compatibility-first: `.env.example` sets `SSH_STRICT_HOST_KEY_CHECKING=false` (`backend/.env.example:75-78`), while production example sets it to `true` (`backend/.env.production.example:70-73`).
- MVP alignment: keep this posture advisory in Xirang. Do not block node creation, task scheduling, terminal use, file browsing, or probes solely because strict checking is disabled; represent the posture through Settings risk summary and Doctor evidence.

#### 2. TOFU / known_hosts inventory

Common convention:

- Trust On First Use (TOFU) records the first host key observed for a target into a known_hosts inventory, then verifies future connections against that inventory.
- OpenSSH `accept-new` is the common non-interactive TOFU mode: first-seen host keys are added, but changed keys are refused.
- known_hosts inventory files are often deployment-scoped and should be treated as sensitive operational evidence. Host patterns can be hashed with OpenSSH tooling when the inventory is user-visible or stored outside a protected runtime path.
- Libraries commonly expose known_hosts as a `HostKeyCallback`; Go's `golang.org/x/crypto/ssh/knownhosts` parses known_hosts files and returns errors that distinguish unknown keys from mismatches.

Xirang mapping:

- `ResolveSSHHostKeyCallback()` loads `SSH_KNOWN_HOSTS_PATH` when strict checking is enabled, creating the file if needed (`backend/internal/sshutil/ssh_auth.go:94-109`, `backend/internal/sshutil/ssh_auth.go:159-169`).
- Unknown hosts in strict mode are accepted by default when `SSH_AUTO_ACCEPT_NEW_HOSTS=true`; the key is appended via `AppendKnownHost`, the callback is refreshed, and the new key is immediately re-checked (`backend/internal/sshutil/ssh_auth.go:110-132`, `backend/internal/sshutil/ssh_auth.go:171-224`).
- Production example still defaults `SSH_AUTO_ACCEPT_NEW_HOSTS=true`, with a comment that known-host key changes remain refused (`backend/.env.production.example:107-109`).
- MVP alignment: inventory should remain operationally advisory and compatibility-first. If surfaced in Xirang UI, show only counts/status labels, not raw host aliases, hostnames, ports, or fingerprint strings.

#### 3. Host-key mismatch alerting / diagnostic posture

Common convention:

- Mature operations tools do not silently auto-replace host keys on mismatch; a changed known host key is treated differently from a new unknown host.
- Common user-facing behavior is: classify the event as a host-key/known_hosts conflict, stop dependent SSH actions for that diagnostic run, and tell an operator to verify the change through an out-of-band process.
- The platform can summarize posture and recent signals without publishing the raw host identifier or fingerprint value.

Xirang mapping:

- Fleet Doctor already includes a `known_hosts` check in its SSH diagnostic flow; host-key callback setup failure yields a structured failed check, and known_hosts failure during dial produces a failed `known_hosts` check plus a skipped SSH-dependent path (`backend/internal/api/handlers/node_doctor_handler.go:129-166`).
- Doctor classification treats errors containing `knownhosts`, `host key`, or `主机密钥` as `known_hosts 校验失败或主机密钥冲突` and returns a bounded suggestion (`backend/internal/api/handlers/node_doctor_handler.go:318-349`).
- Doctor evidence/suggestions are sanitized before response (`backend/internal/api/handlers/node_doctor_handler.go:251-257`), and the Trellis contract requires known_hosts/auth/network/handshake failures to return structured checks rather than raw 500s (`.trellis/spec/backend/error-handling.md:103-118`).
- Settings risk summary already captures two SSH posture signals in `weak_security_defaults`: disabled host-key checking and automatic acceptance of unknown host keys (`backend/internal/api/handlers/settings_handler.go:587-639`).
- MVP alignment: mismatch posture can be represented as diagnostic status and advisory risk, not as hard enforcement. UI/logs/docs should avoid raw hostnames, raw paths, raw fingerprints, and command output.

#### 4. SSH host certificates / SSH CA

Common convention:

- SSH CAs move trust from per-host key pinning to certificate validation: clients trust one or more CA public keys and accept host certificates signed by those CAs.
- OpenSSH supports host/user certificates; known_hosts can contain CA trust anchors for host certificates, and host keys can be signed with `ssh-keygen`.
- This pattern is common for larger fleets because host replacement or rotation can avoid per-node known_hosts updates when the CA and principal policy are correct.

Xirang mapping:

- Current code paths are known_hosts/callback based; there is no discovered Xirang model or migration for SSH CA trust anchors, host certificate principals, certificate validity windows, or per-node host-certificate inventory.
- Compatibility-first MVP mapping: treat SSH CA as a comparable future trust pattern only. Do not introduce enforcement semantics into the MVP research scope; if discussed in UI/docs, use generic posture labels rather than raw CA contents, host certificate principals, or fingerprints.

### Code Patterns

- **Central callback reuse**: Most Go SSH consumers call `sshutil.ResolveSSHHostKeyCallback()` before dialing: node logs (`backend/internal/nodelogs/ssh_runner.go:31-42`), task SSH helper (`backend/internal/task/executor/ssh_connect.go:41`), integrity verifier (`backend/internal/task/verifier/verifier.go:253-264`), file browser (`backend/internal/api/handlers/file_handler.go:373-390`), Docker/file/terminal/node handlers, probes, and related handlers found by code search. This makes posture summary and diagnostic language reusable across surfaces.
- **Two implementation families**: Go SSH paths use `golang.org/x/crypto/ssh` `HostKeyCallback`; rsync uses external `ssh` command-line options (`backend/internal/task/executor/executor.go:151-173`). Any posture description should cover both families.
- **Known-host append behavior**: `AppendKnownHost` normalizes the host argument and de-duplicates exact host/key lines before appending (`backend/internal/sshutil/ssh_auth.go:171-224`). This is TOFU inventory behavior, not a separate database-backed inventory model.
- **Settings risk summary is advisory-only**: The backend spec says the endpoint is admin-only, read-only, must not mutate nodes/keys/settings/known_hosts/remote machines, and examples must be sanitized bounded labels only (`.trellis/spec/backend/quality-guidelines.md:327-345`).
- **Frontend risk rendering is mapper-driven**: The frontend mapper normalizes risk-code/severity/count fields and currently supports `weak_security_defaults` as a known code (`web/src/lib/api/settings-api.ts:26-137`). Components render backend-provided advisory text/examples and must not add host/credential details or mutation actions (`.trellis/spec/frontend/type-safety.md:344-351`).
- **Privacy constraints already exist for diagnostic output**: Node Doctor evidence must not return passwords, private keys, tokens, proxy endpoints, hostnames, raw paths, command text, or full output (`.trellis/spec/backend/error-handling.md:99-109`). Runtime evidence sanitizers also explicitly preserve only generic option prefixes such as `stricthostkeychecking`/`userknownhostsfile` while hiding host/path-like tokens (`backend/internal/runtimeevidence/sanitize.go:24-84`, `backend/internal/task/executor/runtime_sanitize.go:30-88`).

### Common Conventions Summary

| Pattern | Typical posture semantics | Inventory model | User-facing surface |
|---|---|---|---|
| Global strict checking | Platform-wide allow/deny behavior for unknown or changed host keys. | Optional known_hosts file. | Deployment setting, risk summary, diagnostic status. |
| TOFU / known_hosts | First observed key is recorded; later changes are rejected. | known_hosts file, sometimes hashed/protected. | Advisory counts/status; no raw host or key material in broad UI. |
| Mismatch alerting | Changed key is a conflict requiring operator verification. | Existing known_hosts entry remains source of truth. | Warning/check result; dependent checks may skip. |
| SSH CA | Trust a CA key and signed host certificates instead of per-host pins. | CA trust anchors and certificate principals/validity metadata. | Fleet trust posture; usually not per-host raw certificate dumps. |

### Xirang Constraint Mapping

- **Compatibility-first**: Xirang already has compatibility defaults in example envs and auto-accept behavior. Research mapping should preserve the idea that host-key trust posture is surfaced as advisory context rather than forcing immediate production-hardening behavior.
- **No enforcement in MVP**: Existing Settings risk summary contract explicitly says advisory-only and no mutation. For MVP host-key posture, the same model fits: report weak defaults and diagnostic status, but do not block operations from the risk summary itself.
- **No raw hostnames/fingerprints/host-sensitive strings in UI/logs/docs**: Xirang specs already require sanitized examples and diagnostic evidence. Host-key posture inventory should be summarized with safe labels/counts/statuses; do not display raw host aliases, raw known_hosts lines, raw fingerprint values, raw remote paths, or raw command output.
- **Reuse Settings security-risk summary where possible**: `weak_security_defaults` already includes `SSH_STRICT_HOST_KEY_CHECKING=false` and `SSH_AUTO_ACCEPT_NEW_HOSTS=true` as examples. That is the closest existing UX/API surface for MVP posture reporting and avoids adding a separate enforcement path.
- **Keep Doctor as diagnostic context**: Node Doctor already has `known_hosts` classification and sanitized evidence, making it the current place to explain per-node connection trust status without revealing host-sensitive details.

### External References

- [OpenBSD `ssh_config(5)` — `StrictHostKeyChecking`](https://man.openbsd.org/ssh_config#StrictHostKeyChecking) — defines common OpenSSH semantics for `yes`, `accept-new`, `ask`, and `no`/`off` host-key checking modes.
- [OpenBSD `ssh-keygen(1)` — `-H`](https://man.openbsd.org/ssh-keygen#H) — documents hashing known_hosts hostnames, relevant to inventory privacy when known_hosts material may be exposed outside protected runtime storage.
- [OpenBSD `ssh-keygen(1)` — Certificates](https://man.openbsd.org/ssh-keygen#CERTIFICATES) — documents OpenSSH certificate signing concepts used by SSH CA host/user certificate patterns.
- [Go `golang.org/x/crypto/ssh/knownhosts`](https://pkg.go.dev/golang.org/x/crypto/ssh/knownhosts) — documents the Go known_hosts parser/callback used by Xirang's current implementation family.

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md` — Settings security-risk summary contracts, including advisory-only behavior and sanitized bounded examples.
- `.trellis/spec/frontend/type-safety.md` — frontend risk-summary mapper/rendering contracts, including supported codes and no client-side enrichment with host/credential details.
- `.trellis/spec/backend/error-handling.md` — Node Doctor diagnostic contract for known_hosts/auth/network/handshake failures and sanitized evidence.
- `.trellis/spec/backend/logging-guidelines.md` — logging guidance for security-relevant SSH host-key warnings without leaking sensitive material.

## Caveats / Not Found

- No database-backed host-key inventory model was found; current inventory is file-backed known_hosts via `SSH_KNOWN_HOSTS_PATH`.
- No SSH CA trust-anchor or host-certificate model was found in current Xirang code or migrations.
- Current risk-code unions do not have a dedicated host-key posture code; SSH posture is currently represented under `weak_security_defaults`.
- Some current callback error/log formatting includes the host callback argument in code paths around unknown-host handling (`backend/internal/sshutil/ssh_auth.go:110-120`). Treat that as a relevant privacy constraint when documenting or surfacing posture, without copying raw host identifiers into UI/logs/docs.
