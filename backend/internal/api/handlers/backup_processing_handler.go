package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

type BackupProcessingService interface {
	RequestProcessingPreview(context.Context, processing.PreviewJobRequest) (processing.PreviewJobResult, error)
	PollProcessingPreview(context.Context, processing.PreviewJobLookup) (processing.PreviewJobResult, error)
	CancelProcessingPreview(context.Context, processing.PreviewJobLookup) error
	GetProcessingState(context.Context, processing.PreviewStateRequest) (processing.AssetProcessingState, error)
}

type BackupProcessingHandler struct {
	service BackupProcessingService
	audit   BackupAssetAuditSink
}

type backupProcessingCreatePayload struct {
	SchemaVersion  int                              `json:"schema_version" minimum:"1" maximum:"1" example:"1"`
	Representation processing.PreviewRepresentation `json:"representation" enums:"thumbnail,text,document_pages,media_preview,archive_index"`
	Profile        string                           `json:"profile,omitempty"`
}

type backupProcessingCancelPayload struct {
	SchemaVersion int `json:"schema_version" minimum:"1" maximum:"1" example:"1"`
}

type backupProcessingCancelResult struct {
	SchemaVersion int  `json:"schema_version"`
	Canceled      bool `json:"canceled"`
}

func NewBackupProcessingHandler(service BackupProcessingService, audit BackupAssetAuditSink) *BackupProcessingHandler {
	return &BackupProcessingHandler{service: service, audit: audit}
}

