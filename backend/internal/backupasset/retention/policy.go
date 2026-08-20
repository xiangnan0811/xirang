package retention

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	activePolicyScopeIndex         = "idx_backup_retention_policies_active_scope"
	defaultSelectionExpireLimit    = 50
	maxSelectionExpireLimit        = 200
	defaultSelectionInspectedLimit = 200
	maxSelectionInspectedLimit     = 1000
	selectionCursorSchemaVersion   = 2
)

type PolicyServiceDependencies struct {
	DB    *gorm.DB
	Now   func() time.Time
	NewID func() (string, error)
	Audit MutationAuditor
}

type PolicyService struct {
	db                    *gorm.DB
	now                   func() time.Time
	newID                 func() (string, error)
	audit                 MutationAuditor
	selectionPageSize     int
	selectionPageObserver func(int)
}

type CreatePolicyRequest struct {
	Actor     backupasset.AuditActor
	ScopeKind backupasset.RetentionPolicyScopeKind
	ScopeID   string
	Rules     PolicyRules
}

type UpdatePolicyRequest struct {
	Actor            backupasset.AuditActor
	PolicyID         string
	ExpectedRevision int64
	Rules            PolicyRules
}

type DeletePolicyRequest struct {
	Actor            backupasset.AuditActor
	PolicyID         string
	ExpectedRevision int64
}

type PolicyRecord struct {
	ID         string
	ScopeKind  backupasset.RetentionPolicyScopeKind
	ScopeID    string
	Revision   int64
	Rules      PolicyRules
	RulesJSON  string
	RuleDigest string
	Status     backupasset.RetentionPolicyStatus
	CreatedBy  uint
	UpdatedBy  uint
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
}

type SelectionRequest struct {
	PolicyID         string
	ExpectedRevision int64
	EvaluatedAt      time.Time
	Limit            int
	InspectedLimit   int
	Cursor           string
}

type SelectedPoint struct {
	RecoveryPointID    string
	PointRevision      int64
	CapabilityRevision int
}

type Selection struct {
	PolicyID       string
	PolicyRevision int64
	ScopeKind      backupasset.RetentionPolicyScopeKind
	ScopeID        string
	RulesJSON      string
	RuleDigest     string
	EvaluatedAt    time.Time
	Points         []SelectedPoint
	NextCursor     string
	Inspected      int
}

func NewPolicyService(dependencies PolicyServiceDependencies) (*PolicyService, error) {
	if dependencies.DB == nil {
		return nil, fmt.Errorf("%w: retention policy database is unavailable", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.NewID == nil {
		dependencies.NewID = backupasset.NewOpaqueID
	}
	return &PolicyService{db: dependencies.DB, now: dependencies.Now, newID: dependencies.NewID, audit: dependencies.Audit}, nil
}

func (service *PolicyService) AuditsMutations() bool {
	return service != nil && service.audit != nil
}

func (service *PolicyService) Create(ctx context.Context, request CreatePolicyRequest) (PolicyRecord, error) {
	if err := validateAdminActor(request.Actor); err != nil {
		return PolicyRecord{}, err
	}
	if err := backupasset.ValidateRetentionPolicyScope(request.ScopeKind, request.ScopeID); err != nil {
		return PolicyRecord{}, err
	}
	canonical, digest, err := CanonicalizePolicyRules(request.Rules)
	if err != nil {
		return PolicyRecord{}, err
	}
	now := service.utcNow()
	record := model.BackupRetentionPolicy{
		ScopeKind: string(request.ScopeKind), ScopeID: request.ScopeID, Revision: 1,
		RulesJSON: canonical, Status: string(backupasset.RetentionPolicyActive),
		CreatedBy: request.Actor.UserID, UpdatedBy: request.Actor.UserID, CreatedAt: now, UpdatedAt: now,
	}
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repositoryID, err := lockPolicyScope(tx, request.ScopeKind, request.ScopeID)
		if err != nil {
			return err
		}
		var activeCount int64
		if err := tx.Model(&model.BackupRetentionPolicy{}).
			Where("scope_kind = ? AND scope_id = ? AND status = ?", request.ScopeKind, request.ScopeID, backupasset.RetentionPolicyActive).
			Count(&activeCount).Error; err != nil {
			return fmt.Errorf("load active retention policy scope: %w", err)
		}
		if activeCount != 0 {
			return fmt.Errorf("%w: active retention policy scope", backupasset.ErrConflict)
		}
		id, err := service.newID()
		if err != nil || backupasset.ValidateOpaqueID(id) != nil {
			return fmt.Errorf("%w: generate retention policy ID", backupasset.ErrInvalidState)
		}
		record.ID = id
		if err := tx.Create(&record).Error; err != nil {
			return mapPolicyCreateError(tx, request.ScopeKind, request.ScopeID, err)
		}
		return writeMutationAuditTx(ctx, tx, service.audit, mutationAuditInput(
			ctx, request.Actor, backupasset.AuditActionRetentionPolicyCreate, repositoryID, "", 1, record.ID,
		))
	})
	if err != nil {
		return PolicyRecord{}, err
	}
	return policyRecordFromModel(record, request.Rules, digest), nil
}

