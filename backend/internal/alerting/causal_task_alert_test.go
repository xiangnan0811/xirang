package alerting

import (
	"fmt"
	"testing"
	"time"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func openCausalTaskAlertDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openAlertingTestDB(t)
	if err := db.AutoMigrate(
		&model.Task{}, &model.TaskRun{}, &model.Alert{},
		&model.AlertDelivery{}, &model.Integration{},
	); err != nil {
		t.Fatalf("migrate task alert fixtures: %v", err)
	}
	return db
}

func seedCausalTask(t *testing.T, db *gorm.DB) model.Task {
	t.Helper()
	task := model.Task{Name: "causal-alert-task", NodeID: 1, Status: "success", Enabled: true}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func createCausalRun(t *testing.T, db *gorm.DB, task model.Task, trigger, status string) model.TaskRun {
	t.Helper()
	run := model.TaskRun{
		TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: trigger,
		Status: status, FinishedAt: new(time.Now().UTC()),
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create %s run: %v", trigger, err)
	}
	return run
}

func createCausalAlert(t *testing.T, db *gorm.DB, task model.Task, run model.TaskRun, status, severity, code, message string) model.Alert {
	t.Helper()
	alert := model.Alert{
		NodeID: task.NodeID, NodeName: "node", TaskID: &task.ID, TaskRunID: &run.ID,
		Severity: severity, Status: status, ErrorCode: code, Message: message, TriggeredAt: time.Now().UTC(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create %s alert: %v", status, err)
	}
	return alert
}

func TestRaiseTaskFailureForRunCreatesCurrentAlertAndRecoveryResolvesIt(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "15m")
	db := openCausalTaskAlertDB(t)
	task := seedCausalTask(t, db)
	failure := createCausalRun(t, db, task, "manual", model.TaskRunStatusFailed)
	dispatcher := NewDispatcher(db, nil, nil)
	if err := dispatcher.RaiseTaskFailureForRun(task, failure.ID, "failure"); err != nil {
		t.Fatalf("raise current failure: %v", err)
	}
	if err := dispatcher.RaiseTaskFailureForRun(task, failure.ID, "duplicate failure"); err != nil {
		t.Fatalf("raise duplicate failure: %v", err)
	}
	var alerts []model.Alert
	if err := db.Where("task_id = ?", task.ID).Find(&alerts).Error; err != nil {
		t.Fatalf("load failure alerts: %v", err)
	}
	if len(alerts) != 1 || alerts[0].Status != "open" || alerts[0].TaskRunID == nil || *alerts[0].TaskRunID != failure.ID {
		t.Fatalf("current failure alerts=%+v, want one open alert for run %d", alerts, failure.ID)
	}

	success := createCausalRun(t, db, task, "manual", model.TaskRunStatusSuccess)
	if err := dispatcher.ResolveTaskAlertsForRun(task.ID, success.ID, "recovered"); err != nil {
		t.Fatalf("resolve current failure: %v", err)
	}
	var resolved model.Alert
	if err := db.First(&resolved, alerts[0].ID).Error; err != nil {
		t.Fatalf("reload resolved alert: %v", err)
	}
	if resolved.Status != "resolved" || resolved.Retryable {
		t.Fatalf("resolved alert=%+v, want resolved/non-retryable", resolved)
	}
}

func TestResolveTaskAlertsForRunKeepsAckedNewerFailureOpen(t *testing.T) {
	db := openCausalTaskAlertDB(t)
	task := seedCausalTask(t, db)
	success := createCausalRun(t, db, task, "manual", model.TaskRunStatusSuccess)
	newerFailure := createCausalRun(t, db, task, "manual", model.TaskRunStatusFailed)
	alert := createCausalAlert(t, db, task, newerFailure, "acked", "critical", "XR-EXEC-acked", "newer failure")

	dispatcher := NewDispatcher(db, nil, nil)
	if err := dispatcher.ResolveTaskAlertsForRun(task.ID, success.ID, "recovered"); err != nil {
		t.Fatalf("resolve old success: %v", err)
	}
	var stored model.Alert
	if err := db.First(&stored, alert.ID).Error; err != nil {
		t.Fatalf("reload acked alert: %v", err)
	}
	if stored.Status != "acked" {
		t.Fatalf("newer acked failure status=%q, want acked", stored.Status)
	}
}

func TestRaiseVerificationFailureForRunIgnoresSupersededWarning(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")
	db := openCausalTaskAlertDB(t)
	task := seedCausalTask(t, db)
	warning := createCausalRun(t, db, task, "manual", model.TaskRunStatusWarning)
	_ = createCausalRun(t, db, task, "manual", model.TaskRunStatusSuccess)

	dispatcher := NewDispatcher(db, nil, nil)
	if err := dispatcher.RaiseVerificationFailureForRun(task, warning.ID, "stale verification"); err != nil {
		t.Fatalf("raise stale verification warning: %v", err)
	}
	var count int64
	if err := db.Model(&model.Alert{}).Where("task_id = ?", task.ID).Count(&count).Error; err != nil {
		t.Fatalf("count verification alerts: %v", err)
	}
	if count != 0 {
		t.Fatalf("stale verification warning created %d alert(s)", count)
	}
}

func TestLateIntermediateSuccessLeavesLatestFailureRepresented(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "15m")
	db := openCausalTaskAlertDB(t)
	task := seedCausalTask(t, db)
	firstFailure := createCausalRun(t, db, task, "manual", model.TaskRunStatusFailed)
	dispatcher := NewDispatcher(db, nil, nil)
	if err := dispatcher.RaiseTaskFailureForRun(task, firstFailure.ID, "first failure"); err != nil {
		t.Fatalf("raise first failure: %v", err)
	}
	intermediateSuccess := createCausalRun(t, db, task, "manual", model.TaskRunStatusSuccess)
	latestFailure := createCausalRun(t, db, task, "manual", model.TaskRunStatusFailed)
	if err := dispatcher.RaiseTaskFailureForRun(task, latestFailure.ID, "latest failure"); err != nil {
		t.Fatalf("raise latest failure: %v", err)
	}
	if err := dispatcher.ResolveTaskAlertsForRun(task.ID, intermediateSuccess.ID, "intermediate recovery"); err != nil {
		t.Fatalf("resolve intermediate success: %v", err)
	}
	var alerts []model.Alert
	if err := db.Where("task_id = ?", task.ID).Order("id ASC").Find(&alerts).Error; err != nil {
		t.Fatalf("load ordered alerts: %v", err)
	}
	if len(alerts) != 2 {
		t.Fatalf("alerts=%d, want one alert per distinct failure run", len(alerts))
	}
	if alerts[0].Status != "resolved" || alerts[1].Status != "open" ||
		alerts[0].TaskRunID == nil || *alerts[0].TaskRunID != firstFailure.ID ||
		alerts[1].TaskRunID == nil || *alerts[1].TaskRunID != latestFailure.ID {
		t.Fatalf("late intermediate recovery alerts=%+v, want first resolved/latest open", alerts)
	}
}

