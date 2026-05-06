# Docs / Deploy / Repo Hygiene Audit

Slice owner: docs/deploy/repo hygiene.

Scope covered:

- Public docs: `README.md`, `docs/**`, `deploy/nginx/README.md`,
  `backend/README_backend.md`
- Deploy/runtime config: `.env.deploy`, `backend/.env.production.example`,
  `docker-compose*.yml`, `Makefile`, `.dockerignore`, `.gitignore`
- GitHub process/release workflows: `.github/**`, `CONTRIBUTING.md`,
  `.githooks/pre-commit`
- Repo scripts in `scripts/**`

## Fixed Findings

### Medium — development Compose used the wrong Go toolchain

Evidence: `backend/go.mod`, `README.md`, and `deploy/allinone/Dockerfile` use
Go `1.26.2`, but `docker-compose.yml` still used `golang:1.26.1`.

Fix: updated `docker-compose.yml` to `golang:1.26.2`.

Verification:

- `docker compose -f docker-compose.yml config` passes.
- `rg "1\\.26\\.1|1\\.26\\.2"` shows only current intentional references and
  historical notes.

### Medium — Nginx deploy README described stale container ports and TLS mode

Evidence: `deploy/nginx/README.md` claimed the backend listened on `:8080` and
Nginx listened on `80/443`; current All-in-One runtime uses backend `:3000`,
Nginx `8080/8443`, and optional TLS detection in `entrypoint.sh`.

Fix: updated the deploy Nginx README to match the current All-in-One topology,
`BACKEND_UPSTREAM`, optional cert detection, and supercronic backup process.

Verification:

- Cross-checked against `deploy/allinone/Dockerfile`,
  `deploy/allinone/entrypoint.sh`, and Nginx templates.

### Medium — local Docker targets could accidentally publish `latest`

Evidence: `Makefile` tagged and pushed `latest` by default for `docker-build`,
`docker-push`, and `docker-buildx`, conflicting with the release contract that
`latest` is updated only by stable GitHub Release publishing.

Fix: changed Makefile Docker targets to tag only `DOCKER_TAG` by default and
require `TAG_LATEST=1` for intentional stable-release recovery. Updated
`docs/deployment.md` local build examples to avoid `latest`.

Verification:

- `make -n docker-build docker-push docker-buildx`
- `make -n TAG_LATEST=1 docker-build docker-push docker-buildx`

### Medium — production env examples used secret-shaped placeholder values

Evidence: `.env.deploy` and `backend/.env.production.example` contained
placeholder values for required production secrets. Some were known weak values
that runtime validation rejects, but they still looked like set credentials and
were noisy for secret scanners.

Fix: changed required production secret fields to blank fail-closed values and
added comments explaining they must be filled before production start. Kept
development examples unchanged because they are explicitly development-scoped.

Verification:

- `docker compose -f docker-compose.prod.yml config` passes when run from a
  temporary directory with `.env` copied from `.env.deploy`.
- Runtime guards in `config.Load()` and `bootstrap.SeedUsers()` reject blank
  `JWT_SECRET`, `DATA_ENCRYPTION_KEY`, and `ADMIN_INITIAL_PASSWORD` in
  production/first startup.

### Medium — doc freshness checks targeted ignored/local-only docs

Evidence: `scripts/check-doc-freshness.sh`, `.githooks/pre-commit`, and
`CONTRIBUTING.md` told contributors to update `CLAUDE.md`, which is ignored and
cannot satisfy repository review/CI contracts.

Fix: retargeted doc freshness rules to tracked docs/specs:
`backend/README_backend.md`, `docs/env-vars.md`, relevant `.trellis/spec/**`
guides, README/docs for public frontend entry changes, and release docs for
deploy changes. Added `scripts/check-doc-freshness.test.sh` and wired it into
CI.

Verification:

- `bash scripts/check-doc-freshness.sh`
- `bash scripts/check-doc-freshness.test.sh`
- `bash -n scripts/check-doc-freshness.sh scripts/check-doc-freshness.test.sh .githooks/pre-commit`

### Low — backend README migration version was stale

Evidence: latest paired SQLite/PostgreSQL migration is
`000057_service_uptime`, while `backend/README_backend.md` still claimed
`000030_task_run_progress`.

