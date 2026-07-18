package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/overlay"
	assetsearch "xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

type BackupAssetOverlayService interface {
	ListSavedSearches(context.Context, uint, overlay.OverlayListRequest) (overlay.SavedSearchPage, error)
	CreateSavedSearch(context.Context, overlay.Actor, overlay.CreateSavedSearchRequest) (overlay.SavedSearch, error)
	GetSavedSearch(context.Context, uint, string) (overlay.SavedSearch, error)
	UpdateSavedSearch(context.Context, overlay.Actor, string, overlay.UpdateSavedSearchRequest) (overlay.SavedSearch, error)
	DeleteSavedSearch(context.Context, uint, string, int, string) error
	ListFavorites(context.Context, uint, overlay.OverlayListRequest) (overlay.FavoritePage, error)
	AddFavorite(context.Context, overlay.Actor, overlay.AddFavoriteRequest) (overlay.Favorite, error)
	RemoveFavorite(context.Context, uint, backupasset.AssetRef, string) error
	ListTags(context.Context, uint, overlay.OverlayListRequest) (overlay.TagPage, error)
	CreateTag(context.Context, uint, string, string) (overlay.Tag, error)
	UpdateTag(context.Context, uint, string, overlay.UpdateTagRequest) (overlay.Tag, error)
	DeleteTag(context.Context, uint, string, int, string) error
	AssignTag(context.Context, overlay.Actor, string, backupasset.AssetRef, string) (overlay.TagAssignment, error)
	UnassignTag(context.Context, uint, string, backupasset.AssetRef, string) error
	ListRecent(context.Context, uint, overlay.OverlayListRequest) (overlay.RecentAccessPage, error)
	ClearRecent(context.Context, uint, string) (int64, error)
}

type BackupAssetOverlayHandler struct {
	service      BackupAssetOverlayService
	audit        BackupAssetAuditSink
	configSource BackupAssetHandlerConfigSource
}

func NewBackupAssetOverlayHandler(service BackupAssetOverlayService, audit BackupAssetAuditSink, configSource BackupAssetHandlerConfigSource) *BackupAssetOverlayHandler {
	return &BackupAssetOverlayHandler{service: service, audit: audit, configSource: configSource}
}

type featureDisabledBackupAssetOverlayService struct{}

func NewFeatureDisabledBackupAssetOverlayService() BackupAssetOverlayService {
	return featureDisabledBackupAssetOverlayService{}
}

