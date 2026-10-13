package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
)

// configImportPreviewSQLProbe records the SQL emitted while constructing a
// decision snapshot. It intentionally classifies SQL rather than inspecting
// the request body, so regressions that accidentally hydrate or write secrets
// remain observable at the database boundary.
type configImportPreviewSQLProbe struct {
	statements []string
}

func (p *configImportPreviewSQLProbe) LogMode(gormlogger.LogLevel) gormlogger.Interface {
	return p
}

func (*configImportPreviewSQLProbe) Info(context.Context, string, ...any)  {}
func (*configImportPreviewSQLProbe) Warn(context.Context, string, ...any)  {}
func (*configImportPreviewSQLProbe) Error(context.Context, string, ...any) {}

func (p *configImportPreviewSQLProbe) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	if p == nil {
		return
	}
	sql, _ := fc()
	if strings.TrimSpace(sql) != "" {
		p.statements = append(p.statements, sql)
	}
}

func (p *configImportPreviewSQLProbe) reset() {
	if p != nil {
		p.statements = p.statements[:0]
	}
}

func (p *configImportPreviewSQLProbe) writeStatements() []string {
	if p == nil {
		return nil
	}
	writes := make([]string, 0)
	for _, statement := range p.statements {
		upper := strings.ToUpper(strings.TrimSpace(statement))
		for _, prefix := range []string{
			"INSERT ", "UPDATE ", "DELETE ", "REPLACE ",
			"CREATE ", "ALTER ", "DROP ", "TRUNCATE ",
		} {
			if strings.HasPrefix(upper, prefix) {
				writes = append(writes, statement)
				break
			}
		}
	}
	return writes
}

var configImportPreviewPresenceProjection = regexp.MustCompile(
	`(?is)case\s+when\s+[^,]+?\s+then\s+(?:true|1)\s+else\s+(?:false|0)\s+end\s+as\s+(?:password_present|private_key_present)`,
)
var configImportPreviewSecretColumn = regexp.MustCompile(`(?i)\b(?:password|private_key)\b`)

func (p *configImportPreviewSQLProbe) meaningfulSecretReads() []string {
	if p == nil {
		return nil
	}
	reads := make([]string, 0)
	for _, statement := range p.statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if !strings.HasPrefix(normalized, "select ") {
			continue
		}
		from := strings.Index(normalized, " from ")
		if from < 0 {
			continue
		}
		fromClause := normalized[from:]
		if !strings.Contains(fromClause, "from `nodes`") &&
			!strings.Contains(fromClause, `from "nodes"`) &&
			!strings.Contains(fromClause, "from nodes ") &&
			!strings.Contains(fromClause, "from `ssh_keys`") &&
			!strings.Contains(fromClause, `from "ssh_keys"`) &&
			!strings.Contains(fromClause, "from ssh_keys ") {
			continue
		}
		projection := normalized[len("select "):from]
		projection = configImportPreviewPresenceProjection.ReplaceAllString(projection, "")
		if strings.Contains(projection, "*") && !strings.Contains(projection, "count(") {
			reads = append(reads, statement)
			continue
		}
		if configImportPreviewSecretColumn.MatchString(projection) {
			reads = append(reads, statement)
		}
	}
	return reads
}

func previewConfigImportDecision(
	t *testing.T,
	db *gorm.DB,
	conflict string,
	body any,
	probe *configImportPreviewSQLProbe,
) configImportDecision {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal preview import payload: %v", err)
	}
	handler := NewConfigHandler(db, nil)
	input, err := prepareConfigImportInput(context.Background(), handler, payload, conflict)
	if err != nil {
		t.Fatalf("prepare preview import input: %v", err)
	}
	decisionDB := db
	if probe != nil {
		decisionDB = db.Session(&gorm.Session{Logger: probe})
	}
	snapshot, err := loadConfigImportSnapshot(context.Background(), decisionDB, input)
	if err != nil {
		t.Fatalf("load preview import snapshot: %v", err)
	}
	decision, err := evaluateConfigImport(context.Background(), input, snapshot)
	if err != nil {
		t.Fatalf("evaluate preview import decision: %v", err)
	}
	return decision
}

func assertConfigImportPreviewReadOnly(t *testing.T, probe *configImportPreviewSQLProbe) {
	t.Helper()
	if writes := probe.writeStatements(); len(writes) != 0 {
		t.Fatalf("preview decision emitted SQL writes: %v", writes)
	}
	if reads := probe.meaningfulSecretReads(); len(reads) != 0 {
		t.Fatalf("preview decision read persisted secret columns: %v", reads)
	}
}

func configImportPreviewNodeAt(decision configImportDecision, index int) *configImportNodeCandidate {
	for _, candidate := range decision.nodeCandidates {
		if candidate != nil && candidate.index == index {
			return candidate
		}
	}
	return nil
}

func configImportPreviewKeyAt(decision configImportDecision, index int) *configImportSSHKeyCandidate {
	for _, candidate := range decision.keyCandidates {
		if candidate != nil && candidate.index == index {
			return candidate
		}
	}
	return nil
}

func configImportPreviewTaskAt(decision configImportDecision, index int) *configImportTaskCandidate {
	for _, candidate := range decision.taskCandidates {
		if candidate != nil && candidate.taskIndex == index {
			return candidate
		}
	}
	return nil
}

func assertConfigImportPreviewAction(t *testing.T, decision configImportDecision, entity string, index int, want configImportAction) {
	t.Helper()
	var got configImportAction
	switch entity {
	case configImportEntityNodes:
		candidate := configImportPreviewNodeAt(decision, index)
		if candidate == nil {
			t.Fatalf("preview decision omitted %s[%d], want action %q", entity, index, want)
		}
		got = candidate.action
	case configImportEntitySSHKeys:
		candidate := configImportPreviewKeyAt(decision, index)
		if candidate == nil {
			t.Fatalf("preview decision omitted %s[%d], want action %q", entity, index, want)
		}
		got = candidate.action
	case configImportEntityTasks:
		candidate := configImportPreviewTaskAt(decision, index)
		if candidate == nil {
			t.Fatalf("preview decision omitted %s[%d], want action %q", entity, index, want)
		}
		got = candidate.action
	default:
		t.Fatalf("unsupported preview action entity %q", entity)
	}
	if got != want {
		t.Fatalf("preview action for %s[%d]=%q, want %q", entity, index, got, want)
	}
}

func assertConfigImportPreviewPending(t *testing.T, decision configImportDecision, entity string, index int, code string) {
	t.Helper()
	for _, check := range decision.pendingChecks {
		if check.Entity == entity && check.Index == index && check.Code == code {
			return
		}
	}
	t.Fatalf("preview decision missing pending check %s[%d]=%q: %+v", entity, index, code, decision.pendingChecks)
}

func createCorruptedPreviewSSHKey(t *testing.T, db *gorm.DB, name string) model.SSHKey {
	t.Helper()
	privateKey := p1ImportPrivateKey(t)
	prepared, keyType, err := sshutil.ValidateAndPreparePrivateKey(privateKey, sshutil.SSHKeyTypeAuto)
	if err != nil {
		t.Fatalf("prepare corrupted-key fixture: %v", err)
	}
	key := model.SSHKey{
		Name: name, Username: "preview-user", KeyType: keyType,
		PrivateKey: prepared, Fingerprint: generateFingerprint(prepared),
		AllowedPurposes: sshutil.PurposeTerminal,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create corrupted-key fixture: %v", err)
	}
	// Bypass the model hook only while corrupting the stored ciphertext. A
	// normal model read would decrypt this value and hide the regression.
	if err := db.Exec("UPDATE ssh_keys SET private_key = ? WHERE id = ?", "enc:v2:not-valid-base64", key.ID).Error; err != nil {
		t.Fatalf("corrupt stored SSH key fixture: %v", err)
	}
	return key
}

func TestConfigImportPreviewRetainedSecretPendingWithoutSecretReadSQLite(t *testing.T) {
	runConfigImportPreviewRetainedSecretPending(t, "sqlite")
}

func TestConfigImportPreviewRetainedSecretPendingWithoutSecretReadPostgres(t *testing.T) {
	runConfigImportPreviewRetainedSecretPending(t, "postgres")
}

