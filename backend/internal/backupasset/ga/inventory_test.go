package ga

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestInventoryDryRunClassifiesProvidersWithoutProviderMutation(t *testing.T) {
	sharedResticIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("a", 64)
	rsyncIdentity := "rsync-identity:v1:" + strings.Repeat("b", 64)
	rcloneIdentity := "rclone-identity:v1:" + strings.Repeat("c", 64)
	resticRepoA := strings.Repeat("1", 32)
	resticRepoB := strings.Repeat("2", 32)
	rsyncRepo := strings.Repeat("3", 32)
	rcloneRepo := strings.Repeat("4", 32)
	secretIndexPath := "/etc/passwd-fixture-secret"

	source := staticInventorySource{facts: InventoryFacts{
		Tasks: []TaskFact{
			{TaskID: 11, ExecutorType: "restic", IdentityKey: sharedResticIdentity, VersionMode: backupasset.VersionNativeSnapshot},
			{TaskID: 12, ExecutorType: "restic", IdentityKey: sharedResticIdentity, VersionMode: backupasset.VersionNativeSnapshot},
			{TaskID: 21, ExecutorType: "rsync", IdentityKey: rsyncIdentity, VersionMode: backupasset.VersionMutableHead},
			{TaskID: 31, ExecutorType: "rclone", IdentityKey: rcloneIdentity, VersionMode: backupasset.VersionMutableHead},
			{TaskID: 41, ExecutorType: "command"},
		},
		Repositories: []RepositoryFact{
			{ID: resticRepoA, ProviderKind: backupasset.ProviderRestic, IdentityKey: sharedResticIdentity, VersionMode: backupasset.VersionNativeSnapshot},
			{ID: resticRepoB, ProviderKind: backupasset.ProviderRestic, IdentityKey: sharedResticIdentity, VersionMode: backupasset.VersionNativeSnapshot},
			{ID: rsyncRepo, ProviderKind: backupasset.ProviderRsync, IdentityKey: rsyncIdentity, VersionMode: backupasset.VersionMutableHead},
			{ID: rcloneRepo, ProviderKind: backupasset.ProviderRclone, IdentityKey: rcloneIdentity, VersionMode: backupasset.VersionMutableHead},
		},
		Links: []LinkFact{
			{TaskID: 11, RepositoryID: resticRepoA},
			{TaskID: 12, RepositoryID: resticRepoB},
			{TaskID: 21, RepositoryID: rsyncRepo},
			{TaskID: 31, RepositoryID: rcloneRepo},
		},
		SnapshotIndexes: []SnapshotIndexFact{
			{TaskID: 11, Path: secretIndexPath},
			{TaskID: 21, Path: secretIndexPath},
		},
	}}
	surface := &recordingMutationSurface{}
	service := NewInventoryService(InventoryDependencies{Source: source, Mutations: surface})

	first, err := service.DryRun(context.Background())
	if err != nil {
		t.Fatalf("first dry-run: %v", err)
	}
	second, err := service.DryRun(context.Background())
	if err != nil {
		t.Fatalf("second dry-run: %v", err)
	}

	if first.Counts.Candidates != 3 || first.Counts.Conflicts != 2 || first.Counts.Unsupported != 1 || first.Counts.CapabilityGaps != 0 {
		t.Fatalf("counts=%+v, want candidates=3 conflicts=2 unsupported=1 capability_gaps=0", first.Counts)
	}
	if first.TrustedSnapshotIndex {
		t.Fatal("legacy SnapshotFileIndex must never be trusted complete")
	}
	if first.Digest == "" || first.Digest != second.Digest {
		t.Fatalf("digest must be stable and non-empty, first=%q second=%q", first.Digest, second.Digest)
	}

	restic := mustFindCandidate(t, first, sharedResticIdentity)
	if restic.Provider != backupasset.ProviderRestic || restic.VersionMode != backupasset.VersionNativeSnapshot {
		t.Fatalf("shared restic candidate provider/version=%q/%q", restic.Provider, restic.VersionMode)
	}
	if restic.OwnershipMerged {
		t.Fatal("shared Restic identity must consolidate without merging ownership")
	}
	if !sameUintSet(restic.ProducingTaskIDs, []uint{11, 12}) {
		t.Fatalf("shared restic producing tasks=%v, want 11 and 12", restic.ProducingTaskIDs)
	}
	if !sameStringSet(restic.RepositoryIDs, []string{resticRepoA, resticRepoB}) {
		t.Fatalf("shared restic repositories=%v, want both repository IDs", restic.RepositoryIDs)
	}

	rsync := mustFindCandidate(t, first, rsyncIdentity)
	if rsync.Provider != backupasset.ProviderRsync || rsync.VersionMode != backupasset.VersionMutableHead {
		t.Fatalf("rsync candidate must stay mutable_head, got %q/%q", rsync.Provider, rsync.VersionMode)
	}
	rclone := mustFindCandidate(t, first, rcloneIdentity)
	if rclone.Provider != backupasset.ProviderRclone || rclone.VersionMode != backupasset.VersionMutableHead {
		t.Fatalf("rclone candidate must stay mutable_head, got %q/%q", rclone.Provider, rclone.VersionMode)
	}

	sharedConflict := mustFindConflict(t, first, ConflictSharedResticIdentity)
	if !sameUintSet(sharedConflict.TaskIDs, []uint{11, 12}) || sharedConflict.StableReasonCode == "" {
		t.Fatalf("shared restic conflict=%+v", sharedConflict)
	}
	commandConflict := mustFindConflict(t, first, ConflictCommandUnsupported)
	if !sameUintSet(commandConflict.TaskIDs, []uint{41}) || commandConflict.StableReasonCode == "" {
		t.Fatalf("command conflict=%+v", commandConflict)
	}

	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	rendered := string(encoded) + fmt.Sprintf("%+v", first)
	for _, leak := range []string{secretIndexPath, "SnapshotFileIndex", "passwd"} {
		if strings.Contains(rendered, leak) {
			t.Fatalf("inventory document leaked %q", leak)
		}
	}
	if len(surface.calls) != 0 {
		t.Fatalf("dry-run issued Provider mutating commands %v", surface.calls)
	}
}

