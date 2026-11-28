package handlers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"xirang/backend/internal/model"
)

func TestConfigImportRollbackLegacyDrillReferencesMigrated(t *testing.T) {
	for _, scenario := range []struct {
		name                                                                      string
		existing, priorEnabled, changePointer, enable, deleteTarget, wantConflict bool
	}{
		{name: "missing_unchanged"},
		{name: "missing_pointer_changed", changePointer: true, wantConflict: true},
		{name: "missing_enabled_after", enable: true, wantConflict: true},
		{name: "enabled_prior_missing", priorEnabled: true, wantConflict: true},
		{name: "existing_deleted_after", existing: true, deleteTarget: true, wantConflict: true},
		{name: "existing_retained", existing: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				setConfigHandlerTestEncryption(t)
				db := openMigratedRetryTransactionDB(t, engine)
				target := uint(987654)
				if scenario.existing {
					node := model.Node{Name: "drill-target", Host: "127.0.0.1", Username: "root", BackupDir: "drill-target"}
					if err := db.Create(&node).Error; err != nil {
						t.Fatal(err)
					}
					target = node.ID
				}
				policy := model.Policy{Name: "drill-policy", SourcePath: "/data", TargetPath: "/backup", CronSpec: "0 2 * * *", DrillEnabled: scenario.priorEnabled, DrillTargetNodeID: &target}
				if err := db.Create(&policy).Error; err != nil {
					t.Fatal(err)
				}
				snapshot := &configImportRollbackSnapshot{policies: newConfigImportTableSnapshot("policies", "id", "id = ?", policy.ID)}
				ctx := context.Background()
				if err := configImportTransaction(ctx, db, func(tx *gorm.DB) error {
					if err := snapshot.policies.capture(ctx, tx); err != nil {
						return err
					}
					return snapshot.capturePriorMissingDrillTargets(ctx, tx)
				}); err != nil {
					t.Fatal(err)
				}
				updates := map[string]any{"description": "imported"}
				if scenario.priorEnabled {
					updates["drill_enabled"] = false
				}
				if scenario.changePointer {
					updates["drill_target_node_id"] = target + 1
				}
				if scenario.enable {
					updates["drill_enabled"] = true
				}
				if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
				if err := configImportTransaction(ctx, db, func(tx *gorm.DB) error { return snapshot.policies.sealIDs(ctx, tx, []any{policy.ID}) }); err != nil {
					t.Fatal(err)
				}
				if scenario.deleteTarget {
					if err := db.Delete(&model.Node{}, target).Error; err != nil {
						t.Fatal(err)
					}
				}
				err := configImportTransaction(ctx, db, func(tx *gorm.DB) error { return snapshot.validateClassicRollbackReferences(ctx, tx) })
				if (err != nil) != scenario.wantConflict {
					t.Fatalf("conflict=%t, error=%v", scenario.wantConflict, err)
				}
			})
		})
	}
}

