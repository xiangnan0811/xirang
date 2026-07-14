package snapshot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	indexerPointOne = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	indexerPointTwo = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func openIndexerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_loc=UTC", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open indexer database: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.SnapshotFileIndex{}); err != nil {
		t.Fatalf("migrate indexer database: %v", err)
	}
	return db
}

type indexerSettings map[string]string

func (settings indexerSettings) GetEffective(key string) string { return settings[key] }

func indexerFoundation() *backupasset.FoundationService {
	return backupasset.NewFoundationService(indexerSettings{
		"backup_assets.enabled":                          "true",
		"backup_assets.catalog_batch_size":               "2000",
		"backup_assets.catalog_build_timeout":            "30m",
		"backup_assets.repository_reconcile_interval":    "15m",
		"backup_assets.audit_segment_max_events":         "10000",
		"backup_assets.audit_segment_max_age":            "24h",
		"backup_assets.audit_detail_retention_days":      "180",
		"backup_assets.audit_checkpoint_retention_days":  "2555",
		"backup_assets.lease_duration":                   "5m",
		"backup_assets.lease_heartbeat":                  "60s",
		"backup_assets.lease_absolute_deadline":          "168h",
		"backup_assets.provider_operation_timeout":       "2m",
		"backup_assets.provider_max_concurrency":         "4",
		"backup_assets.provider_metadata_limit_bytes":    "16777216",
		"backup_assets.publication_reconcile_interval":   "5m",
		"backup_assets.publication_reconcile_batch_size": "100",
		"backup_assets.publication_worker_concurrency":   "2",
		"backup_assets.publication_missing_grace":        "30m",
		"backup_assets.publication_stream_max_bytes":     "268435456",
		"backup_assets.manifest_timeout":                 "2h",
		"backup_assets.manifest_max_bytes":               "4294967296",
		"backup_assets.manifest_max_entries":             "10000000",
		"backup_assets.manifest_max_record_bytes":        "1048576",
		"backup_assets.manifest_max_depth":               "4096",
	})
}

type indexerLineageSession struct {
	publication.LineageSession
	mode      publication.LineageMode
	points    []publication.CommittedPoint
	list      func(context.Context, string, provider.EntryLocator, provider.PageRequest) (provider.EntryPage, error)
	mu        sync.Mutex
	listCalls []indexerListCall
	closed    int
}

type indexerListCall struct {
	point  string
	parent string
	page   provider.PageRequest
}

func (session *indexerLineageSession) Mode() publication.LineageMode { return session.mode }

func (session *indexerLineageSession) CommittedPoints() []publication.CommittedPoint {
	return append([]publication.CommittedPoint(nil), session.points...)
}

func (session *indexerLineageSession) ListEntries(ctx context.Context, pointID string, parent provider.EntryLocator, page provider.PageRequest) (provider.EntryPage, error) {
	session.mu.Lock()
	session.listCalls = append(session.listCalls, indexerListCall{point: pointID, parent: parent.Native, page: page})
	session.mu.Unlock()
	if session.list == nil {
		return provider.EntryPage{}, errors.New("unexpected provider list")
	}
	return session.list(ctx, pointID, parent, page)
}

func (session *indexerLineageSession) Close() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.closed++
	return nil
}

type indexerLineageGuard struct {
	mu       sync.Mutex
	sessions []publication.LineageSession
	calls    []publication.ResticOperation
}

func (guard *indexerLineageGuard) Begin(_ context.Context, _ uint, operation publication.ResticOperation) (publication.LineageSession, error) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.calls = append(guard.calls, operation)
	if len(guard.sessions) == 0 {
		return nil, errors.New("unexpected lineage admission")
	}
	session := guard.sessions[0]
	guard.sessions = guard.sessions[1:]
	return session, nil
}

var _ publication.LineageSession = (*indexerLineageSession)(nil)
var _ publication.LineageGuard = (*indexerLineageGuard)(nil)