type staticInventorySource struct {
	facts InventoryFacts
}

func (source staticInventorySource) LoadFacts(context.Context) (InventoryFacts, error) {
	return source.facts, nil
}

type recordingMutationSurface struct {
	calls []string
}

func (surface *recordingMutationSurface) OpenProvider(_ context.Context, command string) error {
	surface.calls = append(surface.calls, "open:"+command)
	return errInventoryMustNotMutateProvider
}

func (surface *recordingMutationSurface) DiscoverImport(context.Context) error {
	surface.calls = append(surface.calls, "import")
	return errInventoryMustNotMutateProvider
}

func (surface *recordingMutationSurface) Rebuild(context.Context) error {
	surface.calls = append(surface.calls, "rebuild")
	return errInventoryMustNotMutateProvider
}

func (surface *recordingMutationSurface) Purge(context.Context) error {
	surface.calls = append(surface.calls, "purge")
	return errInventoryMustNotMutateProvider
}

func mustFindCandidate(t *testing.T, document InventoryDocument, identity string) InventoryCandidate {
	t.Helper()
	for _, candidate := range document.Candidates {
		if candidate.IdentityKey == identity {
			return candidate
		}
	}
	t.Fatalf("missing candidate for identity %q", identity)
	return InventoryCandidate{}
}

func mustFindConflict(t *testing.T, document InventoryDocument, kind ConflictKind) InventoryConflict {
	t.Helper()
	for _, conflict := range document.Conflicts {
		if conflict.Kind == kind {
			return conflict
		}
	}
	t.Fatalf("missing conflict %q", kind)
	return InventoryConflict{}
}

