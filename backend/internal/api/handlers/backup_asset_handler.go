package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

const (
	maxBackupAssetRequestBytes = 64 << 10
	maxBackupAssetCursorBytes  = 8 << 10
	maxBackupAssetPageLimit    = 200
)

// BackupAssetCatalogService is the metadata-only Catalog boundary used by the
// HTTP layer. Provider access and Catalog construction remain runtime-owned.
type BackupAssetCatalogService interface {
	ListRecoveryPoints(context.Context, string, catalog.AuthorizationScope, catalog.RecoveryPointListRequest) (catalog.RecoveryPointPage, error)
	GetRecoveryPoint(context.Context, string, catalog.AuthorizationScope) (catalog.RecoveryPointView, error)
	GetCatalogStatus(context.Context, string, catalog.AuthorizationScope) (catalog.StatusDTO, error)
	GetEvidence(context.Context, string, catalog.AuthorizationScope) (catalog.EvidenceDTO, error)
	ListEntries(context.Context, string, catalog.AuthorizationScope, catalog.EntryListRequest) (catalog.EntryPage, error)
	GetEntry(context.Context, string, string, catalog.AuthorizationScope) (catalog.EntryDTO, error)
	Diff(context.Context, catalog.AuthorizationScope, catalog.DiffRequest) (catalog.DiffPage, error)
}

type BackupAssetAuditSink interface {
	Write(context.Context, backupasset.AuditEventInput) error
}

type BackupAssetHandler struct {
	service BackupAssetCatalogService
	audit   BackupAssetAuditSink
}

func NewBackupAssetHandler(service BackupAssetCatalogService, audit BackupAssetAuditSink) *BackupAssetHandler {
	return &BackupAssetHandler{service: service, audit: audit}
}

type featureDisabledBackupAssetCatalogService struct{}

// NewFeatureDisabledBackupAssetCatalogService returns a command-free fail-closed
// service for Router instances that intentionally omit the shared runtime.
func NewFeatureDisabledBackupAssetCatalogService() BackupAssetCatalogService {
	return featureDisabledBackupAssetCatalogService{}
}

func (featureDisabledBackupAssetCatalogService) ListRecoveryPoints(context.Context, string, catalog.AuthorizationScope, catalog.RecoveryPointListRequest) (catalog.RecoveryPointPage, error) {
	return catalog.RecoveryPointPage{}, catalog.ErrFeatureDisabled
}

func (featureDisabledBackupAssetCatalogService) GetRecoveryPoint(context.Context, string, catalog.AuthorizationScope) (catalog.RecoveryPointView, error) {
	return catalog.RecoveryPointView{}, catalog.ErrFeatureDisabled
}

func (featureDisabledBackupAssetCatalogService) GetCatalogStatus(context.Context, string, catalog.AuthorizationScope) (catalog.StatusDTO, error) {
	return catalog.StatusDTO{}, catalog.ErrFeatureDisabled
}

func (featureDisabledBackupAssetCatalogService) GetEvidence(context.Context, string, catalog.AuthorizationScope) (catalog.EvidenceDTO, error) {
	return catalog.EvidenceDTO{}, catalog.ErrFeatureDisabled
}

func (featureDisabledBackupAssetCatalogService) ListEntries(context.Context, string, catalog.AuthorizationScope, catalog.EntryListRequest) (catalog.EntryPage, error) {
	return catalog.EntryPage{}, catalog.ErrFeatureDisabled
}

func (featureDisabledBackupAssetCatalogService) GetEntry(context.Context, string, string, catalog.AuthorizationScope) (catalog.EntryDTO, error) {
	return catalog.EntryDTO{}, catalog.ErrFeatureDisabled
}

func (featureDisabledBackupAssetCatalogService) Diff(context.Context, catalog.AuthorizationScope, catalog.DiffRequest) (catalog.DiffPage, error) {
	return catalog.DiffPage{}, catalog.ErrFeatureDisabled
}