func (service *PolicyService) Update(ctx context.Context, request UpdatePolicyRequest) (PolicyRecord, error) {
	if err := validatePolicyMutation(request.Actor, request.PolicyID, request.ExpectedRevision); err != nil {
		return PolicyRecord{}, err
	}
	canonical, digest, err := CanonicalizePolicyRules(request.Rules)
	if err != nil {
		return PolicyRecord{}, err
	}
	now := service.utcNow()
	var policy model.BackupRetentionPolicy
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&policy, "id = ?", request.PolicyID).Error; err != nil {
			return mapPolicyLookupError(err)
		}
		if policy.Status != string(backupasset.RetentionPolicyActive) || policy.Revision != request.ExpectedRevision {
			return fmt.Errorf("%w: retention policy revision", backupasset.ErrConflict)
		}
		repositoryID, err := lockPolicyScope(tx, backupasset.RetentionPolicyScopeKind(policy.ScopeKind), policy.ScopeID)
		if err != nil {
			return err
		}
		result := tx.Model(&model.BackupRetentionPolicy{}).
			Where("id = ? AND revision = ? AND status = ?", request.PolicyID, request.ExpectedRevision, backupasset.RetentionPolicyActive).
			Updates(map[string]any{
				"revision":   request.ExpectedRevision + 1,
				"rules_json": canonical,
				"updated_by": request.Actor.UserID,
				"updated_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("update retention policy: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: retention policy revision", backupasset.ErrConflict)
		}
		policy.Revision = request.ExpectedRevision + 1
		policy.RulesJSON = canonical
		policy.UpdatedBy = request.Actor.UserID
		policy.UpdatedAt = now
		return writeMutationAuditTx(ctx, tx, service.audit, mutationAuditInput(
			ctx, request.Actor, backupasset.AuditActionRetentionPolicyUpdate, repositoryID, "", 1, policy.ID,
		))
	})
	if err != nil {
		return PolicyRecord{}, err
	}
	return policyRecordFromModel(policy, request.Rules, digest), nil
}

func (service *PolicyService) Delete(ctx context.Context, request DeletePolicyRequest) (PolicyRecord, error) {
	if err := validatePolicyMutation(request.Actor, request.PolicyID, request.ExpectedRevision); err != nil {
		return PolicyRecord{}, err
	}
	now := service.utcNow()
	var policy model.BackupRetentionPolicy
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&policy, "id = ?", request.PolicyID).Error; err != nil {
			return mapPolicyLookupError(err)
		}
		if policy.Status != string(backupasset.RetentionPolicyActive) || policy.Revision != request.ExpectedRevision {
			return fmt.Errorf("%w: retention policy revision", backupasset.ErrConflict)
		}
		repositoryID, err := lockPolicyScope(tx, backupasset.RetentionPolicyScopeKind(policy.ScopeKind), policy.ScopeID)
		if err != nil {
			return err
		}
		result := tx.Model(&model.BackupRetentionPolicy{}).
			Where("id = ? AND revision = ? AND status = ?", request.PolicyID, request.ExpectedRevision, backupasset.RetentionPolicyActive).
			Updates(map[string]any{
				"revision":   request.ExpectedRevision + 1,
				"status":     backupasset.RetentionPolicyDeleted,
				"updated_by": request.Actor.UserID,
				"updated_at": now,
				"deleted_at": now,
			})
		if result.Error != nil {
			return fmt.Errorf("delete retention policy: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: retention policy revision", backupasset.ErrConflict)
		}
		policy.Revision = request.ExpectedRevision + 1
		policy.Status = string(backupasset.RetentionPolicyDeleted)
		policy.UpdatedBy = request.Actor.UserID
		policy.UpdatedAt = now
		policy.DeletedAt = &now
		return writeMutationAuditTx(ctx, tx, service.audit, mutationAuditInput(
			ctx, request.Actor, backupasset.AuditActionRetentionPolicyDelete, repositoryID, "", 1, policy.ID,
		))
	})
	if err != nil {
		return PolicyRecord{}, err
	}
	rules, err := ParsePolicyRules(policy.RulesJSON)
	if err != nil {
		return PolicyRecord{}, err
	}
	_, digest, err := CanonicalizePolicyRules(rules)
	if err != nil {
		return PolicyRecord{}, err
	}
	return policyRecordFromModel(policy, rules, digest), nil
}

