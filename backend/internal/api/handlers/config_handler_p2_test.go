package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"xirang/backend/internal/model"
	nodePkg "xirang/backend/internal/node"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
)

const (
	configNameMappingWarningDuplicateName     = "duplicate_name"
	configNameMappingWarningInvalidReference  = "invalid_reference"
	configNameMappingWarningReferenceConflict = "reference_conflict"
)

func TestConfigImportNameMappingSQLite(t *testing.T) {
	runConfigImportNameMapping(t, "sqlite")
}

func TestConfigImportNameMappingPostgres(t *testing.T) {
	runConfigImportNameMapping(t, "postgres")
}

func runConfigImportNameMapping(t *testing.T, engine string) {
	t.Helper()
	t.Run("export_import_name_and_id_mapping", func(t *testing.T) {
		runConfigNameMappingExportImport(t, engine)
	})
	t.Run("export_snapshot_consistency", func(t *testing.T) {
		runConfigNameMappingExportSnapshotConsistency(t, engine)
	})
	t.Run("reference_validation_skip_and_overwrite", func(t *testing.T) {
		runConfigNameMappingReferenceSemantics(t, engine)
	})
	t.Run("skip_name_resolution", func(t *testing.T) {
		runConfigNameMappingSkipResolution(t, engine)
	})
	t.Run("scope_safety_combinations", func(t *testing.T) {
		runConfigNameMappingScopeSafetyCombinations(t, engine)
	})
	t.Run("legacy_auth_type_compatibility", func(t *testing.T) {
		runConfigNameMappingAuthTypeCompatibility(t, engine)
	})
	t.Run("corrupted_unrelated_node_secret", func(t *testing.T) {
		runConfigNameMappingCorruptedUnrelatedSecret(t, engine)
	})
	t.Run("duplicate_input_names", func(t *testing.T) {
		runConfigNameMappingDuplicateNames(t, engine)
	})
	t.Run("version_compatibility", func(t *testing.T) {
		runConfigNameMappingVersionCompatibility(t, engine)
	})
	t.Run("shared_scope_and_auth_path", func(t *testing.T) {
		runConfigNameMappingScopeAuth(t, engine)
	})
	t.Run("faults_abort_the_transaction", func(t *testing.T) {
		runConfigNameMappingFaults(t, engine)
	})
	t.Run("runtime_failure_preserves_concurrent_journal_change", func(t *testing.T) {
		runConfigNameMappingRuntimeJournal(t, engine)
	})
	t.Run("runtime_failure_full_restore", func(t *testing.T) {
		runConfigNameMappingRuntimeFullRestore(t, engine)
	})
	t.Run("runtime_failure_rejected_name_race", func(t *testing.T) {
		runConfigNameMappingRejectedNameRuntimeRace(t, engine)
	})
	t.Run("row_contention", func(t *testing.T) {
		runConfigNameMappingContention(t, engine)
	})
	t.Run("concurrent_missing_name_creation", func(t *testing.T) {
		runConfigNameMappingConcurrentMissingName(t, engine)
	})
}

func configNameMappingPrivateKey(t *testing.T, first byte) string {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = first + byte(i)
	}
	key := ed25519.NewKeyFromSeed(seed)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal name-mapping private key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func configNameMappingKey(t *testing.T, db *gorm.DB, name, privateKey string, disabled bool) model.SSHKey {
	t.Helper()
	key := model.SSHKey{
		Name:       name,
		Username:   "deploy",
		KeyType:    "auto",
		PrivateKey: privateKey,
		Disabled:   disabled,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create name-mapping key %q: %v", name, err)
	}
	key.PrivateKey = privateKey
	return key
}

func configNameMappingNode(t *testing.T, db *gorm.DB, name string) model.Node {
	t.Helper()
	node := model.Node{
		Name:      name,
		Host:      "10.250.0.10",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		BackupDir: nodePkg.SanitizeBackupDir(name),
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create name-mapping node %q: %v", name, err)
	}
	return node
}
func configNameMappingKeySummary(key model.SSHKey) string {
	return fmt.Sprintf("id=%d name=%q username=%q disabled=%t scope=%q private_key_present=%t", key.ID, key.Name, key.Username, key.Disabled, key.AllowedNodeIDs, strings.TrimSpace(key.PrivateKey) != "")
}

func configNameMappingNodeSummary(node model.Node) string {
	sshKeyID := "nil"
	if node.SSHKeyID != nil {
		sshKeyID = strconv.FormatUint(uint64(*node.SSHKeyID), 10)
	}
	return fmt.Sprintf("id=%d name=%q host=%q username=%q ssh_key_id=%s archived=%t password_present=%t private_key_present=%t", node.ID, node.Name, node.Host, node.Username, sshKeyID, node.Archived, strings.TrimSpace(node.Password) != "", strings.TrimSpace(node.PrivateKey) != "")
}

func openConfigNameMappingSQLitePair(t *testing.T, immediate bool) (*gorm.DB, *gorm.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), handlerTestDBName(t)+".db")
	targetQuery := "?_busy_timeout=5000&_loc=UTC"
	concurrentQuery := targetQuery
	if immediate {
		targetQuery += "&_txlock=immediate"
		concurrentQuery = "?_busy_timeout=0&_loc=UTC"
	}
	open := func(query string) *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dbPath+query), &gorm.Config{})
		if err != nil {
			t.Fatalf("open SQLite name-mapping database: %v", err)
		}
		return db
	}
	return open(targetQuery), open(concurrentQuery)
}
func openConfigNameMappingSQLiteSnapshotPair(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), handlerTestDBName(t)+".db")
	dsn := dbPath + "?_busy_timeout=5000&_journal_mode=WAL&_loc=UTC"
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatalf("open SQLite snapshot database: %v", err)
		}
		return db
	}
	return open(), open()
}

func configNameMappingImportRequest(handler *ConfigHandler, conflict string, body []byte) (*httptest.ResponseRecorder, configImportResult, error) {
	router := gin.New()
	router.POST("/config/import", handler.Import)
	path := "/config/import"
	if conflict != "" {
		path += "?conflict=" + conflict
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var envelope struct {
		Data configImportResult `json:"data"`
	}
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			return response, configImportResult{}, err
		}
	}
	return response, envelope.Data, nil
}

func serveConfigNameMappingImport(t *testing.T, db *gorm.DB, service *settings.Service, conflict string, body any) (*httptest.ResponseRecorder, configImportResult) {
	return serveP1ConfigImport(t, db, service, conflict, body)
}

func configNameMappingWarningsAt(result configImportResult, entity string, index int) []string {
	codes := make([]string, 0)
	for _, warning := range result.Warnings {
		if warning.Entity == entity && warning.Index == index {
			codes = append(codes, warning.Code)
		}
	}
	return codes
}

func requireConfigNameMappingWarning(t *testing.T, result configImportResult, entity string, index int, code string) {
	t.Helper()
	for _, got := range configNameMappingWarningsAt(result, entity, index) {
		if got == code {
			return
		}
	}
	t.Fatalf("missing warning entity=%s index=%d code=%q, warnings=%+v", entity, index, code, result.Warnings)
}

func assertConfigNameMappingWarningCodesUnique(t *testing.T, result configImportResult) {
	t.Helper()
	seen := make(map[string]struct{}, len(result.Warnings))
	for _, warning := range result.Warnings {
		key := fmt.Sprintf("%s/%d/%s", warning.Entity, warning.Index, warning.Code)
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate warning code for input item: %s; warnings=%+v", key, result.Warnings)
		}
		seen[key] = struct{}{}
	}
}

func configNameMappingRecord(t *testing.T, records any, name string) map[string]any {
	t.Helper()
	items, ok := records.([]any)
	if !ok {
		t.Fatalf("export records have type %T, want []any", records)
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("export record has type %T, want map[string]any", raw)
		}
		if item["name"] == name {
			return item
		}
	}
	t.Fatalf("export record %q not found in %#v", name, records)
	return nil
}

