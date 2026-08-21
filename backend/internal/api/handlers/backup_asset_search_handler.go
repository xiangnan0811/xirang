package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/overlay"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type BackupAssetHandlerConfig struct {
	Enabled                bool
	QueryLimits            assetsearch.QueryLimits
	IdempotencyKeyMaxBytes int
}

type BackupAssetHandlerConfigSource func() (BackupAssetHandlerConfig, error)

type BackupAssetSearchService interface {
	Search(context.Context, assetsearch.SearchActor, assetsearch.SearchRequest) (assetsearch.SearchResponse, error)
}

type BackupAssetSavedSearchUseService interface {
	UseSavedSearch(context.Context, overlay.Actor, string) (overlay.SavedSearch, error)
}

type BackupAssetSecretProofVerifier func(*gin.Context) (*assetsearch.SecretRevealProof, error)

type BackupAssetSearchHandler struct {
	service      BackupAssetSearchService
	saved        BackupAssetSavedSearchUseService
	audit        BackupAssetAuditSink
	configSource BackupAssetHandlerConfigSource
	verifyProof  BackupAssetSecretProofVerifier
}

func NewBackupAssetSearchHandler(
	service BackupAssetSearchService,
	saved BackupAssetSavedSearchUseService,
	audit BackupAssetAuditSink,
	configSource BackupAssetHandlerConfigSource,
	verifyProof BackupAssetSecretProofVerifier,
) *BackupAssetSearchHandler {
	return &BackupAssetSearchHandler{service: service, saved: saved, audit: audit, configSource: configSource, verifyProof: verifyProof}
}

type featureDisabledBackupAssetSearchService struct{}

func NewFeatureDisabledBackupAssetSearchService() BackupAssetSearchService {
	return featureDisabledBackupAssetSearchService{}
}

func (featureDisabledBackupAssetSearchService) Search(context.Context, assetsearch.SearchActor, assetsearch.SearchRequest) (assetsearch.SearchResponse, error) {
	return assetsearch.SearchResponse{}, catalog.ErrFeatureDisabled
}

type featureDisabledBackupAssetSavedSearchUseService struct{}

func NewFeatureDisabledBackupAssetSavedSearchUseService() BackupAssetSavedSearchUseService {
	return featureDisabledBackupAssetSavedSearchUseService{}
}

func (featureDisabledBackupAssetSavedSearchUseService) UseSavedSearch(context.Context, overlay.Actor, string) (overlay.SavedSearch, error) {
	return overlay.SavedSearch{}, catalog.ErrFeatureDisabled
}

func NewFeatureDisabledBackupAssetHandlerConfigSource() BackupAssetHandlerConfigSource {
	return func() (BackupAssetHandlerConfig, error) {
		limits := assetsearch.DefaultQueryLimits()
		limits.MaxBodyBytes = maxBackupAssetRequestBytes
		limits.MaxPageSize = maxBackupAssetPageLimit
		return BackupAssetHandlerConfig{Enabled: false, QueryLimits: limits, IdempotencyKeyMaxBytes: 128}, nil
	}
}

func NewBackupAssetSecretProofVerifier(db *gorm.DB, jwtManager *auth.JWTManager) BackupAssetSecretProofVerifier {
	return func(c *gin.Context) (*assetsearch.SecretRevealProof, error) {
		proof := strings.TrimSpace(c.GetHeader(StepUpHeaderName))
		if proof == "" {
			return nil, nil
		}
		if middleware.CurrentRole(c) != "admin" {
			return nil, backupasset.ErrForbidden
		}
		claims, err := VerifyOptionalStepUpProof(db, jwtManager, proof, middleware.CurrentUserID(c), middleware.CurrentRole(c), auth.StepUpActionAssetSecretReveal)
		if err != nil || claims == nil {
			return nil, err
		}
		if claims.ExpiresAt == nil || backupasset.ValidateOpaqueID(claims.ID) != nil {
			return nil, ErrStepUpVerifierUnavailable
		}
		return &assetsearch.SecretRevealProof{ID: claims.ID, ExpiresAt: claims.ExpiresAt.UTC()}, nil
	}
}

type backupAssetSearchPayload struct {
	Query         *assetsearch.SearchRequest `json:"query,omitempty"`
	SavedSearchID string                     `json:"saved_search_id,omitempty"`
	Limit         int                        `json:"limit,omitempty"`
	Cursor        string                     `json:"cursor,omitempty"`
}

