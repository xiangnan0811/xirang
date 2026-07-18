package overlay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AssetAuthorizer interface {
	AuthorizeAsset(context.Context, *gorm.DB, Actor, backupasset.AssetRef) error
}

type PointAuthorizer interface {
	AuthorizePoints(context.Context, Actor, []string) error
}

type KeySource interface {
	Active(context.Context, backupasset.KeyDomain) (backupasset.DomainKeyMaterial, error)
}

type AuditSink interface {
	Write(context.Context, backupasset.AuditEventInput) error
}

type Config struct {
	SavedSearchQuota       int64
	FavoriteQuota          int64
	TagDefinitionQuota     int64
	TagAssignmentQuota     int64
	RecentQuota            int64
	RecentWritesPerMinute  int64
	RecentTTL              time.Duration
	IdempotencyTTL         time.Duration
	MaxBulk                int
	LabelMaxBytes          int
	IdempotencyKeyMaxBytes int
	QueryLimits            assetsearch.QueryLimits
}

func DefaultConfig() Config {
	queryLimits := assetsearch.DefaultQueryLimits()
	queryLimits.MaxDepth = 8
	queryLimits.MaxNodes = 64
	queryLimits.MaxValueBytes = 1024
	queryLimits.MaxValueRunes = 1024
	queryLimits.MaxExecutionTime = 5 * time.Second
	return Config{
		SavedSearchQuota: 100, FavoriteQuota: 5000, TagDefinitionQuota: 100,
		TagAssignmentQuota: 10000, RecentQuota: 10000, RecentWritesPerMinute: 120,
		RecentTTL: 30 * 24 * time.Hour, IdempotencyTTL: 24 * time.Hour, MaxBulk: 200,
		LabelMaxBytes: 256, IdempotencyKeyMaxBytes: 128, QueryLimits: queryLimits,
	}
}

type ServiceDependencies struct {
	DB             *gorm.DB
	Keys           KeySource
	Assets         AssetAuthorizer
	Points         PointAuthorizer
	Audit          AuditSink
	Now            func() time.Time
	Config         Config
	FeatureEnabled func() (bool, error)
}

type Service struct {
	db             *gorm.DB
	keys           KeySource
	assets         AssetAuthorizer
	points         PointAuthorizer
	audit          AuditSink
	now            func() time.Time
	config         Config
	featureEnabled func() (bool, error)
}

type CreateSavedSearchRequest struct {
	Query          assetsearch.SearchRequest
	IdempotencyKey string
}

type AddFavoriteRequest struct {
	Ref            backupasset.AssetRef
	Label          string
	IdempotencyKey string
}

type UpdateSavedSearchRequest struct {
	Query           assetsearch.SearchRequest
	ExpectedVersion int
	IdempotencyKey  string
}

type UpdateTagRequest struct {
	Name            string
	ExpectedVersion int
	IdempotencyKey  string
}

func NewService(dependencies ServiceDependencies) (*Service, error) {
	config := dependencies.Config
	if dependencies.DB == nil || dependencies.Keys == nil || dependencies.Assets == nil || dependencies.Points == nil ||
		dependencies.FeatureEnabled == nil ||
		config.SavedSearchQuota <= 0 || config.FavoriteQuota <= 0 || config.TagDefinitionQuota <= 0 ||
		config.TagAssignmentQuota <= 0 || config.RecentQuota <= 0 || config.RecentWritesPerMinute <= 0 ||
		config.RecentTTL <= 0 || config.IdempotencyTTL <= 0 || config.MaxBulk <= 0 || config.LabelMaxBytes <= 0 ||
		config.IdempotencyKeyMaxBytes < 16 || config.QueryLimits.MaxBodyBytes <= 0 || config.QueryLimits.MaxDepth <= 0 ||
		config.QueryLimits.MaxNodes < config.QueryLimits.MaxDepth || config.QueryLimits.MaxValuesPerNode <= 0 ||
		config.QueryLimits.MaxValueBytes <= 0 || config.QueryLimits.MaxValueRunes <= 0 ||
		config.QueryLimits.MaxValueRunes > config.QueryLimits.MaxValueBytes || config.QueryLimits.MaxPageSize <= 0 ||
		config.QueryLimits.MaxCandidates < config.QueryLimits.MaxPageSize || config.QueryLimits.MaxExecutionTime <= 0 ||
		config.QueryLimits.MaxSuggestions < 0 {
		return nil, fmt.Errorf("%w: overlay service dependencies", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		db: dependencies.DB, keys: dependencies.Keys, assets: dependencies.Assets, points: dependencies.Points,
		audit: dependencies.Audit, now: dependencies.Now, config: config, featureEnabled: dependencies.FeatureEnabled,
	}, nil
}

func (service *Service) CreateSavedSearch(ctx context.Context, actor Actor, request CreateSavedSearchRequest) (SavedSearch, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return SavedSearch{}, err
	}
	if !validActor(actor) || strings.TrimSpace(request.Query.Cursor) != "" {
		return SavedSearch{}, ErrInvalidOverlay
	}
	canonical, err := assetsearch.ValidateAndCanonicalize(request.Query, service.overlayQueryLimits())
	if err != nil {
		return SavedSearch{}, ErrInvalidOverlay
	}
	if canonical.Request.Scope.Mode == assetsearch.SearchScopeExactPoints {
		if err := service.points.AuthorizePoints(ctx, actor, canonical.Request.Scope.RecoveryPointIDs); err != nil {
			return SavedSearch{}, err
		}
	}
	fingerprint := digestStrings("saved-search", string(canonical.JSON))
	var result SavedSearch
	err = service.mutation(ctx, func(tx *gorm.DB) error {
		replay, found, err := service.loadIdempotency(tx, actor.UserID, actionSavedSearchCreate, request.IdempotencyKey, fingerprint)
		if err != nil {
			return err
		}
		if found {
			result, err = loadSavedSearchTx(tx, actor.UserID, replay.ResultResourceID, service.overlayQueryLimits())
			return err
		}
		usage, err := service.lockUsage(tx, actor.UserID)
		if err != nil {
			return err
		}
		if usage.SavedSearchCount >= service.config.SavedSearchQuota {
			return ErrQuotaExceeded
		}
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		now := service.utcNow()
		row := model.BackupAssetSavedSearch{
			ID: id, OwnerUserID: actor.UserID, EncryptedAST: string(canonical.JSON),
			SchemaVersion: canonical.Request.SchemaVersion, ScopeMode: string(canonical.Request.Scope.Mode),
			Version: 1, State: string(SavedSearchActive), CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create saved search: %w", err)
		}
		for _, pointID := range canonical.Request.Scope.RecoveryPointIDs {
			if err := tx.Create(&model.BackupAssetSavedSearchScopePoint{SavedSearchID: id, RecoveryPointID: pointID}).Error; err != nil {
				return fmt.Errorf("create saved exact scope: %w", err)
			}
		}
		if err := service.updateUsage(tx, usage, map[string]any{"saved_search_count": usage.SavedSearchCount + 1}); err != nil {
			return err
		}
		if err := service.createIdempotency(tx, actor.UserID, actionSavedSearchCreate, request.IdempotencyKey, fingerprint, resourceSavedSearch, id, 1); err != nil {
			return err
		}
		result, err = savedSearchFromModel(row, canonical.Request)
		return err
	})
	return result, err
}

