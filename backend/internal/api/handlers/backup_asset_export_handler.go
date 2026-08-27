package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const maxBackupAssetExportRequestBytes = 2 << 20

type BackupAssetExportService interface {
	Create(context.Context, assetexport.CreateRequest) (assetexport.CreateResult, error)
	Status(context.Context, assetexport.StatusRequest) (assetexport.JobStatus, error)
	Cancel(context.Context, assetexport.SelectionActor, string) (assetexport.JobStatus, error)
}

type BackupAssetExportDelivery interface {
	IssueExport(context.Context, assetexport.ExportDeliveryIssueRequest) (assetexport.IssuedDeliveryTicket, error)
}

type BackupAssetExportHandler struct {
	service      BackupAssetExportService
	delivery     BackupAssetExportDelivery
	db           *gorm.DB
	jwtManager   *auth.JWTManager
	audit        BackupAssetAuditSink
	configSource BackupContentHandlerConfigSource
	schemePolicy BackupContentSchemePolicy
}

type backupAssetExportSelectionPayload struct {
	SchemaVersion      int                       `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	Kind               assetexport.SelectionKind `json:"kind" binding:"required" enums:"explicit,saved_search"`
	Refs               []backupasset.AssetRef    `json:"refs,omitempty" validate:"min=1,max=100000"`
	SavedSearchID      string                    `json:"saved_search_id,omitempty" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	SavedSearchVersion int                       `json:"saved_search_version,omitempty" minimum:"1"`
}

type backupAssetExportCreatePayload struct {
	SchemaVersion  int                               `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	Selection      backupAssetExportSelectionPayload `json:"selection" binding:"required"`
	ArchiveFormat  assetexport.ArchiveFormat         `json:"archive_format" binding:"required" enums:"zip,tar"`
	ArchiveProfile string                            `json:"archive_profile" binding:"required" enums:"zip_deflate_v1,tar_none_v1,tar_gzip_v1"`
}

type backupAssetExportVersionPayload struct {
	SchemaVersion int `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
}

func NewBackupAssetExportHandler(
	service BackupAssetExportService,
	delivery BackupAssetExportDelivery,
	db *gorm.DB,
	jwtManager *auth.JWTManager,
	audit BackupAssetAuditSink,
	configSource BackupContentHandlerConfigSource,
) *BackupAssetExportHandler {
	return &BackupAssetExportHandler{
		service: service, delivery: delivery, db: db, jwtManager: jwtManager,
		audit: audit, configSource: configSource,
	}
}

func (handler *BackupAssetExportHandler) WithSchemePolicy(policy BackupContentSchemePolicy) *BackupAssetExportHandler {
	if handler != nil {
		handler.schemePolicy = policy
	}
	return handler
}

