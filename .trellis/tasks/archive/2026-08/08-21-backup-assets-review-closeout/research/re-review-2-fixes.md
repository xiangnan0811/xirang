# Re-review 2 follow-up

Date: 2026-08-21. Implementer response to `research/re-review-2.md`. Not an independent review.

| Finding | Change |
|---|---|
| Playwright live path | Valid fixture search/catalog/ticket/content; asserts browse list, POST search, load-preview ticket, iframe preview text |
| Vitest MSW | Global `server.listen({ onUnhandledRequest: "error" })` in `vitest.setup.ts` |
| Load CI | `TestCatalogPaginatesTenThousandCommittedEntries` + `TestControlledProcessSIGKILLThenRestartReconciles`; million is the same 10k owner; docs in `docs/admin/backup-assets-load.md` |
| SLO hook | `alerting.BackupAssetSLORules(featureRequested)` + PromQL in `docs/admin/backup-assets-slo.md` |
| AC4 HTTP | Config source requested-true/live-false + NewRouter 503 matrix + handler HTTP + broker FeatureLive false |
| Restore matrix | `TestFullRouterSnapshotRestoreLiveAndStepUpMatrix` on `NewRouter` |
| F7 re-prove | Token-change and asset-change tests |
| Ledger | `R-P1-ci-soft` and `R-P1-scale-cloud` are `closed-with-limits`; handler-requested cites HTTP evidence |

Local evidence: Vitest 1520 passed; Playwright chromium+firefox passed (webkit missing in this sandbox); load script `ci-bounded` passed.
