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

func TestConfigImportRollbackResticAccessReceiptMigrated(t *testing.T) {
	for _, scenario := range []struct {
		name            string
		postLocator     string
		postConfig      string
		linkLocator     string
		bindingLocator  string
		bindingSecret   string
		otherOwner      bool
		historicalOwner bool
		renameBefore    bool
		preexistingLink bool
		graphLink       bool
		changeHost      bool
		changeMetadata  bool
		wantConflict    bool
		wantReason      string
	}{
		{name: "later_link_and_binding_locator", postLocator: "/repo/b", linkLocator: "/repo/b", bindingLocator: "/repo/b", wantConflict: true, wantReason: "asset link locator is incompatible"},
		{name: "later_link_shared_binding", postLocator: "/repo/b", linkLocator: "/repo/b", bindingLocator: "/repo/other", otherOwner: true, wantConflict: true, wantReason: "asset link locator is incompatible"},
		{name: "later_graph_link_empty_locator_changed", postLocator: "/repo/b", bindingLocator: "/repo/other", otherOwner: true, graphLink: true, changeMetadata: true, wantConflict: true, wantReason: "asset link locator is incompatible"},
		{name: "later_graph_link_empty_locator_unchanged", postLocator: "/repo/a", bindingLocator: "/repo/other", otherOwner: true, graphLink: true, changeMetadata: true},
		{name: "retained_historical_owner_locator", postLocator: "/repo/b", linkLocator: "/repo/a", bindingLocator: "/repo/b", historicalOwner: true, wantConflict: true, wantReason: "asset binding locator is incompatible"},
		{name: "retained_owner_password", postLocator: "/repo/a", postConfig: `{"repository_password":"post-secret"}`, linkLocator: "/repo/a", bindingLocator: "/repo/a", bindingSecret: "post-secret", wantConflict: true, wantReason: "asset binding access is incompatible"},
		{name: "unchanged_password_other_config", postLocator: "/repo/a", postConfig: `{"repository_password":"prior-secret","exclude_patterns":["cache"]}`, linkLocator: "/repo/a", bindingLocator: "/repo/a"},
		{name: "shared_other_owner", postLocator: "/repo/a", linkLocator: "/repo/a", bindingLocator: "/repo/other", otherOwner: true},
		{name: "restore_prior_link_access", postLocator: "/repo/b", linkLocator: "/repo/a", bindingLocator: "/repo/a"},
		{name: "later_shared_link_password_changed", postLocator: "/repo/a", postConfig: `{"repository_password":"post-secret"}`, linkLocator: "/repo/a", bindingLocator: "/repo/other", otherOwner: true, wantConflict: true, wantReason: "asset link access is incompatible"},
		{name: "later_native_link_host_changed", postLocator: "/repo/a", linkLocator: "/repo/a", bindingLocator: "/repo/other", otherOwner: true, changeHost: true, wantConflict: true, wantReason: "asset link access is incompatible"},
		{name: "preexisting_shared_link_password_and_host_changed", postLocator: "/repo/a", postConfig: `{"repository_password":"post-secret"}`, linkLocator: "/repo/a", bindingLocator: "/repo/other", otherOwner: true, preexistingLink: true, changeHost: true},
		{name: "later_shared_link_metadata_and_options_changed", postLocator: "/repo/a", postConfig: `{"repository_password":"prior-secret","exclude_patterns":["cache"]}`, linkLocator: "/repo/a", bindingLocator: "/repo/other", otherOwner: true, changeMetadata: true},
		{name: "renamed_before_import", postLocator: "/repo/a", linkLocator: "/repo/a", bindingLocator: "/repo/a", renameBefore: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				setConfigHandlerTestEncryption(t)
				db := openMigratedRetryTransactionDB(t, engine)
				ctx := context.Background()
				now := time.Now().UTC()
				node := model.Node{Name: "original-node", Host: "127.0.0.1", Username: "root", BackupDir: "node"}
				if err := db.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				task := model.Task{Name: "original-task", NodeID: node.ID, ExecutorType: "restic", RsyncTarget: "/repo/a", ExecutorConfig: `{"repository_password":"prior-secret"}`, Source: "manual"}
				if err := db.Create(&task).Error; err != nil {
					t.Fatal(err)
				}
				otherNode := model.Node{Name: "other-node", Host: "127.0.0.2", Username: "root", BackupDir: "other-node"}
				if err := db.Create(&otherNode).Error; err != nil {
					t.Fatal(err)
				}
				other := model.Task{Name: "other-task", NodeID: otherNode.ID, ExecutorType: "restic", RsyncTarget: "/repo/other", ExecutorConfig: `{"repository_password":"other-secret"}`, Source: "manual"}
				if err := db.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
				repo := model.BackupRepository{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ProviderKind: "restic", VersionMode: "native_snapshot", DisplayName: "repository", Status: "online", ImmutabilityLevel: "backend_versioned", CreatedAt: now, UpdatedAt: now}
				if err := db.Create(&repo).Error; err != nil {
					t.Fatal(err)
				}
				link := model.TaskRepositoryLink{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", TaskID: &task.ID, RepositoryID: repo.ID, TaskNameSnapshot: task.Name, NodeIDSnapshot: node.ID, NodeNameSnapshot: node.Name, PublicationMode: "native_snapshot", EncryptedLegacyLocator: scenario.linkLocator, LinkedAt: now, CreatedAt: now, UpdatedAt: now}
				if scenario.historicalOwner {
					link.UnlinkedAt = &now
				}
				if scenario.renameBefore || scenario.preexistingLink {
					if err := db.Create(&link).Error; err != nil {
						t.Fatal(err)
					}
				}
				if scenario.renameBefore {
					if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Update("name", "renamed-node").Error; err != nil {
						t.Fatal(err)
					}
					if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("name", "renamed-task").Error; err != nil {
						t.Fatal(err)
					}
				}
				snapshot := &configImportRollbackSnapshot{
					tasks: newConfigImportTableSnapshot("tasks", "id", "id = ?", task.ID),
					nodes: newConfigImportTableSnapshot("nodes", "id", "id = ?", node.ID),
				}
				if err := configImportTransaction(ctx, db, func(tx *gorm.DB) error {
					if err := snapshot.tasks.capture(ctx, tx); err != nil {
						return err
					}
					return snapshot.nodes.capture(ctx, tx)
				}); err != nil {
					t.Fatal(err)
				}
				updates := map[string]any{"rsync_target": scenario.postLocator, "command": "imported"}
				if scenario.postConfig != "" {
					updates["executor_config"] = scenario.postConfig
				}
				if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
					t.Fatal(err)
				}
				nodeUpdates := map[string]any{}
				if scenario.changeHost {
					nodeUpdates["host"] = "192.0.2.10"
				}
				if scenario.changeMetadata {
					nodeUpdates["name"] = "imported-node"
					nodeUpdates["tags"] = "imported-tag"
				}
				if len(nodeUpdates) > 0 {
					if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Updates(nodeUpdates).Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := configImportTransaction(ctx, db, func(tx *gorm.DB) error {
					if err := snapshot.tasks.sealIDs(ctx, tx, []any{task.ID}); err != nil {
						return err
					}
					if err := snapshot.nodes.sealIDs(ctx, tx, []any{node.ID}); err != nil {
						return err
					}
					return snapshot.captureSealedResticLinks(ctx, tx)
				}); err != nil {
					t.Fatal(err)
				}
				if !scenario.renameBefore && !scenario.preexistingLink && !scenario.graphLink {
					if err := db.Create(&link).Error; err != nil {
						t.Fatal(err)
					}
				}
				if scenario.historicalOwner || scenario.otherOwner {
					otherLink := model.TaskRepositoryLink{ID: "cccccccccccccccccccccccccccccccc", TaskID: &other.ID, RepositoryID: repo.ID, TaskNameSnapshot: other.Name, NodeIDSnapshot: otherNode.ID, NodeNameSnapshot: otherNode.Name, PublicationMode: "native_snapshot", EncryptedLegacyLocator: "/repo/other", LinkedAt: now, CreatedAt: now, UpdatedAt: now}
					if err := db.Create(&otherLink).Error; err != nil {
						t.Fatal(err)
					}
				}
				owner, ownerNode := task.ID, node.ID
				secret := scenario.bindingSecret
				if secret == "" {
					secret = "prior-secret"
				}
				if scenario.otherOwner {
					owner, ownerNode, secret = other.ID, otherNode.ID, "other-secret"
				}
				binding := model.RepositoryAccessBinding{ID: "dddddddddddddddddddddddddddddddd", RepositoryID: repo.ID, BindingKind: "task_derived_v1", Status: "active", ConfigFingerprint: "fingerprint", EncryptedConfig: fmt.Sprintf(`{"version":1,"provider":"restic","identity_class":"native_repository","task_id":%d,"node_id":%d,"identity_salt":%q,"locator":%q,"secret":%q,"endpoint_facts":["task:%d","node:%d"],"native_repository_id":%q,"adapter_revision":"restic-v1"}`, owner, ownerNode, strings.Repeat("ab", 32), scenario.bindingLocator, secret, owner, ownerNode, strings.Repeat("c", 64)), CreatedAt: now, UpdatedAt: now}
				if err := db.Create(&binding).Error; err != nil {
					t.Fatal(err)
				}
				if scenario.graphLink {
					graph := &configImportGraphRollback{}
					graphLink := configAssetLinkExport{
						LinkRef: "link", RepositoryRef: "repo", TaskRef: "task:original-task@imported-node",
						TaskNameSnapshot: task.Name, NodeNameSnapshot: "imported-node", PublicationMode: "native_snapshot",
					}
					var linkID string
					if err := configImportTransaction(ctx, db, func(tx *gorm.DB) error {
						var err error
						linkID, err = (&ConfigHandler{}).importConfigAssetLink(tx, "11111111111111111111111111111111", graphLink, repo.ID, now, graph)
						return err
					}); err != nil {
						t.Fatal(err)
					}
					link = model.TaskRepositoryLink{}
					if err := db.First(&link, "id = ?", linkID).Error; err != nil {
						t.Fatal(err)
					}
					if link.TaskID == nil || *link.TaskID != task.ID || link.NodeIDSnapshot != node.ID || link.RepositoryID != repo.ID || link.EncryptedLegacyLocator != "" {
						t.Fatalf("graph link has unexpected task, node, repository or locator: %+v", link)
					}
				}
				journal := newConfigImportRollbackJournal(db, nil)
				journal.install(snapshot)
				err := journal.Restore(ctx)
				if (err != nil) != scenario.wantConflict {
					t.Fatalf("conflict=%t, error=%v", scenario.wantConflict, err)
				}
				if scenario.wantReason != "" && (err == nil || !strings.Contains(err.Error(), scenario.wantReason)) {
					t.Fatalf("error=%v, want reason %q", err, scenario.wantReason)
				}
				var actual model.Task
				if err := db.First(&actual, task.ID).Error; err != nil {
					t.Fatal(err)
				}
				wantLocator := "/repo/a"
				if scenario.wantConflict {
					wantLocator = scenario.postLocator
				}
				if actual.RsyncTarget != wantLocator {
					t.Fatalf("task locator=%q, want %q", actual.RsyncTarget, wantLocator)
				}
				wantConfig := `{"repository_password":"prior-secret"}`
				wantCommand := ""
				if scenario.wantConflict {
					wantCommand = "imported"
					if scenario.postConfig != "" {
						wantConfig = scenario.postConfig
					}
				}
				if actual.ExecutorConfig != wantConfig || actual.Command != wantCommand {
					t.Fatalf("task config=%q command=%q, want config=%q command=%q", actual.ExecutorConfig, actual.Command, wantConfig, wantCommand)
				}
				var actualNode model.Node
				if err := db.First(&actualNode, node.ID).Error; err != nil {
					t.Fatal(err)
				}
				wantHost := "127.0.0.1"
				if scenario.wantConflict && scenario.changeHost {
					wantHost = "192.0.2.10"
				}
				if actualNode.Host != wantHost {
					t.Fatalf("node host=%q, want %q", actualNode.Host, wantHost)
				}
				wantNodeName, wantNodeTags := "original-node", ""
				if scenario.changeMetadata && scenario.wantConflict {
					wantNodeName, wantNodeTags = "imported-node", "imported-tag"
				}
				if scenario.changeMetadata && (actualNode.Name != wantNodeName || actualNode.Tags != wantNodeTags) {
					t.Fatalf("node metadata after restore: name=%q tags=%q, want name=%q tags=%q", actualNode.Name, actualNode.Tags, wantNodeName, wantNodeTags)
				}
				if scenario.renameBefore && (actualNode.Name != "renamed-node" || actual.Name != "renamed-task") {
					t.Fatalf("names after restore: node=%q task=%q", actualNode.Name, actual.Name)
				}
				var count int64
				if err := db.Model(&model.TaskRepositoryLink{}).Where("id = ?", link.ID).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("surviving link count=%d, error=%v", count, err)
				}
				if scenario.graphLink {
					var actualLink model.TaskRepositoryLink
					if err := db.First(&actualLink, "id = ?", link.ID).Error; err != nil {
						t.Fatal(err)
					}
					if actualLink.TaskID == nil || *actualLink.TaskID != task.ID || actualLink.NodeIDSnapshot != node.ID || actualLink.RepositoryID != repo.ID || actualLink.PublicationMode != link.PublicationMode || actualLink.TaskNameSnapshot != link.TaskNameSnapshot || actualLink.NodeNameSnapshot != link.NodeNameSnapshot || actualLink.EncryptedLegacyLocator != "" || actualLink.UnlinkedAt != nil {
						t.Fatalf("graph link changed during rollback: %+v", actualLink)
					}
					if err := db.Model(&model.BackupAssetConfigImportRef{}).Where("local_entity_id = ?", link.ID).Count(&count).Error; err != nil || count != 1 {
						t.Fatalf("surviving graph link mapping count=%d, error=%v", count, err)
					}
				}
				var actualBinding model.RepositoryAccessBinding
				if err := db.Session(&gorm.Session{SkipHooks: true}).First(&actualBinding, "id = ?", binding.ID).Error; err != nil {
					t.Fatal(err)
				}
				if actualBinding.EncryptedConfig != binding.EncryptedConfig || actualBinding.RepositoryID != binding.RepositoryID || actualBinding.BindingKind != binding.BindingKind || actualBinding.Status != binding.Status || actualBinding.ConfigFingerprint != binding.ConfigFingerprint {
					t.Fatal("retained binding changed during rollback")
				}
			})
		})
	}
}
