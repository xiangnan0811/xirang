package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type managedRecoveryEligibilityAuthorities struct {
	preflight      recovery.RecoveryPreflightExternalEvidenceAuthority
	live           recovery.RecoveryAuthorityRevalidator
	reconciliation recovery.RecoveryReconciliationRevisionSource
}

func newManagedRecoveryEligibilityAuthorities(
	dependencies recovery.RecoveryEligibilityAuthorityDependencies,
) (managedRecoveryEligibilityAuthorities, error) {
	authority, err := recovery.NewRecoveryEligibilityAuthority(dependencies)
	if err != nil {
		return managedRecoveryEligibilityAuthorities{}, err
	}
	return managedRecoveryEligibilityAuthorities{
		preflight: authority, live: authority, reconciliation: authority,
	}, nil
}

// managedRecoveryEligibilityRepositoryObserver is the external-I/O half of
// Repository's current managed-Rsync authority. A successful call transfers
// ownership of the returned capability to Recovery.
type managedRecoveryEligibilityRepositoryObserver interface {
	ObserveRecoverySource(
		context.Context,
		provider.RecoverySourceAuthorityRequest,
	) (provider.RsyncRestoreSource, repository.RecoveryRsyncSourceAuthorityObservation, error)
}

// managedRecoveryEligibilitySourceDurable is deliberately a caller-owned-tx
// seam. Repository implements it by reusing its private exact durable snapshot
// loader; it must never open a transaction or perform provider/network I/O.
type managedRecoveryEligibilitySourceDurable interface {
	RevalidateRecoverySourceAuthorityTx(
		context.Context,
		*gorm.DB,
		provider.RecoverySourceAuthorityRequest,
		repository.RecoveryRsyncSourceAuthorityObservation,
	) error
}

type managedRecoveryEligibilitySourceAdapter struct {
	observer managedRecoveryEligibilityRepositoryObserver
	durable  managedRecoveryEligibilitySourceDurable
}