func (service *PolicyService) ListActive(ctx context.Context, limit int) ([]PolicyRecord, error) {
	return service.ListActiveAfter(ctx, limit, "")
}

func (service *PolicyService) ListActiveAfter(ctx context.Context, limit int, afterID string) ([]PolicyRecord, error) {
	if service == nil || service.db == nil {
		return nil, fmt.Errorf("%w: retention policy service is unavailable", backupasset.ErrInvalidState)
	}
	if limit < 1 || limit > 1000 {
		return nil, fmt.Errorf("%w: invalid retention policy list batch", backupasset.ErrInvalidState)
	}
	if afterID != "" && backupasset.ValidateOpaqueID(afterID) != nil {
		return nil, fmt.Errorf("%w: invalid retention policy list cursor", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	query := service.db.WithContext(ctx).Where("status = ?", backupasset.RetentionPolicyActive)
	if afterID != "" {
		query = query.Where("id > ?", afterID)
	}
	var policies []model.BackupRetentionPolicy
	if err := query.Order("id ASC").Limit(limit).Find(&policies).Error; err != nil {
		return nil, fmt.Errorf("list active retention policies: %w", err)
	}
	result := make([]PolicyRecord, 0, len(policies))
	for _, policy := range policies {
		rules, err := ParsePolicyRules(policy.RulesJSON)
		if err != nil {
			return nil, err
		}
		_, digest, err := CanonicalizePolicyRules(rules)
		if err != nil {
			return nil, err
		}
		result = append(result, policyRecordFromModel(policy, rules, digest))
	}
	return result, nil
}

type ImpactPreview struct {
	Selection      Selection
	ImpactRevision int64
	HoldCount      int64
	LeaseCount     int64
	WORMCount      int64
}

func (service *PolicyService) PreviewImpact(ctx context.Context, actor backupasset.AuditActor, request SelectionRequest) (ImpactPreview, error) {
	if err := validateAdminActor(actor); err != nil {
		return ImpactPreview{}, err
	}
	if request.EvaluatedAt.IsZero() && strings.TrimSpace(request.Cursor) == "" {
		request.EvaluatedAt = service.utcNow()
	}
	selection, err := service.Select(ctx, request)
	if err != nil {
		return ImpactPreview{}, err
	}
	counts, err := countLifecycleImpact(ctx, service.db, selectedPointIDs(selection), service.utcNow())
	if err != nil {
		return ImpactPreview{}, err
	}
	return ImpactPreview{
		Selection: selection, ImpactRevision: selection.PolicyRevision,
		HoldCount: counts.HoldCount, LeaseCount: counts.LeaseCount, WORMCount: counts.WORMCount,
	}, nil
}

func (service *PolicyService) Select(ctx context.Context, request SelectionRequest) (Selection, error) {
	var selection Selection
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		selection, err = service.SelectWithTx(ctx, tx, request)
		return err
	})
	return selection, err
}

