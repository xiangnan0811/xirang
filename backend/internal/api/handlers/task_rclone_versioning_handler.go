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

// TaskRcloneVersioningService is the narrow write-only Rclone migration
// boundary exposed to HTTP. Provider identities and credentials never appear
// in a read method or response DTO.
type TaskRcloneVersioningService interface {
	CreateRclonePortableBindingSetupForRequest(context.Context, backupasset.RcloneBindingSetupRequest, backuprepository.RequestContext) (backupasset.RcloneBindingSetupResult, error)
	SetRclonePortableBindingForRequest(context.Context, backupasset.RclonePortableBindingRequest, backuprepository.RequestContext) (backupasset.RclonePublicationSummary, error)
	CreateRcloneNativeBindingSetupForRequest(context.Context, backupasset.RcloneBindingSetupRequest, backuprepository.RequestContext) (backupasset.RcloneBindingSetupResult, error)
	SetRcloneNativeBindingForRequest(context.Context, backupasset.RcloneNativeBindingRequest, backuprepository.RequestContext) (backupasset.RclonePublicationSummary, error)
	CreateRcloneVersioningPreflightForRequest(context.Context, backupasset.RcloneVersioningPreflightRequest, backuprepository.RequestContext) (backupasset.RcloneVersioningPreflightResult, error)
	ActivateRcloneVersioningForRequest(context.Context, backupasset.RcloneVersioningActivationRequest, backuprepository.RequestContext) (backupasset.RcloneVersioningActivationResult, error)
	CleanRollbackRcloneVersioningForRequest(context.Context, backupasset.RcloneVersioningCleanRollbackRequest, backuprepository.RequestContext) (backupasset.RcloneVersioningRollbackResult, error)
	PrepareRcloneVersioningRollbackForRequest(context.Context, backupasset.RcloneVersioningRollbackPreparationRequest, backuprepository.RequestContext) (backupasset.RcloneVersioningRollbackResult, error)
	RcloneVersioningSummary(context.Context, uint) (backupasset.RclonePublicationSummary, error)
}

type TaskRcloneVersioningHandler struct {
	service TaskRcloneVersioningService
}

func NewTaskRcloneVersioningHandler(service TaskRcloneVersioningService) *TaskRcloneVersioningHandler {
	return &TaskRcloneVersioningHandler{service: service}
}

type taskRcloneVersioningRevision uint64

func (revision *taskRcloneVersioningRevision) UnmarshalJSON(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("invalid revision")
	}
	value := string(trimmed)
	if trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return err
		}
	}
	if strings.TrimSpace(value) != value || value == "" || len(value) > 20 || len(value) > 1 && value[0] == '0' {
		return fmt.Errorf("invalid revision")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid revision")
	}
	*revision = taskRcloneVersioningRevision(parsed)
	return nil
}

type taskRcloneBindingSetupRequest struct {
	ExpectedTaskRevision taskRcloneVersioningRevision `json:"expected_task_revision" swaggertype:"string"`
}

type taskRclonePortableBindingRequest struct {
	ExpectedTaskRevision    taskRcloneVersioningRevision `json:"expected_task_revision" swaggertype:"string"`
	ExpectedBindingRevision taskRcloneVersioningRevision `json:"expected_binding_revision" swaggertype:"string"`
	SetupID                 string                       `json:"setup_id"`
	TargetRemote            string                       `json:"target_remote"`
	ManagedRootLocator      string                       `json:"managed_root_locator"`
	BoundConfig             string                       `json:"bound_config"`
}

type taskRcloneNativeBootstrapRequest struct {
	Mode            backupasset.RcloneNativeBootstrapMode `json:"mode"`
	AccessKeyID     string                                `json:"access_key_id,omitempty"`
	SecretAccessKey string                                `json:"secret_access_key,omitempty"`
}

type taskRcloneNativeBindingRequest struct {
	ExpectedTaskRevision    taskRcloneVersioningRevision        `json:"expected_task_revision" swaggertype:"string"`
	ExpectedBindingRevision taskRcloneVersioningRevision        `json:"expected_binding_revision" swaggertype:"string"`
	SetupID                 string                              `json:"setup_id"`
	Region                  string                              `json:"region"`
	Bucket                  string                              `json:"bucket"`
	ManagedPrefix           string                              `json:"managed_prefix"`
	RoleARN                 string                              `json:"role_arn"`
	Bootstrap               taskRcloneNativeBootstrapRequest    `json:"bootstrap"`
	EncryptionProfile       backupasset.RcloneEncryptionProfile `json:"encryption_profile"`
	KMSKeyARN               string                              `json:"kms_key_arn,omitempty"`
}

