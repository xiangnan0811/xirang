package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type BackupArchiveService interface {
	ListIndex(context.Context, processing.ArchiveMemberIndexLookup) (processing.ArchiveMemberIndexView, error)
	Create(context.Context, processing.ArchiveMemberCreateRequest) (processing.ArchiveMemberCreateResult, error)
	Reconcile(context.Context, string) error
	Poll(context.Context, processing.ArchiveMemberLookup) (processing.ArchiveMemberStatusResult, error)
	Cancel(context.Context, processing.ArchiveMemberLookup) error
	AuthorizeReadyDelivery(context.Context, processing.ArchiveMemberLookup) (content.AuthorizedAsset, error)
}

type BackupArchiveDelivery interface {
	IssueArchiveMember(context.Context, assetexport.ArchiveMemberDeliveryIssueRequest) (assetexport.IssuedDeliveryTicket, error)
}

type BackupArchiveHandler struct {
	service      BackupArchiveService
	delivery     BackupArchiveDelivery
	db           *gorm.DB
	jwtManager   *auth.JWTManager
	audit        BackupAssetAuditSink
	configSource BackupContentHandlerConfigSource
	schemePolicy BackupContentSchemePolicy
}

type backupArchiveMemberCreatePayload struct {
	SchemaVersion int      `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	IndexRevision string   `json:"index_revision" binding:"required" minLength:"64" maxLength:"64" extensions:"x-pattern=^[0-9a-f]{64}$"`
	MemberChain   []string `json:"member_chain" binding:"required" validate:"min=1,max=1" minLength:"32" maxLength:"32" format:"lowercase-hex-32"`
}

type backupArchiveMemberBoundPayload struct {
	SchemaVersion int    `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	IndexRevision string `json:"index_revision" binding:"required" minLength:"64" maxLength:"64" extensions:"x-pattern=^[0-9a-f]{64}$"`
}

func NewBackupArchiveHandler(
	service BackupArchiveService,
	delivery BackupArchiveDelivery,
	db *gorm.DB,
	jwtManager *auth.JWTManager,
	audit BackupAssetAuditSink,
	configSource BackupContentHandlerConfigSource,
) *BackupArchiveHandler {
	return &BackupArchiveHandler{
		service: service, delivery: delivery, db: db, jwtManager: jwtManager,
		audit: audit, configSource: configSource,
	}
}

func (handler *BackupArchiveHandler) WithSchemePolicy(policy BackupContentSchemePolicy) *BackupArchiveHandler {
	if handler != nil {
		handler.schemePolicy = policy
	}
	return handler
}

// List godoc
// @Summary      读取安全归档成员索引
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id path string true "恢复点 opaque ID"
// @Param        entryId path string true "Catalog entry opaque ID"
// @Success      200 {object} handlers.Response{data=processing.ArchiveMemberIndexView}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId}/archive-members [get]
func (handler *BackupArchiveHandler) List(c *gin.Context) {
	actor, ref, ok := backupArchiveIdentity(c)
	if !ok || !emptyBackupWorkerAdminRequest(c.Request) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if handler == nil || handler.service == nil {
		respondNotFound(c, "归档资源不存在")
		return
	}
	view, err := handler.service.ListIndex(c.Request.Context(), processing.ArchiveMemberIndexLookup{Actor: actor, Ref: ref})
	if err != nil {
		handler.writeArchiveAudit(c, backupasset.AuditActionArchiveInspect, ref, "inspect", "", archiveAuditError(err))
		respondBackupArchiveError(c, err)
		return
	}
	handler.writeArchiveAudit(c, backupasset.AuditActionArchiveInspect, ref, "inspect", view.IndexRevision, "")
	respondOK(c, view)
}

