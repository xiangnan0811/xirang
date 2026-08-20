package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	configV2SourceRepositoryID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	configV2SourceLinkIDOne    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb1"
	configV2SourceLinkIDTwo    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2"
	configV2SourcePolicyID     = "ccccccccccccccccccccccccccccccc1"
	configV2SourceHoldID       = "ddddddddddddddddddddddddddddddd1"
	configV2SourcePointID      = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeee1"
	configV2SourceBindingID    = "ffffffffffffffffffffffffffffff01"
	configV2PrivateIdentity    = "restic:native:config-v2-shared-identity"
	configV2BindingSecret      = "FAKE_CONFIG_V2_BINDING_SECRET_FOR_TEST_ONLY"
	configV2IdentitySaltHex    = "4242424242424242424242424242424242424242424242424242424242424242"
	configV2HoldReason         = "FAKE_CONFIG_V2_HOLD_REASON_FOR_TEST_ONLY"
	configV2LegacyLocator      = "FAKE_CONFIG_V2_PRIVATE_LOCATOR_FOR_TEST_ONLY"
)

func configV2IdentityRef() string {
	sum := sha256.Sum256([]byte(configV2PrivateIdentity))
	return hex.EncodeToString(sum[:])
}

func configV2BindingPlaintext() string {
	return `{"identity_salt":"` + configV2IdentitySaltHex + `","locator":"` + configV2BindingSecret + `"}`
}

func configV2BindingFingerprint() string {
	salt, err := hex.DecodeString(configV2IdentitySaltHex)
	if err != nil {
		panic(err)
	}
	fingerprint, err := provider.DeriveConfigFingerprint(salt, []byte(configV2BindingPlaintext()))
	if err != nil {
		panic(err)
	}
	return fingerprint
}

func migrateConfigAssetGraphDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(
		&model.Node{},
		&model.Policy{},
		&model.Task{},
		&model.SystemSetting{},
		&model.SSHKey{},
		&model.CredentialAuditEvent{},
		&model.BackupRepository{},
		&model.RepositoryAccessBinding{},
		&model.TaskRepositoryLink{},
		&model.RecoveryPoint{},
		&model.BackupRetentionPolicy{},
		&model.RecoveryPointHold{},
		&model.BackupAssetConfigImportRef{},
	); err != nil {
		t.Fatalf("migrate config asset graph: %v", err)
	}
}

