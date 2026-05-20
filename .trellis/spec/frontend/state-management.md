# State Management

> How state is managed in this project.

---

## Overview

State is managed with React Context, component state, URL/page-local state,
browser storage, and explicit API wrappers. There is no Redux, Zustand, React
Query, or SWR dependency in the current frontend.

Contexts under `web/src/context/` own shared application domains: auth, nodes,
policies, tasks, integrations, SSH keys, alerts, theme, command palette, and a
shared console provider. Page-local state stays inside the route page or a
page-specific state file.

---

## State Categories

- **Auth/session state**: `web/src/context/auth-context.tsx`, stored in
  `sessionStorage` with safe legacy migration from `localStorage`.
- **Theme/preferences**: `theme-context.tsx`, `use-user-preferences.ts`, and
  `use-persistent-state.ts`.
- **Domain collections**: context providers such as `nodes-context.tsx`,
  `tasks-context.tsx`, `policies-context.tsx`, and
  `integrations-context.tsx`.
- **Page view state**: filters, pagination, dialog visibility, draft form state,
  and view mode stay page-local or hook-local.
- **Derived UI state**: calculate from canonical domain state where feasible;
  avoid storing both source and derived values.
- **Streaming state**: logs and terminal-like streams use WebSocket helpers and
  dedicated hooks/components.

---

## When to Use Global State

Use context/global state when:

- Multiple routes or major panels need the same authenticated domain data.
- A mutation in one page must refresh or affect another part of the console.
- The value is truly app-wide, such as auth, theme, command palette, or shared
  console data.

Keep state local when:

- It only controls one dialog, table, filter panel, or form.
- It can be derived from props or an API result.
- It is temporary draft state. Use `use-dialog-draft.ts` for dialog drafts that
  follow the existing pattern.

---

## Server State

- Server state is fetched through typed API wrappers in `web/src/lib/api/`.
- Contexts expose refresh/mutation functions instead of hiding request side
  effects inside components.
- Paginated endpoints should use `unwrapPaginated` from `core.ts` so pages see
  `items`, `total`, `page`, and `pageSize`.
- Keep optimistic updates conservative. This admin console favors correctness
  and explicit refreshes over speculative UI updates for backup, SSH, security,
  and alerting operations.
- Map timestamps, numbers, enum-like status values, and optional fields in API
  wrapper functions before storing them.

---

## Scenario: Terminal credential grant prompt state

### 1. Scope / Trigger

- Trigger: adding or changing frontend flows that request temporary credential-use grants before opening a terminal or another high-risk credential operation.
- Applies to `web/src/components/web-terminal.tsx`, `web/src/lib/api/credential-access-grants-api.ts`, auth step-up context, terminal WebSocket close handling, and future grant prompts.

### 2. Signatures

- Terminal grant API call: `requestTerminalCredentialGrant(token, { nodeId, reason, ttlSeconds }, stepUpProof)`.
- WebSocket denial signal: close reason payload with machine-readable `code="CREDENTIAL_GRANT_REQUIRED"` and sanitized detail/status fields.
- Local UI state: dialog open/submitting/error, bounded reason draft, and a retry trigger for the current terminal connection.

### 3. Contracts

- Grant rows live on the backend. The frontend must not store grant IDs, grant material, reason text, or grant-required status in `localStorage` or `sessionStorage`.
- Keep terminal grant prompt state component-local unless a later feature adds a dedicated grant management surface.
- Reuse `ensureStepUpProof()` for the proof needed by the grant request. Do not ask for or store TOTP codes in the terminal component itself.
- A grant-required WebSocket close must not be treated as login/session expiry and must not unmount the terminal dialog before the reason prompt can complete.
- User-visible denial details must be sanitized and bounded before rendering. Do not display raw WebSocket close text if it can contain host, endpoint, SSH, command, or terminal-output details.
- Retrying terminal open after grant creation should use the existing connection flow and should clear only transient prompt state, not auth/session state.

### 4. Validation & Error Matrix

| Condition | Expected result |
|---|---|
| WebSocket closes with `CREDENTIAL_GRANT_REQUIRED` | Keep terminal UI mounted, open reason dialog, and show sanitized message. |
| Reason is empty or too long | Block submission locally with accessible validation text. |
| Grant request needs step-up | Call `ensureStepUpProof()` and send the proof through the API wrapper, not browser storage. |
| Grant request fails with sanitized backend error | Keep dialog open and render the sanitized message. |
| Grant request succeeds | Close dialog, clear reason draft, and retry terminal open once through normal connection code. |
| User cancels prompt | Clear local reason/error state; do not write browser storage or retry. |

### 5. Good/Base/Bad Cases

- Good: terminal receives a grant-required close, opens a labeled reason dialog, obtains a step-up proof from auth context, requests the grant, then reconnects without storing grant data.
- Base: an expired/revoked grant produces a safe message and lets the admin request a fresh grant.
- Bad: writing `grant_id`, `reason`, or `node_id` grant state to `localStorage`; calling `onDisconnect` immediately for a grant-required close so the parent dialog unmounts.

### 6. Tests Required

- Component tests must cover grant-required close handling, prompt opening, reason validation, successful grant request, retry, and no parent disconnect on grant-required close.
- Storage-safety tests must assert no grant ID/reason/status is written to `localStorage` or `sessionStorage`.
- Error tests must cover sanitized close details and failed grant request rendering.
- `npm run check` must cover typecheck, lint/a11y rules, Vitest, and build after changing the terminal prompt flow.

### 7. Wrong vs Correct

Wrong:

```ts
sessionStorage.setItem("terminal-grant-reason", reason);
onDisconnect?.();
```

Correct:

```ts
setGrantReasonDraft(reason);
setGrantPromptOpen(true);
// Keep the parent terminal dialog mounted until the grant flow resolves.
```

---

## Common Mistakes

- Do not promote every page filter or dialog flag into context.
- Do not persist sensitive session data in `localStorage`; current auth state
  uses `sessionStorage` and removes old localStorage keys.
- Do not duplicate shared filter/search state in multiple layers. Past filter
  sync issues caused lists to appear empty while stats still showed data.
- Do not store both raw backend values and mapped domain objects in the same
  state tree.
- Do not add a new state library without an explicit architectural decision.
