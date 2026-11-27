package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xirang/backend/internal/model"
)

// capturePriorMissingDrillTargets records only disabled legacy references that
// were already missing before the import. Existing targets are locked so a
// concurrent deletion cannot be mistaken for a legacy missing target.
func (snapshot *configImportRollbackSnapshot) capturePriorMissingDrillTargets(ctx context.Context, tx *gorm.DB) error {
	snapshot.priorMissingDrillTargets = make(map[uint]bool)
	targets := make(map[uint]uint)
	ids := make([]uint, 0)
	for _, row := range snapshot.policies.prior {
		if row["drill_target_node_id"] == nil || !configImportRollbackFalse(row["drill_enabled"]) {
			continue
		}
		id, ok := configImportRollbackUint(row["id"])
		if !ok {
			return fmt.Errorf("config import rollback policy identity is invalid")
		}
		target, ok := configImportRollbackUint(row["drill_target_node_id"])
		if !ok {
			continue
		}
		targets[id] = target
		ids = append(ids, target)
	}
	if len(ids) == 0 {
		return nil
	}
	var rows []struct{ ID uint }
	query := tx.WithContext(ctx).Table("nodes").Select("id").Where("id IN ?", ids).Order("id")
	if tx.Name() != "sqlite" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"})
	}
	if err := query.Find(&rows).Error; err != nil {
		return fmt.Errorf("config import rollback drill target lock failed: %w", err)
	}
	existing := make(map[uint]bool, len(rows))
	for _, row := range rows {
		existing[row.ID] = true
	}
	for id, target := range targets {
		if !existing[target] {
			snapshot.priorMissingDrillTargets[id] = true
		}
	}
	return nil
}

func configImportRollbackFalse(value any) bool {
	switch v := value.(type) {
	case bool:
		return !v
	case int64:
		return v == 0
	case int:
		return v == 0
	case []byte:
		return string(v) == "0" || string(v) == "false"
	case string:
		return v == "0" || v == "false"
	default:
		return false
	}
}

func (snapshot *configImportRollbackSnapshot) unchangedLegacyPolicyReference(row map[string]any, column string) bool {
	id, ok := configImportRollbackUint(row["id"])
	if !ok {
		return false
	}
	current, ok := snapshot.policies.current[rawConfigImportKey(id)]
	if !ok || rawConfigImportKey(row[column]) != rawConfigImportKey(current[column]) {
		return false
	}
	switch column {
	case "drill_target_node_id":
		return snapshot.priorMissingDrillTargets[id] && configImportRollbackFalse(row["drill_enabled"]) && configImportRollbackFalse(current["drill_enabled"])
	case "app_credential_id":
		return strings.TrimSpace(rawConfigImportString(row["app_profile"])) == "" && strings.TrimSpace(rawConfigImportString(current["app_profile"])) == ""
	}
	return false
}

