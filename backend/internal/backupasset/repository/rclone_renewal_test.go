package repository

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func assertRcloneRollbackSummary(t *testing.T, summary backupasset.RclonePublicationSummary, state backupasset.RcloneVersioningState, reason backupasset.RcloneVersioningReasonCode, capability backupasset.RcloneRollbackCapability) {
	t.Helper()
	if summary.State != state || summary.ReasonCode != reason || summary.RollbackCapability != capability {
		t.Fatalf("summary=%+v, want state=%s reason=%s rollback=%s", summary, state, reason, capability)
	}
}

func activatePortableForRenewal(t *testing.T, fixture *rclonePortableVersioningWorkflowFixture) (model.Task, managedRcloneBindingDocumentV3) {
	t.Helper()
	_, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: fixture.preflight.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := fixture.db.First(&task, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	var link model.TaskRepositoryLink
	if err := fixture.db.Where("task_id = ? AND unlinked_at IS NULL", task.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	var binding model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ? AND status = ?", link.RepositoryID, bindingStatusActive).First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	document, err := decodeManagedRcloneBindingDocumentV3(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	return task, document
}

func TestRcloneVersioningLegacyCandidateAndActivatedSummaryRollbackCapabilities(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	var link model.TaskRepositoryLink
	if err := fixture.db.Where("task_id = ? AND unlinked_at IS NULL", fixture.task.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	insertManagedHistoryLatchFixture(t, fixture.db, "repository", link.RepositoryID, fixture.now)
	summary, err := fixture.service.RcloneVersioningSummary(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackCleanAvailable)
	ticket, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, RequestedMode: backupasset.PublicationVersionedPrefix,
	})
	if err != nil || ticket.Purpose != backupasset.RclonePreflightPurposeActivation {
		t.Fatalf("legacy preflight ticket=%+v err=%v", ticket, err)
	}
	assertRcloneRollbackSummary(t, ticket.Summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackCleanAvailable)
	activated, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: ticket.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, activated.Summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
	summary, err = fixture.service.RcloneVersioningSummary(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
}

func TestRcloneVersioningActivationRequiresRollbackHistoryBeforeMutation(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	history := fixture.service.history
	fixture.service.history = nil
	request := backupasset.RcloneVersioningActivationRequest{
		TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision,
		PreflightID: fixture.preflight.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint,
	}
	if _, err := fixture.service.ActivateRcloneVersioning(context.Background(), request); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("activation without rollback history error=%v", err)
	}
	summary, err := fixture.service.RcloneVersioningSummary(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackCleanAvailable)
	fixture.service.history = history
	result, err := fixture.service.ActivateRcloneVersioning(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, result.Summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackCleanAvailable)
}

func TestRcloneVersioningRenewalPreservesHistoricalBinding(t *testing.T) {
	fixture := newRclonePortableVersioningWorkflowFixture(t)
	task, before := activatePortableForRenewal(t, fixture)
	revision, err := managedRsyncTaskRevision(task)
	if err != nil {
		t.Fatal(err)
	}
	pointID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	committedAt := fixture.now
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: before.RepositoryID, ProducingTaskID: &task.ID,
		ProducingTaskNameSnapshot: task.Name, ProducingNodeIDSnapshot: task.NodeID, ProducingNodeNameSnapshot: "node",
		Semantics: string(backupasset.PointXirangManifest), State: string(backupasset.RecoveryPointCommitted),
		ImmutabilityLevel: string(backupasset.ImmutabilityXirangManaged), PhysicalAvailability: string(backupasset.PhysicalUnknown),
		HoldState: string(backupasset.HoldNone), CapabilityRevision: int(before.CapabilityRevision), CommittedAt: &committedAt,
	}
	if err := fixture.db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	now := before.PreflightExpiresAt.Add(time.Second)
	fixture.service.now = func() time.Time { return now }
	summary, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, summary, backupasset.RcloneStatePreflightRequired, backupasset.RcloneReasonPreflightExpired, backupasset.RcloneRollbackPreparationOnly)
	request := backupasset.RcloneVersioningPreflightRequest{TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix}
	ticket, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), request)
	if err != nil || ticket.Purpose != backupasset.RclonePreflightPurposeRenewal {
		t.Fatalf("renewal ticket=%+v err=%v", ticket, err)
	}
	assertRcloneRollbackSummary(t, ticket.Summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
	ready, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, ready, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
	candidate, ok := fixture.service.rcloneCandidates.get(task.ID)
	if !ok {
		t.Fatal("missing renewal candidate")
	}
	// A retry remains in the same generation but invalidates the preceding ticket.
	ticket, err = fixture.service.CreateRcloneVersioningPreflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, ticket.Summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
	again, ok := fixture.service.rcloneCandidates.get(task.ID)
	if !ok || again.setupID != candidate.setupID {
		t.Fatal("renewal changed candidate generation")
	}
	result, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: task.ID, ExpectedTaskRevision: revision, PreflightID: ticket.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, result.Summary, backupasset.RcloneStateCommitted, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
	renewed, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneRollbackSummary(t, renewed, backupasset.RcloneStateCommitted, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
	if result.Summary.TaskRevision == summary.TaskRevision {
		t.Fatal("task CAS revision did not advance")
	}
	var updatedTask model.Task
	if err := fixture.db.First(&updatedTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedTask.Enabled {
		t.Fatal("renewal resumed Task")
	}
	newRevision, _ := managedRsyncTaskRevision(updatedTask)
	plan, err := fixture.service.loadRcloneBindingWorkflowPlan(context.Background(), task.ID, newRevision, rcloneBindingSetupPortable)
	if err != nil {
		t.Fatal(err)
	}
	after := *plan.managedBinding
	if !after.PreflightExpiresAt.After(before.PreflightExpiresAt) {
		t.Fatal("renewal did not extend expiry")
	}
	after.PreflightExpiresAt = before.PreflightExpiresAt
	if !reflect.DeepEqual(before, after) {
		t.Fatal("renewal changed historical binding identity or proof")
	}
	if _, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
		TaskID: task.ID, ExpectedTaskRevision: revision, PreflightID: ticket.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("replayed renewal error=%v", err)
	}
}

func TestRcloneVersioningRenewalSummaryUsesDurableRollbackAuthority(t *testing.T) {
	for _, scenario := range []string{"reservation", "latch", "lease", "tombstone"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRclonePortableVersioningWorkflowFixture(t)
			task, binding := activatePortableForRenewal(t, fixture)
			revision, err := managedRsyncTaskRevision(task)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "reservation":
				// This repository-scoped failed reservation is not owned by the current Task.
				seedManagedHistoryPoint(t, fixture.db, strings.Repeat("1", 32), binding.RepositoryID, backupasset.PointXirangManifest, backupasset.RecoveryPointFailed, fixture.now)
			case "latch":
				insertManagedHistoryLatchFixture(t, fixture.db, "repository", binding.RepositoryID, fixture.now)
			case "lease":
				pointID := strings.Repeat("2", 32)
				seedManagedHistoryPoint(t, fixture.db, pointID, binding.RepositoryID, backupasset.PointMutableHead, backupasset.RecoveryPointObserved, fixture.now)
				lease := model.RecoveryPointLease{
					ID: strings.Repeat("3", 32), RecoveryPointID: pointID,
					HolderType: string(backupasset.LeaseHolderPointPublication), OwnerID: "rclone-summary-test",
					AttemptID: strings.Repeat("4", 32), FenceToken: strings.Repeat("5", 64), Status: string(backupasset.LeaseActive),
					LeaseExpiresAt: fixture.now.Add(time.Hour), AbsoluteDeadline: fixture.now.Add(time.Hour),
					LastHeartbeatAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
				}
				if err := fixture.db.Create(&lease).Error; err != nil {
					t.Fatal(err)
				}
			case "tombstone":
				if err := fixture.db.AutoMigrate(&model.RecoveryPointLifecycleTombstone{}); err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Create(&model.RecoveryPointLifecycleTombstone{
					RecoveryPointID: strings.Repeat("6", 32), RepositoryID: binding.RepositoryID,
					OriginalSemantics: string(backupasset.PointXirangManifest),
					TerminalOperation: string(backupasset.LifecycleRetentionExpire),
					TerminalState:     string(backupasset.RecoveryPointExpired),
					ManagedHistory:    true, ResultCode: "provider_deleted", CreatedAt: fixture.now,
				}).Error; err != nil {
					t.Fatal(err)
				}
				tombstones, err := NewLifecycleManagedHistoryTombstones(fixture.db)
				if err != nil {
					t.Fatal(err)
				}
				fixture.service.history, err = NewManagedHistoryResolver(ManagedHistoryResolverDependencies{
					DB: fixture.db, Tombstones: tombstones,
				})
				if err != nil {
					t.Fatal(err)
				}
			}

			now := binding.PreflightExpiresAt.Add(time.Second)
			fixture.service.now = func() time.Time { return now }
			expired, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertRcloneRollbackSummary(t, expired, backupasset.RcloneStatePreflightRequired, backupasset.RcloneReasonPreflightExpired, backupasset.RcloneRollbackPreparationOnly)
			ticket, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
				TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix,
			})
			if err != nil || ticket.Purpose != backupasset.RclonePreflightPurposeRenewal {
				t.Fatalf("renewal ticket=%+v err=%v", ticket, err)
			}
			assertRcloneRollbackSummary(t, ticket.Summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
			ready, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertRcloneRollbackSummary(t, ready, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
			result, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{
				TaskID: task.ID, ExpectedTaskRevision: revision, PreflightID: ticket.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint,
			})
			if err != nil {
				t.Fatal(err)
			}
			assertRcloneRollbackSummary(t, result.Summary, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
			renewed, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertRcloneRollbackSummary(t, renewed, backupasset.RcloneStateReady, backupasset.RcloneReasonReady, backupasset.RcloneRollbackPreparationOnly)
			var current model.Task
			if err := fixture.db.First(&current, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			currentRevision, err := managedRsyncTaskRevision(current)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.service.CleanRollbackRcloneVersioning(context.Background(), backupasset.RcloneVersioningCleanRollbackRequest{
				TaskID: task.ID, ExpectedTaskRevision: currentRevision, ExpectedBindingRevision: binding.BindingRevision,
			}); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("clean rollback with %s history error=%v, want conflict", scenario, err)
			}
		})
	}
}

