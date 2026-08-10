package recovery

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var ErrRecoveryResultUnavailable = errors.New("recovery result unavailable")

type RecoveryResultResolverDependencies struct {
	DB  *gorm.DB
	Now func() time.Time
}

type RecoveryResultResolver struct {
	db  *gorm.DB
	now func() time.Time
}

type ResolveRecoveryResultRequest struct {
	RequesterID   uint
	RecoveryJobID string
	ResultID      string
}

type ResolvedRecoveryResult struct {
	RecoveryJobID       string
	ResultSetID         string
	ResultID            string
	OwnerUserID         uint
	RepositoryID        string
	RecoveryPointID     string
	Provider            backupasset.ProviderKind
	TargetNodeID        uint
	RootRevision        string
	PublicationRevision uint64
	CleanupFence        uint64
	ResultSetState      ResultSetState
	ResultKind          RecoveryResultKind
	MarkerBindingDigest string
	LocatorDigest       string
	Classification      RecoveryResultClassificationBinding
	Size                int64
	ContentDigest       string
	ModifiedAt          *time.Time
	PlaintextDeadline   time.Time
	HardDeadline        time.Time
	TargetObject        TargetObjectRef
	readAuthority       targetResultReadAuthority
}

func NewRecoveryResultResolver(
	dependencies RecoveryResultResolverDependencies,
) (*RecoveryResultResolver, error) {
	if dependencies.DB == nil || dependencies.Now == nil {
		return nil, ErrRecoveryResultUnavailable
	}
	return &RecoveryResultResolver{db: dependencies.DB, now: dependencies.Now}, nil
}

