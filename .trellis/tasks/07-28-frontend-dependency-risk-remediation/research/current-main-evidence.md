# Current-main dependency evidence

## Snapshot

- Inspected baseline: `main@ffa1ebf685af91ee7ebefb1a1535b65f8a870c6c`
- Runtime: Node `20.20.2`, npm `10.8.2`
- `web/package.json` SHA-256 prefix: `db0be2`
- `web/package-lock.json` SHA-256 prefix: `c59e50`
- Reconnaissance made no package, task or Git mutation.

## Audit Findings

The CI-equivalent audit reports four vulnerable package records (`1 moderate +
3 high`) and eight unique advisories:

| Package path | Advisories |
|---|---|
| `brace-expansion@1.1.14` and `5.0.6` | `GHSA-3jxr-9vmj-r5cp`, `GHSA-mh99-v99m-4gvg` |
| `postcss@8.5.15` | `GHSA-r28c-9q8g-f849` |
| `react-router@7.17.0` | `GHSA-wrjc-x8rr-h8h6`, `GHSA-h8fp-f39c-q6mh`, `GHSA-337j-9hxr-rhxg`, `GHSA-chx6-hx7r-mcp5`, `GHSA-qwww-vcr4-c8h2` |
| `react-router-dom@7.17.0` | inherited Router findings |

A non-force dry run proposed compatible in-range updates:

```text
brace-expansion 5.0.6 -> 5.0.8
brace-expansion 1.1.14 -> 1.1.16
postcss 8.5.15 -> 8.5.24
nanoid 3.3.12 -> 3.3.16
react-router 7.17.0 -> 7.18.1
react-router-dom 7.17.0 -> 7.18.1
```

The projected result retains only `GHSA-mh99-v99m-4gvg` on the legacy brace
path and RSC-only `GHSA-qwww-vcr4-c8h2` on Router 7.

## Compatibility

- `brace-expansion@5.0.8`: Node `20 || >=22`.
- `postcss@8.5.24`: Node `>=14`, compatible with current Vite/Tailwind ranges.
- Router 7.18.1: Node `>=20`, React/React DOM `>=18`.
- Router 8.3.0 requires Node `>=22.22` and React `>=19.2.7`; it is outside
  scope. No matching `react-router-dom@8.3.0` exists.
- Xirang uses a static Vite browser SPA with `createBrowserRouter`; it has no
  SSR, RSC, loaders or router actions.

## External State

- PR #383 updates PostCSS only and is behind current main. It overlaps the
  replacement lockfile and should be superseded after this task merges.
- PR #379 attempts ESLint 10, fails current peer resolution and is excluded.
- Dependabot alert API returned HTTP 403 because alerts are disabled or require
  admin scope. This does not block registry/audit evidence.
- `.github/workflows/ci.yml` runs the correct moderate-level audit with dev
  dependencies but marks it `continue-on-error: true`.

## Commands Used

```bash
env -u NODE_ENV npm audit --audit-level=moderate --json
npm ls brace-expansion postcss react-router react-router-dom --all
npm explain brace-expansion
env -u NODE_ENV npm audit fix --dry-run --json
npm view <candidate> engines peerDependencies dependencies --json
gh pr list --state open --author app/dependabot
```
