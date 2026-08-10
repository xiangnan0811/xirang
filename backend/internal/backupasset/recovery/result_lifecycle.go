package recovery

import (
	"context"
	"errors"
	"math"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const recoveryCleanupFailureProjectionTimeout = 5 * time.Second

var (
	ErrInvalidResultLifecycle                = errors.New("invalid recovery result lifecycle")
	ErrRecoveryResultRetainDenied            = errors.New("recovery result retain denied")
	ErrRecoveryResultRetainConflict          = errors.New("recovery result retain conflict")
	ErrRecoveryResultCleanupBusy             = errors.New("recovery result cleanup node busy")
	ErrRecoveryResultCleanupConflict         = errors.New("recovery result cleanup conflict")
	ErrRecoveryResultCleanupValidationFailed = errors.New("recovery result cleanup target validation failed")
)

type RecoveryResultKind string

const (
	RecoveryResultKindRegularFile        RecoveryResultKind = "regular_file"
	RecoveryResultKindVerificationReport RecoveryResultKind = "verification_report"
)

func (kind RecoveryResultKind) valid() bool {
	return kind == RecoveryResultKindRegularFile || kind == RecoveryResultKindVerificationReport
}

type ResultLifecycleDependencies struct {
	DB                  *gorm.DB
	Now                 func() time.Time
	WorkspaceKeys       RecoveryWorkspaceKeySource
	DefaultPlaintextTTL time.Duration
	RetainHardCap       time.Duration
	NodeAdmission       RecoveryNodeAdmission
	ContentLifecycle    RecoveryResultContentLifecycle
	Target              TargetPort
	CleanupLeaseTTL     time.Duration
	NewID               func() (string, error)
}

type RecoveryResultContentLifecycle interface {
	RevokeRecoveryResultGrantsTx(context.Context, *gorm.DB, string, string, time.Time) error
	CancelRecoveryResultReads(string) error
	DrainRecoveryResult(context.Context, string) error
}

var _ RecoveryResultContentLifecycle = (*content.Broker)(nil)

type ResultLifecycleService struct {
	db                  *gorm.DB
	now                 func() time.Time
	workspaceKeys       RecoveryWorkspaceKeySource
	defaultPlaintextTTL time.Duration
	retainHardCap       time.Duration
	nodeAdmission       RecoveryNodeAdmission
	contentLifecycle    RecoveryResultContentLifecycle
	target              TargetPort
	cleanupLeaseTTL     time.Duration
	newID               func() (string, error)
}

type PublishRecoveryResultInput struct {
	JobItemID      string
	Classification RecoveryResultClassificationBinding
}

type PublishRecoveryResultsRequest struct {
	JobID               string
	ExpectedJobRevision uint64
	Results             []PublishRecoveryResultInput
}

type PublishedRecoveryResult struct {
	ID             string
	Kind           RecoveryResultKind
	Size           int64
	ContentDigest  string
	Classification RecoveryResultClassificationBinding
}

type PublishedRecoveryResultSet struct {
	ResultSetID       string
	JobID             string
	JobRevision       uint64
	PlaintextDeadline time.Time
	HardDeadline      time.Time
	Results           []PublishedRecoveryResult
}

type RetainRecoveryResultsRequest struct {
	JobID               string
	ExpectedJobRevision uint64
	RequestedDeadline   time.Time
	Actor               content.DeliveryActor
	Permissions         backupasset.PermissionSet
	Proof               *content.StepUpProof
}

type RetainedRecoveryResultSet struct {
	ResultSetID       string
	JobID             string
	JobRevision       uint64
	PlaintextDeadline time.Time
	HardDeadline      time.Time
}

type ClaimRecoveryResultCleanupRequest struct {
	ResultSetID string
	WorkerID    string
}

type RecoveryResultCleanupClaim struct {
	ResultSetID    string
	JobID          string
	WorkerID       string
	CleanupFence   uint64
	CleanupAttempt uint64
	NodeLeaseID    string
	NodeFence      uint64
	LeaseExpiresAt time.Time
	Phase          CleanupPhase
}

type ClaimRecoveryWorkspaceCleanupRequest struct {
	JobID    string
	WorkerID string
}

type RecoveryWorkspaceCleanupClaim struct {
	JobID          string
	WorkerID       string
	CleanupFence   uint64
	CleanupAttempt uint64
	NodeLeaseID    string
	NodeFence      uint64
	LeaseExpiresAt time.Time
	Phase          CleanupPhase
}

type recoveryResultCleanupCandidate struct {
	ResultSet model.BackupAssetRecoveryResultSet
}

type recoveryWorkspaceCleanupCandidate struct {
	Job model.BackupAssetRecoveryJob
}

type recoveryCleanupTargetBinding struct {
	NodeID              uint
	Object              TargetObjectRef
	MarkerBindingDigest string `json:"-"`
	MarkerCreatorID     string `json:"-"`
	MarkerCreatorFence  uint64 `json:"-"`
	RootRevision        string
	SessionBinding      recoveryTargetSessionBinding
}

func NewResultLifecycleService(dependencies ResultLifecycleDependencies) (*ResultLifecycleService, error) {
	if dependencies.DB == nil || dependencies.Now == nil || dependencies.WorkspaceKeys == nil ||
		dependencies.DefaultPlaintextTTL <= 0 || dependencies.RetainHardCap < dependencies.DefaultPlaintextTTL ||
		dependencies.NodeAdmission == nil || dependencies.ContentLifecycle == nil || dependencies.Target == nil ||
		dependencies.CleanupLeaseTTL <= 0 ||
		dependencies.CleanupLeaseTTL > 24*time.Hour {
		return nil, ErrInvalidResultLifecycle
	}
	if dependencies.NewID == nil {
		dependencies.NewID = backupasset.NewOpaqueID
	}
	return &ResultLifecycleService{
		db: dependencies.DB, now: dependencies.Now, workspaceKeys: dependencies.WorkspaceKeys,
		defaultPlaintextTTL: dependencies.DefaultPlaintextTTL, retainHardCap: dependencies.RetainHardCap,
		nodeAdmission: dependencies.NodeAdmission, contentLifecycle: dependencies.ContentLifecycle,
		target:          dependencies.Target,
		cleanupLeaseTTL: dependencies.CleanupLeaseTTL,
		newID:           dependencies.NewID,
	}, nil
}

func (service *ResultLifecycleService) Publish(
	ctx context.Context,
	request PublishRecoveryResultsRequest,
) (PublishedRecoveryResultSet, error) {
	if service == nil || service.db == nil || service.now == nil || service.workspaceKeys == nil ||
		!validOpaqueID(request.JobID) || request.ExpectedJobRevision == 0 ||
		len(request.Results) == 0 || len(request.Results) > exactSelectionMaxItems {
		return PublishedRecoveryResultSet{}, ErrInvalidResultPublication
	}
	requested := make(map[string]PublishRecoveryResultInput, len(request.Results))
	for _, input := range request.Results {
		if !validOpaqueID(input.JobItemID) || !input.Classification.valid() {
			return PublishedRecoveryResultSet{}, ErrInvalidResultPublication
		}
		if _, duplicate := requested[input.JobItemID]; duplicate {
			return PublishedRecoveryResultSet{}, ErrInvalidResultPublication
		}
		requested[input.JobItemID] = input
	}

	resultSetID, err := service.newID()
	if err != nil || !validOpaqueID(resultSetID) {
		return PublishedRecoveryResultSet{}, ErrInvalidResultPublication
	}
	resultIDs := make(map[string]string, len(request.Results))
	for _, input := range request.Results {
		resultID, idErr := service.newID()
		if idErr != nil || !validOpaqueID(resultID) {
			return PublishedRecoveryResultSet{}, ErrInvalidResultPublication
		}
		resultIDs[input.JobItemID] = resultID
	}

	now := service.now().UTC()
	if now.IsZero() {
		return PublishedRecoveryResultSet{}, ErrInvalidResultPublication
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var published PublishedRecoveryResultSet
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", request.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || job.TransitionRevision != request.ExpectedJobRevision ||
			TargetMode(job.TargetMode) != TargetModeIsolated ||
			(JobState(job.State) != JobStateSucceeded && JobState(job.State) != JobStateDegraded) ||
			WorkspacePhase(job.WorkspacePhase) != WorkspacePhaseSealed ||
			job.PlaintextDeadline == nil || !job.PlaintextDeadline.UTC().After(now) ||
			job.WorkspaceOwner == "" || job.WorkspaceFence == 0 ||
			!validDigest(job.WorkspaceMarkerBindingDigest) || !validRecoveryMarkerValidation(job) {
			return ErrInvalidResultPublication
		}

		var activeAttempts int64
		if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
			Where("job_id = ? AND state IN ?", job.ID, []string{string(AttemptStateClaimed), string(AttemptStateRunning)}).
			Count(&activeAttempts).Error; err != nil {
			return err
		}
		if activeAttempts != 0 {
			return ErrInvalidResultPublication
		}

		var plan model.BackupAssetRecoveryPlan
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", job.PlanID).Limit(1).Find(&plan)
		if loaded.Error != nil {
			return loaded.Error
		}
		workspaceLocator := recoveryWorkspaceLocatorDirectory + "/" + job.ID
		if loaded.RowsAffected != 1 || plan.ID != job.PlanID || plan.RequesterID == 0 ||
			job.EncryptedWorkspaceRelativeLocator != workspaceLocator ||
			job.WorkspaceBindingDigest != recoveryWorkspaceBindingDigest(plan, job.ID, workspaceLocator) {
			return ErrInvalidResultPublication
		}

		var preflight model.BackupAssetRecoveryPreflight
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND plan_id = ?", job.PreflightID, plan.ID).Limit(1).Find(&preflight)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 {
			return ErrInvalidResultPublication
		}
		operationRows, err := rebuildExecuteOperationRows(plan, preflight, tx.WithContext(ctx))
		if err != nil {
			return ErrInvalidResultPublication
		}

		var items []model.BackupAssetRecoveryJobItem
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", job.ID).Order("ordinal ASC").Find(&items)
		if loaded.Error != nil {
			return loaded.Error
		}
		if len(items) != len(operationRows) {
			return ErrInvalidResultPublication
		}

		materials := make(map[int]backupasset.DomainKeyMaterial)
		rows := make([]model.BackupAssetRecoveryResult, 0, len(request.Results))
		publicRows := make([]PublishedRecoveryResult, 0, len(request.Results))
		for index := range items {
			item := items[index]
			operationRow := operationRows[index]
			if !ordinarySourceJobItemMatchesOperation(plan, job, item, operationRow, index) {
				return ErrInvalidResultPublication
			}
			input, selected := requested[item.ID]
			publishable := item.Outcome == "succeeded" && item.FailureCategory == "" &&
				item.DisplayClass == string(RecoveryDisplayClassRegular) &&
				(RecoveryOperationKind(item.OperationKind) == RecoveryOperationCreate ||
					RecoveryOperationKind(item.OperationKind) == RecoveryOperationOverwrite) &&
				item.VerifiedSize >= 0 && validDigest(item.VerifiedDigest)
			if selected != publishable {
				return ErrInvalidResultPublication
			}
			if !selected {
				if item.Outcome != "skipped" && item.Outcome != "failed" {
					return ErrInvalidResultPublication
				}
				continue
			}

			material, found := materials[item.TargetLocatorKeyVersion]
			if !found {
				material, err = service.workspaceKeys.ByVersion(
					ctx, backupasset.KeyDomainRecoveryCleanupOwnership, item.TargetLocatorKeyVersion,
				)
				if err != nil || !validTargetLocatorKey(material, item.TargetLocatorKeyVersion) {
					return ErrInvalidResultPublication
				}
				materials[item.TargetLocatorKeyVersion] = material
			}
			binding := targetLocatorBindingForExecute(
				plan, job.ID, item.ID, operationRow, workspaceLocator, job.WorkspaceBindingDigest,
				item.TargetObjectDigest, item.TargetLocatorKeyVersion,
			)
			locator, openErr := OpenTargetLocatorEnvelope(material, binding, item.EncryptedTargetRelativeLocator)
			if openErr != nil || locator != operationRow.operation.TargetRelativeLocator ||
				!validTargetRelativeLocator(locator) {
				return ErrInvalidResultPublication
			}

			resultID := resultIDs[item.ID]
			locatorDigest := recoveryResultLocatorDigest(job.ID, resultSetID, resultID, locator)
			rows = append(rows, model.BackupAssetRecoveryResult{
				ID: resultID, ResultSetID: resultSetID, JobID: job.ID,
				ResultKind:                   string(RecoveryResultKindRegularFile),
				Classification:               string(input.Classification.Kind),
				ClassificationRevision:       int(input.Classification.Revision),
				ClassificationSourceRevision: input.Classification.SourceRevision,
				EncryptedRelativeLocator:     locator, LocatorDigest: locatorDigest,
				Size: item.VerifiedSize, ContentDigest: item.VerifiedDigest, CreatedAt: now,
			})
			publicRows = append(publicRows, PublishedRecoveryResult{
				ID: resultID, Kind: RecoveryResultKindRegularFile, Size: item.VerifiedSize,
				ContentDigest: item.VerifiedDigest, Classification: input.Classification,
			})
		}
		if len(rows) != len(request.Results) {
			return ErrInvalidResultPublication
		}

		plaintextDeadline := job.PlaintextDeadline.UTC()
		hardDeadline := plaintextDeadline.Add(service.retainHardCap - service.defaultPlaintextTTL).UTC()
		publication := ResultPublicationBinding{
			TargetMode: TargetModeIsolated, JobState: JobState(job.State), WorkspacePhase: WorkspacePhasePublished,
			HasActiveAttempt: false, WorkspaceMarkerBindingDigest: job.WorkspaceMarkerBindingDigest,
			ResultSetMarkerBindingDigest: job.WorkspaceMarkerBindingDigest,
			WorkspacePlaintextDeadline:   plaintextDeadline, InitialResultPlaintextDeadline: plaintextDeadline,
			ResultPlaintextRetentionHardLimit: hardDeadline,
		}
		if publication.ValidateAt(now) != nil {
			return ErrInvalidResultPublication
		}

		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where(`id = ? AND transition_revision = ? AND workspace_phase = ? AND state IN ?`,
				job.ID, job.TransitionRevision, WorkspacePhaseSealed,
				[]string{string(JobStateSucceeded), string(JobStateDegraded)}).
			Updates(map[string]any{
				"workspace_phase":     string(WorkspacePhasePublished),
				"transition_revision": job.TransitionRevision + 1, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrInvalidResultPublication
		}
		resultSet := model.BackupAssetRecoveryResultSet{
			ID: resultSetID, JobID: job.ID, State: string(ResultSetStateReady),
			MarkerBindingDigest: job.WorkspaceMarkerBindingDigest,
			PlaintextDeadline:   plaintextDeadline, HardDeadline: hardDeadline,
			CleanupPhase: string(CleanupPhaseClaimed), CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.WithContext(ctx).Create(&resultSet).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Create(&rows).Error; err != nil {
			return err
		}
		published = PublishedRecoveryResultSet{
			ResultSetID: resultSetID, JobID: job.ID, JobRevision: job.TransitionRevision + 1,
			PlaintextDeadline: plaintextDeadline, HardDeadline: hardDeadline, Results: publicRows,
		}
		return nil
	})
	if err != nil {
		return PublishedRecoveryResultSet{}, err
	}
	return published, nil
}

func (service *ResultLifecycleService) Retain(
	ctx context.Context,
	request RetainRecoveryResultsRequest,
) (RetainedRecoveryResultSet, error) {
	if service == nil || service.db == nil || service.now == nil ||
		!validOpaqueID(request.JobID) || request.ExpectedJobRevision == 0 || request.RequestedDeadline.IsZero() {
		return RetainedRecoveryResultSet{}, ErrInvalidResultLifecycle
	}
	now := service.now().UTC()
	requestedDeadline := request.RequestedDeadline.UTC()
	if now.IsZero() || !validRecoveryResultRetainAuthorization(request, now) {
		return RetainedRecoveryResultSet{}, ErrRecoveryResultRetainDenied
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var retained RetainedRecoveryResultSet
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", request.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || job.TransitionRevision != request.ExpectedJobRevision ||
			TargetMode(job.TargetMode) != TargetModeIsolated ||
			(JobState(job.State) != JobStateSucceeded && JobState(job.State) != JobStateDegraded) ||
			WorkspacePhase(job.WorkspacePhase) != WorkspacePhasePublished || job.PlaintextDeadline == nil ||
			!validDigest(job.WorkspaceMarkerBindingDigest) {
			return ErrRecoveryResultRetainConflict
		}

		var plan model.BackupAssetRecoveryPlan
		loaded = tx.WithContext(ctx).Select("id", "requester_id").
			Where("id = ?", job.PlanID).Limit(1).Find(&plan)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || plan.RequesterID == 0 || plan.RequesterID != request.Actor.UserID {
			return ErrRecoveryResultRetainDenied
		}

		var resultSet model.BackupAssetRecoveryResultSet
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("job_id = ?", job.ID).Limit(1).Find(&resultSet)
		if loaded.Error != nil {
			return loaded.Error
		}
		initialDeadline := job.PlaintextDeadline.UTC()
		if loaded.RowsAffected != 1 || ResultSetState(resultSet.State) != ResultSetStateReady ||
			resultSet.CleanupOwner != "" || resultSet.CleanupLeaseExpiresAt != nil || resultSet.CleanupFence != 0 ||
			resultSet.NodeLeaseID != nil || resultSet.NodeFence != 0 || resultSet.CleanupAttempt != 0 ||
			resultSet.MarkerBindingDigest != job.WorkspaceMarkerBindingDigest ||
			!validRecoveryResultDeadlineWindow(
				initialDeadline, resultSet.PlaintextDeadline.UTC(), resultSet.HardDeadline.UTC(), now,
			) ||
			!requestedDeadline.After(resultSet.PlaintextDeadline.UTC()) ||
			requestedDeadline.After(resultSet.HardDeadline.UTC()) {
			return ErrRecoveryResultRetainConflict
		}

		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
			Where("id = ? AND job_id = ? AND state = ? AND plaintext_deadline = ? AND hard_deadline = ?",
				resultSet.ID, job.ID, ResultSetStateReady, resultSet.PlaintextDeadline, resultSet.HardDeadline).
			Updates(map[string]any{"plaintext_deadline": requestedDeadline, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryResultRetainConflict
		}
		retained = RetainedRecoveryResultSet{
			ResultSetID: resultSet.ID, JobID: job.ID, JobRevision: job.TransitionRevision,
			PlaintextDeadline: requestedDeadline, HardDeadline: resultSet.HardDeadline.UTC(),
		}
		return nil
	})
	if err != nil {
		return RetainedRecoveryResultSet{}, err
	}
	return retained, nil
}

func validRecoveryResultRetainAuthorization(request RetainRecoveryResultsRequest, now time.Time) bool {
	return request.Actor.UserID > 0 && request.Actor.Role == "admin" &&
		request.Permissions.Has(backupasset.PermissionBackupAssetsRecover) && request.Proof != nil &&
		request.Proof.Action == auth.StepUpActionRecoveryResultRetain && validOpaqueID(request.Proof.ID) &&
		request.Proof.ExpiresAt.UTC().After(now)
}

func validRecoveryResultDeadlineWindow(initial, current, hardDeadline, now time.Time) bool {
	return !initial.IsZero() && !current.IsZero() && !hardDeadline.IsZero() && !now.IsZero() &&
		current.After(now) && !current.Before(initial) && !current.After(hardDeadline)
}

func (service *ResultLifecycleService) ClaimCleanup(
	ctx context.Context,
	request ClaimRecoveryResultCleanupRequest,
) (RecoveryResultCleanupClaim, error) {
	if service == nil || service.db == nil || service.now == nil || service.nodeAdmission == nil ||
		service.newID == nil || service.cleanupLeaseTTL <= 0 ||
		!validOpaqueID(request.ResultSetID) || !validRecoveryWorkerID(request.WorkerID) {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}

	var candidate recoveryResultCleanupCandidate
	loaded := service.db.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
		Where("id = ?", request.ResultSetID).Limit(1).Find(&candidate.ResultSet)
	if loaded.Error != nil {
		return RecoveryResultCleanupClaim{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !validRecoveryResultCleanupCandidate(candidate.ResultSet, now) {
		return RecoveryResultCleanupClaim{}, ErrRecoveryResultCleanupConflict
	}
	nodeLeaseID, err := service.newID()
	if err != nil || !validOpaqueID(nodeLeaseID) {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}

	claimLost := false
	var claim RecoveryResultCleanupClaim
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		jobResult := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", candidate.ResultSet.JobID).Limit(1).Find(&job)
		if jobResult.Error != nil {
			return jobResult.Error
		}
		if jobResult.RowsAffected != 1 || job.ID != candidate.ResultSet.JobID || job.TargetNodeID == 0 ||
			TargetMode(job.TargetMode) != TargetModeIsolated ||
			(JobState(job.State) != JobStateSucceeded && JobState(job.State) != JobStateDegraded) ||
			WorkspacePhase(job.WorkspacePhase) != WorkspacePhasePublished ||
			job.WorkspaceMarkerBindingDigest != candidate.ResultSet.MarkerBindingDigest {
			return ErrRecoveryResultCleanupConflict
		}
		nodeLease, err := service.createRecoveryCleanupNodeLeaseTx(
			ctx, tx, job.ID, job.TargetNodeID, request.WorkerID, nodeLeaseID, now,
		)
		if err != nil {
			return err
		}
		nodeFence := nodeLease.Fence
		leaseExpiresAt := nodeLease.LeaseExpiresAt

		var resultSet model.BackupAssetRecoveryResultSet
		resultSetResult := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ?", candidate.ResultSet.ID, job.ID).Limit(1).Find(&resultSet)
		if resultSetResult.Error != nil {
			return resultSetResult.Error
		}
		if resultSetResult.RowsAffected != 1 ||
			!sameRecoveryResultCleanupSnapshot(candidate.ResultSet, resultSet) ||
			!validRecoveryResultCleanupCandidate(resultSet, now) {
			if err := releaseRecoveryResultCleanupNodeLeaseTx(ctx, tx, nodeLease, now); err != nil {
				return err
			}
			claimLost = true
			return nil
		}

		if resultSet.CleanupFence >= math.MaxInt64 || resultSet.CleanupAttempt >= math.MaxInt64 {
			return ErrRecoveryResultCleanupConflict
		}
		nextCleanupFence := resultSet.CleanupFence + 1
		nextCleanupAttempt := resultSet.CleanupAttempt + 1
		nextPhase := CleanupPhaseClaimed
		if ResultSetState(resultSet.State) == ResultSetStateRevoking ||
			ResultSetState(resultSet.State) == ResultSetStateCleanupFailed {
			nextPhase = CleanupPhase(resultSet.CleanupPhase)
		}
		updateQuery := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
			Where(`id = ? AND job_id = ? AND state = ? AND cleanup_phase = ?
				AND cleanup_owner = ? AND cleanup_fence = ? AND node_fence = ?
				AND cleanup_attempt = ? AND updated_at = ?`,
				resultSet.ID, job.ID, resultSet.State, resultSet.CleanupPhase,
				resultSet.CleanupOwner, resultSet.CleanupFence, resultSet.NodeFence,
				resultSet.CleanupAttempt, resultSet.UpdatedAt)
		if resultSet.CleanupLeaseExpiresAt == nil {
			updateQuery = updateQuery.Where("cleanup_lease_expires_at IS NULL")
		} else {
			updateQuery = updateQuery.Where("cleanup_lease_expires_at = ?", *resultSet.CleanupLeaseExpiresAt)
		}
		if resultSet.NodeLeaseID == nil {
			updateQuery = updateQuery.Where("node_lease_id IS NULL")
		} else {
			updateQuery = updateQuery.Where("node_lease_id = ?", *resultSet.NodeLeaseID)
		}
		updated := updateQuery.Updates(map[string]any{
			"state": string(ResultSetStateRevoking), "cleanup_phase": string(nextPhase),
			"cleanup_owner": request.WorkerID, "cleanup_lease_expires_at": leaseExpiresAt,
			"cleanup_fence": nextCleanupFence, "node_lease_id": nodeLease.ID, "node_fence": nodeFence,
			"cleanup_attempt": nextCleanupAttempt, "updated_at": now,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			if err := releaseRecoveryResultCleanupNodeLeaseTx(ctx, tx, nodeLease, now); err != nil {
				return err
			}
			claimLost = true
			return nil
		}

		claim = RecoveryResultCleanupClaim{
			ResultSetID: resultSet.ID, JobID: job.ID, WorkerID: request.WorkerID,
			CleanupFence: nextCleanupFence, CleanupAttempt: nextCleanupAttempt,
			NodeLeaseID: nodeLease.ID, NodeFence: nodeFence,
			LeaseExpiresAt: leaseExpiresAt, Phase: nextPhase,
		}
		return nil
	})
	if err != nil {
		return RecoveryResultCleanupClaim{}, err
	}
	if claimLost {
		return RecoveryResultCleanupClaim{}, ErrRecoveryResultCleanupConflict
	}
	return claim, nil
}

func (service *ResultLifecycleService) ClaimWorkspaceCleanup(
	ctx context.Context,
	request ClaimRecoveryWorkspaceCleanupRequest,
) (RecoveryWorkspaceCleanupClaim, error) {
	if service == nil || service.db == nil || service.now == nil || service.nodeAdmission == nil ||
		service.newID == nil || service.cleanupLeaseTTL <= 0 ||
		!validOpaqueID(request.JobID) || !validRecoveryWorkerID(request.WorkerID) {
		return RecoveryWorkspaceCleanupClaim{}, ErrInvalidResultLifecycle
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryWorkspaceCleanupClaim{}, ErrInvalidResultLifecycle
	}

	var candidate recoveryWorkspaceCleanupCandidate
	loaded := service.db.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
		Where("id = ?", request.JobID).Limit(1).Find(&candidate.Job)
	if loaded.Error != nil {
		return RecoveryWorkspaceCleanupClaim{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !validRecoveryWorkspaceCleanupCandidate(candidate.Job, now) {
		return RecoveryWorkspaceCleanupClaim{}, ErrRecoveryResultCleanupConflict
	}
	nodeLeaseID, err := service.newID()
	if err != nil || !validOpaqueID(nodeLeaseID) {
		return RecoveryWorkspaceCleanupClaim{}, ErrInvalidResultLifecycle
	}

	claimLost := false
	var claim RecoveryWorkspaceCleanupClaim
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		jobResult := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", candidate.Job.ID).Limit(1).Find(&job)
		if jobResult.Error != nil {
			return jobResult.Error
		}
		if jobResult.RowsAffected != 1 || !validRecoveryWorkspaceCleanupCandidate(job, now) {
			return ErrRecoveryResultCleanupConflict
		}
		var resultSets int64
		if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
			Where("job_id = ?", job.ID).Count(&resultSets).Error; err != nil {
			return err
		}
		if resultSets != 0 {
			return ErrRecoveryResultCleanupConflict
		}

		nodeLease, err := service.createRecoveryCleanupNodeLeaseTx(
			ctx, tx, job.ID, job.TargetNodeID, request.WorkerID, nodeLeaseID, now,
		)
		if err != nil {
			return err
		}
		if job.WorkspaceCleanupFence >= math.MaxInt64 || job.WorkspaceCleanupAttempt >= math.MaxInt64 {
			return ErrRecoveryResultCleanupConflict
		}
		nextCleanupFence := job.WorkspaceCleanupFence + 1
		nextCleanupAttempt := job.WorkspaceCleanupAttempt + 1
		nextPhase := CleanupPhaseClaimed
		if job.WorkspaceCleanupOwner != "" ||
			(job.WorkspaceCleanupFence > 0 && job.WorkspaceCleanupAttempt > 0) {
			nextPhase = CleanupPhase(job.WorkspaceCleanupPhase)
		}

		updateQuery := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where(`id = ? AND state = ? AND transition_revision = ? AND target_mode = ?
				AND target_node_id = ? AND workspace_phase = ?
				AND workspace_binding_digest = ? AND workspace_marker_binding_digest = ?
				AND workspace_owner = ? AND workspace_fence = ?
				AND workspace_marker_validation_attempt_id = ?
				AND workspace_marker_validation_attempt_fence = ?
				AND workspace_marker_validation_node_fence = ?
				AND workspace_cleanup_phase = ? AND workspace_cleanup_owner = ?
				AND workspace_cleanup_fence = ? AND workspace_cleanup_node_fence = ?
				AND workspace_cleanup_attempt = ? AND plaintext_deadline = ? AND updated_at = ?`,
				candidate.Job.ID, candidate.Job.State, candidate.Job.TransitionRevision, candidate.Job.TargetMode,
				candidate.Job.TargetNodeID, candidate.Job.WorkspacePhase,
				candidate.Job.WorkspaceBindingDigest, candidate.Job.WorkspaceMarkerBindingDigest,
				candidate.Job.WorkspaceOwner, candidate.Job.WorkspaceFence,
				candidate.Job.WorkspaceMarkerValidationAttemptID,
				candidate.Job.WorkspaceMarkerValidationAttemptFence,
				candidate.Job.WorkspaceMarkerValidationNodeFence,
				candidate.Job.WorkspaceCleanupPhase, candidate.Job.WorkspaceCleanupOwner,
				candidate.Job.WorkspaceCleanupFence, candidate.Job.WorkspaceCleanupNodeFence,
				candidate.Job.WorkspaceCleanupAttempt, candidate.Job.PlaintextDeadline, candidate.Job.UpdatedAt)
		if candidate.Job.WorkspaceCleanupLeaseExpiresAt == nil {
			updateQuery = updateQuery.Where("workspace_cleanup_lease_expires_at IS NULL")
		} else {
			updateQuery = updateQuery.Where(
				"workspace_cleanup_lease_expires_at = ?", *candidate.Job.WorkspaceCleanupLeaseExpiresAt,
			)
		}
		if candidate.Job.WorkspaceCleanupNodeLeaseID == nil {
			updateQuery = updateQuery.Where("workspace_cleanup_node_lease_id IS NULL")
		} else {
			updateQuery = updateQuery.Where(
				"workspace_cleanup_node_lease_id = ?", *candidate.Job.WorkspaceCleanupNodeLeaseID,
			)
		}
		updated := updateQuery.Updates(map[string]any{
			"workspace_cleanup_phase": string(nextPhase), "workspace_cleanup_owner": request.WorkerID,
			"workspace_cleanup_lease_expires_at": nodeLease.LeaseExpiresAt,
			"workspace_cleanup_fence":            nextCleanupFence,
			"workspace_cleanup_node_lease_id":    nodeLease.ID,
			"workspace_cleanup_node_fence":       nodeLease.Fence,
			"workspace_cleanup_attempt":          nextCleanupAttempt,
			"updated_at":                         now,
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			if err := releaseRecoveryResultCleanupNodeLeaseTx(ctx, tx, nodeLease, now); err != nil {
				return err
			}
			claimLost = true
			return nil
		}

		claim = RecoveryWorkspaceCleanupClaim{
			JobID: job.ID, WorkerID: request.WorkerID,
			CleanupFence: nextCleanupFence, CleanupAttempt: nextCleanupAttempt,
			NodeLeaseID: nodeLease.ID, NodeFence: nodeLease.Fence,
			LeaseExpiresAt: nodeLease.LeaseExpiresAt, Phase: nextPhase,
		}
		return nil
	})
	if err != nil {
		return RecoveryWorkspaceCleanupClaim{}, err
	}
	if claimLost {
		return RecoveryWorkspaceCleanupClaim{}, ErrRecoveryResultCleanupConflict
	}
	return claim, nil
}

func (service *ResultLifecycleService) RevokeRecoveryResultCleanup(
	ctx context.Context,
	claim RecoveryResultCleanupClaim,
) (RecoveryResultCleanupClaim, error) {
	if !validRecoveryResultCleanupClaim(claim) || claim.Phase != CleanupPhaseClaimed ||
		service == nil || service.db == nil || service.now == nil || service.contentLifecycle == nil {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}
	ctx = nonNilRecoveryContext(ctx)
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}

	renewed := claim
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var transitionErr error
		renewed, transitionErr = service.transitionRecoveryResultCleanupTx(
			ctx, tx, claim, CleanupPhaseClaimed, CleanupPhaseRevoked, now,
			func(tx *gorm.DB) error {
				return service.contentLifecycle.RevokeRecoveryResultGrantsTx(
					ctx, tx, claim.JobID, content.RecoveryResultCleanupReason, now,
				)
			},
		)
		return transitionErr
	})
	if err != nil {
		return claim, err
	}
	if err := service.contentLifecycle.CancelRecoveryResultReads(claim.JobID); err != nil {
		return renewed, err
	}
	return renewed, nil
}

