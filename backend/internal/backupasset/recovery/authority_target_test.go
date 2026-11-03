package recovery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

// These tests use production migrations and persist the plan, claim, and
// operation history through the coordinator. The target itself is a controlled
// port; no statement about a real remote filesystem follows from these tests.
func TestRecoveryAuthorityTargetDurableSQLite(t *testing.T) {
	testRecoveryAuthorityTargetDurable(t, false)
}

func TestRecoveryAuthorityTargetDurablePostgres(t *testing.T) {
	testRecoveryAuthorityTargetDurable(t, true)
}

func newRecoveryAuthorityTargetFixture(
	t *testing.T, postgres bool, kind RecoveryOperationKind,
) exactMirrorOrdinaryExecutionFixture {
	t.Helper()
	var fixture *authorizationReceiptServiceFixture
	if postgres {
		fixture = newAuthorizationReceiptPostgresMigrationServiceFixtureWithDeleteCountAndKinds(
			t, AuthorizationReceiptWriteAuthorize, true, false, 1, []RecoveryOperationKind{kind},
		)
	} else {
		fixture = newAuthorizationReceiptSQLiteMigrationServiceFixtureWithDeleteCountAndKinds(
			t, AuthorizationReceiptWriteAuthorize, true, false, 1, []RecoveryOperationKind{kind},
		)
	}
	return newExactMirrorOrdinaryExecutionFixtureFromService(t, fixture)
}

func recoveryAuthorityTargetRowsForTest(
	t *testing.T, fixture exactMirrorOrdinaryExecutionFixture,
) (model.BackupAssetRecoveryPlan, model.BackupAssetRecoveryPreflight, model.BackupAssetRecoveryJob, []model.BackupAssetRecoveryJobItem) {
	t.Helper()
	db := fixture.serviceFixture.db
	var job model.BackupAssetRecoveryJob
	var plan model.BackupAssetRecoveryPlan
	var preflight model.BackupAssetRecoveryPreflight
	var items []model.BackupAssetRecoveryJobItem
	if err := db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", job.PreflightID).Take(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", job.ID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].PlanItemID == nil || items[1].OperationKind != string(RecoveryOperationDelete) {
		t.Fatalf("expected anchor followed by pending delete: %+v", items)
	}
	operations, err := decodeRecoveryOperationRows(preflight.EncryptedOperationRows)
	if err != nil || len(operations) != len(items) {
		t.Fatalf("decode frozen operations: count=%d err=%v", len(operations), err)
	}
	anchor := operations[0]
	digest, err := TargetPathDigest(plan.TargetRootID, plan.RootLocatorDigest, anchor.TargetRelativeLocator)
	if err != nil || anchor.TargetRelativeLocator != plan.EncryptedTargetRelativePath ||
		digest != plan.PathDigest || digest != anchor.TargetPathDigest || digest != items[0].TargetPathDigest {
		t.Fatalf("anchor does not match canonical plan locator: plan=%+v operation=%+v item=%+v err=%v", plan, anchor, items[0], err)
	}
	return plan, preflight, job, items
}

func captureRecoveryAuthorityTargetForTest(
	t *testing.T, fixture exactMirrorOrdinaryExecutionFixture, intent recoveryAuthorityTargetIntent,
) recoveryAuthorityTargetExpectation {
	t.Helper()
	got, err := captureRecoveryAuthorityTarget(context.Background(), fixture.serviceFixture.db, fixture.claim, intent, fixture.serviceFixture.now)
	if err != nil {
		t.Fatalf("capture target expectation: %v", err)
	}
	return got
}

