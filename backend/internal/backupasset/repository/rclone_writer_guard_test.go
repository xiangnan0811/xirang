package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestRcloneWriterGuard(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { runRcloneWriterGuard(t, "sqlite") })
	t.Run("postgres", func(t *testing.T) { runRcloneWriterGuard(t, "postgres") })
}

func runRcloneWriterGuard(t *testing.T, engine string) {
	t.Helper()
	ctx := context.Background()
	db := openProducerRetentionIntegrationDB(t, engine)
	fixture := newRclonePublicationFixtureWithDB(t, backupasset.PublicationVersionedPrefix, db)
	execution, err := fixture.service.Prepare(ctx, fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	if err := execution.Abandon(backupasset.ErrPublicationSessionAbandoned); err != nil {
		t.Fatal(err)
	}
	var template model.RecoveryPoint
	if err := db.First(&template, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	fixture.strategy.reconcileCall = func(context.Context, provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
		calls++
		return provider.PublicationReconcileResult{}, errors.New("provider must not run at deadline")
	}
	sequence := 1
	clone := func(state backupasset.RecoveryPointState, fact *bool) model.RecoveryPoint {
		point := template
		point.ID = fmt.Sprintf("%032x", sequence)
		sequence++
		point.ProducingTaskRunID = nil
		point.State = string(state)
		point.SourceFingerprint = ""
		consistency := backupasset.PublicationConsistencyV1{Version: 1, RcloneWriterQuiesced: fact}
		var encodeErr error
		point.ConsistencyJSON, encodeErr = backupasset.EncodePublicationConsistency(consistency)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if err := db.Create(&point).Error; err != nil {
			t.Fatal(err)
		}
		// BeforeSave encrypts locator fields in place; compare later reads with
		// the decrypted stored value, not the mutated Create argument.
		var stored model.RecoveryPoint
		if err := db.First(&stored, "id = ?", point.ID).Error; err != nil {
			t.Fatal(err)
		}
		return stored
	}
	for _, state := range []backupasset.RecoveryPointState{backupasset.RecoveryPointPreparing, backupasset.RecoveryPointVerifying} {
		for _, fact := range []*bool{nil, new(false), new(true)} {
			point := clone(state, fact)
			setRcloneReconciliationTime(t, fixture, attempt.PointDeadlineAt.Add(-time.Nanosecond))
			if _, expired, err := fixture.service.expireAtDeadline(ctx, point.ID); err != nil || expired {
				t.Fatalf("early expiry state=%s fact=%v expired=%t err=%v", state, fact, expired, err)
			}
			setRcloneReconciliationTime(t, fixture, attempt.PointDeadlineAt)
			outcome, err := fixture.service.ProcessPoint(ctx, point.ID)
			if err != nil {
				t.Fatalf("deadline state=%s fact=%v: %v", state, fact, err)
			}
			var after model.RecoveryPoint
			if err := db.First(&after, "id = ?", point.ID).Error; err != nil {
				t.Fatal(err)
			}
			consistency, err := backupasset.DecodePublicationConsistency(after.ConsistencyJSON)
			if err != nil || after.EncryptedProviderLocator != point.EncryptedProviderLocator || after.LineageJSON != point.LineageJSON {
				t.Fatal("deadline discarded immutable publication data")
			}
			if fact != nil && *fact {
				if outcome.State != backupasset.RecoveryPointFailed || after.State != string(backupasset.RecoveryPointFailed) || consistency.Code != backupasset.FailurePublicationDeadlineExceeded || consistency.RcloneWriterQuiesced == nil || !*consistency.RcloneWriterQuiesced {
					t.Fatal("confirmed stopped writer did not expire")
				}
				continue
			}
			if outcome.State != state || after.State != string(state) || consistency.Provider != backupasset.ProviderRclone || consistency.Completion != backupasset.CompletionOutcomeUnknown || consistency.Code != backupasset.FailurePublicationDeadlineExceeded {
				t.Fatalf("unknown writer was not held: outcome=%+v consistency=%+v", outcome, consistency)
			}
			setRcloneReconciliationTime(t, fixture, attempt.PointDeadlineAt.Add(time.Second))
			replay, err := fixture.service.ProcessPoint(ctx, point.ID)
			if err != nil || replay.State != state {
				t.Fatalf("repeat hold=%+v err=%v", replay, err)
			}
			var repeated model.RecoveryPoint
			if err := db.First(&repeated, "id = ?", point.ID).Error; err != nil {
				t.Fatal(err)
			}
			if repeated.ConsistencyJSON != after.ConsistencyJSON || !repeated.UpdatedAt.Equal(after.UpdatedAt) || repeated.PointRevision != after.PointRevision {
				t.Fatal("repeat hold changed durable state")
			}
			var leases int64
			if err := db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ?", point.ID).Count(&leases).Error; err != nil || leases != 0 {
				t.Fatalf("hold claimed a lease: %d %v", leases, err)
			}
		}
	}
	if calls != 0 {
		t.Fatalf("provider ran %d times at or after deadline", calls)
	}
	if _, err := fixture.service.ProcessPoint(ctx, template.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.ensureNoUnresolvedRcloneWriter(ctx, fixture.repository.ID); !errors.Is(err, backupasset.ErrPublicationInProgress) && !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("competing writer admitted: %v", err)
	}
	rollback := &Service{db: db, publication: fixture.service}
	if err := rollback.reconcileRcloneRollbackPoints(ctx, fixture.repository.ID); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("rollback passed unknown writer: %v", err)
	}
	if calls != 0 {
		t.Fatal("rollback contacted provider for expired hold")
	}

	// More than the previous ten-times-LIMIT scan window of expired holds.
	for range 15 {
		point := clone(backupasset.RecoveryPointPreparing, nil)
		if _, err := fixture.service.ProcessPoint(ctx, point.ID); err != nil {
			t.Fatal(err)
		}
	}
	candidate := clone(backupasset.RecoveryPointPreparing, nil)
	lineage, err := backupasset.DecodePublicationLineage(candidate.LineageJSON)
	if err != nil {
		t.Fatal(err)
	}
	lineage.PointDeadlineAt = attempt.PointDeadlineAt.Add(time.Hour)
	candidate.LineageJSON, err = backupasset.EncodePublicationLineage(lineage)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", candidate.ID).Updates(map[string]any{"lineage_json": candidate.LineageJSON, "updated_at": attempt.PointDeadlineAt.Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	ids, err := fixture.service.ListCandidates(ctx, 1)
	if err != nil || len(ids) != 1 || ids[0] != candidate.ID {
		t.Fatalf("expired holds starved eligible candidate: %v %v", ids, err)
	}

	// Historical failed points cannot be inferred safe from provider, age, or
	// code. Inventory must decode every bounded batch and must not rewrite rows.
	for range 260 {
		clone(backupasset.RecoveryPointFailed, new(true))
	}
	historical := clone(backupasset.RecoveryPointFailed, nil)
	before := historical.ConsistencyJSON
	if err := fixture.service.CheckFailedRcloneWriters(ctx); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("legacy failed writer accepted: %v", err)
	}
	var stored model.RecoveryPoint
	if err := db.First(&stored, "id = ?", historical.ID).Error; err != nil || stored.ConsistencyJSON != before || stored.State != string(backupasset.RecoveryPointFailed) {
		t.Fatal("inventory rewrote historical failed point")
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", historical.ID).Update("consistency_json", `{"version":1,"unknown_future_member":true}`).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CheckFailedRcloneWriters(ctx); err == nil {
		t.Fatal("inventory accepted a codec error")
	}
	valid, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{Version: 1, RcloneWriterQuiesced: new(true)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", historical.ID).Update("consistency_json", valid).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CheckFailedRcloneWriters(ctx); err != nil {
		t.Fatalf("positive writer evidence with historical empty provider rejected: %v", err)
	}
}
