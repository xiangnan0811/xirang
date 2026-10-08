package handlers

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
)

func p1ImportPrivateKey(t *testing.T) string {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	key := ed25519.NewKeyFromSeed(seed)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal test private key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func serveP1ConfigImport(t *testing.T, db *gorm.DB, service *settings.Service, conflict string, body any) (*httptest.ResponseRecorder, configImportResult) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal import payload: %v", err)
	}
	handler := NewConfigHandler(db, service)
	router := gin.New()
	router.POST("/config/import", handler.Import)
	path := "/config/import"
	if conflict != "" {
		path += "?conflict=" + conflict
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(payload)))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	var envelope struct {
		Data configImportResult `json:"data"`
	}
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode import response: %v; body=%s", err, response.Body.String())
		}
	}
	return response, envelope.Data
}

func migrateP1ConfigImport(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(
		&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{},
		&model.SSHKey{}, &model.CredentialAuditEvent{},
	); err != nil {
		t.Fatalf("migrate config import P1 tables: %v", err)
	}
}

func warningCodes(result configImportResult) map[string]bool {
	codes := make(map[string]bool, len(result.Warnings))
	for _, warning := range result.Warnings {
		codes[warning.Code] = true
	}
	return codes
}

func TestConfigImportP1SafetySQLite(t *testing.T) {
	runConfigImportP1Safety(t, "sqlite")
}

func TestConfigImportP1SafetyPostgres(t *testing.T) {
	runConfigImportP1Safety(t, "postgres")
}