// Create godoc
// @Summary      创建备份资产导出任务
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        Idempotency-Key header string true "幂等键"
// @Param        X-Xirang-Step-Up header string true "asset.export_create proof"
// @Param        body body backupAssetExportCreatePayload true "冻结选择与归档配置"
// @Success      202 {object} handlers.Response{data=assetexport.CreateResult}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /asset-exports [post]
func (handler *BackupAssetExportHandler) Create(c *gin.Context) {
	actor, ok := backupAssetExportActor(c)
	if !ok || c.Request.URL == nil || c.Request.URL.RawQuery != "" {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if handler == nil {
		respondServiceUnavailable(c, "备份资产导出服务暂不可用")
		return
	}
	proofActor, proofActorOK := backupAssetExportDeliveryActor(c)
	if !proofActorOK {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if _, proofOK := handler.exactDeliveryProof(c, proofActor, auth.StepUpActionAssetExportCreate); !proofOK {
		return
	}
	idempotencyKey, ok := exactIdempotencyKey(c.Request)
	if !ok {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	var payload backupAssetExportCreatePayload
	if decodeStrictBackupAssetExportJSON(c, &payload) != nil || payload.SchemaVersion != 1 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request := assetexport.CreateRequest{
		Actor: actor,
		Selection: assetexport.CreateSelectionV1{
			SchemaVersion: payload.Selection.SchemaVersion, Kind: payload.Selection.Kind,
			Refs: payload.Selection.Refs, SavedSearchID: payload.Selection.SavedSearchID,
			SavedSearchVersion: payload.Selection.SavedSearchVersion,
		},
		IdempotencyKey: idempotencyKey, ArchiveFormat: payload.ArchiveFormat, ArchiveProfile: payload.ArchiveProfile,
	}
	if !assetexport.ValidArchiveProfilePair(request.ArchiveFormat, request.ArchiveProfile) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if assetexport.ValidateCreateSelection(request.Selection) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if handler.service == nil {
		respondNotFound(c, "导出任务不存在")
		return
	}
	result, err := handler.service.Create(c.Request.Context(), request)
	if err != nil {
		handler.writeExportAudit(c, backupasset.AuditActionExportCreate, assetexport.JobStatus{}, exportAuditError(err))
		respondBackupAssetExportError(c, err)
		return
	}
	if !validBackupAssetExportStatus(result.Job) {
		respondServiceUnavailable(c, "备份资产导出服务暂不可用")
		return
	}
	handler.writeExportAudit(c, backupasset.AuditActionExportCreate, result.Job, "")
	c.Header("Location", "/api/v1/asset-exports/"+result.Job.ID)
	respondAccepted(c, result)
}

// Status godoc
// @Summary      查询备份资产导出任务
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id path string true "导出任务 opaque ID"
// @Param        items_limit query int false "逐项状态页大小（最大 200）"
// @Param        items_cursor query string false "签名游标"
// @Success      200 {object} handlers.Response{data=assetexport.JobStatus}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /asset-exports/{id} [get]
func (handler *BackupAssetExportHandler) Status(c *gin.Context) {
	actor, ok := backupAssetExportActor(c)
	jobID, idOK := backupAssetOpaqueParam(c, "id")
	values, queryOK := backupAssetQuery(c, "items_limit", "items_cursor")
	limit, limitOK := backupAssetExportItemsLimit(values)
	cursor, cursorOK := backupAssetExportItemsCursor(values)
	if !ok || !idOK || !queryOK || !limitOK || !cursorOK || !emptyBackupAssetExportReadRequest(c.Request) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if handler == nil || handler.service == nil {
		respondNotFound(c, "导出任务不存在")
		return
	}
	status, err := handler.service.Status(c.Request.Context(), assetexport.StatusRequest{
		Actor: actor, JobID: jobID, ItemsCursor: cursor, ItemsLimit: limit,
	})
	if err != nil {
		respondBackupAssetExportError(c, err)
		return
	}
	if !validBackupAssetExportStatus(status) {
		respondServiceUnavailable(c, "备份资产导出服务暂不可用")
		return
	}
	respondOK(c, status)
}

func emptyBackupAssetExportReadRequest(request *http.Request) bool {
	if request == nil || request.URL == nil || request.ContentLength > 0 || len(request.TransferEncoding) != 0 {
		return false
	}
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	var probe [1]byte
	read, err := request.Body.Read(probe[:])
	return read == 0 && errors.Is(err, io.EOF)
}

// Cancel godoc
// @Summary      取消备份资产导出任务
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "导出任务 opaque ID"
// @Param        body body backupAssetExportVersionPayload true "固定版本取消请求"
// @Success      202 {object} handlers.Response{data=assetexport.JobStatus}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /asset-exports/{id}/cancel [post]
func (handler *BackupAssetExportHandler) Cancel(c *gin.Context) {
	actor, ok := backupAssetExportActor(c)
	jobID, idOK := backupAssetOpaqueParam(c, "id")
	var payload backupAssetExportVersionPayload
	if !ok || !idOK || c.Request.URL == nil || c.Request.URL.RawQuery != "" ||
		decodeStrictBackupAssetExportJSON(c, &payload) != nil || payload.SchemaVersion != 1 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if handler == nil || handler.service == nil {
		respondNotFound(c, "导出任务不存在")
		return
	}
	status, err := handler.service.Cancel(c.Request.Context(), actor, jobID)
	if err != nil {
		handler.writeExportAudit(c, backupasset.AuditActionExportCancel, assetexport.JobStatus{ID: jobID}, exportAuditError(err))
		respondBackupAssetExportError(c, err)
		return
	}
	if !validBackupAssetExportStatus(status) || status.ID != jobID {
		respondServiceUnavailable(c, "备份资产导出服务暂不可用")
		return
	}
	handler.writeExportAudit(c, backupasset.AuditActionExportCancel, status, "")
	respondAccepted(c, status)
}

// DownloadTicket godoc
// @Summary      创建备份资产导出下载票据
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "导出任务 opaque ID"
// @Param        X-Xirang-Step-Up header string true "asset.export_download proof"
// @Param        body body backupAssetExportVersionPayload true "固定版本票据请求"
// @Success      200 {object} handlers.Response{data=assetexport.DeliveryTicketDescriptor}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /asset-exports/{id}/download-ticket [post]
func (handler *BackupAssetExportHandler) DownloadTicket(c *gin.Context) {
	actor, ok := backupAssetExportDeliveryActor(c)
	jobID, idOK := backupAssetOpaqueParam(c, "id")
	var payload backupAssetExportVersionPayload
	if !ok || !idOK || c.Request.URL == nil || c.Request.URL.RawQuery != "" ||
		decodeStrictBackupAssetExportJSON(c, &payload) != nil || payload.SchemaVersion != 1 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	session, sessionOK := backupAssetExportSession(c, actor)
	if !sessionOK {
		respondUnauthorized(c, "会话无效")
		return
	}
	proof, proofOK := handler.exactDeliveryProof(c, actor, auth.StepUpActionAssetExportDownload)
	if !proofOK {
		return
	}
	secureCookie, secureOK := handler.secureCookie(c)
	if !secureOK {
		return
	}
	if handler == nil || handler.delivery == nil {
		respondNotFound(c, "导出任务不存在")
		return
	}
	ticket, err := handler.delivery.IssueExport(c.Request.Context(), assetexport.ExportDeliveryIssueRequest{
		Actor: actor, Session: session, ExportJobID: jobID, Proof: proof, SecureCookie: secureCookie,
	})
	if err != nil {
		respondBackupAssetExportError(c, err)
		return
	}
	if !validBackupAssetDeliveryTicket(ticket, secureCookie, backupAssetDeliveryExportArchive) {
		respondServiceUnavailable(c, "备份资产导出服务暂不可用")
		return
	}
	http.SetCookie(c.Writer, ticket.Cookie)
	respondOK(c, ticket.Descriptor)
}

func backupAssetExportActor(c *gin.Context) (assetexport.SelectionActor, bool) {
	actor := assetexport.SelectionActor{UserID: middleware.CurrentUserID(c), Role: middleware.CurrentRole(c)}
	return actor, actor.UserID != 0 && actor.Role == "admin"
}

func backupAssetExportDeliveryActor(c *gin.Context) (content.DeliveryActor, bool) {
	actor := content.DeliveryActor{
		UserID: middleware.CurrentUserID(c), Username: c.GetString(middleware.CtxUsername), Role: middleware.CurrentRole(c),
	}
	return actor, actor.UserID != 0 && actor.Role == "admin"
}

func backupAssetExportSession(c *gin.Context, actor content.DeliveryActor) (content.DeliverySession, bool) {
	binding, exists := middleware.CurrentSessionBinding(c)
	if !exists || binding.UserID != actor.UserID || binding.Role != actor.Role ||
		backupasset.ValidateOpaqueID(binding.JTI) != nil || !binding.ExpiresAt.After(time.Now()) {
		return content.DeliverySession{}, false
	}
	return content.DeliverySession{
		JTI: binding.JTI, UserID: binding.UserID, Role: binding.Role,
		TokenVersion: binding.TokenVersion, ExpiresAt: binding.ExpiresAt.UTC(),
	}, true
}

func (handler *BackupAssetExportHandler) exactDeliveryProof(
	c *gin.Context,
	actor content.DeliveryActor,
	expected auth.StepUpAction,
) (content.StepUpProof, bool) {
	claims, err := validateStepUpProof(
		handler.db, handler.jwtManager, strings.TrimSpace(c.GetHeader(StepUpHeaderName)), actor.UserID, actor.Role, expected,
	)
	if err != nil || claims == nil || claims.ExpiresAt == nil || backupasset.ValidateOpaqueID(claims.ID) != nil {
		if errors.Is(err, ErrStepUpVerifierUnavailable) {
			respondServiceUnavailable(c, "二次验证服务暂不可用")
		} else {
			respondStepUpRequired(c)
		}
		return content.StepUpProof{}, false
	}
	return content.StepUpProof{Action: claims.StepUpAction, ID: claims.ID, ExpiresAt: claims.ExpiresAt.UTC()}, true
}

func (handler *BackupAssetExportHandler) secureCookie(c *gin.Context) (bool, bool) {
	if handler == nil || handler.configSource == nil {
		respondServiceUnavailable(c, "备份资产导出服务暂不可用")
		return false, false
	}
	config, err := handler.configSource(c.Request.Context())
	if err != nil || config.TicketTimeout < time.Second || config.TicketTimeout > 25*time.Second {
		respondServiceUnavailable(c, "备份资产导出服务暂不可用")
		return false, false
	}
	secure, err := handler.schemePolicy.SecureCookie(c.Request, config.transportOptions())
	if err != nil {
		respondBackupContentSecureTransportRequired(c)
		return false, false
	}
	return secure, true
}

func exactIdempotencyKey(request *http.Request) (string, bool) {
	if request == nil {
		return "", false
	}
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", false
	}
	value := strings.TrimSpace(values[0])
	return value, value == values[0] && len(value) >= assetexport.MinIdempotencyKeyBytes && len(value) <= assetexport.MaxIdempotencyKeyBytes
}

func decodeStrictBackupAssetExportJSON(c *gin.Context, target any) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return io.ErrUnexpectedEOF
	}
	payload, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBackupAssetExportRequestBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxBackupAssetExportRequestBytes || rejectDuplicateBackupContentJSON(payload) != nil {
		return io.ErrUnexpectedEOF
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func backupAssetExportItemsLimit(values map[string][]string) (int, bool) {
	items, present := values["items_limit"]
	if !present {
		return 100, true
	}
	if len(items) != 1 || items[0] == "" || items[0] != strings.TrimSpace(items[0]) {
		return 0, false
	}
	limit, err := strconv.Atoi(items[0])
	return limit, err == nil && limit > 0 && limit <= 200
}

func backupAssetExportItemsCursor(values map[string][]string) (string, bool) {
	items, present := values["items_cursor"]
	if !present {
		return "", true
	}
	if len(items) != 1 {
		return "", false
	}
	value := items[0]
	return value, value == strings.TrimSpace(value) && len(value) <= 4096
}

func validBackupAssetExportStatus(status assetexport.JobStatus) bool {
	if status.SchemaVersion != 1 || backupasset.ValidateOpaqueID(status.ID) != nil ||
		len(status.SelectionDigest) != 64 || status.ItemCount < 0 || status.LogicalBytes < 0 ||
		status.ProviderBytes < 0 || status.ArtifactBytes < 0 || status.AbsoluteDeadline.Location() != time.UTC ||
		status.Items == nil || !assetexport.ValidArchiveProfilePair(status.ArchiveFormat, status.ArchiveProfile) {
		return false
	}
	switch status.ExecutionState {
	case assetexport.ExecutionQueued, assetexport.ExecutionRunning, assetexport.ExecutionRetryWait,
		assetexport.ExecutionSealing, assetexport.ExecutionCancelRequested, assetexport.ExecutionExpiring:
		return status.PollAfterSeconds > 0 && status.PollAfterSeconds <= 300
	case assetexport.ExecutionReady, assetexport.ExecutionFailed, assetexport.ExecutionSourceExpired,
		assetexport.ExecutionCanceled, assetexport.ExecutionExpired:
		return status.PollAfterSeconds == 0
	default:
		return false
	}
}

type backupAssetDeliveryProduct uint8

const (
	backupAssetDeliveryExportArchive backupAssetDeliveryProduct = iota + 1
	backupAssetDeliveryArchiveMember
)

func validBackupAssetDeliveryTicket(
	ticket assetexport.IssuedDeliveryTicket,
	secure bool,
	product backupAssetDeliveryProduct,
) bool {
	descriptor := ticket.Descriptor
	cookie := ticket.Cookie
	now := time.Now().UTC()
	if cookie == nil || descriptor.SchemaVersion != 1 || descriptor.ContentLength < 0 ||
		cookie.Name != content.DeliveryCookieName || cookie.Domain != "" || !cookie.HttpOnly ||
		cookie.Secure != secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 0 ||
		cookie.Partitioned || cookie.Path != descriptor.ContentURL || !cookie.Expires.Equal(descriptor.ExpiresAt) ||
		descriptor.ExpiresAt.IsZero() || descriptor.IdleExpiresAt.IsZero() ||
		descriptor.ExpiresAt.Location() != time.UTC || descriptor.IdleExpiresAt.Location() != time.UTC ||
		!descriptor.ExpiresAt.After(now) || !descriptor.IdleExpiresAt.After(now) ||
		descriptor.IdleExpiresAt.After(descriptor.ExpiresAt) ||
		!validBackupAssetDeliveryPath(descriptor.ContentURL) || !validBackupAssetDeliveryETag(descriptor.ETag) {
		return false
	}
	if _, err := content.ParseDeliveryCookie(cookie.Name + "=" + cookie.Value); err != nil {
		return false
	}
	switch product {
	case backupAssetDeliveryExportArchive:
		return descriptor.Range == content.RangeSingle && validBackupAssetExportContentType(descriptor.ContentType)
	case backupAssetDeliveryArchiveMember:
		return descriptor.Range == content.RangeNone && validBackupAssetArchiveMemberContentType(descriptor.ContentType)
	default:
		return false
	}
}

func validBackupAssetDeliveryPath(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" || value != parsed.Path {
		return false
	}
	const prefix = "/api/v1/asset-content/"
	deliveryID := strings.TrimPrefix(parsed.Path, prefix)
	return parsed.Path == prefix+deliveryID && backupasset.ValidateOpaqueID(deliveryID) == nil
}

func validBackupAssetDeliveryETag(value string) bool {
	if len(value) != 66 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for _, value := range value[1 : len(value)-1] {
		if (value < '0' || value > '9') && (value < 'a' || value > 'f') {
			return false
		}
	}
	return true
}

func validBackupAssetExportContentType(value string) bool {
	switch value {
	case "application/zip", "application/x-tar", "application/gzip":
		return true
	default:
		return false
	}
}

func validBackupAssetArchiveMemberContentType(value string) bool {
	switch value {
	case "text/plain", "image/png", "image/jpeg", "application/pdf", "application/octet-stream":
		return true
	default:
		return false
	}
}

func respondBackupAssetExportError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, assetexport.ErrNotFound), errors.Is(err, backupasset.ErrNotFound), errors.Is(err, backupasset.ErrForbidden):
		respondNotFound(c, "导出任务不存在")
	case errors.Is(err, assetexport.ErrInvalidSelection), errors.Is(err, assetexport.ErrInvalidIdempotency),
		errors.Is(err, assetexport.ErrSelectionLimit), errors.Is(err, assetexport.ErrArchiveLimit),
		errors.Is(err, assetexport.ErrDeadlineUnsafe), errors.Is(err, assetexport.ErrInvalidDeliveryRequest):
		respondBadRequest(c, "请求参数不合法")
	case errors.Is(err, assetexport.ErrConflict), errors.Is(err, assetexport.ErrInvalidTransition), errors.Is(err, assetexport.ErrQuotaExceeded):
		respondConflict(c, "导出任务状态冲突")
	case errors.Is(err, assetexport.ErrUnavailable):
		respondServiceUnavailable(c, "备份资产导出服务暂不可用")
	default:
		respondInternalError(c, err)
	}
}

