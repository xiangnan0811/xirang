package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestResolveBackupFileSourceRecoveryPointReturnsExactAuthorizedCoordinates(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}

	resolved, err := service.ResolveFileSourceRecoveryPoint(context.Background(), fixture.ownedPointID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.NodeID == 0 || backupasset.ValidateOpaqueID(resolved.BackupSetID) != nil ||
		resolved.RecoveryPointID != fixture.ownedPointID || resolved.RepositoryID == "" || resolved.ProducingTaskID == nil {
		t.Fatalf("resolution=%+v", resolved)
	}
	sets, err := service.ListFileSourceBackupSets(context.Background(), resolved.NodeID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !containsFileSourceSet(sets.Items, resolved.BackupSetID, resolved.NodeID) {
		t.Fatalf("resolved Backup Set is not in exact node projection: resolution=%+v sets=%+v", resolved, sets)
	}
	versions, err := service.ListFileSourceVersions(context.Background(), resolved.BackupSetID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !containsFileSourceVersion(versions.Items, resolved.RecoveryPointID, resolved.RepositoryID, resolved.ProducingTaskID) {
		t.Fatalf("resolved version is not in exact Backup Set projection: resolution=%+v versions=%+v", resolved, versions)
	}

	payload, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"provider", "locator", "path", "content", "credential", "token", "proof", "lineage", "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY",
	} {
		if strings.Contains(strings.ToLower(string(payload)), strings.ToLower(forbidden)) {
			t.Fatalf("resolution leaked %q: %s", forbidden, payload)
		}
	}
}

func TestResolveBackupFileSourceRecoveryPointOmitsTaskForTasklessImportedLineage(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 8, 5, 0, 0, time.UTC)
	node := seedCatalogOwnershipNode(t, db, 6201, "taskless-source", now)
	repository := seedCatalogOwnershipRepository(t, db, strings.Repeat("7", 32), now)
	point := seedFileSourceTasklessPoint(t, db, 72, node, repository, now)
	service := newCatalogServiceForTest(t, db, now)

	resolved, err := service.ResolveFileSourceRecoveryPoint(context.Background(), point.ID, AuthorizationScope{Role: "admin", UserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.NodeID != node.ID || resolved.RecoveryPointID != point.ID || resolved.RepositoryID != repository.ID ||
		resolved.ProducingTaskID != nil || backupasset.ValidateOpaqueID(resolved.BackupSetID) != nil {
		t.Fatalf("taskless resolution=%+v", resolved)
	}
	payload, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "producing_task_id") {
		t.Fatalf("taskless resolution serialized an absent task: %s", payload)
	}
}

func TestResolveBackupFileSourceRecoveryPointHidesStaleAndUnauthorizedPoints(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 8, 10, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}

	for _, pointID := range []string{fixture.unownedPointID, fixture.archivedPointID, strings.Repeat("f", 32), "not-an-opaque-id"} {
		resolved, err := service.ResolveFileSourceRecoveryPoint(context.Background(), pointID, scope)
		if !errors.Is(err, backupasset.ErrNotFound) || resolved != (FileSourceRecoveryPointDTO{}) {
			t.Fatalf("point=%q resolution=%+v err=%v", pointID, resolved, err)
		}
	}
}

func TestResolveBackupFileSourceRecoveryPointFailsClosedOnDuplicateProjection(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 8, 20, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	var point model.RecoveryPoint
	if err := db.Where("id = ?", fixture.ownedPointID).Take(&point).Error; err != nil {
		t.Fatal(err)
	}
	service := newCatalogServiceForTest(t, db, now)

	if _, err := service.composeFileSourceProjection(context.Background(), []model.RecoveryPoint{point, point}); !errors.Is(err, ErrIdentityCollision) {
		t.Fatalf("duplicate recovery-point projection error=%v", err)
	}
}

func containsFileSourceSet(items []FileSourceBackupSetDTO, setID string, nodeID uint) bool {
	for _, item := range items {
		if item.BackupSetID == setID && item.NodeID == nodeID {
			return true
		}
	}
	return false
}

func containsFileSourceVersion(items []FileSourceVersionDTO, pointID, repositoryID string, taskID *uint) bool {
	for _, item := range items {
		if item.RecoveryPointID == pointID && item.RepositoryID == repositoryID &&
			((item.ProducingTaskID == nil && taskID == nil) || (item.ProducingTaskID != nil && taskID != nil && *item.ProducingTaskID == *taskID)) {
			return true
		}
	}
	return false
}

