package content

import (
	"context"
	"errors"
	"math"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidBudgetConfig = errors.New("invalid content budget config")
	ErrInvalidReservation  = errors.New("invalid content reservation")
	ErrBudgetExhausted     = errors.New("content budget exhausted")
	ErrGrantUnavailable    = errors.New("content grant unavailable")
	ErrReservationReplay   = errors.New("content reservation replay")
	ErrReservationNotFound = errors.New("content reservation not found")
	ErrReservationStale    = errors.New("content reservation stale")
	ErrBudgetState         = errors.New("invalid content budget state")
	errBudgetConcurrent    = errors.New("concurrent content budget update")
)

type BudgetScopeKind string

const (
	BudgetScopeGlobal   BudgetScopeKind = "global"
	BudgetScopeProvider BudgetScopeKind = "provider"
	BudgetScopeUser     BudgetScopeKind = "user"
)

type BudgetScopeKey struct {
	Kind BudgetScopeKind
	ID   string
}

type BudgetScopeLimits struct {
	WindowBytes    int64
	WindowRequests int64
	MaxInFlight    int64
}

type BudgetLimits struct {
	Window   time.Duration
	Global   BudgetScopeLimits
	Provider BudgetScopeLimits
	User     BudgetScopeLimits
}

type BudgetDependencies struct {
	DB     *gorm.DB
	Now    func() time.Time
	Limits func(context.Context) (BudgetLimits, error)
}

type BudgetService struct {
	db     *gorm.DB
	now    func() time.Time
	limits func(context.Context) (BudgetLimits, error)
}

type RequestState string

const (
	RequestReserved   RequestState = "reserved"
	RequestStreaming  RequestState = "streaming"
	RequestSucceeded  RequestState = "succeeded"
	RequestBlocked    RequestState = "blocked"
	RequestCanceled   RequestState = "canceled"
	RequestFailed     RequestState = "failed"
	RequestReconciled RequestState = "reconciled"
)

type ReservationIntent struct {
	RequestID     string
	GrantID       string
	Method        string
	Range         HTTPRange
	ReservedBytes int64
}

type BlockedRequest struct {
	RequestID      string
	GrantID        string
	Method         string
	Status         int
	FailureCode    RequestFailureCode
	RangeRequested bool
}

type Reservation struct {
	RequestID      string
	GrantID        string
	RequestVersion int64
	ReservedBytes  int64
}

type FinalizeIntent struct {
	RequestID              string
	ExpectedRequestVersion int64
	State                  RequestState
	HTTPStatus             int
	FailureCode            RequestFailureCode
	ProviderBytes          int64
	ResponseBytes          int64
	EvidenceKnown          bool
}

type Finalization struct {
	RequestID        string
	State            RequestState
	ChargedBytes     int64
	AlreadyFinalized bool
}

type reservationSpec struct {
	requestID      string
	grantID        string
	method         string
	rangeValue     HTTPRange
	reservedBytes  int64
	blocked        bool
	rangeRequested bool
	status         int
	failure        RequestFailureCode
}

type lockedBudgetScope struct {
	key    BudgetScopeKey
	row    model.BackupAssetDeliveryUsage
	limits BudgetScopeLimits
}

func NewBudgetService(dependencies BudgetDependencies) (*BudgetService, error) {
	if dependencies.DB == nil || dependencies.Now == nil || dependencies.Limits == nil {
		return nil, ErrInvalidBudgetConfig
	}
	limits, err := dependencies.Limits(context.Background())
	if err != nil || !validBudgetLimits(limits) {
		return nil, ErrInvalidBudgetConfig
	}
	return &BudgetService{db: dependencies.DB, now: dependencies.Now, limits: dependencies.Limits}, nil
}

