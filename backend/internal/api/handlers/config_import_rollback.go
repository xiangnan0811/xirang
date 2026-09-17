package handlers

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/model"
	policyPkg "xirang/backend/internal/policy"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type configImportRollbackJournal struct {
	mu          sync.Mutex
	db          *gorm.DB
	settingsSvc *settings.Service
	snapshot    *configImportRollbackSnapshot
	restored    bool
}

type configImportRollbackSnapshot struct {
	sshKeys     configImportTableSnapshot
	nodes       configImportTableSnapshot
	policies    configImportTableSnapshot
	tasks       configImportTableSnapshot
	settings    configImportTableSnapshot
	graph       configImportGraphRollback
	settingKeys []string
}

type configImportTableSnapshot struct {
	table      string
	primaryKey string
	where      string
	args       []any
	prior      map[string]map[string]any
	current    map[string]map[string]any
	created    []any
}

type configImportGraphRollback struct {
	refs                configImportTableSnapshot
	bindings            configImportTableSnapshot
	createdRepositories []any
	createdLinks        []any
	createdPolicies     []any
}

func newConfigImportRollbackJournal(db *gorm.DB, settingsSvc *settings.Service) *configImportRollbackJournal {
	return &configImportRollbackJournal{db: db, settingsSvc: settingsSvc}
}

