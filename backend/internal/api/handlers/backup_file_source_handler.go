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
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/logger"

	"github.com/gin-gonic/gin"
)

// BackupFileSourceService is a metadata-only Catalog projection boundary. It
// deliberately exposes no Provider, content, proof, or delivery capability.
type BackupFileSourceService interface {
	ListFileSourceNodes(context.Context, catalog.AuthorizationScope, catalog.FileSourcePageRequest) (catalog.FileSourceNodePage, error)
	ListFileSourceBackupSets(context.Context, uint, catalog.AuthorizationScope, catalog.FileSourcePageRequest) (catalog.FileSourceBackupSetPage, error)
	ListFileSourceVersions(context.Context, string, catalog.AuthorizationScope, catalog.FileSourcePageRequest) (catalog.FileSourceVersionPage, error)
	ResolveFileSourceRecoveryPoint(context.Context, string, catalog.AuthorizationScope) (catalog.FileSourceRecoveryPointDTO, error)
	DiffFileSource(context.Context, string, catalog.AuthorizationScope, catalog.FileSourceDiffRequest) (catalog.FileSourceDiffPage, error)
}

type featureDisabledBackupFileSourceService struct{}

func NewFeatureDisabledBackupFileSourceService() BackupFileSourceService {
	return featureDisabledBackupFileSourceService{}
}

func (featureDisabledBackupFileSourceService) ListFileSourceNodes(context.Context, catalog.AuthorizationScope, catalog.FileSourcePageRequest) (catalog.FileSourceNodePage, error) {
	return catalog.FileSourceNodePage{}, catalog.ErrFeatureDisabled
}

func (featureDisabledBackupFileSourceService) ListFileSourceBackupSets(context.Context, uint, catalog.AuthorizationScope, catalog.FileSourcePageRequest) (catalog.FileSourceBackupSetPage, error) {
	return catalog.FileSourceBackupSetPage{}, catalog.ErrFeatureDisabled
}

func (featureDisabledBackupFileSourceService) ListFileSourceVersions(context.Context, string, catalog.AuthorizationScope, catalog.FileSourcePageRequest) (catalog.FileSourceVersionPage, error) {
	return catalog.FileSourceVersionPage{}, catalog.ErrFeatureDisabled
}

func (featureDisabledBackupFileSourceService) ResolveFileSourceRecoveryPoint(context.Context, string, catalog.AuthorizationScope) (catalog.FileSourceRecoveryPointDTO, error) {
	return catalog.FileSourceRecoveryPointDTO{}, catalog.ErrFeatureDisabled
}

func (featureDisabledBackupFileSourceService) DiffFileSource(context.Context, string, catalog.AuthorizationScope, catalog.FileSourceDiffRequest) (catalog.FileSourceDiffPage, error) {
	return catalog.FileSourceDiffPage{}, catalog.ErrFeatureDisabled
}

type BackupFileSourceHandler struct {
	service BackupFileSourceService
	audit   BackupAssetAuditSink
}

func NewBackupFileSourceHandler(service BackupFileSourceService, audit BackupAssetAuditSink) *BackupFileSourceHandler {
	return &BackupFileSourceHandler{service: service, audit: audit}
}

// ListNodes godoc
// @Summary      列出可浏览备份文件的节点
// @Description  按当前权限返回安全的节点级文件来源投影；不访问 Provider
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        limit   query     int     false  "每页数量（最大 200）"
// @Param        cursor  query     string  false  "签名游标"
// @Success      200     {object}  handlers.Response{data=catalog.FileSourceNodePage}
// @Failure      400     {object}  handlers.Response
// @Failure      401     {object}  handlers.Response
// @Failure      403     {object}  handlers.Response
// @Failure      409     {object}  handlers.Response
// @Failure      500     {object}  handlers.Response
// @Failure      503     {object}  handlers.Response
// @Router       /backup-file-sources/nodes [get]
func (handler *BackupFileSourceHandler) ListNodes(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionAssetList)
	request, ok := backupFileSourcePageRequest(c)
	if !ok {
		handler.reject(c, audit)
		return
	}
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.ListFileSourceNodes(c.Request.Context(), backupAssetAuthorizationScope(c), request)
	audit.ItemCount = int64(len(result.Items))
	handler.finish(c, audit, result, err)
}

