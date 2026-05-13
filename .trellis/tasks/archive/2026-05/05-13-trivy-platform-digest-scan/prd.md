# Fix Trivy Platform Digest Scanning

## Goal

Repair the Docker image publish workflow after the native-platform split so each platform digest is scanned on its matching platform before the final multi-arch manifest is created. The `v0.29.2` publish run built the arm64 digest quickly on the native runner, but Trivy tried to resolve that arm64-only digest as linux/amd64 and failed before manifest publication.

## Requirements

- Keep the native amd64/arm64 matrix and digest-first publish model introduced by the previous workflow fix.
- Make the Trivy image scan platform-aware for each matrix row.
- Do not weaken the HIGH/CRITICAL vulnerability gate or publish final tags before scans pass.
- Preserve manual rebuild behavior: manual dispatch publishes only the requested version tags and must not update `latest`.
- Keep the change minimal so it can be validated quickly and released through the normal PR / Release Please / Docker publish flow.

## Acceptance Criteria

- [ ] `.github/workflows/publish-images.yml` passes YAML parsing and `actionlint`.
- [ ] The scan step passes the matrix platform to Trivy for both `linux/amd64` and `linux/arm64`.
- [ ] Local repository quality gates pass, including doc freshness and local CI parity.
- [ ] PR checks pass, the PR merges through `main`, and post-merge Release Please / Docker publish automation is monitored.
- [ ] If the new release Docker publish succeeds, decide whether to backfill the failed `v0.29.2` version tags via manual dispatch without updating `latest`.

## Definition of Done

- Workflow fix committed on `fix/trivy-platform-digest-scan`.
- Trellis task archived and journal updated after verification.
- PR created, CI monitored to green, and merged.
- Release Please, GitHub Release, and Docker publish outcome recorded.

## Technical Approach

Use the pinned `aquasecurity/trivy-action` as-is and provide `TRIVY_PLATFORM: ${{ matrix.platform }}` to the scan step. The pinned action has no explicit `platform` input, but it delegates to the Trivy CLI and respects Trivy environment variables. This keeps the action pin and existing vulnerability gate intact while fixing arm64 digest resolution.

## Decision (ADR-lite)

Context: After switching the Docker publish workflow to native platform runners, build time improved, but arm64 digest scanning failed because Trivy defaulted to linux/amd64 when reading an arm64-only OCI index digest.

Decision: Pass the matrix platform to Trivy via `TRIVY_PLATFORM` on the scan step instead of replacing the action or restructuring the build.

Consequences: The workflow remains small and pinned. Future upgrades should re-check whether `trivy-action` gains a first-class `platform` input, but the environment-variable path matches Trivy's CLI configuration model today.

## Out of Scope

- Reworking Docker image contents or Dockerfile build stages.
- Changing release semantics or Docker tag strategy.
- Changing vulnerability severity thresholds.

## Technical Notes

- `Publish Docker Images` run `25780583570` for `v0.29.2` failed in `Build & Scan (linux/arm64)` during `Scan image for vulnerabilities`.
- Trivy log showed `no child with platform linux/amd64 in index ...@sha256:551966...` for the arm64 digest.
- `docker buildx imagetools inspect` showed the digest was an OCI index containing an arm64 image plus attestation manifest, so the failure was platform resolution, not a vulnerability finding.
- Pinned `aquasecurity/trivy-action@ed142fd0673e97e23eac54620cfb913e5ce36c25` exposes no `platform` input in `action.yaml`; it maps supported inputs to `TRIVY_*` env vars and then runs `entrypoint.sh`.