// ListRecoveryPoints godoc
// @Summary      列出仓库内可见恢复点
// @Description  按当前 producing lineage 授权后返回稳定分页的恢复点与 Catalog 状态
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id      path      string  true   "仓库 opaque ID"
// @Param        limit   query     int     false  "每页数量（最大 200）"
// @Param        cursor  query     string  false  "签名游标"
// @Param        sort    query     string  false  "captured_desc、captured_asc 或 created_desc"
// @Success      200     {object}  handlers.Response{data=catalog.RecoveryPointPage}
// @Failure      400     {object}  handlers.Response
// @Failure      401     {object}  handlers.Response
// @Failure      403     {object}  handlers.Response
// @Failure      404     {object}  handlers.Response
// @Failure      409     {object}  handlers.Response
// @Failure      503     {object}  handlers.Response
// @Router       /backup-repositories/{id}/recovery-points [get]
func (handler *BackupAssetHandler) ListRecoveryPoints(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionRecoveryPointList)
	repositoryID, ok := backupAssetOpaqueParam(c, "id")
	if !ok {
		handler.reject(c, audit)
		return
	}
	audit.RepositoryID = repositoryID
	values, ok := backupAssetQuery(c, "limit", "cursor", "sort")
	if !ok {
		handler.reject(c, audit)
		return
	}
	limit, ok := backupAssetLimit(values)
	if !ok {
		handler.reject(c, audit)
		return
	}
	cursor, ok := backupAssetCursor(values)
	if !ok {
		handler.reject(c, audit)
		return
	}
	sort, ok := backupAssetRecoveryPointSort(values)
	if !ok {
		handler.reject(c, audit)
		return
	}
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.ListRecoveryPoints(c.Request.Context(), repositoryID, backupAssetAuthorizationScope(c), catalog.RecoveryPointListRequest{
		Limit: limit, Cursor: cursor, Sort: sort,
	})
	audit.ItemCount = int64(len(result.Items))
	handler.finish(c, audit, result, err)
}

// GetRecoveryPoint godoc
// @Summary      查看恢复点详情
// @Description  返回经过 producing-lineage 授权和字段净化的恢复点详情
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id   path      string  true  "恢复点 opaque ID"
// @Success      200  {object}  handlers.Response{data=catalog.RecoveryPointView}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /recovery-points/{id} [get]
func (handler *BackupAssetHandler) GetRecoveryPoint(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionRecoveryPointDetail)
	pointID, ok := backupAssetOpaqueParam(c, "id")
	if !ok {
		handler.reject(c, audit)
		return
	}
	audit.RecoveryPointID = pointID
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.GetRecoveryPoint(c.Request.Context(), pointID, backupAssetAuthorizationScope(c))
	handler.finish(c, audit, result, err)
}

// GetCatalogStatus godoc
// @Summary      查看恢复点 Catalog 状态
// @Description  独立返回 generation、coverage、staleness 与内容可用性
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id   path      string  true  "恢复点 opaque ID"
// @Success      200  {object}  handlers.Response{data=catalog.StatusDTO}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /recovery-points/{id}/catalog-status [get]
func (handler *BackupAssetHandler) GetCatalogStatus(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionRecoveryPointDetail)
	pointID, ok := backupAssetOpaqueParam(c, "id")
	if !ok {
		handler.reject(c, audit)
		return
	}
	audit.RecoveryPointID = pointID
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.GetCatalogStatus(c.Request.Context(), pointID, backupAssetAuthorizationScope(c))
	handler.finish(c, audit, result, err)
}

