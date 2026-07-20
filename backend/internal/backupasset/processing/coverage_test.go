package processing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/model"
)

func TestCoverageServiceReturnsOnlyBoundedCapabilityAggregates(t *testing.T) {
	harness := newCoordinatorHarness(t)
	if err := harness.db.AutoMigrate(&model.BackupAssetDerivedArtifactSet{}); err != nil {
		t.Fatal(err)
	}
	now := harness.clock.Now()
	jobs := []model.BackupAssetProcessingJob{
		coverageJobForTest("1", capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1, ProcessingQueued, now.Add(-2*time.Hour)),
		coverageJobForTest("2", capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1, ProcessingSucceeded, now.Add(-time.Hour)),
		coverageJobForTest("3", capabilityspec.CapabilityTextExtract, capabilityspec.ProfileBoundedTextV1, ProcessingFailed, now.Add(-30*time.Minute)),
	}
	for index := range jobs {
		if err := harness.db.Create(&jobs[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewCoverageService(harness.db, harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || result.Eligible != 3 || result.Completed != 1 || result.Queued != 1 || result.Failed != 1 ||
		result.BacklogAgeBucket != "1h_24h" || result.EstimatedSeconds == nil || *result.EstimatedSeconds < 1 {
		t.Fatalf("coverage summary=%+v", result)
	}
	if len(result.ByCapability) != 2 || result.ByCapability[0].Capability != capabilityspec.CapabilityImageThumbnail {
		t.Fatalf("coverage buckets=%+v", result.ByCapability)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"job_id", "recovery_point_id", "entry_id", "worker_id", "pipeline_fingerprint", "path", "source_fingerprint"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("coverage leaked %q: %s", forbidden, payload)
		}
	}
}

func TestCoverageCapabilityInventoryUsesClosedProfilesAndReadyCounts(t *testing.T) {
	harness := newCoordinatorHarness(t)
	if err := harness.db.AutoMigrate(&model.BackupAssetDerivedArtifactSet{}); err != nil {
		t.Fatal(err)
	}
	service, err := NewCoverageService(harness.db, harness.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	before, err := service.Capabilities(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(capabilityspec.RequiredProfiles()) {
		t.Fatalf("capability count=%d want=%d", len(before), len(capabilityspec.RequiredProfiles()))
	}
	for _, item := range before {
		if item.Deployed || item.Capability == capabilityspec.CapabilitySecretClassify || item.ReadyWorkers != 0 {
			t.Fatalf("unexpected default capability=%+v", item)
		}
	}
	advertisement := productionCapabilityForTest(t, capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1)
	registerCapabilityForTest(t, harness, advertisement)
	after, err := service.Capabilities(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range after {
		if item.Capability == advertisement.Capability && item.Profile == advertisement.OutputProfile {
			found = item.Deployed && item.ReadyWorkers == 1
		}
	}
	if !found {
		t.Fatalf("ready closed capability not reflected: %+v", after)
	}
}

func coverageJobForTest(suffix, capability, profile string, state ProcessingState, queuedAt time.Time) model.BackupAssetProcessingJob {
	descriptor := validWorkDescriptor()
	return model.BackupAssetProcessingJob{
		ID: strings.Repeat(suffix, 32), WorkKey: strings.Repeat(suffix, 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{}`), RecoveryPointID: descriptor.Source.RecoveryPointID,
		CatalogGenerationID: descriptor.CatalogGenerationID, EntryID: descriptor.Source.EntryID,
		SourceFingerprint: descriptor.SourceFingerprint, EntryFingerprint: descriptor.EntryFingerprint,
		ProviderCapabilityRevision: 1, Capability: capability, CapabilitySchema: "coverage.v1",
		PipelineFingerprint: strings.Repeat(suffix, 64), OutputProfile: profile,
		SecurityPolicyRevision: "security-policy-v1", PriorityClass: string(PriorityBackground),
		EffectivePriority: 10, State: string(state), TransitionRevision: 1, IsCurrent: true,
		QueuedAt: queuedAt, AbsoluteDeadline: queuedAt.Add(2 * time.Hour), CreatedAt: queuedAt, UpdatedAt: queuedAt,
	}
}
