# Implementation Evidence

Date: 2026-08-26

## Scope

- Changed only the backup-asset workspace native-preview eligibility boundary,
  its behavioral tests, and the selected permanent frontend/cross-layer specs.
- Catalog DTOs/mappers, database code, RBAC, the delivery-ticket service,
  renderer selection, and download/export/recover/archive paths were not changed.
- No production credentials, asset identifiers, storage locators, backup paths,
  filenames, or preview content were used or recorded.

## Behavioral RED

The worktree initially had no installed web dependencies.
`env -u NODE_ENV npm ci` completed successfully (594 packages, audit reported 0
vulnerabilities); the earlier runner-startup failure was not counted as a
product RED.

Before any production-code edit, the producer-realistic fixture used Catalog
`list=true`, Catalog `preview=false`, content available, a selected recovery
point, the exact sequential capability, authenticated Admin/Operator runtimes,
and an exact native-preview renderer.

Command:

```sh
cd web && env -u NODE_ENV npx vitest run src/features/backup-assets/backup-assets-workspace.test.tsx -t "sequential native preview from a list-only Catalog"
```

Result: exit 1; 1 test file failed; 2 tests failed and 32 were skipped. Both the
Admin and Operator cases failed with `Unable to find an accessible element with
the role "button" and name /Load preview|加载预览/`. This proved the shipped UI
hid the action for the real list-only Catalog projection.

The complete pre-production matrix also failed only its three positive cases
(Admin sequential, Operator sequential, Operator Range); all eight negative
cases passed.

## Minimal implementation

`BackupAssetsWorkspace` now derives native-preview eligibility from all of:

- a non-empty authenticated runtime token;
- exact normalized role `admin` or `operator`;
- Catalog list permission and available content;
- a selected recovery point and renderer product; and
- the renderer's exact read requirement (`openSequential` for escaped text and
  metadata hex, `openRange` for Range renderers).

Catalog preview permission is ignored only at this native-preview UI boundary.
The existing typed delivery-ticket request, backend RBAC, Admin secret step-up,
Operator fail-closed state handling, and other product gates remain unchanged.

## GREEN and focused verification

- Exact RED selector rerun: exit 0; 2 passed, 41 skipped.
- Eligibility matrix selector: exit 0; 11 passed, 32 skipped.
- Repeated eligibility matrix: the same selector ran three consecutive times;
  every iteration exited 0 with 11 passed and 32 skipped.
- Whole workspace file:
  `env -u NODE_ENV npx vitest run src/features/backup-assets/backup-assets-workspace.test.tsx`
  — exit 0; 43 passed.
- Final post-format focused selector:

  ```sh
  env -u NODE_ENV npx vitest run src/features/backup-assets/backup-assets-workspace.test.tsx -t "sequential native preview from a list-only Catalog|Range capability for a range-native renderer|hides native preview and does not request a ticket"
  ```

  Result: exit 0; 11 passed, 32 skipped.

Negative coverage includes missing token, Viewer, unknown/missing normalized
role, list denial, unavailable content, missing selected recovery point, missing
sequential capability, and missing Range capability. Every negative asserts that
the action is hidden and no preview ticket action is called.

## Ticket, error, and server authorization verification

```sh
env -u NODE_ENV npx vitest run src/features/backup-assets/use-backup-assets-state.test.tsx -t "ordinary preview ticket|retries secret preview once|does not re-prompt TOTP on preview renew|does not request secret-reveal step-up|fails closed on an untyped preview denial"
```

Result: exit 0; 6 passed, 48 skipped. This retains ordinary ticket issue, Admin
secret retry, no repeated step-up on renewal, Operator fail-closed behavior, and
safe untyped-denial handling.

```sh
env -u NODE_ENV npx vitest run src/lib/api/backup-content-api.test.ts
```

Result: exit 0; 22 passed.

```sh
cd backend && go test ./internal/api -run '^TestBackupContentTicketRequiresPreviewPermissionBeforeFeatureGate$' -count=1 -v
```

Result: exit 0. The unauthenticated case and Admin, Operator, Viewer, and unknown
role subtests all passed, confirming that delivery-ticket RBAC remains final.

## Full web gate

```sh
cd web && env -u NODE_ENV npm run check
```

The first full-gate run exposed a strict type mismatch in the new test fixture:
Catalog capability reasons are structured objects, not strings. The fixture was
corrected to the production domain shape, then the full command was restarted.

Final result: exit 0.

- typecheck: passed;
- lint: passed;
- tests with coverage: 181 files passed, 1531 tests passed;
- production build: passed; Vite transformed 3222 modules and completed the
  build.

After the final indentation-only cleanup, `env -u NODE_ENV npm run typecheck`
and `env -u NODE_ENV npm run lint` both exited 0, in addition to the final
focused selector above.

## Privacy, source-boundary, and diff checks

- FastCtx source scan on the modified production workspace for
  `permissions.preview`, direct `fetch`, `localStorage`, `sessionStorage`, and
  `console` calls returned no matches.
- `git diff --exit-code -- backend web/src/lib web/src/types` exited 0 with no
  output, proving no backend, typed API, or domain DTO source changed.
- `git diff --check` exited 0 with no output, both before and after the final
  formatting cleanup.
- No Go source changed, so `gofmt` was not applicable; the focused backend Go
  selector compiled and passed.

The existing parent-task linkage change and the approved task/planning files
were preserved and were not rewritten or discarded by this implementation.