func (service *BudgetService) Reserve(ctx context.Context, intent ReservationIntent) (Reservation, error) {
	if !validReservationIntent(intent) {
		return Reservation{}, ErrInvalidReservation
	}
	return service.reserve(ctx, reservationSpec{
		requestID: intent.RequestID, grantID: intent.GrantID, method: intent.Method,
		rangeValue: intent.Range, reservedBytes: intent.ReservedBytes,
	})
}

func (service *BudgetService) RecordBlocked(ctx context.Context, request BlockedRequest) error {
	if !validBlockedRequest(request) {
		return ErrInvalidReservation
	}
	_, err := service.reserve(ctx, reservationSpec{
		requestID: request.RequestID, grantID: request.GrantID, method: request.Method,
		rangeValue: HTTPRange{Kind: HTTPRangeFull}, blocked: true, rangeRequested: request.RangeRequested,
		status: request.Status, failure: request.FailureCode,
	})
	return err
}

func (service *BudgetService) reserve(ctx context.Context, spec reservationSpec) (Reservation, error) {
	ctx = nonNilContext(ctx)
	limits, err := service.limits(ctx)
	if err != nil || !validBudgetLimits(limits) {
		return Reservation{}, ErrInvalidBudgetConfig
	}
	scopeGrant, err := service.loadGrantScope(ctx, spec.grantID)
	if err != nil {
		return Reservation{}, err
	}
	keys := orderedBudgetScopeKeys(scopeGrant)
	if !validBudgetScopeKeys(keys) {
		return Reservation{}, ErrGrantUnavailable
	}
	now := service.now().UTC()
	var reservation Reservation
	err = service.transactionWithRetry(ctx, func(tx *gorm.DB) error {
		scopes := make([]lockedBudgetScope, 0, len(keys))
		for _, key := range keys {
			usage, lockErr := service.lockOrCreateUsage(tx, key, now, limits.Window)
			if lockErr != nil {
				return lockErr
			}
			scopes = append(scopes, lockedBudgetScope{key: key, row: usage, limits: limitsForScope(limits, key.Kind)})
		}

		grant, lockErr := service.lockGrant(tx, spec.grantID)
		if lockErr != nil {
			return lockErr
		}
		if grant.OwnerUserID != scopeGrant.OwnerUserID || grant.ProviderKind != scopeGrant.ProviderKind ||
			!grantAcceptsNewRequest(grant, now) {
			return ErrGrantUnavailable
		}
		var existing model.BackupAssetDeliveryRequest
		existingErr := tx.Where("id = ?", spec.requestID).Take(&existing).Error
		if existingErr == nil {
			return ErrReservationReplay
		}
		if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}

		active := !spec.blocked
		for index := range scopes {
			if err := prepareUsageReservation(&scopes[index].row, scopes[index].limits, now, limits.Window, spec.reservedBytes, active); err != nil {
				return err
			}
		}
		if err := prepareGrantReservation(&grant, now, spec.reservedBytes, active); err != nil {
			return err
		}
		if spec.blocked {
			if err := applyGrantAuditSummary(&grant, backupasset.AuditOutcomeBlocked, 0, spec.rangeRequested); err != nil {
				return err
			}
		}
		for _, scope := range scopes {
			if err := service.updateUsage(tx, scope.row); err != nil {
				return err
			}
		}
		if err := service.updateGrantCounters(tx, grant); err != nil {
			return err
		}

		request := buildReservationRow(spec, now)
		if err := tx.Create(&request).Error; err != nil {
			return err
		}
		reservation = Reservation{
			RequestID: request.ID, GrantID: request.GrantID,
			RequestVersion: request.Version, ReservedBytes: request.ReservedBytes,
		}
		return nil
	})
	return reservation, err
}

