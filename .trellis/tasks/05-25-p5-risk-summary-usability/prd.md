# P5 risk-summary usability

## Goal

Continue the adjusted P5 small-team security roadmap with a low-blast-radius usability slice for the existing Settings security-risk summary. Make the report-only cards easier for personal and small-team operators to scan by surfacing aggregate context and ordering the most urgent risks first, without adding new backend risk semantics, remediation links, mutation actions, enforcement, enterprise policy, or deployment requirements.

## What I already know

* Xirang targets personal users and small teams, so this slice should improve clarity and prioritization without adding operational burden.
* Shipped P5 Settings posture cards already cover privileged users without TOTP, SSH host-key trust, audit-log integrity, deployment secrets, backup/restore recoverability, and admin recovery posture.
* `GET /api/v1/settings/security-risk-summary` already returns `generatedAt`, `summary.totalRisks`, `summary.categories`, and a typed item list with severity, count, and examples.
* The current Settings/System UI renders every card in backend order but does not display the aggregate summary or generated timestamp.
* The current UI already avoids remediation links/buttons inside the risk-summary section and must keep that report-only behavior.

## Requirements

* Keep the slice frontend-only unless implementation discovers a necessary mapper/test correction.
* Display existing aggregate summary data from the API in the Settings security-risk section.
* Sort risk cards by severity and count so critical/high-count findings are easier to find first; preserve deterministic ordering for equal priority.
* Keep `info`/zero-count cards visible so already-shipped posture categories remain discoverable.
* Preserve the existing card content model: backend-provided title/description/examples with i18n fallback.
* Keep the section read-only: no links, buttons, route changes, API mutations, remediation actions, or enforcement toggles.
* Do not expose any new raw evidence, host-sensitive strings, secrets, paths, endpoints, command output, logs, audit metadata, or environment values.

## MVP Scope

The MVP is a Settings/System UI refinement for the existing security-risk summary:

* Add a compact summary line that shows total risk count, category count, and generated timestamp when available.
* Render cards sorted by severity (`critical` before `warning` before `info`), then by descending count, then by original API order.
* Keep zero-count/info cards rendered after active findings with the existing “no examples” message.
* Add/update frontend tests for summary rendering, risk ordering, and continued absence of links/buttons.

## Acceptance Criteria

* [x] PRD records the small-team target, frontend-only MVP boundaries, and out-of-scope remediation/enforcement directions.
* [x] Implement/check context files reference only relevant PRD/spec/code files.
* [x] Trellis task is started before implementation.
* [x] Settings/System security-risk section displays existing aggregate summary data without adding backend fields or new API calls.
* [x] Risk cards are sorted by severity and count while preserving deterministic fallback ordering.
* [x] Existing shipped P5 cards remain visible, including info/zero-count cards.
* [x] Frontend tests prove summary rendering, ordering, and no remediation links/buttons.
* [x] `git diff --check` and `TMPDIR=/tmp npm --prefix web run check` pass before commit.
* [x] Trellis check review completes without unresolved findings.
* [ ] PR, CI, merge, release/Docker monitoring if triggered, and local main sync are completed.

## Definition of Done

* The usability slice is implemented, tested, checked, committed, merged, and released end-to-end.
* The feature remains report-only and compatible with existing deployments.
* No enterprise-only direction, new backend posture semantics, or mutation/remediation workflow is introduced.

## Technical Approach

1. Update `web/src/pages/settings-page.system.tsx` to derive a sorted risk item list and display aggregate metadata near the section header.
2. Add minimal i18n strings for summary metadata in English and Chinese.
3. Update `web/src/pages/settings-page.system.test.tsx` to assert sorted card order, aggregate summary text, and continued absence of actionable controls.
4. Avoid backend changes unless a mapper bug is discovered during implementation.

## Decision (ADR-lite)

**Context**: The P5 posture cards now cover several practical small-team risks, but the Settings section renders them as a flat list in backend order and does not show the summary values already returned by the API.

**Decision**: Implement a frontend-only readability pass that prioritizes high-severity/high-count cards and exposes existing aggregate metadata.

**Consequences**: Operators can scan the security summary more quickly without any new security control surface. Detailed remediation remains manual and outside this read-only Settings summary.

## Out of Scope

* Backend risk semantic changes, new risk codes, schema migrations, or new API routes.
* Remediation links, buttons, mutation actions, password/TOTP/recovery resets, backup/restore triggers, or SSH behavior changes.
* Enterprise policy UI, exception management, device trust governance, command approval engines, session recording, full Vault/KMS, SSH CA, WebAuthn/passkeys, or trusted-device flows.
* Hiding shipped P5 cards or removing info/zero-count posture categories.
* Returning or rendering raw secrets, usernames beyond existing safe examples, hostnames, IPs, file paths, endpoints, command output, logs, audit metadata, environment values, or raw errors.

## Technical Notes

* Branch: `security/p5-risk-summary-usability`.
* Task directory: `.trellis/tasks/05-25-p5-risk-summary-usability`.
* Primary frontend extension point: `web/src/pages/settings-page.system.tsx`.
* Existing Settings UI test: `web/src/pages/settings-page.system.test.tsx`.
* Existing API mapper: `web/src/lib/api/settings-api.ts`.
