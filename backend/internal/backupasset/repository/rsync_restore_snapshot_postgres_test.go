package repository

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func newPostgresRsyncRestoreResolverFixture(t *testing.T) *rsyncRestoreResolverFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	db := openProducerRetentionIntegrationDB(t, "postgres")
	return rsyncRestoreResolverFixtureFromPublication(t, newRsyncPublicationFixtureWithDB(t, db), false)
}

func TestRsyncRestorePostgresSnapshotsDoNotBlockOnUncommittedWriter(t *testing.T) {
	tests := []struct {
		name              string
		write             func(*gorm.DB, *rsyncRestoreResolverFixture) *gorm.DB
		invalidatesSource bool
	}{
		{
			name: "binding revocation",
			write: func(tx *gorm.DB, fixture *rsyncRestoreResolverFixture) *gorm.DB {
				return tx.Model(&model.RepositoryAccessBinding{}).Where("repository_id = ? AND status = ?", fixture.ref.RepositoryID, bindingStatusActive).
					Updates(map[string]any{"status": bindingStatusRevoked, "revoked_at": time.Now().UTC()})
			},
			invalidatesSource: true,
		},
		{
			name: "point provenance",
			write: func(tx *gorm.DB, fixture *rsyncRestoreResolverFixture) *gorm.DB {
				return tx.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ref.RecoveryPointID).
					Update("producing_task_name_snapshot", "changed task display name")
			},
		},
		{
			name: "producer archived",
			write: func(tx *gorm.DB, fixture *rsyncRestoreResolverFixture) *gorm.DB {
				return tx.Model(&model.Task{}).Where("id = (SELECT producing_task_id FROM recovery_points WHERE id = ?)", fixture.ref.RecoveryPointID).
					Update("archived_at", time.Now().UTC())
			},
			invalidatesSource: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPostgresRsyncRestoreResolverFixture(t)
			baseline, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(context.Background(), fixture.ref)
			if err != nil {
				t.Fatalf("capture baseline authority: %v", err)
			}
			writer := fixture.db.Begin()
			if writer.Error != nil {
				t.Fatal(writer.Error)
			}
			defer writer.Rollback()
			if result := test.write(writer, fixture); result.Error != nil || result.RowsAffected != 1 {
				t.Fatalf("hold changed row: rows=%d err=%v", result.RowsAffected, result.Error)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			source, err := fixture.service.ResolveRsyncRestoreSource(ctx, fixture.ref)
			if err != nil {
				t.Fatalf("resolve while writer is uncommitted: %v", err)
			}
			defer func() { _ = source.Close() }()
			if err := source.Revalidate(ctx); err != nil {
				t.Fatalf("revalidate source while writer is uncommitted: %v", err)
			}
			current, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(ctx, fixture.ref)
			if err != nil || !reflect.DeepEqual(current, baseline) {
				t.Fatalf("capture previous coherent authority while writer is uncommitted: equal=%t err=%v", reflect.DeepEqual(current, baseline), err)
			}
			if err := fixture.service.revalidateRecoveryRsyncAuthoritySnapshot(ctx, fixture.ref, baseline); err != nil {
				t.Fatalf("revalidate authority while writer is uncommitted: %v", err)
			}
			observed, _, err := observeRecoveryRsyncSourceWithDependencies(ctx,
				provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref},
				recoveryRsyncSourceAuthorityDependencies{
					resolve: fixture.service.ResolveRsyncRestoreSource,
					capture: fixture.service.captureRecoveryRsyncAuthoritySnapshot,
					observe: func(_ context.Context, _ RecoverySourceNamespaceRequest, pinned provider.RsyncRestoreSource) (provider.RsyncRestoreSource, error) {
						return &recoveryAuthorityOwnedSource{pinned: pinned}, nil
					},
					revalidate: fixture.service.revalidateRecoveryRsyncAuthoritySnapshot,
				})
			if err != nil {
				t.Fatalf("observe authority while writer is uncommitted: %v", err)
			}
			if err := observed.Close(); err != nil {
				t.Fatal(err)
			}
			if err := writer.Commit().Error; err != nil {
				t.Fatalf("commit changed proof: %v", err)
			}
			freshCtx, freshCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer freshCancel()
			if err := fixture.service.revalidateRecoveryRsyncAuthoritySnapshot(freshCtx, fixture.ref, baseline); !errors.Is(err, provider.ErrInvalidRestoreRequest) {
				t.Fatalf("revalidate stale authority after commit: %v", err)
			}
			if test.invalidatesSource {
				if err := source.Revalidate(freshCtx); !errors.Is(err, provider.ErrRsyncRestoreSourceDrift) {
					t.Fatalf("revalidate stale source after commit: %v", err)
				}
				fresh, err := fixture.service.ResolveRsyncRestoreSource(freshCtx, fixture.ref)
				if fresh != nil {
					_ = fresh.Close()
				}
				if !errors.Is(err, provider.ErrInvalidRestoreRequest) {
					t.Fatalf("resolve changed source after commit: %v", err)
				}
			}
		})
	}
}