func (service *Service) GetSavedSearch(ctx context.Context, ownerID uint, id string) (SavedSearch, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return SavedSearch{}, err
	}
	if ownerID == 0 || backupasset.ValidateOpaqueID(id) != nil {
		return SavedSearch{}, backupasset.ErrNotFound
	}
	result, err := loadSavedSearchTx(service.db.WithContext(ctx), ownerID, id, service.overlayQueryLimits())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SavedSearch{}, backupasset.ErrNotFound
	}
	return result, err
}

func (service *Service) ListSavedSearches(ctx context.Context, ownerID uint, request OverlayListRequest) (SavedSearchPage, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return SavedSearchPage{}, err
	}
	limit, err := service.overlayListLimit(ownerID, request)
	if err != nil {
		return SavedSearchPage{}, err
	}
	query := service.db.WithContext(ctx).Where("owner_user_id = ?", ownerID)
	if request.Cursor != "" {
		query = query.Where("id > ?", request.Cursor)
	}
	var rows []model.BackupAssetSavedSearch
	if err := query.Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return SavedSearchPage{}, err
	}
	page := SavedSearchPage{Items: make([]SavedSearch, 0, min(len(rows), limit))}
	for _, row := range rows[:min(len(rows), limit)] {
		canonical, err := assetsearch.DecodeAndCanonicalize([]byte(row.EncryptedAST), service.overlayQueryLimits())
		if err != nil {
			return SavedSearchPage{}, ErrOverlayUnavailable
		}
		item, err := savedSearchFromModel(row, canonical.Request)
		if err != nil {
			return SavedSearchPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if len(rows) > limit {
		page.NextCursor = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

func (service *Service) UpdateSavedSearch(
	ctx context.Context,
	actor Actor,
	id string,
	request UpdateSavedSearchRequest,
) (SavedSearch, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return SavedSearch{}, err
	}
	if !validActor(actor) || backupasset.ValidateOpaqueID(id) != nil || request.ExpectedVersion <= 0 ||
		strings.TrimSpace(request.Query.Cursor) != "" {
		return SavedSearch{}, ErrInvalidOverlay
	}
	canonical, err := assetsearch.ValidateAndCanonicalize(request.Query, service.overlayQueryLimits())
	if err != nil {
		return SavedSearch{}, ErrInvalidOverlay
	}
	if canonical.Request.Scope.Mode == assetsearch.SearchScopeExactPoints {
		if err := service.points.AuthorizePoints(ctx, actor, canonical.Request.Scope.RecoveryPointIDs); err != nil {
			return SavedSearch{}, err
		}
	}
	fingerprint := digestStrings("saved-search-update", id, fmt.Sprint(request.ExpectedVersion), string(canonical.JSON))
	var result SavedSearch
	err = service.mutation(ctx, func(tx *gorm.DB) error {
		replay, found, err := service.loadIdempotency(tx, actor.UserID, actionSavedSearchUpdate, request.IdempotencyKey, fingerprint)
		if err != nil {
			return err
		}
		if found {
			result, err = loadSavedSearchTx(tx, actor.UserID, replay.ResultResourceID, service.overlayQueryLimits())
			return err
		}
		var row model.BackupAssetSavedSearch
		if err := tx.Where("id = ? AND owner_user_id = ?", id, actor.UserID).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return backupasset.ErrNotFound
		} else if err != nil {
			return err
		}
		if row.Version != request.ExpectedVersion {
			return backupasset.ErrConflict
		}
		row.EncryptedAST = string(canonical.JSON)
		row.SchemaVersion = canonical.Request.SchemaVersion
		row.ScopeMode = string(canonical.Request.Scope.Mode)
		row.Version++
		row.State = string(SavedSearchActive)
		row.StateReason = ""
		row.BrokenAt = nil
		row.UpdatedAt = service.utcNow()
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if err := tx.Where("saved_search_id = ?", row.ID).Delete(&model.BackupAssetSavedSearchScopePoint{}).Error; err != nil {
			return err
		}
		for _, pointID := range canonical.Request.Scope.RecoveryPointIDs {
			if err := tx.Create(&model.BackupAssetSavedSearchScopePoint{SavedSearchID: row.ID, RecoveryPointID: pointID}).Error; err != nil {
				return err
			}
		}
		if err := service.createIdempotency(tx, actor.UserID, actionSavedSearchUpdate, request.IdempotencyKey, fingerprint,
			resourceSavedSearch, row.ID, row.Version); err != nil {
			return err
		}
		result, err = savedSearchFromModel(row, canonical.Request)
		return err
	})
	return result, err
}

func (service *Service) UseSavedSearch(ctx context.Context, actor Actor, id string) (SavedSearch, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return SavedSearch{}, err
	}
	if !validActor(actor) || backupasset.ValidateOpaqueID(id) != nil {
		return SavedSearch{}, backupasset.ErrNotFound
	}
	saved, err := loadSavedSearchTx(service.db.WithContext(ctx), actor.UserID, id, service.overlayQueryLimits())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SavedSearch{}, backupasset.ErrNotFound
	}
	if err != nil {
		return SavedSearch{}, err
	}
	if saved.State != SavedSearchActive {
		return SavedSearch{}, ErrSavedSearchBroken
	}
	if saved.Query.Scope.Mode != assetsearch.SearchScopeExactPoints {
		return saved, nil
	}
	if err := service.points.AuthorizePoints(ctx, actor, saved.Query.Scope.RecoveryPointIDs); err == nil {
		return saved, nil
	} else if !errors.Is(err, backupasset.ErrForbidden) && !errors.Is(err, backupasset.ErrNotFound) {
		return SavedSearch{}, err
	}
	if err := service.markSavedSearchBroken(ctx, actor.UserID, id, SavedReasonScopeUnauthorized); err != nil {
		return SavedSearch{}, err
	}
	service.writeAudit(ctx, backupasset.AuditEventInput{
		Actor:     backupasset.AuditActor{UserID: actor.UserID, Role: actor.Role},
		Action:    backupasset.AuditActionSavedSearchBroken,
		Outcome:   backupasset.AuditOutcomeSuccess,
		ItemCount: 1,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldReasonCode: string(SavedReasonScopeUnauthorized),
		},
	})
	return SavedSearch{}, ErrSavedSearchBroken
}

func (service *Service) DeleteSavedSearch(ctx context.Context, ownerID uint, id string, expectedVersion int, idempotencyKey string) error {
	if err := service.ensureFeatureEnabled(); err != nil {
		return err
	}
	if ownerID == 0 || backupasset.ValidateOpaqueID(id) != nil || expectedVersion <= 0 {
		return ErrInvalidOverlay
	}
	fingerprint := digestStrings("saved-search-delete", id, fmt.Sprint(expectedVersion))
	return service.mutation(ctx, func(tx *gorm.DB) error {
		_, found, err := service.loadIdempotency(tx, ownerID, actionSavedSearchDelete, idempotencyKey, fingerprint)
		if err != nil || found {
			return err
		}
		var row model.BackupAssetSavedSearch
		if err := tx.Where("id = ? AND owner_user_id = ?", id, ownerID).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return backupasset.ErrNotFound
		} else if err != nil {
			return err
		}
		if row.Version != expectedVersion {
			return backupasset.ErrConflict
		}
		usage, err := service.lockUsage(tx, ownerID)
		if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		if err := service.updateUsage(tx, usage, map[string]any{"saved_search_count": max(usage.SavedSearchCount-1, 0)}); err != nil {
			return err
		}
		return service.createIdempotency(tx, ownerID, actionSavedSearchDelete, idempotencyKey, fingerprint, resourceNone, "", 0)
	})
}