func (service *ResultLifecycleService) DrainRecoveryResultCleanup(
	ctx context.Context,
	claim RecoveryResultCleanupClaim,
) (RecoveryResultCleanupClaim, error) {
	if !validRecoveryResultCleanupClaim(claim) || claim.Phase != CleanupPhaseRevoked ||
		service == nil || service.db == nil || service.now == nil || service.contentLifecycle == nil {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}
	ctx = nonNilRecoveryContext(ctx)
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}

	renewed := claim
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var transitionErr error
		renewed, transitionErr = service.transitionRecoveryResultCleanupTx(
			ctx, tx, claim, CleanupPhaseRevoked, CleanupPhaseRevoked, now, nil,
		)
		return transitionErr
	})
	if err != nil {
		return claim, err
	}
	if err := service.contentLifecycle.DrainRecoveryResult(ctx, claim.JobID); err != nil {
		return renewed, err
	}

	now = service.now().UTC()
	if now.IsZero() {
		return renewed, ErrInvalidResultLifecycle
	}
	drained := renewed
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var transitionErr error
		drained, transitionErr = service.transitionRecoveryResultCleanupTx(
			ctx, tx, renewed, CleanupPhaseRevoked, CleanupPhaseDrained, now, nil,
		)
		return transitionErr
	})
	if err != nil {
		return renewed, err
	}
	return drained, nil
}

