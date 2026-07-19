package processing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrProtocolInvalid               = errors.New("invalid Worker protocol contract")
	ErrProtocolCapabilityUnsupported = errors.New("worker capability is not registered")
	ErrProtocolIncompatible          = errors.New("worker protocol version is incompatible")
	ErrProtocolUnavailable           = errors.New("worker protocol admission is unavailable")
	ErrWorkerQuarantined             = errors.New("worker identity is quarantined")
)

const WorkerProtocolVersion = 1

type ProtocolInputMode string

const (
	ProtocolInputStat       ProtocolInputMode = "stat"
	ProtocolInputSequential ProtocolInputMode = "sequential"
	ProtocolInputRange      ProtocolInputMode = "range"
)

type ProtocolCapabilityLimits struct {
	MaxInputBytes     int64 `json:"max_input_bytes"`
	MaxOutputBytes    int64 `json:"max_output_bytes"`
	MaxOutputCount    int   `json:"max_output_count"`
	MaxPages          int64 `json:"max_pages"`
	MaxPixels         int64 `json:"max_pixels"`
	MaxDurationMillis int64 `json:"max_duration_millis"`
	MaxExpandedBytes  int64 `json:"max_expanded_bytes"`
}

type CapabilityAdvertisement struct {
	SchemaVersion       int                      `json:"schema_version"`
	Capability          string                   `json:"capability"`
	CapabilitySchema    string                   `json:"capability_schema"`
	PipelineFingerprint string                   `json:"pipeline_fingerprint"`
	OutputProfile       string                   `json:"output_profile"`
	InputModes          []ProtocolInputMode      `json:"input_modes"`
	Limits              ProtocolCapabilityLimits `json:"limits"`
}

type HandshakeRequest struct {
	SchemaVersion    int                       `json:"schema_version"`
	ProtocolVersion  int                       `json:"protocol_version"`
	InstanceID       string                    `json:"instance_id"`
	IdentityRevision int64                     `json:"identity_revision"`
	InteractiveSlots int                       `json:"interactive_slots"`
	BackgroundSlots  int                       `json:"background_slots"`
	Capabilities     []CapabilityAdvertisement `json:"capabilities"`
}

type CapabilityDefinition struct {
	Capability          string
	CapabilitySchema    string
	PipelineFingerprint string
	OutputProfile       string
}

type CapabilityRegistry struct {
	definitions map[string]CapabilityDefinition
}

type ValidatedCapability struct {
	Capability          string
	CapabilitySchema    string
	PipelineFingerprint string
	OutputProfile       string
	InputModes          []ProtocolInputMode
	Limits              ProtocolCapabilityLimits
	AdvertisementDigest string
}

type ValidatedHandshake struct {
	TransportKind        WorkerTransportKind
	TransportFingerprint string
	TransportWorkerID    string
	InstanceID           string
	IdentityRevision     int64
	ProtocolVersion      int
	InteractiveSlots     int
	BackgroundSlots      int
	Capabilities         []ValidatedCapability
}

type HandshakeResult struct {
	WorkerID        string `json:"worker_id"`
	TrustState      string `json:"trust_state"`
	HealthState     string `json:"health_state"`
	CapabilityCount int    `json:"capability_count"`
}

type WorkerPullRequest struct {
	SchemaVersion int    `json:"schema_version"`
	WorkerID      string `json:"worker_id"`
	InstanceID    string `json:"instance_id"`
}

type WorkerActivationMaterial struct {
	GrantID string `json:"grant_id"`
	Secret  string `json:"secret"`
}

type WorkerRecoveryPointFence struct {
	LeaseID         string `json:"lease_id"`
	RecoveryPointID string `json:"recovery_point_id"`
	LeaseAttemptID  string `json:"lease_attempt_id"`
	FenceToken      string `json:"fence_token"`
}

type WorkerJobEnvelope struct {
	SchemaVersion               int                      `json:"schema_version"`
	ProtocolVersion             int                      `json:"protocol_version"`
	JobID                       string                   `json:"job_id"`
	AttemptID                   string                   `json:"attempt_id"`
	SlotClass                   SlotClass                `json:"slot_class"`
	TransitionRevision          int64                    `json:"transition_revision"`
	Descriptor                  WorkDescriptorV1         `json:"descriptor"`
	WorkerLeaseExpiresAt        time.Time                `json:"worker_lease_expires_at"`
	RecoveryPointLeaseExpiresAt time.Time                `json:"recovery_point_lease_expires_at"`
	EffectiveLeaseExpiresAt     time.Time                `json:"effective_lease_expires_at"`
	AbsoluteDeadline            time.Time                `json:"absolute_deadline"`
	RecoveryPointFence          WorkerRecoveryPointFence `json:"recovery_point_fence"`
	InputActivation             WorkerActivationMaterial `json:"input_activation"`
	SinkActivation              WorkerActivationMaterial `json:"sink_activation"`
}

type WorkerInputActivateRequest struct {
	SchemaVersion    int    `json:"schema_version"`
	WorkerID         string `json:"worker_id"`
	InstanceID       string `json:"instance_id"`
	JobID            string `json:"job_id"`
	AttemptID        string `json:"attempt_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	GrantID          string `json:"grant_id"`
	Secret           string `json:"secret"`
}

type WorkerInputSourceInfo struct {
	Size              int64  `json:"size"`
	MediaType         string `json:"media_type"`
	FingerprintStrong bool   `json:"fingerprint_strong"`
	Sequential        bool   `json:"sequential"`
	Range             bool   `json:"range"`
}

type WorkerInputActivation struct {
	SchemaVersion      int                   `json:"schema_version"`
	SessionID          string                `json:"session_id"`
	TransitionRevision int64                 `json:"transition_revision"`
	ExpiresAt          time.Time             `json:"expires_at"`
	Source             WorkerInputSourceInfo `json:"source"`
}

type WorkerInputReadRequest struct {
	SchemaVersion int                `json:"schema_version"`
	WorkerID      string             `json:"worker_id"`
	InstanceID    string             `json:"instance_id"`
	SessionID     string             `json:"session_id"`
	Mode          content.SourceMode `json:"mode"`
	Offset        int64              `json:"offset"`
	Length        int64              `json:"length"`
}

type WorkerHeartbeatRequest struct {
	SchemaVersion int    `json:"schema_version"`
	WorkerID      string `json:"worker_id"`
	InstanceID    string `json:"instance_id"`
	AttemptID     string `json:"attempt_id"`
}

type WorkerHeartbeatResult struct {
	SchemaVersion               int          `json:"schema_version"`
	WorkerLeaseExpiresAt        time.Time    `json:"worker_lease_expires_at"`
	RecoveryPointLeaseExpiresAt time.Time    `json:"recovery_point_lease_expires_at"`
	EffectiveLeaseExpiresAt     time.Time    `json:"effective_lease_expires_at"`
	TransitionRevision          int64        `json:"transition_revision"`
	CancelRequested             bool         `json:"cancel_requested"`
	CancelReason                CancelReason `json:"cancel_reason"`
	WorkerDraining              bool         `json:"worker_draining"`
}

type WorkerTransitionRequest struct {
	SchemaVersion    int                 `json:"schema_version"`
	WorkerID         string              `json:"worker_id"`
	InstanceID       string              `json:"instance_id"`
	JobID            string              `json:"job_id"`
	AttemptID        string              `json:"attempt_id"`
	ExpectedRevision int64               `json:"expected_revision"`
	To               ProcessingState     `json:"to"`
	ErrorCode        ProcessingErrorCode `json:"error_code"`
	RetryAt          *time.Time          `json:"retry_at"`
	CancelReason     CancelReason        `json:"cancel_reason"`
	SupersedeReason  SupersedeReason     `json:"supersede_reason"`
	ExpiryReason     ExpiryReason        `json:"expiry_reason"`
}

type WorkerTransitionResult struct {
	SchemaVersion   int             `json:"schema_version"`
	State           ProcessingState `json:"state"`
	Revision        int64           `json:"revision"`
	CancelRequested bool            `json:"cancel_requested"`
}

type WorkerDrainRequest struct {
	SchemaVersion int    `json:"schema_version"`
	WorkerID      string `json:"worker_id"`
	InstanceID    string `json:"instance_id"`
}

type WorkerSinkActivateRequest struct {
	SchemaVersion    int    `json:"schema_version"`
	WorkerID         string `json:"worker_id"`
	InstanceID       string `json:"instance_id"`
	JobID            string `json:"job_id"`
	AttemptID        string `json:"attempt_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	GrantID          string `json:"grant_id"`
	Secret           string `json:"secret"`
}

