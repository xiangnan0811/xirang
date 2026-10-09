package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"xirang/backend/internal/cronbackup"
)

func TestCronBackupJobEvidenceIndependentOfArtifacts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	artifacts := t.TempDir()
	t.Setenv("CRON_DB_BACKUP_STATE_DIR", stateDir)
	t.Setenv("CRON_DB_BACKUP_DIR", artifacts)
	t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "")
	cfg, err := cronbackup.LoadConfig("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if err := cronbackup.Initialize(ctx, cfg, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "backup.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'xirang-sqlite-20261007-010203.db\\n' >&3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := cronbackup.Run(ctx, cfg, script, artifacts); err != nil {
		t.Fatal(err)
	}
	handler := NewSystemHandler(newCronBackupSQLiteDB(t))
	got := cronBackupStatusRequest(t, handler, time.Now().UTC())
	if got.Status != cronBackupStatusNoCompleteBackup || got.Job.Status != cronbackup.JobStatusSuccess || got.Job.LastSuccess == nil {
		t.Fatalf("independent evidence: %+v", got)
	}
	priorSuccess := got.Job.LastSuccess.RunID
	writeCronBackupPair(t, artifacts, "sqlite", time.Now().UTC().Add(-time.Minute), "")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := cronbackup.Run(ctx, cfg, script, artifacts); err == nil {
		t.Fatal("expected backup failure")
	}
	stateFile := filepath.Join(stateDir, "sqlite", "state.json")
	before, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	got = cronBackupStatusRequest(t, handler, time.Now().UTC())
	if got.Status != cronBackupStatusFresh || got.Job.Status != cronbackup.JobStatusFailed || got.Job.LastSuccess == nil || got.Job.LastSuccess.RunID != priorSuccess {
		t.Fatalf("failure hidden by old evidence: %+v", got)
	}
	after, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("GET mutated job state")
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"source_id", "revision", "run.lock", "state.lock", stateDir} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("private evidence exposed: %s", secret)
		}
	}
}
func TestCronBackupStatusSamplesClockAfterPublication(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	artifacts := filepath.Join(root, "artifacts")
	t.Setenv("CRON_DB_BACKUP_STATE_DIR", stateDir)
	t.Setenv("CRON_DB_BACKUP_DIR", artifacts)
	t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "26")
	cfg, err := cronbackup.LoadConfig("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-time.Hour)
	if err := cronbackup.Initialize(ctx, cfg, before); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "backup.sh")
	scriptBody := "#!/bin/sh\nset -eu\nmkdir -p \"$1\"\nprintf 'clock snapshot' > \"$1/xirang-sqlite-20261009-020000.db\"\nprintf 'xirang-sqlite-20261009-020000.db\\n' >&3\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o700); err != nil {
		t.Fatal(err)
	}

	handler := NewSystemHandler(newCronBackupSQLiteDB(t))
	after := before.Add(2 * time.Hour)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	handler.now = func() time.Time {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return before
		}
		return after
	}
	result := make(chan struct {
		response CronBackupStatusResponse
		code     int
		err      error
	}, 1)
	go func() {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/system/cron-backup-status", nil)
		handler.CronBackupStatus(c)
		var envelope struct {
			Code int                      `json:"code"`
			Data CronBackupStatusResponse `json:"data"`
		}
		err := json.NewDecoder(recorder.Body).Decode(&envelope)
		result <- struct {
			response CronBackupStatusResponse
			code     int
			err      error
		}{response: envelope.Data, code: envelope.Code, err: err}
	}()
	<-started
	if err := cronbackup.Run(ctx, cfg, script, artifacts); err != nil {
		t.Fatalf("publish successful backup: %v", err)
	}
	close(release)
	resultValue := <-result
	if resultValue.err != nil {
		t.Fatal(resultValue.err)
	}
	if resultValue.code != http.StatusOK {
		t.Fatalf("cron status response code=%d", resultValue.code)
	}
	got := resultValue.response
	if got.Job.Status != cronbackup.JobStatusSuccess || got.Job.LastSuccess == nil {
		t.Fatalf("publication observed with stale clock: %+v", got.Job)
	}
	if !got.Job.CheckedAt.Equal(after) {
		t.Fatalf("job checked_at=%s, want post-publication clock %s", got.Job.CheckedAt, after)
	}
}

func TestCronBackupJobConfigurationAndReadOnlyMissingState(t *testing.T) {
	t.Setenv("CRON_DB_BACKUP_DIR", "")
	t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "")
	t.Setenv("CRON_DB_BACKUP_STATE_DIR", "")
	handler := NewSystemHandler(newCronBackupSQLiteDB(t))
	got := cronBackupStatusRequest(t, handler, time.Now().UTC())
	if got.Job.Evidence != "job_record" || got.Job.Status != cronbackup.JobStatusNotConfigured {
		t.Fatalf("unconfigured job: %+v", got.Job)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("CRON_DB_BACKUP_STATE_DIR", missing)
	got = cronBackupStatusRequest(t, handler, time.Now().UTC())
	if got.Job.Status != cronbackup.JobStatusNotInitialized {
		t.Fatalf("missing state: %+v", got.Job)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("GET created directory: %v", err)
	}
	t.Setenv("CRON_DB_BACKUP_MAX_AGE_HOURS", "0")
	got = cronBackupStatusRequest(t, handler, time.Now().UTC())
	if got.Job.Status != cronbackup.JobStatusInvalidConfiguration || got.Status != cronBackupStatusInvalidConfiguration {
		t.Fatalf("invalid threshold: %+v", got)
	}
}