func captureConfigImportRollbackSnapshot(
	ctx context.Context,
	tx *gorm.DB,
	data configImportData,
	settingsPlan []configImportSetting,
	envelope configImportEnvelope,
) (*configImportRollbackSnapshot, error) {
	if tx == nil {
		return nil, fmt.Errorf("config import rollback snapshot transaction is unavailable")
	}
	snapshot := &configImportRollbackSnapshot{
		sshKeys:  newConfigImportTableSnapshot("ssh_keys", "id", "name IN ?", importRecordNames(data.SSHKeys)),
		nodes:    newConfigImportTableSnapshot("nodes", "id", "name IN ?", importRecordNames(data.Nodes)),
		policies: newConfigImportTableSnapshot("policies", "id", "name IN ?", importRecordNames(data.Policies)),
		tasks:    newConfigImportTableSnapshot("tasks", "id", "name IN ?", importRecordNames(data.Tasks)),
	}
	for _, setting := range settingsPlan {
		snapshot.settingKeys = append(snapshot.settingKeys, setting.key)
	}
	snapshot.settingKeys = uniqueSortedConfigImportStrings(snapshot.settingKeys)
	snapshot.settings = newConfigImportTableSnapshot("system_settings", "key", "key IN ?", snapshot.settingKeys)
	for _, table := range []*configImportTableSnapshot{
		&snapshot.sshKeys, &snapshot.nodes, &snapshot.policies, &snapshot.tasks, &snapshot.settings,
	} {
		if err := table.capture(ctx, tx); err != nil {
			return nil, err
		}
	}
	if envelope.Version == configExportVersion2 {
		refs := configImportGraphRefs(envelope.Graph)
		snapshot.graph.refs = newConfigImportTableSnapshot(
			"backup_asset_config_import_refs", "id",
			"source_document_id = ? AND source_reference IN ?", envelope.DocumentID, refs,
		)
		if err := snapshot.graph.refs.capture(ctx, tx); err != nil {
			return nil, err
		}
		repositoryIDs := snapshot.graph.refs.localEntityIDs(configAssetEntityRepository)
		snapshot.graph.bindings = newConfigImportTableSnapshot(
			"repository_access_bindings", "id", "repository_id IN ?", repositoryIDs,
		)
		if err := snapshot.graph.bindings.capture(ctx, tx); err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func (snapshot *configImportRollbackSnapshot) seal(ctx context.Context, tx *gorm.DB) error {
	if snapshot == nil || tx == nil {
		return fmt.Errorf("config import rollback snapshot is unavailable")
	}
	for _, table := range []*configImportTableSnapshot{
		&snapshot.sshKeys, &snapshot.nodes, &snapshot.policies, &snapshot.tasks, &snapshot.settings,
	} {
		if err := table.seal(ctx, tx); err != nil {
			return err
		}
	}
	if snapshot.graph.refs.table == "" {
		return nil
	}
	if err := snapshot.graph.refs.seal(ctx, tx); err != nil {
		return err
	}
	for _, row := range snapshot.graph.refs.createdRows() {
		switch rawConfigImportString(row["entity_kind"]) {
		case configAssetEntityRepository:
			snapshot.graph.createdRepositories = append(snapshot.graph.createdRepositories, row["local_entity_id"])
		case configAssetEntityTaskLink:
			snapshot.graph.createdLinks = append(snapshot.graph.createdLinks, row["local_entity_id"])
		case configAssetEntityRetentionPolicy:
			snapshot.graph.createdPolicies = append(snapshot.graph.createdPolicies, row["local_entity_id"])
		}
	}
	repositoryIDs := snapshot.graph.refs.localEntityIDs(configAssetEntityRepository)
	snapshot.graph.bindings.where = "repository_id IN ?"
	snapshot.graph.bindings.args = []any{repositoryIDs}
	return snapshot.graph.bindings.seal(ctx, tx)
}

func (journal *configImportRollbackJournal) install(snapshot *configImportRollbackSnapshot) {
	journal.mu.Lock()
	journal.snapshot = snapshot
	journal.restored = false
	journal.mu.Unlock()
}

func (journal *configImportRollbackJournal) Restore(ctx context.Context) error {
	if journal == nil || journal.db == nil {
		return fmt.Errorf("config import rollback journal is unavailable")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.restored || journal.snapshot == nil {
		return nil
	}
	snapshot := journal.snapshot
	if err := journal.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if snapshot.hasTaskOwnershipRows() {
			if err := policyPkg.LockTargetOwnershipSpace(tx); err != nil {
				return fmt.Errorf("config import rollback target ownership lock: %w", err)
			}
			if err := snapshot.lockTaskRollbackPolicies(ctx, tx); err != nil {
				return err
			}
			if err := snapshot.validateTaskRollbackOwnership(ctx, tx); err != nil {
				return err
			}
		}
		tables := []*configImportTableSnapshot{
			&snapshot.settings, &snapshot.tasks, &snapshot.policies, &snapshot.nodes, &snapshot.sshKeys,
		}
		for _, table := range tables {
			if err := table.verifyCurrent(ctx, tx); err != nil {
				return err
			}
		}
		if err := snapshot.graph.restore(ctx, tx); err != nil {
			return err
		}
		for _, table := range tables {
			if err := table.restore(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	journal.restored = true
	if journal.settingsSvc != nil {
		journal.settingsSvc.InvalidateCachedValues(snapshot.settingKeys)
	}
	return nil
}

func (snapshot *configImportRollbackSnapshot) hasTaskOwnershipRows() bool {
	if snapshot == nil {
		return false
	}
	return len(snapshot.tasks.prior) > 0 || len(snapshot.tasks.current) > 0 || len(snapshot.tasks.created) > 0
}

func (snapshot *configImportRollbackSnapshot) lockTaskRollbackPolicies(ctx context.Context, tx *gorm.DB) error {
	policyIDs := make(map[uint]struct{})
	addPolicyIDs := func(rows map[string]map[string]any, identityKey string) error {
		for _, row := range rows {
			rawID, exists := row[identityKey]
			if !exists || rawID == nil {
				continue
			}
			policyID, ok := configImportRollbackUint(rawID)
			if !ok {
				return fmt.Errorf("config import rollback policy identity is unavailable")
			}
			policyIDs[policyID] = struct{}{}
		}
		return nil
	}
	if err := addPolicyIDs(snapshot.policies.current, "id"); err != nil {
		return err
	}
	if err := addPolicyIDs(snapshot.policies.prior, "id"); err != nil {
		return err
	}
	if err := addPolicyIDs(snapshot.tasks.current, "policy_id"); err != nil {
		return err
	}
	if err := addPolicyIDs(snapshot.tasks.prior, "policy_id"); err != nil {
		return err
	}
	if len(policyIDs) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(policyIDs))
	for policyID := range policyIDs {
		ids = append(ids, policyID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var policies []model.Policy
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).
		Order("id").
		Find(&policies).Error; err != nil {
		return fmt.Errorf("config import rollback policy lock failed: %w", err)
	}
	if len(policies) != len(ids) {
		return fmt.Errorf("config import rollback policy rows changed during restore")
	}
	return nil
}

func newConfigImportTableSnapshot(table, primaryKey, where string, args ...any) configImportTableSnapshot {
	return configImportTableSnapshot{
		table: table, primaryKey: primaryKey, where: where, args: args,
		prior:   make(map[string]map[string]any),
		current: make(map[string]map[string]any),
	}
}

func (snapshot *configImportTableSnapshot) capture(ctx context.Context, tx *gorm.DB) error {
	rows, err := snapshot.load(ctx, tx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		key := rawConfigImportKey(row[snapshot.primaryKey])
		if key == "" {
			return fmt.Errorf("config import rollback row identity is unavailable")
		}
		snapshot.prior[key] = row
	}
	return nil
}

func (snapshot *configImportTableSnapshot) seal(ctx context.Context, tx *gorm.DB) error {
	rows, err := snapshot.load(ctx, tx)
	if err != nil {
		return err
	}
	snapshot.created = snapshot.created[:0]
	snapshot.current = make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		key := rawConfigImportKey(row[snapshot.primaryKey])
		snapshot.current[key] = row
		if _, exists := snapshot.prior[key]; !exists {
			snapshot.created = append(snapshot.created, row[snapshot.primaryKey])
		}
	}
	return nil
}

func (snapshot *configImportTableSnapshot) load(ctx context.Context, tx *gorm.DB) ([]map[string]any, error) {
	if snapshot.table == "" || len(snapshot.args) == 0 || configImportEmptyTarget(snapshot.args) {
		return nil, nil
	}
	rows := make([]map[string]any, 0)
	if err := tx.WithContext(ctx).Table(snapshot.table).Where(snapshot.where, snapshot.args...).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (snapshot *configImportTableSnapshot) verifyCurrent(ctx context.Context, tx *gorm.DB) error {
	if snapshot == nil || snapshot.table == "" {
		return nil
	}
	rows, err := snapshot.load(ctx, tx)
	if err != nil {
		return err
	}
	actual := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		key := rawConfigImportKey(row[snapshot.primaryKey])
		if key == "" {
			return fmt.Errorf("config import rollback row identity is unavailable")
		}
		actual[key] = row
	}
	if len(actual) != len(snapshot.current) {
		return fmt.Errorf("config import rollback detected concurrent changes in %s", snapshot.table)
	}
	for key, expected := range snapshot.current {
		row, ok := actual[key]
		if !ok || !configImportRowsEqual(expected, row) {
			return fmt.Errorf("config import rollback detected concurrent changes in %s", snapshot.table)
		}
	}
	return nil
}

func configImportRowsEqual(expected, actual map[string]any) bool {
	if len(expected) != len(actual) {
		return false
	}
	for key, expectedValue := range expected {
		actualValue, ok := actual[key]
		if !ok || !configImportValuesEqual(expectedValue, actualValue) {
			return false
		}
	}
	return true
}

func configImportValuesEqual(expected, actual any) bool {
	switch expectedValue := expected.(type) {
	case []byte:
		actualValue, ok := actual.([]byte)
		return ok && reflect.DeepEqual(expectedValue, actualValue)
	case string:
		if actualValue, ok := actual.([]byte); ok {
			return expectedValue == string(actualValue)
		}
	}
	if expectedTime, ok := expected.(time.Time); ok {
		actualTime, ok := actual.(time.Time)
		return ok && expectedTime.Equal(actualTime)
	}
	return reflect.DeepEqual(expected, actual)
}

func configImportCASPredicate(db *gorm.DB, primaryKey string, row map[string]any) (string, []any, error) {
	primaryValue, ok := row[primaryKey]
	if !ok {
		return "", nil, fmt.Errorf("config import rollback row identity is unavailable")
	}
	keys := make([]string, 0, len(row))
	for key := range row {
		if key != primaryKey {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	predicate := primaryKey + " = ?"
	args := []any{primaryValue}
	for _, key := range keys {
		if row[key] == nil {
			predicate += " AND " + key + " IS NULL"
			continue
		}
		if timestamp, isTimestamp := row[key].(time.Time); isTimestamp && db != nil && db.Name() == "sqlite" {
			predicate += " AND (CAST(" + key + " AS TEXT) = ? OR CAST(" + key + " AS TEXT) = ? OR CAST(" + key + " AS TEXT) = ? OR CAST(" + key + " AS TEXT) = ?)"
			args = append(args, configImportSQLiteTimestampValues(timestamp)...)
			continue
		}
		predicate += " AND " + key + " = ?"
		args = append(args, configImportCASValue(db, row[key]))
	}
	return predicate, args, nil

}

func configImportCASValue(db *gorm.DB, value any) any {
	if timestamp, ok := value.(time.Time); ok {
		if db != nil && db.Name() == "sqlite" {
			return timestamp.UTC().Format(time.RFC3339Nano)
		}
		return timestamp
	}
	return value
}

func configImportSQLiteTimestampValues(timestamp time.Time) []any {
	utc := timestamp.UTC()
	local := timestamp.Local()
	const sqliteLayout = "2006-01-02 15:04:05.999999999-07:00"
	return []any{
		utc.Format(time.RFC3339Nano),
		utc.Format(sqliteLayout),
		local.Format(time.RFC3339Nano),
		local.Format(sqliteLayout),
	}

}

func (snapshot *configImportTableSnapshot) restore(ctx context.Context, tx *gorm.DB) error {
	if snapshot == nil || snapshot.table == "" {
		return nil
	}
	for _, value := range snapshot.created {
		key := rawConfigImportKey(value)
		row, exists := snapshot.current[key]
		if !exists {
			return fmt.Errorf("config import rollback created row %s in %s is unavailable", key, snapshot.table)
		}
		predicate, args, err := configImportCASPredicate(tx, snapshot.primaryKey, row)
		if err != nil {
			return err
		}
		result := tx.WithContext(ctx).Exec(
			"DELETE FROM "+snapshot.table+" WHERE "+predicate,
			args...,
		)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("config import rollback created row %s in %s changed during restore", key, snapshot.table)
		}
	}
	keys := make([]string, 0, len(snapshot.prior))
	for key := range snapshot.prior {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		row := snapshot.prior[key]
		if expectedCurrent, ok := snapshot.current[key]; ok {
			predicate, args, err := configImportCASPredicate(tx, snapshot.primaryKey, expectedCurrent)
			if err != nil {
				return err
			}
			result := tx.WithContext(ctx).Table(snapshot.table).
				Where(predicate, args...).Updates(row)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("config import rollback prior row %s in %s changed during restore", key, snapshot.table)
			}
			continue
		}
		if err := tx.WithContext(ctx).Table(snapshot.table).Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}

func (snapshot *configImportRollbackSnapshot) validateTaskRollbackOwnership(ctx context.Context, tx *gorm.DB) error {
	var tasks []model.Task
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "node_id", "policy_id", "executor_type", "rsync_target").
		Order("id").
		Find(&tasks).Error; err != nil {
		return fmt.Errorf("config import rollback task ownership query failed: %w", err)
	}

	projected := make(map[uint]policyPkg.TargetOwner, len(tasks))
	for _, task := range tasks {
		if !policyPkg.IsCoreLocalTarget(task.ExecutorType, task.RsyncTarget) {
			continue
		}
		projected[task.ID] = policyPkg.TargetOwner{
			NodeID: task.NodeID,
			TaskID: task.ID,
			Target: task.RsyncTarget,
		}
		if task.PolicyID != nil {
			projected[task.ID] = policyPkg.TargetOwner{
				PolicyID: *task.PolicyID,
				NodeID:   task.NodeID,
				TaskID:   task.ID,
				Target:   task.RsyncTarget,
			}
		}
	}

	for _, row := range snapshot.tasks.current {
		taskID, ok := configImportRollbackUint(row["id"])
		if !ok {
			return fmt.Errorf("config import rollback task identity is unavailable")
		}
		delete(projected, taskID)
	}
	for key, row := range snapshot.tasks.prior {
		taskID, ok := configImportRollbackUint(row["id"])
		if !ok || rawConfigImportKey(row["id"]) != key {
			return fmt.Errorf("config import rollback task identity is unavailable")
		}
		delete(projected, taskID)
		owner, local, err := configImportRollbackTaskOwner(row)
		if err != nil {
			return err
		}
		if local {
			projected[taskID] = owner
		}
	}

	ids := make([]uint, 0, len(projected))
	for taskID := range projected {
		ids = append(ids, taskID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	owners := make([]policyPkg.TargetOwner, 0, len(ids))
	for _, taskID := range ids {
		owners = append(owners, projected[taskID])
	}
	if err := policyPkg.ValidateTargetOwners(owners); err != nil {
		return fmt.Errorf("config import rollback task ownership conflict: %w", err)
	}
	return nil
}

func configImportRollbackTaskOwner(row map[string]any) (policyPkg.TargetOwner, bool, error) {
	taskID, ok := configImportRollbackUint(row["id"])
	if !ok {
		return policyPkg.TargetOwner{}, false, fmt.Errorf("config import rollback task identity is unavailable")
	}
	executorType := rawConfigImportString(row["executor_type"])
	target := rawConfigImportString(row["rsync_target"])
	if !policyPkg.IsCoreLocalTarget(executorType, target) {
		return policyPkg.TargetOwner{}, false, nil
	}
	owner := policyPkg.TargetOwner{
		TaskID: taskID,
		NodeID: 0,
		Target: target,
	}
	if nodeID, ok := configImportRollbackUint(row["node_id"]); ok {
		owner.NodeID = nodeID
	}
	if policyID, ok := configImportRollbackUint(row["policy_id"]); ok {
		owner.PolicyID = policyID
	}
	return owner, true, nil
}

func configImportRollbackUint(value any) (uint, bool) {
	var parsed uint64
	switch typed := value.(type) {
	case uint:
		if typed == 0 {
			return 0, false
		}
		return typed, true
	case uint8:
		parsed = uint64(typed)
	case uint16:
		parsed = uint64(typed)
	case uint32:
		parsed = uint64(typed)
	case uint64:
		parsed = typed
	case int:
		if typed <= 0 {
			return 0, false
		}
		parsed = uint64(typed)
	case int8:
		if typed <= 0 {
			return 0, false
		}
		parsed = uint64(typed)
	case int16:
		if typed <= 0 {
			return 0, false
		}
		parsed = uint64(typed)
	case int32:
		if typed <= 0 {
			return 0, false
		}
		parsed = uint64(typed)
	case int64:
		if typed <= 0 {
			return 0, false
		}
		parsed = uint64(typed)
	default:
		return normalizeUintValue(value)
	}
	if parsed == 0 || parsed > uint64(^uint(0)) {
		return 0, false
	}
	return uint(parsed), true
}

func (snapshot *configImportTableSnapshot) createdRows() []map[string]any {
	rows := make([]map[string]any, 0, len(snapshot.created))
	for _, value := range snapshot.created {
		if row, exists := snapshot.current[rawConfigImportKey(value)]; exists {
			rows = append(rows, row)
		}
	}
	return rows
}

func (snapshot *configImportTableSnapshot) localEntityIDs(kind string) []string {
	ids := make([]string, 0)
	rows := snapshot.current
	if len(rows) == 0 {
		rows = snapshot.prior
	}
	for _, row := range rows {
		if rawConfigImportString(row["entity_kind"]) == kind {
			ids = append(ids, rawConfigImportString(row["local_entity_id"]))
		}
	}
	return uniqueSortedConfigImportStrings(ids)
}

func (graph *configImportGraphRollback) restore(ctx context.Context, tx *gorm.DB) error {
	for _, deletion := range []struct {
		table string
		ids   []any
	}{
		{"backup_retention_policies", graph.createdPolicies},
		{"task_repository_links", graph.createdLinks},
		{"backup_repositories", graph.createdRepositories},
	} {
		if len(deletion.ids) > 0 {
			if err := tx.WithContext(ctx).Exec("DELETE FROM "+deletion.table+" WHERE id IN ?", deletion.ids).Error; err != nil {
				return err
			}
		}
	}
	if err := graph.bindings.restore(ctx, tx); err != nil {
		return err
	}
	return graph.refs.restore(ctx, tx)
}

func importRecordNames(records []map[string]any) []string {
	names := make([]string, 0, len(records))
	for _, record := range records {
		if name, ok := record["name"].(string); ok && strings.TrimSpace(name) != "" {
			names = append(names, strings.TrimSpace(name))
		}
	}
	return uniqueSortedConfigImportStrings(names)
}

func configImportGraphRefs(graph configAssetGraph) []string {
	refs := make([]string, 0, len(graph.BackupRepositories)+len(graph.TaskRepositoryLinks)+len(graph.BackupRetentionPolicies))
	for _, repository := range graph.BackupRepositories {
		refs = append(refs, repository.RepositoryRef)
	}
	for _, link := range graph.TaskRepositoryLinks {
		refs = append(refs, link.LinkRef)
	}
	for _, policy := range graph.BackupRetentionPolicies {
		refs = append(refs, policy.PolicyRef)
	}
	return uniqueSortedConfigImportStrings(refs)
}

func uniqueSortedConfigImportStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func configImportEmptyTarget(args []any) bool {
	for _, arg := range args {
		switch value := arg.(type) {
		case []string:
			if len(value) == 0 {
				return true
			}
		}
	}
	return false
}

func rawConfigImportKey(value any) string {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(value)
	}
}

func rawConfigImportString(value any) string {
	return rawConfigImportKey(value)
}
