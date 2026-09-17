package verifier

import (
	"context"
	"sync/atomic"
	"testing"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type verifierLineageGuardFake struct {
	session *verifierLineageSessionFake
	calls   int
}

func (guard *verifierLineageGuardFake) Begin(_ context.Context, _ uint, _ publication.ResticOperation) (publication.LineageSession, error) {
	guard.calls++
	return guard.session, nil
}

type verifierLineageSessionFake struct {
	publication.LineageSession
	mode   publication.LineageMode
	closed atomic.Int32
}

func (session *verifierLineageSessionFake) Mode() publication.LineageMode { return session.mode }
func (session *verifierLineageSessionFake) Close() error {
	session.closed.Add(1)
	return nil
}

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?_loc=UTC"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	return db
}

func TestManagedRcloneVerifierBlocksBeforeEmptyTargetShortcutAndSSH(t *testing.T) {
	guard := &verifierLineageGuardFake{session: &verifierLineageSessionFake{mode: publication.LineageExact}}
	result := VerifyWithLineageGuard(context.Background(), model.Task{ID: 7, ExecutorType: "rclone", RsyncSource: "/srv/source"}, 0, setupTestDB(t), func(string, string) {}, false, guard)
	if !result.LegacyBlocked || result.Status != "warning" {
		t.Fatalf("managed Rclone verifier result=%+v", result)
	}
	if guard.calls != 1 || guard.session.closed.Load() != 1 {
		t.Fatalf("managed Rclone verifier guard calls=%d closes=%d", guard.calls, guard.session.closed.Load())
	}
}