func (service *Service) AddFavorite(ctx context.Context, actor Actor, request AddFavoriteRequest) (Favorite, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return Favorite{}, err
	}
	if !validActor(actor) || ValidateOverlayRef(request.Ref) != nil || !validUserText(request.Label, service.config.LabelMaxBytes) {
		return Favorite{}, ErrInvalidOverlay
	}
	fingerprint := digestStrings("favorite", request.Ref.RecoveryPointID, request.Ref.EntryID, request.Label)
	var result Favorite
	err := service.mutation(ctx, func(tx *gorm.DB) error {
		if err := service.assets.AuthorizeAsset(ctx, tx, actor, request.Ref); err != nil {
			return err
		}
		replay, found, err := service.loadIdempotency(tx, actor.UserID, actionFavoriteAdd, request.IdempotencyKey, fingerprint)
		if err != nil {
			return err
		}
		if found {
			result, err = loadFavoriteByID(tx, actor.UserID, replay.ResultResourceID)
			return err
		}
		var existing model.BackupAssetFavorite
		err = tx.Where("owner_user_id = ? AND recovery_point_id = ? AND entry_id = ?", actor.UserID, request.Ref.RecoveryPointID, request.Ref.EntryID).Take(&existing).Error
		if err == nil {
			result, err = favoriteFromModel(existing)
			if err != nil {
				return err
			}
			return service.createIdempotency(tx, actor.UserID, actionFavoriteAdd, request.IdempotencyKey, fingerprint, resourceFavorite, existing.ID, existing.Version)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		usage, err := service.lockUsage(tx, actor.UserID)
		if err != nil {
			return err
		}
		if usage.FavoriteCount >= service.config.FavoriteQuota {
			return ErrQuotaExceeded
		}
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		now := service.utcNow()
		row := model.BackupAssetFavorite{
			ID: id, OwnerUserID: actor.UserID, RecoveryPointID: request.Ref.RecoveryPointID, EntryID: request.Ref.EntryID,
			EncryptedLabel: request.Label, State: string(OverlayActive), Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		row.EncryptedLabel = request.Label
		if err := service.updateUsage(tx, usage, map[string]any{"favorite_count": usage.FavoriteCount + 1}); err != nil {
			return err
		}
		if err := service.createIdempotency(tx, actor.UserID, actionFavoriteAdd, request.IdempotencyKey, fingerprint, resourceFavorite, id, 1); err != nil {
			return err
		}
		result, err = favoriteFromModel(row)
		return err
	})
	return result, err
}

func (service *Service) AddFavorites(ctx context.Context, actor Actor, requests []AddFavoriteRequest) ([]Favorite, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return nil, err
	}
	if !validActor(actor) || len(requests) == 0 || len(requests) > service.config.MaxBulk {
		return nil, ErrInvalidOverlay
	}
	unique := make([]AddFavoriteRequest, 0, len(requests))
	seen := make(map[backupasset.AssetRef]bool)
	for _, request := range requests {
		if ValidateOverlayRef(request.Ref) != nil || !validUserText(request.Label, service.config.LabelMaxBytes) {
			return nil, ErrInvalidOverlay
		}
		if !seen[request.Ref] {
			seen[request.Ref] = true
			unique = append(unique, request)
		}
	}
	var result []Favorite
	err := service.mutation(ctx, func(tx *gorm.DB) error {
		for _, request := range unique {
			if err := service.assets.AuthorizeAsset(ctx, tx, actor, request.Ref); err != nil {
				return err
			}
		}
		usage, err := service.lockUsage(tx, actor.UserID)
		if err != nil {
			return err
		}
		newCount := int64(0)
		for _, request := range unique {
			var count int64
			if err := tx.Model(&model.BackupAssetFavorite{}).
				Where("owner_user_id = ? AND recovery_point_id = ? AND entry_id = ?", actor.UserID, request.Ref.RecoveryPointID, request.Ref.EntryID).
				Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				newCount++
			}
		}
		if usage.FavoriteCount+newCount > service.config.FavoriteQuota {
			return ErrQuotaExceeded
		}
		for _, request := range unique {
			var row model.BackupAssetFavorite
			err := tx.Where("owner_user_id = ? AND recovery_point_id = ? AND entry_id = ?", actor.UserID, request.Ref.RecoveryPointID, request.Ref.EntryID).Take(&row).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				id, idErr := backupasset.NewOpaqueID()
				if idErr != nil {
					return idErr
				}
				now := service.utcNow()
				row = model.BackupAssetFavorite{ID: id, OwnerUserID: actor.UserID, RecoveryPointID: request.Ref.RecoveryPointID,
					EntryID: request.Ref.EntryID, EncryptedLabel: request.Label, State: string(OverlayActive), Version: 1, CreatedAt: now, UpdatedAt: now}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
				row.EncryptedLabel = request.Label
			} else if err != nil {
				return err
			}
			item, err := favoriteFromModel(row)
			if err != nil {
				return err
			}
			result = append(result, item)
		}
		return service.updateUsage(tx, usage, map[string]any{"favorite_count": usage.FavoriteCount + newCount})
	})
	return result, err
}