func (service *ResultLifecycleService) RevokeRecoveryWorkspaceCleanup(
	ctx context.Context,
	claim RecoveryWorkspaceCleanupClaim,
) (RecoveryWorkspaceCleanupClaim, error) {
	return service.transitionRecoveryWorkspaceCleanup(
		ctx, claim, CleanupPhaseClaimed, CleanupPhaseRevoked,
	)
}

func (service *ResultLifecycleService) DrainRecoveryWorkspaceCleanup(
	ctx context.Context,
	claim RecoveryWorkspaceCleanupClaim,
) (RecoveryWorkspaceCleanupClaim, error) {
	return service.transitionRecoveryWorkspaceCleanup(
		ctx, claim, CleanupPhaseRevoked, CleanupPhaseDrained,
	)
}

func (service *ResultLifecycleService) ValidateRecoveryResultCleanup(
	ctx context.Context,
	claim RecoveryResultCleanupClaim,
) (RecoveryResultCleanupClaim, error) {
	if service == nil || service.db == nil || service.now == nil || service.target == nil ||
		service.cleanupLeaseTTL <= 0 || !validRecoveryResultCleanupClaim(claim) ||
		claim.Phase != CleanupPhaseDrained {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}
	ctx = nonNilRecoveryContext(ctx)
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}

	renewed := claim
	var binding recoveryCleanupTargetBinding
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var renewalErr error
		renewed, binding, renewalErr = service.renewRecoveryResultCleanupValidationTx(
			ctx, tx, claim, CleanupPhaseDrained, now,
		)
		return renewalErr
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryResultCleanupValidationFailed) {
			return service.failRecoveryResultCleanupValidation(ctx, claim)
		}
		return claim, err
	}
	permit := issueTargetCleanupPermit(TargetCleanupPermit{
		SchemaVersion: 1, Purpose: TargetPurposeCleanup,
		Operation:    TargetCleanupValidateOwnedJobDir,
		ResourceKind: CleanupResourceResultSet, ResourceID: renewed.ResultSetID,
		JobID: renewed.JobID, CleanupOwner: renewed.WorkerID,
		CleanupFence: renewed.CleanupFence, CleanupAttempt: renewed.CleanupAttempt,
		NodeID:      binding.NodeID,
		NodeLeaseID: renewed.NodeLeaseID, NodeFence: renewed.NodeFence,
		RootID: binding.Object.RootID, RootLocatorDigest: binding.Object.RootLocatorDigest,
		TargetPathDigest: binding.Object.TargetPathDigest, RootRevision: binding.RootRevision,
		MarkerBindingDigest: binding.MarkerBindingDigest, UseLatchID: RecoverySchemaUseLatchID,
		MarkerCreatorID: binding.MarkerCreatorID, MarkerCreatorFence: binding.MarkerCreatorFence,
		ExpiresAt: renewed.LeaseExpiresAt,
	}, binding.SessionBinding)
	request := ValidateOwnedJobDirRequest{
		Object: binding.Object, MarkerBindingDigest: binding.MarkerBindingDigest,
		MarkerCreatorID: binding.MarkerCreatorID, MarkerCreatorFence: binding.MarkerCreatorFence,
	}
	if permit.NodeID == 0 || permit.ValidateOwnedJobDirRequestAt(now, request) != nil {
		return service.failRecoveryResultCleanupValidation(ctx, renewed)
	}

	observation, observeErr := service.observeRecoveryCleanupTarget(ctx, permit, request)
	if observeErr != nil || !validOwnedJobDirValidation(observation, request, binding.RootRevision) {
		return service.failRecoveryResultCleanupValidation(ctx, renewed)
	}

	now = service.now().UTC()
	if now.IsZero() {
		return renewed, ErrInvalidResultLifecycle
	}
	validated := renewed
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var closingBinding recoveryCleanupTargetBinding
		var validationErr error
		validated, closingBinding, validationErr = service.renewRecoveryResultCleanupValidationTx(
			ctx, tx, renewed, CleanupPhaseValidated, now,
		)
		if validationErr != nil {
			return validationErr
		}
		if !sameRecoveryCleanupTargetBinding(closingBinding, binding) ||
			!publishedCleanupPermitMatchesClaimAt(permit, renewed, closingBinding, request, now) ||
			!validOwnedJobDirValidation(observation, request, closingBinding.RootRevision) {
			return ErrRecoveryResultCleanupValidationFailed
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryResultCleanupValidationFailed) {
			return service.failRecoveryResultCleanupValidation(ctx, renewed)
		}
		return renewed, err
	}
	return validated, nil
}

func (service *ResultLifecycleService) ValidateRecoveryWorkspaceCleanup(
	ctx context.Context,
	claim RecoveryWorkspaceCleanupClaim,
) (RecoveryWorkspaceCleanupClaim, error) {
	if service == nil || service.db == nil || service.now == nil || service.target == nil ||
		service.cleanupLeaseTTL <= 0 || !validRecoveryWorkspaceCleanupClaim(claim) ||
		claim.Phase != CleanupPhaseDrained {
		return RecoveryWorkspaceCleanupClaim{}, ErrInvalidResultLifecycle
	}
	ctx = nonNilRecoveryContext(ctx)
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryWorkspaceCleanupClaim{}, ErrInvalidResultLifecycle
	}

	renewed := claim
	var binding recoveryCleanupTargetBinding
	var nodeID uint
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var renewalErr error
		renewed, binding, nodeID, renewalErr = service.renewRecoveryWorkspaceCleanupValidationTx(
			ctx, tx, claim, CleanupPhaseDrained, now,
		)
		return renewalErr
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryResultCleanupValidationFailed) {
			return service.failRecoveryWorkspaceCleanupValidation(ctx, claim)
		}
		return claim, err
	}
	permit := issueTargetCleanupPermit(TargetCleanupPermit{
		SchemaVersion: 1, Purpose: TargetPurposeCleanup,
		Operation:    TargetCleanupValidateOwnedJobDir,
		ResourceKind: CleanupResourceWorkspace, ResourceID: renewed.JobID,
		JobID: renewed.JobID, CleanupOwner: renewed.WorkerID,
		CleanupFence: renewed.CleanupFence, CleanupAttempt: renewed.CleanupAttempt,
		NodeID: nodeID, NodeLeaseID: renewed.NodeLeaseID, NodeFence: renewed.NodeFence,
		RootID: binding.Object.RootID, RootLocatorDigest: binding.Object.RootLocatorDigest,
		TargetPathDigest: binding.Object.TargetPathDigest, RootRevision: binding.RootRevision,
		MarkerBindingDigest: binding.MarkerBindingDigest, UseLatchID: RecoverySchemaUseLatchID,
		MarkerCreatorID: binding.MarkerCreatorID, MarkerCreatorFence: binding.MarkerCreatorFence,
		ExpiresAt: renewed.LeaseExpiresAt,
	}, binding.SessionBinding)
	request := ValidateOwnedJobDirRequest{
		Object: binding.Object, MarkerBindingDigest: binding.MarkerBindingDigest,
		MarkerCreatorID: binding.MarkerCreatorID, MarkerCreatorFence: binding.MarkerCreatorFence,
	}
	if permit.ValidateOwnedJobDirRequestAt(now, request) != nil {
		return service.failRecoveryWorkspaceCleanupValidation(ctx, renewed)
	}

	observation, observeErr := service.observeRecoveryCleanupTarget(ctx, permit, request)
	if observeErr != nil || !validOwnedJobDirValidation(observation, request, binding.RootRevision) {
		return service.failRecoveryWorkspaceCleanupValidation(ctx, renewed)
	}

	now = service.now().UTC()
	if now.IsZero() {
		return renewed, ErrInvalidResultLifecycle
	}
	validated := renewed
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var closingBinding recoveryCleanupTargetBinding
		var closingNodeID uint
		var validationErr error
		validated, closingBinding, closingNodeID, validationErr = service.renewRecoveryWorkspaceCleanupValidationTx(
			ctx, tx, renewed, CleanupPhaseValidated, now,
		)
		if validationErr != nil {
			return validationErr
		}
		if closingNodeID != nodeID || !sameRecoveryCleanupTargetBinding(closingBinding, binding) ||
			!workspaceCleanupPermitMatchesClaimAt(permit, renewed, nodeID, closingBinding, request, now) ||
			!validOwnedJobDirValidation(observation, request, closingBinding.RootRevision) {
			return ErrRecoveryResultCleanupValidationFailed
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRecoveryResultCleanupValidationFailed) {
			return service.failRecoveryWorkspaceCleanupValidation(ctx, renewed)
		}
		return renewed, err
	}
	return validated, nil
}

func (service *ResultLifecycleService) AdvanceRecoveryResultCleanup(
	ctx context.Context,
	claim RecoveryResultCleanupClaim,
) (RecoveryCleanupProgress, error) {
	if service == nil || service.db == nil || service.now == nil || service.target == nil ||
		service.cleanupLeaseTTL <= 0 || !validRecoveryResultCleanupClaim(claim) ||
		(claim.Phase != CleanupPhaseValidated && claim.Phase != CleanupPhaseDeleteStarted && claim.Phase != CleanupPhaseDeleted) {
		return RecoveryCleanupProgress{}, ErrInvalidResultLifecycle
	}
	ctx = nonNilRecoveryContext(ctx)
	if err := ctx.Err(); err != nil {
		return RecoveryCleanupProgress{}, err
	}
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryCleanupProgress{}, ErrInvalidResultLifecycle
	}

	renewed := claim
	var binding recoveryCleanupTargetBinding
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var transitionErr error
		if claim.Phase == CleanupPhaseValidated {
			renewed, binding, transitionErr = service.advanceRecoveryResultCleanupTx(ctx, tx, claim, now)
		} else {
			renewed, binding, transitionErr = service.resumeRecoveryResultCleanupTx(ctx, tx, claim, now)
		}
		return transitionErr
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoveryCleanupProgress{}, ctxErr
		}
		return RecoveryCleanupProgress{}, normalizeRecoveryCleanupAdvanceError(err)
	}
	if renewed.Phase == CleanupPhaseDeleted {
		permit, request, err := service.issueRecoveryResultCleanupRemovedValidationPermit(renewed, binding)
		if err != nil {
			return RecoveryCleanupProgress{}, err
		}
		validation, err := service.validateRecoveryRemovedCleanupTarget(ctx, permit, request)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RecoveryCleanupProgress{}, ctxErr
			}
			return RecoveryCleanupProgress{}, normalizeRecoveryCleanupTargetError(err)
		}
		if !validOwnedJobDirRemovalValidation(validation, request, binding.RootRevision) {
			return RecoveryCleanupProgress{}, ErrRecoveryResultCleanupConflict
		}
		now = service.now().UTC()
		if now.IsZero() {
			return RecoveryCleanupProgress{}, ErrInvalidResultLifecycle
		}
		if err := service.finalizeRecoveryResultCleanup(ctx, renewed, binding, validation, now); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RecoveryCleanupProgress{}, ctxErr
			}
			return RecoveryCleanupProgress{}, normalizeRecoveryCleanupAdvanceError(err)
		}
		return RecoveryCleanupProgress{Phase: CleanupPhaseTombstoned, Complete: true}, nil
	}

	permit, request, err := service.issueRecoveryResultCleanupRemovalPermit(renewed, binding)
	if err != nil {
		return RecoveryCleanupProgress{}, err
	}
	removal, err := service.target.RemoveOwnedJobDir(ctx, permit, request)
	if err != nil {
		closingErr := service.releaseRecoveryResultCleanupFailure(ctx, renewed, CleanupPhaseDeleteStarted)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoveryCleanupProgress{}, ctxErr
		}
		if closingErr != nil {
			return RecoveryCleanupProgress{}, normalizeRecoveryCleanupAdvanceError(closingErr)
		}
		return RecoveryCleanupProgress{}, normalizeRecoveryCleanupTargetError(err)
	}
	nextPhase := CleanupPhaseDeleteStarted
	if removal.Complete {
		nextPhase = CleanupPhaseDeleted
	}
	now = service.now().UTC()
	if now.IsZero() {
		return RecoveryCleanupProgress{}, ErrInvalidResultLifecycle
	}
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var renewalErr error
		renewed, renewalErr = service.transitionRecoveryResultCleanupTx(
			ctx, tx, renewed, CleanupPhaseDeleteStarted, nextPhase, now, nil,
		)
		return renewalErr
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoveryCleanupProgress{}, ctxErr
		}
		return RecoveryCleanupProgress{}, normalizeRecoveryCleanupAdvanceError(err)
	}
	return RecoveryCleanupProgress{
		Phase:          renewed.Phase,
		Complete:       removal.Complete,
		RemovedEntries: removal.RemovedEntries,
		ProgressDigest: removal.ProgressDigest,
	}, nil
}