// Create godoc
// @Summary      创建归档成员提取任务
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复点 opaque ID"
// @Param        entryId path string true "Catalog entry opaque ID"
// @Param        Idempotency-Key header string true "幂等键"
// @Param        body body backupArchiveMemberCreatePayload true "单跳归档成员请求"
// @Success      202 {object} handlers.Response{data=processing.ArchiveMemberCreateResult}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId}/archive-member-jobs [post]
func (handler *BackupArchiveHandler) Create(c *gin.Context) {
	actor, ref, ok := backupArchiveIdentity(c)
	idempotencyKey, keyOK := exactIdempotencyKey(c.Request)
	var payload backupArchiveMemberCreatePayload
	if !ok || !keyOK || c.Request.URL == nil || c.Request.URL.RawQuery != "" ||
		decodeStrictBackupContentJSON(c, &payload) != nil || payload.SchemaVersion != 1 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if handler == nil || handler.service == nil {
		respondNotFound(c, "归档资源不存在")
		return
	}
	memberDigest := backupArchiveMemberDigest(ref, payload.IndexRevision, payload.MemberChain)
	result, err := handler.service.Create(c.Request.Context(), processing.ArchiveMemberCreateRequest{
		Actor: actor, Ref: ref, IdempotencyKey: idempotencyKey,
		IndexRevision: payload.IndexRevision, MemberChain: payload.MemberChain,
	})
	if err != nil {
		handler.writeArchiveAudit(c, backupasset.AuditActionArchiveMember, ref, "create", memberDigest, archiveAuditError(err))
		respondBackupArchiveError(c, err)
		return
	}
	if !validArchiveMemberCreateResult(result, ref, payload.IndexRevision) {
		respondServiceUnavailable(c, "归档成员服务暂不可用")
		return
	}
	if reconcileErr := handler.service.Reconcile(c.Request.Context(), result.RequestID); reconcileErr != nil {
		if errors.Is(reconcileErr, processing.ErrNotDeployed) {
			cancelErr := handler.service.Cancel(c.Request.Context(), processing.ArchiveMemberLookup{
				Actor: actor, Ref: ref, RequestID: result.RequestID, IndexRevision: payload.IndexRevision,
			})
			if cancelErr != nil {
				logger.Module("backup_archive_handler").Warn().Msg("归档成员任务部署缺失后的取消失败")
			}
			handler.writeArchiveAudit(c, backupasset.AuditActionArchiveMember, ref, "create", memberDigest, "unavailable")
			respondBackupArchiveError(c, errors.Join(reconcileErr, cancelErr))
			return
		}
		logger.Module("backup_archive_handler").Warn().Msg("归档成员任务初始对账失败")
	}
	handler.writeArchiveAudit(c, backupasset.AuditActionArchiveMember, ref, "create", memberDigest, "")
	location := strings.TrimSuffix(c.Request.URL.Path, "/") + "/" + result.RequestID
	c.Header("Location", location)
	respondAccepted(c, result)
}

// Status godoc
// @Summary      查询归档成员提取任务
// @Tags         backup-assets
// @Security     Bearer
// @Produce      json
// @Param        id path string true "恢复点 opaque ID"
// @Param        entryId path string true "Catalog entry opaque ID"
// @Param        jobId path string true "归档成员任务 opaque ID"
// @Param        index_revision query string true "冻结的归档索引版本" minlength(64) maxlength(64) extensions(x-pattern=^[0-9a-f]{64}$)
// @Success      200 {object} handlers.Response{data=processing.ArchiveMemberStatusResult}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId}/archive-member-jobs/{jobId} [get]
func (handler *BackupArchiveHandler) Status(c *gin.Context) {
	actor, ref, ok := backupArchiveIdentity(c)
	jobID, idOK := backupAssetOpaqueParam(c, "jobId")
	indexRevision, revisionOK := backupArchiveIndexRevisionQuery(c)
	if !ok || !idOK || !revisionOK || !emptyBackupAssetExportReadRequest(c.Request) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if handler == nil || handler.service == nil {
		respondNotFound(c, "归档资源不存在")
		return
	}
	status, err := handler.service.Poll(c.Request.Context(), processing.ArchiveMemberLookup{
		Actor: actor, Ref: ref, RequestID: jobID, IndexRevision: indexRevision,
	})
	if err != nil {
		respondBackupArchiveError(c, err)
		return
	}
	if !validArchiveMemberStatusResult(status, ref, jobID, indexRevision) {
		respondServiceUnavailable(c, "归档成员服务暂不可用")
		return
	}
	respondOK(c, status)
}

