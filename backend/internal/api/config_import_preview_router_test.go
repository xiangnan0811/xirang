package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
)

const configImportPreviewRouterPath = "/api/v1/config/import/preview?conflict=skip"

type configImportPreviewRouterFixture struct {
	db         *gorm.DB
	writer     *gorm.DB
	router     *gin.Engine
	jwt        *auth.JWTManager
	tokens     map[string]string
	knownHosts string
}

type configImportPreviewRouterEntity struct {
	Entity            string `json:"entity"`
	Input             int    `json:"input"`
	Created           int    `json:"created"`
	Updated           int    `json:"updated"`
	Skipped           int    `json:"skipped"`
	Rejected          int    `json:"rejected"`
	PendingValidation int    `json:"pending_validation"`
	DisabledImported  int    `json:"disabled_imported"`
}

type configImportPreviewRouterData struct {
	Conflict                string                            `json:"conflict"`
	Basis                   string                            `json:"basis"`
	Entities                []configImportPreviewRouterEntity `json:"entities"`
	Warnings                []map[string]any                  `json:"warnings"`
	PendingChecks           []map[string]any                  `json:"pending_checks"`
	PendingChecksTruncated  int                               `json:"pending_checks_truncated"`
	WarningsTruncated       int                               `json:"warnings_truncated"`
	SystemSettings          map[string]any                    `json:"system_settings"`
	Secrets                 []map[string]any                  `json:"secrets"`
	SecretsTruncated        int                               `json:"secrets_truncated"`
	AssetGraph              []map[string]any                  `json:"asset_graph"`
	RequiresApplyValidation bool                              `json:"requires_apply_validation"`
}

type configImportPreviewRouterEnvelope struct {
	Data configImportPreviewRouterData `json:"data"`
}
type configImportPreviewRouterGrantEnvelope struct {
	Data struct {
		ID      uint   `json:"id"`
		Action  string `json:"action"`
		Purpose string `json:"purpose"`
		Status  string `json:"status"`
	} `json:"data"`
}

func setupConfigImportPreviewRouterFixture(t *testing.T) configImportPreviewRouterFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_CONFIG_PREVIEW_ROUTER_ENCRYPTION_KEY_FOR_TEST_ONLY")
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "config-preview-router.sqlite")
	dsn := dbPath + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&_synchronous=NORMAL&_txlock=immediate&_loc=UTC"
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatalf("open preview router SQLite database: %v", err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("get preview router SQLite pool: %v", err)
		}
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	db := open()
	writer := open()
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatalf("run production preview router migrations: %v", err)
	}

	manager := auth.NewJWTManager("FAKE_CONFIG_PREVIEW_ROUTER_JWT_SECRET_FOR_TEST_ONLY", time.Hour)
	manager.SetDB(db)
	tokens := make(map[string]string, 5)
	for _, role := range []string{"admin", "operator", "viewer"} {
		user := model.User{Username: "config-preview-" + role, PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: role}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create %s preview user: %v", role, err)
		}
		token, err := manager.GenerateToken(user)
		if err != nil {
			t.Fatalf("generate %s preview token: %v", role, err)
		}
		tokens[role] = token
	}
	revokedUser := model.User{Username: "config-preview-revoked", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}
	if err := db.Create(&revokedUser).Error; err != nil {
		t.Fatalf("create revoked preview user: %v", err)
	}
	revokedToken, err := manager.GenerateToken(revokedUser)
	if err != nil {
		t.Fatalf("generate revoked preview token: %v", err)
	}
	claims, err := manager.ParseToken(revokedToken)
	if err != nil {
		t.Fatalf("parse revoked preview token: %v", err)
	}
	if err := manager.RevokeSession(claims.ID, claims.UserID, claims.ExpiresAt.Time); err != nil {
		t.Fatalf("revoke preview session: %v", err)
	}
	tokens["revoked"] = revokedToken
	tokens["invalid"] = "not-a-valid-jwt"

	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	knownHostsBytes := []byte("router-preview-known-host-sentinel\n")
	if err := os.WriteFile(knownHosts, knownHostsBytes, 0o600); err != nil {
		t.Fatalf("write known_hosts fixture: %v", err)
	}
	t.Setenv("SSH_KNOWN_HOSTS_PATH", knownHosts)

	router := NewRouter(Dependencies{DB: db, JWTManager: manager, SettingsService: settings.NewService(db)})
	return configImportPreviewRouterFixture{db: db, writer: writer, router: router, jwt: manager, tokens: tokens, knownHosts: knownHosts}
}

