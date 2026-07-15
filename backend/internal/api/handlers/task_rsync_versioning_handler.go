package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"xirang/backend/internal/backupasset"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

// TaskRsyncVersioningService is the narrow migration boundary exposed to the
// HTTP layer. It accepts opaque IDs and safe mode literals only; roots,
// locators, marker evidence, and command arguments remain private to the
// repository service.
type TaskRsyncVersioningService interface {
	CreateRsyncVersioningPreflightForRequest(context.Context, backupasset.RsyncVersioningPreflightRequest, backuprepository.RequestContext) (backupasset.RsyncVersioningPreflightResult, error)
	ActivateRsyncVersioningForRequest(context.Context, backupasset.RsyncVersioningActivationRequest, backuprepository.RequestContext) (backupasset.RsyncVersioningActivationResult, error)
	PrepareRsyncVersioningRollbackForRequest(context.Context, backupasset.RsyncVersioningRollbackPreparationRequest, backuprepository.RequestContext) (backupasset.RsyncVersioningRollbackPreparationResult, error)
	RsyncVersioningSummary(context.Context, uint) (backupasset.RsyncVersioningSummary, error)
}

type TaskRsyncVersioningHandler struct {
	service TaskRsyncVersioningService
}

func NewTaskRsyncVersioningHandler(service TaskRsyncVersioningService) *TaskRsyncVersioningHandler {
	return &TaskRsyncVersioningHandler{service: service}
}

type taskRsyncVersioningPreflightRequest struct {
	ExpectedTaskRevision taskRsyncVersioningTaskRevision `json:"expected_task_revision" swaggertype:"string"`
	RequestedMode        backupasset.TaskPublicationMode `json:"requested_mode"`
}

type taskRsyncVersioningActivationRequest struct {
	ExpectedTaskRevision taskRsyncVersioningTaskRevision            `json:"expected_task_revision" swaggertype:"string"`
	PreflightID          string                                     `json:"preflight_id"`
	MigrationChoice      backupasset.RsyncVersioningMigrationChoice `json:"migration_choice"`
}

type taskRsyncVersioningRollbackRequest struct {
	ExpectedTaskRevision taskRsyncVersioningTaskRevision `json:"expected_task_revision" swaggertype:"string"`
}

// taskRsyncVersioningTaskRevision accepts the legacy numeric request form and
// the decimal string form required by JavaScript clients. Managed Rsync uses
// Unix nanoseconds as an exact CAS token, which exceeds Number's safe range.
type taskRsyncVersioningTaskRevision uint64

func (revision *taskRsyncVersioningTaskRevision) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return fmt.Errorf("missing task revision")
	}
	value := string(trimmed)
	if trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return err
		}
	}
	if strings.TrimSpace(value) != value || value == "" {
		return fmt.Errorf("invalid task revision")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return fmt.Errorf("invalid task revision")
	}
	*revision = taskRsyncVersioningTaskRevision(parsed)
	return nil
}

// CreatePreflight godoc
// @Summary      检查 Rsync 版本化迁移条件
// @Description  对一个 legacy Rsync Task 执行有界本地预检；响应不包含路径、locator、命令或文件系统原始证据
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path  int                                  true  "Task ID"
// @Param        body  body  taskRsyncVersioningPreflightRequest  true  "Rsync 版本化预检请求"
// @Success      200   {object} handlers.Response{data=backupasset.RsyncVersioningPreflightResult}
// @Failure      400   {object} handlers.Response
// @Failure      401   {object} handlers.Response
// @Failure      403   {object} handlers.Response
// @Failure      404   {object} handlers.Response
// @Failure      409   {object} handlers.Response
// @Failure      501   {object} handlers.Response
// @Failure      503   {object} handlers.Response
// @Router       /tasks/{id}/rsync-versioning/preflights [post]
func (handler *TaskRsyncVersioningHandler) CreatePreflight(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("rsync versioning service unavailable"))
		return
	}
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var payload taskRsyncVersioningPreflightRequest
	if err := decodeStrictBackupRepositoryJSON(c, &payload); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request := backupasset.RsyncVersioningPreflightRequest{
		TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision), RequestedMode: payload.RequestedMode,
	}
	if err := request.Validate(); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.CreateRsyncVersioningPreflightForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	if err != nil {
		respondTaskRsyncVersioningError(c, err)
		return
	}
	respondOK(c, result)
}