type WorkerSinkActivation struct {
	SchemaVersion    int       `json:"schema_version"`
	SessionID        string    `json:"session_id"`
	ExpiresAt        time.Time `json:"expires_at"`
	MaxArtifacts     int       `json:"max_artifacts"`
	MaxArtifactBytes int64     `json:"max_artifact_bytes"`
	MaxTotalBytes    int64     `json:"max_total_bytes"`
	MaxInFlight      int64     `json:"max_in_flight"`
}

type WorkerUploadArtifactRequest struct {
	SchemaVersion int                 `json:"schema_version"`
	WorkerID      string              `json:"worker_id"`
	InstanceID    string              `json:"instance_id"`
	SessionID     string              `json:"session_id"`
	JobID         string              `json:"job_id"`
	AttemptID     string              `json:"attempt_id"`
	Artifact      ArtifactDeclaration `json:"artifact"`
}

type WorkerCommitManifestRequest struct {
	SchemaVersion          int                   `json:"schema_version"`
	WorkerID               string                `json:"worker_id"`
	InstanceID             string                `json:"instance_id"`
	SessionID              string                `json:"session_id"`
	JobID                  string                `json:"job_id"`
	AttemptID              string                `json:"attempt_id"`
	SecurityPolicyRevision string                `json:"security_policy_revision"`
	Artifacts              []ArtifactDeclaration `json:"artifacts"`
}

type WorkerCommitManifestResult struct {
	SchemaVersion      int    `json:"schema_version"`
	ArtifactSetID      string `json:"artifact_set_id"`
	ManifestDigest     string `json:"manifest_digest"`
	ProjectionRequired bool   `json:"projection_required"`
}

type WorkerInputBroker interface {
	OpenSession(context.Context, content.AttemptSourceBinding) (content.AttemptInputSession, content.AttemptSourceInfo, error)
}

type WorkerArtifactSink interface {
	UploadArtifact(context.Context, UploadArtifactRequest, io.Reader) (UploadedArtifact, error)
	CommitManifest(context.Context, CommitManifestRequest) (CommitManifestResult, error)
}

type WorkerProtocolService struct {
	protocol    *ProtocolService
	coordinator *Coordinator
	grants      *GrantService
	input       WorkerInputBroker
	sink        WorkerArtifactSink

	inputMu       sync.Mutex
	inputSessions map[string]workerInputSession
	sinkMu        sync.Mutex
	sinkSessions  map[string]workerSinkSession
	accepting     atomic.Bool
	closed        atomic.Bool
	lifecycleMu   sync.Mutex
	inFlight      int
	idle          chan struct{}
}

type workerInputSession struct {
	session    content.AttemptInputSession
	workerID   string
	instanceID string
	jobID      string
	attemptID  string
	expiresAt  time.Time
}

type workerSinkSession struct {
	workerID               string
	instanceID             string
	jobID                  string
	attemptID              string
	expiresAt              time.Time
	recoveryPointFence     backupasset.LeaseFence
	securityPolicyRevision string
}

func NewWorkerProtocolService(
	protocol *ProtocolService,
	coordinator *Coordinator,
	grants *GrantService,
	input WorkerInputBroker,
	sink WorkerArtifactSink,
) (*WorkerProtocolService, error) {
	if protocol == nil || coordinator == nil || grants == nil || input == nil || sink == nil ||
		protocol.db != coordinator.db || grants.db != coordinator.db || grants.leaseService != coordinator.leaseService {
		return nil, ErrProtocolInvalid
	}
	service := &WorkerProtocolService{
		protocol: protocol, coordinator: coordinator, grants: grants, input: input, sink: sink,
		inputSessions: make(map[string]workerInputSession),
		sinkSessions:  make(map[string]workerSinkSession),
	}
	service.accepting.Store(true)
	return service, nil
}

