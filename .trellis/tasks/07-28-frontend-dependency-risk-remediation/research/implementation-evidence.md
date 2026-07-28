# Implementation evidence

## 2026-07-28 — Node 20 preflight blocker

### Scope snapshot

- Branch: `codex/frontend-dependency-risk-remediation`
- HEAD/main/origin/main: `ffa1ebf685af91ee7ebefb1a1535b65f8a870c6c`
- Task status: `in_progress`
- Staged paths: `0`
- Product-file diff: none
- `backend/xirang-server`: absent

Frozen-file SHA-256 values at the start and end of this investigation:

```text
db0be2d10de74a3c0489dff41944b2de2bf21b9d019a82661aefa88f1118e571  web/package.json
c59e50da9e648ecab0bfc435b14d32051f1a7d63bc9062275b07dd9cedcb9be6  web/package-lock.json
6517f635067d572fb4773dd27bfbd3c5996041331750582aacd22ba066f8e60b  .github/workflows/ci.yml
```

## 2026-07-28 — Immediate controller-directed retry

After a second controller confirmation that another shell could resolve the
official hosts, this shell retried the required official-release acquisition
without substituting Node 24. It remained reproducibly blocked before any
release metadata, checksum, or archive byte could be obtained:

```text
getent hosts nodejs.org api.github.com
exit: 2

gh api repos/nodejs/node/releases/tags/v20.20.2 --jq \
  '.assets[] | select(.name == "node-v20.20.2-linux-x64.tar.xz") | .browser_download_url'
exit: 1
error connecting to api.github.com
check your internet connection or https://githubstatus.com

curl --fail --show-error --location --connect-timeout 15 \
  --output /tmp/xirang-node20.lS1ZwX/node-v20.20.2-linux-x64.retry2.tar.xz \
  https://nodejs.org/dist/v20.20.2/node-v20.20.2-linux-x64.tar.xz
exit: 6
curl: (6) Could not resolve host: nodejs.org
```

The command failed before writing a verified archive; therefore no checksum,
extraction, Node 20 `npm ci`, audit RED, or lockfile operation was possible.
`find /home/murray/.nvm/.cache /home/murray/.cache /home/murray/Downloads /tmp
-type f \( -iname '*node*v20.20.2*' -o -iname
'node-v20.20.2-linux-x64.tar.xz' \) -print` also exited `1` with no output, so
there is no reusable local official archive in the checked shared caches.
`web/package-lock.json` remains unchanged and no advisory result is claimed.

### Required Node 20 runtime

The task requires a genuine Node 20 clean install and audit RED. The current
shell is Node `v24.18.0`/npm `11.16.0`; the bundled Codex runtime is Node
`v24.14.0`. Neither is valid Node 20 evidence.

Local runtime discovery found only:

```text
/home/murray/.nvm/versions/node/v22.18.0/bin/node
/home/murray/.nvm/versions/node/v22.23.1/bin/node
/home/murray/.nvm/versions/node/v24.18.0/bin/node
/home/murray/.npm/_npx/4bb4bc87b1b72b6c/node_modules/node/bin/node       v22.23.1
/home/murray/.npm/_npx/1838e33cf768caf6/node_modules/node/node_modules/node-linux-x64/bin/node  v26.5.0
```

There is no installed Node 20 executable in standard system, NVM, Codex or
npx cache paths.

### External runtime acquisition diagnostics

These are environment failures, not the required audit RED:

```text
curl --fail --silent --show-error --head --location \
  https://nodejs.org/dist/v20.20.2/node-v20.20.2-linux-x64.tar.xz
exit: 6
curl: (6) Could not resolve host: nodejs.org

gh api user
exit: 1
error connecting to api.github.com
check your internet connection or https://githubstatus.com

docker info --format '{{.ServerVersion}}'
exit: 1
permission denied while trying to connect to the docker API at unix:///var/run/docker.sock
```

`getent hosts nodejs.org` and `getent hosts github.com` returned no addresses.
The official CI mirror path cannot be queried through GitHub in this execution
environment, and Docker cannot supply the `node:20` image.

### Disposition

No `npm ci`, audit RED, lockfile refresh or product gate has been run under an
invalid runtime. `web/package-lock.json` remains byte-identical. The task is
blocked externally until a Node 20 runtime or a working CI-equivalent execution
environment is available. This evidence deliberately makes no claim that the
audit passed, was clean, or that any vulnerability was remediated.

### Narrow validation note

