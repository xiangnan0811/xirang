# Trivy Platform Digest Scan Research

## Evidence From Failed Release Run

- Workflow run: `Publish Docker Images` `25780583570` for `v0.29.2`.
- The arm64 matrix job ran on `ubuntu-24.04-arm` and completed `Build image by digest` quickly.
- Failure occurred in `Scan image for vulnerabilities`.
- Trivy attempted to scan:
  `docker.io/<namespace>/xirang@sha256:551966b266f7d172fa68eb7bee48568688d05523d0c0f7a61a1b208515b6870b`
- Fatal error included:
  `no child with platform linux/amd64 in index ...@sha256:551966...`
- Inspecting that digest showed it was an OCI index with a `linux/arm64` manifest and an attestation manifest.

## Action Contract

The pinned action `aquasecurity/trivy-action@ed142fd0673e97e23eac54620cfb913e5ce36c25` has no `platform` input in `action.yaml`.

The action maps explicit inputs to `TRIVY_*` environment variables and then invokes the Trivy entrypoint. Trivy's image scan supports platform selection through CLI configuration, so setting `TRIVY_PLATFORM` on the step is the smallest change that reaches the underlying scanner without changing the pinned action.

## Options Considered

### A. Set `TRIVY_PLATFORM` on the existing action

- Minimal change.
- Preserves pinned action and cache behavior.
- Keeps the existing HIGH/CRITICAL gate and output format.
- Recommended.

### B. Replace the action with direct `trivy image --platform ...`

- More explicit at the shell command level.
- Requires installing or pinning the Trivy binary ourselves.
- Larger supply-chain and maintenance surface for this urgent release repair.

### C. Scan only the final manifest after publish

- Would avoid platform-specific digest scanning.
- Weakens the current safety contract because public tags would exist before the scan gate finishes.
- Not acceptable for this workflow.
