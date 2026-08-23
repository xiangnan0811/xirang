package model

import (
	"path/filepath"
	"reflect"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestTaskRunStatusAndSnapshotContractIsClosed(t *testing.T) {
	active := TaskRunActiveStatuses()
	terminal := TaskRunTerminalStatuses()
	if !reflect.DeepEqual(active, []string{TaskRunStatusPending, TaskRunStatusRunning, TaskRunStatusRetrying}) {
		t.Fatalf("active TaskRun statuses=%v", active)
	}
	if !reflect.DeepEqual(terminal, []string{TaskRunStatusSuccess, TaskRunStatusFailed, TaskRunStatusCanceled, TaskRunStatusWarning, TaskRunStatusSkipped}) {
		t.Fatalf("terminal TaskRun statuses=%v", terminal)
	}
	for _, status := range active {
		if !IsKnownTaskRunStatus(status) || !IsActiveTaskRunStatus(status) || IsTerminalTaskRunStatus(status) {
			t.Fatalf("active TaskRun status %q is not classified exactly once", status)
		}
	}
	for _, status := range terminal {
		if !IsKnownTaskRunStatus(status) || IsActiveTaskRunStatus(status) || !IsTerminalTaskRunStatus(status) {
			t.Fatalf("terminal TaskRun status %q is not classified exactly once", status)
		}
	}
	if IsKnownTaskRunStatus("unknown") {
		t.Fatal("unknown TaskRun status was accepted")
	}
	active[0] = "mutated"
	if TaskRunActiveStatuses()[0] != TaskRunStatusPending {
		t.Fatal("TaskRunActiveStatuses returned mutable package state")
	}
	if IsTaskRunNodeSnapshotAuthoritative(TaskRunNodeIDLegacyUnknown) {
		t.Fatal("legacy_unknown node snapshot became authoritative")
	}
	if !IsTaskRunNodeSnapshotAuthoritative(1) {
		t.Fatal("positive node snapshot was not authoritative")
	}
	field, ok := reflect.TypeOf(TaskRun{}).FieldByName("NodeIDSnapshot")
	if !ok || field.Tag.Get("json") != "-" {
		t.Fatalf("NodeIDSnapshot JSON tag=%q, want hidden", field.Tag.Get("json"))
	}
}

func TestTaskRunGORMCreateRejectsMissingTaskAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "task-run-authority.db")), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Task{}, &TaskRun{}); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name   string
		taskID uint
	}{
		{name: "zero_task", taskID: 0},
		{name: "missing_task", taskID: 999},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			run := TaskRun{TaskID: testCase.taskID, TriggerType: "manual", Status: TaskRunStatusPending}
			if err := db.Create(&run).Error; err == nil {
				t.Fatalf("GORM created TaskRun without authoritative Task: %+v", run)
			}
		})
	}
}

func TestTaskRunGORMCreateFreezesMatchingPositiveTaskNode(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "task-run-snapshot.db")), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Task{}, &TaskRun{}); err != nil {
		t.Fatal(err)
	}

	authoritativeTask := Task{Name: "authoritative", NodeID: 7, ExecutorType: "local", Status: "success"}
	if err := db.Create(&authoritativeTask).Error; err != nil {
		t.Fatal(err)
	}
	run := TaskRun{TaskID: authoritativeTask.ID, TriggerType: "manual", Status: TaskRunStatusPending}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("GORM rejected authoritative TaskRun: %v", err)
	}
	if run.NodeIDSnapshot != authoritativeTask.NodeID {
		t.Fatalf("GORM snapshot=%d, want Task node %d", run.NodeIDSnapshot, authoritativeTask.NodeID)
	}

	mismatched := TaskRun{
		TaskID: authoritativeTask.ID, NodeIDSnapshot: authoritativeTask.NodeID + 1,
		TriggerType: "manual", Status: TaskRunStatusPending,
	}
	if err := db.Create(&mismatched).Error; err == nil {
		t.Fatalf("GORM created mismatched TaskRun: %+v", mismatched)
	}

	nonAuthoritativeTask := Task{Name: "non-authoritative", NodeID: 0, ExecutorType: "local", Status: "success"}
	if err := db.Create(&nonAuthoritativeTask).Error; err != nil {
		t.Fatal(err)
	}
	nonAuthoritativeRun := TaskRun{
		TaskID: nonAuthoritativeTask.ID, TriggerType: "manual", Status: TaskRunStatusPending,
	}
	if err := db.Create(&nonAuthoritativeRun).Error; err == nil {
		t.Fatalf("GORM created TaskRun for non-authoritative Task node: %+v", nonAuthoritativeRun)
	}
}
