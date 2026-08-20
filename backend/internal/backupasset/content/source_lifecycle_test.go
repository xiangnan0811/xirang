package content

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRecoveryPointSourceLifecycleContentExactLeaseAndRecoveryResultIsolation(t *testing.T) {
	t.Run("live backup asset session releases its exact fence without touching RecoveryResult state", func(t *testing.T) {
		db, now, pointID, attemptID := newContentLifecycleLeaseTestDB(t, "1")
		controller := newSourceLifecycleLeaseControllerRecorder(t, db, func() time.Time { return now })
		assetGrantID := strings.Repeat("2", 32)
		assetRef := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("3", 64)}
		session, err := AcquireContentLease(context.Background(), controller, ContentLeaseRequest{Ref: assetRef, GrantID: assetGrantID})
		if err != nil {
			t.Fatalf("acquire live asset content lease: %v", err)
		}
		binding := session.Binding()
		pointRef := pointID
		assetGrant := model.BackupAssetDeliveryGrant{
			ID: assetGrantID, DeliveryID: strings.Repeat("4", 32), ResourceKind: string(DeliveryResourceBackupAsset),
			RecoveryPointID: &pointRef, State: string(DeliveryActive), LeaseID: binding.LeaseID,
			LeaseAttemptID: binding.AttemptID, LeaseFenceTokenHash: binding.FenceTokenHash,
		}
		if err := db.Create(&assetGrant).Error; err != nil {
			t.Fatalf("seed live asset grant: %v", err)
		}

		resultGrantID, resultLeaseID := strings.Repeat("5", 32), strings.Repeat("6", 32)
		resultJobID, resultID := strings.Repeat("7", 32), strings.Repeat("8", 32)
		resultFence := strings.Repeat("d", 64)
		resultGrant := model.BackupAssetDeliveryGrant{
			ID: resultGrantID, DeliveryID: strings.Repeat("9", 32), ResourceKind: string(DeliveryResourceRecoveryResult),
			RecoveryJobID: &resultJobID, RecoveryResultID: &resultID, State: string(DeliveryActive), LeaseID: resultLeaseID,
			LeaseAttemptID: strings.Repeat("a", 32), LeaseFenceTokenHash: contentSourceFenceHash(resultFence), InFlight: 1,
		}
		seedRecoveryResultContentAuthority(t, db, pointID, resultJobID, resultID)
		if err := db.Create(&resultGrant).Error; err != nil {
			t.Fatalf("seed RecoveryResult grant: %v", err)
		}
		resultRequest := model.BackupAssetDeliveryRequest{
			ID: strings.Repeat("c", 32), GrantID: resultGrantID, State: string(RequestStreaming), StartedAt: now,
		}
		if err := db.Create(&resultRequest).Error; err != nil {
			t.Fatalf("seed RecoveryResult read: %v", err)
		}
		resultLease := model.RecoveryPointLease{
			ID: resultLeaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderContentSession),
			OwnerID: resultGrantID, AttemptID: resultGrant.LeaseAttemptID, FenceToken: resultFence,
			Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(time.Minute),
			AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
		}
		if err := db.Create(&resultLease).Error; err != nil {
			t.Fatalf("seed RecoveryResult content lease: %v", err)
		}
		seedContentLifecycleAttempt(t, db, attemptID, pointID, backupasset.LifecyclePhaseRevoking)

		owner, broker, _ := newContentSourceLifecycleOwnerForTest(t, db, now, 8)
		broker.lease = controller
		broker.assets[assetGrantID] = AuthorizedAsset{Ref: assetRef}
		broker.leases[assetGrantID] = session
		request := backupasset.SourceLifecycleRequest{
			RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
			Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
		}
		if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), request); err != nil {
			t.Fatalf("prepare exact Content owner with independent RecoveryResult lease: %v", err)
		}
		if releases := controller.releaseSnapshot(); len(releases) != 1 || releases[0] != session.fence {
			t.Fatalf("live Content release count=%d exact_match=%v", len(releases), len(releases) == 1 && releases[0] == session.fence)
		}
		assertContentSourceSettled(t, db, assetGrantID, binding.LeaseID)
		assertRecoveryResultContentRowsUnchanged(t, db, resultGrant, resultRequest, resultLease)
	})

	t.Run("restart cleanup takes over the exact persisted grant lease", func(t *testing.T) {
		db, base, pointID, attemptID := newContentLifecycleLeaseTestDB(t, "e")
		clock := base
		controller := newSourceLifecycleLeaseControllerRecorder(t, db, func() time.Time { return clock })
		grantID := strings.Repeat("1", 32)
		ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("2", 64)}
		session, err := AcquireContentLease(context.Background(), controller, ContentLeaseRequest{Ref: ref, GrantID: grantID})
		if err != nil {
			t.Fatalf("acquire pre-restart content lease: %v", err)
		}
		binding := session.Binding()
		pointRef := pointID
		grant := model.BackupAssetDeliveryGrant{
			ID: grantID, DeliveryID: strings.Repeat("3", 32), ResourceKind: string(DeliveryResourceBackupAsset),
			RecoveryPointID: &pointRef, State: string(DeliveryActive), LeaseID: binding.LeaseID,
			LeaseAttemptID: binding.AttemptID, LeaseFenceTokenHash: binding.FenceTokenHash,
		}
		if err := db.Create(&grant).Error; err != nil {
			t.Fatalf("seed restarted content grant: %v", err)
		}
		seedContentLifecycleAttempt(t, db, attemptID, pointID, backupasset.LifecyclePhaseRevoking)
		clock = base.Add(6 * time.Minute)

		owner, broker, _ := newContentSourceLifecycleOwnerForTest(t, db, clock, 8)
		broker.lease = controller
		request := backupasset.SourceLifecycleRequest{
			RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
			Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
		}
		if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), request); err != nil {
			t.Fatalf("prepare restarted Content owner: %v", err)
		}
		takeovers := controller.takeoverSnapshot()
		if len(takeovers) != 1 || takeovers[0].LeaseID != grant.LeaseID || takeovers[0].OwnerID != grant.ID {
			t.Fatalf("restart takeover count=%d exact_binding=%v", len(takeovers), len(takeovers) == 1 && takeovers[0].LeaseID == grant.LeaseID && takeovers[0].OwnerID == grant.ID)
		}
		if releases := controller.releaseSnapshot(); len(releases) != 1 || releases[0].LeaseID != grant.LeaseID || releases[0].OwnerID != grant.ID {
			t.Fatalf("restart release count=%d exact_binding=%v", len(releases), len(releases) == 1 && releases[0].LeaseID == grant.LeaseID && releases[0].OwnerID == grant.ID)
		}
		assertContentSourceSettled(t, db, grant.ID, grant.LeaseID)
	})

	t.Run("grant lease mismatch fails closed without a force release", func(t *testing.T) {
		db, now, pointID, attemptID := newContentLifecycleLeaseTestDB(t, "4")
		controller := newSourceLifecycleLeaseControllerRecorder(t, db, func() time.Time { return now })
		grantID := strings.Repeat("5", 32)
		persistedGrantLeaseID := strings.Repeat("6", 32)
		actualLeaseID := strings.Repeat("7", 32)
		pointRef := pointID
		grant := model.BackupAssetDeliveryGrant{
			ID: grantID, DeliveryID: strings.Repeat("8", 32), ResourceKind: string(DeliveryResourceBackupAsset),
			RecoveryPointID: &pointRef, State: string(DeliveryActive), LeaseID: persistedGrantLeaseID,
			LeaseAttemptID: strings.Repeat("9", 32), LeaseFenceTokenHash: strings.Repeat("a", 64),
		}
		if err := db.Create(&grant).Error; err != nil {
			t.Fatalf("seed mismatched content grant: %v", err)
		}
		actualLease := model.RecoveryPointLease{
			ID: actualLeaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderContentSession),
			OwnerID: grantID, AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("c", 64),
			Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(-time.Minute),
			AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now.Add(-2 * time.Minute),
		}
		if err := db.Create(&actualLease).Error; err != nil {
			t.Fatalf("seed mismatched active content lease: %v", err)
		}
		seedContentLifecycleAttempt(t, db, attemptID, pointID, backupasset.LifecyclePhaseRevoking)

		owner, broker, _ := newContentSourceLifecycleOwnerForTest(t, db, now, 8)
		broker.lease = controller
		request := backupasset.SourceLifecycleRequest{
			RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
			Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
		}
		if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), request); err == nil {
			t.Fatal("mismatched persisted grant lease was force released")
		}
		takeovers := controller.takeoverSnapshot()
		if len(takeovers) != 1 || takeovers[0].LeaseID != persistedGrantLeaseID || takeovers[0].OwnerID != grantID {
			t.Fatalf("mismatched takeover count=%d exact_persisted_binding=%v", len(takeovers), len(takeovers) == 1 && takeovers[0].LeaseID == persistedGrantLeaseID && takeovers[0].OwnerID == grantID)
		}
		assertContentLeaseStillActive(t, db, actualLease)
	})

	t.Run("mismatched takeover fence and release failure never force lease status", func(t *testing.T) {
		for _, test := range []struct {
			name        string
			configure   func(*sourceLifecycleLeaseControllerRecorder, model.RecoveryPointLease)
			wantRelease bool
		}{
			{
				name: "takeover fence mismatch",
				configure: func(controller *sourceLifecycleLeaseControllerRecorder, row model.RecoveryPointLease) {
					controller.takeoverOverride = func(context.Context, backupasset.TakeoverLeaseRequest) (backupasset.Lease, error) {
						lease := sourceLifecycleLeaseFromRow(row, nowForSourceLifecycleLease(row))
						lease.Fence.LeaseID = strings.Repeat("f", 32)
						return lease, nil
					}
				},
				wantRelease: true,
			},
			{
				name: "controller release failure",
				configure: func(controller *sourceLifecycleLeaseControllerRecorder, row model.RecoveryPointLease) {
					controller.takeoverOverride = func(context.Context, backupasset.TakeoverLeaseRequest) (backupasset.Lease, error) {
						return sourceLifecycleLeaseFromRow(row, nowForSourceLifecycleLease(row)), nil
					}
					controller.releaseOverride = func(context.Context, backupasset.LeaseFence) error {
						return errors.New("closed content lease controller failure")
					}
				},
				wantRelease: true,
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				db, now, pointID, attemptID := newContentLifecycleLeaseTestDB(t, "d")
				controller := newSourceLifecycleLeaseControllerRecorder(t, db, func() time.Time { return now })
				grantID, leaseID := strings.Repeat("1", 32), strings.Repeat("2", 32)
				pointRef := pointID
				grant := model.BackupAssetDeliveryGrant{
					ID: grantID, DeliveryID: strings.Repeat("3", 32), ResourceKind: string(DeliveryResourceBackupAsset),
					RecoveryPointID: &pointRef, State: string(DeliveryActive), LeaseID: leaseID,
					LeaseAttemptID: strings.Repeat("4", 32), LeaseFenceTokenHash: strings.Repeat("5", 64),
				}
				if err := db.Create(&grant).Error; err != nil {
					t.Fatalf("seed controller-failure grant: %v", err)
				}
				leaseRow := model.RecoveryPointLease{
					ID: leaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderContentSession),
					OwnerID: grantID, AttemptID: grant.LeaseAttemptID, FenceToken: strings.Repeat("6", 64),
					Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(-time.Minute),
					AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now.Add(-2 * time.Minute),
				}
				if err := db.Create(&leaseRow).Error; err != nil {
					t.Fatalf("seed controller-failure lease: %v", err)
				}
				seedContentLifecycleAttempt(t, db, attemptID, pointID, backupasset.LifecyclePhaseRevoking)
				test.configure(controller, leaseRow)
				if err := db.Model(&model.BackupAssetDeliveryGrant{}).Where("id = ?", grant.ID).
					Update("lease_fence_token_hash", contentSourceFenceHash(leaseRow.FenceToken)).Error; err != nil {
					t.Fatalf("bind controller-failure grant fence: %v", err)
				}
				owner, broker, _ := newContentSourceLifecycleOwnerForTest(t, db, now, 8)
				broker.lease = controller
				request := backupasset.SourceLifecycleRequest{
					RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
					Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
				}
				if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), request); err == nil {
					t.Fatal("controller mismatch/failure force released a persisted lease")
				}
				if releases := controller.releaseSnapshot(); test.wantRelease && len(releases) != 1 {
					t.Fatalf("controller release count=%d want=1", len(releases))
				}
				assertContentLeaseStillActive(t, db, leaseRow)
			})
		}
	})
}

