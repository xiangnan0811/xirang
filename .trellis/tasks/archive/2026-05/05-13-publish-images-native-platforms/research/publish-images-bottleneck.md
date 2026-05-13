# Publish Images Bottleneck Research

## Evidence

- Workflow run `25775900736` for release `v0.29.1` passed:
  - `Build amd64 image (load for scan)`
  - `Scan image for vulnerabilities`
- It then remained in `Build and push multi-arch image` for more than 90 minutes before cancellation.
- After cancellation, the job log showed the slow path was `linux/arm64` inside a single x64 runner:
  - `linux/arm64 supercronic-builder` completed after about 494 seconds.
  - `linux/arm64 backend-builder` `CGO_ENABLED=1` build completed after about 1239 seconds.
  - The run was still in `linux/arm64 web-builder RUN npm ci` at cancellation.
- Comparable successful Docker publish runs completed `Build and push multi-arch image` in about 28-29 minutes:
  - `v0.28.1`: `16:30:09Z` to `16:59:31Z`
  - `v0.28.2`: `02:45:28Z` to `03:13:41Z`
  - `v0.28.3`: `06:42:42Z` to `07:10:58Z`
- GitHub's hosted-runner reference lists `ubuntu-24.04-arm` as a standard arm64 Linux runner for public repositories.
- Docker's GitHub Actions multi-platform guide recommends distributing platform builds across multiple runners and creating the manifest with `docker buildx imagetools create` when single-runner multi-platform builds become too slow.

## Root Cause

The release workflow performs one multi-platform Docker build from `ubuntu-latest`:

```yaml
platforms: linux/amd64,linux/arm64
```

That forces arm64 build stages to run through QEMU on an x64 GitHub-hosted runner. In this repository the arm64 path is expensive because it includes Node dependency installation and Go builds, including CGO for SQLite. The publish failure is therefore a CI architecture issue, not a Docker Hub authentication or Go-version issue.

## Viable Fix

Split image publishing by platform:

- One matrix job builds and pushes each platform-specific image by digest.
- The arm64 matrix leg should run on a native arm64 GitHub-hosted runner if available.
- A final manifest job merges the digests into the public tags with `docker buildx imagetools create`.
- Scan each platform digest before publishing final tags.

This follows Docker's documented pattern for distributing multi-platform builds across multiple runners and avoids a single runner emulating the heavy arm64 build.

## Cancellation Rationale

Cancelling run `25775900736` was justified because:

- It exceeded recent successful multi-arch build duration by more than 3x.
- It had not pushed the public `v0.29.1` manifest yet.
- Letting it continue while preparing a newer fix could allow an older release run to update `latest` after a newer release.

## Verification Strategy

- Validate workflow syntax with local static inspection.
- Run existing repository checks:
  - `git diff --check`
  - `bash scripts/check-doc-freshness.sh`
  - `bash scripts/check-migration-utc-safety.sh`
- Open PR and monitor CI.
- After merge, monitor Release Please and the next release workflow.
- If no release is created by the CI-only change, manually dispatch `Publish Docker Images` for `v0.29.1` with `source_ref=v0.29.1`, then verify Docker Hub manifest tags.

## References

- GitHub Docs: <https://docs.github.com/actions/reference/runners/github-hosted-runners>
- Docker Docs: <https://docs.docker.com/build/ci/github-actions/multi-platform/>