func TestBackupFileSourceProjectionGroupsOneTaskAcrossRepositories(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)

	var original model.RecoveryPoint
	if err := db.Where("id = ?", fixture.ownedPointID).Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	if original.ProducingTaskID == nil {
		t.Fatal("owned fixture point has no producing task")
	}
	if err := db.Where("id <> ?", original.ID).Delete(&model.RecoveryPoint{}).Error; err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := db.Where("id = ?", *original.ProducingTaskID).Take(&task).Error; err != nil {
		t.Fatal(err)
	}
	unlinkedAt := now.Add(30 * time.Second)
	if err := db.Model(&model.TaskRepositoryLink{}).Where("task_id = ?", task.ID).
		Update("unlinked_at", unlinkedAt).Error; err != nil {
		t.Fatal(err)
	}
	secondRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("8", 32), now)
	secondLink := seedCatalogOwnershipLink(t, db, strings.Repeat("9", 32), secondRepository.ID, task.ID, nil, now)
	secondRun := seedCatalogOwnershipRun(t, db, task.ID, now.Add(time.Minute))
	secondPoint := seedFileSourceTaskPoint(t, db, 81, secondRepository, task, secondRun, secondLink, backupasset.PointNativeSnapshot, "", now.Add(time.Minute))

	service := newCatalogServiceForTest(t, db, now.Add(2*time.Minute))
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	sets, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets.Items) != 1 || sets.Items[0].NodeID != task.NodeID || sets.Items[0].LineageKind != FileSourceLineageTask || sets.Items[0].VersionCount != 2 {
		t.Fatalf("backup sets=%+v", sets)
	}
	versions, err := service.ListFileSourceVersions(context.Background(), sets.Items[0].BackupSetID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(versions.Items) != 2 || versions.Items[0].RecoveryPointID != secondPoint.ID || versions.Items[1].RecoveryPointID != original.ID {
		t.Fatalf("versions=%+v", versions)
	}
}

