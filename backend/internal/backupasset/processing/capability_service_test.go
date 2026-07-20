package processing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/model"
)

func TestCapabilityServiceNoWorkerReturnsNotDeployedWithoutNoisyJob(t *testing.T) {
	harness := newCoordinatorHarness(t)
	service := newCapabilityServiceForTest(t, harness, content.AuthorizedAsset{
		Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
		SourceFingerprint: validWorkDescriptor().SourceFingerprint, EntryFingerprint: validWorkDescriptor().EntryFingerprint,
		FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
	})

	result, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: content.DeliveryActor{UserID: 7, Role: "operator"}, Ref: validWorkDescriptor().Source,
		Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != ProcessingProductNotDeployed || result.JobID != "" || !result.Terminal ||
		result.Reason != string(ProcessingErrorWorkerUnavailable) {
		t.Fatalf("no-Worker result=%+v", result)
	}
	var jobs int64
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Count(&jobs).Error; err != nil || jobs != 0 {
		t.Fatalf("no-Worker request persisted jobs=%d err=%v", jobs, err)
	}
}

func TestCapabilityServiceStateDoesNotCreateWorkAndUsesClosedRepresentations(t *testing.T) {
	harness := newCoordinatorHarness(t)
	asset := content.AuthorizedAsset{
		Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
		SourceFingerprint: validWorkDescriptor().SourceFingerprint, EntryFingerprint: validWorkDescriptor().EntryFingerprint,
		FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
	}
	service := newCapabilityServiceForTest(t, harness, asset)
	state, err := service.State(context.Background(), PreviewStateRequest{
		Actor: content.DeliveryActor{UserID: 7, Role: "operator"}, Ref: asset.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []PreviewRepresentation{PreviewThumbnail, PreviewText, PreviewDocumentPages, PreviewMedia, PreviewArchiveIndex}
	if state.SchemaVersion != 1 || len(state.Representations) != len(want) {
		t.Fatalf("processing state=%+v", state)
	}
	for index, item := range state.Representations {
		if item.Representation != want[index] || !item.Terminal || item.JobID != "" {
			t.Fatalf("processing state item %d=%+v", index, item)
		}
	}
	var jobs int64
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Count(&jobs).Error; err != nil || jobs != 0 {
		t.Fatalf("state request persisted jobs=%d err=%v", jobs, err)
	}
}

func TestCapabilityServiceUsesInterestHandleAndConsumesTerminalOnce(t *testing.T) {
	harness := newCoordinatorHarness(t)
	asset := content.AuthorizedAsset{
		Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
		SourceFingerprint: validWorkDescriptor().SourceFingerprint, EntryFingerprint: validWorkDescriptor().EntryFingerprint,
		FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
	}
	service := newCapabilityServiceForTest(t, harness, asset)
	advertisement := productionCapabilityForTest(t, capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1)
	registerCapabilityForTest(t, harness, advertisement)
	actor := content.DeliveryActor{UserID: 7, Role: "operator"}

	created, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: actor, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.State != ProcessingProductQueued || backupasset.ValidateOpaqueID(created.JobID) != nil || created.PollAfterSeconds < 1 {
		t.Fatalf("queued result=%+v", created)
	}
	var interest model.BackupAssetProcessingInterest
	if err := harness.db.First(&interest, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if created.JobID == interest.JobID || interest.OwnerKind != string(InterestWorkspace) {
		t.Fatalf("public handle exposed shared job: result=%+v interest=%+v", created, interest)
	}
	setID := strings.Repeat("d", 32)
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", interest.JobID).Updates(map[string]any{
		"state": string(ProcessingSucceeded), "current_artifact_set_id": setID, "transition_revision": 2,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: interest.JobID, AttemptID: strings.Repeat("e", 32), WorkKey: strings.Repeat("f", 64),
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
		SourceFingerprint: asset.SourceFingerprint, SecurityPolicyRevision: "security-policy-v1",
		ManifestDigest: strings.Repeat("a", 64), State: "active", Completeness: "complete", ArtifactCount: 1,
		CreatedAt: harness.clock.Now(), UpdatedAt: harness.clock.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	terminal, err := service.PollPreview(context.Background(), PreviewJobLookup{Actor: actor, Ref: asset.Ref, JobID: created.JobID})
	if err != nil || terminal.State != ProcessingProductDerived || !terminal.Terminal || terminal.JobID != created.JobID {
		t.Fatalf("terminal result=%+v err=%v", terminal, err)
	}
	if _, err := service.PollPreview(context.Background(), PreviewJobLookup{Actor: actor, Ref: asset.Ref, JobID: created.JobID}); !errors.Is(err, ErrProcessingHandleNotFound) {
		t.Fatalf("terminal replay error=%v, want not found", err)
	}
	if _, err := service.PollPreview(context.Background(), PreviewJobLookup{
		Actor: content.DeliveryActor{UserID: 8, Role: "operator"}, Ref: asset.Ref, JobID: created.JobID,
	}); !errors.Is(err, ErrProcessingHandleNotFound) {
		t.Fatalf("cross-owner poll error=%v", err)
	}
}

func newCapabilityServiceForTest(t *testing.T, harness *coordinatorHarness, asset content.AuthorizedAsset) *CapabilityService {
	t.Helper()
	if err := harness.db.AutoMigrate(&model.BackupAssetDerivedArtifactSet{}); err != nil {
		t.Fatal(err)
	}
	service, err := NewCapabilityService(CapabilityServiceDependencies{
		DB: harness.db, Coordinator: harness.coordinator,
		Authorize: capabilityAssetAuthorizerFake{asset: asset}, Now: harness.clock.Now,
		Enabled:                func(context.Context) (bool, error) { return true, nil },
		SecurityPolicyRevision: func(context.Context) (string, error) { return "security-policy-v1", nil },
		PollAfterSeconds:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func productionCapabilityForTest(t *testing.T, capability, profile string) CapabilityAdvertisement {
	t.Helper()
	for _, advertisement := range productionCapabilityAdvertisements() {
		if advertisement.Capability == capability && advertisement.OutputProfile == profile {
			return advertisement
		}
	}
	t.Fatalf("production capability %s/%s missing", capability, profile)
	return CapabilityAdvertisement{}
}

func registerCapabilityForTest(t *testing.T, harness *coordinatorHarness, advertisement CapabilityAdvertisement) {
	t.Helper()
	workerID := strings.Repeat("9", 32)
	now := harness.clock.Now()
	if err := harness.db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: "local", TransportFingerprint: strings.Repeat("9", 64), InstanceID: strings.Repeat("8", 32),
		IdentityRevision: 1, ProtocolVersion: 1, TrustState: "active", HealthState: "ready",
		InteractiveSlots: 2, BackgroundSlots: 2, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("7", 32), WorkerID: workerID, Capability: advertisement.Capability,
		CapabilitySchema: advertisement.CapabilitySchema, PipelineFingerprint: advertisement.PipelineFingerprint,
		OutputProfile: advertisement.OutputProfile, InputModes: "stat,sequential,range", LimitsCanonical: []byte{1},
		AdvertisementDigest: strings.Repeat("6", 64), HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

type capabilityAssetAuthorizerFake struct {
	asset content.AuthorizedAsset
}

func (fake capabilityAssetAuthorizerFake) Authorize(context.Context, content.DeliveryActor, backupasset.AssetRef, content.DeliveryAction) (content.AuthorizedAsset, error) {
	return fake.asset, nil
}
