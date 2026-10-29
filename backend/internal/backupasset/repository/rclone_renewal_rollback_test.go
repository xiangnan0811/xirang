package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func TestRcloneVersioningRenewalRejectsPreparedRollback(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	task, before := activatePortableForRenewal(t, fixture)
	revision, _ := managedRsyncTaskRevision(task)
	prepared, err := fixture.service.PrepareRcloneVersioningRollback(context.Background(), backupasset.RcloneVersioningRollbackPreparationRequest{TaskID: task.ID, ExpectedTaskRevision: revision, ExpectedBindingRevision: before.BindingRevision})
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, prepared.Summary, backupasset.RcloneStateRollbackPrepared, backupasset.RcloneReasonRollbackPrepared, backupasset.RcloneRollbackPrepared)
	// A prepared rollback is authoritative even if later history reads are unavailable.
	fixture.service.history = nil
	var current model.Task
	if err := fixture.db.First(&current, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, _ = managedRsyncTaskRevision(current)
	now := before.PreflightExpiresAt.Add(time.Second)
	fixture.service.now = func() time.Time { return now }
	_, err = fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("rollback-prepared renewal error=%v", err)
	}
	summary, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, summary, backupasset.RcloneStateRollbackPrepared, backupasset.RcloneReasonRollbackPrepared, backupasset.RcloneRollbackPrepared)
}
