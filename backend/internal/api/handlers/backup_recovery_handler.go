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
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/recovery"
	backupruntime "xirang/backend/internal/backupasset/runtime"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	backupRecoveryAuthorizationSchemaVersion  = 1
	maxBackupRecoveryAuthorizationBodyBytes   = 8 << 10
	maxBackupRecoveryCreatePlanBodyBytes      = 4 << 20
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

type RecoveryTargetRootHandlerService interface {
	ReplayRegistration(
		context.Context,
		recovery.TargetRootRegistrationRequest,
	) (settings.RecoveryTargetRootSummary, bool, error)
	Register(context.Context, recovery.TargetRootRegistrationRequest) (settings.RecoveryTargetRootSummary, error)
	DeleteAuthorized(
		context.Context,
		recovery.TargetRootDeletionRequest,
	) (settings.RecoveryTargetRootSummary, error)
	ReplayDeletion(
		context.Context,
		recovery.TargetRootDeletionRequest,
	) (settings.RecoveryTargetRootSummary, bool, error)
	List(context.Context, uint, uint) ([]settings.RecoveryTargetRootSummary, error)
}

type RecoveryDowngradeHandlerService interface {
	ReplayRecoveryDowngradeReadiness(
		context.Context,
		backupruntime.RecoveryDowngradeReadinessRequest,
	) (backupruntime.RecoveryDowngradeReadiness, bool, error)
	RequestRecoveryDowngradeReadiness(
		context.Context,
		backupruntime.RecoveryDowngradeReadinessRequest,
	) (backupruntime.RecoveryDowngradeReadiness, error)
}

type RecoveryLifecycleHandlerService interface {
	GetPlan(context.Context, uint, string) (recovery.RecoveryPlanView, error)
	CancelPlan(context.Context, recovery.RecoveryPlanMutationRequest) (recovery.RecoveryPlanView, error)
	GetJob(context.Context, uint, string) (recovery.RecoveryJobView, error)
}

type RecoveryOperationsHandlerService interface {
	CreatePlan(context.Context, recovery.CreatePlanIntentRequest) (recovery.CreatePlanResult, error)
	Preflight(context.Context, recovery.RecoveryPreflightRequest) (recovery.RecoveryPreflightView, error)
	CancelJob(context.Context, uint, string, uint64) (recovery.RecoveryJobView, error)
	RetainRecoveryResults(
		context.Context,
		recovery.RetainRecoveryResultsRequest,
	) (recovery.RetainedRecoveryResultSet, error)
	RequestResultCleanup(
		context.Context,
		recovery.RecoveryResultCleanupRequest,
	) (recovery.RecoveryResultCleanupView, error)
}

type BackupRecoveryHandler struct {
	service     RecoveryAuthorizationHandlerService
	targetRoots RecoveryTargetRootHandlerService
	downgrade   RecoveryDowngradeHandlerService
	lifecycle   RecoveryLifecycleHandlerService
	operations  RecoveryOperationsHandlerService
	db          *gorm.DB
	jwtManager  *auth.JWTManager
}

type backupRecoveryAuthorizationEndpoint struct {
	operation recovery.AuthorizationReceiptOperation
	category  recovery.AuthorizationReceiptCategory
	template  string
}

type backupRecoverySecurityOverridePayload struct {
	SchemaVersion    int    `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	ExpectedRevision string `json:"expected_revision" binding:"required" minLength:"1" maxLength:"20"`
	PreflightID      string `json:"preflight_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	FindingCategory  string `json:"finding_category" binding:"required" enums:"malware,suspicious,test_signature"`
	Reason           string `json:"reason" binding:"required" minLength:"1" maxLength:"2048"`
}

type backupRecoveryWriteAuthorizationPayload struct {
	SchemaVersion    int    `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	ExpectedRevision string `json:"expected_revision" binding:"required" minLength:"1" maxLength:"20"`
	PreflightID      string `json:"preflight_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	Reason           string `json:"reason" binding:"required" minLength:"1" maxLength:"2048"`
	GrantSecret      string `json:"grant_secret" binding:"required" minLength:"43" maxLength:"43" format:"password" extensions:"x-pattern=^[A-Za-z0-9_-]{43}$"`
}

type backupRecoveryDeleteAuthorizationPayload struct {
	SchemaVersion    int    `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	PlanID           string `json:"plan_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	CheckpointID     string `json:"checkpoint_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	AttemptID        string `json:"attempt_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	ExpectedRevision string `json:"expected_revision" binding:"required" minLength:"1" maxLength:"20"`
	Reason           string `json:"reason" binding:"required" minLength:"1" maxLength:"2048"`
	GrantSecret      string `json:"grant_secret" binding:"required" minLength:"43" maxLength:"43" format:"password" extensions:"x-pattern=^[A-Za-z0-9_-]{43}$"`
}

type backupRecoveryExecutePayload struct {
	SchemaVersion    int    `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	ExpectedRevision string `json:"expected_revision" binding:"required" minLength:"1" maxLength:"20"`
	PreflightID      string `json:"preflight_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	GrantID          string `json:"grant_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	GrantSecret      string `json:"grant_secret" binding:"required" minLength:"43" maxLength:"43" format:"password" extensions:"x-pattern=^[A-Za-z0-9_-]{43}$"`
}

type backupRecoveryTargetRootPayload struct {
	SchemaVersion        int    `json:"schema_version" minimum:"1" maximum:"1" example:"1"`
	NodeID               uint   `json:"node_id"`
	RootID               string `json:"root_id"`
	SafeLabel            string `json:"safe_label"`
	Locator              string `json:"locator"`
	ReserveBytes         int64  `json:"reserve_bytes" minimum:"0"`
	ReserveInodes        int64  `json:"reserve_inodes" minimum:"0"`
	OverlapPolicyBinding string `json:"overlap_policy_binding"`
}

