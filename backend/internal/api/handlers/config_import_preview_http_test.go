package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
)

// These DTOs intentionally model only the public JSON contract.  The handler
// keeps its implementation DTOs private; testing the wire shape here avoids
// making the tests pass because an internal field happened to be populated.
type configImportPreviewHTTPEnvelope struct {
	Data configImportPreviewHTTPDTO `json:"data"`
}

type configImportPreviewHTTPDTO struct {
	Conflict                string                          `json:"conflict"`
	Basis                   string                          `json:"basis"`
	Entities                []configImportPreviewHTTPEntity `json:"entities"`
	Warnings                []configImportPreviewWarning    `json:"warnings"`
	PendingChecks           []configImportPreviewPending    `json:"pending_checks"`
	PendingChecksTruncated  int                             `json:"pending_checks_truncated"`
	WarningsTruncated       int                             `json:"warnings_truncated"`
	SystemSettings          configImportPreviewSettings     `json:"system_settings"`
	Secrets                 []configImportPreviewHTTPSecret `json:"secrets"`
	SecretsTruncated        int                             `json:"secrets_truncated"`
	AssetGraph              []configImportPreviewHTTPAsset  `json:"asset_graph"`
	RequiresApplyValidation bool                            `json:"requires_apply_validation"`
}

type configImportPreviewHTTPEntity struct {
	Entity            string `json:"entity"`
	Input             int    `json:"input"`
	Created           int    `json:"created"`
	Updated           int    `json:"updated"`
	Skipped           int    `json:"skipped"`
	Rejected          int    `json:"rejected"`
	PendingValidation int    `json:"pending_validation"`
	DisabledImported  int    `json:"disabled_imported"`
}

type configImportPreviewWarning struct {
	Entity string `json:"entity"`
	Index  int    `json:"index"`
	Name   string `json:"name,omitempty"`
	Code   string `json:"code"`
}

type configImportPreviewPending struct {
	Entity string `json:"entity"`
	Index  int    `json:"index"`
	Code   string `json:"code"`
}

type configImportPreviewSettings struct {
	WillWrite     bool `json:"will_write"`
	Created       int  `json:"created"`
	Updated       int  `json:"updated"`
	AppliesInSkip bool `json:"applies_in_skip"`
}

type configImportPreviewHTTPSecret struct {
	Entity  string `json:"entity"`
	Index   int    `json:"index"`
	Field   string `json:"field"`
	Present bool   `json:"present"`
	Action  string `json:"action"`
}

type configImportPreviewHTTPAsset struct {
	Entity string `json:"entity"`
	Input  int    `json:"input"`
	Status string `json:"status"`
}

func serveConfigImportPreviewHTTP(
	t *testing.T,
	db *gorm.DB,
	service *settings.Service,
	conflict string,
	body []byte,
	ctx context.Context,
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := NewConfigHandler(db, service)
	router := gin.New()
	router.POST("/config/import/preview", handler.PreviewImport)
	path := "/config/import/preview?conflict=" + conflict
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeConfigImportPreviewHTTP(t *testing.T, response *httptest.ResponseRecorder) configImportPreviewHTTPDTO {
	t.Helper()
	var envelope configImportPreviewHTTPEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode preview response status=%d: %v; body=%s", response.Code, err, response.Body.String())
	}
	return envelope.Data
}

func configImportPreviewHTTPDB(t *testing.T, engine string) (*gorm.DB, *gorm.DB) {
	t.Helper()
	setConfigHandlerTestEncryption(t)
	primary, secondary := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, primary)
	return primary, secondary
}

func runConfigImportPreviewHTTPBothEngines(t *testing.T, test func(*testing.T, *gorm.DB, *gorm.DB)) {
	t.Helper()
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			primary, secondary := configImportPreviewHTTPDB(t, engine)
			test(t, primary, secondary)
		})
	}
}