func runConfigNameMappingExportImport(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	source, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	target, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateConfigAssetGraphDB(t, source)
	migrateConfigAssetGraphDB(t, target)

	sourceFillerKey := configNameMappingKey(t, source, "source-id-filler-key", "", false)
	if sourceFillerKey.ID != 1 {
		t.Fatalf("source filler key ID=%d, want 1 for deterministic cross-database fixture", sourceFillerKey.ID)
	}
	sourceKey := configNameMappingKey(t, source, "shared,key", configNameMappingPrivateKey(t, 1), false)
	if sourceKey.ID != 2 {
		t.Fatalf("source shared key ID=%d, want 2", sourceKey.ID)
	}
	danglingScopeKey := configNameMappingKey(t, source, "dangling-scope-key", configNameMappingPrivateKey(t, 3), false)
	configNameMappingKey(t, source, "empty-scope-key", "", false)
	sourceFillerNode := configNameMappingNode(t, source, "source-id-filler-node")
	if sourceFillerNode.ID != 1 {
		t.Fatalf("source filler node ID=%d, want 1", sourceFillerNode.ID)
	}
	sourceNode := configNameMappingNode(t, source, "shared-node")
	commaNode := configNameMappingNode(t, source, "node,comma")
	archivedNode := configNameMappingNode(t, source, "archived-node")
	danglingNode := configNameMappingNode(t, source, "dangling-node")
	forwardNode := configNameMappingNode(t, source, "new-forward-node")
	if forwardNode.ID != 6 {
		t.Fatalf("unexpected source forward node ID=%d, want 6", forwardNode.ID)
	}
	if sourceNode.ID != 2 || commaNode.ID != 3 || archivedNode.ID != 4 || danglingNode.ID != 5 || forwardNode.ID != 6 {
		t.Fatalf("unexpected source node IDs: filler=%d shared=%d comma=%d archived=%d dangling=%d forward=%d", sourceFillerNode.ID, sourceNode.ID, commaNode.ID, archivedNode.ID, danglingNode.ID, forwardNode.ID)
	}
	if err := source.Model(&model.SSHKey{}).Where("id = ?", sourceKey.ID).Updates(map[string]any{
		"allowed_purposes": sshutil.PurposeTerminal,
		"allowed_node_ids": fmt.Sprintf("%d,%d,%d,%d", archivedNode.ID, sourceNode.ID, commaNode.ID, forwardNode.ID),
	}).Error; err != nil {
		t.Fatalf("set source key scope: %v", err)
	}
	if err := source.Model(&model.SSHKey{}).Where("id = ?", danglingScopeKey.ID).Update("allowed_node_ids", fmt.Sprintf("%d,999", sourceNode.ID)).Error; err != nil {
		t.Fatalf("set dangling source key scope: %v", err)
	}
	if err := source.Model(&model.Node{}).Where("id = ?", sourceNode.ID).Update("ssh_key_id", sourceKey.ID).Error; err != nil {
		t.Fatalf("bind source shared node: %v", err)
	}
	if err := source.Model(&model.Node{}).Where("id = ?", commaNode.ID).Update("ssh_key_id", sourceKey.ID).Error; err != nil {
		t.Fatalf("bind source comma node: %v", err)
	}
	if err := source.Model(&model.Node{}).Where("id = ?", archivedNode.ID).Updates(map[string]any{"ssh_key_id": sourceKey.ID, "archived": true}).Error; err != nil {
		t.Fatalf("bind source archived node: %v", err)
	}
	if err := source.Model(&model.Node{}).Where("id = ?", forwardNode.ID).Update("ssh_key_id", sourceKey.ID).Error; err != nil {
		t.Fatalf("bind source forward node: %v", err)
	}
	danglingKeyID := uint(999)
	if engine == "postgres" {
		if err := source.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SET LOCAL session_replication_role = replica").Error; err != nil {
				return err
			}
			return tx.Model(&model.Node{}).Where("id = ?", danglingNode.ID).Update("ssh_key_id", danglingKeyID).Error
		}); err != nil {
			t.Fatalf("set dangling source node key with local FK bypass: %v", err)
		}
	} else if err := source.Model(&model.Node{}).Where("id = ?", danglingNode.ID).Update("ssh_key_id", danglingKeyID).Error; err != nil {
		t.Fatalf("set dangling source node key: %v", err)
	}

	targetFillerKey := configNameMappingKey(t, target, "target-id-filler-key", "", false)
	targetUnrelatedKey := configNameMappingKey(t, target, "target-unrelated-key", configNameMappingPrivateKey(t, 5), false)
	targetSharedKey := configNameMappingKey(t, target, "shared,key", configNameMappingPrivateKey(t, 7), true)
	if targetFillerKey.ID != 1 || targetUnrelatedKey.ID != 2 || targetSharedKey.ID != 3 {
		t.Fatalf("unexpected target key IDs: filler=%d unrelated=%d shared=%d", targetFillerKey.ID, targetUnrelatedKey.ID, targetSharedKey.ID)
	}
	targetFillerNode := configNameMappingNode(t, target, "target-id-filler-node")
	targetUnrelatedNode := configNameMappingNode(t, target, "target-unrelated-node")
	targetSharedNode := configNameMappingNode(t, target, "shared-node")
	targetCommaNode := configNameMappingNode(t, target, "node,comma")
	targetArchivedNode := configNameMappingNode(t, target, "archived-node")
	if targetFillerNode.ID != 1 || targetUnrelatedNode.ID != 2 || targetSharedNode.ID != 3 || targetCommaNode.ID != 4 || targetArchivedNode.ID != 5 {
		t.Fatalf("unexpected target node IDs: filler=%d unrelated=%d shared=%d comma=%d archived=%d", targetFillerNode.ID, targetUnrelatedNode.ID, targetSharedNode.ID, targetCommaNode.ID, targetArchivedNode.ID)
	}
	oldPassword := "FAKE_TARGET_NODE_PASSWORD_FOR_TEST_ONLY"
	oldInlineKey := "FAKE_TARGET_NODE_PRIVATE_KEY_FOR_TEST_ONLY"
	if err := target.Model(&model.Node{}).Where("id = ?", targetSharedNode.ID).Updates(map[string]any{
		"ssh_key_id":  targetUnrelatedKey.ID,
		"password":    oldPassword,
		"private_key": oldInlineKey,
		"archived":    false,
	}).Error; err != nil {
		t.Fatalf("seed target shared node: %v", err)
	}
	if err := target.Model(&model.Node{}).Where("id = ?", targetCommaNode.ID).Update("ssh_key_id", targetUnrelatedKey.ID).Error; err != nil {
		t.Fatalf("seed target comma node: %v", err)
	}
	if err := target.Model(&model.Node{}).Where("id = ?", targetArchivedNode.ID).Updates(map[string]any{"ssh_key_id": targetUnrelatedKey.ID, "archived": true}).Error; err != nil {
		t.Fatalf("seed target archived node: %v", err)
	}
	configNameMappingNode(t, target, "source-id-filler-node")
	configNameMappingNode(t, target, "dangling-node")
	if err := target.Model(&model.SSHKey{}).Where("id = ?", targetSharedKey.ID).Update("allowed_node_ids", fmt.Sprintf("%d", targetUnrelatedNode.ID)).Error; err != nil {
		t.Fatalf("seed target shared scope: %v", err)
	}

	exportResponse := serveConfigExport(t, source, true)
	exportPayload := parseConfigExportPayload(t, exportResponse)
	if got, _ := exportPayload["version"].(string); got != configExportVersion2 {
		t.Fatalf("config export version=%q, want %q", got, configExportVersion2)
	}
	classic, ok := exportPayload["data"].(map[string]any)
	if !ok {
		t.Fatalf("export classic data type=%T", exportPayload["data"])
	}
	sharedExport := configNameMappingRecord(t, classic["ssh_keys"], "shared,key")
	allowedNames, ok := sharedExport["allowed_node_names"].([]any)
	if !ok || len(allowedNames) != 4 || allowedNames[0] != "archived-node" || allowedNames[1] != "shared-node" || allowedNames[2] != "node,comma" || allowedNames[3] != "new-forward-node" {
		t.Fatalf("shared key exported names=%#v, want ordered exact names", sharedExport["allowed_node_names"])
	}
	if sharedExport["allowed_node_ids"] != fmt.Sprintf("%d,%d,%d,%d", archivedNode.ID, sourceNode.ID, commaNode.ID, forwardNode.ID) {
		t.Fatalf("shared key export did not retain legacy numeric scope: %#v", sharedExport)
	}
	sharedNodeExport := configNameMappingRecord(t, classic["nodes"], "shared-node")
	sharedNodeKeyID, sharedNodeKeyIDOK := sharedNodeExport["ssh_key_id"].(float64)
	if !sharedNodeKeyIDOK || uint(sharedNodeKeyID) != sourceKey.ID || sharedNodeExport["ssh_key_name"] != "shared,key" {
		t.Fatalf("shared node export did not retain ID and current name: %#v", sharedNodeExport)
	}
	commaNodeExport := configNameMappingRecord(t, classic["nodes"], "node,comma")
	if commaNodeExport["ssh_key_name"] != "shared,key" {
		t.Fatalf("comma-containing node name/key mapping was not exact: %#v", commaNodeExport)
	}
	forwardNodeExport := configNameMappingRecord(t, classic["nodes"], "new-forward-node")
	if forwardNodeExport["ssh_key_name"] != "shared,key" || forwardNodeExport["ssh_key_id"] == nil {
		t.Fatalf("new forward node export did not retain current key mapping: %#v", forwardNodeExport)
	}
	danglingNodeExport := configNameMappingRecord(t, classic["nodes"], "dangling-node")
	danglingNodeKeyID, danglingNodeKeyIDOK := danglingNodeExport["ssh_key_id"].(float64)
	if danglingNodeExport["ssh_key_name"] != nil || !danglingNodeKeyIDOK || uint(danglingNodeKeyID) != danglingKeyID {
		t.Fatalf("dangling node export must preserve ID and emit null name: %#v", danglingNodeExport)
	}
	danglingScopeExport := configNameMappingRecord(t, classic["ssh_keys"], "dangling-scope-key")
	if danglingScopeExport["allowed_node_names"] != nil {
		t.Fatalf("dangling scope export must emit null, got %#v", danglingScopeExport["allowed_node_names"])
	}
	emptyScopeExport := configNameMappingRecord(t, classic["ssh_keys"], "empty-scope-key")
	emptyNames, ok := emptyScopeExport["allowed_node_names"].([]any)
	if !ok || emptyNames == nil || len(emptyNames) != 0 {
		t.Fatalf("empty scope export must emit [] rather than null: %#v", emptyScopeExport["allowed_node_names"])
	}

	importResponse, result := serveConfigNameMappingImport(t, target, settings.NewService(target), "overwrite", exportPayload)
	if importResponse.Code != http.StatusOK {
		t.Fatalf("exported name mapping import status=%d body=%s", importResponse.Code, importResponse.Body.String())
	}
	if result.Imported != result.Created+result.Updated {
		t.Fatalf("import count invariant failed: %+v", result)
	}
	targetSharedKey = model.SSHKey{}
	if err := target.Where("name = ?", "shared,key").First(&targetSharedKey).Error; err != nil {
		t.Fatalf("load imported shared key: %v", err)
	}
	if targetSharedKey.ID != 3 || !targetSharedKey.Disabled {
		t.Fatalf("name mapping must preserve target ID=%d and disabled=%t", targetSharedKey.ID, targetSharedKey.Disabled)
	}
	if targetSharedKey.Fingerprint != generateFingerprint(targetSharedKey.PrivateKey) {
		t.Fatalf("imported target key fingerprint was not recomputed: %s", configNameMappingKeySummary(targetSharedKey))
	}
	var importedSharedNode, importedCommaNode, importedArchivedNode, importedForwardNode, importedDanglingNode model.Node
	for name, row := range map[string]*model.Node{
		"shared-node":      &importedSharedNode,
		"node,comma":       &importedCommaNode,
		"archived-node":    &importedArchivedNode,
		"dangling-node":    &importedDanglingNode,
		"new-forward-node": &importedForwardNode,
	} {
		if err := target.Where("name = ?", name).First(row).Error; err != nil {
			t.Fatalf("load imported node %q: %v", name, err)
		}
	}
	if !importedArchivedNode.Archived {
		t.Fatalf("archived target node lost archived state during scope mapping: %s", configNameMappingNodeSummary(importedArchivedNode))
	}
	wantScope := fmt.Sprintf("%d,%d,%d,%d", targetArchivedNode.ID, targetSharedNode.ID, targetCommaNode.ID, importedForwardNode.ID)
	if targetSharedKey.AllowedNodeIDs != wantScope {
		t.Fatalf("imported target scope=%q, want %q", targetSharedKey.AllowedNodeIDs, wantScope)
	}
	for name, row := range map[string]model.Node{
		"shared-node":      importedSharedNode,
		"node,comma":       importedCommaNode,
		"archived-node":    importedArchivedNode,
		"new-forward-node": importedForwardNode,
	} {
		if row.SSHKeyID == nil || *row.SSHKeyID != targetSharedKey.ID {
			t.Fatalf("node %q bound to wrong target key: %s target-key=%d", name, configNameMappingNodeSummary(row), targetSharedKey.ID)
		}
	}
	if importedDanglingNode.SSHKeyID != nil {
		t.Fatalf("dangling source node was unexpectedly bound: %s", configNameMappingNodeSummary(importedDanglingNode))
	}
	if importedForwardNode.ID == forwardNode.ID || importedForwardNode.SSHKeyID == nil || *importedForwardNode.SSHKeyID != targetSharedKey.ID {
		t.Fatalf("new source node was not created and resolved through target key: source=%d imported=%s target-key=%d", forwardNode.ID, configNameMappingNodeSummary(importedForwardNode), targetSharedKey.ID)
	}
	if importedForwardNode.BackupDir != nodePkg.SanitizeBackupDir(importedForwardNode.Name) {
		t.Fatalf("new imported node backup_dir=%q, want canonical %q", importedForwardNode.BackupDir, nodePkg.SanitizeBackupDir(importedForwardNode.Name))
	}
	var preservedTargetNode model.Node
	if err := target.Where("id = ?", targetSharedNode.ID).First(&preservedTargetNode).Error; err != nil {
		t.Fatalf("reload target shared node: %v", err)
	}
	if preservedTargetNode.Password != oldPassword || preservedTargetNode.PrivateKey != oldInlineKey {
		t.Fatalf("node overwrite replaced protected inline credentials: password_preserved=%t private_key_preserved=%t", preservedTargetNode.Password == oldPassword, preservedTargetNode.PrivateKey == oldInlineKey)
	}
	var importedDanglingScope, importedEmptyScope model.SSHKey
	if err := target.Where("name = ?", "dangling-scope-key").First(&importedDanglingScope).Error; err != nil {
		t.Fatalf("load dangling scope key: %v", err)
	}
	if !importedDanglingScope.Disabled || importedDanglingScope.AllowedNodeIDs != "" {
		t.Fatalf("unsafe imported scope must fail closed: %s", configNameMappingKeySummary(importedDanglingScope))
	}
	if err := target.Where("name = ?", "empty-scope-key").First(&importedEmptyScope).Error; err != nil {
		t.Fatalf("load empty scope key: %v", err)
	}
	if !importedEmptyScope.Disabled || importedEmptyScope.AllowedNodeIDs != "" {
		t.Fatalf("secretless imported key must be disabled with empty scope: %s", configNameMappingKeySummary(importedEmptyScope))
	}
	if !containsConfigNameMappingWarning(result, configImportWarningUnresolvedNodeScope) || !containsConfigNameMappingWarning(result, configImportWarningUnresolvedSSHKey) {
		t.Fatalf("dangling exported references should be surfaced safely: %+v", result.Warnings)
	}
	if err := target.Model(&model.SSHKey{}).Where("id = ?", targetSharedKey.ID).Update("name", "renamed,shared,key").Error; err != nil {
		t.Fatalf("rename imported target key: %v", err)
	}
	if err := target.Model(&model.Node{}).Where("id = ?", importedForwardNode.ID).Update("name", "renamed-forward-node").Error; err != nil {
		t.Fatalf("rename imported target node: %v", err)
	}
	renamedPayload := parseConfigExportPayload(t, serveConfigExport(t, target, true))
	renamedData, ok := renamedPayload["data"].(map[string]any)
	if !ok {
		t.Fatalf("renamed export data type=%T", renamedPayload["data"])
	}
	renamedNodeExport := configNameMappingRecord(t, renamedData["nodes"], "renamed-forward-node")
	nodeKeyID, nodeKeyIDOK := renamedNodeExport["ssh_key_id"].(float64)
	if !nodeKeyIDOK || uint(nodeKeyID) != targetSharedKey.ID || renamedNodeExport["ssh_key_name"] != "renamed,shared,key" {
		t.Fatalf("renamed node export lost stable ID/name binding: ssh_key_id=%v ssh_key_name=%v", renamedNodeExport["ssh_key_id"], renamedNodeExport["ssh_key_name"])
	}
	renamedKeyExport := configNameMappingRecord(t, renamedData["ssh_keys"], "renamed,shared,key")
	renamedAllowedNames, ok := renamedKeyExport["allowed_node_names"].([]any)
	if !ok {
		t.Fatalf("renamed key export scope type=%T", renamedKeyExport["allowed_node_names"])
	}
	foundRenamedNode := false
	for _, name := range renamedAllowedNames {
		if name == "renamed-forward-node" {
			foundRenamedNode = true
			break
		}
	}
	if !foundRenamedNode || renamedKeyExport["allowed_node_ids"] == nil {
		t.Fatalf("renamed key export lost stable scope: allowed_node_names=%v allowed_node_ids_present=%t", renamedKeyExport["allowed_node_names"], renamedKeyExport["allowed_node_ids"] != nil)
	}
}
func runConfigNameMappingExportSnapshotConsistency(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	var exportDB, concurrent *gorm.DB
	if engine == "sqlite" {
		exportDB, concurrent = openConfigNameMappingSQLiteSnapshotPair(t)
	} else {
		exportDB, concurrent = openConfigHandlerTestDBPairForEngine(t, engine)
	}
	migrateConfigAssetGraphDB(t, exportDB)
	seedConfigV2SharedAssetGraph(t, exportDB)
	var assetNode model.Node
	if err := exportDB.Where("name = ?", "asset-node").First(&assetNode).Error; err != nil {
		t.Fatalf("load snapshot asset node: %v", err)
	}
	if engine == "sqlite" {
		if err := exportDB.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
			t.Fatalf("enable SQLite WAL snapshot mode: %v", err)
		}
	}

	ready := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSnapshot := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseSnapshot()
	settingsResult := make(chan error, 1)
	var snapshotOnce sync.Once
	callback := fmt.Sprintf("test:name-mapping-export-snapshot-%d", handlerTestDBSequence.Add(1))
	if err := exportDB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if configNameMappingTable(tx) != "nodes" {
			return
		}
		snapshotOnce.Do(func() {
			if engine == "postgres" {
				queryer, ok := tx.Statement.ConnPool.(interface {
					QueryRowContext(context.Context, string, ...any) *sql.Row
				})
				if !ok {
					settingsResult <- fmt.Errorf("export transaction connection does not expose database/sql queryer: %T", tx.Statement.ConnPool)
				} else {
					var isolation, readOnly string
					err := queryer.QueryRowContext(context.Background(), "SHOW transaction_isolation").Scan(&isolation)
					if err == nil {
						err = queryer.QueryRowContext(context.Background(), "SHOW transaction_read_only").Scan(&readOnly)
					}
					if err == nil &&
						(strings.ToLower(strings.TrimSpace(isolation)) != "repeatable read" ||
							strings.ToLower(strings.TrimSpace(readOnly)) != "on") {
						err = fmt.Errorf("PostgreSQL export transaction settings isolation=%q read_only=%q", isolation, readOnly)
					}
					settingsResult <- err
				}
			}
			close(ready)
			<-release
		})
	}); err != nil {
		t.Fatalf("register export snapshot callback: %v", err)
	}
	t.Cleanup(func() { _ = exportDB.Callback().Query().Remove(callback) })

	exportDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		handler := NewConfigHandler(exportDB, nil)
		router := gin.New()
		router.GET("/config/export", func(c *gin.Context) {
			c.Set("user_id", uint(9))
			c.Set("username", "admin")
			c.Set("role", "admin")
			handler.Export(c)
		})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/config/export", nil))
		exportDone <- response
	}()
	waitConfigNameMappingChannel(t, ready, "export snapshot after classic read")
	if engine == "postgres" {
		select {
		case err := <-settingsResult:
			if err != nil {
				t.Fatalf("verify PostgreSQL export transaction settings: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out verifying PostgreSQL export transaction settings")
		}
	}

	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- concurrent.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.Node{}).Where("id = ?", assetNode.ID).UpdateColumn("name", "asset-node-after").Error; err != nil {
				return err
			}
			return tx.Model(&model.BackupRepository{}).Where("id = ?", configV2SourceRepositoryID).UpdateColumn("display_name", "shared-restic-after").Error
		})
	}()
	select {
	case err := <-mutationDone:
		if err != nil {
			t.Fatalf("commit concurrent snapshot mutation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out committing concurrent snapshot mutation")
	}
	releaseSnapshot()

	var response *httptest.ResponseRecorder
	select {
	case response = <-exportDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for consistent config export")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("snapshot export status=%d body=%s", response.Code, response.Body.String())
	}
	payload := parseConfigExportPayload(t, response)
	classic, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot export data type=%T", payload["data"])
	}
	nodeExport := configNameMappingRecord(t, classic["nodes"], "asset-node")
	if nodeExport["name"] != "asset-node" {
		t.Fatalf("classic export observed post-snapshot node mutation: %#v", nodeExport)
	}
	repositories, ok := classic["backup_repositories"].([]any)
	if !ok {
		t.Fatalf("snapshot export repositories type=%T", classic["backup_repositories"])
	}
	var repositoryExport map[string]any
	for _, raw := range repositories {
		record, recordOK := raw.(map[string]any)
		if recordOK && record["display_name"] == "shared-restic" {
			repositoryExport = record
			break
		}
	}
	if repositoryExport == nil {
		t.Fatalf("asset export did not retain pre-snapshot repository state: %#v", repositories)
	}
	var currentNode model.Node
	if err := concurrent.Where("id = ?", assetNode.ID).First(&currentNode).Error; err != nil {
		t.Fatalf("load concurrent node after snapshot mutation: %v", err)
	}
	if currentNode.Name != "asset-node-after" {
		t.Fatalf("concurrent node mutation did not commit before export continued: %s", configNameMappingNodeSummary(currentNode))
	}
	var currentRepository model.BackupRepository
	if err := concurrent.Where("id = ?", configV2SourceRepositoryID).First(&currentRepository).Error; err != nil {
		t.Fatalf("load concurrent repository after snapshot mutation: %v", err)
	}
	if currentRepository.DisplayName != "shared-restic-after" {
		t.Fatalf("concurrent repository mutation did not commit before export continued: display_name=%q", currentRepository.DisplayName)
	}
}