func seedConfigV2SharedAssetGraph(t *testing.T, db *gorm.DB) (model.Task, model.Task) {
	t.Helper()
	node := model.Node{
		Name:      "asset-node",
		Host:      "10.80.0.10",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: "asset-node",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	first := model.Task{
		Name:         "asset-task-a",
		NodeID:       node.ID,
		ExecutorType: "restic",
		RsyncSource:  "/data/a",
		RsyncTarget:  "/backup/a",
		Status:       "pending",
	}
	second := model.Task{
		Name:         "asset-task-b",
		NodeID:       node.ID,
		ExecutorType: "restic",
		RsyncSource:  "/data/b",
		RsyncTarget:  "/backup/b",
		Status:       "pending",
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first task: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second task: %v", err)
	}
	now := time.Now().UTC()
	identity := configV2PrivateIdentity
	repository := model.BackupRepository{
		ID:                 configV2SourceRepositoryID,
		ProviderKind:       string(backupasset.ProviderRestic),
		RepositoryIdentity: &identity,
		DisplayName:        "shared-restic",
		Description:        "shared source",
		VersionMode:        string(backupasset.VersionNativeSnapshot),
		Status:             string(backupasset.RepositoryOnline),
		CapabilityRevision: 3,
		CapabilitiesJSON:   `{"proof":"should-not-export","grant":"should-not-export"}`,
		ImmutabilityLevel:  string(backupasset.ImmutabilityXirangManaged),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatalf("create repository: %v", err)
	}
	binding := model.RepositoryAccessBinding{
		ID:                configV2SourceBindingID,
		RepositoryID:      repository.ID,
		BindingKind:       "task_derived_v1",
		EncryptedConfig:   configV2BindingPlaintext(),
		ConfigFingerprint: configV2BindingFingerprint(),
		Status:            "active",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := db.Create(&binding).Error; err != nil {
		t.Fatalf("create binding: %v", err)
	}
	for index, task := range []model.Task{first, second} {
		linkID := configV2SourceLinkIDOne
		if index == 1 {
			linkID = configV2SourceLinkIDTwo
		}
		link := model.TaskRepositoryLink{
			ID:                     linkID,
			TaskID:                 &task.ID,
			RepositoryID:           repository.ID,
			TaskNameSnapshot:       task.Name,
			NodeIDSnapshot:         node.ID,
			NodeNameSnapshot:       node.Name,
			PublicationMode:        "native_snapshot",
			EncryptedLegacyLocator: configV2LegacyLocator,
			LinkedAt:               now,
			CreatedAt:              now,
			UpdatedAt:              now,
		}
		if err := db.Create(&link).Error; err != nil {
			t.Fatalf("create link %s: %v", linkID, err)
		}
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID:        configV2SourcePolicyID,
		ScopeKind: "repository",
		ScopeID:   repository.ID,
		Revision:  2,
		RulesJSON: `{"version":1,"count":{"keep_latest":3}}`,
		Status:    "active",
		CreatedBy: 1,
		UpdatedBy: 1,
		CreatedAt: now,
		UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if err := db.Create(&model.RecoveryPoint{
		ID:                   configV2SourcePointID,
		RepositoryID:         repository.ID,
		Semantics:            "native_snapshot",
		State:                "committed",
		HoldState:            "held",
		PointRevision:        4,
		CapabilityRevision:   3,
		CapabilitiesJSON:     `{"ticket":"should-not-export","fence":"should-not-export"}`,
		ImmutabilityLevel:    string(backupasset.ImmutabilityXirangManaged),
		PhysicalAvailability: "available",
		CreatedAt:            now,
		UpdatedAt:            now,
	}).Error; err != nil {
		t.Fatalf("create recovery point: %v", err)
	}
	if err := db.Create(&model.RecoveryPointHold{
		ID:              configV2SourceHoldID,
		RecoveryPointID: configV2SourcePointID,
		HoldType:        "legal",
		State:           "active",
		EncryptedReason: configV2HoldReason,
		CreatedBy:       1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}).Error; err != nil {
		t.Fatalf("create hold: %v", err)
	}
	return first, second
}

func serveConfigExport(t *testing.T, db *gorm.DB, includeSecrets bool) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewConfigHandler(db, nil)
	handler.assetProbe = func(operation string) {
		t.Fatalf("config export must not call Provider: %s", operation)
	}
	router := gin.New()
	router.GET("/config/export", func(c *gin.Context) {
		c.Set("user_id", uint(9))
		c.Set("username", "admin")
		c.Set("role", "admin")
		handler.Export(c)
	})
	path := "/config/export"
	if includeSecrets {
		path += "?include_secrets=true"
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", response.Code, response.Body.String())
	}
	return response
}

func parseConfigExportPayload(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("parse export envelope: %v", err)
	}
	if envelope.Data == nil {
		t.Fatal("export payload is empty")
	}
	return envelope.Data
}

func serveConfigImport(t *testing.T, db *gorm.DB, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewConfigHandler(db, nil)
	handler.assetProbe = func(operation string) {
		t.Fatalf("config import must not call Provider: %s", operation)
	}
	router := gin.New()
	router.POST("/config/import", func(c *gin.Context) {
		c.Set("user_id", uint(9))
		c.Set("userID", uint(9))
		c.Set("username", "admin")
		c.Set("role", "admin")
		handler.Import(c)
	})
	request := httptest.NewRequest(http.MethodPost, "/config/import?conflict=skip", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestConfigExportV2DefaultDocumentAndSafeStableRefs(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, db)
	seedConfigV2SharedAssetGraph(t, db)

	payload := parseConfigExportPayload(t, serveConfigExport(t, db, false))
	if payload["version"] != "2.0" {
		t.Fatalf("default export version=%v, want 2.0", payload["version"])
	}
	documentID, _ := payload["document_id"].(string)
	if err := backupasset.ValidateOpaqueID(documentID); err != nil {
		t.Fatalf("document_id %q is not a 32-hex opaque id: %v", documentID, err)
	}
	data, _ := payload["data"].(map[string]any)
	repositories, _ := data["backup_repositories"].([]any)
	links, _ := data["task_repository_links"].([]any)
	policies, _ := data["backup_retention_policies"].([]any)
	holds, _ := data["recovery_point_holds"].([]any)
	if len(repositories) != 1 || len(links) != 2 || len(policies) != 1 || len(holds) != 0 {
		t.Fatalf("asset graph sizes repos=%d links=%d policies=%d holds=%d", len(repositories), len(links), len(policies), len(holds))
	}
	repository, _ := repositories[0].(map[string]any)
	if repository["repository_ref"] == "" || repository["identity_ref"] != configV2IdentityRef() {
		t.Fatalf("repository refs=%#v", repository)
	}
	if repository["provider_kind"] != "restic" || repository["display_name"] != "shared-restic" || repository["version_mode"] != "native_snapshot" {
		t.Fatalf("safe repository fields=%#v", repository)
	}
	refs := map[string]struct{}{}
	for _, raw := range links {
		link, _ := raw.(map[string]any)
		if link["link_ref"] == "" || link["repository_ref"] != repository["repository_ref"] || link["task_ref"] == "" {
			t.Fatalf("link refs=%#v", link)
		}
		refs[link["link_ref"].(string)] = struct{}{}
	}
	if len(refs) != 2 {
		t.Fatalf("expected two distinct link_ref values, got %#v", refs)
	}
	policy, _ := policies[0].(map[string]any)
	if policy["policy_ref"] == "" || policy["repository_ref"] != repository["repository_ref"] {
		t.Fatalf("policy refs=%#v", policy)
	}
}

func TestConfigExportV2OmitsSecretsNumericIDsAndPrivateMaterial(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, db)
	seedConfigV2SharedAssetGraph(t, db)

	body := serveConfigExport(t, db, false).Body.String()
	for _, forbidden := range []string{
		configV2PrivateIdentity,
		configV2BindingSecret,
		configV2HoldReason,
		configV2LegacyLocator,
		configV2SourceRepositoryID,
		configV2SourceLinkIDOne,
		configV2SourceLinkIDTwo,
		configV2SourcePolicyID,
		configV2SourceHoldID,
		configV2SourcePointID,
		configV2SourceBindingID,
		`"proof"`,
		`"grant"`,
		`"ticket"`,
		`"fence"`,
		"encrypted_config",
		"encrypted_reason",
		"encrypted_legacy_locator",
		"provider_locator",
		"access_binding",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("default v2 export leaked %q: %s", forbidden, body)
		}
	}
	payload := parseConfigExportPayload(t, serveConfigExport(t, db, false))
	data, _ := payload["data"].(map[string]any)
	for _, key := range []string{"backup_repositories", "task_repository_links", "backup_retention_policies", "recovery_point_holds"} {
		raw, _ := json.Marshal(data[key])
		for _, numeric := range []string{`"id"`, `"task_id"`, `"node_id"`, `"policy_id"`, `"repository_id"`} {
			if strings.Contains(string(raw), numeric) {
				t.Fatalf("asset graph %s used numeric/source id field %s: %s", key, numeric, raw)
			}
		}
	}
}