func (service *PolicyService) SelectWithTx(ctx context.Context, tx *gorm.DB, request SelectionRequest) (Selection, error) {
	if tx == nil || backupasset.ValidateOpaqueID(request.PolicyID) != nil || request.ExpectedRevision < 1 {
		return Selection{}, fmt.Errorf("%w: invalid retention policy selection", backupasset.ErrInvalidState)
	}
	if strings.TrimSpace(request.Cursor) == "" && (request.EvaluatedAt.IsZero() || request.EvaluatedAt.Location() != time.UTC) {
		return Selection{}, fmt.Errorf("%w: invalid retention policy selection", backupasset.ErrInvalidState)
	}
	if !request.EvaluatedAt.IsZero() && request.EvaluatedAt.Location() != time.UTC {
		return Selection{}, fmt.Errorf("%w: invalid retention policy selection", backupasset.ErrInvalidState)
	}
	var policy model.BackupRetentionPolicy
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&policy, "id = ?", request.PolicyID).Error; err != nil {
		return Selection{}, mapPolicyLookupError(err)
	}
	if policy.Status != string(backupasset.RetentionPolicyActive) || policy.Revision != request.ExpectedRevision {
		return Selection{}, fmt.Errorf("%w: retention policy revision", backupasset.ErrConflict)
	}
	rules, err := ParsePolicyRules(policy.RulesJSON)
	if err != nil {
		return Selection{}, err
	}
	canonical, digest, err := CanonicalizePolicyRules(rules)
	if err != nil {
		return Selection{}, err
	}
	repositoryID, taskID, err := resolveSelectionScope(tx.WithContext(ctx), policy)
	if err != nil {
		return Selection{}, err
	}
	query := tx.WithContext(ctx).Model(&model.RecoveryPoint{}).
		Where("repository_id = ?", repositoryID).
		Where("semantics IN ?", []string{
			string(backupasset.PointNativeSnapshot), string(backupasset.PointXirangManifest), string(backupasset.PointImportedBaseline),
		}).
		Where("state IN ?", []string{string(backupasset.RecoveryPointCommitted), string(backupasset.RecoveryPointDegraded)}).
		Where("physical_availability = ?", backupasset.PhysicalOnline).
		Where("hold_state IN ?", []string{string(backupasset.HoldNone), string(backupasset.HoldReleased)}).
		Where(`NOT EXISTS (
			SELECT 1 FROM recovery_point_holds AS active_hold
			WHERE active_hold.recovery_point_id = recovery_points.id AND active_hold.state = ?
		)`, backupasset.HoldActive)
	if taskID != nil {
		query = query.Where("producing_task_id = ?", *taskID)
	}
	pageSize := service.selectionPageSize
	if pageSize < 1 {
		pageSize = defaultSelectionInspectedLimit
	}
	limit := request.Limit
	if limit < 1 {
		limit = defaultSelectionExpireLimit
	}
	if limit > maxSelectionExpireLimit {
		limit = maxSelectionExpireLimit
	}
	inspectedLimit := request.InspectedLimit
	if inspectedLimit < 1 {
		inspectedLimit = defaultSelectionInspectedLimit
	}
	if inspectedLimit > maxSelectionInspectedLimit {
		inspectedLimit = maxSelectionInspectedLimit
	}
	resume, err := parseSelectionCursor(request.Cursor)
	if err != nil {
		return Selection{}, err
	}
	evaluatedAt, err := bindSelectionSnapshot(request, resume, policy.ID, policy.Revision, digest)
	if err != nil {
		return Selection{}, err
	}
	state := newSelectionKeepState(rules, evaluatedAt, resume)
	selected := make([]SelectedPoint, 0, limit)
	inspected := 0
	afterCapturedAt := resume.AfterCapturedAt
	afterID := resume.AfterID
	var nextCursor string
	for inspected < inspectedLimit {
		remainingInspected := inspectedLimit - inspected
		queryLimit := pageSize
		if remainingInspected < queryLimit {
			queryLimit = remainingInspected
		}
		pageQuery := query.Session(&gorm.Session{}).Select("id", "point_revision", "capability_revision", "captured_at")
		if afterID != "" && afterCapturedAt != nil {
			pageQuery = pageQuery.Where(
				"captured_at < ? OR (captured_at = ? AND id < ?)",
				afterCapturedAt, afterCapturedAt, afterID,
			)
		}
		var page []model.RecoveryPoint
		if err := pageQuery.Clauses(clause.Locking{Strength: "UPDATE"}).
			Order("captured_at DESC, id DESC").Limit(queryLimit).Find(&page).Error; err != nil {
			return Selection{}, fmt.Errorf("load retention policy recovery points: %w", err)
		}
		if service.selectionPageObserver != nil {
			service.selectionPageObserver(len(page))
		}
		for _, point := range page {
			if backupasset.ValidateOpaqueID(point.ID) != nil || point.PointRevision < 1 || point.CapabilityRevision < 1 ||
				point.CapturedAt == nil || point.CapturedAt.IsZero() {
				return Selection{}, fmt.Errorf("%w: incomplete immutable recovery point selection facts", backupasset.ErrInvalidState)
			}
			inspected++
			capturedAt := point.CapturedAt.UTC()
			kept := state.keep(capturedAt)
			afterCapturedAt = &capturedAt
			afterID = point.ID
			if !kept {
				selected = append(selected, SelectedPoint{
					RecoveryPointID: point.ID, PointRevision: point.PointRevision,
					CapabilityRevision: point.CapabilityRevision,
				})
			}
			if len(selected) == limit || inspected == inspectedLimit {
				nextCursor, err = encodeSelectionCursor(selectionResume{
					AfterCapturedAt: afterCapturedAt, AfterID: afterID,
					NSeen: state.nSeen, Calendar: state.snapshotCalendar(),
					PolicyID: policy.ID, Revision: policy.Revision, RuleDigest: digest, EvaluatedAt: evaluatedAt,
				})
				if err != nil {
					return Selection{}, err
				}
				break
			}
		}
		if nextCursor != "" || len(page) < queryLimit {
			break
		}
	}
	return Selection{
		PolicyID: policy.ID, PolicyRevision: policy.Revision,
		ScopeKind: backupasset.RetentionPolicyScopeKind(policy.ScopeKind), ScopeID: policy.ScopeID,
		RulesJSON: canonical, RuleDigest: digest, EvaluatedAt: evaluatedAt,
		Points: selected, NextCursor: nextCursor, Inspected: inspected,
	}, nil
}

