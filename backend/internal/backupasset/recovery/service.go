package recovery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	planIdempotencyKeyMinBytes = 16
	planIdempotencyKeyMaxBytes = 256
	planCreateRetryAttempts    = 8
	directoryParentBatchSize   = 500
)

var (
	errPlanIdempotencyRace           = errors.New("recovery plan idempotency race")
	errPlanDatabaseBusy              = errors.New("recovery plan database busy")
	ErrRecoveryTargetChanged         = errors.New("recovery target changed")
	ErrTargetRootIdempotencyConflict = errors.New("recovery target root idempotency conflict")
	ErrTargetRootMutationConflict    = errors.New("recovery target root mutation conflict")
	planSavepointSequence            atomic.Uint64
)

type planCreateStage uint8

const (
	planCreateBeforePlanInsert planCreateStage = iota + 1
	planCreateBeforeItemInsert
	planCreateBeforeCommit
)

type planCreateFaultError struct {
	cause error
}

func (err *planCreateFaultError) Error() string {
	return "recovery plan persistence failed"
}

func (err *planCreateFaultError) Unwrap() error {
	return err.cause
}

// CreatePlanRequest carries a fully frozen plan intent. The idempotency key,
// source revision, and encrypted target details are private service-boundary
// values and must not be projected into API responses.
type CreatePlanRequest struct {
	RequesterID       uint
	Endpoint          string
	IdempotencyKey    string
	Selection         ExactSelection
	Plan              RecoveryPlan
	AuthorityCategory AuthorityCategory
	EstimatedItems    int64
	EstimatedBytes    int64
}

// CreatePlanResult is the durable-plan acknowledgement. It contains no source
// locator, target locator, idempotency key, reason, proof, or secret material.
type CreatePlanResult struct {
	PlanID string    `json:"plan_id"`
	State  PlanState `json:"state"`
	Replay bool      `json:"replay"`
}

type RecoveryTargetRootResolver interface {
	ResolveRecoveryTargetRootTx(
		context.Context,
		*gorm.DB,
		uint,
		string,
	) (settings.RecoveryTargetRootResolution, error)
}

// RecoveryTargetRootRegistry is the private encrypted-root persistence seam
// consumed only by the Recovery authority owner.
type RecoveryTargetRootRegistry interface {
	RecoveryTargetRootResolver
	RegisterRecoveryTargetRootTx(
		context.Context,
		*gorm.DB,
		settings.RecoveryTargetRootDefinition,
	) (settings.RecoveryTargetRootResolution, error)
	DeleteRecoveryTargetRootTx(context.Context, *gorm.DB, uint, string) error
	ListRecoveryTargetRoots(context.Context, uint) ([]settings.RecoveryTargetRootSummary, error)
}

const (
	targetRootRegistrationProbeFreshness     = 30 * time.Second
	targetRootRegistrationNodeRevisionDomain = "xirang/recovery/target-root-registration-node/v1"
	targetRootRegistrationCredentialDomain   = "xirang/recovery/target-root-registration-credential/v1"
)

// TargetRootAuthorityServiceDependencies supplies the only owner of
// registration, rotation, and deletion for private Recovery target roots.
type TargetRootAuthorityServiceDependencies struct {
	DB          *gorm.DB
	Registry    RecoveryTargetRootRegistry
	Probe       TargetRootRegistrationProbe
	NewRevision func() (string, error)
	Now         func() time.Time
}

// TargetRootAuthorityService owns purpose-exact read-only registration probes
// and their subsequent durable revalidation.
type TargetRootAuthorityService struct {
	db          *gorm.DB
	registry    RecoveryTargetRootRegistry
	probe       TargetRootRegistrationProbe
	newRevision func() (string, error)
	now         func() time.Time
}

type targetRootPersistedMutationState struct {
	row     model.SystemSetting
	present bool
}

type targetRootMutationReceipt struct {
	SchemaVersion    int                                `json:"schema_version"`
	IntentDigest     string                             `json:"intent_digest"`
	SessionDigest    string                             `json:"session_digest"`
	SessionExpiresAt time.Time                          `json:"session_expires_at"`
	Result           settings.RecoveryTargetRootSummary `json:"result"`
}

// TargetRootMutationRollback is an opaque, Recovery-owned capability for
// restoring one exact private root mutation. Its persisted state is never
// exported, formatted, or serialized.
type TargetRootMutationRollback struct {
	owner         *TargetRootAuthorityService
	key           string
	before        targetRootPersistedMutationState
	after         targetRootPersistedMutationState
	receiptKey    string
	receiptBefore targetRootPersistedMutationState
	receiptAfter  targetRootPersistedMutationState
	noOp          bool
}

func (TargetRootMutationRollback) String() string               { return "TargetRootMutationRollback{}" }
func (rollback TargetRootMutationRollback) GoString() string    { return rollback.String() }
func (TargetRootMutationRollback) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Replay reports whether the transition callback observed an already committed
// receipt instead of applying a new durable root mutation.
func (rollback TargetRootMutationRollback) Replay() bool { return rollback.noOp }

func NewTargetRootAuthorityService(
	dependencies TargetRootAuthorityServiceDependencies,
) (*TargetRootAuthorityService, error) {
	if dependencies.DB == nil || dependencies.Registry == nil || dependencies.Probe == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	if dependencies.NewRevision == nil {
		dependencies.NewRevision = backupasset.NewOpaqueID
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &TargetRootAuthorityService{
		db: dependencies.DB, registry: dependencies.Registry, probe: dependencies.Probe,
		newRevision: dependencies.NewRevision, now: dependencies.Now,
	}, nil
}

// ValidateRegistration applies all local registration validation before a
// runtime transition stops admission or drains owners.
func (service *TargetRootAuthorityService) ValidateRegistration(request TargetRootRegistrationRequest) error {
	if service == nil || service.db == nil || service.registry == nil || service.probe == nil ||
		service.newRevision == nil || service.now == nil {
		return ErrRecoveryTargetUnavailable
	}
	if err := validateTargetRootRegistrationRequest(request); err != nil {
		return err
	}
	if request.Mutation != "" {
		now := service.now().UTC()
		_, _, _, err := targetRootRegistrationReceiptAuthority(request, now)
		if err != nil {
			return err
		}
		if !now.Before(request.SessionExpiresAt.UTC()) {
			return backupasset.ErrForbidden
		}
	}
	return nil
}

// ReplayRegistration returns a durable same-intent, same-session result
// without opening a probe or runtime transition. It is safe to call before
// validating a new step-up proof because absence never authorizes a mutation.
func (service *TargetRootAuthorityService) ReplayRegistration(
	ctx context.Context,
	request TargetRootRegistrationRequest,
) (settings.RecoveryTargetRootSummary, bool, error) {
	if service == nil || service.db == nil || service.now == nil || ctx == nil {
		return settings.RecoveryTargetRootSummary{}, false, ErrRecoveryTargetUnavailable
	}
	if err := validateTargetRootRegistrationRequest(request); err != nil {
		return settings.RecoveryTargetRootSummary{}, false, err
	}
	receiptKey, intentDigest, sessionDigest, err := targetRootRegistrationReceiptAuthority(
		request, service.now().UTC(),
	)
	if err != nil {
		return settings.RecoveryTargetRootSummary{}, false, err
	}
	return service.replayTargetRootMutation(ctx, receiptKey, intentDigest, sessionDigest)
}

// ReplayDeletion is the receipt-first counterpart for a closed root delete.
func (service *TargetRootAuthorityService) ReplayDeletion(
	ctx context.Context,
	request TargetRootDeletionRequest,
) (settings.RecoveryTargetRootSummary, bool, error) {
	if service == nil || service.db == nil || service.now == nil || ctx == nil {
		return settings.RecoveryTargetRootSummary{}, false, ErrRecoveryTargetUnavailable
	}
	receiptKey, intentDigest, sessionDigest, err := targetRootDeletionReceiptAuthority(
		request, service.now().UTC(),
	)
	if err != nil {
		return settings.RecoveryTargetRootSummary{}, false, err
	}
	return service.replayTargetRootMutation(ctx, receiptKey, intentDigest, sessionDigest)
}

// Register performs one fresh read-only target observation outside the
// transaction, then locks and revalidates the durable node, credential, and
// exact registry row before persisting the private authority product.
func (service *TargetRootAuthorityService) Register(
	ctx context.Context,
	request TargetRootRegistrationRequest,
) (settings.RecoveryTargetRootResolution, error) {
	result, _, err := service.registerMutation(ctx, request)
	return result, err
}

// RegisterMutation persists a root while returning only its safe summary and
// an opaque exact rollback capability for the transition owner.
func (service *TargetRootAuthorityService) RegisterMutation(
	ctx context.Context,
	request TargetRootRegistrationRequest,
) (settings.RecoveryTargetRootSummary, TargetRootMutationRollback, error) {
	result, rollback, err := service.registerMutation(ctx, request)
	if err != nil {
		return settings.RecoveryTargetRootSummary{}, TargetRootMutationRollback{}, err
	}
	return settings.RecoveryTargetRootSummary{
		NodeID: result.NodeID, RootID: result.RootID, SafeLabel: result.SafeLabel,
	}, rollback, nil
}

func (service *TargetRootAuthorityService) registerMutation(
	ctx context.Context,
	request TargetRootRegistrationRequest,
) (settings.RecoveryTargetRootResolution, TargetRootMutationRollback, error) {
	if service == nil || service.db == nil || service.registry == nil || service.probe == nil ||
		service.newRevision == nil || service.now == nil || ctx == nil {
		return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, ErrRecoveryTargetUnavailable
	}
	if err := ctx.Err(); err != nil {
		return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, err
	}
	if err := validateTargetRootRegistrationRequest(request); err != nil {
		return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, err
	}
	receiptKey, intentDigest, sessionDigest := "", "", ""
	if request.Mutation != "" {
		var authorityErr error
		receiptKey, intentDigest, sessionDigest, authorityErr =
			targetRootRegistrationReceiptAuthority(request, service.now().UTC())
		if authorityErr != nil {
			return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, authorityErr
		}
		if replay, found, replayErr := service.replayTargetRootMutation(
			ctx, receiptKey, intentDigest, sessionDigest,
		); replayErr != nil {
			return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, replayErr
		} else if found {
			return settings.RecoveryTargetRootResolution{
				NodeID: replay.NodeID, RootID: replay.RootID, SafeLabel: replay.SafeLabel,
			}, TargetRootMutationRollback{owner: service, noOp: true}, nil
		}
		if !service.now().UTC().Before(request.SessionExpiresAt.UTC()) {
			return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, backupasset.ErrForbidden
		}
	}
	key := targetRootAuthorityRecordKey(request.NodeID, request.RootID)
	captureNow := service.now().UTC()
	if captureNow.IsZero() {
		return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, ErrRecoveryTargetUnavailable
	}
	var captured recoveryTargetRootAuthorityNodeCredential
	captureErr := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		captured, err = loadRecoveryTargetRootAuthorityNodeCredential(ctx, tx, request.NodeID, captureNow, false)
		return err
	})
	if captureErr != nil {
		if errors.Is(captureErr, settings.ErrRecoveryTargetRootNotFound) {
			return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, captureErr
		}
		return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, recoveryTargetUnavailableForContext(ctx)
	}
	probeRequest := request
	probeRequest.NodeRevision = captured.nodeRevision
	probeRequest.CredentialRevision = captured.credentialRevision
	probeRequest.Purpose = TargetRootRegistrationPurposeReadOnly
	probeRequest.ReadOnly = true
	observation, probeErr := service.probe.ObserveRecoveryTargetRoot(ctx, probeRequest)
	if probeErr != nil {
		return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, recoveryTargetUnavailableForContext(ctx)
	}
	postProbeNow := service.now().UTC()
	if postProbeNow.IsZero() || postProbeNow.Before(captureNow) ||
		!validTargetRootRegistrationObservation(probeRequest, observation, postProbeNow) {
		return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, ErrRecoveryTargetUnavailable
	}

	var result settings.RecoveryTargetRootResolution
	var before, after, receiptBefore, receiptAfter targetRootPersistedMutationState
	replayed := false
	transactionErr := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lockStartedAt := service.now().UTC()
		if lockStartedAt.IsZero() || lockStartedAt.Before(postProbeNow) {
			return ErrRecoveryTargetUnavailable
		}
		locked, err := loadRecoveryTargetRootAuthorityNodeCredential(ctx, tx, request.NodeID, lockStartedAt, true)
		if err != nil {
			if errors.Is(err, settings.ErrRecoveryTargetRootNotFound) {
				return ErrRecoveryTargetUnavailable
			}
			return err
		}
		lockedNow := service.now().UTC()
		if lockedNow.IsZero() || lockedNow.Before(lockStartedAt) ||
			!validTargetRootRegistrationObservation(probeRequest, observation, lockedNow) ||
			(locked.credentialExpiresAt != nil && !locked.credentialExpiresAt.After(lockedNow)) {
			return ErrRecoveryTargetUnavailable
		}
		if locked.nodeRevision != captured.nodeRevision || locked.credentialRevision != captured.credentialRevision || observation.NodeRevision != locked.nodeRevision ||
			observation.CredentialRevision != locked.credentialRevision {
			return ErrRecoveryTargetUnavailable
		}
		before, err = loadTargetRootPersistedMutationState(ctx, tx, key, true)
		if err != nil {
			return err
		}
		current, currentErr := service.registry.ResolveRecoveryTargetRootTx(ctx, tx, request.NodeID, request.RootID)
		currentExists := currentErr == nil
		if currentErr != nil && !errors.Is(currentErr, settings.ErrRecoveryTargetRootNotFound) {
			return ErrRecoveryTargetUnavailable
		}
		if receiptKey != "" {
			receiptBefore, err = loadTargetRootPersistedMutationState(ctx, tx, receiptKey, true)
			if err != nil {
				return err
			}
			if receiptBefore.present {
				replay, replayErr := targetRootMutationReceiptFromState(
					receiptBefore, intentDigest, sessionDigest, service.now().UTC(),
				)
				if replayErr != nil {
					return replayErr
				}
				result = settings.RecoveryTargetRootResolution{
					NodeID: replay.NodeID, RootID: replay.RootID, SafeLabel: replay.SafeLabel,
				}
				replayed = true
				return nil
			}
			switch request.Mutation {
			case TargetRootMutationRegister:
				if currentExists {
					return ErrTargetRootMutationConflict
				}
			case TargetRootMutationRotate:
				if !currentExists {
					return settings.ErrRecoveryTargetRootNotFound
				}
			default:
				return settings.ErrRecoveryTargetRootInvalid
			}
		}

		authorityRevision := ""
		if currentExists && recoveryTargetRootRegistrationSecurityEquivalent(current, request, observation) {
			authorityRevision = current.AuthorityRevision
		} else {
			generated, generationErr := service.newRevision()
			if generationErr != nil || !validOpaqueID(generated) || (currentExists && generated == current.AuthorityRevision) {
				return ErrRecoveryTargetUnavailable
			}
			authorityRevision = generated
		}

		registered, registerErr := service.registry.RegisterRecoveryTargetRootTx(ctx, tx, settings.RecoveryTargetRootDefinition{
			NodeID: request.NodeID, RootID: request.RootID, SafeLabel: request.SafeLabel, Locator: request.Locator,
			AuthorityRevision: authorityRevision, RootObservationRevision: observation.RootObservationRevision,
			Policy: request.Policy,
		})
		if registerErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return ErrRecoveryTargetUnavailable
		}
		result = registered
		after, err = loadTargetRootPersistedMutationState(ctx, tx, key, false)
		if err != nil || !after.present {
			return ErrRecoveryTargetUnavailable
		}
		if receiptKey != "" {
			receiptAfter, err = createTargetRootMutationReceiptTx(
				ctx, tx, receiptKey, intentDigest, sessionDigest,
				settings.RecoveryTargetRootSummary{NodeID: result.NodeID, RootID: result.RootID, SafeLabel: result.SafeLabel},
				request.SessionExpiresAt, service.now().UTC(),
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if transactionErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, ctxErr
		}
		if errors.Is(transactionErr, ErrTargetRootIdempotencyConflict) ||
			errors.Is(transactionErr, ErrTargetRootMutationConflict) ||
			errors.Is(transactionErr, settings.ErrRecoveryTargetRootNotFound) ||
			errors.Is(transactionErr, settings.ErrRecoveryTargetRootInvalid) ||
			errors.Is(transactionErr, backupasset.ErrForbidden) {
			return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, transactionErr
		}
		return settings.RecoveryTargetRootResolution{}, TargetRootMutationRollback{}, ErrRecoveryTargetUnavailable
	}
	if replayed {
		return result, TargetRootMutationRollback{owner: service, noOp: true}, nil
	}
	return result, TargetRootMutationRollback{
		owner: service, key: key, before: before, after: after,
		receiptKey: receiptKey, receiptBefore: receiptBefore, receiptAfter: receiptAfter,
	}, nil
}

// Delete locks the node and exact registry row, then removes only that private
// record. It never invokes a target probe or any remote mutation path.
func (service *TargetRootAuthorityService) Delete(ctx context.Context, nodeID uint, rootID string) error {
	_, err := service.DeleteMutation(ctx, nodeID, rootID)
	return err
}

// ValidateDelete applies exact-reference validation before runtime drain.
func (service *TargetRootAuthorityService) ValidateDelete(nodeID uint, rootID string) error {
	if service == nil || service.db == nil || service.registry == nil || service.now == nil {
		return ErrRecoveryTargetUnavailable
	}
	return settings.ValidateRecoveryTargetRootReference(settings.RecoveryTargetRootReference{
		NodeID: nodeID, RootID: rootID,
	})
}

// DeleteMutation removes one exact private record and returns an opaque
// capability that can restore the byte-for-byte prior record.
func (service *TargetRootAuthorityService) DeleteMutation(
	ctx context.Context,
	nodeID uint,
	rootID string,
) (TargetRootMutationRollback, error) {
	if service == nil || service.db == nil || service.registry == nil || service.now == nil || ctx == nil {
		return TargetRootMutationRollback{}, ErrRecoveryTargetUnavailable
	}
	if err := ctx.Err(); err != nil {
		return TargetRootMutationRollback{}, err
	}
	if err := settings.ValidateRecoveryTargetRootReference(settings.RecoveryTargetRootReference{
		NodeID: nodeID,
		RootID: rootID,
	}); err != nil {
		return TargetRootMutationRollback{}, err
	}
	key := targetRootAuthorityRecordKey(nodeID, rootID)
	var before targetRootPersistedMutationState
	transactionErr := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockRecoveryTargetRootAuthorityNode(ctx, tx, nodeID); err != nil {
			return err
		}
		var err error
		before, err = loadTargetRootPersistedMutationState(ctx, tx, key, true)
		if err != nil || !before.present {
			return ErrRecoveryTargetUnavailable
		}
		if err := service.registry.DeleteRecoveryTargetRootTx(ctx, tx, nodeID, rootID); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return ErrRecoveryTargetUnavailable
		}
		after, err := loadTargetRootPersistedMutationState(ctx, tx, key, false)
		if err != nil || after.present {
			return ErrRecoveryTargetUnavailable
		}
		return nil
	})
	if transactionErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return TargetRootMutationRollback{}, ctxErr
		}
		return TargetRootMutationRollback{}, ErrRecoveryTargetUnavailable
	}
	return TargetRootMutationRollback{
		owner: service, key: key, before: before,
		after: targetRootPersistedMutationState{},
	}, nil
}

// DeleteAuthorizedMutation owns API replay, exact-session binding, the private
// registry delete, and its receipt in one transaction. It returns only the
// prior safe summary and an opaque transition rollback capability.
func (service *TargetRootAuthorityService) DeleteAuthorizedMutation(
	ctx context.Context,
	request TargetRootDeletionRequest,
) (settings.RecoveryTargetRootSummary, TargetRootMutationRollback, error) {
	if service == nil || service.db == nil || service.registry == nil || service.now == nil || ctx == nil {
		return settings.RecoveryTargetRootSummary{}, TargetRootMutationRollback{}, ErrRecoveryTargetUnavailable
	}
	if err := ctx.Err(); err != nil {
		return settings.RecoveryTargetRootSummary{}, TargetRootMutationRollback{}, err
	}
	receiptKey, intentDigest, sessionDigest, err := targetRootDeletionReceiptAuthority(request, service.now().UTC())
	if err != nil {
		return settings.RecoveryTargetRootSummary{}, TargetRootMutationRollback{}, err
	}
	if replay, found, replayErr := service.replayTargetRootMutation(ctx, receiptKey, intentDigest, sessionDigest); replayErr != nil {
		return settings.RecoveryTargetRootSummary{}, TargetRootMutationRollback{}, replayErr
	} else if found {
		return replay, TargetRootMutationRollback{owner: service, noOp: true}, nil
	}
	if !service.now().UTC().Before(request.SessionExpiresAt.UTC()) {
		return settings.RecoveryTargetRootSummary{}, TargetRootMutationRollback{}, backupasset.ErrForbidden
	}
	key := targetRootAuthorityRecordKey(request.NodeID, request.RootID)
	var result settings.RecoveryTargetRootSummary
	var before, after, receiptBefore, receiptAfter targetRootPersistedMutationState
	replayed := false
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if lockErr := lockRecoveryTargetRootAuthorityNode(ctx, tx, request.NodeID); lockErr != nil {
			return lockErr
		}
		var loadErr error
		before, loadErr = loadTargetRootPersistedMutationState(ctx, tx, key, true)
		if loadErr != nil {
			return loadErr
		}
		receiptBefore, loadErr = loadTargetRootPersistedMutationState(ctx, tx, receiptKey, true)
		if loadErr != nil {
			return loadErr
		}
		if receiptBefore.present {
			result, loadErr = targetRootMutationReceiptFromState(
				receiptBefore, intentDigest, sessionDigest, service.now().UTC(),
			)
			if loadErr != nil {
				return loadErr
			}
			replayed = true
			return nil
		}
		if !before.present {
			return settings.ErrRecoveryTargetRootNotFound
		}
		current, resolveErr := service.registry.ResolveRecoveryTargetRootTx(
			ctx, tx, request.NodeID, request.RootID,
		)
		if resolveErr != nil {
			if errors.Is(resolveErr, settings.ErrRecoveryTargetRootNotFound) {
				return settings.ErrRecoveryTargetRootNotFound
			}
			return ErrRecoveryTargetUnavailable
		}
		result = settings.RecoveryTargetRootSummary{
			NodeID: current.NodeID, RootID: current.RootID, SafeLabel: current.SafeLabel,
		}
		if deleteErr := service.registry.DeleteRecoveryTargetRootTx(
			ctx, tx, request.NodeID, request.RootID,
		); deleteErr != nil {
			return ErrRecoveryTargetUnavailable
		}
		after, loadErr = loadTargetRootPersistedMutationState(ctx, tx, key, false)
		if loadErr != nil || after.present {
			return ErrRecoveryTargetUnavailable
		}
		receiptAfter, loadErr = createTargetRootMutationReceiptTx(
			ctx, tx, receiptKey, intentDigest, sessionDigest, result, request.SessionExpiresAt, service.now().UTC(),
		)
		return loadErr
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return settings.RecoveryTargetRootSummary{}, TargetRootMutationRollback{}, ctxErr
		}
		if errors.Is(err, ErrTargetRootIdempotencyConflict) || errors.Is(err, backupasset.ErrForbidden) ||
			errors.Is(err, settings.ErrRecoveryTargetRootNotFound) {
			return settings.RecoveryTargetRootSummary{}, TargetRootMutationRollback{}, err
		}
		return settings.RecoveryTargetRootSummary{}, TargetRootMutationRollback{}, ErrRecoveryTargetUnavailable
	}
	if replayed {
		return result, TargetRootMutationRollback{owner: service, noOp: true}, nil
	}
	return result, TargetRootMutationRollback{
		owner: service, key: key, before: before, after: after,
		receiptKey: receiptKey, receiptBefore: receiptBefore, receiptAfter: receiptAfter,
	}, nil
}

// RestoreMutation restores the exact prior encrypted row or exact absence. A
// stale, replayed, or foreign token fails closed without changing the row.
func (service *TargetRootAuthorityService) RestoreMutation(
	ctx context.Context,
	rollback TargetRootMutationRollback,
) error {
	if service == nil || service.db == nil || ctx == nil || rollback.owner != service {
		return ErrRecoveryTargetUnavailable
	}
	if rollback.noOp {
		return nil
	}
	if rollback.key == "" {
		return ErrRecoveryTargetUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if rollback.receiptKey != "" {
			if restoreErr := restoreTargetRootPersistedMutationStateTx(
				ctx, tx, rollback.receiptKey, rollback.receiptBefore, rollback.receiptAfter,
			); restoreErr != nil {
				return restoreErr
			}
		}
		return restoreTargetRootPersistedMutationStateTx(ctx, tx, rollback.key, rollback.before, rollback.after)
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return ErrRecoveryTargetUnavailable
	}
	return nil
}

func targetRootAuthorityRecordKey(nodeID uint, rootID string) string {
	return settings.RecoveryTargetRootKeyPrefix + strconv.FormatUint(uint64(nodeID), 10) + "." + rootID
}

const (
	targetRootMutationReceiptSchemaVersion = 1
	targetRootMutationKeyDigestDomain      = "xirang/recovery/target-root-mutation/idempotency-key/v1"
	targetRootMutationIntentDigestDomain   = "xirang/recovery/target-root-mutation/intent/v1"
	targetRootMutationSessionDigestDomain  = "xirang/recovery/target-root-mutation/session/v1"
	targetRootRegisterEndpoint             = "/api/v1/settings/backup-assets/recovery/target-roots"
	targetRootItemEndpoint                 = "/api/v1/settings/backup-assets/recovery/target-roots/:nodeId/:rootId"
)

func targetRootRegistrationReceiptAuthority(
	request TargetRootRegistrationRequest,
	now time.Time,
) (string, string, string, error) {
	if err := validateTargetRootMutationSession(
		request.RequesterID, request.Endpoint, request.IdempotencyKey, request.SessionJTI,
		request.SessionRole, request.SessionTokenVersion, request.SessionExpiresAt, now,
	); err != nil {
		return "", "", "", err
	}
	expectedEndpoint := targetRootRegisterEndpoint
	if request.Mutation == TargetRootMutationRotate {
		expectedEndpoint = targetRootItemEndpoint
	} else if request.Mutation != TargetRootMutationRegister {
		return "", "", "", settings.ErrRecoveryTargetRootInvalid
	}
	if request.Endpoint != expectedEndpoint {
		return "", "", "", settings.ErrRecoveryTargetRootInvalid
	}
	locatorDigest, err := settings.RecoveryTargetRootLocatorDigest(request.NodeID, request.RootID, request.Locator)
	if err != nil {
		return "", "", "", settings.ErrRecoveryTargetRootInvalid
	}
	keyDigest := framedDigest(
		targetRootMutationKeyDigestDomain,
		strconv.FormatUint(uint64(request.RequesterID), 10), string(request.Mutation), request.Endpoint, request.IdempotencyKey,
	)
	intentDigest := framedDigest(
		targetRootMutationIntentDigestDomain,
		string(request.Mutation), strconv.FormatUint(uint64(request.NodeID), 10), request.RootID,
		request.SafeLabel, locatorDigest, strconv.FormatInt(request.Policy.ReserveBytes, 10),
		strconv.FormatInt(request.Policy.ReserveInodes, 10), request.Policy.OverlapPolicyBinding,
	)
	sessionDigest := targetRootMutationSessionDigest(
		request.RequesterID, request.SessionJTI, request.SessionRole,
		request.SessionTokenVersion, request.SessionExpiresAt,
	)
	return settings.RecoveryTargetRootReceiptKeyPrefix + keyDigest, intentDigest, sessionDigest, nil
}

func targetRootDeletionReceiptAuthority(
	request TargetRootDeletionRequest,
	now time.Time,
) (string, string, string, error) {
	if request.Mutation != TargetRootMutationDelete || request.Endpoint != targetRootItemEndpoint {
		return "", "", "", settings.ErrRecoveryTargetRootInvalid
	}
	if err := settings.ValidateRecoveryTargetRootReference(settings.RecoveryTargetRootReference{
		NodeID: request.NodeID, RootID: request.RootID,
	}); err != nil {
		return "", "", "", err
	}
	if err := validateTargetRootMutationSession(
		request.RequesterID, request.Endpoint, request.IdempotencyKey, request.SessionJTI,
		request.SessionRole, request.SessionTokenVersion, request.SessionExpiresAt, now,
	); err != nil {
		return "", "", "", err
	}
	keyDigest := framedDigest(
		targetRootMutationKeyDigestDomain,
		strconv.FormatUint(uint64(request.RequesterID), 10), string(request.Mutation), request.Endpoint, request.IdempotencyKey,
	)
	intentDigest := framedDigest(
		targetRootMutationIntentDigestDomain,
		string(request.Mutation), strconv.FormatUint(uint64(request.NodeID), 10), request.RootID,
	)
	sessionDigest := targetRootMutationSessionDigest(
		request.RequesterID, request.SessionJTI, request.SessionRole,
		request.SessionTokenVersion, request.SessionExpiresAt,
	)
	return settings.RecoveryTargetRootReceiptKeyPrefix + keyDigest, intentDigest, sessionDigest, nil
}

func validateTargetRootMutationSession(
	requesterID uint,
	endpoint string,
	idempotencyKey string,
	sessionJTI string,
	sessionRole string,
	sessionTokenVersion uint,
	sessionExpiresAt time.Time,
	now time.Time,
) error {
	if requesterID == 0 || endpoint == "" || !validPlanIdempotencyKey(idempotencyKey) ||
		!validOpaqueID(sessionJTI) || sessionRole != "admin" || sessionTokenVersion == 0 ||
		now.IsZero() || sessionExpiresAt.IsZero() {
		return backupasset.ErrForbidden
	}
	return nil
}

func targetRootMutationSessionDigest(
	requesterID uint,
	sessionJTI string,
	sessionRole string,
	sessionTokenVersion uint,
	sessionExpiresAt time.Time,
) string {
	return framedDigest(
		targetRootMutationSessionDigestDomain,
		strconv.FormatUint(uint64(requesterID), 10), sessionJTI, sessionRole,
		strconv.FormatUint(uint64(sessionTokenVersion), 10), sessionExpiresAt.UTC().Format(time.RFC3339Nano),
	)
}

func (service *TargetRootAuthorityService) replayTargetRootMutation(
	ctx context.Context,
	receiptKey string,
	intentDigest string,
	sessionDigest string,
) (settings.RecoveryTargetRootSummary, bool, error) {
	if service == nil || service.db == nil || service.now == nil || ctx == nil ||
		!strings.HasPrefix(receiptKey, settings.RecoveryTargetRootReceiptKeyPrefix) {
		return settings.RecoveryTargetRootSummary{}, false, ErrRecoveryTargetUnavailable
	}
	state, err := loadTargetRootPersistedMutationState(ctx, service.db, receiptKey, false)
	if err != nil {
		return settings.RecoveryTargetRootSummary{}, false, err
	}
	if !state.present {
		return settings.RecoveryTargetRootSummary{}, false, nil
	}
	result, err := targetRootMutationReceiptFromState(
		state, intentDigest, sessionDigest, service.now().UTC(),
	)
	if err != nil {
		return settings.RecoveryTargetRootSummary{}, true, err
	}
	return result, true, nil
}

func targetRootMutationReceiptFromState(
	state targetRootPersistedMutationState,
	intentDigest string,
	sessionDigest string,
	now time.Time,
) (settings.RecoveryTargetRootSummary, error) {
	if !state.present || !validDigest(intentDigest) || !validDigest(sessionDigest) || now.IsZero() {
		return settings.RecoveryTargetRootSummary{}, ErrRecoveryTargetUnavailable
	}
	var receipt targetRootMutationReceipt
	if err := json.Unmarshal([]byte(state.row.Value), &receipt); err != nil {
		return settings.RecoveryTargetRootSummary{}, ErrRecoveryTargetUnavailable
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(canonical, []byte(state.row.Value)) ||
		receipt.SchemaVersion != targetRootMutationReceiptSchemaVersion ||
		!validDigest(receipt.IntentDigest) || !validDigest(receipt.SessionDigest) ||
		receipt.SessionExpiresAt.IsZero() ||
		settings.ValidateRecoveryTargetRootReference(settings.RecoveryTargetRootReference{
			NodeID: receipt.Result.NodeID, RootID: receipt.Result.RootID,
		}) != nil || !validTargetRootRegistrationSafeLabel(receipt.Result.SafeLabel) {
		return settings.RecoveryTargetRootSummary{}, ErrRecoveryTargetUnavailable
	}
	if subtle.ConstantTimeCompare([]byte(receipt.IntentDigest), []byte(intentDigest)) != 1 {
		return settings.RecoveryTargetRootSummary{}, ErrTargetRootIdempotencyConflict
	}
	if subtle.ConstantTimeCompare([]byte(receipt.SessionDigest), []byte(sessionDigest)) != 1 {
		return settings.RecoveryTargetRootSummary{}, backupasset.ErrForbidden
	}
	if !now.Before(receipt.SessionExpiresAt.UTC()) {
		return settings.RecoveryTargetRootSummary{}, ErrTargetRootIdempotencyConflict
	}
	return receipt.Result, nil
}

func createTargetRootMutationReceiptTx(
	ctx context.Context,
	tx *gorm.DB,
	receiptKey string,
	intentDigest string,
	sessionDigest string,
	result settings.RecoveryTargetRootSummary,
	sessionExpiresAt time.Time,
	now time.Time,
) (targetRootPersistedMutationState, error) {
	if ctx == nil || tx == nil || !strings.HasPrefix(receiptKey, settings.RecoveryTargetRootReceiptKeyPrefix) ||
		!validDigest(intentDigest) || !validDigest(sessionDigest) || now.IsZero() ||
		sessionExpiresAt.IsZero() || !now.Before(sessionExpiresAt.UTC()) ||
		settings.ValidateRecoveryTargetRootReference(settings.RecoveryTargetRootReference{
			NodeID: result.NodeID, RootID: result.RootID,
		}) != nil || !validTargetRootRegistrationSafeLabel(result.SafeLabel) {
		return targetRootPersistedMutationState{}, backupasset.ErrForbidden
	}
	encoded, err := json.Marshal(targetRootMutationReceipt{
		SchemaVersion: targetRootMutationReceiptSchemaVersion, IntentDigest: intentDigest,
		SessionDigest: sessionDigest, SessionExpiresAt: sessionExpiresAt.UTC(), Result: result,
	})
	if err != nil {
		return targetRootPersistedMutationState{}, ErrRecoveryTargetUnavailable
	}
	row := model.SystemSetting{Key: receiptKey, Value: string(encoded)}
	if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
		return targetRootPersistedMutationState{}, ErrRecoveryTargetUnavailable
	}
	created, err := loadTargetRootPersistedMutationState(ctx, tx, receiptKey, false)
	if err != nil || !created.present || created.row.Value != string(encoded) {
		return targetRootPersistedMutationState{}, ErrRecoveryTargetUnavailable
	}
	return created, nil
}

