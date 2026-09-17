# `v0.52.0` production acceptance remediation evidence

> Historical planning snapshot: this note captured the failed `v0.52.0`
> acceptance and the pre-implementation stop boundary. The user later explicitly
> approved the revised PRD/design/plan; current implementation and verification
> truth is recorded in `implement.md` and `research/implementation-evidence.md`.

## Boundary

This note records only sanitized product and source evidence. Production asset
names, paths, locators, content, credentials, tokens, TOTP, proofs, IDs, and raw
logs are intentionally omitted. The screenshots were inspected read-only.

The released image reached a healthy operational state: configured image identity
matched, container health was healthy with zero restarts, external/internal health
checks returned 200, schema was clean at 72, database integrity checks succeeded,
bounded logs contained no panic/fatal/migration/error-level events, active backup
runs were zero, and collectors remained zero. Infrastructure acceptance therefore
passed; the following product acceptance did not.

## Four reported production failures

1. A representative text/configuration asset returns the generic backup-content
   unavailable state instead of readable source text. The same state incorrectly
   displays optional Worker guidance even though core text preview is a no-Worker
   feature.
2. Admin secret-reveal TOTP prompts recur while switching files even immediately
   after a successful verification.
3. The source selector shows only one node and does not expose other known
   backup-bearing tasks/lineages, including interrupted work whose retained bytes
   still exist.
4. A nested directory has no direct Up action. The only escape is a standalone
   Root action, which is not an acceptable file-manager navigation model.

## Current-code findings

- `asset-preview.tsx` treats every `unsupported` or `temporarily_unavailable`
  preview error as a reason to show the optional Worker copy. This conflates core
  content delivery with derived ZIP/Office/OCR processing.
- `use-backup-assets-state.ts` requests `asset.secret_reveal` with
  `persist: false, reuseCached: false`, then keeps proof ownership per AssetRef and
  clears it whenever the content selection key changes.
- `auth/jwt.go` gives all step-up actions one five-minute lifetime. The existing
  central frontend step-up store already supports action-keyed proof+expiry in
  current-login `sessionStorage` and clears on login/logout/401/TOTP disablement.
- `catalog/file_source.go` begins with public Recovery Points only. The node/set
  projection cannot represent a managed task/repository lineage that has durable
  Provider data but no qualifying public point or complete Catalog.
- `catalog.EntryPage` contains only `items` and `next_cursor`. It validates the
  requested parent but does not return explicit current/parent directory context,
  so an empty directory cannot support truthful client-side Up navigation.
- `content/broker.go` collapses several source open/read/revalidation failures into
  one generic source-unavailable error. Existing unit tests prove the renderer
  with fake sources, but there is no full Repository Service/provider-adapter to
  Issue/grant/Serve text-preview test that reproduces the production path.

## Proven decisions and unresolved diagnosis

- The user selected a 45-minute, non-sliding, login-session proof for the exact
  `asset.secret_reveal` action. It is reusable across files, directories, versions,
  nodes, and page refresh and is cleared on identity/session/expiry/rejection
  boundaries. Other actions are unchanged.
- Source visibility is based on authorized durable retained-data evidence, not
  task runtime state and not task configuration alone.
- Parent navigation uses server-provided opaque parent identity; no path parsing is
  introduced. The standalone Root action is removed, while breadcrumb root and
  ancestor navigation remain.
- The exact production content-source failure stage is not proven from the UI.
  Implementation must first add a production-shaped synthetic integration RED and
  closed stage diagnostics, then fix the smallest demonstrated fault.

## Stop boundary

At the time this evidence was captured, no product code or delivery action was
authorized. The later explicit plan approval superseded that product-edit guard,
but it did not authorize commit, push, PR, CI, merge, release, NAS upgrade,
production acceptance, collectors, or node-log work. Collectors remain zero until
a new released image passes usable-content production acceptance, and node-log P1
still requires separate explicit approval.