func containsConfigNameMappingWarning(result configImportResult, code string) bool {
	for _, warning := range result.Warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
func configNameMappingForeignKeyError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "foreign key")
}
func configNameMappingSQLiteBusyLockedError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "sqlite_busy") ||
		strings.Contains(message, "sqlite_locked")
}

func runConfigNameMappingReferenceSemantics(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	validPrivateKey := configNameMappingPrivateKey(t, 11)
	oldPrivateKey := configNameMappingPrivateKey(t, 41)

	scopeNode := configNameMappingNode(t, db, "scope-node")
	bindingNode := configNameMappingNode(t, db, "binding-node")
	configNameMappingNode(t, db, "bad-node")
	unbindNode := configNameMappingNode(t, db, "unbind-node")
	conflictNode := configNameMappingNode(t, db, "conflict-node")
	scopeKey := configNameMappingKey(t, db, "scope-key", oldPrivateKey, false)
	preserveKey := configNameMappingKey(t, db, "preserve-key", oldPrivateKey, false)
	clearKey := configNameMappingKey(t, db, "clear-key", oldPrivateKey, false)
	nullKey := configNameMappingKey(t, db, "null-key", oldPrivateKey, false)
	invalidTypeKey := configNameMappingKey(t, db, "invalid-type-key", oldPrivateKey, false)
	duplicateMemberKey := configNameMappingKey(t, db, "duplicate-member-key", oldPrivateKey, false)
	conflictKey := configNameMappingKey(t, db, "conflict-key", oldPrivateKey, false)
	rejectedKey := configNameMappingKey(t, db, "rejected-key", oldPrivateKey, false)
	for _, key := range []*model.SSHKey{&scopeKey, &preserveKey, &clearKey, &nullKey, &invalidTypeKey, &duplicateMemberKey, &conflictKey, &rejectedKey} {
		if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Update("allowed_node_ids", fmt.Sprintf("%d", scopeNode.ID)).Error; err != nil {
			t.Fatalf("seed scope for key %q: %v", key.Name, err)
		}
	}
	if err := db.Model(&model.Node{}).Where("id = ?", bindingNode.ID).Update("ssh_key_id", rejectedKey.ID).Error; err != nil {
		t.Fatalf("seed binding node: %v", err)
	}
	if err := db.Model(&model.Node{}).Where("id IN ?", []uint{unbindNode.ID, conflictNode.ID}).Update("ssh_key_id", preserveKey.ID).Error; err != nil {
		t.Fatalf("seed existing node relationships: %v", err)
	}

	body := map[string]any{
		"ssh_keys": []any{
			map[string]any{"name": "scope-key", "username": "updated-scope", "allowed_node_names": []any{"scope-node"}},
			map[string]any{"name": "preserve-key", "username": "updated-preserve"},
			map[string]any{"name": "clear-key", "allowed_node_names": []any{}},
			map[string]any{"name": "null-key", "username": "must-stay", "allowed_node_names": nil},
			map[string]any{"name": "invalid-type-key", "allowed_node_names": "scope-node"},
			map[string]any{"name": "duplicate-member-key", "allowed_node_names": []any{"scope-node", " scope-node "}},
			map[string]any{"name": "conflict-key", "allowed_node_names": []any{}, "allowed_node_ids": fmt.Sprintf("%d", scopeNode.ID)},
			map[string]any{"name": "rejected-key", "allowed_node_names": []any{"SCOPE-NODE"}},
			map[string]any{"name": "bad-new-key", "private_key": "not-a-private-key"},
			map[string]any{"name": "new-unsafe-key", "private_key": validPrivateKey, "allowed_node_names": []any{"missing-scope-node"}},
		},
		"nodes": []any{
			map[string]any{"name": "binding-node", "host": "10.250.1.1", "username": "new-user", "auth_type": "key", "ssh_key_name": "rejected-key"},
			map[string]any{"name": "new-missing-node", "host": "10.250.1.2", "username": "root", "auth_type": "key", "ssh_key_id": 999, "ssh_key_name": "bad-new-key"},
			map[string]any{"name": "bad-node", "host": "10.250.1.4", "username": "new-bad", "auth_type": "key", "ssh_key_name": "missing-target-key"},
			map[string]any{"name": "unbind-node", "host": "10.250.1.5", "username": "root", "auth_type": "key", "ssh_key_name": ""},
			map[string]any{"name": "conflict-node", "host": "10.250.1.6", "username": "root", "auth_type": "key", "ssh_key_name": "", "ssh_key_id": 999},
		},
	}
	response, result := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", body)
	if response.Code != http.StatusOK {
		t.Fatalf("reference semantics import status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 2 || result.Updated != 5 || result.Imported != 7 || result.Rejected != 8 || result.Skipped != 0 || result.DisabledImported != 1 {
		t.Fatalf("unexpected reference semantics counts: %+v", result)
	}
	assertConfigNameMappingWarningCodesUnique(t, result)
	for _, item := range []struct {
		entity string
		index  int
		code   string
	}{
		{configImportEntitySSHKeys, 3, configImportWarningUnresolvedNodeScope},
		{configImportEntitySSHKeys, 4, configNameMappingWarningInvalidReference},
		{configImportEntitySSHKeys, 5, configNameMappingWarningInvalidReference},
		{configImportEntitySSHKeys, 6, configNameMappingWarningReferenceConflict},
		{configImportEntitySSHKeys, 7, configImportWarningUnresolvedNodeScope},
		{configImportEntitySSHKeys, 8, configImportWarningInvalidPrivateKey},
		{configImportEntitySSHKeys, 9, configImportWarningUnresolvedNodeScope},
		{configImportEntityNodes, 1, configImportWarningUnresolvedSSHKey},
		{configImportEntityNodes, 2, configImportWarningUnresolvedSSHKey},
		{configImportEntityNodes, 4, configNameMappingWarningReferenceConflict},
	} {
		requireConfigNameMappingWarning(t, result, item.entity, item.index, item.code)
	}

	var gotScope, gotPreserve, gotClear, gotNull, gotInvalid, gotDuplicate, gotConflict, gotRejected model.SSHKey
	for name, row := range map[string]*model.SSHKey{
		"scope-key":            &gotScope,
		"preserve-key":         &gotPreserve,
		"clear-key":            &gotClear,
		"null-key":             &gotNull,
		"invalid-type-key":     &gotInvalid,
		"duplicate-member-key": &gotDuplicate,
		"conflict-key":         &gotConflict,
		"rejected-key":         &gotRejected,
	} {
		if err := db.Where("name = ?", name).First(row).Error; err != nil {
			t.Fatalf("load key %q: %v", name, err)
		}
	}
	if gotScope.Username != "updated-scope" || gotScope.AllowedNodeIDs != strconv.FormatUint(uint64(scopeNode.ID), 10) {
		t.Fatalf("non-empty name scope did not resolve target ID: %s", configNameMappingKeySummary(gotScope))
	}
	if gotPreserve.Username != "updated-preserve" || gotPreserve.AllowedNodeIDs != strconv.FormatUint(uint64(scopeNode.ID), 10) {
		t.Fatalf("missing scope field did not preserve old scope: %s", configNameMappingKeySummary(gotPreserve))
	}
	if gotClear.AllowedNodeIDs != "" {
		t.Fatalf("explicit empty scope did not clear old ID scope: %s", configNameMappingKeySummary(gotClear))
	}
	for name, got := range map[string]model.SSHKey{
		"null-key": gotNull, "invalid-type-key": gotInvalid, "duplicate-member-key": gotDuplicate, "conflict-key": gotConflict,
	} {
		if got.Username == "must-stay" || got.Username == "updated-scope" {
			t.Fatalf("rejected key %q was mutated: %s", name, configNameMappingKeySummary(got))
		}
		if got.AllowedNodeIDs != strconv.FormatUint(uint64(scopeNode.ID), 10) {
			t.Fatalf("rejected key %q changed old scope: %s", name, configNameMappingKeySummary(got))
		}
	}
	if gotRejected.Username != "deploy" || gotRejected.AllowedNodeIDs != strconv.FormatUint(uint64(scopeNode.ID), 10) {
		t.Fatalf("rejected overwrite key changed old state: %s", configNameMappingKeySummary(gotRejected))
	}
	var unsafeNew model.SSHKey
	if err := db.Where("name = ?", "new-unsafe-key").First(&unsafeNew).Error; err != nil {
		t.Fatalf("load unsafe new key: %v", err)
	}
	if !unsafeNew.Disabled || unsafeNew.AllowedNodeIDs != "" {
		t.Fatalf("unsafe new key was not fail-closed: %s", configNameMappingKeySummary(unsafeNew))
	}
	var badNewCount int64
	if err := db.Model(&model.SSHKey{}).Where("name = ?", "bad-new-key").Count(&badNewCount).Error; err != nil || badNewCount != 0 {
		t.Fatalf("rejected new key remained count=%d err=%v", badNewCount, err)
	}
	var gotBinding, gotBad, gotUnbind, gotConflictNode, gotMissing model.Node
	for name, row := range map[string]*model.Node{
		"binding-node":     &gotBinding,
		"bad-node":         &gotBad,
		"unbind-node":      &gotUnbind,
		"conflict-node":    &gotConflictNode,
		"new-missing-node": &gotMissing,
	} {
		if err := db.Where("name = ?", name).First(row).Error; err != nil {
			t.Fatalf("load node %q: %v", name, err)
		}
	}
	if gotBinding.SSHKeyID == nil || *gotBinding.SSHKeyID != gotRejected.ID {
		t.Fatalf("node should resolve rejected overwrite key's preserved row: %s", configNameMappingNodeSummary(gotBinding))
	}
	if gotBad.Username != "root" || gotBad.Host != "10.250.0.10" {
		t.Fatalf("bad node overwrite was not rejected atomically: %s", configNameMappingNodeSummary(gotBad))
	}
	if gotUnbind.SSHKeyID != nil {
		t.Fatalf("explicit empty node name did not unbind: %s", configNameMappingNodeSummary(gotUnbind))
	}
	if gotConflictNode.SSHKeyID == nil || *gotConflictNode.SSHKeyID != preserveKey.ID {
		t.Fatalf("empty name plus old numeric reference should preserve old node row: %s", configNameMappingNodeSummary(gotConflictNode))
	}
	if gotMissing.SSHKeyID != nil {
		t.Fatalf("new node bound to rejected key unexpectedly: %s", configNameMappingNodeSummary(gotMissing))
	}

	skipBody := map[string]any{
		"ssh_keys": []any{map[string]any{"name": "scope-key", "username": "skip-must-not-write", "allowed_node_names": []any{"missing-scope-node"}}},
		"nodes":    []any{map[string]any{"name": "binding-node", "host": "10.250.9.9", "username": "skip-must-not-write", "auth_type": "key", "ssh_key_name": "missing-target-key"}},
	}
	skipResponse, skipped := serveConfigNameMappingImport(t, db, settings.NewService(db), "skip", skipBody)
	if skipResponse.Code != http.StatusOK || skipped.Skipped != 2 || skipped.Imported != 0 || skipped.Rejected != 0 {
		t.Fatalf("skip result status=%d result=%+v body=%s", skipResponse.Code, skipped, skipResponse.Body.String())
	}
	requireConfigNameMappingWarning(t, skipped, configImportEntitySSHKeys, 0, configImportWarningUnresolvedNodeScope)
	requireConfigNameMappingWarning(t, skipped, configImportEntityNodes, 0, configImportWarningUnresolvedSSHKey)
	var skippedKey model.SSHKey
	var skippedNode model.Node
	if err := db.Where("name = ?", "scope-key").First(&skippedKey).Error; err != nil {
		t.Fatalf("reload skipped key: %v", err)
	}
	if err := db.Where("name = ?", "binding-node").First(&skippedNode).Error; err != nil {
		t.Fatalf("reload skipped node: %v", err)
	}
	if skippedKey.Username != "updated-scope" || skippedNode.Host != "10.250.1.1" || skippedNode.SSHKeyID == nil || *skippedNode.SSHKeyID != rejectedKey.ID {
		t.Fatalf("skip modified existing state: key=%s node=%s", configNameMappingKeySummary(skippedKey), configNameMappingNodeSummary(skippedNode))
	}
	skipResponseAgain, skippedAgain := serveConfigNameMappingImport(t, db, settings.NewService(db), "skip", skipBody)
	if skipResponseAgain.Code != http.StatusOK || skippedAgain.Skipped != skipped.Skipped ||
		skippedAgain.Imported != 0 || skippedAgain.Rejected != 0 {
		t.Fatalf("second skip result status=%d result=%+v body=%s", skipResponseAgain.Code, skippedAgain, skipResponseAgain.Body.String())
	}
	requireConfigNameMappingWarning(t, skippedAgain, configImportEntitySSHKeys, 0, configImportWarningUnresolvedNodeScope)
	requireConfigNameMappingWarning(t, skippedAgain, configImportEntityNodes, 0, configImportWarningUnresolvedSSHKey)
	var skippedKeyAgain model.SSHKey
	var skippedNodeAgain model.Node
	if err := db.Where("name = ?", "scope-key").First(&skippedKeyAgain).Error; err != nil {
		t.Fatalf("reload second-skipped key: %v", err)
	}
	if err := db.Where("name = ?", "binding-node").First(&skippedNodeAgain).Error; err != nil {
		t.Fatalf("reload second-skipped node: %v", err)
	}
	if skippedKeyAgain.Username != skippedKey.Username || skippedKeyAgain.PrivateKey != skippedKey.PrivateKey ||
		skippedNodeAgain.Host != skippedNode.Host || skippedNodeAgain.PrivateKey != skippedNode.PrivateKey ||
		skippedNodeAgain.SSHKeyID == nil || *skippedNodeAgain.SSHKeyID != rejectedKey.ID {
		t.Fatalf("second skip modified existing state: key=%s node=%s", configNameMappingKeySummary(skippedKeyAgain), configNameMappingNodeSummary(skippedNodeAgain))
	}

	invalidNodeResponse, invalidNodes := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", map[string]any{
		"nodes": []any{
			map[string]any{"name": "bad-node", "host": "10.252.9.1", "port": 22, "username": "root", "auth_type": "key", "ssh_key_name": nil},
			map[string]any{"name": "unbind-node", "host": "10.252.9.2", "port": 22, "username": "root", "auth_type": "key", "ssh_key_name": 42},
		},
	})
	if invalidNodeResponse.Code != http.StatusOK || invalidNodes.Imported != 0 || invalidNodes.Rejected != 2 {
		t.Fatalf("invalid node reference result status=%d result=%+v body=%s", invalidNodeResponse.Code, invalidNodes, invalidNodeResponse.Body.String())
	}
	requireConfigNameMappingWarning(t, invalidNodes, configImportEntityNodes, 0, configImportWarningUnresolvedSSHKey)
	requireConfigNameMappingWarning(t, invalidNodes, configImportEntityNodes, 1, configNameMappingWarningInvalidReference)
	var afterBad, afterUnbind model.Node
	if err := db.Where("name = ?", "bad-node").First(&afterBad).Error; err != nil {
		t.Fatalf("reload null-rejected node: %v", err)
	}
	if err := db.Where("name = ?", "unbind-node").First(&afterUnbind).Error; err != nil {
		t.Fatalf("reload invalid-rejected node: %v", err)
	}
	if afterBad.Host != gotBad.Host || afterBad.Username != gotBad.Username || afterBad.SSHKeyID != gotBad.SSHKeyID ||
		afterUnbind.Host != gotUnbind.Host || afterUnbind.Username != gotUnbind.Username || afterUnbind.SSHKeyID != gotUnbind.SSHKeyID {
		t.Fatalf("invalid node references changed existing rows: bad-before=%s bad-after=%s unbind-before=%s unbind-after=%s", configNameMappingNodeSummary(gotBad), configNameMappingNodeSummary(afterBad), configNameMappingNodeSummary(gotUnbind), configNameMappingNodeSummary(afterUnbind))
	}
}

