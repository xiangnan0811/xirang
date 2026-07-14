package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
)

func TestPublicationSharedRepositoryConcurrentTasksRetriesAndManualSnapshotsNeverCrossClaim(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)

	first, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatalf("prepare first Task: %v", err)
	}
	defer func() { _ = first.Abandon(backupasset.ErrPublicationSessionAbandoned) }()

	secondTask := seedTask(t, fixture.db, "restic", "sftp:user@example.invalid:/repo-b", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY"}`)
	var secondNode model.Node
	if err := fixture.db.First(&secondNode, secondTask.NodeID).Error; err != nil {
		t.Fatal(err)
	}
	secondTask.Node = secondNode
	secondRun := model.TaskRun{
		TaskID: secondTask.ID, TriggerType: "manual", Status: "running",
		StartedAt: timePointer(fixture.now.Add(-time.Second)), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&secondRun).Error; err != nil {
		t.Fatal(err)
	}
	secondLink := model.TaskRepositoryLink{
		ID: strings.Repeat("4", 32), TaskID: &secondTask.ID, RepositoryID: fixture.repository.ID,
		TaskNameSnapshot: secondTask.Name, NodeIDSnapshot: secondNode.ID, NodeNameSnapshot: secondNode.Name,
		PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&secondLink).Error; err != nil {
		t.Fatal(err)
	}

	second, err := fixture.service.Prepare(context.Background(), publication.Run{
		Task: secondTask, TaskRunID: secondRun.ID, Trigger: secondRun.TriggerType, StartedAt: *secondRun.StartedAt,
		Audit: backupasset.PublicationAuditContext{Actor: backupasset.AuditActor{UserID: 9, Username: "operator", Role: "operator"}, CorrelationID: "publication-shared-2"},
	})
	if err != nil {
		t.Fatalf("prepare second Task against the shared Repository: %v", err)
	}
	defer func() { _ = second.Abandon(backupasset.ErrPublicationSessionAbandoned) }()

	firstAttempt, secondAttempt := first.Attempt(), second.Attempt()
	if firstAttempt.RepositoryID != secondAttempt.RepositoryID || firstAttempt.RecoveryPointID == secondAttempt.RecoveryPointID ||
		firstAttempt.RequiredTags[0] == secondAttempt.RequiredTags[0] || firstAttempt.RequiredTags[1] == secondAttempt.RequiredTags[1] {
		t.Fatalf("shared Repository attempts are not run-scoped: first=%+v second=%+v", firstAttempt, secondAttempt)
	}
	if firstAttempt.Access.TaskID != fixture.task.ID || secondAttempt.Access.TaskID != secondTask.ID ||
		firstAttempt.Access.NodeID != fixture.node.ID || secondAttempt.Access.NodeID != secondNode.ID {
		t.Fatalf("shared Repository attempt used another Task's access: first_task=%d first_node=%d second_task=%d second_node=%d", firstAttempt.Access.TaskID, firstAttempt.Access.NodeID, secondAttempt.Access.TaskID, secondAttempt.Access.NodeID)
	}
	if _, err := first.RecordProviderCommit(context.Background(), fixture.commitEvidence()); err != nil {
		t.Fatalf("commit first Task evidence: %v", err)
	}
	secondEvidence := fixture.commitEvidence()
	secondEvidence.NativePointID = strings.Repeat("d", 64)
	if _, err := second.RecordProviderCommit(context.Background(), secondEvidence); err != nil {
		t.Fatalf("commit second Task evidence: %v", err)
	}

	var points []model.RecoveryPoint
	if err := fixture.db.Order("id").Find(&points).Error; err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].ProducingTaskID == nil || points[1].ProducingTaskID == nil ||
		(*points[0].ProducingTaskID != fixture.task.ID && *points[1].ProducingTaskID != fixture.task.ID) ||
		(*points[0].ProducingTaskID != secondTask.ID && *points[1].ProducingTaskID != secondTask.ID) {
		t.Fatalf("shared Repository points crossed Task lineage: %+v", points)
	}
}
