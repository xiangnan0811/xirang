package repository

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	postgresgorm "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type importPointListerSpy struct {
	page     provider.NativePointPage
	calls    int
	snapshot provider.ReadSnapshot
	request  provider.PageRequest
}

type managedManifestProofVerifierSpy struct {
	requests []ManagedManifestProofRequest
	err      error
	errFor   map[string]error
}

func migrateImportCandidateTestTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.BackupRepositoryImportCandidate{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_repository_import_candidates_source
		ON backup_repository_import_candidates(repository_id, source_fingerprint)`).Error; err != nil {
		t.Fatal(err)
	}
}

func (spy *managedManifestProofVerifierSpy) VerifyManagedManifest(_ context.Context, request ManagedManifestProofRequest) error {
	request.CommitDigests = append([]string(nil), request.CommitDigests...)
	spy.requests = append(spy.requests, request)
	if spy.errFor != nil {
		if err, ok := spy.errFor[request.CandidateDigest]; ok {
			return err
		}
	}
	return spy.err
}

func (spy *importPointListerSpy) ListPoints(_ context.Context, snapshot provider.ReadSnapshot, request provider.PageRequest) (provider.NativePointPage, error) {
	spy.calls++
	spy.snapshot = snapshot
	spy.request = request
	return spy.page, nil
}

func TestImportDiscoveryPersistsEncryptedKeyedCandidateIdempotently(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repository", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	nativeRepositoryID := strings.Repeat("1", 64)
	nativeSnapshotID := strings.Repeat("2", 64)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, provider.NativeResticIdentityPrefix+nativeRepositoryID)}
	lister := &importPointListerSpy{page: provider.NativePointPage{Items: []provider.NativePoint{{
		OpaqueDigest:   nativeSnapshotID,
		CapturedAt:     time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC),
		Semantics:      backupasset.PointNativeSnapshot,
		SourceRevision: nativeSnapshotID,
		Locator:        provider.PointLocator{Native: nativeSnapshotID},
	}}}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: prober, PointLister: lister}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry,
		Now: func() time.Time { return time.Date(2026, 8, 17, 3, 5, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := RequestContext{Actor: backupasset.AuditActor{UserID: 1, Username: "import-admin", Role: "admin"}}

	first, err := service.DiscoverImportCandidates(context.Background(), connected.Repository.ID, ImportDiscoveryRequest{Limit: 10}, adminContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DiscoverImportCandidates(context.Background(), connected.Repository.ID, ImportDiscoveryRequest{Limit: 10},
		RequestContext{Actor: backupasset.AuditActor{UserID: 2, Username: "import-operator", Role: "operator"}}); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("operator discovered pending import candidates: %v", err)
	}
	second, err := service.DiscoverImportCandidates(context.Background(), connected.Repository.ID, ImportDiscoveryRequest{Limit: 10}, adminContext)
	if err != nil {
		t.Fatal(err)
	}
	if first.Discovered != 1 || first.Existing != 0 || len(first.Candidates) != 1 || first.NextCursor != "" ||
		second.Discovered != 0 || second.Existing != 1 || len(second.Candidates) != 1 || second.Candidates[0].ID != first.Candidates[0].ID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	candidate := first.Candidates[0]
	if candidate.RepositoryID != connected.Repository.ID || candidate.Kind != backupasset.ImportCandidateNativeSnapshot ||
		candidate.State != backupasset.ImportReviewPending || candidate.AcceptedRecoveryPointID != "" {
		t.Fatalf("candidate=%+v", candidate)
	}
	if lister.calls != 2 || lister.request.Limit != 10 || lister.request.Cursor != "" ||
		lister.snapshot.RepositoryID != connected.Repository.ID || lister.snapshot.CapabilityRevision != connected.Repository.CapabilityRevision ||
		lister.snapshot.SourceRevision != prober.observation.SourceRevision || lister.snapshot.Access.RepositoryID != connected.Repository.ID {
		t.Fatalf("lister calls=%d snapshot=%+v request=%+v", lister.calls, lister.snapshot, lister.request)
	}

	var row struct {
		SourceFingerprint        string
		EncryptedProviderLocator string
		EncryptedEvidence        string
	}
	if err := db.Raw(`SELECT source_fingerprint, encrypted_provider_locator, encrypted_evidence
		FROM backup_repository_import_candidates WHERE id = ?`, candidate.ID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if !isLowerHex64(row.SourceFingerprint) || row.SourceFingerprint == nativeSnapshotID ||
		!strings.HasPrefix(row.EncryptedProviderLocator, "enc:v2:") || !strings.HasPrefix(row.EncryptedEvidence, "enc:v2:") ||
		strings.Contains(row.EncryptedProviderLocator, nativeSnapshotID) || strings.Contains(row.EncryptedEvidence, nativeSnapshotID) {
		t.Fatalf("unsafe persisted import candidate: %+v", row)
	}
	payload, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{nativeRepositoryID, nativeSnapshotID, "provider_locator", "source_fingerprint", "encrypted_evidence"} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("import discovery result leaked %q: %s", secret, payload)
		}
	}
}

func TestImportResticTrustedCandidatesRequireFullNativeSnapshotIdentity(t *testing.T) {
	repositoryID := strings.Repeat("1", 32)
	salt := []byte(strings.Repeat("s", provider.IdentitySaltBytes))
	validSnapshotID := strings.Repeat("a", 64)
	for _, testCase := range []struct {
		name      string
		semantics backupasset.PointVersionSemantics
		revision  string
		locator   string
		wantValid bool
	}{
		{name: "native valid", semantics: backupasset.PointNativeSnapshot, revision: validSnapshotID, locator: validSnapshotID, wantValid: true},
		{name: "baseline valid", semantics: backupasset.PointImportedBaseline, revision: validSnapshotID, locator: validSnapshotID, wantValid: true},
		{name: "baseline arbitrary", semantics: backupasset.PointImportedBaseline, revision: "arbitrary-baseline", locator: "arbitrary-baseline"},
		{name: "baseline uppercase", semantics: backupasset.PointImportedBaseline, revision: strings.Repeat("A", 64), locator: strings.Repeat("A", 64)},
		{name: "baseline short", semantics: backupasset.PointImportedBaseline, revision: strings.Repeat("b", 63), locator: strings.Repeat("b", 63)},
		{name: "baseline long", semantics: backupasset.PointImportedBaseline, revision: strings.Repeat("b", 65), locator: strings.Repeat("b", 65)},
		{name: "baseline locator mismatch", semantics: backupasset.PointImportedBaseline, revision: validSnapshotID, locator: strings.Repeat("b", 64)},
		{name: "native uppercase", semantics: backupasset.PointNativeSnapshot, revision: strings.Repeat("C", 64), locator: strings.Repeat("C", 64)},
	} {
		t.Run("discovery/"+testCase.name, func(t *testing.T) {
			_, err := normalizeImportCandidate(backupasset.ProviderRestic, repositoryID, salt, provider.NativePoint{
				OpaqueDigest: strings.Repeat("d", 64), CapturedAt: time.Date(2026, 8, 17, 9, 50, 0, 0, time.UTC),
				Semantics: testCase.semantics, SourceRevision: testCase.revision, Locator: provider.PointLocator{Native: testCase.locator},
			}, nil)
			if testCase.wantValid && err != nil {
				t.Fatalf("valid Restic snapshot rejected: %v", err)
			}
			if !testCase.wantValid && !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("invalid Restic snapshot identity admitted: %v", err)
			}
		})
	}

	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	now := time.Date(2026, 8, 17, 9, 51, 0, 0, time.UTC)
	admin := model.User{Username: "restic-identity-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("e", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "restic identity review", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	seedCandidate := func(id, fingerprint, snapshotID string) model.BackupRepositoryImportCandidate {
		locator, _ := json.Marshal(importCandidateLocator{Version: 1, Native: snapshotID})
		evidence, _ := json.Marshal(importCandidateEvidence{
			Version: 1, Provider: backupasset.ProviderRestic, OpaqueDigest: strings.Repeat("f", 64), CapturedAt: now.Add(-time.Hour),
			Semantics: backupasset.PointImportedBaseline, SourceRevision: snapshotID,
		})
		candidate := model.BackupRepositoryImportCandidate{
			ID: id, RepositoryID: repositoryID, CandidateKind: string(backupasset.ImportCandidateImportedBaseline), SourceFingerprint: fingerprint,
			EncryptedProviderLocator: string(locator), EncryptedEvidence: string(evidence), ReviewState: string(backupasset.ImportReviewPending),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&candidate).Error; err != nil {
			t.Fatal(err)
		}
		return candidate
	}
	invalid := seedCandidate(strings.Repeat("2", 32), strings.Repeat("3", 64), "arbitrary-baseline")
	valid := seedCandidate(strings.Repeat("4", 32), strings.Repeat("5", 64), validSnapshotID)
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := RequestContext{Actor: backupasset.AuditActor{UserID: admin.ID, Username: admin.Username, Role: "admin"}}
	if _, err := service.ReviewImportCandidate(context.Background(), repositoryID, invalid.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewAccepted}, adminContext); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("persisted arbitrary Restic baseline accepted: %v", err)
	}
	if _, err := service.ReviewImportCandidate(context.Background(), repositoryID, valid.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewAccepted}, adminContext); err != nil {
		t.Fatalf("valid persisted Restic baseline rejected: %v", err)
	}
	var points int64
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ?", repositoryID).Count(&points).Error; err != nil || points != 1 {
		t.Fatalf("trusted Restic point count=%d err=%v", points, err)
	}
}

func TestImportConcurrentDiscoveryUniqueCollisionRequiresExactWinner(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		mismatch     bool
		wantExact    bool
		wantConflict bool
	}{
		{name: "exact winner", wantExact: true},
		{name: "private evidence mismatch", mismatch: true, wantConflict: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newRepositoryTestDB(t)
			migrateImportCandidateTestTable(t, db)
			now := time.Date(2026, 8, 17, 9, 57, 0, 0, time.UTC)
			repositoryID := strings.Repeat("7", 32)
			repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
			repository := model.BackupRepository{
				ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
				DisplayName: "discovery race", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
				CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
				CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&repository).Error; err != nil {
				t.Fatal(err)
			}
			snapshotID := strings.Repeat("a", 64)
			locator, _ := json.Marshal(importCandidateLocator{Version: 1, Native: snapshotID})
			evidence, _ := json.Marshal(importCandidateEvidence{
				Version: 1, Provider: backupasset.ProviderRestic, OpaqueDigest: strings.Repeat("b", 64), CapturedAt: now.Add(-time.Hour),
				Semantics: backupasset.PointNativeSnapshot, SourceRevision: snapshotID,
			})
			winnerEvidence := string(evidence)
			if testCase.mismatch {
				mismatchedEvidence, _ := json.Marshal(importCandidateEvidence{
					Version: 1, Provider: backupasset.ProviderRestic, OpaqueDigest: strings.Repeat("c", 64), CapturedAt: now.Add(-time.Hour),
					Semantics: backupasset.PointNativeSnapshot, SourceRevision: snapshotID,
				})
				winnerEvidence = string(mismatchedEvidence)
			}
			normalized := normalizedImportCandidate{
				kind: backupasset.ImportCandidateNativeSnapshot, fingerprint: strings.Repeat("d", 64),
				locator: string(locator), evidence: string(evidence),
			}
			winnerID := strings.Repeat("e", 32)
			injected := false
			var injectionErr error
			callbackName := "test:inject_import_candidate_unique_collision"
			if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
				if injected || tx.Statement.Table != "backup_repository_import_candidates" {
					return
				}
				injected = true
				injectionErr = tx.Exec(`INSERT INTO backup_repository_import_candidates
					(id, repository_id, candidate_kind, source_fingerprint, encrypted_provider_locator, encrypted_evidence,
					 review_state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					winnerID, repositoryID, normalized.kind, normalized.fingerprint, normalized.locator, winnerEvidence,
					backupasset.ImportReviewPending, now, now).Error
			}); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := db.Callback().Create().Remove(callbackName); err != nil {
					t.Errorf("remove collision callback: %v", err)
				}
			}()
			service, err := NewService(Dependencies{
				DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			var winner model.BackupRepositoryImportCandidate
			var created bool
			err = db.Transaction(func(tx *gorm.DB) error {
				var persistErr error
				winner, created, persistErr = service.persistImportCandidate(tx, repositoryID, normalized)
				return persistErr
			})
			if injectionErr != nil {
				t.Fatalf("inject unique winner: %v", injectionErr)
			}
			if testCase.wantExact && (err != nil || created || winner.ID != winnerID ||
				winner.RepositoryID != repositoryID || winner.SourceFingerprint != normalized.fingerprint ||
				winner.EncryptedProviderLocator != normalized.locator || winner.EncryptedEvidence != normalized.evidence) {
				t.Fatalf("exact winner=%+v created=%v err=%v", importCandidateView(winner), created, err)
			}
			if testCase.wantConflict && !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("mismatched unique winner error=%v", err)
			}
		})
	}
}

