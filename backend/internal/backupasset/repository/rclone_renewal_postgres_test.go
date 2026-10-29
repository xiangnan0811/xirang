package repository

import (
	"context"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestRcloneVersioningRenewalRetainedLifecycleLockOrderPostgres(t *testing.T) {
	for _, lifecycleFirst := range []bool{false, true} {
		name := "renewal_first"
		if lifecycleFirst {
			name = "lifecycle_first"
		}
		t.Run(name, func(t *testing.T) {
			db := openProducerRetentionIntegrationDB(t, "postgres")
			fixture := newRclonePortableVersioningWorkflowFixtureWithDB(t, db)
			task, document := activatePortableForRenewal(t, fixture)
			revision, _ := managedRsyncTaskRevision(task)
			now := document.PreflightExpiresAt.Add(time.Second)
			fixture.service.now = func() time.Time { return now }
			ticket, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix})
			if err != nil {
				t.Fatal(err)
			}
			record, ok := fixture.service.rclonePreflights.get(ticket.PreflightID)
			if !ok {
				t.Fatal("missing renewal record")
			}
			run := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: "manual", Status: "success", StartedAt: timePointer(fixture.now), FinishedAt: timePointer(fixture.now.Add(time.Minute)), CreatedAt: fixture.now, UpdatedAt: fixture.now}
			if err := db.Create(&run).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			renew := func(tx *gorm.DB) error { return fixture.service.renewRcloneBindingTx(ctx, tx, record) }
			lifecycle := func(tx *gorm.DB) error {
				_, err := fixture.service.publication.loadExactManagedRcloneLifecycleRuntimeTx(ctx, tx, record.candidate.plan.repository.ID, task.ID, run.ID, record.candidate.plan.link.ID)
				return err
			}
			first, second := renew, lifecycle
			if lifecycleFirst {
				first, second = lifecycle, renew
			}
			locked, release := make(chan struct{}), make(chan struct{})
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
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
				t.Fatalf("first transaction: %v", err)
			case <-ctx.Done():
				t.Fatal("first lock timed out")
			}
			go func() { secondDone <- db.WithContext(ctx).Transaction(second) }()
			select {
			case err := <-secondDone:
				close(release)
				t.Fatalf("second transaction failed to wait: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			if err := <-firstDone; err != nil {
				t.Fatalf("first transaction: %v", err)
			}
			if err := <-secondDone; err != nil {
				t.Fatalf("second transaction: %v", err)
			}
		})
	}
}
