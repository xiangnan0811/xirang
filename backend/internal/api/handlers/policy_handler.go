package handlers

import (
	"net/http"
	"strings"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type PolicyHandler struct {
	db *gorm.DB
}

func NewPolicyHandler(db *gorm.DB) *PolicyHandler {
	return &PolicyHandler{db: db}
}

type policyRequest struct {
	Name          string `json:"name" binding:"required"`
	Description   string `json:"description"`
	SourcePath    string `json:"source_path" binding:"required"`
	TargetPath    string `json:"target_path" binding:"required"`
	CronSpec      string `json:"cron_spec" binding:"required"`
	ExcludeRules  string `json:"exclude_rules"`
	BwLimit       int    `json:"bwlimit"`
	RetentionDays int    `json:"retention_days"`
	MaxConcurrent int    `json:"max_concurrent"`
	Enabled       *bool  `json:"enabled"`
}

func (h *PolicyHandler) List(c *gin.Context) {
	var policies []model.Policy
	if err := h.db.Order("id asc").Find(&policies).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": policies})
}

func (h *PolicyHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var policy model.Policy
	if err := h.db.First(&policy, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "策略不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": policy})
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

	policy := model.Policy{
		Name:          req.Name,
		Description:   strings.TrimSpace(req.Description),
		SourcePath:    req.SourcePath,
		TargetPath:    req.TargetPath,
		CronSpec:      req.CronSpec,
		ExcludeRules:  strings.TrimSpace(req.ExcludeRules),
		BwLimit:       req.BwLimit,
		RetentionDays: req.RetentionDays,
		MaxConcurrent: req.MaxConcurrent,
		Enabled:       enabled,
	}
	if err := h.db.Create(&policy).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": policy})
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

	var policy model.Policy
	if err := h.db.First(&policy, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "策略不存在"})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.SourcePath = strings.TrimSpace(req.SourcePath)
	req.TargetPath = strings.TrimSpace(req.TargetPath)
	req.CronSpec = strings.TrimSpace(req.CronSpec)

	if req.Name == "" {
		req.Name = policy.Name
	}
	if req.SourcePath == "" {
		req.SourcePath = policy.SourcePath
	}
	if req.TargetPath == "" {
		req.TargetPath = policy.TargetPath
	}
	if req.CronSpec == "" {
		req.CronSpec = policy.CronSpec
	}

	if err := validateCronSpec(req.CronSpec); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.MaxConcurrent == 0 {
		req.MaxConcurrent = policy.MaxConcurrent
		if req.MaxConcurrent == 0 {
			req.MaxConcurrent = 1
		}
	}
	if req.RetentionDays == 0 {
		req.RetentionDays = policy.RetentionDays
		if req.RetentionDays == 0 {
			req.RetentionDays = 7
		}
	}

	policy.Name = req.Name
	policy.Description = strings.TrimSpace(req.Description)
	policy.SourcePath = req.SourcePath
	policy.TargetPath = req.TargetPath
	policy.CronSpec = req.CronSpec
	policy.ExcludeRules = strings.TrimSpace(req.ExcludeRules)
	policy.BwLimit = req.BwLimit
	policy.RetentionDays = req.RetentionDays
	policy.MaxConcurrent = req.MaxConcurrent
	if req.Enabled != nil {
		policy.Enabled = *req.Enabled
	}

	if err := h.db.Save(&policy).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": policy})
}

func (h *PolicyHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.db.Delete(&model.Policy{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}