// Cancel godoc
// @Summary      取消归档成员提取任务
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复点 opaque ID"
// @Param        entryId path string true "Catalog entry opaque ID"
// @Param        jobId path string true "归档成员任务 opaque ID"
// @Param        body body backupArchiveMemberBoundPayload true "固定索引版本取消请求"
// @Success      202 {object} handlers.Response{data=processing.ArchiveMemberStatusResult}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId}/archive-member-jobs/{jobId}/cancel [post]
func (handler *BackupArchiveHandler) Cancel(c *gin.Context) {
	actor, ref, ok := backupArchiveIdentity(c)
	jobID, idOK := backupAssetOpaqueParam(c, "jobId")
	var payload backupArchiveMemberBoundPayload
	if !ok || !idOK || c.Request.URL == nil || c.Request.URL.RawQuery != "" ||
		decodeStrictBackupContentJSON(c, &payload) != nil || payload.SchemaVersion != 1 ||
		len(payload.IndexRevision) != 64 || !lowerHexAPI(payload.IndexRevision) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if handler == nil || handler.service == nil {
		respondNotFound(c, "归档资源不存在")
		return
	}
	lookup := processing.ArchiveMemberLookup{
		Actor: actor, Ref: ref, RequestID: jobID, IndexRevision: payload.IndexRevision,
	}
	memberDigest := handler.archiveMemberAuditDigest(c.Request.Context(), lookup)
	if err := handler.service.Cancel(c.Request.Context(), lookup); err != nil {
		handler.writeArchiveAudit(c, backupasset.AuditActionArchiveMember, ref, "cancel", memberDigest, archiveAuditError(err))
		respondBackupArchiveError(c, err)
		return
	}
	status, err := handler.service.Poll(c.Request.Context(), lookup)
	if err != nil {
		respondBackupArchiveError(c, err)
		return
	}
	if !validArchiveMemberStatusResult(status, ref, jobID, payload.IndexRevision) {
		respondServiceUnavailable(c, "归档成员服务暂不可用")
		return
	}
	handler.writeArchiveAudit(c, backupasset.AuditActionArchiveMember, ref, "cancel", memberDigest, "")
	respondAccepted(c, status)
}

func validArchiveMemberCreateResult(
	result processing.ArchiveMemberCreateResult,
	ref backupasset.AssetRef,
	indexRevision string,
) bool {
	return result.SchemaVersion == 1 && backupasset.ValidateOpaqueID(result.RequestID) == nil &&
		result.AssetRef == ref && result.IndexRevision == indexRevision &&
		len(result.IndexRevision) == 64 && lowerHexAPI(result.IndexRevision) && result.State == "queued"
}

func validArchiveMemberStatusResult(
	result processing.ArchiveMemberStatusResult,
	ref backupasset.AssetRef,
	requestID string,
	indexRevision string,
) bool {
	return result.SchemaVersion == 1 && result.RequestID == requestID && result.AssetRef == ref &&
		result.IndexRevision == indexRevision && len(result.IndexRevision) == 64 && lowerHexAPI(result.IndexRevision)
}

func backupArchiveIndexRevisionQuery(c *gin.Context) (string, bool) {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return "", false
	}
	values, ok := backupAssetQuery(c, "index_revision")
	if !ok {
		return "", false
	}
	items, present := values["index_revision"]
	if !present || len(items) != 1 || len(items[0]) != 64 || !lowerHexAPI(items[0]) {
		return "", false
	}
	return items[0], true
}

