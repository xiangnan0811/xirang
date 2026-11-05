package repository

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/fileaccess"
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
	// Historical link/point snapshots remain immutable after a Task moves nodes;
	// Operator authority follows the live Task's current NodeID.
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

func TestVisibilityRequiresLiveCurrentTaskNodeAcrossRepositoryAndLineages(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		missingNode bool
	}{
		{name: "archived"},
		{name: "missing_with_stale_owner", missingNode: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newRepositoryTestDB(t)
			now := time.Date(2026, 8, 28, 19, 30, 0, 0, time.UTC)
			task := seedTask(t, db, "restic", "sftp:user@example.invalid:/unavailable-node", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
			const operatorID uint = 703
			if err := db.Create(&model.User{
				ID: operatorID, Username: "node-liveness-operator", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY",
				Role: "operator", CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.NodeOwner{NodeID: task.NodeID, UserID: operatorID, CreatedAt: now}).Error; err != nil {
				t.Fatal(err)
			}
			repository := seedVisibilityRepository(t, db, strings.Repeat("b", 32), "unavailable-current-node", now)
			seedVisibilityLink(t, db, strings.Repeat("c", 32), repository.ID, &task.ID, "retained-link", task.NodeID, now)
			point := seedVisibilityPoint(t, db, strings.Repeat("d", 32), repository.ID, &task.ID, "retained-point", task.NodeID, now)
			if testCase.missingNode {
				deleteVisibilityNodeRetainingOwner(t, db, task.NodeID)
			} else if err := db.Model(&model.Node{}).Where("id = ?", task.NodeID).Update("archived", true).Error; err != nil {
				t.Fatal(err)
			}

			service := newVisibilityServiceForTest(t, db, now)
			operatorScope := VisibilityScope{Role: "operator", UserID: operatorID}
			operatorPage, err := service.List(context.Background(), RepositoryListRequest{Limit: 20}, operatorScope, RequestContext{})
			if err != nil {
				t.Fatal(err)
			}
			if len(operatorPage.Items) != 0 {
				t.Fatalf("operator repository visibility=%+v through unavailable current node", operatorPage.Items)
			}
			operatorLineages, err := service.loadLineages(context.Background(), repository.ID, operatorScope)
			if err != nil {
				t.Fatal(err)
			}
			if len(operatorLineages) != 0 {
				t.Fatalf("operator lineages=%+v through unavailable current node", operatorLineages)
			}

			adminScope := VisibilityScope{Role: "admin", UserID: 1}
			adminPage, err := service.List(context.Background(), RepositoryListRequest{Limit: 20}, adminScope, RequestContext{})
			if err != nil || len(adminPage.Items) != 1 || adminPage.Items[0].ID != repository.ID {
				t.Fatalf("admin repository visibility=%+v err=%v", adminPage, err)
			}
			adminLineages, err := service.loadLineages(context.Background(), repository.ID, adminScope)
			if err != nil {
				t.Fatal(err)
			}
			if len(adminLineages) != 2 || adminLineages[0].TaskRepositoryLinkID == "" || adminLineages[1].RecoveryPointID != point.ID {
				t.Fatalf("admin provenance lineages=%+v", adminLineages)
			}
		})
	}
}

func deleteVisibilityNodeRetainingOwner(t *testing.T, db *gorm.DB, nodeID uint) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM nodes WHERE id = ?", nodeID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatal(err)
	}
	var owners int64
	if err := db.Model(&model.NodeOwner{}).Where("node_id = ?", nodeID).Count(&owners).Error; err != nil {
		t.Fatal(err)
	}
	if owners == 0 {
		t.Fatal("missing-node fixture did not retain stale ownership evidence")
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

func TestManagedRsyncCatalogBuildCompletesWithStrongFingerprint(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local provider access is Linux-only")
	}
	fixture := newManagedRsyncCatalogFixture(t)
	if _, err := fixture.keyring.Ensure(context.Background(), backupasset.KeyDomainEntryIdentity); err != nil {
		t.Fatal(err)
	}
	indexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
		DB: fixture.db, Factory: fixture.factory, Lease: fixture.lease, IdentityKeys: fixture.keyring,
		Now:    func() time.Time { return fixture.now },
		Config: catalog.IndexerConfig{BatchSize: 100, BuildTimeout: time.Minute, MaxEntries: 100, HeartbeatInterval: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := indexer.Build(context.Background(), catalog.BuildRequest{
		RepositoryID: fixture.repository.ID, RecoveryPointID: fixture.point.ID,
	})
	if err != nil {
		t.Fatalf("build real managed-Rsync Catalog: %v", err)
	}
	if generation.State != string(catalog.GenerationComplete) || !generation.IsActive {
		t.Fatalf("Catalog generation=%+v, want active complete", generation)
	}
	var entry model.CatalogEntry
	if err := fixture.db.Where("generation_id = ?", generation.ID).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("managed Catalog payload\n")))
	if entry.Fingerprint != want || entry.FingerprintStrength != string(catalog.FingerprintStrong) {
		t.Fatalf("persisted committed entry fingerprint=%q/%q, want %s/strong", entry.Fingerprint, entry.FingerprintStrength, want)
	}
	service, ok := fixture.factory.(*Service)
	if !ok {
		t.Fatalf("Catalog factory=%T, want repository service", fixture.factory)
	}
	for _, test := range []struct {
		offset int64
		length int64
		want   string
	}{
		{offset: 8, length: 7, want: "Catalog"},
		{offset: int64(len("managed Catalog payload\n") - 1), length: 1, want: "\n"},
	} {
		session, err := service.OpenContentSource(context.Background(), content.SourceRequest{
			Ref: backupasset.AssetRef{RecoveryPointID: fixture.point.ID, EntryID: entry.EntryID}, CatalogGenerationID: generation.ID,
			ExpectedSource: fixture.point.SourceFingerprint, ExpectedEntry: entry.Fingerprint, Mode: content.SourceModeRange,
			MaxBytes: test.length, Range: &content.ResolvedRange{Offset: test.offset, Length: test.length},
		})
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(session.Reader())
		closeErr := session.Close()
		if readErr != nil || closeErr != nil || string(got) != test.want || !session.Stat().FingerprintStrong || !session.Capabilities().Range {
			t.Fatalf("range offset=%d bytes=%q read=%v close=%v", test.offset, got, readErr, closeErr)
		}
	}
}

func TestRsyncCatalogStrongFingerprintUpgradeSQLite(t *testing.T) {
	runRsyncCatalogStrongFingerprintUpgrade(t, "sqlite")
}

func TestRsyncCatalogStrongFingerprintUpgradePostgres(t *testing.T) {
	runRsyncCatalogStrongFingerprintUpgrade(t, "postgres")
}

