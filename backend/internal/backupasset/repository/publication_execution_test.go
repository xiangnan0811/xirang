package repository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestPreparePristineDisabledReturnsSideEffectFreeCompatibilitySession(t *testing.T) {
	fixture := newPublicationFixture(t, false, publication.AdmissionPristineLegacy)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.CompleteCompatibility(context.Background()) }()
	if execution.Mode() != publication.ModeCompatibility || execution.Attempt() != nil {
		t.Fatalf("compatibility execution=%s attempt=%+v", execution.Mode(), execution.Attempt())
	}
	if fixture.prober.calls != 0 || len(fixture.audit.inputs) != 0 {
		t.Fatalf("pristine compatibility reached probe/audit: probes=%d audits=%+v", fixture.prober.calls, fixture.audit.inputs)
	}
	fixture.requirePublicationCounts(t, 0, 0)
	if got := fixture.admission.operations(); len(got) != 1 || got[0] != publication.OperationLegacyBackup {
		t.Fatalf("compatibility admission operations=%v", got)
	}
}

func TestPrepareDisabledManagedHistoryBlocksLegacyBackupBeforeExecutorOrProvider(t *testing.T) {
	fixture := newPublicationFixture(t, false, publication.AdmissionPristineLegacy)
	fixture.connectExactResticBinding(t)
	history, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{
		DB:         fixture.db,
		Tombstones: managedHistoryTombstoneFake{repository: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.history = history

	if _, err := fixture.service.Prepare(context.Background(), fixture.run()); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("disabled managed-history prepare error=%v, want forbidden", err)
	}
	if fixture.prober.calls != 0 || fixture.publisher.backup != nil {
		t.Fatalf("managed history reached provider: probes=%d backup=%v", fixture.prober.calls, fixture.publisher.backup != nil)
	}
	fixture.requirePublicationCounts(t, 0, 0)
	if got := fixture.admission.closedCount(); got != 1 {
		t.Fatalf("managed-history admission closes=%d, want one", got)
	}
}

func TestPrepareDisabledAllowsExactPristineLegacyBindingDespiteOtherManagedHistory(t *testing.T) {
	fixture := newPublicationFixture(t, false, publication.AdmissionRollbackSafe)
	fixture.connectExactLegacyRsyncBinding(t)
	seedManagedHistoryPoint(t, fixture.db, strings.Repeat("9", 32), fixture.repository.ID, backupasset.PointNativeSnapshot, backupasset.RecoveryPointCommitted, fixture.now)

	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.CompleteCompatibility(context.Background()) }()
	if execution.Mode() != publication.ModeCompatibility || execution.Attempt() != nil {
		t.Fatalf("exact legacy compatibility execution=%s attempt=%+v", execution.Mode(), execution.Attempt())
	}
	if fixture.prober.calls != 0 || fixture.publisher.backup != nil {
		t.Fatalf("exact legacy compatibility reached provider: probes=%d backup=%v", fixture.prober.calls, fixture.publisher.backup != nil)
	}
}

func TestPrepareEnabledRequiresExactActiveResticBindingBeforeMutation(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	if _, err := fixture.service.Prepare(context.Background(), fixture.run()); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("enabled no-binding prepare error=%v, want forbidden", err)
	}
	if fixture.prober.calls != 0 {
		t.Fatalf("enabled no-binding reached provider probe %d times", fixture.prober.calls)
	}
	fixture.requirePublicationCounts(t, 0, 0)
	if got := fixture.admission.operations(); len(got) != 1 || got[0] != publication.OperationEvidenceBackup {
		t.Fatalf("enabled no-binding admission operations=%v", got)
	}
}

func TestPrepareCreatesOneDeterministicPointAndExecutionLease(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	if execution.Mode() != publication.ModeEvidence || execution.Attempt() == nil {
		t.Fatalf("evidence execution=%s attempt=%+v", execution.Mode(), execution.Attempt())
	}
	attempt := resticAttemptForExecution(t, execution)
	if attempt.Provider != backupasset.ProviderRestic || attempt.TaskID != fixture.task.ID || attempt.TaskRunID != fixture.taskRun.ID ||
		attempt.RepositoryID != fixture.repository.ID || attempt.RequiredTags[0] != "xirang.link.v1."+fixture.link.ID || attempt.Fence.HolderType != backupasset.LeaseHolderPointPublication {
		t.Fatalf("invalid publication attempt: %+v", attempt)
	}
	fixture.requirePublicationCounts(t, 1, 1)
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil {
		t.Fatal(err)
	}
	if point.State != string(backupasset.RecoveryPointPreparing) || lineage.TaskID != fixture.task.ID || lineage.TaskRunID != fixture.taskRun.ID || lineage.TaskRepositoryLinkID != fixture.link.ID || !lineage.PointDeadlineAt.After(lineage.PreparedAt) {
		t.Fatalf("prepared point=%+v lineage=%+v", point, lineage)
	}
}