func newManagedRecoveryEligibilitySourceAdapter(
	observer managedRecoveryEligibilityRepositoryObserver,
	durable managedRecoveryEligibilitySourceDurable,
) (*managedRecoveryEligibilitySourceAdapter, error) {
	if observer == nil || durable == nil {
		return nil, fmt.Errorf("%w: Recovery source authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	return &managedRecoveryEligibilitySourceAdapter{observer: observer, durable: durable}, nil
}

func (adapter *managedRecoveryEligibilitySourceAdapter) ObserveRecoveryEligibilitySource(
	ctx context.Context,
	request provider.RecoverySourceAuthorityRequest,
) (provider.RsyncRestoreSource, recovery.RecoveryEligibilitySourceObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, recovery.RecoveryEligibilitySourceObservation{}, err
	}
	if adapter == nil || adapter.observer == nil || request.Provider != backupasset.ProviderRsync {
		return nil, recovery.RecoveryEligibilitySourceObservation{}, managedRecoveryEligibilityUnavailable()
	}
	source, observed, err := adapter.observer.ObserveRecoverySource(ctx, request)
	if err != nil || source == nil || !validManagedRecoveryEligibilityRepositoryObservation(request, observed) {
		if source != nil {
			_ = source.Close()
		}
		return nil, recovery.RecoveryEligibilitySourceObservation{}, managedRecoveryEligibilityDependencyError(ctx, err)
	}
	return source, recovery.RecoveryEligibilitySourceObservation{
		RepositoryCapabilityRevision: observed.RepositoryCapabilityRevision,
		CapabilityRevision:           observed.CapabilityRevision,
		SourceAccessIdentity:         observed.SourceAccessIdentity,
		SourceFingerprint:            observed.SourceFingerprint,
		ManagedRootIdentity:          observed.ManagedRootIdentity,
		RepositoryBindingRevision:    observed.RepositoryBindingRevision,
		ProvenanceRevision:           observed.ProvenanceRevision,
	}, nil
}

func (adapter *managedRecoveryEligibilitySourceAdapter) RevalidateRecoveryEligibilitySourceTx(
	ctx context.Context,
	tx *gorm.DB,
	binding recovery.RecoveryAuthorityBinding,
	observation recovery.RecoveryEligibilitySourceObservation,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	request := provider.RecoverySourceAuthorityRequest{Provider: binding.Provider, RsyncRef: binding.SourceRef}
	expected := repository.RecoveryRsyncSourceAuthorityObservation{
		Provider: binding.Provider, RepositoryID: binding.RepositoryID,
		RecoveryPointID: binding.RecoveryPointID, CatalogGenerationID: binding.CatalogGenerationID,
		SourceRevisionDigest: binding.SourceRevisionDigest, ManifestDigest: binding.ManifestDigest,
		RepositoryCapabilityRevision: observation.RepositoryCapabilityRevision,
		CapabilityRevision:           observation.CapabilityRevision,
		SourceAccessIdentity:         observation.SourceAccessIdentity,
		SourceFingerprint:            observation.SourceFingerprint,
		ManagedRootIdentity:          observation.ManagedRootIdentity,
		RepositoryBindingRevision:    observation.RepositoryBindingRevision,
		ProvenanceRevision:           observation.ProvenanceRevision,
	}
	if adapter == nil || adapter.durable == nil || tx == nil || binding.Provider != backupasset.ProviderRsync ||
		!validManagedRecoveryEligibilityRepositoryObservation(request, expected) {
		return managedRecoveryEligibilityUnavailable()
	}
	if err := adapter.durable.RevalidateRecoverySourceAuthorityTx(ctx, tx, request, expected); err != nil {
		return managedRecoveryEligibilityDependencyError(ctx, err)
	}
	return nil
}

func validManagedRecoveryEligibilityRepositoryObservation(
	request provider.RecoverySourceAuthorityRequest,
	observation repository.RecoveryRsyncSourceAuthorityObservation,
) bool {
	ref := request.RsyncRef
	return request.Provider == backupasset.ProviderRsync && observation.Provider == request.Provider &&
		observation.RepositoryID == ref.RepositoryID && observation.RecoveryPointID == ref.RecoveryPointID &&
		observation.CatalogGenerationID == ref.CatalogGenerationID &&
		observation.SourceRevisionDigest == ref.SourceRevisionDigest && observation.ManifestDigest == ref.ManifestDigest &&
		observation.RepositoryCapabilityRevision > 0 && observation.CapabilityRevision > 0 &&
		observation.SourceAccessIdentity != "" && observation.SourceFingerprint != "" &&
		observation.ManagedRootIdentity != "" && observation.RepositoryBindingRevision != "" &&
		observation.ProvenanceRevision != ""
}

func managedRecoveryEligibilityUnavailable() error {
	return fmt.Errorf("%w: Recovery eligibility authority unavailable", backupasset.ErrCapabilityUnavailable)
}

func managedRecoveryEligibilityDependencyError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, backupasset.ErrConflict) {
		return fmt.Errorf("%w: Recovery eligibility authority changed", backupasset.ErrConflict)
	}
	return managedRecoveryEligibilityUnavailable()
}

type managedRecoveryEligibilitySecurityAdapter struct {
	runtime *managedProcessingRuntime
}

func newManagedRecoveryEligibilitySecurityAdapter(
	runtime *managedProcessingRuntime,
) (*managedRecoveryEligibilitySecurityAdapter, error) {
	// The Processing lifecycle installs the derived-artifact reader during its
	// startup, before Recovery startup. Keep the stable runtime owner here and
	// validate that late-bound reader at observation time.
	if runtime == nil || runtime.db == nil || runtime.authorize == nil || runtime.now == nil {
		return nil, fmt.Errorf("%w: Recovery security authority unavailable", backupasset.ErrCapabilityUnavailable)
	}
	return &managedRecoveryEligibilitySecurityAdapter{runtime: runtime}, nil
}

type managedRecoveryEligibilitySecurityPlanSnapshot struct {
	plan  model.BackupAssetRecoveryPlan
	items []model.BackupAssetRecoveryPlanItem
	actor content.DeliveryActor
}