func TestConfigImportRollbackCredentialReferencesMigrated(t *testing.T) {
	for _, scenario := range []struct {
		name, profile                                                   string
		changePointer, credentialExists, deleteCredential, wantConflict bool
	}{
		{name: "active_missing", profile: "mysql", wantConflict: true},
		{name: "active_present", profile: "mysql", credentialExists: true},
		{name: "active_deleted_after", profile: "mysql", credentialExists: true, deleteCredential: true, wantConflict: true},
		{name: "legacy_unchanged"},
		{name: "legacy_changed", changePointer: true, wantConflict: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				setConfigHandlerTestEncryption(t)
				db := openMigratedRetryTransactionDB(t, engine)
				credentialID := uint(987654)
				if scenario.credentialExists {
					if err := db.Exec("INSERT INTO app_credentials (id, name, type, config, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)", credentialID, "credential", "mysql", "{}", time.Now(), time.Now()).Error; err != nil {
						t.Fatal(err)
					}
				}
				policy := model.Policy{Name: "credential-policy", SourcePath: "/data", TargetPath: "/backup", CronSpec: "0 2 * * *", AppProfile: scenario.profile, AppCredentialID: &credentialID}
				if err := db.Create(&policy).Error; err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				snapshot := &configImportRollbackSnapshot{policies: newConfigImportTableSnapshot("policies", "id", "id = ?", policy.ID)}
				if err := configImportTransaction(ctx, db, func(tx *gorm.DB) error { return snapshot.policies.capture(ctx, tx) }); err != nil {
					t.Fatal(err)
				}
				updates := map[string]any{"description": "imported"}
				if scenario.changePointer {
					updates["app_credential_id"] = credentialID + 1
				}
				if err := db.Model(&model.Policy{}).Where("id = ?", policy.ID).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
				if err := configImportTransaction(ctx, db, func(tx *gorm.DB) error { return snapshot.policies.sealIDs(ctx, tx, []any{policy.ID}) }); err != nil {
					t.Fatal(err)
				}
				if scenario.deleteCredential {
					if err := db.Exec("DELETE FROM app_credentials WHERE id = ?", credentialID).Error; err != nil {
						t.Fatal(err)
					}
				}
				err := configImportTransaction(ctx, db, func(tx *gorm.DB) error { return snapshot.validateClassicRollbackReferences(ctx, tx) })
				if (err != nil) != scenario.wantConflict {
					t.Fatalf("conflict=%t, error=%v", scenario.wantConflict, err)
				}
			})
		})
	}
}

