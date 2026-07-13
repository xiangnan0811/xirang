package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestVisibilityFiltersSharedRepositoryByLiveCurrentTaskOwnership(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	ownedTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/shared", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	unownedTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/shared", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	archivedTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/shared", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	otherRepositoryTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/other", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	if err := db.Model(&model.Task{}).Where("id = ?", archivedTask.ID).Update("archived_at", now).Error; err != nil {
		t.Fatal(err)
	}
	const operatorID uint = 701
	if err := db.Create(&model.User{ID: operatorID, Username: "visibility-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "operator", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedTask.NodeID, UserID: operatorID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	shared := seedVisibilityRepository(t, db, strings.Repeat("1", 32), "shared", now)
	unownedOnly := seedVisibilityRepository(t, db, strings.Repeat("2", 32), "unowned-only", now.Add(time.Second))
	seedVisibilityLink(t, db, strings.Repeat("3", 32), shared.ID, &ownedTask.ID, "owned-link", unownedTask.NodeID, now)
	// Snapshot ownership is deliberately misleading: authorization must use the live Task's current NodeID.
	seedVisibilityLink(t, db, strings.Repeat("4", 32), shared.ID, &unownedTask.ID, "unowned-link", ownedTask.NodeID, now)
	seedVisibilityLink(t, db, strings.Repeat("5", 32), shared.ID, &archivedTask.ID, "archived-link", ownedTask.NodeID, now)
	seedVisibilityLink(t, db, strings.Repeat("6", 32), shared.ID, nil, "deleted-link", ownedTask.NodeID, now)
	seedVisibilityLink(t, db, strings.Repeat("7", 32), unownedOnly.ID, &otherRepositoryTask.ID, "other-repository-link", ownedTask.NodeID, now)
	ownedPoint := seedVisibilityPoint(t, db, strings.Repeat("8", 32), shared.ID, &ownedTask.ID, "owned-point", unownedTask.NodeID, now)
	unownedPoint := seedVisibilityPoint(t, db, strings.Repeat("9", 32), shared.ID, &unownedTask.ID, "unowned-point", ownedTask.NodeID, now)
	deletedPoint := seedVisibilityPoint(t, db, strings.Repeat("a", 32), shared.ID, nil, "deleted-point", ownedTask.NodeID, now)

	service := newVisibilityServiceForTest(t, db, now)
	adminPage, err := service.List(context.Background(), RepositoryListRequest{Limit: 20}, VisibilityScope{Role: "admin", UserID: 1}, RequestContext{})
	if err != nil || len(adminPage.Items) != 2 {
		t.Fatalf("admin page=%+v err=%v", adminPage, err)
	}
	adminDetail, err := service.Detail(context.Background(), shared.ID, VisibilityScope{Role: "admin", UserID: 1}, RequestContext{})
	if err != nil || len(adminDetail.Lineages) != 7 {
		t.Fatalf("admin detail=%+v err=%v", adminDetail, err)
	}

	operatorScope := VisibilityScope{Role: "operator", UserID: operatorID}
	operatorPage, err := service.List(context.Background(), RepositoryListRequest{Limit: 20}, operatorScope, RequestContext{})
	if err != nil || len(operatorPage.Items) != 1 || operatorPage.Items[0].ID != shared.ID {
		t.Fatalf("operator page=%+v err=%v", operatorPage, err)
	}
	operatorDetail, err := service.Detail(context.Background(), shared.ID, operatorScope, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(operatorDetail.Lineages) != 2 {
		t.Fatalf("operator lineages=%+v", operatorDetail.Lineages)
	}
	payload, err := json.Marshal(operatorDetail)
	if err != nil {
		t.Fatal(err)
	}
	body := string(payload)
	for _, forbidden := range []string{
		"unowned-link", "archived-link", "deleted-link", "unowned-point", "deleted-point",
		unownedPoint.ID, deletedPoint.ID, "repository_identity", "encrypted_config", "provider_locator",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("operator detail leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "owned-link") || !strings.Contains(body, ownedPoint.ID) {
		t.Fatalf("owned lineage missing: %s", body)
	}
	if _, err := service.Detail(context.Background(), unownedOnly.ID, operatorScope, RequestContext{}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("unowned detail error=%v", err)
	}
	if _, err := service.Detail(context.Background(), strings.Repeat("f", 32), operatorScope, RequestContext{}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("missing detail error=%v", err)
	}
}

func TestVisibilityRejectsViewerUnknownAndInvalidOperatorScope(t *testing.T) {
	db := newRepositoryTestDB(t)
	service := newVisibilityServiceForTest(t, db, time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC))
	for _, scope := range []VisibilityScope{{Role: "viewer", UserID: 1}, {Role: "", UserID: 1}, {Role: "future", UserID: 1}, {Role: "operator", UserID: 0}} {
		if _, err := service.List(context.Background(), RepositoryListRequest{Limit: 10}, scope, RequestContext{}); !errors.Is(err, backupasset.ErrForbidden) {
			t.Fatalf("scope=%+v error=%v", scope, err)
		}
	}
}

func TestVisibilityOwnershipQueryFailureFailsClosed(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	seedVisibilityRepository(t, db, strings.Repeat("e", 32), "must-not-leak", now)
	if err := db.Exec("DROP TABLE node_owners").Error; err != nil {
		t.Fatal(err)
	}
	service := newVisibilityServiceForTest(t, db, now)
	page, err := service.List(context.Background(), RepositoryListRequest{Limit: 10}, VisibilityScope{Role: "operator", UserID: 99}, RequestContext{})
	if err == nil || len(page.Items) != 0 {
		t.Fatalf("ownership failure returned page=%+v err=%v", page, err)
	}
}

func TestQueryCursorIsSignedStableAndVisibilityScoped(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	seedVisibilityRepository(t, db, strings.Repeat("b", 32), "first", now)
	seedVisibilityRepository(t, db, strings.Repeat("c", 32), "second", now.Add(time.Second))
	seedVisibilityRepository(t, db, strings.Repeat("d", 32), "third", now.Add(2*time.Second))
	service := newVisibilityServiceForTest(t, db, now.Add(3*time.Second))
	scope := VisibilityScope{Role: "admin", UserID: 1}
	first, err := service.List(context.Background(), RepositoryListRequest{Limit: 1}, scope, RequestContext{})
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != strings.Repeat("d", 32) || first.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	for _, raw := range []string{"first", "second", "third", strings.Repeat("d", 32)} {
		if strings.Contains(first.NextCursor, raw) {
			t.Fatalf("cursor leaked %q: %s", raw, first.NextCursor)
		}
	}
	second, err := service.List(context.Background(), RepositoryListRequest{Limit: 1, Cursor: first.NextCursor}, scope, RequestContext{})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != strings.Repeat("c", 32) {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	tampered := first.NextCursor[:len(first.NextCursor)-1] + "A"
	if _, err := service.List(context.Background(), RepositoryListRequest{Limit: 1, Cursor: tampered}, scope, RequestContext{}); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("tampered cursor error=%v", err)
	}
	if _, err := service.List(context.Background(), RepositoryListRequest{Limit: 1, Cursor: first.NextCursor}, VisibilityScope{Role: "admin", UserID: 2}, RequestContext{}); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("cross-scope cursor error=%v", err)
	}
}

func newVisibilityServiceForTest(t *testing.T, db *gorm.DB, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Keyring: backupasset.NewKeyring(db, func() time.Time { return now }), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func seedVisibilityRepository(t *testing.T, db *gorm.DB, id, name string, now time.Time) model.BackupRepository {
	t.Helper()
	identity := "restic-native:v1:" + strings.Repeat(string(id[0]), 64)
	repository := model.BackupRepository{
		ID: id, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &identity, DisplayName: name,
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	return repository
}

func seedVisibilityLink(t *testing.T, db *gorm.DB, id, repositoryID string, taskID *uint, name string, nodeSnapshot uint, now time.Time) {
	t.Helper()
	link := model.TaskRepositoryLink{
		ID: id, TaskID: taskID, RepositoryID: repositoryID, TaskNameSnapshot: name, NodeIDSnapshot: nodeSnapshot,
		NodeNameSnapshot: "snapshot-node", PublicationMode: string(backupasset.PublicationNativeObjectVersions),
		EncryptedLegacyLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
}

func seedVisibilityPoint(t *testing.T, db *gorm.DB, id, repositoryID string, taskID *uint, name string, nodeSnapshot uint, now time.Time) model.RecoveryPoint {
	t.Helper()
	lineage, err := json.Marshal(backupasset.RecoveryPointLineageSummary{ProducingTaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	point := model.RecoveryPoint{
		ID: id, RepositoryID: repositoryID, ProducingTaskID: taskID, ProducingTaskNameSnapshot: name,
		ProducingNodeIDSnapshot: nodeSnapshot, ProducingNodeNameSnapshot: "snapshot-node", LineageJSON: string(lineage),
		EncryptedProviderLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", Semantics: string(backupasset.PointNativeSnapshot),
		State: string(backupasset.RecoveryPointCommitted), CapturedAt: &now, CommittedAt: &now, SourceFingerprint: strings.Repeat(string(id[0]), 64),
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat(string(id[0]), 64), ConsistencyJSON: `{}`,
		FidelityJSON: `{}`, CapabilityRevision: 1, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalOnline),
		HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	return point
}
