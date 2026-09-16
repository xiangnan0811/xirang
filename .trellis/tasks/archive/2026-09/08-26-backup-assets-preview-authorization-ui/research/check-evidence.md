# Independent Trellis Check Evidence

Date: 2026-08-26

## Findings

### Critical

- None.

### Important

- None.

### Minor

- None. No self-fix was required.

## Stage 1 — Spec and acceptance compliance

- Independently read the approved PRD, design, implementation plan, both JSONL
  manifests, root-cause research, session handoff, implementation evidence, and
  every spec selected by `check.jsonl`.
- Inspected the current diff and the live component/action path. The only product
  change is the `BackupAssetsWorkspace.canPreview` predicate. It now requires a
  runtime token, exact `admin` or `operator` role, Catalog `permissions.list`,
  available content, a selected recovery point, a selected preview product, and
  the exact renderer capability. `escaped_text` and `metadata_hex` require
  `openSequential`; every other current native renderer requires `openRange`.
- Catalog `permissions.preview` is no longer consulted at this native-preview UI
  boundary. The frontend mapper still preserves the server value, and the
  backend Catalog service still constructs `PermissionsDTO{List: true}`; neither
  layer fabricates `preview=true`.
- Positive workspace cases cover Admin and Operator with the producer-realistic
  list-only Catalog plus sequential capability, and Operator with a Range-native
  renderer plus Range capability. Clicking Load Preview passes the exact selected
  `BackupAsset` to the unchanged controller action.
- Negative workspace cases cover missing token, Viewer, unknown/missing role
  normalized to null, `list=false`, unavailable content, missing sequential
  capability, missing Range capability, and missing selected recovery point.
  Each case proves the action is absent and `loadPreview` is not called.
- The ticket action still calls the typed `issueTicket` API. Existing state
  selectors prove the ordinary ticket path, Admin-only secret-reveal retry,
  renewal without repeated step-up, Operator fail-closed behavior, and untyped
  denial fail-closed mapping.
- The router still guards the delivery-ticket POST with
  `RBAC(backup_assets:preview)`. The backend selector proves unauthenticated is
  rejected, Admin/Operator reach the feature gate, and Viewer/unknown roles are
  rejected before it.
- The product diff does not change backend/API/DTO/database/RBAC/ticket service,
  renderer selection, download, export, recover, archive, storage, logging, or
  URL behavior.

## Stage 2 — Code quality and regression risk

- The implementation is a five-line predicate correction in the existing owner
  component; no new abstraction, dependency, state, or API surface was added.
- Auth runtime remains explicit from `BackupsDataPage` to
  `BackupAssetsWorkspace`; the feature does not read token storage directly.
- `AssetPreview` remains presentation/action-only and receives the boolean gate;
  ticket issuance and safe error mapping remain controller-owned.
- The test fixture fixes `preview=false` explicitly while preserving the shared
  production-domain shape. Test assertions exercise user-visible button behavior
  and exact action dispatch rather than only testing a detached helper.
- Permanent frontend and cross-layer specs describe the independent Catalog-list
  and delivery-ticket authorization domains, exact capability matrix, fail-closed
  negatives, and required backend selector.
- No debug calls, direct fetch, browser-storage access, proof handling, content
  URL construction, Provider locator handling, or permission-field fabrication
  appears in the modified production source.

## Verification

Environment: Node `v24.18.0`, npm `11.16.0`.

### Focused and repeated frontend selectors

Command:

```sh
env -u NODE_ENV npx vitest run src/features/backup-assets/backup-assets-workspace.test.tsx -t "sequential native preview from a list-only Catalog|Range capability for a range-native renderer|hides native preview and does not request a ticket"
```

Result: three consecutive runs passed; each run reported 1 file passed, 11 tests
passed, and 32 skipped.

Command:

```sh
env -u NODE_ENV npx vitest run src/features/backup-assets/use-backup-assets-state.test.tsx -t "ordinary preview ticket|retries secret preview once|does not re-prompt TOTP on preview renew|does not request secret-reveal step-up|fails closed on an untyped preview denial"
```

Result: 1 file passed; 6 tests passed and 48 skipped.

### Backend final-authorization selector

Command from `backend/`:

```sh
go test ./internal/api -run '^TestBackupContentTicketRequiresPreviewPermissionBeforeFeatureGate$' -count=1 -v
```

Result: pass. Unauthenticated, Admin, Operator, Viewer, and unknown-role cases all
passed.

### Full frontend gate

Command from `web/`:

```sh
env -u NODE_ENV npm run check
```

Result: exit 0.

- Typecheck: passed.
- ESLint: passed.
- Vitest coverage suite: 181 files passed; 1531 tests passed.
- Production build: passed; Vite transformed 3222 modules.

### Privacy and source-boundary checks

- Fast source scan of the modified production workspace for
  `permissions.preview`, direct `fetch`, `localStorage`, `sessionStorage`,
  console calls, content URL/proof handling, and Provider locator handling:
  no matches.
- The protected-source `git diff --exit-code` check for `backend`, `web/src/lib`,
  `web/src/types`, `asset-preview.tsx`, `asset-preview-model.ts`, and
  `use-backup-assets-state.ts` exited 0, proving no changes in those
  backend/API/DTO/ticket/state/renderer surfaces.
- Exact product diff contains only the preview predicate replacement and the
  explicit preview-product presence check.
- `git diff --check`: exit 0.
- New task artifacts contain no trailing whitespace.

QUALITY_OK