func TestRecoveryPointSourceLifecycleContentBatchesBrokerAndRecoveryResultProof(t *testing.T) {
	db, now, pointID, attemptID := newContentLifecycleLeaseTestDB(t, "e")
	seedContentLifecycleAttempt(t, db, attemptID, pointID, backupasset.LifecyclePhaseRevoking)
	controller := newSourceLifecycleLeaseControllerRecorder(t, db, func() time.Time { return now })
	owner, broker, cache := newContentSourceLifecycleOwnerForTest(t, db, now, 2)
	broker.lease = controller

	assetGrantIDs := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		grantID := fmt.Sprintf("%032x", 0x100+index)
		assetGrantIDs = append(assetGrantIDs, grantID)
		ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: fmt.Sprintf("%064x", 0x200+index)}
		session, err := AcquireContentLease(context.Background(), controller, ContentLeaseRequest{Ref: ref, GrantID: grantID})
		if err != nil {
			t.Fatalf("acquire batched Content lease %d: %v", index, err)
		}
		binding := session.Binding()
		pointRef := pointID
		if err := db.Create(&model.BackupAssetDeliveryGrant{
			ID: grantID, DeliveryID: fmt.Sprintf("%032x", 0x300+index), ResourceKind: string(DeliveryResourceBackupAsset),
			RecoveryPointID: &pointRef, State: string(DeliveryActive), LeaseID: binding.LeaseID,
			LeaseAttemptID: binding.AttemptID, LeaseFenceTokenHash: binding.FenceTokenHash,
		}).Error; err != nil {
			t.Fatalf("seed batched Content grant %d: %v", index, err)
		}
		broker.mu.Lock()
		broker.assets[grantID] = AuthorizedAsset{Ref: ref}
		broker.leases[grantID] = session
		broker.mu.Unlock()
	}

	resultGrants := make([]model.BackupAssetDeliveryGrant, 0, 5)
	resultRequests := make([]model.BackupAssetDeliveryRequest, 0, 5)
	resultLeases := make([]model.RecoveryPointLease, 0, 5)
	for index := 0; index < 5; index++ {
		grantID := fmt.Sprintf("%032x", 0x400+index)
		leaseID := fmt.Sprintf("%032x", 0x500+index)
		jobID := fmt.Sprintf("%032x", 0x600+index)
		resultID := fmt.Sprintf("%032x", 0x700+index)
		attemptID := fmt.Sprintf("%032x", 0x800+index)
		fenceToken := fmt.Sprintf("%064x", 0x900+index)
		grant := model.BackupAssetDeliveryGrant{
			ID: grantID, DeliveryID: fmt.Sprintf("%032x", 0xa00+index), ResourceKind: string(DeliveryResourceRecoveryResult),
			RecoveryJobID: &jobID, RecoveryResultID: &resultID, State: string(DeliveryActive), LeaseID: leaseID,
			LeaseAttemptID: attemptID, LeaseFenceTokenHash: contentSourceFenceHash(fenceToken),
		}
		seedRecoveryResultContentAuthority(t, db, pointID, jobID, resultID)
		request := model.BackupAssetDeliveryRequest{
			ID: fmt.Sprintf("%032x", 0xb00+index), GrantID: grantID, State: string(RequestSucceeded),
		}
		lease := model.RecoveryPointLease{
			ID: leaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderContentSession), OwnerID: grantID,
			AttemptID: attemptID, FenceToken: fenceToken, Status: string(backupasset.LeaseActive),
			LeaseExpiresAt: now.Add(time.Hour), AbsoluteDeadline: now.Add(2 * time.Hour), LastHeartbeatAt: now,
		}
		if err := db.Create(&lease).Error; err != nil {
			t.Fatalf("seed preserved RecoveryResult lease %d: %v", index, err)
		}
		if err := db.Create(&grant).Error; err != nil {
			t.Fatalf("seed preserved RecoveryResult grant %d: %v", index, err)
		}
		if err := db.Create(&request).Error; err != nil {
			t.Fatalf("seed preserved RecoveryResult request %d: %v", index, err)
		}
		resultGrants = append(resultGrants, grant)
		resultRequests = append(resultRequests, request)
		resultLeases = append(resultLeases, lease)
	}

	type proofObservation struct {
		hasLimit bool
	}
	var proofMu sync.Mutex
	proofObservations := make([]proofObservation, 0, 3)
	callbackName := "task6:content_bounded_recovery_result_proof"
	observeProof := func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if !strings.Contains(sql, "lease_attempt_id") || !strings.Contains(sql, "grant_fence_token_hash") {
			return
		}
		proofMu.Lock()
		proofObservations = append(proofObservations, proofObservation{hasLimit: strings.Contains(sql, "LIMIT 2")})
		proofMu.Unlock()
	}
	if err := db.Callback().Row().After("gorm:row").Register(callbackName, observeProof); err != nil {
		t.Fatalf("register RecoveryResult proof observer: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Row().Remove(callbackName) })

	firstRelease := make(chan struct{}, 1)
	continueRelease := make(chan struct{})
	releaseCalls := 0
	ctx, cancel := context.WithCancel(context.Background())
	controller.releaseOverride = func(releaseCtx context.Context, fence backupasset.LeaseFence) error {
		releaseCalls++
		if releaseCalls == 1 {
			firstRelease <- struct{}{}
			<-continueRelease
		}
		err := controller.inner.Release(releaseCtx, fence)
		if releaseCalls == 2 {
			cancel()
		}
		return err
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
	}
	firstResult := make(chan error, 1)
	go func() { firstResult <- owner.RevokeAndDrainRecoveryPoint(ctx, request) }()
	select {
	case <-firstRelease:
	case <-time.After(time.Second):
		close(continueRelease)
		t.Fatal("Content lifecycle did not reach its first broker lease release")
	}
	broker.mu.Lock()
	marked := make(map[string]bool, len(broker.revokedGrants))
	for grantID := range broker.revokedGrants {
		marked[grantID] = true
	}
	broker.mu.Unlock()
	close(continueRelease)
	firstErr := <-firstResult
	if !errors.Is(firstErr, context.Canceled) {
		t.Fatalf("partial batched Content lifecycle error=%v, want context cancellation", firstErr)
	}
	if len(marked) != 2 || !marked[assetGrantIDs[0]] || !marked[assetGrantIDs[1]] {
		t.Fatalf("first broker batch markers=%d first=%t second=%t, want exact deterministic batch of 2",
			len(marked), marked[assetGrantIDs[0]], marked[assetGrantIDs[1]])
	}

	controller.releaseOverride = nil
	restarted, err := NewSourceLifecycle(db, broker, cache, func() time.Time { return now }, 2)
	if err != nil {
		t.Fatalf("restart Content source lifecycle: %v", err)
	}
	if err := restarted.RevokeAndDrainRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("restart batched Content source lifecycle: %v", err)
	}
	if err := restarted.RevokeAndDrainRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("idempotent batched Content source lifecycle: %v", err)
	}
	for index := range resultGrants {
		assertRecoveryResultContentRowsUnchanged(t, db, resultGrants[index], resultRequests[index], resultLeases[index])
	}
	proofMu.Lock()
	observations := append([]proofObservation(nil), proofObservations...)
	proofMu.Unlock()
	if len(observations) < 3 {
		t.Fatalf("RecoveryResult proof queries=%d, want at least 3 bounded batches", len(observations))
	}
	for index, observation := range observations {
		if !observation.hasLimit {
			t.Fatalf("RecoveryResult proof batch %d limit=%t, want deterministic LIMIT 2", index, observation.hasLimit)
		}
	}
}