func TestRcloneVersioningRenewalFailsClosedWhenRollbackHistoryUnavailable(t *testing.T) {
	for _, scenario := range []string{"missing-resolver", "tombstone-error"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRclonePortableVersioningWorkflowFixture(t)
			task, binding := activatePortableForRenewal(t, fixture)
			revision, err := managedRsyncTaskRevision(task)
			if err != nil {
				t.Fatal(err)
			}
			expected := backupasset.ErrInvalidState
			if scenario == "missing-resolver" {
				fixture.service.history = nil
			} else {
				expected = errors.New("history source unavailable")
				fixture.service.history, err = NewManagedHistoryResolver(ManagedHistoryResolverDependencies{
					DB: fixture.db, Tombstones: managedHistoryTombstoneFake{err: expected},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			now := binding.PreflightExpiresAt.Add(time.Second)
			fixture.service.now = func() time.Time { return now }
			if _, err := fixture.service.RcloneVersioningSummary(context.Background(), task.ID); !errors.Is(err, expected) {
				t.Fatalf("summary history error=%v, want %v", err, expected)
			}
			ticket, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{
				TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix,
			})
			if !errors.Is(err, expected) || ticket.PreflightID != "" {
				t.Fatalf("preflight ticket=%+v history error=%v, want %v and no ticket", ticket, err, expected)
			}
		})
	}
}

