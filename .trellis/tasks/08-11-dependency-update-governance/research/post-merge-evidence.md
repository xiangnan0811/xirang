# Dependency Update Governance Post-Merge Evidence

## Task 5 CLI Preparation And Legacy Cleanup

- Recorded at: `2026-08-11T23:35:04+08:00`
- Repository: `xiangnan0811/xirang`
- Governance PR: [#416](https://github.com/xiangnan0811/xirang/pull/416)
- Governance PR head OID: `44a0e2bfae02b39a9ededb477363050bd86d718b`
- Governance merge OID: `a28206bdcfd97b43a659ca5a562ab60ab805566a`
- Evidence branch: `codex/chore-dependency-governance-evidence`
- Evidence branch base OID: `a28206bdcfd97b43a659ca5a562ab60ab805566a`
- Scope at capture time: CLI preparation, automatic configuration activation, exact legacy Dependabot cleanup, and grouped-PR reconciliation. No manual Web UI update check was triggered; once the exact automatic jobs existed, no repeat trigger was permitted.

## Pre-Cleanup Validation Blocker

- Detected at: `2026-08-11T23:36:34+08:00`
- Protected Release Please PR: [#386](https://github.com/xiangnan0811/xirang/pull/386), state `OPEN`, head `release-please--branches--main`
- Captured PR [#409](https://github.com/xiangnan0811/xirang/pull/409): expected state `OPEN`, actual state `CLOSED`
- Captured PR #409 author: `app/dependabot`
- Captured PR #409 head: `dependabot/npm_and_yarn/web/eslint-10.8.0`
- Captured PR #409 head OID: `2a6be7bf5aea14e66226b4eee0c440bb22605b11`
- Result: stopped before closing any PR or deleting any captured remote branch because the immutable all-OPEN precondition no longer held.

## Automatic Configuration Activation

- Inspected at: `2026-08-12T00:51:56+08:00`
- Browser authentication: signed in to GitHub at `https://github.com/xiangnan0811/xirang/network/updates`
- Governance merge time: `2026-08-11T10:35:57Z`
- GitHub `main` OID: `a28206bdcfd97b43a659ca5a562ab60ab805566a`
- Remote `.github/dependabot.yml` blob: `1c54e047809702d1cf7e50139822422a7764e2d6`, equal to the governance merge's config blob
- Active configuration: monthly at `03:00` in `Asia/Shanghai`; minor/patch-only groups `go-minor-patch`, `npm-production-minor-patch`, `npm-development-minor-patch`, and `actions-minor-patch`; open version-PR limits `1/2/1`
- Activation proof: all three check runs target governance merge `a28206bdcfd97b43a659ca5a562ab60ab805566a` and started at `2026-08-11T10:36:06Z` or `2026-08-11T10:36:07Z`, nine to ten seconds after the merge.

## Automatic Version-Update Jobs

| ecosystem | directory / UI card | configuration ID | job ID | type | start | completion | terminal check | result | Dependabot details | Actions logs | associated PRs |
|---|---|---:|---:|---|---|---|---|---|---|---|---|
| `gomod` | `/backend` / `backend/go.mod` | `34115050` | `1519658007` | `Version update` | `2026-08-11T10:36:06Z` | `2026-08-11T11:01:11Z` | Actions wrapper/run `31482925783`, job `93751739702`: `completed` / `success`; `mark_as_processed`: HTTP `204` | Approved grouped PR #420 was created; the Recent Jobs card separately reports `Errored` only after the configured one-open-PR limit was occupied and three later eligible updates could not join/open another PR | [job 1519658007](https://github.com/xiangnan0811/xirang/network/updates/1519658007) | [check 93751739702](https://github.com/xiangnan0811/xirang/actions/runs/31482925783/job/93751739702) | [#420](https://github.com/xiangnan0811/xirang/pull/420) |
| `npm` | `/web` / `web/package.json` | `34115054` | `1519658010` | `Version update` | `2026-08-11T10:36:07Z` | `2026-08-11T10:51:44Z` | `succeeded` in `15m37s` | `Update check processed`; `Finished` | [job 1519658010](https://github.com/xiangnan0811/xirang/network/updates/1519658010) | [check 93751740297](https://github.com/xiangnan0811/xirang/actions/runs/31482925908/job/93751740297) | [#418](https://github.com/xiangnan0811/xirang/pull/418), [#419](https://github.com/xiangnan0811/xirang/pull/419) |
| `github-actions` | `/` / `.github/workflows/ci.yml` | `34115055` | `1519658011` | `Version update` | `2026-08-11T10:36:06Z` | `2026-08-11T10:38:53Z` | `succeeded` in `2m47s` | `Update check processed`; `Finished` | [job 1519658011](https://github.com/xiangnan0811/xirang/network/updates/1519658011) | [check 93751739269](https://github.com/xiangnan0811/xirang/actions/runs/31482925733/job/93751739269) | [#417](https://github.com/xiangnan0811/xirang/pull/417) |

The browser job cards and details independently produced this raw mapping:

```text
1519658007 -> 420
1519658010 -> 418, 419
1519658011 -> 417
```

The numeric, unique, sorted browser-derived set is `417 418 419 420`.

### Go Capacity-Bound Acceptance

- The Go grouped update successfully created [#420](https://github.com/xiangnan0811/xirang/pull/420) containing `54` dependency updates.
- After #420 occupied the configured single open Go version-PR slot, later attempts for `github.com/go-openapi/swag`, `github.com/mailru/easyjson`, and `go.yaml.in/yaml/v2` stopped because `open-pull-requests-limit: 1` had been reached.
- The wrapper explicitly PATCHed `mark_as_processed` and received HTTP `204`; Actions wrapper/run `31482925783` and job `93751739702` completed with `success`. This wrapper success is distinct from the Recent Jobs UI card, which separately reports the configured-capacity error.
- The card error is accepted only as a capacity-bound split: #420 was created, the one allowed Go open-PR slot was then occupied, and three later eligible updates (`github.com/go-openapi/swag`, `github.com/mailru/easyjson`, `go.yaml.in/yaml/v2`) could not join that change set or open another PR. No repository or job failure was observed.
- Gate disposition: capacity-bound acceptance. The grouped version-update policy is active and behaved as configured; no rerun or Dependabot configuration change is required.

## Live Dependabot PR Reconciliation

The complete read-only query for current open `app/dependabot` pull requests returned:

| PR | state | author | approved group | exact head | head OID | URL |
|---:|---|---|---|---|---|---|
| 417 | `OPEN` | `app/dependabot` | `actions-minor-patch` | `dependabot/github_actions/actions-minor-patch-0cf509444f` | `79697bded665eab9101ae1015a9c5077d6b96391` | https://github.com/xiangnan0811/xirang/pull/417 |
| 418 | `OPEN` | `app/dependabot` | `npm-production-minor-patch` | `dependabot/npm_and_yarn/web/npm-production-minor-patch-e877850bfe` | `0313fa2545913220708c3d43cafa1877db195e91` | https://github.com/xiangnan0811/xirang/pull/418 |
| 419 | `OPEN` | `app/dependabot` | `npm-development-minor-patch` | `dependabot/npm_and_yarn/web/npm-development-minor-patch-1199ac11bd` | `22b6653fe3aff0a523ea791f1c6055a2c10e2c8d` | https://github.com/xiangnan0811/xirang/pull/419 |
| 420 | `OPEN` | `app/dependabot` | `go-minor-patch` | `dependabot/go_modules/backend/go-minor-patch-1869662807` | `905b7d102e08cdc19ceccec2a801f6983851d0e1` | https://github.com/xiangnan0811/xirang/pull/420 |

- Live PR count: `4`
- Duplicate live PR numbers: none
- Duplicate group identities: none
- Invalid state, author, head, OID, or URL rows: none
- Numeric, unique, sorted live set: `417 418 419 420`
- Numeric, unique, sorted browser-derived job set: `417 418 419 420`
- Exact set equality: `true`

## Automatic Legacy Cleanup

All 13 immutable legacy PRs remained exact-identity `app/dependabot` PRs, had `mergedAt: null`, and were automatically closed by `dependabot[bot]` during activation. GitHub's read-only exact-ref endpoint returned `404 Not Found` for every captured head, proving all 13 remote heads are absent.

| PR | exact head | full head OID | bot reason and timestamp | close timestamp | head-delete timestamp | mergedAt | remote head |
|---:|---|---|---|---|---|---|---|
| 409 | `dependabot/npm_and_yarn/web/eslint-10.8.0` | `2a6be7bf5aea14e66226b4eee0c440bb22605b11` | `Superseded by #419` at `2026-08-11T10:49:34Z` | `2026-08-11T10:49:35Z` | `2026-08-11T10:49:36Z` | `null` | absent |
| 408 | `dependabot/npm_and_yarn/web/react-i18next-17.0.11` | `d86e7a6ba2e9c1ec9237136e9e0c869de47ed60b` | `Superseded by #418` at `2026-08-11T10:40:48Z` | `2026-08-11T10:40:49Z` | `2026-08-11T10:40:51Z` | `null` | absent |
| 407 | `dependabot/npm_and_yarn/web/radix-ui/react-label-2.1.15` | `43bd7be57c4bf1d5a4b0b4f1ad1252b8349e9a9c` | `Superseded by #418` at `2026-08-11T10:40:46Z` | `2026-08-11T10:40:47Z` | `2026-08-11T10:40:48Z` | `null` | absent |
| 406 | `dependabot/npm_and_yarn/web/radix-ui/react-dialog-1.1.23` | `0c35e867171a8ea54e60c63217acee55ce4c73fd` | `Superseded by #418` at `2026-08-11T10:40:44Z` | `2026-08-11T10:40:45Z` | `2026-08-11T10:40:46Z` | `null` | absent |
| 405 | `dependabot/npm_and_yarn/web/radix-ui/react-separator-1.1.15` | `d658da52229b9f8c4e48450f6e4c98e07de23fe8` | `Superseded by #418` at `2026-08-11T10:40:42Z` | `2026-08-11T10:40:43Z` | `2026-08-11T10:40:44Z` | `null` | absent |
| 397 | `dependabot/go_modules/backend/github.com/aws/aws-sdk-go-v2/service/s3-1.105.2` | `c3113613c0495f872817e497ac381a2331b43bc8` | `Superseded by #420` at `2026-08-11T11:01:01Z` | `2026-08-11T11:01:02Z` | `2026-08-11T11:01:04Z` | `null` | absent |
| 396 | `dependabot/github_actions/actions/setup-node-7.0.0` | `678320f47281d9817e31d87113ddfd4a77ab0862` | `Superseded by #417` at `2026-08-11T10:38:29Z` | `2026-08-11T10:38:29Z` | `2026-08-11T10:38:31Z` | `null` | absent |
| 395 | `dependabot/github_actions/actions/setup-go-7.0.0` | `71268bd2a27b497cbbb40ece5c9a9ce0753a08d5` | `actions/setup-go is up-to-date now` at `2026-08-11T10:38:00Z` | `2026-08-11T10:38:02Z` | `2026-08-11T10:38:03Z` | `null` | absent |
| 380 | `dependabot/go_modules/backend/golang.org/x/net-0.57.0` | `ece9de99b00fa95d804ab21df00ab87fbb33f4b4` | `Superseded by #420` at `2026-08-11T11:00:59Z` | `2026-08-11T11:01:00Z` | `2026-08-11T11:01:01Z` | `null` | absent |
| 378 | `dependabot/go_modules/backend/golang.org/x/crypto-0.54.0` | `fb20f71dc1a175d4ba0cd0984068d750f2fc1090` | `Superseded by #420` at `2026-08-11T11:00:56Z` | `2026-08-11T11:00:57Z` | `2026-08-11T11:00:59Z` | `null` | absent |
| 377 | `dependabot/github_actions/docker/metadata-action-6.2.0` | `b7a885862f525f032457cc8e3546733464825257` | `Superseded by #417` at `2026-08-11T10:38:26Z` | `2026-08-11T10:38:27Z` | `2026-08-11T10:38:29Z` | `null` | absent |
| 376 | `dependabot/go_modules/backend/github.com/pkg/sftp-1.13.11` | `c4b76d42df02e132ed13703e5642d2492e855887` | `Superseded by #420` at `2026-08-11T11:00:54Z` | metadata `2026-08-11T11:00:54Z`; timeline close event `2026-08-11T11:00:55Z` | `2026-08-11T11:00:57Z` | `null` | absent |
| 375 | `dependabot/go_modules/backend/github.com/mattn/go-sqlite3-1.14.48` | `919d7d392b850814c394d72ab85abae7c4f48797` | `Superseded by #420` at `2026-08-11T11:00:51Z` | `2026-08-11T11:00:52Z` | `2026-08-11T11:00:54Z` | `null` | absent |

No PR close, comment, or remote-head deletion was performed manually by this Task 5 execution.

## Protected Release Please PR

- PR: [#386](https://github.com/xiangnan0811/xirang/pull/386)
- State: `OPEN`
- Head: `release-please--branches--main`
- Head OID: `0afd1fe566af828a9a5c322f17cb471765706e0b`
- Preservation check: passed after automatic job and live-set reconciliation

## Automatic Activation: No Manual Check

No `Check for updates` control was clicked. Governance merge `a28206bdcfd97b43a659ca5a562ab60ab805566a` automatically launched exactly one version-update job for each configured ecosystem/directory within nine to ten seconds. npm job `1519658010` and Actions job `1519658011` finished normally; Go job `1519658007` created approved grouped PR #420, PATCHed `mark_as_processed` with HTTP `204`, and its Actions wrapper/run `31482925783` / job `93751739702` completed `success`. The Go Recent Jobs UI card separately reports only the configured `open-pull-requests-limit: 1` capacity error after #420 occupied the slot and three later eligible updates could not join/open another PR. The independently queried live version set is exactly equal to the browser/job-derived set `417 418 419 420`. A manual repeat is forbidden once these exact jobs exist; any true failure, queued job, missing PR, failed wrapper/mark, unexpected error, duplicate trigger, or set mismatch remains blocking.

During Task 5, security settings were not changed, and no additional Dependabot job was triggered.

## Task 6 Security Update Enablement

- Recorded at: `2026-08-11T17:14:35Z`
- Vulnerability alerts enable request: `PUT repos/xiangnan0811/xirang/vulnerability-alerts` returned HTTP `204`.
- Automated security fixes enable request: `PUT repos/xiangnan0811/xirang/automated-security-fixes` returned HTTP `204`.
- Vulnerability alerts verification: `GET repos/xiangnan0811/xirang/vulnerability-alerts` returned HTTP `204`.
- Automated security fixes verification: `enabled=true`, `paused=false`.

### Transitional Snapshot During Asynchronous Generation

At `2026-08-11T17:14:35Z`, the immediate post-PUT queries still exposed only the four pre-existing grouped version-update PRs and returned an empty paginated open-alert result. GitHub had already created the two alert records at `17:14:23Z` and `17:14:24Z`, but the alert API and PR list had not yet converged; the two security PRs became visible at `17:15:29Z` and `17:15:37Z`. This early result is retained only as eventual-consistency transition evidence and is not the final Task 6 disposition.

### Final Complete Open Dependabot PR Snapshot

- Recorded at: `2026-08-11T17:20:48Z`
- Complete open `app/dependabot` PR count: `6`
- Preservation result: all four grouped version-update PRs and both security-fix PRs remain open; no bot PR was closed or modified during Task 6.

| PR | classification | exact head | head OID | created at | URL |
|---:|---|---|---|---|---|
| 417 | grouped version update | `dependabot/github_actions/actions-minor-patch-0cf509444f` | `79697bded665eab9101ae1015a9c5077d6b96391` | `2026-08-11T10:38:24Z` | https://github.com/xiangnan0811/xirang/pull/417 |
| 418 | grouped version update | `dependabot/npm_and_yarn/web/npm-production-minor-patch-e877850bfe` | `0313fa2545913220708c3d43cafa1877db195e91` | `2026-08-11T10:40:39Z` | https://github.com/xiangnan0811/xirang/pull/418 |
| 419 | grouped version update | `dependabot/npm_and_yarn/web/npm-development-minor-patch-1199ac11bd` | `22b6653fe3aff0a523ea791f1c6055a2c10e2c8d` | `2026-08-11T10:49:32Z` | https://github.com/xiangnan0811/xirang/pull/419 |
| 420 | grouped version update | `dependabot/go_modules/backend/go-minor-patch-1869662807` | `905b7d102e08cdc19ceccec2a801f6983851d0e1` | `2026-08-11T11:00:48Z` | https://github.com/xiangnan0811/xirang/pull/420 |
| 421 | security fix for alert #4 | `dependabot/npm_and_yarn/web/js-yaml-4.3.1` | `7adfb681096ca484ef59a69cbabc6dc047c7e11f` | `2026-08-11T17:15:29Z` | https://github.com/xiangnan0811/xirang/pull/421 |
| 422 | security fix for alert #5 | `dependabot/npm_and_yarn/web/multi-2181bdc769` | `036b2e54ea34f33ada66874c1f03407f99d35b9f` | `2026-08-11T17:15:37Z` | https://github.com/xiangnan0811/xirang/pull/422 |

### Final Open Dependabot Alert Review

The complete paginated `state=open` alert request returned two high-severity npm alerts. Both have open automated security-fix PRs that use same-major patch releases, so neither requires a manual major-version upgrade.

| alert | package | ecosystem | scope / relationship | vulnerable range | first patched version | automatic-fix PR status | generation run / job | manual-major disposition |
|---:|---|---|---|---|---|---|---|---|
| [#4](https://github.com/xiangnan0811/xirang/security/dependabot/4) | `js-yaml` | `npm` | development / transitive | `>= 4.0.0, < 4.3.1` | `4.3.1` | [#421](https://github.com/xiangnan0811/xirang/pull/421) `OPEN`; patch `4.3.0` -> `4.3.1` | [run 31516550732](https://github.com/xiangnan0811/xirang/actions/runs/31516550732) / [job 93863007220](https://github.com/xiangnan0811/xirang/actions/runs/31516550732/job/93863007220), `completed` / `success` | `NONE`; patch fix available |
| [#5](https://github.com/xiangnan0811/xirang/security/dependabot/5) | `react-router` | `npm` | runtime / transitive | `>= 7.12.0, < 7.18.2` | `7.18.2` | [#422](https://github.com/xiangnan0811/xirang/pull/422) `OPEN`; `react-router` and `react-router-dom` patch `7.18.1` -> `7.18.2` | [run 31516551083](https://github.com/xiangnan0811/xirang/actions/runs/31516551083) / [job 93863007818](https://github.com/xiangnan0811/xirang/actions/runs/31516551083/job/93863007818), `completed` / `success` | `NONE`; patch fix available |

- Open alert count: `2`
- Newly opened security-fix PR count: `2`
- Automatic-fix coverage: alert #4 -> PR #421; alert #5 -> PR #422
- Alert visibility: both alerts remain open pending merge of their preserved fix PRs.

## R7 Follow-Up Task Paths

```text
NONE
```

No R7 child task was created because each open alert has an automated same-major patch fix in an open PR; the complete review found no manual-major security work.

## Task 7 Governance-Merge Automation Evidence

- Governance PR: [#416](https://github.com/xiangnan0811/xirang/pull/416), merge SHA `a28206bdcfd97b43a659ca5a562ab60ab805566a`
- Main CI run: [31482921948](https://github.com/xiangnan0811/xirang/actions/runs/31482921948), `completed` / `success`
- Release Please run: [31482921949](https://github.com/xiangnan0811/xirang/actions/runs/31482921949), `completed` / `success`
- Publish Docker Images runs for the merge SHA: `0`
- Sync Docker Hub Description runs for the merge SHA: `0`
- Protected Release Please PR: [#386](https://github.com/xiangnan0811/xirang/pull/386), `OPEN`, head `release-please--branches--main`
