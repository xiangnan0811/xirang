package settings

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRegistry(t *testing.T) {
	svc := NewService(setupTestDB(t))
	defs := svc.Registry()
	seenKeys := make(map[string]bool, len(defs))
	seenEnv := make(map[string]bool, len(defs))
	for _, def := range defs {
		if seenKeys[def.Key] {
			t.Fatalf("duplicate setting key %q", def.Key)
		}
		if seenEnv[def.EnvVar] {
			t.Fatalf("duplicate setting env var %q", def.EnvVar)
		}
		seenKeys[def.Key] = true
		seenEnv[def.EnvVar] = true
	}
	// 确认返回副本，不影响全局 registry
	defs[0].Key = "mutated"
	if registry[0].Key == "mutated" {
		t.Error("Registry() should return a copy, not a reference")
	}
}

func TestBackupAssetSearchConfigAndOverlayConfigDefinitionsAndSafeDefaults(t *testing.T) {
	type expectedDefinition struct {
		env          string
		defaultValue string
		settingType  SettingType
		min          string
		max          string
		minDuration  string
		maxDuration  string
	}
	want := map[string]expectedDefinition{
		"backup_assets.enabled":                          {"BACKUP_ASSETS_ENABLED", "false", TypeBool, "", "", "", ""},
		"backup_assets.catalog_batch_size":               {"BACKUP_ASSETS_CATALOG_BATCH_SIZE", "2000", TypeInt, "1", "100000", "", ""},
		"backup_assets.catalog_build_timeout":            {"BACKUP_ASSETS_CATALOG_BUILD_TIMEOUT", "30m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.repository_reconcile_interval":    {"BACKUP_ASSETS_REPOSITORY_RECONCILE_INTERVAL", "15m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.audit_segment_max_events":         {"BACKUP_ASSETS_AUDIT_SEGMENT_MAX_EVENTS", "10000", TypeInt, "100", "1000000", "", ""},
		"backup_assets.audit_segment_max_age":            {"BACKUP_ASSETS_AUDIT_SEGMENT_MAX_AGE", "24h", TypeDuration, "", "", "1h", "168h"},
		"backup_assets.audit_detail_retention_days":      {"BACKUP_ASSETS_AUDIT_DETAIL_RETENTION_DAYS", "180", TypeInt, "1", "3650", "", ""},
		"backup_assets.audit_checkpoint_retention_days":  {"BACKUP_ASSETS_AUDIT_CHECKPOINT_RETENTION_DAYS", "2555", TypeInt, "180", "36500", "", ""},
		"backup_assets.lease_duration":                   {"BACKUP_ASSETS_LEASE_DURATION", "5m", TypeDuration, "", "", "30s", "30m"},
		"backup_assets.lease_heartbeat":                  {"BACKUP_ASSETS_LEASE_HEARTBEAT", "60s", TypeDuration, "", "", "10s", "5m"},
		"backup_assets.lease_absolute_deadline":          {"BACKUP_ASSETS_LEASE_ABSOLUTE_DEADLINE", "168h", TypeDuration, "", "", "5m", "168h"},
		"backup_assets.provider_operation_timeout":       {"BACKUP_ASSETS_PROVIDER_OPERATION_TIMEOUT", "2m", TypeDuration, "", "", "5s", "30m"},
		"backup_assets.provider_max_concurrency":         {"BACKUP_ASSETS_PROVIDER_MAX_CONCURRENCY", "4", TypeInt, "1", "32", "", ""},
		"backup_assets.provider_metadata_limit_bytes":    {"BACKUP_ASSETS_PROVIDER_METADATA_LIMIT_BYTES", "16777216", TypeInt, "65536", "67108864", "", ""},
		"backup_assets.publication_reconcile_interval":   {"BACKUP_ASSETS_PUBLICATION_RECONCILE_INTERVAL", "5m", TypeDuration, "", "", "30s", "24h"},
		"backup_assets.publication_reconcile_batch_size": {"BACKUP_ASSETS_PUBLICATION_RECONCILE_BATCH_SIZE", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.publication_worker_concurrency":   {"BACKUP_ASSETS_PUBLICATION_WORKER_CONCURRENCY", "2", TypeInt, "1", "32", "", ""},
		"backup_assets.publication_missing_grace":        {"BACKUP_ASSETS_PUBLICATION_MISSING_GRACE", "30m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.publication_stream_max_bytes":     {"BACKUP_ASSETS_PUBLICATION_STREAM_MAX_BYTES", "268435456", TypeInt, "1048576", "1073741824", "", ""},
		"backup_assets.manifest_timeout":                 {"BACKUP_ASSETS_MANIFEST_TIMEOUT", "2h", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.manifest_max_bytes":               {"BACKUP_ASSETS_MANIFEST_MAX_BYTES", "4294967296", TypeInt, "1048576", "17179869184", "", ""},
		"backup_assets.manifest_max_entries":             {"BACKUP_ASSETS_MANIFEST_MAX_ENTRIES", "10000000", TypeInt, "1", "100000000", "", ""},
		"backup_assets.manifest_max_record_bytes":        {"BACKUP_ASSETS_MANIFEST_MAX_RECORD_BYTES", "1048576", TypeInt, "4096", "4194304", "", ""},
		"backup_assets.manifest_max_depth":               {"BACKUP_ASSETS_MANIFEST_MAX_DEPTH", "4096", TypeInt, "1", "65536", "", ""},
		"backup_assets.rclone_preflight_ttl":             {"BACKUP_ASSETS_RCLONE_PREFLIGHT_TTL", "30m", TypeDuration, "", "", "16m", "24h"},
		"backup_assets.rclone_portable_deadline":         {"BACKUP_ASSETS_RCLONE_PORTABLE_DEADLINE", "24h", TypeDuration, "", "", "5m", "168h"},
		"backup_assets.rclone_native_deadline":           {"BACKUP_ASSETS_RCLONE_NATIVE_DEADLINE", "45m", TypeDuration, "", "", "5m", "55m"},
		"backup_assets.rclone_bound_config_max_bytes":    {"BACKUP_ASSETS_RCLONE_BOUND_CONFIG_MAX_BYTES", "65536", TypeInt, "1024", "65536", "", ""},
		"backup_assets.rclone_control_payload_max_bytes": {"BACKUP_ASSETS_RCLONE_CONTROL_PAYLOAD_MAX_BYTES", "8388608", TypeInt, "65536", "67108864", "", ""},
		"backup_assets.rclone_full_verify_max_bytes":     {"BACKUP_ASSETS_RCLONE_FULL_VERIFY_MAX_BYTES", "1099511627776", TypeInt, "1048576", "17592186044416", "", ""},
		"backup_assets.rclone_manifest_chunk_max_bytes":  {"BACKUP_ASSETS_RCLONE_MANIFEST_CHUNK_MAX_BYTES", "8388608", TypeInt, "65536", "67108864", "", ""},
		"backup_assets.rclone_low_level_retries":         {"BACKUP_ASSETS_RCLONE_LOW_LEVEL_RETRIES", "3", TypeInt, "1", "10", "", ""},
		"backup_assets.rclone_staging_orphan_age":        {"BACKUP_ASSETS_RCLONE_STAGING_ORPHAN_AGE", "24h", TypeDuration, "", "", "1h", "168h"},
		"backup_assets.rclone_staging_scan_limit":        {"BACKUP_ASSETS_RCLONE_STAGING_SCAN_LIMIT", "256", TypeInt, "1", "4096", "", ""},
		"backup_assets.rclone_kms_read_key_max_count":    {"BACKUP_ASSETS_RCLONE_KMS_READ_KEY_MAX_COUNT", "8", TypeInt, "1", "32", "", ""},
		"backup_assets.rclone_health_interval":           {"BACKUP_ASSETS_RCLONE_HEALTH_INTERVAL", "15m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.rclone_health_batch_size":         {"BACKUP_ASSETS_RCLONE_HEALTH_BATCH_SIZE", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.rclone_aws_sdk_max_attempts":      {"BACKUP_ASSETS_RCLONE_AWS_SDK_MAX_ATTEMPTS", "3", TypeInt, "1", "10", "", ""},
		"backup_assets.search_reconcile_interval":        {"BACKUP_ASSETS_SEARCH_RECONCILE_INTERVAL", "1m", TypeDuration, "", "", "10s", "1h"},
		"backup_assets.search_build_timeout":             {"BACKUP_ASSETS_SEARCH_BUILD_TIMEOUT", "30m", TypeDuration, "", "", "1m", "24h"},
		"backup_assets.search_batch_size":                {"BACKUP_ASSETS_SEARCH_BATCH_SIZE", "500", TypeInt, "50", "5000", "", ""},
		"backup_assets.search_max_concurrency":           {"BACKUP_ASSETS_SEARCH_MAX_CONCURRENCY", "2", TypeInt, "1", "16", "", ""},
		"backup_assets.search_ast_max_depth":             {"BACKUP_ASSETS_SEARCH_AST_MAX_DEPTH", "8", TypeInt, "1", "16", "", ""},
		"backup_assets.search_ast_max_nodes":             {"BACKUP_ASSETS_SEARCH_AST_MAX_NODES", "64", TypeInt, "2", "256", "", ""},
		"backup_assets.search_values_per_node":           {"BACKUP_ASSETS_SEARCH_VALUES_PER_NODE", "32", TypeInt, "1", "64", "", ""},
		"backup_assets.search_body_max_bytes":            {"BACKUP_ASSETS_SEARCH_BODY_MAX_BYTES", "65536", TypeInt, "1024", "65536", "", ""},
		"backup_assets.search_value_max_bytes":           {"BACKUP_ASSETS_SEARCH_VALUE_MAX_BYTES", "1024", TypeInt, "1", "4096", "", ""},
		"backup_assets.search_candidate_limit":           {"BACKUP_ASSETS_SEARCH_CANDIDATE_LIMIT", "10000", TypeInt, "100", "100000", "", ""},
		"backup_assets.search_query_timeout":             {"BACKUP_ASSETS_SEARCH_QUERY_TIMEOUT", "5s", TypeDuration, "", "", "100ms", "30s"},
		"backup_assets.search_page_size_max":             {"BACKUP_ASSETS_SEARCH_PAGE_SIZE_MAX", "200", TypeInt, "1", "500", "", ""},
		"backup_assets.search_suggestion_limit":          {"BACKUP_ASSETS_SEARCH_SUGGESTION_LIMIT", "20", TypeInt, "0", "50", "", ""},
		"backup_assets.saved_search_quota":               {"BACKUP_ASSETS_SAVED_SEARCH_QUOTA", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.favorite_quota":                   {"BACKUP_ASSETS_FAVORITE_QUOTA", "5000", TypeInt, "1", "100000", "", ""},
		"backup_assets.tag_definition_quota":             {"BACKUP_ASSETS_TAG_DEFINITION_QUOTA", "100", TypeInt, "1", "1000", "", ""},
		"backup_assets.tag_assignment_quota":             {"BACKUP_ASSETS_TAG_ASSIGNMENT_QUOTA", "10000", TypeInt, "1", "200000", "", ""},
		"backup_assets.overlay_bulk_max_items":           {"BACKUP_ASSETS_OVERLAY_BULK_MAX_ITEMS", "200", TypeInt, "1", "1000", "", ""},
		"backup_assets.overlay_label_max_bytes":          {"BACKUP_ASSETS_OVERLAY_LABEL_MAX_BYTES", "256", TypeInt, "1", "4096", "", ""},
		"backup_assets.recent_quota":                     {"BACKUP_ASSETS_RECENT_QUOTA", "10000", TypeInt, "1", "100000", "", ""},
		"backup_assets.recent_retention":                 {"BACKUP_ASSETS_RECENT_RETENTION", "720h", TypeDuration, "", "", "24h", "8760h"},
		"backup_assets.recent_writes_per_minute":         {"BACKUP_ASSETS_RECENT_WRITES_PER_MINUTE", "120", TypeInt, "1", "10000", "", ""},
		"backup_assets.idempotency_ttl":                  {"BACKUP_ASSETS_IDEMPOTENCY_TTL", "24h", TypeDuration, "", "", "1h", "168h"},
		"backup_assets.idempotency_key_max_bytes":        {"BACKUP_ASSETS_IDEMPOTENCY_KEY_MAX_BYTES", "128", TypeInt, "32", "256", "", ""},
	}
	defs := NewService(setupTestDB(t)).Registry()
	got := make(map[string]SettingDef, len(want))
	for _, def := range defs {
		if strings.HasPrefix(def.Key, "backup_assets.") {
			got[def.Key] = def
		}
	}
	if len(got) != len(want) {
		t.Fatalf("backup asset setting count=%d, want %d", len(got), len(want))
	}
	for key, expected := range want {
		def, ok := got[key]
		if !ok {
			t.Fatalf("missing setting %s", key)
		}
		if def.EnvVar != expected.env || def.CodeDefault != expected.defaultValue || def.Type != expected.settingType ||
			def.Min != expected.min || def.Max != expected.max || def.MinDuration != expected.minDuration || def.MaxDuration != expected.maxDuration {
			t.Errorf("setting %s mismatch: %+v", key, def)
		}
		if def.Sensitive || def.RequiresRestart {
			t.Errorf("foundation setting %s must be dynamic and non-sensitive", key)
		}
	}

	t.Setenv("BACKUP_ASSETS_ENABLED", "")
	service := NewService(setupTestDB(t))
	if got := service.GetEffective("backup_assets.enabled"); got != "false" {
		t.Fatalf("backup assets default=%q, want false", got)
	}
}

func TestMaxDurationValidation(t *testing.T) {
	service := NewService(setupTestDB(t))
	if err := service.Validate("backup_assets.catalog_build_timeout", "24h"); err != nil {
		t.Fatalf("24h maximum rejected: %v", err)
	}
	if err := service.Validate("backup_assets.catalog_build_timeout", "24h1s"); err == nil {
		t.Fatal("duration above MaxDuration unexpectedly accepted")
	}
	if err := validateRegistryDefinitions([]SettingDef{{
		Key: "test.duration", EnvVar: "TEST_DURATION", CodeDefault: "1m", Type: TypeDuration, MaxDuration: "not-a-duration",
	}}); err == nil {
		t.Fatal("malformed MaxDuration definition unexpectedly accepted")
	}
}

func TestBackupAssetSettingsLeaseHeartbeatMustBeLowerThanDuration(t *testing.T) {
	valid := map[string]string{
		"backup_assets.lease_duration":          "5m",
		"backup_assets.lease_heartbeat":         "60s",
		"backup_assets.lease_absolute_deadline": "168h",
	}
	if err := ValidateBackupAssetFoundationConfig(valid); err != nil {
		t.Fatalf("valid foundation lease config rejected: %v", err)
	}
	invalid := map[string]string{
		"backup_assets.lease_duration":          "5m",
		"backup_assets.lease_heartbeat":         "5m",
		"backup_assets.lease_absolute_deadline": "168h",
	}
	if err := ValidateBackupAssetFoundationConfig(invalid); err == nil {
		t.Fatal("heartbeat equal to lease duration unexpectedly accepted")
	}
}

func TestBackupAssetFoundationCrossSettingPublicationBoundaries(t *testing.T) {
	values := backupAssetFoundationValuesForTest()
	values["backup_assets.lease_duration"] = "71s"
	values["backup_assets.lease_heartbeat"] = "60s"
	values["backup_assets.lease_absolute_deadline"] = "2h"
	values["backup_assets.publication_missing_grace"] = "71s"
	values["backup_assets.manifest_timeout"] = "1h"
	values["backup_assets.manifest_max_bytes"] = "1048576"
	values["backup_assets.manifest_max_record_bytes"] = "1048576"
	if err := ValidateBackupAssetFoundationConfig(values); err != nil {
		t.Fatalf("valid publication foundation config rejected: %v", err)
	}
	if sshutil.CommandExecutionJoinTimeout != 10*time.Second {
		t.Fatalf("command execution join timeout=%s, want 10s", sshutil.CommandExecutionJoinTimeout)
	}

	invalidJoinMargin := cloneSettingsValues(values)
	invalidJoinMargin["backup_assets.lease_duration"] = "70s"
	if err := ValidateBackupAssetFoundationConfig(invalidJoinMargin); err == nil {
		t.Fatal("lease duration with no command-join margin unexpectedly accepted")
	}
	invalidMissingGrace := cloneSettingsValues(values)
	invalidMissingGrace["backup_assets.publication_missing_grace"] = "60s"
	if err := ValidateBackupAssetFoundationConfig(invalidMissingGrace); err == nil {
		t.Fatal("publication missing grace below lease duration unexpectedly accepted")
	}
	invalidManifestTimeout := cloneSettingsValues(values)
	invalidManifestTimeout["backup_assets.manifest_timeout"] = "2h"
	if err := ValidateBackupAssetFoundationConfig(invalidManifestTimeout); err == nil {
		t.Fatal("manifest timeout equal to absolute deadline unexpectedly accepted")
	}
	invalidRecordLimit := cloneSettingsValues(values)
	invalidRecordLimit["backup_assets.manifest_max_record_bytes"] = "1048577"
	if err := ValidateBackupAssetFoundationConfig(invalidRecordLimit); err == nil {
		t.Fatal("manifest record limit above total bytes unexpectedly accepted")
	}
}

func TestBackupAssetRcloneSettingCrossFieldBoundaries(t *testing.T) {
	service := NewService(setupTestDB(t))
	values := backupAssetFoundationValuesForTest()
	values["backup_assets.rclone_preflight_ttl"] = "16m"
	values["backup_assets.rclone_native_deadline"] = "55m"
	values["backup_assets.rclone_bound_config_max_bytes"] = "65536"
	values["backup_assets.rclone_control_payload_max_bytes"] = "65536"
	values["backup_assets.rclone_manifest_chunk_max_bytes"] = "65536"
	if err := service.ValidateBackupAssetEffectiveUpdate(values, map[string]string{}); err != nil {
		t.Fatalf("valid Rclone boundary settings rejected: %v", err)
	}

	for name, overrides := range map[string]map[string]string{
		"settle window": {"backup_assets.rclone_preflight_ttl": "15m"},
		"STS margin":    {"backup_assets.rclone_native_deadline": "55m1s"},
		"SecretStdin":   {"backup_assets.rclone_bound_config_max_bytes": "65537"},
		"manifest payload": {
			"backup_assets.rclone_control_payload_max_bytes": "65536",
			"backup_assets.rclone_manifest_chunk_max_bytes":  "65537",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := service.ValidateBackupAssetEffectiveUpdate(values, overrides); err == nil {
				t.Fatalf("unsafe Rclone settings accepted: %#v", overrides)
			}
		})
	}
}

func TestValidateBackupAssetEffectiveUpdateCombinesExplicitCurrentAndRequestOverrides(t *testing.T) {
	service := NewService(setupTestDB(t))
	current := backupAssetFoundationValuesForTest()
	current["backup_assets.lease_duration"] = "71s"
	current["backup_assets.lease_heartbeat"] = "60s"
	if err := service.ValidateBackupAssetEffectiveUpdate(current, map[string]string{"backup_assets.publication_missing_grace": "71s"}); err != nil {
		t.Fatalf("valid explicit current/override combination rejected: %v", err)
	}
	if err := service.ValidateBackupAssetEffectiveUpdate(current, map[string]string{"backup_assets.lease_duration": "70s"}); err == nil {
		t.Fatal("invalid explicit current/override combination unexpectedly accepted")
	}
	if err := service.ValidateBackupAssetEffectiveUpdate(current, map[string]string{"login.rate_limit": "100"}); err == nil {
		t.Fatal("non-foundation override unexpectedly accepted")
	}
}

func TestValidateBackupAssetEffectiveUpdateDoesNotMutateInputsOrReadAgain(t *testing.T) {
	db := setupTestDB(t)
	service := NewService(db)
	current := backupAssetFoundationValuesForTest()
	current["backup_assets.lease_duration"] = "71s"
	current["backup_assets.lease_heartbeat"] = "60s"
	overrides := map[string]string{"backup_assets.publication_missing_grace": "71s"}
	wantCurrent := cloneSettingsValues(current)
	wantOverrides := cloneSettingsValues(overrides)
	if err := service.Update("backup_assets.lease_duration", "70s"); err != nil {
		t.Fatalf("seed divergent DB value: %v", err)
	}
	t.Setenv("BACKUP_ASSETS_PUBLICATION_MISSING_GRACE", "60s")
	if err := service.ValidateBackupAssetEffectiveUpdate(current, overrides); err != nil {
		t.Fatalf("pure explicit validation read DB/env instead of supplied maps: %v", err)
	}
	if !reflect.DeepEqual(current, wantCurrent) || !reflect.DeepEqual(overrides, wantOverrides) {
		t.Fatalf("explicit validation mutated inputs: current=%#v overrides=%#v", current, overrides)
	}
}

func TestWithBackupAssetMutationSerializesCallbacksOverFreshSnapshots(t *testing.T) {
	service := NewService(setupTestDB(t))
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	secondObserved := make(chan string, 1)
	secondDone := make(chan error, 1)

	go func() {
		firstDone <- service.WithBackupAssetMutation(context.Background(), func(current map[string]string) error {
			close(firstEntered)
			<-releaseFirst
			return service.Update("backup_assets.lease_duration", "10m")
		})
	}()
	<-firstEntered
	go func() {
		secondDone <- service.WithBackupAssetMutation(context.Background(), func(current map[string]string) error {
			secondObserved <- current["backup_assets.lease_duration"]
			current["backup_assets.lease_duration"] = "mutated-callback-copy"
			return nil
		})
	}()
	select {
	case observed := <-secondObserved:
		t.Fatalf("second callback entered before first persistence finished: %q", observed)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	if observed := <-secondObserved; observed != "10m" {
		t.Fatalf("second mutation did not receive a fresh snapshot: %q", observed)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second mutation: %v", err)
	}
	if err := service.WithBackupAssetMutation(context.Background(), func(current map[string]string) error {
		if current["backup_assets.lease_duration"] != "10m" {
			t.Fatalf("callback mutation corrupted service-owned snapshot: %q", current["backup_assets.lease_duration"])
		}
		return nil
	}); err != nil {
		t.Fatalf("third mutation: %v", err)
	}
}

func TestBackupAssetSearchConfigAndOverlayConfigCrossSettingBoundaries(t *testing.T) {
	values := backupAssetFoundationValuesForTest()
	if err := ValidateBackupAssetFoundationConfig(values); err != nil {
		t.Fatalf("valid Search/Overlay defaults rejected: %v", err)
	}

	tests := map[string]map[string]string{
		"nodes below depth": {
			"backup_assets.search_ast_max_depth": "9",
			"backup_assets.search_ast_max_nodes": "8",
		},
		"body below value": {
			"backup_assets.search_body_max_bytes":  "1024",
			"backup_assets.search_value_max_bytes": "1025",
		},
		"candidates below page": {
			"backup_assets.search_candidate_limit": "100",
			"backup_assets.search_page_size_max":   "101",
		},
		"page below suggestions": {
			"backup_assets.search_page_size_max":    "20",
			"backup_assets.search_suggestion_limit": "21",
		},
		"assignments below bulk": {
			"backup_assets.tag_assignment_quota":   "199",
			"backup_assets.overlay_bulk_max_items": "200",
		},
		"build beyond lease deadline": {
			"backup_assets.search_build_timeout":    "2h1s",
			"backup_assets.lease_absolute_deadline": "2h",
		},
	}
	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := cloneSettingsValues(values)
			for key, value := range overrides {
				candidate[key] = value
			}
			if err := ValidateBackupAssetFoundationConfig(candidate); err == nil {
				t.Fatalf("unsafe Search/Overlay settings accepted: %#v", overrides)
			}
		})
	}
}

func TestBackupAssetSearchConfigAndOverlayConfigSnapshotIsCompleteCopiedAndMutationAtomic(t *testing.T) {
	service := NewService(setupTestDB(t))
	before, err := service.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatalf("BackupAssetSettingsSnapshot: %v", err)
	}
	if len(before) != len(BackupAssetFoundationSettingKeys()) {
		t.Fatalf("snapshot key count=%d, want %d", len(before), len(BackupAssetFoundationSettingKeys()))
	}
	before["backup_assets.search_candidate_limit"] = "mutated-caller-copy"
	again, err := service.BackupAssetSettingsSnapshot()
	if err != nil {
		t.Fatalf("second BackupAssetSettingsSnapshot: %v", err)
	}
	if again["backup_assets.search_candidate_limit"] != "10000" {
		t.Fatalf("caller mutation corrupted service snapshot: %q", again["backup_assets.search_candidate_limit"])
	}

	firstUpdated := make(chan struct{})
	releaseMutation := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- service.WithBackupAssetMutation(context.Background(), func(map[string]string) error {
			if err := service.Update("backup_assets.search_candidate_limit", "20000"); err != nil {
				return err
			}
			close(firstUpdated)
			<-releaseMutation
			return service.Update("backup_assets.search_page_size_max", "300")
		})
	}()
	<-firstUpdated
	snapshotDone := make(chan map[string]string, 1)
	snapshotErr := make(chan error, 1)
	go func() {
		values, snapshotError := service.BackupAssetSettingsSnapshot()
		if snapshotError != nil {
			snapshotErr <- snapshotError
			return
		}
		snapshotDone <- values
	}()
	select {
	case values := <-snapshotDone:
		t.Fatalf("snapshot observed a mutation mid-transition: %#v", values)
	case err := <-snapshotErr:
		t.Fatalf("snapshot failed during transition: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseMutation)
	if err := <-mutationDone; err != nil {
		t.Fatalf("atomic settings mutation: %v", err)
	}
	select {
	case err := <-snapshotErr:
		t.Fatalf("snapshot after transition: %v", err)
	case values := <-snapshotDone:
		if values["backup_assets.search_candidate_limit"] != "20000" || values["backup_assets.search_page_size_max"] != "300" {
			t.Fatalf("snapshot observed half transition: %#v", values)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot remained blocked after settings transition")
	}
}

func backupAssetFoundationValuesForTest() map[string]string {
	values := make(map[string]string)
	for _, def := range registry {
		if strings.HasPrefix(def.Key, "backup_assets.") {
			values[def.Key] = def.CodeDefault
		}
	}
	return values
}

func cloneSettingsValues(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func TestAnomalyDefaults_AreConservativeAndAlertsOff(t *testing.T) {
	svc := NewService(setupTestDB(t))
	cases := map[string]string{
		"anomaly.enabled":           "true",
		"anomaly.alerts_enabled":    "false",
		"anomaly.ewma_sigma":        "5.0",
		"anomaly.ewma_window_hours": "6",
		"anomaly.ewma_min_samples":  "24",
	}
	for key, want := range cases {
		if got := svc.GetEffective(key); got != want {
			t.Errorf("%s default = %q, want %q", key, got, want)
		}
	}
}

func TestSensitiveSettingPersistsEncryptedAndReadsPlaintext(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	db := setupTestDB(t)
	svc := NewService(db)

	if err := svc.Update("smtp.password", "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY"); err != nil {
		t.Fatalf("update sensitive setting: %v", err)
	}
	var row model.SystemSetting
	if err := db.First(&row, "key = ?", "smtp.password").Error; err != nil {
		t.Fatalf("load stored setting: %v", err)
	}
	if !strings.HasPrefix(row.Value, "enc:v2:") || strings.Contains(row.Value, "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY") {
		t.Fatalf("expected encrypted stored value, got %q", row.Value)
	}
	if got := svc.GetEffective("smtp.password"); got != "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY" {
		t.Fatalf("expected decrypted effective value, got %q", got)
	}
	all, err := svc.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if got := all["smtp.password"].Value; got != "FAKE_SMTP_PASSWORD_FOR_TEST_ONLY" {
		t.Fatalf("expected decrypted GetAll value, got %q", got)
	}
}

func TestSensitiveSettingEmptyValuePersistsWithoutEncryption(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	db := setupTestDB(t)
	svc := NewService(db)

	if err := svc.Update("smtp.password", ""); err != nil {
		t.Fatalf("update empty sensitive setting: %v", err)
	}
	var row model.SystemSetting
	if err := db.First(&row, "key = ?", "smtp.password").Error; err != nil {
		t.Fatalf("load stored setting: %v", err)
	}
	if row.Value != "" {
		t.Fatalf("empty sensitive setting should stay empty, got %q", row.Value)
	}
}

func TestGetEffectiveDBErrorKeepsExpiredCache(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	svc.cache["login.rate_limit"] = cachedValue{value: "77", expiresAt: time.Now().Add(-time.Minute)}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if got := svc.GetEffective("login.rate_limit"); got != "77" {
		t.Fatalf("DB error should return stale cached value, got %q", got)
	}
}

func TestGetEffective_Default(t *testing.T) {
	svc := NewService(setupTestDB(t))
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Errorf("expected '10', got '%s'", val)
	}
}

func TestGetEffective_EnvOverride(t *testing.T) {
	t.Setenv("LOGIN_RATE_LIMIT", "20")
	svc := NewService(setupTestDB(t))
	val := svc.GetEffective("login.rate_limit")
	if val != "20" {
		t.Errorf("expected '20', got '%s'", val)
	}
}

func TestGetEffective_DBOverride(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	if err := svc.Update("login.rate_limit", "30"); err != nil {
		t.Fatal(err)
	}
	val := svc.GetEffective("login.rate_limit")
	if val != "30" {
		t.Errorf("expected '30', got '%s'", val)
	}
}

func TestGetEffective_DBOverridesEnv(t *testing.T) {
	t.Setenv("LOGIN_RATE_LIMIT", "20")
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "30")
	val := svc.GetEffective("login.rate_limit")
	if val != "30" {
		t.Errorf("expected DB value '30' to override env '20', got '%s'", val)
	}
}

func TestUpdate_Invalid(t *testing.T) {
	svc := NewService(setupTestDB(t))
	if err := svc.Update("login.rate_limit", "abc"); err == nil {
		t.Error("expected error for non-integer value")
	}
	if err := svc.Update("unknown.key", "1"); err == nil {
		t.Error("expected error for unknown key")
	}
}

func TestUpdate_SecurityFloor(t *testing.T) {
	svc := NewService(setupTestDB(t))
	// login.rate_limit Min=5
	if err := svc.Update("login.rate_limit", "2"); err == nil {
		t.Error("expected error: rate_limit below security floor of 5")
	}
	// login.fail_lock_threshold Min=3
	if err := svc.Update("login.fail_lock_threshold", "1"); err == nil {
		t.Error("expected error: lock threshold below security floor of 3")
	}
	// login.rate_window MinDuration=10s
	if err := svc.Update("login.rate_window", "5s"); err == nil {
		t.Error("expected error: rate_window below 10s floor")
	}
	// login.fail_lock_duration MinDuration=1m
	if err := svc.Update("login.fail_lock_duration", "30s"); err == nil {
		t.Error("expected error: lock_duration below 1m floor")
	}
}

func TestUpdate_ValueTooLong(t *testing.T) {
	svc := NewService(setupTestDB(t))
	longVal := make([]byte, maxValueLength+1)
	for i := range longVal {
		longVal[i] = '1'
	}
	if err := svc.Update("login.rate_limit", string(longVal)); err == nil {
		t.Error("expected error for value exceeding max length")
	}
}

func TestValidate_Bool(t *testing.T) {
	svc := NewService(setupTestDB(t))
	if err := svc.Validate("login.captcha_enabled", "true"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.Validate("login.captcha_enabled", "false"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.Validate("login.captcha_enabled", "yes"); err == nil {
		t.Error("expected error for non-bool value")
	}
}

func TestValidate_Duration(t *testing.T) {
	svc := NewService(setupTestDB(t))
	if err := svc.Validate("alert.dedup_window", "5m"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := svc.Validate("alert.dedup_window", "-1m"); err == nil {
		t.Error("expected error for negative duration")
	}
	if err := svc.Validate("alert.dedup_window", "invalid"); err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestDelete(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "30")
	if err := svc.Delete("login.rate_limit"); err != nil {
		t.Fatal(err)
	}
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Errorf("expected default '10' after delete, got '%s'", val)
	}
}

func TestGetAll(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "25")
	all, err := svc.GetAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(registry) {
		t.Errorf("expected %d settings, got %d", len(registry), len(all))
	}
	if all["login.rate_limit"].Source != "db" {
		t.Errorf("expected source 'db', got '%s'", all["login.rate_limit"].Source)
	}
	if all["login.rate_limit"].Value != "25" {
		t.Errorf("expected '25', got '%s'", all["login.rate_limit"].Value)
	}
}

func TestCache_InvalidatedOnUpdate(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	// Prime cache
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Fatalf("expected '10', got '%s'", val)
	}
	// Update should invalidate cache
	_ = svc.Update("login.rate_limit", "50")
	val = svc.GetEffective("login.rate_limit")
	if val != "50" {
		t.Errorf("expected '50' after update, got '%s' (cache not invalidated?)", val)
	}
}

func TestCache_InvalidatedOnDelete(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db)
	_ = svc.Update("login.rate_limit", "50")
	// Prime cache with DB value
	_ = svc.GetEffective("login.rate_limit")
	// Delete should invalidate cache
	_ = svc.Delete("login.rate_limit")
	val := svc.GetEffective("login.rate_limit")
	if val != "10" {
		t.Errorf("expected default '10' after delete, got '%s' (cache not invalidated?)", val)
	}
}