func previewHTTPEntity(t *testing.T, dto configImportPreviewHTTPDTO, entity string) configImportPreviewHTTPEntity {
	t.Helper()
	for _, item := range dto.Entities {
		if item.Entity == entity {
			return item
		}
	}
	t.Fatalf("preview response omitted entity %q: %+v", entity, dto.Entities)
	return configImportPreviewHTTPEntity{}
}

func previewHTTPAsset(t *testing.T, dto configImportPreviewHTTPDTO, entity string) configImportPreviewHTTPAsset {
	t.Helper()
	for _, item := range dto.AssetGraph {
		if item.Entity == entity {
			return item
		}
	}
	t.Fatalf("preview response omitted asset entity %q: %+v", entity, dto.AssetGraph)
	return configImportPreviewHTTPAsset{}
}

func assertPreviewHTTPFixedEntityOrder(t *testing.T, dto configImportPreviewHTTPDTO) {
	t.Helper()
	want := []string{"nodes", "ssh_keys", "policies", "tasks", "system_settings"}
	if len(dto.Entities) != len(want) {
		t.Fatalf("entity count=%d, want %d: %+v", len(dto.Entities), len(want), dto.Entities)
	}
	for index, entity := range want {
		if dto.Entities[index].Entity != entity {
			t.Fatalf("entity[%d]=%q, want %q", index, dto.Entities[index].Entity, entity)
		}
		for _, value := range []int{
			dto.Entities[index].Input,
			dto.Entities[index].Created,
			dto.Entities[index].Updated,
			dto.Entities[index].Skipped,
			dto.Entities[index].Rejected,
			dto.Entities[index].PendingValidation,
			dto.Entities[index].DisabledImported,
		} {
			if value < 0 {
				t.Fatalf("entity[%d] contains a negative count: %+v", index, dto.Entities[index])
			}
		}
	}
}

func previewHTTPCoreCounts(t *testing.T, db *gorm.DB) map[string]int64 {
	t.Helper()
	counts := make(map[string]int64, 6)
	for _, name := range []string{"nodes", "ssh_keys", "policies", "tasks", "system_settings", "credential_audit_events"} {
		var count int64
		if err := db.Table(name).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		counts[name] = count
	}
	return counts
}

func assertPreviewHTTPFileUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preview fixture file after request: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("preview request changed fixture file %q", path)
	}
}

func TestConfigImportPreviewHTTPBodyValidationBothEngines(t *testing.T) {
	runConfigImportPreviewHTTPBothEngines(t, func(t *testing.T, db, _ *gorm.DB) {
		before := previewHTTPCoreCounts(t, db)
		knownHosts := t.TempDir() + "/known_hosts"
		fileBefore := []byte("preview-http-sentinel\n")
		if err := os.WriteFile(knownHosts, fileBefore, 0o600); err != nil {
			t.Fatalf("write file fixture: %v", err)
		}
		cases := []struct {
			name string
			body []byte
		}{
			{name: "empty", body: nil},
			{name: "malformed", body: []byte(`{"data":`)},
			{name: "trailing-json", body: []byte(`{"data":{}} {}`)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				response := serveConfigImportPreviewHTTP(t, db, nil, "skip", tc.body, nil)
				if response.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s, want 400", response.Code, response.Body.String())
				}
				if got := previewHTTPCoreCounts(t, db); !reflect.DeepEqual(got, before) {
					t.Fatalf("malformed preview changed database: before=%v after=%v", before, got)
				}
				assertPreviewHTTPFileUnchanged(t, knownHosts, fileBefore)
			})
		}

		valid := []byte(`{"data":{}}`)
		oversized := append(append([]byte(nil), valid...), bytes.Repeat([]byte{' '}, (10<<20)-len(valid)+1)...)
		response := serveConfigImportPreviewHTTP(t, db, nil, "skip", oversized, nil)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized status=%d body=%s, want safe 400/413", response.Code, response.Body.String())
		}
		if got := previewHTTPCoreCounts(t, db); !reflect.DeepEqual(got, before) {
			t.Fatalf("oversized preview changed database: before=%v after=%v", before, got)
		}
		assertPreviewHTTPFileUnchanged(t, knownHosts, fileBefore)
	})
}

