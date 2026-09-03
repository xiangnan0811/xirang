package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

type resticPointLocatorV1 struct {
	Version        int    `json:"version"`
	Provider       string `json:"provider"`
	FullSnapshotID string `json:"full_snapshot_id"`
}

type lineageBinding struct {
	link       model.TaskRepositoryLink
	repository model.BackupRepository
}

func (service *Service) Begin(ctx context.Context, taskID uint, operation publication.ResticOperation) (publication.LineageSession, error) {
	if service == nil || service.db == nil || service.foundation == nil || service.admission == nil || service.history == nil || service.metrics == nil || taskID == 0 {
		return nil, fmt.Errorf("%w: lineage guard dependencies are unavailable", backupasset.ErrInvalidState)
	}
	token, err := service.admission.Acquire(ctx, operation)
	if err != nil {
		return nil, err
	}
	keepToken := false
	defer func() {
		if !keepToken {
			_ = token.Close()
		}
	}()

	var taskEntity model.Task
	if err := service.db.WithContext(ctx).First(&taskEntity, taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: lineage Task", backupasset.ErrNotFound)
		}
		return nil, fmt.Errorf("load lineage Task: %w", err)
	}
	providerKind := bindingProviderForTask(taskEntity)
	if providerKind == backupasset.ProviderRsync || providerKind == backupasset.ProviderRclone {
		session, err := service.beginMutableLegacySession(ctx, taskEntity, operation, token)
		if err != nil {
			return nil, err
		}
		keepToken = true
		return session, nil
	}
	if providerKind != backupasset.ProviderRestic {
		return nil, fmt.Errorf("%w: lineage guard requires a Restic Task", backupasset.ErrInvalidState)
	}
	enabled, err := service.foundation.FeatureEnabled()
	if err != nil {
		return nil, err
	}
	binding, activeLinkPresent, err := service.loadLineageBinding(ctx, taskEntity.ID)
	if err != nil {
		return nil, err
	}
	if activeLinkPresent && binding == nil {
		return nil, service.blockLegacy(operation)
	}

	installationHistory, err := service.history.HasInstallationManagedHistory(ctx)
	if err != nil {
		return nil, err
	}
	activePublicationLease, err := service.history.HasActivePublicationLease(ctx)
	if err != nil {
		return nil, err
	}
	repositoryHistory := false
	if binding != nil {
		repositoryHistory, err = service.history.HasRepositoryManagedHistory(ctx, binding.repository.ID)
		if err != nil {
			return nil, err
		}
	}

	exact, err := service.chooseLineageMode(enabled, token.Mode(), binding != nil, repositoryHistory, installationHistory, activePublicationLease, operation)
	if err != nil {
		return nil, err
	}
	session := &lineageSession{service: service, token: token, mode: publication.LineageCompatibility}
	if !exact {
		keepToken = true
		return session, nil
	}
	if binding == nil {
		return nil, service.blockLegacy(operation)
	}
	points, err := service.loadCommittedLineagePoints(ctx, taskEntity.ID, binding.link, binding.repository)
	if err != nil {
		return nil, err
	}
	session.mode = publication.LineageExact
	session.taskID = taskEntity.ID
	session.repositoryID = binding.repository.ID
	session.repositoryCapabilityRevision = binding.repository.CapabilityRevision
	session.linkTag = "xirang.link.v1." + binding.link.ID
	session.points = points
	keepToken = true
	return session, nil
}

// beginMutableLegacySession permits only an independently proven mutable v1
// binding. A versioned link, durable managed-history fact, active publication
// lease, or binding ambiguity is a hard stop before any legacy path can touch
// its target.
func (service *Service) beginMutableLegacySession(ctx context.Context, taskEntity model.Task, operation publication.ResticOperation, token publication.AdmissionToken) (publication.LineageSession, error) {
	if token == nil || publication.ValidateAdmissionMode(token.Mode()) != nil {
		return nil, fmt.Errorf("%w: managed Rsync legacy admission is invalid", backupasset.ErrInvalidState)
	}
	allowed, err := service.history.legacyFallbackAllowed(ctx, taskEntity)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, service.blockLegacy(operation)
	}
	return &lineageSession{service: service, token: token, mode: publication.LineageCompatibility}, nil
}

func (service *Service) chooseLineageMode(enabled bool, tokenMode publication.AdmissionMode, hasBinding, repositoryHistory, installationHistory, activePublicationLease bool, operation publication.ResticOperation) (bool, error) {
	if err := publication.ValidateAdmissionMode(tokenMode); err != nil {
		return false, err
	}
	if enabled {
		if !hasBinding {
			return false, service.blockLegacy(operation)
		}
		return true, nil
	}
	switch tokenMode {
	case publication.AdmissionManaged, publication.AdmissionRollbackSafe:
		if !hasBinding {
			return false, service.blockLegacy(operation)
		}
		return true, nil
	case publication.AdmissionPristineLegacy:
		if hasBinding && (repositoryHistory || activePublicationLease) {
			return true, nil
		}
		if !hasBinding && (installationHistory || activePublicationLease) {
			return false, service.blockLegacy(operation)
		}
		return false, nil
	default:
		return false, fmt.Errorf("%w: unsupported admission mode", backupasset.ErrInvalidState)
	}
}

