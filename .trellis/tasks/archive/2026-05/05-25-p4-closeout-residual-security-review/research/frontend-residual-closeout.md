# Research: Frontend residual closeout

- **Query**: Research frontend/browser residual security surfaces for the active Trellis task `.trellis/tasks/05-25-p4-closeout-residual-security-review`; focus on browser storage, React state, config import/export UI, file preview/browser, terminal/log viewers, error/toast surfaces, downloaded/exported content handling, and API client behavior; identify whether raw secret material, file contents, command output, endpoints/hosts/paths, executor config, or step-up/grant material can remain in long-lived UI storage or unauthorized UI state; rank candidate slices by confidence and compatibility.
- **Scope**: internal frontend audit
- **Date**: 2026-05-25

## Scope Reviewed

| Surface | Files reviewed | Notes |
|---|---|---|
| Browser storage helpers | `web/src/hooks/use-persistent-state.ts`, `web/src/hooks/use-page-filters.ts`, `web/src/context/auth-context-provider.tsx`, `web/src/lib/step-up-storage.ts` | Reviewed `localStorage`/`sessionStorage` readers/writers, auth migration, and step-up proof persistence. |
| Persistent page filters | `web/src/pages/logs/logs-page.tsx`, `web/src/pages/logs-page.utils.tsx`, `web/src/pages/nodes-page.state.ts`, `web/src/pages/tasks-page.tsx`, `web/src/pages/policies-page.tsx`, `web/src/pages/ssh-keys-page.state.ts`, `web/src/pages/notifications/alert-center.tsx` | Reviewed whether filter keywords or selections can retain host/path/output fragments in `localStorage`. |
| Config import/export | `web/src/components/config-export-import.tsx`, `web/src/components/config-export-import.test.tsx`, `web/src/lib/api/config-api.ts` | Reviewed grant reason state, import parsing timing, non-persistent step-up use, and download Blob handling. |
| File browser/preview | `web/src/components/file-browser.tsx`, `web/src/components/file-preview-dialog.tsx`, `web/src/components/file-preview-dialog.test.tsx`, `web/src/lib/api/files-api.ts` | Reviewed previous file-content residual hardening and current request abort/clear behavior. |
| Terminal/log viewers | `web/src/components/web-terminal.tsx`, `web/src/components/web-terminal.test.tsx`, `web/src/hooks/use-live-logs.ts`, `web/src/lib/ws/logs-socket.ts`, `web/src/pages/logs/logs-viewer.tsx`, `web/src/pages/logs-page.log-entry.tsx`, `web/src/pages/logs/logs-history.tsx` | Reviewed WebSocket auth transport, terminal close-reason handling, grant reason storage, live-log state caps, and log rendering/export paths. |
| Task/batch output state | `web/src/components/task-run-history.tsx`, `web/src/components/task-run-detail.tsx`, `web/src/components/batch-command-dialog.tsx`, `web/src/components/batch-result-dialog.tsx` | Reviewed command input, step-up mode, task logs, batch logs, and close cleanup. |
| SSH key import/export | `web/src/components/ssh-key-batch-import-dialog.tsx`, `web/src/pages/ssh-keys-page.tsx`, `web/src/components/ssh-key-export-dialog.tsx`, `web/src/components/ssh-key-export-dialog.test.tsx` | Reviewed private-key import state, export preview, step-up use, and download Blob handling. |
| API client/error surfaces | `web/src/lib/api/core.ts`, typed API modules under `web/src/lib/api/` | Reviewed header-vs-URL transport, error object retention, 401 cleanup, and `AbortSignal` passthrough. |

## Findings

### 1. Browser storage helpers persist generic UI filters in `localStorage`

`usePersistentState` reads/writes arbitrary serialized values to `window.localStorage` and syncs across tabs (`web/src/hooks/use-persistent-state.ts:25-49`, `web/src/hooks/use-persistent-state.ts:51-73`). `usePageFilters` does the same for string filter fields (`web/src/hooks/use-page-filters.ts:24-38`, `web/src/hooks/use-page-filters.ts:63-76`). These helpers are not secret-aware; safety depends on each call site choosing non-sensitive values.

Security-relevant keyword call sites:

