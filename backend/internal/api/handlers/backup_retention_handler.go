package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	backuprepository "xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/backupasset/retention"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

type BackupRetentionPolicyListRequest struct {
	Actor  backupasset.AuditActor
	Limit  int
	Cursor string
}

type BackupRetentionPolicyCreateRequest struct {
	Actor     backupasset.AuditActor
	ScopeKind backupasset.RetentionPolicyScopeKind
	ScopeID   string
	Rules     retention.PolicyRules
}

type BackupRetentionPolicyUpdateRequest struct {
	Actor            backupasset.AuditActor
	PolicyID         string
	ExpectedRevision int64
	Rules            retention.PolicyRules
}

type BackupRetentionPolicyDeleteRequest struct {
	Actor            backupasset.AuditActor
	PolicyID         string
	ExpectedRevision int64
}

type BackupRetentionImpactRequest struct {
	Actor            backupasset.AuditActor
	PolicyID         string
	ExpectedRevision int64
	Limit            int
	InspectedLimit   int
	Cursor           string
	EvaluatedAt      time.Time
}

type BackupRetentionPolicyView struct {
	ID         string                               `json:"id"`
	ScopeKind  backupasset.RetentionPolicyScopeKind `json:"scope_kind"`
	ScopeID    string                               `json:"scope_id"`
	Revision   int64                                `json:"revision"`
	Rules      retention.PolicyRules                `json:"rules"`
	RuleDigest string                               `json:"rule_digest"`
	Status     backupasset.RetentionPolicyStatus    `json:"status"`
	CreatedBy  uint                                 `json:"created_by"`
	UpdatedBy  uint                                 `json:"updated_by"`
	CreatedAt  time.Time                            `json:"created_at"`
	UpdatedAt  time.Time                            `json:"updated_at"`
}

type BackupRetentionPolicyPage struct {
	Items      []BackupRetentionPolicyView `json:"items"`
	NextCursor string                      `json:"next_cursor,omitempty"`
}

type BackupRetentionImpactPoint struct {
	RecoveryPointID    string `json:"recovery_point_id"`
	PointRevision      int64  `json:"point_revision"`
	CapabilityRevision int    `json:"capability_revision"`
}

type BackupRetentionImpactView struct {
	PolicyID       string                       `json:"policy_id"`
	PolicyRevision int64                        `json:"policy_revision"`
	ImpactRevision int64                        `json:"impact_revision"`
	EvaluatedAt    time.Time                    `json:"evaluated_at"`
	SelectedCount  int                          `json:"selected_count"`
	HoldCount      int64                        `json:"hold_count"`
	LeaseCount     int64                        `json:"lease_count"`
	WORMCount      int64                        `json:"worm_count"`
	Points         []BackupRetentionImpactPoint `json:"points"`
	NextCursor     string                       `json:"next_cursor,omitempty"`
}

type BackupRetentionHoldCreateRequest struct {
	Actor           backupasset.AuditActor
	RecoveryPointID string
	HoldType        backupasset.RecoveryPointHoldType
	Reason          string
	ExpiresAt       *time.Time
}

type BackupRetentionHoldListRequest struct {
	Actor           backupasset.AuditActor
	RecoveryPointID string
}

type BackupRetentionHoldPage struct {
	Items []retention.HoldRecord `json:"items"`
}

type BackupRetentionHoldReleaseRequest struct {
	Actor           backupasset.AuditActor
	RecoveryPointID string
	HoldID          string
	Reason          string
}

type BackupRetentionPurgePlanRequest struct {
	Actor                  backupasset.AuditActor
	RepositoryID           string
	ExpectedImpactRevision int64
	Items                  []BackupRetentionPurgePlanItemView
}

type BackupRetentionPurgeExecuteRequest struct {
	Actor                  backupasset.AuditActor
	RepositoryID           string
	PlanID                 string
	ExpectedRevision       int64
	ExpectedImpactRevision int64
	Reason                 string
	ProofDigest            string
}

type BackupRetentionPurgePlanItemView struct {
	RecoveryPointID    string `json:"recovery_point_id"`
	PointRevision      int64  `json:"point_revision"`
	CapabilityRevision int    `json:"capability_revision"`
}

type BackupRetentionPurgePlanView struct {
	ID             string                             `json:"id"`
	RepositoryID   string                             `json:"repository_id"`
	Revision       int64                              `json:"revision"`
	ImpactRevision int64                              `json:"impact_revision"`
	ExpiresAt      time.Time                          `json:"expires_at"`
	HoldCount      int64                              `json:"hold_count"`
	LeaseCount     int64                              `json:"lease_count"`
	WORMCount      int64                              `json:"worm_count"`
	Status         backupasset.PurgePlanStatus        `json:"status"`
	ItemCount      int                                `json:"item_count"`
	Items          []BackupRetentionPurgePlanItemView `json:"items"`
}

type BackupRetentionPurgeResult struct {
	PlanID  string `json:"plan_id"`
	Claimed int    `json:"claimed"`
	Blocked int    `json:"blocked"`
}