type managedRecoveryEligibilitySecurityEvidenceRow struct {
	PlanItemID                 string
	Ordinal                    int
	RecoveryPointID            string
	CatalogGenerationID        string
	EntryID                    string
	PlanSourceFingerprint      string
	RelativePathDigest         string
	JobID                      string
	JobTransitionRevision      int64
	SourceFingerprint          string
	EntryFingerprint           string
	ProviderCapabilityRevision int64
	PipelineFingerprint        string
	ArtifactSetID              string
	ArtifactSetManifestDigest  string
	ArtifactID                 string
	ArtifactPlaintextSize      int64
	ArtifactPlaintextDigest    string
}

func (adapter *managedRecoveryEligibilitySecurityAdapter) ObserveRecoveryEligibilitySecurity(
	ctx context.Context,
	binding recovery.RecoveryAuthorityBinding,
) (recovery.RecoveryEligibilitySecurityObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return recovery.RecoveryEligibilitySecurityObservation{}, err
	}
	if adapter == nil || adapter.runtime == nil || adapter.runtime.db == nil ||
		adapter.runtime.authorize == nil || adapter.runtime.malwareEvidence == nil || adapter.runtime.now == nil {
		return recovery.RecoveryEligibilitySecurityObservation{}, managedRecoveryEligibilityUnavailable()
	}
	var snapshot managedRecoveryEligibilitySecurityPlanSnapshot
	err := adapter.runtime.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var captureErr error
		snapshot, captureErr = loadManagedRecoveryEligibilitySecurityPlanTx(ctx, tx, binding, false, true)
		return captureErr
	})
	if err != nil {
		return recovery.RecoveryEligibilitySecurityObservation{}, managedRecoveryEligibilityDependencyError(ctx, err)
	}

	states := make([]capabilityspec.ScanState, len(snapshot.items))
	assets := make([]content.AuthorizedAsset, len(snapshot.items))
	for index, item := range snapshot.items {
		asset, authorizeErr := adapter.runtime.authorize.Authorize(
			ctx, snapshot.actor,
			backupasset.AssetRef{RecoveryPointID: item.RecoveryPointID, EntryID: item.EntryID},
			content.DeliveryPreview,
		)
		if authorizeErr != nil || !validManagedRecoveryEligibilitySecurityAsset(binding, item, asset) {
			return recovery.RecoveryEligibilitySecurityObservation{},
				managedRecoveryEligibilityDependencyError(ctx, authorizeErr)
		}
		observed, observeErr := adapter.runtime.recoverySecurityObservation(ctx, asset)
		if observeErr != nil || !observed.Complete || observed.PolicyRevision != binding.SecurityPolicyRevision ||
			(observed.ScanState != capabilityspec.ScanNoFinding && observed.ScanState != capabilityspec.ScanFinding) {
			return recovery.RecoveryEligibilitySecurityObservation{},
				managedRecoveryEligibilityDependencyError(ctx, observeErr)
		}
		assets[index] = asset
		states[index] = observed.ScanState
	}

	var rows []managedRecoveryEligibilitySecurityEvidenceRow
	var bundleFingerprint string
	err = adapter.runtime.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var captureErr error
		rows, bundleFingerprint, captureErr = adapter.loadManagedRecoveryEligibilitySecurityEvidenceTx(
			ctx, tx, snapshot.items, false,
		)
		return captureErr
	})
	if err != nil || len(rows) != len(assets) {
		return recovery.RecoveryEligibilitySecurityObservation{}, managedRecoveryEligibilityDependencyError(ctx, err)
	}
	for index := range rows {
		if rows[index].SourceFingerprint != assets[index].SourceFingerprint ||
			rows[index].EntryFingerprint != assets[index].EntryFingerprint ||
			rows[index].ProviderCapabilityRevision != assets[index].ProviderCapabilityRevision ||
			rows[index].ArtifactPlaintextSize <= 0 || rows[index].ArtifactPlaintextDigest == "" {
			return recovery.RecoveryEligibilitySecurityObservation{}, managedRecoveryEligibilityUnavailable()
		}
	}
	digest := managedRecoveryEligibilitySecurityDigest(binding, bundleFingerprint, rows)
	if digest == "" || digest != binding.SecurityFindingSetDigest {
		return recovery.RecoveryEligibilitySecurityObservation{}, managedRecoveryEligibilityUnavailable()
	}
	disposition := recovery.SecurityFindingDispositionClean
	for _, state := range states {
		if state == capabilityspec.ScanFinding {
			disposition = recovery.SecurityFindingDispositionBlocked
			break
		}
	}
	if !managedRecoveryEligibilitySecurityDecisionMatches(binding.SecurityDecision, disposition) {
		return recovery.RecoveryEligibilitySecurityObservation{}, managedRecoveryEligibilityUnavailable()
	}
	observedAt := adapter.runtime.now().UTC()
	if observedAt.IsZero() {
		return recovery.RecoveryEligibilitySecurityObservation{}, managedRecoveryEligibilityUnavailable()
	}
	return recovery.RecoveryEligibilitySecurityObservation{
		PolicyRevision: binding.SecurityPolicyRevision, FindingSetDigest: digest,
		Disposition: disposition, Complete: true, ObservedAt: observedAt,
	}, nil
}