func testRecoveryAuthorityTargetDurable(t *testing.T, postgres bool) {
	t.Run("fresh_and_fences", func(t *testing.T) {
		fixture := newRecoveryAuthorityTargetFixture(t, postgres, RecoveryOperationCreate)
		plan, preflight, job, items := recoveryAuthorityTargetRowsForTest(t, fixture)
		intent := recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetFreshFirstWrite}
		fresh := captureRecoveryAuthorityTargetForTest(t, fixture, intent)
		wantHistory := framedDigest("xirang/recovery/authority-empty-history/v1", job.ID, preflight.TargetRevision)
		if fresh.phase != intent.phase || fresh.jobID != job.ID || fresh.jobTransitionRevision != job.TransitionRevision ||
			fresh.chainRevision != preflight.TargetRevision || fresh.historyDigest != wantHistory ||
			fresh.anchorItemID != items[0].ID || fresh.anchorOperationDigest != recoveryJobItemOperationDigest(items[0]) ||
			fresh.anchorCheckpointID != "" || fresh.expectedDigest != "" || fresh.expectedBytes != 0 ||
			fresh.sourceFenceDigest != recoveryAuthoritySourceFenceDigest(fixture.claim.SourceFence) ||
			!fresh.valid(TargetModeInPlace) || plan.PathDigest != items[0].TargetPathDigest {
			t.Fatalf("fresh expectation has incorrect durable identity: %+v", fresh)
		}
		for _, mutate := range []func(*backupasset.LeaseFence){
			func(fence *backupasset.LeaseFence) { fence.LeaseID += "-different" },
			func(fence *backupasset.LeaseFence) { fence.RecoveryPointID += "-different" },
			func(fence *backupasset.LeaseFence) { fence.HolderType = backupasset.LeaseHolderType("different") },
			func(fence *backupasset.LeaseFence) { fence.OwnerID += "-different" },
			func(fence *backupasset.LeaseFence) { fence.AttemptID += "-different" },
			func(fence *backupasset.LeaseFence) { fence.FenceToken += "-different" },
		} {
			altered := fixture.claim.SourceFence
			mutate(&altered)
			if recoveryAuthoritySourceFenceDigest(altered) == fresh.sourceFenceDigest {
				t.Fatal("source fence digest omitted an identity field")
			}
		}
		if _, err := captureRecoveryAuthorityTarget(context.Background(), fixture.serviceFixture.db, fixture.claim,
			recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetCheckpointed}, fixture.serviceFixture.now); err == nil {
			t.Fatal("unarmed attempt admitted checkpointed authority")
		}
		for _, test := range []struct {
			name   string
			mutate func(*RecoveryWorkerClaim)
		}{
			{"attempt", func(claim *RecoveryWorkerClaim) { claim.AttemptFence++ }},
			{"node", func(claim *RecoveryWorkerClaim) { claim.NodeFence++ }},
			{"source", func(claim *RecoveryWorkerClaim) { claim.SourceFence.FenceToken += "-stale" }},
			{"transition", func(claim *RecoveryWorkerClaim) { claim.TransitionRevision++ }},
		} {
			t.Run(test.name, func(t *testing.T) {
				stale := fixture.claim
				test.mutate(&stale)
				if _, err := captureRecoveryAuthorityTarget(context.Background(), fixture.serviceFixture.db, stale, intent, fixture.serviceFixture.now); err == nil {
					t.Fatal("stale durable fence admitted")
				}
			})
		}
		for _, test := range []struct {
			name   string
			mutate func(*recoveryAuthorityTargetExpectation)
		}{
			{"wrong_anchor_item", func(value *recoveryAuthorityTargetExpectation) { value.anchorItemID = items[1].ID }},
			{"wrong_anchor_operation", func(value *recoveryAuthorityTargetExpectation) { value.anchorOperationDigest = strings.Repeat("a", 64) }},
			{"wrong_source_fence", func(value *recoveryAuthorityTargetExpectation) { value.sourceFenceDigest = strings.Repeat("b", 64) }},
			{"empty_history", func(value *recoveryAuthorityTargetExpectation) { value.historyDigest = "" }},
		} {
			t.Run(test.name, func(t *testing.T) {
				tampered := fresh
				test.mutate(&tampered)
				err := fixture.serviceFixture.db.Transaction(func(tx *gorm.DB) error {
					return revalidateRecoveryAuthorityTargetTx(context.Background(), tx, fixture.claim, intent, tampered, fixture.serviceFixture.now)
				})
				if !errors.Is(err, ErrRecoveryTargetChanged) {
					t.Fatalf("substituted target expectation error=%v", err)
				}
			})
		}
		if _, err := fixture.coordinator.PrepareFirstWrite(context.Background(), fixture.claim); err != nil {
			t.Fatalf("arm first write: %v", err)
		}
		if _, err := captureRecoveryAuthorityTarget(context.Background(), fixture.serviceFixture.db, fixture.claim, intent, fixture.serviceFixture.now); err == nil {
			t.Fatal("armed attempt reused fresh-first-write authority")
		}
		err := fixture.serviceFixture.db.Transaction(func(tx *gorm.DB) error {
			return revalidateRecoveryAuthorityTargetTx(context.Background(), tx, fixture.claim, intent, fresh, fixture.serviceFixture.now)
		})
		if err == nil {
			t.Fatal("pre-arm capture survived arming")
		}
		checkpointed := captureRecoveryAuthorityTargetForTest(t, fixture, recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetCheckpointed})
		if checkpointed.anchorItemID != fresh.anchorItemID || checkpointed.anchorCheckpointID != "" ||
			checkpointed.expectedDigest != "" || checkpointed.chainRevision != preflight.TargetRevision {
			t.Fatalf("armed empty history failed to retain frozen anchor: %+v", checkpointed)
		}
	})

	for _, kind := range []RecoveryOperationKind{RecoveryOperationCreate, RecoveryOperationOverwrite, RecoveryOperationSkip} {
		t.Run("anchor_"+string(kind), func(t *testing.T) {
			fixture := newRecoveryAuthorityTargetFixture(t, postgres, kind)
			_, preflight, beforeJob, items := recoveryAuthorityTargetRowsForTest(t, fixture)
			if _, err := fixture.coordinator.PrepareFirstWrite(context.Background(), fixture.claim); err != nil {
				t.Fatalf("arm anchor: %v", err)
			}
			postIntent := recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetCurrentPost, itemID: items[0].ID}
			post, postErr := captureRecoveryAuthorityTarget(context.Background(), fixture.serviceFixture.db, fixture.claim, postIntent, fixture.serviceFixture.now)
			if kind == RecoveryOperationSkip {
				if postErr == nil {
					t.Fatal("skip admitted as a current post-write anchor")
				}
			} else if postErr != nil || post.currentItemID != items[0].ID || post.anchorItemID != items[0].ID ||
				post.currentOperationDigest != post.anchorOperationDigest || post.expectedDigest != items[0].ExpectedPostIdentityDigest ||
				post.expectedBytes != items[0].ExpectedPostBytes || post.anchorCheckpointID != "" {
				t.Fatalf("current post expectation=%+v err=%v", post, postErr)
			}
			for _, wrongID := range []string{items[1].ID, strings.Repeat("9", 32)} {
				wrong := postIntent
				wrong.itemID = wrongID
				if _, err := captureRecoveryAuthorityTarget(context.Background(), fixture.serviceFixture.db, fixture.claim, wrong, fixture.serviceFixture.now); err == nil {
					t.Fatalf("current post admitted wrong item %q", wrongID)
				}
			}
			primeRecoveryOrdinaryCompletedItem(t, fixture.coordinator, fixture.claim, items[0].ID)
			_, afterPreflight, afterJob, afterItems := recoveryAuthorityTargetRowsForTest(t, fixture)
			var checkpoint model.BackupAssetRecoveryCheckpoint
			if err := fixture.serviceFixture.db.Where("job_id = ? AND phase = ?", fixture.jobID, CheckpointPhaseOperation).Take(&checkpoint).Error; err != nil {
				t.Fatal(err)
			}
			intent := recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetCheckpointed, itemID: afterItems[1].ID}
			captured := captureRecoveryAuthorityTargetForTest(t, fixture, intent)
			wantDigest, wantBytes, wantOutcome := afterItems[0].ExpectedPostIdentityDigest, afterItems[0].ExpectedPostBytes, "succeeded"
			if kind == RecoveryOperationSkip {
				wantDigest, wantBytes, wantOutcome = afterItems[0].ExpectedPriorDigest, afterItems[0].ExpectedPriorBytes, "skipped"
			}
			if captured.anchorItemID != afterItems[0].ID || captured.anchorCheckpointID != checkpoint.ID ||
				captured.anchorOperationDigest != checkpoint.OperationDigest || captured.expectedDigest != wantDigest ||
				captured.expectedBytes != wantBytes || captured.currentItemID != afterItems[1].ID ||
				captured.chainRevision != afterJob.TargetChainRevision || afterItems[0].Outcome != wantOutcome ||
				afterPreflight.TargetRevision != preflight.TargetRevision || !afterPreflight.ExpiresAt.Equal(preflight.ExpiresAt) {
				t.Fatalf("checkpointed anchor identity=%+v item=%+v checkpoint=%+v", captured, afterItems[0], checkpoint)
			}
			if (kind == RecoveryOperationSkip) != (afterJob.TargetChainRevision == beforeJob.TargetChainRevision) {
				t.Fatalf("incorrect chain advancement for %s: before=%q after=%q", kind, beforeJob.TargetChainRevision, afterJob.TargetChainRevision)
			}
			if _, err := captureRecoveryAuthorityTarget(context.Background(), fixture.serviceFixture.db, fixture.claim, postIntent, fixture.serviceFixture.now); err == nil {
				t.Fatal("checkpointed anchor admitted as still-pending current post")
			}
			err := fixture.serviceFixture.db.Transaction(func(tx *gorm.DB) error {
				return revalidateRecoveryAuthorityTargetTx(context.Background(), tx, fixture.claim, intent, captured, fixture.serviceFixture.now)
			})
			if err != nil {
				t.Fatalf("revalidate exact checkpointed expectation: %v", err)
			}
		})
	}

	t.Run("job_bound_seal", func(t *testing.T) {
		fixture := newRecoveryAuthorityTargetFixture(t, postgres, RecoveryOperationCreate)
		_, _, _, items := recoveryAuthorityTargetRowsForTest(t, fixture)
		if _, err := fixture.coordinator.PrepareFirstWrite(context.Background(), fixture.claim); err != nil {
			t.Fatal(err)
		}
		expectation := captureRecoveryAuthorityTargetForTest(t, fixture, recoveryAuthorityTargetIntent{
			phase: recoveryAuthorityTargetCurrentPost, itemID: items[0].ID,
		})
		harness := newRecoveryEligibilityHarnessOnDB(t, fixture.serviceFixture.db)
		harness.binding.TargetMode = TargetModeInPlace
		harness.binding.ConflictPolicy = ConflictExactMirror
		harness.binding.targetExpectation = expectation
		harness.target.current.TargetRevision = "current-verified-target-metadata"
		harness.target.current.targetContentVerified = true
		harness.target.current.verifiedTargetDigest = expectation.expectedDigest
		harness.target.current.verifiedTargetBytes = expectation.expectedBytes
		sealed, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
		if err != nil {
			t.Fatalf("seal job-bound target observation: %v", err)
		}
		if sealed.binding.target.TargetRevision == harness.binding.PreflightTargetRevision ||
			sealed.binding.authority.targetExpectation != expectation {
			t.Fatalf("seal did not retain the actual target revision and job expectation: %#v", sealed)
		}
		revalidate := func(binding RecoveryAuthorityBinding, observation RecoveryAuthorityObservation) error {
			return fixture.serviceFixture.db.Transaction(func(tx *gorm.DB) error {
				return harness.authority.RevalidateRecoveryAuthorityTx(context.Background(), tx, binding, observation)
			})
		}
		if err := revalidate(harness.binding, sealed); err != nil {
			t.Fatalf("revalidate exact job-bound seal: %v", err)
		}
		for _, test := range []struct {
			name   string
			mutate func(*RecoveryAuthorityBinding, *RecoveryAuthorityObservation)
		}{
			{"different_job", func(binding *RecoveryAuthorityBinding, _ *RecoveryAuthorityObservation) {
				binding.targetExpectation.jobID = strings.Repeat("9", 32)
			}},
			{"different_item", func(binding *RecoveryAuthorityBinding, _ *RecoveryAuthorityObservation) {
				binding.targetExpectation.anchorItemID = items[1].ID
			}},
			{"different_fence", func(binding *RecoveryAuthorityBinding, _ *RecoveryAuthorityObservation) {
				binding.targetExpectation.attemptFence++
			}},
			{"different_observed_digest", func(_ *RecoveryAuthorityBinding, observation *RecoveryAuthorityObservation) {
				observation.binding.target.verifiedTargetDigest = strings.Repeat("b", 64)
			}},
			{"different_observed_size", func(_ *RecoveryAuthorityBinding, observation *RecoveryAuthorityObservation) {
				observation.binding.target.verifiedTargetBytes++
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				binding, altered := harness.binding, sealed
				test.mutate(&binding, &altered)
				if err := revalidate(binding, altered); !errors.Is(err, ErrRecoveryTargetChanged) {
					t.Fatalf("substituted job-bound seal error=%v", err)
				}
			})
		}
	})

	t.Run("verification_evidence_replay", func(t *testing.T) {
		fixture := newRecoveryAuthorityTargetFixture(t, postgres, RecoveryOperationCreate)
		_, _, beforeJob, items := recoveryAuthorityTargetRowsForTest(t, fixture)
		primeRecoveryOrdinaryCompletedItem(t, fixture.coordinator, fixture.claim, items[0].ID)
		_, _, completedJob, completedItems := recoveryAuthorityTargetRowsForTest(t, fixture)
		item := completedItems[0]
		var checkpoint model.BackupAssetRecoveryCheckpoint
		if err := fixture.serviceFixture.db.Where("job_id = ? AND job_item_id = ? AND phase = ?", fixture.jobID, item.ID, CheckpointPhaseOperation).Take(&checkpoint).Error; err != nil {
			t.Fatal(err)
		}
		project := func(claim RecoveryWorkerClaim, checkpointID string, bytes, size int64, digest string) (model.BackupAssetRecoveryEvidence, error) {
			return fixture.coordinator.ProjectOperationVerification(context.Background(), claim, item.ID, checkpointID, bytes, size, digest, fixture.serviceFixture.now)
		}
		evidence, err := project(fixture.claim, checkpoint.ID, item.BytesWritten, item.VerifiedSize, item.VerifiedDigest)
		if err != nil {
			t.Fatalf("attach evidence to completed checkpoint: %v", err)
		}
		replay, err := project(fixture.claim, checkpoint.ID, item.BytesWritten, item.VerifiedSize, item.VerifiedDigest)
		if err != nil || replay.ID != evidence.ID {
			t.Fatalf("exact verification replay evidence=%+v replay=%+v err=%v", evidence, replay, err)
		}
		for _, test := range []struct {
			name         string
			claim        RecoveryWorkerClaim
			checkpointID string
			bytes, size  int64
			digest       string
		}{
			{"bytes", fixture.claim, checkpoint.ID, item.BytesWritten + 1, item.VerifiedSize, item.VerifiedDigest},
			{"size", fixture.claim, checkpoint.ID, item.BytesWritten, item.VerifiedSize + 1, item.VerifiedDigest},
			{"digest", fixture.claim, checkpoint.ID, item.BytesWritten, item.VerifiedSize, strings.Repeat("b", 64)},
			{"checkpoint", fixture.claim, strings.Repeat("9", 32), item.BytesWritten, item.VerifiedSize, item.VerifiedDigest},
		} {
			t.Run(test.name, func(t *testing.T) {
				if _, err := project(test.claim, test.checkpointID, test.bytes, test.size, test.digest); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
					t.Fatalf("mismatched verification evidence error=%v", err)
				}
			})
		}
		stale := fixture.claim
		stale.NodeFence++
		if _, err := project(stale, checkpoint.ID, item.BytesWritten, item.VerifiedSize, item.VerifiedDigest); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("stale verification fence error=%v", err)
		}
		_, _, afterJob, afterItems := recoveryAuthorityTargetRowsForTest(t, fixture)
		var count int64
		if err := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryEvidence{}).Where("job_id = ? AND kind = ?", fixture.jobID, "verification").Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 || !reflect.DeepEqual(afterItems[0], item) || afterJob.TargetChainRevision != completedJob.TargetChainRevision ||
			completedJob.TargetChainRevision == beforeJob.TargetChainRevision {
			t.Fatalf("verification replay changed durable projection: evidence=%d item=%+v/%+v chain=%q/%q", count, item, afterItems[0], completedJob.TargetChainRevision, afterJob.TargetChainRevision)
		}
	})

	t.Run("adoption_verified", func(t *testing.T) {
		fixture, before, item := interruptedPublishedRecoveryAnchorForTest(t, postgres)
		claim := takeoverRecoveryAuthorityFixtureForTest(t, fixture)
		blindSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, claim.JobID)
		if err := fixture.coordinator.ExecuteClaim(context.Background(), claim, blindSource, ""); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("replayed interrupted write before adoption: %v", err)
		}
		writesBefore := len(fixture.target.writes)
		checkpoint, err := fixture.coordinator.AdoptInterruptedOperation(context.Background(), claim, item.ID)
		if err != nil {
			t.Fatalf("adopt verified published anchor: %v", err)
		}
		_, _, job, items := recoveryAuthorityTargetRowsForTest(t, fixture)
		if checkpoint.Sequence != 0 || checkpoint.JobItemID != item.ID || checkpoint.AttemptID != claim.AttemptID ||
			checkpoint.AttemptFence != claim.AttemptFence || checkpoint.NodeFence != claim.NodeFence ||
			checkpoint.PriorTargetRevision != before.TargetChainRevision || checkpoint.NextTargetRevision != job.TargetChainRevision ||
			items[0].Outcome != "succeeded" || items[0].VerifiedDigest != items[0].ExpectedPostIdentityDigest ||
			items[0].VerifiedSize != items[0].ExpectedPostBytes || len(fixture.target.writes) != writesBefore {
			t.Fatalf("adoption did not preserve verified anchor and takeover fence: checkpoint=%+v job=%+v item=%+v", checkpoint, job, items[0])
		}
		captured := captureRecoveryAuthorityTargetForTest(t, exactMirrorOrdinaryExecutionFixture{
			serviceFixture: fixture.serviceFixture, coordinator: fixture.coordinator, claim: claim, target: fixture.target, jobID: fixture.jobID,
		}, recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetCheckpointed, itemID: items[1].ID})
		if captured.anchorCheckpointID != checkpoint.ID || captured.attemptID != claim.AttemptID || captured.nodeFence != claim.NodeFence {
			t.Fatalf("adopted anchor was not bound to current expectation: %+v", captured)
		}
	})

	t.Run("adoption_mismatch", func(t *testing.T) {
		fixture, before, item := interruptedPublishedRecoveryAnchorForTest(t, postgres)
		claim := takeoverRecoveryAuthorityFixtureForTest(t, fixture)
		fixture.target.verifyObservation = func(_ int, observation TargetVerifyObservation) TargetVerifyObservation {
			observation.Present.IdentityDigest = strings.Repeat("b", 64)
			return observation
		}
		spy := &recoveryWorkerAuthorityEffectSpy{observeErr: ErrRecoveryTargetChanged, revalidateErr: ErrRecoveryTargetChanged}
		fixture.coordinator.liveRevalidator = spy
		writesBefore := len(fixture.target.writes)
		if _, err := fixture.coordinator.AdoptInterruptedOperation(context.Background(), claim, item.ID); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("adoption mismatch error=%v", err)
		}
		if len(spy.events) != 0 || len(fixture.target.writes) != writesBefore {
			t.Fatalf("mismatch used positive authority or replayed a write: events=%v writes=%d/%d", spy.events, writesBefore, len(fixture.target.writes))
		}
		assertInterruptedAnchorFailureForTest(t, fixture, claim, before, item.ID)
	})

	t.Run("adoption_missing_overwrite", func(t *testing.T) {
		fixture := newRecoveryAuthorityTargetFixture(t, postgres, RecoveryOperationOverwrite)
		_, _, before, items := recoveryAuthorityTargetRowsForTest(t, fixture)
		if _, err := fixture.coordinator.PrepareFirstWrite(context.Background(), fixture.claim); err != nil {
			t.Fatalf("arm overwrite fixture: %v", err)
		}
		handoff, err := fixture.coordinator.loadOrdinaryOperationHandoff(context.Background(), fixture.claim, items[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		fixture.target.rememberTargetState(handoff.object, recoveryExecutionTargetState{kind: TargetPresenceAbsent, revision: "target-absent-before-publication"})
		claim := takeoverRecoveryAuthorityFixtureForTest(t, fixture)
		if _, err := fixture.coordinator.AdoptInterruptedOperation(context.Background(), claim, items[0].ID); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("missing overwrite target was adopted: %v", err)
		}
		if len(fixture.target.writes) != 0 {
			t.Fatalf("missing overwrite target caused a blind replay: %d writes", len(fixture.target.writes))
		}
		assertInterruptedAnchorFailureForTest(t, fixture, claim, before, items[0].ID)
	})

	t.Run("adoption_checkpointed_anchor_consumed_delete", func(t *testing.T) {
		paused := newPausedAuthorizedExactMirrorDeleteFromExecution(t, "anchor-consumed-delete", newRecoveryAuthorityTargetFixture(t, postgres, RecoveryOperationCreate))
		fixture := paused.execution
		db := fixture.serviceFixture.db
		_, _, _, items := recoveryAuthorityTargetRowsForTest(t, fixture)
		anchor := items[0]
		var anchorCheckpoint model.BackupAssetRecoveryCheckpoint
		if err := db.Where("job_id = ? AND job_item_id = ? AND phase = ?", fixture.jobID, anchor.ID, CheckpointPhaseOperation).Take(&anchorCheckpoint).Error; err != nil {
			t.Fatal(err)
		}
		callback := "test:interrupt-consumed-delete:" + t.Name()
		projectionErr := errors.New("controlled interruption before delete checkpoint")
		if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() && len(fixture.target.deletes) == 1 {
				_ = tx.AddError(projectionErr)
			}
		}); err != nil {
			t.Fatal(err)
		}
		source := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
		interrupted := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, source, paused.request.GrantSecret)
		if err := db.Callback().Create().Remove(callback); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(interrupted, ErrRecoveryWorkerUnavailable) || len(fixture.target.deletes) != 1 {
			t.Fatalf("interrupt consumed delete: err=%v deletes=%d", interrupted, len(fixture.target.deletes))
		}
		var consumed model.BackupAssetRecoveryCheckpoint
		var grant model.BackupAssetRecoveryGrant
		if err := db.Where("job_id = ? AND phase = ?", fixture.jobID, CheckpointPhaseDeleteAuthorityConsumed).Take(&consumed).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Where("id = ?", paused.result.GrantID).Take(&grant).Error; err != nil {
			t.Fatal(err)
		}
		if grant.ConsumedAt == nil || consumed.DeleteGrantID != grant.ID || consumed.AttemptID != fixture.claim.AttemptID {
			t.Fatalf("delete consumption lost original authority: checkpoint=%+v grant=%+v", consumed, grant)
		}
		claim := takeoverRecoveryAuthorityFixtureForTest(t, fixture)
		spy := &recoveryWorkerAuthorityEffectSpy{observation: RecoveryAuthorityObservation{
			observedAt: fixture.serviceFixture.now, expiresAt: fixture.serviceFixture.now.Add(time.Minute),
		}}
		fixture.coordinator.liveRevalidator = spy
		deletesBefore := len(fixture.target.deletes)
		adopted, err := fixture.coordinator.AdoptInterruptedOperation(context.Background(), claim, items[1].ID)
		if err != nil {
			t.Fatalf("adopt verified consumed delete: %v", err)
		}
		if len(spy.observeBindings) == 0 || len(spy.revalidateBindings) == 0 {
			t.Fatalf("delete adoption omitted fresh authority: %v", spy.events)
		}
		expectation := spy.observeBindings[0].targetExpectation
		if expectation.phase != recoveryAuthorityTargetCheckpointed || expectation.anchorItemID != anchor.ID ||
			expectation.anchorCheckpointID != anchorCheckpoint.ID || expectation.expectedDigest != anchor.ExpectedPostIdentityDigest ||
			expectation.expectedBytes != anchor.ExpectedPostBytes || expectation.currentItemID != items[1].ID ||
			spy.revalidateBindings[0] != spy.observeBindings[0] || adopted.JobItemID != items[1].ID ||
			adopted.AttemptID != claim.AttemptID || adopted.Sequence != consumed.Sequence+1 || len(fixture.target.deletes) != deletesBefore {
			t.Fatalf("adoption lost checkpointed anchor or replayed delete: expectation=%+v checkpoint=%+v deletes=%d/%d", expectation, adopted, deletesBefore, len(fixture.target.deletes))
		}
		var retained model.BackupAssetRecoveryGrant
		if err := db.Where("id = ?", grant.ID).Take(&retained).Error; err != nil {
			t.Fatal(err)
		}
		if retained.ConsumedAt == nil || !retained.ConsumedAt.Equal(*grant.ConsumedAt) ||
			retained.DeleteAttemptID == nil || *retained.DeleteAttemptID != fixture.claim.AttemptID ||
			retained.DeleteAttemptFence != fixture.claim.AttemptFence || retained.DeleteNodeFence != fixture.claim.NodeFence {
			t.Fatalf("takeover rewrote consumed delete grant: before=%+v after=%+v", grant, retained)
		}
	})

	t.Run("history_integrity", func(t *testing.T) {
		fixture := newRecoveryAuthorityTargetFixture(t, postgres, RecoveryOperationCreate)
		plan, preflight, _, items := recoveryAuthorityTargetRowsForTest(t, fixture)
		primeRecoveryOrdinaryCompletedItem(t, fixture.coordinator, fixture.claim, items[0].ID)
		_, _, job, items := recoveryAuthorityTargetRowsForTest(t, fixture)
		intent := recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetCheckpointed, itemID: items[1].ID}
		captured := captureRecoveryAuthorityTargetForTest(t, fixture, intent)
		var checkpoints []model.BackupAssetRecoveryCheckpoint
		if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
			t.Fatal(err)
		}
		rows, err := rebuildExecuteOperationRows(plan, preflight, fixture.serviceFixture.db)
		if err != nil {
			t.Fatal(err)
		}
		operations, err := newInPlaceOrdinaryCheckpointOperations(plan, job, items, rows)
		if err != nil || len(checkpoints) != 1 {
			t.Fatalf("load persisted anchor history: checkpoints=%+v err=%v", checkpoints, err)
		}
		for _, test := range []struct {
			name   string
			mutate func(*model.BackupAssetRecoveryCheckpoint)
		}{
			{"wrong_item", func(checkpoint *model.BackupAssetRecoveryCheckpoint) { checkpoint.JobItemID = items[1].ID }},
			{"wrong_operation", func(checkpoint *model.BackupAssetRecoveryCheckpoint) {
				checkpoint.OperationDigest = strings.Repeat("b", 64)
			}},
			{"reordered", func(checkpoint *model.BackupAssetRecoveryCheckpoint) { checkpoint.Sequence = 1 }},
			{"invalid_prior_chain", func(checkpoint *model.BackupAssetRecoveryCheckpoint) { checkpoint.PriorTargetRevision = "other-chain" }},
			{"empty_next_chain", func(checkpoint *model.BackupAssetRecoveryCheckpoint) { checkpoint.NextTargetRevision = "" }},
		} {
			t.Run(test.name, func(t *testing.T) {
				bad := append([]model.BackupAssetRecoveryCheckpoint(nil), checkpoints...)
				test.mutate(&bad[0])
				if _, _, _, err := validateInPlaceOrdinaryCheckpointHistory(plan, job, fixture.claim, bad, operations, fixture.serviceFixture.now); err == nil {
					t.Fatal("invalid checkpoint history was accepted")
				}
			})
		}
		rollback := errors.New("rollback test-only durable corruption")
		for _, test := range []struct {
			name   string
			mutate func(*gorm.DB) error
		}{
			{"missing_checkpoint", func(tx *gorm.DB) error {
				return tx.Where("id = ?", checkpoints[0].ID).Delete(&model.BackupAssetRecoveryCheckpoint{}).Error
			}},
			{"empty_chain", func(tx *gorm.DB) error {
				return tx.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", job.ID).Update("target_chain_revision", "").Error
			}},
			{"invalid_chain", func(tx *gorm.DB) error {
				return tx.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", job.ID).Update("target_chain_revision", "other-chain").Error
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				err := fixture.serviceFixture.db.Transaction(func(tx *gorm.DB) error {
					if err := test.mutate(tx); err != nil {
						return err
					}
					if err := revalidateRecoveryAuthorityTargetTx(context.Background(), tx, fixture.claim, intent, captured, fixture.serviceFixture.now); err == nil {
						t.Error("durable history drift was accepted")
					}
					return rollback
				})
				if !errors.Is(err, rollback) {
					t.Fatalf("test-only corruption or rollback failed: %v", err)
				}
			})
		}
	})

	t.Run("isolated_completed_history", func(t *testing.T) {
		fixture, item, checkpoint := newCompletedIsolatedRecoveryAuthorityFixture(t, postgres)
		intent := recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetCheckpointed, itemID: item.ID}
		expectation := captureRecoveryAuthorityTargetForTest(t, fixture, intent)
		if expectation.currentItemID != item.ID || expectation.anchorItemID != "" ||
			expectation.anchorCheckpointID != "" || expectation.expectedDigest != "" ||
			expectation.chainRevision != checkpoint.NextTargetRevision || !validDigest(expectation.historyDigest) {
			t.Fatalf("completed isolated history expectation=%+v checkpoint=%+v", expectation, checkpoint)
		}
		evidence, err := fixture.coordinator.ProjectOperationVerification(context.Background(), fixture.claim,
			item.ID, checkpoint.ID, item.BytesWritten, item.VerifiedSize, item.VerifiedDigest, fixture.serviceFixture.now)
		if err != nil || evidence.Outcome != "succeeded" || evidence.CheckpointID == nil || *evidence.CheckpointID != checkpoint.ID {
			t.Fatalf("completed isolated verification evidence=%+v err=%v", evidence, err)
		}
	})

	t.Run("issuance_final_clock_rollback", func(t *testing.T) {
		for _, name := range []string{"observation_expiry", "deadline", "clock_rollback"} {
			t.Run(name, func(t *testing.T) {
				fixture := newRecoveryAuthorityTargetFixture(t, postgres, RecoveryOperationCreate)
				start := fixture.serviceFixture.now
				expiry := start.Add(time.Minute)
				finalTime := expiry
				switch name {
				case "deadline":
					expiry = fixture.claim.AbsoluteDeadline.Add(time.Minute)
					finalTime = fixture.claim.AbsoluteDeadline
				case "clock_rollback":
					finalTime = start.Add(-time.Nanosecond)
				}
				spy := &recoveryWorkerAuthorityEffectSpy{observation: RecoveryAuthorityObservation{observedAt: start, expiresAt: expiry}}
				fixture.coordinator.liveRevalidator = spy
				var latchBefore int64
				if err := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryEvidence{}).Where("id = ?", recoverySchemaUseLatchRowID).Count(&latchBefore).Error; err != nil {
					t.Fatal(err)
				}
				fired := false
				callback := "test:authority-final-clock:" + t.Name()
				if err := fixture.serviceFixture.db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
					if fired || tx.Error != nil || tx.RowsAffected != 1 || tx.Statement == nil ||
						tx.Statement.Table != (model.BackupAssetRecoveryAttempt{}).TableName() {
						return
					}
					updates, ok := tx.Statement.Dest.(map[string]any)
					if ok && updates["mutation_armed"] == true {
						fired = true
						fixture.serviceFixture.now = finalTime
					}
				}); err != nil {
					t.Fatal(err)
				}
				_, issueErr := fixture.coordinator.PrepareFirstWrite(context.Background(), fixture.claim)
				if err := fixture.serviceFixture.db.Callback().Update().Remove(callback); err != nil {
					t.Fatal(err)
				}
				if !fired || issueErr == nil {
					t.Fatalf("final clock boundary was not rejected: fired=%t err=%v", fired, issueErr)
				}
				var attempt model.BackupAssetRecoveryAttempt
				var latchAfter, checkpoints int64
				if err := fixture.serviceFixture.db.Where("id = ?", fixture.claim.AttemptID).Take(&attempt).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryEvidence{}).Where("id = ?", recoverySchemaUseLatchRowID).Count(&latchAfter).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).Where("job_id = ?", fixture.jobID).Count(&checkpoints).Error; err != nil {
					t.Fatal(err)
				}
				if attempt.MutationArmed || latchAfter != latchBefore || checkpoints != 0 || len(fixture.target.writes) != 0 {
					t.Fatalf("expired issuance committed effects: attempt=%+v latches=%d/%d checkpoints=%d writes=%d", attempt, latchBefore, latchAfter, checkpoints, len(fixture.target.writes))
				}
			})
		}
	})

	t.Run("observation_time_boundaries", func(t *testing.T) {
		fixture := newRecoveryAuthorityTargetFixture(t, postgres, RecoveryOperationCreate)
		plan, preflight, _, _ := recoveryAuthorityTargetRowsForTest(t, fixture)
		start := fixture.serviceFixture.now
		spy := &recoveryWorkerAuthorityEffectSpy{observation: RecoveryAuthorityObservation{
			observedAt: start, expiresAt: start.Add(time.Minute),
		}}
		fixture.coordinator.liveRevalidator = spy
		intent := recoveryAuthorityTargetIntent{phase: recoveryAuthorityTargetFreshFirstWrite}
		observed, err := fixture.coordinator.observeRecoveryAuthority(context.Background(), plan, preflight, fixture.claim, intent)
		if err != nil {
			t.Fatalf("observe fresh target: %v", err)
		}
		revalidate := func() error {
			return fixture.serviceFixture.db.Transaction(func(tx *gorm.DB) error {
				return fixture.coordinator.revalidateObservedRecoveryAuthorityTx(context.Background(), tx, plan, preflight, observed)
			})
		}
		if err := revalidate(); err != nil {
			t.Fatalf("revalidate fresh observation: %v", err)
		}
		fixture.serviceFixture.now = start.Add(-time.Nanosecond)
		if err := revalidate(); !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("clock rollback error=%v", err)
		}
		fixture.serviceFixture.now = spy.observation.expiresAt
		if err := revalidate(); !errors.Is(err, ErrRecoveryTargetChanged) {
			t.Fatalf("observation expiry equality error=%v", err)
		}
		if _, err := captureRecoveryAuthorityTarget(context.Background(), fixture.serviceFixture.db, fixture.claim, intent, fixture.claim.AbsoluteDeadline); err == nil {
			t.Fatal("absolute deadline equality admitted new authority")
		}
	})
}

