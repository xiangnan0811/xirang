package export

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"
)

func TestDeliveryAuditProjectsOnlyClosedExportSummary(t *testing.T) {
	sink := &exportAuditSinkSpy{}
	audit, err := NewDeliveryAudit(sink)
	if err != nil {
		t.Fatal(err)
	}
	event := DeliveryAuditEvent{
		Actor:           backupasset.AuditActor{UserID: 7, Username: "admin", Role: "admin"},
		Action:          backupasset.AuditActionExportDownload,
		Outcome:         backupasset.AuditOutcomeFailure,
		ExportJobID:     strings.Repeat("a", 32),
		SelectionDigest: strings.Repeat("b", 64),
		ItemCount:       3,
		ByteCount:       4096,
		RangeCount:      1,
		RangeBytes:      128,
		ArchiveFormat:   "zip",
		ErrorCategory:   "delivery_failed",
	}
	if err := audit.Write(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("audit writes=%d", len(sink.inputs))
	}
	input := sink.inputs[0]
	if input.Actor != event.Actor || input.Action != event.Action || input.Outcome != event.Outcome ||
		input.ExportJobID != event.ExportJobID || input.ItemCount != event.ItemCount ||
		input.ByteCount != event.ByteCount || input.Range.Count != event.RangeCount ||
		input.Range.Bytes != event.RangeBytes || input.FailureCode != event.ErrorCategory {
		t.Fatalf("audit input=%+v", input)
	}
	if input.StepUpAction != "" || input.StepUpProofID != "" || input.GrantID != "" ||
		input.RepositoryID != "" || input.RecoveryPointID != "" || input.EntryID != "" ||
		input.Fingerprints.Path != "" || input.Fingerprints.Query != "" {
		t.Fatalf("audit carried forbidden identity=%+v", input)
	}
	wantFields := map[backupasset.AuditField]any{
		backupasset.AuditFieldSource: event.SelectionDigest,
		backupasset.AuditFieldFormat: event.ArchiveFormat,
	}
	if fmt.Sprint(input.Fields) != fmt.Sprint(wantFields) {
		t.Fatalf("audit fields=%v want=%v", input.Fields, wantFields)
	}
}

func TestDeliveryAuditProjectsOnlyClosedArchiveMemberSummary(t *testing.T) {
	sink := &exportAuditSinkSpy{}
	audit, err := NewDeliveryAudit(sink)
	if err != nil {
		t.Fatal(err)
	}
	event := DeliveryAuditEvent{
		Actor:           backupasset.AuditActor{UserID: 42, Username: "admin", Role: "admin"},
		Action:          backupasset.AuditActionArchiveMember,
		Outcome:         backupasset.AuditOutcomeSuccess,
		RecoveryPointID: strings.Repeat("1", 32),
		EntryID:         strings.Repeat("2", 64),
		SelectionDigest: strings.Repeat("3", 64),
		ItemCount:       1,
		ByteCount:       14,
		Mode:            "issue",
	}
	if err := audit.Write(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(sink.inputs) != 1 {
		t.Fatalf("audit writes=%d", len(sink.inputs))
	}
	input := sink.inputs[0]
	if input.Action != backupasset.AuditActionArchiveMember || input.RecoveryPointID != event.RecoveryPointID ||
		input.EntryID != event.EntryID || input.ExportJobID != "" || input.ItemCount != 1 || input.ByteCount != 14 {
		t.Fatalf("member audit input=%+v", input)
	}
	wantFields := map[backupasset.AuditField]any{
		backupasset.AuditFieldSource: event.SelectionDigest,
		backupasset.AuditFieldMode:   event.Mode,
	}
	if fmt.Sprint(input.Fields) != fmt.Sprint(wantFields) || input.StepUpAction != "" ||
		input.StepUpProofID != "" || input.GrantID != "" || input.Fingerprints.Path != "" || input.Fingerprints.Query != "" {
		t.Fatalf("member audit leaked private binding: %+v", input)
	}
}

func TestArchiveMemberDeliveryAuditRejectsEveryExportUnionField(t *testing.T) {
	now := time.Now().UTC()
	memberRequestID := strings.Repeat("1", 32)
	base := model.BackupAssetExportDeliveryGrant{
		ID: strings.Repeat("2", 32), ResourceKind: "archive_member", OwnerUserID: 42,
		MemberRequestID: &memberRequestID, OuterRecoveryPointID: strings.Repeat("3", 32),
		OuterEntryID: strings.Repeat("4", 64), MemberChainDigest: strings.Repeat("5", 64),
		Action: "archive_member_download", RangePolicy: string(content.RangeNone),
		AuditRequestCount: 1, AuditSuccessCount: 1,
	}
	requests := []model.BackupAssetExportDeliveryRequest{{
		GrantID: base.ID, State: string(DeliveryRequestSucceeded), PlaintextBytes: 14, FinishedAt: &now,
	}}
	exportID := strings.Repeat("6", 32)
	for _, testCase := range []struct {
		name   string
		mutate func(*model.BackupAssetExportDeliveryGrant)
	}{
		{name: "job", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.ExportJobID = &exportID }},
		{name: "artifact", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.ExportArtifactID = &exportID }},
		{name: "attempt", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.ExportAttemptID = &exportID }},
		{name: "fence", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.ExportFenceDigest = strings.Repeat("7", 64) }},
		{name: "selection", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.SelectionDigest = strings.Repeat("8", 64) }},
		{name: "artifact digest", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.ArtifactDigest = strings.Repeat("9", 64) }},
		{name: "plaintext size", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.PlaintextSize = 14 }},
		{name: "ciphertext size", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.CiphertextSize = 30 }},
		{name: "format version", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.FormatVersion = 1 }},
		{name: "chunk bytes", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.ChunkBytes = 64 << 10 }},
		{name: "job key", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.JobKeyID = &exportID }},
		{name: "job key version", mutate: func(grant *model.BackupAssetExportDeliveryGrant) { grant.JobKeyVersion = 1 }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			grant := base
			testCase.mutate(&grant)
			if _, err := archiveMemberDeliveryAuditEvent(grant, requests); !errors.Is(err, ErrDeliveryAudit) {
				t.Fatalf("contaminated member audit error=%v grant=%+v", err, grant)
			}
		})
	}
}