func runConfigNameMappingSkipResolution(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	key := configNameMappingKey(t, db, "skip-existing-key", configNameMappingPrivateKey(t, 70), false)
	node := configNameMappingNode(t, db, "skip-existing-node")
	existingReferenceNode := configNameMappingNode(t, db, "skip-existing-reference-node")
	if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Update("ssh_key_id", key.ID).Error; err != nil {
		t.Fatalf("bind skipped node: %v", err)
	}
	body := map[string]any{
		"ssh_keys": []any{
			map[string]any{"name": "skip-created-key", "private_key": configNameMappingPrivateKey(t, 71)},
		},
		"nodes": []any{
			map[string]any{"name": "skip-existing-node", "host": "10.251.8.1", "username": "root", "auth_type": "key", "ssh_key_name": "skip-created-key"},
			map[string]any{"name": existingReferenceNode.Name, "ssh_key_name": key.Name},
			map[string]any{"name": "skip-new-node", "host": "10.251.8.2", "username": "root", "auth_type": "key", "ssh_key_name": "skip-created-key"},
		},
	}
	response, result := serveConfigNameMappingImport(t, db, settings.NewService(db), "skip", body)
	if response.Code != http.StatusOK || result.Created != 2 || result.Updated != 0 || result.Skipped != 2 || result.Rejected != 0 || result.Imported != 2 {
		t.Fatalf("skip name resolution result status=%d result=%+v body=%s", response.Code, result, response.Body.String())
	}
	for _, index := range []int{0, 1, 2} {
		for _, warning := range result.Warnings {
			if warning.Entity == configImportEntityNodes && warning.Index == index && warning.Code == configImportWarningUnresolvedSSHKey {
				t.Fatalf("skip emitted false unresolved key warning: %+v", result.Warnings)
			}
		}
	}
	var gotNode model.Node
	if err := db.Where("name = ?", "skip-existing-node").First(&gotNode).Error; err != nil {
		t.Fatalf("reload skipped node: %v", err)
	}
	if gotNode.Host != node.Host || gotNode.SSHKeyID == nil || *gotNode.SSHKeyID != key.ID {
		t.Fatalf("skip modified existing node: before=%s after=%s", configNameMappingNodeSummary(node), configNameMappingNodeSummary(gotNode))
	}
	var gotExistingReferenceNode model.Node
	if err := db.First(&gotExistingReferenceNode, existingReferenceNode.ID).Error; err != nil {
		t.Fatalf("reload existing-reference skipped node: %v", err)
	}
	if gotExistingReferenceNode.Host != existingReferenceNode.Host || gotExistingReferenceNode.SSHKeyID != nil {
		t.Fatalf("skip backfilled existing-reference node: %+v", gotExistingReferenceNode)
	}
	var createdKey model.SSHKey
	if err := db.Where("name = ?", "skip-created-key").First(&createdKey).Error; err != nil {
		t.Fatalf("reload created key: %v", err)
	}
	var createdNode model.Node
	if err := db.Where("name = ?", "skip-new-node").First(&createdNode).Error; err != nil {
		t.Fatalf("reload created node: %v", err)
	}
	if createdNode.SSHKeyID == nil || *createdNode.SSHKeyID != createdKey.ID {
		t.Fatalf("new node was not linked to same-import key: node=%s key=%s", configNameMappingNodeSummary(createdNode), configNameMappingKeySummary(createdKey))
	}
}

func runConfigNameMappingAuthTypeCompatibility(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	for _, version := range []string{"", configExportVersion1, configExportVersion2} {
		t.Run(func() string {
			if version == "" {
				return "legacy"
			}
			return version
		}(), func(t *testing.T) {
			db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateConfigAssetGraphDB(t, db)
			existing := configNameMappingNode(t, db, "auth-compat-existing")
			bodyData := map[string]any{
				"nodes": []any{
					map[string]any{"name": existing.Name, "host": "10.254.0.1", "username": "root", "auth_type": " \t"},
					map[string]any{"name": "auth-compat-new", "host": "10.254.0.2", "username": "root", "auth_type": "   "},
				},
			}
			var body any = bodyData
			if version != "" {
				body = map[string]any{"version": version, "document_id": strings.Repeat("c", 32), "data": bodyData}
			}
			response, result := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", body)
			if response.Code != http.StatusOK || result.Imported != 2 {
				t.Fatalf("version=%q auth compatibility status=%d result=%+v body=%s", version, response.Code, result, response.Body.String())
			}
			var gotExisting, gotNew model.Node
			if err := db.Where("name = ?", existing.Name).First(&gotExisting).Error; err != nil {
				t.Fatalf("version=%q reload existing node: %v", version, err)
			}
			if err := db.Where("name = ?", "auth-compat-new").First(&gotNew).Error; err != nil {
				t.Fatalf("version=%q reload new node: %v", version, err)
			}
			if gotExisting.AuthType != "" || gotNew.AuthType != "key" {
				t.Fatalf("version=%q auth_type compatibility changed: existing=%q new=%q", version, gotExisting.AuthType, gotNew.AuthType)
			}
		})
	}
}

