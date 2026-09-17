package task

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task/executor"
)

type managedRecoveryPointRetentionFake struct {
	calls []ManagedRecoveryPointRetentionRequest
	err   error
}

func (fake *managedRecoveryPointRetentionFake) EnforceManagedRetention(_ context.Context, request ManagedRecoveryPointRetentionRequest) error {
	request.RecoveryPointIDs = append([]string(nil), request.RecoveryPointIDs...)
	fake.calls = append(fake.calls, request)
	return fake.err
}

type managedRetentionLineageSessionFake struct {
	publication.LineageSession
	repositoryID    string
	repositoryIDs   []string
	repositoryCalls int32
	points          []publication.CommittedPoint
	closed          int32
}

func (session *managedRetentionLineageSessionFake) Mode() publication.LineageMode {
	return publication.LineageExact
}

func (session *managedRetentionLineageSessionFake) RepositoryID() string {
	call := int(atomic.AddInt32(&session.repositoryCalls, 1)) - 1
	if len(session.repositoryIDs) > 0 {
		if call < len(session.repositoryIDs) {
			return session.repositoryIDs[call]
		}
		return session.repositoryIDs[len(session.repositoryIDs)-1]
	}
	return session.repositoryID
}

func (session *managedRetentionLineageSessionFake) CommittedPoints() []publication.CommittedPoint {
	return append([]publication.CommittedPoint(nil), session.points...)
}

func (session *managedRetentionLineageSessionFake) Close() error {
	atomic.AddInt32(&session.closed, 1)
	return nil
}

func TestManagedTaskRetentionRejectsUnsafeExactAuthorityInputs(t *testing.T) {
	validRepositoryID := "dddddddddddddddddddddddddddddddd"
	validPointID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name                    string
		repositoryIDs           []string
		points                  []publication.CommittedPoint
		authorityErr            error
		wantAuthorityCalls      int
		wantBlocks              int
		wantRequestRepositoryID string
	}{
		{
			name:               "empty exact set is a managed no-op",
			repositoryIDs:      []string{validRepositoryID},
			wantAuthorityCalls: 0,
			wantBlocks:         0,
		},
		{
			name:               "managed authority error fails closed",
			repositoryIDs:      []string{validRepositoryID},
			points:             []publication.CommittedPoint{{RecoveryPointID: validPointID}},
			authorityErr:       errors.New("managed authority unavailable"),
			wantAuthorityCalls: 1,
			wantBlocks:         1,
		},
		{
			name:               "invalid repository ID fails closed",
			repositoryIDs:      []string{"invalid-repository"},
			points:             []publication.CommittedPoint{{RecoveryPointID: validPointID}},
			wantAuthorityCalls: 0,
			wantBlocks:         1,
		},
		{
			name:               "invalid point ID fails closed",
			repositoryIDs:      []string{validRepositoryID},
			points:             []publication.CommittedPoint{{RecoveryPointID: "invalid-point"}},
			wantAuthorityCalls: 0,
			wantBlocks:         1,
		},
		{
			name:          "duplicate point ID fails closed",
			repositoryIDs: []string{validRepositoryID},
			points: []publication.CommittedPoint{
				{RecoveryPointID: validPointID},
				{RecoveryPointID: validPointID},
			},
			wantAuthorityCalls: 0,
			wantBlocks:         1,
		},
		{
			name:                    "validated repository ID is snapshotted once",
			repositoryIDs:           []string{validRepositoryID, "unsafe-second-read"},
			points:                  []publication.CommittedPoint{{RecoveryPointID: validPointID}},
			wantAuthorityCalls:      1,
			wantBlocks:              0,
			wantRequestRepositoryID: validRepositoryID,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openManagerTestDB(t)
			manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
			t.Cleanup(func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = manager.Shutdown(shutdownCtx)
			})
			taskEntity := seedTaskForManagerTest(t, db)
			taskEntity.ExecutorType = "restic"
			session := &managedRetentionLineageSessionFake{repositoryIDs: test.repositoryIDs, points: test.points}
			guard := &legacyLineageGuardFake{session: session}
			recorder := &legacyBlockRecorderFake{}
			authority := &managedRecoveryPointRetentionFake{err: test.authorityErr}
			manager.SetLineageGuard(guard)
			manager.SetLegacyBlockRecorder(recorder)
			manager.SetManagedRecoveryPointRetention(authority)
			var legacyCalls int32
			manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
				atomic.AddInt32(&legacyCalls, 1)
			}

			manager.enforceResticRetention(model.Policy{ID: 72, RetentionDays: 7}, taskEntity)

			if len(authority.calls) != test.wantAuthorityCalls {
				t.Fatalf("managed authority calls=%d, want %d", len(authority.calls), test.wantAuthorityCalls)
			}
			if len(recorder.blocks) != test.wantBlocks {
				t.Fatalf("legacy blocks=%+v, want count %d", recorder.blocks, test.wantBlocks)
			}
			if test.wantBlocks == 1 {
				block := recorder.blocks[0]
				if block.TaskID != taskEntity.ID || block.TaskRunID != nil || block.Operation != publication.OperationLegacyRetention {
					t.Fatalf("unsafe legacy block=%+v", block)
				}
			}
			if got := atomic.LoadInt32(&legacyCalls); got != 0 {
				t.Fatalf("unsafe managed input fell through to legacy retention %d time(s)", got)
			}
			if got := atomic.LoadInt32(&session.closed); got != 1 {
				t.Fatalf("session close count=%d, want 1", got)
			}
			if got := atomic.LoadInt32(&session.repositoryCalls); got != 1 {
				t.Fatalf("RepositoryID calls=%d, want exactly 1", got)
			}
			if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
				t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
			}
			if test.wantRequestRepositoryID != "" && authority.calls[0].RepositoryID != test.wantRequestRepositoryID {
				t.Fatalf("delegated repository ID=%q, want snapshotted %q", authority.calls[0].RepositoryID, test.wantRequestRepositoryID)
			}
		})
	}
}