func (adapter *managedRecoveryEligibilitySecurityAdapter) RevalidateRecoveryEligibilitySecurityTx(
	ctx context.Context,
	tx *gorm.DB,
	binding recovery.RecoveryAuthorityBinding,
	observation recovery.RecoveryEligibilitySecurityObservation,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if adapter == nil || adapter.runtime == nil || tx == nil || !observation.Complete ||
		observation.PolicyRevision != binding.SecurityPolicyRevision ||
		observation.FindingSetDigest != binding.SecurityFindingSetDigest || observation.ObservedAt.IsZero() ||
		!managedRecoveryEligibilitySecurityDecisionMatches(binding.SecurityDecision, observation.Disposition) {
		return fmt.Errorf("%w: Recovery security authority changed", backupasset.ErrConflict)
	}
	snapshot, err := loadManagedRecoveryEligibilitySecurityPlanTx(ctx, tx, binding, true, false)
	if err != nil {
		return managedRecoveryEligibilityRevalidationError(ctx, err)
	}
	rows, bundleFingerprint, err := adapter.loadManagedRecoveryEligibilitySecurityEvidenceTx(
		ctx, tx, snapshot.items, true,
	)
	if err != nil || len(rows) != len(snapshot.items) ||
		managedRecoveryEligibilitySecurityDigest(binding, bundleFingerprint, rows) != observation.FindingSetDigest {
		return managedRecoveryEligibilityRevalidationError(ctx, err)
	}
	return nil
}