// Search godoc
// @Summary      搜索可见备份资产
// @Description  在服务端授权范围内执行便携 metadata 搜索；query 只存在于请求 body
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      backupAssetSearchPayload  true  "inline query 或 opaque saved-search ID"
// @Success      200   {object}  handlers.Response{data=assetsearch.SearchResponse}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /asset-search [post]
func (handler *BackupAssetSearchHandler) Search(c *gin.Context) {
	var payload backupAssetSearchPayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || !validBackupAssetSearchPayload(payload) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	config, ok := loadBackupAssetHandlerConfig(c, handler.configSource)
	if !ok || !ensureBackupAssetHandlerEnabled(c, config) {
		return
	}
	if payload.Limit > config.QueryLimits.MaxPageSize {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	request := assetsearch.SearchRequest{}
	savedSearchUsed := false
	if payload.Query != nil {
		request = *payload.Query
	} else {
		if handler == nil || handler.saved == nil {
			respondServiceUnavailable(c, "备份资产搜索暂不可用")
			return
		}
		saved, err := handler.saved.UseSavedSearch(c.Request.Context(), overlay.Actor{
			UserID: middleware.CurrentUserID(c), Role: middleware.CurrentRole(c),
		}, payload.SavedSearchID)
		if err != nil {
			respondBackupAssetSearchOverlayError(c, err)
			return
		}
		request = saved.Query
		if payload.Limit != 0 {
			request.Limit = payload.Limit
		}
		request.Cursor = payload.Cursor
		savedSearchUsed = true
	}
	canonical, err := assetsearch.ValidateAndCanonicalize(request, config.QueryLimits)
	if err != nil {
		respondBackupAssetSearchOverlayError(c, err)
		return
	}
	if handler == nil || handler.service == nil {
		respondServiceUnavailable(c, "备份资产搜索暂不可用")
		return
	}
	var proof *assetsearch.SecretRevealProof
	if strings.TrimSpace(c.GetHeader(StepUpHeaderName)) != "" {
		if handler.verifyProof == nil {
			respondServiceUnavailable(c, "备份资产搜索暂不可用")
			return
		}
		proof, err = handler.verifyProof(c)
		if err != nil {
			respondBackupAssetSearchOverlayError(c, err)
			return
		}
	}
	result, err := handler.service.Search(c.Request.Context(), assetsearch.SearchActor{
		Authorization: backupAssetAuthorizationScope(c), SecretProof: proof,
	}, canonical.Request)
	if err != nil {
		handler.writeSearchAudit(c, canonical.JSON, proof, backupasset.AuditOutcomeFailure, searchOverlayFailureCode(err), int64(len(result.Items)))
		respondBackupAssetSearchOverlayError(c, err)
		return
	}
	if savedSearchUsed {
		handler.writeTypedAudit(c, backupasset.AuditActionSavedSearchUse, backupasset.AuditOutcomeSuccess, "", 0)
	}
	handler.writeSearchAudit(c, canonical.JSON, proof, backupasset.AuditOutcomeSuccess, "", int64(len(result.Items)))
	respondOK(c, result)
}

func validBackupAssetSearchPayload(payload backupAssetSearchPayload) bool {
	inline := payload.Query != nil
	saved := payload.SavedSearchID != ""
	if inline == saved || payload.Limit < 0 || len(payload.Cursor) > maxBackupAssetCursorBytes || payload.Cursor != strings.TrimSpace(payload.Cursor) {
		return false
	}
	if inline {
		return payload.Limit == 0 && payload.Cursor == "" && strings.TrimSpace(payload.Query.Cursor) == payload.Query.Cursor &&
			len(payload.Query.Cursor) <= maxBackupAssetCursorBytes
	}
	return backupasset.ValidateOpaqueID(payload.SavedSearchID) == nil
}

func loadBackupAssetHandlerConfig(c *gin.Context, source BackupAssetHandlerConfigSource) (BackupAssetHandlerConfig, bool) {
	if source == nil {
		respondServiceUnavailable(c, "备份资产服务暂不可用")
		return BackupAssetHandlerConfig{}, false
	}
	config, err := source()
	if err != nil || config.QueryLimits.MaxBodyBytes <= 0 || config.QueryLimits.MaxBodyBytes > maxBackupAssetRequestBytes ||
		config.QueryLimits.MaxPageSize <= 0 || config.QueryLimits.MaxPageSize > 500 ||
		config.IdempotencyKeyMaxBytes < 16 || config.IdempotencyKeyMaxBytes > 256 {
		respondServiceUnavailable(c, "备份资产服务暂不可用")
		return BackupAssetHandlerConfig{}, false
	}
	return config, true
}

func ensureBackupAssetHandlerEnabled(c *gin.Context, config BackupAssetHandlerConfig) bool {
	if config.Enabled {
		return true
	}
	respondBackupAssetError(c, catalog.ErrFeatureDisabled, http.StatusServiceUnavailable)
	return false
}

func (handler *BackupAssetSearchHandler) writeSearchAudit(
	c *gin.Context,
	canonicalQuery []byte,
	proof *assetsearch.SecretRevealProof,
	outcome backupasset.AuditOutcome,
	failureCode string,
	itemCount int64,
) {
	input := backupAssetAuditInput(c, backupasset.AuditActionAssetSearch)
	input.Outcome = outcome
	input.FailureCode = failureCode
	input.ItemCount = itemCount
	input.Fingerprints.Query = string(canonicalQuery)
	if proof != nil {
		input.StepUpAction = string(auth.StepUpActionAssetSecretReveal)
		input.StepUpProofID = proof.ID
	}
	handler.writeAudit(c, input)
}

func (handler *BackupAssetSearchHandler) writeTypedAudit(c *gin.Context, action backupasset.AuditAction, outcome backupasset.AuditOutcome, failureCode string, count int64) {
	input := backupAssetAuditInput(c, action)
	input.Outcome = outcome
	input.FailureCode = failureCode
	input.ItemCount = count
	handler.writeAudit(c, input)
}

func (handler *BackupAssetSearchHandler) writeAudit(c *gin.Context, input backupasset.AuditEventInput) {
	if handler == nil || handler.audit == nil {
		return
	}
	if err := handler.audit.Write(c.Request.Context(), input); err != nil {
		logger.Module("backup_asset_search_handler").Warn().Str("action", string(input.Action)).Msg("备份资产搜索审计写入失败")
	}
}

func searchOverlayFailureCode(err error) string {
	_, code := backupAssetSearchOverlayErrorStatus(err)
	return code
}

func backupAssetSearchOverlayErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, assetsearch.ErrInvalidQuery), errors.Is(err, assetsearch.ErrInvalidScope),
		errors.Is(err, assetsearch.ErrInvalidCursor), errors.Is(err, overlay.ErrInvalidOverlay):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, backupasset.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, backupasset.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, assetsearch.ErrStaleCursor), errors.Is(err, assetsearch.ErrScopeStale),
		errors.Is(err, overlay.ErrSavedSearchBroken), errors.Is(err, overlay.ErrIdempotencyConflict),
		errors.Is(err, overlay.ErrQuotaExceeded), errors.Is(err, backupasset.ErrConflict):
		return http.StatusConflict, "stale_state"
	case errors.Is(err, catalog.ErrFeatureDisabled):
		return http.StatusServiceUnavailable, "feature_disabled"
	case errors.Is(err, assetsearch.ErrResourceLimit), errors.Is(err, assetsearch.ErrSearchKeyUnavailable),
		errors.Is(err, overlay.ErrOverlayUnavailable), errors.Is(err, ErrStepUpVerifierUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, "temporarily_unavailable"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

func respondBackupAssetSearchOverlayError(c *gin.Context, err error) {
	status, _ := backupAssetSearchOverlayErrorStatus(err)
	switch status {
	case http.StatusBadRequest:
		respondBadRequest(c, "请求参数不合法")
	case http.StatusForbidden:
		respondForbidden(c, "权限不足")
	case http.StatusNotFound:
		respondNotFound(c, "用户备份资产数据不存在")
	case http.StatusConflict:
		respondConflict(c, "用户备份资产状态已变化，请重试")
	case http.StatusServiceUnavailable:
		if errors.Is(err, catalog.ErrFeatureDisabled) {
			respondBackupAssetError(c, err, status)
			return
		}
		respondServiceUnavailable(c, "备份资产搜索暂不可用")
	default:
		logger.Module("backup_asset_search_handler").Error().Str("path", c.FullPath()).Str("code", "internal_error").Msg("备份资产搜索请求失败")
		respondInternalError(c, nil)
	}
}
