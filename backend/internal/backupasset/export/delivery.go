package export

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidDeliveryRequest   = errors.New("invalid export delivery request")
	ErrDeliveryReplay           = errors.New("export delivery request replay conflict")
	ErrDeliveryBudgetExceeded   = errors.New("export delivery budget exceeded")
	ErrDeliveryState            = errors.New("invalid export delivery state")
	errDeliveryBudgetConcurrent = errors.New("concurrent export delivery budget update")
)

const maxDeliveryAuditAttempts int64 = 8

type DeliveryRequestState string

const (
	DeliveryRequestReserved   DeliveryRequestState = "reserved"
	DeliveryRequestStreaming  DeliveryRequestState = "streaming"
	DeliveryRequestSucceeded  DeliveryRequestState = "succeeded"
	DeliveryRequestBlocked    DeliveryRequestState = "blocked"
	DeliveryRequestCanceled   DeliveryRequestState = "canceled"
	DeliveryRequestFailed     DeliveryRequestState = "failed"
	DeliveryRequestReconciled DeliveryRequestState = "reconciled"
)

type DeliveryReservationIntent struct {
	RequestID     string
	GrantID       string
	Method        string
	Range         content.HTTPRange
	ReservedBytes int64
}

type DeliveryReservation struct {
	RequestID       string
	GrantID         string
	ReservedBytes   int64
	AlreadyReserved bool
}

type DeliveryBlockedIntent struct {
	RequestID      string
	GrantID        string
	Method         string
	RangeRequested bool
	FailureCode    content.RequestFailureCode
}

type DeliveryFinalizeIntent struct {
	RequestID       string
	State           DeliveryRequestState
	EvidenceKnown   bool
	PlaintextBytes  int64
	CiphertextBytes int64
	FailureCode     string
}

type DeliveryFinalization struct {
	RequestID        string
	State            DeliveryRequestState
	ChargedBytes     int64
	AlreadyFinalized bool
}

type deliveryBudget struct {
	db  *gorm.DB
	now func() time.Time
}

type DeliveryGatewayConfig struct {
	TicketTTL          time.Duration
	MaxRequests        int64
	MaxCumulativeBytes int64
	MaxInFlight        int64
}

type DeliveryGatewayDependencies struct {
	DB                     *gorm.DB
	Now                    func() time.Time
	Session                content.DeliverySessionValidator
	Store                  *Store
	Keys                   DeliveryKeySource
	ArchiveMembers         ArchiveMemberDeliverySource
	ArchiveMemberAuthorize content.AssetAuthorizer
	Audit                  DeliveryAuditor
	TicketMaterial         func() (content.TicketMaterial, error)
	RequestID              func() (string, error)
	Config                 DeliveryGatewayConfig
}

type DeliveryKeySource interface {
	ByVersion(context.Context, backupasset.KeyDomain, int) (backupasset.DomainKeyMaterial, error)
}

type ArchiveMemberDeliverySource interface {
	ResolveArchiveMember(
		context.Context,
		content.ArchiveMemberArtifactRequest,
	) (content.ResolvedArchiveMemberArtifact, error)
	ReadArchiveMember(context.Context, content.ResolvedArchiveMemberArtifact, io.Writer) error
}

type DeliveryGateway struct {
	db                     *gorm.DB
	now                    func() time.Time
	session                content.DeliverySessionValidator
	store                  *Store
	keys                   DeliveryKeySource
	archiveMembers         ArchiveMemberDeliverySource
	archiveMemberAuthorize content.AssetAuthorizer
	audit                  DeliveryAuditor
	ticketMaterial         func() (content.TicketMaterial, error)
	requestID              func() (string, error)
	config                 DeliveryGatewayConfig
	budget                 *deliveryBudget

	mu    sync.Mutex
	reads map[string]map[string]activeExportDeliveryRead
}

type activeExportDeliveryRead struct {
	sessionJTI string
	cancel     context.CancelFunc
	done       chan struct{}
}

type ExportDeliveryIssueRequest struct {
	Actor        content.DeliveryActor
	Session      content.DeliverySession
	ExportJobID  string
	Proof        content.StepUpProof
	SecureCookie bool
}

type ArchiveMemberDeliveryIssueRequest struct {
	Actor           content.DeliveryActor
	Session         content.DeliverySession
	Asset           content.AuthorizedAsset
	MemberRequestID string
	Proof           content.StepUpProof
	SecureCookie    bool
}

type DeliveryTicketDescriptor struct {
	SchemaVersion int                 `json:"schema_version"`
	ContentURL    string              `json:"content_url"`
	ContentType   string              `json:"content_type"`
	ContentLength int64               `json:"content_length"`
	ETag          string              `json:"etag"`
	Range         content.RangePolicy `json:"range"`
	ExpiresAt     time.Time           `json:"expires_at"`
	IdleExpiresAt time.Time           `json:"idle_expires_at"`
}

type IssuedDeliveryTicket struct {
	Descriptor DeliveryTicketDescriptor `json:"-"`
	Cookie     *http.Cookie             `json:"-"`
}

func NewDeliveryGateway(dependencies DeliveryGatewayDependencies) (*DeliveryGateway, error) {
	if dependencies.DB == nil || dependencies.Now == nil || dependencies.Session == nil ||
		dependencies.Store == nil || dependencies.Keys == nil || dependencies.Audit == nil ||
		!validDeliveryGatewayConfig(dependencies.Config) {
		return nil, ErrInvalidDeliveryRequest
	}
	if dependencies.TicketMaterial == nil {
		dependencies.TicketMaterial = content.NewTicketMaterial
	}
	if dependencies.RequestID == nil {
		dependencies.RequestID = backupasset.NewOpaqueID
	}
	budget, err := newDeliveryBudget(dependencies.DB, dependencies.Now)
	if err != nil {
		return nil, err
	}
	return &DeliveryGateway{
		db: dependencies.DB, now: dependencies.Now, session: dependencies.Session,
		store: dependencies.Store, keys: dependencies.Keys, archiveMembers: dependencies.ArchiveMembers,
		archiveMemberAuthorize: dependencies.ArchiveMemberAuthorize,
		audit:                  dependencies.Audit,
		ticketMaterial:         dependencies.TicketMaterial, requestID: dependencies.RequestID,
		config: dependencies.Config, budget: budget,
		reads: make(map[string]map[string]activeExportDeliveryRead),
	}, nil
}

func (gateway *DeliveryGateway) IssueArchiveMember(
	ctx context.Context,
	request ArchiveMemberDeliveryIssueRequest,
) (IssuedDeliveryTicket, error) {
	if gateway == nil || gateway.archiveMembers == nil || gateway.archiveMemberAuthorize == nil ||
		!validArchiveMemberDeliveryIssueRequest(request, gateway.now().UTC()) {
		return IssuedDeliveryTicket{}, ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	if err := gateway.session.Validate(ctx, request.Session); err != nil {
		return IssuedDeliveryTicket{}, ErrNotFound
	}
	authorized, err := gateway.archiveMemberAuthorize.Authorize(
		ctx, request.Actor, request.Asset.Ref, content.DeliveryDownload,
	)
	if err != nil || !sameArchiveMemberDeliveryAsset(authorized, request.Asset) {
		return IssuedDeliveryTicket{}, ErrNotFound
	}
	binding, err := gateway.archiveMembers.ResolveArchiveMember(ctx, content.ArchiveMemberArtifactRequest{
		RequestID: request.MemberRequestID, OwnerUserID: request.Actor.UserID, Asset: request.Asset,
	})
	if err != nil || !validArchiveMemberDeliveryBinding(request, binding, gateway.now().UTC()) {
		return IssuedDeliveryTicket{}, ErrNotFound
	}
	material, err := gateway.ticketMaterial()
	if err != nil || !validExportTicketMaterial(material) {
		return IssuedDeliveryTicket{}, ErrUnavailable
	}
	now := gateway.now().UTC()
	var descriptor DeliveryTicketDescriptor
	var auditEvent DeliveryAuditEvent
	err = gateway.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if collision, collisionErr := deliveryIdentifierCollisionTx(tx, material.DeliveryID); collisionErr != nil {
			return collisionErr
		} else if collision {
			return ErrConflict
		}
		expiresAt := minimumDeliveryExpiry(
			now.Add(gateway.config.TicketTTL), request.Session.ExpiresAt,
			request.Proof.ExpiresAt, binding.AbsoluteExpiresAt,
		)
		if !expiresAt.After(now) {
			return ErrNotFound
		}
		cookie, cookieErr := content.NewDeliveryCookie(
			material.DeliveryID, material.CookieSecret, expiresAt, request.SecureCookie,
		)
		if cookieErr != nil {
			return cookieErr
		}
		memberRequestID := binding.MemberRequestID
		processingJobID := binding.ProcessingJobID
		processingAttemptID := binding.ProcessingAttemptID
		artifactSetID := binding.DerivedArtifactSetID
		artifactID := binding.DerivedArtifactID
		blobID := binding.DerivedBlobID
		grant := model.BackupAssetExportDeliveryGrant{
			ID: material.GrantID, DeliveryID: material.DeliveryID, ResourceKind: "archive_member",
			MemberRequestID: &memberRequestID, OuterRecoveryPointID: binding.Ref.RecoveryPointID,
			OuterEntryID: binding.Ref.EntryID, OuterSourceFingerprint: binding.SourceFingerprint,
			OuterEntryFingerprint: binding.EntryFingerprint, MemberChainDigest: binding.MemberChainDigest,
			ProcessingJobID: &processingJobID, ProcessingAttemptID: &processingAttemptID,
			DerivedArtifactSetID: &artifactSetID, DerivedArtifactID: &artifactID, DerivedBlobID: &blobID,
			DerivedDigest: binding.DerivedDigest, DerivedSize: binding.DerivedSize,
			OwnerUserID: request.Actor.UserID, SessionJTI: request.Session.JTI,
			TokenVersion: int64(request.Session.TokenVersion), RoleRevision: int64(request.Session.TokenVersion),
			ProofAction: string(request.Proof.Action), ProofID: request.Proof.ID,
			ProofExpiresAt: request.Proof.ExpiresAt.UTC(), CookieSecretHash: material.CookieSecretHash,
			Action: "archive_member_download", CanonicalPath: cookie.Path,
			MethodPolicy: string(content.MethodGetHead), RangePolicy: string(content.RangeNone), State: "issued",
			IdleExpiresAt: expiresAt, AbsoluteExpiresAt: expiresAt,
			MaxRequests: gateway.config.MaxRequests, MaxCumulativeBytes: gateway.config.MaxCumulativeBytes,
			MaxInFlight: gateway.config.MaxInFlight, AuditState: "none",
			IssuedAt: now, CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		if err := tx.Create(&grant).Error; err != nil {
			return err
		}
		descriptor = DeliveryTicketDescriptor{
			SchemaVersion: 1, ContentURL: cookie.Path, ContentType: binding.MediaType,
			ContentLength: binding.DerivedSize, ETag: `"` + binding.DerivedDigest + `"`, Range: content.RangeNone,
			ExpiresAt: expiresAt, IdleExpiresAt: expiresAt,
		}
		auditEvent = DeliveryAuditEvent{
			Actor: backupasset.AuditActor{
				UserID: request.Actor.UserID, Username: request.Actor.Username, Role: request.Actor.Role,
			},
			Action: backupasset.AuditActionArchiveMember, Outcome: backupasset.AuditOutcomeSuccess,
			RecoveryPointID: binding.Ref.RecoveryPointID, EntryID: binding.Ref.EntryID,
			SelectionDigest: binding.MemberChainDigest, ItemCount: 1, ByteCount: binding.DerivedSize, Mode: "issue",
		}
		return nil
	})
	if err != nil {
		return IssuedDeliveryTicket{}, err
	}
	if err := gateway.audit.Write(ctx, auditEvent); err != nil {
		return IssuedDeliveryTicket{}, errors.Join(
			err,
			gateway.revokeIssuedDelivery(ctx, material.GrantID, "audit_failed", now),
		)
	}
	activation := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id = ? AND state = ? AND version = ?", material.GrantID, "issued", 1).
		Updates(map[string]any{"state": "active", "updated_at": now, "version": 2})
	if activation.Error != nil || activation.RowsAffected != 1 {
		return IssuedDeliveryTicket{}, errors.Join(
			ErrUnavailable,
			gateway.revokeIssuedDelivery(ctx, material.GrantID, "request_failed", now),
		)
	}
	cookie, err := content.NewDeliveryCookie(
		material.DeliveryID, material.CookieSecret, descriptor.ExpiresAt, request.SecureCookie,
	)
	if err != nil {
		return IssuedDeliveryTicket{}, ErrUnavailable
	}
	return IssuedDeliveryTicket{Descriptor: descriptor, Cookie: cookie}, nil
}

