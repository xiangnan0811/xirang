# Documentation Truth Guide

> **Purpose**: Keep public and maintainer docs aligned with the current repo without erasing useful historical records.

---

## Before Editing Documentation

Classify each document before changing it:

| Document type | How to treat it |
|---|---|
| `README.md` | Current public entry point. Keep claims accurate for the current repo. |
| `docs/deployment.md`, `docs/env-vars.md`, `docs/release-maintainers.md` | Current operating documentation. Cross-check every command, path, env key, and workflow claim. |
| `docs/specs/<date>-*.md` | Historical design or implementation snapshot. Preserve history; add context notes and fix obvious errors or broken links only. |
| Gallery or reference docs | Verify local paths and current frontend structure before editing examples. |

---

## Truth Source Order

Use current repository evidence before changing claims:

1. Source code and config that implement the behavior.
2. Workflow files, manifests, Docker Compose files, Makefile targets, and env examples.
3. Current generated or maintained docs such as `CHANGELOG.md`.
4. Live external sources only when the claim is intentionally about current external state.

Do not invent roadmap, release, Docker Hub, or GitHub state. If an external fact is high-drift, either verify it during the task or word the doc so it does not pretend permanence.

---

## Historical Specs Convention

Dated files under `docs/specs/` are implementation history. Do not rewrite them wholesale to match today's code.

Allowed changes:

- Add a short historical note near the top.
- Fix broken local links.
- Correct a small statement that would actively mislead a reader, while saying it is a historical correction.

Avoid:

- Updating every old version number just because dependencies moved on.
- Replacing planned file layouts with current file layouts throughout the document.
- Turning old task checklists into current status reports.

---

## Verification Checklist

- [ ] `git diff --check` passes.
- [ ] `bash scripts/check-doc-freshness.sh` passes.
- [ ] Local markdown/html links resolve.
- [ ] Version, image, release, and deployment claims are backed by current repo files.
- [ ] If code checks are skipped for a docs-only change, the reason is recorded.