func TestConfigExportV2SensitiveBindingOnlyWithIncludeSecrets(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, db)
	seedConfigV2SharedAssetGraph(t, db)

	sensitive := parseConfigExportPayload(t, serveConfigExport(t, db, true))
	data, _ := sensitive["data"].(map[string]any)
	repositories, _ := data["backup_repositories"].([]any)
	repository, _ := repositories[0].(map[string]any)
	binding, _ := repository["access_binding"].(map[string]any)
	if binding["binding_kind"] != "task_derived_v1" || binding["envelope"] == "" {
		t.Fatalf("sensitive export binding=%#v", binding)
	}
	if strings.Contains(serveConfigExport(t, db, false).Body.String(), "envelope") {
		t.Fatal("default export must omit the binding envelope")
	}
	var event model.CredentialAuditEvent
	if err := db.Where("action = ? AND metadata LIKE ?", "config.export", "%\"with_sensitive\":true%").First(&event).Error; err != nil {
		t.Fatalf("sensitive export audit: %v", err)
	}
	if strings.Contains(event.Metadata, configV2BindingSecret) || strings.Contains(event.Metadata, "envelope") {
		t.Fatalf("audit recorded envelope: %s", event.Metadata)
	}
	if !strings.Contains(event.Metadata, `"with_sensitive":true`) {
		t.Fatalf("audit missing with_sensitive: %s", event.Metadata)
	}
}

func TestConfigImportV2RejectsUnrestorableHolds(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
	data, _ := payload["data"].(map[string]any)
	repositories, _ := data["backup_repositories"].([]any)
	repository, _ := repositories[0].(map[string]any)
	data["recovery_point_holds"] = []map[string]any{{
		"hold_ref": "hold_1", "repository_ref": repository["repository_ref"], "hold_type": "legal", "state": "active",
	}}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	target := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, target)
	response := serveConfigImport(t, target, body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unrestorable hold import status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConfigImportV2SharedRepositoryRemapAndRepeatIdempotency(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	exported, err := json.Marshal(parseConfigExportPayload(t, serveConfigExport(t, source, false)))
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}

	target := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, target)
	node := model.Node{Name: "asset-node", Host: "10.80.0.99", Port: 22, Username: "root", AuthType: "key", BackupDir: "asset-node"}
	if err := target.Create(&node).Error; err != nil {
		t.Fatalf("create target node: %v", err)
	}
	for _, name := range []string{"asset-task-a", "asset-task-b"} {
		task := model.Task{Name: name, NodeID: node.ID, ExecutorType: "restic", Status: "pending"}
		if err := target.Create(&task).Error; err != nil {
			t.Fatalf("create target task %s: %v", name, err)
		}
	}

	first := serveConfigImport(t, target, exported)
	if first.Code != http.StatusOK {
		t.Fatalf("first import status=%d body=%s", first.Code, first.Body.String())
	}
	second := serveConfigImport(t, target, exported)
	if second.Code != http.StatusOK {
		t.Fatalf("repeat import status=%d body=%s", second.Code, second.Body.String())
	}

	var repositories []model.BackupRepository
	if err := target.Find(&repositories).Error; err != nil {
		t.Fatalf("list repositories: %v", err)
	}
	if len(repositories) != 1 {
		t.Fatalf("shared remap should create one repository, got %d", len(repositories))
	}
	if repositories[0].Status != string(backupasset.RepositoryDisconnected) {
		t.Fatalf("imported repository status=%q", repositories[0].Status)
	}
	if repositories[0].ID == configV2SourceRepositoryID {
		t.Fatal("import reused source repository id")
	}
	var links []model.TaskRepositoryLink
	if err := target.Find(&links).Error; err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(links) != 2 || links[0].RepositoryID != repositories[0].ID || links[1].RepositoryID != repositories[0].ID {
		t.Fatalf("shared links=%#v repo=%s", links, repositories[0].ID)
	}
	if links[0].ID == configV2SourceLinkIDOne || links[1].ID == configV2SourceLinkIDTwo {
		t.Fatal("import reused source link ids")
	}
	var policies []model.BackupRetentionPolicy
	if err := target.Find(&policies).Error; err != nil || len(policies) != 1 || policies[0].ScopeID != repositories[0].ID {
		t.Fatalf("imported policy=%#v err=%v", policies, err)
	}
	if policies[0].CreatedBy != 9 || policies[0].UpdatedBy != 9 {
		t.Fatalf("imported policy actor=%d/%d, want 9/9", policies[0].CreatedBy, policies[0].UpdatedBy)
	}
}