func TestBackupFileSourceProjectionKeepsTaskHistoryIsolatedByExactProducingNode(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 9, 30, 0, 0, time.UTC)
	firstNode := seedCatalogOwnershipNode(t, db, 6251, "task-move-first", now)
	secondNode := seedCatalogOwnershipNode(t, db, 6252, "task-move-second", now)
	task := seedCatalogOwnershipTask(t, db, firstNode.ID, "moved-task", now)
	firstRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("6", 32), now)
	firstLink := seedCatalogOwnershipLink(t, db, strings.Repeat("7", 32), firstRepository.ID, task.ID, nil, now)
	firstRun := seedCatalogOwnershipRun(t, db, task.ID, now)
	firstPoint := seedFileSourceTaskPoint(t, db, 88, firstRepository, task, firstRun, firstLink, backupasset.PointNativeSnapshot, "", now)

	unlinkedAt := now.Add(30 * time.Second)
	if err := db.Model(&model.TaskRepositoryLink{}).Where("id = ?", firstLink.ID).Update("unlinked_at", unlinkedAt).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("node_id", secondNode.ID).Error; err != nil {
		t.Fatal(err)
	}
	task.NodeID = secondNode.ID
	secondRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("8", 32), now)
	secondLink := seedCatalogOwnershipLink(t, db, strings.Repeat("9", 32), secondRepository.ID, task.ID, nil, now.Add(time.Minute))
	secondRun := seedCatalogOwnershipRun(t, db, task.ID, now.Add(time.Minute))
	secondPoint := seedFileSourceTaskPoint(t, db, 89, secondRepository, task, secondRun, secondLink, backupasset.PointNativeSnapshot, "", now.Add(time.Minute))

	service := newCatalogServiceForTest(t, db, now.Add(2*time.Minute))
	scope := AuthorizationScope{Role: "admin", UserID: 1}
	nodes, err := service.ListFileSourceNodes(context.Background(), scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(nodes.Items) != 2 || nodes.Items[0].NodeID != firstNode.ID || nodes.Items[1].NodeID != secondNode.ID {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	firstSets, err := service.ListFileSourceBackupSets(context.Background(), firstNode.ID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(firstSets.Items) != 1 || firstSets.Items[0].LineageKind != FileSourceLineageTask || firstSets.Items[0].VersionCount != 1 {
		t.Fatalf("first node sets=%+v err=%v", firstSets, err)
	}
	secondSets, err := service.ListFileSourceBackupSets(context.Background(), secondNode.ID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(secondSets.Items) != 1 || secondSets.Items[0].LineageKind != FileSourceLineageTask || secondSets.Items[0].VersionCount != 1 ||
		secondSets.Items[0].BackupSetID == firstSets.Items[0].BackupSetID {
		t.Fatalf("second node sets=%+v err=%v", secondSets, err)
	}
	firstVersions, err := service.ListFileSourceVersions(context.Background(), firstSets.Items[0].BackupSetID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(firstVersions.Items) != 1 || firstVersions.Items[0].RecoveryPointID != firstPoint.ID {
		t.Fatalf("first versions=%+v err=%v", firstVersions, err)
	}
	secondVersions, err := service.ListFileSourceVersions(context.Background(), secondSets.Items[0].BackupSetID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(secondVersions.Items) != 1 || secondVersions.Items[0].RecoveryPointID != secondPoint.ID {
		t.Fatalf("second versions=%+v err=%v", secondVersions, err)
	}
}

func TestBackupFileSourceSetCursorPaginatesWithoutDuplicates(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}

	var ownedPoint model.RecoveryPoint
	if err := db.Select("producing_task_id").Where("id = ?", fixture.ownedPointID).Take(&ownedPoint).Error; err != nil {
		t.Fatal(err)
	}
	if ownedPoint.ProducingTaskID == nil {
		t.Fatal("owned fixture point has no producing task")
	}
	var task model.Task
	if err := db.Select("node_id").Where("id = ?", *ownedPoint.ProducingTaskID).Take(&task).Error; err != nil {
		t.Fatal(err)
	}

	first, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first page=%+v", first)
	}
	second, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{
		Limit: 1, Cursor: first.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.NextCursor != "" || second.Items[0].BackupSetID == first.Items[0].BackupSetID {
		t.Fatalf("second page=%+v first=%+v", second, first)
	}
}

func TestBackupFileSourceNodeCursorPaginatesWithoutDuplicates(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 11, 0, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	secondNode := seedCatalogOwnershipNode(t, db, 6301, "second-owned", now)
	if err := db.Create(&model.NodeOwner{NodeID: secondNode.ID, UserID: fixture.operatorID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	secondTask := seedCatalogOwnershipTask(t, db, secondNode.ID, "second-owned-task", now)
	secondRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("a", 32), now)
	secondLink := seedCatalogOwnershipLink(t, db, strings.Repeat("b", 32), secondRepository.ID, secondTask.ID, nil, now)
	secondRun := seedCatalogOwnershipRun(t, db, secondTask.ID, now)
	seedFileSourceTaskPoint(t, db, 82, secondRepository, secondTask, secondRun, secondLink, backupasset.PointNativeSnapshot, "", now)

	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	first, err := service.ListFileSourceNodes(context.Background(), scope, FileSourcePageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first page=%+v", first)
	}
	if first.Items[0].BackupSetCount != 2 {
		t.Fatalf("first node page count=%d, want full projection count 2", first.Items[0].BackupSetCount)
	}
	second, err := service.ListFileSourceNodes(context.Background(), scope, FileSourcePageRequest{Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.NextCursor != "" || second.Items[0].NodeID == first.Items[0].NodeID {
		t.Fatalf("second page=%+v first=%+v", second, first)
	}
	if second.Items[0].BackupSetCount != 1 {
		t.Fatalf("second node page count=%d, want full projection count 1", second.Items[0].BackupSetCount)
	}
}

func TestBackupFileSourceVersionCursorPaginatesWithoutDuplicates(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)

	var original model.RecoveryPoint
	if err := db.Where("id = ?", fixture.ownedPointID).Take(&original).Error; err != nil {
		t.Fatal(err)
	}
	if original.ProducingTaskID == nil {
		t.Fatal("owned fixture point has no producing task")
	}
	var task model.Task
	if err := db.Where("id = ?", *original.ProducingTaskID).Take(&task).Error; err != nil {
		t.Fatal(err)
	}
	var repository model.BackupRepository
	if err := db.Where("id = ?", original.RepositoryID).Take(&repository).Error; err != nil {
		t.Fatal(err)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ?", task.ID).Take(&link).Error; err != nil {
		t.Fatal(err)
	}
	newerRun := seedCatalogOwnershipRun(t, db, task.ID, now.Add(time.Minute))
	newer := seedFileSourceTaskPoint(t, db, 83, repository, task, newerRun, link, backupasset.PointNativeSnapshot, "", now.Add(time.Minute))

	service := newCatalogServiceForTest(t, db, now.Add(2*time.Minute))
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	sets, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var backupSetID string
	for _, set := range sets.Items {
		if set.DisplayLabel == task.Name {
			backupSetID = set.BackupSetID
			if set.VersionCount != 2 {
				t.Fatalf("task Backup Set version count=%d, want full projection count 2", set.VersionCount)
			}
			break
		}
	}
	if backupSetID == "" {
		t.Fatalf("task Backup Set missing: %+v", sets)
	}
	first, err := service.ListFileSourceVersions(context.Background(), backupSetID, scope, FileSourcePageRequest{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].RecoveryPointID != newer.ID || first.NextCursor == "" {
		t.Fatalf("first page=%+v", first)
	}
	second, err := service.ListFileSourceVersions(context.Background(), backupSetID, scope, FileSourcePageRequest{Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].RecoveryPointID != original.ID || second.NextCursor != "" {
		t.Fatalf("second page=%+v", second)
	}
}

func TestBackupFileSourceCursorFailsClosedWhenProjectionChanges(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}

	var ownedPoint model.RecoveryPoint
	if err := db.Select("producing_task_id").Where("id = ?", fixture.ownedPointID).Take(&ownedPoint).Error; err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if ownedPoint.ProducingTaskID == nil {
		t.Fatal("owned fixture point has no producing task")
	}
	if err := db.Select("node_id").Where("id = ?", *ownedPoint.ProducingTaskID).Take(&task).Error; err != nil {
		t.Fatal(err)
	}
	first, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	pointToDelete := fixture.unlinkedPointID
	if first.Items[0].DisplayLabel == "unlinked-task" {
		pointToDelete = fixture.ownedPointID
	}
	if err := db.Delete(&model.RecoveryPoint{}, "id = ?", pointToDelete).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{
		Limit: 1, Cursor: first.NextCursor,
	}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("changed projection cursor error=%v", err)
	}
}

func TestBackupFileSourceCursorFailsClosedWhenVisibleFactsChange(t *testing.T) {
	t.Run("nodes", func(t *testing.T) {
		db, _ := openCatalogBehaviorSQLite(t)
		now := time.Date(2026, 8, 27, 13, 10, 0, 0, time.UTC)
		fixture := seedFileSourceOwnershipFixture(t, db, now)
		secondNode := seedCatalogOwnershipNode(t, db, 6351, "digest-second", now)
		if err := db.Create(&model.NodeOwner{NodeID: secondNode.ID, UserID: fixture.operatorID, CreatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		secondTask := seedCatalogOwnershipTask(t, db, secondNode.ID, "digest-second-task", now)
		secondRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("8", 32), now)
		secondLink := seedCatalogOwnershipLink(t, db, strings.Repeat("9", 32), secondRepository.ID, secondTask.ID, nil, now)
		secondRun := seedCatalogOwnershipRun(t, db, secondTask.ID, now)
		seedFileSourceTaskPoint(t, db, 84, secondRepository, secondTask, secondRun, secondLink, backupasset.PointNativeSnapshot, "", now)

		service := newCatalogServiceForTest(t, db, now)
		scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
		first, err := service.ListFileSourceNodes(context.Background(), scope, FileSourcePageRequest{Limit: 1})
		if err != nil || first.NextCursor == "" {
			t.Fatalf("first page=%+v err=%v", first, err)
		}
		if err := db.Model(&model.Node{}).Where("id = ?", first.Items[0].NodeID).Update("name", "digest-renamed-node").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := service.ListFileSourceNodes(context.Background(), scope, FileSourcePageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrStaleCursor) {
			t.Fatalf("node fact drift cursor error=%v", err)
		}
	})

	t.Run("sets", func(t *testing.T) {
		db, _ := openCatalogBehaviorSQLite(t)
		now := time.Date(2026, 8, 27, 13, 20, 0, 0, time.UTC)
		fixture := seedFileSourceOwnershipFixture(t, db, now)
		var point model.RecoveryPoint
		if err := db.Select("producing_task_id").Where("id = ?", fixture.ownedPointID).Take(&point).Error; err != nil || point.ProducingTaskID == nil {
			t.Fatalf("load producing task: point=%+v err=%v", point, err)
		}
		var task model.Task
		if err := db.Select("node_id").Where("id = ?", *point.ProducingTaskID).Take(&task).Error; err != nil {
			t.Fatal(err)
		}
		service := newCatalogServiceForTest(t, db, now)
		scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
		first, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 1})
		if err != nil || first.NextCursor == "" {
			t.Fatalf("first page=%+v err=%v", first, err)
		}
		if err := db.Model(&model.Task{}).Where("name = ?", first.Items[0].DisplayLabel).Update("name", "digest-renamed-task").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrStaleCursor) {
			t.Fatalf("Backup Set fact drift cursor error=%v", err)
		}
	})

	t.Run("versions", func(t *testing.T) {
		db, _ := openCatalogBehaviorSQLite(t)
		now := time.Date(2026, 8, 27, 13, 30, 0, 0, time.UTC)
		fixture := seedFileSourceOwnershipFixture(t, db, now)
		var original model.RecoveryPoint
		if err := db.Where("id = ?", fixture.ownedPointID).Take(&original).Error; err != nil || original.ProducingTaskID == nil {
			t.Fatalf("load original point: point=%+v err=%v", original, err)
		}
		var task model.Task
		if err := db.Where("id = ?", *original.ProducingTaskID).Take(&task).Error; err != nil {
			t.Fatal(err)
		}
		var repository model.BackupRepository
		if err := db.Where("id = ?", original.RepositoryID).Take(&repository).Error; err != nil {
			t.Fatal(err)
		}
		var link model.TaskRepositoryLink
		if err := db.Where("task_id = ?", task.ID).Take(&link).Error; err != nil {
			t.Fatal(err)
		}
		newerRun := seedCatalogOwnershipRun(t, db, task.ID, now.Add(time.Minute))
		seedFileSourceTaskPoint(t, db, 85, repository, task, newerRun, link, backupasset.PointNativeSnapshot, "", now.Add(time.Minute))
		service := newCatalogServiceForTest(t, db, now.Add(2*time.Minute))
		scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
		sets, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var backupSetID string
		for _, set := range sets.Items {
			if set.DisplayLabel == task.Name {
				backupSetID = set.BackupSetID
			}
		}
		first, err := service.ListFileSourceVersions(context.Background(), backupSetID, scope, FileSourcePageRequest{Limit: 1})
		if err != nil || first.NextCursor == "" {
			t.Fatalf("first page=%+v err=%v", first, err)
		}
		if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", first.Items[0].RecoveryPointID).Update("entry_count", int64(999)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := service.ListFileSourceVersions(context.Background(), backupSetID, scope, FileSourcePageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrStaleCursor) {
			t.Fatalf("version fact drift cursor error=%v", err)
		}
	})
}

func TestBackupFileSourceCursorRejectsResourceEndpointAndExpiryReplay(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 13, 40, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	var point model.RecoveryPoint
	if err := db.Select("producing_task_id").Where("id = ?", fixture.ownedPointID).Take(&point).Error; err != nil || point.ProducingTaskID == nil {
		t.Fatalf("load producing task: point=%+v err=%v", point, err)
	}
	var task model.Task
	if err := db.Where("id = ?", *point.ProducingTaskID).Take(&task).Error; err != nil {
		t.Fatal(err)
	}
	secondNode := seedCatalogOwnershipNode(t, db, 6352, "cursor-resource", now)
	if err := db.Create(&model.NodeOwner{NodeID: secondNode.ID, UserID: fixture.operatorID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	secondTask := seedCatalogOwnershipTask(t, db, secondNode.ID, "cursor-resource-task", now)
	secondRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("6", 32), now)
	secondLink := seedCatalogOwnershipLink(t, db, strings.Repeat("7", 32), secondRepository.ID, secondTask.ID, nil, now)
	secondRun := seedCatalogOwnershipRun(t, db, secondTask.ID, now)
	seedFileSourceTaskPoint(t, db, 86, secondRepository, secondTask, secondRun, secondLink, backupasset.PointNativeSnapshot, "", now)

	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	first, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	if _, err := service.ListFileSourceBackupSets(context.Background(), secondNode.ID, scope, FileSourcePageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("resource replay cursor error=%v", err)
	}
	if _, err := service.ListFileSourceVersions(context.Background(), first.Items[0].BackupSetID, scope, FileSourcePageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("endpoint replay cursor error=%v", err)
	}
	service.cursor.now = func() time.Time { return now.Add(16 * time.Minute) }
	if _, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("expired cursor error=%v", err)
	}
}

func TestBackupFileSourceProjectionPreservesNullRetainedTimesAndOrdersThemLast(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 13, 50, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	var original model.RecoveryPoint
	if err := db.Where("id = ?", fixture.ownedPointID).Take(&original).Error; err != nil || original.ProducingTaskID == nil {
		t.Fatalf("load original point: point=%+v err=%v", original, err)
	}
	var task model.Task
	if err := db.Where("id = ?", *original.ProducingTaskID).Take(&task).Error; err != nil {
		t.Fatal(err)
	}
	var repository model.BackupRepository
	if err := db.Where("id = ?", original.RepositoryID).Take(&repository).Error; err != nil {
		t.Fatal(err)
	}
	var link model.TaskRepositoryLink
	if err := db.Where("task_id = ?", task.ID).Take(&link).Error; err != nil {
		t.Fatal(err)
	}
	newerAt := now.Add(time.Minute)
	newerRun := seedCatalogOwnershipRun(t, db, task.ID, newerAt)
	nullRetained := seedFileSourceTaskPoint(t, db, 87, repository, task, newerRun, link, backupasset.PointNativeSnapshot, "", newerAt)
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", nullRetained.ID).Updates(map[string]any{
		"captured_at": nil, "committed_at": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}

	service := newCatalogServiceForTest(t, db, now.Add(2*time.Minute))
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	sets, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var set FileSourceBackupSetDTO
	for _, candidate := range sets.Items {
		if candidate.DisplayLabel == task.Name {
			set = candidate
		}
	}
	versions, err := service.ListFileSourceVersions(context.Background(), set.BackupSetID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(versions.Items) != 2 {
		t.Fatalf("versions=%+v err=%v", versions, err)
	}
	if versions.Items[0].RecoveryPointID != original.ID || versions.Items[1].RecoveryPointID != nullRetained.ID ||
		versions.Items[1].CapturedAt != nil || versions.Items[1].CommittedAt != nil || !versions.Items[1].CreatedAt.Equal(newerAt) {
		t.Fatalf("null retained ordering=%+v", versions.Items)
	}
}

func TestBackupFileSourceProjectionKeepsTaskAndImportedLineagesIsolatedAndSafe(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)
	node := seedCatalogOwnershipNode(t, db, 6401, "projection", now)
	firstTask := seedCatalogOwnershipTask(t, db, node.ID, "daily-files", now)
	secondTask := seedCatalogOwnershipTask(t, db, node.ID, "weekly-files", now)
	firstRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("c", 32), now)
	secondRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("d", 32), now)
	thirdRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("e", 32), now)
	unlinkedAt := now.Add(-time.Minute)
	firstHistoricalLink := seedCatalogOwnershipLink(t, db, strings.Repeat("f", 32), firstRepository.ID, firstTask.ID, &unlinkedAt, now)
	firstActiveLink := seedCatalogOwnershipLink(t, db, strings.Repeat("1", 31)+"0", secondRepository.ID, firstTask.ID, nil, now)
	secondTaskLink := seedCatalogOwnershipLink(t, db, strings.Repeat("2", 31)+"0", firstRepository.ID, secondTask.ID, nil, now)
	firstRun := seedCatalogOwnershipRun(t, db, firstTask.ID, now)
	secondRun := seedCatalogOwnershipRun(t, db, firstTask.ID, now.Add(time.Minute))
	thirdRun := seedCatalogOwnershipRun(t, db, secondTask.ID, now.Add(2*time.Minute))
	seedFileSourceTaskPoint(t, db, 91, firstRepository, firstTask, firstRun, firstHistoricalLink, backupasset.PointNativeSnapshot, "", now)
	seedFileSourceTaskPoint(t, db, 92, secondRepository, firstTask, secondRun, firstActiveLink, backupasset.PointNativeSnapshot, "", now.Add(time.Minute))
	seedFileSourceTaskPoint(t, db, 93, firstRepository, secondTask, thirdRun, secondTaskLink, backupasset.PointNativeSnapshot, "", now.Add(2*time.Minute))
	seedFileSourceTasklessPoint(t, db, 94, node, secondRepository, now.Add(3*time.Minute))
	seedFileSourceTasklessPoint(t, db, 95, node, thirdRepository, now.Add(4*time.Minute))

	service := newCatalogServiceForTest(t, db, now.Add(5*time.Minute))
	scope := AuthorizationScope{Role: "admin", UserID: 1}
	nodes, err := service.ListFileSourceNodes(context.Background(), scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(nodes.Items) != 1 || nodes.Items[0].BackupSetCount != 4 {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	sets, err := service.ListFileSourceBackupSets(context.Background(), node.ID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(sets.Items) != 4 {
		t.Fatalf("sets=%+v err=%v", sets, err)
	}
	counts := map[FileSourceLineageKind][]int{}
	versionPages := make(map[string]FileSourceVersionPage, len(sets.Items))
	for _, set := range sets.Items {
		counts[set.LineageKind] = append(counts[set.LineageKind], set.VersionCount)
		versions, versionErr := service.ListFileSourceVersions(context.Background(), set.BackupSetID, scope, FileSourcePageRequest{Limit: 10})
		if versionErr != nil || len(versions.Items) != set.VersionCount {
			t.Fatalf("set=%+v versions=%+v err=%v", set, versions, versionErr)
		}
		versionPages[set.BackupSetID] = versions
		for _, version := range versions.Items {
			if !version.Permissions.List || version.Permissions.Preview || version.Permissions.Download {
				t.Fatalf("version inferred content permission: %+v", version)
			}
		}
	}
	if fmt.Sprint(counts[FileSourceLineageTask]) != "[2 1]" && fmt.Sprint(counts[FileSourceLineageTask]) != "[1 2]" {
		t.Fatalf("task lineage counts=%v", counts[FileSourceLineageTask])
	}
	if fmt.Sprint(counts[FileSourceLineageImported]) != "[1 1]" {
		t.Fatalf("imported lineage counts=%v", counts[FileSourceLineageImported])
	}
	payload, err := json.Marshal(struct {
		Nodes    FileSourceNodePage               `json:"nodes"`
		Sets     FileSourceBackupSetPage          `json:"sets"`
		Versions map[string]FileSourceVersionPage `json:"versions"`
	}{Nodes: nodes, Sets: sets, Versions: versionPages})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"shared-sensitive-repository", "provider_kind", "provider_locator", "lineage_json", "normalized_path",
		`"content":`, "content_base64", "content_type", "credential", "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", "sensitive-link-name", "sensitive-point-task",
	} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("file-source projection leaked %q: %s", forbidden, payload)
		}
	}
}