func TestRcloneVersioningRenewalRejectsUnsafeChanges(t *testing.T) {
	for _, scenario := range []string{"identity", "imported", "active-run", "enabled", "no-extension"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newRclonePortableVersioningWorkflowFixture(t)
			task, before := activatePortableForRenewal(t, fixture)
			revision, _ := managedRsyncTaskRevision(task)
			now := before.PreflightExpiresAt.Add(time.Second)
			if scenario == "no-extension" {
				now = fixture.now
			}
			fixture.service.now = func() time.Time { return now }
			if scenario == "identity" {
				fixture.service.rclonePreflighter.(*rcloneVersioningPreflighterStub).evidence.ManagedRootIdentityDigest = strings.Repeat("f", 64)
			}
			if scenario == "enabled" {
				if err := fixture.db.Model(&task).Update("enabled", true).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.First(&task, task.ID).Error; err != nil {
					t.Fatal(err)
				}
				revision, _ = managedRsyncTaskRevision(task)
			}
			if scenario == "active-run" {
				run := model.TaskRun{TaskID: task.ID, NodeIDSnapshot: task.NodeID, TriggerType: "manual", Status: "running", StartedAt: timePointer(now), CreatedAt: now, UpdatedAt: now}
				if err := fixture.db.Create(&run).Error; err != nil {
					t.Fatal(err)
				}
			}
			ticket, err := fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationVersionedPrefix})
			if scenario == "identity" || scenario == "active-run" || scenario == "enabled" {
				if !errors.Is(err, backupasset.ErrConflict) {
					t.Fatalf("preflight error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			choice := backupasset.RcloneFirstNewPoint
			if scenario == "imported" {
				choice = backupasset.RcloneImportedBaseline
			}
			_, err = fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{TaskID: task.ID, ExpectedTaskRevision: revision, PreflightID: ticket.PreflightID, MigrationChoice: choice})
			if !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("activation error=%v", err)
			}
			plan, err := fixture.service.loadRcloneBindingWorkflowPlan(context.Background(), task.ID, revision, rcloneBindingSetupPortable)
			if err != nil || !reflect.DeepEqual(*plan.managedBinding, before) {
				t.Fatalf("rejected renewal changed binding: %v", err)
			}
		})
	}
}

