package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"xirang/backend/internal/backupasset"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

const (
	maxBackupRepositoryRequestBytes = 64 << 10
	maxBackupRepositoryCursorBytes  = 8192
)

type BackupRepositoryService interface {
	Connect(context.Context, backuprepository.ConnectRequest, backuprepository.RequestContext) (backuprepository.ConnectResult, error)
	List(context.Context, backuprepository.RepositoryListRequest, backuprepository.VisibilityScope, backuprepository.RequestContext) (backuprepository.RepositoryPage, error)
	Detail(context.Context, string, backuprepository.VisibilityScope, backuprepository.RequestContext) (backuprepository.RepositoryView, error)
	Reconcile(context.Context, string, backuprepository.RequestContext) (backuprepository.ConnectResult, error)
	Disconnect(context.Context, string, backuprepository.RequestContext) (backuprepository.ConnectResult, error)
	DiscoverImportCandidates(context.Context, string, backuprepository.ImportDiscoveryRequest, backuprepository.RequestContext) (backuprepository.ImportDiscoveryResult, error)
	ListImportCandidates(context.Context, string, backuprepository.ImportCandidateListRequest, backuprepository.VisibilityScope, backuprepository.RequestContext) (backuprepository.ImportCandidatePage, error)
	ReviewImportCandidate(context.Context, string, string, backuprepository.ImportReviewRequest, backuprepository.RequestContext) (backuprepository.ImportCandidateView, error)
	RebuildAcceptedImports(context.Context, string, backuprepository.RebuildRequest, backuprepository.RequestContext) (backuprepository.RebuildResult, error)
}

type BackupRepositoryHandler struct {
	service BackupRepositoryService
}

func NewBackupRepositoryHandler(service BackupRepositoryService) *BackupRepositoryHandler {
	return &BackupRepositoryHandler{service: service}
}