func (gateway *DeliveryGateway) IssueExport(
	ctx context.Context,
	request ExportDeliveryIssueRequest,
) (IssuedDeliveryTicket, error) {
	if gateway == nil || !validExportDeliveryIssueRequest(request, gateway.now().UTC()) {
		return IssuedDeliveryTicket{}, ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	if err := gateway.session.Validate(ctx, request.Session); err != nil {
		return IssuedDeliveryTicket{}, ErrNotFound
	}
	material, err := gateway.ticketMaterial()
	if err != nil || !validExportTicketMaterial(material) {
		return IssuedDeliveryTicket{}, ErrUnavailable
	}
	now := gateway.now().UTC()
	var descriptor DeliveryTicketDescriptor
	var auditEvent DeliveryAuditEvent
	err = gateway.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if collision, collisionErr := deliveryIdentifierCollisionTx(tx, material.DeliveryID); collisionErr != nil {
			return collisionErr
		} else if collision {
			return ErrConflict
		}

		var job model.BackupAssetExportJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND owner_user_id = ?", request.ExportJobID, request.Actor.UserID).
			Limit(1).Find(&job)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || !validReadyDeliveryJob(job, now) {
			return ErrNotFound
		}

		var artifacts []model.BackupAssetExportArtifact
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("job_id = ? AND state = ? AND purged_at IS NULL", job.ID, "sealed").
			Order("id ASC").Limit(2).Find(&artifacts)
		if result.Error != nil {
			return result.Error
		}
		if len(artifacts) != 1 {
			return ErrNotFound
		}
		artifact := artifacts[0]

		var attempt model.BackupAssetExportAttempt
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ?", artifact.AttemptID, job.ID).Limit(1).Find(&attempt)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || !validReadyDeliveryArtifact(job, attempt, artifact, now) {
			return ErrNotFound
		}

		var key model.BackupAssetExportKey
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND job_id = ? AND state = ?", artifact.JobKeyID, job.ID, "active").
			Limit(1).Find(&key)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || key.KeyRevision <= 0 || key.KeyRevision > math.MaxInt ||
			key.KEKVersion <= 0 || len(key.WrappedDEK) == 0 || len(key.EnvelopeNonce) == 0 {
			return ErrNotFound
		}
		keyMaterial, keyErr := gateway.keys.ByVersion(
			ctx, backupasset.KeyDomainExportStore, key.KEKVersion,
		)
		defer clear(keyMaterial.Key)
		if keyErr != nil || keyMaterial.Domain != backupasset.KeyDomainExportStore ||
			keyMaterial.Version != key.KEKVersion ||
			(keyMaterial.State != backupasset.DomainKeyActive &&
				keyMaterial.State != backupasset.DomainKeyVerifyOnly) ||
			len(keyMaterial.Key) != 32 {
			return ErrNotFound
		}
		dek, unwrapErr := UnwrapJobDEK(JobKeyBinding{
			ExportID: job.ID, SelectionDigest: job.SelectionDigest,
			KEKVersion: key.KEKVersion, WrapAlgorithm: key.WrapAlgorithm,
		}, keyMaterial.Key, JobKeyEnvelope{
			Nonce: key.EnvelopeNonce, Ciphertext: key.WrappedDEK,
		})
		defer clear(dek)
		if unwrapErr != nil {
			return ErrNotFound
		}

		expiresAt := minimumDeliveryExpiry(
			now.Add(gateway.config.TicketTTL), request.Session.ExpiresAt,
			request.Proof.ExpiresAt, *job.ExpiresAt, *artifact.ExpiresAt,
		)
		if !expiresAt.After(now) {
			return ErrNotFound
		}
		cookie, cookieErr := content.NewDeliveryCookie(
			material.DeliveryID, material.CookieSecret, expiresAt, request.SecureCookie,
		)
		if cookieErr != nil {
			return cookieErr
		}
		artifactDigest := exportDeliveryBindingDigest(job, attempt, artifact, key)
		grant := model.BackupAssetExportDeliveryGrant{
			ID: material.GrantID, DeliveryID: material.DeliveryID, ResourceKind: "export_archive",
			ExportJobID: &job.ID, ExportArtifactID: &artifact.ID, ExportAttemptID: &attempt.ID,
			ExportFenceDigest: attempt.FenceDigest, SelectionDigest: job.SelectionDigest,
			ArtifactDigest: artifactDigest, PlaintextSize: artifact.PlaintextSize,
			CiphertextSize: artifact.CiphertextSize, FormatVersion: artifact.FormatVersion,
			ChunkBytes: artifact.ChunkBytes, JobKeyID: &key.ID, JobKeyVersion: int(key.KeyRevision),
			OwnerUserID: request.Actor.UserID, SessionJTI: request.Session.JTI,
			TokenVersion: int64(request.Session.TokenVersion), RoleRevision: int64(request.Session.TokenVersion),
			ProofAction: string(request.Proof.Action), ProofID: request.Proof.ID,
			ProofExpiresAt: request.Proof.ExpiresAt.UTC(), CookieSecretHash: material.CookieSecretHash,
			Action: "export_download", CanonicalPath: cookie.Path,
			MethodPolicy: string(content.MethodGetHead), RangePolicy: string(content.RangeSingle), State: "issued",
			IdleExpiresAt: expiresAt, AbsoluteExpiresAt: expiresAt,
			MaxRequests: gateway.config.MaxRequests, MaxCumulativeBytes: gateway.config.MaxCumulativeBytes,
			MaxInFlight: gateway.config.MaxInFlight, AuditState: "none",
			IssuedAt: now, CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		if err := tx.Create(&grant).Error; err != nil {
			return err
		}
		descriptor = DeliveryTicketDescriptor{
			SchemaVersion: 1, ContentURL: cookie.Path,
			ContentType:   exportDeliveryContentType(job.ArchiveFormat, job.ArchiveProfile),
			ContentLength: artifact.PlaintextSize, ETag: `"` + artifactDigest + `"`, Range: content.RangeSingle,
			ExpiresAt: expiresAt, IdleExpiresAt: expiresAt,
		}
		auditEvent = DeliveryAuditEvent{
			Actor: backupasset.AuditActor{
				UserID: request.Actor.UserID, Username: request.Actor.Username, Role: request.Actor.Role,
			},
			Action: backupasset.AuditActionExportDownloadTicket, Outcome: backupasset.AuditOutcomeSuccess,
			ExportJobID: job.ID, SelectionDigest: job.SelectionDigest, ItemCount: job.ItemCount,
			ByteCount: artifact.CiphertextSize, ArchiveFormat: job.ArchiveFormat,
		}
		return nil
	})
	if err != nil {
		return IssuedDeliveryTicket{}, err
	}
	if err := gateway.audit.Write(ctx, auditEvent); err != nil {
		return IssuedDeliveryTicket{}, errors.Join(
			err,
			gateway.revokeIssuedDelivery(ctx, material.GrantID, "audit_failed", now),
		)
	}
	activation := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id = ? AND state = ? AND version = ?", material.GrantID, "issued", 1).
		Updates(map[string]any{"state": "active", "updated_at": now, "version": 2})
	if activation.Error != nil || activation.RowsAffected != 1 {
		return IssuedDeliveryTicket{}, errors.Join(
			ErrUnavailable,
			gateway.revokeIssuedDelivery(ctx, material.GrantID, "request_failed", now),
		)
	}
	cookie, err := content.NewDeliveryCookie(
		material.DeliveryID, material.CookieSecret, descriptor.ExpiresAt, request.SecureCookie,
	)
	if err != nil {
		return IssuedDeliveryTicket{}, ErrUnavailable
	}
	return IssuedDeliveryTicket{Descriptor: descriptor, Cookie: cookie}, nil
}