| Storage key | File / lines | Search corpus |
|---|---|---|
| `xirang.logs.keyword` | `web/src/pages/logs-page.utils.tsx:4-7`, `web/src/pages/logs/logs-page.tsx:112-115` | Log text is filtered against node name, task id, level, `log.message`, and error code (`web/src/pages/logs/logs-page.tsx:223-244`). |
| `xirang.policies.keyword` | `web/src/pages/policies-page.tsx:35`, `web/src/pages/policies-page.tsx:76-83` | Policy keyword is matched against `policy.name`, `policy.sourcePath`, `policy.targetPath`, and cron (`web/src/pages/policies-page.tsx:108-118`). |
| `xirang.nodes.keyword` | `web/src/pages/nodes-page.state.ts:23-30`, `web/src/pages/nodes-page.state.ts:55-68` | Node keyword is matched against node name, host, IP, username, tags, and status (`web/src/pages/nodes-page.state.ts:149-164`). |
| `xirang.tasks.keyword` | `web/src/pages/tasks-page.tsx:31-34`, `web/src/pages/tasks-page.tsx:78-88` | Task keyword is matched against task id/name, policy/node names, status, error code, and `lastError` (`web/src/pages/tasks-page.tsx:116-141`). |
| `xirang.sshkeys.keyword` | `web/src/pages/ssh-keys-page.state.ts:21-29`, `web/src/pages/ssh-keys-page.state.ts:53-67` | SSH key keyword is matched against key name, username, and fingerprint (`web/src/pages/ssh-keys-page.state.ts:145-153`). |
| `xirang.notifications.keyword` | `web/src/pages/notifications/alert-center.tsx:53-64` | Alert keyword is sent to the backend as the alert search term (`web/src/pages/notifications/alert-center.tsx:116-147`). |

Finding: user-entered free-text filter terms can be long-lived in `localStorage`. If an operator pastes a raw hostname, path, command-output fragment, secret-like string, or log fragment into a keyword field, that exact string persists across browser restarts. This is local-only and high-confidence. It is not evidence that backend-returned raw secrets are automatically persisted; the persisted value is the user's own filter text. Compatibility is moderate because current UX intentionally preserves page filters across sessions.

### 2. Auth token and step-up proof storage are session-scoped by design

Auth state is stored in `sessionStorage`, with legacy `localStorage` migration/removal for auth keys (`web/src/context/auth-context-provider.tsx:84-141`, `web/src/context/auth-context-provider.tsx:151-183`). Logout and 401 handling remove auth keys and step-up proof (`web/src/context/auth-context-provider.tsx:193-207`, `web/src/lib/api/core.ts:132-146`). Step-up proofs are stored under `xirang-step-up-proof` / `xirang-step-up-expires-at` in `sessionStorage` and cleared when missing/expired (`web/src/lib/step-up-storage.ts:1-67`).

`ensureStepUpProof` defaults to persistent cached proof behavior (`persist=true`, `reuseCached=true`) but allows one-shot callers to opt out (`web/src/context/auth-context-provider.tsx:224-252`). This is a deliberate auth/session behavior rather than a hidden `localStorage` leak. One-shot sensitive flows reviewed below explicitly pass `{ persist: false, reuseCached: false }` where implemented.

### 3. Config import/export avoids long-lived grant/payload storage

Config import/export is admin-only (`web/src/components/config-export-import.tsx:18-31`). Normal and sensitive config downloads use a Blob URL and revoke it after click (`web/src/components/config-export-import.tsx:32-40`). Import does not parse the selected file until grant submission; it parses once to validate before step-up/grant and again immediately before `importConfig` (`web/src/components/config-export-import.tsx:97-131`). Sensitive export and import both request non-persistent, non-reused step-up proofs (`web/src/components/config-export-import.tsx:119-153`). Grant reason and file input state are reset on cancel/success (`web/src/components/config-export-import.tsx:55-60`, `web/src/components/config-export-import.tsx:88-95`).

Current tests assert that grant reasons, payload markers, grant statuses, and grant IDs do not land in browser storage for import and sensitive export (`web/src/components/config-export-import.test.tsx:80-143`). Tests also assert invalid files do not request step-up/grant and that import payload parsing is deferred until grant submission (`web/src/components/config-export-import.test.tsx:203-246`).

Conclusion: no new config import/export frontend storage slice found beyond intended transient file read/download behavior.

### 4. File browser/preview prior residual appears fixed

Directory browsing aborts stale directory requests (`web/src/components/file-browser.tsx:65-86`), and preview fetches pass the current `AbortSignal` to the file-content loader (`web/src/components/file-browser.tsx:251-257`). `FilePreviewDialog` clears content/size/truncation/error/loaded path, aborts active requests, increments a request sequence, and ignores late resolution when closed/unmounted or when `filePath` changes (`web/src/components/file-preview-dialog.tsx:39-94`). Rendered content is gated by `loadedPath === filePath` (`web/src/components/file-preview-dialog.tsx:105-140`). Tests cover passing an `AbortSignal`, aborting and clearing on close, clearing before reopen, clearing while loading a different file, and ignoring late unmount resolution (`web/src/components/file-preview-dialog.test.tsx:16-229`).

Conclusion: the previously identified file-preview content-in-memory-after-close issue is already mitigated in current code.