func configImportPreviewRouterRequest(
	t *testing.T,
	router http.Handler,
	method, path, token string,
	body []byte,
	ctx context.Context,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func configImportPreviewRouterRequestWithProof(
	t *testing.T,
	router http.Handler,
	method, path, token, proof string,
	body []byte,
	ctx context.Context,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if proof != "" {
		request.Header.Set("X-Xirang-Step-Up", proof)
	}
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeConfigImportPreviewRouterData(t *testing.T, response *httptest.ResponseRecorder) configImportPreviewRouterData {
	t.Helper()
	var envelope configImportPreviewRouterEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode preview router response status=%d: %v; body=%s", response.Code, err, response.Body.String())
	}
	return envelope.Data
}

func configImportPreviewRouterEntityAt(t *testing.T, data configImportPreviewRouterData, name string) configImportPreviewRouterEntity {
	t.Helper()
	for _, entity := range data.Entities {
		if entity.Entity == name {
			return entity
		}
	}
	t.Fatalf("preview router response omitted entity %q: %+v", name, data.Entities)
	return configImportPreviewRouterEntity{}
}

func configImportPreviewRouterCanonicalValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "<nil>"
	case []byte:
		return "bytes:" + hex.EncodeToString(typed)
	case time.Time:
		return "time:" + typed.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%T:%v", value, value)
	}
}

// configImportPreviewRouterDatabaseSnapshot reads every SQLite table, including
// audit/revocation tables. It is deliberately independent of GORM model hooks:
// an unexpected middleware write must be visible even when a model would hide
// or transform a value on read.
func configImportPreviewRouterDatabaseSnapshot(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	rows, err := db.Raw("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name").Rows()
	if err != nil {
		t.Fatalf("list preview router tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan preview router table name: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read preview router table names: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close preview router table names: %v", err)
	}
	snapshot := make(map[string]string, len(tables))
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		result, err := db.Raw("SELECT * FROM " + quoted).Rows()
		if err != nil {
			t.Fatalf("read preview router table %s: %v", table, err)
		}
		columns, err := result.Columns()
		if err != nil {
			_ = result.Close()
			t.Fatalf("read preview router columns %s: %v", table, err)
		}
		rowValues := make([]string, 0)
		for result.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := result.Scan(pointers...); err != nil {
				_ = result.Close()
				t.Fatalf("scan preview router table %s: %v", table, err)
			}
			encoded := make([]string, len(values))
			for index, value := range values {
				encoded[index] = configImportPreviewRouterCanonicalValue(value)
			}
			rowValues = append(rowValues, strings.Join(encoded, "\x1f"))
		}
		if err := result.Err(); err != nil {
			_ = result.Close()
			t.Fatalf("read preview router table %s rows: %v", table, err)
		}
		if err := result.Close(); err != nil {
			t.Fatalf("close preview router table %s: %v", table, err)
		}
		sort.Strings(rowValues)
		snapshot[table] = strings.Join(rowValues, "\x1e")
	}
	return snapshot
}

func installConfigImportPreviewRouterWriteGate(t *testing.T, db *gorm.DB) *atomic.Int64 {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get preview router DB for write gate: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	connection, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("checkout preview router SQLite connection: %v", err)
	}
	var writes atomic.Int64
	err = connection.Raw(func(driverConnection any) error {
		physical, ok := driverConnection.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("preview router DB returned %T, want *sqlite3.SQLiteConn", driverConnection)
		}
		physical.RegisterAuthorizer(func(op int, _, _, _ string) int {
			switch op {
			case sqlite3.SQLITE_INSERT, sqlite3.SQLITE_UPDATE, sqlite3.SQLITE_DELETE,
				sqlite3.SQLITE_CREATE_INDEX, sqlite3.SQLITE_CREATE_TABLE, sqlite3.SQLITE_CREATE_TRIGGER,
				sqlite3.SQLITE_CREATE_VIEW, sqlite3.SQLITE_CREATE_TEMP_INDEX, sqlite3.SQLITE_CREATE_TEMP_TABLE,
				sqlite3.SQLITE_CREATE_TEMP_TRIGGER, sqlite3.SQLITE_CREATE_TEMP_VIEW, sqlite3.SQLITE_DROP_INDEX,
				sqlite3.SQLITE_DROP_TABLE, sqlite3.SQLITE_DROP_TRIGGER, sqlite3.SQLITE_DROP_VIEW,
				sqlite3.SQLITE_DROP_TEMP_INDEX, sqlite3.SQLITE_DROP_TEMP_TABLE, sqlite3.SQLITE_DROP_TEMP_TRIGGER,
				sqlite3.SQLITE_DROP_TEMP_VIEW, sqlite3.SQLITE_ALTER_TABLE, sqlite3.SQLITE_REINDEX,
				sqlite3.SQLITE_ATTACH, sqlite3.SQLITE_DETACH:
				writes.Add(1)
				return sqlite3.SQLITE_DENY
			default:
				return sqlite3.SQLITE_OK
			}
		})
		return nil
	})
	closeErr := connection.Close()
	if err != nil {
		t.Fatalf("install preview router SQLite write gate: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("return preview router SQLite connection: %v", closeErr)
	}
	return &writes
}

func previewRouterFixtureFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preview router fixture file: %v", err)
	}
	return data
}

func assertConfigImportPreviewRouterReadOnly(t *testing.T, fixture configImportPreviewRouterFixture, before map[string]string, fileBefore []byte, writes *atomic.Int64) {
	t.Helper()
	after := configImportPreviewRouterDatabaseSnapshot(t, fixture.db)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("preview changed a persistent table: before=%v after=%v", before, after)
	}
	if got := previewRouterFixtureFile(t, fixture.knownHosts); !bytes.Equal(fileBefore, got) {
		t.Fatalf("preview changed known_hosts fixture: before=%q after=%q", fileBefore, got)
	}
	if writes != nil && writes.Load() != 0 {
		t.Fatalf("preview attempted %d SQLite write operations", writes.Load())
	}
}

func TestConfigImportPreviewRouterAdminWithoutProtectionIsReadOnly(t *testing.T) {
	fixture := setupConfigImportPreviewRouterFixture(t)
	writes := installConfigImportPreviewRouterWriteGate(t, fixture.db)
	before := configImportPreviewRouterDatabaseSnapshot(t, fixture.db)
	fileBefore := previewRouterFixtureFile(t, fixture.knownHosts)
	body := []byte(`{"nodes":[{"name":"router-preview-new-node","host":"10.100.0.1","port":22,"username":"root","auth_type":"key"}],"system_settings":[{"key":"storage.min_free_gb","value":"42"}]}`)

	response := configImportPreviewRouterRequest(t, fixture.router, http.MethodPost, configImportPreviewRouterPath, fixture.tokens["admin"], body, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("admin preview status=%d body=%s", response.Code, response.Body.String())
	}
	data := decodeConfigImportPreviewRouterData(t, response)
	if data.Conflict != "skip" || data.Basis != "current_snapshot" || !data.RequiresApplyValidation {
		t.Fatalf("admin preview header=%+v", data)
	}
	nodes := configImportPreviewRouterEntityAt(t, data, "nodes")
	if nodes.Input != 1 || nodes.Created != 1 || nodes.Updated != 0 || nodes.Skipped != 0 || nodes.Rejected != 0 || nodes.PendingValidation != 0 {
		t.Fatalf("admin preview node counts=%+v", nodes)
	}
	settingsEntity := configImportPreviewRouterEntityAt(t, data, "system_settings")
	if settingsEntity.Input != 1 || settingsEntity.Created != 1 || settingsEntity.Updated != 0 || settingsEntity.Skipped != 0 || settingsEntity.Rejected != 0 {
		t.Fatalf("admin preview settings counts=%+v", settingsEntity)
	}
	assertConfigImportPreviewRouterReadOnly(t, fixture, before, fileBefore, writes)
}

func TestConfigImportPreviewRouterRolesAndInvalidSessions(t *testing.T) {
	fixture := setupConfigImportPreviewRouterFixture(t)
	writes := installConfigImportPreviewRouterWriteGate(t, fixture.db)
	body := []byte(`{"nodes":[{"name":"router-preview-role-node","host":"10.101.0.1","port":22,"username":"root","auth_type":"key"}]}`)
	for _, testCase := range []struct {
		name  string
		token string
		want  int
	}{
		{name: "operator", token: fixture.tokens["operator"], want: http.StatusForbidden},
		{name: "viewer", token: fixture.tokens["viewer"], want: http.StatusForbidden},
		{name: "revoked", token: fixture.tokens["revoked"], want: http.StatusUnauthorized},
		{name: "invalid", token: fixture.tokens["invalid"], want: http.StatusUnauthorized},
		{name: "missing", token: "", want: http.StatusUnauthorized},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			before := configImportPreviewRouterDatabaseSnapshot(t, fixture.db)
			fileBefore := previewRouterFixtureFile(t, fixture.knownHosts)
			response := configImportPreviewRouterRequest(t, fixture.router, http.MethodPost, configImportPreviewRouterPath, testCase.token, body, nil)
			if response.Code != testCase.want {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), testCase.want)
			}
			assertConfigImportPreviewRouterReadOnly(t, fixture, before, fileBefore, writes)
		})
	}
}