func (service *WorkerProtocolService) Pull(ctx context.Context, identity WorkerTransportIdentity, request WorkerPullRequest) (WorkerJobEnvelope, error) {
	if service == nil {
		return WorkerJobEnvelope{}, ErrProtocolInvalid
	}
	if err := service.beginCall(true); err != nil {
		return WorkerJobEnvelope{}, err
	}
	defer service.endCall()
	if request.SchemaVersion != 1 || !lowerHex(request.WorkerID, 32) || !lowerHex(request.InstanceID, 32) {
		return WorkerJobEnvelope{}, ErrProtocolInvalid
	}
	if err := service.protocol.AuthenticateWorker(ctx, identity, request.WorkerID, request.InstanceID); err != nil {
		return WorkerJobEnvelope{}, err
	}
	leased, err := service.coordinator.PullAttempt(ctx, PullRequest{WorkerID: request.WorkerID}, service.grants)
	if err != nil {
		return WorkerJobEnvelope{}, err
	}
	descriptor, err := DecodeWorkDescriptorV1(leased.Lease.DescriptorCanonical)
	if err != nil {
		return WorkerJobEnvelope{}, errors.Join(ErrProtocolInvalid, err)
	}
	var job model.BackupAssetProcessingJob
	result := service.coordinator.db.WithContext(ctx).
		Where("id = ? AND current_attempt_id = ? AND state = ? AND is_current = ?", leased.Lease.JobID, leased.Lease.AttemptID, ProcessingLeased, true).
		Limit(1).Find(&job)
	if result.Error != nil {
		return WorkerJobEnvelope{}, fmt.Errorf("load pulled Worker envelope: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return WorkerJobEnvelope{}, ErrAttemptLost
	}
	effectiveDeadline := minimumTime(leased.Lease.WorkerLeaseExpiresAt, leased.Lease.RecoveryPointLeaseExpiresAt)
	return WorkerJobEnvelope{
		SchemaVersion: 1, ProtocolVersion: WorkerProtocolVersion,
		JobID: leased.Lease.JobID, AttemptID: leased.Lease.AttemptID, SlotClass: leased.Lease.SlotClass,
		TransitionRevision: job.TransitionRevision, Descriptor: descriptor,
		WorkerLeaseExpiresAt:        leased.Lease.WorkerLeaseExpiresAt.UTC(),
		RecoveryPointLeaseExpiresAt: leased.Lease.RecoveryPointLeaseExpiresAt.UTC(),
		EffectiveLeaseExpiresAt:     effectiveDeadline.UTC(), AbsoluteDeadline: job.AbsoluteDeadline.UTC(),
		RecoveryPointFence: WorkerRecoveryPointFence{
			LeaseID:         leased.Lease.RecoveryPointFence.LeaseID,
			RecoveryPointID: leased.Lease.RecoveryPointFence.RecoveryPointID,
			LeaseAttemptID:  leased.Lease.RecoveryPointFence.AttemptID,
			FenceToken:      leased.Lease.RecoveryPointFence.FenceToken,
		},
		InputActivation: WorkerActivationMaterial{
			GrantID: leased.Grants.Input.GrantID, Secret: leased.Grants.Input.Secret,
		},
		SinkActivation: WorkerActivationMaterial{
			GrantID: leased.Grants.Sink.GrantID, Secret: leased.Grants.Sink.Secret,
		},
	}, nil
}

func (service *WorkerProtocolService) Handshake(ctx context.Context, identity WorkerTransportIdentity, request HandshakeRequest) (HandshakeResult, error) {
	if service == nil {
		return HandshakeResult{}, ErrProtocolInvalid
	}
	if err := service.beginCall(true); err != nil {
		return HandshakeResult{}, err
	}
	defer service.endCall()
	return service.protocol.Handshake(ctx, identity, request)
}

func (service *WorkerProtocolService) StopAccepting() {
	if service == nil {
		return
	}
	service.lifecycleMu.Lock()
	service.accepting.Store(false)
	service.lifecycleMu.Unlock()
}

func (service *WorkerProtocolService) Shutdown(ctx context.Context) error {
	if service == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	service.StopAccepting()
	service.lifecycleMu.Lock()
	service.closed.Store(true)
	var idle <-chan struct{}
	if service.inFlight > 0 {
		if service.idle == nil {
			service.idle = make(chan struct{})
		}
		idle = service.idle
	}
	service.lifecycleMu.Unlock()
	if idle != nil {
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	service.inputMu.Lock()
	sessions := make([]content.AttemptInputSession, 0, len(service.inputSessions))
	for sessionID, owned := range service.inputSessions {
		delete(service.inputSessions, sessionID)
		if owned.session != nil {
			sessions = append(sessions, owned.session)
		}
	}
	service.inputMu.Unlock()

	service.sinkMu.Lock()
	clear(service.sinkSessions)
	service.sinkMu.Unlock()

	closeErrors := make([]error, 0, len(sessions))
	for _, session := range sessions {
		if err := session.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

func (service *WorkerProtocolService) beginCall(admission bool) error {
	service.lifecycleMu.Lock()
	defer service.lifecycleMu.Unlock()
	if service.closed.Load() || (admission && !service.accepting.Load()) {
		return ErrProtocolUnavailable
	}
	service.inFlight++
	return nil
}

func (service *WorkerProtocolService) endCall() {
	service.lifecycleMu.Lock()
	service.inFlight--
	if service.inFlight == 0 && service.idle != nil {
		close(service.idle)
		service.idle = nil
	}
	service.lifecycleMu.Unlock()
}

func (service *WorkerProtocolService) ActivateInput(ctx context.Context, identity WorkerTransportIdentity, request WorkerInputActivateRequest) (WorkerInputActivation, error) {
	if service == nil {
		return WorkerInputActivation{}, ErrProtocolInvalid
	}
	if err := service.beginCall(false); err != nil {
		return WorkerInputActivation{}, err
	}
	defer service.endCall()
	if request.SchemaVersion != 1 || !lowerHex(request.WorkerID, 32) || !lowerHex(request.InstanceID, 32) ||
		!lowerHex(request.JobID, 32) || !lowerHex(request.AttemptID, 32) || !lowerHex(request.GrantID, 32) ||
		request.ExpectedRevision <= 0 || request.Secret == "" {
		return WorkerInputActivation{}, ErrProtocolInvalid
	}
	if err := service.protocol.AuthenticateWorker(ctx, identity, request.WorkerID, request.InstanceID); err != nil {
		return WorkerInputActivation{}, err
	}

	var activated ActivatedGrant
	var binding content.AttemptSourceBinding
	var transitionRevision int64
	err := service.coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetProcessingJob
		result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", request.JobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load input activation job: %w", result.Error)
		}
		if result.RowsAffected != 1 || !job.IsCurrent || job.State != string(ProcessingLeased) ||
			job.CurrentAttemptID == nil || *job.CurrentAttemptID != request.AttemptID ||
			job.TransitionRevision != request.ExpectedRevision {
			return ErrAttemptLost
		}
		descriptor, err := DecodeWorkDescriptorV1(job.DescriptorCanonical)
		if err != nil {
			return errors.Join(ErrProtocolInvalid, err)
		}
		modes, err := service.inputModesTx(ctx, tx, request.WorkerID, descriptor)
		if err != nil {
			return err
		}
		activated, err = service.grants.activateTx(ctx, tx, ActivateGrantRequest{
			GrantID: request.GrantID, Kind: GrantInput, JobID: request.JobID,
			AttemptID: request.AttemptID, WorkerID: request.WorkerID, Secret: request.Secret,
		})
		if err != nil {
			return err
		}
		transitionRevision, err = ValidateTransition(TransitionRequest{
			From: ProcessingLeased, To: ProcessingFetching,
			CurrentRevision: job.TransitionRevision, ExpectedRevision: request.ExpectedRevision,
		})
		if err != nil {
			return err
		}
		updated := tx.WithContext(ctx).Model(&model.BackupAssetProcessingJob{}).
			Where("id = ? AND state = ? AND transition_revision = ? AND current_attempt_id = ?", job.ID, ProcessingLeased, job.TransitionRevision, request.AttemptID).
			Updates(map[string]any{
				"state": string(ProcessingFetching), "transition_revision": transitionRevision,
				"updated_at": service.coordinator.utcNow(), "version": gorm.Expr("version + 1"),
			})
		if updated.Error != nil {
			return fmt.Errorf("advance input activation state: %w", updated.Error)
		}
		if updated.RowsAffected != 1 {
			return ErrRevisionConflict
		}
		binding = content.AttemptSourceBinding{
			SessionID: activated.SessionID, Ref: descriptor.Source,
			CatalogGenerationID: descriptor.CatalogGenerationID,
			SourceFingerprint:   descriptor.SourceFingerprint, EntryFingerprint: descriptor.EntryFingerprint,
			AllowedModes: modes,
			Limits: content.AttemptReadLimits{
				MaxRequests: activated.Limits.MaxRequests, MaxBytesPerRequest: activated.Limits.MaxBytesPerRequest,
				MaxCumulativeBytes: activated.Limits.MaxCumulativeBytes, MaxInFlight: activated.Limits.MaxInFlight,
			},
			AbsoluteExpiresAt: job.AbsoluteDeadline.UTC(),
		}
		return nil
	})
	if err != nil {
		return WorkerInputActivation{}, errors.Join(ErrGrantDenied, err)
	}
	session, info, err := service.input.OpenSession(ctx, binding)
	if err != nil {
		cleanupCtx, cancel := workerProtocolCleanupContext(ctx)
		defer cancel()
		_ = service.grants.RevokeAttempt(cleanupCtx, request.AttemptID, "source_changed")
		return WorkerInputActivation{}, err
	}
	owned := workerInputSession{
		session: session, workerID: request.WorkerID, instanceID: request.InstanceID,
		jobID: request.JobID, attemptID: request.AttemptID, expiresAt: activated.ExpiresAt,
	}
	service.inputMu.Lock()
	if _, exists := service.inputSessions[activated.SessionID]; exists {
		service.inputMu.Unlock()
		_ = session.Close()
		return WorkerInputActivation{}, ErrGrantDenied
	}
	service.inputSessions[activated.SessionID] = owned
	service.inputMu.Unlock()
	return WorkerInputActivation{
		SchemaVersion: 1, SessionID: activated.SessionID, TransitionRevision: transitionRevision,
		ExpiresAt: activated.ExpiresAt.UTC(),
		Source: WorkerInputSourceInfo{
			Size: info.Size, MediaType: info.MediaType, FingerprintStrong: info.FingerprintStrong,
			Sequential: info.Sequential, Range: info.Range,
		},
	}, nil
}

