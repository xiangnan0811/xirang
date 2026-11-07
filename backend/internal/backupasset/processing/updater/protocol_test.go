package updater

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUpdaterProtocolAcceptsPullAndActivationReport(t *testing.T) {
	pullPayload := []byte(`{"schema_version":1,"active_fingerprint":"` + strings.Repeat("a", 64) + `"}`)
	if _, err := DecodePullActivationRequest(pullPayload); err != nil {
		t.Fatalf("decode pull: %v", err)
	}
	reportPayload, err := json.Marshal(ActivationReportRequest{SchemaVersion: 1, Receipt: ActivationReceipt{
		SchemaVersion: 1, CandidateID: strings.Repeat("1", 32), OldFingerprint: strings.Repeat("a", 64),
		NewFingerprint: strings.Repeat("b", 64), State: "swapped",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeActivationReportRequest(reportPayload); err != nil {
		t.Fatalf("decode activation report: %v", err)
	}
}

func TestUpdaterProtocolRejectsInvalidPullAndActivationReport(t *testing.T) {
	badPulls := [][]byte{
		[]byte(`{"schema_version":1,"active_fingerprint":"latest"}`),
		[]byte(`{"schema_version":1,"active_fingerprint":null}`),
		[]byte(`{"schema_version":1,"active_fingerprint":"","url":"https://example.test"}`),
	}
	for index, payload := range badPulls {
		if _, err := DecodePullActivationRequest(payload); !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("invalid pull %d error=%v", index, err)
		}
	}

	badReport := []byte(`{"schema_version":1,"receipt":{"schema_version":1,"candidate_id":"` + strings.Repeat("1", 32) +
		`","old_fingerprint":"","new_fingerprint":"` + strings.Repeat("b", 64) + `","state":"executed"}}`)
	if _, err := DecodeActivationReportRequest(badReport); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("open-ended report error=%v", err)
	}
}

func TestUpdaterProtocolValidatesOpaqueResultsAndDirectives(t *testing.T) {
	directive := ActivationDirective{
		SchemaVersion: 1, CandidateID: strings.Repeat("1", 32),
		ExpectedOldFingerprint: strings.Repeat("a", 64), NewFingerprint: strings.Repeat("b", 64),
	}
	if err := ValidateActivationDirective(directive); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRegisterCandidateResult(RegisterCandidateResult{
		SchemaVersion: 1, CandidateID: strings.Repeat("2", 32),
	}); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePullActivationResult(PullActivationResult{SchemaVersion: 1, RetryAfterSeconds: 5, Directive: &directive}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateActivationReportResult(ActivationReportResult{
		SchemaVersion: 1, Decision: ActivationDecisionCommit, ActiveFingerprint: directive.NewFingerprint,
	}); err != nil {
		t.Fatal(err)
	}

	invalid := directive
	invalid.NewFingerprint = invalid.ExpectedOldFingerprint
	if err := ValidateActivationDirective(invalid); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("same-fingerprint directive error=%v", err)
	}
	if err := ValidatePullActivationResult(PullActivationResult{SchemaVersion: 1, RetryAfterSeconds: 0}); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("unbounded retry result error=%v", err)
	}
	if err := ValidateActivationReportResult(ActivationReportResult{
		SchemaVersion: 1, Decision: "maybe", ActiveFingerprint: directive.NewFingerprint,
	}); !errors.Is(err, ErrProtocolInvalid) {
		t.Fatalf("open decision error=%v", err)
	}
}

func protocolTestReceipt(now time.Time) InboxReceipt {
	return InboxReceipt{
		SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "offline.default", Version: "1.2.3",
		ManifestDigest: strings.Repeat("a", 64), SigningKeyFingerprint: strings.Repeat("b", 64),
		BundleFingerprint: strings.Repeat("c", 64), BundleSHA256: strings.Repeat("d", 64), VerifiedAt: now,
		Capabilities: []ManifestCapability{{
			Capability: "image.ocr", Schema: "image.ocr.v1", Profiles: []string{"tesseract_text_v1"},
			ToolRevision: "tesseract-5", ModelRevision: "model-v1", DataRevision: "none",
		}},
	}
}
