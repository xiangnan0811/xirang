package retention

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task"

	"gorm.io/gorm"
)

func TestLifecycleManagedTaskFacadeSelectsOnlyExactHandoffRecoveryPointIDs(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 17, 15, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(800)
	linkID := testOpaqueID(801)
	policyID := testOpaqueID(802)
	taskID := uint(77)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	seedManagedTaskPolicyBinding(t, db, taskID, 91)
	if err := db.Create(&model.TaskRepositoryLink{
		ID: linkID, TaskID: &taskID, RepositoryID: repositoryID,
		TaskNameSnapshot: "managed task", NodeIDSnapshot: 1, NodeNameSnapshot: "node",
		PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed task retention link: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeTaskLink), ScopeID: linkID,
		Revision: 1, RulesJSON: `{"version":1,"count":{"keep_latest":1}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed task retention policy: %v", err)
	}
	handoffIDs := []string{testOpaqueID(803), testOpaqueID(804), testOpaqueID(805)}
	for index, pointID := range handoffIDs {
		capturedAt := now.Add(time.Duration(index) * time.Minute)
		point := newSelectionPoint(pointID, repositoryID, &taskID, capturedAt, 2)
		point.PointRevision = int64(index + 4)
		if err := db.Create(&point).Error; err != nil {
			t.Fatalf("seed handoff point %d: %v", index, err)
		}
	}
	// This valid policy candidate is in the same scope but was not handed off by
	// Task 1's lineage session. The facade must never claim it.
	outsideID := testOpaqueID(806)
	outsideCapturedAt := now.Add(-24 * time.Hour)
	outsidePoint := newSelectionPoint(outsideID, repositoryID, &taskID, outsideCapturedAt, 2)
	outsidePoint.PointRevision = 8
	if err := db.Create(&outsidePoint).Error; err != nil {
		t.Fatalf("seed outside handoff point: %v", err)
	}

	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	nextID := uint64(810)
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }), Now: func() time.Time { return now },
		NewID:        func() (string, error) { nextID++; return testOpaqueID(nextID), nil },
		LeaseOwnerID: "retention-worker-task-facade-test",
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	policies, err := NewPolicyService(PolicyServiceDependencies{DB: db, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	facade, err := NewManagedTaskRetentionFacade(ManagedTaskRetentionFacadeDependencies{
		DB: db, Policies: policies, Coordinator: coordinator, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewManagedTaskRetentionFacade: %v", err)
	}
	if err := facade.EnforceManagedRetention(context.Background(), task.ManagedRecoveryPointRetentionRequest{
		TaskID: taskID, PolicyID: 91, RepositoryID: repositoryID, RecoveryPointIDs: handoffIDs,
	}); err != nil {
		t.Fatalf("EnforceManagedRetention: %v", err)
	}
	var attempts []model.RecoveryPointLifecycleAttempt
	if err := db.Order("recovery_point_id ASC").Find(&attempts).Error; err != nil {
		t.Fatalf("load task retention attempts: %v", err)
	}
	gotIDs := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		gotIDs = append(gotIDs, attempt.RecoveryPointID)
	}
	wantIDs := append([]string(nil), handoffIDs[:2]...)
	sort.Strings(wantIDs)
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("managed task lifecycle attempt point IDs=%v, want exact handoff subset %v", gotIDs, wantIDs)
	}
	var outside model.RecoveryPoint
	if err := db.First(&outside, "id = ?", outsideID).Error; err != nil {
		t.Fatalf("load outside handoff point: %v", err)
	}
	if outside.State != string(backupasset.RecoveryPointCommitted) {
		t.Fatalf("outside handoff point state=%q, want committed", outside.State)
	}
}

func TestLifecycleManagedTaskFacadeRollsBackAllClaimsOnTaskPolicyOrLaterPointDrift(t *testing.T) {
	tests := []struct {
		name            string
		requestPolicyID uint
		failSecondClaim bool
		unlinkBeforeRun bool
	}{
		{name: "current Task policy changed", requestPolicyID: 92},
		{name: "later point claim fails", requestPolicyID: 91, failSecondClaim: true},
		{name: "active Task link changed", requestPolicyID: 91, unlinkBeforeRun: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newLifecycleCoordinatorTestDB(t)
			now := time.Date(2026, 8, 17, 15, 30, 0, 0, time.UTC)
			repositoryID := testOpaqueID(uint64(820 + index*20))
			linkID := testOpaqueID(uint64(821 + index*20))
			policyID := testOpaqueID(uint64(822 + index*20))
			taskID := uint(177 + index)
			seedRetentionUsersAndRepository(t, db, repositoryID)
			seedManagedTaskPolicyBinding(t, db, taskID, 91)
			if err := db.Create(&model.TaskRepositoryLink{
				ID: linkID, TaskID: &taskID, RepositoryID: repositoryID,
				TaskNameSnapshot: "managed task", NodeIDSnapshot: 1, NodeNameSnapshot: "node",
				PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: now, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("seed task retention link: %v", err)
			}
			if err := db.Create(&model.BackupRetentionPolicy{
				ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeTaskLink), ScopeID: linkID,
				Revision: 1, RulesJSON: `{"version":1,"count":{"keep_latest":1}}`,
				Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("seed task retention policy: %v", err)
			}
			pointIDs := []string{
				testOpaqueID(uint64(823 + index*20)),
				testOpaqueID(uint64(824 + index*20)),
				testOpaqueID(uint64(825 + index*20)),
			}
			for pointIndex, pointID := range pointIDs {
				point := newSelectionPoint(pointID, repositoryID, &taskID, now.Add(time.Duration(pointIndex)*time.Minute), 2)
				point.PointRevision = int64(pointIndex + 1)
				if err := db.Create(&point).Error; err != nil {
					t.Fatalf("seed managed point %d: %v", pointIndex, err)
				}
			}
			leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
				Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
			})
			if err != nil {
				t.Fatalf("NewLeaseService: %v", err)
			}
			claimCalls := 0
			coordinator, err := NewCoordinator(CoordinatorDependencies{
				DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }), Now: func() time.Time { return now }, LeaseOwnerID: "retention-worker-atomic-facade-test",
				NewID: func() (string, error) {
					claimCalls++
					if test.failSecondClaim && claimCalls == 2 {
						return "", errors.New("injected later point claim failure")
					}
					return testOpaqueID(uint64(830 + index*20 + claimCalls)), nil
				},
			})
			if err != nil {
				t.Fatalf("NewCoordinator: %v", err)
			}
			policies, err := NewPolicyService(PolicyServiceDependencies{DB: db, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatalf("NewPolicyService: %v", err)
			}
			facade, err := NewManagedTaskRetentionFacade(ManagedTaskRetentionFacadeDependencies{
				DB: db, Policies: policies, Coordinator: coordinator, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatalf("NewManagedTaskRetentionFacade: %v", err)
			}
			if test.unlinkBeforeRun {
				unlinkedAt := now.Add(time.Second)
				if err := db.Model(&model.TaskRepositoryLink{}).Where("id = ?", linkID).
					Updates(map[string]any{"unlinked_at": unlinkedAt, "updated_at": unlinkedAt}).Error; err != nil {
					t.Fatalf("drift active Task link: %v", err)
				}
			}
			err = facade.EnforceManagedRetention(context.Background(), task.ManagedRecoveryPointRetentionRequest{
				TaskID: taskID, PolicyID: test.requestPolicyID, RepositoryID: repositoryID, RecoveryPointIDs: pointIDs,
			})
			if err == nil {
				t.Fatalf("EnforceManagedRetention unexpectedly succeeded")
			}
			var attemptCount int64
			if countErr := db.Model(&model.RecoveryPointLifecycleAttempt{}).Count(&attemptCount).Error; countErr != nil {
				t.Fatalf("count lifecycle attempts: %v", countErr)
			}
			if attemptCount != 0 {
				t.Fatalf("atomic facade persisted %d partial lifecycle attempts", attemptCount)
			}
			for _, pointID := range pointIDs {
				assertLifecyclePointState(t, db, pointID, backupasset.RecoveryPointCommitted)
			}
		})
	}
}

func seedManagedTaskPolicyBinding(t *testing.T, db *gorm.DB, taskID, policyID uint) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS tasks (
		id integer PRIMARY KEY,
		policy_id integer,
		archived_at datetime
	)`).Error; err != nil {
		t.Fatalf("create focused Task binding table: %v", err)
	}
	if err := db.Exec("INSERT INTO tasks (id, policy_id) VALUES (?, ?)", taskID, policyID).Error; err != nil {
		t.Fatalf("seed focused Task policy binding: %v", err)
	}
}