func TestConfigImportStillRequiresStepUpAndGrant(t *testing.T) {
	fixture := setupConfigImportPreviewRouterFixture(t)
	before := configImportPreviewRouterDatabaseSnapshot(t, fixture.db)
	body := []byte(`{"nodes":[{"name":"router-apply-protected-node","host":"10.102.0.1","port":22,"username":"root","auth_type":"key"}]}`)
	response := configImportPreviewRouterRequest(t, fixture.router, http.MethodPost, "/api/v1/config/import?conflict=skip", fixture.tokens["admin"], body, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unprotected apply status=%d body=%s, want 403", response.Code, response.Body.String())
	}
	var nodes int64
	if err := fixture.db.Model(&model.Node{}).Where("name = ?", "router-apply-protected-node").Count(&nodes).Error; err != nil {
		t.Fatalf("count protected apply node: %v", err)
	}
	if nodes != 0 {
		t.Fatalf("unprotected apply persisted %d config nodes", nodes)
	}
	// The secured apply route is expected to leave its existing audit trail;
	// only the configuration state is required to remain untouched here.
	after := configImportPreviewRouterDatabaseSnapshot(t, fixture.db)
	for _, table := range []string{"nodes", "ssh_keys", "policies", "tasks", "system_settings"} {
		if before[table] != after[table] {
			t.Fatalf("protected apply changed config table %s", table)
		}
	}
}