func TestBackupFileSourceProjectionRejectsZeroRetainedTimestamp(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 15, 0, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ownedPointID).
		Update("captured_at", time.Time{}).Error; err != nil {
		t.Fatal(err)
	}
	service := newCatalogServiceForTest(t, db, now)
	if _, err := service.ListFileSourceNodes(context.Background(), AuthorizationScope{
		Role: "operator", UserID: fixture.operatorID,
	}, FileSourcePageRequest{Limit: 10}); !errors.Is(err, ErrUnknownInternalState) {
		t.Fatalf("zero retained timestamp error=%v", err)
	}
}

func TestBackupFileSourceProjectionAppliesCurrentOwnershipByRole(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 16, 0, 0, 0, time.UTC)
	operator := model.User{ID: 6501, Username: "file-source-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&operator).Error; err != nil {
		t.Fatal(err)
	}
	ownedNode := seedCatalogOwnershipNode(t, db, 6502, "authority-owned", now)
	unownedNode := seedCatalogOwnershipNode(t, db, 6503, "authority-unowned", now)
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: operator.ID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	ownedTask := seedCatalogOwnershipTask(t, db, ownedNode.ID, "authority-owned-task", now)
	unownedTask := seedCatalogOwnershipTask(t, db, unownedNode.ID, "authority-unowned-task", now)
	ownedRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("3", 32), now)
	unownedRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("4", 32), now)
	importedRepository := seedCatalogOwnershipRepository(t, db, strings.Repeat("5", 32), now)
	ownedLink := seedCatalogOwnershipLink(t, db, strings.Repeat("6", 32), ownedRepository.ID, ownedTask.ID, nil, now)
	unownedLink := seedCatalogOwnershipLink(t, db, strings.Repeat("7", 32), unownedRepository.ID, unownedTask.ID, nil, now)
	ownedRun := seedCatalogOwnershipRun(t, db, ownedTask.ID, now)
	unownedRun := seedCatalogOwnershipRun(t, db, unownedTask.ID, now)
	seedFileSourceTaskPoint(t, db, 101, ownedRepository, ownedTask, ownedRun, ownedLink, backupasset.PointNativeSnapshot, "", now)
	seedFileSourceTaskPoint(t, db, 102, unownedRepository, unownedTask, unownedRun, unownedLink, backupasset.PointNativeSnapshot, "", now)
	seedFileSourceTasklessPoint(t, db, 103, ownedNode, importedRepository, now)

	service := newCatalogServiceForTest(t, db, now)
	operatorPage, err := service.ListFileSourceNodes(context.Background(), AuthorizationScope{Role: "operator", UserID: operator.ID}, FileSourcePageRequest{Limit: 10})
	if err != nil || len(operatorPage.Items) != 1 || operatorPage.Items[0].NodeID != ownedNode.ID || operatorPage.Items[0].BackupSetCount != 1 {
		t.Fatalf("operator page=%+v err=%v", operatorPage, err)
	}
	adminPage, err := service.ListFileSourceNodes(context.Background(), AuthorizationScope{Role: "admin", UserID: 1}, FileSourcePageRequest{Limit: 10})
	if err != nil || len(adminPage.Items) != 2 {
		t.Fatalf("admin page=%+v err=%v", adminPage, err)
	}
	adminSets, err := service.ListFileSourceBackupSets(context.Background(), ownedNode.ID, AuthorizationScope{Role: "admin", UserID: 1}, FileSourcePageRequest{Limit: 10})
	if err != nil || len(adminSets.Items) != 2 {
		t.Fatalf("admin owned-node sets=%+v err=%v", adminSets, err)
	}
	for _, scope := range []AuthorizationScope{{Role: "viewer", UserID: operator.ID}, {Role: "unknown", UserID: operator.ID}, {Role: "operator", UserID: 0}} {
		if page, err := service.ListFileSourceNodes(context.Background(), scope, FileSourcePageRequest{Limit: 10}); !errors.Is(err, backupasset.ErrForbidden) || len(page.Items) != 0 {
			t.Fatalf("scope=%+v page=%+v err=%v", scope, page, err)
		}
	}
}

