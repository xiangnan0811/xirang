package uptime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMonitorResultConfigCommitRacePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN required")
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_MONITOR_RACE_ENCRYPTION_KEY_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("monitor_race_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		db, _ := base.DB()
		if db != nil {
			_ = db.Close()
		}
	})
	if strings.Contains(dsn, "://") {
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		dsn += separator + "search_path=" + schema
	} else {
		dsn += " search_path=" + schema
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.ServiceMonitor{}, &model.ServiceUptimeSample{}); err != nil {
		t.Fatal(err)
	}
	monitor := model.ServiceMonitor{Name: "config-race", Type: "http", Target: "http://127.0.0.1/old", Enabled: true, HTTPHeaders: "{}"}
	if err := db.Create(&monitor).Error; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	readSnapshot, release := make(chan struct{}), make(chan struct{})
	releaseSnapshot := sync.OnceFunc(func() { close(release) })
	defer releaseSnapshot()
	if err := db.Callback().Query().After("gorm:query").Register("monitor_config_snapshot_barrier", func(tx *gorm.DB) {
		if tx.Statement.Table == "service_monitors" {
			close(readSnapshot)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("monitor_config_snapshot_barrier") })
	resultDone := make(chan error, 1)
	prober := NewProber(db, time.Minute)
	go func() { _, err := prober.persistResult(ctx, monitor, time.Now().UTC(), false); resultDone <- err }()
	select {
	case <-readSnapshot:
	case <-ctx.Done():
		t.Fatal("probe did not read configuration")
	}

	pidReady := make(chan int, 1)
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
			var pid int
			if err := conn.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			pidReady <- pid
			return conn.Table("service_monitors").Where("id = ?", monitor.ID).Update("target", "http://127.0.0.1/new").Error
		})
	}()
	var pid int
	select {
	case pid = <-pidReady:
	case <-ctx.Done():
		t.Fatal("configuration writer did not connect")
	}
	updateCommitted := false
	var updateErr error
	for {
		select {
		case updateErr = <-updateDone:
			updateCommitted = true
		default:
		}
		if updateCommitted {
			break
		}
		var waiting bool
		if err := base.WithContext(ctx).Raw("SELECT COALESCE(wait_event_type = 'Lock',false) FROM pg_stat_activity WHERE pid = ?", pid).Scan(&waiting).Error; err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("writer neither committed nor reached row-lock wait")
		case <-time.After(time.Millisecond):
		}
	}
	// A writer that already committed makes the old probe stale. Otherwise the
	// row lock must order the old result before the new configuration commit.
	releaseSnapshot()
	var resultErr error
	select {
	case resultErr = <-resultDone:
	case <-ctx.Done():
		t.Fatal("probe transaction did not finish")
	}
	if !updateCommitted {
		select {
		case updateErr = <-updateDone:
		case <-ctx.Done():
			t.Fatal("configuration writer did not finish")
		}
	}
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	if updateCommitted && !errors.Is(resultErr, errMonitorStale) {
		t.Fatalf("committed new configuration accepted stale probe: %v", resultErr)
	}
	if !updateCommitted && resultErr != nil {
		t.Fatalf("ordered probe result failed: %v", resultErr)
	}
}
