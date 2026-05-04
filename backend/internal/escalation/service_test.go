package escalation

import (
	"context"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSvcDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(
		&model.EscalationPolicy{}, &model.Alert{}, &model.Task{}, &model.Policy{},
		&model.SLODefinition{}, &model.Node{}, &model.AlertEscalationEvent{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func basicInput(name string) PolicyInput {
	return PolicyInput{
		Name:        name,
		MinSeverity: "warning",
		Enabled:     true,
		Levels: []model.EscalationLevel{
			{DelaySeconds: 0, IntegrationIDs: []uint{1}},
			{DelaySeconds: 300, IntegrationIDs: []uint{2}, SeverityOverride: "critical"},
		},
	}
}

func TestValidate_FirstLevelMustBeZero(t *testing.T) {
	in := basicInput("x")
	in.Levels[0].DelaySeconds = 30
	if err := ValidatePolicyInput(in); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidate_DelayMustBeStrictlyIncreasing(t *testing.T) {
	in := basicInput("x")
	in.Levels[1].DelaySeconds = 0
	if err := ValidatePolicyInput(in); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidate_LevelsCountBounds(t *testing.T) {
	in := basicInput("x")
	in.Levels = nil
	if err := ValidatePolicyInput(in); err == nil {
		t.Fatal("empty: expected error")
	}
	in = basicInput("x")
	in.Levels = make([]model.EscalationLevel, 6)
	for i := range in.Levels {
		in.Levels[i] = model.EscalationLevel{DelaySeconds: i * 60, IntegrationIDs: []uint{1}}
	}
	if err := ValidatePolicyInput(in); err == nil {
		t.Fatal("6 levels: expected error")
	}
}

func TestValidate_EachLevelNeedsIntegration(t *testing.T) {
	in := basicInput("x")
	in.Levels[1].IntegrationIDs = nil
	if err := ValidatePolicyInput(in); err == nil {
		t.Fatal("empty integrations: expected error")
	}
}

func TestValidate_InvalidSeverityOverride(t *testing.T) {
	in := basicInput("x")
	in.Levels[1].SeverityOverride = "fatal"
	if err := ValidatePolicyInput(in); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidate_InvalidMinSeverity(t *testing.T) {
	in := basicInput("x")
	in.MinSeverity = "meh"
	if err := ValidatePolicyInput(in); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidate_TagLengthBounds(t *testing.T) {
	in := basicInput("x")
	long := ""
	for i := 0; i < 33; i++ {
		long += "a"
	}
	in.Levels[0].Tags = []string{long}
	if err := ValidatePolicyInput(in); err == nil {
		t.Fatal("expected error")
	}
	in = basicInput("x")
	for i := 0; i < 11; i++ {
		in.Levels[0].Tags = append(in.Levels[0].Tags, "t")
	}
	if err := ValidatePolicyInput(in); err == nil {
		t.Fatal("11 tags: expected error")
	}
}

func TestService_CreateAndGet(t *testing.T) {
	s := NewService(openSvcDB(t))
	p, err := s.Create(context.Background(), basicInput("ops"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.Get(context.Background(), p.ID)
	if err != nil || got.Name != "ops" {
		t.Fatalf("get: %v / %+v", err, got)
	}
	if len(got.DecodedLevels()) != 2 {
		t.Fatalf("levels len=%d", len(got.DecodedLevels()))
	}
}

func TestService_Create_DuplicateName_Conflict(t *testing.T) {
	s := NewService(openSvcDB(t))
	_, _ = s.Create(context.Background(), basicInput("dup"))
	_, err := s.Create(context.Background(), basicInput("dup"))
	if err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestService_Update(t *testing.T) {
	s := NewService(openSvcDB(t))
	p, _ := s.Create(context.Background(), basicInput("a"))
	in := basicInput("a")
	in.Enabled = false
	got, err := s.Update(context.Background(), p.ID, in)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Enabled {
		t.Fatal("expected disabled")
	}
}

func TestService_Delete(t *testing.T) {
	s := NewService(openSvcDB(t))
	p, _ := s.Create(context.Background(), basicInput("a"))
	if err := s.Delete(context.Background(), p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(context.Background(), p.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolve_TaskDirectLink(t *testing.T) {
	db := openSvcDB(t)
	s := NewService(db)
	p, _ := s.Create(context.Background(), basicInput("p"))
	db.Create(&model.Task{Name: "t", NodeID: 1, EscalationPolicyID: &p.ID})
	tid := uint(1)
	alert := model.Alert{TaskID: &tid, NodeID: 1}
	got, err := s.ResolvePolicyForAlert(context.Background(), alert)
	if err != nil || got == nil || got.ID != p.ID {
		t.Fatalf("expected p.ID=%d, got err=%v p=%+v", p.ID, err, got)
	}
}

func TestResolve_TaskFallsBackToPolicy(t *testing.T) {
	db := openSvcDB(t)
	s := NewService(db)
	p, _ := s.Create(context.Background(), basicInput("p"))
	// Task has no direct link; its policy does.
	// Policy requires source_path, target_path, cron_spec (NOT NULL).
	polID := uint(77)
	db.Create(&model.Policy{
		ID:                 polID,
		Name:               "bk",
		SourcePath:         "/src",
		TargetPath:         "/dst",
		CronSpec:           "0 * * * *",
		EscalationPolicyID: &p.ID,
	})
	db.Create(&model.Task{ID: 1, Name: "t", NodeID: 1, PolicyID: &polID})
	tid := uint(1)
	alert := model.Alert{TaskID: &tid, NodeID: 1}
	got, _ := s.ResolvePolicyForAlert(context.Background(), alert)
	if got == nil || got.ID != p.ID {
		t.Fatalf("expected fallback to policy link")
	}
}

func TestResolve_NodeFallback(t *testing.T) {
	db := openSvcDB(t)
	s := NewService(db)
	p, _ := s.Create(context.Background(), basicInput("p"))
	db.Create(&model.Node{ID: 5, Name: "n", Host: "h", Username: "u", BackupDir: "/b", EscalationPolicyID: &p.ID})
	alert := model.Alert{NodeID: 5, ErrorCode: "XR-NODE-5"}
	got, _ := s.ResolvePolicyForAlert(context.Background(), alert)
	if got == nil || got.ID != p.ID {
		t.Fatal("expected node fallback")
	}
}

func TestResolve_NoLinkReturnsNil(t *testing.T) {
	s := NewService(openSvcDB(t))
	got, err := s.ResolvePolicyForAlert(context.Background(), model.Alert{NodeID: 99})
	if err != nil || got != nil {
		t.Fatalf("expected nil/nil, got %v / %+v", err, got)
	}
}

func TestSeverityAtLeast(t *testing.T) {
	if !SeverityAtLeast("critical", "warning") {
		t.Fatal("critical >= warning")
	}
	if SeverityAtLeast("info", "warning") {
		t.Fatal("info >= warning should be false")
	}
	if !SeverityAtLeast("warning", "warning") {
		t.Fatal("warning >= warning")
	}
}
