package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"xirang/backend/internal/model"
	nodepkg "xirang/backend/internal/node"
	gormrepo "xirang/backend/internal/repository/gorm"
)

// Exercise the real compensation journal while its parent locks are still held
// by the surrounding transaction. The delete must observe the restored child.
func TestConfigImportRestoredReferencesAgainstDeletion(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, kind := range []string{"ssh-single", "ssh-batch", "credential", "node-single", "node-batch", "drill-single", "drill-batch"} {
			t.Run(engine+"/"+kind, func(t *testing.T) {
				setConfigHandlerTestEncryption(t)
				var holderDB, deleteDB *gorm.DB
				if engine == "sqlite" {
					holderDB, deleteDB = openConfigNameMappingSQLitePair(t, true)
				} else {
					holderDB, deleteDB = openConfigHandlerTestDBPairForEngine(t, engine)
				}
				migrateConfigAssetGraphDB(t, holderDB)
				if err := holderDB.AutoMigrate(&model.AppCredential{}, &model.Alert{}); err != nil {
					t.Fatal(err)
				}
				key := model.SSHKey{Name: "restore-delete-key", Username: "root", KeyType: "ed25519", PrivateKey: p1ImportPrivateKey(t)}
				if err := holderDB.Create(&key).Error; err != nil {
					t.Fatal(err)
				}
				credential := model.AppCredential{Name: "restore-delete-credential", Type: "mysql", Config: `{}`}
				if err := holderDB.Create(&credential).Error; err != nil {
					t.Fatal(err)
				}
				node := model.Node{Name: "restore-delete-node", Host: "127.0.0.1", BackupDir: "restore-delete-node", SSHKeyID: &key.ID}
				other := model.Node{Name: "restore-delete-other", Host: "127.0.0.2", BackupDir: "restore-delete-other"}
				if err := holderDB.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				if err := holderDB.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
				p := model.Policy{Name: "restore-delete-policy", SourcePath: "/data", TargetPath: "/backup", CronSpec: "0 2 * * *", AppProfile: "mysql", AppCredentialID: &credential.ID}
				if strings.HasPrefix(kind, "drill-") {
					p.DrillEnabled = true
					p.DrillTargetNodeID = &node.ID
					p.IsTemplate = true
				}
				if err := holderDB.Create(&p).Error; err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(kind, "drill-") {
					// A disabled policy can still be manually triggered or scanned for drills.
					if err := holderDB.Model(&p).Update("enabled", false).Error; err != nil {
						t.Fatal(err)
					}
					if err := holderDB.Create(&model.PolicyNode{PolicyID: p.ID, NodeID: node.ID}).Error; err != nil {
						t.Fatal(err)
					}
				}
				task := model.Task{Name: "restore-delete-task", NodeID: node.ID, ExecutorType: "command", Command: "true"}
				if err := holderDB.Create(&task).Error; err != nil {
					t.Fatal(err)
				}

				var data configImportData
				var entity string
				var rowID uint
				switch kind {
				case "ssh-single", "ssh-batch":
					data.Nodes = []map[string]interface{}{{"name": node.Name}}
					entity, rowID = configImportEntityNodes, node.ID
				case "credential", "drill-single", "drill-batch":
					data.Policies = []map[string]interface{}{{"name": p.Name}}
					entity, rowID = configImportEntityPolicies, p.ID
				default:
					data.Tasks = []map[string]interface{}{{"name": task.Name}}
					entity, rowID = configImportEntityTasks, task.ID
				}
				var snapshot *configImportRollbackSnapshot
				if err := holderDB.Transaction(func(tx *gorm.DB) error {
					var err error
					snapshot, err = captureConfigImportRollbackSnapshot(context.Background(), tx, data, nil)
					if err != nil {
						return err
					}
					switch kind {
					case "ssh-single", "ssh-batch":
						err = tx.Model(&model.Node{}).Where("id = ?", node.ID).Update("ssh_key_id", nil).Error
					case "credential":
						err = tx.Model(&model.Policy{}).Where("id = ?", p.ID).Update("app_credential_id", nil).Error
					case "drill-single", "drill-batch":
						err = tx.Model(&model.Policy{}).Where("id = ?", p.ID).Update("drill_target_node_id", nil).Error
					default:
						err = tx.Model(&model.Task{}).Where("id = ?", task.ID).Update("node_id", other.ID).Error
					}
					if err != nil {
						return err
					}
					accumulator := newConfigImportAccumulator()
					accumulator.recordWrite(entity, rowID)
					return snapshot.seal(context.Background(), tx, accumulator)
				}); err != nil {
					t.Fatal(err)
				}
				holder := holderDB.Begin()
				if holder.Error != nil {
					t.Fatal(holder.Error)
				}
				t.Cleanup(func() { _ = holder.Rollback().Error })
				var holderPID int
				if engine == "postgres" {
					if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
						t.Fatal(err)
					}
				}
				journal := newConfigImportRollbackJournal(holder, nil)
				journal.install(snapshot)
				if err := journal.Restore(context.Background()); err != nil {
					t.Fatalf("restore in holder transaction: %v", err)
				}

				request := func(db *gorm.DB) *httptest.ResponseRecorder {
					router := gin.New()
					router.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
					var method, path, body string
					switch kind {
					case "ssh-single":
						router.DELETE("/ssh-keys/:id", NewSSHKeyHandler(db, nil).Delete)
						method, path = http.MethodDelete, fmt.Sprintf("/ssh-keys/%d", key.ID)
					case "ssh-batch":
						router.POST("/ssh-keys/batch-delete", NewSSHKeyHandler(db, nil).BatchDelete)
						method, path, body = http.MethodPost, "/ssh-keys/batch-delete", fmt.Sprintf(`{"ids":[%d,%d]}`, key.ID, key.ID)
					case "credential":
						router.DELETE("/app-credentials/:id", NewAppCredentialHandler(db).Delete)
						method, path = http.MethodDelete, fmt.Sprintf("/app-credentials/%d", credential.ID)
					default:
						h := NewNodeHandler(db, nil, nodepkg.NewNodeService(gormrepo.NewNodeRepository(db)))
						if kind == "node-single" || kind == "drill-single" {
							router.DELETE("/nodes/:id", h.Delete)
							method, path = http.MethodDelete, fmt.Sprintf("/nodes/%d", node.ID)
						} else {
							router.POST("/nodes/batch-delete", h.BatchDelete)
							method, path, body = http.MethodPost, "/nodes/batch-delete", fmt.Sprintf(`{"ids":[%d,%d]}`, node.ID, node.ID)
							if kind == "drill-batch" {
								body = fmt.Sprintf(`{"ids":[%d,%d]}`, node.ID, other.ID)
							}
						}
					}
					req := httptest.NewRequest(method, path, strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, req)
					return response
				}
				var response *httptest.ResponseRecorder
				if engine == "sqlite" {
					busy := request(deleteDB)
					if busy.Code != http.StatusInternalServerError {
						t.Fatalf("delete while SQLite writer held: status=%d body=%s", busy.Code, busy.Body.String())
					}
					if err := holder.Commit().Error; err != nil {
						t.Fatal(err)
					}
					response = request(deleteDB)
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					ready := make(chan int, 1)
					type deleteResult struct {
						response *httptest.ResponseRecorder
						err      error
					}
					done := make(chan deleteResult, 1)
					go func() {
						var result deleteResult
						result.err = deleteDB.WithContext(ctx).Connection(func(conn *gorm.DB) error {
							if err := conn.Exec("SET default_transaction_isolation = 'repeatable read'").Error; err != nil {
								return err
							}
							var pid int
							if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
								return err
							}
							ready <- pid
							result.response = request(conn)
							return nil
						})
						done <- result
					}()
					var waiterPID int
					select {
					case waiterPID = <-ready:
					case result := <-done:
						t.Fatalf("delete exited before lock: response=%v err=%v", result.response, result.err)
					case <-ctx.Done():
						t.Fatal("delete did not start")
					}
					sqlDB, err := holderDB.DB()
					if err != nil {
						t.Fatal(err)
					}
					waitConfigNameMappingPostgresBlock(t, sqlDB, waiterPID, holderPID, kind+" deletion")
					if err := holder.Commit().Error; err != nil {
						t.Fatal(err)
					}
					select {
					case result := <-done:
						if result.err != nil {
							t.Fatalf("delete connection failed: %v", result.err)
						}
						response = result.response
					case <-ctx.Done():
						t.Fatal("delete did not finish")
					}
				}
				want := http.StatusOK
				switch kind {
				case "ssh-single":
					want = http.StatusBadRequest
				case "credential", "drill-single", "drill-batch":
					want = http.StatusConflict
				}
				if response.Code != want {
					t.Fatalf("delete after restoration: status=%d want=%d body=%s", response.Code, want, response.Body.String())
				}
				if kind == "ssh-batch" || kind == "node-batch" {
					var envelope struct {
						Data struct {
							Deleted int      `json:"deleted"`
							Skipped []string `json:"skipped_in_use"`
						} `json:"data"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
						t.Fatal(err)
					}
					if kind == "ssh-batch" && (envelope.Data.Deleted != 0 || len(envelope.Data.Skipped) != 1 || envelope.Data.Skipped[0] != key.Name) {
						t.Fatalf("batch key result: %s", response.Body.String())
					}
					if kind == "node-batch" && envelope.Data.Deleted != 1 {
						t.Fatalf("batch node result: %s", response.Body.String())
					}
				}
				if strings.HasPrefix(kind, "drill-") {
					var current model.Policy
					if err := holderDB.First(&current, p.ID).Error; err != nil || current.DrillTargetNodeID == nil || *current.DrillTargetNodeID != node.ID {
						t.Fatalf("restored drill target changed: %+v err=%v", current.DrillTargetNodeID, err)
					}
					for _, check := range []struct {
						model any
						where string
						id    uint
					}{
						{&model.Node{}, "id = ?", node.ID},
						{&model.Node{}, "id = ?", other.ID},
						{&model.Task{}, "id = ?", task.ID},
						{&model.PolicyNode{}, "node_id = ?", node.ID},
					} {
						var count int64
						if err := holderDB.Model(check.model).Where(check.where, check.id).Count(&count).Error; err != nil || count != 1 {
							t.Fatalf("rejected deletion changed %T: count=%d err=%v", check.model, count, err)
						}
					}
				} else if strings.HasPrefix(kind, "node-") {
					var count int64
					if err := holderDB.Model(&model.Task{}).Where("id = ?", task.ID).Count(&count).Error; err != nil || count != 0 {
						t.Fatalf("restored task not removed with node: count=%d err=%v", count, err)
					}
				} else {
					var count int64
					var err error
					if kind == "credential" {
						err = holderDB.Model(&model.AppCredential{}).Where("id = ?", credential.ID).Count(&count).Error
					} else {
						err = holderDB.Model(&model.SSHKey{}).Where("id = ?", key.ID).Count(&count).Error
					}
					if err != nil || count != 1 {
						t.Fatalf("referenced parent removed: count=%d err=%v", count, err)
					}
				}

				// Repeat the import, but let deletion commit first. Compensation must
				// reject the missing parent without restoring a dangling reference.
				if strings.HasPrefix(kind, "node-") {
					if err := holderDB.Create(&node).Error; err != nil {
						t.Fatal(err)
					}
					if err := holderDB.Create(&task).Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := holderDB.Transaction(func(tx *gorm.DB) error {
					var err error
					snapshot, err = captureConfigImportRollbackSnapshot(context.Background(), tx, data, nil)
					if err != nil {
						return err
					}
					switch kind {
					case "ssh-single", "ssh-batch":
						err = tx.Model(&model.Node{}).Where("id = ?", node.ID).Update("ssh_key_id", nil).Error
					case "credential":
						err = tx.Model(&model.Policy{}).Where("id = ?", p.ID).Update("app_credential_id", nil).Error
					case "drill-single", "drill-batch":
						err = tx.Model(&model.Policy{}).Where("id = ?", p.ID).Update("drill_target_node_id", nil).Error
					default:
						err = tx.Model(&model.Task{}).Where("id = ?", task.ID).Update("node_id", other.ID).Error
					}
					if err != nil {
						return err
					}
					accumulator := newConfigImportAccumulator()
					accumulator.recordWrite(entity, rowID)
					return snapshot.seal(context.Background(), tx, accumulator)
				}); err != nil {
					t.Fatal(err)
				}
				response = request(deleteDB)
				if response.Code != http.StatusOK {
					t.Fatalf("delete before restoration: status=%d body=%s", response.Code, response.Body.String())
				}
				journal = newConfigImportRollbackJournal(holderDB, nil)
				journal.install(snapshot)
				if err := journal.Restore(context.Background()); err == nil {
					t.Fatal("compensation restored a reference to a deleted parent")
				}
				switch kind {
				case "ssh-single", "ssh-batch":
					var current model.Node
					if err := holderDB.First(&current, node.ID).Error; err != nil || current.SSHKeyID != nil {
						t.Fatalf("node reference changed after rejected compensation: %+v err=%v", current.SSHKeyID, err)
					}
				case "credential":
					var current model.Policy
					if err := holderDB.First(&current, p.ID).Error; err != nil || current.AppCredentialID != nil {
						t.Fatalf("policy reference changed after rejected compensation: %+v err=%v", current.AppCredentialID, err)
					}
				case "drill-single", "drill-batch":
					var current model.Policy
					if err := holderDB.First(&current, p.ID).Error; err != nil || current.DrillTargetNodeID != nil {
						t.Fatalf("drill reference changed after rejected compensation: %+v err=%v", current.DrillTargetNodeID, err)
					}
				default:
					var current model.Task
					if err := holderDB.First(&current, task.ID).Error; err != nil || current.NodeID != other.ID {
						t.Fatalf("task reference changed after rejected compensation: %d err=%v", current.NodeID, err)
					}
				}
				response = request(deleteDB)
				want = http.StatusOK
				switch kind {
				case "credential", "node-single", "drill-single":
					want = http.StatusNotFound
				}
				if response.Code != want {
					t.Fatalf("repeat delete: status=%d want=%d body=%s", response.Code, want, response.Body.String())
				}
				if kind == "ssh-batch" || kind == "node-batch" || kind == "drill-batch" {
					var envelope struct {
						Data struct {
							Deleted     int    `json:"deleted"`
							NotFoundIDs []uint `json:"not_found_ids"`
						} `json:"data"`
					}
					if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
						t.Fatal(err)
					}
					if envelope.Data.Deleted != 0 || (kind == "node-batch" && (len(envelope.Data.NotFoundIDs) != 1 || envelope.Data.NotFoundIDs[0] != node.ID)) || (kind == "drill-batch" && (len(envelope.Data.NotFoundIDs) != 2 || envelope.Data.NotFoundIDs[0] != node.ID || envelope.Data.NotFoundIDs[1] != other.ID)) {
						t.Fatalf("repeat batch deletion result: %s", response.Body.String())
					}
				}
			})
		}
	}
}

func TestNodeDeletionAllowsDisabledDrillTarget(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, batch := range []bool{false, true} {
			name := "single"
			if batch {
				name = "batch"
			}
			t.Run(engine+"/"+name, func(t *testing.T) {
				setConfigHandlerTestEncryption(t)
				db, _ := openConfigHandlerTestDBPairForEngine(t, engine)
				migrateConfigAssetGraphDB(t, db)
				if err := db.AutoMigrate(&model.Alert{}); err != nil {
					t.Fatal(err)
				}
				node := model.Node{Name: "disabled-drill-target", Host: "127.0.0.1", BackupDir: "disabled-drill-target"}
				if err := db.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				p := model.Policy{Name: "disabled-drill", SourcePath: "/data", TargetPath: "/backup", CronSpec: "0 2 * * *", DrillTargetNodeID: &node.ID}
				if err := db.Create(&p).Error; err != nil {
					t.Fatal(err)
				}
				h := NewNodeHandler(db, nil, nodepkg.NewNodeService(gormrepo.NewNodeRepository(db)))
				router := gin.New()
				router.Use(func(c *gin.Context) { c.Set("role", "admin"); c.Next() })
				var method, path, body string
				if batch {
					router.POST("/nodes/batch-delete", h.BatchDelete)
					method, path, body = http.MethodPost, "/nodes/batch-delete", fmt.Sprintf(`{"ids":[%d]}`, node.ID)
				} else {
					router.DELETE("/nodes/:id", h.Delete)
					method, path = http.MethodDelete, fmt.Sprintf("/nodes/%d", node.ID)
				}
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				if response.Code != http.StatusOK {
					t.Fatalf("delete disabled drill target: status=%d body=%s", response.Code, response.Body.String())
				}
				var count int64
				if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("target node still exists: count=%d err=%v", count, err)
				}
				var current model.Policy
				if err := db.First(&current, p.ID).Error; err != nil || current.DrillEnabled || current.DrillTargetNodeID == nil || *current.DrillTargetNodeID != node.ID {
					t.Fatalf("disabled legacy reference changed: %+v err=%v", current.DrillTargetNodeID, err)
				}
			})
		}
	}
}

func TestNodeDeletionTaskLockDoesNotDeadlockRetry(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		for _, batch := range []bool{false, true} {
			name := "single"
			if batch {
				name = "batch"
			}
			t.Run(engine+"/"+name, func(t *testing.T) {
				setConfigHandlerTestEncryption(t)
				var holderDB, deleteDB *gorm.DB
				if engine == "sqlite" {
					holderDB, deleteDB = openConfigNameMappingSQLitePair(t, true)
				} else {
					holderDB, deleteDB = openConfigHandlerTestDBPairForEngine(t, engine)
				}
				migrateP1ConfigImport(t, holderDB)
				if err := holderDB.AutoMigrate(&model.Alert{}); err != nil {
					t.Fatal(err)
				}
				node := model.Node{Name: "delete-task-lock-node", Host: "127.0.0.1", BackupDir: "delete-task-lock"}
				if err := holderDB.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				task := model.Task{Name: "delete-task-lock-task", NodeID: node.ID, ExecutorType: "command", Command: "true"}
				if err := holderDB.Create(&task).Error; err != nil {
					t.Fatal(err)
				}
				holder := holderDB.Begin()
				if holder.Error != nil {
					t.Fatal(holder.Error)
				}
				t.Cleanup(func() { _ = holder.Rollback().Error })
				if engine == "postgres" {
					var locked model.Task
					if err := holder.Raw("SELECT id FROM tasks WHERE id = ? FOR UPDATE", task.ID).Scan(&locked).Error; err != nil {
						t.Fatal(err)
					}
				} else if err := holder.Exec("UPDATE tasks SET id = id WHERE id = ?", task.ID).Error; err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				deleteNode := func() error {
					repo := gormrepo.NewNodeRepository(deleteDB)
					if batch {
						_, _, err := repo.BatchDeleteWithAssociations(ctx, []uint{node.ID})
						return err
					}
					return repo.DeleteWithAssociations(ctx, node.ID)
				}
				done := make(chan error, 1)
				go func() { done <- deleteNode() }()
				select {
				case err := <-done:
					if engine == "postgres" {
						var pgErr *pgconn.PgError
						if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
							t.Fatalf("task lock should fail NOWAIT: %v", err)
						}
					} else if !configNameMappingSQLiteBusyLockedError(err) {
						t.Fatalf("SQLite writer should report BUSY/LOCKED: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("delete waited for a task held before its node")
				}
				if err := holder.Commit().Error; err != nil {
					t.Fatal(err)
				}
				var count int64
				if err := holderDB.Model(&model.Task{}).Where("id = ?", task.ID).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("failed delete changed task: count=%d err=%v", count, err)
				}
				if err := deleteNode(); err != nil {
					t.Fatalf("delete after releasing task lock: %v", err)
				}
			})
		}
	}
}
