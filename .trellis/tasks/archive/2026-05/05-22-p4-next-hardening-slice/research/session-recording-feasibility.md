# Research: session recording feasibility

- **Query**: Research whether terminal/session recording or command approval is a feasible next P4 slice in this repo, and compare it against smaller credential-provider follow-up slices. Inspect current terminal/task code and specs.
- **Scope**: internal
- **Date**: 2026-05-22

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/api/handlers/terminal_handler.go` | WebSocket SSH PTY bridge; central terminal auth, step-up, JIT grant, SSH auth, audit, and raw I/O pass-through implementation. |
| `web/src/components/web-terminal.tsx` | xterm.js terminal client; sends first auth message with token/proof, writes SSH output to terminal, sends raw keyboard data to WebSocket, handles terminal grant retry dialog. |
| `backend/internal/api/handlers/credential_access_grant.go` | Row-backed JIT grant implementation for terminal/task/batch/config/restore operations, including terminal WebSocket enforcement before node load and SSH credential resolution. |
| `backend/internal/credentialaudit/audit.go` | Credential audit writer and sanitizer; explicitly drops metadata keys/values containing stream/command/output/content/payload/credential markers. |
| `backend/internal/model/models.go` | Domain models for `Task`, `TaskRun`, `CredentialAuditEvent`, `CredentialAccessGrant`, and `TaskLog`; no terminal session/recording model found. |
| `backend/internal/api/router.go` | Routes for credential audit/grants, task triggers, batch commands, and WebSocket terminal/log routes; terminal WS is outside `secured` middleware and authenticates inside the WS protocol. |
| `backend/internal/task/executor/command_executor.go` | Non-interactive command executor; command is structured in `Task.Command`, logged before execution, and stdout/stderr are persisted via task logs. |
| `backend/internal/task/runner.go` | Task trigger/run lifecycle; creates `TaskRun`, wraps execution context with credential-audit correlation, and executes resolved executor. |
| `backend/internal/task/log_writer.go` | Task log persistence and WebSocket log publishing; existing persisted stream model applies to task logs, not terminal sessions. |
| `backend/internal/task/executor/ssh_connect.go` | Purpose-aware SSH dialing for task executors and runtime credential audit writes. |
| `backend/internal/api/handlers/batch_handler.go` | Batch command creation; validates command, ownership, step-up, and per-node grants before creating command tasks. |
| `backend/internal/sshutil/credential_provider.go` | SSH credential provider interface and local provider implementation. |
| `backend/internal/sshutil/scope.go` | SSH key purpose/node/tag scope constants and validation helpers. |
| `backend/internal/sshutil/ssh_auth.go` | Purpose-aware SSH auth helpers plus legacy no-purpose compatibility shims. |
| `backend/internal/api/handlers/terminal_handler_test.go` | Terminal tests currently cover session-slot concurrency/limit behavior, not stream recording or command approval. |
| `web/src/components/web-terminal.test.tsx` | Frontend terminal tests currently cover step-up proof and terminal grant UX, not recording/replay or approval. |
| `backend/internal/sshutil/credential_provider_test.go` | Credential provider tests for encrypted local storage, inline/password credentials, managed-key scope denials, and secret-free errors. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend security specs for SSH key least privilege, credential audit events, credential access grants, and Settings risk summary. |
| `.trellis/spec/backend/error-handling.md` | Error-handling spec forbidding raw command output, SFTP/file content, exported payloads, stack details, and caller-selected doctor commands in client responses. |
| `.trellis/spec/frontend/a11y-guidelines.md` | Frontend a11y spec; explicitly exempts xterm.js terminal buffer screen-reader UX while requiring labeled wrapper mitigation. |
| `.trellis/spec/frontend/component-guidelines.md` | Frontend component spec for SSH key least-privilege scope UI fields/badges and sensitive-data exclusions. |

### Code Patterns

#### Terminal/session recording feasibility

- `backend/internal/api/handlers/terminal_handler.go:160-626` is the central terminal path. It reserves a slot, upgrades to WebSocket, reads a first auth message, validates admin JWT and step-up proof, enforces a terminal credential grant, loads the node with `Preload("SSHKey")`, resolves SSH auth for purpose `terminal`, dials SSH, requests a PTY, starts a shell, writes `terminal.open`/`terminal.close` audit events, then bridges streams.
- The stream interception points are centralized but raw:

```go
// backend/internal/api/handlers/terminal_handler.go:569-587
// SSH stdout → WebSocket（二进制帧）
go func() {
	defer cleanup()
	buf := make([]byte, 4096)
	for {
		n, readErr := sshStdout.Read(buf)
		if n > 0 {
			if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				log.Printf("debug: terminal: ssh stdout 读取结束")
			}
			return
		}
	}
}()
```

```go
// backend/internal/api/handlers/terminal_handler.go:595-623
// WebSocket → SSH stdin（主循环，阻塞直到连接关闭）
for {
	msgType, data, readErr := conn.ReadMessage()
	if readErr != nil {
		break
	}

	if msgType == websocket.TextMessage {
		// 尝试解析为控制消息（resize）
		var ctrl struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &ctrl) == nil && ctrl.Type == "resize" {
			var resizeMsg terminalResizeMessage
			if json.Unmarshal(data, &resizeMsg) == nil && resizeMsg.Cols > 0 && resizeMsg.Rows > 0 {
				_ = session.WindowChange(int(resizeMsg.Rows), int(resizeMsg.Cols))
				continue
			}
		}
		// 普通文本输入（键盘输入）
		if _, writeErr := sshStdin.Write(data); writeErr != nil {
			break
		}
	} else if msgType == websocket.BinaryMessage {
		if _, writeErr := sshStdin.Write(data); writeErr != nil {
			break
		}
	}
}
```

- `backend/internal/api/handlers/terminal_handler.go:508-520` records only safe terminal-open metadata (`stage`, `session_id`); `backend/internal/api/handlers/terminal_handler.go:552-565` records terminal-close metadata (`stage`, `session_id`, `duration_ms`). There is no persisted terminal stream.
- `backend/internal/model/models.go:421-423` documents `CredentialAuditEvent` as a domain evidence table that must never contain raw secrets, terminal streams, command output, or executor config. This makes the existing credential-audit table unsuitable for terminal/session recording storage.
- `backend/internal/credentialaudit/audit.go:263-281` denies audit metadata keys/values containing `output`, `stream`, `command`, `content`, or `payload`; session recording would need a separate storage contract rather than reuse `credential_audit_events.metadata`.
- `backend/internal/model/models.go:424-480` contains credential audit, grant, and task log models, but no `TerminalSession`, terminal frame, transcript, or replay table was found.
- `backend/internal/api/handlers/terminal_handler_test.go:15-150` tests terminal session slot limits and full responses only; no current backend test coverage for stream capture, transcript retention, replay ordering, or recording access control was found.
- `web/src/components/web-terminal.tsx:196-200` writes WebSocket binary/string messages directly to xterm; `web/src/components/web-terminal.tsx:245-248` sends `terminal.onData` directly as socket data. Frontend terminal input is keystroke/PTY data, not command-level semantic data.
- `web/src/components/web-terminal.tsx:286-294` labels the terminal wrapper with `role="region"`/`aria-label`; `.trellis/spec/frontend/a11y-guidelines.md:141-144` and `.trellis/spec/frontend/a11y-guidelines.md:167-174` explicitly treat xterm buffer screen-reader UX as out of scope.

#### Command approval feasibility

- Interactive terminal command approval is not equivalent to task/batch command approval. Terminal input is captured by xterm as raw data at `web/src/components/web-terminal.tsx:245-248` and forwarded to SSH stdin in `backend/internal/api/handlers/terminal_handler.go:595-623`. That stream can include keystrokes, paste bursts, control characters, cursor editing, shell prompts, curses programs, password prompts, and PTY escape behavior. No current command parser or shell semantic boundary was found.
- Terminal already has coarse operation-level approval/gating: `backend/internal/api/handlers/terminal_handler.go:202-226` validates primary token and step-up proof, and `backend/internal/api/handlers/terminal_handler.go:246-260` calls `EnforceTerminalCredentialGrantForWebSocket` before node load or SSH auth.
- `backend/internal/api/handlers/credential_access_grant.go:632-645` enforces terminal grants by `(user, role, action="terminal.open", purpose="terminal", node_id)` and writes safe grant-use audit evidence. This is an existing terminal-session gate, not per-command approval.
- The grant spec forbids storing commands or terminal streams: `.trellis/spec/backend/quality-guidelines.md:467-473` says grants store only bounded safe fields and must never store commands, terminal streams, command output, file contents, exported payloads, raw SQL, endpoint/proxy values, or host-sensitive strings.
- `backend/internal/api/handlers/credential_access_grant.go:492-503` sanitizes/rejects grant reasons before storage. `.trellis/spec/backend/quality-guidelines.md:484-485` explicitly covers reason text containing password/token/key/output/command/host/endpoint/proxy-shaped text.
- Non-interactive command tasks are structurally different. `backend/internal/model/models.go:296-324` persists `Task.Command`; `backend/internal/task/executor/command_executor.go:17-21` requires a non-empty command; `backend/internal/task/executor/command_executor.go:58-66` optionally wraps sudo and starts the command; `backend/internal/task/executor/command_executor.go:69-104` reads stdout/stderr and sends lines to task logs.
- `backend/internal/api/handlers/batch_handler.go:68-82` validates batch command text and blocks dangerous commands before execution; `backend/internal/api/handlers/batch_handler.go:113-122` enforces step-up and batch command grants before creating/running tasks; `backend/internal/api/handlers/batch_handler.go:168-181` writes credential audit metadata without raw command text.
- Task triggers already have route or handler gates: `backend/internal/api/router.go:291-297` shows manual task trigger uses `RequireStepUp` and `RequireTaskManualTriggerCredentialGrant`; `backend/internal/api/handlers/credential_access_grant.go:689-703` has enforcement helpers for batch task triggers and batch commands.
- `backend/internal/task/log_writer.go:82-117` persists `TaskLog.Message` rows for task output and publishes them over WS. This is an existing persisted task-output/log path, not a terminal recording path.

#### Smaller credential-provider follow-up feasibility

- `backend/internal/sshutil/credential_provider.go:13-24` defines a `CredentialProvider` seam, but `DefaultCredentialProvider()` always returns `LocalCredentialProvider{}`. No alternate provider implementation was found.
- `backend/internal/sshutil/credential_provider.go:26-60` resolves managed SSH keys, DB-loaded SSH keys, inline node private keys, or no key. Managed keys go through `ValidateSSHKeyScope`; inline node private keys return safe source metadata but are not governed by SSHKey scope metadata.
- `backend/internal/sshutil/credential_provider.go:62-94` builds password or key auth and returns `ResolvedCredential`; successful managed-key use updates `last_used_at` via `backend/internal/sshutil/ssh_auth.go:67-73`.
- `backend/internal/sshutil/scope.go:13-55` defines stable purpose strings including `terminal`, `task_command`, `batch_command`, `file_browser`, `snapshot`, `integrity_check`, `retention`, and `node_migration`; `backend/internal/sshutil/scope.go:129-154` enforces disabled, expiry, purpose, node-ID, and tag restrictions.
- `backend/internal/sshutil/ssh_auth.go:26-65` keeps no-purpose legacy helpers (`ResolveKeyContent`, `BuildSSHAuth`, `BuildSSHAuthWithKey`) as compatibility shims, while purpose-aware helpers (`ResolveKeyContentForPurpose`, `BuildSSHAuthForPurpose`, `BuildSSHAuthWithKeyForPurpose`) delegate to the provider seam.
- Search found legacy no-purpose call sites only at helper definitions and `executor.DialSSHForNode` wrapper; purpose-aware call sites are used across terminal, file browser, Docker volumes, node logs, probes, task executors, snapshot/diff, integrity checks, retention, node migration, SSH key tests, and node tests.
- `backend/internal/task/executor/ssh_connect.go:22-57` is a purpose-aware task SSH boundary and writes runtime credential audit events with safe metadata (`stage`, `auth_type`, optional latency). `backend/internal/task/runner.go:848-867` supplies task/run/node/policy correlation through `credentialaudit.WithRuntimeContext`.
- `backend/internal/sshutil/credential_provider_test.go:16-254` already covers local provider storage/encryption behavior, managed-key scope denials before use, missing managed key failure, and secret-free invalid-key errors. This makes provider-follow-up work comparatively localized and testable.

### External References

- Not requested and not used. This was an internal code/spec feasibility pass.

### Related Specs

- `.trellis/spec/backend/quality-guidelines.md:224-289` — SSH Key least-privilege scope: new managed-key SSH use sites must call purpose-aware helpers; legacy no-purpose helpers are compatibility shims; inline node credentials are credential-audited but not controlled by SSHKey scope metadata.
- `.trellis/spec/backend/quality-guidelines.md:380-447` — Credential-use audit events: audit identifies credential use without storing raw passwords, private keys, TOTP/JWT/recovery codes, decrypted executor config, terminal input/output, SFTP payloads, file contents, raw command output, Docker output/volume names, diagnostic evidence, exported config payloads, or full command text. Terminal tests must assert open/failure/close events do not include terminal input/output.
- `.trellis/spec/backend/quality-guidelines.md:451-519` — Credential access grants: grants are row-backed authorization records, terminal WebSocket grant check must happen before node load/credential resolution/SSH dial, and grants must never store commands, terminal streams, command output, file contents, raw SQL, endpoint/proxy values, or host-sensitive strings.
- `.trellis/spec/backend/error-handling.md:71-74` — API errors must not expose raw SQL, encryption details, SSH private keys, tokens, command output, SFTP/file content, Docker output, diagnostic evidence, exported config payloads, or stack-like details.
- `.trellis/spec/backend/error-handling.md:97-109` — Doctor diagnostics are read-only, must not accept arbitrary command strings, and must sanitize evidence rather than return full command output.
- `.trellis/spec/frontend/a11y-guidelines.md:141-144` and `.trellis/spec/frontend/a11y-guidelines.md:167-174` — xterm.js terminal buffer screen-reader UX is exempt/out of scope; wrapper labeling is the mitigation.
- `.trellis/spec/frontend/component-guidelines.md:164-195` — SSH key scope UI must expose compact metadata fields/badges and avoid private keys, passwords, audit metadata, raw endpoints, host enrichment, and one-click remediation actions.

### Feasibility Comparison

| Candidate P4 slice | Feasibility from current code | Main repo anchors | Size / coupling |
|---|---|---|---|
| Terminal/session recording | Technically possible because terminal I/O is centralized in `terminal_handler.go`, but it is not already modeled and cannot reuse credential-audit/grant metadata under current specs. | `terminal_handler.go:569-623`, `model.CredentialAuditEvent` comment, `credentialaudit` sanitizer, backend audit/grant specs. | Larger slice: likely needs new DB models/migrations for session/frame/transcript storage, retention/access controls, replay/list/export API, frontend replay/list UI, and tests proving recordings are separate from credential audit and protected from broad exposure. |
| Interactive terminal command approval | Harder than session recording as a faithful control because current terminal data is raw PTY/keystroke stream, not command-semantic events. Existing terminal gate is session-level (`terminal.open`) via step-up + JIT grant. | `web-terminal.tsx:245-248`, `terminal_handler.go:595-623`, `credential_access_grant.go:632-645`. | Large/cross-cutting: would need command reconstruction or a constrained shell/proxy model; must handle editing, paste, control programs, secrets typed into prompts, and avoid storing forbidden command/stream/output content in grants/audit. |
| Task/batch command approval | More feasible than terminal command approval because command text is structured before execution and trigger/create boundaries already have step-up/JIT grants. | `Task.Command`, `command_executor.go`, `batch_handler.go`, router task trigger routes, task log writer. | Medium: would still need a separate approval/state contract if distinct from current self-grant flow, and must reconcile that commands/output are already stored in task/task-log surfaces while forbidden in credential audit/grant metadata. |
| Credential-provider follow-up | Most localized among compared options. Provider seam, purpose constants, scope enforcement, runtime audit, and tests already exist; only local provider is implemented. | `credential_provider.go`, `scope.go`, `ssh_auth.go`, `ssh_connect.go`, provider tests, backend SSH scope spec. | Smaller slice: likely constrained to provider seam hardening/coverage, guardrails against no-purpose helper use, provider-source audit metadata consistency, or risk-summary/test coverage. |

## Caveats / Not Found

- No terminal recording/session replay model, migration, handler, API client, frontend route, or UI was found.
- No command-approval model or approval queue was found. Existing `credential_access_grants` are short-lived self-grants for operation boundaries, not reviewer approval records for command text.
- No command-semantic parser for interactive terminal input was found. Terminal I/O is raw xterm/PTY data.
- Existing task command execution persists command text (`Task.Command`) and task log output, while credential audit/grant specs forbid storing commands/output in audit/grant metadata. Any command-approval design would need to keep those domains separate.
- Existing terminal tests focus on slot limiting and grant/auth UI; they do not cover recording, replay, or per-command approval.
- External documentation/best-practice research was not performed because the requested task emphasized current repo code and specs.
