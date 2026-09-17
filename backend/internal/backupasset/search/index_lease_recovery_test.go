package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func TestSearchRestartRecoversExpiredIndexLease(t *testing.T) {
	indexer, harness := newIndexerTestHarness(t)
	assertSearchRestartRecoversExpiredIndexLease(t, indexer, harness)
}

func assertSearchRestartRecoversExpiredIndexLease(t *testing.T, indexer *Indexer, harness *indexerTestHarness) {
	t.Helper()
	ctx := context.Background()
	pointID, _, _ := harness.produceMutableCatalog(t)
	request := BuildRequest{RecoveryPointID: pointID}
	prior, err := indexer.Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.lease.Acquire(ctx, backupasset.AcquireLeaseRequest{RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderSearchIndex, OwnerID: searchBuildOwnerPrefix + pointID})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := indexer.loadFrozenProjection(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := indexer.beginGeneration(ctx, frozen, lease.Fence, "restart-test")
	if err != nil {
		t.Fatal(err)
	}
	harness.now = harness.now.Add(31 * time.Minute)
	restartedLease, err := backupasset.NewLeaseService(harness.db, func() time.Time { return harness.now }, backupasset.LeaseConfig{Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewIndexer(IndexerDependencies{DB: harness.db, Lease: restartedLease, Keys: harness.ring, Now: func() time.Time { return harness.now }, Config: standardIndexerConfig()})
	if err != nil {
		t.Fatal(err)
	}
	count, err := restarted.ReconcileAbandoned(ctx, harness.now.Add(-30*time.Minute), 10)
	if err != nil || count != 1 {
		t.Fatalf("reconcile count=%d err=%v", count, err)
	}
	fresh, err := restarted.Build(ctx, request)
	if err != nil {
		t.Fatalf("restart build blocked by old lease: %v", err)
	}
	if fresh.Generation != abandoned.Generation+1 || !fresh.IsActive || fresh.State != string(SearchGenerationComplete) || fresh.WrittenDocumentCount != 1 {
		t.Fatalf("replacement generation did not complete: %+v", fresh)
	}
	if _, err := restarted.activate(ctx, frozen, lease.Fence, abandoned.ID); !errors.Is(err, backupasset.ErrLeaseFenceLost) {
		t.Fatalf("old attempt activation error=%v", err)
	}
	var failed, oldActive model.BackupAssetSearchGeneration
	if err := harness.db.First(&failed, "id = ?", abandoned.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&oldActive, "id = ?", prior.ID).Error; err != nil {
		t.Fatal(err)
	}
	if failed.State != string(SearchGenerationFailed) || failed.ErrorCode != string(SearchErrorBuildAbandoned) || failed.IsActive || oldActive.IsActive {
		t.Fatal("restart lost abandoned evidence or kept old projection active")
	}
}