// ListBackupSets godoc
// @Summary      列出节点的备份集
// @Description  按 producing task 或权威导入 lineage 返回隔离的备份集；不访问 Provider
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        nodeId  path      int     true   "节点 ID"
// @Param        limit   query     int     false  "每页数量（最大 200）"
// @Param        cursor  query     string  false  "签名游标"
// @Success      200     {object}  handlers.Response{data=catalog.FileSourceBackupSetPage}
// @Failure      400     {object}  handlers.Response
// @Failure      401     {object}  handlers.Response
// @Failure      403     {object}  handlers.Response
// @Failure      404     {object}  handlers.Response
// @Failure      409     {object}  handlers.Response
// @Failure      500     {object}  handlers.Response
// @Failure      503     {object}  handlers.Response
// @Router       /backup-file-sources/nodes/{nodeId}/sets [get]
func (handler *BackupFileSourceHandler) ListBackupSets(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionAssetList)
	nodeID, ok := backupFileSourceNodeParam(c, "nodeId")
	if !ok {
		handler.reject(c, audit)
		return
	}
	request, ok := backupFileSourcePageRequest(c)
	if !ok {
		handler.reject(c, audit)
		return
	}
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.ListFileSourceBackupSets(c.Request.Context(), nodeID, backupAssetAuthorizationScope(c), request)
	audit.ItemCount = int64(len(result.Items))
	handler.finish(c, audit, result, err)
}

// ListVersions godoc
// @Summary      列出备份集版本
// @Description  返回备份集内已授权恢复点的安全版本事实；不访问 Provider 或内容
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        backupSetId  path      string  true   "备份集 opaque ID"
// @Param        limit        query     int     false  "每页数量（最大 200）"
// @Param        cursor       query     string  false  "签名游标"
// @Success      200          {object}  handlers.Response{data=catalog.FileSourceVersionPage}
// @Failure      400          {object}  handlers.Response
// @Failure      401          {object}  handlers.Response
// @Failure      403          {object}  handlers.Response
// @Failure      404          {object}  handlers.Response
// @Failure      409          {object}  handlers.Response
// @Failure      500          {object}  handlers.Response
// @Failure      503          {object}  handlers.Response
// @Router       /backup-file-sources/sets/{backupSetId}/versions [get]
func (handler *BackupFileSourceHandler) ListVersions(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionAssetList)
	backupSetID, ok := backupAssetOpaqueParam(c, "backupSetId")
	if !ok {
		handler.reject(c, audit)
		return
	}
	request, ok := backupFileSourcePageRequest(c)
	if !ok {
		handler.reject(c, audit)
		return
	}
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.ListFileSourceVersions(c.Request.Context(), backupSetID, backupAssetAuthorizationScope(c), request)
	audit.ItemCount = int64(len(result.Items))
	handler.finish(c, audit, result, err)
}

// ResolveRecoveryPointSource godoc
// @Summary      解析恢复点的精确备份文件来源
// @Description  以当前权限将一个 opaque 恢复点解析为脱敏的节点、备份集、仓库和任务坐标；不访问 Provider 或内容
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        recoveryPointId  path      string  true  "恢复点 opaque ID"
// @Success      200              {object}  handlers.Response{data=catalog.FileSourceRecoveryPointDTO}
// @Failure      400              {object}  handlers.Response
// @Failure      401              {object}  handlers.Response
// @Failure      403              {object}  handlers.Response
// @Failure      404              {object}  handlers.Response
// @Failure      500              {object}  handlers.Response
// @Failure      503              {object}  handlers.Response
// @Router       /backup-file-sources/recovery-points/{recoveryPointId}/source [get]
func (handler *BackupFileSourceHandler) ResolveRecoveryPointSource(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionAssetList)
	recoveryPointID, ok := backupAssetOpaqueParam(c, "recoveryPointId")
	if !ok {
		handler.reject(c, audit)
		return
	}
	if _, ok := backupAssetQuery(c); !ok {
		handler.reject(c, audit)
		return
	}
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.ResolveFileSourceRecoveryPoint(c.Request.Context(), recoveryPointID, backupAssetAuthorizationScope(c))
	if err == nil {
		audit.ItemCount = 1
	}
	handler.finish(c, audit, result, err)
}

// Diff godoc
// @Summary      比较同一备份集的两个历史恢复点
// @Description  使用已授权的完整 Catalog 比较元数据，不访问 Provider 或内容
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        backupSetId  path  string  true  "备份集 opaque ID"
// @Param        body  body  catalog.FileSourceDiffRequest  true  "精确恢复点比较请求"
// @Success      200  {object}  handlers.Response{data=catalog.FileSourceDiffPage}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      409  {object}  handlers.Response
// @Failure      500  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /backup-file-sources/sets/{backupSetId}/diffs [post]
func (handler *BackupFileSourceHandler) Diff(c *gin.Context) {
	audit := backupAssetAuditInput(c, backupasset.AuditActionRecoveryPointDiff)
	setID, ok := backupAssetOpaqueParam(c, "backupSetId")
	if !ok {
		handler.reject(c, audit)
		return
	}
	if _, ok := backupAssetQuery(c); !ok {
		handler.reject(c, audit)
		return
	}
	var request catalog.FileSourceDiffRequest
	if decodeFileSourceDiffJSON(c, &request) != nil ||
		backupasset.ValidateOpaqueID(request.BaseRecoveryPointID) != nil || backupasset.ValidateOpaqueID(request.CompareRecoveryPointID) != nil ||
		request.BaseRecoveryPointID == request.CompareRecoveryPointID || request.Limit < 0 || request.Limit > maxBackupAssetPageLimit ||
		len(request.Cursor) > maxBackupAssetCursorBytes || request.Cursor != strings.TrimSpace(request.Cursor) {
		handler.reject(c, audit)
		return
	}
	if !handler.available(c, audit) {
		return
	}
	result, err := handler.service.DiffFileSource(c.Request.Context(), setID, backupAssetAuthorizationScope(c), request)
	audit.ItemCount = int64(len(result.Items))
	handler.finish(c, audit, result, err)
}