func TestConfigImportRollbackProjectedAssetLinksMigrated(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		conflict bool
	}{
		{"compatible", false}, {"executor", true}, {"provider", true}, {"publication", true}, {"version", true},
		{"node", true}, {"node_name", false}, {"binding", true}, {"owned", false}, {"unrelated", false},
		{"rclone_legacy_node_name", false}, {"rclone_prefix_node_name", true}, {"rclone_native_node_name", true},
		{"history_compatible", false}, {"history_incompatible", true}, {"history_other_owner", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				setConfigHandlerTestEncryption(t)
				db := openMigratedRetryTransactionDB(t, engine)
				now := time.Now().UTC()
				node := model.Node{Name: "asset-node", Host: "127.0.0.1", Username: "root", BackupDir: "asset-node"}
				if err := db.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				task := model.Task{Name: "asset-task", NodeID: node.ID, ExecutorType: "restic", Source: "manual"}
				if strings.HasPrefix(scenario.name, "rclone_") {
					task.ExecutorType = "rclone"
				}
				if err := db.Create(&task).Error; err != nil {
					t.Fatal(err)
				}
				repo := model.BackupRepository{ID: "11111111111111111111111111111111", ProviderKind: "restic", VersionMode: "native_snapshot", DisplayName: "repository", Status: "online", ImmutabilityLevel: "backend_versioned", CreatedAt: now, UpdatedAt: now}
				if scenario.name == "provider" {
					repo.ProviderKind = "rclone"
				}
				if scenario.name == "version" {
					repo.VersionMode = "mutable_head"
				}
				if strings.HasPrefix(scenario.name, "rclone_") {
					repo.ProviderKind = "rclone"
					switch scenario.name {
					case "rclone_legacy_node_name":
						repo.VersionMode = "mutable_head"
					case "rclone_prefix_node_name":
						repo.VersionMode = "versioned_prefix"
					case "rclone_native_node_name":
						repo.VersionMode = "native_object_versions"
					}
				}
				if err := db.Create(&repo).Error; err != nil {
					t.Fatal(err)
				}
				link := model.TaskRepositoryLink{ID: "22222222222222222222222222222222", TaskID: &task.ID, RepositoryID: repo.ID, TaskNameSnapshot: task.Name, NodeIDSnapshot: node.ID, NodeNameSnapshot: node.Name, PublicationMode: "native_snapshot", LinkedAt: now, CreatedAt: now, UpdatedAt: now}
				if scenario.name == "publication" {
					link.PublicationMode = "legacy_mutable"
				}
				if strings.HasPrefix(scenario.name, "rclone_") {
					switch scenario.name {
					case "rclone_legacy_node_name":
						link.PublicationMode = "legacy_mutable"
					case "rclone_prefix_node_name":
						link.PublicationMode = "versioned_prefix"
					case "rclone_native_node_name":
						link.PublicationMode = "native_object_versions"
					}
				}
				if strings.HasPrefix(scenario.name, "history_") {
					link.UnlinkedAt = &now
				}
				if err := db.Create(&link).Error; err != nil {
					t.Fatal(err)
				}
				if scenario.name == "binding" {
					binding := model.RepositoryAccessBinding{ID: "33333333333333333333333333333333", RepositoryID: repo.ID, BindingKind: "task_derived_v1", Status: "active", ConfigFingerprint: "fingerprint", EncryptedConfig: fmt.Sprintf(`{"provider":"restic","task_id":%d,"node_id":%d}`, task.ID, node.ID+1), CreatedAt: now, UpdatedAt: now}
					if err := db.Create(&binding).Error; err != nil {
						t.Fatal(err)
					}
				}
				if strings.HasPrefix(scenario.name, "history_") {
					other := model.Task{Name: "asset-other", NodeID: node.ID, ExecutorType: "restic", Source: "manual"}
					if err := db.Create(&other).Error; err != nil {
						t.Fatal(err)
					}
					otherLink := model.TaskRepositoryLink{ID: "44444444444444444444444444444444", TaskID: &other.ID, RepositoryID: repo.ID, TaskNameSnapshot: other.Name, NodeIDSnapshot: node.ID, NodeNameSnapshot: node.Name, PublicationMode: "native_snapshot", LinkedAt: now, CreatedAt: now, UpdatedAt: now}
					if err := db.Create(&otherLink).Error; err != nil {
						t.Fatal(err)
					}
					ownerID := task.ID
					if scenario.name == "history_other_owner" {
						ownerID = other.ID
					}
					binding := model.RepositoryAccessBinding{ID: "55555555555555555555555555555555", RepositoryID: repo.ID, BindingKind: "task_derived_v1", Status: "active", ConfigFingerprint: "fingerprint", EncryptedConfig: fmt.Sprintf(`{"provider":"restic","task_id":%d,"node_id":%d}`, ownerID, node.ID), CreatedAt: now, UpdatedAt: now}
					if err := db.Create(&binding).Error; err != nil {
						t.Fatal(err)
					}
				}
				prior := map[string]any{"id": task.ID, "node_id": node.ID, "name": task.Name, "executor_type": "restic", "archived_at": nil}
				if scenario.name == "executor" || scenario.name == "owned" {
					prior["executor_type"] = "command"
				}
				if scenario.name == "history_incompatible" || scenario.name == "history_other_owner" {
					prior["executor_type"] = "command"
				}
				if scenario.name == "node" {
					prior["node_id"] = node.ID + 1
				}
				snapshot := &configImportRollbackSnapshot{}
				if scenario.name == "unrelated" {
					prior["id"] = task.ID + 1
					prior["executor_type"] = "command"
				}
				snapshot.tasks.prior = map[string]map[string]any{rawConfigImportKey(prior["id"]): prior}
				if scenario.name == "node_name" || strings.HasPrefix(scenario.name, "rclone_") {
					snapshot.tasks.prior = nil
					snapshot.nodes.prior = map[string]map[string]any{rawConfigImportKey(node.ID): {"id": node.ID, "name": "previous-node-name"}}
				}
				if scenario.name == "owned" {
					snapshot.graph.recordLink(link.ID)
				}
				ctx := context.Background()
				err := configImportTransaction(ctx, db, func(tx *gorm.DB) error { return snapshot.validateProjectedAssetLinks(ctx, tx) })
				if (err != nil) != scenario.conflict {
					t.Fatalf("conflict=%t, error=%v", scenario.conflict, err)
				}
			})
		})
	}
}

