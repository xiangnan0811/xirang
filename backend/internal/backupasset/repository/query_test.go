package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
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

func TestBeginManagedRsyncPointReadRejectsUncommittedPointBeforeReaderAccess(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	attempt, err := execution.Attempt().RsyncTreeAttempt()
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, History: fixture.service.history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.BeginManagedRsyncPointRead(context.Background(), fixture.task.ID, attempt.RecoveryPointID)
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("uncommitted managed Rsync reader error=%v, want capability unavailable", err)
	}
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Reason.Code != backupasset.CapabilityPointNotCommitted {
		t.Fatalf("uncommitted managed Rsync reader reason=%+v", capabilityErr)
	}
	operations := fixture.admission.operations()
	if len(operations) != 2 || operations[1] != publication.ResticOperation("managed_rsync_point_read") {
		t.Fatalf("managed Rsync reader admission operations=%v", operations)
	}
	if got := fixture.admission.closedCount(); got != 1 {
		t.Fatalf("rejected managed Rsync reader left admission open: closed=%d", got)
	}
}

func TestContentReadManagedRsyncPointUsesDedicatedAdmissionOperation(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	attempt, err := execution.Attempt().RsyncTreeAttempt()
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, History: fixture.service.history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeOperations := fixture.admission.operations()
	token := &managedRsyncPointReadTokenFake{operation: publication.OperationContentRead}
	_, err = service.beginManagedRsyncPointReadWithAdmission(
		context.Background(), fixture.task.ID, attempt.RecoveryPointID, token,
	)
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("uncommitted content Rsync reader error=%v", err)
	}
	operations := fixture.admission.operations()
	if len(operations) != len(beforeOperations) || token.closed.Load() != 0 {
		t.Fatalf("content Rsync reacquired admission before=%v after=%v token_closes=%d", beforeOperations, operations, token.closed.Load())
	}
	if err := token.Close(); err != nil || token.closed.Load() != 1 {
		t.Fatalf("caller failed to release borrowed content admission: err=%v closes=%d", err, token.closed.Load())
	}
}

