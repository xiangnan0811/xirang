# Re-review 1 — Wave 1 work branch

Date: 2026-08-21  
Reviewer: independent pass on `feat/backup-assets-closeout` uncommitted Wave 1  
Baseline: v0.50.0 / `1750a238` plus Child 16 working tree  
Scope: entire backup-asset explorer go-live path, not only the punch-list diff

Gate: PRD AC7b — commit / PR only after a clean re-review. **This pass is not clean.**

## Verdict

Do **not** commit or open a PR on this tree.

Wave 1 closed the 08-21 **requested-vs-live** split for Catalog / Search / Overlay / Content. That was the real enablement-safety bug. The rest of R1–R5 is mostly landed, with three contract misses that would ship a dishonest or incomplete product.

Go-live on an existing production box (single Admin, Core-only) is **conditional** after the must-fix items below, a process restart after settings enable, and a one-repository catalog check. It is **not** default-on GA. Parent stays `planning`. CodeDefault stays `false`.

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

Frontend vitest was not re-run: this host has no usable `web` vitest binary. Frontend claims below are from source, not a fresh green run.

Security-review subagent: no medium+ issue in the *tightening* of the live gate. Bugbot and this pass found product-contract holes in the new UI wiring.

## Must-fix before commit (AC7b)

### F1 — Operator can complete secret reveal (AC3)

PRD AC3: Operator cannot reveal. Design: Admin-only `asset.secret_reveal`.

Wave 1 wired preview retry whenever `ensureStepUpProof` exists (`use-backup-assets-state.ts:1246–1260`). The page passes that helper for every role. `POST /auth/step-up` issues any valid action after TOTP with **no role allowlist** (`auth_handler.go:441–481`). Content broker allows operator preview and accepts a valid secret-reveal proof (`broker.go:340–341`, `contracts.go:168–169`). Pre-Wave-1 UI never asked for that proof, so Operator stayed blocked. This wave **widens** Operator capability.

Fix: gate the retry on Admin; keep Operator on the blocked empty state. Add an Operator test that `ensureStepUpProof` is not called and no ticket is retried.

### F2 — Search drops the reveal proof after page one

`executeSearch` forwards `secretRevealProofRef` (`use-backup-assets-state.ts:627–629`). `loadMore` and saved-search reload do not (`:708–714`). After Admin reveal, page two of content/OCR search silently loses secret hits or changes coverage.

Fix: pass the same optional proof on every search request that uses that session ref.

### F3 — All-retained version count is not observable in search

Backend grouping sets `retained_version_count` (`search/service.go:1314–1328`). The API mapper keeps it (`backup-asset-search-api.ts:221–231`). `commitSearchProjection` drops it (`use-backup-assets-state.ts:1501–1508`). No feature component renders `retainedVersionCount`.

The versions inspector lists other points (`asset-versions.tsx`). That is not enough for R3: search still looks like a single version unless the user already opened the inspector.

Fix: keep the count on the result row and show it on all-retained hits.

## Should-fix before production enable

### F4 — Settings enable does not start Search

`TransitionFeature(true)` authorizes, prepares content, and sets content ready (`runtime.go:1940–1969`). `startupSearch` runs only from `StartupPass` (`:2275`, `:2305–2342`). Hot enable without restart leaves search `temporarily_unavailable`.

Fix: start search (and document restart if any other plane still needs it) inside the enable transition, or make the Admin UI state that a restart is required before search.

### F5 — HTTP handler `Enabled` still follows the requested setting

`runtimeBackupAssetHandlerConfigSource` uses `SearchOverlayConfig()` (`router.go` ~188). Services use `FeatureLive`. Today the service still returns `feature_disabled`, so this is not a disclosure hole. It is a second door.

### F6 — OpenAPI is stale

`ListEntryVersions` is routed and has handler comments. `docs.go` has no versions path. Run `make swag-init` **after** F1–F3, not as a substitute.

## Confirmed closed from 08-21

| Item | Evidence |
|---|---|
| Env true without ack keeps Catalog/Search/Content closed | `featureLive` in `ga_runtime.go:36–57`; injection in `runtime.go:499–514`; tests in `ga_runtime_test.go` |
| Settings enable without ack still 409 | existing `settings_transition_test.go` still in the package run |
| `Initialize()` alone cannot become managed | `controller.go:44–55`; `admission_controller_test.go:199–208` |
| Ack audit `conflicts` uses inventory count | `inventory.go:219–223` |
| In-place create is confirmed | `recovery-plan-wizard.tsx:230–236` (confirm on create, not on mode change) |
| Preview pane uses capability codes | `asset-preview.tsx:384–394` |
| Versions API is opaque + ownership-filtered | `catalog/service.go:404–462` |

## Out of scope (do not treat as Wave 1 failures)

Leftover snapshot HTTP, Worker publish, CodeDefault flip, million-entry CI, AWS live-suite, Child 13 RED, production click-through, parent archive.

## Go-live position (existing single-Admin Core-only box)

After F1–F3 land and this re-review is re-run clean:

1. Upgrade with `BACKUP_ASSETS_ENABLED` left false.
2. Confirm overview health and disabled data/recovery shells.
3. Inventory dry-run → read conflicts → ack → settings enable.
4. Restart the process (required until F4).
5. Validate one non-critical repository: points, catalog rows, native preview, one secret preview, one isolated recovery.
6. Keep Worker unpublished.

Do not flip CodeDefault. Do not archive the parent.

## Next wave

Amend this child’s `prd.md` / `design.md` / `implement.md` with F1–F3 (and F4 if taken). Implement on this branch. Re-review again. Do not create Child 17.
