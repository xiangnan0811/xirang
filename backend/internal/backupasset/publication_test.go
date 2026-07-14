package backupasset

import (
	"strings"
	"testing"
	"time"
)

func TestPublicationLineageAndConsistencyCodecsRejectUnsafeOrAmbiguousValues(t *testing.T) {
	startedAt := time.Date(2026, 7, 14, 3, 4, 5, 123456789, time.UTC)
	preparedAt := startedAt.Add(time.Second)
	deadlineAt := preparedAt.Add(2 * time.Hour)
	lineage := PublicationLineageV1{
		Version:              1,
		TaskRepositoryLinkID: strings.Repeat("a", 32),
		TaskID:               41,
		TaskRunID:            42,
		Trigger:              "manual",
		ChainRunIDPresent:    true,
		ChainRunIDDigest:     strings.Repeat("b", 64),
		PublicationMode:      string(PublicationNativeSnapshot),
		PointCodecVersion:    1,
		TagCodecVersion:      1,
		StartedAt:            startedAt,
		PreparedAt:           preparedAt,
		PointDeadlineAt:      deadlineAt,
	}
	encodedLineage, err := EncodePublicationLineage(lineage)
	if err != nil {
		t.Fatalf("encode lineage: %v", err)
	}
	for _, unsafe := range []string{
		"FAKE_REPOSITORY_LOCATOR_FOR_TEST_ONLY",
		"FAKE_NATIVE_SNAPSHOT_ID_FOR_TEST_ONLY",
		"xirang.link.v1.FAKE_TAG_FOR_TEST_ONLY",
		"xirang.point.v1.FAKE_TAG_FOR_TEST_ONLY",
	} {
		if strings.Contains(encodedLineage, unsafe) {
			t.Fatalf("lineage codec leaked %q: %s", unsafe, encodedLineage)
		}
	}
	decodedLineage, err := DecodePublicationLineage(encodedLineage)
	if err != nil {
		t.Fatalf("decode lineage: %v", err)
	}
	if !decodedLineage.StartedAt.Equal(startedAt) || !decodedLineage.PreparedAt.Equal(preparedAt) || !decodedLineage.PointDeadlineAt.Equal(deadlineAt) {
		t.Fatalf("lineage timestamps lost UTC nanoseconds: %+v", decodedLineage)
	}
	if !decodedLineage.StartedAt.Before(decodedLineage.PreparedAt) || !decodedLineage.PreparedAt.Before(decodedLineage.PointDeadlineAt) {
		t.Fatalf("lineage time ordering drifted: %+v", decodedLineage)
	}
	if _, err := DecodePublicationLineage(encodedLineage + "{}"); err == nil {
		t.Fatal("lineage codec accepted trailing JSON")
	}
	unknownLineage := strings.TrimSuffix(encodedLineage, "}") + `,"raw_locator":"FAKE_REPOSITORY_LOCATOR_FOR_TEST_ONLY"}`
	if _, err := DecodePublicationLineage(unknownLineage); err == nil {
		t.Fatal("lineage codec accepted an unknown raw locator field")
	}
	nonUTCLineage := strings.Replace(encodedLineage, startedAt.Format(time.RFC3339Nano), "2026-07-14T11:04:05.123456789+08:00", 1)
	if _, err := DecodePublicationLineage(nonUTCLineage); err == nil {
		t.Fatal("lineage codec accepted a non-UTC persisted timestamp")
	}
	invalidOrdering := lineage
	invalidOrdering.PreparedAt = startedAt.Add(-time.Nanosecond)
	if _, err := EncodePublicationLineage(invalidOrdering); err == nil {
		t.Fatal("lineage codec accepted non-increasing timestamps")
	}

	consistency := PublicationConsistencyV1{
		Version:                  1,
		PublicationRevision:      2,
		AttemptCount:             3,
		Completion:               CompletionKnownExitZero,
		Code:                     FailureEvidenceMissingSummary,
		CaptureStartedAt:         &startedAt,
		CaptureFinishedAt:        &preparedAt,
		FilesProcessed:           7,
		LogicalBytes:             16384,
		Provider:                 ProviderRestic,
		RepositoryIdentityDigest: strings.Repeat("c", 64),
		RequestedTagDigest:       strings.Repeat("d", 64),
		ProviderCommitDigest:     strings.Repeat("e", 64),
		AdapterRevision:          "restic-publication-v1",
		CapabilityRevision:       1,
	}
	encodedConsistency, err := EncodePublicationConsistency(consistency)
	if err != nil {
		t.Fatalf("encode consistency: %v", err)
	}
	decodedConsistency, err := DecodePublicationConsistency(encodedConsistency)
	if err != nil {
		t.Fatalf("decode consistency: %v", err)
	}
	if decodedConsistency.ProviderCommitDigest != consistency.ProviderCommitDigest || decodedConsistency.CaptureStartedAt == nil || !decodedConsistency.CaptureStartedAt.Equal(startedAt) {
		t.Fatalf("consistency round trip drifted: %+v", decodedConsistency)
	}
	if _, err := DecodePublicationConsistency(encodedConsistency + "{}"); err == nil {
		t.Fatal("consistency codec accepted trailing JSON")
	}
	unknownConsistency := strings.TrimSuffix(encodedConsistency, "}") + `,"native_snapshot":"FAKE_NATIVE_SNAPSHOT_ID_FOR_TEST_ONLY"}`
	if _, err := DecodePublicationConsistency(unknownConsistency); err == nil {
		t.Fatal("consistency codec accepted an unknown native snapshot field")
	}
}