// Seed a consistent, still-running legacy isolated job whose only item and
// checkpoint are already complete. Production completion normally follows in
// the same transaction; this exercises the supported persisted-history seam.
func newCompletedIsolatedRecoveryAuthorityFixture(
	t *testing.T, postgres bool,
) (exactMirrorOrdinaryExecutionFixture, model.BackupAssetRecoveryJobItem, model.BackupAssetRecoveryCheckpoint) {
	t.Helper()
	var base *authorizationReceiptServiceFixture
	if postgres {
		base = newAuthorizationReceiptPostgresMigrationServiceFixtureWithDeleteCountAndKinds(
			t, AuthorizationReceiptWriteAuthorize, false, false, 0, []RecoveryOperationKind{RecoveryOperationCreate},
		)
	} else {
		base = newAuthorizationReceiptSQLiteMigrationServiceFixtureWithDeleteCountAndKinds(
			t, AuthorizationReceiptWriteAuthorize, false, false, 0, []RecoveryOperationKind{RecoveryOperationCreate},
		)
	}
	fixture := newExactMirrorOrdinaryExecutionFixtureFromService(t, base)
	if _, err := fixture.coordinator.PrepareFirstWrite(context.Background(), fixture.claim); err != nil {
		t.Fatalf("arm isolated fixture: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	var item model.BackupAssetRecoveryJobItem
	if err := base.db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := base.db.Where("job_id = ?", fixture.jobID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	advance := recoveryTargetChainAdvanceForTest(t, job, item, fixture.claim)
	nextRevision, err := advance.NextRevision()
	if err != nil {
		t.Fatal(err)
	}
	checkpointID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := model.BackupAssetRecoveryCheckpoint{
		ID: checkpointID, JobID: job.ID, JobItemID: item.ID, AttemptID: fixture.claim.AttemptID,
		Sequence: 1, Phase: string(CheckpointPhaseOperation), AuthorityCategory: string(AuthorityWrite),
		OperationDigest: recoveryJobItemOperationDigest(item), PriorTargetRevision: job.TargetChainRevision,
		NextTargetRevision: nextRevision, NodeFence: fixture.claim.NodeFence, AttemptFence: fixture.claim.AttemptFence,
		PlanBindingDigest: job.PlanBindingDigest, SourceRevisionDigest: job.SourceRevisionDigest,
		PreflightID: job.PreflightID, PreflightRevision: job.PreflightRevision, PreflightExpiresAt: job.PreflightExpiresAt,
		SecurityDecision: job.SecurityDecision, SecurityDecisionDigest: job.SecurityDecisionDigest,
		SecurityFindingSetDigest: job.SecurityFindingSetDigest, SecurityPolicyRevision: job.SecurityPolicyRevision,
		AuthorityGrantID: job.AuthorityGrantID, JobAuthorityCategory: job.AuthorityCategory,
		AuthorityBindingDigest: job.AuthorityBindingDigest, AuthorityExpiresAt: job.AuthorityExpiresAt,
		CreatedAt: base.now,
	}
	err = base.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&checkpoint).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.BackupAssetRecoveryJobItem{}).Where("id = ? AND outcome = ''", item.ID).
			Updates(map[string]any{"outcome": "succeeded", "bytes_written": item.ExpectedPostBytes,
				"verified_size": item.ExpectedPostBytes, "verified_digest": item.ExpectedPostIdentityDigest,
				"updated_at": base.now}).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetRecoveryJob{}).Where("id = ? AND state = ?", job.ID, JobStateRunning).
			Updates(map[string]any{"target_chain_revision": nextRevision, "workspace_phase": WorkspacePhaseWriting, "updated_at": base.now}).Error
	})
	if err != nil {
		t.Fatalf("seed completed isolated history: %v", err)
	}
	if err := base.db.Where("id = ?", item.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	return fixture, item, checkpoint
}