func TestConfigImportPreviewHTTPEmptyDTOAndSettingsSkipBothEngines(t *testing.T) {
	runConfigImportPreviewHTTPBothEngines(t, func(t *testing.T, db, _ *gorm.DB) {
		service := settings.NewService(db)
		if err := db.Create(&model.SystemSetting{Key: "storage.min_free_gb", Value: "7"}).Error; err != nil {
			t.Fatalf("seed existing setting: %v", err)
		}
		before := previewHTTPCoreCounts(t, db)

		emptyResponse := serveConfigImportPreviewHTTP(t, db, service, "skip", []byte(`{}`), nil)
		if emptyResponse.Code != http.StatusOK {
			t.Fatalf("empty preview status=%d body=%s", emptyResponse.Code, emptyResponse.Body.String())
		}
		empty := decodeConfigImportPreviewHTTP(t, emptyResponse)
		assertPreviewHTTPFixedEntityOrder(t, empty)
		if empty.Conflict != "skip" || empty.Basis != "current_snapshot" || !empty.RequiresApplyValidation {
			t.Fatalf("empty preview header=%+v", empty)
		}
		for _, entity := range empty.Entities {
			if entity.Input != 0 || entity.Created != 0 || entity.Updated != 0 || entity.Skipped != 0 || entity.Rejected != 0 || entity.PendingValidation != 0 || entity.DisabledImported != 0 {
				t.Fatalf("empty preview reported impact: %+v", entity)
			}
		}
		for _, entity := range []string{"backup_repositories", "task_repository_links", "backup_retention_policies", "recovery_point_holds"} {
			asset := previewHTTPAsset(t, empty, entity)
			if asset.Input != 0 || asset.Status != "not_present" {
				t.Fatalf("empty asset=%+v", asset)
			}
		}

		body := []byte(`{"system_settings":[{"key":"storage.min_free_gb","value":"42"}]}`)
		response := serveConfigImportPreviewHTTP(t, db, service, "skip", body, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("settings preview status=%d body=%s", response.Code, response.Body.String())
		}
		dto := decodeConfigImportPreviewHTTP(t, response)
		settingEntity := previewHTTPEntity(t, dto, "system_settings")
		if settingEntity.Input != 1 || settingEntity.Created != 0 || settingEntity.Updated != 1 || settingEntity.Skipped != 0 || settingEntity.Rejected != 0 || settingEntity.PendingValidation != 0 {
			t.Fatalf("settings entity=%+v", settingEntity)
		}
		if !dto.SystemSettings.WillWrite || dto.SystemSettings.Created != 0 || dto.SystemSettings.Updated != 1 || !dto.SystemSettings.AppliesInSkip {
			t.Fatalf("settings DTO=%+v", dto.SystemSettings)
		}
		var stored model.SystemSetting
		if err := db.Where("key = ?", "storage.min_free_gb").First(&stored).Error; err != nil {
			t.Fatalf("reload existing setting: %v", err)
		}
		if stored.Value != "7" {
			t.Fatalf("preview mutated setting value=%q", stored.Value)
		}
		if got := previewHTTPCoreCounts(t, db); !reflect.DeepEqual(got, before) {
			t.Fatalf("preview changed database: before=%v after=%v", before, got)
		}
	})
}