type BackupRetentionPolicyService interface {
	List(context.Context, BackupRetentionPolicyListRequest) (BackupRetentionPolicyPage, error)
	Create(context.Context, BackupRetentionPolicyCreateRequest) (BackupRetentionPolicyView, error)
	Update(context.Context, BackupRetentionPolicyUpdateRequest) (BackupRetentionPolicyView, error)
	Delete(context.Context, BackupRetentionPolicyDeleteRequest) (BackupRetentionPolicyView, error)
	PreviewImpact(context.Context, BackupRetentionImpactRequest) (BackupRetentionImpactView, error)
}

type BackupRetentionHoldService interface {
	List(context.Context, BackupRetentionHoldListRequest) (BackupRetentionHoldPage, error)
	Create(context.Context, BackupRetentionHoldCreateRequest) (retention.HoldRecord, error)
	Release(context.Context, BackupRetentionHoldReleaseRequest) (retention.HoldRecord, error)
}

type BackupRetentionPurgePreviewRequest struct {
	Actor            backupasset.AuditActor
	RepositoryID     string
	Items            []BackupRetentionPurgePlanItemView
	RecoveryPointIDs []string
}

type BackupRetentionPurgeImpactView struct {
	RepositoryID   string                             `json:"repository_id"`
	ImpactRevision int64                              `json:"impact_revision"`
	HoldCount      int64                              `json:"hold_count"`
	LeaseCount     int64                              `json:"lease_count"`
	WORMCount      int64                              `json:"worm_count"`
	SelectedCount  int                                `json:"selected_count"`
	Points         []BackupRetentionPurgePlanItemView `json:"points"`
}

type BackupRetentionPurgeService interface {
	Preview(context.Context, BackupRetentionPurgePreviewRequest) (BackupRetentionPurgeImpactView, error)
	CreatePlan(context.Context, BackupRetentionPurgePlanRequest) (BackupRetentionPurgePlanView, error)
	Execute(context.Context, BackupRetentionPurgeExecuteRequest) (BackupRetentionPurgeResult, error)
}

type BackupRetentionHandler struct {
	policies     BackupRetentionPolicyService
	holds        BackupRetentionHoldService
	purge        BackupRetentionPurgeService
	audit        BackupAssetAuditSink
	configSource BackupAssetHandlerConfigSource
}

func NewBackupRetentionHandler(
	policies BackupRetentionPolicyService,
	holds BackupRetentionHoldService,
	purge BackupRetentionPurgeService,
	audit BackupAssetAuditSink,
	configSource BackupAssetHandlerConfigSource,
) *BackupRetentionHandler {
	return &BackupRetentionHandler{policies: policies, holds: holds, purge: purge, audit: audit, configSource: configSource}
}

func NewRetentionPolicyHTTPService(service *retention.PolicyService) BackupRetentionPolicyService {
	return retentionPolicyHTTPService{service: service}
}

func NewRetentionHoldHTTPService(service *retention.HoldService) BackupRetentionHoldService {
	return retentionHoldHTTPService{service: service}
}

func NewRetentionPurgeHTTPService(service *retention.PurgeService) BackupRetentionPurgeService {
	return retentionPurgeHTTPService{service: service}
}

type backupRetentionPolicyWritePayload struct {
	ScopeKind        string                `json:"scope_kind"`
	ScopeID          string                `json:"scope_id"`
	ExpectedRevision int64                 `json:"expected_revision"`
	Rules            retention.PolicyRules `json:"rules"`
}

type backupRetentionRevisionPayload struct {
	ExpectedRevision int64      `json:"expected_revision"`
	Limit            int        `json:"limit"`
	InspectedLimit   int        `json:"inspected_limit"`
	Cursor           string     `json:"cursor"`
	EvaluatedAt      *time.Time `json:"evaluated_at"`
}