func runConfigImportPreviewRetainedSecretPending(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	key := createCorruptedPreviewSSHKey(t, db, "preview-retained-corrupt-key")
	probe := &configImportPreviewSQLProbe{}
	decision := previewConfigImportDecision(t, db, "overwrite", map[string]any{
		"ssh_keys": []any{map[string]any{"name": key.Name, "username": "replacement-user"}},
	}, probe)
	assertConfigImportPreviewAction(t, decision, configImportEntitySSHKeys, 0, configImportActionPending)
	assertConfigImportPreviewPending(t, decision, configImportEntitySSHKeys, 0, configImportPendingRetainedSecret)
	assertConfigImportPreviewReadOnly(t, probe)

	response, result := serveP1ConfigImport(t, db, nil, "overwrite", map[string]any{
		"ssh_keys": []any{map[string]any{"name": key.Name, "username": "replacement-user"}},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("retained-secret apply status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 0 || result.Updated != 0 || result.Imported != 0 || result.Rejected != 1 {
		t.Fatalf("retained-secret apply result=%+v", result)
	}
	var stored struct {
		Username    string
		PrivateKey  string
		Fingerprint string
	}
	if err := db.Table("ssh_keys").Select("username", "private_key", "fingerprint").Where("id = ?", key.ID).Scan(&stored).Error; err != nil {
		t.Fatalf("read retained-secret row: %v", err)
	}
	if stored.Username != "preview-user" || stored.PrivateKey != "enc:v2:not-valid-base64" || stored.Fingerprint != key.Fingerprint {
		t.Fatalf("retained-secret row changed after apply rejection: %+v", stored)
	}
}

func TestConfigImportPreviewSkipAndReplacementDoNotReadRetainedSecretSQLite(t *testing.T) {
	runConfigImportPreviewSkipAndReplacement(t, "sqlite")
}

func TestConfigImportPreviewSkipAndReplacementDoNotReadRetainedSecretPostgres(t *testing.T) {
	runConfigImportPreviewSkipAndReplacement(t, "postgres")
}

func runConfigImportPreviewSkipAndReplacement(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	key := createCorruptedPreviewSSHKey(t, db, "preview-retained-skip-or-replace")
	body := func(privateKey string) map[string]any {
		item := map[string]any{"name": key.Name}
		if privateKey != "" {
			item["private_key"] = privateKey
		}
		return map[string]any{"ssh_keys": []any{item}}
	}

	probe := &configImportPreviewSQLProbe{}
	skipDecision := previewConfigImportDecision(t, db, "skip", body(""), probe)
	assertConfigImportPreviewAction(t, skipDecision, configImportEntitySSHKeys, 0, configImportActionSkip)
	assertConfigImportPreviewReadOnly(t, probe)

	probe.reset()
	replaceDecision := previewConfigImportDecision(t, db, "overwrite", body(p1ImportPrivateKey(t)), probe)
	assertConfigImportPreviewAction(t, replaceDecision, configImportEntitySSHKeys, 0, configImportActionUpdate)
	if len(replaceDecision.pendingChecks) != 0 {
		t.Fatalf("replacement preview unexpectedly pending retained secret: %+v", replaceDecision.pendingChecks)
	}
	assertConfigImportPreviewReadOnly(t, probe)
}

func TestConfigImportPreviewInvalidSuppliedPrivateKeyRejectsSQLite(t *testing.T) {
	runConfigImportPreviewInvalidSuppliedPrivateKey(t, "sqlite")
}

func TestConfigImportPreviewInvalidSuppliedPrivateKeyRejectsPostgres(t *testing.T) {
	runConfigImportPreviewInvalidSuppliedPrivateKey(t, "postgres")
}

func runConfigImportPreviewInvalidSuppliedPrivateKey(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	body := map[string]any{
		"ssh_keys": []any{map[string]any{
			"name": "preview-invalid-private-key", "username": "preview-user",
			"private_key": "not-a-private-key",
		}},
	}
	probe := &configImportPreviewSQLProbe{}
	decision := previewConfigImportDecision(t, db, "skip", body, probe)
	assertConfigImportPreviewAction(t, decision, configImportEntitySSHKeys, 0, configImportActionReject)
	if !configImportPreviewHasWarning(decision, configImportWarningInvalidPrivateKey) {
		t.Fatalf("invalid private key preview omitted warning: %+v", decision.warnings)
	}
	assertConfigImportPreviewReadOnly(t, probe)

	response, result := serveP1ConfigImport(t, db, nil, "skip", body)
	if response.Code != http.StatusOK {
		t.Fatalf("invalid private key apply status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 0 || result.Updated != 0 || result.Imported != 0 || result.Rejected != 1 {
		t.Fatalf("invalid private key apply result=%+v", result)
	}
	var count int64
	if err := db.Model(&model.SSHKey{}).Where("name = ?", "preview-invalid-private-key").Count(&count).Error; err != nil {
		t.Fatalf("count invalid private key row: %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid private key unexpectedly persisted: %d", count)
	}
}

func configImportPreviewHasWarning(decision configImportDecision, code string) bool {
	for _, warning := range decision.warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func TestConfigImportPreviewNewNodeDefaultTaskTargetPendingSQLite(t *testing.T) {
	runConfigImportPreviewNewNodeDefaultTaskTarget(t, "sqlite")
}

func TestConfigImportPreviewNewNodeDefaultTaskTargetPendingPostgres(t *testing.T) {
	runConfigImportPreviewNewNodeDefaultTaskTarget(t, "postgres")
}

func runConfigImportPreviewNewNodeDefaultTaskTarget(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	first := model.Node{Name: "preview-target-gap-a", Host: "10.20.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "preview-target-gap-a"}
	second := model.Node{Name: "preview-target-gap-b", Host: "10.20.0.2", Port: 22, Username: "root", AuthType: "key", BackupDir: "preview-target-gap-b"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first target-gap node: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second target-gap node: %v", err)
	}
	if err := db.Delete(&first).Error; err != nil {
		t.Fatalf("delete first target-gap node: %v", err)
	}
	body := map[string]any{
		"nodes": []any{map[string]any{
			"name": "preview-new-target-node", "host": "10.20.0.3", "port": 22,
			"username": "root", "auth_type": "key",
		}},
		"tasks": []any{map[string]any{
			"name": "preview-new-target-task", "node_name": "preview-new-target-node",
			"executor_type": "rsync", "rsync_source": "/preview-source",
		}},
	}
	probe := &configImportPreviewSQLProbe{}
	decision := previewConfigImportDecision(t, db, "skip", body, probe)
	assertConfigImportPreviewAction(t, decision, configImportEntityNodes, 0, configImportActionCreate)
	assertConfigImportPreviewAction(t, decision, configImportEntityTasks, 0, configImportActionPending)
	assertConfigImportPreviewPending(t, decision, configImportEntityTasks, 0, configImportPendingAllocatedID)
	taskCandidate := configImportPreviewTaskAt(decision, 0)
	if taskCandidate == nil || taskCandidate.targetSource != configImportTargetManualDefault || strings.TrimSpace(taskCandidate.req.RsyncTarget) != "" {
		t.Fatalf("new-node default target preview used a provisional filesystem path: %+v", taskCandidate)
	}
	assertConfigImportPreviewReadOnly(t, probe)

	response, result := serveP1ConfigImport(t, db, nil, "skip", body)
	if response.Code != http.StatusOK {
		t.Fatalf("new-node default target apply status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 2 || result.Updated != 0 || result.Imported != 2 || result.Rejected != 0 {
		t.Fatalf("new-node default target apply result=%+v", result)
	}
	var importedNode model.Node
	if err := db.Where("name = ?", "preview-new-target-node").First(&importedNode).Error; err != nil {
		t.Fatalf("read new target node: %v", err)
	}
	var importedTask model.Task
	if err := db.Where("name = ?", "preview-new-target-task").First(&importedTask).Error; err != nil {
		t.Fatalf("read new target task: %v", err)
	}
	if importedTask.NodeID != importedNode.ID || importedTask.RsyncTarget == "" ||
		!strings.HasSuffix(importedTask.RsyncTarget, "/nodes/"+fmt.Sprint(importedNode.ID)) {
		t.Fatalf("new-node task target did not use persisted node identity: node=%+v task=%+v", importedNode, importedTask)
	}
	if strings.HasSuffix(importedTask.RsyncTarget, "/nodes/"+fmt.Sprint(taskCandidate.req.NodeID)) {
		t.Fatalf("new-node task target retained provisional node identity: node=%+v task=%+v preview=%+v", importedNode, importedTask, taskCandidate)
	}
}

func TestConfigImportPreviewForwardDependencySQLite(t *testing.T) {
	runConfigImportPreviewForwardDependency(t, "sqlite")
}

func TestConfigImportPreviewForwardDependencyPostgres(t *testing.T) {
	runConfigImportPreviewForwardDependency(t, "postgres")
}

func runConfigImportPreviewForwardDependency(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	node := model.Node{Name: "preview-forward-node", Host: "10.21.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "preview-forward-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create forward-dependency node: %v", err)
	}
	body := map[string]any{
		"tasks": []any{
			map[string]any{
				"name": "preview-forward-dependent", "node_name": node.Name,
				"executor_type": "command", "command": "echo dependent",
				"depends_on_task_name":      "preview-forward-base",
				"depends_on_task_node_name": node.Name,
			},
			map[string]any{
				"name": "preview-forward-base", "node_name": node.Name,
				"executor_type": "command", "command": "echo base",
			},
		},
	}
	probe := &configImportPreviewSQLProbe{}
	decision := previewConfigImportDecision(t, db, "skip", body, probe)
	assertConfigImportPreviewAction(t, decision, configImportEntityTasks, 0, configImportActionCreate)
	assertConfigImportPreviewAction(t, decision, configImportEntityTasks, 1, configImportActionCreate)
	dependent := configImportPreviewTaskAt(decision, 0)
	base := configImportPreviewTaskAt(decision, 1)
	if dependent == nil || base == nil || dependent.dependencyID == nil || *dependent.dependencyID != base.task.ID {
		t.Fatalf("forward dependency was not resolved in the complete candidate graph: dependent=%+v base=%+v", dependent, base)
	}
	assertConfigImportPreviewReadOnly(t, probe)

	response, result := serveP1ConfigImport(t, db, nil, "skip", body)
	if response.Code != http.StatusOK {
		t.Fatalf("forward dependency apply status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 2 || result.Updated != 0 || result.Imported != 2 || result.Rejected != 0 {
		t.Fatalf("forward dependency apply result=%+v", result)
	}
	var persistedDependent, persistedBase model.Task
	if err := db.Where("name = ?", "preview-forward-dependent").First(&persistedDependent).Error; err != nil {
		t.Fatalf("read persisted dependent task: %v", err)
	}
	if err := db.Where("name = ?", "preview-forward-base").First(&persistedBase).Error; err != nil {
		t.Fatalf("read persisted base task: %v", err)
	}
	if persistedDependent.DependsOnTaskID == nil || *persistedDependent.DependsOnTaskID != persistedBase.ID {
		t.Fatalf("forward dependency was not persisted against the real task ID: dependent=%+v base=%+v", persistedDependent, persistedBase)
	}
}

func TestConfigImportPreviewRejectsLongDependencyCycleSQLite(t *testing.T) {
	runConfigImportPreviewLongDependencyCycle(t, "sqlite")
}

func TestConfigImportPreviewRejectsLongDependencyCyclePostgres(t *testing.T) {
	runConfigImportPreviewLongDependencyCycle(t, "postgres")
}

func runConfigImportPreviewLongDependencyCycle(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	node := model.Node{Name: "preview-cycle-node", Host: "10.22.0.1", Port: 22, Username: "root", AuthType: "key", BackupDir: "preview-cycle-node"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create cycle node: %v", err)
	}
	const cycleLength = 12
	tasks := make([]any, cycleLength)
	for index := range tasks {
		tasks[index] = map[string]any{
			"name": fmt.Sprintf("preview-cycle-%02d", index), "node_name": node.Name,
			"executor_type": "command", "command": "echo cycle",
			"depends_on_task_name":      fmt.Sprintf("preview-cycle-%02d", (index+1)%cycleLength),
			"depends_on_task_node_name": node.Name,
		}
	}
	body := map[string]any{"tasks": tasks}
	probe := &configImportPreviewSQLProbe{}
	decision := previewConfigImportDecision(t, db, "overwrite", body, probe)
	for index := range tasks {
		assertConfigImportPreviewAction(t, decision, configImportEntityTasks, index, configImportActionReject)
	}
	if decision.rejected != cycleLength || len(decision.taskCandidates) != cycleLength {
		t.Fatalf("long-cycle preview decision=%+v candidates=%d", decision, len(decision.taskCandidates))
	}
	assertConfigImportPreviewReadOnly(t, probe)

	response, result := serveP1ConfigImport(t, db, nil, "overwrite", body)
	if response.Code != http.StatusOK {
		t.Fatalf("long-cycle apply status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 0 || result.Updated != 0 || result.Imported != 0 || result.Rejected != cycleLength {
		t.Fatalf("long-cycle apply result=%+v", result)
	}
	var count int64
	if err := db.Model(&model.Task{}).Where("name LIKE ?", "preview-cycle-%").Count(&count).Error; err != nil {
		t.Fatalf("count long-cycle tasks: %v", err)
	}
	if count != 0 {
		t.Fatalf("long-cycle rejected tasks persisted: %d", count)
	}
}

func TestConfigImportPreviewQueryFailureNeverUsesEmptyInventorySQLite(t *testing.T) {
	runConfigImportPreviewQueryFailure(t, "sqlite")
}

func TestConfigImportPreviewQueryFailureNeverUsesEmptyInventoryPostgres(t *testing.T) {
	runConfigImportPreviewQueryFailure(t, "postgres")
}

func runConfigImportPreviewQueryFailure(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	key := model.SSHKey{Name: "preview-query-failure-key", Username: "preview-user"}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create query-failure key: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"ssh_keys": []any{map[string]any{"name": key.Name}},
	})
	if err != nil {
		t.Fatalf("marshal query-failure body: %v", err)
	}
	input, err := prepareConfigImportInput(context.Background(), NewConfigHandler(db, nil), payload, "skip")
	if err != nil {
		t.Fatalf("prepare query-failure input: %v", err)
	}
	injected := errors.New("CONFIG_IMPORT_PREVIEW_INVENTORY_QUERY_FAILURE")
	callbackName := "test:config-import-preview-query-failure-" + engine
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
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	snapshot, loadErr := loadConfigImportSnapshot(context.Background(), db, input)
	if loadErr == nil {
		t.Fatalf("inventory query failure was swallowed; snapshot=%+v", snapshot)
	}
	if !queryFailed {
		t.Fatalf("inventory query-failure callback did not observe the SSH-key query: %v", loadErr)
	}
	if strings.Contains(loadErr.Error(), "empty inventory") {
		t.Fatalf("query failure was converted to an empty-inventory result: %v", loadErr)
	}
}

func assertConfigImportPreviewItemAction(
	t *testing.T,
	decision configImportDecision,
	entity string,
	index int,
	want configImportAction,
) configImportItemDecision {
	t.Helper()
	rows := decision.itemDecisions[entity]
	if index < 0 || index >= len(rows) {
		t.Fatalf("preview decision omitted ledger row %s[%d], want action %q: %+v", entity, index, want, rows)
	}
	got := rows[index]
	if got.Action != want {
		t.Fatalf("preview ledger action for %s[%d]=%q, want %q: %+v", entity, index, got.Action, want, got)
	}
	return got
}

func assertConfigImportPreviewItemPending(
	t *testing.T,
	decision configImportDecision,
	entity string,
	index int,
	code string,
) {
	t.Helper()
	row := assertConfigImportPreviewItemAction(t, decision, entity, index, configImportActionPending)
	if row.PendingCode != code {
		t.Fatalf("preview ledger pending code for %s[%d]=%q, want %q", entity, index, row.PendingCode, code)
	}
	assertConfigImportPreviewPending(t, decision, entity, index, code)
}

func readConfigImportPreviewRawRow(t *testing.T, db *gorm.DB, table string, id uint) map[string]any {
	t.Helper()
	row := make(map[string]any)
	if err := db.Table(table).Where("id = ?", id).Take(&row).Error; err != nil {
		t.Fatalf("read raw %s[%d]: %v", table, id, err)
	}
	return row
}

func createValidPreviewSSHKey(t *testing.T, db *gorm.DB, name string) model.SSHKey {
	t.Helper()
	privateKey := p1ImportPrivateKey(t)
	prepared, keyType, err := sshutil.ValidateAndPreparePrivateKey(privateKey, sshutil.SSHKeyTypeAuto)
	if err != nil {
		t.Fatalf("prepare valid retained-key fixture: %v", err)
	}
	key := model.SSHKey{
		Name: name, Username: "preview-user", KeyType: keyType,
		PrivateKey: prepared, Fingerprint: generateFingerprint(prepared),
		AllowedPurposes: sshutil.PurposeTerminal,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create valid retained-key fixture: %v", err)
	}
	return key
}

func assertConfigImportPreviewNoProvisionalSQL(t *testing.T, probe *configImportPreviewSQLProbe, decision configImportDecision) {
	t.Helper()
	provisional := make([]string, 0)
	for _, candidate := range decision.nodeCandidates {
		if candidate != nil && candidate.provisional != 0 {
			provisional = append(provisional, fmt.Sprint(candidate.provisional))
		}
	}
	for _, candidate := range decision.keyCandidates {
		if candidate != nil && candidate.provisional != 0 {
			provisional = append(provisional, fmt.Sprint(candidate.provisional))
		}
	}
	for _, candidate := range decision.policyCandidates {
		if candidate != nil && candidate.provisional != 0 {
			provisional = append(provisional, fmt.Sprint(candidate.provisional))
		}
	}
	for _, candidate := range decision.taskCandidates {
		if candidate != nil && candidate.provisional != 0 {
			provisional = append(provisional, fmt.Sprint(candidate.provisional))
		}
	}
	for _, statement := range probe.statements {
		for _, id := range provisional {
			if strings.Contains(statement, id) {
				t.Fatalf("preview SQL exposed provisional identity %s: %s", id, statement)
			}
		}
	}
}

func countConfigImportPreviewRows(t *testing.T, db *gorm.DB, model any, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := db.Model(model).Where(query, args...).Count(&count).Error; err != nil {
		t.Fatalf("count preview rows: %v", err)
	}
	return count
}

func assertConfigImportPreviewTaskWrites(t *testing.T, probe *configImportPreviewSQLProbe, want int) {
	t.Helper()
	count := 0
	for _, statement := range probe.writeStatements() {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		normalized = strings.ReplaceAll(normalized, `"`, "")
		normalized = strings.ReplaceAll(normalized, "`", "")
		if strings.HasPrefix(normalized, "insert into tasks ") ||
			strings.HasPrefix(normalized, "update tasks ") {
			count++
		}
	}
	if count != want {
		t.Fatalf("task SQL writes=%d, want %d: %v", count, want, probe.writeStatements())
	}
}

func TestConfigImportPreviewRetainedValidKeyReferencesAndDefaultTargetsSQLite(t *testing.T) {
	runConfigImportPreviewRetainedValidKeyReferencesAndDefaultTargets(t, "sqlite")
}

func TestConfigImportPreviewRetainedValidKeyReferencesAndDefaultTargetsPostgres(t *testing.T) {
	runConfigImportPreviewRetainedValidKeyReferencesAndDefaultTargets(t, "postgres")
}

func runConfigImportPreviewRetainedValidKeyReferencesAndDefaultTargets(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	key := createValidPreviewSSHKey(t, db, "preview-retained-valid-key")
	policy := model.Policy{
		Name:       "preview-existing-policy",
		SourcePath: "/preview-policy-source",
		TargetPath: filepath.Join(t.TempDir(), "policy-target"),
		CronSpec:   "",
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create retained-key policy: %v", err)
	}
	newNodeName := "preview-retained-valid-node"
	body := map[string]any{
		"nodes": []any{map[string]any{
			"name": newNodeName, "host": "10.30.0.1", "port": 22,
			"username": "root", "auth_type": "key", "ssh_key_name": key.Name,
		}},
		"ssh_keys": []any{map[string]any{
			"name": key.Name, "allowed_node_names": []any{newNodeName},
		}},
		"tasks": []any{
			map[string]any{
				"name": "preview-retained-manual-task", "node_name": newNodeName,
				"executor_type": "rsync", "rsync_source": "/preview-manual-source",
			},
			map[string]any{
				"name": "preview-retained-policy-task", "node_name": newNodeName,
				"policy_name": policy.Name, "executor_type": "rsync",
			},
		},
	}
	probe := &configImportPreviewSQLProbe{}
	decision := previewConfigImportDecision(t, db, "overwrite", body, probe)
	assertConfigImportPreviewItemPending(t, decision, configImportEntitySSHKeys, 0, configImportPendingRetainedSecret)
	assertConfigImportPreviewItemPending(t, decision, configImportEntityNodes, 0, configImportPendingReference)
	for index, wantSource := range []configImportTargetSource{
		configImportTargetManualDefault, configImportTargetPolicyDefault,
	} {
		assertConfigImportPreviewItemPending(t, decision, configImportEntityTasks, index, configImportPendingAllocatedID)
		candidate := configImportPreviewTaskAt(decision, index)
		if candidate == nil || candidate.targetSource != wantSource || strings.TrimSpace(candidate.req.RsyncTarget) != "" {
			t.Fatalf("retained-key default target preview used an identity-dependent path at task %d: %+v", index, candidate)
		}
	}
	assertConfigImportPreviewReadOnly(t, probe)
	assertConfigImportPreviewNoProvisionalSQL(t, probe, decision)

	applyProbe := &configImportPreviewSQLProbe{}
	applyDB := db.Session(&gorm.Session{Logger: applyProbe})
	response, result := serveP1ConfigImport(t, applyDB, nil, "overwrite", body)
	if response.Code != http.StatusOK {
		t.Fatalf("retained valid-key apply status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 3 || result.Updated != 1 || result.Imported != 4 ||
		result.Rejected != 0 || result.Nodes != 1 || result.SSHKeys != 1 || result.Tasks != 2 {
		t.Fatalf("retained valid-key apply result=%+v", result)
	}
	var importedNode model.Node
	if err := db.Where("name = ?", newNodeName).First(&importedNode).Error; err != nil {
		t.Fatalf("read retained valid-key node: %v", err)
	}
	var storedKey model.SSHKey
	if err := db.Where("id = ?", key.ID).First(&storedKey).Error; err != nil {
		t.Fatalf("read retained valid-key row: %v", err)
	}
	if storedKey.PrivateKey == "" || storedKey.Fingerprint != key.Fingerprint ||
		storedKey.AllowedNodeIDs != fmt.Sprint(importedNode.ID) {
		t.Fatalf("retained key was not validated/remapped to the real node identity: key=%+v node=%+v", storedKey, importedNode)
	}
	if importedNode.SSHKeyID == nil || *importedNode.SSHKeyID != storedKey.ID {
		t.Fatalf("new node was not bound to the retained key: node=%+v key=%+v", importedNode, storedKey)
	}
	var manualTask, policyTask model.Task
	if err := db.Where("name = ?", "preview-retained-manual-task").First(&manualTask).Error; err != nil {
		t.Fatalf("read retained manual task: %v", err)
	}
	if err := db.Where("name = ?", "preview-retained-policy-task").First(&policyTask).Error; err != nil {
		t.Fatalf("read retained policy task: %v", err)
	}
	if manualTask.NodeID != importedNode.ID || policyTask.NodeID != importedNode.ID ||
		manualTask.RsyncTarget == "" || policyTask.RsyncTarget == "" ||
		!strings.HasSuffix(manualTask.RsyncTarget, "/manual/nodes/"+fmt.Sprint(importedNode.ID)) ||
		!strings.HasSuffix(policyTask.RsyncTarget, "/policies/"+fmt.Sprint(policy.ID)+"/nodes/"+fmt.Sprint(importedNode.ID)) {
		t.Fatalf("retained default tasks did not use persisted identities: node=%+v manual=%+v policy=%+v", importedNode, manualTask, policyTask)
	}
	for _, candidate := range decision.taskCandidates {
		if candidate != nil && candidate.provisional != 0 {
			if strings.Contains(manualTask.RsyncTarget, fmt.Sprint(candidate.provisional)) ||
				strings.Contains(policyTask.RsyncTarget, fmt.Sprint(candidate.provisional)) {
				t.Fatalf("retained default task target used provisional identity: candidate=%+v manual=%+v policy=%+v", candidate, manualTask, policyTask)
			}
		}
	}
	assertConfigImportPreviewNoProvisionalSQL(t, applyProbe, decision)
}

func TestConfigImportPreviewCorruptRetainedKeyReferenceOutcomesSQLite(t *testing.T) {
	runConfigImportPreviewCorruptRetainedKeyReferenceOutcomes(t, "sqlite")
}

func TestConfigImportPreviewCorruptRetainedKeyReferenceOutcomesPostgres(t *testing.T) {
	runConfigImportPreviewCorruptRetainedKeyReferenceOutcomes(t, "postgres")
}

func runConfigImportPreviewCorruptRetainedKeyReferenceOutcomes(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	key := createCorruptedPreviewSSHKey(t, db, "preview-corrupt-reference-key")
	existingNode := model.Node{
		Name: "preview-corrupt-existing-node", Host: "10.31.0.1", Port: 22,
		Username: "old-user", AuthType: "key", Tags: "old-tag",
		BasePath: "/old/base", BackupDir: "old-backup", SSHKeyID: &key.ID,
	}
	if err := db.Create(&existingNode).Error; err != nil {
		t.Fatalf("create corrupt-reference existing node: %v", err)
	}
	beforeNode := readConfigImportPreviewRawRow(t, db, "nodes", existingNode.ID)
	beforeKey := readConfigImportPreviewRawRow(t, db, "ssh_keys", key.ID)
	newNodeName := "preview-corrupt-new-node"
	body := map[string]any{
		"nodes": []any{
			map[string]any{
				"name": existingNode.Name, "host": "10.31.0.99", "port": 2200,
				"username": "changed-user", "auth_type": "key", "tags": "changed-tag",
				"base_path": "/changed/base", "ssh_key_name": key.Name,
			},
			map[string]any{
				"name": newNodeName, "host": "10.31.0.2", "port": 22,
				"username": "new-user", "auth_type": "key", "ssh_key_name": key.Name,
			},
		},
		"ssh_keys": []any{map[string]any{"name": key.Name}},
		"tasks": []any{map[string]any{
			"name": "preview-corrupt-structural-task", "node_name": newNodeName,
			"executor_type": "command", "command": "echo structural",
		}},
	}
	probe := &configImportPreviewSQLProbe{}
	decision := previewConfigImportDecision(t, db, "overwrite", body, probe)
	assertConfigImportPreviewItemPending(t, decision, configImportEntitySSHKeys, 0, configImportPendingRetainedSecret)
	assertConfigImportPreviewItemPending(t, decision, configImportEntityNodes, 0, configImportPendingReference)
	assertConfigImportPreviewItemPending(t, decision, configImportEntityNodes, 1, configImportPendingReference)
	assertConfigImportPreviewItemPending(t, decision, configImportEntityTasks, 0, configImportPendingReference)
	assertConfigImportPreviewReadOnly(t, probe)
	assertConfigImportPreviewNoProvisionalSQL(t, probe, decision)

	applyProbe := &configImportPreviewSQLProbe{}
	response, result := serveP1ConfigImport(t, db.Session(&gorm.Session{Logger: applyProbe}), nil, "overwrite", body)
	if response.Code != http.StatusOK {
		t.Fatalf("corrupt-reference apply status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 2 || result.Updated != 0 || result.Imported != 2 ||
		result.Rejected != 2 || result.Nodes != 1 || result.SSHKeys != 0 || result.Tasks != 1 {
		t.Fatalf("corrupt-reference apply result=%+v", result)
	}
	afterNode := readConfigImportPreviewRawRow(t, db, "nodes", existingNode.ID)
	afterKey := readConfigImportPreviewRawRow(t, db, "ssh_keys", key.ID)
	if !reflect.DeepEqual(beforeNode, afterNode) {
		t.Fatalf("existing node changed despite retained-key validation failure: before=%v after=%v", beforeNode, afterNode)
	}
	if !reflect.DeepEqual(beforeKey, afterKey) {
		t.Fatalf("corrupt retained key changed despite validation failure: before=%v after=%v", beforeKey, afterKey)
	}
	var importedNode model.Node
	if err := db.Where("name = ?", newNodeName).First(&importedNode).Error; err != nil {
		t.Fatalf("read structural new node: %v", err)
	}
	if importedNode.SSHKeyID != nil {
		t.Fatalf("structural new node retained a failed key relation: %+v", importedNode)
	}
	var structuralTask model.Task
	if err := db.Where("name = ?", "preview-corrupt-structural-task").First(&structuralTask).Error; err != nil {
		t.Fatalf("read structural task: %v", err)
	}
	if structuralTask.NodeID != importedNode.ID || structuralTask.Command != "echo structural" {
		t.Fatalf("structural task was not retained after key rejection: node=%+v task=%+v", importedNode, structuralTask)
	}
	if !configImportPreviewResultHasWarning(result, configImportEntitySSHKeys, 0, configImportWarningInvalidPrivateKey) ||
		!configImportPreviewResultHasWarning(result, configImportEntityNodes, 0, configImportWarningUnresolvedSSHKey) ||
		!configImportPreviewResultHasWarning(result, configImportEntityNodes, 1, configImportWarningUnresolvedSSHKey) {
		t.Fatalf("corrupt-reference apply warnings=%+v", result.Warnings)
	}
	assertConfigImportPreviewNoProvisionalSQL(t, applyProbe, decision)
}

func configImportPreviewResultHasWarning(result configImportResult, entity string, index int, code string) bool {
	for _, warning := range result.Warnings {
		if warning.Entity == entity && warning.Index == index && warning.Code == code {
			return true
		}
	}
	return false
}

func TestConfigImportPreviewDependencyRejectionClosureSQLite(t *testing.T) {
	runConfigImportPreviewDependencyRejectionClosure(t, "sqlite")
}

func TestConfigImportPreviewDependencyRejectionClosurePostgres(t *testing.T) {
	runConfigImportPreviewDependencyRejectionClosure(t, "postgres")
}

func runConfigImportPreviewDependencyRejectionClosure(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	node := model.Node{
		Name: "preview-dependency-closure-node", Host: "10.32.0.1",
		Port: 22, Username: "root", AuthType: "key", BackupDir: "dependency-closure",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("create dependency-closure node: %v", err)
	}
	root := model.Task{
		Name: "preview-dependency-root", NodeID: node.ID, ExecutorType: "command",
		Command: "echo root", Status: "pending", Source: "manual",
	}
	if err := db.Create(&root).Error; err != nil {
		t.Fatalf("create dependency-closure root: %v", err)
	}
	beforeRoot := readConfigImportPreviewRawRow(t, db, "tasks", root.ID)
	body := map[string]any{
		"tasks": []any{
			map[string]any{
				"name": "preview-dependency-a", "node_name": node.Name,
				"executor_type": "command", "command": "echo a",
				"cron_spec":            "*/5 * * * *",
				"depends_on_task_name": root.Name, "depends_on_task_node_name": node.Name,
			},
			map[string]any{
				"name": "preview-dependency-b", "node_name": node.Name,
				"executor_type": "command", "command": "echo b",
				"depends_on_task_name": "preview-dependency-a", "depends_on_task_node_name": node.Name,
			},
			map[string]any{
				"name": "preview-dependency-c", "node_name": node.Name,
				"executor_type": "command", "command": "echo c",
				"depends_on_task_name": "preview-dependency-b", "depends_on_task_node_name": node.Name,
			},
			map[string]any{
				"name": "preview-dependency-unrelated", "node_name": node.Name,
				"executor_type": "command", "command": "echo unrelated",
			},
		},
	}
	probe := &configImportPreviewSQLProbe{}
	decision := previewConfigImportDecision(t, db, "overwrite", body, probe)
	for index := range 3 {
		assertConfigImportPreviewItemAction(t, decision, configImportEntityTasks, index, configImportActionReject)
	}
	assertConfigImportPreviewItemAction(t, decision, configImportEntityTasks, 3, configImportActionCreate)
	if decision.rejected != 3 || len(decision.pendingChecks) != 0 {
		t.Fatalf("dependency rejection closure preview decision=%+v", decision)
	}
	assertConfigImportPreviewReadOnly(t, probe)
	assertConfigImportPreviewNoProvisionalSQL(t, probe, decision)

	applyProbe := &configImportPreviewSQLProbe{}
	response, result := serveP1ConfigImport(t, db.Session(&gorm.Session{Logger: applyProbe}), nil, "overwrite", body)
	if response.Code != http.StatusOK {
		t.Fatalf("dependency rejection closure apply status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 1 || result.Updated != 0 || result.Imported != 1 ||
		result.Rejected != 3 || result.Tasks != 1 {
		t.Fatalf("dependency rejection closure apply result=%+v", result)
	}
	afterRoot := readConfigImportPreviewRawRow(t, db, "tasks", root.ID)
	if !reflect.DeepEqual(beforeRoot, afterRoot) {
		t.Fatalf("existing dependency root changed: before=%v after=%v", beforeRoot, afterRoot)
	}
	if got := countConfigImportPreviewRows(t, db, &model.Task{}, "name IN ?", []string{
		"preview-dependency-a", "preview-dependency-b", "preview-dependency-c",
	}); got != 0 {
		t.Fatalf("rejected dependency closure persisted %d rows", got)
	}
	var unrelated model.Task
	if err := db.Where("name = ?", "preview-dependency-unrelated").First(&unrelated).Error; err != nil {
		t.Fatalf("read unrelated surviving task: %v", err)
	}
	if unrelated.DependsOnTaskID != nil || unrelated.Command != "echo unrelated" {
		t.Fatalf("unrelated task was not independently imported: %+v", unrelated)
	}
	assertConfigImportPreviewNoProvisionalSQL(t, applyProbe, decision)
}

func TestConfigImportPreviewAllocatedIdentityStillRejectsInvalidCronSQLite(t *testing.T) {
	runConfigImportPreviewAllocatedIdentityStillRejectsInvalidCron(t, "sqlite")
}

func TestConfigImportPreviewAllocatedIdentityStillRejectsInvalidCronPostgres(t *testing.T) {
	runConfigImportPreviewAllocatedIdentityStillRejectsInvalidCron(t, "postgres")
}

func runConfigImportPreviewAllocatedIdentityStillRejectsInvalidCron(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	body := map[string]any{
		"nodes": []any{map[string]any{
			"name": "preview-invalid-cron-node", "host": "10.33.0.1",
			"port": 22, "username": "root", "auth_type": "key",
		}},
		"tasks": []any{map[string]any{
			"name": "preview-invalid-cron-task", "node_name": "preview-invalid-cron-node",
			"executor_type": "rsync", "rsync_source": "/preview-invalid-cron-source",
			"cron_spec": "not-a-cron-expression",
		}},
	}
	probe := &configImportPreviewSQLProbe{}
	decision := previewConfigImportDecision(t, db, "skip", body, probe)
	assertConfigImportPreviewItemAction(t, decision, configImportEntityNodes, 0, configImportActionCreate)
	assertConfigImportPreviewItemAction(t, decision, configImportEntityTasks, 0, configImportActionReject)
	if len(decision.pendingChecks) != 0 || decision.rejected != 1 ||
		!configImportPreviewHasWarning(decision, configImportWarningInvalidInput) {
		t.Fatalf("invalid-cron preview deferred a known error: %+v", decision)
	}
	if configImportPreviewTaskAt(decision, 0) != nil {
		t.Fatalf("invalid-cron preview retained a task candidate after rejection: %+v", decision.taskCandidates)
	}
	assertConfigImportPreviewReadOnly(t, probe)
	assertConfigImportPreviewNoProvisionalSQL(t, probe, decision)

	applyProbe := &configImportPreviewSQLProbe{}
	response, result := serveP1ConfigImport(t, db.Session(&gorm.Session{Logger: applyProbe}), nil, "skip", body)
	if response.Code != http.StatusOK {
		t.Fatalf("invalid-cron apply status=%d body=%s", response.Code, response.Body.String())
	}
	if result.Created != 1 || result.Updated != 0 || result.Imported != 1 ||
		result.Rejected != 1 || result.Nodes != 1 || result.Tasks != 0 {
		t.Fatalf("invalid-cron apply result=%+v", result)
	}
	if got := countConfigImportPreviewRows(t, db, &model.Task{}, "name = ?", "preview-invalid-cron-task"); got != 0 {
		t.Fatalf("invalid-cron task unexpectedly persisted: %d", got)
	}
	assertConfigImportPreviewNoProvisionalSQL(t, applyProbe, decision)
}

func TestConfigImportPreviewRealIdentityTargetConflictRollsBackBeforeTaskWritesSQLite(t *testing.T) {
	runConfigImportPreviewRealIdentityTargetConflictRollsBackBeforeTaskWrites(t, "sqlite")
}

func TestConfigImportPreviewRealIdentityTargetConflictRollsBackBeforeTaskWritesPostgres(t *testing.T) {
	runConfigImportPreviewRealIdentityTargetConflictRollsBackBeforeTaskWrites(t, "postgres")
}

func runConfigImportPreviewRealIdentityTargetConflictRollsBackBeforeTaskWrites(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	realRoot := filepath.Join(t.TempDir(), "preview-real-target-root")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatalf("create real target root: %v", err)
	}
	aliasParent := t.TempDir()
	aliasRoot := filepath.Join(aliasParent, "preview-target-alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Fatalf("create target alias: %v", err)
	}
	ownerNode := model.Node{
		Name: "preview-target-owner-node", Host: "10.34.0.1",
		Port: 22, Username: "root", AuthType: "key", BackupDir: "target-owner",
	}
	if err := db.Create(&ownerNode).Error; err != nil {
		t.Fatalf("create target-owner node: %v", err)
	}
	ownerPolicy := model.Policy{
		Name:       "preview-target-owner-policy",
		SourcePath: "/preview-owner-source",
		TargetPath: filepath.Join(t.TempDir(), "owner-policy-target"),
		CronSpec:   "",
	}
	if err := db.Create(&ownerPolicy).Error; err != nil {
		t.Fatalf("create target-owner policy: %v", err)
	}
	conflictTarget := filepath.Join(aliasRoot, ".xirang", "policies")
	ownerTask := model.Task{
		Name: "preview-target-owner-task", NodeID: ownerNode.ID, PolicyID: &ownerPolicy.ID,
		ExecutorType: "rsync", RsyncSource: "/preview-owner-source",
		RsyncTarget: conflictTarget, Status: "pending", Source: "manual",
	}
	if err := db.Create(&ownerTask).Error; err != nil {
		t.Fatalf("create target-owner task: %v", err)
	}
	importPolicyName := "preview-target-import-policy"
	importNodeName := "preview-target-import-node"
	importTaskName := "preview-target-import-task"
	body := map[string]any{
		"nodes": []any{map[string]any{
			"name": importNodeName, "host": "10.34.0.2",
			"port": 22, "username": "root", "auth_type": "key",
		}},
		"policies": []any{map[string]any{
			"name": importPolicyName, "source_path": "/preview-import-source",
			"target_path": realRoot, "cron_spec": "",
		}},
		"tasks": []any{map[string]any{
			"name": importTaskName, "node_name": importNodeName,
			"policy_name": importPolicyName, "executor_type": "rsync",
		}},
	}
	probe := &configImportPreviewSQLProbe{}
	decision := previewConfigImportDecision(t, db, "skip", body, probe)
	assertConfigImportPreviewItemAction(t, decision, configImportEntityNodes, 0, configImportActionCreate)
	assertConfigImportPreviewItemAction(t, decision, configImportEntityPolicies, 0, configImportActionCreate)
	assertConfigImportPreviewItemPending(t, decision, configImportEntityTasks, 0, configImportPendingAllocatedID)
	taskCandidate := configImportPreviewTaskAt(decision, 0)
	if taskCandidate == nil || taskCandidate.targetSource != configImportTargetPolicyDefault ||
		strings.TrimSpace(taskCandidate.req.RsyncTarget) != "" {
		t.Fatalf("target-conflict preview materialized a provisional filesystem path: %+v", taskCandidate)
	}
	assertConfigImportPreviewReadOnly(t, probe)
	assertConfigImportPreviewNoProvisionalSQL(t, probe, decision)

	beforeOwnerNode := readConfigImportPreviewRawRow(t, db, "nodes", ownerNode.ID)
	beforeOwnerPolicy := readConfigImportPreviewRawRow(t, db, "policies", ownerPolicy.ID)
	beforeOwnerTask := readConfigImportPreviewRawRow(t, db, "tasks", ownerTask.ID)
	applyProbe := &configImportPreviewSQLProbe{}
	response, _ := serveP1ConfigImport(t, db.Session(&gorm.Session{Logger: applyProbe}), nil, "skip", body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("target-conflict apply status=%d body=%s", response.Code, response.Body.String())
	}
	assertConfigImportPreviewTaskWrites(t, applyProbe, 0)
	afterOwnerNode := readConfigImportPreviewRawRow(t, db, "nodes", ownerNode.ID)
	afterOwnerPolicy := readConfigImportPreviewRawRow(t, db, "policies", ownerPolicy.ID)
	afterOwnerTask := readConfigImportPreviewRawRow(t, db, "tasks", ownerTask.ID)
	if !reflect.DeepEqual(beforeOwnerNode, afterOwnerNode) ||
		!reflect.DeepEqual(beforeOwnerPolicy, afterOwnerPolicy) ||
		!reflect.DeepEqual(beforeOwnerTask, afterOwnerTask) {
		t.Fatalf("target-conflict rollback changed an existing row")
	}
	if got := countConfigImportPreviewRows(t, db, &model.Node{}, "name = ?", importNodeName); got != 0 {
		t.Fatalf("target-conflict rollback retained new node: %d", got)
	}
	if got := countConfigImportPreviewRows(t, db, &model.Policy{}, "name = ?", importPolicyName); got != 0 {
		t.Fatalf("target-conflict rollback retained new policy: %d", got)
	}
	if got := countConfigImportPreviewRows(t, db, &model.Task{}, "name = ?", importTaskName); got != 0 {
		t.Fatalf("target-conflict rollback retained new task: %d", got)
	}
	foundPriorWrites := false
	for _, statement := range applyProbe.writeStatements() {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		normalized = strings.ReplaceAll(normalized, `"`, "")
		normalized = strings.ReplaceAll(normalized, "`", "")
		if strings.HasPrefix(normalized, "insert into nodes ") ||
			strings.HasPrefix(normalized, "insert into policies ") {
			foundPriorWrites = true
			break
		}
	}
	if !foundPriorWrites {
		t.Fatalf("target-conflict apply did not reach node/policy persistence: %v", applyProbe.writeStatements())
	}
}
func previewConfigImportDecisionWithError(
	ctx context.Context,
	t *testing.T,
	db *gorm.DB,
	conflict string,
	body any,
	probe *configImportPreviewSQLProbe,
) (configImportDecision, error) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		return configImportDecision{}, fmt.Errorf("marshal preview import payload: %w", err)
	}
	handler := NewConfigHandler(db, nil)
	input, err := prepareConfigImportInput(ctx, handler, payload, conflict)
	if err != nil {
		return configImportDecision{}, err
	}
	decisionDB := db
	if probe != nil {
		decisionDB = db.Session(&gorm.Session{Logger: probe})
	}
	snapshot, err := loadConfigImportSnapshot(ctx, decisionDB, input)
	if err != nil {
		return configImportDecision{}, err
	}
	return evaluateConfigImport(ctx, input, snapshot)
}

func cloneConfigImportPreviewJSONMap(t *testing.T, source map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal preview JSON clone: %v", err)
	}
	var clone map[string]any
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatalf("unmarshal preview JSON clone: %v", err)
	}
	return clone
}

func TestConfigImportPreviewStandaloneExportRoundTripNilPolicySQLite(t *testing.T) {
	runConfigImportPreviewStandaloneExportRoundTrip(t, "sqlite")
}

func TestConfigImportPreviewStandaloneExportRoundTripNilPolicyPostgres(t *testing.T) {
	runConfigImportPreviewStandaloneExportRoundTrip(t, "postgres")
}

func runConfigImportPreviewStandaloneExportRoundTrip(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	source, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateConfigAssetGraphDB(t, source)
	node := model.Node{
		Name: "preview-standalone-export-node", Host: "10.40.0.1", Port: 22,
		Username: "root", AuthType: "key", BackupDir: "preview-standalone-export-node",
	}
	if err := source.Create(&node).Error; err != nil {
		t.Fatalf("create standalone export node: %v", err)
	}
	task := model.Task{
		Name: "preview-standalone-export-task", NodeID: node.ID,
		ExecutorType: "command", Command: "echo standalone export",
		Status: "pending", Source: "manual", Enabled: true,
	}
	if err := source.Create(&task).Error; err != nil {
		t.Fatalf("create standalone export task: %v", err)
	}
	exported := parseConfigExportPayload(t, serveConfigExport(t, source, false))
	classic, ok := exported["data"].(map[string]any)
	if !ok {
		t.Fatalf("exported classic data type=%T", exported["data"])
	}
	rawTasks, ok := classic["tasks"].([]any)
	if !ok || len(rawTasks) != 1 {
		t.Fatalf("exported standalone task payload=%#v", classic["tasks"])
	}
	exportedTask, ok := rawTasks[0].(map[string]any)
	if !ok {
		t.Fatalf("exported standalone task type=%T", rawTasks[0])
	}
	if exportedTask["policy_id"] != nil || exportedTask["policy_name"] != "" {
		t.Fatalf("standalone export lost null/empty policy reference semantics: %#v", exportedTask)
	}

	variants := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name:   "production_export_null_id_empty_name",
			mutate: func(map[string]any) {},
		},
		{
			name: "both_policy_references_null",
			mutate: func(item map[string]any) {
				item["policy_id"] = nil
				item["policy_name"] = nil
			},
		},
		{
			name: "empty_name_without_numeric_reference",
			mutate: func(item map[string]any) {
				delete(item, "policy_id")
				item["policy_name"] = ""
			},
		},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			target, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateConfigAssetGraphDB(t, target)
			payload := cloneConfigImportPreviewJSONMap(t, exported)
			data := payload["data"].(map[string]any)
			items := data["tasks"].([]any)
			item := items[0].(map[string]any)
			variant.mutate(item)
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal standalone round-trip payload: %v", err)
			}

			preview := serveConfigImportPreviewHTTP(t, target, nil, "skip", body, nil)
			if preview.Code != http.StatusOK {
				t.Fatalf("standalone preview status=%d body=%s", preview.Code, preview.Body.String())
			}
			dto := decodeConfigImportPreviewHTTP(t, preview)
			taskEntity := previewHTTPEntity(t, dto, configImportEntityTasks)
			if taskEntity.Input != 1 || taskEntity.Created != 1 ||
				taskEntity.Updated != 0 || taskEntity.Skipped != 0 ||
				taskEntity.Rejected != 0 || taskEntity.PendingValidation != 0 {
				t.Fatalf("standalone preview task entity=%+v", taskEntity)
			}

			response, result := serveP1ConfigImport(t, target, nil, "skip", payload)
			if response.Code != http.StatusOK {
				t.Fatalf("standalone apply status=%d body=%s", response.Code, response.Body.String())
			}
			if result.Nodes != 1 || result.Tasks != 1 || result.Created != 2 || result.Updated != 0 ||
				result.Imported != 2 || result.Rejected != 0 || result.Skipped != 0 {
				t.Fatalf("standalone apply result=%+v", result)
			}
			var persisted model.Task
			if err := target.Where("name = ?", task.Name).First(&persisted).Error; err != nil {
				t.Fatalf("read standalone imported task: %v", err)
			}
			if persisted.PolicyID != nil || persisted.ExecutorType != "command" ||
				persisted.Command != task.Command {
				t.Fatalf("standalone task policy/executor changed during round-trip: %+v", persisted)
			}
		})
	}
}

func repeatedPreviewPolicyBody(t *testing.T) map[string]any {
	t.Helper()
	root := t.TempDir()
	return map[string]any{
		"policies": []any{
			map[string]any{
				"name": "preview-repeated-new-policy", "description": "first policy",
				"source_path":    filepath.Join(root, "source-first"),
				"target_path":    filepath.Join(root, "target-first"),
				"retention_days": 3, "max_retries": 1, "enabled": false,
			},
			map[string]any{
				"name": "preview-repeated-new-policy", "description": "last policy",
				"source_path":    filepath.Join(root, "source-last"),
				"target_path":    filepath.Join(root, "target-last"),
				"retention_days": 17, "max_retries": 9, "enabled": true,
			},
		},
	}
}

func TestConfigImportPreviewRepeatedNewPolicySQLite(t *testing.T) {
	runConfigImportPreviewRepeatedNewPolicy(t, "sqlite")
}

func TestConfigImportPreviewRepeatedNewPolicyPostgres(t *testing.T) {
	runConfigImportPreviewRepeatedNewPolicy(t, "postgres")
}

func runConfigImportPreviewRepeatedNewPolicy(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	for _, conflict := range []string{"skip", "overwrite"} {
		t.Run(conflict, func(t *testing.T) {
			db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateP1ConfigImport(t, db)
			body := repeatedPreviewPolicyBody(t)
			probe := &configImportPreviewSQLProbe{}
			decision, err := previewConfigImportDecisionWithError(context.Background(), t, db, conflict, body, probe)
			if err != nil {
				t.Fatalf("repeated policy preview decision: %v", err)
			}
			assertConfigImportPreviewReadOnly(t, probe)
			assertConfigImportPreviewNoProvisionalSQL(t, probe, decision)
			policyCounts := configImportDecisionCountsFor(&decision, configImportEntityPolicies)
			if policyCounts.Input != 2 || policyCounts.Created != 1 || policyCounts.Rejected != 0 {
				t.Fatalf("repeated policy preview counts=%+v", policyCounts)
			}
			if conflict == "skip" {
				if policyCounts.Updated != 0 || policyCounts.Skipped != 1 {
					t.Fatalf("repeated policy skip preview counts=%+v", policyCounts)
				}
				assertConfigImportPreviewItemAction(t, decision, configImportEntityPolicies, 0, configImportActionCreate)
				assertConfigImportPreviewItemAction(t, decision, configImportEntityPolicies, 1, configImportActionSkip)
			} else {
				if policyCounts.Updated != 1 || policyCounts.Skipped != 0 {
					t.Fatalf("repeated policy overwrite preview counts=%+v", policyCounts)
				}
				assertConfigImportPreviewItemAction(t, decision, configImportEntityPolicies, 0, configImportActionCreate)
				assertConfigImportPreviewItemAction(t, decision, configImportEntityPolicies, 1, configImportActionUpdate)
			}

			applyProbe := &configImportPreviewSQLProbe{}
			response, result := serveP1ConfigImport(t, db.Session(&gorm.Session{Logger: applyProbe}), nil, conflict, body)
			if response.Code != http.StatusOK {
				t.Fatalf("repeated policy %s apply status=%d body=%s", conflict, response.Code, response.Body.String())
			}
			wantPolicyClassCount := 2
			if conflict == "skip" {
				wantPolicyClassCount = 1
			}
			if result.Policies != wantPolicyClassCount {
				t.Fatalf("repeated policy %s class count=%+v", conflict, result)
			}
			if conflict == "skip" {
				if result.Created != 1 || result.Updated != 0 || result.Imported != 1 ||
					result.Skipped != 1 || result.Rejected != 0 {
					t.Fatalf("repeated policy skip apply result=%+v", result)
				}
			} else if result.Created != 1 || result.Updated != 1 || result.Imported != 2 ||
				result.Skipped != 0 || result.Rejected != 0 {
				t.Fatalf("repeated policy overwrite apply result=%+v", result)
			}
			assertConfigImportPreviewNoProvisionalSQL(t, applyProbe, decision)
			var persisted model.Policy
			if err := db.Where("name = ?", "preview-repeated-new-policy").First(&persisted).Error; err != nil {
				t.Fatalf("read repeated policy: %v", err)
			}
			if conflict == "skip" {
				if persisted.Description != "first policy" ||
					persisted.SourcePath != body["policies"].([]any)[0].(map[string]any)["source_path"] ||
					persisted.RetentionDays != 3 || persisted.MaxRetries != 1 || persisted.Enabled {
					t.Fatalf("repeated policy skip retained wrong fields: %+v", persisted)
				}
			} else if persisted.Description != "last policy" ||
				persisted.SourcePath != body["policies"].([]any)[1].(map[string]any)["source_path"] ||
				persisted.RetentionDays != 17 || persisted.MaxRetries != 9 || !persisted.Enabled {
				t.Fatalf("repeated policy overwrite did not retain last fields: %+v", persisted)
			}
		})
	}
}

func TestConfigImportPreviewCrossExecutorTargetConflictParitySQLite(t *testing.T) {
	runConfigImportPreviewCrossExecutorTargetConflictParity(t, "sqlite")
}

func TestConfigImportPreviewCrossExecutorTargetConflictParityPostgres(t *testing.T) {
	runConfigImportPreviewCrossExecutorTargetConflictParity(t, "postgres")
}

func runConfigImportPreviewCrossExecutorTargetConflictParity(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	for _, executor := range []string{"restic", "rclone"} {
		t.Run(executor, func(t *testing.T) {
			db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateP1ConfigImport(t, db)
			targetRoot := t.TempDir()
			conflictTarget := filepath.Join(targetRoot, "claimed")
			node := model.Node{
				Name: "preview-cross-executor-node-" + executor, Host: "10.41.0.1",
				Port: 22, Username: "root", AuthType: "key",
				BackupDir: "preview-cross-executor-" + executor,
			}
			if err := db.Create(&node).Error; err != nil {
				t.Fatalf("create cross-executor node: %v", err)
			}
			owner := model.Task{
				Name: "preview-cross-executor-owner-" + executor, NodeID: node.ID,
				ExecutorType: "rsync", RsyncSource: "/owner-source",
				RsyncTarget: conflictTarget, ExecutorConfig: canonicalLegacyRsyncImportConfig(),
				Status: "pending", Source: "manual",
			}
			if err := db.Create(&owner).Error; err != nil {
				t.Fatalf("create cross-executor owner: %v", err)
			}
			taskData := map[string]any{
				"name":      "preview-cross-executor-import-" + executor,
				"node_name": node.Name, "executor_type": executor,
				"rsync_source": "/import-source", "rsync_target": conflictTarget,
			}
			if executor == "rclone" {
				taskData["executor_config"] = `{"version":1,"publication_mode":"legacy_mutable"}`
			}
			body := map[string]any{"tasks": []any{taskData}}
			_, err := previewConfigImportDecisionWithError(context.Background(), t, db, "skip", body, nil)
			if err == nil || !errors.Is(err, errConfigImportTargetConflict) {
				t.Fatalf("%s preview target conflict error=%v, want target conflict", executor, err)
			}
			// A blocking target conflict intentionally returns no usable decision.
			preview := serveConfigImportPreviewHTTP(t, db, nil, "skip", mustMarshalConfigImportPreviewBody(t, body), nil)
			if preview.Code != http.StatusBadRequest {
				t.Fatalf("%s preview HTTP status=%d body=%s", executor, preview.Code, preview.Body.String())
			}
			response, _ := serveP1ConfigImport(t, db, nil, "skip", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s apply status=%d body=%s", executor, response.Code, response.Body.String())
			}
			if got := countConfigImportPreviewRows(t, db, &model.Task{}, "name = ?", taskData["name"]); got != 0 {
				t.Fatalf("%s cross-executor conflicting task persisted: %d", executor, got)
			}
			var ownerAfter model.Task
			if err := db.First(&ownerAfter, owner.ID).Error; err != nil {
				t.Fatalf("read cross-executor owner after rejection: %v", err)
			}
			if ownerAfter.RsyncTarget != owner.RsyncTarget || ownerAfter.RsyncSource != owner.RsyncSource {
				t.Fatalf("%s cross-executor conflict changed baseline owner: before=%+v after=%+v", executor, owner, ownerAfter)
			}
		})
	}
}

func mustMarshalConfigImportPreviewBody(t *testing.T, body any) []byte {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal config import preview body: %v", err)
	}
	return raw
}

func TestConfigImportPreviewExistingTaskTargetCompatibilitySQLite(t *testing.T) {
	runConfigImportPreviewExistingTaskTargetCompatibility(t, "sqlite")
}

func TestConfigImportPreviewExistingTaskTargetCompatibilityPostgres(t *testing.T) {
	runConfigImportPreviewExistingTaskTargetCompatibility(t, "postgres")
}

func runConfigImportPreviewExistingTaskTargetCompatibility(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	for _, scenario := range []string{"self_ancestor", "self_descendant", "unchanged_legacy_overlap"} {
		t.Run(scenario, func(t *testing.T) {
			db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateP1ConfigImport(t, db)
			root := t.TempDir()
			node := model.Node{
				Name: "preview-existing-target-node-" + scenario, Host: "10.42.0.1",
				Port: 22, Username: "root", AuthType: "key",
				BackupDir: "preview-existing-target-" + scenario,
			}
			if err := db.Create(&node).Error; err != nil {
				t.Fatalf("create existing-target node: %v", err)
			}
			currentTarget := filepath.Join(root, "owned", "current")
			if err := os.MkdirAll(currentTarget, 0o755); err != nil {
				t.Fatalf("create existing target: %v", err)
			}
			existing := model.Task{
				Name: "preview-existing-target-task-" + scenario, NodeID: node.ID,
				ExecutorType: "rsync", RsyncSource: "/old-source",
				RsyncTarget: currentTarget, ExecutorConfig: canonicalLegacyRsyncImportConfig(),
				Status: "pending", Source: "manual",
			}
			if err := db.Create(&existing).Error; err != nil {
				t.Fatalf("create existing target task: %v", err)
			}
			target := filepath.Join(root, "owned", "new-child")
			switch scenario {
			case "self_ancestor":
				target = root
			case "self_descendant":
				target = filepath.Join(currentTarget, "new-descendant")
			case "unchanged_legacy_overlap":
				legacy := model.Task{
					Name: "preview-existing-target-legacy-" + scenario, NodeID: node.ID,
					ExecutorType: "rsync", RsyncSource: "/legacy-source",
					RsyncTarget:    filepath.Join(currentTarget, "legacy-child"),
					ExecutorConfig: canonicalLegacyRsyncImportConfig(),
					Status:         "pending", Source: "manual",
				}
				if err := db.Create(&legacy).Error; err != nil {
					t.Fatalf("create legacy overlapping task: %v", err)
				}
				target = currentTarget
			}
			body := map[string]any{"tasks": []any{map[string]any{
				"name": existing.Name, "node_name": node.Name, "executor_type": "rsync",
				"rsync_source": "/new-source", "rsync_target": target,
				"executor_config": canonicalLegacyRsyncImportConfig(),
			}}}
			preview := serveConfigImportPreviewHTTP(t, db, nil, "overwrite", mustMarshalConfigImportPreviewBody(t, body), nil)
			if preview.Code != http.StatusOK {
				t.Fatalf("%s preview status=%d body=%s", scenario, preview.Code, preview.Body.String())
			}
			dto := decodeConfigImportPreviewHTTP(t, preview)
			taskEntity := previewHTTPEntity(t, dto, configImportEntityTasks)
			if taskEntity.Input != 1 || taskEntity.Updated != 1 || taskEntity.Created != 0 ||
				taskEntity.Rejected != 0 || taskEntity.PendingValidation != 0 {
				t.Fatalf("%s preview task entity=%+v", scenario, taskEntity)
			}
			response, result := serveP1ConfigImport(t, db, nil, "overwrite", body)
			if response.Code != http.StatusOK {
				t.Fatalf("%s apply status=%d body=%s", scenario, response.Code, response.Body.String())
			}
			if result.Tasks != 1 || result.Updated != 1 || result.Created != 0 ||
				result.Imported != 1 || result.Rejected != 0 {
				t.Fatalf("%s apply result=%+v", scenario, result)
			}
			var persisted model.Task
			if err := db.First(&persisted, existing.ID).Error; err != nil {
				t.Fatalf("read existing target task after %s: %v", scenario, err)
			}
			if persisted.RsyncTarget != target || persisted.RsyncSource != "/new-source" {
				t.Fatalf("%s existing target overwrite mismatch: %+v", scenario, persisted)
			}
		})
	}
}

func TestConfigImportPreviewIgnoredSuppliedNodeSecretsRemainMissingSQLite(t *testing.T) {
	runConfigImportPreviewIgnoredSuppliedNodeSecretsRemainMissing(t, "sqlite")
}

func TestConfigImportPreviewIgnoredSuppliedNodeSecretsRemainMissingPostgres(t *testing.T) {
	runConfigImportPreviewIgnoredSuppliedNodeSecretsRemainMissing(t, "postgres")
}

func runConfigImportPreviewIgnoredSuppliedNodeSecretsRemainMissing(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	for _, fixture := range []struct {
		name     string
		authType string
		field    string
		value    string
		warning  string
	}{
		{name: "password", authType: "password", field: "password", value: "SUPPLIED_PASSWORD_MUST_BE_IGNORED", warning: configImportWarningMissingPassword},
		{name: "inline_private_key", authType: "key", field: "private_key", value: p1ImportPrivateKey(t), warning: configImportWarningMissingInlineKey},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateP1ConfigImport(t, db)
			node := model.Node{
				Name: "preview-missing-secret-node-" + fixture.name, Host: "10.43.0.1",
				Port: 22, Username: "old-user", AuthType: fixture.authType,
				BackupDir: "preview-missing-secret-" + fixture.name,
			}
			if err := db.Create(&node).Error; err != nil {
				t.Fatalf("create missing-secret node: %v", err)
			}
			body := map[string]any{"nodes": []any{map[string]any{
				"name": node.Name, "host": node.Host, "port": node.Port,
				"username": "new-user", "auth_type": fixture.authType,
				fixture.field: fixture.value,
			}}}
			preview := serveConfigImportPreviewHTTP(t, db, nil, "overwrite", mustMarshalConfigImportPreviewBody(t, body), nil)
			if preview.Code != http.StatusOK {
				t.Fatalf("%s missing-secret preview status=%d body=%s", fixture.name, preview.Code, preview.Body.String())
			}
			dto := decodeConfigImportPreviewHTTP(t, preview)
			entity := previewHTTPEntity(t, dto, configImportEntityNodes)
			if entity.Input != 1 || entity.Updated != 1 || entity.Rejected != 0 ||
				entity.PendingValidation != 0 {
				t.Fatalf("%s missing-secret preview entity=%+v", fixture.name, entity)
			}
			foundWarning := false
			for _, warning := range dto.Warnings {
				if warning.Entity == configImportEntityNodes && warning.Index == 0 && warning.Code == fixture.warning {
					foundWarning = true
				}
			}
			if !foundWarning {
				t.Fatalf("%s preview missing warning %q: %+v", fixture.name, fixture.warning, dto.Warnings)
			}
			foundSecret := false
			for _, secret := range dto.Secrets {
				if secret.Entity == configImportEntityNodes && secret.Index == 0 && secret.Field == fixture.field {
					foundSecret = true
					if !secret.Present || secret.Action != "missing" {
						t.Fatalf("%s preview secret projection=%+v", fixture.name, secret)
					}
				}
			}
			if !foundSecret {
				t.Fatalf("%s preview omitted supplied ignored secret field %q: %+v", fixture.name, fixture.field, dto.Secrets)
			}

			response, result := serveP1ConfigImport(t, db, nil, "overwrite", body)
			if response.Code != http.StatusOK {
				t.Fatalf("%s missing-secret apply status=%d body=%s", fixture.name, response.Code, response.Body.String())
			}
			if result.Updated != 1 || result.Imported != 1 || result.Rejected != 0 ||
				!configImportPreviewResultHasWarning(result, configImportEntityNodes, 0, fixture.warning) {
				t.Fatalf("%s missing-secret apply result=%+v", fixture.name, result)
			}
			var raw struct {
				Username   string
				Password   string
				PrivateKey string
			}
			if err := db.Table("nodes").Select("username", "password", "private_key").Where("id = ?", node.ID).Scan(&raw).Error; err != nil {
				t.Fatalf("read missing-secret node raw fields: %v", err)
			}
			if raw.Username != "new-user" || raw.Password != "" || raw.PrivateKey != "" {
				t.Fatalf("%s ignored supplied secret was persisted: %+v", fixture.name, raw)
			}
		})
	}
}

