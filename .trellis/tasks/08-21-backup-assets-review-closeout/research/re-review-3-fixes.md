# Re-review 3 follow-up

Date: 2026-08-21. Implementer response to `research/re-review-3.md`. Not an independent review.

| Finding | Change |
|---|---|
| SLO PromQL / dead hook | Expressions use Gin `http_requests_total{method,path,status}` and GA gauges `xirang_backup_asset_feature_requested` / `xirang_backup_asset_feature_live`. `Runtime.FeatureLive()` writes those gauges. `Dispatcher.BackupAssetSLORules()` is the settings hook; `cmd/server` calls it at startup. Tests reject `code=` / `xirang_http_requests_total`. |
| NewRouter AC4 Catalog/Content stubs | `TestRequestedTrueFeatureLiveFalseClosesProductionCatalogAndContentHTTP` wires production `catalog.Service` + `content.Broker` with `FeatureEnabled = Runtime.FeatureLive`. Closed (requested, unacked) catalog is 503 `备份资产功能未启用`; live (fresh ready) catalog/content leave that feature-disabled door. |
| Ledger | `R-P1-handler-requested` now cites the production Catalog/Content HTTP test. |

Do not commit `web/test-results/`. `.gitignore` now excludes that directory.
