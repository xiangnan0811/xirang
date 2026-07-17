package task

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
)

func TestManagedRcloneIntegrityBlocksLegacyCheckBeforeSSH(t *testing.T) {
	db := openManagerTestDB(t)
	manager := NewManager(db, stubExecutorFactory{executor: &successExecutor{}}, nil, nil, nil, nil, 8, 90)
	taskEntity := seedTaskForManagerTest(t, db)
	taskEntity.ExecutorType = "rclone"
	taskEntity.RsyncSource = "/srv/source"
	taskEntity.RsyncTarget = "backup:legacy"
	session := &legacyLineageSessionFake{mode: publication.LineageExact}
	guard := &legacyLineageGuardFake{session: session}
	recorder := &legacyBlockRecorderFake{}
	manager.SetLineageGuard(guard)
	manager.SetLegacyBlockRecorder(recorder)

	manager.checkRcloneIntegrity(model.Policy{ID: 24}, taskEntity)

	if guard.calls != 1 || guard.operation != publication.OperationLegacyIntegrity {
		t.Fatalf("guard calls=%d operation=%q", guard.calls, guard.operation)
	}
	if len(recorder.blocks) != 1 || recorder.blocks[0].Operation != publication.OperationLegacyIntegrity {
		t.Fatalf("managed Rclone integrity blocks=%+v", recorder.blocks)
	}
	if got := atomic.LoadInt32(&session.closed); got != 1 {
		t.Fatalf("session close count=%d, want 1", got)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = manager.Shutdown(shutdownCtx)
}
