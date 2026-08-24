package handlers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"xirang/backend/internal/settings"

	"gorm.io/gorm"
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
		if err := snapshot.graph.restore(ctx, tx); err != nil {
			return err
		}
		for _, table := range []*configImportTableSnapshot{
			&snapshot.settings, &snapshot.tasks, &snapshot.policies, &snapshot.nodes, &snapshot.sshKeys,
		} {
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

func (snapshot *configImportTableSnapshot) restore(ctx context.Context, tx *gorm.DB) error {
	if snapshot.table == "" {
		return nil
	}
	if len(snapshot.created) > 0 {
		statement := "DELETE FROM " + snapshot.table + " WHERE " + snapshot.primaryKey + " IN ?"
		if err := tx.WithContext(ctx).Exec(statement, snapshot.created).Error; err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(snapshot.prior))
	for key := range snapshot.prior {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		row := snapshot.prior[key]
		result := tx.WithContext(ctx).Table(snapshot.table).
			Where(snapshot.primaryKey+" = ?", row[snapshot.primaryKey]).Updates(row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			if err := tx.WithContext(ctx).Table(snapshot.table).Create(row).Error; err != nil {
				return err
			}
		}
	}
	return nil
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
		{"repository_access_bindings", graph.bindings.created},
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
