package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	processingupdater "xirang/backend/internal/backupasset/processing/updater"
	"xirang/backend/internal/backupasset/search"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

func TestProcessingRuntimeDisabledOrUnconfiguredDoesNotCreateDerivedState(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		enabled bool
	}{
		{name: "feature disabled", enabled: false},
		{name: "no Worker transport", enabled: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := openProcessingRuntimeTestDB(t)
			root := filepath.Join(t.TempDir(), "derived")
			foundation := processingRuntimeFoundation(t, db, root, testCase.enabled, false)
			keyring := backupasset.NewKeyring(db, time.Now)
			lease := processingRuntimeLease(t, db)
			runtime, err := newProcessingRuntime(processingRuntimeDependencies{
				DB: db, Foundation: foundation, Keyring: keyring, Lease: lease,
				Source: processingRuntimeSourceFake{}, Authorize: processingRuntimeAssetAuthorizerFake{}, ValidateRoot: processingRuntimeRootValidator,
				RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{},
			})
			if err != nil {
				t.Fatalf("newProcessingRuntime: %v", err)
			}
			if err := runtime.Startup(context.Background()); err != nil {
				t.Fatalf("Startup: %v", err)
			}
			if runtime.WorkerProtocol() != nil {
				t.Fatal("inactive runtime exposed a Worker protocol port")
			}
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("inactive runtime created Derived root: %v", err)
			}
			var count int64
			if err := db.Model(&model.WrappedDomainKey{}).
				Where("domain = ?", backupasset.KeyDomainDerivedStore).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("inactive runtime created %d Derived key rows", count)
			}
			summary, err := runtime.AdminSummary(context.Background())
			if err != nil {
				t.Fatalf("AdminSummary: %v", err)
			}
			if summary.Configured || summary.LocalEnabled || summary.RemoteEnabled || summary.Workers.Active != 0 || summary.Queue.Total != 0 {
				t.Fatalf("inactive runtime summary is noisy: %+v", summary)
			}
		})
	}
}

func TestProcessingRuntimeNoWorkerReturnsNotDeployedWithoutCreatingJob(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	root := filepath.Join(t.TempDir(), "derived")
	ref := backupasset.AssetRef{RecoveryPointID: strings.Repeat("1", 32), EntryID: strings.Repeat("2", 64)}
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, root, true, false),
		Keyring: backupasset.NewKeyring(db, time.Now), Lease: processingRuntimeLease(t, db),
		Source: processingRuntimeSourceFake{},
		Authorize: processingRuntimeAssetAuthorizerFake{asset: content.AuthorizedAsset{
			Ref: ref, CatalogGenerationID: strings.Repeat("3", 32), Provider: backupasset.ProviderRsync,
			ProviderCapabilityRevision: 1, SourceFingerprint: "source-v1", EntryFingerprint: "entry-v1",
			FingerprintStrength: "strong", Size: 1024, MediaType: "image/png",
		}},
		ValidateRoot: processingRuntimeRootValidator, RevalidateSource: processingRuntimeSourceRevalidatorFake{},
		Projection: processingRuntimeProjectionFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.RequestPreview(context.Background(), processing.PreviewJobRequest{
		Actor: content.DeliveryActor{UserID: 7, Username: "operator", Role: "operator"},
		Ref:   ref, Representation: processing.PreviewThumbnail,
	})
	if err != nil || result.State != processing.ProcessingProductNotDeployed || result.Terminal != true {
		t.Fatalf("no-Worker preview result=%+v err=%v", result, err)
	}
	var jobs int64
	if err := db.Model(&model.BackupAssetProcessingJob{}).Count(&jobs).Error; err != nil || jobs != 0 {
		t.Fatalf("no-Worker preview jobs=%d err=%v", jobs, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-Worker preview created Derived root: %v", err)
	}
	summary, err := runtime.AdminSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.BackfillPolicy.Revision) != 64 || !summary.BackfillPolicy.Paused ||
		summary.BackfillPolicy.BatchSize <= 0 || summary.BackfillPolicy.JobsPerHour <= 0 ||
		summary.BackfillPolicy.BytesPerHour <= 0 || summary.BackfillPolicy.ProviderConcurrency <= 0 ||
		summary.BackfillPolicy.CapabilityConcurrency <= 0 {
		t.Fatalf("no-Worker backfill policy=%+v", summary.BackfillPolicy)
	}
}

func TestProcessingRuntimeArchiveMemberCoordinatorRequiresReadyAndRunning(t *testing.T) {
	coordinator := &processing.Coordinator{}
	runtime := &managedProcessingRuntime{coordinator: coordinator}
	adapter := runtimeArchiveMemberCoordinator{runtime: runtime}

	if current := (runtimeArchiveMemberCoordinator{}).current(); current != nil {
		t.Fatal("nil Processing runtime exposed an archive-member coordinator")
	}
	if current := adapter.current(); current != nil {
		t.Fatal("not-ready Processing runtime exposed an archive-member coordinator")
	}
	runtime.ready.Store(true)
	if current := adapter.current(); current != coordinator {
		t.Fatalf("ready Processing runtime coordinator=%p, want %p", current, coordinator)
	}
	runtime.stopped.Store(true)
	if current := adapter.current(); current != nil {
		t.Fatal("stopped Processing runtime exposed an archive-member coordinator")
	}
}

func TestProcessingRuntimeMalwareSafetyRequiresCurrentCompleteExactEvidence(t *testing.T) {
	now := time.Date(2026, 7, 21, 2, 0, 0, 0, time.UTC)
	bundleFingerprint := strings.Repeat("a", 64)
	asset := content.AuthorizedAsset{
		Ref: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("1", 32),
			EntryID:         strings.Repeat("2", 64),
		},
		CatalogGenerationID:        strings.Repeat("3", 32),
		Provider:                   backupasset.ProviderRestic,
		ProviderCapabilityRevision: 7,
		SourceFingerprint:          "source-fingerprint-v1",
		EntryFingerprint:           strings.Repeat("4", 64),
		FingerprintStrength:        "strong",
		Size:                       4096,
		MediaType:                  "application/pdf",
	}

	tests := []struct {
		name             string
		result           capabilityspec.MalwareResult
		mutateAsset      func(*content.AuthorizedAsset)
		readerErr        error
		wantSafe         bool
		wantStatus       capabilityspec.ScanState
		wantContinuation bool
	}{
		{
			name: "current complete no finding",
			result: processingRuntimeMalwareResult(now, bundleFingerprint, capabilityspec.ScanNoFinding,
				capabilityspec.CoverageComplete, asset.Size),
			wantSafe: true, wantStatus: capabilityspec.ScanNoFinding,
		},
		{
			name: "finding",
			result: processingRuntimeMalwareResult(now, bundleFingerprint, capabilityspec.ScanFinding,
				capabilityspec.CoverageComplete, asset.Size),
			wantStatus: capabilityspec.ScanFinding,
		},
		{
			name: "partial",
			result: processingRuntimeMalwareResult(now, bundleFingerprint, capabilityspec.ScanNoFinding,
				capabilityspec.CoveragePartial, asset.Size),
			wantStatus: capabilityspec.ScanStale, wantContinuation: true,
		},
		{
			name: "wrong signature bundle",
			result: processingRuntimeMalwareResult(now, strings.Repeat("b", 64), capabilityspec.ScanNoFinding,
				capabilityspec.CoverageComplete, asset.Size),
			wantStatus: capabilityspec.ScanStale, wantContinuation: true,
		},
		{
			name: "wrong scanned byte count",
			result: processingRuntimeMalwareResult(now, bundleFingerprint, capabilityspec.ScanNoFinding,
				capabilityspec.CoverageComplete, asset.Size-1),
			wantStatus: capabilityspec.ScanStale, wantContinuation: true,
		},
		{
			name: "exact source mismatch",
			result: processingRuntimeMalwareResult(now, bundleFingerprint, capabilityspec.ScanNoFinding,
				capabilityspec.CoverageComplete, asset.Size),
			mutateAsset: func(value *content.AuthorizedAsset) {
				value.SourceFingerprint = "changed-source"
			},
			wantStatus: capabilityspec.ScanNotScanned, wantContinuation: true,
		},
		{
			name: "unreadable evidence",
			result: processingRuntimeMalwareResult(now, bundleFingerprint, capabilityspec.ScanNoFinding,
				capabilityspec.CoverageComplete, asset.Size),
			readerErr:  processing.ErrDerivedTamper,
			wantStatus: capabilityspec.ScanStale, wantContinuation: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			db := openProcessingRuntimeTestDB(t)
			pipeline := seedProcessingRuntimeMalwareEvidence(t, db, now, asset, bundleFingerprint)
			payload, err := json.Marshal(testCase.result)
			if err != nil {
				t.Fatal(err)
			}
			reader := &processingRuntimeMalwareEvidenceReaderFake{payload: payload, err: testCase.readerErr}
			requester := &processingRuntimeWorkRequesterFake{}
			runtime := &managedProcessingRuntime{
				db: db, now: func() time.Time { return now },
				malwareEvidence: reader, malwareWork: requester,
			}
			currentAsset := asset
			if testCase.mutateAsset != nil {
				testCase.mutateAsset(&currentAsset)
			}
			decision, err := runtime.malwareSafetyForAsset(context.Background(), currentAsset)
			if err != nil {
				t.Fatal(err)
			}
			if !decision.Active || decision.Safe != testCase.wantSafe || decision.Status != testCase.wantStatus {
				t.Fatalf("decision=%+v want safe=%v status=%s", decision, testCase.wantSafe, testCase.wantStatus)
			}
			if got := len(requester.requests); (got == 1) != testCase.wantContinuation {
				t.Fatalf("continuation requests=%d want=%v", got, testCase.wantContinuation)
			}
			if testCase.wantContinuation {
				request := requester.requests[0]
				if request.Descriptor.Source != currentAsset.Ref ||
					request.Descriptor.CatalogGenerationID != currentAsset.CatalogGenerationID ||
					request.Descriptor.SourceFingerprint != currentAsset.SourceFingerprint ||
					request.Descriptor.EntryFingerprint != currentAsset.EntryFingerprint ||
					request.Descriptor.ProviderCapabilityRevision != currentAsset.ProviderCapabilityRevision ||
					request.Descriptor.PipelineFingerprint != pipeline ||
					request.Descriptor.Capability != capabilityspec.CapabilityMalwareScan ||
					request.Interest.OwnerKind != processing.InterestSystem {
					t.Fatalf("unsafe malware continuation=%+v", request)
				}
			}
		})
	}
}

func TestProcessingRuntimeMalwareSafetyWithoutActivePipelineIsNotDeployedAndQuiet(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	reader := &processingRuntimeMalwareEvidenceReaderFake{}
	requester := &processingRuntimeWorkRequesterFake{}
	runtime := &managedProcessingRuntime{db: db, malwareEvidence: reader, malwareWork: requester}
	decision, err := runtime.malwareSafetyForAsset(context.Background(), content.AuthorizedAsset{
		Ref: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("1", 32),
			EntryID:         strings.Repeat("2", 64),
		},
		CatalogGenerationID: strings.Repeat("3", 32), Provider: backupasset.ProviderRsync,
		ProviderCapabilityRevision: 1, SourceFingerprint: "source-v1", EntryFingerprint: "entry-v1",
		FingerprintStrength: "strong", Size: 1, MediaType: "image/png",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Active || decision.Safe || decision.Status != capabilityspec.ScanNotScanned ||
		reader.calls != 0 || len(requester.requests) != 0 {
		t.Fatalf("inactive malware pipeline decision=%+v reads=%d work=%d", decision, reader.calls, len(requester.requests))
	}
}

func TestProcessingRuntimeMalwareSafetyRejectsMultipleActivePublications(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	now := time.Date(2026, 7, 21, 2, 30, 0, 0, time.UTC)
	asset := content.AuthorizedAsset{
		Ref: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("1", 32),
			EntryID:         strings.Repeat("2", 64),
		},
		CatalogGenerationID:        strings.Repeat("3", 32),
		Provider:                   backupasset.ProviderRestic,
		ProviderCapabilityRevision: 7,
		SourceFingerprint:          "source-fingerprint-v1",
		EntryFingerprint:           strings.Repeat("4", 64),
		FingerprintStrength:        "strong",
		Size:                       4096,
		MediaType:                  "application/pdf",
	}
	seedProcessingRuntimeMalwareEvidence(t, db, now, asset, strings.Repeat("a", 64))
	duplicateProcessingRuntimeMalwarePublication(t, db, now)
	runtime := &managedProcessingRuntime{
		db: db, now: func() time.Time { return now },
		malwareEvidence: &processingRuntimeMalwareEvidenceReaderFake{},
		malwareWork:     &processingRuntimeWorkRequesterFake{},
	}
	if _, err := runtime.malwareSafetyForAsset(context.Background(), asset); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("multiple malware publications error=%v, want conflict", err)
	}
}