func TestConfigImportPreviewHTTPV1AndV2AssetsBothEngines(t *testing.T) {
	runConfigImportPreviewHTTPBothEngines(t, func(t *testing.T, db, _ *gorm.DB) {
		v1 := []byte(`{"version":"1.0","data":{"backup_repositories":[{"repository_ref":"v1-repository","provider_kind":"not-used","display_name":"v1","version_mode":"ignored","immutability_level":"ignored"}]}}`)
		response := serveConfigImportPreviewHTTP(t, db, nil, "skip", v1, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("v1 preview status=%d body=%s", response.Code, response.Body.String())
		}
		v1DTO := decodeConfigImportPreviewHTTP(t, response)
		v1Repository := previewHTTPAsset(t, v1DTO, "backup_repositories")
		if v1Repository.Input != 1 || v1Repository.Status != "unsupported" {
			t.Fatalf("v1 repository asset=%+v", v1Repository)
		}
		for _, entity := range []string{"task_repository_links", "backup_retention_policies", "recovery_point_holds"} {
			asset := previewHTTPAsset(t, v1DTO, entity)
			if asset.Input != 0 || asset.Status != "not_present" {
				t.Fatalf("v1 empty asset %s=%+v", entity, asset)
			}
		}

		v2 := []byte(`{"version":"2.0","document_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","data":{"backup_repositories":[{"repository_ref":"v2-repository","provider_kind":"restic","display_name":"v2","version_mode":"native_snapshot","immutability_level":"xirang_managed"}]}}`)
		response = serveConfigImportPreviewHTTP(t, db, nil, "overwrite", v2, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("v2 preview status=%d body=%s", response.Code, response.Body.String())
		}
		v2DTO := decodeConfigImportPreviewHTTP(t, response)
		if v2DTO.Conflict != "overwrite" || v2DTO.Basis != "current_snapshot" {
			t.Fatalf("v2 header=%+v", v2DTO)
		}
		v2Repository := previewHTTPAsset(t, v2DTO, "backup_repositories")
		if v2Repository.Input != 1 || v2Repository.Status != "unknown" {
			t.Fatalf("v2 repository asset=%+v", v2Repository)
		}
		for _, entity := range []string{"task_repository_links", "backup_retention_policies", "recovery_point_holds"} {
			asset := previewHTTPAsset(t, v2DTO, entity)
			if asset.Input != 0 || asset.Status != "not_present" {
				t.Fatalf("v2 empty asset %s=%+v", entity, asset)
			}
		}
	})
}

func TestConfigImportPreviewHTTPWarningsTruncatedBothEngines(t *testing.T) {
	runConfigImportPreviewHTTPBothEngines(t, func(t *testing.T, db, _ *gorm.DB) {
		keys := make([]any, 105)
		for index := range keys {
			keys[index] = map[string]any{
				"name":        fmt.Sprintf("preview-invalid-key-%03d", index),
				"username":    "preview-user",
				"private_key": "not-a-private-key",
			}
		}
		body, err := json.Marshal(map[string]any{"ssh_keys": keys})
		if err != nil {
			t.Fatalf("marshal warning fixture: %v", err)
		}
		before := previewHTTPCoreCounts(t, db)
		response := serveConfigImportPreviewHTTP(t, db, nil, "skip", body, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("warning preview status=%d body=%s", response.Code, response.Body.String())
		}
		dto := decodeConfigImportPreviewHTTP(t, response)
		entity := previewHTTPEntity(t, dto, "ssh_keys")
		if entity.Input != 105 || entity.Rejected != 105 || entity.Created != 0 || entity.Updated != 0 || entity.Skipped != 0 || entity.PendingValidation != 0 {
			t.Fatalf("warning entity=%+v", entity)
		}
		if len(dto.Warnings) != 100 || dto.WarningsTruncated != 5 {
			t.Fatalf("warnings=%d truncated=%d", len(dto.Warnings), dto.WarningsTruncated)
		}
		for _, warning := range dto.Warnings {
			if warning.Entity != "ssh_keys" || warning.Code != "invalid_private_key" || warning.Index < 0 || warning.Index >= 105 {
				t.Fatalf("unsafe warning=%+v", warning)
			}
		}
		if strings.Contains(response.Body.String(), "not-a-private-key") {
			t.Fatal("preview response leaked invalid private-key material")
		}
		if got := previewHTTPCoreCounts(t, db); !reflect.DeepEqual(got, before) {
			t.Fatalf("warning preview changed database: before=%v after=%v", before, got)
		}
	})
}