func (resolver *RecoveryResultResolver) Resolve(
	ctx context.Context,
	request ResolveRecoveryResultRequest,
) (ResolvedRecoveryResult, error) {
	if resolver == nil || resolver.db == nil || resolver.now == nil || request.RequesterID == 0 ||
		!validOpaqueID(request.RecoveryJobID) || !validOpaqueID(request.ResultID) {
		return ResolvedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, err := resolver.resolve(ctx, request)
	if err != nil {
		return ResolvedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	return resolved, nil
}

func (resolver *RecoveryResultResolver) Revalidate(
	ctx context.Context,
	expected ResolvedRecoveryResult,
) error {
	current, err := resolver.Resolve(ctx, ResolveRecoveryResultRequest{
		RequesterID: expected.OwnerUserID, RecoveryJobID: expected.RecoveryJobID, ResultID: expected.ResultID,
	})
	if err != nil || !sameResolvedRecoveryResult(current, expected) {
		return ErrRecoveryResultUnavailable
	}
	return nil
}

func (resolver *RecoveryResultResolver) resolve(
	ctx context.Context,
	request ResolveRecoveryResultRequest,
) (ResolvedRecoveryResult, error) {
	var result model.BackupAssetRecoveryResult
	loaded := resolver.db.WithContext(ctx).Where("id = ? AND job_id = ?", request.ResultID, request.RecoveryJobID).
		Limit(1).Find(&result)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		return ResolvedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	var resultSet model.BackupAssetRecoveryResultSet
	loaded = resolver.db.WithContext(ctx).Where("id = ? AND job_id = ?", result.ResultSetID, request.RecoveryJobID).
		Limit(1).Find(&resultSet)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		return ResolvedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	var job model.BackupAssetRecoveryJob
	loaded = resolver.db.WithContext(ctx).Where("id = ?", request.RecoveryJobID).Limit(1).Find(&job)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		return ResolvedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	var plan model.BackupAssetRecoveryPlan
	loaded = resolver.db.WithContext(ctx).Where("id = ?", job.PlanID).Limit(1).Find(&plan)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		return ResolvedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	var authority model.BackupAssetRecoveryGrant
	loaded = resolver.db.WithContext(ctx).Where("id = ? AND plan_id = ?", job.AuthorityGrantID, job.PlanID).
		Limit(1).Find(&authority)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		return ResolvedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	var repository model.BackupRepository
	loaded = resolver.db.WithContext(ctx).Where("id = ?", plan.RepositoryID).Limit(1).Find(&repository)
	if loaded.Error != nil || loaded.RowsAffected != 1 {
		return ResolvedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	var activeAttempts int64
	if err := resolver.db.WithContext(ctx).Model(&model.BackupAssetRecoveryAttempt{}).
		Where("job_id = ? AND state IN ?", job.ID, []string{string(AttemptStateClaimed), string(AttemptStateRunning)}).
		Count(&activeAttempts).Error; err != nil {
		return ResolvedRecoveryResult{}, err
	}

	now := resolver.now().UTC()
	classification := RecoveryResultClassificationBinding{
		Kind:     RecoveryResultClassification(result.Classification),
		Revision: int64(result.ClassificationRevision), SourceRevision: result.ClassificationSourceRevision,
	}
	kind := RecoveryResultKind(result.ResultKind)
	workspaceLocator := recoveryWorkspaceLocatorDirectory + "/" + job.ID
	fullLocator := job.EncryptedWorkspaceRelativeLocator + "/" + result.EncryptedRelativeLocator
	targetPathDigest, digestErr := TargetPathDigest(job.TargetRootID, job.RootLocatorDigest, fullLocator)
	approvedSecurity := SecurityDecisionKind(job.SecurityDecision) == SecurityDecisionAllowClean ||
		SecurityDecisionKind(job.SecurityDecision) == SecurityDecisionAdminOverride
	provider := backupasset.ProviderKind(repository.ProviderKind)
	sessionBinding, sessionBindingErr := newRecoveryTargetSessionBinding(plan)
	if now.IsZero() || plan.RequesterID != request.RequesterID || plan.ID != job.PlanID ||
		!validOpaqueID(plan.RepositoryID) || !validOpaqueID(plan.RecoveryPointID) ||
		repository.ID != plan.RepositoryID || !validRecoveryProvider(provider) ||
		!validOpaqueRevision(plan.RootRevision) ||
		TargetMode(job.TargetMode) != TargetModeIsolated ||
		(JobState(job.State) != JobStateSucceeded && JobState(job.State) != JobStateDegraded) ||
		WorkspacePhase(job.WorkspacePhase) != WorkspacePhasePublished || activeAttempts != 0 ||
		job.EncryptedWorkspaceRelativeLocator != workspaceLocator ||
		job.WorkspaceBindingDigest != recoveryWorkspaceBindingDigest(plan, job.ID, workspaceLocator) ||
		!validRecoveryMarkerValidation(job) || !validDigest(job.WorkspaceMarkerBindingDigest) ||
		!validRecoveryWorkerID(job.WorkspaceOwner) || job.WorkspaceFence == 0 || sessionBindingErr != nil ||
		ResultSetState(resultSet.State) != ResultSetStateReady || resultSet.CleanupFence != 0 ||
		resultSet.CleanupOwner != "" || resultSet.NodeLeaseID != nil ||
		resultSet.MarkerBindingDigest != job.WorkspaceMarkerBindingDigest ||
		job.PlaintextDeadline == nil || !validRecoveryResultDeadlineWindow(
		job.PlaintextDeadline.UTC(), resultSet.PlaintextDeadline.UTC(), resultSet.HardDeadline.UTC(), now,
	) ||
		!kind.valid() || !classification.valid() || result.Size < 0 || !validDigest(result.ContentDigest) ||
		!validTargetRelativeLocator(result.EncryptedRelativeLocator) ||
		result.LocatorDigest != recoveryResultLocatorDigest(job.ID, resultSet.ID, result.ID, result.EncryptedRelativeLocator) ||
		digestErr != nil || !approvedSecurity || AuthorityCategory(job.AuthorityCategory) != AuthorityWrite ||
		AuthorityCategory(authority.AuthorityCategory) != AuthorityWrite ||
		authority.BindingDigest != job.AuthorityBindingDigest ||
		!authority.ExpiresAt.Equal(job.AuthorityExpiresAt) || authority.ConsumedAt == nil ||
		!authority.ConsumedAt.Equal(job.AuthorityConsumedAt) || authority.RevokedAt != nil {
		return ResolvedRecoveryResult{}, ErrRecoveryResultUnavailable
	}

	targetObject := TargetObjectRef{
		RootID: job.TargetRootID, RootLocatorDigest: job.RootLocatorDigest,
		TargetPathDigest: targetPathDigest, PrivateRelativeLocator: fullLocator,
	}
	readAuthority := targetResultReadAuthority{
		sessionBinding: sessionBinding, jobID: job.ID, resultSetID: resultSet.ID, resultID: result.ID,
		publicationRevision: job.TransitionRevision, cleanupFence: resultSet.CleanupFence,
		resultSetState: ResultSetStateReady, markerBindingDigest: resultSet.MarkerBindingDigest,
		markerCreatorID: job.WorkspaceOwner, markerCreatorFence: job.WorkspaceFence,
		locatorDigest: result.LocatorDigest, object: targetObject, expectedBytes: result.Size,
		expectedContentDigest: result.ContentDigest, plaintextDeadline: resultSet.PlaintextDeadline.UTC(),
	}
	if !readAuthority.valid() {
		return ResolvedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	return ResolvedRecoveryResult{
		RecoveryJobID: job.ID, ResultSetID: resultSet.ID, ResultID: result.ID,
		OwnerUserID: plan.RequesterID, RepositoryID: plan.RepositoryID,
		RecoveryPointID: plan.RecoveryPointID, Provider: provider,
		TargetNodeID: job.TargetNodeID, RootRevision: plan.RootRevision,
		PublicationRevision: job.TransitionRevision, CleanupFence: resultSet.CleanupFence,
		ResultSetState: ResultSetStateReady, ResultKind: kind,
		MarkerBindingDigest: resultSet.MarkerBindingDigest, LocatorDigest: result.LocatorDigest,
		Classification: classification, Size: result.Size, ContentDigest: result.ContentDigest,
		ModifiedAt: result.ModifiedAt, PlaintextDeadline: resultSet.PlaintextDeadline.UTC(),
		HardDeadline: resultSet.HardDeadline.UTC(), TargetObject: targetObject,
		readAuthority: readAuthority,
	}, nil
}

func sameResolvedRecoveryResult(left, right ResolvedRecoveryResult) bool {
	return left.RecoveryJobID == right.RecoveryJobID && left.ResultSetID == right.ResultSetID &&
		left.ResultID == right.ResultID && left.OwnerUserID == right.OwnerUserID &&
		left.RepositoryID == right.RepositoryID && left.RecoveryPointID == right.RecoveryPointID &&
		left.Provider == right.Provider && left.TargetNodeID == right.TargetNodeID &&
		left.RootRevision == right.RootRevision && left.PublicationRevision == right.PublicationRevision &&
		left.CleanupFence == right.CleanupFence && left.ResultSetState == right.ResultSetState &&
		left.ResultKind == right.ResultKind && left.MarkerBindingDigest == right.MarkerBindingDigest &&
		left.LocatorDigest == right.LocatorDigest && left.Classification == right.Classification &&
		left.Size == right.Size && left.ContentDigest == right.ContentDigest &&
		sameContentTime(left.ModifiedAt, right.ModifiedAt) &&
		left.PlaintextDeadline.Equal(right.PlaintextDeadline) && left.HardDeadline.Equal(right.HardDeadline) &&
		left.TargetObject == right.TargetObject && left.readAuthority == right.readAuthority
}

func sameContentTime(left, right *time.Time) bool {
	if (left == nil) != (right == nil) {
		return false
	}
	return left == nil || left.Equal(*right)
}

type RecoveryResultDeliveryAdapterDependencies struct {
	Resolver      *RecoveryResultResolver
	Target        TargetPort
	Now           func() time.Time
	ReadPermitTTL time.Duration
}

type RecoveryResultDeliveryAdapter struct {
	resolver      *RecoveryResultResolver
	target        TargetPort
	now           func() time.Time
	readPermitTTL time.Duration
}

func NewRecoveryResultDeliveryAdapter(
	dependencies RecoveryResultDeliveryAdapterDependencies,
) (*RecoveryResultDeliveryAdapter, error) {
	if dependencies.Resolver == nil || dependencies.Target == nil || dependencies.Now == nil ||
		dependencies.ReadPermitTTL <= 0 {
		return nil, ErrRecoveryResultUnavailable
	}
	return &RecoveryResultDeliveryAdapter{
		resolver: dependencies.Resolver, target: dependencies.Target,
		now: dependencies.Now, readPermitTTL: dependencies.ReadPermitTTL,
	}, nil
}

func (adapter *RecoveryResultDeliveryAdapter) AuthorizeRecoveryResult(
	ctx context.Context,
	actor content.DeliveryActor,
	ref content.RecoveryResultRef,
	action content.DeliveryAction,
) (content.AuthorizedRecoveryResult, error) {
	if !validRecoveryResultDeliveryActor(actor, action) || adapter == nil || adapter.resolver == nil {
		return content.AuthorizedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	resolved, err := adapter.resolver.Resolve(ctx, ResolveRecoveryResultRequest{
		RequesterID: actor.UserID, RecoveryJobID: ref.RecoveryJobID, ResultID: ref.ResultID,
	})
	if err != nil {
		return content.AuthorizedRecoveryResult{}, ErrRecoveryResultUnavailable
	}
	return authorizedRecoveryResult(resolved), nil
}

func (adapter *RecoveryResultDeliveryAdapter) ReauthorizeRecoveryResult(
	ctx context.Context,
	actor content.DeliveryActor,
	expected content.AuthorizedRecoveryResult,
	action content.DeliveryAction,
) error {
	if !validRecoveryResultDeliveryActor(actor, action) || actor.UserID != expected.OwnerUserID ||
		adapter == nil || adapter.resolver == nil {
		return ErrRecoveryResultUnavailable
	}
	resolved, err := adapter.resolver.Resolve(ctx, ResolveRecoveryResultRequest{
		RequesterID: actor.UserID, RecoveryJobID: expected.Ref.RecoveryJobID, ResultID: expected.Ref.ResultID,
	})
	if err != nil || !sameAuthorizedRecoveryResult(authorizedRecoveryResult(resolved), expected) {
		return ErrRecoveryResultUnavailable
	}
	return nil
}

func (adapter *RecoveryResultDeliveryAdapter) OpenRecoveryResultSource(
	ctx context.Context,
	request content.RecoveryResultSourceRequest,
) (content.SourceSession, error) {
	if adapter == nil || adapter.resolver == nil || adapter.target == nil || adapter.now == nil ||
		content.ValidateRecoveryResultSourceRequest(request) != nil || request.Mode == content.SourceModeRange {
		return nil, ErrRecoveryResultUnavailable
	}
	resolved, err := adapter.resolver.Resolve(ctx, ResolveRecoveryResultRequest{
		RequesterID: request.OwnerUserID, RecoveryJobID: request.Ref.RecoveryJobID, ResultID: request.Ref.ResultID,
	})
	if err != nil || request.ExpectedPublication != recoveryResultPublicationFingerprint(resolved) ||
		request.ExpectedContent != resolved.ContentDigest ||
		request.Mode == content.SourceModeSequential && request.MaxBytes != resolved.Size {
		return nil, ErrRecoveryResultUnavailable
	}

	session := &recoveryResultSourceSession{
		resolver: adapter.resolver, resolved: resolved,
		stat: content.SourceStat{
			Size: resolved.Size, ModifiedAt: cloneContentTime(resolved.ModifiedAt), MediaType: "application/octet-stream",
			SourceFingerprint: request.ExpectedPublication, EntryFingerprint: resolved.ContentDigest,
			FingerprintStrong: true,
		},
		capabilities: content.SourceCapabilities{Provider: resolved.Provider, Sequential: true, Range: false},
	}
	if request.Mode == content.SourceModeStat {
		return session, nil
	}

	now := adapter.now().UTC()
	expiresAt := now.Add(adapter.readPermitTTL)
	if resolved.PlaintextDeadline.Before(expiresAt) {
		expiresAt = resolved.PlaintextDeadline
	}
	openRequest := OpenOwnedResultRequest{
		Object: resolved.TargetObject, ExpectedBytes: resolved.Size, IdentityDigest: resolved.ContentDigest,
	}
	observationPermit := issueTargetResultReadPermit(TargetObservationPermit{
		SchemaVersion: 1, NodeID: resolved.TargetNodeID, Purpose: TargetPurposeResultRead,
		RootID: resolved.TargetObject.RootID, RootLocatorDigest: resolved.TargetObject.RootLocatorDigest,
		TargetPathDigest: resolved.TargetObject.TargetPathDigest, RootRevision: resolved.RootRevision,
		ExpiresAt: expiresAt,
	}, resolved.readAuthority, openRequest)
	permit, err := NewTargetResultReadPermit(observationPermit, now)
	if err != nil {
		return nil, ErrRecoveryResultUnavailable
	}
	reader, err := adapter.target.OpenOwnedResult(ctx, permit, openRequest)
	if err != nil {
		if reader != nil {
			if closeErr := reader.Close(); closeErr != nil {
				return nil, ErrRecoveryResultUnavailable
			}
		}
		return nil, ErrRecoveryResultUnavailable
	}
	if reader == nil {
		return nil, ErrRecoveryResultUnavailable
	}
	if adapter.resolver.Revalidate(ctx, resolved) != nil {
		if closeErr := reader.Close(); closeErr != nil {
			return nil, ErrRecoveryResultUnavailable
		}
		return nil, ErrRecoveryResultUnavailable
	}
	session.reader = &recoveryResultSourceReader{inner: reader}
	return session, nil
}

func validRecoveryResultDeliveryActor(actor content.DeliveryActor, action content.DeliveryAction) bool {
	return actor.UserID > 0 && actor.Role == "admin" && action == content.DeliveryDownload
}

func authorizedRecoveryResult(resolved ResolvedRecoveryResult) content.AuthorizedRecoveryResult {
	return content.AuthorizedRecoveryResult{
		Ref:         content.RecoveryResultRef{RecoveryJobID: resolved.RecoveryJobID, ResultID: resolved.ResultID},
		OwnerUserID: resolved.OwnerUserID, RepositoryID: resolved.RepositoryID,
		RecoveryPointID: resolved.RecoveryPointID, Provider: resolved.Provider,
		PublicationRevision: resolved.PublicationRevision, CleanupFence: resolved.CleanupFence,
		MarkerBindingDigest:    resolved.MarkerBindingDigest,
		PublicationFingerprint: recoveryResultPublicationFingerprint(resolved),
		ContentDigest:          resolved.ContentDigest, Size: resolved.Size, ModifiedAt: cloneContentTime(resolved.ModifiedAt),
		MediaType: "application/octet-stream", RangeProven: false,
		Classification:               content.Classification(resolved.Classification.Kind),
		ClassificationRevision:       resolved.Classification.Revision,
		ClassificationSourceRevision: resolved.Classification.SourceRevision,
		PlaintextDeadline:            resolved.PlaintextDeadline, HardDeadline: resolved.HardDeadline,
	}
}

func sameAuthorizedRecoveryResult(left, right content.AuthorizedRecoveryResult) bool {
	return left.Ref == right.Ref && left.OwnerUserID == right.OwnerUserID &&
		left.RepositoryID == right.RepositoryID && left.RecoveryPointID == right.RecoveryPointID &&
		left.Provider == right.Provider && left.PublicationRevision == right.PublicationRevision &&
		left.CleanupFence == right.CleanupFence && left.MarkerBindingDigest == right.MarkerBindingDigest &&
		left.PublicationFingerprint == right.PublicationFingerprint && left.ContentDigest == right.ContentDigest &&
		left.Size == right.Size && sameContentTime(left.ModifiedAt, right.ModifiedAt) &&
		left.MediaType == right.MediaType && left.RangeProven == right.RangeProven &&
		left.Classification == right.Classification &&
		left.ClassificationRevision == right.ClassificationRevision &&
		left.ClassificationSourceRevision == right.ClassificationSourceRevision &&
		left.PlaintextDeadline.Equal(right.PlaintextDeadline) && left.HardDeadline.Equal(right.HardDeadline)
}

func recoveryResultPublicationFingerprint(resolved ResolvedRecoveryResult) string {
	modifiedAt := ""
	if resolved.ModifiedAt != nil {
		modifiedAt = resolved.ModifiedAt.UTC().Format(time.RFC3339Nano)
	}
	return framedDigest("xirang/recovery/result-publication/v1",
		resolved.RecoveryJobID, resolved.ResultSetID, resolved.ResultID,
		strconv.FormatUint(uint64(resolved.OwnerUserID), 10), resolved.RepositoryID, resolved.RecoveryPointID,
		string(resolved.Provider), strconv.FormatUint(uint64(resolved.TargetNodeID), 10), resolved.RootRevision,
		strconv.FormatUint(resolved.PublicationRevision, 10), strconv.FormatUint(resolved.CleanupFence, 10),
		resolved.MarkerBindingDigest, resolved.LocatorDigest, string(resolved.ResultKind),
		string(resolved.Classification.Kind), strconv.FormatInt(resolved.Classification.Revision, 10),
		strconv.FormatInt(resolved.Classification.SourceRevision, 10), strconv.FormatInt(resolved.Size, 10),
		resolved.ContentDigest, modifiedAt, resolved.PlaintextDeadline.UTC().Format(time.RFC3339Nano),
		resolved.HardDeadline.UTC().Format(time.RFC3339Nano), resolved.TargetObject.RootID,
		resolved.TargetObject.RootLocatorDigest, resolved.TargetObject.TargetPathDigest,
		resolved.readAuthority.sessionBinding.bindingDigest,
		resolved.readAuthority.markerCreatorID,
		strconv.FormatUint(resolved.readAuthority.markerCreatorFence, 10),
	)
}

type recoveryResultSourceSession struct {
	resolver     *RecoveryResultResolver
	resolved     ResolvedRecoveryResult
	stat         content.SourceStat
	capabilities content.SourceCapabilities
	reader       *recoveryResultSourceReader
}

func (session *recoveryResultSourceSession) Stat() content.SourceStat {
	result := session.stat
	result.ModifiedAt = cloneContentTime(result.ModifiedAt)
	return result
}

func (session *recoveryResultSourceSession) Capabilities() content.SourceCapabilities {
	return session.capabilities
}

func (session *recoveryResultSourceSession) Reader() content.SourceReader {
	if session == nil || session.reader == nil {
		return nil
	}
	return session.reader
}

func (session *recoveryResultSourceSession) Revalidate(ctx context.Context) error {
	if session == nil || session.resolver == nil || session.resolver.Revalidate(ctx, session.resolved) != nil {
		return ErrRecoveryResultUnavailable
	}
	return nil
}

func (session *recoveryResultSourceSession) Close() error {
	if session == nil || session.reader == nil {
		return nil
	}
	return session.reader.Close()
}

type recoveryResultSourceReader struct {
	inner     io.ReadCloser
	readBytes atomic.Int64
	closeOnce sync.Once
	closeErr  error
}

func (reader *recoveryResultSourceReader) Read(payload []byte) (int, error) {
	count, err := reader.inner.Read(payload)
	reader.readBytes.Add(int64(count))
	return count, err
}

func (reader *recoveryResultSourceReader) Close() error {
	reader.closeOnce.Do(func() { reader.closeErr = reader.inner.Close() })
	return reader.closeErr
}

func (reader *recoveryResultSourceReader) ProviderBytes() int64 {
	return reader.readBytes.Load()
}

func cloneContentTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}
