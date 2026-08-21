package alerting

import (
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/settings"
)

const backupAssetSearchHTTPPath = "/api/v1/asset-search"

// HTTPRequestsTotalMetric is the Gin series from middleware.PrometheusMetrics.
const HTTPRequestsTotalMetric = "http_requests_total"

// BackupAssetSLORule is a settings-gated PromQL contract for the default-off
// backup-asset pilot. Rules are returned only when backup_assets.enabled is
// requested; they are the first alert hook, not a Grafana product.
type BackupAssetSLORule struct {
	ID       string
	Expr     string
	For      string
	Severity string
}

// BackupAssetSLORules returns the configurable search / audit / FeatureLive
// alert expressions when the feature has been requested.
func BackupAssetSLORules(featureRequested bool) []BackupAssetSLORule {
	if !featureRequested {
		return nil
	}
	return []BackupAssetSLORule{
		{
			ID: "backup_asset_search_5xx",
			Expr: `sum(rate(` + HTTPRequestsTotalMetric + `{method="POST",path="` + backupAssetSearchHTTPPath + `",status=~"5.."}[10m])) / ` +
				`sum(rate(` + HTTPRequestsTotalMetric + `{method="POST",path="` + backupAssetSearchHTTPPath + `"}[10m])) > 0.01`,
			For:      "10m",
			Severity: "page",
		},
		{
			ID:       "backup_asset_search_audit_fail",
			Expr:     `increase(` + HTTPRequestsTotalMetric + `{method="POST",path="` + backupAssetSearchHTTPPath + `",status="503"}[15m]) > 0`,
			For:      "0m",
			Severity: "page",
		},
		{
			ID:       "backup_asset_feature_live_jitter",
			Expr:     ga.FeatureRequestedMetric + ` - ` + ga.FeatureLiveMetric + ` != 0`,
			For:      "5m",
			Severity: "warn",
		},
	}
}

// BackupAssetSLORulesFromSettings is the settings-backed hook. Production
// startup calls Dispatcher.BackupAssetSLORules, which uses this.
func BackupAssetSLORulesFromSettings(svc *settings.Service) []BackupAssetSLORule {
	if svc == nil {
		return nil
	}
	return BackupAssetSLORules(svc.GetEffective("backup_assets.enabled") == "true")
}

// BackupAssetSLORules returns the PromQL contract for the current
// backup_assets.enabled setting.
func (d *Dispatcher) BackupAssetSLORules() []BackupAssetSLORule {
	if d == nil {
		return nil
	}
	return BackupAssetSLORulesFromSettings(d.Settings)
}