Fix: updated `backend/README_backend.md`.

Verification:

- `find backend/internal/database/migrations/{sqlite,postgres} -maxdepth 1 -type f | sort | tail`

### Low — release maintainer docs had stale manifest baseline text

Evidence: `.release-please-manifest.json` is at `0.28.0`, but
`docs/release-maintainers.md` claimed `0.19.0`.

Fix: updated the maintainer doc to point maintainers at the manifest/GitHub
Release as the current source and note the reviewed manifest value.

Verification:

- `sed -n '1,120p' .release-please-manifest.json`

### Low — remaining workflow action refs used moving tags

Evidence: most workflow actions were SHA pinned, but `golangci-lint-action@v7`,
`codecov-action@v5`, and `trivy-action@v0.36.0` still used tag refs.

Fix: pinned them to resolved SHAs and updated the Trivy release maintainer note.

Verification:

- `git ls-remote` for the action tag refs.
- Ruby YAML parse for all workflows.

### Low — generated/local deploy artifacts were not consistently ignored

Evidence: root `data/`, `backups/`, `certs/`, and local build outputs were
created or referenced by deploy docs/Compose but not consistently ignored or
excluded from Docker build context.

Fix: added root deploy/runtime artifacts to `.gitignore`; expanded
`.dockerignore` for local artifacts while preserving source paths such as
`web/src/data` by anchoring root-only patterns. Reviewer follow-up also ignored
repo-local `.tmp/`, which is used as a writable temp directory for Go/Vitest
checks when the system temp path is unavailable.

Verification:

- `git diff --check`
- Manual review of `.dockerignore` root-anchored entries.

## Deferred / Cross-Slice Findings

### Medium — backend database spec had stale latest migration

Evidence: `.trellis/spec/backend/database-guidelines.md` still states latest
migration is `000047_alert_deliveries_drop_error`; current latest migration is
`000057_service_uptime`.

Status: fixed during main-session integration.

Fix: updated `.trellis/spec/backend/database-guidelines.md` to point at
`000057_service_uptime`.

Verification:

```bash
find backend/internal/database/migrations/{sqlite,postgres} -maxdepth 1 -type f | sed 's#.*/##' | sort | tail
```

The command shows the paired `000057_service_uptime` migration files.

### Low — `actionlint`, `shellcheck`, `yq`, and PyYAML are unavailable locally

Evidence: `command -v actionlint`, `shellcheck`, and `yq` returned no path;
Python YAML parse failed with `No module named 'yaml'`.

Deferral reason: adding tools to the environment is outside this repo slice.
Used available checks instead: `bash -n`, Ruby YAML parser, Docker Compose
config rendering, and script self-tests.

## Verification Commands

Passed:

- `bash scripts/check-doc-freshness.sh`
- `bash scripts/check-doc-freshness.test.sh`
- `bash scripts/check-migration-utc-safety.sh`
- `bash scripts/check-migration-utc-safety.test.sh`
- `bash -n scripts/check-doc-freshness.sh scripts/check-doc-freshness.test.sh scripts/check-migration-utc-safety.sh scripts/check-migration-utc-safety.test.sh scripts/check-pr-title.sh scripts/backup-db.sh scripts/restore-db.sh scripts/smoke-e2e.sh scripts/e2e-alert-demo.sh .githooks/pre-commit`
- `ruby -e 'require "yaml"; ARGV.each { |f| YAML.load_file(f); puts "OK #{f}" }' .github/workflows/ci.yml .github/workflows/release-please.yml .github/workflows/publish-images.yml .github/workflows/deploy.yml .github/workflows/dockerhub-description.yml docker-compose.yml docker-compose.prod.yml`
- `docker compose -f docker-compose.yml config`
- `tmpdir="$(mktemp -d)"; cp docker-compose.prod.yml "$tmpdir/docker-compose.prod.yml"; cp .env.deploy "$tmpdir/.env"; (cd "$tmpdir" && docker compose -f docker-compose.prod.yml config)`
- `make -n docker-build docker-push docker-buildx`
- `make -n TAG_LATEST=1 docker-build docker-push docker-buildx`
- `git diff --check`

Unavailable:

- `actionlint`
- `shellcheck`
- `yq`
- `python -c 'import yaml'` / PyYAML