func TestRecoveryPointSourceLifecycleContentRejectsUnownedContentSessionsAndPreservesRecoveryResult(t *testing.T) {
	for _, test := range []struct {
		name       string
		seed       string
		seedUnsafe func(*testing.T, *gorm.DB, time.Time, string) model.RecoveryPointLease
	}{
		{
			name: "orphan content lease without a durable grant",
			seed: "8",
			seedUnsafe: func(t *testing.T, db *gorm.DB, now time.Time, pointID string) model.RecoveryPointLease {
				t.Helper()
				lease := model.RecoveryPointLease{
					ID: strings.Repeat("1", 32), RecoveryPointID: pointID,
					HolderType: string(backupasset.LeaseHolderContentSession), OwnerID: strings.Repeat("2", 32),
					AttemptID: strings.Repeat("3", 32), FenceToken: strings.Repeat("4", 64),
					Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(5 * time.Minute),
					AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
				}
				if err := db.Create(&lease).Error; err != nil {
					t.Fatalf("seed orphan Content lease: %v", err)
				}
				return lease
			},
		},
		{
			name: "content lease with an unknown durable resource kind",
			seed: "9",
			seedUnsafe: func(t *testing.T, db *gorm.DB, now time.Time, pointID string) model.RecoveryPointLease {
				t.Helper()
				grantID, leaseID := strings.Repeat("1", 32), strings.Repeat("2", 32)
				pointRef := pointID
				fenceToken := strings.Repeat("3", 64)
				grant := model.BackupAssetDeliveryGrant{
					ID: grantID, DeliveryID: strings.Repeat("4", 32), ResourceKind: "unknown_content_resource",
					RecoveryPointID: &pointRef, State: string(DeliveryActive), LeaseID: leaseID,
					LeaseAttemptID: strings.Repeat("5", 32), LeaseFenceTokenHash: contentSourceFenceHash(fenceToken),
				}
				if err := db.Create(&grant).Error; err != nil {
					t.Fatalf("seed unknown-kind Content grant: %v", err)
				}
				lease := model.RecoveryPointLease{
					ID: leaseID, RecoveryPointID: pointID,
					HolderType: string(backupasset.LeaseHolderContentSession), OwnerID: grantID,
					AttemptID: grant.LeaseAttemptID, FenceToken: fenceToken,
					Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(5 * time.Minute),
					AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
				}
				if err := db.Create(&lease).Error; err != nil {
					t.Fatalf("seed unknown-kind Content lease: %v", err)
				}
				return lease
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, now, pointID, attemptID := newContentLifecycleLeaseTestDB(t, test.seed)
			resultGrantID, resultLeaseID := strings.Repeat("a", 32), strings.Repeat("b", 32)
			resultJobID, resultID := strings.Repeat("c", 32), strings.Repeat("d", 32)
			resultFenceToken := strings.Repeat("e", 64)
			resultGrant := model.BackupAssetDeliveryGrant{
				ID: resultGrantID, DeliveryID: strings.Repeat("f", 32), ResourceKind: string(DeliveryResourceRecoveryResult),
				RecoveryJobID: &resultJobID, RecoveryResultID: &resultID, State: string(DeliveryActive),
				LeaseID: resultLeaseID, LeaseAttemptID: strings.Repeat("6", 32),
				LeaseFenceTokenHash: contentSourceFenceHash(resultFenceToken), InFlight: 1,
			}
			seedRecoveryResultContentAuthority(t, db, pointID, resultJobID, resultID)
			if err := db.Create(&resultGrant).Error; err != nil {
				t.Fatalf("seed valid RecoveryResult grant: %v", err)
			}
			resultRequest := model.BackupAssetDeliveryRequest{
				ID: strings.Repeat("7", 32), GrantID: resultGrantID,
				State: string(RequestStreaming), StartedAt: now,
			}
			if err := db.Create(&resultRequest).Error; err != nil {
				t.Fatalf("seed valid RecoveryResult read: %v", err)
			}
			resultLease := model.RecoveryPointLease{
				ID: resultLeaseID, RecoveryPointID: pointID,
				HolderType: string(backupasset.LeaseHolderContentSession), OwnerID: resultGrantID,
				AttemptID: resultGrant.LeaseAttemptID, FenceToken: resultFenceToken,
				Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(5 * time.Minute),
				AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
			}
			if err := db.Create(&resultLease).Error; err != nil {
				t.Fatalf("seed valid RecoveryResult Content lease: %v", err)
			}
			unsafeLease := test.seedUnsafe(t, db, now, pointID)
			seedContentLifecycleAttempt(t, db, attemptID, pointID, backupasset.LifecyclePhaseRevoking)

			owner, _, _ := newContentSourceLifecycleOwnerForTest(t, db, now, 8)
			err := owner.RevokeAndDrainRecoveryPoint(context.Background(), backupasset.SourceLifecycleRequest{
				RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
				Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
			})
			if !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("unowned Content lease proof error=%v, want closed ErrConflict", err)
			}
			assertContentLeaseStillActive(t, db, unsafeLease)
			assertRecoveryResultContentRowsUnchanged(t, db, resultGrant, resultRequest, resultLease)
		})
	}
}

func TestRecoveryPointSourceLifecycleContentRequiresExactRecoveryResultLeaseBinding(t *testing.T) {
	for index, test := range []struct {
		name   string
		mutate func(*model.BackupAssetDeliveryGrant, *model.RecoveryPointLease)
	}{
		{
			name: "lease attempt differs from the preserved grant",
			mutate: func(_ *model.BackupAssetDeliveryGrant, lease *model.RecoveryPointLease) {
				lease.AttemptID = strings.Repeat("0", 32)
			},
		},
		{
			name: "lease fence token hash differs from the preserved grant",
			mutate: func(grant *model.BackupAssetDeliveryGrant, _ *model.RecoveryPointLease) {
				grant.LeaseFenceTokenHash = strings.Repeat("0", 64)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, now, pointID, attemptID := newContentLifecycleLeaseTestDB(t, fmt.Sprintf("%x", index+5))

			validGrantID, validLeaseID := strings.Repeat("a", 32), strings.Repeat("b", 32)
			validJobID, validResultID := strings.Repeat("c", 32), strings.Repeat("d", 32)
			validFenceToken := strings.Repeat("f", 64)
			validGrant := model.BackupAssetDeliveryGrant{
				ID: validGrantID, DeliveryID: strings.Repeat("e", 32), ResourceKind: string(DeliveryResourceRecoveryResult),
				RecoveryJobID: &validJobID, RecoveryResultID: &validResultID, State: string(DeliveryActive),
				LeaseID: validLeaseID, LeaseAttemptID: strings.Repeat("1", 32),
				LeaseFenceTokenHash: contentSourceFenceHash(validFenceToken), InFlight: 1,
			}
			seedRecoveryResultContentAuthority(t, db, pointID, validJobID, validResultID)
			if err := db.Create(&validGrant).Error; err != nil {
				t.Fatalf("seed valid RecoveryResult grant: %v", err)
			}
			validRequest := model.BackupAssetDeliveryRequest{
				ID: strings.Repeat("2", 32), GrantID: validGrantID,
				State: string(RequestStreaming), StartedAt: now,
			}
			if err := db.Create(&validRequest).Error; err != nil {
				t.Fatalf("seed valid RecoveryResult request: %v", err)
			}
			validLease := model.RecoveryPointLease{
				ID: validLeaseID, RecoveryPointID: pointID,
				HolderType: string(backupasset.LeaseHolderContentSession), OwnerID: validGrantID,
				AttemptID: validGrant.LeaseAttemptID, FenceToken: validFenceToken,
				Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(5 * time.Minute),
				AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
			}
			if err := db.Create(&validLease).Error; err != nil {
				t.Fatalf("seed valid RecoveryResult lease: %v", err)
			}

			unsafeGrantID, unsafeLeaseID := strings.Repeat("3", 32), strings.Repeat("4", 32)
			unsafeJobID, unsafeResultID := strings.Repeat("6", 32), strings.Repeat("7", 32)
			unsafeFenceToken := strings.Repeat("8", 64)
			unsafeGrant := model.BackupAssetDeliveryGrant{
				ID: unsafeGrantID, DeliveryID: strings.Repeat("9", 32), ResourceKind: string(DeliveryResourceRecoveryResult),
				RecoveryJobID: &unsafeJobID, RecoveryResultID: &unsafeResultID, State: string(DeliveryActive),
				LeaseID: unsafeLeaseID, LeaseAttemptID: strings.Repeat("a", 32),
				LeaseFenceTokenHash: contentSourceFenceHash(unsafeFenceToken), InFlight: 1,
			}
			seedRecoveryResultContentAuthority(t, db, pointID, unsafeJobID, unsafeResultID)
			unsafeLease := model.RecoveryPointLease{
				ID: unsafeLeaseID, RecoveryPointID: pointID,
				HolderType: string(backupasset.LeaseHolderContentSession), OwnerID: unsafeGrantID,
				AttemptID: unsafeGrant.LeaseAttemptID, FenceToken: unsafeFenceToken,
				Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(5 * time.Minute),
				AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
			}
			test.mutate(&unsafeGrant, &unsafeLease)
			if err := db.Create(&unsafeGrant).Error; err != nil {
				t.Fatalf("seed mismatched RecoveryResult grant: %v", err)
			}
			unsafeRequest := model.BackupAssetDeliveryRequest{
				ID: strings.Repeat("b", 32), GrantID: unsafeGrantID,
				State: string(RequestStreaming), StartedAt: now,
			}
			if err := db.Create(&unsafeRequest).Error; err != nil {
				t.Fatalf("seed mismatched RecoveryResult request: %v", err)
			}
			if err := db.Create(&unsafeLease).Error; err != nil {
				t.Fatalf("seed mismatched RecoveryResult lease: %v", err)
			}
			seedContentLifecycleAttempt(t, db, attemptID, pointID, backupasset.LifecyclePhaseRevoking)

			owner, _, _ := newContentSourceLifecycleOwnerForTest(t, db, now, 8)
			err := owner.RevokeAndDrainRecoveryPoint(context.Background(), backupasset.SourceLifecycleRequest{
				RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
				Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
			})
			if !errors.Is(err, backupasset.ErrConflict) {
				t.Errorf("mismatched RecoveryResult lease binding error=%v, want closed ErrConflict", err)
			}
			assertRecoveryResultContentRowsUnchanged(t, db, validGrant, validRequest, validLease)
			assertRecoveryResultContentRowsUnchanged(t, db, unsafeGrant, unsafeRequest, unsafeLease)
		})
	}
}

