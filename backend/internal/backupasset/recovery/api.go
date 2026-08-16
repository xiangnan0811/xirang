package recovery

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrRecoveryAPIObjectNotFound = errors.New("recovery API object not found")
	ErrRecoveryAPIConflict       = errors.New("recovery API state conflict")
	ErrRecoveryAPIUnavailable    = errors.New("recovery API unavailable")
	ErrRecoveryAPIInvalidPage    = errors.New("invalid recovery API page")
)

const (
	recoveryAPIDefaultPageSize = 25
	recoveryAPIHardPageSize    = 100
	recoveryAPIMaxSafeInteger  = int64(9_007_199_254_740_991)
)

type APIServiceDependencies struct {
	DB    *gorm.DB
	Now   func() time.Time
	Audit RecoveryAPIAuditWriter
}

type RecoveryAPIAuditWriter interface {
	Write(context.Context, backupasset.AuditEventInput) (model.BackupAssetAuditEvent, error)
}

// APIService owns Recovery API reads and simple CAS transitions. It projects
// reviewed scalar DTOs instead of exposing GORM models containing ciphertext,
// digests, reasons, workspace locators, or internal phases.
type APIService struct {
	db    *gorm.DB
	now   func() time.Time
	audit RecoveryAPIAuditWriter
}