func (service *Service) loadLineageBinding(ctx context.Context, taskID uint) (*lineageBinding, bool, error) {
	var link model.TaskRepositoryLink
	err := service.db.WithContext(ctx).Where("task_id = ? AND unlinked_at IS NULL", taskID).Limit(1).Find(&link).Error
	if err != nil {
		return nil, false, fmt.Errorf("load active lineage link: %w", err)
	}
	if link.ID == "" {
		return nil, false, nil
	}
	if link.PublicationMode != string(backupasset.PublicationNativeSnapshot) || backupasset.ValidateOpaqueID(link.ID) != nil || backupasset.ValidateOpaqueID(link.RepositoryID) != nil {
		return nil, true, nil
	}
	var repository model.BackupRepository
	if err := service.db.WithContext(ctx).First(&repository, "id = ?", link.RepositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, true, nil
		}
		return nil, true, fmt.Errorf("load lineage repository: %w", err)
	}
	if repository.ProviderKind != string(backupasset.ProviderRestic) || repository.VersionMode != string(backupasset.VersionNativeSnapshot) {
		return nil, true, nil
	}
	return &lineageBinding{link: link, repository: repository}, true, nil
}

func (service *Service) loadCommittedLineagePoints(ctx context.Context, taskID uint, link model.TaskRepositoryLink, repository model.BackupRepository) ([]publication.CommittedPoint, error) {
	var records []model.RecoveryPoint
	if err := service.db.WithContext(ctx).
		Where("repository_id = ? AND semantics = ? AND state = ?", repository.ID, backupasset.PointNativeSnapshot, backupasset.RecoveryPointCommitted).
		Find(&records).Error; err != nil {
		return nil, fmt.Errorf("load committed lineage points: %w", err)
	}
	points := make([]publication.CommittedPoint, 0, len(records))
	for _, record := range records {
		lineage, err := backupasset.DecodePublicationLineage(record.LineageJSON)
		if err != nil {
			// A malformed foreign/native row cannot prove ownership for this Task;
			// omit it rather than allowing it into the exact set.
			continue
		}
		if lineage.TaskID != taskID || lineage.TaskRepositoryLinkID != link.ID {
			continue
		}
		if record.CapabilityRevision <= 0 || record.CapabilityRevision != repository.CapabilityRevision {
			return nil, fmt.Errorf("%w: committed lineage point capability revision changed", backupasset.ErrConflict)
		}
		if (record.ProducingTaskID != nil && *record.ProducingTaskID != lineage.TaskID) ||
			(record.ProducingTaskRunID != nil && *record.ProducingTaskRunID != lineage.TaskRunID) {
			return nil, fmt.Errorf("%w: live producing foreign key conflicts with immutable lineage", backupasset.ErrConflict)
		}
		locator, err := decodeResticPointLocator(record.EncryptedProviderLocator)
		if err != nil {
			return nil, err
		}
		if record.CapturedAt == nil || record.CapturedAt.IsZero() || backupasset.ValidateOpaqueID(record.ID) != nil {
			return nil, fmt.Errorf("%w: invalid committed lineage point", backupasset.ErrInvalidState)
		}
		points = append(points, publication.CommittedPoint{RecoveryPointID: record.ID, FullNativeID: locator.FullSnapshotID, CapturedAt: record.CapturedAt.UTC()})
	}
	sort.Slice(points, func(left, right int) bool {
		if points[left].CapturedAt.Equal(points[right].CapturedAt) {
			return points[left].RecoveryPointID > points[right].RecoveryPointID
		}
		return points[left].CapturedAt.After(points[right].CapturedAt)
	})
	return points, nil
}

func decodeResticPointLocator(raw string) (resticPointLocatorV1, error) {
	var locator resticPointLocatorV1
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&locator); err != nil {
		return resticPointLocatorV1{}, fmt.Errorf("%w: invalid encrypted Restic point locator", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return resticPointLocatorV1{}, fmt.Errorf("%w: trailing Restic point locator data", backupasset.ErrInvalidState)
	}
	if locator.Version != 1 || locator.Provider != string(backupasset.ProviderRestic) || !validFullNativeID(locator.FullSnapshotID) {
		return resticPointLocatorV1{}, fmt.Errorf("%w: invalid Restic point locator", backupasset.ErrInvalidState)
	}
	return locator, nil
}