// CreatePreview godoc
// @Summary      创建备份资产增强预览任务
// @Description  只接受 URI 中的精确 AssetRef 与闭合 representation/profile；queued 响应中的 job_id 是独立 processing-interest handle
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id       path  string                         true  "恢复点 opaque ID"
// @Param        entryId  path  string                         true  "Catalog entry opaque ID"
// @Param        body     body  backupProcessingCreatePayload true  "闭合增强预览请求"
// @Success      200  {object} handlers.Response{data=processing.PreviewJobResult}
// @Success      202  {object} handlers.Response{data=processing.PreviewJobResult}
// @Failure      400  {object} handlers.Response
// @Failure      401  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId}/preview-jobs [post]
func (handler *BackupProcessingHandler) CreatePreview(c *gin.Context) {
	ref, actor, ok := handler.requestIdentity(c, true)
	if !ok {
		return
	}
	var payload backupProcessingCreatePayload
	if decodeStrictBackupContentJSON(c, &payload) != nil || !validBackupProcessingCreate(payload) {
		handler.finishAudit(c, ref, backupasset.AuditOutcomeBlocked, "invalid_request")
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.RequestProcessingPreview(c.Request.Context(), processing.PreviewJobRequest{
		Actor: actor, Ref: ref, Representation: payload.Representation, Profile: payload.Profile,
	})
	if err != nil {
		handler.finishError(c, ref, err)
		return
	}
	if !validBackupProcessingResult(result, payload.Representation, "") {
		handler.finishError(c, ref, processing.ErrInvalidContract)
		return
	}
	handler.finishAudit(c, ref, backupasset.AuditOutcomeSuccess, "")
	if result.State == processing.ProcessingProductQueued {
		location := strings.TrimSuffix(c.Request.URL.Path, "/") + "/" + result.JobID
		c.Header("Location", location)
		c.Header("Retry-After", strconv.Itoa(result.PollAfterSeconds))
		respondAccepted(c, result)
		return
	}
	respondOK(c, result)
}

// PollPreview godoc
// @Summary      查询备份资产增强预览任务
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id       path  string true "恢复点 opaque ID"
// @Param        entryId  path  string true "Catalog entry opaque ID"
// @Param        jobId    path  string true "processing-interest opaque ID"
// @Success      200  {object} handlers.Response{data=processing.PreviewJobResult}
// @Failure      400  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId}/preview-jobs/{jobId} [get]
func (handler *BackupProcessingHandler) PollPreview(c *gin.Context) {
	ref, actor, ok := handler.requestIdentity(c, false)
	if !ok {
		return
	}
	jobID, ok := backupAssetOpaqueParam(c, "jobId")
	if !ok {
		handler.finishAudit(c, ref, backupasset.AuditOutcomeBlocked, "invalid_request")
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.PollProcessingPreview(c.Request.Context(), processing.PreviewJobLookup{
		Actor: actor, Ref: ref, JobID: jobID,
	})
	if err != nil {
		handler.finishError(c, ref, err)
		return
	}
	if !validBackupProcessingResult(result, "", jobID) {
		handler.finishError(c, ref, processing.ErrInvalidContract)
		return
	}
	handler.finishAudit(c, ref, backupasset.AuditOutcomeSuccess, "")
	respondOK(c, result)
}

// CancelPreview godoc
// @Summary      取消当前用户的增强预览 interest
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id       path  string true "恢复点 opaque ID"
// @Param        entryId  path  string true "Catalog entry opaque ID"
// @Param        jobId    path  string true "processing-interest opaque ID"
// @Param        body     body  backupProcessingCancelPayload true "固定版本取消请求"
// @Success      200  {object} handlers.Response{data=backupProcessingCancelResult}
// @Failure      400  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId}/preview-jobs/{jobId}/cancel [post]
func (handler *BackupProcessingHandler) CancelPreview(c *gin.Context) {
	ref, actor, ok := handler.requestIdentity(c, true)
	if !ok {
		return
	}
	jobID, ok := backupAssetOpaqueParam(c, "jobId")
	var payload backupProcessingCancelPayload
	if !ok || decodeStrictBackupContentJSON(c, &payload) != nil || payload.SchemaVersion != 1 {
		handler.finishAudit(c, ref, backupasset.AuditOutcomeBlocked, "invalid_request")
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if err := handler.service.CancelProcessingPreview(c.Request.Context(), processing.PreviewJobLookup{
		Actor: actor, Ref: ref, JobID: jobID,
	}); err != nil {
		handler.finishError(c, ref, err)
		return
	}
	handler.finishAudit(c, ref, backupasset.AuditOutcomeSuccess, "")
	respondOK(c, backupProcessingCancelResult{SchemaVersion: 1, Canceled: true})
}

// GetState godoc
// @Summary      查看精确备份资产的增强处理状态
// @Description  返回闭合 representation 状态，不创建任务且不暴露内部处理身份
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id       path  string true "恢复点 opaque ID"
// @Param        entryId  path  string true "Catalog entry opaque ID"
// @Success      200  {object} handlers.Response{data=processing.AssetProcessingState}
// @Failure      400  {object} handlers.Response
// @Failure      401  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId}/processing [get]
func (handler *BackupProcessingHandler) GetState(c *gin.Context) {
	ref, actor, ok := handler.requestIdentity(c, false)
	if !ok {
		return
	}
	result, err := handler.service.GetProcessingState(c.Request.Context(), processing.PreviewStateRequest{Actor: actor, Ref: ref})
	if err != nil {
		handler.finishError(c, ref, err)
		return
	}
	if !validAssetProcessingState(result) {
		handler.finishError(c, ref, processing.ErrInvalidContract)
		return
	}
	respondOK(c, result)
}

func (handler *BackupProcessingHandler) requestIdentity(
	c *gin.Context,
	withJSON bool,
) (backupasset.AssetRef, content.DeliveryActor, bool) {
	ref := backupasset.AssetRef{}
	if c == nil || c.Request == nil || c.Request.URL == nil || c.Request.URL.RawQuery != "" ||
		handler == nil || handler.service == nil {
		respondBadRequest(c, "请求参数不合法")
		return ref, content.DeliveryActor{}, false
	}
	if !withJSON && !emptyBackupWorkerAdminRequest(c.Request) {
		respondBadRequest(c, "请求不得包含查询参数或请求体")
		return ref, content.DeliveryActor{}, false
	}
	pointID, pointOK := backupAssetOpaqueParam(c, "id")
	entryID := strings.TrimSpace(c.Param("entryId"))
	if !pointOK || c.Param("entryId") != entryID || len(entryID) != 64 || !lowerHexAPI(entryID) {
		respondBadRequest(c, "请求参数不合法")
		return ref, content.DeliveryActor{}, false
	}
	ref = backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}
	actor := content.DeliveryActor{
		UserID: middleware.CurrentUserID(c), Username: c.GetString(middleware.CtxUsername), Role: middleware.CurrentRole(c),
	}
	if actor.UserID == 0 || (actor.Role != "admin" && actor.Role != "operator") {
		handler.finishAudit(c, ref, backupasset.AuditOutcomeBlocked, "forbidden")
		respondForbidden(c, "无权执行该操作")
		return ref, actor, false
	}
	return ref, actor, true
}

func validBackupProcessingCreate(payload backupProcessingCreatePayload) bool {
	if payload.SchemaVersion != 1 {
		return false
	}
	switch payload.Representation {
	case processing.PreviewThumbnail, processing.PreviewText, processing.PreviewDocumentPages,
		processing.PreviewMedia, processing.PreviewArchiveIndex:
	default:
		return false
	}
	if payload.Profile == "" {
		return true
	}
	switch payload.Profile {
	case capabilityspec.ProfileRasterThumbnailV1, capabilityspec.ProfileBoundedTextV1,
		capabilityspec.ProfileTesseractTextV1, capabilityspec.ProfileStaticPagesV1,
		capabilityspec.ProfileMediaProbeV1, capabilityspec.ProfileBrowserPreviewV1,
		capabilityspec.ProfileArchiveIndexV1:
		return true
	default:
		return false
	}
}

func validBackupProcessingResult(
	result processing.PreviewJobResult,
	expected processing.PreviewRepresentation,
	expectedJobID string,
) bool {
	if result.SchemaVersion != 1 || expected != "" && result.Representation != expected ||
		result.Representation == "" || len(result.FallbackActions) == 0 || len(result.FallbackActions) > 3 {
		return false
	}
	for _, action := range result.FallbackActions {
		if action != "native_preview" && action != "download" && action != "recovery" {
			return false
		}
	}
	switch result.State {
	case processing.ProcessingProductQueued:
		return backupasset.ValidateOpaqueID(result.JobID) == nil &&
			(expectedJobID == "" || result.JobID == expectedJobID) && result.PollAfterSeconds >= 1 &&
			result.PollAfterSeconds <= 30 && !result.Terminal
	case processing.ProcessingProductNative, processing.ProcessingProductDerived, processing.ProcessingProductPartial,
		processing.ProcessingProductUnsupported, processing.ProcessingProductNotDeployed, processing.ProcessingProductFailed:
		return (expectedJobID == "" && result.JobID == "" || expectedJobID != "" && result.JobID == expectedJobID) &&
			result.PollAfterSeconds == 0 && result.Terminal
	default:
		return false
	}
}

func validAssetProcessingState(value processing.AssetProcessingState) bool {
	want := []processing.PreviewRepresentation{
		processing.PreviewThumbnail, processing.PreviewText, processing.PreviewDocumentPages,
		processing.PreviewMedia, processing.PreviewArchiveIndex,
	}
	if value.SchemaVersion != 1 || len(value.Representations) != len(want) {
		return false
	}
	for index, item := range value.Representations {
		if !validBackupProcessingResult(item, want[index], "") || !item.Terminal {
			return false
		}
	}
	return true
}

func (handler *BackupProcessingHandler) finishError(c *gin.Context, ref backupasset.AssetRef, err error) {
	status := http.StatusServiceUnavailable
	outcome := backupasset.AuditOutcomeFailure
	code := "unavailable"
	switch {
	case errors.Is(err, processing.ErrProcessingDisabled), errors.Is(err, processing.ErrProcessingHandleNotFound),
		errors.Is(err, backupasset.ErrNotFound):
		status, outcome, code = http.StatusNotFound, backupasset.AuditOutcomeBlocked, "not_found"
	case errors.Is(err, backupasset.ErrForbidden):
		status, outcome, code = http.StatusForbidden, backupasset.AuditOutcomeBlocked, "forbidden"
	}
	handler.finishAudit(c, ref, outcome, code)
	switch status {
	case http.StatusBadRequest:
		respondBadRequest(c, "请求参数不合法")
	case http.StatusForbidden:
		respondForbidden(c, "无权执行该操作")
	case http.StatusNotFound:
		respondNotFound(c, "备份资产处理资源不存在")
	default:
		respondServiceUnavailable(c, "备份资产处理服务暂不可用")
	}
}

func (handler *BackupProcessingHandler) finishAudit(
	c *gin.Context,
	ref backupasset.AssetRef,
	outcome backupasset.AuditOutcome,
	code string,
) {
	if handler == nil || handler.audit == nil || c == nil || c.Request == nil {
		return
	}
	input := backupAssetAuditInput(c, backupasset.AuditActionPreviewJob)
	input.RecoveryPointID = ref.RecoveryPointID
	input.EntryID = ref.EntryID
	input.Outcome = outcome
	if code != "" {
		input.Fields[backupasset.AuditFieldCode] = code
	}
	if err := handler.audit.Write(c.Request.Context(), input); err != nil {
		logger.Module("backup_processing_handler").Warn().Msg("备份资产处理审计写入失败")
	}
}

func lowerHexAPI(value string) bool {
	for index := range len(value) {
		if value[index] < '0' || value[index] > '9' && value[index] < 'a' || value[index] > 'f' {
			return false
		}
	}
	return value != ""
}