func TestPrepareCopiesImmutableTaskRunAndLinkLineage(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", resticAttemptForExecution(t, execution).RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.ProducingTaskID == nil || *point.ProducingTaskID != fixture.task.ID || point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != fixture.taskRun.ID ||
		point.ProducingTaskNameSnapshot != fixture.task.Name || point.ProducingNodeIDSnapshot != fixture.task.NodeID || point.ProducingNodeNameSnapshot != fixture.node.Name {
		t.Fatalf("point lost immutable task/run/link snapshots: %+v", point)
	}
}

func TestPrepareCopiesDatabaseTaskSnapshotInsteadOfCallerFields(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	run := fixture.run()
	run.Task.Name = "stale-caller-task-name"
	run.Task.NodeID = fixture.node.ID + 1000

	execution, err := fixture.service.Prepare(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()

	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", resticAttemptForExecution(t, execution).RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.ProducingTaskNameSnapshot != fixture.task.Name || point.ProducingNodeIDSnapshot != fixture.task.NodeID ||
		point.ProducingNodeNameSnapshot != fixture.node.Name {
		t.Fatalf("publication snapshot trusted caller fields: %+v", point)
	}
}

func TestPrepareLegacyRsyncKeepsCompatibilityExecutionWhenManagedAdmissionIsOpen(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactLegacyRsyncBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if execution.Mode() != publication.ModeCompatibility || execution.Attempt() != nil {
		t.Fatalf("legacy Rsync execution mode=%s attempt=%+v", execution.Mode(), execution.Attempt())
	}
	if err := execution.CompleteCompatibility(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareDuplicateSameRunNeverReturnsAnotherFenceOrReplaysBackup(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	first, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	if _, err := fixture.service.Prepare(context.Background(), fixture.run()); !errors.Is(err, backupasset.ErrPublicationInProgress) {
		t.Fatalf("duplicate prepare error=%v, want publication in progress", err)
	}
	fixture.requirePublicationCounts(t, 1, 1)
	if fixture.prober.calls != 2 {
		t.Fatalf("duplicate prepare probe calls=%d, want two preflight probes but no second point/fence", fixture.prober.calls)
	}
}

func TestPrepareImmutableConflictRollsBackWithoutSecondPoint(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	conflicting := model.RecoveryPoint{
		ID: strings.Repeat("d", 32), RepositoryID: fixture.repository.ID,
		ProducingTaskID: &fixture.task.ID, ProducingTaskRunID: &fixture.taskRun.ID,
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointFailed),
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalUnknown),
		HoldState: string(backupasset.HoldNone), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&conflicting).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Prepare(context.Background(), fixture.run()); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("immutable TaskRun conflict error=%v, want conflict", err)
	}
	fixture.requirePublicationCounts(t, 1, 0)
	var unexpected model.RecoveryPoint
	if err := fixture.db.First(&unexpected, "id = ?", fixture.expectedPointID(t)).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deterministic point exists after conflict: point=%+v err=%v", unexpected, err)
	}
}

func TestExecutionCancelRejectsUnsafeCauseAndRetainsAdmissionUntilAbandon(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Cancel(errors.New("FAKE_UNSAFE_PUBLICATION_CANCEL_CAUSE_FOR_TEST_ONLY")); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("unsafe cancel error=%v, want invalid state", err)
	}
	select {
	case <-execution.Context().Done():
		t.Fatal("unsafe cancel stopped the execution context")
	default:
	}
	if got := fixture.admission.closedCount(); got != 0 {
		t.Fatalf("unsafe cancel closed admission %d times", got)
	}
	if err := execution.Cancel(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	select {
	case <-execution.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("accepted cancel did not stop execution context")
	}
	if got := fixture.admission.closedCount(); got != 0 {
		t.Fatalf("cancel closed admission %d times before terminal choice", got)
	}
	if err := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	if got := fixture.admission.closedCount(); got != 1 {
		t.Fatalf("abandon closed admission %d times", got)
	}
}

