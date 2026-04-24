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
// Match fields (match_node_id, match_category, match_tags) are intentionally
// absent: clients sending them will have them silently ignored by Go's JSON
// unmarshaller (unknown fields are discarded), keeping match criteria immutable
// after creation.
type silencePatchRequest struct {
	Name     string    `json:"name" binding:"required"`
	EndsAt   time.Time `json:"ends_at" binding:"required"`
	StartsAt time.Time `json:"starts_at" binding:"required"` // required for end>start validation
	Note     string    `json:"note"`
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
	if !req.EndsAt.After(req.StartsAt) {
		respondBadRequest(c, "ends_at 必须晚于 starts_at")
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