func TestRecoveryPointSourceLifecycleContentRejectsRecoveryResultFromDifferentSourcePoint(t *testing.T) {
	db, now, pointID, attemptID := newContentLifecycleLeaseTestDB(t, "4")
	otherPointID := strings.Repeat("5", 32)
	jobID, resultID := strings.Repeat("6", 32), strings.Repeat("7", 32)
	grantID, leaseID := strings.Repeat("8", 32), strings.Repeat("9", 32)
	fenceToken := strings.Repeat("a", 64)
	seedRecoveryResultContentAuthority(t, db, otherPointID, jobID, resultID)

	grant := model.BackupAssetDeliveryGrant{
		ID: grantID, DeliveryID: strings.Repeat("b", 32), ResourceKind: string(DeliveryResourceRecoveryResult),
		RecoveryJobID: &jobID, RecoveryResultID: &resultID, State: string(DeliveryActive),
		LeaseID: leaseID, LeaseAttemptID: strings.Repeat("c", 32),
		LeaseFenceTokenHash: contentSourceFenceHash(fenceToken), InFlight: 1,
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("seed cross-point RecoveryResult grant: %v", err)
	}
	request := model.BackupAssetDeliveryRequest{
		ID: strings.Repeat("d", 32), GrantID: grantID, State: string(RequestStreaming), StartedAt: now,
	}
	if err := db.Create(&request).Error; err != nil {
		t.Fatalf("seed cross-point RecoveryResult request: %v", err)
	}
	lease := model.RecoveryPointLease{
		ID: leaseID, RecoveryPointID: pointID,
		HolderType: string(backupasset.LeaseHolderContentSession), OwnerID: grantID,
		AttemptID: grant.LeaseAttemptID, FenceToken: fenceToken,
		Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(5 * time.Minute),
		AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
	}
	if err := db.Create(&lease).Error; err != nil {
		t.Fatalf("seed cross-point RecoveryResult lease: %v", err)
	}
	seedContentLifecycleAttempt(t, db, attemptID, pointID, backupasset.LifecyclePhaseRevoking)

	owner, _, _ := newContentSourceLifecycleOwnerForTest(t, db, now, 8)
	err := owner.RevokeAndDrainRecoveryPoint(context.Background(), backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("cross-point RecoveryResult lease proof error=%v, want closed ErrConflict", err)
	}
	assertRecoveryResultContentRowsUnchanged(t, db, grant, request, lease)
}

func TestRecoveryPointSourceLifecycleContentRejectsInvalidLiveBrokerSessionWithoutRelease(t *testing.T) {
	for index, test := range []struct {
		name    string
		corrupt func(*ContentLeaseSession, ContentLeaseController)
	}{
		{
			name: "wrong lease",
			corrupt: func(session *ContentLeaseSession, _ ContentLeaseController) {
				session.fence.LeaseID = strings.Repeat("0", 32)
			},
		},
		{
			name: "wrong attempt",
			corrupt: func(session *ContentLeaseSession, _ ContentLeaseController) {
				session.fence.AttemptID = strings.Repeat("0", 32)
			},
		},
		{
			name: "wrong fence token",
			corrupt: func(session *ContentLeaseSession, _ ContentLeaseController) {
				session.fence.FenceToken = strings.Repeat("0", 64)
			},
		},
		{
			name: "wrong controller",
			corrupt: func(session *ContentLeaseSession, controller ContentLeaseController) {
				session.controller = controller
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, now, pointID, _ := newContentLifecycleLeaseTestDB(t, fmt.Sprintf("%x", index+8))
			controller := newSourceLifecycleLeaseControllerRecorder(t, db, func() time.Time { return now })
			alternate := newSourceLifecycleLeaseControllerRecorder(t, db, func() time.Time { return now })
			grantID := strings.Repeat("1", 32)
			ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("2", 64)}
			session, err := AcquireContentLease(context.Background(), controller, ContentLeaseRequest{Ref: ref, GrantID: grantID})
			if err != nil {
				t.Fatalf("acquire live Broker session: %v", err)
			}
			binding := session.Binding()
			pointRef := pointID
			grant := model.BackupAssetDeliveryGrant{
				ID: grantID, DeliveryID: strings.Repeat("3", 32), ResourceKind: string(DeliveryResourceBackupAsset),
				RecoveryPointID: &pointRef, State: string(DeliveryActive), LeaseID: binding.LeaseID,
				LeaseAttemptID: binding.AttemptID, LeaseFenceTokenHash: binding.FenceTokenHash,
			}
			if err := db.Create(&grant).Error; err != nil {
				t.Fatalf("seed live Broker grant: %v", err)
			}

			session.mu.Lock()
			test.corrupt(session, alternate)
			session.mu.Unlock()
			broker := newSourceLifecycleBrokerForTest(db, nil, now, controller)
			broker.assets[grantID] = AuthorizedAsset{Ref: ref}
			broker.leases[grantID] = session
			err = broker.drainRecoveryPoint(context.Background(), pointID, []string{grantID})
			if releases := append(controller.releaseSnapshot(), alternate.releaseSnapshot()...); len(releases) != 0 {
				t.Errorf("invalid live Broker session invoked Release count=%d", len(releases))
			}
			if !errors.Is(err, backupasset.ErrConflict) {
				t.Errorf("invalid live Broker session error=%v, want closed ErrConflict", err)
			}
			var lease model.RecoveryPointLease
			if err := db.First(&lease, "id = ?", binding.LeaseID).Error; err != nil {
				t.Fatalf("load durable live Broker lease: %v", err)
			}
			assertContentLeaseStillActive(t, db, lease)
		})
	}
}

func TestRecoveryPointSourceLifecycleContentRetriesTransientLiveSessionRelease(t *testing.T) {
	db, now, pointID, _ := newContentLifecycleLeaseTestDB(t, "c")
	controller := newSourceLifecycleLeaseControllerRecorder(t, db, func() time.Time { return now })
	grantID := strings.Repeat("1", 32)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("2", 64)}
	session, err := AcquireContentLease(context.Background(), controller, ContentLeaseRequest{Ref: ref, GrantID: grantID})
	if err != nil {
		t.Fatalf("acquire retryable live Broker session: %v", err)
	}
	binding := session.Binding()
	pointRef := pointID
	grant := model.BackupAssetDeliveryGrant{
		ID: grantID, DeliveryID: strings.Repeat("3", 32), ResourceKind: string(DeliveryResourceBackupAsset),
		RecoveryPointID: &pointRef, State: string(DeliveryActive), LeaseID: binding.LeaseID,
		LeaseAttemptID: binding.AttemptID, LeaseFenceTokenHash: binding.FenceTokenHash,
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("seed retryable live Broker grant: %v", err)
	}
	releaseAttempts := 0
	controller.releaseOverride = func(ctx context.Context, fence backupasset.LeaseFence) error {
		releaseAttempts++
		if releaseAttempts == 1 {
			return errors.New("closed transient content release failure")
		}
		return controller.inner.Release(ctx, fence)
	}
	broker := newSourceLifecycleBrokerForTest(db, nil, now, controller)
	broker.assets[grantID] = AuthorizedAsset{Ref: ref}
	broker.leases[grantID] = session

	if err := broker.drainRecoveryPoint(context.Background(), pointID, []string{grantID}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("first transient Broker drain error=%v, want closed ErrConflict", err)
	}
	var activeLease model.RecoveryPointLease
	if err := db.First(&activeLease, "id = ?", binding.LeaseID).Error; err != nil {
		t.Fatalf("load active lease after transient release: %v", err)
	}
	assertContentLeaseStillActive(t, db, activeLease)
	if err := broker.drainRecoveryPoint(context.Background(), pointID, []string{grantID}); err != nil {
		t.Errorf("retry transient Broker drain: %v", err)
	}
	if releases := controller.releaseSnapshot(); len(releases) != 2 {
		t.Errorf("transient Broker release calls=%d, want retry then memoized success", len(releases))
	}
	var releasedLease model.RecoveryPointLease
	if err := db.First(&releasedLease, "id = ?", binding.LeaseID).Error; err != nil {
		t.Fatalf("load released lease after retry: %v", err)
	}
	if releasedLease.Status != string(backupasset.LeaseReleased) {
		t.Errorf("lease status after retry=%q, want released", releasedLease.Status)
	}
}

func TestContentLifecycleErrorsRedactPrivatePaths(t *testing.T) {
	db, now, pointID, attemptID := newContentLifecycleLeaseTestDB(t, "b")
	seedContentLifecycleAttempt(t, db, attemptID, pointID, backupasset.LifecyclePhaseCleaning)
	root := filepath.Join(t.TempDir(), "cache")
	config := testCacheConfig(root)
	config.MemoryObjectBytes = 1
	cache := newDiskCacheForTest(t, config)
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	object := testCacheObject(8)
	object.Ref.RecoveryPointID = pointID
	if _, err := cache.Materialize(context.Background(), object, newCacheSourceFake([]byte("12345678"), object)); err != nil {
		t.Fatalf("materialize private-path cache fixture: %v", err)
	}
	privateCanary := filepath.Join(t.TempDir(), "provider-private-chunk-CANARY")
	cache.removeChunkFile = func(string) error {
		return &os.PathError{Op: "remove", Path: privateCanary, Err: syscall.EACCES}
	}
	broker := newSourceLifecycleBrokerForTest(db, cache, now, nil)
	owner, err := NewSourceLifecycle(db, broker, cache, func() time.Time { return now }, 8)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecycleCleanup,
	}
	err = owner.RevokeAndDrainRecoveryPoint(context.Background(), request)
	if err == nil {
		t.Fatal("private-path cache deletion failure was not surfaced")
	}
	if strings.Contains(err.Error(), privateCanary) || strings.Contains(err.Error(), filepath.Base(privateCanary)) {
		t.Fatalf("Content owner error leaked raw cache path canary: %v", err)
	}
}