type taskRclonePreflightRequest struct {
	ExpectedTaskRevision taskRcloneVersioningRevision    `json:"expected_task_revision" swaggertype:"string"`
	RequestedMode        backupasset.TaskPublicationMode `json:"requested_mode"`
}

type taskRcloneActivationRequest struct {
	ExpectedTaskRevision taskRcloneVersioningRevision                `json:"expected_task_revision" swaggertype:"string"`
	PreflightID          string                                      `json:"preflight_id"`
	MigrationChoice      backupasset.RcloneVersioningMigrationChoice `json:"migration_choice"`
}

type taskRcloneRollbackRequest struct {
	ExpectedTaskRevision    taskRcloneVersioningRevision `json:"expected_task_revision" swaggertype:"string"`
	ExpectedBindingRevision taskRcloneVersioningRevision `json:"expected_binding_revision" swaggertype:"string"`
}

// CreatePortableBindingSetup godoc
// @Summary      创建 Rclone portable 绑定设置
// @Description  创建短期一次性 setup；响应不包含 Remote 或配置
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path int true "Task ID"
// @Param        body body taskRcloneBindingSetupRequest true "Rclone portable setup 请求"
// @Success      200 {object} handlers.Response{data=backupasset.RcloneBindingSetupResult}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      501 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /tasks/{id}/rclone-versioning/portable-binding-setups [post]
func (handler *TaskRcloneVersioningHandler) CreatePortableBindingSetup(c *gin.Context) {
	taskID, payload, ok := handler.bindSetup(c)
	if !ok {
		return
	}
	request := backupasset.RcloneBindingSetupRequest{TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision)}
	if request.Validate() != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.CreateRclonePortableBindingSetupForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	if err != nil {
		respondTaskRcloneVersioningError(c, err)
		return
	}
	if result.Validate(false) != nil {
		respondInternalError(c, fmt.Errorf("invalid portable Rclone setup result"))
		return
	}
	respondOK(c, result)
}

// SetPortableBinding godoc
// @Summary      写入 Rclone portable 绑定
// @Description  一次性接收 private Remote、managed root 与 bound config；响应只返回安全摘要
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path int true "Task ID"
// @Param        body body taskRclonePortableBindingRequest true "Rclone portable 绑定请求"
// @Success      200 {object} handlers.Response{data=backupasset.RclonePublicationSummary}
// @Router       /tasks/{id}/rclone-versioning/portable-binding [put]
func (handler *TaskRcloneVersioningHandler) SetPortableBinding(c *gin.Context) {
	if !handler.available(c) {
		return
	}
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var payload taskRclonePortableBindingRequest
	if decodeStrictRcloneVersioningJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request := backupasset.RclonePortableBindingRequest{
		TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision),
		ExpectedBindingRevision: uint64(payload.ExpectedBindingRevision), SetupID: payload.SetupID,
		TargetRemote: payload.TargetRemote, ManagedRootLocator: payload.ManagedRootLocator, BoundConfig: payload.BoundConfig,
	}
	if request.Validate() != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.SetRclonePortableBindingForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	if err != nil {
		respondTaskRcloneVersioningError(c, err)
		return
	}
	respondOK(c, backupasset.SafeRclonePublicationSummary(result))
}

// CreateNativeBindingSetup godoc
// @Summary      创建 Rclone native 绑定设置
// @Description  创建短期 setup 与仅本响应可见的 external ID
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path int true "Task ID"
// @Param        body body taskRcloneBindingSetupRequest true "Rclone native setup 请求"
// @Success      200 {object} handlers.Response{data=backupasset.RcloneBindingSetupResult}
// @Router       /tasks/{id}/rclone-versioning/native-binding-setups [post]
func (handler *TaskRcloneVersioningHandler) CreateNativeBindingSetup(c *gin.Context) {
	taskID, payload, ok := handler.bindSetup(c)
	if !ok {
		return
	}
	request := backupasset.RcloneBindingSetupRequest{TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision)}
	if request.Validate() != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.CreateRcloneNativeBindingSetupForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	if err != nil {
		respondTaskRcloneVersioningError(c, err)
		return
	}
	if result.Validate(true) != nil {
		respondInternalError(c, fmt.Errorf("invalid native Rclone setup result"))
		return
	}
	respondOK(c, result)
}

