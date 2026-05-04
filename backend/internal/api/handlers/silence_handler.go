package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// silenceCategoryRE accepts A-Z, 0-9, dot, dash and underscore up to 64
// chars. This is a sanity guard, not a closed whitelist: new detector
// categories (XR-NODE-*, XR-SLO-*, kubernetes.probe.* for future integrations)
// are added without a handler change. Empty string is allowed and means
// "match any category", per MatchSilence's prefix-or-empty semantics in
// silence.go. Dot is included so hierarchical external categories don't
// require a handler change.
var silenceCategoryRE = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)

func validateSilenceCategory(s string) error {
	if s == "" {
		return nil
	}
	if !silenceCategoryRE.MatchString(s) {
		return errors.New("match_category: 仅允许字母/数字/下划线/连字符，长度 1-64")
	}
	return nil
}

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

// silencePatchRequest is the dedicated request type for PATCH /silences/:id.
//
// Match fields (match_node_id, match_category, match_tags) are intentionally
// absent: clients sending them will have them silently ignored by Go's JSON
// unmarshaller (unknown fields are discarded), keeping match criteria immutable
// after creation.
//
// Wave 2 (PR-C C7) 起，starts_at 也被设计为不可在 Patch 中修改：
//   - 之前实现：客户端必须传 starts_at 走 ends_at>starts_at 校验，但该值不写库
//     → 客户端可绕过校验把 ends_at 设到旧 starts_at 之前，导致 silence 被错误
//     "复活"或延长（finding F-5）。
//   - 现在：拒绝请求体中的 starts_at（语义上 "开始时间在创建后冻结"），
//     校验改为用数据库里的 stored starts_at 与新 ends_at 比较。如确需调整起始
//     时间，应删除并重建 silence。
type silencePatchRequest struct {
	Name   string    `json:"name" binding:"required"`
	EndsAt time.Time `json:"ends_at" binding:"required"`
	Note   string    `json:"note"`
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
	if err := validateSilenceCategory(req.MatchCategory); err != nil {
		respondBadRequest(c, err.Error())
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
	respondCreated(c, s)
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

	var req silencePatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	// Wave 2 (PR-C C7) 之前用客户端 starts_at 做"end > start"校验，但 starts_at
	// 不写库——可被绕过把 ends_at 设到 stored starts_at 之前。现在用 stored
	// starts_at 校验，starts_at 不可改。
	if !req.EndsAt.After(s.StartsAt) {
		respondBadRequest(c, "ends_at 必须晚于已创建的 starts_at；如需调整起始时间请删除后重建")
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
	if err := h.DB.First(&s, id).Error; err != nil {
		respondInternalError(c, err)
		return
	}
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
