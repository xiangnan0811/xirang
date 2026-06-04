package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"xirang/backend/internal/config"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"
	"xirang/backend/internal/profile"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/sshutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// drillTriggerer 定义恢复演练触发接口，由 *task.Manager 实现。
// 独立接口便于测试注入，避免 handler 直接依赖 task 包。
type drillTriggerer interface {
	TriggerDrill(policyID uint) (uint, error)
}

type PolicyHandler struct {
	db             *gorm.DB
	runner         policy.TaskRunner
	drillTriggerer drillTriggerer
	svc            *policy.PolicyService
}

func NewPolicyHandler(db *gorm.DB, runner policy.TaskRunner) *PolicyHandler {
	var dt drillTriggerer
	if t, ok := runner.(drillTriggerer); ok {
		dt = t
	}
	return &PolicyHandler{db: db, runner: runner, drillTriggerer: dt}
}

// WithPolicyService injects a PolicyService for business logic delegation.
func (h *PolicyHandler) WithPolicyService(svc *policy.PolicyService) *PolicyHandler {
	h.svc = svc
	return h
}

// service returns the injected PolicyService, or lazily creates one
// from the handler's db and runner (backward-compatible with tests).
func (h *PolicyHandler) service() *policy.PolicyService {
	if h.svc != nil {
		return h.svc
	}
	return policy.NewPolicyService(gormrepo.NewPolicyRepository(h.db), h.runner)
}

type policyRequest struct {
	Name                string `json:"name" binding:"required"`
	Description         string `json:"description"`
	SourcePath          string `json:"source_path" binding:"required"`
	TargetPath          string `json:"target_path"`
	CronSpec            string `json:"cron_spec" binding:"required"`
	ExcludeRules        string `json:"exclude_rules"`
	BwLimit             int    `json:"bwlimit"`
	RetentionDays       int    `json:"retention_days"`
	MaxConcurrent       int    `json:"max_concurrent"`
	Enabled             *bool  `json:"enabled"`
	VerifyEnabled       *bool  `json:"verify_enabled"`
	VerifySampleRate    *int   `json:"verify_sample_rate"`
	IsTemplate          *bool  `json:"is_template"`
	PreHook             string `json:"pre_hook"`
	PostHook            string `json:"post_hook"`
	HookTimeoutSeconds  *int   `json:"hook_timeout_seconds"`
	MaxExecutionSeconds *int   `json:"max_execution_seconds"`
	MaxRetries          *int   `json:"max_retries"`
	RetryBaseSeconds    *int   `json:"retry_base_seconds"`
	BandwidthSchedule   string `json:"bandwidth_schedule"`
	AppProfile          string `json:"app_profile"`
	AppCredentialID     *uint  `json:"app_credential_id"`
	EscalationPolicyID  *uint  `json:"escalation_policy_id"`
	DrillEnabled        *bool  `json:"drill_enabled"`
	DrillCron           string `json:"drill_cron"`
	DrillTargetNodeID   *uint  `json:"drill_target_node_id"`
	DrillRestorePath    string `json:"drill_restore_path"`
	DrillPreVerify      string `json:"drill_pre_verify"`
	DrillVerify         string `json:"drill_verify"`
	DrillPostVerify     string `json:"drill_post_verify"`
	DrillAutoCleanup    *bool  `json:"drill_auto_cleanup"`
	RPOMinutes          *int   `json:"rpo_minutes"`
	RTOMinutes          *int   `json:"rto_minutes"`
	RetentionMode       string `json:"retention_mode"`
	KeepDaily           *int   `json:"keep_daily"`
	KeepWeekly          *int   `json:"keep_weekly"`
	KeepMonthly         *int   `json:"keep_monthly"`
	KeepYearly          *int   `json:"keep_yearly"`
	NodeIDs             []uint `json:"node_ids"`
}

// List godoc
// @Summary      列出备份策略
// @Description  返回所有备份策略列表
// @Tags         policies
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response{data=[]object}
// @Failure      401  {object}  handlers.Response
// @Router       /policies [get]
func (h *PolicyHandler) List(c *gin.Context) {
	// TODO: Preloading full Nodes is wasteful — buildPolicyResponse only uses node IDs.
	// Replace Preload("Nodes") with a lighter query on policy_nodes to collect policy_id → []nodeID
	// maps, then pass those maps into buildPolicyResponse instead of p.Nodes.
	query := h.db.Preload("Nodes").Order("id asc")

	if nodeIDs, needFilter, err := ownershipNodeFilter(c, h.db); err != nil {
		respondInternalError(c, err)
		return
	} else if needFilter {
		// union 规则：策略关联的任意节点属于 operator 即可见
		query = query.Where("id IN (SELECT policy_id FROM policy_nodes WHERE node_id IN ?)", nodeIDs)
	}

	var policies []model.Policy
	if err := query.Find(&policies).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	latestDrillByPolicy, err := h.latestDrillSummaries(c, policies)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	result := make([]gin.H, len(policies))
	for i, p := range policies {
		result[i] = buildPolicyResponse(p, latestDrillByPolicy[p.ID])
	}
	respondOK(c, result)
}