func runConfigNameMappingScopeSafetyCombinations(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	allowed := configNameMappingNode(t, db, "scope-allowed")
	duplicateTarget := configNameMappingNode(t, db, "duplicate-target")
	bound := configNameMappingNode(t, db, "scope-bound")
	body := map[string]any{
		"ssh_keys": []any{
			map[string]any{"name": "scope-valid-legacy-invalid", "private_key": configNameMappingPrivateKey(t, 72), "allowed_node_ids": []any{123}, "allowed_node_names": []any{"scope-allowed"}},
			map[string]any{"name": "scope-mixed", "private_key": configNameMappingPrivateKey(t, 73), "allowed_node_names": []any{"scope-allowed", "missing-scope-node"}},
			map[string]any{"name": "scope-disabled", "private_key": configNameMappingPrivateKey(t, 74), "disabled": true, "allowed_node_names": []any{"scope-allowed"}},
			map[string]any{"name": "scope-ambiguous", "private_key": configNameMappingPrivateKey(t, 75), "allowed_node_names": []any{"duplicate-target"}},
			map[string]any{"name": "duplicate-key", "private_key": configNameMappingPrivateKey(t, 76)},
			map[string]any{"name": " duplicate-key ", "private_key": configNameMappingPrivateKey(t, 77)},
		},
		"nodes": []any{
			map[string]any{"name": "duplicate-target", "host": "10.255.0.1", "username": "root", "auth_type": "key"},
			map[string]any{"name": "duplicate-target", "host": "10.255.0.2", "username": "root", "auth_type": "key"},
			map[string]any{"name": "scope-bound", "host": "10.255.0.3", "username": "root", "auth_type": "key", "ssh_key_name": "duplicate-key"},
		},
	}
	response, result := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", body)
	if response.Code != http.StatusOK {
		t.Fatalf("scope safety import status=%d body=%s", response.Code, response.Body.String())
	}
	for index := range 2 {
		requireConfigNameMappingWarning(t, result, configImportEntityNodes, index, configNameMappingWarningDuplicateName)
	}
	requireConfigNameMappingWarning(t, result, configImportEntitySSHKeys, 4, configNameMappingWarningDuplicateName)
	requireConfigNameMappingWarning(t, result, configImportEntitySSHKeys, 5, configNameMappingWarningDuplicateName)
	requireConfigNameMappingWarning(t, result, configImportEntitySSHKeys, 0, configImportWarningInvalidScope)
	requireConfigNameMappingWarning(t, result, configImportEntitySSHKeys, 1, configImportWarningUnresolvedNodeScope)
	requireConfigNameMappingWarning(t, result, configImportEntitySSHKeys, 3, configImportWarningUnresolvedNodeScope)
	var valid, mixed, disabled, ambiguous model.SSHKey
	scopeKeys := []struct {
		key  *model.SSHKey
		name string
	}{
		{key: &valid, name: "scope-valid-legacy-invalid"},
		{key: &mixed, name: "scope-mixed"},
		{key: &disabled, name: "scope-disabled"},
		{key: &ambiguous, name: "scope-ambiguous"},
	}
	for _, item := range scopeKeys {
		if err := db.Where("name = ?", item.name).First(item.key).Error; err != nil {
			t.Fatalf("reload scope key %q: %v", item.name, err)
		}
	}
	wantAllowed := strconv.FormatUint(uint64(allowed.ID), 10)
	if valid.AllowedNodeIDs != wantAllowed || !valid.Disabled {
		t.Fatalf("malformed legacy scope was not fail-closed with verified IDs: %s", configNameMappingKeySummary(valid))
	}
	if mixed.AllowedNodeIDs != "" || !mixed.Disabled {
		t.Fatalf("mixed scope retained a partial or unrestricted scope: %s", configNameMappingKeySummary(mixed))
	}
	if disabled.AllowedNodeIDs != wantAllowed || !disabled.Disabled {
		t.Fatalf("disabled resolved key changed enabled state or scope: %s", configNameMappingKeySummary(disabled))
	}
	if ambiguous.AllowedNodeIDs != "" || !ambiguous.Disabled {
		t.Fatalf("duplicate name scope fell back to unique target: %s", configNameMappingKeySummary(ambiguous))
	}
	var gotDuplicateTarget, gotBound model.Node
	if err := db.Where("name = ?", duplicateTarget.Name).First(&gotDuplicateTarget).Error; err != nil {
		t.Fatalf("reload duplicate target: %v", err)
	}
	if err := db.Where("name = ?", bound.Name).First(&gotBound).Error; err != nil {
		t.Fatalf("reload bound node: %v", err)
	}
	if gotDuplicateTarget.Host != duplicateTarget.Host || gotBound.SSHKeyID != nil {
		t.Fatalf("unsafe node references changed existing rows: target=%s bound=%s", configNameMappingNodeSummary(gotDuplicateTarget), configNameMappingNodeSummary(gotBound))
	}

	unsafe := configNameMappingKey(t, db, "unsafe-overwrite", configNameMappingPrivateKey(t, 78), false)
	if err := db.Model(&model.SSHKey{}).Where("id = ?", unsafe.ID).Update("allowed_node_ids", wantAllowed).Error; err != nil {
		t.Fatalf("seed unsafe overwrite scope: %v", err)
	}
	var before model.SSHKey
	if err := db.Where("id = ?", unsafe.ID).First(&before).Error; err != nil {
		t.Fatalf("load unsafe overwrite baseline: %v", err)
	}
	unsafeBody := map[string]any{"ssh_keys": []any{
		map[string]any{"name": unsafe.Name, "private_key": configNameMappingPrivateKey(t, 79), "allowed_node_names": []any{"scope-allowed", "missing-scope-node"}},
	}}
	unsafeResponse, unsafeResult := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", unsafeBody)
	if unsafeResponse.Code != http.StatusOK {
		t.Fatalf("unsafe overwrite status=%d body=%s", unsafeResponse.Code, unsafeResponse.Body.String())
	}
	var after model.SSHKey
	if err := db.Where("id = ?", unsafe.ID).First(&after).Error; err != nil {
		t.Fatalf("load unsafe overwrite result: %v", err)
	}
	if before.Username != after.Username || before.PrivateKey != after.PrivateKey || before.AllowedNodeIDs != after.AllowedNodeIDs || before.Disabled != after.Disabled ||
		!containsConfigNameMappingWarning(unsafeResult, configImportWarningUnresolvedNodeScope) {
		t.Fatalf("unsafe overwrite was not fully rejected: before=%s after=%s result=%+v", configNameMappingKeySummary(before), configNameMappingKeySummary(after), unsafeResult)
	}
}

func runConfigNameMappingCorruptedUnrelatedSecret(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	unrelated := configNameMappingNode(t, db, "corrupt-unrelated-node")
	if err := db.Model(&model.Node{}).Where("id = ?", unrelated.ID).UpdateColumn("private_key", "enc:v2:corrupted-unrelated-secret").Error; err != nil {
		t.Fatalf("corrupt unrelated node secret: %v", err)
	}
	body := map[string]any{"ssh_keys": []any{
		map[string]any{"name": "key-with-corrupt-unrelated-node", "private_key": configNameMappingPrivateKey(t, 80)},
	}}
	response, result := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", body)
	if response.Code != http.StatusOK || result.Created != 1 || result.Rejected != 0 {
		t.Fatalf("corrupted unrelated secret blocked import status=%d result=%+v body=%s", response.Code, result, response.Body.String())
	}
	var imported model.SSHKey
	if err := db.Where("name = ?", "key-with-corrupt-unrelated-node").First(&imported).Error; err != nil {
		t.Fatalf("reload import after unrelated corruption: %v", err)
	}
	if imported.Disabled {
		t.Fatalf("valid imported key was disabled after unrelated node corruption: %s", configNameMappingKeySummary(imported))
	}
}

