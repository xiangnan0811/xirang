package processing

import (
	"testing"
	"time"
)

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