func runConfigImportP1Safety(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	validPrivateKey := p1ImportPrivateKey(t)
	preparedPrivateKey, preparedType, err := sshutil.ValidateAndPreparePrivateKey(validPrivateKey, sshutil.SSHKeyTypeAuto)
	if err != nil {
		t.Fatalf("prepare fixture private key: %v", err)
	}
	oldKey := model.SSHKey{
		Name:            "p1-existing-key",
		Username:        "old-user",
		KeyType:         preparedType,
		PrivateKey:      preparedPrivateKey,
		Fingerprint:     generateFingerprint(preparedPrivateKey),
		Disabled:        true,
		AllowedNodeIDs:  "7",
		AllowedPurposes: sshutil.PurposeTerminal,
	}
	if err := db.Create(&oldKey).Error; err != nil {
		t.Fatalf("create existing key: %v", err)
	}
	oldNode := model.Node{
		Name: "p1-existing-node", Host: "10.0.0.2", Port: 22, Username: "old-user",
		AuthType: "password", Password: "old-password", PrivateKey: "old-inline-private", BackupDir: "p1-existing-node",
	}
	if err := db.Create(&oldNode).Error; err != nil {
		t.Fatalf("create existing node: %v", err)
	}

	body := map[string]any{
		"ssh_keys": []any{
			map[string]any{
				"name": "p1-new-unsafe", "username": "murray", "key_type": "auto", "private_key": "",
				"fingerprint": "SOURCE_DIGEST_UNTRUSTED", "allowed_node_ids": "1", "disabled": false,
			},
			map[string]any{
				"name": "p1-existing-key", "username": "new-user", "allowed_node_ids": "2", "disabled": false,
			},
			map[string]any{
				"name": "p1-valid-key", "username": "murray", "key_type": "auto", "private_key": validPrivateKey,
				"fingerprint": "SOURCE_DIGEST_UNTRUSTED",
			},
			map[string]any{
				"name": "p1-invalid-scope", "username": "murray", "private_key": validPrivateKey,
				"allowed_purposes": []any{"terminal"}, "disabled": false,
			},
			map[string]any{
				"name": "p1-invalid-private", "username": "murray", "private_key": "not-a-private-key",
			},
		},
		"nodes": []any{
			map[string]any{
				"name": "p1-new-node", "host": "10.0.0.3", "port": 22, "username": "root", "auth_type": "key",
				"ssh_key_id": 999,
			},
			map[string]any{
				"name": "p1-existing-node", "host": "10.0.0.4", "port": 22, "username": "updated-user",
				"auth_type": "password", "password": "new-password", "private_key": "new-private-must-not-overwrite",
			},
		},
		"policies": []any{
			map[string]any{"name": "p1-valid-policy", "source_path": "/source", "target_path": "/target", "cron_spec": "*/5 * * * *"},
			map[string]any{"name": "p1-invalid-policy", "source_path": "relative/../../secret", "target_path": "/target"},
		},
		"tasks": []any{
			map[string]any{"name": "p1-valid-task", "node_name": "p1-new-node", "executor_type": "command", "command": "echo safe"},
			map[string]any{"name": "p1-invalid-task", "node_name": "missing-node", "executor_type": "command", "command": "echo rejected"},
		},
		"system_settings": []any{map[string]any{"key": "storage.min_free_gb", "value": "42"}},
	}
	response, result := serveP1ConfigImport(t, db, settings.NewService(db), "overwrite", body)
	if response.Code != http.StatusOK {
		t.Fatalf("P1 import status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 7 || result.Updated != 1 || result.Imported != 8 || result.Skipped != 0 || result.Rejected != 4 || result.DisabledImported != 2 {
		t.Fatalf("unexpected P1 result: %+v", result)
	}
	codes := warningCodes(result)
	for _, code := range []string{
		configImportWarningUnresolvedNodeScope, configImportWarningInvalidScope,
		configImportWarningInvalidPrivateKey, configImportWarningMissingPrivateKey,
		configImportWarningUnresolvedSSHKey, configImportWarningMissingInlineKey,
		configImportWarningInvalidInput,
	} {
		if !codes[code] {
			t.Fatalf("P1 result missing warning code %q: %+v", code, result.Warnings)
		}
	}
	if strings.Contains(response.Body.String(), "SOURCE_DIGEST_UNTRUSTED") || strings.Contains(response.Body.String(), "not-a-private-key") {
		t.Fatalf("import result leaked source credential material: %s", response.Body.String())
	}

	var unsafeKey, invalidScopeKey, validKey, unchangedKey model.SSHKey
	for name, target := range map[string]*model.SSHKey{
		"p1-new-unsafe":    &unsafeKey,
		"p1-invalid-scope": &invalidScopeKey,
		"p1-valid-key":     &validKey,
		"p1-existing-key":  &unchangedKey,
	} {
		if err := db.Where("name = ?", name).First(target).Error; err != nil {
			t.Fatalf("read imported key %q: %v", name, err)
		}
	}
	if !unsafeKey.Disabled || unsafeKey.AllowedNodeIDs != "" || unsafeKey.Fingerprint != "" {
		t.Fatalf("unsafe new key was not fail-closed: %+v", unsafeKey)
	}
	if !invalidScopeKey.Disabled || invalidScopeKey.AllowedPurposes != "" {
		t.Fatalf("invalid scope did not force safe disabled state: %+v", invalidScopeKey)
	}
	if validKey.Fingerprint != generateFingerprint(preparedPrivateKey) || validKey.Fingerprint == "SOURCE_DIGEST_UNTRUSTED" || validKey.KeyType != preparedType {
		t.Fatalf("valid key was not normalized/recomputed: %+v", validKey)
	}
	if unchangedKey.Username != oldKey.Username || unchangedKey.AllowedNodeIDs != oldKey.AllowedNodeIDs || unchangedKey.Disabled != oldKey.Disabled || unchangedKey.Fingerprint != oldKey.Fingerprint {
		t.Fatalf("unsafe overwrite changed existing key: got=%+v old=%+v", unchangedKey, oldKey)
	}

	var importedNode model.Node
	if err := db.Where("name = ?", "p1-new-node").First(&importedNode).Error; err != nil {
		t.Fatalf("read imported node: %v", err)
	}
	if importedNode.SSHKeyID != nil || importedNode.PrivateKey != "" {
		t.Fatalf("source ssh_key_id/private material was bound unexpectedly: %+v", importedNode.Sanitized())
	}
	var preservedNode model.Node
	if err := db.Where("name = ?", "p1-existing-node").First(&preservedNode).Error; err != nil {
		t.Fatalf("read overwritten node: %v", err)
	}
	if preservedNode.Password != "old-password" || preservedNode.PrivateKey != "old-inline-private" {
		t.Fatalf("node overwrite changed password/private fields: password=%q private=%q", preservedNode.Password, preservedNode.PrivateKey)
	}

	response, skipped := serveP1ConfigImport(t, db, nil, "skip", map[string]any{
		"ssh_keys": []any{map[string]any{"name": "p1-valid-key"}},
		"nodes":    []any{map[string]any{"name": "p1-existing-node"}},
	})
	if response.Code != http.StatusOK || skipped.Skipped != 2 || skipped.Imported != 0 || skipped.Created != 0 || skipped.Updated != 0 {
		t.Fatalf("conflict skip result status=%d result=%+v body=%s", response.Code, skipped, response.Body.String())
	}
}

func TestConfigImportWarningCapAndNameSanitizer(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	migrateP1ConfigImport(t, db)
	longName := strings.Repeat("名", 130)
	nodes := make([]any, 0, 105)
	nodes = append(nodes, map[string]any{"name": longName, "host": "relative/../../secret", "username": "root", "auth_type": "key"})
	for i := 1; i < 105; i++ {
		nodes = append(nodes, map[string]any{"host": fmt.Sprintf("10.0.0.%d", i), "username": "root", "auth_type": "key"})
	}
	response, result := serveP1ConfigImport(t, db, nil, "skip", map[string]any{"nodes": nodes})
	if response.Code != http.StatusOK {
		t.Fatalf("warning cap import status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Rejected != 105 || len(result.Warnings) != configImportWarningLimit || result.WarningsTruncated != 5 {
		t.Fatalf("warning cap result=%+v", result)
	}
	if got := len([]rune(result.Warnings[0].Name)); got != 120 {
		t.Fatalf("warning name length=%d, want 120 runes", got)
	}
}

func TestParseImportedSSHKeyScopeStrictTypes(t *testing.T) {
	tests := []struct {
		name string
		data map[string]interface{}
		code string
	}{
		{name: "disabled string", data: map[string]interface{}{"disabled": "false"}, code: configImportWarningInvalidScope},
		{name: "expiry number", data: map[string]interface{}{"expires_at": 42}, code: configImportWarningInvalidScope},
		{name: "purpose array", data: map[string]interface{}{"allowed_purposes": []any{"terminal"}}, code: configImportWarningInvalidScope},
		{name: "node id array", data: map[string]interface{}{"allowed_node_ids": []any{1}}, code: configImportWarningInvalidScope},
		{name: "node id syntax", data: map[string]interface{}{"allowed_node_ids": "not-a-number"}, code: configImportWarningInvalidScope},
		{name: "tag object", data: map[string]interface{}{"allowed_node_tags": map[string]any{"name": "prod"}}, code: configImportWarningInvalidScope},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, code := parseImportedSSHKeyScope(test.data)
			if code != test.code {
				t.Fatalf("scope code=%q, want %q", code, test.code)
			}
		})
	}

	candidate, code := parseImportedSSHKeyScope(map[string]interface{}{
		"disabled":          false,
		"expires_at":        "",
		"allowed_purposes":  "terminal,task_command",
		"allowed_node_ids":  "01,2",
		"allowed_node_tags": "prod,prod,db",
	})
	if code != configImportWarningUnresolvedNodeScope ||
		candidate.allowedPurposes == nil || *candidate.allowedPurposes != "terminal,task_command" ||
		candidate.allowedNodeIDs == nil || *candidate.allowedNodeIDs != "1,2" ||
		candidate.allowedNodeTags == nil || *candidate.allowedNodeTags != "prod,db" ||
		!candidate.expiresAtSet || candidate.expiresAt != nil {
		t.Fatalf("unexpected normalized scope candidate=%+v code=%q", candidate, code)
	}
}

