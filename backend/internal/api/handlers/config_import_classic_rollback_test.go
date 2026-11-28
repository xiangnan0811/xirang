package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
)

func serveClassicRollbackFailure(t *testing.T, db *gorm.DB, body string, after func() error, skip ...bool) {
	t.Helper()
	called := false
	var callbackErr error
	runtime := &configImportRollbackRuntime{failure: errors.New("test publication failure")}
	runtime.afterPersist = func() error { called = true; callbackErr = after(); return callbackErr }
	router := gin.New()
	router.POST("/config/import", NewConfigHandler(db, settings.NewService(db)).WithBackupAssetTransitioner(runtime).Import)
	path := "/config/import?conflict=overwrite"
	if len(skip) > 0 && skip[0] {
		path = "/config/import?conflict=skip"
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if callbackErr != nil {
		t.Fatalf("publication callback failed: %v", callbackErr)
	}
	if !called || response.Code != http.StatusInternalServerError {
		t.Fatalf("publication callback called=%t status=%d", called, response.Code)
	}
}

func TestConfigImportClassicRollbackScopedWritesMigrated(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		setConfigHandlerTestEncryption(t)
		db := openMigratedRetryTransactionDB(t, engine)
		nodes := []model.Node{
			{Name: "receipt-node-a", Host: "127.0.0.1", Username: "root", BackupDir: "receipt-node-a"},
			{Name: "receipt-node-b", Host: "127.0.0.2", Username: "root", BackupDir: "receipt-node-b"},
		}
		for i := range nodes {
			if err := db.Create(&nodes[i]).Error; err != nil {
				t.Fatal(err)
			}
		}
		tasks := []model.Task{
			{Name: "receipt-shared", NodeID: nodes[0].ID, ExecutorType: "command", Command: "old-a", Source: "manual"},
			{Name: "receipt-shared", NodeID: nodes[1].ID, ExecutorType: "command", Command: "old-b", Source: "manual"},
		}
		for i := range tasks {
			if err := db.Create(&tasks[i]).Error; err != nil {
				t.Fatal(err)
			}
		}
		key := model.SSHKey{Name: "receipt-rejected", Username: "old-user", KeyType: "ed25519"}
		if err := db.Create(&key).Error; err != nil {
			t.Fatal(err)
		}
		body := `{"ssh_keys":[{"name":"receipt-rejected","private_key":true}],"policies":[{"name":"receipt-new-policy","source_path":"/data","target_path":"/backup","cron_spec":"0 2 * * *"}],"tasks":[{"name":"receipt-shared","node_name":"receipt-node-a","executor_type":"command","command":"imported"}],"system_settings":[{"key":"backup_assets.content_preview_ttl","value":"3m"}]}`
		serveClassicRollbackFailure(t, db, body, func() error {
			var imported model.Task
			if err := db.First(&imported, tasks[0].ID).Error; err != nil {
				return err
			}
			if imported.Command != "imported" {
				return errors.New("expected imported task before publication")
			}
			if err := db.Model(&model.Task{}).Where("id = ?", tasks[1].ID).Update("command", "foreign-change").Error; err != nil {
				return err
			}
			return db.Model(&model.SSHKey{}).Where("id = ?", key.ID).Update("username", "foreign-user").Error
		})
		var restored, foreign model.Task
		if err := db.First(&restored, tasks[0].ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&foreign, tasks[1].ID).Error; err != nil {
			t.Fatal(err)
		}
		if restored.Command != "old-a" || foreign.Command != "foreign-change" {
			t.Fatalf("unexpected task commands: restored=%q foreign=%q", restored.Command, foreign.Command)
		}
		var storedKey model.SSHKey
		if err := db.First(&storedKey, key.ID).Error; err != nil || storedKey.Username != "foreign-user" {
			t.Fatalf("rejected key changed: %v", err)
		}

		var count int64
		if err := db.Model(&model.Policy{}).Where("name = ?", "receipt-new-policy").Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("created policy remains: count=%d err=%v", count, err)
		}
	})
}

