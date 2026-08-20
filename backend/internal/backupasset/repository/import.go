package repository

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxImportDiscoveryPageSize        = 200
	importFingerprintDomain           = "xirang.repository.import-source.v1"
	importMarkerDigestDomain          = "xirang.repository.import-marker.v1"
	importQuarantineFingerprintDomain = "xirang.repository.import-quarantine.v1"
	importFailedSourceIdentityDomain  = "xirang.import.failed-source.v1"
)

type ImportDiscoveryRequest struct {
	Limit  int
	Cursor string
}

type ImportCandidateView struct {
	ID                      string                          `json:"id"`
	RepositoryID            string                          `json:"repository_id"`
	Kind                    backupasset.ImportCandidateKind `json:"kind"`
	State                   backupasset.ImportReviewState   `json:"state"`
	AcceptedRecoveryPointID string                          `json:"accepted_recovery_point_id,omitempty"`
	Quarantined             bool                            `json:"quarantined"`
	CreatedAt               time.Time                       `json:"created_at"`
	ReviewedAt              *time.Time                      `json:"reviewed_at,omitempty"`
}

type ImportDiscoveryResult struct {
	Candidates []ImportCandidateView `json:"candidates"`
	NextCursor string                `json:"next_cursor,omitempty"`
	Discovered int                   `json:"discovered"`
	Existing   int                   `json:"existing"`
}

type ImportCandidateListRequest struct {
	Limit  int
	Cursor string
}