func TestConfigImportUnexpectedEntityWriteErrorsAbortTransaction(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	for _, entity := range []struct {
		name  string
		table string
		body  map[string]any
	}{
		{name: "ssh key", table: "ssh_keys", body: map[string]any{"ssh_keys": []any{map[string]any{"name": "write-fail-key"}}}},
		{name: "node", table: "nodes", body: map[string]any{"nodes": []any{map[string]any{"name": "write-fail-node", "host": "10.1.0.2", "username": "root", "auth_type": "key"}}}},
		{name: "policy", table: "policies", body: map[string]any{"policies": []any{map[string]any{"name": "write-fail-policy", "source_path": "/src", "target_path": "/dst"}}}},
		{name: "task", table: "tasks", body: map[string]any{"nodes": []any{map[string]any{"name": "write-fail-task-node", "host": "10.1.0.3", "username": "root", "auth_type": "key"}}, "tasks": []any{map[string]any{"name": "write-fail-task", "node_name": "write-fail-task-node", "executor_type": "command", "command": "echo fail"}}}},
		{name: "system setting", table: "system_settings", body: map[string]any{"system_settings": []any{map[string]any{"key": "storage.min_free_gb", "value": "42"}}}},
	} {
		t.Run(entity.name, func(t *testing.T) {
			db := openConfigHandlerTestDB(t)
			migrateP1ConfigImport(t, db)
			injected := errors.New("P1_CONFIG_IMPORT_WRITE_FAILURE")
			callbackName := "test:p1-config-import-write-failure-" + strings.ReplaceAll(entity.table, "_", "-")
			if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == entity.table {
					_ = tx.AddError(injected)
				}
			}); err != nil {
				t.Fatalf("register write failure callback: %v", err)
			}
			t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
			response, _ := serveP1ConfigImport(t, db, settings.NewService(db), "skip", entity.body)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("write failure status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), injected.Error()) {
				t.Fatalf("write failure leaked internal error: %s", response.Body.String())
			}
		})
	}
}

func TestConfigImportUnexpectedEntityQueryErrorAbortsTransaction(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	for _, entity := range []struct {
		name  string
		table string
		body  map[string]any
	}{
		{name: "ssh key", table: "ssh_keys", body: map[string]any{"ssh_keys": []any{map[string]any{"name": "query-fail-key"}}}},
		{name: "node", table: "nodes", body: map[string]any{"nodes": []any{map[string]any{"name": "query-fail-node", "host": "10.2.0.2", "username": "root", "auth_type": "key"}}}},
		{name: "policy", table: "policies", body: map[string]any{"policies": []any{map[string]any{"name": "query-fail-policy", "source_path": "/src", "target_path": "/dst"}}}},
		{name: "task", table: "tasks", body: map[string]any{"nodes": []any{map[string]any{"name": "query-fail-task-node", "host": "10.2.0.3", "username": "root", "auth_type": "key"}}, "tasks": []any{map[string]any{"name": "query-fail-task", "node_name": "query-fail-task-node", "executor_type": "command", "command": "echo fail"}}}},
		{name: "system setting", table: "system_settings", body: map[string]any{"system_settings": []any{map[string]any{"key": "storage.min_free_gb", "value": "42"}}}},
	} {
		t.Run(entity.name, func(t *testing.T) {
			db := openConfigHandlerTestDB(t)
			migrateP1ConfigImport(t, db)
			injected := errors.New("P1_CONFIG_IMPORT_QUERY_FAILURE")
			callbackName := "test:p1-config-import-query-failure-" + strings.ReplaceAll(entity.table, "_", "-")
			if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == entity.table {
					_ = tx.AddError(injected)
				}
			}); err != nil {
				t.Fatalf("register query failure callback: %v", err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })
			response, _ := serveP1ConfigImport(t, db, settings.NewService(db), "skip", entity.body)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("query failure status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), injected.Error()) {
				t.Fatalf("query failure leaked internal error: %s", response.Body.String())
			}
		})
	}
}

type configImportFaultRow struct {
	table  string
	column string
	value  string
}

type configImportFaultCase struct {
	name   string
	table  string
	first  string
	second string
	failOn int
	body   map[string]any
	rows   []configImportFaultRow
}

func assertConfigImportRowsAbsent(t *testing.T, db *gorm.DB, rows []configImportFaultRow) {
	t.Helper()
	for _, row := range rows {
		var count int64
		if err := db.Table(row.table).Where(row.column+" = ?", row.value).Count(&count).Error; err != nil {
			t.Fatalf("count rolled-back %s %s=%q: %v", row.table, row.column, row.value, err)
		}
		if count != 0 {
			t.Fatalf("rolled-back %s %s=%q remains count=%d", row.table, row.column, row.value, count)
		}
	}
}