func loadManagedRecoveryEligibilitySecurityPlanTx(
	ctx context.Context,
	tx *gorm.DB,
	binding recovery.RecoveryAuthorityBinding,
	lock bool,
	loadActor bool,
) (managedRecoveryEligibilitySecurityPlanSnapshot, error) {
	if tx == nil || binding.Provider != backupasset.ProviderRsync || binding.PlanID == "" ||
		binding.PlanBindingDigest == "" || binding.PlanTransitionRevision == 0 || binding.RepositoryID == "" ||
		binding.RecoveryPointID == "" || binding.CatalogGenerationID == "" || binding.SelectionDigest == "" ||
		binding.SourceRevisionDigest == "" || binding.ManifestDigest == "" || binding.SecurityFindingSetDigest == "" ||
		binding.SecurityPolicyRevision == "" {
		return managedRecoveryEligibilitySecurityPlanSnapshot{}, managedRecoveryEligibilityUnavailable()
	}
	query := tx.WithContext(ctx)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var plan model.BackupAssetRecoveryPlan
	loaded := query.Where("id = ?", binding.PlanID).Limit(1).Find(&plan)
	if loaded.Error != nil || loaded.RowsAffected != 1 || plan.ID != binding.PlanID ||
		plan.BindingDigest != binding.PlanBindingDigest || plan.TransitionRevision != binding.PlanTransitionRevision ||
		plan.RepositoryID != binding.RepositoryID || plan.RecoveryPointID != binding.RecoveryPointID ||
		plan.CatalogGenerationID != binding.CatalogGenerationID || plan.SelectionDigest != binding.SelectionDigest ||
		plan.SourceRevisionDigest != binding.SourceRevisionDigest || plan.ImmutableManifestDigest != binding.ManifestDigest ||
		plan.SecurityDecision != string(binding.SecurityDecision) ||
		plan.SecurityDecisionDigest != binding.SecurityDecisionDigest ||
		plan.SecurityFindingSetDigest != binding.SecurityFindingSetDigest ||
		plan.SecurityPolicyRevision != binding.SecurityPolicyRevision || plan.RequesterID == 0 {
		return managedRecoveryEligibilitySecurityPlanSnapshot{}, fmt.Errorf("%w: Recovery security plan changed", backupasset.ErrConflict)
	}
	itemQuery := tx.WithContext(ctx)
	if lock {
		itemQuery = itemQuery.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var items []model.BackupAssetRecoveryPlanItem
	if err := itemQuery.Where("plan_id = ?", plan.ID).Order("ordinal ASC").Find(&items).Error; err != nil {
		return managedRecoveryEligibilitySecurityPlanSnapshot{}, err
	}
	if len(items) == 0 || plan.EstimatedItems != int64(len(items)) {
		return managedRecoveryEligibilitySecurityPlanSnapshot{}, fmt.Errorf("%w: incomplete Recovery security plan", backupasset.ErrConflict)
	}
	for ordinal, item := range items {
		if item.PlanID != plan.ID || item.Ordinal != ordinal || item.RecoveryPointID != plan.RecoveryPointID ||
			item.CatalogGenerationID != plan.CatalogGenerationID || item.EntryID == "" || item.ID == "" ||
			item.EntryType != string(backupasset.CatalogEntryFile) || item.RelativePathDigest == "" {
			return managedRecoveryEligibilitySecurityPlanSnapshot{}, fmt.Errorf("%w: invalid Recovery security plan item", backupasset.ErrConflict)
		}
	}
	snapshot := managedRecoveryEligibilitySecurityPlanSnapshot{plan: plan, items: items}
	if loadActor {
		var user model.User
		loaded = tx.WithContext(ctx).Where("id = ?", plan.RequesterID).Limit(1).Find(&user)
		if loaded.Error != nil || loaded.RowsAffected != 1 || user.ID != plan.RequesterID ||
			strings.TrimSpace(user.Username) == "" || (user.Role != "admin" && user.Role != "operator") {
			return managedRecoveryEligibilitySecurityPlanSnapshot{}, managedRecoveryEligibilityUnavailable()
		}
		snapshot.actor = content.DeliveryActor{UserID: user.ID, Username: user.Username, Role: user.Role}
	}
	return snapshot, nil
}

func validManagedRecoveryEligibilitySecurityAsset(
	binding recovery.RecoveryAuthorityBinding,
	item model.BackupAssetRecoveryPlanItem,
	asset content.AuthorizedAsset,
) bool {
	return asset.Ref == (backupasset.AssetRef{RecoveryPointID: item.RecoveryPointID, EntryID: item.EntryID}) &&
		asset.CatalogGenerationID == item.CatalogGenerationID && asset.RepositoryID == binding.RepositoryID &&
		asset.Provider == binding.Provider && asset.ProviderCapabilityRevision > 0 &&
		asset.SourceFingerprint != "" && asset.EntryFingerprint != "" && asset.Size >= 0
}

func (adapter *managedRecoveryEligibilitySecurityAdapter) loadManagedRecoveryEligibilitySecurityEvidenceTx(
	ctx context.Context,
	tx *gorm.DB,
	items []model.BackupAssetRecoveryPlanItem,
	lock bool,
) ([]managedRecoveryEligibilitySecurityEvidenceRow, string, error) {
	if adapter == nil || adapter.runtime == nil || tx == nil || len(items) == 0 {
		return nil, "", managedRecoveryEligibilityUnavailable()
	}
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1, false,
	)
	if !ok {
		return nil, "", managedRecoveryEligibilityUnavailable()
	}
	publication, err := adapter.runtime.activePublicationIdentityTx(
		ctx, tx, profile.Capability, profile.OutputProfile,
	)
	if err != nil || publication.PipelineFingerprint == "" ||
		publication.SecurityPolicyRevision != processingSecurityPolicyRevision {
		return nil, "", managedRecoveryEligibilityDependencyError(ctx, err)
	}
	bundleFingerprint, err := activeUpdaterFingerprintDB(ctx, tx, lock)
	if err != nil || bundleFingerprint == "" {
		return nil, "", managedRecoveryEligibilityDependencyError(ctx, err)
	}
	rows := make([]managedRecoveryEligibilitySecurityEvidenceRow, 0, len(items))
	for _, item := range items {
		var current []managedRecoveryEligibilitySecurityEvidenceRow
		query := tx.WithContext(ctx).Table("backup_asset_processing_jobs AS jobs").
			Select(`jobs.id AS job_id, jobs.transition_revision AS job_transition_revision,
				jobs.source_fingerprint AS source_fingerprint, jobs.entry_fingerprint AS entry_fingerprint,
				jobs.provider_capability_revision AS provider_capability_revision,
				jobs.pipeline_fingerprint AS pipeline_fingerprint,
				artifact_sets.id AS artifact_set_id, artifact_sets.manifest_digest AS artifact_set_manifest_digest,
				artifacts.id AS artifact_id, artifacts.plaintext_size AS artifact_plaintext_size,
				artifacts.plaintext_digest AS artifact_plaintext_digest`).
			Joins(`JOIN backup_asset_derived_artifact_sets AS artifact_sets
				ON artifact_sets.id = jobs.current_artifact_set_id AND artifact_sets.job_id = jobs.id`).
			Joins(`JOIN backup_asset_derived_artifacts AS artifacts
				ON artifacts.artifact_set_id = artifact_sets.id`).
			Joins(`JOIN backup_asset_processing_attempts AS attempts
				ON attempts.id = artifact_sets.attempt_id AND attempts.job_id = jobs.id`).
			Joins(`JOIN backup_asset_derived_blobs AS blobs ON blobs.id = artifacts.blob_id`).
			Joins(`JOIN backup_asset_derived_blob_references AS refs
				ON refs.artifact_id = artifacts.id AND refs.blob_id = blobs.id`).
			Where(`jobs.recovery_point_id = ? AND jobs.catalog_generation_id = ? AND jobs.entry_id = ?
				AND jobs.capability = ? AND jobs.capability_schema = ? AND jobs.pipeline_fingerprint = ?
				AND jobs.output_profile = ? AND jobs.security_policy_revision = ?
				AND jobs.state = ? AND jobs.is_current = ? AND jobs.finished_at IS NOT NULL
				AND jobs.current_attempt_id = artifact_sets.attempt_id
				AND attempts.state = ? AND attempts.is_current = ? AND attempts.finished_at IS NOT NULL`,
				item.RecoveryPointID, item.CatalogGenerationID, item.EntryID,
				profile.Capability, profile.CapabilitySchema, publication.PipelineFingerprint,
				profile.OutputProfile, processingSecurityPolicyRevision,
				processing.ProcessingSucceeded, false, "succeeded", false).
			Where(`artifact_sets.recovery_point_id = ? AND artifact_sets.catalog_generation_id = ?
				AND artifact_sets.entry_id = ? AND artifact_sets.source_fingerprint = jobs.source_fingerprint
				AND artifact_sets.security_policy_revision = ? AND artifact_sets.state = ?
				AND artifact_sets.completeness = ? AND artifact_sets.artifact_count = ?
				AND artifact_sets.projection_required = ?
				AND artifacts.ordinal = ? AND artifacts.role = ? AND artifacts.media_type = ?
				AND artifacts.completeness = ? AND artifacts.plaintext_size > 0 AND artifacts.plaintext_size <= ?
				AND blobs.state = ? AND blobs.plaintext_digest = artifacts.plaintext_digest
				AND blobs.plaintext_size = artifacts.plaintext_size
				AND refs.recovery_point_id = ? AND refs.catalog_generation_id = ? AND refs.entry_id = ?
				AND refs.source_fingerprint = jobs.source_fingerprint AND refs.state = ? AND refs.revoked_at IS NULL`,
				item.RecoveryPointID, item.CatalogGenerationID, item.EntryID,
				processingSecurityPolicyRevision, "active", processing.ArtifactComplete, 1, false,
				0, processing.ArtifactRoleMetadata, "application/json", processing.ArtifactComplete,
				malwareResultMaxBytes, "active", item.RecoveryPointID, item.CatalogGenerationID, item.EntryID, "active").
			Order("jobs.updated_at DESC, jobs.id ASC").Limit(2)
		if lock {
			query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
		}
		if err := query.Find(&current).Error; err != nil {
			return nil, "", err
		}
		if len(current) != 1 || current[0].PipelineFingerprint != publication.PipelineFingerprint ||
			current[0].ArtifactPlaintextSize <= 0 || current[0].ArtifactPlaintextDigest == "" {
			return nil, "", managedRecoveryEligibilityUnavailable()
		}
		row := current[0]
		row.PlanItemID = item.ID
		row.Ordinal = item.Ordinal
		row.RecoveryPointID = item.RecoveryPointID
		row.CatalogGenerationID = item.CatalogGenerationID
		row.EntryID = item.EntryID
		row.PlanSourceFingerprint = item.SourceFingerprint
		row.RelativePathDigest = item.RelativePathDigest
		rows = append(rows, row)
	}
	return rows, bundleFingerprint, nil
}