func loadTargetRootPersistedMutationState(
	ctx context.Context,
	tx *gorm.DB,
	key string,
	lock bool,
) (targetRootPersistedMutationState, error) {
	query := tx.WithContext(ctx)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var rows []model.SystemSetting
	if err := query.Where("key = ?", key).Limit(2).Find(&rows).Error; err != nil || len(rows) > 1 {
		return targetRootPersistedMutationState{}, ErrRecoveryTargetUnavailable
	}
	if len(rows) == 0 {
		return targetRootPersistedMutationState{}, nil
	}
	return targetRootPersistedMutationState{row: rows[0], present: true}, nil
}

func targetRootPersistedMutationStatesEqual(left, right targetRootPersistedMutationState) bool {
	return left.present == right.present && (!left.present || left.row == right.row)
}

func restoreTargetRootPersistedMutationStateTx(
	ctx context.Context,
	tx *gorm.DB,
	key string,
	before targetRootPersistedMutationState,
	after targetRootPersistedMutationState,
) error {
	current, err := loadTargetRootPersistedMutationState(ctx, tx, key, true)
	if err != nil || !targetRootPersistedMutationStatesEqual(current, after) {
		return ErrRecoveryTargetUnavailable
	}
	switch {
	case !before.present && after.present:
		result := tx.WithContext(ctx).Where(
			"key = ? AND value = ? AND updated_at = ?", key, after.row.Value, after.row.UpdatedAt,
		).Delete(&model.SystemSetting{})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrRecoveryTargetUnavailable
		}
	case before.present && !after.present:
		if createErr := tx.WithContext(ctx).Create(&before.row).Error; createErr != nil {
			return ErrRecoveryTargetUnavailable
		}
	case before.present && after.present:
		result := tx.WithContext(ctx).Model(&model.SystemSetting{}).
			Where("key = ? AND value = ? AND updated_at = ?", key, after.row.Value, after.row.UpdatedAt).
			UpdateColumns(map[string]any{"value": before.row.Value, "updated_at": before.row.UpdatedAt})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrRecoveryTargetUnavailable
		}
	case !before.present && !after.present:
		return nil
	default:
		return ErrRecoveryTargetUnavailable
	}
	restored, err := loadTargetRootPersistedMutationState(ctx, tx, key, false)
	if err != nil || !targetRootPersistedMutationStatesEqual(restored, before) {
		return ErrRecoveryTargetUnavailable
	}
	return nil
}

func lockRecoveryTargetRootAuthorityNode(ctx context.Context, db *gorm.DB, nodeID uint) error {
	if ctx == nil || db == nil || nodeID == 0 {
		return ErrRecoveryTargetUnavailable
	}
	type nodeRow struct {
		ID uint
	}
	var node nodeRow
	loaded := db.WithContext(ctx).Table("nodes").Select("id").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", nodeID).Limit(1).Find(&node)
	if loaded.Error != nil {
		return recoveryTargetUnavailableForContext(ctx)
	}
	if loaded.RowsAffected != 1 || node.ID != nodeID {
		return settings.ErrRecoveryTargetRootNotFound
	}
	return nil
}

// List returns only the registry's reviewed safe summary DTO.
func (service *TargetRootAuthorityService) List(
	ctx context.Context,
	nodeID uint,
) ([]settings.RecoveryTargetRootSummary, error) {
	if service == nil || service.registry == nil || ctx == nil {
		return nil, ErrRecoveryTargetUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if nodeID == 0 {
		return nil, settings.ErrRecoveryTargetRootInvalid
	}
	summaries, err := service.registry.ListRecoveryTargetRoots(ctx, nodeID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, settings.ErrRecoveryTargetRootNotFound) {
			return nil, err
		}
		return nil, ErrRecoveryTargetUnavailable
	}
	return summaries, nil
}

func validateTargetRootRegistrationRequest(request TargetRootRegistrationRequest) error {
	if request.NodeID == 0 || request.RootID == "" || !validTargetRootRegistrationSafeLabel(request.SafeLabel) || request.Locator == "" ||
		request.Policy.ReserveBytes < 0 || request.Policy.ReserveInodes < 0 ||
		!validTargetRootRegistrationOpaqueBinding(request.Policy.OverlapPolicyBinding) {
		return settings.ErrRecoveryTargetRootInvalid
	}
	if _, err := settings.RecoveryTargetRootLocatorDigest(request.NodeID, request.RootID, request.Locator); err != nil {
		return settings.ErrRecoveryTargetRootInvalid
	}
	return nil
}

func validTargetRootRegistrationSafeLabel(value string) bool {
	if !utf8.ValidString(value) || len(value) == 0 || len(value) > 128 || strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validTargetRootRegistrationOpaqueBinding(value string) bool {
	if !utf8.ValidString(value) || len(value) == 0 || len(value) > 256 || strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validTargetRootRegistrationObservation(
	request TargetRootRegistrationRequest,
	observation TargetRootRegistrationObservation,
	now time.Time,
) bool {
	expectedDigest, err := settings.RecoveryTargetRootLocatorDigest(request.NodeID, request.RootID, request.Locator)
	if err != nil || observation.NodeID != request.NodeID || observation.RootID != request.RootID ||
		observation.LocatorDigest != expectedDigest || observation.Purpose != TargetRootRegistrationPurposeReadOnly ||
		!observation.ReadOnly || observation.NodeRevision != request.NodeRevision ||
		observation.CredentialRevision != request.CredentialRevision ||
		!validOpaqueRevision(observation.NodeRevision) || !validOpaqueRevision(observation.CredentialRevision) ||
		!validOpaqueRevision(observation.RootObservationRevision) || observation.ObservedAt.IsZero() ||
		observation.ObservedAt.After(now) || now.Sub(observation.ObservedAt) > targetRootRegistrationProbeFreshness {
		return false
	}
	return true
}

func recoveryTargetRootRegistrationSecurityEquivalent(
	current settings.RecoveryTargetRootResolution,
	request TargetRootRegistrationRequest,
	observation TargetRootRegistrationObservation,
) bool {
	expectedDigest, err := settings.RecoveryTargetRootLocatorDigest(request.NodeID, request.RootID, request.Locator)
	return err == nil && current.LocatorDigest == expectedDigest &&
		current.RootObservationRevision == observation.RootObservationRevision && current.Policy == request.Policy
}

type recoveryTargetRootAuthorityNodeCredential struct {
	nodeRevision        string
	credentialRevision  string
	credentialExpiresAt *time.Time
}

func loadRecoveryTargetRootAuthorityNodeCredential(
	ctx context.Context,
	db *gorm.DB,
	nodeID uint,
	now time.Time,
	lock bool,
) (recoveryTargetRootAuthorityNodeCredential, error) {
	if ctx == nil || db == nil || nodeID == 0 || now.IsZero() {
		return recoveryTargetRootAuthorityNodeCredential{}, ErrRecoveryTargetUnavailable
	}
	type nodeRow struct {
		ID        uint
		Host      string
		Port      int
		Username  string
		AuthType  string
		SSHKeyID  *uint
		Archived  bool
		UpdatedAt time.Time
	}
	nodeQuery := db.WithContext(ctx).Table("nodes").
		Select("id", "host", "port", "username", "auth_type", "ssh_key_id", "archived", "updated_at").
		Where("id = ?", nodeID).Limit(1)
	if lock {
		nodeQuery = nodeQuery.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var node nodeRow
	loaded := nodeQuery.Find(&node)
	if loaded.Error != nil {
		return recoveryTargetRootAuthorityNodeCredential{}, recoveryTargetUnavailableForContext(ctx)
	}
	if loaded.RowsAffected != 1 || node.ID != nodeID || node.Archived {
		return recoveryTargetRootAuthorityNodeCredential{}, settings.ErrRecoveryTargetRootNotFound
	}
	if node.AuthType != "key" || node.SSHKeyID == nil || *node.SSHKeyID == 0 {
		return recoveryTargetRootAuthorityNodeCredential{}, ErrRecoveryTargetUnavailable
	}

	type credentialRow struct {
		ID              uint
		Username        string
		KeyType         string
		PrivateKey      string
		Fingerprint     string
		Disabled        bool
		ExpiresAt       *time.Time
		AllowedPurposes string
		AllowedNodeIDs  string
		AllowedNodeTags string
		UpdatedAt       time.Time
	}
	credentialQuery := db.WithContext(ctx).Table("ssh_keys").
		Select("id", "username", "key_type", "private_key", "fingerprint", "disabled", "expires_at", "allowed_purposes", "allowed_node_ids", "allowed_node_tags", "updated_at").
		Where("id = ?", *node.SSHKeyID).Limit(1)
	if lock {
		credentialQuery = credentialQuery.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var credential credentialRow
	loaded = credentialQuery.Find(&credential)
	if loaded.Error != nil || loaded.RowsAffected != 1 || credential.ID != *node.SSHKeyID || credential.Disabled ||
		credential.PrivateKey == "" || credential.Fingerprint == "" ||
		(credential.ExpiresAt != nil && !credential.ExpiresAt.After(now)) {
		return recoveryTargetRootAuthorityNodeCredential{}, recoveryTargetUnavailableForContext(ctx)
	}

	return recoveryTargetRootAuthorityNodeCredential{
		nodeRevision: framedDigest(
			targetRootRegistrationNodeRevisionDomain,
			strconv.FormatUint(uint64(node.ID), 10), node.Host, strconv.Itoa(node.Port), node.Username,
			node.AuthType, strconv.FormatUint(uint64(*node.SSHKeyID), 10), strconv.FormatBool(node.Archived),
			node.UpdatedAt.UTC().Format(time.RFC3339Nano),
		),
		credentialRevision: framedDigest(
			targetRootRegistrationCredentialDomain,
			strconv.FormatUint(uint64(credential.ID), 10), credential.Username, credential.KeyType,
			credential.PrivateKey, credential.Fingerprint, strconv.FormatBool(credential.Disabled),
			authorizationIntentTimePointer(credential.ExpiresAt), credential.AllowedPurposes,
			credential.AllowedNodeIDs, credential.AllowedNodeTags,
			credential.UpdatedAt.UTC().Format(time.RFC3339Nano),
		), credentialExpiresAt: credential.ExpiresAt,
	}, nil
}

// PlanServiceDependencies provides the database, private target-root resolver,
// and injectable clock for plan creation. It intentionally has no Provider
// dependency: creating a plan only revalidates durable facts and never performs
// Provider I/O.
type PlanServiceDependencies struct {
	DB                 *gorm.DB
	Now                func() time.Time
	Audit              RecoveryAPIAuditWriter
	TargetRootResolver RecoveryTargetRootResolver
	Policy             PlanPolicy
	PreflightPolicy    PreflightPolicy
	beforePersist      func(planCreateStage, int) error
}

const (
	recoveryPlanPolicyMaxSelectionItems int64 = 100_000
	recoveryPlanMaxLogicalBytes         int64 = 1 << 40
)

// PlanPolicy is the immutable installed admission policy for one plan service.
type PlanPolicy struct {
	MaxSelectionItems int64
	MaxLogicalBytes   int64
}

func (policy PlanPolicy) valid() bool {
	return policy.MaxSelectionItems > 0 && policy.MaxSelectionItems <= recoveryPlanPolicyMaxSelectionItems &&
		policy.MaxLogicalBytes > 0 && policy.MaxLogicalBytes <= recoveryPlanMaxLogicalBytes
}

func (policy PlanPolicy) effectiveSelectionLimit() int64 {
	return min(policy.MaxSelectionItems, int64(exactSelectionMaxItems))
}

// PlanService persists an exact recovery plan and its canonical items as one
// transaction. Preflight, grants, jobs, attempts, and leases are deliberately
// outside this creation boundary.
type PlanService struct {
	db                 *gorm.DB
	now                func() time.Time
	audit              RecoveryAPIAuditWriter
	targetRootResolver RecoveryTargetRootResolver
	policy             PlanPolicy
	preflightPolicy    PreflightPolicy
	beforePersist      func(planCreateStage, int) error
}

// NewPlanService creates a plan service over the supplied database.
func NewPlanService(dependencies PlanServiceDependencies) (*PlanService, error) {
	if dependencies.DB == nil || dependencies.TargetRootResolver == nil || !dependencies.Policy.valid() ||
		!dependencies.PreflightPolicy.valid() {
		return nil, ErrRecoveryPlanUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &PlanService{
		db: dependencies.DB, now: dependencies.Now, audit: dependencies.Audit,
		targetRootResolver: dependencies.TargetRootResolver,
		policy:             dependencies.Policy,
		preflightPolicy:    dependencies.PreflightPolicy,
		beforePersist:      dependencies.beforePersist,
	}, nil
}

// CreatePlan creates or replays the sole durable plan for a requester,
// endpoint, and idempotency key. The transaction belongs to the service.
func (service *PlanService) CreatePlan(ctx context.Context, request CreatePlanRequest) (CreatePlanResult, error) {
	if service == nil || service.db == nil || service.targetRootResolver == nil {
		return CreatePlanResult{}, ErrRecoveryPlanUnavailable
	}
	ctx = sourceValidationContext(ctx)
	if err := ctx.Err(); err != nil {
		return CreatePlanResult{}, err
	}
	now := service.now().UTC()
	request.Plan.PreflightExpiresAt = now.Add(service.preflightPolicy.TTL).UTC()
	normalized, err := normalizeCreatePlanRequest(request, now)
	if err != nil {
		return CreatePlanResult{}, err
	}
	if service.validatePlanPolicy(normalized) != nil {
		return CreatePlanResult{}, ErrExactSelectionLimit
	}
	keyDigest := planIdempotencyKeyDigest(normalized.RequesterID, normalized.Endpoint, normalized.IdempotencyKey)

	for attempt := 0; attempt < planCreateRetryAttempts; attempt++ {
		var result CreatePlanResult
		err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var createErr error
			result, createErr = service.createPlanTx(ctx, tx, normalized, now)
			if createErr != nil {
				return createErr
			}
			return service.beforePlanPersist(planCreateBeforeCommit, -1)
		})
		err = classifyPlanCreateError(ctx, err)
		if err == nil {
			if !result.Replay {
				service.writeCreatePlanAudit(ctx, normalized, result)
			}
			return result, nil
		}
		if !errors.Is(err, errPlanIdempotencyRace) && !errors.Is(err, errPlanDatabaseBusy) {
			return CreatePlanResult{}, publicPlanCreateError(ctx, err)
		}
		replay, found, replayErr := loadPlanReplayTx(
			ctx, service.db, normalized.RequesterID, normalized.Endpoint, keyDigest,
		)
		if replayErr == nil && found {
			return planCreateResultFromReplay(normalized, replay)
		}
		if replayErr != nil && !errors.Is(replayErr, errPlanIdempotencyRace) &&
			!errors.Is(replayErr, errPlanDatabaseBusy) {
			return CreatePlanResult{}, publicPlanCreateError(ctx, replayErr)
		}
		if attempt+1 == planCreateRetryAttempts {
			replay, found, replayErr = waitForPlanCreateWinner(ctx, service.db, normalized, keyDigest)
			if replayErr != nil {
				return CreatePlanResult{}, publicPlanCreateError(ctx, replayErr)
			}
			if found {
				return planCreateResultFromReplay(normalized, replay)
			}
			break
		}
		if err := waitForPlanCreateRetry(ctx, attempt); err != nil {
			return CreatePlanResult{}, err
		}
	}
	return CreatePlanResult{}, ErrRecoveryPlanUnavailable
}

func (service *PlanService) writeCreatePlanAudit(
	ctx context.Context,
	request CreatePlanRequest,
	result CreatePlanResult,
) {
	if service == nil || service.audit == nil {
		return
	}
	auditCtx, cancel := context.WithTimeout(
		context.WithoutCancel(sourceValidationContext(ctx)), authorizationAuditTimeout,
	)
	defer cancel()
	_, _ = service.audit.Write(auditCtx, backupasset.AuditEventInput{
		Actor:           backupasset.AuditActor{UserID: request.RequesterID},
		Action:          backupasset.AuditActionRecoveryPlan,
		RepositoryID:    request.Selection.RepositoryID,
		RecoveryPointID: request.Selection.RecoveryPointID,
		ItemCount:       request.EstimatedItems,
		ByteCount:       request.EstimatedBytes,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldStage:  "create",
			backupasset.AuditFieldStatus: string(result.State),
		},
	})
}

func waitForPlanCreateWinner(
	ctx context.Context,
	db *gorm.DB,
	request CreatePlanRequest,
	keyDigest string,
) (planReplayRow, bool, error) {
	for attempt := 0; attempt < planCreateRetryAttempts; attempt++ {
		if err := waitForPlanCreateRetry(ctx, attempt); err != nil {
			return planReplayRow{}, false, err
		}
		replay, found, err := loadPlanReplayTx(ctx, db, request.RequesterID, request.Endpoint, keyDigest)
		if err == nil {
			if found {
				return replay, true, nil
			}
			continue
		}
		if !errors.Is(err, errPlanIdempotencyRace) && !errors.Is(err, errPlanDatabaseBusy) {
			return planReplayRow{}, false, err
		}
	}
	return planReplayRow{}, false, nil
}

// CreatePlanTx performs the plan write inside a caller-owned transaction. A
// scoped savepoint removes only this method's write set on failure; the method
// never begins, commits, or rolls back the caller's transaction.
func (service *PlanService) CreatePlanTx(ctx context.Context, tx *gorm.DB, request CreatePlanRequest) (CreatePlanResult, error) {
	if service == nil || service.db == nil || service.targetRootResolver == nil || tx == nil || tx.Error != nil {
		return CreatePlanResult{}, ErrRecoveryPlanUnavailable
	}
	ctx = sourceValidationContext(ctx)
	if err := ctx.Err(); err != nil {
		return CreatePlanResult{}, err
	}
	now := service.now().UTC()
	request.Plan.PreflightExpiresAt = now.Add(service.preflightPolicy.TTL).UTC()
	normalized, err := normalizeCreatePlanRequest(request, now)
	if err != nil {
		return CreatePlanResult{}, err
	}
	if service.validatePlanPolicy(normalized) != nil {
		return CreatePlanResult{}, ErrExactSelectionLimit
	}

	savepoint := fmt.Sprintf("recovery_plan_create_%d", planSavepointSequence.Add(1))
	if err := tx.WithContext(ctx).Exec("SAVEPOINT " + savepoint).Error; err != nil {
		return CreatePlanResult{}, ErrRecoveryPlanUnavailable
	}
	result, createErr := service.createPlanTx(ctx, tx, normalized, now)
	if createErr != nil {
		rollbackTx := tx.WithContext(context.WithoutCancel(ctx))
		if err := rollbackTx.Exec("ROLLBACK TO SAVEPOINT " + savepoint).Error; err != nil {
			return CreatePlanResult{}, ErrRecoveryPlanUnavailable
		}
		if err := rollbackTx.Exec("RELEASE SAVEPOINT " + savepoint).Error; err != nil {
			return CreatePlanResult{}, ErrRecoveryPlanUnavailable
		}
		return CreatePlanResult{}, publicPlanCreateError(ctx, createErr)
	}
	if err := tx.WithContext(ctx).Exec("RELEASE SAVEPOINT " + savepoint).Error; err != nil {
		return CreatePlanResult{}, ErrRecoveryPlanUnavailable
	}
	return result, nil
}

func (service *PlanService) createPlanTx(
	ctx context.Context,
	tx *gorm.DB,
	request CreatePlanRequest,
	now time.Time,
) (CreatePlanResult, error) {
	keyDigest := planIdempotencyKeyDigest(request.RequesterID, request.Endpoint, request.IdempotencyKey)
	replay, found, err := loadPlanReplayTx(ctx, tx, request.RequesterID, request.Endpoint, keyDigest)
	if err != nil {
		return CreatePlanResult{}, err
	}
	if found {
		return planCreateResultFromReplay(request, replay)
	}

	source, err := loadValidatedSource(
		ctx, tx, request.Selection.RepositoryID, request.Selection.RecoveryPointID, request.Selection.CatalogGenerationID,
	)
	if err != nil {
		return CreatePlanResult{}, sanitizePlanSourceError(ctx, err)
	}
	if !secure.IsEncrypted(source.point.EncryptedProviderLocator) {
		return CreatePlanResult{}, ErrRecoverySourceChanged
	}
	currentRevision, currentPrivateBinding, err := source.frozenSourceRevision()
	if err != nil {
		return CreatePlanResult{}, sanitizePlanSourceError(ctx, err)
	}
	if !sameSourceRevision(request.Selection.SourceRevision, currentRevision) ||
		!sameFrozenSourceBinding(request.Selection.privateSourceBinding, &currentPrivateBinding) {
		return CreatePlanResult{}, ErrRecoverySourceChanged
	}
	if err := revalidateSelectedEntries(ctx, tx, source, request.Selection.AssetRefs); err != nil {
		return CreatePlanResult{}, sanitizePlanSourceError(ctx, err)
	}
	targetRoot, err := service.resolvePlanTargetRoot(ctx, tx, request.Plan.Binding.Target)
	if err != nil {
		return CreatePlanResult{}, err
	}

	planID, err := backupasset.NewOpaqueID()
	if err != nil {
		return CreatePlanResult{}, ErrRecoveryPlanUnavailable
	}
	plan := recoveryPlanRow(request, keyDigest, planID, source, targetRoot, now)
	items, logicalBytes, err := recoveryPlanItemRows(ctx, tx, source, request.Selection, planID, now)
	if err != nil {
		return CreatePlanResult{}, err
	}
	if int64(len(items)) > service.policy.effectiveSelectionLimit() || logicalBytes > service.policy.MaxLogicalBytes {
		return CreatePlanResult{}, ErrExactSelectionLimit
	}
	if err := service.beforePlanPersist(planCreateBeforePlanInsert, -1); err != nil {
		return CreatePlanResult{}, err
	}
	if err := tx.WithContext(ctx).Create(&plan).Error; err != nil {
		return CreatePlanResult{}, sanitizePlanDatabaseError(ctx, err)
	}
	for index := range items {
		if err := service.beforePlanPersist(planCreateBeforeItemInsert, items[index].Ordinal); err != nil {
			return CreatePlanResult{}, err
		}
		if err := tx.WithContext(ctx).Create(&items[index]).Error; err != nil {
			return CreatePlanResult{}, sanitizePlanDatabaseError(ctx, err)
		}
	}
	return CreatePlanResult{PlanID: planID, State: PlanStateDraft}, nil
}

func (service *PlanService) validatePlanPolicy(request CreatePlanRequest) error {
	if service == nil || !service.policy.valid() || int64(len(request.Selection.AssetRefs)) > service.policy.effectiveSelectionLimit() ||
		request.EstimatedItems > service.policy.effectiveSelectionLimit() || request.EstimatedBytes > service.policy.MaxLogicalBytes {
		return ErrExactSelectionLimit
	}
	return nil
}

func (service *PlanService) resolvePlanTargetRoot(
	ctx context.Context,
	tx *gorm.DB,
	target TargetBinding,
) (settings.RecoveryTargetRootResolution, error) {
	resolved, err := service.targetRootResolver.ResolveRecoveryTargetRootTx(ctx, tx, target.NodeID, target.RootID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return settings.RecoveryTargetRootResolution{}, ctxErr
		}
		switch {
		case errors.Is(err, context.Canceled):
			return settings.RecoveryTargetRootResolution{}, context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			return settings.RecoveryTargetRootResolution{}, context.DeadlineExceeded
		case errors.Is(err, settings.ErrRecoveryTargetRootNotFound):
			return settings.RecoveryTargetRootResolution{}, ErrRecoveryTargetChanged
		default:
			return settings.RecoveryTargetRootResolution{}, ErrRecoveryPlanUnavailable
		}
	}
	if resolved.NodeID != target.NodeID || resolved.RootID != target.RootID {
		return settings.RecoveryTargetRootResolution{}, ErrRecoveryTargetChanged
	}
	digest, digestErr := settings.RecoveryTargetRootLocatorDigest(
		resolved.NodeID, resolved.RootID, resolved.Locator,
	)
	if digestErr != nil || digest != resolved.LocatorDigest {
		return settings.RecoveryTargetRootResolution{}, ErrRecoveryPlanUnavailable
	}
	if resolved.LocatorDigest != target.RootLocatorDigest {
		return settings.RecoveryTargetRootResolution{}, ErrRecoveryTargetChanged
	}
	return resolved, nil
}

func (service *PlanService) beforePlanPersist(stage planCreateStage, itemOrdinal int) error {
	if service.beforePersist == nil {
		return nil
	}
	if err := service.beforePersist(stage, itemOrdinal); err != nil {
		return &planCreateFaultError{cause: err}
	}
	return nil
}

type planReplayRow struct {
	ID                         string
	BindingDigest              string
	State                      string
	PreflightRevision          string
	PreflightExpiresAt         time.Time
	TargetNodeID               uint
	TargetRootID               string
	EncryptedTargetRootLocator string
	RootLocatorDigest          string
}

func planCreateResultFromReplay(request CreatePlanRequest, replay planReplayRow) (CreatePlanResult, error) {
	request.Plan.PreflightExpiresAt = replay.PreflightExpiresAt.UTC()
	request.Plan.Binding.PreflightRevision = replay.PreflightRevision
	request.Plan.Binding.PlanDigest = planIntentDigest(request)
	if replay.BindingDigest != request.Plan.Binding.PlanDigest {
		return CreatePlanResult{}, ErrPlanIdempotencyConflict
	}
	return CreatePlanResult{PlanID: replay.ID, State: PlanState(replay.State), Replay: true}, nil
}

func loadPlanReplayTx(
	ctx context.Context,
	tx *gorm.DB,
	requesterID uint,
	endpoint, keyDigest string,
) (planReplayRow, bool, error) {
	var row planReplayRow
	result := tx.WithContext(ctx).Table((model.BackupAssetRecoveryPlan{}).TableName()).
		Select("id, binding_digest, state, preflight_revision, preflight_expires_at, target_node_id, target_root_id, encrypted_target_root_locator, root_locator_digest").
		Where("requester_id = ? AND endpoint = ? AND idempotency_key_digest = ?", requesterID, endpoint, keyDigest).
		Limit(1).Find(&row)
	if result.Error != nil {
		return planReplayRow{}, false, sanitizePlanDatabaseError(ctx, result.Error)
	}
	if result.RowsAffected == 0 {
		return planReplayRow{}, false, nil
	}
	if !validOpaqueID(row.ID) || !validDigest(row.BindingDigest) || !PlanState(row.State).Valid() ||
		!validOpaqueRevision(row.PreflightRevision) || row.PreflightExpiresAt.IsZero() ||
		!strings.HasPrefix(row.EncryptedTargetRootLocator, "enc:v2:") {
		return planReplayRow{}, false, ErrRecoveryPlanUnavailable
	}
	locator, decryptErr := secure.DecryptString(row.EncryptedTargetRootLocator)
	if decryptErr != nil {
		return planReplayRow{}, false, ErrRecoveryPlanUnavailable
	}
	digest, digestErr := settings.RecoveryTargetRootLocatorDigest(row.TargetNodeID, row.TargetRootID, locator)
	if digestErr != nil || digest != row.RootLocatorDigest {
		return planReplayRow{}, false, ErrRecoveryPlanUnavailable
	}
	return row, true, nil
}

func normalizeCreatePlanRequest(request CreatePlanRequest, now time.Time) (CreatePlanRequest, error) {
	if now.IsZero() || request.RequesterID == 0 || !validPlanEndpoint(request.Endpoint) ||
		!validPlanIdempotencyKey(request.IdempotencyKey) || request.AuthorityCategory.Validate() != nil ||
		request.EstimatedItems < 0 || request.EstimatedBytes < 0 || request.Selection.Validate() != nil ||
		!request.Selection.hasPrivateSourceBinding() {
		return CreatePlanRequest{}, ErrInvalidRecoveryPlan
	}

	normalized := copyCreatePlanRequest(request)
	normalized.Plan.Binding.PlanDigest = strings.Repeat("0", sha256DigestLength)
	if normalized.Plan.ValidateAt(now) != nil ||
		normalized.Plan.Binding.SelectionDigest != normalized.Selection.SelectionDigest ||
		normalized.Plan.Binding.RepositoryID != normalized.Selection.RepositoryID ||
		normalized.Plan.Binding.RecoveryPointID != normalized.Selection.RecoveryPointID ||
		normalized.Plan.Binding.SourceRevisionDigest != normalized.Selection.SourceRevisionDigest ||
		!sameSourceRevision(normalized.Plan.Binding.SourceRevision, normalized.Selection.SourceRevision) {
		return CreatePlanRequest{}, ErrInvalidRecoveryPlan
	}
	normalized.Plan.Binding.PlanDigest = planIntentDigest(normalized)
	return normalized, nil
}

func copyCreatePlanRequest(request CreatePlanRequest) CreatePlanRequest {
	result := request
	result.Selection.AssetRefs = append([]backupasset.AssetRef(nil), request.Selection.AssetRefs...)
	result.Selection.SourceRevision = cloneSourceRevision(request.Selection.SourceRevision)
	result.Selection.privateSourceBinding = cloneFrozenSourceBinding(request.Selection.privateSourceBinding)
	result.Plan.Binding.SourceRevision = cloneSourceRevision(request.Plan.Binding.SourceRevision)
	return result
}

func validPlanEndpoint(value string) bool {
	if value == "/api/v1/recovery-plans" {
		return true
	}
	if !validBoundedOpaque(value, 64) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' && character != '.' {
			return false
		}
	}
	return true
}

func validPlanIdempotencyKey(value string) bool {
	if len(value) < planIdempotencyKeyMinBytes || len(value) > planIdempotencyKeyMaxBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' && character != '.' && character != '~' {
			return false
		}
	}
	return true
}

func planIdempotencyKeyDigest(requesterID uint, endpoint, key string) string {
	return framedDigest(planIdempotencyDigestDomain, strconv.FormatUint(uint64(requesterID), 10), endpoint, key)
}

func planIntentDigest(request CreatePlanRequest) string {
	binding := request.Plan.Binding
	revision := request.Selection.SourceRevision
	privateBinding := request.Selection.privateSourceBinding
	values := []string{
		strconv.Itoa(binding.SchemaVersion), request.Endpoint,
		request.Selection.RepositoryID, request.Selection.RecoveryPointID, request.Selection.CatalogGenerationID,
		request.Selection.SelectionDigest, request.Selection.SourceRevisionDigest, string(revision.Kind),
		string(binding.Target.Mode), strconv.FormatUint(uint64(binding.Target.NodeID), 10), binding.Target.RootID,
		binding.Target.RootLocatorDigest, binding.Target.PathDigest, binding.Target.BaseNodeRevision,
		binding.Target.CredentialScopeRevision, binding.Target.RootRevision, binding.Target.FilesystemRevision,
		string(binding.ConflictPolicy), binding.OperationSetDigest, binding.DeleteSetDigest, binding.CapabilityRevision,
		string(binding.SecurityDecision.Kind), binding.SecurityDecision.DecisionDigest,
		binding.SecurityDecision.FindingSetDigest, binding.SecurityDecision.PolicyRevision,
		binding.SecurityDecision.OverrideBindingDigest, binding.PreflightRevision,
		request.Plan.PreflightExpiresAt.UTC().Format(time.RFC3339Nano),
		strconv.FormatInt(request.EstimatedItems, 10), strconv.FormatInt(request.EstimatedBytes, 10),
		string(request.AuthorityCategory),
	}
	if privateBinding != nil {
		values = append(values, string(privateBinding.Provider), privateBinding.LocatorDigest)
	}
	if revision.Immutable != nil {
		values = append(values, revision.Immutable.LocatorDigest, revision.Immutable.ManifestDigest)
	} else {
		values = append(values, revision.MutableObservation.SourceFingerprint, revision.MutableObservation.CatalogGenerationID,
			revision.MutableObservation.ObservedAt.UTC().Format(time.RFC3339Nano))
	}
	return framedDigest(planIntentDigestDomain, values...)
}

