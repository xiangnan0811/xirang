package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"xirang/backend/internal/apperr"
	"xirang/backend/internal/model"
	nodepkg "xirang/backend/internal/node"
	policyPkg "xirang/backend/internal/policy"
	"xirang/backend/internal/repository"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"
	taskpkg "xirang/backend/internal/task"

	"gorm.io/gorm"
)

// removeNodeAfterPreflight models compensation committing between the service's
// initial validation and its write transaction.
type removeNodeAfterPreflight struct {
	repository.NodeRepository
	db *gorm.DB
}

func (r *removeNodeAfterPreflight) ExistsByID(ctx context.Context, id uint) (bool, error) {
	exists, err := r.NodeRepository.ExistsByID(ctx, id)
	if err != nil || !exists {
		return exists, err
	}
	if err := r.db.WithContext(ctx).Delete(&model.Node{}, id).Error; err != nil {
		return false, err
	}
	return true, nil
}

type removeKeyAfterPreflight struct {
	repository.NodeRepository
	db *gorm.DB
}

func (r *removeKeyAfterPreflight) FindSSHKeyByID(ctx context.Context, id uint) (*model.SSHKey, error) {
	key, err := r.NodeRepository.FindSSHKeyByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Delete(&model.SSHKey{}, id).Error; err != nil {
		return nil, err
	}
	return key, nil
}

type restoreTemplateBeforeClone struct {
	repository.PolicyRepository
	db         *gorm.DB
	templateID uint
	nodeID     uint
}

func (r *restoreTemplateBeforeClone) Transaction(ctx context.Context, fn func(repository.PolicyRepository) error) error {
	if err := r.db.WithContext(ctx).Model(&model.Policy{}).Where("id = ?", r.templateID).Update("drill_target_node_id", nil).Error; err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Delete(&model.Node{}, r.nodeID).Error; err != nil {
		return err
	}
	return r.PolicyRepository.Transaction(ctx, fn)
}

func TestConfigImportReferenceWritersRejectRemovedParents(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := openR306DB(t, engine, &model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{}, &model.BatchCommand{}, &model.BatchCommandDispatch{})
			ctx := context.Background()
			nodes := gormrepo.NewNodeRepository(db)
			key := model.SSHKey{Name: "removed-key", Username: "root", PrivateKey: "test key", Fingerprint: "test"}
			if err := db.Create(&key).Error; err != nil {
				t.Fatal(err)
			}
			nodeService := nodepkg.NewNodeService(&removeKeyAfterPreflight{NodeRepository: nodes, db: db})
			_, err := nodeService.Create(ctx, nodepkg.CreateNodeInput{Name: "stale-key", Host: "node.example", Username: "root", AuthType: "key", SSHKeyID: &key.ID})
			if !errors.Is(err, apperr.ErrValidation) {
				t.Fatalf("node with removed preflight key: %v", err)
			}
			missingKey := uint(987654)
			candidate := &model.Node{Name: "missing-key", Host: "node.example", Username: "root", BackupDir: "missing-key", SSHKeyID: &missingKey}
			if err := nodes.Create(ctx, candidate); !errors.Is(err, repository.ErrNodeSSHKeyNotFound) {
				t.Fatalf("create with removed key: %v", err)
			}
			current := &model.Node{Name: "update-key", Host: "node.example", Username: "root", BackupDir: "update-key"}
			if err := nodes.Create(ctx, current); err != nil {
				t.Fatal(err)
			}
			current.SSHKeyID = &missingKey
			if err := nodes.Update(ctx, current, false); !errors.Is(err, repository.ErrNodeSSHKeyNotFound) {
				t.Fatalf("update with removed key: %v", err)
			}
			var stored model.Node
			if err := db.First(&stored, current.ID).Error; err != nil || stored.SSHKeyID != nil {
				t.Fatalf("update persisted missing key: %+v, %v", stored.Sanitized(), err)
			}

			victim := model.Node{Name: "removed-node", Host: "node.example", Username: "root", BackupDir: "removed-node"}
			if err := db.Create(&victim).Error; err != nil {
				t.Fatal(err)
			}
			service := taskpkg.NewTaskApiService(gormrepo.NewTaskRepository(db), &removeNodeAfterPreflight{NodeRepository: nodes, db: db}, gormrepo.NewPolicyRepository(db), nil)
			_, err = service.CreateTask(ctx, taskpkg.CreateTaskInput{Name: "stale-task", NodeID: victim.ID, ExecutorType: "command", Command: "true"})
			if !taskpkg.IsTaskValidationError(err) {
				t.Fatalf("task with removed node: %v", err)
			}
			var count int64
			if err := db.Model(&model.Task{}).Where("node_id = ?", victim.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("stale tasks=%d, error=%v", count, err)
			}

			handler := NewBatchHandler(db, nil)
			req := batchCommandRequest{NodeIDs: []uint{victim.ID}, Command: "true"}
			_, _, _, err = handler.createBatchTasks(ctx, 1, "missing-node", req, []batchNode{{ID: victim.ID, Name: "removed"}})
			var missing *batchNodeMissingError
			if !errors.As(err, &missing) || missing.id != victim.ID {
				t.Fatalf("batch with removed node: %v", err)
			}
			if err := db.Model(&model.BatchCommand{}).Where("idempotency_key = ?", "missing-node").Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("rolled-back batches=%d, error=%v", count, err)
			}
			if err := referenceWriteTransaction(db, func(tx *gorm.DB) error { return lockPolicyReferences(tx, []uint{victim.ID}, nil, false) }); err == nil {
				t.Fatal("policy node validation accepted removed node")
			}

			goodReq := batchCommandRequest{NodeIDs: []uint{current.ID}, Command: "true"}
			first, firstDispatches, created, err := handler.createBatchTasks(ctx, 1, "replay", goodReq, []batchNode{{ID: current.ID, Name: current.Name}})
			if err != nil || !created || len(firstDispatches) != 1 {
				t.Fatalf("create batch: created=%v dispatches=%d err=%v", created, len(firstDispatches), err)
			}
			replayed, dispatches, created, err := handler.createBatchTasks(ctx, 1, "replay", goodReq, []batchNode{{ID: victim.ID}})
			if err != nil || created || replayed.ID != first.ID || len(dispatches) != 1 || dispatches[0].TaskID != firstDispatches[0].TaskID {
				t.Fatalf("batch replay changed: created=%v batch=%q dispatches=%v err=%v", created, replayed.ID, dispatches, err)
			}
		})
	}
}