func TestPrepareDifferentRetryRunCreatesDifferentPointAndTags(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	first, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	secondRun := model.TaskRun{TaskID: fixture.task.ID, TriggerType: "retry", Status: "running", StartedAt: timePointer(fixture.now.Add(time.Second)), CreatedAt: fixture.now, UpdatedAt: fixture.now}
	if err := fixture.db.Create(&secondRun).Error; err != nil {
		t.Fatal(err)
	}
	secondRunInput := fixture.run()
	secondRunInput.TaskRunID = secondRun.ID
	secondRunInput.Trigger = secondRun.TriggerType
	second, err := fixture.service.Prepare(context.Background(), secondRunInput)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	firstAttempt := resticAttemptForExecution(t, first)
	secondAttempt := resticAttemptForExecution(t, second)
	if firstAttempt.RecoveryPointID == secondAttempt.RecoveryPointID || firstAttempt.RequiredTags[1] == secondAttempt.RequiredTags[1] {
		t.Fatalf("retry reused point/tag: first=%+v second=%+v", first.Attempt(), second.Attempt())
	}
	fixture.requirePublicationCounts(t, 2, 2)
}

type publicationFixture struct {
	t          *testing.T
	db         *gorm.DB
	now        time.Time
	settings   repositorySettings
	service    *PublicationService
	admission  *publicationAdmission
	prober     *scriptedProber
	publisher  *scriptedResticPublisher
	manifest   *scriptedManifestBuilder
	audit      *auditSpy
	task       model.Task
	node       model.Node
	taskRun    model.TaskRun
	repository model.BackupRepository
	link       model.TaskRepositoryLink
}