func TestPublicationDeferralValidation(t *testing.T) {
	tests := []struct {
		name  string
		value ProviderCompletionClass
		code  PublicationFailureCode
		valid bool
	}{
		{"exit zero evidence defect", CompletionKnownExitZero, FailureEvidenceMissingSummary, true},
		{"unknown timeout", CompletionOutcomeUnknown, FailureProviderTimeout, true},
		{"unknown cancellation", CompletionOutcomeUnknown, FailureProviderCanceled, true},
		{"unknown resource limit", CompletionOutcomeUnknown, FailureProviderResourceLimit, true},
		{"unknown command lifecycle", CompletionOutcomeUnknown, FailureProviderOutcomeUnknown, true},
		{"abandoned joined command", CompletionOutcomeUnknown, FailurePublicationSessionAbandoned, true},
		{"nonzero cannot defer", CompletionKnownNonzero, FailureProviderNonzeroExit, false},
		{"exit zero cannot use timeout", CompletionKnownExitZero, FailureProviderTimeout, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidatePublicationDeferral(test.value, test.code)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
}

func TestPublicationConsistencyAllowsTerminalPublicationCodeWithoutProviderCompletion(t *testing.T) {
	value := PublicationConsistencyV1{
		Version: 1,
		Code:    FailureProviderCompletionUnproven,
	}
	encoded, err := EncodePublicationConsistency(value)
	if err != nil {
		t.Fatalf("encode terminal publication code: %v", err)
	}
	decoded, err := DecodePublicationConsistency(encoded)
	if err != nil || decoded.Completion != "" || decoded.Code != FailureProviderCompletionUnproven {
		t.Fatalf("terminal publication code round trip=%+v err=%v", decoded, err)
	}
	if _, err := EncodePublicationConsistency(PublicationConsistencyV1{Version: 1, Code: FailureEvidenceMissingSummary}); err == nil {
		t.Fatal("exit-zero evidence code was accepted without its completion class")
	}
}

func TestPublicationAuditContextRequiresSafeUserOrSystemIdentityAndCorrelation(t *testing.T) {
	user := PublicationAuditContext{
		Actor:         AuditActor{UserID: 7, Username: "operator", Role: "operator"},
		CorrelationID: "corr.publication-7",
	}
	if err := ValidatePublicationAuditContext(user); err != nil {
		t.Fatalf("valid user publication audit context rejected: %v", err)
	}
	system, err := NewSystemPublicationAuditContext("publication-worker")
	if err != nil {
		t.Fatalf("create system publication audit context: %v", err)
	}
	if system.Actor != (AuditActor{Username: "system", Role: "system"}) || system.CorrelationID != "publication-worker" {
		t.Fatalf("unexpected system audit context: %+v", system)
	}
	for _, invalid := range []PublicationAuditContext{
		{Actor: AuditActor{UserID: 0, Username: "operator", Role: "operator"}, CorrelationID: "corr"},
		{Actor: AuditActor{UserID: 7, Username: "system", Role: "system"}, CorrelationID: "corr"},
		{Actor: AuditActor{Username: "system", Role: "system"}, CorrelationID: "invalid space"},
		{Actor: AuditActor{Username: "system", Role: "system"}, CorrelationID: strings.Repeat("a", 65)},
	} {
		if err := ValidatePublicationAuditContext(invalid); err == nil {
			t.Fatalf("unsafe publication audit context accepted: %+v", invalid)
		}
	}
}