// SetNativeBinding godoc
// @Summary      写入 Rclone AWS native 绑定
// @Description  一次性接收 STS 与加密配置；响应不回显 provider identity 或凭据
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path int true "Task ID"
// @Param        body body taskRcloneNativeBindingRequest true "Rclone native 绑定请求"
// @Success      200 {object} handlers.Response{data=backupasset.RclonePublicationSummary}
// @Router       /tasks/{id}/rclone-versioning/native-binding [put]
func (handler *TaskRcloneVersioningHandler) SetNativeBinding(c *gin.Context) {
	if !handler.available(c) {
		return
	}
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var payload taskRcloneNativeBindingRequest
	if decodeStrictRcloneVersioningJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request := backupasset.RcloneNativeBindingRequest{
		TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision),
		ExpectedBindingRevision: uint64(payload.ExpectedBindingRevision), SetupID: payload.SetupID,
		Region: payload.Region, Bucket: payload.Bucket, ManagedPrefix: payload.ManagedPrefix, RoleARN: payload.RoleARN,
		Bootstrap: backupasset.RcloneNativeBootstrapInput{
			Mode: payload.Bootstrap.Mode, AccessKeyID: payload.Bootstrap.AccessKeyID, SecretAccessKey: payload.Bootstrap.SecretAccessKey,
		},
		EncryptionProfile: payload.EncryptionProfile, KMSKeyARN: payload.KMSKeyARN,
	}
	if request.Validate() != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.SetRcloneNativeBindingForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	if err != nil {
		respondTaskRcloneVersioningError(c, err)
		return
	}
	respondOK(c, backupasset.SafeRclonePublicationSummary(result))
}

// CreatePreflight godoc
// @Summary      执行 Rclone 版本化预检
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path int true "Task ID"
// @Param        body body taskRclonePreflightRequest true "Rclone 预检请求"
// @Success      200 {object} handlers.Response{data=backupasset.RcloneVersioningPreflightResult}
// @Router       /tasks/{id}/rclone-versioning/preflights [post]
func (handler *TaskRcloneVersioningHandler) CreatePreflight(c *gin.Context) {
	if !handler.available(c) {
		return
	}
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var payload taskRclonePreflightRequest
	if decodeStrictRcloneVersioningJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request := backupasset.RcloneVersioningPreflightRequest{TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision), RequestedMode: payload.RequestedMode}
	if request.Validate() != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.CreateRcloneVersioningPreflightForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	if err != nil {
		respondTaskRcloneVersioningError(c, err)
		return
	}
	result.Summary = backupasset.SafeRclonePublicationSummary(result.Summary)
	respondOK(c, result)
}

// Activate godoc
// @Summary      激活 Rclone 版本化
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path int true "Task ID"
// @Param        body body taskRcloneActivationRequest true "Rclone 激活请求"
// @Success      200 {object} handlers.Response{data=backupasset.RcloneVersioningActivationResult}
// @Router       /tasks/{id}/rclone-versioning/activate [post]
func (handler *TaskRcloneVersioningHandler) Activate(c *gin.Context) {
	if !handler.available(c) {
		return
	}
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var payload taskRcloneActivationRequest
	if decodeStrictRcloneVersioningJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request := backupasset.RcloneVersioningActivationRequest{
		TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision), PreflightID: payload.PreflightID, MigrationChoice: payload.MigrationChoice,
	}
	if request.Validate() != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.service.ActivateRcloneVersioningForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	if err != nil {
		respondTaskRcloneVersioningError(c, err)
		return
	}
	result.Summary = backupasset.SafeRclonePublicationSummary(result.Summary)
	respondOK(c, result)
}

// CleanRollback godoc
// @Summary      清理回退未产生历史的 Rclone 版本化激活
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path int true "Task ID"
// @Param        body body taskRcloneRollbackRequest true "Rclone clean rollback 请求"
// @Success      200 {object} handlers.Response{data=backupasset.RcloneVersioningRollbackResult}
// @Router       /tasks/{id}/rclone-versioning/clean-rollbacks [post]
func (handler *TaskRcloneVersioningHandler) CleanRollback(c *gin.Context) {
	handler.handleRollback(c, true)
}

// PrepareRollback godoc
// @Summary      准备保留证据的 Rclone 版本化回退
// @Tags         tasks
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path int true "Task ID"
// @Param        body body taskRcloneRollbackRequest true "Rclone rollback preparation 请求"
// @Success      200 {object} handlers.Response{data=backupasset.RcloneVersioningRollbackResult}
// @Router       /tasks/{id}/rclone-versioning/rollback-preparations [post]
func (handler *TaskRcloneVersioningHandler) PrepareRollback(c *gin.Context) {
	handler.handleRollback(c, false)
}