func newPublicationFixture(t *testing.T, enabled bool, mode publication.AdmissionMode) *publicationFixture {
	t.Helper()
	db := newRepositoryTestDB(t)
	now := time.Date(2026, 7, 14, 11, 0, 0, 0, time.UTC)
	task := seedTask(t, db, "restic", "sftp:user@example.invalid:/repository", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	var node model.Node
	if err := db.First(&node, task.NodeID).Error; err != nil {
		t.Fatal(err)
	}
	task.Node = node
	taskRun := model.TaskRun{TaskID: task.ID, TriggerType: "manual", Status: "running", StartedAt: timePointer(now.Add(-time.Minute)), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&taskRun).Error; err != nil {
		t.Fatal(err)
	}
	identity := provider.NativeResticIdentityPrefix + strings.Repeat("a", 64)
	repository := model.BackupRepository{
		ID: strings.Repeat("1", 32), ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &identity, DisplayName: "publication-repository",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)}
	publisher := &scriptedResticPublisher{}
	manifest := &scriptedManifestBuilder{}
	strategy, err := provider.NewResticPublicationStrategy(publisher, manifest)
	if err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: prober, PublicationStrategy: strategy}); err != nil {
		t.Fatal(err)
	}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 168 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	history, err := NewManagedHistoryResolver(ManagedHistoryResolverDependencies{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	admission := &publicationAdmission{mode: mode, generation: 1}
	audit := &auditSpy{}
	service, err := NewPublicationService(PublicationDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(completeRepositoryFoundationSettings(enabled)), Registry: registry, Lease: lease,
		Admission: admission, Metrics: publication.NoopMetrics{}, Audit: audit, History: history, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &publicationFixture{t: t, db: db, now: now, settings: completeRepositoryFoundationSettings(enabled), service: service, admission: admission, prober: prober, publisher: publisher, manifest: manifest, audit: audit, task: task, node: node, taskRun: taskRun, repository: repository}
}

func (fixture *publicationFixture) connectExactResticBinding(t *testing.T) {
	t.Helper()
	link := model.TaskRepositoryLink{
		ID: strings.Repeat("2", 32), TaskID: &fixture.task.ID, RepositoryID: fixture.repository.ID, TaskNameSnapshot: fixture.task.Name,
		NodeIDSnapshot: fixture.node.ID, NodeNameSnapshot: fixture.node.Name, PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	salt := bytes.Repeat([]byte{7}, provider.IdentitySaltBytes)
	document := bindingDocument{
		Version: bindingDocumentVersion, Provider: backupasset.ProviderRestic, IdentityClass: provider.IdentityNativeRepository,
		TaskID: fixture.task.ID, NodeID: fixture.node.ID, IdentitySalt: fmt.Sprintf("%x", salt), Locator: fixture.task.RsyncTarget,
		Secret: "FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY", EndpointFacts: []string{fmt.Sprintf("task:%d", fixture.task.ID), fmt.Sprintf("node:%d", fixture.node.ID)},
		NativeRepositoryID: strings.Repeat("a", 64), AdapterRevision: "test-reader:v1",
	}
	payload, err := encodeBindingDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	binding := model.RepositoryAccessBinding{ID: strings.Repeat("3", 32), RepositoryID: fixture.repository.ID, BindingKind: "task", EncryptedConfig: payload, ConfigFingerprint: strings.Repeat("b", 64), Status: bindingStatusActive, CreatedAt: fixture.now, UpdatedAt: fixture.now}
	if err := fixture.db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	fixture.link = link
}

func (fixture *publicationFixture) connectExactLegacyRsyncBinding(t *testing.T) {
	t.Helper()
	legacyTarget := t.TempDir()
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Updates(map[string]any{
		"executor_type": "rsync",
		"rsync_target":  legacyTarget,
	}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.task.ExecutorType = "rsync"
	fixture.task.RsyncTarget = legacyTarget
	identity := provider.ScopedIdentityPrefix(backupasset.ProviderRsync) + strings.Repeat("d", 64)
	repository := model.BackupRepository{
		ID: strings.Repeat("4", 32), ProviderKind: string(backupasset.ProviderRsync), RepositoryIdentity: &identity, DisplayName: "legacy-rsync-repository",
		VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityMutable), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	link := model.TaskRepositoryLink{
		ID: strings.Repeat("5", 32), TaskID: &fixture.task.ID, RepositoryID: repository.ID, TaskNameSnapshot: fixture.task.Name,
		NodeIDSnapshot: fixture.node.ID, NodeNameSnapshot: fixture.node.Name, PublicationMode: string(backupasset.PublicationLegacyMutable),
		EncryptedLegacyLocator: legacyTarget, LinkedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	document := bindingDocument{
		Version: bindingDocumentVersion, Provider: backupasset.ProviderRsync, IdentityClass: provider.IdentityTaskScopedEndpoint,
		TaskID: fixture.task.ID, NodeID: fixture.node.ID, IdentitySalt: strings.Repeat("07", provider.IdentitySaltBytes), Locator: legacyTarget,
		EndpointFacts: []string{fmt.Sprintf("task:%d", fixture.task.ID), fmt.Sprintf("node:%d", fixture.node.ID), "transport:local", "root:" + legacyTarget},
	}
	payload, err := encodeBindingDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	binding := model.RepositoryAccessBinding{ID: strings.Repeat("6", 32), RepositoryID: repository.ID, BindingKind: "task_derived_v1", EncryptedConfig: payload, ConfigFingerprint: strings.Repeat("e", 64), Status: bindingStatusActive, CreatedAt: fixture.now, UpdatedAt: fixture.now}
	if err := fixture.db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
}

func (fixture *publicationFixture) run() publication.Run {
	return publication.Run{Task: fixture.task, TaskRunID: fixture.taskRun.ID, Trigger: fixture.taskRun.TriggerType, StartedAt: *fixture.taskRun.StartedAt,
		Audit: backupasset.PublicationAuditContext{Actor: backupasset.AuditActor{UserID: 9, Username: "operator", Role: "operator"}, CorrelationID: "publication-prepare-1"}}
}

func (fixture *publicationFixture) commitEvidence() provider.ResticCommitV1 {
	return provider.ResticCommitV1{
		Provider: backupasset.ProviderRestic, RepositoryIdentity: fixture.attemptIdentity(), NativePointID: strings.Repeat("c", 64),
		CaptureStartedAt: fixture.now.Add(-5 * time.Second), CaptureFinishedAt: fixture.now, FilesProcessed: 12, LogicalBytes: 3456,
	}
}

func (fixture *publicationFixture) attemptIdentity() string {
	return provider.NativeResticIdentityPrefix + strings.Repeat("a", 64)
}

func (fixture *publicationFixture) expectedPointID(t *testing.T) string {
	t.Helper()
	pointID, err := deriveRecoveryPointID(fixture.link.ID, fixture.taskRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	return pointID
}

func (fixture *publicationFixture) requirePublicationCounts(t *testing.T, points, leases int64) {
	t.Helper()
	for modelType, want := range map[any]int64{&model.RecoveryPoint{}: points, &model.RecoveryPointLease{}: leases} {
		var count int64
		if err := fixture.db.Model(modelType).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%T count=%d want=%d err=%v", modelType, count, want, err)
		}
	}
}

func (fixture *publicationFixture) requirePointStateAndNoActiveLease(t *testing.T, pointID string, state backupasset.RecoveryPointState) {
	t.Helper()
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", pointID).Error; err != nil || point.State != string(state) {
		t.Fatalf("point state=%q err=%v, want %s", point.State, err, state)
	}
	var active int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", pointID, backupasset.LeaseActive).Count(&active).Error; err != nil || active != 0 {
		t.Fatalf("active leases=%d err=%v, want zero", active, err)
	}
}

func (fixture *publicationFixture) requirePointStateAndActiveLease(t *testing.T, pointID string, state backupasset.RecoveryPointState) {
	t.Helper()
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", pointID).Error; err != nil || point.State != string(state) {
		t.Fatalf("point state=%q err=%v, want %s", point.State, err, state)
	}
	var active int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", pointID, backupasset.LeaseActive).Count(&active).Error; err != nil || active != 1 {
		t.Fatalf("active leases=%d err=%v, want one", active, err)
	}
}

type publicationAdmission struct {
	mu         sync.Mutex
	mode       publication.AdmissionMode
	generation uint64
	values     []publication.ResticOperation
	tokens     []*publicationAdmissionToken
}

func (admission *publicationAdmission) Acquire(_ context.Context, operation publication.ResticOperation) (publication.AdmissionToken, error) {
	if err := publication.ValidateResticOperation(operation); err != nil {
		return nil, err
	}
	admission.mu.Lock()
	admission.values = append(admission.values, operation)
	token := &publicationAdmissionToken{mode: admission.mode, generation: admission.generation, operation: operation}
	admission.tokens = append(admission.tokens, token)
	admission.mu.Unlock()
	return token, nil
}

func (admission *publicationAdmission) operations() []publication.ResticOperation {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	return append([]publication.ResticOperation(nil), admission.values...)
}

func (admission *publicationAdmission) closedCount() int32 {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	var count int32
	for _, token := range admission.tokens {
		count += token.closed.Load()
	}
	return count
}

type publicationAdmissionToken struct {
	mode       publication.AdmissionMode
	generation uint64
	operation  publication.ResticOperation
	once       sync.Once
	closed     atomic.Int32
}

func (token *publicationAdmissionToken) Generation() uint64              { return token.generation }
func (token *publicationAdmissionToken) Mode() publication.AdmissionMode { return token.mode }
func (token *publicationAdmissionToken) Operation() publication.ResticOperation {
	return token.operation
}
func (token *publicationAdmissionToken) Close() error {
	token.once.Do(func() { token.closed.Add(1) })
	return nil
}

func timePointer(value time.Time) *time.Time { return &value }

type scriptedResticPublisher struct {
	backup func(context.Context, provider.ResticAttemptV1, provider.ResticBackupInput, func(provider.ResticBackupProgress)) (provider.ResticBackupResult, error)
	lookup func(context.Context, provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error)
}

func (publisher *scriptedResticPublisher) Backup(ctx context.Context, attempt provider.ResticAttemptV1, input provider.ResticBackupInput, progress func(provider.ResticBackupProgress)) (provider.ResticBackupResult, error) {
	if publisher.backup != nil {
		return publisher.backup(ctx, attempt, input, progress)
	}
	return provider.ResticBackupResult{}, errors.New("FAKE_RESTIC_PUBLISHER_BACKUP_NOT_CONFIGURED_FOR_TEST_ONLY")
}

func (publisher *scriptedResticPublisher) LookupAttempt(ctx context.Context, attempt provider.ResticAttemptV1) ([]provider.ResticSnapshotObservation, error) {
	if publisher.lookup != nil {
		return publisher.lookup(ctx, attempt)
	}
	return nil, errors.New("FAKE_RESTIC_PUBLISHER_LOOKUP_NOT_CONFIGURED_FOR_TEST_ONLY")
}

type scriptedManifestBuilder struct {
	build func(context.Context, provider.ResticAttemptV1, provider.ResticCommitV1, provider.ManifestLimits) (provider.ResticManifestV1, error)
}

func (builder *scriptedManifestBuilder) BuildManifest(ctx context.Context, attempt provider.ResticAttemptV1, commit provider.ResticCommitV1, limits provider.ManifestLimits) (provider.ResticManifestV1, error) {
	if builder.build != nil {
		return builder.build(ctx, attempt, commit, limits)
	}
	return provider.ResticManifestV1{}, errors.New("FAKE_RESTIC_MANIFEST_BUILDER_NOT_CONFIGURED_FOR_TEST_ONLY")
}

var _ publication.Admission = (*publicationAdmission)(nil)