func TestConfigImportRollbackSurvivingAssetLinkReceiptMigrated(t *testing.T) {
	for _, scenario := range []struct {
		name, priorExecutor string
		restored            bool
	}{
		{"compatible", "restic", true}, {"incompatible", "command", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				setConfigHandlerTestEncryption(t)
				db := openMigratedRetryTransactionDB(t, engine)
				ctx := context.Background()
				now := time.Now().UTC()
				node := model.Node{Name: "receipt-asset-node", Host: "127.0.0.1", Username: "root", BackupDir: "receipt-asset-node"}
				if err := db.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				task := model.Task{Name: "receipt-asset-task", NodeID: node.ID, ExecutorType: scenario.priorExecutor, Command: "prior", Source: "manual"}
				if err := db.Create(&task).Error; err != nil {
					t.Fatal(err)
				}
				snapshot := &configImportRollbackSnapshot{tasks: newConfigImportTableSnapshot("tasks", "id", "id = ?", task.ID)}
				if err := configImportTransaction(ctx, db, func(tx *gorm.DB) error { return snapshot.tasks.capture(ctx, tx) }); err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{"executor_type": "restic", "command": "imported"}).Error; err != nil {
					t.Fatal(err)
				}
				if err := configImportTransaction(ctx, db, func(tx *gorm.DB) error { return snapshot.tasks.sealIDs(ctx, tx, []any{task.ID}) }); err != nil {
					t.Fatal(err)
				}
				repo := model.BackupRepository{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ProviderKind: "restic", VersionMode: "native_snapshot", DisplayName: "repository", Status: "online", ImmutabilityLevel: "backend_versioned", CreatedAt: now, UpdatedAt: now}
				if err := db.Create(&repo).Error; err != nil {
					t.Fatal(err)
				}
				link := model.TaskRepositoryLink{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", TaskID: &task.ID, RepositoryID: repo.ID, TaskNameSnapshot: task.Name, NodeIDSnapshot: node.ID, NodeNameSnapshot: node.Name, PublicationMode: "native_snapshot", LinkedAt: now, CreatedAt: now, UpdatedAt: now}
				if err := db.Create(&link).Error; err != nil {
					t.Fatal(err)
				}
				journal := newConfigImportRollbackJournal(db, nil)
				journal.install(snapshot)
				err := journal.Restore(ctx)
				if (err == nil) != scenario.restored {
					t.Fatalf("restored=%t, error=%v", scenario.restored, err)
				}
				var actual model.Task
				if err := db.First(&actual, task.ID).Error; err != nil {
					t.Fatal(err)
				}
				wantCommand := "imported"
				if scenario.restored {
					wantCommand = "prior"
				}
				if actual.Command != wantCommand {
					t.Fatalf("command=%q, want %q", actual.Command, wantCommand)
				}
				var count int64
				if err := db.Model(&model.TaskRepositoryLink{}).Where("id = ?", link.ID).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("surviving link count=%d, error=%v", count, err)
				}
			})
		})
	}
}

func TestConfigImportRollbackLegacyDrillReceiptMigrated(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		setConfigHandlerTestEncryption(t)
		db := openMigratedRetryTransactionDB(t, engine)
		missing := uint(987654)
		policy := model.Policy{Name: "legacy-drill-receipt", Description: "prior", SourcePath: "/data", TargetPath: "/backup", CronSpec: "0 2 * * *", DrillTargetNodeID: &missing}
		if err := db.Create(&policy).Error; err != nil {
			t.Fatal(err)
		}
		body := `{"policies":[{"name":"legacy-drill-receipt","description":"imported","source_path":"/data","target_path":"/backup","cron_spec":"0 2 * * *"}],"system_settings":[{"key":"backup_assets.content_preview_ttl","value":"3m"}]}`
		serveClassicRollbackFailure(t, db, body, func() error {
			var imported model.Policy
			if err := db.First(&imported, policy.ID).Error; err != nil {
				return err
			}
			if imported.Description != "imported" {
				return fmt.Errorf("policy was not imported")
			}
			return nil
		})
		var restored model.Policy
		if err := db.First(&restored, policy.ID).Error; err != nil {
			t.Fatal(err)
		}
		if restored.Description != "prior" || restored.DrillEnabled || restored.DrillTargetNodeID == nil || *restored.DrillTargetNodeID != missing {
			t.Fatal("legacy disabled drill reference was not restored")
		}
	})
}