func TestRcloneVersioningRenewalNativeCapabilityDrift(t *testing.T) {
	fixture := newRcloneNativeVersioningWorkflowFixture(t)
	_, err := fixture.service.ActivateRcloneVersioning(context.Background(), backupasset.RcloneVersioningActivationRequest{TaskID: fixture.task.ID, ExpectedTaskRevision: fixture.revision, PreflightID: fixture.preflight.PreflightID, MigrationChoice: backupasset.RcloneFirstNewPoint})
	if err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := fixture.db.First(&task, fixture.task.ID).Error; err != nil {
		t.Fatal(err)
	}
	revision, _ := managedRsyncTaskRevision(task)
	plan, err := fixture.service.loadRcloneBindingWorkflowPlan(context.Background(), task.ID, revision, rcloneBindingSetupNative)
	if err != nil {
		t.Fatal(err)
	}
	now := plan.managedBinding.PreflightExpiresAt.Add(time.Second)
	fixture.service.now = func() time.Time { return now }
	stub := fixture.service.rclonePreflighter.(*rcloneVersioningPreflighterStub)
	stub.evidence.Native.VersioningDigest = strings.Repeat("f", 64)
	_, err = fixture.service.CreateRcloneVersioningPreflight(context.Background(), backupasset.RcloneVersioningPreflightRequest{TaskID: task.ID, ExpectedTaskRevision: revision, RequestedMode: backupasset.PublicationNativeObjectVersions})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("native capability drift error=%v", err)
	}
}