func recoveryPlanRow(
	request CreatePlanRequest,
	keyDigest, planID string,
	source validatedSource,
	targetRoot settings.RecoveryTargetRootResolution,
	now time.Time,
) model.BackupAssetRecoveryPlan {
	binding := request.Plan.Binding
	row := model.BackupAssetRecoveryPlan{
		ID:                            planID,
		RequesterID:                   request.RequesterID,
		Endpoint:                      request.Endpoint,
		IdempotencyKeyDigest:          keyDigest,
		RepositoryID:                  binding.RepositoryID,
		RecoveryPointID:               binding.RecoveryPointID,
		SourceRevisionDigest:          binding.SourceRevisionDigest,
		SourceRevisionKind:            string(binding.SourceRevision.Kind),
		CatalogGenerationID:           request.Selection.CatalogGenerationID,
		EncryptedSourceLocator:        source.point.EncryptedProviderLocator,
		TargetMode:                    string(binding.Target.Mode),
		TargetNodeID:                  binding.Target.NodeID,
		TargetRootID:                  binding.Target.RootID,
		EncryptedTargetRootLocator:    targetRoot.Locator,
		EncryptedTargetRelativePath:   binding.Target.EncryptedRelativePath,
		RootLocatorDigest:             binding.Target.RootLocatorDigest,
		PathDigest:                    binding.Target.PathDigest,
		TargetBaseRevision:            binding.Target.BaseNodeRevision,
		CredentialScopeRevision:       binding.Target.CredentialScopeRevision,
		RootRevision:                  binding.Target.RootRevision,
		FilesystemRevision:            binding.Target.FilesystemRevision,
		SelectionDigest:               binding.SelectionDigest,
		BindingDigest:                 binding.PlanDigest,
		CapabilityRevision:            binding.CapabilityRevision,
		ConflictPolicy:                string(binding.ConflictPolicy),
		OperationSetDigest:            binding.OperationSetDigest,
		DeleteSetDigest:               binding.DeleteSetDigest,
		SecurityDecision:              string(binding.SecurityDecision.Kind),
		SecurityDecisionDigest:        binding.SecurityDecision.DecisionDigest,
		SecurityFindingSetDigest:      binding.SecurityDecision.FindingSetDigest,
		SecurityPolicyRevision:        binding.SecurityDecision.PolicyRevision,
		SecurityOverrideBindingDigest: binding.SecurityDecision.OverrideBindingDigest,
		PreflightRevision:             binding.PreflightRevision,
		PreflightExpiresAt:            request.Plan.PreflightExpiresAt.UTC(),
		EstimatedItems:                request.EstimatedItems,
		EstimatedBytes:                request.EstimatedBytes,
		State:                         string(PlanStateDraft),
		TransitionRevision:            1,
		CreatedAt:                     now,
		UpdatedAt:                     now,
	}
	if immutable := binding.SourceRevision.Immutable; immutable != nil {
		row.ImmutableLocatorDigest = immutable.LocatorDigest
		row.ImmutableManifestDigest = immutable.ManifestDigest
	} else {
		row.ObservationFingerprint = binding.SourceRevision.MutableObservation.SourceFingerprint
		observedAt := binding.SourceRevision.MutableObservation.ObservedAt.UTC()
		row.ObservedAt = &observedAt
	}
	return row
}

func recoveryPlanItemRows(
	ctx context.Context,
	tx *gorm.DB,
	source validatedSource,
	selection ExactSelection,
	planID string,
	now time.Time,
) ([]model.BackupAssetRecoveryPlanItem, int64, error) {
	entryIDs := make([]string, 0, len(selection.AssetRefs))
	for _, ref := range selection.AssetRefs {
		entryIDs = append(entryIDs, ref.EntryID)
	}
	var entries []catalogEntrySourceRow
	result := tx.WithContext(ctx).Table("catalog_entries").Clauses(recoverySourceRowLock()).
		Select("generation_id, entry_id, recovery_point_id, parent_entry_id, normalized_path, entry_type, size").
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id IN ?", selection.CatalogGenerationID, selection.RecoveryPointID, entryIDs).
		Find(&entries)
	if result.Error != nil {
		return nil, 0, sanitizePlanDatabaseError(ctx, result.Error)
	}
	if len(entries) != len(selection.AssetRefs) {
		return nil, 0, ErrRecoverySourceChanged
	}
	byEntryID := make(map[string]catalogEntrySourceRow, len(entries))
	for _, entry := range entries {
		if !validCatalogLeafForSource(entry, source) {
			return nil, 0, ErrRecoverySourceChanged
		}
		byEntryID[entry.EntryID] = entry
	}
	sourceFingerprint := ""
	if selection.SourceRevision.Kind == SourceRevisionObservation {
		sourceFingerprint = selection.SourceRevision.MutableObservation.SourceFingerprint
	}
	items := make([]model.BackupAssetRecoveryPlanItem, 0, len(selection.AssetRefs))
	var logicalBytes int64
	for ordinal, ref := range selection.AssetRefs {
		entry, found := byEntryID[ref.EntryID]
		if !found {
			return nil, 0, ErrRecoverySourceChanged
		}
		if entry.Size < 0 || entry.Size > recoveryPlanMaxLogicalBytes-logicalBytes {
			return nil, 0, ErrExactSelectionLimit
		}
		logicalBytes += entry.Size
		itemID, err := backupasset.NewOpaqueID()
		if err != nil {
			return nil, 0, ErrRecoveryPlanUnavailable
		}
		items = append(items, model.BackupAssetRecoveryPlanItem{
			ID:                  itemID,
			PlanID:              planID,
			Ordinal:             ordinal,
			RecoveryPointID:     selection.RecoveryPointID,
			CatalogGenerationID: selection.CatalogGenerationID,
			EntryID:             entry.EntryID,
			EntryType:           entry.EntryType,
			SourceFingerprint:   sourceFingerprint,
			RelativePathDigest: publication.RecoveryPlanItemPathDigest(
				selection.RepositoryID, selection.RecoveryPointID, selection.CatalogGenerationID,
				entry.EntryID, entry.NormalizedPath,
			),
			CreatedAt: now,
		})
	}
	return items, logicalBytes, nil
}

func sanitizePlanSourceError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if isPlanDatabaseBusy(err) {
		return errPlanDatabaseBusy
	}
	switch {
	case errors.Is(err, ErrInvalidExactSelection), errors.Is(err, ErrRecoverySourceUnavailable), errors.Is(err, ErrRecoverySourceChanged):
		return err
	default:
		return ErrRecoveryPlanUnavailable
	}
}

func sanitizeSourceConsumerError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrRecoverySourceUnavailable
}

func sanitizePlanDatabaseError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if isPlanDuplicateKey(err) {
		return errPlanIdempotencyRace
	}
	if isPlanDatabaseBusy(err) {
		return errPlanDatabaseBusy
	}
	return ErrRecoveryPlanUnavailable
}

func classifyPlanCreateError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, errPlanIdempotencyRace) || errors.Is(err, errPlanDatabaseBusy) ||
		errors.Is(err, ErrInvalidRecoveryPlan) || errors.Is(err, ErrExactSelectionLimit) || errors.Is(err, ErrPlanIdempotencyConflict) ||
		errors.Is(err, ErrRecoverySourceUnavailable) || errors.Is(err, ErrRecoverySourceChanged) ||
		errors.Is(err, ErrRecoveryTargetChanged) || errors.Is(err, ErrRecoveryPlanUnavailable) {
		return err
	}
	var fault *planCreateFaultError
	if errors.As(err, &fault) {
		return err
	}
	return sanitizePlanDatabaseError(ctx, err)
}

func publicPlanCreateError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	switch {
	case isPlanCreateFault(err):
		return err
	case errors.Is(err, ErrInvalidRecoveryPlan), errors.Is(err, ErrExactSelectionLimit), errors.Is(err, ErrPlanIdempotencyConflict),
		errors.Is(err, ErrRecoverySourceUnavailable), errors.Is(err, ErrRecoverySourceChanged),
		errors.Is(err, ErrRecoveryTargetChanged), errors.Is(err, ErrRecoveryPlanUnavailable):
		return err
	default:
		return ErrRecoveryPlanUnavailable
	}
}

func isPlanCreateFault(err error) bool {
	var fault *planCreateFaultError
	return errors.As(err, &fault)
}

func isPlanDuplicateKey(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate key") ||
		strings.Contains(message, "sqlstate 23505")
}

func isPlanDatabaseBusy(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "database schema is locked") || strings.Contains(message, "deadlock detected") ||
		strings.Contains(message, "could not serialize access") || strings.Contains(message, "serialization failure")
}

func waitForPlanCreateRetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt+1) * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SourceSelectionRequest names the one source tuple that may be frozen for a
// recovery. AssetRefs are explicit user-selected entries or directories; the
// validator expands directories into their exact non-directory descendants.
type SourceSelectionRequest struct {
	RepositoryID        string
	RecoveryPointID     string
	CatalogGenerationID string
	AssetRefs           []backupasset.AssetRef
	MaxItems            int
}

// NewRsyncRestoreSourceRef projects the durable Recovery plan into the only
// portable Rsync source facts that may cross into Provider and Repository.
func NewRsyncRestoreSourceRef(plan model.BackupAssetRecoveryPlan) (provider.RsyncRestoreSourceRef, error) {
	ref := provider.RsyncRestoreSourceRef{
		PlanID:               plan.ID,
		PlanBindingDigest:    plan.BindingDigest,
		RepositoryID:         plan.RepositoryID,
		RecoveryPointID:      plan.RecoveryPointID,
		CatalogGenerationID:  plan.CatalogGenerationID,
		SelectionDigest:      plan.SelectionDigest,
		SourceRevisionDigest: plan.SourceRevisionDigest,
		ManifestDigest:       plan.ImmutableManifestDigest,
	}
	if err := ref.Validate(); err != nil {
		return provider.RsyncRestoreSourceRef{}, ErrInvalidExactSelection
	}
	return ref, nil
}

// SourceValidator owns the database source boundary. It deliberately keeps
// Provider locators out of returned selections and releases them only through
// a post-commit consumer or a typed caller-transaction handoff.
type SourceValidator struct {
	db *gorm.DB
}

// NewSourceValidator creates a source validator over the supplied database.
func NewSourceValidator(db *gorm.DB) (*SourceValidator, error) {
	if db == nil {
		return nil, ErrRecoverySourceUnavailable
	}
	return &SourceValidator{db: db}, nil
}

// FreezeSelection validates one Repository/RecoveryPoint/Catalog tuple,
// expands selected directories deterministically within MaxItems, and freezes
// the exact source revision without exposing the source locator.
func (validator *SourceValidator) FreezeSelection(ctx context.Context, request SourceSelectionRequest) (ExactSelection, error) {
	if validator == nil || validator.db == nil || !validSourceSelectionRequest(request) {
		return ExactSelection{}, ErrInvalidExactSelection
	}
	ctx = sourceValidationContext(ctx)

	var selection ExactSelection
	err := validator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		source, err := loadValidatedSource(ctx, tx, request.RepositoryID, request.RecoveryPointID, request.CatalogGenerationID)
		if err != nil {
			return err
		}
		refs, err := expandExactAssetRefs(ctx, tx, source, request)
		if err != nil {
			return err
		}
		revision, privateBinding, err := source.frozenSourceRevision()
		if err != nil {
			return err
		}
		selection, err = newExactSelectionWithPrivateSourceBinding(ExactSelectionInput{
			RepositoryID:        request.RepositoryID,
			RecoveryPointID:     request.RecoveryPointID,
			CatalogGenerationID: request.CatalogGenerationID,
			AssetRefs:           refs,
			SourceRevision:      revision,
		}, privateBinding)
		return err
	})
	if err != nil {
		return ExactSelection{}, err
	}
	return selection, nil
}

// Revalidate reloads the exact source tuple in one database transaction, commits
// that validation, and only then hands the ephemeral locator to the typed
// Provider consumer.
func (validator *SourceValidator) Revalidate(
	ctx context.Context,
	selection ExactSelection,
	consumer publication.ExactRecoverySourceConsumer,
) error {
	if validator == nil || validator.db == nil {
		return ErrRecoverySourceUnavailable
	}
	if consumer == nil {
		return ErrInvalidExactSelection
	}
	ctx = sourceValidationContext(ctx)
	var exactSource publication.ExactRecoverySource
	err := validator.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		exactSource, err = validator.materializeTx(ctx, tx, selection)
		return err
	})
	if err != nil {
		clearExactRecoverySource(&exactSource)
		return err
	}
	defer clearExactRecoverySource(&exactSource)
	if exactSource.Provider == backupasset.ProviderRsync {
		// Rsync local locators require the managed-root capability path below.
		// Never allow the generic raw-locator handoff to become a bypass.
		return ErrRecoverySourceUnavailable
	}
	return sanitizeSourceConsumerError(ctx, consumer.ConsumeExactRecoverySource(ctx, exactSource))
}

// RevalidateTx performs the same source verification against the caller's
// transaction and returns the materialized, typed Provider handoff. It never
// performs Provider I/O; the caller consumes the handoff only after committing
// its own durable transition.
func (validator *SourceValidator) RevalidateTx(
	ctx context.Context,
	tx *gorm.DB,
	selection ExactSelection,
) (publication.ExactRecoverySource, error) {
	if validator == nil || validator.db == nil || tx == nil || tx.Error != nil {
		return publication.ExactRecoverySource{}, ErrRecoverySourceUnavailable
	}
	exactSource, err := validator.materializeTx(sourceValidationContext(ctx), tx, selection)
	if err != nil {
		return publication.ExactRecoverySource{}, err
	}
	if exactSource.Provider == backupasset.ProviderRsync {
		clearExactRecoverySource(&exactSource)
		return publication.ExactRecoverySource{}, ErrRecoverySourceUnavailable
	}
	return exactSource, nil
}

// RevalidatePlanTx reconstructs the plan's private exact selection and proves
// that its source tuple and selected Catalog entries still match inside the
// caller's authority transaction. It never returns a Provider locator.
func (validator *SourceValidator) RevalidatePlanTx(
	ctx context.Context,
	tx *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
) error {
	if validator == nil || validator.db == nil || tx == nil || tx.Error != nil ||
		!validOpaqueID(plan.ID) || !validPrivateLocator(plan.EncryptedSourceLocator) {
		return ErrRecoverySourceUnavailable
	}
	var repository struct {
		ID           string `gorm:"column:id"`
		ProviderKind string `gorm:"column:provider_kind"`
	}
	loaded := tx.WithContext(ctx).Table("backup_repositories").Clauses(recoverySourceRowLock()).
		Select("id, provider_kind").Where("id = ?", plan.RepositoryID).Limit(1).Find(&repository)
	if loaded.Error != nil {
		return loaded.Error
	}
	provider := backupasset.ProviderKind(repository.ProviderKind)
	if loaded.RowsAffected != 1 || repository.ID != plan.RepositoryID || !validRecoveryProvider(provider) {
		return ErrRecoverySourceChanged
	}
	locatorDigest, err := SourceLocatorDigest(plan.RepositoryID, provider, plan.RecoveryPointID, plan.EncryptedSourceLocator)
	if err != nil {
		return ErrRecoverySourceChanged
	}
	revision, err := recoveryPlanSourceRevision(plan)
	if err != nil {
		return err
	}
	var items []model.BackupAssetRecoveryPlanItem
	if err := tx.WithContext(ctx).Where("plan_id = ?", plan.ID).Order("ordinal ASC").Find(&items).Error; err != nil {
		return err
	}
	if len(items) == 0 || len(items) > exactSelectionMaxItems {
		return ErrRecoverySourceChanged
	}
	refs := make([]backupasset.AssetRef, len(items))
	for index, item := range items {
		if item.PlanID != plan.ID || item.Ordinal != index || item.RecoveryPointID != plan.RecoveryPointID ||
			item.CatalogGenerationID != plan.CatalogGenerationID {
			return ErrRecoverySourceChanged
		}
		refs[index] = backupasset.AssetRef{RecoveryPointID: item.RecoveryPointID, EntryID: item.EntryID}
	}
	selection, err := newExactSelectionWithPrivateSourceBinding(ExactSelectionInput{
		RepositoryID: plan.RepositoryID, RecoveryPointID: plan.RecoveryPointID,
		CatalogGenerationID: plan.CatalogGenerationID, AssetRefs: refs, SourceRevision: revision,
	}, frozenSourceBinding{Provider: provider, LocatorDigest: locatorDigest})
	if err != nil || selection.SelectionDigest != plan.SelectionDigest ||
		selection.SourceRevisionDigest != plan.SourceRevisionDigest {
		return ErrRecoverySourceChanged
	}
	source, err := validator.validateSelectionTx(sourceValidationContext(ctx), tx, selection)
	if err != nil {
		return err
	}
	return revalidateFrozenPlanItems(ctx, tx, source, plan.RepositoryID, items)
}

func recoveryPlanSourceRevision(plan model.BackupAssetRecoveryPlan) (SourceRevision, error) {
	switch SourceRevisionKind(plan.SourceRevisionKind) {
	case SourceRevisionImmutable:
		if plan.ObservedAt != nil || plan.ObservationFingerprint != "" || plan.CatalogGenerationID == "" {
			return SourceRevision{}, ErrRecoverySourceChanged
		}
		revision := SourceRevision{Kind: SourceRevisionImmutable, Immutable: &ImmutableSourceRevision{
			LocatorDigest: plan.ImmutableLocatorDigest, ManifestDigest: plan.ImmutableManifestDigest,
		}}
		if revision.Validate() != nil {
			return SourceRevision{}, ErrRecoverySourceChanged
		}
		return revision, nil
	case SourceRevisionObservation:
		if plan.ObservedAt == nil || plan.ImmutableLocatorDigest != "" || plan.ImmutableManifestDigest != "" {
			return SourceRevision{}, ErrRecoverySourceChanged
		}
		revision := SourceRevision{Kind: SourceRevisionObservation, MutableObservation: &ObservationRevision{
			SourceFingerprint: plan.ObservationFingerprint, CatalogGenerationID: plan.CatalogGenerationID,
			ObservedAt: plan.ObservedAt.UTC(),
		}}
		if revision.Validate() != nil {
			return SourceRevision{}, ErrRecoverySourceChanged
		}
		return revision, nil
	default:
		return SourceRevision{}, ErrRecoverySourceChanged
	}
}

func (validator *SourceValidator) materializeTx(
	ctx context.Context,
	tx *gorm.DB,
	selection ExactSelection,
) (publication.ExactRecoverySource, error) {
	source, err := validator.validateSelectionTx(ctx, tx, selection)
	if err != nil {
		return publication.ExactRecoverySource{}, err
	}

	exactSource, err := source.exactRecoverySource()
	if err != nil {
		return publication.ExactRecoverySource{}, err
	}
	if err := exactSource.Validate(); err != nil {
		clearExactRecoverySource(&exactSource)
		return publication.ExactRecoverySource{}, ErrRecoverySourceChanged
	}
	return exactSource, nil
}

func (validator *SourceValidator) validateSelectionTx(
	ctx context.Context,
	tx *gorm.DB,
	selection ExactSelection,
) (validatedSource, error) {
	if selection.Validate() != nil || !selection.hasPrivateSourceBinding() {
		return validatedSource{}, ErrInvalidExactSelection
	}
	source, err := loadValidatedSource(ctx, tx, selection.RepositoryID, selection.RecoveryPointID, selection.CatalogGenerationID)
	if err != nil {
		return validatedSource{}, err
	}
	currentRevision, currentPrivateBinding, err := source.frozenSourceRevision()
	if err != nil {
		return validatedSource{}, err
	}
	if !sameSourceRevision(selection.SourceRevision, currentRevision) ||
		!sameFrozenSourceBinding(selection.privateSourceBinding, &currentPrivateBinding) {
		return validatedSource{}, ErrRecoverySourceChanged
	}
	if err := revalidateSelectedEntries(ctx, tx, source, selection.AssetRefs); err != nil {
		return validatedSource{}, err
	}
	return source, nil
}

type validatedSource struct {
	repository model.BackupRepository
	point      recoveryPointSourceRow
	generation catalogGenerationSourceRow
	manifest   *recoveryManifestSourceRow
}

// recoveryPointSourceRow intentionally contains ciphertext. The source
// boundary validates the Repository/RecoveryPoint/Catalog relation first, and
// only decrypts EncryptedProviderLocator in decryptedLocator immediately before
// deriving a revision or calling the typed Provider consumer.
type recoveryPointSourceRow struct {
	ID                       string     `gorm:"column:id"`
	RepositoryID             string     `gorm:"column:repository_id"`
	EncryptedProviderLocator string     `gorm:"column:encrypted_provider_locator"`
	Semantics                string     `gorm:"column:semantics"`
	State                    string     `gorm:"column:state"`
	ObservedAt               *time.Time `gorm:"column:observed_at"`
	SourceFingerprint        string     `gorm:"column:source_fingerprint"`
	ManifestDigestAlgorithm  string     `gorm:"column:manifest_digest_algorithm"`
	ManifestDigest           string     `gorm:"column:manifest_digest"`
	PhysicalAvailability     string     `gorm:"column:physical_availability"`
}

type catalogGenerationSourceRow struct {
	ID                string  `gorm:"column:id"`
	RecoveryPointID   string  `gorm:"column:recovery_point_id"`
	ManifestID        *string `gorm:"column:manifest_id"`
	State             string  `gorm:"column:state"`
	IsActive          bool    `gorm:"column:is_active"`
	SourceFingerprint string  `gorm:"column:source_fingerprint"`
}

type recoveryManifestSourceRow struct {
	ID              string `gorm:"column:id"`
	RecoveryPointID string `gorm:"column:recovery_point_id"`
	DigestAlgorithm string `gorm:"column:digest_algorithm"`
	Digest          string `gorm:"column:digest"`
	Completeness    string `gorm:"column:completeness"`
	IsActive        bool   `gorm:"column:is_active"`
}

// catalogEntrySourceRow deliberately excludes EncryptedProviderLocator so
// catalog validation never decrypts entry locators.
type catalogEntrySourceRow struct {
	GenerationID    string  `gorm:"column:generation_id"`
	EntryID         string  `gorm:"column:entry_id"`
	RecoveryPointID string  `gorm:"column:recovery_point_id"`
	ParentEntryID   *string `gorm:"column:parent_entry_id"`
	NormalizedPath  string  `gorm:"column:normalized_path"`
	EntryType       string  `gorm:"column:entry_type"`
	Size            int64   `gorm:"column:size"`
}

func loadValidatedSource(ctx context.Context, tx *gorm.DB, repositoryID, recoveryPointID, catalogGenerationID string) (validatedSource, error) {
	if tx == nil || !validOpaqueID(repositoryID) || !validOpaqueID(recoveryPointID) || !validOpaqueID(catalogGenerationID) {
		return validatedSource{}, ErrInvalidExactSelection
	}

	var repository model.BackupRepository
	result := tx.WithContext(ctx).Clauses(recoverySourceRowLock()).
		Where("id = ?", repositoryID).Limit(1).Find(&repository)
	if result.Error != nil {
		return validatedSource{}, fmt.Errorf("load recovery repository: %w", result.Error)
	}
	if result.RowsAffected != 1 || repository.ID != repositoryID || !validRecoveryProvider(backupasset.ProviderKind(repository.ProviderKind)) ||
		repository.Status != string(backupasset.RepositoryOnline) {
		return validatedSource{}, ErrRecoverySourceUnavailable
	}

	var point recoveryPointSourceRow
	result = tx.WithContext(ctx).Table("recovery_points").Clauses(recoverySourceRowLock()).
		Select("id, repository_id, encrypted_provider_locator, semantics, state, observed_at, source_fingerprint, manifest_digest_algorithm, manifest_digest, physical_availability").
		Where("id = ? AND repository_id = ?", recoveryPointID, repositoryID).Limit(1).Find(&point)
	if result.Error != nil {
		return validatedSource{}, fmt.Errorf("load recovery point source: %w", result.Error)
	}
	if result.RowsAffected != 1 || point.ID != recoveryPointID || point.RepositoryID != repositoryID ||
		point.PhysicalAvailability != string(backupasset.PhysicalOnline) {
		return validatedSource{}, ErrRecoverySourceUnavailable
	}

	var generation catalogGenerationSourceRow
	result = tx.WithContext(ctx).Table("catalog_generations").Clauses(recoverySourceRowLock()).
		Select("id, recovery_point_id, manifest_id, state, is_active, source_fingerprint").
		Where("id = ? AND recovery_point_id = ?", catalogGenerationID, recoveryPointID).Limit(1).Find(&generation)
	if result.Error != nil {
		return validatedSource{}, fmt.Errorf("load recovery catalog generation: %w", result.Error)
	}
	if result.RowsAffected != 1 || generation.ID != catalogGenerationID || generation.RecoveryPointID != recoveryPointID ||
		generation.State != string(backupasset.CatalogGenerationComplete) || !generation.IsActive {
		return validatedSource{}, ErrRecoverySourceUnavailable
	}

	source := validatedSource{repository: repository, point: point, generation: generation}
	switch backupasset.PointVersionSemantics(point.Semantics) {
	case backupasset.PointMutableHead:
		if repository.VersionMode != string(backupasset.VersionMutableHead) ||
			point.State != string(backupasset.RecoveryPointObserved) || !validDigest(point.SourceFingerprint) ||
			point.ObservedAt == nil || point.ObservedAt.IsZero() || generation.SourceFingerprint != point.SourceFingerprint {
			return validatedSource{}, ErrRecoverySourceUnavailable
		}
	case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest, backupasset.PointImportedBaseline:
		if repository.VersionMode == string(backupasset.VersionMutableHead) ||
			(point.State != string(backupasset.RecoveryPointCommitted) && point.State != string(backupasset.RecoveryPointDegraded)) ||
			point.ManifestDigestAlgorithm != "sha256" || !validDigest(point.ManifestDigest) || generation.ManifestID == nil ||
			!validOpaqueID(*generation.ManifestID) {
			return validatedSource{}, ErrRecoverySourceUnavailable
		}
		var manifest recoveryManifestSourceRow
		result = tx.WithContext(ctx).Table("recovery_point_manifests").Clauses(recoverySourceRowLock()).
			Select("id, recovery_point_id, digest_algorithm, digest, completeness, is_active").
			Where("id = ? AND recovery_point_id = ?", *generation.ManifestID, recoveryPointID).Limit(1).Find(&manifest)
		if result.Error != nil {
			return validatedSource{}, fmt.Errorf("load recovery manifest: %w", result.Error)
		}
		if result.RowsAffected != 1 || manifest.ID != *generation.ManifestID || manifest.RecoveryPointID != recoveryPointID ||
			manifest.DigestAlgorithm != "sha256" || manifest.Digest != point.ManifestDigest ||
			manifest.Completeness != string(backupasset.ManifestComplete) || !manifest.IsActive {
			return validatedSource{}, ErrRecoverySourceUnavailable
		}
		source.manifest = &manifest
	default:
		return validatedSource{}, ErrRecoverySourceUnavailable
	}
	return source, nil
}

func (source validatedSource) frozenSourceRevision() (SourceRevision, frozenSourceBinding, error) {
	privateBinding, err := source.privateSourceBinding()
	if err != nil {
		return SourceRevision{}, frozenSourceBinding{}, err
	}

	switch backupasset.PointVersionSemantics(source.point.Semantics) {
	case backupasset.PointMutableHead:
		if source.point.ObservedAt == nil {
			return SourceRevision{}, frozenSourceBinding{}, ErrRecoverySourceUnavailable
		}
		return SourceRevision{
			Kind: SourceRevisionObservation,
			MutableObservation: &ObservationRevision{
				SourceFingerprint:   source.point.SourceFingerprint,
				CatalogGenerationID: source.generation.ID,
				ObservedAt:          source.point.ObservedAt.UTC(),
			},
		}, privateBinding, nil
	case backupasset.PointNativeSnapshot, backupasset.PointXirangManifest, backupasset.PointImportedBaseline:
		if source.manifest == nil {
			return SourceRevision{}, frozenSourceBinding{}, ErrRecoverySourceUnavailable
		}
		return SourceRevision{
			Kind: SourceRevisionImmutable,
			Immutable: &ImmutableSourceRevision{
				LocatorDigest:  privateBinding.LocatorDigest,
				ManifestDigest: source.manifest.Digest,
			},
		}, privateBinding, nil
	default:
		return SourceRevision{}, frozenSourceBinding{}, ErrRecoverySourceUnavailable
	}
}

func (source validatedSource) privateSourceBinding() (frozenSourceBinding, error) {
	locator, err := source.decryptedLocator()
	if err != nil {
		return frozenSourceBinding{}, err
	}
	defer clearPrivateString(&locator)

	digest, err := SourceLocatorDigest(
		source.repository.ID,
		backupasset.ProviderKind(source.repository.ProviderKind),
		source.point.ID,
		locator,
	)
	if err != nil {
		return frozenSourceBinding{}, ErrRecoverySourceChanged
	}
	return frozenSourceBinding{
		Provider:      backupasset.ProviderKind(source.repository.ProviderKind),
		LocatorDigest: digest,
	}, nil
}

func (source validatedSource) exactRecoverySource() (publication.ExactRecoverySource, error) {
	locator, err := source.decryptedLocator()
	if err != nil {
		return publication.ExactRecoverySource{}, err
	}
	locatorDigest, err := SourceLocatorDigest(
		source.repository.ID, backupasset.ProviderKind(source.repository.ProviderKind), source.point.ID, locator,
	)
	if err != nil {
		clearPrivateString(&locator)
		return publication.ExactRecoverySource{}, ErrRecoverySourceChanged
	}
	return publication.ExactRecoverySource{
		RepositoryID:    source.repository.ID,
		RecoveryPointID: source.point.ID,
		Provider:        backupasset.ProviderKind(source.repository.ProviderKind),
		Locator:         locator,
		LocatorDigest:   locatorDigest,
	}, nil
}

func (source validatedSource) decryptedLocator() (string, error) {
	if !secure.IsEncrypted(source.point.EncryptedProviderLocator) {
		return "", ErrRecoverySourceChanged
	}
	locator, err := secure.DecryptIfNeeded(source.point.EncryptedProviderLocator)
	if err != nil || !validPrivateLocator(locator) {
		return "", ErrRecoverySourceChanged
	}
	return locator, nil
}

func expandExactAssetRefs(
	ctx context.Context,
	tx *gorm.DB,
	source validatedSource,
	request SourceSelectionRequest,
) ([]backupasset.AssetRef, error) {
	requested, err := canonicalExactAssetRefs(source.point.ID, request.AssetRefs)
	if err != nil {
		return nil, err
	}
	if len(requested) > request.MaxItems {
		return nil, ErrExactSelectionLimit
	}

	entryIDs := make([]string, 0, len(requested))
	for _, ref := range requested {
		entryIDs = append(entryIDs, ref.EntryID)
	}
	var roots []catalogEntrySourceRow
	result := tx.WithContext(ctx).Table("catalog_entries").Clauses(recoverySourceRowLock()).
		Select("generation_id, entry_id, recovery_point_id, parent_entry_id, normalized_path, entry_type").
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id IN ?", source.generation.ID, source.point.ID, entryIDs).
		Order("entry_id ASC").Find(&roots)
	if result.Error != nil {
		return nil, fmt.Errorf("load recovery selection entries: %w", result.Error)
	}
	if len(roots) != len(requested) {
		return nil, ErrRecoverySourceChanged
	}

	byID := make(map[string]catalogEntrySourceRow, len(roots))
	for _, root := range roots {
		if !validCatalogEntryForSource(root, source) {
			return nil, ErrRecoverySourceChanged
		}
		byID[root.EntryID] = root
	}
	selected := make(map[string]backupasset.AssetRef, len(requested))
	directories := make([]catalogEntrySourceRow, 0, len(requested))
	for _, ref := range requested {
		root, found := byID[ref.EntryID]
		if !found {
			return nil, ErrRecoverySourceChanged
		}
		if backupasset.CatalogEntryType(root.EntryType) == backupasset.CatalogEntryDirectory {
			directories = append(directories, root)
			continue
		}
		if _, alreadySelected := selected[root.EntryID]; alreadySelected {
			return nil, ErrInvalidExactSelection
		}
		selected[root.EntryID] = backupasset.AssetRef{RecoveryPointID: source.point.ID, EntryID: root.EntryID}
	}

	if len(directories) > 0 {
		leaves, err := directoryLeaves(ctx, tx, source, directories, request.MaxItems)
		if err != nil {
			return nil, err
		}
		for _, leaf := range leaves {
			if !validCatalogLeafForSource(leaf, source) {
				return nil, ErrRecoverySourceChanged
			}
			if _, alreadySelected := selected[leaf.EntryID]; alreadySelected {
				return nil, ErrInvalidExactSelection
			}
			selected[leaf.EntryID] = backupasset.AssetRef{RecoveryPointID: source.point.ID, EntryID: leaf.EntryID}
			if len(selected) > request.MaxItems {
				return nil, ErrExactSelectionLimit
			}
		}
	}

	refs := make([]backupasset.AssetRef, 0, len(selected))
	for _, ref := range selected {
		refs = append(refs, ref)
	}
	return canonicalExactAssetRefs(source.point.ID, refs)
}

