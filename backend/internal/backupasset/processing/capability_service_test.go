package processing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
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

func TestCapabilityServiceRequiresCurrentCompleteMalwareEvidenceForEnhancedPreview(t *testing.T) {
	tests := []struct {
		name     string
		decision MalwareSafetyDecision
		status   capabilityspec.ScanState
	}{
		{name: "missing", decision: MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanNotScanned}},
		{name: "not scanned", decision: MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanNotScanned}},
		{name: "finding", decision: MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanFinding}},
		{name: "stale", decision: MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanStale}},
		{name: "partial no finding", decision: MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanStale}},
		{name: "wrong signature bundle", decision: MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanStale}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newCoordinatorHarness(t)
			asset := content.AuthorizedAsset{
				Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
				Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
				SourceFingerprint: validWorkDescriptor().SourceFingerprint, EntryFingerprint: validWorkDescriptor().EntryFingerprint,
				FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
			}
			service := newCapabilityServiceForTest(t, harness, asset)
			registerCapabilityForTest(t, harness,
				productionCapabilityForTest(t, capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1))
			service.malwareSafety = func(context.Context, content.AuthorizedAsset) (MalwareSafetyDecision, error) {
				return testCase.decision, nil
			}
			result, err := service.RequestPreview(context.Background(), PreviewJobRequest{
				Actor: content.DeliveryActor{UserID: 7, Role: "operator"}, Ref: asset.Ref, Representation: PreviewThumbnail,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.State != ProcessingProductUnsupported || !result.Terminal || result.JobID != "" ||
				result.ScanStatus != string(testCase.decision.Status) {
				t.Fatalf("unsafe malware decision=%+v result=%+v", testCase.decision, result)
			}
			var jobs int64
			if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Count(&jobs).Error; err != nil || jobs != 0 {
				t.Fatalf("unsafe enhanced preview persisted jobs=%d err=%v", jobs, err)
			}
		})
	}
}