func runConfigNameMappingDuplicateNames(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	body := map[string]any{
		"ssh_keys": []any{
			map[string]any{"name": "duplicate-name", "private_key": configNameMappingPrivateKey(t, 61)},
			map[string]any{"name": " duplicate-name ", "private_key": configNameMappingPrivateKey(t, 62)},
		},
		"nodes": []any{
			map[string]any{"name": "duplicate-node", "host": "10.251.0.1", "username": "root", "auth_type": "key"},
			map[string]any{"name": "duplicate-node", "host": "10.251.0.2", "username": "root", "auth_type": "key"},
		},
	}
	response, result := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", body)
	if response.Code != http.StatusOK || result.Created != 0 || result.Updated != 0 || result.Rejected != 4 || result.Imported != 0 {
		t.Fatalf("duplicate name result status=%d result=%+v body=%s", response.Code, result, response.Body.String())
	}
	for index := range 2 {
		requireConfigNameMappingWarning(t, result, configImportEntitySSHKeys, index, configNameMappingWarningDuplicateName)
		requireConfigNameMappingWarning(t, result, configImportEntityNodes, index, configNameMappingWarningDuplicateName)
	}
	assertConfigNameMappingWarningCodesUnique(t, result)
	for table, name := range map[string]string{"ssh_keys": "duplicate-name", "nodes": "duplicate-node"} {
		var count int64
		if err := db.Table(table).Where("name = ?", name).Count(&count).Error; err != nil {
			t.Fatalf("count duplicate %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("duplicate %s was partially imported count=%d", table, count)
		}
	}
}

func runConfigNameMappingVersionCompatibility(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	for _, version := range []string{"", configExportVersion1, configExportVersion2} {
		t.Run(func() string {
			if version == "" {
				return "legacy"
			}
			return version
		}(), func(t *testing.T) {
			db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateConfigAssetGraphDB(t, db)
			node := configNameMappingNode(t, db, "version-node")
			bodyData := map[string]any{
				"ssh_keys": []any{map[string]any{
					"name":               "version-key",
					"private_key":        configNameMappingPrivateKey(t, 71),
					"allowed_node_ids":   "999",
					"allowed_node_names": []any{"version-node"},
				}},
				"nodes": []any{map[string]any{
					"name": "version-node", "host": "10.252.0.1", "username": "root", "auth_type": "key",
					"ssh_key_id": 999, "ssh_key_name": "version-key",
				}},
			}
			body := any(bodyData)
			if version != "" {
				body = map[string]any{"version": version, "document_id": strings.Repeat("a", 32), "data": bodyData}
			}
			response, result := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", body)
			if response.Code != http.StatusOK {
				t.Fatalf("version=%q import status=%d body=%s", version, response.Code, response.Body.String())
			}
			if result.Imported != 2 {
				t.Fatalf("version=%q result=%+v", version, result)
			}
			var importedKey model.SSHKey
			if err := db.Where("name = ?", "version-key").First(&importedKey).Error; err != nil {
				t.Fatalf("version=%q load key: %v", version, err)
			}
			if importedKey.AllowedNodeIDs != strconv.FormatUint(uint64(node.ID), 10) || importedKey.Disabled {
				t.Fatalf("version=%q name scope did not take precedence over source numeric IDs: %s", version, configNameMappingKeySummary(importedKey))
			}
			var importedNode model.Node
			if err := db.Where("name = ?", "version-node").First(&importedNode).Error; err != nil {
				t.Fatalf("version=%q load node: %v", version, err)
			}
			if importedNode.SSHKeyID == nil || *importedNode.SSHKeyID != importedKey.ID {
				t.Fatalf("version=%q node relation=%v want key=%d", version, importedNode.SSHKeyID, importedKey.ID)
			}
		})
	}

	for _, version := range []string{"", configExportVersion1, configExportVersion2} {
		t.Run(func() string {
			if version == "" {
				return "legacy_fields"
			}
			return version + "_legacy_fields"
		}(), func(t *testing.T) {
			db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateConfigAssetGraphDB(t, db)
			legacyBodyData := map[string]any{
				"ssh_keys": []any{map[string]any{"name": "legacy-unsafe-key", "private_key": "", "allowed_node_ids": "999"}},
				"nodes":    []any{map[string]any{"name": "legacy-unbound-node", "host": "10.252.0.2", "username": "root", "auth_type": "key", "ssh_key_id": 999}},
			}
			var legacyBody any = legacyBodyData
			if version != "" {
				legacyBody = map[string]any{"version": version, "document_id": strings.Repeat("b", 32), "data": legacyBodyData}
			}
			response, result := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", legacyBody)
			if response.Code != http.StatusOK {
				t.Fatalf("version=%q legacy fields import status=%d body=%s", version, response.Code, response.Body.String())
			}
			var legacyKey model.SSHKey
			var legacyNode model.Node
			if err := db.Where("name = ?", "legacy-unsafe-key").First(&legacyKey).Error; err != nil {
				t.Fatalf("version=%q load legacy key: %v", version, err)
			}
			if err := db.Where("name = ?", "legacy-unbound-node").First(&legacyNode).Error; err != nil {
				t.Fatalf("version=%q load legacy node: %v", version, err)
			}
			if !legacyKey.Disabled || legacyKey.AllowedNodeIDs != "" || legacyNode.SSHKeyID != nil {
				t.Fatalf("version=%q legacy numeric IDs were used as cross-database mappings: key=%s node=%s", version, configNameMappingKeySummary(legacyKey), configNameMappingNodeSummary(legacyNode))
			}
			if !containsConfigNameMappingWarning(result, configImportWarningUnresolvedNodeScope) || !containsConfigNameMappingWarning(result, configImportWarningUnresolvedSSHKey) {
				t.Fatalf("version=%q legacy numeric IDs should remain unresolved: %+v", version, result.Warnings)
			}
		})
	}

}
func runConfigNameMappingScopeAuth(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	allowed := configNameMappingNode(t, db, "auth-allowed-node")
	denied := configNameMappingNode(t, db, "auth-denied-node")
	body := map[string]any{
		"ssh_keys": []any{map[string]any{
			"name":               "auth-imported-key",
			"username":           "deploy",
			"private_key":        p1ImportPrivateKey(t),
			"allowed_purposes":   sshutil.PurposeTerminal,
			"allowed_node_names": []any{"auth-allowed-node"},
		}},
		"nodes": []any{
			map[string]any{"name": "auth-allowed-node", "host": "10.253.0.1", "username": "root", "auth_type": "key", "ssh_key_name": "auth-imported-key"},
			map[string]any{"name": "auth-denied-node", "host": "10.253.0.2", "username": "root", "auth_type": "key", "ssh_key_name": "auth-imported-key"},
		},
	}
	response, _ := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", body)
	if response.Code != http.StatusOK {
		t.Fatalf("scope/auth import status=%d body=%s", response.Code, response.Body.String())
	}
	var key model.SSHKey
	if err := db.Where("name = ?", "auth-imported-key").First(&key).Error; err != nil {
		t.Fatalf("load auth key: %v", err)
	}
	if err := db.Where("name = ?", allowed.Name).First(&allowed).Error; err != nil {
		t.Fatalf("load allowed node: %v", err)
	}
	if err := db.Where("name = ?", denied.Name).First(&denied).Error; err != nil {
		t.Fatalf("load denied node: %v", err)
	}
	if err := sshutil.ValidateSSHKeyScope(key, allowed, sshutil.PurposeTerminal); err != nil {
		t.Fatalf("imported scope should allow exact target node: %v; key=%s node=%s", err, configNameMappingKeySummary(key), configNameMappingNodeSummary(allowed))
	}
	if err := sshutil.ValidateSSHKeyScope(key, denied, sshutil.PurposeTerminal); err == nil {
		t.Fatal("imported scope unexpectedly allowed a different target node")
	}
	if err := sshutil.ValidateSSHKeyScope(key, allowed, sshutil.PurposeTaskCommand); err == nil {
		t.Fatal("imported purpose scope unexpectedly allowed an unlisted purpose")
	}
	if _, _, _, err := sshutil.BuildSSHAuthWithKeyForPurpose(allowed, db, sshutil.PurposeTerminal); err != nil {
		t.Fatalf("shared auth provider rejected imported allowed scope: %v", err)
	}
	if _, _, _, err := sshutil.BuildSSHAuthWithKeyForPurpose(denied, db, sshutil.PurposeTerminal); err == nil {
		t.Fatal("shared auth provider accepted imported disallowed node")
	}
	if err := db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Update("disabled", true).Error; err != nil {
		t.Fatalf("disable imported key: %v", err)
	}
	if _, _, _, err := sshutil.BuildSSHAuthWithKeyForPurpose(allowed, db, sshutil.PurposeTerminal); err == nil {
		t.Fatal("shared auth provider accepted disabled imported key")
	}
}

func runConfigNameMappingFaults(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	t.Run("new_node_create", func(t *testing.T) {
		db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
		migrateP1ConfigImport(t, db)
		injected := errors.New("FAKE_NAME_MAPPING_NODE_CREATE_FAILURE_FOR_TEST_ONLY")
		callback := fmt.Sprintf("test:name-mapping-node-create-%d", handlerTestDBSequence.Add(1))
		if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "nodes" {
				_ = tx.AddError(injected)
			}
		}); err != nil {
			t.Fatalf("register node create failure: %v", err)
		}
		t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
		response, _ := serveConfigNameMappingImport(t, db, settings.NewService(db), "skip", map[string]any{
			"ssh_keys": []any{map[string]any{"name": "node-create-key", "private_key": configNameMappingPrivateKey(t, 91)}},
			"nodes":    []any{map[string]any{"name": "node-create-failure", "host": "10.254.0.1", "username": "root", "auth_type": "key", "ssh_key_name": "node-create-key"}},
		})
		if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), injected.Error()) {
			t.Fatalf("node create failure status=%d body=%s", response.Code, response.Body.String())
		}
		assertConfigNameMappingRowsAbsent(t, db, []configNameMappingAbsentRow{
			{table: "ssh_keys", column: "name", value: "node-create-key"},
			{table: "nodes", column: "name", value: "node-create-failure"},
		})
	})

	t.Run("key_save", func(t *testing.T) {
		db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
		migrateP1ConfigImport(t, db)
		old := configNameMappingKey(t, db, "save-failure-key", configNameMappingPrivateKey(t, 101), false)
		injected := errors.New("FAKE_NAME_MAPPING_KEY_SAVE_FAILURE_FOR_TEST_ONLY")
		callback := fmt.Sprintf("test:name-mapping-key-save-%d", handlerTestDBSequence.Add(1))
		if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "ssh_keys" {
				_ = tx.AddError(injected)
			}
		}); err != nil {
			t.Fatalf("register key save failure: %v", err)
		}
		t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
		response, _ := serveConfigNameMappingImport(t, db, settings.NewService(db), "overwrite", map[string]any{
			"ssh_keys": []any{map[string]any{"name": old.Name, "username": "must-not-save"}},
			"nodes":    []any{map[string]any{"name": "key-save-earlier-node", "host": "10.254.0.2", "username": "root", "auth_type": "key"}},
		})
		if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), injected.Error()) {
			t.Fatalf("key save failure status=%d body=%s", response.Code, response.Body.String())
		}
		var got model.SSHKey
		if err := db.Where("id = ?", old.ID).First(&got).Error; err != nil {
			t.Fatalf("load key after save rollback: %v", err)
		}
		if got.Username != old.Username || got.PrivateKey != old.PrivateKey {
			t.Fatalf("key save failure changed old row: %s", configNameMappingKeySummary(got))
		}
		assertConfigNameMappingRowsAbsent(t, db, []configNameMappingAbsentRow{
			{table: "nodes", column: "name", value: "key-save-earlier-node"},
		})
	})

	t.Run("node_backfill", func(t *testing.T) {
		db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
		migrateP1ConfigImport(t, db)
		injected := errors.New("FAKE_NAME_MAPPING_NODE_BACKFILL_FAILURE_FOR_TEST_ONLY")
		callback := fmt.Sprintf("test:name-mapping-node-backfill-%d", handlerTestDBSequence.Add(1))
		if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "nodes" {
				_ = tx.AddError(injected)
			}
		}); err != nil {
			t.Fatalf("register node backfill failure: %v", err)
		}
		t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
		response, _ := serveConfigNameMappingImport(t, db, settings.NewService(db), "skip", map[string]any{
			"ssh_keys": []any{map[string]any{"name": "backfill-key", "private_key": configNameMappingPrivateKey(t, 111)}},
			"nodes":    []any{map[string]any{"name": "backfill-node", "host": "10.254.0.3", "username": "root", "auth_type": "key", "ssh_key_name": "backfill-key"}},
		})
		if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), injected.Error()) {
			t.Fatalf("node backfill failure status=%d body=%s", response.Code, response.Body.String())
		}
		assertConfigNameMappingRowsAbsent(t, db, []configNameMappingAbsentRow{
			{table: "ssh_keys", column: "name", value: "backfill-key"},
			{table: "nodes", column: "name", value: "backfill-node"},
		})
	})
}

type configNameMappingAbsentRow struct {
	table  string
	column string
	value  string
}

func assertConfigNameMappingRowsAbsent(t *testing.T, db *gorm.DB, rows []configNameMappingAbsentRow) {
	t.Helper()
	for _, row := range rows {
		var count int64
		if err := db.Table(row.table).Where(row.column+" = ?", row.value).Count(&count).Error; err != nil {
			t.Fatalf("count rolled-back %s: %v", row.table, err)
		}
		if count != 0 {
			t.Fatalf("rolled-back %s row remains count=%d", row.table, count)
		}
	}
}

func runConfigNameMappingRuntimeJournal(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	target, concurrent := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateConfigAssetGraphDB(t, target)
	oldPrivateKey := configNameMappingPrivateKey(t, 121)
	key := configNameMappingKey(t, target, "journal-key", oldPrivateKey, false)
	node := configNameMappingNode(t, target, "journal-node")
	if err := target.Model(&model.Node{}).Where("id = ?", node.ID).Update("ssh_key_id", key.ID).Error; err != nil {
		t.Fatalf("bind journal node: %v", err)
	}
	if err := target.Create(&model.SystemSetting{Key: "backup_assets.content_preview_ttl", Value: "2m"}).Error; err != nil {
		t.Fatalf("seed journal setting: %v", err)
	}
	runtime := &configImportRollbackRuntime{failure: errors.New("FAKE_NAME_MAPPING_RUNTIME_FAILURE_FOR_TEST_ONLY")}
	runtime.afterPersist = func() error {
		return concurrent.Model(&model.Node{}).Where("id = ?", node.ID).Update("host", "10.255.0.99").Error
	}
	handler := NewConfigHandler(target, settings.NewService(target)).WithBackupAssetTransitioner(runtime)
	body := map[string]any{
		"ssh_keys":        []any{map[string]any{"name": "journal-key", "username": "new-journal-user"}},
		"nodes":           []any{map[string]any{"name": "journal-node", "host": "10.255.0.2", "username": "new-journal-user", "auth_type": "key", "ssh_key_name": "journal-key"}},
		"system_settings": []any{map[string]any{"key": "backup_assets.content_preview_ttl", "value": "3m"}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal journal payload: %v", err)
	}
	response, _, err := configNameMappingImportRequest(handler, "overwrite", payload)
	if err != nil {
		t.Fatalf("decode journal response: %v", err)
	}
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), runtime.failure.Error()) {
		t.Fatalf("runtime journal failure status=%d body=%s", response.Code, response.Body.String())
	}
	var gotNode model.Node
	if err := target.Where("id = ?", node.ID).First(&gotNode).Error; err != nil {
		t.Fatalf("load journal node: %v", err)
	}
	if gotNode.Host != "10.255.0.99" || gotNode.Username != "new-journal-user" || gotNode.SSHKeyID == nil || *gotNode.SSHKeyID != key.ID {
		t.Fatalf("rollback conflict should preserve user node change while retaining imported fields: host=%q username=%q ssh_key_id=%v", gotNode.Host, gotNode.Username, gotNode.SSHKeyID)
	}
	var gotKey model.SSHKey
	if err := target.Where("id = ?", key.ID).First(&gotKey).Error; err != nil {
		t.Fatalf("load journal key: %v", err)
	}
	if gotKey.Username != "new-journal-user" || gotKey.PrivateKey != key.PrivateKey {
		t.Fatalf("rollback conflict should retain imported key while preserving its prior secret: id=%d username=%q private_preserved=%t", gotKey.ID, gotKey.Username, gotKey.PrivateKey == key.PrivateKey)
	}
	var setting model.SystemSetting
	if err := target.Where("key = ?", "backup_assets.content_preview_ttl").First(&setting).Error; err != nil {
		t.Fatalf("load restored journal setting: %v", err)
	}
	if setting.Value != "3m" {
		t.Fatalf("rollback conflict should retain imported setting value: %q", setting.Value)
	}
}
func runConfigNameMappingRuntimeFullRestore(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	target, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateConfigAssetGraphDB(t, target)
	oldPrivateKey := configNameMappingPrivateKey(t, 122)
	key := configNameMappingKey(t, target, "runtime-full-key", oldPrivateKey, false)
	node := configNameMappingNode(t, target, "runtime-full-node")
	if err := target.Create(&model.SystemSetting{Key: "backup_assets.content_preview_ttl", Value: "2m"}).Error; err != nil {
		t.Fatalf("seed full-restore setting: %v", err)
	}
	runtime := &configImportRollbackRuntime{failure: errors.New("FAKE_NAME_MAPPING_RUNTIME_FULL_RESTORE_FAILURE_FOR_TEST_ONLY")}
	handler := NewConfigHandler(target, settings.NewService(target)).WithBackupAssetTransitioner(runtime)
	body := map[string]any{
		"ssh_keys":        []any{map[string]any{"name": key.Name, "username": "runtime-imported-user"}},
		"nodes":           []any{map[string]any{"name": node.Name, "host": "10.255.1.2", "username": "runtime-imported-user", "auth_type": "key", "ssh_key_name": key.Name}},
		"system_settings": []any{map[string]any{"key": "backup_assets.content_preview_ttl", "value": "3m"}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal full-restore payload: %v", err)
	}
	response, _, err := configNameMappingImportRequest(handler, "overwrite", payload)
	if err != nil {
		t.Fatalf("decode full-restore response: %v", err)
	}
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), runtime.failure.Error()) {
		t.Fatalf("full-restore runtime failure status=%d body=%s", response.Code, response.Body.String())
	}
	var gotKey model.SSHKey
	if err := target.Where("id = ?", key.ID).First(&gotKey).Error; err != nil {
		t.Fatalf("load fully restored key: %v", err)
	}
	if gotKey.Username != key.Username || gotKey.PrivateKey != key.PrivateKey {
		t.Fatalf("full runtime rollback did not restore key fields: id=%d username=%q private_preserved=%t", gotKey.ID, gotKey.Username, gotKey.PrivateKey == key.PrivateKey)
	}
	var gotNode model.Node
	if err := target.Where("id = ?", node.ID).First(&gotNode).Error; err != nil {
		t.Fatalf("load fully restored node: %v", err)
	}
	if gotNode.Host != node.Host || gotNode.Username != node.Username || gotNode.SSHKeyID != nil {
		t.Fatalf("full runtime rollback did not restore node/backfill: id=%d host=%q username=%q ssh_key_id=%v", gotNode.ID, gotNode.Host, gotNode.Username, gotNode.SSHKeyID)
	}
	var setting model.SystemSetting
	if err := target.Where("key = ?", "backup_assets.content_preview_ttl").First(&setting).Error; err != nil {
		t.Fatalf("load fully restored setting: %v", err)
	}
	if setting.Value != "2m" {
		t.Fatalf("full runtime rollback did not restore setting value: %q", setting.Value)
	}
}

