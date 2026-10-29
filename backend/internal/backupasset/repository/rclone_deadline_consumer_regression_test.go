package repository

import (
	"context"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

// This test intentionally uses only pre-change public types, allowing the
// original candidate to be exercised with a Go overlay and unchanged sources.
func TestRcloneUnknownWriterRemainsProtectedAtDeadline(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
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
	var before model.RecoveryPoint
	if err := fixture.db.First(&before, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	setRcloneReconciliationTime(t, fixture, attempt.PointDeadlineAt)
	outcome, err := fixture.service.ProcessPoint(context.Background(), before.ID)
	if err != nil || outcome.State != backupasset.RecoveryPointPreparing || outcome.Code != backupasset.FailurePublicationDeadlineExceeded {
		t.Fatalf("unknown writer lost its deadline protection: outcome=%+v error=%v", outcome, err)
	}
	var after model.RecoveryPoint
	if err := fixture.db.First(&after, "id = ?", before.ID).Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.DecodePublicationConsistency(after.ConsistencyJSON)
	if err != nil || consistency.Completion != backupasset.CompletionOutcomeUnknown || after.State != before.State ||
		after.EncryptedProviderLocator != before.EncryptedProviderLocator || after.LineageJSON != before.LineageJSON {
		t.Fatal("deadline did not preserve the unknown writer and its immutable attempt")
	}
}