func sameUintSet(got, want []uint) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[uint]int, len(want))
	for _, value := range want {
		seen[value]++
	}
	for _, value := range got {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(want))
	for _, value := range want {
		seen[value]++
	}
	for _, value := range got {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}

func TestInventoryDryRunPersistsClassificationFromDatabase(t *testing.T) {
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_GA_INVENTORY_DATA_KEY_FOR_TEST_ONLY")
	now := time.Date(2026, 8, 20, 10, 15, 0, 0, time.UTC)

	t.Run("loads_db_facts_and_persists_run_conflicts_in_one_transaction", func(t *testing.T) {
		db := openInventoryTestDB(t)
		fixture := seedPersistedInventoryFacts(t, db, now)
		surface := &recordingMutationSurface{}
		service := NewInventoryService(InventoryDependencies{DB: db, Now: func() time.Time { return now }, Mutations: surface})

		first, err := service.DryRun(context.Background())
		if err != nil {
			t.Fatalf("first dry-run: %v", err)
		}
		second, err := service.DryRun(context.Background())
		if err != nil {
			t.Fatalf("second dry-run: %v", err)
		}
		if first.Class != InstallationExisting {
			t.Fatalf("class=%q, want existing", first.Class)
		}
		if first.TrustedSnapshotIndex || first.Digest == "" || first.Digest != second.Digest {
			t.Fatalf("document digest/trust first=%+v second=%+v", first, second)
		}
		if first.Counts.Candidates != 3 || first.Counts.Unsupported != 1 || first.Counts.CapabilityGaps != 1 || first.Counts.Conflicts != 4 {
			t.Fatalf("counts=%+v, want candidates=3 unsupported=1 capability_gaps=1 conflicts=4", first.Counts)
		}

		restic := mustFindCandidate(t, first, fixture.sharedResticIdentity)
		if restic.OwnershipMerged || restic.VersionMode != backupasset.VersionNativeSnapshot {
			t.Fatalf("shared restic candidate=%+v", restic)
		}
		if !sameUintSet(restic.ProducingTaskIDs, []uint{fixture.resticTaskA, fixture.resticTaskB}) {
			t.Fatalf("shared restic tasks=%v", restic.ProducingTaskIDs)
		}
		rsync := mustFindCandidate(t, first, fixture.rsyncIdentity)
		if rsync.VersionMode != backupasset.VersionMutableHead {
			t.Fatalf("rsync candidate must stay mutable_head: %+v", rsync)
		}
		rclone := mustFindCandidate(t, first, fixture.rcloneIdentity)
		if rclone.VersionMode != backupasset.VersionMutableHead {
			t.Fatalf("rclone candidate must stay mutable_head: %+v", rclone)
		}
		mustFindConflict(t, first, ConflictSharedResticIdentity)
		mustFindConflict(t, first, ConflictCommandUnsupported)
		gap := mustFindConflict(t, first, ConflictCapabilityGap)
		if !sameUintSet(gap.TaskIDs, []uint{fixture.unlinkedTask}) || gap.StableReasonCode == "" {
			t.Fatalf("capability gap=%+v", gap)
		}
		mismatch := mustFindConflict(t, first, ConflictTaskRepositoryMismatch)
		if !sameUintSet(mismatch.TaskIDs, []uint{fixture.mismatchTask}) || mismatch.StableReasonCode == "" {
			t.Fatalf("task/repository mismatch=%+v", mismatch)
		}

		encoded, err := json.Marshal(first)
		if err != nil {
			t.Fatalf("marshal document: %v", err)
		}
		rendered := string(encoded) + fmt.Sprintf("%+v", first)
		for _, leak := range []string{
			fixture.secretIndexPath, fixture.rsyncTarget, fixture.executorSecret, fixture.legacyLocator,
			"SnapshotFileIndex", "passwd",
		} {
			if strings.Contains(rendered, leak) {
				t.Fatalf("inventory document leaked %q", leak)
			}
		}
		if len(surface.calls) != 0 {
			t.Fatalf("dry-run issued Provider mutating commands %v", surface.calls)
		}

		var runs []model.BackupAssetInventoryRun
		if err := db.Order("created_at, id").Find(&runs).Error; err != nil {
			t.Fatalf("load inventory runs: %v", err)
		}
		if len(runs) != 2 {
			t.Fatalf("inventory runs=%d, want 2", len(runs))
		}
		for _, run := range runs {
			if run.Status != InventoryRunComplete || run.Digest != first.Digest || run.ErrorCategory != "" {
				t.Fatalf("complete run=%+v", run)
			}
			assertInventoryCountsJSON(t, run.CountsJSON, first.Counts)
		}
		var conflicts []model.BackupAssetRepositoryConflict
		if err := db.Where("run_id = ?", runs[0].ID).Find(&conflicts).Error; err != nil {
			t.Fatalf("load conflicts: %v", err)
		}
		if len(conflicts) != 4 {
			t.Fatalf("persisted conflicts=%d, want 4", len(conflicts))
		}
		for _, conflict := range conflicts {
			if backupasset.ValidateOpaqueID(conflict.ID) != nil || conflict.StableReasonCode == "" {
				t.Fatalf("conflict row=%+v", conflict)
			}
			if strings.Contains(conflict.TaskIDsJSON, fixture.rsyncTarget) || strings.Contains(conflict.TaskIDsJSON, fixture.legacyLocator) {
				t.Fatalf("conflict row leaked private material: %+v", conflict)
			}
		}
		var installation model.BackupAssetInstallation
		if err := db.Where("slot = ?", 1).First(&installation).Error; err != nil {
			t.Fatalf("load installation: %v", err)
		}
		if installation.Class != string(InstallationExisting) || installation.InventoryDigest != first.Digest {
			t.Fatalf("installation=%+v", installation)
		}
		if installation.Readiness == string(ReadinessReady) || installation.Readiness == string(ReadinessAcknowledged) {
			t.Fatalf("inventory must not promote readiness: %+v", installation)
		}
	})

	t.Run("persist_rolls_back_run_and_conflicts_together", func(t *testing.T) {
		db := openInventoryTestDB(t)
		seedPersistedInventoryFacts(t, db, now)
		const callbackName = "ga:fail-inventory-conflict-write"
		if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRepositoryConflict{}).TableName() {
				_ = tx.AddError(errors.New("FAKE_INVENTORY_CONFLICT_WRITE_FAILURE_FOR_TEST_ONLY"))
			}
		}); err != nil {
			t.Fatalf("register conflict failure: %v", err)
		}
		t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

		_, err := NewInventoryService(InventoryDependencies{DB: db, Now: func() time.Time { return now }}).DryRun(context.Background())
		if err == nil {
			t.Fatal("expected transactional persist failure")
		}
		var runs int64
		if err := db.Model(&model.BackupAssetInventoryRun{}).Count(&runs).Error; err != nil {
			t.Fatal(err)
		}
		var conflicts int64
		if err := db.Model(&model.BackupAssetRepositoryConflict{}).Count(&conflicts).Error; err != nil {
			t.Fatal(err)
		}
		var installations int64
		if err := db.Model(&model.BackupAssetInstallation{}).Count(&installations).Error; err != nil {
			t.Fatal(err)
		}
		if runs != 0 || conflicts != 0 || installations != 0 {
			t.Fatalf("partial persist survived rollback runs=%d conflicts=%d installations=%d", runs, conflicts, installations)
		}
	})

	t.Run("failed_run_does_not_promote_readiness", func(t *testing.T) {
		db := openInventoryTestDB(t)
		if err := db.Create(&model.BackupAssetInstallation{
			ID: strings.Repeat("a", 32), Slot: 1, Class: string(InstallationFresh),
			Readiness: string(ReadinessUnknown), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		_, err := NewInventoryService(InventoryDependencies{
			DB:     db,
			Source: failingInventorySource{err: errors.New("FAKE_INVENTORY_SOURCE_FAILURE_FOR_TEST_ONLY")},
			Now:    func() time.Time { return now },
		}).DryRun(context.Background())
		if err == nil {
			t.Fatal("expected failed dry-run")
		}
		var run model.BackupAssetInventoryRun
		if err := db.First(&run).Error; err != nil {
			t.Fatalf("failed run missing: %v", err)
		}
		if run.Status != InventoryRunFailed || run.ErrorCategory != InventoryErrorFailed || len(run.Digest) != 64 {
			t.Fatalf("failed run=%+v", run)
		}
		var installation model.BackupAssetInstallation
		if err := db.Where("slot = ?", 1).First(&installation).Error; err != nil {
			t.Fatal(err)
		}
		if installation.Readiness != string(ReadinessUnknown) || installation.InventoryDigest != "" {
			t.Fatalf("failed run promoted readiness: %+v", installation)
		}
	})

	t.Run("existing_class_never_reverses_to_fresh", func(t *testing.T) {
		db := openInventoryTestDB(t)
		if err := db.Create(&model.BackupAssetInstallation{
			ID: strings.Repeat("b", 32), Slot: 1, Class: string(InstallationExisting),
			Readiness: string(ReadinessBlocked), InventoryDigest: strings.Repeat("c", 64),
			CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		document, err := NewInventoryService(InventoryDependencies{DB: db, Now: func() time.Time { return now }}).DryRun(context.Background())
		if err != nil {
			t.Fatalf("empty dry-run: %v", err)
		}
		if document.Class != InstallationExisting {
			t.Fatalf("class reversed to %q", document.Class)
		}
		var installation model.BackupAssetInstallation
		if err := db.Where("slot = ?", 1).First(&installation).Error; err != nil {
			t.Fatal(err)
		}
		if installation.Class != string(InstallationExisting) {
			t.Fatalf("persisted class reversed: %+v", installation)
		}
	})

	t.Run("materialize_ready_and_enablement_stamp_are_used_down_latches", func(t *testing.T) {
		db := openInventoryTestDB(t)
		service := NewInventoryService(InventoryDependencies{DB: db, Now: func() time.Time { return now }})
		if _, err := service.DryRun(context.Background()); err != nil {
			t.Fatalf("fresh dry-run: %v", err)
		}
		var before model.BackupAssetInstallation
		if err := db.Where("slot = ?", 1).First(&before).Error; err != nil {
			t.Fatal(err)
		}
		if before.Readiness == string(ReadinessReady) || before.EnablementSucceededAt != nil {
			t.Fatalf("dry-run promoted used-down latches: %+v", before)
		}
		if err := service.MaterializeReadiness(context.Background(), ReadinessSnapshot{
			Class: InstallationFresh, Status: ReadinessReady, InventoryComplete: true,
			ExportRootValid: true, KeyDomainsReady: true,
		}); err != nil {
			t.Fatalf("materialize ready: %v", err)
		}
		if err := service.RecordEnablementSucceeded(context.Background()); err != nil {
			t.Fatalf("stamp enablement: %v", err)
		}
		var after model.BackupAssetInstallation
		if err := db.Where("slot = ?", 1).First(&after).Error; err != nil {
			t.Fatal(err)
		}
		if after.Readiness != string(ReadinessReady) || after.EnablementSucceededAt == nil || after.EnablementSucceededAt.IsZero() {
			t.Fatalf("used-down latches missing after materialize/stamp: %+v", after)
		}
		firstStamp := *after.EnablementSucceededAt
		later := now.Add(time.Hour)
		if err := NewInventoryService(InventoryDependencies{DB: db, Now: func() time.Time { return later }}).
			RecordEnablementSucceeded(context.Background()); err != nil {
			t.Fatalf("repeat stamp: %v", err)
		}
		var repeated model.BackupAssetInstallation
		if err := db.Where("slot = ?", 1).First(&repeated).Error; err != nil {
			t.Fatal(err)
		}
		if !repeated.EnablementSucceededAt.Equal(firstStamp) {
			t.Fatalf("enablement stamp was rewritten: first=%s got=%s", firstStamp, repeated.EnablementSucceededAt)
		}
	})

	t.Run("managed_history_latch_makes_install_existing", func(t *testing.T) {
		db := openInventoryTestDB(t)
		if err := db.Create(&model.BackupAssetManagedHistoryLatch{
			ID: "installation", Scope: "installation", FirstSemantics: "native_snapshot",
			FirstOrigin: "publication", FirstSeenAt: now, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		document, err := NewInventoryService(InventoryDependencies{DB: db, Now: func() time.Time { return now }}).DryRun(context.Background())
		if err != nil {
			t.Fatalf("latch dry-run: %v", err)
		}
		if document.Class != InstallationExisting {
			t.Fatalf("latch class=%q, want existing", document.Class)
		}
	})
}

type failingInventorySource struct{ err error }

func (source failingInventorySource) LoadFacts(context.Context) (InventoryFacts, error) {
	return InventoryFacts{}, source.err
}

type persistedInventoryFixture struct {
	sharedResticIdentity string
	rsyncIdentity        string
	rcloneIdentity       string
	resticTaskA          uint
	resticTaskB          uint
	unlinkedTask         uint
	mismatchTask         uint
	secretIndexPath      string
	rsyncTarget          string
	executorSecret       string
	legacyLocator        string
}

func seedPersistedInventoryFacts(t *testing.T, db *gorm.DB, now time.Time) persistedInventoryFixture {
	t.Helper()
	fixture := persistedInventoryFixture{
		sharedResticIdentity: provider.NativeResticIdentityPrefix + strings.Repeat("a", 64),
		rsyncIdentity:        "rsync-identity:v1:" + strings.Repeat("b", 64),
		rcloneIdentity:       "rclone-identity:v1:" + strings.Repeat("c", 64),
		secretIndexPath:      "/etc/passwd-fixture-secret",
		rsyncTarget:          "/secret/rsync-target-leak",
		executorSecret:       "super-secret-token",
		legacyLocator:        "sftp://secret-host/repo",
	}
	node := model.Node{
		Name: "ga-inventory", Host: "10.0.0.9", Port: 22, Username: "ops", AuthType: "key",
		BackupDir: "ga-inv", Status: "online", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	createTask := func(name, executor, target, config string) uint {
		t.Helper()
		task := model.Task{
			Name: name, NodeID: node.ID, ExecutorType: executor, Status: "active",
			RsyncTarget: target, ExecutorConfig: config, CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&task).Error; err != nil {
			t.Fatal(err)
		}
		return task.ID
	}
	fixture.resticTaskA = createTask("restic-a", "restic", fixture.rsyncTarget, "")
	fixture.resticTaskB = createTask("restic-b", "restic", fixture.rsyncTarget, "")
	rsyncTask := createTask("rsync-head", "rsync", fixture.rsyncTarget, `{"token":"`+fixture.executorSecret+`"}`)
	rcloneTask := createTask("rclone-head", "rclone", fixture.rsyncTarget, "")
	_ = createTask("command-task", "command", "", "echo secret")
	fixture.unlinkedTask = createTask("unlinked-restic", "restic", fixture.rsyncTarget, "")
	fixture.mismatchTask = createTask("mismatched-restic", "restic", fixture.rsyncTarget, "")

	createRepo := func(id, kind, identity, mode string) {
		t.Helper()
		identityCopy := identity
		repo := model.BackupRepository{
			ID: id, ProviderKind: kind, RepositoryIdentity: &identityCopy, DisplayName: kind + "-repo",
			VersionMode: mode, Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
			CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&repo).Error; err != nil {
			t.Fatal(err)
		}
	}
	resticRepoA := strings.Repeat("1", 32)
	resticRepoB := strings.Repeat("2", 32)
	rsyncRepo := strings.Repeat("3", 32)
	rcloneRepo := strings.Repeat("4", 32)
	mismatchRepo := strings.Repeat("5", 32)
	createRepo(resticRepoA, string(backupasset.ProviderRestic), fixture.sharedResticIdentity, string(backupasset.VersionNativeSnapshot))
	createRepo(resticRepoB, string(backupasset.ProviderRestic), fixture.sharedResticIdentity, string(backupasset.VersionNativeSnapshot))
	createRepo(rsyncRepo, string(backupasset.ProviderRsync), fixture.rsyncIdentity, string(backupasset.VersionMutableHead))
	createRepo(rcloneRepo, string(backupasset.ProviderRclone), fixture.rcloneIdentity, string(backupasset.VersionMutableHead))
	createRepo(mismatchRepo, string(backupasset.ProviderRsync), fixture.rsyncIdentity, string(backupasset.VersionMutableHead))

	createLink := func(id string, taskID uint, repositoryID string) {
		t.Helper()
		link := model.TaskRepositoryLink{
			ID: id, TaskID: &taskID, RepositoryID: repositoryID, TaskNameSnapshot: "linked",
			NodeIDSnapshot: node.ID, NodeNameSnapshot: node.Name,
			PublicationMode:        string(backupasset.PublicationNativeSnapshot),
			EncryptedLegacyLocator: fixture.legacyLocator, LinkedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&link).Error; err != nil {
			t.Fatal(err)
		}
	}
	createLink(strings.Repeat("6", 32), fixture.resticTaskA, resticRepoA)
	createLink(strings.Repeat("7", 32), fixture.resticTaskB, resticRepoB)
	createLink(strings.Repeat("8", 32), rsyncTask, rsyncRepo)
	createLink(strings.Repeat("9", 32), rcloneTask, rcloneRepo)
	createLink(strings.Repeat("0", 32), fixture.mismatchTask, mismatchRepo)

	if err := db.Create(&model.SnapshotFileIndex{
		TaskID: fixture.resticTaskA, SnapshotID: "snap-secret", Path: fixture.secretIndexPath, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetManagedHistoryLatch{
		ID: "repository:" + resticRepoA, Scope: "repository", RepositoryID: &resticRepoA,
		FirstSemantics: "native_snapshot", FirstOrigin: "publication", FirstSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return fixture
}

func openInventoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.Node{}, &model.Task{}, &model.SnapshotFileIndex{},
		&model.BackupRepository{}, &model.TaskRepositoryLink{},
		&model.BackupAssetManagedHistoryLatch{}, &model.RecoveryPointLifecycleTombstone{},
		&model.BackupAssetInstallation{}, &model.BackupAssetInventoryRun{}, &model.BackupAssetRepositoryConflict{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func assertInventoryCountsJSON(t *testing.T, raw string, want InventoryCounts) {
	t.Helper()
	var got InventoryCounts
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("counts_json=%q: %v", raw, err)
	}
	if got != want {
		t.Fatalf("counts_json=%+v, want %+v", got, want)
	}
	if strings.Contains(raw, "/") || strings.Contains(raw, "sftp") || strings.Contains(raw, "passwd") {
		t.Fatalf("counts_json contained private material: %s", raw)
	}
}

func TestInventoryConflictCountFromCountsJSON(t *testing.T) {
	if got := inventoryConflictCount(`{"candidates":4,"conflicts":2,"unsupported":0,"capability_gaps":0}`); got != 2 {
		t.Fatalf("conflicts=%d, want 2", got)
	}
	if got := inventoryConflictCount(`not-json`); got != 0 {
		t.Fatalf("invalid counts=%d, want 0", got)
	}
}
