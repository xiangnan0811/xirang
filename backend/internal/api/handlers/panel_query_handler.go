package handlers

import (
	"context"
	"errors"
	"time"

	"xirang/backend/internal/dashboards"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type PanelQueryHandler struct {
	db *gorm.DB
}

func NewPanelQueryHandler(db *gorm.DB) *PanelQueryHandler {
	return &PanelQueryHandler{db: db}
}

type panelQueryPayload struct {
	Metric      string             `json:"metric"`
	Filters     dashboards.Filters `json:"filters"`
	Aggregation string             `json:"aggregation"`
	Start       time.Time          `json:"start"`
	End         time.Time          `json:"end"`
}

func (h *PanelQueryHandler) Query(c *gin.Context) {
	var req panelQueryPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	resp, err := dashboards.Query(c.Request.Context(), h.db, dashboards.QueryRequest{
		Metric: req.Metric, Filters: req.Filters, Aggregation: req.Aggregation,
		Start: req.Start.UTC(), End: req.End.UTC(),
	})
	if err != nil {
		switch {
		case errors.Is(err, dashboards.ErrInvalidMetric),
			errors.Is(err, dashboards.ErrInvalidAggregation),
			errors.Is(err, dashboards.ErrInvalidFilters),
			errors.Is(err, dashboards.ErrInvalidTimeRange):
			respondBadRequest(c, err.Error())
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// Client disconnected (AbortController-driven debounce cancels
			// on every keystroke in the panel editor). Do not log as an
			// error and do not write the gin error to the structured log —
			// just close the response with 499 Client Closed Request so the
			// HTTP layer records the cause without alarming anyone.
			c.AbortWithStatus(499)
		default:
			respondInternalError(c, err)
		}
		return
	}
	respondOK(c, resp)
}

func (h *PanelQueryHandler) ListMetrics(c *gin.Context) {
	respondOK(c, dashboards.ListMetrics())
}
