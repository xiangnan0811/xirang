# Re-review 2 — Wave 2 (F1–F3)

Date: 2026-08-21  
Reviewer: independent pass on `feat/backup-assets-closeout` uncommitted Wave 1+2  
Previous: `research/re-review-1.md` (not clean; no commit / PR)

## Verdict

**Clean for AC7b.** F1–F3 from re-review 1 are implemented in the worktree. Commit and PR are allowed after `make swag-init` (F6) if handler annotations still need generated OpenAPI.

This is **not** parent archive, **not** CodeDefault flip, **not** “default-on GA.” Child stays `in_progress` until Alan archives it (AC8–AC9).

## Evidence this session

Backend packages (exit 0, `-count=1`):

```text
ok  backupasset/runtime
ok  backupasset/ga
ok  backupasset/catalog
ok  backupasset/search
ok  backupasset/content
ok  internal/api/handlers
```

Frontend focused vitest (exit 0, 74 tests):

```text
use-backup-assets-state.test.tsx
asset-list.test.tsx
asset-grid.test.tsx
backup-asset-search-api.test.ts
```

Security-review: no medium+ issues on the Wave 2 secret-reveal / FeatureLive paths.  
Bugbot: one residual (F7 below), not an F1–F3 miss.

## F1–F3 independently verified

| ID | Claim | Live evidence |
|---|---|---|
| F1 | Admin-only reveal UI | `use-backup-assets-state.ts:1242–1247` requires `role === "admin"`. `backups-page.data.tsx:78–83` passes `role`. Operator/Viewer test does not call step-up and does not retry (`use-backup-assets-state.test.tsx:1783–1804`). |
| F1 | Backend rejects non-admin proof | Search verifier `backup_asset_search_handler.go:92–94`; ticket `backup_content_handler.go:469–471`; broker `broker.go:346–347`; search service `search/service.go:254–256`. |
| F2 | Same proof on every search | `withSecretRevealProof` at `:1488–1493`; used by saved-search `:584`, first search `:626`, `loadMore` `:709`. Test `:1807–1879`. |
| F3 | Count visible on result rows | Row keeps `retainedVersionCount` (`:1495–1502`). List/grid show it only when `source === "search"` and count > 1 (`asset-list.tsx:193–198`). Illegal `0` fails the whole projection (`backup-asset-search-api.ts:221–225`). |

## Still not blocking this wave

F4–F6 from re-review 1 stay should-fix / enable-time:

- **F4** Settings enable does not start Search (`TransitionFeature` vs `startupSearch`). Restart after enable.
- **F5** HTTP handler `Enabled` still follows requested setting; services use `FeatureLive`.
- **F6** `docs.go` still missing versions route. Run `make swag-init` **now**, as part of the ship sequence, not as a substitute for F1–F3.

## New residual (not a Wave 2 fail)

### F7 — Preview renew does not reuse the session reveal proof

`loadPreview` / `renewPreview` always issue the first ticket without `secretRevealProofRef` (`use-backup-assets-state.ts:1285–1306`). After Admin reveal, ticket expiry re-hits `secret_reveal_required` and opens step-up again (`reuseCached: false`). Fail-closed; not an Operator hole.

Do **not** treat this as a reason to hold the Wave 2 PR. Record it for a later wave if Alan wants it.

## Go-live (unchanged conditions)

After this PR merges:

1. Upgrade with the feature left off.
2. Inventory → ack → settings enable.
3. **Restart** the process (F4).
4. Validate one repository: browse, native preview, one secret preview, one isolated recovery, all-retained search count.
5. Keep CodeDefault `"false"` (`settings/service.go:296`). Keep Worker unpublished.

## Next

`make swag-init` if OpenAPI annotations changed. Commit on `feat/backup-assets-closeout`. Open PR. Required CI green. Merge. Watch Release Please. No Worker publish. No parent archive. No Child 17.
