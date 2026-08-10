package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	backupRecoveryAuthorizationSchemaVersion  = 1
	maxBackupRecoveryAuthorizationBodyBytes   = 8 << 10
	maxBackupRecoveryAuthorizationReasonBytes = 2048
	minBackupRecoveryIdempotencyKeyBytes      = 16
	maxBackupRecoveryIdempotencyKeyBytes      = 256
	maxBackupRecoveryStepUpProofBytes         = 8 << 10

	backupRecoverySecurityOverrideEndpoint    = "/api/v1/recovery-plans/:id/security-overrides"
	backupRecoveryWriteAuthorizationEndpoint  = "/api/v1/recovery-plans/:id/write-authorizations"
	backupRecoveryDeleteAuthorizationEndpoint = "/api/v1/recovery-jobs/:id/exact-mirror-delete-authorizations"
	backupRecoveryExecuteEndpoint             = "/api/v1/recovery-plans/:id/execute"
)

type RecoveryAuthorizationHandlerService interface {
	ReplayAuthorization(context.Context, recovery.RecoveryAuthorizationRequest) (recovery.RecoveryAuthorizationResult, bool, error)
	Authorize(context.Context, recovery.RecoveryAuthorizationRequest) (recovery.RecoveryAuthorizationResult, error)
}

type BackupRecoveryHandler struct {
	service    RecoveryAuthorizationHandlerService
	db         *gorm.DB
	jwtManager *auth.JWTManager
}

type backupRecoveryAuthorizationEndpoint struct {
	operation recovery.AuthorizationReceiptOperation
	category  recovery.AuthorizationReceiptCategory
	template  string
}

type backupRecoverySecurityOverridePayload struct {
	SchemaVersion    int    `json:"schema_version"`
	ExpectedRevision string `json:"expected_revision"`
	PreflightID      string `json:"preflight_id"`
	FindingCategory  string `json:"finding_category"`
	Reason           string `json:"reason"`
}

type backupRecoveryWriteAuthorizationPayload struct {
	SchemaVersion    int    `json:"schema_version"`
	ExpectedRevision string `json:"expected_revision"`
	PreflightID      string `json:"preflight_id"`
	Reason           string `json:"reason"`
	GrantSecret      string `json:"grant_secret"`
}

type backupRecoveryDeleteAuthorizationPayload struct {
	SchemaVersion    int    `json:"schema_version"`
	PlanID           string `json:"plan_id"`
	CheckpointID     string `json:"checkpoint_id"`
	AttemptID        string `json:"attempt_id"`
	ExpectedRevision string `json:"expected_revision"`
	Reason           string `json:"reason"`
	GrantSecret      string `json:"grant_secret"`
}

type backupRecoveryExecutePayload struct {
	SchemaVersion    int    `json:"schema_version"`
	ExpectedRevision string `json:"expected_revision"`
	PreflightID      string `json:"preflight_id"`
	GrantID          string `json:"grant_id"`
	GrantSecret      string `json:"grant_secret"`
}

type backupRecoveryAuthorizationResponse struct {
	SchemaVersion          int                                       `json:"schema_version"`
	ReceiptID              string                                    `json:"receipt_id"`
	PlanID                 string                                    `json:"plan_id"`
	GrantID                string                                    `json:"grant_id,omitempty"`
	GrantCategory          recovery.AuthorityCategory                `json:"grant_category,omitempty"`
	GrantBindingDigest     string                                    `json:"grant_binding_digest,omitempty"`
	GrantExpiresAt         *time.Time                                `json:"grant_expires_at,omitempty"`
	GrantStatus            recovery.RecoveryAuthorizationGrantStatus `json:"grant_status,omitempty"`
	JobID                  string                                    `json:"job_id,omitempty"`
	Operation              recovery.AuthorizationReceiptOperation    `json:"operation"`
	Category               recovery.AuthorizationReceiptCategory     `json:"category"`
	PlanTransitionRevision string                                    `json:"plan_transition_revision"`
	Replay                 bool                                      `json:"replay"`
}

