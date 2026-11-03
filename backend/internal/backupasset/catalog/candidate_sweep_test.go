package catalog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestCatalogCandidateSweepSQLite(t *testing.T) {
	runCatalogCandidateSweep(t, false, "")
}

func TestCatalogCandidateSweepPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_CATALOG_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_CATALOG_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	runCatalogCandidateSweep(t, true, dsn)
}

func runCatalogCandidateSweep(t *testing.T, postgres bool, dsn string) {
	t.Helper()
	t.Run("starved_prefix", func(t *testing.T) { testSweepStarvedPrefix(t, postgres, dsn) })
	t.Run("page_bounds_and_wrap", func(t *testing.T) { testSweepPageBounds(t, postgres, dsn) })
	t.Run("empty_eligible_page", func(t *testing.T) { testSweepEmptyEligiblePage(t, postgres, dsn) })
	t.Run("deleted_boundaries_and_late_arrivals", func(t *testing.T) { testSweepDeletedBoundaries(t, postgres, dsn) })
	t.Run("restart_and_two_selectors", func(t *testing.T) { testSweepTwoSelectors(t, postgres, dsn) })
	t.Run("retry_and_history_precedence", func(t *testing.T) { testSweepHistoryPrecedence(t, postgres, dsn) })
	t.Run("rollback_and_cancel", func(t *testing.T) { testSweepRollbackAndCancel(t, postgres, dsn) })
	t.Run("revision_ceiling_and_cas", func(t *testing.T) { testSweepRevisionAndCAS(t, postgres, dsn) })
	t.Run("lost_commit_acknowledgement", func(t *testing.T) { testSweepLostCommit(t, postgres, dsn) })
	t.Run("committed_before_dispatch", func(t *testing.T) { testSweepCommittedBeforeDispatch(t, postgres, dsn) })
	t.Run("query_budget", func(t *testing.T) { testSweepQueryBudget(t, postgres, dsn) })
}

type sweepTestFixture struct {
	primary  *gorm.DB
	observer *gorm.DB
	fixture  catalogIndexerFixture
	indexer  *Indexer
	now      time.Time
}

func openSweepTestDB(t *testing.T, postgres bool, dsn string) (*gorm.DB, *gorm.DB) {
	t.Helper()
	if postgres {
		return openCatalogBehaviorPostgres(t, dsn)
	}
	return openCatalogBehaviorSQLite(t)
}

func newSweepTestFixture(t *testing.T, postgres bool, dsn string) sweepTestFixture {
	t.Helper()
	primary, observer := openSweepTestDB(t, postgres, dsn)
	now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
	fixture := seedCatalogBehaviorFixture(t, primary, now, 9100, backupasset.ProviderRsync)
	return sweepTestFixture{primary: primary, observer: observer, fixture: fixture, indexer: fixture.newIndexer(t, fixture.factory()), now: now}
}

func (fixture sweepTestFixture) points(t *testing.T, count int) []model.RecoveryPoint {
	t.Helper()
	if count < 1 {
		t.Fatal("fixture needs at least one point")
	}
	points := make([]model.RecoveryPoint, count)
	points[0] = fixture.fixture.point
	for index := 1; index < count; index++ {
		points[index] = fixture.fixture.point
		points[index].ID = fmt.Sprintf("%032x", 0x100000+index)
		points[index].SourceFingerprint = fmt.Sprintf("%064x", 0x100000+index)
		points[index].CreatedAt = fixture.now.Add(time.Duration(index) * time.Second)
		points[index].UpdatedAt = points[index].CreatedAt
	}
	if count > 1 {
		if err := fixture.primary.CreateInBatches(points[1:], 100).Error; err != nil {
			t.Fatal(err)
		}
	}
	return points
}

func seedSweepGeneration(t *testing.T, db *gorm.DB, point model.RecoveryPoint, id, sequence int, state GenerationState, active bool, options ...func(*model.CatalogGeneration)) model.CatalogGeneration {
	t.Helper()
	finished := point.CreatedAt
	generation := model.CatalogGeneration{
		ID: fmt.Sprintf("%032x", 0x700000+id), RecoveryPointID: point.ID, Generation: sequence,
		State: string(state), IsActive: active, SourceFingerprint: point.SourceFingerprint,
		ExpectedDigest: point.ManifestDigest, ExpectedEntryCount: point.EntryCount,
		StartedAt: point.CreatedAt, FinishedAt: &finished, CreatedAt: point.CreatedAt, UpdatedAt: point.CreatedAt,
	}
	for _, option := range options {
		option(&generation)
	}
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	return generation
}

func sweepIDs(candidates []BuildCandidate) []string {
	ids := make([]string, len(candidates))
	for index := range candidates {
		ids[index] = candidates[index].RecoveryPointID
	}
	return ids
}

func sweepPage(t *testing.T, indexer *Indexer, limit int, now time.Time) []BuildCandidate {
	t.Helper()
	candidates, err := indexer.ListCandidates(context.Background(), limit, now, catalogCandidateTestConfig())
	if err != nil {
		t.Fatalf("list candidate page: %v", err)
	}
	return candidates
}