func directoryLeaves(
	ctx context.Context,
	tx *gorm.DB,
	source validatedSource,
	directories []catalogEntrySourceRow,
	maxItems int,
) ([]catalogEntrySourceRow, error) {
	if maxItems <= 0 {
		return nil, ErrExactSelectionLimit
	}

	type directoryTraversalNode struct {
		entry      catalogEntrySourceRow
		provenance string
	}

	frontier := make([]directoryTraversalNode, 0, len(directories))
	directoryProvenance := make(map[string]string, len(directories))
	for _, directory := range directories {
		if _, exists := directoryProvenance[directory.EntryID]; exists {
			return nil, ErrInvalidExactSelection
		}
		directoryProvenance[directory.EntryID] = directory.EntryID
		frontier = append(frontier, directoryTraversalNode{entry: directory, provenance: directory.EntryID})
	}
	processedDirectories := make(map[string]struct{}, len(directories))
	leafProvenance := make(map[string]string, maxItems)
	leaves := make([]catalogEntrySourceRow, 0, maxItems)
	expandedNodes := 0

	for len(frontier) > 0 {
		active := make([]directoryTraversalNode, 0, len(frontier))
		for _, directory := range frontier {
			if _, alreadyProcessed := processedDirectories[directory.entry.EntryID]; alreadyProcessed {
				continue
			}
			processedDirectories[directory.entry.EntryID] = struct{}{}
			active = append(active, directory)
		}
		next := make([]directoryTraversalNode, 0)
		for start := 0; start < len(active); start += directoryParentBatchSize {
			end := start + directoryParentBatchSize
			if end > len(active) {
				end = len(active)
			}
			parentIDs := make([]string, 0, end-start)
			parentProvenance := make(map[string]string, end-start)
			for _, directory := range active[start:end] {
				parentIDs = append(parentIDs, directory.entry.EntryID)
				parentProvenance[directory.entry.EntryID] = directory.provenance
			}

			remainingNodes := exactSelectionMaxItems - expandedNodes
			if remainingNodes <= 0 {
				return nil, ErrExactSelectionLimit
			}
			var children []catalogEntrySourceRow
			result := tx.WithContext(ctx).Table("catalog_entries").
				Select("generation_id, entry_id, recovery_point_id, parent_entry_id, normalized_path, entry_type").
				Where("generation_id = ? AND recovery_point_id = ? AND parent_entry_id IN ?", source.generation.ID, source.point.ID, parentIDs).
				Order("entry_id ASC").Limit(remainingNodes + 1).Find(&children)
			if result.Error != nil {
				return nil, fmt.Errorf("expand recovery directories: %w", result.Error)
			}
			if len(children) > remainingNodes {
				return nil, ErrExactSelectionLimit
			}
			expandedNodes += len(children)

			for _, child := range children {
				if !validCatalogEntryForSource(child, source) {
					return nil, ErrRecoverySourceChanged
				}
				if child.ParentEntryID == nil {
					return nil, ErrRecoverySourceChanged
				}
				provenance, found := parentProvenance[*child.ParentEntryID]
				if !found {
					return nil, ErrRecoverySourceChanged
				}
				if backupasset.CatalogEntryType(child.EntryType) == backupasset.CatalogEntryDirectory {
					if existing, alreadyExpanded := directoryProvenance[child.EntryID]; alreadyExpanded {
						if existing != provenance {
							return nil, ErrInvalidExactSelection
						}
						continue
					}
					directoryProvenance[child.EntryID] = provenance
					next = append(next, directoryTraversalNode{entry: child, provenance: provenance})
					continue
				}
				if existing, alreadyExpanded := leafProvenance[child.EntryID]; alreadyExpanded {
					if existing != provenance {
						return nil, ErrInvalidExactSelection
					}
					continue
				}
				leafProvenance[child.EntryID] = provenance
				leaves = append(leaves, child)
				if len(leaves) > maxItems {
					return nil, ErrExactSelectionLimit
				}
			}
		}

		sort.Slice(next, func(left, right int) bool {
			return next[left].entry.EntryID < next[right].entry.EntryID
		})
		frontier = next
	}

	sort.Slice(leaves, func(left, right int) bool {
		if leaves[left].NormalizedPath != leaves[right].NormalizedPath {
			return leaves[left].NormalizedPath < leaves[right].NormalizedPath
		}
		return leaves[left].EntryID < leaves[right].EntryID
	})
	return leaves, nil
}

func revalidateSelectedEntries(ctx context.Context, tx *gorm.DB, source validatedSource, refs []backupasset.AssetRef) error {
	entryIDs := make([]string, 0, len(refs))
	for _, ref := range refs {
		entryIDs = append(entryIDs, ref.EntryID)
	}
	var rows []catalogEntrySourceRow
	result := tx.WithContext(ctx).Table("catalog_entries").Clauses(recoverySourceRowLock()).
		Select("generation_id, entry_id, recovery_point_id, parent_entry_id, normalized_path, entry_type").
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id IN ?", source.generation.ID, source.point.ID, entryIDs).
		Order("entry_id ASC").Find(&rows)
	if result.Error != nil {
		return fmt.Errorf("revalidate recovery selection entries: %w", result.Error)
	}
	if len(rows) != len(refs) {
		return ErrRecoverySourceChanged
	}
	for _, row := range rows {
		if !validCatalogLeafForSource(row, source) {
			return ErrRecoverySourceChanged
		}
	}
	return nil
}

func revalidateFrozenPlanItems(
	ctx context.Context,
	tx *gorm.DB,
	source validatedSource,
	repositoryID string,
	items []model.BackupAssetRecoveryPlanItem,
) error {
	if !validOpaqueID(repositoryID) || len(items) == 0 || len(items) > exactSelectionMaxItems {
		return ErrRecoverySourceChanged
	}
	entryIDs := make([]string, 0, len(items))
	itemsByEntryID := make(map[string]model.BackupAssetRecoveryPlanItem, len(items))
	for _, item := range items {
		if item.EntryType == "" || !validDigest(item.RelativePathDigest) {
			return ErrRecoverySourceChanged
		}
		if _, duplicate := itemsByEntryID[item.EntryID]; duplicate {
			return ErrRecoverySourceChanged
		}
		itemsByEntryID[item.EntryID] = item
		entryIDs = append(entryIDs, item.EntryID)
	}
	var rows []catalogEntrySourceRow
	result := tx.WithContext(ctx).Table("catalog_entries").Clauses(recoverySourceRowLock()).
		Select("generation_id, entry_id, recovery_point_id, parent_entry_id, normalized_path, entry_type").
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id IN ?", source.generation.ID, source.point.ID, entryIDs).
		Order("entry_id ASC").Find(&rows)
	if result.Error != nil {
		return fmt.Errorf("revalidate frozen recovery plan items: %w", result.Error)
	}
	if len(rows) != len(items) {
		return ErrRecoverySourceChanged
	}
	for _, row := range rows {
		item, found := itemsByEntryID[row.EntryID]
		if !found || !validCatalogLeafForSource(row, source) || item.EntryType != row.EntryType ||
			item.RelativePathDigest != publication.RecoveryPlanItemPathDigest(
				repositoryID, source.point.ID, source.generation.ID, row.EntryID, row.NormalizedPath,
			) {
			return ErrRecoverySourceChanged
		}
	}
	return nil
}

func validSourceSelectionRequest(request SourceSelectionRequest) bool {
	return validOpaqueID(request.RepositoryID) && validOpaqueID(request.RecoveryPointID) &&
		validOpaqueID(request.CatalogGenerationID) && len(request.AssetRefs) > 0 &&
		request.MaxItems > 0 && request.MaxItems <= exactSelectionMaxItems
}

func validCatalogEntryForSource(entry catalogEntrySourceRow, source validatedSource) bool {
	return entry.GenerationID == source.generation.ID && entry.RecoveryPointID == source.point.ID &&
		backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: entry.RecoveryPointID, EntryID: entry.EntryID}) == nil &&
		entry.EntryType != ""
}

func validCatalogLeafForSource(entry catalogEntrySourceRow, source validatedSource) bool {
	return validCatalogEntryForSource(entry, source) && backupasset.CatalogEntryType(entry.EntryType) != backupasset.CatalogEntryDirectory
}

func recoverySourceRowLock() clause.Locking {
	return clause.Locking{Strength: clause.LockingStrengthUpdate}
}

func sourceValidationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func clearPrivateString(value *string) {
	if value != nil {
		*value = ""
	}
}

func clearExactRecoverySource(source *publication.ExactRecoverySource) {
	if source == nil {
		return
	}
	source.Locator = ""
	source.LocatorDigest = ""
}

const (
	recoveryAuthorizationIdempotencyDomain   = "xirang/recovery/authorization/idempotency-key/v1"
	recoveryAuthorizationIntentDomain        = "xirang/recovery/authorization/intent/v1"
	recoveryAuthorizationExecuteReplayDomain = "xirang/recovery/authorization/execute-replay/v1"
	recoveryAuthorizationProofDomain         = "xirang/recovery/authorization/step-up-jti/v1"
	recoveryAuthorizationSessionDomain       = "xirang/recovery/authorization/presenting-session/v1"
	recoveryAuthorizationReasonDomain        = "xirang/recovery/authorization/reason/v1"
	recoveryAuthorizationGrantSecretDomain   = "xirang/recovery/authorization/grant-secret/v1"
	recoveryAuthorizationGrantBindingDomain  = "xirang/recovery/authorization/grant-binding/v1"
	recoveryAuthorizationSourceLeaseDomain   = "xirang/recovery/authorization/source-lease/v1"
	recoveryAuthorizationOverrideDomain      = "xirang/recovery/authorization/security-override/v1"
	recoveryAuthorizationDecisionDomain      = "xirang/recovery/authorization/security-decision/v1"
	recoveryAuthorizationPlanBindingDomain   = "xirang/recovery/authorization/plan-binding/v1"

	recoverySecurityOverrideEndpoint    = "/api/v1/recovery-plans/:id/security-overrides"
	recoveryWriteAuthorizationEndpoint  = "/api/v1/recovery-plans/:id/write-authorizations"
	recoveryDeleteAuthorizationEndpoint = "/api/v1/recovery-jobs/:id/exact-mirror-delete-authorizations"
	recoveryExecuteEndpoint             = "/api/v1/recovery-plans/:id/execute"

	authorizationRetryAttempts  = 12
	maxAuthorizationReasonBytes = 2048
	authorizationAuditTimeout   = 5 * time.Second
)

// AuthorizationReceiptOperation is the closed write boundary represented by
// one durable authorization receipt.
type AuthorizationReceiptOperation string

const (
	AuthorizationReceiptSecurityOverride AuthorizationReceiptOperation = "security_override"
	AuthorizationReceiptWriteAuthorize   AuthorizationReceiptOperation = "write_authorize"
	AuthorizationReceiptDeleteAuthorize  AuthorizationReceiptOperation = "exact_mirror_delete_authorize"
	AuthorizationReceiptExecute          AuthorizationReceiptOperation = "execute"
)

// AuthorizationReceiptCategory is intentionally distinct from grant
// authority: execute consumes a write grant but persists the execute category.
type AuthorizationReceiptCategory string

const (
	AuthorizationReceiptCategorySecurityOverride  AuthorizationReceiptCategory = "security_override"
	AuthorizationReceiptCategoryWrite             AuthorizationReceiptCategory = "write"
	AuthorizationReceiptCategoryExactMirrorDelete AuthorizationReceiptCategory = "exact_mirror_delete"
	AuthorizationReceiptCategoryExecute           AuthorizationReceiptCategory = "execute"
)

var (
	ErrInvalidRecoveryAuthorization     = errors.New("invalid recovery authorization request")
	ErrInvalidAuthorizationGrantSecret  = errors.New("invalid recovery authorization grant secret")
	ErrAuthorizationIdempotencyConflict = errors.New("recovery authorization idempotency conflict")
	ErrAuthorizationProofUsed           = errors.New("recovery authorization proof was already used")
	ErrAuthorizationSessionMismatch     = errors.New("recovery authorization session mismatch")
	ErrAuthorizationProofLifetime       = errors.New("recovery authorization proof lifetime cannot be covered")
	ErrAuthorizationDenied              = errors.New("recovery authorization denied")
	ErrAuthorizationUnavailable         = errors.New("recovery authorization is unavailable")
)

type RecoveryAuthorizationProof struct {
	JTI          string
	Action       string
	UserID       uint
	Role         string
	TokenVersion uint
	ExpiresAt    time.Time
}

type RecoveryAuthorizationSession struct {
	JTI          string
	UserID       uint
	Role         string
	TokenVersion uint
	ExpiresAt    time.Time
}

type RecoveryAuthorizationRequest struct {
	RequesterID          uint
	PlanID               string
	JobID                string
	CheckpointID         string
	GrantID              string
	AttemptID            string
	Endpoint             string
	IdempotencyKey       string
	Operation            AuthorizationReceiptOperation
	Category             AuthorizationReceiptCategory
	ExpectedPlanRevision uint64
	PreflightID          string
	FindingCategory      SecurityFindingCategory
	Reason               string
	GrantSecret          string
	Proof                RecoveryAuthorizationProof
	Session              RecoveryAuthorizationSession
}

type RecoveryAuthorizationGrantStatus string

const (
	RecoveryAuthorizationGrantIssued   RecoveryAuthorizationGrantStatus = "issued"
	RecoveryAuthorizationGrantConsumed RecoveryAuthorizationGrantStatus = "consumed"
)

type RecoveryAuthorizationResult struct {
	ReceiptID              string                           `json:"receipt_id"`
	PlanID                 string                           `json:"plan_id"`
	GrantID                string                           `json:"grant_id,omitempty"`
	GrantCategory          AuthorityCategory                `json:"grant_category,omitempty"`
	GrantBindingDigest     string                           `json:"grant_binding_digest,omitempty"`
	GrantExpiresAt         *time.Time                       `json:"grant_expires_at,omitempty"`
	GrantStatus            RecoveryAuthorizationGrantStatus `json:"grant_status,omitempty"`
	JobID                  string                           `json:"job_id,omitempty"`
	AttemptID              string                           `json:"attempt_id,omitempty"`
	SourceLeaseID          string                           `json:"source_lease_id,omitempty"`
	NodeLeaseID            string                           `json:"node_lease_id,omitempty"`
	NodeLeaseFence         uint64                           `json:"node_lease_fence,omitempty"`
	PlanTransitionRevision uint64                           `json:"plan_transition_revision"`
	Replay                 bool                             `json:"replay"`
}

func (result RecoveryAuthorizationResult) sameDurableEffect(other RecoveryAuthorizationResult) bool {
	return result.ReceiptID == other.ReceiptID && result.PlanID == other.PlanID &&
		result.GrantID == other.GrantID && result.GrantCategory == other.GrantCategory &&
		result.GrantBindingDigest == other.GrantBindingDigest &&
		authorizationTimesEqual(result.GrantExpiresAt, other.GrantExpiresAt) &&
		result.GrantStatus == other.GrantStatus && result.JobID == other.JobID &&
		result.AttemptID == other.AttemptID && result.SourceLeaseID == other.SourceLeaseID &&
		result.NodeLeaseID == other.NodeLeaseID && result.NodeLeaseFence == other.NodeLeaseFence &&
		result.PlanTransitionRevision == other.PlanTransitionRevision
}

func authorizationTimesEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

type RecoveryAuthorizationAuditWriter interface {
	Write(context.Context, backupasset.AuditEventInput) (model.BackupAssetAuditEvent, error)
}

type RecoveryNodeAdmission interface {
	AdmitRecoveryTx(context.Context, *gorm.DB, uint) error
}

type RecoveryLocatorKeyring interface {
	Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error)
	LockActiveTx(context.Context, *gorm.DB, backupasset.DomainKeyMaterial) (backupasset.DomainKeyMaterial, error)
}

// RecoveryAuthorityBinding is the private, opaque authority-time product a
// live target/security validator must recheck before write authorization or
// execute can commit. It deliberately excludes raw locators and credentials.
type RecoveryAuthorityBinding struct {
	Operation                     AuthorizationReceiptOperation
	Provider                      backupasset.ProviderKind
	PlanID                        string
	PlanBindingDigest             string
	PlanTransitionRevision        uint64
	RepositoryID                  string
	RecoveryPointID               string
	CatalogGenerationID           string
	SelectionDigest               string
	SourceRevisionDigest          string
	ManifestDigest                string
	SourceRef                     provider.RsyncRestoreSourceRef
	TargetMode                    TargetMode
	TargetNodeID                  uint
	TargetRootID                  string
	RootLocatorDigest             string
	PathDigest                    string
	TargetBaseRevision            string
	CredentialScopeRevision       string
	RootRevision                  string
	FilesystemRevision            string
	CapabilityRevision            string
	ConflictPolicy                ConflictPolicy
	OperationSetDigest            string
	DeleteSetDigest               string
	SecurityDecision              SecurityDecisionKind
	SecurityDecisionDigest        string
	SecurityFindingSetDigest      string
	SecurityPolicyRevision        string
	SecurityOverrideBindingDigest string
	PreflightID                   string
	PreflightRevision             string
	PreflightTargetRevision       string
	PreflightNodeRevision         string
	RequiredBytes                 int64
	RequiredInodes                int64
	draftPreflight                bool
}

// RecoveryAuthorityRevalidator checks the current target/node/credential,
// capability, policy, and finding product in the caller-owned transaction.
// Implementations must fail closed when current evidence is unavailable.
type RecoveryAuthorityRevalidator interface {
	ObserveRecoveryAuthority(
		context.Context,
		RecoveryAuthorityBinding,
	) (RecoveryAuthorityObservation, error)
	RevalidateRecoveryAuthorityTx(
		context.Context,
		*gorm.DB,
		RecoveryAuthorityBinding,
		RecoveryAuthorityObservation,
	) error
}

type AuthorizationServiceDependencies struct {
	DB               *gorm.DB
	Now              func() time.Time
	Metrics          Metrics
	SourceLeases     *backupasset.LeaseService
	NodeAdmission    RecoveryNodeAdmission
	LiveRevalidator  RecoveryAuthorityRevalidator
	LocatorKeys      RecoveryLocatorKeyring
	AuditWriter      RecoveryAuthorizationAuditWriter
	ReceiptReplayTTL time.Duration
	WriteGrantTTL    time.Duration
	DeleteGrantTTL   time.Duration
	NodeLeaseTTL     time.Duration
	Policy           WorkerPolicy
}

type authorizationPersistStage uint8

const (
	authorizationPersistBeforeCommit authorizationPersistStage = iota + 1
)

type AuthorizationService struct {
	db               *gorm.DB
	now              func() time.Time
	metrics          Metrics
	sourceValidator  *SourceValidator
	sourceLeases     *backupasset.LeaseService
	nodeAdmission    RecoveryNodeAdmission
	liveRevalidator  RecoveryAuthorityRevalidator
	locatorKeys      RecoveryLocatorKeyring
	auditWriter      RecoveryAuthorizationAuditWriter
	receiptReplayTTL time.Duration
	writeGrantTTL    time.Duration
	deleteGrantTTL   time.Duration
	nodeLeaseTTL     time.Duration
	policy           WorkerPolicy
	beforePersist    func(authorizationPersistStage) error
}

type normalizedRecoveryAuthorization struct {
	request         RecoveryAuthorizationRequest
	keyDigest       string
	intentDigest    string
	proofDigest     string
	sessionDigest   string
	reasonDigest    string
	grantSecretHash string
	replayExpiresAt time.Time
	grantExpiresAt  time.Time
}

// authorizationIntentBinding is the closed, private input product committed
// into a receipt's full intent digest. It deliberately stores only opaque IDs,
// digests, revisions, closed states, and fences; locators and credentials never
// enter this structure.
type authorizationIntentBinding struct {
	plan       model.BackupAssetRecoveryPlan
	preflight  model.BackupAssetRecoveryPreflight
	source     authorizationIntentSource
	grant      *model.BackupAssetRecoveryGrant
	job        *model.BackupAssetRecoveryJob
	checkpoint *model.BackupAssetRecoveryCheckpoint
	attempt    *model.BackupAssetRecoveryAttempt
	nodeLease  *model.BackupAssetRecoveryNodeLease
}

type authorizationIntentSource struct {
	ID                 string
	RepositoryID       string
	SourceFingerprint  string
	ManifestDigest     string
	Semantics          string
	State              string
	CapabilityRevision int
}

func NewAuthorizationService(dependencies AuthorizationServiceDependencies) (*AuthorizationService, error) {
	if dependencies.DB == nil || dependencies.SourceLeases == nil || dependencies.NodeAdmission == nil ||
		dependencies.LiveRevalidator == nil || dependencies.LocatorKeys == nil ||
		dependencies.ReceiptReplayTTL <= 0 || dependencies.WriteGrantTTL <= 0 ||
		dependencies.DeleteGrantTTL <= 0 || dependencies.NodeLeaseTTL <= 0 ||
		!dependencies.Policy.valid() || dependencies.Policy.LeaseRenewMargin >= dependencies.NodeLeaseTTL ||
		dependencies.WriteGrantTTL > dependencies.ReceiptReplayTTL ||
		dependencies.DeleteGrantTTL > dependencies.ReceiptReplayTTL {
		return nil, ErrAuthorizationUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.Metrics == nil {
		dependencies.Metrics = NoopMetrics{}
	}
	sourceValidator, err := NewSourceValidator(dependencies.DB)
	if err != nil {
		return nil, ErrAuthorizationUnavailable
	}
	return &AuthorizationService{
		db: dependencies.DB, now: dependencies.Now, metrics: dependencies.Metrics, sourceValidator: sourceValidator, sourceLeases: dependencies.SourceLeases,
		nodeAdmission: dependencies.NodeAdmission, liveRevalidator: dependencies.LiveRevalidator, auditWriter: dependencies.AuditWriter,
		locatorKeys:      dependencies.LocatorKeys,
		receiptReplayTTL: dependencies.ReceiptReplayTTL, writeGrantTTL: dependencies.WriteGrantTTL,
		deleteGrantTTL: dependencies.DeleteGrantTTL, nodeLeaseTTL: dependencies.NodeLeaseTTL,
		policy: dependencies.Policy,
	}, nil
}

func (service *AuthorizationService) ReplayAuthorization(
	ctx context.Context,
	request RecoveryAuthorizationRequest,
) (RecoveryAuthorizationResult, bool, error) {
	if service == nil || service.db == nil {
		return RecoveryAuthorizationResult{}, false, ErrAuthorizationUnavailable
	}
	ctx = sourceValidationContext(ctx)
	if err := ctx.Err(); err != nil {
		return RecoveryAuthorizationResult{}, false, err
	}
	lookup, err := normalizeAuthorizationLookup(request, service.now().UTC())
	if err != nil {
		return RecoveryAuthorizationResult{}, false, err
	}
	if request.Operation == AuthorizationReceiptExecute {
		return service.loadAuthorizationReplay(ctx, lookup)
	}
	normalized, err := service.normalizeAuthorizationWithRetry(ctx, request, false)
	if err != nil {
		return RecoveryAuthorizationResult{}, false, err
	}
	return service.loadAuthorizationReplay(ctx, normalized)
}

func (service *AuthorizationService) Authorize(
	ctx context.Context,
	request RecoveryAuthorizationRequest,
) (result RecoveryAuthorizationResult, resultErr error) {
	if service == nil || service.db == nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationUnavailable
	}
	effectTransactionStarted := false
	defer func() {
		if resultErr != nil && !effectTransactionStarted {
			service.metrics.ObserveCategory(request.Category, recoveryAuthorizationMetricOutcome(resultErr))
		}
	}()
	ctx = sourceValidationContext(ctx)
	if err := ctx.Err(); err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	now := service.now().UTC()
	replayLookup, err := normalizeAuthorizationLookup(request, now)
	if err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	var receiptExists bool
	receiptErr := retryAuthorizationRead(ctx, func() error {
		var readErr error
		receiptExists, readErr = authorizationReceiptKeyExists(ctx, service.db, replayLookup)
		return readErr
	})
	if receiptErr != nil {
		return RecoveryAuthorizationResult{}, publicAuthorizationReadError(ctx, receiptErr)
	}
	if receiptExists {
		if request.Operation == AuthorizationReceiptExecute {
			if replay, found, replayErr := service.loadAuthorizationReplay(ctx, replayLookup); found || replayErr != nil {
				return replay, replayErr
			}
		} else {
			replayNormalized, normalizeErr := service.normalizeAuthorizationWithRetry(ctx, request, false)
			if normalizeErr != nil {
				return RecoveryAuthorizationResult{}, normalizeErr
			}
			if replay, found, replayErr := service.loadAuthorizationReplay(ctx, replayNormalized); found || replayErr != nil {
				return replay, replayErr
			}
		}
	}
	if !validAuthorizationProof(request.Proof, request.RequesterID, now) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if request.Proof.ExpiresAt.UTC().After(request.Session.ExpiresAt.UTC()) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationProofLifetime
	}
	var proofUsed bool
	proofErr := retryAuthorizationRead(ctx, func() error {
		var readErr error
		proofUsed, readErr = authorizationProofDigestExists(ctx, service.db, replayLookup.proofDigest)
		return readErr
	})
	if proofErr != nil {
		return RecoveryAuthorizationResult{}, publicAuthorizationReadError(ctx, proofErr)
	} else if proofUsed {
		if request.Operation == AuthorizationReceiptExecute {
			if replay, found, replayErr := service.loadAuthorizationReplay(ctx, replayLookup); found || replayErr != nil {
				return replay, replayErr
			}
		} else {
			replayNormalized, normalizeErr := service.normalizeAuthorizationWithRetry(ctx, request, false)
			if normalizeErr == nil {
				if replay, found, replayErr := service.loadAuthorizationReplay(ctx, replayNormalized); found || replayErr != nil {
					return replay, replayErr
				}
			}
		}
		return RecoveryAuthorizationResult{}, ErrAuthorizationProofUsed
	}
	normalized, err := service.normalizeAuthorizationWithRetry(ctx, request, true)
	if err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	var preparedExecute *preparedExecuteAggregate
	if normalized.request.Operation == AuthorizationReceiptExecute {
		preparedExecute, err = service.prepareExecuteAggregate(ctx, normalized, now)
		if err != nil {
			return RecoveryAuthorizationResult{}, publicPreparedExecuteError(err)
		}
		defer clear(preparedExecute.cleanupKey.Key)
	}

	for attempt := 0; attempt < authorizationRetryAttempts; attempt++ {
		effectTransactionStarted = true
		result, created, authorizeErr := service.authorizeOnce(ctx, normalized, preparedExecute)
		if authorizeErr == nil {
			if created && normalized.request.Operation == AuthorizationReceiptExecute {
				provider, _ := recoveryJobProvider(ctx, service.db, result.JobID)
				service.metrics.ObserveState(provider, JobStateQueued)
			}
			if created {
				service.metrics.ObserveCategory(normalized.request.Category, MetricOutcomeSuccess)
			}
			service.writeAuthorizationAudit(ctx, normalized.request, result)
			return result, nil
		}
		if replay, found, replayErr := service.loadAuthorizationReplay(ctx, normalized); found || replayErr != nil {
			return replay, replayErr
		}
		if isAuthorizationPublicError(authorizeErr) || isAuthorizationFault(authorizeErr) {
			return RecoveryAuthorizationResult{}, authorizeErr
		}
		proofUsed, proofErr := authorizationProofDigestExists(ctx, service.db, normalized.proofDigest)
		if proofErr == nil && proofUsed {
			if replay, found, replayErr := service.loadAuthorizationReplay(ctx, normalized); found || replayErr != nil {
				return replay, replayErr
			}
			return RecoveryAuthorizationResult{}, ErrAuthorizationProofUsed
		}
		if !isAuthorizationRetryable(authorizeErr) || attempt+1 == authorizationRetryAttempts {
			return RecoveryAuthorizationResult{}, ErrAuthorizationUnavailable
		}
		if err := waitForPlanCreateRetry(ctx, attempt); err != nil {
			return RecoveryAuthorizationResult{}, err
		}
	}
	return RecoveryAuthorizationResult{}, ErrAuthorizationUnavailable
}

func (service *AuthorizationService) authorizeOnce(
	ctx context.Context,
	normalized normalizedRecoveryAuthorization,
	preparedExecute *preparedExecuteAggregate,
) (RecoveryAuthorizationResult, bool, error) {
	authority, err := service.observeAuthorizationAuthority(ctx, normalized)
	if err != nil {
		return RecoveryAuthorizationResult{}, false, err
	}
	var result RecoveryAuthorizationResult
	created := false
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if replay, found, err := loadAuthorizationReplay(ctx, tx, normalized, service.now().UTC()); found || err != nil {
			result = replay
			return err
		}
		used, err := authorizationProofDigestExists(ctx, tx, normalized.proofDigest)
		if err != nil {
			return err
		}
		if used {
			return ErrAuthorizationProofUsed
		}
		result, err = service.persistAuthorizationTx(ctx, tx, normalized, preparedExecute, authority)
		if err != nil {
			return err
		}
		if service.beforePersist != nil {
			if err := service.beforePersist(authorizationPersistBeforeCommit); err != nil {
				return &authorizationFaultError{cause: err}
			}
		}
		created = true
		return nil
	})
	if err != nil {
		return RecoveryAuthorizationResult{}, false, err
	}
	return result, created, nil
}

type observedAuthorizationAuthority struct {
	binding     RecoveryAuthorityBinding
	observation RecoveryAuthorityObservation
}

func (service *AuthorizationService) observeAuthorizationAuthority(
	ctx context.Context,
	normalized normalizedRecoveryAuthorization,
) (observedAuthorizationAuthority, error) {
	if service == nil || service.db == nil || service.liveRevalidator == nil {
		return observedAuthorizationAuthority{}, ErrAuthorizationUnavailable
	}
	intent, err := loadAuthorizationIntentBinding(ctx, service.db, normalized.request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return observedAuthorizationAuthority{}, ctxErr
		}
		if isAuthorizationRetryable(err) {
			return observedAuthorizationAuthority{}, err
		}
		return observedAuthorizationAuthority{}, ErrAuthorizationDenied
	}
	if intent.plan.RequesterID != normalized.request.RequesterID ||
		intent.plan.TransitionRevision != normalized.request.ExpectedPlanRevision ||
		intent.preflight.ID == "" || intent.preflight.PlanID != intent.plan.ID {
		return observedAuthorizationAuthority{}, ErrAuthorizationDenied
	}
	if err := validateAuthorizationPreflightBindings(intent.plan, intent.preflight, service.now().UTC()); err != nil {
		return observedAuthorizationAuthority{}, ErrAuthorizationDenied
	}
	providerKind, err := recoveryAuthorityProvider(ctx, service.db, intent.plan)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return observedAuthorizationAuthority{}, ctxErr
		}
		if isAuthorizationRetryable(err) {
			return observedAuthorizationAuthority{}, err
		}
		return observedAuthorizationAuthority{}, ErrAuthorizationDenied
	}
	binding := recoveryAuthorityBinding(normalized.request.Operation, providerKind, intent.plan, intent.preflight)
	observation, err := service.liveRevalidator.ObserveRecoveryAuthority(ctx, binding)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return observedAuthorizationAuthority{}, ctxErr
		}
		return observedAuthorizationAuthority{}, ErrAuthorizationDenied
	}
	return observedAuthorizationAuthority{binding: binding, observation: observation}, nil
}

type authorizationFaultError struct{ cause error }

func (err *authorizationFaultError) Error() string {
	return "recovery authorization persistence failed"
}
func (err *authorizationFaultError) Unwrap() error { return err.cause }

func isAuthorizationFault(err error) bool {
	var fault *authorizationFaultError
	return errors.As(err, &fault)
}

func isAuthorizationPublicError(err error) bool {
	return errors.Is(err, ErrInvalidRecoveryAuthorization) ||
		errors.Is(err, ErrInvalidAuthorizationGrantSecret) ||
		errors.Is(err, ErrAuthorizationIdempotencyConflict) ||
		errors.Is(err, ErrAuthorizationProofUsed) ||
		errors.Is(err, ErrAuthorizationSessionMismatch) ||
		errors.Is(err, ErrAuthorizationProofLifetime) ||
		errors.Is(err, ErrAuthorizationDenied)
}

func isAuthorizationRetryable(err error) bool {
	return isPlanDuplicateKey(err) || isPlanDatabaseBusy(err)
}

func retryAuthorizationRead(ctx context.Context, read func() error) error {
	for attempt := 0; attempt < authorizationRetryAttempts; attempt++ {
		err := read()
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if !isAuthorizationRetryable(err) {
			return err
		}
		if attempt+1 == authorizationRetryAttempts {
			return ErrAuthorizationUnavailable
		}
		if err := waitForPlanCreateRetry(ctx, attempt); err != nil {
			return err
		}
	}
	return ErrAuthorizationUnavailable
}

func publicAuthorizationReadError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if isAuthorizationPublicError(err) {
		return err
	}
	return ErrAuthorizationUnavailable
}

func (service *AuthorizationService) normalizeAuthorizationWithRetry(
	ctx context.Context,
	request RecoveryAuthorizationRequest,
	requireProof bool,
) (normalizedRecoveryAuthorization, error) {
	var normalized normalizedRecoveryAuthorization
	err := retryAuthorizationRead(ctx, func() error {
		var readErr error
		normalized, readErr = service.normalizeAuthorization(ctx, request, requireProof)
		return readErr
	})
	if err != nil {
		return normalizedRecoveryAuthorization{}, publicAuthorizationReadError(ctx, err)
	}
	return normalized, nil
}

func (service *AuthorizationService) loadAuthorizationReplay(
	ctx context.Context,
	normalized normalizedRecoveryAuthorization,
) (RecoveryAuthorizationResult, bool, error) {
	for attempt := 0; attempt < authorizationRetryAttempts; attempt++ {
		result, found, err := loadAuthorizationReplay(ctx, service.db, normalized, service.now().UTC())
		if err == nil || !isAuthorizationRetryable(err) {
			return result, found, err
		}
		if attempt+1 == authorizationRetryAttempts {
			return RecoveryAuthorizationResult{}, false, ErrAuthorizationUnavailable
		}
		if err := waitForPlanCreateRetry(ctx, attempt); err != nil {
			return RecoveryAuthorizationResult{}, false, err
		}
	}
	return RecoveryAuthorizationResult{}, false, ErrAuthorizationUnavailable
}