func NewBackupRecoveryHandler(
	service RecoveryAuthorizationHandlerService,
	db *gorm.DB,
	jwtManager *auth.JWTManager,
) *BackupRecoveryHandler {
	return &BackupRecoveryHandler{service: service, db: db, jwtManager: jwtManager}
}

func (handler *BackupRecoveryHandler) SecurityOverride(c *gin.Context) {
	endpoint, request, ok := handler.prepareAuthorization(c, recovery.AuthorizationReceiptSecurityOverride)
	if !ok {
		return
	}
	planID, idOK := backupAssetOpaqueParam(c, "id")
	var payload backupRecoverySecurityOverridePayload
	if !idOK || decodeStrictBackupRecoveryAuthorizationJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	revision, revisionOK := backupRecoveryAuthorizationRevision(payload.ExpectedRevision)
	findingCategory, findingOK := backupRecoverySecurityFindingCategory(payload.FindingCategory)
	if payload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion || !revisionOK ||
		!backupRecoveryAuthorizationOpaqueID(payload.PreflightID) ||
		!backupRecoveryAuthorizationReason(payload.Reason) || !findingOK {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request.PlanID = planID
	request.ExpectedPlanRevision = revision
	request.PreflightID = payload.PreflightID
	request.FindingCategory = findingCategory
	request.Reason = payload.Reason
	handler.authorize(c, endpoint, request)
}

func (handler *BackupRecoveryHandler) AuthorizeWrite(c *gin.Context) {
	endpoint, request, ok := handler.prepareAuthorization(c, recovery.AuthorizationReceiptWriteAuthorize)
	if !ok {
		return
	}
	planID, idOK := backupAssetOpaqueParam(c, "id")
	var payload backupRecoveryWriteAuthorizationPayload
	if !idOK || decodeStrictBackupRecoveryAuthorizationJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	revision, revisionOK := backupRecoveryAuthorizationRevision(payload.ExpectedRevision)
	if payload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion || !revisionOK ||
		!backupRecoveryAuthorizationOpaqueID(payload.PreflightID) ||
		!backupRecoveryAuthorizationReason(payload.Reason) ||
		!backupRecoveryAuthorizationGrantSecret(payload.GrantSecret) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request.PlanID = planID
	request.ExpectedPlanRevision = revision
	request.PreflightID = payload.PreflightID
	request.Reason = payload.Reason
	request.GrantSecret = payload.GrantSecret
	handler.authorize(c, endpoint, request)
}

func (handler *BackupRecoveryHandler) AuthorizeExactMirrorDelete(c *gin.Context) {
	endpoint, request, ok := handler.prepareAuthorization(c, recovery.AuthorizationReceiptDeleteAuthorize)
	if !ok {
		return
	}
	jobID, idOK := backupAssetOpaqueParam(c, "id")
	var payload backupRecoveryDeleteAuthorizationPayload
	if !idOK || decodeStrictBackupRecoveryAuthorizationJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	revision, revisionOK := backupRecoveryAuthorizationRevision(payload.ExpectedRevision)
	if payload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion || !revisionOK ||
		!backupRecoveryAuthorizationOpaqueID(payload.PlanID) ||
		!backupRecoveryAuthorizationOpaqueID(payload.CheckpointID) ||
		!backupRecoveryAuthorizationOpaqueID(payload.AttemptID) ||
		!backupRecoveryAuthorizationReason(payload.Reason) ||
		!backupRecoveryAuthorizationGrantSecret(payload.GrantSecret) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request.PlanID = payload.PlanID
	request.JobID = jobID
	request.CheckpointID = payload.CheckpointID
	request.AttemptID = payload.AttemptID
	request.ExpectedPlanRevision = revision
	request.Reason = payload.Reason
	request.GrantSecret = payload.GrantSecret
	handler.authorize(c, endpoint, request)
}

func (handler *BackupRecoveryHandler) Execute(c *gin.Context) {
	endpoint, request, ok := handler.prepareAuthorization(c, recovery.AuthorizationReceiptExecute)
	if !ok {
		return
	}
	planID, idOK := backupAssetOpaqueParam(c, "id")
	var payload backupRecoveryExecutePayload
	if !idOK || decodeStrictBackupRecoveryAuthorizationJSON(c, &payload) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	revision, revisionOK := backupRecoveryAuthorizationRevision(payload.ExpectedRevision)
	if payload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion || !revisionOK ||
		!backupRecoveryAuthorizationOpaqueID(payload.PreflightID) ||
		!backupRecoveryAuthorizationOpaqueID(payload.GrantID) ||
		!backupRecoveryAuthorizationGrantSecret(payload.GrantSecret) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request.PlanID = planID
	request.ExpectedPlanRevision = revision
	request.PreflightID = payload.PreflightID
	request.GrantID = payload.GrantID
	request.GrantSecret = payload.GrantSecret
	handler.authorize(c, endpoint, request)
}

func (handler *BackupRecoveryHandler) prepareAuthorization(
	c *gin.Context,
	operation recovery.AuthorizationReceiptOperation,
) (backupRecoveryAuthorizationEndpoint, recovery.RecoveryAuthorizationRequest, bool) {
	endpoint, ok := backupRecoveryAuthorizationEndpointFor(operation)
	if !ok || c == nil || c.Request == nil || c.Request.URL == nil ||
		c.Request.Method != http.MethodPost || c.FullPath() != endpoint.template ||
		c.Request.URL.RawQuery != "" || !backupRecoveryAuthorizationJSONContentType(c.Request) {
		if c != nil {
			respondBadRequest(c, "请求参数不合法")
		}
		return backupRecoveryAuthorizationEndpoint{}, recovery.RecoveryAuthorizationRequest{}, false
	}
	if handler == nil || handler.service == nil {
		respondServiceUnavailable(c, "恢复授权服务暂不可用")
		return backupRecoveryAuthorizationEndpoint{}, recovery.RecoveryAuthorizationRequest{}, false
	}
	idempotencyKey, ok := backupRecoveryAuthorizationIdempotencyKey(c.Request)
	if !ok {
		respondBadRequest(c, "请求参数不合法")
		return backupRecoveryAuthorizationEndpoint{}, recovery.RecoveryAuthorizationRequest{}, false
	}
	requesterID := middleware.CurrentUserID(c)
	role := middleware.CurrentRole(c)
	session, ok := middleware.CurrentSessionBinding(c)
	if !ok || !backupRecoveryAuthorizationSession(session, requesterID, role, time.Now().UTC()) {
		respondForbidden(c, "权限不足")
		return backupRecoveryAuthorizationEndpoint{}, recovery.RecoveryAuthorizationRequest{}, false
	}
	return endpoint, recovery.RecoveryAuthorizationRequest{
		RequesterID:    requesterID,
		Endpoint:       endpoint.template,
		IdempotencyKey: idempotencyKey,
		Operation:      endpoint.operation,
		Category:       endpoint.category,
		Session: recovery.RecoveryAuthorizationSession{
			JTI:          session.JTI,
			UserID:       session.UserID,
			Role:         session.Role,
			TokenVersion: session.TokenVersion,
			ExpiresAt:    session.ExpiresAt.UTC(),
		},
	}, true
}

func (handler *BackupRecoveryHandler) authorize(
	c *gin.Context,
	endpoint backupRecoveryAuthorizationEndpoint,
	request recovery.RecoveryAuthorizationRequest,
) {
	result, found, err := handler.service.ReplayAuthorization(c.Request.Context(), request)
	if err != nil {
		respondBackupRecoveryAuthorizationError(c, err)
		return
	}
	if found {
		handler.respondAuthorization(c, endpoint, request, result, true)
		return
	}
	proof, ok := backupRecoveryAuthorizationStepUpProof(c.Request)
	if !ok {
		respondStepUpRequired(c)
		return
	}
	claims, err := validateStepUpProof(
		handler.db,
		handler.jwtManager,
		proof,
		request.RequesterID,
		request.Session.Role,
		auth.StepUpActionAssetRecover,
	)
	if err != nil {
		if errors.Is(err, ErrStepUpVerifierUnavailable) {
			respondServiceUnavailable(c, "二次验证服务暂不可用")
		} else {
			respondStepUpRequired(c)
		}
		return
	}
	if claims == nil || claims.ExpiresAt == nil || claims.ID == "" ||
		claims.UserID != request.Session.UserID || claims.Role != request.Session.Role ||
		claims.TokenVersion != request.Session.TokenVersion {
		respondForbidden(c, "权限不足")
		return
	}
	request.Proof = recovery.RecoveryAuthorizationProof{
		JTI:          claims.ID,
		Action:       string(claims.StepUpAction),
		UserID:       claims.UserID,
		Role:         claims.Role,
		TokenVersion: claims.TokenVersion,
		ExpiresAt:    claims.ExpiresAt.UTC(),
	}
	result, err = handler.service.Authorize(c.Request.Context(), request)
	if err != nil {
		respondBackupRecoveryAuthorizationError(c, err)
		return
	}
	handler.respondAuthorization(c, endpoint, request, result, false)
}

func (handler *BackupRecoveryHandler) respondAuthorization(
	c *gin.Context,
	endpoint backupRecoveryAuthorizationEndpoint,
	request recovery.RecoveryAuthorizationRequest,
	result recovery.RecoveryAuthorizationResult,
	replay bool,
) {
	response, ok := safeBackupRecoveryAuthorizationResponse(endpoint, request, result, replay)
	if !ok {
		respondInternalError(c, errors.New("invalid recovery authorization result"))
		return
	}
	if endpoint.operation == recovery.AuthorizationReceiptExecute {
		respondAccepted(c, response)
		return
	}
	respondOK(c, response)
}

func backupRecoveryAuthorizationEndpointFor(
	operation recovery.AuthorizationReceiptOperation,
) (backupRecoveryAuthorizationEndpoint, bool) {
	switch operation {
	case recovery.AuthorizationReceiptSecurityOverride:
		return backupRecoveryAuthorizationEndpoint{
			operation: operation,
			category:  recovery.AuthorizationReceiptCategorySecurityOverride,
			template:  backupRecoverySecurityOverrideEndpoint,
		}, true
	case recovery.AuthorizationReceiptWriteAuthorize:
		return backupRecoveryAuthorizationEndpoint{
			operation: operation,
			category:  recovery.AuthorizationReceiptCategoryWrite,
			template:  backupRecoveryWriteAuthorizationEndpoint,
		}, true
	case recovery.AuthorizationReceiptDeleteAuthorize:
		return backupRecoveryAuthorizationEndpoint{
			operation: operation,
			category:  recovery.AuthorizationReceiptCategoryExactMirrorDelete,
			template:  backupRecoveryDeleteAuthorizationEndpoint,
		}, true
	case recovery.AuthorizationReceiptExecute:
		return backupRecoveryAuthorizationEndpoint{
			operation: operation,
			category:  recovery.AuthorizationReceiptCategoryExecute,
			template:  backupRecoveryExecuteEndpoint,
		}, true
	default:
		return backupRecoveryAuthorizationEndpoint{}, false
	}
}

func safeBackupRecoveryAuthorizationResponse(
	endpoint backupRecoveryAuthorizationEndpoint,
	request recovery.RecoveryAuthorizationRequest,
	result recovery.RecoveryAuthorizationResult,
	replay bool,
) (backupRecoveryAuthorizationResponse, bool) {
	if !backupRecoveryAuthorizationOpaqueID(result.ReceiptID) ||
		!backupRecoveryAuthorizationOpaqueID(result.PlanID) || result.PlanID != request.PlanID ||
		result.PlanTransitionRevision == 0 {
		return backupRecoveryAuthorizationResponse{}, false
	}
	switch endpoint.operation {
	case recovery.AuthorizationReceiptSecurityOverride:
		if result.GrantID != "" || result.JobID != "" || result.AttemptID != "" ||
			result.SourceLeaseID != "" || result.NodeLeaseID != "" || result.NodeLeaseFence != 0 ||
			!backupRecoveryAuthorizationGrantMetadata(result, "", "") {
			return backupRecoveryAuthorizationResponse{}, false
		}
	case recovery.AuthorizationReceiptWriteAuthorize:
		if !backupRecoveryAuthorizationOpaqueID(result.GrantID) || result.JobID != "" || result.AttemptID != "" ||
			result.SourceLeaseID != "" || result.NodeLeaseID != "" || result.NodeLeaseFence != 0 ||
			!backupRecoveryAuthorizationGrantMetadata(result, recovery.AuthorityWrite,
				recovery.RecoveryAuthorizationGrantIssued) {
			return backupRecoveryAuthorizationResponse{}, false
		}
	case recovery.AuthorizationReceiptDeleteAuthorize:
		if !backupRecoveryAuthorizationOpaqueID(result.GrantID) ||
			!backupRecoveryAuthorizationOpaqueID(result.JobID) || result.JobID != request.JobID ||
			!backupRecoveryAuthorizationOpaqueID(result.AttemptID) || result.AttemptID != request.AttemptID ||
			result.SourceLeaseID != "" || result.NodeLeaseID != "" || result.NodeLeaseFence != 0 ||
			!backupRecoveryAuthorizationGrantMetadata(result, recovery.AuthorityExactMirrorDelete,
				recovery.RecoveryAuthorizationGrantIssued) {
			return backupRecoveryAuthorizationResponse{}, false
		}
	case recovery.AuthorizationReceiptExecute:
		if !backupRecoveryAuthorizationOpaqueID(result.GrantID) || result.GrantID != request.GrantID ||
			!backupRecoveryAuthorizationOpaqueID(result.JobID) ||
			!backupRecoveryAuthorizationOpaqueID(result.AttemptID) ||
			!backupRecoveryAuthorizationOpaqueID(result.SourceLeaseID) ||
			!backupRecoveryAuthorizationOpaqueID(result.NodeLeaseID) || result.NodeLeaseFence == 0 ||
			!backupRecoveryAuthorizationGrantMetadata(result, recovery.AuthorityWrite,
				recovery.RecoveryAuthorizationGrantConsumed) {
			return backupRecoveryAuthorizationResponse{}, false
		}
	default:
		return backupRecoveryAuthorizationResponse{}, false
	}
	return backupRecoveryAuthorizationResponse{
		SchemaVersion:          backupRecoveryAuthorizationSchemaVersion,
		ReceiptID:              result.ReceiptID,
		PlanID:                 result.PlanID,
		GrantID:                result.GrantID,
		GrantCategory:          result.GrantCategory,
		GrantBindingDigest:     result.GrantBindingDigest,
		GrantExpiresAt:         result.GrantExpiresAt,
		GrantStatus:            result.GrantStatus,
		JobID:                  result.JobID,
		Operation:              endpoint.operation,
		Category:               endpoint.category,
		PlanTransitionRevision: strconv.FormatUint(result.PlanTransitionRevision, 10),
		Replay:                 replay || result.Replay,
	}, true
}

func backupRecoveryAuthorizationGrantMetadata(
	result recovery.RecoveryAuthorizationResult,
	category recovery.AuthorityCategory,
	status recovery.RecoveryAuthorizationGrantStatus,
) bool {
	if category == "" {
		return result.GrantCategory == "" && result.GrantBindingDigest == "" &&
			result.GrantExpiresAt == nil && result.GrantStatus == ""
	}
	return result.GrantCategory == category && result.GrantStatus == status &&
		len(result.GrantBindingDigest) == 64 && lowerHexAPI(result.GrantBindingDigest) &&
		result.GrantExpiresAt != nil && !result.GrantExpiresAt.IsZero() &&
		result.GrantExpiresAt.Location() == time.UTC
}

func respondBackupRecoveryAuthorizationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, recovery.ErrInvalidRecoveryAuthorization),
		errors.Is(err, recovery.ErrInvalidAuthorizationGrantSecret):
		respondBadRequest(c, "请求参数不合法")
	case errors.Is(err, recovery.ErrAuthorizationIdempotencyConflict),
		errors.Is(err, recovery.ErrAuthorizationProofUsed):
		respondConflict(c, "授权请求冲突")
	case errors.Is(err, recovery.ErrAuthorizationProofLifetime):
		respondStepUpRequired(c)
	case errors.Is(err, recovery.ErrAuthorizationSessionMismatch),
		errors.Is(err, recovery.ErrAuthorizationDenied):
		respondForbidden(c, "权限不足")
	case errors.Is(err, recovery.ErrAuthorizationUnavailable),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		respondServiceUnavailable(c, "恢复授权服务暂不可用")
	default:
		respondInternalError(c, err)
	}
}

