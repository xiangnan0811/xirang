package handlers

import (
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
