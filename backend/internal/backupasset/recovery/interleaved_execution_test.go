package recovery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestRecoveryExactMirrorInterleavedExecutionSQLite(t *testing.T) {
	testRecoveryExactMirrorInterleavedExecution(t, false)
}

func TestRecoveryExactMirrorInterleavedExecutionPostgres(t *testing.T) {
	testRecoveryExactMirrorInterleavedExecution(t, true)
}

func newInterleavedExecutionFixture(t *testing.T, postgres bool) exactMirrorOrdinaryExecutionFixture {
	t.Helper()
	options := authorizationReceiptMigrationOptions{interleaved: true, rsync: true}
	kinds := []RecoveryOperationKind{RecoveryOperationOverwrite, RecoveryOperationCreate}
	var fixture *authorizationReceiptServiceFixture
	if postgres {
		fixture = newAuthorizationReceiptPostgresMigrationFixtureWithOptions(t, AuthorizationReceiptWriteAuthorize, true, false, 2, kinds, options)
	} else {
		fixture = newAuthorizationReceiptSQLiteMigrationFixtureWithOptions(t, AuthorizationReceiptWriteAuthorize, true, false, 2, kinds, options)
	}
	return newExactMirrorOrdinaryExecutionFixtureFromService(t, fixture)
}

func interleavedRows(t *testing.T, fixture exactMirrorOrdinaryExecutionFixture) (model.BackupAssetRecoveryPlan, model.BackupAssetRecoveryPreflight, model.BackupAssetRecoveryJob, []model.BackupAssetRecoveryJobItem) {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	var plan model.BackupAssetRecoveryPlan
	var preflight model.BackupAssetRecoveryPreflight
	var items []model.BackupAssetRecoveryJobItem
	db := fixture.serviceFixture.db
	if err := db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", job.PreflightID).Take(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", fixture.jobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{string(RecoveryOperationDelete), string(RecoveryOperationOverwrite), string(RecoveryOperationDelete), string(RecoveryOperationCreate)}
	if len(items) != len(wantKinds) {
		t.Fatalf("canonical operation count=%d", len(items))
	}
	for index, item := range items {
		if item.Ordinal != index || item.OperationKind != wantKinds[index] {
			t.Fatalf("canonical operation %d = %+v, want %s", index, item, wantKinds[index])
		}
	}
	return plan, preflight, job, items
}