`task.py validate`, JSON/JSONL parsing, tracked and cached `git diff --check`,
and trailing-whitespace checks passed. A separate untracked-artifact newline
scan found that pre-existing `task.json` lacks a final newline. It is outside
this execution session's exclusive write scope, so it was not changed; this is
not represented as a passing all-artifact format gate.

## 2026-07-28 — Controller-supplied Node 20 recovery retry

The controller reported that its own shell could resolve `nodejs.org` and query
the Node GitHub release. This execution shell retried the prescribed recovery
path before treating the environment issue as blocked. No repository product
file changed during the retry.

Fresh preflight still found only Node `v24.18.0`/npm `11.16.0` in the shell and
NVM runtimes `v22.18.0`, `v22.23.1`, and `v24.18.0`; no local Node 20 runtime
was available. The disposable destination was
`/tmp/xirang-node20.lS1ZwX` and remained outside the repository.

```text
getent hosts nodejs.org api.github.com
exit: 2

gh api repos/nodejs/node/releases/tags/v20.20.2 --jq \
  '.assets[] | select(.name == "node-v20.20.2-linux-x64.tar.xz") | .browser_download_url'
exit: 1
error connecting to api.github.com
check your internet connection or https://githubstatus.com

curl --fail --show-error --location --retry 1 --connect-timeout 15 \
  --output /tmp/xirang-node20.lS1ZwX/node-v20.20.2-linux-x64.tar.xz \
  https://nodejs.org/dist/v20.20.2/node-v20.20.2-linux-x64.tar.xz
exit: 6
curl: (6) Could not resolve host: nodejs.org
```

Because DNS resolution fails in this execution shell, the official release
asset URL and `SHASUMS256.txt` cannot be retrieved, verified, or extracted.
This is an external environment blocker, not an audit RED. The required Node
20 clean install, strict audit, and lockfile refresh have not been run or
claimed. Frozen product hashes remain unchanged:

```text
db0be2d10de74a3c0489dff41944b2de2bf21b9d019a82661aefa88f1118e571  web/package.json
c59e50da9e648ecab0bfc435b14d32051f1a7d63bc9062275b07dd9cedcb9be6  web/package-lock.json
6517f635067d572fb4773dd27bfbd3c5996041331750582aacd22ba066f8e60b  .github/workflows/ci.yml
```

## 2026-07-28 — Superseding Node 20 RED/GREEN and compatibility gates

This section supersedes the earlier environment blocker. The prior attempts
remain above as historical evidence; they are not the current disposition.

### Official Node 20 runtime

The controller shell resolved the official release and downloaded
`node-v20.20.2-linux-x64.tar.xz` plus `SHASUMS256.txt` into a disposable
directory outside the repository. The expected and actual archive digest
matched:

```text
Node: v20.20.2
npm: 10.8.2
archive SHA-256: df770b2a6f130ed8627c9782c988fda9669fa23898329a61a871e32f965e007d
```

### Genuine audit RED

With the official Node 20 runtime first in `PATH` and `NODE_ENV` unset:

```text
env -u NODE_ENV npm --prefix web ci
exit: 0
added: 591 packages

env -u NODE_ENV npm --prefix web audit --audit-level=moderate --json
exit: 1
vulnerable package records: 4
unique GHSAs: 8
audit JSON SHA-256: 125a98f3b30450b07adf5811c46aaf214d6fe6a815c6f21a179998da4d30e998
```

The eight baseline identifiers were:

```text
GHSA-337j-9hxr-rhxg
GHSA-3jxr-9vmj-r5cp
GHSA-chx6-hx7r-mcp5
GHSA-h8fp-f39c-q6mh
GHSA-mh99-v99m-4gvg
GHSA-qwww-vcr4-c8h2
GHSA-r28c-9q8g-f849
GHSA-wrjc-x8rr-h8h6
```

This was an audit failure after a successful clean install, not an environment
or omitted-dev-dependency failure.

### Bounded lockfile refresh

The implementation used Node 20 npm's targeted lockfile operation:

```text
npm update brace-expansion postcss nanoid react-router react-router-dom \
  --package-lock-only --ignore-scripts --no-audit --no-fund
exit: 0
```

npm 10 removed unchanged `libc` metadata from eight optional platform packages
while rewriting the lockfile. Those unrelated metadata-only removals were
restored, and a structural comparison against `HEAD` proves that exactly six
package records changed:

```text
@typescript-eslint/typescript-estree/brace-expansion  5.0.6  -> 5.0.8
brace-expansion                                      1.1.14 -> 1.1.16
nanoid                                               3.3.12 -> 3.3.16
postcss                                              8.5.15 -> 8.5.24
react-router                                         7.17.0 -> 7.18.1
react-router-dom                                     7.17.0 -> 7.18.1
```