func TestManagedRsyncPointReadSessionRetainsAdmissionUntilReadHandleCloses(t *testing.T) {
	token := &managedRsyncPointReadTokenFake{}
	session := &ManagedRsyncPointReadSession{
		adapter: &managedRsyncPointReadAdapterFake{}, token: token,
	}
	handle, _, err := session.OpenSequential(context.Background(), provider.EntryLocator{Native: "file"}, provider.ReadRequest{MaxBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if got := token.closed.Load(); got != 0 {
		t.Fatalf("session released admission before read handle close: %d", got)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if got := token.closed.Load(); got != 1 {
		t.Fatalf("session admission close count=%d, want 1", got)
	}
	if _, _, err := session.OpenSequential(context.Background(), provider.EntryLocator{Native: "file"}, provider.ReadRequest{MaxBytes: 16}); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("closed session open error=%v, want forbidden", err)
	}
}

func TestManagedRsyncReadHandleForwardsProviderByteReporter(t *testing.T) {
	underlying := &meteredProviderReadHandleFake{Reader: strings.NewReader("data"), providerBytes: 5}
	handle := &managedRsyncPointReadHandle{underlying: underlying, session: &ManagedRsyncPointReadSession{}}
	reporter, ok := any(handle).(provider.ProviderByteReporter)
	if !ok {
		t.Fatal("managed Rsync wrapper hides ProviderByteReporter")
	}
	if payload, err := io.ReadAll(handle); err != nil || string(payload) != "data" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	if got := reporter.ProviderBytes(); got != 5 {
		t.Fatalf("forwarded Provider bytes=%d, want 5", got)
	}
}

type meteredProviderReadHandleFake struct {
	io.Reader
	providerBytes int64
}

func (*meteredProviderReadHandleFake) Close() error { return nil }

func (handle *meteredProviderReadHandleFake) ProviderBytes() int64 { return handle.providerBytes }

func TestManagedRsyncCommittedPointReadRequestBindsExactCommittedEvidence(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	state := execution.(*rsyncPublicationExecution)
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(state.markerKey, fixture.binding, state.attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(state.markerKey, state.childFence),
		PointDeadlineAt: state.attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", state.attempt.RecoveryPointID).Updates(map[string]any{
		"state": string(backupasset.RecoveryPointCommitted), "committed_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, History: fixture.service.history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := loadExactManagedRsyncPublicationRuntime(context.Background(), fixture.db, fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	request, access, err := service.managedRsyncCommittedPointReadRequest(context.Background(), runtime, point)
	if err != nil {
		t.Fatal(err)
	}
	if request.Attempt != state.attempt || request.ManagedRoot != fixture.binding.ManagedRootLocator ||
		request.CommitMarkerDigest != commit.CommitMarkerDigest || request.SourceFingerprint != point.SourceFingerprint ||
		request.ManifestDigest != point.ManifestDigest || request.ManifestEntryCount != uint64(point.EntryCount) || request.LogicalBytes != uint64(point.LogicalBytes) {
		t.Fatalf("committed Rsync reader request=%+v", request)
	}
	if access.AdapterData != nil || access.Locator != "" || access.RepositoryID != fixture.repository.ID || access.TaskID != fixture.task.ID || access.NodeID != fixture.task.NodeID {
		t.Fatalf("committed Rsync reader access=%+v", access)
	}
	if _, err := service.BeginManagedRsyncPointRead(context.Background(), fixture.task.ID, point.ID); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("unreadable committed Rsync tree error=%v, want capability unavailable", err)
	} else if reason, _, ok := CapabilityFromError(err); !ok || reason.Code != backupasset.CapabilityMutableSourceChanged {
		t.Fatalf("unreadable committed Rsync tree capability=%+v ok=%t", reason, ok)
	}

	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("lineage_json", `{}`).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&point, "id = ?", point.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.managedRsyncCommittedPointReadRequest(context.Background(), runtime, point); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("drifted committed Rsync lineage error=%v, want invalid state", err)
	}
}

type managedRsyncPointReadAdapterFake struct{}

func (*managedRsyncPointReadAdapterFake) ListPoints(context.Context, provider.ReadSnapshot, provider.PageRequest) (provider.NativePointPage, error) {
	return provider.NativePointPage{}, nil
}

func (*managedRsyncPointReadAdapterFake) ListEntries(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator, provider.PageRequest) (provider.EntryPage, error) {
	return provider.EntryPage{}, nil
}

func (*managedRsyncPointReadAdapterFake) StatEntry(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator) (provider.Entry, error) {
	return provider.Entry{}, nil
}

func (*managedRsyncPointReadAdapterFake) OpenSequential(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator, provider.ReadRequest) (provider.ReadHandle, provider.ContentStat, error) {
	return io.NopCloser(strings.NewReader("managed-rsync")), provider.ContentStat{}, nil
}

func (*managedRsyncPointReadAdapterFake) OpenRange(context.Context, provider.ReadSnapshot, provider.PointLocator, provider.EntryLocator, provider.ByteRange) (provider.ReadHandle, provider.ContentStat, error) {
	return io.NopCloser(strings.NewReader("managed-rsync")), provider.ContentStat{}, nil
}

type managedRsyncPointReadTokenFake struct {
	closed    atomic.Int32
	operation publication.ResticOperation
}

func (*managedRsyncPointReadTokenFake) Generation() uint64 { return 1 }
func (*managedRsyncPointReadTokenFake) Mode() publication.AdmissionMode {
	return publication.AdmissionManaged
}
func (token *managedRsyncPointReadTokenFake) Operation() publication.ResticOperation {
	if token.operation != "" {
		return token.operation
	}
	return publication.OperationManagedRsyncPointRead
}
func (token *managedRsyncPointReadTokenFake) Close() error {
	token.closed.Add(1)
	return nil
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

func TestVisibilityRejectsMalformedOwnedPointBeforeRepositoryProjection(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 17, 19, 0, 0, 0, time.UTC)
	ownedTask := seedTask(t, db, "restic", "sftp:user@example.invalid:/malformed", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	const operatorID uint = 702
	if err := db.Create(&model.User{
		ID: operatorID, Username: "malformed-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
		Role: "operator", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeOwner{NodeID: ownedTask.NodeID, UserID: operatorID, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	repository := seedVisibilityRepository(t, db, strings.Repeat("f", 32), "MUST_NOT_PROJECT_MALFORMED", now)
	unlinkedAt := now
	seedVisibilityLink(t, db, strings.Repeat("e", 32), repository.ID, &ownedTask.ID, "malformed-link", ownedTask.NodeID, now)
	if err := db.Model(&model.TaskRepositoryLink{}).Where("repository_id = ?", repository.ID).Update("unlinked_at", unlinkedAt).Error; err != nil {
		t.Fatal(err)
	}
	point := seedVisibilityPoint(t, db, strings.Repeat("d", 32), repository.ID, &ownedTask.ID, "malformed-point", ownedTask.NodeID, now)
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("lineage_json", `{}`).Error; err != nil {
		t.Fatal(err)
	}
	service := newVisibilityServiceForTest(t, db, now)
	page, err := service.List(context.Background(), RepositoryListRequest{Limit: 10}, VisibilityScope{Role: "operator", UserID: operatorID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("malformed owned point projected repository=%+v", page.Items)
	}
	if _, err := service.Detail(context.Background(), repository.ID, VisibilityScope{Role: "operator", UserID: operatorID}, RequestContext{}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("malformed repository detail error=%v", err)
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
		NodeNameSnapshot: "snapshot-node", PublicationMode: string(backupasset.PublicationNativeSnapshot),
		EncryptedLegacyLocator: "FAKE_PROVIDER_LOCATOR_FOR_TEST_ONLY", LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
}

func seedVisibilityPoint(t *testing.T, db *gorm.DB, id, repositoryID string, taskID *uint, name string, nodeSnapshot uint, now time.Time) model.RecoveryPoint {
	t.Helper()
	lineage := `{}`
	var taskRunID *uint
	if taskID != nil {
		started := now.Add(-2 * time.Minute)
		run := model.TaskRun{
			TaskID: *taskID, TriggerType: "manual", Status: "success", StartedAt: &started, FinishedAt: &now,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&run).Error; err != nil {
			t.Fatal(err)
		}
		var link model.TaskRepositoryLink
		if err := db.Where("repository_id = ? AND task_id = ?", repositoryID, *taskID).First(&link).Error; err != nil {
			t.Fatal(err)
		}
		encoded, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
			Version: 1, TaskRepositoryLinkID: link.ID, TaskID: *taskID, TaskRunID: run.ID,
			Trigger: "manual", PublicationMode: string(backupasset.PublicationNativeSnapshot),
			PointCodecVersion: 1, TagCodecVersion: 1, StartedAt: started, PreparedAt: now.Add(-time.Minute), PointDeadlineAt: now.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		lineage = encoded
		taskRunID = &run.ID
	}
	point := model.RecoveryPoint{
		ID: id, RepositoryID: repositoryID, ProducingTaskID: taskID, ProducingTaskRunID: taskRunID, ProducingTaskNameSnapshot: name,
		ProducingNodeIDSnapshot: nodeSnapshot, ProducingNodeNameSnapshot: "snapshot-node", LineageJSON: lineage,
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
