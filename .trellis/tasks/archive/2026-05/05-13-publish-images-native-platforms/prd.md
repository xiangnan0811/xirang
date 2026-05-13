# Fix Docker Publish Multi-Arch Timeout

## Goal

Restore reliable official Docker image publishing after the `v0.29.1` release by removing the slow QEMU arm64 build path from `Publish Docker Images` while preserving the repository contract: stable releases publish `vX.Y.Z`, `X.Y.Z`, and `latest` to Docker Hub, with vulnerability scanning before public release and provenance attestation after manifest creation.

## What I Already Know

- `v0.29.0` Docker publish failed because `deploy/allinone/Dockerfile` used Go 1.26.2 while `backend/go.mod` requires Go 1.26.3.
- PR #151 fixed the Go builder version drift and Release Please produced `v0.29.1`.
- `v0.29.1` publish run `25775900736` passed the amd64 build and Trivy scan, then spent more than 90 minutes in `Build and push multi-arch image`.
- The stuck run was cancelled to avoid a delayed old release writing `latest` after a future fixed release.
- After cancellation, logs showed the multi-arch step built arm64 through QEMU:
  - arm64 supercronic build took about 8 minutes.
  - arm64 backend CGO build took about 20 minutes.
  - the run was still in arm64 `web-builder RUN npm ci` when cancelled.
- Recent successful publish runs completed the same multi-arch step in about 28-29 minutes.

## Requirements

- Avoid single-runner QEMU builds for the heavy arm64 path.
- Keep Docker Hub namespace/tag behavior unchanged.
- Keep release-event and manual `workflow_dispatch` entry points working.
- Keep pre-publish vulnerability blocking for the image that is published.
- Preserve provenance attestation for the final image.
- Prefer pinned actions or existing pinned actions; any newly introduced action must be pinned to a commit SHA.
- Do not change application runtime behavior or Dockerfile semantics unless required for the publish fix.

## Acceptance Criteria

- [ ] `Publish Docker Images` builds amd64 and arm64 images through native runner jobs or another bounded strategy that avoids the observed QEMU hang.
- [ ] Release tags still publish `vX.Y.Z`, `X.Y.Z`, and `latest`; manual rebuilds do not update `latest`.
- [ ] The final multi-platform manifest is pushed to `docker.io/${IMAGE_NAMESPACE}/xirang`.
- [ ] The workflow can be validated via `workflow_dispatch` for `v0.29.1` or a replacement release.
- [ ] Main CI, Release Please, and Docker publishing are monitored through final outcome.

## Out Of Scope

- Replacing `mattn/go-sqlite3` or removing CGO from the backend.
- Rewriting the Dockerfile into separate application build artifacts.
- Changing Docker Hub namespace, public image name, or semver release policy.

## Technical Notes

- Main workflow file: `.github/workflows/publish-images.yml`.
- Dockerfile: `deploy/allinone/Dockerfile`.
- Current bottleneck source: `docker/build-push-action` with `platforms: linux/amd64,linux/arm64` on `ubuntu-latest`.
- Existing docs mention local multi-arch build in `docs/deployment.md`; this task only needs docs changes if public maintainer instructions become stale.

## Research References

- `research/publish-images-bottleneck.md` — evidence from current failed/cancelled run and viable workflow strategy.