func TestBackupFileSourceProjectionSummarizesPartialCatalogCoverage(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 17, 0, 0, 0, time.UTC)
	node := seedCatalogOwnershipNode(t, db, 6601, "coverage", now)
	task := seedCatalogOwnershipTask(t, db, node.ID, "coverage-task", now)
	repository := seedCatalogOwnershipRepository(t, db, strings.Repeat("8", 32), now)
	link := seedCatalogOwnershipLink(t, db, strings.Repeat("9", 32), repository.ID, task.ID, nil, now)
	firstRun := seedCatalogOwnershipRun(t, db, task.ID, now)
	secondRun := seedCatalogOwnershipRun(t, db, task.ID, now.Add(time.Minute))
	firstPoint := seedFileSourceTaskPoint(t, db, 111, repository, task, firstRun, link, backupasset.PointNativeSnapshot, "", now)
	secondPoint := seedFileSourceTaskPoint(t, db, 112, repository, task, secondRun, link, backupasset.PointNativeSnapshot, "", now.Add(time.Minute))
	seedCatalogServiceGeneration(t, db, firstPoint.ID, 1, true, GenerationComplete, now)
	seedCatalogServiceGeneration(t, db, secondPoint.ID, 1, false, GenerationPartial, now.Add(time.Minute))

	service := newCatalogServiceForTest(t, db, now.Add(2*time.Minute))
	scope := AuthorizationScope{Role: "admin", UserID: 1}
	nodes, err := service.ListFileSourceNodes(context.Background(), scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(nodes.Items) != 1 || nodes.Items[0].CatalogCoverage != CoveragePartial {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	sets, err := service.ListFileSourceBackupSets(context.Background(), node.ID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(sets.Items) != 1 || sets.Items[0].CatalogCoverage != CoveragePartial {
		t.Fatalf("sets=%+v err=%v", sets, err)
	}
	versions, err := service.ListFileSourceVersions(context.Background(), sets.Items[0].BackupSetID, scope, FileSourcePageRequest{Limit: 10})
	if err != nil || len(versions.Items) != 2 || versions.Items[0].CatalogCoverage != CoveragePartial || versions.Items[1].CatalogCoverage != CoverageComplete {
		t.Fatalf("versions=%+v err=%v", versions, err)
	}
}

func TestBackupFileSourceCursorRejectsTamperAndAuthorizationReplay(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	service := newCatalogServiceForTest(t, db, now)
	scope := AuthorizationScope{Role: "operator", UserID: fixture.operatorID}
	var point model.RecoveryPoint
	if err := db.Select("producing_task_id").Where("id = ?", fixture.ownedPointID).Take(&point).Error; err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if point.ProducingTaskID == nil {
		t.Fatal("owned fixture point has no producing task")
	}
	if err := db.Select("node_id").Where("id = ?", *point.ProducingTaskID).Take(&task).Error; err != nil {
		t.Fatal(err)
	}
	first, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	separator := strings.LastIndexByte(first.NextCursor, '.')
	if separator < 0 || separator+1 >= len(first.NextCursor) {
		t.Fatalf("cursor has no signature: %q", first.NextCursor)
	}
	replacement := byte('A')
	if first.NextCursor[separator+1] == replacement {
		replacement = 'B'
	}
	tampered := first.NextCursor[:separator+1] + string(replacement) + first.NextCursor[separator+2:]
	if _, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, scope, FileSourcePageRequest{Limit: 1, Cursor: tampered}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered cursor error=%v", err)
	}
	replayUser := model.User{ID: scope.UserID + 1, Username: "cursor-replay-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&replayUser).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: task.NodeID, UserID: replayUser.ID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListFileSourceBackupSets(context.Background(), task.NodeID, AuthorizationScope{Role: "operator", UserID: replayUser.ID}, FileSourcePageRequest{
		Limit: 1, Cursor: first.NextCursor,
	}); !errors.Is(err, ErrStaleCursor) {
		t.Fatalf("authorization replay cursor error=%v", err)
	}
}