func TestImportConcurrentDiscoveryUniqueCollisionPostgres(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		mismatch bool
	}{
		{name: "exact winner"},
		{name: "private evidence mismatch", mismatch: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newImportPostgresTestDB(t)
			now := time.Date(2026, 8, 17, 10, 5, 0, 0, time.UTC)
			repositoryID := strings.Repeat("7", 32)
			repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("8", 64)
			repository := model.BackupRepository{
				ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
				DisplayName: "PostgreSQL discovery race", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
				CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
				CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&repository).Error; err != nil {
				t.Fatal(err)
			}
			snapshotID := strings.Repeat("a", 64)
			locator, _ := json.Marshal(importCandidateLocator{Version: 1, Native: snapshotID})
			evidence, _ := json.Marshal(importCandidateEvidence{
				Version: 1, Provider: backupasset.ProviderRestic, OpaqueDigest: strings.Repeat("b", 64), CapturedAt: now.Add(-time.Hour),
				Semantics: backupasset.PointNativeSnapshot, SourceRevision: snapshotID,
			})
			candidates := []normalizedImportCandidate{
				{kind: backupasset.ImportCandidateNativeSnapshot, fingerprint: strings.Repeat("d", 64), locator: string(locator), evidence: string(evidence)},
				{kind: backupasset.ImportCandidateNativeSnapshot, fingerprint: strings.Repeat("d", 64), locator: string(locator), evidence: string(evidence)},
			}
			if testCase.mismatch {
				mismatchedEvidence, _ := json.Marshal(importCandidateEvidence{
					Version: 1, Provider: backupasset.ProviderRestic, OpaqueDigest: strings.Repeat("c", 64), CapturedAt: now.Add(-time.Hour),
					Semantics: backupasset.PointNativeSnapshot, SourceRevision: snapshotID,
				})
				candidates[1].evidence = string(mismatchedEvidence)
			}
			service, err := NewService(Dependencies{
				DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}

			arrived := make(chan struct{}, 2)
			release := make(chan struct{})
			var initialQueries atomic.Int32
			callbackName := "test:block_postgres_import_candidate_initial_lookup"
			if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Table != "backup_repository_import_candidates" || initialQueries.Add(1) > 2 {
					return
				}
				arrived <- struct{}{}
				<-release
			}); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := db.Callback().Query().Remove(callbackName); err != nil {
					t.Errorf("remove PostgreSQL collision callback: %v", err)
				}
			}()

			type collisionOutcome struct {
				row     model.BackupRepositoryImportCandidate
				created bool
				err     error
			}
			outcomes := make(chan collisionOutcome, 2)
			for index := range candidates {
				candidate := candidates[index]
				go func() {
					var outcome collisionOutcome
					outcome.err = db.Transaction(func(tx *gorm.DB) error {
						var persistErr error
						outcome.row, outcome.created, persistErr = service.persistImportCandidate(tx, repositoryID, candidate)
						return persistErr
					})
					outcomes <- outcome
				}()
			}
			timeout := time.NewTimer(5 * time.Second)
			defer timeout.Stop()
			for index := 0; index < 2; index++ {
				select {
				case <-arrived:
				case <-timeout.C:
					close(release)
					t.Fatal("PostgreSQL collision contenders did not reach the initial lookup barrier")
				}
			}
			close(release)
			results := []collisionOutcome{<-outcomes, <-outcomes}
			createdCount := 0
			successCount := 0
			conflictCount := 0
			winnerID := ""
			for _, result := range results {
				if result.err == nil {
					successCount++
					if result.created {
						createdCount++
					}
					if winnerID == "" {
						winnerID = result.row.ID
					} else if result.row.ID != winnerID {
						t.Fatalf("PostgreSQL collision returned different winner IDs")
					}
				} else if errors.Is(result.err, backupasset.ErrConflict) {
					conflictCount++
				} else {
					t.Fatalf("PostgreSQL collision returned non-typed error: %v", result.err)
				}
			}
			if !testCase.mismatch && (successCount != 2 || conflictCount != 0 || createdCount != 1) {
				t.Fatalf("exact PostgreSQL collision success=%d conflict=%d created=%d", successCount, conflictCount, createdCount)
			}
			if testCase.mismatch && (successCount != 1 || conflictCount != 1 || createdCount != 1) {
				t.Fatalf("mismatched PostgreSQL collision success=%d conflict=%d created=%d", successCount, conflictCount, createdCount)
			}
			var persisted int64
			if err := db.Model(&model.BackupRepositoryImportCandidate{}).
				Where("repository_id = ? AND source_fingerprint = ?", repositoryID, candidates[0].fingerprint).
				Count(&persisted).Error; err != nil || persisted != 1 {
				t.Fatalf("PostgreSQL collision persisted=%d err=%v", persisted, err)
			}
		})
	}

	t.Run("unrelated insert error", func(t *testing.T) {
		db := newImportPostgresTestDB(t)
		now := time.Date(2026, 8, 17, 10, 5, 0, 0, time.UTC)
		if err := db.Exec(`ALTER TABLE backup_repository_import_candidates
			ADD CONSTRAINT test_reject_unrelated_kind CHECK (candidate_kind <> 'native_snapshot')`).Error; err != nil {
			t.Fatalf("create unrelated PostgreSQL insert constraint: %v", err)
		}
		service, err := NewService(Dependencies{
			DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now },
		})
		if err != nil {
			t.Fatal(err)
		}
		snapshotID := strings.Repeat("a", 64)
		locator, _ := json.Marshal(importCandidateLocator{Version: 1, Native: snapshotID})
		evidence, _ := json.Marshal(importCandidateEvidence{
			Version: 1, Provider: backupasset.ProviderRestic, OpaqueDigest: strings.Repeat("b", 64), CapturedAt: now.Add(-time.Hour),
			Semantics: backupasset.PointNativeSnapshot, SourceRevision: snapshotID,
		})
		candidate := normalizedImportCandidate{
			kind: backupasset.ImportCandidateNativeSnapshot, fingerprint: strings.Repeat("d", 64),
			locator: string(locator), evidence: string(evidence),
		}
		err = db.Transaction(func(tx *gorm.DB) error {
			_, _, persistErr := service.persistImportCandidate(tx, strings.Repeat("f", 32), candidate)
			return persistErr
		})
		if err == nil || errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("unrelated PostgreSQL insert error was swallowed or relabeled: %v", err)
		}
	})
}

func newImportPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_REPOSITORY_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_REPOSITORY_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is required for the focused PostgreSQL import collision regression")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatal("TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	adminDB, err := gorm.Open(postgresgorm.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open PostgreSQL import fixture database: %v", err)
	}
	digest := sha256.Sum256([]byte(t.Name()))
	schema := fmt.Sprintf("repository_import_%x", digest[:8])
	if err := adminDB.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create PostgreSQL import schema: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := gorm.Open(postgresgorm.Open(parsed.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = adminDB.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		t.Fatalf("open isolated PostgreSQL import schema: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("isolated PostgreSQL import sql database: %v", err)
	}
	adminSQLDB, err := adminDB.DB()
	if err != nil {
		t.Fatalf("PostgreSQL import fixture sql database: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		_ = adminDB.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		_ = adminSQLDB.Close()
	})
	if err := db.AutoMigrate(&model.BackupRepository{}, &model.BackupRepositoryImportCandidate{}); err != nil {
		t.Fatalf("migrate isolated PostgreSQL import schema: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_backup_repository_import_candidates_source
		ON backup_repository_import_candidates(repository_id, source_fingerprint)`).Error; err != nil {
		t.Fatalf("create PostgreSQL import source index: %v", err)
	}
	return db
}

func TestImportManagedManifestProofMatrixMissingVerifierFailsClosed(t *testing.T) {
	for _, providerKind := range []backupasset.ProviderKind{backupasset.ProviderRsync, backupasset.ProviderRclone} {
		t.Run(string(providerKind), func(t *testing.T) {
			db := newRepositoryTestDB(t)
			migrateImportCandidateTestTable(t, db)
			executorConfig := ""
			target := t.TempDir()
			if providerKind == backupasset.ProviderRclone {
				target = "backup:legacy"
				executorConfig = `{"bandwidth_limit":"10M","transfers":4}`
			}
			taskEntity := seedTask(t, db, string(providerKind), target, executorConfig)
			prober := scopedObservationProber(providerKind)
			lister := &importPointListerSpy{page: provider.NativePointPage{Items: []provider.NativePoint{{
				OpaqueDigest: strings.Repeat("d", 64), CapturedAt: time.Date(2026, 8, 17, 6, 0, 0, 0, time.UTC),
				Semantics: backupasset.PointXirangManifest, SourceRevision: strings.Repeat("e", 64),
				Locator: provider.PointLocator{Native: "UNVERIFIED_XIRANG_MANIFEST_FOR_TEST_ONLY"},
			}}}}
			registry := provider.NewRegistry()
			if err := registry.Register(providerKind, provider.Registration{Prober: prober, PointLister: lister}); err != nil {
				t.Fatal(err)
			}
			service, err := NewService(Dependencies{
				DB: db, Foundation: enabledFoundation(), Registry: registry,
				Now: func() time.Time { return time.Date(2026, 8, 17, 6, 5, 0, 0, time.UTC) },
			})
			if err != nil {
				t.Fatal(err)
			}
			connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.DiscoverImportCandidates(context.Background(), connected.Repository.ID, ImportDiscoveryRequest{Limit: 10},
				RequestContext{Actor: backupasset.AuditActor{UserID: 41, Username: "admin", Role: "admin"}})
			if !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("unverified %s xirang_manifest admitted: %v", providerKind, err)
			}
			var candidateCount int64
			if countErr := db.Model(&model.BackupRepositoryImportCandidate{}).Count(&candidateCount).Error; countErr != nil || candidateCount != 0 {
				t.Fatalf("candidate count=%d err=%v", candidateCount, countErr)
			}
		})
	}
}

func TestImportManagedManifestProofMatrixValidAndInvalid(t *testing.T) {
	for _, providerKind := range []backupasset.ProviderKind{backupasset.ProviderRsync, backupasset.ProviderRclone} {
		for _, proofCase := range []struct {
			name string
			err  error
			want bool
		}{
			{name: "valid", want: true},
			{name: "invalid_marker", err: errors.New("FAKE_INVALID_MARKER_FOR_TEST_ONLY")},
			{name: "incomplete_commit_graph", err: errors.New("FAKE_INCOMPLETE_COMMIT_GRAPH_FOR_TEST_ONLY")},
			{name: "digest_mismatch", err: errors.New("FAKE_COMMIT_DIGEST_MISMATCH_FOR_TEST_ONLY")},
		} {
			t.Run(string(providerKind)+"/"+proofCase.name, func(t *testing.T) {
				db := newRepositoryTestDB(t)
				migrateImportCandidateTestTable(t, db)
				executorConfig := ""
				target := t.TempDir()
				if providerKind == backupasset.ProviderRclone {
					target = "backup:legacy"
					executorConfig = `{"bandwidth_limit":"10M","transfers":4}`
				}
				taskEntity := seedTask(t, db, string(providerKind), target, executorConfig)
				prober := scopedObservationProber(providerKind)
				candidateDigest := strings.Repeat("7", 64)
				commitGraphDigest := strings.Repeat("8", 64)
				privateLocator := "PRIVATE_MANAGED_MARKER_LOCATOR_FOR_TEST_ONLY"
				lister := &importPointListerSpy{page: provider.NativePointPage{Items: []provider.NativePoint{{
					OpaqueDigest: candidateDigest, CapturedAt: time.Date(2026, 8, 17, 6, 30, 0, 0, time.UTC),
					Semantics: backupasset.PointXirangManifest, SourceRevision: commitGraphDigest,
					Locator: provider.PointLocator{Native: privateLocator},
				}}}}
				registry := provider.NewRegistry()
				if err := registry.Register(providerKind, provider.Registration{Prober: prober, PointLister: lister}); err != nil {
					t.Fatal(err)
				}
				verifier := &managedManifestProofVerifierSpy{err: proofCase.err}
				service, err := NewService(Dependencies{
					DB: db, Foundation: enabledFoundation(), Registry: registry, ManifestProof: verifier,
					Now: func() time.Time { return time.Date(2026, 8, 17, 6, 35, 0, 0, time.UTC) },
				})
				if err != nil {
					t.Fatal(err)
				}
				connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
				if err != nil {
					t.Fatal(err)
				}
				result, err := service.DiscoverImportCandidates(context.Background(), connected.Repository.ID, ImportDiscoveryRequest{Limit: 10},
					RequestContext{Actor: backupasset.AuditActor{UserID: 42, Username: "admin", Role: "admin"}})
				if proofCase.want {
					if err != nil || result.Discovered != 1 || len(result.Candidates) != 1 || result.Candidates[0].Kind != backupasset.ImportCandidateXirangManifest {
						t.Fatalf("result=%+v err=%v", result, err)
					}
				} else if !errors.Is(err, backupasset.ErrConflict) {
					t.Fatalf("invalid proof admitted: result=%+v err=%v", result, err)
				}
				if len(verifier.requests) != 1 {
					t.Fatalf("proof requests=%+v", verifier.requests)
				}
				request := verifier.requests[0]
				if request.RepositoryID != connected.Repository.ID || request.Provider != providerKind ||
					request.CandidateDigest != candidateDigest || !isLowerHex64(request.MarkerDigest) ||
					len(request.CommitDigests) != 1 || request.CommitDigests[0] != commitGraphDigest {
					t.Fatalf("proof request=%+v", request)
				}
				payload, marshalErr := json.Marshal(request)
				if marshalErr != nil || strings.Contains(string(payload), privateLocator) {
					t.Fatalf("unsafe proof request=%s err=%v", payload, marshalErr)
				}
				var candidateCount int64
				if countErr := db.Model(&model.BackupRepositoryImportCandidate{}).Count(&candidateCount).Error; countErr != nil ||
					(proofCase.want && candidateCount != 1) || (!proofCase.want && candidateCount != 0) {
					t.Fatalf("candidate count=%d err=%v", candidateCount, countErr)
				}
			})
		}
	}
}

func TestImportManagedManifestReviewRevalidatesProofBeforeAcceptance(t *testing.T) {
	for index, providerKind := range []backupasset.ProviderKind{backupasset.ProviderRsync, backupasset.ProviderRclone} {
		t.Run(string(providerKind), func(t *testing.T) {
			db := newRepositoryTestDB(t)
			migrateImportCandidateTestTable(t, db)
			now := time.Date(2026, 8, 17, 7, 0, 0, 0, time.UTC)
			admin := model.User{Username: "managed-review-" + string(providerKind), PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}
			if err := db.Create(&admin).Error; err != nil {
				t.Fatal(err)
			}
			repositoryID := strings.Repeat(string(byte('1'+index)), 32)
			repositoryIdentity := provider.ScopedIdentityPrefix(providerKind) + strings.Repeat(string(byte('3'+index)), 64)
			versionMode := backupasset.VersionHardlinkTree
			if providerKind == backupasset.ProviderRclone {
				versionMode = backupasset.VersionVersionedPrefix
			}
			repository := model.BackupRepository{
				ID: repositoryID, ProviderKind: string(providerKind), RepositoryIdentity: &repositoryIdentity,
				DisplayName: "managed review", VersionMode: string(versionMode), Status: string(backupasset.RepositoryOnline),
				CapabilityRevision: 1, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
				ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&repository).Error; err != nil {
				t.Fatal(err)
			}
			candidateDigest := strings.Repeat("a", 64)
			markerDigest := strings.Repeat("b", 64)
			commitDigest := strings.Repeat("c", 64)
			locator, _ := json.Marshal(importCandidateLocator{Version: 1, Native: "PRIVATE_MANAGED_REVIEW_LOCATOR_FOR_TEST_ONLY"})
			evidence, _ := json.Marshal(importCandidateEvidence{
				Version: 1, Provider: providerKind, OpaqueDigest: candidateDigest, CapturedAt: now.Add(-time.Hour),
				Semantics: backupasset.PointXirangManifest, SourceRevision: commitDigest,
				ManagedManifestProof: &importManagedManifestProof{
					Version: 1, CandidateDigest: candidateDigest, MarkerDigest: markerDigest, CommitDigests: []string{commitDigest},
				},
			})
			candidate := model.BackupRepositoryImportCandidate{
				ID: strings.Repeat(string(byte('5'+index)), 32), RepositoryID: repositoryID,
				CandidateKind: string(backupasset.ImportCandidateXirangManifest), SourceFingerprint: strings.Repeat(string(byte('7'+index)), 64),
				EncryptedProviderLocator: string(locator), EncryptedEvidence: string(evidence), ReviewState: string(backupasset.ImportReviewPending),
				CreatedAt: now, UpdatedAt: now,
			}
			if err := db.Create(&candidate).Error; err != nil {
				t.Fatal(err)
			}
			verifier := &managedManifestProofVerifierSpy{err: errors.New("FAKE_REVIEW_PROOF_REJECTED_FOR_TEST_ONLY")}
			service, err := NewService(Dependencies{
				DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), ManifestProof: verifier, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID,
				ImportReviewRequest{Decision: backupasset.ImportReviewAccepted},
				RequestContext{Actor: backupasset.AuditActor{UserID: admin.ID, Username: admin.Username, Role: "admin"}})
			if !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("stored proof self-authorized acceptance: %v", err)
			}
			if len(verifier.requests) != 1 || verifier.requests[0].RepositoryID != repositoryID || verifier.requests[0].Provider != providerKind ||
				verifier.requests[0].CandidateDigest != candidateDigest || verifier.requests[0].MarkerDigest != markerDigest ||
				len(verifier.requests[0].CommitDigests) != 1 || verifier.requests[0].CommitDigests[0] != commitDigest {
				t.Fatalf("proof requests=%+v", verifier.requests)
			}
			var persisted model.BackupRepositoryImportCandidate
			if err := db.First(&persisted, "id = ?", candidate.ID).Error; err != nil {
				t.Fatal(err)
			}
			var pointCount int64
			if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ?", repositoryID).Count(&pointCount).Error; err != nil ||
				persisted.ReviewState != string(backupasset.ImportReviewPending) || persisted.AcceptedRecoveryPointID != nil || pointCount != 0 {
				t.Fatalf("persisted=%+v pointCount=%d err=%v", persisted, pointCount, err)
			}
		})
	}
}

func TestImportReviewAcceptIsTerminalIdempotentAndCreatesOneRecoveryPoint(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	now := time.Date(2026, 8, 17, 3, 30, 0, 0, time.UTC)
	admin := model.User{Username: "import-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	repositoryID := strings.Repeat("3", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("4", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "import review", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 2, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	snapshotID := strings.Repeat("5", 64)
	locatorJSON, err := json.Marshal(struct {
		Version int    `json:"version"`
		Native  string `json:"native"`
	}{Version: 1, Native: snapshotID})
	if err != nil {
		t.Fatal(err)
	}
	evidenceJSON, err := json.Marshal(importCandidateEvidence{
		Version: 1, Provider: backupasset.ProviderRestic, OpaqueDigest: snapshotID,
		CapturedAt: now.Add(-time.Hour), Semantics: backupasset.PointNativeSnapshot, SourceRevision: snapshotID,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := model.BackupRepositoryImportCandidate{
		ID: strings.Repeat("6", 32), RepositoryID: repositoryID, CandidateKind: string(backupasset.ImportCandidateNativeSnapshot),
		SourceFingerprint: strings.Repeat("7", 64), EncryptedProviderLocator: string(locatorJSON), EncryptedEvidence: string(evidenceJSON),
		ReviewState: string(backupasset.ImportReviewPending), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	requestContext := RequestContext{Actor: backupasset.AuditActor{UserID: admin.ID, Username: admin.Username, Role: "admin"}}
	request := ImportReviewRequest{Decision: backupasset.ImportReviewAccepted}
	first, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID, request, requestContext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID, request, requestContext)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != backupasset.ImportReviewAccepted || first.AcceptedRecoveryPointID == "" ||
		second.AcceptedRecoveryPointID != first.AcceptedRecoveryPointID || second.State != first.State {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if _, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewRejected}, requestContext); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("terminal accept changed by reject: %v", err)
	}
	var points []model.RecoveryPoint
	if err := db.Where("repository_id = ?", repositoryID).Find(&points).Error; err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("recovery points=%+v", points)
	}
	point := points[0]
	if point.ID != first.AcceptedRecoveryPointID || point.Semantics != string(backupasset.PointNativeSnapshot) ||
		point.State != string(backupasset.RecoveryPointCommitted) || point.SourceFingerprint != candidate.SourceFingerprint ||
		point.ManifestDigest != snapshotID || point.EncryptedProviderLocator != string(locatorJSON) ||
		point.CapturedAt == nil || !point.CapturedAt.Equal(now.Add(-time.Hour)) || point.CommittedAt == nil || !point.CommittedAt.Equal(now) ||
		point.CapabilityRevision != repository.CapabilityRevision || point.CapabilitiesJSON != repository.CapabilitiesJSON ||
		point.ImmutabilityLevel != repository.ImmutabilityLevel || point.PhysicalAvailability != string(backupasset.PhysicalOnline) ||
		point.HoldState != string(backupasset.HoldNone) {
		t.Fatalf("accepted point=%+v", point)
	}
	var persisted struct {
		ReviewState             string
		ReviewedBy              *uint
		AcceptedRecoveryPointID *string
		EncryptedLocator        string
	}
	if err := db.Raw(`SELECT review_state, reviewed_by, accepted_recovery_point_id,
		(SELECT encrypted_provider_locator FROM recovery_points WHERE id = accepted_recovery_point_id) AS encrypted_locator
		FROM backup_repository_import_candidates WHERE id = ?`, candidate.ID).Scan(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.ReviewState != string(backupasset.ImportReviewAccepted) || persisted.ReviewedBy == nil || *persisted.ReviewedBy != admin.ID ||
		persisted.AcceptedRecoveryPointID == nil || *persisted.AcceptedRecoveryPointID != point.ID ||
		!strings.HasPrefix(persisted.EncryptedLocator, "enc:v2:") || strings.Contains(persisted.EncryptedLocator, snapshotID) {
		t.Fatalf("persisted terminal review=%+v", persisted)
	}
}

func TestImportReviewAcceptBindsExistingExactPointWithoutDuplicate(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	now := time.Date(2026, 8, 17, 3, 45, 0, 0, time.UTC)
	admin := model.User{Username: "import-bind-admin", PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	repositoryID := strings.Repeat("a", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("b", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "import bind", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	snapshotID := strings.Repeat("c", 64)
	locator, _ := json.Marshal(importCandidateLocator{Version: 1, Native: snapshotID})
	evidence, _ := json.Marshal(importCandidateEvidence{
		Version: 1, Provider: backupasset.ProviderRestic, OpaqueDigest: snapshotID, CapturedAt: now.Add(-time.Hour),
		Semantics: backupasset.PointNativeSnapshot, SourceRevision: snapshotID,
	})
	sourceFingerprint := strings.Repeat("d", 64)
	existingPoint := model.RecoveryPoint{
		ID: strings.Repeat("e", 32), RepositoryID: repositoryID, LineageJSON: `{}`, EncryptedProviderLocator: string(locator),
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted), CapturedAt: pointerTime(now.Add(-time.Hour)),
		CommittedAt: pointerTime(now.Add(-30 * time.Minute)), SourceFingerprint: sourceFingerprint, ManifestDigestAlgorithm: "sha256", ManifestDigest: snapshotID,
		ConsistencyJSON: `{}`, FidelityJSON: `{}`, PointRevision: 1, CapabilityRevision: 1, CapabilitiesJSON: `{}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), PhysicalAvailability: string(backupasset.PhysicalOnline),
		HoldState: string(backupasset.HoldNone), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-30 * time.Minute),
	}
	if err := db.Create(&existingPoint).Error; err != nil {
		t.Fatal(err)
	}
	candidate := model.BackupRepositoryImportCandidate{
		ID: strings.Repeat("f", 32), RepositoryID: repositoryID, CandidateKind: string(backupasset.ImportCandidateNativeSnapshot),
		SourceFingerprint: sourceFingerprint, EncryptedProviderLocator: string(locator), EncryptedEvidence: string(evidence),
		ReviewState: string(backupasset.ImportReviewPending), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewAccepted},
		RequestContext{Actor: backupasset.AuditActor{UserID: admin.ID, Username: admin.Username, Role: "admin"}})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.AcceptedRecoveryPointID != existingPoint.ID {
		t.Fatalf("accepted=%+v existing=%s", accepted, existingPoint.ID)
	}
	var pointCount int64
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ?", repositoryID).Count(&pointCount).Error; err != nil || pointCount != 1 {
		t.Fatalf("point count=%d err=%v", pointCount, err)
	}
}

