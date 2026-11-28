package repository

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"

	"gorm.io/gorm/clause"
)

func TestRsyncPublicationReservationAuthorityLockPostgres(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("strict pinned source capability is Linux-only")
	}
	for _, scenario := range []string{"reservation_after_release", "cancellation", "live_link_drift", "deadline_expired_while_waiting"} {
		t.Run(scenario, func(t *testing.T) {
			db := openProducerRetentionIntegrationDB(t, "postgres")
			fixture := newRsyncPublicationFixtureWithDB(t, db)
			source := managedRsyncCatalogFixtureFromPublication(t, fixture, nil)
			leaseConfig, err := fixture.service.foundation.LeaseConfig()
			if err != nil {
				t.Fatal(err)
			}
			var clock atomic.Int64
			clock.Store(fixture.now.UnixNano())
			fixture.service.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			var dispatches atomic.Int64
			fixture.strategy.prepare = func(context.Context, provider.PublicationPrepareRequest) (provider.PreparedPublication, error) {
				dispatches.Add(1)
				return provider.PreparedPublication{}, nil
			}
			fixture.strategy.execute = func(context.Context, provider.PreparedPublication, provider.PublicationProgress) (provider.ProviderExecutionResult, error) {
				dispatches.Add(1)
				return provider.ProviderExecutionResult{}, nil
			}
			fixture.strategy.record = func(context.Context, provider.PreparedPublication, provider.ProviderExecutionResult) (provider.ProviderCommit, error) {
				dispatches.Add(1)
				return provider.ProviderCommit{}, nil
			}
			fixture.strategy.reconcile = func(context.Context, provider.PublicationReconcileRequest) (provider.PublicationReconcileResult, error) {
				dispatches.Add(1)
				return provider.PublicationReconcileResult{}, nil
			}
			secondRun := model.TaskRun{
				TaskID: fixture.task.ID, NodeIDSnapshot: fixture.task.NodeID, TriggerType: "manual",
				Status: "running", StartedAt: timePointer(fixture.now), CreatedAt: fixture.now, UpdatedAt: fixture.now,
			}
			if err := db.Create(&secondRun).Error; err != nil {
				t.Fatalf("create second active TaskRun: %v", err)
			}
			pointID, err := deriveRecoveryPointID(fixture.link.ID, secondRun.ID)
			if err != nil {
				t.Fatal(err)
			}
			run := fixture.run()
			run.TaskRunID = secondRun.ID
			run.StartedAt = *secondRun.StartedAt

			holder := db.Begin()
			if holder.Error != nil {
				t.Fatal(holder.Error)
			}
			defer holder.Rollback()
			var holderPID int
			if err := holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error; err != nil {
				t.Fatal(err)
			}
			key := "xirang:recovery-authority-repository:v1:" + fixture.repository.ID
			if err := holder.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
				t.Fatal(err)
			}
			var heldTask model.Task
			if err := holder.Clauses(clause.Locking{Strength: "UPDATE"}).First(&heldTask, fixture.task.ID).Error; err != nil {
				t.Fatalf("lock producing Task as Recovery observation: %v", err)
			}

			type prepareResult struct {
				execution publication.Execution
				err       error
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			finished := make(chan struct{})
			results := make(chan prepareResult, 1)
			consumed := false
			defer func() {
				cancel()
				_ = holder.Rollback().Error
				if !consumed {
					select {
					case result := <-results:
						if result.execution != nil {
							_ = result.execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
						}
					case <-time.After(5 * time.Second):
						t.Error("Prepare did not join after cancellation and gate release")
					}
				}
			}()
			go func() {
				execution, err := fixture.service.Prepare(ctx, run)
				results <- prepareResult{execution: execution, err: err}
				close(finished)
			}()
			waitCtx, stopWait := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopWait()
			waitRsyncRepositoryAuthorityGate(t, waitCtx, db, holderPID, fixture.repository.ID, finished)

			// The holder already owns Task. All other rows that reservation and
			// Recovery share must remain independently lockable while it waits.
			probeCtx, stopProbe := context.WithTimeout(context.Background(), 3*time.Second)
			probe := db.WithContext(probeCtx).Begin()
			if probe.Error != nil {
				stopProbe()
				t.Fatal(probe.Error)
			}
			for _, row := range []struct {
				name  string
				model interface{}
				query string
				arg   interface{}
			}{
				{"repository", &model.BackupRepository{}, "id = ?", fixture.repository.ID},
				{"TaskRun", &model.TaskRun{}, "id = ?", secondRun.ID},
				{"link", &model.TaskRepositoryLink{}, "id = ?", fixture.link.ID},
				{"binding", &model.RepositoryAccessBinding{}, "repository_id = ?", fixture.repository.ID},
				{"Node", &model.Node{}, "id = ?", fixture.task.NodeID},
				{"source point", &model.RecoveryPoint{}, "id = ?", source.point.ID},
			} {
				if err := probe.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where(row.query, row.arg).First(row.model).Error; err != nil {
					_ = probe.Rollback().Error
					stopProbe()
					t.Fatalf("independently lock %s while Prepare waits: %v", row.name, err)
				}
			}
			if err := probe.Rollback().Error; err != nil {
				stopProbe()
				t.Fatal(err)
			}
			stopProbe()

			switch scenario {
			case "cancellation":
				cancel()
			case "deadline_expired_while_waiting":
				clock.Store(fixture.now.Add(leaseConfig.AbsoluteDeadline + time.Second).UnixNano())
				if err := holder.Commit().Error; err != nil {
					t.Fatal(err)
				}
			case "live_link_drift":
				if err := holder.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
					Update("publication_mode", backupasset.PublicationVersionedHardlink).Error; err != nil {
					t.Fatalf("change live link while holding Recovery gate: %v", err)
				}
				if err := holder.Commit().Error; err != nil {
					t.Fatal(err)
				}
			default:
				if err := holder.Commit().Error; err != nil {
					t.Fatal(err)
				}
			}

			var result prepareResult
			select {
			case result = <-results:
				consumed = true
			case <-time.After(5 * time.Second):
				t.Fatal("Prepare did not finish after gate release or cancellation")
			}
			if result.execution != nil {
				defer func() { _ = result.execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
			}
			if scenario != "reservation_after_release" {
				if result.execution != nil {
					t.Fatalf("unexpected execution after %s", scenario)
				}
				if scenario == "cancellation" && !errors.Is(result.err, context.Canceled) {
					t.Fatalf("canceled Prepare error=%v", result.err)
				}
				if scenario == "live_link_drift" && !errors.Is(result.err, backupasset.ErrConflict) {
					t.Fatalf("Prepare accepted live link drift: %v", result.err)
				}
				if scenario == "deadline_expired_while_waiting" && (!errors.Is(result.err, backupasset.ErrInvalidState) || !strings.Contains(result.err.Error(), "point deadline elapsed")) {
					t.Fatalf("Prepare accepted expired original deadline: %v", result.err)
				}
				if dispatches.Load() != 0 {
					t.Fatalf("%s dispatched provider operations: %d", scenario, dispatches.Load())
				}
				var points, leases int64
				if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Count(&points).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&model.RecoveryPointLease{}).Where("recovery_point_id = ?", pointID).Count(&leases).Error; err != nil {
					t.Fatal(err)
				}
				if points != 0 || leases != 0 {
					t.Fatalf("%s left reservation: points=%d leases=%d", scenario, points, leases)
				}
				return
			}
			if result.err != nil || result.execution == nil || result.execution.Attempt() == nil {
				t.Fatalf("Prepare after Recovery gate release: execution=%v err=%v", result.execution, result.err)
			}
			attempt, err := result.execution.Attempt().RsyncTreeAttempt()
			if err != nil {
				t.Fatal(err)
			}
			if attempt.RecoveryPointID != pointID || attempt.RepositoryID != fixture.repository.ID || attempt.TaskRunID != secondRun.ID {
				t.Fatalf("unexpected reservation attempt: %+v", attempt)
			}
			var point model.RecoveryPoint
			if err := db.First(&point, "id = ?", pointID).Error; err != nil {
				t.Fatal(err)
			}
			lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
			if err != nil {
				t.Fatal(err)
			}
			if point.State != string(backupasset.RecoveryPointPreparing) || point.ProducingTaskRunID == nil || *point.ProducingTaskRunID != secondRun.ID || !lineage.PreparedAt.Equal(fixture.now) {
				t.Fatalf("unexpected reserved point=%+v lineage=%+v", point, lineage)
			}
			var lease model.RecoveryPointLease
			if err := db.Where("recovery_point_id = ? AND status = ?", pointID, backupasset.LeaseActive).First(&lease).Error; err != nil {
				t.Fatal(err)
			}
			if lease.HolderType != string(backupasset.LeaseHolderPointPublication) || !lease.AbsoluteDeadline.Equal(lineage.PointDeadlineAt) {
				t.Fatalf("unexpected publication lease: %+v", lease)
			}
		})
	}
}
