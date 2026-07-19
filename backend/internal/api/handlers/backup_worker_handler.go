package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"

	"xirang/backend/internal/backupasset"
	backupruntime "xirang/backend/internal/backupasset/runtime"

	"github.com/gin-gonic/gin"
)

type BackupWorkerAdminService interface {
	ProcessingConfig() (backupasset.ProcessingConfig, error)
	ProcessingAdminSummary(context.Context) (backupruntime.ProcessingAdminSummary, error)
}

type BackupWorkerHandler struct {
	service BackupWorkerAdminService
}

func NewBackupWorkerHandler(service BackupWorkerAdminService) *BackupWorkerHandler {
	return &BackupWorkerHandler{service: service}
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