func (service *AuthorizationService) normalizeAuthorization(
	ctx context.Context,
	request RecoveryAuthorizationRequest,
	requireProof bool,
) (normalizedRecoveryAuthorization, error) {
	now := service.now().UTC()
	normalized, err := normalizeAuthorizationLookup(request, now)
	if err != nil {
		return normalizedRecoveryAuthorization{}, err
	}
	if requireProof && !validAuthorizationProof(request.Proof, request.RequesterID, now) {
		return normalizedRecoveryAuthorization{}, ErrAuthorizationDenied
	}
	if requireProof && request.Proof.ExpiresAt.UTC().After(request.Session.ExpiresAt.UTC()) {
		return normalizedRecoveryAuthorization{}, ErrAuthorizationProofLifetime
	}
	if requireProof {
		if request.Operation == AuthorizationReceiptExecute {
			if request.Reason != "" || !validOpaqueID(request.GrantID) || request.JobID != "" ||
				request.CheckpointID != "" || request.AttemptID != "" {
				return normalizedRecoveryAuthorization{}, ErrInvalidRecoveryAuthorization
			}
		} else if request.Reason == "" {
			return normalizedRecoveryAuthorization{}, ErrInvalidRecoveryAuthorization
		}
		if request.Operation == AuthorizationReceiptDeleteAuthorize {
			if !validOpaqueID(request.JobID) || !validOpaqueID(request.CheckpointID) ||
				!validOpaqueID(request.AttemptID) || request.GrantID != "" {
				return normalizedRecoveryAuthorization{}, ErrInvalidRecoveryAuthorization
			}
		} else if request.Operation != AuthorizationReceiptExecute &&
			(request.JobID != "" || request.CheckpointID != "" || request.GrantID != "" || request.AttemptID != "") {
			return normalizedRecoveryAuthorization{}, ErrInvalidRecoveryAuthorization
		}
		if request.Operation == AuthorizationReceiptSecurityOverride && !request.FindingCategory.known() {
			return normalizedRecoveryAuthorization{}, ErrInvalidRecoveryAuthorization
		}
	}

	binding, err := loadAuthorizationIntentBinding(ctx, service.db, request)
	if err != nil {
		if errors.Is(err, ErrAuthorizationDenied) {
			return normalizedRecoveryAuthorization{}, ErrAuthorizationDenied
		}
		if isAuthorizationRetryable(err) {
			return normalizedRecoveryAuthorization{}, err
		}
		return normalizedRecoveryAuthorization{}, ErrAuthorizationUnavailable
	}
	normalized.intentDigest = authorizationIntentDigest(request, normalized.reasonDigest, normalized.grantSecretHash, binding)
	if requireProof {
		normalized.replayExpiresAt = now.Add(service.receiptReplayTTL)
		if normalized.replayExpiresAt.After(request.Session.ExpiresAt.UTC()) {
			normalized.replayExpiresAt = request.Session.ExpiresAt.UTC()
		}
		if request.Proof.ExpiresAt.UTC().After(normalized.replayExpiresAt) {
			return normalizedRecoveryAuthorization{}, ErrAuthorizationProofLifetime
		}
		switch request.Operation {
		case AuthorizationReceiptWriteAuthorize:
			normalized.grantExpiresAt = now.Add(service.writeGrantTTL)
		case AuthorizationReceiptDeleteAuthorize:
			normalized.grantExpiresAt = now.Add(service.deleteGrantTTL)
		}
		if !normalized.grantExpiresAt.IsZero() && normalized.grantExpiresAt.After(normalized.replayExpiresAt) {
			return normalizedRecoveryAuthorization{}, ErrAuthorizationProofLifetime
		}
	}
	return normalized, nil
}

// normalizeAuthorizationLookup validates only the facts needed to locate an
// existing receipt or a globally consumed proof. Full plan/effect binding is
// deliberately deferred until after that proof-used check.
func normalizeAuthorizationLookup(
	request RecoveryAuthorizationRequest,
	now time.Time,
) (normalizedRecoveryAuthorization, error) {
	if now.IsZero() || request.RequesterID == 0 || !validOpaqueID(request.PlanID) ||
		!validPlanIdempotencyKey(request.IdempotencyKey) || request.ExpectedPlanRevision == 0 ||
		!authorizationOperationMatches(request.Operation, request.Category, request.Endpoint) ||
		!validAuthorizationSession(request.Session, request.RequesterID, now) ||
		len(request.Reason) > maxAuthorizationReasonBytes || strings.TrimSpace(request.Reason) != request.Reason {
		return normalizedRecoveryAuthorization{}, ErrInvalidRecoveryAuthorization
	}

	needsSecret := request.Operation == AuthorizationReceiptWriteAuthorize ||
		request.Operation == AuthorizationReceiptDeleteAuthorize || request.Operation == AuthorizationReceiptExecute
	secretHash := ""
	if needsSecret {
		if !validAuthorizationGrantSecret(request.GrantSecret) {
			return normalizedRecoveryAuthorization{}, ErrInvalidAuthorizationGrantSecret
		}
		secretCategory := request.Category
		if request.Operation == AuthorizationReceiptExecute {
			secretCategory = AuthorizationReceiptCategoryWrite
		}
		secretHash = authorizationGrantSecretHash(secretCategory, request.PlanID, request.JobID, request.CheckpointID, request.GrantSecret)
	} else if request.GrantSecret != "" {
		return normalizedRecoveryAuthorization{}, ErrInvalidAuthorizationGrantSecret
	}
	reasonDigest := framedDigest(recoveryAuthorizationReasonDomain, request.Reason)
	normalized := normalizedRecoveryAuthorization{
		request: request,
		keyDigest: framedDigest(recoveryAuthorizationIdempotencyDomain,
			strconv.FormatUint(uint64(request.RequesterID), 10), request.Endpoint, request.IdempotencyKey),
		proofDigest: framedDigest(recoveryAuthorizationProofDomain, request.Proof.JTI),
		sessionDigest: framedDigest(recoveryAuthorizationSessionDomain, request.Session.JTI,
			strconv.FormatUint(uint64(request.Session.UserID), 10), request.Session.Role,
			strconv.FormatUint(uint64(request.Session.TokenVersion), 10), request.Session.ExpiresAt.UTC().Format(time.RFC3339Nano)),
		reasonDigest: reasonDigest, grantSecretHash: secretHash,
	}
	return normalized, nil
}

func authorizationOperationMatches(
	operation AuthorizationReceiptOperation,
	category AuthorizationReceiptCategory,
	endpoint string,
) bool {
	switch operation {
	case AuthorizationReceiptSecurityOverride:
		return category == AuthorizationReceiptCategorySecurityOverride && endpoint == recoverySecurityOverrideEndpoint
	case AuthorizationReceiptWriteAuthorize:
		return category == AuthorizationReceiptCategoryWrite && endpoint == recoveryWriteAuthorizationEndpoint
	case AuthorizationReceiptDeleteAuthorize:
		return category == AuthorizationReceiptCategoryExactMirrorDelete && endpoint == recoveryDeleteAuthorizationEndpoint
	case AuthorizationReceiptExecute:
		return category == AuthorizationReceiptCategoryExecute && endpoint == recoveryExecuteEndpoint
	default:
		return false
	}
}

func validAuthorizationSession(session RecoveryAuthorizationSession, requesterID uint, now time.Time) bool {
	return session.JTI != "" && len(session.JTI) <= 256 && session.UserID == requesterID &&
		session.Role == "admin" && session.TokenVersion > 0 && now.Before(session.ExpiresAt.UTC())
}

func validAuthorizationProof(proof RecoveryAuthorizationProof, requesterID uint, now time.Time) bool {
	return proof.JTI != "" && len(proof.JTI) <= 256 && proof.Action == "asset.recover" &&
		proof.UserID == requesterID && proof.Role == "admin" && proof.TokenVersion > 0 &&
		now.Before(proof.ExpiresAt.UTC())
}