func (handler *TaskRcloneVersioningHandler) handleRollback(c *gin.Context, clean bool) {
	if !handler.available(c) {
		return
	}
	taskID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var payload taskRcloneRollbackRequest
	if decodeStrictRcloneVersioningJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	var result backupasset.RcloneVersioningRollbackResult
	var err error
	if clean {
		request := backupasset.RcloneVersioningCleanRollbackRequest{
			TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision), ExpectedBindingRevision: uint64(payload.ExpectedBindingRevision),
		}
		if request.Validate() != nil {
			respondBadRequest(c, "请求参数不合法")
			return
		}
		result, err = handler.service.CleanRollbackRcloneVersioningForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	} else {
		request := backupasset.RcloneVersioningRollbackPreparationRequest{
			TaskID: taskID, ExpectedTaskRevision: uint64(payload.ExpectedTaskRevision), ExpectedBindingRevision: uint64(payload.ExpectedBindingRevision),
		}
		if request.Validate() != nil {
			respondBadRequest(c, "请求参数不合法")
			return
		}
		result, err = handler.service.PrepareRcloneVersioningRollbackForRequest(c.Request.Context(), request, backupRepositoryRequestContext(c))
	}
	if err != nil {
		respondTaskRcloneVersioningError(c, err)
		return
	}
	result.Summary = backupasset.SafeRclonePublicationSummary(result.Summary)
	respondOK(c, result)
}

func (handler *TaskRcloneVersioningHandler) bindSetup(c *gin.Context) (uint, taskRcloneBindingSetupRequest, bool) {
	if !handler.available(c) {
		return 0, taskRcloneBindingSetupRequest{}, false
	}
	taskID, ok := parseID(c, "id")
	if !ok {
		return 0, taskRcloneBindingSetupRequest{}, false
	}
	var payload taskRcloneBindingSetupRequest
	if decodeStrictRcloneVersioningJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return 0, taskRcloneBindingSetupRequest{}, false
	}
	return taskID, payload, true
}

func (handler *TaskRcloneVersioningHandler) available(c *gin.Context) bool {
	if handler == nil || handler.service == nil {
		respondInternalError(c, fmt.Errorf("rclone versioning service unavailable"))
		return false
	}
	return true
}

func decodeStrictRcloneVersioningJSON(c *gin.Context, target any) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return fmt.Errorf("request body missing")
	}
	payload, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBackupRepositoryRequestBytes+1))
	if err != nil || len(payload) > maxBackupRepositoryRequestBytes {
		return fmt.Errorf("request body exceeds limit")
	}
	if err := rejectDuplicateOrNullRcloneJSON(payload); err != nil {
		return err
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

func rejectDuplicateOrNullRcloneJSON(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if err := walkRcloneJSONValue(decoder, token, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing request data")
	}
	return nil
}

func walkRcloneJSONValue(decoder *json.Decoder, token json.Token, depth int) error {
	if depth > 32 || token == nil {
		return fmt.Errorf("invalid JSON value")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("invalid JSON member")
			}
			if _, exists := members[name]; exists {
				return fmt.Errorf("duplicate JSON member")
			}
			members[name] = struct{}{}
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := walkRcloneJSONValue(decoder, value, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			value, err := decoder.Token()
			if err != nil {
				return err
			}
			if err := walkRcloneJSONValue(decoder, value, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid JSON delimiter")
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	expected := json.Delim('}')
	if delimiter == '[' {
		expected = ']'
	}
	if closing != expected {
		return fmt.Errorf("invalid JSON closing delimiter")
	}
	return nil
}

func respondTaskRcloneVersioningError(c *gin.Context, err error) {
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
		respondForbidden(c, "Rclone 版本化操作被阻止")
	case errors.Is(err, backupasset.ErrNotFound):
		respondNotFound(c, "任务不存在")
	case errors.Is(err, backupasset.ErrConflict):
		respondConflict(c, "Rclone 版本化状态冲突")
	case errors.Is(err, context.DeadlineExceeded):
		respondBackupCapabilityError(c, http.StatusServiceUnavailable, backupasset.CapabilityReason{Code: backupasset.CapabilityProviderOperationTimeout}, c.GetString(middleware.RequestIDKey))
	case errors.Is(err, backupasset.ErrCapabilityUnavailable):
		respondNotImplemented(c, "当前环境不支持 Rclone 版本化操作")
	default:
		respondInternalError(c, err)
	}
}