func (service *WorkerProtocolService) OpenInput(ctx context.Context, identity WorkerTransportIdentity, request WorkerInputReadRequest) (content.AttemptReadHandle, error) {
	if service == nil {
		return nil, ErrProtocolInvalid
	}
	if err := service.beginCall(false); err != nil {
		return nil, err
	}
	defer service.endCall()
	if request.SchemaVersion != 1 || !lowerHex(request.WorkerID, 32) ||
		!lowerHex(request.InstanceID, 32) || !lowerHex(request.SessionID, 32) || request.Length <= 0 ||
		(request.Mode != content.SourceModeSequential && request.Mode != content.SourceModeRange) ||
		(request.Mode == content.SourceModeSequential && request.Offset != 0) ||
		(request.Mode == content.SourceModeRange && (request.Offset < 0 || request.Offset > int64(^uint64(0)>>1)-request.Length)) {
		return nil, ErrProtocolInvalid
	}
	if err := service.protocol.AuthenticateWorker(ctx, identity, request.WorkerID, request.InstanceID); err != nil {
		return nil, err
	}
	service.inputMu.Lock()
	owned, exists := service.inputSessions[request.SessionID]
	if exists && !service.coordinator.utcNow().Before(owned.expiresAt.UTC()) {
		delete(service.inputSessions, request.SessionID)
		exists = false
	}
	service.inputMu.Unlock()
	if !exists || owned.workerID != request.WorkerID || owned.instanceID != request.InstanceID || owned.session == nil {
		if owned.session != nil && !exists {
			_ = owned.session.Close()
		}
		return nil, ErrGrantDenied
	}
	switch request.Mode {
	case content.SourceModeSequential:
		return owned.session.OpenSequential(ctx, request.Length)
	case content.SourceModeRange:
		return owned.session.OpenRange(ctx, request.Offset, request.Length)
	default:
		return nil, ErrProtocolInvalid
	}
}

func (service *WorkerProtocolService) Heartbeat(ctx context.Context, identity WorkerTransportIdentity, request WorkerHeartbeatRequest) (WorkerHeartbeatResult, error) {
	if service == nil {
		return WorkerHeartbeatResult{}, ErrProtocolInvalid
	}
	if err := service.beginCall(false); err != nil {
		return WorkerHeartbeatResult{}, err
	}
	defer service.endCall()
	if request.SchemaVersion != 1 || !lowerHex(request.WorkerID, 32) ||
		!lowerHex(request.InstanceID, 32) || !lowerHex(request.AttemptID, 32) {
		return WorkerHeartbeatResult{}, ErrProtocolInvalid
	}
	if err := service.protocol.AuthenticateWorker(ctx, identity, request.WorkerID, request.InstanceID); err != nil {
		return WorkerHeartbeatResult{}, err
	}
	result, err := service.coordinator.HeartbeatAttempt(ctx, HeartbeatRequest{AttemptID: request.AttemptID, WorkerID: request.WorkerID}, service.grants)
	if err != nil {
		return WorkerHeartbeatResult{}, err
	}
	service.renewAttemptSessionExpiries(request.AttemptID, result.GrantExpiresAt)
	return WorkerHeartbeatResult{
		SchemaVersion:               1,
		WorkerLeaseExpiresAt:        result.WorkerLeaseExpiresAt.UTC(),
		RecoveryPointLeaseExpiresAt: result.RecoveryPointLeaseExpiresAt.UTC(),
		EffectiveLeaseExpiresAt:     result.EffectiveLeaseExpiresAt.UTC(),
		TransitionRevision:          result.TransitionRevision,
		CancelRequested:             result.CancelRequested, CancelReason: result.CancelReason,
		WorkerDraining: result.WorkerDraining || !service.accepting.Load(),
	}, nil
}

func (service *WorkerProtocolService) Transition(ctx context.Context, identity WorkerTransportIdentity, request WorkerTransitionRequest) (WorkerTransitionResult, error) {
	if service == nil {
		return WorkerTransitionResult{}, ErrProtocolInvalid
	}
	if err := service.beginCall(false); err != nil {
		return WorkerTransitionResult{}, err
	}
	defer service.endCall()
	if request.SchemaVersion != 1 || !lowerHex(request.WorkerID, 32) ||
		!lowerHex(request.InstanceID, 32) || !lowerHex(request.JobID, 32) || !lowerHex(request.AttemptID, 32) ||
		request.ExpectedRevision <= 0 || request.To == ProcessingFetching {
		return WorkerTransitionResult{}, ErrProtocolInvalid
	}
	if err := service.protocol.AuthenticateWorker(ctx, identity, request.WorkerID, request.InstanceID); err != nil {
		return WorkerTransitionResult{}, err
	}
	result, err := service.coordinator.TransitionAttempt(ctx, AttemptTransitionRequest{
		JobID: request.JobID, AttemptID: request.AttemptID, WorkerID: request.WorkerID,
		ExpectedRevision: request.ExpectedRevision, To: request.To, ErrorCode: request.ErrorCode,
		RetryAt: request.RetryAt, CancelReason: request.CancelReason,
		SupersedeReason: request.SupersedeReason, ExpiryReason: request.ExpiryReason,
	})
	if err != nil {
		return WorkerTransitionResult{}, err
	}
	if attemptTerminalTransition(request.To) {
		service.closeAttemptInputSessions(request.AttemptID)
	}
	return WorkerTransitionResult{
		SchemaVersion: 1, State: result.State, Revision: result.Revision,
		CancelRequested: result.CancelRequested,
	}, nil
}

func (service *WorkerProtocolService) Drain(ctx context.Context, identity WorkerTransportIdentity, request WorkerDrainRequest) error {
	if service == nil {
		return ErrProtocolInvalid
	}
	if err := service.beginCall(false); err != nil {
		return err
	}
	defer service.endCall()
	if request.SchemaVersion != 1 || !lowerHex(request.WorkerID, 32) || !lowerHex(request.InstanceID, 32) {
		return ErrProtocolInvalid
	}
	if err := service.protocol.AuthenticateWorker(ctx, identity, request.WorkerID, request.InstanceID); err != nil {
		return err
	}
	return service.protocol.SetDraining(ctx, request.WorkerID)
}