func (service *ResultLifecycleService) AdvanceRecoveryWorkspaceCleanup(
	ctx context.Context,
	claim RecoveryWorkspaceCleanupClaim,
) (RecoveryCleanupProgress, error) {
	if service == nil || service.db == nil || service.now == nil || service.target == nil ||
		service.cleanupLeaseTTL <= 0 || !validRecoveryWorkspaceCleanupClaim(claim) ||
		(claim.Phase != CleanupPhaseValidated && claim.Phase != CleanupPhaseDeleteStarted && claim.Phase != CleanupPhaseDeleted) {
		return RecoveryCleanupProgress{}, ErrInvalidResultLifecycle
	}
	ctx = nonNilRecoveryContext(ctx)
	if err := ctx.Err(); err != nil {
		return RecoveryCleanupProgress{}, err
	}
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryCleanupProgress{}, ErrInvalidResultLifecycle
	}

	renewed := claim
	var binding recoveryCleanupTargetBinding
	var nodeID uint
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var transitionErr error
		if claim.Phase == CleanupPhaseValidated {
			renewed, binding, nodeID, transitionErr = service.advanceRecoveryWorkspaceCleanupTx(ctx, tx, claim, now)
		} else {
			renewed, binding, nodeID, transitionErr = service.resumeRecoveryWorkspaceCleanupTx(ctx, tx, claim, now)
		}
		return transitionErr
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoveryCleanupProgress{}, ctxErr
		}
		return RecoveryCleanupProgress{}, normalizeRecoveryCleanupAdvanceError(err)
	}
	if renewed.Phase == CleanupPhaseDeleted {
		permit, request, err := service.issueRecoveryWorkspaceCleanupRemovedValidationPermit(renewed, nodeID, binding)
		if err != nil {
			return RecoveryCleanupProgress{}, err
		}
		validation, err := service.validateRecoveryRemovedCleanupTarget(ctx, permit, request)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RecoveryCleanupProgress{}, ctxErr
			}
			return RecoveryCleanupProgress{}, normalizeRecoveryCleanupTargetError(err)
		}
		if !validOwnedJobDirRemovalValidation(validation, request, binding.RootRevision) {
			return RecoveryCleanupProgress{}, ErrRecoveryResultCleanupConflict
		}
		now = service.now().UTC()
		if now.IsZero() {
			return RecoveryCleanupProgress{}, ErrInvalidResultLifecycle
		}
		if err := service.finalizeRecoveryWorkspaceCleanup(ctx, renewed, nodeID, binding, validation, now); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return RecoveryCleanupProgress{}, ctxErr
			}
			return RecoveryCleanupProgress{}, normalizeRecoveryCleanupAdvanceError(err)
		}
		return RecoveryCleanupProgress{Phase: CleanupPhaseTombstoned, Complete: true}, nil
	}

	permit, request, err := service.issueRecoveryWorkspaceCleanupRemovalPermit(renewed, nodeID, binding)
	if err != nil {
		return RecoveryCleanupProgress{}, err
	}
	removal, err := service.target.RemoveOwnedJobDir(ctx, permit, request)
	if err != nil {
		closingErr := service.releaseRecoveryWorkspaceCleanupFailure(ctx, renewed, CleanupPhaseDeleteStarted)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoveryCleanupProgress{}, ctxErr
		}
		if closingErr != nil {
			return RecoveryCleanupProgress{}, normalizeRecoveryCleanupAdvanceError(closingErr)
		}
		return RecoveryCleanupProgress{}, normalizeRecoveryCleanupTargetError(err)
	}
	nextPhase := CleanupPhaseDeleteStarted
	if removal.Complete {
		nextPhase = CleanupPhaseDeleted
	}
	renewed, err = service.transitionRecoveryWorkspaceCleanup(
		ctx, renewed, CleanupPhaseDeleteStarted, nextPhase,
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoveryCleanupProgress{}, ctxErr
		}
		return RecoveryCleanupProgress{}, normalizeRecoveryCleanupAdvanceError(err)
	}
	return RecoveryCleanupProgress{
		Phase:          renewed.Phase,
		Complete:       removal.Complete,
		RemovedEntries: removal.RemovedEntries,
		ProgressDigest: removal.ProgressDigest,
	}, nil
}

func normalizeRecoveryCleanupAdvanceError(err error) error {
	if err == nil {
		return nil
	}
	for _, stable := range []error{
		ErrInvalidResultLifecycle, ErrRecoveryResultCleanupBusy,
		ErrRecoveryResultCleanupConflict, ErrRecoveryResultCleanupValidationFailed,
	} {
		if errors.Is(err, stable) {
			return stable
		}
	}
	return ErrRecoveryResultCleanupConflict
}

func normalizeRecoveryCleanupTargetError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, ErrInvalidTargetPermit) || errors.Is(err, ErrRecoveryTargetChanged) ||
		errors.Is(err, ErrRecoveryResultCleanupConflict) {
		return ErrRecoveryResultCleanupConflict
	}
	return ErrRecoveryTargetUnavailable
}

func (service *ResultLifecycleService) advanceRecoveryResultCleanupTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryResultCleanupClaim,
	now time.Time,
) (RecoveryResultCleanupClaim, recoveryCleanupTargetBinding, error) {
	if tx == nil || !validRecoveryResultCleanupClaim(claim) || claim.Phase != CleanupPhaseValidated || now.IsZero() {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrInvalidResultLifecycle
	}
	var job model.BackupAssetRecoveryJob
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !currentPublishedRecoveryCleanupJob(job) {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupConflict
	}
	leaseExpiresAt, err := service.renewRecoveryCleanupNodeLeaseTx(
		ctx, tx, claim.JobID, job.TargetNodeID, claim.WorkerID, claim.NodeLeaseID,
		claim.NodeFence, claim.LeaseExpiresAt, now,
	)
	if err != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, err
	}
	var resultSet model.BackupAssetRecoveryResultSet
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND job_id = ?", claim.ResultSetID, claim.JobID).Limit(1).Find(&resultSet)
	if loaded.Error != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !currentRecoveryResultCleanupClaim(resultSet, job, claim, CleanupPhaseValidated, now) {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupConflict
	}
	binding, err := loadRecoveryCleanupTargetBindingTx(ctx, tx, job, true)
	if err != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, err
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
		Where(`id = ? AND job_id = ? AND state = ? AND cleanup_phase = ?
			AND cleanup_owner = ? AND cleanup_lease_expires_at = ? AND cleanup_fence = ?
			AND node_lease_id = ? AND node_fence = ? AND cleanup_attempt = ? AND updated_at = ?`,
			resultSet.ID, resultSet.JobID, ResultSetStateRevoking, CleanupPhaseValidated,
			claim.WorkerID, claim.LeaseExpiresAt, claim.CleanupFence,
			claim.NodeLeaseID, claim.NodeFence, claim.CleanupAttempt, resultSet.UpdatedAt).
		Updates(map[string]any{
			"cleanup_phase": string(CleanupPhaseDeleteStarted), "cleanup_lease_expires_at": leaseExpiresAt,
			"updated_at": now,
		})
	if updated.Error != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupConflict
	}
	claim.Phase = CleanupPhaseDeleteStarted
	claim.LeaseExpiresAt = leaseExpiresAt
	return claim, binding, nil
}

func (service *ResultLifecycleService) advanceRecoveryWorkspaceCleanupTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryWorkspaceCleanupClaim,
	now time.Time,
) (RecoveryWorkspaceCleanupClaim, recoveryCleanupTargetBinding, uint, error) {
	if tx == nil || !validRecoveryWorkspaceCleanupClaim(claim) || claim.Phase != CleanupPhaseValidated || now.IsZero() {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, ErrInvalidResultLifecycle
	}
	var job model.BackupAssetRecoveryJob
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, loaded.Error
	}
	if loaded.RowsAffected != 1 || !currentRecoveryWorkspaceCleanupClaim(job, claim, CleanupPhaseValidated, now) {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, ErrRecoveryResultCleanupConflict
	}
	leaseExpiresAt, err := service.renewRecoveryCleanupNodeLeaseTx(
		ctx, tx, claim.JobID, job.TargetNodeID, claim.WorkerID, claim.NodeLeaseID,
		claim.NodeFence, claim.LeaseExpiresAt, now,
	)
	if err != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, err
	}
	binding, err := loadRecoveryCleanupTargetBindingTx(ctx, tx, job, false)
	if err != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, err
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
		Where(`id = ? AND state = ? AND target_mode = ? AND target_node_id = ?
			AND workspace_phase = ? AND workspace_cleanup_phase = ?
			AND workspace_cleanup_owner = ? AND workspace_cleanup_lease_expires_at = ?
			AND workspace_cleanup_fence = ? AND workspace_cleanup_node_lease_id = ?
			AND workspace_cleanup_node_fence = ? AND workspace_cleanup_attempt = ?
			AND updated_at = ?`,
			job.ID, job.State, job.TargetMode, job.TargetNodeID, job.WorkspacePhase,
			CleanupPhaseValidated, claim.WorkerID, claim.LeaseExpiresAt, claim.CleanupFence,
			claim.NodeLeaseID, claim.NodeFence, claim.CleanupAttempt, job.UpdatedAt).
		Updates(map[string]any{
			"workspace_cleanup_phase":            string(CleanupPhaseDeleteStarted),
			"workspace_cleanup_lease_expires_at": leaseExpiresAt, "updated_at": now,
		})
	if updated.Error != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, ErrRecoveryResultCleanupConflict
	}
	claim.Phase = CleanupPhaseDeleteStarted
	claim.LeaseExpiresAt = leaseExpiresAt
	return claim, binding, job.TargetNodeID, nil
}

func (service *ResultLifecycleService) resumeRecoveryResultCleanupTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryResultCleanupClaim,
	now time.Time,
) (RecoveryResultCleanupClaim, recoveryCleanupTargetBinding, error) {
	if tx == nil || !validRecoveryResultCleanupClaim(claim) ||
		(claim.Phase != CleanupPhaseDeleteStarted && claim.Phase != CleanupPhaseDeleted) || now.IsZero() {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrInvalidResultLifecycle
	}
	expectedPhase := claim.Phase
	var job model.BackupAssetRecoveryJob
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !currentPublishedRecoveryCleanupJob(job) {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupConflict
	}

	var resultSet model.BackupAssetRecoveryResultSet
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND job_id = ?", claim.ResultSetID, claim.JobID).Limit(1).Find(&resultSet)
	if loaded.Error != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || resultSet.CleanupLeaseExpiresAt == nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupConflict
	}
	current := claim
	current.LeaseExpiresAt = resultSet.CleanupLeaseExpiresAt.UTC()
	if !currentRecoveryResultCleanupClaim(resultSet, job, current, expectedPhase, now) {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupConflict
	}
	leaseExpiresAt, err := service.renewRecoveryCleanupNodeLeaseTx(
		ctx, tx, current.JobID, job.TargetNodeID, current.WorkerID, current.NodeLeaseID,
		current.NodeFence, current.LeaseExpiresAt, now,
	)
	if err != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, err
	}
	binding, err := loadRecoveryCleanupTargetBindingTx(ctx, tx, job, true)
	if err != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, err
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
		Where(`id = ? AND job_id = ? AND state = ? AND cleanup_phase = ?
			AND cleanup_owner = ? AND cleanup_lease_expires_at = ? AND cleanup_fence = ?
			AND node_lease_id = ? AND node_fence = ? AND cleanup_attempt = ? AND updated_at = ?`,
			resultSet.ID, resultSet.JobID, ResultSetStateRevoking, expectedPhase,
			current.WorkerID, current.LeaseExpiresAt, current.CleanupFence,
			current.NodeLeaseID, current.NodeFence, current.CleanupAttempt, resultSet.UpdatedAt).
		Updates(map[string]any{"cleanup_lease_expires_at": leaseExpiresAt, "updated_at": now})
	if updated.Error != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupConflict
	}
	current.LeaseExpiresAt = leaseExpiresAt
	return current, binding, nil
}

func (service *ResultLifecycleService) resumeRecoveryWorkspaceCleanupTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryWorkspaceCleanupClaim,
	now time.Time,
) (RecoveryWorkspaceCleanupClaim, recoveryCleanupTargetBinding, uint, error) {
	if tx == nil || !validRecoveryWorkspaceCleanupClaim(claim) ||
		(claim.Phase != CleanupPhaseDeleteStarted && claim.Phase != CleanupPhaseDeleted) || now.IsZero() {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, ErrInvalidResultLifecycle
	}
	expectedPhase := claim.Phase
	var job model.BackupAssetRecoveryJob
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, loaded.Error
	}
	if loaded.RowsAffected != 1 || job.WorkspaceCleanupLeaseExpiresAt == nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, ErrRecoveryResultCleanupConflict
	}
	current := claim
	current.LeaseExpiresAt = job.WorkspaceCleanupLeaseExpiresAt.UTC()
	if !currentRecoveryWorkspaceCleanupClaim(job, current, expectedPhase, now) {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, ErrRecoveryResultCleanupConflict
	}
	leaseExpiresAt, err := service.renewRecoveryCleanupNodeLeaseTx(
		ctx, tx, current.JobID, job.TargetNodeID, current.WorkerID, current.NodeLeaseID,
		current.NodeFence, current.LeaseExpiresAt, now,
	)
	if err != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, err
	}
	binding, err := loadRecoveryCleanupTargetBindingTx(ctx, tx, job, false)
	if err != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, err
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
		Where(`id = ? AND state = ? AND target_mode = ? AND target_node_id = ?
			AND workspace_phase = ? AND workspace_cleanup_phase = ?
			AND workspace_cleanup_owner = ? AND workspace_cleanup_lease_expires_at = ?
			AND workspace_cleanup_fence = ? AND workspace_cleanup_node_lease_id = ?
			AND workspace_cleanup_node_fence = ? AND workspace_cleanup_attempt = ?
			AND updated_at = ?`,
			job.ID, job.State, job.TargetMode, job.TargetNodeID,
			WorkspacePhaseCleanupDue, expectedPhase,
			current.WorkerID, current.LeaseExpiresAt, current.CleanupFence, current.NodeLeaseID,
			current.NodeFence, current.CleanupAttempt, job.UpdatedAt).
		Updates(map[string]any{"workspace_cleanup_lease_expires_at": leaseExpiresAt, "updated_at": now})
	if updated.Error != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, ErrRecoveryResultCleanupConflict
	}
	current.LeaseExpiresAt = leaseExpiresAt
	return current, binding, job.TargetNodeID, nil
}

func (service *ResultLifecycleService) issueRecoveryResultCleanupRemovalPermit(
	claim RecoveryResultCleanupClaim,
	binding recoveryCleanupTargetBinding,
) (TargetCleanupPermit, RemoveOwnedJobDirRequest, error) {
	permit := issueTargetCleanupPermitWithLiveValidator(TargetCleanupPermit{
		SchemaVersion: 1, Purpose: TargetPurposeCleanup, Operation: TargetCleanupRemoveOwnedJobDir,
		ResourceKind: CleanupResourceResultSet, ResourceID: claim.ResultSetID, JobID: claim.JobID,
		CleanupOwner: claim.WorkerID, CleanupFence: claim.CleanupFence, CleanupAttempt: claim.CleanupAttempt,
		NodeID: binding.NodeID, NodeLeaseID: claim.NodeLeaseID, NodeFence: claim.NodeFence,
		RootID: binding.Object.RootID, RootLocatorDigest: binding.Object.RootLocatorDigest,
		TargetPathDigest: binding.Object.TargetPathDigest, RootRevision: binding.RootRevision,
		MarkerBindingDigest: binding.MarkerBindingDigest, UseLatchID: RecoverySchemaUseLatchID,
		MarkerCreatorID: binding.MarkerCreatorID, MarkerCreatorFence: binding.MarkerCreatorFence,
		ExpiresAt: claim.LeaseExpiresAt,
	}, service.validateLiveCleanupPermit, binding.SessionBinding)
	request := RemoveOwnedJobDirRequest{Object: binding.Object, MarkerBindingDigest: binding.MarkerBindingDigest}
	if permit.ValidateAt(service.now().UTC()) != nil || permit.proof == nil || permit.proof.validateLive == nil {
		return TargetCleanupPermit{}, RemoveOwnedJobDirRequest{}, ErrRecoveryResultCleanupConflict
	}
	return permit, request, nil
}