func (service *Service) ListFavorites(ctx context.Context, ownerID uint, request OverlayListRequest) (FavoritePage, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return FavoritePage{}, err
	}
	limit, err := service.overlayListLimit(ownerID, request)
	if err != nil {
		return FavoritePage{}, err
	}
	query := service.db.WithContext(ctx).Where("owner_user_id = ?", ownerID)
	if request.Cursor != "" {
		query = query.Where("id > ?", request.Cursor)
	}
	var rows []model.BackupAssetFavorite
	if err := query.Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return FavoritePage{}, err
	}
	page := FavoritePage{Items: make([]Favorite, 0, min(len(rows), limit))}
	for _, row := range rows[:min(len(rows), limit)] {
		item, err := favoriteFromModel(row)
		if err != nil {
			return FavoritePage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if len(rows) > limit {
		page.NextCursor = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

func (service *Service) RemoveFavorite(ctx context.Context, ownerID uint, ref backupasset.AssetRef, idempotencyKey string) error {
	if err := service.ensureFeatureEnabled(); err != nil {
		return err
	}
	if ownerID == 0 || ValidateOverlayRef(ref) != nil {
		return ErrInvalidOverlay
	}
	fingerprint := digestStrings("favorite-remove", ref.RecoveryPointID, ref.EntryID)
	return service.mutation(ctx, func(tx *gorm.DB) error {
		_, found, err := service.loadIdempotency(tx, ownerID, actionFavoriteRemove, idempotencyKey, fingerprint)
		if err != nil || found {
			return err
		}
		var row model.BackupAssetFavorite
		err = tx.Where("owner_user_id = ? AND recovery_point_id = ? AND entry_id = ?", ownerID, ref.RecoveryPointID, ref.EntryID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return service.createIdempotency(tx, ownerID, actionFavoriteRemove, idempotencyKey, fingerprint, resourceNone, "", 0)
		}
		if err != nil {
			return err
		}
		usage, err := service.lockUsage(tx, ownerID)
		if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		if err := service.updateUsage(tx, usage, map[string]any{"favorite_count": max(usage.FavoriteCount-1, 0)}); err != nil {
			return err
		}
		return service.createIdempotency(tx, ownerID, actionFavoriteRemove, idempotencyKey, fingerprint, resourceNone, "", 0)
	})
}

func (service *Service) CreateTag(ctx context.Context, ownerID uint, name, idempotencyKey string) (Tag, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return Tag{}, err
	}
	if ownerID == 0 || !validUserText(name, service.config.LabelMaxBytes) || strings.TrimSpace(name) == "" {
		return Tag{}, ErrInvalidOverlay
	}
	key, canonical, token, err := service.tagToken(ctx, name)
	if err != nil {
		return Tag{}, err
	}
	if err := service.ensureTagAvailable(ctx, key.Version); err != nil {
		return Tag{}, err
	}
	fingerprint := digestStrings("tag", canonical)
	var result Tag
	err = service.mutation(ctx, func(tx *gorm.DB) error {
		replay, found, err := service.loadIdempotency(tx, ownerID, actionTagCreate, idempotencyKey, fingerprint)
		if err != nil {
			return err
		}
		if found {
			result, err = loadTagByID(tx, ownerID, replay.ResultResourceID)
			return err
		}
		var existing model.BackupAssetTagDefinition
		err = tx.Where("owner_user_id = ? AND name_token = ?", ownerID, token).Take(&existing).Error
		if err == nil {
			result, err = tagFromModel(existing)
			if err != nil {
				return err
			}
			return service.createIdempotency(tx, ownerID, actionTagCreate, idempotencyKey, fingerprint, resourceTagDefinition, existing.ID, existing.Version)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		usage, err := service.lockUsage(tx, ownerID)
		if err != nil {
			return err
		}
		if usage.TagDefinitionCount >= service.config.TagDefinitionQuota {
			return ErrQuotaExceeded
		}
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		now := service.utcNow()
		row := model.BackupAssetTagDefinition{ID: id, OwnerUserID: ownerID, EncryptedName: canonical, NameToken: token,
			KeyVersion: key.Version, TokenState: "active", Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		row.EncryptedName = canonical
		if err := service.updateUsage(tx, usage, map[string]any{"tag_definition_count": usage.TagDefinitionCount + 1}); err != nil {
			return err
		}
		if err := service.createIdempotency(tx, ownerID, actionTagCreate, idempotencyKey, fingerprint, resourceTagDefinition, id, 1); err != nil {
			return err
		}
		result, err = tagFromModel(row)
		return err
	})
	return result, err
}

func (service *Service) ListTags(ctx context.Context, ownerID uint, request OverlayListRequest) (TagPage, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return TagPage{}, err
	}
	limit, err := service.overlayListLimit(ownerID, request)
	if err != nil {
		return TagPage{}, err
	}
	query := service.db.WithContext(ctx).Where("owner_user_id = ?", ownerID)
	if request.Cursor != "" {
		query = query.Where("id > ?", request.Cursor)
	}
	var rows []model.BackupAssetTagDefinition
	if err := query.Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return TagPage{}, err
	}
	page := TagPage{Items: make([]Tag, 0, min(len(rows), limit))}
	for _, row := range rows[:min(len(rows), limit)] {
		item, err := tagFromModel(row)
		if err != nil {
			return TagPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if len(rows) > limit {
		page.NextCursor = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

func (service *Service) UpdateTag(ctx context.Context, ownerID uint, id string, request UpdateTagRequest) (Tag, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return Tag{}, err
	}
	if ownerID == 0 || backupasset.ValidateOpaqueID(id) != nil || request.ExpectedVersion <= 0 ||
		!validUserText(request.Name, service.config.LabelMaxBytes) || strings.TrimSpace(request.Name) == "" {
		return Tag{}, ErrInvalidOverlay
	}
	key, canonical, token, err := service.tagToken(ctx, request.Name)
	if err != nil {
		return Tag{}, err
	}
	if err := service.ensureTagAvailable(ctx, key.Version); err != nil {
		return Tag{}, err
	}
	fingerprint := digestStrings("tag-update", id, fmt.Sprint(request.ExpectedVersion), canonical)
	var result Tag
	err = service.mutation(ctx, func(tx *gorm.DB) error {
		replay, found, err := service.loadIdempotency(tx, ownerID, actionTagUpdate, request.IdempotencyKey, fingerprint)
		if err != nil {
			return err
		}
		if found {
			result, err = loadTagByID(tx, ownerID, replay.ResultResourceID)
			return err
		}
		var row model.BackupAssetTagDefinition
		if err := tx.Where("id = ? AND owner_user_id = ?", id, ownerID).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return backupasset.ErrNotFound
		} else if err != nil {
			return err
		}
		if row.Version != request.ExpectedVersion {
			return backupasset.ErrConflict
		}
		var duplicateCount int64
		if err := tx.Model(&model.BackupAssetTagDefinition{}).
			Where("owner_user_id = ? AND name_token = ? AND id <> ?", ownerID, token, id).Count(&duplicateCount).Error; err != nil {
			return err
		}
		if duplicateCount > 0 {
			return backupasset.ErrConflict
		}
		row.EncryptedName = canonical
		row.NameToken = token
		row.KeyVersion = key.Version
		row.TokenState = "active"
		row.Version++
		row.UpdatedAt = service.utcNow()
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		row.EncryptedName = canonical
		if err := service.createIdempotency(tx, ownerID, actionTagUpdate, request.IdempotencyKey, fingerprint,
			resourceTagDefinition, row.ID, row.Version); err != nil {
			return err
		}
		result, err = tagFromModel(row)
		return err
	})
	return result, err
}

func (service *Service) DeleteTag(ctx context.Context, ownerID uint, id string, expectedVersion int, idempotencyKey string) error {
	if err := service.ensureFeatureEnabled(); err != nil {
		return err
	}
	if ownerID == 0 || backupasset.ValidateOpaqueID(id) != nil || expectedVersion <= 0 {
		return ErrInvalidOverlay
	}
	fingerprint := digestStrings("tag-delete", id, fmt.Sprint(expectedVersion))
	return service.mutation(ctx, func(tx *gorm.DB) error {
		_, found, err := service.loadIdempotency(tx, ownerID, actionTagDelete, idempotencyKey, fingerprint)
		if err != nil || found {
			return err
		}
		var row model.BackupAssetTagDefinition
		if err := tx.Where("id = ? AND owner_user_id = ?", id, ownerID).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return backupasset.ErrNotFound
		} else if err != nil {
			return err
		}
		if row.Version != expectedVersion {
			return backupasset.ErrConflict
		}
		var assignmentCount int64
		if err := tx.Model(&model.BackupAssetTagAssignment{}).
			Where("owner_user_id = ? AND tag_id = ?", ownerID, id).Count(&assignmentCount).Error; err != nil {
			return err
		}
		usage, err := service.lockUsage(tx, ownerID)
		if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		if err := service.updateUsage(tx, usage, map[string]any{
			"tag_definition_count": max(usage.TagDefinitionCount-1, 0),
			"tag_assignment_count": max(usage.TagAssignmentCount-assignmentCount, 0),
		}); err != nil {
			return err
		}
		return service.createIdempotency(tx, ownerID, actionTagDelete, idempotencyKey, fingerprint, resourceNone, "", 0)
	})
}

func (service *Service) AssignTag(ctx context.Context, actor Actor, tagID string, ref backupasset.AssetRef, idempotencyKey string) (TagAssignment, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return TagAssignment{}, err
	}
	if !validActor(actor) || backupasset.ValidateOpaqueID(tagID) != nil || ValidateOverlayRef(ref) != nil {
		return TagAssignment{}, ErrInvalidOverlay
	}
	key, err := service.activeSearchKey(ctx)
	if err != nil {
		return TagAssignment{}, err
	}
	if err := service.ensureTagAvailable(ctx, key.Version); err != nil {
		return TagAssignment{}, err
	}
	fingerprint := digestStrings("tag-assignment", tagID, ref.RecoveryPointID, ref.EntryID)
	var result TagAssignment
	err = service.mutation(ctx, func(tx *gorm.DB) error {
		if err := service.assets.AuthorizeAsset(ctx, tx, actor, ref); err != nil {
			return err
		}
		replay, found, err := service.loadIdempotency(tx, actor.UserID, actionTagAssign, idempotencyKey, fingerprint)
		if err != nil {
			return err
		}
		if found {
			result, err = loadAssignmentByID(tx, actor.UserID, replay.ResultResourceID)
			return err
		}
		var tag model.BackupAssetTagDefinition
		if err := tx.Where("id = ? AND owner_user_id = ? AND token_state = ? AND key_version = ?", tagID, actor.UserID, "active", key.Version).Take(&tag).Error; err != nil {
			return backupasset.ErrNotFound
		}
		var existing model.BackupAssetTagAssignment
		err = tx.Where("owner_user_id = ? AND tag_id = ? AND recovery_point_id = ? AND entry_id = ?",
			actor.UserID, tagID, ref.RecoveryPointID, ref.EntryID).Take(&existing).Error
		if err == nil {
			result, err = assignmentFromModel(existing)
			if err != nil {
				return err
			}
			return service.createIdempotency(tx, actor.UserID, actionTagAssign, idempotencyKey, fingerprint, resourceTagAssignment, existing.ID, existing.Version)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		usage, err := service.lockUsage(tx, actor.UserID)
		if err != nil {
			return err
		}
		if usage.TagAssignmentCount >= service.config.TagAssignmentQuota {
			return ErrQuotaExceeded
		}
		id, err := backupasset.NewOpaqueID()
		if err != nil {
			return err
		}
		now := service.utcNow()
		row := model.BackupAssetTagAssignment{ID: id, OwnerUserID: actor.UserID, TagID: tagID, RecoveryPointID: ref.RecoveryPointID,
			EntryID: ref.EntryID, State: string(OverlayActive), Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := service.updateUsage(tx, usage, map[string]any{"tag_assignment_count": usage.TagAssignmentCount + 1}); err != nil {
			return err
		}
		if err := service.createIdempotency(tx, actor.UserID, actionTagAssign, idempotencyKey, fingerprint, resourceTagAssignment, id, 1); err != nil {
			return err
		}
		result, err = assignmentFromModel(row)
		return err
	})
	return result, err
}

func (service *Service) UnassignTag(
	ctx context.Context,
	ownerID uint,
	tagID string,
	ref backupasset.AssetRef,
	idempotencyKey string,
) error {
	if err := service.ensureFeatureEnabled(); err != nil {
		return err
	}
	if ownerID == 0 || backupasset.ValidateOpaqueID(tagID) != nil || ValidateOverlayRef(ref) != nil {
		return ErrInvalidOverlay
	}
	fingerprint := digestStrings("tag-unassign", tagID, ref.RecoveryPointID, ref.EntryID)
	return service.mutation(ctx, func(tx *gorm.DB) error {
		_, found, err := service.loadIdempotency(tx, ownerID, actionTagUnassign, idempotencyKey, fingerprint)
		if err != nil || found {
			return err
		}
		var tagCount int64
		if err := tx.Model(&model.BackupAssetTagDefinition{}).
			Where("id = ? AND owner_user_id = ?", tagID, ownerID).Count(&tagCount).Error; err != nil {
			return err
		}
		if tagCount != 1 {
			return backupasset.ErrNotFound
		}
		var row model.BackupAssetTagAssignment
		err = tx.Where("owner_user_id = ? AND tag_id = ? AND recovery_point_id = ? AND entry_id = ?",
			ownerID, tagID, ref.RecoveryPointID, ref.EntryID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return service.createIdempotency(tx, ownerID, actionTagUnassign, idempotencyKey, fingerprint, resourceNone, "", 0)
		}
		if err != nil {
			return err
		}
		usage, err := service.lockUsage(tx, ownerID)
		if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		if err := service.updateUsage(tx, usage, map[string]any{
			"tag_assignment_count": max(usage.TagAssignmentCount-1, 0),
		}); err != nil {
			return err
		}
		return service.createIdempotency(tx, ownerID, actionTagUnassign, idempotencyKey, fingerprint, resourceNone, "", 0)
	})
}

func (service *Service) Matches(ctx context.Context, ownerID uint, ref backupasset.AssetRef, name string) (bool, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return false, err
	}
	if ownerID == 0 || ValidateOverlayRef(ref) != nil {
		return false, ErrInvalidOverlay
	}
	key, _, token, err := service.tagToken(ctx, name)
	if err != nil {
		return false, err
	}
	if err := service.ensureTagAvailable(ctx, key.Version); err != nil {
		return false, err
	}
	var count int64
	err = service.db.WithContext(ctx).Table("backup_asset_tag_assignments AS assignments").
		Joins("JOIN backup_asset_tag_definitions AS tags ON tags.id = assignments.tag_id AND tags.owner_user_id = assignments.owner_user_id").
		Where(`assignments.owner_user_id = ? AND assignments.recovery_point_id = ? AND assignments.entry_id = ?
			AND assignments.state = ? AND tags.name_token = ? AND tags.key_version = ? AND tags.token_state = ?`,
			ownerID, ref.RecoveryPointID, ref.EntryID, OverlayActive, token, key.Version, "active").Count(&count).Error
	return count > 0, err
}

func (service *Service) CandidateRefs(
	ctx context.Context,
	ownerID uint,
	name string,
	pointIDs []string,
	limit int,
) ([]backupasset.AssetRef, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return nil, err
	}
	if ownerID == 0 || limit <= 0 || len(pointIDs) > service.config.QueryLimits.MaxCandidates {
		return nil, ErrInvalidOverlay
	}
	if len(pointIDs) == 0 {
		return []backupasset.AssetRef{}, nil
	}
	uniquePoints := make([]string, 0, len(pointIDs))
	seenPoints := make(map[string]struct{}, len(pointIDs))
	for _, pointID := range pointIDs {
		if backupasset.ValidateOpaqueID(pointID) != nil {
			return nil, ErrInvalidOverlay
		}
		if _, exists := seenPoints[pointID]; !exists {
			seenPoints[pointID] = struct{}{}
			uniquePoints = append(uniquePoints, pointID)
		}
	}
	sort.Strings(uniquePoints)
	key, _, token, err := service.tagToken(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := service.ensureTagAvailable(ctx, key.Version); err != nil {
		return nil, err
	}
	type candidateRef struct {
		RecoveryPointID string
		EntryID         string
	}
	result := make([]backupasset.AssetRef, 0)
	for start := 0; start < len(uniquePoints) && len(result) <= limit; start += 400 {
		end := min(start+400, len(uniquePoints))
		var rows []candidateRef
		if err := service.db.WithContext(ctx).Table("backup_asset_tag_assignments AS assignments").
			Select("assignments.recovery_point_id, assignments.entry_id").
			Joins("JOIN backup_asset_tag_definitions AS tags ON tags.id = assignments.tag_id AND tags.owner_user_id = assignments.owner_user_id").
			Where(`assignments.owner_user_id = ? AND assignments.recovery_point_id IN ? AND assignments.state = ?
				AND tags.name_token = ? AND tags.key_version = ? AND tags.token_state = ?`,
				ownerID, uniquePoints[start:end], OverlayActive, token, key.Version, "active").
			Order("assignments.recovery_point_id ASC, assignments.entry_id ASC").
			Limit(limit + 1 - len(result)).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			ref := backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}
			if ValidateOverlayRef(ref) != nil {
				return nil, ErrOverlayUnavailable
			}
			result = append(result, ref)
		}
	}
	if len(result) > limit {
		return nil, assetsearch.ErrResourceLimit
	}
	return result, nil
}

func (service *Service) Revision(ctx context.Context, ownerID uint) (string, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return "", err
	}
	if ownerID == 0 {
		return "", ErrInvalidOverlay
	}
	key, err := service.activeSearchKey(ctx)
	if err != nil {
		return "", err
	}
	if err := service.ensureTagAvailable(ctx, key.Version); err != nil {
		return "", err
	}
	type definitionRevision struct {
		ID         string `json:"id"`
		NameToken  string `json:"name_token"`
		KeyVersion int    `json:"key_version"`
		TokenState string `json:"token_state"`
		Version    int    `json:"version"`
	}
	type assignmentRevision struct {
		ID              string `json:"id"`
		TagID           string `json:"tag_id"`
		RecoveryPointID string `json:"recovery_point_id"`
		EntryID         string `json:"entry_id"`
		State           string `json:"state"`
		TombstoneReason string `json:"tombstone_reason"`
		Version         int    `json:"version"`
	}
	var definitions []definitionRevision
	if err := service.db.WithContext(ctx).Table("backup_asset_tag_definitions").
		Select("id, name_token, key_version, token_state, version").
		Where("owner_user_id = ?", ownerID).Order("id ASC").Scan(&definitions).Error; err != nil {
		return "", err
	}
	var assignments []assignmentRevision
	if err := service.db.WithContext(ctx).Table("backup_asset_tag_assignments").
		Select("id, tag_id, recovery_point_id, entry_id, state, tombstone_reason, version").
		Where("owner_user_id = ?", ownerID).Order("id ASC").Scan(&assignments).Error; err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Definitions []definitionRevision `json:"definitions"`
		Assignments []assignmentRevision `json:"assignments"`
	}{Definitions: definitions, Assignments: assignments})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("xirang/search/owner-tags/v1\x00"), payload...))
	return hex.EncodeToString(digest[:]), nil
}

