package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/processing"
	backupruntime "xirang/backend/internal/backupasset/runtime"

	"github.com/gin-gonic/gin"
)

type BackupWorkerAdminService interface {
	ProcessingConfig() (backupasset.ProcessingConfig, error)
	ProcessingAdminSummary(context.Context) (backupruntime.ProcessingAdminSummary, error)
	ProcessingCapabilities(context.Context) ([]processing.CapabilityInventoryItem, error)
	ProcessingCoverage(context.Context) (processing.CoverageSummary, error)
	ProcessingUpdaterStatus(context.Context) (backupruntime.ProcessingUpdaterStatus, error)
	ProcessingUpdaterCandidates(context.Context) ([]backupruntime.ProcessingUpdaterCandidate, error)
	ProcessingBackfillPolicy() (backupruntime.ProcessingBackfillPolicy, error)
	UpdateProcessingBackfillPolicy(context.Context, backupruntime.ProcessingBackfillPolicyUpdate) (backupruntime.ProcessingBackfillPolicy, error)
	RequestProcessingUpdaterScan(context.Context) error
	ActivateProcessingUpdaterCandidate(context.Context, backupruntime.ProcessingUpdaterActivationRequest) error
}

type BackupWorkerHandler struct {
	service BackupWorkerAdminService
	audit   BackupAssetAuditSink
}

type backupWorkerCapabilitiesResult struct {
	SchemaVersion int                                  `json:"schema_version"`
	Items         []processing.CapabilityInventoryItem `json:"items"`
}

type backupWorkerCandidatesResult struct {
	SchemaVersion int                                        `json:"schema_version"`
	Items         []backupruntime.ProcessingUpdaterCandidate `json:"items"`
}

type backupWorkerAcceptedResult struct {
	SchemaVersion int  `json:"schema_version"`
	Accepted      bool `json:"accepted"`
}