func TestConfigImportPreviewPendingChecksReconciledSQLite(t *testing.T) {
	runConfigImportPreviewPendingChecksReconciled(t, "sqlite")
}

func TestConfigImportPreviewPendingChecksReconciledPostgres(t *testing.T) {
	runConfigImportPreviewPendingChecksReconciled(t, "postgres")
}

func runConfigImportPreviewPendingChecksReconciled(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	for _, scenario := range []string{"rejected_reference", "superseded_task"} {
		t.Run(scenario, func(t *testing.T) {
			db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
			migrateP1ConfigImport(t, db)
			var body map[string]any
			switch scenario {
			case "rejected_reference":
				node := model.Node{
					Name: "preview-pending-rejected-node", Host: "10.44.0.1",
					Port: 22, Username: "old-user", AuthType: "password",
					Password: "stored-password", BackupDir: "preview-pending-rejected-node",
				}
				if err := db.Create(&node).Error; err != nil {
					t.Fatalf("create pending-rejected node: %v", err)
				}
				body = map[string]any{"nodes": []any{map[string]any{
					"name": node.Name, "host": node.Host, "port": node.Port,
					"username": "new-user", "auth_type": "password",
					"password": "ignored-input", "ssh_key_name": "missing-pending-rejection-key",
				}}}
			case "superseded_task":
				node := model.Node{
					Name: "preview-pending-superseded-node", Host: "10.44.0.2",
					Port: 22, Username: "root", AuthType: "key",
					BackupDir: "preview-pending-superseded-node",
				}
				if err := db.Create(&node).Error; err != nil {
					t.Fatalf("create pending-superseded node: %v", err)
				}
				body = map[string]any{
					"policies": []any{map[string]any{
						"name":        "preview-pending-superseded-policy",
						"source_path": filepath.Join(t.TempDir(), "policy-source"),
						"target_path": filepath.Join(t.TempDir(), "policy-target"),
					}},
					"tasks": []any{
						map[string]any{
							"name": "preview-pending-superseded-task", "node_name": node.Name,
							"policy_name":   "preview-pending-superseded-policy",
							"executor_type": "rsync", "rsync_source": "/superseded-source",
							"executor_config": canonicalLegacyRsyncImportConfig(),
						},
						map[string]any{
							"name": "preview-pending-superseded-task", "node_name": node.Name,
							"policy_name":   "preview-pending-superseded-policy",
							"executor_type": "rsync", "rsync_source": "/superseded-source",
							"rsync_target":    filepath.Join(t.TempDir(), "explicit-target"),
							"executor_config": canonicalLegacyRsyncImportConfig(),
						},
					},
				}
			}
			decision, err := previewConfigImportDecisionWithError(context.Background(), t, db, "overwrite", body, nil)
			if err != nil {
				t.Fatalf("%s pending reconciliation decision: %v", scenario, err)
			}
			if scenario == "rejected_reference" {
				assertConfigImportPreviewItemAction(t, decision, configImportEntityNodes, 0, configImportActionReject)
				if configImportDecisionCountsFor(&decision, configImportEntityNodes).PendingValidation != 0 ||
					configImportDecisionCountsFor(&decision, configImportEntityNodes).Rejected != 1 {
					t.Fatalf("rejected-reference counts=%+v", configImportDecisionCountsFor(&decision, configImportEntityNodes))
				}
			} else {
				assertConfigImportPreviewItemAction(t, decision, configImportEntityTasks, 0, configImportActionSkip)
				assertConfigImportPreviewItemAction(t, decision, configImportEntityTasks, 1, configImportActionCreate)
				taskCounts := configImportDecisionCountsFor(&decision, configImportEntityTasks)
				if taskCounts.PendingValidation != 0 || taskCounts.Created != 1 || taskCounts.Skipped != 1 {
					t.Fatalf("superseded-task counts=%+v", taskCounts)
				}
			}
			preview := serveConfigImportPreviewHTTP(t, db, nil, "overwrite", mustMarshalConfigImportPreviewBody(t, body), nil)
			if preview.Code != http.StatusOK {
				t.Fatalf("%s pending reconciliation preview status=%d body=%s", scenario, preview.Code, preview.Body.String())
			}
			dto := decodeConfigImportPreviewHTTP(t, preview)
			if len(dto.PendingChecks) != 0 || dto.PendingChecksTruncated != 0 {
				t.Fatalf("%s wire pending checks=%+v truncated=%d", scenario, dto.PendingChecks, dto.PendingChecksTruncated)
			}
		})
	}
}