func managedRecoveryEligibilitySecurityDigest(
	binding recovery.RecoveryAuthorityBinding,
	bundleFingerprint string,
	rows []managedRecoveryEligibilitySecurityEvidenceRow,
) string {
	if bundleFingerprint == "" || len(rows) == 0 {
		return ""
	}
	canonical := backupasset.NewCanonicalSHA256()
	canonical.String("xirang/recovery/security-finding-set/plan/v1")
	canonical.String(processingSecurityPolicyRevision)
	canonical.String(bundleFingerprint)
	canonical.Uint64(uint64(len(rows)))
	for _, row := range rows {
		canonical.Uint64(uint64(row.Ordinal))
		canonical.String(row.RecoveryPointID)
		canonical.String(row.CatalogGenerationID)
		canonical.String(row.EntryID)
		canonical.String(row.SourceFingerprint)
		canonical.String(row.EntryFingerprint)
		canonical.Int64(row.ProviderCapabilityRevision)
		canonical.String(row.PipelineFingerprint)
		canonical.Int64(row.ArtifactPlaintextSize)
		canonical.String(row.ArtifactPlaintextDigest)
	}
	digest, err := canonical.HexDigest()
	if err != nil {
		return ""
	}
	return digest
}

func managedRecoveryEligibilitySecurityDecisionMatches(
	decision recovery.SecurityDecisionKind,
	disposition recovery.SecurityFindingDisposition,
) bool {
	switch disposition {
	case recovery.SecurityFindingDispositionClean:
		return decision == recovery.SecurityDecisionAllowClean
	case recovery.SecurityFindingDispositionBlocked:
		return decision == recovery.SecurityDecisionBlock || decision == recovery.SecurityDecisionAdminOverride
	default:
		return false
	}
}

func managedRecoveryEligibilityRevalidationError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return fmt.Errorf("%w: Recovery security authority changed", backupasset.ErrConflict)
}

var _ recovery.RecoveryEligibilitySourcePort = (*managedRecoveryEligibilitySourceAdapter)(nil)
var _ recovery.RecoveryEligibilitySecurityPort = (*managedRecoveryEligibilitySecurityAdapter)(nil)
var _ managedRecoveryEligibilityRepositoryObserver = (*repository.Service)(nil)
var _ managedRecoveryEligibilitySourceDurable = (*repository.Service)(nil)