func configImportCreateFaultCases() []configImportFaultCase {
	return []configImportFaultCase{
		{
			name: "ssh keys", table: "ssh_keys", first: "create-first-key", second: "create-second-key",
			body: map[string]any{"ssh_keys": []any{
				map[string]any{"name": "create-first-key"},
				map[string]any{"name": "create-second-key"},
			}},
			rows: []configImportFaultRow{
				{table: "ssh_keys", column: "name", value: "create-first-key"},
				{table: "ssh_keys", column: "name", value: "create-second-key"},
			},
		},
		{
			name: "node", table: "nodes", first: "create-node", second: "create-node", failOn: 1,
			body: map[string]any{
				"ssh_keys": []any{map[string]any{"name": "create-earlier-key"}},
				"nodes": []any{map[string]any{
					"name": "create-node", "host": "10.3.0.2", "port": 22,
					"username": "root", "auth_type": "key",
				}},
			},
			rows: []configImportFaultRow{
				{table: "ssh_keys", column: "name", value: "create-earlier-key"},
				{table: "nodes", column: "name", value: "create-node"},
			},
		},
		{
			name: "policy", table: "policies", first: "create-policy", second: "create-policy", failOn: 1,
			body: map[string]any{
				"nodes": []any{map[string]any{
					"name": "create-policy-node", "host": "10.3.0.3", "port": 22,
					"username": "root", "auth_type": "key",
				}},
				"policies": []any{map[string]any{
					"name": "create-policy", "source_path": "/source", "target_path": "/target",
				}},
			},
			rows: []configImportFaultRow{
				{table: "nodes", column: "name", value: "create-policy-node"},
				{table: "policies", column: "name", value: "create-policy"},
			},
		},
		{
			name: "task", table: "tasks", first: "create-first-task", second: "create-second-task",
			body: map[string]any{
				"nodes": []any{map[string]any{
					"name": "create-task-node", "host": "10.3.0.4", "port": 22,
					"username": "root", "auth_type": "key",
				}},
				"tasks": []any{
					map[string]any{
						"name": "create-first-task", "node_name": "create-task-node",
						"executor_type": "command", "command": "echo first",
					},
					map[string]any{
						"name": "create-second-task", "node_name": "create-task-node",
						"executor_type": "command", "command": "echo second",
					},
				},
			},
			rows: []configImportFaultRow{
				{table: "nodes", column: "name", value: "create-task-node"},
				{table: "tasks", column: "name", value: "create-first-task"},
				{table: "tasks", column: "name", value: "create-second-task"},
			},
		},
		{
			name: "system settings", table: "system_settings", first: "storage.min_free_gb", second: "login.rate_limit",
			body: map[string]any{
				"nodes": []any{map[string]any{
					"name": "create-setting-node", "host": "10.3.0.5", "port": 22,
					"username": "root", "auth_type": "key",
				}},
				"system_settings": []any{
					map[string]any{"key": "storage.min_free_gb", "value": "42"},
					map[string]any{"key": "login.rate_limit", "value": "11"},
				},
			},
			rows: []configImportFaultRow{
				{table: "nodes", column: "name", value: "create-setting-node"},
				{table: "system_settings", column: "key", value: "storage.min_free_gb"},
				{table: "system_settings", column: "key", value: "login.rate_limit"},
			},
		},
	}
}

func configImportQueryFaultCases() []configImportFaultCase {
	return []configImportFaultCase{
		{
			name: "ssh keys", table: "ssh_keys", first: "query-first-key", second: "query-second-key",
			body: map[string]any{"ssh_keys": []any{
				map[string]any{"name": "query-first-key"},
				map[string]any{"name": "query-second-key"},
			}},
			rows: []configImportFaultRow{
				{table: "ssh_keys", column: "name", value: "query-first-key"},
				{table: "ssh_keys", column: "name", value: "query-second-key"},
			},
		},
		{
			name: "node", table: "nodes", first: "query-node", second: "query-node",
			body: map[string]any{
				"ssh_keys": []any{map[string]any{"name": "query-earlier-key"}},
				"nodes": []any{map[string]any{
					"name": "query-node", "host": "10.4.0.2", "port": 22,
					"username": "root", "auth_type": "key",
				}},
			},
			rows: []configImportFaultRow{
				{table: "ssh_keys", column: "name", value: "query-earlier-key"},
				{table: "nodes", column: "name", value: "query-node"},
			},
		},
		{
			name: "policy", table: "policies", first: "query-policy", second: "query-policy",
			body: map[string]any{
				"nodes": []any{map[string]any{
					"name": "query-policy-node", "host": "10.4.0.3", "port": 22,
					"username": "root", "auth_type": "key",
				}},
				"policies": []any{map[string]any{
					"name": "query-policy", "source_path": "/source", "target_path": "/target",
				}},
			},
			rows: []configImportFaultRow{
				{table: "nodes", column: "name", value: "query-policy-node"},
				{table: "policies", column: "name", value: "query-policy"},
			},
		},
		{
			name: "task", table: "tasks", first: "query-first-task", second: "query-second-task",
			body: map[string]any{
				"nodes": []any{map[string]any{
					"name": "query-task-node", "host": "10.4.0.4", "port": 22,
					"username": "root", "auth_type": "key",
				}},
				"tasks": []any{
					map[string]any{
						"name": "query-first-task", "node_name": "query-task-node",
						"executor_type": "command", "command": "echo first",
					},
					map[string]any{
						"name": "query-second-task", "node_name": "query-task-node",
						"executor_type": "command", "command": "echo second",
					},
				},
			},
			rows: []configImportFaultRow{
				{table: "nodes", column: "name", value: "query-task-node"},
				{table: "tasks", column: "name", value: "query-first-task"},
				{table: "tasks", column: "name", value: "query-second-task"},
			},
		},
		{
			name: "system settings", table: "system_settings", first: "storage.min_free_gb", second: "login.rate_limit",
			body: map[string]any{
				"nodes": []any{map[string]any{
					"name": "query-setting-node", "host": "10.4.0.5", "port": 22,
					"username": "root", "auth_type": "key",
				}},
				"system_settings": []any{
					map[string]any{"key": "storage.min_free_gb", "value": "42"},
					map[string]any{"key": "login.rate_limit", "value": "11"},
				},
			},
			rows: []configImportFaultRow{
				{table: "nodes", column: "name", value: "query-setting-node"},
				{table: "system_settings", column: "key", value: "storage.min_free_gb"},
				{table: "system_settings", column: "key", value: "login.rate_limit"},
			},
		},
	}
}