func TestConfigImportPreviewHTTPPendingDTOAndSecretsBothEngines(t *testing.T) {
	runConfigImportPreviewHTTPBothEngines(t, func(t *testing.T, db, _ *gorm.DB) {
		key := createCorruptedPreviewSSHKey(t, db, "preview-http-retained-secret")
		body := []byte(`{"ssh_keys":[{"name":"preview-http-retained-secret","username":"replacement-user"}]}`)
		before := previewHTTPCoreCounts(t, db)
		response := serveConfigImportPreviewHTTP(t, db, settings.NewService(db), "overwrite", body, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("pending preview status=%d body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "not-valid-base64") {
			t.Fatalf("pending preview leaked retained-secret material: %s", response.Body.String())
		}
		dto := decodeConfigImportPreviewHTTP(t, response)
		entity := previewHTTPEntity(t, dto, "ssh_keys")
		if entity.Input != 1 || entity.PendingValidation != 1 || entity.Created != 0 || entity.Updated != 0 || entity.Rejected != 0 || entity.Skipped != 0 {
			t.Fatalf("pending entity=%+v", entity)
		}
		if len(dto.PendingChecks) != 1 || dto.PendingChecks[0].Entity != "ssh_keys" || dto.PendingChecks[0].Index != 0 || dto.PendingChecks[0].Code != "retained_secret" || dto.PendingChecksTruncated != 0 {
			t.Fatalf("pending checks=%+v truncated=%d", dto.PendingChecks, dto.PendingChecksTruncated)
		}
		foundSecret := false
		for _, secret := range dto.Secrets {
			if secret.Entity == "ssh_keys" && secret.Index == 0 && secret.Field == "private_key" {
				foundSecret = true
				if secret.Present || secret.Action != "preserved" {
					t.Fatalf("retained secret DTO=%+v", secret)
				}
			}
		}
		if !foundSecret {
			t.Fatalf("pending DTO omitted retained-secret presence: %+v", dto.Secrets)
		}
		if !dto.RequiresApplyValidation {
			t.Fatal("pending preview must require apply validation")
		}
		var storedUsername string
		if err := db.Table("ssh_keys").Select("username").Where("id = ?", key.ID).Scan(&storedUsername).Error; err != nil {
			t.Fatalf("reload retained key metadata: %v", err)
		}
		if storedUsername != "preview-user" {
			t.Fatalf("preview mutated retained key username=%q", storedUsername)
		}
		if got := previewHTTPCoreCounts(t, db); !reflect.DeepEqual(got, before) {
			t.Fatalf("pending preview changed database: before=%v after=%v", before, got)
		}
	})
}

func TestConfigImportPreviewHTTPQueryFailureIsSafeBothEngines(t *testing.T) {
	runConfigImportPreviewHTTPBothEngines(t, func(t *testing.T, db, _ *gorm.DB) {
		key := model.SSHKey{Name: "preview-http-query-failure", Username: "preview-user"}
		if err := db.Create(&key).Error; err != nil {
			t.Fatalf("seed query-failure key: %v", err)
		}
		before := previewHTTPCoreCounts(t, db)
		injected := errors.New("CONFIG_IMPORT_PREVIEW_HTTP_QUERY_FAILURE")
		callbackName := "test:config-import-preview-http-query-failure"
		queryFailed := false
		if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "ssh_keys" {
				return
			}
			queryFailed = true
			_ = tx.AddError(injected)
		}); err != nil {
			t.Fatalf("register query-failure callback: %v", err)
		}
		response := serveConfigImportPreviewHTTP(t, db, nil, "skip", []byte(`{"ssh_keys":[{"name":"preview-http-query-failure"}]}`), nil)
		_ = db.Callback().Query().Remove(callbackName)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("query failure status=%d body=%s, want 503", response.Code, response.Body.String())
		}
		if !queryFailed {
			t.Fatalf("query-failure callback did not observe SSH-key inventory query")
		}
		if strings.Contains(response.Body.String(), injected.Error()) {
			t.Fatalf("query failure leaked internal error: %s", response.Body.String())
		}
		if got := previewHTTPCoreCounts(t, db); !reflect.DeepEqual(got, before) {
			t.Fatalf("query failure changed database: before=%v after=%v", before, got)
		}
	})
}