func TestContentLifecycleBrokerMarkersAreBounded(t *testing.T) {
	harness, owner, firstRequest := newRealIssueContentLifecycleHarness(t)
	requests := []backupasset.SourceLifecycleRequest{firstRequest}
	for index := 1; index < 8; index++ {
		pointID := fmt.Sprintf("%032x", 0x200+index)
		attemptID := fmt.Sprintf("%032x", 0x300+index)
		if err := harness.db.Create(&model.RecoveryPoint{ID: pointID, RepositoryID: harness.asset.RepositoryID}).Error; err != nil {
			t.Fatalf("seed marker point %d: %v", index, err)
		}
		seedContentLifecycleAttempt(t, harness.db, attemptID, pointID, backupasset.LifecyclePhaseRevoking)
		requests = append(requests, backupasset.SourceLifecycleRequest{
			RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
			Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
		})
	}
	markerController := newSourceLifecycleLeaseControllerRecorder(t, harness.db, func() time.Time { return harness.now })
	harness.broker.lease = markerController
	for index, request := range requests {
		grantID := fmt.Sprintf("%032x", 0x400+index)
		fence := backupasset.LeaseFence{
			LeaseID: fmt.Sprintf("%032x", 0x600+index), RecoveryPointID: request.RecoveryPointID,
			HolderType: backupasset.LeaseHolderContentSession, OwnerID: grantID,
			AttemptID: fmt.Sprintf("%032x", 0x700+index), FenceToken: fmt.Sprintf("%064x", 0x800+index),
		}
		pointRef := request.RecoveryPointID
		if err := harness.db.Create(&model.BackupAssetDeliveryGrant{
			ID: grantID, DeliveryID: fmt.Sprintf("%032x", 0x900+index),
			ResourceKind: string(DeliveryResourceBackupAsset), RecoveryPointID: &pointRef,
			State: string(DeliveryActive), LeaseID: fence.LeaseID,
			LeaseAttemptID: fence.AttemptID, LeaseFenceTokenHash: contentSourceFenceHash(fence.FenceToken),
		}).Error; err != nil {
			t.Fatalf("seed marker grant %d: %v", index, err)
		}
		if err := harness.db.Create(&model.RecoveryPointLease{
			ID: fence.LeaseID, RecoveryPointID: request.RecoveryPointID,
			HolderType: string(fence.HolderType), OwnerID: grantID, AttemptID: fence.AttemptID,
			FenceToken: fence.FenceToken, Status: string(backupasset.LeaseActive),
			LeaseExpiresAt: harness.now.Add(5 * time.Minute), AbsoluteDeadline: harness.now.Add(time.Hour),
			LastHeartbeatAt: harness.now,
		}).Error; err != nil {
			t.Fatalf("seed marker lease %d: %v", index, err)
		}
		harness.broker.mu.Lock()
		harness.broker.assets[grantID] = AuthorizedAsset{Ref: backupasset.AssetRef{
			RecoveryPointID: request.RecoveryPointID, EntryID: fmt.Sprintf("%064x", 0x500+index),
		}}
		harness.broker.leases[grantID] = &ContentLeaseSession{
			controller: markerController, fence: fence,
			binding: ContentLeaseBinding{
				LeaseID: fence.LeaseID, AttemptID: fence.AttemptID,
				FenceTokenHash: contentSourceFenceHash(fence.FenceToken),
				LeaseExpiresAt: harness.now.Add(5 * time.Minute), AbsoluteDeadline: harness.now.Add(time.Hour),
			},
			lastHeartbeatAt: harness.now,
		}
		harness.broker.mu.Unlock()
		if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), request); err != nil {
			t.Fatalf("drain marker point %d: %v", index, err)
		}
	}
	harness.broker.mu.Lock()
	issueMarkers := len(harness.broker.assetIssues)
	revokedMarkers := len(harness.broker.revokedGrants)
	harness.broker.mu.Unlock()
	if issueMarkers != 0 || revokedMarkers != 0 {
		t.Errorf("drained Content markers issue=%d revoked=%d, want bounded reclamation", issueMarkers, revokedMarkers)
	}

	harness.broker.lease = harness.lease
	leaseCalls := 0
	harness.lease.acquire = func(context.Context, backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
		leaseCalls++
		return backupasset.Lease{}, backupasset.ErrConflict
	}
	harness.authorizer.authorize = func(ref backupasset.AssetRef) (AuthorizedAsset, error) {
		asset := *harness.asset
		asset.Ref = ref
		return asset, nil
	}
	late := harness.issueRequest()
	late.Ref = backupasset.AssetRef{RecoveryPointID: firstRequest.RecoveryPointID, EntryID: strings.Repeat("f", 64)}
	late.Resource = DeliveryResource{}
	late.Session.Role = late.Actor.Role
	if _, err := harness.broker.Issue(context.Background(), late); !errors.Is(err, ErrContentSourceUnavailable) {
		t.Fatalf("late lifecycle Issue error=%v, want durable source denial", err)
	}
	if leaseCalls != 0 {
		t.Fatalf("late lifecycle Issue reached lease acquisition %d times", leaseCalls)
	}
}

func TestContentLifecycleBrokerZeroGrantMarkersAreBounded(t *testing.T) {
	harness, owner, _ := newRealIssueContentLifecycleHarness(t)
	for index := 0; index < 32; index++ {
		pointID := fmt.Sprintf("%032x", 0xb00+index)
		attemptID := fmt.Sprintf("%032x", 0xc00+index)
		if err := harness.db.Create(&model.RecoveryPoint{ID: pointID, RepositoryID: harness.asset.RepositoryID}).Error; err != nil {
			t.Fatalf("seed zero-grant point %d: %v", index, err)
		}
		seedContentLifecycleAttempt(t, harness.db, attemptID, pointID, backupasset.LifecyclePhaseRevoking)
		if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), backupasset.SourceLifecycleRequest{
			RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
			Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
		}); err != nil {
			t.Fatalf("drain zero-grant point %d: %v", index, err)
		}
	}
	harness.broker.mu.Lock()
	issueMarkers := len(harness.broker.assetIssues)
	harness.broker.mu.Unlock()
	if issueMarkers != 0 {
		t.Fatalf("zero-grant Content lifecycle retained issue markers=%d, want bounded reclamation", issueMarkers)
	}
}

func TestRecoveryPointSourceLifecycleContentRevokesAndDrainsExactPoint(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/source-lifecycle.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{}, &model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{}); err != nil {
		t.Fatalf("migrate source lifecycle tables: %v", err)
	}
	installProductionContentRevocationReasonConstraint(t, db)
	now := time.Date(2026, 8, 17, 14, 23, 0, 0, time.UTC)
	pointID, otherPointID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	attemptID, grantID, leaseID := strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 32)
	for _, point := range []model.RecoveryPoint{{ID: pointID, RepositoryID: strings.Repeat("6", 32)}, {ID: otherPointID, RepositoryID: strings.Repeat("6", 32)}} {
		if err := db.Create(&point).Error; err != nil {
			t.Fatalf("seed point: %v", err)
		}
	}
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking)}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}
	pointRef := pointID
	leaseAttemptID := strings.Repeat("9", 32)
	leaseFenceToken := strings.Repeat("a", 64)
	grant := model.BackupAssetDeliveryGrant{
		ID: grantID, DeliveryID: strings.Repeat("7", 32), ResourceKind: string(DeliveryResourceBackupAsset),
		RecoveryPointID: &pointRef, State: string(DeliveryActive), LeaseID: leaseID,
		LeaseAttemptID: leaseAttemptID, LeaseFenceTokenHash: contentSourceFenceHash(leaseFenceToken), InFlight: 0,
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("seed content grant: %v", err)
	}
	request := model.BackupAssetDeliveryRequest{ID: strings.Repeat("8", 32), GrantID: grantID, State: string(RequestSucceeded), StartedAt: now}
	if err := db.Create(&request).Error; err != nil {
		t.Fatalf("seed content request history: %v", err)
	}
	lease := model.RecoveryPointLease{
		ID: leaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderContentSession),
		OwnerID: grantID, AttemptID: leaseAttemptID, FenceToken: leaseFenceToken, Status: string(backupasset.LeaseActive),
		LeaseExpiresAt: now.Add(-time.Minute), AbsoluteDeadline: now.Add(2 * time.Hour), LastHeartbeatAt: now.Add(-2 * time.Minute),
	}
	if err := db.Create(&lease).Error; err != nil {
		t.Fatalf("seed content lease: %v", err)
	}

	owner, _, _ := newContentSourceLifecycleOwnerForTest(t, db, now, 16)
	lifecycleRequest := backupasset.SourceLifecycleRequest{RecoveryPointID: pointID, LifecycleAttemptID: attemptID, Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare}
	if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), lifecycleRequest); err != nil {
		t.Fatalf("prepare content lifecycle: %v", err)
	}
	assertContentSourceSettled(t, db, grantID, leaseID)
	var settledGrant model.BackupAssetDeliveryGrant
	if err := db.First(&settledGrant, "id = ?", grantID).Error; err != nil {
		t.Fatalf("load settled content grant: %v", err)
	}
	var historyCount int64
	if err := db.Model(&model.BackupAssetDeliveryRequest{}).Where("id = ?", request.ID).Count(&historyCount).Error; err != nil || historyCount != 1 {
		t.Fatalf("content request history count=%d err=%v, want preserved", historyCount, err)
	}

	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attemptID).Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
		t.Fatalf("advance lifecycle: %v", err)
	}
	lifecycleRequest.Stage = backupasset.SourceLifecycleCleanup
	restartedOwner, _, _ := newContentSourceLifecycleOwnerForTest(t, db, now, 16)
	if err := restartedOwner.RevokeAndDrainRecoveryPoint(context.Background(), lifecycleRequest); err != nil {
		t.Fatalf("cleanup content lifecycle: %v", err)
	}
	if err := restartedOwner.RevokeAndDrainRecoveryPoint(context.Background(), lifecycleRequest); err != nil {
		t.Fatalf("idempotent cleanup content lifecycle: %v", err)
	}
	assertContentSourceSettled(t, db, grantID, leaseID)
	var replayedGrant model.BackupAssetDeliveryGrant
	if err := db.First(&replayedGrant, "id = ?", grantID).Error; err != nil {
		t.Fatalf("load replayed content grant: %v", err)
	}
	if replayedGrant.Version != settledGrant.Version || replayedGrant.RevocationReason != settledGrant.RevocationReason ||
		!replayedGrant.RevokedAt.Equal(*settledGrant.RevokedAt) {
		t.Fatalf("restarted Content owner changed settled grant version=%d reason_match=%v revoked_at_match=%v",
			replayedGrant.Version, replayedGrant.RevocationReason == settledGrant.RevocationReason,
			replayedGrant.RevokedAt.Equal(*settledGrant.RevokedAt))
	}
}