func TestBackupFileSourceProjectionRejectsUnknownClosedEnums(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 19, 0, 0, 0, time.UTC)
	fixture := seedFileSourceOwnershipFixture(t, db, now)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA ignore_check_constraints = ON").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Exec("PRAGMA ignore_check_constraints = OFF").Error })
	if err := db.Model(&model.BackupRepository{}).Where("id = ?", strings.Repeat("1", 32)).Update("status", "future_status").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("PRAGMA ignore_check_constraints = OFF").Error; err != nil {
		t.Fatal(err)
	}
	service := newCatalogServiceForTest(t, db, now)
	if _, err := service.ListFileSourceNodes(context.Background(), AuthorizationScope{Role: "operator", UserID: fixture.operatorID}, FileSourcePageRequest{Limit: 10}); !errors.Is(err, ErrUnknownInternalState) {
		t.Fatalf("unknown repository status error=%v", err)
	}
}

func TestBackupFileSourceProjectionFailsClosedAtCandidateLimit(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 8, 27, 20, 0, 0, 0, time.UTC)
	node := seedCatalogOwnershipNode(t, db, 6701, "limit", now)
	repository := seedCatalogOwnershipRepository(t, db, strings.Repeat("a", 32), now)
	points := make([]model.RecoveryPoint, 0, maxOwnershipCandidateIDs+1)
	for index := 1; index <= maxOwnershipCandidateIDs+1; index++ {
		captured := now.Add(time.Duration(index) * time.Second)
		points = append(points, model.RecoveryPoint{
			ID: fmt.Sprintf("%032x", index), RepositoryID: repository.ID,
			ProducingNodeIDSnapshot: node.ID, ProducingNodeNameSnapshot: "sensitive-node", LineageJSON: `{}`,
			Semantics: string(backupasset.PointImportedBaseline), State: string(backupasset.RecoveryPointCommitted),
			CapturedAt: &captured, CommittedAt: &captured, SourceFingerprint: fmt.Sprintf("%064x", index),
			ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("b", 64), ConsistencyJSON: `{}`, FidelityJSON: `{}`,
			CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
			PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone), CreatedAt: captured, UpdatedAt: captured,
		})
	}
	if err := db.CreateInBatches(points, 250).Error; err != nil {
		t.Fatal(err)
	}
	service := newCatalogServiceForTest(t, db, now)
	if page, err := service.ListFileSourceNodes(context.Background(), AuthorizationScope{Role: "admin", UserID: 1}, FileSourcePageRequest{Limit: 10}); !errors.Is(err, ErrOwnershipProjectionLimit) || len(page.Items) != 0 {
		t.Fatalf("limit page=%+v err=%v", page, err)
	}
	if resolved, err := service.ResolveFileSourceRecoveryPoint(context.Background(), points[0].ID, AuthorizationScope{Role: "admin", UserID: 1}); !errors.Is(err, ErrOwnershipProjectionLimit) || resolved != (FileSourceRecoveryPointDTO{}) {
		t.Fatalf("limit resolution=%+v err=%v", resolved, err)
	}
}