func TestResolveTaskAlertsForRunKeepsNewerFailureOpen(t *testing.T) {
	db := openCausalTaskAlertDB(t)
	task := seedCausalTask(t, db)
	success := createCausalRun(t, db, task, "manual", model.TaskRunStatusSuccess)
	newerFailure := createCausalRun(t, db, task, "manual", model.TaskRunStatusFailed)
	alert := model.Alert{
		NodeID: 1, NodeName: "node", TaskID: &task.ID, TaskRunID: &newerFailure.ID,
		Severity: "critical", Status: "open", ErrorCode: "XR-EXEC-causal",
		Message: "newer failure", TriggeredAt: time.Now().UTC(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}

	dispatcher := NewDispatcher(db, nil, nil)
	if err := dispatcher.ResolveTaskAlertsForRun(task.ID, success.ID, "recovered"); err != nil {
		t.Fatalf("resolve old success: %v", err)
	}
	var stored model.Alert
	if err := db.First(&stored, alert.ID).Error; err != nil {
		t.Fatalf("reload alert: %v", err)
	}
	if stored.Status != "open" {
		t.Fatalf("newer failure status=%q, want open", stored.Status)
	}
}

func TestRaiseTaskFailureForRunIgnoresFailureSupersededBySuccess(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")
	db := openCausalTaskAlertDB(t)
	task := seedCausalTask(t, db)
	failure := createCausalRun(t, db, task, "manual", model.TaskRunStatusFailed)
	_ = createCausalRun(t, db, task, "manual", model.TaskRunStatusSuccess)

	dispatcher := NewDispatcher(db, nil, nil)
	if err := dispatcher.RaiseTaskFailureForRun(task, failure.ID, "stale failure"); err != nil {
		t.Fatalf("raise stale failure: %v", err)
	}
	var count int64
	if err := db.Model(&model.Alert{}).Where("task_id = ?", task.ID).Count(&count).Error; err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if count != 0 {
		t.Fatalf("stale failure created %d alert(s)", count)
	}
}

func TestResolveTaskAlertsForRunIgnoresDrillSuccess(t *testing.T) {
	db := openCausalTaskAlertDB(t)
	task := seedCausalTask(t, db)
	failure := createCausalRun(t, db, task, "manual", model.TaskRunStatusFailed)
	drill := createCausalRun(t, db, task, "drill", model.TaskRunStatusSuccess)
	alert := model.Alert{
		NodeID: 1, NodeName: "node", TaskID: &task.ID, TaskRunID: &failure.ID,
		Severity: "critical", Status: "open", ErrorCode: "XR-EXEC-drill",
		Message: "failure", TriggeredAt: time.Now().UTC(),
	}
	if err := db.Create(&alert).Error; err != nil {
		t.Fatalf("create alert: %v", err)
	}
	dispatcher := NewDispatcher(db, nil, nil)
	if err := dispatcher.ResolveTaskAlertsForRun(task.ID, drill.ID, "drill complete"); err != nil {
		t.Fatalf("resolve drill: %v", err)
	}
	var stored model.Alert
	if err := db.First(&stored, alert.ID).Error; err != nil {
		t.Fatalf("reload alert: %v", err)
	}
	if stored.Status != "open" {
		t.Fatalf("ordinary failure status=%q after drill success, want open", stored.Status)
	}
}
func TestCausalAlertReplayIsPermanentAcrossDedupWindowAndStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window string
		status string
	}{
		{name: "zero-window", window: "0", status: "resolved"},
		{name: "expired-window", window: "1ms", status: "acked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ALERT_DEDUP_WINDOW", tc.window)
			db := openCausalTaskAlertDB(t)
			task := seedCausalTask(t, db)
			failure := createCausalRun(t, db, task, "manual", model.TaskRunStatusFailed)
			dispatcher := NewDispatcher(db, nil, nil)

			if err := dispatcher.RaiseTaskFailureForRun(task, failure.ID, "failure"); err != nil {
				t.Fatalf("raise failure: %v", err)
			}
			var first model.Alert
			if err := db.Where("task_id = ? AND task_run_id = ?", task.ID, failure.ID).First(&first).Error; err != nil {
				t.Fatalf("load failure alert: %v", err)
			}
			oldCreatedAt := time.Now().UTC().Add(-time.Hour)
			if err := db.Model(&model.Alert{}).Where("id = ?", first.ID).Updates(map[string]interface{}{
				"status": tc.status, "created_at": oldCreatedAt,
			}).Error; err != nil {
				t.Fatalf("age failure alert: %v", err)
			}
			if err := dispatcher.RaiseTaskFailureForRun(task, failure.ID, "replayed failure"); err != nil {
				t.Fatalf("replay failure: %v", err)
			}

			var alerts []model.Alert
			if err := db.Where("task_id = ?", task.ID).Find(&alerts).Error; err != nil {
				t.Fatalf("load replayed alerts: %v", err)
			}
			if len(alerts) != 1 || alerts[0].Status != tc.status {
				t.Fatalf("replayed failure alerts=%+v, want one %s alert", alerts, tc.status)
			}

			warning := createCausalRun(t, db, task, "manual", model.TaskRunStatusWarning)
			if err := dispatcher.RaiseVerificationFailureForRun(task, warning.ID, "verification warning"); err != nil {
				t.Fatalf("raise verification warning: %v", err)
			}
			var warningAlert model.Alert
			if err := db.Where("task_id = ? AND task_run_id = ? AND error_code = ?", task.ID, warning.ID, "XR-VRFY-"+fmt.Sprint(task.ID)).First(&warningAlert).Error; err != nil {
				t.Fatalf("load verification alert: %v", err)
			}
			if err := db.Model(&model.Alert{}).Where("id = ?", warningAlert.ID).Updates(map[string]interface{}{
				"status": tc.status, "created_at": oldCreatedAt,
			}).Error; err != nil {
				t.Fatalf("age verification alert: %v", err)
			}
			if err := dispatcher.RaiseVerificationFailureForRun(task, warning.ID, "replayed verification warning"); err != nil {
				t.Fatalf("replay verification warning: %v", err)
			}
			if err := db.Where("task_id = ?", task.ID).Find(&alerts).Error; err != nil {
				t.Fatalf("reload replayed alerts: %v", err)
			}
			if len(alerts) != 2 {
				t.Fatalf("replayed verification alerts=%+v, want two total alerts", alerts)
			}
		})
	}
}

