package export

import (
	"context"
	"errors"

	"xirang/backend/internal/backupasset"
)

var ErrDeliveryAudit = errors.New("export delivery audit unavailable")

type DeliveryAuditSink interface {
	Write(context.Context, backupasset.AuditEventInput) error
}

type DeliveryAuditor interface {
	Write(context.Context, DeliveryAuditEvent) error
}

type DeliveryAuditEvent struct {
	Actor           backupasset.AuditActor
	Action          backupasset.AuditAction
	Outcome         backupasset.AuditOutcome
	RecoveryPointID string
	EntryID         string
	ExportJobID     string
	SelectionDigest string
	ItemCount       int64
	ByteCount       int64
	RangeCount      int64
	RangeBytes      int64
	ArchiveFormat   string
	Mode            string
	ErrorCategory   string
}

type DeliveryAudit struct {
	sink DeliveryAuditSink
}

func NewDeliveryAudit(sink DeliveryAuditSink) (*DeliveryAudit, error) {
	if sink == nil {
		return nil, ErrDeliveryAudit
	}
	return &DeliveryAudit{sink: sink}, nil
}

func (audit *DeliveryAudit) Write(ctx context.Context, event DeliveryAuditEvent) error {
	if audit == nil || audit.sink == nil || !validDeliveryAuditEvent(event) {
		return ErrDeliveryAudit
	}
	fields := map[backupasset.AuditField]any{
		backupasset.AuditFieldSource: event.SelectionDigest,
	}
	if event.ArchiveFormat != "" {
		fields[backupasset.AuditFieldFormat] = event.ArchiveFormat
	}
	if event.Mode != "" {
		fields[backupasset.AuditFieldMode] = event.Mode
	}
	input := backupasset.AuditEventInput{
		Actor: event.Actor, Action: event.Action, Outcome: event.Outcome,
		RecoveryPointID: event.RecoveryPointID, EntryID: event.EntryID,
		ExportJobID: event.ExportJobID, ItemCount: event.ItemCount, ByteCount: event.ByteCount,
		Range:       backupasset.NewRangeSummary(event.RangeCount, event.RangeBytes),
		FailureCode: event.ErrorCategory,
		Fields:      fields,
	}
	prepared, err := backupasset.NewAuditEvent(input)
	if err != nil {
		return ErrDeliveryAudit
	}
	if err := audit.sink.Write(nonNilDeliveryContext(ctx), prepared.AuditEventInput); err != nil {
		return ErrDeliveryAudit
	}
	return nil
}

func validDeliveryAuditEvent(event DeliveryAuditEvent) bool {
	if event.Actor.UserID == 0 || event.Actor.Role != "admin" || !lowerHex(event.SelectionDigest, 64) ||
		event.ItemCount < 0 || event.ByteCount < 0 ||
		event.RangeCount < 0 || event.RangeBytes < 0 || event.RangeBytes > event.ByteCount ||
		(event.RecoveryPointID != "" || event.EntryID != "") != (event.RecoveryPointID != "" && event.EntryID != "") {
		return false
	}
	switch event.Action {
	case backupasset.AuditActionExportDownloadTicket, backupasset.AuditActionExportDownload:
		if backupasset.ValidateOpaqueID(event.ExportJobID) != nil || event.RecoveryPointID != "" || event.EntryID != "" ||
			(event.ArchiveFormat != string(ArchiveZIP) && event.ArchiveFormat != string(ArchiveTAR)) || event.Mode != "" {
			return false
		}
	case backupasset.AuditActionArchiveMember:
		if backupasset.ValidateAssetRef(backupasset.AssetRef{
			RecoveryPointID: event.RecoveryPointID, EntryID: event.EntryID,
		}) != nil || event.ExportJobID != "" || event.ArchiveFormat != "" || event.ItemCount != 1 ||
			(event.Mode != "issue" && event.Mode != "read") || event.RangeCount != 0 || event.RangeBytes != 0 {
			return false
		}
	default:
		return false
	}
	if event.Outcome == backupasset.AuditOutcomeSuccess {
		return event.ErrorCategory == ""
	}
	if event.Outcome != backupasset.AuditOutcomeFailure && event.Outcome != backupasset.AuditOutcomeBlocked {
		return false
	}
	return validDeliveryAuditErrorCategories[event.ErrorCategory]
}

var validDeliveryAuditErrorCategories = map[string]bool{
	"invalid_range":     true,
	"request_too_large": true,
	"budget_exhausted":  true,
	"client_canceled":   true,
	"delivery_failed":   true,
	"reconciled_crash":  true,
	"session_revoked":   true,
	"artifact_changed":  true,
	"unavailable":       true,
}
