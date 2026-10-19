package escalation

import (
	"context"
	"errors"
	"testing"

	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestEscalationPreviewResolverPrecedenceAndMissingResources(t *testing.T) {
	db := openEngineDB(t)
	svc := NewService(db)
	policies := make([]*model.EscalationPolicy, 4)
	for i, name := range []string{"task", "policy", "slo", "node"} {
		policies[i] = seedPolicy(t, svc, name, []model.EscalationLevel{{IntegrationIDs: []uint{1}}}, "warning")
	}
	node := model.Node{Name: "resolver-node", Host: "host", Username: "user", BackupDir: "/backup", EscalationPolicyID: &policies[3].ID}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	policy := model.Policy{Name: "backup", SourcePath: "/src", TargetPath: "/dst", CronSpec: "0 * * * *", EscalationPolicyID: &policies[1].ID}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatal(err)
	}
	task := model.Task{Name: "resolver-task", NodeID: node.ID, PolicyID: &policy.ID, EscalationPolicyID: &policies[0].ID}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	slo := model.SLODefinition{Name: "resolver-slo", MetricType: "success_rate", Threshold: 0.9, EscalationPolicyID: &policies[2].ID}
	if err := db.Create(&slo).Error; err != nil {
		t.Fatal(err)
	}
	a := model.Alert{TaskID: &task.ID, SLOID: &slo.ID, NodeID: node.ID}
	for i, source := range []string{"task", "policy", "slo", "node", "none"} {
		got, ref, err := svc.ResolvePolicyForPreview(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		if ref.Source != source {
			t.Fatalf("source=%+v want=%s", ref, source)
		}
		if source == "none" {
			if got != nil || ref.ID != nil || ref.ResourceID != nil {
				t.Fatalf("none=%+v %+v", got, ref)
			}
			break
		}
		resourceID := []uint{task.ID, policy.ID, slo.ID, node.ID}[i]
		if got == nil || got.ID != policies[i].ID || ref.ID == nil || *ref.ID != policies[i].ID || ref.ResourceID == nil || *ref.ResourceID != resourceID {
			t.Fatalf("policy=%+v reference=%+v", got, ref)
		}
		table := []string{"tasks", "policies", "slo_definitions", "nodes"}[i]
		if err := db.Table(table).Where("id = ?", resourceID).Update("escalation_policy_id", nil).Error; err != nil {
			t.Fatal(err)
		}
	}
	missing := uint(999999)
	a.TaskID = &missing
	a.SLOID = &missing
	if err := db.Model(&node).Update("escalation_policy_id", policies[3].ID).Error; err != nil {
		t.Fatal(err)
	}
	got, ref, err := svc.ResolvePolicyForPreview(context.Background(), a)
	if err != nil || got == nil || got.ID != policies[3].ID || ref.Source != "node" {
		t.Fatalf("missing associations must continue: %v %+v %+v", err, got, ref)
	}
}

func TestEscalationPreviewResolverDisabledMissingSelectedAndErrors(t *testing.T) {
	for _, tc := range []string{"disabled", "selected-missing", "task-error", "policy-error", "slo-error", "node-error"} {
		t.Run(tc, func(t *testing.T) {
			db := openEngineDB(t)
			svc := NewService(db)
			high := seedPolicy(t, svc, "high", []model.EscalationLevel{{IntegrationIDs: []uint{1}}}, "warning")
			low := seedPolicy(t, svc, "low", []model.EscalationLevel{{IntegrationIDs: []uint{1}}}, "warning")
			node := model.Node{Name: "n", Host: "h", Username: "u", BackupDir: "/b", EscalationPolicyID: &low.ID}
			if err := db.Create(&node).Error; err != nil {
				t.Fatal(err)
			}
			policy := model.Policy{Name: "p", SourcePath: "/a", TargetPath: "/b", CronSpec: "* * * * *", EscalationPolicyID: &high.ID}
			if err := db.Create(&policy).Error; err != nil {
				t.Fatal(err)
			}
			task := model.Task{Name: "t", NodeID: node.ID, PolicyID: &policy.ID, EscalationPolicyID: &high.ID}
			if err := db.Create(&task).Error; err != nil {
				t.Fatal(err)
			}
			slo := model.SLODefinition{Name: "s", MetricType: "success_rate", Threshold: 0.9}
			if err := db.Create(&slo).Error; err != nil {
				t.Fatal(err)
			}
			a := model.Alert{TaskID: &task.ID, SLOID: &slo.ID, NodeID: node.ID}
			if tc == "disabled" {
				if err := db.Model(high).Update("enabled", false).Error; err != nil {
					t.Fatal(err)
				}
				got, ref, err := svc.ResolvePolicyForPreview(context.Background(), a)
				if err != nil || got == nil || got.Enabled || got.ID != high.ID || ref.Source != "task" {
					t.Fatalf("disabled fallback: %v %+v %+v", err, got, ref)
				}
				return
			}
			if tc == "selected-missing" {
				missing := uint(9999)
				if err := db.Model(&task).Update("escalation_policy_id", missing).Error; err != nil {
					t.Fatal(err)
				}
				got, ref, err := svc.ResolvePolicyForPreview(context.Background(), a)
				if !errors.Is(err, ErrNotFound) || got != nil || ref.Source != "task" {
					t.Fatalf("missing selected fallback: %v %+v %+v", err, got, ref)
				}
				return
			}
			if err := db.Model(&task).Update("escalation_policy_id", nil).Error; err != nil {
				t.Fatal(err)
			}
			if tc == "slo-error" || tc == "node-error" {
				a.TaskID = nil
			}
			if tc == "node-error" {
				a.SLOID = nil
			}
			table := map[string]string{"task-error": "tasks", "policy-error": "policies", "slo-error": "slo_definitions", "node-error": "nodes"}[tc]
			injected := errors.New("FAKE_PREVIEW_DB_FAILURE_FOR_TEST_ONLY")
			observed := false
			name := "test:strict-preview-resolver"
			if err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					observed = true
					_ = tx.AddError(injected)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
			got, _, err := svc.ResolvePolicyForPreview(context.Background(), a)
			if !observed || !errors.Is(err, injected) || got != nil {
				t.Fatalf("strict swallowed DB error: observed=%v got=%+v err=%v", observed, got, err)
			}
			// Runtime intentionally retains its existing ignore-and-fall-back policy.
			got, err = svc.ResolvePolicyForAlert(context.Background(), a)
			if tc != "node-error" && (err != nil || got == nil || got.ID != low.ID) {
				t.Fatalf("runtime compatibility changed: got=%+v err=%v", got, err)
			}
			if tc == "node-error" && (err != nil || got != nil) {
				t.Fatalf("runtime node compatibility: got=%+v err=%v", got, err)
			}
		})
	}
}
