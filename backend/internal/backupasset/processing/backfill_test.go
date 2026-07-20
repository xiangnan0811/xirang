package processing

import (
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestBackfillScoreBandsOrderingAndHistoryAging(t *testing.T) {
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	policy := BackfillPolicy{RecentWindow: 30 * 24 * time.Hour, HistoryAgingStep: 24 * time.Hour}
	latestNew := ScoreBackfill(BackfillScoreInput{Latest: true, CapturedAt: now.Add(-time.Hour), QueuedAt: now.Add(-time.Hour)}, now, policy)
	latestOld := ScoreBackfill(BackfillScoreInput{Latest: true, CapturedAt: now.Add(-20 * 24 * time.Hour), QueuedAt: now.Add(-time.Hour)}, now, policy)
	recentNew := ScoreBackfill(BackfillScoreInput{CapturedAt: now.Add(-24 * time.Hour), QueuedAt: now.Add(-time.Hour)}, now, policy)
	recentOld := ScoreBackfill(BackfillScoreInput{CapturedAt: now.Add(-20 * 24 * time.Hour), QueuedAt: now.Add(-time.Hour)}, now, policy)
	historyNew := ScoreBackfill(BackfillScoreInput{CapturedAt: now.Add(-31 * 24 * time.Hour), QueuedAt: now.Add(-time.Hour)}, now, policy)
	historyAged := ScoreBackfill(BackfillScoreInput{CapturedAt: now.Add(-365 * 24 * time.Hour), QueuedAt: now.Add(-1000 * 24 * time.Hour)}, now, policy)

	if latestNew < 900 || latestNew > 1000 || latestOld < 900 || latestOld >= latestNew {
		t.Fatalf("latest scores new=%d old=%d", latestNew, latestOld)
	}
	if recentNew < 400 || recentNew > 699 || recentOld < 400 || recentOld >= recentNew {
		t.Fatalf("recent scores new=%d old=%d", recentNew, recentOld)
	}
	if historyNew < 1 || historyNew > 399 || historyAged != 399 {
		t.Fatalf("history scores new=%d aged=%d", historyNew, historyAged)
	}
	if latestOld <= recentNew || recentOld <= historyNew {
		t.Fatalf("score bands crossed: latest=%d recent=%d history=%d", latestOld, recentOld, historyNew)
	}
}

func TestBackfillAdmissionHonorsPauseQuotaAndInteractiveBypass(t *testing.T) {
	now := time.Date(2026, 7, 20, 8, 0, 0, 0, time.UTC)
	policy := BackfillPolicy{
		Paused: true, BatchSize: 10, JobsPerHour: 2, BytesPerHour: 100,
		ProviderConcurrency: 1, CapabilityConcurrency: 1,
		RecentWindow: 30 * 24 * time.Hour, HistoryAgingStep: 24 * time.Hour,
	}
	usage := BackfillUsage{
		WindowStartedAt: now.Add(-time.Minute), Jobs: 1, Bytes: 40,
		ProviderActive: map[string]int{"restic": 1}, CapabilityActive: map[string]int{"image.thumbnail": 1},
	}
	request := BackfillAdmissionRequest{PriorityClass: PriorityBackground, Provider: "restic", Capability: "image.thumbnail", EstimatedBytes: 20}
	if admission := AdmitBackfill(policy, usage, request, now); admission.Allowed || admission.Reason != BackfillAdmissionPaused {
		t.Fatalf("paused admission=%+v", admission)
	}
	request.PriorityClass = PriorityInteractive
	if admission := AdmitBackfill(policy, usage, request, now); !admission.Allowed || admission.Reason != BackfillAdmissionInteractive {
		t.Fatalf("interactive admission=%+v", admission)
	}

	policy.Paused = false
	request.PriorityClass = PriorityBackground
	if admission := AdmitBackfill(policy, usage, request, now); admission.Allowed || admission.Reason != BackfillAdmissionProviderQuota {
		t.Fatalf("provider quota admission=%+v", admission)
	}
	usage.ProviderActive["restic"] = 0
	if admission := AdmitBackfill(policy, usage, request, now); admission.Allowed || admission.Reason != BackfillAdmissionCapabilityQuota {
		t.Fatalf("capability quota admission=%+v", admission)
	}
	usage.CapabilityActive["image.thumbnail"] = 0
	usage.Jobs = 2
	if admission := AdmitBackfill(policy, usage, request, now); admission.Allowed || admission.Reason != BackfillAdmissionJobQuota {
		t.Fatalf("job quota admission=%+v", admission)
	}
	usage.Jobs = 1
	usage.Bytes = 90
	if admission := AdmitBackfill(policy, usage, request, now); admission.Allowed || admission.Reason != BackfillAdmissionByteQuota {
		t.Fatalf("byte quota admission=%+v", admission)
	}
	usage.Bytes = 40
	if admission := AdmitBackfill(policy, usage, request, now); !admission.Allowed || admission.Reason != BackfillAdmissionAllowed {
		t.Fatalf("eligible admission=%+v", admission)
	}
}

func TestDerivedReuseRequiresStrongCrossPointOrExactWeakBinding(t *testing.T) {
	base := DerivedReuseIdentity{
		Ref:                 backupasset.AssetRef{RecoveryPointID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EntryID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		CatalogGenerationID: "cccccccccccccccccccccccccccccccc", SourceFingerprint: "source-v1", EntryFingerprint: "entry-v1",
		FingerprintStrength: "strong", Capability: "image.thumbnail", CapabilitySchema: "image.thumbnail.v1",
		PipelineFingerprint: "pipeline-v1", OutputProfile: "raster_thumbnail_v1", SecurityPolicyRevision: "policy-v1",
		ParametersDigest: "parameters-v1",
	}
	crossPoint := base
	crossPoint.Ref.RecoveryPointID = "dddddddddddddddddddddddddddddddd"
	crossPoint.Ref.EntryID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	crossPoint.CatalogGenerationID = "ffffffffffffffffffffffffffffffff"
	if !CanReuseDerived(base, crossPoint) {
		t.Fatal("identical strong fingerprints did not permit cross-point physical reuse")
	}
	differentProfile := crossPoint
	differentProfile.OutputProfile = "other"
	if CanReuseDerived(base, differentProfile) {
		t.Fatal("output profile mismatch reused Derived output")
	}
	weak := base
	weak.FingerprintStrength = "weak"
	weakCrossPoint := crossPoint
	weakCrossPoint.FingerprintStrength = "weak"
	if CanReuseDerived(weak, weakCrossPoint) {
		t.Fatal("weak fingerprint reused across AssetRef")
	}
	weakExact := weak
	if !CanReuseDerived(weak, weakExact) {
		t.Fatal("weak fingerprint did not reuse within exact active binding")
	}
}