// GetEvidence godoc
// @Summary      查看恢复点可信证据
// @Description  返回相互独立的 lineage、manifest、publication verification 与 restore drill 证据层
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id   path      string  true  "恢复点 opaque ID"
// @Success      200  {object}  handlers.Response{data=catalog.EvidenceDTO}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /recovery-points/{id}/evidence [get]
func (handler *BackupAssetHandler) GetEvidence(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionRecoveryPointEvidence)
	pointID, ok := backupAssetOpaqueParam(c, "id")
	if !ok {
		handler.reject(c, audit)
		return
	}
	audit.RecoveryPointID = pointID
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.GetEvidence(c.Request.Context(), pointID, backupAssetAuthorizationScope(c))
	handler.finish(c, audit, result, err)
}

// ListEntries godoc
// @Summary      列出恢复点 Catalog 目录项
// @Description  只接受 opaque parent entry ID，不接受 Provider 路径
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id      path      string  true   "恢复点 opaque ID"
// @Param        parent  query     string  false  "父目录 opaque entry ID；空值表示根"
// @Param        limit   query     int     false  "每页数量（最大 200）"
// @Param        cursor  query     string  false  "签名游标"
// @Param        sort    query     string  false  "name_asc、name_desc、size_desc 或 modified_desc"
// @Success      200     {object}  handlers.Response{data=catalog.EntryPage}
// @Failure      400     {object}  handlers.Response
// @Failure      401     {object}  handlers.Response
// @Failure      403     {object}  handlers.Response
// @Failure      404     {object}  handlers.Response
// @Failure      409     {object}  handlers.Response
// @Failure      503     {object}  handlers.Response
// @Router       /recovery-points/{id}/entries [get]
func (handler *BackupAssetHandler) ListEntries(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionAssetList)
	pointID, ok := backupAssetOpaqueParam(c, "id")
	if !ok {
		handler.reject(c, audit)
		return
	}
	audit.RecoveryPointID = pointID
	values, ok := backupAssetQuery(c, "parent", "limit", "cursor", "sort")
	if !ok {
		handler.reject(c, audit)
		return
	}
	parentID, ok := backupAssetOptionalEntryID(values, pointID, "parent")
	if !ok {
		handler.reject(c, audit)
		return
	}
	limit, ok := backupAssetLimit(values)
	if !ok {
		handler.reject(c, audit)
		return
	}
	cursor, ok := backupAssetCursor(values)
	if !ok {
		handler.reject(c, audit)
		return
	}
	sort, ok := backupAssetEntrySort(values)
	if !ok {
		handler.reject(c, audit)
		return
	}
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.ListEntries(c.Request.Context(), pointID, backupAssetAuthorizationScope(c), catalog.EntryListRequest{
		ParentEntryID: parentID, Limit: limit, Cursor: cursor, Sort: sort,
	})
	audit.ItemCount = int64(len(result.Items))
	handler.finish(c, audit, result, err)
}

// GetEntry godoc
// @Summary      查看恢复点 Catalog 目录项
// @Description  必须同时提供 recovery point 与 entry opaque ID
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id       path      string  true  "恢复点 opaque ID"
// @Param        entryId  path      string  true  "目录项 opaque ID"
// @Success      200      {object}  handlers.Response{data=catalog.EntryDTO}
// @Failure      400      {object}  handlers.Response
// @Failure      401      {object}  handlers.Response
// @Failure      403      {object}  handlers.Response
// @Failure      404      {object}  handlers.Response
// @Failure      503      {object}  handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId} [get]
func (handler *BackupAssetHandler) GetEntry(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionAssetList)
	pointID, ok := backupAssetOpaqueParam(c, "id")
	if !ok {
		handler.reject(c, audit)
		return
	}
	audit.RecoveryPointID = pointID
	entryID := strings.TrimSpace(c.Param("entryId"))
	if c.Param("entryId") != entryID || backupasset.ValidateAssetRef(backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}) != nil {
		handler.reject(c, audit)
		return
	}
	audit.EntryID = entryID
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.GetEntry(c.Request.Context(), pointID, entryID, backupAssetAuthorizationScope(c))
	handler.finish(c, audit, result, err)
}