// DeliveryTicket godoc
// @Summary      创建归档成员下载票据
// @Tags         backup-assets
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复点 opaque ID"
// @Param        entryId path string true "Catalog entry opaque ID"
// @Param        jobId path string true "归档成员任务 opaque ID"
// @Param        X-Xirang-Step-Up header string true "asset.download proof"
// @Param        body body backupAssetExportVersionPayload true "固定版本票据请求"
// @Success      200 {object} handlers.Response{data=assetexport.DeliveryTicketDescriptor}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-points/{id}/entries/{entryId}/archive-member-jobs/{jobId}/delivery-ticket [post]
func (handler *BackupArchiveHandler) DeliveryTicket(c *gin.Context) {
	actor, ref, ok := backupArchiveIdentity(c)
	jobID, idOK := backupAssetOpaqueParam(c, "jobId")
	var payload backupAssetExportVersionPayload
	if !ok || !idOK || c.Request.URL == nil || c.Request.URL.RawQuery != "" ||
		decodeStrictBackupContentJSON(c, &payload) != nil || payload.SchemaVersion != 1 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	session, sessionOK := backupAssetExportSession(c, actor)
	if !sessionOK {
		respondUnauthorized(c, "会话无效")
		return
	}
	proofHandler := &BackupAssetExportHandler{
		db: handler.db, jwtManager: handler.jwtManager, configSource: handler.configSource, schemePolicy: handler.schemePolicy,
	}
	proof, proofOK := proofHandler.exactDeliveryProof(c, actor, auth.StepUpActionAssetDownload)
	if !proofOK {
		return
	}
	secureCookie, secureOK := proofHandler.secureCookie(c)
	if !secureOK {
		return
	}
	if handler == nil || handler.service == nil || handler.delivery == nil {
		respondNotFound(c, "归档资源不存在")
		return
	}
	asset, err := handler.service.AuthorizeReadyDelivery(c.Request.Context(), processing.ArchiveMemberLookup{
		Actor: actor, Ref: ref, RequestID: jobID,
	})
	if err != nil {
		respondBackupArchiveError(c, err)
		return
	}
	ticket, err := handler.delivery.IssueArchiveMember(c.Request.Context(), assetexport.ArchiveMemberDeliveryIssueRequest{
		Actor: actor, Session: session, Asset: asset, MemberRequestID: jobID,
		Proof: proof, SecureCookie: secureCookie,
	})
	if err != nil {
		respondBackupArchiveError(c, err)
		return
	}
	if !validBackupAssetDeliveryTicket(ticket, secureCookie, backupAssetDeliveryArchiveMember) {
		respondServiceUnavailable(c, "归档成员服务暂不可用")
		return
	}
	http.SetCookie(c.Writer, ticket.Cookie)
	respondOK(c, ticket.Descriptor)
}

func backupArchiveIdentity(c *gin.Context) (content.DeliveryActor, backupasset.AssetRef, bool) {
	actor := content.DeliveryActor{
		UserID: middleware.CurrentUserID(c), Username: c.GetString(middleware.CtxUsername), Role: middleware.CurrentRole(c),
	}
	ref := backupasset.AssetRef{
		RecoveryPointID: strings.TrimSpace(c.Param("id")), EntryID: strings.TrimSpace(c.Param("entryId")),
	}
	ok := actor.UserID != 0 && (actor.Role == "admin" || actor.Role == "operator") &&
		backupasset.ValidateAssetRef(ref) == nil && c.Param("id") == ref.RecoveryPointID && c.Param("entryId") == ref.EntryID &&
		c.Request != nil && c.Request.URL != nil
	return actor, ref, ok
}

func respondBackupArchiveError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, processing.ErrNotDeployed), errors.Is(err, assetexport.ErrUnavailable):
		respondServiceUnavailable(c, "归档成员服务暂不可用")
	case errors.Is(err, processing.ErrArchiveMemberUnavailable), errors.Is(err, processing.ErrProcessingHandleNotFound),
		errors.Is(err, backupasset.ErrNotFound), errors.Is(err, backupasset.ErrForbidden), errors.Is(err, assetexport.ErrNotFound):
		respondNotFound(c, "归档资源不存在")
	case errors.Is(err, processing.ErrArchiveNestedUnsupported), errors.Is(err, processing.ErrInvalidContract),
		errors.Is(err, assetexport.ErrInvalidIdempotency), errors.Is(err, assetexport.ErrInvalidDeliveryRequest):
		respondBadRequest(c, "请求参数不合法")
	case errors.Is(err, backupasset.ErrConflict), errors.Is(err, processing.ErrRevisionConflict), errors.Is(err, assetexport.ErrConflict):
		respondConflict(c, "归档任务状态冲突")
	default:
		respondInternalError(c, err)
	}
}