func TestCapabilityServiceStateAndPollRecheckMalwareSafety(t *testing.T) {
	harness := newCoordinatorHarness(t)
	asset := content.AuthorizedAsset{
		Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
		SourceFingerprint: validWorkDescriptor().SourceFingerprint, EntryFingerprint: validWorkDescriptor().EntryFingerprint,
		FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
	}
	service := newCapabilityServiceForTest(t, harness, asset)
	decision := MalwareSafetyDecision{Active: true, Safe: true, Status: capabilityspec.ScanNoFinding}
	service.malwareSafety = func(context.Context, content.AuthorizedAsset) (MalwareSafetyDecision, error) {
		return decision, nil
	}
	advertisement := productionCapabilityForTest(t, capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1)
	registerCapabilityForTest(t, harness, advertisement)
	actor := content.DeliveryActor{UserID: 37, Role: "operator"}
	created, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: actor, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatal(err)
	}
	var interest model.BackupAssetProcessingInterest
	if err := harness.db.First(&interest, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	setID := strings.Repeat("b", 32)
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", interest.JobID).Updates(map[string]any{
		"state": string(ProcessingSucceeded), "current_artifact_set_id": setID, "transition_revision": 2,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: interest.JobID, AttemptID: strings.Repeat("c", 32), WorkKey: capabilityJobWorkKey(t, harness, interest.JobID),
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
		SourceFingerprint: asset.SourceFingerprint, SecurityPolicyRevision: "security-policy-v1",
		ManifestDigest: strings.Repeat("e", 64), State: "active", Completeness: "complete", ArtifactCount: 1,
		CreatedAt: harness.clock.Now(), UpdatedAt: harness.clock.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	decision = MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanStale}
	state, err := service.State(context.Background(), PreviewStateRequest{Actor: actor, Ref: asset.Ref})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range state.Representations {
		if item.Representation == PreviewThumbnail {
			if item.State != ProcessingProductUnsupported || item.ScanStatus != string(capabilityspec.ScanStale) {
				t.Fatalf("unsafe processing state=%+v", item)
			}
		}
	}
	poll, err := service.PollPreview(context.Background(), PreviewJobLookup{Actor: actor, Ref: asset.Ref, JobID: created.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if poll.State != ProcessingProductUnsupported || poll.ScanStatus != string(capabilityspec.ScanStale) || !poll.Terminal {
		t.Fatalf("unsafe processing poll=%+v", poll)
	}
}

func TestCapabilityServicePollConsumesMalwareBlockedQueuedInterest(t *testing.T) {
	actor := content.DeliveryActor{UserID: 67, Role: "operator"}
	harness, service, asset, created, interest := newQueuedThumbnailPreviewForTest(t, actor)
	malwareReads := 0
	service.malwareSafety = func(context.Context, content.AuthorizedAsset) (MalwareSafetyDecision, error) {
		malwareReads++
		status := capabilityspec.ScanFinding
		if malwareReads == 2 {
			status = capabilityspec.ScanStale
		}
		return MalwareSafetyDecision{Active: true, Status: status}, nil
	}

	blocked, err := service.PollPreview(context.Background(), PreviewJobLookup{
		Actor: actor, Ref: asset.Ref, JobID: created.JobID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.State != ProcessingProductUnsupported || blocked.Reason != string(ProcessingErrorInvalidOutput) ||
		blocked.ScanStatus != string(capabilityspec.ScanStale) || malwareReads != 2 || !blocked.Terminal ||
		blocked.JobID != created.JobID || blocked.PollAfterSeconds != 0 {
		t.Fatalf("malware-blocked queued poll=%+v reads=%d", blocked, malwareReads)
	}

	var storedInterest model.BackupAssetProcessingInterest
	if err := harness.db.First(&storedInterest, "id = ?", interest.ID).Error; err != nil {
		t.Fatal(err)
	}
	if storedInterest.Active || storedInterest.RemovedReason != string(InterestRemovedCompleted) || storedInterest.RemovedAt == nil {
		t.Fatalf("malware-blocked interest not consumed: %+v", storedInterest)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", interest.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(ProcessingCancelRequested) || job.CancelReason != string(CancelReasonInterestWithdrawn) {
		t.Fatalf("last-interest malware block did not request cancellation: %+v", job)
	}
	if _, err := service.PollPreview(context.Background(), PreviewJobLookup{
		Actor: actor, Ref: asset.Ref, JobID: created.JobID,
	}); !errors.Is(err, ErrProcessingHandleNotFound) {
		t.Fatalf("malware-blocked handle replay error=%v, want not found", err)
	}
}

func TestCapabilityServicePollMalwareBlockedSharedJobKeepsOtherInterest(t *testing.T) {
	firstActor := content.DeliveryActor{UserID: 77, Role: "operator"}
	harness, service, asset, first, firstInterest := newQueuedThumbnailPreviewForTest(t, firstActor)
	secondActor := content.DeliveryActor{UserID: 78, Role: "operator"}
	second, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: secondActor, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatal(err)
	}
	var secondInterest model.BackupAssetProcessingInterest
	if err := harness.db.First(&secondInterest, "id = ?", second.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if secondInterest.JobID != firstInterest.JobID || secondInterest.ID == firstInterest.ID {
		t.Fatalf("preview interests did not share one job: first=%+v second=%+v", firstInterest, secondInterest)
	}
	service.malwareSafety = func(context.Context, content.AuthorizedAsset) (MalwareSafetyDecision, error) {
		return MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanStale}, nil
	}

	blocked, err := service.PollPreview(context.Background(), PreviewJobLookup{
		Actor: firstActor, Ref: asset.Ref, JobID: first.JobID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.State != ProcessingProductUnsupported || blocked.ScanStatus != string(capabilityspec.ScanStale) || !blocked.Terminal {
		t.Fatalf("shared-job malware-blocked poll=%+v", blocked)
	}
	if err := harness.db.First(&firstInterest, "id = ?", firstInterest.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&secondInterest, "id = ?", secondInterest.ID).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", firstInterest.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if firstInterest.Active || firstInterest.RemovedReason != string(InterestRemovedCompleted) ||
		!secondInterest.Active || job.State != string(ProcessingQueued) {
		t.Fatalf("shared-job malware removal invalid: first=%+v second=%+v job=%+v", firstInterest, secondInterest, job)
	}
	if _, err := service.PollPreview(context.Background(), PreviewJobLookup{
		Actor: firstActor, Ref: asset.Ref, JobID: first.JobID,
	}); !errors.Is(err, ErrProcessingHandleNotFound) {
		t.Fatalf("consumed shared-job handle replay error=%v, want not found", err)
	}
}

func TestCapabilityServicePollMalwareSafetyChangeReturnsCurrentSafeJob(t *testing.T) {
	actor := content.DeliveryActor{UserID: 87, Role: "operator"}
	harness, service, asset, created, interest := newQueuedThumbnailPreviewForTest(t, actor)
	malwareReads := 0
	service.malwareSafety = func(context.Context, content.AuthorizedAsset) (MalwareSafetyDecision, error) {
		malwareReads++
		if malwareReads == 1 {
			return MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanFinding}, nil
		}
		return MalwareSafetyDecision{Active: true, Safe: true, Status: capabilityspec.ScanNoFinding}, nil
	}

	result, err := service.PollPreview(context.Background(), PreviewJobLookup{
		Actor: actor, Ref: asset.Ref, JobID: created.JobID,
	})
	if err != nil {
		t.Fatalf("poll after malware safety became current: %v", err)
	}
	if result.State != ProcessingProductQueued || result.Terminal || result.JobID != created.JobID || malwareReads != 2 {
		t.Fatalf("poll after malware safety became current=%+v reads=%d", result, malwareReads)
	}
	if err := harness.db.First(&interest, "id = ?", interest.ID).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", interest.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if !interest.Active || interest.RemovedReason != "" || interest.RemovedAt != nil || job.State != string(ProcessingQueued) {
		t.Fatalf("malware safety race consumed interest: interest=%+v job=%+v", interest, job)
	}
}

func TestCapabilityServiceRequestPreviewDisclosesQueuedInterestWhenMalwareBecomesSafe(t *testing.T) {
	harness := newCoordinatorHarness(t)
	asset := content.AuthorizedAsset{
		Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
		SourceFingerprint: validWorkDescriptor().SourceFingerprint, EntryFingerprint: validWorkDescriptor().EntryFingerprint,
		FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
	}
	service := newCapabilityServiceForTest(t, harness, asset)
	registerCapabilityForTest(t, harness,
		productionCapabilityForTest(t, capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1))
	malwareReads := 0
	service.malwareSafety = func(context.Context, content.AuthorizedAsset) (MalwareSafetyDecision, error) {
		malwareReads++
		if malwareReads == 2 {
			return MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanStale}, nil
		}
		return MalwareSafetyDecision{Active: true, Safe: true, Status: capabilityspec.ScanNoFinding}, nil
	}

	result, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: content.DeliveryActor{UserID: 88, Role: "operator"}, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatalf("RequestPreview after malware safety became current: %v", err)
	}
	if result.State != ProcessingProductQueued || result.Terminal || backupasset.ValidateOpaqueID(result.JobID) != nil ||
		malwareReads != 3 {
		t.Fatalf("RequestPreview after malware safety became current=%+v reads=%d", result, malwareReads)
	}
	var interest model.BackupAssetProcessingInterest
	if err := harness.db.First(&interest, "id = ?", result.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if !interest.Active || interest.RemovedReason != "" || interest.RemovedAt != nil {
		t.Fatalf("disclosed queued interest was consumed: %+v", interest)
	}
}

func TestCapabilityServiceRequestPreviewConsumesImmediateTerminalOrMalwareBlockedInterest(t *testing.T) {
	tests := []struct {
		name               string
		mutate             func(*testing.T, *coordinatorHarness, *MalwareSafetyDecision, WorkResult)
		wantState          ProcessingProductState
		wantReason         string
		wantScanStatus     string
		wantRemovals       int
		wantStoredJobState ProcessingState
	}{
		{
			name: "terminal job",
			mutate: func(t *testing.T, harness *coordinatorHarness, _ *MalwareSafetyDecision, work WorkResult) {
				t.Helper()
				finishedAt := harness.clock.Now()
				if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", work.JobID).Updates(map[string]any{
					"state": string(ProcessingFailed), "is_current": false, "transition_revision": 2,
					"error_code": string(ProcessingErrorInvalidOutput), "finished_at": &finishedAt,
				}).Error; err != nil {
					t.Fatalf("finish requested preview before RequestWork returns: %v", err)
				}
			},
			wantState: ProcessingProductFailed, wantReason: string(ProcessingErrorInvalidOutput),
			wantStoredJobState: ProcessingFailed,
		},
		{
			name: "malware blocked",
			mutate: func(_ *testing.T, _ *coordinatorHarness, decision *MalwareSafetyDecision, _ WorkResult) {
				*decision = MalwareSafetyDecision{Active: true, Status: capabilityspec.ScanFinding}
			},
			wantState: ProcessingProductUnsupported, wantReason: string(ProcessingErrorInvalidOutput),
			wantScanStatus: string(capabilityspec.ScanFinding), wantRemovals: 1,
			wantStoredJobState: ProcessingCancelRequested,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newCoordinatorHarness(t)
			asset := content.AuthorizedAsset{
				Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
				Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
				SourceFingerprint: validWorkDescriptor().SourceFingerprint, EntryFingerprint: validWorkDescriptor().EntryFingerprint,
				FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
			}
			service := newCapabilityServiceForTest(t, harness, asset)
			registerCapabilityForTest(t, harness,
				productionCapabilityForTest(t, capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1))
			decision := MalwareSafetyDecision{Active: true, Safe: true, Status: capabilityspec.ScanNoFinding}
			service.malwareSafety = func(context.Context, content.AuthorizedAsset) (MalwareSafetyDecision, error) {
				return decision, nil
			}
			coordinator := &requestWorkMutationCoordinator{
				inner: harness.coordinator,
				mutate: func(work WorkResult) {
					testCase.mutate(t, harness, &decision, work)
				},
			}
			service.coordinator = coordinator
			actor := content.DeliveryActor{UserID: 107, Role: "operator"}

			result, err := service.RequestPreview(context.Background(), PreviewJobRequest{
				Actor: actor, Ref: asset.Ref, Representation: PreviewThumbnail,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.State != testCase.wantState || result.Reason != testCase.wantReason ||
				result.ScanStatus != testCase.wantScanStatus || !result.Terminal || result.JobID != "" || result.PollAfterSeconds != 0 {
				t.Fatalf("immediate terminal create result=%+v", result)
			}
			if backupasset.ValidateOpaqueID(coordinator.work.InterestID) != nil {
				t.Fatalf("RequestWork interest handle=%q", coordinator.work.InterestID)
			}
			var interest model.BackupAssetProcessingInterest
			if err := harness.db.First(&interest, "id = ?", coordinator.work.InterestID).Error; err != nil {
				t.Fatal(err)
			}
			if interest.Active || interest.RemovedReason != string(InterestRemovedCompleted) || interest.RemovedAt == nil {
				t.Fatalf("immediate terminal create left active interest: %+v", interest)
			}
			if coordinator.removals != testCase.wantRemovals {
				t.Fatalf("immediate terminal create removals=%d, want %d", coordinator.removals, testCase.wantRemovals)
			}
			var job model.BackupAssetProcessingJob
			if err := harness.db.First(&job, "id = ?", coordinator.work.JobID).Error; err != nil {
				t.Fatal(err)
			}
			if ProcessingState(job.State) != testCase.wantStoredJobState {
				t.Fatalf("immediate terminal create job=%+v", job)
			}
			if _, err := service.PollPreview(context.Background(), PreviewJobLookup{
				Actor: actor, Ref: asset.Ref, JobID: coordinator.work.InterestID,
			}); !errors.Is(err, ErrProcessingHandleNotFound) {
				t.Fatalf("consumed immediate terminal handle replay error=%v, want not found", err)
			}
		})
	}
}