func TestImportPendingListAndRejectAreAdminOnlyTerminalAndPointFree(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	now := time.Date(2026, 8, 17, 4, 0, 0, 0, time.UTC)
	repositoryID := strings.Repeat("8", 32)
	repositoryIdentity := provider.NativeResticIdentityPrefix + strings.Repeat("9", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRestic), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "pending import", VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	snapshotID := strings.Repeat("a", 64)
	locator, _ := json.Marshal(importCandidateLocator{Version: 1, Native: snapshotID})
	evidence, _ := json.Marshal(importCandidateEvidence{
		Version: 1, Provider: backupasset.ProviderRestic, OpaqueDigest: snapshotID, CapturedAt: now.Add(-time.Hour),
		Semantics: backupasset.PointNativeSnapshot, SourceRevision: snapshotID,
	})
	candidate := model.BackupRepositoryImportCandidate{
		ID: strings.Repeat("b", 32), RepositoryID: repositoryID, CandidateKind: string(backupasset.ImportCandidateNativeSnapshot),
		SourceFingerprint: strings.Repeat("c", 64), EncryptedProviderLocator: string(locator), EncryptedEvidence: string(evidence),
		ReviewState: string(backupasset.ImportReviewPending), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListImportCandidates(context.Background(), repositoryID, ImportCandidateListRequest{Limit: 10},
		VisibilityScope{Role: "operator", UserID: 12}, RequestContext{}); !errors.Is(err, backupasset.ErrForbidden) {
		t.Fatalf("operator listed pending candidates: %v", err)
	}
	page, err := service.ListImportCandidates(context.Background(), repositoryID, ImportCandidateListRequest{Limit: 10},
		VisibilityScope{Role: "admin", UserID: 11}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != candidate.ID || page.Items[0].State != backupasset.ImportReviewPending || page.NextCursor != "" {
		t.Fatalf("pending page=%+v", page)
	}
	adminContext := RequestContext{Actor: backupasset.AuditActor{UserID: 11, Username: "admin", Role: "admin"}}
	request := ImportReviewRequest{Decision: backupasset.ImportReviewRejected}
	first, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID, request, adminContext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID, request, adminContext)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != backupasset.ImportReviewRejected || first.AcceptedRecoveryPointID != "" || second.State != first.State || second.AcceptedRecoveryPointID != "" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if _, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewAccepted}, adminContext); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("terminal rejection changed by accept: %v", err)
	}
	var pointCount int64
	if err := db.Model(&model.RecoveryPoint{}).Where("repository_id = ?", repositoryID).Count(&pointCount).Error; err != nil || pointCount != 0 {
		t.Fatalf("rejected candidate points=%d err=%v", pointCount, err)
	}
}

func TestImportMutableCandidateRequiresExplicitBaselineAndNeverReactivatesRetiredHead(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	now := time.Date(2026, 8, 17, 4, 30, 0, 0, time.UTC)
	repositoryID := strings.Repeat("d", 32)
	repositoryIdentity := provider.ScopedIdentityPrefix(backupasset.ProviderRsync) + strings.Repeat("e", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRsync), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "mutable import", VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityMutable), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	retiredAt := now.Add(-time.Hour)
	retirementReason := string(backupasset.RetirementWithdrawn)
	retired := model.RecoveryPoint{
		ID: strings.Repeat("1", 32), RepositoryID: repositoryID, LineageJSON: `{}`, EncryptedProviderLocator: `{"native":"old"}`,
		EncryptedRollbackLocator: `{"native":"old"}`, Semantics: string(backupasset.PointMutableHead), State: string(backupasset.RecoveryPointRetired),
		ObservedAt: &retiredAt, SourceFingerprint: strings.Repeat("2", 64), ManifestDigestAlgorithm: "sha256",
		ConsistencyJSON: `{}`, FidelityJSON: `{}`, PointRevision: 2, CapabilityRevision: 1, CapabilitiesJSON: repository.CapabilitiesJSON,
		ImmutabilityLevel: string(backupasset.ImmutabilityMutable), PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		RetirementReason: &retirementReason, RetiredAt: &retiredAt, CreatedAt: retiredAt, UpdatedAt: retiredAt,
	}
	if err := db.Create(&retired).Error; err != nil {
		t.Fatal(err)
	}
	sourceRevision := strings.Repeat("3", 64)
	locator, _ := json.Marshal(importCandidateLocator{Version: 1, Native: "arbitrary-tree"})
	evidence, _ := json.Marshal(importCandidateEvidence{
		Version: 1, Provider: backupasset.ProviderRsync, OpaqueDigest: strings.Repeat("4", 64), CapturedAt: now.Add(-30 * time.Minute),
		Semantics: backupasset.PointMutableHead, SourceRevision: sourceRevision,
	})
	candidate := model.BackupRepositoryImportCandidate{
		ID: strings.Repeat("5", 32), RepositoryID: repositoryID, CandidateKind: string(backupasset.ImportCandidateMutableHead),
		SourceFingerprint: strings.Repeat("6", 64), EncryptedProviderLocator: string(locator), EncryptedEvidence: string(evidence),
		ReviewState: string(backupasset.ImportReviewPending), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	admin := RequestContext{Actor: backupasset.AuditActor{UserID: 21, Username: "admin", Role: "admin"}}
	if _, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewAccepted}, admin); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("mutable candidate accepted without explicit disposition: %v", err)
	}
	accepted, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewAccepted, AcceptAs: backupasset.ImportCandidateImportedBaseline}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.AcceptedRecoveryPointID == "" || accepted.AcceptedRecoveryPointID == retired.ID || accepted.State != backupasset.ImportReviewAccepted {
		t.Fatalf("accepted=%+v retired=%s", accepted, retired.ID)
	}
	var points []model.RecoveryPoint
	if err := db.Where("repository_id = ?", repositoryID).Order("created_at ASC").Find(&points).Error; err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].ID != retired.ID || points[0].State != string(backupasset.RecoveryPointRetired) ||
		points[0].RetiredAt == nil || !points[0].RetiredAt.Equal(retiredAt) || points[0].RetirementReason == nil || *points[0].RetirementReason != retirementReason ||
		points[1].ID != accepted.AcceptedRecoveryPointID || points[1].Semantics != string(backupasset.PointImportedBaseline) ||
		points[1].State != string(backupasset.RecoveryPointCommitted) || points[1].ImmutabilityLevel != string(backupasset.ImmutabilityXirangManaged) {
		t.Fatalf("points=%+v", points)
	}
	replayed, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewAccepted, AcceptAs: backupasset.ImportCandidateImportedBaseline}, admin)
	if err != nil || replayed.AcceptedRecoveryPointID != accepted.AcceptedRecoveryPointID {
		t.Fatalf("exact accepted disposition replay=%+v err=%v", replayed, err)
	}
	if _, err := service.ReviewImportCandidate(context.Background(), repositoryID, candidate.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewAccepted, AcceptAs: backupasset.ImportCandidateMutableHead}, admin); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("contradictory accepted disposition replay error=%v", err)
	}
	var replayPoints []model.RecoveryPoint
	if err := db.Where("repository_id = ?", repositoryID).Order("created_at ASC").Find(&replayPoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(replayPoints) != len(points) || replayPoints[0].ID != points[0].ID || replayPoints[1].ID != points[1].ID ||
		replayPoints[1].Semantics != points[1].Semantics {
		t.Fatalf("terminal replay mutated points: before=%+v after=%+v", points, replayPoints)
	}
}