func (service *Service) RecordRecent(ctx context.Context, actor Actor, ref backupasset.AssetRef) (RecentAccess, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return RecentAccess{}, err
	}
	if !validActor(actor) || ValidateOverlayRef(ref) != nil {
		return RecentAccess{}, ErrInvalidOverlay
	}
	var result RecentAccess
	err := service.mutation(ctx, func(tx *gorm.DB) error {
		if err := service.assets.AuthorizeAsset(ctx, tx, actor, ref); err != nil {
			return err
		}
		usage, err := service.lockUsage(tx, actor.UserID)
		if err != nil {
			return err
		}
		now := service.utcNow()
		windowStart := usage.RecentRateWindowStartedAt.UTC()
		windowWrites := usage.RecentRateWindowWriteCount
		if windowStart.IsZero() || !now.Before(windowStart.Add(time.Minute)) {
			windowStart = now
			windowWrites = 0
		}
		if windowWrites >= service.config.RecentWritesPerMinute {
			return ErrRateLimited
		}
		var row model.BackupAssetRecentAccess
		err = tx.Where("owner_user_id = ? AND recovery_point_id = ? AND entry_id = ?", actor.UserID, ref.RecoveryPointID, ref.EntryID).Take(&row).Error
		newCount := int64(0)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if usage.RecentCount >= service.config.RecentQuota {
				return ErrQuotaExceeded
			}
			id, idErr := backupasset.NewOpaqueID()
			if idErr != nil {
				return idErr
			}
			row = model.BackupAssetRecentAccess{ID: id, OwnerUserID: actor.UserID, RecoveryPointID: ref.RecoveryPointID,
				EntryID: ref.EntryID, AccessCount: 1, LastAccessedAt: now, ExpiresAt: now.Add(service.config.RecentTTL),
				Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			newCount = 1
		} else if err != nil {
			return err
		} else {
			row.AccessCount++
			row.LastAccessedAt = now
			row.ExpiresAt = now.Add(service.config.RecentTTL)
			row.Version++
			row.UpdatedAt = now
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		}
		if err := service.updateUsage(tx, usage, map[string]any{
			"recent_count": usage.RecentCount + newCount, "recent_rate_window_started_at": windowStart,
			"recent_rate_window_write_count": windowWrites + 1,
		}); err != nil {
			return err
		}
		result, err = recentFromModel(row)
		return err
	})
	if err == nil {
		service.writeAudit(ctx, backupasset.AuditEventInput{
			Actor: backupasset.AuditActor{UserID: actor.UserID, Role: actor.Role}, Action: backupasset.AuditActionRecentRecord,
			Outcome: backupasset.AuditOutcomeSuccess, RecoveryPointID: ref.RecoveryPointID, EntryID: ref.EntryID, ItemCount: 1,
		})
	}
	return result, err
}