func TestConfigImportRouterAuthorizedApplyWritesAndAudits(t *testing.T) {
	fixture := setupConfigImportPreviewRouterFixture(t)
	before := configImportPreviewRouterDatabaseSnapshot(t, fixture.db)
	for _, table := range []string{"task_runs", "task_run_effects", "backup_repositories", "backup_asset_config_import_refs"} {
		if _, ok := before[table]; !ok {
			t.Fatalf("production router fixture omitted persistent table %q", table)
		}
	}

	var admin model.User
	if err := fixture.db.Where("username = ?", "config-preview-admin").First(&admin).Error; err != nil {
		t.Fatalf("load authorized apply admin: %v", err)
	}
	if err := fixture.db.Model(&model.User{}).Where("id = ?", admin.ID).Update("totp_enabled", true).Error; err != nil {
		t.Fatalf("enable authorized apply test TOTP state: %v", err)
	}
	if err := fixture.db.First(&admin, admin.ID).Error; err != nil {
		t.Fatalf("reload authorized apply admin: %v", err)
	}

	var auditBefore, credentialAuditBefore int64
	if err := fixture.db.Model(&model.AuditLog{}).Count(&auditBefore).Error; err != nil {
		t.Fatalf("count audit logs before authorized apply: %v", err)
	}
	if err := fixture.db.Model(&model.CredentialAuditEvent{}).Count(&credentialAuditBefore).Error; err != nil {
		t.Fatalf("count credential audit events before authorized apply: %v", err)
	}

	grantProof, _, err := fixture.jwt.GenerateStepUpToken(admin, auth.StepUpActionConfigImport)
	if err != nil {
		t.Fatalf("generate config import grant proof: %v", err)
	}
	grantResponse := configImportPreviewRouterRequestWithProof(
		t,
		fixture.router,
		http.MethodPost,
		"/api/v1/credential-access-grants/config-import",
		fixture.tokens["admin"],
		grantProof,
		[]byte(`{"reason":"authorized preview apply","requested_ttl_seconds":600}`),
		nil,
	)
	if grantResponse.Code != http.StatusCreated {
		t.Fatalf("authorized config import grant status=%d body=%s", grantResponse.Code, grantResponse.Body.String())
	}
	var grant configImportPreviewRouterGrantEnvelope
	if err := json.Unmarshal(grantResponse.Body.Bytes(), &grant); err != nil {
		t.Fatalf("decode authorized config import grant: %v; body=%s", err, grantResponse.Body.String())
	}
	if grant.Data.ID == 0 || grant.Data.Action != "config.import" || grant.Data.Purpose != "config_import" || grant.Data.Status != "active" {
		t.Fatalf("authorized config import grant DTO=%+v", grant.Data)
	}

	var persistedGrant model.CredentialAccessGrant
	if err := fixture.db.First(&persistedGrant, grant.Data.ID).Error; err != nil {
		t.Fatalf("load persisted config import grant: %v", err)
	}
	if persistedGrant.Status != "active" || persistedGrant.RequesterUserID != admin.ID {
		t.Fatalf("persisted config import grant=%+v", persistedGrant)
	}

	applyProof, _, err := fixture.jwt.GenerateStepUpToken(admin, auth.StepUpActionConfigImport)
	if err != nil {
		t.Fatalf("generate config import apply proof: %v", err)
	}
	applyBody := []byte(`{"nodes":[{"name":"router-authorized-apply-node","host":"10.105.0.1","port":22,"username":"root","auth_type":"key"}],"system_settings":[{"key":"storage.min_free_gb","value":"43"}]}`)
	applyResponse := configImportPreviewRouterRequestWithProof(
		t,
		fixture.router,
		http.MethodPost,
		"/api/v1/config/import?conflict=skip",
		fixture.tokens["admin"],
		applyProof,
		applyBody,
		nil,
	)
	if applyResponse.Code != http.StatusOK {
		t.Fatalf("authorized config import apply status=%d body=%s", applyResponse.Code, applyResponse.Body.String())
	}

	var node model.Node
	if err := fixture.db.Where("name = ?", "router-authorized-apply-node").First(&node).Error; err != nil {
		t.Fatalf("authorized config import did not persist node: %v", err)
	}
	if node.Host != "10.105.0.1" || node.Username != "root" {
		t.Fatalf("authorized config import persisted unexpected node=%+v", node)
	}
	var setting model.SystemSetting
	if err := fixture.db.Where("key = ?", "storage.min_free_gb").First(&setting).Error; err != nil {
		t.Fatalf("authorized config import did not persist system setting: %v", err)
	}
	if setting.Value != "43" {
		t.Fatalf("authorized config import persisted system setting value=%q, want 43", setting.Value)
	}

	var auditAfter, credentialAuditAfter int64
	if err := fixture.db.Model(&model.AuditLog{}).Count(&auditAfter).Error; err != nil {
		t.Fatalf("count audit logs after authorized apply: %v", err)
	}
	if auditAfter <= auditBefore {
		t.Fatalf("authorized apply did not persist original audit-log path: before=%d after=%d", auditBefore, auditAfter)
	}
	var applyAudit model.AuditLog
	if err := fixture.db.Where("method = ? AND path = ? AND status_code = ?", http.MethodPost, "/api/v1/config/import", http.StatusOK).Order("id DESC").First(&applyAudit).Error; err != nil {
		t.Fatalf("load authorized config import audit-log entry: %v", err)
	}
	if applyAudit.EntryHash == "" {
		t.Fatalf("authorized apply audit-log entry has empty hash: %+v", applyAudit)
	}

	if err := fixture.db.Model(&model.CredentialAuditEvent{}).Count(&credentialAuditAfter).Error; err != nil {
		t.Fatalf("count credential audit events after authorized apply: %v", err)
	}
	if credentialAuditAfter <= credentialAuditBefore {
		t.Fatalf("authorized apply did not persist credential audit path: before=%d after=%d", credentialAuditBefore, credentialAuditAfter)
	}
	var credentialEvents []model.CredentialAuditEvent
	if err := fixture.db.Where("action = ? AND purpose = ? AND outcome = ?", string(auth.StepUpActionConfigImport), "config_import", credentialaudit.OutcomeSuccess).Find(&credentialEvents).Error; err != nil {
		t.Fatalf("load authorized config import credential audit events: %v", err)
	}
	if len(credentialEvents) == 0 {
		t.Fatal("authorized apply persisted no successful config import credential audit event")
	}
	actorMatched := false
	for _, event := range credentialEvents {
		if event.UserID == admin.ID {
			actorMatched = true
			break
		}
	}
	if !actorMatched {
		t.Fatalf("successful config import credential audit events omit admin actor: %+v", credentialEvents)
	}
}

