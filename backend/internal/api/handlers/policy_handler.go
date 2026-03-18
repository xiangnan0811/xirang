package handlers

import (
	"fmt"
	"net/http"
	"strings"

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
	TargetPath         string `json:"target_path" binding:"required"`
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
	HookTimeoutSeconds *int   `json:"hook_timeout_seconds"`
	MaxRetries         *int   `json:"max_retries"`
	RetryBaseSeconds   *int   `json:"retry_base_seconds"`
	BandwidthSchedule  string `json:"bandwidth_schedule"`
	NodeIDs            []uint `json:"node_ids"`
}

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
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func (h *PolicyHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var p model.Policy
	if err := h.db.Preload("Nodes").First(&p, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "策略不存在"})
		return
	}
	if !checkOwnershipByPolicyNodes(c, h.db, p) {
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该策略"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": buildPolicyResponse(p)})
}

func (h *PolicyHandler) Create(c *gin.Context) {
	var req policyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.SourcePath = strings.TrimSpace(req.SourcePath)
	req.TargetPath = strings.TrimSpace(req.TargetPath)
	req.CronSpec = strings.TrimSpace(req.CronSpec)

	if req.Name == "" || req.SourcePath == "" || req.TargetPath == "" || req.CronSpec == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}
	if err := validateCronSpec(req.CronSpec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 非 admin 不允许设置 hook 命令
	if req.PreHook != "" || req.PostHook != "" {
		role, _ := c.Get("role")
		if roleStr, ok := role.(string); !ok || roleStr != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "仅管理员可配置 hook 命令"})
			return
		}
	}
	if req.PreHook != "" {
		if err := validateHookCommand(req.PreHook); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.PostHook != "" {
		if err := validateHookCommand(req.PostHook); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		Name:             req.Name,
		Description:      strings.TrimSpace(req.Description),
		SourcePath:       req.SourcePath,
		TargetPath:       req.TargetPath,
		CronSpec:         req.CronSpec,
		ExcludeRules:     strings.TrimSpace(req.ExcludeRules),
		BwLimit:          req.BwLimit,
		RetentionDays:    req.RetentionDays,
		MaxConcurrent:    req.MaxConcurrent,
		Enabled:          enabled,
		VerifyEnabled:    verifyEnabled,
		VerifySampleRate: verifySampleRate,
		IsTemplate:       isTemplate,
		PreHook:           strings.TrimSpace(req.PreHook),
		PostHook:          strings.TrimSpace(req.PostHook),
		BandwidthSchedule: strings.TrimSpace(req.BandwidthSchedule),
	}
	if req.HookTimeoutSeconds != nil {
		p.HookTimeoutSeconds = *req.HookTimeoutSeconds
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 重新加载以获取关联节点
	h.db.Preload("Nodes").First(&p, p.ID)
	c.JSON(http.StatusCreated, gin.H{"data": buildPolicyResponse(p)})
}

func (h *PolicyHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req policyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
		return
	}

	var p model.Policy
	if err := h.db.First(&p, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "策略不存在"})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.SourcePath = strings.TrimSpace(req.SourcePath)
	req.TargetPath = strings.TrimSpace(req.TargetPath)
	req.CronSpec = strings.TrimSpace(req.CronSpec)

	if req.Name == "" {
		req.Name = p.Name
	}
	if req.SourcePath == "" {
		req.SourcePath = p.SourcePath
	}
	if req.TargetPath == "" {
		req.TargetPath = p.TargetPath
	}
	if req.CronSpec == "" {
		req.CronSpec = p.CronSpec
	}

	if err := validateCronSpec(req.CronSpec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 非 admin 不允许设置 hook 命令
	if req.PreHook != "" || req.PostHook != "" {
		role, _ := c.Get("role")
		if roleStr, ok := role.(string); !ok || roleStr != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "仅管理员可配置 hook 命令"})
			return
		}
	}
	if req.PreHook != "" {
		if err := validateHookCommand(req.PreHook); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if req.PostHook != "" {
		if err := validateHookCommand(req.PostHook); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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
		p.HookTimeoutSeconds = *req.HookTimeoutSeconds
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.db.Preload("Nodes").First(&p, p.ID)
	c.JSON(http.StatusOK, gin.H{"data": buildPolicyResponse(p)})
}

func (h *PolicyHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var p model.Policy
	if err := h.db.First(&p, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "策略不存在"})
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
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// validateHookCommand 校验 hook 命令的安全性。
func validateHookCommand(cmd string) error {
	if len(cmd) > 2048 {
		return fmt.Errorf("hook 命令长度不能超过 2048 个字符")
	}
	dangerousPatterns := []string{
		"curl ", "wget ", "nc ", "ncat ", "python", "perl ", "ruby ",
		"base64 ", "/dev/tcp", "mkfifo", "telnet ",
	}
	lower := strings.ToLower(cmd)
	for _, p := range dangerousPatterns {
		if strings.Contains(lower, p) {
			return fmt.Errorf("hook 命令包含不允许的模式: %s", strings.TrimSpace(p))
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
		"hook_timeout_seconds": p.HookTimeoutSeconds,
		"max_retries":          p.MaxRetries,
		"retry_base_seconds":   p.RetryBaseSeconds,
		"bandwidth_schedule":   p.BandwidthSchedule,
		"node_ids":             nodeIDs,
		"created_at":           p.CreatedAt,
		"updated_at":           p.UpdatedAt,
	}
}

// BatchToggle 批量启用/停用策略。
// POST /policies/batch-toggle
func (h *PolicyHandler) BatchToggle(c *gin.Context) {
	var req struct {
		PolicyIDs []uint `json:"policy_ids" binding:"required,min=1"`
		Enabled   bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数不合法"})
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ok", "count": len(req.PolicyIDs)})
}

// CloneFromTemplate 从模板策略克隆一个新策略。
// POST /policies/from-template/:id
func (h *PolicyHandler) CloneFromTemplate(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}

	var tmpl model.Policy
	if err := h.db.Preload("Nodes").First(&tmpl, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "模板策略不存在"})
		return
	}
	if !tmpl.IsTemplate {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该策略不是模板"})
		return
	}

	newPolicy := model.Policy{
		Name:             tmpl.Name + " (副本)",
		Description:      tmpl.Description,
		SourcePath:       tmpl.SourcePath,
		TargetPath:       tmpl.TargetPath,
		CronSpec:         tmpl.CronSpec,
		ExcludeRules:     tmpl.ExcludeRules,
		BwLimit:           tmpl.BwLimit,
		BandwidthSchedule: tmpl.BandwidthSchedule,
		RetentionDays:    tmpl.RetentionDays,
		MaxConcurrent:    tmpl.MaxConcurrent,
		Enabled:          false,
		VerifyEnabled:    tmpl.VerifyEnabled,
		VerifySampleRate: tmpl.VerifySampleRate,
		IsTemplate:       false,
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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.db.Preload("Nodes").First(&newPolicy, newPolicy.ID)
	c.JSON(http.StatusCreated, gin.H{"data": buildPolicyResponse(newPolicy)})
}