func seedFileSourceOwnershipFixture(t *testing.T, db *gorm.DB, now time.Time) catalogOwnershipFixture {
	t.Helper()
	fixture := seedCatalogOwnershipFixture(t, db, now)

	var tasks []model.Task
	if err := db.Select("id", "node_id").Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	taskNodeIDs := make(map[uint]uint, len(tasks))
	for _, task := range tasks {
		taskNodeIDs[task.ID] = task.NodeID
	}

	var points []model.RecoveryPoint
	if err := db.Select("id", "producing_task_id").Where("producing_task_id IS NOT NULL").Find(&points).Error; err != nil {
		t.Fatal(err)
	}
	for _, point := range points {
		nodeID, ok := taskNodeIDs[*point.ProducingTaskID]
		if !ok || nodeID == 0 {
			t.Fatalf("point %s has no production-shaped producing node", point.ID)
		}
		if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
			UpdateColumn("producing_node_id_snapshot", nodeID).Error; err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func seedFileSourceTaskPoint(
	t *testing.T,
	db *gorm.DB,
	seed int,
	repository model.BackupRepository,
	task model.Task,
	run model.TaskRun,
	link model.TaskRepositoryLink,
	semantics backupasset.PointVersionSemantics,
	lineageOverride string,
	now time.Time,
) model.RecoveryPoint {
	t.Helper()
	point := seedCatalogOwnershipPoint(t, db, seed, repository, task, run, link, semantics, lineageOverride, now)
	if task.NodeID == 0 {
		t.Fatalf("task %d has no production-shaped node", task.ID)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
		UpdateColumn("producing_node_id_snapshot", task.NodeID).Error; err != nil {
		t.Fatal(err)
	}
	point.ProducingNodeIDSnapshot = task.NodeID
	return point
}

func seedFileSourceTasklessPoint(
	t *testing.T,
	db *gorm.DB,
	seed int,
	node model.Node,
	repository model.BackupRepository,
	now time.Time,
) model.RecoveryPoint {
	t.Helper()
	point := model.RecoveryPoint{
		ID: fmt.Sprintf("%032x", seed), RepositoryID: repository.ID,
		ProducingNodeIDSnapshot: node.ID, ProducingNodeNameSnapshot: "sensitive-point-node", LineageJSON: `{}`,
		EncryptedProviderLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", Semantics: string(backupasset.PointImportedBaseline),
		State: string(backupasset.RecoveryPointCommitted), CapturedAt: &now, CommittedAt: &now,
		SourceFingerprint: fmt.Sprintf("%064x", seed), ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("b", 64),
		ConsistencyJSON: `{}`, FidelityJSON: `{}`, CapabilityRevision: 1, CapabilitiesJSON: `{}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalOnline),
		HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	return point
}