func exportAuditError(err error) string {
	switch {
	case errors.Is(err, assetexport.ErrNotFound), errors.Is(err, backupasset.ErrForbidden):
		return "not_found"
	case errors.Is(err, assetexport.ErrQuotaExceeded):
		return "quota_exceeded"
	case errors.Is(err, assetexport.ErrInvalidSelection), errors.Is(err, assetexport.ErrInvalidIdempotency):
		return "invalid_request"
	case err != nil:
		return "unavailable"
	default:
		return ""
	}
}

func (handler *BackupAssetExportHandler) writeExportAudit(
	c *gin.Context,
	action backupasset.AuditAction,
	status assetexport.JobStatus,
	errorCategory string,
) {
	if handler == nil || handler.audit == nil || c == nil || c.Request == nil {
		return
	}
	input := backupAssetAuditInput(c, action)
	input.ExportJobID = status.ID
	input.ItemCount = status.ItemCount
	input.ByteCount = status.LogicalBytes
	input.Outcome = backupasset.AuditOutcomeSuccess
	if status.SelectionDigest != "" {
		input.Fields[backupasset.AuditFieldSource] = status.SelectionDigest
	}
	if status.ArchiveFormat != "" {
		input.Fields[backupasset.AuditFieldFormat] = string(status.ArchiveFormat)
	}
	if errorCategory != "" {
		input.Outcome = backupasset.AuditOutcomeBlocked
		input.FailureCode = errorCategory
		input.Fields[backupasset.AuditFieldCode] = errorCategory
	}
	if err := handler.audit.Write(c.Request.Context(), input); err != nil {
		logger.Module("backup_asset_export_handler").Warn().Msg("备份资产导出审计写入失败")
	}
}