### 5. Terminal and live logs avoid token-in-URL and bounded grant reason storage

Terminal WebSocket URLs contain `node_id` only (`web/src/components/web-terminal.tsx:185-195`); token and step-up proof are sent in the first WebSocket auth message. Logs WebSocket URLs include only `task_id` and `since_id`, and token is sent in an auth message on open (`web/src/lib/ws/logs-socket.ts:136-146`, `web/src/lib/ws/logs-socket.ts:205-219`). Central REST requests place bearer tokens and step-up proofs in headers, not normal query strings (`web/src/lib/api/core.ts:49-68`).

Terminal credential grant close reasons are bounded and character-filtered before display (`web/src/components/web-terminal.tsx:64-75`). Terminal grant reason is local component state, max 240 chars, and cleared when the grant dialog closes or succeeds (`web/src/components/web-terminal.tsx:87-140`, `web/src/components/web-terminal.tsx:295-340`). Tests assert grant-required close does not persist `CREDENTIAL_GRANT_REQUIRED` or the reason text to storage, and close-reason detail strips `<script>` (`web/src/components/web-terminal.test.tsx:144-195`).

Live log state is bounded: pending queue max 500, rendered live logs max 400, and disconnect cleanup clears pending logs and disconnects the socket (`web/src/hooks/use-live-logs.ts:10-12`, `web/src/hooks/use-live-logs.ts:29-58`, `web/src/hooks/use-live-logs.ts:98-107`). Log rendering uses React text nodes/highlighting rather than raw HTML (`web/src/pages/logs-page.utils.tsx:19-33`).

Residual note: `xirang.logs.keyword` can persist user-entered search text in `localStorage` (Finding 1), but log rows themselves are React state/backed by backend history APIs rather than browser storage.

### 6. Batch command/log output is mostly transient and already uses non-persistent step-up for command creation

Batch command input (`command`) is local state, reset on open, and command execution uses `useStepUpAction({ persist: false, reuseCached: false })` before requesting a batch-command credential grant and creating the task (`web/src/components/batch-command-dialog.tsx:31-49`, `web/src/components/batch-command-dialog.tsx:75-101`). Batch result logs are held in state/ref while the dialog is open and explicitly cleared when closed or when no batch is active (`web/src/components/batch-result-dialog.tsx:47-54`, `web/src/components/batch-result-dialog.tsx:80-98`). Expanded task log messages render through React text in `<pre>` (`web/src/components/batch-result-dialog.tsx:243-258`).

Conclusion: no new batch command/log frontend storage slice found.

### 7. SSH key export download handling is bounded, but default step-up caching remains general auth behavior

SSH key export preview renders only public-key/fingerprint/name fields (`web/src/components/ssh-key-export-dialog.tsx:34-71`). Export downloads fetch the file with bearer token and step-up proof, then create/revoke a Blob URL (`web/src/components/ssh-key-export-dialog.tsx:133-159`). The export dialog uses default `useStepUpAction()` (`web/src/components/ssh-key-export-dialog.tsx:96-101`), so it participates in the app-wide session step-up proof cache rather than one-shot non-persistent proof behavior. This mirrors general step-up semantics and is not by itself evidence of exported private key material being persisted in browser storage.

### 8. SSH key batch import keeps parsed private keys in mounted component state after close

`SSHKeyBatchImportDialog` parses uploaded JSON into `ParsedEntry[]`, and each `ParsedEntry` includes the raw `privateKey` string (`web/src/components/ssh-key-batch-import-dialog.tsx:25-39`, `web/src/components/ssh-key-batch-import-dialog.tsx:77-135`). The component stores parsed entries in React state (`web/src/components/ssh-key-batch-import-dialog.tsx:150-153`) and populates that state from a `FileReader.onload` callback (`web/src/components/ssh-key-batch-import-dialog.tsx:170-206`). The visible preview table intentionally renders only index, name, username, and status, not private-key text (`web/src/components/ssh-key-batch-import-dialog.tsx:367-417`).

However, state reset only occurs when `open` becomes true (`web/src/components/ssh-key-batch-import-dialog.tsx:155-163`). Cancel uses `onOpenChange(false)` without clearing `entries` (`web/src/components/ssh-key-batch-import-dialog.tsx:423-438`), and successful import sets phase to `done` then closes without clearing `entries` (`web/src/components/ssh-key-batch-import-dialog.tsx:237-265`). The parent renders `SSHKeyBatchImportDialog` unconditionally inside `Suspense`, passing `open={batchImportOpen}` (`web/src/pages/ssh-keys-page.tsx:339-347`), so the component can remain mounted after close with parsed private-key strings still in React state until the next open/unmount.

Conclusion: this is the strongest frontend residual candidate found. It is raw secret material retained in unauthorized/closed UI state, is local-only, and can be fixed without changing visible API/schema/deployment behavior because reopening already resets the dialog.

