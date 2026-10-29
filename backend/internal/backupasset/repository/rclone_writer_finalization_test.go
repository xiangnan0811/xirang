package repository

import (
	"context"
	"errors"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
)

func TestRcloneWriterFactsFinalizeAtomically(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { runRcloneWriterFactsFinalizeAtomically(t, "sqlite") })
	t.Run("postgres", func(t *testing.T) { runRcloneWriterFactsFinalizeAtomically(t, "postgres") })
}

func runRcloneWriterFactsFinalizeAtomically(t *testing.T, engine string) {
	t.Helper()
	ctx := context.Background()
	fixture := newRclonePublicationFixtureWithDB(t, backupasset.PublicationVersionedPrefix, openProducerRetentionIntegrationDB(t, engine))
	cases := []struct {
		code       backupasset.PublicationFailureCode
		reject     bool
		completion backupasset.ProviderCompletionClass
	}{
		{backupasset.FailurePublicationPreconditionMissing, true, ""},
		{backupasset.FailureProviderNonzeroExit, false, backupasset.CompletionKnownNonzero},
		{backupasset.FailureProviderCompletionUnproven, false, ""},
	}
	for index, test := range cases {
		if index > 0 {
			seedNextRcloneFinalizationRun(t, fixture)
		}
		execution, err := fixture.service.Prepare(ctx, fixture.run())
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := execution.Attempt().RcloneAttempt()
		if err != nil {
			t.Fatal(err)
		}
		if test.reject {
			err = execution.Reject(ctx, test.code)
		} else {
			err = execution.Fail(ctx, test.code)
		}
		if err != nil {
			t.Fatal(err)
		}
		var point model.RecoveryPoint
		if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
			t.Fatal(err)
		}
		fact, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil || point.State != string(backupasset.RecoveryPointFailed) || fact.Provider != backupasset.ProviderRclone || fact.RcloneWriterQuiesced == nil || !*fact.RcloneWriterQuiesced || fact.Code != test.code || fact.Completion != test.completion {
			t.Fatalf("terminal state=%s facts=%+v err=%v", point.State, fact, err)
		}
		var active int64
		if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ? AND status = ?", point.ID, backupasset.LeaseActive).Count(&active).Error; err != nil || active != 0 {
			t.Fatalf("terminal publication retained live lease: %d %v", active, err)
		}
	}
	if err := fixture.service.CheckFailedRcloneWriters(ctx); err != nil {
		t.Fatalf("new terminal failures lack writer evidence: %v", err)
	}
	seedNextRcloneFinalizationRun(t, fixture)
	execution, err := fixture.service.Prepare(ctx, fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	deferFact := func(value *bool) backupasset.PublicationConsistencyV1 {
		t.Helper()
		if err := execution.Defer(ctx, publication.Deferral{Completion: backupasset.CompletionOutcomeUnknown, Code: backupasset.FailureProviderOutcomeUnknown, RcloneWriterQuiesced: value}); err != nil {
			t.Fatal(err)
		}
		var point model.RecoveryPoint
		if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
			t.Fatal(err)
		}
		fact, err := backupasset.DecodePublicationConsistency(point.ConsistencyJSON)
		if err != nil || point.State != string(backupasset.RecoveryPointPreparing) {
			t.Fatal("deferral changed point state")
		}
		return fact
	}
	first := deferFact(new(false))
	replayed := deferFact(new(false))
	if first.Provider != backupasset.ProviderRclone || first.RcloneWriterQuiesced == nil || *first.RcloneWriterQuiesced || replayed.PublicationRevision != first.PublicationRevision || replayed.AttemptCount != first.AttemptCount {
		t.Fatal("false writer fact was not persisted idempotently")
	}
	stopped := deferFact(new(true))
	if stopped.RcloneWriterQuiesced == nil || !*stopped.RcloneWriterQuiesced || stopped.PublicationRevision != first.PublicationRevision+1 {
		t.Fatal("changed writer fact did not advance evidence")
	}
	missing := deferFact(nil)
	if missing.RcloneWriterQuiesced != nil || missing.PublicationRevision != stopped.PublicationRevision+1 {
		t.Fatal("missing writer fact was not preserved distinctly")
	}
	if err := execution.Fail(ctx, backupasset.FailureProviderNonzeroExit); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("unknown writer was terminalized: %v", err)
	}
}

func seedNextRcloneFinalizationRun(t *testing.T, fixture *rclonePublicationFixture) {
	t.Helper()
	if err := fixture.db.Model(&model.TaskRun{}).Where("id = ?", fixture.taskRun.ID).Update("status", model.TaskRunStatusFailed).Error; err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{TaskID: fixture.task.ID, TriggerType: "manual", Status: model.TaskRunStatusRunning,
		StartedAt: new(fixture.now), CreatedAt: fixture.now, UpdatedAt: fixture.now}
	if err := fixture.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	fixture.taskRun = run
}