type backupAssetDiffRequest struct {
	BaseRecoveryPointID    string           `json:"base_recovery_point_id"`
	CompareRecoveryPointID string           `json:"compare_recovery_point_id"`
	BaseParentEntryID      string           `json:"base_parent_entry_id,omitempty"`
	CompareParentEntryID   string           `json:"compare_parent_entry_id,omitempty"`
	Sort                   catalog.DiffSort `json:"sort,omitempty"`
	Limit                  int              `json:"limit,omitempty"`
	Cursor                 string           `json:"cursor,omitempty"`
}

// Diff godoc
// @Summary      比较两个精确恢复点
// @Description  返回两个已授权 active Catalog generation 的精确 metadata diff；请求不接受路径
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      backupAssetDiffRequest  true  "精确两点 diff 请求"
// @Success      200   {object}  handlers.Response{data=catalog.DiffPage}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      501   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /recovery-point-diffs [post]
func (handler *BackupAssetHandler) Diff(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionRecoveryPointDiff)
	if values, ok := backupAssetQuery(c); !ok || len(values) != 0 {
		handler.reject(c, audit)
		return
	}
	var request backupAssetDiffRequest
	if err := decodeStrictBackupAssetJSON(c, &request); err != nil || !validBackupAssetDiffRequest(request) {
		handler.reject(c, audit)
		return
	}
	audit.RecoveryPointID = request.BaseRecoveryPointID
	audit.Fields[backupasset.AuditFieldRecoveryPointID] = request.CompareRecoveryPointID
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.Diff(c.Request.Context(), backupAssetAuthorizationScope(c), catalog.DiffRequest{
		BaseRecoveryPointID: request.BaseRecoveryPointID, CompareRecoveryPointID: request.CompareRecoveryPointID,
		BaseParentEntryID: request.BaseParentEntryID, CompareParentEntryID: request.CompareParentEntryID,
		Sort: request.Sort, Limit: request.Limit, Cursor: request.Cursor,
	})
	audit.ItemCount = int64(len(result.Items))
	handler.finish(c, audit, result, err)
}

func (handler *BackupAssetHandler) available(c *gin.Context, audit backupasset.AuditEventInput) bool {
	if handler != nil && handler.service != nil {
		return true
	}
	audit.Outcome = backupasset.AuditOutcomeFailure
	audit.FailureCode = "service_unavailable"
	audit.Fields[backupasset.AuditFieldStatus] = "unavailable"
	handler.writeAudit(c, audit)
	respondInternalError(c, fmt.Errorf("backup asset Catalog service unavailable"))
	return false
}

func (handler *BackupAssetHandler) reject(c *gin.Context, audit backupasset.AuditEventInput) {
	audit.Outcome = backupasset.AuditOutcomeBlocked
	audit.FailureCode = "invalid_request"
	audit.Fields[backupasset.AuditFieldStatus] = "blocked"
	audit.Fields[backupasset.AuditFieldCode] = "invalid_request"
	handler.writeAudit(c, audit)
	respondBadRequest(c, "请求参数不合法")
}

func (handler *BackupAssetHandler) finish(c *gin.Context, audit backupasset.AuditEventInput, result any, err error) {
	if err == nil {
		audit.Outcome = backupasset.AuditOutcomeSuccess
		audit.Fields[backupasset.AuditFieldStatus] = "success"
		handler.writeAudit(c, audit)
		respondOK(c, result)
		return
	}
	status, code := backupAssetErrorStatus(err)
	if status == http.StatusBadRequest || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusConflict {
		audit.Outcome = backupasset.AuditOutcomeBlocked
	} else {
		audit.Outcome = backupasset.AuditOutcomeFailure
	}
	audit.FailureCode = code
	audit.Fields[backupasset.AuditFieldStatus] = string(audit.Outcome)
	audit.Fields[backupasset.AuditFieldCode] = code
	handler.writeAudit(c, audit)
	respondBackupAssetError(c, err, status)
}

