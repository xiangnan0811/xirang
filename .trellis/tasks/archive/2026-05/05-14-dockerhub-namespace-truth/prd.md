# Fix Docker Hub Namespace Truth

## Goal

Align the repository's current public deployment documentation and image defaults with the actual official Docker Hub namespace. The official namespace is `linnea7171`; there has never been a `xirang` Docker Hub namespace. Current docs and defaults that point users at `docker.io/xirang/xirang` are misleading and can make installs fail even though the release pipeline published successfully.

## What I Already Know

* The user confirmed the official Docker Hub namespace is `linnea7171`.
* GitHub repository variable `DOCKERHUB_NAMESPACE` is currently `linnea7171`.
* Release `v0.31.1` successfully published to `docker.io/linnea7171/xirang`.
* Docker Hub tag `docker.io/linnea7171/xirang:v0.31.1` resolves publicly and contains `linux/amd64` and `linux/arm64`.
* Current user-facing docs still mention `docker.io/xirang/xirang`.
* `docker-compose.prod.yml` defaults `IMAGE_NAMESPACE` to `xirang`, so a user following docs without overriding it will pull the wrong repository.
* `Makefile` defaults `DOCKER_NAMESPACE` to `xirang`, so manual maintainer Docker targets would also use the wrong namespace unless overridden.
* The publish workflow already uses `${{ vars.DOCKERHUB_NAMESPACE || secrets.DOCKERHUB_USERNAME }}` and should not be changed for this fix.

## Requirements

* Update current public and operating docs to name `docker.io/linnea7171/xirang` as the official image.
* Update deployment defaults so the default Compose and Makefile Docker paths point at `docker.io/linnea7171/xirang`.
* Preserve historical design docs under `docs/specs/` unless they actively mislead current users.
* Keep `DOCKERHUB_NAMESPACE` documented as configurable for maintainers, while naming `linnea7171` as the current official namespace.
* Keep release semantics unchanged: GitHub Release remains the version source of truth, Docker Hub remains the official public image source, and stable semver tags remain `vX.Y.Z`, `X.Y.Z`, and `latest`.

## Acceptance Criteria

* [x] `README.md` public install examples use `docker.io/linnea7171/xirang`.
* [x] `docs/deployment.md` uses `docker.io/linnea7171/xirang` for official deployment examples and local build examples.
* [x] `docs/env-vars.md` documents default `IMAGE_NAMESPACE=linnea7171`.
* [x] `docker-compose.prod.yml` defaults `IMAGE_NAMESPACE` to `linnea7171`.
* [x] `Makefile` Docker image targets default `DOCKER_NAMESPACE` to `linnea7171`.
* [x] Maintainer release docs state `DOCKERHUB_NAMESPACE=linnea7171` as the current official namespace.
* [x] Historical `docs/specs/*` references are left alone unless there is a direct current-user risk.
* [x] Searches for `docker.io/xirang/xirang` and `IMAGE_NAMESPACE:-xirang` return no current-doc/default references.
* [x] `bash scripts/check-doc-freshness.sh` and `git diff --check` pass.

## Out of Scope

* Changing Docker Hub repository ownership or creating a new Docker Hub namespace.
* Changing GitHub Actions release or image publishing semantics.
* Rewriting old dated planning/spec documents.
* Re-publishing an already published release image.

## Technical Notes

* Current release verification found `docker.io/linnea7171/xirang:v0.31.1` and `latest` share digest `sha256:dd2652ed040cbee5679820be09f0b4d3bc002650ba42376c4dbf0f0eeca96f65`.
* Initial scan found likely impacted files: `README.md`, `docs/deployment.md`, `docs/env-vars.md`, `docs/release-maintainers.md`, `docker-compose.prod.yml`, and `Makefile`.
* `.github/workflows/publish-images.yml`, `.github/workflows/deploy.yml`, and `.github/workflows/dockerhub-description.yml` already derive the namespace from `DOCKERHUB_NAMESPACE` or `DOCKERHUB_USERNAME`.
