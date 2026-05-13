# Fix Docker Go Builder Version Drift

## Problem

The `v0.29.0` release created successfully, but the `Publish Docker Images` workflow failed while building the all-in-one image. The backend module now requires `go 1.26.3`, while Docker builder images and the development Compose backend still reference Go 1.26.2.

## Goals

- Align Docker build and local Compose Go images with `backend/go.mod`.
- Keep current public README source-run requirements accurate.
- Verify that the replacement Docker image tag exists for the published platforms.
- Re-run relevant repository checks and complete the change through PR, CI, release, and Docker publish monitoring.

## Non-Goals

- No Go module dependency changes.
- No release workflow redesign.
- No broad historical documentation rewrite.

## Acceptance Criteria

- `deploy/allinone/Dockerfile` uses Go 1.26.3 for backend and supercronic builders.
- `docker-compose.yml` and `README.md` no longer advertise Go 1.26.2 as the current development baseline.
- Local validation covers documentation freshness and the Docker builder failure mode where practical.
- The PR merges only after required checks pass, followed by Release Please and Docker publish monitoring.