func (featureDisabledBackupAssetOverlayService) ListSavedSearches(context.Context, uint, overlay.OverlayListRequest) (overlay.SavedSearchPage, error) {
	return overlay.SavedSearchPage{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) CreateSavedSearch(context.Context, overlay.Actor, overlay.CreateSavedSearchRequest) (overlay.SavedSearch, error) {
	return overlay.SavedSearch{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) GetSavedSearch(context.Context, uint, string) (overlay.SavedSearch, error) {
	return overlay.SavedSearch{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) UpdateSavedSearch(context.Context, overlay.Actor, string, overlay.UpdateSavedSearchRequest) (overlay.SavedSearch, error) {
	return overlay.SavedSearch{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) DeleteSavedSearch(context.Context, uint, string, int, string) error {
	return catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) ListFavorites(context.Context, uint, overlay.OverlayListRequest) (overlay.FavoritePage, error) {
	return overlay.FavoritePage{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) AddFavorite(context.Context, overlay.Actor, overlay.AddFavoriteRequest) (overlay.Favorite, error) {
	return overlay.Favorite{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) RemoveFavorite(context.Context, uint, backupasset.AssetRef, string) error {
	return catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) ListTags(context.Context, uint, overlay.OverlayListRequest) (overlay.TagPage, error) {
	return overlay.TagPage{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) CreateTag(context.Context, uint, string, string) (overlay.Tag, error) {
	return overlay.Tag{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) UpdateTag(context.Context, uint, string, overlay.UpdateTagRequest) (overlay.Tag, error) {
	return overlay.Tag{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) DeleteTag(context.Context, uint, string, int, string) error {
	return catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) AssignTag(context.Context, overlay.Actor, string, backupasset.AssetRef, string) (overlay.TagAssignment, error) {
	return overlay.TagAssignment{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) UnassignTag(context.Context, uint, string, backupasset.AssetRef, string) error {
	return catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) ListRecent(context.Context, uint, overlay.OverlayListRequest) (overlay.RecentAccessPage, error) {
	return overlay.RecentAccessPage{}, catalog.ErrFeatureDisabled
}
func (featureDisabledBackupAssetOverlayService) ClearRecent(context.Context, uint, string) (int64, error) {
	return 0, catalog.ErrFeatureDisabled
}

type createSavedSearchPayload struct {
	Query assetsearch.SearchRequest `json:"query"`
}

type updateSavedSearchPayload struct {
	Query           assetsearch.SearchRequest `json:"query"`
	ExpectedVersion int                       `json:"expected_version"`
}

type expectedOverlayVersionPayload struct {
	ExpectedVersion int `json:"expected_version"`
}

type favoritePayload struct {
	Ref   backupasset.AssetRef `json:"ref"`
	Label string               `json:"label,omitempty"`
}

type createTagPayload struct {
	Name string `json:"name"`
}

type updateTagPayload struct {
	Name            string `json:"name"`
	ExpectedVersion int    `json:"expected_version"`
}

type tagAssignmentPayload struct {
	Ref backupasset.AssetRef `json:"ref"`
}

// @Summary 列出保存搜索
// @Tags backup-assets
// @Security Bearer
// @Router /asset-saved-searches [get]
func (handler *BackupAssetOverlayHandler) ListSavedSearches(c *gin.Context) {
	request, ok := handler.prepareList(c)
	if !ok {
		return
	}
	result, err := handler.service.ListSavedSearches(c.Request.Context(), middleware.CurrentUserID(c), request)
	handler.finishRead(c, result, err)
}

// @Summary 创建保存搜索
// @Tags backup-assets
// @Security Bearer
// @Router /asset-saved-searches [post]
func (handler *BackupAssetOverlayHandler) CreateSavedSearch(c *gin.Context) {
	var payload createSavedSearchPayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	actor := backupAssetOverlayActor(c)
	result, err := handler.service.CreateSavedSearch(c.Request.Context(), actor, overlay.CreateSavedSearchRequest{Query: payload.Query, IdempotencyKey: key})
	handler.finishMutation(c, backupasset.AuditActionSavedSearchCreate, result, err, true, 0, backupasset.AssetRef{})
}

// @Summary 查看保存搜索
// @Tags backup-assets
// @Security Bearer
// @Router /asset-saved-searches/{id} [get]
func (handler *BackupAssetOverlayHandler) GetSavedSearch(c *gin.Context) {
	id, ok := backupAssetOpaqueParam(c, "id")
	if !ok {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if !handler.ensureAvailable(c) {
		return
	}
	result, err := handler.service.GetSavedSearch(c.Request.Context(), middleware.CurrentUserID(c), id)
	handler.finishRead(c, result, err)
}

// @Summary 更新保存搜索
// @Tags backup-assets
// @Security Bearer
// @Router /asset-saved-searches/{id} [patch]
func (handler *BackupAssetOverlayHandler) UpdateSavedSearch(c *gin.Context) {
	id, ok := backupAssetOpaqueParam(c, "id")
	var payload updateSavedSearchPayload
	if !ok || decodeStrictBackupAssetJSON(c, &payload) != nil || payload.ExpectedVersion <= 0 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	result, err := handler.service.UpdateSavedSearch(c.Request.Context(), backupAssetOverlayActor(c), id, overlay.UpdateSavedSearchRequest{
		Query: payload.Query, ExpectedVersion: payload.ExpectedVersion, IdempotencyKey: key,
	})
	handler.finishMutation(c, backupasset.AuditActionSavedSearchUpdate, result, err, false, 0, backupasset.AssetRef{})
}

// @Summary 删除保存搜索
// @Tags backup-assets
// @Security Bearer
// @Router /asset-saved-searches/{id} [delete]
func (handler *BackupAssetOverlayHandler) DeleteSavedSearch(c *gin.Context) {
	id, ok := backupAssetOpaqueParam(c, "id")
	var payload expectedOverlayVersionPayload
	if !ok || decodeStrictBackupAssetJSON(c, &payload) != nil || payload.ExpectedVersion <= 0 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	err := handler.service.DeleteSavedSearch(c.Request.Context(), middleware.CurrentUserID(c), id, payload.ExpectedVersion, key)
	handler.finishMutation(c, backupasset.AuditActionSavedSearchDelete, nil, err, false, 0, backupasset.AssetRef{})
}

// @Summary 列出收藏
// @Tags backup-assets
// @Security Bearer
// @Router /asset-favorites [get]
func (handler *BackupAssetOverlayHandler) ListFavorites(c *gin.Context) {
	request, ok := handler.prepareList(c)
	if !ok {
		return
	}
	result, err := handler.service.ListFavorites(c.Request.Context(), middleware.CurrentUserID(c), request)
	handler.finishRead(c, result, err)
}

// @Summary 添加收藏
// @Tags backup-assets
// @Security Bearer
// @Router /asset-favorites [post]
func (handler *BackupAssetOverlayHandler) AddFavorite(c *gin.Context) {
	var payload favoritePayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || overlay.ValidateOverlayRef(payload.Ref) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	result, err := handler.service.AddFavorite(c.Request.Context(), backupAssetOverlayActor(c), overlay.AddFavoriteRequest{
		Ref: payload.Ref, Label: payload.Label, IdempotencyKey: key,
	})
	handler.finishMutation(c, backupasset.AuditActionFavoriteAdd, result, err, true, 0, payload.Ref)
}

// @Summary 删除收藏
// @Tags backup-assets
// @Security Bearer
// @Router /asset-favorites/{recoveryPointId}/{entryId} [delete]
func (handler *BackupAssetOverlayHandler) RemoveFavorite(c *gin.Context) {
	ref, ok := backupAssetOverlayRefParams(c)
	if !ok || !backupAssetOverlayEmptyBody(c) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	err := handler.service.RemoveFavorite(c.Request.Context(), middleware.CurrentUserID(c), ref, key)
	handler.finishMutation(c, backupasset.AuditActionFavoriteRemove, nil, err, false, 0, ref)
}

// @Summary 列出用户标签
// @Tags backup-assets
// @Security Bearer
// @Router /asset-tags [get]
func (handler *BackupAssetOverlayHandler) ListTags(c *gin.Context) {
	request, ok := handler.prepareList(c)
	if !ok {
		return
	}
	result, err := handler.service.ListTags(c.Request.Context(), middleware.CurrentUserID(c), request)
	handler.finishRead(c, result, err)
}

// @Summary 创建用户标签
// @Tags backup-assets
// @Security Bearer
// @Router /asset-tags [post]
func (handler *BackupAssetOverlayHandler) CreateTag(c *gin.Context) {
	var payload createTagPayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || strings.TrimSpace(payload.Name) == "" {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	result, err := handler.service.CreateTag(c.Request.Context(), middleware.CurrentUserID(c), payload.Name, key)
	handler.finishMutation(c, backupasset.AuditActionTagCreate, result, err, true, 0, backupasset.AssetRef{})
}

// @Summary 更新用户标签
// @Tags backup-assets
// @Security Bearer
// @Router /asset-tags/{id} [patch]
func (handler *BackupAssetOverlayHandler) UpdateTag(c *gin.Context) {
	id, ok := backupAssetOpaqueParam(c, "id")
	var payload updateTagPayload
	if !ok || decodeStrictBackupAssetJSON(c, &payload) != nil || strings.TrimSpace(payload.Name) == "" || payload.ExpectedVersion <= 0 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	result, err := handler.service.UpdateTag(c.Request.Context(), middleware.CurrentUserID(c), id, overlay.UpdateTagRequest{
		Name: payload.Name, ExpectedVersion: payload.ExpectedVersion, IdempotencyKey: key,
	})
	handler.finishMutation(c, backupasset.AuditActionTagUpdate, result, err, false, 0, backupasset.AssetRef{})
}

// @Summary 删除用户标签
// @Tags backup-assets
// @Security Bearer
// @Router /asset-tags/{id} [delete]
func (handler *BackupAssetOverlayHandler) DeleteTag(c *gin.Context) {
	id, ok := backupAssetOpaqueParam(c, "id")
	var payload expectedOverlayVersionPayload
	if !ok || decodeStrictBackupAssetJSON(c, &payload) != nil || payload.ExpectedVersion <= 0 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	err := handler.service.DeleteTag(c.Request.Context(), middleware.CurrentUserID(c), id, payload.ExpectedVersion, key)
	handler.finishMutation(c, backupasset.AuditActionTagDelete, nil, err, false, 0, backupasset.AssetRef{})
}

// @Summary 分配用户标签
// @Tags backup-assets
// @Security Bearer
// @Router /asset-tags/{id}/assignments [post]
func (handler *BackupAssetOverlayHandler) AssignTag(c *gin.Context) {
	id, ok := backupAssetOpaqueParam(c, "id")
	var payload tagAssignmentPayload
	if !ok || decodeStrictBackupAssetJSON(c, &payload) != nil || overlay.ValidateOverlayRef(payload.Ref) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	result, err := handler.service.AssignTag(c.Request.Context(), backupAssetOverlayActor(c), id, payload.Ref, key)
	handler.finishMutation(c, backupasset.AuditActionTagAssign, result, err, true, 0, payload.Ref)
}

// @Summary 取消用户标签分配
// @Tags backup-assets
// @Security Bearer
// @Router /asset-tags/{id}/assignments/{recoveryPointId}/{entryId} [delete]
func (handler *BackupAssetOverlayHandler) UnassignTag(c *gin.Context) {
	id, idOK := backupAssetOpaqueParam(c, "id")
	ref, refOK := backupAssetOverlayRefParams(c)
	if !idOK || !refOK || !backupAssetOverlayEmptyBody(c) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	err := handler.service.UnassignTag(c.Request.Context(), middleware.CurrentUserID(c), id, ref, key)
	handler.finishMutation(c, backupasset.AuditActionTagUnassign, nil, err, false, 0, ref)
}

// @Summary 列出最近访问
// @Tags backup-assets
// @Security Bearer
// @Router /asset-recent [get]
func (handler *BackupAssetOverlayHandler) ListRecent(c *gin.Context) {
	request, ok := handler.prepareList(c)
	if !ok {
		return
	}
	result, err := handler.service.ListRecent(c.Request.Context(), middleware.CurrentUserID(c), request)
	handler.finishRead(c, result, err)
}

// @Summary 清空最近访问
// @Tags backup-assets
// @Security Bearer
// @Router /asset-recent/clear [post]
func (handler *BackupAssetOverlayHandler) ClearRecent(c *gin.Context) {
	if !backupAssetOverlayEmptyBody(c) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := handler.prepareMutation(c)
	if !ok {
		return
	}
	count, err := handler.service.ClearRecent(c.Request.Context(), middleware.CurrentUserID(c), key)
	handler.finishMutation(c, backupasset.AuditActionRecentClear, map[string]int64{"cleared_count": count}, err, false, count, backupasset.AssetRef{})
}

func (handler *BackupAssetOverlayHandler) ensureAvailable(c *gin.Context) bool {
	if handler == nil || handler.service == nil {
		respondServiceUnavailable(c, "用户备份资产数据暂不可用")
		return false
	}
	config, ok := loadBackupAssetHandlerConfig(c, handler.configSource)
	return ok && ensureBackupAssetHandlerEnabled(c, config)
}

func (handler *BackupAssetOverlayHandler) prepareMutation(c *gin.Context) (string, bool) {
	values := c.Request.Header.Values("Idempotency-Key")
	if len(values) != 1 || !validBackupAssetIdempotencyKey(values[0], 256) {
		respondBadRequest(c, "Idempotency-Key 不合法")
		return "", false
	}
	if handler == nil || handler.service == nil {
		respondServiceUnavailable(c, "用户备份资产数据暂不可用")
		return "", false
	}
	config, ok := loadBackupAssetHandlerConfig(c, handler.configSource)
	if !ok {
		return "", false
	}
	if !validBackupAssetIdempotencyKey(values[0], config.IdempotencyKeyMaxBytes) {
		respondBadRequest(c, "Idempotency-Key 不合法")
		return "", false
	}
	if !ensureBackupAssetHandlerEnabled(c, config) {
		return "", false
	}
	return values[0], true
}

func (handler *BackupAssetOverlayHandler) prepareList(c *gin.Context) (overlay.OverlayListRequest, bool) {
	request, ok := backupAssetOverlayListRequest(c)
	if !ok {
		return overlay.OverlayListRequest{}, false
	}
	if handler == nil || handler.service == nil {
		respondServiceUnavailable(c, "用户备份资产数据暂不可用")
		return overlay.OverlayListRequest{}, false
	}
	config, ok := loadBackupAssetHandlerConfig(c, handler.configSource)
	if !ok {
		return overlay.OverlayListRequest{}, false
	}
	if request.Limit > config.QueryLimits.MaxPageSize {
		respondBadRequest(c, "请求参数不合法")
		return overlay.OverlayListRequest{}, false
	}
	if !ensureBackupAssetHandlerEnabled(c, config) {
		return overlay.OverlayListRequest{}, false
	}
	return request, true
}

func validBackupAssetIdempotencyKey(value string, maxBytes int) bool {
	if len(value) < 16 || len(value) > maxBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' && character != '.' && character != '~' {
			return false
		}
	}
	return true
}

func backupAssetOverlayListRequest(c *gin.Context) (overlay.OverlayListRequest, bool) {
	values, ok := backupAssetQuery(c, "limit", "cursor")
	if !ok {
		respondBadRequest(c, "请求参数不合法")
		return overlay.OverlayListRequest{}, false
	}
	request := overlay.OverlayListRequest{Cursor: values.Get("cursor")}
	if request.Cursor != "" && backupasset.ValidateOpaqueID(request.Cursor) != nil {
		respondBadRequest(c, "请求参数不合法")
		return overlay.OverlayListRequest{}, false
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > maxBackupAssetPageLimit {
			respondBadRequest(c, "请求参数不合法")
			return overlay.OverlayListRequest{}, false
		}
		request.Limit = limit
	}
	return request, true
}

func backupAssetOverlayRefParams(c *gin.Context) (backupasset.AssetRef, bool) {
	ref := backupasset.AssetRef{
		RecoveryPointID: strings.TrimSpace(c.Param("recoveryPointId")),
		EntryID:         strings.TrimSpace(c.Param("entryId")),
	}
	return ref, c.Param("recoveryPointId") == ref.RecoveryPointID && c.Param("entryId") == ref.EntryID && overlay.ValidateOverlayRef(ref) == nil
}

func backupAssetOverlayEmptyBody(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.Body == nil || c.Request.Body == http.NoBody {
		return true
	}
	payload, err := io.ReadAll(io.LimitReader(c.Request.Body, 1025))
	return err == nil && len(payload) <= 1024 && len(bytes.TrimSpace(payload)) == 0
}

func backupAssetOverlayActor(c *gin.Context) overlay.Actor {
	return overlay.Actor{UserID: middleware.CurrentUserID(c), Role: middleware.CurrentRole(c)}
}

func (handler *BackupAssetOverlayHandler) finishRead(c *gin.Context, result any, err error) {
	if err != nil {
		respondBackupAssetSearchOverlayError(c, err)
		return
	}
	respondOK(c, result)
}

func (handler *BackupAssetOverlayHandler) finishMutation(
	c *gin.Context,
	action backupasset.AuditAction,
	result any,
	err error,
	created bool,
	itemCount int64,
	ref backupasset.AssetRef,
) {
	input := backupAssetAuditInput(c, action)
	input.ItemCount = itemCount
	if overlay.ValidateOverlayRef(ref) == nil {
		input.RecoveryPointID = ref.RecoveryPointID
		input.EntryID = ref.EntryID
	}
	if err != nil {
		status, code := backupAssetSearchOverlayErrorStatus(err)
		if status >= http.StatusInternalServerError {
			input.Outcome = backupasset.AuditOutcomeFailure
		} else {
			input.Outcome = backupasset.AuditOutcomeBlocked
		}
		input.FailureCode = code
		handler.writeAudit(c, input)
		respondBackupAssetSearchOverlayError(c, err)
		return
	}
	input.Outcome = backupasset.AuditOutcomeSuccess
	handler.writeAudit(c, input)
	if created {
		respondCreated(c, result)
		return
	}
	if result == nil {
		respondMessage(c, "ok")
		return
	}
	respondOK(c, result)
}

func (handler *BackupAssetOverlayHandler) writeAudit(c *gin.Context, input backupasset.AuditEventInput) {
	if handler == nil || handler.audit == nil {
		return
	}
	if err := handler.audit.Write(c.Request.Context(), input); err != nil {
		logger.Module("backup_asset_overlay_handler").Warn().Str("action", string(input.Action)).Msg("备份资产覆盖审计写入失败")
	}
}
