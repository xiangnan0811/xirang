package export

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"
)

// renewalReader runs the supplied action while the real broker holds its read reservation.
type renewalReader struct {
	content.SourceReader
	action func() error
}

func (reader *renewalReader) Read(p []byte) (int, error) {
	if reader.action != nil {
		action := reader.action
		reader.action = nil
		if err := action(); err != nil {
			return 0, err
		}
	}
	return reader.SourceReader.Read(p)
}

type renewalSession struct {
	content.SourceSession
	action func() error
}

func (session *renewalSession) Reader() content.SourceReader {
	return &renewalReader{SourceReader: session.SourceSession.Reader(), action: session.action}
}

type renewalResolver struct {
	content.SourceResolver
	action func() error
}

func (resolver *renewalResolver) OpenContentSource(ctx context.Context, request content.SourceRequest) (content.SourceSession, error) {
	session, err := resolver.SourceResolver.OpenContentSource(ctx, request)
	if err != nil || request.Mode != content.SourceModeSequential {
		return session, err
	}
	return &renewalSession{SourceSession: session, action: resolver.action}, nil
}

func renewalCoordinator(t *testing.T, fixture exportBehaviorFixture, clock *time.Time) *AttemptCoordinator {
	t.Helper()
	leases, err := backupasset.NewLeaseService(fixture.db, func() time.Time { return *clock }, backupasset.LeaseConfig{
		Duration: fixture.config.LeaseTTL, Heartbeat: fixture.config.LeaseRenewMargin, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewAttemptCoordinator(fixture.db, func() time.Time { return *clock }, leases)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func runRenewedSpoolContract(t *testing.T, open func(*testing.T) exportBehaviorFixture) {
	t.Helper()
	for _, scenario := range []string{"renewed", "expired initial", "invalid current tuple"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := open(t)
			created, claim, itemAttempt := createClaimedExportForAttemptBudget(t, fixture.serviceHarness, 41, "renewed-spool-"+scenario)
			clock := fixture.service.now().UTC()
			coordinator := renewalCoordinator(t, fixture, &clock)
			budget, err := NewAttemptBudgetService(fixture.db, func() time.Time { return clock })
			if err != nil {
				t.Fatal(err)
			}
			item := frozenItemFixture()
			source := &persistentSourceResolverFake{payload: bytes.Repeat([]byte("r"), int(item.LogicalSize)), providerBytes: item.LogicalSize}
			resolver := &renewalResolver{SourceResolver: source}
			resolver.action = func() error {
				if scenario == "invalid current tuple" {
					return fixture.db.Model(&model.BackupAssetExportItem{}).Where("id = ?", itemAttempt.ItemID).UpdateColumn("current_attempt_id", nil).Error
				}
				clock = claim.LeaseExpiresAt.Add(-fixture.config.LeaseRenewMargin / 2)
				if _, err := coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken}); err != nil {
					return err
				}
				clock = claim.LeaseExpiresAt.Add(time.Second)
				if processed, err := budget.ReconcileExpiredAttemptReads(context.Background(), 10); err != nil || processed != 0 {
					t.Fatalf("held renewed reader sweep processed=%d err=%v", processed, err)
				}
				return nil
			}
			broker, err := content.NewAttemptBroker(resolver, budget, func() time.Time { return clock })
			if err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "spool")})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			worker, err := NewPersistentWorker(PersistentWorkerDependencies{DB: fixture.db, Keys: backupasset.NewKeyring(fixture.db, func() time.Time { return clock }), Broker: broker, Metadata: &metadataValidatorFake{}, Store: store, AttemptWork: NewAttemptWorkRegistry(), Now: func() time.Time { return clock }})
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "expired initial" {
				clock = claim.LeaseExpiresAt.Add(time.Second)
			}
			_, err = worker.SpoolItem(context.Background(), PersistentSpoolItemRequest{JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken, ItemID: itemAttempt.ItemID})
			if scenario == "renewed" && err != nil {
				t.Fatalf("renewed spool: %v", err)
			}
			if scenario != "renewed" && err == nil {
				t.Fatal("invalid attempt spooled successfully")
			}
			if scenario == "expired initial" && len(source.requests) != 0 {
				t.Fatalf("expired attempt opened source: %+v", source.requests)
			}
		})
	}
}