func runRsyncCatalogStrongFingerprintUpgrade(t *testing.T, engine string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("strict local provider access is Linux-only")
	}
	runHistoricalUpgrade := func(t *testing.T, strength catalog.FingerprintStrength, historicalFingerprint string) {
		t.Helper()
		db := openProducerRetentionIntegrationDB(t, engine)
		sourceModifiedAt := time.Date(2026, 7, 15, 10, 0, 0, 123456876, time.UTC)
		fixture := managedRsyncCatalogFixtureFromPublication(t, newRsyncCatalogPublicationFixtureWithDB(t, db), &sourceModifiedAt)
		identityKey, err := fixture.keyring.Ensure(context.Background(), backupasset.KeyDomainEntryIdentity)
		if err != nil {
			t.Fatal(err)
		}
		historicalIdentity, err := catalog.DeriveEntryIdentity(identityKey.Key, fixture.point.ID, "payload.txt")
		if err != nil {
			t.Fatal(err)
		}
		service, ok := fixture.factory.(*Service)
		if !ok {
			t.Fatalf("Catalog factory=%T, want repository service", fixture.factory)
		}
		wantPrecision := time.Duration(0)
		wantCapture := fixture.now
		if engine == "postgres" {
			wantPrecision = time.Microsecond
			wantCapture = fixture.now.Truncate(time.Microsecond)
		}
		if fixture.point.CapturedAt == nil || !fixture.point.CapturedAt.Equal(wantCapture) ||
			fixture.now.Nanosecond()%1000 != 876 {
			t.Fatalf("stored capture=%v signed capture=%s precision=%s", fixture.point.CapturedAt, fixture.now, wantPrecision)
		}
		var committedRequest provider.RsyncCommittedPointReadRequest
		if err := db.Transaction(func(tx *gorm.DB) error {
			runtimeAccess, err := loadLockedExactManagedRsyncPublicationRuntime(context.Background(), tx, *fixture.point.ProducingTaskID)
			if err != nil {
				return err
			}
			committedRequest, _, err = service.managedRsyncCommittedPointReadRequest(context.Background(), tx, runtimeAccess, fixture.point)
			return err
		}); err != nil {
			t.Fatalf("construct committed reader from transaction: %v", err)
		}
		if committedRequest.CapturedAtPrecision != wantPrecision || !committedRequest.CapturedAt.Equal(wantCapture) ||
			!committedRequest.Attempt.PointDeadlineAt.Equal(fixture.now.Add(168*time.Hour)) {
			t.Fatalf("transaction capture=%s precision=%s deadline=%s", committedRequest.CapturedAt, committedRequest.CapturedAtPrecision, committedRequest.Attempt.PointDeadlineAt)
		}
		if _, err := provider.NewRsyncCommittedPointRuntimeAccess(context.Background(), committedRequest); err != nil {
			t.Fatalf("authenticate transaction-derived committed point: %v", err)
		}
		var manifest model.RecoveryPointManifest
		if err := db.Where("recovery_point_id = ? AND is_active = ?", fixture.point.ID, true).First(&manifest).Error; err != nil {
			t.Fatal(err)
		}
		oldProjection, err := provider.NewCatalogProjectionAccumulator(backupasset.ProviderRsync, fixture.repository.ID, fixture.point.ID, fixture.point.SourceFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		if err := oldProjection.Write(provider.CatalogRecord{
			NormalizedPath: "payload.txt", Name: "payload.txt", Type: backupasset.CatalogEntryFile,
			Size: int64(len("managed Catalog payload\n")), Fingerprint: historicalFingerprint, FingerprintStrength: string(strength),
		}); err != nil {
			t.Fatal(err)
		}
		oldProjectionDigest, oldProjectionCount, err := oldProjection.Finalize()
		if err != nil || oldProjectionCount != fixture.point.EntryCount {
			t.Fatalf("finalize retained Catalog projection: count=%d err=%v", oldProjectionCount, err)
		}
		finished := fixture.now
		old := model.CatalogGeneration{
			ID: strings.Repeat("4", 32), RecoveryPointID: fixture.point.ID, ManifestID: &manifest.ID, Generation: 1,
			State: string(catalog.GenerationComplete), IsActive: true, SourceFingerprint: fixture.point.SourceFingerprint,
			ExpectedEntryCount: fixture.point.EntryCount, WrittenEntryCount: fixture.point.EntryCount,
			ExpectedDigest: fixture.point.ManifestDigest, WrittenDigest: oldProjectionDigest,
			StartedAt: fixture.now, FinishedAt: &finished, CreatedAt: fixture.now, UpdatedAt: fixture.now,
		}
		if err := db.Create(&old).Error; err != nil {
			t.Fatal(err)
		}
		// Seed a retained pre-upgrade generation, rather than changing a newly
		// produced strong Catalog to make it look like historical data.
		entry := model.CatalogEntry{
			GenerationID: old.ID, EntryID: historicalIdentity.EntryID, RecoveryPointID: fixture.point.ID,
			NormalizedPath: "payload.txt", Name: "payload.txt", EntryType: string(backupasset.CatalogEntryFile),
			Size: int64(len("managed Catalog payload\n")), Fingerprint: historicalFingerprint, FingerprintStrength: string(strength),
			EncryptedProviderLocator: `{"version":1,"native":"payload.txt"}`, SecurityState: "sealed", CreatedAt: fixture.now,
		}
		if err := db.Create(&entry).Error; err != nil {
			t.Fatal(err)
		}
		job := model.BackupAssetProcessingJob{
			ID: strings.Repeat("5", 32), WorkKey: strings.Repeat("6", 64), DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`),
			RecoveryPointID: fixture.point.ID, CatalogGenerationID: old.ID, EntryID: entry.EntryID,
			SourceFingerprint: fixture.point.SourceFingerprint, ProviderCapabilityRevision: int64(fixture.point.CapabilityRevision),
			Capability: "malware.scan", CapabilitySchema: "malware.scan.v1", PipelineFingerprint: "historical-pipeline",
			OutputProfile: "signature_scan_v1", SecurityPolicyRevision: "policy-v1", PriorityClass: "background",
			State: "queued", TransitionRevision: 1, IsCurrent: true, QueuedAt: fixture.now,
			AbsoluteDeadline: fixture.now.Add(time.Hour), CreatedAt: fixture.now, UpdatedAt: fixture.now, Version: 1,
		}
		if err := db.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
		const (
			candidatePageSize     = 20
			historicalPrefixCount = 401
		)
		seedHistoricalCatalogPrefix(t, fixture, manifest.ID, identityKey.Key, historicalPrefixCount)
		var prefixCount, pointCount int64
		if err := db.Model(&model.RecoveryPoint{}).
			Where("created_at < (SELECT created_at FROM recovery_points WHERE id = ?)", fixture.point.ID).
			Count(&prefixCount).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.RecoveryPoint{}).Count(&pointCount).Error; err != nil {
			t.Fatal(err)
		}
		if prefixCount != historicalPrefixCount || pointCount != historicalPrefixCount+1 {
			t.Fatalf("historical prefix=%d total points=%d, want %d and %d", prefixCount, pointCount, historicalPrefixCount, historicalPrefixCount+1)
		}
		indexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
			DB: db, Factory: fixture.factory, Lease: fixture.lease, IdentityKeys: fixture.keyring,
			Now:    func() time.Time { return fixture.now },
			Config: catalog.IndexerConfig{BatchSize: 2, BuildTimeout: time.Minute, MaxEntries: 100, HeartbeatInterval: time.Second},
		})
		if err != nil {
			t.Fatal(err)
		}
		config := backupasset.CatalogConfig{Enabled: true, BatchSize: 2, BuildTimeout: time.Minute, ReconcileInterval: time.Minute, MaxConcurrency: 1, MaxEntries: 100}
		// A complete window is 21 pages: the former limit*20 scan stopped at
		// point 400 and could never reach the publication-backed point 402.
		const windowPages = (historicalPrefixCount + 1 + candidatePageSize - 1) / candidatePageSize
		collectWindow := func(at time.Time) []catalog.BuildCandidate {
			t.Helper()
			var found []catalog.BuildCandidate
			for page := range windowPages {
				candidates, err := indexer.ListCandidates(context.Background(), candidatePageSize, at, config)
				if err != nil {
					t.Fatalf("list Catalog window page %d: %v", page+1, err)
				}
				if len(candidates) > candidatePageSize || (page < windowPages-1 && len(candidates) != 0) {
					t.Fatalf("unexpected Catalog window page %d: %+v", page+1, candidates)
				}
				found = append(found, candidates...)
			}
			var sweep model.CatalogCandidateSweep
			if err := db.First(&sweep, "id = ?", 1).Error; err != nil {
				t.Fatalf("load Catalog sweep after complete window: %v", err)
			}
			if sweep.AfterCreatedAt != nil || sweep.AfterPointID != nil || sweep.HighWaterCreatedAt != nil || sweep.HighWaterPointID != nil {
				t.Fatalf("Catalog window did not wrap: %+v", sweep)
			}
			return found
		}
		candidates := collectWindow(fixture.now)
		if len(candidates) != 1 || candidates[0].RepositoryID != fixture.repository.ID || candidates[0].RecoveryPointID != fixture.point.ID {
			t.Fatalf("historical upgrade candidate=%+v", candidates)
		}
		file := filepath.Join(fixture.treeRoot, "payload.txt")
		stat, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if !stat.ModTime().Equal(sourceModifiedAt) || stat.ModTime().Nanosecond()%1000 != 876 {
			t.Fatalf("published physical mtime=%s, want original %s", stat.ModTime(), sourceModifiedAt)
		}
		writeAtOriginalTime := func(body string) {
			t.Helper()
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(file, stat.ModTime(), stat.ModTime()); err != nil {
				t.Fatal(err)
			}
		}
		writeAtOriginalTime(strings.Repeat("x", len("managed Catalog payload\n")))
		request := catalog.BuildRequest{RepositoryID: fixture.repository.ID, RecoveryPointID: fixture.point.ID}
		failed, err := indexer.Build(context.Background(), request)
		if err == nil || failed.ID == "" {
			t.Fatalf("tampered source upgrade generation=%+v err=%v", failed, err)
		}
		var active model.CatalogGeneration
		if err := db.Where("recovery_point_id = ? AND is_active = ?", fixture.point.ID, true).First(&active).Error; err != nil || active.ID != old.ID {
			t.Fatalf("active after rejected source=%+v err=%v", active, err)
		}
		writeAtOriginalTime("managed Catalog payload\n")
		var failedStored model.CatalogGeneration
		if err := db.First(&failedStored, "id = ?", failed.ID).Error; err != nil || failedStored.FinishedAt == nil {
			t.Fatalf("failed generation=%+v err=%v", failedStored, err)
		}
		candidates = collectWindow(fixture.now)
		if len(candidates) != 0 {
			t.Fatalf("retry bypassed delay: candidates=%+v", candidates)
		}
		retryAt := failedStored.FinishedAt.UTC().Add(catalog.RetryDelay(config, 1, fixture.point.ID, failed.ID) + time.Second)
		candidates = collectWindow(retryAt)
		if len(candidates) != 1 || candidates[0].RepositoryID != fixture.repository.ID || candidates[0].RecoveryPointID != fixture.point.ID {
			t.Fatalf("retry candidate=%+v", candidates)
		}
		replacement, err := indexer.Build(context.Background(), request)
		if err != nil || !replacement.IsActive || replacement.State != string(catalog.GenerationComplete) || replacement.ID == old.ID {
			t.Fatalf("upgraded generation=%+v err=%v", replacement, err)
		}
		var upgraded model.CatalogEntry
		if err := db.Where("generation_id = ?", replacement.ID).First(&upgraded).Error; err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("%x", sha256.Sum256([]byte("managed Catalog payload\n")))
		if upgraded.EntryID != entry.EntryID || upgraded.Fingerprint != want || upgraded.FingerprintStrength != string(catalog.FingerprintStrong) {
			t.Fatalf("new entry identity=%q fingerprint=%q/%q, want %q and %s/strong", upgraded.EntryID, upgraded.Fingerprint, upgraded.FingerprintStrength, entry.EntryID, want)
		}
		sourceRequest := content.SourceRequest{
			Ref: backupasset.AssetRef{RecoveryPointID: fixture.point.ID, EntryID: upgraded.EntryID}, CatalogGenerationID: replacement.ID,
			ExpectedSource: fixture.point.SourceFingerprint, ExpectedEntry: upgraded.Fingerprint, Mode: content.SourceModeRange,
			MaxBytes: 7, Range: &content.ResolvedRange{Offset: 8, Length: 7},
		}
		record, err := service.loadExactContentRecord(context.Background(), sourceRequest)
		if err != nil {
			t.Fatalf("load durable Catalog record: %v", err)
		}
		pointSession, err := service.BeginManagedRsyncPointRead(context.Background(), *fixture.point.ProducingTaskID, fixture.point.ID)
		if err != nil {
			t.Fatalf("open authenticated point for physical stat: %v", err)
		}
		physical, statErr := pointSession.StatEntry(context.Background(), record.locator)
		pointCloseErr := pointSession.Close()
		if statErr != nil || pointCloseErr != nil || physical.Type != backupasset.CatalogEntryFile || physical.Size != upgraded.Size ||
			physical.SourceRevision == "" || upgraded.ModifiedAt == nil || !physical.ModTime.Equal(sourceModifiedAt) {
			t.Fatalf("authenticated stat type=%s size=%d revision_present=%t physical=%s stored=%v stat=%v close=%v",
				physical.Type, physical.Size, physical.SourceRevision != "", physical.ModTime, upgraded.ModifiedAt, statErr, pointCloseErr)
		}
		unprojectedErr := validateContentEntryStat(record, physical, false)
		if engine == "postgres" {
			if physical.ModTime.Equal(*upgraded.ModifiedAt) || !physical.ModTime.Truncate(time.Microsecond).Equal(*upgraded.ModifiedAt) ||
				!errors.Is(unprojectedErr, backupasset.ErrConflict) || record.physicalModTime != nil {
				t.Fatalf("PostgreSQL mtime predicate physical=%s stored=%s override=%v unprojected_error=%v",
					physical.ModTime, *upgraded.ModifiedAt, record.physicalModTime, unprojectedErr)
			}
		} else if !physical.ModTime.Equal(*upgraded.ModifiedAt) || unprojectedErr != nil {
			t.Fatalf("SQLite mtime predicate physical=%s stored=%s error=%v", physical.ModTime, *upgraded.ModifiedAt, unprojectedErr)
		}
		wholeRequest := sourceRequest
		wholeRequest.Mode = content.SourceModeSequential
		wholeRequest.Range = nil
		wholeRequest.MaxBytes = upgraded.Size
		wholeSession, err := service.OpenContentSource(context.Background(), wholeRequest)
		if err != nil {
			t.Fatalf("open upgraded committed whole file: %v", err)
		}
		wholePayload, wholeReadErr := io.ReadAll(wholeSession.Reader())
		wholeCloseErr := wholeSession.Close()
		if wholeReadErr != nil || wholeCloseErr != nil || string(wholePayload) != "managed Catalog payload\n" ||
			fmt.Sprintf("%x", sha256.Sum256(wholePayload)) != want || !wholeSession.Stat().FingerprintStrong || !wholeSession.Capabilities().Sequential {
			t.Fatalf("upgraded whole file=%q read=%v close=%v", wholePayload, wholeReadErr, wholeCloseErr)
		}
		rangeSession, err := service.OpenContentSource(context.Background(), sourceRequest)
		if err != nil {
			t.Fatalf("open upgraded committed range: %v", err)
		}
		payload, readErr := io.ReadAll(rangeSession.Reader())
		closeErr := rangeSession.Close()
		if readErr != nil || closeErr != nil || string(payload) != "Catalog" || !rangeSession.Stat().FingerprintStrong ||
			rangeSession.Stat().ModifiedAt == nil || !rangeSession.Stat().ModifiedAt.Equal(*upgraded.ModifiedAt) {
			t.Fatalf("upgraded range=%q read=%v close=%v", payload, readErr, closeErr)
		}
		durableOriginal := *upgraded.ModifiedAt
		storedDrift := durableOriginal.Add(time.Nanosecond)
		if engine == "postgres" {
			storedDrift = durableOriginal.Add(time.Microsecond)
		}
		writeCatalogTime := func(value time.Time) {
			t.Helper()
			if err := db.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", replacement.ID, upgraded.EntryID).
				UpdateColumn("modified_at", value).Error; err != nil {
				t.Fatal(err)
			}
		}
		writeCatalogTime(storedDrift)
		if session, err := service.OpenContentSource(context.Background(), sourceRequest); !errors.Is(err, backupasset.ErrConflict) {
			if session != nil {
				_ = session.Close()
			}
			t.Fatalf("stored Catalog mtime drift accepted: %v", err)
		}
		writeCatalogTime(durableOriginal)
		durableSession, err := service.OpenContentSource(context.Background(), sourceRequest)
		if err != nil {
			t.Fatalf("open before durable mtime drift: %v", err)
		}
		writeCatalogTime(storedDrift)
		if err := durableSession.Revalidate(context.Background()); !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("durable mtime drift during session accepted: %v", err)
		}
		if err := durableSession.Close(); !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("close after durable mtime drift: %v", err)
		}
		writeCatalogTime(durableOriginal)
		physicalDrift := stat.ModTime().Add(time.Nanosecond)
		if physicalDrift.Equal(stat.ModTime()) || !physicalDrift.Truncate(time.Microsecond).Equal(stat.ModTime().Truncate(time.Microsecond)) {
			t.Fatal("fixture physical drift must stay inside the original microsecond")
		}

		beforeOpen, err := service.BeginManagedRsyncPointRead(context.Background(), *fixture.point.ProducingTaskID, fixture.point.ID)
		if err != nil {
			t.Fatal(err)
		}
		beforeOpen.adapter = &rsyncRangeBeforeOpenTestAdapter{
			managedRsyncPointReadAdapter: beforeOpen.adapter,
			before:                       func() error { return os.Chtimes(file, physicalDrift, physicalDrift) },
		}
		if session, err := service.openManagedRsyncContentSession(context.Background(), sourceRequest, record, beforeOpen); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
			if session != nil {
				_ = session.Close()
			}
			t.Fatalf("same-microsecond physical drift before range open: %v", err)
		}
		if err := os.Chtimes(file, stat.ModTime(), stat.ModTime()); err != nil {
			t.Fatal(err)
		}
		physicalSession, err := service.OpenContentSource(context.Background(), sourceRequest)
		if err != nil {
			t.Fatalf("open before physical mtime drift: %v", err)
		}
		if err := os.Chtimes(file, physicalDrift, physicalDrift); err != nil {
			t.Fatal(err)
		}
		if err := physicalSession.Close(); !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("close after same-microsecond physical drift: %v", err)
		}
		if err := os.Chtimes(file, stat.ModTime(), stat.ModTime()); err != nil {
			t.Fatal(err)
		}
		var oldStored model.CatalogGeneration
		var oldEntry model.CatalogEntry
		var oldJob model.BackupAssetProcessingJob
		if err := db.First(&oldStored, "id = ?", old.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&oldEntry, "generation_id = ? AND entry_id = ?", old.ID, entry.EntryID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&oldJob, "id = ?", job.ID).Error; err != nil {
			t.Fatal(err)
		}
		if oldStored.IsActive || oldStored.State != string(catalog.GenerationSuperseded) ||
			oldStored.ManifestID == nil || *oldStored.ManifestID != manifest.ID || oldStored.SourceFingerprint != fixture.point.SourceFingerprint ||
			oldStored.WrittenDigest != oldProjectionDigest || oldEntry.RecoveryPointID != fixture.point.ID || oldEntry.Fingerprint != historicalFingerprint ||
			oldEntry.FingerprintStrength != string(strength) || oldJob.RecoveryPointID != fixture.point.ID ||
			oldJob.CatalogGenerationID != old.ID || oldJob.EntryID != entry.EntryID {
			t.Fatalf("old generation=%+v entry=%+v job=%+v", oldStored, oldEntry, oldJob)
		}
		candidates = collectWindow(retryAt)
		if len(candidates) != 0 {
			t.Fatalf("strong generation rebuilt again: candidates=%+v", candidates)
		}
	}
	t.Run("historical_upgrade", func(t *testing.T) {
		runHistoricalUpgrade(t, catalog.FingerprintNone, "")
	})
	t.Run("historical_weak_upgrade", func(t *testing.T) {
		runHistoricalUpgrade(t, catalog.FingerprintWeak, "legacy-weak-fingerprint")
	})
}

// The prefix models retained historical Catalog rows only. The point exercised
// by the upgrade is created separately by the real publication fixture.
func seedHistoricalCatalogPrefix(t *testing.T, fixture managedRsyncCatalogFixture, targetManifestID string, identityKey []byte, count int) {
	t.Helper()
	createdAt := fixture.point.CreatedAt.Add(-time.Hour).UTC()
	const body = "historical prefix payload\n"
	strongFingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	points := make([]model.RecoveryPoint, 0, count)
	manifests := make([]model.RecoveryPointManifest, 0, count)
	generations := make([]model.CatalogGeneration, 0, count)
	entries := make([]model.CatalogEntry, 0, count)
	for index := range count {
		pointID := fmt.Sprintf("%032x", 0x3000+index)
		if pointID == fixture.point.ID {
			pointID = fmt.Sprintf("%032x", 0x3000+count+index)
		}
		manifestID := fmt.Sprintf("%032x", 0x5000+index)
		if manifestID == targetManifestID {
			manifestID = fmt.Sprintf("%032x", 0x5000+count+index)
		}
		generationID := fmt.Sprintf("%032x", 0x7000+index)
		sourceFingerprint := fmt.Sprintf("%064x", index+1)
		if sourceFingerprint == fixture.point.SourceFingerprint {
			sourceFingerprint = fmt.Sprintf("%064x", count+index+1)
		}
		manifestDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("historical manifest %d", index))))
		identity, err := catalog.DeriveEntryIdentity(identityKey, pointID, "prefix.txt")
		if err != nil {
			t.Fatal(err)
		}
		projection, err := provider.NewCatalogProjectionAccumulator(backupasset.ProviderRsync, fixture.repository.ID, pointID, sourceFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		if err := projection.Write(provider.CatalogRecord{
			NormalizedPath: "prefix.txt", Name: "prefix.txt", Type: backupasset.CatalogEntryFile, Size: int64(len(body)),
			Fingerprint: strongFingerprint, FingerprintStrength: string(catalog.FingerprintStrong),
		}); err != nil {
			t.Fatal(err)
		}
		projectionDigest, projectionCount, err := projection.Finalize()
		if err != nil || projectionCount != 1 {
			t.Fatalf("finalize historical Catalog projection: count=%d err=%v", projectionCount, err)
		}
		points = append(points, model.RecoveryPoint{
			ID: pointID, RepositoryID: fixture.repository.ID, Semantics: string(backupasset.PointXirangManifest),
			State: string(backupasset.RecoveryPointCommitted), LineageJSON: "{}", CapturedAt: &createdAt, CommittedAt: &createdAt,
			SourceFingerprint: sourceFingerprint, ManifestDigestAlgorithm: "sha256", ManifestDigest: manifestDigest,
			EntryCount: 1, LogicalBytes: int64(len(body)), ConsistencyJSON: "{}", FidelityJSON: "{}",
			CapabilityRevision: fixture.repository.CapabilityRevision, CapabilitiesJSON: "{}",
			ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), PhysicalAvailability: string(backupasset.PhysicalOnline),
			HoldState: string(backupasset.HoldNone), CreatedAt: createdAt, UpdatedAt: createdAt,
		})
		manifests = append(manifests, model.RecoveryPointManifest{
			ID: manifestID, RecoveryPointID: pointID, Revision: 1, DigestAlgorithm: "sha256", Digest: manifestDigest,
			Generator: "historical-test-fixture", GeneratorVersion: "1", Completeness: string(backupasset.ManifestComplete),
			EntryCount: 1, LogicalBytes: int64(len(body)), FidelityJSON: "{}", IsActive: true, CreatedAt: createdAt, UpdatedAt: createdAt,
		})
		generations = append(generations, model.CatalogGeneration{
			ID: generationID, RecoveryPointID: pointID, ManifestID: &manifestID, Generation: 1,
			State: string(catalog.GenerationComplete), IsActive: true, SourceFingerprint: sourceFingerprint,
			ExpectedEntryCount: 1, WrittenEntryCount: 1, ExpectedDigest: manifestDigest, WrittenDigest: projectionDigest,
			StartedAt: createdAt, FinishedAt: &createdAt, CreatedAt: createdAt, UpdatedAt: createdAt,
		})
		entries = append(entries, model.CatalogEntry{
			GenerationID: generationID, EntryID: identity.EntryID, RecoveryPointID: pointID,
			NormalizedPath: "prefix.txt", Name: "prefix.txt", EntryType: string(backupasset.CatalogEntryFile), Size: int64(len(body)),
			Fingerprint: strongFingerprint, FingerprintStrength: string(catalog.FingerprintStrong),
			EncryptedProviderLocator: `{"version":1,"native":"prefix.txt"}`, SecurityState: "sealed", CreatedAt: createdAt,
		})
	}
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		for _, records := range []any{&points, &manifests, &generations, &entries} {
			if err := tx.CreateInBatches(records, 20).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed retained historical Catalog prefix: %v", err)
	}
}

type rsyncRangeBeforeOpenTestAdapter struct {
	managedRsyncPointReadAdapter
	before func() error
}

func (adapter *rsyncRangeBeforeOpenTestAdapter) OpenRange(ctx context.Context, snapshot provider.ReadSnapshot, point provider.PointLocator, locator provider.EntryLocator, byteRange provider.ByteRange) (provider.ReadHandle, provider.ContentStat, error) {
	if err := adapter.before(); err != nil {
		return nil, provider.ContentStat{}, err
	}
	return adapter.managedRsyncPointReadAdapter.OpenRange(ctx, snapshot, point, locator, byteRange)
}

func TestMutableRsyncCatalogBuildRefreshesRootSourceBeforeOpeningSession(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local Rsync catalog access is Linux-only")
	}
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	clock := now
	db := newRepositoryTestDB(t)
	root := t.TempDir()
	payload := []byte("services:\n  app:\n    image: xirang:test\n")
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return clock })
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainCursorSigning); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainEntryIdentity); err != nil {
		t.Fatal(err)
	}
	cursors := provider.NewCursorCodec(keyring, func() time.Time { return clock }, time.Hour)
	limits, err := provider.NewMetadataOperationLimits(time.Minute, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := provider.NewRsyncAdapter(cursors, limits, 100, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	prober := &transientRsyncProber{adapter: adapter}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{
		Prober: prober, PointLister: adapter, EntryStatter: adapter,
		SequentialReader: adapter, RangeReader: adapter, CatalogReader: adapter,
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry, Keyring: keyring,
		Now: func() time.Time { return clock }, Admission: &publicationAdmission{
			mode: publication.AdmissionManaged, generation: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskEntity := seedTask(t, db, "rsync", root, "")
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connect local Rsync repository: result=%+v err=%v", connected, err)
	}
	var before model.RecoveryPoint
	if err := db.First(&before, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	indexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
		DB: db, Factory: service, Lease: lease, IdentityKeys: keyring,
		Now: func() time.Time { return clock },
		Config: catalog.IndexerConfig{
			BatchSize: 100, BuildTimeout: time.Minute, MaxEntries: 100, HeartbeatInterval: time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := indexer.Build(context.Background(), catalog.BuildRequest{
		RepositoryID: connected.Repository.ID, RecoveryPointID: connected.MutablePoint.ID,
	})
	if err != nil {
		t.Fatalf("initial local Rsync catalog build: %v", err)
	}
	if first.State != string(catalog.GenerationComplete) || !first.IsActive {
		t.Fatalf("initial catalog generation=%+v, want active complete", first)
	}
	beforeRoot, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatal(err)
	}
	afterRoot, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if beforeRoot.Mode() == afterRoot.Mode() {
		t.Fatalf("root metadata did not change: before=%v after=%v", beforeRoot.Mode(), afterRoot.Mode())
	}
	if got, err := os.ReadFile(filepath.Join(root, "docker-compose.yml")); err != nil || string(got) != string(payload) {
		t.Fatalf("root file changed after metadata mutation: got=%q err=%v", got, err)
	}
	prober.fail.Store(true)
	failed, err := indexer.Build(context.Background(), catalog.BuildRequest{
		RepositoryID: connected.Repository.ID, RecoveryPointID: connected.MutablePoint.ID,
	})
	if err == nil || failed.ID == "" {
		t.Fatalf("transient refresh failure generation=%+v err=%v", failed, err)
	}
	var failedStored model.CatalogGeneration
	if err := db.Where("recovery_point_id = ?", connected.MutablePoint.ID).
		Order("generation DESC").First(&failedStored).Error; err != nil {
		t.Fatal(err)
	}
	if failedStored.State != string(catalog.GenerationFailed) || failedStored.FinishedAt == nil {
		t.Fatalf("transient refresh generation=%+v", failedStored)
	}
	config := backupasset.CatalogConfig{
		Enabled: true, BatchSize: 100, BuildTimeout: time.Minute, ReconcileInterval: time.Minute,
		MaxConcurrency: 1, MaxEntries: 100,
		Lease: backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Second, AbsoluteDeadline: time.Hour},
	}
	retryAt := failedStored.FinishedAt.UTC().Add(catalog.RetryDelay(config, 1, connected.MutablePoint.ID, failedStored.ID) + time.Second)
	clock = retryAt
	candidates, err := indexer.ListCandidates(context.Background(), 20, retryAt, config)
	if err != nil || len(candidates) != 1 || candidates[0].RecoveryPointID != connected.MutablePoint.ID {
		t.Fatalf("transient refresh retry candidate=%+v err=%v", candidates, err)
	}
	prober.fail.Store(false)
	second, err := indexer.Build(context.Background(), catalog.BuildRequest{
		RepositoryID: connected.Repository.ID, RecoveryPointID: connected.MutablePoint.ID,
	})
	if err != nil {
		t.Fatalf("catalog build after transient refresh failure: %v", err)
	}
	if second.State != string(catalog.GenerationComplete) || !second.IsActive || second.ID == first.ID {
		t.Fatalf("recovered catalog generation=%+v, first=%+v, want new active complete generation", second, first)
	}
	var after model.RecoveryPoint
	if err := db.First(&after, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.SourceFingerprint == before.SourceFingerprint || after.ObservedAt == nil {
		t.Fatalf("mutable point source did not refresh: before=%q after=%q observed_at=%v", before.SourceFingerprint, after.SourceFingerprint, after.ObservedAt)
	}
	var entry model.CatalogEntry
	if err := db.Where("generation_id = ?", second.ID).First(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if entry.Fingerprint != "" || entry.FingerprintStrength != string(catalog.FingerprintNone) {
		t.Fatalf("mutable Rsync entry fingerprint=%q/%q, want none", entry.Fingerprint, entry.FingerprintStrength)
	}
	session, err := service.OpenContentSource(context.Background(), content.SourceRequest{
		Ref:                 backupasset.AssetRef{RecoveryPointID: after.ID, EntryID: entry.EntryID},
		CatalogGenerationID: second.ID, ExpectedSource: after.SourceFingerprint,
		ExpectedEntry: entry.Fingerprint, Mode: content.SourceModeSequential, MaxBytes: entry.Size,
	})
	if err != nil {
		t.Fatalf("open recovered local Rsync content source: %v", err)
	}
	read, err := io.ReadAll(session.Reader())
	closeErr := session.Close()
	if err != nil || closeErr != nil || string(read) != string(payload) {
		t.Fatalf("read recovered local Rsync content source: bytes=%q read_err=%v close_err=%v", read, err, closeErr)
	}
}

// TestMutableRsyncCatalogBuildReplacesGenerationAfterCompletionObserver proves
// backup completion still supersedes and rebuilds even when an in-place child
// change leaves the mutable root fingerprint unchanged.
func TestMutableRsyncCatalogBuildReplacesGenerationAfterCompletionObserver(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict local Rsync catalog access is Linux-only")
	}
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	clock := now
	db := newRepositoryTestDB(t)
	root := t.TempDir()
	payload := []byte("services:\n  app:\n    image: xirang:completion\n")
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return clock })
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainCursorSigning); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Ensure(context.Background(), backupasset.KeyDomainEntryIdentity); err != nil {
		t.Fatal(err)
	}
	cursors := provider.NewCursorCodec(keyring, func() time.Time { return clock }, time.Hour)
	limits, err := provider.NewMetadataOperationLimits(time.Minute, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := provider.NewRsyncAdapter(cursors, limits, 100, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	prober := &transientRsyncProber{adapter: adapter}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{
		Prober: prober, PointLister: adapter, EntryStatter: adapter,
		SequentialReader: adapter, RangeReader: adapter, CatalogReader: adapter,
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry, Keyring: keyring,
		Now: func() time.Time { return clock }, Admission: &publicationAdmission{
			mode: publication.AdmissionManaged, generation: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskEntity := seedTask(t, db, "rsync", root, "")
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil || connected.MutablePoint == nil {
		t.Fatalf("connect local Rsync repository: result=%+v err=%v", connected, err)
	}
	lease, err := backupasset.NewLeaseService(db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	indexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
		DB: db, Factory: service, Lease: lease, IdentityKeys: keyring,
		Now:    func() time.Time { return clock },
		Config: catalog.IndexerConfig{BatchSize: 100, BuildTimeout: time.Minute, MaxEntries: 100, HeartbeatInterval: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := indexer.Build(context.Background(), catalog.BuildRequest{
		RepositoryID: connected.Repository.ID, RecoveryPointID: connected.MutablePoint.ID,
	})
	if err != nil || first.State != string(catalog.GenerationComplete) || !first.IsActive {
		t.Fatalf("initial catalog generation=%+v err=%v", first, err)
	}
	var before model.RecoveryPoint
	if err := db.First(&before, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}

	// A completed backup whose child content changed in place: the root
	// fingerprint is unchanged but the projection must still be invalidated.
	if err := service.ObserveBackupSourceCompletion(context.Background(), taskEntity.ID); err != nil {
		t.Fatalf("observe backup source completion: %v", err)
	}
	var pointAfter model.RecoveryPoint
	if err := db.First(&pointAfter, "id = ?", connected.MutablePoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if pointAfter.SourceFingerprint != before.SourceFingerprint {
		t.Fatalf("completion changed the mutable root fingerprint: before=%q after=%q", before.SourceFingerprint, pointAfter.SourceFingerprint)
	}
	var superseded model.CatalogGeneration
	if err := db.First(&superseded, "id = ?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if superseded.State != string(catalog.GenerationSuperseded) || superseded.IsActive {
		t.Fatalf("completion did not supersede the active generation: %+v", superseded)
	}

	config := backupasset.CatalogConfig{
		Enabled: true, BatchSize: 100, BuildTimeout: time.Minute, ReconcileInterval: time.Minute,
		MaxConcurrency: 1, MaxEntries: 100,
		Lease: backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Second, AbsoluteDeadline: time.Hour},
	}
	candidates, err := indexer.ListCandidates(context.Background(), 20, clock, config)
	if err != nil || len(candidates) != 1 || candidates[0].RecoveryPointID != connected.MutablePoint.ID {
		t.Fatalf("completion candidate candidates=%+v err=%v", candidates, err)
	}
	replacement, err := indexer.Build(context.Background(), catalog.BuildRequest{
		RepositoryID: connected.Repository.ID, RecoveryPointID: connected.MutablePoint.ID,
	})
	if err != nil {
		t.Fatalf("build completion replacement: %v", err)
	}
	if replacement.State != string(catalog.GenerationComplete) || !replacement.IsActive ||
		replacement.ID == first.ID || replacement.SourceFingerprint != pointAfter.SourceFingerprint {
		t.Fatalf("completion replacement generation=%+v first=%+v", replacement, first)
	}
	var generations int64
	if err := db.Model(&model.CatalogGeneration{}).Where("recovery_point_id = ?", connected.MutablePoint.ID).Count(&generations).Error; err != nil {
		t.Fatal(err)
	}
	if generations != 2 {
		t.Fatalf("completion produced %d generations, want exactly the replacement", generations)
	}
}

type transientRsyncProber struct {
	adapter *provider.RsyncAdapter
	fail    atomic.Bool
}

func (prober *transientRsyncProber) Probe(ctx context.Context, binding provider.AccessBinding, limits provider.OperationLimits) (provider.RepositoryObservation, error) {
	if prober.fail.Load() {
		return provider.RepositoryObservation{}, &provider.CapabilityError{
			Reason: backupasset.CapabilityReason{Code: backupasset.CapabilityProviderUnavailable},
		}
	}
	return prober.adapter.Probe(ctx, binding, limits)
}

type managedRsyncCatalogFixture struct {
	db         *gorm.DB
	factory    catalog.PointReadFactory
	lease      *backupasset.LeaseService
	keyring    *backupasset.Keyring
	now        time.Time
	repository model.BackupRepository
	point      model.RecoveryPoint
	treeRoot   string
}

func newManagedRsyncCatalogFixture(t *testing.T) managedRsyncCatalogFixture {
	t.Helper()
	return managedRsyncCatalogFixtureFromPublication(t, newRsyncPublicationFixture(t), nil)
}

func managedRsyncCatalogFixtureFromPublication(t *testing.T, publicationFixture *rsyncPublicationFixture, sourceModifiedAt *time.Time) managedRsyncCatalogFixture {
	t.Helper()
	markerKey, err := publicationFixture.service.rsyncMarkerKey(context.Background(), publicationFixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	managedRoot := filepath.Join(t.TempDir(), "managed-rsync-root")
	bootstrap, err := provider.BootstrapRsyncManagedRoot(context.Background(), provider.RsyncManagedRootBootstrapRequest{
		ManagedRoot: managedRoot, RepositoryID: publicationFixture.repository.ID, MarkerKey: markerKey, CreatedAt: publicationFixture.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publicationFixture.binding.ManagedRootLocator = managedRoot
	publicationFixture.binding.RootMarkerDigest = bootstrap.RepositoryMarkerDigest
	publicationFixture.binding.ManagedRootIdentityDigest = bootstrap.ManagedRootIdentityDigest
	bindingPayload, err := encodeManagedRsyncBindingDocumentV2(publicationFixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := managedRsyncRepositoryIdentity(publicationFixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	var binding model.RepositoryAccessBinding
	if err := publicationFixture.db.Where("repository_id = ?", publicationFixture.repository.ID).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	binding.EncryptedConfig = bindingPayload
	binding.UpdatedAt = publicationFixture.now
	if err := publicationFixture.db.Save(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if err := publicationFixture.db.Model(&model.BackupRepository{}).Where("id = ?", publicationFixture.repository.ID).
		Update("repository_identity", identity).Error; err != nil {
		t.Fatal(err)
	}
	publicationFixture.repository.RepositoryIdentity = &identity

	execution, err := publicationFixture.service.Prepare(context.Background(), publicationFixture.run())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) })
	state, ok := execution.(*rsyncPublicationExecution)
	if !ok {
		t.Fatalf("managed Rsync execution type=%T", execution)
	}
	input, err := state.RsyncTreePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	sourceDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDirectory, "payload.txt"), []byte("managed Catalog payload\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sourceModifiedAt != nil {
		if err := os.Chtimes(filepath.Join(sourceDirectory, "payload.txt"), *sourceModifiedAt, *sourceModifiedAt); err != nil {
			t.Fatal(err)
		}
	}
	input.Source = provider.RsyncTreeCommandSource{LocalPath: sourceDirectory}
	strategy, err := provider.NewLocalRsyncTreePublicationStrategy(func() time.Time { return publicationFixture.now })
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := strategy.Prepare(context.Background(), provider.PublicationPrepareRequest{
		Attempt: provider.NewRsyncTreePublicationAttempt(state.attempt), RsyncTreeInput: &input,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Execute(context.Background(), prepared, provider.PublicationProgress{})
	if err != nil {
		t.Fatal(err)
	}
	commitRecord, err := strategy.RecordCommit(context.Background(), prepared, result)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := commitRecord.RsyncTreeCommit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	publicationFixture.strategy.reconcile = strategy.Reconcile
	outcome, err := publicationFixture.service.ProcessPoint(context.Background(), state.attempt.RecoveryPointID)
	if err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		t.Fatalf("reconcile real Rsync publication: outcome=%+v err=%v", outcome, err)
	}
	var point model.RecoveryPoint
	if err := publicationFixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: publicationFixture.db, Foundation: publicationFixture.service.foundation, Registry: publicationFixture.service.registry,
		Keyring: publicationFixture.service.keyring, Now: func() time.Time { return publicationFixture.now },
		Admission: publicationFixture.admission, History: publicationFixture.service.history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var manifest model.RecoveryPointManifest
	if err := publicationFixture.db.Where("recovery_point_id = ? AND is_active = ?", point.ID, true).First(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	return managedRsyncCatalogFixture{
		db: publicationFixture.db, factory: service, lease: publicationFixture.service.lease, keyring: publicationFixture.service.keyring,
		now: publicationFixture.now, repository: publicationFixture.repository, point: point,
		treeRoot: filepath.Join(managedRoot, "points", point.ID, "tree"),
	}
}

func newRsyncCatalogPublicationFixtureWithDB(t *testing.T, db *gorm.DB) *rsyncPublicationFixture {
	t.Helper()
	// Preserve a known 876ns residue at the end of the second through the
	// real publication, PostgreSQL write/read, and authenticated marker checks.
	now := time.Unix(time.Now().Unix(), 999999876).UTC()
	legacyTarget := t.TempDir()
	task := seedTask(t, db, "rsync", legacyTarget, "")
	if err := db.Model(&model.Task{}).Where("id = ?", task.ID).Update("updated_at", now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Preload("Node").First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{TaskID: task.ID, TriggerType: "manual", Status: "running", StartedAt: timePointer(now.Add(-time.Minute)), CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	binding := managedRsyncBindingDocumentV2{
		Version: managedRsyncBindingDocumentVersion, Provider: backupasset.ProviderRsync, IdentityClass: provider.IdentityXirangManagedRepository,
		TaskID: task.ID, NodeID: task.NodeID, RepositoryID: strings.Repeat("1", 32), TaskRepositoryLinkID: strings.Repeat("2", 32),
		LayoutRevision: managedRsyncLayoutRevisionV1, ManagedRootLocator: t.TempDir(), RootMarkerDigest: strings.Repeat("b", 64),
		ManagedRootIdentityDigest: strings.Repeat("c", 64), PublicationMode: backupasset.PublicationVersionedFullCopy,
		PreflightID: strings.Repeat("d", 32), PreflightDigest: strings.Repeat("e", 64), IdentitySalt: strings.Repeat("42", provider.IdentitySaltBytes),
	}
	identity, err := managedRsyncRepositoryIdentity(binding)
	if err != nil {
		t.Fatal(err)
	}
	repository := model.BackupRepository{
		ID: binding.RepositoryID, ProviderKind: string(backupasset.ProviderRsync), RepositoryIdentity: &identity, DisplayName: "managed-rsync-repository",
		VersionMode: string(backupasset.VersionFullCopyTree), Status: string(backupasset.RepositoryOnline), CapabilityRevision: 1,
		CapabilitiesJSON: `{"list":true,"open_sequential":true}`, ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	link := model.TaskRepositoryLink{
		ID: binding.TaskRepositoryLinkID, TaskID: &task.ID, RepositoryID: repository.ID, TaskNameSnapshot: task.Name,
		NodeIDSnapshot: task.NodeID, NodeNameSnapshot: task.Node.Name, PublicationMode: string(backupasset.PublicationVersionedFullCopy),
		EncryptedLegacyLocator: legacyTarget, LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatal(err)
	}
	payload, err := encodeManagedRsyncBindingDocumentV2(binding)
	if err != nil {
		t.Fatal(err)
	}
	access := model.RepositoryAccessBinding{
		ID: strings.Repeat("3", 32), RepositoryID: repository.ID, BindingKind: "managed_rsync_v2", EncryptedConfig: payload,
		ConfigFingerprint: strings.Repeat("f", 64), Status: bindingStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&access).Error; err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry()
	strategy := &rsyncPublicationStrategyStub{}
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{Prober: &scriptedProber{}, PublicationStrategy: strategy}); err != nil {
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
	admission := &publicationAdmission{mode: publication.AdmissionManaged, generation: 1}
	service, err := NewPublicationService(PublicationDependencies{
		DB: db, Foundation: backupasset.NewFoundationService(completeRepositoryFoundationSettings(true)), Registry: registry,
		Keyring: backupasset.NewKeyring(db, func() time.Time { return now }), Lease: lease,
		Admission: admission, Metrics: publication.NoopMetrics{}, Audit: &auditSpy{}, History: history, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rsyncPublicationFixture{t: t, db: db, now: now, service: service, admission: admission, task: task, taskRun: run, repository: repository, link: link, binding: binding, strategy: strategy}
}

// The portable resolver boundary deliberately has no caller-supplied Task ID.
// Repository must derive the producing Task from the durable RecoveryPoint.
func TestRsyncResolverDerivesProducingTaskOnly(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)

	var resolver provider.RsyncRestoreSourceResolver = fixture.service
	source, err := resolver.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve durable Rsync scalar ref: %v", err)
	}
	defer func() { _ = source.Close() }()
	if err := source.Revalidate(context.Background()); err != nil {
		t.Fatalf("revalidate derived pinned source: %v", err)
	}

	forged := fixture.ref
	forged.RecoveryPointID = strings.Repeat("f", 32)
	if _, err := resolver.ResolveRsyncRestoreSource(context.Background(), forged); !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("substituted scalar source ref error=%v, want invalid restore request", err)
	}
}

func TestRecoveryRsyncSourceAuthorityFailsClosedWithoutAuthenticatedNamespaceEvidence(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	request := provider.RecoverySourceAuthorityRequest{
		Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref,
	}
	var authority RecoveryRsyncSourceAuthority = fixture.service
	source, observation, err := authority.ObserveRecoverySource(context.Background(), request)
	if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) ||
		!errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("unproved namespace source=%v observation=%+v err=%v, want capability unavailable", source, observation, err)
	}
}

func TestRecoveryRsyncSourceAuthorityReturnsCompleteTransferredSource(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	var transferred *recoveryAuthorityOwnedSource
	source, observation, err := observeRecoveryRsyncSourceWithDependencies(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref},
		recoveryRsyncSourceAuthorityDependencies{
			resolve: fixture.service.ResolveRsyncRestoreSource,
			capture: fixture.service.captureRecoveryRsyncAuthoritySnapshot,
			observe: func(_ context.Context, request RecoverySourceNamespaceRequest, pinned provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
				if request.SourceRef != fixture.ref || request.ProducingTaskID == 0 ||
					!isLowerHex64(request.RepositoryBindingRevision) || !isLowerHex64(request.ProvenanceRevision) {
					t.Fatalf("incomplete Repository transfer request: %+v", request)
				}
				transferred = &recoveryAuthorityOwnedSource{pinned: pinned}
				return transferred, nil
			},
			revalidate: fixture.service.revalidateRecoveryRsyncAuthoritySnapshot,
		},
	)
	if err != nil || source == nil || transferred == nil || source != transferred ||
		observation.Provider != backupasset.ProviderRsync || observation.RepositoryID != fixture.ref.RepositoryID ||
		!isLowerHex64(observation.RepositoryBindingRevision) || !isLowerHex64(observation.ProvenanceRevision) {
		t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
	}
	materialized, err := source.MaterializeDeclaredEntries(context.Background(), []provider.RestoreEntry{fixture.entry})
	if err != nil || len(materialized) != 1 || materialized[0].AssetRef != fixture.entry.AssetRef ||
		materialized[0].Type != fixture.entry.Type || materialized[0].ExpectedSize != fixture.entry.ExpectedSize ||
		materialized[0].TargetObjectDigest != fixture.entry.TargetObjectDigest || !isLowerHex64(materialized[0].ExpectedDigest) {
		t.Fatalf("materialized=%+v err=%v", materialized, err)
	}
	if err := source.Revalidate(context.Background()); err != nil {
		t.Fatalf("revalidate complete transferred source: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if transferred.closeCalls.Load() != 1 {
		t.Fatalf("transferred source close calls=%d, want exactly one", transferred.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityTransfersOwnershipThenRevalidates(t *testing.T) {
	ref := provider.RsyncRestoreSourceRef{
		PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
		RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
		CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
		SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
	}
	pinned := &recoveryAuthoritySourceFake{}
	closed := &recoveryAuthorityOwnedSource{pinned: pinned}
	phases := make([]string, 0, 4)
	snapshot := recoveryRsyncAuthoritySnapshot{
		producingTaskID:           42,
		repositoryBindingRevision: strings.Repeat("9", 64),
		provenanceRevision:        strings.Repeat("a", 64),
	}

	source, observation, err := observeRecoveryRsyncSourceWithDependencies(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: ref},
		recoveryRsyncSourceAuthorityDependencies{
			resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
				phases = append(phases, "resolve")
				return pinned, nil
			},
			capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
				phases = append(phases, "capture")
				return snapshot, nil
			},
			observe: func(_ context.Context, request RecoverySourceNamespaceRequest, transferred provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
				phases = append(phases, "observe")
				if pinned.revalidateCalls.Load() != 1 {
					t.Fatalf("pinned revalidate calls before namespace transfer=%d, want 1", pinned.revalidateCalls.Load())
				}
				if transferred != pinned || request.SourceRef != ref || request.ProducingTaskID != snapshot.producingTaskID ||
					request.RepositoryBindingRevision != snapshot.repositoryBindingRevision || request.ProvenanceRevision != snapshot.provenanceRevision {
					t.Fatalf("observer request=%+v transferred=%T", request, transferred)
				}
				return closed, nil
			},
			revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
				phases = append(phases, "revalidate")
				return nil
			},
		},
	)
	if err != nil || source != closed || observation.Provider != backupasset.ProviderRsync ||
		observation.RepositoryBindingRevision != snapshot.repositoryBindingRevision || observation.ProvenanceRevision != snapshot.provenanceRevision {
		t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
	}
	if !reflect.DeepEqual(phases, []string{"resolve", "capture", "observe", "revalidate"}) {
		t.Fatalf("phases=%v", phases)
	}
	if pinned.revalidateCalls.Load() != 2 {
		t.Fatalf("pinned revalidate calls after post-observation revalidation=%d, want 2", pinned.revalidateCalls.Load())
	}
	if pinned.closeCalls.Load() != 0 || closed.closeCalls.Load() != 0 {
		t.Fatalf("success closed transferred owners early: pinned=%d closed=%d", pinned.closeCalls.Load(), closed.closeCalls.Load())
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if closed.closeCalls.Load() != 1 || pinned.closeCalls.Load() != 1 {
		t.Fatalf("caller close counts observation=%d pinned=%d, want one each", closed.closeCalls.Load(), pinned.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityClosesObservationOnPostObservationDrift(t *testing.T) {
	pinned := &recoveryAuthoritySourceFake{}
	observed := &recoveryAuthorityOwnedSource{pinned: pinned}
	snapshot := recoveryRsyncAuthoritySnapshot{
		producingTaskID:           42,
		repositoryBindingRevision: strings.Repeat("9", 64),
		provenanceRevision:        strings.Repeat("a", 64),
	}
	source, observation, err := observeRecoveryRsyncSourceWithDependencies(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync},
		recoveryRsyncSourceAuthorityDependencies{
			resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
				return pinned, nil
			},
			capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
				return snapshot, nil
			},
			observe: func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
				return observed, nil
			},
			revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
				return errors.New("PRIVATE_POST_OBSERVATION_DRIFT_CANARY")
			},
		},
	)
	if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
	}
	if strings.Contains(fmt.Sprint(err), "PRIVATE_POST_OBSERVATION_DRIFT_CANARY") {
		t.Fatalf("post-observation error leaked: %v", err)
	}
	if pinned.closeCalls.Load() != 1 || observed.closeCalls.Load() != 1 {
		t.Fatalf("drift ownership closes pinned=%d observed=%d, want 1/1", pinned.closeCalls.Load(), observed.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityNilObservationClosesPinnedExactlyOnce(t *testing.T) {
	pinned := &recoveryAuthoritySourceFake{}
	snapshot := recoveryRsyncAuthoritySnapshot{
		producingTaskID:           42,
		repositoryBindingRevision: strings.Repeat("9", 64),
		provenanceRevision:        strings.Repeat("a", 64),
	}
	source, observation, err := observeRecoveryRsyncSourceWithDependencies(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync},
		recoveryRsyncSourceAuthorityDependencies{
			resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
				return pinned, nil
			},
			capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
				return snapshot, nil
			},
			observe: func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
				return nil, nil
			},
			revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
				return nil
			},
		},
	)
	if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
	}
	if pinned.closeCalls.Load() != 1 {
		t.Fatalf("nil observation pinned close calls=%d, want exactly one", pinned.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityRejectsIncompleteTransferredObservation(t *testing.T) {
	pinned := &recoveryAuthoritySourceFake{}
	observed := &recoveryAuthorityOwnedSource{
		pinned: pinned, revalidateErr: errors.New("PRIVATE_INCOMPLETE_OBSERVATION_CANARY"),
	}
	snapshot := recoveryRsyncAuthoritySnapshot{
		producingTaskID:           42,
		repositoryBindingRevision: strings.Repeat("9", 64),
		provenanceRevision:        strings.Repeat("a", 64),
	}
	source, observation, err := observeRecoveryRsyncSourceWithDependencies(
		context.Background(),
		provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync},
		recoveryRsyncSourceAuthorityDependencies{
			resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
				return pinned, nil
			},
			capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
				return snapshot, nil
			},
			observe: func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
				return observed, nil
			},
			revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
				return nil
			},
		},
	)
	if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
	}
	if strings.Contains(fmt.Sprint(err), "PRIVATE_INCOMPLETE_OBSERVATION_CANARY") {
		t.Fatalf("incomplete observation leaked: %v", err)
	}
	if observed.closeCalls.Load() != 1 || pinned.closeCalls.Load() != 1 {
		t.Fatalf("incomplete observation closes observed=%d pinned=%d, want one each", observed.closeCalls.Load(), pinned.closeCalls.Load())
	}
}

func TestRecoveryRsyncSourceAuthorityRejectsCapabilityDrift(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	var point model.RecoveryPoint
	if err := fixture.db.Where("id = ?", fixture.ref.RecoveryPointID).First(&point).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ref.RecoveryPointID).
		Update("capability_revision", point.CapabilityRevision+1).Error; err != nil {
		t.Fatal(err)
	}
	source, _, err := fixture.service.ObserveRecoverySource(context.Background(), provider.RecoverySourceAuthorityRequest{
		Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref,
	})
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("capability drift error=%v, want invalid restore request", err)
	}
}

func TestRecoveryRsyncSourceAuthorityRevalidatesClosedObservationInCallerTransaction(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	snapshot, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("capture source authority: %v", err)
	}
	expected := snapshot.observation
	expected.Provider = backupasset.ProviderRsync
	expected.RepositoryID = fixture.ref.RepositoryID
	expected.RecoveryPointID = fixture.ref.RecoveryPointID
	expected.CatalogGenerationID = fixture.ref.CatalogGenerationID
	expected.SourceRevisionDigest = fixture.ref.SourceRevisionDigest
	expected.ManifestDigest = fixture.ref.ManifestDigest
	expected.RepositoryBindingRevision = snapshot.repositoryBindingRevision
	expected.ProvenanceRevision = snapshot.provenanceRevision
	request := provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref}

	err = fixture.db.Transaction(func(tx *gorm.DB) error {
		return fixture.service.RevalidateRecoverySourceAuthorityTx(context.Background(), tx, request, expected)
	})
	if err != nil {
		t.Fatalf("revalidate exact closed observation: %v", err)
	}
	for _, mutate := range []func(*RecoveryRsyncSourceAuthorityObservation){
		func(value *RecoveryRsyncSourceAuthorityObservation) { value.CapabilityRevision++ },
		func(value *RecoveryRsyncSourceAuthorityObservation) {
			value.RepositoryBindingRevision = strings.Repeat("0", 64)
		},
		func(value *RecoveryRsyncSourceAuthorityObservation) {
			value.ProvenanceRevision = strings.Repeat("1", 64)
		},
	} {
		candidate := expected
		mutate(&candidate)
		err = fixture.db.Transaction(func(tx *gorm.DB) error {
			return fixture.service.RevalidateRecoverySourceAuthorityTx(context.Background(), tx, request, candidate)
		})
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("substituted observation error=%v, want conflict", err)
		}
	}
}

func TestRecoveryRsyncSourceAuthorityRevalidatesCompleteLockedBindingAndProvenanceRows(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	tests := []struct {
		name   string
		mutate func(*testing.T, *rsyncRestoreResolverFixture)
	}{
		{
			name: "binding plaintext with stable config fingerprint",
			mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture) {
				var binding model.RepositoryAccessBinding
				if err := fixture.db.Where("repository_id = ? AND status = ?", fixture.ref.RepositoryID, bindingStatusActive).First(&binding).Error; err != nil {
					t.Fatal(err)
				}
				fingerprint := binding.ConfigFingerprint
				updatedAt := binding.UpdatedAt
				stored, err := decodeStoredBindingDocument(binding.EncryptedConfig)
				if err != nil || stored.ManagedRsyncV2 == nil {
					t.Fatalf("decode binding err=%v stored=%+v", err, stored)
				}
				document := *stored.ManagedRsyncV2
				document.PreflightDigest = strings.Repeat("d", 64)
				payload, err := encodeManagedRsyncBindingDocumentV2(document)
				if err != nil {
					t.Fatal(err)
				}
				binding.EncryptedConfig = payload
				if err := fixture.db.Save(&binding).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RepositoryAccessBinding{}).Where("id = ?", binding.ID).
					UpdateColumn("updated_at", updatedAt).Error; err != nil {
					t.Fatal(err)
				}
				var current model.RepositoryAccessBinding
				if err := fixture.db.Where("id = ?", binding.ID).First(&current).Error; err != nil {
					t.Fatal(err)
				}
				if current.ConfigFingerprint != fingerprint || !current.UpdatedAt.Equal(updatedAt) {
					t.Fatalf("fixture changed non-plaintext authority: fingerprint=%q updated_at=%s", current.ConfigFingerprint, current.UpdatedAt)
				}
			},
		},
		{
			name: "point lineage",
			mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture) {
				var point model.RecoveryPoint
				if err := fixture.db.Where("id = ?", fixture.ref.RecoveryPointID).First(&point).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ref.RecoveryPointID).
					Update("lineage_json", `{"revision":"PRIVATE_POINT_LINEAGE_CANARY"}`).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ref.RecoveryPointID).
					UpdateColumn("updated_at", point.UpdatedAt).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRsyncRestoreResolverFixture(t)
			var transferred *recoveryAuthorityOwnedSource
			source, observation, err := observeRecoveryRsyncSourceWithDependencies(
				context.Background(),
				provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref},
				recoveryRsyncSourceAuthorityDependencies{
					resolve: fixture.service.ResolveRsyncRestoreSource,
					capture: fixture.service.captureRecoveryRsyncAuthoritySnapshot,
					observe: func(_ context.Context, request RecoverySourceNamespaceRequest, pinned provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
						if request.ProducingTaskID == 0 || request.RepositoryBindingRevision == "" || request.ProvenanceRevision == "" ||
							request.RepositoryBindingRevision == request.ProvenanceRevision {
							t.Fatalf("incomplete namespace request: %+v", request)
						}
						testCase.mutate(t, fixture)
						transferred = &recoveryAuthorityOwnedSource{pinned: pinned}
						return transferred, nil
					},
					revalidate: fixture.service.revalidateRecoveryRsyncAuthoritySnapshot,
				},
			)
			if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				t.Fatalf("source=%T observation=%+v err=%v", source, observation, err)
			}
			if transferred == nil || transferred.closeCalls.Load() != 1 {
				t.Fatalf("post-observation drift close calls=%v, want exactly one", transferred)
			}
			for _, canary := range []string{"PRIVATE_POINT_LINEAGE_CANARY", fixture.root, fixture.taskSource} {
				if strings.Contains(fmt.Sprint(err), canary) {
					t.Fatalf("post-observation drift leaked %q: %v", canary, err)
				}
			}
		})
	}
}

func TestRecoveryRsyncSourceAuthoritySQLiteRevalidationExcludesConcurrentWriter(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	expected, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("capture Recovery source authority snapshot: %v", err)
	}

	lockReady := make(chan error, 1)
	releaseLock := make(chan struct{})
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- fixture.db.Transaction(func(tx *gorm.DB) error {
			current, loadErr := fixture.service.loadRecoveryRsyncAuthoritySnapshotTx(
				context.Background(), tx, fixture.ref,
			)
			if loadErr == nil && !reflect.DeepEqual(current, expected) {
				loadErr = errors.New("Recovery source authority snapshot changed before lock test")
			}
			lockReady <- loadErr
			if loadErr != nil {
				return loadErr
			}
			<-releaseLock
			return nil
		})
	}()
	if lockErr := <-lockReady; lockErr != nil {
		close(releaseLock)
		<-lockDone
		t.Fatalf("hold Repository revalidation transaction: %v", lockErr)
	}
	concurrentWriteErr := fixture.db.Model(&model.RecoveryPoint{}).
		Where("id = ?", fixture.ref.RecoveryPointID).
		Update("lineage_json", `{"revision":"concurrent-writer"}`).Error
	close(releaseLock)
	if lockErr := <-lockDone; lockErr != nil {
		t.Fatalf("finish Repository revalidation transaction: %v", lockErr)
	}
	if concurrentWriteErr == nil {
		t.Fatal("concurrent RecoveryPoint writer committed while Repository revalidation was active")
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).
		Where("id = ?", fixture.ref.RecoveryPointID).
		Update("lineage_json", `{"revision":"after-revalidation"}`).Error; err != nil {
		t.Fatalf("RecoveryPoint writer remained blocked after Repository revalidation committed: %v", err)
	}
}

func TestRecoveryRsyncSourceAuthorityUnsupportedProvidersAreUnavailableBeforeAccess(t *testing.T) {
	for _, kind := range []backupasset.ProviderKind{backupasset.ProviderRestic, backupasset.ProviderRclone} {
		t.Run(string(kind), func(t *testing.T) {
			source, observation, err := (*Service)(nil).ObserveRecoverySource(context.Background(), provider.RecoverySourceAuthorityRequest{
				Provider: kind,
			})
			if source != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) ||
				!errors.Is(err, backupasset.ErrCapabilityUnavailable) {
				t.Fatalf("unsupported provider source=%v observation=%+v err=%v", source, observation, err)
			}
		})
	}
}

func TestRecoveryRsyncSourceAuthorityProductsRedactPrivateEvidence(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	observation := RecoveryRsyncSourceAuthorityObservation{
		Provider: backupasset.ProviderRsync, RepositoryID: fixture.ref.RepositoryID,
		RecoveryPointID: fixture.ref.RecoveryPointID, CatalogGenerationID: fixture.ref.CatalogGenerationID,
		SourceRevisionDigest: fixture.ref.SourceRevisionDigest, ManifestDigest: fixture.ref.ManifestDigest,
		SourceAccessIdentity:      "PRIVATE_SOURCE_ACCESS_IDENTITY_CANARY",
		SourceFingerprint:         fixture.sourceFingerprint,
		ManagedRootIdentity:       "PRIVATE_MANAGED_ROOT_IDENTITY_CANARY",
		RepositoryBindingRevision: "PRIVATE_REPOSITORY_BINDING_REVISION_CANARY",
		ProvenanceRevision:        "PRIVATE_PROVENANCE_REVISION_CANARY",
	}
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	observationJSON, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	formatted := []string{
		string(sourceJSON), string(observationJSON), fmt.Sprintf("%v", source), fmt.Sprintf("%+v", source), fmt.Sprintf("%#v", source),
		fmt.Sprintf("%v", observation), fmt.Sprintf("%+v", observation), fmt.Sprintf("%#v", observation),
	}
	for _, output := range formatted {
		for _, canary := range []string{
			fixture.root, fixture.taskSource, fixture.ref.PlanID, fixture.ref.ManifestDigest,
			fixture.sourceFingerprint, observation.SourceAccessIdentity, observation.ManagedRootIdentity,
			observation.RepositoryBindingRevision, observation.ProvenanceRevision,
		} {
			if strings.Contains(output, canary) {
				t.Fatalf("private source authority product leaked %q in %q", canary, output)
			}
		}
	}
}

func TestRecoveryRsyncSourceAuthorityRevalidatesAndClosesBeforeFailingUnavailable(t *testing.T) {
	request := provider.RecoverySourceAuthorityRequest{
		Provider: backupasset.ProviderRsync,
		RsyncRef: provider.RsyncRestoreSourceRef{
			PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
			RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
			CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
			SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
		},
	}
	snapshot := recoveryRsyncAuthoritySnapshot{
		producingTaskID:           42,
		repositoryBindingRevision: strings.Repeat("9", 64),
		provenanceRevision:        strings.Repeat("a", 64),
	}

	t.Run("namespace authority unavailable revalidates and closes source once", func(t *testing.T) {
		source := &recoveryAuthoritySourceFake{}
		resolved, observation, err := observeRecoveryRsyncSourceWithDependencies(
			context.Background(),
			request,
			recoveryRsyncSourceAuthorityDependencies{
				resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
					return source, nil
				},
				capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
					if source.revalidateCalls.Load() != 1 || source.closeCalls.Load() != 0 {
						t.Fatalf("before capture revalidate=%d close=%d, want 1/0", source.revalidateCalls.Load(), source.closeCalls.Load())
					}
					return snapshot, nil
				},
				observe: func(_ context.Context, _ RecoverySourceNamespaceRequest, transferred provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
					if transferred != source || source.revalidateCalls.Load() != 1 || source.closeCalls.Load() != 0 {
						t.Fatalf("before unavailable observe transferred=%T revalidate=%d close=%d, want source/1/0", transferred, source.revalidateCalls.Load(), source.closeCalls.Load())
					}
					_ = transferred.Close()
					return nil, backupasset.ErrCapabilityUnavailable
				},
				revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
					t.Fatal("unexpected post-observation revalidation when namespace authority is unavailable")
					return nil
				},
			},
		)
		if resolved != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) ||
			!errors.Is(err, backupasset.ErrCapabilityUnavailable) {
			t.Fatalf("unproved namespace source=%v observation=%+v err=%v, want capability unavailable", resolved, observation, err)
		}
		if source.revalidateCalls.Load() != 1 || source.closeCalls.Load() != 1 {
			t.Fatalf("source lifecycle revalidate=%d close=%d, want exactly once", source.revalidateCalls.Load(), source.closeCalls.Load())
		}
	})

	t.Run("pinned source revalidation failure closes source once and skips capture and observe", func(t *testing.T) {
		const privateFailure = "PRIVATE_PINNED_REVALIDATE_FAILURE_CANARY"
		source := &recoveryAuthoritySourceFake{revalidateErr: errors.New(privateFailure)}
		resolved, observation, err := observeRecoveryRsyncSourceWithDependencies(
			context.Background(),
			request,
			recoveryRsyncSourceAuthorityDependencies{
				resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
					return source, nil
				},
				capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
					t.Fatal("unexpected snapshot capture after pinned revalidation failure")
					return recoveryRsyncAuthoritySnapshot{}, nil
				},
				observe: func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
					t.Fatal("unexpected namespace observation after pinned revalidation failure")
					return nil, nil
				},
				revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
					t.Fatal("unexpected post-observation revalidation after pinned revalidation failure")
					return nil
				},
			},
		)
		if resolved != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) ||
			!errors.Is(err, provider.ErrInvalidRestoreRequest) {
			t.Fatalf("pinned revalidation failure source=%v observation=%+v err=%v, want invalid restore request", resolved, observation, err)
		}
		if strings.Contains(fmt.Sprint(err), privateFailure) {
			t.Fatalf("pinned revalidation failure leaked private dependency error: %v", err)
		}
		if source.revalidateCalls.Load() != 1 || source.closeCalls.Load() != 1 {
			t.Fatalf("source lifecycle revalidate=%d close=%d, want 1/1", source.revalidateCalls.Load(), source.closeCalls.Load())
		}
	})
}

func TestRecoveryRsyncSourceAuthoritySanitizesResolverFailureAndClosesReturnedSource(t *testing.T) {
	const privateFailure = "PRIVATE_SOURCE_RESOLVER_FAILURE_CANARY"
	snapshot := recoveryRsyncAuthoritySnapshot{
		producingTaskID:           42,
		repositoryBindingRevision: strings.Repeat("9", 64),
		provenanceRevision:        strings.Repeat("a", 64),
	}

	t.Run("resolver failure with returned source handle closes once and sanitizes error", func(t *testing.T) {
		source := &recoveryAuthoritySourceFake{}
		resolved, observation, err := observeRecoveryRsyncSourceWithDependencies(
			context.Background(),
			provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync},
			recoveryRsyncSourceAuthorityDependencies{
				resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
					return source, errors.New(privateFailure)
				},
				capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
					t.Fatal("unexpected capture after resolver failure")
					return recoveryRsyncAuthoritySnapshot{}, nil
				},
				observe: func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
					t.Fatal("unexpected observe after resolver failure")
					return nil, nil
				},
				revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
					t.Fatal("unexpected revalidate after resolver failure")
					return nil
				},
			},
		)
		if resolved != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) ||
			!errors.Is(err, provider.ErrInvalidRestoreRequest) {
			t.Fatalf("resolver failure source=%v observation=%+v err=%v, want invalid restore request", resolved, observation, err)
		}
		if strings.Contains(fmt.Sprint(err), privateFailure) {
			t.Fatalf("resolver failure leaked private dependency error: %v", err)
		}
		if source.closeCalls.Load() != 1 || source.revalidateCalls.Load() != 0 {
			t.Fatalf("resolver failure close=%d revalidate=%d, want 1/0", source.closeCalls.Load(), source.revalidateCalls.Load())
		}
	})

	t.Run("namespace observer failure with returned observed handle closes once and sanitizes error", func(t *testing.T) {
		pinned := &recoveryAuthoritySourceFake{}
		observed := &recoveryAuthorityOwnedSource{pinned: pinned}
		resolved, observation, err := observeRecoveryRsyncSourceWithDependencies(
			context.Background(),
			provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync},
			recoveryRsyncSourceAuthorityDependencies{
				resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
					return pinned, nil
				},
				capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
					return snapshot, nil
				},
				observe: func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
					return observed, errors.New(privateFailure)
				},
				revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
					t.Fatal("unexpected revalidate after namespace observer failure")
					return nil
				},
			},
		)
		if resolved != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) ||
			!errors.Is(err, provider.ErrInvalidRestoreRequest) {
			t.Fatalf("observer failure source=%v observation=%+v err=%v, want invalid restore request", resolved, observation, err)
		}
		if strings.Contains(fmt.Sprint(err), privateFailure) {
			t.Fatalf("observer failure leaked private dependency error: %v", err)
		}
		if observed.closeCalls.Load() != 1 || pinned.closeCalls.Load() != 1 || pinned.revalidateCalls.Load() != 1 {
			t.Fatalf("observer failure observedClose=%d pinnedClose=%d pinnedRevalidate=%d, want 1/1/1", observed.closeCalls.Load(), pinned.closeCalls.Load(), pinned.revalidateCalls.Load())
		}
	})

	for _, wantErr := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, stage := range []string{"resolve", "pinned_revalidate", "capture", "observe", "observed_revalidate", "revalidate"} {
			t.Run(fmt.Sprintf("context_%v_at_%s", wantErr, stage), func(t *testing.T) {
				ctx := newStageTriggeredContext(wantErr)
				pinned := &recoveryAuthoritySourceFake{}
				observed := &recoveryAuthorityOwnedSource{pinned: pinned}
				if stage == "pinned_revalidate" {
					pinned.revalidateErr = errors.New(privateFailure)
				}
				if stage == "observed_revalidate" {
					observed.revalidateErr = errors.New(privateFailure)
				}

				resolved, observation, err := observeRecoveryRsyncSourceWithDependencies(
					ctx,
					provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync},
					recoveryRsyncSourceAuthorityDependencies{
						resolve: func(context.Context, provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
							if stage == "resolve" {
								ctx.trigger()
								return pinned, errors.New(privateFailure)
							}
							if stage == "pinned_revalidate" {
								ctx.trigger()
							}
							return pinned, nil
						},
						capture: func(context.Context, provider.RsyncRestoreSourceRef) (recoveryRsyncAuthoritySnapshot, error) {
							if stage == "capture" {
								ctx.trigger()
								return recoveryRsyncAuthoritySnapshot{}, errors.New(privateFailure)
							}
							return snapshot, nil
						},
						observe: func(context.Context, RecoverySourceNamespaceRequest, provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
							if stage == "observe" {
								ctx.trigger()
								return observed, errors.New(privateFailure)
							}
							if stage == "observed_revalidate" {
								ctx.trigger()
							}
							return observed, nil
						},
						revalidate: func(context.Context, provider.RsyncRestoreSourceRef, recoveryRsyncAuthoritySnapshot) error {
							if stage == "revalidate" {
								ctx.trigger()
								return errors.New(privateFailure)
							}
							return nil
						},
					},
				)
				if resolved != nil || observation != (RecoveryRsyncSourceAuthorityObservation{}) || !errors.Is(err, wantErr) {
					t.Fatalf("stage %s source=%v observation=%+v err=%v, want %v", stage, resolved, observation, err, wantErr)
				}
				if strings.Contains(fmt.Sprint(err), privateFailure) {
					t.Fatalf("stage %s leaked private error: %v", stage, err)
				}
				if pinned.closeCalls.Load() != 1 {
					t.Fatalf("stage %s pinned close calls=%d, want exactly once", stage, pinned.closeCalls.Load())
				}
				if (stage == "observe" || stage == "observed_revalidate" || stage == "revalidate") && observed.closeCalls.Load() != 1 {
					t.Fatalf("stage %s observed close calls=%d, want exactly once", stage, observed.closeCalls.Load())
				}
			})
		}
	}
}

func TestRecoveryRsyncSourceAuthorityObservesOutsideTransactionsAndClosesEveryPath(t *testing.T) {
	ref := provider.RsyncRestoreSourceRef{
		PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
		RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
		CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
		SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
	}
	restoreEntry := provider.RestoreEntry{
		AssetRef: backupasset.AssetRef{RecoveryPointID: ref.RecoveryPointID, EntryID: strings.Repeat("9", 64)},
		Type:     backupasset.CatalogEntryFile, ExpectedSize: 1, ExpectedDigest: strings.Repeat("a", 64),
		TargetObjectDigest: strings.Repeat("b", 64),
	}
	strictEntry := fileaccess.Entry{Name: "payload", Type: fileaccess.EntryFile, Size: 1, SourceRevision: "source-revision"}
	snapshot := rsyncRestoreDurableSnapshot{
		declared: []rsyncRestoreDeclaredEntry{{restore: restoreEntry, name: strictEntry.Name}},
	}

	const privateFailure = "PRIVATE_RESOLVER_FAILURE_CANARY"
	tests := []struct {
		name              string
		observeErr        error
		revalidateErr     error
		treeRevalidateErr error
		wantSuccess       bool
	}{
		{name: "filesystem observation returned capability with error", observeErr: errors.New(privateFailure)},
		{name: "locked snapshot revalidation or commit failed", revalidateErr: errors.New(privateFailure)},
		{name: "pinned tree revalidation failed", treeRevalidateErr: errors.New(privateFailure)},
		{name: "success transfers ownership", wantSuccess: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			phases := make([]string, 0, 3)
			tree := &recoveryAuthorityPinnedTree{revalidateErr: testCase.treeRevalidateErr}
			dependencies := rsyncRestoreSourceResolverDependencies{
				loadSnapshot: func(context.Context, provider.RsyncRestoreSourceRef) (rsyncRestoreDurableSnapshot, error) {
					phases = append(phases, "durable_snapshot_committed")
					return snapshot, nil
				},
				observeFilesystem: func(context.Context, rsyncRestoreDurableSnapshot) (fileaccess.PinnedStrictTree, map[provider.RestoreEntry]fileaccess.Entry, []provider.RestoreEntry, error) {
					phases = append(phases, "filesystem_observation")
					return tree, map[provider.RestoreEntry]fileaccess.Entry{restoreEntry: strictEntry}, []provider.RestoreEntry{restoreEntry}, testCase.observeErr
				},
				revalidateSnapshot: func(context.Context, provider.RsyncRestoreSourceRef, rsyncRestoreDurableSnapshot, map[provider.RestoreEntry]fileaccess.Entry) error {
					phases = append(phases, "locked_revalidation_committed")
					return testCase.revalidateErr
				},
				revalidateSource: func(context.Context, provider.RsyncRestoreSourceRef, rsyncRestoreDurableSnapshot, map[provider.RestoreEntry]fileaccess.Entry) error {
					return nil
				},
			}
			source, err := resolveRsyncRestoreSourceWithDependencies(context.Background(), ref, dependencies)
			if testCase.wantSuccess {
				if err != nil || source == nil {
					t.Fatalf("resolve source=%v err=%v, want transferred ownership", source, err)
				}
				if !reflect.DeepEqual(phases, []string{"durable_snapshot_committed", "filesystem_observation", "locked_revalidation_committed"}) {
					t.Fatalf("authority phases=%v, want snapshot -> filesystem -> locked revalidation", phases)
				}
				if tree.closeCalls.Load() != 0 {
					t.Fatalf("successful source close calls=%d before transfer owner close, want 0", tree.closeCalls.Load())
				}
				if closeErr := source.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
				if closeErr := source.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
				if tree.closeCalls.Load() != 1 {
					t.Fatalf("successful source close calls=%d, want exactly 1", tree.closeCalls.Load())
				}
				return
			}
			if source != nil || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				t.Fatalf("failed resolution source=%v err=%v, want invalid restore request", source, err)
			}
			if strings.Contains(fmt.Sprint(err), privateFailure) {
				t.Fatalf("failed resolution leaked private dependency error: %v", err)
			}
			if tree.closeCalls.Load() != 1 {
				t.Fatalf("failed resolution close calls=%d, want exactly 1", tree.closeCalls.Load())
			}
		})
	}
}

type stageTriggeredContext struct {
	context.Context
	err       error
	triggered atomic.Bool
	done      chan struct{}
	once      sync.Once
}

func newStageTriggeredContext(err error) *stageTriggeredContext {
	return &stageTriggeredContext{
		Context: context.Background(),
		err:     err,
		done:    make(chan struct{}),
	}
}

func (ctx *stageTriggeredContext) trigger() {
	ctx.once.Do(func() {
		ctx.triggered.Store(true)
		close(ctx.done)
	})
}

func (ctx *stageTriggeredContext) Done() <-chan struct{} {
	return ctx.done
}

func (ctx *stageTriggeredContext) Err() error {
	if ctx.triggered.Load() {
		return ctx.err
	}
	return nil
}

type recoveryAuthorityPinnedTree struct {
	revalidateErr error
	closeCalls    atomic.Int64
}

type recoveryAuthoritySourceFake struct {
	revalidateErr   error
	revalidateCalls atomic.Int64
	closeCalls      atomic.Int64
}

type recoveryAuthorityOwnedSource struct {
	pinned        provider.RsyncRestoreSource
	revalidateErr error
	closeOnce     sync.Once
	closeCalls    atomic.Int64
	closeErr      error
}

func (source *recoveryAuthorityOwnedSource) OpenDeclaredRegular(ctx context.Context, entry provider.RestoreEntry) (provider.RsyncRestoreSourceStream, error) {
	return source.pinned.OpenDeclaredRegular(ctx, entry)
}

func (source *recoveryAuthorityOwnedSource) MaterializeDeclaredEntries(ctx context.Context, entries []provider.RestoreEntry) ([]provider.RestoreEntry, error) {
	return source.pinned.MaterializeDeclaredEntries(ctx, entries)
}

func (source *recoveryAuthorityOwnedSource) Revalidate(ctx context.Context) error {
	if source.revalidateErr != nil {
		return source.revalidateErr
	}
	return source.pinned.Revalidate(ctx)
}

func (source *recoveryAuthorityOwnedSource) Close() error {
	source.closeOnce.Do(func() {
		source.closeCalls.Add(1)
		source.closeErr = source.pinned.Close()
	})
	return source.closeErr
}

func (*recoveryAuthoritySourceFake) OpenDeclaredRegular(context.Context, provider.RestoreEntry) (provider.RsyncRestoreSourceStream, error) {
	return nil, provider.ErrRsyncRestoreSourceDrift
}

func (*recoveryAuthoritySourceFake) MaterializeDeclaredEntries(context.Context, []provider.RestoreEntry) ([]provider.RestoreEntry, error) {
	return nil, provider.ErrRsyncRestoreSourceDrift
}

func (source *recoveryAuthoritySourceFake) Revalidate(context.Context) error {
	source.revalidateCalls.Add(1)
	return source.revalidateErr
}

func (source *recoveryAuthoritySourceFake) Close() error {
	source.closeCalls.Add(1)
	return nil
}

func (*recoveryAuthorityPinnedTree) OpenDeclaredRegular(context.Context, fileaccess.Entry) (fileaccess.ReadHandle, fileaccess.ContentStat, error) {
	return nil, fileaccess.ContentStat{}, fileaccess.ErrSourceChanged
}

func (tree *recoveryAuthorityPinnedTree) Revalidate(context.Context) error {
	return tree.revalidateErr
}

func (tree *recoveryAuthorityPinnedTree) Close() error {
	tree.closeCalls.Add(1)
	return nil
}

func TestRsyncResolverRejectsPlanSelectionCatalogRevisionAndCiphertextDrift(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	tests := []struct {
		name   string
		mutate func(*testing.T, *rsyncRestoreResolverFixture, *provider.RsyncRestoreSourceRef)
	}{
		{name: "plan binding", mutate: func(_ *testing.T, _ *rsyncRestoreResolverFixture, ref *provider.RsyncRestoreSourceRef) {
			ref.PlanBindingDigest = strings.Repeat("e", 64)
		}},
		{name: "selection", mutate: func(_ *testing.T, _ *rsyncRestoreResolverFixture, ref *provider.RsyncRestoreSourceRef) {
			ref.SelectionDigest = strings.Repeat("e", 64)
		}},
		{name: "catalog", mutate: func(_ *testing.T, _ *rsyncRestoreResolverFixture, ref *provider.RsyncRestoreSourceRef) {
			ref.CatalogGenerationID = strings.Repeat("e", 32)
		}},
		{name: "source revision", mutate: func(_ *testing.T, _ *rsyncRestoreResolverFixture, ref *provider.RsyncRestoreSourceRef) {
			ref.SourceRevisionDigest = strings.Repeat("e", 64)
		}},
		{name: "repository capability revision", mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture, _ *provider.RsyncRestoreSourceRef) {
			if err := fixture.db.Model(&model.BackupRepository{}).
				Where("id = ?", fixture.ref.RepositoryID).
				Update("capability_revision", gorm.Expr("capability_revision + 1")).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mutable source revision kind", mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture, _ *provider.RsyncRestoreSourceRef) {
			if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).
				Where("id = ?", fixture.ref.PlanID).
				Update("source_revision_kind", "observation").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mutable plan item fingerprint", mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture, _ *provider.RsyncRestoreSourceRef) {
			if err := fixture.db.Model(&model.BackupAssetRecoveryPlanItem{}).
				Where("plan_id = ?", fixture.ref.PlanID).
				Update("source_fingerprint", strings.Repeat("e", 64)).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "manifest", mutate: func(_ *testing.T, _ *rsyncRestoreResolverFixture, ref *provider.RsyncRestoreSourceRef) {
			ref.ManifestDigest = strings.Repeat("e", 64)
		}},
		{name: "encrypted plan source", mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture, _ *provider.RsyncRestoreSourceRef) {
			var plan model.BackupAssetRecoveryPlan
			if err := fixture.db.First(&plan, "id = ?", fixture.ref.PlanID).Error; err != nil {
				t.Fatal(err)
			}
			plan.EncryptedSourceLocator = "FAKE_REPLACED_RSYNC_POINT_LOCATOR_FOR_TEST_ONLY"
			if err := fixture.db.Save(&plan).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "matching encrypted point and plan source", mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture, _ *provider.RsyncRestoreSourceRef) {
			const replacement = "FAKE_REPLACED_RSYNC_POINT_LOCATOR_FOR_TEST_ONLY"
			var plan model.BackupAssetRecoveryPlan
			if err := fixture.db.First(&plan, "id = ?", fixture.ref.PlanID).Error; err != nil {
				t.Fatal(err)
			}
			var point model.RecoveryPoint
			if err := fixture.db.First(&point, "id = ?", fixture.ref.RecoveryPointID).Error; err != nil {
				t.Fatal(err)
			}
			plan.EncryptedSourceLocator = replacement
			point.EncryptedProviderLocator = replacement
			if err := fixture.db.Save(&plan).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Save(&point).Error; err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRsyncRestoreResolverFixture(t)
			candidate := fixture.ref
			test.mutate(t, fixture, &candidate)
			if source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), candidate); !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				if source != nil {
					_ = source.Close()
				}
				t.Fatalf("substituted Rsync source error=%v, want invalid restore request", err)
			}
		})
	}
}

func TestRsyncResolverRequiresExactDurablePlanItems(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	if err := fixture.db.Where("plan_id = ?", fixture.ref.PlanID).Delete(&model.BackupAssetRecoveryPlanItem{}).Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("resolve without durable plan items error=%v, want invalid restore request", err)
	}
}

func TestRsyncResolverRejectsMissingSelectedCatalogEntry(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	if err := fixture.db.Where("generation_id = ?", fixture.ref.CatalogGenerationID).Delete(&model.CatalogEntry{}).Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("resolve without selected catalog entry error=%v, want invalid restore request", err)
	}
}

func TestRsyncResolverRejectsImmutableLocatorDigestDrift(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).
		Where("id = ?", fixture.ref.PlanID).
		Update("immutable_locator_digest", strings.Repeat("8", 64)).Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("resolve with stale immutable locator digest error=%v, want invalid restore request", err)
	}
}

type rsyncRestoreResolverFixture struct {
	service           *Service
	db                *gorm.DB
	ref               provider.RsyncRestoreSourceRef
	entry             provider.RestoreEntry
	content           string
	root              string
	taskSource        string
	sourceFingerprint string
}

func newRsyncRestoreResolverFixture(t *testing.T) *rsyncRestoreResolverFixture {
	return newRsyncRestoreResolverFixtureWithAlternate(t, false)
}

func newRsyncRestoreResolverFixtureWithAlternate(t *testing.T, alternate bool) *rsyncRestoreResolverFixture {
	t.Helper()
	fixture := newRsyncPublicationFixture(t)
	markerKey, err := fixture.service.rsyncMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	managedRoot := filepath.Join(t.TempDir(), "managed-rsync-root")
	bootstrap, err := provider.BootstrapRsyncManagedRoot(context.Background(), provider.RsyncManagedRootBootstrapRequest{
		ManagedRoot: managedRoot, RepositoryID: fixture.repository.ID, MarkerKey: markerKey, CreatedAt: fixture.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.binding.ManagedRootLocator = managedRoot
	fixture.binding.RootMarkerDigest = bootstrap.RepositoryMarkerDigest
	fixture.binding.ManagedRootIdentityDigest = bootstrap.ManagedRootIdentityDigest
	bindingPayload, err := encodeManagedRsyncBindingDocumentV2(fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := managedRsyncRepositoryIdentity(fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	var storedBinding model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ?", fixture.repository.ID).First(&storedBinding).Error; err != nil {
		t.Fatal(err)
	}
	storedBinding.EncryptedConfig = bindingPayload
	storedBinding.UpdatedAt = fixture.now
	if err := fixture.db.Save(&storedBinding).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupRepository{}).Where("id = ?", fixture.repository.ID).Update("repository_identity", identity).Error; err != nil {
		t.Fatal(err)
	}
	fixture.repository.RepositoryIdentity = &identity

	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) })
	state, ok := execution.(*rsyncPublicationExecution)
	if !ok {
		t.Fatalf("managed Rsync execution type=%T", execution)
	}
	input, err := state.RsyncTreePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	sourceDirectory := t.TempDir()
	fixture.task.RsyncSource = sourceDirectory
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Update("rsync_source", sourceDirectory).Error; err != nil {
		t.Fatal(err)
	}
	const sourceName = "payload.txt"
	const sourceContents = "managed restore payload\n"
	if err := os.WriteFile(filepath.Join(sourceDirectory, sourceName), []byte(sourceContents), 0o600); err != nil {
		t.Fatal(err)
	}
	if alternate {
		alternateDirectory := filepath.Join(sourceDirectory, "alternate")
		if err := os.Mkdir(alternateDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(alternateDirectory, sourceName), []byte(sourceContents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	input.Source = provider.RsyncTreeCommandSource{LocalPath: sourceDirectory}
	strategy, err := provider.NewLocalRsyncTreePublicationStrategy(func() time.Time { return fixture.now })
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := strategy.Prepare(context.Background(), provider.PublicationPrepareRequest{
		Attempt: provider.NewRsyncTreePublicationAttempt(state.attempt), RsyncTreeInput: &input,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Execute(context.Background(), prepared, provider.PublicationProgress{})
	if err != nil {
		t.Fatal(err)
	}
	commitRecord, err := strategy.RecordCommit(context.Background(), prepared, result)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := commitRecord.RsyncTreeCommit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", state.attempt.RecoveryPointID).Updates(map[string]any{
		"state": string(backupasset.RecoveryPointCommitted), "committed_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.AutoMigrate(&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryPlanItem{}); err != nil {
		t.Fatal(err)
	}

	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	manifest := model.RecoveryPointManifest{
		ID: strings.Repeat("3", 32), RecoveryPointID: point.ID, Revision: 1, DigestAlgorithm: "sha256",
		Digest: point.ManifestDigest, Generator: "rsync-managed-tree", GeneratorVersion: "v1",
		Completeness: string(backupasset.ManifestComplete), EntryCount: point.EntryCount, LogicalBytes: point.LogicalBytes,
		IsActive: true, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	immutableLocatorDigest, err := publication.ImmutableLocatorDigest(
		fixture.repository.ID, backupasset.ProviderRsync, point.ID, point.EncryptedProviderLocator,
	)
	if err != nil {
		t.Fatal(err)
	}
	finishedAt := fixture.now
	generation := model.CatalogGeneration{
		ID: strings.Repeat("4", 32), RecoveryPointID: point.ID, Generation: 1, State: string(catalog.GenerationComplete), IsActive: true,
		ManifestID:        &manifest.ID,
		SourceFingerprint: point.SourceFingerprint, ExpectedEntryCount: point.EntryCount, WrittenEntryCount: point.EntryCount,
		ExpectedDigest: point.ManifestDigest, WrittenDigest: strings.Repeat("9", 64),
		StartedAt: fixture.now.Add(-time.Minute), FinishedAt: &finishedAt, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	plan := model.BackupAssetRecoveryPlan{
		ID:                       strings.Repeat("5", 32),
		RequesterID:              1,
		Endpoint:                 "recovery_execute",
		IdempotencyKeyDigest:     strings.Repeat("6", 64),
		RepositoryID:             fixture.repository.ID,
		RecoveryPointID:          point.ID,
		SourceRevisionDigest:     strings.Repeat("7", 64),
		SourceRevisionKind:       "immutable",
		ImmutableLocatorDigest:   immutableLocatorDigest,
		ImmutableManifestDigest:  point.ManifestDigest,
		CatalogGenerationID:      generation.ID,
		EncryptedSourceLocator:   point.EncryptedProviderLocator,
		TargetMode:               "isolated",
		TargetNodeID:             fixture.task.NodeID,
		TargetRootID:             strings.Repeat("9", 32),
		RootLocatorDigest:        strings.Repeat("a", 64),
		PathDigest:               strings.Repeat("b", 64),
		TargetBaseRevision:       "node-revision",
		CredentialScopeRevision:  "credential-revision",
		RootRevision:             "root-revision",
		FilesystemRevision:       "filesystem-revision",
		SelectionDigest:          strings.Repeat("c", 64),
		BindingDigest:            strings.Repeat("d", 64),
		CapabilityRevision:       "capability-revision",
		ConflictPolicy:           "fail_on_conflict",
		OperationSetDigest:       strings.Repeat("e", 64),
		DeleteSetDigest:          strings.Repeat("f", 64),
		SecurityDecision:         "clean",
		SecurityDecisionDigest:   strings.Repeat("1", 64),
		SecurityFindingSetDigest: strings.Repeat("2", 64),
		SecurityPolicyRevision:   "security-revision",
		PreflightRevision:        "preflight-revision",
		PreflightExpiresAt:       fixture.now.Add(time.Hour),
		State:                    "draft",
		TransitionRevision:       1,
		CreatedAt:                fixture.now,
		UpdatedAt:                fixture.now,
	}
	if err := fixture.db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	// Deliberately seed the historical none generation for restore compatibility;
	// the current committed Rsync Catalog producer writes strong fingerprints.
	entry := model.CatalogEntry{
		GenerationID: generation.ID, EntryID: strings.Repeat("f", 64), RecoveryPointID: point.ID,
		NormalizedPath: sourceName, Name: sourceName, EntryType: string(backupasset.CatalogEntryFile), Size: int64(len(sourceContents)),
		FingerprintStrength:      string(catalog.FingerprintNone),
		EncryptedProviderLocator: `{"version":1,"native":"` + sourceName + `"}`, SecurityState: "sealed", CreatedAt: fixture.now,
	}
	if err := fixture.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	item := model.BackupAssetRecoveryPlanItem{
		ID: strings.Repeat("b", 32), PlanID: plan.ID, Ordinal: 0, RecoveryPointID: point.ID, CatalogGenerationID: generation.ID,
		EntryID: entry.EntryID, EntryType: entry.EntryType, RelativePathDigest: publication.RecoveryPlanItemPathDigest(
			plan.RepositoryID, point.ID, generation.ID, entry.EntryID, entry.NormalizedPath,
		), CreatedAt: fixture.now,
	}
	if err := fixture.db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry, Keyring: fixture.service.keyring,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, History: fixture.service.history, Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rsyncRestoreResolverFixture{service: service, db: fixture.db, ref: provider.RsyncRestoreSourceRef{
		PlanID:               plan.ID,
		PlanBindingDigest:    plan.BindingDigest,
		RepositoryID:         plan.RepositoryID,
		RecoveryPointID:      plan.RecoveryPointID,
		CatalogGenerationID:  plan.CatalogGenerationID,
		SelectionDigest:      plan.SelectionDigest,
		SourceRevisionDigest: plan.SourceRevisionDigest,
		ManifestDigest:       plan.ImmutableManifestDigest,
	}, entry: provider.RestoreEntry{
		AssetRef:           backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID},
		Type:               backupasset.CatalogEntryFile,
		ExpectedSize:       entry.Size,
		TargetObjectDigest: item.RelativePathDigest,
	}, content: sourceContents, root: managedRoot, taskSource: sourceDirectory,
		sourceFingerprint: point.SourceFingerprint,
	}
}

func TestRsyncResolverExposesOnlyExactDeclaredEntries(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve durable Rsync source: %v", err)
	}
	defer func() { _ = source.Close() }()
	materialized, err := source.MaterializeDeclaredEntries(context.Background(), []provider.RestoreEntry{fixture.entry})
	if err != nil || len(materialized) != 1 {
		t.Fatalf("materialize exact declared entry: entries=%#v err=%v", materialized, err)
	}
	strictEntry := materialized[0]
	if strictEntry.ExpectedDigest != "51f1348ebc6156725f54b40bbef3ed19683928c3ad1037c183cf282a838f15be" {
		t.Fatalf("materialized digest = %q, want digest of the published payload", strictEntry.ExpectedDigest)
	}
	if entries, err := source.MaterializeDeclaredEntries(context.Background(), nil); entries != nil || !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) {
		t.Fatalf("missing declared entry materialization = %#v, %v; want source drift", entries, err)
	}
	if entries, err := source.MaterializeDeclaredEntries(context.Background(), []provider.RestoreEntry{fixture.entry, fixture.entry}); entries != nil || !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) {
		t.Fatalf("duplicate declared entry materialization = %#v, %v; want source drift", entries, err)
	}
	undeclared := fixture.entry
	undeclared.AssetRef.EntryID = strings.Repeat("e", 64)
	if entries, err := source.MaterializeDeclaredEntries(context.Background(), []provider.RestoreEntry{undeclared}); entries != nil || !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) {
		t.Fatalf("undeclared entry materialization = %#v, %v; want source drift", entries, err)
	}

	stream, err := source.OpenDeclaredRegular(context.Background(), strictEntry)
	if err != nil {
		t.Fatalf("open exact declared entry: %v", err)
	}
	payload, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read exact declared entry: read=%v close=%v", readErr, closeErr)
	}
	if string(payload) != fixture.content {
		t.Fatalf("declared entry payload = %q, want %q", payload, fixture.content)
	}

	for _, test := range []struct {
		name   string
		mutate func(*provider.RestoreEntry)
	}{
		{name: "asset ref", mutate: func(entry *provider.RestoreEntry) { entry.AssetRef.EntryID = strings.Repeat("e", 64) }},
		{name: "type", mutate: func(entry *provider.RestoreEntry) { entry.Type = backupasset.CatalogEntryDirectory }},
		{name: "size", mutate: func(entry *provider.RestoreEntry) { entry.ExpectedSize++ }},
		{name: "digest", mutate: func(entry *provider.RestoreEntry) { entry.ExpectedDigest = strings.Repeat("e", 64) }},
		{name: "target object", mutate: func(entry *provider.RestoreEntry) { entry.TargetObjectDigest = strings.Repeat("e", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := strictEntry
			test.mutate(&candidate)
			stream, err := source.OpenDeclaredRegular(context.Background(), candidate)
			if stream != nil {
				_ = stream.Close()
			}
			if !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) {
				t.Fatalf("forged declared entry error=%v, want source drift", err)
			}
		})
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close durable Rsync source: %v", err)
	}
	closedStream, err := source.OpenDeclaredRegular(context.Background(), strictEntry)
	if closedStream != nil {
		_ = closedStream.Close()
	}
	if !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) {
		t.Fatalf("open declared entry after source close = %v, want source drift", err)
	}
}

func TestRsyncResolverAcceptsCatalogProjectionDigestDistinctFromManifest(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve Catalog-none source before strong projection: %v", err)
	}
	materialized, err := source.MaterializeDeclaredEntries(context.Background(), []provider.RestoreEntry{fixture.entry})
	closeErr := source.Close()
	if err != nil || closeErr != nil || len(materialized) != 1 {
		t.Fatalf("materialize strong projection digest: entries=%#v materialize=%v close=%v", materialized, err, closeErr)
	}
	if err := fixture.db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).
		Updates(map[string]any{
			"fingerprint": materialized[0].ExpectedDigest, "fingerprint_strength": string(catalog.FingerprintStrong),
		}).Error; err != nil {
		t.Fatal(err)
	}
	var generation model.CatalogGeneration
	if err := fixture.db.First(&generation, "id = ?", fixture.ref.CatalogGenerationID).Error; err != nil {
		t.Fatal(err)
	}
	if generation.WrittenDigest == generation.ExpectedDigest {
		t.Fatal("fixture must keep the Catalog projection digest distinct from the provider manifest digest")
	}

	source, err = fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve with distinct Catalog projection digest: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close resolved source: %v", err)
	}
}

func TestRsyncResolverDerivesMissingCatalogFingerprintFromAuthenticatedTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	var entry model.CatalogEntry
	if err := fixture.db.First(&entry,
		"generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).Error; err != nil {
		t.Fatal(err)
	}
	if entry.Fingerprint != "" || entry.FingerprintStrength != string(catalog.FingerprintNone) {
		t.Fatalf("fixture Catalog fingerprint = %q/%q, want unavailable/none", entry.Fingerprint, entry.FingerprintStrength)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve from authenticated managed-tree manifest: %v", err)
	}
	defer func() { _ = source.Close() }()
	materialized, err := source.MaterializeDeclaredEntries(context.Background(), []provider.RestoreEntry{fixture.entry})
	if err != nil || len(materialized) != 1 || materialized[0].Validate(fixture.ref.RecoveryPointID) != nil {
		t.Fatalf("materialize manifest-backed declared entry: entries=%#v err=%v", materialized, err)
	}
}

func TestRsyncRestorePortMaterializesFingerprintNoneDigestFromDurableSource(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.First(&plan, "id = ?", fixture.ref.PlanID).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryPlanItem
	if err := fixture.db.Where("plan_id = ?", plan.ID).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	var catalogEntry model.CatalogEntry
	if err := fixture.db.Where("generation_id = ? AND entry_id = ?", item.CatalogGenerationID, item.EntryID).
		First(&catalogEntry).Error; err != nil {
		t.Fatal(err)
	}
	if catalogEntry.Fingerprint != "" || catalogEntry.FingerprintStrength != string(catalog.FingerprintNone) {
		t.Fatalf("durable Catalog entry fingerprint = %q/%q, want unavailable/none", catalogEntry.Fingerprint, catalogEntry.FingerprintStrength)
	}
	ref := provider.RsyncRestoreSourceRef{
		PlanID:               plan.ID,
		PlanBindingDigest:    plan.BindingDigest,
		RepositoryID:         plan.RepositoryID,
		RecoveryPointID:      plan.RecoveryPointID,
		CatalogGenerationID:  plan.CatalogGenerationID,
		SelectionDigest:      plan.SelectionDigest,
		SourceRevisionDigest: plan.SourceRevisionDigest,
		ManifestDigest:       plan.ImmutableManifestDigest,
	}
	if err := ref.Validate(); err != nil {
		t.Fatalf("project durable Recovery plan to scalar Rsync ref: %v", err)
	}

	request := validRepositoryRsyncRestoreRequest(t)
	request.Rsync = &provider.RsyncRestoreRequest{ManifestDigest: ref.ManifestDigest, SourceRef: ref}
	request.Entries = []provider.RestoreEntry{{
		AssetRef: backupasset.AssetRef{RecoveryPointID: item.RecoveryPointID, EntryID: item.EntryID},
		Type:     backupasset.CatalogEntryType(item.EntryType), ExpectedSize: catalogEntry.Size,
		TargetObjectDigest: item.RelativePathDigest,
	}}
	durableFacts := request.Entries[0]
	if request.Entries[0].ExpectedDigest != "" {
		t.Fatal("production-path fixture invented a caller-side content digest")
	}

	now := time.Now().UTC()
	preflightPermit, err := provider.NewTargetPreflightPermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session: provider.TargetSession{
			ID: strings.Repeat("a", 32), Purpose: provider.TargetPurposePreflight,
			CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute),
		},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}
	verifyPermit, err := provider.NewTargetVerifyPermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session: provider.TargetSession{
			ID: strings.Repeat("b", 32), Purpose: provider.TargetPurposeVerify,
			CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute),
		},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}
	reconcilePermit, err := provider.NewTargetReconcilePermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session: provider.TargetSession{
			ID: strings.Repeat("c", 32), Purpose: provider.TargetPurposeReconcile,
			CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute),
		},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}

	runner := &recordingRepositoryRsyncRestoreRunner{}
	port := NewRsyncRestorePort(fixture.service, &recordingRepositoryRsyncTargetWriter{}, runner)
	if _, err := port.Preflight(context.Background(), provider.RestorePreflightRequest{Request: request, Permit: preflightPermit}); err != nil {
		t.Errorf("Preflight with scalar ref and durable entry facts: %v", err)
	}
	if _, err := port.Execute(context.Background(), request, provider.RestoreProgress{}); err != nil {
		t.Errorf("Execute with scalar ref and durable entry facts: %v", err)
	}
	if _, err := port.Verify(context.Background(), provider.RestoreVerifyRequest{Request: request, Permit: verifyPermit}); err != nil {
		t.Errorf("Verify with scalar ref and durable entry facts: %v", err)
	}
	if _, err := port.Reconcile(context.Background(), provider.RestoreReconcileRequest{Request: request, Permit: reconcilePermit}); err != nil {
		t.Errorf("Reconcile with scalar ref and durable entry facts: %v", err)
	}

	phaseIntents := map[string]provider.RsyncRestoreIntent{}
	if len(runner.preflights) == 1 {
		phaseIntents["preflight"] = runner.preflights[0].RsyncRestoreIntent
	}
	if len(runner.executes) == 1 {
		phaseIntents["execute"] = runner.executes[0].RsyncRestoreIntent
	}
	if len(runner.verifies) == 1 {
		phaseIntents["verify"] = runner.verifies[0].RsyncRestoreIntent
	}
	if len(runner.reconciles) == 1 {
		phaseIntents["reconcile"] = runner.reconciles[0].RsyncRestoreIntent
	}
	if len(phaseIntents) != 4 {
		t.Fatalf("runner phases = %v, want preflight/execute/verify/reconcile", phaseIntents)
	}
	for phase, intent := range phaseIntents {
		entries := intent.Entries
		if len(entries) != 1 || entries[0].Validate(ref.RecoveryPointID) != nil || entries[0].ExpectedDigest != "51f1348ebc6156725f54b40bbef3ed19683928c3ad1037c183cf282a838f15be" {
			t.Errorf("%s runner entries = %#v, want one exact Repository-materialized strict entry", phase, entries)
			continue
		}
		strict := entries[0]
		strict.ExpectedDigest = ""
		if strict != durableFacts {
			t.Errorf("%s runner durable entry facts = %#v, want %#v", phase, strict, durableFacts)
		}
		if intent.ManifestDigest != ref.ManifestDigest {
			t.Errorf("%s runner manifest = %q, want durable manifest %q", phase, intent.ManifestDigest, ref.ManifestDigest)
		}
		stream, err := intent.Source.OpenDeclaredRegular(context.Background(), entries[0])
		if stream != nil {
			_ = stream.Close()
		}
		if !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) {
			t.Errorf("%s source open after port returned = %v, want closed source", phase, err)
		}
	}
	if request.Entries[0].ExpectedDigest != "" {
		t.Fatalf("caller request learned Repository-private digest %q", request.Entries[0].ExpectedDigest)
	}
}

func TestRsyncResolverRejectsEmptyCatalogFingerprintStrength(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	if err := fixture.db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).
		Update("fingerprint_strength", "").Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("empty Catalog fingerprint strength error=%v, want invalid restore request", err)
	}
}

func TestRsyncResolverRejectsStrongCatalogFingerprintMismatchAgainstAuthenticatedTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixture(t)
	if err := fixture.db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).
		Updates(map[string]any{
			"fingerprint": strings.Repeat("0", 64), "fingerprint_strength": string(catalog.FingerprintStrong),
		}).Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("strong Catalog fingerprint mismatch error=%v, want invalid restore request", err)
	}
}

func TestRsyncResolverRejectsCatalogLocatorSubstitutionWithinAuthenticatedTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixtureWithAlternate(t, true)
	var entry model.CatalogEntry
	if err := fixture.db.First(&entry,
		"generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).Error; err != nil {
		t.Fatal(err)
	}
	entry.EncryptedProviderLocator = `{"version":1,"native":"alternate/payload.txt"}`
	if err := fixture.db.Save(&entry).Error; err != nil {
		t.Fatal(err)
	}

	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("Catalog locator substitution error=%v, want invalid restore request", err)
	}
}

func TestRsyncResolverRejectsSameKeyCatalogEntryPathSubstitutionWithinAuthenticatedTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	fixture := newRsyncRestoreResolverFixtureWithAlternate(t, true)
	source, err := fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if err != nil {
		t.Fatalf("resolve frozen Catalog entry before substitution: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close frozen Catalog source: %v", err)
	}

	var entry model.CatalogEntry
	if err := fixture.db.First(&entry,
		"generation_id = ? AND entry_id = ?", fixture.ref.CatalogGenerationID, fixture.entry.AssetRef.EntryID).Error; err != nil {
		t.Fatal(err)
	}
	entry.NormalizedPath = "alternate/payload.txt"
	entry.EncryptedProviderLocator = `{"version":1,"native":"alternate/payload.txt"}`
	if err := fixture.db.Save(&entry).Error; err != nil {
		t.Fatal(err)
	}

	source, err = fixture.service.ResolveRsyncRestoreSource(context.Background(), fixture.ref)
	if source != nil {
		_ = source.Close()
	}
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("same-key Catalog substitution error=%v, want invalid restore request", err)
	}
	if err != nil && strings.Contains(err.Error(), "alternate/payload.txt") {
		t.Fatalf("same-key Catalog substitution leaked a private path: %v", err)
	}
}

func TestRsyncRestorePortRevalidatesAllFourPhases(t *testing.T) {
	request := validRepositoryRsyncRestoreRequest(t)
	resolver := &recordingRepositoryRsyncRestoreSourceResolver{root: t.TempDir()}
	writer := &recordingRepositoryRsyncTargetWriter{}
	runner := &recordingRepositoryRsyncRestoreRunner{}
	port := NewRsyncRestorePort(resolver, writer, runner)
	now := time.Now().UTC()

	preflightPermit, err := provider.NewTargetPreflightPermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session:             provider.TargetSession{ID: strings.Repeat("a", 32), Purpose: provider.TargetPurposePreflight, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := port.Preflight(context.Background(), provider.RestorePreflightRequest{Request: request, Permit: preflightPermit}); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if _, err := port.Execute(context.Background(), request, provider.RestoreProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	verifyPermit, err := provider.NewTargetVerifyPermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session:             provider.TargetSession{ID: strings.Repeat("b", 32), Purpose: provider.TargetPurposeVerify, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := port.Verify(context.Background(), provider.RestoreVerifyRequest{Request: request, Permit: verifyPermit}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	reconcilePermit, err := provider.NewTargetReconcilePermit(provider.TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session:             provider.TargetSession{ID: strings.Repeat("c", 32), Purpose: provider.TargetPurposeReconcile, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)},
	}, request.Target, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := port.Reconcile(context.Background(), provider.RestoreReconcileRequest{Request: request, Permit: reconcilePermit}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if got := len(resolver.refs); got != 4 {
		t.Fatalf("resolver calls = %d, want 4", got)
	}
	for index, ref := range resolver.refs {
		if ref != request.Rsync.SourceRef {
			t.Fatalf("resolver ref %d = %#v, want exact scalar ref %#v", index, ref, request.Rsync.SourceRef)
		}
	}
	if len(runner.preflights) != 1 || len(runner.executes) != 1 || len(runner.verifies) != 1 || len(runner.reconciles) != 1 {
		t.Fatalf("runner calls preflight=%d execute=%d verify=%d reconcile=%d, want one each", len(runner.preflights), len(runner.executes), len(runner.verifies), len(runner.reconciles))
	}
	for _, injected := range []provider.RsyncTargetWriter{
		runner.preflights[0].TargetWriter,
		runner.executes[0].TargetWriter,
		runner.verifies[0].TargetWriter,
		runner.reconciles[0].TargetWriter,
	} {
		if injected != writer {
			t.Fatalf("runner target writer = %T, want exact injected authority", injected)
		}
	}
	for index, tree := range resolver.trees {
		if tree.revalidations != 2 || tree.closes != 1 {
			t.Fatalf("source %d revalidations=%d closes=%d, want 2/1", index, tree.revalidations, tree.closes)
		}
	}
}

type repositoryRsyncTestPermits struct {
	preflight provider.TargetPreflightPermit
	verify    provider.TargetVerifyPermit
	reconcile provider.TargetReconcilePermit
}

func newRepositoryRsyncTestPermits(t *testing.T, target provider.RestoreTarget, issuedAt, expiresAt time.Time) repositoryRsyncTestPermits {
	t.Helper()
	observation := func(purpose provider.RestoreTargetPurpose) provider.TargetObservationPermit {
		return provider.TargetObservationPermit{
			TargetBindingDigest: target.BindingDigest,
			Session: provider.TargetSession{
				ID: strings.Repeat("a", 32), Purpose: purpose,
				CredentialRevision: "credential-revision-1", ExpiresAt: expiresAt,
			},
		}
	}
	preflight, err := provider.NewTargetPreflightPermit(observation(provider.TargetPurposePreflight), target, issuedAt)
	if err != nil {
		t.Fatalf("create preflight permit: %v", err)
	}
	verify, err := provider.NewTargetVerifyPermit(observation(provider.TargetPurposeVerify), target, issuedAt)
	if err != nil {
		t.Fatalf("create verify permit: %v", err)
	}
	reconcile, err := provider.NewTargetReconcilePermit(observation(provider.TargetPurposeReconcile), target, issuedAt)
	if err != nil {
		t.Fatalf("create reconcile permit: %v", err)
	}
	return repositoryRsyncTestPermits{preflight: preflight, verify: verify, reconcile: reconcile}
}

type repositoryRsyncTestPhase struct {
	name      string
	invoke    func(*RsyncRestorePort, provider.RestoreRequest, repositoryRsyncTestPermits) (any, error)
	callCount func(*recordingRepositoryRsyncRestoreRunner) int
}

func repositoryRsyncTestPhases() []repositoryRsyncTestPhase {
	return []repositoryRsyncTestPhase{
		{
			name: "preflight",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest, permits repositoryRsyncTestPermits) (any, error) {
				return port.Preflight(context.Background(), provider.RestorePreflightRequest{Request: request, Permit: permits.preflight})
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.preflights) },
		},
		{
			name: "execute",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest, _ repositoryRsyncTestPermits) (any, error) {
				return port.Execute(context.Background(), request, provider.RestoreProgress{})
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.executes) },
		},
		{
			name: "verify",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest, permits repositoryRsyncTestPermits) (any, error) {
				return port.Verify(context.Background(), provider.RestoreVerifyRequest{Request: request, Permit: permits.verify})
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.verifies) },
		},
		{
			name: "reconcile",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest, permits repositoryRsyncTestPermits) (any, error) {
				return port.Reconcile(context.Background(), provider.RestoreReconcileRequest{Request: request, Permit: permits.reconcile})
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.reconciles) },
		},
	}
}

func assertRepositoryRsyncPortRejectedBeforeEffects(t *testing.T, result any, err error, resolver *recordingRepositoryRsyncRestoreSourceResolver, runner *recordingRepositoryRsyncRestoreRunner, writer *recordingRepositoryRsyncTargetWriter) {
	t.Helper()
	if !errors.Is(err, provider.ErrInvalidRestoreRequest) || !reflect.ValueOf(result).IsZero() {
		t.Fatalf("restore result=%#v err=%v, want zero result and invalid restore request", result, err)
	}
	if len(resolver.refs) != 0 || len(resolver.trees) != 0 || len(runner.preflights)+len(runner.executes)+len(runner.verifies)+len(runner.reconciles) != 0 || writer.calls != 0 {
		t.Fatalf("rejected request caused resolver=%d runner=%d writer=%d calls", len(resolver.refs), len(runner.preflights)+len(runner.executes)+len(runner.verifies)+len(runner.reconciles), writer.calls)
	}
}

func TestRsyncRestorePortRejectsInvalidIntentBeforeEffects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *provider.RestoreRequest)
	}{
		{name: "empty request", mutate: func(_ *testing.T, request *provider.RestoreRequest) { *request = provider.RestoreRequest{} }},
		{name: "unknown provider", mutate: func(_ *testing.T, request *provider.RestoreRequest) { request.Provider = backupasset.ProviderCommand }},
		{name: "wrong provider", mutate: func(_ *testing.T, request *provider.RestoreRequest) { request.Provider = backupasset.ProviderRestic }},
		{name: "missing rsync arm", mutate: func(_ *testing.T, request *provider.RestoreRequest) { request.Rsync = nil }},
		{name: "dual restic arm", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Restic = &provider.ResticRestoreRequest{}
		}},
		{name: "dual rclone arm", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Rclone = &provider.RcloneRestoreRequest{}
		}},
		{name: "raw source arm", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Source = provider.RestoreSource{Provider: backupasset.ProviderRsync, RepositoryID: request.Rsync.SourceRef.RepositoryID, RecoveryPointID: request.Rsync.SourceRef.RecoveryPointID, Locator: "FAKE_RAW_RSYNC_SOURCE_FOR_TEST_ONLY"}
		}},
		{name: "recovery point substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Rsync.SourceRef.RecoveryPointID = strings.Repeat("e", 32)
		}},
		{name: "manifest substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Rsync.SourceRef.ManifestDigest = strings.Repeat("e", 64)
		}},
		{name: "caller supplied content digest", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Entries[0].ExpectedDigest = strings.Repeat("e", 64)
		}},
		{name: "target root substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) { request.Target.RootID = "other-approved-root" }},
		{name: "target root digest substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Target.RootLocatorDigest = strings.Repeat("e", 64)
		}},
		{name: "target path substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Target.TargetPathDigest = strings.Repeat("e", 64)
		}},
		{name: "root revision substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Target.RootRevision = "other-root-revision"
		}},
		{name: "target revision substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Target.TargetRevision = "other-target-revision"
		}},
		{name: "valid replacement target", mutate: func(t *testing.T, request *provider.RestoreRequest) {
			target, err := provider.NewRestoreTarget(request.Target.NodeID, "other-approved-root", request.Target.RootLocatorDigest, request.Target.TargetPathDigest, request.Target.RootRevision, request.Target.TargetRevision)
			if err != nil {
				t.Fatal(err)
			}
			request.Target = target
		}},
		{name: "attempt fence substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) { request.Fence.AttemptFence++ }},
		{name: "node fence substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) { request.Fence.NodeFence++ }},
		{name: "fence revision substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Fence.ExpectedTargetRevision = "other-target-revision"
		}},
		{name: "checkpoint revision substitution", mutate: func(_ *testing.T, request *provider.RestoreRequest) {
			request.Checkpoint.VerifiedTargetRevision = "other-target-revision"
		}},
	}
	for _, phase := range repositoryRsyncTestPhases() {
		for _, test := range tests {
			t.Run(phase.name+"/"+test.name, func(t *testing.T) {
				request := validRepositoryRsyncRestoreRequest(t)
				now := time.Now().UTC()
				permits := newRepositoryRsyncTestPermits(t, request.Target, now, now.Add(time.Minute))
				test.mutate(t, &request)
				resolver := &recordingRepositoryRsyncRestoreSourceResolver{}
				runner := &recordingRepositoryRsyncRestoreRunner{}
				writer := &recordingRepositoryRsyncTargetWriter{}
				result, err := phase.invoke(NewRsyncRestorePort(resolver, writer, runner), request, permits)
				assertRepositoryRsyncPortRejectedBeforeEffects(t, result, err, resolver, runner, writer)
			})
		}
	}
}

func TestRsyncRestorePortRejectsMutationPermitSubstitutionBeforeEffects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*provider.RestoreRequest)
	}{
		{name: "missing latch", mutate: func(request *provider.RestoreRequest) { request.MutationPermit.UseLatchID = "" }},
		{name: "wrong latch", mutate: func(request *provider.RestoreRequest) { request.MutationPermit.UseLatchID = "other-latch" }},
		{name: "target binding", mutate: func(request *provider.RestoreRequest) {
			request.MutationPermit.TargetBindingDigest = strings.Repeat("e", 64)
		}},
		{name: "job", mutate: func(request *provider.RestoreRequest) { request.MutationPermit.JobID = strings.Repeat("e", 32) }},
		{name: "attempt", mutate: func(request *provider.RestoreRequest) { request.MutationPermit.AttemptID = strings.Repeat("e", 32) }},
		{name: "node lease", mutate: func(request *provider.RestoreRequest) { request.MutationPermit.NodeLeaseID = strings.Repeat("e", 32) }},
		{name: "attempt fence", mutate: func(request *provider.RestoreRequest) { request.MutationPermit.AttemptFence++ }},
		{name: "node fence", mutate: func(request *provider.RestoreRequest) { request.MutationPermit.NodeFence++ }},
		{name: "target revision", mutate: func(request *provider.RestoreRequest) {
			request.MutationPermit.ExpectedTargetRevision = "other-target-revision"
		}},
		{name: "missing session", mutate: func(request *provider.RestoreRequest) { request.MutationPermit.Session.ID = "" }},
		{name: "wrong purpose", mutate: func(request *provider.RestoreRequest) {
			request.MutationPermit.Session.Purpose = provider.TargetPurposeVerify
		}},
		{name: "missing credential revision", mutate: func(request *provider.RestoreRequest) { request.MutationPermit.Session.CredentialRevision = "" }},
		{name: "expired session", mutate: func(request *provider.RestoreRequest) {
			request.MutationPermit.Session.ExpiresAt = time.Now().UTC().Add(-time.Minute)
		}},
		{name: "replacement fence and checkpoint", mutate: func(request *provider.RestoreRequest) {
			request.Fence.AttemptFence++
			request.Checkpoint.AttemptFence++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validRepositoryRsyncRestoreRequest(t)
			test.mutate(&request)
			resolver := &recordingRepositoryRsyncRestoreSourceResolver{}
			runner := &recordingRepositoryRsyncRestoreRunner{}
			writer := &recordingRepositoryRsyncTargetWriter{}
			result, err := NewRsyncRestorePort(resolver, writer, runner).Execute(context.Background(), request, provider.RestoreProgress{})
			assertRepositoryRsyncPortRejectedBeforeEffects(t, result, err, resolver, runner, writer)
		})
	}
}

func TestRsyncRestorePortRejectsObservationPermitSubstitutionBeforeEffects(t *testing.T) {
	for _, phase := range repositoryRsyncTestPhases() {
		if phase.name == "execute" {
			continue
		}
		for _, scenario := range []string{"missing", "expired", "other target"} {
			t.Run(phase.name+"/"+scenario, func(t *testing.T) {
				request := validRepositoryRsyncRestoreRequest(t)
				now := time.Now().UTC()
				var permits repositoryRsyncTestPermits
				switch scenario {
				case "expired":
					permits = newRepositoryRsyncTestPermits(t, request.Target, now.Add(-2*time.Minute), now.Add(-time.Minute))
				case "other target":
					other, err := provider.NewRestoreTarget(request.Target.NodeID, "other-approved-root", request.Target.RootLocatorDigest, request.Target.TargetPathDigest, request.Target.RootRevision, request.Target.TargetRevision)
					if err != nil {
						t.Fatal(err)
					}
					permits = newRepositoryRsyncTestPermits(t, other, now, now.Add(time.Minute))
				}
				resolver := &recordingRepositoryRsyncRestoreSourceResolver{}
				runner := &recordingRepositoryRsyncRestoreRunner{}
				writer := &recordingRepositoryRsyncTargetWriter{}
				result, err := phase.invoke(NewRsyncRestorePort(resolver, writer, runner), request, permits)
				assertRepositoryRsyncPortRejectedBeforeEffects(t, result, err, resolver, runner, writer)
			})
		}
	}
}

func TestRsyncRestorePortRejectsInvalidRunnerEvidence(t *testing.T) {
	processEvidence, err := provider.NewRestoreEvidence(provider.RestoreProcessEvidenceInput{
		Stdout: []byte("FAKE_PROVIDER_STDOUT_FOR_TEST_ONLY"),
		Stderr: []byte("FAKE_PROVIDER_STDERR_FOR_TEST_ONLY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	type evidenceCase struct {
		name   string
		mutate func(*provider.RsyncRestoreRunnerEvidence)
	}
	for _, phase := range repositoryRsyncTestPhases() {
		tests := []evidenceCase{
			{name: "missing verified target identity", mutate: func(result *provider.RsyncRestoreRunnerEvidence) { result.Checkpoint.VerifiedTargetIdentityDigest = "" }},
			{name: "process evidence cannot replace target verification", mutate: func(result *provider.RsyncRestoreRunnerEvidence) {
				result.Checkpoint.VerifiedTargetIdentityDigest = ""
				result.Evidence = []provider.RestoreEvidence{processEvidence}
			}},
			{name: "wrong checkpoint prior revision", mutate: func(result *provider.RsyncRestoreRunnerEvidence) {
				result.Checkpoint.PriorTargetRevision = "other-target-revision"
			}},
			{name: "wrong checkpoint attempt fence", mutate: func(result *provider.RsyncRestoreRunnerEvidence) { result.Checkpoint.AttemptFence++ }},
			{name: "wrong checkpoint node fence", mutate: func(result *provider.RsyncRestoreRunnerEvidence) { result.Checkpoint.NodeFence++ }},
			{name: "negative verified bytes", mutate: func(result *provider.RsyncRestoreRunnerEvidence) { result.Checkpoint.VerifiedBytes = -1 }},
			{name: "invalid process evidence digest", mutate: func(result *provider.RsyncRestoreRunnerEvidence) {
				result.Evidence = []provider.RestoreEvidence{{OutputDigest: "", OutputBytes: 1}}
			}},
			{name: "invalid process evidence size", mutate: func(result *provider.RsyncRestoreRunnerEvidence) {
				result.Evidence = []provider.RestoreEvidence{{OutputDigest: strings.Repeat("a", 64), OutputBytes: -1}}
			}},
		}
		if phase.name != "execute" {
			tests = append(tests,
				evidenceCase{name: "other target binding", mutate: func(result *provider.RsyncRestoreRunnerEvidence) {
					result.TargetBindingDigest = strings.Repeat("e", 64)
				}},
				evidenceCase{name: "other target revision", mutate: func(result *provider.RsyncRestoreRunnerEvidence) { result.TargetRevision = "other-target-revision" }},
				evidenceCase{name: "different valid checkpoint", mutate: func(result *provider.RsyncRestoreRunnerEvidence) { result.Checkpoint.ID = strings.Repeat("e", 32) }},
			)
		}
		for _, test := range tests {
			t.Run(phase.name+"/"+test.name, func(t *testing.T) {
				request := validRepositoryRsyncRestoreRequest(t)
				now := time.Now().UTC()
				permits := newRepositoryRsyncTestPermits(t, request.Target, now, now.Add(time.Minute))
				runnerEvidence := provider.RsyncRestoreRunnerEvidence{TargetBindingDigest: request.Target.BindingDigest, TargetRevision: request.Target.TargetRevision, Checkpoint: request.Checkpoint}
				test.mutate(&runnerEvidence)
				resolver := &recordingRepositoryRsyncRestoreSourceResolver{}
				runner := &recordingRepositoryRsyncRestoreRunner{result: &runnerEvidence}
				writer := &recordingRepositoryRsyncTargetWriter{}
				result, err := phase.invoke(NewRsyncRestorePort(resolver, writer, runner), request, permits)
				if !errors.Is(err, provider.ErrInvalidRestoreRequest) || !reflect.ValueOf(result).IsZero() {
					t.Fatalf("invalid runner evidence result=%#v err=%v, want zero result and invalid restore request", result, err)
				}
				if len(resolver.refs) != 1 || phase.callCount(runner) != 1 || len(resolver.trees) != 1 || resolver.trees[0].closes != 1 || writer.calls != 0 {
					t.Fatalf("invalid result resolver=%d runner=%d trees=%d writer=%d", len(resolver.refs), phase.callCount(runner), len(resolver.trees), writer.calls)
				}
			})
		}
	}
}

func TestRsyncRestorePortRejectsSourceCloseFailure(t *testing.T) {
	const privateFailure = "FAKE_RSYNC_SOURCE_CLOSE_FAILURE_FOR_TEST_ONLY"
	for _, phase := range repositoryRsyncTestPhases() {
		t.Run(phase.name, func(t *testing.T) {
			request := validRepositoryRsyncRestoreRequest(t)
			now := time.Now().UTC()
			permits := newRepositoryRsyncTestPermits(t, request.Target, now, now.Add(time.Minute))
			resolver := &recordingRepositoryRsyncRestoreSourceResolver{closeErr: errors.New(privateFailure)}
			runner := &recordingRepositoryRsyncRestoreRunner{}
			writer := &recordingRepositoryRsyncTargetWriter{}
			result, err := phase.invoke(NewRsyncRestorePort(resolver, writer, runner), request, permits)
			if !errors.Is(err, provider.ErrRsyncRestoreUnavailable) || strings.Contains(fmt.Sprint(err), privateFailure) || !reflect.ValueOf(result).IsZero() {
				t.Fatalf("source close failure result=%#v err=%v, want sanitized unavailable and zero result", result, err)
			}
			if len(resolver.refs) != 1 || phase.callCount(runner) != 1 || len(resolver.trees) != 1 || resolver.trees[0].closes != 1 || resolver.trees[0].revalidations != 2 {
				t.Fatalf("source close failure resolver=%d runner=%d trees=%d", len(resolver.refs), phase.callCount(runner), len(resolver.trees))
			}
		})
	}
}

func TestRsyncRestorePortRejectsRequestEntrySetMismatchBeforeRunner(t *testing.T) {
	request := validRepositoryRsyncRestoreRequest(t)
	extra := request.Entries[0]
	extra.AssetRef.EntryID = strings.Repeat("e", 64)
	resolver := &recordingRepositoryRsyncRestoreSourceResolver{
		declared: []provider.RestoreEntry{request.Entries[0], extra},
	}
	runner := &recordingRepositoryRsyncRestoreRunner{}

	_, err := NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, runner).
		Execute(context.Background(), request, provider.RestoreProgress{})

	if !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("request entry-set mismatch error=%v, want typed source drift", err)
	}
	if len(runner.executes) != 0 {
		t.Fatalf("request entry-set mismatch reached runner %d time(s)", len(runner.executes))
	}
	if len(resolver.trees) != 1 || resolver.trees[0].entryValidations != 1 {
		t.Fatal("request entry set was not validated exactly once before runner dispatch")
	}
}

func TestRsyncRestorePortRejectsPostPhaseDurableAndMarkerDrift(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *rsyncRestoreResolverFixture)
	}{
		{
			name: "durable plan",
			mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).
					Where("id = ?", fixture.ref.PlanID).
					Update("selection_digest", strings.Repeat("e", 64)).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "authenticated commit marker",
			mutate: func(t *testing.T, fixture *rsyncRestoreResolverFixture) {
				t.Helper()
				marker := filepath.Join(fixture.root, "points", fixture.ref.RecoveryPointID, "commit.json")
				if err := os.WriteFile(marker, []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRsyncRestoreResolverFixture(t)
			request := validRepositoryRsyncRestoreRequest(t)
			request.Rsync = &provider.RsyncRestoreRequest{ManifestDigest: fixture.ref.ManifestDigest, SourceRef: fixture.ref}
			request.Entries = []provider.RestoreEntry{fixture.entry}
			runner := &recordingRepositoryRsyncRestoreRunner{executeHook: func() { test.mutate(t, fixture) }}

			_, err := NewRsyncRestorePort(fixture.service, &recordingRepositoryRsyncTargetWriter{}, runner).
				Execute(context.Background(), request, provider.RestoreProgress{})

			if !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				t.Fatalf("post-phase source drift error=%v, want typed source drift", err)
			}
			if len(runner.executes) != 1 {
				t.Fatalf("runner calls = %d, want one before post-phase rejection", len(runner.executes))
			}
		})
	}
}

func TestRsyncRestorePortRejectsSourceOrRootSwapBeforeRunner(t *testing.T) {
	for _, phase := range []struct {
		name   string
		invoke func(*RsyncRestorePort, provider.RestoreRequest) error
		calls  func(*recordingRepositoryRsyncRestoreRunner) int
	}{
		{
			name: "preflight",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetPreflightPermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("a", 32), Purpose: provider.TargetPurposePreflight, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Preflight(context.Background(), provider.RestorePreflightRequest{Request: request, Permit: permit})
				return err
			},
			calls: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.preflights) },
		},
		{
			name: "execute",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				_, err := port.Execute(context.Background(), request, provider.RestoreProgress{})
				return err
			},
			calls: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.executes) },
		},
		{
			name: "verify",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetVerifyPermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("b", 32), Purpose: provider.TargetPurposeVerify, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Verify(context.Background(), provider.RestoreVerifyRequest{Request: request, Permit: permit})
				return err
			},
			calls: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.verifies) },
		},
		{
			name: "reconcile",
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetReconcilePermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("c", 32), Purpose: provider.TargetPurposeReconcile, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Reconcile(context.Background(), provider.RestoreReconcileRequest{Request: request, Permit: permit})
				return err
			},
			calls: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.reconciles) },
		},
	} {
		t.Run(phase.name, func(t *testing.T) {
			resolver := &recordingRepositoryRsyncRestoreSourceResolver{root: t.TempDir(), revalidateErr: fileaccess.ErrSourceChanged}
			runner := &recordingRepositoryRsyncRestoreRunner{}
			err := phase.invoke(NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, runner), validRepositoryRsyncRestoreRequest(t))
			if !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) || !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				t.Fatalf("source/root swap error=%v, want typed source drift", err)
			}
			if calls := phase.calls(runner); calls != 0 {
				t.Fatalf("source/root swap reached %s runner %d time(s)", phase.name, calls)
			}
		})
	}
}

func TestRsyncRestorePortRevalidatesAfterRunnerError(t *testing.T) {
	const privateFailure = "FAKE_RSYNC_PRIVATE_RUNNER_FAILURE_FOR_TEST_ONLY"
	for _, phase := range []struct {
		name      string
		setError  func(*recordingRepositoryRsyncRestoreRunner, error)
		invoke    func(*RsyncRestorePort, provider.RestoreRequest) error
		callCount func(*recordingRepositoryRsyncRestoreRunner) int
	}{
		{
			name:     "preflight",
			setError: func(runner *recordingRepositoryRsyncRestoreRunner, err error) { runner.preflightErr = err },
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetPreflightPermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("a", 32), Purpose: provider.TargetPurposePreflight, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Preflight(context.Background(), provider.RestorePreflightRequest{Request: request, Permit: permit})
				return err
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.preflights) },
		},
		{
			name:     "execute",
			setError: func(runner *recordingRepositoryRsyncRestoreRunner, err error) { runner.executeErr = err },
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				_, err := port.Execute(context.Background(), request, provider.RestoreProgress{})
				return err
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.executes) },
		},
		{
			name:     "verify",
			setError: func(runner *recordingRepositoryRsyncRestoreRunner, err error) { runner.verifyErr = err },
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetVerifyPermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("b", 32), Purpose: provider.TargetPurposeVerify, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Verify(context.Background(), provider.RestoreVerifyRequest{Request: request, Permit: permit})
				return err
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.verifies) },
		},
		{
			name:     "reconcile",
			setError: func(runner *recordingRepositoryRsyncRestoreRunner, err error) { runner.reconcileErr = err },
			invoke: func(port *RsyncRestorePort, request provider.RestoreRequest) error {
				now := time.Now().UTC()
				permit, err := provider.NewTargetReconcilePermit(provider.TargetObservationPermit{TargetBindingDigest: request.Target.BindingDigest, Session: provider.TargetSession{ID: strings.Repeat("c", 32), Purpose: provider.TargetPurposeReconcile, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}}, request.Target, now)
				if err != nil {
					return err
				}
				_, err = port.Reconcile(context.Background(), provider.RestoreReconcileRequest{Request: request, Permit: permit})
				return err
			},
			callCount: func(runner *recordingRepositoryRsyncRestoreRunner) int { return len(runner.reconciles) },
		},
	} {
		for _, failure := range []struct {
			name      string
			runnerErr error
			want      error
		}{
			{name: "ordinary", runnerErr: errors.New(privateFailure), want: provider.ErrRsyncRestoreSourceDrift},
			{name: "canceled", runnerErr: fmt.Errorf("%s: %w", privateFailure, context.Canceled), want: context.Canceled},
			{name: "deadline", runnerErr: fmt.Errorf("%s: %w", privateFailure, context.DeadlineExceeded), want: context.DeadlineExceeded},
		} {
			t.Run(phase.name+"/"+failure.name, func(t *testing.T) {
				resolver := &recordingRepositoryRsyncRestoreSourceResolver{
					revalidateErr:   fileaccess.ErrSourceChanged,
					revalidateErrAt: 2,
				}
				runner := &recordingRepositoryRsyncRestoreRunner{}
				phase.setError(runner, failure.runnerErr)

				err := phase.invoke(NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, runner), validRepositoryRsyncRestoreRequest(t))

				if !errors.Is(err, failure.want) || strings.Contains(err.Error(), privateFailure) {
					t.Fatalf("post-runner error=%v, want sanitized %v identity", err, failure.want)
				}
				if calls := phase.callCount(runner); calls != 1 {
					t.Fatalf("%s runner calls=%d, want one", phase.name, calls)
				}
				if len(resolver.trees) != 1 {
					t.Fatalf("source trees=%d, want one", len(resolver.trees))
				}
				tree := resolver.trees[0]
				if tree.revalidations != 2 || tree.closes != 1 {
					t.Fatalf("source revalidations/closes=%d/%d, want 2/1", tree.revalidations, tree.closes)
				}
			})
		}
	}
}

func TestRsyncRestorePortSanitizesResolverAndRunnerErrors(t *testing.T) {
	const privateFailure = "FAKE_RSYNC_PRIVATE_PATH_TOKEN_FOR_TEST_ONLY"
	request := validRepositoryRsyncRestoreRequest(t)
	resolver := &recordingRepositoryRsyncRestoreSourceResolver{root: t.TempDir(), resolveErr: errors.New(privateFailure)}
	if _, err := NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, &recordingRepositoryRsyncRestoreRunner{}).Execute(context.Background(), request, provider.RestoreProgress{}); !errors.Is(err, provider.ErrRsyncRestoreUnavailable) || strings.Contains(err.Error(), privateFailure) {
		t.Fatalf("resolver error=%v, want sanitized unavailable", err)
	}
	resolver = &recordingRepositoryRsyncRestoreSourceResolver{root: t.TempDir()}
	runner := &recordingRepositoryRsyncRestoreRunner{executeErr: errors.New(privateFailure)}
	if _, err := NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, runner).Execute(context.Background(), request, provider.RestoreProgress{}); !errors.Is(err, provider.ErrRsyncRestoreUnavailable) || strings.Contains(err.Error(), privateFailure) {
		t.Fatalf("runner error=%v, want sanitized unavailable", err)
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(sentinel.Error()+" from resolver", func(t *testing.T) {
			resolver := &recordingRepositoryRsyncRestoreSourceResolver{resolveErr: fmt.Errorf("%s: %w", privateFailure, sentinel)}
			_, err := NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, &recordingRepositoryRsyncRestoreRunner{}).
				Execute(context.Background(), request, provider.RestoreProgress{})
			if !errors.Is(err, sentinel) || strings.Contains(err.Error(), privateFailure) {
				t.Fatalf("resolver context error=%v, want sanitized %v identity", err, sentinel)
			}
		})
		t.Run(sentinel.Error()+" from runner", func(t *testing.T) {
			resolver := &recordingRepositoryRsyncRestoreSourceResolver{}
			runner := &recordingRepositoryRsyncRestoreRunner{executeErr: fmt.Errorf("%s: %w", privateFailure, sentinel)}
			_, err := NewRsyncRestorePort(resolver, &recordingRepositoryRsyncTargetWriter{}, runner).
				Execute(context.Background(), request, provider.RestoreProgress{})
			if !errors.Is(err, sentinel) || strings.Contains(err.Error(), privateFailure) {
				t.Fatalf("runner context error=%v, want sanitized %v identity", err, sentinel)
			}
		})
	}
}

func validRepositoryRsyncRestoreRequest(t *testing.T) provider.RestoreRequest {
	t.Helper()
	now := time.Now().UTC()
	target, err := provider.NewRestoreTarget(7, "approved-root", strings.Repeat("3", 64), strings.Repeat("4", 64), "root-revision-1", "target-revision-1")
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.RsyncRestoreSourceRef{
		PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64), RepositoryID: strings.Repeat("3", 32),
		RecoveryPointID: strings.Repeat("4", 32), CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
		SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
	}
	fence := provider.RestoreFence{JobID: strings.Repeat("9", 32), AttemptID: strings.Repeat("a", 32), NodeLeaseID: strings.Repeat("b", 32), AttemptFence: 11, NodeFence: 13, ExpectedTargetRevision: target.TargetRevision}
	checkpoint := provider.RestoreCheckpoint{ID: strings.Repeat("c", 32), OperationDigest: strings.Repeat("d", 64), PriorTargetRevision: target.TargetRevision, VerifiedTargetIdentityDigest: strings.Repeat("e", 64), VerifiedTargetRevision: target.TargetRevision, VerifiedBytes: 17, AttemptFence: fence.AttemptFence, NodeFence: fence.NodeFence}
	return provider.RestoreRequest{
		Version: provider.RestoreRequestSchemaV1, Provider: backupasset.ProviderRsync,
		Entries: []provider.RestoreEntry{{AssetRef: backupasset.AssetRef{RecoveryPointID: ref.RecoveryPointID, EntryID: strings.Repeat("f", 64)}, Type: backupasset.CatalogEntryFile, ExpectedSize: 17, TargetObjectDigest: strings.Repeat("b", 64)}},
		Target:  target, Limits: provider.RestoreLimits{MaxEntries: 2, MaxBytes: 1024, MaxEntryBytes: 1024}, ConflictPolicy: provider.RestoreConflictFailOnConflict,
		Fence: fence, Checkpoint: checkpoint,
		MutationPermit: provider.TargetMutationPermit{TargetBindingDigest: target.BindingDigest, UseLatchID: provider.RestoreSchemaUseLatchID, JobID: fence.JobID, AttemptID: fence.AttemptID, NodeLeaseID: fence.NodeLeaseID, AttemptFence: fence.AttemptFence, NodeFence: fence.NodeFence, ExpectedTargetRevision: target.TargetRevision, Session: provider.TargetSession{ID: strings.Repeat("d", 32), Purpose: provider.TargetPurposeWrite, CredentialRevision: "credential-revision-1", ExpiresAt: now.Add(time.Minute)}},
		Rsync:          &provider.RsyncRestoreRequest{ManifestDigest: ref.ManifestDigest, SourceRef: ref},
	}
}

type recordingRepositoryRsyncRestoreSourceResolver struct {
	root            string
	refs            []provider.RsyncRestoreSourceRef
	trees           []*recordingRepositoryPinnedStrictTree
	declared        []provider.RestoreEntry
	resolveErr      error
	revalidateErr   error
	closeErr        error
	revalidateErrAt int
}

func (resolver *recordingRepositoryRsyncRestoreSourceResolver) ResolveRsyncRestoreSource(_ context.Context, ref provider.RsyncRestoreSourceRef) (provider.RsyncRestoreSource, error) {
	resolver.refs = append(resolver.refs, ref)
	if resolver.resolveErr != nil {
		return nil, resolver.resolveErr
	}
	recording := &recordingRepositoryPinnedStrictTree{
		declared: append([]provider.RestoreEntry(nil), resolver.declared...), revalidateErr: resolver.revalidateErr,
		revalidateErrAt: resolver.revalidateErrAt, closeErr: resolver.closeErr,
	}
	resolver.trees = append(resolver.trees, recording)
	return recording, nil
}

type recordingRepositoryPinnedStrictTree struct {
	revalidations    int
	entryValidations int
	closes           int
	declared         []provider.RestoreEntry
	closeErr         error
	revalidateErr    error
	revalidateErrAt  int
}

func (*recordingRepositoryPinnedStrictTree) OpenDeclaredRegular(context.Context, provider.RestoreEntry) (provider.RsyncRestoreSourceStream, error) {
	return nil, provider.ErrRsyncRestoreSourceDrift
}

func (tree *recordingRepositoryPinnedStrictTree) MaterializeDeclaredEntries(_ context.Context, entries []provider.RestoreEntry) ([]provider.RestoreEntry, error) {
	tree.entryValidations++
	for _, entry := range entries {
		if entry.ExpectedDigest != "" {
			return nil, provider.ErrRsyncRestoreSourceDrift
		}
	}
	if len(tree.declared) == 0 {
		materialized := append([]provider.RestoreEntry(nil), entries...)
		for index := range materialized {
			materialized[index].ExpectedDigest = strings.Repeat("a", 64)
		}
		return materialized, nil
	}
	declaredFacts := append([]provider.RestoreEntry(nil), tree.declared...)
	for index := range declaredFacts {
		declaredFacts[index].ExpectedDigest = ""
	}
	if !reflect.DeepEqual(declaredFacts, entries) {
		return nil, provider.ErrRsyncRestoreSourceDrift
	}
	materialized := append([]provider.RestoreEntry(nil), tree.declared...)
	for index := range materialized {
		if materialized[index].ExpectedDigest == "" {
			materialized[index].ExpectedDigest = strings.Repeat("a", 64)
		}
	}
	return materialized, nil
}

func (tree *recordingRepositoryPinnedStrictTree) Revalidate(ctx context.Context) error {
	tree.revalidations++
	if tree.revalidateErr != nil && (tree.revalidateErrAt == 0 || tree.revalidations == tree.revalidateErrAt) {
		return tree.revalidateErr
	}
	return nil
}

func (tree *recordingRepositoryPinnedStrictTree) Close() error {
	tree.closes++
	return tree.closeErr
}

type recordingRepositoryRsyncTargetWriter struct{ calls int }

func (writer *recordingRepositoryRsyncTargetWriter) WriteDeclaredRegular(context.Context, provider.RsyncTargetWriteCall) error {
	writer.calls++
	return nil
}

type recordingRepositoryRsyncRestoreRunner struct {
	preflights   []provider.RsyncRestorePreflightCall
	executes     []provider.RsyncRestoreExecuteCall
	verifies     []provider.RsyncRestoreVerifyCall
	reconciles   []provider.RsyncRestoreReconcileCall
	preflightErr error
	executeErr   error
	verifyErr    error
	reconcileErr error
	executeHook  func()
	result       *provider.RsyncRestoreRunnerEvidence
}

func (runner *recordingRepositoryRsyncRestoreRunner) Preflight(_ context.Context, call provider.RsyncRestorePreflightCall) (provider.RsyncRestoreRunnerEvidence, error) {
	if call.Source == nil {
		return provider.RsyncRestoreRunnerEvidence{}, errors.New("pinned source was not provided")
	}
	runner.preflights = append(runner.preflights, call)
	if runner.preflightErr != nil {
		return provider.RsyncRestoreRunnerEvidence{}, runner.preflightErr
	}
	if runner.result != nil {
		return *runner.result, nil
	}
	return provider.RsyncRestoreRunnerEvidence{TargetBindingDigest: call.Target.TargetBindingDigest, TargetRevision: call.Target.TargetRevision, Checkpoint: call.Checkpoint}, nil
}

func (runner *recordingRepositoryRsyncRestoreRunner) Execute(_ context.Context, call provider.RsyncRestoreExecuteCall) (provider.RsyncRestoreRunnerResult, error) {
	if call.Source == nil {
		return provider.RsyncRestoreRunnerResult{}, errors.New("pinned source was not provided")
	}
	runner.executes = append(runner.executes, call)
	if runner.executeHook != nil {
		runner.executeHook()
	}
	if runner.executeErr != nil {
		return provider.RsyncRestoreRunnerResult{}, runner.executeErr
	}
	if runner.result != nil {
		return *runner.result, nil
	}
	return provider.RsyncRestoreRunnerResult{Checkpoint: provider.RestoreCheckpoint{ID: strings.Repeat("e", 32), OperationDigest: strings.Repeat("f", 64), PriorTargetRevision: call.Checkpoint.PriorTargetRevision, VerifiedTargetIdentityDigest: strings.Repeat("a", 64), VerifiedTargetRevision: "target-revision-next", VerifiedBytes: 17, AttemptFence: call.Fence.AttemptFence, NodeFence: call.Fence.NodeFence}}, nil
}

func (runner *recordingRepositoryRsyncRestoreRunner) Verify(_ context.Context, call provider.RsyncRestoreVerifyCall) (provider.RsyncRestoreRunnerEvidence, error) {
	if call.Source == nil {
		return provider.RsyncRestoreRunnerEvidence{}, errors.New("pinned source was not provided")
	}
	runner.verifies = append(runner.verifies, call)
	if runner.verifyErr != nil {
		return provider.RsyncRestoreRunnerEvidence{}, runner.verifyErr
	}
	if runner.result != nil {
		return *runner.result, nil
	}
	return provider.RsyncRestoreRunnerEvidence{TargetBindingDigest: call.Target.TargetBindingDigest, TargetRevision: call.Target.TargetRevision, Checkpoint: call.Checkpoint}, nil
}

func (runner *recordingRepositoryRsyncRestoreRunner) Reconcile(_ context.Context, call provider.RsyncRestoreReconcileCall) (provider.RsyncRestoreRunnerEvidence, error) {
	if call.Source == nil {
		return provider.RsyncRestoreRunnerEvidence{}, errors.New("pinned source was not provided")
	}
	runner.reconciles = append(runner.reconciles, call)
	if runner.reconcileErr != nil {
		return provider.RsyncRestoreRunnerEvidence{}, runner.reconcileErr
	}
	if runner.result != nil {
		return *runner.result, nil
	}
	return provider.RsyncRestoreRunnerEvidence{TargetBindingDigest: call.Target.TargetBindingDigest, TargetRevision: call.Target.TargetRevision, Checkpoint: call.Checkpoint}, nil
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
	request, access, err := service.managedRsyncCommittedPointReadRequest(context.Background(), fixture.db, runtime, point)
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
	driftedRuntime := runtime
	driftedRuntime.repository.CapabilityRevision++
	serviceWithoutKeyring, err := NewService(Dependencies{
		DB: fixture.db, Foundation: fixture.service.foundation, Registry: fixture.service.registry,
		Now: func() time.Time { return fixture.now }, Admission: fixture.admission, History: fixture.service.history,
		Metrics: publication.NoopMetrics{},
	})
	if err != nil {
		t.Fatal(err)
	}
	driftedRequest, driftedAccess, driftErr := serviceWithoutKeyring.managedRsyncCommittedPointReadRequest(
		context.Background(), fixture.db, driftedRuntime, point,
	)
	if !errors.Is(driftErr, backupasset.ErrConflict) {
		t.Fatalf("capability-drift committed Rsync reader error=%v, want ErrConflict", driftErr)
	}
	if !strings.Contains(driftErr.Error(), "committed Rsync point capability revision changed") {
		t.Fatalf("capability-drift committed Rsync reader error=%v, want capability revision message", driftErr)
	}
	if !reflect.DeepEqual(driftedRequest, provider.RsyncCommittedPointReadRequest{}) ||
		!reflect.DeepEqual(driftedAccess, provider.AccessBinding{}) {
		t.Fatalf("capability-drift committed Rsync reader returned request=%+v access=%+v, want zero values", driftedRequest, driftedAccess)
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
	if _, _, err := service.managedRsyncCommittedPointReadRequest(context.Background(), fixture.db, runtime, point); !errors.Is(err, backupasset.ErrInvalidState) {
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