func runConfigImportCreateOrQueryRollback(t *testing.T, engine, mode string) {
	t.Helper()
	for _, testCase := range func() []configImportFaultCase {
		if mode == "create" {
			return configImportCreateFaultCases()
		}
		return configImportQueryFaultCases()
	}() {
		t.Run(testCase.name, func(t *testing.T) {
			setConfigHandlerTestEncryption(t)
			db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateP1ConfigImport(t, db)
			injected := errors.New("P1_CONFIG_IMPORT_" + strings.ToUpper(mode) + "_FAILURE")
			callbackName := "test:p1-config-import-" + mode + "-" + strings.ReplaceAll(testCase.table, "_", "-")
			armed := true
			calls := 0
			failOn := testCase.failOn
			if failOn == 0 {
				failOn = 2
			}
			var registerErr error
			if mode == "create" {
				registerErr = db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
					if !armed || tx.Statement == nil || tx.Statement.Schema == nil ||
						tx.Statement.Schema.Table != testCase.table {
						return
					}
					calls++
					if calls == failOn {
						_ = tx.AddError(injected)
					}
				})
			} else {
				registerErr = db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
					if !armed || tx.Statement == nil || tx.Statement.Schema == nil ||
						tx.Statement.Schema.Table != testCase.table {
						return
					}
					if strings.Contains(fmt.Sprint(tx.Statement.Vars), testCase.second) {
						_ = tx.AddError(injected)
					}
				})
			}
			if registerErr != nil {
				t.Fatalf("register %s failure callback: %v", mode, registerErr)
			}
			t.Cleanup(func() {
				if mode == "create" {
					_ = db.Callback().Create().Remove(callbackName)
				} else {
					_ = db.Callback().Query().Remove(callbackName)
				}
			})

			response, _ := serveP1ConfigImport(t, db, settings.NewService(db), "skip", testCase.body)
			armed = false
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("%s failure status=%d body=%s", mode, response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), injected.Error()) {
				t.Fatalf("%s failure leaked internal error: %s", mode, response.Body.String())
			}
			assertConfigImportRowsAbsent(t, db, testCase.rows)
		})
	}
}

func TestConfigImportCreateFailureRollsBackEarlierRowsSQLite(t *testing.T) {
	runConfigImportCreateOrQueryRollback(t, "sqlite", "create")
}

func TestConfigImportCreateFailureRollsBackEarlierRowsPostgres(t *testing.T) {
	runConfigImportCreateOrQueryRollback(t, "postgres", "create")
}

func TestConfigImportQueryFailureRollsBackEarlierRowsSQLite(t *testing.T) {
	runConfigImportCreateOrQueryRollback(t, "sqlite", "query")
}

func TestConfigImportQueryFailureRollsBackEarlierRowsPostgres(t *testing.T) {
	runConfigImportCreateOrQueryRollback(t, "postgres", "query")
}

func configImportSaveFaultCases() []configImportFaultCase {
	return []configImportFaultCase{
		{
			name: "ssh keys", table: "ssh_keys", first: "save-first-key", second: "save-second-key",
			body: map[string]any{"ssh_keys": []any{
				map[string]any{"name": "save-first-key", "username": "new-first"},
				map[string]any{"name": "save-second-key", "username": "new-second"},
			}},
			rows: []configImportFaultRow{
				{table: "ssh_keys", column: "name", value: "save-first-key"},
				{table: "ssh_keys", column: "name", value: "save-second-key"},
			},
		},
		{
			name: "nodes", table: "nodes", first: "save-first-node", second: "save-second-node",
			body: map[string]any{"nodes": []any{
				map[string]any{"name": "save-first-node", "host": "10.5.0.2", "username": "new-first", "auth_type": "key"},
				map[string]any{"name": "save-second-node", "host": "10.5.0.3", "username": "new-second", "auth_type": "key"},
			}},
			rows: []configImportFaultRow{
				{table: "nodes", column: "name", value: "save-first-node"},
				{table: "nodes", column: "name", value: "save-second-node"},
			},
		},
		{
			name: "policies", table: "policies", first: "save-first-policy", second: "save-second-policy",
			body: map[string]any{"policies": []any{
				map[string]any{"name": "save-first-policy", "source_path": "/new/source1", "target_path": "/new/target1"},
				map[string]any{"name": "save-second-policy", "source_path": "/new/source2", "target_path": "/new/target2"},
			}},
			rows: []configImportFaultRow{
				{table: "policies", column: "name", value: "save-first-policy"},
				{table: "policies", column: "name", value: "save-second-policy"},
			},
		},
		{
			name: "tasks", table: "tasks", first: "save-first-task", second: "save-second-task",
			body: map[string]any{
				"nodes": []any{map[string]any{
					"name": "save-task-node", "host": "10.5.0.4", "username": "root", "auth_type": "key",
				}},
				"tasks": []any{
					map[string]any{"name": "save-first-task", "node_name": "save-task-node", "executor_type": "command", "command": "new first"},
					map[string]any{"name": "save-second-task", "node_name": "save-task-node", "executor_type": "command", "command": "new second"},
				},
			},
			rows: []configImportFaultRow{
				{table: "tasks", column: "name", value: "save-first-task"},
				{table: "tasks", column: "name", value: "save-second-task"},
			},
		},
	}
}