func TestConfigImportV2WholeGraphConflictRollback(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	exported, err := json.Marshal(parseConfigExportPayload(t, serveConfigExport(t, source, false)))
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}

	target := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, target)
	now := time.Now().UTC()
	identity := configImportedIdentityRefPrefix + configV2IdentityRef()
	if err := target.Create(&model.BackupRepository{
		ID:                 "99999999999999999999999999999999",
		ProviderKind:       string(backupasset.ProviderRestic),
		RepositoryIdentity: &identity,
		DisplayName:        "preexisting",
		VersionMode:        string(backupasset.VersionNativeSnapshot),
		Status:             string(backupasset.RepositoryOffline),
		CapabilityRevision: 1,
		CapabilitiesJSON:   "{}",
		ImmutabilityLevel:  string(backupasset.ImmutabilityMutable),
		CreatedAt:          now,
		UpdatedAt:          now,
	}).Error; err != nil {
		t.Fatalf("create conflicting repository: %v", err)
	}

	response := serveConfigImport(t, target, exported)
	if response.Code != http.StatusConflict && response.Code != http.StatusBadRequest {
		t.Fatalf("conflict import status=%d body=%s", response.Code, response.Body.String())
	}
	var links int64
	if err := target.Model(&model.TaskRepositoryLink{}).Count(&links).Error; err != nil || links != 0 {
		t.Fatalf("conflict left %d links err=%v", links, err)
	}
	var policies int64
	if err := target.Model(&model.BackupRetentionPolicy{}).Count(&policies).Error; err != nil || policies != 0 {
		t.Fatalf("conflict left %d policies err=%v", policies, err)
	}
	var imported int64
	if err := target.Model(&model.BackupRepository{}).Count(&imported).Error; err != nil || imported != 1 {
		t.Fatalf("conflict should keep only the preexisting repository, count=%d err=%v", imported, err)
	}
}

func TestConfigImportV2DisconnectedBindingsZeroProviderCalls(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	exported, err := json.Marshal(parseConfigExportPayload(t, serveConfigExport(t, source, true)))
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}

	target := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, target)
	response := serveConfigImport(t, target, exported)
	if response.Code != http.StatusOK {
		t.Fatalf("sensitive import status=%d body=%s", response.Code, response.Body.String())
	}
	var repository model.BackupRepository
	if err := target.First(&repository).Error; err != nil {
		t.Fatalf("imported repository: %v", err)
	}
	if repository.Status != string(backupasset.RepositoryDisconnected) {
		t.Fatalf("imported status=%q", repository.Status)
	}
	var bindings []model.RepositoryAccessBinding
	if err := target.Find(&bindings).Error; err != nil || len(bindings) != 1 {
		t.Fatalf("imported bindings=%#v err=%v", bindings, err)
	}
	if bindings[0].Status != "revoked" || bindings[0].RevokedAt == nil {
		t.Fatalf("imported binding should stay disconnected: %#v", bindings[0])
	}
	if bindings[0].EncryptedConfig != configV2BindingPlaintext() {
		t.Fatalf("imported binding should decrypt to the envelope, got %q", bindings[0].EncryptedConfig)
	}
	var stored string
	if err := target.Raw("SELECT encrypted_config FROM repository_access_bindings LIMIT 1").Scan(&stored).Error; err != nil {
		t.Fatalf("read stored binding: %v", err)
	}
	if stored == "" || stored == configV2BindingPlaintext() {
		t.Fatalf("binding must be stored encrypted, got %q", stored)
	}
}

func TestConfigImportV1Compatibility(t *testing.T) {
	target := openConfigHandlerTestDB(t)
	if err := target.AutoMigrate(&model.Node{}, &model.Policy{}, &model.Task{}, &model.SystemSetting{}, &model.SSHKey{}, &model.BackupRepository{}); err != nil {
		t.Fatalf("migrate v1 target: %v", err)
	}
	body := `{"version":"1.0","exported_at":"2026-03-24T00:00:00Z","data":{"nodes":[{"name":"v1-node","host":"10.0.0.1","port":22,"username":"root","auth_type":"key"}]}}`
	response := serveConfigImport(t, target, []byte(body))
	if response.Code != http.StatusOK {
		t.Fatalf("v1 import status=%d body=%s", response.Code, response.Body.String())
	}
	var count int64
	if err := target.Model(&model.Node{}).Where("name = ?", "v1-node").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("v1 import node count=%d err=%v", count, err)
	}
	var repos int64
	if err := target.Model(&model.BackupRepository{}).Count(&repos).Error; err != nil {
		t.Fatalf("count repos: %v", err)
	}
	if repos != 0 {
		t.Fatalf("v1 import must not create backup repositories, got %d", repos)
	}
}

func TestConfigAssetGraphChangedMappingAborts(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
	firstBody, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}

	target := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, target)
	if response := serveConfigImport(t, target, firstBody); response.Code != http.StatusOK {
		t.Fatalf("first import status=%d body=%s", response.Code, response.Body.String())
	}
	var original model.BackupRepository
	if err := target.First(&original).Error; err != nil {
		t.Fatalf("original repository: %v", err)
	}

	data, _ := payload["data"].(map[string]any)
	repositories, _ := data["backup_repositories"].([]any)
	repository, _ := repositories[0].(map[string]any)
	repository["identity_ref"] = strings.Repeat("c", 64)
	repositories[0] = repository
	data["backup_repositories"] = repositories
	payload["data"] = data
	changed, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal changed: %v", err)
	}
	response := serveConfigImport(t, target, changed)
	if response.Code != http.StatusConflict && response.Code != http.StatusBadRequest {
		t.Fatalf("changed mapping status=%d body=%s", response.Code, response.Body.String())
	}
	var after model.BackupRepository
	if err := target.First(&after).Error; err != nil {
		t.Fatalf("reload repository: %v", err)
	}
	if after.ID != original.ID {
		t.Fatalf("changed mapping replaced repository %s -> %s", original.ID, after.ID)
	}
	if after.RepositoryIdentity == nil || *after.RepositoryIdentity != *original.RepositoryIdentity {
		t.Fatalf("changed mapping mutated identity %v -> %v", original.RepositoryIdentity, after.RepositoryIdentity)
	}
}

