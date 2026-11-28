package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// configImportGraphRollback contains only rows actually created by this import.
// Reused rows are protected while importing but are never claimed by the journal.
type configImportGraphRollback struct {
	repositories  configImportTableSnapshot
	links         configImportTableSnapshot
	policies      configImportTableSnapshot
	bindings      configImportTableSnapshot
	refs          configImportTableSnapshot
	repositoryIDs []any
	linkIDs       []any
	policyIDs     []any
	bindingIDs    []any
	refIDs        []any
}

func (graph *configImportGraphRollback) recordRepository(id string) {
	if graph != nil {
		graph.repositoryIDs = append(graph.repositoryIDs, id)
	}
}
func (graph *configImportGraphRollback) recordLink(id string) {
	if graph != nil {
		graph.linkIDs = append(graph.linkIDs, id)
	}
}
func (graph *configImportGraphRollback) recordPolicy(id string) {
	if graph != nil {
		graph.policyIDs = append(graph.policyIDs, id)
	}
}
func (graph *configImportGraphRollback) recordBinding(id string) {
	if graph != nil {
		graph.bindingIDs = append(graph.bindingIDs, id)
	}
}
func (graph *configImportGraphRollback) recordRef(id string) {
	if graph != nil {
		graph.refIDs = append(graph.refIDs, id)
	}
}

func (graph *configImportGraphRollback) createdLinkIDs() []any {
	if graph == nil {
		return nil
	}
	return graph.linkIDs
}

func (graph *configImportGraphRollback) seal(ctx context.Context, tx *gorm.DB) error {
	if graph == nil || tx == nil {
		return fmt.Errorf("config import asset rollback is unavailable")
	}
	for _, entry := range []struct {
		snapshot *configImportTableSnapshot
		table    string
		ids      []any
	}{
		{&graph.repositories, "backup_repositories", graph.repositoryIDs},
		{&graph.links, "task_repository_links", graph.linkIDs},
		{&graph.policies, "backup_retention_policies", graph.policyIDs},
		{&graph.bindings, "repository_access_bindings", graph.bindingIDs},
		{&graph.refs, "backup_asset_config_import_refs", graph.refIDs},
	} {
		*entry.snapshot = newConfigImportTableSnapshot(entry.table, "id", "id IN ?", entry.ids)
		if err := entry.snapshot.sealIDs(ctx, tx, entry.ids); err != nil {
			return err
		}
	}
	return nil
}

func (graph *configImportGraphRollback) verifyCurrent(ctx context.Context, tx *gorm.DB) error {
	if graph == nil {
		return nil
	}
	// Parents first: participating writers lock and revalidate these rows before
	// creating references. NOWAIT on the remaining rows avoids inverse-order waits.
	for _, snapshot := range []*configImportTableSnapshot{&graph.repositories, &graph.links, &graph.policies, &graph.bindings, &graph.refs} {
		if err := snapshot.verifyCurrent(ctx, tx); err != nil {
			return err
		}
	}
	return nil
}