func runConfigImportSaveRollback(t *testing.T, engine string) {
	t.Helper()
	for _, testCase := range configImportSaveFaultCases() {
		t.Run(testCase.name, func(t *testing.T) {
			setConfigHandlerTestEncryption(t)
			db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateP1ConfigImport(t, db)
			var node model.Node
			switch testCase.table {
			case "tasks":
				node = model.Node{Name: "save-task-node", Host: "10.5.0.4", Port: 22, Username: "root", AuthType: "key", BackupDir: "save-task-node"}
				if err := db.Create(&node).Error; err != nil {
					t.Fatalf("create save task node: %v", err)
				}
				for _, name := range []string{testCase.first, testCase.second} {
					if err := db.Create(&model.Task{Name: name, NodeID: node.ID, ExecutorType: "command", Command: "old", Status: "pending", Source: "manual", Enabled: false}).Error; err != nil {
						t.Fatalf("create save task %q: %v", name, err)
					}
				}
			case "nodes":
				for i, name := range []string{testCase.first, testCase.second} {
					if err := db.Create(&model.Node{Name: name, Host: fmt.Sprintf("10.5.0.%d", i+2), Port: 22, Username: "old", AuthType: "key", BackupDir: name}).Error; err != nil {
						t.Fatalf("create save node %q: %v", name, err)
					}
				}
			case "policies":
				for _, name := range []string{testCase.first, testCase.second} {
					if err := db.Create(&model.Policy{Name: name, SourcePath: "/old/source", TargetPath: "/old/target"}).Error; err != nil {
						t.Fatalf("create save policy %q: %v", name, err)
					}
				}
			default:
				for _, name := range []string{testCase.first, testCase.second} {
					if err := db.Create(&model.SSHKey{Name: name, Username: "old"}).Error; err != nil {
						t.Fatalf("create save key %q: %v", name, err)
					}
				}
			}

			injected := errors.New("P1_CONFIG_IMPORT_SAVE_FAILURE")
			callbackName := "test:p1-config-import-save-" + strings.ReplaceAll(testCase.table, "_", "-")
			calls := 0
			if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != testCase.table {
					return
				}
				calls++
				if calls == 2 {
					_ = tx.AddError(injected)
				}
			}); err != nil {
				t.Fatalf("register save failure callback: %v", err)
			}
			t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

			response, _ := serveP1ConfigImport(t, db, settings.NewService(db), "overwrite", testCase.body)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("save failure status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), injected.Error()) {
				t.Fatalf("save failure leaked internal error: %s", response.Body.String())
			}
			if calls < 2 {
				t.Fatalf("save failure callback calls=%d, want second Save to fail", calls)
			}
			// Existing rows must retain their original values after the first
			// overwrite has already reached the transaction.
			for _, row := range testCase.rows {
				var count int64
				if err := db.Table(row.table).Where(row.column+" = ?", row.value).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("save rollback row %s=%q count=%d err=%v", row.column, row.value, count, err)
				}
			}
			for _, name := range []string{testCase.first, testCase.second} {
				switch testCase.table {
				case "ssh_keys":
					var key model.SSHKey
					if err := db.Where("name = ?", name).First(&key).Error; err != nil || key.Username != "old" {
						t.Fatalf("save rollback key %q username=%q err=%v", name, key.Username, err)
					}
				case "nodes":
					var node model.Node
					if err := db.Where("name = ?", name).First(&node).Error; err != nil || node.Username != "old" {
						t.Fatalf("save rollback node %q username=%q err=%v", name, node.Username, err)
					}
				case "policies":
					var policy model.Policy
					if err := db.Where("name = ?", name).First(&policy).Error; err != nil || policy.SourcePath != "/old/source" {
						t.Fatalf("save rollback policy %q source=%q err=%v", name, policy.SourcePath, err)
					}
				case "tasks":
					var task model.Task
					if err := db.Where("name = ?", name).First(&task).Error; err != nil || task.Command != "old" {
						t.Fatalf("save rollback task %q command=%q err=%v", name, task.Command, err)
					}
				}
			}
		})
	}
}

func TestConfigImportSaveFailureRollsBackEarlierRowsSQLite(t *testing.T) {
	runConfigImportSaveRollback(t, "sqlite")
}

func TestConfigImportSaveFailureRollsBackEarlierRowsPostgres(t *testing.T) {
	runConfigImportSaveRollback(t, "postgres")
}