func (service *BudgetService) Finalize(ctx context.Context, intent FinalizeIntent) (Finalization, error) {
	if !validFinalizeIntent(intent) {
		return Finalization{}, ErrInvalidReservation
	}
	ctx = nonNilContext(ctx)
	var snapshot model.BackupAssetDeliveryRequest
	if err := service.db.WithContext(ctx).Select("id", "grant_id").Where("id = ?", intent.RequestID).Take(&snapshot).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Finalization{}, ErrReservationNotFound
		}
		return Finalization{}, err
	}
	scopeGrant, err := service.loadGrantScope(ctx, snapshot.GrantID)
	if err != nil {
		return Finalization{}, err
	}
	keys := orderedBudgetScopeKeys(scopeGrant)
	if !validBudgetScopeKeys(keys) {
		return Finalization{}, ErrBudgetState
	}
	now := service.now().UTC()
	var finalization Finalization
	err = service.transactionWithRetry(ctx, func(tx *gorm.DB) error {
		scopes := make([]model.BackupAssetDeliveryUsage, 0, len(keys))
		for _, key := range keys {
			usage, lockErr := service.lockExistingUsage(tx, key)
			if lockErr != nil {
				return lockErr
			}
			scopes = append(scopes, usage)
		}
		grant, lockErr := service.lockGrant(tx, snapshot.GrantID)
		if lockErr != nil {
			return lockErr
		}
		if grant.OwnerUserID != scopeGrant.OwnerUserID || grant.ProviderKind != scopeGrant.ProviderKind {
			return ErrBudgetState
		}
		request, lockErr := service.lockRequest(tx, intent.RequestID)
		if lockErr != nil {
			return lockErr
		}
		if request.GrantID != grant.ID {
			return ErrBudgetState
		}
		if requestTerminal(RequestState(request.State)) {
			finalization = terminalFinalization(request)
			return nil
		}
		if RequestState(request.State) != RequestReserved && RequestState(request.State) != RequestStreaming {
			return ErrBudgetState
		}
		if request.Version != intent.ExpectedRequestVersion {
			return ErrReservationStale
		}

		charged, evidenceValid := finalCharge(request.ReservedBytes, intent)
		if grant.ReservedBytes < request.ReservedBytes || grant.InFlight <= 0 ||
			wouldAddOverflow(grant.DeliveredBytes, charged) {
			return ErrBudgetState
		}
		for _, usage := range scopes {
			if usage.ReservedBytes < request.ReservedBytes || usage.InFlight <= 0 ||
				wouldAddOverflow(usage.DeliveredBytes, charged) {
				return ErrBudgetState
			}
		}

		for index := range scopes {
			scopes[index].ReservedBytes -= request.ReservedBytes
			scopes[index].DeliveredBytes += charged
			scopes[index].InFlight--
			scopes[index].Version++
			scopes[index].UpdatedAt = now
			if err := service.updateUsage(tx, scopes[index]); err != nil {
				return err
			}
		}
		storedProvider, storedResponse := boundedEvidence(request.ReservedBytes, intent.ProviderBytes), boundedEvidence(request.ReservedBytes, intent.ResponseBytes)
		outcome := backupasset.AuditOutcomeFailure
		if intent.State == RequestSucceeded {
			outcome = backupasset.AuditOutcomeSuccess
		}
		if err := applyGrantAuditSummary(&grant, outcome, storedResponse, request.RangeKind != string(HTTPRangeFull)); err != nil {
			return err
		}
		grant.ReservedBytes -= request.ReservedBytes
		grant.DeliveredBytes += charged
		grant.InFlight--
		grant.Version++
		grant.UpdatedAt = now
		if err := service.updateGrantCounters(tx, grant); err != nil {
			return err
		}

		request.State = string(intent.State)
		request.ProviderBytes = storedProvider
		request.ResponseBytes = storedResponse
		request.HTTPStatus = intent.HTTPStatus
		request.FailureCode = string(intent.FailureCode)
		request.FinishedAt = &now
		request.LastProgressAt = now
		request.UpdatedAt = now
		request.Version++
		if !evidenceValid && request.State == string(RequestSucceeded) {
			return ErrInvalidReservation
		}
		if err := service.updateRequest(tx, request); err != nil {
			return err
		}
		finalization = Finalization{RequestID: request.ID, State: intent.State, ChargedBytes: charged}
		return nil
	})
	return finalization, err
}