func (service *PolicyService) utcNow() time.Time {
	return service.now().UTC()
}

func validateAdminActor(actor backupasset.AuditActor) error {
	if actor.UserID == 0 || actor.Role != "admin" {
		return fmt.Errorf("%w: administrator actor required", backupasset.ErrForbidden)
	}
	return nil
}

func validatePolicyMutation(actor backupasset.AuditActor, policyID string, expectedRevision int64) error {
	if err := validateAdminActor(actor); err != nil {
		return err
	}
	if backupasset.ValidateOpaqueID(policyID) != nil || expectedRevision < 1 {
		return fmt.Errorf("%w: invalid retention policy mutation", backupasset.ErrInvalidState)
	}
	return nil
}

func lockPolicyScope(tx *gorm.DB, scopeKind backupasset.RetentionPolicyScopeKind, scopeID string) (string, error) {
	var err error
	switch scopeKind {
	case backupasset.RetentionPolicyScopeRepository:
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").
			First(&model.BackupRepository{}, "id = ?", scopeID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("%w: retention policy scope", backupasset.ErrNotFound)
		}
		if err != nil {
			return "", fmt.Errorf("load retention policy scope: %w", err)
		}
		return scopeID, nil
	case backupasset.RetentionPolicyScopeTaskLink:
		var link model.TaskRepositoryLink
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "task_id", "repository_id").
			Where("id = ? AND unlinked_at IS NULL", scopeID).
			First(&link).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("%w: retention policy scope", backupasset.ErrNotFound)
		}
		if err != nil {
			return "", fmt.Errorf("load retention policy scope: %w", err)
		}
		if link.TaskID == nil {
			return "", fmt.Errorf("%w: active Task repository link", backupasset.ErrNotFound)
		}
		return link.RepositoryID, nil
	default:
		return "", fmt.Errorf("%w: invalid retention policy scope", backupasset.ErrInvalidState)
	}
}

func resolveSelectionScope(tx *gorm.DB, policy model.BackupRetentionPolicy) (string, *uint, error) {
	switch backupasset.RetentionPolicyScopeKind(policy.ScopeKind) {
	case backupasset.RetentionPolicyScopeRepository:
		var repository model.BackupRepository
		if err := tx.Select("id").First(&repository, "id = ?", policy.ScopeID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", nil, fmt.Errorf("%w: retention policy repository scope changed", backupasset.ErrConflict)
			}
			return "", nil, fmt.Errorf("load retention policy repository scope: %w", err)
		}
		return repository.ID, nil, nil
	case backupasset.RetentionPolicyScopeTaskLink:
		var link model.TaskRepositoryLink
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("repository_id", "task_id").
			Where("id = ? AND unlinked_at IS NULL", policy.ScopeID).
			First(&link).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return "", nil, fmt.Errorf("%w: retention policy Task-link scope changed", backupasset.ErrConflict)
			}
			return "", nil, fmt.Errorf("load retention policy Task-link scope: %w", err)
		}
		if link.TaskID == nil || backupasset.ValidateOpaqueID(link.RepositoryID) != nil {
			return "", nil, fmt.Errorf("%w: retention policy Task-link scope changed", backupasset.ErrConflict)
		}
		taskID := *link.TaskID
		return link.RepositoryID, &taskID, nil
	default:
		return "", nil, fmt.Errorf("%w: invalid retention policy scope", backupasset.ErrInvalidState)
	}
}