type backupRetentionHoldCreatePayload struct {
	HoldType  string     `json:"hold_type"`
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type backupRetentionHoldReleasePayload struct {
	Reason string `json:"reason"`
}

type backupRetentionPurgePreviewPayload struct {
	RecoveryPointIDs []string                           `json:"recovery_point_ids"`
	Items            []BackupRetentionPurgePlanItemView `json:"items"`
}

type backupRetentionPurgePlanPayload struct {
	ExpectedImpactRevision int64                              `json:"expected_impact_revision"`
	Items                  []BackupRetentionPurgePlanItemView `json:"items"`
}

type backupRetentionPurgeExecutePayload struct {
	PlanID                 string `json:"plan_id"`
	ExpectedRevision       int64  `json:"expected_revision"`
	ExpectedImpactRevision int64  `json:"expected_impact_revision"`
	Reason                 string `json:"reason"`
}

// ListPolicies godoc
// @Summary      列出活跃备份保留策略
// @Description  Admin 列出当前活跃的版本化保留策略；不返回私有 locator 或明文原因
// @Tags         backup-retention
// @Security     Bearer
// @Produce      json
// @Param        limit   query     int     false  "每页数量"
// @Param        cursor  query     string  false  "未签名策略 ID 游标"
// @Success      200     {object}  handlers.Response{data=BackupRetentionPolicyPage}
// @Failure      400     {object}  handlers.Response
// @Failure      401     {object}  handlers.Response
// @Failure      403     {object}  handlers.Response
// @Failure      503     {object}  handlers.Response
// @Router       /backup-retention-policies [get]
func (handler *BackupRetentionHandler) ListPolicies(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	limit, cursor, ok := backupRetentionPage(c)
	if !ok {
		return
	}
	result, err := handler.policies.List(c.Request.Context(), BackupRetentionPolicyListRequest{
		Actor: backupRetentionActor(c), Limit: limit, Cursor: cursor,
	})
	if err != nil {
		handler.finish(c, backupasset.AuditActionRetentionPolicyList, 0, result, err)
		return
	}
	respondOK(c, result)
}

// CreatePolicy godoc
// @Summary      创建备份保留策略
// @Description  Admin 创建仓库或 Task 链接范围的版本化保留策略
// @Tags         backup-retention
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      backupRetentionPolicyWritePayload  true  "策略范围与规则"
// @Success      200   {object}  handlers.Response{data=BackupRetentionPolicyView}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /backup-retention-policies [post]
func (handler *BackupRetentionHandler) CreatePolicy(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	var payload backupRetentionPolicyWritePayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || payload.ExpectedRevision != 0 ||
		backupasset.ValidateRetentionPolicyScope(backupasset.RetentionPolicyScopeKind(payload.ScopeKind), payload.ScopeID) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.policies.Create(backupRetentionMutationContext(c), BackupRetentionPolicyCreateRequest{
		Actor: backupRetentionActor(c), ScopeKind: backupasset.RetentionPolicyScopeKind(payload.ScopeKind),
		ScopeID: payload.ScopeID, Rules: payload.Rules,
	})
	handler.finishMutation(c, backupasset.AuditActionRetentionPolicyCreate, 1, result, err)
}

// UpdatePolicy godoc
// @Summary      更新备份保留策略
// @Description  按期望修订号更新活跃策略规则
// @Tags         backup-retention
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      string                             true  "策略 opaque ID"
// @Param        body  body      backupRetentionPolicyWritePayload  true  "期望修订与规则"
// @Success      200   {object}  handlers.Response{data=BackupRetentionPolicyView}
// @Failure      400   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /backup-retention-policies/{id} [patch]
func (handler *BackupRetentionHandler) UpdatePolicy(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	policyID, ok := backupRetentionOpaqueParam(c, "id")
	if !ok {
		return
	}
	var payload backupRetentionPolicyWritePayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || payload.ExpectedRevision < 1 || payload.ScopeKind != "" || payload.ScopeID != "" {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.policies.Update(backupRetentionMutationContext(c), BackupRetentionPolicyUpdateRequest{
		Actor: backupRetentionActor(c), PolicyID: policyID, ExpectedRevision: payload.ExpectedRevision, Rules: payload.Rules,
	})
	handler.finishMutation(c, backupasset.AuditActionRetentionPolicyUpdate, 1, result, err)
}

// DeletePolicy godoc
// @Summary      删除备份保留策略
// @Description  按期望修订号停用活跃策略
// @Tags         backup-retention
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      string                            true  "策略 opaque ID"
// @Param        body  body      backupRetentionRevisionPayload    true  "期望修订"
// @Success      200   {object}  handlers.Response{data=BackupRetentionPolicyView}
// @Failure      400   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /backup-retention-policies/{id} [delete]
func (handler *BackupRetentionHandler) DeletePolicy(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	policyID, ok := backupRetentionOpaqueParam(c, "id")
	if !ok {
		return
	}
	var payload backupRetentionRevisionPayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || payload.ExpectedRevision < 1 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.policies.Delete(backupRetentionMutationContext(c), BackupRetentionPolicyDeleteRequest{
		Actor: backupRetentionActor(c), PolicyID: policyID, ExpectedRevision: payload.ExpectedRevision,
	})
	handler.finishMutation(c, backupasset.AuditActionRetentionPolicyDelete, 1, result, err)
}

// PreviewImpact godoc
// @Summary      预览保留策略精确影响
// @Description  返回策略选中的精确 RecoveryPoint ID 以及 hold/lease/WORM 计数
// @Tags         backup-retention
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      string                          true  "策略 opaque ID"
// @Param        body  body      backupRetentionRevisionPayload  true  "期望修订"
// @Success      200   {object}  handlers.Response{data=BackupRetentionImpactView}
// @Failure      400   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /backup-retention-policies/{id}/impact [post]
func (handler *BackupRetentionHandler) PreviewImpact(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	policyID, ok := backupRetentionOpaqueParam(c, "id")
	if !ok {
		return
	}
	var payload backupRetentionRevisionPayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || payload.ExpectedRevision < 1 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	impactRequest := BackupRetentionImpactRequest{
		Actor: backupRetentionActor(c), PolicyID: policyID, ExpectedRevision: payload.ExpectedRevision,
		Limit: payload.Limit, InspectedLimit: payload.InspectedLimit, Cursor: payload.Cursor,
	}
	if payload.EvaluatedAt != nil {
		impactRequest.EvaluatedAt = payload.EvaluatedAt.UTC()
	}
	result, err := handler.policies.PreviewImpact(c.Request.Context(), impactRequest)
	handler.finish(c, backupasset.AuditActionRetentionPolicyUpdate, int64(result.SelectedCount), result, err)
}

// ListHolds godoc
// @Summary      列出恢复点冻结
// @Description  Admin 列出指定恢复点的活跃 hold；不返回加密原因
// @Tags         backup-retention
// @Security     Bearer
// @Produce      json
// @Param        id   path      string  true  "恢复点 opaque ID"
// @Success      200  {object}  handlers.Response{data=BackupRetentionHoldPage}
// @Failure      400  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Failure      503  {object}  handlers.Response
// @Router       /recovery-points/{id}/holds [get]
func (handler *BackupRetentionHandler) ListHolds(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	pointID, ok := backupRetentionOpaqueParam(c, "id")
	if !ok {
		return
	}
	result, err := handler.holds.List(c.Request.Context(), BackupRetentionHoldListRequest{
		Actor: backupRetentionActor(c), RecoveryPointID: pointID,
	})
	if err != nil {
		handler.finish(c, backupasset.AuditActionHoldList, 0, result, err)
		return
	}
	respondOK(c, result)
}

// CreateHold godoc
// @Summary      创建恢复点冻结
// @Description  对精确不可变 RecoveryPoint 创建 operational 或 legal hold
// @Tags         backup-retention
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      string                             true  "恢复点 opaque ID"
// @Param        body  body      backupRetentionHoldCreatePayload   true  "冻结类型与原因"
// @Success      200   {object}  handlers.Response{data=retention.HoldRecord}
// @Failure      400   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /recovery-points/{id}/holds [post]
func (handler *BackupRetentionHandler) CreateHold(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	pointID, ok := backupRetentionOpaqueParam(c, "id")
	if !ok {
		return
	}
	var payload backupRetentionHoldCreatePayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || backupasset.ValidateRecoveryPointHoldType(backupasset.RecoveryPointHoldType(payload.HoldType)) != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.holds.Create(backupRetentionMutationContext(c), BackupRetentionHoldCreateRequest{
		Actor: backupRetentionActor(c), RecoveryPointID: pointID,
		HoldType: backupasset.RecoveryPointHoldType(payload.HoldType), Reason: payload.Reason, ExpiresAt: payload.ExpiresAt,
	})
	handler.finishMutation(c, backupasset.AuditActionHoldCreate, 1, result, err)
}

// ReleaseHold godoc
// @Summary      释放恢复点冻结
// @Description  需要 Admin 与新鲜的 retention.hold_release proof；仓库 purge proof 不能授权
// @Tags         backup-retention
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id      path      string                              true  "恢复点 opaque ID"
// @Param        holdId  path      string                              true  "冻结 opaque ID"
// @Param        body    body      backupRetentionHoldReleasePayload   true  "释放原因"
// @Success      200     {object}  handlers.Response{data=retention.HoldRecord}
// @Failure      400     {object}  handlers.Response
// @Failure      403     {object}  handlers.Response
// @Failure      404     {object}  handlers.Response
// @Failure      409     {object}  handlers.Response
// @Failure      503     {object}  handlers.Response
// @Router       /recovery-points/{id}/holds/{holdId}/release [post]
func (handler *BackupRetentionHandler) ReleaseHold(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	pointID, ok := backupRetentionOpaqueParam(c, "id")
	if !ok {
		return
	}
	holdID, ok := backupRetentionOpaqueParam(c, "holdId")
	if !ok {
		return
	}
	var payload backupRetentionHoldReleasePayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || strings.TrimSpace(payload.Reason) == "" {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.holds.Release(backupRetentionMutationContext(c), BackupRetentionHoldReleaseRequest{
		Actor: backupRetentionActor(c), RecoveryPointID: pointID, HoldID: holdID, Reason: payload.Reason,
	})
	handler.finishMutation(c, backupasset.AuditActionHoldRelease, 1, result, err)
}

// PreviewPurge godoc
// @Summary      预览精确清理影响
// @Description  按所选 RecoveryPoint 计算仓库级清理影响修订，不要求仓库范围保留策略
// @Tags         backup-retention
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      string                               true  "仓库 opaque ID"
// @Param        body  body      backupRetentionPurgePreviewPayload   true  "精确清理项"
// @Success      200   {object}  handlers.Response{data=BackupRetentionPurgeImpactView}
// @Failure      400   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /backup-repositories/{id}/purge-preview [post]
func (handler *BackupRetentionHandler) PreviewPurge(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	repositoryID, ok := backupRetentionOpaqueParam(c, "id")
	if !ok {
		return
	}
	var payload backupRetentionPurgePreviewPayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || (len(payload.Items) == 0 && len(payload.RecoveryPointIDs) == 0) {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.purge.Preview(c.Request.Context(), BackupRetentionPurgePreviewRequest{
		Actor: backupRetentionActor(c), RepositoryID: repositoryID, Items: payload.Items,
		RecoveryPointIDs: payload.RecoveryPointIDs,
	})
	handler.finish(c, backupasset.AuditActionRepositoryPurgePlan, int64(result.SelectedCount), result, err)
}

// CreatePurgePlan godoc
// @Summary      创建精确清理计划
// @Description  冻结仓库内精确 RecoveryPoint 修订与 hold/lease/WORM 影响计数
// @Tags         backup-retention
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      string                            true  "仓库 opaque ID"
// @Param        body  body      backupRetentionPurgePlanPayload   true  "精确清理项"
// @Success      200   {object}  handlers.Response{data=BackupRetentionPurgePlanView}
// @Failure      400   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /backup-repositories/{id}/purge-plans [post]
func (handler *BackupRetentionHandler) CreatePurgePlan(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	repositoryID, ok := backupRetentionOpaqueParam(c, "id")
	if !ok {
		return
	}
	var payload backupRetentionPurgePlanPayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || payload.ExpectedImpactRevision < 1 || len(payload.Items) == 0 {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.purge.CreatePlan(backupRetentionMutationContext(c), BackupRetentionPurgePlanRequest{
		Actor: backupRetentionActor(c), RepositoryID: repositoryID,
		ExpectedImpactRevision: payload.ExpectedImpactRevision, Items: payload.Items,
	})
	handler.finishMutation(c, backupasset.AuditActionRepositoryPurgePlan, int64(result.ItemCount), result, err)
}

// ExecutePurge godoc
// @Summary      执行精确清理计划
// @Description  需要 Admin、backup_repositories:purge 与新鲜的 repository.purge proof；hold_release proof 不能授权
// @Tags         backup-retention
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      string                              true  "仓库 opaque ID"
// @Param        body  body      backupRetentionPurgeExecutePayload  true  "计划修订与原因"
// @Success      200   {object}  handlers.Response{data=BackupRetentionPurgeResult}
// @Failure      400   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Failure      409   {object}  handlers.Response
// @Failure      501   {object}  handlers.Response
// @Failure      503   {object}  handlers.Response
// @Router       /backup-repositories/{id}/purges [post]
func (handler *BackupRetentionHandler) ExecutePurge(c *gin.Context) {
	if !handler.ready(c) {
		return
	}
	repositoryID, ok := backupRetentionOpaqueParam(c, "id")
	if !ok {
		return
	}
	var payload backupRetentionPurgeExecutePayload
	if decodeStrictBackupAssetJSON(c, &payload) != nil || backupasset.ValidateOpaqueID(payload.PlanID) != nil ||
		payload.ExpectedRevision < 1 || payload.ExpectedImpactRevision < 1 || strings.TrimSpace(payload.Reason) == "" {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	result, err := handler.purge.Execute(backupRetentionMutationContext(c), BackupRetentionPurgeExecuteRequest{
		Actor: backupRetentionActor(c), RepositoryID: repositoryID, PlanID: payload.PlanID,
		ExpectedRevision: payload.ExpectedRevision, ExpectedImpactRevision: payload.ExpectedImpactRevision,
		Reason: payload.Reason, ProofDigest: sha256HeaderDigest(c.GetHeader(StepUpHeaderName)),
	})
	handler.finishMutation(c, backupasset.AuditActionRepositoryPurge, int64(result.Claimed), result, err)
}

func (handler *BackupRetentionHandler) ready(c *gin.Context) bool {
	config, ok := loadBackupAssetHandlerConfig(c, handler.configSource)
	if !ok {
		return false
	}
	if !ensureBackupAssetHandlerEnabled(c, config) {
		return false
	}
	if handler == nil || handler.policies == nil || handler.holds == nil || handler.purge == nil {
		respondInternalError(c, fmt.Errorf("backup retention service unavailable"))
		return false
	}
	return true
}

func (handler *BackupRetentionHandler) finish(c *gin.Context, action backupasset.AuditAction, itemCount int64, result any, err error) {
	handler.complete(c, action, itemCount, result, err, false)
}

func (handler *BackupRetentionHandler) finishMutation(c *gin.Context, action backupasset.AuditAction, itemCount int64, result any, err error) {
	handler.complete(c, action, itemCount, result, err, true)
}

func (handler *BackupRetentionHandler) complete(c *gin.Context, action backupasset.AuditAction, itemCount int64, result any, err error, mutation bool) {
	audit := backupAssetAuditInput(c, action)
	audit.ItemCount = itemCount
	if err == nil {
		if !mutation || !handler.mutationSelfAudited(action) {
			status := "success"
			if action == backupasset.AuditActionRepositoryPurge {
				status = "claimed"
			}
			audit.Outcome = backupasset.AuditOutcomeSuccess
			audit.Fields[backupasset.AuditFieldStatus] = status
			audit.Fields[backupasset.AuditFieldItemCount] = itemCount
			if auditErr := handler.writeAudit(c, audit); auditErr != nil {
				respondInternalError(c, auditErr)
				return
			}
		}
		respondOK(c, result)
		return
	}
	status, code := backupRetentionErrorStatus(err)
	if status == http.StatusBadRequest || status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusConflict {
		audit.Outcome = backupasset.AuditOutcomeBlocked
	} else {
		audit.Outcome = backupasset.AuditOutcomeFailure
	}
	audit.FailureCode = code
	audit.Fields[backupasset.AuditFieldStatus] = string(audit.Outcome)
	audit.Fields[backupasset.AuditFieldCode] = code
	if auditErr := handler.writeAudit(c, audit); auditErr != nil {
		respondInternalError(c, auditErr)
		return
	}
	respondBackupRetentionError(c, err, status)
}

func (handler *BackupRetentionHandler) mutationSelfAudited(action backupasset.AuditAction) bool {
	switch action {
	case backupasset.AuditActionRetentionPolicyCreate, backupasset.AuditActionRetentionPolicyUpdate, backupasset.AuditActionRetentionPolicyDelete:
		return retentionServiceSelfAudits(handler.policies)
	case backupasset.AuditActionHoldCreate, backupasset.AuditActionHoldRelease:
		return retentionServiceSelfAudits(handler.holds)
	case backupasset.AuditActionRepositoryPurgePlan, backupasset.AuditActionRepositoryPurge:
		return retentionServiceSelfAudits(handler.purge)
	default:
		return false
	}
}

func retentionServiceSelfAudits(service any) bool {
	type auditor interface{ AuditsMutations() bool }
	if candidate, ok := service.(auditor); ok {
		return candidate.AuditsMutations()
	}
	return false
}

func (handler *BackupRetentionHandler) writeAudit(c *gin.Context, input backupasset.AuditEventInput) error {
	if handler == nil || handler.audit == nil {
		return nil
	}
	if err := handler.audit.Write(c.Request.Context(), input); err != nil {
		return err
	}
	return nil
}

func backupRetentionActor(c *gin.Context) backupasset.AuditActor {
	return backupasset.AuditActor{
		UserID: middleware.CurrentUserID(c), Username: c.GetString(middleware.CtxUsername), Role: middleware.CurrentRole(c),
	}
}

func backupRetentionOpaqueParam(c *gin.Context, name string) (string, bool) {
	id := strings.TrimSpace(c.Param(name))
	if backupasset.ValidateOpaqueID(id) != nil {
		respondBadRequest(c, "请求参数不合法")
		return "", false
	}
	return id, true
}

func backupRetentionPage(c *gin.Context) (int, string, bool) {
	values, ok := backupAssetQuery(c, "limit", "cursor")
	if !ok {
		respondBadRequest(c, "分页参数不合法")
		return 0, "", false
	}
	limit := 50
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxBackupAssetPageLimit {
			respondBadRequest(c, "分页参数不合法")
			return 0, "", false
		}
		limit = parsed
	}
	cursor := values.Get("cursor")
	if len(cursor) > maxBackupAssetCursorBytes || cursor != strings.TrimSpace(cursor) {
		respondBadRequest(c, "分页参数不合法")
		return 0, "", false
	}
	return limit, cursor, true
}

func backupRetentionErrorStatus(err error) (int, string) {
	if reason, _, ok := backuprepository.CapabilityFromError(err); ok {
		switch reason.Code {
		case backupasset.CapabilityFeatureDisabled, backupasset.CapabilityRepositoryOffline,
			backupasset.CapabilityRepositoryDisconnected, backupasset.CapabilityProviderUnavailable,
			backupasset.CapabilityProviderOperationTimeout, backupasset.CapabilityProviderResourceLimit:
			return http.StatusServiceUnavailable, string(reason.Code)
		default:
			return http.StatusNotImplemented, string(reason.Code)
		}
	}
	switch {
	case errors.Is(err, backupasset.ErrInvalidState), errors.Is(err, backupasset.ErrInvalidAssetRef):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, backupasset.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, backupasset.ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, backupasset.ErrConflict):
		return http.StatusConflict, "stale_state"
	case errors.Is(err, catalog.ErrFeatureDisabled):
		return http.StatusServiceUnavailable, "feature_disabled"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

func respondBackupRetentionError(c *gin.Context, err error, status int) {
	if reason, correlationID, ok := backuprepository.CapabilityFromError(err); ok {
		if correlationID == "" {
			correlationID = c.GetString(middleware.RequestIDKey)
		}
		respondBackupCapabilityError(c, status, reason, correlationID)
		return
	}
	respondBackupAssetError(c, err, status)
}

func backupRetentionMutationContext(c *gin.Context) context.Context {
	ctx := c.Request.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	return retention.WithRequestCorrelationID(ctx, c.GetString(middleware.RequestIDKey))
}

func sha256HeaderDigest(value string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(digest[:])
}

type retentionPolicyHTTPService struct {
	service *retention.PolicyService
}

func (adapter retentionPolicyHTTPService) List(ctx context.Context, request BackupRetentionPolicyListRequest) (BackupRetentionPolicyPage, error) {
	if adapter.service == nil {
		return BackupRetentionPolicyPage{}, fmt.Errorf("%w: retention policy service unavailable", backupasset.ErrInvalidState)
	}
	limit := request.Limit
	if limit < 1 {
		limit = 50
	}
	afterID := strings.TrimSpace(request.Cursor)
	if afterID != "" && backupasset.ValidateOpaqueID(afterID) != nil {
		return BackupRetentionPolicyPage{}, fmt.Errorf("%w: invalid retention policy cursor", backupasset.ErrInvalidState)
	}
	records, err := adapter.service.ListActiveAfter(ctx, limit+1, afterID)
	if err != nil {
		return BackupRetentionPolicyPage{}, err
	}
	page := BackupRetentionPolicyPage{Items: make([]BackupRetentionPolicyView, 0, len(records))}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	for _, record := range records {
		page.Items = append(page.Items, backupRetentionPolicyView(record))
	}
	if hasMore {
		page.NextCursor = records[len(records)-1].ID
	}
	return page, nil
}

func (adapter retentionPolicyHTTPService) Create(ctx context.Context, request BackupRetentionPolicyCreateRequest) (BackupRetentionPolicyView, error) {
	if adapter.service == nil {
		return BackupRetentionPolicyView{}, fmt.Errorf("%w: retention policy service unavailable", backupasset.ErrInvalidState)
	}
	record, err := adapter.service.Create(ctx, retention.CreatePolicyRequest{
		Actor: request.Actor, ScopeKind: request.ScopeKind, ScopeID: request.ScopeID, Rules: request.Rules,
	})
	if err != nil {
		return BackupRetentionPolicyView{}, err
	}
	return backupRetentionPolicyView(record), nil
}

func (adapter retentionPolicyHTTPService) Update(ctx context.Context, request BackupRetentionPolicyUpdateRequest) (BackupRetentionPolicyView, error) {
	if adapter.service == nil {
		return BackupRetentionPolicyView{}, fmt.Errorf("%w: retention policy service unavailable", backupasset.ErrInvalidState)
	}
	record, err := adapter.service.Update(ctx, retention.UpdatePolicyRequest{
		Actor: request.Actor, PolicyID: request.PolicyID, ExpectedRevision: request.ExpectedRevision, Rules: request.Rules,
	})
	if err != nil {
		return BackupRetentionPolicyView{}, err
	}
	return backupRetentionPolicyView(record), nil
}

func (adapter retentionPolicyHTTPService) Delete(ctx context.Context, request BackupRetentionPolicyDeleteRequest) (BackupRetentionPolicyView, error) {
	if adapter.service == nil {
		return BackupRetentionPolicyView{}, fmt.Errorf("%w: retention policy service unavailable", backupasset.ErrInvalidState)
	}
	record, err := adapter.service.Delete(ctx, retention.DeletePolicyRequest{
		Actor: request.Actor, PolicyID: request.PolicyID, ExpectedRevision: request.ExpectedRevision,
	})
	if err != nil {
		return BackupRetentionPolicyView{}, err
	}
	return backupRetentionPolicyView(record), nil
}

func (adapter retentionPolicyHTTPService) PreviewImpact(ctx context.Context, request BackupRetentionImpactRequest) (BackupRetentionImpactView, error) {
	if adapter.service == nil {
		return BackupRetentionImpactView{}, fmt.Errorf("%w: retention policy service unavailable", backupasset.ErrInvalidState)
	}
	preview, err := adapter.service.PreviewImpact(ctx, request.Actor, retention.SelectionRequest{
		PolicyID: request.PolicyID, ExpectedRevision: request.ExpectedRevision, EvaluatedAt: request.EvaluatedAt,
		Limit: request.Limit, InspectedLimit: request.InspectedLimit, Cursor: request.Cursor,
	})
	if err != nil {
		return BackupRetentionImpactView{}, err
	}
	points := make([]BackupRetentionImpactPoint, 0, len(preview.Selection.Points))
	for _, point := range preview.Selection.Points {
		points = append(points, BackupRetentionImpactPoint{
			RecoveryPointID: point.RecoveryPointID, PointRevision: point.PointRevision, CapabilityRevision: point.CapabilityRevision,
		})
	}
	return BackupRetentionImpactView{
		PolicyID: preview.Selection.PolicyID, PolicyRevision: preview.Selection.PolicyRevision,
		ImpactRevision: preview.ImpactRevision, EvaluatedAt: preview.Selection.EvaluatedAt,
		SelectedCount: len(points), HoldCount: preview.HoldCount, LeaseCount: preview.LeaseCount,
		WORMCount: preview.WORMCount, Points: points, NextCursor: preview.Selection.NextCursor,
	}, nil
}

func (adapter retentionPolicyHTTPService) AuditsMutations() bool {
	return adapter.service != nil && adapter.service.AuditsMutations()
}

func (adapter retentionHoldHTTPService) AuditsMutations() bool {
	return adapter.service != nil && adapter.service.AuditsMutations()
}

func (adapter retentionPurgeHTTPService) AuditsMutations() bool {
	return adapter.service != nil && adapter.service.AuditsMutations()
}

type retentionHoldHTTPService struct {
	service *retention.HoldService
}

func (adapter retentionHoldHTTPService) List(ctx context.Context, request BackupRetentionHoldListRequest) (BackupRetentionHoldPage, error) {
	if adapter.service == nil {
		return BackupRetentionHoldPage{}, fmt.Errorf("%w: recovery point hold service unavailable", backupasset.ErrInvalidState)
	}
	holds, err := adapter.service.List(ctx, request.Actor, request.RecoveryPointID)
	if err != nil {
		return BackupRetentionHoldPage{}, err
	}
	return BackupRetentionHoldPage{Items: holds}, nil
}

func (adapter retentionHoldHTTPService) Create(ctx context.Context, request BackupRetentionHoldCreateRequest) (retention.HoldRecord, error) {
	if adapter.service == nil {
		return retention.HoldRecord{}, fmt.Errorf("%w: recovery point hold service unavailable", backupasset.ErrInvalidState)
	}
	return adapter.service.Create(ctx, retention.CreateHoldRequest{
		Actor: request.Actor, RecoveryPointID: request.RecoveryPointID, HoldType: request.HoldType,
		Reason: request.Reason, ExpiresAt: request.ExpiresAt,
	})
}

func (adapter retentionHoldHTTPService) Release(ctx context.Context, request BackupRetentionHoldReleaseRequest) (retention.HoldRecord, error) {
	if adapter.service == nil {
		return retention.HoldRecord{}, fmt.Errorf("%w: recovery point hold service unavailable", backupasset.ErrInvalidState)
	}
	return adapter.service.Release(ctx, retention.ReleaseHoldRequest{
		Actor: request.Actor, RecoveryPointID: request.RecoveryPointID, HoldID: request.HoldID, Reason: request.Reason,
	})
}

type retentionPurgeHTTPService struct {
	service *retention.PurgeService
}

func (adapter retentionPurgeHTTPService) Preview(ctx context.Context, request BackupRetentionPurgePreviewRequest) (BackupRetentionPurgeImpactView, error) {
	if adapter.service == nil {
		return BackupRetentionPurgeImpactView{}, fmt.Errorf("%w: retention purge service unavailable", backupasset.ErrInvalidState)
	}
	items := make([]retention.PurgePlanItemInput, 0, len(request.Items))
	for _, item := range request.Items {
		items = append(items, retention.PurgePlanItemInput{
			RecoveryPointID: item.RecoveryPointID, PointRevision: item.PointRevision, CapabilityRevision: item.CapabilityRevision,
		})
	}
	preview, err := adapter.service.Preview(ctx, retention.PreviewPurgeRequest{
		Actor: request.Actor, RepositoryID: request.RepositoryID, Items: items,
		RecoveryPointIDs: request.RecoveryPointIDs,
	})
	if err != nil {
		return BackupRetentionPurgeImpactView{}, err
	}
	return backupRetentionPurgeImpactView(preview), nil
}

func (adapter retentionPurgeHTTPService) CreatePlan(ctx context.Context, request BackupRetentionPurgePlanRequest) (BackupRetentionPurgePlanView, error) {
	if adapter.service == nil {
		return BackupRetentionPurgePlanView{}, fmt.Errorf("%w: retention purge service unavailable", backupasset.ErrInvalidState)
	}
	items := make([]retention.PurgePlanItemInput, 0, len(request.Items))
	for _, item := range request.Items {
		items = append(items, retention.PurgePlanItemInput{
			RecoveryPointID: item.RecoveryPointID, PointRevision: item.PointRevision, CapabilityRevision: item.CapabilityRevision,
		})
	}
	plan, err := adapter.service.CreatePlan(ctx, retention.CreatePurgePlanRequest{
		Actor: request.Actor, RepositoryID: request.RepositoryID,
		ExpectedImpactRevision: request.ExpectedImpactRevision, Items: items,
	})
	if err != nil {
		return BackupRetentionPurgePlanView{}, err
	}
	return backupRetentionPurgePlanView(plan), nil
}

func (adapter retentionPurgeHTTPService) Execute(ctx context.Context, request BackupRetentionPurgeExecuteRequest) (BackupRetentionPurgeResult, error) {
	if adapter.service == nil {
		return BackupRetentionPurgeResult{}, fmt.Errorf("%w: retention purge service unavailable", backupasset.ErrInvalidState)
	}
	result, err := adapter.service.Execute(ctx, retention.ExecutePurgeRequest{
		Actor: request.Actor, RepositoryID: request.RepositoryID, PlanID: request.PlanID,
		ExpectedRevision: request.ExpectedRevision, ExpectedImpactRevision: request.ExpectedImpactRevision,
		Reason: request.Reason, ProofDigest: request.ProofDigest,
	})
	if err != nil {
		return BackupRetentionPurgeResult{}, err
	}
	return BackupRetentionPurgeResult{PlanID: result.PlanID, Claimed: result.Claimed, Blocked: result.Blocked}, nil
}

func backupRetentionPolicyView(record retention.PolicyRecord) BackupRetentionPolicyView {
	return BackupRetentionPolicyView{
		ID: record.ID, ScopeKind: record.ScopeKind, ScopeID: record.ScopeID, Revision: record.Revision,
		Rules: record.Rules, RuleDigest: record.RuleDigest, Status: record.Status,
		CreatedBy: record.CreatedBy, UpdatedBy: record.UpdatedBy, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func backupRetentionPurgeImpactView(plan retention.PurgePlanView) BackupRetentionPurgeImpactView {
	items := make([]BackupRetentionPurgePlanItemView, 0, len(plan.Items))
	for _, item := range plan.Items {
		items = append(items, BackupRetentionPurgePlanItemView{
			RecoveryPointID: item.RecoveryPointID, PointRevision: item.PointRevision, CapabilityRevision: item.CapabilityRevision,
		})
	}
	return BackupRetentionPurgeImpactView{
		RepositoryID: plan.RepositoryID, ImpactRevision: plan.ImpactRevision,
		HoldCount: plan.HoldCount, LeaseCount: plan.LeaseCount, WORMCount: plan.WORMCount,
		SelectedCount: plan.ItemCount, Points: items,
	}
}

func backupRetentionPurgePlanView(plan retention.PurgePlanView) BackupRetentionPurgePlanView {
	items := make([]BackupRetentionPurgePlanItemView, 0, len(plan.Items))
	for _, item := range plan.Items {
		items = append(items, BackupRetentionPurgePlanItemView{
			RecoveryPointID: item.RecoveryPointID, PointRevision: item.PointRevision, CapabilityRevision: item.CapabilityRevision,
		})
	}
	return BackupRetentionPurgePlanView{
		ID: plan.ID, RepositoryID: plan.RepositoryID, Revision: plan.Revision, ImpactRevision: plan.ImpactRevision,
		ExpiresAt: plan.ExpiresAt, HoldCount: plan.HoldCount, LeaseCount: plan.LeaseCount, WORMCount: plan.WORMCount,
		Status: plan.Status, ItemCount: plan.ItemCount, Items: items,
	}
}