func TestProcessingRuntimeSecretContinuationIsDefaultOffCompleteOnlyAndIdempotent(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	lease := processingRuntimeLease(t, db)
	coordinator, err := processing.NewCoordinator(db, lease, func() time.Time { return now }, processing.CoordinatorConfig{
		QueueMax: 100, InteractiveReservedSlots: 1, BackgroundSlots: 1,
		PullLease: time.Minute, AttemptTimeout: 5 * time.Minute, RetryMax: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	seedProcessingRuntimeSecretContinuation(t, db, now)
	runtime := &managedProcessingRuntime{
		db: db, coordinator: coordinator, now: func() time.Time { return now },
		config: backupasset.ProcessingConfig{SecretClassify: false},
	}
	type secretContinuationScheduler interface {
		scheduleSecretContinuations(context.Context) (int, error)
	}
	scheduler, ok := any(runtime).(secretContinuationScheduler)
	if !ok {
		t.Fatal("processing runtime has no closed secret classification continuation")
	}
	if scheduled, err := scheduler.scheduleSecretContinuations(context.Background()); err != nil || scheduled != 0 {
		t.Fatalf("default-off secret continuations=%d err=%v", scheduled, err)
	}
	runtime.config.SecretClassify = true
	if err := runtime.reconcile(context.Background()); err != nil {
		t.Fatalf("enabled secret continuation reconcile: %v", err)
	}
	if scheduled, err := scheduler.scheduleSecretContinuations(context.Background()); err != nil || scheduled != 0 {
		t.Fatalf("replayed secret continuations=%d err=%v", scheduled, err)
	}
	var jobs []model.BackupAssetProcessingJob
	if err := db.Where("capability = ?", capabilityspec.CapabilitySecretClassify).Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].EntryFingerprint != strings.Repeat("6", 64) ||
		jobs[0].SourceFingerprint != "secret-continuation-source" || jobs[0].SecurityPolicyRevision != processingSecurityPolicyRevision {
		t.Fatalf("secret continuation jobs=%+v", jobs)
	}
}

func TestProcessingRuntimeUpdaterCommitsMetadataAndPipelineRevisionsAtomically(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupAssetUpdaterMetadata{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	for key, value := range map[string]string{
		"backup_assets.enabled":                "true",
		"backup_assets.worker_updater_enabled": "true",
	} {
		if err := settingsService.Update(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := settingsService.AdvanceProcessingPipelineRevisionsTx(context.Background(), tx, true, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	metrics := &processingRuntimeMetricsFake{}
	invalidation := &processingRuntimeInvalidationFake{}
	runtime := &managedProcessingRuntime{
		db: db, foundation: backupasset.NewFoundationService(settingsService), settings: settingsService,
		metrics: metrics, invalidation: invalidation, now: func() time.Time { return now },
	}
	identity := processingupdater.UpdaterTransportIdentity{
		Fingerprint: strings.Repeat("a", 64), PeerPID: 42, PeerUID: 10002, PeerGID: 10002,
	}
	receipt := processingupdater.InboxReceipt{
		SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "offline-2026-07", Version: "1.2.3",
		ManifestDigest: strings.Repeat("b", 64), SigningKeyFingerprint: strings.Repeat("c", 64),
		BundleFingerprint: strings.Repeat("d", 64), BundleSHA256: strings.Repeat("e", 64), VerifiedAt: now,
		Capabilities: []processingupdater.ManifestCapability{{
			Capability: "text.extract", Schema: "text.extract.v1", Profiles: []string{"bounded_text_v1"},
			ToolRevision: "tool-v1", ModelRevision: "builtin-v1", DataRevision: "builtin-v1",
		}},
	}
	registered, err := runtime.RegisterUpdaterCandidate(context.Background(), identity, processingupdater.RegisterCandidateRequest{
		SchemaVersion: 1, Receipt: receipt,
	})
	if err != nil || backupasset.ValidateOpaqueID(registered.CandidateID) != nil {
		t.Fatalf("registered=%+v err=%v", registered, err)
	}
	if err := runtime.ActivateProcessingUpdaterCandidate(context.Background(), ProcessingUpdaterActivationRequest{
		CandidateID: registered.CandidateID,
	}); err != nil {
		t.Fatal(err)
	}
	pulled, err := runtime.PullUpdaterActivation(context.Background(), identity, processingupdater.PullActivationRequest{SchemaVersion: 1})
	if err != nil || pulled.Directive == nil || pulled.Directive.CandidateID != registered.CandidateID ||
		pulled.Directive.ExpectedOldFingerprint != "" || pulled.Directive.NewFingerprint != receipt.BundleFingerprint {
		t.Fatalf("pulled=%+v err=%v", pulled, err)
	}
	reported, err := runtime.ReportUpdaterActivation(context.Background(), identity, processingupdater.ActivationReportRequest{
		SchemaVersion: 1,
		Receipt: processingupdater.ActivationReceipt{
			SchemaVersion: 1, CandidateID: registered.CandidateID, NewFingerprint: receipt.BundleFingerprint, State: "swapped",
		},
	})
	if err != nil || reported.Decision != processingupdater.ActivationDecisionCommit || reported.ActiveFingerprint != receipt.BundleFingerprint {
		t.Fatalf("reported=%+v err=%v", reported, err)
	}
	if len(metrics.activationOutcomes) != 1 || metrics.activationOutcomes[0] != processing.UpdaterActivationCommit {
		t.Fatalf("activation metrics=%v", metrics.activationOutcomes)
	}
	if invalidation.calls != 1 || len(invalidation.last.Targets) != len(capabilityspec.WorkerProfiles()) ||
		invalidation.last.BatchSize <= 0 || invalidation.last.RequeuePriority != 900 {
		t.Fatalf("activation invalidation=%+v calls=%d", invalidation.last, invalidation.calls)
	}
	var metadata model.BackupAssetUpdaterMetadata
	if err := db.First(&metadata, "id = ?", registered.CandidateID).Error; err != nil || metadata.State != "active" || metadata.ActivatedAt == nil {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	revisions, err := settingsService.ProcessingPipelineRevisions(context.Background())
	if err != nil || revisions.Content != 2 || revisions.OCR != 1 {
		t.Fatalf("pipeline revisions=%+v err=%v", revisions, err)
	}
}

func TestValidRuntimeUpdaterIdentityAcceptsNamespaceHiddenPID(t *testing.T) {
	identity := processingupdater.UpdaterTransportIdentity{
		Fingerprint: strings.Repeat("a", 64), PeerPID: 0, PeerUID: 10002, PeerGID: 10002,
	}
	if !validRuntimeUpdaterIdentity(identity) {
		t.Fatal("namespace-hidden peer PID was rejected")
	}
	identity.PeerPID = -1
	if validRuntimeUpdaterIdentity(identity) {
		t.Fatal("negative peer PID was accepted")
	}
	if validRuntimeUpdaterIdentity(processingupdater.UpdaterTransportIdentity{}) {
		t.Fatal("missing updater identity was accepted")
	}
}

func TestProcessingRuntimeUpdaterReceiptReplayRestoresImpactAndCandidateChanges(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupAssetUpdaterMetadata{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	for key, value := range map[string]string{
		"backup_assets.enabled":                "true",
		"backup_assets.worker_updater_enabled": "true",
	} {
		if err := settingsService.Update(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := settingsService.AdvanceProcessingPipelineRevisionsTx(context.Background(), tx, true, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before, err := settingsService.ProcessingPipelineRevisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	identity := processingupdater.UpdaterTransportIdentity{Fingerprint: strings.Repeat("a", 64), PeerPID: 42}
	receipt := processingupdater.InboxReceipt{
		SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "offline-replay", Version: "1.2.3",
		ManifestDigest: strings.Repeat("b", 64), SigningKeyFingerprint: strings.Repeat("c", 64),
		BundleFingerprint: strings.Repeat("d", 64), BundleSHA256: strings.Repeat("e", 64), VerifiedAt: now,
		Capabilities: []processingupdater.ManifestCapability{{
			Capability: capabilityspec.CapabilityTextExtract, Schema: "text.extract.v1",
			Profiles: []string{capabilityspec.ProfileBoundedTextV1}, ToolRevision: "tool-v2",
			ModelRevision: "builtin-v1", DataRevision: "builtin-v1",
		}},
	}
	first := &managedProcessingRuntime{
		db: db, foundation: backupasset.NewFoundationService(settingsService), settings: settingsService,
		now: func() time.Time { return now },
	}
	registered, err := first.RegisterUpdaterCandidate(context.Background(), identity, processingupdater.RegisterCandidateRequest{
		SchemaVersion: 1, Receipt: receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := first.ProcessingUpdaterCandidates(context.Background())
	if err != nil || len(candidates) != 1 || len(candidates[0].CapabilityChanges) != 1 ||
		candidates[0].CapabilityChanges[0].Capability != capabilityspec.CapabilityTextExtract ||
		!reflect.DeepEqual(candidates[0].CapabilityChanges[0].Profiles, []string{capabilityspec.ProfileBoundedTextV1}) {
		t.Fatalf("sanitized candidate changes=%+v err=%v", candidates, err)
	}

	restarted := &managedProcessingRuntime{
		db: db, foundation: backupasset.NewFoundationService(settingsService), settings: settingsService,
		now: func() time.Time { return now.Add(time.Minute) },
	}
	replayed, err := restarted.RegisterUpdaterCandidate(context.Background(), identity, processingupdater.RegisterCandidateRequest{
		SchemaVersion: 1, Receipt: receipt,
	})
	if err != nil || replayed.CandidateID != registered.CandidateID {
		t.Fatalf("replayed=%+v registered=%+v err=%v", replayed, registered, err)
	}
	if err := restarted.ActivateProcessingUpdaterCandidate(context.Background(), ProcessingUpdaterActivationRequest{
		CandidateID: registered.CandidateID,
	}); err != nil {
		t.Fatal(err)
	}
	reported, err := restarted.ReportUpdaterActivation(context.Background(), identity, processingupdater.ActivationReportRequest{
		SchemaVersion: 1, Receipt: processingupdater.ActivationReceipt{
			SchemaVersion: 1, CandidateID: registered.CandidateID, NewFingerprint: receipt.BundleFingerprint, State: "swapped",
		},
	})
	if err != nil || reported.Decision != processingupdater.ActivationDecisionCommit {
		t.Fatalf("replayed activation result=%+v err=%v", reported, err)
	}
	after, err := settingsService.ProcessingPipelineRevisions(context.Background())
	if err != nil || after.Content != before.Content+1 || after.OCR != before.OCR {
		t.Fatalf("replayed impact revisions before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestProcessingRuntimeUpdaterActivationAdmissionSurvivesCoreRestart(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupAssetUpdaterMetadata{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	for key, value := range map[string]string{
		"backup_assets.enabled":                "true",
		"backup_assets.worker_updater_enabled": "true",
	} {
		if err := settingsService.Update(key, value); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	identity := processingupdater.UpdaterTransportIdentity{Fingerprint: strings.Repeat("a", 64), PeerPID: 42}
	receipt := processingupdater.InboxReceipt{
		SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "offline-admission", Version: "1.2.3",
		ManifestDigest: strings.Repeat("b", 64), SigningKeyFingerprint: strings.Repeat("c", 64),
		BundleFingerprint: strings.Repeat("d", 64), BundleSHA256: strings.Repeat("e", 64), VerifiedAt: now,
		Capabilities: []processingupdater.ManifestCapability{{
			Capability: capabilityspec.CapabilityTextExtract, Schema: "text.extract.v1",
			Profiles: []string{capabilityspec.ProfileBoundedTextV1}, ToolRevision: "tool-v2",
			ModelRevision: "builtin-v1", DataRevision: "builtin-v1",
		}},
	}
	first := &managedProcessingRuntime{
		db: db, foundation: backupasset.NewFoundationService(settingsService), settings: settingsService,
		now: func() time.Time { return now },
	}
	registered, err := first.RegisterUpdaterCandidate(context.Background(), identity, processingupdater.RegisterCandidateRequest{
		SchemaVersion: 1, Receipt: receipt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ActivateProcessingUpdaterCandidate(context.Background(), ProcessingUpdaterActivationRequest{
		CandidateID: registered.CandidateID,
	}); err != nil {
		t.Fatal(err)
	}
	restarted := &managedProcessingRuntime{
		db: db, foundation: backupasset.NewFoundationService(settingsService), settings: settingsService,
		now: func() time.Time { return now.Add(time.Minute) },
	}
	if err := restarted.ActivateProcessingUpdaterCandidate(context.Background(), ProcessingUpdaterActivationRequest{
		CandidateID: registered.CandidateID,
	}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("restart activation admission error=%v, want conflict", err)
	}
}

func TestProcessingRuntimeUpdaterReturnsRollbackWhenCoreTransactionCannotCommit(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupAssetUpdaterMetadata{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	for key, value := range map[string]string{
		"backup_assets.enabled":                "true",
		"backup_assets.worker_updater_enabled": "true",
	} {
		if err := settingsService.Update(key, value); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	candidateID := strings.Repeat("1", 32)
	fingerprint := strings.Repeat("2", 64)
	if err := db.Create(&model.BackupAssetUpdaterMetadata{
		ID: candidateID, SourceKind: "admin_registered", SourceID: "offline", Version: "1.0.0",
		ManifestDigest: strings.Repeat("3", 64), SigningKeyFingerprint: strings.Repeat("4", 64),
		BundleFingerprint: fingerprint, State: "verified", VerifiedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	runtime := &managedProcessingRuntime{
		db: db, foundation: backupasset.NewFoundationService(settingsService), now: func() time.Time { return now },
		pendingUpdaterActivation: &ProcessingUpdaterActivationRequest{CandidateID: candidateID},
	}
	identity := processingupdater.UpdaterTransportIdentity{Fingerprint: strings.Repeat("a", 64), PeerPID: 42}
	result, err := runtime.ReportUpdaterActivation(context.Background(), identity, processingupdater.ActivationReportRequest{
		SchemaVersion: 1, Receipt: processingupdater.ActivationReceipt{
			SchemaVersion: 1, CandidateID: candidateID, NewFingerprint: fingerprint, State: "swapped",
		},
	})
	if err != nil || result.Decision != processingupdater.ActivationDecisionRollback || result.ActiveFingerprint != "" {
		t.Fatalf("rollback=%+v err=%v", result, err)
	}
	var metadata model.BackupAssetUpdaterMetadata
	if err := db.First(&metadata, "id = ?", candidateID).Error; err != nil || metadata.State != "verified" || metadata.ActivatedAt != nil {
		t.Fatalf("rollback metadata=%+v err=%v", metadata, err)
	}
}

func TestProcessingRuntimeUpdaterReacknowledgesCommittedActivationAfterCoreRestart(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupAssetUpdaterMetadata{}); err != nil {
		t.Fatal(err)
	}
	settingsService := settings.NewService(db)
	for key, value := range map[string]string{
		"backup_assets.enabled":                "true",
		"backup_assets.worker_updater_enabled": "true",
	} {
		if err := settingsService.Update(key, value); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	candidateID := strings.Repeat("1", 32)
	fingerprint := strings.Repeat("2", 64)
	if err := db.Create(&model.BackupAssetUpdaterMetadata{
		ID: candidateID, SourceKind: "admin_registered", SourceID: "offline", Version: "1.0.0",
		ManifestDigest: strings.Repeat("3", 64), SigningKeyFingerprint: strings.Repeat("4", 64),
		BundleFingerprint: fingerprint, State: "active", VerifiedAt: &now, ActivatedAt: &now,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	restarted := &managedProcessingRuntime{
		db: db, foundation: backupasset.NewFoundationService(settingsService), settings: settingsService,
		now: func() time.Time { return now.Add(time.Minute) },
	}
	identity := processingupdater.UpdaterTransportIdentity{Fingerprint: strings.Repeat("a", 64), PeerPID: 42}
	result, err := restarted.ReportUpdaterActivation(context.Background(), identity, processingupdater.ActivationReportRequest{
		SchemaVersion: 1, Receipt: processingupdater.ActivationReceipt{
			SchemaVersion: 1, CandidateID: candidateID, NewFingerprint: fingerprint, State: "swapped",
		},
	})
	if err != nil || result.Decision != processingupdater.ActivationDecisionCommit || result.ActiveFingerprint != fingerprint {
		t.Fatalf("restart acknowledgement=%+v err=%v", result, err)
	}
}

func TestProcessingRuntimeUpdaterScanRequestIsDeliveredOnce(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	settingsService := settings.NewService(db)
	for key, value := range map[string]string{
		"backup_assets.enabled":                "true",
		"backup_assets.worker_updater_enabled": "true",
	} {
		if err := settingsService.Update(key, value); err != nil {
			t.Fatal(err)
		}
	}
	runtime := &managedProcessingRuntime{
		db: db, foundation: backupasset.NewFoundationService(settingsService), settings: settingsService,
		now: func() time.Time { return time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC) },
	}
	if err := runtime.RequestProcessingUpdaterScan(context.Background()); err != nil {
		t.Fatal(err)
	}
	identity := processingupdater.UpdaterTransportIdentity{Fingerprint: strings.Repeat("a", 64), PeerPID: 42}
	first, err := runtime.PullUpdaterActivation(context.Background(), identity, processingupdater.PullActivationRequest{SchemaVersion: 1})
	if err != nil || !first.ScanRequested {
		t.Fatalf("first pull=%+v err=%v", first, err)
	}
	second, err := runtime.PullUpdaterActivation(context.Background(), identity, processingupdater.PullActivationRequest{SchemaVersion: 1})
	if err != nil || second.ScanRequested {
		t.Fatalf("second pull=%+v err=%v", second, err)
	}
}

func TestProcessingRuntimeConfiguredBuildsOneEmptyCapabilityGraph(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	root := filepath.Join(t.TempDir(), "derived")
	foundation := processingRuntimeFoundation(t, db, root, true, true)
	keyring := backupasset.NewKeyring(db, time.Now)
	lease := processingRuntimeLease(t, db)
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: foundation, Keyring: keyring, Lease: lease,
		Source: processingRuntimeSourceFake{}, Authorize: processingRuntimeAssetAuthorizerFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{},
	})
	if err != nil {
		t.Fatalf("newProcessingRuntime: %v", err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if runtime.coordinator == nil || runtime.grants == nil || runtime.attemptBroker == nil || runtime.store == nil ||
		runtime.lifecycle == nil || runtime.sink == nil || runtime.reconciler == nil || runtime.derivedReconciler == nil || runtime.protocol == nil ||
		runtime.workerProtocol == nil || runtime.capabilityService == nil || runtime.coverageService == nil {
		t.Fatal("configured runtime omitted a processing graph component")
	}
	if runtime.keyring != keyring || runtime.lease != lease {
		t.Fatal("processing runtime duplicated the shared keyring or lease service")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("configured runtime did not create Derived root: %v", err)
	}
	if _, err := keyring.Active(context.Background(), backupasset.KeyDomainDerivedStore); err != nil {
		t.Fatalf("configured runtime did not create Derived key: %v", err)
	}

	identity := processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("a", 64), PeerUID: uint32(os.Geteuid()),
	}
	protocolPort := runtime.WorkerProtocol()
	_, err = protocolPort.Handshake(context.Background(), identity, processing.HandshakeRequest{
		SchemaVersion: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		InstanceID: strings.Repeat("b", 32), IdentityRevision: 1, InteractiveSlots: 1,
		Capabilities: []processing.CapabilityAdvertisement{{
			SchemaVersion: 1, Capability: "thumbnail", CapabilitySchema: "thumbnail.v1",
			PipelineFingerprint: "test-pipeline", OutputProfile: "preview",
			InputModes: []processing.ProtocolInputMode{processing.ProtocolInputStat},
			Limits: processing.ProtocolCapabilityLimits{
				MaxInputBytes: 64 * 1024, MaxOutputBytes: 1, MaxOutputCount: 1,
				MaxPages: 1, MaxPixels: 1, MaxDurationMillis: 1, MaxExpandedBytes: 1,
			},
		}},
	})
	if !errors.Is(err, processing.ErrProtocolCapabilityUnsupported) {
		t.Fatalf("production runtime capability registry error=%v, want unsupported", err)
	}
	summary, err := runtime.AdminSummary(context.Background())
	if err != nil {
		t.Fatalf("AdminSummary: %v", err)
	}
	if !summary.Configured || !summary.LocalEnabled || summary.RemoteEnabled {
		t.Fatalf("configured runtime summary=%+v", summary)
	}
	runtime.StopAccepting()
	if _, err := protocolPort.Handshake(context.Background(), identity, processing.HandshakeRequest{
		SchemaVersion: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		InstanceID: strings.Repeat("b", 32), IdentityRevision: 1, InteractiveSlots: 1,
	}); !errors.Is(err, processing.ErrProtocolUnavailable) {
		t.Fatalf("captured Worker protocol admitted a handshake after runtime drain: %v", err)
	}
}

func TestProcessingRuntimeRegistersClosedCapabilitiesOnlyForActiveBundle(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupAssetUpdaterMetadata{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	fingerprint := strings.Repeat("d", 64)
	if err := db.Create(&model.BackupAssetUpdaterMetadata{
		ID: strings.Repeat("1", 32), SourceKind: "builtin", SourceID: "builtin-2026-07", Version: "1.0.0",
		ManifestDigest: strings.Repeat("2", 64), SigningKeyFingerprint: strings.Repeat("3", 64),
		BundleFingerprint: fingerprint, State: "active", VerifiedAt: &now, ActivatedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	foundation := processingRuntimeFoundation(t, db, filepath.Join(t.TempDir(), "derived"), true, true)
	settingsService := settings.NewService(db)
	if err := settingsService.Update("backup_assets.worker_updater_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: foundation, Settings: settingsService,
		Keyring: backupasset.NewKeyring(db, time.Now), Lease: processingRuntimeLease(t, db),
		Source: processingRuntimeSourceFake{}, Authorize: processingRuntimeAssetAuthorizerFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	bundles := processingRuntimeBundleFingerprints(fingerprint)
	worker, err := processing.NewProductionWorkerCapabilitySetWithBundles(bundles)
	if err != nil {
		t.Fatal(err)
	}
	request := processing.HandshakeRequest{
		SchemaVersion: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		InstanceID: strings.Repeat("4", 32), IdentityRevision: 1, InteractiveSlots: 1,
		Capabilities: worker.Advertisements(),
	}
	identity := processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: strings.Repeat("5", 64), PeerUID: uint32(os.Geteuid()),
	}
	protocolPort := runtime.WorkerProtocol()
	if _, err := protocolPort.Handshake(context.Background(), identity, request); err != nil {
		t.Fatalf("active-bundle handshake: %v", err)
	}
	request.InstanceID = strings.Repeat("6", 32)
	request.Capabilities = processing.NewProductionWorkerCapabilitySet().Advertisements()
	if _, err := protocolPort.Handshake(context.Background(), identity, request); !errors.Is(err, processing.ErrProtocolCapabilityUnsupported) {
		t.Fatalf("unbound advertisement error=%v, want unsupported", err)
	}

	newFingerprint := strings.Repeat("e", 64)
	updaterIdentity := processingupdater.UpdaterTransportIdentity{Fingerprint: strings.Repeat("a", 64), PeerPID: 42}
	registered, err := runtime.RegisterUpdaterCandidate(context.Background(), updaterIdentity, processingupdater.RegisterCandidateRequest{
		SchemaVersion: 1,
		Receipt: processingupdater.InboxReceipt{
			SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "offline-2026-08", Version: "1.1.0",
			ManifestDigest: strings.Repeat("7", 64), SigningKeyFingerprint: strings.Repeat("8", 64),
			BundleFingerprint: newFingerprint, BundleSHA256: strings.Repeat("9", 64), VerifiedAt: now,
			Capabilities: []processingupdater.ManifestCapability{{
				Capability: capabilityspec.CapabilityTextExtract, Schema: "text.extract.v1",
				Profiles: []string{capabilityspec.ProfileBoundedTextV1}, ToolRevision: "tool-v2",
				ModelRevision: "builtin-v1", DataRevision: "builtin-v1",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ActivateProcessingUpdaterCandidate(context.Background(), ProcessingUpdaterActivationRequest{
		CandidateID: registered.CandidateID, ExpectedActiveFingerprint: &fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := runtime.ReportUpdaterActivation(context.Background(), updaterIdentity, processingupdater.ActivationReportRequest{
		SchemaVersion: 1,
		Receipt: processingupdater.ActivationReceipt{
			SchemaVersion: 1, CandidateID: registered.CandidateID, OldFingerprint: fingerprint,
			NewFingerprint: newFingerprint, State: "swapped",
		},
	})
	if err != nil || result.Decision != processingupdater.ActivationDecisionCommit {
		t.Fatalf("activation result=%+v err=%v", result, err)
	}
	newWorker, err := processing.NewProductionWorkerCapabilitySetWithBundles(processingRuntimeBundleFingerprints(newFingerprint))
	if err != nil {
		t.Fatal(err)
	}
	request.InstanceID = strings.Repeat("a", 32)
	request.Capabilities = newWorker.Advertisements()
	identity.Fingerprint = strings.Repeat("b", 64)
	if runtime.WorkerProtocol() != protocolPort {
		t.Fatal("updater activation replaced the protocol port held by the Worker HTTP router")
	}
	if _, err := protocolPort.Handshake(context.Background(), identity, request); err != nil {
		t.Fatalf("new active-bundle handshake: %v", err)
	}
	request.InstanceID = strings.Repeat("c", 32)
	request.Capabilities = worker.Advertisements()
	identity.Fingerprint = strings.Repeat("c", 64)
	if _, err := protocolPort.Handshake(context.Background(), identity, request); !errors.Is(err, processing.ErrProtocolCapabilityUnsupported) {
		t.Fatalf("superseded bundle advertisement error=%v, want unsupported", err)
	}
}

func TestProcessingRuntimeTerminalPollDoesNotConsumeAcrossUpdaterActivation(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	now := time.Date(2026, 7, 21, 4, 0, 0, 0, time.UTC)
	asset := content.AuthorizedAsset{
		Ref: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("1", 32),
			EntryID:         strings.Repeat("2", 64),
		},
		CatalogGenerationID: strings.Repeat("3", 32), Provider: backupasset.ProviderRestic,
		ProviderCapabilityRevision: 7, SourceFingerprint: "runtime-terminal-source-v1",
		EntryFingerprint: strings.Repeat("4", 64), FingerprintStrength: "strong",
		Size: 4096, MediaType: "image/png",
	}
	oldBundle := strings.Repeat("a", 64)
	seedProcessingRuntimeMalwareEvidence(t, db, now, asset, oldBundle)
	malwarePayload, err := json.Marshal(processingRuntimeMalwareResult(
		now, oldBundle, capabilityspec.ScanNoFinding, capabilityspec.CoverageComplete, asset.Size,
	))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, filepath.Join(t.TempDir(), "derived"), true, false),
		Keyring: backupasset.NewKeyring(db, func() time.Time { return now }), Lease: processingRuntimeLease(t, db),
		Source: processingRuntimeSourceFake{}, Authorize: processingRuntimeAssetAuthorizerFake{asset: asset},
		ValidateRoot: processingRuntimeRootValidator, RevalidateSource: processingRuntimeSourceRevalidatorFake{},
		Projection: processingRuntimeProjectionFake{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	runtime.malwareEvidence = &processingRuntimeMalwareEvidenceReaderFake{payload: malwarePayload}
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1, false,
	)
	if !ok {
		t.Fatal("closed thumbnail profile unavailable")
	}
	oldPipeline, err := runtime.activePipelineFingerprint(context.Background(), profile.Capability, profile.OutputProfile)
	if err != nil || oldPipeline == "" {
		t.Fatalf("active thumbnail pipeline=%q err=%v", oldPipeline, err)
	}
	workerID := strings.Repeat("5", 32)
	if err := db.Create(&model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: "local", TransportFingerprint: strings.Repeat("5", 64),
		InstanceID: strings.Repeat("6", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", InteractiveSlots: 1, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetWorkerCapability{
		ID: strings.Repeat("7", 32), WorkerID: workerID, Capability: profile.Capability,
		CapabilitySchema: profile.CapabilitySchema, PipelineFingerprint: oldPipeline, OutputProfile: profile.OutputProfile,
		InputModes: "stat,sequential,range", LimitsCanonical: []byte{1}, AdvertisementDigest: strings.Repeat("8", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	actor := content.DeliveryActor{UserID: 107, Role: "operator"}
	created, err := runtime.RequestPreview(context.Background(), processing.PreviewJobRequest{
		Actor: actor, Ref: asset.Ref, Representation: processing.PreviewThumbnail,
	})
	if err != nil || created.State != processing.ProcessingProductQueued {
		t.Fatalf("queued runtime preview=%+v err=%v", created, err)
	}
	var interest model.BackupAssetProcessingInterest
	if err := db.First(&interest, "id = ?", created.JobID).Error; err != nil {
		t.Fatal(err)
	}
	setID := strings.Repeat("b", 32)
	attemptID := strings.Repeat("c", 32)
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", interest.JobID).Updates(map[string]any{
		"state": string(processing.ProcessingSucceeded), "current_artifact_set_id": setID,
		"current_attempt_id": attemptID, "transition_revision": 2, "is_current": false,
		"finished_at": &now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetProcessingAttempt{
		ID: attemptID, JobID: interest.JobID, AttemptNumber: 1, WorkerID: workerID, SlotClass: "interactive",
		State: "succeeded", WorkerLeaseExpiresAt: now.Add(time.Minute), LastHeartbeatAt: now,
		RecoveryPointLeaseID: strings.Repeat("d", 32), RecoveryPointAttemptID: strings.Repeat("e", 32),
		RecoveryPointFenceHash: strings.Repeat("f", 64), AbsoluteDeadline: now.Add(time.Hour), IsCurrent: false,
		StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", attemptID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetProcessingJob
	if err := db.First(&job, "id = ?", interest.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: job.ID, AttemptID: attemptID, WorkKey: job.WorkKey,
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID,
		EntryID: asset.Ref.EntryID, SourceFingerprint: asset.SourceFingerprint,
		SecurityPolicyRevision: processingSecurityPolicyRevision, ManifestDigest: strings.Repeat("1", 64),
		State: "active", Completeness: "complete", ArtifactCount: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	callbackName := "test:terminal-poll-updater-activation"
	activated := false
	publicationReads := 0
	if err := db.Callback().Row().Before("gorm:row").Register(callbackName, func(tx *gorm.DB) {
		if activated || tx.Statement.Table != "sets" {
			return
		}
		publicationReads++
		if publicationReads != 1 {
			return
		}
		newBundle := strings.Repeat("9", 64)
		mutationDB := tx.Session(&gorm.Session{NewDB: true})
		mutationErr := mutationDB.Transaction(func(updateTx *gorm.DB) error {
			if err := updateTx.Model(&model.BackupAssetUpdaterMetadata{}).Where("state = ?", "active").
				Update("state", "superseded").Error; err != nil {
				return err
			}
			return updateTx.Create(&model.BackupAssetUpdaterMetadata{
				ID: strings.Repeat("0", 32), SourceKind: "builtin", SourceID: "runtime-terminal-next",
				Version: "2.0.0", ManifestDigest: strings.Repeat("2", 64), SigningKeyFingerprint: strings.Repeat("3", 64),
				BundleFingerprint: newBundle, State: "active", VerifiedAt: &now, ActivatedAt: &now,
				CreatedAt: now, UpdatedAt: now.Add(time.Second),
			}).Error
		})
		if mutationErr != nil {
			t.Errorf("activate updater between terminal identity reads: %v", mutationErr)
			return
		}
		activated = true
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Row().Remove(callbackName) })

	product, err := runtime.PollPreview(context.Background(), processing.PreviewJobLookup{
		Actor: actor, Ref: asset.Ref, JobID: created.JobID,
	})
	if err == nil || product.State == processing.ProcessingProductDerived {
		t.Fatalf("updater-raced terminal product=%+v err=%v", product, err)
	}
	if !activated {
		t.Fatal("updater activation race was not exercised")
	}
	if err := db.First(&interest, "id = ?", interest.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !interest.Active || interest.RemovedReason != "" || interest.RemovedAt != nil {
		t.Fatalf("updater activation consumed terminal interest: %+v", interest)
	}
}

func processingRuntimeBundleFingerprints(fingerprint string) processing.CapabilityBundleFingerprints {
	bundles := make(processing.CapabilityBundleFingerprints)
	for _, profile := range capabilityspec.WorkerProfiles() {
		bundles[profile.Capability] = []string{fingerprint}
	}
	return bundles
}

func TestProcessingRuntimeReadsDerivedArtifactOnlyThroughLifecycleAuthorization(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, filepath.Join(t.TempDir(), "derived"), true, true),
		Keyring: backupasset.NewKeyring(db, func() time.Time { return now }), Lease: processingRuntimeLease(t, db),
		Source: processingRuntimeSourceFake{}, Authorize: processingRuntimeAssetAuthorizerFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	payload := []byte("derived broker payload")
	digest := sha256.Sum256(payload)
	blob, err := runtime.store.PutBlob(context.Background(), processing.DerivedBlobDeclaration{
		PlaintextSize: int64(len(payload)), PlaintextDigest: fmt.Sprintf("%x", digest[:]),
	}, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request := content.DerivedArtifactRead{
		ArtifactID: strings.Repeat("1", 32), RecoveryPointID: strings.Repeat("2", 32),
		CatalogGenerationID: strings.Repeat("3", 32), EntryID: strings.Repeat("4", 64),
		SourceFingerprint: "runtime-derived-source-v1",
	}
	setID := strings.Repeat("5", 32)
	if err := db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: strings.Repeat("6", 32), AttemptID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64),
		RecoveryPointID: request.RecoveryPointID, CatalogGenerationID: request.CatalogGenerationID,
		EntryID: request.EntryID, SourceFingerprint: request.SourceFingerprint,
		SecurityPolicyRevision: processingSecurityPolicyRevision, ManifestDigest: strings.Repeat("9", 64),
		State: "active", Completeness: "complete", ArtifactCount: 1, TotalPlaintextBytes: int64(len(payload)),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedArtifact{
		ID: request.ArtifactID, ArtifactSetID: setID, Role: "content", MediaType: "text/plain",
		PlaintextSize: int64(len(payload)), PlaintextDigest: fmt.Sprintf("%x", digest[:]), Completeness: "complete",
		CoverageCanonical: []byte(`{"schema_version":1}`), BlobID: blob.BlobID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedBlobReference{
		ID: strings.Repeat("a", 32), BlobID: blob.BlobID, ArtifactID: request.ArtifactID,
		RecoveryPointID: request.RecoveryPointID, CatalogGenerationID: request.CatalogGenerationID,
		EntryID: request.EntryID, SourceFingerprint: request.SourceFingerprint, State: "active", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var destination bytes.Buffer
	if err := runtime.ReadDerivedArtifact(context.Background(), request, &destination); err != nil || !bytes.Equal(destination.Bytes(), payload) {
		t.Fatalf("ReadDerivedArtifact payload=%q err=%v", destination.Bytes(), err)
	}
	request.SourceFingerprint = "stale-source"
	if err := runtime.ReadDerivedArtifact(context.Background(), request, io.Discard); !errors.Is(err, processing.ErrDerivedUnauthorized) {
		t.Fatalf("stale Derived authorization error=%v", err)
	}
}

func TestProcessingRuntimeReconcilePublishesBoundedMetrics(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	metrics := &processingRuntimeMetricsFake{}
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, filepath.Join(t.TempDir(), "derived"), true, true),
		Keyring: backupasset.NewKeyring(db, time.Now), Lease: processingRuntimeLease(t, db),
		Source: processingRuntimeSourceFake{}, Authorize: processingRuntimeAssetAuthorizerFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{}, Metrics: metrics,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if metrics.queueCalls == 0 || metrics.workerCalls == 0 || metrics.slotCalls == 0 || metrics.derivedCalls == 0 ||
		metrics.coverageCalls == 0 {
		t.Fatalf("Processing reconcile did not publish aggregate metrics: %+v", metrics)
	}
}

func TestProcessingRuntimePublishMetricsReadsSQLiteQueueAge(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	fixedNow := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	metrics := &processingRuntimeMetricsFake{}
	runtime := &managedProcessingRuntime{
		db: db, metrics: metrics, now: func() time.Time { return fixedNow },
		config: backupasset.ProcessingConfig{
			InteractiveSlots: 2, BackgroundSlots: 2,
			DerivedStore: backupasset.ProcessingDerivedStoreConfig{GlobalMaxBytes: 1024},
		},
	}
	job := model.BackupAssetProcessingJob{
		ID: strings.Repeat("1", 32), WorkKey: strings.Repeat("2", 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{"schema_version":1}`), RecoveryPointID: strings.Repeat("3", 32),
		CatalogGenerationID: strings.Repeat("4", 32), EntryID: strings.Repeat("5", 64), SourceFingerprint: "source",
		ProviderCapabilityRevision: 1, Capability: "noop", CapabilitySchema: "noop.v1", PipelineFingerprint: "pipeline",
		OutputProfile: "noop.v1", SecurityPolicyRevision: "policy", PriorityClass: string(processing.PriorityInteractive),
		EffectivePriority: 100, State: string(processing.ProcessingQueued), TransitionRevision: 1, IsCurrent: true,
		QueuedAt: fixedNow.Add(-90 * time.Second), AbsoluteDeadline: fixedNow.Add(time.Hour),
		CreatedAt: fixedNow.Add(-90 * time.Second), UpdatedAt: fixedNow.Add(-90 * time.Second), Version: 1,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	runtime.publishMetrics(context.Background())
	value := metrics.queueValues[string(processing.PriorityInteractive)+"/"+string(processing.ProcessingQueued)]
	if value.count != 1 || value.age < 89*time.Second || value.age > 91*time.Second {
		t.Fatalf("SQLite queue metrics=%+v, want count=1 age=90s", value)
	}
}

func TestProcessingRuntimeAtomicProjectionFailureLeavesNoPendingStateAndRetrySucceeds(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	now := time.Now().UTC()
	bundleFingerprint := strings.Repeat("d", 64)
	if err := db.Create(&model.BackupAssetUpdaterMetadata{
		ID: strings.Repeat("6", 32), SourceKind: "builtin", SourceID: "runtime-projection-recovery", Version: "1.0.0",
		ManifestDigest: strings.Repeat("7", 64), SigningKeyFingerprint: strings.Repeat("8", 64),
		BundleFingerprint: bundleFingerprint, State: "active", VerifiedAt: &now, ActivatedAt: &now,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	projection := &processingRuntimeRecoveringProjectionFake{}
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, filepath.Join(t.TempDir(), "derived"), true, true),
		Keyring: backupasset.NewKeyring(db, time.Now), Lease: processingRuntimeLease(t, db),
		Source: processingRuntimeSourceFake{}, Authorize: processingRuntimeAssetAuthorizerFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: projection,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	descriptor := processingRuntimeWorkDescriptor()
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityTextExtract,
		capabilityspec.ProfileBoundedTextV1,
		false,
	)
	if !ok {
		t.Fatal("closed text extraction profile unavailable")
	}
	pipeline, err := runtime.activePipelineFingerprint(context.Background(), profile.Capability, profile.OutputProfile)
	if err != nil || pipeline == "" {
		t.Fatalf("active text extraction pipeline=%q err=%v", pipeline, err)
	}
	descriptor.Capability = profile.Capability
	descriptor.CapabilitySchema = profile.CapabilitySchema
	descriptor.PipelineFingerprint = pipeline
	descriptor.OutputProfile = profile.OutputProfile
	descriptor.Parameters = processing.CanonicalProductionParameters(profile)
	if err := processing.ValidateProductionWorkDescriptorV1(descriptor, false); err != nil {
		t.Fatalf("production work descriptor: %v", err)
	}
	workerID := strings.Repeat("4", 32)
	worker := model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("4", 64),
		InstanceID: strings.Repeat("4", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", InteractiveSlots: 1, BackgroundSlots: 1,
		LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}
	capability := model.BackupAssetWorkerCapability{
		ID: strings.Repeat("5", 32), WorkerID: workerID, Capability: descriptor.Capability,
		CapabilitySchema: descriptor.CapabilitySchema, PipelineFingerprint: descriptor.PipelineFingerprint,
		OutputProfile: descriptor.OutputProfile, InputModes: "stat,sequential", LimitsCanonical: []byte{1},
		AdvertisementDigest: strings.Repeat("5", 64), HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&worker).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	work, err := runtime.coordinator.RequestWork(context.Background(), processing.WorkRequest{
		Descriptor: descriptor,
		Interest: processing.InterestRequest{
			OwnerKind: processing.InterestSystem, OwnerKey: "runtime-projection-recovery",
			PriorityClass: processing.PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := runtime.coordinator.PullAttempt(context.Background(), processing.PullRequest{WorkerID: workerID}, runtime.grants)
	if err != nil {
		t.Fatal(err)
	}
	activated, err := runtime.grants.Activate(context.Background(), processing.ActivateGrantRequest{
		GrantID: leased.Grants.Sink.GrantID, Kind: processing.GrantSink, JobID: work.JobID,
		AttemptID: leased.Lease.AttemptID, WorkerID: workerID, Secret: leased.Grants.Sink.Secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", work.JobID).
		Updates(map[string]any{"state": string(processing.ProcessingUploading), "transition_revision": int64(5)}).Error; err != nil {
		t.Fatal(err)
	}
	payloads := [][]byte{
		[]byte("runtime-pending-projection"),
		[]byte(`{"schema_version":1,"coverage":"complete","truncated":false,"input_bytes":26,"runes":26,"lines":1}`),
	}
	declarations := []processing.ArtifactDeclaration{
		{
			Ordinal: 0, Role: processing.ArtifactRoleContent, MediaType: "text/plain", PlaintextSize: int64(len(payloads[0])),
			PlaintextDigest: fmt.Sprintf("%x", sha256.Sum256(payloads[0])), Completeness: processing.ArtifactComplete,
			CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`),
		},
		{
			Ordinal: 1, Role: processing.ArtifactRoleMetadata, MediaType: "application/json", PlaintextSize: int64(len(payloads[1])),
			PlaintextDigest: fmt.Sprintf("%x", sha256.Sum256(payloads[1])), Completeness: processing.ArtifactComplete,
			CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`),
		},
	}
	for index := range declarations {
		if _, err := runtime.sink.UploadArtifact(context.Background(), processing.UploadArtifactRequest{
			JobID: work.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: workerID,
			GrantID: activated.SessionID, Artifact: declarations[index],
		}, bytes.NewReader(payloads[index])); err != nil {
			t.Fatal(err)
		}
	}
	_, firstCommitErr := runtime.sink.CommitManifest(context.Background(), processing.CommitManifestRequest{
		JobID: work.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: workerID,
		GrantID: activated.SessionID, RecoveryPointFence: leased.Lease.RecoveryPointFence,
		SecurityPolicyRevision: descriptor.SecurityPolicyRevision, Artifacts: declarations,
	})
	if firstCommitErr == nil {
		t.Fatal("first projection publication unexpectedly acknowledged")
	}
	var job model.BackupAssetProcessingJob
	if err := db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	var setCount int64
	if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("job_id = ?", work.JobID).Count(&setCount).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(processing.ProcessingUploading) || job.CurrentArtifactSetID != nil || setCount != 0 || projection.calls != 1 {
		t.Fatalf("failed projection escaped atomic rollback: err=%v calls=%d job=%+v sets=%d", firstCommitErr, projection.calls, job, setCount)
	}
	if err := runtime.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if projection.calls != 1 {
		t.Fatalf("reconciler observed an impossible pending projection: calls=%d", projection.calls)
	}
	if _, err := runtime.sink.CommitManifest(context.Background(), processing.CommitManifestRequest{
		JobID: work.JobID, AttemptID: leased.Lease.AttemptID, WorkerID: workerID,
		GrantID: activated.SessionID, RecoveryPointFence: leased.Lease.RecoveryPointFence,
		SecurityPolicyRevision: descriptor.SecurityPolicyRevision, Artifacts: declarations,
	}); err != nil {
		t.Fatalf("atomic projection retry: %v", err)
	}
	var set model.BackupAssetDerivedArtifactSet
	if err := db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&set, "id = ?", job.CurrentArtifactSetID).Error; err != nil {
		t.Fatal(err)
	}
	if projection.calls != 2 || job.State != string(processing.ProcessingSucceeded) || !set.ProjectionPublished || set.ProjectionRevision != 17 {
		t.Fatalf("runtime did not commit atomic projection retry: calls=%d job=%+v set=%+v", projection.calls, job, set)
	}
}

func TestProcessingRuntimeMarksUnreadableDerivedKeyLostAndStaysUnavailable(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	root := filepath.Join(t.TempDir(), "derived")
	keyring := backupasset.NewKeyring(db, time.Now)
	material, err := keyring.Ensure(context.Background(), backupasset.KeyDomainDerivedStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.WrappedDomainKey{}).
		Where("domain = ? AND version = ?", backupasset.KeyDomainDerivedStore, material.Version).
		Update("wrapped_key", []byte("corrupt-derived-envelope")).Error; err != nil {
		t.Fatal(err)
	}
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, root, true, true), Keyring: keyring,
		Lease: processingRuntimeLease(t, db), Source: processingRuntimeSourceFake{}, Authorize: processingRuntimeAssetAuthorizerFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); !errors.Is(err, processing.ErrDerivedStoreUnavailable) {
		t.Fatalf("Startup unreadable Derived key error=%v, want unavailable", err)
	}
	if runtime.WorkerProtocol() != nil {
		t.Fatal("runtime exposed Worker protocol after Derived key loss")
	}
	if _, err := keyring.Active(context.Background(), backupasset.KeyDomainDerivedStore); !errors.Is(err, backupasset.ErrKeyLost) {
		t.Fatalf("unreadable Derived key was not recorded lost: %v", err)
	}
}

func TestProcessingRuntimeShutdownRetiresAttemptsGrantsAndRecoveryLease(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	root := filepath.Join(t.TempDir(), "derived")
	runtime, err := newProcessingRuntime(processingRuntimeDependencies{
		DB: db, Foundation: processingRuntimeFoundation(t, db, root, true, true),
		Keyring: backupasset.NewKeyring(db, time.Now), Lease: processingRuntimeLease(t, db),
		Source: processingRuntimeSourceFake{}, Authorize: processingRuntimeAssetAuthorizerFake{}, ValidateRoot: processingRuntimeRootValidator,
		RevalidateSource: processingRuntimeSourceRevalidatorFake{}, Projection: processingRuntimeProjectionFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Startup(context.Background()); err != nil {
		t.Fatal(err)
	}
	descriptor := processingRuntimeWorkDescriptor()
	now := time.Now().UTC()
	workerID := strings.Repeat("6", 32)
	worker := model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("6", 64),
		InstanceID: strings.Repeat("6", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", InteractiveSlots: 1, BackgroundSlots: 1,
		LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}
	capability := model.BackupAssetWorkerCapability{
		ID: strings.Repeat("7", 32), WorkerID: workerID, Capability: descriptor.Capability,
		CapabilitySchema: descriptor.CapabilitySchema, PipelineFingerprint: descriptor.PipelineFingerprint,
		OutputProfile: descriptor.OutputProfile, InputModes: "stat,sequential", LimitsCanonical: []byte{1},
		AdvertisementDigest: strings.Repeat("8", 64), HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&worker).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&capability).Error; err != nil {
		t.Fatal(err)
	}
	work, err := runtime.coordinator.RequestWork(context.Background(), processing.WorkRequest{
		Descriptor: descriptor,
		Interest: processing.InterestRequest{
			OwnerKind: processing.InterestSystem, OwnerKey: "runtime-shutdown",
			PriorityClass: processing.PriorityInteractive, Priority: 100,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := runtime.workerProtocol.Pull(context.Background(), processing.WorkerTransportIdentity{
		Kind: processing.WorkerTransportLocal, Fingerprint: worker.TransportFingerprint, PeerUID: uint32(os.Geteuid()),
	}, processing.WorkerPullRequest{SchemaVersion: 1, WorkerID: workerID, InstanceID: worker.InstanceID})
	if err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var lease model.RecoveryPointLease
	var grants []model.BackupAssetProcessingGrant
	if err := db.First(&job, "id = ?", work.JobID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&attempt, "id = ?", envelope.AttemptID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&lease, "id = ?", envelope.RecoveryPointFence.LeaseID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("attempt_id = ?", envelope.AttemptID).Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(processing.ProcessingRetryWait) || job.CurrentAttemptID != nil ||
		attempt.State != "canceled" || attempt.IsCurrent || lease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("shutdown left Processing authority active: job=%+v attempt=%+v lease=%+v", job, attempt, lease)
	}
	if len(grants) != 2 {
		t.Fatalf("shutdown grant count=%d, want 2", len(grants))
	}
	for _, grant := range grants {
		if grant.State != string(processing.GrantRevoked) || grant.RevocationReason != "shutdown" {
			t.Fatalf("shutdown left grant active: %+v", grant)
		}
	}
}

func TestRuntimeDerivedProjectionPortMapsPreparedFieldsAndUsesCallerTransaction(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
		&model.BackupAssetSearchDocumentField{}, &model.BackupAssetSearchPosting{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	pointID := strings.Repeat("a", 32)
	catalogID := strings.Repeat("b", 32)
	entryID := strings.Repeat("c", 64)
	searchID := strings.Repeat("d", 32)
	setID := strings.Repeat("e", 32)
	artifactID := strings.Repeat("f", 32)
	source := "runtime-projection-source"
	generation := model.BackupAssetSearchGeneration{
		ID: searchID, RecoveryPointID: pointID, CatalogGenerationID: catalogID, Generation: 1,
		State: string(search.SearchGenerationComplete), IsActive: true, SourceFingerprint: source,
		NormalizerVersion: search.NormalizerVersion, SearchKeyVersion: 1, ProjectionRevision: 7,
		LeaseID: strings.Repeat("1", 32), BuildAttemptID: strings.Repeat("2", 32), FenceTokenHash: strings.Repeat("3", 64),
		ExpectedDocumentCount: 1, WrittenDocumentCount: 1, StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	document := model.BackupAssetSearchDocument{
		SearchGenerationID: searchID, DocumentID: entryID, RecoveryPointID: pointID, CatalogGenerationID: catalogID,
		EntryID: entryID, Sensitivity: string(search.SensitivitySecret), ClassificationRevision: 5, MetadataRevision: 1,
		EntryType: "file", LineageToken: strings.Repeat("4", 64), PathGroupToken: strings.Repeat("5", 64),
		PathSortKey: "safe", NameSortKey: "safe", CreatedAt: now, UpdatedAt: now,
	}
	field := model.BackupAssetSearchDocumentField{
		SearchGenerationID: searchID, DocumentID: entryID, Field: string(search.SearchFieldContent),
		State: string(search.FieldCoverageUnavailable), CoverageRevision: 1, ClassificationRevision: 5,
		PipelineRevision: 1, IndexRevision: 1, SourceFingerprint: source, UpdatedAt: now,
	}
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&document).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&field).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedArtifact{
		ID: artifactID, ArtifactSetID: setID, Ordinal: 0, Role: string(processing.ArtifactRoleContent), MediaType: "text/plain",
		PlaintextSize: 1, PlaintextDigest: strings.Repeat("6", 64), Completeness: string(processing.ArtifactPartial),
		CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`), BlobID: strings.Repeat("7", 32), ExcerptRef: artifactID, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	recorder := &runtimeContentIngestRecorder{db: db}
	port := runtimeDerivedProjectionPort{
		db: db, ingest: recorder,
		pipelineRevisions: func(context.Context) (runtimeProjectionRevisions, error) {
			return runtimeProjectionRevisions{Content: 2, OCR: 3}, nil
		},
	}
	fence := backupasset.LeaseFence{
		LeaseID: strings.Repeat("8", 32), RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderProcessingJob,
		OwnerID: strings.Repeat("9", 32), AttemptID: strings.Repeat("a", 32), FenceToken: strings.Repeat("b", 64),
	}
	prepared, err := port.PreparePublish(context.Background(), processing.DerivedProjectionPublish{
		ArtifactSetID: setID, RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID,
		SourceFingerprint: source, RecoveryPointFence: fence,
		Fields: []processing.DerivedProjectionField{{
			ExcerptArtifactID: artifactID, Role: processing.ArtifactRoleContent, Completeness: processing.ArtifactPartial,
			Terms: []processing.DerivedProjectionTerm{{Term: "needle", Frequency: 2}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.prepared) != 1 {
		t.Fatalf("prepared Search projections=%d", len(recorder.prepared))
	}
	mapped := recorder.prepared[0]
	if mapped.Ref != (backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}) || mapped.Field != search.SearchFieldContent ||
		mapped.SearchGenerationID != searchID || mapped.ProcessingLeaseID != fence.LeaseID || mapped.AttemptID != fence.AttemptID ||
		mapped.FenceToken != fence.FenceToken || mapped.ExpectedClassificationRevision != 5 || mapped.Classification != search.SensitivitySecret ||
		mapped.ClassificationRevision != 5 || mapped.CoverageRevision != 2 || mapped.PipelineRevision != 2 || mapped.IndexRevision != 2 ||
		mapped.Coverage != search.FieldCoveragePartial || mapped.ExcerptRef == nil || *mapped.ExcerptRef != artifactID ||
		len(mapped.Terms) != 1 || mapped.Terms[0] != (search.TermFrequency{Term: "needle", Frequency: 2}) {
		t.Fatalf("mapped Search projection=%+v", mapped)
	}
	var publication processing.DerivedProjectionPublication
	if err := db.Transaction(func(tx *gorm.DB) error {
		var publishErr error
		publication, publishErr = prepared.PublishTx(context.Background(), tx)
		return publishErr
	}); err != nil {
		t.Fatal(err)
	}
	if publication.ArtifactSetID != setID || publication.Revision != 8 || recorder.publishCalls != 1 {
		t.Fatalf("publication=%+v calls=%d", publication, recorder.publishCalls)
	}

	preparedRevoke, err := port.PrepareRevoke(context.Background(), processing.DerivedProjectionRevoke{
		ArtifactSetID: setID, RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID,
		SourceFingerprint: source, ProjectionRevision: publication.Revision, Reason: processing.DerivedRevokeRollback,
		RecoveryPointFence: fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return preparedRevoke.RevokeTx(context.Background(), tx)
	}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.revoked) != 1 || recorder.revoked[0].Field != search.SearchFieldContent ||
		recorder.revoked[0].CoverageRevision != 2 || recorder.revoked[0].PipelineRevision != 2 ||
		recorder.revoked[0].IndexRevision != 2 {
		t.Fatalf("mapped Search revocations=%+v", recorder.revoked)
	}
}

func TestRuntimeDerivedProjectionPortMapsClassificationAndUsesCallerTransaction(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
		&model.BackupAssetSearchDocumentField{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	pointID := strings.Repeat("1", 32)
	catalogID := strings.Repeat("2", 32)
	entryID := strings.Repeat("3", 64)
	searchID := strings.Repeat("4", 32)
	setID := strings.Repeat("5", 32)
	artifactID := strings.Repeat("6", 32)
	source := "runtime-classification-source"
	if err := db.Create(&model.BackupAssetSearchGeneration{
		ID: searchID, RecoveryPointID: pointID, CatalogGenerationID: catalogID, Generation: 1,
		State: string(search.SearchGenerationComplete), IsActive: true, SourceFingerprint: source,
		NormalizerVersion: search.NormalizerVersion, SearchKeyVersion: 1, ProjectionRevision: 7,
		LeaseID: strings.Repeat("7", 32), BuildAttemptID: strings.Repeat("8", 32), FenceTokenHash: strings.Repeat("9", 64),
		ExpectedDocumentCount: 1, WrittenDocumentCount: 1, StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetSearchDocument{
		SearchGenerationID: searchID, DocumentID: entryID, RecoveryPointID: pointID, CatalogGenerationID: catalogID,
		EntryID: entryID, Sensitivity: string(search.SensitivityNonSecret), ClassificationRevision: 3, MetadataRevision: 1,
		EntryType: "file", LineageToken: strings.Repeat("a", 64), PathGroupToken: strings.Repeat("b", 64),
		PathSortKey: "safe", NameSortKey: "safe", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	excerpt := strings.Repeat("c", 32)
	for _, field := range []search.SearchField{search.SearchFieldContent, search.SearchFieldOCR} {
		if err := db.Create(&model.BackupAssetSearchDocumentField{
			SearchGenerationID: searchID, DocumentID: entryID, Field: string(field),
			State: string(search.FieldCoverageComplete), CoverageRevision: 4, ClassificationRevision: 3,
			PipelineRevision: 2, IndexRevision: 5, SourceFingerprint: source,
			ExcerptRef: &excerpt, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	recorder := &runtimeContentIngestRecorder{db: db}
	port := runtimeDerivedProjectionPort{
		db: db, ingest: recorder, classification: recorder,
		pipelineRevisions: func(context.Context) (runtimeProjectionRevisions, error) {
			return runtimeProjectionRevisions{Content: 2, OCR: 2}, nil
		},
	}
	fence := backupasset.LeaseFence{
		LeaseID: strings.Repeat("d", 32), RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderProcessingJob,
		OwnerID: strings.Repeat("e", 32), AttemptID: strings.Repeat("f", 32), FenceToken: strings.Repeat("0", 64),
	}
	prepared, err := port.PreparePublish(context.Background(), processing.DerivedProjectionPublish{
		ArtifactSetID: setID, RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID,
		SourceFingerprint: source, RecoveryPointFence: fence,
		Classification: &processing.DerivedClassificationEvidence{
			ArtifactID: artifactID, Sensitivity: processing.DerivedSensitivitySecret,
			Categories: []string{"credential_pattern"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.classifications) != 1 {
		t.Fatalf("prepared Search classifications=%d", len(recorder.classifications))
	}
	mapped := recorder.classifications[0]
	if mapped.Ref != (backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}) ||
		mapped.CatalogGenerationID != catalogID || mapped.SearchGenerationID != searchID ||
		mapped.ProcessingLeaseID != fence.LeaseID || mapped.AttemptID != fence.AttemptID || mapped.FenceToken != fence.FenceToken ||
		mapped.ExpectedClassificationRevision != 3 || mapped.ClassificationRevision != 4 ||
		mapped.Classification != search.SensitivitySecret || mapped.EvidenceArtifactID != artifactID {
		t.Fatalf("mapped Search classification=%+v", mapped)
	}
	rollback := errors.New("force classification adapter rollback")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if _, publishErr := prepared.PublishTx(context.Background(), tx); publishErr != nil {
			return publishErr
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("classification rollback error=%v", err)
	}
	var revision int64
	if err := db.Model(&model.BackupAssetSearchGeneration{}).Select("projection_revision").Where("id = ?", searchID).Scan(&revision).Error; err != nil {
		t.Fatal(err)
	}
	if revision != 7 {
		t.Fatalf("classification adapter rollback leaked revision=%d", revision)
	}
	var publication processing.DerivedProjectionPublication
	if err := db.Transaction(func(tx *gorm.DB) error {
		var publishErr error
		publication, publishErr = prepared.PublishTx(context.Background(), tx)
		return publishErr
	}); err != nil {
		t.Fatal(err)
	}
	if publication.ArtifactSetID != setID || publication.Revision != 8 || recorder.classificationPublishCalls != 2 {
		t.Fatalf("classification publication=%+v calls=%d", publication, recorder.classificationPublishCalls)
	}
}

func TestRuntimeSearchSensitivityMappingIsClosed(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		input processing.DerivedSensitivity
		want  search.Sensitivity
	}{
		{name: "public", input: processing.DerivedSensitivityPublic, want: search.SensitivityNonSecret},
		{name: "secret", input: processing.DerivedSensitivitySecret, want: search.SensitivitySecret},
		{name: "unknown", input: processing.DerivedSensitivityUnknown, want: search.SensitivityUnknown},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := runtimeSearchSensitivity(testCase.input)
			if err != nil || got != testCase.want {
				t.Fatalf("runtime Search sensitivity=%q err=%v want=%q", got, err, testCase.want)
			}
		})
	}
	if got, err := runtimeSearchSensitivity("future"); err == nil || got != "" {
		t.Fatalf("future sensitivity=%q err=%v", got, err)
	}
}

func TestRuntimeDerivedProjectionPortRevokesClassificationToUnknownInCallerTransaction(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.BackupAssetProcessingJob{}, &model.BackupAssetDerivedArtifactSet{}, &model.BackupAssetDerivedArtifact{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{}, &model.BackupAssetSearchDocumentField{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	pointID := strings.Repeat("1", 32)
	catalogID := strings.Repeat("2", 32)
	entryID := strings.Repeat("3", 64)
	searchID := strings.Repeat("4", 32)
	jobID := strings.Repeat("5", 32)
	setID := strings.Repeat("6", 32)
	artifactID := strings.Repeat("7", 32)
	source := "runtime-classification-revoke-source"
	if err := db.Create(&model.BackupAssetProcessingJob{
		ID: jobID, DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`),
		Capability: "secret.classify", CapabilitySchema: "secret.classify.v1",
		RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID, SourceFingerprint: source,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: jobID, AttemptID: strings.Repeat("8", 32), WorkKey: strings.Repeat("9", 64),
		RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID, SourceFingerprint: source,
		SecurityPolicyRevision: "policy-v1", ManifestDigest: strings.Repeat("a", 64), State: "active",
		Completeness: "complete", ArtifactCount: 1, TotalPlaintextBytes: 1, ProjectionRequired: true,
		ProjectionPublished: true, ProjectionRevision: 7, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetDerivedArtifact{
		ID: artifactID, ArtifactSetID: setID, Ordinal: 0, Role: string(processing.ArtifactRoleMetadata), MediaType: "application/json",
		PlaintextSize: 1, PlaintextDigest: strings.Repeat("b", 64), Completeness: string(processing.ArtifactComplete),
		CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`), BlobID: strings.Repeat("c", 32), CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetSearchGeneration{
		ID: searchID, RecoveryPointID: pointID, CatalogGenerationID: catalogID, Generation: 1,
		State: string(search.SearchGenerationComplete), IsActive: true, SourceFingerprint: source,
		NormalizerVersion: search.NormalizerVersion, SearchKeyVersion: 1, ProjectionRevision: 7,
		LeaseID: strings.Repeat("d", 32), BuildAttemptID: strings.Repeat("e", 32), FenceTokenHash: strings.Repeat("f", 64),
		ExpectedDocumentCount: 1, WrittenDocumentCount: 1, StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.BackupAssetSearchDocument{
		SearchGenerationID: searchID, DocumentID: entryID, RecoveryPointID: pointID, CatalogGenerationID: catalogID,
		EntryID: entryID, Sensitivity: string(search.SensitivitySecret), ClassificationRevision: 4, MetadataRevision: 1,
		EntryType: "file", LineageToken: strings.Repeat("0", 64), PathGroupToken: strings.Repeat("1", 64),
		PathSortKey: "safe", NameSortKey: "safe", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	excerpt := strings.Repeat("2", 32)
	for _, field := range []search.SearchField{search.SearchFieldContent, search.SearchFieldOCR} {
		if err := db.Create(&model.BackupAssetSearchDocumentField{
			SearchGenerationID: searchID, DocumentID: entryID, Field: string(field),
			State: string(search.FieldCoverageUnavailable), CoverageRevision: 5, ClassificationRevision: 4,
			PipelineRevision: 2, IndexRevision: 6, SourceFingerprint: source, ExcerptRef: &excerpt, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	recorder := &runtimeContentIngestRecorder{db: db}
	port := runtimeDerivedProjectionPort{
		db: db, ingest: recorder, classification: recorder,
		pipelineRevisions: func(context.Context) (runtimeProjectionRevisions, error) {
			return runtimeProjectionRevisions{Content: 2, OCR: 2}, nil
		},
	}
	fence := backupasset.LeaseFence{
		LeaseID: strings.Repeat("3", 32), RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderProcessingJob,
		OwnerID: jobID, AttemptID: strings.Repeat("4", 32), FenceToken: strings.Repeat("5", 64),
	}
	prepared, err := port.PrepareRevoke(context.Background(), processing.DerivedProjectionRevoke{
		ArtifactSetID: setID, RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID,
		SourceFingerprint: source, ProjectionRevision: 7, Reason: processing.DerivedRevokePolicyChanged,
		RecoveryPointFence: fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.classifications) != 1 || len(recorder.revoked) != 0 {
		t.Fatalf("classification revoke prepared=%+v content revokes=%+v", recorder.classifications, recorder.revoked)
	}
	mapped := recorder.classifications[0]
	if mapped.Classification != search.SensitivityUnknown || mapped.ExpectedClassificationRevision != 4 ||
		mapped.ClassificationRevision != 5 || mapped.EvidenceArtifactID != "" {
		t.Fatalf("classification revoke mapping=%+v", mapped)
	}
	rollback := errors.New("force classification revoke rollback")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if revokeErr := prepared.RevokeTx(context.Background(), tx); revokeErr != nil {
			return revokeErr
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("classification revoke rollback error=%v", err)
	}
	var revision int64
	if err := db.Model(&model.BackupAssetSearchGeneration{}).Select("projection_revision").Where("id = ?", searchID).Scan(&revision).Error; err != nil {
		t.Fatal(err)
	}
	if revision != 7 {
		t.Fatalf("classification revoke rollback leaked revision=%d", revision)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return prepared.RevokeTx(context.Background(), tx) }); err != nil {
		t.Fatal(err)
	}
	if recorder.classificationPublishCalls != 2 {
		t.Fatalf("classification revoke calls=%d", recorder.classificationPublishCalls)
	}
}

func TestRuntimeDerivedProjectionPortRealSQLiteSearchFailureRollsBackDerivedAndSearch(t *testing.T) {
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.CatalogGeneration{}, &model.BackupAssetSearchGeneration{},
		&model.BackupAssetSearchDocument{}, &model.BackupAssetSearchDocumentField{}, &model.BackupAssetSearchPosting{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)
	pointID := strings.Repeat("1", 32)
	catalogID := strings.Repeat("2", 32)
	entryID := strings.Repeat("3", 64)
	searchID := strings.Repeat("4", 32)
	setID := strings.Repeat("5", 32)
	artifactID := strings.Repeat("6", 32)
	source := "sqlite-atomic-source"
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: strings.Repeat("7", 32), Semantics: string(backupasset.PointImportedBaseline),
		State: string(backupasset.RecoveryPointCommitted), SourceFingerprint: source,
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CreatedAt: now, UpdatedAt: now,
	}
	generation := model.CatalogGeneration{
		ID: catalogID, RecoveryPointID: pointID, Generation: 1, State: string(catalog.GenerationComplete), IsActive: true,
		SourceFingerprint: source, ExpectedEntryCount: 1, WrittenEntryCount: 1, StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	keyring := backupasset.NewKeyring(db, func() time.Time { return now })
	key, err := keyring.Ensure(context.Background(), backupasset.KeyDomainSearchToken)
	if err != nil {
		t.Fatal(err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := leaseService.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
		RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderProcessingJob, OwnerID: strings.Repeat("8", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	searchGeneration := model.BackupAssetSearchGeneration{
		ID: searchID, RecoveryPointID: pointID, CatalogGenerationID: catalogID, Generation: 1,
		State: string(search.SearchGenerationComplete), IsActive: true, SourceFingerprint: source,
		NormalizerVersion: search.NormalizerVersion, SearchKeyVersion: key.Version, ProjectionRevision: 1,
		LeaseID: strings.Repeat("9", 32), BuildAttemptID: strings.Repeat("a", 32), FenceTokenHash: strings.Repeat("b", 64),
		ExpectedDocumentCount: 1, WrittenDocumentCount: 1, StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	document := model.BackupAssetSearchDocument{
		SearchGenerationID: searchID, DocumentID: entryID, RecoveryPointID: pointID, CatalogGenerationID: catalogID,
		EntryID: entryID, Sensitivity: string(search.SensitivityNonSecret), ClassificationRevision: 1, MetadataRevision: 1,
		EntryType: "file", LineageToken: strings.Repeat("c", 64), PathGroupToken: strings.Repeat("d", 64),
		PathSortKey: "atomic", NameSortKey: "atomic", CreatedAt: now, UpdatedAt: now,
	}
	field := model.BackupAssetSearchDocumentField{
		SearchGenerationID: searchID, DocumentID: entryID, Field: string(search.SearchFieldContent),
		State: string(search.FieldCoverageUnavailable), CoverageRevision: 1, ClassificationRevision: 1,
		PipelineRevision: 1, IndexRevision: 1, SourceFingerprint: source, UpdatedAt: now,
	}
	if err := db.Create(&searchGeneration).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&document).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&field).Error; err != nil {
		t.Fatal(err)
	}
	ocrField := field
	ocrField.Field = string(search.SearchFieldOCR)
	if err := db.Create(&ocrField).Error; err != nil {
		t.Fatal(err)
	}
	ingest, err := search.NewContentIngestService(search.ContentIngestDependencies{
		DB: db, Keys: keyring, Lease: leaseService, Now: func() time.Time { return now }, Limits: search.DefaultContentIngestLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	port := runtimeDerivedProjectionPort{
		db: db, ingest: ingest,
		pipelineRevisions: func(context.Context) (runtimeProjectionRevisions, error) {
			return runtimeProjectionRevisions{Content: 2, OCR: 2}, nil
		},
	}
	prepared, err := port.PreparePublish(context.Background(), processing.DerivedProjectionPublish{
		ArtifactSetID: setID, RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID,
		SourceFingerprint: source, RecoveryPointFence: lease.Fence,
		Fields: []processing.DerivedProjectionField{{
			ExcerptArtifactID: artifactID, Role: processing.ArtifactRoleContent, Completeness: processing.ArtifactComplete,
			Terms: []processing.DerivedProjectionTerm{{Term: "atomic-needle", Frequency: 1}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	derivedSet := model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: strings.Repeat("e", 32), AttemptID: strings.Repeat("f", 32), WorkKey: strings.Repeat("0", 64),
		RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID, SourceFingerprint: source,
		SecurityPolicyRevision: "policy-v1", ManifestDigest: strings.Repeat("1", 64), State: "active", Completeness: "complete",
		ArtifactCount: 1, TotalPlaintextBytes: 1, ProjectionRequired: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Exec(`CREATE TRIGGER reject_atomic_search_posting BEFORE INSERT ON backup_asset_search_postings
		BEGIN SELECT RAISE(ABORT, 'reject atomic Search posting'); END`).Error; err != nil {
		t.Fatal(err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if createErr := tx.Create(&derivedSet).Error; createErr != nil {
			return createErr
		}
		_, publishErr := prepared.PublishTx(context.Background(), tx)
		return publishErr
	})
	if err == nil {
		t.Fatal("injected Search failure unexpectedly committed")
	}
	var sets int64
	var postings int64
	if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", setID).Count(&sets).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetSearchPosting{}).Where("search_generation_id = ? AND document_id = ?", searchID, entryID).Count(&postings).Error; err != nil {
		t.Fatal(err)
	}
	var afterFailure model.BackupAssetSearchDocumentField
	if err := db.Where("search_generation_id = ? AND document_id = ? AND field = ?", searchID, entryID, search.SearchFieldContent).
		Take(&afterFailure).Error; err != nil {
		t.Fatal(err)
	}
	if sets != 0 || postings != 0 || afterFailure.State != string(search.FieldCoverageUnavailable) || afterFailure.IndexRevision != 1 {
		t.Fatalf("Search failure escaped outer rollback: sets=%d postings=%d field=%+v", sets, postings, afterFailure)
	}
	if err := db.Exec("DROP TRIGGER reject_atomic_search_posting").Error; err != nil {
		t.Fatal(err)
	}
	var publication processing.DerivedProjectionPublication
	if err := db.Transaction(func(tx *gorm.DB) error {
		if createErr := tx.Create(&derivedSet).Error; createErr != nil {
			return createErr
		}
		var publishErr error
		publication, publishErr = prepared.PublishTx(context.Background(), tx)
		if publishErr != nil {
			return publishErr
		}
		return tx.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", setID).
			Updates(map[string]any{"projection_published": true, "projection_revision": publication.Revision}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetSearchPosting{}).Where("search_generation_id = ? AND document_id = ?", searchID, entryID).Count(&postings).Error; err != nil {
		t.Fatal(err)
	}
	var committed model.BackupAssetDerivedArtifactSet
	if err := db.First(&committed, "id = ?", setID).Error; err != nil {
		t.Fatal(err)
	}
	if postings == 0 || !committed.ProjectionPublished || committed.ProjectionRevision != publication.Revision || publication.Revision != 2 {
		t.Fatalf("atomic SQLite commit incomplete: postings=%d set=%+v publication=%+v", postings, committed, publication)
	}

	classificationSetID := strings.Repeat("2", 32)
	classificationArtifactID := strings.Repeat("3", 32)
	port.classification = ingest
	preparedClassification, err := port.PreparePublish(context.Background(), processing.DerivedProjectionPublish{
		ArtifactSetID: classificationSetID, RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID,
		SourceFingerprint: source, RecoveryPointFence: lease.Fence,
		Classification: &processing.DerivedClassificationEvidence{
			ArtifactID: classificationArtifactID, Sensitivity: processing.DerivedSensitivitySecret,
			Categories: []string{"credential_pattern"},
		},
	})
	if err != nil {
		var controls []model.BackupAssetSearchDocumentField
		_ = db.Where("search_generation_id = ? AND document_id = ?", searchID, entryID).Order("field ASC").Find(&controls).Error
		t.Fatalf("prepare SQLite classification: %v fields=%+v", err, controls)
	}
	classificationSet := derivedSet
	classificationSet.ID = classificationSetID
	classificationSet.JobID = strings.Repeat("4", 32)
	classificationSet.AttemptID = strings.Repeat("5", 32)
	classificationSet.WorkKey = strings.Repeat("6", 64)
	classificationSet.ManifestDigest = strings.Repeat("7", 64)
	classificationArtifact := model.BackupAssetDerivedArtifact{
		ID: classificationArtifactID, ArtifactSetID: classificationSetID, Ordinal: 0,
		Role: string(processing.ArtifactRoleMetadata), MediaType: "application/json", PlaintextSize: 64,
		PlaintextDigest: strings.Repeat("8", 64), Completeness: string(processing.ArtifactComplete),
		CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`), BlobID: strings.Repeat("9", 32), CreatedAt: now,
	}
	if err := db.Exec(`CREATE TRIGGER reject_atomic_classification BEFORE UPDATE OF classification_revision ON backup_asset_search_documents
		BEGIN SELECT RAISE(ABORT, 'reject atomic Search classification'); END`).Error; err != nil {
		t.Fatal(err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if createErr := tx.Create(&classificationSet).Error; createErr != nil {
			return createErr
		}
		if createErr := tx.Create(&classificationArtifact).Error; createErr != nil {
			return createErr
		}
		_, publishErr := preparedClassification.PublishTx(context.Background(), tx)
		return publishErr
	})
	if err == nil {
		t.Fatal("injected classification Search failure unexpectedly committed")
	}
	var classificationSets int64
	var classificationArtifacts int64
	if err := db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", classificationSetID).Count(&classificationSets).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetDerivedArtifact{}).Where("id = ?", classificationArtifactID).Count(&classificationArtifacts).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetSearchPosting{}).
		Where("search_generation_id = ? AND document_id = ?", searchID, entryID).Count(&postings).Error; err != nil {
		t.Fatal(err)
	}
	var afterClassificationFailure model.BackupAssetSearchDocument
	if err := db.Where("search_generation_id = ? AND document_id = ?", searchID, entryID).Take(&afterClassificationFailure).Error; err != nil {
		t.Fatal(err)
	}
	if classificationSets != 0 || classificationArtifacts != 0 ||
		afterClassificationFailure.Sensitivity != string(search.SensitivityNonSecret) ||
		afterClassificationFailure.ClassificationRevision != 1 || postings == 0 {
		t.Fatalf("classification failure escaped outer rollback: sets=%d artifacts=%d document=%+v postings=%d",
			classificationSets, classificationArtifacts, afterClassificationFailure, postings)
	}
	if err := db.Exec("DROP TRIGGER reject_atomic_classification").Error; err != nil {
		t.Fatal(err)
	}
	var classificationPublication processing.DerivedProjectionPublication
	if err := db.Transaction(func(tx *gorm.DB) error {
		if createErr := tx.Create(&classificationSet).Error; createErr != nil {
			return createErr
		}
		if createErr := tx.Create(&classificationArtifact).Error; createErr != nil {
			return createErr
		}
		var publishErr error
		classificationPublication, publishErr = preparedClassification.PublishTx(context.Background(), tx)
		if publishErr != nil {
			return publishErr
		}
		return tx.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", classificationSetID).
			Updates(map[string]any{"projection_published": true, "projection_revision": classificationPublication.Revision}).Error
	}); err != nil {
		t.Fatal(err)
	}
	var classified model.BackupAssetSearchDocument
	if err := db.Where("search_generation_id = ? AND document_id = ?", searchID, entryID).Take(&classified).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetSearchPosting{}).Where("search_generation_id = ? AND document_id = ?", searchID, entryID).Count(&postings).Error; err != nil {
		t.Fatal(err)
	}
	var classifiedFields []model.BackupAssetSearchDocumentField
	if err := db.Where("search_generation_id = ? AND document_id = ?", searchID, entryID).Order("field ASC").Find(&classifiedFields).Error; err != nil {
		t.Fatal(err)
	}
	if classified.Sensitivity != string(search.SensitivitySecret) || classified.ClassificationRevision != 2 ||
		postings != 0 || len(classifiedFields) != 2 || classificationPublication.Revision != 3 {
		t.Fatalf("atomic classification commit incomplete: document=%+v postings=%d fields=%+v publication=%+v",
			classified, postings, classifiedFields, classificationPublication)
	}
	for _, classifiedField := range classifiedFields {
		if classifiedField.State != string(search.FieldCoverageUnavailable) || classifiedField.ClassificationRevision != 2 ||
			classifiedField.ExcerptRef != nil {
			t.Fatalf("classified Search field retained evidence: %+v", classifiedField)
		}
	}
}

func processingRuntimeWorkDescriptor() processing.WorkDescriptorV1 {
	return processing.WorkDescriptorV1{
		SchemaVersion: 1,
		Source: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64),
		},
		CatalogGenerationID: strings.Repeat("c", 32), SourceFingerprint: "runtime-source-v1",
		EntryFingerprint: "runtime-entry-v1", ProviderCapabilityRevision: 1,
		Capability: "noop", CapabilitySchema: "noop.v1", PipelineFingerprint: "runtime-noop-v1",
		OutputProfile: "noop.v1", SecurityPolicyRevision: "security-policy-v1",
		Parameters: processing.CanonicalParametersV1{
			SchemaVersion: 1, Width: 1, Height: 1, Codec: "noop", PageStart: 1, PageEnd: 1,
			Quality: 1, Language: "none", Model: "noop", FontProfile: "none",
			Orientation: "none", CropWidth: 1, CropHeight: 1,
			MaxPages: 1, MaxPixels: 1, MaxDurationMillis: 1, MaxExpandedBytes: 1,
			MaxOutputBytes: 1, MaxOutputCount: 1, TruncationPolicy: "reject",
		},
	}
}

type processingRuntimeAssetAuthorizerFake struct {
	asset content.AuthorizedAsset
	err   error
}

func (fake processingRuntimeAssetAuthorizerFake) Authorize(
	context.Context,
	content.DeliveryActor,
	backupasset.AssetRef,
	content.DeliveryAction,
) (content.AuthorizedAsset, error) {
	if fake.err != nil {
		return content.AuthorizedAsset{}, fake.err
	}
	if backupasset.ValidateAssetRef(fake.asset.Ref) != nil {
		return content.AuthorizedAsset{}, backupasset.ErrNotFound
	}
	return fake.asset, nil
}

func openProcessingRuntimeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_PROCESSING_RUNTIME_DATA_KEY_FOR_TEST_ONLY")
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(
		&model.WrappedDomainKey{}, &model.BackupAssetProcessingJob{}, &model.BackupAssetProcessingInterest{},
		&model.BackupAssetProcessingAttempt{}, &model.BackupAssetProcessingGrant{},
		&model.BackupAssetProcessingGrantRequest{}, &model.BackupAssetProcessingUpload{},
		&model.BackupAssetWorkerIdentity{}, &model.BackupAssetWorkerCapability{},
		&model.BackupAssetDerivedBlob{}, &model.BackupAssetDerivedArtifactSet{},
		&model.BackupAssetDerivedArtifact{}, &model.BackupAssetDerivedBlobReference{},
		&model.BackupAssetUpdaterMetadata{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedProcessingRuntimeSecretContinuation(t *testing.T, db *gorm.DB, now time.Time) {
	t.Helper()
	workerID := strings.Repeat("1", 32)
	parentJobID := strings.Repeat("2", 32)
	setID := strings.Repeat("3", 32)
	artifactID := strings.Repeat("4", 32)
	blobID := strings.Repeat("5", 32)
	digest := strings.Repeat("6", 64)
	referenceID := strings.Repeat("7", 32)
	pointID := strings.Repeat("8", 32)
	catalogID := strings.Repeat("9", 32)
	entryID := strings.Repeat("a", 64)
	worker := model.BackupAssetWorkerIdentity{
		ID: workerID, TransportKind: string(processing.WorkerTransportLocal), TransportFingerprint: strings.Repeat("b", 64),
		InstanceID: strings.Repeat("c", 32), IdentityRevision: 1, ProtocolVersion: processing.WorkerProtocolVersion,
		TrustState: "active", HealthState: "ready", BackgroundSlots: 1, LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}
	capability := model.BackupAssetWorkerCapability{
		ID: strings.Repeat("d", 32), WorkerID: workerID, Capability: capabilityspec.CapabilitySecretClassify,
		CapabilitySchema: "secret.classify.v1", PipelineFingerprint: "secret-pipeline-v1",
		OutputProfile: capabilityspec.ProfileBoundedSecretV1, InputModes: "stat,sequential",
		LimitsCanonical: []byte(`{"schema_version":1}`), AdvertisementDigest: strings.Repeat("e", 64),
		HealthState: "ready", CreatedAt: now, UpdatedAt: now,
	}
	parent := model.BackupAssetProcessingJob{
		ID: parentJobID, WorkKey: strings.Repeat("f", 64), DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`),
		RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID,
		SourceFingerprint: "secret-continuation-source", EntryFingerprint: "source-entry-v1", ProviderCapabilityRevision: 1,
		Capability: capabilityspec.CapabilityTextExtract, CapabilitySchema: "text.extract.v1",
		PipelineFingerprint: "text-pipeline-v1", OutputProfile: capabilityspec.ProfileBoundedTextV1,
		SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
		EffectivePriority: 1, State: string(processing.ProcessingSucceeded), TransitionRevision: 7, IsCurrent: false,
		QueuedAt: now, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	set := model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: parentJobID, AttemptID: strings.Repeat("0", 32), WorkKey: parent.WorkKey,
		RecoveryPointID: pointID, CatalogGenerationID: catalogID, EntryID: entryID,
		SourceFingerprint: parent.SourceFingerprint, SecurityPolicyRevision: processingSecurityPolicyRevision,
		ManifestDigest: strings.Repeat("1", 64), State: "active", Completeness: string(processing.ArtifactComplete),
		ArtifactCount: 1, TotalPlaintextBytes: 32, ProjectionRequired: true, ProjectionPublished: true,
		ProjectionRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
	blob := model.BackupAssetDerivedBlob{
		ID: blobID, PlaintextDigest: digest, PlaintextSize: 32, PhysicalSize: 64, CipherFormatVersion: 1,
		ChunkSize: 64 << 10, ChunkCount: 1, NoncePrefix: []byte{1}, OpaqueLocator: "aa/blob",
		WrappedDEK: []byte{1}, EnvelopeNonce: []byte{1}, DerivedKEKVersion: 1, State: "active", RefCount: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	artifact := model.BackupAssetDerivedArtifact{
		ID: artifactID, ArtifactSetID: setID, Ordinal: 0, Role: string(processing.ArtifactRoleContent), MediaType: "text/plain",
		PlaintextSize: 32, PlaintextDigest: digest, Completeness: string(processing.ArtifactComplete),
		CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`), BlobID: blobID, ExcerptRef: artifactID, CreatedAt: now,
	}
	reference := model.BackupAssetDerivedBlobReference{
		ID: referenceID, BlobID: blobID, ArtifactID: artifactID, RecoveryPointID: pointID,
		CatalogGenerationID: catalogID, EntryID: entryID, SourceFingerprint: parent.SourceFingerprint,
		State: "active", CreatedAt: now, UpdatedAt: now,
	}
	for _, value := range []any{&worker, &capability, &parent, &set, &blob, &artifact, &reference} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func seedProcessingRuntimeMalwareEvidence(
	t *testing.T,
	db *gorm.DB,
	now time.Time,
	asset content.AuthorizedAsset,
	bundleFingerprint string,
) string {
	t.Helper()
	if err := db.Create(&model.BackupAssetUpdaterMetadata{
		ID: strings.Repeat("a", 32), SourceKind: "admin_registered", SourceID: "offline-test", Version: "1.0.0",
		ManifestDigest: strings.Repeat("b", 64), SigningKeyFingerprint: strings.Repeat("c", 64),
		BundleFingerprint: bundleFingerprint, State: "active", ActivatedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	pipeline, err := (&managedProcessingRuntime{db: db}).activePipelineFingerprint(
		context.Background(), capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1,
	)
	if err != nil || pipeline == "" {
		t.Fatalf("active malware pipeline=%q err=%v", pipeline, err)
	}
	jobID := strings.Repeat("d", 32)
	attemptID := strings.Repeat("2", 32)
	setID := strings.Repeat("e", 32)
	artifactID := strings.Repeat("f", 32)
	blobID := strings.Repeat("0", 32)
	job := model.BackupAssetProcessingJob{
		ID: jobID, WorkKey: strings.Repeat("1", 64), DescriptorSchemaVersion: 1, DescriptorCanonical: []byte(`{}`),
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		Capability:                 capabilityspec.CapabilityMalwareScan, CapabilitySchema: "malware.scan.v1",
		PipelineFingerprint: pipeline, OutputProfile: capabilityspec.ProfileSignatureScanV1,
		SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
		EffectivePriority: 900, State: string(processing.ProcessingSucceeded), TransitionRevision: 2,
		CurrentAttemptID: &attemptID, CurrentArtifactSetID: &setID, IsCurrent: false, QueuedAt: now, FinishedAt: &now,
		AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	attempt := model.BackupAssetProcessingAttempt{
		ID: attemptID, JobID: jobID, AttemptNumber: 1, WorkerID: strings.Repeat("6", 32),
		SlotClass: string(processing.PriorityBackground), State: "succeeded",
		WorkerLeaseExpiresAt: now.Add(time.Minute), LastHeartbeatAt: now,
		RecoveryPointLeaseID: strings.Repeat("7", 32), RecoveryPointAttemptID: strings.Repeat("8", 32),
		RecoveryPointFenceHash: strings.Repeat("9", 64), AbsoluteDeadline: now.Add(time.Hour),
		IsCurrent: false, StartedAt: now, FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	set := model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: jobID, AttemptID: attemptID, WorkKey: job.WorkKey,
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID,
		EntryID: asset.Ref.EntryID, SourceFingerprint: asset.SourceFingerprint,
		SecurityPolicyRevision: processingSecurityPolicyRevision, ManifestDigest: strings.Repeat("3", 64),
		State: "active", Completeness: string(processing.ArtifactComplete), ArtifactCount: 1,
		TotalPlaintextBytes: 256, ProjectionRequired: false, ProjectionPublished: false,
		CreatedAt: now, UpdatedAt: now,
	}
	blob := model.BackupAssetDerivedBlob{
		ID: blobID, PlaintextDigest: strings.Repeat("4", 64), PlaintextSize: 256, PhysicalSize: 512,
		CipherFormatVersion: 1, ChunkSize: 64 << 10, ChunkCount: 1, NoncePrefix: []byte{1},
		OpaqueLocator: "aa/malware", WrappedDEK: []byte{1}, EnvelopeNonce: []byte{1}, DerivedKEKVersion: 1,
		State: "active", RefCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	artifact := model.BackupAssetDerivedArtifact{
		ID: artifactID, ArtifactSetID: setID, Ordinal: 0, Role: string(processing.ArtifactRoleMetadata),
		MediaType: "application/json", PlaintextSize: 256, PlaintextDigest: strings.Repeat("4", 64),
		Completeness: string(processing.ArtifactComplete), CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`),
		BlobID: blobID, CreatedAt: now,
	}
	reference := model.BackupAssetDerivedBlobReference{
		ID: strings.Repeat("5", 32), BlobID: blobID, ArtifactID: artifactID,
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID,
		EntryID: asset.Ref.EntryID, SourceFingerprint: asset.SourceFingerprint,
		State: "active", CreatedAt: now, UpdatedAt: now,
	}
	for _, value := range []any{&job, &attempt, &set, &blob, &artifact, &reference} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", jobID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", attemptID).
		Update("is_current", false).Error; err != nil {
		t.Fatal(err)
	}
	return pipeline
}

func duplicateProcessingRuntimeMalwarePublication(t *testing.T, db *gorm.DB, now time.Time) {
	t.Helper()
	var job model.BackupAssetProcessingJob
	var attempt model.BackupAssetProcessingAttempt
	var set model.BackupAssetDerivedArtifactSet
	var artifact model.BackupAssetDerivedArtifact
	if err := db.First(&job, "id = ?", strings.Repeat("d", 32)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&attempt, "id = ?", strings.Repeat("2", 32)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&set, "id = ?", strings.Repeat("e", 32)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&artifact, "id = ?", strings.Repeat("f", 32)).Error; err != nil {
		t.Fatal(err)
	}
	newJobID := strings.Repeat("6", 32)
	newAttemptID := strings.Repeat("7", 32)
	newSetID := strings.Repeat("8", 32)
	job.ID = newJobID
	job.WorkKey = strings.Repeat("6", 64)
	job.CurrentAttemptID = &newAttemptID
	job.CurrentArtifactSetID = &newSetID
	job.UpdatedAt = now.Add(time.Minute)
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
	set.ManifestDigest = strings.Repeat("7", 64)
	set.UpdatedAt = job.UpdatedAt
	if err := db.Create(&set).Error; err != nil {
		t.Fatal(err)
	}
	artifact.ID = strings.Repeat("9", 32)
	artifact.ArtifactSetID = newSetID
	if err := db.Create(&artifact).Error; err != nil {
		t.Fatal(err)
	}
}

func processingRuntimeMalwareResult(
	now time.Time,
	bundleFingerprint string,
	state capabilityspec.ScanState,
	completeness capabilityspec.CoverageState,
	scannedBytes int64,
) capabilityspec.MalwareResult {
	result := capabilityspec.MalwareResult{
		SchemaVersion: 1, EngineFamily: "clamav", SignatureBundleFingerprint: bundleFingerprint,
		Result: state, ScannedBytes: scannedBytes, Completeness: completeness,
		ScannedAt: now.UTC().Format(time.RFC3339),
	}
	if state == capabilityspec.ScanFinding {
		result.FindingCategory = "malware"
	}
	return result
}

type processingRuntimeMalwareEvidenceReaderFake struct {
	payload        []byte
	err            error
	calls          int
	authorizations []processing.DerivedArtifactAuthorization
}

func (fake *processingRuntimeMalwareEvidenceReaderFake) ReadAuthorized(
	_ context.Context,
	authorization processing.DerivedArtifactAuthorization,
	destination io.Writer,
) error {
	fake.calls++
	fake.authorizations = append(fake.authorizations, authorization)
	if fake.err != nil {
		return fake.err
	}
	_, err := destination.Write(fake.payload)
	return err
}

type processingRuntimeWorkRequesterFake struct {
	requests []processing.WorkRequest
	err      error
}

func (fake *processingRuntimeWorkRequesterFake) RequestWork(
	_ context.Context,
	request processing.WorkRequest,
) (processing.WorkResult, error) {
	fake.requests = append(fake.requests, request)
	if fake.err != nil {
		return processing.WorkResult{}, fake.err
	}
	return processing.WorkResult{InterestID: strings.Repeat("6", 32), Created: true}, nil
}

func processingRuntimeFoundation(t *testing.T, db *gorm.DB, root string, enabled, local bool) *backupasset.FoundationService {
	t.Helper()
	service := settings.NewService(db)
	for key, value := range map[string]string{
		"backup_assets.derived_store_root":   root,
		"backup_assets.enabled":              fmt.Sprintf("%t", enabled),
		"backup_assets.worker_local_enabled": fmt.Sprintf("%t", local),
	} {
		if err := service.Update(key, value); err != nil {
			t.Fatalf("update %s: %v", key, err)
		}
	}
	return backupasset.NewFoundationService(service)
}

func processingRuntimeLease(t *testing.T, db *gorm.DB) *backupasset.LeaseService {
	t.Helper()
	service, err := backupasset.NewLeaseService(db, time.Now, backupasset.LeaseConfig{
		Duration: 2 * time.Minute, Heartbeat: 20 * time.Second, AbsoluteDeadline: 3 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type processingRuntimeSourceFake struct{}

func (processingRuntimeSourceFake) OpenContentSource(context.Context, content.SourceRequest) (content.SourceSession, error) {
	return nil, errors.New("not used")
}

func (processingRuntimeSourceFake) ValidateContentCacheRoot(context.Context, string) error {
	return nil
}

type processingRuntimeSourceRevalidatorFake struct{}

func (processingRuntimeSourceRevalidatorFake) RevalidateProcessingSource(context.Context, processing.WorkDescriptorV1) error {
	return nil
}

type processingRuntimeProjectionFake struct{}

type processingRuntimePreparedProjection struct {
	request processing.DerivedProjectionPublish
}

type processingRuntimePreparedRevocation struct{}

func (processingRuntimeProjectionFake) PreparePublish(_ context.Context, request processing.DerivedProjectionPublish) (processing.PreparedDerivedProjection, error) {
	return processingRuntimePreparedProjection{request: request}, nil
}

func (prepared processingRuntimePreparedProjection) PublishTx(context.Context, *gorm.DB) (processing.DerivedProjectionPublication, error) {
	return processing.DerivedProjectionPublication{ArtifactSetID: prepared.request.ArtifactSetID, Revision: 1}, nil
}

func (processingRuntimeProjectionFake) PrepareRevoke(context.Context, processing.DerivedProjectionRevoke) (processing.PreparedDerivedRevocation, error) {
	return processingRuntimePreparedRevocation{}, nil
}

func (processingRuntimePreparedRevocation) RevokeTx(context.Context, *gorm.DB) error {
	return nil
}

type processingRuntimeRecoveringProjectionFake struct {
	calls int
}

type runtimeContentIngestRecorder struct {
	db                         *gorm.DB
	prepared                   []search.ContentProjection
	classifications            []search.ClassificationProjection
	revoked                    []search.RevokeProjection
	publishCalls               int
	classificationPublishCalls int
}

func (recorder *runtimeContentIngestRecorder) PrepareClassificationProjection(
	_ context.Context,
	projection search.ClassificationProjection,
) (search.PreparedClassificationProjection, error) {
	recorder.classifications = append(recorder.classifications, projection)
	return search.PreparedClassificationProjection{}, nil
}

func (recorder *runtimeContentIngestRecorder) PublishClassificationProjectionTx(
	_ context.Context,
	tx *gorm.DB,
	_ search.PreparedClassificationProjection,
) error {
	recorder.classificationPublishCalls++
	return tx.Model(&model.BackupAssetSearchGeneration{}).Where("is_active = ?", true).
		Update("projection_revision", gorm.Expr("projection_revision + 1")).Error
}

func (recorder *runtimeContentIngestRecorder) PrepareContentProjection(
	_ context.Context,
	projection search.ContentProjection,
) (search.PreparedContentProjection, error) {
	recorder.prepared = append(recorder.prepared, projection)
	return search.PreparedContentProjection{}, nil
}

func (recorder *runtimeContentIngestRecorder) PublishContentProjectionTx(
	_ context.Context,
	tx *gorm.DB,
	_ search.PreparedContentProjection,
) error {
	recorder.publishCalls++
	return tx.Model(&model.BackupAssetSearchGeneration{}).Where("is_active = ?", true).
		Update("projection_revision", gorm.Expr("projection_revision + 1")).Error
}

func (recorder *runtimeContentIngestRecorder) RevokeContentProjectionTx(
	_ context.Context,
	tx *gorm.DB,
	projection search.RevokeProjection,
) error {
	recorder.revoked = append(recorder.revoked, projection)
	return tx.Model(&model.BackupAssetSearchGeneration{}).Where("id = ?", projection.SearchGenerationID).
		Update("projection_revision", gorm.Expr("projection_revision + 1")).Error
}

type processingRuntimeRecoveringPreparedProjection struct {
	fake    *processingRuntimeRecoveringProjectionFake
	request processing.DerivedProjectionPublish
}

func (fake *processingRuntimeRecoveringProjectionFake) PreparePublish(_ context.Context, request processing.DerivedProjectionPublish) (processing.PreparedDerivedProjection, error) {
	return &processingRuntimeRecoveringPreparedProjection{fake: fake, request: request}, nil
}

func (prepared *processingRuntimeRecoveringPreparedProjection) PublishTx(context.Context, *gorm.DB) (processing.DerivedProjectionPublication, error) {
	fake := prepared.fake
	fake.calls++
	if fake.calls == 1 {
		return processing.DerivedProjectionPublication{}, errors.New("projection acknowledgement lost")
	}
	return processing.DerivedProjectionPublication{ArtifactSetID: prepared.request.ArtifactSetID, Revision: 17}, nil
}

func (*processingRuntimeRecoveringProjectionFake) PrepareRevoke(context.Context, processing.DerivedProjectionRevoke) (processing.PreparedDerivedRevocation, error) {
	return processingRuntimePreparedRevocation{}, nil
}

type processingRuntimeInvalidationFake struct {
	calls int
	last  processing.InvalidationRequest
	err   error
}

func (fake *processingRuntimeInvalidationFake) Invalidate(
	_ context.Context,
	request processing.InvalidationRequest,
) (processing.InvalidationResult, error) {
	fake.calls++
	fake.last = request
	return processing.InvalidationResult{}, fake.err
}

type processingRuntimeMetricsFake struct {
	queueCalls         int
	workerCalls        int
	slotCalls          int
	derivedCalls       int
	coverageCalls      int
	activationOutcomes []processing.UpdaterActivationOutcome
	queueValues        map[string]processingRuntimeQueueMetric
}

type processingRuntimeQueueMetric struct {
	count int64
	age   time.Duration
}

func (metrics *processingRuntimeMetricsFake) ObserveJob(processing.PriorityClass, processing.ProcessingState, processing.ProcessingErrorCategory) {
}
func (metrics *processingRuntimeMetricsFake) ObserveLeaseLoss() {}
func (metrics *processingRuntimeMetricsFake) ObserveJobDuration(processing.PriorityClass, processing.ProcessingState, time.Duration) {
}
func (metrics *processingRuntimeMetricsFake) SetWorkers(processing.WorkerTrustClass, processing.WorkerHealthClass, int64) {
	metrics.workerCalls++
}
func (metrics *processingRuntimeMetricsFake) SetSlots(processing.SlotClass, processing.SlotMetricKind, int64) {
	metrics.slotCalls++
}
func (metrics *processingRuntimeMetricsFake) SetQueue(priority processing.PriorityClass, state processing.ProcessingState, count int64, age time.Duration) {
	metrics.queueCalls++
	if metrics.queueValues == nil {
		metrics.queueValues = make(map[string]processingRuntimeQueueMetric)
	}
	metrics.queueValues[string(priority)+"/"+string(state)] = processingRuntimeQueueMetric{count: count, age: age}
}
func (metrics *processingRuntimeMetricsFake) AddSinkBytes(int64) {}
func (metrics *processingRuntimeMetricsFake) SetDerived(processing.DerivedMetricKind, int64) {
	metrics.derivedCalls++
}
func (metrics *processingRuntimeMetricsFake) ObserveDerived(processing.DerivedMetricEvent) {}
func (metrics *processingRuntimeMetricsFake) SetCoverage(string, string, processing.CoverageMetricState, int64) {
	metrics.coverageCalls++
}
func (metrics *processingRuntimeMetricsFake) ObserveUpdaterActivation(outcome processing.UpdaterActivationOutcome) {
	metrics.activationOutcomes = append(metrics.activationOutcomes, outcome)
}

func processingRuntimeRootValidator(context.Context, string) error { return nil }