// Get godoc
// @Summary      获取备份策略详情
// @Description  返回单个备份策略的详细信息
// @Tags         policies
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "策略 ID"
// @Success      200  {object}  handlers.Response{data=object}
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /policies/{id} [get]
func (h *PolicyHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var p model.Policy
	if err := h.db.Preload("Nodes").First(&p, id).Error; err != nil {
		respondNotFound(c, "策略不存在")
		return
	}
	if allowed, err := authorizePolicyOwnership(c, h.db, p); err != nil {
		respondInternalError(c, err)
		return
	} else if !allowed {
		respondForbidden(c, "无权访问该策略")
		return
	}
	latestDrill, err := h.latestDrillSummary(c, p.ID)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, buildPolicyResponse(p, latestDrill))
}

// Create godoc
// @Summary      创建备份策略
// @Description  创建新的备份策略（支持应用感知备份：可通过 app_profile 选择数据库类型并关联凭据，自动生成 dump hook）
// @Tags         policies
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      policyRequest  true  "创建策略请求"
// @Success      201   {object}  handlers.Response{data=object}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      403   {object}  handlers.Response
// @Router       /policies [post]
func (h *PolicyHandler) Create(c *gin.Context) {
	var req policyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.SourcePath = strings.TrimSpace(req.SourcePath)
	req.TargetPath = strings.TrimSpace(req.TargetPath)
	req.CronSpec = strings.TrimSpace(req.CronSpec)
	req.TargetPath = config.BackupRoot

	if req.Name == "" || req.SourcePath == "" || req.CronSpec == "" {
		respondBadRequest(c, "请求参数不合法")
		return
	}
	if err := validateCronSpec(req.CronSpec); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := validatePathChars(req.SourcePath, "source_path"); err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	// 非 admin 不允许设置 hook 命令
	if req.PreHook != "" || req.PostHook != "" {
		role, _ := c.Get("role")
		if roleStr, ok := role.(string); !ok || roleStr != "admin" {
			respondForbidden(c, "仅管理员可配置 hook 命令")
			return
		}
	}
	if req.PreHook != "" {
		if err := policy.ValidateHookCommand(req.PreHook); err != nil {
			respondBadRequest(c, err.Error())
			return
		}
	}
	if req.PostHook != "" {
		if err := policy.ValidateHookCommand(req.PostHook); err != nil {
			respondBadRequest(c, err.Error())
			return
		}
	}

	if req.MaxConcurrent == 0 {
		req.MaxConcurrent = 1
	}
	if req.RetentionDays == 0 {
		req.RetentionDays = 7
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	verifyEnabled := true
	if req.VerifyEnabled != nil {
		verifyEnabled = *req.VerifyEnabled
	}
	verifySampleRate := 0
	if req.VerifySampleRate != nil {
		verifySampleRate = *req.VerifySampleRate
	}

	isTemplate := false
	if req.IsTemplate != nil {
		isTemplate = *req.IsTemplate
	}

	// 应用感知备份：app_profile 非空时渲染 hook
	preHook := strings.TrimSpace(req.PreHook)
	postHook := strings.TrimSpace(req.PostHook)
	appProfile := strings.TrimSpace(req.AppProfile)

	if appProfile != "" {
		if _, ok := profile.GetProfile(appProfile); !ok {
			respondBadRequest(c, "不支持的应用类型: "+appProfile)
			return
		}
		// 用户手动提供了 hook → 保留用户值（用户 override 优先级最高）
		userProvidedPre := preHook != ""
		userProvidedPost := postHook != ""

		if !userProvidedPre || !userProvidedPost {
			// 需要从 profile 渲染 hook
			if req.AppCredentialID == nil || *req.AppCredentialID == 0 {
				respondBadRequest(c, "选择应用类型后必须指定凭据")
				return
			}
			access, err := profile.ResolveAppProfileAccess(h.db, *req.AppCredentialID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					respondBadRequest(c, "指定的凭据不存在")
					return
				}
				respondInternalError(c, err)
				return
			}
			renderedPre, renderedPost, err := profile.RenderHooks(appProfile, access.Config())
			if err != nil {
				respondInternalError(c, err)
				return
			}
			if !userProvidedPre {
				preHook = renderedPre
			}
			if !userProvidedPost {
				postHook = renderedPost
			}
		}
	}

	// drill 演练配置校验
	drillEnabled := false
	if req.DrillEnabled != nil {
		drillEnabled = *req.DrillEnabled
	}
	drillCron := strings.TrimSpace(req.DrillCron)
	drillRestorePath := strings.TrimSpace(req.DrillRestorePath)
	if drillRestorePath == "" {
		drillRestorePath = "/tmp/xirang-drill"
	}
	if drillEnabled {
		if drillCron == "" {
			respondBadRequest(c, "启用恢复演练后必须设置 drill_cron")
			return
		}
		if err := validateCronSpec(drillCron); err != nil {
			respondBadRequest(c, "drill_cron 格式不合法: "+err.Error())
			return
		}
		if req.DrillTargetNodeID == nil || *req.DrillTargetNodeID == 0 {
			respondBadRequest(c, "启用恢复演练后必须指定沙箱节点 drill_target_node_id")
			return
		}
		// 沙箱节点不能等于任一备份源节点
		for _, nid := range req.NodeIDs {
			if nid == *req.DrillTargetNodeID {
				respondBadRequest(c, "沙箱节点不能与备份源节点相同")
				return
			}
		}
		if err := policy.ValidateDrillRestorePath(drillRestorePath); err != nil {
			respondBadRequest(c, err.Error())
			return
		}
		// 校验 drill 脚本长度
		if len(req.DrillPreVerify) > 4096 {
			respondBadRequest(c, "drill_pre_verify 长度不能超过 4096 个字符")
			return
		}
		if len(req.DrillVerify) > 4096 {
			respondBadRequest(c, "drill_verify 长度不能超过 4096 个字符")
			return
		}
		if len(req.DrillPostVerify) > 4096 {
			respondBadRequest(c, "drill_post_verify 长度不能超过 4096 个字符")
			return
		}
	}
	drillAutoCleanup := true
	if req.DrillAutoCleanup != nil {
		drillAutoCleanup = *req.DrillAutoCleanup
	}

	// GFS 保留模式校验
	retentionMode := strings.TrimSpace(req.RetentionMode)
	if retentionMode == "" {
		retentionMode = "simple"
	}
	if retentionMode != "simple" && retentionMode != "gfs" {
		respondBadRequest(c, "retention_mode 只能是 simple 或 gfs")
		return
	}
	keepDaily := 0
	if req.KeepDaily != nil {
		keepDaily = *req.KeepDaily
	}
	keepWeekly := 0
	if req.KeepWeekly != nil {
		keepWeekly = *req.KeepWeekly
	}
	keepMonthly := 0
	if req.KeepMonthly != nil {
		keepMonthly = *req.KeepMonthly
	}
	keepYearly := 0
	if req.KeepYearly != nil {
		keepYearly = *req.KeepYearly
	}
	if retentionMode == "gfs" && keepDaily == 0 && keepWeekly == 0 && keepMonthly == 0 && keepYearly == 0 {
		respondBadRequest(c, "GFS 模式下至少需要设置一个保留数量（keep_daily/weekly/monthly/yearly）")
		return
	}
	// RPO/RTO 目标
	rpoMinutes := 0
	if req.RPOMinutes != nil {
		rpoMinutes = *req.RPOMinutes
	}
	rtoMinutes := 0
	if req.RTOMinutes != nil {
		rtoMinutes = *req.RTOMinutes
	}

	p := model.Policy{
		Name:               req.Name,
		Description:        strings.TrimSpace(req.Description),
		SourcePath:         req.SourcePath,
		TargetPath:         req.TargetPath,
		CronSpec:           req.CronSpec,
		ExcludeRules:       strings.TrimSpace(req.ExcludeRules),
		BwLimit:            req.BwLimit,
		RetentionDays:      req.RetentionDays,
		MaxConcurrent:      req.MaxConcurrent,
		Enabled:            enabled,
		VerifyEnabled:      verifyEnabled,
		VerifySampleRate:   verifySampleRate,
		IsTemplate:         isTemplate,
		PreHook:            preHook,
		PostHook:           postHook,
		AppProfile:         appProfile,
		AppCredentialID:    req.AppCredentialID,
		EscalationPolicyID: req.EscalationPolicyID,
		BandwidthSchedule:  strings.TrimSpace(req.BandwidthSchedule),
		DrillEnabled:       drillEnabled,
		DrillCron:          drillCron,
		DrillTargetNodeID:  req.DrillTargetNodeID,
		DrillRestorePath:   drillRestorePath,
		DrillPreVerify:     strings.TrimSpace(req.DrillPreVerify),
		DrillVerify:        strings.TrimSpace(req.DrillVerify),
		DrillPostVerify:    strings.TrimSpace(req.DrillPostVerify),
		DrillAutoCleanup:   drillAutoCleanup,
		RPOMinutes:         rpoMinutes,
		RTOMinutes:         rtoMinutes,
		RetentionMode:      retentionMode,
		KeepDaily:          keepDaily,
		KeepWeekly:         keepWeekly,
		KeepMonthly:        keepMonthly,
		KeepYearly:         keepYearly,
	}
	if req.HookTimeoutSeconds != nil {
		if *req.HookTimeoutSeconds < 0 || *req.HookTimeoutSeconds > 3600 {
			respondBadRequest(c, "hook 超时时间必须在 0-3600 秒之间")
			return
		}
		p.HookTimeoutSeconds = *req.HookTimeoutSeconds
	}
	if req.MaxExecutionSeconds != nil {
		if *req.MaxExecutionSeconds < 0 || *req.MaxExecutionSeconds > 7*86400 {
			respondBadRequest(c, "任务最大执行秒数必须在 0-604800 (7 天) 之间，0=使用全局兜底")
			return
		}
		p.MaxExecutionSeconds = *req.MaxExecutionSeconds
	}
	if req.MaxRetries != nil {
		p.MaxRetries = *req.MaxRetries
	}
	if req.RetryBaseSeconds != nil {
		p.RetryBaseSeconds = *req.RetryBaseSeconds
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&p).Error; err != nil {
			return err
		}
		// 保存策略-节点关联
		if len(req.NodeIDs) > 0 {
			// 验证所有节点 ID 存在
			var existCount int64
			if err := tx.Model(&model.Node{}).Where("id IN ?", req.NodeIDs).Count(&existCount).Error; err != nil {
				return err
			}
			if int(existCount) != len(req.NodeIDs) {
				return fmt.Errorf("部分节点不存在，请检查节点列表")
			}
			for _, nid := range req.NodeIDs {
				pn := model.PolicyNode{PolicyID: p.ID, NodeID: nid}
				if err := tx.Create(&pn).Error; err != nil {
					return err
				}
			}
			// 模板策略不生成任务
			if h.runner != nil && !p.IsTemplate {
				if err := policy.SyncPolicyTasks(tx, h.runner, p, req.NodeIDs); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	// 重新加载以获取关联节点
	h.db.Preload("Nodes").First(&p, p.ID)
	respondCreated(c, buildPolicyResponse(p, nil))
}

// Update godoc
// @Summary      更新备份策略
// @Description  更新备份策略配置（支持应用感知备份：可通过 app_profile 选择数据库类型并关联凭据，自动生成 dump hook）
// @Tags         policies
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        id    path      int            true  "策略 ID"
// @Param        body  body      policyRequest  true  "更新策略请求"
// @Success      200   {object}  handlers.Response{data=object}
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Failure      404   {object}  handlers.Response
// @Router       /policies/{id} [put]
func (h *PolicyHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req policyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	var p model.Policy
	if err := h.db.First(&p, id).Error; err != nil {
		respondNotFound(c, "策略不存在")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.SourcePath = strings.TrimSpace(req.SourcePath)
	req.TargetPath = strings.TrimSpace(req.TargetPath)
	req.CronSpec = strings.TrimSpace(req.CronSpec)
	oldTargetPath := p.TargetPath

	if req.Name == "" {
		req.Name = p.Name
	}
	if req.SourcePath == "" {
		req.SourcePath = p.SourcePath
	}
	if req.TargetPath == "" {
		req.TargetPath = p.TargetPath
	}
	req.TargetPath = config.BackupRoot
	if req.CronSpec == "" {
		req.CronSpec = p.CronSpec
	}

	if err := validateCronSpec(req.CronSpec); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := validatePathChars(req.SourcePath, "source_path"); err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	// 非 admin 不允许设置 hook 命令
	if req.PreHook != "" || req.PostHook != "" {
		role, _ := c.Get("role")
		if roleStr, ok := role.(string); !ok || roleStr != "admin" {
			respondForbidden(c, "仅管理员可配置 hook 命令")
			return
		}
	}
	if req.PreHook != "" {
		if err := policy.ValidateHookCommand(req.PreHook); err != nil {
			respondBadRequest(c, err.Error())
			return
		}
	}
	if req.PostHook != "" {
		if err := policy.ValidateHookCommand(req.PostHook); err != nil {
			respondBadRequest(c, err.Error())
			return
		}
	}

	if req.MaxConcurrent == 0 {
		req.MaxConcurrent = p.MaxConcurrent
		if req.MaxConcurrent == 0 {
			req.MaxConcurrent = 1
		}
	}
	if req.RetentionDays == 0 {
		req.RetentionDays = p.RetentionDays
		if req.RetentionDays == 0 {
			req.RetentionDays = 7
		}
	}

	// 应用感知备份：app_profile 非空时渲染 hook
	appProfile := strings.TrimSpace(req.AppProfile)
	updatePreHook := strings.TrimSpace(req.PreHook)
	updatePostHook := strings.TrimSpace(req.PostHook)

	if appProfile != "" {
		if _, ok := profile.GetProfile(appProfile); !ok {
			respondBadRequest(c, "不支持的应用类型: "+appProfile)
			return
		}
		userProvidedPre := updatePreHook != ""
		userProvidedPost := updatePostHook != ""

		if !userProvidedPre || !userProvidedPost {
			if req.AppCredentialID == nil || *req.AppCredentialID == 0 {
				respondBadRequest(c, "选择应用类型后必须指定凭据")
				return
			}
			access, err := profile.ResolveAppProfileAccess(h.db, *req.AppCredentialID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					respondBadRequest(c, "指定的凭据不存在")
					return
				}
				respondInternalError(c, err)
				return
			}
			renderedPre, renderedPost, err := profile.RenderHooks(appProfile, access.Config())
			if err != nil {
				respondInternalError(c, err)
				return
			}
			if !userProvidedPre {
				updatePreHook = renderedPre
			}
			if !userProvidedPost {
				updatePostHook = renderedPost
			}
		}
	}

	// drill 演练配置校验
	drillEnabledUpdate := p.DrillEnabled
	if req.DrillEnabled != nil {
		drillEnabledUpdate = *req.DrillEnabled
	}
	drillCronUpdate := strings.TrimSpace(req.DrillCron)
	if drillCronUpdate == "" && req.DrillEnabled == nil {
		drillCronUpdate = p.DrillCron
	}
	drillTargetNodeIDUpdate := req.DrillTargetNodeID
	if drillTargetNodeIDUpdate == nil && req.DrillEnabled == nil {
		drillTargetNodeIDUpdate = p.DrillTargetNodeID
	}
	drillPathUpdate := strings.TrimSpace(req.DrillRestorePath)
	if drillPathUpdate == "" && req.DrillEnabled == nil {
		drillPathUpdate = p.DrillRestorePath
	}
	if drillPathUpdate == "" {
		drillPathUpdate = "/tmp/xirang-drill"
	}
	drillPreVerifyUpdate := strings.TrimSpace(req.DrillPreVerify)
	if drillPreVerifyUpdate == "" && req.DrillEnabled == nil {
		drillPreVerifyUpdate = p.DrillPreVerify
	}
	drillVerifyUpdate := strings.TrimSpace(req.DrillVerify)
	if drillVerifyUpdate == "" && req.DrillEnabled == nil {
		drillVerifyUpdate = p.DrillVerify
	}
	drillPostVerifyUpdate := strings.TrimSpace(req.DrillPostVerify)
	if drillPostVerifyUpdate == "" && req.DrillEnabled == nil {
		drillPostVerifyUpdate = p.DrillPostVerify
	}
	if drillEnabledUpdate {
		if drillCronUpdate == "" {
			respondBadRequest(c, "启用恢复演练后必须设置 drill_cron")
			return
		}
		if err := validateCronSpec(drillCronUpdate); err != nil {
			respondBadRequest(c, "drill_cron 格式不合法: "+err.Error())
			return
		}
		if drillTargetNodeIDUpdate == nil || *drillTargetNodeIDUpdate == 0 {
			respondBadRequest(c, "启用恢复演练后必须指定沙箱节点 drill_target_node_id")
			return
		}
		// 沙箱节点不能等于任一备份源节点
		if req.NodeIDs != nil {
			for _, nid := range req.NodeIDs {
				if nid == *drillTargetNodeIDUpdate {
					respondBadRequest(c, "沙箱节点不能与备份源节点相同")
					return
				}
			}
		}
		if err := policy.ValidateDrillRestorePath(drillPathUpdate); err != nil {
			respondBadRequest(c, err.Error())
			return
		}
		if len(drillPreVerifyUpdate) > 4096 {
			respondBadRequest(c, "drill_pre_verify 长度不能超过 4096 个字符")
			return
		}
		if len(drillVerifyUpdate) > 4096 {
			respondBadRequest(c, "drill_verify 长度不能超过 4096 个字符")
			return
		}
		if len(drillPostVerifyUpdate) > 4096 {
			respondBadRequest(c, "drill_post_verify 长度不能超过 4096 个字符")
			return
		}
	}
	previousEnabled := p.Enabled

	// GFS 保留模式校验
	retentionModeUpdate := strings.TrimSpace(req.RetentionMode)
	if retentionModeUpdate == "" {
		retentionModeUpdate = p.RetentionMode
		if retentionModeUpdate == "" {
			retentionModeUpdate = "simple"
		}
	}
	if retentionModeUpdate != "simple" && retentionModeUpdate != "gfs" {
		respondBadRequest(c, "retention_mode 只能是 simple 或 gfs")
		return
	}
	keepDailyUpdate := p.KeepDaily
	if req.KeepDaily != nil {
		keepDailyUpdate = *req.KeepDaily
	}
	keepWeeklyUpdate := p.KeepWeekly
	if req.KeepWeekly != nil {
		keepWeeklyUpdate = *req.KeepWeekly
	}
	keepMonthlyUpdate := p.KeepMonthly
	if req.KeepMonthly != nil {
		keepMonthlyUpdate = *req.KeepMonthly
	}
	keepYearlyUpdate := p.KeepYearly
	if req.KeepYearly != nil {
		keepYearlyUpdate = *req.KeepYearly
	}
	if retentionModeUpdate == "gfs" && keepDailyUpdate == 0 && keepWeeklyUpdate == 0 && keepMonthlyUpdate == 0 && keepYearlyUpdate == 0 {
		respondBadRequest(c, "GFS 模式下至少需要设置一个保留数量（keep_daily/weekly/monthly/yearly）")
		return
	}
	// RPO/RTO 目标
	rpoMinutesUpdate := p.RPOMinutes
	if req.RPOMinutes != nil {
		rpoMinutesUpdate = *req.RPOMinutes
	}
	rtoMinutesUpdate := p.RTOMinutes
	if req.RTOMinutes != nil {
		rtoMinutesUpdate = *req.RTOMinutes
	}

	p.Name = req.Name
	p.Description = strings.TrimSpace(req.Description)
	p.SourcePath = req.SourcePath
	p.TargetPath = req.TargetPath
	p.CronSpec = req.CronSpec
	p.ExcludeRules = strings.TrimSpace(req.ExcludeRules)
	p.BwLimit = req.BwLimit
	p.RetentionDays = req.RetentionDays
	p.MaxConcurrent = req.MaxConcurrent
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if req.VerifyEnabled != nil {
		p.VerifyEnabled = *req.VerifyEnabled
	}
	if req.VerifySampleRate != nil {
		p.VerifySampleRate = *req.VerifySampleRate
	}
	if req.IsTemplate != nil {
		p.IsTemplate = *req.IsTemplate
	}
	p.PreHook = updatePreHook
	p.PostHook = updatePostHook
	p.AppProfile = appProfile
	p.AppCredentialID = req.AppCredentialID
	p.EscalationPolicyID = req.EscalationPolicyID
	p.BandwidthSchedule = strings.TrimSpace(req.BandwidthSchedule)
	p.DrillEnabled = drillEnabledUpdate
	p.DrillCron = drillCronUpdate
	p.DrillTargetNodeID = drillTargetNodeIDUpdate
	p.DrillRestorePath = drillPathUpdate
	p.DrillPreVerify = drillPreVerifyUpdate
	p.DrillVerify = drillVerifyUpdate
	p.DrillPostVerify = drillPostVerifyUpdate
	if req.DrillAutoCleanup != nil {
		p.DrillAutoCleanup = *req.DrillAutoCleanup
	}
	p.RPOMinutes = rpoMinutesUpdate
	p.RTOMinutes = rtoMinutesUpdate
	p.RetentionMode = retentionModeUpdate
	p.KeepDaily = keepDailyUpdate
	p.KeepWeekly = keepWeeklyUpdate
	p.KeepMonthly = keepMonthlyUpdate
	p.KeepYearly = keepYearlyUpdate
	if req.HookTimeoutSeconds != nil {
		if *req.HookTimeoutSeconds < 0 || *req.HookTimeoutSeconds > 3600 {
			respondBadRequest(c, "hook 超时时间必须在 0-3600 秒之间")
			return
		}
		p.HookTimeoutSeconds = *req.HookTimeoutSeconds
	}
	if req.MaxExecutionSeconds != nil {
		if *req.MaxExecutionSeconds < 0 || *req.MaxExecutionSeconds > 7*86400 {
			respondBadRequest(c, "任务最大执行秒数必须在 0-604800 (7 天) 之间，0=使用全局兜底")
			return
		}
		p.MaxExecutionSeconds = *req.MaxExecutionSeconds
	}
	if req.MaxRetries != nil {
		p.MaxRetries = *req.MaxRetries
	}
	if req.RetryBaseSeconds != nil {
		p.RetryBaseSeconds = *req.RetryBaseSeconds
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&p).Error; err != nil {
			return err
		}
		// 替换策略-节点关联
		if req.NodeIDs != nil {
			// 验证所有节点 ID 存在
			if len(req.NodeIDs) > 0 {
				var existCount int64
				if err := tx.Model(&model.Node{}).Where("id IN ?", req.NodeIDs).Count(&existCount).Error; err != nil {
					return err
				}
				if int(existCount) != len(req.NodeIDs) {
					return fmt.Errorf("部分节点不存在，请检查节点列表")
				}
			}
			if err := tx.Where("policy_id = ?", p.ID).Delete(&model.PolicyNode{}).Error; err != nil {
				return err
			}
			for _, nid := range req.NodeIDs {
				pn := model.PolicyNode{PolicyID: p.ID, NodeID: nid}
				if err := tx.Create(&pn).Error; err != nil {
					return err
				}
			}
			// 模板策略不生成任务
			if h.runner != nil && !p.IsTemplate {
				if err := policy.SyncPolicyTasks(tx, h.runner, p, req.NodeIDs); err != nil {
					return err
				}
			}
		}
		// 策略从启用变为禁用时，暂停所有关联任务的调度
		if previousEnabled && !p.Enabled && h.runner != nil {
			if err := policy.PauseTasksForPolicy(tx, h.runner, p.ID); err != nil {
				return err
			}
		}
		// 策略从禁用变为启用时，恢复所有关联任务的调度
		if !previousEnabled && p.Enabled && h.runner != nil {
			if err := policy.ResumeTasksForPolicy(tx, h.runner, p.ID, p.CronSpec); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	h.db.Preload("Nodes").First(&p, p.ID)
	if oldTargetPath != "" && oldTargetPath != config.BackupRoot {
		// 警告信息走标准信封的 message 字段，避免破坏前端 request() 的自动解包；
		// 旧的 {data, warning} 顶层结构会让 mapPolicy 收到嵌套对象、字段全部 undefined，
		// 进而触发 describeCron(undefined) 崩溃。
		c.JSON(http.StatusOK, Response{
			Code:    http.StatusOK,
			Message: fmt.Sprintf("策略备份目标路径已从 %s 统一为 /backup，旧路径下的备份数据不会自动迁移", oldTargetPath),
			Data:    buildPolicyResponse(p, nil),
		})
		return
	}
	respondOK(c, buildPolicyResponse(p, nil))
}

// Delete godoc
// @Summary      删除备份策略
// @Description  删除指定备份策略及关联的节点绑定
// @Tags         policies
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "策略 ID"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /policies/{id} [delete]
func (h *PolicyHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var p model.Policy
	if err := h.db.First(&p, id).Error; err != nil {
		respondNotFound(c, "策略不存在")
		return
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		// 先将关联任务标记为孤立并移除调度
		if h.runner != nil {
			if err := policy.OrphanTasksForPolicy(tx, h.runner, id); err != nil {
				return err
			}
		}
		// 删除策略-节点关联
		if err := tx.Where("policy_id = ?", id).Delete(&model.PolicyNode{}).Error; err != nil {
			return err
		}
		// 删除策略
		if err := tx.Delete(&model.Policy{}, id).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		respondInternalError(c, err)
		return
	}
	respondMessage(c, "deleted")
}

// TriggerDrill godoc
// @Summary      手动触发恢复演练
// @Description  立即对指定策略执行一次恢复演练（不等 cron）
// @Tags         policies
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "策略 ID"
// @Success      200  {object}  handlers.Response{data=object}
// @Failure      400  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /policies/{id}/drill-trigger [post]
func (h *PolicyHandler) TriggerDrill(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var policy model.Policy
	if err := h.db.Preload("Nodes").First(&policy, id).Error; err != nil {
		respondNotFound(c, "策略不存在")
		return
	}
	if allowed, err := authorizePolicyOwnership(c, h.db, policy); err != nil {
		respondInternalError(c, err)
		return
	} else if !allowed {
		respondForbidden(c, "无权访问该策略")
		return
	}
	if !policy.DrillEnabled {
		respondBadRequest(c, "该策略未启用恢复演练")
		return
	}

	if h.drillTriggerer == nil {
		respondInternalError(c, fmt.Errorf("恢复演练功能不可用"))
		return
	}

	taskRunID, err := h.drillTriggerer.TriggerDrill(policy.ID)
	if err != nil {
		writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
			Action:       "drill.trigger",
			Purpose:      sshutil.PurposeDrill,
			PolicyID:     credentialaudit.PtrUint(policy.ID),
			Outcome:      credentialaudit.OutcomeFailure,
			ErrorMessage: err.Error(),
			Metadata: map[string]any{
				"node_count": len(policy.Nodes),
			},
		})
		respondBadRequest(c, err.Error())
		return
	}
	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:    "drill.trigger",
		Purpose:   sshutil.PurposeDrill,
		PolicyID:  credentialaudit.PtrUint(policy.ID),
		TaskRunID: credentialaudit.PtrUint(taskRunID),
		Outcome:   credentialaudit.OutcomeSuccess,
		Metadata: map[string]any{
			"node_count": len(policy.Nodes),
		},
	})
	respondOK(c, gin.H{"task_run_id": taskRunID, "message": "恢复演练已触发"})
}