func TestConfigImportClassicRollbackSkippedWriteMigrated(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		setConfigHandlerTestEncryption(t)
		db := openMigratedRetryTransactionDB(t, engine)
		node := model.Node{Name: "receipt-skip-node", Host: "127.0.0.1", Username: "root", BackupDir: "receipt-skip-node"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatal(err)
		}
		task := model.Task{Name: "receipt-skipped", NodeID: node.ID, ExecutorType: "command", Command: "old", Source: "manual"}
		if err := db.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
		body := `{"tasks":[{"name":"receipt-skipped","node_name":"receipt-skip-node","executor_type":"command","command":"ignored"},{"name":"receipt-created","node_name":"receipt-skip-node","executor_type":"command","command":"created"}],"system_settings":[{"key":"backup_assets.content_preview_ttl","value":"3m"}]}`
		serveClassicRollbackFailure(t, db, body, func() error {
			return db.Model(&model.Task{}).Where("id = ?", task.ID).Update("command", "foreign-change").Error
		}, true)
		var stored model.Task
		if err := db.First(&stored, task.ID).Error; err != nil || stored.Command != "foreign-change" {
			t.Fatalf("skipped task changed: %v", err)
		}
		var count int64
		if err := db.Model(&model.Task{}).Where("name = ?", "receipt-created").Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("created task remains: count=%d err=%v", count, err)
		}
	})
}
func TestConfigImportClassicRollbackSurvivingReferencesMigrated(t *testing.T) {
	for _, scenario := range []string{"dependent", "foreign_node_key"} {
		t.Run(scenario, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				setConfigHandlerTestEncryption(t)
				db := openMigratedRetryTransactionDB(t, engine)
				node := model.Node{Name: "receipt-parent-node", Host: "127.0.0.1", Username: "root", BackupDir: "receipt-parent-node"}
				if err := db.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				keyMaterial := configNameMappingPrivateKey(t, 91)
				body := fmt.Sprintf(`{"ssh_keys":[{"name":"receipt-created-key","private_key":%q}],"tasks":[{"name":"receipt-created-task","node_name":"receipt-parent-node","executor_type":"command","command":"imported"}],"system_settings":[{"key":"backup_assets.content_preview_ttl","value":"3m"}]}`, keyMaterial)
				var dependentID, foreignNodeID uint
				serveClassicRollbackFailure(t, db, body, func() error {
					var task model.Task
					if err := db.Where("name = ?", "receipt-created-task").First(&task).Error; err != nil {
						return err
					}
					var key model.SSHKey
					if err := db.Where("name = ?", "receipt-created-key").First(&key).Error; err != nil {
						return err
					}
					if scenario == "dependent" {
						dependent := model.Task{Name: "receipt-foreign-dependent", NodeID: node.ID, DependsOnTaskID: &task.ID, ExecutorType: "command", Command: "true", Source: "manual"}
						if err := db.Create(&dependent).Error; err != nil {
							return err
						}
						dependentID = dependent.ID
					} else {
						foreign := model.Node{Name: "receipt-foreign-node", Host: "127.0.0.2", Username: "root", BackupDir: "receipt-foreign-node", SSHKeyID: &key.ID}
						if err := db.Create(&foreign).Error; err != nil {
							return err
						}
						foreignNodeID = foreign.ID
					}
					return nil
				})
				var task model.Task
				if err := db.Where("name = ?", "receipt-created-task").First(&task).Error; err != nil {
					t.Fatalf("created task removed despite conflict: %v", err)
				}
				var key model.SSHKey
				if err := db.Where("name = ?", "receipt-created-key").First(&key).Error; err != nil {
					t.Fatalf("created key removed despite conflict: %v", err)
				}
				if scenario == "dependent" {
					var dependent model.Task
					if err := db.First(&dependent, dependentID).Error; err != nil || dependent.DependsOnTaskID == nil || *dependent.DependsOnTaskID != task.ID {
						t.Fatalf("dependent reference changed: %v", err)
					}
				} else {
					var foreign model.Node
					if err := db.First(&foreign, foreignNodeID).Error; err != nil || foreign.SSHKeyID == nil || *foreign.SSHKeyID != key.ID {
						t.Fatalf("foreign node reference changed: %v", err)
					}
				}
			})
		})
	}
}

func TestConfigImportClassicRollbackRestoredDependencyMigrated(t *testing.T) {
	for _, scenario := range []string{"archived", "cycle"} {
		t.Run(scenario, func(t *testing.T) {
			retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
				setConfigHandlerTestEncryption(t)
				db := openMigratedRetryTransactionDB(t, engine)
				node := model.Node{Name: "receipt-dependency-node", Host: "127.0.0.1", Username: "root", BackupDir: "receipt-dependency-node"}
				if err := db.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				parent := model.Task{Name: "receipt-dependency-parent", NodeID: node.ID, ExecutorType: "command", Command: "true", Source: "manual"}
				if err := db.Create(&parent).Error; err != nil {
					t.Fatal(err)
				}
				child := model.Task{Name: "receipt-dependency-child", NodeID: node.ID, DependsOnTaskID: &parent.ID, ExecutorType: "command", Command: "old", Source: "manual"}
				if err := db.Create(&child).Error; err != nil {
					t.Fatal(err)
				}
				body := `{"tasks":[{"name":"receipt-dependency-child","node_name":"receipt-dependency-node","executor_type":"command","command":"imported"}],"system_settings":[{"key":"backup_assets.content_preview_ttl","value":"3m"}]}`
				serveClassicRollbackFailure(t, db, body, func() error {
					var detached model.Task
					if err := db.First(&detached, child.ID).Error; err != nil {
						return err
					}
					if detached.DependsOnTaskID != nil {
						return errors.New("task was not detached")
					}
					if scenario == "archived" {
						return db.Model(&model.Task{}).Where("id = ?", parent.ID).Update("archived_at", time.Now().UTC()).Error
					}
					return db.Model(&model.Task{}).Where("id = ?", parent.ID).Update("depends_on_task_id", child.ID).Error
				})
				var stored model.Task
				if err := db.First(&stored, child.ID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.Command != "imported" || stored.DependsOnTaskID != nil {
					t.Fatal("rollback restored an invalid dependency")
				}
			})
		})
	}
}