func (service *ResultLifecycleService) issueRecoveryWorkspaceCleanupRemovalPermit(
	claim RecoveryWorkspaceCleanupClaim,
	nodeID uint,
	binding recoveryCleanupTargetBinding,
) (TargetCleanupPermit, RemoveOwnedJobDirRequest, error) {
	permit := issueTargetCleanupPermitWithLiveValidator(TargetCleanupPermit{
		SchemaVersion: 1, Purpose: TargetPurposeCleanup, Operation: TargetCleanupRemoveOwnedJobDir,
		ResourceKind: CleanupResourceWorkspace, ResourceID: claim.JobID, JobID: claim.JobID,
		CleanupOwner: claim.WorkerID, CleanupFence: claim.CleanupFence, CleanupAttempt: claim.CleanupAttempt,
		NodeID: nodeID, NodeLeaseID: claim.NodeLeaseID, NodeFence: claim.NodeFence,
		RootID: binding.Object.RootID, RootLocatorDigest: binding.Object.RootLocatorDigest,
		TargetPathDigest: binding.Object.TargetPathDigest, RootRevision: binding.RootRevision,
		MarkerBindingDigest: binding.MarkerBindingDigest, UseLatchID: RecoverySchemaUseLatchID,
		MarkerCreatorID: binding.MarkerCreatorID, MarkerCreatorFence: binding.MarkerCreatorFence,
		ExpiresAt: claim.LeaseExpiresAt,
	}, service.validateLiveCleanupPermit, binding.SessionBinding)
	request := RemoveOwnedJobDirRequest{Object: binding.Object, MarkerBindingDigest: binding.MarkerBindingDigest}
	if permit.ValidateAt(service.now().UTC()) != nil || permit.proof == nil || permit.proof.validateLive == nil {
		return TargetCleanupPermit{}, RemoveOwnedJobDirRequest{}, ErrRecoveryResultCleanupConflict
	}
	return permit, request, nil
}

func (service *ResultLifecycleService) issueRecoveryResultCleanupRemovedValidationPermit(
	claim RecoveryResultCleanupClaim,
	binding recoveryCleanupTargetBinding,
) (TargetCleanupPermit, RemoveOwnedJobDirRequest, error) {
	permit := issueTargetCleanupPermit(TargetCleanupPermit{
		SchemaVersion: 1, Purpose: TargetPurposeCleanup, Operation: TargetCleanupValidateRemovedJobDir,
		ResourceKind: CleanupResourceResultSet, ResourceID: claim.ResultSetID, JobID: claim.JobID,
		CleanupOwner: claim.WorkerID, CleanupFence: claim.CleanupFence, CleanupAttempt: claim.CleanupAttempt,
		NodeID: binding.NodeID, NodeLeaseID: claim.NodeLeaseID, NodeFence: claim.NodeFence,
		RootID: binding.Object.RootID, RootLocatorDigest: binding.Object.RootLocatorDigest,
		TargetPathDigest: binding.Object.TargetPathDigest, RootRevision: binding.RootRevision,
		MarkerBindingDigest: binding.MarkerBindingDigest, UseLatchID: RecoverySchemaUseLatchID,
		MarkerCreatorID: binding.MarkerCreatorID, MarkerCreatorFence: binding.MarkerCreatorFence,
		ExpiresAt: claim.LeaseExpiresAt,
	}, binding.SessionBinding)
	request := RemoveOwnedJobDirRequest{Object: binding.Object, MarkerBindingDigest: binding.MarkerBindingDigest}
	if permit.ValidateAt(service.now().UTC()) != nil || permit.Operation != TargetCleanupValidateRemovedJobDir {
		return TargetCleanupPermit{}, RemoveOwnedJobDirRequest{}, ErrRecoveryResultCleanupConflict
	}
	return permit, request, nil
}

func (service *ResultLifecycleService) issueRecoveryWorkspaceCleanupRemovedValidationPermit(
	claim RecoveryWorkspaceCleanupClaim,
	nodeID uint,
	binding recoveryCleanupTargetBinding,
) (TargetCleanupPermit, RemoveOwnedJobDirRequest, error) {
	permit := issueTargetCleanupPermit(TargetCleanupPermit{
		SchemaVersion: 1, Purpose: TargetPurposeCleanup, Operation: TargetCleanupValidateRemovedJobDir,
		ResourceKind: CleanupResourceWorkspace, ResourceID: claim.JobID, JobID: claim.JobID,
		CleanupOwner: claim.WorkerID, CleanupFence: claim.CleanupFence, CleanupAttempt: claim.CleanupAttempt,
		NodeID: nodeID, NodeLeaseID: claim.NodeLeaseID, NodeFence: claim.NodeFence,
		RootID: binding.Object.RootID, RootLocatorDigest: binding.Object.RootLocatorDigest,
		TargetPathDigest: binding.Object.TargetPathDigest, RootRevision: binding.RootRevision,
		MarkerBindingDigest: binding.MarkerBindingDigest, UseLatchID: RecoverySchemaUseLatchID,
		MarkerCreatorID: binding.MarkerCreatorID, MarkerCreatorFence: binding.MarkerCreatorFence,
		ExpiresAt: claim.LeaseExpiresAt,
	}, binding.SessionBinding)
	request := RemoveOwnedJobDirRequest{Object: binding.Object, MarkerBindingDigest: binding.MarkerBindingDigest}
	if permit.ValidateAt(service.now().UTC()) != nil || permit.Operation != TargetCleanupValidateRemovedJobDir {
		return TargetCleanupPermit{}, RemoveOwnedJobDirRequest{}, ErrRecoveryResultCleanupConflict
	}
	return permit, request, nil
}

func (service *ResultLifecycleService) validateRecoveryRemovedCleanupTarget(
	ctx context.Context,
	permit TargetCleanupPermit,
	request RemoveOwnedJobDirRequest,
) (OwnedJobDirRemovalValidation, error) {
	now := service.now().UTC()
	if now.IsZero() || !permit.ExpiresAt.UTC().After(now) {
		return OwnedJobDirRemovalValidation{}, ErrRecoveryResultCleanupValidationFailed
	}
	halfLife := permit.ExpiresAt.UTC().Sub(now) / 2
	deadline := now.Add(halfLife).UTC()
	if halfLife <= 0 || !deadline.After(now) || !deadline.Before(permit.ExpiresAt.UTC()) {
		return OwnedJobDirRemovalValidation{}, ErrRecoveryResultCleanupValidationFailed
	}
	targetCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return service.target.ValidateOwnedJobDirRemoved(targetCtx, permit, request)
}

func validOwnedJobDirRemovalValidation(
	validation OwnedJobDirRemovalValidation,
	request RemoveOwnedJobDirRequest,
	rootRevision string,
) bool {
	return validation.Object == request.Object && validation.RootRevision == rootRevision &&
		validOpaqueRevision(validation.TargetRevision)
}

func (service *ResultLifecycleService) finalizeRecoveryResultCleanup(
	ctx context.Context,
	claim RecoveryResultCleanupClaim,
	binding recoveryCleanupTargetBinding,
	validation OwnedJobDirRemovalValidation,
	now time.Time,
) error {
	if service == nil || service.db == nil || !validRecoveryResultCleanupClaim(claim) ||
		claim.Phase != CleanupPhaseDeleted || now.IsZero() ||
		!validOwnedJobDirRemovalValidation(validation, RemoveOwnedJobDirRequest{Object: binding.Object, MarkerBindingDigest: binding.MarkerBindingDigest}, binding.RootRevision) {
		return ErrInvalidResultLifecycle
	}
	return service.db.WithContext(nonNilRecoveryContext(ctx)).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !currentPublishedRecoveryCleanupJob(job) {
			return ErrRecoveryResultCleanupConflict
		}
		nodeLease, err := lockRecoveryCleanupNodeLeaseTx(
			ctx, tx, claim.JobID, job.TargetNodeID, claim.WorkerID, claim.NodeLeaseID,
			claim.NodeFence, claim.LeaseExpiresAt, now,
		)
		if err != nil {
			return err
		}
		var resultSet model.BackupAssetRecoveryResultSet
		loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ?", claim.ResultSetID, claim.JobID).Limit(1).Find(&resultSet)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !currentRecoveryResultCleanupClaim(resultSet, job, claim, CleanupPhaseDeleted, now) {
			return ErrRecoveryResultCleanupConflict
		}
		closingBinding, err := loadRecoveryCleanupTargetBindingTx(ctx, tx, job, true)
		if err != nil {
			return err
		}
		if !sameRecoveryCleanupTargetBinding(closingBinding, binding) ||
			!validOwnedJobDirRemovalValidation(validation,
				RemoveOwnedJobDirRequest{Object: closingBinding.Object, MarkerBindingDigest: closingBinding.MarkerBindingDigest},
				closingBinding.RootRevision) {
			return ErrRecoveryResultCleanupConflict
		}
		if err := releaseRecoveryResultCleanupNodeLeaseTx(ctx, tx, nodeLease, now); err != nil {
			return err
		}
		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
			Where(`id = ? AND job_id = ? AND state = ? AND cleanup_phase = ?
				AND cleanup_owner = ? AND cleanup_lease_expires_at = ? AND cleanup_fence = ?
				AND node_lease_id = ? AND node_fence = ? AND cleanup_attempt = ? AND updated_at = ?`,
				resultSet.ID, resultSet.JobID, ResultSetStateRevoking, CleanupPhaseDeleted,
				claim.WorkerID, claim.LeaseExpiresAt, claim.CleanupFence,
				claim.NodeLeaseID, claim.NodeFence, claim.CleanupAttempt, resultSet.UpdatedAt).
			Updates(map[string]any{
				"state": string(ResultSetStateCleaned), "cleanup_phase": string(CleanupPhaseTombstoned),
				"cleanup_owner": "", "cleanup_lease_expires_at": nil,
				"node_lease_id": nil, "node_fence": uint64(0), "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryResultCleanupConflict
		}
		return nil
	})
}

func (service *ResultLifecycleService) finalizeRecoveryWorkspaceCleanup(
	ctx context.Context,
	claim RecoveryWorkspaceCleanupClaim,
	nodeID uint,
	binding recoveryCleanupTargetBinding,
	validation OwnedJobDirRemovalValidation,
	now time.Time,
) error {
	if service == nil || service.db == nil || !validRecoveryWorkspaceCleanupClaim(claim) ||
		claim.Phase != CleanupPhaseDeleted || nodeID == 0 || now.IsZero() ||
		!validOwnedJobDirRemovalValidation(validation, RemoveOwnedJobDirRequest{Object: binding.Object, MarkerBindingDigest: binding.MarkerBindingDigest}, binding.RootRevision) {
		return ErrInvalidResultLifecycle
	}
	return service.db.WithContext(nonNilRecoveryContext(ctx)).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !currentRecoveryWorkspaceCleanupClaim(job, claim, CleanupPhaseDeleted, now) || job.TargetNodeID != nodeID {
			return ErrRecoveryResultCleanupConflict
		}
		nodeLease, err := lockRecoveryCleanupNodeLeaseTx(
			ctx, tx, claim.JobID, nodeID, claim.WorkerID, claim.NodeLeaseID,
			claim.NodeFence, claim.LeaseExpiresAt, now,
		)
		if err != nil {
			return err
		}
		var resultSetCount int64
		if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
			Where("job_id = ?", job.ID).Count(&resultSetCount).Error; err != nil {
			return err
		}
		if resultSetCount != 0 {
			return ErrRecoveryResultCleanupConflict
		}
		closingBinding, err := loadRecoveryCleanupTargetBindingTx(ctx, tx, job, false)
		if err != nil {
			return err
		}
		if !sameRecoveryCleanupTargetBinding(closingBinding, binding) ||
			!validOwnedJobDirRemovalValidation(validation,
				RemoveOwnedJobDirRequest{Object: closingBinding.Object, MarkerBindingDigest: closingBinding.MarkerBindingDigest},
				closingBinding.RootRevision) {
			return ErrRecoveryResultCleanupConflict
		}
		if err := releaseRecoveryResultCleanupNodeLeaseTx(ctx, tx, nodeLease, now); err != nil {
			return err
		}
		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where(`id = ? AND state = ? AND target_mode = ? AND target_node_id = ?
				AND workspace_phase = ? AND workspace_cleanup_phase = ?
				AND workspace_cleanup_owner = ? AND workspace_cleanup_lease_expires_at = ?
				AND workspace_cleanup_fence = ? AND workspace_cleanup_node_lease_id = ?
				AND workspace_cleanup_node_fence = ? AND workspace_cleanup_attempt = ?
				AND updated_at = ?`,
				job.ID, job.State, job.TargetMode, job.TargetNodeID, WorkspacePhaseCleanupDue,
				CleanupPhaseDeleted, claim.WorkerID, claim.LeaseExpiresAt, claim.CleanupFence,
				claim.NodeLeaseID, claim.NodeFence, claim.CleanupAttempt, job.UpdatedAt).
			Updates(map[string]any{
				"workspace_phase":         string(WorkspacePhaseCleaned),
				"workspace_cleanup_phase": string(CleanupPhaseTombstoned),
				"workspace_cleanup_owner": "", "workspace_cleanup_lease_expires_at": nil,
				"workspace_cleanup_node_lease_id": nil, "workspace_cleanup_node_fence": uint64(0),
				"updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryResultCleanupConflict
		}
		return nil
	})
}

func (service *ResultLifecycleService) validateLiveCleanupPermit(
	ctx context.Context,
	permit TargetCleanupPermit,
) error {
	ctx = nonNilRecoveryContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil || service.db == nil || service.now == nil {
		return ErrRecoveryResultCleanupConflict
	}
	now := service.now().UTC()
	if now.IsZero() || permit.ValidateAt(now) != nil || permit.Operation != TargetCleanupRemoveOwnedJobDir ||
		permit.proof == nil || permit.proof.validateLive == nil {
		return ErrRecoveryResultCleanupConflict
	}
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", permit.JobID).Limit(1).Find(&job)
		if loaded.Error != nil || loaded.RowsAffected != 1 {
			return ErrRecoveryResultCleanupConflict
		}
		var binding recoveryCleanupTargetBinding
		var err error
		switch permit.ResourceKind {
		case CleanupResourceResultSet:
			if !currentPublishedRecoveryCleanupJob(job) {
				return ErrRecoveryResultCleanupConflict
			}
			binding, err = loadRecoveryCleanupTargetBindingTx(ctx, tx, job, true)
			if err != nil {
				return ErrRecoveryResultCleanupConflict
			}
			var resultSet model.BackupAssetRecoveryResultSet
			loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
				Where("id = ? AND job_id = ?", permit.ResourceID, permit.JobID).Limit(1).Find(&resultSet)
			if loaded.Error != nil || loaded.RowsAffected != 1 {
				return ErrRecoveryResultCleanupConflict
			}
			claim := RecoveryResultCleanupClaim{
				ResultSetID: permit.ResourceID, JobID: permit.JobID, WorkerID: permit.CleanupOwner,
				CleanupFence: permit.CleanupFence, CleanupAttempt: permit.CleanupAttempt,
				NodeLeaseID: permit.NodeLeaseID, NodeFence: permit.NodeFence,
				LeaseExpiresAt: permit.ExpiresAt, Phase: CleanupPhaseDeleteStarted,
			}
			if !currentRecoveryResultCleanupClaim(resultSet, job, claim, CleanupPhaseDeleteStarted, now) {
				return ErrRecoveryResultCleanupConflict
			}
		case CleanupResourceWorkspace:
			if permit.ResourceID != permit.JobID || !currentRecoveryWorkspaceCleanupClaim(
				job,
				RecoveryWorkspaceCleanupClaim{
					JobID: permit.JobID, WorkerID: permit.CleanupOwner,
					CleanupFence: permit.CleanupFence, CleanupAttempt: permit.CleanupAttempt,
					NodeLeaseID: permit.NodeLeaseID, NodeFence: permit.NodeFence,
					LeaseExpiresAt: permit.ExpiresAt, Phase: CleanupPhaseDeleteStarted,
				},
				CleanupPhaseDeleteStarted,
				now,
			) {
				return ErrRecoveryResultCleanupConflict
			}
			binding, err = loadRecoveryCleanupTargetBindingTx(ctx, tx, job, false)
			if err != nil {
				return ErrRecoveryResultCleanupConflict
			}
		default:
			return ErrRecoveryResultCleanupConflict
		}
		if !cleanupPermitMatchesBinding(permit, binding) {
			return ErrRecoveryResultCleanupConflict
		}
		if _, err := lockRecoveryCleanupNodeLeaseTx(
			ctx, tx, permit.JobID, permit.NodeID, permit.CleanupOwner, permit.NodeLeaseID,
			permit.NodeFence, permit.ExpiresAt, now,
		); err != nil {
			return ErrRecoveryResultCleanupConflict
		}
		return nil
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return normalizeRecoveryCleanupAdvanceError(err)
}

