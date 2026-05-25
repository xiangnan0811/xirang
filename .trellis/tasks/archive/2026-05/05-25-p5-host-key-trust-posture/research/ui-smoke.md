# Research: UI Smoke — Settings/System Security Risk Summary

- **Query**: Use Chrome DevTools to perform a read-only browser smoke test for the running Vite frontend at `http://127.0.0.1:5173/`; confirm the Settings/System security risk summary area can load/render after the SSH host-key trust posture risk-code changes. Backend may not be running; record API failures. Do not modify source code. If auth blocks direct `/app/settings?tab=system` access, try normal app local auth state only; do not invent credentials or hit external services.
- **Scope**: internal runtime/browser smoke
- **Date**: 2026-05-25

## Findings

### Files Found

| File Path | Description |
|---|---|
| `web/src/pages/settings-page.system.tsx` | Runtime UI surface for the System tab and security risk summary card. |
| `web/src/pages/settings-page.tsx` | Settings tab routing; System tab is only visible for `role === "admin"`. |
| `web/src/lib/api/settings-api.ts` | Security risk summary API mapping and `SecurityRiskCode` union. |
| `web/src/i18n/locales/zh.ts` | Chinese localized title/description for `ssh_host_key_trust_posture`. |
| `web/src/i18n/locales/en.ts` | English localized title/description for `ssh_host_key_trust_posture`. |
| `backend/internal/api/handlers/settings_handler.go` | Backend adds `ssh_host_key_trust_posture` item to the security risk summary response. |

### Runtime Smoke Observations

- Vite frontend responded at `http://127.0.0.1:5173/` with HTTP 200 and served the React/Vite app.
- No matching project verifier skill was present under `.claude/skills/`; available project skills were Trellis workflow helpers only.
- No pre-existing Chrome DevTools endpoint was listening on `127.0.0.1:9222`, so an isolated headless Chrome was launched with DevTools on `127.0.0.1:9223`.
- Direct navigation through Chrome DevTools to `http://127.0.0.1:5173/app/settings?tab=system` loaded the app shell, then redirected to `http://127.0.0.1:5173/login`.
- The observed page body after redirect was the login page: `息壤控制台`, `欢迎登录`, `用户名`, `密码`, `登录控制台`.
- Browser storage in the isolated Chrome profile contained no app auth state:
  - `localStorage`: `xirang-accent-color`, `xirang-density`, `xirang-power-mode`
  - `sessionStorage`: empty
- Because there was no normal local auth state and no credentials were invented, the Settings/System tab did not render in this smoke session. The security risk summary area and the new `SSH 主机密钥信任姿态` / `SSH host-key trust posture` text were therefore not observed in the DOM.
- No uncaught runtime exceptions were captured during the direct route attempt. Console output was limited to Vite connection messages, React DevTools info, and a React Router future-flag warning.

### API / Backend Limitations Observed

- The unauthenticated login page attempted `GET /api/v1/auth/captcha` through the Vite origin and received HTTP 404 from `http://127.0.0.1:5173/api/v1/auth/captcha`.
- The app’s localhost direct fallback attempted `GET http://127.0.0.1:8080/api/v1/auth/captcha` in the browser and failed with `net::ERR_FAILED`.
- A no-credential browser fetch to `http://127.0.0.1:5173/api/v1/settings/security-risk-summary` returned HTTP 404 with body sample `404 page not found`.
- No authenticated request to `GET /api/v1/settings/security-risk-summary` was issued by the real Settings/System UI, because the route was blocked before the System tab mounted.

### Code Patterns

- `web/src/pages/settings-page.system.tsx:35-42` loads settings, log settings, and `apiClient.getSecurityRiskSummary(token)` together via `Promise.allSettled` when a token exists.
- `web/src/pages/settings-page.system.tsx:176-211` renders the security risk summary section when `securityRisk` is set, mapping `securityRisk.items` to cards keyed by `item.code` and translating titles/descriptions through `settings.system.securityRisk.items.${item.code}` with backend-provided defaults.
- `web/src/pages/settings-page.tsx:25-30` limits visible Settings tabs to all tabs only when `role === "admin"`; otherwise only personal/account tabs are visible.
- `web/src/pages/settings-page.tsx:138` mounts `<SystemTab />` only when the active tab is `system` and the current role is admin.
- `web/src/lib/api/settings-api.ts:27-38` includes `"ssh_host_key_trust_posture"` in `SecurityRiskCode`.
- `web/src/lib/api/settings-api.ts:88-104` normalizes `"ssh_host_key_trust_posture"` as a known risk code instead of falling back to `weak_security_defaults`.
- `backend/internal/api/handlers/settings_handler.go:235-250` adds `hostKeyTrustItem` into the security risk items list before `weak_security_defaults`.

### External References

- None. This was a local, read-only browser smoke test; no external services were used.

### Related Specs

- Not checked for this browser smoke. The task was limited to runtime observation of the running Vite frontend and local browser/API behavior.

## Caveats / Not Found

- **Not confirmed**: The Settings/System security risk summary area could not be observed rendering in the live browser session because the app redirected unauthenticated access to `/login` and the isolated Chrome profile had no stored auth state.
- **Not confirmed**: The new SSH host-key trust posture risk item could not be observed in the live DOM for the same auth limitation.
- **API limitation**: Local API calls needed for login/captcha or risk-summary data were not available through the tested browser path; observed failures were HTTP 404 on the Vite-origin API path and `net::ERR_FAILED` on the direct localhost fallback for captcha.
- **Source code was not modified** by the smoke test. Only this research note was written under the requested Trellis task research directory.
