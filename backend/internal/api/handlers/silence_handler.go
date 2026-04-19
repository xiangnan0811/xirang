package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type SilenceHandler struct{ DB *gorm.DB }

func NewSilenceHandler(db *gorm.DB) *SilenceHandler {
	return &SilenceHandler{DB: db}
}

type silenceRequest struct {
	Name          string    `json:"name" binding:"required"`
	MatchNodeID   *uint     `json:"match_node_id"`
	MatchCategory string    `json:"match_category"`
	MatchTags     []string  `json:"match_tags"`
	StartsAt      time.Time `json:"starts_at" binding:"required"`
	EndsAt        time.Time `json:"ends_at" binding:"required"`
	Note          string    `json:"note"`
}

// List 列出静默规则。?active=true 仅返回当前生效的规则。
func (h *SilenceHandler) List(c *gin.Context) {
	q := h.DB.Model(&model.Silence{})
	if c.Query("active") == "true" {
		now := time.Now()
		q = q.Where("starts_at <= ? AND ends_at > ?", now, now)
	}
	var rows []model.Silence
	if err := q.Order("starts_at DESC").Find(&rows).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, rows)
}

// Get 获取单条静默规则。
func (h *SilenceHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var s model.Silence
	if err := h.DB.First(&s, id).Error; err != nil {
		respondNotFound(c, "静默规则不存在")
		return
	}
	respondOK(c, s)
}

// Create 创建静默规则（admin only）。
func (h *SilenceHandler) Create(c *gin.Context) {
	var req silenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if !req.EndsAt.After(req.StartsAt) {
		respondBadRequest(c, "ends_at 必须晚于 starts_at")
		return
	}
	tagsJSON, _ := json.Marshal(req.MatchTags)
	uid := middleware.CurrentUserID(c)
	s := model.Silence{
		Name:          req.Name,
		MatchNodeID:   req.MatchNodeID,
		MatchCategory: req.MatchCategory,
		MatchTags:     string(tagsJSON),
		StartsAt:      req.StartsAt,
		EndsAt:        req.EndsAt,
		Note:          req.Note,
		CreatedBy:     uid,
	}
	if err := h.DB.Create(&s).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, Response{Code: 0, Message: "ok", Data: s})
}

// Patch 更新静默规则（admin only）。
func (h *SilenceHandler) Patch(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	// 先确认记录存在
	var s model.Silence
	if err := h.DB.First(&s, id).Error; err != nil {
		respondNotFound(c, "静默规则不存在")
		return
	}

	var req silenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	updates := map[string]any{
		"name":    req.Name,
		"ends_at": req.EndsAt,
		"note":    req.Note,
	}
	if err := h.DB.Model(&model.Silence{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	h.DB.First(&s, id)
	respondOK(c, s)
}

// Delete 通过将 ends_at 设置为当前时间来软删除静默规则（admin only）。
func (h *SilenceHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.DB.Model(&model.Silence{}).Where("id = ?", id).Update("ends_at", time.Now()).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