func TestCapabilityServiceRequestPreviewCompensatesTerminalIdentityRace(t *testing.T) {
	harness := newCoordinatorHarness(t)
	asset := content.AuthorizedAsset{
		Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
		SourceFingerprint: validWorkDescriptor().SourceFingerprint, EntryFingerprint: validWorkDescriptor().EntryFingerprint,
		FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
	}
	service := newCapabilityServiceForTest(t, harness, asset)
	advertisement := productionCapabilityForTest(
		t, capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1,
	)
	registerCapabilityForTest(t, harness, advertisement)
	pipelineReads := 0
	service.activePipeline = func(context.Context, string, string) (string, error) {
		pipelineReads++
		if pipelineReads <= 3 {
			return advertisement.PipelineFingerprint, nil
		}
		return strings.Repeat("4", 64), nil
	}
	coordinator := &requestWorkMutationCoordinator{inner: harness.coordinator}
	coordinator.mutate = func(work WorkResult) {
		finishedAt := harness.clock.Now()
		setID := strings.Repeat("1", 32)
		attemptID := strings.Repeat("2", 32)
		if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", work.JobID).Updates(map[string]any{
			"state": string(ProcessingSucceeded), "current_artifact_set_id": setID, "current_attempt_id": attemptID,
			"transition_revision": 2, "is_current": false, "finished_at": &finishedAt,
		}).Error; err != nil {
			t.Fatalf("finish requested preview before RequestWork returns: %v", err)
		}
		seedCapabilitySucceededAttempt(t, harness, work.JobID, attemptID, finishedAt)
		if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
			ID: setID, JobID: work.JobID, AttemptID: attemptID, WorkKey: work.WorkKey,
			RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
			SourceFingerprint: asset.SourceFingerprint, SecurityPolicyRevision: "security-policy-v1",
			ManifestDigest: strings.Repeat("3", 64), State: "active", Completeness: "complete", ArtifactCount: 1,
			CreatedAt: finishedAt, UpdatedAt: finishedAt,
		}).Error; err != nil {
			t.Fatalf("publish requested preview before RequestWork returns: %v", err)
		}
	}
	service.coordinator = coordinator

	_, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: content.DeliveryActor{UserID: 108, Role: "operator"}, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if !errors.Is(err, ErrProcessingPublicationIdentityChanged) {
		t.Fatalf("RequestPreview terminal identity race error=%v", err)
	}
	if coordinator.removals != 1 {
		t.Fatalf("RequestPreview terminal identity race removals=%d, want 1", coordinator.removals)
	}
	var interest model.BackupAssetProcessingInterest
	if err := harness.db.First(&interest, "id = ?", coordinator.work.InterestID).Error; err != nil {
		t.Fatal(err)
	}
	if interest.Active || interest.RemovedReason != string(InterestRemovedCanceled) || interest.RemovedAt == nil {
		t.Fatalf("RequestPreview terminal identity race left undisclosed interest: %+v", interest)
	}
}