func TestRecoveryRsyncAuthorityPostgresSnapshotRemainsCoherentAcrossCommit(t *testing.T) {
	fixture := newPostgresRsyncRestoreResolverFixture(t)
	baseline, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(context.Background(), fixture.ref)
	if err != nil {
		t.Fatal(err)
	}
	writer := fixture.db.Begin()
	if writer.Error != nil {
		t.Fatal(writer.Error)
	}
	defer writer.Rollback()
	if err := writer.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ref.RecoveryPointID).
		Update("producing_task_name_snapshot", "changed during snapshot").Error; err != nil {
		t.Fatal(err)
	}

	type contextKey struct{}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), contextKey{}, true), 3*time.Second)
	defer cancel()
	planRead := make(chan struct{})
	resume := make(chan struct{})
	var signalOnce, resumeOnce sync.Once
	const callbackName = "test:pause_rsync_authority_after_plan_read"
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.BackupAssetRecoveryPlan{}).TableName() && tx.Statement.Context.Value(contextKey{}) == true {
			signalOnce.Do(func() {
				close(planRead)
				select {
				case <-resume:
				case <-ctx.Done():
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		snapshot recoveryRsyncAuthoritySnapshot
		err      error
	}
	finished := make(chan result, 1)
	complete := false
	defer func() {
		cancel()
		resumeOnce.Do(func() { close(resume) })
		if !complete {
			<-finished
		}
		_ = fixture.db.Callback().Query().Remove(callbackName)
	}()
	go func() {
		snapshot, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(ctx, fixture.ref)
		finished <- result{snapshot: snapshot, err: err}
	}()
	select {
	case <-planRead:
	case <-ctx.Done():
		t.Fatalf("waiting for first snapshot read: %v", ctx.Err())
	}
	if err := writer.Commit().Error; err != nil {
		t.Fatalf("commit changed point during snapshot: %v", err)
	}
	resumeOnce.Do(func() { close(resume) })
	select {
	case got := <-finished:
		complete = true
		if got.err != nil || !reflect.DeepEqual(got.snapshot, baseline) {
			t.Fatalf("in-flight snapshot did not retain previous proof: equal=%t err=%v", reflect.DeepEqual(got.snapshot, baseline), got.err)
		}
	case <-ctx.Done():
		t.Fatalf("waiting for snapshot to finish: %v", ctx.Err())
	}
	fresh, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(context.Background(), fixture.ref)
	if err != nil || fresh.provenanceRevision == baseline.provenanceRevision {
		t.Fatalf("fresh snapshot did not observe changed provenance: %v", err)
	}
	if err := fixture.service.revalidateRecoveryRsyncAuthoritySnapshot(context.Background(), fixture.ref, baseline); !errors.Is(err, provider.ErrInvalidRestoreRequest) {
		t.Fatalf("fresh revalidation accepted previous proof: %v", err)
	}
}

func TestRecoveryRsyncSourceAuthorityPostgresCallerTransactionExcludesWriter(t *testing.T) {
	fixture := newPostgresRsyncRestoreResolverFixture(t)
	snapshot, err := fixture.service.captureRecoveryRsyncAuthoritySnapshot(context.Background(), fixture.ref)
	if err != nil {
		t.Fatal(err)
	}
	request := provider.RecoverySourceAuthorityRequest{Provider: backupasset.ProviderRsync, RsyncRef: fixture.ref}
	expected := recoveryRsyncAuthorityObservation(fixture.ref, snapshot)
	holder := fixture.db.Begin()
	if holder.Error != nil {
		t.Fatal(holder.Error)
	}
	defer holder.Rollback()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := fixture.service.RevalidateRecoverySourceAuthorityTx(ctx, holder, request, expected); err != nil {
		t.Fatalf("revalidate in caller transaction: %v", err)
	}
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer writeCancel()
	err = fixture.db.WithContext(writeCtx).Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ref.RecoveryPointID).
		Update("lineage_json", `{"revision":"blocked"}`).Error
	if err == nil || !errors.Is(writeCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("writer should time out while caller transaction holds locks: write=%v context=%v", err, writeCtx.Err())
	}
	if err := holder.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.WithContext(ctx).Model(&model.RecoveryPoint{}).Where("id = ?", fixture.ref.RecoveryPointID).
		Update("lineage_json", `{"revision":"after-release"}`).Error; err != nil {
		t.Fatalf("write after caller transaction committed: %v", err)
	}
}
