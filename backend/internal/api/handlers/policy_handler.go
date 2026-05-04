package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"xirang/backend/internal/config"
	"xirang/backend/internal/model"
	"xirang/backend/internal/policy"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type PolicyHandler struct {
	db     *gorm.DB
	runner policy.TaskRunner
}

func NewPolicyHandler(db *gorm.DB, runner policy.TaskRunner) *PolicyHandler {
	return &PolicyHandler{db: db, runner: runner}
}

type policyRequest struct {
	Name               string `json:"name" binding:"required"`
	Description        string `json:"description"`
	SourcePath         string `json:"source_path" binding:"required"`
	TargetPath         string `json:"target_path"`
	CronSpec           string `json:"cron_spec" binding:"required"`
	ExcludeRules       string `json:"exclude_rules"`
	BwLimit            int    `json:"bwlimit"`
	RetentionDays      int    `json:"retention_days"`
	MaxConcurrent      int    `json:"max_concurrent"`
	Enabled            *bool  `json:"enabled"`
	VerifyEnabled      *bool  `json:"verify_enabled"`
	VerifySampleRate   *int   `json:"verify_sample_rate"`
	IsTemplate         *bool  `json:"is_template"`
	PreHook            string `json:"pre_hook"`
	PostHook           string `json:"post_hook"`
	HookTimeoutSeconds  *int   `json:"hook_timeout_seconds"`
	MaxExecutionSeconds *int   `json:"max_execution_seconds"`
	MaxRetries          *int   `json:"max_retries"`
	RetryBaseSeconds   *int   `json:"retry_base_seconds"`
	BandwidthSchedule  string `json:"bandwidth_schedule"`
	NodeIDs            []uint `json:"node_ids"`
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
	result := make([]gin.H, len(policies))
	for i, p := range policies {
		result[i] = buildPolicyResponse(p)
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
	respondOK(c, buildPolicyResponse(p))
}

// Create godoc
// @Summary      创建备份策略
// @Description  创建新的备份策略
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
		if err := validateHookCommand(req.PreHook); err != nil {
			respondBadRequest(c, err.Error())
			return
		}
	}
	if req.PostHook != "" {
		if err := validateHookCommand(req.PostHook); err != nil {
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

	p := model.Policy{
		Name:              req.Name,
		Description:       strings.TrimSpace(req.Description),
		SourcePath:        req.SourcePath,
		TargetPath:        req.TargetPath,
		CronSpec:          req.CronSpec,
		ExcludeRules:      strings.TrimSpace(req.ExcludeRules),
		BwLimit:           req.BwLimit,
		RetentionDays:     req.RetentionDays,
		MaxConcurrent:     req.MaxConcurrent,
		Enabled:           enabled,
		VerifyEnabled:     verifyEnabled,
		VerifySampleRate:  verifySampleRate,
		IsTemplate:        isTemplate,
		PreHook:           strings.TrimSpace(req.PreHook),
		PostHook:          strings.TrimSpace(req.PostHook),
		BandwidthSchedule: strings.TrimSpace(req.BandwidthSchedule),
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
	respondCreated(c, buildPolicyResponse(p))
}

// Update godoc
// @Summary      更新备份策略
// @Description  更新备份策略配置
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
		if err := validateHookCommand(req.PreHook); err != nil {
			respondBadRequest(c, err.Error())
			return
		}
	}
	if req.PostHook != "" {
		if err := validateHookCommand(req.PostHook); err != nil {
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

	previousEnabled := p.Enabled

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
	p.PreHook = strings.TrimSpace(req.PreHook)
	p.PostHook = strings.TrimSpace(req.PostHook)
	p.BandwidthSchedule = strings.TrimSpace(req.BandwidthSchedule)
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
			Code:    0,
			Message: fmt.Sprintf("策略备份目标路径已从 %s 统一为 /backup，旧路径下的备份数据不会自动迁移", oldTargetPath),
			Data:    buildPolicyResponse(p),
		})
		return
	}
	respondOK(c, buildPolicyResponse(p))
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