func sweepState(t *testing.T, db *gorm.DB) model.CatalogCandidateSweep {
	t.Helper()
	var state model.CatalogCandidateSweep
	if err := db.Where("id = ?", 1).Take(&state).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func requireSweepIdle(t *testing.T, state model.CatalogCandidateSweep, revision int64) {
	t.Helper()
	if state.Revision != revision || state.AfterCreatedAt != nil || state.AfterPointID != nil || state.HighWaterCreatedAt != nil || state.HighWaterPointID != nil {
		t.Fatalf("sweep state=%+v, want idle revision %d", state, revision)
	}
}

func testSweepStarvedPrefix(t *testing.T, postgres bool, dsn string) {
	fixture := newSweepTestFixture(t, postgres, dsn)
	points := fixture.points(t, 201)
	for index, point := range points {
		switch index % 4 {
		case 0:
			seedSweepGeneration(t, fixture.primary, point, index, 1, GenerationComplete, true)
		case 1:
			seedSweepGeneration(t, fixture.primary, point, index, 1, GenerationBuilding, false, func(row *model.CatalogGeneration) { row.FinishedAt = nil })
		case 2:
			seedSweepGeneration(t, fixture.primary, point, index, 1, GenerationFailed, false, func(row *model.CatalogGeneration) { row.ErrorCode = "catalog_proof_mismatch" })
		case 3:
			seedSweepGeneration(t, fixture.primary, point, index, 1, GenerationFailed, false, func(row *model.CatalogGeneration) {
				row.ErrorCode = "catalog_provider_unavailable"
				row.FinishedAt = &fixture.now
			})
		}
	}
	type upgrade struct {
		fixture catalogIndexerFixture
		old     model.CatalogGeneration
		entry   model.CatalogEntry
	}
	upgrades := make([]upgrade, 0, 2)
	for index, strength := range []FingerprintStrength{FingerprintNone, FingerprintWeak} {
		target := seedCatalogBehaviorFixture(t, fixture.primary, fixture.now.Add(time.Duration(300+index)*time.Second), 9200+index, backupasset.ProviderRsync)
		target.point.EntryCount = 1
		target.manifest.EntryCount = 1
		if err := fixture.primary.Model(&model.RecoveryPoint{}).Where("id = ?", target.point.ID).UpdateColumn("entry_count", 1).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.primary.Model(&model.RecoveryPointManifest{}).Where("id = ?", target.manifest.ID).UpdateColumn("entry_count", 1).Error; err != nil {
			t.Fatal(err)
		}
		old := seedSweepGeneration(t, fixture.primary, target.point, 300+index, 1, GenerationComplete, true)
		entry := seedCatalogServiceEntry(t, fixture.primary, old, fmt.Sprintf("%064x", 500+index), nil, "file", "file", backupasset.CatalogEntryFile, 7, fixture.now)
		if err := fixture.primary.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", old.ID, entry.EntryID).
			Updates(map[string]any{"fingerprint": "", "fingerprint_strength": string(strength)}).Error; err != nil {
			t.Fatal(err)
		}
		upgrades = append(upgrades, upgrade{fixture: target, old: old, entry: entry})
	}
	first := upgrades[0]
	job := model.BackupAssetProcessingJob{
		ID: fmt.Sprintf("%032x", 0x800001), WorkKey: strings.Repeat("6", 64), DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`),
		RecoveryPointID: first.fixture.point.ID, CatalogGenerationID: first.old.ID, EntryID: first.entry.EntryID,
		SourceFingerprint: first.fixture.point.SourceFingerprint, ProviderCapabilityRevision: int64(first.fixture.point.CapabilityRevision),
		Capability: "malware.scan", CapabilitySchema: "malware.scan.v1", PipelineFingerprint: "historical-pipeline",
		OutputProfile: "signature_scan_v1", SecurityPolicyRevision: "policy-v1", PriorityClass: "background",
		State: "queued", TransitionRevision: 1, IsCurrent: true, QueuedAt: fixture.now,
		AbsoluteDeadline: fixture.now.Add(time.Hour), CreatedAt: fixture.now, UpdatedAt: fixture.now, Version: 1,
	}
	if err := fixture.primary.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	for index := range points {
		if candidates := sweepPage(t, fixture.indexer, 1, fixture.now); len(candidates) != 0 {
			t.Fatalf("prefix page %d unexpectedly eligible: %+v", index, candidates)
		}
	}
	for _, target := range upgrades {
		candidates := sweepPage(t, fixture.indexer, 1, fixture.now)
		if got := sweepIDs(candidates); !slices.Equal(got, []string{target.fixture.point.ID}) {
			t.Fatalf("upgrade page IDs=%v want %s", got, target.fixture.point.ID)
		}
		factory := target.fixture.factory(sealedCatalogRecordForTest(t, "file", "", "file", backupasset.CatalogEntryFile, 7))
		indexer := target.fixture.newIndexer(t, factory)
		built, err := indexer.Build(context.Background(), BuildRequest{RepositoryID: target.fixture.point.RepositoryID, RecoveryPointID: target.fixture.point.ID})
		if err != nil || built.ID == target.old.ID || !built.IsActive || built.State != string(GenerationComplete) {
			t.Fatalf("build upgrade=%+v error=%v", built, err)
		}
		assertCatalogActiveGeneration(t, fixture.observer, target.fixture.point.ID, built.ID)
		var old model.CatalogGeneration
		var entry model.CatalogEntry
		if err := fixture.observer.First(&old, "id = ?", target.old.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.observer.First(&entry, "generation_id = ? AND entry_id = ?", target.old.ID, target.entry.EntryID).Error; err != nil {
			t.Fatal(err)
		}
		if old.IsActive || old.State != string(GenerationSuperseded) || entry.FingerprintStrength == string(FingerprintStrong) {
			t.Fatalf("historical generation=%+v entry=%+v", old, entry)
		}
	}
	var persisted model.BackupAssetProcessingJob
	if err := fixture.observer.First(&persisted, "id = ?", job.ID).Error; err != nil || persisted.CatalogGenerationID != first.old.ID || persisted.EntryID != first.entry.EntryID {
		t.Fatalf("historical reference=%+v error=%v", persisted, err)
	}
}

func testSweepPageBounds(t *testing.T, postgres bool, dsn string) {
	t.Run("empty", func(t *testing.T) {
		primary, _ := openSweepTestDB(t, postgres, dsn)
		indexer := &Indexer{db: primary}
		now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
		if candidates := sweepPage(t, indexer, 1, now); len(candidates) != 0 {
			t.Fatalf("empty database candidates=%+v", candidates)
		}
		if candidates, err := indexer.ListCandidates(nil, 1, now, catalogCandidateTestConfig()); err != nil || len(candidates) != 0 { //nolint:staticcheck // Exercise the documented nil-context fallback.
			t.Fatalf("nil context candidates=%+v error=%v", candidates, err)
		}
		requireSweepIdle(t, sweepState(t, primary), 1)
	})
	t.Run("maximum_and_short_page", func(t *testing.T) {
		fixture := newSweepTestFixture(t, postgres, dsn)
		points := fixture.points(t, 1001)
		for _, limit := range []int{0, MaxCandidatePageSize + 1} {
			candidates, err := fixture.indexer.ListCandidates(context.Background(), limit, fixture.now, catalogCandidateTestConfig())
			if !errors.Is(err, backupasset.ErrInvalidState) || candidates != nil {
				t.Fatalf("limit=%d candidates=%+v error=%v", limit, candidates, err)
			}
		}
		invalidConfig := catalogCandidateTestConfig()
		invalidConfig.ReconcileInterval = 0
		if candidates, err := fixture.indexer.ListCandidates(context.Background(), 1, fixture.now, invalidConfig); !errors.Is(err, backupasset.ErrInvalidState) || candidates != nil {
			t.Fatalf("invalid config candidates=%+v error=%v", candidates, err)
		}
		if candidates, err := fixture.indexer.ListCandidates(context.Background(), 1, time.Time{}, catalogCandidateTestConfig()); !errors.Is(err, backupasset.ErrInvalidState) || candidates != nil {
			t.Fatalf("zero clock candidates=%+v error=%v", candidates, err)
		}
		candidates := sweepPage(t, fixture.indexer, MaxCandidatePageSize, fixture.now)
		if len(candidates) != MaxCandidatePageSize {
			t.Fatalf("first page returned %d candidates", len(candidates))
		}
		for index, candidate := range candidates {
			if candidate.RecoveryPointID != points[index].ID {
				t.Fatalf("page[%d]=%s want %s", index, candidate.RecoveryPointID, points[index].ID)
			}
		}
		state := sweepState(t, fixture.observer)
		if state.Revision != 2 || state.AfterPointID == nil || *state.AfterPointID != points[999].ID || state.HighWaterPointID == nil || *state.HighWaterPointID != points[1000].ID {
			t.Fatalf("first page cursor=%+v", state)
		}
		if got := sweepIDs(sweepPage(t, fixture.indexer, MaxCandidatePageSize, fixture.now)); !slices.Equal(got, []string{points[1000].ID}) {
			t.Fatalf("short page IDs=%v", got)
		}
		requireSweepIdle(t, sweepState(t, fixture.observer), 3)
		if got := sweepIDs(sweepPage(t, fixture.indexer, 1, fixture.now)); !slices.Equal(got, []string{points[0].ID}) {
			t.Fatalf("wrapped first page IDs=%v", got)
		}
	})
	t.Run("full_final_page", func(t *testing.T) {
		fixture := newSweepTestFixture(t, postgres, dsn)
		points := fixture.points(t, 2)
		for _, point := range points {
			if got := sweepIDs(sweepPage(t, fixture.indexer, 1, fixture.now)); !slices.Equal(got, []string{point.ID}) {
				t.Fatalf("one-point page=%v want %s", got, point.ID)
			}
		}
		requireSweepIdle(t, sweepState(t, fixture.observer), 3)
	})
	t.Run("same_created_at", func(t *testing.T) {
		fixture := newSweepTestFixture(t, postgres, dsn)
		points := fixture.points(t, 3)
		if err := fixture.primary.Model(&model.RecoveryPoint{}).Where("repository_id = ?", points[0].RepositoryID).
			UpdateColumn("created_at", fixture.now).Error; err != nil {
			t.Fatal(err)
		}
		var ordered []string
		if err := fixture.observer.Table("recovery_points").Order("created_at ASC, id ASC").Pluck("id", &ordered).Error; err != nil {
			t.Fatal(err)
		}
		for _, id := range ordered {
			if got := sweepIDs(sweepPage(t, fixture.indexer, 1, fixture.now)); !slices.Equal(got, []string{id}) {
				t.Fatalf("same-time page=%v want %s", got, id)
			}
		}
		requireSweepIdle(t, sweepState(t, fixture.observer), 4)
	})
}

func testSweepEmptyEligiblePage(t *testing.T, postgres bool, dsn string) {
	fixture := newSweepTestFixture(t, postgres, dsn)
	points := fixture.points(t, 2)
	seedSweepGeneration(t, fixture.primary, points[0], 1, 1, GenerationComplete, true)
	if got := sweepPage(t, fixture.indexer, 1, fixture.now); len(got) != 0 {
		t.Fatalf("first page should be ineligible: %+v", got)
	}
	if got := sweepIDs(sweepPage(t, fixture.indexer, 1, fixture.now)); !slices.Equal(got, []string{points[1].ID}) {
		t.Fatalf("later page=%v want %s", got, points[1].ID)
	}
}

func testSweepDeletedBoundaries(t *testing.T, postgres bool, dsn string) {
	fixture := newSweepTestFixture(t, postgres, dsn)
	points := fixture.points(t, 5)
	active := seedSweepGeneration(t, fixture.primary, points[0], 1, 1, GenerationComplete, true)
	if got := sweepPage(t, fixture.indexer, 1, fixture.now); len(got) != 0 {
		t.Fatalf("first page=%+v", got)
	}
	if got := sweepIDs(sweepPage(t, fixture.indexer, 1, fixture.now)); !slices.Equal(got, []string{points[1].ID}) {
		t.Fatalf("second page=%v", got)
	}
	if err := fixture.primary.Where("id IN ?", []string{points[1].ID, points[4].ID}).Delete(&model.RecoveryPoint{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.primary.Model(&model.CatalogGeneration{}).Where("id = ?", active.ID).UpdateColumn("expected_digest", strings.Repeat("f", 64)).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.primary.Model(&model.RecoveryPoint{}).Where("id = ?", points[2].ID).UpdateColumn("created_at", fixture.now.Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	before, after := points[3], points[3]
	before.ID, before.CreatedAt = fmt.Sprintf("%032x", 0x200001), fixture.now.Add(-2*time.Minute)
	after.ID, after.CreatedAt = fmt.Sprintf("%032x", 0x200002), fixture.now.Add(10*time.Minute)
	before.SourceFingerprint = fmt.Sprintf("%064x", 0x200001)
	after.SourceFingerprint = fmt.Sprintf("%064x", 0x200002)
	if err := fixture.primary.Create(&[]model.RecoveryPoint{before, after}).Error; err != nil {
		t.Fatal(err)
	}
	if got := sweepIDs(sweepPage(t, fixture.indexer, 2, fixture.now)); !slices.Equal(got, []string{points[3].ID}) {
		t.Fatalf("continued window IDs=%v", got)
	}
	requireSweepIdle(t, sweepState(t, fixture.observer), 4)
	want := []string{before.ID, points[2].ID, points[0].ID, points[3].ID, after.ID}
	if got := sweepIDs(sweepPage(t, fixture.indexer, 100, fixture.now)); !slices.Equal(got, want) {
		t.Fatalf("next window IDs=%v want=%v", got, want)
	}
	if !postgres {
		t.Run("sqlite_raw_datetime", func(t *testing.T) { testSweepSQLiteRawDatetime(t, dsn) })
	}
}

func testSweepSQLiteRawDatetime(t *testing.T, dsn string) {
	fixture := newSweepTestFixture(t, false, dsn)
	points := fixture.points(t, 5)
	values := []string{
		"2026-07-17 11:00:00+00:00", "2026-07-17 11:00:00+00:00", "2026-07-17 11:00:00Z",
		"2026-07-17T11:00:00+02:00", "2026-07-17T11:00:00Z",
	}
	for index := range points {
		if err := fixture.primary.Exec("UPDATE recovery_points SET created_at = ? WHERE id = ?", values[index], points[index].ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	var expected []string
	if err := fixture.observer.Table("recovery_points").Order("created_at ASC, id ASC").Pluck("id", &expected).Error; err != nil {
		t.Fatal(err)
	}
	rawByID := make(map[string]string, len(points))
	for index, point := range points {
		rawByID[point.ID] = values[index]
	}
	for index, id := range expected {
		if got := sweepIDs(sweepPage(t, fixture.indexer, 1, fixture.now)); !slices.Equal(got, []string{id}) {
			t.Fatalf("raw timestamp page %d IDs=%v want %s", index, got, id)
		}
		if index == len(expected)-1 {
			break
		}
		var raw struct{ AfterValue, HighValue string }
		if err := fixture.observer.Raw(`SELECT CAST(after_created_at AS TEXT) AS after_value, CAST(high_water_created_at AS TEXT) AS high_value FROM catalog_candidate_sweeps WHERE id = 1`).Scan(&raw).Error; err != nil {
			t.Fatal(err)
		}
		if raw.AfterValue != rawByID[id] || raw.HighValue != rawByID[expected[len(expected)-1]] {
			t.Fatalf("raw boundaries=%+v want after=%q high=%q", raw, rawByID[id], rawByID[expected[len(expected)-1]])
		}
	}
	requireSweepIdle(t, sweepState(t, fixture.observer), int64(len(expected)+1))
}

func testSweepHistoryPrecedence(t *testing.T, postgres bool, dsn string) {
	fixture := newSweepTestFixture(t, postgres, dsn)
	points := fixture.points(t, 18)
	seedSweepGeneration(t, fixture.primary, points[1], 100, 1, GenerationBuilding, false, func(row *model.CatalogGeneration) { row.FinishedAt = nil })
	seedSweepGeneration(t, fixture.primary, points[2], 200, 1, GenerationFailed, false, func(row *model.CatalogGeneration) {
		row.SourceFingerprint = strings.Repeat("f", 64)
		row.ErrorCode = "catalog_proof_mismatch"
	})
	seedSweepGeneration(t, fixture.primary, points[3], 300, 1, GenerationFailed, false, func(row *model.CatalogGeneration) { row.ErrorCode = "catalog_proof_mismatch" })
	seedSweepGeneration(t, fixture.primary, points[4], 400, 1, GenerationPartial, false, func(row *model.CatalogGeneration) { row.FinishedAt = nil })
	retry := seedSweepGeneration(t, fixture.primary, points[5], 500, 1, GenerationFailed, false, func(row *model.CatalogGeneration) {
		row.ErrorCode, row.FinishedAt = "catalog_provider_unavailable", &fixture.now
	})
	seedSweepGeneration(t, fixture.primary, points[6], 600, 1, GenerationComplete, true, func(row *model.CatalogGeneration) { row.ExpectedDigest = strings.Repeat("f", 64) })
	seedSweepGeneration(t, fixture.primary, points[6], 601, 2, GenerationBuilding, false, func(row *model.CatalogGeneration) { row.FinishedAt = nil })
	seedSweepGeneration(t, fixture.primary, points[7], 700, 1, GenerationComplete, true, func(row *model.CatalogGeneration) { row.ExpectedEntryCount++ })
	seedSweepGeneration(t, fixture.primary, points[7], 701, 2, GenerationFailed, false, func(row *model.CatalogGeneration) { row.ErrorCode = "catalog_proof_mismatch" })
	seedSweepGeneration(t, fixture.primary, points[8], 800, 1, GenerationComplete, true)
	seedSweepGeneration(t, fixture.primary, points[8], 801, 2, GenerationBuilding, false, func(row *model.CatalogGeneration) { row.FinishedAt = nil })
	for _, index := range []int{9, 10, 15, 16, 17} {
		points[index].EntryCount = 1
		if err := fixture.primary.Model(&model.RecoveryPoint{}).Where("id = ?", points[index].ID).UpdateColumn("entry_count", 1).Error; err != nil {
			t.Fatal(err)
		}
		active := seedSweepGeneration(t, fixture.primary, points[index], index*100, 1, GenerationComplete, true)
		entry := seedCatalogServiceEntry(t, fixture.primary, active, fmt.Sprintf("%064x", 900+index), nil, "file", "file", backupasset.CatalogEntryFile, 7, fixture.now)
		if err := fixture.primary.Model(&model.CatalogEntry{}).Where("generation_id = ? AND entry_id = ?", active.ID, entry.EntryID).
			UpdateColumn("fingerprint_strength", string(FingerprintWeak)).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedSweepGeneration(t, fixture.primary, points[9], 901, 2, GenerationBuilding, false, func(row *model.CatalogGeneration) { row.FinishedAt = nil })
	weakRetry := seedSweepGeneration(t, fixture.primary, points[10], 1001, 2, GenerationPartial, false, func(row *model.CatalogGeneration) {
		row.ErrorCode, row.FinishedAt = "catalog_provider_unavailable", &fixture.now
	})
	for _, test := range []struct{ point, count int }{{11, 12}, {12, 13}} {
		for sequence := 1; sequence <= test.count; sequence++ {
			state, active := GenerationSuperseded, false
			if sequence == 1 {
				state, active = GenerationComplete, true
			}
			seedSweepGeneration(t, fixture.primary, points[test.point], test.point*100+sequence, sequence, state, active)
		}
	}
	seedSweepGeneration(t, fixture.primary, points[13], 1301, 1, GenerationFailed, false, func(row *model.CatalogGeneration) { row.ErrorCode = "catalog_provider_unavailable" })
	seedSweepGeneration(t, fixture.primary, points[13], 1302, 2, GenerationSuperseded, false)
	seedSweepGeneration(t, fixture.primary, points[13], 1303, 3, GenerationFailed, false, func(row *model.CatalogGeneration) { row.ErrorCode = "catalog_provider_unavailable" })
	interrupted := seedSweepGeneration(t, fixture.primary, points[13], 1304, 4, GenerationPartial, false, func(row *model.CatalogGeneration) {
		row.ErrorCode, row.FinishedAt = "catalog_provider_unavailable", &fixture.now
	})
	seedSweepGeneration(t, fixture.primary, points[14], 1400, 1, GenerationBuilding, false, func(row *model.CatalogGeneration) {
		row.SourceFingerprint = strings.Repeat("f", 64)
		row.FinishedAt = nil
	})
	seedSweepGeneration(t, fixture.primary, points[15], 1501, 2, GenerationFailed, false, func(row *model.CatalogGeneration) { row.ErrorCode = "catalog_proof_mismatch" })
	seedSweepGeneration(t, fixture.primary, points[16], 1601, 2, GenerationPartial, false, func(row *model.CatalogGeneration) { row.FinishedAt = nil })
	seedSweepGeneration(t, fixture.primary, points[17], 1701, 2, GenerationBuilding, false, func(row *model.CatalogGeneration) {
		row.SourceFingerprint = strings.Repeat("f", 64)
		row.FinishedAt = nil
	})

	mutable := seedCatalogBehaviorFixture(t, fixture.primary, fixture.now.Add(time.Minute), 9500, backupasset.ProviderRsync)
	if err := fixture.primary.Model(&model.BackupRepository{}).Where("id = ?", mutable.point.RepositoryID).
		Updates(map[string]any{"version_mode": string(backupasset.VersionMutableHead), "immutability_level": string(backupasset.ImmutabilityMutable)}).Error; err != nil {
		t.Fatal(err)
	}
	mutable.point.Semantics = string(backupasset.PointMutableHead)
	mutable.point.State = string(backupasset.RecoveryPointObserved)
	if err := fixture.primary.Model(&model.RecoveryPoint{}).Where("id = ?", mutable.point.ID).
		Updates(map[string]any{"semantics": mutable.point.Semantics, "state": mutable.point.State, "immutability_level": string(backupasset.ImmutabilityMutable), "observed_at": fixture.now}).Error; err != nil {
		t.Fatal(err)
	}
	seedSweepGeneration(t, fixture.primary, mutable.point, 2500, 1, GenerationComplete, true)
	mutableRetry := seedSweepGeneration(t, fixture.primary, mutable.point, 2501, 2, GenerationFailed, false, func(row *model.CatalogGeneration) {
		row.ErrorCode, row.FinishedAt = "catalog_source_changed", &fixture.now
	})

	contains := func(at time.Time, config backupasset.CatalogConfig) map[string]bool {
		t.Helper()
		candidates, err := fixture.indexer.ListCandidates(context.Background(), 1000, at, config)
		if err != nil {
			t.Fatal(err)
		}
		found := make(map[string]bool, len(candidates))
		for _, candidate := range candidates {
			found[candidate.RecoveryPointID] = true
		}
		return found
	}
	config := catalogCandidateTestConfig()
	atStart := contains(fixture.now, config)
	for index, want := range []bool{true, false, true, false, false, false, true, true, false, false, false, false, true, false, false, false, false, true} {
		if atStart[points[index].ID] != want {
			t.Fatalf("point %d eligibility=%t want=%t", index, atStart[points[index].ID], want)
		}
	}
	if atStart[mutable.point.ID] {
		t.Fatal("mutable catalog_source_changed bypassed backoff")
	}
	for _, test := range []struct {
		point      model.RecoveryPoint
		generation model.CatalogGeneration
		failures   int
	}{
		{points[5], retry, 1}, {points[10], weakRetry, 1}, {points[13], interrupted, 2}, {mutable.point, mutableRetry, 1},
	} {
		due := fixture.now.Add(RetryDelay(config, test.failures, test.point.ID, test.generation.ID))
		if contains(due.Add(-time.Nanosecond), config)[test.point.ID] || !contains(due, config)[test.point.ID] {
			t.Fatalf("retry boundary for point %s at %s", test.point.ID, due)
		}
	}
	changed := config
	changed.ReconcileInterval = time.Minute
	newDue := fixture.now.Add(RetryDelay(changed, 1, points[5].ID, retry.ID))
	if contains(newDue, config)[points[5].ID] || !contains(newDue, changed)[points[5].ID] {
		t.Fatal("configuration change did not change the retry decision")
	}
}

var errSweepInjected = errors.New("FAKE_CATALOG_SWEEP_FAILURE_FOR_TEST_ONLY")

type sweepStatement struct {
	query string
	args  []any
}

type sweepProbe struct {
	mu              sync.Mutex
	queries         []sweepStatement
	execs           []sweepStatement
	failQuery       int
	failExec        bool
	zeroCAS         bool
	deleteBeforeCAS string
	postgres        bool
	cancelOnExec    context.CancelFunc
	cancelOnCommit  context.CancelFunc
	loseCommit      bool
	failCommit      bool
	commitCalled    bool
	pid             chan<- int
	held            chan<- struct{}
	release         <-chan struct{}
}

type sweepProbePool struct {
	gorm.ConnPool
	probe *sweepProbe
}

func (pool sweepProbePool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	var connection gorm.ConnPool
	var err error
	if beginner, ok := pool.ConnPool.(gorm.ConnPoolBeginner); ok {
		connection, err = beginner.BeginTx(ctx, options)
	} else {
		connection, err = pool.ConnPool.(gorm.TxBeginner).BeginTx(ctx, options)
	}
	if err != nil {
		return nil, err
	}
	return &sweepProbeTx{ConnPool: connection, committer: connection.(gorm.TxCommitter), probe: pool.probe}, nil
}

type sweepProbeTx struct {
	gorm.ConnPool
	committer gorm.TxCommitter
	probe     *sweepProbe
}

func (tx *sweepProbeTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.probe.mu.Lock()
	tx.probe.queries = append(tx.probe.queries, sweepStatement{query: query, args: append([]any(nil), args...)})
	ordinal := len(tx.probe.queries)
	fail := tx.probe.failQuery == ordinal
	tx.probe.mu.Unlock()
	if fail {
		return nil, errSweepInjected
	}
	if ordinal == 1 && tx.probe.pid != nil {
		var pid int
		if err := tx.ConnPool.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			return nil, err
		}
		select {
		case tx.probe.pid <- pid:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if ordinal == 2 && tx.probe.held != nil {
		// The first query has been consumed, so PostgreSQL holds the actual row
		// lock and SQLite holds its immediate transaction before this signal.
		select {
		case tx.probe.held <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case <-tx.probe.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return tx.ConnPool.QueryContext(ctx, query, args...)
}

func (tx *sweepProbeTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.probe.mu.Lock()
	tx.probe.execs = append(tx.probe.execs, sweepStatement{query: query, args: append([]any(nil), args...)})
	zero := tx.probe.zeroCAS
	tx.probe.mu.Unlock()
	if tx.probe.failExec {
		return nil, errSweepInjected
	}
	if zero {
		return driver.RowsAffected(0), nil
	}
	if tx.probe.deleteBeforeCAS != "" {
		deleteSQL := "DELETE FROM recovery_points WHERE id = ?"
		if tx.probe.postgres {
			deleteSQL = "DELETE FROM recovery_points WHERE id = $1"
		}
		if _, err := tx.ConnPool.ExecContext(ctx, deleteSQL, tx.probe.deleteBeforeCAS); err != nil {
			return nil, err
		}
	}
	result, err := tx.ConnPool.ExecContext(ctx, query, args...)
	if err == nil && tx.probe.cancelOnExec != nil {
		tx.probe.cancelOnExec()
	}
	return result, err
}

func (tx *sweepProbeTx) Commit() error {
	tx.probe.mu.Lock()
	tx.probe.commitCalled = true
	tx.probe.mu.Unlock()
	if tx.probe.failCommit {
		return errSweepInjected
	}
	if err := tx.committer.Commit(); err != nil {
		return err
	}
	if tx.probe.cancelOnCommit != nil {
		tx.probe.cancelOnCommit()
	}
	if tx.probe.loseCommit {
		return errSweepInjected
	}
	return nil
}

func (tx *sweepProbeTx) Rollback() error { return tx.committer.Rollback() }

func sweepProbeIndexer(db *gorm.DB, probe *sweepProbe) *Indexer {
	wrapped := db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	wrapped.Statement.ConnPool = sweepProbePool{ConnPool: wrapped.Statement.ConnPool, probe: probe}
	return &Indexer{db: wrapped}
}

func (probe *sweepProbe) reset() {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.queries = nil
	probe.execs = nil
	probe.commitCalled = false
}

func (probe *sweepProbe) statements() ([]sweepStatement, []sweepStatement, bool) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return append([]sweepStatement(nil), probe.queries...), append([]sweepStatement(nil), probe.execs...), probe.commitCalled
}

type sweepCallResult struct {
	candidates []BuildCandidate
	err        error
}

func testSweepTwoSelectors(t *testing.T, postgres bool, dsn string) {
	fixture := newSweepTestFixture(t, postgres, dsn)
	points := fixture.points(t, 2)
	if !postgres {
		sqlDB, err := fixture.observer.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		if err := fixture.observer.Exec("PRAGMA busy_timeout = 0").Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	held, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	holderPID, contenderPID := make(chan int, 1), make(chan int, 1)
	holderProbe := &sweepProbe{held: held, release: release}
	if postgres {
		holderProbe.pid = holderPID
	}
	holder := sweepProbeIndexer(fixture.primary, holderProbe)
	holderDone := make(chan sweepCallResult, 1)
	go func() {
		candidates, err := holder.ListCandidates(ctx, 1, fixture.now, catalogCandidateTestConfig())
		holderDone <- sweepCallResult{candidates, err}
	}()
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("first selector did not hold the singleton transaction")
	}
	var firstPID int
	if postgres {
		firstPID = <-holderPID
	}
	contenderProbe := &sweepProbe{}
	if postgres {
		contenderProbe.pid = contenderPID
	}
	contender := sweepProbeIndexer(fixture.observer, contenderProbe)
	contenderDone := make(chan sweepCallResult, 1)
	go func() {
		candidates, err := contender.ListCandidates(ctx, 1, fixture.now, catalogCandidateTestConfig())
		contenderDone <- sweepCallResult{candidates, err}
	}()
	var second sweepCallResult
	if postgres {
		var secondPID int
		select {
		case secondPID = <-contenderPID:
		case <-ctx.Done():
			t.Fatal("second selector did not reach the row-lock query")
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var blocked bool
			if err := fixture.primary.Raw("SELECT ? = ANY(pg_blocking_pids(?))", firstPID, secondPID).Scan(&blocked).Error; err != nil {
				t.Fatal(err)
			}
			if blocked {
				break
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				t.Fatal("second PostgreSQL selector did not wait on the first selector")
			}
		}
	} else {
		select {
		case second = <-contenderDone:
		case <-ctx.Done():
			t.Fatal("second SQLite selector did not report lock contention")
		}
		if second.err == nil || second.candidates != nil {
			t.Fatalf("contended SQLite selector candidates=%+v error=%v", second.candidates, second.err)
		}
	}
	unblock()
	first := <-holderDone
	if first.err != nil || !slices.Equal(sweepIDs(first.candidates), []string{points[0].ID}) {
		t.Fatalf("first selector candidates=%+v error=%v", first.candidates, first.err)
	}
	if postgres {
		second = <-contenderDone
	}
	if second.err != nil {
		if second.candidates != nil {
			t.Fatalf("failed second selector returned candidates=%+v", second.candidates)
		}
		restarted := &Indexer{db: fixture.observer}
		if got := sweepIDs(sweepPage(t, restarted, 1, fixture.now)); !slices.Equal(got, []string{points[1].ID}) {
			t.Fatalf("restart after contention IDs=%v", got)
		}
	} else if got := sweepIDs(second.candidates); !slices.Equal(got, []string{points[1].ID}) {
		t.Fatalf("second selector IDs=%v", got)
	}
	requireSweepIdle(t, sweepState(t, fixture.observer), 3)
}

func testSweepRollbackAndCancel(t *testing.T, postgres bool, dsn string) {
	for _, test := range []struct {
		name       string
		failQuery  int
		failExec   bool
		failCommit bool
	}{
		{"lock", 1, false, false}, {"high_water", 2, false, false},
		{"page", 3, false, false}, {"history", 4, false, false},
		{"cas", 0, true, false}, {"commit", 0, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSweepTestFixture(t, postgres, dsn)
			fixture.points(t, 2)
			probe := &sweepProbe{failQuery: test.failQuery, failExec: test.failExec, failCommit: test.failCommit}
			indexer := sweepProbeIndexer(fixture.primary, probe)
			candidates, err := indexer.ListCandidates(context.Background(), 1, fixture.now, catalogCandidateTestConfig())
			if !errors.Is(err, errSweepInjected) || candidates != nil {
				t.Fatalf("injected failure candidates=%+v error=%v", candidates, err)
			}
			requireSweepIdle(t, sweepState(t, fixture.observer), 1)
		})
	}
	t.Run("cancel_before_commit", func(t *testing.T) {
		fixture := newSweepTestFixture(t, postgres, dsn)
		fixture.points(t, 2)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		probe := &sweepProbe{cancelOnExec: cancel}
		candidates, err := sweepProbeIndexer(fixture.primary, probe).ListCandidates(ctx, 1, fixture.now, catalogCandidateTestConfig())
		if !errors.Is(err, context.Canceled) || candidates != nil {
			t.Fatalf("canceled transaction candidates=%+v error=%v", candidates, err)
		}
		requireSweepIdle(t, sweepState(t, fixture.observer), 1)
	})
	t.Run("cancel_after_commit", func(t *testing.T) {
		fixture := newSweepTestFixture(t, postgres, dsn)
		points := fixture.points(t, 2)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		probe := &sweepProbe{cancelOnCommit: cancel}
		candidates, err := sweepProbeIndexer(fixture.primary, probe).ListCandidates(ctx, 1, fixture.now, catalogCandidateTestConfig())
		if !errors.Is(err, context.Canceled) || candidates != nil {
			t.Fatalf("post-commit cancellation candidates=%+v error=%v", candidates, err)
		}
		state := sweepState(t, fixture.observer)
		if state.Revision != 2 || state.AfterPointID == nil || *state.AfterPointID != points[0].ID {
			t.Fatalf("successful commit was not visible after cancellation: %+v", state)
		}
	})
}

func testSweepRevisionAndCAS(t *testing.T, postgres bool, dsn string) {
	t.Run("revision_ceiling", func(t *testing.T) {
		fixture := newSweepTestFixture(t, postgres, dsn)
		points := fixture.points(t, 2)
		if err := fixture.primary.Model(&model.CatalogCandidateSweep{}).Where("id = ?", 1).UpdateColumn("revision", int64(math.MaxInt64-1)).Error; err != nil {
			t.Fatal(err)
		}
		if got := sweepIDs(sweepPage(t, fixture.indexer, 1, fixture.now)); !slices.Equal(got, []string{points[0].ID}) {
			t.Fatalf("last revision page=%v", got)
		}
		state := sweepState(t, fixture.observer)
		if state.Revision != math.MaxInt64 || state.AfterPointID == nil || *state.AfterPointID != points[0].ID {
			t.Fatalf("last revision state=%+v", state)
		}
		candidates, err := fixture.indexer.ListCandidates(context.Background(), 1, fixture.now, catalogCandidateTestConfig())
		if !errors.Is(err, backupasset.ErrInvalidState) || candidates != nil {
			t.Fatalf("exhausted revision candidates=%+v error=%v", candidates, err)
		}
		if after := sweepState(t, fixture.observer); after.Revision != state.Revision || after.AfterPointID == nil || *after.AfterPointID != points[0].ID {
			t.Fatalf("exhausted revision changed state=%+v", after)
		}
	})
	t.Run("lost_cas", func(t *testing.T) {
		fixture := newSweepTestFixture(t, postgres, dsn)
		fixture.points(t, 2)
		probe := &sweepProbe{zeroCAS: true}
		candidates, err := sweepProbeIndexer(fixture.primary, probe).ListCandidates(context.Background(), 1, fixture.now, catalogCandidateTestConfig())
		if !errors.Is(err, backupasset.ErrConflict) || candidates != nil {
			t.Fatalf("lost CAS candidates=%+v error=%v", candidates, err)
		}
		requireSweepIdle(t, sweepState(t, fixture.observer), 1)
	})
	for _, test := range []struct {
		name  string
		index int
	}{{"missing_last_point", 0}, {"missing_high_water", 1}} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSweepTestFixture(t, postgres, dsn)
			points := fixture.points(t, 2)
			probe := &sweepProbe{deleteBeforeCAS: points[test.index].ID, postgres: postgres}
			candidates, err := sweepProbeIndexer(fixture.primary, probe).ListCandidates(context.Background(), 1, fixture.now, catalogCandidateTestConfig())
			if !errors.Is(err, backupasset.ErrConflict) || candidates != nil {
				t.Fatalf("missing CAS source candidates=%+v error=%v", candidates, err)
			}
			requireSweepIdle(t, sweepState(t, fixture.observer), 1)
			var count int64
			if err := fixture.observer.Model(&model.RecoveryPoint{}).Where("id = ?", points[test.index].ID).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("CAS rollback did not restore source point: count=%d error=%v", count, err)
			}
		})
	}
	t.Run("missing_singleton", func(t *testing.T) {
		fixture := newSweepTestFixture(t, postgres, dsn)
		if err := fixture.primary.Where("id = ?", 1).Delete(&model.CatalogCandidateSweep{}).Error; err != nil {
			t.Fatal(err)
		}
		candidates, err := fixture.indexer.ListCandidates(context.Background(), 1, fixture.now, catalogCandidateTestConfig())
		if !errors.Is(err, backupasset.ErrInvalidState) || candidates != nil {
			t.Fatalf("missing singleton candidates=%+v error=%v", candidates, err)
		}
		var count int64
		if err := fixture.observer.Model(&model.CatalogCandidateSweep{}).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("missing singleton was repaired: count=%d error=%v", count, err)
		}
	})
	if !postgres {
		for _, test := range []struct {
			name, expression string
			value            any
		}{
			{"nul_id", "?", strings.Repeat("a", 32) + "\x00"},
			{"blob_id", "CAST(? AS BLOB)", strings.Repeat("a", 32)},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := newSweepTestFixture(t, false, dsn)
				sqlDB, err := fixture.primary.DB()
				if err != nil {
					t.Fatal(err)
				}
				sqlDB.SetMaxOpenConns(1)
				if err := fixture.primary.Exec("PRAGMA ignore_check_constraints = ON").Error; err != nil {
					t.Fatal(err)
				}
				query := "UPDATE catalog_candidate_sweeps SET after_created_at = ?, after_point_id = " + test.expression + ", high_water_created_at = ?, high_water_point_id = ? WHERE id = 1"
				if err := fixture.primary.Exec(query, fixture.now.Add(-time.Second), test.value, fixture.now, strings.Repeat("f", 32)).Error; err != nil {
					t.Fatal(err)
				}
				candidates, err := fixture.indexer.ListCandidates(context.Background(), 1, fixture.now, catalogCandidateTestConfig())
				if !errors.Is(err, backupasset.ErrInvalidState) || candidates != nil {
					t.Fatalf("invalid persisted ID candidates=%+v error=%v", candidates, err)
				}
				if state := sweepState(t, fixture.observer); state.Revision != 1 {
					t.Fatalf("invalid state was modified: %+v", state)
				}
			})
		}
	}
}

func testSweepLostCommit(t *testing.T, postgres bool, dsn string) {
	fixture := newSweepTestFixture(t, postgres, dsn)
	points := fixture.points(t, 2)
	probe := &sweepProbe{loseCommit: true}
	candidates, err := sweepProbeIndexer(fixture.primary, probe).ListCandidates(context.Background(), 1, fixture.now, catalogCandidateTestConfig())
	if !errors.Is(err, errSweepInjected) || candidates != nil {
		t.Fatalf("lost commit acknowledgement candidates=%+v error=%v", candidates, err)
	}
	_, _, committed := probe.statements()
	state := sweepState(t, fixture.observer)
	if !committed || state.Revision != 2 || state.AfterPointID == nil || *state.AfterPointID != points[0].ID {
		t.Fatalf("lost commit acknowledgement state=%+v commit called=%t", state, committed)
	}
	restarted := &Indexer{db: fixture.observer}
	if got := sweepIDs(sweepPage(t, restarted, 1, fixture.now)); !slices.Equal(got, []string{points[1].ID}) {
		t.Fatalf("continuation after lost acknowledgement=%v", got)
	}
	if got := sweepIDs(sweepPage(t, restarted, 1, fixture.now)); !slices.Equal(got, []string{points[0].ID}) {
		t.Fatalf("later window did not revisit lost page: %v", got)
	}
}

func testSweepCommittedBeforeDispatch(t *testing.T, postgres bool, dsn string) {
	fixture := newSweepTestFixture(t, postgres, dsn)
	points := fixture.points(t, 2)
	if got := sweepIDs(sweepPage(t, fixture.indexer, 1, fixture.now)); !slices.Equal(got, []string{points[0].ID}) {
		t.Fatalf("undispatched first page=%v", got)
	}
	restarted := &Indexer{db: fixture.observer}
	if got := sweepIDs(sweepPage(t, restarted, 1, fixture.now)); !slices.Equal(got, []string{points[1].ID}) {
		t.Fatalf("restarted continuation=%v", got)
	}
	if got := sweepIDs(sweepPage(t, restarted, 1, fixture.now)); !slices.Equal(got, []string{points[0].ID}) {
		t.Fatalf("undispatched page not revisited on wrap: %v", got)
	}
}

func testSweepQueryBudget(t *testing.T, postgres bool, dsn string) {
	t.Run("empty", func(t *testing.T) {
		primary, _ := openSweepTestDB(t, postgres, dsn)
		probe := &sweepProbe{}
		indexer := sweepProbeIndexer(primary, probe)
		now := time.Date(2026, 7, 17, 11, 0, 0, 0, time.UTC)
		if got := sweepPage(t, indexer, 1, now); len(got) != 0 {
			t.Fatalf("empty page=%+v", got)
		}
		queries, execs, committed := probe.statements()
		if len(queries) != 2 || len(execs) != 0 || !committed {
			t.Fatalf("empty database SQL queries=%d execs=%d commit=%t", len(queries), len(execs), committed)
		}
		requireSweepIdle(t, sweepState(t, primary), 1)
	})
	t.Run("one", func(t *testing.T) { testSweepBudgetCase(t, postgres, dsn, 1) })
	t.Run("maximum", func(t *testing.T) { testSweepBudgetCase(t, postgres, dsn, MaxCandidatePageSize) })
}

func testSweepBudgetCase(t *testing.T, postgres bool, dsn string, limit int) {
	t.Helper()
	fixture := newSweepTestFixture(t, postgres, dsn)
	points := fixture.points(t, 1001)
	for sequence := 1; sequence <= 50; sequence++ {
		seedSweepGeneration(t, fixture.primary, points[0], 3000+sequence, sequence, GenerationSuperseded, false)
	}
	probe := &sweepProbe{}
	indexer := sweepProbeIndexer(fixture.primary, probe)
	first := sweepPage(t, indexer, limit, fixture.now)
	if len(first) != limit {
		t.Fatalf("limit=%d first page has %d eligible points", limit, len(first))
	}
	for index := range first {
		if first[index].RecoveryPointID != points[index].ID {
			t.Fatalf("first page[%d]=%s want %s", index, first[index].RecoveryPointID, points[index].ID)
		}
	}
	queries, execs, committed := probe.statements()
	if len(queries) != 4 || len(execs) != 1 || !committed {
		t.Fatalf("opening SQL queries=%d execs=%d commit=%t, want 4+1", len(queries), len(execs), committed)
	}
	state := sweepState(t, fixture.observer)
	if state.Revision != 2 || state.AfterPointID == nil || *state.AfterPointID != points[limit-1].ID || state.HighWaterPointID == nil || *state.HighWaterPointID != points[1000].ID {
		t.Fatalf("opening page cursor=%+v", state)
	}
	assertSweepIndexAvailable(t, fixture.observer, postgres, "recovery_points", "created_at", "id")
	assertSweepIndexAvailable(t, fixture.observer, postgres, "catalog_generations", "recovery_point_id", "generation")
	logSweepQueryPlan(t, fixture.observer, postgres, "page", queries[2])
	logSweepQueryPlan(t, fixture.observer, postgres, "ranked_history", queries[3])
	probe.reset()
	second := sweepPage(t, indexer, limit, fixture.now)
	wantCount := min(limit, len(points)-limit)
	if len(second) != wantCount {
		t.Fatalf("limit=%d continuation has %d candidates, want %d", limit, len(second), wantCount)
	}
	for index := range second {
		if second[index].RecoveryPointID != points[limit+index].ID {
			t.Fatalf("continuation[%d]=%s want %s", index, second[index].RecoveryPointID, points[limit+index].ID)
		}
	}
	queries, execs, committed = probe.statements()
	if len(queries) != 3 || len(execs) != 1 || !committed {
		t.Fatalf("continuation SQL queries=%d execs=%d commit=%t, want 3+1", len(queries), len(execs), committed)
	}
	if limit == MaxCandidatePageSize {
		requireSweepIdle(t, sweepState(t, fixture.observer), 3)
	}
}

func assertSweepIndexAvailable(t *testing.T, db *gorm.DB, postgres bool, table, first, second string) {
	t.Helper()
	query := `SELECT COUNT(*) FROM pragma_index_list(?) AS idx
		WHERE idx.partial = 0
		AND EXISTS (SELECT 1 FROM pragma_index_info(idx.name) AS col WHERE col.seqno = 0 AND col.name = ?)
		AND EXISTS (SELECT 1 FROM pragma_index_info(idx.name) AS col WHERE col.seqno = 1 AND col.name = ?)`
	if postgres {
		query = `SELECT COUNT(*) FROM pg_index AS idx
			JOIN pg_class AS tbl ON tbl.oid = idx.indrelid
			JOIN pg_namespace AS ns ON ns.oid = tbl.relnamespace
			JOIN pg_attribute AS first_col ON first_col.attrelid = tbl.oid AND first_col.attnum = idx.indkey[0]
			JOIN pg_attribute AS second_col ON second_col.attrelid = tbl.oid AND second_col.attnum = idx.indkey[1]
			WHERE ns.nspname = current_schema() AND tbl.relname = ? AND first_col.attname = ? AND second_col.attname = ?
			AND idx.indnkeyatts >= 2 AND idx.indpred IS NULL AND idx.indisvalid AND idx.indisready AND idx.indislive`
	}
	var count int64
	if err := db.Raw(query, table, first, second).Scan(&count).Error; err != nil || count == 0 {
		t.Fatalf("missing usable %s(%s,%s) index: count=%d error=%v", table, first, second, count, err)
	}
}

func logSweepQueryPlan(t *testing.T, db *gorm.DB, postgres bool, label string, statement sweepStatement) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	prefix := "EXPLAIN QUERY PLAN "
	if postgres {
		prefix = "EXPLAIN (ANALYZE, BUFFERS) "
	}
	rows, err := sqlDB.QueryContext(context.Background(), prefix+statement.query, statement.args...)
	if err != nil {
		t.Fatalf("explain %s: %v", label, err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close %s plan: %v", label, err)
		}
	}()
	for rows.Next() {
		var detail string
		if postgres {
			err = rows.Scan(&detail)
		} else {
			var id, parent, unused int
			err = rows.Scan(&id, &parent, &unused, &detail)
		}
		if err != nil {
			t.Fatalf("read %s plan: %v", label, err)
		}
		t.Logf("%s plan: %s", label, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s plan: %v", label, err)
	}
}
