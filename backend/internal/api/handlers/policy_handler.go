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
	Name             string `json:"name" binding:"required"`
	Description      string `json:"description"`
	SourcePath       string `json:"source_path" binding:"required"`
	TargetPath       string `json:"target_path" binding:"required"`
	CronSpec         string `json:"cron_spec" binding:"required"`
	ExcludeRules     string `json:"exclude_rules"`
	BwLimit          int    `json:"bwlimit"`
	RetentionDays    int    `json:"retention_days"`
	MaxConcurrent    int    `json:"max_concurrent"`
	Enabled          *bool  `json:"enabled"`
	VerifyEnabled    *bool  `json:"verify_enabled"`
	VerifySampleRate *int   `json:"verify_sample_rate"`
	NodeIDs          []uint `json:"node_ids"`
}

func (h *PolicyHandler) List(c *gin.Context) {
	var policies []model.Policy
	if err := h.db.Preload("Nodes").Order("id asc").Find(&policies).Error; err != nil {
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
			// 同步策略任务
			if h.runner != nil {
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
			// 同步策略任务
			if h.runner != nil {
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

// buildPolicyResponse 构建策略响应，避免序列化 Node 中的敏感字段（Password/PrivateKey）。
func buildPolicyResponse(p model.Policy) gin.H {
	nodeIDs := make([]uint, len(p.Nodes))
	for i, n := range p.Nodes {
		nodeIDs[i] = n.ID
	}
	return gin.H{
		"id":                 p.ID,
		"name":               p.Name,
		"description":        p.Description,
		"source_path":        p.SourcePath,
		"target_path":        p.TargetPath,
		"cron_spec":          p.CronSpec,
		"exclude_rules":      p.ExcludeRules,
		"bwlimit":            p.BwLimit,
		"retention_days":     p.RetentionDays,
		"max_concurrent":     p.MaxConcurrent,
		"enabled":            p.Enabled,
		"verify_enabled":     p.VerifyEnabled,
		"verify_sample_rate": p.VerifySampleRate,
		"node_ids":           nodeIDs,
		"created_at":         p.CreatedAt,
		"updated_at":         p.UpdatedAt,
	}
}
