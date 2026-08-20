package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

const (
	maxBackupGAAcknowledgeBodyBytes = 4 << 10
	backupGASchemaVersion           = 1
)

type BackupGAService interface {
	RunInventory(context.Context) (ga.AdminReport, error)
	Readiness(context.Context) (ga.AdminReport, error)
	Acknowledge(context.Context, uint, string) (ga.AdminReport, error)
}

type BackupGAHandler struct {
	service BackupGAService
}

type backupGAAcknowledgePayload struct {
	Digest string `json:"digest"`
}

type backupGACountsResponse struct {
	Candidates     int `json:"candidates"`
	Conflicts      int `json:"conflicts"`
	Unsupported    int `json:"unsupported"`
	CapabilityGaps int `json:"capability_gaps"`
}

type backupGAConflictResponse struct {
	Kind             string `json:"kind"`
	TaskIDs          []uint `json:"task_ids"`
	RepositoryID     string `json:"repository_id,omitempty"`
	StableReasonCode string `json:"stable_reason_code"`
}

type backupGAReportResponse struct {
	SchemaVersion      int                        `json:"schema_version"`
	Class              string                     `json:"class"`
	Status             string                     `json:"status"`
	InventoryComplete  bool                       `json:"inventory_complete"`
	InventoryDigest    string                     `json:"inventory_digest"`
	AcknowledgedDigest string                     `json:"acknowledged_digest"`
	ExportRootValid    bool                       `json:"export_root_valid"`
	KeyDomainsReady    bool                       `json:"key_domains_ready"`
	WorkerOptional     bool                       `json:"worker_optional"`
	Counts             backupGACountsResponse     `json:"counts"`
	Conflicts          []backupGAConflictResponse `json:"conflicts"`
}

func NewBackupGAHandler(service BackupGAService) *BackupGAHandler {
	return &BackupGAHandler{service: service}
}

func (handler *BackupGAHandler) Inventory(c *gin.Context) {
	if !handler.prepare(c) {
		return
	}
	report, err := handler.service.RunInventory(c.Request.Context())
	if err != nil {
		respondBackupGAError(c, err)
		return
	}
	respondOK(c, publicBackupGAReport(report))
}

func (handler *BackupGAHandler) Readiness(c *gin.Context) {
	if !handler.prepare(c) {
		return
	}
	report, err := handler.service.Readiness(c.Request.Context())
	if err != nil {
		respondBackupGAError(c, err)
		return
	}
	respondOK(c, publicBackupGAReport(report))
}

func (handler *BackupGAHandler) Acknowledge(c *gin.Context) {
	if handler.service == nil {
		respondServiceUnavailable(c, "备份资产就绪状态暂不可用")
		return
	}
	digest, ok := readBackupGAAcknowledgeDigest(c)
	if !ok {
		return
	}
	report, err := handler.service.Acknowledge(c.Request.Context(), middleware.CurrentUserID(c), digest)
	if err != nil {
		respondBackupGAError(c, err)
		return
	}
	respondOK(c, publicBackupGAReport(report))
}

func (handler *BackupGAHandler) prepare(c *gin.Context) bool {
	if handler == nil || handler.service == nil {
		respondServiceUnavailable(c, "备份资产就绪状态暂不可用")
		return false
	}
	return true
}

func readBackupGAAcknowledgeDigest(c *gin.Context) (string, bool) {
	if c == nil || c.Request == nil {
		respondBadRequest(c, "清单摘要无效")
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBackupGAAcknowledgeBodyBytes+1))
	if err != nil {
		respondBadRequest(c, "清单摘要无效")
		return "", false
	}
	if len(body) == 0 || len(body) > maxBackupGAAcknowledgeBodyBytes {
		respondBadRequest(c, "清单摘要无效")
		return "", false
	}
	var payload backupGAAcknowledgePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		respondBadRequest(c, "清单摘要无效")
		return "", false
	}
	digest := strings.ToLower(strings.TrimSpace(payload.Digest))
	if len(digest) != 64 {
		respondBadRequest(c, "清单摘要无效")
		return "", false
	}
	for _, r := range digest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			respondBadRequest(c, "清单摘要无效")
			return "", false
		}
	}
	return digest, true
}

func publicBackupGAReport(report ga.AdminReport) backupGAReportResponse {
	conflicts := make([]backupGAConflictResponse, 0, len(report.Inventory.Conflicts))
	for _, conflict := range report.Inventory.Conflicts {
		taskIDs := append([]uint(nil), conflict.TaskIDs...)
		if taskIDs == nil {
			taskIDs = []uint{}
		}
		conflicts = append(conflicts, backupGAConflictResponse{
			Kind:             string(conflict.Kind),
			TaskIDs:          taskIDs,
			RepositoryID:     strings.TrimSpace(conflict.RepositoryID),
			StableReasonCode: conflict.StableReasonCode,
		})
	}
	digest := strings.TrimSpace(report.Snapshot.InventoryDigest)
	if digest == "" {
		digest = strings.TrimSpace(report.Inventory.Digest)
	}
	class := string(report.Snapshot.Class)
	if class == "" {
		class = string(report.Inventory.Class)
	}
	return backupGAReportResponse{
		SchemaVersion:      backupGASchemaVersion,
		Class:              class,
		Status:             string(report.Snapshot.Status),
		InventoryComplete:  report.Snapshot.InventoryComplete,
		InventoryDigest:    digest,
		AcknowledgedDigest: strings.TrimSpace(report.Snapshot.AcknowledgedDigest),
		ExportRootValid:    report.Snapshot.ExportRootValid,
		KeyDomainsReady:    report.Snapshot.KeyDomainsReady,
		WorkerOptional:     true,
		Counts: backupGACountsResponse{
			Candidates:     report.Inventory.Counts.Candidates,
			Conflicts:      report.Inventory.Counts.Conflicts,
			Unsupported:    report.Inventory.Counts.Unsupported,
			CapabilityGaps: report.Inventory.Counts.CapabilityGaps,
		},
		Conflicts: conflicts,
	}
}

func respondBackupAssetEnablementConflict(c *gin.Context, err error) bool {
	if errors.Is(err, ga.ErrEnablementBlocked) || errors.Is(err, ga.ErrEnablementAckRequired) {
		respondConflict(c, "就绪检查未完成")
		return true
	}
	return false
}

func respondBackupGAError(c *gin.Context, err error) {
	if respondBackupAssetEnablementConflict(c, err) {
		return
	}
	switch {
	case errors.Is(err, ga.ErrInvalidInventoryDigest):
		respondBadRequest(c, "清单摘要无效")
	case errors.Is(err, ga.ErrAcknowledgeNotRequired):
		respondBadRequest(c, "当前安装无需确认")
	case errors.Is(err, ga.ErrInventoryDigestMismatch):
		respondConflict(c, "清单已变化，请重新核对")
	case errors.Is(err, backupasset.ErrInvalidState):
		respondServiceUnavailable(c, "备份资产就绪状态暂不可用")
	default:
		respondInternalError(c, err)
	}
}
