package middleware

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type auditContextTestKey struct{}

func TestSaveAuditLogWithHashChainContextCancelsWhileContended(t *testing.T) {
	db := openAuditContextTestDB(t)
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("初始化审计表失败: %v", err)
	}

	auditWriteMu.Lock()
	defer auditWriteMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- SaveAuditLogWithHashChain(db.WithContext(ctx), &model.AuditLog{
			UserID: 1, Username: "context-test", Role: "admin", Method: "WS",
			Path: "/api/v1/ws/terminal?action=close", StatusCode: 101,
			CreatedAt: time.Now().UTC(),
		})
	}()

	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("哈希链锁等待应返回 context deadline，实际: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("审计写入在哈希链锁竞争下未按上下文截止")
	}

	var count int64
	if err := db.Model(&model.AuditLog{}).Count(&count).Error; err != nil {
		t.Fatalf("统计审计记录失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("锁等待取消后不应写入审计记录，实际 %d", count)
	}
}

func TestSaveAuditLogWithHashChainContextCancelsBlockedDatabase(t *testing.T) {
	db := openAuditContextTestDB(t)
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("初始化审计表失败: %v", err)
	}

	marker := "blocked-database"
	started := make(chan struct{})
	var startedOnce sync.Once
	callbackName := fmt.Sprintf("test:audit_context_%d", time.Now().UnixNano())
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Context == nil ||
			tx.Statement.Context.Value(auditContextTestKey{}) != marker ||
			tx.Statement.Schema == nil || tx.Statement.Schema.Table != "audit_logs" {
			return
		}
		startedOnce.Do(func() { close(started) })
		<-tx.Statement.Context.Done()
		_ = tx.AddError(tx.Statement.Context.Err())
	}); err != nil {
		t.Fatalf("注册审计阻塞回调失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	parent := context.WithValue(context.Background(), auditContextTestKey{}, marker)
	ctx, cancel := context.WithTimeout(parent, 50*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- SaveAuditLogWithHashChain(db.WithContext(ctx), &model.AuditLog{
			UserID: 1, Username: "context-test", Role: "admin", Method: "WS",
			Path: "/api/v1/ws/terminal?action=close", StatusCode: 101,
			CreatedAt: time.Now().UTC(),
		})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("审计数据库阻塞回调未启动")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("数据库阻塞应返回 context deadline，实际: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("审计写入在数据库阻塞下未按上下文截止")
	}

	var count int64
	if err := db.Model(&model.AuditLog{}).Count(&count).Error; err != nil {
		t.Fatalf("统计审计记录失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("数据库阻塞取消后不应写入审计记录，实际 %d", count)
	}
}

func TestSaveAuditLogWithHashChainPreservesHashChain(t *testing.T) {
	db := openAuditContextTestDB(t)
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("初始化审计表失败: %v", err)
	}

	first := model.AuditLog{
		UserID: 1, Username: "context-test", Role: "admin", Method: "POST",
		Path: "/api/v1/tasks/1/trigger", StatusCode: 202, CreatedAt: time.Now().UTC(),
	}
	if err := SaveAuditLogWithHashChain(db, &first); err != nil {
		t.Fatalf("写入首条审计记录失败: %v", err)
	}
	second := model.AuditLog{
		UserID: 1, Username: "context-test", Role: "admin", Method: "WS",
		Path: "/api/v1/ws/terminal?action=close", StatusCode: 101, CreatedAt: time.Now().UTC(),
	}
	if err := SaveAuditLogWithHashChain(db, &second); err != nil {
		t.Fatalf("写入第二条审计记录失败: %v", err)
	}
	if first.EntryHash == "" || second.PrevHash != first.EntryHash || second.EntryHash == "" {
		t.Fatalf("哈希链未保持，first=%+v second=%+v", first, second)
	}
}

func openAuditContextTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "audit.sqlite")+"?_busy_timeout=1000&_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开审计测试数据库失败: %v", err)
	}
	return db
}