func decodeFileSourceDiffJSON(c *gin.Context, request *catalog.FileSourceDiffRequest) error {
	if c.Request == nil || c.Request.Body == nil {
		return fmt.Errorf("missing comparison body")
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBackupAssetRequestBytes+1))
	if err != nil || len(body) > maxBackupAssetRequestBytes {
		return fmt.Errorf("invalid comparison body")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("invalid comparison object")
	}
	seen := make(map[string]bool, 4)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return fmt.Errorf("invalid comparison field")
		}
		name, ok := token.(string)
		if !ok || seen[name] || (name != "base_recovery_point_id" && name != "compare_recovery_point_id" && name != "limit" && name != "cursor") {
			return fmt.Errorf("invalid comparison field")
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("invalid comparison value")
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return fmt.Errorf("invalid comparison object")
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing comparison data")
	}
	if err := json.Unmarshal(body, request); err != nil {
		return err
	}
	if seen["limit"] && request.Limit <= 0 {
		return fmt.Errorf("invalid comparison limit")
	}
	return nil
}

func backupFileSourcePageRequest(c *gin.Context) (catalog.FileSourcePageRequest, bool) {
	values, ok := backupAssetQuery(c, "limit", "cursor")
	if !ok {
		return catalog.FileSourcePageRequest{}, false
	}
	limit, ok := backupAssetLimit(values)
	if !ok {
		return catalog.FileSourcePageRequest{}, false
	}
	cursor, ok := backupAssetCursor(values)
	if !ok {
		return catalog.FileSourcePageRequest{}, false
	}
	return catalog.FileSourcePageRequest{Limit: limit, Cursor: cursor}, true
}

func backupFileSourceNodeParam(c *gin.Context, name string) (uint, bool) {
	raw := c.Param(name)
	if raw == "" || raw != strings.TrimSpace(raw) {
		return 0, false
	}
	value, err := strconv.ParseUint(raw, 10, strconv.IntSize)
	if err != nil || value == 0 || strconv.FormatUint(value, 10) != raw {
		return 0, false
	}
	return uint(value), true
}

func (handler *BackupFileSourceHandler) available(c *gin.Context, audit backupasset.AuditEventInput) bool {
	if handler != nil && handler.service != nil {
		return true
	}
	audit.Outcome = backupasset.AuditOutcomeFailure
	audit.FailureCode = "service_unavailable"
	audit.Fields[backupasset.AuditFieldStatus] = "unavailable"
	handler.writeAudit(c, audit)
	respondInternalError(c, fmt.Errorf("backup file-source service unavailable"))
	return false
}

func (handler *BackupFileSourceHandler) reject(c *gin.Context, audit backupasset.AuditEventInput) {
	audit.Outcome = backupasset.AuditOutcomeBlocked
	audit.FailureCode = "invalid_request"
	audit.Fields[backupasset.AuditFieldStatus] = "blocked"
	audit.Fields[backupasset.AuditFieldCode] = "invalid_request"
	handler.writeAudit(c, audit)
	respondBadRequest(c, "请求参数不合法")
}

func (handler *BackupFileSourceHandler) finish(c *gin.Context, audit backupasset.AuditEventInput, result any, err error) {
	if err == nil {
		audit.Outcome = backupasset.AuditOutcomeSuccess
		audit.Fields[backupasset.AuditFieldStatus] = "success"
		handler.writeAudit(c, audit)
		respondOK(c, result)
		return
	}
	status, code := backupFileSourceErrorStatus(err)
	if status >= 400 && status < 500 {
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

func backupFileSourceErrorStatus(err error) (int, string) {
	if errors.Is(err, backupasset.ErrInvalidState) || errors.Is(err, catalog.ErrInvalidCatalogContract) {
		return http.StatusInternalServerError, "internal_error"
	}
	return backupAssetErrorStatus(err)
}

func (handler *BackupFileSourceHandler) writeAudit(c *gin.Context, input backupasset.AuditEventInput) {
	if handler == nil || handler.audit == nil {
		return
	}
	if err := handler.audit.Write(c.Request.Context(), input); err != nil {
		logger.Module("backup_file_source_handler").Warn().Str("action", string(input.Action)).Msg("备份文件来源审计写入失败")
	}
}