func (service *WorkerProtocolService) ActivateSink(ctx context.Context, identity WorkerTransportIdentity, request WorkerSinkActivateRequest) (WorkerSinkActivation, error) {
	if service == nil {
		return WorkerSinkActivation{}, ErrProtocolInvalid
	}
	if err := service.beginCall(false); err != nil {
		return WorkerSinkActivation{}, err
	}
	defer service.endCall()
	if request.SchemaVersion != 1 || !lowerHex(request.WorkerID, 32) || !lowerHex(request.InstanceID, 32) ||
		!lowerHex(request.JobID, 32) || !lowerHex(request.AttemptID, 32) || !lowerHex(request.GrantID, 32) ||
		request.ExpectedRevision <= 0 || request.Secret == "" {
		return WorkerSinkActivation{}, ErrProtocolInvalid
	}
	if err := service.protocol.AuthenticateWorker(ctx, identity, request.WorkerID, request.InstanceID); err != nil {
		return WorkerSinkActivation{}, err
	}
	var activated ActivatedGrant
	var owned workerSinkSession
	err := service.coordinator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetProcessingJob
		result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", request.JobID).Limit(1).Find(&job)
		if result.Error != nil {
			return fmt.Errorf("load Sink activation job: %w", result.Error)
		}
		if result.RowsAffected != 1 || !job.IsCurrent || job.State != string(ProcessingUploading) ||
			job.CurrentAttemptID == nil || *job.CurrentAttemptID != request.AttemptID ||
			job.TransitionRevision != request.ExpectedRevision {
			return ErrAttemptLost
		}
		descriptor, err := DecodeWorkDescriptorV1(job.DescriptorCanonical)
		if err != nil {
			return errors.Join(ErrProtocolInvalid, err)
		}
		activated, err = service.grants.activateTx(ctx, tx, ActivateGrantRequest{
			GrantID: request.GrantID, Kind: GrantSink, JobID: request.JobID,
			AttemptID: request.AttemptID, WorkerID: request.WorkerID, Secret: request.Secret,
		})
		if err != nil {
			return err
		}
		_, lease, err := service.grants.validateAttemptFenceTx(ctx, tx, request.JobID, request.AttemptID, request.WorkerID)
		if err != nil {
			return err
		}
		owned = workerSinkSession{
			workerID: request.WorkerID, instanceID: request.InstanceID,
			jobID: request.JobID, attemptID: request.AttemptID, expiresAt: activated.ExpiresAt,
			recoveryPointFence: leaseFenceFromRow(lease), securityPolicyRevision: descriptor.SecurityPolicyRevision,
		}
		return nil
	})
	if err != nil {
		return WorkerSinkActivation{}, errors.Join(ErrGrantDenied, err)
	}
	service.sinkMu.Lock()
	if _, exists := service.sinkSessions[activated.SessionID]; exists {
		service.sinkMu.Unlock()
		return WorkerSinkActivation{}, ErrGrantDenied
	}
	service.sinkSessions[activated.SessionID] = owned
	service.sinkMu.Unlock()
	return WorkerSinkActivation{
		SchemaVersion: 1, SessionID: activated.SessionID, ExpiresAt: activated.ExpiresAt.UTC(),
		MaxArtifacts: int(activated.Limits.MaxRequests), MaxArtifactBytes: activated.Limits.MaxBytesPerRequest,
		MaxTotalBytes: activated.Limits.MaxCumulativeBytes, MaxInFlight: activated.Limits.MaxInFlight,
	}, nil
}

func (service *WorkerProtocolService) UploadArtifact(ctx context.Context, identity WorkerTransportIdentity, request WorkerUploadArtifactRequest, body io.Reader) (UploadedArtifact, error) {
	if service == nil {
		return UploadedArtifact{}, ErrProtocolInvalid
	}
	if err := service.beginCall(false); err != nil {
		return UploadedArtifact{}, err
	}
	defer service.endCall()
	if body == nil || request.SchemaVersion != 1 || !lowerHex(request.WorkerID, 32) ||
		!lowerHex(request.InstanceID, 32) || !lowerHex(request.SessionID, 32) ||
		!lowerHex(request.JobID, 32) || !lowerHex(request.AttemptID, 32) {
		return UploadedArtifact{}, ErrProtocolInvalid
	}
	if err := service.protocol.AuthenticateWorker(ctx, identity, request.WorkerID, request.InstanceID); err != nil {
		return UploadedArtifact{}, err
	}
	owned, ok := service.sinkSession(request.SessionID)
	if !ok || owned.workerID != request.WorkerID || owned.instanceID != request.InstanceID ||
		owned.jobID != request.JobID || owned.attemptID != request.AttemptID {
		return UploadedArtifact{}, ErrGrantDenied
	}
	return service.sink.UploadArtifact(ctx, UploadArtifactRequest{
		JobID: request.JobID, AttemptID: request.AttemptID, WorkerID: request.WorkerID,
		GrantID: request.SessionID, Artifact: request.Artifact,
	}, body)
}

func (service *WorkerProtocolService) CommitManifest(ctx context.Context, identity WorkerTransportIdentity, request WorkerCommitManifestRequest) (WorkerCommitManifestResult, error) {
	if service == nil {
		return WorkerCommitManifestResult{}, ErrProtocolInvalid
	}
	if err := service.beginCall(false); err != nil {
		return WorkerCommitManifestResult{}, err
	}
	defer service.endCall()
	if request.SchemaVersion != 1 || !lowerHex(request.WorkerID, 32) ||
		!lowerHex(request.InstanceID, 32) || !lowerHex(request.SessionID, 32) ||
		!lowerHex(request.JobID, 32) || !lowerHex(request.AttemptID, 32) ||
		strings.TrimSpace(request.SecurityPolicyRevision) == "" {
		return WorkerCommitManifestResult{}, ErrProtocolInvalid
	}
	if err := service.protocol.AuthenticateWorker(ctx, identity, request.WorkerID, request.InstanceID); err != nil {
		return WorkerCommitManifestResult{}, err
	}
	owned, ok := service.sinkSession(request.SessionID)
	if !ok || owned.workerID != request.WorkerID || owned.instanceID != request.InstanceID ||
		owned.jobID != request.JobID || owned.attemptID != request.AttemptID ||
		owned.securityPolicyRevision != request.SecurityPolicyRevision {
		return WorkerCommitManifestResult{}, ErrGrantDenied
	}
	result, err := service.sink.CommitManifest(ctx, CommitManifestRequest{
		JobID: request.JobID, AttemptID: request.AttemptID, WorkerID: request.WorkerID,
		GrantID: request.SessionID, RecoveryPointFence: owned.recoveryPointFence,
		SecurityPolicyRevision: request.SecurityPolicyRevision, Artifacts: request.Artifacts,
	})
	if err == nil || result.ArtifactSetID != "" {
		service.removeSinkSession(request.SessionID)
	}
	if err != nil {
		return WorkerCommitManifestResult{}, err
	}
	service.closeAttemptInputSessions(request.AttemptID)
	return WorkerCommitManifestResult{
		SchemaVersion: 1, ArtifactSetID: result.ArtifactSetID,
		ManifestDigest: result.ManifestDigest, ProjectionRequired: result.ProjectionRequired,
	}, nil
}