func cleanupPermitMatchesBinding(permit TargetCleanupPermit, binding recoveryCleanupTargetBinding) bool {
	return permit.NodeID == binding.NodeID && permit.RootID == binding.Object.RootID &&
		permit.RootLocatorDigest == binding.Object.RootLocatorDigest &&
		permit.TargetPathDigest == binding.Object.TargetPathDigest && permit.RootRevision == binding.RootRevision &&
		permit.MarkerBindingDigest == binding.MarkerBindingDigest &&
		permit.MarkerCreatorID == binding.MarkerCreatorID && permit.MarkerCreatorFence == binding.MarkerCreatorFence &&
		permit.proof != nil && permit.proof.sessionBinding == binding.SessionBinding
}

func (service *ResultLifecycleService) failRecoveryResultCleanupValidation(
	ctx context.Context,
	claim RecoveryResultCleanupClaim,
) (RecoveryResultCleanupClaim, error) {
	if service == nil || service.db == nil || service.now == nil ||
		!validRecoveryResultCleanupClaim(claim) || claim.Phase != CleanupPhaseDrained {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}
	if err := service.releaseRecoveryResultCleanupFailure(ctx, claim, CleanupPhaseDrained); err != nil {
		return claim, err
	}
	return claim, ErrRecoveryResultCleanupValidationFailed
}

func (service *ResultLifecycleService) failRecoveryWorkspaceCleanupValidation(
	ctx context.Context,
	claim RecoveryWorkspaceCleanupClaim,
) (RecoveryWorkspaceCleanupClaim, error) {
	if service == nil || service.db == nil || service.now == nil ||
		!validRecoveryWorkspaceCleanupClaim(claim) || claim.Phase != CleanupPhaseDrained {
		return RecoveryWorkspaceCleanupClaim{}, ErrInvalidResultLifecycle
	}
	if err := service.releaseRecoveryWorkspaceCleanupFailure(ctx, claim, CleanupPhaseDrained); err != nil {
		return claim, err
	}
	return claim, ErrRecoveryResultCleanupValidationFailed
}

func (service *ResultLifecycleService) releaseRecoveryResultCleanupFailure(
	ctx context.Context,
	claim RecoveryResultCleanupClaim,
	expectedPhase CleanupPhase,
) error {
	if service == nil || service.db == nil || service.now == nil ||
		!validRecoveryResultCleanupClaim(claim) || claim.Phase != expectedPhase ||
		(expectedPhase != CleanupPhaseDrained && expectedPhase != CleanupPhaseDeleteStarted) {
		return ErrInvalidResultLifecycle
	}
	failureCtx, cancel := context.WithTimeout(
		context.WithoutCancel(nonNilRecoveryContext(ctx)),
		recoveryCleanupFailureProjectionTimeout,
	)
	defer cancel()
	now := service.now().UTC()
	if now.IsZero() {
		return ErrInvalidResultLifecycle
	}

	return service.db.WithContext(failureCtx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(failureCtx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !currentPublishedRecoveryCleanupJob(job) {
			return ErrRecoveryResultCleanupConflict
		}

		nodeLease, err := lockRecoveryCleanupNodeLeaseTx(
			failureCtx, tx, claim.JobID, job.TargetNodeID, claim.WorkerID,
			claim.NodeLeaseID, claim.NodeFence, claim.LeaseExpiresAt, now,
		)
		if err != nil {
			return err
		}

		var resultSet model.BackupAssetRecoveryResultSet
		loaded = tx.WithContext(failureCtx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_id = ?", claim.ResultSetID, claim.JobID).Limit(1).Find(&resultSet)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 ||
			!currentRecoveryResultCleanupClaim(resultSet, job, claim, expectedPhase, now) {
			return ErrRecoveryResultCleanupConflict
		}
		if err := releaseRecoveryResultCleanupNodeLeaseTx(failureCtx, tx, nodeLease, now); err != nil {
			return err
		}

		updated := tx.WithContext(failureCtx).Model(&model.BackupAssetRecoveryResultSet{}).
			Where(`id = ? AND job_id = ? AND state = ? AND cleanup_phase = ?
				AND cleanup_owner = ? AND cleanup_lease_expires_at = ? AND cleanup_fence = ?
				AND node_lease_id = ? AND node_fence = ? AND cleanup_attempt = ? AND updated_at = ?`,
				resultSet.ID, resultSet.JobID, ResultSetStateRevoking, expectedPhase,
				claim.WorkerID, claim.LeaseExpiresAt, claim.CleanupFence,
				claim.NodeLeaseID, claim.NodeFence, claim.CleanupAttempt, resultSet.UpdatedAt).
			Updates(map[string]any{
				"state": string(ResultSetStateCleanupFailed), "cleanup_phase": string(expectedPhase),
				"cleanup_owner": "", "cleanup_lease_expires_at": nil,
				"node_lease_id": nil, "node_fence": uint64(0), "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryResultCleanupConflict
		}
		return nil
	})
}

func (service *ResultLifecycleService) releaseRecoveryWorkspaceCleanupFailure(
	ctx context.Context,
	claim RecoveryWorkspaceCleanupClaim,
	expectedPhase CleanupPhase,
) error {
	if service == nil || service.db == nil || service.now == nil ||
		!validRecoveryWorkspaceCleanupClaim(claim) || claim.Phase != expectedPhase ||
		(expectedPhase != CleanupPhaseDrained && expectedPhase != CleanupPhaseDeleteStarted) {
		return ErrInvalidResultLifecycle
	}
	failureCtx, cancel := context.WithTimeout(
		context.WithoutCancel(nonNilRecoveryContext(ctx)),
		recoveryCleanupFailureProjectionTimeout,
	)
	defer cancel()
	now := service.now().UTC()
	if now.IsZero() {
		return ErrInvalidResultLifecycle
	}

	return service.db.WithContext(failureCtx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(failureCtx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 ||
			!currentRecoveryWorkspaceCleanupClaim(job, claim, expectedPhase, now) {
			return ErrRecoveryResultCleanupConflict
		}

		nodeLease, err := lockRecoveryCleanupNodeLeaseTx(
			failureCtx, tx, claim.JobID, job.TargetNodeID, claim.WorkerID,
			claim.NodeLeaseID, claim.NodeFence, claim.LeaseExpiresAt, now,
		)
		if err != nil {
			return err
		}
		if err := releaseRecoveryResultCleanupNodeLeaseTx(failureCtx, tx, nodeLease, now); err != nil {
			return err
		}

		updated := tx.WithContext(failureCtx).Model(&model.BackupAssetRecoveryJob{}).
			Where(`id = ? AND state = ? AND target_mode = ? AND target_node_id = ?
				AND workspace_phase = ? AND workspace_cleanup_phase = ?
				AND workspace_cleanup_owner = ? AND workspace_cleanup_lease_expires_at = ?
				AND workspace_cleanup_fence = ? AND workspace_cleanup_node_lease_id = ?
				AND workspace_cleanup_node_fence = ? AND workspace_cleanup_attempt = ?
				AND updated_at = ?`,
				job.ID, job.State, job.TargetMode, job.TargetNodeID,
				WorkspacePhaseCleanupDue, expectedPhase,
				claim.WorkerID, claim.LeaseExpiresAt, claim.CleanupFence, claim.NodeLeaseID,
				claim.NodeFence, claim.CleanupAttempt, job.UpdatedAt).
			Updates(map[string]any{
				"workspace_phase":         string(WorkspacePhaseCleanupDue),
				"workspace_cleanup_phase": string(expectedPhase),
				"workspace_cleanup_owner": "", "workspace_cleanup_lease_expires_at": nil,
				"workspace_cleanup_node_lease_id": nil, "workspace_cleanup_node_fence": uint64(0),
				"updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryResultCleanupConflict
		}
		return nil
	})
}

func (service *ResultLifecycleService) renewRecoveryResultCleanupValidationTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryResultCleanupClaim,
	nextPhase CleanupPhase,
	now time.Time,
) (RecoveryResultCleanupClaim, recoveryCleanupTargetBinding, error) {
	if tx == nil || !validRecoveryResultCleanupClaim(claim) || claim.Phase != CleanupPhaseDrained ||
		(nextPhase != CleanupPhaseDrained && nextPhase != CleanupPhaseValidated) || now.IsZero() {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrInvalidResultLifecycle
	}

	var job model.BackupAssetRecoveryJob
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !currentPublishedRecoveryCleanupJob(job) {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupConflict
	}

	leaseExpiresAt, err := service.renewRecoveryCleanupNodeLeaseTx(
		ctx, tx, claim.JobID, job.TargetNodeID, claim.WorkerID, claim.NodeLeaseID,
		claim.NodeFence, claim.LeaseExpiresAt, now,
	)
	if err != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, err
	}

	var resultSet model.BackupAssetRecoveryResultSet
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND job_id = ?", claim.ResultSetID, claim.JobID).Limit(1).Find(&resultSet)
	if loaded.Error != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, loaded.Error
	}
	if loaded.RowsAffected != 1 ||
		!currentRecoveryResultCleanupClaim(resultSet, job, claim, CleanupPhaseDrained, now) {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupConflict
	}
	var resultSetCount int64
	if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
		Where("job_id = ?", job.ID).Count(&resultSetCount).Error; err != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, err
	}
	if resultSetCount != 1 {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupValidationFailed
	}
	binding, err := loadRecoveryCleanupTargetBindingTx(ctx, tx, job, true)
	if err != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, err
	}

	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
		Where(`id = ? AND job_id = ? AND state = ? AND cleanup_phase = ?
			AND cleanup_owner = ? AND cleanup_lease_expires_at = ? AND cleanup_fence = ?
			AND node_lease_id = ? AND node_fence = ? AND cleanup_attempt = ? AND updated_at = ?`,
			resultSet.ID, resultSet.JobID, ResultSetStateRevoking, CleanupPhaseDrained,
			claim.WorkerID, claim.LeaseExpiresAt, claim.CleanupFence,
			claim.NodeLeaseID, claim.NodeFence, claim.CleanupAttempt, resultSet.UpdatedAt).
		Updates(map[string]any{
			"cleanup_phase": string(nextPhase), "cleanup_lease_expires_at": leaseExpiresAt,
			"updated_at": now,
		})
	if updated.Error != nil {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RecoveryResultCleanupClaim{}, recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupConflict
	}
	claim.Phase = nextPhase
	claim.LeaseExpiresAt = leaseExpiresAt
	return claim, binding, nil
}

func (service *ResultLifecycleService) renewRecoveryWorkspaceCleanupValidationTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryWorkspaceCleanupClaim,
	nextPhase CleanupPhase,
	now time.Time,
) (RecoveryWorkspaceCleanupClaim, recoveryCleanupTargetBinding, uint, error) {
	if tx == nil || !validRecoveryWorkspaceCleanupClaim(claim) || claim.Phase != CleanupPhaseDrained ||
		(nextPhase != CleanupPhaseDrained && nextPhase != CleanupPhaseValidated) || now.IsZero() {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, ErrInvalidResultLifecycle
	}

	var job model.BackupAssetRecoveryJob
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, loaded.Error
	}
	if loaded.RowsAffected != 1 ||
		!currentRecoveryWorkspaceCleanupClaim(job, claim, CleanupPhaseDrained, now) {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0,
			ErrRecoveryResultCleanupConflict
	}

	leaseExpiresAt, err := service.renewRecoveryCleanupNodeLeaseTx(
		ctx, tx, claim.JobID, job.TargetNodeID, claim.WorkerID, claim.NodeLeaseID,
		claim.NodeFence, claim.LeaseExpiresAt, now,
	)
	if err != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, err
	}
	var resultSetCount int64
	if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
		Where("job_id = ?", job.ID).Count(&resultSetCount).Error; err != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, err
	}
	if resultSetCount != 0 {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0,
			ErrRecoveryResultCleanupValidationFailed
	}
	binding, err := loadRecoveryCleanupTargetBindingTx(ctx, tx, job, false)
	if err != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, err
	}

	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
		Where(`id = ? AND state = ? AND target_mode = ? AND target_node_id = ?
			AND workspace_phase = ? AND workspace_cleanup_phase = ?
			AND workspace_cleanup_owner = ? AND workspace_cleanup_lease_expires_at = ?
			AND workspace_cleanup_fence = ? AND workspace_cleanup_node_lease_id = ?
			AND workspace_cleanup_node_fence = ? AND workspace_cleanup_attempt = ?
			AND updated_at = ?`,
			job.ID, job.State, job.TargetMode, job.TargetNodeID,
			job.WorkspacePhase, CleanupPhaseDrained, claim.WorkerID, claim.LeaseExpiresAt,
			claim.CleanupFence, claim.NodeLeaseID, claim.NodeFence, claim.CleanupAttempt,
			job.UpdatedAt).
		Updates(map[string]any{
			"workspace_cleanup_phase":            string(nextPhase),
			"workspace_cleanup_lease_expires_at": leaseExpiresAt,
			"updated_at":                         now,
		})
	if updated.Error != nil {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RecoveryWorkspaceCleanupClaim{}, recoveryCleanupTargetBinding{}, 0,
			ErrRecoveryResultCleanupConflict
	}
	claim.Phase = nextPhase
	claim.LeaseExpiresAt = leaseExpiresAt
	return claim, binding, job.TargetNodeID, nil
}

