# Audit and Refresh Project Documentation

## Goal

Perform a comprehensive truth audit of the project documentation so public and maintainer-facing docs reflect the current repository state. Update outdated information, remove incorrect statements, fill in missing current information only when it can be verified from the repo or authoritative live project sources, and avoid inventing unsupported claims.

## What I Already Know

* User requested a full documentation review covering the root `README.md` and all documentation under `docs/`.
* Work is on branch `docs/current-state-audit`, created from a clean, up-to-date `main`.
* Documentation scope currently includes:
  * `README.md`
  * `docs/deployment.md`
  * `docs/env-vars.md`
  * `docs/release-maintainers.md`
  * `docs/specs/*.md`
  * `docs/ui-gallery/README.md`
  * `docs/ui-gallery/*.html` should be checked for stale prose or broken local references, but HTML gallery implementation changes are out of scope unless a document truth issue requires them.
* Current repo has no root `package.json`; frontend code lives under `web/` and backend under `backend/`.
* Current release manifest is `.release-please-manifest.json` with version `0.19.0`.
* Recent history includes `chore(main): release 0.19.0 (#98)` and later backend/docs/process commits.
* Existing release governance states: GitHub Release is the public version source of truth, Docker Hub is the official public image source, and stable semver tags use `vX.Y.Z`.
* Prior memory for this repo flags high-value truth surfaces: release workflows, `release-please` config, `.release-please-manifest.json`, `README.md`, deployment/env/release docs, Docker Compose files, environment examples, and version handler code.

## Requirements

* Audit `README.md` and all markdown files under `docs/` for claims that are stale, wrong, incomplete, or unsupported by the current repo.
* Preserve current language style per document unless correcting the document requires targeted wording changes. Do not use this task as a broad translation pass.
* Treat current repo files as the primary source of truth for local architecture, commands, paths, environment variables, workflows, and feature availability.
* Treat high-drift external facts, such as latest GitHub release or Docker Hub state, as verifiable-at-time-of-work facts. If included, they must either be verified during this task or worded without pretending permanence.
* Remove or soften unsupported promises rather than filling gaps with guesses.
* Treat dated `docs/specs/*` design and implementation plan files as historical snapshots. Preserve their historical record value; only fix obvious factual errors, broken links, or wording that would mislead readers into treating old plans as current operating docs.
* Update cross-links and examples when paths, commands, versions, image tags, or workflow names have changed.
* Avoid changing runtime code unless documentation verification exposes a tiny doc-adjacent inconsistency that cannot be resolved safely in docs alone.

## Acceptance Criteria

* [ ] `README.md` accurately describes current project purpose, feature set, deployment path, source-run commands, release/update path, and related docs.
* [ ] `docs/deployment.md` matches current Docker Compose, Makefile, release workflow, image naming, version check behavior, and operational commands.
* [ ] `docs/env-vars.md` matches current backend/frontend/deployment environment files and code reads; removed variables are not presented as active.
* [ ] `docs/release-maintainers.md` matches current GitHub Actions workflows, release-please config, Docker Hub publishing behavior, and branch workflow.
* [ ] `docs/specs/*.md` are handled as historical snapshots: keep them intact where accurate for their time, add/adjust historical context where needed, and fix obvious errors or broken links.
* [ ] `docs/ui-gallery/README.md` and gallery prose/references do not contradict current frontend structure.
* [ ] Any new factual claim is traceable to a repo file, workflow, manifest, code path, or explicitly verified live project source.
* [ ] No unsupported roadmap, release, deployment, or feature claims are introduced.
* [ ] Documentation links are checked for local path correctness.
* [ ] Relevant doc freshness checks, lint, tests, or builds are run where practical; any skipped verification is recorded with a reason.

## Definition of Done

* Documentation changes are implemented on the work branch.
* Trellis implement/check context is curated before execution.
* Final verification includes at least repository doc checks plus targeted backend/frontend checks if docs were changed based on code behavior.
* Spec-update review is performed; if no `.trellis/spec/` update is needed, that conclusion is recorded.
* Work changes are committed only after presenting the Trellis commit plan for confirmation.

## Decisions

* Dated `docs/specs/*` files are historical snapshots, not current docs to rewrite wholesale. This task should preserve them and apply only targeted corrections/context where needed.

## Technical Approach

1. Inventory documentation claims and classify each as stable repo fact, semi-stable workflow/process fact, high-drift external fact, or historical artifact.
2. Cross-check documentation against current repo evidence: manifests, Makefile, Docker Compose files, GitHub Actions workflows, env examples, backend config/env reads, API/version behavior, and frontend package/scripts.
3. Update docs with conservative wording: current facts where verified, historical labels where appropriate, and no invented future plans.
4. Run doc/local-link checks and targeted project checks.

## Out of Scope

* Large runtime feature changes.
* Broad translation of all docs.
* Creating new public guarantees that are not already implemented or intentionally documented elsewhere.
* Changing release/deploy infrastructure unless a tiny doc-adjacent workflow metadata correction is necessary and verified.

## Technical Notes

* Project docs are partly public-user-facing and partly maintainer-facing; this audit should separate those audiences rather than flattening all details into the README.
* Existing `.trellis/spec/backend/index.md` and `.trellis/spec/frontend/index.md` say documentation in those spec directories should be English. The current public project docs are mostly Chinese; preserve existing public-doc language unless the user explicitly requests otherwise.
* The first repo listing command accidentally searched nonexistent root `package.json` and `frontend`; current evidence shows frontend is `web/` and `web/package.json` exists.
* Memory-derived release governance clues must be re-verified against current repo files before editing docs.
