package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestLineageDecisionMatrix(t *testing.T) {
	tests := []struct {
		name         string
		enabled      bool
		tokenMode    publication.AdmissionMode
		link         bool
		history      bool
		otherHistory bool
		activeLease  bool
		wantMode     publication.LineageMode
		wantError    bool
	}{
		{"disabled pristine without link is compatibility", false, publication.AdmissionPristineLegacy, false, false, false, false, publication.LineageCompatibility, false},
		{"enabled with exact active link is exact", true, publication.AdmissionManaged, true, false, false, false, publication.LineageExact, false},
		{"enabled without link blocks legacy fallback", true, publication.AdmissionManaged, false, false, false, false, "", true},
		{"disabled repository history is exact rollback safe", false, publication.AdmissionPristineLegacy, true, true, false, false, publication.LineageExact, false},
		{"disabled installation history and no link fails closed", false, publication.AdmissionPristineLegacy, false, false, true, false, "", true},
		{"disabled active managed link remains exact before first point", false, publication.AdmissionPristineLegacy, true, false, true, false, publication.LineageExact, false},
		{"rollback-safe token with active lease is never compatibility", false, publication.AdmissionRollbackSafe, true, false, false, true, publication.LineageExact, false},
		{"managed token with disabled setting and no binding never falls back", false, publication.AdmissionManaged, false, false, false, false, "", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLineageFixture(t, test.enabled, test.tokenMode)
			if test.link {
				fixture.link = fixture.createActiveLink(t, fixture.repository.ID)
			}
			if test.history {
				if fixture.link.ID == "" {
					t.Fatal("test requires active link for repository history")
				}
				fixture.createCommittedPoint(t, fixture.task.ID, fixture.link.ID, strings.Repeat("a", 64), fixture.now)
			}
			if test.otherHistory {
				other := seedManagedHistoryRepository(t, fixture.db, strings.Repeat("b", 32), fixture.now.Add(time.Second))
				seedManagedHistoryPoint(t, fixture.db, strings.Repeat("c", 32), other.ID, backupasset.PointNativeSnapshot, backupasset.RecoveryPointCommitted, fixture.now)
			}
			if test.activeLease {
				fixture.createActivePublicationLease(t)
			}

			session, err := fixture.service.Begin(context.Background(), fixture.task.ID, publication.OperationLegacySnapshotList)
			if test.wantError {
				if err == nil {
					_ = session.Close()
					t.Fatal("unsafe legacy fallback was allowed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			if session.Mode() != test.wantMode {
				t.Fatalf("session mode=%s, want %s", session.Mode(), test.wantMode)
			}
		})
	}
}

func TestLineageExactUsesImmutableLineageAfterProducingFKsSetNull(t *testing.T) {
	fixture := newLineageFixture(t, true, publication.AdmissionManaged)
	fixture.link = fixture.createActiveLink(t, fixture.repository.ID)
	fullID := strings.Repeat("d", 64)
	point := fixture.createCommittedPoint(t, fixture.task.ID, fixture.link.ID, fullID, fixture.now)
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Updates(map[string]any{
		"producing_task_id":     nil,
		"producing_task_run_id": nil,
	}).Error; err != nil {
		t.Fatal(err)
	}

	session, err := fixture.service.Begin(context.Background(), fixture.task.ID, publication.OperationLegacySnapshotFiles)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if session.Mode() != publication.LineageExact || len(session.CommittedPoints()) != 1 {
		t.Fatalf("unexpected exact session: mode=%s points=%+v", session.Mode(), session.CommittedPoints())
	}
	if got, err := session.ResolveNativeID(fullID[:12]); err != nil || got != fullID {
		t.Fatalf("resolved full ID=%q err=%v, want %q/nil", got, err, fullID)
	}
	current, previous, err := session.CurrentAndPrevious(fullID)
	if err != nil || current.RecoveryPointID != point.ID || previous != nil {
		t.Fatalf("current/previous=%+v/%+v err=%v", current, previous, err)
	}
}

func TestLineageExactRejectsLiveFKConflictWithImmutableLineage(t *testing.T) {
	fixture := newLineageFixture(t, true, publication.AdmissionManaged)
	fixture.link = fixture.createActiveLink(t, fixture.repository.ID)
	point := fixture.createCommittedPoint(t, fixture.task.ID, fixture.link.ID, strings.Repeat("e", 64), fixture.now)
	otherTask := seedTask(t, fixture.db, "restic", "sftp:user@example.invalid:/other", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("producing_task_id", otherTask.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Begin(context.Background(), fixture.task.ID, publication.OperationLegacySnapshotFiles); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("conflicting live FK error=%v, want conflict", err)
	}
}

func TestLineageExactFiltersOtherTasksAndUncommittedPointsAndResolvesPrefixesOnlyWithinCurrentTask(t *testing.T) {
	fixture := newLineageFixture(t, true, publication.AdmissionManaged)
	fixture.link = fixture.createActiveLink(t, fixture.repository.ID)
	current := fixture.createCommittedPoint(t, fixture.task.ID, fixture.link.ID, strings.Repeat("1", 64), fixture.now.Add(2*time.Second))
	otherTask := seedTask(t, fixture.db, "restic", "sftp:user@example.invalid:/other", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	otherLink := fixture.createLinkForTask(t, otherTask.ID, fixture.repository.ID, strings.Repeat("2", 32))
	fixture.createCommittedPoint(t, otherTask.ID, otherLink.ID, strings.Repeat("2", 64), fixture.now.Add(3*time.Second))
	preparing := fixture.createCommittedPoint(t, fixture.task.ID, fixture.link.ID, strings.Repeat("3", 64), fixture.now)
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", preparing.ID).Update("state", backupasset.RecoveryPointPreparing).Error; err != nil {
		t.Fatal(err)
	}

	session, err := fixture.service.Begin(context.Background(), fixture.task.ID, publication.OperationLegacyDiff)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	points := session.CommittedPoints()
	if len(points) != 1 || points[0].RecoveryPointID != current.ID {
		t.Fatalf("current task committed points=%+v", points)
	}
	if _, err := session.ResolveNativeID(strings.Repeat("2", 8)); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("foreign prefix error=%v, want not found", err)
	}
	if _, err := session.ResolveNativeID("abc"); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("short prefix error=%v, want invalid state", err)
	}
}