func TestConfigExportV2AuditRecordsCountsWithoutEnvelope(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, db)
	seedConfigV2SharedAssetGraph(t, db)
	serveConfigExport(t, db, false)

	var event model.CredentialAuditEvent
	if err := db.Where("action = ?", "config.export").First(&event).Error; err != nil {
		t.Fatalf("export audit: %v", err)
	}
	if event.Outcome != credentialaudit.OutcomeSuccess {
		t.Fatalf("audit outcome=%s", event.Outcome)
	}
	for _, required := range []string{"repository_count", "link_count", "retention_policy_count", "hold_count", `"with_sensitive":false`} {
		if !strings.Contains(event.Metadata, required) {
			t.Fatalf("audit metadata missing %s: %s", required, event.Metadata)
		}
	}
	if !strings.Contains(event.Metadata, `"retention_policy_count":1`) || !strings.Contains(event.Metadata, `"hold_count":0`) {
		t.Fatalf("audit asset counts are wrong: %s", event.Metadata)
	}
}

func TestConfigAssetGraphChangedLinkMappingAborts(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
	firstBody, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	target := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, target)
	if response := serveConfigImport(t, target, firstBody); response.Code != http.StatusOK {
		t.Fatalf("first import status=%d body=%s", response.Code, response.Body.String())
	}

	data, _ := payload["data"].(map[string]any)
	links, _ := data["task_repository_links"].([]any)
	link, _ := links[0].(map[string]any)
	link["publication_mode"] = "legacy_mutable"
	links[0] = link
	data["task_repository_links"] = links
	payload["data"] = data
	changed, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal changed: %v", err)
	}
	response := serveConfigImport(t, target, changed)
	if response.Code != http.StatusConflict {
		t.Fatalf("changed link mapping status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConfigImportV2RepeatSensitiveAddsBinding(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	payload := parseConfigExportPayload(t, serveConfigExport(t, source, true))
	data, _ := payload["data"].(map[string]any)
	repositories, _ := data["backup_repositories"].([]any)
	repository, _ := repositories[0].(map[string]any)
	delete(repository, "access_binding")
	repositories[0] = repository
	data["backup_repositories"] = repositories
	payload["data"] = data
	stripped, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal stripped: %v", err)
	}
	target := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, target)
	if response := serveConfigImport(t, target, stripped); response.Code != http.StatusOK {
		t.Fatalf("default import status=%d body=%s", response.Code, response.Body.String())
	}
	var bindings int64
	if err := target.Model(&model.RepositoryAccessBinding{}).Count(&bindings).Error; err != nil || bindings != 0 {
		t.Fatalf("default import should omit bindings, count=%d err=%v", bindings, err)
	}

	sensitive := parseConfigExportPayload(t, serveConfigExport(t, source, true))
	sensitive["document_id"] = payload["document_id"]
	full, err := json.Marshal(sensitive)
	if err != nil {
		t.Fatalf("marshal sensitive: %v", err)
	}
	if response := serveConfigImport(t, target, full); response.Code != http.StatusOK {
		t.Fatalf("sensitive rematch status=%d body=%s", response.Code, response.Body.String())
	}
	if err := target.Model(&model.RepositoryAccessBinding{}).Count(&bindings).Error; err != nil || bindings != 1 {
		t.Fatalf("sensitive rematch should store one binding, count=%d err=%v", bindings, err)
	}
}