type latestDrillSummary struct {
	TaskRunID          uint       `json:"task_run_id"`
	Status             string     `json:"status"`
	FailedStep         string     `json:"failed_step"`
	ConfidenceEligible bool       `json:"confidence_eligible"`
	StartedAt          *time.Time `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at"`
	DurationMs         int64      `json:"duration_ms"`
}

func buildLatestDrillSummary(e model.RestoreDrillEvidence) *latestDrillSummary {
	return &latestDrillSummary{
		TaskRunID:          e.TaskRunID,
		Status:             e.Status,
		FailedStep:         e.FailedStep,
		ConfidenceEligible: e.ConfidenceEligible,
		StartedAt:          e.StartedAt,
		FinishedAt:         e.FinishedAt,
		DurationMs:         e.DurationMs,
	}
}

func (h *PolicyHandler) latestDrillSummary(c *gin.Context, policyID uint) (*latestDrillSummary, error) {
	var evidence model.RestoreDrillEvidence
	err := h.db.WithContext(c.Request.Context()).Where("policy_id = ?", policyID).Order("created_at desc, id desc").First(&evidence).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return buildLatestDrillSummary(evidence), nil
}

func (h *PolicyHandler) latestDrillSummaries(c *gin.Context, policies []model.Policy) (map[uint]*latestDrillSummary, error) {
	result := make(map[uint]*latestDrillSummary, len(policies))
	if len(policies) == 0 {
		return result, nil
	}
	policyIDs := make([]uint, 0, len(policies))
	for _, p := range policies {
		policyIDs = append(policyIDs, p.ID)
	}

	var evidences []model.RestoreDrillEvidence
	if err := h.db.WithContext(c.Request.Context()).Where("policy_id IN ?", policyIDs).Order("policy_id asc, created_at desc, id desc").Find(&evidences).Error; err != nil {
		return nil, err
	}
	for _, evidence := range evidences {
		if _, exists := result[evidence.PolicyID]; exists {
			continue
		}
		result[evidence.PolicyID] = buildLatestDrillSummary(evidence)
	}
	return result, nil
}

