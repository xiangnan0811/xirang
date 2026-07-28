# Frontend dependency risk remediation design

## 1. Boundary

The intended product diff is `web/package-lock.json` only. Resolution stays
inside the semver ranges already declared in `web/package.json`:

| Dependency path | Current | Compatible target | Purpose |
|---|---:|---:|---|
| `brace-expansion` modern path | 5.0.6 | 5.0.8 | remove current high advisories on the modern tree |
| `brace-expansion` legacy path | 1.1.14 | 1.1.16 | take latest compatible release; one broad advisory remains |
| `postcss` | 8.5.15 | 8.5.24 | remove direct/build-time high advisory |
| `nanoid` | 3.3.12 | 3.3.16 | refresh the compatible transitive build path |
| `react-router` | 7.17.0 | 7.18.1 | remove six Router 7 advisories except the RSC-only residual |
| `react-router-dom` | 7.17.0 | 7.18.1 | keep the browser binding paired with Router core |

The implementation must derive the exact final versions from the registry at
execution time and re-run the advisory assessment. Newer compatible fixed
versions are acceptable; downgrades and version-range widening are not.

## 2. Resolution Flow

1. Capture the clean Node 20 install and audit JSON as RED evidence.
2. Refresh only the named packages in the lockfile, without `--force`, peer
   bypass or manifest edits.
3. Inspect the lockfile graph with `npm ls` and `npm explain`; reject unrelated
   dependency churn.
4. Re-run the strict audit and compare GHSA identifiers, not only npm's package
   summary counts.
5. Run clean-install, frontend, bundle, full-project and Docker gates from the
   final lockfile.

The lockfile is the source of reproducible resolution truth. No application
code changes are required because Router 7.18's server-adapter origin behavior
does not affect Xirang's `createBrowserRouter` static SPA.

## 3. Residual Risk Ledger

### `GHSA-mh99-v99m-4gvg`

- Path: ESLint/jsx-a11y -> minimatch 3 -> `brace-expansion@1.x`.
- Applicability: build/test tooling; included in audit because development
  dependencies are part of the CI and Docker builder attack surface.
- Disposition: unresolved upstream, not suppressed.
- Owner: frontend dependency-risk task / repository maintainer.
- Revisit trigger: a compatible fixed `brace-expansion@1.x`, an ESLint 9
  compatible jsx-a11y release that removes minimatch 3, or a separately planned
  ESLint migration.

### `GHSA-qwww-vcr4-c8h2`

- Path: `react-router-dom@7.18.1` -> `react-router@7.18.1`.
- Applicability: advisory is limited to unstable RSC APIs; Xirang has no RSC,
  SSR, loaders or actions.
- Disposition: version match remains visible and is documented as not reachable
  in the current architecture.
- Owner: frontend dependency-risk task / repository maintainer.
- Revisit trigger: a compatible Router 7 fix, introduction of any RSC/server
  Router execution path, or an independently planned Node/React/Router major
  migration.

## 4. Dependabot And CI

PR #383 is a partial, now-stale PostCSS-only update. The replacement PR should
state that it supersedes #383; close #383 only after the replacement merges.
PR #379 remains excluded because its ESLint 10 graph cannot satisfy current
peer contracts.

The existing CI audit step remains unchanged. Its `continue-on-error` behavior
means the task must attach explicit audit evidence to the PR rather than infer
audit status from the frontend job conclusion.

## 5. Rollback

Rollback is a single-file reversion of `web/package-lock.json`. No database,
runtime state, API contract or deployment migration is involved. If any gate or
browser behavior regresses, restore the baseline lockfile and leave the task in
planning/in-progress with the audit risk still explicit.