func TestConfigImportPreviewRouterMalformedQueryFailureAndCancelAreReadOnly(t *testing.T) {
	fixture := setupConfigImportPreviewRouterFixture(t)
	queryKey := model.SSHKey{Name: "router-preview-query-error-key", Username: "preview-user"}
	if err := fixture.db.Create(&queryKey).Error; err != nil {
		t.Fatalf("seed query-error key: %v", err)
	}
	writes := installConfigImportPreviewRouterWriteGate(t, fixture.db)
	fileBefore := previewRouterFixtureFile(t, fixture.knownHosts)

	before := configImportPreviewRouterDatabaseSnapshot(t, fixture.db)
	malformed := configImportPreviewRouterRequest(t, fixture.router, http.MethodPost, configImportPreviewRouterPath, fixture.tokens["admin"], []byte(`{"data":`), nil)
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed status=%d body=%s, want 400", malformed.Code, malformed.Body.String())
	}
	assertConfigImportPreviewRouterReadOnly(t, fixture, before, fileBefore, writes)
	before = configImportPreviewRouterDatabaseSnapshot(t, fixture.db)
	callbackName := "test:config-import-preview-router-query-failure"
	queryFailed := false
	injected := fmt.Errorf("CONFIG_IMPORT_PREVIEW_ROUTER_QUERY_FAILURE")
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "ssh_keys" {
			return
		}
		queryFailed = true
		_ = tx.AddError(injected)
	}); err != nil {
		t.Fatalf("register router query-failure callback: %v", err)
	}
	queryResponse := configImportPreviewRouterRequest(t, fixture.router, http.MethodPost, configImportPreviewRouterPath, fixture.tokens["admin"], []byte(`{"ssh_keys":[{"name":"router-preview-query-error-key"}]}`), nil)
	_ = fixture.db.Callback().Query().Remove(callbackName)
	if queryResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("query-error status=%d body=%s, want 503", queryResponse.Code, queryResponse.Body.String())
	}
	if !queryFailed {
		t.Fatal("router query-failure callback did not observe SSH-key inventory query")
	}
	if strings.Contains(queryResponse.Body.String(), injected.Error()) {
		t.Fatalf("query error leaked internal detail: %s", queryResponse.Body.String())
	}
	assertConfigImportPreviewRouterReadOnly(t, fixture, before, fileBefore, writes)

	before = configImportPreviewRouterDatabaseSnapshot(t, fixture.db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelResponse := configImportPreviewRouterRequest(t, fixture.router, http.MethodPost, configImportPreviewRouterPath, fixture.tokens["admin"], []byte(`{"nodes":[{"name":"router-preview-cancelled","host":"10.103.0.1","username":"root","auth_type":"key"}]}`), ctx)
	if cancelResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("cancelled status=%d body=%s, want 503", cancelResponse.Code, cancelResponse.Body.String())
	}
	assertConfigImportPreviewRouterReadOnly(t, fixture, before, fileBefore, writes)
}

func TestConfigImportPreviewRouterDoesNotReserveImmediateSQLiteTransaction(t *testing.T) {
	fixture := setupConfigImportPreviewRouterFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	callbackName := "test:config-import-preview-router-read-gate"
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "nodes" {
			return
		}
		once.Do(func() { close(started) })
		<-release
	}); err != nil {
		t.Fatalf("register router read gate: %v", err)
	}
	t.Cleanup(func() {
		releaseGate()
		_ = fixture.db.Callback().Query().Remove(callbackName)
	})

	responseCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseCh <- configImportPreviewRouterRequest(t, fixture.router, http.MethodPost, configImportPreviewRouterPath, fixture.tokens["admin"], []byte(`{"nodes":[{"name":"router-preview-lock-node","host":"10.104.0.1","port":22,"username":"root","auth_type":"key"}]}`), nil)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		releaseGate()
		t.Fatal("preview did not reach the snapshot query gate")
	}

	writerResult := make(chan error, 1)
	go func() {
		err := fixture.writer.Exec("BEGIN IMMEDIATE").Error
		if err == nil {
			err = fixture.writer.Exec("ROLLBACK").Error
		}
		writerResult <- err
	}()
	select {
	case err := <-writerResult:
		if err != nil {
			releaseGate()
			t.Fatalf("writer could not begin while preview was reading: %v", err)
		}
	case <-time.After(750 * time.Millisecond):
		releaseGate()
		t.Fatal("preview held an immediate SQLite reservation while reading")
	}
	releaseGate()
	response := <-responseCh
	if response.Code != http.StatusOK {
		t.Fatalf("lock probe preview status=%d body=%s", response.Code, response.Body.String())
	}
}