func TestIndexerExactModeEnumeratesOnlyCommittedTaskPoints(t *testing.T) {
	db := openIndexerTestDB(t)
	taskEntity := model.Task{Name: "exact-index", ExecutorType: "restic", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SnapshotFileIndex{TaskID: taskEntity.ID, SnapshotID: indexerPointTwo, Path: "/foreign.txt", Size: 99, Mtime: "old"}).Error; err != nil {
		t.Fatal(err)
	}

	point := publication.CommittedPoint{RecoveryPointID: "11111111111111111111111111111111", FullNativeID: indexerPointOne, CapturedAt: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)}
	session := &indexerLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{point}}
	session.list = func(_ context.Context, pointID string, parent provider.EntryLocator, page provider.PageRequest) (provider.EntryPage, error) {
		if pointID != indexerPointOne {
			t.Fatalf("listed uncommitted/foreign point %q", pointID)
		}
		if page.Limit != exactIndexPageSize {
			t.Fatalf("page limit=%d, want %d", page.Limit, exactIndexPageSize)
		}
		switch parent.Native {
		case "/":
			return provider.EntryPage{Items: []provider.Entry{
				{Name: "top.txt", Type: backupasset.CatalogEntryFile, Size: 7, ModTime: point.CapturedAt, Locator: provider.EntryLocator{Native: "/top.txt"}},
				{Name: "nested", Type: backupasset.CatalogEntryDirectory, ModTime: point.CapturedAt, Locator: provider.EntryLocator{Native: "/nested"}},
			}}, nil
		case "/nested":
			return provider.EntryPage{Items: []provider.Entry{{Name: "deep.txt", Type: backupasset.CatalogEntryFile, Size: 11, ModTime: point.CapturedAt, Locator: provider.EntryLocator{Native: "/nested/deep.txt"}}}}, nil
		default:
			return provider.EntryPage{}, fmt.Errorf("unexpected parent %q", parent.Native)
		}
	}
	guard := &indexerLineageGuard{sessions: []publication.LineageSession{session}}
	indexer := NewIndexer(db, guard, indexerFoundation())

	if err := indexer.Build(context.Background(), taskEntity); err != nil {
		t.Fatalf("build exact index: %v", err)
	}

	var rows []model.SnapshotFileIndex
	if err := db.Where("task_id = ?", taskEntity.ID).Order("snapshot_id, path").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("indexed rows=%+v, want 3 entries plus completion marker", rows)
	}
	for _, row := range rows {
		if row.SnapshotID != indexerPointOne {
			t.Fatalf("foreign or stale row survived exact build: %+v", row)
		}
	}
	if rows[0].Path != "" || rows[0].Mtime != exactIndexCompleteMarkerMtime || rows[0].Size != 3 {
		t.Fatalf("completion marker=%+v", rows[0])
	}
	if session.closed != 1 {
		t.Fatalf("admission close count=%d, want 1", session.closed)
	}
	if len(guard.calls) != 1 || guard.calls[0] != publication.OperationLegacyIndex {
		t.Fatalf("index admissions=%v", guard.calls)
	}
}

func TestEnsureIndexedRejectsPartialRowsWithoutCompletionMarker(t *testing.T) {
	db := openIndexerTestDB(t)
	taskEntity := model.Task{Name: "partial-index", ExecutorType: "restic", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SnapshotFileIndex{TaskID: taskEntity.ID, SnapshotID: indexerPointOne, Path: "/partial.txt", Size: 1, Mtime: "old"}).Error; err != nil {
		t.Fatal(err)
	}
	point := publication.CommittedPoint{RecoveryPointID: "22222222222222222222222222222222", FullNativeID: indexerPointOne, CapturedAt: time.Now().UTC()}
	handlerSession := &indexerLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{point}}
	buildSession := &indexerLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{point}}
	buildSession.list = func(context.Context, string, provider.EntryLocator, provider.PageRequest) (provider.EntryPage, error) {
		return provider.EntryPage{}, errors.New("provider temporarily unavailable")
	}
	guard := &indexerLineageGuard{sessions: []publication.LineageSession{buildSession}}
	indexer := NewIndexer(db, guard, indexerFoundation())

	ready, err := indexer.EnsureIndexed(context.Background(), taskEntity.ID, handlerSession)
	if err != nil {
		t.Fatalf("ensure exact index: %v", err)
	}
	if ready {
		t.Fatal("partial rows without a completion marker reported ready")
	}

	deadline := time.Now().Add(3 * time.Second)
	for IsIndexing(taskEntity.ID) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	var markers int64
	if err := db.Model(&model.SnapshotFileIndex{}).Where("task_id = ? AND snapshot_id = ? AND path = ? AND mtime = ?", taskEntity.ID, indexerPointOne, exactIndexCompleteMarkerPath, exactIndexCompleteMarkerMtime).Count(&markers).Error; err != nil {
		t.Fatal(err)
	}
	if markers != 0 {
		t.Fatalf("failed exact index wrote %d completion markers", markers)
	}
}