func (graph *configImportGraphRollback) validateReferences(ctx context.Context, tx *gorm.DB) error {
	if graph == nil {
		return nil
	}
	for _, entity := range []struct {
		kind string
		ids  []any
	}{
		{configAssetEntityRepository, graph.repositoryIDs},
		{configAssetEntityTaskLink, graph.linkIDs},
		{configAssetEntityRetentionPolicy, graph.policyIDs},
	} {
		if len(entity.ids) == 0 {
			continue
		}
		query := tx.WithContext(ctx).Table("backup_asset_config_import_refs").Where("entity_kind = ? AND local_entity_id IN ?", entity.kind, entity.ids)
		if len(graph.refIDs) != 0 {
			query = query.Where("id NOT IN ?", graph.refIDs)
		}
		if err := rejectConfigImportAssetReferences(query, "import mapping"); err != nil {
			return err
		}
	}
	if len(graph.repositoryIDs) != 0 {
		for _, reference := range []struct {
			table, column string
			owned         []any
		}{
			{"repository_access_bindings", "repository_id", graph.bindingIDs},
			{"task_repository_links", "repository_id", graph.linkIDs},
			{"recovery_points", "repository_id", nil},
			{"backup_asset_managed_history_latches", "repository_id", nil},
		} {
			query := tx.WithContext(ctx).Table(reference.table).Where(reference.column+" IN ?", graph.repositoryIDs)
			if len(reference.owned) != 0 {
				query = query.Where("id NOT IN ?", reference.owned)
			}
			if err := rejectConfigImportAssetReferences(query, reference.table); err != nil {
				return err
			}
		}
	}
	for _, scope := range []struct {
		kind string
		ids  []any
	}{{"repository", graph.repositoryIDs}, {"task_link", graph.linkIDs}} {
		if len(scope.ids) == 0 {
			continue
		}
		query := tx.WithContext(ctx).Table("backup_retention_policies").Where("scope_kind = ? AND scope_id IN ?", scope.kind, scope.ids)
		if len(graph.policyIDs) != 0 {
			query = query.Where("id NOT IN ?", graph.policyIDs)
		}
		if err := rejectConfigImportAssetReferences(query, "retention policy scope"); err != nil {
			return err
		}
	}
	// Lineage is persisted as JSON text without a link foreign key. IDs are
	// opaque hex strings, so matching their quoted representation is portable
	// across SQLite and PostgreSQL and conservatively preserves history.
	for _, id := range graph.linkIDs {
		query := tx.WithContext(ctx).Table("recovery_points").Where("lineage_json LIKE ?", "%\""+rawConfigImportString(id)+"\"%")
		if err := rejectConfigImportAssetReferences(query, "recovery point lineage"); err != nil {
			return err
		}
	}
	return nil
}

func rejectConfigImportAssetReferences(query *gorm.DB, description string) error {
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("config import rollback detected foreign %s", description)
	}
	return nil
}

func (graph *configImportGraphRollback) restore(ctx context.Context, tx *gorm.DB) error {
	if graph == nil {
		return nil
	}
	for _, snapshot := range []*configImportTableSnapshot{&graph.refs, &graph.policies, &graph.bindings, &graph.links, &graph.repositories} {
		if err := snapshot.restore(ctx, tx); err != nil {
			return err
		}
	}
	return nil
}

func lockConfigImportAssetQuery(tx *gorm.DB) *gorm.DB {
	if tx.Name() == "postgres" {
		return tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"})
	}
	return tx
}

func lockConfigImportAssetParent(tx *gorm.DB, table, id string) error {
	var row map[string]any
	result := lockConfigImportAssetQuery(tx).Table(table).Select("id").Where("id = ?", id).Take(&row)
	if result.Error != nil {
		return fmt.Errorf("%w: asset parent %s is unavailable: %v", errConfigAssetGraphConflict, table, result.Error)
	}
	return nil
}