// The registration and rotation documentation types keep route-specific
// required fields truthful while mutateTargetRoot uses one closed decoder.
type backupRecoveryTargetRootRegisterPayload struct {
	SchemaVersion        int    `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	NodeID               uint   `json:"node_id" binding:"required" minimum:"1"`
	RootID               string `json:"root_id" binding:"required" minLength:"1" maxLength:"32" extensions:"x-pattern=^[A-Za-z0-9_.-]+$"`
	SafeLabel            string `json:"safe_label" binding:"required" minLength:"1" maxLength:"128"`
	Locator              string `json:"locator" binding:"required" minLength:"1"`
	ReserveBytes         int64  `json:"reserve_bytes" minimum:"0"`
	ReserveInodes        int64  `json:"reserve_inodes" minimum:"0"`
	OverlapPolicyBinding string `json:"overlap_policy_binding" binding:"required" minLength:"1" maxLength:"256"`
}

type backupRecoveryTargetRootRotatePayload struct {
	SchemaVersion        int    `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	NodeID               uint   `json:"node_id,omitempty" minimum:"1"`
	RootID               string `json:"root_id,omitempty" minLength:"1" maxLength:"32" extensions:"x-pattern=^[A-Za-z0-9_.-]+$"`
	SafeLabel            string `json:"safe_label" binding:"required" minLength:"1" maxLength:"128"`
	Locator              string `json:"locator" binding:"required" minLength:"1"`
	ReserveBytes         int64  `json:"reserve_bytes" minimum:"0"`
	ReserveInodes        int64  `json:"reserve_inodes" minimum:"0"`
	OverlapPolicyBinding string `json:"overlap_policy_binding" binding:"required" minLength:"1" maxLength:"256"`
}

type backupRecoverySchemaPayload struct {
	SchemaVersion int `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
}

type backupRecoveryDowngradePayload struct {
	SchemaVersion int    `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	Reason        string `json:"reason" binding:"required" minLength:"1" maxLength:"2048"`
}

type backupRecoveryRevisionPayload struct {
	SchemaVersion    int    `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	ExpectedRevision string `json:"expected_revision" binding:"required" example:"1"`
}

type backupRecoveryCreatePlanPayload struct {
	SchemaVersion       int                     `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	RepositoryID        string                  `json:"repository_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	RecoveryPointID     string                  `json:"recovery_point_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	CatalogGenerationID string                  `json:"catalog_generation_id" binding:"required" minLength:"32" maxLength:"32" extensions:"x-pattern=^[0-9a-f]{32}$"`
	EntryIDs            []string                `json:"entry_ids" binding:"required,min=1,max=10000"`
	TargetMode          recovery.TargetMode     `json:"target_mode" binding:"required" enums:"isolated,in_place"`
	TargetNodeID        uint                    `json:"target_node_id" binding:"required" minimum:"1"`
	TargetRootID        string                  `json:"target_root_id" binding:"required" minLength:"1" maxLength:"128"`
	ConflictPolicy      recovery.ConflictPolicy `json:"conflict_policy" binding:"required" enums:"fail_on_conflict,skip_existing,overwrite_selected,exact_mirror"`
}

type backupRecoveryRetainPayload struct {
	SchemaVersion     int       `json:"schema_version" binding:"required" minimum:"1" maximum:"1" example:"1"`
	ExpectedRevision  string    `json:"expected_revision" binding:"required" minLength:"1" maxLength:"20" example:"1"`
	RequestedDeadline time.Time `json:"requested_deadline" binding:"required" format:"date-time"`
}

type backupRecoveryTargetRootListResponse struct {
	SchemaVersion int                                  `json:"schema_version"`
	Items         []settings.RecoveryTargetRootSummary `json:"items"`
}

type backupRecoveryCreatePlanResponse struct {
	SchemaVersion int                `json:"schema_version"`
	PlanID        string             `json:"plan_id"`
	State         recovery.PlanState `json:"state"`
	Replay        bool               `json:"replay"`
}

type backupRecoveryRetainResponse struct {
	SchemaVersion     int       `json:"schema_version"`
	ResultSetID       string    `json:"result_set_id"`
	JobID             string    `json:"job_id"`
	JobRevision       string    `json:"job_revision"`
	PlaintextDeadline time.Time `json:"plaintext_deadline"`
	HardDeadline      time.Time `json:"hard_deadline"`
}

type backupRecoveryTargetRootResponse struct {
	SchemaVersion int    `json:"schema_version"`
	NodeID        uint   `json:"node_id"`
	RootID        string `json:"root_id"`
	SafeLabel     string `json:"safe_label"`
}

type backupRecoveryDowngradeResponse struct {
	SchemaVersion       int                                           `json:"schema_version"`
	State               backupruntime.RecoveryDowngradeReadinessState `json:"state"`
	AdmissionGeneration string                                        `json:"admission_generation"`
	Blockers            backupruntime.RecoveryDowngradeBlockers       `json:"blockers"`
	Replay              bool                                          `json:"replay"`
}