func TestConfigImportV2BoundIdentityRematchAndConflict(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
	firstBody, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	target := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, target)
	if response := serveConfigImport(t, target, firstBody); response.Code != http.StatusOK {
		t.Fatalf("first import status=%d body=%s", response.Code, response.Body.String())
	}
	identity := configV2PrivateIdentity
	if err := target.Model(&model.BackupRepository{}).Where("1 = 1").Update("repository_identity", identity).Error; err != nil {
		t.Fatalf("simulate connect bind: %v", err)
	}
	if response := serveConfigImport(t, target, firstBody); response.Code != http.StatusOK {
		t.Fatalf("repeat import after bind status=%d body=%s", response.Code, response.Body.String())
	}
	var count int64
	if err := target.Model(&model.BackupRepository{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("repeat after bind should stay one repository, count=%d err=%v", count, err)
	}

	payload["document_id"] = strings.Repeat("c", 32)
	secondBody, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	response := serveConfigImport(t, target, secondBody)
	if response.Code != http.StatusConflict {
		t.Fatalf("bound identity conflict status=%d body=%s", response.Code, response.Body.String())
	}
	if err := target.Model(&model.BackupRepository{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("conflict should not fork the bound repository, count=%d err=%v", count, err)
	}
}

func TestConfigImportV2DuplicateActiveLinkConflicts(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
	firstBody, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	target := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, target)
	node := model.Node{Name: "asset-node", Host: "10.80.0.99", Port: 22, Username: "root", AuthType: "key", BackupDir: "asset-node"}
	if err := target.Create(&node).Error; err != nil {
		t.Fatalf("create target node: %v", err)
	}
	for _, name := range []string{"asset-task-a", "asset-task-b"} {
		task := model.Task{Name: name, NodeID: node.ID, ExecutorType: "restic", Status: "pending"}
		if err := target.Create(&task).Error; err != nil {
			t.Fatalf("create target task %s: %v", name, err)
		}
	}
	if response := serveConfigImport(t, target, firstBody); response.Code != http.StatusOK {
		t.Fatalf("first import status=%d body=%s", response.Code, response.Body.String())
	}

	payload["document_id"] = strings.Repeat("b", 32)
	data, _ := payload["data"].(map[string]any)
	repositories, _ := data["backup_repositories"].([]any)
	repository, _ := repositories[0].(map[string]any)
	repository["identity_ref"] = strings.Repeat("d", 64)
	repositories[0] = repository
	data["backup_repositories"] = repositories
	payload["data"] = data
	secondBody, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	response := serveConfigImport(t, target, secondBody)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate active link status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConfigExportV2OmitsUnlinkedAndArchivedTaskLinks(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, db)
	first, second := seedConfigV2SharedAssetGraph(t, db)
	now := time.Now().UTC()
	archivedAt := now
	archived := model.Task{
		Name: "archived-task", NodeID: first.NodeID, ExecutorType: "restic", Status: "pending", ArchivedAt: &archivedAt,
	}
	if err := db.Create(&archived).Error; err != nil {
		t.Fatalf("create archived task: %v", err)
	}
	unlinkedAt := now
	if err := db.Create(&model.TaskRepositoryLink{
		ID: strings.Repeat("1", 32), TaskID: &second.ID, RepositoryID: configV2SourceRepositoryID,
		TaskNameSnapshot: second.Name, NodeNameSnapshot: "asset-node", PublicationMode: "native_object_versions",
		LinkedAt: now.Add(-time.Hour), UnlinkedAt: &unlinkedAt, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create unlinked historical link: %v", err)
	}
	if err := db.Create(&model.TaskRepositoryLink{
		ID: strings.Repeat("2", 32), TaskID: &archived.ID, RepositoryID: configV2SourceRepositoryID,
		TaskNameSnapshot: archived.Name, NodeNameSnapshot: "asset-node", PublicationMode: "native_object_versions",
		LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create archived-task link: %v", err)
	}
	payload := parseConfigExportPayload(t, serveConfigExport(t, db, false))
	data, _ := payload["data"].(map[string]any)
	links, _ := data["task_repository_links"].([]any)
	if len(links) != 2 {
		t.Fatalf("export links=%d, want only the two live active links", len(links))
	}
	for _, raw := range links {
		link, _ := raw.(map[string]any)
		if link["task_ref"] == "archived-task" || strings.Contains(fmt.Sprint(link), "archived") {
			t.Fatalf("exported archived or unlinked task graph: %#v", link)
		}
	}
}

func TestConfigImportV2RejectsArchivedTaskActiveLinkAndProviderDrift(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
	exported, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}

	archivedTarget := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, archivedTarget)
	node := model.Node{Name: "asset-node", Host: "10.80.0.99", Port: 22, Username: "root", AuthType: "key", BackupDir: "asset-node"}
	if err := archivedTarget.Create(&node).Error; err != nil {
		t.Fatalf("create archived target node: %v", err)
	}
	archivedAt := time.Now().UTC()
	for _, name := range []string{"asset-task-a", "asset-task-b"} {
		task := model.Task{Name: name, NodeID: node.ID, ExecutorType: "restic", Status: "pending"}
		if name == "asset-task-a" {
			task.ArchivedAt = &archivedAt
		}
		if err := archivedTarget.Create(&task).Error; err != nil {
			t.Fatalf("create target task %s: %v", name, err)
		}
	}
	archivedImport := serveConfigImport(t, archivedTarget, exported)
	if archivedImport.Code == http.StatusOK {
		t.Fatalf("import activated an archived task link: %s", archivedImport.Body.String())
	}

	liveTarget := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, liveTarget)
	liveNode := model.Node{Name: "asset-node", Host: "10.80.0.88", Port: 22, Username: "root", AuthType: "key", BackupDir: "asset-node"}
	if err := liveTarget.Create(&liveNode).Error; err != nil {
		t.Fatalf("create live target node: %v", err)
	}
	for _, name := range []string{"asset-task-a", "asset-task-b"} {
		task := model.Task{Name: name, NodeID: liveNode.ID, ExecutorType: "restic", Status: "pending"}
		if err := liveTarget.Create(&task).Error; err != nil {
			t.Fatalf("create live target task %s: %v", name, err)
		}
	}
	first := serveConfigImport(t, liveTarget, exported)
	if first.Code != http.StatusOK {
		t.Fatalf("first live import status=%d body=%s", first.Code, first.Body.String())
	}
	data, _ := payload["data"].(map[string]any)
	repositories, _ := data["backup_repositories"].([]any)
	repository, _ := repositories[0].(map[string]any)
	repository["provider_kind"] = "rclone"
	repository["immutability_level"] = "storage_worm"
	repositories[0] = repository
	data["backup_repositories"] = repositories
	payload["data"] = data
	drifted, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal drifted export: %v", err)
	}
	driftedImport := serveConfigImport(t, liveTarget, drifted)
	if driftedImport.Code == http.StatusOK {
		t.Fatalf("drifted provider/immutability rematch succeeded: %s", driftedImport.Body.String())
	}
}

func seedConfigV2ImportTarget(t *testing.T, db *gorm.DB) {
	t.Helper()
	node := model.Node{Name: "asset-node", Host: "10.80.0.40", Port: 22, Username: "root", AuthType: "key", BackupDir: "asset-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create target node: %v", err)
	}
	for _, name := range []string{"asset-task-a", "asset-task-b"} {
		task := model.Task{Name: name, NodeID: node.ID, ExecutorType: "restic", Status: "pending"}
		if err := db.Create(&task).Error; err != nil {
			t.Fatalf("create target task %s: %v", name, err)
		}
	}
}