func runRenewedReaderSweepContract(t *testing.T, fixture exportBehaviorFixture) {
	t.Helper()
	clock := fixture.service.now().UTC()
	created, claim, itemAttempt := createClaimedExportForAttemptBudget(t, fixture.serviceHarness, 41, "renewed-reader-sweep")
	budget, err := NewAttemptBudgetService(fixture.db, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := budget.ReserveAttemptRead(context.Background(), content.AttemptReadIntent{SessionID: itemAttempt.ID, Mode: content.SourceModeSequential, Bytes: frozenItemFixture().LogicalSize})
	if err != nil {
		t.Fatal(err)
	}
	// A second, genuinely expired owner follows the deferred row in the sweep.
	if err := fixture.db.Create(&model.User{ID: 142, Username: "renewed-sweep-142", PasswordHash: "hash", Role: "admin", Onboarded: true, CreatedAt: clock, UpdatedAt: clock}).Error; err != nil {
		t.Fatal(err)
	}
	later := createExpiredAttemptReadForSweep(t, fixture.serviceHarness, budget, 142, "renewed-sweep-later", clock.Add(-time.Second))
	coordinator := renewalCoordinator(t, fixture, &clock)
	clock = claim.LeaseExpiresAt.Add(-fixture.config.LeaseRenewMargin / 2)
	heartbeat, err := coordinator.Heartbeat(context.Background(), AttemptHeartbeatRequest{JobID: created.JobID, AttemptID: claim.AttemptID, FenceToken: claim.FenceToken})
	if err != nil {
		t.Fatal(err)
	}
	clock = claim.LeaseExpiresAt.Add(time.Second)
	if processed, err := budget.ReconcileExpiredAttemptReads(context.Background(), 1); err != nil || processed != 0 {
		t.Fatalf("renewed sweep processed=%d err=%v", processed, err)
	}
	if processed, err := budget.ReconcileExpiredAttemptReads(context.Background(), 1); err != nil || processed != 1 {
		t.Fatalf("fair sweep processed=%d err=%v", processed, err)
	}
	assertReaderReservationState(t, fixture.serviceHarness, later.leaseOwner, "released")
	clock = heartbeat.LeaseExpiresAt.Add(time.Second)
	if processed, err := budget.ReconcileExpiredAttemptReads(context.Background(), 10); err != nil || processed != 1 {
		t.Fatalf("expired renewed sweep processed=%d err=%v", processed, err)
	}
	var after model.BackupAssetExportItemAttempt
	if err := fixture.db.First(&after, "id = ?", itemAttempt.ID).Error; err != nil || after.ProviderBytes != reservation.ReservedBytes {
		t.Fatalf("conservative charge=%d want=%d err=%v", after.ProviderBytes, reservation.ReservedBytes, err)
	}
}

func runPublishedWorkerCapacityContract(t *testing.T, open func(*testing.T) exportBehaviorFixture, selection FrozenSelection, actor SelectionActor, item FrozenItem) {
	t.Helper()
	for _, scenario := range []string{"cleanup", "missing", "mixed", "wrong owner", "wrong release"} {
		t.Run(scenario, func(t *testing.T) {
			limits := WorkerCapacityLimits{WorkerConcurrency: 1, UserActiveJobs: 1}
			ready := createPublishedExportBehaviorReadyFixture(t, open, selection, actor, item, "capacity-ready-"+scenario, limits)
			fixture := ready.fixture
			var rows []model.BackupAssetExportReservation
			if err := fixture.db.Where("job_id = ? AND attempt_id = ? AND kind = ?", ready.created.Job.ID, ready.attemptID, "worker").Order("id").Find(&rows).Error; err != nil || len(rows) != 2 {
				t.Fatalf("published worker pair=%+v err=%v", rows, err)
			}
			switch scenario {
			case "missing":
				if err := fixture.db.Delete(&rows[0]).Error; err != nil {
					t.Fatal(err)
				}
			case "mixed":
				if err := fixture.db.Model(&rows[0]).UpdateColumn("state", "active").Error; err != nil {
					t.Fatal(err)
				}
			case "wrong owner":
				if err := fixture.db.Model(&rows[0]).UpdateColumn("lease_owner", "wrong").Error; err != nil {
					t.Fatal(err)
				}
			case "wrong release":
				if err := fixture.db.Model(&rows[0]).UpdateColumn("released_at", ready.clock.Add(time.Second)).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := fixture.service.Cancel(context.Background(), actor, ready.created.Job.ID); err != nil {
				t.Fatal(err)
			}
			quota, err := NewQuotaService(fixture.db, func() time.Time { return ready.clock }, fixture.config.Quota)
			if err != nil {
				t.Fatal(err)
			}
			port, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{DB: fixture.db, Delivery: exportBehaviorLifecycleDeliveryStub{}, Sources: fixture.lease, Quota: quota, Store: ready.store, AttemptWork: ready.worker.attemptWork, WorkerCapacity: &limits, Now: func() time.Time { return ready.clock }})
			if err != nil {
				t.Fatal(err)
			}
			lifecycle, err := NewLifecycle(LifecycleDependencies{DB: fixture.db, Port: port, Now: func() time.Time { return ready.clock }})
			if err != nil {
				t.Fatal(err)
			}
			result, err := lifecycle.Cleanup(context.Background(), ready.created.Job.ID)
			if scenario != "cleanup" {
				if !errors.Is(err, ErrAttemptFenceLost) {
					t.Fatalf("malformed pair cleanup=%s err=%v", result, err)
				}
				return
			}
			if err != nil || result != CleanupPurged {
				t.Fatalf("cleanup=%s err=%v", result, err)
			}
			assertReleasedDurableWorkerReservationPair(t, fixture.db, ready.created.Job.ID, ready.attemptID, 0)
		})
	}
}
