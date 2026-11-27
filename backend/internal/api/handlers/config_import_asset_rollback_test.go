package handlers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"xirang/backend/internal/database"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestConfigImportAssetRollbackMigrated(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, scenario := range []string{"full", "reused-all", "reused-binding", "changed-image", "foreign-binding", "historical-link", "retention-repository", "retention-link", "foreign-mapping", "recovery-point", "recovery-lineage", "history-latch"} {
			t.Run(engine+"/"+scenario, func(t *testing.T) {
				setConfigHandlerTestEncryption(t)
				db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
				if engine == "sqlite" {
					pool, err := db.DB()
					if err != nil {
						t.Fatal(err)
					}
					pool.SetMaxOpenConns(1)
					if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := database.RunMigrations(db, engine); err != nil {
					t.Fatal(err)
				}
				actor := model.User{Username: "asset-rollback", PasswordHash: "test-hash", Role: "admin"}
				if err := db.Create(&actor).Error; err != nil {
					t.Fatal(err)
				}
				envelope := configImportEnvelope{
					Version: configExportVersion2, DocumentID: "11111111111111111111111111111111",
					Graph: configAssetGraph{
						BackupRepositories:      []configAssetRepositoryExport{{RepositoryRef: "repo", ProviderKind: "restic", DisplayName: "repository", VersionMode: "native_snapshot", ImmutabilityLevel: "xirang_managed", AccessBinding: &configAssetBindingEnvelope{BindingKind: "task_derived_v1", ConfigFingerprint: configV2BindingFingerprint(), Envelope: configV2BindingPlaintext()}}},
						TaskRepositoryLinks:     []configAssetLinkExport{{LinkRef: "link", RepositoryRef: "repo", TaskRef: "task:missing@missing", TaskNameSnapshot: "missing", NodeNameSnapshot: "missing", PublicationMode: "native_snapshot"}},
						BackupRetentionPolicies: []configAssetPolicyExport{{PolicyRef: "policy", ScopeKind: "repository", RepositoryRef: "repo", Revision: 1, RulesJSON: `{"version":1,"count":{"keep_latest":3}}`, Status: "active"}},
					},
				}
				if scenario == "reused-binding" {
					first := envelope
					first.Graph = configAssetGraph{BackupRepositories: append([]configAssetRepositoryExport(nil), envelope.Graph.BackupRepositories...)}
					first.Graph.BackupRepositories[0].AccessBinding = nil
					importAssetRollbackFixture(t, db, first, actor.ID)
					envelope.Graph.TaskRepositoryLinks = nil
					envelope.Graph.BackupRetentionPolicies = nil
				}
				if scenario == "reused-all" {
					importAssetRollbackFixture(t, db, envelope, actor.ID)
				}
				graph := importAssetRollbackFixture(t, db, envelope, actor.ID)
				var repository model.BackupRepository
				if err := db.First(&repository).Error; err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC()
				switch scenario {
				case "changed-image":
					if err := db.Model(&model.BackupRepository{}).Where("id = ?", repository.ID).Update("display_name", "changed").Error; err != nil {
						t.Fatal(err)
					}
				case "foreign-binding":
					binding := model.RepositoryAccessBinding{ID: "22222222222222222222222222222222", RepositoryID: repository.ID, BindingKind: "task_derived_v1", EncryptedConfig: configV2BindingPlaintext(), ConfigFingerprint: configV2BindingFingerprint(), Status: "revoked", RevokedAt: &now, CreatedAt: now, UpdatedAt: now}
					if err := db.Create(&binding).Error; err != nil {
						t.Fatal(err)
					}
				case "historical-link":
					link := model.TaskRepositoryLink{ID: "66666666666666666666666666666666", RepositoryID: repository.ID, TaskNameSnapshot: "historical", PublicationMode: "native_snapshot", LinkedAt: now, UnlinkedAt: &now, CreatedAt: now, UpdatedAt: now}
					if err := db.Create(&link).Error; err != nil {
						t.Fatal(err)
					}
				case "retention-repository", "retention-link":
					kind, scope := "repository", repository.ID
					if scenario == "retention-link" {
						kind, scope = "task_link", fmt.Sprint(graph.createdLinkIDs()[0])
					}
					policy := model.BackupRetentionPolicy{ID: "33333333333333333333333333333333", ScopeKind: kind, ScopeID: scope, Revision: 1, RulesJSON: `{"version":1,"count":{"keep_latest":2}}`, Status: "deleted", CreatedBy: actor.ID, UpdatedBy: actor.ID, CreatedAt: now, UpdatedAt: now, DeletedAt: &now}
					if err := db.Create(&policy).Error; err != nil {
						t.Fatal(err)
					}
				case "foreign-mapping":
					ref := model.BackupAssetConfigImportRef{ID: "44444444444444444444444444444444", SourceDocumentID: "55555555555555555555555555555555", SourceReference: "other", EntityKind: configAssetEntityRepository, LocalEntityID: repository.ID, CreatedAt: now}
					if err := db.Create(&ref).Error; err != nil {
						t.Fatal(err)
					}
				case "recovery-point", "recovery-lineage":
					pointRepository := repository.ID
					if scenario == "recovery-lineage" {
						other := model.BackupRepository{ID: "77777777777777777777777777777777", ProviderKind: "restic", DisplayName: "other", VersionMode: "native_snapshot", Status: "disconnected", CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: "xirang_managed", CreatedAt: now, UpdatedAt: now}
						if err := db.Create(&other).Error; err != nil {
							t.Fatal(err)
						}
						pointRepository = other.ID
					}
					lineage := "{}"
					if scenario == "recovery-lineage" {
						lineage = fmt.Sprintf(`{"task_repository_link_id":%q}`, graph.createdLinkIDs()[0])
					}
					point := model.RecoveryPoint{ID: "88888888888888888888888888888888", RepositoryID: pointRepository, LineageJSON: lineage, Semantics: "native_snapshot", State: "observed", ObservedAt: &now, CapabilityRevision: 1, CapabilitiesJSON: "{}", ImmutabilityLevel: "xirang_managed", PhysicalAvailability: "online", HoldState: "none", CreatedAt: now, UpdatedAt: now}
					if err := db.Create(&point).Error; err != nil {
						t.Fatal(err)
					}
				case "history-latch":
					latch := model.BackupAssetManagedHistoryLatch{ID: "repository:" + repository.ID, Scope: "repository", RepositoryID: &repository.ID, RepositoryIdentityDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", FirstSemantics: "native_snapshot", FirstOrigin: "publication", FirstSeenAt: now, CreatedAt: now, UpdatedAt: now}
					if err := db.Create(&latch).Error; err != nil {
						t.Fatal(err)
					}
				}
				before := assetRollbackCounts(t, db)
				err := db.Transaction(func(tx *gorm.DB) error {
					ctx := context.Background()
					if err := graph.verifyCurrent(ctx, tx); err != nil {
						return err
					}
					if err := graph.validateReferences(ctx, tx); err != nil {
						return err
					}
					return graph.restore(ctx, tx)
				})
				if scenario != "full" && scenario != "reused-all" && scenario != "reused-binding" {
					if err == nil {
						t.Fatal("expected compensation conflict")
					}
					if after := assetRollbackCounts(t, db); after != before {
						t.Fatalf("conflict changed asset rows: before %v after %v", before, after)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				after := assetRollbackCounts(t, db)
				if scenario == "full" && after != [5]int64{} {
					t.Fatalf("full compensation left rows: %v", after)
				}
				if scenario == "reused-binding" && after != [5]int64{1, 0, 0, 0, 1} {
					t.Fatalf("reused repository was not preserved: %v", after)
				}
				if scenario == "reused-all" && after != [5]int64{1, 1, 1, 1, 3} {
					t.Fatalf("replayed graph was changed: %v", after)
				}
			})
		}
	}
}

func importAssetRollbackFixture(t *testing.T, db *gorm.DB, envelope configImportEnvelope, actorID uint) *configImportGraphRollback {
	t.Helper()
	graph := &configImportGraphRollback{}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := (&ConfigHandler{}).importBackupAssetGraph(tx, envelope, actorID, graph); err != nil {
			return err
		}
		return graph.seal(context.Background(), tx)
	})
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func assetRollbackCounts(t *testing.T, db *gorm.DB) [5]int64 {
	t.Helper()
	var counts [5]int64
	for index, table := range []string{"backup_repositories", "task_repository_links", "backup_retention_policies", "repository_access_bindings", "backup_asset_config_import_refs"} {
		if err := db.Table(table).Count(&counts[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return counts
}