func installProductionContentRevocationReasonConstraint(t *testing.T, db *gorm.DB) {
	t.Helper()
	// AutoMigrate does not preserve the production migration CHECK. This trigger
	// enforces the same closed allowlist on the lifecycle UPDATE exercised here.
	if err := db.Exec(`
		CREATE TRIGGER backup_asset_delivery_grants_revocation_reason_check
		BEFORE UPDATE OF revocation_reason ON backup_asset_delivery_grants
		WHEN NEW.revocation_reason NOT IN (
			'', 'logout', 'session_revoked', 'session_changed', 'permission_changed',
			'ownership_changed', 'classification_changed', 'point_unavailable',
			'source_changed', 'lease_lost', 'expired', 'budget_exhausted',
			'feature_disabled', 'shutdown', 'process_restarted', 'audit_failed',
			'request_failed', 'cache_invalid'
		)
		BEGIN
			SELECT RAISE(ABORT, 'backup_asset_delivery_grants_revocation_reason_check');
		END
	`).Error; err != nil {
		t.Fatalf("install production revocation reason constraint: %v", err)
	}
}

func TestRecoveryPointSourceLifecycleContentDrainsExactBrokerStateAndEvictsExactCache(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/source-lifecycle-broker.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{}, &model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{}); err != nil {
		t.Fatalf("migrate source lifecycle tables: %v", err)
	}
	now := time.Date(2026, 8, 17, 15, 59, 0, 0, time.UTC)
	pointID, otherPointID := strings.Repeat("b", 32), strings.Repeat("c", 32)
	attemptID, grantID, leaseID := strings.Repeat("d", 32), strings.Repeat("e", 32), strings.Repeat("f", 32)
	for _, point := range []model.RecoveryPoint{{ID: pointID, RepositoryID: strings.Repeat("1", 32)}, {ID: otherPointID, RepositoryID: strings.Repeat("1", 32)}} {
		if err := db.Create(&point).Error; err != nil {
			t.Fatalf("seed point: %v", err)
		}
	}
	pointRef := pointID
	leaseAttemptID := strings.Repeat("4", 32)
	leaseFenceToken := strings.Repeat("5", 64)
	grant := model.BackupAssetDeliveryGrant{
		ID: grantID, DeliveryID: strings.Repeat("2", 32), ResourceKind: string(DeliveryResourceBackupAsset),
		RecoveryPointID: &pointRef, State: string(DeliveryActive), LeaseID: leaseID,
		LeaseAttemptID: leaseAttemptID, LeaseFenceTokenHash: contentSourceFenceHash(leaseFenceToken), InFlight: 0,
	}
	if err := db.Create(&grant).Error; err != nil {
		t.Fatalf("seed content grant: %v", err)
	}
	requestHistory := model.BackupAssetDeliveryRequest{ID: strings.Repeat("3", 32), GrantID: grantID, State: string(RequestSucceeded), StartedAt: now}
	if err := db.Create(&requestHistory).Error; err != nil {
		t.Fatalf("seed request history: %v", err)
	}
	lease := model.RecoveryPointLease{
		ID: leaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderContentSession), OwnerID: grantID,
		AttemptID: leaseAttemptID, FenceToken: leaseFenceToken, Status: string(backupasset.LeaseActive),
		LeaseExpiresAt: now.Add(time.Hour), AbsoluteDeadline: now.Add(2 * time.Hour), LastHeartbeatAt: now,
	}
	if err := db.Create(&lease).Error; err != nil {
		t.Fatalf("seed content lease: %v", err)
	}

	config := testCacheConfig(filepath.Join(t.TempDir(), "cache"))
	config.MemoryObjectBytes = 8
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: config, Now: func() time.Time { return now }, Random: rand.Reader,
		SourceRoots: &cacheRootValidatorFake{}, VerifyMount: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	memoryObject := testCacheObject(4)
	memoryObject.Ref.RecoveryPointID = pointID
	diskObject := testCacheObject(16)
	diskObject.Ref.RecoveryPointID = pointID
	diskObject.ContentFingerprint = "source-lifecycle-disk"
	otherObject := testCacheObject(4)
	otherObject.Ref.RecoveryPointID = otherPointID
	otherObject.ContentFingerprint = "source-lifecycle-other"
	for _, fixture := range []struct {
		object  CacheObject
		payload string
	}{
		{object: memoryObject, payload: "memo"},
		{object: diskObject, payload: "disk-object-data"},
		{object: otherObject, payload: "stay"},
	} {
		if _, err := cache.Materialize(context.Background(), fixture.object, newCacheSourceFake([]byte(fixture.payload), fixture.object)); err != nil {
			t.Fatalf("materialize cache fixture: %v", err)
		}
	}

	leaseController, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	broker := &Broker{
		db: db, now: func() time.Time { return now }, accepting: true, metrics: NoopMetrics{}, cache: cache,
		lease:  leaseController,
		leases: make(map[string]*ContentLeaseSession), assets: make(map[string]AuthorizedAsset),
		recoveryResults: make(map[string]AuthorizedRecoveryResult), derivedBindings: make(map[string]DerivedRepresentation),
		reads: make(map[string]map[string]activeContentRead), revokedGrants: make(map[string]struct{}), inFlight: make(map[backupasset.ProviderKind]int),
	}
	otherGrantID, resultGrantID := strings.Repeat("6", 32), strings.Repeat("7", 32)
	otherLease, resultLease := &ContentLeaseSession{}, &ContentLeaseSession{}
	otherReadDone, resultReadDone := make(chan struct{}), make(chan struct{})
	resultCanceled := make(chan struct{})
	broker.assets[otherGrantID] = AuthorizedAsset{Ref: backupasset.AssetRef{RecoveryPointID: otherPointID, EntryID: strings.Repeat("8", 64)}}
	broker.leases[otherGrantID] = otherLease
	broker.reads[otherGrantID] = map[string]activeContentRead{strings.Repeat("9", 32): {provider: backupasset.ProviderRsync, cancel: func() {}, done: otherReadDone}}
	broker.recoveryResults[resultGrantID] = AuthorizedRecoveryResult{RecoveryPointID: pointID}
	broker.leases[resultGrantID] = resultLease
	broker.reads[resultGrantID] = map[string]activeContentRead{strings.Repeat("a", 32): {provider: backupasset.ProviderRsync, cancel: func() { close(resultCanceled) }, done: resultReadDone}}

	broker.assets[grantID] = AuthorizedAsset{Ref: backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("b", 64)}}
	grantFence := backupasset.LeaseFence{
		LeaseID: leaseID, RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderContentSession,
		OwnerID: grantID, AttemptID: leaseAttemptID, FenceToken: leaseFenceToken,
	}
	broker.leases[grantID] = &ContentLeaseSession{
		controller: leaseController, fence: grantFence,
		binding: ContentLeaseBinding{
			LeaseID: leaseID, AttemptID: leaseAttemptID, FenceTokenHash: contentSourceFenceHash(leaseFenceToken),
			LeaseExpiresAt: lease.LeaseExpiresAt, AbsoluteDeadline: lease.AbsoluteDeadline,
		},
		lastHeartbeatAt: now,
	}
	readCtx, cancelRead := context.WithCancel(context.Background())
	done, registered := broker.registerRead(grantID, strings.Repeat("c", 32), strings.Repeat("d", 32), backupasset.ProviderRsync, cancelRead)
	if !registered {
		t.Fatal("register exact-point read")
	}
	go func() {
		<-readCtx.Done()
		broker.unregisterRead(grantID, strings.Repeat("c", 32), done)
	}()
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking)}).Error; err != nil {
		t.Fatalf("claim lifecycle attempt: %v", err)
	}

	owner, err := NewSourceLifecycle(db, broker, cache, func() time.Time { return now }, 1)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	lifecycleRequest := backupasset.SourceLifecycleRequest{RecoveryPointID: pointID, LifecycleAttemptID: attemptID, Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare}
	if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), lifecycleRequest); err != nil {
		t.Fatalf("prepare content lifecycle: %v", err)
	}
	assertContentSourceSettled(t, db, grantID, leaseID)
	if !cacheHasEntryForTest(cache, memoryObject) || !cacheHasEntryForTest(cache, diskObject) || !cacheHasEntryForTest(cache, otherObject) {
		t.Fatal("prepare removed cache payload instead of separating cleanup")
	}

	broker.mu.Lock()
	_, exactAssetRemains := broker.assets[grantID]
	exactReads := len(broker.reads[grantID])
	_, resultRemains := broker.recoveryResults[resultGrantID]
	resultLeaseRemains := broker.leases[resultGrantID] == resultLease
	resultReads := len(broker.reads[resultGrantID])
	otherAsset := broker.assets[otherGrantID]
	otherLeaseRemains := broker.leases[otherGrantID] == otherLease
	otherReads := len(broker.reads[otherGrantID])
	broker.mu.Unlock()
	if exactAssetRemains || exactReads != 0 {
		t.Fatalf("exact source broker state asset=%v reads=%d, want drained", exactAssetRemains, exactReads)
	}
	if !resultRemains || !resultLeaseRemains || resultReads != 1 {
		t.Fatalf("RecoveryResult resources changed result=%v lease=%v reads=%d", resultRemains, resultLeaseRemains, resultReads)
	}
	if otherAsset.Ref.RecoveryPointID != otherPointID || !otherLeaseRemains || otherReads != 1 {
		t.Fatalf("other-point broker resources changed point_match=%v lease=%v reads=%d", otherAsset.Ref.RecoveryPointID == otherPointID, otherLeaseRemains, otherReads)
	}
	select {
	case <-resultCanceled:
		t.Fatal("source cleanup canceled an independent RecoveryResult read")
	default:
	}

	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attemptID).Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
		t.Fatalf("advance lifecycle: %v", err)
	}
	lifecycleRequest.Stage = backupasset.SourceLifecycleCleanup
	busy, err := cache.OpenRange(memoryObject, 0, 1)
	if err != nil {
		t.Fatalf("open busy cache lease: %v", err)
	}
	if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), lifecycleRequest); !errors.Is(err, ErrCacheBusy) {
		t.Fatalf("busy cache cleanup error=%v", err)
	}
	if !cacheHasEntryForTest(cache, memoryObject) || !cacheHasEntryForTest(cache, diskObject) {
		t.Fatal("busy cache cleanup partially removed exact point")
	}
	if err := busy.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), lifecycleRequest); err != nil {
		t.Fatalf("cleanup content lifecycle: %v", err)
	}
	if err := owner.RevokeAndDrainRecoveryPoint(context.Background(), lifecycleRequest); err != nil {
		t.Fatalf("idempotent content cleanup: %v", err)
	}
	if cacheHasEntryForTest(cache, memoryObject) || cacheHasEntryForTest(cache, diskObject) || !cacheHasEntryForTest(cache, otherObject) {
		t.Fatal("cleanup did not evict only the exact point")
	}
	var historyCount int64
	if err := db.Model(&model.BackupAssetDeliveryRequest{}).Where("id = ?", requestHistory.ID).Count(&historyCount).Error; err != nil || historyCount != 1 {
		t.Fatalf("content request history count=%d err=%v, want preserved", historyCount, err)
	}
	broker.mu.Lock()
	_, resultRemains = broker.recoveryResults[resultGrantID]
	resultLeaseRemains = broker.leases[resultGrantID] == resultLease
	broker.mu.Unlock()
	if !resultRemains || !resultLeaseRemains {
		t.Fatal("cleanup removed independent RecoveryResult resources")
	}
}

