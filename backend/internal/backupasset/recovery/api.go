package recovery

import (
	"context"
	"errors"
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
	SchemaVersion     int        `json:"schema_version"`
	ID                string     `json:"id"`
	PlanID            string     `json:"plan_id"`
	State             JobState   `json:"state"`
	Revision          string     `json:"revision"`
	TargetMode        TargetMode `json:"target_mode"`
	TargetNodeID      uint       `json:"target_node_id"`
	TargetRootID      string     `json:"target_root_id"`
	EstimatedItems    int64      `json:"estimated_items"`
	EstimatedBytes    int64      `json:"estimated_bytes"`
	PlaintextDeadline *time.Time `json:"plaintext_deadline,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
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
	var row recoveryAPIJobRow
	loaded := service.db.WithContext(nonNilRecoveryAPIContext(ctx)).
		Table((model.BackupAssetRecoveryJob{}).TableName()+" AS jobs").
		Select(`jobs.id, jobs.plan_id, jobs.state, jobs.transition_revision, jobs.target_mode,
			jobs.target_node_id, jobs.target_root_id, jobs.estimated_items, jobs.estimated_bytes,
			jobs.plaintext_deadline, jobs.created_at, jobs.updated_at`).
		Joins("JOIN "+(model.BackupAssetRecoveryPlan{}).TableName()+" AS plans ON plans.id = jobs.plan_id").
		Where("jobs.id = ? AND plans.requester_id = ?", jobID, requesterID).Limit(1).Find(&row)
	if loaded.Error != nil {
		return RecoveryJobView{}, recoveryAPIError(ctx)
	}
	if loaded.RowsAffected != 1 || !row.valid() {
		return RecoveryJobView{}, ErrRecoveryAPIObjectNotFound
	}
	return row.view(), nil
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
		var set struct {
			ID                    string
			State                 string
			PlaintextDeadline     time.Time
			CleanupOwner          string
			CleanupLeaseExpiresAt *time.Time
			CleanupFence          uint64
		}
		loaded = tx.WithContext(ctx).Table((model.BackupAssetRecoveryResultSet{}).TableName()).
			Select("id, state, plaintext_deadline, cleanup_owner, cleanup_lease_expires_at, cleanup_fence").
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", request.JobID).Limit(1).Find(&set)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !validOpaqueID(set.ID) || ResultSetState(set.State) != ResultSetStateReady ||
			set.CleanupOwner != "" || set.CleanupLeaseExpiresAt != nil || set.CleanupFence != 0 ||
			set.PlaintextDeadline.IsZero() || !set.PlaintextDeadline.UTC().After(now) {
			return ErrRecoveryAPIConflict
		}
		updated := tx.WithContext(ctx).Table((model.BackupAssetRecoveryResultSet{}).TableName()).
			Where("id = ? AND job_id = ? AND state = ? AND plaintext_deadline = ? AND cleanup_fence = 0",
				set.ID, request.JobID, ResultSetStateReady, set.PlaintextDeadline).
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
		settingsTargetRootIDValid(row.TargetRootID) && ConflictPolicy(row.ConflictPolicy).Validate() == nil &&
		validRecoveryAPISecurityDecision(SecurityDecisionKind(row.SecurityDecision)) &&
		validDigest(row.SelectionDigest) && validDigest(row.OperationSetDigest) && validDigest(row.DeleteSetDigest) &&
		row.EstimatedItems >= 0 && row.EstimatedBytes >= 0 && !row.CreatedAt.IsZero() && !row.UpdatedAt.IsZero()
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
	ID                 string
	PlanID             string
	State              string
	TransitionRevision uint64
	TargetMode         string
	TargetNodeID       uint
	TargetRootID       string
	EstimatedItems     int64
	EstimatedBytes     int64
	PlaintextDeadline  *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (row recoveryAPIJobRow) valid() bool {
	return validOpaqueID(row.ID) && validOpaqueID(row.PlanID) && JobState(row.State).Valid() &&
		row.TransitionRevision > 0 && TargetMode(row.TargetMode).Validate() == nil && row.TargetNodeID > 0 &&
		settingsTargetRootIDValid(row.TargetRootID) && row.EstimatedItems >= 0 && row.EstimatedBytes >= 0 &&
		!row.CreatedAt.IsZero() && !row.UpdatedAt.IsZero()
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
		PlaintextDeadline: deadline, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
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