type RecoveryPlanView struct {
	SchemaVersion      int                  `json:"schema_version"`
	ID                 string               `json:"id"`
	State              PlanState            `json:"state"`
	Revision           string               `json:"revision"`
	RepositoryID       string               `json:"repository_id"`
	RecoveryPointID    string               `json:"recovery_point_id"`
	TargetMode         TargetMode           `json:"target_mode"`
	TargetNodeID       uint                 `json:"target_node_id"`
	TargetRootID       string               `json:"target_root_id"`
	ConflictPolicy     ConflictPolicy       `json:"conflict_policy"`
	Security           SecurityDecisionKind `json:"security_decision"`
	SelectionDigest    string               `json:"selection_digest"`
	OperationSetDigest string               `json:"operation_set_digest"`
	DeleteSetDigest    string               `json:"delete_set_digest"`
	EstimatedItems     int64                `json:"estimated_items"`
	EstimatedBytes     int64                `json:"estimated_bytes"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
}

// RecoveryPreflightImpactView is an aggregate-only public impact product.
// Exact rows and target-path digests remain private to the execution graph.
type RecoveryPreflightImpactView struct {
	CreateCount    int64 `json:"create_count"`
	OverwriteCount int64 `json:"overwrite_count"`
	SkipCount      int64 `json:"skip_count"`
	DeleteCount    int64 `json:"delete_count"`
	EstimatedItems int64 `json:"estimated_items"`
	EstimatedBytes int64 `json:"estimated_bytes"`
}

type RecoveryPreflightSecurityView struct {
	Decision              SecurityDecisionKind      `json:"decision"`
	FindingCount          int                       `json:"finding_count"`
	OverridableCategories []SecurityFindingCategory `json:"overridable_categories"`
}

// RecoveryPreflightView is the only application/API projection of a private
// preflight evaluation. It intentionally omits source/target/root revisions,
// semantic/final path digests, credentials, permits, exact rows, and binding
// material while retaining the explicitly public operation summaries.
type RecoveryPreflightView struct {
	SchemaVersion      int                           `json:"schema_version"`
	PlanID             string                        `json:"plan_id"`
	Persisted          bool                          `json:"persisted"`
	PlanRevision       string                        `json:"plan_revision,omitempty"`
	Eligible           bool                          `json:"eligible"`
	Preferred          bool                          `json:"preferred"`
	Reasons            []TargetPreflightReason       `json:"reasons"`
	PreflightID        string                        `json:"preflight_id,omitempty"`
	PreflightRevision  string                        `json:"preflight_revision,omitempty"`
	TargetMode         TargetMode                    `json:"target_mode,omitempty"`
	ConflictPolicy     ConflictPolicy                `json:"conflict_policy,omitempty"`
	OperationSetDigest string                        `json:"operation_set_digest,omitempty"`
	DeleteSetDigest    string                        `json:"delete_set_digest,omitempty"`
	Impact             RecoveryPreflightImpactView   `json:"impact"`
	Security           RecoveryPreflightSecurityView `json:"security"`
	ObservedAt         *time.Time                    `json:"observed_at,omitempty"`
	ExpiresAt          *time.Time                    `json:"expires_at,omitempty"`
}

type RecoveryJobView struct {
	SchemaVersion     int                           `json:"schema_version"`
	ID                string                        `json:"id"`
	PlanID            string                        `json:"plan_id"`
	State             JobState                      `json:"state"`
	Revision          string                        `json:"revision"`
	TargetMode        TargetMode                    `json:"target_mode"`
	TargetNodeID      uint                          `json:"target_node_id"`
	TargetRootID      string                        `json:"target_root_id"`
	EstimatedItems    int64                         `json:"estimated_items"`
	EstimatedBytes    int64                         `json:"estimated_bytes"`
	Progress          RecoveryJobProgressView       `json:"progress"`
	FailureCategory   RecoveryPublicFailureCategory `json:"failure_category,omitempty"`
	DeleteCheckpoint  *RecoveryDeleteCheckpointView `json:"delete_checkpoint,omitempty"`
	ResultSet         *RecoveryResultSetView        `json:"result_set,omitempty"`
	PlaintextDeadline *time.Time                    `json:"plaintext_deadline,omitempty"`
	CreatedAt         time.Time                     `json:"created_at"`
	UpdatedAt         time.Time                     `json:"updated_at"`
}

type RecoveryPublicFailureCategory string

const (
	RecoveryPublicFailureSourceDrift             RecoveryPublicFailureCategory = "source_drift"
	RecoveryPublicFailureVerificationMismatch    RecoveryPublicFailureCategory = "verification_mismatch"
	RecoveryPublicFailureRemoteOutcomeUnresolved RecoveryPublicFailureCategory = "remote_outcome_unresolved"
	RecoveryPublicFailurePartialWrite            RecoveryPublicFailureCategory = "partial_write"
	RecoveryPublicFailureCleanupUnavailable      RecoveryPublicFailureCategory = "cleanup_unavailable"
)

type RecoveryJobProgressView struct {
	TotalItems     int64 `json:"total_items"`
	CompletedItems int64 `json:"completed_items"`
	SucceededItems int64 `json:"succeeded_items"`
	SkippedItems   int64 `json:"skipped_items"`
	FailedItems    int64 `json:"failed_items"`
	BytesWritten   int64 `json:"bytes_written"`
}

type RecoveryDeleteCheckpointStatus string

const RecoveryDeleteCheckpointAwaitingAuthorization RecoveryDeleteCheckpointStatus = "awaiting_authorization"

type RecoveryDeleteCheckpointView struct {
	ID                   string                         `json:"id"`
	AttemptID            string                         `json:"attempt_id"`
	ExpectedPlanRevision string                         `json:"expected_plan_revision"`
	Status               RecoveryDeleteCheckpointStatus `json:"status"`
	ExpiresAt            time.Time                      `json:"expires_at"`
}

type RecoveryResultSetView struct {
	ID                string         `json:"id"`
	State             ResultSetState `json:"state"`
	PlaintextDeadline time.Time      `json:"plaintext_deadline"`
	HardDeadline      time.Time      `json:"hard_deadline"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type RecoveryPageRequest struct {
	Page     int
	PageSize int
}

type RecoveryJobItemOutcome string

const (
	RecoveryJobItemPending   RecoveryJobItemOutcome = "pending"
	RecoveryJobItemSucceeded RecoveryJobItemOutcome = "succeeded"
	RecoveryJobItemSkipped   RecoveryJobItemOutcome = "skipped"
	RecoveryJobItemFailed    RecoveryJobItemOutcome = "failed"
)

type RecoveryJobItemView struct {
	ID              string                        `json:"id"`
	Ordinal         int                           `json:"ordinal"`
	Operation       RecoveryOperationKind         `json:"operation"`
	Outcome         RecoveryJobItemOutcome        `json:"outcome"`
	EstimatedBytes  int64                         `json:"estimated_bytes"`
	BytesWritten    int64                         `json:"bytes_written"`
	VerifiedSize    int64                         `json:"verified_size"`
	FailureCategory RecoveryPublicFailureCategory `json:"failure_category,omitempty"`
	CreatedAt       time.Time                     `json:"created_at"`
	UpdatedAt       time.Time                     `json:"updated_at"`
}

type RecoveryJobItemPage struct {
	SchemaVersion int                   `json:"schema_version"`
	JobID         string                `json:"job_id"`
	Page          int                   `json:"page"`
	PageSize      int                   `json:"page_size"`
	Total         int64                 `json:"total"`
	Items         []RecoveryJobItemView `json:"items"`
}

type RecoveryResultView struct {
	ID         string             `json:"id"`
	Kind       RecoveryResultKind `json:"kind"`
	Size       int64              `json:"size"`
	ModifiedAt *time.Time         `json:"modified_at,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
}

type RecoveryResultPage struct {
	SchemaVersion int                   `json:"schema_version"`
	JobID         string                `json:"job_id"`
	ResultSet     RecoveryResultSetView `json:"result_set"`
	Page          int                   `json:"page"`
	PageSize      int                   `json:"page_size"`
	Total         int64                 `json:"total"`
	Items         []RecoveryResultView  `json:"items"`
}

type RecoveryPlanMutationRequest struct {
	RequesterID      uint
	PlanID           string
	ExpectedRevision uint64
}

type RecoveryResultCleanupRequest struct {
	RequesterID         uint
	JobID               string
	ExpectedJobRevision uint64
}

type RecoveryResultCleanupView struct {
	SchemaVersion int            `json:"schema_version"`
	JobID         string         `json:"job_id"`
	ResultSetID   string         `json:"result_set_id"`
	State         ResultSetState `json:"state"`
	ScheduledAt   time.Time      `json:"scheduled_at"`
}

// CreatePlanIntentRequest is the public application command. It contains only
// opaque source selection and safe target intent; all revisions, digests,
// operation products, security products, estimates, locators, and expiry are
// materialized inside Recovery.
type CreatePlanIntentRequest struct {
	RequesterID         uint
	Endpoint            string
	IdempotencyKey      string
	RepositoryID        string
	RecoveryPointID     string
	CatalogGenerationID string
	EntryIDs            []string
	TargetMode          TargetMode
	TargetNodeID        uint
	TargetRootID        string
	ConflictPolicy      ConflictPolicy
}

// RecoveryPreflightRequest carries only the owner-scoped plan identity and
// opaque CAS revision. Private observation inputs and permits are reconstructed
// by Recovery's application owner.
type RecoveryPreflightRequest struct {
	RequesterID          uint
	PlanID               string
	ExpectedPlanRevision uint64
}

func ProjectPreflightResult(result PreflightPersistenceResult) (RecoveryPreflightView, error) {
	if !validOpaqueID(result.PlanID) || !validRecoveryAPISecurityDecision(result.Evaluation.Security.Decision.Kind) ||
		result.Evaluation.Security.FindingCount < 0 {
		return RecoveryPreflightView{}, ErrInvalidTargetPreflight
	}
	reasons := append([]TargetPreflightReason(nil), result.Evaluation.Reasons...)
	for _, reason := range reasons {
		if !validRecoveryAPIPreflightReason(reason) {
			return RecoveryPreflightView{}, ErrInvalidTargetPreflight
		}
	}
	categories := []SecurityFindingCategory{}
	if candidate := result.Evaluation.Security.OverrideCandidate; candidate != nil {
		categories = append(categories, candidate.Categories...)
		for _, category := range categories {
			if !category.known() {
				return RecoveryPreflightView{}, ErrInvalidTargetPreflight
			}
		}
	}
	view := RecoveryPreflightView{
		SchemaVersion: 1, PlanID: result.PlanID, Persisted: result.Persisted,
		Eligible: result.Evaluation.Eligible, Preferred: result.Evaluation.Preferred,
		Reasons: reasons,
		Security: RecoveryPreflightSecurityView{
			Decision:              result.Evaluation.Security.Decision.Kind,
			FindingCount:          result.Evaluation.Security.FindingCount,
			OverridableCategories: categories,
		},
	}
	if !result.Persisted {
		if result.PlanTransitionRevision != 0 || result.Evaluation.Eligible || len(reasons) == 0 {
			return RecoveryPreflightView{}, ErrInvalidTargetPreflight
		}
		return view, nil
	}
	snapshot := result.Evaluation.Snapshot
	if result.PlanTransitionRevision == 0 || !validOpaqueID(snapshot.ID) ||
		!validOpaqueRevision(snapshot.Revision) || snapshot.TargetMode.Validate() != nil ||
		snapshot.ConflictPolicy.Validate() != nil || !validDigest(snapshot.OperationSetDigest) ||
		!validDigest(snapshot.DeleteSetDigest) || snapshot.ObservedAt.IsZero() || snapshot.ExpiresAt.IsZero() ||
		!snapshot.ExpiresAt.After(snapshot.ObservedAt) || snapshot.Impact.CreateCount < 0 ||
		snapshot.Impact.OverwriteCount < 0 || snapshot.Impact.SkipCount < 0 || snapshot.Impact.DeleteCount < 0 ||
		snapshot.Impact.EstimatedItems < 0 || snapshot.Impact.EstimatedBytes < 0 {
		return RecoveryPreflightView{}, ErrInvalidTargetPreflight
	}
	observedAt, expiresAt := snapshot.ObservedAt.UTC(), snapshot.ExpiresAt.UTC()
	view.PlanRevision = strconv.FormatUint(result.PlanTransitionRevision, 10)
	view.PreflightID, view.PreflightRevision = snapshot.ID, snapshot.Revision
	view.TargetMode, view.ConflictPolicy = snapshot.TargetMode, snapshot.ConflictPolicy
	view.OperationSetDigest, view.DeleteSetDigest = snapshot.OperationSetDigest, snapshot.DeleteSetDigest
	view.Impact = RecoveryPreflightImpactView{
		CreateCount: snapshot.Impact.CreateCount, OverwriteCount: snapshot.Impact.OverwriteCount,
		SkipCount: snapshot.Impact.SkipCount, DeleteCount: snapshot.Impact.DeleteCount,
		EstimatedItems: snapshot.Impact.EstimatedItems, EstimatedBytes: snapshot.Impact.EstimatedBytes,
	}
	view.ObservedAt, view.ExpiresAt = &observedAt, &expiresAt
	return view, nil
}

func NewAPIService(dependencies APIServiceDependencies) (*APIService, error) {
	if dependencies.DB == nil {
		return nil, ErrRecoveryAPIUnavailable
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &APIService{db: dependencies.DB, now: dependencies.Now, audit: dependencies.Audit}, nil
}

func (service *APIService) GetPlan(
	ctx context.Context,
	requesterID uint,
	planID string,
) (RecoveryPlanView, error) {
	view, err := service.getPlan(ctx, requesterID, planID)
	if err == nil {
		service.writeAudit(ctx, backupasset.AuditEventInput{
			Actor: backupasset.AuditActor{UserID: requesterID}, Action: backupasset.AuditActionRecoveryPlan,
			Fields: map[backupasset.AuditField]any{
				backupasset.AuditFieldStage: "read", backupasset.AuditFieldStatus: string(view.State),
				backupasset.AuditFieldCorrelationID: planID,
			},
		})
	}
	return view, err
}

func (service *APIService) getPlan(
	ctx context.Context,
	requesterID uint,
	planID string,
) (RecoveryPlanView, error) {
	if service == nil || service.db == nil || requesterID == 0 || !validOpaqueID(planID) {
		return RecoveryPlanView{}, ErrRecoveryAPIObjectNotFound
	}
	var row recoveryAPIPlanRow
	loaded := service.db.WithContext(nonNilRecoveryAPIContext(ctx)).
		Table((model.BackupAssetRecoveryPlan{}).TableName()).
		Select(`id, state, transition_revision, repository_id, recovery_point_id,
			target_mode, target_node_id, target_root_id, conflict_policy, security_decision,
			selection_digest, operation_set_digest, delete_set_digest,
			estimated_items, estimated_bytes, created_at, updated_at`).
		Where("id = ? AND requester_id = ?", planID, requesterID).Limit(1).Find(&row)
	if loaded.Error != nil {
		return RecoveryPlanView{}, recoveryAPIError(ctx)
	}
	if loaded.RowsAffected != 1 || !row.valid() {
		return RecoveryPlanView{}, ErrRecoveryAPIObjectNotFound
	}
	return row.view(), nil
}

func (service *APIService) GetJob(
	ctx context.Context,
	requesterID uint,
	jobID string,
) (RecoveryJobView, error) {
	view, err := service.getJob(ctx, requesterID, jobID)
	if err != nil {
		return RecoveryJobView{}, err
	}
	service.writeAudit(ctx, backupasset.AuditEventInput{
		Actor: backupasset.AuditActor{UserID: requesterID}, Action: backupasset.AuditActionRecoveryVerify,
		RecoveryJobID: jobID,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldStage: "read", backupasset.AuditFieldStatus: string(view.State),
		},
	})
	return view, nil
}

// ProjectJob returns the same owner-scoped safe view as GetJob without
// projecting a read audit. Mutation owners use it after they have emitted the
// purpose-exact transition audit.
func (service *APIService) ProjectJob(
	ctx context.Context,
	requesterID uint,
	jobID string,
) (RecoveryJobView, error) {
	return service.getJob(ctx, requesterID, jobID)
}

func (service *APIService) getJob(
	ctx context.Context,
	requesterID uint,
	jobID string,
) (RecoveryJobView, error) {
	if service == nil || service.db == nil || requesterID == 0 || !validOpaqueID(jobID) {
		return RecoveryJobView{}, ErrRecoveryAPIObjectNotFound
	}
	ctx = nonNilRecoveryAPIContext(ctx)
	var view RecoveryJobView
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := loadOwnedRecoveryAPIJob(tx, requesterID, jobID)
		if err != nil {
			return err
		}
		projected, err := service.projectRecoveryAPIJobTx(tx, row)
		if err != nil {
			return err
		}
		view = projected
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryAPIObjectNotFound) {
			return RecoveryJobView{}, ErrRecoveryAPIObjectNotFound
		}
		return RecoveryJobView{}, recoveryAPIError(ctx)
	}
	return view, nil
}

func (service *APIService) ListJobItems(
	ctx context.Context,
	requesterID uint,
	jobID string,
	request RecoveryPageRequest,
) (RecoveryJobItemPage, error) {
	page, pageSize, offset, err := normalizeRecoveryAPIPage(request)
	if err != nil {
		return RecoveryJobItemPage{}, err
	}
	if service == nil || service.db == nil || requesterID == 0 || !validOpaqueID(jobID) {
		return RecoveryJobItemPage{}, ErrRecoveryAPIObjectNotFound
	}
	ctx = nonNilRecoveryAPIContext(ctx)
	result := RecoveryJobItemPage{SchemaVersion: 1, JobID: jobID, Page: page, PageSize: pageSize, Items: []RecoveryJobItemView{}}
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, loadErr := loadOwnedRecoveryAPIJob(tx, requesterID, jobID)
		if loadErr != nil {
			return loadErr
		}
		whole, _, loadErr := loadValidatedRecoveryAPIJobProduct(tx.WithContext(ctx), job)
		if loadErr != nil {
			return loadErr
		}
		result.Total = int64(len(whole))
		var rows []recoveryAPIJobItemRow
		loaded := tx.WithContext(ctx).Table((model.BackupAssetRecoveryJobItem{}).TableName()).
			Select(`id, ordinal, operation_kind, outcome, estimated_bytes, bytes_written,
				verified_size, failure_category, created_at, updated_at`).
			Where("job_id = ?", jobID).Order("ordinal ASC").Order("id ASC").
			Limit(pageSize).Offset(offset).Find(&rows)
		if loaded.Error != nil {
			return loaded.Error
		}
		if err := validateRecoveryAPIJobItemRows(rows, offset); err != nil {
			return err
		}
		for _, row := range rows {
			item, mapErr := row.view()
			if mapErr != nil {
				return mapErr
			}
			result.Items = append(result.Items, item)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryAPIObjectNotFound) {
			return RecoveryJobItemPage{}, ErrRecoveryAPIObjectNotFound
		}
		return RecoveryJobItemPage{}, recoveryAPIError(ctx)
	}
	return result, nil
}

func (service *APIService) ListPublishedResults(
	ctx context.Context,
	requesterID uint,
	jobID string,
	request RecoveryPageRequest,
) (RecoveryResultPage, error) {
	page, pageSize, offset, err := normalizeRecoveryAPIPage(request)
	if err != nil {
		return RecoveryResultPage{}, err
	}
	if service == nil || service.db == nil || service.now == nil || requesterID == 0 || !validOpaqueID(jobID) {
		return RecoveryResultPage{}, ErrRecoveryAPIObjectNotFound
	}
	ctx = nonNilRecoveryAPIContext(ctx)
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryResultPage{}, ErrRecoveryAPIUnavailable
	}
	result := RecoveryResultPage{SchemaVersion: 1, JobID: jobID, Page: page, PageSize: pageSize, Items: []RecoveryResultView{}}
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, loadErr := loadOwnedRecoveryAPIJob(tx, requesterID, jobID)
		if loadErr != nil {
			return loadErr
		}
		if _, _, validateErr := loadValidatedRecoveryAPIJobProduct(tx.WithContext(ctx), job); validateErr != nil {
			return validateErr
		}
		if TargetMode(job.TargetMode) != TargetModeIsolated ||
			(JobState(job.State) != JobStateSucceeded && JobState(job.State) != JobStateDegraded) {
			return ErrRecoveryAPIConflict
		}
		set, setErr := loadRecoveryAPIResultSet(tx, jobID)
		if setErr != nil {
			return setErr
		}
		if ResultSetState(set.State) != ResultSetStateReady || !set.PlaintextDeadline.UTC().After(now) {
			return ErrRecoveryAPIConflict
		}
		result.ResultSet = set.view()
		var whole []recoveryAPIResultRow
		loaded := tx.WithContext(ctx).Table((model.BackupAssetRecoveryResult{}).TableName()).
			Select("id, result_kind, size, modified_at, created_at").
			Where("job_id = ? AND result_set_id = ?", jobID, set.ID).
			Order("created_at ASC").Order("id ASC").Limit(exactSelectionMaxItems + 1).Find(&whole)
		if loaded.Error != nil {
			return loaded.Error
		}
		if len(whole) > exactSelectionMaxItems {
			return ErrRecoveryAPIUnavailable
		}
		resolver, resolverErr := NewRecoveryResultResolver(RecoveryResultResolverDependencies{
			DB: tx, Now: func() time.Time { return now },
		})
		if resolverErr != nil {
			return ErrRecoveryAPIUnavailable
		}
		for _, row := range whole {
			if _, mapErr := row.view(); mapErr != nil {
				return mapErr
			}
			if _, resolveErr := resolver.Resolve(ctx, ResolveRecoveryResultRequest{
				RequesterID: requesterID, RecoveryJobID: jobID, ResultID: row.ID,
			}); resolveErr != nil {
				return ErrRecoveryAPIUnavailable
			}
		}
		result.Total = int64(len(whole))
		var rows []recoveryAPIResultRow
		loaded = tx.WithContext(ctx).Table((model.BackupAssetRecoveryResult{}).TableName()).
			Select("id, result_kind, size, modified_at, created_at").
			Where("job_id = ? AND result_set_id = ?", jobID, set.ID).
			Order("created_at ASC").Order("id ASC").Limit(pageSize).Offset(offset).Find(&rows)
		if loaded.Error != nil {
			return loaded.Error
		}
		for _, row := range rows {
			item, mapErr := row.view()
			if mapErr != nil {
				return mapErr
			}
			result.Items = append(result.Items, item)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrRecoveryAPIObjectNotFound):
			return RecoveryResultPage{}, ErrRecoveryAPIObjectNotFound
		case errors.Is(err, ErrRecoveryAPIConflict):
			return RecoveryResultPage{}, ErrRecoveryAPIConflict
		default:
			return RecoveryResultPage{}, recoveryAPIError(ctx)
		}
	}
	return result, nil
}

func (service *APIService) CancelPlan(
	ctx context.Context,
	request RecoveryPlanMutationRequest,
) (RecoveryPlanView, error) {
	if service == nil || service.db == nil || service.now == nil || request.RequesterID == 0 ||
		!validOpaqueID(request.PlanID) || request.ExpectedRevision == 0 {
		return RecoveryPlanView{}, ErrRecoveryAPIObjectNotFound
	}
	ctx = nonNilRecoveryAPIContext(ctx)
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryPlanView{}, ErrRecoveryAPIUnavailable
	}
	var hidden bool
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct {
			RequesterID        uint
			State              string
			TransitionRevision uint64
		}
		loaded := tx.WithContext(ctx).Table((model.BackupAssetRecoveryPlan{}).TableName()).
			Select("requester_id, state, transition_revision").
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", request.PlanID).Limit(1).Find(&row)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || row.RequesterID != request.RequesterID {
			hidden = true
			return ErrRecoveryAPIObjectNotFound
		}
		state := PlanState(row.State)
		if row.TransitionRevision != request.ExpectedRevision ||
			!state.CanTransitionTo(PlanStateCanceled, PlanTransitionGuard{}) {
			return ErrRecoveryAPIConflict
		}
		updated := tx.WithContext(ctx).Table((model.BackupAssetRecoveryPlan{}).TableName()).
			Where("id = ? AND requester_id = ? AND state = ? AND transition_revision = ?",
				request.PlanID, request.RequesterID, row.State, request.ExpectedRevision).
			Updates(map[string]any{
				"state": PlanStateCanceled, "transition_revision": request.ExpectedRevision + 1, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryAPIConflict
		}
		return nil
	})
	if err != nil {
		if hidden || errors.Is(err, ErrRecoveryAPIObjectNotFound) {
			return RecoveryPlanView{}, ErrRecoveryAPIObjectNotFound
		}
		if errors.Is(err, ErrRecoveryAPIConflict) {
			return RecoveryPlanView{}, ErrRecoveryAPIConflict
		}
		return RecoveryPlanView{}, recoveryAPIError(ctx)
	}
	view, err := service.getPlan(ctx, request.RequesterID, request.PlanID)
	if err != nil {
		return RecoveryPlanView{}, err
	}
	service.writeAudit(ctx, backupasset.AuditEventInput{
		Actor: backupasset.AuditActor{UserID: request.RequesterID}, Action: backupasset.AuditActionRecoveryCancel,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldStage: "plan", backupasset.AuditFieldStatus: string(view.State),
			backupasset.AuditFieldCorrelationID: request.PlanID,
		},
	})
	return view, nil
}

// RequestResultCleanup shortens only the effective ResultSet delivery window;
// the immutable job plaintext anchor remains untouched. The existing cleanup
// owner then performs revoke, drain, fenced validation, deletion, and evidence.
func (service *APIService) RequestResultCleanup(
	ctx context.Context,
	request RecoveryResultCleanupRequest,
) (RecoveryResultCleanupView, error) {
	if service == nil || service.db == nil || service.now == nil || request.RequesterID == 0 ||
		!validOpaqueID(request.JobID) || request.ExpectedJobRevision == 0 {
		return RecoveryResultCleanupView{}, ErrRecoveryAPIObjectNotFound
	}
	ctx = nonNilRecoveryAPIContext(ctx)
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryResultCleanupView{}, ErrRecoveryAPIUnavailable
	}
	var result RecoveryResultCleanupView
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job struct {
			PlanID             string
			State              string
			TransitionRevision uint64
		}
		loaded := tx.WithContext(ctx).Table((model.BackupAssetRecoveryJob{}).TableName()).
			Select("plan_id, state, transition_revision").
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", request.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 {
			return ErrRecoveryAPIObjectNotFound
		}
		var owner struct{ RequesterID uint }
		loaded = tx.WithContext(ctx).Table((model.BackupAssetRecoveryPlan{}).TableName()).
			Select("requester_id").Where("id = ?", job.PlanID).Limit(1).Find(&owner)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || owner.RequesterID != request.RequesterID {
			return ErrRecoveryAPIObjectNotFound
		}
		if job.TransitionRevision != request.ExpectedJobRevision ||
			(JobState(job.State) != JobStateSucceeded && JobState(job.State) != JobStateDegraded) {
			return ErrRecoveryAPIConflict
		}
		var set recoveryAPIResultSetRow
		loaded = tx.WithContext(ctx).Table((model.BackupAssetRecoveryResultSet{}).TableName()).
			Select(`id, state, plaintext_deadline, hard_deadline, cleanup_phase, cleanup_owner,
				cleanup_lease_expires_at, cleanup_fence, node_lease_id, node_fence, cleanup_attempt,
				created_at, updated_at`).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", request.JobID).Limit(1).Find(&set)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !set.valid() || ResultSetState(set.State) != ResultSetStateReady ||
			set.PlaintextDeadline.IsZero() || !set.PlaintextDeadline.UTC().After(now) {
			return ErrRecoveryAPIConflict
		}
		updated := tx.WithContext(ctx).Table((model.BackupAssetRecoveryResultSet{}).TableName()).
			Where(`id = ? AND job_id = ? AND state = ? AND plaintext_deadline = ?
				AND cleanup_phase = ? AND cleanup_owner = '' AND cleanup_lease_expires_at IS NULL
				AND cleanup_fence = 0 AND node_lease_id IS NULL AND node_fence = 0 AND cleanup_attempt = 0`,
				set.ID, request.JobID, ResultSetStateReady, set.PlaintextDeadline, CleanupPhaseClaimed).
			Updates(map[string]any{"plaintext_deadline": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryAPIConflict
		}
		result = RecoveryResultCleanupView{
			SchemaVersion: 1, JobID: request.JobID, ResultSetID: set.ID,
			State: ResultSetStateReady, ScheduledAt: now,
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrRecoveryAPIObjectNotFound):
			return RecoveryResultCleanupView{}, ErrRecoveryAPIObjectNotFound
		case errors.Is(err, ErrRecoveryAPIConflict):
			return RecoveryResultCleanupView{}, ErrRecoveryAPIConflict
		default:
			return RecoveryResultCleanupView{}, recoveryAPIError(ctx)
		}
	}
	service.writeAudit(ctx, backupasset.AuditEventInput{
		Actor: backupasset.AuditActor{UserID: request.RequesterID}, Action: backupasset.AuditActionRecoveryCleanup,
		RecoveryJobID: request.JobID,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldStage: "request", backupasset.AuditFieldStatus: string(result.State),
		},
	})
	return result, nil
}

func (service *APIService) writeAudit(ctx context.Context, input backupasset.AuditEventInput) {
	if service == nil || service.audit == nil {
		return
	}
	auditCtx, cancel := context.WithTimeout(
		context.WithoutCancel(nonNilRecoveryAPIContext(ctx)), authorizationAuditTimeout,
	)
	defer cancel()
	_, _ = service.audit.Write(auditCtx, input)
}

type recoveryAPIPlanRow struct {
	ID                 string
	State              string
	TransitionRevision uint64
	RepositoryID       string
	RecoveryPointID    string
	TargetMode         string
	TargetNodeID       uint
	TargetRootID       string
	ConflictPolicy     string
	SecurityDecision   string
	SelectionDigest    string
	OperationSetDigest string
	DeleteSetDigest    string
	EstimatedItems     int64
	EstimatedBytes     int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (row recoveryAPIPlanRow) valid() bool {
	return validOpaqueID(row.ID) && PlanState(row.State).Valid() && row.TransitionRevision > 0 &&
		validOpaqueID(row.RepositoryID) && validOpaqueID(row.RecoveryPointID) &&
		TargetMode(row.TargetMode).Validate() == nil && row.TargetNodeID > 0 &&
		uint64(row.TargetNodeID) <= uint64(recoveryAPIMaxSafeInteger) &&
		settingsTargetRootIDValid(row.TargetRootID) && ConflictPolicy(row.ConflictPolicy).Validate() == nil &&
		validRecoveryAPISecurityDecision(SecurityDecisionKind(row.SecurityDecision)) &&
		validDigest(row.SelectionDigest) && validDigest(row.OperationSetDigest) && validDigest(row.DeleteSetDigest) &&
		row.EstimatedItems >= 0 && row.EstimatedItems <= recoveryAPIMaxSafeInteger &&
		row.EstimatedBytes >= 0 && row.EstimatedBytes <= recoveryAPIMaxSafeInteger &&
		!row.CreatedAt.IsZero() && !row.UpdatedAt.IsZero() && !row.UpdatedAt.Before(row.CreatedAt)
}

func (row recoveryAPIPlanRow) view() RecoveryPlanView {
	return RecoveryPlanView{
		SchemaVersion: 1, ID: row.ID, State: PlanState(row.State),
		Revision: strconv.FormatUint(row.TransitionRevision, 10), RepositoryID: row.RepositoryID,
		RecoveryPointID: row.RecoveryPointID, TargetMode: TargetMode(row.TargetMode),
		TargetNodeID: row.TargetNodeID, TargetRootID: row.TargetRootID,
		ConflictPolicy: ConflictPolicy(row.ConflictPolicy), Security: SecurityDecisionKind(row.SecurityDecision),
		SelectionDigest: row.SelectionDigest, OperationSetDigest: row.OperationSetDigest,
		DeleteSetDigest: row.DeleteSetDigest,
		EstimatedItems:  row.EstimatedItems, EstimatedBytes: row.EstimatedBytes,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
}

type recoveryAPIJobRow struct {
	ID                     string
	PlanID                 string
	PlanTransitionRevision uint64
	PlanConflictPolicy     string
	State                  string
	FailureCategory        string
	TransitionRevision     uint64
	TargetMode             string
	TargetNodeID           uint
	TargetRootID           string
	EstimatedItems         int64
	EstimatedBytes         int64
	PlaintextDeadline      *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func (row recoveryAPIJobRow) valid() bool {
	return validOpaqueID(row.ID) && validOpaqueID(row.PlanID) && JobState(row.State).Valid() &&
		row.PlanTransitionRevision > 0 && ConflictPolicy(row.PlanConflictPolicy).Validate() == nil &&
		row.TransitionRevision > 0 && TargetMode(row.TargetMode).Validate() == nil && row.TargetNodeID > 0 &&
		uint64(row.TargetNodeID) <= uint64(recoveryAPIMaxSafeInteger) && settingsTargetRootIDValid(row.TargetRootID) &&
		row.EstimatedItems >= 0 && row.EstimatedItems <= recoveryAPIMaxSafeInteger &&
		row.EstimatedBytes >= 0 && row.EstimatedBytes <= recoveryAPIMaxSafeInteger &&
		!row.CreatedAt.IsZero() && !row.UpdatedAt.IsZero() && !row.UpdatedAt.Before(row.CreatedAt)
}

func (row recoveryAPIJobRow) view() RecoveryJobView {
	deadline := row.PlaintextDeadline
	if deadline != nil {
		value := deadline.UTC()
		deadline = &value
	}
	return RecoveryJobView{
		SchemaVersion: 1, ID: row.ID, PlanID: row.PlanID, State: JobState(row.State),
		Revision: strconv.FormatUint(row.TransitionRevision, 10), TargetMode: TargetMode(row.TargetMode),
		TargetNodeID: row.TargetNodeID, TargetRootID: row.TargetRootID,
		EstimatedItems: row.EstimatedItems, EstimatedBytes: row.EstimatedBytes,
		FailureCategory:   mustRecoveryPublicFailureCategory(row.FailureCategory),
		PlaintextDeadline: deadline, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
}

func loadOwnedRecoveryAPIJob(tx *gorm.DB, requesterID uint, jobID string) (recoveryAPIJobRow, error) {
	if tx == nil || requesterID == 0 || !validOpaqueID(jobID) {
		return recoveryAPIJobRow{}, ErrRecoveryAPIObjectNotFound
	}
	var row recoveryAPIJobRow
	loaded := tx.Table((model.BackupAssetRecoveryJob{}).TableName()+" AS jobs").
		Select(`jobs.id, jobs.plan_id, plans.transition_revision AS plan_transition_revision,
			plans.conflict_policy AS plan_conflict_policy, jobs.state, jobs.failure_category,
			jobs.transition_revision, jobs.target_mode, jobs.target_node_id, jobs.target_root_id,
			jobs.estimated_items, jobs.estimated_bytes, jobs.plaintext_deadline,
			jobs.created_at, jobs.updated_at`).
		Joins("JOIN "+(model.BackupAssetRecoveryPlan{}).TableName()+" AS plans ON plans.id = jobs.plan_id").
		Where("jobs.id = ? AND plans.requester_id = ?", jobID, requesterID).Limit(1).Find(&row)
	if loaded.Error != nil {
		return recoveryAPIJobRow{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !row.valid() {
		return recoveryAPIJobRow{}, ErrRecoveryAPIObjectNotFound
	}
	if row.FailureCategory != "" {
		if _, ok := recoveryPublicFailureCategory(row.FailureCategory); !ok {
			return recoveryAPIJobRow{}, ErrRecoveryAPIUnavailable
		}
	}
	return row, nil
}

func (service *APIService) projectRecoveryAPIJobTx(
	tx *gorm.DB,
	row recoveryAPIJobRow,
) (RecoveryJobView, error) {
	view := row.view()
	_, progress, err := loadValidatedRecoveryAPIJobProduct(tx, row)
	if err != nil {
		return RecoveryJobView{}, err
	}
	view.Progress = progress
	checkpoint, err := service.projectRecoveryAPIDeleteCheckpointTx(tx, row)
	if err != nil {
		return RecoveryJobView{}, err
	}
	view.DeleteCheckpoint = checkpoint
	set, err := loadOptionalRecoveryAPIResultSet(tx, row.ID)
	if err != nil {
		return RecoveryJobView{}, err
	}
	if set != nil {
		if TargetMode(row.TargetMode) != TargetModeIsolated ||
			(JobState(row.State) != JobStateSucceeded && JobState(row.State) != JobStateDegraded) {
			return RecoveryJobView{}, ErrRecoveryAPIUnavailable
		}
		value := set.view()
		view.ResultSet = &value
	}
	return view, nil
}

func (service *APIService) projectRecoveryAPIDeleteCheckpointTx(
	tx *gorm.DB,
	job recoveryAPIJobRow,
) (*RecoveryDeleteCheckpointView, error) {
	if service == nil || service.now == nil {
		return nil, ErrRecoveryAPIUnavailable
	}
	var attempts []model.BackupAssetRecoveryAttempt
	now := service.now().UTC()
	loaded := tx.Where("job_id = ? AND state = ? AND lease_expires_at > ?", job.ID, AttemptStateRunning, now).
		Order("created_at DESC").Order("id DESC").Limit(2).Find(&attempts)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	if len(attempts) == 0 {
		return nil, nil
	}
	if len(attempts) != 1 || !validOpaqueID(attempts[0].ID) || attempts[0].Fence == 0 ||
		!attempts[0].MutationArmed || attempts[0].LeaseExpiresAt == nil ||
		!attempts[0].LeaseExpiresAt.UTC().After(now) {
		return nil, ErrRecoveryAPIUnavailable
	}
	attempt := attempts[0]
	ctx := tx.Statement.Context
	var durableJob model.BackupAssetRecoveryJob
	loaded = tx.WithContext(ctx).Where("id = ?", job.ID).Limit(1).Find(&durableJob)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	if loaded.RowsAffected != 1 || durableJob.PlanID != job.PlanID ||
		durableJob.TransitionRevision != job.TransitionRevision {
		return nil, ErrRecoveryAPIUnavailable
	}
	var plan model.BackupAssetRecoveryPlan
	loaded = tx.WithContext(ctx).Where("id = ?", durableJob.PlanID).Limit(1).Find(&plan)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	if loaded.RowsAffected != 1 || plan.RequesterID == 0 ||
		ConflictPolicy(plan.ConflictPolicy) != ConflictExactMirror ||
		TargetMode(plan.TargetMode) != TargetModeInPlace {
		return nil, ErrRecoveryAPIUnavailable
	}
	var preflight model.BackupAssetRecoveryPreflight
	loaded = tx.WithContext(ctx).Where("id = ? AND plan_id = ?", durableJob.PreflightID, plan.ID).Limit(1).Find(&preflight)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return nil, ErrRecoveryAPIUnavailable
	}
	var nodeLeases []model.BackupAssetRecoveryNodeLease
	loaded = tx.WithContext(ctx).
		Where("job_id = ? AND attempt_id = ? AND state = ?", job.ID, attempt.ID, "active").Limit(2).Find(&nodeLeases)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	var sourceLeases []model.RecoveryPointLease
	loaded = tx.WithContext(ctx).
		Where("holder_type = ? AND owner_id = ? AND attempt_id = ? AND status = ?",
			backupasset.LeaseHolderRecoveryJob, job.ID, attempt.ID, backupasset.LeaseActive).Limit(2).Find(&sourceLeases)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	if len(nodeLeases) != 1 || len(sourceLeases) != 1 || attempt.LeaseExpiresAt == nil ||
		nodeLeases[0].AttemptID == nil || *nodeLeases[0].AttemptID != attempt.ID ||
		nodeLeases[0].OwnerID != attempt.OwnerID || nodeLeases[0].Fence != attempt.Fence {
		return nil, ErrRecoveryAPIUnavailable
	}
	claim := RecoveryWorkerClaim{
		JobID: job.ID, AttemptID: attempt.ID, NodeLeaseID: nodeLeases[0].ID, WorkerID: attempt.OwnerID,
		AttemptFence: attempt.Fence, NodeFence: nodeLeases[0].Fence,
		TransitionRevision: durableJob.TransitionRevision, LeaseExpiresAt: attempt.LeaseExpiresAt.UTC(),
		AbsoluteDeadline: sourceLeases[0].AbsoluteDeadline.UTC(), SourceFence: recoverySourceFence(sourceLeases[0]),
	}
	if !validRecoveryWorkerClaim(claim) || !nodeLeases[0].LeaseExpiresAt.UTC().After(now) ||
		!sourceLeases[0].LeaseExpiresAt.UTC().After(now) {
		return nil, ErrRecoveryAPIUnavailable
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	loaded = tx.WithContext(ctx).Where("job_id = ?", job.ID).Order("sequence ASC").Find(&checkpoints)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	if len(checkpoints) == 0 {
		return nil, nil
	}
	operations, err := loadInPlaceOrdinaryCheckpointOperationsTx(ctx, tx, plan, preflight, durableJob)
	if err != nil {
		return nil, ErrRecoveryAPIUnavailable
	}
	required, hasRequired, _, err := validateInPlaceOrdinaryCheckpointHistory(
		plan, durableJob, claim, checkpoints, operations, now,
	)
	if err != nil {
		return nil, ErrRecoveryAPIUnavailable
	}
	last := checkpoints[len(checkpoints)-1]
	if !hasRequired || CheckpointPhase(last.Phase) != CheckpointPhaseDeleteAuthorityRequired {
		return nil, nil
	}
	if required.ID != last.ID || required.AttemptID != attempt.ID || required.AttemptFence != attempt.Fence ||
		required.NodeFence != nodeLeases[0].Fence || required.DeleteAuthorityExpiresAt == nil ||
		!required.DeleteAuthorityExpiresAt.UTC().After(now) {
		return nil, ErrRecoveryAPIUnavailable
	}
	return &RecoveryDeleteCheckpointView{
		ID: required.ID, AttemptID: attempt.ID,
		ExpectedPlanRevision: strconv.FormatUint(job.PlanTransitionRevision, 10),
		Status:               RecoveryDeleteCheckpointAwaitingAuthorization,
		ExpiresAt:            required.DeleteAuthorityExpiresAt.UTC(),
	}, nil
}

type recoveryAPIJobItemRow struct {
	ID              string
	Ordinal         int
	OperationKind   string
	Outcome         string
	EstimatedBytes  int64
	BytesWritten    int64
	VerifiedSize    int64
	FailureCategory string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (row recoveryAPIJobItemRow) view() (RecoveryJobItemView, error) {
	if !validOpaqueID(row.ID) || row.Ordinal < 0 || row.EstimatedBytes < 0 ||
		row.EstimatedBytes > recoveryAPIMaxSafeInteger || row.BytesWritten < 0 ||
		row.BytesWritten > recoveryAPIMaxSafeInteger || row.BytesWritten > row.EstimatedBytes || row.VerifiedSize < 0 ||
		row.VerifiedSize > recoveryAPIMaxSafeInteger || row.CreatedAt.IsZero() ||
		row.UpdatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) {
		return RecoveryJobItemView{}, ErrRecoveryAPIUnavailable
	}
	operation := RecoveryOperationKind(row.OperationKind)
	if operation != RecoveryOperationCreate && operation != RecoveryOperationOverwrite &&
		operation != RecoveryOperationSkip && operation != RecoveryOperationDelete {
		return RecoveryJobItemView{}, ErrRecoveryAPIUnavailable
	}
	outcome := RecoveryJobItemOutcome(row.Outcome)
	failure := RecoveryPublicFailureCategory("")
	switch row.Outcome {
	case "":
		outcome = RecoveryJobItemPending
		if row.FailureCategory != "" {
			return RecoveryJobItemView{}, ErrRecoveryAPIUnavailable
		}
	case string(RecoveryJobItemSucceeded), string(RecoveryJobItemSkipped):
		if row.FailureCategory != "" {
			return RecoveryJobItemView{}, ErrRecoveryAPIUnavailable
		}
	case string(RecoveryJobItemFailed):
		var ok bool
		failure, ok = recoveryPublicFailureCategory(row.FailureCategory)
		if !ok {
			return RecoveryJobItemView{}, ErrRecoveryAPIUnavailable
		}
	default:
		return RecoveryJobItemView{}, ErrRecoveryAPIUnavailable
	}
	return RecoveryJobItemView{
		ID: row.ID, Ordinal: row.Ordinal, Operation: operation, Outcome: outcome,
		EstimatedBytes: row.EstimatedBytes, BytesWritten: row.BytesWritten, VerifiedSize: row.VerifiedSize,
		FailureCategory: failure, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}, nil
}

func validateRecoveryAPIJobItemRows(rows []recoveryAPIJobItemRow, firstOrdinal int) error {
	if len(rows) > exactSelectionMaxItems {
		return ErrRecoveryAPIUnavailable
	}
	var estimatedTotal, writtenTotal int64
	for index, row := range rows {
		item, err := row.view()
		if err != nil || row.Ordinal != firstOrdinal+index {
			return ErrRecoveryAPIUnavailable
		}
		switch item.Outcome {
		case RecoveryJobItemPending:
			if item.BytesWritten != 0 || item.VerifiedSize != 0 {
				return ErrRecoveryAPIUnavailable
			}
		case RecoveryJobItemSkipped:
			if item.Operation != RecoveryOperationSkip || item.BytesWritten != 0 {
				return ErrRecoveryAPIUnavailable
			}
		case RecoveryJobItemSucceeded:
			switch item.Operation {
			case RecoveryOperationCreate, RecoveryOperationOverwrite:
				if item.BytesWritten != item.EstimatedBytes || item.VerifiedSize != item.EstimatedBytes {
					return ErrRecoveryAPIUnavailable
				}
			case RecoveryOperationDelete:
				if item.BytesWritten != 0 || item.VerifiedSize != 0 {
					return ErrRecoveryAPIUnavailable
				}
			default:
				return ErrRecoveryAPIUnavailable
			}
		}
		if estimatedTotal > recoveryAPIMaxSafeInteger-item.EstimatedBytes ||
			writtenTotal > recoveryAPIMaxSafeInteger-item.BytesWritten {
			return ErrRecoveryAPIUnavailable
		}
		estimatedTotal += item.EstimatedBytes
		writtenTotal += item.BytesWritten
	}
	return nil
}

func validateRecoveryAPIJobProduct(
	job recoveryAPIJobRow,
	rows []recoveryAPIJobItemRow,
) (RecoveryJobProgressView, error) {
	if err := validateRecoveryAPIJobItemRows(rows, 0); err != nil {
		return RecoveryJobProgressView{}, err
	}
	progress := RecoveryJobProgressView{TotalItems: int64(len(rows))}
	var estimatedTotal int64
	for _, row := range rows {
		item, err := row.view()
		if err != nil || estimatedTotal > recoveryAPIMaxSafeInteger-item.EstimatedBytes ||
			progress.BytesWritten > recoveryAPIMaxSafeInteger-item.BytesWritten {
			return RecoveryJobProgressView{}, ErrRecoveryAPIUnavailable
		}
		estimatedTotal += item.EstimatedBytes
		progress.BytesWritten += item.BytesWritten
		switch item.Outcome {
		case RecoveryJobItemPending:
		case RecoveryJobItemSucceeded:
			progress.CompletedItems++
			progress.SucceededItems++
		case RecoveryJobItemSkipped:
			progress.CompletedItems++
			progress.SkippedItems++
		case RecoveryJobItemFailed:
			progress.CompletedItems++
			progress.FailedItems++
		default:
			return RecoveryJobProgressView{}, ErrRecoveryAPIUnavailable
		}
	}
	if progress.TotalItems != job.EstimatedItems || estimatedTotal != job.EstimatedBytes ||
		progress.BytesWritten > job.EstimatedBytes {
		return RecoveryJobProgressView{}, ErrRecoveryAPIUnavailable
	}
	state := JobState(job.State)
	categoryPresent := job.FailureCategory != ""
	switch state {
	case JobStateQueued:
		if categoryPresent || progress.CompletedItems != 0 || progress.BytesWritten != 0 {
			return RecoveryJobProgressView{}, ErrRecoveryAPIUnavailable
		}
	case JobStateRunning, JobStateCancelRequested, JobStateCanceled:
		if categoryPresent || progress.FailedItems != 0 {
			return RecoveryJobProgressView{}, ErrRecoveryAPIUnavailable
		}
	case JobStateVerifying, JobStateSucceeded, JobStateDegraded:
		if categoryPresent || progress.CompletedItems != progress.TotalItems || progress.FailedItems != 0 {
			return RecoveryJobProgressView{}, ErrRecoveryAPIUnavailable
		}
	case JobStateNeedsAttention, JobStateFailed:
		if !categoryPresent {
			return RecoveryJobProgressView{}, ErrRecoveryAPIUnavailable
		}
	default:
		return RecoveryJobProgressView{}, ErrRecoveryAPIUnavailable
	}
	return progress, nil
}

func loadValidatedRecoveryAPIJobProduct(
	tx *gorm.DB,
	job recoveryAPIJobRow,
) ([]recoveryAPIJobItemRow, RecoveryJobProgressView, error) {
	if tx == nil {
		return nil, RecoveryJobProgressView{}, ErrRecoveryAPIUnavailable
	}
	var rows []recoveryAPIJobItemRow
	loaded := tx.Table((model.BackupAssetRecoveryJobItem{}).TableName()).
		Select(`id, ordinal, operation_kind, outcome, estimated_bytes, bytes_written,
			verified_size, failure_category, created_at, updated_at`).
		Where("job_id = ?", job.ID).Order("ordinal ASC").Order("id ASC").
		Limit(exactSelectionMaxItems + 1).Find(&rows)
	if loaded.Error != nil {
		return nil, RecoveryJobProgressView{}, loaded.Error
	}
	if len(rows) > exactSelectionMaxItems {
		return nil, RecoveryJobProgressView{}, ErrRecoveryAPIUnavailable
	}
	progress, err := validateRecoveryAPIJobProduct(job, rows)
	if err != nil {
		return nil, RecoveryJobProgressView{}, err
	}
	return rows, progress, nil
}

type recoveryAPIResultSetRow struct {
	ID                    string
	State                 string
	PlaintextDeadline     time.Time
	HardDeadline          time.Time
	CleanupPhase          string
	CleanupOwner          string
	CleanupLeaseExpiresAt *time.Time
	CleanupFence          uint64
	NodeLeaseID           *string
	NodeFence             uint64
	CleanupAttempt        uint64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (row recoveryAPIResultSetRow) valid() bool {
	if !validOpaqueID(row.ID) || !ResultSetState(row.State).Valid() || row.PlaintextDeadline.IsZero() ||
		row.HardDeadline.IsZero() || row.HardDeadline.Before(row.PlaintextDeadline) ||
		row.CreatedAt.IsZero() || !row.PlaintextDeadline.After(row.CreatedAt) ||
		row.UpdatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) {
		return false
	}
	phase := CleanupPhase(row.CleanupPhase)
	switch ResultSetState(row.State) {
	case ResultSetStateReady:
		return phase == CleanupPhaseClaimed && row.CleanupOwner == "" && row.CleanupLeaseExpiresAt == nil &&
			row.CleanupFence == 0 && row.NodeLeaseID == nil && row.NodeFence == 0 && row.CleanupAttempt == 0
	case ResultSetStateRevoking:
		return phase.Valid() && phase != CleanupPhaseTombstoned && validRecoveryWorkerID(row.CleanupOwner) &&
			row.CleanupLeaseExpiresAt != nil && !row.CleanupLeaseExpiresAt.IsZero() && row.CleanupFence > 0 &&
			row.NodeLeaseID != nil && validOpaqueID(*row.NodeLeaseID) && row.NodeFence > 0 && row.CleanupAttempt > 0
	case ResultSetStateCleanupFailed:
		return (phase == CleanupPhaseDrained || phase == CleanupPhaseDeleteStarted) && row.CleanupOwner == "" &&
			row.CleanupLeaseExpiresAt == nil && row.CleanupFence > 0 && row.NodeLeaseID == nil &&
			row.NodeFence == 0 && row.CleanupAttempt > 0
	case ResultSetStateCleaned:
		return phase == CleanupPhaseTombstoned && row.CleanupOwner == "" && row.CleanupLeaseExpiresAt == nil &&
			row.CleanupFence > 0 && row.NodeLeaseID == nil && row.NodeFence == 0 && row.CleanupAttempt > 0
	default:
		return false
	}
}

func (row recoveryAPIResultSetRow) view() RecoveryResultSetView {
	return RecoveryResultSetView{
		ID: row.ID, State: ResultSetState(row.State), PlaintextDeadline: row.PlaintextDeadline.UTC(),
		HardDeadline: row.HardDeadline.UTC(), CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
}

func loadOptionalRecoveryAPIResultSet(tx *gorm.DB, jobID string) (*recoveryAPIResultSetRow, error) {
	var rows []recoveryAPIResultSetRow
	loaded := tx.Table((model.BackupAssetRecoveryResultSet{}).TableName()).
		Select(`id, state, plaintext_deadline, hard_deadline, cleanup_phase, cleanup_owner,
			cleanup_lease_expires_at, cleanup_fence, node_lease_id, node_fence, cleanup_attempt,
			created_at, updated_at`).
		Where("job_id = ?", jobID).Order("id ASC").Limit(2).Find(&rows)
	if loaded.Error != nil {
		return nil, loaded.Error
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) != 1 || !rows[0].valid() {
		return nil, ErrRecoveryAPIUnavailable
	}
	return &rows[0], nil
}

func loadRecoveryAPIResultSet(tx *gorm.DB, jobID string) (recoveryAPIResultSetRow, error) {
	row, err := loadOptionalRecoveryAPIResultSet(tx, jobID)
	if err != nil {
		return recoveryAPIResultSetRow{}, err
	}
	if row == nil {
		return recoveryAPIResultSetRow{}, ErrRecoveryAPIConflict
	}
	return *row, nil
}

type recoveryAPIResultRow struct {
	ID         string
	ResultKind string
	Size       int64
	ModifiedAt *time.Time
	CreatedAt  time.Time
}

func (row recoveryAPIResultRow) view() (RecoveryResultView, error) {
	kind := RecoveryResultKind(row.ResultKind)
	if !validOpaqueID(row.ID) || !kind.valid() || row.Size < 0 || row.Size > recoveryAPIMaxSafeInteger || row.CreatedAt.IsZero() ||
		(row.ModifiedAt != nil && row.ModifiedAt.IsZero()) {
		return RecoveryResultView{}, ErrRecoveryAPIUnavailable
	}
	modified := row.ModifiedAt
	if modified != nil {
		value := modified.UTC()
		modified = &value
	}
	return RecoveryResultView{ID: row.ID, Kind: kind, Size: row.Size, ModifiedAt: modified, CreatedAt: row.CreatedAt.UTC()}, nil
}

func normalizeRecoveryAPIPage(request RecoveryPageRequest) (int, int, int, error) {
	page, pageSize := request.Page, request.PageSize
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = recoveryAPIDefaultPageSize
	}
	if page < 1 || pageSize < 1 || pageSize > recoveryAPIHardPageSize || page-1 > math.MaxInt/pageSize {
		return 0, 0, 0, ErrRecoveryAPIInvalidPage
	}
	return page, pageSize, (page - 1) * pageSize, nil
}

func recoveryPublicFailureCategory(value string) (RecoveryPublicFailureCategory, bool) {
	switch value {
	case recoveryPreWriteDriftFailureCategory, "source_revalidation_failed":
		return RecoveryPublicFailureSourceDrift, true
	case recoveryVerificationMismatchFailureCategory:
		return RecoveryPublicFailureVerificationMismatch, true
	case recoveryRemoteOutcomeUnresolvedFailureCategory, recoveryPostPauseFailureCategory:
		return RecoveryPublicFailureRemoteOutcomeUnresolved, true
	case recoveryCancellationAfterMutationArmFailureCategory:
		return RecoveryPublicFailurePartialWrite, true
	case recoveryCleanupKeyUnavailableFailureCategory:
		return RecoveryPublicFailureCleanupUnavailable, true
	default:
		return "", false
	}
}

func mustRecoveryPublicFailureCategory(value string) RecoveryPublicFailureCategory {
	if value == "" {
		return ""
	}
	category, _ := recoveryPublicFailureCategory(value)
	return category
}

func settingsTargetRootIDValid(value string) bool {
	return len(value) > 0 && len(value) <= 32
}

func validRecoveryAPISecurityDecision(value SecurityDecisionKind) bool {
	switch value {
	case SecurityDecisionAllowClean, SecurityDecisionBlock, SecurityDecisionAdminOverride:
		return true
	default:
		return false
	}
}

func validRecoveryAPIPreflightReason(reason TargetPreflightReason) bool {
	switch reason {
	case TargetPreflightNodeUnregistered, TargetPreflightNodeArchived, TargetPreflightNodeOffline,
		TargetPreflightNodeUnauthorized, TargetPreflightCredentialPurpose, TargetPreflightToolUnavailable,
		TargetPreflightSourceUnavailable, TargetPreflightRootNotReal, TargetPreflightRootNoncanonical,
		TargetPreflightDeviceInvalid, TargetPreflightMountInvalid, TargetPreflightOwnerInvalid,
		TargetPreflightModeInvalid, TargetPreflightSymlinkComponent, TargetPreflightXirangOverlap,
		TargetPreflightSourceOverlap, TargetPreflightInsufficientBytes, TargetPreflightInsufficientInodes,
		TargetPreflightActiveWriter, TargetPreflightTargetConflict, TargetPreflightSecurityBlocked:
		return true
	default:
		return false
	}
}

func nonNilRecoveryAPIContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func recoveryAPIError(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrRecoveryAPIUnavailable
}