// validateClassicRollbackReferences checks the projected state before any
// compensation write. Receipt rows are already locked by the journal.
func (snapshot *configImportRollbackSnapshot) validateClassicRollbackReferences(ctx context.Context, tx *gorm.DB) error {
	if snapshot == nil || tx == nil {
		return fmt.Errorf("config import rollback reference validation is unavailable")
	}
	createdNodes, createdKeys := snapshot.nodes.created, snapshot.sshKeys.created
	createdPolicies, createdTasks := snapshot.policies.created, snapshot.tasks.created

	// Only links actually owned by the graph receipt can be removed along with
	// a task. All referenced tables are required by the current schema.
	checks := []struct {
		table, column string
		ids           []any
		excludeIDs    []any
	}{
		{"policy_nodes", "node_id", createdNodes, nil},
		{"policy_nodes", "policy_id", createdPolicies, nil},
		{"node_owners", "node_id", createdNodes, nil},
		{"silences", "match_node_id", createdNodes, nil},
		{"anomaly_events", "node_id", createdNodes, nil},
		{"task_runs", "task_id", createdTasks, nil},
		{"task_cron_occurrences", "task_id", createdTasks, nil},
		{"task_repository_links", "task_id", createdTasks, snapshot.graph.createdLinkIDs()},
		{"recovery_points", "producing_task_id", createdTasks, nil},
	}
	for _, check := range checks {
		if len(check.ids) == 0 {
			continue
		}
		query := tx.WithContext(ctx).Table(check.table).Where(check.column+" IN ?", check.ids)
		if len(check.excludeIDs) > 0 {
			query = query.Where("id NOT IN ?", check.excludeIDs)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return fmt.Errorf("config import rollback reference query failed: %w", err)
		}
		if count != 0 {
			return fmt.Errorf("config import rollback parent has surviving %s references", check.table)
		}
	}

	// Lock restored parents, including those not themselves in the receipt. A
	// missing parent must not be reintroduced as a dangling reference.
	for _, parent := range []struct {
		rows    map[string]map[string]any
		column  string
		table   string
		removed []any
	}{
		{snapshot.nodes.prior, "ssh_key_id", "ssh_keys", createdKeys},
		{snapshot.policies.prior, "drill_target_node_id", "nodes", createdNodes},
		{snapshot.policies.prior, "app_credential_id", "app_credentials", nil},
		{snapshot.tasks.prior, "node_id", "nodes", createdNodes},
		{snapshot.tasks.prior, "policy_id", "policies", createdPolicies},
	} {
		ids := make(map[string]any)
		for _, row := range parent.rows {
			if value := row[parent.column]; value != nil {
				if parent.table == "nodes" && parent.column == "drill_target_node_id" && snapshot.unchangedLegacyPolicyReference(row, parent.column) {
					continue
				}
				if parent.table == "app_credentials" && snapshot.unchangedLegacyPolicyReference(row, parent.column) {
					continue
				}
				if _, ok := configImportRollbackUint(value); !ok {
					return fmt.Errorf("config import rollback parent identity is invalid")
				}
				ids[rawConfigImportKey(value)] = value
			}
		}
		for _, id := range parent.removed {
			if _, exists := ids[rawConfigImportKey(id)]; exists {
				return fmt.Errorf("config import rollback restored parent would be removed")
			}
		}
		if len(ids) == 0 {
			continue
		}
		values := make([]any, 0, len(ids))
		for _, id := range ids {
			values = append(values, id)
		}
		var rows []struct{ ID uint }
		query := tx.WithContext(ctx).Table(parent.table).Select("id").Where("id IN ?", values).Order("id")
		if !strings.EqualFold(tx.Name(), "sqlite") {
			query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"})
		}
		if err := query.Find(&rows).Error; err != nil {
			return fmt.Errorf("config import rollback parent lock failed: %w", err)
		}
		if len(rows) != len(ids) {
			return fmt.Errorf("config import rollback restored parent is missing")
		}
	}

	if len(createdKeys) > 0 {
		var nodes []struct {
			ID       uint
			SSHKeyID *uint
		}
		if err := tx.WithContext(ctx).Table("nodes").Select("id", "ssh_key_id").Where("ssh_key_id IN ?", createdKeys).Find(&nodes).Error; err != nil {
			return fmt.Errorf("config import rollback node references query failed: %w", err)
		}
		for _, node := range nodes {
			key := rawConfigImportKey(node.ID)
			if _, owned := snapshot.nodes.current[key]; owned {
				continue // removed or restored to its checked before-image
			}
			return fmt.Errorf("config import rollback key has a surviving node reference")
		}
	}
	if len(createdNodes) > 0 {
		var policies []struct{ ID uint }
		if err := tx.WithContext(ctx).Table("policies").Select("id").Where("drill_target_node_id IN ?", createdNodes).Find(&policies).Error; err != nil {
			return fmt.Errorf("config import rollback drill references query failed: %w", err)
		}
		for _, policy := range policies {
			if _, owned := snapshot.policies.current[rawConfigImportKey(policy.ID)]; !owned {
				return fmt.Errorf("config import rollback node has a surviving drill reference")
			}
		}
	}
	if err := snapshot.validateClassicRollbackTasks(ctx, tx); err != nil {
		return err
	}
	return snapshot.validateProjectedAssetLinks(ctx, tx)
}

type configImportRollbackTaskReference struct {
	ID              uint
	NodeID          uint
	PolicyID        *uint
	DependsOnTaskID *uint
	ArchivedAt      *time.Time
}