// buildPolicyResponse 构建策略响应，避免序列化 Node 中的敏感字段（Password/PrivateKey）。
func buildPolicyResponse(p model.Policy, latestDrill *latestDrillSummary) gin.H {
	nodeIDs := make([]uint, len(p.Nodes))
	for i, n := range p.Nodes {
		nodeIDs[i] = n.ID
	}
	return gin.H{
		"id":                    p.ID,
		"name":                  p.Name,
		"description":           p.Description,
		"source_path":           p.SourcePath,
		"target_path":           p.TargetPath,
		"cron_spec":             p.CronSpec,
		"exclude_rules":         p.ExcludeRules,
		"bwlimit":               p.BwLimit,
		"retention_days":        p.RetentionDays,
		"max_concurrent":        p.MaxConcurrent,
		"enabled":               p.Enabled,
		"verify_enabled":        p.VerifyEnabled,
		"verify_sample_rate":    p.VerifySampleRate,
		"is_template":           p.IsTemplate,
		"pre_hook":              p.PreHook,
		"post_hook":             p.PostHook,
		"hook_timeout_seconds":  p.HookTimeoutSeconds,
		"max_execution_seconds": p.MaxExecutionSeconds,
		"max_retries":           p.MaxRetries,
		"retry_base_seconds":    p.RetryBaseSeconds,
		"bandwidth_schedule":    p.BandwidthSchedule,
		"drill_enabled":         p.DrillEnabled,
		"drill_cron":            p.DrillCron,
		"drill_target_node_id":  p.DrillTargetNodeID,
		"drill_restore_path":    p.DrillRestorePath,
		"drill_pre_verify":      p.DrillPreVerify,
		"drill_verify":          p.DrillVerify,
		"drill_post_verify":     p.DrillPostVerify,
		"drill_auto_cleanup":    p.DrillAutoCleanup,
		"app_profile":           p.AppProfile,
		"app_credential_id":     p.AppCredentialID,
		"escalation_policy_id":  p.EscalationPolicyID,
		"rpo_minutes":           p.RPOMinutes,
		"rto_minutes":           p.RTOMinutes,
		"retention_mode":        p.RetentionMode,
		"keep_daily":            p.KeepDaily,
		"keep_weekly":           p.KeepWeekly,
		"keep_monthly":          p.KeepMonthly,
		"keep_yearly":           p.KeepYearly,
		"node_ids":              nodeIDs,
		"latest_drill":          latestDrill,
		"created_at":            p.CreatedAt,
		"updated_at":            p.UpdatedAt,
	}
}

