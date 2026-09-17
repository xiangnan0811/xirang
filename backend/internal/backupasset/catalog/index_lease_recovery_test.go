package catalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func TestCatalogRestartRecoversExpiredIndexLease(t *testing.T) {
	fixture := newCatalogIndexerFixture(t, true, 0)
	assertCatalogRestartRecoversExpiredIndexLease(t, fixture)
}

func assertCatalogRestartRecoversExpiredIndexLease(t *testing.T, fixture catalogIndexerFixture) {
	t.Helper()
	ctx := context.Background()
	_, frozen, oldFence, prior, abandoned, proof, digest := prepareCatalogActivation(t, fixture)
	fixture.now = fixture.now.Add(31 * time.Minute)
	var err error
	fixture.lease, err = backupasset.NewLeaseService(fixture.db, func() time.Time { return fixture.now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := fixture.newIndexer(t, fixture.factory())
	count, err := restarted.ReconcileAbandoned(ctx, 30*time.Minute, 10)
	if err != nil || count != 1 {
		t.Fatalf("reconcile count=%d err=%v", count, err)
	}
	fresh, err := restarted.Build(ctx, BuildRequest{RepositoryID: fixture.point.RepositoryID, RecoveryPointID: fixture.point.ID})
	if err != nil {
		t.Fatalf("restart build blocked by old lease: %v", err)
	}
	if fresh.Generation != abandoned.Generation+1 || !fresh.IsActive || fresh.State != string(GenerationComplete) {
		t.Fatalf("replacement generation did not complete: %+v", fresh)
	}
	if _, err := restarted.activate(ctx, abandoned, frozen, oldFence, proof, 0, digest); !errors.Is(err, backupasset.ErrLeaseFenceLost) {
		t.Fatalf("old attempt activation error=%v", err)
	}
	var failed, oldActive model.CatalogGeneration
	if err := fixture.db.First(&failed, "id = ?", abandoned.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&oldActive, "id = ?", prior.ID).Error; err != nil {
		t.Fatal(err)
	}
	if failed.State != string(GenerationFailed) || failed.ErrorCode != "catalog_build_abandoned" || failed.IsActive || oldActive.IsActive {
		t.Fatal("restart lost abandoned evidence or kept old projection active")
	}
}