func TestManagedTaskRetentionDelegatesExactRecoveryPointIDs(t *testing.T) {
	db := openManagerTestDB(t)
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.TaskRepositoryLink{}, &model.RecoveryPoint{}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)

	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	repositoryID := "dddddddddddddddddddddddddddddddd"
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), DisplayName: "managed-retention",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	taskID := taskEntity.ID
	link := model.TaskRepositoryLink{
		ID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", TaskID: &taskID, RepositoryID: repositoryID,
		TaskNameSnapshot: taskEntity.Name, NodeIDSnapshot: taskEntity.NodeID, NodeNameSnapshot: taskEntity.Node.Name,
		PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	seedPointIDs := []string{
		"cccccccccccccccccccccccccccccccc",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	for index, pointID := range seedPointIDs {
		capturedAt := now.Add(time.Duration(index) * time.Minute)
		point := model.RecoveryPoint{
			ID: pointID, RepositoryID: repositoryID, ProducingTaskID: &taskID,
			ProducingTaskNameSnapshot: taskEntity.Name, ProducingNodeIDSnapshot: taskEntity.NodeID,
			ProducingNodeNameSnapshot: taskEntity.Node.Name, LineageJSON: `{}`,
			Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
			CapturedAt: &capturedAt, CommittedAt: &capturedAt, ManifestDigestAlgorithm: "sha256",
			ConsistencyJSON: `{}`, FidelityJSON: `{}`, CapabilityRevision: 1, CapabilitiesJSON: `{}`,
			ImmutabilityLevel:    string(backupasset.ImmutabilityBackendVersioned),
			PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&point).Error; err != nil {
			t.Fatal(err)
		}
	}

	var persistedPoints []model.RecoveryPoint
	if err := db.Where("repository_id = ?", repositoryID).Order("id ASC").Find(&persistedPoints).Error; err != nil {
		t.Fatal(err)
	}
	wantPointIDs := make([]string, 0, len(persistedPoints))
	for _, point := range persistedPoints {
		wantPointIDs = append(wantPointIDs, point.ID)
	}
	wantSeededPointIDs := []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"cccccccccccccccccccccccccccccccc",
	}
	if !reflect.DeepEqual(wantPointIDs, wantSeededPointIDs) {
		t.Fatalf("persisted managed RecoveryPoint IDs=%v, want %v", wantPointIDs, wantSeededPointIDs)
	}
	newSession := func() *managedRetentionLineageSessionFake {
		return &managedRetentionLineageSessionFake{
			repositoryID: repositoryID,
			points: []publication.CommittedPoint{
				{RecoveryPointID: wantPointIDs[2]},
				{RecoveryPointID: wantPointIDs[0]},
				{RecoveryPointID: wantPointIDs[1]},
			},
		}
	}
	sessions := []*managedRetentionLineageSessionFake{newSession(), newSession(), newSession()}
	guards := []*legacyLineageGuardFake{
		{session: sessions[0]},
		{session: sessions[1]},
		{session: sessions[2]},
	}
	recorder := &legacyBlockRecorderFake{}
	authority := &managedRecoveryPointRetentionFake{}
	manager.SetLegacyBlockRecorder(recorder)
	manager.SetManagedRecoveryPointRetention(authority)

	target := t.TempDir()
	stale := filepath.Join(target, "stale")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	staleAt := now.AddDate(0, 0, -30)
	if err := os.Chtimes(stale, staleAt, staleAt); err != nil {
		t.Fatal(err)
	}
	var legacyResticCalls int32
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		atomic.AddInt32(&legacyResticCalls, 1)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	sshAttempted := make(chan struct{}, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		sshAttempted <- struct{}{}
		_ = connection.Close()
	}()

	policy := model.Policy{
		ID: 71, TargetPath: target, RetentionDays: 7, RetentionMode: "gfs",
		KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 12, KeepYearly: 3,
	}
	taskEntity.ExecutorConfig = `{"repository_password":"FAKE_MANAGED_RETENTION_PASSWORD_MUST_NOT_BE_READ"}`
	taskEntity.RsyncTarget = "backup:legacy-must-not-reach-min-age"
	taskEntity.Node.Host = "127.0.0.1"
	taskEntity.Node.Port = listener.Addr().(*net.TCPAddr).Port

	taskEntity.ExecutorType = "rsync"
	manager.SetLineageGuard(guards[0])
	manager.enforceRsyncRetention(policy, taskEntity)
	taskEntity.ExecutorType = "restic"
	manager.SetLineageGuard(guards[1])
	manager.enforceResticRetention(policy, taskEntity)
	taskEntity.ExecutorType = "rclone"
	manager.SetLineageGuard(guards[2])
	manager.enforceRcloneRetention(policy, taskEntity)

	if len(authority.calls) != 3 {
		t.Fatalf("managed authority calls=%d, want one per executor", len(authority.calls))
	}
	for index, call := range authority.calls {
		if call.TaskID != taskEntity.ID || call.PolicyID != policy.ID || call.RepositoryID != repositoryID || !reflect.DeepEqual(call.RecoveryPointIDs, wantPointIDs) {
			t.Fatalf("managed authority call[%d]=%+v, want task=%d policy=%d repository=%s points=%v", index, call, taskEntity.ID, policy.ID, repositoryID, wantPointIDs)
		}
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("managed retention reached legacy directory mtime/RemoveAll path: %v", err)
	}
	if got := atomic.LoadInt32(&legacyResticCalls); got != 0 {
		t.Fatalf("managed retention reached legacy credential/SSH or Restic age/GFS path %d time(s)", got)
	}
	select {
	case <-sshAttempted:
		t.Fatal("managed retention reached legacy Rclone SSH/--min-age path")
	default:
	}
	if len(recorder.blocks) != 0 {
		t.Fatalf("successful managed delegation recorded legacy blocks: %+v", recorder.blocks)
	}
	for index, guard := range guards {
		if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
			t.Fatalf("guard[%d] calls=%d operation=%q", index, guard.calls, guard.operation)
		}
	}
	for index, session := range sessions {
		if got := atomic.LoadInt32(&session.closed); got != 1 {
			t.Fatalf("session[%d] close count=%d, want 1", index, got)
		}
		if got := atomic.LoadInt32(&session.repositoryCalls); got != 1 {
			t.Fatalf("session[%d] RepositoryID calls=%d, want 1", index, got)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestManagedResticRetentionBlocksForgetPruneBeforeCredentialAndSSH(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	policy := model.Policy{ID: 17, RetentionDays: 7}
	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)
	var legacyCalls int32
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		atomic.AddInt32(&legacyCalls, 1)
	}

	manager.enforceResticRetention(policy, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if got := atomic.LoadInt32(&legacyCalls); got != 0 {
		t.Fatalf("managed retention reached legacy credential/SSH path %d time(s)", got)
	}
	if len(recorder.blocks) != 1 || recorder.blocks[0].Operation != publication.OperationLegacyRetention || recorder.blocks[0].TaskID != taskEntity.ID || recorder.blocks[0].TaskRunID != nil {
		t.Fatalf("unexpected retention blocks: %+v", recorder.blocks)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestManagedRsyncRetentionBlocksLegacyDirectoryDeletion(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "rsync"
	target := t.TempDir()
	stale := filepath.Join(target, "stale")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	staleAt := time.Now().AddDate(0, 0, -30)
	if err := os.Chtimes(stale, staleAt, staleAt); err != nil {
		t.Fatal(err)
	}
	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)

	manager.enforceRsyncRetention(model.Policy{ID: 22, TargetPath: target, RetentionDays: 7}, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("managed Rsync retention deleted legacy directory: %v", err)
	}
	if len(recorder.blocks) != 1 || recorder.blocks[0].Operation != publication.OperationLegacyRetention {
		t.Fatalf("managed Rsync retention blocks=%+v", recorder.blocks)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestManagedRcloneRetentionBlocksLegacyDeleteBeforeSSH(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "rclone"
	taskEntity.RsyncTarget = "backup:legacy"
	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)

	manager.enforceRcloneRetention(model.Policy{ID: 23, RetentionDays: 7}, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if len(recorder.blocks) != 1 || recorder.blocks[0].Operation != publication.OperationLegacyRetention {
		t.Fatalf("managed Rclone retention blocks=%+v", recorder.blocks)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestLegacyRcloneRetentionRejectsMutableMirrorBeforeSSH(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "rclone"
	taskEntity.RsyncTarget = "backup:legacy-current-mirror"
	session := &legacyLineageSessionFake{mode: publication.LineageCompatibility}
	manager.SetLineageGuard(&legacyLineageGuardFake{session: session})
	recorder := &legacyBlockRecorderFake{}
	manager.SetLegacyBlockRecorder(recorder)

	manager.enforceRcloneRetention(model.Policy{ID: 230, RetentionDays: 7}, taskEntity)

	if len(recorder.blocks) != 1 || recorder.blocks[0].Operation != publication.OperationLegacyRetention {
		t.Fatalf("legacy Rclone retention was not visibly rejected: %+v", recorder.blocks)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("legacy Rclone lineage session close count=%d, want 1", got)
	}
}

func TestRollbackSafeDisabledRetentionRemainsBlocked(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	policy := model.Policy{ID: 18, RetentionDays: 7}
	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	manager.SetLineageGuard(guard)
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		t.Fatal("rollback-safe retention must not reach legacy path")
	}

	manager.enforceResticRetention(policy, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestPristineResticRetentionRetainsCompatibility(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	policy := model.Policy{ID: 19, RetentionDays: 7}
	session := &legacyLineageSessionFake{mode: publication.LineageCompatibility}
	guard := &legacyLineageGuardFake{session: session}
	manager.SetLineageGuard(guard)
	var legacyCalls int32
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		atomic.AddInt32(&legacyCalls, 1)
	}

	manager.enforceResticRetention(policy, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyRetention {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if got := atomic.LoadInt32(&legacyCalls); got != 1 {
		t.Fatalf("pristine retention legacy calls=%d, want 1", got)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestResticRetentionAdmissionDrainsThroughCommandAndConnectionClose(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	policy := model.Policy{ID: 20, RetentionDays: 7}
	session := &legacyLineageSessionFake{mode: publication.LineageCompatibility}
	manager.SetLineageGuard(&legacyLineageGuardFake{session: session})
	started := make(chan struct{})
	release := make(chan struct{})
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		close(started)
		<-release
	}
	finished := make(chan struct{})
	go func() {
		manager.enforceResticRetention(policy, taskEntity)
		close(finished)
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("legacy retention path did not begin")
	}
	if got := atomic.LoadInt32(&session.closed); got != 0 {
		t.Fatalf("admission closed before command/connection returned: %d", got)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("legacy retention path did not finish")
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("admission close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestManagedRestoreAndRetentionRecordTypedLegacyBlockAuditAndMetric(t *testing.T) {
	db := openManagerTestDB(t)
	restoreExecutor := &trackingRestoreExecutor{err: errors.New("must remain unreachable")}
	manager := NewManager(db, stubExecutorFactory{executor: restoreExecutor}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "restic"
	restoreRunID := createTestTaskRun(t, db, taskEntity.ID, "restore")
	recorder := &legacyBlockRecorderFake{}
	manager.SetLegacyBlockRecorder(recorder)
	manager.SetLineageGuard(&legacyLineageGuardFake{session: &legacyLineageSessionFake{mode: publication.LineageExact}})
	manager.ensureRemoteTargetReadyFunc = func(context.Context, model.Node, string) error {
		t.Fatal("managed restore must not precheck")
		return nil
	}
	manager.resticRetentionFunc = func(context.Context, model.Policy, model.Task) {
		t.Fatal("managed retention must not touch its legacy path")
	}

	manager.runRestoreTask(taskEntity.ID, restoreRunID, taskEntity)
	manager.enforceResticRetention(model.Policy{ID: 21, RetentionDays: 7}, taskEntity)

	if len(recorder.blocks) != 2 {
		t.Fatalf("legacy block count=%d, want 2", len(recorder.blocks))
	}
	if restore := recorder.blocks[0]; restore.Operation != publication.OperationLegacyRestoreLatest || restore.TaskRunID == nil || *restore.TaskRunID != restoreRunID {
		t.Fatalf("restore block=%+v", restore)
	}
	if retention := recorder.blocks[1]; retention.Operation != publication.OperationLegacyRetention || retention.TaskRunID != nil {
		t.Fatalf("retention block=%+v", retention)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}

func TestShellEscape(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"空字符串", ""},
		{"简单字符串", "hello"},
		{"包含单引号", "it's"},
		{"包含空格", "a b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shellEscape(tc.input)
			out, err := exec.Command("sh", "-c", "printf %s "+got).Output()
			if err != nil {
				t.Fatalf("shellEscape(%q) 生成的 shell 表达式执行失败: %v", tc.input, err)
			}
			if string(out) != tc.input {
				t.Fatalf("shellEscape(%q) 回放结果错误，实际 %q", tc.input, string(out))
			}
		})
	}
}

func TestResolveResticRepositoryAccessForRetention(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		expect string
	}{
		{"有效 JSON", `{"repository_password": "FAKE_RETENTION_RESTIC_PASSWORD_FOR_TEST_ONLY"}`, "FAKE_RETENTION_RESTIC_PASSWORD_FOR_TEST_ONLY"},
		{"无访问口令字段", `{"repo": "/backup"}`, ""},
		{"空访问口令", `{"repository_password": ""}`, ""},
		{"额外空白", `{"repository_password" : "FAKE_RETENTION_RESTIC_PASSWORD_WITH_SPACE_FOR_TEST_ONLY"}`, "FAKE_RETENTION_RESTIC_PASSWORD_WITH_SPACE_FOR_TEST_ONLY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			access := executor.ResolveResticRepositoryAccessOrEmpty(tc.input)
			if access.Password() != tc.expect {
				t.Fatalf("ResolveResticRepositoryAccessOrEmpty(%q): 期望 %q，实际 %q", tc.input, tc.expect, access.Password())
			}
		})
	}
}

func TestEnforceRsyncRetentionRejectsMutableTreeWithoutVersionAge(t *testing.T) {
	targetDir := t.TempDir()
	freshDir := filepath.Join(targetDir, "node-a")
	staleDir := filepath.Join(targetDir, "node-b")
	for _, path := range []string{freshDir, staleDir} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatalf("创建备份目录失败: %v", err)
		}
	}
	staleTime := time.Now().AddDate(0, 0, -30)
	if err := os.Chtimes(staleDir, staleTime, staleTime); err != nil {
		t.Fatalf("设置旧目录修改时间失败: %v", err)
	}

	db := openManagerTestDB(t)
	node := model.Node{
		Name: "node-retention-test", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthType: "key",
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("创建节点失败: %v", err)
	}
	policy := model.Policy{
		Name: "policy-retention-test", SourcePath: "/tmp/src",
		TargetPath: targetDir, CronSpec: "@daily", RetentionDays: 7,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("创建策略失败: %v", err)
	}
	taskEntity := model.Task{
		Name: "task-retention-test", NodeID: node.ID, ExecutorType: "rsync",
		Status: string(StatusPending), RsyncSource: "/tmp/src",
		RsyncTarget: targetDir, PolicyID: &policy.ID,
	}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := db.Preload("Node").First(&taskEntity, taskEntity.ID).Error; err != nil {
		t.Fatalf("加载任务节点失败: %v", err)
	}

	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	shutdownManagerOnCleanup(t, manager)
	recorder := &legacyBlockRecorderFake{}
	manager.SetLegacyBlockRecorder(recorder)
	manager.enforceRsyncRetention(policy, taskEntity)

	for _, path := range []string{freshDir, staleDir} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("旧版可变 Rsync 保留清理删除了当前材料 %q: %v", path, err)
		}
	}
	if len(recorder.blocks) != 1 || recorder.blocks[0].Operation != publication.OperationLegacyRetention {
		t.Fatalf("旧版可变 Rsync 保留清理未记录安全拒绝: %+v", recorder.blocks)
	}
}
