package handlers

import (
	"errors"
	"time"

	"xirang/backend/internal/dashboards"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type DashboardHandler struct {
	svc *dashboards.Service
}

func NewDashboardHandler(db *gorm.DB) *DashboardHandler {
	return &DashboardHandler{svc: dashboards.NewService(db)}
}

type dashboardPayload struct {
	Name               string     `json:"name"`
	Description        string     `json:"description"`
	TimeRange          string     `json:"time_range"`
	CustomStart        *time.Time `json:"custom_start"`
	CustomEnd          *time.Time `json:"custom_end"`
	AutoRefreshSeconds int        `json:"auto_refresh_seconds"`
}

func (h *DashboardHandler) List(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	list, err := h.svc.List(c.Request.Context(), uid)
	if err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, list)
}

func (h *DashboardHandler) Get(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	d, err := h.svc.Get(c.Request.Context(), uid, id)
	if err != nil {
		if errors.Is(err, dashboards.ErrNotFound) {
			respondNotFound(c, "看板不存在")
			return
		}
		respondInternalError(c, err)
		return
	}
	respondOK(c, d)
}

func (h *DashboardHandler) Create(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	var req dashboardPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	d, err := h.svc.Create(c.Request.Context(), uid, dashboards.DashboardInput{
		Name: req.Name, Description: req.Description, TimeRange: req.TimeRange,
		AutoRefreshSeconds: req.AutoRefreshSeconds,
	})
	if err != nil {
		mapServiceErr(c, err)
		return
	}
	respondOK(c, d)
}

func (h *DashboardHandler) Update(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req dashboardPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	d, err := h.svc.Update(c.Request.Context(), uid, id, dashboards.DashboardInput{
		Name: req.Name, Description: req.Description, TimeRange: req.TimeRange,
		AutoRefreshSeconds: req.AutoRefreshSeconds,
	})
	if err != nil {
		mapServiceErr(c, err)
		return
	}
	respondOK(c, d)
}

func (h *DashboardHandler) Delete(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(c.Request.Context(), uid, id); err != nil {
		mapServiceErr(c, err)
		return
	}
	respondOK(c, gin.H{"deleted": true})
}

// Panel operations

type panelPayload struct {
	Title       string             `json:"title"`
	ChartType   string             `json:"chart_type"`
	Metric      string             `json:"metric"`
	Filters     model.PanelFilters `json:"filters"`
	Aggregation string             `json:"aggregation"`
	LayoutX     int                `json:"layout_x"`
	LayoutY     int                `json:"layout_y"`
	LayoutW     int                `json:"layout_w"`
	LayoutH     int                `json:"layout_h"`
}

func (h *DashboardHandler) AddPanel(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req panelPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	p, err := h.svc.AddPanel(c.Request.Context(), uid, id, dashboards.PanelInput(req))
	if err != nil {
		mapServiceErr(c, err)
		return
	}
	respondOK(c, p)
}

func (h *DashboardHandler) UpdatePanel(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	pid, ok := parseID(c, "pid")
	if !ok {
		return
	}
	var req panelPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	p, err := h.svc.UpdatePanel(c.Request.Context(), uid, id, pid, dashboards.PanelInput(req))
	if err != nil {
		mapServiceErr(c, err)
		return
	}
	respondOK(c, p)
}

func (h *DashboardHandler) DeletePanel(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	pid, ok := parseID(c, "pid")
	if !ok {
		return
	}
	if err := h.svc.DeletePanel(c.Request.Context(), uid, id, pid); err != nil {
		mapServiceErr(c, err)
		return
	}
	respondOK(c, gin.H{"deleted": true})
}

type layoutPayload struct {
	Items []dashboards.LayoutItem `json:"items"`
}

func (h *DashboardHandler) UpdateLayout(c *gin.Context) {
	uid := middleware.CurrentUserID(c)
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req layoutPayload
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	if err := h.svc.UpdateLayout(c.Request.Context(), uid, id, req.Items); err != nil {
		mapServiceErr(c, err)
		return
	}
	respondOK(c, gin.H{"updated": len(req.Items)})
}

// mapServiceErr maps dashboards package sentinels to HTTP responses.
func mapServiceErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, dashboards.ErrNotFound):
		respondNotFound(c, "看板不存在")
	case errors.Is(err, dashboards.ErrConflict):
		c.JSON(409, gin.H{"code": 409, "message": "看板名称已存在", "data": nil})
	case errors.Is(err, dashboards.ErrInvalidMetric),
		errors.Is(err, dashboards.ErrInvalidAggregation),
		errors.Is(err, dashboards.ErrInvalidFilters),
		errors.Is(err, dashboards.ErrInvalidTimeRange):
		respondBadRequest(c, err.Error())
	default:
		respondBadRequest(c, err.Error())
	}
}