func TestRecoveryPointSourceLifecycleContentWaitsOnlyExactBackupAssetIssues(t *testing.T) {
	t.Run("unrelated asset and RecoveryResult issues do not delay the target", func(t *testing.T) {
		harness, owner, request := newRealIssueContentLifecycleHarness(t)
		targetPointID := request.RecoveryPointID
		otherPointID := strings.Repeat("8", 32)
		resultPointID := harness.recoveryResult.RecoveryPointID
		if err := harness.db.Delete(&model.RecoveryPointLifecycleAttempt{}, "id = ?", request.LifecycleAttemptID).Error; err != nil {
			t.Fatalf("remove pre-Issue lifecycle attempt: %v", err)
		}
		observedAt := harness.now.Add(-time.Hour)
		if err := harness.db.Create(&model.RecoveryPoint{
			ID: otherPointID, RepositoryID: harness.asset.RepositoryID,
			Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
			ObservedAt: &observedAt, PhysicalAvailability: string(backupasset.PhysicalOnline),
		}).Error; err != nil {
			t.Fatalf("seed unrelated Issue point: %v", err)
		}
		started := make(chan string, 3)
		targetRelease := make(chan struct{})
		otherRelease := make(chan struct{})
		resultRelease := make(chan struct{})
		gates := map[string]<-chan struct{}{
			targetPointID: targetRelease,
			otherPointID:  otherRelease,
			resultPointID: resultRelease,
		}
		harness.lease.acquire = func(ctx context.Context, acquire backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
			started <- acquire.RecoveryPointID
			select {
			case <-gates[acquire.RecoveryPointID]:
				return backupasset.Lease{}, backupasset.ErrConflict
			case <-ctx.Done():
				return backupasset.Lease{}, ctx.Err()
			}
		}
		harness.authorizer.authorize = func(ref backupasset.AssetRef) (AuthorizedAsset, error) {
			asset := *harness.asset
			asset.Ref = ref
			return asset, nil
		}
		assetOrder, recoveryOrder, leaseOrder, auditOrder := []string{}, []string{}, []string{}, []string{}
		harness.authorizer.order = &assetOrder
		harness.recoveryAuthorizer.order = &recoveryOrder
		harness.lease.order = &leaseOrder
		harness.audit.order = &auditOrder

		targetIssue := harness.issueRequest()
		targetIssue.Session.Role = targetIssue.Actor.Role
		otherIssue := targetIssue
		otherIssue.Ref = backupasset.AssetRef{RecoveryPointID: otherPointID, EntryID: strings.Repeat("9", 64)}
		issueDone := make(chan error, 3)
		for _, fixture := range []struct {
			request IssueRequest
			pointID string
		}{
			{request: targetIssue, pointID: targetPointID},
			{request: otherIssue, pointID: otherPointID},
			{request: harness.recoveryIssueRequest(), pointID: resultPointID},
		} {
			issue := fixture.request
			go func() {
				_, err := harness.broker.Issue(context.Background(), issue)
				issueDone <- err
			}()
			select {
			case pointID := <-started:
				if pointID != fixture.pointID {
					close(targetRelease)
					close(otherRelease)
					close(resultRelease)
					t.Fatalf("Issue reached lease acquisition for point=%q, want %q", pointID, fixture.pointID)
				}
			case err := <-issueDone:
				close(targetRelease)
				close(otherRelease)
				close(resultRelease)
				t.Fatalf("Issue for point=%q returned before lease acquisition: %v", fixture.pointID, err)
			case <-time.After(time.Second):
				close(targetRelease)
				close(otherRelease)
				close(resultRelease)
				t.Fatalf("Issue for point=%q did not reach lease acquisition", fixture.pointID)
			}
		}
		seedContentLifecycleAttempt(t, harness.db, request.LifecycleAttemptID, targetPointID, backupasset.LifecyclePhaseRevoking)

		prepareDone := make(chan error, 1)
		go func() { prepareDone <- owner.RevokeAndDrainRecoveryPoint(context.Background(), request) }()
		select {
		case err := <-prepareDone:
			close(targetRelease)
			close(otherRelease)
			close(resultRelease)
			t.Fatalf("prepare returned before the exact-point Issue exited: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(targetRelease)
		select {
		case err := <-prepareDone:
			if err != nil {
				close(otherRelease)
				close(resultRelease)
				t.Fatalf("prepare exact point: %v", err)
			}
		case <-time.After(250 * time.Millisecond):
			close(otherRelease)
			close(resultRelease)
			for range 3 {
				<-issueDone
			}
			<-prepareDone
			t.Fatal("prepare was delayed by unrelated backup-asset or RecoveryResult Issues")
		}
		close(otherRelease)
		close(resultRelease)
		for range 3 {
			if err := <-issueDone; !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("Issue error=%v, want ErrConflict", err)
			}
		}
	})

	t.Run("canceled target issue unregisters", func(t *testing.T) {
		harness, owner, request := newRealIssueContentLifecycleHarness(t)
		if err := harness.db.Delete(&model.RecoveryPointLifecycleAttempt{}, "id = ?", request.LifecycleAttemptID).Error; err != nil {
			t.Fatalf("remove pre-Issue lifecycle attempt: %v", err)
		}
		started := make(chan struct{})
		harness.lease.acquire = func(ctx context.Context, _ backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
			close(started)
			<-ctx.Done()
			return backupasset.Lease{}, ctx.Err()
		}
		assetIssue := harness.issueRequest()
		assetIssue.Session.Role = assetIssue.Actor.Role
		issueCtx, cancelIssue := context.WithCancel(context.Background())
		issueDone := make(chan error, 1)
		go func() {
			_, err := harness.broker.Issue(issueCtx, assetIssue)
			issueDone <- err
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("target Issue did not reach lease acquisition")
		}
		seedContentLifecycleAttempt(t, harness.db, request.LifecycleAttemptID, request.RecoveryPointID, backupasset.LifecyclePhaseRevoking)
		prepareDone := make(chan error, 1)
		go func() { prepareDone <- owner.RevokeAndDrainRecoveryPoint(context.Background(), request) }()
		select {
		case err := <-prepareDone:
			cancelIssue()
			t.Fatalf("prepare returned before target cancellation: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		cancelIssue()
		if err := <-issueDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Issue error=%v", err)
		}
		select {
		case err := <-prepareDone:
			if err != nil {
				t.Fatalf("prepare after target cancellation: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("prepare did not observe canceled target Issue unregister")
		}
		harness.broker.mu.Lock()
		marker := harness.broker.assetIssues[request.RecoveryPointID]
		harness.broker.mu.Unlock()
		if marker != nil {
			t.Fatal("canceled zero-grant Issue retained its lifecycle drain marker")
		}
	})
}

func newRealIssueContentLifecycleHarness(
	t *testing.T,
) (*recoveryBrokerTestHarness, *SourceLifecycle, backupasset.SourceLifecycleRequest) {
	t.Helper()
	harness := newRecoveryBrokerTestHarness(t)
	if err := harness.db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
	); err != nil {
		t.Fatalf("migrate lifecycle issue tables: %v", err)
	}
	pointID := harness.asset.Ref.RecoveryPointID
	attemptID := strings.Repeat("7", 32)
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking),
	}).Error; err != nil {
		t.Fatalf("seed lifecycle Issue attempt: %v", err)
	}
	config := testCacheConfig("")
	config.DiskEnabled = false
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: config, Now: func() time.Time { return harness.now }, Random: rand.Reader,
	})
	if err != nil {
		t.Fatalf("create lifecycle Issue cache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	if err := harness.broker.SetCache(cache); err != nil {
		t.Fatalf("set lifecycle Issue cache: %v", err)
	}
	owner, err := NewSourceLifecycle(harness.db, harness.broker, cache, func() time.Time { return harness.now }, 1)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	return harness, owner, backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
	}
}

func assertContentSourceSettled(t *testing.T, db *gorm.DB, grantID, leaseID string) {
	t.Helper()
	var grant model.BackupAssetDeliveryGrant
	if err := db.First(&grant, "id = ?", grantID).Error; err != nil || grant.State != string(DeliveryRevoked) || grant.RevocationReason != "point_unavailable" {
		t.Fatalf("content grant load_err=%v state=%q reason_match=%v, want lifecycle revoked", err, grant.State, grant.RevocationReason == "point_unavailable")
	}
	var lease model.RecoveryPointLease
	if err := db.First(&lease, "id = ?", leaseID).Error; err != nil || lease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("content lease load_err=%v status=%q, want released", err, lease.Status)
	}
}

