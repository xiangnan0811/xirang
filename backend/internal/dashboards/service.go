package dashboards

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

var jsonMarshal = json.Marshal

// Service is the CRUD + ownership layer for dashboards + panels.
type Service struct {
	db *gorm.DB
}

// NewService returns a Service bound to db.
func NewService(db *gorm.DB) *Service { return &Service{db: db} }

// DashboardInput is the payload accepted by Create/Update.
type DashboardInput struct {
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	TimeRange          string  `json:"time_range"`
	CustomStart        *string `json:"custom_start"`
	CustomEnd          *string `json:"custom_end"`
	AutoRefreshSeconds int     `json:"auto_refresh_seconds"`
}

// PanelInput is the payload accepted by AddPanel/UpdatePanel.
type PanelInput struct {
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

// LayoutItem is a single panel position update.
type LayoutItem struct {
	ID      uint `json:"id"`
	LayoutX int  `json:"layout_x"`
	LayoutY int  `json:"layout_y"`
	LayoutW int  `json:"layout_w"`
	LayoutH int  `json:"layout_h"`
}

func (s *Service) List(ctx context.Context, userID uint) ([]model.Dashboard, error) {
	var out []model.Dashboard
	if err := s.db.WithContext(ctx).Where("owner_id = ?", userID).Order("updated_at DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, userID, id uint) (*model.Dashboard, error) {
	var d model.Dashboard
	if err := s.db.WithContext(ctx).Preload("Panels").Where("id = ? AND owner_id = ?", id, userID).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &d, nil
}

func (s *Service) Create(ctx context.Context, userID uint, in DashboardInput) (*model.Dashboard, error) {
	if err := validateDashboardInput(&in); err != nil {
		return nil, err
	}
	d := model.Dashboard{
		OwnerID:            userID,
		Name:               strings.TrimSpace(in.Name),
		Description:        in.Description,
		TimeRange:          in.TimeRange,
		AutoRefreshSeconds: in.AutoRefreshSeconds,
	}
	if err := s.db.WithContext(ctx).Create(&d).Error; err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return &d, nil
}

func (s *Service) Update(ctx context.Context, userID, id uint, in DashboardInput) (*model.Dashboard, error) {
	if err := validateDashboardInput(&in); err != nil {
		return nil, err
	}
	d, err := s.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	updates := map[string]any{
		"name":                 strings.TrimSpace(in.Name),
		"description":          in.Description,
		"time_range":           in.TimeRange,
		"auto_refresh_seconds": in.AutoRefreshSeconds,
		"custom_start":         nil,
		"custom_end":           nil,
	}
	if in.TimeRange == "custom" {
		// Parsing handled at handler layer; values passed through.
	}
	if err := s.db.WithContext(ctx).Model(d).Updates(updates).Error; err != nil {
		if isUniqueConstraintErr(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return s.Get(ctx, userID, id)
}

func (s *Service) Delete(ctx context.Context, userID, id uint) error {
	d, err := s.Get(ctx, userID, id)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("dashboard_id = ?", d.ID).Delete(&model.DashboardPanel{}).Error; err != nil {
			return err
		}
		return tx.Delete(d).Error
	})
}

func (s *Service) AddPanel(ctx context.Context, userID, dashboardID uint, in PanelInput) (*model.DashboardPanel, error) {
	if _, err := s.Get(ctx, userID, dashboardID); err != nil {
		return nil, err
	}
	if err := validatePanelInput(&in); err != nil {
		return nil, err
	}
	filtersJSON, _ := marshalFilters(in.Filters)
	p := model.DashboardPanel{
		DashboardID: dashboardID,
		Title:       strings.TrimSpace(in.Title),
		ChartType:   in.ChartType,
		Metric:      in.Metric,
		Filters:     filtersJSON,
		Aggregation: in.Aggregation,
		LayoutX:     in.LayoutX, LayoutY: in.LayoutY, LayoutW: in.LayoutW, LayoutH: in.LayoutH,
	}
	if err := s.db.WithContext(ctx).Create(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Service) UpdatePanel(ctx context.Context, userID, dashboardID, panelID uint, in PanelInput) (*model.DashboardPanel, error) {
	if _, err := s.Get(ctx, userID, dashboardID); err != nil {
		return nil, err
	}
	if err := validatePanelInput(&in); err != nil {
		return nil, err
	}
	var p model.DashboardPanel
	if err := s.db.WithContext(ctx).Where("id = ? AND dashboard_id = ?", panelID, dashboardID).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	filtersJSON, _ := marshalFilters(in.Filters)
	updates := map[string]any{
		"title": strings.TrimSpace(in.Title), "chart_type": in.ChartType, "metric": in.Metric,
		"filters": filtersJSON, "aggregation": in.Aggregation,
		"layout_x": in.LayoutX, "layout_y": in.LayoutY, "layout_w": in.LayoutW, "layout_h": in.LayoutH,
	}
	if err := s.db.WithContext(ctx).Model(&p).Updates(updates).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Service) DeletePanel(ctx context.Context, userID, dashboardID, panelID uint) error {
	if _, err := s.Get(ctx, userID, dashboardID); err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Where("id = ? AND dashboard_id = ?", panelID, dashboardID).Delete(&model.DashboardPanel{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) UpdateLayout(ctx context.Context, userID, dashboardID uint, items []LayoutItem) error {
	if _, err := s.Get(ctx, userID, dashboardID); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, it := range items {
			if err := tx.Model(&model.DashboardPanel{}).
				Where("id = ? AND dashboard_id = ?", it.ID, dashboardID).
				Updates(map[string]any{
					"layout_x": it.LayoutX, "layout_y": it.LayoutY,
					"layout_w": it.LayoutW, "layout_h": it.LayoutH,
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// helpers

func validateDashboardInput(in *DashboardInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return errors.New("name: 1-100 字符")
	}
	if len(in.Description) > 2000 {
		return errors.New("description: 最多 2000 字符")
	}
	switch in.TimeRange {
	case "1h", "6h", "24h", "7d", "custom":
	default:
		return errors.New("time_range: 仅支持 1h/6h/24h/7d/custom")
	}
	switch in.AutoRefreshSeconds {
	case 0, 10, 30, 60, 300:
	default:
		return errors.New("auto_refresh_seconds: 仅支持 0/10/30/60/300")
	}
	return nil
}

func validatePanelInput(in *PanelInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 100 {
		return errors.New("title: 1-100 字符")
	}
	switch in.ChartType {
	case "line", "area", "bar", "number", "table":
	default:
		return errors.New("chart_type: 仅支持 line/area/bar/number/table")
	}
	desc := DescribeMetric(in.Metric)
	if desc == nil {
		return ErrInvalidMetric
	}
	if !containsString(desc.SupportedAggregations, in.Aggregation) {
		return ErrInvalidAggregation
	}
	if desc.Family == FamilyNode && len(in.Filters.TaskIDs) > 0 {
		return ErrInvalidFilters
	}
	if desc.Family == FamilyTask && len(in.Filters.NodeIDs) > 0 {
		return ErrInvalidFilters
	}
	if in.LayoutW <= 0 || in.LayoutH <= 0 {
		return errors.New("layout_w/layout_h 必须大于 0")
	}
	return nil
}

func marshalFilters(f model.PanelFilters) (string, error) {
	// Keep import low-cost — reuse the domain type and marshal via standard json.
	b, err := jsonMarshal(f)
	if err != nil {
		return "{}", err
	}
	return string(b), nil
}

func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint") || strings.Contains(s, "duplicate key") || strings.Contains(s, "SQLSTATE 23505")
}