func interruptedPublishedRecoveryAnchorForTest(
	t *testing.T, postgres bool,
) (exactMirrorOrdinaryExecutionFixture, model.BackupAssetRecoveryJob, model.BackupAssetRecoveryJobItem) {
	t.Helper()
	fixture := newRecoveryAuthorityTargetFixture(t, postgres, RecoveryOperationCreate)
	_, _, before, items := recoveryAuthorityTargetRowsForTest(t, fixture)
	callback := "test:interrupt-anchor-projection:" + t.Name()
	projectionErr := errors.New("controlled interruption before operation checkpoint")
	if err := fixture.serviceFixture.db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
			len(fixture.target.writes) == 1 {
			_ = tx.AddError(projectionErr)
		}
	}); err != nil {
		t.Fatalf("register interruption: %v", err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
	executeErr := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, source, "")
	if err := fixture.serviceFixture.db.Callback().Create().Remove(callback); err != nil {
		t.Fatalf("remove interruption: %v", err)
	}
	if !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) || len(fixture.target.writes) != 1 {
		t.Fatalf("interrupt after controlled target write: err=%v writes=%d", executeErr, len(fixture.target.writes))
	}
	_, _, after, current := recoveryAuthorityTargetRowsForTest(t, fixture)
	var count int64
	if err := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).Where("job_id = ?", fixture.jobID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if current[0].Outcome != "" || count != 0 || before.TargetChainRevision != after.TargetChainRevision {
		t.Fatalf("interrupted anchor crossed checkpoint boundary: item=%+v checkpoints=%d before=%q after=%q", current[0], count, before.TargetChainRevision, after.TargetChainRevision)
	}
	return fixture, before, items[0]
}