type pointCandidate struct {
	point      selectionFact
	capturedAt time.Time
}

type selectionFact struct {
	id                 string
	pointRevision      int64
	capabilityRevision int
	capturedAt         *time.Time
}

func selectRecoveryPoints(points []selectionFact, rules PolicyRules, evaluatedAt time.Time) ([]SelectedPoint, error) {
	candidates := make([]pointCandidate, 0, len(points))
	for _, point := range points {
		if backupasset.ValidateOpaqueID(point.id) != nil || point.pointRevision < 1 || point.capabilityRevision < 1 ||
			point.capturedAt == nil || point.capturedAt.IsZero() {
			return nil, fmt.Errorf("%w: incomplete immutable recovery point selection facts", backupasset.ErrInvalidState)
		}
		candidates = append(candidates, pointCandidate{point: point, capturedAt: point.capturedAt.UTC()})
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].capturedAt.Equal(candidates[right].capturedAt) {
			return candidates[left].point.id < candidates[right].point.id
		}
		return candidates[left].capturedAt.After(candidates[right].capturedAt)
	})

	kept := make(map[string]bool, len(candidates))
	if rules.Age != nil {
		cutoff := evaluatedAt.AddDate(0, 0, -rules.Age.KeepDays)
		for _, candidate := range candidates {
			if !candidate.capturedAt.Before(cutoff) {
				kept[candidate.point.id] = true
			}
		}
	}
	if rules.Count != nil {
		limit := rules.Count.KeepLatest
		if limit > len(candidates) {
			limit = len(candidates)
		}
		for index := 0; index < limit; index++ {
			kept[candidates[index].point.id] = true
		}
	}
	for _, calendarRule := range rules.Calendar {
		buckets := make(map[string]bool, calendarRule.Keep)
		for _, candidate := range candidates {
			bucket := calendarBucket(candidate.capturedAt, calendarRule.Unit)
			if buckets[bucket] {
				continue
			}
			if len(buckets) >= calendarRule.Keep {
				break
			}
			buckets[bucket] = true
			kept[candidate.point.id] = true
		}
	}
	selected := make([]SelectedPoint, 0, len(candidates))
	for _, candidate := range candidates {
		if kept[candidate.point.id] {
			continue
		}
		selected = append(selected, SelectedPoint{
			RecoveryPointID: candidate.point.id, PointRevision: candidate.point.pointRevision,
			CapabilityRevision: candidate.point.capabilityRevision,
		})
	}
	sort.Slice(selected, func(left, right int) bool {
		return selected[left].RecoveryPointID < selected[right].RecoveryPointID
	})
	return selected, nil
}

type selectionResume struct {
	AfterCapturedAt *time.Time
	AfterID         string
	NSeen           int
	Calendar        []map[string]bool
	PolicyID        string
	Revision        int64
	RuleDigest      string
	EvaluatedAt     time.Time
}

type selectionCursorPayload struct {
	Version         int               `json:"v"`
	AfterCapturedAt *time.Time        `json:"t,omitempty"`
	AfterID         string            `json:"id,omitempty"`
	NSeen           int               `json:"n"`
	Calendar        []map[string]bool `json:"c,omitempty"`
	PolicyID        string            `json:"p"`
	Revision        int64             `json:"r"`
	RuleDigest      string            `json:"d"`
	EvaluatedAt     time.Time         `json:"e"`
}