func (service *Service) ListRecent(ctx context.Context, ownerID uint, request OverlayListRequest) (RecentAccessPage, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return RecentAccessPage{}, err
	}
	limit, err := service.overlayListLimit(ownerID, request)
	if err != nil {
		return RecentAccessPage{}, err
	}
	query := service.db.WithContext(ctx).Where("owner_user_id = ? AND expires_at > ?", ownerID, service.utcNow())
	if request.Cursor != "" {
		query = query.Where("id > ?", request.Cursor)
	}
	var rows []model.BackupAssetRecentAccess
	if err := query.Order("id ASC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return RecentAccessPage{}, err
	}
	page := RecentAccessPage{Items: make([]RecentAccess, 0, min(len(rows), limit))}
	for _, row := range rows[:min(len(rows), limit)] {
		item, err := recentFromModel(row)
		if err != nil {
			return RecentAccessPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if len(rows) > limit {
		page.NextCursor = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

func (service *Service) ClearRecent(ctx context.Context, ownerID uint, idempotencyKey string) (int64, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return 0, err
	}
	if ownerID == 0 {
		return 0, ErrInvalidOverlay
	}
	fingerprint := digestStrings("recent-clear")
	var cleared int64
	err := service.mutation(ctx, func(tx *gorm.DB) error {
		replay, found, err := service.loadIdempotency(tx, ownerID, actionRecentClear, idempotencyKey, fingerprint)
		if err != nil {
			return err
		}
		if found {
			cleared = int64(replay.ResultVersion)
			return nil
		}
		usage, err := service.lockUsage(tx, ownerID)
		if err != nil {
			return err
		}
		deleted := tx.Where("owner_user_id = ?", ownerID).Delete(&model.BackupAssetRecentAccess{})
		if deleted.Error != nil {
			return deleted.Error
		}
		cleared = deleted.RowsAffected
		if err := service.updateUsage(tx, usage, map[string]any{"recent_count": max(usage.RecentCount-cleared, 0)}); err != nil {
			return err
		}
		return service.createIdempotency(tx, ownerID, actionRecentClear, idempotencyKey, fingerprint,
			resourceRecent, "", int(cleared))
	})
	return cleared, err
}

func (service *Service) InvalidateSearchKey(ctx context.Context, tx *gorm.DB, transition backupasset.RebuildableKeyTransition) error {
	if service == nil || tx == nil || transition.Domain != backupasset.KeyDomainSearchToken || transition.PreviousVersion <= 0 {
		return ErrInvalidOverlay
	}
	now := service.utcNow()
	if tx.Migrator().HasTable(&model.BackupAssetSearchGeneration{}) {
		if err := tx.Model(&model.BackupAssetSearchGeneration{}).Where("is_active = ?", true).
			Updates(map[string]any{"is_active": false, "state": assetsearch.SearchGenerationSuperseded, "updated_at": now}).Error; err != nil {
			return err
		}
	}
	return tx.Model(&model.BackupAssetTagDefinition{}).
		Where("key_version = ? AND token_state = ?", transition.PreviousVersion, "active").
		Updates(map[string]any{"token_state": "rekeying", "updated_at": now}).Error
}

func (service *Service) ReconcileTagKeys(ctx context.Context, limit int) (int64, error) {
	if err := service.ensureFeatureEnabled(); err != nil {
		return 0, err
	}
	if limit <= 0 || limit > 100000 {
		return 0, ErrInvalidOverlay
	}
	key, err := service.activeSearchKey(ctx)
	if err != nil {
		return 0, err
	}
	var rows []model.BackupAssetTagDefinition
	if err := service.db.WithContext(ctx).Where("token_state = ? OR key_version <> ?", "rekeying", key.Version).
		Order("id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return 0, err
	}
	var count int64
	for _, row := range rows {
		normalized, err := assetsearch.NormalizeFieldV1(assetsearch.SearchFieldTag, row.EncryptedName, assetsearch.DefaultNormalizerLimits())
		if err != nil {
			return count, ErrInvalidOverlay
		}
		token, err := assetsearch.TokenHMAC(key.Key, key.Version, assetsearch.NormalizerVersion, assetsearch.SearchFieldTag, assetsearch.TokenKindExact, normalized.Canonical)
		if err != nil {
			return count, err
		}
		result := service.db.WithContext(ctx).Model(&model.BackupAssetTagDefinition{}).
			Where("id = ? AND token_state = ?", row.ID, "rekeying").
			Updates(map[string]any{"name_token": token, "key_version": key.Version, "token_state": "active", "updated_at": service.utcNow()})
		if result.Error != nil {
			return count, result.Error
		}
		count += result.RowsAffected
	}
	return count, nil
}

func (service *Service) tagToken(ctx context.Context, name string) (backupasset.DomainKeyMaterial, string, string, error) {
	key, err := service.activeSearchKey(ctx)
	if err != nil {
		return backupasset.DomainKeyMaterial{}, "", "", err
	}
	normalized, err := assetsearch.NormalizeFieldV1(assetsearch.SearchFieldTag, name, assetsearch.DefaultNormalizerLimits())
	if err != nil {
		return backupasset.DomainKeyMaterial{}, "", "", ErrInvalidOverlay
	}
	token, err := assetsearch.TokenHMAC(key.Key, key.Version, assetsearch.NormalizerVersion, assetsearch.SearchFieldTag, assetsearch.TokenKindExact, normalized.Canonical)
	if err != nil {
		return backupasset.DomainKeyMaterial{}, "", "", err
	}
	return key, normalized.Canonical, token, nil
}

func (service *Service) ensureTagAvailable(ctx context.Context, keyVersion int) error {
	var count int64
	if err := service.db.WithContext(ctx).Model(&model.BackupAssetTagDefinition{}).
		Where("token_state <> ? OR key_version <> ?", "active", keyVersion).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return ErrOverlayUnavailable
	}
	return nil
}

func (service *Service) activeSearchKey(ctx context.Context) (backupasset.DomainKeyMaterial, error) {
	key, err := service.keys.Active(ctx, backupasset.KeyDomainSearchToken)
	if err != nil || key.Domain != backupasset.KeyDomainSearchToken || key.State != backupasset.DomainKeyActive || len(key.Key) != 32 {
		return backupasset.DomainKeyMaterial{}, ErrOverlayUnavailable
	}
	return key, nil
}

func (service *Service) lockUsage(tx *gorm.DB, ownerID uint) (model.BackupAssetOverlayUsage, error) {
	var usage model.BackupAssetOverlayUsage
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_user_id = ?", ownerID).Take(&usage).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		candidate := model.BackupAssetOverlayUsage{
			OwnerUserID: ownerID, RecentRateWindowStartedAt: service.utcNow(), Version: 1, UpdatedAt: service.utcNow(),
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
			return usage, err
		}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("owner_user_id = ?", ownerID).Take(&usage).Error
	}
	return usage, err
}