func decodeStrictBackupRecoveryAuthorizationJSON(c *gin.Context, target any) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return io.ErrUnexpectedEOF
	}
	payload, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBackupRecoveryAuthorizationBodyBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxBackupRecoveryAuthorizationBodyBytes ||
		rejectDuplicateBackupContentJSON(payload) != nil {
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

func backupRecoveryAuthorizationJSONContentType(request *http.Request) bool {
	if request == nil {
		return false
	}
	values := request.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" || len(parameters) > 1 {
		return false
	}
	if charset, ok := parameters["charset"]; ok && !strings.EqualFold(charset, "utf-8") {
		return false
	}
	return true
}

func backupRecoveryAuthorizationIdempotencyKey(request *http.Request) (string, bool) {
	if request == nil {
		return "", false
	}
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", false
	}
	value := values[0]
	if len(value) < minBackupRecoveryIdempotencyKeyBytes || len(value) > maxBackupRecoveryIdempotencyKeyBytes ||
		strings.TrimSpace(value) != value {
		return "", false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' &&
			character != '.' && character != '~' {
			return "", false
		}
	}
	return value, true
}

func backupRecoveryAuthorizationStepUpProof(request *http.Request) (string, bool) {
	if request == nil {
		return "", false
	}
	values := request.Header.Values(StepUpHeaderName)
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > maxBackupRecoveryStepUpProofBytes ||
		strings.TrimSpace(values[0]) != values[0] {
		return "", false
	}
	return values[0], true
}