func runConfigImportUpdateRollback(t *testing.T, engine string) {
	t.Helper()
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	body := map[string]any{
		"nodes": []any{map[string]any{
			"name": "update-task-node", "host": "10.6.0.2", "port": 22,
			"username": "root", "auth_type": "key",
		}},
		"tasks": []any{
			map[string]any{"name": "update-first-task", "node_name": "update-task-node", "executor_type": "command", "command": "echo first", "enabled": false},
			map[string]any{"name": "update-second-task", "node_name": "update-task-node", "executor_type": "command", "command": "echo second", "enabled": false},
		},
	}
	injected := errors.New("P1_CONFIG_IMPORT_UPDATES_FAILURE")
	callbackName := "test:p1-config-import-updates"
	calls := 0
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "tasks" {
			calls++
			if calls == 2 {
				_ = tx.AddError(injected)
			}
		}
	}); err != nil {
		t.Fatalf("register updates failure callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	response, _ := serveP1ConfigImport(t, db, settings.NewService(db), "skip", body)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("updates failure status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), injected.Error()) {
		t.Fatalf("updates failure leaked internal error: %s", response.Body.String())
	}
	if calls < 2 {
		t.Fatalf("updates failure callback calls=%d, want second task update", calls)
	}
	assertConfigImportRowsAbsent(t, db, []configImportFaultRow{
		{table: "nodes", column: "name", value: "update-task-node"},
		{table: "tasks", column: "name", value: "update-first-task"},
		{table: "tasks", column: "name", value: "update-second-task"},
	})
}

func TestConfigImportUpdatesFailureRollsBackEarlierRowsSQLite(t *testing.T) {
	runConfigImportUpdateRollback(t, "sqlite")
}

func TestConfigImportUpdatesFailureRollsBackEarlierRowsPostgres(t *testing.T) {
	runConfigImportUpdateRollback(t, "postgres")
}

