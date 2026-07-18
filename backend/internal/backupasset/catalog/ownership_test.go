package catalog

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestCatalogOwnershipFiltersSharedRepositoryByStrictProducingLineage(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	ownership, err := NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}

	candidates := []string{
		fixture.unownedPointID,
		fixture.malformedPointID,
		fixture.ownedPointID,
		fixture.conflictingPointID,
		fixture.importedPointID,
		fixture.unattributedPointID,
		fixture.archivedPointID,
		fixture.unlinkedPointID,
	}
	visible, err := ownership.AuthorizedPointIDs(context.Background(), AuthorizationScope{
		Role: "operator", UserID: fixture.operatorID,
	}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{fixture.ownedPointID, fixture.unlinkedPointID}
	if !reflect.DeepEqual(visible, want) {
		t.Fatalf("operator visible points=%v, want %v", visible, want)
	}

	adminVisible, err := ownership.AuthorizedPointIDs(context.Background(), AuthorizationScope{
		Role: "admin", UserID: 1,
	}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(adminVisible, candidates) {
		t.Fatalf("admin visible points=%v, want all candidates", adminVisible)
	}
}

func TestCatalogOwnershipFailsClosedForViewerUnknownAndQueryFailure(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	ownership, err := NewOwnership(db)
	if err != nil {
		t.Fatal(err)
	}
	pointID := strings.Repeat("1", 32)
	for _, scope := range []AuthorizationScope{
		{Role: "viewer", UserID: 8},
		{Role: "", UserID: 8},
		{Role: "operator", UserID: 0},
		{Role: "admin", UserID: 0},
	} {
		if visible, err := ownership.AuthorizedPointIDs(context.Background(), scope, []string{pointID}); !errors.Is(err, backupasset.ErrForbidden) || len(visible) != 0 {
			t.Fatalf("scope=%+v visible=%v err=%v", scope, visible, err)
		}
	}
	if err := db.Exec("DROP TABLE node_owners").Error; err != nil {
		t.Fatal(err)
	}
	if visible, err := ownership.AuthorizedPointIDs(context.Background(), AuthorizationScope{Role: "operator", UserID: 9}, []string{pointID}); err == nil || len(visible) != 0 {
		t.Fatalf("ownership query failure visible=%v err=%v", visible, err)
	}
}

type catalogOwnershipFixture struct {
	operatorID          uint
	ownedPointID        string
	unownedPointID      string
	malformedPointID    string
	conflictingPointID  string
	importedPointID     string
	unattributedPointID string
	archivedPointID     string
	unlinkedPointID     string
}

func seedCatalogOwnershipFixture(t *testing.T, db *gorm.DB, now time.Time) catalogOwnershipFixture {
	t.Helper()
	const operatorID uint = 6101
	if err := db.Create(&model.User{
		ID: operatorID, Username: "catalog-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "operator", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	ownedNode := seedCatalogOwnershipNode(t, db, 6201, "owned", now)
	unownedNode := seedCatalogOwnershipNode(t, db, 6202, "unowned", now)
	if err := db.Create(&model.NodeOwner{NodeID: ownedNode.ID, UserID: operatorID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	ownedTask := seedCatalogOwnershipTask(t, db, ownedNode.ID, "owned-task", now)
	unownedTask := seedCatalogOwnershipTask(t, db, unownedNode.ID, "unowned-task", now)
	archivedTask := seedCatalogOwnershipTask(t, db, ownedNode.ID, "archived-task", now)
	if err := db.Model(&model.Task{}).Where("id = ?", archivedTask.ID).Update("archived_at", now).Error; err != nil {
		t.Fatal(err)
	}
	unlinkedTask := seedCatalogOwnershipTask(t, db, ownedNode.ID, "unlinked-task", now)
	repository := seedCatalogOwnershipRepository(t, db, strings.Repeat("1", 32), now)
	ownedLink := seedCatalogOwnershipLink(t, db, strings.Repeat("2", 32), repository.ID, ownedTask.ID, nil, now)
	unownedLink := seedCatalogOwnershipLink(t, db, strings.Repeat("3", 32), repository.ID, unownedTask.ID, nil, now)
	archivedLink := seedCatalogOwnershipLink(t, db, strings.Repeat("4", 32), repository.ID, archivedTask.ID, nil, now)
	unlinkedAt := now.Add(-time.Minute)
	unlinkedLink := seedCatalogOwnershipLink(t, db, strings.Repeat("5", 32), repository.ID, unlinkedTask.ID, &unlinkedAt, now)

	ownedRun := seedCatalogOwnershipRun(t, db, ownedTask.ID, now)
	unownedRun := seedCatalogOwnershipRun(t, db, unownedTask.ID, now)
	malformedRun := seedCatalogOwnershipRun(t, db, ownedTask.ID, now)
	conflictingRun := seedCatalogOwnershipRun(t, db, ownedTask.ID, now)
	lineageOtherRun := seedCatalogOwnershipRun(t, db, ownedTask.ID, now)
	importedRun := seedCatalogOwnershipRun(t, db, ownedTask.ID, now)
	archivedRun := seedCatalogOwnershipRun(t, db, archivedTask.ID, now)
	unlinkedRun := seedCatalogOwnershipRun(t, db, unlinkedTask.ID, now)

	ownedPoint := seedCatalogOwnershipPoint(t, db, 10, repository, ownedTask, ownedRun, ownedLink, backupasset.PointNativeSnapshot, "", now)
	unownedPoint := seedCatalogOwnershipPoint(t, db, 11, repository, unownedTask, unownedRun, unownedLink, backupasset.PointNativeSnapshot, "", now)
	malformedPoint := seedCatalogOwnershipPoint(t, db, 12, repository, ownedTask, malformedRun, ownedLink, backupasset.PointNativeSnapshot, `{}`, now)
	conflictingLineage := catalogOwnershipLineage(t, ownedTask.ID, lineageOtherRun.ID, ownedLink.ID, now)
	conflictingPoint := seedCatalogOwnershipPoint(t, db, 13, repository, ownedTask, conflictingRun, ownedLink, backupasset.PointNativeSnapshot, conflictingLineage, now)
	importedPoint := seedCatalogOwnershipPoint(t, db, 14, repository, ownedTask, importedRun, ownedLink, backupasset.PointImportedBaseline, "", now)
	unattributedPoint := model.RecoveryPoint{
		ID: fmt.Sprintf("%032x", 15), RepositoryID: repository.ID, LineageJSON: `{}`,
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
		SourceFingerprint: fmt.Sprintf("%064x", 15), ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("b", 64),
		ConsistencyJSON: `{}`, FidelityJSON: `{}`, CapabilityRevision: 1, CapabilitiesJSON: `{}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalOnline),
		HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&unattributedPoint).Error; err != nil {
		t.Fatal(err)
	}
	archivedPoint := seedCatalogOwnershipPoint(t, db, 16, repository, archivedTask, archivedRun, archivedLink, backupasset.PointNativeSnapshot, "", now)
	unlinkedPoint := seedCatalogOwnershipPoint(t, db, 17, repository, unlinkedTask, unlinkedRun, unlinkedLink, backupasset.PointNativeSnapshot, "", now)
	return catalogOwnershipFixture{
		operatorID: operatorID, ownedPointID: ownedPoint.ID, unownedPointID: unownedPoint.ID,
		malformedPointID: malformedPoint.ID, conflictingPointID: conflictingPoint.ID, importedPointID: importedPoint.ID,
		unattributedPointID: unattributedPoint.ID, archivedPointID: archivedPoint.ID, unlinkedPointID: unlinkedPoint.ID,
	}
}

func seedCatalogOwnershipNode(t *testing.T, db *gorm.DB, id uint, suffix string, now time.Time) model.Node {
	t.Helper()
	node := model.Node{
		ID: id, Name: "catalog-" + suffix, Host: suffix + ".example.invalid", Port: 22, Username: "catalog",
		AuthType: "key", Status: "online", BackupDir: "catalog-" + suffix, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	return node
}

func seedCatalogOwnershipTask(t *testing.T, db *gorm.DB, nodeID uint, name string, now time.Time) model.Task {
	t.Helper()
	task := model.Task{Name: name, NodeID: nodeID, ExecutorType: "restic", Status: "idle", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	return task
}

func seedCatalogOwnershipRun(t *testing.T, db *gorm.DB, taskID uint, now time.Time) model.TaskRun {
	t.Helper()
	started := now.Add(-time.Minute)
	run := model.TaskRun{TaskID: taskID, TriggerType: "manual", Status: "success", StartedAt: &started, FinishedAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	return run
}

func seedCatalogOwnershipRepository(t *testing.T, db *gorm.DB, id string, now time.Time) model.BackupRepository {
	t.Helper()
	repository := model.BackupRepository{
		ID: id, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "shared-sensitive-repository",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	return repository
}

func seedCatalogOwnershipLink(
	t *testing.T,
	db *gorm.DB,
	id, repositoryID string,
	taskID uint,
	unlinkedAt *time.Time,
	now time.Time,
) model.TaskRepositoryLink {
	t.Helper()
	link := model.TaskRepositoryLink{
		ID: id, TaskID: &taskID, RepositoryID: repositoryID, TaskNameSnapshot: "sensitive-link-name",
		NodeIDSnapshot: 999999, NodeNameSnapshot: "sensitive-node-name", PublicationMode: string(backupasset.PublicationNativeSnapshot),
		EncryptedLegacyLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", LinkedAt: now, UnlinkedAt: unlinkedAt,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	return link
}

func seedCatalogOwnershipPoint(
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
	lineage := lineageOverride
	if lineage == "" {
		lineage = catalogOwnershipLineage(t, task.ID, run.ID, link.ID, now)
	}
	point := model.RecoveryPoint{
		ID: fmt.Sprintf("%032x", seed), RepositoryID: repository.ID, ProducingTaskID: &task.ID, ProducingTaskRunID: &run.ID,
		ProducingTaskNameSnapshot: "sensitive-point-task", ProducingNodeIDSnapshot: 999999,
		ProducingNodeNameSnapshot: "sensitive-point-node", LineageJSON: lineage,
		EncryptedProviderLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", Semantics: string(semantics),
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

func catalogOwnershipLineage(t *testing.T, taskID, taskRunID uint, linkID string, now time.Time) string {
	t.Helper()
	encoded, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: linkID, TaskID: taskID, TaskRunID: taskRunID,
		Trigger: "manual", PublicationMode: string(backupasset.PublicationNativeSnapshot), PointCodecVersion: 1,
		TagCodecVersion: 1, StartedAt: now.Add(-2 * time.Minute), PreparedAt: now.Add(-time.Minute), PointDeadlineAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