func backupRecoveryAuthorizationSession(
	session middleware.SessionBinding,
	requesterID uint,
	role string,
	now time.Time,
) bool {
	return requesterID != 0 && role == "admin" && session.JTI != "" && len(session.JTI) <= 256 &&
		strings.TrimSpace(session.JTI) == session.JTI && session.UserID == requesterID &&
		session.Role == role && session.TokenVersion > 0 && now.Before(session.ExpiresAt.UTC())
}

func backupRecoveryAuthorizationRevision(value string) (uint64, bool) {
	if value == "" || strings.TrimSpace(value) != value {
		return 0, false
	}
	revision, err := strconv.ParseUint(value, 10, 64)
	return revision, err == nil && revision > 0 && strconv.FormatUint(revision, 10) == value
}

func backupRecoveryAuthorizationOpaqueID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && backupasset.ValidateOpaqueID(value) == nil
}

func backupRecoveryAuthorizationReason(value string) bool {
	return value != "" && len(value) <= maxBackupRecoveryAuthorizationReasonBytes &&
		strings.TrimSpace(value) == value
}

func backupRecoveryAuthorizationGrantSecret(value string) bool {
	if len(value) != 43 || strings.TrimSpace(value) != value || strings.Contains(value, "=") {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func backupRecoverySecurityFindingCategory(value string) (recovery.SecurityFindingCategory, bool) {
	category := recovery.SecurityFindingCategory(value)
	switch category {
	case recovery.SecurityFindingMalware,
		recovery.SecurityFindingSuspicious,
		recovery.SecurityFindingTestSignature:
		return category, true
	default:
		return "", false
	}
}