### 9. API client keeps backend error detail in memory but does not persist it

`ApiError` preserves backend message/detail in the thrown object (`web/src/lib/api/core.ts:7-17`, `web/src/lib/api/core.ts:148-162`). This can flow to `getErrorMessage`/toast surfaces, but the central client does not write errors to browser storage. Current frontend relies on backend sanitization contracts for response messages and details. Config and terminal tests demonstrate React text rendering for potentially hostile error/close text (`web/src/components/config-export-import.test.tsx:157-188`, `web/src/components/web-terminal.test.tsx:180-195`).

Conclusion: no standalone frontend-only error/toast persistence slice found; remaining exposure depends on backend envelope sanitization, which is being audited separately.

## Candidate Slices

| Rank | Candidate | Confidence | Compatibility | Evidence | Notes |
|---:|---|---|---|---|---|
| 1 | Clear SSH key batch import parsed entries/private keys on close/success/unmount and ignore late `FileReader` completion after close. | High | High | `ParsedEntry.privateKey` is stored in component state; reset only runs on `open=true`; parent keeps component mounted (`web/src/components/ssh-key-batch-import-dialog.tsx:25-39`, `web/src/components/ssh-key-batch-import-dialog.tsx:150-163`, `web/src/components/ssh-key-batch-import-dialog.tsx:423-438`, `web/src/pages/ssh-keys-page.tsx:339-347`). | Minimal local-only hardening. Visible behavior should remain compatible because closing already hides the dialog and reopening already resets state. Focused tests can prove stale private keys are not submitted or retained after close/late reader resolution. |
| 2 | Stop long-lived `localStorage` persistence for high-risk free-text keyword filters, especially logs/policies/nodes/tasks. | High that persistence exists; medium that it is a residual requiring this P4 slice | Medium | Generic storage helpers write all values to `localStorage`; keyword filters can match log messages, source/target paths, host/IP/usernames, and task errors (`web/src/hooks/use-persistent-state.ts:25-49`, `web/src/hooks/use-page-filters.ts:24-76`, call sites listed in Finding 1). | Would reduce long-lived browser-storage residuals for user-pasted host/path/output fragments. Compatibility is lower than rank 1 because cross-session filter persistence is existing UX. |
| 3 | Make terminal and SSH-key export step-up proofs explicitly non-persistent. | Medium | Low/Medium | Terminal and SSH export use default persistent step-up (`web/src/components/web-terminal.tsx:122-127`, `web/src/components/web-terminal.tsx:160-166`, `web/src/components/ssh-key-export-dialog.tsx:96-101`); one-shot config/batch flows opt out. | This changes app-wide step-up reuse semantics for interactive/export flows and is closer to policy behavior than residual cleanup. Not recommended as a frontend closeout slice without broader product decision. |
| 4 | Frontend error/toast detail scrubbing. | Low as frontend-only slice | Medium | API client preserves backend detail in memory (`web/src/lib/api/core.ts:7-17`, `web/src/lib/api/core.ts:148-162`). | Backend sanitization contracts are the primary control. Frontend-wide error rewriting risks hiding useful, already-sanitized messages and is not a high-confidence local-only residual slice. |

## Exclusions

- Architecture-level items intentionally excluded by the PRD: external Vault/KMS/secret brokers, SSH CA/device trust/WebAuthn, command approval/inspection, terminal/session recording, enterprise policy UI, and broad executor redesign.
- Browser download folders, OS temp files, browser HTTP cache internals, DevTools memory inspection, and user-saved exported files are outside this frontend local-state audit.
- Intentional, authorized transient data while a dialog is open is not counted as a residual by itself; the residual concern is data still retained after close, unmount, cancellation, late async completion, or long-lived storage.
- Backend response/log/audit sanitization is not re-audited here except where frontend behavior depends on it. Backend and credential/executor residuals are being researched separately.

## Recommendation

If the closeout chooses a frontend implementation slice, select **Candidate 1: SSH key batch import private-key state cleanup**. It is the highest-confidence frontend finding: raw private-key material is parsed into React state and can remain after the import dialog is closed because the parent keeps the component mounted and cleanup only runs on open. The bounded implementation shape is local-only and behavior-compatible: clear `entries`/phase/errors/file-reader effects on close and success, guard or abort late `FileReader` completion after close, and add focused tests proving old private-key entries cannot be submitted or observed after close/reopen/late load.

Do not select persistent keyword-filter changes ahead of Candidate 1 unless the main closeout decision explicitly prioritizes long-lived user-entered search text in `localStorage` over raw secret material in closed component state; that alternative is real but has a larger UX compatibility impact because cross-session filter persistence currently exists by design.