func archiveAuditError(err error) string {
	switch {
	case errors.Is(err, processing.ErrNotDeployed), errors.Is(err, assetexport.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, processing.ErrArchiveMemberUnavailable), errors.Is(err, backupasset.ErrForbidden):
		return "not_found"
	case errors.Is(err, processing.ErrArchiveNestedUnsupported), errors.Is(err, processing.ErrInvalidContract),
		errors.Is(err, assetexport.ErrInvalidIdempotency):
		return "invalid_request"
	case err != nil:
		return "unavailable"
	default:
		return ""
	}
}

func (handler *BackupArchiveHandler) writeArchiveAudit(
	c *gin.Context,
	action backupasset.AuditAction,
	ref backupasset.AssetRef,
	mode string,
	sourceDigest string,
	errorCategory string,
) {
	if handler == nil || handler.audit == nil || c == nil || c.Request == nil {
		return
	}
	input := backupAssetAuditInput(c, action)
	input.RecoveryPointID = ref.RecoveryPointID
	input.EntryID = ref.EntryID
	input.Outcome = backupasset.AuditOutcomeSuccess
	input.ItemCount = 1
	input.Fields[backupasset.AuditFieldMode] = mode
	if len(sourceDigest) == 64 && lowerHexAPI(sourceDigest) {
		input.Fields[backupasset.AuditFieldSource] = sourceDigest
	}
	if errorCategory != "" {
		input.Outcome = backupasset.AuditOutcomeBlocked
		input.FailureCode = errorCategory
		input.Fields[backupasset.AuditFieldCode] = errorCategory
	}
	if err := handler.audit.Write(c.Request.Context(), input); err != nil {
		logger.Module("backup_archive_handler").Warn().Msg("备份归档审计写入失败")
	}
}

func backupArchiveMemberDigest(ref backupasset.AssetRef, indexRevision string, memberChain []string) string {
	if backupasset.ValidateAssetRef(ref) != nil || len(indexRevision) != 64 || !lowerHexAPI(indexRevision) ||
		len(memberChain) != 1 || backupasset.ValidateOpaqueID(memberChain[0]) != nil {
		return ""
	}
	return content.ArchiveMemberChainDigest(ref, indexRevision, memberChain[0])
}

func (handler *BackupArchiveHandler) archiveMemberAuditDigest(
	ctx context.Context,
	lookup processing.ArchiveMemberLookup,
) string {
	if handler == nil || handler.db == nil || lookup.Actor.UserID == 0 ||
		backupasset.ValidateAssetRef(lookup.Ref) != nil || backupasset.ValidateOpaqueID(lookup.RequestID) != nil ||
		len(lookup.IndexRevision) != 64 || !lowerHexAPI(lookup.IndexRevision) {
		return ""
	}
	var row struct {
		MemberChainDigest string
	}
	result := handler.db.WithContext(ctx).Model(&model.BackupAssetArchiveMemberRequest{}).
		Select("member_chain_digest").
		Where("id = ? AND owner_user_id = ? AND recovery_point_id = ? AND entry_id = ? AND index_revision = ?",
			lookup.RequestID, lookup.Actor.UserID, lookup.Ref.RecoveryPointID, lookup.Ref.EntryID, lookup.IndexRevision).
		Limit(1).Find(&row)
	if result.Error != nil || result.RowsAffected != 1 || len(row.MemberChainDigest) != 64 || !lowerHexAPI(row.MemberChainDigest) {
		return ""
	}
	return row.MemberChainDigest
}