func (service *Service) updateUsage(tx *gorm.DB, usage model.BackupAssetOverlayUsage, updates map[string]any) error {
	updates["version"] = usage.Version + 1
	updates["updated_at"] = service.utcNow()
	result := tx.Model(&model.BackupAssetOverlayUsage{}).
		Where("owner_user_id = ? AND version = ?", usage.OwnerUserID, usage.Version).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return backupasset.ErrConflict
	}
	return nil
}

func (service *Service) mutation(ctx context.Context, operation func(*gorm.DB) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		err := service.db.WithContext(ctx).Transaction(operation)
		if err == nil || !retryableMutation(err) {
			return err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Millisecond):
		}
	}
	return lastErr
}

func retryableMutation(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "locked") || strings.Contains(message, "busy") || strings.Contains(message, "serialization")
}

func loadSavedSearchTx(tx *gorm.DB, ownerID uint, id string, limits assetsearch.QueryLimits) (SavedSearch, error) {
	var row model.BackupAssetSavedSearch
	if err := tx.Where("id = ? AND owner_user_id = ?", id, ownerID).Take(&row).Error; err != nil {
		return SavedSearch{}, err
	}
	canonical, err := assetsearch.DecodeAndCanonicalize([]byte(row.EncryptedAST), limits)
	if err != nil {
		return SavedSearch{}, ErrOverlayUnavailable
	}
	return savedSearchFromModel(row, canonical.Request)
}

func (service *Service) markSavedSearchBroken(ctx context.Context, ownerID uint, id string, reason SavedSearchReason) error {
	return service.mutation(ctx, func(tx *gorm.DB) error {
		var row model.BackupAssetSavedSearch
		if err := tx.Where("id = ? AND owner_user_id = ?", id, ownerID).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return backupasset.ErrNotFound
		} else if err != nil {
			return err
		}
		if row.State != string(SavedSearchActive) {
			return nil
		}
		now := service.utcNow()
		return tx.Model(&model.BackupAssetSavedSearch{}).Where("id = ? AND owner_user_id = ? AND version = ?", id, ownerID, row.Version).
			Updates(map[string]any{
				"state": string(SavedSearchBroken), "state_reason": string(reason), "broken_at": now,
				"version": row.Version + 1, "updated_at": now,
			}).Error
	})
}

func savedSearchFromModel(row model.BackupAssetSavedSearch, query assetsearch.SearchRequest) (SavedSearch, error) {
	if !validStoredOverlayBase(row.ID, row.OwnerUserID, row.Version, row.CreatedAt, row.UpdatedAt) {
		return SavedSearch{}, ErrOverlayUnavailable
	}
	state, err := ParseSavedSearchState(row.State)
	if err != nil {
		return SavedSearch{}, ErrOverlayUnavailable
	}
	reason := SavedSearchReason(row.StateReason)
	if !validSavedSearchProduct(state, reason, row.BrokenAt) {
		return SavedSearch{}, ErrOverlayUnavailable
	}
	return SavedSearch{ID: row.ID, OwnerUserID: row.OwnerUserID, Query: query, Version: row.Version,
		State: state, StateReason: reason, BrokenAt: utcPointer(row.BrokenAt),
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}, nil
}