type requestWorkMutationCoordinator struct {
	inner    capabilityCoordinator
	mutate   func(WorkResult)
	work     WorkResult
	removals int
}

func (coordinator *requestWorkMutationCoordinator) RequestWork(ctx context.Context, request WorkRequest) (WorkResult, error) {
	work, err := coordinator.inner.RequestWork(ctx, request)
	if err != nil {
		return WorkResult{}, err
	}
	coordinator.work = work
	coordinator.mutate(work)
	return work, nil
}

func (coordinator *requestWorkMutationCoordinator) RemoveInterest(
	ctx context.Context,
	jobID string,
	ownerKind InterestOwnerKind,
	ownerKey string,
	reason InterestRemovedReason,
) error {
	coordinator.removals++
	return coordinator.inner.RemoveInterest(ctx, jobID, ownerKind, ownerKey, reason)
}

func TestCapabilityServiceStateReadsSucceededNonCurrentPublication(t *testing.T) {
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
	actor := content.DeliveryActor{UserID: 47, Role: "operator"}
	created, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: actor, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatal(err)
	}
	var interest model.BackupAssetProcessingInterest
	if err := harness.db.First(&interest, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	setID := strings.Repeat("1", 32)
	attemptID := strings.Repeat("2", 32)
	finishedAt := harness.clock.Now()
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", interest.JobID).Updates(map[string]any{
		"state": string(ProcessingSucceeded), "current_artifact_set_id": setID, "current_attempt_id": attemptID,
		"transition_revision": 2, "is_current": false, "finished_at": &finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	seedCapabilitySucceededAttempt(t, harness, interest.JobID, attemptID, finishedAt)
	if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: interest.JobID, AttemptID: attemptID, WorkKey: capabilityJobWorkKey(t, harness, interest.JobID),
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
		SourceFingerprint: asset.SourceFingerprint, SecurityPolicyRevision: "security-policy-v1",
		ManifestDigest: strings.Repeat("4", 64), State: "active", Completeness: "complete", ArtifactCount: 1,
		CreatedAt: finishedAt, UpdatedAt: finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}

	state, err := service.State(context.Background(), PreviewStateRequest{Actor: actor, Ref: asset.Ref})
	if err != nil {
		t.Fatal(err)
	}
	for _, representation := range state.Representations {
		if representation.Representation == PreviewThumbnail {
			if representation.State != ProcessingProductDerived || representation.Coverage != string(ArtifactComplete) {
				t.Fatalf("terminal publication state=%+v", representation)
			}
			break
		}
	}
	duplicateCapabilityPublication(t, harness.db, interest.JobID, attemptID, setID, finishedAt)
	profile, ok := capabilityspec.Lookup(advertisement.Capability, advertisement.OutputProfile, false)
	if !ok {
		t.Fatal("thumbnail profile unavailable")
	}
	if _, _, err := service.activeDerived(context.Background(), asset, PreviewThumbnail, profile); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("multiple active Derived publications error=%v, want conflict", err)
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
	attemptID := strings.Repeat("e", 32)
	finishedAt := harness.clock.Now()
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", interest.JobID).Updates(map[string]any{
		"state": string(ProcessingSucceeded), "current_artifact_set_id": setID, "current_attempt_id": attemptID,
		"transition_revision": 2, "is_current": false, "finished_at": &finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	seedCapabilitySucceededAttempt(t, harness, interest.JobID, attemptID, finishedAt)
	if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: interest.JobID, AttemptID: attemptID, WorkKey: capabilityJobWorkKey(t, harness, interest.JobID),
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

