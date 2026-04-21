package dashboards

import (
	"context"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSvcDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Dashboard{}, &model.DashboardPanel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestService_CreateAndGet(t *testing.T) {
	s := NewService(openSvcDB(t))
	d, err := s.Create(context.Background(), 1, DashboardInput{
		Name: "ops", TimeRange: "1h", AutoRefreshSeconds: 30,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.Get(context.Background(), 1, d.ID)
	if err != nil || got.Name != "ops" {
		t.Fatalf("get: %v / %+v", err, got)
	}
}

func TestService_Get_OtherUser_404(t *testing.T) {
	s := NewService(openSvcDB(t))
	d, _ := s.Create(context.Background(), 1, DashboardInput{Name: "a", TimeRange: "1h", AutoRefreshSeconds: 30})
	_, err := s.Get(context.Background(), 2, d.ID)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestService_Create_DuplicateName_Conflict(t *testing.T) {
	s := NewService(openSvcDB(t))
	_, _ = s.Create(context.Background(), 1, DashboardInput{Name: "dup", TimeRange: "1h", AutoRefreshSeconds: 30})
	_, err := s.Create(context.Background(), 1, DashboardInput{Name: "dup", TimeRange: "1h", AutoRefreshSeconds: 30})
	if err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestService_Create_SameNameDifferentUsers_OK(t *testing.T) {
	s := NewService(openSvcDB(t))
	_, err1 := s.Create(context.Background(), 1, DashboardInput{Name: "ok", TimeRange: "1h", AutoRefreshSeconds: 30})
	_, err2 := s.Create(context.Background(), 2, DashboardInput{Name: "ok", TimeRange: "1h", AutoRefreshSeconds: 30})
	if err1 != nil || err2 != nil {
		t.Fatalf("%v / %v", err1, err2)
	}
}

func TestService_Update_OtherUser_404(t *testing.T) {
	s := NewService(openSvcDB(t))
	d, _ := s.Create(context.Background(), 1, DashboardInput{Name: "a", TimeRange: "1h", AutoRefreshSeconds: 30})
	_, err := s.Update(context.Background(), 2, d.ID, DashboardInput{Name: "b", TimeRange: "6h", AutoRefreshSeconds: 60})
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestService_Delete_CascadesPanels(t *testing.T) {
	s := NewService(openSvcDB(t))
	// Register a fake provider so panel input validates
	resetForTest()
	t.Cleanup(resetForTest)
	d, _ := s.Create(context.Background(), 1, DashboardInput{Name: "a", TimeRange: "1h", AutoRefreshSeconds: 30})
	_, err := s.AddPanel(context.Background(), 1, d.ID, PanelInput{
		Title: "cpu", ChartType: "line", Metric: "node.cpu",
		Filters: model.PanelFilters{NodeIDs: []uint{1}}, Aggregation: "avg",
		LayoutX: 0, LayoutY: 0, LayoutW: 6, LayoutH: 4,
	})
	if err != nil {
		t.Fatalf("addpanel: %v", err)
	}
	if err := s.Delete(context.Background(), 1, d.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var count int64
	s.db.Model(&model.DashboardPanel{}).Where("dashboard_id = ?", d.ID).Count(&count)
	if count != 0 {
		t.Fatalf("panels not cascaded: %d remain", count)
	}
}

func TestService_UpdatePanel_OtherUser_404(t *testing.T) {
	s := NewService(openSvcDB(t))
	d, _ := s.Create(context.Background(), 1, DashboardInput{Name: "a", TimeRange: "1h", AutoRefreshSeconds: 30})
	p, _ := s.AddPanel(context.Background(), 1, d.ID, PanelInput{
		Title: "cpu", ChartType: "line", Metric: "node.cpu", Aggregation: "avg",
		Filters: model.PanelFilters{NodeIDs: []uint{1}},
		LayoutX: 0, LayoutY: 0, LayoutW: 6, LayoutH: 4,
	})
	_, err := s.UpdatePanel(context.Background(), 2, d.ID, p.ID, PanelInput{
		Title: "x", ChartType: "line", Metric: "node.cpu", Aggregation: "avg",
		Filters: model.PanelFilters{NodeIDs: []uint{1}},
		LayoutW: 6, LayoutH: 4,
	})
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestService_UpdateLayout_Batch(t *testing.T) {
	s := NewService(openSvcDB(t))
	d, _ := s.Create(context.Background(), 1, DashboardInput{Name: "a", TimeRange: "1h", AutoRefreshSeconds: 30})
	p1, _ := s.AddPanel(context.Background(), 1, d.ID, PanelInput{
		Title: "a", ChartType: "line", Metric: "node.cpu", Aggregation: "avg",
		Filters: model.PanelFilters{NodeIDs: []uint{1}}, LayoutW: 6, LayoutH: 4,
	})
	p2, _ := s.AddPanel(context.Background(), 1, d.ID, PanelInput{
		Title: "b", ChartType: "bar", Metric: "node.memory", Aggregation: "avg",
		Filters: model.PanelFilters{NodeIDs: []uint{1}}, LayoutW: 6, LayoutH: 4,
	})
	err := s.UpdateLayout(context.Background(), 1, d.ID, []LayoutItem{
		{ID: p1.ID, LayoutX: 0, LayoutY: 0, LayoutW: 4, LayoutH: 3},
		{ID: p2.ID, LayoutX: 4, LayoutY: 0, LayoutW: 8, LayoutH: 6},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	var got model.DashboardPanel
	s.db.First(&got, p2.ID)
	if got.LayoutW != 8 || got.LayoutH != 6 || got.LayoutX != 4 {
		t.Fatalf("not updated: %+v", got)
	}
}

func TestService_List_SortedByUpdatedAtDesc(t *testing.T) {
	s := NewService(openSvcDB(t))
	a, _ := s.Create(context.Background(), 1, DashboardInput{Name: "a", TimeRange: "1h", AutoRefreshSeconds: 30})
	b, _ := s.Create(context.Background(), 1, DashboardInput{Name: "b", TimeRange: "1h", AutoRefreshSeconds: 30})
	// Update a to bump its updated_at
	_, _ = s.Update(context.Background(), 1, a.ID, DashboardInput{Name: "a2", TimeRange: "6h", AutoRefreshSeconds: 60})
	list, _ := s.List(context.Background(), 1)
	if len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("order: %+v", list)
	}
}
