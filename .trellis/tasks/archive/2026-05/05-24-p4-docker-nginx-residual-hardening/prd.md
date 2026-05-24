# P4 Docker nginx residual security hardening

## Goal

Reduce the remaining local Docker/Nginx runtime-log residual surface without changing the official deployment shape, API behavior, or UI behavior.

## Requirements

- Keep the official All-in-One runtime contract unchanged: image/port `10761`, `/logs` bind mount, `/api/v1/*` proxying, `/healthz`, and SPA fallback stay as-is.
- Keep Nginx access logging enabled at `/logs/nginx-access.log`, but avoid persisting request query strings in the logged request line.
- Keep Nginx error logging behavior unchanged for this slice because its format is not directly customizable like access logs and lowering severity would change observability.
- Keep Docker volume discovery API/UI behavior unchanged; volume `name`, `driver`, and `mountpoint` remain product data for authorized callers.
- Keep AppCredential/profile/policy hook behavior unchanged; broader rendered-hook redesign is out of scope for this slice.
- Update deployment documentation only where needed to keep operator-facing log behavior accurate.

## Acceptance Criteria

- [ ] `deploy/nginx/templates/default.conf.template` defines and uses an access-log format that logs path-only `$uri` instead of a full request URI with query args.
- [ ] `/logs/nginx-access.log` remains the configured access-log file.
- [ ] Nginx routes, proxy headers, WebSocket upgrade behavior, gzip, security headers, and error-log path/level remain unchanged.
- [ ] Deployment docs state that Nginx access logs omit query strings.
- [ ] Docker volume audit/API/profile/hook-template behavior is not changed.
- [ ] Nginx config syntax is validated with the template rendered as a conf.d file and writable `/logs`.
- [ ] `docker compose -f docker-compose.yml config` passes with the deployment env template copied to `.env` in a temporary directory.
- [ ] `bash scripts/check-doc-freshness.sh` and `git diff --check` pass.

## Definition of Done

- Trellis research, PRD, implement/check context, implementation, and check artifacts are complete.
- Required local verification commands pass with recorded output.
- Changes are committed on the work branch.
- Trellis task is archived and session journal recorded after the work commit.
- PR is created, CI is green, PR is merged, release automation completes, Docker publishing is verified if triggered, and local `main` is synced cleanly.

## Technical Approach

Use a custom Nginx `log_format` in `deploy/nginx/templates/default.conf.template` and change the access log directive to reference it. The format should preserve method, status, bytes, referer, user agent, forwarded-for, and timing context, but log `"$request_method $uri $server_protocol"` so query parameters are not persisted in `/logs/nginx-access.log`.

## Decision (ADR-lite)

**Context**: Backend structured logs and audit already avoid query strings, but the production Nginx template uses default access-log formatting. Default Nginx request lines include query strings, and the log file is persisted to the local host via `./logs:/logs`.

**Decision**: Harden only the official deployment Nginx access-log format in this slice. Do not change Docker volume API data, Docker audit behavior, policy hook responses, AppCredential profile rendering, or external GitHub Actions deploy logs.

**Consequences**: Operators keep the same log file and deployment behavior while query strings are no longer stored in access logs. Error-log request context remains a documented caveat for future review because changing severity or format would be less behavior-compatible.

## Out of Scope

- External Vault/KMS/SSH CA/session recording/command approval/WebAuthn/passkeys/device trust.
- Broad AppCredential rendered-hook storage/API redesign.
- Removing Docker volume names or mountpoints from authorized API/UI responses.
- Changing Nginx error-log level or routing.
- Changing Docker Compose logging options, image names, ports, TLS behavior, or bind mounts.
- External-provider workflow changes such as GitHub Actions deploy failure `docker logs` output.

## Research References

- [`research/docker-nginx-residual.md`](research/docker-nginx-residual.md) — Identifies Nginx access-log query minimization as the smallest local-only compatible slice.

## Technical Notes

- Deployment runtime spec requires fixed image/port, `/logs`, `/healthz`, and no Compose logging options.
- Logging spec forbids secrets, Docker command output/volume names, diagnostic evidence, executor config, and command output in logs.
- Documentation truth guide requires deployment docs to match source/config changes.