func newContentSourceLifecycleOwnerForTest(t *testing.T, db *gorm.DB, now time.Time, batchSize int) (*SourceLifecycle, *Broker, *AuthenticatedCache) {
	t.Helper()
	config := testCacheConfig("")
	config.DiskEnabled = false
	cache, err := NewAuthenticatedCache(context.Background(), CacheDependencies{
		Config: config, Now: func() time.Time { return now }, Random: rand.Reader,
	})
	if err != nil {
		t.Fatalf("create source lifecycle cache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Shutdown(context.Background()) })
	leaseController, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	broker := &Broker{
		db: db, now: func() time.Time { return now }, accepting: true, metrics: NoopMetrics{}, cache: cache,
		lease:  leaseController,
		leases: make(map[string]*ContentLeaseSession), assets: make(map[string]AuthorizedAsset),
		recoveryResults: make(map[string]AuthorizedRecoveryResult), derivedBindings: make(map[string]DerivedRepresentation),
		reads: make(map[string]map[string]activeContentRead), revokedGrants: make(map[string]struct{}), inFlight: make(map[backupasset.ProviderKind]int),
	}
	owner, err := NewSourceLifecycle(db, broker, cache, func() time.Time { return now }, batchSize)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	return owner, broker, cache
}

type sourceLifecycleLeaseControllerRecorder struct {
	inner            ContentLeaseController
	mu               sync.Mutex
	takeovers        []backupasset.TakeoverLeaseRequest
	releases         []backupasset.LeaseFence
	takeoverOverride func(context.Context, backupasset.TakeoverLeaseRequest) (backupasset.Lease, error)
	releaseOverride  func(context.Context, backupasset.LeaseFence) error
}

func newSourceLifecycleLeaseControllerRecorder(
	t *testing.T,
	db *gorm.DB,
	now func() time.Time,
) *sourceLifecycleLeaseControllerRecorder {
	t.Helper()
	service, err := backupasset.NewLeaseService(db, now, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	return &sourceLifecycleLeaseControllerRecorder{inner: service}
}

func (controller *sourceLifecycleLeaseControllerRecorder) Acquire(
	ctx context.Context,
	request backupasset.AcquireLeaseRequest,
) (backupasset.Lease, error) {
	return controller.inner.Acquire(ctx, request)
}

func (controller *sourceLifecycleLeaseControllerRecorder) Renew(
	ctx context.Context,
	fence backupasset.LeaseFence,
) (backupasset.Lease, error) {
	return controller.inner.Renew(ctx, fence)
}

func (controller *sourceLifecycleLeaseControllerRecorder) ValidateFence(
	ctx context.Context,
	fence backupasset.LeaseFence,
) error {
	return controller.inner.ValidateFence(ctx, fence)
}

func (controller *sourceLifecycleLeaseControllerRecorder) Release(
	ctx context.Context,
	fence backupasset.LeaseFence,
) error {
	controller.mu.Lock()
	controller.releases = append(controller.releases, fence)
	override := controller.releaseOverride
	controller.mu.Unlock()
	if override != nil {
		return override(ctx, fence)
	}
	return controller.inner.Release(ctx, fence)
}

func (controller *sourceLifecycleLeaseControllerRecorder) Takeover(
	ctx context.Context,
	request backupasset.TakeoverLeaseRequest,
) (backupasset.Lease, error) {
	controller.mu.Lock()
	controller.takeovers = append(controller.takeovers, request)
	override := controller.takeoverOverride
	controller.mu.Unlock()
	if override != nil {
		return override(ctx, request)
	}
	return controller.inner.Takeover(ctx, request)
}

func (controller *sourceLifecycleLeaseControllerRecorder) takeoverSnapshot() []backupasset.TakeoverLeaseRequest {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return append([]backupasset.TakeoverLeaseRequest(nil), controller.takeovers...)
}

func (controller *sourceLifecycleLeaseControllerRecorder) releaseSnapshot() []backupasset.LeaseFence {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return append([]backupasset.LeaseFence(nil), controller.releases...)
}

func newContentLifecycleLeaseTestDB(
	t *testing.T,
	seed string,
) (*gorm.DB, time.Time, string, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "content-lifecycle.db")+"?_busy_timeout=5000&_loc=UTC"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open Content lifecycle database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
		&model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{},
		&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{}, &model.BackupAssetRecoveryResult{},
	); err != nil {
		t.Fatalf("migrate Content lifecycle database: %v", err)
	}
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	pointID := strings.Repeat(seed, 32)
	attemptID := seed + strings.Repeat("0", 31)
	observedAt := now.Add(-24 * time.Hour)
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: strings.Repeat("a", 32),
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
		ObservedAt: &observedAt, PhysicalAvailability: string(backupasset.PhysicalOnline),
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed Content lifecycle point: %v", err)
	}
	return db, now, pointID, attemptID
}

func seedRecoveryResultContentAuthority(
	t *testing.T,
	db *gorm.DB,
	pointID string,
	jobID string,
	resultID string,
) {
	t.Helper()
	withoutHooks := db.Session(&gorm.Session{SkipHooks: true})
	if err := withoutHooks.Create(&model.BackupAssetRecoveryPlan{
		ID: jobID, RecoveryPointID: pointID,
	}).Error; err != nil {
		t.Fatalf("seed RecoveryResult source plan: %v", err)
	}
	if err := withoutHooks.Create(&model.BackupAssetRecoveryJob{
		ID: jobID, PlanID: jobID,
	}).Error; err != nil {
		t.Fatalf("seed RecoveryResult source job: %v", err)
	}
	if err := withoutHooks.Create(&model.BackupAssetRecoveryResult{
		ID: resultID, JobID: jobID,
	}).Error; err != nil {
		t.Fatalf("seed RecoveryResult source row: %v", err)
	}
}

func seedContentLifecycleAttempt(
	t *testing.T,
	db *gorm.DB,
	attemptID string,
	pointID string,
	phase backupasset.LifecyclePhase,
) {
	t.Helper()
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(phase),
	}).Error; err != nil {
		t.Fatalf("seed Content lifecycle attempt: %v", err)
	}
}

func assertRecoveryResultContentRowsUnchanged(
	t *testing.T,
	db *gorm.DB,
	wantGrant model.BackupAssetDeliveryGrant,
	wantRequest model.BackupAssetDeliveryRequest,
	wantLease model.RecoveryPointLease,
) {
	t.Helper()
	var grant model.BackupAssetDeliveryGrant
	if err := db.First(&grant, "id = ?", wantGrant.ID).Error; err != nil {
		t.Fatalf("load RecoveryResult grant: %v", err)
	}
	if grant.ResourceKind != wantGrant.ResourceKind || grant.State != wantGrant.State || grant.InFlight != wantGrant.InFlight ||
		grant.LeaseID != wantGrant.LeaseID || grant.RecoveryPointID != nil || grant.RecoveryResultID == nil ||
		*grant.RecoveryResultID != *wantGrant.RecoveryResultID {
		t.Fatalf("RecoveryResult grant changed: kind_match=%v state_match=%v in_flight_match=%v lease_match=%v result_binding_match=%v",
			grant.ResourceKind == wantGrant.ResourceKind, grant.State == wantGrant.State, grant.InFlight == wantGrant.InFlight,
			grant.LeaseID == wantGrant.LeaseID, grant.RecoveryPointID == nil && grant.RecoveryResultID != nil && *grant.RecoveryResultID == *wantGrant.RecoveryResultID)
	}
	var request model.BackupAssetDeliveryRequest
	if err := db.First(&request, "id = ?", wantRequest.ID).Error; err != nil {
		t.Fatalf("load RecoveryResult read: %v", err)
	}
	if request.GrantID != wantRequest.GrantID || request.State != wantRequest.State {
		t.Fatalf("RecoveryResult read changed: grant_match=%v state_match=%v", request.GrantID == wantRequest.GrantID, request.State == wantRequest.State)
	}
	assertContentLeaseStillActive(t, db, wantLease)
}

func assertContentLeaseStillActive(t *testing.T, db *gorm.DB, want model.RecoveryPointLease) {
	t.Helper()
	var lease model.RecoveryPointLease
	if err := db.First(&lease, "id = ?", want.ID).Error; err != nil {
		t.Fatalf("load exact Content lease: %v", err)
	}
	if lease.Status != string(backupasset.LeaseActive) || lease.RecoveryPointID != want.RecoveryPointID ||
		lease.OwnerID != want.OwnerID || lease.AttemptID != want.AttemptID || lease.FenceToken != want.FenceToken {
		t.Fatalf("Content lease was force changed: status=%q point_match=%v owner_match=%v attempt_match=%v token_match=%v",
			lease.Status, lease.RecoveryPointID == want.RecoveryPointID, lease.OwnerID == want.OwnerID,
			lease.AttemptID == want.AttemptID, lease.FenceToken == want.FenceToken)
	}
}

func sourceLifecycleLeaseFromRow(row model.RecoveryPointLease, now time.Time) backupasset.Lease {
	attemptID := strings.Repeat("e", 32)
	fenceToken := strings.Repeat("f", 64)
	fence := backupasset.LeaseFence{
		LeaseID: row.ID, RecoveryPointID: row.RecoveryPointID,
		HolderType: backupasset.LeaseHolderType(row.HolderType), OwnerID: row.OwnerID,
		AttemptID: attemptID, FenceToken: fenceToken,
	}
	return backupasset.Lease{
		ID: row.ID, RecoveryPointID: row.RecoveryPointID,
		HolderType: backupasset.LeaseHolderType(row.HolderType), OwnerID: row.OwnerID,
		Status: backupasset.LeaseActive, LeaseExpiresAt: now.Add(5 * time.Minute),
		AbsoluteDeadline: row.AbsoluteDeadline.UTC(), LastHeartbeatAt: now, Fence: fence,
	}
}

func nowForSourceLifecycleLease(row model.RecoveryPointLease) time.Time {
	now := row.LeaseExpiresAt.UTC().Add(time.Minute)
	if !now.Add(5 * time.Minute).Before(row.AbsoluteDeadline.UTC()) {
		now = row.LastHeartbeatAt.UTC()
	}
	return now
}

func newSourceLifecycleBrokerForTest(
	db *gorm.DB,
	cache *AuthenticatedCache,
	now time.Time,
	lease ContentLeaseController,
) *Broker {
	return &Broker{
		db: db, now: func() time.Time { return now }, accepting: true, metrics: NoopMetrics{}, cache: cache, lease: lease,
		assetIssues: make(map[string]*recoveryPointIssueState), leases: make(map[string]*ContentLeaseSession),
		assets: make(map[string]AuthorizedAsset), recoveryResults: make(map[string]AuthorizedRecoveryResult),
		derivedBindings: make(map[string]DerivedRepresentation), reads: make(map[string]map[string]activeContentRead),
		revokedGrants: make(map[string]struct{}), inFlight: make(map[backupasset.ProviderKind]int),
	}
}