func TestDeliveryAuditRejectsRawOrUnboundedCategoriesBeforeSink(t *testing.T) {
	sink := &exportAuditSinkSpy{}
	audit, err := NewDeliveryAudit(sink)
	if err != nil {
		t.Fatal(err)
	}
	base := DeliveryAuditEvent{
		Actor:           backupasset.AuditActor{UserID: 7, Role: "admin"},
		Action:          backupasset.AuditActionExportDownloadTicket,
		Outcome:         backupasset.AuditOutcomeSuccess,
		ExportJobID:     strings.Repeat("a", 32),
		SelectionDigest: strings.Repeat("b", 64),
		ArchiveFormat:   "zip",
	}
	tests := []struct {
		name   string
		mutate func(*DeliveryAuditEvent)
	}{
		{name: "raw selection", mutate: func(event *DeliveryAuditEvent) { event.SelectionDigest = "FAKE_RAW_SELECTION_FOR_TEST_ONLY" }},
		{name: "raw error", mutate: func(event *DeliveryAuditEvent) { event.ErrorCategory = "/raw/provider/path" }},
		{name: "unknown error", mutate: func(event *DeliveryAuditEvent) { event.ErrorCategory = "FAKE_RAW_DIAGNOSTIC_FOR_TEST_ONLY" }},
		{name: "negative bytes", mutate: func(event *DeliveryAuditEvent) { event.ByteCount = -1 }},
		{name: "wrong action", mutate: func(event *DeliveryAuditEvent) { event.Action = backupasset.AuditActionExportCreate }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := base
			test.mutate(&event)
			if err := audit.Write(context.Background(), event); !errors.Is(err, ErrDeliveryAudit) {
				t.Fatalf("audit error=%v", err)
			}
		})
	}
	if len(sink.inputs) != 0 {
		t.Fatalf("invalid audit writes=%d", len(sink.inputs))
	}
}

type exportAuditSinkSpy struct {
	inputs     []backupasset.AuditEventInput
	err        error
	afterWrite func(backupasset.AuditEventInput)
}

func (sink *exportAuditSinkSpy) Write(_ context.Context, input backupasset.AuditEventInput) error {
	sink.inputs = append(sink.inputs, input)
	if sink.afterWrite != nil {
		sink.afterWrite(input)
	}
	return sink.err
}