type backupRecoveryAdminMutationAuthority struct {
	requesterID uint
	endpoint    string
	idempotency string
	session     middleware.SessionBinding
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

var errBackupRecoverySanitizedInternal = errors.New("backup recovery internal error")

var errBackupRecoveryBodyTooLarge = errors.New("backup recovery request body too large")

func NewBackupRecoveryHandler(
	service RecoveryAuthorizationHandlerService,
	db *gorm.DB,
	jwtManager *auth.JWTManager,
) *BackupRecoveryHandler {
	return &BackupRecoveryHandler{service: service, db: db, jwtManager: jwtManager}
}

func (handler *BackupRecoveryHandler) WithRecoveryAdministration(
	targetRoots RecoveryTargetRootHandlerService,
	downgrade RecoveryDowngradeHandlerService,
) *BackupRecoveryHandler {
	if handler != nil {
		handler.targetRoots = targetRoots
		handler.downgrade = downgrade
	}
	return handler
}

func (handler *BackupRecoveryHandler) WithRecoveryLifecycle(
	lifecycle RecoveryLifecycleHandlerService,
) *BackupRecoveryHandler {
	if handler != nil {
		handler.lifecycle = lifecycle
	}
	return handler
}

func (handler *BackupRecoveryHandler) WithRecoveryOperations(
	operations RecoveryOperationsHandlerService,
) *BackupRecoveryHandler {
	if handler != nil {
		handler.operations = operations
	}
	return handler
}

func (handler *BackupRecoveryHandler) Unavailable(c *gin.Context) {
	respondServiceUnavailable(c, "恢复服务暂不可用")
}

// CreatePlan persists or replays one owner-scoped frozen Recovery plan.
// @Summary      创建恢复计划
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        Idempotency-Key header string true "bounded requester/endpoint scoped key"
// @Param        body body backupRecoveryCreatePlanPayload true "safe recovery intent"
// @Success      201 {object} handlers.Response{data=backupRecoveryCreatePlanResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-plans [post]
func (handler *BackupRecoveryHandler) CreatePlan(c *gin.Context) {
	if handler == nil || handler.operations == nil {
		respondServiceUnavailable(c, "恢复服务暂不可用")
		return
	}
	if c == nil || c.Request == nil || c.Request.URL == nil || c.Request.Method != http.MethodPost ||
		c.FullPath() != "/api/v1/recovery-plans" || c.Request.URL.RawQuery != "" ||
		!backupRecoveryAuthorizationJSONContentType(c.Request) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	key, ok := backupRecoveryAuthorizationIdempotencyKey(c.Request)
	if !ok {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	var payload backupRecoveryCreatePlanPayload
	if err := decodeStrictBackupRecoveryJSON(c, &payload, maxBackupRecoveryCreatePlanBodyBytes); err != nil {
		if errors.Is(err, errBackupRecoveryBodyTooLarge) {
			respondPayloadTooLarge(c, "恢复请求超过允许范围")
		} else {
			respondBadRequest(c, "请求参数不合法")
		}
		return
	}
	if len(payload.EntryIDs) > 10_000 {
		respondPayloadTooLarge(c, "恢复请求超过允许范围")
		return
	}
	if payload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion ||
		!validBackupRecoveryCreatePlanPayload(payload) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.operations.CreatePlan(c.Request.Context(), recovery.CreatePlanIntentRequest{
		RequesterID: middleware.CurrentUserID(c), Endpoint: "/api/v1/recovery-plans", IdempotencyKey: key,
		RepositoryID: payload.RepositoryID, RecoveryPointID: payload.RecoveryPointID,
		CatalogGenerationID: payload.CatalogGenerationID, EntryIDs: append([]string(nil), payload.EntryIDs...),
		TargetMode: payload.TargetMode, TargetNodeID: payload.TargetNodeID,
		TargetRootID: payload.TargetRootID, ConflictPolicy: payload.ConflictPolicy,
	})
	if err != nil {
		respondBackupRecoveryLifecycleError(c, err)
		return
	}
	response, ok := safeBackupRecoveryCreatePlanResponse(result)
	if !ok {
		respondBackupRecoverySanitizedInternalError(c)
		return
	}
	respondCreated(c, response)
}

// Preflight submits only the owner, plan ID, and opaque revision. Recovery
// reconstructs every private observation/permit/input from its durable graph.
// @Summary      运行恢复预检
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复计划 opaque ID"
// @Param        body body backupRecoveryRevisionPayload true "opaque decimal revision"
// @Success      200 {object} handlers.Response{data=recovery.RecoveryPreflightView}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-plans/{id}/preflights [post]
func (handler *BackupRecoveryHandler) Preflight(c *gin.Context) {
	if handler == nil || handler.operations == nil {
		respondServiceUnavailable(c, "恢复服务暂不可用")
		return
	}
	id, revision, ok := prepareRecoveryRevisionMutation(c, "/api/v1/recovery-plans/:id/preflights")
	if !ok {
		return
	}
	result, err := handler.operations.Preflight(c.Request.Context(), recovery.RecoveryPreflightRequest{
		RequesterID: middleware.CurrentUserID(c), PlanID: id, ExpectedPlanRevision: revision,
	})
	if err != nil {
		respondBackupRecoveryLifecycleError(c, err)
		return
	}
	respondOK(c, result)
}

func validBackupRecoveryCreatePlanPayload(payload backupRecoveryCreatePlanPayload) bool {
	if backupasset.ValidateOpaqueID(payload.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(payload.RecoveryPointID) != nil ||
		backupasset.ValidateOpaqueID(payload.CatalogGenerationID) != nil || payload.TargetNodeID == 0 ||
		payload.TargetMode.Validate() != nil || payload.ConflictPolicy.Validate() != nil ||
		len(payload.EntryIDs) == 0 || len(payload.EntryIDs) > 10_000 ||
		payload.TargetRootID == "" || len(payload.TargetRootID) > 128 ||
		strings.TrimSpace(payload.TargetRootID) != payload.TargetRootID {
		return false
	}
	if payload.ConflictPolicy == recovery.ConflictExactMirror && payload.TargetMode != recovery.TargetModeInPlace {
		return false
	}
	seen := make(map[string]struct{}, len(payload.EntryIDs))
	for _, entryID := range payload.EntryIDs {
		if backupasset.ValidateAssetRef(backupasset.AssetRef{
			RecoveryPointID: payload.RecoveryPointID, EntryID: entryID,
		}) != nil {
			return false
		}
		if _, duplicate := seen[entryID]; duplicate {
			return false
		}
		seen[entryID] = struct{}{}
	}
	return true
}

func safeBackupRecoveryCreatePlanResponse(
	result recovery.CreatePlanResult,
) (backupRecoveryCreatePlanResponse, bool) {
	if !backupRecoveryAuthorizationOpaqueID(result.PlanID) || !result.State.Valid() {
		return backupRecoveryCreatePlanResponse{}, false
	}
	return backupRecoveryCreatePlanResponse{
		SchemaVersion: 1, PlanID: result.PlanID, State: result.State, Replay: result.Replay,
	}, true
}

func safeBackupRecoveryRetainResponse(
	result recovery.RetainedRecoveryResultSet,
) (backupRecoveryRetainResponse, bool) {
	if !backupRecoveryAuthorizationOpaqueID(result.ResultSetID) ||
		!backupRecoveryAuthorizationOpaqueID(result.JobID) || result.JobRevision == 0 ||
		result.PlaintextDeadline.IsZero() || result.HardDeadline.IsZero() ||
		result.PlaintextDeadline.After(result.HardDeadline) {
		return backupRecoveryRetainResponse{}, false
	}
	return backupRecoveryRetainResponse{
		SchemaVersion: 1, ResultSetID: result.ResultSetID, JobID: result.JobID,
		JobRevision:       strconv.FormatUint(result.JobRevision, 10),
		PlaintextDeadline: result.PlaintextDeadline.UTC(), HardDeadline: result.HardDeadline.UTC(),
	}, true
}

func safeBackupRecoveryTargetRootResponse(
	result settings.RecoveryTargetRootSummary,
) backupRecoveryTargetRootResponse {
	return backupRecoveryTargetRootResponse{
		SchemaVersion: 1, NodeID: result.NodeID, RootID: result.RootID, SafeLabel: result.SafeLabel,
	}
}

func safeBackupRecoveryDowngradeResponse(
	result backupruntime.RecoveryDowngradeReadiness,
) backupRecoveryDowngradeResponse {
	return backupRecoveryDowngradeResponse{
		SchemaVersion: 1, State: result.State, AdmissionGeneration: result.AdmissionGeneration,
		Blockers: result.Blockers, Replay: result.Replay,
	}
}

// CancelJob applies owner-scoped opaque revision CAS inside Recovery.
// @Summary      取消恢复作业
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复作业 opaque ID"
// @Param        body body backupRecoveryRevisionPayload true "opaque decimal revision"
// @Success      200 {object} handlers.Response{data=recovery.RecoveryJobView}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-jobs/{id}/cancel [post]
func (handler *BackupRecoveryHandler) CancelJob(c *gin.Context) {
	if handler == nil || handler.operations == nil {
		respondServiceUnavailable(c, "恢复服务暂不可用")
		return
	}
	id, revision, ok := prepareRecoveryRevisionMutation(c, "/api/v1/recovery-jobs/:id/cancel")
	if !ok {
		return
	}
	result, err := handler.operations.CancelJob(
		c.Request.Context(), middleware.CurrentUserID(c), id, revision,
	)
	if err != nil {
		respondBackupRecoveryLifecycleError(c, err)
		return
	}
	respondOK(c, result)
}

// RetainResults extends a ready result set under exact retain step-up.
// @Summary      延长恢复结果保留时间
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复作业 opaque ID"
// @Param        X-Xirang-Step-Up header string true "recovery.result_retain proof"
// @Param        body body backupRecoveryRetainPayload true "retain deadline and opaque revision"
// @Success      200 {object} handlers.Response{data=backupRecoveryRetainResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-jobs/{id}/results/retain [post]
func (handler *BackupRecoveryHandler) RetainResults(c *gin.Context) {
	if handler == nil || handler.operations == nil {
		respondServiceUnavailable(c, "恢复服务暂不可用")
		return
	}
	if c == nil || c.Request == nil || c.Request.URL == nil || c.Request.Method != http.MethodPost ||
		c.FullPath() != "/api/v1/recovery-jobs/:id/results/retain" || c.Request.URL.RawQuery != "" ||
		!backupRecoveryAuthorizationJSONContentType(c.Request) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	jobID, jobIDOK := backupAssetOpaqueParam(c, "id")
	var payload backupRecoveryRetainPayload
	if !jobIDOK || decodeStrictBackupRecoveryAuthorizationJSON(c, &payload) != nil ||
		payload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion || payload.RequestedDeadline.IsZero() {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	revision, ok := backupRecoveryAuthorizationRevision(payload.ExpectedRevision)
	if !ok {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	actor, proof, ok := handler.prepareRecoveryResultRetainProof(c)
	if !ok {
		return
	}
	result, err := handler.operations.RetainRecoveryResults(c.Request.Context(), recovery.RetainRecoveryResultsRequest{
		JobID: jobID, ExpectedJobRevision: revision, RequestedDeadline: payload.RequestedDeadline.UTC(),
		Actor: actor, Permissions: backupasset.PermissionSet{backupasset.PermissionBackupAssetsRecover: true}, Proof: proof,
	})
	if err != nil {
		respondBackupRecoveryLifecycleError(c, err)
		return
	}
	response, ok := safeBackupRecoveryRetainResponse(result)
	if !ok {
		respondBackupRecoverySanitizedInternalError(c)
		return
	}
	respondOK(c, response)
}

// CleanupResults schedules owner-scoped cleanup under an opaque revision CAS.
// @Summary      清理恢复结果
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复作业 opaque ID"
// @Param        body body backupRecoveryRevisionPayload true "opaque decimal revision"
// @Success      200 {object} handlers.Response{data=recovery.RecoveryResultCleanupView}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-jobs/{id}/results/cleanup [post]
func (handler *BackupRecoveryHandler) CleanupResults(c *gin.Context) {
	if handler == nil || handler.operations == nil {
		respondServiceUnavailable(c, "恢复服务暂不可用")
		return
	}
	id, revision, ok := prepareRecoveryRevisionMutation(c, "/api/v1/recovery-jobs/:id/results/cleanup")
	if !ok {
		return
	}
	result, err := handler.operations.RequestResultCleanup(c.Request.Context(), recovery.RecoveryResultCleanupRequest{
		RequesterID: middleware.CurrentUserID(c), JobID: id, ExpectedJobRevision: revision,
	})
	if err != nil {
		respondBackupRecoveryLifecycleError(c, err)
		return
	}
	respondOK(c, result)
}

func (handler *BackupRecoveryHandler) prepareRecoveryResultRetainProof(
	c *gin.Context,
) (content.DeliveryActor, *content.StepUpProof, bool) {
	actor := content.DeliveryActor{
		UserID: middleware.CurrentUserID(c), Username: c.GetString(middleware.CtxUsername), Role: middleware.CurrentRole(c),
	}
	session, exists := middleware.CurrentSessionBinding(c)
	if !exists || actor.UserID == 0 || actor.Role != "admin" || session.UserID != actor.UserID ||
		session.Role != actor.Role || session.TokenVersion == 0 || !session.ExpiresAt.After(time.Now().UTC()) {
		respondForbidden(c, "权限不足")
		return content.DeliveryActor{}, nil, false
	}
	rawProof, proofOK := backupRecoveryAuthorizationStepUpProof(c.Request)
	if !proofOK {
		respondStepUpRequired(c)
		return content.DeliveryActor{}, nil, false
	}
	claims, err := validateStepUpProof(
		handler.db, handler.jwtManager, rawProof, actor.UserID, actor.Role, auth.StepUpActionRecoveryResultRetain,
	)
	if err != nil || claims == nil || claims.ExpiresAt == nil || claims.UserID != session.UserID ||
		claims.Role != session.Role || claims.TokenVersion != session.TokenVersion ||
		!claims.ExpiresAt.After(time.Now().UTC()) {
		if errors.Is(err, ErrStepUpVerifierUnavailable) {
			respondServiceUnavailable(c, "二次验证服务暂不可用")
		} else {
			respondStepUpRequired(c)
		}
		return content.DeliveryActor{}, nil, false
	}
	return actor, &content.StepUpProof{
		Action: claims.StepUpAction, ID: claims.ID, ExpiresAt: claims.ExpiresAt.UTC(),
	}, true
}

// GetPlan returns the owner-scoped safe Recovery plan projection.
// @Summary      获取恢复计划
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Produce      json
// @Param        id path string true "恢复计划 opaque ID"
// @Success      200 {object} handlers.Response{data=recovery.RecoveryPlanView}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-plans/{id} [get]
func (handler *BackupRecoveryHandler) GetPlan(c *gin.Context) {
	if handler == nil || handler.lifecycle == nil {
		respondServiceUnavailable(c, "恢复服务暂不可用")
		return
	}
	id, ok := handler.prepareRecoveryRead(c, "/api/v1/recovery-plans/:id")
	if !ok {
		return
	}
	result, err := handler.lifecycle.GetPlan(c.Request.Context(), middleware.CurrentUserID(c), id)
	if err != nil {
		respondBackupRecoveryLifecycleError(c, err)
		return
	}
	respondOK(c, result)
}

// CancelPlan applies owner-scoped opaque revision CAS inside Recovery.
// @Summary      取消恢复计划
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复计划 opaque ID"
// @Param        body body backupRecoveryRevisionPayload true "opaque decimal revision"
// @Success      200 {object} handlers.Response{data=recovery.RecoveryPlanView}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-plans/{id}/cancel [post]
func (handler *BackupRecoveryHandler) CancelPlan(c *gin.Context) {
	if handler == nil || handler.lifecycle == nil {
		respondServiceUnavailable(c, "恢复服务暂不可用")
		return
	}
	id, revision, ok := prepareRecoveryRevisionMutation(c, "/api/v1/recovery-plans/:id/cancel")
	if !ok {
		return
	}
	result, err := handler.lifecycle.CancelPlan(c.Request.Context(), recovery.RecoveryPlanMutationRequest{
		RequesterID: middleware.CurrentUserID(c), PlanID: id, ExpectedRevision: revision,
	})
	if err != nil {
		respondBackupRecoveryLifecycleError(c, err)
		return
	}
	respondOK(c, result)
}

// GetJob returns the owner-scoped safe Recovery job projection.
// @Summary      获取恢复作业
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Produce      json
// @Param        id path string true "恢复作业 opaque ID"
// @Success      200 {object} handlers.Response{data=recovery.RecoveryJobView}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-jobs/{id} [get]
func (handler *BackupRecoveryHandler) GetJob(c *gin.Context) {
	if handler == nil || handler.lifecycle == nil {
		respondServiceUnavailable(c, "恢复服务暂不可用")
		return
	}
	id, ok := handler.prepareRecoveryRead(c, "/api/v1/recovery-jobs/:id")
	if !ok {
		return
	}
	result, err := handler.lifecycle.GetJob(c.Request.Context(), middleware.CurrentUserID(c), id)
	if err != nil {
		respondBackupRecoveryLifecycleError(c, err)
		return
	}
	respondOK(c, result)
}

func (handler *BackupRecoveryHandler) prepareRecoveryRead(c *gin.Context, template string) (string, bool) {
	if c == nil || c.Request == nil || c.Request.URL == nil || c.Request.Method != http.MethodGet ||
		c.FullPath() != template || c.Request.URL.RawQuery != "" {
		if c != nil {
			respondBadRequest(c, "请求参数不合法")
		}
		return "", false
	}
	id, ok := backupAssetOpaqueParam(c, "id")
	if !ok {
		respondBadRequest(c, "请求参数不合法")
		return "", false
	}
	return id, true
}

func prepareRecoveryRevisionMutation(c *gin.Context, template string) (string, uint64, bool) {
	if c == nil || c.Request == nil || c.Request.URL == nil || c.Request.Method != http.MethodPost ||
		c.FullPath() != template || c.Request.URL.RawQuery != "" ||
		!backupRecoveryAuthorizationJSONContentType(c.Request) {
		if c != nil {
			respondBadRequest(c, "请求参数不合法")
		}
		return "", 0, false
	}
	id, idOK := backupAssetOpaqueParam(c, "id")
	var payload backupRecoveryRevisionPayload
	if !idOK || decodeStrictBackupRecoveryAuthorizationJSON(c, &payload) != nil ||
		payload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion {
		respondBadRequest(c, "请求参数不合法")
		return "", 0, false
	}
	revision, ok := backupRecoveryAuthorizationRevision(payload.ExpectedRevision)
	if !ok {
		respondBadRequest(c, "请求参数不合法")
		return "", 0, false
	}
	return id, revision, true
}

// RegisterTargetRoot installs one Recovery-owned target root through the runtime transition owner.
// @Summary      注册恢复目标根
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        Idempotency-Key header string true "requester and endpoint scoped key"
// @Param        X-Xirang-Step-Up header string true "asset.recover proof"
// @Param        body body backupRecoveryTargetRootRegisterPayload true "target-root definition"
// @Success      200 {object} handlers.Response{data=backupRecoveryTargetRootResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /settings/backup-assets/recovery/target-roots [post]
func (handler *BackupRecoveryHandler) RegisterTargetRoot(c *gin.Context) {
	handler.mutateTargetRoot(c, http.MethodPost, "/api/v1/settings/backup-assets/recovery/target-roots", false)
}

// RotateTargetRoot replaces one exact target-root authority through the runtime transition owner.
// @Summary      轮换恢复目标根
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        nodeId path integer true "节点 ID"
// @Param        rootId path string true "目标根 ID"
// @Param        Idempotency-Key header string true "requester and endpoint scoped key"
// @Param        X-Xirang-Step-Up header string true "asset.recover proof"
// @Param        body body backupRecoveryTargetRootRotatePayload true "target-root definition"
// @Success      200 {object} handlers.Response{data=backupRecoveryTargetRootResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /settings/backup-assets/recovery/target-roots/{nodeId}/{rootId} [put]
func (handler *BackupRecoveryHandler) RotateTargetRoot(c *gin.Context) {
	handler.mutateTargetRoot(c, http.MethodPut, "/api/v1/settings/backup-assets/recovery/target-roots/:nodeId/:rootId", true)
}

func (handler *BackupRecoveryHandler) mutateTargetRoot(
	c *gin.Context,
	method string,
	template string,
	pathBound bool,
) {
	if handler == nil || handler.targetRoots == nil {
		respondServiceUnavailable(c, "恢复目标根服务暂不可用")
		return
	}
	authority, ok := handler.prepareRecoveryAdminMutation(c, method, template)
	if !ok {
		return
	}
	payload, decodeErr := decodeBackupRecoveryTargetRootPayload(c, pathBound)
	if decodeErr != nil ||
		payload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if pathBound {
		nodeID, nodeOK := backupRecoveryCanonicalUint(c.Param("nodeId"))
		rootID, rootOK := backupRecoveryTargetRootID(c.Param("rootId"))
		if !nodeOK || !rootOK || (payload.NodeID != 0 && payload.NodeID != nodeID) ||
			(payload.RootID != "" && payload.RootID != rootID) {
			respondBadRequest(c, "请求参数不合法")
			return
		}
		payload.NodeID = nodeID
		payload.RootID = rootID
	}
	request := recovery.TargetRootRegistrationRequest{
		Mutation: recovery.TargetRootMutationRegister, RequesterID: authority.requesterID,
		Endpoint: authority.endpoint, IdempotencyKey: authority.idempotency,
		SessionJTI: authority.session.JTI, SessionRole: authority.session.Role,
		SessionTokenVersion: authority.session.TokenVersion, SessionExpiresAt: authority.session.ExpiresAt,
		NodeID: payload.NodeID, RootID: payload.RootID, SafeLabel: payload.SafeLabel, Locator: payload.Locator,
		Policy: settings.RecoveryTargetRootPolicy{
			ReserveBytes: payload.ReserveBytes, ReserveInodes: payload.ReserveInodes,
			OverlapPolicyBinding: payload.OverlapPolicyBinding,
		},
	}
	if pathBound {
		request.Mutation = recovery.TargetRootMutationRotate
	}
	if replay, found, err := handler.targetRoots.ReplayRegistration(c.Request.Context(), request); err != nil {
		respondBackupRecoveryAdministrationError(c, err)
		return
	} else if found {
		if replay.NodeID != request.NodeID || replay.RootID != request.RootID ||
			strings.TrimSpace(replay.SafeLabel) == "" || len(replay.SafeLabel) > 128 {
			respondBackupRecoverySanitizedInternalError(c)
			return
		}
		respondOK(c, safeBackupRecoveryTargetRootResponse(replay))
		return
	}
	if !handler.validateRecoveryAdminMutationStepUp(c, authority) {
		return
	}
	result, err := handler.targetRoots.Register(c.Request.Context(), request)
	if err != nil {
		respondBackupRecoveryAdministrationError(c, err)
		return
	}
	if result.NodeID != request.NodeID || result.RootID != request.RootID ||
		strings.TrimSpace(result.SafeLabel) == "" || len(result.SafeLabel) > 128 {
		respondBackupRecoverySanitizedInternalError(c)
		return
	}
	respondOK(c, safeBackupRecoveryTargetRootResponse(result))
}

func decodeBackupRecoveryTargetRootPayload(
	c *gin.Context,
	pathBound bool,
) (backupRecoveryTargetRootPayload, error) {
	if pathBound {
		var payload backupRecoveryTargetRootRotatePayload
		if err := decodeStrictBackupRecoveryAuthorizationJSON(c, &payload); err != nil {
			return backupRecoveryTargetRootPayload{}, err
		}
		return backupRecoveryTargetRootPayload(payload), nil
	}
	var payload backupRecoveryTargetRootRegisterPayload
	if err := decodeStrictBackupRecoveryAuthorizationJSON(c, &payload); err != nil {
		return backupRecoveryTargetRootPayload{}, err
	}
	return backupRecoveryTargetRootPayload(payload), nil
}

// DeleteTargetRoot removes one exact target-root authority through the runtime transition owner.
// @Summary      删除恢复目标根
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        nodeId path integer true "节点 ID"
// @Param        rootId path string true "目标根 ID"
// @Param        Idempotency-Key header string true "requester and endpoint scoped key"
// @Param        X-Xirang-Step-Up header string true "asset.recover proof"
// @Param        body body backupRecoverySchemaPayload true "schema version"
// @Success      200 {object} handlers.Response{data=backupRecoveryTargetRootResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /settings/backup-assets/recovery/target-roots/{nodeId}/{rootId} [delete]
func (handler *BackupRecoveryHandler) DeleteTargetRoot(c *gin.Context) {
	if handler == nil || handler.targetRoots == nil {
		respondServiceUnavailable(c, "恢复目标根服务暂不可用")
		return
	}
	const template = "/api/v1/settings/backup-assets/recovery/target-roots/:nodeId/:rootId"
	authority, ok := handler.prepareRecoveryAdminMutation(c, http.MethodDelete, template)
	if !ok {
		return
	}
	var payload backupRecoverySchemaPayload
	if decodeStrictBackupRecoveryAuthorizationJSON(c, &payload) != nil ||
		payload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	nodeID, nodeOK := backupRecoveryCanonicalUint(c.Param("nodeId"))
	rootID, rootOK := backupRecoveryTargetRootID(c.Param("rootId"))
	if !nodeOK || !rootOK {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request := recovery.TargetRootDeletionRequest{
		Mutation: recovery.TargetRootMutationDelete, RequesterID: authority.requesterID,
		Endpoint: authority.endpoint, IdempotencyKey: authority.idempotency,
		SessionJTI: authority.session.JTI, SessionRole: authority.session.Role,
		SessionTokenVersion: authority.session.TokenVersion, SessionExpiresAt: authority.session.ExpiresAt,
		NodeID: nodeID, RootID: rootID,
	}
	if replay, found, err := handler.targetRoots.ReplayDeletion(c.Request.Context(), request); err != nil {
		respondBackupRecoveryAdministrationError(c, err)
		return
	} else if found {
		if replay.NodeID != nodeID || replay.RootID != rootID || strings.TrimSpace(replay.SafeLabel) == "" {
			respondBackupRecoverySanitizedInternalError(c)
			return
		}
		respondOK(c, safeBackupRecoveryTargetRootResponse(replay))
		return
	}
	if !handler.validateRecoveryAdminMutationStepUp(c, authority) {
		return
	}
	result, err := handler.targetRoots.DeleteAuthorized(c.Request.Context(), request)
	if err != nil {
		respondBackupRecoveryAdministrationError(c, err)
		return
	}
	if result.NodeID != nodeID || result.RootID != rootID || strings.TrimSpace(result.SafeLabel) == "" {
		respondBackupRecoverySanitizedInternalError(c)
		return
	}
	respondOK(c, safeBackupRecoveryTargetRootResponse(result))
}

// ListTargetRoots returns node-scoped safe root summaries only.
// @Summary      列出恢复目标根
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Produce      json
// @Param        node_id query integer true "节点 ID"
// @Success      200 {object} handlers.Response{data=backupRecoveryTargetRootListResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /settings/backup-assets/recovery/target-roots [get]
func (handler *BackupRecoveryHandler) ListTargetRoots(c *gin.Context) {
	if handler == nil || handler.targetRoots == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		respondServiceUnavailable(c, "恢复目标根服务暂不可用")
		return
	}
	query := c.Request.URL.Query()
	values, found := query["node_id"]
	if c.Request.Method != http.MethodGet || c.FullPath() != "/api/v1/settings/backup-assets/recovery/target-roots" ||
		len(query) != 1 || !found || len(values) != 1 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	nodeID, ok := backupRecoveryCanonicalUint(values[0])
	if !ok {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	items, err := handler.targetRoots.List(c.Request.Context(), middleware.CurrentUserID(c), nodeID)
	if err != nil {
		respondBackupRecoveryAdministrationError(c, err)
		return
	}
	for _, item := range items {
		_, rootOK := backupRecoveryTargetRootID(item.RootID)
		if item.NodeID != nodeID || !rootOK ||
			strings.TrimSpace(item.SafeLabel) == "" || len(item.SafeLabel) > 128 {
			respondInternalError(c, errors.New("invalid recovery target root list"))
			return
		}
	}
	respondOK(c, backupRecoveryTargetRootListResponse{SchemaVersion: 1, Items: items})
}

// DowngradeReadiness installs the sticky fence and returns a safe readiness product.
// @Summary      检查恢复降级就绪状态
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        Idempotency-Key header string true "requester and endpoint scoped key"
// @Param        X-Xirang-Step-Up header string true "asset.recover proof"
// @Param        body body backupRecoveryDowngradePayload true "bounded operator reason"
// @Success      200 {object} handlers.Response{data=backupRecoveryDowngradeResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /settings/backup-assets/recovery/downgrade-readiness [post]
func (handler *BackupRecoveryHandler) DowngradeReadiness(c *gin.Context) {
	if handler == nil || handler.downgrade == nil {
		respondServiceUnavailable(c, "恢复降级检查暂不可用")
		return
	}
	const template = "/api/v1/settings/backup-assets/recovery/downgrade-readiness"
	authority, ok := handler.prepareRecoveryAdminMutation(c, http.MethodPost, template)
	if !ok {
		return
	}
	var payload backupRecoveryDowngradePayload
	if decodeStrictBackupRecoveryAuthorizationJSON(c, &payload) != nil ||
		payload.SchemaVersion != backupRecoveryAuthorizationSchemaVersion ||
		!backupRecoveryAuthorizationReason(payload.Reason) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	request := backupruntime.RecoveryDowngradeReadinessRequest{
		RequesterID: authority.requesterID, Endpoint: authority.endpoint, IdempotencyKey: authority.idempotency,
		SessionJTI: authority.session.JTI, SessionRole: authority.session.Role,
		SessionTokenVersion: authority.session.TokenVersion, SessionExpiresAt: authority.session.ExpiresAt,
		Reason: payload.Reason,
	}
	if replay, found, err := handler.downgrade.ReplayRecoveryDowngradeReadiness(c.Request.Context(), request); err != nil {
		respondBackupRecoveryAdministrationError(c, err)
		return
	} else if found {
		if !validBackupRecoveryDowngradeReadiness(replay) {
			respondBackupRecoverySanitizedInternalError(c)
			return
		}
		respondOK(c, safeBackupRecoveryDowngradeResponse(replay))
		return
	}
	if !handler.validateRecoveryAdminMutationStepUp(c, authority) {
		return
	}
	result, err := handler.downgrade.RequestRecoveryDowngradeReadiness(c.Request.Context(), request)
	if err != nil {
		respondBackupRecoveryAdministrationError(c, err)
		return
	}
	if !validBackupRecoveryDowngradeReadiness(result) {
		respondBackupRecoverySanitizedInternalError(c)
		return
	}
	respondOK(c, safeBackupRecoveryDowngradeResponse(result))
}

func validBackupRecoveryDowngradeReadiness(result backupruntime.RecoveryDowngradeReadiness) bool {
	if !strings.HasPrefix(result.AdmissionGeneration, "recovery-downgrade-") ||
		!backupRecoveryAuthorizationOpaqueID(strings.TrimPrefix(result.AdmissionGeneration, "recovery-downgrade-")) {
		return false
	}
	switch result.State {
	case backupruntime.RecoveryDowngradePristineAllowed,
		backupruntime.RecoveryDowngradeBlocked,
		backupruntime.RecoveryDowngradeForwardFixOnly:
		return true
	default:
		return false
	}
}

func (handler *BackupRecoveryHandler) prepareRecoveryAdminMutation(
	c *gin.Context,
	method string,
	template string,
) (backupRecoveryAdminMutationAuthority, bool) {
	if c == nil || c.Request == nil || c.Request.URL == nil || c.Request.Method != method ||
		c.FullPath() != template || c.Request.URL.RawQuery != "" ||
		!backupRecoveryAuthorizationJSONContentType(c.Request) {
		if c != nil {
			respondBadRequest(c, "请求参数不合法")
		}
		return backupRecoveryAdminMutationAuthority{}, false
	}
	idempotencyKey, ok := backupRecoveryAuthorizationIdempotencyKey(c.Request)
	if !ok {
		respondBadRequest(c, "请求参数不合法")
		return backupRecoveryAdminMutationAuthority{}, false
	}
	requesterID := middleware.CurrentUserID(c)
	role := middleware.CurrentRole(c)
	session, ok := middleware.CurrentSessionBinding(c)
	if !ok || !backupRecoveryAuthorizationSession(session, requesterID, role, time.Now().UTC()) {
		respondForbidden(c, "权限不足")
		return backupRecoveryAdminMutationAuthority{}, false
	}
	return backupRecoveryAdminMutationAuthority{
		requesterID: requesterID, endpoint: template, idempotency: idempotencyKey, session: session,
	}, true
}

func (handler *BackupRecoveryHandler) validateRecoveryAdminMutationStepUp(
	c *gin.Context,
	authority backupRecoveryAdminMutationAuthority,
) bool {
	proof, ok := backupRecoveryAuthorizationStepUpProof(c.Request)
	if !ok {
		respondStepUpRequired(c)
		return false
	}
	claims, err := validateStepUpProof(
		handler.db, handler.jwtManager, proof, authority.requesterID, authority.session.Role, auth.StepUpActionAssetRecover,
	)
	if err != nil {
		if errors.Is(err, ErrStepUpVerifierUnavailable) {
			respondServiceUnavailable(c, "二次验证服务暂不可用")
		} else {
			respondStepUpRequired(c)
		}
		return false
	}
	if claims == nil || claims.ExpiresAt == nil || claims.ID == "" || claims.UserID != authority.session.UserID ||
		claims.Role != authority.session.Role || claims.TokenVersion != authority.session.TokenVersion {
		respondForbidden(c, "权限不足")
		return false
	}
	return true
}

// SecurityOverride records one purpose-exact security override receipt.
// @Summary      覆盖恢复安全阻断
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复计划 opaque ID"
// @Param        Idempotency-Key header string true "requester and endpoint scoped key"
// @Param        X-Xirang-Step-Up header string true "asset.recover proof"
// @Param        body body backupRecoverySecurityOverridePayload true "override intent"
// @Success      200 {object} handlers.Response{data=backupRecoveryAuthorizationResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-plans/{id}/security-overrides [post]
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

// AuthorizeWrite mints one durable hash-only write grant.
// @Summary      授权恢复写入
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复计划 opaque ID"
// @Param        Idempotency-Key header string true "requester and endpoint scoped key"
// @Param        X-Xirang-Step-Up header string true "asset.recover proof"
// @Param        body body backupRecoveryWriteAuthorizationPayload true "write authorization intent"
// @Success      200 {object} handlers.Response{data=backupRecoveryAuthorizationResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-plans/{id}/write-authorizations [post]
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

// AuthorizeExactMirrorDelete mints a checkpoint-bound exact-mirror delete grant.
// @Summary      授权精确镜像删除
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复作业 opaque ID"
// @Param        Idempotency-Key header string true "requester and endpoint scoped key"
// @Param        X-Xirang-Step-Up header string true "asset.recover proof"
// @Param        body body backupRecoveryDeleteAuthorizationPayload true "delete authorization intent"
// @Success      200 {object} handlers.Response{data=backupRecoveryAuthorizationResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-jobs/{id}/exact-mirror-delete-authorizations [post]
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

// Execute consumes the exact write grant and returns the durable job receipt.
// @Summary      执行恢复计划
// @Tags         backup-assets-recovery
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id path string true "恢复计划 opaque ID"
// @Param        Idempotency-Key header string true "requester and endpoint scoped key"
// @Param        X-Xirang-Step-Up header string true "asset.recover proof"
// @Param        body body backupRecoveryExecutePayload true "execute intent"
// @Success      202 {object} handlers.Response{data=backupRecoveryAuthorizationResponse}
// @Failure      400 {object} handlers.Response
// @Failure      401 {object} handlers.Response
// @Failure      403 {object} handlers.Response
// @Failure      404 {object} handlers.Response
// @Failure      409 {object} handlers.Response
// @Failure      413 {object} handlers.Response
// @Failure      429 {object} handlers.Response
// @Failure      500 {object} handlers.Response
// @Failure      503 {object} handlers.Response
// @Router       /recovery-plans/{id}/execute [post]
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
		respondBackupRecoverySanitizedInternalError(c)
	}
}

func decodeStrictBackupRecoveryAuthorizationJSON(c *gin.Context, target any) error {
	return decodeStrictBackupRecoveryJSON(c, target, maxBackupRecoveryAuthorizationBodyBytes)
}

func decodeStrictBackupRecoveryJSON(c *gin.Context, target any, maxBodyBytes int64) error {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return io.ErrUnexpectedEOF
	}
	payload, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes+1))
	if err != nil || len(payload) == 0 {
		return io.ErrUnexpectedEOF
	}
	if int64(len(payload)) > maxBodyBytes {
		return errBackupRecoveryBodyTooLarge
	}
	if rejectDuplicateBackupContentJSON(payload) != nil {
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
	if len(parameters) == 1 {
		charset, ok := parameters["charset"]
		if !ok || !strings.EqualFold(charset, "utf-8") {
			return false
		}
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

func backupRecoveryCanonicalUint(value string) (uint, bool) {
	if value == "" || strings.TrimSpace(value) != value {
		return 0, false
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value || uint64(uint(parsed)) != parsed {
		return 0, false
	}
	return uint(parsed), true
}

func backupRecoveryTargetRootID(value string) (string, bool) {
	if len(value) == 0 || len(value) > 32 || strings.TrimSpace(value) != value {
		return "", false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return "", false
		}
	}
	return value, true
}

func respondBackupRecoveryAdministrationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, settings.ErrRecoveryTargetRootInvalid):
		respondBadRequest(c, "请求参数不合法")
	case errors.Is(err, settings.ErrRecoveryTargetRootNotFound), errors.Is(err, backupasset.ErrNotFound):
		respondNotFound(c, "恢复对象不存在")
	case errors.Is(err, backupasset.ErrForbidden):
		respondForbidden(c, "权限不足")
	case errors.Is(err, backupasset.ErrConflict), errors.Is(err, recovery.ErrRecoveryTargetChanged):
		respondConflict(c, "恢复状态冲突")
	case errors.Is(err, recovery.ErrTargetRootIdempotencyConflict),
		errors.Is(err, recovery.ErrTargetRootMutationConflict),
		errors.Is(err, backupruntime.ErrRecoveryDowngradeIdempotencyConflict):
		respondConflict(c, "恢复状态冲突")
	case errors.Is(err, recovery.ErrRecoveryTargetUnavailable),
		errors.Is(err, settings.ErrRecoveryTargetRootUnavailable),
		errors.Is(err, backupasset.ErrInvalidState),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		respondServiceUnavailable(c, "恢复服务暂不可用")
	default:
		respondBackupRecoverySanitizedInternalError(c)
	}
}

func respondBackupRecoverySanitizedInternalError(c *gin.Context) {
	respondInternalError(c, errBackupRecoverySanitizedInternal)
}

func respondBackupRecoveryLifecycleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, recovery.ErrInvalidRecoveryPlan),
		errors.Is(err, recovery.ErrInvalidExactSelection),
		errors.Is(err, recovery.ErrInvalidTargetPreflight),
		errors.Is(err, recovery.ErrInvalidResultLifecycle):
		respondBadRequest(c, "请求参数不合法")
	case errors.Is(err, recovery.ErrRecoveryAPIObjectNotFound), errors.Is(err, backupasset.ErrNotFound):
		respondNotFound(c, "恢复对象不存在")
	case errors.Is(err, backupasset.ErrForbidden), errors.Is(err, recovery.ErrRecoveryResultRetainDenied):
		respondForbidden(c, "权限不足")
	case errors.Is(err, recovery.ErrRecoveryAPIConflict), errors.Is(err, recovery.ErrRecoveryPreflightConflict),
		errors.Is(err, recovery.ErrRecoveryResultRetainConflict), errors.Is(err, backupasset.ErrConflict),
		errors.Is(err, recovery.ErrPlanIdempotencyConflict), errors.Is(err, recovery.ErrRecoverySourceChanged),
		errors.Is(err, recovery.ErrRecoveryTargetChanged):
		respondConflict(c, "恢复状态冲突")
	case errors.Is(err, recovery.ErrExactSelectionLimit), errors.Is(err, recovery.ErrRecoveryOperationLimit),
		errors.Is(err, recovery.ErrRecoveryImpactLimit):
		respondPayloadTooLarge(c, "恢复请求超过允许范围")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, recovery.ErrRecoveryAPIUnavailable), errors.Is(err, recovery.ErrRecoveryPlanUnavailable),
		errors.Is(err, recovery.ErrRecoveryResultUnavailable), errors.Is(err, recovery.ErrTargetPreflightUnavailable),
		errors.Is(err, recovery.ErrRecoverySourceUnavailable), errors.Is(err, recovery.ErrRecoveryTargetUnavailable),
		errors.Is(err, backupasset.ErrCapabilityUnavailable):
		respondServiceUnavailable(c, "恢复服务暂不可用")
	default:
		respondBackupRecoverySanitizedInternalError(c)
	}
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
