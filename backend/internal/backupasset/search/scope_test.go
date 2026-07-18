package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestScopeCurrentSelectsNewestAuthorizedProducingLineageBeforeCoverage(t *testing.T) {
	db := openScopeTestDB(t)
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	oldID := fmt.Sprintf("%032x", 1)
	newID := fmt.Sprintf("%032x", 2)
	mutableID := fmt.Sprintf("%032x", 3)
	importedID := fmt.Sprintf("%032x", 4)
	unownedID := fmt.Sprintf("%032x", 5)
	insertScopePoint(t, db, scopePointSeed{ID: oldID, RepositoryID: strings.Repeat("a", 32), TaskID: 71, RunID: 701, LinkID: strings.Repeat("1", 32), Semantics: "xirang_manifest", State: "committed", CommittedAt: now.Add(-time.Hour), CreatedAt: now.Add(-time.Hour)})
	insertScopePoint(t, db, scopePointSeed{ID: newID, RepositoryID: strings.Repeat("a", 32), TaskID: 71, RunID: 702, LinkID: strings.Repeat("1", 32), Semantics: "xirang_manifest", State: "degraded", CommittedAt: now, CreatedAt: now})
	insertScopePoint(t, db, scopePointSeed{ID: mutableID, RepositoryID: strings.Repeat("b", 32), TaskID: 72, Semantics: "mutable_head", State: "observed", ObservedAt: now, CreatedAt: now})
	insertScopePoint(t, db, scopePointSeed{ID: importedID, RepositoryID: strings.Repeat("c", 32), Semantics: "imported_baseline", State: "committed", CommittedAt: now, CreatedAt: now})
	insertScopePoint(t, db, scopePointSeed{ID: unownedID, RepositoryID: strings.Repeat("d", 32), TaskID: 73, RunID: 703, LinkID: strings.Repeat("2", 32), Semantics: "native_snapshot", State: "committed", CommittedAt: now.Add(time.Minute), CreatedAt: now})

	authorizer := &scopeTestAuthorizer{allowed: map[string]bool{oldID: true, newID: true, mutableID: true, importedID: true}}
	resolver, err := NewScopeResolver(db, authorizer, ScopeResolverLimits{MaxCandidates: 3000})
	if err != nil {
		t.Fatalf("NewScopeResolver: %v", err)
	}
	selection, err := resolver.Resolve(context.Background(), catalog.AuthorizationScope{Role: "admin", UserID: 1}, SearchScope{Mode: SearchScopeCurrent})
	if err != nil {
		t.Fatalf("Resolve current: %v", err)
	}
	if got, want := selectedPointIDs(selection), []string{newID, mutableID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("current selection=%v, want newest immutable plus mutable %v", got, want)
	}
	if selection.Points[0].Lineage == selection.Points[1].Lineage || selection.RevisionDigest == "" {
		t.Fatalf("selection lacks isolated lineage/revision: %+v", selection)
	}
}

func TestScopeAllRetainedAndExactAreAuthorizedAndFailClosed(t *testing.T) {
	db := openScopeTestDB(t)
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	owned := fmt.Sprintf("%032x", 11)
	imported := fmt.Sprintf("%032x", 12)
	unowned := fmt.Sprintf("%032x", 13)
	insertScopePoint(t, db, scopePointSeed{ID: owned, RepositoryID: strings.Repeat("a", 32), TaskID: 81, RunID: 801, LinkID: strings.Repeat("3", 32), Semantics: "native_snapshot", State: "committed", CommittedAt: now, CreatedAt: now})
	insertScopePoint(t, db, scopePointSeed{ID: imported, RepositoryID: strings.Repeat("b", 32), Semantics: "imported_baseline", State: "committed", CommittedAt: now, CreatedAt: now})
	insertScopePoint(t, db, scopePointSeed{ID: unowned, RepositoryID: strings.Repeat("c", 32), TaskID: 82, RunID: 802, LinkID: strings.Repeat("4", 32), Semantics: "xirang_manifest", State: "committed", CommittedAt: now, CreatedAt: now})
	authorizer := &scopeTestAuthorizer{allowed: map[string]bool{owned: true, imported: true}}
	resolver, _ := NewScopeResolver(db, authorizer, ScopeResolverLimits{MaxCandidates: 3000})

	all, err := resolver.Resolve(context.Background(), catalog.AuthorizationScope{Role: "admin", UserID: 1}, SearchScope{Mode: SearchScopeAllRetained})
	if err != nil {
		t.Fatalf("Resolve all retained: %v", err)
	}
	if got, want := selectedPointIDs(all), []string{owned, imported}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all retained selection=%v, want %v", got, want)
	}
	exact, err := resolver.Resolve(context.Background(), catalog.AuthorizationScope{Role: "admin", UserID: 1}, SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: []string{imported, owned}})
	if err != nil || !reflect.DeepEqual(selectedPointIDs(exact), []string{owned, imported}) {
		t.Fatalf("exact selection=%v err=%v", selectedPointIDs(exact), err)
	}
	for _, ids := range [][]string{{owned, unowned}, {owned, strings.Repeat("f", 32)}} {
		if _, err := resolver.Resolve(context.Background(), catalog.AuthorizationScope{Role: "admin", UserID: 1}, SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: ids}); !errors.Is(err, ErrScopeStale) {
			t.Fatalf("exact scope %v got %v, want ErrScopeStale", ids, err)
		}
	}
}