func runConfigNameMappingRejectedNameRuntimeRace(t *testing.T, engine string) {
	if engine != "postgres" {
		return
	}
	setConfigHandlerTestEncryption(t)
	target, concurrent := openConfigHandlerTestDBPairForEngine(t, "postgres")
	migrateConfigAssetGraphDB(t, target)
	captureReached := make(chan struct{})
	releaseCapture := make(chan struct{})
	var captureOnce sync.Once
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseCapture) })
	}
	callback := fmt.Sprintf("test:name-mapping-rejected-name-seal-%d", handlerTestDBSequence.Add(1))
	if err := target.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		querySQL := strings.ToLower(tx.Statement.SQL.String())
		if configNameMappingTable(tx) != "ssh_keys" ||
			!strings.Contains(querySQL, "name") ||
			!strings.Contains(querySQL, " in ") {
			return
		}
		captureOnce.Do(func() {
			close(captureReached)
			<-releaseCapture
		})
	}); err != nil {
		t.Fatalf("register rejected-name race barrier: %v", err)
	}
	t.Cleanup(func() {
		_ = target.Callback().Query().Remove(callback)
		release()
	})
	runtime := &configImportRollbackRuntime{failure: errors.New("FAKE_NAME_MAPPING_REJECTED_NAME_RUNTIME_FAILURE_FOR_TEST_ONLY")}
	handler := NewConfigHandler(target, settings.NewService(target)).WithBackupAssetTransitioner(runtime)
	body := map[string]any{
		"ssh_keys": []any{
			map[string]any{"name": "rejected-absent-key", "private_key": "not-a-private-key"},
		},
		"system_settings": []any{
			map[string]any{"key": "backup_assets.content_preview_ttl", "value": "3m"},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal rejected-name race payload: %v", err)
	}
	importDone := make(chan configNameMappingImportOutcome, 1)
	go func() {
		response, result, requestErr := configNameMappingImportRequest(handler, "overwrite", payload)
		importDone <- configNameMappingImportOutcome{response: response, result: result, err: requestErr}
	}()
	waitConfigNameMappingChannel(t, captureReached, "PostgreSQL rejected-name rollback capture")
	concurrentKey := configNameMappingKey(t, concurrent, "rejected-absent-key", configNameMappingPrivateKey(t, 81), false)
	release()
	outcome := <-importDone
	if outcome.err != nil {
		t.Fatalf("decode rejected-name race response: %v", outcome.err)
	}
	if outcome.response.Code != http.StatusInternalServerError {
		t.Fatalf("rejected-name runtime failure status=%d result=%+v body=%s", outcome.response.Code, outcome.result, outcome.response.Body.String())
	}
	var survivors []model.SSHKey
	if err := target.Where("name = ?", "rejected-absent-key").Find(&survivors).Error; err != nil {
		t.Fatalf("reload concurrent rejected-name key: %v", err)
	}
	if len(survivors) != 1 || survivors[0].ID != concurrentKey.ID {
		t.Fatalf("rollback deleted concurrent row for rejected input name: survivors=%v concurrent_id=%d", survivors, concurrentKey.ID)
	}
}

func runConfigNameMappingConcurrentMissingName(t *testing.T, engine string) {
	if engine == "sqlite" {
		runConfigNameMappingConcurrentMissingNameSQLite(t)
		return
	}
	runConfigNameMappingConcurrentMissingNamePostgres(t)
}

type configNameMappingImportOutcome struct {
	response *httptest.ResponseRecorder
	result   configImportResult
	err      error
}

func configNameMappingConcurrentMissingNamePayload(t *testing.T) []byte {
	t.Helper()
	body := map[string]any{
		"ssh_keys": []any{map[string]any{
			"name":        "racing-key",
			"username":    "deploy",
			"private_key": configNameMappingPrivateKey(t, 151),
		}},
		"nodes": []any{map[string]any{
			"name": "racing-node", "host": "10.25.8.1", "port": 22,
			"username": "root", "auth_type": "key", "ssh_key_name": "racing-key",
		}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal concurrent missing-name race body: %v", err)
	}
	return payload
}

func assertConfigNameMappingConcurrentMissingNamePair(t *testing.T, db *gorm.DB, engine string) {
	t.Helper()
	var keyCount, nodeCount int64
	if err := db.Model(&model.SSHKey{}).Where("name = ?", "racing-key").Count(&keyCount).Error; err != nil {
		t.Fatalf("count %s racing keys: %v", engine, err)
	}
	if err := db.Model(&model.Node{}).Where("name = ?", "racing-node").Count(&nodeCount).Error; err != nil {
		t.Fatalf("count %s racing nodes: %v", engine, err)
	}
	if keyCount != 1 || nodeCount != 1 {
		t.Fatalf("%s concurrent missing-name race left key_count=%d node_count=%d", engine, keyCount, nodeCount)
	}
	var key model.SSHKey
	if err := db.Where("name = ?", "racing-key").First(&key).Error; err != nil {
		t.Fatalf("load %s racing key: %v", engine, err)
	}
	var node model.Node
	if err := db.Where("name = ?", "racing-node").First(&node).Error; err != nil {
		t.Fatalf("load %s racing node: %v", engine, err)
	}
	if node.SSHKeyID == nil || *node.SSHKeyID != key.ID {
		t.Fatalf("%s concurrent missing-name race bound node to wrong key: key=%s node=%s", engine, configNameMappingKeySummary(key), configNameMappingNodeSummary(node))
	}
}

func runConfigNameMappingConcurrentMissingNameSQLite(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	first, second := openConfigNameMappingSQLitePair(t, false)
	migrateP1ConfigImport(t, first)
	secondSQL, err := second.DB()
	if err != nil {
		t.Fatalf("open SQLite missing-name competitor SQL DB: %v", err)
	}
	secondSQL.SetMaxOpenConns(1)
	secondSQL.SetMaxIdleConns(1)
	if err := second.Exec("PRAGMA busy_timeout = 0").Error; err != nil {
		t.Fatalf("set SQLite missing-name competitor busy timeout: %v", err)
	}

	ready := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() {
		releaseOnce.Do(func() { close(release) })
	}
	var importWG sync.WaitGroup
	defer func() {
		releaseAll()
		importWG.Wait()
	}()

	callback := fmt.Sprintf("test:name-mapping-missing-name-sqlite-%d", handlerTestDBSequence.Add(1))
	var reachedOnce sync.Once
	if err := first.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if configNameMappingTable(tx) != "nodes" {
			return
		}
		reachedOnce.Do(func() {
			close(ready)
			<-release
		})
	}); err != nil {
		t.Fatalf("register SQLite missing-name barrier: %v", err)
	}
	t.Cleanup(func() { _ = first.Callback().Query().Remove(callback) })

	var reservationErrorOnce sync.Once
	reservationError := make(chan error, 1)
	rawCallback := fmt.Sprintf("test:name-mapping-missing-name-sqlite-reservation-%d", handlerTestDBSequence.Add(1))
	if err := second.Callback().Raw().After("gorm:raw").Register(rawCallback, func(tx *gorm.DB) {
		querySQL := strings.ToLower(tx.Statement.SQL.String())
		if !strings.Contains(querySQL, "update nodes set id = id where 1 = 0") {
			return
		}
		reservationErrorOnce.Do(func() { reservationError <- tx.Error })
	}); err != nil {
		t.Fatalf("register SQLite missing-name reservation observer: %v", err)
	}
	t.Cleanup(func() { _ = second.Callback().Raw().Remove(rawCallback) })

	payload := configNameMappingConcurrentMissingNamePayload(t)
	firstDone := make(chan configNameMappingImportOutcome, 1)
	secondDone := make(chan configNameMappingImportOutcome, 1)
	importWG.Add(1)
	go func() {
		defer importWG.Done()
		response, result, requestErr := configNameMappingImportRequest(NewConfigHandler(first, settings.NewService(first)), "overwrite", payload)
		firstDone <- configNameMappingImportOutcome{response: response, result: result, err: requestErr}
	}()
	waitConfigNameMappingChannel(t, ready, "SQLite first missing-name inventory read")

	importWG.Add(1)
	go func() {
		defer importWG.Done()
		response, result, requestErr := configNameMappingImportRequest(NewConfigHandler(second, settings.NewService(second)), "overwrite", payload)
		secondDone <- configNameMappingImportOutcome{response: response, result: result, err: requestErr}
	}()

	var busyErr error
	select {
	case busyErr = <-reservationError:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SQLite second importer BUSY/LOCKED reservation evidence")
	}
	if !configNameMappingSQLiteBusyLockedError(busyErr) {
		t.Fatalf("SQLite second importer reservation returned unexpected error: %v", busyErr)
	}
	var secondOutcome configNameMappingImportOutcome
	select {
	case secondOutcome = <-secondDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for SQLite second missing-name importer failure")
	}
	if secondOutcome.err != nil {
		t.Fatalf("SQLite second missing-name response decode: %v", secondOutcome.err)
	}
	if secondOutcome.response.Code == http.StatusOK {
		t.Fatalf("SQLite second missing-name importer unexpectedly succeeded: result=%+v", secondOutcome.result)
	}

	releaseAll()
	var firstOutcome configNameMappingImportOutcome
	select {
	case firstOutcome = <-firstDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for SQLite first missing-name importer after release")
	}
	if firstOutcome.err != nil {
		t.Fatalf("SQLite first missing-name response decode: %v", firstOutcome.err)
	}
	if firstOutcome.response.Code != http.StatusOK || firstOutcome.result.Imported != 2 || firstOutcome.result.Created != 2 {
		t.Fatalf("SQLite first missing-name importer did not uniquely commit: status=%d result=%+v body=%s", firstOutcome.response.Code, firstOutcome.result, firstOutcome.response.Body.String())
	}
	assertConfigNameMappingConcurrentMissingNamePair(t, first, "sqlite")
}

func runConfigNameMappingConcurrentMissingNamePostgres(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	first, second := openConfigHandlerTestDBPairForEngine(t, "postgres")
	migrateP1ConfigImport(t, first)

	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() {
		releaseOnce.Do(func() { close(release) })
	}
	var importWG sync.WaitGroup
	defer func() {
		releaseAll()
		importWG.Wait()
	}()

	for index, db := range []*gorm.DB{first, second} {
		db := db
		var reachedOnce sync.Once
		callback := fmt.Sprintf("test:name-mapping-missing-name-postgres-%d", index)
		if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
			if configNameMappingTable(tx) != "nodes" {
				return
			}
			reachedOnce.Do(func() {
				ready <- struct{}{}
				<-release
			})
		}); err != nil {
			t.Fatalf("register PostgreSQL missing-name barrier: %v", err)
		}
		t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	}

	payload := configNameMappingConcurrentMissingNamePayload(t)
	outcomes := make(chan configNameMappingImportOutcome, 2)
	for _, db := range []*gorm.DB{first, second} {
		db := db
		importWG.Add(1)
		go func() {
			defer importWG.Done()
			response, result, requestErr := configNameMappingImportRequest(NewConfigHandler(db, settings.NewService(db)), "overwrite", payload)
			outcomes <- configNameMappingImportOutcome{response: response, result: result, err: requestErr}
		}()
	}
	for range 2 {
		waitConfigNameMappingChannel(t, ready, "PostgreSQL concurrent missing-name inventory reads")
	}
	releaseAll()

	successes := 0
	rejections := 0
	for range 2 {
		select {
		case got := <-outcomes:
			if got.err != nil {
				t.Fatalf("PostgreSQL concurrent missing-name response decode: %v", got.err)
			}
			if got.response.Code == http.StatusOK && got.result.Imported == 2 && got.result.Created == 2 {
				successes++
				continue
			}
			if got.response.Code != http.StatusOK || (got.result.Imported == 0 && got.result.Rejected > 0) {
				rejections++
				continue
			}
			t.Fatalf("PostgreSQL concurrent missing-name result was neither a unique success nor an explicit rejection: status=%d result=%+v body=%s", got.response.Code, got.result, got.response.Body.String())
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for PostgreSQL concurrent missing-name import")
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatalf("PostgreSQL concurrent missing-name imports successes=%d rejections=%d, want one of each", successes, rejections)
	}
	assertConfigNameMappingConcurrentMissingNamePair(t, first, "postgres")
}

func runConfigNameMappingContention(t *testing.T, engine string) {
	if engine == "sqlite" {
		for _, immediate := range []bool{false, true} {
			t.Run(func() string {
				if immediate {
					return "sqlite_immediate"
				}
				return "sqlite_non_immediate"
			}(), func(t *testing.T) {
				runConfigNameMappingSQLiteContention(t, immediate)
			})
		}
		return
	}
	runConfigNameMappingPostgresContention(t)
}

func configNameMappingTable(tx *gorm.DB) string {
	if tx == nil || tx.Statement == nil {
		return ""
	}
	if tx.Statement.Schema != nil && tx.Statement.Schema.Table != "" {
		return tx.Statement.Schema.Table
	}
	return tx.Statement.Table
}