func TestEnsureIndexedRepresentsEmptySnapshotWithCompletionMarker(t *testing.T) {
	db := openIndexerTestDB(t)
	taskEntity := model.Task{Name: "empty-index", ExecutorType: "restic", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	point := publication.CommittedPoint{RecoveryPointID: "33333333333333333333333333333333", FullNativeID: indexerPointOne, CapturedAt: time.Now().UTC()}
	session := &indexerLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{point}}
	session.list = func(_ context.Context, pointID string, parent provider.EntryLocator, _ provider.PageRequest) (provider.EntryPage, error) {
		if pointID != indexerPointOne || parent.Native != "/" {
			t.Fatalf("empty snapshot lookup point=%q parent=%q", pointID, parent.Native)
		}
		return provider.EntryPage{}, nil
	}
	guard := &indexerLineageGuard{sessions: []publication.LineageSession{session}}
	indexer := NewIndexer(db, guard, indexerFoundation())

	if err := indexer.Build(context.Background(), taskEntity); err != nil {
		t.Fatalf("build empty exact index: %v", err)
	}
	ready, err := indexer.EnsureIndexed(context.Background(), taskEntity.ID, &indexerLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{point}})
	if err != nil || !ready {
		t.Fatalf("empty snapshot ready=%v err=%v", ready, err)
	}
	var rows []model.SnapshotFileIndex
	if err := db.Where("task_id = ?", taskEntity.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Path != exactIndexCompleteMarkerPath || rows[0].Mtime != exactIndexCompleteMarkerMtime || rows[0].Size != 0 {
		t.Fatalf("empty snapshot rows=%+v", rows)
	}
}

func TestIndexerExactModeRetainsPreviousCompletePointAfterReplacementFailure(t *testing.T) {
	db := openIndexerTestDB(t)
	taskEntity := model.Task{Name: "replace-exact-index", ExecutorType: "restic", Status: "pending"}
	if err := db.Create(&taskEntity).Error; err != nil {
		t.Fatal(err)
	}
	previousRows := []model.SnapshotFileIndex{
		{TaskID: taskEntity.ID, SnapshotID: indexerPointOne, Path: "/trusted.txt", Size: 7, Mtime: "old"},
		{TaskID: taskEntity.ID, SnapshotID: indexerPointOne, Path: exactIndexCompleteMarkerPath, Size: 1, Mtime: exactIndexCompleteMarkerMtime},
	}
	if err := db.Create(&previousRows).Error; err != nil {
		t.Fatal(err)
	}

	point := publication.CommittedPoint{RecoveryPointID: "44444444444444444444444444444444", FullNativeID: indexerPointOne, CapturedAt: time.Now().UTC()}
	session := &indexerLineageSession{mode: publication.LineageExact, points: []publication.CommittedPoint{point}}
	session.list = func(context.Context, string, provider.EntryLocator, provider.PageRequest) (provider.EntryPage, error) {
		return provider.EntryPage{}, errors.New("provider replacement failure")
	}
	guard := &indexerLineageGuard{sessions: []publication.LineageSession{session}}
	indexer := NewIndexer(db, guard, indexerFoundation())

	if err := indexer.Build(context.Background(), taskEntity); err == nil {
		t.Fatal("exact replacement unexpectedly succeeded")
	}
	var rows []model.SnapshotFileIndex
	if err := db.Where("task_id = ? AND snapshot_id = ?", taskEntity.ID, indexerPointOne).Order("path").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(previousRows) || rows[0].Path != exactIndexCompleteMarkerPath || rows[1].Path != "/trusted.txt" {
		t.Fatalf("failed replacement removed prior complete index: %+v", rows)
	}
}