func TestConfigImportV2ReplayChecksMappedEntityLifecycle(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)
	payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
	exported, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}

	t.Run("unlinked mapped link", func(t *testing.T) {
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		if response := serveConfigImport(t, target, exported); response.Code != http.StatusOK {
			t.Fatalf("first import status=%d body=%s", response.Code, response.Body.String())
		}
		now := time.Now().UTC()
		if err := target.Model(&model.TaskRepositoryLink{}).Where("unlinked_at IS NULL").Update("unlinked_at", now).Error; err != nil {
			t.Fatalf("unlink mapped links: %v", err)
		}
		replay := serveConfigImport(t, target, exported)
		if replay.Code == http.StatusOK {
			t.Fatalf("replay succeeded after mapped link was unlinked: %s", replay.Body.String())
		}
	})

	t.Run("archived mapped task", func(t *testing.T) {
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		if response := serveConfigImport(t, target, exported); response.Code != http.StatusOK {
			t.Fatalf("first import status=%d body=%s", response.Code, response.Body.String())
		}
		now := time.Now().UTC()
		if err := target.Model(&model.Task{}).Where("name = ?", "asset-task-a").Update("archived_at", now).Error; err != nil {
			t.Fatalf("archive mapped task: %v", err)
		}
		replay := serveConfigImport(t, target, exported)
		if replay.Code == http.StatusOK {
			t.Fatalf("replay succeeded after mapped task was archived: %s", replay.Body.String())
		}
	})

	t.Run("deleted mapped policy", func(t *testing.T) {
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		if response := serveConfigImport(t, target, exported); response.Code != http.StatusOK {
			t.Fatalf("first import status=%d body=%s", response.Code, response.Body.String())
		}
		now := time.Now().UTC()
		if err := target.Model(&model.BackupRetentionPolicy{}).Where("status = ?", "active").Updates(map[string]any{
			"status":     "deleted",
			"deleted_at": now,
		}).Error; err != nil {
			t.Fatalf("delete mapped policy: %v", err)
		}
		replay := serveConfigImport(t, target, exported)
		if replay.Code == http.StatusOK {
			t.Fatalf("replay succeeded after mapped policy was deleted: %s", replay.Body.String())
		}
	})

	t.Run("policy revision drift", func(t *testing.T) {
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		if response := serveConfigImport(t, target, exported); response.Code != http.StatusOK {
			t.Fatalf("first import status=%d body=%s", response.Code, response.Body.String())
		}
		if err := target.Model(&model.BackupRetentionPolicy{}).Where("status = ?", "active").Update("revision", int64(99)).Error; err != nil {
			t.Fatalf("drift mapped policy revision: %v", err)
		}
		replay := serveConfigImport(t, target, exported)
		if replay.Code == http.StatusOK {
			t.Fatalf("replay succeeded after mapped policy revision drifted: %s", replay.Body.String())
		}
	})
}

func TestConfigImportV2RejectsOpenEnumsAndBindingFingerprintMismatch(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)

	t.Run("unknown publication_mode", func(t *testing.T) {
		payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
		data, _ := payload["data"].(map[string]any)
		links, _ := data["task_repository_links"].([]any)
		link, _ := links[0].(map[string]any)
		link["publication_mode"] = "not_a_publication_mode"
		links[0] = link
		data["task_repository_links"] = links
		payload["data"] = data
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal invalid mode: %v", err)
		}
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		response := serveConfigImport(t, target, body)
		if response.Code == http.StatusOK {
			t.Fatalf("unknown publication_mode was accepted: %s", response.Body.String())
		}
	})

	t.Run("unknown binding_kind", func(t *testing.T) {
		payload := parseConfigExportPayload(t, serveConfigExport(t, source, true))
		data, _ := payload["data"].(map[string]any)
		repositories, _ := data["backup_repositories"].([]any)
		repository, _ := repositories[0].(map[string]any)
		binding, _ := repository["access_binding"].(map[string]any)
		binding["binding_kind"] = "not_a_binding_kind"
		repository["access_binding"] = binding
		repositories[0] = repository
		data["backup_repositories"] = repositories
		payload["data"] = data
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal invalid binding: %v", err)
		}
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		response := serveConfigImport(t, target, body)
		if response.Code == http.StatusOK {
			t.Fatalf("unknown binding_kind was accepted: %s", response.Body.String())
		}
	})

	t.Run("same fingerprint different envelope", func(t *testing.T) {
		payload := parseConfigExportPayload(t, serveConfigExport(t, source, true))
		data, _ := payload["data"].(map[string]any)
		repositories, _ := data["backup_repositories"].([]any)
		repository, _ := repositories[0].(map[string]any)
		binding, _ := repository["access_binding"].(map[string]any)
		tampered, err := secure.EncryptIfNeeded("FAKE_DIFFERENT_CONFIG_V2_BINDING_SECRET")
		if err != nil {
			t.Fatalf("encrypt tampered envelope: %v", err)
		}
		binding["envelope"] = tampered
		repository["access_binding"] = binding
		repositories[0] = repository
		data["backup_repositories"] = repositories
		payload["data"] = data
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal tampered binding: %v", err)
		}
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		response := serveConfigImport(t, target, body)
		if response.Code == http.StatusOK {
			t.Fatalf("fingerprint/envelope mismatch was accepted: %s", response.Body.String())
		}
		var bindings int64
		if err := target.Model(&model.RepositoryAccessBinding{}).Count(&bindings).Error; err != nil || bindings != 0 {
			t.Fatalf("mismatch must have zero Provider/binding writes, count=%d err=%v", bindings, err)
		}
	})

	t.Run("sha256 plaintext fingerprint against hmac export", func(t *testing.T) {
		payload := parseConfigExportPayload(t, serveConfigExport(t, source, true))
		data, _ := payload["data"].(map[string]any)
		repositories, _ := data["backup_repositories"].([]any)
		repository, _ := repositories[0].(map[string]any)
		binding, _ := repository["access_binding"].(map[string]any)
		sum := sha256.Sum256([]byte(configV2BindingPlaintext()))
		binding["config_fingerprint"] = hex.EncodeToString(sum[:])
		repository["access_binding"] = binding
		repositories[0] = repository
		data["backup_repositories"] = repositories
		payload["data"] = data
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal sha256 fingerprint: %v", err)
		}
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		response := serveConfigImport(t, target, body)
		if response.Code == http.StatusOK {
			t.Fatalf("sha256 plaintext fingerprint was accepted against hmac export: %s", response.Body.String())
		}
	})

	t.Run("empty claimed fingerprint is rejected", func(t *testing.T) {
		payload := parseConfigExportPayload(t, serveConfigExport(t, source, true))
		data, _ := payload["data"].(map[string]any)
		repositories, _ := data["backup_repositories"].([]any)
		repository, _ := repositories[0].(map[string]any)
		binding, _ := repository["access_binding"].(map[string]any)
		binding["config_fingerprint"] = ""
		repository["access_binding"] = binding
		repositories[0] = repository
		data["backup_repositories"] = repositories
		payload["data"] = data
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal empty fingerprint: %v", err)
		}
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		response := serveConfigImport(t, target, body)
		if response.Code == http.StatusOK {
			t.Fatalf("empty claimed fingerprint was accepted: %s", response.Body.String())
		}
	})

	t.Run("restic repository native_object_versions is rejected", func(t *testing.T) {
		payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
		data, _ := payload["data"].(map[string]any)
		repositories, _ := data["backup_repositories"].([]any)
		repository, _ := repositories[0].(map[string]any)
		repository["version_mode"] = "native_object_versions"
		repositories[0] = repository
		links, _ := data["task_repository_links"].([]any)
		link, _ := links[0].(map[string]any)
		link["publication_mode"] = "native_snapshot"
		links[0] = link
		data["backup_repositories"] = repositories
		data["task_repository_links"] = links
		payload["data"] = data
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal restic repository native versions: %v", err)
		}
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		response := serveConfigImport(t, target, body)
		if response.Code == http.StatusOK {
			t.Fatalf("restic repository + native_object_versions was accepted: %s", response.Body.String())
		}
	})

	t.Run("restic native_object_versions is rejected", func(t *testing.T) {
		payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
		data, _ := payload["data"].(map[string]any)
		links, _ := data["task_repository_links"].([]any)
		link, _ := links[0].(map[string]any)
		link["publication_mode"] = "native_object_versions"
		links[0] = link
		data["task_repository_links"] = links
		payload["data"] = data
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal restic native versions: %v", err)
		}
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		response := serveConfigImport(t, target, body)
		if response.Code == http.StatusOK {
			t.Fatalf("restic + native_object_versions was accepted: %s", response.Body.String())
		}
	})
}