func testRecoveryExactMirrorInterleavedExecution(t *testing.T, postgres bool) {
	t.Run("execution_and_builder", func(t *testing.T) {
		fixture := newInterleavedExecutionFixture(t, postgres)
		planBefore, preflightBefore, jobBefore, itemsBefore := interleavedRows(t, fixture)
		ctx := context.Background()
		request, err := fixture.coordinator.BuildRsyncRestoreRequest(ctx, fixture.claim)
		if err != nil {
			t.Fatalf("build pristine request: %v", err)
		}
		if len(request.Entries) != 2 || request.Entries[0].ExpectedSize != 1 || request.Entries[1].ExpectedSize != 2 ||
			request.Target.TargetRevision != jobBefore.TargetChainRevision ||
			!request.MutationPermit.Session.ExpiresAt.Equal(fixture.claim.AbsoluteDeadline) {
			t.Fatalf("pristine request=%+v", request)
		}
		fixture.serviceFixture.advanceTo(fixture.serviceFixture.now.Add(time.Minute))
		renewed, err := fixture.coordinator.Heartbeat(ctx, fixture.claim)
		if err != nil || !renewed.LeaseExpiresAt.After(fixture.claim.LeaseExpiresAt) || !renewed.AbsoluteDeadline.Equal(fixture.claim.AbsoluteDeadline) {
			t.Fatalf("renew claim: old=%+v new=%+v err=%v", fixture.claim, renewed, err)
		}
		fixture.claim = renewed
		request, err = fixture.coordinator.BuildRsyncRestoreRequest(ctx, renewed)
		if err != nil || !request.MutationPermit.Session.ExpiresAt.Equal(renewed.AbsoluteDeadline) {
			t.Fatalf("renewed request did not preserve absolute deadline: %+v error=%v", request.MutationPermit.Session, err)
		}
		fresh := captureRecoveryAuthorityTargetForTest(t, fixture, recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetFreshFirstWrite})
		if fresh.anchorItemID != itemsBefore[3].ID || fresh.anchorCheckpointID != "" {
			t.Fatalf("fresh nonzero anchor=%+v", fresh)
		}
		spy := &recoveryWorkerAuthorityEffectSpy{observation: RecoveryAuthorityObservation{
			observedAt: fixture.serviceFixture.now, expiresAt: fixture.serviceFixture.now.Add(time.Minute),
		}}
		fixture.coordinator.liveRevalidator = spy
		paused := newPausedAuthorizedExactMirrorDeleteFromExecution(t, "interleaved", fixture)
		var post recoveryAuthorityTargetExpectation
		for _, binding := range spy.observeBindings {
			if binding.targetExpectation.phase == recoveryAuthorityTargetCurrentPost &&
				binding.targetExpectation.currentItemID == itemsBefore[3].ID {
				post = binding.targetExpectation
			}
		}
		if post.anchorItemID != itemsBefore[3].ID || post.currentItemID != itemsBefore[3].ID ||
			post.expectedDigest != itemsBefore[3].ExpectedPostIdentityDigest {
			t.Fatalf("pending nonzero anchor post-state=%+v", post)
		}
		if len(paused.checkpoints) != 3 || paused.checkpoints[0].JobItemID != itemsBefore[1].ID ||
			paused.checkpoints[1].JobItemID != itemsBefore[3].ID ||
			paused.checkpoints[2].Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
			t.Fatalf("write-first paused history=%+v", paused.checkpoints)
		}
		checkpointed := captureRecoveryAuthorityTargetForTest(t, fixture, recoveryAuthorityTargetIntent{
			phase: recoveryAuthorityTargetCheckpointed, itemID: itemsBefore[0].ID,
		})
		if checkpointed.anchorItemID != itemsBefore[3].ID || checkpointed.anchorCheckpointID != paused.checkpoints[1].ID ||
			checkpointed.currentItemID != itemsBefore[0].ID {
			t.Fatalf("checkpointed nonzero anchor=%+v", checkpointed)
		}
		if _, err := captureRecoveryAuthorityTarget(ctx, fixture.serviceFixture.db, fixture.claim,
			recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetCheckpointed, itemID: itemsBefore[2].ID}, fixture.serviceFixture.now); err == nil {
			t.Fatal("second delete was accepted before the first pending delete")
		}
		request, err = fixture.coordinator.BuildRsyncRestoreRequest(ctx, fixture.claim)
		if err != nil || request.Target.TargetRevision != paused.job.TargetChainRevision ||
			request.Target.TargetRevision == preflightBefore.TargetRevision || request.Fence.ExpectedTargetRevision != paused.job.TargetChainRevision ||
			len(request.Entries) != 2 {
			t.Fatalf("builder rejected authenticated advanced chain: request=%+v err=%v", request, err)
		}
		source := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
		if err := fixture.coordinator.ExecuteClaim(ctx, fixture.claim, source, paused.request.GrantSecret); err != nil {
			t.Fatalf("resume interleaved exact mirror: %v", err)
		}
		planAfter, preflightAfter, completedJob, itemsAfter := interleavedRows(t, fixture)
		if !reflect.DeepEqual(planBefore, planAfter) || !reflect.DeepEqual(preflightBefore, preflightAfter) {
			t.Fatal("execution changed immutable plan or preflight")
		}
		var checkpoints []model.BackupAssetRecoveryCheckpoint
		if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
			t.Fatal(err)
		}
		if len(checkpoints) != 6 || completedJob.State != string(JobStateSucceeded) ||
			checkpoints[2].Phase != string(CheckpointPhaseDeleteAuthorityRequired) ||
			checkpoints[3].Phase != string(CheckpointPhaseDeleteAuthorityConsumed) {
			t.Fatalf("interleaved completion job=%+v checkpoints=%+v", completedJob, checkpoints)
		}
		for position, ordinal := range []int{1, 3, 0, 2} {
			checkpointIndex := position
			if position >= 2 {
				checkpointIndex += 2
			}
			checkpoint, before, after := checkpoints[checkpointIndex], itemsBefore[ordinal], itemsAfter[ordinal]
			if checkpoint.Sequence != checkpointIndex || checkpoint.Phase != string(CheckpointPhaseOperation) ||
				checkpoint.JobItemID != before.ID || checkpoint.OperationDigest != recoveryJobItemOperationDigest(before) ||
				after.ID != before.ID || after.Ordinal != before.Ordinal || after.OperationKind != before.OperationKind ||
				recoveryJobItemOperationDigest(after) != recoveryJobItemOperationDigest(before) || after.Outcome != "succeeded" {
				t.Fatalf("execution position=%d canonical ordinal=%d before=%+v after=%+v checkpoint=%+v", position, ordinal, before, after, checkpoint)
			}
		}
		rows, err := rebuildExecuteOperationRows(planAfter, preflightAfter, fixture.serviceFixture.db)
		if err != nil {
			t.Fatal(err)
		}
		operations, err := newInPlaceOrdinaryCheckpointOperations(planAfter, completedJob, itemsAfter, rows)
		if err != nil {
			t.Fatalf("project fully completed schedule: %v", err)
		}
		for _, operation := range operations {
			if !operation.completed {
				t.Fatalf("completed history retained a pending operation: %+v", operation)
			}
		}
		if _, _, _, err := validateInPlaceOrdinaryCheckpointHistory(planAfter, completedJob, fixture.claim,
			checkpoints, operations, fixture.serviceFixture.now); err != nil {
			t.Fatalf("fully completed history did not close to the final chain: %v", err)
		}
		if len(fixture.target.writes) != 2 || len(fixture.target.deletes) != 2 ||
			completedJob.TargetChainRevision != checkpoints[5].NextTargetRevision {
			t.Fatalf("interleaved target calls writes=%d deletes=%d chain=%s", len(fixture.target.writes), len(fixture.target.deletes), completedJob.TargetChainRevision)
		}
		assertPristineLeaseStates(t, fixture.serviceFixture.db, fixture.claim, "released", "released")
	})

	testGrantBindingUTC := func(t *testing.T, shortTTL bool) {
		fixture := newInterleavedExecutionFixture(t, postgres)
		fixture.serviceFixture.advanceTo(fixture.serviceFixture.now.Add(123456 * time.Microsecond))
		if shortTTL {
			fixture.serviceFixture.service.deleteGrantTTL = time.Minute
			fixture.serviceFixture.advanceTo(fixture.serviceFixture.now.Add(789 * time.Nanosecond))
		}
		db := fixture.serviceFixture.db
		zone := time.FixedZone("test-offset", 5*60*60+30*60)
		callback := "test:exact-mirror-grant-offset:" + t.Name()
		var mutate func(*model.BackupAssetRecoveryGrant)
		var storedExpiry time.Time
		var pendingReads, consumedReads int
		if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Error != nil || tx.RowsAffected != 1 || tx.Statement == nil {
				return
			}
			grant, ok := tx.Statement.Dest.(*model.BackupAssetRecoveryGrant)
			if !ok || grant.AuthorityCategory != string(AuthorityExactMirrorDelete) {
				return
			}
			// Reproduce a driver returning the same stored instant in another location.
			storedExpiry = grant.ExpiresAt
			grant.ExpiresAt = grant.ExpiresAt.In(zone)
			if grant.ConsumedAt == nil {
				pendingReads++
			} else {
				consumedReads++
			}
			if mutate != nil {
				mutate(grant)
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Callback().Query().Remove(callback); err != nil {
				t.Errorf("remove grant offset callback: %v", err)
			}
		})

		paused := newPausedAuthorizedExactMirrorDeleteFromExecution(t, "grant-offset", fixture)
		if pendingReads == 0 {
			t.Fatal("authorization did not validate a non-UTC grant reread")
		}
		required := paused.checkpoints[len(paused.checkpoints)-1]
		var grant model.BackupAssetRecoveryGrant
		if err := db.Where("id = ?", paused.result.GrantID).Take(&grant).Error; err != nil {
			t.Fatal(err)
		}
		if grant.ExpiresAt.Location() != zone || !grant.ExpiresAt.Equal(storedExpiry) ||
			required.DeleteAuthorityExpiresAt == nil || grant.ExpiresAt.After(*required.DeleteAuthorityExpiresAt) {
			t.Fatalf("grant reread changed its instant or exceeded the checkpoint: expiry=%v stored=%v checkpoint=%v",
				grant.ExpiresAt, storedExpiry, required.DeleteAuthorityExpiresAt)
		}
		if shortTTL {
			wantExpiry := fixture.serviceFixture.now.Add(time.Minute)
			if postgres {
				wantExpiry = wantExpiry.Truncate(time.Microsecond)
			}
			if !grant.ExpiresAt.Equal(wantExpiry) || !grant.ExpiresAt.After(fixture.serviceFixture.now) ||
				!grant.ExpiresAt.Before(*required.DeleteAuthorityExpiresAt) {
				t.Fatalf("short grant expiry=%v want=%v now=%v checkpoint=%v",
					grant.ExpiresAt, wantExpiry, fixture.serviceFixture.now, *required.DeleteAuthorityExpiresAt)
			}
		}
		secretHash := authorizationGrantSecretHash(
			AuthorizationReceiptCategoryExactMirrorDelete, paused.request.PlanID, fixture.jobID,
			required.ID, paused.request.GrantSecret,
		)
		wantDigest := framedDigest(
			"xirang/recovery/authorization/grant-binding/v1", "exact_mirror_delete",
			paused.request.PlanID, fixture.jobID, required.ID, secretHash,
			grant.ExpiresAt.UTC().Format(time.RFC3339Nano),
		)
		if grant.GrantHash != secretHash || grant.BindingDigest != wantDigest {
			t.Fatal("issued grant did not bind the original tuple and UTC expiry")
		}

		before := pendingReads
		consumed, err := fixture.coordinator.ValidateDeleteAuthorizationHandoff(context.Background(), paused.request, paused.result)
		if err != nil || consumed || pendingReads == before {
			t.Fatalf("pending non-UTC handoff consumed=%t reads=%d/%d error=%v", consumed, before, pendingReads, err)
		}
		rebindExpiry := func(grant *model.BackupAssetRecoveryGrant, expiry time.Time) {
			grant.ExpiresAt = expiry.In(zone)
			grant.BindingDigest = framedDigest(
				"xirang/recovery/authorization/grant-binding/v1", "exact_mirror_delete",
				paused.request.PlanID, fixture.jobID, required.ID, grant.GrantHash,
				grant.ExpiresAt.UTC().Format(time.RFC3339Nano),
			)
		}
		for _, tc := range []struct {
			name   string
			mutate func(*model.BackupAssetRecoveryGrant)
			secret bool
		}{
			{name: "changed_expiry_instant", mutate: func(grant *model.BackupAssetRecoveryGrant) {
				grant.ExpiresAt = grant.ExpiresAt.Add(-time.Nanosecond)
			}},
			{name: "expiry_equal_now", mutate: func(grant *model.BackupAssetRecoveryGrant) {
				rebindExpiry(grant, fixture.serviceFixture.now)
			}},
			{name: "expiry_after_checkpoint", mutate: func(grant *model.BackupAssetRecoveryGrant) {
				rebindExpiry(grant, required.DeleteAuthorityExpiresAt.Add(time.Microsecond))
			}},
			{name: "changed_attempt_fence", mutate: func(grant *model.BackupAssetRecoveryGrant) {
				grant.DeleteAttemptFence++
			}},
			{name: "changed_secret", secret: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				mutate = tc.mutate
				request := paused.request
				if tc.secret {
					request.GrantSecret = mustAuthorizationReceiptSecret(t)
				}
				before := pendingReads
				consumed, err := fixture.coordinator.ValidateDeleteAuthorizationHandoff(context.Background(), request, paused.result)
				if consumed || !errors.Is(err, ErrRecoveryWorkerFenceLost) || pendingReads == before {
					mutate = nil
					t.Fatalf("changed grant handoff accepted: consumed=%t reads=%d/%d error=%v", consumed, before, pendingReads, err)
				}
				before = pendingReads
				source := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
				err = fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, source, request.GrantSecret)
				mutate = nil
				if !errors.Is(err, ErrRecoveryWorkerFenceLost) || pendingReads == before || len(fixture.target.deletes) != 0 {
					t.Fatalf("changed grant consumption error=%v reads=%d/%d deletes=%d", err, before, pendingReads, len(fixture.target.deletes))
				}
			})
		}
		if err := db.Where("id = ?", paused.result.GrantID).Take(&grant).Error; err != nil {
			t.Fatal(err)
		}
		var rejectedJob model.BackupAssetRecoveryJob
		if err := db.Where("id = ?", fixture.jobID).Take(&rejectedJob).Error; err != nil {
			t.Fatal(err)
		}
		var consumedCount int64
		if err := db.Model(&model.BackupAssetRecoveryCheckpoint{}).
			Where("job_id = ? AND phase = ?", fixture.jobID, CheckpointPhaseDeleteAuthorityConsumed).
			Count(&consumedCount).Error; err != nil {
			t.Fatal(err)
		}
		if grant.ConsumedAt != nil || grant.RevokedAt != nil || consumedCount != 0 ||
			rejectedJob.State != string(JobStateRunning) || rejectedJob.TargetChainRevision != paused.job.TargetChainRevision ||
			len(fixture.target.deletes) != 0 {
			t.Fatalf("rejected grant changed durable or target state: consumed=%t revoked=%t checkpoints=%d job=%s chain_changed=%t deletes=%d",
				grant.ConsumedAt != nil, grant.RevokedAt != nil, consumedCount, rejectedJob.State,
				rejectedJob.TargetChainRevision != paused.job.TargetChainRevision, len(fixture.target.deletes))
		}

		before = pendingReads
		source := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
		if err := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, source, paused.request.GrantSecret); err != nil {
			t.Fatalf("consume and use non-UTC grant: %v", err)
		}
		if pendingReads == before || consumedReads == 0 || len(fixture.target.deletes) != 2 {
			t.Fatalf("grant was not consumed and revalidated: pending reads=%d/%d consumed reads=%d deletes=%d",
				before, pendingReads, consumedReads, len(fixture.target.deletes))
		}
		var consumedCheckpoint model.BackupAssetRecoveryCheckpoint
		if err := db.Where("job_id = ? AND phase = ?", fixture.jobID, CheckpointPhaseDeleteAuthorityConsumed).
			Take(&consumedCheckpoint).Error; err != nil {
			t.Fatal(err)
		}
		if consumedCheckpoint.DeleteGrantID != grant.ID || consumedCheckpoint.DeleteGrantBindingDigest != wantDigest ||
			consumedCheckpoint.DeleteGrantExpiresAt == nil || !consumedCheckpoint.DeleteGrantExpiresAt.Equal(grant.ExpiresAt) {
			t.Fatal("consumed checkpoint did not preserve the issued grant binding and deadline")
		}
		before = consumedReads
		consumed, err = fixture.coordinator.ValidateDeleteAuthorizationHandoff(context.Background(), paused.request, paused.result)
		if err != nil || !consumed || consumedReads == before {
			t.Fatalf("consumed non-UTC handoff consumed=%t reads=%d/%d error=%v", consumed, before, consumedReads, err)
		}
		mutate = func(grant *model.BackupAssetRecoveryGrant) { grant.DeleteNodeFence++ }
		before = consumedReads
		consumed, err = fixture.coordinator.ValidateDeleteAuthorizationHandoff(context.Background(), paused.request, paused.result)
		mutate = nil
		if consumed || !errors.Is(err, ErrRecoveryWorkerFenceLost) || consumedReads == before {
			t.Fatalf("changed consumed grant tuple accepted: consumed=%t reads=%d/%d error=%v", consumed, before, consumedReads, err)
		}
	}
	t.Run("grant_binding_utc", func(t *testing.T) { testGrantBindingUTC(t, false) })
	t.Run("grant_binding_utc_short_ttl", func(t *testing.T) { testGrantBindingUTC(t, true) })

	for _, tc := range []struct {
		name  string
		clock func(*authorizationReceiptServiceFixture, RecoveryWorkerClaim) time.Time
	}{
		{name: "builder_lock_expiry", clock: func(_ *authorizationReceiptServiceFixture, claim RecoveryWorkerClaim) time.Time {
			return claim.AbsoluteDeadline
		}},
		{name: "builder_lock_clock_rollback", clock: func(fixture *authorizationReceiptServiceFixture, _ RecoveryWorkerClaim) time.Time {
			return fixture.now.Add(-time.Second)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newInterleavedExecutionFixture(t, postgres)
			before := captureRecoveryBeforeFirstWriteProjection(t, fixture.serviceFixture.db, fixture.claim)
			callback := "test:interleaved-builder-clock:" + t.Name()
			fired := false
			if err := fixture.serviceFixture.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if !fired && tx.Statement != nil && tx.Statement.Table == (model.CatalogEntry{}).TableName() {
					fired = true
					fixture.serviceFixture.advanceTo(tc.clock(fixture.serviceFixture, fixture.claim))
				}
			}); err != nil {
				t.Fatal(err)
			}
			_, err := fixture.coordinator.BuildRsyncRestoreRequest(context.Background(), fixture.claim)
			if removeErr := fixture.serviceFixture.db.Callback().Query().Remove(callback); removeErr != nil {
				t.Fatal(removeErr)
			}
			if !fired || !errors.Is(err, ErrRecoveryWorkerFenceLost) {
				t.Fatalf("builder clock boundary fired=%t error=%v", fired, err)
			}
			if after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.serviceFixture.db, fixture.claim.JobID, fixture.claim.AttemptID); !reflect.DeepEqual(before, after) {
				t.Fatalf("builder clock failure changed job or attempt: before=%+v after=%+v", before, after)
			}
		})
	}

	t.Run("builder_then_source_drift", func(t *testing.T) {
		fixture := newInterleavedExecutionFixture(t, postgres)
		if _, err := fixture.coordinator.BuildRsyncRestoreRequest(context.Background(), fixture.claim); err != nil {
			t.Fatalf("build before source drift: %v", err)
		}
		_, _, job, _ := interleavedRows(t, fixture)
		var planItem model.BackupAssetRecoveryPlanItem
		if err := fixture.serviceFixture.db.Where("plan_id = ? AND ordinal = ?", job.PlanID, 0).Take(&planItem).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.serviceFixture.db.Model(&model.CatalogEntry{}).
			Where("generation_id = ? AND entry_id = ?", planItem.CatalogGenerationID, planItem.EntryID).
			Update("normalized_path", "/changed-after-builder").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.coordinator.PrepareFirstWrite(context.Background(), fixture.claim); !errors.Is(err, ErrRecoverySourceChanged) {
			t.Fatalf("post-builder source drift reached permit: %v", err)
		}
		if len(fixture.target.writes)+len(fixture.target.deletes)+len(fixture.target.workspaceCalls) != 0 {
			t.Fatal("source drift after builder reached target mutation")
		}
	})

	t.Run("wrong_source_declaration", func(t *testing.T) {
		fixture := newInterleavedExecutionFixture(t, postgres)
		before := captureRecoveryBeforeFirstWriteProjection(t, fixture.serviceFixture.db, fixture.claim)
		source := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
		source.materialize = func(entries []provider.RestoreEntry) []provider.RestoreEntry {
			entries[1].ExpectedDigest = strings.Repeat("f", 64)
			return entries
		}
		if err := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, source, ""); !errors.Is(err, ErrRecoverySourceChanged) {
			t.Fatalf("wrong interleaved source declaration error=%v", err)
		}
		assertRecoveryFailedBeforeFirstWrite(t, fixture.serviceFixture.db, fixture.claim, before, fixture.target, source)
	})

	t.Run("history_and_migration_guard", func(t *testing.T) {
		fixture := newInterleavedExecutionFixture(t, postgres)
		plan, preflight, _, items := interleavedRows(t, fixture)
		primeRecoveryOrdinaryCompletedItem(t, fixture.coordinator, fixture.claim, items[1].ID)
		_, _, job, items := interleavedRows(t, fixture)
		var checkpoints []model.BackupAssetRecoveryCheckpoint
		db := fixture.serviceFixture.db
		if err := db.Where("job_id = ?", fixture.jobID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
			t.Fatal(err)
		}
		rows, err := rebuildExecuteOperationRows(plan, preflight, db)
		if err != nil {
			t.Fatal(err)
		}
		operations, err := newInPlaceOrdinaryCheckpointOperations(plan, job, items, rows)
		if err != nil || len(checkpoints) != 1 {
			t.Fatalf("history fixture checkpoints=%+v error=%v", checkpoints, err)
		}
		for _, mutate := range []func([]model.BackupAssetRecoveryCheckpoint) []model.BackupAssetRecoveryCheckpoint{
			func(rows []model.BackupAssetRecoveryCheckpoint) []model.BackupAssetRecoveryCheckpoint { return nil },
			func(rows []model.BackupAssetRecoveryCheckpoint) []model.BackupAssetRecoveryCheckpoint {
				return append(rows, rows[0])
			},
			func(rows []model.BackupAssetRecoveryCheckpoint) []model.BackupAssetRecoveryCheckpoint {
				rows[0].JobItemID = items[3].ID
				return rows
			},
			func(rows []model.BackupAssetRecoveryCheckpoint) []model.BackupAssetRecoveryCheckpoint {
				rows[0].Sequence++
				return rows
			},
			func(rows []model.BackupAssetRecoveryCheckpoint) []model.BackupAssetRecoveryCheckpoint {
				rows[0].PriorTargetRevision = "wrong-chain"
				return rows
			},
		} {
			bad := mutate(append([]model.BackupAssetRecoveryCheckpoint(nil), checkpoints...))
			if _, _, _, err := validateInPlaceOrdinaryCheckpointHistory(plan, job, fixture.claim, bad, operations, fixture.serviceFixture.now); err == nil {
				t.Fatalf("invalid history accepted: %+v", bad)
			}
		}
		badItems := append([]model.BackupAssetRecoveryJobItem(nil), items...)
		badItems[1].Outcome = ""
		badItems[3].Outcome = "succeeded"
		if _, err := newInPlaceOrdinaryCheckpointOperations(plan, job, badItems, rows); err == nil {
			t.Fatal("completed operation after a pending execution item was accepted")
		}
		invalidFence := checkpoints[0]
		invalidFence.ID = strings.Repeat("f", 32)
		invalidFence.Sequence = 1
		invalidFence.JobItemID = items[3].ID
		invalidFence.OperationDigest = recoveryJobItemOperationDigest(items[3])
		invalidFence.PriorTargetRevision = job.TargetChainRevision
		invalidFence.NextTargetRevision = "invalid-fence-next-revision"
		validFence := invalidFence
		rollback := errors.New("rollback valid checkpoint control")
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&validFence).Error; err != nil {
				return err
			}
			return rollback
		}); !errors.Is(err, rollback) {
			t.Fatalf("valid insertion control failed: %v", err)
		}
		invalidFence.AttemptFence++
		if err := db.Create(&invalidFence).Error; err == nil {
			t.Fatal("production migration accepted checkpoint with wrong insertion-time fence")
		}
	})

	t.Run("armed_takeover_is_not_pristine", func(t *testing.T) {
		fixture := newInterleavedExecutionFixture(t, postgres)
		if _, err := fixture.coordinator.PrepareFirstWrite(context.Background(), fixture.claim); err != nil {
			t.Fatalf("arm in-place attempt: %v", err)
		}
		fixture.serviceFixture.advanceTo(fixture.claim.LeaseExpiresAt.Add(time.Second))
		takeover, found, err := fixture.coordinator.TakeoverExpired(context.Background(), "armed-interleaved-takeover")
		if err != nil || !found {
			t.Fatalf("take over armed attempt: found=%t error=%v", found, err)
		}
		before := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.serviceFixture.db, takeover.JobID, takeover.AttemptID)
		if !before.attempt.MutationArmed || before.checkpoints != 0 {
			t.Fatalf("armed takeover control=%+v", before)
		}
		if err := fixture.coordinator.FailPristinePrewrite(context.Background(), takeover); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("armed takeover accepted as pristine: %v", err)
		}
		after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, fixture.serviceFixture.db, takeover.JobID, takeover.AttemptID)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("pristine closure changed armed takeover: before=%+v after=%+v", before, after)
		}
	})

	t.Run("takeover_consumed_authority", func(t *testing.T) {
		fixture := newInterleavedExecutionFixture(t, postgres)
		paused := newPausedAuthorizedExactMirrorDeleteFromExecution(t, "interleaved-takeover", fixture)
		db := fixture.serviceFixture.db
		_, _, _, items := interleavedRows(t, fixture)
		callback := "test:interleaved-delete-projection:" + t.Name()
		if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() && len(fixture.target.deletes) == 1 {
				_ = tx.AddError(errors.New("controlled interruption before delete checkpoint"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		source := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
		err := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, source, paused.request.GrantSecret)
		if removeErr := db.Callback().Create().Remove(callback); removeErr != nil {
			t.Fatal(removeErr)
		}
		if !errors.Is(err, ErrRecoveryWorkerUnavailable) || len(fixture.target.deletes) != 1 {
			t.Fatalf("controlled interruption err=%v delete calls=%d", err, len(fixture.target.deletes))
		}
		var consumed model.BackupAssetRecoveryCheckpoint
		if err := db.Where("job_id = ? AND phase = ?", fixture.jobID, CheckpointPhaseDeleteAuthorityConsumed).Take(&consumed).Error; err != nil {
			t.Fatal(err)
		}
		fixture.serviceFixture.advanceTo(fixture.claim.LeaseExpiresAt.Add(time.Second))
		takeover, found, err := fixture.coordinator.TakeoverExpired(context.Background(), "interleaved-takeover-worker")
		if err != nil || !found || takeover.AttemptID == fixture.claim.AttemptID {
			t.Fatalf("takeover found=%t err=%v", found, err)
		}
		if _, err := fixture.coordinator.BuildRsyncRestoreRequest(context.Background(), takeover); err != nil {
			t.Fatalf("builder rejected authenticated historical consumed authority: %v", err)
		}
		adopted, err := fixture.coordinator.AdoptInterruptedOperation(context.Background(), takeover, items[0].ID)
		if err != nil || adopted.JobItemID != items[0].ID || adopted.AttemptID != takeover.AttemptID || adopted.Sequence != consumed.Sequence+1 {
			t.Fatalf("adopt first delete checkpoint=%+v error=%v", adopted, err)
		}
		var retained model.BackupAssetRecoveryGrant
		if err := db.Where("id = ?", paused.result.GrantID).Take(&retained).Error; err != nil {
			t.Fatal(err)
		}
		if retained.ConsumedAt == nil || retained.DeleteAttemptID == nil || *retained.DeleteAttemptID != fixture.claim.AttemptID ||
			retained.DeleteAttemptFence != fixture.claim.AttemptFence || retained.DeleteNodeFence != fixture.claim.NodeFence {
			t.Fatalf("takeover rewrote consumed grant: %+v", retained)
		}
		continuation := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
		if err := fixture.coordinator.ExecuteClaim(context.Background(), takeover, continuation, ""); err != nil {
			t.Fatalf("continue second delete after adoption: %v", err)
		}
		_, _, job, finalItems := interleavedRows(t, fixture)
		if JobState(job.State) != JobStateSucceeded || finalItems[0].Outcome != "succeeded" || finalItems[2].Outcome != "succeeded" || len(fixture.target.deletes) != 2 {
			t.Fatalf("takeover completion job=%+v items=%+v deletes=%d", job, finalItems, len(fixture.target.deletes))
		}
	})
}