Final lockfile SHA-256 is
`8c4a890696278af3661f680a19b7aae9f8f8e81438b94b2f36986a9dfc7ffecc`.
The final diff is 21 insertions and 21 deletions in
`web/package-lock.json`; no package was added or removed.

### Audit GREEN-with-residuals

A second clean Node 20 install succeeded, then the same strict audit returned:

```text
env -u NODE_ENV npm --prefix web ci
exit: 0

env -u NODE_ENV npm --prefix web audit --audit-level=moderate --json
exit: 1
unique GHSAs: GHSA-mh99-v99m-4gvg, GHSA-qwww-vcr4-c8h2
audit JSON SHA-256: bf2e0fb59f997baf153942c2e12fa5b8d23b320890bebc175d52e312f62b3f0c
```

The audit is not clean and did not pass. npm reports eight propagated high
package records for the two remaining advisory chains. `npm ls` and
`npm explain` confirm the residual paths:

- ESLint / jsx-a11y -> `minimatch@3.1.5` ->
  `brace-expansion@1.1.16` (`GHSA-mh99-v99m-4gvg`); this is build/test tooling,
  remains unsuppressed, and must be revisited when an ESLint 9-compatible
  upstream path removes minimatch 3 or a separately planned ESLint migration
  lands.
- `react-router-dom@7.18.1` -> `react-router@7.18.1`
  (`GHSA-qwww-vcr4-c8h2`); the advisory is limited to unstable RSC APIs, while
  Xirang remains a static Vite browser SPA without RSC, SSR, loaders or actions.
  Revisit on a compatible Router 7 fix or any introduction of server/RSC Router
  execution.

The six remediable baseline GHSAs are absent. No new GHSA was accepted.

### Compatibility and repository gates

All commands used the final lockfile and Node 20 unless the backend-only or
Docker toolchain supplied its own declared runtime:

```text
env -u NODE_ENV npm --prefix web run check
exit: 0
typecheck: pass
ESLint: 0 errors, 1 configured existing warning
Vitest: 168 files / 1388 tests passed
build: pass

cd web && env -u NODE_ENV node scripts/check-bundle-budget.mjs
exit: 0
main JS: 499.00 / 500.00 KiB
main CSS: 104.94 / 105.00 KiB

env -u NODE_ENV make check
exit: 0
backend and frontend lint/test/coverage/build: pass
```

The first exact `make docker-build` attempt reached the Docker daemon but
failed before the first container command because this host cannot create veth
pairs for the default bridge network. A minimal reproduction showed
`docker run --network bridge` exit 125 with `operation not supported`, while
the same `node:20-alpine` image under `--network host` exited 0. Re-running the
same Dockerfile, context and tag with only `docker build --network host`
completed all 44 steps:

```text
docker build --network host -f deploy/allinone/Dockerfile \
  -t docker.io/linnea7171/xirang:v0.45.0-12-gffa1ebf-dirty .
exit: 0
image id: 6307a55c44f7
```

The Docker install also displayed eight high package records; it did not run or
recommendably apply `npm audit fix --force`, and this evidence does not call
the audit clean.

### Final local scope before review

```text
web/package.json SHA-256:
  db0be2d10de74a3c0489dff41944b2de2bf21b9d019a82661aefa88f1118e571
.github/workflows/ci.yml SHA-256:
  6517f635067d572fb4773dd27bfbd3c5996041331750582aacd22ba066f8e60b
staged paths: 0
backend/xirang-server: absent after generated-binary cleanup
task status: in_progress
```

No application, backend, deployment, CI or public documentation file changed.
The existing Trellis frontend quality spec was updated with the reusable npm
lockfile-metadata and advisory-counting guardrails discovered during this run;
this is a non-product code-spec update, not an expansion of dependency scope.
At the time of this local-scope snapshot, review, commit, PR, CI, merge,
Dependabot #383 closure, post-merge monitoring, archive and journal were still
pending. The delivery checkpoint below supersedes that historical state.

## 2026-07-28 — Delivery checkpoint

- Feature commit:
  `e63a28a8d9878577cc4b93bac2ac08dcb7217893`
- Pull request: <https://github.com/xiangnan0811/xirang/pull/401>
- PR state at recording: open, ready (not draft), required checks running
- Dependabot #383: still open; close only after PR #401 merges
- Dependabot #379: unchanged and excluded
- Merge, post-merge CI, Release Please, archive and journal: not executed