func TestCapabilityServiceTerminalIdentityChangeDoesNotConsumeHandle(t *testing.T) {
	for _, identity := range []string{"pipeline", "policy"} {
		t.Run(identity, func(t *testing.T) {
			actor := content.DeliveryActor{UserID: 97, Role: "operator"}
			harness, service, asset, created, interest := newQueuedThumbnailPreviewForTest(t, actor)
			profile := productionCapabilityForTest(
				t, capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1,
			)
			setID := strings.Repeat("1", 32)
			attemptID := strings.Repeat("2", 32)
			finishedAt := harness.clock.Now()
			if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", interest.JobID).Updates(map[string]any{
				"state": string(ProcessingSucceeded), "current_artifact_set_id": setID, "current_attempt_id": attemptID,
				"transition_revision": 2, "is_current": false, "finished_at": &finishedAt,
			}).Error; err != nil {
				t.Fatal(err)
			}
			seedCapabilitySucceededAttempt(t, harness, interest.JobID, attemptID, finishedAt)
			if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
				ID: setID, JobID: interest.JobID, AttemptID: attemptID, WorkKey: capabilityJobWorkKey(t, harness, interest.JobID),
				RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
				SourceFingerprint: asset.SourceFingerprint, SecurityPolicyRevision: "security-policy-v1",
				ManifestDigest: strings.Repeat("3", 64), State: "active", Completeness: "complete", ArtifactCount: 1,
				CreatedAt: finishedAt, UpdatedAt: finishedAt,
			}).Error; err != nil {
				t.Fatal(err)
			}

			identityReads := 0
			switch identity {
			case "pipeline":
				service.activePipeline = func(context.Context, string, string) (string, error) {
					identityReads++
					if identityReads == 1 {
						return profile.PipelineFingerprint, nil
					}
					return strings.Repeat("4", 64), nil
				}
			case "policy":
				service.securityPolicyRevision = func(context.Context) (string, error) {
					identityReads++
					if identityReads == 1 {
						return "security-policy-v1", nil
					}
					return "security-policy-v2", nil
				}
			}

			product, err := service.PollPreview(context.Background(), PreviewJobLookup{
				Actor: actor, Ref: asset.Ref, JobID: created.JobID,
			})
			if err == nil {
				t.Fatalf("identity-changing terminal poll returned product=%+v", product)
			}
			if !errors.Is(err, ErrProcessingPublicationIdentityChanged) {
				t.Fatalf("identity-changing terminal poll error=%v, want typed identity change", err)
			}
			if identityReads != 2 {
				t.Fatalf("%s identity reads=%d, want observation plus terminal recheck", identity, identityReads)
			}
			if err := harness.db.First(&interest, "id = ?", interest.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !interest.Active || interest.RemovedReason != "" || interest.RemovedAt != nil {
				t.Fatalf("%s identity race consumed terminal handle: %+v", identity, interest)
			}
		})
	}
}