func validAuthorizationGrantSecret(value string) bool {
	if len(value) != 43 || strings.TrimSpace(value) != value || strings.Contains(value, "=") {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func authorizationGrantSecretHash(
	category AuthorizationReceiptCategory,
	planID, jobID, checkpointID, secret string,
) string {
	return framedDigest(recoveryAuthorizationGrantSecretDomain, string(category), planID, jobID, checkpointID, secret)
}

func loadAuthorizationIntentBinding(
	ctx context.Context,
	db *gorm.DB,
	request RecoveryAuthorizationRequest,
) (authorizationIntentBinding, error) {
	if db == nil {
		return authorizationIntentBinding{}, ErrAuthorizationUnavailable
	}
	binding := authorizationIntentBinding{}
	loaded := db.WithContext(ctx).Where("id = ? AND requester_id = ?", request.PlanID, request.RequesterID).
		Limit(1).Find(&binding.plan)
	if loaded.Error != nil {
		return authorizationIntentBinding{}, loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return authorizationIntentBinding{}, ErrAuthorizationDenied
	}

	loaded = db.WithContext(ctx).Table((model.RecoveryPoint{}).TableName()).
		Select("id, repository_id, source_fingerprint, manifest_digest, semantics, state, capability_revision").
		Where("id = ? AND repository_id = ?", binding.plan.RecoveryPointID, binding.plan.RepositoryID).
		Limit(1).Find(&binding.source)
	if loaded.Error != nil {
		return authorizationIntentBinding{}, loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return authorizationIntentBinding{}, ErrAuthorizationDenied
	}

	loadPreflight := func(id string) error {
		if !validOpaqueID(id) {
			return ErrAuthorizationDenied
		}
		result := db.WithContext(ctx).Where("id = ? AND plan_id = ?", id, binding.plan.ID).
			Limit(1).Find(&binding.preflight)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAuthorizationDenied
		}
		return nil
	}

	switch request.Operation {
	case AuthorizationReceiptSecurityOverride, AuthorizationReceiptWriteAuthorize:
		if err := loadPreflight(request.PreflightID); err != nil {
			return authorizationIntentBinding{}, err
		}
	case AuthorizationReceiptExecute:
		if err := loadPreflight(request.PreflightID); err != nil {
			return authorizationIntentBinding{}, err
		}
		grant := model.BackupAssetRecoveryGrant{}
		result := db.WithContext(ctx).Where("id = ? AND plan_id = ?", request.GrantID, binding.plan.ID).
			Limit(1).Find(&grant)
		if result.Error != nil {
			return authorizationIntentBinding{}, result.Error
		}
		if result.RowsAffected != 1 {
			return authorizationIntentBinding{}, ErrAuthorizationDenied
		}
		binding.grant = &grant
	case AuthorizationReceiptDeleteAuthorize:
		job := model.BackupAssetRecoveryJob{}
		result := db.WithContext(ctx).Where("id = ? AND plan_id = ?", request.JobID, binding.plan.ID).
			Limit(1).Find(&job)
		if result.Error != nil {
			return authorizationIntentBinding{}, result.Error
		}
		if result.RowsAffected != 1 {
			return authorizationIntentBinding{}, ErrAuthorizationDenied
		}
		binding.job = &job
		if err := loadPreflight(job.PreflightID); err != nil {
			return authorizationIntentBinding{}, err
		}

		checkpoint := model.BackupAssetRecoveryCheckpoint{}
		result = db.WithContext(ctx).Where("id = ? AND job_id = ? AND attempt_id = ?",
			request.CheckpointID, job.ID, request.AttemptID).Limit(1).Find(&checkpoint)
		if result.Error != nil {
			return authorizationIntentBinding{}, result.Error
		}
		if result.RowsAffected != 1 {
			return authorizationIntentBinding{}, ErrAuthorizationDenied
		}
		binding.checkpoint = &checkpoint

		attempt := model.BackupAssetRecoveryAttempt{}
		result = db.WithContext(ctx).Where("id = ? AND job_id = ?", request.AttemptID, job.ID).
			Limit(1).Find(&attempt)
		if result.Error != nil {
			return authorizationIntentBinding{}, result.Error
		}
		if result.RowsAffected != 1 {
			return authorizationIntentBinding{}, ErrAuthorizationDenied
		}
		binding.attempt = &attempt

		nodeLease := model.BackupAssetRecoveryNodeLease{}
		result = db.WithContext(ctx).Where("job_id = ? AND attempt_id = ? AND fence = ?",
			job.ID, request.AttemptID, checkpoint.NodeFence).Limit(1).Find(&nodeLease)
		if result.Error != nil {
			return authorizationIntentBinding{}, result.Error
		}
		if result.RowsAffected != 1 {
			return authorizationIntentBinding{}, ErrAuthorizationDenied
		}
		binding.nodeLease = &nodeLease
	default:
		return authorizationIntentBinding{}, ErrInvalidRecoveryAuthorization
	}
	return binding, nil
}

func authorizationIntentDigest(
	request RecoveryAuthorizationRequest,
	reasonDigest, secretHash string,
	binding authorizationIntentBinding,
) string {
	plan := binding.plan
	preflight := binding.preflight
	values := []string{
		string(request.Operation), string(request.Category), request.Endpoint,
		strconv.FormatUint(uint64(request.RequesterID), 10), request.PlanID, request.JobID,
		request.CheckpointID, request.GrantID, request.AttemptID,
		strconv.FormatUint(request.ExpectedPlanRevision, 10), request.PreflightID,
		string(request.FindingCategory), reasonDigest, secretHash,
		"plan", plan.ID, plan.RepositoryID, plan.RecoveryPointID, plan.CatalogGenerationID,
		plan.SelectionDigest, plan.SourceRevisionDigest, plan.SourceRevisionKind,
		plan.ImmutableLocatorDigest, plan.ImmutableManifestDigest, plan.ObservationFingerprint,
		authorizationIntentTimePointer(plan.ObservedAt),
		plan.TargetMode, strconv.FormatUint(uint64(plan.TargetNodeID), 10), plan.TargetRootID,
		plan.RootLocatorDigest, plan.PathDigest, plan.TargetBaseRevision,
		plan.CredentialScopeRevision, plan.RootRevision, plan.FilesystemRevision,
		plan.CapabilityRevision, plan.ConflictPolicy, plan.OperationSetDigest, plan.DeleteSetDigest,
		plan.PreflightRevision, authorizationIntentTime(plan.PreflightExpiresAt),
		strconv.FormatInt(plan.EstimatedItems, 10), strconv.FormatInt(plan.EstimatedBytes, 10),
	}
	if request.Operation == AuthorizationReceiptSecurityOverride {
		values = append(values,
			"security_override_precondition", string(SecurityDecisionBlock),
			plan.SecurityFindingSetDigest, plan.SecurityPolicyRevision,
			preflight.FindingSetDigest, preflight.PolicyRevision,
			preflight.SecurityOverrideCandidateDigest, preflight.SecurityOverrideCategories,
		)
	} else {
		values = append(values,
			"security", plan.BindingDigest, plan.SecurityDecision, plan.SecurityDecisionDigest,
			plan.SecurityFindingSetDigest, plan.SecurityPolicyRevision, plan.SecurityOverrideBindingDigest,
		)
	}
	values = append(values,
		"preflight", preflight.ID, preflight.PlanID, preflight.Revision, preflight.SourceRevisionDigest,
		strconv.FormatUint(uint64(preflight.TargetNodeID), 10), preflight.NodeRevision,
		preflight.TargetRootID, preflight.RootLocatorDigest, preflight.PathDigest, preflight.TargetRevision,
		preflight.CapabilityRevision, preflight.PolicyRevision, preflight.FindingSetDigest,
		preflight.SecurityOverrideCandidateDigest, preflight.SecurityOverrideCategories,
		preflight.OperationSetDigest, preflight.DeleteSetDigest,
		strconv.FormatInt(preflight.EstimatedItems, 10), strconv.FormatInt(preflight.EstimatedBytes, 10),
		authorizationIntentTime(preflight.ExpiresAt), authorizationIntentTime(preflight.CreatedAt),
		"source", binding.source.ID, binding.source.RepositoryID, binding.source.SourceFingerprint,
		binding.source.ManifestDigest, binding.source.Semantics, binding.source.State,
		strconv.Itoa(binding.source.CapabilityRevision),
	)
	if binding.grant != nil {
		grant := binding.grant
		values = append(values,
			"grant", grant.ID, grant.PlanID, authorizationIntentStringPointer(grant.JobID),
			grant.AuthorityCategory, grant.GrantHash, grant.BindingDigest,
			authorizationIntentStringPointer(grant.DeleteCheckpointID), grant.DeleteSetDigest,
			grant.DeleteTargetRevision, authorizationIntentStringPointer(grant.DeleteAttemptID),
			strconv.FormatUint(grant.DeleteAttemptFence, 10), strconv.FormatUint(grant.DeleteNodeFence, 10),
			authorizationIntentTime(grant.ExpiresAt),
		)
	} else {
		values = append(values, "grant", "", "", "", "", "", "", "", "", "", "", "", "", "")
	}
	if binding.job != nil {
		job := binding.job
		values = append(values,
			"job", job.ID, job.PlanID, job.PlanBindingDigest, job.SelectionDigest, job.SourceRevisionDigest,
			job.PreflightID, job.PreflightRevision, authorizationIntentTime(job.PreflightExpiresAt),
			job.PreflightTargetRevision, job.PreflightNodeRevision, job.CapabilityRevision,
			job.OperationSetDigest, job.DeleteSetDigest, job.SecurityDecision, job.SecurityDecisionDigest,
			job.SecurityFindingSetDigest, job.SecurityPolicyRevision, job.SecurityOverrideBindingDigest,
			job.AuthorityGrantID, job.AuthorityCategory, job.AuthorityBindingDigest,
			authorizationIntentTime(job.AuthorityExpiresAt), job.TargetMode,
			strconv.FormatUint(uint64(job.TargetNodeID), 10), job.TargetRootID,
			job.RootLocatorDigest, job.PathDigest, job.TargetChainRevision,
		)
	} else {
		values = append(values, "job", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "")
	}
	if binding.checkpoint != nil {
		checkpoint := binding.checkpoint
		values = append(values,
			"checkpoint", checkpoint.ID, checkpoint.JobID, checkpoint.AttemptID,
			strconv.Itoa(checkpoint.Sequence), checkpoint.Phase, checkpoint.AuthorityCategory,
			checkpoint.OperationDigest, checkpoint.PriorTargetRevision, checkpoint.NextTargetRevision,
			strconv.FormatUint(checkpoint.NodeFence, 10), strconv.FormatUint(checkpoint.AttemptFence, 10),
			checkpoint.PlanBindingDigest, checkpoint.SourceRevisionDigest, checkpoint.PreflightID,
			checkpoint.PreflightRevision, authorizationIntentTime(checkpoint.PreflightExpiresAt),
			checkpoint.SecurityDecision, checkpoint.SecurityDecisionDigest, checkpoint.SecurityFindingSetDigest,
			checkpoint.SecurityPolicyRevision, checkpoint.AuthorityGrantID, checkpoint.JobAuthorityCategory,
			checkpoint.AuthorityBindingDigest, authorizationIntentTime(checkpoint.AuthorityExpiresAt),
			checkpoint.DeleteNodeRevision, checkpoint.DeleteRootRevision,
			authorizationIntentTimePointer(checkpoint.DeleteAuthorityExpiresAt), checkpoint.DeleteGrantID,
			checkpoint.DeleteGrantBindingDigest, authorizationIntentTimePointer(checkpoint.DeleteGrantExpiresAt),
		)
	} else {
		values = append(values, "checkpoint", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "")
	}
	if binding.attempt != nil {
		values = append(values, "attempt", binding.attempt.ID, binding.attempt.JobID,
			strconv.FormatUint(binding.attempt.Fence, 10))
	} else {
		values = append(values, "attempt", "", "", "")
	}
	if binding.nodeLease != nil {
		values = append(values, "node_lease", binding.nodeLease.ID,
			strconv.FormatUint(uint64(binding.nodeLease.NodeID), 10), binding.nodeLease.JobID,
			authorizationIntentStringPointer(binding.nodeLease.AttemptID),
			strconv.FormatUint(binding.nodeLease.Fence, 10))
	} else {
		values = append(values, "node_lease", "", "", "", "", "")
	}
	return framedDigest(recoveryAuthorizationIntentDomain, values...)
}

func executeAuthorizationReplayIntentDigest(
	normalized normalizedRecoveryAuthorization,
	result RecoveryAuthorizationResult,
) string {
	request := normalized.request
	return framedDigest(
		recoveryAuthorizationExecuteReplayDomain,
		string(request.Operation), string(request.Category), request.Endpoint,
		strconv.FormatUint(uint64(request.RequesterID), 10), request.PlanID,
		request.JobID, request.CheckpointID, request.GrantID, request.AttemptID,
		strconv.FormatUint(request.ExpectedPlanRevision, 10), request.PreflightID,
		string(request.FindingCategory), normalized.reasonDigest, normalized.grantSecretHash,
		result.PlanID, result.GrantID, result.JobID, result.AttemptID,
		result.SourceLeaseID, result.NodeLeaseID, strconv.FormatUint(result.NodeLeaseFence, 10),
		strconv.FormatUint(result.PlanTransitionRevision, 10),
	)
}

func authorizationIntentTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func authorizationIntentTimePointer(value *time.Time) string {
	if value == nil {
		return ""
	}
	return authorizationIntentTime(*value)
}

func authorizationIntentStringPointer(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (service *AuthorizationService) refreshAuthorizationIntentTx(
	ctx context.Context,
	tx *gorm.DB,
	normalized *normalizedRecoveryAuthorization,
) error {
	if normalized == nil {
		return ErrAuthorizationUnavailable
	}
	binding, err := loadAuthorizationIntentBinding(ctx, tx, normalized.request)
	if err != nil {
		if errors.Is(err, ErrAuthorizationDenied) {
			return ErrAuthorizationDenied
		}
		return ErrAuthorizationUnavailable
	}
	digest := authorizationIntentDigest(
		normalized.request,
		normalized.reasonDigest,
		normalized.grantSecretHash,
		binding,
	)
	if digest != normalized.intentDigest {
		return ErrAuthorizationIdempotencyConflict
	}
	normalized.intentDigest = digest
	return nil
}

func loadAuthorizationReplay(
	ctx context.Context,
	db *gorm.DB,
	normalized normalizedRecoveryAuthorization,
	now time.Time,
) (RecoveryAuthorizationResult, bool, error) {
	var receipt model.BackupAssetRecoveryEvidence
	query := db.WithContext(ctx).Where(
		"kind = ? AND requester_id = ? AND endpoint = ? AND idempotency_key_digest = ?",
		"authorization_receipt", normalized.request.RequesterID, normalized.request.Endpoint, normalized.keyDigest,
	).Limit(1).Find(&receipt)
	if query.Error != nil {
		return RecoveryAuthorizationResult{}, false, query.Error
	}
	if query.RowsAffected == 0 {
		return RecoveryAuthorizationResult{}, false, nil
	}
	if receipt.PresentingSessionDigest != normalized.sessionDigest {
		return RecoveryAuthorizationResult{}, true, ErrAuthorizationSessionMismatch
	}
	if receipt.ReplayExpiresAt == nil {
		return RecoveryAuthorizationResult{}, true, ErrAuthorizationUnavailable
	}
	if !now.Before(receipt.ReplayExpiresAt.UTC()) {
		return RecoveryAuthorizationResult{}, true, ErrAuthorizationIdempotencyConflict
	}
	result, err := authorizationResultFromReceipt(ctx, db, receipt)
	if err != nil {
		return RecoveryAuthorizationResult{}, true, err
	}
	wantIntent := normalized.intentDigest
	if AuthorizationReceiptOperation(receipt.Operation) == AuthorizationReceiptExecute {
		wantIntent = executeAuthorizationReplayIntentDigest(normalized, result)
	}
	if receipt.IntentDigest != wantIntent {
		return RecoveryAuthorizationResult{}, true, ErrAuthorizationIdempotencyConflict
	}
	result.Replay = true
	return result, true, nil
}

func authorizationResultFromReceipt(
	ctx context.Context,
	db *gorm.DB,
	receipt model.BackupAssetRecoveryEvidence,
) (RecoveryAuthorizationResult, error) {
	if receipt.Kind != "authorization_receipt" || receipt.PlanID == nil || !validOpaqueID(receipt.ID) ||
		!validOpaqueID(*receipt.PlanID) || receipt.ResultPlanTransitionRevision == 0 ||
		!authorizationOperationMatches(AuthorizationReceiptOperation(receipt.Operation),
			AuthorizationReceiptCategory(receipt.Category), receipt.Endpoint) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationUnavailable
	}
	result := RecoveryAuthorizationResult{
		ReceiptID: receipt.ID, PlanID: *receipt.PlanID,
		PlanTransitionRevision: receipt.ResultPlanTransitionRevision,
		NodeLeaseFence:         receipt.NodeLeaseFence,
	}
	if receipt.GrantID != nil {
		result.GrantID = *receipt.GrantID
	}
	if receipt.JobID != nil {
		result.JobID = *receipt.JobID
	}
	if receipt.AttemptID != nil {
		result.AttemptID = *receipt.AttemptID
	}
	if receipt.SourceLeaseID != nil {
		result.SourceLeaseID = *receipt.SourceLeaseID
	}
	if receipt.NodeLeaseID != nil {
		result.NodeLeaseID = *receipt.NodeLeaseID
	}
	if err := attachAuthorizationGrantMetadata(
		ctx,
		db,
		AuthorizationReceiptOperation(receipt.Operation),
		AuthorizationReceiptCategory(receipt.Category),
		receipt.GrantBindingDigest,
		true,
		&result,
	); err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	return result, nil
}

type authorizationGrantMetadata struct {
	ID                string
	AuthorityCategory string
	BindingDigest     string
	ExpiresAt         time.Time
}

func attachAuthorizationGrantMetadata(
	ctx context.Context,
	db *gorm.DB,
	operation AuthorizationReceiptOperation,
	category AuthorizationReceiptCategory,
	receiptBindingDigest string,
	requireReceiptBinding bool,
	result *RecoveryAuthorizationResult,
) error {
	expectedCategory, status, hasGrant := authorizationGrantEffect(operation, category)
	if result == nil || db == nil {
		return ErrAuthorizationUnavailable
	}
	if !hasGrant {
		if result.GrantID != "" || receiptBindingDigest != "" {
			return ErrAuthorizationUnavailable
		}
		return nil
	}
	if !validOpaqueID(result.GrantID) || requireReceiptBinding && !validDigest(receiptBindingDigest) {
		return ErrAuthorizationUnavailable
	}

	var grant authorizationGrantMetadata
	loaded := db.WithContext(ctx).Table((model.BackupAssetRecoveryGrant{}).TableName()).
		Select("id", "authority_category", "binding_digest", "expires_at").
		Where("id = ?", result.GrantID).Limit(1).Find(&grant)
	if loaded.Error != nil {
		return loaded.Error
	}
	if loaded.RowsAffected != 1 || grant.ID != result.GrantID ||
		AuthorityCategory(grant.AuthorityCategory) != expectedCategory ||
		!validDigest(grant.BindingDigest) || grant.ExpiresAt.IsZero() ||
		requireReceiptBinding && grant.BindingDigest != receiptBindingDigest {
		return ErrAuthorizationUnavailable
	}

	expiresAt := grant.ExpiresAt.UTC()
	result.GrantCategory = expectedCategory
	result.GrantBindingDigest = grant.BindingDigest
	result.GrantExpiresAt = &expiresAt
	result.GrantStatus = status
	return nil
}

func authorizationGrantEffect(
	operation AuthorizationReceiptOperation,
	category AuthorizationReceiptCategory,
) (AuthorityCategory, RecoveryAuthorizationGrantStatus, bool) {
	switch operation {
	case AuthorizationReceiptWriteAuthorize:
		if category == AuthorizationReceiptCategoryWrite {
			return AuthorityWrite, RecoveryAuthorizationGrantIssued, true
		}
	case AuthorizationReceiptDeleteAuthorize:
		if category == AuthorizationReceiptCategoryExactMirrorDelete {
			return AuthorityExactMirrorDelete, RecoveryAuthorizationGrantIssued, true
		}
	case AuthorizationReceiptExecute:
		if category == AuthorizationReceiptCategoryExecute {
			return AuthorityWrite, RecoveryAuthorizationGrantConsumed, true
		}
	case AuthorizationReceiptSecurityOverride:
		if category == AuthorizationReceiptCategorySecurityOverride {
			return "", "", false
		}
	}
	return "", "", false
}

func (service *AuthorizationService) persistAuthorizationTx(
	ctx context.Context,
	tx *gorm.DB,
	normalized normalizedRecoveryAuthorization,
	preparedExecute *preparedExecuteAggregate,
	authority observedAuthorizationAuthority,
) (RecoveryAuthorizationResult, error) {
	now := service.now().UTC()
	var plan model.BackupAssetRecoveryPlan
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", normalized.request.PlanID).Limit(1).Find(&plan)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || plan.RequesterID != normalized.request.RequesterID ||
		plan.TransitionRevision != normalized.request.ExpectedPlanRevision {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := service.refreshAuthorizationIntentTx(ctx, tx, &normalized); err != nil {
		return RecoveryAuthorizationResult{}, err
	}

	var result RecoveryAuthorizationResult
	var err error
	switch normalized.request.Operation {
	case AuthorizationReceiptSecurityOverride:
		result, err = service.persistSecurityOverrideTx(ctx, tx, normalized, &plan, now, authority)
	case AuthorizationReceiptWriteAuthorize:
		result, err = service.persistWriteAuthorizationTx(ctx, tx, normalized, &plan, now, authority)
	case AuthorizationReceiptExecute:
		result, err = service.persistExecuteAuthorizationTx(ctx, tx, normalized, &plan, preparedExecute, now, authority)
	case AuthorizationReceiptDeleteAuthorize:
		result, err = service.persistDeleteAuthorizationTx(ctx, tx, normalized, &plan, now, authority)
	default:
		err = ErrInvalidRecoveryAuthorization
	}
	if err != nil {
		return RecoveryAuthorizationResult{}, err
	}

	receiptID, err := backupasset.NewOpaqueID()
	if err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	if err := attachAuthorizationGrantMetadata(ctx, tx, normalized.request.Operation,
		normalized.request.Category, "", false, &result); err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	receipt := authorizationReceiptRow(normalized, result, receiptID, now)
	receipt.GrantBindingDigest = result.GrantBindingDigest
	if err := tx.WithContext(ctx).Create(&receipt).Error; err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	result.ReceiptID = receiptID
	return result, nil
}

func (service *AuthorizationService) persistSecurityOverrideTx(
	ctx context.Context,
	tx *gorm.DB,
	normalized normalizedRecoveryAuthorization,
	plan *model.BackupAssetRecoveryPlan,
	now time.Time,
	authority observedAuthorizationAuthority,
) (RecoveryAuthorizationResult, error) {
	if PlanState(plan.State) != PlanStatePreflightReady || plan.SecurityDecision != string(SecurityDecisionBlock) ||
		normalized.request.PreflightID == "" || !validOpaqueID(normalized.request.PreflightID) ||
		normalized.request.FindingCategory == SecurityFindingUnknown || !now.Before(plan.PreflightExpiresAt.UTC()) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	var preflight model.BackupAssetRecoveryPreflight
	if result := tx.WithContext(ctx).Where("id = ? AND plan_id = ?", normalized.request.PreflightID, plan.ID).
		Limit(1).Find(&preflight); result.Error != nil || result.RowsAffected != 1 {
		if result.Error != nil {
			return RecoveryAuthorizationResult{}, result.Error
		}
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := validateAuthorizationPreflightBindings(*plan, preflight, now); err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	candidate, err := loadPersistedSecurityOverrideCandidate(preflight)
	if err != nil || !candidate.allows(normalized.request.FindingCategory) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := service.sourceValidator.RevalidatePlanTx(ctx, tx, *plan); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := service.revalidateAuthorizationAuthorityTx(
		ctx, tx, normalized.request.Operation, *plan, preflight, authority,
	); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	nextRevision := plan.TransitionRevision + 1
	previousBindingDigest := plan.BindingDigest
	previousDecisionDigest := plan.SecurityDecisionDigest
	overrideBindingDigest := framedDigest(recoveryAuthorizationOverrideDomain,
		plan.ID, previousBindingDigest, previousDecisionDigest, plan.SecurityFindingSetDigest,
		plan.SecurityPolicyRevision, candidate.BindingDigest, string(normalized.request.FindingCategory), normalized.reasonDigest,
		strconv.FormatUint(nextRevision, 10))
	securityDecisionDigest := framedDigest(recoveryAuthorizationDecisionDomain,
		string(SecurityDecisionAdminOverride), previousDecisionDigest, plan.SecurityFindingSetDigest,
		plan.SecurityPolicyRevision, candidate.BindingDigest, overrideBindingDigest)
	planBindingDigest := framedDigest(recoveryAuthorizationPlanBindingDomain,
		previousBindingDigest, securityDecisionDigest, overrideBindingDigest, strconv.FormatUint(nextRevision, 10))
	plan.SecurityDecision = string(SecurityDecisionAdminOverride)
	plan.SecurityDecisionDigest = securityDecisionDigest
	plan.SecurityOverrideBindingDigest = overrideBindingDigest
	plan.BindingDigest = planBindingDigest
	plan.EncryptedOverrideReason = normalized.request.Reason
	plan.TransitionRevision = nextRevision
	plan.UpdatedAt = now
	updated := tx.WithContext(ctx).Model(plan).
		Where("id = ? AND state = ? AND transition_revision = ?", plan.ID, PlanStatePreflightReady, normalized.request.ExpectedPlanRevision).
		Select("security_decision", "security_decision_digest", "security_override_binding_digest", "binding_digest", "encrypted_override_reason", "transition_revision", "updated_at").
		Updates(plan)
	if updated.Error != nil {
		return RecoveryAuthorizationResult{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	return RecoveryAuthorizationResult{PlanID: plan.ID, PlanTransitionRevision: nextRevision}, nil
}

func (service *AuthorizationService) persistWriteAuthorizationTx(
	ctx context.Context,
	tx *gorm.DB,
	normalized normalizedRecoveryAuthorization,
	plan *model.BackupAssetRecoveryPlan,
	now time.Time,
	authority observedAuthorizationAuthority,
) (RecoveryAuthorizationResult, error) {
	if PlanState(plan.State) != PlanStatePreflightReady ||
		(plan.SecurityDecision != string(SecurityDecisionAllowClean) && plan.SecurityDecision != string(SecurityDecisionAdminOverride)) ||
		!validOpaqueID(normalized.request.PreflightID) || !now.Before(plan.PreflightExpiresAt.UTC()) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	var preflight model.BackupAssetRecoveryPreflight
	loaded := tx.WithContext(ctx).Where("id = ? AND plan_id = ?", normalized.request.PreflightID, plan.ID).
		Limit(1).Find(&preflight)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := validateAuthorizationPreflightBindings(*plan, preflight, now); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := service.sourceValidator.RevalidatePlanTx(ctx, tx, *plan); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := service.revalidateAuthorizationAuthorityTx(
		ctx, tx, normalized.request.Operation, *plan, preflight, authority,
	); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	grantID, err := backupasset.NewOpaqueID()
	if err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	bindingDigest := framedDigest(recoveryAuthorizationGrantBindingDomain,
		string(AuthorizationReceiptCategoryWrite), plan.ID, plan.BindingDigest,
		normalized.grantSecretHash, normalized.grantExpiresAt.UTC().Format(time.RFC3339Nano))
	grant := model.BackupAssetRecoveryGrant{
		ID: grantID, PlanID: plan.ID, AuthorityCategory: string(AuthorityWrite),
		GrantHash: normalized.grantSecretHash, ActorUserID: normalized.request.RequesterID,
		ActorSessionID: normalized.sessionDigest, BindingDigest: bindingDigest,
		EncryptedReason: normalized.request.Reason, ExpiresAt: normalized.grantExpiresAt,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&grant).Error; err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	nextRevision := plan.TransitionRevision + 1
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryPlan{}).
		Where("id = ? AND state = ? AND transition_revision = ?", plan.ID, PlanStatePreflightReady, plan.TransitionRevision).
		Updates(map[string]any{"state": string(PlanStateAuthorized), "transition_revision": nextRevision, "updated_at": now})
	if updated.Error != nil {
		return RecoveryAuthorizationResult{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	return RecoveryAuthorizationResult{
		PlanID: plan.ID, GrantID: grantID, PlanTransitionRevision: nextRevision,
	}, nil
}

func (service *AuthorizationService) persistExecuteAuthorizationTx(
	ctx context.Context,
	tx *gorm.DB,
	normalized normalizedRecoveryAuthorization,
	plan *model.BackupAssetRecoveryPlan,
	prepared *preparedExecuteAggregate,
	now time.Time,
	authority observedAuthorizationAuthority,
) (RecoveryAuthorizationResult, error) {
	if PlanState(plan.State) != PlanStateAuthorized || !validOpaqueID(normalized.request.GrantID) ||
		!validOpaqueID(normalized.request.PreflightID) || !now.Before(plan.PreflightExpiresAt.UTC()) ||
		prepared == nil || prepared.planID != plan.ID || prepared.preflightID != normalized.request.PreflightID {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	var preflight model.BackupAssetRecoveryPreflight
	loaded := tx.WithContext(ctx).Where("id = ? AND plan_id = ?", normalized.request.PreflightID, plan.ID).
		Limit(1).Find(&preflight)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := validateAuthorizationPreflightBindings(*plan, preflight, now); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := service.sourceValidator.RevalidatePlanTx(ctx, tx, *plan); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := service.revalidateAuthorizationAuthorityTx(
		ctx, tx, normalized.request.Operation, *plan, preflight, authority,
	); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	operationRows, err := rebuildExecuteOperationRows(*plan, preflight, tx.WithContext(ctx))
	if err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if !preparedExecuteMatchesLocked(*plan, operationRows, prepared) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	var grant model.BackupAssetRecoveryGrant
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND plan_id = ?", normalized.request.GrantID, plan.ID).Limit(1).Find(&grant)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || grant.AuthorityCategory != string(AuthorityWrite) ||
		grant.JobID != nil || !validDigest(grant.BindingDigest) ||
		grant.ConsumedAt != nil || grant.RevokedAt != nil || !now.Before(grant.ExpiresAt.UTC()) ||
		subtle.ConstantTimeCompare([]byte(grant.GrantHash), []byte(normalized.grantSecretHash)) != 1 {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := service.nodeAdmission.AdmitRecoveryTx(ctx, tx, plan.TargetNodeID); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	lockedCleanupKey, err := service.locatorKeys.LockActiveTx(ctx, tx, prepared.cleanupKey)
	if err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationUnavailable
	}
	defer clear(lockedCleanupKey.Key)
	if !sameRecoveryLocatorKey(lockedCleanupKey, prepared.cleanupKey) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationUnavailable
	}
	consumed := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryGrant{}).
		Where("id = ? AND plan_id = ? AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > ?",
			grant.ID, plan.ID, now).
		Updates(map[string]any{"consumed_at": now, "updated_at": now})
	if consumed.Error != nil {
		return RecoveryAuthorizationResult{}, consumed.Error
	}
	if consumed.RowsAffected != 1 {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}

	jobID := prepared.jobID
	absoluteDeadline := now.Add(service.policy.ExecutionTimeout).UTC()
	for _, bound := range []time.Time{grant.ExpiresAt.UTC(), preflight.ExpiresAt.UTC()} {
		if bound.Before(absoluteDeadline) {
			absoluteDeadline = bound
		}
	}
	if !absoluteDeadline.After(now) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	sourceLease, err := service.sourceLeases.AcquireTx(ctx, tx, backupasset.AcquireLeaseRequest{
		RecoveryPointID: plan.RecoveryPointID, HolderType: backupasset.LeaseHolderRecoveryJob, OwnerID: jobID,
		AbsoluteDeadline: absoluteDeadline,
	})
	if err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	attemptID := sourceLease.Fence.AttemptID
	nodeLeaseID, err := backupasset.NewOpaqueID()
	if err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	job := model.BackupAssetRecoveryJob{
		ID: jobID, PlanID: plan.ID, PlanBindingDigest: plan.BindingDigest,
		SelectionDigest: plan.SelectionDigest, SourceRevisionDigest: plan.SourceRevisionDigest,
		PreflightID: preflight.ID, PreflightRevision: preflight.Revision,
		PreflightExpiresAt: preflight.ExpiresAt, PreflightTargetRevision: preflight.TargetRevision,
		PreflightNodeRevision: preflight.NodeRevision, CapabilityRevision: preflight.CapabilityRevision,
		OperationSetDigest: preflight.OperationSetDigest, DeleteSetDigest: preflight.DeleteSetDigest,
		SecurityDecision: plan.SecurityDecision, SecurityDecisionDigest: plan.SecurityDecisionDigest,
		SecurityFindingSetDigest: plan.SecurityFindingSetDigest, SecurityPolicyRevision: plan.SecurityPolicyRevision,
		SecurityOverrideBindingDigest: plan.SecurityOverrideBindingDigest,
		EstimatedItems:                plan.EstimatedItems, EstimatedBytes: plan.EstimatedBytes,
		AuthorityGrantID: grant.ID, AuthorityCategory: string(AuthorityWrite),
		AuthorityBindingDigest: grant.BindingDigest, AuthorityExpiresAt: grant.ExpiresAt,
		AuthorityConsumedAt: now, State: "queued", TransitionRevision: 1, WorkspacePhase: "none",
		EncryptedWorkspaceRelativeLocator: prepared.encryptedWorkspaceLocator,
		WorkspaceBindingDigest:            prepared.workspaceBindingDigest,
		TargetMode:                        plan.TargetMode, TargetNodeID: plan.TargetNodeID, TargetRootID: plan.TargetRootID,
		RootLocatorDigest: plan.RootLocatorDigest, PathDigest: plan.PathDigest,
		TargetChainRevision: preflight.TargetRevision, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&job).Error; err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	for index := range prepared.items {
		if err := tx.WithContext(ctx).Create(&prepared.items[index].row).Error; err != nil {
			return RecoveryAuthorizationResult{}, err
		}
	}
	leaseExpiresAt := now.Add(service.nodeLeaseTTL)
	if sourceLease.LeaseExpiresAt.Before(leaseExpiresAt) {
		leaseExpiresAt = sourceLease.LeaseExpiresAt.UTC()
	}
	if sourceLease.AbsoluteDeadline.Before(leaseExpiresAt) {
		leaseExpiresAt = sourceLease.AbsoluteDeadline.UTC()
	}
	attempt := model.BackupAssetRecoveryAttempt{
		ID: attemptID, JobID: jobID, OwnerID: "recovery-authorization", Fence: 1,
		State: "claimed", LeaseExpiresAt: timePointerValue(leaseExpiresAt),
		HeartbeatAt: timePointerValue(now), CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&attempt).Error; err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	nodeLease := model.BackupAssetRecoveryNodeLease{
		ID: nodeLeaseID, NodeID: plan.TargetNodeID, HolderKind: "recovery_job", JobID: jobID,
		AttemptID: authorizationStringPointer(attemptID), OwnerID: "recovery-authorization",
		Fence: 1, State: "active", LeaseExpiresAt: leaseExpiresAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&nodeLease).Error; err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	nextRevision := plan.TransitionRevision + 1
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryPlan{}).
		Where("id = ? AND state = ? AND transition_revision = ?", plan.ID, PlanStateAuthorized, plan.TransitionRevision).
		Updates(map[string]any{"state": string(PlanStateExecuted), "transition_revision": nextRevision, "updated_at": now})
	if updated.Error != nil {
		return RecoveryAuthorizationResult{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	return RecoveryAuthorizationResult{
		PlanID: plan.ID, GrantID: grant.ID, JobID: jobID, AttemptID: attemptID,
		SourceLeaseID: sourceLease.ID, NodeLeaseID: nodeLeaseID, NodeLeaseFence: 1,
		PlanTransitionRevision: nextRevision,
	}, nil
}

func preparedExecuteMatchesLocked(
	plan model.BackupAssetRecoveryPlan,
	rows []executeOperationRow,
	prepared *preparedExecuteAggregate,
) bool {
	if prepared == nil || prepared.planID != plan.ID || !validOpaqueID(prepared.jobID) ||
		len(rows) == 0 || len(prepared.items) != len(rows) ||
		prepared.cleanupKey.Domain != backupasset.KeyDomainRecoveryCleanupOwnership ||
		prepared.cleanupKey.State != backupasset.DomainKeyActive || prepared.cleanupKey.Version <= 0 ||
		!validOpaqueID(prepared.cleanupKey.ID) || len(prepared.cleanupKey.Key) != 32 {
		return false
	}
	if TargetMode(plan.TargetMode) == TargetModeIsolated {
		if prepared.workspaceRelativeLocator != "jobs/"+prepared.jobID ||
			prepared.workspaceBindingDigest != recoveryWorkspaceBindingDigest(plan, prepared.jobID, prepared.workspaceRelativeLocator) ||
			prepared.encryptedWorkspaceLocator == "" || !secure.IsEncrypted(prepared.encryptedWorkspaceLocator) {
			return false
		}
	} else if prepared.workspaceRelativeLocator != "" || prepared.workspaceBindingDigest != "" ||
		prepared.encryptedWorkspaceLocator != "" {
		return false
	}

	for index, operationRow := range rows {
		operation := operationRow.operation
		item := prepared.items[index]
		row := item.row
		semanticDigest, err := SemanticTargetDigest(
			TargetMode(plan.TargetMode), plan.TargetRootID, plan.RootLocatorDigest, operation.TargetRelativeLocator,
		)
		if err != nil || semanticDigest != operation.SemanticTargetDigest {
			return false
		}
		finalLocator := operation.TargetRelativeLocator
		if prepared.workspaceRelativeLocator != "" {
			finalLocator = prepared.workspaceRelativeLocator + "/" + finalLocator
		}
		targetObjectDigest, err := TargetObjectDigest(plan.TargetRootID, plan.RootLocatorDigest, finalLocator)
		if err != nil || targetObjectDigest == semanticDigest {
			return false
		}
		if !sameAuthorizationString(row.PlanItemID, operationRow.planItemID) ||
			row.ID == "" || !validOpaqueID(row.ID) || row.PlanID != plan.ID || row.JobID != prepared.jobID ||
			row.Ordinal != index || row.OperationKind != string(operation.Kind) ||
			row.TargetPathDigest != operation.TargetPathDigest || row.SemanticTargetDigest != semanticDigest ||
			row.TargetObjectDigest != targetObjectDigest || row.ExpectedPriorKind != string(operation.ExpectedPrior.Kind) ||
			row.ExpectedPriorDigest != operation.ExpectedPrior.Digest ||
			row.ExpectedPostIdentityDigest != operation.ExpectedPostIdentityDigest ||
			row.ExpectedPostBytes != operation.ExpectedPostBytes || row.ExpectedPriorBytes != operation.ExpectedPriorBytes ||
			row.EncryptedTargetRelativeLocator == "" ||
			row.TargetLocatorKeyVersion != prepared.cleanupKey.Version ||
			row.TargetLocatorCipherVersion != targetLocatorCipherVersion ||
			row.DisplayClass != string(operation.DisplayClass) || row.EstimatedBytes != operation.EstimatedBytes ||
			item.sourceRecoveryPointID != operationRow.sourceRecoveryPointID || item.sourceEntryID != operationRow.sourceEntryID {
			return false
		}
	}
	return true
}

func sameAuthorizationString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameRecoveryLocatorKey(left, right backupasset.DomainKeyMaterial) bool {
	return left.ID == right.ID && left.Domain == right.Domain && left.Version == right.Version &&
		left.State == backupasset.DomainKeyActive && right.State == backupasset.DomainKeyActive &&
		len(left.Key) == 32 && len(right.Key) == 32 && subtle.ConstantTimeCompare(left.Key, right.Key) == 1
}

func recoveryAuthorityBinding(
	operation AuthorizationReceiptOperation,
	providerKind backupasset.ProviderKind,
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
) RecoveryAuthorityBinding {
	return RecoveryAuthorityBinding{
		Operation:              operation,
		Provider:               providerKind,
		PlanID:                 plan.ID,
		PlanBindingDigest:      plan.BindingDigest,
		PlanTransitionRevision: plan.TransitionRevision,
		RepositoryID:           plan.RepositoryID,
		RecoveryPointID:        plan.RecoveryPointID,
		CatalogGenerationID:    plan.CatalogGenerationID,
		SelectionDigest:        plan.SelectionDigest,
		SourceRevisionDigest:   plan.SourceRevisionDigest,
		ManifestDigest:         plan.ImmutableManifestDigest,
		SourceRef: provider.RsyncRestoreSourceRef{
			PlanID: plan.ID, PlanBindingDigest: plan.BindingDigest,
			RepositoryID: plan.RepositoryID, RecoveryPointID: plan.RecoveryPointID,
			CatalogGenerationID: plan.CatalogGenerationID, SelectionDigest: plan.SelectionDigest,
			SourceRevisionDigest: plan.SourceRevisionDigest, ManifestDigest: plan.ImmutableManifestDigest,
		},
		TargetMode:                    TargetMode(plan.TargetMode),
		TargetNodeID:                  plan.TargetNodeID,
		TargetRootID:                  plan.TargetRootID,
		RootLocatorDigest:             plan.RootLocatorDigest,
		PathDigest:                    plan.PathDigest,
		TargetBaseRevision:            plan.TargetBaseRevision,
		CredentialScopeRevision:       plan.CredentialScopeRevision,
		RootRevision:                  plan.RootRevision,
		FilesystemRevision:            plan.FilesystemRevision,
		CapabilityRevision:            plan.CapabilityRevision,
		ConflictPolicy:                ConflictPolicy(plan.ConflictPolicy),
		OperationSetDigest:            plan.OperationSetDigest,
		DeleteSetDigest:               plan.DeleteSetDigest,
		SecurityDecision:              SecurityDecisionKind(plan.SecurityDecision),
		SecurityDecisionDigest:        plan.SecurityDecisionDigest,
		SecurityFindingSetDigest:      plan.SecurityFindingSetDigest,
		SecurityPolicyRevision:        plan.SecurityPolicyRevision,
		SecurityOverrideBindingDigest: plan.SecurityOverrideBindingDigest,
		PreflightID:                   preflight.ID,
		PreflightRevision:             preflight.Revision,
		PreflightTargetRevision:       preflight.TargetRevision,
		PreflightNodeRevision:         preflight.NodeRevision,
		RequiredBytes:                 preflight.EstimatedBytes,
		RequiredInodes:                preflight.EstimatedItems,
	}
}

func recoveryAuthorityProvider(
	ctx context.Context,
	db *gorm.DB,
	plan model.BackupAssetRecoveryPlan,
) (backupasset.ProviderKind, error) {
	if db == nil || db.Error != nil || !validOpaqueID(plan.RepositoryID) || !validOpaqueID(plan.RecoveryPointID) {
		return "", ErrRecoverySourceUnavailable
	}
	var source struct {
		RepositoryID    string `gorm:"column:repository_id"`
		RecoveryPointID string `gorm:"column:recovery_point_id"`
		ProviderKind    string `gorm:"column:provider_kind"`
	}
	loaded := db.WithContext(ctx).Table("backup_repositories AS repositories").
		Select("repositories.id AS repository_id, points.id AS recovery_point_id, repositories.provider_kind").
		Joins("JOIN recovery_points AS points ON points.repository_id = repositories.id").
		Where("repositories.id = ? AND points.id = ?", plan.RepositoryID, plan.RecoveryPointID).
		Limit(1).Find(&source)
	if loaded.Error != nil {
		return "", loaded.Error
	}
	providerKind := backupasset.ProviderKind(source.ProviderKind)
	if loaded.RowsAffected != 1 || source.RepositoryID != plan.RepositoryID ||
		source.RecoveryPointID != plan.RecoveryPointID || !validRecoveryProvider(providerKind) {
		return "", ErrRecoverySourceChanged
	}
	return providerKind, nil
}

func (service *AuthorizationService) revalidateAuthorizationAuthorityTx(
	ctx context.Context,
	tx *gorm.DB,
	operation AuthorizationReceiptOperation,
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
	authority observedAuthorizationAuthority,
) error {
	if service == nil || service.liveRevalidator == nil || tx == nil || tx.Error != nil ||
		authority.binding.Operation != operation || authority.binding.PlanID != plan.ID ||
		authority.binding.PreflightID != preflight.ID {
		return ErrAuthorizationDenied
	}
	providerKind, err := recoveryAuthorityProvider(ctx, tx, plan)
	if err != nil {
		return err
	}
	lockedBinding := recoveryAuthorityBinding(operation, providerKind, plan, preflight)
	return service.liveRevalidator.RevalidateRecoveryAuthorityTx(
		ctx, tx, lockedBinding, authority.observation,
	)
}

func validateAuthorizationPreflightBindings(
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
	now time.Time,
) error {
	if now.IsZero() || !validOpaqueID(plan.ID) || !validOpaqueID(preflight.ID) ||
		preflight.PlanID != plan.ID || preflight.Revision != plan.PreflightRevision ||
		preflight.SourceRevisionDigest != plan.SourceRevisionDigest ||
		preflight.TargetNodeID != plan.TargetNodeID || preflight.NodeRevision != plan.TargetBaseRevision ||
		preflight.TargetRootID != plan.TargetRootID || preflight.RootLocatorDigest != plan.RootLocatorDigest ||
		preflight.PathDigest != plan.PathDigest || !validOpaqueRevision(preflight.TargetRevision) ||
		preflight.CapabilityRevision != plan.CapabilityRevision ||
		preflight.PolicyRevision != plan.SecurityPolicyRevision ||
		preflight.FindingSetDigest != plan.SecurityFindingSetDigest ||
		preflight.OperationSetDigest != plan.OperationSetDigest ||
		preflight.DeleteSetDigest != plan.DeleteSetDigest ||
		preflight.EstimatedItems != plan.EstimatedItems || preflight.EstimatedBytes != plan.EstimatedBytes ||
		preflight.EstimatedItems <= 0 || preflight.EstimatedBytes < 0 || preflight.EncryptedOperationRows == "" ||
		preflight.CreatedAt.IsZero() || preflight.CreatedAt.UTC().After(now) ||
		!preflight.ExpiresAt.UTC().Equal(plan.PreflightExpiresAt.UTC()) ||
		!now.Before(preflight.ExpiresAt.UTC()) || !preflight.ExpiresAt.UTC().After(preflight.CreatedAt.UTC()) {
		return ErrAuthorizationDenied
	}
	return nil
}

type persistedSecurityOverrideCandidate struct {
	BindingDigest string
	Categories    map[SecurityFindingCategory]struct{}
}

func (candidate persistedSecurityOverrideCandidate) allows(category SecurityFindingCategory) bool {
	if !category.known() {
		return false
	}
	_, allowed := candidate.Categories[category]
	return allowed
}

func loadPersistedSecurityOverrideCandidate(preflight model.BackupAssetRecoveryPreflight) (persistedSecurityOverrideCandidate, error) {
	if preflight.SecurityOverrideCandidateDigest == "" || preflight.SecurityOverrideCategories == "" ||
		!validDigest(preflight.SecurityOverrideCandidateDigest) {
		return persistedSecurityOverrideCandidate{}, ErrAuthorizationDenied
	}
	values := strings.Split(preflight.SecurityOverrideCategories, ",")
	if len(values) == 0 {
		return persistedSecurityOverrideCandidate{}, ErrAuthorizationDenied
	}
	candidate := persistedSecurityOverrideCandidate{
		BindingDigest: preflight.SecurityOverrideCandidateDigest,
		Categories:    make(map[SecurityFindingCategory]struct{}, len(values)),
	}
	previous := SecurityFindingCategory("")
	for _, value := range values {
		category := SecurityFindingCategory(value)
		if !category.known() || category <= previous {
			return persistedSecurityOverrideCandidate{}, ErrAuthorizationDenied
		}
		candidate.Categories[category] = struct{}{}
		previous = category
	}
	expected := framedDigest(
		securityOverrideCandidateDomain,
		preflight.FindingSetDigest,
		preflight.PolicyRevision,
		preflight.SecurityOverrideCategories,
	)
	if candidate.BindingDigest != expected {
		return persistedSecurityOverrideCandidate{}, ErrAuthorizationDenied
	}
	return candidate, nil
}

type executeOperationRow struct {
	operation             RecoveryOperation
	planItemID            *string
	sourceRecoveryPointID string
	sourceEntryID         string
}

type preparedExecuteItem struct {
	row model.BackupAssetRecoveryJobItem

	sourceRecoveryPointID string
	sourceEntryID         string
}

type preparedExecuteAggregate struct {
	planID                    string
	preflightID               string
	jobID                     string
	workspaceRelativeLocator  string
	workspaceBindingDigest    string
	encryptedWorkspaceLocator string
	items                     []preparedExecuteItem
	cleanupKey                backupasset.DomainKeyMaterial
}

type executeOperationSourceKey struct {
	recoveryPointID string
	entryID         string
}

func rebuildExecuteOperationRows(
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
	tx *gorm.DB,
) ([]executeOperationRow, error) {
	if tx == nil || preflight.EncryptedOperationRows == "" ||
		plan.OperationSetDigest != preflight.OperationSetDigest ||
		plan.DeleteSetDigest != preflight.DeleteSetDigest ||
		plan.EstimatedItems != preflight.EstimatedItems || plan.EstimatedBytes != preflight.EstimatedBytes ||
		plan.EstimatedItems <= 0 || plan.EstimatedItems > exactSelectionMaxItems || plan.EstimatedBytes < 0 {
		return nil, ErrRecoveryPreflightConflict
	}

	operations, err := decodeRecoveryOperationRows(preflight.EncryptedOperationRows)
	if err != nil {
		return nil, ErrRecoveryPreflightConflict
	}
	products, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetMode(plan.TargetMode), ConflictPolicy: ConflictPolicy(plan.ConflictPolicy),
		Operations: operations,
		Limits: RecoveryOperationLimits{
			MaxRows: exactSelectionMaxItems, MaxItems: exactSelectionMaxItems,
			MaxBytes: plan.EstimatedBytes, MaxImpactRows: exactSelectionMaxItems,
		},
	})
	if err != nil || products.OperationSetDigest != plan.OperationSetDigest ||
		products.DeleteSetDigest != plan.DeleteSetDigest ||
		products.Impact.EstimatedItems != plan.EstimatedItems ||
		products.Impact.EstimatedBytes != plan.EstimatedBytes {
		return nil, ErrRecoveryPreflightConflict
	}

	var planItems []model.BackupAssetRecoveryPlanItem
	if err := tx.Where("plan_id = ?", plan.ID).Order("ordinal ASC").Find(&planItems).Error; err != nil {
		return nil, err
	}
	if len(planItems) == 0 {
		return nil, ErrRecoveryPreflightConflict
	}
	planItemBySource := make(map[executeOperationSourceKey]string, len(planItems))
	for _, item := range planItems {
		ref := backupasset.AssetRef{RecoveryPointID: item.RecoveryPointID, EntryID: item.EntryID}
		key := executeOperationSourceKey{recoveryPointID: item.RecoveryPointID, entryID: item.EntryID}
		if !validOpaqueID(item.ID) || item.RecoveryPointID != plan.RecoveryPointID ||
			item.CatalogGenerationID != plan.CatalogGenerationID || backupasset.ValidateAssetRef(ref) != nil {
			return nil, ErrRecoveryPreflightConflict
		}
		if _, duplicate := planItemBySource[key]; duplicate {
			return nil, ErrRecoveryPreflightConflict
		}
		planItemBySource[key] = item.ID
	}

	rows := make([]executeOperationRow, len(products.Rows))
	usedPlanItems := make(map[string]struct{}, len(planItems))
	for index, operation := range products.Rows {
		rows[index].operation = cloneRecoveryOperation(operation)
		if operation.Kind == RecoveryOperationDelete {
			continue
		}
		if operation.Source.AssetRef == nil {
			return nil, ErrRecoveryPreflightConflict
		}
		key := executeOperationSourceKey{
			recoveryPointID: operation.Source.AssetRef.RecoveryPointID,
			entryID:         operation.Source.AssetRef.EntryID,
		}
		planItemID, found := planItemBySource[key]
		if !found {
			return nil, ErrRecoveryPreflightConflict
		}
		if _, duplicate := usedPlanItems[planItemID]; duplicate {
			return nil, ErrRecoveryPreflightConflict
		}
		usedPlanItems[planItemID] = struct{}{}
		rows[index].planItemID = authorizationStringPointer(planItemID)
		rows[index].sourceRecoveryPointID = operation.Source.AssetRef.RecoveryPointID
		rows[index].sourceEntryID = operation.Source.AssetRef.EntryID
	}
	if len(usedPlanItems) != len(planItems) {
		return nil, ErrRecoveryPreflightConflict
	}
	return rows, nil
}

func (service *AuthorizationService) prepareExecuteAggregate(
	ctx context.Context,
	normalized normalizedRecoveryAuthorization,
	now time.Time,
) (*preparedExecuteAggregate, error) {
	if service == nil || service.db == nil || service.locatorKeys == nil ||
		normalized.request.Operation != AuthorizationReceiptExecute || now.IsZero() {
		return nil, ErrAuthorizationDenied
	}
	if _, found, err := loadAuthorizationReplay(ctx, service.db, normalized, now); found || err != nil {
		if err != nil {
			return nil, err
		}
		return nil, ErrAuthorizationIdempotencyConflict
	}

	var plan model.BackupAssetRecoveryPlan
	loaded := service.db.WithContext(ctx).Where("id = ?", normalized.request.PlanID).Limit(1).Find(&plan)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	if loaded.RowsAffected != 1 || PlanState(plan.State) != PlanStateAuthorized ||
		plan.RequesterID != normalized.request.RequesterID || plan.TransitionRevision != normalized.request.ExpectedPlanRevision ||
		!validOpaqueID(normalized.request.PreflightID) || !validOpaqueID(normalized.request.GrantID) ||
		!now.Before(plan.PreflightExpiresAt.UTC()) {
		return nil, ErrAuthorizationDenied
	}
	var preflight model.BackupAssetRecoveryPreflight
	loaded = service.db.WithContext(ctx).Where("id = ? AND plan_id = ?", normalized.request.PreflightID, plan.ID).
		Limit(1).Find(&preflight)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	if loaded.RowsAffected != 1 || validateAuthorizationPreflightBindings(plan, preflight, now) != nil {
		return nil, ErrAuthorizationDenied
	}
	rows, err := rebuildExecuteOperationRows(plan, preflight, service.db.WithContext(ctx))
	if err != nil {
		return nil, ErrAuthorizationDenied
	}
	cleanupKey, err := service.locatorKeys.Active(ctx, backupasset.KeyDomainRecoveryCleanupOwnership)
	if err != nil || cleanupKey.Domain != backupasset.KeyDomainRecoveryCleanupOwnership ||
		cleanupKey.State != backupasset.DomainKeyActive || cleanupKey.Version <= 0 || len(cleanupKey.Key) != 32 {
		return nil, ErrAuthorizationUnavailable
	}
	jobID, err := backupasset.NewOpaqueID()
	if err != nil {
		return nil, err
	}
	prepared := &preparedExecuteAggregate{
		planID: plan.ID, preflightID: preflight.ID, jobID: jobID, cleanupKey: cloneDomainKeyMaterial(cleanupKey),
		items: make([]preparedExecuteItem, len(rows)),
	}
	if TargetMode(plan.TargetMode) == TargetModeIsolated {
		prepared.workspaceRelativeLocator = "jobs/" + jobID
		prepared.workspaceBindingDigest = recoveryWorkspaceBindingDigest(plan, jobID, prepared.workspaceRelativeLocator)
		prepared.encryptedWorkspaceLocator, err = secure.EncryptIfNeeded(prepared.workspaceRelativeLocator)
		if err != nil || !secure.IsEncrypted(prepared.encryptedWorkspaceLocator) {
			return nil, ErrAuthorizationUnavailable
		}
	}

	for index, operationRow := range rows {
		operation := operationRow.operation
		semanticDigest, digestErr := SemanticTargetDigest(
			TargetMode(plan.TargetMode), plan.TargetRootID, plan.RootLocatorDigest, operation.TargetRelativeLocator,
		)
		if digestErr != nil || semanticDigest != operation.SemanticTargetDigest {
			return nil, ErrAuthorizationDenied
		}
		finalLocator := operation.TargetRelativeLocator
		if prepared.workspaceRelativeLocator != "" {
			finalLocator = prepared.workspaceRelativeLocator + "/" + operation.TargetRelativeLocator
		}
		objectDigest, digestErr := TargetObjectDigest(plan.TargetRootID, plan.RootLocatorDigest, finalLocator)
		if digestErr != nil || objectDigest == semanticDigest {
			return nil, ErrAuthorizationDenied
		}
		itemID, idErr := backupasset.NewOpaqueID()
		if idErr != nil {
			return nil, idErr
		}
		binding := targetLocatorBindingForExecute(
			plan, jobID, itemID, operationRow, prepared.workspaceRelativeLocator,
			prepared.workspaceBindingDigest, objectDigest, cleanupKey.Version,
		)
		ciphertext, sealErr := SealTargetLocatorEnvelope(cleanupKey, binding, operation.TargetRelativeLocator)
		if sealErr != nil {
			return nil, ErrAuthorizationUnavailable
		}
		prepared.items[index] = preparedExecuteItem{
			row: model.BackupAssetRecoveryJobItem{
				ID: itemID, PlanID: plan.ID, JobID: jobID, PlanItemID: operationRow.planItemID,
				Ordinal: index, OperationKind: string(operation.Kind), TargetPathDigest: operation.TargetPathDigest,
				SemanticTargetDigest: semanticDigest, TargetObjectDigest: objectDigest,
				ExpectedPriorKind: string(operation.ExpectedPrior.Kind), ExpectedPriorDigest: operation.ExpectedPrior.Digest,
				ExpectedPostIdentityDigest: operation.ExpectedPostIdentityDigest,
				ExpectedPostBytes:          operation.ExpectedPostBytes, ExpectedPriorBytes: operation.ExpectedPriorBytes,
				EncryptedTargetRelativeLocator: ciphertext, TargetLocatorKeyVersion: cleanupKey.Version,
				TargetLocatorCipherVersion: targetLocatorCipherVersion,
				DisplayClass:               string(operation.DisplayClass), EstimatedBytes: operation.EstimatedBytes,
				CreatedAt: now, UpdatedAt: now,
			},
			sourceRecoveryPointID: operationRow.sourceRecoveryPointID,
			sourceEntryID:         operationRow.sourceEntryID,
		}
	}
	return prepared, nil
}

func targetLocatorBindingForExecute(
	plan model.BackupAssetRecoveryPlan,
	jobID,
	itemID string,
	row executeOperationRow,
	workspaceLocator,
	workspaceBindingDigest,
	targetObjectDigest string,
	keyVersion int,
) TargetLocatorEnvelopeBinding {
	planItemID := ""
	if row.planItemID != nil {
		planItemID = *row.planItemID
	}
	return TargetLocatorEnvelopeBinding{
		CodecVersion: targetLocatorEnvelopeSchemaVersion,
		JobID:        jobID, JobItemID: itemID, PlanDigest: plan.BindingDigest, PlanItemID: planItemID,
		SourceRecoveryPointID: row.sourceRecoveryPointID, SourceEntryID: row.sourceEntryID,
		TargetMode: TargetMode(plan.TargetMode), NodeID: plan.TargetNodeID, RootID: plan.TargetRootID,
		RootLocatorDigest: plan.RootLocatorDigest, SemanticTargetDigest: row.operation.SemanticTargetDigest,
		TargetObjectDigest: targetObjectDigest, Operation: row.operation.Kind,
		WorkspaceBindingDigest: workspaceBindingDigest, WorkspaceRelativeLocator: workspaceLocator,
		ExpectedPriorKind: row.operation.ExpectedPrior.Kind, ExpectedPriorDigest: row.operation.ExpectedPrior.Digest,
		ExpectedPostIdentityDigest: row.operation.ExpectedPostIdentityDigest,
		ExpectedPostBytes:          row.operation.ExpectedPostBytes, ExpectedPriorBytes: row.operation.ExpectedPriorBytes,
		TargetLocatorKeyVersion: keyVersion, TargetLocatorCipherVersion: targetLocatorCipherVersion,
	}
}

func recoveryWorkspaceBindingDigest(
	plan model.BackupAssetRecoveryPlan,
	jobID,
	workspaceLocator string,
) string {
	return framedDigest(
		"xirang/recovery/workspace-binding/v1", jobID, plan.ID, plan.BindingDigest,
		string(TargetModeIsolated), strconv.FormatUint(uint64(plan.TargetNodeID), 10),
		plan.TargetRootID, plan.RootLocatorDigest, workspaceLocator,
	)
}

func cloneDomainKeyMaterial(material backupasset.DomainKeyMaterial) backupasset.DomainKeyMaterial {
	clone := material
	clone.Key = append([]byte(nil), material.Key...)
	return clone
}

func publicPreparedExecuteError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrAuthorizationDenied) || errors.Is(err, ErrAuthorizationIdempotencyConflict) {
		return err
	}
	return ErrAuthorizationUnavailable
}

func (service *AuthorizationService) persistDeleteAuthorizationTx(
	ctx context.Context,
	tx *gorm.DB,
	normalized normalizedRecoveryAuthorization,
	plan *model.BackupAssetRecoveryPlan,
	now time.Time,
	authority observedAuthorizationAuthority,
) (RecoveryAuthorizationResult, error) {
	if PlanState(plan.State) != PlanStateExecuted || plan.TargetMode != string(TargetModeInPlace) ||
		!validOpaqueID(normalized.request.JobID) || !validOpaqueID(normalized.request.CheckpointID) ||
		!validOpaqueID(normalized.request.AttemptID) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	var job model.BackupAssetRecoveryJob
	loaded := tx.WithContext(ctx).Where("id = ? AND plan_id = ?", normalized.request.JobID, plan.ID).
		Limit(1).Find(&job)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || job.State != "running" || job.TargetMode != string(TargetModeInPlace) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	var checkpoint model.BackupAssetRecoveryCheckpoint
	loaded = tx.WithContext(ctx).Where("id = ? AND job_id = ? AND attempt_id = ?",
		normalized.request.CheckpointID, job.ID, normalized.request.AttemptID).Limit(1).Find(&checkpoint)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || checkpoint.Phase != "delete_authority_required" ||
		checkpoint.DeleteAuthorityExpiresAt == nil || !now.Before(checkpoint.DeleteAuthorityExpiresAt.UTC()) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	var nodeLeases []model.BackupAssetRecoveryNodeLease
	loaded = tx.WithContext(ctx).Where("job_id = ? AND attempt_id = ? AND state = ?",
		job.ID, normalized.request.AttemptID, "active").Limit(2).Find(&nodeLeases)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	if len(nodeLeases) != 1 || nodeLeases[0].Fence != checkpoint.NodeFence {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	nodeLease := nodeLeases[0]
	var preflight model.BackupAssetRecoveryPreflight
	loaded = tx.WithContext(ctx).Where("id = ? AND plan_id = ?", job.PreflightID, plan.ID).
		Limit(1).Find(&preflight)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || validateAuthorizationPreflightBindings(*plan, preflight, now) != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := service.sourceValidator.RevalidatePlanTx(ctx, tx, *plan); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	if err := service.revalidateAuthorizationAuthorityTx(
		ctx, tx, normalized.request.Operation, *plan, preflight, authority,
	); err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	var attempt model.BackupAssetRecoveryAttempt
	loaded = tx.WithContext(ctx).Where("id = ? AND job_id = ?", normalized.request.AttemptID, job.ID).
		Limit(1).Find(&attempt)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	var sourceLeases []model.RecoveryPointLease
	loaded = tx.WithContext(ctx).Where("holder_type = ? AND owner_id = ? AND attempt_id = ? AND status = ?",
		backupasset.LeaseHolderRecoveryJob, job.ID, attempt.ID, backupasset.LeaseActive).Limit(2).Find(&sourceLeases)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	if attempt.ID == "" || attempt.State != string(AttemptStateRunning) || !attempt.MutationArmed ||
		attempt.LeaseExpiresAt == nil || !attempt.LeaseExpiresAt.UTC().After(now) ||
		nodeLease.AttemptID == nil || *nodeLease.AttemptID != attempt.ID ||
		nodeLease.OwnerID != attempt.OwnerID || nodeLease.Fence != attempt.Fence ||
		!nodeLease.LeaseExpiresAt.UTC().After(now) || len(sourceLeases) != 1 ||
		!sourceLeases[0].LeaseExpiresAt.UTC().After(now) {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	claim := RecoveryWorkerClaim{
		JobID: job.ID, AttemptID: attempt.ID, NodeLeaseID: nodeLease.ID, WorkerID: attempt.OwnerID,
		AttemptFence: attempt.Fence, NodeFence: nodeLease.Fence, TransitionRevision: job.TransitionRevision,
		LeaseExpiresAt: attempt.LeaseExpiresAt.UTC(), AbsoluteDeadline: sourceLeases[0].AbsoluteDeadline.UTC(),
		SourceFence: recoverySourceFence(sourceLeases[0]),
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	loaded = tx.WithContext(ctx).Where("job_id = ?", job.ID).Order("sequence ASC").Find(&checkpoints)
	if loaded.Error != nil {
		return RecoveryAuthorizationResult{}, loaded.Error
	}
	operations, err := loadInPlaceOrdinaryCheckpointOperationsTx(ctx, tx, *plan, preflight, job)
	if err != nil {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	required, hasRequired, _, err := validateInPlaceOrdinaryCheckpointHistory(
		*plan, job, claim, checkpoints, operations, now,
	)
	if err != nil || !hasRequired || len(checkpoints) == 0 ||
		required.ID != checkpoint.ID || required.AttemptID != attempt.ID ||
		required.AttemptFence != claim.AttemptFence || required.NodeFence != claim.NodeFence ||
		checkpoints[len(checkpoints)-1].ID != required.ID {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	grantExpiresAt := normalized.grantExpiresAt
	if checkpoint.DeleteAuthorityExpiresAt.UTC().Before(grantExpiresAt) {
		grantExpiresAt = checkpoint.DeleteAuthorityExpiresAt.UTC()
	}
	grantID, err := backupasset.NewOpaqueID()
	if err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	bindingDigest := framedDigest(recoveryAuthorizationGrantBindingDomain,
		string(AuthorizationReceiptCategoryExactMirrorDelete), plan.ID, job.ID, checkpoint.ID,
		normalized.grantSecretHash, grantExpiresAt.Format(time.RFC3339Nano))
	grant := model.BackupAssetRecoveryGrant{
		ID: grantID, PlanID: plan.ID, JobID: authorizationStringPointer(job.ID),
		AuthorityCategory: string(AuthorityExactMirrorDelete), GrantHash: normalized.grantSecretHash,
		ActorUserID: normalized.request.RequesterID, ActorSessionID: normalized.sessionDigest,
		BindingDigest: bindingDigest, EncryptedReason: normalized.request.Reason,
		DeleteCheckpointID: authorizationStringPointer(checkpoint.ID), DeleteSetDigest: job.DeleteSetDigest,
		DeleteTargetRevision: job.TargetChainRevision, DeleteAttemptID: authorizationStringPointer(normalized.request.AttemptID),
		DeleteAttemptFence: checkpoint.AttemptFence, DeleteNodeFence: checkpoint.NodeFence,
		ExpiresAt: grantExpiresAt, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&grant).Error; err != nil {
		return RecoveryAuthorizationResult{}, err
	}
	validatedGrant, err := validatePendingOrdinaryDeleteGrantTx(
		ctx, tx, *plan, job, claim, required, normalized.request.GrantSecret, now,
	)
	if err != nil || validatedGrant.ID != grant.ID || validatedGrant.BindingDigest != grant.BindingDigest {
		return RecoveryAuthorizationResult{}, ErrAuthorizationDenied
	}
	return RecoveryAuthorizationResult{
		PlanID: plan.ID, GrantID: grantID, JobID: job.ID, AttemptID: normalized.request.AttemptID,
		PlanTransitionRevision: plan.TransitionRevision,
	}, nil
}

func authorizationReceiptRow(
	normalized normalizedRecoveryAuthorization,
	result RecoveryAuthorizationResult,
	receiptID string,
	now time.Time,
) model.BackupAssetRecoveryEvidence {
	proofExpiry := normalized.request.Proof.ExpiresAt.UTC()
	sessionExpiry := normalized.request.Session.ExpiresAt.UTC()
	replayExpiry := normalized.replayExpiresAt.UTC()
	receipt := model.BackupAssetRecoveryEvidence{
		ID: receiptID, Kind: "authorization_receipt", PlanID: authorizationStringPointer(result.PlanID),
		RequesterID: authorizationUintPointer(normalized.request.RequesterID),
		Operation:   string(normalized.request.Operation), Category: string(normalized.request.Category),
		Endpoint: normalized.request.Endpoint, IdempotencyKeyDigest: normalized.keyDigest,
		IntentDigest: normalized.intentDigest, StepUpJTIDigest: normalized.proofDigest,
		PresentingSessionDigest:       normalized.sessionDigest,
		PresentingSessionUserID:       authorizationUintPointer(normalized.request.Session.UserID),
		PresentingSessionRole:         normalized.request.Session.Role,
		PresentingSessionTokenVersion: normalized.request.Session.TokenVersion,
		ProofExpiresAt:                &proofExpiry, PresentingSessionExpiresAt: &sessionExpiry, ReplayExpiresAt: &replayExpiry,
		ExpectedPlanTransitionRevision: normalized.request.ExpectedPlanRevision,
		ResultPlanTransitionRevision:   result.PlanTransitionRevision,
		NodeLeaseFence:                 result.NodeLeaseFence, CreatedAt: now, UpdatedAt: now,
	}
	receipt.JobID = authorizationStringPointer(result.JobID)
	receipt.GrantID = authorizationStringPointer(result.GrantID)
	receipt.AttemptID = authorizationStringPointer(result.AttemptID)
	receipt.SourceLeaseID = authorizationStringPointer(result.SourceLeaseID)
	receipt.NodeLeaseID = authorizationStringPointer(result.NodeLeaseID)
	if normalized.request.Operation == AuthorizationReceiptDeleteAuthorize {
		receipt.CheckpointID = authorizationStringPointer(normalized.request.CheckpointID)
	}
	if result.SourceLeaseID != "" {
		receipt.SourceLeaseBindingDigest = framedDigest(recoveryAuthorizationSourceLeaseDomain,
			result.PlanID, result.JobID, result.AttemptID, result.SourceLeaseID)
	}
	if normalized.request.Operation == AuthorizationReceiptExecute {
		receipt.IntentDigest = executeAuthorizationReplayIntentDigest(normalized, result)
	}
	return receipt
}

func authorizationStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func authorizationUintPointer(value uint) *uint {
	if value == 0 {
		return nil
	}
	copy := value
	return &copy
}

func timePointerValue(value time.Time) *time.Time {
	copy := value.UTC()
	return &copy
}

func authorizationProofDigestExists(ctx context.Context, db *gorm.DB, digest string) (bool, error) {
	var count int64
	err := db.WithContext(ctx).Model(&model.BackupAssetRecoveryEvidence{}).
		Where("kind = ? AND step_up_jti_digest = ?", "authorization_receipt", digest).Count(&count).Error
	return count != 0, err
}

func authorizationReceiptKeyExists(
	ctx context.Context,
	db *gorm.DB,
	normalized normalizedRecoveryAuthorization,
) (bool, error) {
	var receipt struct {
		ID string `gorm:"column:id"`
	}
	result := db.WithContext(ctx).Table((model.BackupAssetRecoveryEvidence{}).TableName()).
		Select("id").Where(
		"kind = ? AND requester_id = ? AND endpoint = ? AND idempotency_key_digest = ?",
		"authorization_receipt", normalized.request.RequesterID, normalized.request.Endpoint, normalized.keyDigest,
	).Limit(1).Find(&receipt)
	return result.RowsAffected == 1, result.Error
}

func (service *AuthorizationService) ReapAuthorizationReceipts(ctx context.Context, limit int) (int, error) {
	if service == nil || service.db == nil || limit <= 0 || limit > 1000 {
		return 0, ErrAuthorizationUnavailable
	}
	return reapAuthorizationReceipts(ctx, service.db, limit)
}

// AuthorizationReceiptReaper is the maintenance-only database boundary used
// when Recovery admission is disabled and the full authorization graph is not
// constructed.
type AuthorizationReceiptReaper struct {
	db *gorm.DB
}

func NewAuthorizationReceiptReaper(db *gorm.DB) (*AuthorizationReceiptReaper, error) {
	if db == nil {
		return nil, ErrAuthorizationUnavailable
	}
	return &AuthorizationReceiptReaper{db: db}, nil
}

func (reaper *AuthorizationReceiptReaper) ReapAuthorizationReceipts(
	ctx context.Context,
	limit int,
) (int, error) {
	if reaper == nil || reaper.db == nil || limit <= 0 || limit > 1000 {
		return 0, ErrAuthorizationUnavailable
	}
	return reapAuthorizationReceipts(ctx, reaper.db, limit)
}

func reapAuthorizationReceipts(ctx context.Context, db *gorm.DB, limit int) (int, error) {
	ctx = sourceValidationContext(ctx)
	removed := 0
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Keep candidate selection and deletion in one statement/transaction. The
		// predicate is deliberately stricter than the row CHECKs so a malformed
		// historical receipt cannot be selected ahead of an eligible row.
		candidates := tx.Table("backup_asset_recovery_evidence AS e").
			Select("e.id").
			Where(authorizationReceiptReapEligibility).
			Order("e.replay_expires_at ASC, e.id ASC").
			Limit(limit)
		result := tx.Where("id IN (?)", candidates).Delete(&model.BackupAssetRecoveryEvidence{})
		if result.Error != nil {
			return result.Error
		}
		removed = int(result.RowsAffected)
		return nil
	})
	if err != nil {
		return 0, ErrAuthorizationUnavailable
	}
	return removed, nil
}

// authorizationReceiptReapEligibility is kept as one SQL predicate so the
// bounded candidate read and the final delete share exactly the same guards.
// The operation-specific EXISTS clauses preserve the private effect linkage
// until its applicable grant deadline has also elapsed.
const authorizationReceiptReapEligibility = `
	e.kind = 'authorization_receipt'
	AND e.proof_expires_at IS NOT NULL
	AND e.replay_expires_at IS NOT NULL
	AND e.presenting_session_expires_at IS NOT NULL
	AND e.proof_expires_at <= CURRENT_TIMESTAMP
	AND e.replay_expires_at <= CURRENT_TIMESTAMP
	AND EXISTS (
		SELECT 1
		FROM backup_asset_recovery_plans AS plan_row
		WHERE plan_row.id = e.plan_id AND plan_row.requester_id = e.requester_id
	)
	AND (
		(e.operation = 'security_override' AND e.category = 'security_override')
		OR (
			e.operation = 'write_authorize'
			AND e.category = 'write'
			AND EXISTS (
				SELECT 1
				FROM backup_asset_recovery_grants AS grant_row
				WHERE grant_row.id = e.grant_id
					AND grant_row.plan_id = e.plan_id
					AND grant_row.authority_category = 'write'
					AND grant_row.job_id IS NULL
					AND grant_row.binding_digest = e.grant_binding_digest
					AND grant_row.expires_at <= CURRENT_TIMESTAMP
			)
		)
		OR (
			e.operation = 'exact_mirror_delete_authorize'
			AND e.category = 'exact_mirror_delete'
			AND EXISTS (
				SELECT 1
				FROM backup_asset_recovery_grants AS grant_row
				JOIN backup_asset_recovery_jobs AS job_row
					ON job_row.id = e.job_id AND job_row.plan_id = e.plan_id
				JOIN backup_asset_recovery_checkpoints AS checkpoint_row
					ON checkpoint_row.id = e.checkpoint_id
					AND checkpoint_row.job_id = e.job_id
					AND checkpoint_row.attempt_id = e.attempt_id
				JOIN backup_asset_recovery_attempts AS attempt_row
					ON attempt_row.id = e.attempt_id AND attempt_row.job_id = e.job_id
				WHERE grant_row.id = e.grant_id
					AND grant_row.plan_id = e.plan_id
					AND grant_row.authority_category = 'exact_mirror_delete'
					AND grant_row.job_id = e.job_id
					AND grant_row.delete_checkpoint_id = e.checkpoint_id
					AND grant_row.delete_attempt_id = e.attempt_id
					AND grant_row.binding_digest = e.grant_binding_digest
					AND grant_row.expires_at <= CURRENT_TIMESTAMP
			)
		)
		OR (
			e.operation = 'execute'
			AND e.category = 'execute'
			AND EXISTS (
				SELECT 1
				FROM backup_asset_recovery_grants AS grant_row
				JOIN backup_asset_recovery_jobs AS job_row
					ON job_row.id = e.job_id
					AND job_row.plan_id = e.plan_id
					AND job_row.authority_grant_id = grant_row.id
				WHERE grant_row.id = e.grant_id
					AND grant_row.plan_id = e.plan_id
					AND grant_row.authority_category = 'write'
					AND grant_row.job_id IS NULL
					AND grant_row.binding_digest = e.grant_binding_digest
					AND grant_row.consumed_at IS NOT NULL
					AND grant_row.expires_at <= CURRENT_TIMESTAMP
			)
			AND EXISTS (
				SELECT 1
				FROM backup_asset_recovery_attempts AS attempt_row
				WHERE attempt_row.id = e.attempt_id AND attempt_row.job_id = e.job_id
			)
			AND EXISTS (
				SELECT 1
				FROM backup_asset_recovery_node_leases AS node_lease
				WHERE node_lease.id = e.node_lease_id
					AND node_lease.job_id = e.job_id
					AND node_lease.attempt_id = e.attempt_id
					AND node_lease.fence = e.node_lease_fence
			)
			AND EXISTS (
				SELECT 1
				FROM recovery_point_leases AS source_lease
				JOIN backup_asset_recovery_plans AS source_plan
					ON source_plan.id = e.plan_id
					AND source_plan.recovery_point_id = source_lease.recovery_point_id
				WHERE source_lease.id = e.source_lease_id
					AND source_lease.holder_type = 'recovery_job'
					AND source_lease.owner_id = e.job_id
					AND source_lease.attempt_id = e.attempt_id
			)
			AND (
				SELECT COUNT(*)
				FROM recovery_point_leases AS source_lease_count
				WHERE source_lease_count.holder_type = 'recovery_job'
					AND source_lease_count.owner_id = e.job_id
					AND source_lease_count.attempt_id = e.attempt_id
			) = 1
		)
	)`

func (service *AuthorizationService) writeAuthorizationAudit(
	ctx context.Context,
	request RecoveryAuthorizationRequest,
	result RecoveryAuthorizationResult,
) {
	if service.auditWriter == nil {
		return
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), authorizationAuditTimeout)
	defer cancel()
	action := backupasset.AuditActionRecoveryAuthorize
	if request.Operation == AuthorizationReceiptExecute {
		action = backupasset.AuditActionRecoveryExecute
	}
	_, _ = service.auditWriter.Write(auditCtx, backupasset.AuditEventInput{
		Actor:  backupasset.AuditActor{UserID: request.RequesterID, Role: request.Session.Role},
		Action: action, Outcome: backupasset.AuditOutcomeSuccess,
		RecoveryJobID: result.JobID,
		Fields:        map[backupasset.AuditField]any{backupasset.AuditFieldOperation: string(request.Operation)},
	})
}

const (
	recoveryReconciliationRootLimit       = 1024
	recoveryReconciliationExpectedLimit   = 4096
	recoveryReconciliationPageLimit       = 256
	recoveryReconciliationChainLimit      = 4096
	recoveryReconciliationFindingLimit    = 256
	recoveryReconciliationCursorMax       = 2048
	recoveryReconciliationPermitTTL       = 2 * time.Minute
	recoveryReconciliationComponentDomain = "xirang/recovery/reconcile-component-token/v1"
	recoveryReconciliationExpectedDomain  = "xirang/recovery/reconcile-expected-set/v1"

	recoveryReconciliationRemoteFinal         = "final"
	recoveryReconciliationRemoteDeleteStarted = "delete_started"
	recoveryReconciliationRemoteAbsent        = "absent"
)

var (
	ErrInvalidRecoveryReconciliation     = errors.New("invalid recovery reconciliation")
	ErrRecoveryReconciliationUnavailable = errors.New("recovery reconciliation unavailable")
	errRecoveryReconciliationIncomplete  = errors.New("recovery reconciliation expected set incomplete")
)

type RecoveryReconciliationAlert struct {
	NodeID   uint
	RootID   string
	State    RecoveryReconciliationState
	Counts   RecoveryReconciliationCounts
	Findings []RecoveryReconciliationFinding
}

func (RecoveryReconciliationAlert) String() string {
	return redactedRecoveryTargetProduct("RecoveryReconciliationAlert")
}

func (RecoveryReconciliationAlert) GoString() string {
	return redactedRecoveryTargetProduct("RecoveryReconciliationAlert")
}

type RecoveryReconciliationFindingSink interface {
	NotifyRecoveryReconciliation(
		context.Context,
		RecoveryReconciliationAlert,
	) error
}

type RecoveryReconciliationKeySource interface {
	Active(
		context.Context,
		backupasset.KeyDomain,
	) (backupasset.DomainKeyMaterial, error)
	ByVersion(
		context.Context,
		backupasset.KeyDomain,
		int,
	) (backupasset.DomainKeyMaterial, error)
}

type RecoveryReconciliationRootRegistry interface {
	ListAllRecoveryTargetRoots(
		context.Context,
	) ([]settings.RecoveryTargetRootReference, error)
	ResolveRecoveryTargetRootTx(
		context.Context,
		*gorm.DB,
		uint,
		string,
	) (settings.RecoveryTargetRootResolution, error)
}

type RecoveryReconciliationRevisionSnapshot struct {
	NodeRevision       string `json:"-"`
	CredentialRevision string `json:"-"`
	RootRevision       string `json:"-"`
}

func (RecoveryReconciliationRevisionSnapshot) String() string {
	return redactedRecoveryTargetProduct("RecoveryReconciliationRevisionSnapshot")
}

func (RecoveryReconciliationRevisionSnapshot) GoString() string {
	return redactedRecoveryTargetProduct("RecoveryReconciliationRevisionSnapshot")
}

func (snapshot RecoveryReconciliationRevisionSnapshot) valid() bool {
	return validOpaqueRevision(snapshot.NodeRevision) &&
		validOpaqueRevision(snapshot.CredentialRevision) &&
		validOpaqueRevision(snapshot.RootRevision)
}

type RecoveryReconciliationRevisionSource interface {
	ResolveRecoveryReconciliationRevisionsTx(
		context.Context,
		*gorm.DB,
		uint,
		string,
	) (RecoveryReconciliationRevisionSnapshot, error)
}

type RecoveryReconciliationServiceDependencies struct {
	DB        *gorm.DB
	Now       func() time.Time
	Roots     RecoveryReconciliationRootRegistry
	Revisions RecoveryReconciliationRevisionSource
	Keys      RecoveryReconciliationKeySource
	Target    TargetReconciliationPort
	Audit     RecoveryAuthorizationAuditWriter
	Findings  RecoveryReconciliationFindingSink
	Policy    ReconciliationPolicy
}

// ReconciliationPolicy owns the configurable finding bound while the target
// retains the immutable protocol hard cap.
type ReconciliationPolicy struct {
	FindingLimit int
}

func (policy ReconciliationPolicy) valid() bool {
	return policy.FindingLimit > 0 && policy.FindingLimit <= recoveryReconciliationFindingLimit
}

type RecoveryReconciliationService struct {
	db        *gorm.DB
	now       func() time.Time
	roots     RecoveryReconciliationRootRegistry
	revisions RecoveryReconciliationRevisionSource
	keys      RecoveryReconciliationKeySource
	target    TargetReconciliationPort
	audit     RecoveryAuthorizationAuditWriter
	findings  RecoveryReconciliationFindingSink
	policy    ReconciliationPolicy
}

type recoveryReconciliationExpectedSnapshot struct {
	root       settings.RecoveryTargetRootResolution
	session    recoveryTargetReconciliationSessionBinding
	candidates []recoveryReconciliationExpectedCandidate
}

type recoveryReconciliationExpectedCandidate struct {
	component string
	expected  targetReconciliationExpected
}

type recoveryReconciliationResultRelationship struct {
	JobID       string
	ResultSetID string
	ResultCount int64
}

func NewRecoveryReconciliationService(
	dependencies RecoveryReconciliationServiceDependencies,
) (*RecoveryReconciliationService, error) {
	if dependencies.DB == nil || dependencies.Now == nil || dependencies.Roots == nil || dependencies.Revisions == nil ||
		dependencies.Keys == nil || dependencies.Target == nil || dependencies.Audit == nil ||
		dependencies.Findings == nil || !dependencies.Policy.valid() {
		return nil, ErrInvalidRecoveryReconciliation
	}
	return &RecoveryReconciliationService{
		db: dependencies.DB, now: dependencies.Now, roots: dependencies.Roots, revisions: dependencies.Revisions,
		keys: dependencies.Keys, target: dependencies.Target, audit: dependencies.Audit,
		findings: dependencies.Findings, policy: dependencies.Policy,
	}, nil
}

func (service *RecoveryReconciliationService) ReconcileRoot(
	ctx context.Context,
	request ReconcileRecoveryRootRequest,
) (RecoveryReconciliationResult, error) {
	return service.reconcileRoot(ctx, request, "")
}

func (service *RecoveryReconciliationService) reconcileRoot(
	ctx context.Context,
	request ReconcileRecoveryRootRequest,
	admissionGeneration string,
) (RecoveryReconciliationResult, error) {
	if ctx == nil {
		return RecoveryReconciliationResult{}, ErrInvalidRecoveryReconciliation
	}
	if err := ctx.Err(); err != nil {
		return RecoveryReconciliationResult{}, err
	}
	if service == nil || service.db == nil || service.now == nil || service.roots == nil || service.revisions == nil ||
		service.keys == nil || service.target == nil || service.audit == nil || service.findings == nil || request.NodeID == 0 ||
		!validBoundedOpaque(request.RootID, targetRootIDMax) || len(request.Cursor) > recoveryReconciliationCursorMax ||
		(admissionGeneration != "" && !validOpaqueRevision(admissionGeneration)) {
		return RecoveryReconciliationResult{}, ErrInvalidRecoveryReconciliation
	}
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryReconciliationResult{}, ErrRecoveryReconciliationUnavailable
	}

	snapshot, err := service.buildRecoveryReconciliationExpectedSnapshot(ctx, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoveryReconciliationResult{}, ctxErr
		}
		if errors.Is(err, errRecoveryReconciliationIncomplete) {
			material, keyErr := service.recoveryReconciliationAuditKey(ctx, request.Cursor)
			if keyErr != nil {
				return RecoveryReconciliationResult{}, keyErr
			}
			defer clear(material.Key)
			return service.publishRecoveryReconciliationResult(
				ctx, request, recoveryReconciliationIncompleteResult(material, request),
			)
		}
		return RecoveryReconciliationResult{}, ErrRecoveryReconciliationUnavailable
	}

	material, err := service.recoveryReconciliationAuditKey(ctx, request.Cursor)
	if err != nil {
		return RecoveryReconciliationResult{}, err
	}
	defer clear(material.Key)
	var auditTokenKey [sha256.Size]byte
	copy(auditTokenKey[:], material.Key)
	expected := make([]targetReconciliationExpected, 0, len(snapshot.candidates))
	for index := range snapshot.candidates {
		candidate := snapshot.candidates[index]
		candidate.expected.componentToken = recoveryReconciliationComponentToken(
			auditTokenKey, material.Version, snapshot.session, candidate.component, candidate.expected,
		)
		expected = append(expected, candidate.expected)
		snapshot.candidates[index].component = ""
	}
	snapshot.candidates = nil
	sort.Slice(expected, func(left, right int) bool {
		if expected[left].jobID != expected[right].jobID {
			return expected[left].jobID < expected[right].jobID
		}
		return expected[left].componentToken < expected[right].componentToken
	})
	expectedDigest := recoveryReconciliationExpectedSetDigest(
		material.Version, snapshot.session, expected,
	)
	permit := TargetReconciliationPermit{
		SchemaVersion: 1, Purpose: TargetPurposeReconcile, Operation: TargetReconciliationScanRoot,
		NodeID: request.NodeID, RootID: request.RootID, RootLocatorDigest: snapshot.root.LocatorDigest,
		RootRevision: snapshot.session.rootRevision, ExpectedSetDigest: expectedDigest,
		PageLimit: recoveryReconciliationPageLimit, ChainLimit: recoveryReconciliationChainLimit,
		FindingLimit: service.policy.FindingLimit, Cursor: request.Cursor,
		AdmissionGeneration: admissionGeneration, ExpiresAt: now.Add(recoveryReconciliationPermitTTL),
	}
	proof := &targetReconciliationPermitProof{
		sessionBinding: snapshot.session, auditKeyVersion: material.Version,
		auditTokenKey: auditTokenKey,
		expected:      append([]targetReconciliationExpected(nil), expected...),
	}
	permit.proof = proof
	proof.bindingDigest = targetReconciliationPermitBindingDigest(
		auditTokenKey, material.Version, permit, snapshot.session.bindingDigest,
	)
	clear(auditTokenKey[:])

	page, err := service.target.ScanRecoveryRoot(
		ctx, permit, TargetReconciliationRequest{RootID: request.RootID},
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoveryReconciliationResult{}, ctxErr
		}
		return RecoveryReconciliationResult{}, ErrRecoveryReconciliationUnavailable
	}
	return service.publishRecoveryReconciliationResult(
		ctx, request, recoveryReconciliationResultFromPage(page),
	)
}

func (service *RecoveryReconciliationService) publishRecoveryReconciliationResult(
	ctx context.Context,
	request ReconcileRecoveryRootRequest,
	result RecoveryReconciliationResult,
) (RecoveryReconciliationResult, error) {
	itemCount := int64(result.Counts.Scanned)
	if itemCount < 0 {
		itemCount = 0
	}
	if itemCount > recoveryReconciliationChainLimit {
		itemCount = recoveryReconciliationChainLimit
	}
	outcome := backupasset.AuditOutcomeBlocked
	if result.State == RecoveryReconciliationClear {
		outcome = backupasset.AuditOutcomeSuccess
	}
	_, auditErr := service.audit.Write(ctx, backupasset.AuditEventInput{
		Action:    backupasset.AuditActionRecoveryCleanup,
		Outcome:   outcome,
		ItemCount: itemCount,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldOperation: "recovery_reconcile",
			backupasset.AuditFieldStatus:    string(result.State),
		},
	})
	alertErr := service.findings.NotifyRecoveryReconciliation(ctx, RecoveryReconciliationAlert{
		NodeID: request.NodeID, RootID: request.RootID, State: result.State, Counts: result.Counts,
		Findings: append([]RecoveryReconciliationFinding(nil), result.Findings...),
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return RecoveryReconciliationResult{}, ctxErr
	}
	if auditErr != nil || alertErr != nil {
		return RecoveryReconciliationResult{}, ErrRecoveryReconciliationUnavailable
	}
	return result, nil
}

func (service *RecoveryReconciliationService) activeRecoveryReconciliationAuditKey(
	ctx context.Context,
) (backupasset.DomainKeyMaterial, error) {
	material, err := service.keys.Active(ctx, backupasset.KeyDomainAuditFingerprint)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return backupasset.DomainKeyMaterial{}, ctxErr
		}
		return backupasset.DomainKeyMaterial{}, ErrRecoveryReconciliationUnavailable
	}
	if material.Domain != backupasset.KeyDomainAuditFingerprint || material.Version <= 0 ||
		material.Version > math.MaxUint32 ||
		material.State != backupasset.DomainKeyActive || len(material.Key) != sha256.Size {
		clear(material.Key)
		return backupasset.DomainKeyMaterial{}, ErrRecoveryReconciliationUnavailable
	}
	return material, nil
}

