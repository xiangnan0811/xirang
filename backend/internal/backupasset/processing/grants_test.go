package processing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
)

func TestGrantActivationIsOneUseAndBoundToAttemptWorkerAndFence(t *testing.T) {
	harness, service, lease := newGrantHarness(t)
	material, err := service.IssueAttemptGrants(context.Background(), IssueGrantsRequest{
		JobID: lease.JobID, AttemptID: lease.AttemptID, WorkerID: lease.WorkerID,
		RecoveryPointFence: lease.RecoveryPointFence,
	})
	if err != nil {
		t.Fatalf("IssueAttemptGrants: %v", err)
	}
	if material.Input.Secret == "" || material.Sink.Secret == "" || material.Input.Secret == material.Sink.Secret {
		t.Fatalf("grant activation material is missing or reused: %+v", material)
	}
	var input model.BackupAssetProcessingGrant
	if err := harness.db.First(&input, "id = ?", material.Input.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if input.ActivationSecretHash == "" || strings.Contains(string(input.ActivationSecretHash), material.Input.Secret) {
		t.Fatalf("plaintext secret was persisted or hash is missing: %+v", input)
	}

	base := ActivateGrantRequest{
		GrantID: material.Input.GrantID, Kind: GrantInput, JobID: lease.JobID,
		AttemptID: lease.AttemptID, WorkerID: lease.WorkerID, Secret: material.Input.Secret,
	}
	wrongWorker := base
	wrongWorker.WorkerID = strings.Repeat("f", 32)
	if _, err := service.Activate(context.Background(), wrongWorker); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("wrong Worker activation got %v", err)
	}
	wrongSecret := base
	wrongSecret.Secret = strings.Repeat("0", 64)
	if _, err := service.Activate(context.Background(), wrongSecret); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("wrong secret activation got %v", err)
	}
	activated, err := service.Activate(context.Background(), base)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if activated.SessionID != material.Input.GrantID || activated.Kind != GrantInput {
		t.Fatalf("activation session is not narrowly bound: %+v", activated)
	}
	if _, err := service.Activate(context.Background(), base); !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("activation replay got %v", err)
	}
	if err := harness.db.First(&input, "id = ?", material.Input.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if input.State != string(GrantActive) || input.ActivationSecretHash != "" || input.ActivatedAt == nil {
		t.Fatalf("activated grant lifecycle invalid: %+v", input)
	}
}

