package updater

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUpdaterProtocolAcceptsOnlyClosedSanitizedMessages(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	receipt := protocolTestReceipt(now)
	registration := RegisterCandidateRequest{SchemaVersion: 1, Receipt: receipt}
	payload, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRegisterCandidateRequest(payload)
	if err != nil || decoded.Receipt.BundleFingerprint != receipt.BundleFingerprint {
		t.Fatalf("registration decoded=%+v err=%v", decoded, err)
	}
	for _, forbidden := range []string{"path", "filename", "content", "signature\"", "credential", "bundle_bytes", "worker_id", "attempt_id", "fence"} {
		if strings.Contains(strings.ToLower(string(payload)), forbidden) {
			t.Fatalf("protocol payload leaked %q: %s", forbidden, payload)
		}
	}

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

func TestUpdaterProtocolRejectsUnknownDuplicateMalformedAndOpenEndedFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	base, err := json.Marshal(RegisterCandidateRequest{SchemaVersion: 1, Receipt: protocolTestReceipt(now)})
	if err != nil {
		t.Fatal(err)
	}
	invalid := [][]byte{
		append([]byte(nil), base[:len(base)-1]...),
		[]byte(`{"schema_version":1,"schema_version":1,"receipt":{}}`),
		[]byte(`{"schema_version":1,"receipt":{},"path":"/private/inbox"}`),
		[]byte{'{', '"', 0xff, '"', ':', '1', '}'},
	}
	for index, payload := range invalid {
		if _, err := DecodeRegisterCandidateRequest(payload); !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("invalid registration %d error=%v payload=%q", index, err, payload)
		}
	}

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