func TestRestoreCausalAlertsStaySeparateFromOrdinaryAlerts(t *testing.T) {
	t.Setenv("ALERT_DEDUP_WINDOW", "0")
	db := openCausalTaskAlertDB(t)
	task := seedCausalTask(t, db)
	dispatcher := NewDispatcher(db, nil, nil)

	restoreFailure := createCausalRun(t, db, task, "restore", model.TaskRunStatusFailed)
	if err := dispatcher.RaiseTaskFailureForRestoreRun(task, restoreFailure.ID, "restore failure"); err != nil {
		t.Fatalf("raise restore failure: %v", err)
	}
	ordinaryFailure := createCausalRun(t, db, task, "manual", model.TaskRunStatusFailed)
	if err := dispatcher.RaiseTaskFailureForRun(task, ordinaryFailure.ID, "ordinary failure"); err != nil {
		t.Fatalf("raise ordinary failure: %v", err)
	}
	ordinarySuccess := createCausalRun(t, db, task, "manual", model.TaskRunStatusSuccess)
	if err := dispatcher.ResolveTaskAlertsForRun(task.ID, ordinarySuccess.ID, "ordinary recovery"); err != nil {
		t.Fatalf("resolve ordinary recovery: %v", err)
	}

	var restoreAlert model.Alert
	if err := db.Where("task_id = ? AND task_run_id = ?", task.ID, restoreFailure.ID).First(&restoreAlert).Error; err != nil {
		t.Fatalf("load restore alert after ordinary recovery: %v", err)
	}
	if restoreAlert.Status != "open" {
		t.Fatalf("ordinary success changed restore alert status=%q, want open", restoreAlert.Status)
	}

	ordinaryFailureAfterRestore := createCausalRun(t, db, task, "manual", model.TaskRunStatusFailed)
	if err := dispatcher.RaiseTaskFailureForRun(task, ordinaryFailureAfterRestore.ID, "ordinary failure after restore"); err != nil {
		t.Fatalf("raise later ordinary failure: %v", err)
	}
	restoreWarning := createCausalRun(t, db, task, "restore", model.TaskRunStatusWarning)
	if err := dispatcher.RaiseVerificationFailureForRestoreRun(task, restoreWarning.ID, "restore verification warning"); err != nil {
		t.Fatalf("raise restore verification warning: %v", err)
	}
	if err := dispatcher.RaiseVerificationFailureForRestoreRun(task, restoreWarning.ID, "replayed restore verification warning"); err != nil {
		t.Fatalf("replay restore verification warning: %v", err)
	}
	restoreSuccess := createCausalRun(t, db, task, "restore", model.TaskRunStatusSuccess)
	if err := dispatcher.ResolveTaskAlertsForRestoreRun(task.ID, restoreSuccess.ID, "restore recovery"); err != nil {
		t.Fatalf("resolve restore recovery: %v", err)
	}

	var alerts []model.Alert
	if err := db.Where("task_id = ?", task.ID).Order("id ASC").Find(&alerts).Error; err != nil {
		t.Fatalf("load interleaved alerts: %v", err)
	}
	if len(alerts) != 4 {
		t.Fatalf("interleaved alerts=%d, want four distinct task-run/action alerts", len(alerts))
	}
	if alerts[0].Status != "resolved" || alerts[1].Status != "resolved" ||
		alerts[2].Status != "open" || alerts[3].Status != "resolved" {
		t.Fatalf("interleaved alert statuses=%q,%q,%q,%q, want resolved,resolved,open,resolved",
			alerts[0].Status, alerts[1].Status, alerts[2].Status, alerts[3].Status)
	}
	if alerts[0].TaskRunID == nil || *alerts[0].TaskRunID != restoreFailure.ID ||
		alerts[1].TaskRunID == nil || *alerts[1].TaskRunID != ordinaryFailure.ID ||
		alerts[2].TaskRunID == nil || *alerts[2].TaskRunID != ordinaryFailureAfterRestore.ID ||
		alerts[3].TaskRunID == nil || *alerts[3].TaskRunID != restoreWarning.ID {
		t.Fatalf("interleaved alert run identities=%+v, want archived run identities preserved", alerts)
	}
}