// BatchToggle 批量启用/停用策略。
// @Summary      批量启用/停用策略
// @Description  批量启用或停用多个备份策略
// @Tags         policies
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      object  true  "policy_ids 数组 + enabled 布尔值"
// @Success      200   {object}  handlers.Response
// @Failure      400   {object}  handlers.Response
// @Failure      401   {object}  handlers.Response
// @Router       /policies/batch-toggle [post]
func (h *PolicyHandler) BatchToggle(c *gin.Context) {
	var req struct {
		PolicyIDs []uint `json:"policy_ids" binding:"required,min=1"`
		Enabled   bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		for _, pid := range req.PolicyIDs {
			var p model.Policy
			if err := tx.First(&p, pid).Error; err != nil {
				return fmt.Errorf("策略 %d 不存在", pid)
			}
			previousEnabled := p.Enabled
			p.Enabled = req.Enabled
			if err := tx.Save(&p).Error; err != nil {
				return err
			}
			if h.runner != nil {
				if previousEnabled && !req.Enabled {
					if err := policy.PauseTasksForPolicy(tx, h.runner, pid); err != nil {
						return err
					}
				}
				if !previousEnabled && req.Enabled {
					if err := policy.ResumeTasksForPolicy(tx, h.runner, pid, p.CronSpec); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	respondOK(c, gin.H{"count": len(req.PolicyIDs)})
}

// CloneFromTemplate 从模板策略克隆一个新策略。
// @Summary      从模板克隆策略
// @Description  从指定模板策略克隆一个新的备份策略
// @Tags         policies
// @Security     Bearer
// @Produce      json
// @Param        id   path      int  true  "模板策略 ID"
// @Success      201  {object}  handlers.Response{data=object}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      404  {object}  handlers.Response
// @Router       /policies/from-template/{id} [post]
func (h *PolicyHandler) CloneFromTemplate(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	// Delegate to PolicyService for business logic.
	newPolicy, err := h.service().CloneFromTemplate(c.Request.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, policy.ErrTemplateNotFound):
			respondNotFound(c, err.Error())
		case errors.Is(err, policy.ErrNotTemplate):
			respondBadRequest(c, err.Error())
		default:
			respondBadRequest(c, err.Error())
		}
		return
	}

	respondCreated(c, buildPolicyResponse(*newPolicy, nil))
}