func (handler *BackupAssetHandler) writeAudit(c *gin.Context, input backupasset.AuditEventInput) {
	if handler == nil || handler.audit == nil {
		return
	}
	if err := handler.audit.Write(c.Request.Context(), input); err != nil {
		logger.Module("backup_asset_handler").Warn().Str("action", string(input.Action)).Msg("备份资产审计写入失败")
	}
}

func backupAssetAuditInput(c *gin.Context, action backupasset.AuditAction) backupasset.AuditEventInput {
	correlationID := c.GetString(middleware.RequestIDKey)
	return backupasset.AuditEventInput{
		Actor: backupasset.AuditActor{
			UserID: middleware.CurrentUserID(c), Username: c.GetString(middleware.CtxUsername), Role: middleware.CurrentRole(c),
		},
		Action: action,
		Fields: map[backupasset.AuditField]any{
			backupasset.AuditFieldStage: "request", backupasset.AuditFieldCorrelationID: correlationID,
		},
	}
}

func backupAssetAuthorizationScope(c *gin.Context) catalog.AuthorizationScope {
	return catalog.AuthorizationScope{Role: middleware.CurrentRole(c), UserID: middleware.CurrentUserID(c)}
}

func backupAssetOpaqueParam(c *gin.Context, name string) (string, bool) {
	raw := c.Param(name)
	id := strings.TrimSpace(raw)
	return id, raw == id && backupasset.ValidateOpaqueID(id) == nil
}

func backupAssetQuery(c *gin.Context, allowed ...string) (url.Values, bool) {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return nil, false
	}
	allow := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		allow[key] = true
	}
	for key, items := range values {
		if !allow[key] || len(items) != 1 {
			return nil, false
		}
	}
	return values, true
}

func backupAssetLimit(values url.Values) (int, bool) {
	items, present := values["limit"]
	if !present {
		return 0, true
	}
	raw := items[0]
	if raw == "" || raw != strings.TrimSpace(raw) {
		return 0, false
	}
	limit, err := strconv.Atoi(raw)
	return limit, err == nil && limit > 0 && limit <= maxBackupAssetPageLimit
}

func backupAssetCursor(values url.Values) (string, bool) {
	items, present := values["cursor"]
	if !present {
		return "", true
	}
	cursor := items[0]
	return cursor, cursor == strings.TrimSpace(cursor) && len(cursor) <= maxBackupAssetCursorBytes
}

func backupAssetRecoveryPointSort(values url.Values) (catalog.RecoveryPointSort, bool) {
	raw := values.Get("sort")
	if raw == "" {
		return catalog.RecoveryPointSortCapturedDesc, true
	}
	sort := catalog.RecoveryPointSort(raw)
	switch sort {
	case catalog.RecoveryPointSortCapturedDesc, catalog.RecoveryPointSortCapturedAsc, catalog.RecoveryPointSortCreatedDesc:
		return sort, true
	default:
		return "", false
	}
}

func backupAssetEntrySort(values url.Values) (catalog.EntrySort, bool) {
	raw := values.Get("sort")
	if raw == "" {
		return catalog.EntrySortNameAsc, true
	}
	sort := catalog.EntrySort(raw)
	switch sort {
	case catalog.EntrySortNameAsc, catalog.EntrySortNameDesc, catalog.EntrySortSizeDesc, catalog.EntrySortModifiedDesc:
		return sort, true
	default:
		return "", false
	}
}

func backupAssetOptionalEntryID(values url.Values, pointID, key string) (string, bool) {
	items, present := values[key]
	if !present || items[0] == "" {
		return "", true
	}
	entryID := items[0]
	return entryID, entryID == strings.TrimSpace(entryID) && backupasset.ValidateAssetRef(backupasset.AssetRef{
		RecoveryPointID: pointID, EntryID: entryID,
	}) == nil
}