func TestImportRejectsSecondMutableBaselineFromSameFailedSource(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	now := time.Date(2026, 8, 19, 4, 30, 0, 0, time.UTC)
	repositoryID := strings.Repeat("d", 32)
	repositoryIdentity := provider.ScopedIdentityPrefix(backupasset.ProviderRsync) + strings.Repeat("e", 64)
	repository := model.BackupRepository{
		ID: repositoryID, ProviderKind: string(backupasset.ProviderRsync), RepositoryIdentity: &repositoryIdentity,
		DisplayName: "failed source uniqueness", VersionMode: string(backupasset.VersionMutableHead), Status: string(backupasset.RepositoryOnline),
		CapabilityRevision: 1, CapabilitiesJSON: `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel: string(backupasset.ImmutabilityMutable), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{DB: db, Foundation: enabledFoundation(), Registry: provider.NewRegistry(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	admin := RequestContext{Actor: backupasset.AuditActor{UserID: 21, Username: "admin", Role: "admin"}}
	first := seedPendingMutableImportCandidate(t, db, repositoryID, strings.Repeat("5", 32), strings.Repeat("6", 64), strings.Repeat("3", 64), now)
	accepted, err := service.ReviewImportCandidate(context.Background(), repositoryID, first.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewAccepted, AcceptAs: backupasset.ImportCandidateImportedBaseline}, admin)
	if err != nil {
		t.Fatal(err)
	}
	second := seedPendingMutableImportCandidate(t, db, repositoryID, strings.Repeat("7", 32), strings.Repeat("8", 64), strings.Repeat("9", 64), now.Add(time.Minute))
	if _, err := service.ReviewImportCandidate(context.Background(), repositoryID, second.ID,
		ImportReviewRequest{Decision: backupasset.ImportReviewAccepted, AcceptAs: backupasset.ImportCandidateImportedBaseline}, admin); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("second mutable baseline from same failed source error=%v, want ErrConflict", err)
	}
	var baselines int64
	if err := db.Model(&model.RecoveryPoint{}).
		Where("repository_id = ? AND semantics = ?", repositoryID, backupasset.PointImportedBaseline).
		Count(&baselines).Error; err != nil || baselines != 1 {
		t.Fatalf("imported baselines=%d err=%v, want 1 (accepted=%s)", baselines, err, accepted.AcceptedRecoveryPointID)
	}
}

func TestImportDiscoveryQuarantinesBadPointAndKeepsGoodCandidate(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repository", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	nativeRepositoryID := strings.Repeat("1", 64)
	goodSnapshot := strings.Repeat("2", 64)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, provider.NativeResticIdentityPrefix+nativeRepositoryID)}
	lister := &importPointListerSpy{page: provider.NativePointPage{Items: []provider.NativePoint{
		{
			OpaqueDigest:   "not-a-digest",
			CapturedAt:     time.Time{},
			Semantics:      backupasset.PointVersionSemantics("unknown_kind"),
			SourceRevision: "",
			Locator:        provider.PointLocator{Native: ""},
		},
		{
			OpaqueDigest:   goodSnapshot,
			CapturedAt:     time.Date(2026, 8, 19, 3, 0, 0, 0, time.UTC),
			Semantics:      backupasset.PointNativeSnapshot,
			SourceRevision: goodSnapshot,
			Locator:        provider.PointLocator{Native: goodSnapshot},
		},
	}}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: prober, PointLister: lister}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry,
		Now: func() time.Time { return time.Date(2026, 8, 19, 3, 5, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	admin := RequestContext{Actor: backupasset.AuditActor{UserID: 1, Username: "import-admin", Role: "admin"}}
	discovered, err := service.DiscoverImportCandidates(context.Background(), connected.Repository.ID, ImportDiscoveryRequest{Limit: 10}, admin)
	if err != nil {
		t.Fatalf("DiscoverImportCandidates: %v", err)
	}
	if discovered.Discovered != 2 || len(discovered.Candidates) != 2 {
		t.Fatalf("discovered=%+v, want 2 candidates including quarantine", discovered)
	}
	assertImportCandidateQuarantineViews(t, discovered.Candidates, "discover")
	listed, err := service.ListImportCandidates(context.Background(), connected.Repository.ID, ImportCandidateListRequest{Limit: 10},
		VisibilityScope{Role: "admin", UserID: 1}, admin)
	if err != nil {
		t.Fatalf("ListImportCandidates: %v", err)
	}
	assertImportCandidateQuarantineViews(t, listed.Items, "list")
	var rows []model.BackupRepositoryImportCandidate
	if err := db.Where("repository_id = ?", connected.Repository.ID).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("persisted candidates=%d, want 2", len(rows))
	}
	quarantined := 0
	good := 0
	operator := RequestContext{Actor: backupasset.AuditActor{UserID: 2, Username: "import-operator", Role: "operator"}}
	for _, row := range rows {
		var stored string
		if err := db.Raw(`SELECT encrypted_evidence FROM backup_repository_import_candidates WHERE id = ?`, row.ID).Scan(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(stored, "enc:v2:") {
			t.Fatalf("stored evidence is not sealed: id=%s prefix=%q", row.ID, stored)
		}
		var evidence importCandidateEvidence
		if err := json.Unmarshal([]byte(row.EncryptedEvidence), &evidence); err != nil {
			t.Fatalf("decode evidence: %v", err)
		}
		sealedView := importCandidateView(model.BackupRepositoryImportCandidate{
			ID: row.ID, RepositoryID: row.RepositoryID, CandidateKind: row.CandidateKind,
			ReviewState: row.ReviewState, CreatedAt: row.CreatedAt, EncryptedEvidence: stored,
		})
		if sealedView.Quarantined != evidence.Quarantined {
			t.Fatalf("sealed view quarantined=%v evidence=%+v stored_prefix=%q", sealedView.Quarantined, evidence, stored[:len("enc:v2:")])
		}
		if evidence.Quarantined {
			quarantined++
			if _, err := service.ReviewImportCandidate(context.Background(), connected.Repository.ID, row.ID,
				ImportReviewRequest{Decision: backupasset.ImportReviewRejected}, operator); !errors.Is(err, backupasset.ErrForbidden) {
				t.Fatalf("operator reject quarantined error=%v, want ErrForbidden", err)
			}
			if _, err := service.ReviewImportCandidate(context.Background(), connected.Repository.ID, row.ID,
				ImportReviewRequest{Decision: backupasset.ImportReviewAccepted, AcceptAs: backupasset.ImportCandidateKind(row.CandidateKind)}, admin); !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("accept quarantined error=%v, want ErrInvalidState", err)
			}
			rejected, err := service.ReviewImportCandidate(context.Background(), connected.Repository.ID, row.ID,
				ImportReviewRequest{Decision: backupasset.ImportReviewRejected}, admin)
			if err != nil {
				t.Fatalf("reject quarantined error=%v, want success", err)
			}
			if rejected.State != backupasset.ImportReviewRejected || !rejected.Quarantined {
				t.Fatalf("rejected quarantined view=%+v", rejected)
			}
			continue
		}
		if row.CandidateKind != string(backupasset.ImportCandidateNativeSnapshot) || evidence.SourceRevision != goodSnapshot {
			t.Fatalf("good candidate=%+v evidence=%+v", row, evidence)
		}
		good++
	}
	if quarantined != 1 || good != 1 {
		t.Fatalf("quarantined=%d good=%d, want 1/1", quarantined, good)
	}
}

func TestImportCandidateViewExposesQuarantinedFromEvidence(t *testing.T) {
	now := time.Date(2026, 8, 19, 5, 0, 0, 0, time.UTC)
	row := model.BackupRepositoryImportCandidate{
		ID: strings.Repeat("a", 32), RepositoryID: strings.Repeat("b", 32),
		CandidateKind: string(backupasset.ImportCandidateImportedBaseline),
		ReviewState:   string(backupasset.ImportReviewPending), CreatedAt: now,
		EncryptedEvidence: `{"version":1,"quarantined":true}`,
	}
	if view := importCandidateView(row); !view.Quarantined {
		t.Fatalf("fallback evidence view=%+v, want Quarantined=true", view)
	}
	goodEvidence := `{"version":1,"provider":"restic","opaque_digest":"` + strings.Repeat("c", 64) +
		`","captured_at":"2026-08-19T05:00:00Z","semantics":"native_snapshot","source_revision":"` + strings.Repeat("c", 64) + `"}`
	row.EncryptedEvidence = goodEvidence
	if view := importCandidateView(row); view.Quarantined {
		t.Fatalf("good evidence view=%+v, want Quarantined=false", view)
	}

	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	sealedQuarantine, err := secure.EncryptIfNeeded(`{"version":1,"quarantined":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealedQuarantine, "enc:v2:") {
		t.Fatalf("sealed quarantine=%q, want enc:v2:", sealedQuarantine)
	}
	row.EncryptedEvidence = sealedQuarantine
	if view := importCandidateView(row); !view.Quarantined {
		t.Fatalf("sealed quarantine view=%+v, want Quarantined=true", view)
	}
	sealedGood, err := secure.EncryptIfNeeded(goodEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealedGood, "enc:v2:") || strings.Contains(sealedGood, `"quarantined"`) {
		t.Fatalf("sealed good=%q", sealedGood)
	}
	row.EncryptedEvidence = sealedGood
	if view := importCandidateView(row); view.Quarantined {
		t.Fatalf("sealed good view=%+v, want Quarantined=false", view)
	}
}

func assertImportCandidateQuarantineViews(t *testing.T, views []ImportCandidateView, label string) {
	t.Helper()
	quarantined := 0
	good := 0
	for _, view := range views {
		if view.Quarantined {
			quarantined++
		} else {
			good++
		}
	}
	if quarantined != 1 || good != 1 {
		t.Fatalf("%s quarantined=%d good=%d views=%+v, want 1/1", label, quarantined, good, views)
	}
}

func TestImportDiscoveryKeepsGoodCandidateWhenSiblingProofFails(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	taskEntity := seedTask(t, db, "rsync", t.TempDir(), "")
	goodDigest := strings.Repeat("a", 64)
	badDigest := strings.Repeat("b", 64)
	commitDigest := strings.Repeat("c", 64)
	prober := scopedObservationProber(backupasset.ProviderRsync)
	lister := &importPointListerSpy{page: provider.NativePointPage{Items: []provider.NativePoint{
		{
			OpaqueDigest: badDigest, CapturedAt: time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC),
			Semantics: backupasset.PointXirangManifest, SourceRevision: commitDigest,
			Locator: provider.PointLocator{Native: "PRIVATE_INVALID_PROOF_SIBLING_FOR_TEST_ONLY"},
		},
		{
			OpaqueDigest: goodDigest, CapturedAt: time.Date(2026, 8, 19, 4, 1, 0, 0, time.UTC),
			Semantics: backupasset.PointXirangManifest, SourceRevision: commitDigest,
			Locator: provider.PointLocator{Native: "PRIVATE_VALID_PROOF_SIBLING_FOR_TEST_ONLY"},
		},
	}}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRsync, provider.Registration{Prober: prober, PointLister: lister}); err != nil {
		t.Fatal(err)
	}
	verifier := &managedManifestProofVerifierSpy{
		errFor: map[string]error{badDigest: errors.New("FAKE_SIBLING_PROOF_REJECTED_FOR_TEST_ONLY")},
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry, ManifestProof: verifier,
		Now: func() time.Time { return time.Date(2026, 8, 19, 4, 5, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.DiscoverImportCandidates(context.Background(), connected.Repository.ID, ImportDiscoveryRequest{Limit: 10},
		RequestContext{Actor: backupasset.AuditActor{UserID: 43, Username: "admin", Role: "admin"}})
	if err != nil || result.Discovered != 1 || len(result.Candidates) != 1 || result.Candidates[0].Kind != backupasset.ImportCandidateXirangManifest {
		t.Fatalf("sibling proof page result=%+v err=%v", result, err)
	}
	var rows []model.BackupRepositoryImportCandidate
	if err := db.Where("repository_id = ?", connected.Repository.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted candidates=%d, want 1 trusted sibling", len(rows))
	}
	var evidence importCandidateEvidence
	if err := json.Unmarshal([]byte(rows[0].EncryptedEvidence), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Quarantined || evidence.OpaqueDigest != goodDigest {
		t.Fatalf("persisted evidence=%+v, want trusted good digest", evidence)
	}
}

func seedPendingMutableImportCandidate(
	t *testing.T,
	db *gorm.DB,
	repositoryID, candidateID, fingerprint, sourceRevision string,
	now time.Time,
) model.BackupRepositoryImportCandidate {
	t.Helper()
	locator, _ := json.Marshal(importCandidateLocator{Version: 1, Native: "arbitrary-tree-" + sourceRevision[:8]})
	evidence, _ := json.Marshal(importCandidateEvidence{
		Version: 1, Provider: backupasset.ProviderRsync, OpaqueDigest: strings.Repeat("4", 64), CapturedAt: now.Add(-30 * time.Minute),
		Semantics: backupasset.PointMutableHead, SourceRevision: sourceRevision,
	})
	candidate := model.BackupRepositoryImportCandidate{
		ID: candidateID, RepositoryID: repositoryID, CandidateKind: string(backupasset.ImportCandidateMutableHead),
		SourceFingerprint: fingerprint, EncryptedProviderLocator: string(locator), EncryptedEvidence: string(evidence),
		ReviewState: string(backupasset.ImportReviewPending), CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	return candidate
}

func TestReconcileImportsRefreshesSecondPagePendingAcrossTicks(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	snapshotID := strings.Repeat("a", 64)
	seed := seedPendingResticImportRepo(t, db, strings.Repeat("1", 64), snapshotID)
	var before model.BackupRepositoryImportCandidate
	if err := db.First(&before, "id = ?", seed.candidateID).Error; err != nil {
		t.Fatal(err)
	}
	matching := provider.NativePoint{
		OpaqueDigest:   snapshotID,
		CapturedAt:     time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC),
		Semantics:      backupasset.PointNativeSnapshot,
		SourceRevision: snapshotID,
		Locator:        provider.PointLocator{Native: snapshotID},
	}
	other := matching
	other.OpaqueDigest = strings.Repeat("c", 64)
	other.SourceRevision = other.OpaqueDigest
	other.Locator = provider.PointLocator{Native: other.OpaqueDigest}
	lister := &cursorPagedImportLister{
		pages: map[string]provider.NativePointPage{
			"":       {Items: []provider.NativePoint{other}, NextCursor: "page-2"},
			"page-2": {Items: []provider.NativePoint{matching}},
		},
	}
	service := newReconcileImportService(t, db, seed, resticImportRepoSeed{}, lister, nil)

	first, err := service.ReconcileImports(context.Background(), 10)
	if err != nil {
		t.Fatalf("first ReconcileImports: %v", err)
	}
	if first != 1 {
		t.Fatalf("first attempted=%d, want 1", first)
	}
	assertPendingImportCount(t, db, seed.candidateID, 1)
	var afterFirst model.BackupRepositoryImportCandidate
	if err := db.First(&afterFirst, "id = ?", seed.candidateID).Error; err != nil {
		t.Fatal(err)
	}
	if !afterFirst.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("first tick refreshed a second-page pending candidate")
	}
	if len(lister.cursors) != 1 || lister.cursors[0] != "" {
		t.Fatalf("first tick cursors=%v, want [\"\"]", lister.cursors)
	}

	second, err := service.ReconcileImports(context.Background(), 10)
	if err != nil {
		t.Fatalf("second ReconcileImports: %v", err)
	}
	if second != 1 {
		t.Fatalf("second attempted=%d, want 1", second)
	}
	var afterSecond model.BackupRepositoryImportCandidate
	if err := db.First(&afterSecond, "id = ?", seed.candidateID).Error; err != nil {
		t.Fatal(err)
	}
	if !afterSecond.UpdatedAt.After(before.UpdatedAt) {
		t.Fatalf("second-page pending candidate was not refreshed across ticks")
	}
	if len(lister.cursors) != 2 || lister.cursors[1] != "page-2" {
		t.Fatalf("second tick cursors=%v, want [\"\", \"page-2\"]", lister.cursors)
	}
}

func TestReconcileImportsLastPageTickDoesNotDropFirstPagePending(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	snapshotID := strings.Repeat("a", 64)
	seed := seedPendingResticImportRepo(t, db, strings.Repeat("1", 64), snapshotID)
	matching := provider.NativePoint{
		OpaqueDigest:   snapshotID,
		CapturedAt:     time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC),
		Semantics:      backupasset.PointNativeSnapshot,
		SourceRevision: snapshotID,
		Locator:        provider.PointLocator{Native: snapshotID},
	}
	other := matching
	other.OpaqueDigest = strings.Repeat("c", 64)
	other.SourceRevision = other.OpaqueDigest
	other.Locator = provider.PointLocator{Native: other.OpaqueDigest}
	lister := &cursorPagedImportLister{
		pages: map[string]provider.NativePointPage{
			"":       {Items: []provider.NativePoint{matching}, NextCursor: "page-2"},
			"page-2": {Items: []provider.NativePoint{other}},
		},
	}
	service := newReconcileImportService(t, db, seed, resticImportRepoSeed{}, lister, nil)

	first, err := service.ReconcileImports(context.Background(), 10)
	if err != nil {
		t.Fatalf("first ReconcileImports: %v", err)
	}
	if first != 1 {
		t.Fatalf("first attempted=%d, want 1", first)
	}
	assertPendingImportCount(t, db, seed.candidateID, 1)

	second, err := service.ReconcileImports(context.Background(), 10)
	if err != nil {
		t.Fatalf("second ReconcileImports: %v", err)
	}
	if second != 1 {
		t.Fatalf("second attempted=%d, want 1", second)
	}
	assertPendingImportCount(t, db, seed.candidateID, 1)
	if len(lister.cursors) != 2 || lister.cursors[1] != "page-2" {
		t.Fatalf("last-page tick cursors=%v, want [\"\", \"page-2\"]", lister.cursors)
	}
}

func TestReconcileImportsDoesNotAccumulateProviderListingInMemory(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	snapshotID := strings.Repeat("a", 64)
	seed := seedPendingResticImportRepo(t, db, strings.Repeat("1", 64), snapshotID)
	matching := provider.NativePoint{
		OpaqueDigest:   snapshotID,
		CapturedAt:     time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC),
		Semantics:      backupasset.PointNativeSnapshot,
		SourceRevision: snapshotID,
		Locator:        provider.PointLocator{Native: snapshotID},
	}
	pagePoint := func(digest string) provider.NativePoint {
		point := matching
		point.OpaqueDigest = digest
		point.SourceRevision = digest
		point.Locator = provider.PointLocator{Native: digest}
		return point
	}
	lister := &cursorPagedImportLister{
		pages: map[string]provider.NativePointPage{
			"":       {Items: []provider.NativePoint{matching}, NextCursor: "page-2"},
			"page-2": {Items: []provider.NativePoint{pagePoint(strings.Repeat("c", 64))}, NextCursor: "page-3"},
			"page-3": {Items: []provider.NativePoint{pagePoint(strings.Repeat("d", 64))}, NextCursor: "page-4"},
			"page-4": {Items: []provider.NativePoint{pagePoint(strings.Repeat("e", 64))}},
		},
	}
	service := newReconcileImportService(t, db, seed, resticImportRepoSeed{}, lister, nil)

	seenCount := func() int {
		service.importListingMu.Lock()
		defer service.importListingMu.Unlock()
		return len(service.importListingSeen[seed.repositoryID])
	}

	if _, err := service.ReconcileImports(context.Background(), 10); err != nil {
		t.Fatalf("page-1 ReconcileImports: %v", err)
	}
	if got := seenCount(); got > 1 {
		t.Fatalf("after page 1 seen fingerprints=%d, want current page only", got)
	}
	assertPendingImportCount(t, db, seed.candidateID, 1)

	if _, err := service.ReconcileImports(context.Background(), 10); err != nil {
		t.Fatalf("page-2 ReconcileImports: %v", err)
	}
	if got := seenCount(); got > 1 {
		t.Fatalf("after page 2 seen fingerprints=%d, want current page only not a growing union", got)
	}
	assertPendingImportCount(t, db, seed.candidateID, 1)

	if _, err := service.ReconcileImports(context.Background(), 10); err != nil {
		t.Fatalf("page-3 ReconcileImports: %v", err)
	}
	if got := seenCount(); got > 1 {
		t.Fatalf("after page 3 seen fingerprints=%d, want current page only", got)
	}

	if _, err := service.ReconcileImports(context.Background(), 10); err != nil {
		t.Fatalf("complete-cycle ReconcileImports: %v", err)
	}
	if got := seenCount(); got != 0 {
		t.Fatalf("after complete cycle seen fingerprints=%d, want map dropped", got)
	}
	assertPendingImportCount(t, db, seed.candidateID, 1)

	missingDB := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, missingDB)
	missing := seedPendingResticImportRepo(t, missingDB, strings.Repeat("2", 64), strings.Repeat("b", 64))
	missingLister := &cursorPagedImportLister{
		pages: map[string]provider.NativePointPage{
			"":       {Items: []provider.NativePoint{pagePoint(strings.Repeat("c", 64))}, NextCursor: "page-2"},
			"page-2": {Items: []provider.NativePoint{pagePoint(strings.Repeat("d", 64))}},
		},
	}
	missingService := newReconcileImportService(t, missingDB, missing, resticImportRepoSeed{}, missingLister, nil)
	if _, err := missingService.ReconcileImports(context.Background(), 10); err != nil {
		t.Fatalf("missing first-page tick: %v", err)
	}
	assertPendingImportCount(t, missingDB, missing.candidateID, 1)
	if _, err := missingService.ReconcileImports(context.Background(), 10); err != nil {
		t.Fatalf("missing complete-cycle tick: %v", err)
	}
	assertPendingImportCount(t, missingDB, missing.candidateID, 0)
}

func TestReconcileImportsWalksPastSkippedPrefix(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	first := seedPendingResticImportRepo(t, db, strings.Repeat("1", 64), strings.Repeat("a", 64))
	second := seedPendingResticImportRepo(t, db, strings.Repeat("2", 64), strings.Repeat("b", 64))
	var rows []model.BackupRepositoryImportCandidate
	if err := db.Order("id ASC").Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatalf("pending candidates=%d err=%v", len(rows), err)
	}
	prefix := rows[0]
	later := rows[1]
	lister := &keyedImportPointLister{
		pages: map[string]provider.NativePointPage{
			later.RepositoryID: {},
		},
		failFor: map[string]error{
			prefix.RepositoryID: errors.New("FAKE_IMPORT_PREFIX_LISTING_FAILURE_FOR_TEST_ONLY"),
		},
	}
	service := newReconcileImportService(t, db, first, second, lister, nil)
	firstAttempted, err := service.ReconcileImports(context.Background(), 1)
	if err == nil {
		t.Fatal("prefix-skip first ReconcileImports succeeded after listing failure")
	}
	if firstAttempted != 0 {
		t.Fatalf("first tick attempted=%d, want 0 after inspecting skipped prefix", firstAttempted)
	}
	attempted, err := service.ReconcileImports(context.Background(), 1)
	if err != nil {
		t.Fatalf("prefix-skip ReconcileImports: %v", err)
	}
	if attempted != 1 {
		t.Fatalf("ReconcileImports attempted=%d, want 1 later repo after skipped prefix", attempted)
	}
	assertPendingImportCount(t, db, later.ID, 0)
	assertPendingImportCount(t, db, prefix.ID, 1)
}

func TestReconcileImportsDropsStalePendingCandidates(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repository", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	nativeRepositoryID := strings.Repeat("1", 64)
	nativeSnapshotID := strings.Repeat("2", 64)
	prober := &scriptedProber{observation: testObservation(backupasset.ProviderRestic, provider.NativeResticIdentityPrefix+nativeRepositoryID)}
	lister := &importPointListerSpy{page: provider.NativePointPage{Items: []provider.NativePoint{{
		OpaqueDigest:   nativeSnapshotID,
		CapturedAt:     time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC),
		Semantics:      backupasset.PointNativeSnapshot,
		SourceRevision: nativeSnapshotID,
		Locator:        provider.PointLocator{Native: nativeSnapshotID},
	}}}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: prober, PointLister: lister}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry,
		Now: func() time.Time { return time.Date(2026, 8, 17, 3, 5, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := RequestContext{Actor: backupasset.AuditActor{UserID: 1, Username: "import-admin", Role: "admin"}}
	discovered, err := service.DiscoverImportCandidates(context.Background(), connected.Repository.ID, ImportDiscoveryRequest{Limit: 10}, adminContext)
	if err != nil || discovered.Discovered != 1 {
		t.Fatalf("discover pending import: %+v err=%v", discovered, err)
	}
	lister.page = provider.NativePointPage{}
	repaired, err := service.ReconcileImports(context.Background(), 10)
	if err != nil {
		t.Fatalf("ReconcileImports: %v", err)
	}
	if repaired != 1 {
		t.Fatalf("ReconcileImports attempted=%d, want 1", repaired)
	}
	var remaining int64
	if err := db.Model(&model.BackupRepositoryImportCandidate{}).
		Where("id = ?", discovered.Candidates[0].ID).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("stale pending import candidate remained after reconcile")
	}
}

type cursorPagedImportLister struct {
	pages   map[string]provider.NativePointPage
	cursors []string
}

func (lister *cursorPagedImportLister) ListPoints(_ context.Context, _ provider.ReadSnapshot, request provider.PageRequest) (provider.NativePointPage, error) {
	lister.cursors = append(lister.cursors, request.Cursor)
	page, ok := lister.pages[request.Cursor]
	if !ok {
		return provider.NativePointPage{}, nil
	}
	return page, nil
}

type keyedImportPointLister struct {
	pages   map[string]provider.NativePointPage
	failFor map[string]error
}

func (lister *keyedImportPointLister) ListPoints(_ context.Context, snapshot provider.ReadSnapshot, _ provider.PageRequest) (provider.NativePointPage, error) {
	if err := lister.failFor[snapshot.RepositoryID]; err != nil {
		return provider.NativePointPage{}, err
	}
	return lister.pages[snapshot.RepositoryID], nil
}

func TestReconcileImportsFailsClosedWhenEveryListingFails(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	first := seedPendingResticImportRepo(t, db, strings.Repeat("1", 64), strings.Repeat("a", 64))
	second := seedPendingResticImportRepo(t, db, strings.Repeat("2", 64), strings.Repeat("b", 64))
	lister := &keyedImportPointLister{
		failFor: map[string]error{
			first.repositoryID:  errors.New("FAKE_IMPORT_LISTING_FAILURE_FOR_TEST_ONLY"),
			second.repositoryID: errors.New("FAKE_IMPORT_LISTING_FAILURE_FOR_TEST_ONLY"),
		},
	}
	service := newReconcileImportService(t, db, first, second, lister, nil)

	attempted, err := service.ReconcileImports(context.Background(), 10)
	if err == nil {
		t.Fatal("ReconcileImports skip-all listing failures succeeded")
	}
	if attempted != 0 {
		t.Fatalf("ReconcileImports attempted=%d, want 0", attempted)
	}
	assertPendingImportCount(t, db, first.candidateID, 1)
	assertPendingImportCount(t, db, second.candidateID, 1)
}

func TestReconcileImportsCompleteCycleSweepsPendingOutsideCurrentBatch(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	seed := seedPendingResticImportRepo(t, db, strings.Repeat("1", 64), strings.Repeat("a", 64))
	var first model.BackupRepositoryImportCandidate
	if err := db.First(&first, "id = ?", seed.candidateID).Error; err != nil {
		t.Fatalf("load first pending: %v", err)
	}
	second := first
	second.ID = strings.Repeat("f", 32)
	if second.ID <= first.ID {
		second.ID = strings.Repeat("e", 31) + "f"
	}
	second.SourceFingerprint = strings.Repeat("b", 64)
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("seed second pending: %v", err)
	}
	pagePoint := func(digest string) provider.NativePoint {
		return provider.NativePoint{
			OpaqueDigest:   digest,
			CapturedAt:     time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC),
			Semantics:      backupasset.PointNativeSnapshot,
			SourceRevision: digest,
			Locator:        provider.PointLocator{Native: digest},
		}
	}
	lister := &cursorPagedImportLister{
		pages: map[string]provider.NativePointPage{
			"":       {Items: []provider.NativePoint{pagePoint(strings.Repeat("c", 64))}, NextCursor: "page-2"},
			"page-2": {Items: []provider.NativePoint{pagePoint(strings.Repeat("d", 64))}},
		},
	}
	service := newReconcileImportService(t, db, seed, resticImportRepoSeed{}, lister, nil)

	if _, err := service.ReconcileImports(context.Background(), 1); err != nil {
		t.Fatalf("page-1 ReconcileImports: %v", err)
	}
	assertPendingImportCount(t, db, first.ID, 1)
	assertPendingImportCount(t, db, second.ID, 1)

	if _, err := service.ReconcileImports(context.Background(), 1); err != nil {
		t.Fatalf("complete-cycle ReconcileImports: %v", err)
	}
	if _, err := service.ReconcileImports(context.Background(), 1); err != nil {
		t.Fatalf("stale-sweep ReconcileImports: %v", err)
	}
	assertPendingImportCount(t, db, first.ID, 0)
	assertPendingImportCount(t, db, second.ID, 0)
}

func TestReconcileImportsRefreshesLivePendingOutsideInspectedBatch(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	offPageSnapshot := strings.Repeat("a", 64)
	liveSnapshot := strings.Repeat("b", 64)
	seed := seedPendingResticImportRepo(t, db, strings.Repeat("1", 64), offPageSnapshot)

	livePoint := provider.NativePoint{
		OpaqueDigest:   liveSnapshot,
		CapturedAt:     time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC),
		Semantics:      backupasset.PointNativeSnapshot,
		SourceRevision: liveSnapshot,
		Locator:        provider.PointLocator{Native: liveSnapshot},
	}
	discoverService := newReconcileImportService(t, db, seed, resticImportRepoSeed{}, &importPointListerSpy{
		page: provider.NativePointPage{Items: []provider.NativePoint{livePoint}},
	}, nil)
	admin := RequestContext{Actor: backupasset.AuditActor{UserID: 1, Username: "import-admin", Role: "admin"}}
	discovered, err := discoverService.DiscoverImportCandidates(context.Background(), seed.repositoryID, ImportDiscoveryRequest{Limit: 10}, admin)
	if err != nil || discovered.Discovered != 1 || len(discovered.Candidates) != 1 {
		t.Fatalf("discover live pending: %+v err=%v", discovered, err)
	}

	offPageID := strings.Repeat("0", 32)
	liveID := strings.Repeat("f", 32)
	if err := db.Exec(`UPDATE backup_repository_import_candidates SET id = ? WHERE id = ?`, offPageID, seed.candidateID).Error; err != nil {
		t.Fatalf("pin off-page candidate id: %v", err)
	}
	if err := db.Exec(`UPDATE backup_repository_import_candidates SET id = ? WHERE id = ?`, liveID, discovered.Candidates[0].ID).Error; err != nil {
		t.Fatalf("pin live candidate id: %v", err)
	}

	lister := &cursorPagedImportLister{
		pages: map[string]provider.NativePointPage{
			"": {Items: []provider.NativePoint{livePoint}},
		},
	}
	service := newReconcileImportService(t, db, seed, resticImportRepoSeed{}, lister, nil)
	if _, err := service.ReconcileImports(context.Background(), 1); err != nil {
		t.Fatalf("complete-cycle ReconcileImports: %v", err)
	}
	assertPendingImportCount(t, db, offPageID, 0)
	assertPendingImportCount(t, db, liveID, 1)
}

func TestReconcileImportsDoesNotRelistUntilStaleSweepFinishes(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	offPageSnapshot := strings.Repeat("a", 64)
	liveSnapshot := strings.Repeat("b", 64)
	seed := seedPendingResticImportRepo(t, db, strings.Repeat("1", 64), offPageSnapshot)

	livePoint := provider.NativePoint{
		OpaqueDigest:   liveSnapshot,
		CapturedAt:     time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC),
		Semantics:      backupasset.PointNativeSnapshot,
		SourceRevision: liveSnapshot,
		Locator:        provider.PointLocator{Native: liveSnapshot},
	}
	discoverService := newReconcileImportService(t, db, seed, resticImportRepoSeed{}, &importPointListerSpy{
		page: provider.NativePointPage{Items: []provider.NativePoint{livePoint}},
	}, nil)
	admin := RequestContext{Actor: backupasset.AuditActor{UserID: 1, Username: "import-admin", Role: "admin"}}
	discovered, err := discoverService.DiscoverImportCandidates(context.Background(), seed.repositoryID, ImportDiscoveryRequest{Limit: 10}, admin)
	if err != nil || discovered.Discovered != 1 || len(discovered.Candidates) != 1 {
		t.Fatalf("discover live pending: %+v err=%v", discovered, err)
	}

	var template model.BackupRepositoryImportCandidate
	if err := db.First(&template, "id = ?", seed.candidateID).Error; err != nil {
		t.Fatalf("load stale template: %v", err)
	}
	staleIDs := make([]string, 0, 51)
	for i := 0; i < 51; i++ {
		id := fmt.Sprintf("a%031x", i)
		staleIDs = append(staleIDs, id)
		if i == 0 {
			if err := db.Exec(`UPDATE backup_repository_import_candidates SET id = ? WHERE id = ?`, id, seed.candidateID).Error; err != nil {
				t.Fatalf("pin first stale id: %v", err)
			}
			continue
		}
		row := template
		row.ID = id
		row.SourceFingerprint = fmt.Sprintf("%064x", i+2)
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed extra stale pending %s: %v", id, err)
		}
	}
	liveID := strings.Repeat("f", 32)
	if err := db.Exec(`UPDATE backup_repository_import_candidates SET id = ? WHERE id = ?`, liveID, discovered.Candidates[0].ID).Error; err != nil {
		t.Fatalf("pin live candidate id: %v", err)
	}

	lister := &cursorPagedImportLister{
		pages: map[string]provider.NativePointPage{
			"": {Items: []provider.NativePoint{livePoint}},
		},
	}
	service := newReconcileImportService(t, db, seed, resticImportRepoSeed{}, lister, nil)
	if _, err := service.ReconcileImports(context.Background(), 1); err != nil {
		t.Fatalf("first ReconcileImports: %v", err)
	}
	if len(lister.cursors) != 1 || lister.cursors[0] != "" {
		t.Fatalf("first tick cursors=%v, want [\"\"]", lister.cursors)
	}
	firstStarted, ok := service.importCycleStart(seed.repositoryID)
	if !ok {
		t.Fatal("first tick finished the listing cycle before the stale sweep completed")
	}
	var remainingAfterFirst int64
	if err := db.Model(&model.BackupRepositoryImportCandidate{}).
		Where("repository_id = ? AND review_state = ?", seed.repositoryID, backupasset.ImportReviewPending).
		Count(&remainingAfterFirst).Error; err != nil {
		t.Fatal(err)
	}
	if remainingAfterFirst < 1 {
		t.Fatal("first tick deleted the live refreshed candidate")
	}
	if _, stillSweeping := service.importCycleStart(seed.repositoryID); !stillSweeping && remainingAfterFirst > 0 {
		t.Fatal("first tick finished the listing cycle while pending rows remained")
	}
	assertPendingImportCount(t, db, liveID, 1)

	if _, err := service.ReconcileImports(context.Background(), 10); err != nil {
		t.Fatalf("sweep-only ReconcileImports: %v", err)
	}
	if len(lister.cursors) != 1 {
		t.Fatalf("incomplete sweep re-listed Provider pages: cursors=%v", lister.cursors)
	}
	if started, still := service.importCycleStart(seed.repositoryID); still && !started.Equal(firstStarted) {
		t.Fatalf("cycleStartedAt reset from %s to %s during incomplete sweep", firstStarted, started)
	}

	if _, still := service.importCycleStart(seed.repositoryID); still {
		if _, err := service.ReconcileImports(context.Background(), 10); err != nil {
			t.Fatalf("final sweep ReconcileImports: %v", err)
		}
	}
	if len(lister.cursors) != 1 {
		t.Fatalf("later sweep tick re-listed Provider pages: cursors=%v", lister.cursors)
	}
	assertPendingImportCount(t, db, liveID, 1)
	for _, staleID := range staleIDs {
		assertPendingImportCount(t, db, staleID, 0)
	}
}

func TestReconcileImportsIsolatesListingFailuresAndRepairsSuccessfulRepos(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	success := seedPendingResticImportRepo(t, db, strings.Repeat("1", 64), strings.Repeat("a", 64))
	skipped := seedPendingResticImportRepo(t, db, strings.Repeat("2", 64), strings.Repeat("b", 64))
	lister := &keyedImportPointLister{
		pages: map[string]provider.NativePointPage{
			success.repositoryID: {},
		},
		failFor: map[string]error{
			skipped.repositoryID: errors.New("FAKE_IMPORT_LISTING_FAILURE_FOR_TEST_ONLY"),
		},
	}
	service := newReconcileImportService(t, db, success, skipped, lister, nil)

	attempted, err := service.ReconcileImports(context.Background(), 10)
	if err == nil {
		t.Fatal("mixed listing ReconcileImports succeeded despite a failed repository list")
	}
	if attempted != 1 {
		t.Fatalf("ReconcileImports attempted=%d, want 1 successful repo", attempted)
	}
	assertPendingImportCount(t, db, success.candidateID, 0)
	assertPendingImportCount(t, db, skipped.candidateID, 1)
}

func TestReconcileImportsDoesNotDropPendingOnIdentityMismatch(t *testing.T) {
	db := newRepositoryTestDB(t)
	migrateImportCandidateTestTable(t, db)
	seed := seedPendingResticImportRepo(t, db, strings.Repeat("1", 64), strings.Repeat("a", 64))
	lister := &keyedImportPointLister{pages: map[string]provider.NativePointPage{seed.repositoryID: {}}}
	service := newReconcileImportService(t, db, seed, resticImportRepoSeed{}, lister, func(provider.AccessBinding) (provider.RepositoryObservation, error) {
		return testObservation(backupasset.ProviderRestic, provider.NativeResticIdentityPrefix+strings.Repeat("9", 64)), nil
	})

	attempted, err := service.ReconcileImports(context.Background(), 10)
	if err == nil {
		t.Fatal("identity-mismatch listing succeeded")
	}
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("identity-mismatch error=%v, want ErrConflict", err)
	}
	if attempted != 0 {
		t.Fatalf("identity-mismatch attempted=%d, want 0", attempted)
	}
	assertPendingImportCount(t, db, seed.candidateID, 1)
}

type resticImportRepoSeed struct {
	taskID       uint
	repositoryID string
	candidateID  string
	identity     string
}

func seedPendingResticImportRepo(t *testing.T, db *gorm.DB, identitySuffix, snapshotID string) resticImportRepoSeed {
	t.Helper()
	taskEntity := seedTask(t, db, "restic", "sftp:user@example.invalid:/repository", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	identity := provider.NativeResticIdentityPrefix + identitySuffix
	lister := &importPointListerSpy{page: provider.NativePointPage{Items: []provider.NativePoint{{
		OpaqueDigest:   snapshotID,
		CapturedAt:     time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC),
		Semantics:      backupasset.PointNativeSnapshot,
		SourceRevision: snapshotID,
		Locator:        provider.PointLocator{Native: snapshotID},
	}}}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{
		Prober:      &scriptedProber{observation: testObservation(backupasset.ProviderRestic, identity)},
		PointLister: lister,
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry,
		Now: func() time.Time { return time.Date(2026, 8, 17, 3, 5, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := service.Connect(context.Background(), ConnectRequest{TaskID: taskEntity.ID}, RequestContext{})
	if err != nil {
		t.Fatal(err)
	}
	adminContext := RequestContext{Actor: backupasset.AuditActor{UserID: 1, Username: "import-admin", Role: "admin"}}
	discovered, err := service.DiscoverImportCandidates(context.Background(), connected.Repository.ID, ImportDiscoveryRequest{Limit: 10}, adminContext)
	if err != nil || discovered.Discovered != 1 || len(discovered.Candidates) != 1 {
		t.Fatalf("discover pending import: %+v err=%v", discovered, err)
	}
	return resticImportRepoSeed{
		taskID: taskEntity.ID, repositoryID: connected.Repository.ID,
		candidateID: discovered.Candidates[0].ID, identity: identity,
	}
}

func newReconcileImportService(
	t *testing.T,
	db *gorm.DB,
	first resticImportRepoSeed,
	second resticImportRepoSeed,
	lister provider.PointLister,
	probe func(provider.AccessBinding) (provider.RepositoryObservation, error),
) *Service {
	t.Helper()
	identities := map[uint]string{first.taskID: first.identity}
	if second.taskID != 0 {
		identities[second.taskID] = second.identity
	}
	prober := &scriptedProber{probe: func(binding provider.AccessBinding) (provider.RepositoryObservation, error) {
		if probe != nil {
			return probe(binding)
		}
		identity, ok := identities[binding.TaskID]
		if !ok {
			return provider.RepositoryObservation{}, errors.New("FAKE_IMPORT_PROBE_UNKNOWN_TASK_FOR_TEST_ONLY")
		}
		return testObservation(backupasset.ProviderRestic, identity), nil
	}}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: prober, PointLister: lister}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Dependencies{
		DB: db, Foundation: enabledFoundation(), Registry: registry,
		Now: func() time.Time { return time.Date(2026, 8, 17, 3, 10, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertPendingImportCount(t *testing.T, db *gorm.DB, candidateID string, want int64) {
	t.Helper()
	var remaining int64
	if err := db.Model(&model.BackupRepositoryImportCandidate{}).
		Where("id = ?", candidateID).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != want {
		t.Fatalf("pending import candidate %s count=%d, want %d", candidateID, remaining, want)
	}
}