type ImportCandidatePage struct {
	Items      []ImportCandidateView `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

type ImportReviewRequest struct {
	Decision backupasset.ImportReviewState
	AcceptAs backupasset.ImportCandidateKind
}

type ManagedManifestProofRequest struct {
	RepositoryID    string
	Provider        backupasset.ProviderKind
	CandidateDigest string
	MarkerDigest    string
	CommitDigests   []string
}

type ManagedManifestProofVerifier interface {
	VerifyManagedManifest(context.Context, ManagedManifestProofRequest) error
}

type importCandidateLocator struct {
	Version int    `json:"version"`
	Native  string `json:"native"`
}

type importCandidateEvidence struct {
	Version              int                               `json:"version"`
	Provider             backupasset.ProviderKind          `json:"provider"`
	OpaqueDigest         string                            `json:"opaque_digest"`
	CapturedAt           time.Time                         `json:"captured_at"`
	Semantics            backupasset.PointVersionSemantics `json:"semantics"`
	SourceRevision       string                            `json:"source_revision"`
	ManagedManifestProof *importManagedManifestProof       `json:"managed_manifest_proof,omitempty"`
	Quarantined          bool                              `json:"quarantined,omitempty"`
}

type importFidelityDocument struct {
	Version              int    `json:"version"`
	FailedSourceIdentity string `json:"failed_source_identity,omitempty"`
}

type importManagedManifestProof struct {
	Version         int      `json:"version"`
	CandidateDigest string   `json:"candidate_digest"`
	MarkerDigest    string   `json:"marker_digest"`
	CommitDigests   []string `json:"commit_digests"`
}

type normalizedImportCandidate struct {
	kind        backupasset.ImportCandidateKind
	fingerprint string
	locator     string
	evidence    string
}

func (service *Service) DiscoverImportCandidates(
	ctx context.Context,
	repositoryID string,
	request ImportDiscoveryRequest,
	requestContext RequestContext,
) (ImportDiscoveryResult, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return ImportDiscoveryResult{}, err
	}
	if err := requireImportAdmin(requestContext.Actor); err != nil {
		return ImportDiscoveryResult{}, err
	}
	if err := service.requireRuntime(); err != nil {
		return ImportDiscoveryResult{}, err
	}
	if backupasset.ValidateOpaqueID(repositoryID) != nil {
		return ImportDiscoveryResult{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	}
	pageRequest, err := (provider.PageRequest{Limit: request.Limit, Cursor: request.Cursor}).Normalize(maxImportDiscoveryPageSize)
	if err != nil {
		return ImportDiscoveryResult{}, err
	}
	runtime, normalized, nextCursor, err := service.collectNormalizedImportCandidates(ctx, repositoryID, pageRequest, requestContext)
	if err != nil {
		return ImportDiscoveryResult{}, err
	}

	result := ImportDiscoveryResult{Candidates: make([]ImportCandidateView, 0, len(normalized)), NextCursor: nextCursor}
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, candidate := range normalized {
			row, created, err := service.persistImportCandidate(tx, repositoryID, candidate)
			if err != nil {
				return err
			}
			if created {
				result.Discovered++
			} else {
				result.Existing++
			}
			result.Candidates = append(result.Candidates, importCandidateView(row))
		}
		return nil
	})
	if err != nil {
		service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryImport, backupasset.AuditOutcomeBlocked, repositoryID, &runtime.task.ID, "discover", err)
		return ImportDiscoveryResult{}, err
	}
	service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryImport, backupasset.AuditOutcomeSuccess, repositoryID, &runtime.task.ID, "discover", nil)
	return result, nil
}

func (service *Service) collectNormalizedImportCandidates(
	ctx context.Context,
	repositoryID string,
	pageRequest provider.PageRequest,
	requestContext RequestContext,
) (repositoryRuntime, []normalizedImportCandidate, string, error) {
	runtime, err := service.loadRepositoryRuntime(ctx, repositoryID)
	if err != nil {
		return repositoryRuntime{}, nil, "", err
	}
	runtime.access = withRemoteAuditContext(runtime.access, requestContext, runtime.document.TaskID)
	prober, err := service.registry.Prober(runtime.access.Provider)
	if err != nil {
		return repositoryRuntime{}, nil, "", err
	}
	limits, err := service.providerOperationLimits()
	if err != nil {
		return repositoryRuntime{}, nil, "", err
	}
	observation, err := prober.Probe(ctx, runtime.access, limits)
	if err != nil {
		return repositoryRuntime{}, nil, "", err
	}
	if err := validateObservation(runtime.access, observation); err != nil {
		return repositoryRuntime{}, nil, "", err
	}
	if runtime.repository.RepositoryIdentity == nil || *runtime.repository.RepositoryIdentity != observation.RepositoryIdentity ||
		runtime.repository.ProviderKind != string(observation.Provider) {
		return repositoryRuntime{}, nil, "", fmt.Errorf("%w: repository identity mismatch", backupasset.ErrConflict)
	}
	lister, err := service.registry.PointLister(observation.Provider)
	if err != nil {
		return repositoryRuntime{}, nil, "", err
	}
	page, err := lister.ListPoints(ctx, provider.ReadSnapshot{
		RepositoryID: repositoryID, CapabilityRevision: runtime.repository.CapabilityRevision,
		SourceRevision: observation.SourceRevision, Access: runtime.access,
	}, pageRequest)
	if err != nil {
		return repositoryRuntime{}, nil, "", err
	}
	if len(page.Items) > pageRequest.Limit {
		return repositoryRuntime{}, nil, "", fmt.Errorf("%w: import discovery page exceeds limit", backupasset.ErrInvalidState)
	}
	salt, err := hexDecodeSalt(runtime.document.IdentitySalt)
	if err != nil {
		return repositoryRuntime{}, nil, "", err
	}
	normalized := make([]normalizedImportCandidate, 0, len(page.Items))
	proofRejected := false
	for _, point := range page.Items {
		var managedProof *importManagedManifestProof
		if (observation.Provider == backupasset.ProviderRsync || observation.Provider == backupasset.ProviderRclone) &&
			point.Semantics == backupasset.PointXirangManifest {
			if service.manifestProof == nil {
				return repositoryRuntime{}, nil, "", fmt.Errorf("%w: managed manifest proof verifier unavailable", backupasset.ErrInvalidState)
			}
			proofRequest, proof, proofErr := managedManifestProofForPoint(observation.Provider, repositoryID, salt, point)
			if proofErr != nil {
				normalized = append(normalized, quarantineImportCandidate(observation.Provider, repositoryID, salt, point, service.utcNow()))
				continue
			}
			if verifyErr := service.manifestProof.VerifyManagedManifest(ctx, proofRequest); verifyErr != nil {
				proofRejected = true
				continue
			}
			managedProof = &proof
		}
		item, normalizeErr := normalizeImportCandidate(observation.Provider, repositoryID, salt, point, managedProof)
		if normalizeErr != nil {
			normalized = append(normalized, quarantineImportCandidate(observation.Provider, repositoryID, salt, point, service.utcNow()))
			continue
		}
		normalized = append(normalized, item)
	}
	if proofRejected && len(normalized) == 0 {
		return repositoryRuntime{}, nil, "", fmt.Errorf("%w: managed manifest proof rejected", backupasset.ErrConflict)
	}
	return runtime, normalized, page.NextCursor, nil
}

func (service *Service) ReviewImportCandidate(
	ctx context.Context,
	repositoryID string,
	candidateID string,
	request ImportReviewRequest,
	requestContext RequestContext,
) (ImportCandidateView, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return ImportCandidateView{}, err
	}
	if service.db == nil {
		return ImportCandidateView{}, fmt.Errorf("%w: repository database unavailable", backupasset.ErrInvalidState)
	}
	if err := requireImportAdmin(requestContext.Actor); err != nil {
		return ImportCandidateView{}, err
	}
	if backupasset.ValidateOpaqueID(repositoryID) != nil || backupasset.ValidateOpaqueID(candidateID) != nil ||
		(request.Decision != backupasset.ImportReviewAccepted && request.Decision != backupasset.ImportReviewRejected) {
		return ImportCandidateView{}, fmt.Errorf("%w: invalid import review", backupasset.ErrInvalidState)
	}
	var proofRepository model.BackupRepository
	if err := service.db.WithContext(ctx).Where("id = ?", repositoryID).First(&proofRepository).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ImportCandidateView{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	} else if err != nil {
		return ImportCandidateView{}, fmt.Errorf("load import-review proof repository: %w", err)
	}
	var proofCandidate model.BackupRepositoryImportCandidate
	if err := service.db.WithContext(ctx).Where("id = ? AND repository_id = ?", candidateID, repositoryID).First(&proofCandidate).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ImportCandidateView{}, fmt.Errorf("%w: import candidate", backupasset.ErrNotFound)
	} else if err != nil {
		return ImportCandidateView{}, fmt.Errorf("load import candidate proof snapshot: %w", err)
	}
	_, proofEvidence, err := validateStoredImportCandidate(proofRepository, proofCandidate)
	if err != nil {
		return ImportCandidateView{}, err
	}
	if proofEvidence.Quarantined && request.Decision == backupasset.ImportReviewAccepted {
		return ImportCandidateView{}, fmt.Errorf("%w: quarantined import candidate cannot be accepted", backupasset.ErrInvalidState)
	}
	if request.Decision == backupasset.ImportReviewAccepted &&
		proofCandidate.ReviewState == string(backupasset.ImportReviewPending) &&
		proofCandidate.CandidateKind == string(backupasset.ImportCandidateXirangManifest) {
		if service.manifestProof == nil {
			return ImportCandidateView{}, fmt.Errorf("%w: managed manifest proof verifier unavailable", backupasset.ErrInvalidState)
		}
		proofRequest, proofErr := managedManifestProofRequestFromEvidence(proofRepository, proofEvidence)
		if proofErr != nil {
			return ImportCandidateView{}, proofErr
		}
		if verifyErr := service.manifestProof.VerifyManagedManifest(ctx, proofRequest); verifyErr != nil {
			return ImportCandidateView{}, fmt.Errorf("%w: managed manifest proof rejected", backupasset.ErrConflict)
		}
	}
	var reviewed model.BackupRepositoryImportCandidate
	err = service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repository model.BackupRepository
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", repositoryID).First(&repository).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: repository", backupasset.ErrNotFound)
		} else if err != nil {
			return fmt.Errorf("load import-review repository: %w", err)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND repository_id = ?", candidateID, repositoryID).First(&reviewed).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: import candidate", backupasset.ErrNotFound)
		} else if err != nil {
			return fmt.Errorf("load import candidate for review: %w", err)
		}
		state := backupasset.ImportReviewState(reviewed.ReviewState)
		if err := backupasset.ValidateImportReviewState(state); err != nil {
			return err
		}
		locator, evidence, err := validateStoredImportCandidate(repository, reviewed)
		if err != nil {
			return err
		}
		if evidence.Quarantined && request.Decision == backupasset.ImportReviewAccepted {
			return fmt.Errorf("%w: quarantined import candidate cannot be accepted", backupasset.ErrInvalidState)
		}
		if repository.ProviderKind != proofRepository.ProviderKind || repository.CapabilityRevision != proofRepository.CapabilityRevision ||
			reviewed.CandidateKind != proofCandidate.CandidateKind || reviewed.SourceFingerprint != proofCandidate.SourceFingerprint ||
			reviewed.EncryptedProviderLocator != proofCandidate.EncryptedProviderLocator || reviewed.EncryptedEvidence != proofCandidate.EncryptedEvidence ||
			reviewed.ReviewState != proofCandidate.ReviewState {
			return fmt.Errorf("%w: import review proof snapshot drift", backupasset.ErrConflict)
		}
		if state != backupasset.ImportReviewPending {
			if state != request.Decision {
				return fmt.Errorf("%w: import review is terminal", backupasset.ErrConflict)
			}
			return validateTerminalImportReview(tx, reviewed, request.AcceptAs)
		}
		now := service.utcNow()
		reviewedBy := requestContext.Actor.UserID
		reviewed.ReviewState = string(request.Decision)
		reviewed.ReviewedBy = &reviewedBy
		reviewed.ReviewedAt = &now
		reviewed.UpdatedAt = now
		if request.Decision == backupasset.ImportReviewAccepted {
			point, err := acceptedImportRecoveryPoint(repository, reviewed, locator, evidence, request.AcceptAs, now)
			if err != nil {
				return err
			}
			point, err = bindOrCreateAcceptedImportRecoveryPoint(tx, point)
			if err != nil {
				return err
			}
			reviewed.AcceptedRecoveryPointID = &point.ID
		}
		update := tx.Model(&model.BackupRepositoryImportCandidate{}).Where("id = ? AND review_state = ?", reviewed.ID, backupasset.ImportReviewPending).
			Updates(map[string]any{
				"review_state": reviewed.ReviewState, "reviewed_by": reviewedBy, "reviewed_at": now,
				"accepted_recovery_point_id": reviewed.AcceptedRecoveryPointID, "updated_at": now,
			})
		if update.Error != nil {
			return fmt.Errorf("persist terminal import review: %w", update.Error)
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("%w: import candidate review raced", backupasset.ErrConflict)
		}
		return nil
	})
	if err != nil {
		service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryImport, backupasset.AuditOutcomeBlocked, repositoryID, nil, "review", err)
		return ImportCandidateView{}, err
	}
	service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryImport, backupasset.AuditOutcomeSuccess, repositoryID, nil, "review", nil)
	return importCandidateView(reviewed), nil
}

func managedManifestProofRequestFromEvidence(
	repository model.BackupRepository,
	evidence importCandidateEvidence,
) (ManagedManifestProofRequest, error) {
	proof := evidence.ManagedManifestProof
	providerKind := backupasset.ProviderKind(repository.ProviderKind)
	if (providerKind != backupasset.ProviderRsync && providerKind != backupasset.ProviderRclone) ||
		backupasset.ValidateOpaqueID(repository.ID) != nil || !validManagedManifestProof(proof, evidence.OpaqueDigest, evidence.SourceRevision) {
		return ManagedManifestProofRequest{}, fmt.Errorf("%w: invalid managed manifest proof snapshot", backupasset.ErrInvalidState)
	}
	return ManagedManifestProofRequest{
		RepositoryID: repository.ID, Provider: providerKind, CandidateDigest: proof.CandidateDigest,
		MarkerDigest: proof.MarkerDigest, CommitDigests: append([]string(nil), proof.CommitDigests...),
	}, nil
}

func (service *Service) ListImportCandidates(
	ctx context.Context,
	repositoryID string,
	request ImportCandidateListRequest,
	scope VisibilityScope,
	requestContext RequestContext,
) (ImportCandidatePage, error) {
	if err := service.ensureEnabled(requestContext.CorrelationID); err != nil {
		return ImportCandidatePage{}, err
	}
	if service.db == nil {
		return ImportCandidatePage{}, fmt.Errorf("%w: repository database unavailable", backupasset.ErrInvalidState)
	}
	if scope.Role != "admin" || scope.UserID == 0 {
		return ImportCandidatePage{}, fmt.Errorf("%w: administrator scope required", backupasset.ErrForbidden)
	}
	if backupasset.ValidateOpaqueID(repositoryID) != nil || request.Limit < 0 {
		return ImportCandidatePage{}, fmt.Errorf("%w: invalid import candidate list", backupasset.ErrInvalidState)
	}
	limit := request.Limit
	if limit == 0 {
		limit = 100
	}
	if limit > maxImportDiscoveryPageSize {
		limit = maxImportDiscoveryPageSize
	}
	var repositoryCount int64
	if err := service.db.WithContext(ctx).Model(&model.BackupRepository{}).Where("id = ?", repositoryID).Count(&repositoryCount).Error; err != nil {
		return ImportCandidatePage{}, fmt.Errorf("load import-candidate repository: %w", err)
	}
	if repositoryCount != 1 {
		return ImportCandidatePage{}, fmt.Errorf("%w: repository", backupasset.ErrNotFound)
	}
	query := service.db.WithContext(ctx).Model(&model.BackupRepositoryImportCandidate{}).Where("repository_id = ?", repositoryID)
	if request.Cursor != "" {
		if backupasset.ValidateOpaqueID(request.Cursor) != nil {
			return ImportCandidatePage{}, fmt.Errorf("%w: invalid import candidate cursor", backupasset.ErrInvalidState)
		}
		var anchor model.BackupRepositoryImportCandidate
		if err := service.db.WithContext(ctx).Select("id", "created_at").
			Where("id = ? AND repository_id = ?", request.Cursor, repositoryID).First(&anchor).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ImportCandidatePage{}, fmt.Errorf("%w: invalid import candidate cursor", backupasset.ErrInvalidState)
		} else if err != nil {
			return ImportCandidatePage{}, fmt.Errorf("load import candidate cursor: %w", err)
		}
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", anchor.CreatedAt, anchor.CreatedAt, anchor.ID)
	}
	var rows []model.BackupRepositoryImportCandidate
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return ImportCandidatePage{}, fmt.Errorf("list import candidates: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	page := ImportCandidatePage{Items: make([]ImportCandidateView, 0, len(rows))}
	for _, row := range rows {
		page.Items = append(page.Items, importCandidateView(row))
	}
	if hasMore {
		page.NextCursor = rows[len(rows)-1].ID
	}
	service.writeAudit(ctx, requestContext, backupasset.AuditActionRepositoryImport, backupasset.AuditOutcomeSuccess, repositoryID, nil, "candidate_list", nil)
	return page, nil
}

func requireImportAdmin(actor backupasset.AuditActor) error {
	if actor.UserID == 0 || actor.Role != "admin" {
		return fmt.Errorf("%w: administrator actor required", backupasset.ErrForbidden)
	}
	return nil
}

func normalizeImportCandidate(
	providerKind backupasset.ProviderKind,
	repositoryID string,
	salt []byte,
	point provider.NativePoint,
	managedProof *importManagedManifestProof,
) (normalizedImportCandidate, error) {
	if backupasset.ValidateOpaqueID(repositoryID) != nil || len(salt) != provider.IdentitySaltBytes || !isLowerHex64(point.OpaqueDigest) ||
		point.CapturedAt.IsZero() || point.CapturedAt.Location() != time.UTC || strings.TrimSpace(point.SourceRevision) == "" ||
		strings.TrimSpace(point.Locator.Native) == "" || strings.ContainsAny(point.SourceRevision+point.Locator.Native, "\r\n\x00") {
		return normalizedImportCandidate{}, fmt.Errorf("%w: invalid Provider import candidate", backupasset.ErrInvalidState)
	}
	kind := backupasset.ImportCandidateKind("")
	switch {
	case providerKind == backupasset.ProviderRestic && point.Semantics == backupasset.PointNativeSnapshot &&
		validResticSnapshotIdentity(point.Locator.Native, point.SourceRevision):
		kind = backupasset.ImportCandidateNativeSnapshot
	case providerKind == backupasset.ProviderRestic && point.Semantics == backupasset.PointImportedBaseline &&
		validResticSnapshotIdentity(point.Locator.Native, point.SourceRevision):
		kind = backupasset.ImportCandidateImportedBaseline
	case (providerKind == backupasset.ProviderRsync || providerKind == backupasset.ProviderRclone) &&
		point.Semantics == backupasset.PointXirangManifest && isLowerHex64(point.SourceRevision) &&
		validManagedManifestProof(managedProof, point.OpaqueDigest, point.SourceRevision):
		kind = backupasset.ImportCandidateXirangManifest
	case (providerKind == backupasset.ProviderRsync || providerKind == backupasset.ProviderRclone) &&
		point.Semantics == backupasset.PointImportedBaseline:
		kind = backupasset.ImportCandidateImportedBaseline
	case (providerKind == backupasset.ProviderRsync || providerKind == backupasset.ProviderRclone) &&
		point.Semantics == backupasset.PointMutableHead:
		kind = backupasset.ImportCandidateMutableHead
	default:
		return normalizedImportCandidate{}, fmt.Errorf("%w: unattributable Provider import candidate", backupasset.ErrInvalidState)
	}
	if err := backupasset.ValidateImportCandidateKind(kind); err != nil {
		return normalizedImportCandidate{}, err
	}
	locatorJSON, err := json.Marshal(struct {
		Version int    `json:"version"`
		Native  string `json:"native"`
	}{Version: 1, Native: point.Locator.Native})
	if err != nil {
		return normalizedImportCandidate{}, fmt.Errorf("marshal import candidate locator: %w", err)
	}
	evidenceJSON, err := json.Marshal(importCandidateEvidence{
		Version: 1, Provider: providerKind, OpaqueDigest: point.OpaqueDigest, CapturedAt: point.CapturedAt.UTC(),
		Semantics: point.Semantics, SourceRevision: point.SourceRevision, ManagedManifestProof: managedProof,
	})
	if err != nil {
		return normalizedImportCandidate{}, fmt.Errorf("marshal import candidate evidence: %w", err)
	}
	mac := hmac.New(sha256.New, salt)
	for _, value := range []string{importFingerprintDomain, repositoryID, string(providerKind), string(kind), point.OpaqueDigest, point.SourceRevision, point.Locator.Native} {
		_, _ = mac.Write([]byte(value))
		_, _ = mac.Write([]byte{0})
	}
	return normalizedImportCandidate{
		kind: kind, fingerprint: hex.EncodeToString(mac.Sum(nil)), locator: string(locatorJSON), evidence: string(evidenceJSON),
	}, nil
}

func validResticSnapshotIdentity(locator, sourceRevision string) bool {
	return isLowerHex64(sourceRevision) && locator == sourceRevision
}

func managedManifestProofForPoint(
	providerKind backupasset.ProviderKind,
	repositoryID string,
	salt []byte,
	point provider.NativePoint,
) (ManagedManifestProofRequest, importManagedManifestProof, error) {
	if (providerKind != backupasset.ProviderRsync && providerKind != backupasset.ProviderRclone) ||
		backupasset.ValidateOpaqueID(repositoryID) != nil || len(salt) != provider.IdentitySaltBytes ||
		!isLowerHex64(point.OpaqueDigest) || !isLowerHex64(point.SourceRevision) ||
		strings.TrimSpace(point.Locator.Native) == "" || strings.ContainsAny(point.Locator.Native, "\r\n\x00") {
		return ManagedManifestProofRequest{}, importManagedManifestProof{}, fmt.Errorf("%w: invalid managed manifest proof input", backupasset.ErrInvalidState)
	}
	mac := hmac.New(sha256.New, salt)
	for _, value := range []string{importMarkerDigestDomain, repositoryID, string(providerKind), point.Locator.Native} {
		_, _ = mac.Write([]byte(value))
		_, _ = mac.Write([]byte{0})
	}
	proof := importManagedManifestProof{
		Version: 1, CandidateDigest: point.OpaqueDigest, MarkerDigest: hex.EncodeToString(mac.Sum(nil)),
		CommitDigests: []string{point.SourceRevision},
	}
	request := ManagedManifestProofRequest{
		RepositoryID: repositoryID, Provider: providerKind, CandidateDigest: proof.CandidateDigest,
		MarkerDigest: proof.MarkerDigest, CommitDigests: append([]string(nil), proof.CommitDigests...),
	}
	return request, proof, nil
}

func validManagedManifestProof(proof *importManagedManifestProof, candidateDigest, sourceRevision string) bool {
	if proof == nil || proof.Version != 1 || proof.CandidateDigest != candidateDigest || !isLowerHex64(proof.CandidateDigest) ||
		!isLowerHex64(proof.MarkerDigest) || len(proof.CommitDigests) != 1 || proof.CommitDigests[0] != sourceRevision ||
		!isLowerHex64(proof.CommitDigests[0]) {
		return false
	}
	return true
}

func validateStoredImportCandidate(
	repository model.BackupRepository,
	candidate model.BackupRepositoryImportCandidate,
) (importCandidateLocator, importCandidateEvidence, error) {
	kind := backupasset.ImportCandidateKind(candidate.CandidateKind)
	providerKind := backupasset.ProviderKind(repository.ProviderKind)
	if candidate.RepositoryID != repository.ID || backupasset.ValidateImportCandidateKind(kind) != nil ||
		!isLowerHex64(candidate.SourceFingerprint) ||
		(providerKind != backupasset.ProviderRestic && providerKind != backupasset.ProviderRsync && providerKind != backupasset.ProviderRclone) {
		return importCandidateLocator{}, importCandidateEvidence{}, fmt.Errorf("%w: invalid persisted import candidate", backupasset.ErrInvalidState)
	}
	var locator importCandidateLocator
	if err := decodeStrictImportJSON(candidate.EncryptedProviderLocator, &locator); err != nil || locator.Version != 1 ||
		strings.TrimSpace(locator.Native) == "" || strings.ContainsAny(locator.Native, "\r\n\x00") {
		return importCandidateLocator{}, importCandidateEvidence{}, fmt.Errorf("%w: invalid import candidate locator", backupasset.ErrInvalidState)
	}
	var evidence importCandidateEvidence
	if err := decodeStrictImportJSON(candidate.EncryptedEvidence, &evidence); err != nil || evidence.Version != 1 ||
		evidence.Provider != providerKind || !isLowerHex64(evidence.OpaqueDigest) || evidence.CapturedAt.IsZero() ||
		evidence.CapturedAt.Location() != time.UTC || strings.TrimSpace(evidence.SourceRevision) == "" ||
		strings.ContainsAny(evidence.SourceRevision, "\r\n\x00") {
		return importCandidateLocator{}, importCandidateEvidence{}, fmt.Errorf("%w: invalid import candidate evidence", backupasset.ErrInvalidState)
	}
	if evidence.Quarantined {
		return locator, evidence, nil
	}
	valid := false
	switch kind {
	case backupasset.ImportCandidateNativeSnapshot:
		valid = providerKind == backupasset.ProviderRestic && evidence.Semantics == backupasset.PointNativeSnapshot &&
			validResticSnapshotIdentity(locator.Native, evidence.SourceRevision) && evidence.ManagedManifestProof == nil
	case backupasset.ImportCandidateXirangManifest:
		valid = (providerKind == backupasset.ProviderRsync || providerKind == backupasset.ProviderRclone) &&
			evidence.Semantics == backupasset.PointXirangManifest && isLowerHex64(evidence.SourceRevision) &&
			validManagedManifestProof(evidence.ManagedManifestProof, evidence.OpaqueDigest, evidence.SourceRevision)
	case backupasset.ImportCandidateImportedBaseline:
		valid = evidence.Semantics == backupasset.PointImportedBaseline && evidence.ManagedManifestProof == nil &&
			((providerKind == backupasset.ProviderRestic && validResticSnapshotIdentity(locator.Native, evidence.SourceRevision)) ||
				providerKind == backupasset.ProviderRsync || providerKind == backupasset.ProviderRclone)
	case backupasset.ImportCandidateMutableHead:
		valid = (providerKind == backupasset.ProviderRsync || providerKind == backupasset.ProviderRclone) &&
			evidence.Semantics == backupasset.PointMutableHead && evidence.ManagedManifestProof == nil
	}
	if !valid {
		return importCandidateLocator{}, importCandidateEvidence{}, fmt.Errorf("%w: import candidate evidence mismatch", backupasset.ErrInvalidState)
	}
	return locator, evidence, nil
}

func decodeStrictImportJSON(document string, target any) error {
	if strings.TrimSpace(document) == "" || len(document) > 64<<10 {
		return fmt.Errorf("invalid import JSON")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing import JSON")
	}
	return nil
}

func acceptedImportRecoveryPoint(
	repository model.BackupRepository,
	candidate model.BackupRepositoryImportCandidate,
	locator importCandidateLocator,
	evidence importCandidateEvidence,
	acceptAs backupasset.ImportCandidateKind,
	now time.Time,
) (model.RecoveryPoint, error) {
	candidateKind := backupasset.ImportCandidateKind(candidate.CandidateKind)
	acceptAs, err := normalizeAcceptedImportDisposition(candidateKind, acceptAs)
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	semantics := backupasset.PointVersionSemantics(acceptAs)
	state := backupasset.RecoveryPointCommitted
	immutability := backupasset.ImmutabilityLevel(repository.ImmutabilityLevel)
	var observedAt *time.Time
	var committedAt *time.Time
	if acceptAs == backupasset.ImportCandidateMutableHead {
		semantics = backupasset.PointMutableHead
		state = backupasset.RecoveryPointObserved
		immutability = backupasset.ImmutabilityMutable
		observed := now
		observedAt = &observed
	} else {
		committed := now
		committedAt = &committed
		if semantics == backupasset.PointXirangManifest {
			immutability = backupasset.ImmutabilityXirangManaged
		} else if semantics == backupasset.PointImportedBaseline && immutability != backupasset.ImmutabilityStorageWORM {
			if repository.ProviderKind == string(backupasset.ProviderRestic) {
				immutability = backupasset.ImmutabilityBackendVersioned
			} else {
				immutability = backupasset.ImmutabilityXirangManaged
			}
		}
	}
	captured := evidence.CapturedAt.UTC()
	point := model.RecoveryPoint{
		ID: id, RepositoryID: repository.ID, LineageJSON: `{}`, EncryptedProviderLocator: candidate.EncryptedProviderLocator,
		Semantics: string(semantics), State: string(state), CapturedAt: &captured, CommittedAt: committedAt, ObservedAt: observedAt,
		SourceFingerprint: candidate.SourceFingerprint, ManifestDigestAlgorithm: "sha256", ManifestDigest: evidence.OpaqueDigest,
		ConsistencyJSON: `{}`, FidelityJSON: `{}`, PointRevision: 1, CapabilityRevision: repository.CapabilityRevision,
		CapabilitiesJSON: repository.CapabilitiesJSON, ImmutabilityLevel: string(immutability),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone), CreatedAt: now, UpdatedAt: now,
	}
	if candidateKind == backupasset.ImportCandidateMutableHead && acceptAs == backupasset.ImportCandidateImportedBaseline {
		identity, identityErr := failedImportSourceIdentity(repository)
		if identityErr != nil {
			return model.RecoveryPoint{}, identityErr
		}
		fidelity, marshalErr := json.Marshal(importFidelityDocument{Version: 1, FailedSourceIdentity: identity})
		if marshalErr != nil {
			return model.RecoveryPoint{}, fmt.Errorf("marshal failed-source fidelity: %w", marshalErr)
		}
		point.FidelityJSON = string(fidelity)
	}
	if strings.TrimSpace(locator.Native) == "" {
		return model.RecoveryPoint{}, fmt.Errorf("%w: empty accepted import locator", backupasset.ErrInvalidState)
	}
	return point, nil
}

func normalizeAcceptedImportDisposition(
	candidateKind backupasset.ImportCandidateKind,
	acceptAs backupasset.ImportCandidateKind,
) (backupasset.ImportCandidateKind, error) {
	if acceptAs == "" && candidateKind != backupasset.ImportCandidateMutableHead {
		acceptAs = candidateKind
	}
	switch candidateKind {
	case backupasset.ImportCandidateMutableHead:
		if acceptAs != backupasset.ImportCandidateMutableHead && acceptAs != backupasset.ImportCandidateImportedBaseline {
			return "", fmt.Errorf("%w: mutable import requires an explicit reviewed disposition", backupasset.ErrInvalidState)
		}
	default:
		if acceptAs != candidateKind {
			return "", fmt.Errorf("%w: import candidate cannot be relabeled", backupasset.ErrConflict)
		}
	}
	return acceptAs, nil
}

func bindOrCreateAcceptedImportRecoveryPoint(tx *gorm.DB, proposed model.RecoveryPoint) (model.RecoveryPoint, error) {
	var existing model.RecoveryPoint
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND source_fingerprint = ?", proposed.RepositoryID, proposed.SourceFingerprint).
		First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if proposed.Semantics == string(backupasset.PointMutableHead) {
			var mutableCount int64
			if countErr := tx.Model(&model.RecoveryPoint{}).
				Where("repository_id = ? AND semantics = ?", proposed.RepositoryID, backupasset.PointMutableHead).
				Count(&mutableCount).Error; countErr != nil {
				return model.RecoveryPoint{}, fmt.Errorf("query existing mutable import point: %w", countErr)
			}
			if mutableCount != 0 {
				return model.RecoveryPoint{}, fmt.Errorf("%w: mutable import head already exists", backupasset.ErrConflict)
			}
		}
		if err := rejectDuplicateFailedSourceBaseline(tx, proposed); err != nil {
			return model.RecoveryPoint{}, err
		}
		if createErr := tx.Create(&proposed).Error; createErr != nil {
			return model.RecoveryPoint{}, fmt.Errorf("create accepted import RecoveryPoint: %w", createErr)
		}
		return proposed, nil
	}
	if err != nil {
		return model.RecoveryPoint{}, fmt.Errorf("load matching import RecoveryPoint: %w", err)
	}
	if existing.Semantics != proposed.Semantics || existing.State != proposed.State ||
		existing.SourceFingerprint != proposed.SourceFingerprint || existing.ManifestDigestAlgorithm != proposed.ManifestDigestAlgorithm ||
		existing.ManifestDigest != proposed.ManifestDigest || existing.EncryptedProviderLocator != proposed.EncryptedProviderLocator ||
		existing.CapturedAt == nil || proposed.CapturedAt == nil || !existing.CapturedAt.Equal(*proposed.CapturedAt) ||
		existing.CapabilityRevision < 1 || existing.ImmutabilityLevel == string(backupasset.ImmutabilityMutable) &&
		proposed.Semantics != string(backupasset.PointMutableHead) {
		return model.RecoveryPoint{}, fmt.Errorf("%w: existing RecoveryPoint does not match import evidence", backupasset.ErrConflict)
	}
	return existing, nil
}

func validateTerminalImportReview(
	tx *gorm.DB,
	candidate model.BackupRepositoryImportCandidate,
	acceptAs backupasset.ImportCandidateKind,
) error {
	if candidate.ReviewedBy == nil || *candidate.ReviewedBy == 0 || candidate.ReviewedAt == nil || candidate.ReviewedAt.IsZero() {
		return fmt.Errorf("%w: incomplete terminal import review", backupasset.ErrInvalidState)
	}
	state := backupasset.ImportReviewState(candidate.ReviewState)
	if state == backupasset.ImportReviewRejected {
		if candidate.AcceptedRecoveryPointID != nil {
			return fmt.Errorf("%w: rejected import candidate has a RecoveryPoint", backupasset.ErrInvalidState)
		}
		return nil
	}
	if state != backupasset.ImportReviewAccepted || candidate.AcceptedRecoveryPointID == nil ||
		backupasset.ValidateOpaqueID(*candidate.AcceptedRecoveryPointID) != nil {
		return fmt.Errorf("%w: accepted import candidate is not mapped", backupasset.ErrInvalidState)
	}
	var point model.RecoveryPoint
	if err := tx.Where("id = ? AND repository_id = ?", *candidate.AcceptedRecoveryPointID, candidate.RepositoryID).First(&point).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: accepted import RecoveryPoint", backupasset.ErrInvalidState)
	} else if err != nil {
		return fmt.Errorf("load accepted import RecoveryPoint: %w", err)
	}
	if point.SourceFingerprint != candidate.SourceFingerprint {
		return fmt.Errorf("%w: accepted import mapping drift", backupasset.ErrInvalidState)
	}
	disposition, err := normalizeAcceptedImportDisposition(backupasset.ImportCandidateKind(candidate.CandidateKind), acceptAs)
	if err != nil {
		return err
	}
	if point.Semantics != string(disposition) {
		return fmt.Errorf("%w: accepted import disposition is terminal", backupasset.ErrConflict)
	}
	return nil
}

func (service *Service) persistImportCandidate(
	tx *gorm.DB,
	repositoryID string,
	candidate normalizedImportCandidate,
) (model.BackupRepositoryImportCandidate, bool, error) {
	var existing model.BackupRepositoryImportCandidate
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("repository_id = ? AND source_fingerprint = ?", repositoryID, candidate.fingerprint).
		First(&existing).Error
	if err == nil {
		if existing.CandidateKind != string(candidate.kind) || existing.EncryptedProviderLocator != candidate.locator || existing.EncryptedEvidence != candidate.evidence {
			return model.BackupRepositoryImportCandidate{}, false, fmt.Errorf("%w: import candidate fingerprint collision", backupasset.ErrConflict)
		}
		return existing, false, nil
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		return model.BackupRepositoryImportCandidate{}, false, fmt.Errorf("load import candidate: %w", err)
	}
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return model.BackupRepositoryImportCandidate{}, false, err
	}
	now := service.utcNow()
	row := model.BackupRepositoryImportCandidate{
		ID: id, RepositoryID: repositoryID, CandidateKind: string(candidate.kind), SourceFingerprint: candidate.fingerprint,
		EncryptedProviderLocator: candidate.locator, EncryptedEvidence: candidate.evidence,
		ReviewState: string(backupasset.ImportReviewPending), CreatedAt: now, UpdatedAt: now,
	}
	create := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repository_id"}, {Name: "source_fingerprint"}},
		DoNothing: true,
	}).Create(&row)
	if create.Error != nil {
		return model.BackupRepositoryImportCandidate{}, false, fmt.Errorf("create import candidate: %w", create.Error)
	}
	if create.RowsAffected == 0 {
		var winner model.BackupRepositoryImportCandidate
		if reloadErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("repository_id = ? AND source_fingerprint = ?", repositoryID, candidate.fingerprint).
			First(&winner).Error; reloadErr != nil {
			return model.BackupRepositoryImportCandidate{}, false, fmt.Errorf("reload import candidate race winner: %w", reloadErr)
		}
		var repository model.BackupRepository
		if reloadErr := tx.Where("id = ?", repositoryID).First(&repository).Error; reloadErr != nil {
			return model.BackupRepositoryImportCandidate{}, false, fmt.Errorf("reload import candidate repository: %w", reloadErr)
		}
		if winner.RepositoryID != repositoryID || winner.SourceFingerprint != candidate.fingerprint ||
			winner.CandidateKind != string(candidate.kind) || winner.EncryptedProviderLocator != candidate.locator ||
			winner.EncryptedEvidence != candidate.evidence {
			return model.BackupRepositoryImportCandidate{}, false, fmt.Errorf("%w: import candidate race winner mismatch", backupasset.ErrConflict)
		}
		if _, _, validateErr := validateStoredImportCandidate(repository, winner); validateErr != nil {
			return model.BackupRepositoryImportCandidate{}, false, fmt.Errorf("%w: import candidate race winner invalid", backupasset.ErrConflict)
		}
		return winner, false, nil
	}
	if create.RowsAffected != 1 {
		return model.BackupRepositoryImportCandidate{}, false, fmt.Errorf("%w: invalid import candidate create result", backupasset.ErrInvalidState)
	}
	return row, true, nil
}

func (service *Service) nextImportListingCursor(repositoryID string) string {
	if service == nil {
		return ""
	}
	service.importListingMu.Lock()
	defer service.importListingMu.Unlock()
	if service.importListingCursors == nil {
		return ""
	}
	return service.importListingCursors[repositoryID]
}

func (service *Service) rememberImportListingCursor(repositoryID, nextCursor string) {
	if service == nil {
		return
	}
	service.importListingMu.Lock()
	defer service.importListingMu.Unlock()
	if service.importListingCursors == nil {
		service.importListingCursors = map[string]string{}
	}
	if nextCursor == "" {
		delete(service.importListingCursors, repositoryID)
		return
	}
	service.importListingCursors[repositoryID] = nextCursor
}

func (service *Service) rememberImportListingPage(
	repositoryID, requestCursor, nextCursor string,
	items []normalizedImportCandidate,
) (map[string]normalizedImportCandidate, bool) {
	page := make(map[string]normalizedImportCandidate, len(items))
	for _, item := range items {
		page[item.fingerprint] = item
	}
	if service == nil {
		return page, requestCursor == "" && nextCursor == ""
	}
	service.importListingMu.Lock()
	defer service.importListingMu.Unlock()
	if service.importListingSeen == nil {
		service.importListingSeen = map[string]map[string]normalizedImportCandidate{}
	}
	if service.importListingFromEmpty == nil {
		service.importListingFromEmpty = map[string]bool{}
	}
	if service.importCycleStartedAt == nil {
		service.importCycleStartedAt = map[string]time.Time{}
	}
	if requestCursor == "" {
		service.importListingFromEmpty[repositoryID] = true
		service.importCycleStartedAt[repositoryID] = service.utcNow()
	}
	service.importListingSeen[repositoryID] = page
	complete := service.importListingFromEmpty[repositoryID] && nextCursor == ""
	if complete {
		delete(service.importListingSeen, repositoryID)
		delete(service.importListingFromEmpty, repositoryID)
		if service.importListingComplete == nil {
			service.importListingComplete = map[string]bool{}
		}
		service.importListingComplete[repositoryID] = true
		return page, true
	}
	return page, false
}

func (service *Service) importCycleStart(repositoryID string) (time.Time, bool) {
	if service == nil {
		return time.Time{}, false
	}
	service.importListingMu.Lock()
	defer service.importListingMu.Unlock()
	startedAt, ok := service.importCycleStartedAt[repositoryID]
	return startedAt, ok
}

func (service *Service) importCycleRefreshTime(repositoryID string) time.Time {
	now := service.utcNow()
	startedAt, ok := service.importCycleStart(repositoryID)
	if ok && !now.After(startedAt) {
		return startedAt.Add(time.Second)
	}
	return now
}

func (service *Service) refreshPendingImportCandidatesOnPage(
	ctx context.Context,
	repositoryID string,
	page map[string]normalizedImportCandidate,
) error {
	if service == nil || service.db == nil || len(page) == 0 {
		return nil
	}
	fingerprints := make([]string, 0, len(page))
	for fingerprint := range page {
		if fingerprint != "" {
			fingerprints = append(fingerprints, fingerprint)
		}
	}
	if len(fingerprints) == 0 {
		return nil
	}
	if err := service.db.WithContext(ctx).Model(&model.BackupRepositoryImportCandidate{}).
		Where("repository_id = ? AND review_state = ? AND source_fingerprint IN ?",
			repositoryID, backupasset.ImportReviewPending, fingerprints).
		Update("updated_at", service.importCycleRefreshTime(repositoryID)).Error; err != nil {
		return fmt.Errorf("refresh pending import candidates on provider page: %w", err)
	}
	return nil
}

func (service *Service) finishImportListingCycle(repositoryID string) {
	if service == nil {
		return
	}
	service.importListingMu.Lock()
	defer service.importListingMu.Unlock()
	delete(service.importListingSeen, repositoryID)
	delete(service.importListingFromEmpty, repositoryID)
	delete(service.importCycleStartedAt, repositoryID)
	delete(service.importListingComplete, repositoryID)
	delete(service.importStaleAfterID, repositoryID)
}

func (service *Service) completedImportListingRepos() []string {
	if service == nil {
		return nil
	}
	service.importListingMu.Lock()
	defer service.importListingMu.Unlock()
	repos := make([]string, 0, len(service.importListingComplete))
	for repositoryID, complete := range service.importListingComplete {
		if complete {
			repos = append(repos, repositoryID)
		}
	}
	return repos
}

func (service *Service) importListingSweepPending(repositoryID string) bool {
	if service == nil {
		return false
	}
	service.importListingMu.Lock()
	defer service.importListingMu.Unlock()
	return service.importListingComplete[repositoryID]
}

func (service *Service) sweepStalePendingForRepository(ctx context.Context, repositoryID string, limit int) (bool, int, error) {
	if limit < 1 {
		limit = 50
	}
	startedAt, ok := service.importCycleStart(repositoryID)
	if !ok {
		return true, 0, nil
	}
	service.importListingMu.Lock()
	afterID := ""
	if service.importStaleAfterID != nil {
		afterID = service.importStaleAfterID[repositoryID]
	}
	service.importListingMu.Unlock()
	query := service.db.WithContext(ctx).
		Where("repository_id = ? AND review_state = ?", repositoryID, backupasset.ImportReviewPending)
	if afterID != "" {
		query = query.Where("id > ?", afterID)
	}
	var candidates []model.BackupRepositoryImportCandidate
	if err := query.Order("id ASC").Limit(limit).Find(&candidates).Error; err != nil {
		return false, 0, fmt.Errorf("sweep stale pending import candidates: %w", err)
	}
	if len(candidates) == 0 {
		return true, 0, nil
	}
	lastID := afterID
	for _, candidate := range candidates {
		lastID = candidate.ID
		if candidate.UpdatedAt.After(startedAt) {
			continue
		}
		if err := service.db.WithContext(ctx).
			Where("id = ? AND review_state = ?", candidate.ID, backupasset.ImportReviewPending).
			Delete(&model.BackupRepositoryImportCandidate{}).Error; err != nil {
			return false, 0, fmt.Errorf("drop stale pending import candidate: %w", err)
		}
	}
	if len(candidates) < limit {
		return true, len(candidates), nil
	}
	service.importListingMu.Lock()
	if service.importStaleAfterID == nil {
		service.importStaleAfterID = map[string]string{}
	}
	service.importStaleAfterID[repositoryID] = lastID
	service.importListingMu.Unlock()
	return false, len(candidates), nil
}

func (service *Service) ReconcileImports(ctx context.Context, limit int) (int, error) {
	if service == nil || service.db == nil {
		return 0, fmt.Errorf("%w: repository import reconciliation is unavailable", backupasset.ErrInvalidState)
	}
	if limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("%w: invalid import reconciliation batch", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !service.db.Migrator().HasTable(&model.BackupRepositoryImportCandidate{}) {
		return 0, nil
	}
	if err := service.requireRuntime(); err != nil {
		return 0, err
	}
	pageRequest, err := (provider.PageRequest{Limit: maxImportDiscoveryPageSize}).Normalize(maxImportDiscoveryPageSize)
	if err != nil {
		return 0, err
	}
	const reconcilePageSize = 50
	attempted := 0
	inspected := 0
	pendingSweepRepos := service.completedImportListingRepos()
	for _, repositoryID := range pendingSweepRepos {
		if inspected >= limit {
			return attempted, nil
		}
		remaining := limit - inspected
		sweepLimit := reconcilePageSize
		if remaining < sweepLimit {
			sweepLimit = remaining
		}
		done, swept, sweepErr := service.sweepStalePendingForRepository(ctx, repositoryID, sweepLimit)
		if sweepErr != nil {
			return attempted, sweepErr
		}
		inspected += swept
		attempted += swept
		if done {
			service.finishImportListingCycle(repositoryID)
		}
	}
	if len(pendingSweepRepos) > 0 {
		return attempted, nil
	}
	type repoListing struct {
		repository model.BackupRepository
		current    map[string]normalizedImportCandidate
		nextCursor string
		complete   bool
		skip       bool
		sweepOnly  bool
	}
	listings := make(map[string]*repoListing)
	var listingErr error
	afterID := service.importAfterID
	for inspected < limit {
		query := service.db.WithContext(ctx).Where("review_state = ?", backupasset.ImportReviewPending)
		if afterID != "" {
			query = query.Where("id > ?", afterID)
		}
		var candidates []model.BackupRepositoryImportCandidate
		if err := query.Order("id ASC").Limit(reconcilePageSize).Find(&candidates).Error; err != nil {
			return attempted, fmt.Errorf("reconcile pending import candidates: %w", err)
		}
		if len(candidates) == 0 {
			service.importAfterID = ""
			break
		}
		for _, candidate := range candidates {
			afterID = candidate.ID
			service.importAfterID = afterID
			inspected++
			listing, ok := listings[candidate.RepositoryID]
			if !ok {
				if service.importListingSweepPending(candidate.RepositoryID) {
					listing = &repoListing{skip: true, sweepOnly: true}
					listings[candidate.RepositoryID] = listing
				} else {
					listing = &repoListing{current: map[string]normalizedImportCandidate{}}
					listingRequest := pageRequest
					listingRequest.Cursor = service.nextImportListingCursor(candidate.RepositoryID)
					runtime, normalized, nextCursor, listErr := service.collectNormalizedImportCandidates(ctx, candidate.RepositoryID, listingRequest, RequestContext{})
					if listErr != nil {
						listing.skip = true
						if listingErr == nil {
							listingErr = listErr
						}
					} else {
						listing.repository = runtime.repository
						listing.nextCursor = nextCursor
						listing.current, listing.complete = service.rememberImportListingPage(
							candidate.RepositoryID, listingRequest.Cursor, nextCursor, normalized,
						)
						service.rememberImportListingCursor(candidate.RepositoryID, nextCursor)
						if err := service.refreshPendingImportCandidatesOnPage(ctx, candidate.RepositoryID, listing.current); err != nil {
							return attempted, err
						}
					}
					listings[candidate.RepositoryID] = listing
				}
			}
			if listing.skip {
				if inspected >= limit {
					break
				}
				continue
			}
			attempted++
			_, _, validateErr := validateStoredImportCandidate(listing.repository, candidate)
			current, found := listing.current[candidate.SourceFingerprint]
			stale := validateErr != nil ||
				found && current.kind != backupasset.ImportCandidateKind(candidate.CandidateKind)
			if !stale && !found && listing.complete {
				startedAt, ok := service.importCycleStart(candidate.RepositoryID)
				stale = ok && !candidate.UpdatedAt.After(startedAt)
			}
			if stale {
				if err := service.db.WithContext(ctx).
					Where("id = ? AND review_state = ?", candidate.ID, backupasset.ImportReviewPending).
					Delete(&model.BackupRepositoryImportCandidate{}).Error; err != nil {
					return attempted, fmt.Errorf("drop stale pending import candidate: %w", err)
				}
			} else if found {
				if err := service.db.WithContext(ctx).Model(&model.BackupRepositoryImportCandidate{}).
					Where("id = ? AND review_state = ?", candidate.ID, backupasset.ImportReviewPending).
					Update("updated_at", service.importCycleRefreshTime(candidate.RepositoryID)).Error; err != nil {
					return attempted, fmt.Errorf("refresh pending import candidate: %w", err)
				}
			}
			if inspected >= limit {
				break
			}
		}
		if inspected >= limit {
			break
		}
		if len(candidates) < reconcilePageSize {
			service.importAfterID = ""
			break
		}
	}
	for repositoryID, listing := range listings {
		if listing != nil && listing.complete && !listing.skip && !listing.sweepOnly {
			done, _, sweepErr := service.sweepStalePendingForRepository(ctx, repositoryID, reconcilePageSize)
			if sweepErr != nil {
				return attempted, sweepErr
			}
			if done {
				service.finishImportListingCycle(repositoryID)
			}
		}
	}
	if listingErr != nil {
		return attempted, listingErr
	}
	return attempted, nil
}

func quarantineImportCandidate(
	providerKind backupasset.ProviderKind,
	repositoryID string,
	salt []byte,
	point provider.NativePoint,
	now time.Time,
) normalizedImportCandidate {
	kind := backupasset.ImportCandidateImportedBaseline
	switch point.Semantics {
	case backupasset.PointNativeSnapshot:
		kind = backupasset.ImportCandidateNativeSnapshot
	case backupasset.PointXirangManifest:
		kind = backupasset.ImportCandidateXirangManifest
	case backupasset.PointMutableHead:
		kind = backupasset.ImportCandidateMutableHead
	case backupasset.PointImportedBaseline:
		kind = backupasset.ImportCandidateImportedBaseline
	}
	opaque := point.OpaqueDigest
	if !isLowerHex64(opaque) {
		sum := sha256.Sum256([]byte(strings.TrimSpace(opaque) + "\x00" + point.Locator.Native + "\x00" + point.SourceRevision))
		opaque = hex.EncodeToString(sum[:])
	}
	native := strings.TrimSpace(point.Locator.Native)
	if native == "" || strings.ContainsAny(native, "\r\n\x00") {
		native = "quarantine:" + opaque
	}
	sourceRevision := strings.TrimSpace(point.SourceRevision)
	if sourceRevision == "" || strings.ContainsAny(sourceRevision, "\r\n\x00") {
		sourceRevision = opaque
	}
	captured := point.CapturedAt
	if captured.IsZero() || captured.Location() != time.UTC {
		captured = now.UTC()
	}
	locatorJSON, err := json.Marshal(importCandidateLocator{Version: 1, Native: native})
	if err != nil {
		locatorJSON = []byte(`{"version":1,"native":"quarantine"}`)
	}
	evidenceJSON, err := json.Marshal(importCandidateEvidence{
		Version: 1, Provider: providerKind, OpaqueDigest: opaque, CapturedAt: captured.UTC(),
		Semantics: point.Semantics, SourceRevision: sourceRevision, Quarantined: true,
	})
	if err != nil {
		evidenceJSON = []byte(`{"version":1,"quarantined":true}`)
	}
	mac := hmac.New(sha256.New, salt)
	for _, value := range []string{importQuarantineFingerprintDomain, repositoryID, string(providerKind), opaque, native, sourceRevision} {
		_, _ = mac.Write([]byte(value))
		_, _ = mac.Write([]byte{0})
	}
	return normalizedImportCandidate{
		kind: kind, fingerprint: hex.EncodeToString(mac.Sum(nil)),
		locator: string(locatorJSON), evidence: string(evidenceJSON),
	}
}

func failedImportSourceIdentity(repository model.BackupRepository) (string, error) {
	if repository.RepositoryIdentity == nil || strings.TrimSpace(*repository.RepositoryIdentity) == "" {
		return "", fmt.Errorf("%w: repository identity required for failed-source import", backupasset.ErrInvalidState)
	}
	sum := sha256.Sum256([]byte(importFailedSourceIdentityDomain + "\x00" + repository.ID + "\x00" + *repository.RepositoryIdentity))
	return hex.EncodeToString(sum[:]), nil
}

func parseFailedSourceIdentity(fidelityJSON string) string {
	var document importFidelityDocument
	if decodeStrictImportJSON(fidelityJSON, &document) != nil || document.Version != 1 {
		return ""
	}
	if !isLowerHex64(document.FailedSourceIdentity) {
		return ""
	}
	return document.FailedSourceIdentity
}

func rejectDuplicateFailedSourceBaseline(tx *gorm.DB, proposed model.RecoveryPoint) error {
	identity := parseFailedSourceIdentity(proposed.FidelityJSON)
	if identity == "" || proposed.Semantics != string(backupasset.PointImportedBaseline) {
		return nil
	}
	var existing []model.RecoveryPoint
	if err := tx.Select("id", "fidelity_json").
		Where("repository_id = ? AND semantics = ?", proposed.RepositoryID, backupasset.PointImportedBaseline).
		Find(&existing).Error; err != nil {
		return fmt.Errorf("query failed-source import baselines: %w", err)
	}
	for _, point := range existing {
		if parseFailedSourceIdentity(point.FidelityJSON) == identity {
			return fmt.Errorf("%w: failed source already has an imported baseline", backupasset.ErrConflict)
		}
	}
	return nil
}

func importCandidateView(row model.BackupRepositoryImportCandidate) ImportCandidateView {
	view := ImportCandidateView{
		ID: row.ID, RepositoryID: row.RepositoryID, Kind: backupasset.ImportCandidateKind(row.CandidateKind),
		State: backupasset.ImportReviewState(row.ReviewState), CreatedAt: row.CreatedAt, ReviewedAt: row.ReviewedAt,
	}
	if row.AcceptedRecoveryPointID != nil {
		view.AcceptedRecoveryPointID = *row.AcceptedRecoveryPointID
	}
	view.Quarantined = importCandidateEvidenceQuarantined(row.EncryptedEvidence)
	return view
}

func importCandidateEvidenceQuarantined(encryptedEvidence string) bool {
	plaintext, err := secure.DecryptIfNeeded(encryptedEvidence)
	if err != nil {
		return true
	}
	var evidence struct {
		Quarantined bool `json:"quarantined"`
	}
	if json.Unmarshal([]byte(plaintext), &evidence) != nil {
		return true
	}
	return evidence.Quarantined
}