func TestConfigImportReferenceCloneRefreshesTemplate(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := openR306DB(t, engine, &model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.PolicyNode{})
			ctx := context.Background()
			drillNode := model.Node{Name: "drill-node", Host: "node.example", Username: "root", BackupDir: "drill-node"}
			if err := db.Create(&drillNode).Error; err != nil {
				t.Fatal(err)
			}
			template := model.Policy{Name: "template", IsTemplate: true, DrillTargetNodeID: &drillNode.ID}
			if err := db.Create(&template).Error; err != nil {
				t.Fatal(err)
			}
			policyRepo := gormrepo.NewPolicyRepository(db)
			cloneService := policyPkg.NewPolicyService(&restoreTemplateBeforeClone{PolicyRepository: policyRepo, db: db, templateID: template.ID, nodeID: drillNode.ID}, nil)
			clone, err := cloneService.CloneFromTemplate(ctx, template.ID)
			if err != nil || clone.DrillTargetNodeID != nil || clone.Enabled || clone.DrillEnabled {
				t.Fatalf("clone retained undone target or changed disabled state: %+v, %v", clone, err)
			}
			legacyTarget := uint(987654)
			legacy := model.Policy{Name: "legacy-template", IsTemplate: true, DrillTargetNodeID: &legacyTarget}
			if err := db.Create(&legacy).Error; err != nil {
				t.Fatal(err)
			}
			legacyClone, err := policyPkg.NewPolicyService(policyRepo, nil).CloneFromTemplate(ctx, legacy.ID)
			if err != nil || legacyClone.DrillTargetNodeID == nil || *legacyClone.DrillTargetNodeID != legacyTarget || legacyClone.DrillEnabled {
				t.Fatalf("legacy disabled clone changed: %+v, %v", legacyClone, err)
			}
		})
	}
}

func TestConfigImportReferenceLegacyDisabledDrillUpdate(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := openR306DB(t, engine, &model.SSHKey{}, &model.Node{}, &model.Policy{}, &model.PolicyNode{}, &model.Task{})
			missing := uint(987654)
			entity := model.Policy{Name: "legacy-disabled", SourcePath: "/data", CronSpec: "0 2 * * *", DrillTargetNodeID: &missing}
			if err := db.Create(&entity).Error; err != nil {
				t.Fatal(err)
			}
			router := r306AdminRouter()
			router.PUT("/policies/:id", NewPolicyHandler(db, nil).Update)
			path := fmt.Sprintf("/policies/%d", entity.ID)
			body := map[string]any{"name": entity.Name, "source_path": "/data", "cron_spec": "0 2 * * *"}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, r306JSONRequest(t, http.MethodPut, path, body))
			if response.Code != http.StatusOK {
				t.Fatalf("unchanged disabled target: status=%d body=%s", response.Code, response.Body.String())
			}
			var updated model.Policy
			if err := db.First(&updated, entity.ID).Error; err != nil || updated.DrillTargetNodeID == nil || *updated.DrillTargetNodeID != missing {
				t.Fatalf("legacy target was not retained: %+v, %v", updated, err)
			}
			body["drill_target_node_id"] = missing
			response = httptest.NewRecorder()
			router.ServeHTTP(response, r306JSONRequest(t, http.MethodPut, path, body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("explicit missing target: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