func ComputeReservationBytes(responseBytes, providerLimit int64, probePossible bool) (int64, error) {
	if responseBytes < 0 || providerLimit < 0 || probePossible && providerLimit == math.MaxInt64 {
		return 0, ErrInvalidReservation
	}
	if probePossible {
		providerLimit++
	}
	return max(responseBytes, providerLimit), nil
}

func orderedBudgetScopeKeys(grant model.BackupAssetDeliveryGrant) []BudgetScopeKey {
	return []BudgetScopeKey{
		{Kind: BudgetScopeGlobal, ID: "global"},
		{Kind: BudgetScopeProvider, ID: grant.ProviderKind},
		{Kind: BudgetScopeUser, ID: strconv.FormatUint(uint64(grant.OwnerUserID), 10)},
	}
}

func (service *BudgetService) loadGrantScope(ctx context.Context, grantID string) (model.BackupAssetDeliveryGrant, error) {
	var grant model.BackupAssetDeliveryGrant
	if err := service.db.WithContext(ctx).Select("id", "owner_user_id", "provider_kind").Where("id = ?", grantID).Take(&grant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return grant, ErrGrantUnavailable
		}
		return grant, err
	}
	return grant, nil
}

func (service *BudgetService) lockOrCreateUsage(
	tx *gorm.DB,
	key BudgetScopeKey,
	now time.Time,
	window time.Duration,
) (model.BackupAssetDeliveryUsage, error) {
	usage, err := service.lockUsage(tx, key)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return usage, err
	}
	candidate := model.BackupAssetDeliveryUsage{
		ScopeKind: string(key.Kind), ScopeID: key.ID,
		WindowStartedAt: now, WindowExpiresAt: now.Add(window), Version: 1, UpdatedAt: now,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
		return usage, err
	}
	return service.lockUsage(tx, key)
}

func (service *BudgetService) lockExistingUsage(tx *gorm.DB, key BudgetScopeKey) (model.BackupAssetDeliveryUsage, error) {
	usage, err := service.lockUsage(tx, key)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return usage, ErrBudgetState
	}
	return usage, err
}

func (service *BudgetService) lockUsage(tx *gorm.DB, key BudgetScopeKey) (model.BackupAssetDeliveryUsage, error) {
	var usage model.BackupAssetDeliveryUsage
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("scope_kind = ? AND scope_id = ?", key.Kind, key.ID).Take(&usage).Error
	return usage, err
}

func (service *BudgetService) lockGrant(tx *gorm.DB, grantID string) (model.BackupAssetDeliveryGrant, error) {
	var grant model.BackupAssetDeliveryGrant
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", grantID).Take(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return grant, ErrGrantUnavailable
	}
	return grant, err
}

func (service *BudgetService) lockRequest(tx *gorm.DB, requestID string) (model.BackupAssetDeliveryRequest, error) {
	var request model.BackupAssetDeliveryRequest
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", requestID).Take(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return request, ErrReservationNotFound
	}
	return request, err
}