func (service *RecoveryReconciliationService) recoveryReconciliationAuditKey(
	ctx context.Context,
	cursor string,
) (backupasset.DomainKeyMaterial, error) {
	if cursor == "" {
		return service.activeRecoveryReconciliationAuditKey(ctx)
	}
	version, ok := recoveryReconciliationCursorAuditKeyVersion(cursor)
	if !ok {
		return backupasset.DomainKeyMaterial{}, ErrRecoveryReconciliationUnavailable
	}
	material, err := service.keys.ByVersion(ctx, backupasset.KeyDomainAuditFingerprint, version)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return backupasset.DomainKeyMaterial{}, ctxErr
		}
		return backupasset.DomainKeyMaterial{}, ErrRecoveryReconciliationUnavailable
	}
	if material.Domain != backupasset.KeyDomainAuditFingerprint || material.Version != version ||
		(material.State != backupasset.DomainKeyActive && material.State != backupasset.DomainKeyVerifyOnly) ||
		len(material.Key) != sha256.Size {
		clear(material.Key)
		return backupasset.DomainKeyMaterial{}, ErrRecoveryReconciliationUnavailable
	}
	return material, nil
}

func (service *RecoveryReconciliationService) listRecoveryReconciliationRoots(
	ctx context.Context,
) ([]settings.RecoveryTargetRootReference, error) {
	if ctx == nil {
		return nil, ErrRecoveryReconciliationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if service == nil || service.roots == nil {
		return nil, ErrRecoveryReconciliationUnavailable
	}
	roots, err := service.roots.ListAllRecoveryTargetRoots(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, ErrRecoveryReconciliationUnavailable
	}
	if len(roots) > recoveryReconciliationRootLimit {
		return nil, ErrRecoveryReconciliationUnavailable
	}
	return roots, nil
}

func (service *RecoveryReconciliationService) ReconcileDowngradeReadiness(
	ctx context.Context,
	request RecoveryDowngradeReconciliationRequest,
) (RecoveryReconciliationResult, error) {
	if ctx == nil {
		return RecoveryReconciliationResult{}, ErrInvalidRecoveryReconciliation
	}
	if err := ctx.Err(); err != nil {
		return RecoveryReconciliationResult{}, err
	}
	if service == nil || !validOpaqueRevision(request.AdmissionGeneration) {
		return RecoveryReconciliationResult{}, ErrInvalidRecoveryReconciliation
	}
	roots, err := service.listRecoveryReconciliationRoots(ctx)
	if err != nil {
		return RecoveryReconciliationResult{}, err
	}
	if len(roots) == 0 {
		return RecoveryReconciliationResult{}, ErrRecoveryReconciliationUnavailable
	}
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if root.NodeID == 0 || !validBoundedOpaque(root.RootID, targetRootIDMax) {
			return RecoveryReconciliationResult{}, ErrRecoveryReconciliationUnavailable
		}
		key := strconv.FormatUint(uint64(root.NodeID), 10) + "/" + root.RootID
		if _, duplicate := seen[key]; duplicate {
			return RecoveryReconciliationResult{}, ErrRecoveryReconciliationUnavailable
		}
		seen[key] = struct{}{}
	}

	aggregate := RecoveryReconciliationResult{
		State: RecoveryReconciliationClear, Complete: true,
	}
	const maximumPagesPerRoot = recoveryReconciliationChainLimit / recoveryReconciliationPageLimit
	for _, root := range roots {
		cursor := ""
		rootClear := false
		for pageNumber := 0; pageNumber < maximumPagesPerRoot; pageNumber++ {
			result, reconcileErr := service.reconcileRoot(ctx, ReconcileRecoveryRootRequest{
				NodeID: root.NodeID, RootID: root.RootID, Cursor: cursor,
			}, request.AdmissionGeneration)
			if reconcileErr != nil {
				return RecoveryReconciliationResult{}, reconcileErr
			}
			if validRecoveryReconciliationClearResult(result) {
				if !addRecoveryReconciliationCounts(&aggregate.Counts, result.Counts) {
					return RecoveryReconciliationResult{}, ErrRecoveryReconciliationUnavailable
				}
				rootClear = true
				break
			}
			if !recoveryReconciliationPaginationOnly(result) ||
				result.NextCursor == cursor {
				return result, nil
			}
			if _, ok := recoveryReconciliationCursorAuditKeyVersion(result.NextCursor); !ok {
				return result, nil
			}
			cursor = result.NextCursor
		}
		if !rootClear {
			return RecoveryReconciliationResult{
				State: RecoveryReconciliationBlocked, Complete: false,
				Counts: RecoveryReconciliationCounts{ScanIncomplete: 1},
			}, nil
		}
	}
	return aggregate, nil
}