func loadFavoriteByID(tx *gorm.DB, ownerID uint, id string) (Favorite, error) {
	var row model.BackupAssetFavorite
	if err := tx.Where("id = ? AND owner_user_id = ?", id, ownerID).Take(&row).Error; err != nil {
		return Favorite{}, err
	}
	return favoriteFromModel(row)
}

func favoriteFromModel(row model.BackupAssetFavorite) (Favorite, error) {
	if !validStoredOverlayBase(row.ID, row.OwnerUserID, row.Version, row.CreatedAt, row.UpdatedAt) ||
		ValidateOverlayRef(backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}) != nil ||
		!validOverlayStateProduct(OverlayState(row.State), TombstoneReason(row.TombstoneReason)) {
		return Favorite{}, ErrOverlayUnavailable
	}
	return Favorite{ID: row.ID, OwnerUserID: row.OwnerUserID,
		Ref: backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}, Label: row.EncryptedLabel,
		State: OverlayState(row.State), Reason: TombstoneReason(row.TombstoneReason), Version: row.Version,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}, nil
}

func loadTagByID(tx *gorm.DB, ownerID uint, id string) (Tag, error) {
	var row model.BackupAssetTagDefinition
	if err := tx.Where("id = ? AND owner_user_id = ?", id, ownerID).Take(&row).Error; err != nil {
		return Tag{}, err
	}
	return tagFromModel(row)
}

func tagFromModel(row model.BackupAssetTagDefinition) (Tag, error) {
	if !validStoredOverlayBase(row.ID, row.OwnerUserID, row.Version, row.CreatedAt, row.UpdatedAt) ||
		!utf8.ValidString(row.EncryptedName) || strings.TrimSpace(row.EncryptedName) == "" ||
		!validLowerHex(row.NameToken, 64) || row.KeyVersion <= 0 ||
		(row.TokenState != "active" && row.TokenState != "rekeying") {
		return Tag{}, ErrOverlayUnavailable
	}
	return Tag{ID: row.ID, OwnerUserID: row.OwnerUserID, Name: row.EncryptedName, Version: row.Version,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}, nil
}

func loadAssignmentByID(tx *gorm.DB, ownerID uint, id string) (TagAssignment, error) {
	var row model.BackupAssetTagAssignment
	if err := tx.Where("id = ? AND owner_user_id = ?", id, ownerID).Take(&row).Error; err != nil {
		return TagAssignment{}, err
	}
	return assignmentFromModel(row)
}

func assignmentFromModel(row model.BackupAssetTagAssignment) (TagAssignment, error) {
	if !validStoredOverlayBase(row.ID, row.OwnerUserID, row.Version, row.CreatedAt, row.UpdatedAt) ||
		backupasset.ValidateOpaqueID(row.TagID) != nil ||
		ValidateOverlayRef(backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}) != nil ||
		!validOverlayStateProduct(OverlayState(row.State), TombstoneReason(row.TombstoneReason)) {
		return TagAssignment{}, ErrOverlayUnavailable
	}
	return TagAssignment{ID: row.ID, OwnerUserID: row.OwnerUserID, TagID: row.TagID,
		Ref:   backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID},
		State: OverlayState(row.State), Reason: TombstoneReason(row.TombstoneReason), Version: row.Version}, nil
}

func recentFromModel(row model.BackupAssetRecentAccess) (RecentAccess, error) {
	if !validStoredOverlayBase(row.ID, row.OwnerUserID, row.Version, row.CreatedAt, row.UpdatedAt) ||
		ValidateOverlayRef(backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}) != nil ||
		row.AccessCount <= 0 || row.LastAccessedAt.IsZero() || row.ExpiresAt.IsZero() || !row.ExpiresAt.After(row.LastAccessedAt) {
		return RecentAccess{}, ErrOverlayUnavailable
	}
	return RecentAccess{ID: row.ID, OwnerUserID: row.OwnerUserID,
		Ref: backupasset.AssetRef{RecoveryPointID: row.RecoveryPointID, EntryID: row.EntryID}, AccessCount: row.AccessCount,
		LastAccessedAt: row.LastAccessedAt.UTC(), ExpiresAt: row.ExpiresAt.UTC(), Version: row.Version}, nil
}

func validStoredOverlayBase(id string, ownerID uint, version int, createdAt, updatedAt time.Time) bool {
	return backupasset.ValidateOpaqueID(id) == nil && ownerID > 0 && version > 0 &&
		!createdAt.IsZero() && !updatedAt.IsZero() && !updatedAt.Before(createdAt)
}

func validSavedSearchProduct(state SavedSearchState, reason SavedSearchReason, brokenAt *time.Time) bool {
	switch state {
	case SavedSearchActive:
		return reason == SavedReasonNone && brokenAt == nil
	case SavedSearchBroken:
		return brokenAt != nil && (reason == SavedReasonPointRetired || reason == SavedReasonPointExpiring ||
			reason == SavedReasonPointExpired || reason == SavedReasonPointFailed || reason == SavedReasonPointPurgeBlocked ||
			reason == SavedReasonPointMissing || reason == SavedReasonScopeUnauthorized)
	case SavedSearchBlocked:
		return brokenAt == nil && reason == SavedReasonASTSchemaUnsupported
	default:
		return false
	}
}

func validOverlayStateProduct(state OverlayState, reason TombstoneReason) bool {
	if state == OverlayActive {
		return reason == ""
	}
	if state != OverlayTombstone {
		return false
	}
	return reason == TombstoneSourceRetired || reason == TombstoneSourceExpiring || reason == TombstoneSourceExpired ||
		reason == TombstoneSourceFailed || reason == TombstoneSourcePurgeBlocked || reason == TombstoneSourceMissing
}

func validLowerHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (service *Service) overlayQueryLimits() assetsearch.QueryLimits {
	return service.config.QueryLimits
}

func (service *Service) overlayListLimit(ownerID uint, request OverlayListRequest) (int, error) {
	if ownerID == 0 || request.Limit < 0 || request.Limit > service.config.QueryLimits.MaxPageSize ||
		(request.Cursor != "" && backupasset.ValidateOpaqueID(request.Cursor) != nil) {
		return 0, ErrInvalidOverlay
	}
	if request.Limit == 0 {
		return min(50, service.config.QueryLimits.MaxPageSize), nil
	}
	return request.Limit, nil
}

func validUserText(value string, maxBytes int) bool {
	return utf8.ValidString(value) && len(value) <= maxBytes && !strings.ContainsAny(value, "\r\n\x00")
}

func digestStrings(values ...string) string {
	hash := sha256.New()
	for index, value := range values {
		if index > 0 {
			_, _ = hash.Write([]byte{0})
		}
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (service *Service) writeAudit(ctx context.Context, input backupasset.AuditEventInput) {
	if service == nil || service.audit == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := service.audit.Write(ctx, input); err != nil {
		logger.Module("backup_asset_overlay").Warn().Str("action", string(input.Action)).Msg("备份资产用户覆盖审计写入失败")
	}
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func (service *Service) utcNow() time.Time { return service.now().UTC() }

func (service *Service) ensureFeatureEnabled() error {
	if service == nil || service.featureEnabled == nil {
		return fmt.Errorf("%w: Overlay feature gate unavailable", backupasset.ErrInvalidState)
	}
	enabled, err := service.featureEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		return catalog.ErrFeatureDisabled
	}
	return nil
}