func TestScopeAuthorizationUsesStableBatchesOfAtMost2000(t *testing.T) {
	db := openScopeTestDB(t)
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	ids := make([]string, 2001)
	authorizer := &scopeTestAuthorizer{allowed: make(map[string]bool, len(ids))}
	tx := db.Begin()
	for index := range ids {
		ids[index] = fmt.Sprintf("%032x", index+1000)
		insertScopePoint(t, tx, scopePointSeed{ID: ids[index], RepositoryID: fmt.Sprintf("%032x", index+5000), Semantics: "imported_baseline", State: "committed", CommittedAt: now, CreatedAt: now})
		authorizer.allowed[ids[index]] = true
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit scope batch fixture: %v", err)
	}
	resolver, _ := NewScopeResolver(db, authorizer, ScopeResolverLimits{MaxCandidates: len(ids)})
	selection, err := resolver.Resolve(context.Background(), catalog.AuthorizationScope{Role: "admin", UserID: 1}, SearchScope{Mode: SearchScopeExactPoints, RecoveryPointIDs: ids})
	if err != nil {
		t.Fatalf("Resolve exact batched scope: %v", err)
	}
	if len(selection.Points) != len(ids) || !reflect.DeepEqual(authorizer.batchSizes, []int{2000, 1}) {
		t.Fatalf("selection=%d batch sizes=%v", len(selection.Points), authorizer.batchSizes)
	}
}

type scopeTestAuthorizer struct {
	allowed    map[string]bool
	batchSizes []int
}

func (authorizer *scopeTestAuthorizer) AuthorizedPointIDs(_ context.Context, _ catalog.AuthorizationScope, candidateIDs []string) ([]string, error) {
	authorizer.batchSizes = append(authorizer.batchSizes, len(candidateIDs))
	result := make([]string, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		if authorizer.allowed[id] {
			result = append(result, id)
		}
	}
	return result, nil
}

type scopePointSeed struct {
	ID, RepositoryID, LinkID, Semantics, State string
	TaskID, RunID                              uint
	CapturedAt, CommittedAt, ObservedAt        time.Time
	CreatedAt                                  time.Time
}

func openScopeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open scope test DB: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("open scope SQL DB: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec(`CREATE TABLE recovery_points (
		id TEXT PRIMARY KEY, repository_id TEXT NOT NULL, producing_task_id INTEGER,
		producing_task_run_id INTEGER, semantics TEXT NOT NULL, state TEXT NOT NULL,
		lineage_json TEXT NOT NULL, captured_at DATETIME, committed_at DATETIME,
		observed_at DATETIME, created_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create scope points: %v", err)
	}
	return db
}

func insertScopePoint(t *testing.T, db *gorm.DB, seed scopePointSeed) {
	t.Helper()
	lineage := "{}"
	if seed.Semantics == "mutable_head" {
		encoded, _ := json.Marshal(backupasset.RecoveryPointLineageSummary{ProducingTaskID: &seed.TaskID})
		lineage = string(encoded)
	} else if seed.Semantics != "imported_baseline" {
		started := seed.CreatedAt.Add(-2 * time.Minute).UTC()
		encoded, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
			Version: 1, TaskRepositoryLinkID: seed.LinkID, TaskID: seed.TaskID, TaskRunID: seed.RunID,
			Trigger: "manual", PublicationMode: string(backupasset.PublicationVersionedFullCopy),
			PointCodecVersion: 1, StartedAt: started, PreparedAt: started.Add(time.Minute), PointDeadlineAt: started.Add(time.Hour),
		})
		if err != nil {
			t.Fatalf("encode scope lineage: %v", err)
		}
		lineage = encoded
	}
	nullTime := func(value time.Time) any {
		if value.IsZero() {
			return nil
		}
		return value.UTC()
	}
	var taskID, runID any
	if seed.TaskID != 0 {
		taskID = seed.TaskID
	}
	if seed.RunID != 0 {
		runID = seed.RunID
	}
	if err := db.Exec(`INSERT INTO recovery_points
		(id, repository_id, producing_task_id, producing_task_run_id, semantics, state,
		 lineage_json, captured_at, committed_at, observed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, seed.ID, seed.RepositoryID, taskID, runID,
		seed.Semantics, seed.State, lineage, nullTime(seed.CapturedAt), nullTime(seed.CommittedAt),
		nullTime(seed.ObservedAt), seed.CreatedAt.UTC()).Error; err != nil {
		t.Fatalf("insert scope point: %v", err)
	}
}

func selectedPointIDs(selection ScopeSelection) []string {
	result := make([]string, len(selection.Points))
	for index, point := range selection.Points {
		result[index] = point.ID
	}
	return result
}