func validFullNativeID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validNativePrefix(value string) bool {
	return len(value) >= 4 && len(value) <= 64 && validHex(value)
}

func validHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (service *Service) blockLegacy(operation publication.ResticOperation) error {
	if service.metrics != nil {
		service.metrics.ObserveLegacyBlocked(operation)
	}
	return fmt.Errorf("%w: %s", backupasset.ErrForbidden, backupasset.FailureLegacyFallbackBlocked)
}

type lineageSession struct {
	service                      *Service
	token                        publication.AdmissionToken
	mode                         publication.LineageMode
	taskID                       uint
	repositoryID                 string
	repositoryCapabilityRevision int
	linkTag                      string
	points                       []publication.CommittedPoint
	closeOnce                    sync.Once
}

func (session *lineageSession) Mode() publication.LineageMode { return session.mode }
func (session *lineageSession) RepositoryID() string          { return session.repositoryID }
func (session *lineageSession) LinkTag() string               { return session.linkTag }
func (session *lineageSession) CommittedPoints() []publication.CommittedPoint {
	return append([]publication.CommittedPoint(nil), session.points...)
}

func (session *lineageSession) ResolveNativeID(prefix string) (string, error) {
	if session == nil || session.mode != publication.LineageExact || !validNativePrefix(prefix) {
		return "", fmt.Errorf("%w: invalid exact snapshot reference", backupasset.ErrInvalidState)
	}
	match := ""
	for _, point := range session.points {
		if strings.HasPrefix(point.FullNativeID, prefix) {
			if match != "" {
				return "", fmt.Errorf("%w: ambiguous exact snapshot reference", backupasset.ErrConflict)
			}
			match = point.FullNativeID
		}
	}
	if match == "" {
		return "", fmt.Errorf("%w: committed exact snapshot", backupasset.ErrNotFound)
	}
	return match, nil
}

func (session *lineageSession) CurrentAndPrevious(currentFullNativeID string) (publication.CommittedPoint, *publication.CommittedPoint, error) {
	if session == nil || session.mode != publication.LineageExact || !validFullNativeID(currentFullNativeID) {
		return publication.CommittedPoint{}, nil, fmt.Errorf("%w: invalid exact snapshot reference", backupasset.ErrInvalidState)
	}
	for index, point := range session.points {
		if point.FullNativeID != currentFullNativeID {
			continue
		}
		if index+1 == len(session.points) {
			return point, nil, nil
		}
		previous := session.points[index+1]
		return point, &previous, nil
	}
	return publication.CommittedPoint{}, nil, fmt.Errorf("%w: committed exact snapshot", backupasset.ErrNotFound)
}

func (session *lineageSession) ListEntries(ctx context.Context, fullNativeID string, parent provider.EntryLocator, request provider.PageRequest) (provider.EntryPage, error) {
	if session == nil || session.mode != publication.LineageExact || session.service == nil {
		return provider.EntryPage{}, fmt.Errorf("%w: exact lineage session required", backupasset.ErrForbidden)
	}
	resolved, err := session.ResolveNativeID(fullNativeID)
	if err != nil {
		return provider.EntryPage{}, err
	}
	if resolved != fullNativeID {
		return provider.EntryPage{}, fmt.Errorf("%w: full exact snapshot ID required", backupasset.ErrInvalidState)
	}
	if err := session.service.requireRuntime(); err != nil {
		return provider.EntryPage{}, err
	}
	runtime, err := session.service.loadRepositoryRuntime(ctx, session.repositoryID)
	if err != nil {
		return provider.EntryPage{}, err
	}
	if runtime.repository.ProviderKind != string(backupasset.ProviderRestic) {
		return provider.EntryPage{}, fmt.Errorf("%w: exact Restic repository required", backupasset.ErrInvalidState)
	}
	if runtime.repository.CapabilityRevision != session.repositoryCapabilityRevision {
		return provider.EntryPage{}, fmt.Errorf("%w: exact Restic repository capability revision changed", backupasset.ErrConflict)
	}
	lister, err := session.service.registry.EntryLister(backupasset.ProviderRestic)
	if err != nil {
		return provider.EntryPage{}, err
	}
	return lister.ListEntries(ctx, provider.ReadSnapshot{
		RepositoryID:       session.repositoryID,
		CapabilityRevision: session.repositoryCapabilityRevision,
		SourceRevision:     runtime.document.AdapterRevision,
		Access:             runtime.access,
	}, provider.PointLocator{Native: fullNativeID}, parent, request)
}

func (session *lineageSession) Close() error {
	if session == nil || session.token == nil {
		return nil
	}
	var err error
	session.closeOnce.Do(func() { err = session.token.Close() })
	return err
}

var _ publication.LineageGuard = (*Service)(nil)
var _ publication.LineageSession = (*lineageSession)(nil)