func (service *WorkerProtocolService) sinkSession(sessionID string) (workerSinkSession, bool) {
	service.sinkMu.Lock()
	defer service.sinkMu.Unlock()
	owned, exists := service.sinkSessions[sessionID]
	if exists && !service.coordinator.utcNow().Before(owned.expiresAt.UTC()) {
		delete(service.sinkSessions, sessionID)
		return workerSinkSession{}, false
	}
	return owned, exists
}

func (service *WorkerProtocolService) removeSinkSession(sessionID string) {
	service.sinkMu.Lock()
	delete(service.sinkSessions, sessionID)
	service.sinkMu.Unlock()
}

func (service *WorkerProtocolService) renewAttemptSessionExpiries(attemptID string, expiresAt time.Time) {
	service.inputMu.Lock()
	for sessionID, owned := range service.inputSessions {
		if owned.attemptID == attemptID {
			owned.expiresAt = expiresAt.UTC()
			service.inputSessions[sessionID] = owned
		}
	}
	service.inputMu.Unlock()
	service.sinkMu.Lock()
	for sessionID, owned := range service.sinkSessions {
		if owned.attemptID == attemptID {
			owned.expiresAt = expiresAt.UTC()
			service.sinkSessions[sessionID] = owned
		}
	}
	service.sinkMu.Unlock()
}

func (service *WorkerProtocolService) closeAttemptInputSessions(attemptID string) {
	service.inputMu.Lock()
	sessions := make([]content.AttemptInputSession, 0)
	for sessionID, owned := range service.inputSessions {
		if owned.attemptID == attemptID {
			delete(service.inputSessions, sessionID)
			if owned.session != nil {
				sessions = append(sessions, owned.session)
			}
		}
	}
	service.inputMu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
}

func (service *WorkerProtocolService) inputModesTx(ctx context.Context, tx *gorm.DB, workerID string, descriptor WorkDescriptorV1) ([]content.SourceMode, error) {
	var capability model.BackupAssetWorkerCapability
	result := tx.WithContext(ctx).Where(
		"worker_id = ? AND capability = ? AND capability_schema = ? AND pipeline_fingerprint = ? AND output_profile = ? AND health_state IN ?",
		workerID, descriptor.Capability, descriptor.CapabilitySchema, descriptor.PipelineFingerprint, descriptor.OutputProfile,
		[]string{"ready", "degraded"},
	).Limit(1).Find(&capability)
	if result.Error != nil {
		return nil, fmt.Errorf("load input capability contract: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return nil, ErrProtocolCapabilityUnsupported
	}
	parts := strings.Split(capability.InputModes, ",")
	modes := make([]content.SourceMode, 0, len(parts))
	seen := make(map[content.SourceMode]bool, len(parts))
	for _, part := range parts {
		mode := content.SourceMode(part)
		if mode != content.SourceModeStat && mode != content.SourceModeSequential && mode != content.SourceModeRange || seen[mode] {
			return nil, ErrProtocolInvalid
		}
		seen[mode] = true
		modes = append(modes, mode)
	}
	if len(modes) == 0 || !seen[content.SourceModeStat] {
		return nil, ErrProtocolInvalid
	}
	return modes, nil
}

func workerProtocolCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

type ProtocolService struct {
	db       *gorm.DB
	registry *CapabilityRegistry
	now      func() time.Time
}

func NewProtocolService(db *gorm.DB, registry *CapabilityRegistry, now func() time.Time) (*ProtocolService, error) {
	if db == nil || registry == nil {
		return nil, ErrProtocolInvalid
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ProtocolService{db: db, registry: registry, now: now}, nil
}

func (service *ProtocolService) Handshake(ctx context.Context, identity WorkerTransportIdentity, request HandshakeRequest) (HandshakeResult, error) {
	validated, err := ValidateHandshake(identity, request, service.registry)
	if err != nil {
		return HandshakeResult{}, err
	}
	var response HandshakeResult
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.BackupAssetWorkerIdentity
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"})
		if validated.TransportKind == WorkerTransportMTLS {
			query = query.Where("id = ?", validated.TransportWorkerID)
		} else {
			query = query.Where("transport_kind = ? AND instance_id = ?", validated.TransportKind, validated.InstanceID)
		}
		query = query.Limit(1).Find(&existing)
		if query.Error != nil {
			return fmt.Errorf("load Worker transport identity: %w", query.Error)
		}
		now := service.utcNow()
		if query.RowsAffected == 1 {
			if existing.TransportKind != string(validated.TransportKind) ||
				(validated.TransportKind == WorkerTransportLocal &&
					(existing.TransportFingerprint != validated.TransportFingerprint || existing.IdentityRevision != validated.IdentityRevision)) {
				return ErrWorkerUnauthenticated
			}
			if existing.TrustState == "quarantined" {
				return ErrWorkerQuarantined
			}
			if existing.TrustState != "active" {
				return ErrWorkerUnauthenticated
			}
			identityUpdates := map[string]any{}
			if validated.TransportKind == WorkerTransportMTLS {
				fingerprintChanged := existing.TransportFingerprint != validated.TransportFingerprint
				instanceChanged := existing.InstanceID != validated.InstanceID
				switch {
				case fingerprintChanged && validated.IdentityRevision != existing.IdentityRevision+1:
					return ErrWorkerUnauthenticated
				case !fingerprintChanged && validated.IdentityRevision != existing.IdentityRevision:
					return ErrWorkerUnauthenticated
				}
				if fingerprintChanged || instanceChanged {
					live, err := service.hasLiveWorkerAuthorityTx(ctx, tx, existing.ID)
					if err != nil {
						return err
					}
					if live {
						return ErrWorkerUnauthenticated
					}
					identityUpdates["transport_fingerprint"] = validated.TransportFingerprint
					identityUpdates["instance_id"] = validated.InstanceID
					identityUpdates["identity_revision"] = validated.IdentityRevision
				}
			}
			identityUpdates["protocol_version"] = validated.ProtocolVersion
			identityUpdates["health_state"] = "ready"
			identityUpdates["interactive_slots"] = validated.InteractiveSlots
			identityUpdates["background_slots"] = validated.BackgroundSlots
			identityUpdates["last_seen_at"] = now
			identityUpdates["updated_at"] = now
			if err := tx.Model(&model.BackupAssetWorkerIdentity{}).Where("id = ? AND trust_state = ?", existing.ID, "active").
				Updates(identityUpdates).Error; err != nil {
				return fmt.Errorf("refresh Worker identity: %w", err)
			}
			if err := service.replaceCapabilitiesTx(tx, existing.ID, validated.Capabilities, now); err != nil {
				return err
			}
			response = HandshakeResult{WorkerID: existing.ID, TrustState: "active", HealthState: "ready", CapabilityCount: len(validated.Capabilities)}
			return nil
		}

		workerID := validated.TransportWorkerID
		if validated.TransportKind == WorkerTransportLocal {
			workerID, err = backupasset.NewOpaqueID()
			if err != nil {
				return err
			}
		}
		row := model.BackupAssetWorkerIdentity{
			ID: workerID, TransportKind: string(validated.TransportKind), TransportFingerprint: validated.TransportFingerprint,
			InstanceID: validated.InstanceID, IdentityRevision: validated.IdentityRevision, ProtocolVersion: validated.ProtocolVersion,
			TrustState: "active", HealthState: "ready", InteractiveSlots: validated.InteractiveSlots,
			BackgroundSlots: validated.BackgroundSlots, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create Worker identity: %w", err)
		}
		if err := service.replaceCapabilitiesTx(tx, row.ID, validated.Capabilities, now); err != nil {
			return err
		}
		response = HandshakeResult{WorkerID: row.ID, TrustState: row.TrustState, HealthState: row.HealthState, CapabilityCount: len(validated.Capabilities)}
		return nil
	})
	if err != nil {
		return HandshakeResult{}, err
	}
	return response, nil
}