// Activate godoc
// @Summary      激活 Rsync 版本化迁移
// @Description  消费精确预检，并显式选择 imported_baseline 或 first_new_point；任务保持暂停直到受管流程完成
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path  int                                   true  "Task ID"
// @Param        body  body  taskRsyncVersioningActivationRequest  true  "Rsync 版本化激活请求"
// @Success      200   {object} handlers.Response{data=backupasset.RsyncVersioningActivationResult}
// @Failure      400   {object} handlers.Response
// @Failure      401   {object} handlers.Response
// @Failure      403   {object} handlers.Response
// @Failure      404   {object} handlers.Response
// @Failure      409   {object} handlers.Response
// @Failure      501   {object} handlers.Response
// @Failure      503   {object} handlers.Response
// @Router       /tasks/{id}/rsync-versioning/activate [post]
func (handler *TaskRsyncVersioningHandler) Activate(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("rsync versioning service unavailable"))
		return
	}
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var payload taskRsyncVersioningActivationRequest
	if err := decodeStrictBackupRepositoryJSON(c, &payload); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request := backupasset.RsyncVersioningActivationRequest{
		TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision), PreflightID: payload.PreflightID, MigrationChoice: payload.MigrationChoice,
	}
	if err := request.Validate(); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.ActivateRsyncVersioningForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	if err != nil {
		respondTaskRsyncVersioningError(c, err)
		return
	}
	respondOK(c, result)
}

// PrepareRollback godoc
// @Summary      准备 Rsync 版本化回退
// @Description  排空受管 admission 并保留所有已提交恢复点；不会删除 Provider tree 或重新启用 mutable 写入
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path  int                                 true  "Task ID"
// @Param        body  body  taskRsyncVersioningRollbackRequest  true  "Rsync 回退准备请求"
// @Success      200   {object} handlers.Response{data=backupasset.RsyncVersioningRollbackPreparationResult}
// @Failure      400   {object} handlers.Response
// @Failure      401   {object} handlers.Response
// @Failure      403   {object} handlers.Response
// @Failure      404   {object} handlers.Response
// @Failure      409   {object} handlers.Response
// @Failure      501   {object} handlers.Response
// @Failure      503   {object} handlers.Response
// @Router       /tasks/{id}/rsync-versioning/rollback-preparations [post]
func (handler *TaskRsyncVersioningHandler) PrepareRollback(c *gin.Context) {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("rsync versioning service unavailable"))
		return
	}
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var payload taskRsyncVersioningRollbackRequest
	if err := decodeStrictBackupRepositoryJSON(c, &payload); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request := backupasset.RsyncVersioningRollbackPreparationRequest{TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision)}
	if err := request.Validate(); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.PrepareRsyncVersioningRollbackForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	if err != nil {
		respondTaskRsyncVersioningError(c, err)
		return
	}
	respondOK(c, result)
}

func respondTaskRsyncVersioningError(c *gin.Context, err error) {
	if reason, correlationID, ok := backuprepository.CapabilityFromError(err); ok {
		if correlationID == "" {
			correlationID = c.GetString(middleware.RequestIDKey)
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
	case errors.Is(err, backupasset.ErrForbidden):
		respondForbidden(c, "Rsync 版本化迁移被阻止")
	case errors.Is(err, backupasset.ErrNotFound):
		respondNotFound(c, "任务不存在")
	case errors.Is(err, backupasset.ErrConflict):
		respondConflict(c, "Rsync 版本化状态冲突")
	case errors.Is(err, context.DeadlineExceeded):
		respondBackupCapabilityError(c, http.StatusServiceUnavailable, backupasset.CapabilityReason{Code: backupasset.CapabilityProviderOperationTimeout}, c.GetString(middleware.RequestIDKey))
	case errors.Is(err, backupasset.ErrCapabilityUnavailable):
		respondNotImplemented(c, "当前环境不支持 Rsync 版本化迁移")
	default:
		respondInternalError(c, err)
	}
}
