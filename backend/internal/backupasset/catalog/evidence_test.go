package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

func TestCatalogEvidenceUsesExactSourceRunAndNeverSerializesRawEvidence(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 17, 15, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", fixture.ownedPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.ProducingTaskID == nil || point.ProducingTaskRunID == nil {
		t.Fatal("owned fixture has no producing lineage")
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", *point.ProducingTaskRunID).
		Update("last_error", "SECRET_TASK_RUN_LAST_ERROR").Error; err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{
		Version: 1, PublicationRevision: 2, AttemptCount: 1, Provider: backupasset.ProviderRestic,
		CaptureStartedAt: timePointer(now.Add(-2 * time.Minute)), CaptureFinishedAt: timePointer(now.Add(-time.Minute)),
		FilesProcessed: 4, LogicalBytes: 99, CapabilityRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).Update("consistency_json", consistency).Error; err != nil {
		t.Fatal(err)
	}
	manifest := model.RecoveryPointManifest{
		ID: strings.Repeat("6", 32), RecoveryPointID: point.ID, Revision: 2, DigestAlgorithm: "sha256",
		Digest: strings.Repeat("7", 64), Generator: "restic", GeneratorVersion: "1", Completeness: string(backupasset.ManifestComplete),
		EntryCount: 4, LogicalBytes: 99, FidelityJSON: `{}`,
		EncryptedCommitEvidence: `{"version":1,"provider":"restic","repository_identity":"SECRET_REPOSITORY_IDENTITY","native_point_id":"SECRET_NATIVE_POINT","observed_tags":["SECRET_TAG_A","SECRET_TAG_B"]}`,
		IsActive:                true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	drillRun := seedCatalogOwnershipRun(t, db, *point.ProducingTaskID, now.Add(time.Minute))
	nilSourceRun := seedCatalogOwnershipRun(t, db, *point.ProducingTaskID, now.Add(2*time.Minute))
	exact := model.RestoreDrillEvidence{
		PolicyID: 1, TaskID: *point.ProducingTaskID, TaskRunID: drillRun.ID, SourceTaskRunID: point.ProducingTaskRunID,
		SnapshotRef: "SECRET_SNAPSHOT_REF", SandboxNodeID: 999, SandboxNodeName: "SECRET_SANDBOX_NODE",
		SandboxPath: "/SECRET/SANDBOX/PATH", Status: "success", ConfidenceEligible: true,
		StartedAt: timePointer(now), FinishedAt: timePointer(now.Add(time.Minute)), DurationMs: 60000,
		RestoreStatus: "success", RestoreError: "SECRET_RESTORE_ERROR", VerifyStatus: "success", VerifyError: "SECRET_VERIFY_ERROR",
		PostVerifyStatus: "success", PostVerifyError: "SECRET_POST_VERIFY_ERROR", CleanupStatus: "success", CleanupError: "SECRET_CLEANUP_ERROR",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&exact).Error; err != nil {
		t.Fatal(err)
	}
	nilSource := model.RestoreDrillEvidence{
		PolicyID: 1, TaskID: *point.ProducingTaskID, TaskRunID: nilSourceRun.ID, SourceTaskRunID: nil,
		SandboxNodeID: 999, SandboxPath: "/SECRET/NIL/SOURCE", Status: "success", RestoreStatus: "success",
		VerifyStatus: "success", PostVerifyStatus: "success", CleanupStatus: "success", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&nilSource).Error; err != nil {
		t.Fatal(err)
	}

	service := newCatalogServiceForTest(t, db, now.Add(3*time.Minute))
	evidence, err := service.GetEvidence(context.Background(), point.ID, AuthorizationScope{Role: "operator", UserID: fixture.operatorID})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.RecoveryPointID != point.ID || evidence.Lineage.Status != EvidenceRecorded ||
		evidence.Lineage.TaskRunID == nil || *evidence.Lineage.TaskRunID != *point.ProducingTaskRunID ||
		evidence.Manifest.Status != EvidenceRecorded || evidence.Manifest.ID != manifest.ID ||
		evidence.Publication.Status != EvidenceRecorded || evidence.Publication.Provider != backupasset.ProviderRestic ||
		evidence.RestoreDrills.Status != EvidenceRecorded || len(evidence.RestoreDrills.Items) != 1 ||
		evidence.RestoreDrills.Items[0].TaskRunID != drillRun.ID {
		t.Fatalf("evidence projection=%+v", evidence)
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	body := string(payload)
	for _, forbidden := range []string{
		"SECRET_TASK_RUN_LAST_ERROR", "SECRET_REPOSITORY_IDENTITY", "SECRET_NATIVE_POINT", "SECRET_TAG_A",
		"SECRET_SNAPSHOT_REF", "SECRET_SANDBOX_NODE", "/SECRET/SANDBOX/PATH", "/SECRET/NIL/SOURCE",
		"SECRET_RESTORE_ERROR", "SECRET_VERIFY_ERROR", "SECRET_POST_VERIFY_ERROR", "SECRET_CLEANUP_ERROR",
		"encrypted_commit_evidence", "lineage_json", "consistency_json",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("evidence leaked %q: %s", forbidden, body)
		}
	}
	if _, err := service.GetEvidence(context.Background(), fixture.unownedPointID, AuthorizationScope{Role: "operator", UserID: fixture.operatorID}); !errors.Is(err, backupasset.ErrNotFound) {
		t.Fatalf("unowned evidence error=%v", err)
	}
}

func TestCatalogEvidenceMarksMalformedLayersInvalidWithoutEchoingValues(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 17, 16, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", fixture.ownedPointID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
		Update("consistency_json", `{"provider":"SECRET_UNKNOWN_PROVIDER"}`).Error; err != nil {
		t.Fatal(err)
	}
	manifest := model.RecoveryPointManifest{
		ID: strings.Repeat("8", 32), RecoveryPointID: point.ID, Revision: 1, DigestAlgorithm: "sha256",
		Digest: strings.Repeat("9", 64), Generator: "fixture", GeneratorVersion: "1", Completeness: string(backupasset.ManifestComplete),
		FidelityJSON: `{}`, EncryptedCommitEvidence: `{"SECRET_MALFORMED":`, IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	evidence, err := newCatalogServiceForTest(t, db, now).GetEvidence(
		context.Background(), point.ID, AuthorizationScope{Role: "operator", UserID: fixture.operatorID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Publication.Status != EvidenceInvalid {
		t.Fatalf("malformed publication status=%q", evidence.Publication.Status)
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "SECRET_UNKNOWN_PROVIDER") || strings.Contains(string(payload), "SECRET_MALFORMED") {
		t.Fatalf("malformed evidence echoed raw values: %s", payload)
	}
}

func TestCatalogEvidenceFailsClosedForUnknownRunAndDrillStatuses(t *testing.T) {
	db, _ := openCatalogBehaviorSQLite(t)
	now := time.Date(2026, 7, 17, 17, 0, 0, 0, time.UTC)
	fixture := seedCatalogOwnershipFixture(t, db, now)
	var point model.RecoveryPoint
	if err := db.First(&point, "id = ?", fixture.ownedPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.ProducingTaskID == nil || point.ProducingTaskRunID == nil {
		t.Fatal("owned fixture has no producing lineage")
	}
	if err := db.Model(&model.TaskRun{}).Where("id = ?", *point.ProducingTaskRunID).
		Update("status", "SECRET_FUTURE_RUN_STATUS").Error; err != nil {
		t.Fatal(err)
	}
	drillRun := seedCatalogOwnershipRun(t, db, *point.ProducingTaskID, now.Add(time.Minute))
	drill := model.RestoreDrillEvidence{
		PolicyID: 1, TaskID: *point.ProducingTaskID, TaskRunID: drillRun.ID, SourceTaskRunID: point.ProducingTaskRunID,
		SandboxNodeID: 1, Status: "SECRET_FUTURE_DRILL_STATUS", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&drill).Error; err != nil {
		t.Fatal(err)
	}
	evidence, err := newCatalogServiceForTest(t, db, now).GetEvidence(
		context.Background(), point.ID, AuthorizationScope{Role: "operator", UserID: fixture.operatorID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Lineage.Status != EvidenceInvalid || evidence.Lineage.RunStatus != "" ||
		evidence.RestoreDrills.Status != EvidenceInvalid || len(evidence.RestoreDrills.Items) != 0 {
		t.Fatalf("unknown status evidence=%+v", evidence)
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "SECRET_FUTURE") {
		t.Fatalf("unknown evidence status echoed raw value: %s", payload)
	}
}