type configImportCancelDuringEvaluationContext struct {
	parent      context.Context
	cancelAfter int
	checks      int
	cancelled   bool
	done        chan struct{}
}

func (c *configImportCancelDuringEvaluationContext) Deadline() (time.Time, bool) {
	return c.parent.Deadline()
}

func (c *configImportCancelDuringEvaluationContext) Done() <-chan struct{} {
	return c.done
}

func (c *configImportCancelDuringEvaluationContext) Err() error {
	c.checks++
	if c.checks > c.cancelAfter {
		if !c.cancelled {
			c.cancelled = true
			close(c.done)
		}
		return context.Canceled
	}
	return c.parent.Err()
}

func (c *configImportCancelDuringEvaluationContext) Value(key any) any {
	return c.parent.Value(key)
}

func TestConfigImportPreviewCancellationDuringEvaluationSQLite(t *testing.T) {
	runConfigImportPreviewCancellationDuringEvaluation(t, "sqlite")
}

func TestConfigImportPreviewCancellationDuringEvaluationPostgres(t *testing.T) {
	runConfigImportPreviewCancellationDuringEvaluation(t, "postgres")
}

func runConfigImportPreviewCancellationDuringEvaluation(t *testing.T, engine string) {
	setConfigHandlerTestEncryption(t)
	db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
	migrateP1ConfigImport(t, db)
	nodes := make([]any, 96)
	for index := range nodes {
		nodes[index] = map[string]any{
			"name": fmt.Sprintf("preview-cancel-node-%03d", index),
			"host": fmt.Sprintf("10.45.0.%d", (index%200)+1),
			"port": 22, "username": "root", "auth_type": "key",
		}
	}
	body := map[string]any{"nodes": nodes}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal cancellation body: %v", err)
	}
	input, err := prepareConfigImportInput(context.Background(), NewConfigHandler(db, nil), payload, "skip")
	if err != nil {
		t.Fatalf("prepare cancellation input: %v", err)
	}
	snapshot, err := loadConfigImportSnapshot(context.Background(), db, input)
	if err != nil {
		t.Fatalf("load cancellation snapshot: %v", err)
	}
	before := previewHTTPCoreCounts(t, db)
	ctx := &configImportCancelDuringEvaluationContext{parent: context.Background(), cancelAfter: 8, done: make(chan struct{})}
	_, err = evaluateConfigImport(ctx, input, snapshot)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-evaluation cancellation error=%v, want context.Canceled", err)
	}
	if !ctx.cancelled || ctx.checks <= ctx.cancelAfter {
		t.Fatalf("cancellation context did not trip during evaluation: checks=%d threshold=%d cancelled=%v", ctx.checks, ctx.cancelAfter, ctx.cancelled)
	}
	after := previewHTTPCoreCounts(t, db)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("mid-evaluation cancellation changed database: before=%v after=%v", before, after)
	}
}