func (service *BudgetService) updateUsage(tx *gorm.DB, usage model.BackupAssetDeliveryUsage) error {
	previousVersion := usage.Version - 1
	result := tx.Model(&model.BackupAssetDeliveryUsage{}).
		Where("scope_kind = ? AND scope_id = ? AND version = ?", usage.ScopeKind, usage.ScopeID, previousVersion).
		Updates(map[string]any{
			"window_started_at": usage.WindowStartedAt, "window_expires_at": usage.WindowExpiresAt,
			"request_count": usage.RequestCount, "reserved_bytes": usage.ReservedBytes,
			"delivered_bytes": usage.DeliveredBytes, "in_flight": usage.InFlight,
			"version": usage.Version, "updated_at": usage.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errBudgetConcurrent
	}
	return nil
}

func (service *BudgetService) updateGrantCounters(tx *gorm.DB, grant model.BackupAssetDeliveryGrant) error {
	previousVersion := grant.Version - 1
	result := tx.Model(&model.BackupAssetDeliveryGrant{}).Where("id = ? AND version = ?", grant.ID, previousVersion).
		Updates(map[string]any{
			"idle_expires_at": grant.IdleExpiresAt, "last_activity_at": grant.LastActivityAt,
			"reserved_bytes": grant.ReservedBytes, "delivered_bytes": grant.DeliveredBytes,
			"request_count": grant.RequestCount, "in_flight": grant.InFlight,
			"audit_state": grant.AuditState, "audit_range_count": grant.AuditRangeCount,
			"audit_range_bytes": grant.AuditRangeBytes, "audit_request_count": grant.AuditRequestCount,
			"audit_success_count": grant.AuditSuccessCount, "audit_blocked_count": grant.AuditBlockedCount,
			"audit_failure_count": grant.AuditFailureCount,
			"version":             grant.Version, "updated_at": grant.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errBudgetConcurrent
	}
	return nil
}

func (service *BudgetService) updateRequest(tx *gorm.DB, request model.BackupAssetDeliveryRequest) error {
	previousVersion := request.Version - 1
	result := tx.Model(&model.BackupAssetDeliveryRequest{}).Where("id = ? AND version = ?", request.ID, previousVersion).
		Updates(map[string]any{
			"state": request.State, "provider_bytes": request.ProviderBytes,
			"response_bytes": request.ResponseBytes, "http_status": request.HTTPStatus,
			"failure_code": request.FailureCode, "last_progress_at": request.LastProgressAt,
			"finished_at": request.FinishedAt, "version": request.Version, "updated_at": request.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errBudgetConcurrent
	}
	return nil
}

func (service *BudgetService) transactionWithRetry(ctx context.Context, operation func(*gorm.DB) error) error {
	var lastErr error
	for attempt := 0; attempt < 16; attempt++ {
		err := service.db.WithContext(ctx).Transaction(operation)
		if err == nil || !retryableBudgetError(err) {
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

func prepareUsageReservation(
	usage *model.BackupAssetDeliveryUsage,
	limits BudgetScopeLimits,
	now time.Time,
	window time.Duration,
	reservedBytes int64,
	active bool,
) error {
	if usage == nil || usage.Version <= 0 || usage.RequestCount < 0 || usage.ReservedBytes < 0 ||
		usage.DeliveredBytes < 0 || usage.InFlight < 0 || !usage.WindowExpiresAt.After(usage.WindowStartedAt) {
		return ErrBudgetState
	}
	if !now.Before(usage.WindowExpiresAt) && usage.InFlight == 0 {
		usage.WindowStartedAt = now
		usage.WindowExpiresAt = now.Add(window)
		usage.RequestCount = 0
		usage.ReservedBytes = 0
		usage.DeliveredBytes = 0
	}
	if usage.RequestCount >= limits.WindowRequests {
		return ErrBudgetExhausted
	}
	if active {
		if usage.InFlight >= limits.MaxInFlight || !withinByteLimit(usage.DeliveredBytes, usage.ReservedBytes, reservedBytes, limits.WindowBytes) {
			return ErrBudgetExhausted
		}
		usage.ReservedBytes += reservedBytes
		usage.InFlight++
	}
	usage.RequestCount++
	usage.Version++
	usage.UpdatedAt = now
	return nil
}

func prepareGrantReservation(grant *model.BackupAssetDeliveryGrant, now time.Time, reservedBytes int64, active bool) error {
	if grant == nil || grant.Version <= 0 || grant.RequestCount < 0 || grant.ReservedBytes < 0 ||
		grant.DeliveredBytes < 0 || grant.InFlight < 0 || grant.MaxRequests <= 0 || grant.MaxInFlight <= 0 ||
		grant.MaxBytesPerRequest <= 0 || grant.MaxCumulativeBytes < grant.MaxBytesPerRequest {
		return ErrBudgetState
	}
	if grant.RequestCount >= grant.MaxRequests {
		return ErrBudgetExhausted
	}
	if active {
		if reservedBytes > grant.MaxBytesPerRequest || grant.InFlight >= grant.MaxInFlight ||
			!withinByteLimit(grant.DeliveredBytes, grant.ReservedBytes, reservedBytes, grant.MaxCumulativeBytes) {
			return ErrBudgetExhausted
		}
		grant.ReservedBytes += reservedBytes
		grant.InFlight++
		grant.LastActivityAt = now
		grant.IdleExpiresAt = refreshedIdleExpiry(*grant, now)
	}
	grant.RequestCount++
	grant.Version++
	grant.UpdatedAt = now
	return nil
}

func applyGrantAuditSummary(
	grant *model.BackupAssetDeliveryGrant,
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
		return ErrBudgetState
	}
	if grant.AuditState == "none" && (grant.AuditRangeCount != 0 || grant.AuditRangeBytes != 0 ||
		grant.AuditRequestCount != 0 || grant.AuditSuccessCount != 0 || grant.AuditBlockedCount != 0 ||
		grant.AuditFailureCount != 0) {
		return ErrBudgetState
	}
	grant.AuditState = "pending"
	grant.AuditRequestCount = boundedAuditCounter(grant.AuditRequestCount, 1, backupasset.MaxAuditRangeCount)
	if rangeRequested {
		grant.AuditRangeCount = boundedAuditCounter(grant.AuditRangeCount, 1, backupasset.MaxAuditRangeCount)
		grant.AuditRangeBytes = boundedAuditCounter(grant.AuditRangeBytes, responseBytes, backupasset.MaxAuditRangeBytes)
	}
	switch outcome {
	case backupasset.AuditOutcomeSuccess:
		grant.AuditSuccessCount = boundedAuditCounter(grant.AuditSuccessCount, 1, backupasset.MaxAuditRangeCount)
	case backupasset.AuditOutcomeBlocked:
		grant.AuditBlockedCount = boundedAuditCounter(grant.AuditBlockedCount, 1, backupasset.MaxAuditRangeCount)
	case backupasset.AuditOutcomeFailure:
		grant.AuditFailureCount = boundedAuditCounter(grant.AuditFailureCount, 1, backupasset.MaxAuditRangeCount)
	}
	return nil
}

func boundedAuditCounter(current, increment, maximum int64) int64 {
	if current >= maximum || increment >= maximum-current {
		return maximum
	}
	return current + increment
}

func buildReservationRow(spec reservationSpec, now time.Time) model.BackupAssetDeliveryRequest {
	request := model.BackupAssetDeliveryRequest{
		ID: spec.requestID, GrantID: spec.grantID, Method: spec.method,
		RangeKind: string(spec.rangeValue.Kind), RangeStart: spec.rangeValue.Start,
		RangeEndExclusive: spec.rangeValue.EndExclusive, SuffixLength: spec.rangeValue.SuffixLength,
		State: string(RequestReserved), ReservedBytes: spec.reservedBytes,
		StartedAt: now, LastProgressAt: now, CreatedAt: now, UpdatedAt: now, Version: 1,
	}
	if spec.blocked {
		request.State = string(RequestBlocked)
		request.ReservedBytes = 0
		request.HTTPStatus = spec.status
		request.FailureCode = string(spec.failure)
		request.FinishedAt = &now
	}
	return request
}

func validReservationIntent(intent ReservationIntent) bool {
	if backupasset.ValidateOpaqueID(intent.RequestID) != nil || backupasset.ValidateOpaqueID(intent.GrantID) != nil ||
		(intent.Method != http.MethodGet && intent.Method != http.MethodHead) || intent.ReservedBytes < 0 ||
		!validPersistedHTTPRange(intent.Range) {
		return false
	}
	if intent.Method == http.MethodHead {
		return intent.ReservedBytes == 0
	}
	return intent.ReservedBytes >= intent.Range.Length
}

func validBlockedRequest(request BlockedRequest) bool {
	return backupasset.ValidateOpaqueID(request.RequestID) == nil && backupasset.ValidateOpaqueID(request.GrantID) == nil &&
		(request.Method == http.MethodGet || request.Method == http.MethodHead) && request.Status >= 100 && request.Status <= 599 &&
		request.FailureCode != "" && validRequestFailureCode(request.FailureCode)
}

func validFinalizeIntent(intent FinalizeIntent) bool {
	if backupasset.ValidateOpaqueID(intent.RequestID) != nil || intent.ExpectedRequestVersion <= 0 ||
		!requestFinalizable(intent.State) || intent.HTTPStatus < 100 || intent.HTTPStatus > 599 {
		return false
	}
	if intent.State == RequestSucceeded {
		return intent.HTTPStatus >= 200 && intent.HTTPStatus <= 299 && intent.FailureCode == "" &&
			intent.EvidenceKnown && intent.ProviderBytes >= 0 && intent.ResponseBytes >= 0
	}
	return intent.FailureCode != "" && validRequestFailureCode(intent.FailureCode)
}

func validPersistedHTTPRange(value HTTPRange) bool {
	switch value.Kind {
	case HTTPRangeFull:
		return value.Start == nil && value.EndExclusive == nil && value.SuffixLength == nil && value.Offset == 0 && value.Length >= 0
	case HTTPRangeNormal:
		return value.Start != nil && value.EndExclusive != nil && value.SuffixLength == nil &&
			*value.Start >= 0 && *value.EndExclusive > *value.Start && value.Offset == *value.Start &&
			value.Length == *value.EndExclusive-*value.Start
	case HTTPRangeOpenEnded:
		return value.Start != nil && value.EndExclusive == nil && value.SuffixLength == nil &&
			*value.Start >= 0 && value.Offset == *value.Start && value.Length > 0
	case HTTPRangeSuffix:
		return value.Start == nil && value.EndExclusive == nil && value.SuffixLength != nil &&
			*value.SuffixLength > 0 && value.Offset >= 0 && value.Length > 0 && value.Length <= *value.SuffixLength
	default:
		return false
	}
}

func validBudgetLimits(limits BudgetLimits) bool {
	return limits.Window > 0 && validBudgetScopeLimits(limits.Global) &&
		validBudgetScopeLimits(limits.Provider) && validBudgetScopeLimits(limits.User)
}

func validBudgetScopeLimits(limits BudgetScopeLimits) bool {
	return limits.WindowBytes > 0 && limits.WindowRequests > 0 && limits.MaxInFlight > 0
}

func validBudgetScopeKeys(keys []BudgetScopeKey) bool {
	if len(keys) != 3 || keys[0] != (BudgetScopeKey{Kind: BudgetScopeGlobal, ID: "global"}) ||
		keys[1].Kind != BudgetScopeProvider || keys[2].Kind != BudgetScopeUser {
		return false
	}
	if keys[1].ID != string(backupasset.ProviderRestic) && keys[1].ID != string(backupasset.ProviderRsync) &&
		keys[1].ID != string(backupasset.ProviderRclone) {
		return false
	}
	ownerID, err := strconv.ParseInt(keys[2].ID, 10, 64)
	return err == nil && ownerID > 0
}

func limitsForScope(limits BudgetLimits, kind BudgetScopeKind) BudgetScopeLimits {
	switch kind {
	case BudgetScopeGlobal:
		return limits.Global
	case BudgetScopeProvider:
		return limits.Provider
	case BudgetScopeUser:
		return limits.User
	default:
		return BudgetScopeLimits{}
	}
}

func grantAcceptsNewRequest(grant model.BackupAssetDeliveryGrant, now time.Time) bool {
	return grant.State == string(DeliveryActive) && now.Before(grant.IdleExpiresAt.UTC()) &&
		now.Before(grant.AbsoluteExpiresAt.UTC()) && now.Before(grant.SessionExpiresAt.UTC())
}

func refreshedIdleExpiry(grant model.BackupAssetDeliveryGrant, now time.Time) time.Time {
	if grant.IdleTTLSeconds <= 0 || grant.IdleTTLSeconds > math.MaxInt64/int64(time.Second) {
		return grant.AbsoluteExpiresAt.UTC()
	}
	candidate := now.Add(time.Duration(grant.IdleTTLSeconds) * time.Second)
	if candidate.After(grant.AbsoluteExpiresAt.UTC()) {
		return grant.AbsoluteExpiresAt.UTC()
	}
	return candidate
}

func withinByteLimit(delivered, reserved, additional, limit int64) bool {
	return delivered >= 0 && reserved >= 0 && additional >= 0 && limit >= 0 &&
		delivered <= limit && reserved <= limit-delivered && additional <= limit-delivered-reserved
}

func wouldAddOverflow(current, additional int64) bool {
	return current < 0 || additional < 0 || current > math.MaxInt64-additional
}

func finalCharge(reserved int64, intent FinalizeIntent) (int64, bool) {
	valid := intent.EvidenceKnown && intent.ProviderBytes >= 0 && intent.ResponseBytes >= 0 &&
		intent.ProviderBytes <= reserved && intent.ResponseBytes <= reserved
	if !valid {
		return reserved, false
	}
	return max(intent.ProviderBytes, intent.ResponseBytes), true
}

func boundedEvidence(reserved, evidence int64) int64 {
	if evidence < 0 || evidence > reserved {
		return 0
	}
	return evidence
}

func requestFinalizable(state RequestState) bool {
	return state == RequestSucceeded || state == RequestCanceled || state == RequestFailed || state == RequestReconciled
}

func requestTerminal(state RequestState) bool {
	return state == RequestSucceeded || state == RequestBlocked || state == RequestCanceled || state == RequestFailed || state == RequestReconciled
}

func terminalFinalization(request model.BackupAssetDeliveryRequest) Finalization {
	charge := max(request.ProviderBytes, request.ResponseBytes)
	if RequestState(request.State) == RequestReconciled {
		charge = request.ReservedBytes
	}
	return Finalization{
		RequestID: request.ID, State: RequestState(request.State),
		ChargedBytes: charge, AlreadyFinalized: true,
	}
}

func validRequestFailureCode(value RequestFailureCode) bool {
	return validRequestFailureCodes[value]
}

var validRequestFailureCodes = map[RequestFailureCode]bool{
	RequestFailureInvalidRange: true, RequestFailureRangeNotAllowed: true,
	RequestFailureIfRangeFullForbidden: true, RequestFailureRequestTooLarge: true,
	RequestFailureBudgetExhausted: true, RequestFailureCode("session_revoked"): true,
	RequestFailureCode("permission_changed"): true, RequestFailureCode("source_changed"): true,
	RequestFailureCode("lease_lost"): true, RequestFailureCode("feature_disabled"): true,
	RequestFailureClientCanceled: true, RequestFailureWriteFailed: true,
	RequestFailureSourceFailed: true, RequestFailureReconciledCrash: true,
	RequestFailureInternal: true,
}

func retryableBudgetError(err error) bool {
	if errors.Is(err, errBudgetConcurrent) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "deadlock detected") || strings.Contains(message, "could not serialize access") ||
		strings.Contains(message, "serialization failure")
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