func configNameMappingBackendPID(queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	var pid int
	if err := queryer.QueryRowContext(context.Background(), "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func waitConfigNameMappingChannel(t *testing.T, channel <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func runConfigNameMappingSQLiteContention(t *testing.T, immediate bool) {
	setConfigHandlerTestEncryption(t)
	target, concurrent := openConfigNameMappingSQLitePair(t, immediate)
	migrateP1ConfigImport(t, target)
	key := configNameMappingKey(t, target, "locked-key", configNameMappingPrivateKey(t, 131), false)
	contenderKey := configNameMappingKey(t, target, "contender-key", configNameMappingPrivateKey(t, 132), false)
	node := configNameMappingNode(t, target, "locked-node")
	if err := target.Model(&model.Node{}).Where("id = ?", node.ID).Update("ssh_key_id", key.ID).Error; err != nil {
		t.Fatalf("bind locked node: %v", err)
	}
	ready := make(chan struct{})
	release := make(chan struct{})
	var lockOnce sync.Once
	callback := fmt.Sprintf("test:name-mapping-lock-%d", handlerTestDBSequence.Add(1))
	if err := target.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if configNameMappingTable(tx) != "nodes" {
			return
		}
		lockOnce.Do(func() {
			close(ready)
			<-release
		})
	}); err != nil {
		t.Fatalf("register SQLite lock callback: %v", err)
	}
	t.Cleanup(func() { _ = target.Callback().Query().Remove(callback) })

	body := map[string]any{
		"ssh_keys": []any{map[string]any{"name": "locked-key", "username": "imported-locked-user"}},
		"nodes":    []any{map[string]any{"name": "locked-node", "host": "10.256.0.2", "username": "root", "auth_type": "key", "ssh_key_name": "locked-key"}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal SQLite contention body: %v", err)
	}
	importDone := make(chan struct {
		response *httptest.ResponseRecorder
		err      error
	}, 1)
	go func() {
		response, _, requestErr := configNameMappingImportRequest(NewConfigHandler(target, settings.NewService(target)), "overwrite", payload)
		importDone <- struct {
			response *httptest.ResponseRecorder
			err      error
		}{response: response, err: requestErr}
	}()
	waitConfigNameMappingChannel(t, ready, "SQLite importer row-lock boundary")

	updateStarted := make(chan struct{})
	deleteStarted := make(chan struct{})
	updateDone := make(chan error, 1)
	deleteDone := make(chan error, 1)
	updateCallback := fmt.Sprintf("test:name-mapping-contender-update-%d", handlerTestDBSequence.Add(1))
	if err := concurrent.Callback().Update().Before("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if configNameMappingTable(tx) == "nodes" {
			select {
			case <-updateStarted:
			default:
				close(updateStarted)
			}
		}
	}); err != nil {
		t.Fatalf("register SQLite competing update callback: %v", err)
	}
	deleteCallback := fmt.Sprintf("test:name-mapping-contender-delete-%d", handlerTestDBSequence.Add(1))
	if err := concurrent.Callback().Delete().Before("gorm:delete").Register(deleteCallback, func(tx *gorm.DB) {
		if configNameMappingTable(tx) == "ssh_keys" {
			select {
			case <-deleteStarted:
			default:
				close(deleteStarted)
			}
		}
	}); err != nil {
		t.Fatalf("register SQLite competing delete callback: %v", err)
	}
	t.Cleanup(func() {
		_ = concurrent.Callback().Update().Remove(updateCallback)
		_ = concurrent.Callback().Delete().Remove(deleteCallback)
	})
	go func() {
		updateDone <- concurrent.Model(&model.Node{}).Where("id = ?", node.ID).UpdateColumn("name", "locked-node-concurrent").Error
	}()
	go func() {
		deleteDone <- concurrent.Delete(&model.SSHKey{}, contenderKey.ID).Error
	}()
	waitConfigNameMappingChannel(t, updateStarted, "SQLite competing node rename")
	waitConfigNameMappingChannel(t, deleteStarted, "SQLite competing key delete")
	var updateBusyBeforeRelease, deleteBusyBeforeRelease bool
	if immediate {
		select {
		case err := <-updateDone:
			if !configNameMappingSQLiteBusyLockedError(err) {
				t.Fatalf("SQLite immediate node rename returned unexpected contention result: %v", err)
			}
			updateBusyBeforeRelease = true
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for SQLite immediate node BUSY/LOCKED evidence")
		}
		select {
		case err := <-deleteDone:
			if !configNameMappingSQLiteBusyLockedError(err) {
				t.Fatalf("SQLite immediate unbound key delete returned unexpected contention result: %v", err)
			}
			deleteBusyBeforeRelease = true
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for SQLite immediate key DELETE BUSY/LOCKED evidence")
		}
	} else {
		select {
		case err := <-updateDone:
			if !configNameMappingSQLiteBusyLockedError(err) {
				t.Fatalf("SQLite competing node rename completed before importer release with unexpected result: %v", err)
			}
			updateBusyBeforeRelease = true
		default:
		}
		err := <-deleteDone
		if !configNameMappingSQLiteBusyLockedError(err) {
			t.Fatalf("SQLite competing unbound key delete completed before importer release with unexpected result: %v", err)
		}
		deleteBusyBeforeRelease = true
	}
	if immediate {
		if !deleteBusyBeforeRelease || !updateBusyBeforeRelease {
			t.Fatalf("SQLite immediate contention did not produce deterministic BUSY/LOCKED evidence: update_busy=%t delete_busy=%t", updateBusyBeforeRelease, deleteBusyBeforeRelease)
		}
		var observedNode model.Node
		if err := concurrent.Where("id = ?", node.ID).First(&observedNode).Error; err != nil {
			t.Fatalf("read SQLite node during immediate contention: %v", err)
		}
		if observedNode.Name != "locked-node" {
			t.Fatalf("SQLite immediate contention changed node before importer release: name=%q", observedNode.Name)
		}
		var keyCount int64
		if err := concurrent.Model(&model.SSHKey{}).Where("id = ?", contenderKey.ID).Count(&keyCount).Error; err != nil {
			t.Fatalf("read SQLite unbound key during immediate contention: %v", err)
		}
		if keyCount != 1 {
			t.Fatalf("SQLite immediate contention changed unbound key before importer release: count=%d", keyCount)
		}
	}
	close(release)
	select {
	case outcome := <-importDone:
		if outcome.err != nil {
			t.Fatalf("SQLite contention import request error: %v", outcome.err)
		}
		if outcome.response.Code != http.StatusOK {
			t.Fatalf("SQLite contention import status=%d body=%s", outcome.response.Code, outcome.response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for SQLite import after releasing lock")
	}
	if !updateBusyBeforeRelease {
		select {
		case err := <-updateDone:
			if err != nil && !configNameMappingSQLiteBusyLockedError(err) {
				t.Fatalf("SQLite competing node rename after release: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for SQLite competing node rename")
		}
	}
	if !deleteBusyBeforeRelease {
		select {
		case err := <-deleteDone:
			if err != nil && !configNameMappingSQLiteBusyLockedError(err) {
				t.Fatalf("SQLite competing unbound key delete after release: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for SQLite competing unbound key delete")
		}
	}
}

func runConfigNameMappingPostgresContention(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	target, concurrent := openConfigHandlerTestDBPairForEngine(t, "postgres")
	migrateP1ConfigImport(t, target)
	key := configNameMappingKey(t, target, "locked-key", configNameMappingPrivateKey(t, 141), false)
	node := configNameMappingNode(t, target, "locked-node")
	if err := target.Model(&model.Node{}).Where("id = ?", node.ID).Update("ssh_key_id", key.ID).Error; err != nil {
		t.Fatalf("bind PostgreSQL locked node: %v", err)
	}
	ready := make(chan int, 1)
	pidError := make(chan error, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() {
		releaseOnce.Do(func() { close(release) })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	var contentionWG sync.WaitGroup
	defer func() {
		cancel()
		releaseAll()
		contentionWG.Wait()
	}()
	var lockOnce sync.Once
	callback := fmt.Sprintf("test:name-mapping-pg-lock-%d", handlerTestDBSequence.Add(1))
	if err := target.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if configNameMappingTable(tx) != "ssh_keys" || !strings.Contains(strings.ToUpper(tx.Statement.SQL.String()), "FOR UPDATE") {
			return
		}
		lockOnce.Do(func() {
			rowQueryer, ok := tx.Statement.ConnPool.(interface {
				QueryRowContext(context.Context, string, ...any) *sql.Row
			})
			if !ok {
				pidError <- fmt.Errorf("import lock connection does not expose database/sql queryer: %T", tx.Statement.ConnPool)
				close(ready)
				return
			}
			pid, err := configNameMappingBackendPID(rowQueryer)
			if err != nil {
				pidError <- err
				close(ready)
				return
			}
			ready <- pid
			<-release
		})
	}); err != nil {
		t.Fatalf("register PostgreSQL lock callback: %v", err)
	}
	t.Cleanup(func() { _ = target.Callback().Query().Remove(callback) })

	body := map[string]any{
		"ssh_keys": []any{map[string]any{"name": "locked-key", "username": "imported-locked-user"}},
		"nodes":    []any{map[string]any{"name": "locked-node", "host": "10.257.0.2", "username": "root", "auth_type": "key", "ssh_key_name": "locked-key"}},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal PostgreSQL contention body: %v", err)
	}
	importDone := make(chan struct {
		response *httptest.ResponseRecorder
		err      error
	}, 1)
	contentionWG.Add(1)
	go func() {
		defer contentionWG.Done()
		response, _, requestErr := configNameMappingImportRequest(NewConfigHandler(target, settings.NewService(target)), "overwrite", payload)
		importDone <- struct {
			response *httptest.ResponseRecorder
			err      error
		}{response: response, err: requestErr}
	}()
	var importerPID int
	select {
	case err := <-pidError:
		t.Fatalf("PostgreSQL importer lock PID: %v", err)
	case pid := <-ready:
		importerPID = pid
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for PostgreSQL importer key-lock boundary")
	}
	if importerPID == 0 {
		t.Fatal("PostgreSQL importer PID was zero")
	}

	sqlDB, err := concurrent.DB()
	if err != nil {
		t.Fatalf("open PostgreSQL contention SQL DB: %v", err)
	}
	// The cleanup registered above handles cancellation on every fatal path.
	nodeConn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire PostgreSQL node contender connection: %v", err)
	}
	defer func() { _ = nodeConn.Close() }()
	keyConn, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire PostgreSQL key contender connection: %v", err)
	}
	defer func() { _ = keyConn.Close() }()
	defer func() {
		cancel()
		releaseAll()
		contentionWG.Wait()
	}()
	nodePID, err := configNameMappingBackendPID(nodeConn)
	if err != nil {
		t.Fatalf("read PostgreSQL node contender PID: %v", err)
	}
	keyPID, err := configNameMappingBackendPID(keyConn)
	if err != nil {
		t.Fatalf("read PostgreSQL key contender PID: %v", err)
	}
	nodeDone := make(chan error, 1)
	keyDone := make(chan error, 1)
	contentionWG.Add(2)
	go func() {
		defer contentionWG.Done()
		_, execErr := nodeConn.ExecContext(ctx, "UPDATE nodes SET name = $1 WHERE id = $2", "locked-node-concurrent", node.ID)
		nodeDone <- execErr
	}()
	go func() {
		defer contentionWG.Done()
		_, execErr := keyConn.ExecContext(ctx, "DELETE FROM ssh_keys WHERE id = $1", key.ID)
		keyDone <- execErr
	}()
	waitConfigNameMappingPostgresBlock(t, sqlDB, nodePID, importerPID, "node rename")
	waitConfigNameMappingPostgresBlock(t, sqlDB, keyPID, importerPID, "key delete")
	select {
	case err := <-nodeDone:
		t.Fatalf("PostgreSQL node rename completed before importer release: %v", err)
	default:
	}
	select {
	case err := <-keyDone:
		t.Fatalf("PostgreSQL key contender completed before importer release: %v", err)
	default:
	}
	releaseAll()
	select {
	case outcome := <-importDone:
		if outcome.err != nil {
			t.Fatalf("PostgreSQL contention import request error: %v", outcome.err)
		}
		if outcome.response.Code != http.StatusOK {
			t.Fatalf("PostgreSQL contention import status=%d body=%s", outcome.response.Code, outcome.response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for PostgreSQL import after releasing lock")
	}
	select {
	case err := <-nodeDone:
		if err != nil {
			t.Fatalf("PostgreSQL node rename after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for PostgreSQL node rename")
	}
	select {
	case err := <-keyDone:
		if err != nil && !configNameMappingForeignKeyError(err) {
			t.Fatalf("PostgreSQL key delete after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for PostgreSQL key contender")
	}
}

func waitConfigNameMappingPostgresBlock(t *testing.T, db *sql.DB, waiterPID, holderPID int, label string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	observer, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire PostgreSQL lock observer for %s: %v", label, err)
	}
	defer func() { _ = observer.Close() }()
	for {
		var blocked bool
		err := observer.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE pid = $1
				  AND wait_event_type = 'Lock'
				  AND $2 = ANY(pg_blocking_pids(pid))
			)`, waiterPID, holderPID).Scan(&blocked)
		if err != nil {
			t.Fatalf("inspect PostgreSQL lock contention for %s: %v", label, err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for PostgreSQL %s to report holder PID %d", label, holderPID)
		default:
			runtime.Gosched()
		}
	}
}