func validRecoveryReconciliationClearResult(result RecoveryReconciliationResult) bool {
	return result.State == RecoveryReconciliationClear && result.Complete && result.NextCursor == "" &&
		len(result.Findings) == 0 && result.Counts.Scanned >= 0 &&
		result.Counts.Scanned <= recoveryReconciliationChainLimit &&
		result.Counts.KnownHealthy == result.Counts.Scanned &&
		result.Counts.KnownDrift == 0 && result.Counts.DBUnmatched == 0 &&
		result.Counts.ForgedOrUnknown == 0 && result.Counts.ScanIncomplete == 0
}

func recoveryReconciliationPaginationOnly(result RecoveryReconciliationResult) bool {
	return result.State == RecoveryReconciliationBlocked && !result.Complete &&
		result.NextCursor != "" && len(result.Findings) == 0 &&
		result.Counts.Scanned > 0 && result.Counts.Scanned < recoveryReconciliationChainLimit &&
		result.Counts.Scanned%recoveryReconciliationPageLimit == 0 &&
		result.Counts.KnownHealthy == result.Counts.Scanned &&
		result.Counts.KnownDrift == 0 && result.Counts.DBUnmatched == 0 &&
		result.Counts.ForgedOrUnknown == 0 && result.Counts.ScanIncomplete == 0
}

func addRecoveryReconciliationCounts(
	destination *RecoveryReconciliationCounts,
	addition RecoveryReconciliationCounts,
) bool {
	if destination == nil {
		return false
	}
	maximum := int64(recoveryReconciliationRootLimit * recoveryReconciliationChainLimit)
	values := []*int{
		&destination.Scanned, &destination.KnownHealthy, &destination.KnownDrift,
		&destination.DBUnmatched, &destination.ForgedOrUnknown, &destination.ScanIncomplete,
	}
	additions := []int{
		addition.Scanned, addition.KnownHealthy, addition.KnownDrift,
		addition.DBUnmatched, addition.ForgedOrUnknown, addition.ScanIncomplete,
	}
	for index, value := range values {
		if additions[index] < 0 || int64(*value) > maximum-int64(additions[index]) {
			return false
		}
		*value += additions[index]
	}
	return true
}

func (service *RecoveryReconciliationService) buildRecoveryReconciliationExpectedSnapshot(
	ctx context.Context,
	request ReconcileRecoveryRootRequest,
) (recoveryReconciliationExpectedSnapshot, error) {
	var snapshot recoveryReconciliationExpectedSnapshot
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, err := service.roots.ResolveRecoveryTargetRootTx(
			ctx, tx, request.NodeID, request.RootID,
		)
		if err != nil || root.NodeID != request.NodeID || root.RootID != request.RootID ||
			root.Locator == "" || !validDigest(root.LocatorDigest) {
			return errRecoveryReconciliationIncomplete
		}
		snapshot.root = root
		revisions, err := service.revisions.ResolveRecoveryReconciliationRevisionsTx(
			ctx, tx, request.NodeID, request.RootID,
		)
		if err != nil || !revisions.valid() {
			return errRecoveryReconciliationIncomplete
		}
		snapshot.session = recoveryTargetReconciliationSessionBinding{
			nodeID: request.NodeID, nodeRevision: revisions.NodeRevision,
			credentialRevision: revisions.CredentialRevision,
			rootID:             request.RootID, rootLocator: root.Locator,
			rootLocatorDigest: root.LocatorDigest, rootRevision: revisions.RootRevision,
		}
		snapshot.session.bindingDigest = snapshot.session.digest()
		if !snapshot.session.valid() {
			return errRecoveryReconciliationIncomplete
		}

		var jobs []model.BackupAssetRecoveryJob
		if err := tx.WithContext(ctx).
			Where("target_mode = ? AND target_node_id = ? AND target_root_id = ?",
				string(TargetModeIsolated), request.NodeID, request.RootID).
			Where(recoveryReconciliationNonTombstoneJobScope,
				string(WorkspacePhaseCleaned), string(CleanupPhaseTombstoned),
				string(ResultSetStateCleaned), string(CleanupPhaseTombstoned)).
			Order("id ASC").Limit(recoveryReconciliationExpectedLimit + 1).Find(&jobs).Error; err != nil {
			return errRecoveryReconciliationIncomplete
		}
		if len(jobs) > recoveryReconciliationExpectedLimit {
			return errRecoveryReconciliationIncomplete
		}

		planIDs := make([]string, 0, len(jobs))
		jobIDs := make([]string, 0, len(jobs))
		seenPlans := make(map[string]struct{}, len(jobs))
		for _, job := range jobs {
			jobIDs = append(jobIDs, job.ID)
			if _, exists := seenPlans[job.PlanID]; !exists {
				seenPlans[job.PlanID] = struct{}{}
				planIDs = append(planIDs, job.PlanID)
			}
		}
		var plans []model.BackupAssetRecoveryPlan
		if err := tx.WithContext(ctx).Where("id IN ?", planIDs).Order("id ASC").
			Limit(len(planIDs) + 1).Find(&plans).Error; err != nil || len(plans) != len(planIDs) {
			return errRecoveryReconciliationIncomplete
		}
		plansByID := make(map[string]model.BackupAssetRecoveryPlan, len(plans))
		for _, plan := range plans {
			if _, duplicate := plansByID[plan.ID]; duplicate {
				return errRecoveryReconciliationIncomplete
			}
			plansByID[plan.ID] = plan
		}

		var resultSets []model.BackupAssetRecoveryResultSet
		if err := tx.WithContext(ctx).Where("job_id IN ?", jobIDs).Order("job_id ASC, id ASC").
			Limit(recoveryReconciliationExpectedLimit + 1).Find(&resultSets).Error; err != nil ||
			len(resultSets) > recoveryReconciliationExpectedLimit {
			return errRecoveryReconciliationIncomplete
		}
		var resultRelationships []recoveryReconciliationResultRelationship
		if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResult{}).
			Select("job_id, result_set_id, COUNT(*) AS result_count").
			Where("job_id IN ?", jobIDs).
			Group("job_id, result_set_id").Order("job_id ASC, result_set_id ASC").
			Limit(recoveryReconciliationExpectedLimit + 1).Scan(&resultRelationships).Error; err != nil ||
			len(resultRelationships) > recoveryReconciliationExpectedLimit {
			return errRecoveryReconciliationIncomplete
		}
		setsByJob := make(map[string][]model.BackupAssetRecoveryResultSet, len(resultSets))
		for _, resultSet := range resultSets {
			setsByJob[resultSet.JobID] = append(setsByJob[resultSet.JobID], resultSet)
		}
		resultsByJob := make(map[string][]recoveryReconciliationResultRelationship, len(resultRelationships))
		for _, relationship := range resultRelationships {
			if !validOpaqueID(relationship.JobID) || !validOpaqueID(relationship.ResultSetID) ||
				relationship.ResultCount <= 0 {
				return errRecoveryReconciliationIncomplete
			}
			resultsByJob[relationship.JobID] = append(resultsByJob[relationship.JobID], relationship)
		}

		candidates := make([]recoveryReconciliationExpectedCandidate, 0, len(jobs))
		for _, job := range jobs {
			remoteState, entryKind, contributes, stateErr := recoveryReconciliationJobRemoteState(
				job, setsByJob[job.ID], resultsByJob[job.ID],
			)
			if stateErr != nil {
				return errRecoveryReconciliationIncomplete
			}
			if !contributes {
				if WorkspacePhase(job.WorkspacePhase) == WorkspacePhaseNone {
					plan, ok := plansByID[job.PlanID]
					if !ok || !validRecoveryReconciliationUnreservedJobBinding(job, plan, root) {
						return errRecoveryReconciliationIncomplete
					}
				}
				continue
			}
			plan, ok := plansByID[job.PlanID]
			if !ok || !validRecoveryReconciliationJobBinding(job, plan, root) {
				return errRecoveryReconciliationIncomplete
			}
			jobCandidates := recoveryReconciliationExpectedComponents(job, plan.RootRevision, remoteState, entryKind)
			if len(jobCandidates) != 3 || len(candidates) > recoveryReconciliationExpectedLimit-len(jobCandidates) {
				return errRecoveryReconciliationIncomplete
			}
			candidates = append(candidates, jobCandidates...)
		}
		snapshot.candidates = candidates
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return recoveryReconciliationExpectedSnapshot{}, err
	}
	snapshot.candidates = append([]recoveryReconciliationExpectedCandidate(nil), snapshot.candidates...)
	return snapshot, nil
}

const recoveryReconciliationNonTombstoneJobScope = `
	NOT (
		workspace_phase = ?
		AND state IN ('succeeded', 'degraded', 'needs_attention', 'failed', 'canceled')
		AND workspace_cleanup_phase = ?
		AND workspace_marker_binding_digest <> ''
		AND workspace_owner <> ''
		AND workspace_fence > 0
		AND (
			(workspace_marker_validation_attempt_id = ''
				AND workspace_marker_validation_attempt_fence = 0
				AND workspace_marker_validation_node_fence = 0)
			OR (workspace_marker_validation_attempt_id <> ''
				AND workspace_marker_validation_attempt_fence > 0
				AND workspace_marker_validation_node_fence > 0)
		)
		AND plaintext_deadline IS NOT NULL
		AND workspace_cleanup_owner = ''
		AND workspace_cleanup_lease_expires_at IS NULL
		AND workspace_cleanup_fence > 0
		AND workspace_cleanup_node_lease_id IS NULL
		AND workspace_cleanup_node_fence = 0
		AND workspace_cleanup_attempt > 0
	)
	AND NOT EXISTS (
		SELECT 1
		FROM backup_asset_recovery_result_sets AS reconciliation_tombstone
		WHERE reconciliation_tombstone.job_id = backup_asset_recovery_jobs.id
			AND backup_asset_recovery_jobs.workspace_phase = 'published'
			AND backup_asset_recovery_jobs.state IN ('succeeded', 'degraded')
			AND backup_asset_recovery_jobs.workspace_marker_binding_digest <> ''
			AND backup_asset_recovery_jobs.workspace_owner <> ''
			AND backup_asset_recovery_jobs.workspace_fence > 0
			AND backup_asset_recovery_jobs.workspace_marker_validation_attempt_id <> ''
			AND backup_asset_recovery_jobs.workspace_marker_validation_attempt_fence > 0
			AND backup_asset_recovery_jobs.workspace_marker_validation_node_fence > 0
			AND backup_asset_recovery_jobs.plaintext_deadline IS NOT NULL
			AND backup_asset_recovery_jobs.workspace_cleanup_phase = 'claimed'
			AND backup_asset_recovery_jobs.workspace_cleanup_owner = ''
			AND backup_asset_recovery_jobs.workspace_cleanup_lease_expires_at IS NULL
			AND backup_asset_recovery_jobs.workspace_cleanup_fence = 0
			AND backup_asset_recovery_jobs.workspace_cleanup_node_lease_id IS NULL
			AND backup_asset_recovery_jobs.workspace_cleanup_node_fence = 0
			AND backup_asset_recovery_jobs.workspace_cleanup_attempt = 0
			AND reconciliation_tombstone.marker_binding_digest = backup_asset_recovery_jobs.workspace_marker_binding_digest
			AND reconciliation_tombstone.state = ?
			AND reconciliation_tombstone.cleanup_phase = ?
			AND reconciliation_tombstone.cleanup_owner = ''
			AND reconciliation_tombstone.cleanup_lease_expires_at IS NULL
			AND reconciliation_tombstone.cleanup_fence > 0
			AND reconciliation_tombstone.node_lease_id IS NULL
			AND reconciliation_tombstone.node_fence = 0
			AND reconciliation_tombstone.cleanup_attempt > 0
	)`

func recoveryReconciliationExpectedComponents(
	job model.BackupAssetRecoveryJob,
	rootRevision string,
	remoteState string,
	entryKind TargetEntryKind,
) []recoveryReconciliationExpectedCandidate {
	capturedComponent, verifiedComponent := recoveryOwnedCleanupComponents(
		job.ID, job.TargetRootID, rootRevision, job.PathDigest,
		job.WorkspaceMarkerBindingDigest, job.WorkspaceOwner, job.WorkspaceFence,
	)
	base := targetReconciliationExpected{
		jobID: job.ID, markerBindingDigest: job.WorkspaceMarkerBindingDigest,
		markerCreatorID: job.WorkspaceOwner, markerCreatorFence: job.WorkspaceFence,
	}
	final := base
	final.entryKind = entryKind
	final.remoteState = remoteState
	captured := base
	verified := base
	captured.entryKind = TargetEntryMissing
	captured.remoteState = recoveryReconciliationRemoteAbsent
	verified.entryKind = TargetEntryMissing
	verified.remoteState = recoveryReconciliationRemoteAbsent
	if remoteState == recoveryReconciliationRemoteDeleteStarted {
		captured.entryKind = TargetEntryDirectory
		captured.remoteState = recoveryReconciliationRemoteDeleteStarted
		verified.entryKind = TargetEntryRegular
		verified.remoteState = recoveryReconciliationRemoteDeleteStarted
	}
	return []recoveryReconciliationExpectedCandidate{
		{component: job.ID, expected: final},
		{component: capturedComponent, expected: captured},
		{component: verifiedComponent, expected: verified},
	}
}

func validRecoveryReconciliationJobBinding(
	job model.BackupAssetRecoveryJob,
	plan model.BackupAssetRecoveryPlan,
	root settings.RecoveryTargetRootResolution,
) bool {
	if !validRecoveryReconciliationJobPlanRootBinding(job, plan, root) ||
		!validDigest(job.WorkspaceMarkerBindingDigest) || !validRecoveryWorkerID(job.WorkspaceOwner) ||
		job.WorkspaceFence == 0 || job.PlaintextDeadline == nil || job.PlaintextDeadline.IsZero() ||
		!validRecoveryReconciliationJobStateAndCleanupShape(job) {
		return false
	}
	phase := WorkspacePhase(job.WorkspacePhase)
	switch phase {
	case WorkspacePhaseReserved:
		return recoveryReconciliationMarkerValidationEmpty(job)
	case WorkspacePhaseMarkerCreated, WorkspacePhaseWriting, WorkspacePhaseSealed, WorkspacePhasePublished:
		return recoveryReconciliationMarkerValidationValid(job)
	case WorkspacePhaseCleanupDue, WorkspacePhaseCleaned:
		return recoveryReconciliationMarkerValidationEmpty(job) || recoveryReconciliationMarkerValidationValid(job)
	default:
		return false
	}
}

func validRecoveryReconciliationUnreservedJobBinding(
	job model.BackupAssetRecoveryJob,
	plan model.BackupAssetRecoveryPlan,
	root settings.RecoveryTargetRootResolution,
) bool {
	return validRecoveryReconciliationJobPlanRootBinding(job, plan, root) &&
		WorkspacePhase(job.WorkspacePhase) == WorkspacePhaseNone && JobState(job.State).Valid() &&
		job.WorkspaceMarkerBindingDigest == "" && job.WorkspaceOwner == "" && job.WorkspaceFence == 0 &&
		recoveryReconciliationMarkerValidationEmpty(job) && job.PlaintextDeadline == nil &&
		validRecoveryReconciliationNeutralWorkspaceCleanup(job)
}

func validRecoveryReconciliationJobPlanRootBinding(
	job model.BackupAssetRecoveryJob,
	plan model.BackupAssetRecoveryPlan,
	root settings.RecoveryTargetRootResolution,
) bool {
	return validOpaqueID(job.ID) && validOpaqueID(job.PlanID) && job.PlanID == plan.ID &&
		TargetMode(job.TargetMode) == TargetModeIsolated && TargetMode(plan.TargetMode) == TargetModeIsolated &&
		job.TargetNodeID == root.NodeID && plan.TargetNodeID == root.NodeID &&
		job.TargetRootID == root.RootID && plan.TargetRootID == root.RootID &&
		job.RootLocatorDigest == root.LocatorDigest && plan.RootLocatorDigest == root.LocatorDigest &&
		plan.EncryptedTargetRootLocator == root.Locator &&
		validOpaqueRevision(plan.TargetBaseRevision) && validOpaqueRevision(plan.CredentialScopeRevision) &&
		validOpaqueRevision(plan.RootRevision) && job.PreflightNodeRevision == plan.TargetBaseRevision &&
		validOpaqueRevision(job.PreflightTargetRevision) && validDigest(job.PathDigest) &&
		job.EncryptedWorkspaceRelativeLocator == recoveryWorkspaceLocatorDirectory+"/"+job.ID &&
		validDigest(job.WorkspaceBindingDigest)
}

func recoveryReconciliationMarkerValidationEmpty(job model.BackupAssetRecoveryJob) bool {
	return job.WorkspaceMarkerValidationAttemptID == "" &&
		job.WorkspaceMarkerValidationAttemptFence == 0 && job.WorkspaceMarkerValidationNodeFence == 0
}

func recoveryReconciliationMarkerValidationValid(job model.BackupAssetRecoveryJob) bool {
	return validOpaqueID(job.WorkspaceMarkerValidationAttemptID) &&
		job.WorkspaceMarkerValidationAttemptFence > 0 && job.WorkspaceMarkerValidationNodeFence > 0
}

func validRecoveryReconciliationJobStateAndCleanupShape(job model.BackupAssetRecoveryJob) bool {
	state := JobState(job.State)
	phase := WorkspacePhase(job.WorkspacePhase)
	switch phase {
	case WorkspacePhaseReserved, WorkspacePhaseMarkerCreated, WorkspacePhaseWriting:
		return state == JobStateRunning && validRecoveryReconciliationNeutralWorkspaceCleanup(job)
	case WorkspacePhaseSealed, WorkspacePhasePublished:
		return (state == JobStateSucceeded || state == JobStateDegraded) &&
			validRecoveryReconciliationNeutralWorkspaceCleanup(job)
	case WorkspacePhaseCleanupDue:
		return terminalRecoveryWorkspaceCleanupJobState(state) &&
			validRecoveryReconciliationWorkspaceCleanupShape(job, false)
	case WorkspacePhaseCleaned:
		return terminalRecoveryWorkspaceCleanupJobState(state) &&
			validRecoveryReconciliationWorkspaceCleanupShape(job, true)
	default:
		return false
	}
}

func validRecoveryReconciliationNeutralWorkspaceCleanup(job model.BackupAssetRecoveryJob) bool {
	return CleanupPhase(job.WorkspaceCleanupPhase) == CleanupPhaseClaimed &&
		job.WorkspaceCleanupOwner == "" && job.WorkspaceCleanupLeaseExpiresAt == nil &&
		job.WorkspaceCleanupFence == 0 && job.WorkspaceCleanupNodeLeaseID == nil &&
		job.WorkspaceCleanupNodeFence == 0 && job.WorkspaceCleanupAttempt == 0
}

func validRecoveryReconciliationWorkspaceCleanupShape(
	job model.BackupAssetRecoveryJob,
	tombstoned bool,
) bool {
	phase := CleanupPhase(job.WorkspaceCleanupPhase)
	if !phase.Valid() {
		return false
	}
	if tombstoned {
		return phase == CleanupPhaseTombstoned && job.WorkspaceCleanupOwner == "" &&
			job.WorkspaceCleanupLeaseExpiresAt == nil && job.WorkspaceCleanupFence > 0 &&
			job.WorkspaceCleanupNodeLeaseID == nil && job.WorkspaceCleanupNodeFence == 0 &&
			job.WorkspaceCleanupAttempt > 0
	}
	if phase == CleanupPhaseTombstoned {
		return false
	}
	if validRecoveryReconciliationNeutralWorkspaceCleanup(job) {
		return true
	}
	if job.WorkspaceCleanupOwner == "" {
		return job.WorkspaceCleanupLeaseExpiresAt == nil && job.WorkspaceCleanupFence > 0 &&
			job.WorkspaceCleanupNodeLeaseID == nil && job.WorkspaceCleanupNodeFence == 0 &&
			job.WorkspaceCleanupAttempt > 0
	}
	return validRecoveryWorkerID(job.WorkspaceCleanupOwner) && job.WorkspaceCleanupLeaseExpiresAt != nil &&
		!job.WorkspaceCleanupLeaseExpiresAt.IsZero() && job.WorkspaceCleanupFence > 0 &&
		job.WorkspaceCleanupNodeLeaseID != nil && validOpaqueID(*job.WorkspaceCleanupNodeLeaseID) &&
		job.WorkspaceCleanupNodeFence > 0 && job.WorkspaceCleanupAttempt > 0
}

func recoveryReconciliationJobRemoteState(
	job model.BackupAssetRecoveryJob,
	resultSets []model.BackupAssetRecoveryResultSet,
	results []recoveryReconciliationResultRelationship,
) (string, TargetEntryKind, bool, error) {
	phase := WorkspacePhase(job.WorkspacePhase)
	switch phase {
	case WorkspacePhaseNone:
		if len(resultSets) != 0 || len(results) != 0 {
			return "", "", false, errRecoveryReconciliationIncomplete
		}
		return "", "", false, nil
	case WorkspacePhaseReserved, WorkspacePhaseMarkerCreated, WorkspacePhaseWriting, WorkspacePhaseSealed:
		if len(resultSets) != 0 || len(results) != 0 {
			return "", "", false, errRecoveryReconciliationIncomplete
		}
		return recoveryReconciliationRemoteFinal, TargetEntryDirectory, true, nil
	case WorkspacePhasePublished:
		if len(resultSets) != 1 || len(results) == 0 {
			return "", "", false, errRecoveryReconciliationIncomplete
		}
		resultSet := resultSets[0]
		if resultSet.JobID != job.ID || resultSet.MarkerBindingDigest != job.WorkspaceMarkerBindingDigest ||
			!validOpaqueID(resultSet.ID) || !validDigest(resultSet.MarkerBindingDigest) {
			return "", "", false, errRecoveryReconciliationIncomplete
		}
		for _, result := range results {
			if result.JobID != job.ID || result.ResultSetID != resultSet.ID || result.ResultCount <= 0 {
				return "", "", false, errRecoveryReconciliationIncomplete
			}
		}
		return recoveryReconciliationResultSetRemoteState(resultSet)
	case WorkspacePhaseCleanupDue:
		if len(resultSets) != 0 || len(results) != 0 {
			return "", "", false, errRecoveryReconciliationIncomplete
		}
		return recoveryReconciliationCleanupRemoteState(CleanupPhase(job.WorkspaceCleanupPhase), false)
	case WorkspacePhaseCleaned:
		if len(resultSets) != 0 || len(results) != 0 ||
			CleanupPhase(job.WorkspaceCleanupPhase) != CleanupPhaseTombstoned {
			return "", "", false, errRecoveryReconciliationIncomplete
		}
		return "", "", false, nil
	default:
		return "", "", false, errRecoveryReconciliationIncomplete
	}
}

func recoveryReconciliationResultSetRemoteState(
	resultSet model.BackupAssetRecoveryResultSet,
) (string, TargetEntryKind, bool, error) {
	state := ResultSetState(resultSet.State)
	phase := CleanupPhase(resultSet.CleanupPhase)
	if !validRecoveryReconciliationResultSetCleanupShape(resultSet) {
		return "", "", false, errRecoveryReconciliationIncomplete
	}
	switch state {
	case ResultSetStateReady:
		if phase != CleanupPhaseClaimed {
			return "", "", false, errRecoveryReconciliationIncomplete
		}
		return recoveryReconciliationRemoteFinal, TargetEntryDirectory, true, nil
	case ResultSetStateRevoking:
		return recoveryReconciliationCleanupRemoteState(phase, false)
	case ResultSetStateCleanupFailed:
		if phase != CleanupPhaseDrained && phase != CleanupPhaseDeleteStarted {
			return "", "", false, errRecoveryReconciliationIncomplete
		}
		return recoveryReconciliationCleanupRemoteState(phase, false)
	case ResultSetStateCleaned:
		if phase != CleanupPhaseTombstoned {
			return "", "", false, errRecoveryReconciliationIncomplete
		}
		return "", "", false, nil
	default:
		return "", "", false, errRecoveryReconciliationIncomplete
	}
}

func validRecoveryReconciliationResultSetCleanupShape(
	resultSet model.BackupAssetRecoveryResultSet,
) bool {
	phase := CleanupPhase(resultSet.CleanupPhase)
	switch ResultSetState(resultSet.State) {
	case ResultSetStateReady:
		return phase == CleanupPhaseClaimed && resultSet.CleanupOwner == "" &&
			resultSet.CleanupLeaseExpiresAt == nil && resultSet.CleanupFence == 0 &&
			resultSet.NodeLeaseID == nil && resultSet.NodeFence == 0 && resultSet.CleanupAttempt == 0
	case ResultSetStateRevoking:
		return phase.Valid() && phase != CleanupPhaseTombstoned &&
			validRecoveryWorkerID(resultSet.CleanupOwner) && resultSet.CleanupLeaseExpiresAt != nil &&
			!resultSet.CleanupLeaseExpiresAt.IsZero() && resultSet.CleanupFence > 0 &&
			resultSet.NodeLeaseID != nil && validOpaqueID(*resultSet.NodeLeaseID) &&
			resultSet.NodeFence > 0 && resultSet.CleanupAttempt > 0
	case ResultSetStateCleanupFailed:
		return (phase == CleanupPhaseDrained || phase == CleanupPhaseDeleteStarted) &&
			resultSet.CleanupOwner == "" && resultSet.CleanupLeaseExpiresAt == nil &&
			resultSet.CleanupFence > 0 && resultSet.NodeLeaseID == nil && resultSet.NodeFence == 0 &&
			resultSet.CleanupAttempt > 0
	case ResultSetStateCleaned:
		return phase == CleanupPhaseTombstoned && resultSet.CleanupOwner == "" &&
			resultSet.CleanupLeaseExpiresAt == nil && resultSet.CleanupFence > 0 &&
			resultSet.NodeLeaseID == nil && resultSet.NodeFence == 0 && resultSet.CleanupAttempt > 0
	default:
		return false
	}
}

func recoveryReconciliationCleanupRemoteState(
	phase CleanupPhase,
	allowTombstone bool,
) (string, TargetEntryKind, bool, error) {
	switch phase {
	case CleanupPhaseClaimed, CleanupPhaseRevoked, CleanupPhaseDrained, CleanupPhaseValidated:
		return recoveryReconciliationRemoteFinal, TargetEntryDirectory, true, nil
	case CleanupPhaseDeleteStarted:
		return recoveryReconciliationRemoteDeleteStarted, TargetEntryDirectory, true, nil
	case CleanupPhaseDeleted:
		return recoveryReconciliationRemoteAbsent, TargetEntryMissing, true, nil
	case CleanupPhaseTombstoned:
		if allowTombstone {
			return "", "", false, nil
		}
		fallthrough
	default:
		return "", "", false, errRecoveryReconciliationIncomplete
	}
}

func recoveryReconciliationComponentToken(
	key [sha256.Size]byte,
	auditKeyVersion int,
	session recoveryTargetReconciliationSessionBinding,
	component string,
	expected targetReconciliationExpected,
) string {
	buffer := bytes.NewBuffer(nil)
	writeRecoveryDigestString(buffer, recoveryReconciliationComponentDomain)
	writeRecoveryDigestString(buffer, strconv.Itoa(auditKeyVersion))
	writeRecoveryDigestString(buffer, session.bindingDigest)
	writeRecoveryDigestString(buffer, component)
	writeRecoveryDigestString(buffer, string(expected.entryKind))
	writeRecoveryDigestString(buffer, expected.remoteState)
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write(buffer.Bytes())
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func recoveryReconciliationExpectedSetDigest(
	auditKeyVersion int,
	session recoveryTargetReconciliationSessionBinding,
	expected []targetReconciliationExpected,
) string {
	values := make([]string, 0, 3+len(expected)*7)
	values = append(values, strconv.Itoa(auditKeyVersion), session.bindingDigest, strconv.Itoa(len(expected)))
	for _, row := range expected {
		values = append(values,
			row.componentToken, row.jobID, string(row.entryKind), row.remoteState,
			row.markerBindingDigest, row.markerCreatorID, strconv.FormatUint(row.markerCreatorFence, 10),
		)
	}
	return framedDigest(recoveryReconciliationExpectedDomain, values...)
}

func targetReconciliationPermitBindingDigest(
	key [sha256.Size]byte,
	auditKeyVersion int,
	permit TargetReconciliationPermit,
	sessionBindingDigest string,
) string {
	buffer := bytes.NewBuffer(nil)
	for _, value := range []string{
		"xirang/recovery/target-reconciliation-permit/v1", strconv.Itoa(auditKeyVersion),
		strconv.Itoa(permit.SchemaVersion), string(permit.Purpose), string(permit.Operation),
		strconv.FormatUint(uint64(permit.NodeID), 10), permit.RootID, permit.RootLocatorDigest,
		permit.RootRevision, permit.ExpectedSetDigest, strconv.Itoa(permit.PageLimit),
		strconv.Itoa(permit.ChainLimit), strconv.Itoa(permit.FindingLimit), permit.Cursor,
		permit.AdmissionGeneration, permit.ExpiresAt.UTC().Format(time.RFC3339Nano), sessionBindingDigest,
	} {
		writeRecoveryDigestString(buffer, value)
	}
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write(buffer.Bytes())
	return hex.EncodeToString(mac.Sum(nil))
}

func recoveryReconciliationResultFromPage(page TargetReconciliationPage) RecoveryReconciliationResult {
	findings := append([]RecoveryReconciliationFinding(nil), page.Findings...)
	state := RecoveryReconciliationClear
	if !page.Complete || page.NextCursor != "" || page.Counts.KnownDrift > 0 ||
		page.Counts.DBUnmatched > 0 || page.Counts.ForgedOrUnknown > 0 ||
		page.Counts.ScanIncomplete > 0 || len(findings) > 0 {
		state = RecoveryReconciliationBlocked
	}
	return RecoveryReconciliationResult{
		State: state, Complete: page.Complete, NextCursor: page.NextCursor,
		Counts: page.Counts, Findings: findings,
	}
}

func recoveryReconciliationIncompleteResult(
	material backupasset.DomainKeyMaterial,
	request ReconcileRecoveryRootRequest,
) RecoveryReconciliationResult {
	buffer := bytes.NewBuffer(nil)
	writeRecoveryDigestString(buffer, recoveryReconciliationFindingDomain)
	writeRecoveryDigestString(buffer, strconv.Itoa(material.Version))
	writeRecoveryDigestString(buffer, strconv.FormatUint(uint64(request.NodeID), 10))
	writeRecoveryDigestString(buffer, request.RootID)
	writeRecoveryDigestString(buffer, string(RecoveryReconciliationScanIncomplete))
	mac := hmac.New(sha256.New, material.Key)
	_, _ = mac.Write(buffer.Bytes())
	finding := RecoveryReconciliationFinding{
		Category:    RecoveryReconciliationScanIncomplete,
		Fingerprint: base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
		EntryKind:   TargetEntryMissing,
	}
	return RecoveryReconciliationResult{
		State: RecoveryReconciliationBlocked, Complete: false,
		Counts:   RecoveryReconciliationCounts{ScanIncomplete: 1},
		Findings: []RecoveryReconciliationFinding{finding},
	}
}
