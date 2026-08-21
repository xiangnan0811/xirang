# Backup asset SLOs

These signals already exist. Treat them as the first operational contract for a
default-off Core-only pilot. They are not a Grafana dashboard.

The configurable hook is `alerting.Dispatcher.BackupAssetSLORules()`, which
reads `backup_assets.enabled` through `settings.Service`. The process calls that
method at startup. When the setting is requested, the function returns the
PromQL below. When the setting is false, it returns no rules.

Search 5xx / audit-fail expressions use Gin's `http_requests_total` series
(`method`, `path`, `status`). FeatureLive jitter uses the GA gauges
`xirang_backup_asset_feature_requested` and `xirang_backup_asset_feature_live`,
which `Runtime.FeatureLive()` updates on every door check.

| Signal | Metric / log | SLO | Alert |
|---|---|---|---|
| Search HTTP success | `http_requests_total{method="POST",path="/api/v1/asset-search"}` | 99% of successful FeatureLive searches return 2xx over 15m | `backup_asset_search_5xx` |
| Search audit fail-closed | same series with `status="503"` plus handler warn `备份资产搜索审计写入失败` | No secret search 200 without audit | `backup_asset_search_audit_fail` |
| FeatureLive | `xirang_backup_asset_feature_requested` vs `xirang_backup_asset_feature_live` | Requested true and FeatureLive false is a pending-enable state, not an outage | `backup_asset_feature_live_jitter` |
| Search builds | `xirang_backup_asset_search_builds_total` | Build failures do not stay silent | Warn when `outcome="error"` increases |
| Abandoned search | `xirang_backup_asset_search_reconciled_abandoned_total` | Reconcile makes progress | Warn if abandoned reconcile stalls |

```promql
# backup_asset_search_5xx (page, 10m)
sum(rate(http_requests_total{method="POST",path="/api/v1/asset-search",status=~"5.."}[10m]))
/
sum(rate(http_requests_total{method="POST",path="/api/v1/asset-search"}[10m]))
> 0.01

# backup_asset_search_audit_fail (page, immediate)
increase(http_requests_total{method="POST",path="/api/v1/asset-search",status="503"}[15m]) > 0

# backup_asset_feature_live_jitter (warn, 5m)
xirang_backup_asset_feature_requested - xirang_backup_asset_feature_live != 0
```

Rollback: set `backup_assets.enabled=false`. Leftover snapshot read APIs stay 410.