func TestConfigImportRejectsInvalidDependencyGraphWithoutRowsOrOverwriteMutation(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	migrateP1ConfigImport(t, db)

	node := model.Node{
		Name: "dependency-node", Host: "10.0.0.40", Port: 22,
		Username: "root", AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create dependency node: %v", err)
	}
	base := model.Task{
		Name: "dependency-base", NodeID: node.ID, ExecutorType: "command",
		Command: "echo base", Status: "pending", Source: "manual", Enabled: false,
	}
	if err := db.Create(&base).Error; err != nil {
		t.Fatalf("create dependency base task: %v", err)
	}
	overwrite := model.Task{
		Name: "dependency-overwrite", NodeID: node.ID, ExecutorType: "command",
		Command: "echo old", Status: "pending", Source: "manual", Enabled: false,
	}
	if err := db.Create(&overwrite).Error; err != nil {
		t.Fatalf("create dependency overwrite task: %v", err)
	}
	originalOverwrite := overwrite

	body := map[string]any{
		"tasks": []any{
			map[string]any{
				"name": "missing-dependency", "node_name": node.Name,
				"executor_type": "command", "command": "echo missing",
				"depends_on_task_name":      "does-not-exist",
				"depends_on_task_node_name": node.Name,
			},
			map[string]any{
				"name": "self-dependency", "node_name": node.Name,
				"executor_type": "command", "command": "echo self",
				"depends_on_task_name":      "self-dependency",
				"depends_on_task_node_name": node.Name,
			},
			map[string]any{
				"name": "cycle-a", "node_name": node.Name,
				"executor_type": "command", "command": "echo a",
				"depends_on_task_name":      "cycle-b",
				"depends_on_task_node_name": node.Name,
			},
			map[string]any{
				"name": "cycle-b", "node_name": node.Name,
				"executor_type": "command", "command": "echo b",
				"depends_on_task_name":      "cycle-a",
				"depends_on_task_node_name": node.Name,
			},
			map[string]any{
				"name": "cron-dependency", "node_name": node.Name,
				"executor_type": "command", "command": "echo cron",
				"cron_spec":                 "*/5 * * * *",
				"depends_on_task_name":      base.Name,
				"depends_on_task_node_name": node.Name,
			},
			map[string]any{
				"name": overwrite.Name, "node_name": node.Name,
				"executor_type": "command", "command": "echo changed",
				"depends_on_task_name":      overwrite.Name,
				"depends_on_task_node_name": node.Name,
			},
		},
	}
	response, result := serveP1ConfigImport(t, db, nil, "overwrite", body)
	if response.Code != http.StatusOK {
		t.Fatalf("invalid dependency import status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 0 || result.Updated != 0 || result.Imported != 0 ||
		result.Skipped != 0 || result.Rejected != 6 {
		t.Fatalf("invalid dependency import result=%+v", result)
	}

	for _, name := range []string{
		"missing-dependency", "self-dependency", "cycle-a",
		"cycle-b", "cron-dependency",
	} {
		var count int64
		if err := db.Model(&model.Task{}).Where("name = ?", name).Count(&count).Error; err != nil {
			t.Fatalf("count rejected task %q: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("rejected task %q left %d rows", name, count)
		}
	}

	var after model.Task
	if err := db.First(&after, originalOverwrite.ID).Error; err != nil {
		t.Fatalf("reload overwrite target: %v", err)
	}
	if after.Name != originalOverwrite.Name ||
		after.NodeID != originalOverwrite.NodeID ||
		after.Command != originalOverwrite.Command ||
		after.ExecutorType != originalOverwrite.ExecutorType ||
		after.CronSpec != originalOverwrite.CronSpec ||
		after.Status != originalOverwrite.Status ||
		after.Source != originalOverwrite.Source ||
		after.Enabled != originalOverwrite.Enabled ||
		after.DependsOnTaskID != nil ||
		!after.CreatedAt.Equal(originalOverwrite.CreatedAt) ||
		!after.UpdatedAt.Equal(originalOverwrite.UpdatedAt) {
		t.Fatalf("rejected overwrite mutated existing task: before=%+v after=%+v", originalOverwrite, after)
	}

	var taskCount int64
	if err := db.Model(&model.Task{}).Count(&taskCount).Error; err != nil {
		t.Fatalf("count tasks after rejected dependency import: %v", err)
	}
	if taskCount != 2 {
		t.Fatalf("task rows after rejected dependency import=%d, want base and unchanged overwrite only", taskCount)
	}
}

func TestConfigImportDoesNotFallbackToSourceNumericNodeIDs(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	migrateP1ConfigImport(t, db)

	ownNode := model.Node{
		Name: "own-node", Host: "10.0.0.41", Port: 22,
		Username: "root", AuthType: "key", BackupDir: "own-node",
	}
	foreignNode := model.Node{
		Name: "foreign-node", Host: "10.0.0.42", Port: 22,
		Username: "root", AuthType: "key", BackupDir: "foreign-node",
	}
	for _, candidate := range []*model.Node{&ownNode, &foreignNode} {
		if err := db.Create(candidate).Error; err != nil {
			t.Fatalf("create source-node collision fixture %q: %v", candidate.Name, err)
		}
	}
	foreignDependency := model.Task{
		Name: "numeric-dependency", NodeID: foreignNode.ID,
		ExecutorType: "command", Command: "echo dependency",
		Status: "pending", Source: "manual", Enabled: false,
	}
	if err := db.Create(&foreignDependency).Error; err != nil {
		t.Fatalf("create numeric dependency fixture: %v", err)
	}

	body := map[string]any{
		"tasks": []any{
			map[string]any{
				"name": "missing-source-name", "node_name": "not-imported",
				"node_id": ownNode.ID, "executor_type": "command",
				"command": "echo should reject",
			},
			map[string]any{
				"name": "numeric-only-source-node", "node_id": ownNode.ID,
				"executor_type": "command", "command": "echo should reject",
			},
			map[string]any{
				"name": "numeric-dependency-node", "node_name": ownNode.Name,
				"executor_type": "command", "command": "echo should reject",
				"depends_on_task_name":    foreignDependency.Name,
				"depends_on_task_node_id": foreignNode.ID,
			},
		},
	}
	response, result := serveP1ConfigImport(t, db, nil, "skip", body)
	if response.Code != http.StatusOK {
		t.Fatalf("numeric source node import status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 0 || result.Updated != 0 || result.Imported != 0 ||
		result.Skipped != 0 || result.Rejected != 3 {
		t.Fatalf("numeric source node import result=%+v", result)
	}
	for _, name := range []string{
		"missing-source-name", "numeric-only-source-node", "numeric-dependency-node",
	} {
		var count int64
		if err := db.Model(&model.Task{}).Where("name = ?", name).Count(&count).Error; err != nil {
			t.Fatalf("count rejected numeric-source task %q: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("numeric-source task %q fell back to a target row, count=%d", name, count)
		}
	}
}

func TestConfigImportRejectsLongDependencyCycles(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, overwrite := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/overwrite=%t", engine, overwrite), func(t *testing.T) {
				setConfigHandlerTestEncryption(t)
				db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
				migrateP1ConfigImport(t, db)
				node := model.Node{Name: "long-cycle-node", Host: "10.0.0.50", Port: 22, Username: "root", AuthType: "key"}
				if err := db.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				var original model.Task
				if overwrite {
					original = model.Task{Name: "long-cycle-0", NodeID: node.ID, ExecutorType: "command", Command: "old", Status: "pending", Source: "manual"}
					if err := db.Create(&original).Error; err != nil {
						t.Fatal(err)
					}
					if err := db.First(&original, original.ID).Error; err != nil {
						t.Fatal(err)
					}
				}
				tasks := make([]map[string]any, 12)
				for i := range tasks {
					tasks[i] = map[string]any{
						"name": fmt.Sprintf("long-cycle-%d", i), "node_name": node.Name,
						"executor_type": "command", "command": "new",
						"depends_on_task_name": fmt.Sprintf("long-cycle-%d", (i+1)%len(tasks)),
					}
				}
				response, result := serveP1ConfigImport(t, db, nil, "overwrite", map[string]any{"tasks": tasks})
				if response.Code != http.StatusOK || result.Rejected != 12 || result.Imported != 0 || result.Created != 0 || result.Updated != 0 {
					t.Fatalf("long cycle status=%d result=%+v", response.Code, result)
				}
				var rows []model.Task
				if err := db.Find(&rows).Error; err != nil {
					t.Fatal(err)
				}
				if !overwrite && len(rows) != 0 {
					t.Fatalf("rejected cycle left rows: %+v", rows)
				}
				if overwrite && (len(rows) != 1 || rows[0].ID != original.ID || rows[0].Command != original.Command || rows[0].DependsOnTaskID != nil || !rows[0].UpdatedAt.Equal(original.UpdatedAt)) {
					t.Fatalf("rejected cycle changed existing row: %+v", rows)
				}
			})
		}
	}
}
