package task

import (
	"context"
	"strings"
	"testing"

	"xirang/backend/internal/model"
)

func TestTerminalEffectsRespectAggregateOutcome(t *testing.T) {
	for _, tc := range []struct {
		name       string
		taskStatus TaskStatus
		runStatus  TaskStatus
		alerts     int
		automation int64
		downstream int64
	}{
		{"retrying", StatusRetrying, StatusFailed, 0, 0, 0},
		{"final_failure", StatusFailed, StatusFailed, 1, 1, 1},
		{"verification_warning", StatusWarning, StatusWarning, 1, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openManagerTestDB(t)
			taskEntity := seedTaskForManagerTest(t, db)
			policy := model.Policy{Name: "terminal-effect-policy"}
			if err := db.Create(&policy).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.Task{}).Where("id = ?", taskEntity.ID).Updates(map[string]interface{}{"status": string(StatusRunning), "policy_id": policy.ID, "last_error": "previous-attempt-error"}).Error; err != nil {
				t.Fatal(err)
			}
			downstream := model.Task{Name: "terminal-effect-downstream", NodeID: taskEntity.NodeID, DependsOnTaskID: &taskEntity.ID, ExecutorType: "rsync", Status: string(StatusPending), RsyncSource: "/tmp/src", RsyncTarget: "/tmp/dst"}
			if err := db.Create(&downstream).Error; err != nil {
				t.Fatal(err)
			}
			run := model.TaskRun{TaskID: taskEntity.ID, NodeIDSnapshot: taskEntity.NodeID, TriggerType: "manual", Status: model.TaskRunStatusRunning, LastError: "previous-attempt-error"}
			if err := db.Create(&run).Error; err != nil {
				t.Fatal(err)
			}
			manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
			shutdownManagerOnCleanup(t, manager)
			const currentError = "current-attempt-error"
			if err := manager.terminalizeTaskRun(context.Background(), taskEntity.ID, run.ID, nil, &tc.taskStatus, map[string]interface{}{"last_error": currentError}, tc.runStatus, map[string]interface{}{"last_error": currentError}); err != nil {
				t.Fatal(err)
			}
			var alerts []model.Alert
			if err := db.Where("task_id = ?", taskEntity.ID).Find(&alerts).Error; err != nil {
				t.Fatal(err)
			}
			if len(alerts) != tc.alerts {
				t.Fatalf("alerts=%d, want %d", len(alerts), tc.alerts)
			}
			for _, alert := range alerts {
				if !strings.Contains(alert.Message, currentError) || strings.Contains(alert.Message, "previous-attempt-error") {
					t.Fatalf("alert does not describe committed attempt: %q", alert.Message)
				}
			}
			var automationEffects int64
			if err := db.Model(&model.TaskRunEffect{}).Where("task_run_id = ? AND effect_type = ?", run.ID, model.TaskRunEffectTypeAutomation).Count(&automationEffects).Error; err != nil {
				t.Fatal(err)
			}
			if automationEffects != tc.automation {
				t.Fatalf("automation deliveries=%d, want %d", automationEffects, tc.automation)
			}
			var downstreamRuns int64
			if err := db.Model(&model.TaskRun{}).Where("task_id = ?", downstream.ID).Count(&downstreamRuns).Error; err != nil {
				t.Fatal(err)
			}
			if downstreamRuns != tc.downstream {
				t.Fatalf("downstream runs=%d, want %d", downstreamRuns, tc.downstream)
			}
		})
	}
}