func parseSelectionCursor(raw string) (selectionResume, error) {
	if strings.TrimSpace(raw) == "" {
		return selectionResume{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return selectionResume{}, fmt.Errorf("%w: invalid retention selection cursor", backupasset.ErrInvalidState)
	}
	var payload selectionCursorPayload
	if err := json.Unmarshal(decoded, &payload); err != nil || payload.Version != selectionCursorSchemaVersion {
		return selectionResume{}, fmt.Errorf("%w: invalid retention selection cursor", backupasset.ErrInvalidState)
	}
	if payload.AfterID != "" && backupasset.ValidateOpaqueID(payload.AfterID) != nil {
		return selectionResume{}, fmt.Errorf("%w: invalid retention selection cursor", backupasset.ErrInvalidState)
	}
	if backupasset.ValidateOpaqueID(payload.PolicyID) != nil || payload.Revision < 1 ||
		payload.RuleDigest == "" || payload.EvaluatedAt.IsZero() {
		return selectionResume{}, fmt.Errorf("%w: invalid retention selection cursor", backupasset.ErrInvalidState)
	}
	return selectionResume{
		AfterCapturedAt: payload.AfterCapturedAt, AfterID: payload.AfterID,
		NSeen: payload.NSeen, Calendar: payload.Calendar,
		PolicyID: payload.PolicyID, Revision: payload.Revision,
		RuleDigest: payload.RuleDigest, EvaluatedAt: payload.EvaluatedAt.UTC(),
	}, nil
}

func encodeSelectionCursor(resume selectionResume) (string, error) {
	payload, err := json.Marshal(selectionCursorPayload{
		Version: selectionCursorSchemaVersion, AfterCapturedAt: resume.AfterCapturedAt,
		AfterID: resume.AfterID, NSeen: resume.NSeen, Calendar: resume.Calendar,
		PolicyID: resume.PolicyID, Revision: resume.Revision,
		RuleDigest: resume.RuleDigest, EvaluatedAt: resume.EvaluatedAt.UTC(),
	})
	if err != nil {
		return "", fmt.Errorf("%w: encode retention selection cursor", backupasset.ErrInvalidState)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func bindSelectionSnapshot(
	request SelectionRequest,
	resume selectionResume,
	policyID string,
	revision int64,
	digest string,
) (time.Time, error) {
	evaluatedAt := request.EvaluatedAt
	if strings.TrimSpace(request.Cursor) == "" {
		if evaluatedAt.IsZero() || evaluatedAt.Location() != time.UTC {
			return time.Time{}, fmt.Errorf("%w: invalid retention policy selection", backupasset.ErrInvalidState)
		}
		return evaluatedAt, nil
	}
	if evaluatedAt.IsZero() {
		evaluatedAt = resume.EvaluatedAt
	}
	if evaluatedAt.IsZero() || evaluatedAt.Location() != time.UTC {
		return time.Time{}, fmt.Errorf("%w: invalid retention policy selection", backupasset.ErrInvalidState)
	}
	if resume.PolicyID != policyID || resume.Revision != revision || resume.RuleDigest != digest ||
		!resume.EvaluatedAt.Equal(evaluatedAt) {
		return time.Time{}, fmt.Errorf("%w: retention selection cursor snapshot", backupasset.ErrConflict)
	}
	return evaluatedAt, nil
}

type selectionKeepState struct {
	nSeen      int
	ageCutoff  *time.Time
	keepLatest int
	calendar   []calendarKeepState
}

type calendarKeepState struct {
	unit    CalendarUnit
	keep    int
	buckets map[string]bool
}

func newSelectionKeepState(rules PolicyRules, evaluatedAt time.Time, resume selectionResume) *selectionKeepState {
	state := &selectionKeepState{nSeen: resume.NSeen}
	if rules.Age != nil {
		cutoff := evaluatedAt.AddDate(0, 0, -rules.Age.KeepDays)
		state.ageCutoff = &cutoff
	}
	if rules.Count != nil {
		state.keepLatest = rules.Count.KeepLatest
	}
	state.calendar = make([]calendarKeepState, 0, len(rules.Calendar))
	for index, rule := range rules.Calendar {
		buckets := map[string]bool{}
		if index < len(resume.Calendar) && resume.Calendar[index] != nil {
			for bucket, present := range resume.Calendar[index] {
				if present {
					buckets[bucket] = true
				}
			}
		}
		state.calendar = append(state.calendar, calendarKeepState{unit: rule.Unit, keep: rule.Keep, buckets: buckets})
	}
	return state
}

func (state *selectionKeepState) keep(capturedAt time.Time) bool {
	if state == nil {
		return false
	}
	kept := state.ageCutoff != nil && !capturedAt.Before(*state.ageCutoff)
	if state.keepLatest > 0 && state.nSeen < state.keepLatest {
		kept = true
	}
	for index := range state.calendar {
		bucket := calendarBucket(capturedAt, state.calendar[index].unit)
		if state.calendar[index].buckets[bucket] {
			continue
		}
		if len(state.calendar[index].buckets) >= state.calendar[index].keep {
			continue
		}
		state.calendar[index].buckets[bucket] = true
		kept = true
	}
	state.nSeen++
	return kept
}

func (state *selectionKeepState) snapshotCalendar() []map[string]bool {
	if state == nil {
		return nil
	}
	copied := make([]map[string]bool, len(state.calendar))
	for index, rule := range state.calendar {
		copied[index] = make(map[string]bool, len(rule.buckets))
		for bucket, present := range rule.buckets {
			if present {
				copied[index][bucket] = true
			}
		}
	}
	return copied
}

func calendarBucket(value time.Time, unit CalendarUnit) string {
	value = value.UTC()
	switch unit {
	case CalendarDay:
		return value.Format("2006-01-02")
	case CalendarWeek:
		year, week := value.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", year, week)
	case CalendarMonth:
		return value.Format("2006-01")
	case CalendarYear:
		return value.Format("2006")
	default:
		return ""
	}
}

func mapPolicyLookupError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: retention policy", backupasset.ErrNotFound)
	}
	return fmt.Errorf("load retention policy: %w", err)
}

func mapPolicyCreateError(tx *gorm.DB, scopeKind backupasset.RetentionPolicyScopeKind, scopeID string, createErr error) error {
	var postgresError *pgconn.PgError
	if errors.As(createErr, &postgresError) {
		if postgresError.Code == "23505" && postgresError.ConstraintName == activePolicyScopeIndex {
			return fmt.Errorf("%w: active retention policy scope", backupasset.ErrConflict)
		}
		return fmt.Errorf("create retention policy: %w", createErr)
	}
	var activeCount int64
	if err := tx.Model(&model.BackupRetentionPolicy{}).
		Where("scope_kind = ? AND scope_id = ? AND status = ?", scopeKind, scopeID, backupasset.RetentionPolicyActive).
		Count(&activeCount).Error; err == nil && activeCount != 0 {
		return fmt.Errorf("%w: active retention policy scope", backupasset.ErrConflict)
	}
	return fmt.Errorf("create retention policy: %w", createErr)
}

type lifecycleImpactCounts struct {
	HoldCount  int64
	LeaseCount int64
	WORMCount  int64
}

func selectedPointIDs(selection Selection) []string {
	ids := make([]string, 0, len(selection.Points))
	for _, point := range selection.Points {
		ids = append(ids, point.RecoveryPointID)
	}
	return ids
}

func countLifecycleImpact(ctx context.Context, db *gorm.DB, pointIDs []string, now time.Time) (lifecycleImpactCounts, error) {
	var counts lifecycleImpactCounts
	if db == nil {
		return counts, fmt.Errorf("%w: retention impact database is unavailable", backupasset.ErrInvalidState)
	}
	if len(pointIDs) == 0 {
		return counts, nil
	}
	if err := db.WithContext(ctx).Model(&model.RecoveryPointHold{}).
		Where("recovery_point_id IN ? AND state = ?", pointIDs, backupasset.HoldActive).
		Count(&counts.HoldCount).Error; err != nil {
		return lifecycleImpactCounts{}, fmt.Errorf("count retention impact holds: %w", err)
	}
	if err := db.WithContext(ctx).Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id IN ? AND status = ? AND released_at IS NULL AND lease_expires_at > ? AND absolute_deadline > ?",
			pointIDs, backupasset.LeaseActive, now, now).
		Count(&counts.LeaseCount).Error; err != nil {
		return lifecycleImpactCounts{}, fmt.Errorf("count retention impact leases: %w", err)
	}
	if err := db.WithContext(ctx).Model(&model.RecoveryPoint{}).
		Where("id IN ? AND immutability_level = ?", pointIDs, backupasset.ImmutabilityStorageWORM).
		Count(&counts.WORMCount).Error; err != nil {
		return lifecycleImpactCounts{}, fmt.Errorf("count retention impact WORM points: %w", err)
	}
	return counts, nil
}

func policyRecordFromModel(policy model.BackupRetentionPolicy, rules PolicyRules, digest string) PolicyRecord {
	return PolicyRecord{
		ID: policy.ID, ScopeKind: backupasset.RetentionPolicyScopeKind(policy.ScopeKind), ScopeID: policy.ScopeID,
		Revision: policy.Revision, Rules: rules, RulesJSON: policy.RulesJSON, RuleDigest: digest,
		Status: backupasset.RetentionPolicyStatus(policy.Status), CreatedBy: policy.CreatedBy, UpdatedBy: policy.UpdatedBy,
		CreatedAt: policy.CreatedAt.UTC(), UpdatedAt: policy.UpdatedAt.UTC(), DeletedAt: utcTimePointer(policy.DeletedAt),
	}
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}