func TestLineageRollbackSafeTokenWithActiveLeaseOnlyNeverUsesLegacyRead(t *testing.T) {
	fixture := newLineageFixture(t, false, publication.AdmissionRollbackSafe)
	fixture.link = fixture.createActiveLink(t, fixture.repository.ID)
	fixture.createActivePublicationLease(t)
	session, err := fixture.service.Begin(context.Background(), fixture.task.ID, publication.OperationLegacySnapshotList)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if session.Mode() != publication.LineageExact || len(session.CommittedPoints()) != 0 {
		t.Fatalf("rollback-safe session=%s points=%+v, want exact empty", session.Mode(), session.CommittedPoints())
	}
}

func TestRsyncLegacyGuardAllowsOnlyExactPristineMutableBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	connect := newRepositoryServiceForTest(t, db, backupasset.ProviderRsync, scopedObservationProber(backupasset.ProviderRsync))
	if _, err := connect.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	history, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: backupasset.NewFoundationService(completeRepositoryFoundationSettings(false)),
		Now: func() time.Time { return now }, Admission: &lineageAdmission{mode: publication.AdmissionPristineLegacy, generation: 9},
		History: history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Begin(context.Background(), taskEntity.ID, publication.OperationLegacyRestoreLatest)
	if err != nil {
		t.Fatal(err)
	}
	if session.Mode() != publication.LineageCompatibility {
		_ = session.Close()
		t.Fatalf("pristine Rsync session mode=%s, want compatibility", session.Mode())
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	if err := db.Model(&model.TaskRepositoryLink{}).Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).
		Update("publication_mode", string(backupasset.PublicationVersionedFullCopy)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Begin(context.Background(), taskEntity.ID, publication.OperationLegacyRestoreLatest); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("managed Rsync legacy guard error=%v, want forbidden", err)
	}
}

func TestRcloneLegacyGuardAllowsOnlyExactPristineMutableBinding(t *testing.T) {
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
	taskEntity := seedTask(t, db, "rclone", "backup:legacy", `{}`)
	connect := newRepositoryServiceForTest(t, db, backupasset.ProviderRclone, scopedObservationProber(backupasset.ProviderRclone))
	if _, err := connect.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{}); err != nil {
		t.Fatal(err)
	}
	history, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: backupasset.NewFoundationService(completeRepositoryFoundationSettings(false)),
		Now: func() time.Time { return now }, Admission: &lineageAdmission{mode: publication.AdmissionPristineLegacy, generation: 10},
		History: history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Begin(context.Background(), taskEntity.ID, publication.OperationLegacyIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	if session.Mode() != publication.LineageCompatibility {
		_ = session.Close()
		t.Fatalf("pristine Rclone session mode=%s, want compatibility", session.Mode())
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.TaskRepositoryLink{}).Where("task_id = ? AND unlinked_at IS NULL", taskEntity.ID).
		Update("publication_mode", string(backupasset.PublicationVersionedPrefix)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Begin(context.Background(), taskEntity.ID, publication.OperationLegacyIntegrity); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("managed Rclone legacy guard error=%v, want forbidden", err)
	}
}

type lineageFixture struct {
	t          *testing.T
	db         *gorm.DB
	now        time.Time
	settings   repositorySettings
	admit      *lineageAdmission
	service    *Service
	task       model.Task
	repository model.BackupRepository
	link       model.TaskRepositoryLink
	nextRunID  uint
}