func (service *ProtocolService) hasLiveWorkerAuthorityTx(ctx context.Context, tx *gorm.DB, workerID string) (bool, error) {
	var attempts int64
	if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingAttempt{}).
		Where("worker_id = ? AND state = ? AND is_current = ?", workerID, "active", true).Count(&attempts).Error; err != nil {
		return false, fmt.Errorf("check Worker attempt authority: %w", err)
	}
	if attempts != 0 {
		return true, nil
	}
	var grants int64
	if err := tx.WithContext(ctx).Model(&model.BackupAssetProcessingGrant{}).
		Where("worker_id = ? AND state IN ?", workerID, []string{string(GrantIssued), string(GrantActive)}).Count(&grants).Error; err != nil {
		return false, fmt.Errorf("check Worker grant authority: %w", err)
	}
	return grants != 0, nil
}

func (service *ProtocolService) AuthenticateWorker(ctx context.Context, identity WorkerTransportIdentity, workerID, instanceID string) error {
	if service == nil || !validTransportIdentity(identity) || !lowerHex(workerID, 32) || !lowerHex(instanceID, 32) {
		return ErrWorkerUnauthenticated
	}
	var worker model.BackupAssetWorkerIdentity
	result := service.db.WithContext(ctx).Where("id = ?", workerID).Limit(1).Find(&worker)
	if result.Error != nil || result.RowsAffected != 1 || worker.TransportKind != string(identity.Kind) ||
		worker.TransportFingerprint != identity.Fingerprint || worker.InstanceID != instanceID {
		return ErrWorkerUnauthenticated
	}
	if identity.Kind == WorkerTransportMTLS && identity.WorkerID != worker.ID {
		return ErrWorkerUnauthenticated
	}
	if worker.TrustState == "quarantined" {
		return ErrWorkerQuarantined
	}
	if worker.TrustState != "active" || !oneOf(worker.HealthState, "ready", "degraded", "draining") {
		return ErrWorkerUnauthenticated
	}
	return nil
}

func (service *ProtocolService) Quarantine(ctx context.Context, workerID string, code ProcessingErrorCode) error {
	category, err := code.Category()
	if backupasset.ValidateOpaqueID(workerID) != nil || err != nil || category != ContractSecurityError {
		return ErrProtocolInvalid
	}
	now := service.utcNow()
	return service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var worker model.BackupAssetWorkerIdentity
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", workerID).Limit(1).Find(&worker)
		if result.Error != nil || result.RowsAffected != 1 || worker.TrustState != "active" {
			return ErrWorkerUnauthenticated
		}
		if err := tx.Model(&model.BackupAssetProcessingGrant{}).Where("worker_id = ? AND state IN ?", workerID, []string{"issued", "active"}).
			Updates(map[string]any{
				"state": string(GrantRevoked), "activation_secret_hash": "", "revoked_at": now,
				"revocation_reason": "quarantine", "updated_at": now, "version": gorm.Expr("version + 1"),
			}).Error; err != nil {
			return fmt.Errorf("revoke quarantined Worker grants: %w", err)
		}
		updated := tx.Model(&model.BackupAssetWorkerIdentity{}).Where("id = ? AND trust_state = ?", worker.ID, "active").
			Updates(map[string]any{
				"trust_state": "quarantined", "health_state": "draining", "quarantine_code": string(code),
				"updated_at": now,
			})
		if updated.Error != nil || updated.RowsAffected != 1 {
			return errors.Join(ErrWorkerUnauthenticated, updated.Error)
		}
		return nil
	})
}

func (service *ProtocolService) SetDraining(ctx context.Context, workerID string) error {
	if backupasset.ValidateOpaqueID(workerID) != nil {
		return ErrProtocolInvalid
	}
	updated := service.db.WithContext(ctx).Model(&model.BackupAssetWorkerIdentity{}).
		Where("id = ? AND trust_state = ? AND health_state IN ?", workerID, "active", []string{"ready", "degraded"}).
		Updates(map[string]any{"health_state": "draining", "updated_at": service.utcNow()})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrWorkerUnauthenticated
	}
	return nil
}

func (service *ProtocolService) replaceCapabilitiesTx(tx *gorm.DB, workerID string, capabilities []ValidatedCapability, now time.Time) error {
	if err := tx.Where("worker_id = ?", workerID).Delete(&model.BackupAssetWorkerCapability{}).Error; err != nil {
		return fmt.Errorf("replace Worker capabilities: %w", err)
	}
	for _, capability := range capabilities {
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		limits, err := json.Marshal(capability.Limits)
		if err != nil {
			return ErrProtocolInvalid
		}
		modes := make([]string, len(capability.InputModes))
		for index, mode := range capability.InputModes {
			modes[index] = string(mode)
		}
		row := model.BackupAssetWorkerCapability{
			ID: id, WorkerID: workerID, Capability: capability.Capability, CapabilitySchema: capability.CapabilitySchema,
			PipelineFingerprint: capability.PipelineFingerprint, OutputProfile: capability.OutputProfile,
			InputModes: strings.Join(modes, ","), LimitsCanonical: limits,
			AdvertisementDigest: capability.AdvertisementDigest, HealthState: "ready", CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("persist Worker capability: %w", err)
		}
	}
	return nil
}

func (service *ProtocolService) utcNow() time.Time { return service.now().UTC() }

func NewProductionCapabilityRegistry() *CapabilityRegistry {
	return &CapabilityRegistry{definitions: map[string]CapabilityDefinition{}}
}

func NewCapabilityRegistry(definitions []CapabilityDefinition) (*CapabilityRegistry, error) {
	registry := &CapabilityRegistry{definitions: make(map[string]CapabilityDefinition, len(definitions))}
	for _, definition := range definitions {
		if !validCapabilityIdentity(definition.Capability, definition.CapabilitySchema, definition.PipelineFingerprint, definition.OutputProfile) {
			return nil, ErrProtocolInvalid
		}
		key := capabilityKey(definition.Capability, definition.CapabilitySchema, definition.PipelineFingerprint, definition.OutputProfile)
		if _, exists := registry.definitions[key]; exists {
			return nil, ErrProtocolInvalid
		}
		registry.definitions[key] = definition
	}
	return registry, nil
}

