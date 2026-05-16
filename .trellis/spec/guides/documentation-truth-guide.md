# Documentation Truth Guide

> **Purpose**: Keep public, maintainer, and agent-facing docs aligned with the current repository while keeping process/archive material out of the public docs tree.

---

## Before Editing Documentation

Classify each document before changing it:

| Document type | How to treat it |
|---|---|
| `README.md` | Current public entry point. Keep it concise and link to detailed docs instead of duplicating manuals. |
| `docs/deployment.md`, `docs/env-vars.md`, `docs/admin/*.md` | Current user/operator documentation. Cross-check every command, path, env key, API path, and workflow claim. |
| `docs/maintainers/*.md`, `CONTRIBUTING.md`, `.github/*` templates | Maintainer/contributor documentation. Keep process details here, not in user-facing admin guides. |
| `CLAUDE.md`, `GEMINI.md`, `AGENTS.md`, `.trellis/spec/**` | Agent/development guidance. Keep current because future sessions rely on it, but do not expose it as product docs. |
| Process, planning, PRD, historical design, archive, and temporary docs | Do not keep in tracked public docs. Extract durable user/maintainer facts into the right current doc, then delete or keep only locally/untracked/private. |

---

## Truth Source Order

Use current repository evidence before changing claims:

1. Source code and config that implement the behavior.
2. Workflow files, manifests, Docker Compose files, Makefile targets, env examples, and scripts.
3. Current generated or maintained docs such as `CHANGELOG.md`.
4. Live external sources only when the claim is intentionally about current external state.

Do not invent roadmap, release, Docker Hub, or GitHub state. If an external fact is high-drift, either verify it during the task or word the doc so it does not pretend permanence.

### Docker Hub Namespace Claims

Do not infer the official Docker Hub namespace from the project name. For Xirang,
the current official namespace is `linnea7171`, so public install docs and local
Docker defaults should point at `docker.io/linnea7171/xirang` unless the
maintainer explicitly changes the repository variable and release docs.

---

## Open-source Documentation Rules

- README is a front door, not the manual.
- Detailed deployment, configuration, backup/recovery, monitoring, and security content belongs under `docs/`.
- User/operator docs should be organized by task and audience.
- Maintainer-only release/process details belong under `docs/maintainers/` or root governance files.
- Do not add new `docs/specs/`, archive, historical-plan, or process-doc directories to the tracked public repository.
- If a process document contains a durable fact, move that fact into the relevant current doc and delete the original process document.

---

## Verification Checklist

- [ ] `git diff --check` passes.
- [ ] `bash scripts/check-doc-freshness.sh` passes.
- [ ] Local markdown links resolve or are intentionally external.
- [ ] Version, image, release, migration, and deployment claims are backed by current repo files.
- [ ] No tracked public docs contain stale process/archive material.
- [ ] If code checks are skipped for a docs-only change, the reason is recorded.