// validateProjectedAssetLinks checks only active links whose task or node is
// restored. Receipt-owned links disappear before the classic rows are restored.
func (snapshot *configImportRollbackSnapshot) validateProjectedAssetLinks(ctx context.Context, tx *gorm.DB) error {
	taskIDs, nodeIDs, err := snapshot.configImportAssetRollbackScope(ctx, tx)
	if err != nil {
		return err
	}
	if len(taskIDs) == 0 && len(nodeIDs) == 0 {
		return nil
	}
	query := tx.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&model.Task{}).
		Select("id", "node_id", "name", "executor_type", "rsync_target", "archived_at").Order("id")
	switch {
	case len(taskIDs) > 0 && len(nodeIDs) > 0:
		query = query.Where("id IN ? OR node_id IN ?", taskIDs, nodeIDs)
	case len(taskIDs) > 0:
		query = query.Where("id IN ?", taskIDs)
	default:
		query = query.Where("node_id IN ?", nodeIDs)
	}
	var tasks []model.Task
	if err := lockConfigImportAssetQuery(query).Find(&tasks).Error; err != nil {
		return fmt.Errorf("config import rollback asset task lock: %w", err)
	}
	projected := make(map[uint]model.Task, len(tasks))
	ids := make([]uint, 0, len(tasks))
	for _, task := range tasks {
		if row, ok := snapshot.tasks.prior[rawConfigImportKey(task.ID)]; ok {
			nodeID, valid := configImportRollbackUint(row["node_id"])
			if !valid {
				return fmt.Errorf("config import rollback asset task node is invalid")
			}
			task.NodeID = nodeID
			task.Name = rawConfigImportString(row["name"])
			task.ExecutorType = rawConfigImportString(row["executor_type"])
			task.RsyncTarget = rawConfigImportString(row["rsync_target"])
			if row["archived_at"] == nil {
				task.ArchivedAt = nil
			} else {
				stamp := time.Time{}
				task.ArchivedAt = &stamp
			}
		}
		projected[task.ID] = task
		ids = append(ids, task.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	var links []model.TaskRepositoryLink
	linkQuery := tx.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Where("task_id IN ?", ids)
	if owned := snapshot.graph.createdLinkIDs(); len(owned) > 0 {
		linkQuery = linkQuery.Where("id NOT IN ?", owned)
	}
	if err := lockConfigImportAssetQuery(linkQuery.Order("id")).Find(&links).Error; err != nil {
		return fmt.Errorf("config import rollback asset link lock: %w", err)
	}
	for _, link := range links {
		if link.TaskID == nil {
			continue
		}
		task := projected[*link.TaskID]
		if link.UnlinkedAt != nil {
			// Restic lifecycle deletion can use an unlinked owner's historical
			// link while another task still uses the repository binding.
			if err := snapshot.validateHistoricalAssetBindingOwner(ctx, tx, link, task); err != nil {
				return err
			}
			continue
		}
		var repository model.BackupRepository
		if err := lockConfigImportAssetQuery(tx.WithContext(ctx)).Where("id = ?", link.RepositoryID).First(&repository).Error; err != nil {
			return fmt.Errorf("config import rollback asset repository lock: %w", err)
		}
		provider := backupasset.ProviderKind(strings.ToLower(strings.TrimSpace(task.ExecutorType)))
		version, _, _, err := backupasset.MapPublicationMode(provider, backupasset.TaskPublicationMode(link.PublicationMode))
		if err != nil || string(provider) != repository.ProviderKind || string(version) != repository.VersionMode || task.ArchivedAt != nil || task.NodeID != link.NodeIDSnapshot {
			return fmt.Errorf("config import rollback asset link is incompatible with restored task")
		}
		// Managed Rclone publication binds the live task and node names to its
		// link. Other providers use these names only as historical snapshots.
		if provider == backupasset.ProviderRclone && link.PublicationMode != string(backupasset.PublicationLegacyMutable) {
			var node struct {
				ID   uint
				Name string
			}
			if row, ok := snapshot.nodes.prior[rawConfigImportKey(task.NodeID)]; ok {
				node.ID, node.Name = task.NodeID, rawConfigImportString(row["name"])
			} else if err := lockConfigImportAssetQuery(tx.WithContext(ctx)).Table("nodes").Select("id", "name").Where("id = ?", task.NodeID).Take(&node).Error; err != nil {
				return fmt.Errorf("config import rollback asset node lock: %w", err)
			}
			if node.Name != link.NodeNameSnapshot || task.Name != link.TaskNameSnapshot {
				return fmt.Errorf("config import rollback asset link snapshot is incompatible")
			}
		}
		if provider == backupasset.ProviderRestic {
			if err := snapshot.validateResticLinkLocator(link); err != nil {
				return err
			}
		}
		var bindings []model.RepositoryAccessBinding
		if err := lockConfigImportAssetQuery(tx.WithContext(ctx)).Where("repository_id = ? AND status = ?", repository.ID, "active").Order("id").Find(&bindings).Error; err != nil {
			return fmt.Errorf("config import rollback asset binding lock: %w", err)
		}
		for _, binding := range bindings {
			var document struct {
				Provider        string `json:"provider"`
				TaskID          uint   `json:"task_id"`
				NodeID          uint   `json:"node_id"`
				RepositoryID    string `json:"repository_id"`
				LinkID          string `json:"task_repository_link_id"`
				PublicationMode string `json:"publication_mode"`
				IdentityClass   string `json:"identity_class"`
				Locator         string `json:"locator"`
				Secret          string `json:"secret"`
			}
			if err := json.Unmarshal([]byte(binding.EncryptedConfig), &document); err != nil {
				return fmt.Errorf("config import rollback asset binding is invalid")
			}
			expectedKind := "task_derived_v1"
			if provider == backupasset.ProviderRsync && link.PublicationMode != string(backupasset.PublicationLegacyMutable) {
				expectedKind = "managed_rsync_v2"
			}
			if provider == backupasset.ProviderRclone && link.PublicationMode != string(backupasset.PublicationLegacyMutable) {
				expectedKind = "managed_rclone_v3"
			}
			if binding.BindingKind != expectedKind || document.Provider != string(provider) {
				return fmt.Errorf("config import rollback asset binding is incompatible")
			}
			if expectedKind != "task_derived_v1" && (document.TaskID != task.ID || document.NodeID != task.NodeID || document.RepositoryID != repository.ID || document.LinkID != link.ID || document.PublicationMode != link.PublicationMode) {
				return fmt.Errorf("config import rollback managed asset binding is incompatible")
			}
			if document.IdentityClass == "task_scoped_endpoint" && (document.TaskID != task.ID || document.NodeID != task.NodeID || document.Locator != strings.TrimSpace(task.RsyncTarget)) {
				return fmt.Errorf("config import rollback asset endpoint binding is incompatible")
			}
			if owner, ok := projected[document.TaskID]; ok && (owner.NodeID != document.NodeID || strings.ToLower(strings.TrimSpace(owner.ExecutorType)) != document.Provider || owner.ArchivedAt != nil) {
				return fmt.Errorf("config import rollback asset binding owner is incompatible")
			}
			if provider == backupasset.ProviderRestic && document.Provider == "restic" {
				if err := snapshot.validateResticBindingAccess(document.TaskID, document.Locator, document.Secret); err != nil {
					return err
				}
			}
		}
		if provider == backupasset.ProviderRestic && link.PublicationMode == string(backupasset.PublicationNativeSnapshot) {
			if err := snapshot.validateNewResticLinkAccess(ctx, tx, link, task); err != nil {
				return err
			}
		}
	}
	return nil
}

func (snapshot *configImportRollbackSnapshot) validateHistoricalAssetBindingOwner(ctx context.Context, tx *gorm.DB, link model.TaskRepositoryLink, task model.Task) error {
	if link.PublicationMode != string(backupasset.PublicationNativeSnapshot) {
		return nil
	}
	if err := lockConfigImportAssetParent(tx.WithContext(ctx), "backup_repositories", link.RepositoryID); err != nil {
		return err
	}
	var bindings []model.RepositoryAccessBinding
	if err := lockConfigImportAssetQuery(tx.WithContext(ctx)).Where("repository_id = ? AND status = ?", link.RepositoryID, "active").Order("id").Find(&bindings).Error; err != nil {
		return fmt.Errorf("config import rollback historical asset binding lock: %w", err)
	}
	for _, binding := range bindings {
		var document struct {
			Provider string `json:"provider"`
			TaskID   uint   `json:"task_id"`
			NodeID   uint   `json:"node_id"`
			Locator  string `json:"locator"`
			Secret   string `json:"secret"`
		}
		if err := json.Unmarshal([]byte(binding.EncryptedConfig), &document); err != nil {
			return fmt.Errorf("config import rollback historical asset binding is invalid")
		}
		if document.TaskID == task.ID && (document.Provider != "restic" || strings.ToLower(strings.TrimSpace(task.ExecutorType)) != document.Provider || task.NodeID != document.NodeID) {
			return fmt.Errorf("config import rollback historical asset binding owner is incompatible")
		}
		if document.TaskID == task.ID && document.Provider == "restic" {
			if err := snapshot.validateResticBindingAccess(document.TaskID, document.Locator, document.Secret); err != nil {
				return err
			}
		}
	}
	return nil
}

// A link records the locator used at connect time, independently of a shared
// repository's retained binding, which may belong to a different task.
func (snapshot *configImportRollbackSnapshot) validateResticLinkLocator(link model.TaskRepositoryLink) error {
	if link.TaskID == nil {
		return nil
	}
	key := rawConfigImportKey(*link.TaskID)
	prior, priorOK := snapshot.tasks.prior[key]
	current, currentOK := snapshot.tasks.current[key]
	if !priorOK || !currentOK {
		return nil
	}
	before := strings.TrimSpace(rawConfigImportString(prior["rsync_target"]))
	after := strings.TrimSpace(rawConfigImportString(current["rsync_target"]))
	if before == after || after == "" {
		return nil
	}
	locator, err := secure.DecryptIfNeeded(link.EncryptedLegacyLocator)
	if err != nil {
		return fmt.Errorf("config import rollback asset link locator is invalid")
	}
	if strings.TrimSpace(locator) == after {
		return fmt.Errorf("config import rollback asset link locator is incompatible")
	}
	return nil
}

// Compare only access owned by a restored task; a shared Restic repository's
// binding is not required to belong to every task linked to that repository.
func (snapshot *configImportRollbackSnapshot) validateResticBindingAccess(taskID uint, locator, secret string) error {
	key := rawConfigImportKey(taskID)
	prior, priorOK := snapshot.tasks.prior[key]
	current, currentOK := snapshot.tasks.current[key]
	if !priorOK || !currentOK {
		return nil
	}
	before := strings.TrimSpace(rawConfigImportString(prior["rsync_target"]))
	after := strings.TrimSpace(rawConfigImportString(current["rsync_target"]))
	if before != after && after != "" && after == locator {
		return fmt.Errorf("config import rollback asset binding locator is incompatible")
	}
	priorConfig := rawConfigImportString(prior["executor_config"])
	currentConfig := rawConfigImportString(current["executor_config"])
	if priorConfig == currentConfig || secret == "" {
		return nil
	}
	priorConfig, err := secure.DecryptIfNeeded(priorConfig)
	if err != nil {
		return fmt.Errorf("config import rollback asset task access is invalid")
	}
	currentConfig, err = secure.DecryptIfNeeded(currentConfig)
	if err != nil {
		return fmt.Errorf("config import rollback asset task access is invalid")
	}
	priorSecret, priorErr := configImportResticBindingSecret(priorConfig)
	currentSecret, currentErr := configImportResticBindingSecret(currentConfig)
	if priorErr != nil || currentErr != nil {
		return fmt.Errorf("config import rollback asset task access is invalid")
	}
	if priorSecret != currentSecret && currentSecret == secret {
		return fmt.Errorf("config import rollback asset binding access is incompatible")
	}
	return nil
}

// Match repository.bindingFromTask's decodeTaskConfig: empty config is an
// empty object and unrelated Restic execution options do not affect access.
func configImportResticBindingSecret(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var config struct {
		RepositoryPassword string `json:"repository_password"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return "", err
	}
	return config.RepositoryPassword, nil
}