func takeoverRecoveryAuthorityFixtureForTest(
	t *testing.T, fixture exactMirrorOrdinaryExecutionFixture,
) RecoveryWorkerClaim {
	t.Helper()
	fixture.serviceFixture.now = fixture.claim.LeaseExpiresAt.Add(time.Second)
	claim, found, err := fixture.coordinator.TakeoverExpired(context.Background(), "recovery-authority-anchor-takeover")
	if err != nil || !found || claim.JobID != fixture.jobID || claim.AttemptID == fixture.claim.AttemptID ||
		claim.AttemptFence == fixture.claim.AttemptFence || claim.NodeFence == fixture.claim.NodeFence ||
		claim.SourceFence.FenceToken == fixture.claim.SourceFence.FenceToken {
		t.Fatalf("take over expired anchor claim: claim=%+v found=%t err=%v", claim, found, err)
	}
	return claim
}

func assertInterruptedAnchorFailureForTest(
	t *testing.T, fixture exactMirrorOrdinaryExecutionFixture, claim RecoveryWorkerClaim,
	before model.BackupAssetRecoveryJob, itemID string,
) {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	var item model.BackupAssetRecoveryJobItem
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	var failures []model.BackupAssetRecoveryEvidence
	db := fixture.serviceFixture.db
	if err := db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", itemID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", job.ID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ? AND kind = ?", job.ID, "failure").Find(&failures).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateNeedsAttention) || job.TargetChainRevision != before.TargetChainRevision ||
		item.Outcome != "failed" || len(checkpoints) != 1 || checkpoints[0].JobItemID != item.ID ||
		checkpoints[0].Phase != string(CheckpointPhaseOperationUnresolved) || checkpoints[0].NextTargetRevision != "" ||
		checkpoints[0].UnresolvedCategory != string(UnresolvedOperationVerificationMismatch) ||
		checkpoints[0].SourceRevalidationOutcome != string(SourceRevalidationMatched) ||
		checkpoints[0].AttemptID != claim.AttemptID || checkpoints[0].AttemptFence != claim.AttemptFence ||
		checkpoints[0].NodeFence != claim.NodeFence || len(failures) != 1 || failures[0].Outcome != "needs_attention" {
		t.Fatalf("interrupted anchor failure projection job=%+v item=%+v checkpoints=%+v failures=%+v", job, item, checkpoints, failures)
	}
	if _, err := fixture.coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("failed adoption retained new write authority: %v", err)
	}
}