func TestGrantActivationFailsClosedAfterExpiryOrFenceLoss(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		harness, service, lease := newGrantHarness(t)
		material, err := service.IssueAttemptGrants(context.Background(), IssueGrantsRequest{
			JobID: lease.JobID, AttemptID: lease.AttemptID, WorkerID: lease.WorkerID,
			RecoveryPointFence: lease.RecoveryPointFence,
		})
		if err != nil {
			t.Fatal(err)
		}
		harness.clock.Advance(31 * time.Second)
		_, err = service.Activate(context.Background(), ActivateGrantRequest{
			GrantID: material.Input.GrantID, Kind: GrantInput, JobID: lease.JobID,
			AttemptID: lease.AttemptID, WorkerID: lease.WorkerID, Secret: material.Input.Secret,
		})
		if !errors.Is(err, ErrGrantDenied) {
			t.Fatalf("expired activation got %v", err)
		}
	})

	t.Run("fence loss", func(t *testing.T) {
		harness, service, lease := newGrantHarness(t)
		material, err := service.IssueAttemptGrants(context.Background(), IssueGrantsRequest{
			JobID: lease.JobID, AttemptID: lease.AttemptID, WorkerID: lease.WorkerID,
			RecoveryPointFence: lease.RecoveryPointFence,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", lease.RecoveryPointFence.LeaseID).
			Update("fence_token", strings.Repeat("e", 64)).Error; err != nil {
			t.Fatal(err)
		}
		_, err = service.Activate(context.Background(), ActivateGrantRequest{
			GrantID: material.Input.GrantID, Kind: GrantInput, JobID: lease.JobID,
			AttemptID: lease.AttemptID, WorkerID: lease.WorkerID, Secret: material.Input.Secret,
		})
		if !errors.Is(err, ErrGrantDenied) {
			t.Fatalf("lost fence activation got %v", err)
		}
	})
}

func TestGrantReservationsAreAtomicAndConservative(t *testing.T) {
	_, service, lease := newGrantHarness(t)
	material, err := service.IssueAttemptGrants(context.Background(), IssueGrantsRequest{
		JobID: lease.JobID, AttemptID: lease.AttemptID, WorkerID: lease.WorkerID,
		RecoveryPointFence: lease.RecoveryPointFence,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Activate(context.Background(), ActivateGrantRequest{
		GrantID: material.Input.GrantID, Kind: GrantInput, JobID: lease.JobID,
		AttemptID: lease.AttemptID, WorkerID: lease.WorkerID, Secret: material.Input.Secret,
	})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 6
	reservations := make(chan GrantReservation, callers)
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			reservation, reserveErr := service.Reserve(context.Background(), ReserveGrantRequest{
				GrantID: material.Input.GrantID, Kind: GrantRequestRange,
				RangeOffset: pointerInt64(0), RangeLength: pointerInt64(16), Bytes: 16,
			})
			if reserveErr != nil {
				errs <- reserveErr
				return
			}
			reservations <- reservation
		}()
	}
	wait.Wait()
	close(reservations)
	close(errs)
	var admitted []GrantReservation
	for reservation := range reservations {
		admitted = append(admitted, reservation)
	}
	if len(admitted) != 2 {
		t.Fatalf("in-flight atomic admission=%d, want 2", len(admitted))
	}
	for err := range errs {
		if !errors.Is(err, ErrGrantBudgetExceeded) {
			t.Fatalf("reservation failure=%v", err)
		}
	}
	if err := service.Finalize(context.Background(), FinalizeGrantRequest{
		ReservationID: admitted[0].ReservationID, Outcome: GrantRequestSucceeded,
		ProviderBytes: 7, EvidenceKnown: true,
	}); err != nil {
		t.Fatalf("Finalize(known): %v", err)
	}
	if err := service.Finalize(context.Background(), FinalizeGrantRequest{
		ReservationID: admitted[1].ReservationID, Outcome: GrantRequestReconciled,
		EvidenceKnown: false, FailureCode: GrantFailureReconciledCrash,
	}); err != nil {
		t.Fatalf("Finalize(unknown): %v", err)
	}
	var grant model.BackupAssetProcessingGrant
	if err := service.db.First(&grant, "id = ?", material.Input.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	if grant.InFlight != 0 || grant.ReservedBytes != 0 || grant.ConsumedBytes != 23 || grant.RequestCount != 2 {
		t.Fatalf("grant accounting is not conservative: %+v", grant)
	}
}

func TestActiveGrantReservationFailsClosedAfterFenceLoss(t *testing.T) {
	harness, service, lease := newGrantHarness(t)
	material, err := service.IssueAttemptGrants(context.Background(), IssueGrantsRequest{
		JobID: lease.JobID, AttemptID: lease.AttemptID, WorkerID: lease.WorkerID,
		RecoveryPointFence: lease.RecoveryPointFence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(context.Background(), ActivateGrantRequest{
		GrantID: material.Input.GrantID, Kind: GrantInput, JobID: lease.JobID,
		AttemptID: lease.AttemptID, WorkerID: lease.WorkerID, Secret: material.Input.Secret,
	}); err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.RecoveryPointLease{}).Where("id = ?", lease.RecoveryPointFence.LeaseID).
		Update("fence_token", strings.Repeat("e", 64)).Error; err != nil {
		t.Fatal(err)
	}
	_, err = service.Reserve(context.Background(), ReserveGrantRequest{
		GrantID: material.Input.GrantID, Kind: GrantRequestRange,
		RangeOffset: pointerInt64(0), RangeLength: pointerInt64(16), Bytes: 16,
	})
	if !errors.Is(err, ErrGrantDenied) {
		t.Fatalf("lost-fence reservation got %v", err)
	}
	var grant model.BackupAssetProcessingGrant
	if err := harness.db.First(&grant, "id = ?", material.Input.GrantID).Error; err != nil {
		t.Fatal(err)
	}
	var requests int64
	if err := harness.db.Model(&model.BackupAssetProcessingGrantRequest{}).Where("grant_id = ?", grant.ID).Count(&requests).Error; err != nil {
		t.Fatal(err)
	}
	if requests != 0 || grant.RequestCount != 0 || grant.ReservedBytes != 0 || grant.InFlight != 0 {
		t.Fatalf("lost-fence reservation mutated budget: grant=%+v requests=%d", grant, requests)
	}
}

func TestGrantServicePersistsDistinctInputAndSinkLimits(t *testing.T) {
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "e")
	work, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "distinct-grant-limits", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil || lease.JobID != work.JobID {
		t.Fatalf("Pull: lease=%+v err=%v", lease, err)
	}
	service, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second,
		InputLimits: GrantLimits{
			MaxRequests: 512, MaxBytesPerRequest: 64 << 20, MaxCumulativeBytes: 2 << 30, MaxInFlight: 4,
		},
		SinkLimits: GrantLimits{
			MaxRequests: 32, MaxBytesPerRequest: 512 << 20, MaxCumulativeBytes: 1 << 30, MaxInFlight: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	material, err := service.IssueAttemptGrants(context.Background(), IssueGrantsRequest{
		JobID: lease.JobID, AttemptID: lease.AttemptID, WorkerID: lease.WorkerID,
		RecoveryPointFence: lease.RecoveryPointFence,
	})
	if err != nil {
		t.Fatal(err)
	}
	for kind, grantID := range map[GrantKind]string{GrantInput: material.Input.GrantID, GrantSink: material.Sink.GrantID} {
		var grant model.BackupAssetProcessingGrant
		if err := harness.db.First(&grant, "id = ?", grantID).Error; err != nil {
			t.Fatal(err)
		}
		want := GrantLimits{MaxRequests: 512, MaxBytesPerRequest: 64 << 20, MaxCumulativeBytes: 2 << 30, MaxInFlight: 4}
		if kind == GrantSink {
			want = GrantLimits{MaxRequests: 32, MaxBytesPerRequest: 512 << 20, MaxCumulativeBytes: 1 << 30, MaxInFlight: 1}
		}
		if grant.MaxRequests != want.MaxRequests || grant.MaxBytesPerRequest != want.MaxBytesPerRequest ||
			grant.MaxCumulativeBytes != want.MaxCumulativeBytes || grant.MaxInFlight != want.MaxInFlight {
			t.Fatalf("%s limits=%+v, want %+v", kind, grant, want)
		}
	}
}

func newGrantHarness(t *testing.T) (*coordinatorHarness, *GrantService, AttemptLease) {
	t.Helper()
	harness := newCoordinatorHarness(t)
	workerID := harness.registerNoopWorker(t, "d")
	result, err := harness.coordinator.RequestWork(context.Background(), WorkRequest{
		Descriptor: validWorkDescriptor(),
		Interest:   InterestRequest{OwnerKind: InterestSystem, OwnerKey: "grant-test", PriorityClass: PriorityInteractive, Priority: 100},
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := harness.coordinator.Pull(context.Background(), PullRequest{WorkerID: workerID})
	if err != nil || lease.JobID != result.JobID {
		t.Fatalf("Pull: lease=%+v err=%v", lease, err)
	}
	service, err := NewGrantService(harness.db, harness.coordinator.leaseService, harness.clock.Now, GrantConfig{
		TTL: 30 * time.Second, MaxRequests: 8, MaxBytesPerRequest: 32,
		MaxCumulativeBytes: 64, MaxInFlight: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return harness, service, lease
}

func pointerInt64(value int64) *int64 { return &value }
