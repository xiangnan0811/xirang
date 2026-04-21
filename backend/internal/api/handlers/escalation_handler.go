package handlers

import (
	"errors"

	"xirang/backend/internal/escalation"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type EscalationHandler struct{ svc *escalation.Service }

func NewEscalationHandler(db *gorm.DB) *EscalationHandler {
	return &EscalationHandler{svc: escalation.NewService(db)}
}

type escalationPayload struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	MinSeverity string                  `json:"min_severity"`
	Enabled     bool                    `json:"enabled"`
	Levels      []model.EscalationLevel `json:"levels"`
}

func (h *EscalationHandler) List(c *gin.Context) {
	list, err := h.svc.List(c.Request.Context())
	if err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, list)
}

func (h *EscalationHandler) Get(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	p, err := h.svc.Get(c.Request.Context(), id)
	if err != nil {
		mapEscalationErr(c, err)
		return
	}
	respondOK(c, p)
}

func (h *EscalationHandler) Create(c *gin.Context) {
	var req escalationPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	p, err := h.svc.Create(c.Request.Context(), escalation.PolicyInput(req))
	if err != nil {
		mapEscalationErr(c, err)
		return
	}
	respondOK(c, p)
}

func (h *EscalationHandler) Update(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req escalationPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	p, err := h.svc.Update(c.Request.Context(), id, escalation.PolicyInput(req))
	if err != nil {
		mapEscalationErr(c, err)
		return
	}
	respondOK(c, p)
}

func (h *EscalationHandler) Delete(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id); err != nil {
		mapEscalationErr(c, err)
		return
	}
	respondOK(c, gin.H{"deleted": true})
}

func mapEscalationErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, escalation.ErrNotFound):
		respondNotFound(c, "升级策略不存在")
	case errors.Is(err, escalation.ErrConflict):
		respondConflict(c, "升级策略名称已存在")
	case errors.Is(err, escalation.ErrInvalidLevels),
		errors.Is(err, escalation.ErrInvalidSeverity):
		respondBadRequest(c, err.Error())
	default:
		respondBadRequest(c, err.Error())
	}
}