func loadRecoveryCleanupTargetBindingTx(
	ctx context.Context,
	tx *gorm.DB,
	job model.BackupAssetRecoveryJob,
	requireMarkerValidation bool,
) (recoveryCleanupTargetBinding, error) {
	if tx == nil || !validOpaqueID(job.ID) || !validOpaqueID(job.PlanID) {
		return recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupValidationFailed
	}
	var plan model.BackupAssetRecoveryPlan
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", job.PlanID).Limit(1).Find(&plan)
	if loaded.Error != nil {
		return recoveryCleanupTargetBinding{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || plan.ID != job.PlanID || PlanState(plan.State) != PlanStateExecuted ||
		TargetMode(plan.TargetMode) != TargetModeIsolated || plan.TargetMode != job.TargetMode ||
		plan.TargetNodeID != job.TargetNodeID || plan.TargetRootID != job.TargetRootID ||
		plan.RootLocatorDigest != job.RootLocatorDigest || plan.BindingDigest != job.PlanBindingDigest ||
		!nonEmpty(plan.EncryptedTargetRootLocator) || !validOpaqueRevision(plan.RootRevision) ||
		!validBoundedOpaque(job.TargetRootID, targetRootIDMax) || !validDigest(job.RootLocatorDigest) ||
		!validRecoveryWorkerID(job.WorkspaceOwner) || job.WorkspaceFence == 0 ||
		!validDigest(job.WorkspaceMarkerBindingDigest) || job.PlaintextDeadline == nil ||
		job.PlaintextDeadline.IsZero() ||
		(requireMarkerValidation && !validRecoveryMarkerValidation(job)) ||
		(!requireMarkerValidation && !emptyRecoveryMarkerValidation(job) && !validRecoveryMarkerValidation(job)) {
		return recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupValidationFailed
	}
	sessionBinding, err := newRecoveryTargetSessionBinding(plan)
	if err != nil {
		return recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupValidationFailed
	}

	workspaceLocator := recoveryWorkspaceLocatorDirectory + "/" + job.ID
	if job.EncryptedWorkspaceRelativeLocator != workspaceLocator ||
		job.WorkspaceBindingDigest != recoveryWorkspaceBindingDigest(plan, job.ID, workspaceLocator) {
		return recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupValidationFailed
	}
	pathDigest, err := TargetPathDigest(job.TargetRootID, job.RootLocatorDigest, workspaceLocator)
	if err != nil {
		return recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupValidationFailed
	}
	object := TargetObjectRef{
		RootID: job.TargetRootID, RootLocatorDigest: job.RootLocatorDigest,
		TargetPathDigest: pathDigest, PrivateRelativeLocator: workspaceLocator,
	}
	if !object.valid() {
		return recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupValidationFailed
	}

	var latch model.BackupAssetRecoveryEvidence
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", recoverySchemaUseLatchRowID).Limit(1).Find(&latch)
	if loaded.Error != nil {
		return recoveryCleanupTargetBinding{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !validRecoverySchemaUseLatch(latch) {
		return recoveryCleanupTargetBinding{}, ErrRecoveryResultCleanupValidationFailed
	}
	return recoveryCleanupTargetBinding{
		NodeID: job.TargetNodeID, Object: object,
		MarkerBindingDigest: job.WorkspaceMarkerBindingDigest,
		MarkerCreatorID:     job.WorkspaceOwner, MarkerCreatorFence: job.WorkspaceFence,
		RootRevision: plan.RootRevision, SessionBinding: sessionBinding,
	}, nil
}

func (service *ResultLifecycleService) observeRecoveryCleanupTarget(
	ctx context.Context,
	permit TargetCleanupPermit,
	request ValidateOwnedJobDirRequest,
) (OwnedJobDirValidation, error) {
	now := service.now().UTC()
	if now.IsZero() || !permit.ExpiresAt.UTC().After(now) {
		return OwnedJobDirValidation{}, ErrRecoveryResultCleanupValidationFailed
	}
	halfLife := permit.ExpiresAt.UTC().Sub(now) / 2
	deadline := now.Add(halfLife).UTC()
	if halfLife <= 0 || !deadline.After(now) || !deadline.Before(permit.ExpiresAt.UTC()) {
		return OwnedJobDirValidation{}, ErrRecoveryResultCleanupValidationFailed
	}
	targetCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return service.target.ValidateOwnedJobDir(targetCtx, permit, request)
}

func validOwnedJobDirValidation(
	validation OwnedJobDirValidation,
	request ValidateOwnedJobDirRequest,
	rootRevision string,
) bool {
	return validation.Object == request.Object && validation.MarkerBindingDigest == request.MarkerBindingDigest &&
		validation.RootRevision == rootRevision && validOpaqueRevision(validation.TargetRevision)
}

func sameRecoveryCleanupTargetBinding(left, right recoveryCleanupTargetBinding) bool {
	return left.NodeID == right.NodeID && left.Object == right.Object &&
		left.MarkerBindingDigest == right.MarkerBindingDigest &&
		left.MarkerCreatorID == right.MarkerCreatorID && left.MarkerCreatorFence == right.MarkerCreatorFence &&
		left.RootRevision == right.RootRevision && left.SessionBinding == right.SessionBinding
}

func publishedCleanupPermitMatchesClaimAt(
	permit TargetCleanupPermit,
	claim RecoveryResultCleanupClaim,
	binding recoveryCleanupTargetBinding,
	request ValidateOwnedJobDirRequest,
	now time.Time,
) bool {
	return permit.ValidateOwnedJobDirRequestAt(now, request) == nil &&
		permit.ResourceKind == CleanupResourceResultSet && permit.ResourceID == claim.ResultSetID &&
		permit.JobID == claim.JobID && permit.CleanupOwner == claim.WorkerID &&
		permit.CleanupFence == claim.CleanupFence && permit.CleanupAttempt == claim.CleanupAttempt &&
		permit.NodeID == binding.NodeID && permit.NodeLeaseID == claim.NodeLeaseID &&
		permit.NodeFence == claim.NodeFence && permit.RootID == binding.Object.RootID &&
		permit.RootLocatorDigest == binding.Object.RootLocatorDigest &&
		permit.TargetPathDigest == binding.Object.TargetPathDigest &&
		permit.RootRevision == binding.RootRevision &&
		permit.MarkerBindingDigest == binding.MarkerBindingDigest &&
		permit.MarkerCreatorID == binding.MarkerCreatorID && permit.MarkerCreatorFence == binding.MarkerCreatorFence &&
		permit.proof != nil && permit.proof.sessionBinding == binding.SessionBinding &&
		permit.UseLatchID == RecoverySchemaUseLatchID && permit.ExpiresAt.Equal(claim.LeaseExpiresAt)
}

func workspaceCleanupPermitMatchesClaimAt(
	permit TargetCleanupPermit,
	claim RecoveryWorkspaceCleanupClaim,
	nodeID uint,
	binding recoveryCleanupTargetBinding,
	request ValidateOwnedJobDirRequest,
	now time.Time,
) bool {
	return permit.ValidateOwnedJobDirRequestAt(now, request) == nil &&
		permit.ResourceKind == CleanupResourceWorkspace && permit.ResourceID == claim.JobID &&
		permit.JobID == claim.JobID && permit.CleanupOwner == claim.WorkerID &&
		permit.CleanupFence == claim.CleanupFence && permit.CleanupAttempt == claim.CleanupAttempt &&
		permit.NodeID == nodeID && permit.NodeID == binding.NodeID && permit.NodeLeaseID == claim.NodeLeaseID &&
		permit.NodeFence == claim.NodeFence && permit.RootID == binding.Object.RootID &&
		permit.RootLocatorDigest == binding.Object.RootLocatorDigest &&
		permit.TargetPathDigest == binding.Object.TargetPathDigest &&
		permit.RootRevision == binding.RootRevision &&
		permit.MarkerBindingDigest == binding.MarkerBindingDigest &&
		permit.MarkerCreatorID == binding.MarkerCreatorID && permit.MarkerCreatorFence == binding.MarkerCreatorFence &&
		permit.proof != nil && permit.proof.sessionBinding == binding.SessionBinding &&
		permit.UseLatchID == RecoverySchemaUseLatchID && permit.ExpiresAt.Equal(claim.LeaseExpiresAt)
}

func (service *ResultLifecycleService) transitionRecoveryWorkspaceCleanup(
	ctx context.Context,
	claim RecoveryWorkspaceCleanupClaim,
	expectedPhase CleanupPhase,
	nextPhase CleanupPhase,
) (RecoveryWorkspaceCleanupClaim, error) {
	if service == nil || service.db == nil || service.now == nil || service.cleanupLeaseTTL <= 0 ||
		!validRecoveryWorkspaceCleanupClaim(claim) || claim.Phase != expectedPhase ||
		(nextPhase != expectedPhase && !expectedPhase.CanTransitionTo(nextPhase)) {
		return RecoveryWorkspaceCleanupClaim{}, ErrInvalidResultLifecycle
	}
	ctx = nonNilRecoveryContext(ctx)
	now := service.now().UTC()
	if now.IsZero() {
		return RecoveryWorkspaceCleanupClaim{}, ErrInvalidResultLifecycle
	}

	renewed := claim
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.BackupAssetRecoveryJob
		loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", claim.JobID).Limit(1).Find(&job)
		if loaded.Error != nil {
			return loaded.Error
		}
		if loaded.RowsAffected != 1 || !currentRecoveryWorkspaceCleanupClaim(job, claim, expectedPhase, now) {
			return ErrRecoveryResultCleanupConflict
		}

		leaseExpiresAt, err := service.renewRecoveryCleanupNodeLeaseTx(
			ctx, tx, claim.JobID, job.TargetNodeID, claim.WorkerID, claim.NodeLeaseID,
			claim.NodeFence, claim.LeaseExpiresAt, now,
		)
		if err != nil {
			return err
		}
		updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryJob{}).
			Where(`id = ? AND state = ? AND target_mode = ? AND target_node_id = ?
				AND workspace_phase = ? AND workspace_cleanup_phase = ?
				AND workspace_cleanup_owner = ? AND workspace_cleanup_lease_expires_at = ?
				AND workspace_cleanup_fence = ? AND workspace_cleanup_node_lease_id = ?
				AND workspace_cleanup_node_fence = ? AND workspace_cleanup_attempt = ?
				AND updated_at = ?`,
				job.ID, job.State, job.TargetMode, job.TargetNodeID,
				job.WorkspacePhase, expectedPhase, claim.WorkerID, claim.LeaseExpiresAt,
				claim.CleanupFence, claim.NodeLeaseID, claim.NodeFence, claim.CleanupAttempt,
				job.UpdatedAt).
			Updates(map[string]any{
				"workspace_cleanup_phase":            string(nextPhase),
				"workspace_cleanup_lease_expires_at": leaseExpiresAt,
				"updated_at":                         now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrRecoveryResultCleanupConflict
		}
		renewed.Phase = nextPhase
		renewed.LeaseExpiresAt = leaseExpiresAt
		return nil
	})
	if err != nil {
		return claim, err
	}
	return renewed, nil
}

func (service *ResultLifecycleService) transitionRecoveryResultCleanupTx(
	ctx context.Context,
	tx *gorm.DB,
	claim RecoveryResultCleanupClaim,
	expectedPhase CleanupPhase,
	nextPhase CleanupPhase,
	now time.Time,
	beforeTransition func(*gorm.DB) error,
) (RecoveryResultCleanupClaim, error) {
	if tx == nil || !validRecoveryResultCleanupClaim(claim) || claim.Phase != expectedPhase || now.IsZero() ||
		(nextPhase != expectedPhase && !expectedPhase.CanTransitionTo(nextPhase)) {
		return RecoveryResultCleanupClaim{}, ErrInvalidResultLifecycle
	}

	var job model.BackupAssetRecoveryJob
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", claim.JobID).Limit(1).Find(&job)
	if loaded.Error != nil {
		return RecoveryResultCleanupClaim{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !currentPublishedRecoveryCleanupJob(job) {
		return RecoveryResultCleanupClaim{}, ErrRecoveryResultCleanupConflict
	}

	leaseExpiresAt, err := service.renewRecoveryCleanupNodeLeaseTx(
		ctx, tx, claim.JobID, job.TargetNodeID, claim.WorkerID, claim.NodeLeaseID,
		claim.NodeFence, claim.LeaseExpiresAt, now,
	)
	if err != nil {
		return RecoveryResultCleanupClaim{}, err
	}

	var resultSet model.BackupAssetRecoveryResultSet
	loaded = tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND job_id = ?", claim.ResultSetID, claim.JobID).Limit(1).Find(&resultSet)
	if loaded.Error != nil {
		return RecoveryResultCleanupClaim{}, loaded.Error
	}
	if loaded.RowsAffected != 1 || !currentRecoveryResultCleanupClaim(resultSet, job, claim, expectedPhase, now) {
		return RecoveryResultCleanupClaim{}, ErrRecoveryResultCleanupConflict
	}
	if beforeTransition != nil {
		if err := beforeTransition(tx); err != nil {
			return RecoveryResultCleanupClaim{}, err
		}
	}

	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryResultSet{}).
		Where(`id = ? AND job_id = ? AND state = ? AND cleanup_phase = ?
			AND cleanup_owner = ? AND cleanup_lease_expires_at = ? AND cleanup_fence = ?
			AND node_lease_id = ? AND node_fence = ? AND cleanup_attempt = ? AND updated_at = ?`,
			resultSet.ID, resultSet.JobID, ResultSetStateRevoking, expectedPhase,
			claim.WorkerID, claim.LeaseExpiresAt, claim.CleanupFence,
			claim.NodeLeaseID, claim.NodeFence, claim.CleanupAttempt, resultSet.UpdatedAt).
		Updates(map[string]any{
			"cleanup_phase": string(nextPhase), "cleanup_lease_expires_at": leaseExpiresAt,
			"updated_at": now,
		})
	if updated.Error != nil {
		return RecoveryResultCleanupClaim{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return RecoveryResultCleanupClaim{}, ErrRecoveryResultCleanupConflict
	}
	claim.Phase = nextPhase
	claim.LeaseExpiresAt = leaseExpiresAt
	return claim, nil
}

func (service *ResultLifecycleService) renewRecoveryCleanupNodeLeaseTx(
	ctx context.Context,
	tx *gorm.DB,
	jobID string,
	nodeID uint,
	workerID string,
	nodeLeaseID string,
	nodeFence uint64,
	currentExpiry time.Time,
	now time.Time,
) (time.Time, error) {
	nodeLease, err := lockRecoveryCleanupNodeLeaseTx(
		ctx, tx, jobID, nodeID, workerID, nodeLeaseID, nodeFence, currentExpiry, now,
	)
	if err != nil {
		return time.Time{}, err
	}
	leaseExpiresAt := nextRecoveryCleanupLeaseExpiry(currentExpiry, now, service.cleanupLeaseTTL)
	if leaseExpiresAt.IsZero() {
		return time.Time{}, ErrRecoveryResultCleanupConflict
	}
	updated := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
		Where(`id = ? AND job_id = ? AND node_id = ? AND holder_kind = ? AND attempt_id IS NULL
			AND owner_id = ? AND fence = ? AND state = ? AND released_at IS NULL
			AND lease_expires_at = ? AND lease_expires_at > ? AND updated_at = ?`,
			nodeLease.ID, jobID, nodeID, "recovery_cleanup", workerID, nodeFence, "active",
			nodeLease.LeaseExpiresAt, now, nodeLease.UpdatedAt).
		Updates(map[string]any{"lease_expires_at": leaseExpiresAt, "updated_at": now})
	if updated.Error != nil {
		return time.Time{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return time.Time{}, ErrRecoveryResultCleanupConflict
	}
	return leaseExpiresAt, nil
}

func lockRecoveryCleanupNodeLeaseTx(
	ctx context.Context,
	tx *gorm.DB,
	jobID string,
	nodeID uint,
	workerID string,
	nodeLeaseID string,
	nodeFence uint64,
	currentExpiry time.Time,
	now time.Time,
) (model.BackupAssetRecoveryNodeLease, error) {
	if tx == nil || !validOpaqueID(jobID) || nodeID == 0 || !validRecoveryWorkerID(workerID) ||
		!validOpaqueID(nodeLeaseID) || nodeFence == 0 || currentExpiry.IsZero() || now.IsZero() {
		return model.BackupAssetRecoveryNodeLease{}, ErrRecoveryResultCleanupConflict
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	loaded := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where(`id = ? AND job_id = ? AND node_id = ? AND holder_kind = ? AND attempt_id IS NULL
			AND owner_id = ? AND fence = ? AND state = ? AND released_at IS NULL
			AND lease_expires_at = ? AND lease_expires_at > ?`,
			nodeLeaseID, jobID, nodeID, "recovery_cleanup", workerID, nodeFence, "active",
			currentExpiry, now).
		Limit(1).Find(&nodeLease)
	if loaded.Error != nil {
		return model.BackupAssetRecoveryNodeLease{}, loaded.Error
	}
	if loaded.RowsAffected != 1 {
		return model.BackupAssetRecoveryNodeLease{}, ErrRecoveryResultCleanupConflict
	}
	return nodeLease, nil
}

func nextRecoveryCleanupLeaseExpiry(current, now time.Time, ttl time.Duration) time.Time {
	if current.IsZero() || now.IsZero() || ttl <= 0 {
		return time.Time{}
	}
	next := now.Add(ttl).UTC()
	if !next.After(current.UTC()) {
		next = current.UTC().Add(time.Second)
	}
	if !next.After(current.UTC()) {
		return time.Time{}
	}
	return next
}

func validRecoveryResultCleanupClaim(claim RecoveryResultCleanupClaim) bool {
	return validOpaqueID(claim.ResultSetID) && validOpaqueID(claim.JobID) &&
		validRecoveryWorkerID(claim.WorkerID) && claim.CleanupFence > 0 && claim.CleanupAttempt > 0 &&
		validOpaqueID(claim.NodeLeaseID) && claim.NodeFence > 0 && !claim.LeaseExpiresAt.IsZero() &&
		claim.Phase.Valid() && claim.Phase != CleanupPhaseTombstoned
}

func validRecoveryWorkspaceCleanupClaim(claim RecoveryWorkspaceCleanupClaim) bool {
	return validOpaqueID(claim.JobID) && validRecoveryWorkerID(claim.WorkerID) &&
		claim.CleanupFence > 0 && claim.CleanupAttempt > 0 && validOpaqueID(claim.NodeLeaseID) &&
		claim.NodeFence > 0 && !claim.LeaseExpiresAt.IsZero() && claim.Phase.Valid() &&
		claim.Phase != CleanupPhaseTombstoned
}

func currentPublishedRecoveryCleanupJob(job model.BackupAssetRecoveryJob) bool {
	return validOpaqueID(job.ID) && job.TargetNodeID > 0 && TargetMode(job.TargetMode) == TargetModeIsolated &&
		(JobState(job.State) == JobStateSucceeded || JobState(job.State) == JobStateDegraded) &&
		WorkspacePhase(job.WorkspacePhase) == WorkspacePhasePublished &&
		validDigest(job.WorkspaceMarkerBindingDigest)
}

func currentRecoveryResultCleanupClaim(
	resultSet model.BackupAssetRecoveryResultSet,
	job model.BackupAssetRecoveryJob,
	claim RecoveryResultCleanupClaim,
	expectedPhase CleanupPhase,
	now time.Time,
) bool {
	return resultSet.ID == claim.ResultSetID && resultSet.JobID == claim.JobID &&
		ResultSetState(resultSet.State) == ResultSetStateRevoking &&
		resultSet.MarkerBindingDigest == job.WorkspaceMarkerBindingDigest &&
		CleanupPhase(resultSet.CleanupPhase) == expectedPhase &&
		resultSet.CleanupOwner == claim.WorkerID && resultSet.CleanupLeaseExpiresAt != nil &&
		resultSet.CleanupLeaseExpiresAt.Equal(claim.LeaseExpiresAt) &&
		resultSet.CleanupLeaseExpiresAt.UTC().After(now) && resultSet.CleanupFence == claim.CleanupFence &&
		resultSet.NodeLeaseID != nil && *resultSet.NodeLeaseID == claim.NodeLeaseID &&
		resultSet.NodeFence == claim.NodeFence && resultSet.CleanupAttempt == claim.CleanupAttempt
}

func currentRecoveryWorkspaceCleanupClaim(
	job model.BackupAssetRecoveryJob,
	claim RecoveryWorkspaceCleanupClaim,
	expectedPhase CleanupPhase,
	now time.Time,
) bool {
	return validOpaqueID(job.ID) && job.ID == claim.JobID && job.TargetNodeID > 0 &&
		TargetMode(job.TargetMode) == TargetModeIsolated &&
		WorkspacePhase(job.WorkspacePhase) == WorkspacePhaseCleanupDue &&
		terminalRecoveryWorkspaceCleanupJobState(JobState(job.State)) &&
		job.EncryptedWorkspaceRelativeLocator != "" && validDigest(job.WorkspaceBindingDigest) &&
		validDigest(job.WorkspaceMarkerBindingDigest) && validRecoveryWorkerID(job.WorkspaceOwner) &&
		job.WorkspaceFence > 0 && job.PlaintextDeadline != nil &&
		(emptyRecoveryMarkerValidation(job) || validRecoveryMarkerValidation(job)) &&
		CleanupPhase(job.WorkspaceCleanupPhase) == expectedPhase &&
		job.WorkspaceCleanupOwner == claim.WorkerID && job.WorkspaceCleanupLeaseExpiresAt != nil &&
		job.WorkspaceCleanupLeaseExpiresAt.Equal(claim.LeaseExpiresAt) &&
		job.WorkspaceCleanupLeaseExpiresAt.UTC().After(now) &&
		job.WorkspaceCleanupFence == claim.CleanupFence && job.WorkspaceCleanupNodeLeaseID != nil &&
		*job.WorkspaceCleanupNodeLeaseID == claim.NodeLeaseID &&
		job.WorkspaceCleanupNodeFence == claim.NodeFence &&
		job.WorkspaceCleanupAttempt == claim.CleanupAttempt
}

func nonNilRecoveryContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (service *ResultLifecycleService) createRecoveryCleanupNodeLeaseTx(
	ctx context.Context,
	tx *gorm.DB,
	jobID string,
	nodeID uint,
	workerID string,
	nodeLeaseID string,
	now time.Time,
) (model.BackupAssetRecoveryNodeLease, error) {
	if err := service.nodeAdmission.AdmitRecoveryTx(ctx, tx, nodeID); err != nil {
		if errors.Is(err, task.ErrNodeWriteConflict) {
			return model.BackupAssetRecoveryNodeLease{}, ErrRecoveryResultCleanupBusy
		}
		return model.BackupAssetRecoveryNodeLease{}, err
	}

	var maxFence int64
	if err := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("node_id = ?", nodeID).Select("COALESCE(MAX(fence), 0)").
		Scan(&maxFence).Error; err != nil {
		return model.BackupAssetRecoveryNodeLease{}, err
	}
	if maxFence < 0 || maxFence == math.MaxInt64 {
		return model.BackupAssetRecoveryNodeLease{}, ErrRecoveryResultCleanupConflict
	}
	leaseExpiresAt := now.Add(service.cleanupLeaseTTL).UTC()
	nodeLease := model.BackupAssetRecoveryNodeLease{
		ID: nodeLeaseID, NodeID: nodeID, HolderKind: "recovery_cleanup", JobID: jobID,
		OwnerID: workerID, Fence: uint64(maxFence + 1), State: "active", LeaseExpiresAt: leaseExpiresAt,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.WithContext(ctx).Create(&nodeLease).Error; err != nil {
		return model.BackupAssetRecoveryNodeLease{}, err
	}
	return nodeLease, nil
}

func validReadyRecoveryResultCleanupCandidate(resultSet model.BackupAssetRecoveryResultSet) bool {
	return validOpaqueID(resultSet.ID) && validOpaqueID(resultSet.JobID) &&
		ResultSetState(resultSet.State) == ResultSetStateReady &&
		CleanupPhase(resultSet.CleanupPhase) == CleanupPhaseClaimed &&
		resultSet.CleanupOwner == "" && resultSet.CleanupLeaseExpiresAt == nil &&
		resultSet.CleanupFence == 0 && resultSet.NodeLeaseID == nil && resultSet.NodeFence == 0 &&
		resultSet.CleanupAttempt == 0 && !resultSet.UpdatedAt.IsZero()
}

func validRecoveryResultCleanupCandidate(resultSet model.BackupAssetRecoveryResultSet, now time.Time) bool {
	if !validOpaqueID(resultSet.ID) || !validOpaqueID(resultSet.JobID) || now.IsZero() ||
		!CleanupPhase(resultSet.CleanupPhase).Valid() || resultSet.UpdatedAt.IsZero() {
		return false
	}
	switch ResultSetState(resultSet.State) {
	case ResultSetStateReady:
		return validReadyRecoveryResultCleanupCandidate(resultSet)
	case ResultSetStateCleanupFailed:
		return CleanupPhase(resultSet.CleanupPhase) != CleanupPhaseTombstoned &&
			resultSet.CleanupOwner == "" && resultSet.CleanupLeaseExpiresAt == nil &&
			resultSet.CleanupFence > 0 && resultSet.NodeLeaseID == nil && resultSet.NodeFence == 0 &&
			resultSet.CleanupAttempt > 0
	case ResultSetStateRevoking:
		return CleanupPhase(resultSet.CleanupPhase) != CleanupPhaseTombstoned &&
			validRecoveryWorkerID(resultSet.CleanupOwner) && resultSet.CleanupLeaseExpiresAt != nil &&
			!resultSet.CleanupLeaseExpiresAt.UTC().After(now) && resultSet.CleanupFence > 0 &&
			resultSet.NodeLeaseID != nil && validOpaqueID(*resultSet.NodeLeaseID) &&
			resultSet.NodeFence > 0 && resultSet.CleanupAttempt > 0
	default:
		return false
	}
}

func validRecoveryWorkspaceCleanupCandidate(job model.BackupAssetRecoveryJob, now time.Time) bool {
	if !validOpaqueID(job.ID) || now.IsZero() || job.TargetNodeID == 0 ||
		TargetMode(job.TargetMode) != TargetModeIsolated ||
		WorkspacePhase(job.WorkspacePhase) != WorkspacePhaseCleanupDue ||
		!terminalRecoveryWorkspaceCleanupJobState(JobState(job.State)) ||
		job.EncryptedWorkspaceRelativeLocator == "" || !validDigest(job.WorkspaceBindingDigest) ||
		!validDigest(job.WorkspaceMarkerBindingDigest) || !validRecoveryWorkerID(job.WorkspaceOwner) ||
		job.WorkspaceFence == 0 || job.PlaintextDeadline == nil ||
		(!emptyRecoveryMarkerValidation(job) && !validRecoveryMarkerValidation(job)) ||
		!CleanupPhase(job.WorkspaceCleanupPhase).Valid() || job.UpdatedAt.IsZero() {
		return false
	}

	switch {
	case job.WorkspaceCleanupPhase == string(CleanupPhaseClaimed) &&
		job.WorkspaceCleanupOwner == "" && job.WorkspaceCleanupLeaseExpiresAt == nil &&
		job.WorkspaceCleanupFence == 0 && job.WorkspaceCleanupNodeLeaseID == nil &&
		job.WorkspaceCleanupNodeFence == 0 && job.WorkspaceCleanupAttempt == 0:
		return true
	case CleanupPhase(job.WorkspaceCleanupPhase) != CleanupPhaseTombstoned &&
		job.WorkspaceCleanupOwner == "" && job.WorkspaceCleanupLeaseExpiresAt == nil &&
		job.WorkspaceCleanupFence > 0 && job.WorkspaceCleanupNodeLeaseID == nil &&
		job.WorkspaceCleanupNodeFence == 0 && job.WorkspaceCleanupAttempt > 0:
		return true
	case CleanupPhase(job.WorkspaceCleanupPhase) != CleanupPhaseTombstoned &&
		validRecoveryWorkerID(job.WorkspaceCleanupOwner) && job.WorkspaceCleanupLeaseExpiresAt != nil &&
		!job.WorkspaceCleanupLeaseExpiresAt.UTC().After(now) && job.WorkspaceCleanupFence > 0 &&
		job.WorkspaceCleanupNodeLeaseID != nil && validOpaqueID(*job.WorkspaceCleanupNodeLeaseID) &&
		job.WorkspaceCleanupNodeFence > 0 && job.WorkspaceCleanupAttempt > 0:
		return true
	default:
		return false
	}
}

func terminalRecoveryWorkspaceCleanupJobState(state JobState) bool {
	switch state {
	case JobStateSucceeded, JobStateDegraded, JobStateNeedsAttention, JobStateFailed, JobStateCanceled:
		return true
	default:
		return false
	}
}

func sameRecoveryResultCleanupSnapshot(
	left model.BackupAssetRecoveryResultSet,
	right model.BackupAssetRecoveryResultSet,
) bool {
	return left.ID == right.ID && left.JobID == right.JobID && left.State == right.State &&
		left.MarkerBindingDigest == right.MarkerBindingDigest &&
		left.PlaintextDeadline.Equal(right.PlaintextDeadline) && left.HardDeadline.Equal(right.HardDeadline) &&
		left.CleanupPhase == right.CleanupPhase && left.CleanupOwner == right.CleanupOwner &&
		sameContentTime(left.CleanupLeaseExpiresAt, right.CleanupLeaseExpiresAt) &&
		left.CleanupFence == right.CleanupFence && sameOptionalOpaqueID(left.NodeLeaseID, right.NodeLeaseID) &&
		left.NodeFence == right.NodeFence && left.CleanupAttempt == right.CleanupAttempt &&
		left.UpdatedAt.Equal(right.UpdatedAt)
}

func sameRecoveryWorkspaceCleanupSnapshot(
	left model.BackupAssetRecoveryJob,
	right model.BackupAssetRecoveryJob,
) bool {
	return left.ID == right.ID && left.State == right.State &&
		left.TransitionRevision == right.TransitionRevision && left.TargetMode == right.TargetMode &&
		left.TargetNodeID == right.TargetNodeID && left.WorkspacePhase == right.WorkspacePhase &&
		left.WorkspaceBindingDigest == right.WorkspaceBindingDigest &&
		left.WorkspaceMarkerBindingDigest == right.WorkspaceMarkerBindingDigest &&
		left.WorkspaceOwner == right.WorkspaceOwner && left.WorkspaceFence == right.WorkspaceFence &&
		left.WorkspaceMarkerValidationAttemptID == right.WorkspaceMarkerValidationAttemptID &&
		left.WorkspaceMarkerValidationAttemptFence == right.WorkspaceMarkerValidationAttemptFence &&
		left.WorkspaceMarkerValidationNodeFence == right.WorkspaceMarkerValidationNodeFence &&
		left.WorkspaceCleanupPhase == right.WorkspaceCleanupPhase &&
		left.WorkspaceCleanupOwner == right.WorkspaceCleanupOwner &&
		sameContentTime(left.WorkspaceCleanupLeaseExpiresAt, right.WorkspaceCleanupLeaseExpiresAt) &&
		left.WorkspaceCleanupFence == right.WorkspaceCleanupFence &&
		sameOptionalOpaqueID(left.WorkspaceCleanupNodeLeaseID, right.WorkspaceCleanupNodeLeaseID) &&
		left.WorkspaceCleanupNodeFence == right.WorkspaceCleanupNodeFence &&
		left.WorkspaceCleanupAttempt == right.WorkspaceCleanupAttempt &&
		sameContentTime(left.PlaintextDeadline, right.PlaintextDeadline) &&
		left.UpdatedAt.Equal(right.UpdatedAt)
}

func sameOptionalOpaqueID(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func releaseRecoveryResultCleanupNodeLeaseTx(
	ctx context.Context,
	tx *gorm.DB,
	lease model.BackupAssetRecoveryNodeLease,
	now time.Time,
) error {
	released := tx.WithContext(ctx).Model(&model.BackupAssetRecoveryNodeLease{}).
		Where("id = ? AND job_id = ? AND owner_id = ? AND fence = ? AND state = ?",
			lease.ID, lease.JobID, lease.OwnerID, lease.Fence, "active").
		Updates(map[string]any{"state": "released", "released_at": now, "updated_at": now})
	if released.Error != nil {
		return released.Error
	}
	if released.RowsAffected != 1 {
		return ErrRecoveryResultCleanupConflict
	}
	return nil
}

func recoveryResultLocatorDigest(jobID, resultSetID, resultID, relativeLocator string) string {
	return framedDigest("xirang/recovery/result-locator/v1", jobID, resultSetID, resultID, relativeLocator)
}