func DecodeHandshakeRequest(payload []byte) (HandshakeRequest, error) {
	if len(payload) == 0 || len(payload) > 64*1024 || !json.Valid(payload) || rejectDuplicateJSONMembers(payload) != nil {
		return HandshakeRequest{}, ErrProtocolInvalid
	}
	if err := requireHandshakeFields(payload); err != nil {
		return HandshakeRequest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var request HandshakeRequest
	if err := decoder.Decode(&request); err != nil || ensureJSONEOF(decoder) != nil {
		return HandshakeRequest{}, ErrProtocolInvalid
	}
	return request, nil
}

func DecodeWorkerJSON(payload []byte, destination any) error {
	if destination == nil || len(payload) == 0 || !json.Valid(payload) || rejectDuplicateJSONMembers(payload) != nil {
		return ErrProtocolInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil || ensureJSONEOF(decoder) != nil {
		return ErrProtocolInvalid
	}
	return nil
}

func ValidateHandshake(identity WorkerTransportIdentity, request HandshakeRequest, registry *CapabilityRegistry) (ValidatedHandshake, error) {
	if registry == nil || !validTransportIdentity(identity) || request.SchemaVersion != 1 ||
		request.InstanceID == "" || !lowerHex(request.InstanceID, 32) || request.IdentityRevision <= 0 ||
		request.InteractiveSlots < 0 || request.InteractiveSlots > 64 || request.BackgroundSlots < 0 || request.BackgroundSlots > 64 ||
		request.InteractiveSlots+request.BackgroundSlots <= 0 || len(request.Capabilities) > 32 {
		return ValidatedHandshake{}, ErrProtocolInvalid
	}
	if request.ProtocolVersion != WorkerProtocolVersion {
		return ValidatedHandshake{}, ErrProtocolIncompatible
	}
	result := ValidatedHandshake{
		TransportKind: identity.Kind, TransportFingerprint: identity.Fingerprint, TransportWorkerID: identity.WorkerID,
		InstanceID: request.InstanceID, IdentityRevision: request.IdentityRevision, ProtocolVersion: request.ProtocolVersion,
		InteractiveSlots: request.InteractiveSlots, BackgroundSlots: request.BackgroundSlots,
		Capabilities: make([]ValidatedCapability, 0, len(request.Capabilities)),
	}
	seen := make(map[string]bool, len(request.Capabilities))
	for _, advertisement := range request.Capabilities {
		validated, err := registry.validateAdvertisement(advertisement)
		if err != nil {
			return ValidatedHandshake{}, err
		}
		key := capabilityKey(validated.Capability, validated.CapabilitySchema, validated.PipelineFingerprint, validated.OutputProfile)
		if seen[key] {
			return ValidatedHandshake{}, ErrProtocolInvalid
		}
		seen[key] = true
		result.Capabilities = append(result.Capabilities, validated)
	}
	return result, nil
}

func (registry *CapabilityRegistry) validateAdvertisement(value CapabilityAdvertisement) (ValidatedCapability, error) {
	if value.SchemaVersion != 1 || !validCapabilityIdentity(value.Capability, value.CapabilitySchema, value.PipelineFingerprint, value.OutputProfile) ||
		!validProtocolLimits(value.Limits) {
		return ValidatedCapability{}, ErrProtocolInvalid
	}
	key := capabilityKey(value.Capability, value.CapabilitySchema, value.PipelineFingerprint, value.OutputProfile)
	if _, supported := registry.definitions[key]; !supported {
		return ValidatedCapability{}, ErrProtocolCapabilityUnsupported
	}
	modes := make([]ProtocolInputMode, len(value.InputModes))
	copy(modes, value.InputModes)
	seen := make(map[ProtocolInputMode]bool, len(modes))
	for _, mode := range modes {
		if mode != ProtocolInputStat && mode != ProtocolInputSequential && mode != ProtocolInputRange || seen[mode] {
			return ValidatedCapability{}, ErrProtocolInvalid
		}
		seen[mode] = true
	}
	if len(modes) == 0 || !seen[ProtocolInputStat] {
		return ValidatedCapability{}, ErrProtocolInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return ValidatedCapability{}, ErrProtocolInvalid
	}
	digest := sha256.Sum256(canonical)
	return ValidatedCapability{
		Capability: value.Capability, CapabilitySchema: value.CapabilitySchema,
		PipelineFingerprint: value.PipelineFingerprint, OutputProfile: value.OutputProfile,
		InputModes: modes, Limits: value.Limits, AdvertisementDigest: hex.EncodeToString(digest[:]),
	}, nil
}

func validTransportIdentity(identity WorkerTransportIdentity) bool {
	if !lowerHex(identity.Fingerprint, 64) {
		return false
	}
	switch identity.Kind {
	case WorkerTransportLocal:
		return identity.WorkerID == ""
	case WorkerTransportMTLS:
		return lowerHex(identity.WorkerID, 32)
	default:
		return false
	}
}

func validCapabilityIdentity(capability, schema, pipeline, profile string) bool {
	for _, field := range []struct {
		value   string
		maximum int
	}{{capability, 64}, {schema, 64}, {pipeline, 128}, {profile, 64}} {
		if field.value == "" || strings.TrimSpace(field.value) != field.value || len(field.value) > field.maximum || strings.ContainsAny(field.value, "\r\n\x00") {
			return false
		}
	}
	return true
}

func validProtocolLimits(value ProtocolCapabilityLimits) bool {
	return value.MaxInputBytes >= 64*1024 && value.MaxInputBytes <= 16*1024*1024*1024 &&
		value.MaxOutputBytes > 0 && value.MaxOutputBytes <= value.MaxInputBytes &&
		value.MaxOutputCount > 0 && value.MaxOutputCount <= 256 && value.MaxPages > 0 && value.MaxPages <= 100000 &&
		value.MaxPixels > 0 && value.MaxDurationMillis > 0 && value.MaxExpandedBytes > 0
}

func requireHandshakeFields(payload []byte) error {
	var root map[string]json.RawMessage
	if json.Unmarshal(payload, &root) != nil || requireExactProtocolFields(root, []string{
		"schema_version", "protocol_version", "instance_id", "identity_revision", "interactive_slots", "background_slots", "capabilities",
	}) != nil {
		return ErrProtocolInvalid
	}
	var capabilities []json.RawMessage
	if json.Unmarshal(root["capabilities"], &capabilities) != nil {
		return ErrProtocolInvalid
	}
	for _, raw := range capabilities {
		var capability map[string]json.RawMessage
		if json.Unmarshal(raw, &capability) != nil || requireExactProtocolFields(capability, []string{
			"schema_version", "capability", "capability_schema", "pipeline_fingerprint", "output_profile", "input_modes", "limits",
		}) != nil {
			return ErrProtocolInvalid
		}
		var limits map[string]json.RawMessage
		if json.Unmarshal(capability["limits"], &limits) != nil || requireExactProtocolFields(limits, []string{
			"max_input_bytes", "max_output_bytes", "max_output_count", "max_pages", "max_pixels", "max_duration_millis", "max_expanded_bytes",
		}) != nil {
			return ErrProtocolInvalid
		}
	}
	return nil
}

func requireExactProtocolFields(value map[string]json.RawMessage, fields []string) error {
	if len(value) != len(fields) {
		return fmt.Errorf("%w: missing or unknown field", ErrProtocolInvalid)
	}
	for _, field := range fields {
		if _, exists := value[field]; !exists {
			return fmt.Errorf("%w: missing field", ErrProtocolInvalid)
		}
	}
	return nil
}