func TestConfigImportV2RejectsIllegalRetentionPolicyRevisionAndStatus(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	source := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, source)
	seedConfigV2SharedAssetGraph(t, source)

	mutatePolicy := func(t *testing.T, mutate func(map[string]any)) []byte {
		t.Helper()
		payload := parseConfigExportPayload(t, serveConfigExport(t, source, false))
		data, _ := payload["data"].(map[string]any)
		policies, _ := data["backup_retention_policies"].([]any)
		if len(policies) == 0 {
			t.Fatal("export omitted retention policies")
		}
		policy, _ := policies[0].(map[string]any)
		mutate(policy)
		policies[0] = policy
		data["backup_retention_policies"] = policies
		payload["data"] = data
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal mutated policy: %v", err)
		}
		return body
	}
	importMustReject := func(t *testing.T, body []byte, label string) {
		t.Helper()
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		response := serveConfigImport(t, target, body)
		if response.Code == http.StatusOK {
			t.Fatalf("%s was accepted: %s", label, response.Body.String())
		}
		var policies int64
		if err := target.Model(&model.BackupRetentionPolicy{}).Count(&policies).Error; err != nil || policies != 0 {
			t.Fatalf("%s must write zero policies, count=%d err=%v", label, policies, err)
		}
	}

	t.Run("revision 0 is rejected", func(t *testing.T) {
		importMustReject(t, mutatePolicy(t, func(policy map[string]any) { policy["revision"] = 0 }), "revision 0")
	})
	t.Run("status deleted is rejected", func(t *testing.T) {
		importMustReject(t, mutatePolicy(t, func(policy map[string]any) { policy["status"] = "deleted" }), "status deleted")
	})
	t.Run("status garbage is rejected", func(t *testing.T) {
		importMustReject(t, mutatePolicy(t, func(policy map[string]any) { policy["status"] = "garbage" }), "status garbage")
	})
	t.Run("active revision at least 1 still imports", func(t *testing.T) {
		body := mutatePolicy(t, func(policy map[string]any) {
			policy["revision"] = 2
			policy["status"] = "active"
		})
		target := openConfigHandlerTestDB(t)
		migrateConfigAssetGraphDB(t, target)
		seedConfigV2ImportTarget(t, target)
		response := serveConfigImport(t, target, body)
		if response.Code != http.StatusOK {
			t.Fatalf("valid active policy import status=%d body=%s", response.Code, response.Body.String())
		}
		var stored model.BackupRetentionPolicy
		if err := target.First(&stored).Error; err != nil {
			t.Fatalf("load imported policy: %v", err)
		}
		if stored.Revision != 2 || stored.Status != string(backupasset.RetentionPolicyActive) {
			t.Fatalf("imported policy revision=%d status=%q, want 2/active", stored.Revision, stored.Status)
		}
	})
}