func (gateway *DeliveryGateway) revokeIssuedDelivery(
	ctx context.Context,
	grantID string,
	reason string,
	now time.Time,
) error {
	if gateway == nil || gateway.db == nil || backupasset.ValidateOpaqueID(grantID) != nil ||
		(reason != "audit_failed" && reason != "request_failed") {
		return ErrUnavailable
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(nonNilDeliveryContext(ctx)), 5*time.Second)
	defer cancel()
	result := gateway.db.WithContext(cleanupCtx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id = ? AND state IN ?", grantID, []string{"issued", "active"}).
		Updates(map[string]any{
			"state": "revoked", "revoke_reason": reason, "revoked_at": now,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		return ErrUnavailable
	}
	return nil
}

type exportDeliveryBinding struct {
	grant    model.BackupAssetExportDeliveryGrant
	job      model.BackupAssetExportJob
	attempt  model.BackupAssetExportAttempt
	artifact model.BackupAssetExportArtifact
	key      model.BackupAssetExportKey
}

type archiveMemberDeliveryBinding struct {
	grant  model.BackupAssetExportDeliveryGrant
	asset  content.AuthorizedAsset
	member content.ResolvedArchiveMemberArtifact
}

func (gateway *DeliveryGateway) MatchesDelivery(ctx context.Context, deliveryID string) (bool, error) {
	if gateway == nil || gateway.db == nil || backupasset.ValidateOpaqueID(deliveryID) != nil {
		return false, nil
	}
	var count int64
	err := gateway.db.WithContext(nonNilDeliveryContext(ctx)).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("delivery_id = ?", deliveryID).Limit(1).Count(&count).Error
	return count == 1, err
}

func (gateway *DeliveryGateway) RevokeSession(ctx context.Context, sessionJTI, reason string) error {
	if gateway == nil || gateway.db == nil || backupasset.ValidateOpaqueID(sessionJTI) != nil ||
		(reason != "logout" && reason != "session_revoked") {
		return ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	now := gateway.now().UTC()
	gateway.mu.Lock()
	result := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("session_jti = ? AND state IN ?", sessionJTI, []string{"issued", "active"}).
		Updates(map[string]any{
			"state": "draining", "revoke_reason": reason,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		gateway.mu.Unlock()
		return result.Error
	}
	waits := make([]<-chan struct{}, 0)
	for _, byRequest := range gateway.reads {
		for _, read := range byRequest {
			if read.sessionJTI == sessionJTI {
				read.cancel()
				waits = append(waits, read.done)
			}
		}
	}
	gateway.mu.Unlock()
	for _, done := range waits {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	result = gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("session_jti = ? AND state IN ?", sessionJTI, []string{"issued", "active", "draining"}).
		Updates(map[string]any{
			"state": "revoked", "revoke_reason": reason, "revoked_at": now,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		return result.Error
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := gateway.flushDeliveryAudits(auditCtx, sessionJTI); err != nil {
		return ErrDeliveryAudit
	}
	return nil
}

func (gateway *DeliveryGateway) BeginRevokeExportJob(ctx context.Context, jobID, reason string) error {
	if gateway == nil || gateway.db == nil || backupasset.ValidateOpaqueID(jobID) != nil ||
		!validExportJobDeliveryRevokeReason(reason) {
		return ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	now := gateway.now().UTC()
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	var grantIDs []string
	if err := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("resource_kind = ? AND export_job_id = ?", "export_archive", jobID).
		Order("id ASC").Pluck("id", &grantIDs).Error; err != nil {
		return err
	}
	result := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id IN ? AND state IN ?", grantIDs, []string{"issued", "active"}).
		Updates(map[string]any{
			"state": "draining", "revoke_reason": reason,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		return result.Error
	}
	for _, grantID := range grantIDs {
		for _, read := range gateway.reads[grantID] {
			read.cancel()
		}
	}
	return nil
}

func (gateway *DeliveryGateway) DrainExportJob(ctx context.Context, jobID string) error {
	if gateway == nil || gateway.db == nil || backupasset.ValidateOpaqueID(jobID) != nil {
		return ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	gateway.mu.Lock()
	var grantIDs []string
	if err := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("resource_kind = ? AND export_job_id = ?", "export_archive", jobID).
		Order("id ASC").Pluck("id", &grantIDs).Error; err != nil {
		gateway.mu.Unlock()
		return err
	}
	waits := make([]<-chan struct{}, 0)
	for _, grantID := range grantIDs {
		for _, read := range gateway.reads[grantID] {
			read.cancel()
			waits = append(waits, read.done)
		}
	}
	gateway.mu.Unlock()
	for _, done := range waits {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	now := gateway.now().UTC()
	result := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id IN ? AND state = ?", grantIDs, "draining").
		Updates(map[string]any{
			"state": "revoked", "revoked_at": now,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		return result.Error
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var auditErrors []error
	for _, grantID := range grantIDs {
		if err := gateway.flushDeliveryAudit(auditCtx, grantID); err != nil {
			auditErrors = append(auditErrors, ErrDeliveryAudit)
		}
	}
	return errors.Join(auditErrors...)
}

func validExportJobDeliveryRevokeReason(reason string) bool {
	switch reason {
	case "job_canceled", "job_failed", "source_expired", "artifact_expired", "key_loss", "artifact_changed", "feature_disabled", "runtime_shutdown":
		return true
	default:
		return false
	}
}

func (gateway *DeliveryGateway) RevokeArchiveMember(
	ctx context.Context,
	memberRequestID string,
	reason string,
) error {
	if gateway == nil || gateway.db == nil || backupasset.ValidateOpaqueID(memberRequestID) != nil ||
		!validArchiveMemberDeliveryRevokeReason(reason) {
		return ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	now := gateway.now().UTC()
	gateway.mu.Lock()
	var grantIDs []string
	query := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("resource_kind = ? AND member_request_id = ? AND state IN ?", "archive_member", memberRequestID,
			[]string{"issued", "active", "draining", "revoked"}).
		Order("id ASC").Pluck("id", &grantIDs)
	if query.Error != nil {
		gateway.mu.Unlock()
		return query.Error
	}
	result := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id IN ? AND state IN ?", grantIDs, []string{"issued", "active"}).
		Updates(map[string]any{
			"state": "draining", "revoke_reason": reason,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		gateway.mu.Unlock()
		return result.Error
	}
	waits := make([]<-chan struct{}, 0)
	for _, grantID := range grantIDs {
		for _, read := range gateway.reads[grantID] {
			read.cancel()
			waits = append(waits, read.done)
		}
	}
	gateway.mu.Unlock()
	for _, done := range waits {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	result = gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id IN ? AND state IN ?", grantIDs, []string{"issued", "active", "draining"}).
		Updates(map[string]any{
			"state": "revoked", "revoke_reason": reason, "revoked_at": now,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		return result.Error
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var auditErrors []error
	for _, grantID := range grantIDs {
		if err := gateway.flushDeliveryAudit(auditCtx, grantID); err != nil {
			auditErrors = append(auditErrors, ErrDeliveryAudit)
		}
	}
	return errors.Join(auditErrors...)
}

func validArchiveMemberDeliveryRevokeReason(reason string) bool {
	switch reason {
	case "member_canceled", "member_expired", "source_changed", "policy_changed", "key_loss", "artifact_changed":
		return true
	default:
		return false
	}
}

func (gateway *DeliveryGateway) flushDeliveryAudits(ctx context.Context, sessionJTI string) error {
	if gateway == nil || gateway.db == nil || gateway.audit == nil ||
		(sessionJTI != "" && backupasset.ValidateOpaqueID(sessionJTI) != nil) {
		return ErrDeliveryAudit
	}
	ctx = nonNilDeliveryContext(ctx)
	now := gateway.now().UTC()
	query := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("state IN ? AND in_flight = 0", []string{"revoked", "expired", "closed"}).
		Where("audit_state = ? OR (audit_state IN ? AND (audit_next_attempt_at IS NULL OR audit_next_attempt_at <= ?))",
			"pending", []string{"retry_wait", "failed"}, now)
	if sessionJTI != "" {
		query = query.Where("session_jti = ?", sessionJTI)
	}
	var grantIDs []string
	if err := query.Order("id ASC").Pluck("id", &grantIDs).Error; err != nil {
		return ErrDeliveryAudit
	}
	var flushErrors []error
	for _, grantID := range grantIDs {
		if err := gateway.flushDeliveryAudit(ctx, grantID); err != nil {
			flushErrors = append(flushErrors, ErrDeliveryAudit)
		}
	}
	return errors.Join(flushErrors...)
}

func (gateway *DeliveryGateway) flushDeliveryAudit(ctx context.Context, grantID string) error {
	var grant model.BackupAssetExportDeliveryGrant
	if err := gateway.db.WithContext(ctx).Where("id = ?", grantID).Take(&grant).Error; err != nil {
		return ErrDeliveryAudit
	}
	if grant.AuditState == "none" || grant.AuditState == "emitted" {
		return nil
	}
	if grant.InFlight != 0 ||
		(grant.State != "revoked" && grant.State != "expired" && grant.State != "closed") {
		return gateway.queueDeliveryAuditRetry(ctx, grant, "reconciliation_failed")
	}
	var requests []model.BackupAssetExportDeliveryRequest
	if err := gateway.db.WithContext(ctx).Where("grant_id = ?", grant.ID).
		Order("started_at ASC, id ASC").Find(&requests).Error; err != nil {
		return errors.Join(ErrDeliveryAudit, gateway.queueDeliveryAuditRetry(ctx, grant, "reconciliation_failed"))
	}
	var event DeliveryAuditEvent
	var err error
	switch grant.ResourceKind {
	case "export_archive":
		if grant.ExportJobID == nil {
			return gateway.queueDeliveryAuditRetry(ctx, grant, "reconciliation_failed")
		}
		var job model.BackupAssetExportJob
		if loadErr := gateway.db.WithContext(ctx).Where("id = ?", *grant.ExportJobID).Take(&job).Error; loadErr != nil {
			return errors.Join(ErrDeliveryAudit, gateway.queueDeliveryAuditRetry(ctx, grant, "reconciliation_failed"))
		}
		event, err = deliveryAuditEvent(grant, job, requests)
	case "archive_member":
		event, err = archiveMemberDeliveryAuditEvent(grant, requests)
	default:
		err = ErrDeliveryAudit
	}
	if err != nil {
		return errors.Join(ErrDeliveryAudit, gateway.queueDeliveryAuditRetry(ctx, grant, "reconciliation_failed"))
	}
	if err := gateway.audit.Write(ctx, event); err != nil {
		return errors.Join(ErrDeliveryAudit, gateway.queueDeliveryAuditRetry(ctx, grant, "audit_write_failed"))
	}
	result := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id = ? AND version = ? AND audit_state IN ?", grant.ID, grant.Version, []string{"pending", "retry_wait", "failed"}).
		Updates(map[string]any{
			"audit_state": "emitted", "audit_failure_code": "", "audit_next_attempt_at": nil,
			"updated_at": gateway.now().UTC(), "version": grant.Version + 1,
		})
	if result.Error != nil || result.RowsAffected != 1 {
		return ErrDeliveryAudit
	}
	return nil
}

func (gateway *DeliveryGateway) queueDeliveryAuditRetry(
	ctx context.Context,
	grant model.BackupAssetExportDeliveryGrant,
	failureCode string,
) error {
	if failureCode != "audit_write_failed" && failureCode != "reconciliation_failed" {
		return ErrDeliveryAudit
	}
	attempt := grant.AuditAttemptCount + 1
	shift := min(attempt-1, int64(6))
	next := gateway.now().UTC().Add(time.Duration(1<<shift) * time.Second)
	state := "retry_wait"
	if attempt >= maxDeliveryAuditAttempts {
		state = "failed"
	}
	result := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("id = ? AND version = ? AND audit_state = ?", grant.ID, grant.Version, grant.AuditState).
		Updates(map[string]any{
			"audit_state": state, "audit_failure_code": failureCode,
			"audit_attempt_count": attempt, "audit_next_attempt_at": next,
			"updated_at": gateway.now().UTC(), "version": grant.Version + 1,
		})
	if result.Error != nil || result.RowsAffected != 1 {
		return ErrDeliveryAudit
	}
	return ErrDeliveryAudit
}

func deliveryAuditEvent(
	grant model.BackupAssetExportDeliveryGrant,
	job model.BackupAssetExportJob,
	requests []model.BackupAssetExportDeliveryRequest,
) (DeliveryAuditEvent, error) {
	if grant.ResourceKind != "export_archive" || grant.ExportJobID == nil || *grant.ExportJobID != job.ID ||
		grant.OwnerUserID == 0 {
		return DeliveryAuditEvent{}, ErrDeliveryAudit
	}
	summary, err := summarizeDeliveryAudit(grant, requests)
	if err != nil {
		return DeliveryAuditEvent{}, err
	}
	return DeliveryAuditEvent{
		Actor:  backupasset.AuditActor{UserID: grant.OwnerUserID, Role: "admin"},
		Action: backupasset.AuditActionExportDownload, Outcome: summary.outcome,
		ExportJobID: job.ID, SelectionDigest: job.SelectionDigest, ItemCount: job.ItemCount,
		ByteCount: summary.byteCount, RangeCount: grant.AuditRangeCount, RangeBytes: grant.AuditRangeBytes,
		ArchiveFormat: job.ArchiveFormat, ErrorCategory: summary.errorCategory,
	}, nil
}

func archiveMemberDeliveryAuditEvent(
	grant model.BackupAssetExportDeliveryGrant,
	requests []model.BackupAssetExportDeliveryRequest,
) (DeliveryAuditEvent, error) {
	ref := backupasset.AssetRef{RecoveryPointID: grant.OuterRecoveryPointID, EntryID: grant.OuterEntryID}
	if grant.ResourceKind != "archive_member" || grant.OwnerUserID == 0 || grant.MemberRequestID == nil ||
		backupasset.ValidateOpaqueID(*grant.MemberRequestID) != nil || backupasset.ValidateAssetRef(ref) != nil ||
		!lowerHex(grant.MemberChainDigest, 64) || grant.ExportJobID != nil || grant.ExportArtifactID != nil ||
		grant.ExportAttemptID != nil || grant.ExportFenceDigest != "" || grant.SelectionDigest != "" ||
		grant.ArtifactDigest != "" || grant.PlaintextSize != 0 || grant.CiphertextSize != 0 ||
		grant.FormatVersion != 0 || grant.ChunkBytes != 0 || grant.JobKeyID != nil || grant.JobKeyVersion != 0 ||
		grant.Action != "archive_member_download" ||
		grant.RangePolicy != string(content.RangeNone) {
		return DeliveryAuditEvent{}, ErrDeliveryAudit
	}
	summary, err := summarizeDeliveryAudit(grant, requests)
	if err != nil {
		return DeliveryAuditEvent{}, err
	}
	return DeliveryAuditEvent{
		Actor:  backupasset.AuditActor{UserID: grant.OwnerUserID, Role: "admin"},
		Action: backupasset.AuditActionArchiveMember, Outcome: summary.outcome,
		RecoveryPointID: ref.RecoveryPointID, EntryID: ref.EntryID,
		SelectionDigest: grant.MemberChainDigest, ItemCount: 1, ByteCount: summary.byteCount,
		Mode: "read", ErrorCategory: summary.errorCategory,
	}, nil
}

type deliveryAuditSummary struct {
	outcome       backupasset.AuditOutcome
	byteCount     int64
	errorCategory string
}

func summarizeDeliveryAudit(
	grant model.BackupAssetExportDeliveryGrant,
	requests []model.BackupAssetExportDeliveryRequest,
) (deliveryAuditSummary, error) {
	if grant.AuditRequestCount <= 0 || int64(len(requests)) != grant.AuditRequestCount ||
		grant.AuditSuccessCount < 0 || grant.AuditBlockedCount < 0 || grant.AuditFailureCount < 0 ||
		grant.AuditSuccessCount+grant.AuditBlockedCount+grant.AuditFailureCount != grant.AuditRequestCount ||
		grant.AuditRangeCount < 0 || grant.AuditRangeCount > grant.AuditRequestCount || grant.AuditRangeBytes < 0 {
		return deliveryAuditSummary{}, ErrDeliveryAudit
	}
	var byteCount, successCount, blockedCount, failureCount int64
	errorCategory := ""
	for _, request := range requests {
		if request.GrantID != grant.ID || request.FinishedAt == nil || request.PlaintextBytes < 0 ||
			byteCount > math.MaxInt64-request.PlaintextBytes {
			return deliveryAuditSummary{}, ErrDeliveryAudit
		}
		byteCount += request.PlaintextBytes
		switch DeliveryRequestState(request.State) {
		case DeliveryRequestSucceeded:
			if request.FailureCode != "" {
				return deliveryAuditSummary{}, ErrDeliveryAudit
			}
			successCount++
		case DeliveryRequestBlocked:
			blockedCount++
			if request.FailureCode == string(content.RequestFailureRequestTooLarge) {
				errorCategory = higherDeliveryAuditError(errorCategory, "request_too_large")
			} else {
				errorCategory = higherDeliveryAuditError(errorCategory, "invalid_range")
			}
		case DeliveryRequestCanceled:
			failureCount++
			errorCategory = higherDeliveryAuditError(errorCategory, "client_canceled")
		case DeliveryRequestFailed:
			failureCount++
			errorCategory = higherDeliveryAuditError(errorCategory, "delivery_failed")
		case DeliveryRequestReconciled:
			failureCount++
			errorCategory = higherDeliveryAuditError(errorCategory, "reconciled_crash")
		default:
			return deliveryAuditSummary{}, ErrDeliveryAudit
		}
	}
	if successCount != grant.AuditSuccessCount || blockedCount != grant.AuditBlockedCount ||
		failureCount != grant.AuditFailureCount || grant.AuditRangeBytes > byteCount {
		return deliveryAuditSummary{}, ErrDeliveryAudit
	}
	outcome := backupasset.AuditOutcomeSuccess
	if failureCount > 0 {
		outcome = backupasset.AuditOutcomeFailure
	} else if blockedCount > 0 {
		outcome = backupasset.AuditOutcomeBlocked
	}
	return deliveryAuditSummary{outcome: outcome, byteCount: byteCount, errorCategory: errorCategory}, nil
}

func higherDeliveryAuditError(current, candidate string) string {
	if deliveryAuditErrorRank(candidate) > deliveryAuditErrorRank(current) {
		return candidate
	}
	return current
}

func deliveryAuditErrorRank(value string) int {
	switch value {
	case "invalid_range":
		return 1
	case "request_too_large":
		return 2
	case "client_canceled":
		return 3
	case "delivery_failed":
		return 4
	case "reconciled_crash":
		return 5
	default:
		return 0
	}
}

func (gateway *DeliveryGateway) ReconcileDeliveries(ctx context.Context) error {
	if gateway == nil || gateway.db == nil || gateway.budget == nil {
		return ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	budgetErr := gateway.budget.ReconcilePending(ctx)
	now := gateway.now().UTC()
	result := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Where("state IN ?", []string{"issued", "active", "draining"}).
		Updates(map[string]any{
			"state": "revoked", "revoke_reason": "process_restarted", "revoked_at": now,
			"updated_at": now, "version": gorm.Expr("version + 1"),
		})
	auditErr := gateway.flushDeliveryAudits(ctx, "")
	return errors.Join(budgetErr, result.Error, auditErr)
}

// MaintainDeliveries retries only eligible terminal delivery audits. Unlike
// ReconcileDeliveries, it is safe to call while this process is serving traffic:
// it never finalizes an open request or revokes an issued, active, or draining
// grant. Restart and drain reconciliation deliberately retain those actions.
func (gateway *DeliveryGateway) MaintainDeliveries(ctx context.Context) error {
	if gateway == nil || gateway.db == nil || gateway.audit == nil {
		return ErrInvalidDeliveryRequest
	}
	return gateway.flushDeliveryAudits(nonNilDeliveryContext(ctx), "")
}

func (gateway *DeliveryGateway) registerDeliveryRead(
	ctx context.Context,
	grantID string,
	requestID string,
	sessionJTI string,
) (context.Context, context.CancelFunc, chan struct{}, error) {
	if gateway == nil || backupasset.ValidateOpaqueID(grantID) != nil ||
		backupasset.ValidateOpaqueID(requestID) != nil || backupasset.ValidateOpaqueID(sessionJTI) != nil {
		return nil, nil, nil, ErrInvalidDeliveryRequest
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	var current struct {
		State      string
		SessionJTI string
	}
	result := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryGrant{}).
		Select("state", "session_jti").Where("id = ?", grantID).Limit(1).Scan(&current)
	if result.Error != nil {
		return nil, nil, nil, result.Error
	}
	if result.RowsAffected != 1 || current.State != "active" || current.SessionJTI != sessionJTI {
		return nil, nil, nil, ErrNotFound
	}
	readCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	if gateway.reads[grantID] == nil {
		gateway.reads[grantID] = make(map[string]activeExportDeliveryRead)
	}
	if _, exists := gateway.reads[grantID][requestID]; exists {
		cancel()
		return nil, nil, nil, ErrDeliveryReplay
	}
	gateway.reads[grantID][requestID] = activeExportDeliveryRead{
		sessionJTI: sessionJTI, cancel: cancel, done: done,
	}
	return readCtx, cancel, done, nil
}

func (gateway *DeliveryGateway) unregisterDeliveryRead(
	grantID string,
	requestID string,
	cancel context.CancelFunc,
	done chan struct{},
) {
	if gateway == nil || cancel == nil || done == nil {
		return
	}
	cancel()
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	read, exists := gateway.reads[grantID][requestID]
	if !exists || read.done != done {
		return
	}
	delete(gateway.reads[grantID], requestID)
	if len(gateway.reads[grantID]) == 0 {
		delete(gateway.reads, grantID)
	}
	close(done)
}

func (gateway *DeliveryGateway) Serve(
	ctx context.Context,
	request content.GatewayRequest,
	writer http.ResponseWriter,
) (resultErr error) {
	if gateway == nil || writer == nil || !validExportGatewayRequest(request) {
		return content.ErrContentNotFound
	}
	ctx = nonNilDeliveryContext(ctx)
	secret, err := content.ParseDeliveryCookie(request.RawCookie)
	if err != nil {
		return content.ErrContentNotFound
	}
	resourceKind, err := gateway.loadDeliveryResourceKind(ctx, request.DeliveryID)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return content.ErrContentUnavailable
		}
		return content.ErrContentNotFound
	}
	if resourceKind == "archive_member" {
		return gateway.serveArchiveMember(ctx, request, secret, writer)
	}
	if resourceKind != "export_archive" {
		return content.ErrContentNotFound
	}
	binding, err := gateway.loadExportDeliveryBinding(ctx, request.DeliveryID, secret)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return content.ErrContentUnavailable
		}
		return content.ErrContentNotFound
	}

	etag := `"` + binding.grant.ArtifactDigest + `"`
	plan, err := content.PlanRepresentation(content.RepresentationRequest{
		Method: request.Method, RangeHeaders: request.RangeHeaders, IfRangeHeaders: request.IfRangeHeaders,
		Size: binding.grant.PlaintextSize, ETag: etag, RangePolicy: content.RangeSingle, Seekable: true,
		FullAllowed: true, MaxResponseBytes: binding.grant.MaxCumulativeBytes,
	})
	if err != nil {
		return content.ErrContentNotFound
	}
	requestID, err := gateway.requestID()
	if err != nil || backupasset.ValidateOpaqueID(requestID) != nil {
		return content.ErrContentUnavailable
	}
	if plan.FailureCode != "" {
		if err := gateway.budget.RecordBlocked(ctx, DeliveryBlockedIntent{
			RequestID: requestID, GrantID: binding.grant.ID, Method: request.Method,
			RangeRequested: len(request.RangeHeaders) > 0, FailureCode: plan.FailureCode,
		}); err != nil {
			if errors.Is(err, ErrDeliveryBudgetExceeded) {
				return content.ErrContentBudgetExceeded
			}
			if errors.Is(err, ErrUnavailable) {
				return content.ErrContentUnavailable
			}
			return content.ErrContentNotFound
		}
		headers := exportDeliveryHeaders(binding, plan, etag)
		for name, values := range headers {
			writer.Header()[name] = append([]string(nil), values...)
		}
		writer.WriteHeader(plan.Status)
		return nil
	}
	reservedBytes, err := exportDeliveryReservationBytes(binding.artifact, request.Method)
	if err != nil {
		return content.ErrContentBudgetExceeded
	}
	reservation, err := gateway.budget.Reserve(ctx, DeliveryReservationIntent{
		RequestID: requestID, GrantID: binding.grant.ID, Method: request.Method,
		Range: plan.Range, ReservedBytes: reservedBytes,
	})
	if err != nil {
		if errors.Is(err, ErrDeliveryBudgetExceeded) {
			return content.ErrContentBudgetExceeded
		}
		if errors.Is(err, ErrUnavailable) {
			return content.ErrContentUnavailable
		}
		return content.ErrContentNotFound
	}
	var readResult CipherRangeResult
	evidenceKnown := false
	readCtx := ctx
	var readCancel context.CancelFunc
	var readDone chan struct{}
	defer func() {
		state := DeliveryRequestSucceeded
		failureCode := ""
		if resultErr != nil {
			state = DeliveryRequestFailed
			failureCode = "delivery_failed"
			if errors.Is(resultErr, context.Canceled) || errors.Is(resultErr, context.DeadlineExceeded) {
				state = DeliveryRequestCanceled
				failureCode = "client_canceled"
			}
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, finalizeErr := gateway.budget.Finalize(cleanupCtx, DeliveryFinalizeIntent{
			RequestID: reservation.RequestID, State: state, EvidenceKnown: evidenceKnown,
			PlaintextBytes: readResult.PlaintextBytes, CiphertextBytes: readResult.CiphertextBytes,
			FailureCode: failureCode,
		})
		if finalizeErr != nil {
			resultErr = errors.Join(resultErr, content.ErrContentUnavailable)
		}
		if readCancel != nil {
			gateway.unregisterDeliveryRead(
				binding.grant.ID, reservation.RequestID, readCancel, readDone,
			)
		}
	}()
	validateBinding := func(validationCtx context.Context) error {
		current, validationErr := gateway.loadExportDeliveryBinding(validationCtx, request.DeliveryID, secret)
		if validationErr != nil || !sameExportDeliveryBinding(current, binding) {
			return content.ErrContentNotFound
		}
		return nil
	}
	if request.Method == http.MethodHead {
		headers := exportDeliveryHeaders(binding, plan, etag)
		delayed := &delayedDeliveryWriter{
			destination: writer, status: plan.Status, headers: headers,
			validate: func() error { return validateBinding(ctx) },
		}
		if err := delayed.commit(); err != nil {
			return content.ErrContentNotFound
		}
		evidenceKnown = true
		return nil
	}
	readCtx, readCancel, readDone, err = gateway.registerDeliveryRead(
		ctx, binding.grant.ID, reservation.RequestID, binding.grant.SessionJTI,
	)
	if err != nil {
		return content.ErrContentNotFound
	}

	keyMaterial, err := gateway.keys.ByVersion(readCtx, backupasset.KeyDomainExportStore, binding.key.KEKVersion)
	defer clear(keyMaterial.Key)
	if err != nil || keyMaterial.Domain != backupasset.KeyDomainExportStore ||
		keyMaterial.Version != binding.key.KEKVersion ||
		(keyMaterial.State != backupasset.DomainKeyActive && keyMaterial.State != backupasset.DomainKeyVerifyOnly) ||
		len(keyMaterial.Key) != 32 {
		return content.ErrContentNotFound
	}
	dek, err := UnwrapJobDEK(JobKeyBinding{
		ExportID: binding.job.ID, SelectionDigest: binding.job.SelectionDigest,
		KEKVersion: binding.key.KEKVersion, WrapAlgorithm: binding.key.WrapAlgorithm,
	}, keyMaterial.Key, JobKeyEnvelope{
		Nonce: binding.key.EnvelopeNonce, Ciphertext: binding.key.WrappedDEK,
	})
	defer clear(dek)
	if err != nil {
		return content.ErrContentNotFound
	}
	file, err := gateway.store.OpenSealed(binding.artifact.Locator)
	if err != nil {
		return content.ErrContentNotFound
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, content.ErrContentUnavailable)
		}
	}()

	headers := exportDeliveryHeaders(binding, plan, etag)
	delayed := &delayedDeliveryWriter{
		destination: writer, status: plan.Status, headers: headers,
		validate: func() error { return validateBinding(readCtx) },
	}
	readResult, err = DecryptRange(
		readCtx, delayed, file, dek,
		CipherBinding{
			ExportID: binding.job.ID, SelectionDigest: binding.job.SelectionDigest,
			ArchiveProfile: binding.job.ArchiveProfile, FormatVersion: binding.artifact.FormatVersion,
			AttemptFenceDigest: binding.attempt.FenceDigest, Purpose: CipherPurposeFinalArchive,
		},
		CipherRangeMetadata{
			ChunkBytes: binding.artifact.ChunkBytes, ChunkCount: binding.artifact.ChunkCount,
			PlaintextBytes: binding.artifact.PlaintextSize, CiphertextBytes: binding.artifact.CiphertextSize,
			PlaintextDigest:  binding.artifact.PlaintextDigest,
			CiphertextDigest: binding.artifact.CiphertextDigest,
			NoncePrefix:      append([]byte(nil), binding.artifact.NoncePrefix...),
		},
		plan.Range.Offset, plan.Range.Length,
	)
	if readResult.CiphertextBytes > 0 {
		evidenceKnown = true
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || readCtx.Err() != nil {
			return errors.Join(content.ErrContentNotFound, readCtx.Err())
		}
		return content.ErrContentNotFound
	}
	evidenceKnown = true
	return nil
}

func (gateway *DeliveryGateway) serveArchiveMember(
	ctx context.Context,
	request content.GatewayRequest,
	secret string,
	writer http.ResponseWriter,
) (resultErr error) {
	binding, err := gateway.loadArchiveMemberDeliveryBinding(ctx, request.DeliveryID, secret)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return content.ErrContentUnavailable
		}
		return content.ErrContentNotFound
	}
	etag := `"` + binding.member.DerivedDigest + `"`
	plan, err := content.PlanRepresentation(content.RepresentationRequest{
		Method: request.Method, RangeHeaders: request.RangeHeaders, IfRangeHeaders: request.IfRangeHeaders,
		Size: binding.member.DerivedSize, ETag: etag, RangePolicy: content.RangeNone, Seekable: false,
		FullAllowed: true, MaxResponseBytes: binding.grant.MaxCumulativeBytes,
	})
	if err != nil {
		return content.ErrContentNotFound
	}
	requestID, err := gateway.requestID()
	if err != nil || backupasset.ValidateOpaqueID(requestID) != nil {
		return content.ErrContentUnavailable
	}
	if plan.FailureCode != "" {
		if err := gateway.budget.RecordBlocked(ctx, DeliveryBlockedIntent{
			RequestID: requestID, GrantID: binding.grant.ID, Method: request.Method,
			RangeRequested: len(request.RangeHeaders) > 0, FailureCode: plan.FailureCode,
		}); err != nil {
			if errors.Is(err, ErrDeliveryBudgetExceeded) {
				return content.ErrContentBudgetExceeded
			}
			if errors.Is(err, ErrUnavailable) {
				return content.ErrContentUnavailable
			}
			return content.ErrContentNotFound
		}
		headers := archiveMemberDeliveryHeaders(binding, plan, etag)
		for name, values := range headers {
			writer.Header()[name] = append([]string(nil), values...)
		}
		writer.WriteHeader(plan.Status)
		return nil
	}
	reservedBytes := binding.member.DerivedSize
	if request.Method == http.MethodHead {
		reservedBytes = 0
	}
	reservation, err := gateway.budget.Reserve(ctx, DeliveryReservationIntent{
		RequestID: requestID, GrantID: binding.grant.ID, Method: request.Method,
		Range: plan.Range, ReservedBytes: reservedBytes,
	})
	if err != nil {
		if errors.Is(err, ErrDeliveryBudgetExceeded) {
			return content.ErrContentBudgetExceeded
		}
		if errors.Is(err, ErrUnavailable) {
			return content.ErrContentUnavailable
		}
		return content.ErrContentNotFound
	}
	var plaintextBytes int64
	evidenceKnown := false
	readCtx := ctx
	var readCancel context.CancelFunc
	var readDone chan struct{}
	defer func() {
		state := DeliveryRequestSucceeded
		failureCode := ""
		if resultErr != nil {
			state = DeliveryRequestFailed
			failureCode = "delivery_failed"
			if errors.Is(resultErr, context.Canceled) || errors.Is(resultErr, context.DeadlineExceeded) {
				state = DeliveryRequestCanceled
				failureCode = "client_canceled"
			}
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, finalizeErr := gateway.budget.Finalize(cleanupCtx, DeliveryFinalizeIntent{
			RequestID: reservation.RequestID, State: state, EvidenceKnown: evidenceKnown,
			PlaintextBytes: plaintextBytes, CiphertextBytes: plaintextBytes, FailureCode: failureCode,
		})
		if finalizeErr != nil {
			resultErr = errors.Join(resultErr, content.ErrContentUnavailable)
		}
		if readCancel != nil {
			gateway.unregisterDeliveryRead(
				binding.grant.ID, reservation.RequestID, readCancel, readDone,
			)
		}
	}()
	validateBinding := func(validationCtx context.Context) error {
		current, validationErr := gateway.loadArchiveMemberDeliveryBinding(validationCtx, request.DeliveryID, secret)
		if validationErr != nil || !sameArchiveMemberDeliveryBinding(current, binding) {
			return content.ErrContentNotFound
		}
		return nil
	}
	headers := archiveMemberDeliveryHeaders(binding, plan, etag)
	if request.Method == http.MethodHead {
		delayed := &delayedDeliveryWriter{
			destination: writer, status: plan.Status, headers: headers,
			validate: func() error { return validateBinding(ctx) },
		}
		if err := delayed.commit(); err != nil {
			return content.ErrContentNotFound
		}
		evidenceKnown = true
		return nil
	}
	readCtx, readCancel, readDone, err = gateway.registerDeliveryRead(
		ctx, binding.grant.ID, reservation.RequestID, binding.grant.SessionJTI,
	)
	if err != nil {
		return content.ErrContentNotFound
	}
	delayed := &delayedDeliveryWriter{
		destination: writer, status: plan.Status, headers: headers,
		validate: func() error { return validateBinding(readCtx) },
	}
	exactWriter := &exactSizeDeliveryWriter{destination: delayed, maximum: binding.member.DerivedSize}
	if err := gateway.archiveMembers.ReadArchiveMember(readCtx, binding.member, exactWriter); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || readCtx.Err() != nil {
			return errors.Join(content.ErrContentNotFound, readCtx.Err())
		}
		return content.ErrContentNotFound
	}
	if exactWriter.written != binding.member.DerivedSize {
		return content.ErrContentNotFound
	}
	if !delayed.committed {
		if err := delayed.commit(); err != nil {
			return content.ErrContentNotFound
		}
	}
	plaintextBytes = exactWriter.written
	evidenceKnown = true
	return nil
}

func (gateway *DeliveryGateway) loadDeliveryResourceKind(
	ctx context.Context,
	deliveryID string,
) (string, error) {
	if gateway == nil || gateway.db == nil || backupasset.ValidateOpaqueID(deliveryID) != nil {
		return "", ErrNotFound
	}
	var row struct {
		ResourceKind string
	}
	result := gateway.db.WithContext(nonNilDeliveryContext(ctx)).
		Model(&model.BackupAssetExportDeliveryGrant{}).
		Select("resource_kind").Where("delivery_id = ?", deliveryID).Limit(1).Scan(&row)
	if result.Error != nil {
		return "", ErrUnavailable
	}
	if result.RowsAffected != 1 || (row.ResourceKind != "export_archive" && row.ResourceKind != "archive_member") {
		return "", ErrNotFound
	}
	return row.ResourceKind, nil
}

func (gateway *DeliveryGateway) loadArchiveMemberDeliveryBinding(
	ctx context.Context,
	deliveryID string,
	secret string,
) (archiveMemberDeliveryBinding, error) {
	var binding archiveMemberDeliveryBinding
	if gateway == nil || gateway.db == nil || gateway.archiveMembers == nil || gateway.archiveMemberAuthorize == nil {
		return binding, ErrNotFound
	}
	result := gateway.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).Limit(1).Find(&binding.grant)
	if result.Error != nil {
		return binding, ErrUnavailable
	}
	now := gateway.now().UTC()
	if result.RowsAffected != 1 || !validActiveArchiveMemberDeliveryGrant(binding.grant, deliveryID, secret, now) {
		return binding, ErrNotFound
	}
	if binding.grant.TokenVersion < 0 || uint64(binding.grant.TokenVersion) > uint64(^uint(0)) {
		return binding, ErrNotFound
	}
	session := content.DeliverySession{
		JTI: binding.grant.SessionJTI, UserID: binding.grant.OwnerUserID, Role: "admin",
		TokenVersion: uint(binding.grant.TokenVersion), ExpiresAt: binding.grant.AbsoluteExpiresAt,
	}
	if err := gateway.session.Validate(ctx, session); err != nil {
		return binding, ErrNotFound
	}
	ref := backupasset.AssetRef{
		RecoveryPointID: binding.grant.OuterRecoveryPointID,
		EntryID:         binding.grant.OuterEntryID,
	}
	actor := content.DeliveryActor{UserID: binding.grant.OwnerUserID, Role: "admin"}
	asset, err := gateway.archiveMemberAuthorize.Authorize(ctx, actor, ref, content.DeliveryDownload)
	if err != nil || !archiveMemberGrantMatchesAsset(binding.grant, asset) {
		return binding, ErrNotFound
	}
	if err := gateway.archiveMemberAuthorize.Reauthorize(ctx, actor, asset, content.DeliveryDownload); err != nil {
		return binding, ErrNotFound
	}
	member, err := gateway.archiveMembers.ResolveArchiveMember(ctx, content.ArchiveMemberArtifactRequest{
		RequestID: *binding.grant.MemberRequestID, OwnerUserID: binding.grant.OwnerUserID, Asset: asset,
	})
	if err != nil {
		return binding, ErrNotFound
	}
	binding.asset, binding.member = asset, member
	if !archiveMemberDeliveryBindingMatches(binding, now) {
		return archiveMemberDeliveryBinding{}, ErrNotFound
	}
	return binding, nil
}

func (gateway *DeliveryGateway) loadExportDeliveryBinding(
	ctx context.Context,
	deliveryID string,
	secret string,
) (exportDeliveryBinding, error) {
	var binding exportDeliveryBinding
	result := gateway.db.WithContext(ctx).Where("delivery_id = ?", deliveryID).Limit(1).Find(&binding.grant)
	if result.Error != nil {
		return binding, ErrUnavailable
	}
	now := gateway.now().UTC()
	if result.RowsAffected != 1 || !validActiveExportDeliveryGrant(binding.grant, deliveryID, secret, now) {
		return binding, ErrNotFound
	}
	if binding.grant.TokenVersion < 0 || uint64(binding.grant.TokenVersion) > uint64(^uint(0)) {
		return binding, ErrNotFound
	}
	session := content.DeliverySession{
		JTI: binding.grant.SessionJTI, UserID: binding.grant.OwnerUserID, Role: "admin",
		TokenVersion: uint(binding.grant.TokenVersion), ExpiresAt: binding.grant.AbsoluteExpiresAt,
	}
	if err := gateway.session.Validate(ctx, session); err != nil {
		return binding, ErrNotFound
	}
	result = gateway.db.WithContext(ctx).Where("id = ?", *binding.grant.ExportJobID).Limit(1).Find(&binding.job)
	if result.Error != nil {
		return binding, ErrUnavailable
	}
	if result.RowsAffected != 1 {
		return binding, ErrNotFound
	}
	var artifactCount int64
	if err := gateway.db.WithContext(ctx).Model(&model.BackupAssetExportArtifact{}).
		Where("job_id = ? AND state = ? AND purged_at IS NULL", binding.job.ID, "sealed").
		Count(&artifactCount).Error; err != nil {
		return binding, ErrUnavailable
	}
	if artifactCount != 1 {
		return binding, ErrNotFound
	}
	result = gateway.db.WithContext(ctx).Where("id = ?", *binding.grant.ExportArtifactID).
		Limit(1).Find(&binding.artifact)
	if result.Error != nil {
		return binding, ErrUnavailable
	}
	if result.RowsAffected != 1 {
		return binding, ErrNotFound
	}
	result = gateway.db.WithContext(ctx).Where("id = ?", *binding.grant.ExportAttemptID).
		Limit(1).Find(&binding.attempt)
	if result.Error != nil {
		return binding, ErrUnavailable
	}
	if result.RowsAffected != 1 || !validAttemptFenceDigest(binding.attempt.FenceToken, binding.attempt.FenceDigest) {
		return binding, ErrNotFound
	}
	result = gateway.db.WithContext(ctx).Where("id = ?", *binding.grant.JobKeyID).
		Limit(1).Find(&binding.key)
	if result.Error != nil {
		return binding, ErrUnavailable
	}
	if result.RowsAffected != 1 || !exportDeliveryBindingMatches(binding, now) {
		return binding, ErrNotFound
	}
	return binding, nil
}

type delayedDeliveryWriter struct {
	destination http.ResponseWriter
	status      int
	headers     http.Header
	validate    func() error
	committed   bool
}

func (writer *delayedDeliveryWriter) Write(payload []byte) (int, error) {
	if writer == nil || writer.destination == nil {
		return 0, content.ErrContentNotFound
	}
	if writer.validate != nil {
		if err := writer.validate(); err != nil {
			return 0, err
		}
	}
	if err := writer.commit(); err != nil {
		return 0, err
	}
	return writer.destination.Write(payload)
}

func (writer *delayedDeliveryWriter) commit() error {
	if writer == nil || writer.destination == nil {
		return content.ErrContentNotFound
	}
	if writer.committed {
		return nil
	}
	destinationHeaders := writer.destination.Header()
	if writer.validate != nil {
		if err := writer.validate(); err != nil {
			return err
		}
	}
	for name, values := range writer.headers {
		destinationHeaders[name] = append([]string(nil), values...)
	}
	writer.destination.WriteHeader(writer.status)
	writer.committed = true
	return nil
}

type exactSizeDeliveryWriter struct {
	destination io.Writer
	maximum     int64
	written     int64
}

func (writer *exactSizeDeliveryWriter) Write(payload []byte) (int, error) {
	if writer == nil || writer.destination == nil || writer.maximum < 0 || writer.written < 0 ||
		writer.written > writer.maximum || int64(len(payload)) > writer.maximum-writer.written {
		return 0, content.ErrContentNotFound
	}
	written, err := writer.destination.Write(payload)
	if written < 0 || written > len(payload) {
		return 0, content.ErrContentNotFound
	}
	writer.written += int64(written)
	if err == nil && written != len(payload) {
		err = io.ErrShortWrite
	}
	return written, err
}

func newDeliveryBudget(db *gorm.DB, now func() time.Time) (*deliveryBudget, error) {
	if db == nil || now == nil {
		return nil, ErrInvalidDeliveryRequest
	}
	return &deliveryBudget{db: db, now: now}, nil
}

func (budget *deliveryBudget) Reserve(
	ctx context.Context,
	intent DeliveryReservationIntent,
) (DeliveryReservation, error) {
	if budget == nil || !validDeliveryReservationIntent(intent) {
		return DeliveryReservation{}, ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	now := budget.now().UTC()
	var reservation DeliveryReservation
	err := budget.transactionWithRetry(ctx, func(tx *gorm.DB) error {
		var existing model.BackupAssetExportDeliveryRequest
		existingResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", intent.RequestID).Limit(1).Find(&existing)
		if existingResult.Error != nil {
			return existingResult.Error
		}
		if existingResult.RowsAffected == 1 {
			if existing.State != string(DeliveryRequestReserved) || !deliveryReservationMatches(existing, intent) {
				return ErrDeliveryReplay
			}
			reservation = DeliveryReservation{
				RequestID: existing.ID, GrantID: existing.GrantID,
				ReservedBytes: existing.ReservedBytes, AlreadyReserved: true,
			}
			return nil
		}

		var grant model.BackupAssetExportDeliveryGrant
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", intent.GrantID).Limit(1).Find(&grant)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || !deliveryGrantAcceptsRequest(grant, now) {
			return ErrNotFound
		}
		if grant.RequestCount >= grant.MaxRequests || grant.InFlight >= grant.MaxInFlight ||
			!withinDeliveryByteLimit(grant.ConsumedBytes, grant.ReservedBytes, intent.ReservedBytes, grant.MaxCumulativeBytes) {
			return ErrDeliveryBudgetExceeded
		}

		request := model.BackupAssetExportDeliveryRequest{
			ID: intent.RequestID, GrantID: grant.ID, Method: intent.Method,
			RangeRequested: intent.Range.Kind != content.HTTPRangeFull,
			State:          string(DeliveryRequestReserved), ReservedBytes: intent.ReservedBytes,
			StartedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if intent.Range.Kind != content.HTTPRangeFull {
			offset, length := intent.Range.Offset, intent.Range.Length
			request.RangeOffset, request.RangeLength = &offset, &length
		}
		if err := tx.Create(&request).Error; err != nil {
			return err
		}
		result = tx.Model(&model.BackupAssetExportDeliveryGrant{}).
			Where("id = ? AND version = ? AND state = ?", grant.ID, grant.Version, "active").
			Updates(map[string]any{
				"request_count":  grant.RequestCount + 1,
				"reserved_bytes": grant.ReservedBytes + intent.ReservedBytes,
				"in_flight":      grant.InFlight + 1,
				"last_access_at": now,
				"updated_at":     now,
				"version":        grant.Version + 1,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errDeliveryBudgetConcurrent
		}
		reservation = DeliveryReservation{
			RequestID: request.ID, GrantID: request.GrantID, ReservedBytes: request.ReservedBytes,
		}
		return nil
	})
	return reservation, err
}

func (budget *deliveryBudget) RecordBlocked(ctx context.Context, intent DeliveryBlockedIntent) error {
	if budget == nil || !validDeliveryBlockedIntent(intent) {
		return ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	now := budget.now().UTC()
	return budget.transactionWithRetry(ctx, func(tx *gorm.DB) error {
		var existing model.BackupAssetExportDeliveryRequest
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", intent.RequestID).Limit(1).Find(&existing)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			if existing.GrantID == intent.GrantID && existing.Method == intent.Method &&
				existing.State == string(DeliveryRequestBlocked) && existing.FailureCode == string(intent.FailureCode) &&
				existing.RangeRequested == intent.RangeRequested && existing.ReservedBytes == 0 &&
				existing.RangeOffset == nil && existing.RangeLength == nil {
				return nil
			}
			return ErrDeliveryReplay
		}

		var grant model.BackupAssetExportDeliveryGrant
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", intent.GrantID).Limit(1).Find(&grant)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || !deliveryGrantAcceptsRequest(grant, now) {
			return ErrNotFound
		}
		if grant.RequestCount >= grant.MaxRequests {
			return ErrDeliveryBudgetExceeded
		}
		if err := applyExportDeliveryAuditSummary(
			&grant, backupasset.AuditOutcomeBlocked, 0, intent.RangeRequested,
		); err != nil {
			return err
		}
		finishedAt := now
		request := model.BackupAssetExportDeliveryRequest{
			ID: intent.RequestID, GrantID: grant.ID, Method: intent.Method,
			RangeRequested: intent.RangeRequested,
			State:          string(DeliveryRequestBlocked), FailureCode: string(intent.FailureCode),
			StartedAt: now, FinishedAt: &finishedAt, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&request).Error; err != nil {
			return err
		}
		result = tx.Model(&model.BackupAssetExportDeliveryGrant{}).
			Where("id = ? AND version = ? AND state = ?", grant.ID, grant.Version, "active").
			Updates(map[string]any{
				"request_count": grant.RequestCount + 1, "last_access_at": now,
				"audit_state": grant.AuditState, "audit_range_count": grant.AuditRangeCount,
				"audit_range_bytes": grant.AuditRangeBytes, "audit_request_count": grant.AuditRequestCount,
				"audit_success_count": grant.AuditSuccessCount, "audit_blocked_count": grant.AuditBlockedCount,
				"audit_failure_count": grant.AuditFailureCount,
				"updated_at":          now, "version": grant.Version + 1,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errDeliveryBudgetConcurrent
		}
		return nil
	})
}

func (budget *deliveryBudget) Finalize(
	ctx context.Context,
	intent DeliveryFinalizeIntent,
) (DeliveryFinalization, error) {
	if budget == nil || !validDeliveryFinalizeIntent(intent) {
		return DeliveryFinalization{}, ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	now := budget.now().UTC()
	var finalization DeliveryFinalization
	err := budget.transactionWithRetry(ctx, func(tx *gorm.DB) error {
		var request model.BackupAssetExportDeliveryRequest
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", intent.RequestID).Limit(1).Find(&request)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		if deliveryRequestTerminal(DeliveryRequestState(request.State)) {
			if !deliveryFinalizationMatches(request, intent) {
				return ErrDeliveryReplay
			}
			finalization = terminalDeliveryFinalization(request)
			return nil
		}
		if request.State != string(DeliveryRequestReserved) && request.State != string(DeliveryRequestStreaming) {
			return ErrDeliveryState
		}

		var grant model.BackupAssetExportDeliveryGrant
		result = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", request.GrantID).Limit(1).Find(&grant)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 || grant.ReservedBytes < request.ReservedBytes || grant.InFlight <= 0 {
			return ErrDeliveryState
		}
		charged, evidenceValid := deliveryFinalCharge(request.ReservedBytes, intent)
		if intent.State == DeliveryRequestSucceeded && !evidenceValid ||
			grant.ConsumedBytes > math.MaxInt64-charged {
			return ErrInvalidDeliveryRequest
		}
		outcome := backupasset.AuditOutcomeFailure
		switch intent.State {
		case DeliveryRequestSucceeded:
			outcome = backupasset.AuditOutcomeSuccess
		case DeliveryRequestBlocked:
			outcome = backupasset.AuditOutcomeBlocked
		}
		plaintext := boundedDeliveryEvidence(request.ReservedBytes, intent.PlaintextBytes)
		if err := applyExportDeliveryAuditSummary(
			&grant, outcome, plaintext, request.RangeRequested,
		); err != nil {
			return err
		}

		result = tx.Model(&model.BackupAssetExportDeliveryGrant{}).
			Where("id = ? AND version = ?", grant.ID, grant.Version).
			Updates(map[string]any{
				"reserved_bytes": grant.ReservedBytes - request.ReservedBytes,
				"consumed_bytes": grant.ConsumedBytes + charged,
				"in_flight":      grant.InFlight - 1,
				"audit_state":    grant.AuditState, "audit_range_count": grant.AuditRangeCount,
				"audit_range_bytes": grant.AuditRangeBytes, "audit_request_count": grant.AuditRequestCount,
				"audit_success_count": grant.AuditSuccessCount, "audit_blocked_count": grant.AuditBlockedCount,
				"audit_failure_count": grant.AuditFailureCount,
				"updated_at":          now,
				"version":             grant.Version + 1,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errDeliveryBudgetConcurrent
		}

		ciphertext := boundedDeliveryEvidence(request.ReservedBytes, intent.CiphertextBytes)
		failureCode := intent.FailureCode
		if intent.State == DeliveryRequestReconciled && failureCode == "" {
			failureCode = "reconciled_crash"
		}
		finishedAt := now
		result = tx.Model(&model.BackupAssetExportDeliveryRequest{}).
			Where("id = ? AND state IN ?", request.ID,
				[]string{string(DeliveryRequestReserved), string(DeliveryRequestStreaming)}).
			Updates(map[string]any{
				"state": string(intent.State), "plaintext_bytes": plaintext,
				"ciphertext_bytes": ciphertext, "failure_code": failureCode,
				"finished_at": finishedAt, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errDeliveryBudgetConcurrent
		}
		finalization = DeliveryFinalization{RequestID: request.ID, State: intent.State, ChargedBytes: charged}
		return nil
	})
	return finalization, err
}

func (budget *deliveryBudget) transactionWithRetry(
	ctx context.Context,
	operation func(*gorm.DB) error,
) error {
	var lastErr error
	for attempt := 0; attempt < 16; attempt++ {
		err := budget.db.WithContext(ctx).Transaction(operation)
		if err == nil || !retryableDeliveryBudgetError(err) {
			return err
		}
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		runtime.Gosched()
	}
	return lastErr
}

func retryableDeliveryBudgetError(err error) bool {
	if errors.Is(err, errDeliveryBudgetConcurrent) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "deadlock detected") || strings.Contains(message, "could not serialize access") ||
		strings.Contains(message, "serialization failure")
}

func (budget *deliveryBudget) ReconcilePending(ctx context.Context) error {
	if budget == nil {
		return ErrInvalidDeliveryRequest
	}
	ctx = nonNilDeliveryContext(ctx)
	var rows []struct{ ID string }
	query := budget.db.WithContext(ctx).Model(&model.BackupAssetExportDeliveryRequest{}).
		Select("id").Where("state IN ?", []string{string(DeliveryRequestReserved), string(DeliveryRequestStreaming)}).
		Order("created_at ASC, id ASC").Scan(&rows)
	if query.Error != nil {
		return query.Error
	}
	var reconcileErrors []error
	for _, row := range rows {
		if _, err := budget.Finalize(ctx, DeliveryFinalizeIntent{
			RequestID: row.ID, State: DeliveryRequestReconciled, FailureCode: "reconciled_crash",
		}); err != nil {
			reconcileErrors = append(reconcileErrors, err)
		}
	}
	return errors.Join(reconcileErrors...)
}

func validDeliveryReservationIntent(intent DeliveryReservationIntent) bool {
	if backupasset.ValidateOpaqueID(intent.RequestID) != nil || backupasset.ValidateOpaqueID(intent.GrantID) != nil ||
		(intent.Method != http.MethodGet && intent.Method != http.MethodHead) || intent.ReservedBytes < 0 ||
		!validResolvedDeliveryRange(intent.Range) {
		return false
	}
	if intent.Method == http.MethodHead {
		return intent.ReservedBytes == 0
	}
	return intent.ReservedBytes >= intent.Range.Length
}

func validDeliveryBlockedIntent(intent DeliveryBlockedIntent) bool {
	if backupasset.ValidateOpaqueID(intent.RequestID) != nil || backupasset.ValidateOpaqueID(intent.GrantID) != nil ||
		(intent.Method != http.MethodGet && intent.Method != http.MethodHead) {
		return false
	}
	switch intent.FailureCode {
	case content.RequestFailureInvalidRange, content.RequestFailureRangeNotAllowed,
		content.RequestFailureIfRangeFullForbidden, content.RequestFailureRequestTooLarge:
		return true
	default:
		return false
	}
}

func validResolvedDeliveryRange(value content.HTTPRange) bool {
	switch value.Kind {
	case content.HTTPRangeFull:
		return value.Start == nil && value.EndExclusive == nil && value.SuffixLength == nil &&
			value.Offset == 0 && value.Length >= 0
	case content.HTTPRangeNormal:
		return value.Start != nil && value.EndExclusive != nil && value.SuffixLength == nil &&
			value.Offset >= 0 && value.Length > 0 && value.Offset <= math.MaxInt64-value.Length &&
			*value.Start == value.Offset && *value.EndExclusive == value.Offset+value.Length
	case content.HTTPRangeOpenEnded:
		return value.Start != nil && value.EndExclusive == nil && value.SuffixLength == nil &&
			*value.Start == value.Offset && value.Offset >= 0 && value.Length > 0
	case content.HTTPRangeSuffix:
		return value.Start == nil && value.EndExclusive == nil && value.SuffixLength != nil &&
			*value.SuffixLength > 0 && value.Offset >= 0 && value.Length > 0
	default:
		return false
	}
}

func validDeliveryFinalizeIntent(intent DeliveryFinalizeIntent) bool {
	if backupasset.ValidateOpaqueID(intent.RequestID) != nil || len(intent.FailureCode) > 64 ||
		!deliveryRequestFinalizable(intent.State) {
		return false
	}
	if intent.State == DeliveryRequestSucceeded {
		return intent.FailureCode == "" && intent.EvidenceKnown && intent.PlaintextBytes >= 0 && intent.CiphertextBytes >= 0
	}
	return intent.PlaintextBytes >= 0 && intent.CiphertextBytes >= 0
}

func deliveryReservationMatches(row model.BackupAssetExportDeliveryRequest, intent DeliveryReservationIntent) bool {
	if row.GrantID != intent.GrantID || row.Method != intent.Method || row.ReservedBytes != intent.ReservedBytes {
		return false
	}
	if intent.Range.Kind == content.HTTPRangeFull {
		return row.RangeOffset == nil && row.RangeLength == nil
	}
	return row.RangeOffset != nil && row.RangeLength != nil &&
		*row.RangeOffset == intent.Range.Offset && *row.RangeLength == intent.Range.Length
}

func deliveryGrantAcceptsRequest(grant model.BackupAssetExportDeliveryGrant, now time.Time) bool {
	return grant.State == "active" && now.Before(grant.IdleExpiresAt.UTC()) && now.Before(grant.AbsoluteExpiresAt.UTC()) &&
		grant.MaxRequests > 0 && grant.MaxCumulativeBytes > 0 && grant.MaxInFlight > 0 &&
		grant.RequestCount >= 0 && grant.ReservedBytes >= 0 && grant.ConsumedBytes >= 0 && grant.InFlight >= 0
}

func withinDeliveryByteLimit(consumed, reserved, additional, limit int64) bool {
	return consumed >= 0 && reserved >= 0 && additional >= 0 && limit >= 0 &&
		consumed <= limit && reserved <= limit-consumed && additional <= limit-consumed-reserved
}

func deliveryFinalCharge(reserved int64, intent DeliveryFinalizeIntent) (int64, bool) {
	valid := intent.EvidenceKnown && intent.PlaintextBytes >= 0 && intent.CiphertextBytes >= 0 &&
		intent.PlaintextBytes <= reserved && intent.CiphertextBytes <= reserved
	if !valid {
		return reserved, false
	}
	return max(intent.PlaintextBytes, intent.CiphertextBytes), true
}

func boundedDeliveryEvidence(reserved, value int64) int64 {
	if value < 0 || value > reserved {
		return 0
	}
	return value
}

func deliveryRequestFinalizable(state DeliveryRequestState) bool {
	return state == DeliveryRequestSucceeded || state == DeliveryRequestBlocked || state == DeliveryRequestCanceled ||
		state == DeliveryRequestFailed || state == DeliveryRequestReconciled
}

func deliveryRequestTerminal(state DeliveryRequestState) bool {
	return deliveryRequestFinalizable(state)
}

func terminalDeliveryFinalization(request model.BackupAssetExportDeliveryRequest) DeliveryFinalization {
	charged := max(request.PlaintextBytes, request.CiphertextBytes)
	if DeliveryRequestState(request.State) == DeliveryRequestReconciled {
		charged = request.ReservedBytes
	}
	return DeliveryFinalization{
		RequestID: request.ID, State: DeliveryRequestState(request.State),
		ChargedBytes: charged, AlreadyFinalized: true,
	}
}

func deliveryFinalizationMatches(
	request model.BackupAssetExportDeliveryRequest,
	intent DeliveryFinalizeIntent,
) bool {
	return request.State == string(intent.State) && request.FailureCode == intent.FailureCode &&
		request.PlaintextBytes == boundedDeliveryEvidence(request.ReservedBytes, intent.PlaintextBytes) &&
		request.CiphertextBytes == boundedDeliveryEvidence(request.ReservedBytes, intent.CiphertextBytes)
}

func validDeliveryGatewayConfig(config DeliveryGatewayConfig) bool {
	return config.TicketTTL >= 30*time.Second && config.TicketTTL <= 15*time.Minute &&
		config.MaxRequests > 0 && config.MaxRequests <= backupasset.MaxAuditRangeCount &&
		config.MaxCumulativeBytes > 0 && config.MaxInFlight > 0
}

func applyExportDeliveryAuditSummary(
	grant *model.BackupAssetExportDeliveryGrant,
	outcome backupasset.AuditOutcome,
	responseBytes int64,
	rangeRequested bool,
) error {
	if grant == nil || responseBytes < 0 ||
		(outcome != backupasset.AuditOutcomeSuccess && outcome != backupasset.AuditOutcomeBlocked &&
			outcome != backupasset.AuditOutcomeFailure) ||
		(grant.AuditState != "none" && grant.AuditState != "pending") ||
		grant.AuditRangeCount < 0 || grant.AuditRangeBytes < 0 || grant.AuditRequestCount < 0 ||
		grant.AuditSuccessCount < 0 || grant.AuditBlockedCount < 0 || grant.AuditFailureCount < 0 ||
		grant.AuditFailureCode != "" || grant.AuditAttemptCount != 0 || grant.AuditNextAttemptAt != nil {
		return ErrDeliveryState
	}
	if grant.AuditState == "none" && (grant.AuditRangeCount != 0 || grant.AuditRangeBytes != 0 ||
		grant.AuditRequestCount != 0 || grant.AuditSuccessCount != 0 || grant.AuditBlockedCount != 0 ||
		grant.AuditFailureCount != 0) {
		return ErrDeliveryState
	}
	if grant.AuditRequestCount >= backupasset.MaxAuditRangeCount {
		return ErrDeliveryBudgetExceeded
	}
	grant.AuditState = "pending"
	grant.AuditRequestCount++
	if rangeRequested {
		if grant.AuditRangeCount >= backupasset.MaxAuditRangeCount {
			return ErrDeliveryBudgetExceeded
		}
		grant.AuditRangeCount++
		grant.AuditRangeBytes = boundedExportDeliveryAuditMetric(
			grant.AuditRangeBytes, responseBytes, backupasset.MaxAuditRangeBytes,
		)
	}
	switch outcome {
	case backupasset.AuditOutcomeSuccess:
		grant.AuditSuccessCount++
	case backupasset.AuditOutcomeBlocked:
		grant.AuditBlockedCount++
	case backupasset.AuditOutcomeFailure:
		grant.AuditFailureCount++
	}
	return nil
}

func boundedExportDeliveryAuditMetric(current, increment, maximum int64) int64 {
	if current >= maximum || increment >= maximum-current {
		return maximum
	}
	return current + increment
}

func validExportDeliveryIssueRequest(request ExportDeliveryIssueRequest, now time.Time) bool {
	return request.Actor.UserID > 0 && request.Actor.Role == "admin" && request.Session.UserID == request.Actor.UserID &&
		request.Session.Role == request.Actor.Role && backupasset.ValidateOpaqueID(request.Session.JTI) == nil &&
		uint64(request.Session.TokenVersion) <= uint64(math.MaxInt64) && request.Session.ExpiresAt.After(now) &&
		backupasset.ValidateOpaqueID(request.ExportJobID) == nil &&
		request.Proof.Action == "asset.export_download" && backupasset.ValidateOpaqueID(request.Proof.ID) == nil &&
		request.Proof.ExpiresAt.After(now)
}

func validArchiveMemberDeliveryIssueRequest(request ArchiveMemberDeliveryIssueRequest, now time.Time) bool {
	return request.Actor.UserID > 0 && request.Actor.Role == "admin" &&
		request.Session.UserID == request.Actor.UserID && request.Session.Role == request.Actor.Role &&
		backupasset.ValidateOpaqueID(request.Session.JTI) == nil &&
		uint64(request.Session.TokenVersion) <= uint64(math.MaxInt64) && request.Session.ExpiresAt.After(now) &&
		backupasset.ValidateOpaqueID(request.MemberRequestID) == nil && validArchiveMemberDeliveryAsset(request.Asset) &&
		request.Proof.Action == "asset.download" && backupasset.ValidateOpaqueID(request.Proof.ID) == nil &&
		request.Proof.ExpiresAt.After(now)
}

func validArchiveMemberDeliveryAsset(asset content.AuthorizedAsset) bool {
	return backupasset.ValidateAssetRef(asset.Ref) == nil && backupasset.ValidateOpaqueID(asset.CatalogGenerationID) == nil &&
		asset.SourceFingerprint != "" && len(asset.SourceFingerprint) <= 128 &&
		asset.EntryFingerprint != "" && len(asset.EntryFingerprint) <= 128 &&
		asset.ProviderCapabilityRevision > 0 && asset.Size >= 0 &&
		(asset.Provider == backupasset.ProviderRestic || asset.Provider == backupasset.ProviderRsync ||
			asset.Provider == backupasset.ProviderRclone)
}

func sameArchiveMemberDeliveryAsset(left, right content.AuthorizedAsset) bool {
	return validArchiveMemberDeliveryAsset(left) && validArchiveMemberDeliveryAsset(right) &&
		left.Ref == right.Ref && left.CatalogGenerationID == right.CatalogGenerationID &&
		left.RepositoryID == right.RepositoryID && left.Provider == right.Provider &&
		left.ProviderCapabilityRevision == right.ProviderCapabilityRevision &&
		left.SourceFingerprint == right.SourceFingerprint && left.EntryFingerprint == right.EntryFingerprint &&
		left.FingerprintStrength == right.FingerprintStrength && left.Size == right.Size &&
		sameArchiveMemberDeliveryTime(left.ModifiedAt, right.ModifiedAt) && left.MediaType == right.MediaType &&
		left.Path == right.Path && left.Name == right.Name && left.RangeProven == right.RangeProven &&
		left.SearchClassification == right.SearchClassification &&
		left.SearchClassificationRevision == right.SearchClassificationRevision
}

func sameArchiveMemberDeliveryTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.UTC().Equal(right.UTC())
}

func validArchiveMemberDeliveryBinding(
	request ArchiveMemberDeliveryIssueRequest,
	binding content.ResolvedArchiveMemberArtifact,
	now time.Time,
) bool {
	asset := request.Asset
	return binding.MemberRequestID == request.MemberRequestID && binding.OwnerUserID == request.Actor.UserID &&
		binding.Ref == asset.Ref && binding.CatalogGenerationID == asset.CatalogGenerationID &&
		binding.SourceFingerprint == asset.SourceFingerprint && binding.EntryFingerprint == asset.EntryFingerprint &&
		lowerHex(binding.MemberChainDigest, 64) && backupasset.ValidateOpaqueID(binding.ProcessingJobID) == nil &&
		backupasset.ValidateOpaqueID(binding.ProcessingAttemptID) == nil &&
		backupasset.ValidateOpaqueID(binding.DerivedArtifactSetID) == nil &&
		backupasset.ValidateOpaqueID(binding.DerivedArtifactID) == nil &&
		backupasset.ValidateOpaqueID(binding.DerivedBlobID) == nil && lowerHex(binding.DerivedDigest, 64) &&
		binding.DerivedSize >= 0 && binding.DerivedSize <= 256<<20 && validArchiveMemberMediaType(binding.MediaType) &&
		binding.AbsoluteExpiresAt.Location() == time.UTC && binding.AbsoluteExpiresAt.After(now) &&
		binding.Provider == asset.Provider && binding.ProviderCapabilityRevision == asset.ProviderCapabilityRevision &&
		binding.FingerprintStrength == asset.FingerprintStrength && binding.SourceSize == asset.Size &&
		binding.SourceMediaType == asset.MediaType && strings.TrimSpace(binding.SecurityPolicyRevision) != "" &&
		len(binding.SecurityPolicyRevision) <= 128
}

func validArchiveMemberMediaType(value string) bool {
	switch value {
	case "text/plain", "image/png", "image/jpeg", "application/pdf", "application/octet-stream":
		return true
	default:
		return false
	}
}

func validExportTicketMaterial(material content.TicketMaterial) bool {
	return backupasset.ValidateOpaqueID(material.GrantID) == nil &&
		backupasset.ValidateOpaqueID(material.DeliveryID) == nil && material.GrantID != material.DeliveryID &&
		content.VerifyCookieSecret(material.CookieSecretHash, material.CookieSecret)
}

func deliveryIdentifierCollisionTx(tx *gorm.DB, deliveryID string) (bool, error) {
	if tx == nil || backupasset.ValidateOpaqueID(deliveryID) != nil {
		return false, ErrInvalidDeliveryRequest
	}
	var contentCount int64
	if err := tx.Model(&model.BackupAssetDeliveryGrant{}).Where("delivery_id = ?", deliveryID).Count(&contentCount).Error; err != nil {
		return false, err
	}
	var exportCount int64
	if err := tx.Model(&model.BackupAssetExportDeliveryGrant{}).Where("delivery_id = ?", deliveryID).Count(&exportCount).Error; err != nil {
		return false, err
	}
	return contentCount != 0 || exportCount != 0, nil
}

func validReadyDeliveryJob(job model.BackupAssetExportJob, now time.Time) bool {
	return backupasset.ValidateOpaqueID(job.ID) == nil && job.OwnerUserID > 0 &&
		lowerHex(job.SelectionDigest, 64) && job.ExecutionState == string(ExecutionReady) &&
		(job.ResultKind == string(ResultComplete) || job.ResultKind == string(ResultPartial)) &&
		job.CleanupState == string(CleanupNone) && job.CurrentAttemptID != nil &&
		backupasset.ValidateOpaqueID(*job.CurrentAttemptID) == nil && job.ReadyAt != nil && !job.ReadyAt.After(now) &&
		job.ExpiresAt != nil && job.ExpiresAt.After(now) && job.ArtifactBytes > 0 &&
		ValidArchiveProfilePair(ArchiveFormat(job.ArchiveFormat), job.ArchiveProfile)
}

func validReadyDeliveryArtifact(
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
	artifact model.BackupAssetExportArtifact,
	now time.Time,
) bool {
	if job.CurrentAttemptID == nil || attempt.ID != *job.CurrentAttemptID || attempt.JobID != job.ID ||
		attempt.State != string(AttemptSealed) || attempt.IsCurrent || attempt.FinishedAt == nil ||
		!validAttemptFenceDigest(attempt.FenceToken, attempt.FenceDigest) || artifact.JobID != job.ID || artifact.AttemptID != attempt.ID ||
		artifact.State != "sealed" || backupasset.ValidateOpaqueID(artifact.ID) != nil ||
		backupasset.ValidateOpaqueID(artifact.JobKeyID) != nil || artifact.SealedAt == nil ||
		artifact.ExpiresAt == nil || !artifact.ExpiresAt.After(now) || job.ExpiresAt == nil ||
		!artifact.ExpiresAt.Equal(*job.ExpiresAt) || artifact.PurgedAt != nil || artifact.PurgeError != "" ||
		artifact.CipherVersion != 1 || artifact.FormatVersion != 1 || artifact.ChunkBytes != job.ChunkBytes ||
		artifact.ChunkCount <= 0 || job.ArtifactBytes != artifact.CiphertextSize ||
		!lowerHex(artifact.ArchiveDigest, 64) || !bytes.Equal(artifact.NoncePrefix, attempt.NoncePrefix) ||
		!validStoreLocator(artifact.Locator) || !strings.HasSuffix(artifact.Locator, ".xre") {
		return false
	}
	return validCipherRangeMetadata(CipherRangeMetadata{
		ChunkBytes: artifact.ChunkBytes, ChunkCount: artifact.ChunkCount,
		PlaintextBytes: artifact.PlaintextSize, CiphertextBytes: artifact.CiphertextSize,
		PlaintextDigest: artifact.PlaintextDigest, CiphertextDigest: artifact.CiphertextDigest,
		NoncePrefix: artifact.NoncePrefix,
	}, 0, artifact.PlaintextSize)
}

func minimumDeliveryExpiry(values ...time.Time) time.Time {
	if len(values) == 0 {
		return time.Time{}
	}
	minimum := values[0].UTC()
	for _, value := range values[1:] {
		value = value.UTC()
		if value.Before(minimum) {
			minimum = value
		}
	}
	return minimum
}

func exportDeliveryBindingDigest(
	job model.BackupAssetExportJob,
	attempt model.BackupAssetExportAttempt,
	artifact model.BackupAssetExportArtifact,
	key model.BackupAssetExportKey,
) string {
	var buffer bytes.Buffer
	writeString(&buffer, "xirang.backup_asset.export.delivery_binding.v1")
	writeString(&buffer, job.ID)
	writeUint64(&buffer, uint64(job.SelectionSchemaVersion))
	writeString(&buffer, job.SelectionDigest)
	writeString(&buffer, job.ArchiveFormat)
	writeString(&buffer, job.ArchiveProfile)
	writeUint64(&buffer, uint64(job.ChunkBytes))
	writeString(&buffer, attempt.ID)
	writeString(&buffer, attempt.JobID)
	writeString(&buffer, attempt.FenceDigest)
	writeString(&buffer, hex.EncodeToString(attempt.NoncePrefix))
	writeString(&buffer, artifact.ID)
	writeString(&buffer, artifact.JobID)
	writeString(&buffer, artifact.AttemptID)
	writeString(&buffer, artifact.JobKeyID)
	writeString(&buffer, artifact.Locator)
	writeUint64(&buffer, uint64(artifact.CipherVersion))
	writeUint64(&buffer, uint64(artifact.FormatVersion))
	writeString(&buffer, hex.EncodeToString(artifact.NoncePrefix))
	writeUint64(&buffer, uint64(artifact.ChunkCount))
	writeUint64(&buffer, uint64(artifact.ChunkBytes))
	writeUint64(&buffer, uint64(artifact.PlaintextSize))
	writeUint64(&buffer, uint64(artifact.CiphertextSize))
	writeString(&buffer, artifact.PlaintextDigest)
	writeString(&buffer, artifact.ArchiveDigest)
	writeString(&buffer, artifact.CiphertextDigest)
	writeString(&buffer, key.ID)
	writeString(&buffer, key.JobID)
	writeUint64(&buffer, uint64(key.KeyRevision))
	writeUint64(&buffer, uint64(key.KEKVersion))
	writeString(&buffer, key.WrapAlgorithm)
	writeString(&buffer, hex.EncodeToString(key.EnvelopeNonce))
	writeString(&buffer, hex.EncodeToString(key.WrappedDEK))
	digest := sha256.Sum256(buffer.Bytes())
	return hex.EncodeToString(digest[:])
}

func exportDeliveryContentType(format string, profile string) string {
	contentType, _ := exportDeliveryMetadata(format, profile)
	return contentType
}

func exportDeliveryMetadata(format string, profile string) (string, string) {
	if !ValidArchiveProfilePair(ArchiveFormat(format), profile) {
		return "", ""
	}
	switch profile {
	case ArchiveProfileZIPDeflateV1:
		return "application/zip", ".zip"
	case ArchiveProfileTARNoneV1:
		return "application/x-tar", ".tar"
	case ArchiveProfileTARGzipV1:
		return "application/gzip", ".tar.gz"
	default:
		return "", ""
	}
}

func validExportGatewayRequest(request content.GatewayRequest) bool {
	if backupasset.ValidateOpaqueID(request.DeliveryID) != nil ||
		(request.Method != http.MethodGet && request.Method != http.MethodHead) || request.RawCookie == "" ||
		len(request.RangeHeaders) > 2 || len(request.IfRangeHeaders) > 2 {
		return false
	}
	for _, values := range [][]string{request.RangeHeaders, request.IfRangeHeaders} {
		for _, value := range values {
			if len(value) > 1024 {
				return false
			}
		}
	}
	return true
}

func exportDeliveryReservationBytes(artifact model.BackupAssetExportArtifact, method string) (int64, error) {
	if method == http.MethodHead {
		return 0, nil
	}
	if method != http.MethodGet || artifact.CiphertextSize <= 0 || artifact.CiphertextSize > math.MaxInt64/2 {
		return 0, ErrDeliveryBudgetExceeded
	}
	return artifact.CiphertextSize * 2, nil
}

func validActiveExportDeliveryGrant(
	grant model.BackupAssetExportDeliveryGrant,
	deliveryID string,
	secret string,
	now time.Time,
) bool {
	wantPath := "/api/v1/asset-content/" + deliveryID
	return backupasset.ValidateOpaqueID(grant.ID) == nil && grant.DeliveryID == deliveryID &&
		grant.ResourceKind == "export_archive" && grant.ExportJobID != nil && grant.ExportArtifactID != nil &&
		grant.ExportAttemptID != nil && grant.JobKeyID != nil && grant.MemberRequestID == nil &&
		grant.OuterRecoveryPointID == "" && grant.OuterEntryID == "" && grant.OuterSourceFingerprint == "" &&
		grant.OuterEntryFingerprint == "" && grant.MemberChainDigest == "" && grant.ProcessingJobID == nil &&
		grant.ProcessingAttemptID == nil && grant.DerivedArtifactSetID == nil && grant.DerivedArtifactID == nil &&
		grant.DerivedBlobID == nil && grant.DerivedDigest == "" && grant.DerivedSize == 0 &&
		lowerHex(grant.ExportFenceDigest, 64) && lowerHex(grant.SelectionDigest, 64) &&
		lowerHex(grant.ArtifactDigest, 64) && grant.PlaintextSize >= 0 && grant.CiphertextSize > 0 &&
		grant.FormatVersion > 0 && grant.ChunkBytes > 0 && grant.JobKeyVersion > 0 &&
		grant.OwnerUserID > 0 && backupasset.ValidateOpaqueID(grant.SessionJTI) == nil &&
		grant.TokenVersion >= 0 && grant.RoleRevision == grant.TokenVersion &&
		grant.ProofAction == "asset.export_download" && backupasset.ValidateOpaqueID(grant.ProofID) == nil &&
		grant.ProofExpiresAt.After(now) && content.VerifyCookieSecret(grant.CookieSecretHash, secret) &&
		grant.Action == "export_download" && grant.CanonicalPath == wantPath &&
		grant.MethodPolicy == string(content.MethodGetHead) && grant.RangePolicy == string(content.RangeSingle) &&
		grant.State == "active" && now.Before(grant.IdleExpiresAt.UTC()) &&
		now.Before(grant.AbsoluteExpiresAt.UTC()) && !grant.AbsoluteExpiresAt.After(grant.ProofExpiresAt.UTC()) &&
		grant.MaxRequests > 0 && grant.MaxCumulativeBytes > 0 && grant.MaxInFlight > 0 &&
		grant.RequestCount >= 0 && grant.RequestCount <= grant.MaxRequests && grant.ReservedBytes >= 0 &&
		grant.ConsumedBytes >= 0 && grant.ConsumedBytes <= grant.MaxCumulativeBytes && grant.InFlight >= 0 &&
		grant.InFlight <= grant.MaxInFlight &&
		withinDeliveryByteLimit(grant.ConsumedBytes, grant.ReservedBytes, 0, grant.MaxCumulativeBytes)
}

func validActiveArchiveMemberDeliveryGrant(
	grant model.BackupAssetExportDeliveryGrant,
	deliveryID string,
	secret string,
	now time.Time,
) bool {
	wantPath := "/api/v1/asset-content/" + deliveryID
	ref := backupasset.AssetRef{RecoveryPointID: grant.OuterRecoveryPointID, EntryID: grant.OuterEntryID}
	return backupasset.ValidateOpaqueID(grant.ID) == nil && grant.DeliveryID == deliveryID &&
		grant.ResourceKind == "archive_member" && grant.ExportJobID == nil && grant.ExportArtifactID == nil &&
		grant.ExportAttemptID == nil && grant.JobKeyID == nil && grant.ExportFenceDigest == "" &&
		grant.SelectionDigest == "" && grant.ArtifactDigest == "" && grant.PlaintextSize == 0 &&
		grant.CiphertextSize == 0 && grant.FormatVersion == 0 && grant.ChunkBytes == 0 &&
		grant.JobKeyVersion == 0 && grant.MemberRequestID != nil &&
		backupasset.ValidateOpaqueID(*grant.MemberRequestID) == nil && backupasset.ValidateAssetRef(ref) == nil &&
		grant.OuterSourceFingerprint != "" && len(grant.OuterSourceFingerprint) <= 128 &&
		grant.OuterEntryFingerprint != "" && len(grant.OuterEntryFingerprint) <= 128 &&
		lowerHex(grant.MemberChainDigest, 64) && grant.ProcessingJobID != nil &&
		backupasset.ValidateOpaqueID(*grant.ProcessingJobID) == nil && grant.ProcessingAttemptID != nil &&
		backupasset.ValidateOpaqueID(*grant.ProcessingAttemptID) == nil && grant.DerivedArtifactSetID != nil &&
		backupasset.ValidateOpaqueID(*grant.DerivedArtifactSetID) == nil && grant.DerivedArtifactID != nil &&
		backupasset.ValidateOpaqueID(*grant.DerivedArtifactID) == nil && grant.DerivedBlobID != nil &&
		backupasset.ValidateOpaqueID(*grant.DerivedBlobID) == nil && lowerHex(grant.DerivedDigest, 64) &&
		grant.DerivedSize >= 0 && grant.DerivedSize <= 256<<20 && grant.OwnerUserID > 0 &&
		backupasset.ValidateOpaqueID(grant.SessionJTI) == nil && grant.TokenVersion >= 0 &&
		grant.RoleRevision == grant.TokenVersion && grant.ProofAction == "asset.download" &&
		backupasset.ValidateOpaqueID(grant.ProofID) == nil && grant.ProofExpiresAt.After(now) &&
		content.VerifyCookieSecret(grant.CookieSecretHash, secret) && grant.Action == "archive_member_download" &&
		grant.CanonicalPath == wantPath && grant.MethodPolicy == string(content.MethodGetHead) &&
		grant.RangePolicy == string(content.RangeNone) && grant.State == "active" &&
		now.Before(grant.IdleExpiresAt.UTC()) && now.Before(grant.AbsoluteExpiresAt.UTC()) &&
		!grant.AbsoluteExpiresAt.After(grant.ProofExpiresAt.UTC()) && grant.MaxRequests > 0 &&
		grant.MaxCumulativeBytes > 0 && grant.MaxInFlight > 0 && grant.RequestCount >= 0 &&
		grant.RequestCount <= grant.MaxRequests && grant.ReservedBytes >= 0 && grant.ConsumedBytes >= 0 &&
		grant.ConsumedBytes <= grant.MaxCumulativeBytes && grant.InFlight >= 0 && grant.InFlight <= grant.MaxInFlight &&
		withinDeliveryByteLimit(grant.ConsumedBytes, grant.ReservedBytes, 0, grant.MaxCumulativeBytes)
}

func archiveMemberGrantMatchesAsset(
	grant model.BackupAssetExportDeliveryGrant,
	asset content.AuthorizedAsset,
) bool {
	return validArchiveMemberDeliveryAsset(asset) && asset.Ref.RecoveryPointID == grant.OuterRecoveryPointID &&
		asset.Ref.EntryID == grant.OuterEntryID && asset.SourceFingerprint == grant.OuterSourceFingerprint &&
		asset.EntryFingerprint == grant.OuterEntryFingerprint
}

func archiveMemberDeliveryBindingMatches(binding archiveMemberDeliveryBinding, now time.Time) bool {
	grant, asset, member := binding.grant, binding.asset, binding.member
	if grant.MemberRequestID == nil || grant.ProcessingJobID == nil || grant.ProcessingAttemptID == nil ||
		grant.DerivedArtifactSetID == nil || grant.DerivedArtifactID == nil || grant.DerivedBlobID == nil {
		return false
	}
	request := ArchiveMemberDeliveryIssueRequest{
		Actor: content.DeliveryActor{UserID: grant.OwnerUserID, Role: "admin"},
		Asset: asset, MemberRequestID: *grant.MemberRequestID,
	}
	return archiveMemberGrantMatchesAsset(grant, asset) && validArchiveMemberDeliveryBinding(request, member, now) &&
		member.MemberRequestID == *grant.MemberRequestID && member.MemberChainDigest == grant.MemberChainDigest &&
		member.ProcessingJobID == *grant.ProcessingJobID && member.ProcessingAttemptID == *grant.ProcessingAttemptID &&
		member.DerivedArtifactSetID == *grant.DerivedArtifactSetID &&
		member.DerivedArtifactID == *grant.DerivedArtifactID && member.DerivedBlobID == *grant.DerivedBlobID &&
		member.DerivedDigest == grant.DerivedDigest && member.DerivedSize == grant.DerivedSize &&
		!grant.AbsoluteExpiresAt.After(member.AbsoluteExpiresAt.UTC())
}

func sameArchiveMemberDeliveryBinding(left, right archiveMemberDeliveryBinding) bool {
	return sameArchiveMemberDeliveryGrantBinding(left.grant, right.grant) &&
		sameArchiveMemberDeliveryAsset(left.asset, right.asset) && left.member == right.member
}

// The delivery grant's counters and audit fields change while a request is in
// flight. Everything below freezes the authorization and artifact tuple that
// was admitted before reserving that request.
func sameArchiveMemberDeliveryGrantBinding(left, right model.BackupAssetExportDeliveryGrant) bool {
	return left.ID == right.ID && left.DeliveryID == right.DeliveryID &&
		left.ResourceKind == right.ResourceKind &&
		sameExportDeliveryBindingID(left.ExportJobID, right.ExportJobID) &&
		sameExportDeliveryBindingID(left.ExportArtifactID, right.ExportArtifactID) &&
		sameExportDeliveryBindingID(left.ExportAttemptID, right.ExportAttemptID) &&
		left.ExportFenceDigest == right.ExportFenceDigest && left.SelectionDigest == right.SelectionDigest &&
		left.ArtifactDigest == right.ArtifactDigest && left.PlaintextSize == right.PlaintextSize &&
		left.CiphertextSize == right.CiphertextSize && left.FormatVersion == right.FormatVersion &&
		left.ChunkBytes == right.ChunkBytes && sameExportDeliveryBindingID(left.JobKeyID, right.JobKeyID) &&
		left.JobKeyVersion == right.JobKeyVersion &&
		sameExportDeliveryBindingID(left.MemberRequestID, right.MemberRequestID) &&
		left.OuterRecoveryPointID == right.OuterRecoveryPointID && left.OuterEntryID == right.OuterEntryID &&
		left.OuterSourceFingerprint == right.OuterSourceFingerprint &&
		left.OuterEntryFingerprint == right.OuterEntryFingerprint &&
		left.MemberChainDigest == right.MemberChainDigest &&
		sameExportDeliveryBindingID(left.ProcessingJobID, right.ProcessingJobID) &&
		sameExportDeliveryBindingID(left.ProcessingAttemptID, right.ProcessingAttemptID) &&
		sameExportDeliveryBindingID(left.DerivedArtifactSetID, right.DerivedArtifactSetID) &&
		sameExportDeliveryBindingID(left.DerivedArtifactID, right.DerivedArtifactID) &&
		sameExportDeliveryBindingID(left.DerivedBlobID, right.DerivedBlobID) &&
		left.DerivedDigest == right.DerivedDigest && left.DerivedSize == right.DerivedSize &&
		left.OwnerUserID == right.OwnerUserID && left.SessionJTI == right.SessionJTI &&
		left.TokenVersion == right.TokenVersion && left.RoleRevision == right.RoleRevision &&
		left.ProofAction == right.ProofAction && left.ProofID == right.ProofID &&
		left.ProofExpiresAt.Equal(right.ProofExpiresAt) &&
		left.CookieSecretHash == right.CookieSecretHash && left.Action == right.Action &&
		left.CanonicalPath == right.CanonicalPath && left.MethodPolicy == right.MethodPolicy &&
		left.RangePolicy == right.RangePolicy && left.State == right.State &&
		left.IdleExpiresAt.Equal(right.IdleExpiresAt) && left.AbsoluteExpiresAt.Equal(right.AbsoluteExpiresAt) &&
		left.MaxRequests == right.MaxRequests && left.MaxCumulativeBytes == right.MaxCumulativeBytes &&
		left.MaxInFlight == right.MaxInFlight
}

func sameExportDeliveryBinding(left, right exportDeliveryBinding) bool {
	leftGrant, rightGrant := left.grant, right.grant
	return leftGrant.ID == rightGrant.ID && leftGrant.DeliveryID == rightGrant.DeliveryID &&
		leftGrant.ResourceKind == rightGrant.ResourceKind &&
		sameExportDeliveryBindingID(leftGrant.ExportJobID, rightGrant.ExportJobID) &&
		sameExportDeliveryBindingID(leftGrant.ExportArtifactID, rightGrant.ExportArtifactID) &&
		sameExportDeliveryBindingID(leftGrant.ExportAttemptID, rightGrant.ExportAttemptID) &&
		sameExportDeliveryBindingID(leftGrant.JobKeyID, rightGrant.JobKeyID) &&
		leftGrant.ExportFenceDigest == rightGrant.ExportFenceDigest &&
		leftGrant.SelectionDigest == rightGrant.SelectionDigest && leftGrant.ArtifactDigest == rightGrant.ArtifactDigest &&
		leftGrant.PlaintextSize == rightGrant.PlaintextSize && leftGrant.CiphertextSize == rightGrant.CiphertextSize &&
		leftGrant.FormatVersion == rightGrant.FormatVersion && leftGrant.ChunkBytes == rightGrant.ChunkBytes &&
		leftGrant.JobKeyVersion == rightGrant.JobKeyVersion && leftGrant.OwnerUserID == rightGrant.OwnerUserID &&
		leftGrant.SessionJTI == rightGrant.SessionJTI && leftGrant.TokenVersion == rightGrant.TokenVersion &&
		leftGrant.RoleRevision == rightGrant.RoleRevision && leftGrant.ProofAction == rightGrant.ProofAction &&
		leftGrant.ProofID == rightGrant.ProofID && leftGrant.ProofExpiresAt.Equal(rightGrant.ProofExpiresAt) &&
		leftGrant.CookieSecretHash == rightGrant.CookieSecretHash && leftGrant.Action == rightGrant.Action &&
		leftGrant.CanonicalPath == rightGrant.CanonicalPath && leftGrant.MethodPolicy == rightGrant.MethodPolicy &&
		leftGrant.RangePolicy == rightGrant.RangePolicy && leftGrant.IdleExpiresAt.Equal(rightGrant.IdleExpiresAt) &&
		leftGrant.AbsoluteExpiresAt.Equal(rightGrant.AbsoluteExpiresAt) &&
		leftGrant.MaxRequests == rightGrant.MaxRequests &&
		leftGrant.MaxCumulativeBytes == rightGrant.MaxCumulativeBytes && leftGrant.MaxInFlight == rightGrant.MaxInFlight &&
		exportDeliveryBindingDigest(left.job, left.attempt, left.artifact, left.key) ==
			exportDeliveryBindingDigest(right.job, right.attempt, right.artifact, right.key)
}

func sameExportDeliveryBindingID(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func exportDeliveryBindingMatches(binding exportDeliveryBinding, now time.Time) bool {
	grant, job, attempt, artifact, key := binding.grant, binding.job, binding.attempt, binding.artifact, binding.key
	return validReadyDeliveryJob(job, now) && validReadyDeliveryArtifact(job, attempt, artifact, now) &&
		grant.ExportJobID != nil && *grant.ExportJobID == job.ID && job.OwnerUserID == grant.OwnerUserID &&
		grant.ExportArtifactID != nil && *grant.ExportArtifactID == artifact.ID &&
		grant.ExportAttemptID != nil && *grant.ExportAttemptID == attempt.ID &&
		grant.JobKeyID != nil && *grant.JobKeyID == key.ID && key.JobID == job.ID && key.State == "active" &&
		key.KeyRevision > 0 && key.KeyRevision <= int64(math.MaxInt) && grant.JobKeyVersion == int(key.KeyRevision) &&
		key.KEKVersion > 0 && len(key.WrappedDEK) > 0 && len(key.EnvelopeNonce) > 0 &&
		grant.ExportFenceDigest == attempt.FenceDigest && grant.SelectionDigest == job.SelectionDigest &&
		grant.ArtifactDigest == exportDeliveryBindingDigest(job, attempt, artifact, key) &&
		grant.PlaintextSize == artifact.PlaintextSize && grant.CiphertextSize == artifact.CiphertextSize &&
		grant.FormatVersion == artifact.FormatVersion && grant.ChunkBytes == artifact.ChunkBytes &&
		job.ExpiresAt != nil && artifact.ExpiresAt != nil &&
		!grant.AbsoluteExpiresAt.After(job.ExpiresAt.UTC()) &&
		!grant.AbsoluteExpiresAt.After(artifact.ExpiresAt.UTC())
}

func exportDeliveryHeaders(
	binding exportDeliveryBinding,
	plan content.RepresentationPlan,
	etag string,
) http.Header {
	header := make(http.Header)
	contentType, extension := exportDeliveryMetadata(binding.job.ArchiveFormat, binding.job.ArchiveProfile)
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.FormatInt(plan.ContentLength, 10))
	header.Set("ETag", etag)
	header.Set("Accept-Ranges", plan.AcceptRanges)
	if plan.ContentRange != "" {
		header.Set("Content-Range", plan.ContentRange)
	}
	header.Set("Content-Disposition", `attachment; filename="xirang-export-`+binding.job.ID[:16]+extension+`"`)
	return header
}

func archiveMemberDeliveryHeaders(
	binding archiveMemberDeliveryBinding,
	plan content.RepresentationPlan,
	etag string,
) http.Header {
	header := make(http.Header)
	header.Set("Content-Type", binding.member.MediaType)
	header.Set("Content-Length", strconv.FormatInt(plan.ContentLength, 10))
	header.Set("ETag", etag)
	header.Set("Accept-Ranges", plan.AcceptRanges)
	if plan.ContentRange != "" {
		header.Set("Content-Range", plan.ContentRange)
	}
	header.Set("Content-Disposition", `attachment; filename="xirang-archive-member.bin"`)
	return header
}

func nonNilDeliveryContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