func newLineageFixture(t *testing.T, enabled bool, mode publication.AdmissionMode) *lineageFixture {
	t.Helper()
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	settings := completeRepositoryFoundationSettings(enabled)
	history, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	admit := &lineageAdmission{mode: mode, generation: 7}
	service, err := NewService(Dependencies{
		DB: db, Foundation: backupasset.NewFoundationService(settings), Now: func() time.Time { return now },
		Admission: admit, History: history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &lineageFixture{t: t, db: db, now: now, settings: settings, admit: admit, service: service}
	fixture.task = seedTask(t, db, "restic", "sftp:user@example.invalid:/managed", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	fixture.repository = seedManagedHistoryRepository(t, db, strings.Repeat("9", 32), now)
	return fixture
}

func (fixture *lineageFixture) createActiveLink(t *testing.T, repositoryID string) model.TaskRepositoryLink {
	t.Helper()
	return fixture.createLinkForTask(t, fixture.task.ID, repositoryID, strings.Repeat("8", 32))
}

func (fixture *lineageFixture) createLinkForTask(t *testing.T, taskID uint, repositoryID, id string) model.TaskRepositoryLink {
	t.Helper()
	link := model.TaskRepositoryLink{
		ID: id, TaskID: &taskID, RepositoryID: repositoryID, TaskNameSnapshot: "lineage-task", NodeIDSnapshot: fixture.task.NodeID,
		NodeNameSnapshot: "lineage-node", PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	return link
}

func (fixture *lineageFixture) createCommittedPoint(t *testing.T, taskID uint, linkID, fullNativeID string, capturedAt time.Time) model.RecoveryPoint {
	t.Helper()
	fixture.nextRunID++
	runID := fixture.nextRunID
	lineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: linkID, TaskID: taskID, TaskRunID: runID, Trigger: "manual",
		PublicationMode: string(backupasset.PublicationNativeSnapshot), PointCodecVersion: 1, TagCodecVersion: 1,
		StartedAt: fixture.now.Add(-2 * time.Minute), PreparedAt: fixture.now.Add(-time.Minute), PointDeadlineAt: fixture.now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	locator := fmt.Sprintf(`{"version":1,"provider":"restic","full_snapshot_id":"%s"}`, fullNativeID)
	pointID := fmt.Sprintf("%032x", uint64(len(fullNativeID))+uint64(taskID)+uint64(capturedAt.UnixNano()))
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: fixture.repository.ID, ProducingTaskID: &taskID, ProducingTaskRunID: &runID,
		ProducingTaskNameSnapshot: "lineage-task", ProducingNodeIDSnapshot: fixture.task.NodeID, ProducingNodeNameSnapshot: "lineage-node",
		LineageJSON: lineage, EncryptedProviderLocator: locator, Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
		CapturedAt: &capturedAt, CommittedAt: &capturedAt, SourceFingerprint: fullNativeID, ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("e", 64),
		ConsistencyJSON: `{"version":1}`, FidelityJSON: `{}`, CapabilityRevision: 1, CapabilitiesJSON: `{}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	return point
}

func (fixture *lineageFixture) createActivePublicationLease(t *testing.T) {
	t.Helper()
	lease := model.RecoveryPointLease{
		ID: strings.Repeat("7", 32), RecoveryPointID: strings.Repeat("6", 32), HolderType: string(backupasset.LeaseHolderPointPublication), OwnerID: "publication-worker",
		AttemptID: strings.Repeat("5", 32), FenceToken: strings.Repeat("4", 64), Status: string(backupasset.LeaseActive),
		LeaseExpiresAt: fixture.now.Add(time.Minute), AbsoluteDeadline: fixture.now.Add(time.Hour), LastHeartbeatAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&lease).Error; err != nil {
		t.Fatal(err)
	}
}

type lineageAdmission struct {
	mode       publication.AdmissionMode
	generation uint64
}

func (admission *lineageAdmission) Acquire(_ context.Context, operation publication.ResticOperation) (publication.AdmissionToken, error) {
	if err := publication.ValidateResticOperation(operation); err != nil {
		return nil, err
	}
	return &lineageAdmissionToken{mode: admission.mode, generation: admission.generation, operation: operation}, nil
}

type lineageAdmissionToken struct {
	mode       publication.AdmissionMode
	generation uint64
	operation  publication.ResticOperation
	once       sync.Once
}

func (token *lineageAdmissionToken) Generation() uint64                     { return token.generation }
func (token *lineageAdmissionToken) Mode() publication.AdmissionMode        { return token.mode }
func (token *lineageAdmissionToken) Operation() publication.ResticOperation { return token.operation }
func (token *lineageAdmissionToken) Close() error                           { token.once.Do(func() {}); return nil }

var _ publication.Admission = (*lineageAdmission)(nil)
