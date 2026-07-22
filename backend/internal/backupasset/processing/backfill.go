package processing

import (
	"time"

	"xirang/backend/internal/backupasset"
)

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

type BackfillScoreInput struct {
	Latest     bool
	CapturedAt time.Time
	QueuedAt   time.Time
}

func ScoreBackfill(input BackfillScoreInput, now time.Time, policy BackfillPolicy) int {
	if now.Location() != time.UTC || input.CapturedAt.Location() != time.UTC || input.QueuedAt.Location() != time.UTC ||
		input.CapturedAt.After(now) || input.QueuedAt.After(now) || policy.RecentWindow <= 0 || policy.HistoryAgingStep <= 0 {
		return 0
	}
	captureAge := now.Sub(input.CapturedAt)
	if input.Latest {
		penalty := int(captureAge / (24 * time.Hour))
		if penalty > 100 {
			penalty = 100
		}
		return 1000 - penalty
	}
	if captureAge <= policy.RecentWindow {
		penalty := int(captureAge * 299 / policy.RecentWindow)
		if penalty > 299 {
			penalty = 299
		}
		return 699 - penalty
	}
	historyAge := captureAge - policy.RecentWindow
	penalty := int(historyAge / (24 * time.Hour))
	if penalty > 299 {
		penalty = 299
	}
	score := 300 - penalty
	score += int(now.Sub(input.QueuedAt) / policy.HistoryAgingStep)
	if score > 399 {
		return 399
	}
	if score < 1 {
		return 1
	}
	return score
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

type DerivedReuseIdentity struct {
	Ref                    backupasset.AssetRef
	CatalogGenerationID    string
	SourceFingerprint      string
	EntryFingerprint       string
	FingerprintStrength    string
	Capability             string
	CapabilitySchema       string
	PipelineFingerprint    string
	OutputProfile          string
	SecurityPolicyRevision string
	ParametersDigest       string
}

func CanReuseDerived(existing, requested DerivedReuseIdentity) bool {
	if !validDerivedReuseIdentity(existing) || !validDerivedReuseIdentity(requested) ||
		existing.SourceFingerprint != requested.SourceFingerprint || existing.EntryFingerprint != requested.EntryFingerprint ||
		existing.FingerprintStrength != requested.FingerprintStrength || existing.Capability != requested.Capability ||
		existing.CapabilitySchema != requested.CapabilitySchema || existing.PipelineFingerprint != requested.PipelineFingerprint ||
		existing.OutputProfile != requested.OutputProfile || existing.SecurityPolicyRevision != requested.SecurityPolicyRevision ||
		existing.ParametersDigest != requested.ParametersDigest {
		return false
	}
	if existing.FingerprintStrength == "strong" {
		return true
	}
	return existing.Ref == requested.Ref && existing.CatalogGenerationID == requested.CatalogGenerationID
}

func validDerivedReuseIdentity(value DerivedReuseIdentity) bool {
	if backupasset.ValidateAssetRef(value.Ref) != nil || backupasset.ValidateOpaqueID(value.CatalogGenerationID) != nil ||
		(value.FingerprintStrength != "strong" && value.FingerprintStrength != "weak" && value.FingerprintStrength != "none") {
		return false
	}
	for _, field := range []string{
		value.SourceFingerprint, value.EntryFingerprint, value.Capability, value.CapabilitySchema,
		value.PipelineFingerprint, value.OutputProfile, value.SecurityPolicyRevision, value.ParametersDigest,
	} {
		if field == "" || len(field) > 128 {
			return false
		}
	}
	return true
}
