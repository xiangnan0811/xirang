package processing

import "time"

type BackfillPolicy struct {
	Paused                bool
	BatchSize             int
	JobsPerHour           int
	BytesPerHour          int64
	ProviderConcurrency   int
	CapabilityConcurrency int
	RecentWindow          time.Duration
	HistoryAgingStep      time.Duration
}

type BackfillUsage struct {
	WindowStartedAt  time.Time
	Jobs             int
	Bytes            int64
	ProviderActive   map[string]int
	CapabilityActive map[string]int
}

type BackfillAdmissionRequest struct {
	PriorityClass  PriorityClass
	Provider       string
	Capability     string
	EstimatedBytes int64
}

type BackfillAdmissionReason string

const (
	BackfillAdmissionAllowed         BackfillAdmissionReason = "allowed"
	BackfillAdmissionInteractive     BackfillAdmissionReason = "interactive"
	BackfillAdmissionPaused          BackfillAdmissionReason = "paused"
	BackfillAdmissionJobQuota        BackfillAdmissionReason = "job_quota"
	BackfillAdmissionByteQuota       BackfillAdmissionReason = "byte_quota"
	BackfillAdmissionProviderQuota   BackfillAdmissionReason = "provider_quota"
	BackfillAdmissionCapabilityQuota BackfillAdmissionReason = "capability_quota"
	BackfillAdmissionInvalid         BackfillAdmissionReason = "invalid"
)

type BackfillAdmission struct {
	Allowed bool
	Reason  BackfillAdmissionReason
}

func AdmitBackfill(policy BackfillPolicy, usage BackfillUsage, request BackfillAdmissionRequest, now time.Time) BackfillAdmission {
	if !validBackfillPolicy(policy) || now.Location() != time.UTC || usage.WindowStartedAt.Location() != time.UTC ||
		usage.WindowStartedAt.After(now) || request.EstimatedBytes < 0 || request.Provider == "" || request.Capability == "" ||
		(request.PriorityClass != PriorityInteractive && request.PriorityClass != PriorityBackground) {
		return BackfillAdmission{Reason: BackfillAdmissionInvalid}
	}
	if request.PriorityClass == PriorityInteractive {
		return BackfillAdmission{Allowed: true, Reason: BackfillAdmissionInteractive}
	}
	if policy.Paused {
		return BackfillAdmission{Reason: BackfillAdmissionPaused}
	}
	if usage.ProviderActive[request.Provider] >= policy.ProviderConcurrency {
		return BackfillAdmission{Reason: BackfillAdmissionProviderQuota}
	}
	if usage.CapabilityActive[request.Capability] >= policy.CapabilityConcurrency {
		return BackfillAdmission{Reason: BackfillAdmissionCapabilityQuota}
	}
	jobs, consumedBytes := usage.Jobs, usage.Bytes
	if now.Sub(usage.WindowStartedAt) >= time.Hour {
		jobs, consumedBytes = 0, 0
	}
	if jobs >= policy.JobsPerHour {
		return BackfillAdmission{Reason: BackfillAdmissionJobQuota}
	}
	if request.EstimatedBytes > policy.BytesPerHour-consumedBytes {
		return BackfillAdmission{Reason: BackfillAdmissionByteQuota}
	}
	return BackfillAdmission{Allowed: true, Reason: BackfillAdmissionAllowed}
}

func validBackfillPolicy(policy BackfillPolicy) bool {
	return policy.BatchSize > 0 && policy.BatchSize <= 10000 && policy.JobsPerHour > 0 && policy.BytesPerHour > 0 &&
		policy.ProviderConcurrency > 0 && policy.CapabilityConcurrency > 0 && policy.RecentWindow > 0 &&
		policy.HistoryAgingStep > 0
}
