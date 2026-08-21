package alerting

import (
	"strings"
	"testing"

	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBackupAssetSLORulesGatedByRequestedSetting(t *testing.T) {
	if rules := BackupAssetSLORules(false); len(rules) != 0 {
		t.Fatalf("disabled setting leaked rules=%+v", rules)
	}
	rules := BackupAssetSLORules(true)
	if len(rules) != 3 {
		t.Fatalf("rules=%+v", rules)
	}
	want := map[string]string{
		"backup_asset_search_5xx":          "page",
		"backup_asset_search_audit_fail":   "page",
		"backup_asset_feature_live_jitter": "warn",
	}
	for _, rule := range rules {
		if want[rule.ID] != rule.Severity || rule.Expr == "" || rule.For == "" {
			t.Fatalf("rule=%+v", rule)
		}
		delete(want, rule.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing rules=%v", want)
	}
}

func TestBackupAssetSLORulesUseEmittedHTTPAndFeatureSeries(t *testing.T) {
	rules := BackupAssetSLORules(true)
	byID := map[string]string{}
	for _, rule := range rules {
		byID[rule.ID] = rule.Expr
	}
	search5xx := byID["backup_asset_search_5xx"]
	audit := byID["backup_asset_search_audit_fail"]
	jitter := byID["backup_asset_feature_live_jitter"]
	if search5xx == "" || audit == "" || jitter == "" {
		t.Fatalf("rules=%+v", rules)
	}
	for _, expr := range []string{search5xx, audit} {
		if !strings.Contains(expr, HTTPRequestsTotalMetric) ||
			strings.Contains(expr, "xirang_http_requests_total") ||
			strings.Contains(expr, `code=`) ||
			!strings.Contains(expr, `status=`) ||
			!strings.Contains(expr, `path="/api/v1/asset-search"`) ||
			!strings.Contains(expr, `method="POST"`) {
			t.Fatalf("HTTP expr must use Gin series/labels: %s", expr)
		}
	}
	if !strings.Contains(jitter, ga.FeatureRequestedMetric) || !strings.Contains(jitter, ga.FeatureLiveMetric) {
		t.Fatalf("jitter expr=%s", jitter)
	}
}

func TestDispatcherBackupAssetSLORulesFollowSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:dispatcher-slo?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(db)
	dispatcher := NewDispatcher(db, svc, nil)
	if rules := dispatcher.BackupAssetSLORules(); len(rules) != 0 {
		t.Fatalf("default-off leaked rules=%+v", rules)
	}
	if err := svc.Update("backup_assets.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	rules := dispatcher.BackupAssetSLORules()
	if len(rules) != 3 {
		t.Fatalf("requested rules=%+v", rules)
	}
	if got := BackupAssetSLORulesFromSettings(svc); len(got) != 3 {
		t.Fatalf("settings hook=%+v", got)
	}
	if BackupAssetSLORulesFromSettings(nil) != nil {
		t.Fatal("nil settings must return no rules")
	}
}