func (snapshot *configImportRollbackSnapshot) validateClassicRollbackTasks(ctx context.Context, tx *gorm.DB) error {
	if len(snapshot.tasks.current) == 0 && len(snapshot.nodes.created) == 0 && len(snapshot.policies.created) == 0 {
		return nil
	}
	var rows []model.Task
	query := tx.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&model.Task{}).
		Select("id", "node_id", "policy_id", "depends_on_task_id", "archived_at").Order("id")
	if !strings.EqualFold(tx.Name(), "sqlite") {
		query = query.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"})
	}
	if err := query.Find(&rows).Error; err != nil {
		return fmt.Errorf("config import rollback task reference lock failed: %w", err)
	}
	projected := make(map[uint]configImportRollbackTaskReference, len(rows))
	for _, row := range rows {
		projected[row.ID] = configImportRollbackTaskReference{row.ID, row.NodeID, row.PolicyID, row.DependsOnTaskID, row.ArchivedAt}
	}
	for _, id := range snapshot.tasks.created {
		value, ok := configImportRollbackUint(id)
		if !ok {
			return fmt.Errorf("config import rollback task identity is invalid")
		}
		delete(projected, value)
	}
	roots := make(map[uint]bool, len(snapshot.tasks.prior))
	for _, row := range snapshot.tasks.prior {
		id, ok := configImportRollbackUint(row["id"])
		if !ok {
			return fmt.Errorf("config import rollback task identity is invalid")
		}
		nodeID, ok := configImportRollbackUint(row["node_id"])
		if !ok {
			return fmt.Errorf("config import rollback task node identity is invalid")
		}
		prior := projected[id]
		prior.ID, prior.NodeID = id, nodeID
		prior.PolicyID, prior.DependsOnTaskID = nil, nil
		for _, ref := range []struct {
			column string
			dst    **uint
		}{{"policy_id", &prior.PolicyID}, {"depends_on_task_id", &prior.DependsOnTaskID}} {
			if row[ref.column] != nil {
				value, ok := configImportRollbackUint(row[ref.column])
				if !ok {
					return fmt.Errorf("config import rollback task reference is invalid")
				}
				*ref.dst = &value
			}
		}
		// Archive state is part of the prior image; only its nullness matters.
		if row["archived_at"] == nil {
			prior.ArchivedAt = nil
		} else {
			stamp := time.Time{}
			prior.ArchivedAt = &stamp
		}
		projected[id] = prior
		roots[id] = true
	}
	removedNodes, removedPolicies, removedTasks := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range snapshot.nodes.created {
		removedNodes[rawConfigImportKey(id)] = true
	}
	for _, id := range snapshot.policies.created {
		removedPolicies[rawConfigImportKey(id)] = true
	}
	for _, id := range snapshot.tasks.created {
		removedTasks[rawConfigImportKey(id)] = true
	}
	dependents := make(map[uint][]uint)
	for id, task := range projected {
		if removedNodes[rawConfigImportKey(task.NodeID)] || (task.PolicyID != nil && removedPolicies[rawConfigImportKey(*task.PolicyID)]) {
			return fmt.Errorf("config import rollback parent has a surviving task reference")
		}
		if task.DependsOnTaskID != nil {
			dependency := *task.DependsOnTaskID
			if removedTasks[rawConfigImportKey(dependency)] {
				return fmt.Errorf("config import rollback task has a surviving dependent")
			}
			dependents[dependency] = append(dependents[dependency], id)
		}
	}
	// Only the reverse closure of restored tasks can acquire a new graph
	// defect. Preserve unrelated legacy defects, then walk each affected chain
	// once, sharing the state of already validated tails.
	queue := make([]uint, 0, len(roots))
	for id := range roots {
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, id := range dependents[current] {
			if !roots[id] {
				roots[id] = true
				queue = append(queue, id)
			}
		}
	}
	state := make(map[uint]uint8)
	for root := range roots {
		path := make([]uint, 0)
		id := root
		for state[id] != 2 {
			if state[id] == 1 {
				return fmt.Errorf("config import rollback task dependency cycle")
			}
			state[id] = 1
			path = append(path, id)
			task, exists := projected[id]
			if !exists {
				return fmt.Errorf("config import rollback task dependency is missing")
			}
			if task.DependsOnTaskID == nil {
				break
			}
			id = *task.DependsOnTaskID
			dependency, exists := projected[id]
			if !exists || dependency.ArchivedAt != nil {
				return fmt.Errorf("config import rollback task dependency is missing or archived")
			}
		}
		for _, visited := range path {
			state[visited] = 2
		}
	}
	return nil
}