// validateHookCommand 校验 hook 命令的安全性（白名单：禁止 shell 元字符 + 危险程序名）。
func validateHookCommand(cmd string) error {
	if len(cmd) > 2048 {
		return fmt.Errorf("hook 命令长度不能超过 2048 个字符")
	}
	// 禁止 shell 元字符，防止命令注入
	for _, ch := range []string{";", "|", "&", "`", "$", "(", ")", "{", "}", "<", ">", "!", "\\", "\n", "\r"} {
		if strings.Contains(cmd, ch) {
			return fmt.Errorf("hook 命令包含不允许的字符: %s", ch)
		}
	}
	// 按命令名（basename）阻止已知危险程序
	blocked := map[string]bool{
		"curl": true, "wget": true, "nc": true, "ncat": true,
		"python": true, "python3": true, "perl": true, "ruby": true,
		"bash": true, "sh": true, "zsh": true, "php": true, "node": true,
		"ssh": true, "scp": true, "telnet": true, "base64": true,
	}
	for _, part := range strings.Fields(strings.ToLower(cmd)) {
		base := part
		if idx := strings.LastIndex(part, "/"); idx >= 0 {
			base = part[idx+1:]
		}
		if blocked[base] {
			return fmt.Errorf("hook 命令包含不允许的程序: %s", base)
		}
	}
	return nil
}

// buildPolicyResponse 构建策略响应，避免序列化 Node 中的敏感字段（Password/PrivateKey）。
func buildPolicyResponse(p model.Policy) gin.H {
	nodeIDs := make([]uint, len(p.Nodes))
	for i, n := range p.Nodes {
		nodeIDs[i] = n.ID
	}
	return gin.H{
		"id":                   p.ID,
		"name":                 p.Name,
		"description":          p.Description,
		"source_path":          p.SourcePath,
		"target_path":          p.TargetPath,
		"cron_spec":            p.CronSpec,
		"exclude_rules":        p.ExcludeRules,
		"bwlimit":              p.BwLimit,
		"retention_days":       p.RetentionDays,
		"max_concurrent":       p.MaxConcurrent,
		"enabled":              p.Enabled,
		"verify_enabled":       p.VerifyEnabled,
		"verify_sample_rate":   p.VerifySampleRate,
		"is_template":          p.IsTemplate,
		"pre_hook":             p.PreHook,
		"post_hook":            p.PostHook,
		"hook_timeout_seconds":  p.HookTimeoutSeconds,
		"max_execution_seconds": p.MaxExecutionSeconds,
		"max_retries":           p.MaxRetries,
		"retry_base_seconds":   p.RetryBaseSeconds,
		"bandwidth_schedule":   p.BandwidthSchedule,
		"node_ids":             nodeIDs,
		"created_at":           p.CreatedAt,
		"updated_at":           p.UpdatedAt,
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

	var tmpl model.Policy
	if err := h.db.Preload("Nodes").First(&tmpl, id).Error; err != nil {
		respondNotFound(c, "模板策略不存在")
		return
	}
	if !tmpl.IsTemplate {
		respondBadRequest(c, "该策略不是模板")
		return
	}

	newPolicy := model.Policy{
		Name:              tmpl.Name + " (副本)",
		Description:       tmpl.Description,
		SourcePath:        tmpl.SourcePath,
		TargetPath:        config.BackupRoot,
		CronSpec:          tmpl.CronSpec,
		ExcludeRules:      tmpl.ExcludeRules,
		BwLimit:           tmpl.BwLimit,
		BandwidthSchedule: tmpl.BandwidthSchedule,
		RetentionDays:     tmpl.RetentionDays,
		MaxConcurrent:     tmpl.MaxConcurrent,
		Enabled:           false,
		VerifyEnabled:     tmpl.VerifyEnabled,
		VerifySampleRate:  tmpl.VerifySampleRate,
		IsTemplate:        false,
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&newPolicy).Error; err != nil {
			return err
		}
		// 复制模板的节点关联
		for _, n := range tmpl.Nodes {
			pn := model.PolicyNode{PolicyID: newPolicy.ID, NodeID: n.ID}
			if err := tx.Create(&pn).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		respondBadRequest(c, err.Error())
		return
	}

	h.db.Preload("Nodes").First(&newPolicy, newPolicy.ID)
	respondCreated(c, buildPolicyResponse(newPolicy))
}