type backupWorkerPolicyPayload struct {
	SchemaVersion         int    `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	ExpectedRevision      string `json:"expected_revision" binding:"required" minLength:"64" maxLength:"64" extensions:"x-pattern=^[0-9a-f]{64}$"`
	Paused                bool   `json:"paused" binding:"required"`
	BatchSize             int    `json:"batch_size" binding:"required" minimum:"1"`
	JobsPerHour           int    `json:"jobs_per_hour" binding:"required" minimum:"1"`
	BytesPerHour          int64  `json:"bytes_per_hour" binding:"required" minimum:"1"`
	ProviderConcurrency   int    `json:"provider_concurrency" binding:"required" minimum:"1"`
	CapabilityConcurrency int    `json:"capability_concurrency" binding:"required" minimum:"1"`
}

type backupWorkerActivationPayload struct {
	SchemaVersion             int             `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	CandidateID               string          `json:"candidate_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	ExpectedActiveFingerprint json.RawMessage `json:"expected_active_fingerprint" binding:"required" swaggertype:"string" minLength:"64" maxLength:"64" extensions:"x-nullable,x-pattern=^[0-9a-f]{64}$"`
}

func NewBackupWorkerHandler(service BackupWorkerAdminService) *BackupWorkerHandler {
	return &BackupWorkerHandler{service: service}
}

func (handler *BackupWorkerHandler) WithAudit(audit BackupAssetAuditSink) *BackupWorkerHandler {
	if handler != nil {
		handler.audit = audit
	}
	return handler
}

// Get godoc
// @Summary      查看备份资产 Worker 与派生存储健康摘要
// @Description  仅返回无身份、来源、路径、凭证或原始错误的有界管理聚合
// @Tags         admin
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=backupruntime.ProcessingAdminSummary}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      429  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /admin/backup-asset-processing [get]
func (handler *BackupWorkerHandler) Get(c *gin.Context) {
	if !emptyBackupWorkerAdminRequest(c.Request) {
		respondBadRequest(c, "请求不得包含查询参数或请求体")
		return
	}
	if handler == nil || handler.service == nil {
		respondServiceUnavailable(c, "备份资产处理状态暂不可用")
		return
	}
	config, err := handler.service.ProcessingConfig()
	if err != nil {
		respondServiceUnavailable(c, "备份资产处理状态暂不可用")
		return
	}
	if !config.Enabled {
		respondNotFound(c, "备份资产处理功能未启用")
		return
	}
	summary, err := handler.service.ProcessingAdminSummary(c.Request.Context())
	if err != nil {
		respondServiceUnavailable(c, "备份资产处理状态暂不可用")
		return
	}
	respondOK(c, summary)
}

// Capabilities godoc
// @Summary      查看备份资产 Worker capability inventory
// @Description  Admin-only、feature-gated 的闭合 capability/profile 清单；不返回 Worker 身份、路径、凭证或原始诊断
// @Tags         admin
// @Security     Bearer
// @Produce      json
// @Success      200  {object} handlers.Response{data=backupWorkerCapabilitiesResult}
// @Failure      400  {object} handlers.Response
// @Failure      401  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      429  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /admin/backup-asset-processing/capabilities [get]
func (handler *BackupWorkerHandler) Capabilities(c *gin.Context) {
	if !handler.prepareRead(c) {
		return
	}
	items, err := handler.service.ProcessingCapabilities(c.Request.Context())
	if err != nil {
		handler.respondError(c, err)
		return
	}
	if items == nil {
		items = []processing.CapabilityInventoryItem{}
	}
	respondOK(c, backupWorkerCapabilitiesResult{SchemaVersion: 1, Items: items})
}

// Coverage godoc
// @Summary      查看备份资产处理覆盖率
// @Description  Admin-only、feature-gated 的有界 coverage 聚合；generation、coverage、staleness 与 availability 保持独立
// @Tags         admin
// @Security     Bearer
// @Produce      json
// @Success      200  {object} handlers.Response{data=processing.CoverageSummary}
// @Failure      400  {object} handlers.Response
// @Failure      401  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      429  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /admin/backup-asset-processing/coverage [get]
func (handler *BackupWorkerHandler) Coverage(c *gin.Context) {
	if !handler.prepareRead(c) {
		return
	}
	result, err := handler.service.ProcessingCoverage(c.Request.Context())
	if err != nil {
		handler.respondError(c, err)
		return
	}
	respondOK(c, result)
}

// Updater godoc
// @Summary      查看备份资产 updater 状态
// @Description  Admin-only、feature-gated 的脱敏 updater 状态；不返回 inbox 路径、bundle bytes、凭证或原始错误
// @Tags         admin
// @Security     Bearer
// @Produce      json
// @Success      200  {object} handlers.Response{data=backupruntime.ProcessingUpdaterStatus}
// @Failure      400  {object} handlers.Response
// @Failure      401  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      429  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /admin/backup-asset-processing/updater [get]
func (handler *BackupWorkerHandler) Updater(c *gin.Context) {
	if !handler.prepareRead(c) {
		return
	}
	result, err := handler.service.ProcessingUpdaterStatus(c.Request.Context())
	if err != nil {
		handler.respondError(c, err)
		return
	}
	respondOK(c, result)
}

// OfflineCandidates godoc
// @Summary      列出备份资产 updater 离线候选
// @Description  Admin-only、feature-gated 的已验签 candidate 摘要；只返回 opaque candidate ID 与有界版本/指纹
// @Tags         admin
// @Security     Bearer
// @Produce      json
// @Success      200  {object} handlers.Response{data=backupWorkerCandidatesResult}
// @Failure      400  {object} handlers.Response
// @Failure      401  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      429  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /admin/backup-asset-processing/updater/offline-candidates [get]
func (handler *BackupWorkerHandler) OfflineCandidates(c *gin.Context) {
	if !handler.prepareRead(c) {
		return
	}
	items, err := handler.service.ProcessingUpdaterCandidates(c.Request.Context())
	if err != nil {
		handler.respondError(c, err)
		return
	}
	if items == nil {
		items = []backupruntime.ProcessingUpdaterCandidate{}
	}
	respondOK(c, backupWorkerCandidatesResult{SchemaVersion: 1, Items: items})
}

// UpdateBackfillPolicy godoc
// @Summary      更新备份资产处理回填策略
// @Description  Admin-only、feature-gated 的 revision-CAS mutation；请求只包含闭合 pause/quota/concurrency 字段并写入 typed audit
// @Tags         admin
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body  backupWorkerPolicyPayload  true  "回填 pause/quota 策略"
// @Success      200  {object} handlers.Response{data=backupruntime.ProcessingBackfillPolicy}
// @Failure      400  {object} handlers.Response
// @Failure      401  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      409  {object} handlers.Response
// @Failure      429  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /admin/backup-asset-processing/backfill-policy [patch]
func (handler *BackupWorkerHandler) UpdateBackfillPolicy(c *gin.Context) {
	var payload backupWorkerPolicyPayload
	if decodeStrictBackupContentJSON(c, &payload) != nil || payload.SchemaVersion != 1 ||
		!lowerHexAPI(payload.ExpectedRevision) || len(payload.ExpectedRevision) != 64 ||
		payload.BatchSize <= 0 || payload.JobsPerHour <= 0 || payload.BytesPerHour <= 0 ||
		payload.ProviderConcurrency <= 0 || payload.CapabilityConcurrency <= 0 {
		handler.writeMutationAudit(c, "backfill_policy_update", "", backupasset.AuditOutcomeBlocked, "invalid_request")
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if !handler.prepareMutationEnabled(c, "backfill_policy_update", "") {
		return
	}
	result, err := handler.service.UpdateProcessingBackfillPolicy(c.Request.Context(), backupruntime.ProcessingBackfillPolicyUpdate{
		ExpectedRevision: payload.ExpectedRevision, Paused: payload.Paused, BatchSize: payload.BatchSize,
		JobsPerHour: payload.JobsPerHour, BytesPerHour: payload.BytesPerHour,
		ProviderConcurrency: payload.ProviderConcurrency, CapabilityConcurrency: payload.CapabilityConcurrency,
	})
	if err != nil {
		handler.respondMutationError(c, "backfill_policy_update", "", err)
		return
	}
	handler.writeMutationAudit(c, "backfill_policy_update", "", backupasset.AuditOutcomeSuccess, "")
	respondOK(c, result)
}

// ScanOfflineCandidates godoc
// @Summary      扫描备份资产 updater 离线候选
// @Description  Admin-only、feature-gated 的固定 inbox scan；请求不接受 body/query，异步结果只返回 bounded accepted 状态
// @Tags         admin
// @Security     Bearer
// @Produce      json
// @Success      202  {object} handlers.Response{data=backupWorkerAcceptedResult}
// @Failure      400  {object} handlers.Response
// @Failure      401  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      409  {object} handlers.Response
// @Failure      429  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /admin/backup-asset-processing/updater/offline-candidates/scan [post]
func (handler *BackupWorkerHandler) ScanOfflineCandidates(c *gin.Context) {
	if !emptyBackupWorkerAdminRequest(c.Request) {
		handler.writeMutationAudit(c, "offline_candidate_scan", "", backupasset.AuditOutcomeBlocked, "invalid_request")
		respondBadRequest(c, "请求不得包含查询参数或请求体")
		return
	}
	if !handler.prepareMutationEnabled(c, "offline_candidate_scan", "") {
		return
	}
	if err := handler.service.RequestProcessingUpdaterScan(c.Request.Context()); err != nil {
		handler.respondMutationError(c, "offline_candidate_scan", "", err)
		return
	}
	handler.writeMutationAudit(c, "offline_candidate_scan", "", backupasset.AuditOutcomeSuccess, "")
	respondAccepted(c, backupWorkerAcceptedResult{SchemaVersion: 1, Accepted: true})
}

// ActivateOfflineCandidate godoc
// @Summary      激活备份资产 updater 离线候选
// @Description  Admin-only、feature-gated 的 bounded candidate activation；使用 expected_active_fingerprint CAS，不接受 bundle bytes、URL 或服务器路径
// @Tags         admin
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body  backupWorkerActivationPayload  true  "candidate opaque ID 与期望 active fingerprint"
// @Success      202  {object} handlers.Response{data=backupWorkerAcceptedResult}
// @Failure      400  {object} handlers.Response
// @Failure      401  {object} handlers.Response
// @Failure      403  {object} handlers.Response
// @Failure      404  {object} handlers.Response
// @Failure      409  {object} handlers.Response
// @Failure      429  {object} handlers.Response
// @Failure      503  {object} handlers.Response
// @Router       /admin/backup-asset-processing/updater/offline-imports [post]
func (handler *BackupWorkerHandler) ActivateOfflineCandidate(c *gin.Context) {
	var payload backupWorkerActivationPayload
	decodeErr := decodeStrictBackupContentJSON(c, &payload)
	candidateID := ""
	if backupasset.ValidateOpaqueID(payload.CandidateID) == nil {
		candidateID = payload.CandidateID
	}
	if decodeErr != nil || payload.SchemaVersion != 1 ||
		backupasset.ValidateOpaqueID(payload.CandidateID) != nil || len(payload.ExpectedActiveFingerprint) == 0 {
		handler.writeMutationAudit(c, "offline_candidate_activate", candidateID, backupasset.AuditOutcomeBlocked, "invalid_request")
		respondBadRequest(c, "请求参数不合法")
		return
	}
	var expected *string
	if !bytes.Equal(bytes.TrimSpace(payload.ExpectedActiveFingerprint), []byte("null")) {
		var value string
		if json.Unmarshal(payload.ExpectedActiveFingerprint, &value) != nil || len(value) != 64 || !lowerHexAPI(value) {
			handler.writeMutationAudit(c, "offline_candidate_activate", candidateID, backupasset.AuditOutcomeBlocked, "invalid_request")
			respondBadRequest(c, "请求参数不合法")
			return
		}
		expected = &value
	}
	if !handler.prepareMutationEnabled(c, "offline_candidate_activate", candidateID) {
		return
	}
	if err := handler.service.ActivateProcessingUpdaterCandidate(c.Request.Context(), backupruntime.ProcessingUpdaterActivationRequest{
		CandidateID: payload.CandidateID, ExpectedActiveFingerprint: expected,
	}); err != nil {
		handler.respondMutationError(c, "offline_candidate_activate", candidateID, err)
		return
	}
	handler.writeMutationAudit(c, "offline_candidate_activate", candidateID, backupasset.AuditOutcomeSuccess, "")
	respondAccepted(c, backupWorkerAcceptedResult{SchemaVersion: 1, Accepted: true})
}

func (handler *BackupWorkerHandler) prepareRead(c *gin.Context) bool {
	if !emptyBackupWorkerAdminRequest(c.Request) {
		respondBadRequest(c, "请求不得包含查询参数或请求体")
		return false
	}
	return handler.prepareEnabled(c)
}

func (handler *BackupWorkerHandler) prepareEnabled(c *gin.Context) bool {
	if handler == nil || handler.service == nil {
		respondServiceUnavailable(c, "备份资产处理状态暂不可用")
		return false
	}
	config, err := handler.service.ProcessingConfig()
	if err != nil {
		respondServiceUnavailable(c, "备份资产处理状态暂不可用")
		return false
	}
	if !config.Enabled {
		respondNotFound(c, "备份资产处理功能未启用")
		return false
	}
	return true
}

func (handler *BackupWorkerHandler) prepareMutationEnabled(c *gin.Context, mode, correlationID string) bool {
	if handler.prepareEnabled(c) {
		return true
	}
	if c != nil && c.Writer.Status() == http.StatusNotFound {
		handler.writeMutationAudit(c, mode, correlationID, backupasset.AuditOutcomeBlocked, "not_found")
		return false
	}
	handler.writeMutationAudit(c, mode, correlationID, backupasset.AuditOutcomeFailure, "unavailable")
	return false
}

func (handler *BackupWorkerHandler) respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, processing.ErrProcessingDisabled), errors.Is(err, backupasset.ErrNotFound):
		respondNotFound(c, "备份资产处理资源不存在")
	case errors.Is(err, backupasset.ErrConflict), errors.Is(err, processing.ErrRevisionConflict):
		respondConflict(c, "备份资产处理状态已变化")
	case errors.Is(err, processing.ErrInvalidContract):
		respondBadRequest(c, "请求参数不合法")
	default:
		respondServiceUnavailable(c, "备份资产处理状态暂不可用")
	}
}

func (handler *BackupWorkerHandler) respondMutationError(c *gin.Context, mode, correlationID string, err error) {
	outcome, code := backupWorkerAuditDisposition(err)
	handler.writeMutationAudit(c, mode, correlationID, outcome, code)
	handler.respondError(c, err)
}

func backupWorkerAuditDisposition(err error) (backupasset.AuditOutcome, string) {
	switch {
	case errors.Is(err, processing.ErrProcessingDisabled), errors.Is(err, backupasset.ErrNotFound):
		return backupasset.AuditOutcomeBlocked, "not_found"
	case errors.Is(err, backupasset.ErrConflict), errors.Is(err, processing.ErrRevisionConflict):
		return backupasset.AuditOutcomeBlocked, "conflict"
	case errors.Is(err, processing.ErrInvalidContract):
		return backupasset.AuditOutcomeBlocked, "invalid_request"
	default:
		return backupasset.AuditOutcomeFailure, "unavailable"
	}
}

func (handler *BackupWorkerHandler) writeMutationAudit(
	c *gin.Context,
	mode string,
	correlationID string,
	outcome backupasset.AuditOutcome,
	code string,
) {
	if handler == nil || handler.audit == nil || c == nil || c.Request == nil {
		return
	}
	input := backupAssetAuditInput(c, backupasset.AuditActionProcessingPolicyUpdate)
	input.Outcome = outcome
	input.Fields[backupasset.AuditFieldMode] = mode
	if correlationID != "" {
		input.Fields[backupasset.AuditFieldCorrelationID] = correlationID
	}
	if code != "" {
		input.FailureCode = code
		input.Fields[backupasset.AuditFieldCode] = code
	}
	_ = handler.audit.Write(c.Request.Context(), input)
}

func emptyBackupWorkerAdminRequest(request *http.Request) bool {
	if request == nil || request.URL == nil || request.URL.RawQuery != "" || request.ContentLength > 0 || len(request.TransferEncoding) != 0 {
		return false
	}
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	var probe [1]byte
	read, err := request.Body.Read(probe[:])
	return read == 0 && errors.Is(err, io.EOF)
}