func TestConfigImportPreviewHTTPRequestCancellationIsSafeBothEngines(t *testing.T) {
	runConfigImportPreviewHTTPBothEngines(t, func(t *testing.T, db, _ *gorm.DB) {
		before := previewHTTPCoreCounts(t, db)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		response := serveConfigImportPreviewHTTP(t, db, nil, "skip", []byte(`{"nodes":[{"name":"cancelled-preview-node","host":"10.90.0.1","username":"root","auth_type":"key"}]}`), ctx)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("cancelled preview status=%d body=%s, want 503", response.Code, response.Body.String())
		}
		if got := previewHTTPCoreCounts(t, db); !reflect.DeepEqual(got, before) {
			t.Fatalf("cancelled preview changed database: before=%v after=%v", before, got)
		}
	})
}

func TestConfigImportPreviewHTTPChangedBaselineReevaluatesBothEngines(t *testing.T) {
	runConfigImportPreviewHTTPBothEngines(t, func(t *testing.T, db, secondary *gorm.DB) {
		node := model.Node{Name: "preview-http-changing-node", Host: "10.91.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "preview-http-changing-node"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("seed changing node: %v", err)
		}
		body := []byte(`{"nodes":[{"name":"preview-http-changing-node","host":"10.91.0.2","username":"root","auth_type":"key"}]}`)

		first := serveConfigImportPreviewHTTP(t, db, nil, "skip", body, nil)
		if first.Code != http.StatusOK {
			t.Fatalf("first changed-baseline preview status=%d body=%s", first.Code, first.Body.String())
		}
		firstDTO := decodeConfigImportPreviewHTTP(t, first)
		firstNode := previewHTTPEntity(t, firstDTO, "nodes")
		if firstNode.Input != 1 || firstNode.Skipped != 1 || firstNode.Created != 0 || firstNode.Updated != 0 || firstNode.Rejected != 0 {
			t.Fatalf("first changed-baseline entity=%+v", firstNode)
		}

		if err := secondary.Where("name = ?", node.Name).Delete(&model.Node{}).Error; err != nil {
			t.Fatalf("delete changing node through second connection: %v", err)
		}
		second := serveConfigImportPreviewHTTP(t, db, nil, "skip", body, nil)
		if second.Code != http.StatusOK {
			t.Fatalf("second changed-baseline preview status=%d body=%s", second.Code, second.Body.String())
		}
		secondDTO := decodeConfigImportPreviewHTTP(t, second)
		secondNode := previewHTTPEntity(t, secondDTO, "nodes")
		if secondNode.Input != 1 || secondNode.Created != 1 || secondNode.Updated != 0 || secondNode.Skipped != 0 || secondNode.Rejected != 0 {
			t.Fatalf("second changed-baseline entity=%+v", secondNode)
		}
	})
}

func TestConfigImportPreviewHTTPRejectsInvalidV2AssetsBothEngines(t *testing.T) {
	runConfigImportPreviewHTTPBothEngines(t, func(t *testing.T, db, _ *gorm.DB) {
		before := previewHTTPCoreCounts(t, db)
		body := []byte(`{"version":"2.0","document_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","data":{"recovery_point_holds":[{"hold_ref":"hold-1","repository_ref":"repository-1","hold_type":"legal","state":"active"}]}}`)
		response := serveConfigImportPreviewHTTP(t, db, nil, "skip", body, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid v2 asset status=%d body=%s, want 400", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "repository-1") || strings.Contains(response.Body.String(), "hold-1") {
			t.Fatalf("invalid v2 asset response leaked input details: %s", response.Body.String())
		}
		if got := previewHTTPCoreCounts(t, db); !reflect.DeepEqual(got, before) {
			t.Fatalf("invalid v2 asset preview changed database: before=%v after=%v", before, got)
		}
	})
}
