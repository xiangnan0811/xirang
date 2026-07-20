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
	SchemaVersion         int    `json:"schema_version"`
	ExpectedRevision      string `json:"expected_revision"`
	Paused                bool   `json:"paused"`
	BatchSize             int    `json:"batch_size"`
	JobsPerHour           int    `json:"jobs_per_hour"`
	BytesPerHour          int64  `json:"bytes_per_hour"`
	ProviderConcurrency   int    `json:"provider_concurrency"`
	CapabilityConcurrency int    `json:"capability_concurrency"`
}

type backupWorkerActivationPayload struct {
	SchemaVersion             int             `json:"schema_version"`
	CandidateID               string          `json:"candidate_id"`
	ExpectedActiveFingerprint json.RawMessage `json:"expected_active_fingerprint"`
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

func (handler *BackupWorkerHandler) UpdateBackfillPolicy(c *gin.Context) {
	var payload backupWorkerPolicyPayload
	if decodeStrictBackupContentJSON(c, &payload) != nil || payload.SchemaVersion != 1 ||
		!lowerHexAPI(payload.ExpectedRevision) || len(payload.ExpectedRevision) != 64 ||
		payload.BatchSize <= 0 || payload.JobsPerHour <= 0 || payload.BytesPerHour <= 0 ||
		payload.ProviderConcurrency <= 0 || payload.CapabilityConcurrency <= 0 || !handler.prepareEnabled(c) {
		if !c.IsAborted() && c.Writer.Status() == http.StatusOK {
			respondBadRequest(c, "请求参数不合法")
		}
		return
	}
	result, err := handler.service.UpdateProcessingBackfillPolicy(c.Request.Context(), backupruntime.ProcessingBackfillPolicyUpdate{
		ExpectedRevision: payload.ExpectedRevision, Paused: payload.Paused, BatchSize: payload.BatchSize,
		JobsPerHour: payload.JobsPerHour, BytesPerHour: payload.BytesPerHour,
		ProviderConcurrency: payload.ProviderConcurrency, CapabilityConcurrency: payload.CapabilityConcurrency,
	})
	if err != nil {
		handler.respondError(c, err)
		return
	}
	handler.writePolicyAudit(c, backupasset.AuditOutcomeSuccess, "")
	respondOK(c, result)
}

func (handler *BackupWorkerHandler) ScanOfflineCandidates(c *gin.Context) {
	if !emptyBackupWorkerAdminRequest(c.Request) {
		respondBadRequest(c, "请求不得包含查询参数或请求体")
		return
	}
	if !handler.prepareEnabled(c) {
		return
	}
	if err := handler.service.RequestProcessingUpdaterScan(c.Request.Context()); err != nil {
		handler.respondError(c, err)
		return
	}
	respondAccepted(c, backupWorkerAcceptedResult{SchemaVersion: 1, Accepted: true})
}

func (handler *BackupWorkerHandler) ActivateOfflineCandidate(c *gin.Context) {
	var payload backupWorkerActivationPayload
	if decodeStrictBackupContentJSON(c, &payload) != nil || payload.SchemaVersion != 1 ||
		backupasset.ValidateOpaqueID(payload.CandidateID) != nil || len(payload.ExpectedActiveFingerprint) == 0 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	var expected *string
	if !bytes.Equal(bytes.TrimSpace(payload.ExpectedActiveFingerprint), []byte("null")) {
		var value string
		if json.Unmarshal(payload.ExpectedActiveFingerprint, &value) != nil || len(value) != 64 || !lowerHexAPI(value) {
			respondBadRequest(c, "请求参数不合法")
			return
		}
		expected = &value
	}
	if !handler.prepareEnabled(c) {
		return
	}
	if err := handler.service.ActivateProcessingUpdaterCandidate(c.Request.Context(), backupruntime.ProcessingUpdaterActivationRequest{
		CandidateID: payload.CandidateID, ExpectedActiveFingerprint: expected,
	}); err != nil {
		handler.respondError(c, err)
		return
	}
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

func (handler *BackupWorkerHandler) writePolicyAudit(c *gin.Context, outcome backupasset.AuditOutcome, code string) {
	if handler == nil || handler.audit == nil || c == nil || c.Request == nil {
		return
	}
	input := backupAssetAuditInput(c, backupasset.AuditActionProcessingPolicyUpdate)
	input.Outcome = outcome
	if code != "" {
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
