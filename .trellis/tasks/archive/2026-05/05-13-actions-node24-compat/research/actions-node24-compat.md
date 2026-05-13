# GitHub Actions Node 24 Compatibility Research

Date: 2026-05-13

## External Runtime Schedule

GitHub's Node 20 deprecation notice says GitHub Actions runners now support Node 20 and Node 24, JavaScript actions begin defaulting to Node 24 on June 2, 2026, and the temporary Node 20 opt-out is only available until runner support for Node 20 is removed later in 2026.

Source: <https://github.blog/changelog/2025-09-19-deprecation-of-node-20-on-github-actions-runners/>

## Current Workflow Risk

Recent `Publish Docker Images` runs emitted Node 20 deprecation warnings for existing pinned actions including:

- `actions/checkout`
- `docker/setup-buildx-action`
- `docker/login-action`
- `docker/metadata-action`
- `docker/build-push-action`
- `actions/upload-artifact`
- `actions/download-artifact`
- `actions/attest-build-provenance`

The same repository also pins other JavaScript actions in CI, Release Please, Docker Hub description sync, and manual deploy workflows. Those should be reviewed together so the repo does not keep latent Node 20 action pins.

## Resolved Upgrade Targets

The following tags were checked with `gh release view`, action metadata was checked with `gh api repos/<repo>/contents/action.yml?ref=<tag>` or `action.yaml`, and pins were resolved with `git ls-remote`.

| Action | Target | Resolved SHA | Runtime / Decision |
|---|---:|---|---|
| `actions/checkout` | `v6.0.2` | `de0fac2e4500dabe0009e67214ff5f5447ce83dd` | `runs.using: node24` |
| `actions/setup-go` | `v6.4.0` | `4a3601121dd01d1626a1e23e37211e3254c1c06c` | `runs.using: node24` |
| `actions/setup-node` | `v6.4.0` | `48b55a011bda9f5d6aeb4c2d9c7362e8dae4041e` | `runs.using: node24` |
| `actions/upload-artifact` | `v7.0.1` | `043fb46d1a93c77aae656e7c1c64a875d1fc6a0a` | `runs.using: node24` |
| `actions/download-artifact` | `v8.0.1` | `3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c` | `runs.using: node24` |
| `actions/attest-build-provenance` | `v4.1.0` | `a2bbfa25375fe432b6a289bc6b6cd05ecd0c4c32` | composite wrapper; internal `actions/attest@59d89421...` uses `node24` |
| `docker/setup-buildx-action` | `v4.0.0` | `4d04d5d9486b7bd6fa91e7baf45bbb4f8b9deedd` | `runs.using: node24` |
| `docker/login-action` | `v4.1.0` | `4907a6ddec9925e35a0a9e82d7399ccc52663121` | `runs.using: node24` |
| `docker/metadata-action` | `v6.0.0` | `030e881283bb7a6894de51c315a6bfe6a94e05cf` | `runs.using: node24` |
| `docker/build-push-action` | `v7.1.0` | `bcafcacb16a39f128d818304e6c9c0c18556b85f` | `runs.using: node24` |
| `googleapis/release-please-action` | `v5.0.0` | `45996ed1f6d02564a971a2fa1b5860e934307cf7` | `runs.using: node24`; replaces archived `google-github-actions/release-please-action` |
| `codecov/codecov-action` | `v6.0.0` | `57e3a136b779b570ffcdbf80b3bdc90e7fab3de2` | composite; internal `actions/github-script@ed597411...` uses `node24`; release notes flag Node 24 support |
| `golangci/golangci-lint-action` | `v9.2.0` | `1e7e51e771db61008b38414a730f564565cf7c20` | `runs.using: node24` |
| `peter-evans/dockerhub-description` | `v5.0.0` | `1b9a80c056b620d92cedb9d9b5a223409c68ddfa` | `runs.using: node24` |
| `appleboy/ssh-action` | keep `v1.2.2` | `2ead5e36573f08b82fbfce1504f1a4b05a647c6f` | composite shell action; not a JavaScript runtime risk, so avoid an unrelated private deploy behavior change |
| `aquasecurity/trivy-action` | `v0.36.0` | `ed142fd0673e97e23eac54620cfb913e5ce36c25` | already current; composite action; dependencies checked: `actions/cache@v5.0.5` uses `node24`, `aquasecurity/setup-trivy@v0.2.6` is composite |

## Notes And Constraints

- Keep SHA pins instead of moving to floating version tags.
- Use accurate version comments next to pins so future maintainers can map SHA pins back to release tags.
- Do not introduce temporary runtime override environment variables; the durable fix is to update action versions.
- `release-please-action` migration is an owner/repo migration, not just a version bump, because `google-github-actions/release-please-action` is archived and its latest release still runs on Node 20.
- This task should use a `chore(ci): ...` commit/PR title so Release Please should not create a product release. Post-merge monitoring still needs to confirm that outcome.