func decodeStrictBackupAssetJSON(c *gin.Context, target any) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return fmt.Errorf("request body missing")
	}
	payload, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBackupAssetRequestBytes+1))
	if err != nil || len(payload) > maxBackupAssetRequestBytes {
		return fmt.Errorf("request body exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing request data")
	}
	return nil
}

func validBackupAssetDiffRequest(request backupAssetDiffRequest) bool {
	if request.BaseRecoveryPointID == request.CompareRecoveryPointID ||
		backupasset.ValidateOpaqueID(request.BaseRecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(request.CompareRecoveryPointID) != nil ||
		request.Limit < 0 || request.Limit > maxBackupAssetPageLimit ||
		len(request.Cursor) > maxBackupAssetCursorBytes || request.Cursor != strings.TrimSpace(request.Cursor) {
		return false
	}
	if request.Sort == "" {
		request.Sort = catalog.DiffSortPathAsc
	}
	if request.Sort != catalog.DiffSortPathAsc {
		return false
	}
	if request.BaseParentEntryID != "" && backupasset.ValidateAssetRef(backupasset.AssetRef{
		RecoveryPointID: request.BaseRecoveryPointID, EntryID: request.BaseParentEntryID,
	}) != nil {
		return false
	}
	if request.CompareParentEntryID != "" && backupasset.ValidateAssetRef(backupasset.AssetRef{
		RecoveryPointID: request.CompareRecoveryPointID, EntryID: request.CompareParentEntryID,
	}) != nil {
		return false
	}
	return true
}

func backupAssetErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, catalog.ErrInvalidCursor), errors.Is(err, catalog.ErrInvalidCatalogContract),
		errors.Is(err, catalog.ErrInvalidAssetReference), errors.Is(err, backupasset.ErrInvalidAssetRef),
		errors.Is(err, backupasset.ErrInvalidState):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, backupasset.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, backupasset.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, catalog.ErrStaleCursor), errors.Is(err, catalog.ErrCatalogSourceChanged), errors.Is(err, backupasset.ErrConflict):
		return http.StatusConflict, "stale_state"
	case errors.Is(err, backupasset.ErrCapabilityUnavailable):
		return http.StatusNotImplemented, "unsupported"
	case errors.Is(err, catalog.ErrFeatureDisabled):
		return http.StatusServiceUnavailable, "feature_disabled"
	case errors.Is(err, catalog.ErrCatalogUnavailable), errors.Is(err, catalog.ErrOwnershipProjectionLimit),
		errors.Is(err, catalog.ErrCatalogBuildLimit), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, "temporarily_unavailable"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

func respondBackupAssetError(c *gin.Context, err error, status int) {
	switch status {
	case http.StatusBadRequest:
		respondBadRequest(c, "请求参数不合法")
	case http.StatusForbidden:
		respondForbidden(c, "权限不足")
	case http.StatusNotFound:
		respondNotFound(c, "备份资产不存在")
	case http.StatusConflict:
		respondConflict(c, "备份资产状态已变化，请重试")
	case http.StatusNotImplemented:
		respondBackupCapabilityError(c, status, backupasset.CapabilityReason{Code: backupasset.CapabilityTaskArtifactContractMissing}, c.GetString(middleware.RequestIDKey))
	case http.StatusServiceUnavailable:
		reason := backupasset.CapabilityProviderUnavailable
		if errors.Is(err, catalog.ErrFeatureDisabled) {
			reason = backupasset.CapabilityFeatureDisabled
		} else if errors.Is(err, context.DeadlineExceeded) {
			reason = backupasset.CapabilityProviderOperationTimeout
		}
		respondBackupCapabilityError(c, status, backupasset.CapabilityReason{Code: reason}, c.GetString(middleware.RequestIDKey))
	default:
		respondInternalError(c, err)
	}
}