type backupRepositoryConnectRequest struct {
	TaskID        uint   `json:"task_id"`
	RepositoryID  string `json:"repository_id,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
	Description   string `json:"description,omitempty"`
	ReplaceAccess bool   `json:"replace_access,omitempty"`
}

// Connect godoc
// @Summary      接入现有备份仓库
// @Description  从现有 Task 派生只读访问并先探测后接入；不接受任意 Provider 路径或凭据
// @Tags         backup-repositories
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      backupRepositoryConnectRequest  true  "Task 派生接入请求"
// @Success      200   {object}  handlers.Response{data=backuprepository.ConnectResult}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      501   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /backup-repositories/connect [post]
func (handler *BackupRepositoryHandler) Connect(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("backup repository service unavailable"))
		return
	}
	var request backupRepositoryConnectRequest
	if err := decodeStrictBackupRepositoryJSON(c, &request); err != nil || request.TaskID == 0 ||
		(request.RepositoryID != "" && backupasset.ValidateOpaqueID(request.RepositoryID) != nil) ||
		len(strings.TrimSpace(request.DisplayName)) > 255 || len(request.Description) > 16<<10 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	requestContext := backupRepositoryRequestContext(c)
	result, err := handler.service.Connect(c.Request.Context(), backuprepository.ConnectRequest{
		TaskID: request.TaskID, RepositoryID: request.RepositoryID, DisplayName: strings.TrimSpace(request.DisplayName),
		Description: strings.TrimSpace(request.Description), ReplaceAccess: request.ReplaceAccess,
	}, requestContext)
	if err != nil {
		respondBackupRepositoryError(c, err, requestContext.CorrelationID)
		return
	}
	respondOK(c, result)
}

// List godoc
// @Summary      列出可见备份仓库
// @Description  按当前用户的实时 Task/Node 谱系过滤仓库和安全谱系摘要
// @Tags         backup-repositories
// @Security     Bearer
// @Produce      json
// @Param        limit   query     int     false  "每页数量"
// @Param        cursor  query     string  false  "签名游标"
// @Success      200     {object}  handlers.Response{data=backuprepository.RepositoryPage}
// @Failure      400     {object}  handlers.Response
// @Failure      401     {object}  handlers.Response
// @Failure      403     {object}  handlers.Response
// @Failure      503     {object}  handlers.Response
// @Router       /backup-repositories [get]
func (handler *BackupRepositoryHandler) List(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("backup repository service unavailable"))
		return
	}
	limit := 0
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			respondBadRequest(c, "分页参数不合法")
			return
		}
		limit = parsed
	}
	cursor := strings.TrimSpace(c.Query("cursor"))
	if len(cursor) > maxBackupRepositoryCursorBytes {
		respondBadRequest(c, "分页参数不合法")
		return
	}
	requestContext := backupRepositoryRequestContext(c)
	result, err := handler.service.List(c.Request.Context(), backuprepository.RepositoryListRequest{Limit: limit, Cursor: cursor}, backupRepositoryVisibilityScope(c), requestContext)
	if err != nil {
		respondBackupRepositoryError(c, err, requestContext.CorrelationID)
		return
	}
	respondOK(c, result)
}

// Detail godoc
// @Summary      查看备份仓库详情
// @Description  返回经过实时谱系授权和字段净化的仓库详情
// @Tags         backup-repositories
// @Security     Bearer
// @Produce      json
// @Param        id   path      string  true  "仓库 opaque ID"
// @Success      200  {object}  handlers.Response{data=backuprepository.RepositoryView}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /backup-repositories/{id} [get]
func (handler *BackupRepositoryHandler) Detail(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("backup repository service unavailable"))
		return
	}
	id, ok := backupRepositoryOpaqueID(c)
	if !ok {
		return
	}
	requestContext := backupRepositoryRequestContext(c)
	result, err := handler.service.Detail(c.Request.Context(), id, backupRepositoryVisibilityScope(c), requestContext)
	if err != nil {
		respondBackupRepositoryError(c, err, requestContext.CorrelationID)
		return
	}
	respondOK(c, result)
}

// Reconcile godoc
// @Summary      重新探测备份仓库
// @Description  使用当前加密绑定执行有界只读探测并原子刷新观察结果
// @Tags         backup-repositories
// @Security     Bearer
// @Produce      json
// @Param        id   path      string  true  "仓库 opaque ID"
// @Success      200  {object}  handlers.Response{data=backuprepository.ConnectResult}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      409  {object}  handlers.Response
// @Failure      501  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /backup-repositories/{id}/reconcile [post]
func (handler *BackupRepositoryHandler) Reconcile(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("backup repository service unavailable"))
		return
	}
	if !backupRepositoryEmptyBody(c) {
		respondBadRequest(c, "请求体必须为空")
		return
	}
	id, ok := backupRepositoryOpaqueID(c)
	if !ok {
		return
	}
	requestContext := backupRepositoryRequestContext(c)
	result, err := handler.service.Reconcile(c.Request.Context(), id, requestContext)
	if err != nil {
		respondBackupRepositoryError(c, err, requestContext.CorrelationID)
		return
	}
	respondOK(c, result)
}

// Disconnect godoc
// @Summary      撤销备份仓库访问
// @Description  仅撤销当前访问绑定并保留仓库、恢复点、目录和 Provider 数据
// @Tags         backup-repositories
// @Security     Bearer
// @Produce      json
// @Param        id   path      string  true  "仓库 opaque ID"
// @Success      200  {object}  handlers.Response{data=backuprepository.ConnectResult}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      409  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /backup-repositories/{id}/disconnect [post]
func (handler *BackupRepositoryHandler) Disconnect(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("backup repository service unavailable"))
		return
	}
	if !backupRepositoryEmptyBody(c) {
		respondBadRequest(c, "请求体必须为空")
		return
	}
	id, ok := backupRepositoryOpaqueID(c)
	if !ok {
		return
	}
	requestContext := backupRepositoryRequestContext(c)
	result, err := handler.service.Disconnect(c.Request.Context(), id, requestContext)
	if err != nil {
		respondBackupRepositoryError(c, err, requestContext.CorrelationID)
		return
	}
	respondOK(c, result)
}

type backupRepositoryPagePayload struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type backupRepositoryImportReviewPayload struct {
	Decision string `json:"decision"`
	AcceptAs string `json:"accept_as,omitempty"`
}

// ImportScan godoc
// @Summary      扫描可导入备份点
// @Description  Admin 发现可归因 Provider 点并写入待审候选；不接受路径或凭据
// @Tags         backup-repositories
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      string                         true  "仓库 opaque ID"
// @Param        body  body      backupRepositoryPagePayload    true  "分页"
// @Success      200   {object}  handlers.Response{data=backuprepository.ImportDiscoveryResult}
// @Failure      400   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /backup-repositories/{id}/import-scans [post]
func (handler *BackupRepositoryHandler) ImportScan(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("backup repository service unavailable"))
		return
	}
	id, ok := backupRepositoryOpaqueID(c)
	if !ok {
		return
	}
	var payload backupRepositoryPagePayload
	if err := decodeStrictBackupRepositoryJSON(c, &payload); err != nil || payload.Limit < 0 ||
		len(payload.Cursor) > maxBackupRepositoryCursorBytes || payload.Cursor != strings.TrimSpace(payload.Cursor) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	requestContext := backupRepositoryRequestContext(c)
	result, err := handler.service.DiscoverImportCandidates(c.Request.Context(), id, backuprepository.ImportDiscoveryRequest{
		Limit: payload.Limit, Cursor: payload.Cursor,
	}, requestContext)
	if err != nil {
		respondBackupRepositoryError(c, err, requestContext.CorrelationID)
		return
	}
	respondOK(c, result)
}

// ListImportCandidates godoc
// @Summary      列出导入候选
// @Description  Admin 列出仓库的待审/已审导入候选，不含 Provider locator
// @Tags         backup-repositories
// @Security     Bearer
// @Produce      json
// @Param        id      path      string  true   "仓库 opaque ID"
// @Param        limit   query     int     false  "每页数量"
// @Param        cursor  query     string  false  "签名游标"
// @Success      200     {object}  handlers.Response{data=backuprepository.ImportCandidatePage}
// @Failure      400     {object}  handlers.Response
// @Failure      404     {object}  handlers.Response
// @Failure      503     {object}  handlers.Response
// @Router       /backup-repositories/{id}/import-candidates [get]
func (handler *BackupRepositoryHandler) ListImportCandidates(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("backup repository service unavailable"))
		return
	}
	id, ok := backupRepositoryOpaqueID(c)
	if !ok {
		return
	}
	limit := 0
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			respondBadRequest(c, "分页参数不合法")
			return
		}
		limit = parsed
	}
	cursor := strings.TrimSpace(c.Query("cursor"))
	if len(cursor) > maxBackupRepositoryCursorBytes {
		respondBadRequest(c, "分页参数不合法")
		return
	}
	requestContext := backupRepositoryRequestContext(c)
	result, err := handler.service.ListImportCandidates(c.Request.Context(), id, backuprepository.ImportCandidateListRequest{
		Limit: limit, Cursor: cursor,
	}, backupRepositoryVisibilityScope(c), requestContext)
	if err != nil {
		respondBackupRepositoryError(c, err, requestContext.CorrelationID)
		return
	}
	respondOK(c, result)
}

// ReviewImportCandidate godoc
// @Summary      审核导入候选
// @Description  Admin 接受或拒绝精确候选；接受时仅允许封闭候选类型
// @Tags         backup-repositories
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id           path      string                                 true  "仓库 opaque ID"
// @Param        candidateId  path      string                                 true  "候选 opaque ID"
// @Param        body         body      backupRepositoryImportReviewPayload    true  "审核决定"
// @Success      200          {object}  handlers.Response{data=backuprepository.ImportCandidateView}
// @Failure      400          {object}  handlers.Response
// @Failure      404          {object}  handlers.Response
// @Failure      409          {object}  handlers.Response
// @Failure      503          {object}  handlers.Response
// @Router       /backup-repositories/{id}/import-candidates/{candidateId}/reviews [post]
func (handler *BackupRepositoryHandler) ReviewImportCandidate(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("backup repository service unavailable"))
		return
	}
	id, ok := backupRepositoryOpaqueID(c)
	if !ok {
		return
	}
	candidateID := strings.TrimSpace(c.Param("candidateId"))
	if backupasset.ValidateOpaqueID(candidateID) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	var payload backupRepositoryImportReviewPayload
	if decodeStrictBackupRepositoryJSON(c, &payload) != nil ||
		(payload.Decision != string(backupasset.ImportReviewAccepted) && payload.Decision != string(backupasset.ImportReviewRejected)) ||
		(payload.AcceptAs != "" && backupasset.ValidateImportCandidateKind(backupasset.ImportCandidateKind(payload.AcceptAs)) != nil) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	requestContext := backupRepositoryRequestContext(c)
	result, err := handler.service.ReviewImportCandidate(c.Request.Context(), id, candidateID, backuprepository.ImportReviewRequest{
		Decision: backupasset.ImportReviewState(payload.Decision), AcceptAs: backupasset.ImportCandidateKind(payload.AcceptAs),
	}, requestContext)
	if err != nil {
		respondBackupRepositoryError(c, err, requestContext.CorrelationID)
		return
	}
	respondOK(c, result)
}

// Rebuild godoc
// @Summary      重建已接受导入
// @Description  Admin 对已接受清单重建 Catalog 与可回填 derived 数据
// @Tags         backup-repositories
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      string                       true  "仓库 opaque ID"
// @Param        body  body      backupRepositoryPagePayload  true  "分页"
// @Success      200   {object}  handlers.Response{data=backuprepository.RebuildResult}
// @Failure      400   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /backup-repositories/{id}/rebuilds [post]
func (handler *BackupRepositoryHandler) Rebuild(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("backup repository service unavailable"))
		return
	}
	id, ok := backupRepositoryOpaqueID(c)
	if !ok {
		return
	}
	var payload backupRepositoryPagePayload
	if err := decodeStrictBackupRepositoryJSON(c, &payload); err != nil || payload.Limit < 0 ||
		len(payload.Cursor) > maxBackupRepositoryCursorBytes || payload.Cursor != strings.TrimSpace(payload.Cursor) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	requestContext := backupRepositoryRequestContext(c)
	result, err := handler.service.RebuildAcceptedImports(c.Request.Context(), id, backuprepository.RebuildRequest{
		Limit: payload.Limit, Cursor: payload.Cursor,
	}, requestContext)
	if err != nil {
		respondBackupRepositoryError(c, err, requestContext.CorrelationID)
		return
	}
	respondOK(c, result)
}

func decodeStrictBackupRepositoryJSON(c *gin.Context, target any) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return fmt.Errorf("request body missing")
	}
	payload, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBackupRepositoryRequestBytes+1))
	if err != nil || len(payload) > maxBackupRepositoryRequestBytes {
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

func backupRepositoryEmptyBody(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return true
	}
	payload, err := io.ReadAll(io.LimitReader(c.Request.Body, 1025))
	return err == nil && len(payload) <= 1024 && len(bytes.TrimSpace(payload)) == 0
}

func backupRepositoryOpaqueID(c *gin.Context) (string, bool) {
	id := strings.TrimSpace(c.Param("id"))
	if backupasset.ValidateOpaqueID(id) != nil {
		respondBadRequest(c, "仓库 ID 不合法")
		return "", false
	}
	return id, true
}

func backupRepositoryRequestContext(c *gin.Context) backuprepository.RequestContext {
	return backuprepository.RequestContext{
		Actor: backupasset.AuditActor{
			UserID: middleware.CurrentUserID(c), Username: c.GetString(middleware.CtxUsername), Role: middleware.CurrentRole(c),
		},
		CorrelationID: c.GetString(middleware.RequestIDKey),
	}
}

func backupRepositoryVisibilityScope(c *gin.Context) backuprepository.VisibilityScope {
	return backuprepository.VisibilityScope{Role: middleware.CurrentRole(c), UserID: middleware.CurrentUserID(c)}
}

func respondBackupRepositoryError(c *gin.Context, err error, correlationID string) {
	if reason, errorCorrelationID, ok := backuprepository.CapabilityFromError(err); ok {
		if errorCorrelationID != "" {
			correlationID = errorCorrelationID
		}
		status := http.StatusNotImplemented
		switch reason.Code {
		case backupasset.CapabilityFeatureDisabled, backupasset.CapabilityRepositoryOffline,
			backupasset.CapabilityRepositoryDisconnected, backupasset.CapabilityProviderUnavailable,
			backupasset.CapabilityProviderOperationTimeout, backupasset.CapabilityProviderResourceLimit:
			status = http.StatusServiceUnavailable
		}
		respondBackupCapabilityError(c, status, reason, correlationID)
		return
	}
	switch {
	case errors.Is(err, backupasset.ErrInvalidState), errors.Is(err, backupasset.ErrInvalidAssetRef):
		respondBadRequest(c, "请求参数不合法")
	case errors.Is(err, backupasset.ErrForbidden):
		respondForbidden(c, "权限不足")
	case errors.Is(err, backupasset.ErrNotFound):
		respondNotFound(c, "备份仓库不存在")
	case errors.Is(err, backupasset.ErrConflict):
		respondConflict(c, "备份仓库状态冲突")
	case errors.Is(err, context.DeadlineExceeded):
		respondBackupCapabilityError(c, http.StatusServiceUnavailable, backupasset.CapabilityReason{Code: backupasset.CapabilityProviderOperationTimeout}, correlationID)
	case errors.Is(err, backupasset.ErrCapabilityUnavailable):
		respondBackupCapabilityError(c, http.StatusNotImplemented, backupasset.CapabilityReason{Code: backupasset.CapabilityTaskArtifactContractMissing}, correlationID)
	default:
		respondInternalError(c, err)
	}
}
