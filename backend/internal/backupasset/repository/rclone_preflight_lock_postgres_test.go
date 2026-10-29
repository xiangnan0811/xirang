package repository

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestRclonePreflightFinalizeRetainedLifecycleLockOrderPostgres(t *testing.T) {
	db := openProducerRetentionIntegrationDB(t, "postgres")
	fixture := newRclonePortableVersioningWorkflowFixtureWithDB(t, db)
	_, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: fixture.preflight.PreflightID,
		MigrationChoice: backupasset.RcloneFirstNewPoint,
	})
	if err != nil {
		t.Fatalf("activate managed fixture: %v", err)
	}
	var taskEntity model.Task
	if err := db.First(&taskEntity, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, err := managedRsyncTaskRevision(taskEntity)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.service.loadRcloneBindingWorkflowPlan(context.Background(), taskEntity.ID, revision, rcloneBindingSetupPortable)
	if err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{
		TaskID: taskEntity.ID, NodeIDSnapshot: taskEntity.NodeID, TriggerType: "manual", Status: "success",
		StartedAt: timePointer(fixture.now), FinishedAt: timePointer(fixture.now.Add(time.Minute)),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	for _, lifecycleFirst := range []bool{false, true} {
		name := "finalize_first"
		if lifecycleFirst {
			name = "lifecycle_first"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			locked, release := make(chan struct{}), make(chan struct{})
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			finalize := func(tx *gorm.DB) error { return revalidateRcloneWorkflowPlanTx(ctx, tx, snapshot) }
			lifecycle := func(tx *gorm.DB) error {
				_, err := fixture.service.publication.loadExactManagedRcloneLifecycleRuntimeTx(ctx, tx, snapshot.repository.ID, taskEntity.ID, run.ID, snapshot.link.ID)
				return err
			}
			first, second := finalize, lifecycle
			if lifecycleFirst {
				first, second = lifecycle, finalize
			}
			go func() {
				firstDone <- db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
					if err := first(tx); err != nil {
						return err
					}
					close(locked)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			select {
			case <-locked:
			case err := <-firstDone:
				t.Fatalf("first authority failed before locking: %v", err)
			case <-ctx.Done():
				t.Fatal("first authority timed out")
			}
			go func() { secondDone <- db.WithContext(ctx).Transaction(second) }()
			select {
			case err := <-secondDone:
				close(release)
				t.Fatalf("second authority did not wait on repository lock: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			if err := <-firstDone; err != nil {
				t.Fatalf("first authority failed: %v", err)
			}
			if err := <-secondDone; err != nil {
				t.Fatalf("second authority failed or deadlocked: %v", err)
			}
		})
	}
}