func TestCapabilityServicePollRejectsTerminalPublicationFromStalePolicy(t *testing.T) {
	harness := newCoordinatorHarness(t)
	asset := content.AuthorizedAsset{
		Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
		SourceFingerprint: validWorkDescriptor().SourceFingerprint, EntryFingerprint: validWorkDescriptor().EntryFingerprint,
		FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
	}
	service := newCapabilityServiceForTest(t, harness, asset)
	registerCapabilityForTest(t, harness,
		productionCapabilityForTest(t, capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1))
	actor := content.DeliveryActor{UserID: 57, Role: "operator"}
	created, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: actor, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatal(err)
	}
	var interest model.BackupAssetProcessingInterest
	if err := harness.db.First(&interest, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	setID := strings.Repeat("1", 32)
	attemptID := strings.Repeat("2", 32)
	finishedAt := harness.clock.Now()
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", interest.JobID).Updates(map[string]any{
		"state": string(ProcessingSucceeded), "current_artifact_set_id": setID, "current_attempt_id": attemptID,
		"transition_revision": 2, "is_current": false, "finished_at": &finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	seedCapabilitySucceededAttempt(t, harness, interest.JobID, attemptID, finishedAt)
	if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: interest.JobID, AttemptID: attemptID, WorkKey: capabilityJobWorkKey(t, harness, interest.JobID),
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
		SourceFingerprint: asset.SourceFingerprint, SecurityPolicyRevision: "stale-policy",
		ManifestDigest: strings.Repeat("4", 64), State: "active", Completeness: "complete", ArtifactCount: 1,
		CreatedAt: finishedAt, UpdatedAt: finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}

	terminal, err := service.PollPreview(context.Background(), PreviewJobLookup{
		Actor: actor, Ref: asset.Ref, JobID: created.JobID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.State != ProcessingProductFailed || terminal.Reason != string(ProcessingErrorInvalidOutput) {
		t.Fatalf("stale-policy terminal publication=%+v, want failed invalid output", terminal)
	}
}

func TestCapabilityServiceDoesNotConsumeTerminalHandleBeforeResultLoads(t *testing.T) {
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
	actor := content.DeliveryActor{UserID: 17, Role: "operator"}
	created, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: actor, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatal(err)
	}
	var interest model.BackupAssetProcessingInterest
	if err := harness.db.First(&interest, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	setID := strings.Repeat("8", 32)
	attemptID := strings.Repeat("9", 32)
	finishedAt := harness.clock.Now()
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", interest.JobID).Updates(map[string]any{
		"state": string(ProcessingSucceeded), "current_artifact_set_id": setID, "current_attempt_id": attemptID,
		"transition_revision": 2, "is_current": false, "finished_at": &finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	seedCapabilitySucceededAttempt(t, harness, interest.JobID, attemptID, finishedAt)
	if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: interest.JobID, AttemptID: attemptID, WorkKey: capabilityJobWorkKey(t, harness, interest.JobID),
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
		SourceFingerprint: asset.SourceFingerprint, SecurityPolicyRevision: "security-policy-v1",
		ManifestDigest: strings.Repeat("6", 64), State: "active", Completeness: "complete", ArtifactCount: 1,
		CreatedAt: harness.clock.Now(), UpdatedAt: harness.clock.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Exec("ALTER TABLE backup_asset_derived_artifact_sets RENAME TO backup_asset_derived_artifact_sets_unavailable").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.PollPreview(context.Background(), PreviewJobLookup{Actor: actor, Ref: asset.Ref, JobID: created.JobID}); err == nil {
		t.Fatal("terminal poll unexpectedly succeeded while Derived result storage was unavailable")
	}
	if err := harness.db.Exec("ALTER TABLE backup_asset_derived_artifact_sets_unavailable RENAME TO backup_asset_derived_artifact_sets").Error; err != nil {
		t.Fatal(err)
	}
	terminal, err := service.PollPreview(context.Background(), PreviewJobLookup{Actor: actor, Ref: asset.Ref, JobID: created.JobID})
	if err != nil || terminal.State != ProcessingProductDerived || !terminal.Terminal {
		t.Fatalf("retry after transient result load failure=%+v err=%v", terminal, err)
	}
}

func TestCapabilityServiceTerminalConsumeRechecksPublicationInTransaction(t *testing.T) {
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
	actor := content.DeliveryActor{UserID: 67, Role: "operator"}
	created, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: actor, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatal(err)
	}
	var interest model.BackupAssetProcessingInterest
	if err := harness.db.First(&interest, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	setID := strings.Repeat("3", 32)
	attemptID := strings.Repeat("4", 32)
	finishedAt := harness.clock.Now()
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", interest.JobID).Updates(map[string]any{
		"state": string(ProcessingSucceeded), "current_artifact_set_id": setID, "current_attempt_id": attemptID,
		"transition_revision": 2, "is_current": false, "finished_at": &finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	seedCapabilitySucceededAttempt(t, harness, interest.JobID, attemptID, finishedAt)
	if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: interest.JobID, AttemptID: attemptID, WorkKey: capabilityJobWorkKey(t, harness, interest.JobID),
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
		SourceFingerprint: asset.SourceFingerprint, SecurityPolicyRevision: "security-policy-v1",
		ManifestDigest: strings.Repeat("5", 64), State: "active", Completeness: "complete", ArtifactCount: 1,
		CreatedAt: finishedAt, UpdatedAt: finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}

	callbackName := "test:terminal-consume-revokes-publication"
	publicationReads := 0
	revoked := false
	if err := harness.db.Callback().Row().Before("gorm:row").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "sets" {
			return
		}
		publicationReads++
		if publicationReads != 2 {
			return
		}
		if updateErr := tx.Session(&gorm.Session{NewDB: true}).Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", setID).
			Update("state", "revoked").Error; updateErr != nil {
			t.Errorf("revoke publication between terminal reads: %v", updateErr)
			return
		}
		revoked = true
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.db.Callback().Row().Remove(callbackName) })

	terminal, err := service.PollPreview(context.Background(), PreviewJobLookup{
		Actor: actor, Ref: asset.Ref, JobID: created.JobID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !revoked || terminal.State != ProcessingProductFailed ||
		terminal.Reason != string(ProcessingErrorInvalidOutput) || !terminal.Terminal {
		t.Fatalf("terminal result after publication race=%+v revoked=%t", terminal, revoked)
	}
	if _, err := service.PollPreview(context.Background(), PreviewJobLookup{
		Actor: actor, Ref: asset.Ref, JobID: created.JobID,
	}); !errors.Is(err, ErrProcessingHandleNotFound) {
		t.Fatalf("terminal handle replay error=%v, want not found", err)
	}
}

func TestCapabilityServiceNeverServesDerivedFromInactivePipeline(t *testing.T) {
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
	actor := content.DeliveryActor{UserID: 27, Role: "operator"}
	created, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: actor, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatal(err)
	}
	var interest model.BackupAssetProcessingInterest
	if err := harness.db.First(&interest, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	setID := strings.Repeat("4", 32)
	if err := harness.db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", interest.JobID).Updates(map[string]any{
		"state": string(ProcessingSucceeded), "current_artifact_set_id": setID, "transition_revision": 2,
		"pipeline_fingerprint": strings.Repeat("0", 64),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: interest.JobID, AttemptID: strings.Repeat("5", 32), WorkKey: capabilityJobWorkKey(t, harness, interest.JobID),
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
		SourceFingerprint: asset.SourceFingerprint, SecurityPolicyRevision: "security-policy-v1",
		ManifestDigest: strings.Repeat("2", 64), State: "active", Completeness: "complete", ArtifactCount: 1,
		CreatedAt: harness.clock.Now(), UpdatedAt: harness.clock.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	state, err := service.State(context.Background(), PreviewStateRequest{Actor: actor, Ref: asset.Ref})
	if err != nil {
		t.Fatal(err)
	}
	for _, representation := range state.Representations {
		if representation.Representation == PreviewThumbnail && representation.State == ProcessingProductDerived {
			t.Fatalf("inactive pipeline leaked through state: %+v", representation)
		}
	}
	terminal, err := service.PollPreview(context.Background(), PreviewJobLookup{Actor: actor, Ref: asset.Ref, JobID: created.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.State == ProcessingProductDerived || terminal.State == ProcessingProductPartial {
		t.Fatalf("inactive pipeline leaked through poll: %+v", terminal)
	}
}

func newQueuedThumbnailPreviewForTest(
	t *testing.T,
	actor content.DeliveryActor,
) (*coordinatorHarness, *CapabilityService, content.AuthorizedAsset, PreviewJobResult, model.BackupAssetProcessingInterest) {
	t.Helper()
	harness := newCoordinatorHarness(t)
	asset := content.AuthorizedAsset{
		Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
		SourceFingerprint: validWorkDescriptor().SourceFingerprint, EntryFingerprint: validWorkDescriptor().EntryFingerprint,
		FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
	}
	service := newCapabilityServiceForTest(t, harness, asset)
	registerCapabilityForTest(t, harness,
		productionCapabilityForTest(t, capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1))
	created, err := service.RequestPreview(context.Background(), PreviewJobRequest{
		Actor: actor, Ref: asset.Ref, Representation: PreviewThumbnail,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.State != ProcessingProductQueued || created.Terminal {
		t.Fatalf("queued preview=%+v", created)
	}
	var interest model.BackupAssetProcessingInterest
	if err := harness.db.First(&interest, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	return harness, service, asset, created, interest
}

func TestCapabilityServiceResolvesOnlyReadyArchiveExtractCapability(t *testing.T) {
	harness := newCoordinatorHarness(t)
	asset := content.AuthorizedAsset{
		Ref: validWorkDescriptor().Source, CatalogGenerationID: validWorkDescriptor().CatalogGenerationID,
		Provider: backupasset.ProviderRestic, ProviderCapabilityRevision: 1,
		SourceFingerprint:   validWorkDescriptor().SourceFingerprint,
		EntryFingerprint:    validWorkDescriptor().EntryFingerprint,
		FingerprintStrength: "strong", Size: 1024, MediaType: "application/zip",
	}
	service := newCapabilityServiceForTest(t, harness, asset)
	if _, err := service.ArchiveExtractCapability(context.Background()); !errors.Is(err, ErrNotDeployed) {
		t.Fatalf("missing capability error=%v", err)
	}
	want := productionCapabilityForTest(
		t, capabilityspec.CapabilityArchiveExtractEntry, capabilityspec.ProfileArchiveMemberV1,
	)
	registerCapabilityForTest(t, harness, want)
	got, err := service.ArchiveExtractCapability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || got.Capability != want.Capability ||
		got.CapabilitySchema != want.CapabilitySchema || got.PipelineFingerprint != want.PipelineFingerprint ||
		got.OutputProfile != want.OutputProfile {
		t.Fatalf("capability=%+v want=%+v", got, want)
	}
}

func newCapabilityServiceForTest(t *testing.T, harness *coordinatorHarness, asset content.AuthorizedAsset) *CapabilityService {
	t.Helper()
	if err := harness.db.AutoMigrate(&model.BackupAssetDerivedArtifactSet{}); err != nil {
		t.Fatal(err)
	}
	var service *CapabilityService
	service, err := NewCapabilityService(CapabilityServiceDependencies{
		DB: harness.db, Coordinator: harness.coordinator,
		Authorize: capabilityAssetAuthorizerFake{asset: asset}, Now: harness.clock.Now,
		Enabled:                func(context.Context) (bool, error) { return true, nil },
		SecurityPolicyRevision: func(context.Context) (string, error) { return "security-policy-v1", nil },
		ActivePipeline: func(_ context.Context, capability, profile string) (string, error) {
			return productionCapabilityForTest(t, capability, profile).PipelineFingerprint, nil
		},
		PublicationIdentityTx: func(ctx context.Context, tx *gorm.DB, capability, profile string) (ActivePublicationIdentity, error) {
			if service == nil || tx == nil {
				return ActivePublicationIdentity{}, ErrInvalidContract
			}
			pipeline, err := service.activePipeline(ctx, capability, profile)
			if err != nil || pipeline == "" {
				return ActivePublicationIdentity{}, err
			}
			policy, err := service.securityPolicyRevision(ctx)
			return ActivePublicationIdentity{PipelineFingerprint: pipeline, SecurityPolicyRevision: policy}, err
		},
		MalwareSafety: func(context.Context, content.AuthorizedAsset) (MalwareSafetyDecision, error) {
			return MalwareSafetyDecision{Active: true, Safe: true, Status: capabilityspec.ScanNoFinding}, nil
		},
		PollAfterSeconds: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func seedCapabilitySucceededAttempt(
	t *testing.T,
	harness *coordinatorHarness,
	jobID string,
	attemptID string,
	finishedAt time.Time,
) {
	t.Helper()
	if err := harness.db.Create(&model.BackupAssetProcessingAttempt{
		ID: attemptID, JobID: jobID, AttemptNumber: 1, WorkerID: strings.Repeat("9", 32),
		SlotClass: "interactive", State: "succeeded", WorkerLeaseExpiresAt: finishedAt.Add(time.Minute),
		LastHeartbeatAt: finishedAt, RecoveryPointLeaseID: strings.Repeat("a", 32),
		RecoveryPointAttemptID: strings.Repeat("b", 32), RecoveryPointFenceHash: strings.Repeat("c", 64),
		AbsoluteDeadline: finishedAt.Add(time.Hour), IsCurrent: false, StartedAt: finishedAt,
		FinishedAt: &finishedAt, CreatedAt: finishedAt, UpdatedAt: finishedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", attemptID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
}

func capabilityJobWorkKey(t *testing.T, harness *coordinatorHarness, jobID string) string {
	t.Helper()
	var job model.BackupAssetProcessingJob
	if err := harness.db.First(&job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	return job.WorkKey
}

func duplicateCapabilityPublication(
	t *testing.T,
	db *gorm.DB,
	jobID string,
	attemptID string,
	setID string,
	updatedAt time.Time,
) {
	t.Helper()
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var set model.BackupAssetDerivedArtifactSet
	if err := db.First(&job, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&attempt, "id = ?", attemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&set, "id = ?", setID).Error; err != nil {
		t.Fatal(err)
	}
	newJobID := strings.Repeat("a", 32)
	newAttemptID := strings.Repeat("b", 32)
	newSetID := strings.Repeat("c", 32)
	job.ID = newJobID
	job.WorkKey = strings.Repeat("d", 64)
	job.CurrentAttemptID = &newAttemptID
	job.CurrentArtifactSetID = &newSetID
	job.UpdatedAt = updatedAt.Add(time.Minute)
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", newJobID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	attempt.ID = newAttemptID
	attempt.JobID = newJobID
	attempt.UpdatedAt = job.UpdatedAt
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", newAttemptID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	set.ID = newSetID
	set.JobID = newJobID
	set.AttemptID = newAttemptID
	set.WorkKey = job.WorkKey
	set.ManifestDigest = strings.Repeat("e", 64)
	set.UpdatedAt = job.UpdatedAt
	if err := db.Create(&set).Error; err != nil {
		t.Fatal(err)
	}
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
