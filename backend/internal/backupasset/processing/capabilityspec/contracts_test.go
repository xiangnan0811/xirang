package capabilityspec

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDiagnosticIsClosedBoundedAndSecretSafe(t *testing.T) {
	diagnostic := Diagnostic{
		Failure: FailureUnsupportedFormat,
		Reason:  ReasonMIMENotAllowlisted,
		Params:  map[string]int64{"limit": 64 << 20, "observed": 65 << 20},
	}
	if err := diagnostic.Validate(); err != nil {
		t.Fatalf("valid diagnostic: %v", err)
	}
	payload, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > MaxDiagnosticBytes {
		t.Fatalf("diagnostic exceeds bound: %d", len(payload))
	}
	for _, forbidden := range []string{"path", "filename", "credential", "stdout", "stderr", "worker_id", "attempt_id", "fence"} {
		if strings.Contains(strings.ToLower(string(payload)), forbidden) {
			t.Fatalf("diagnostic leaked %q: %s", forbidden, payload)
		}
	}

	invalid := []Diagnostic{
		{Failure: "tool said /tmp/source.txt failed", Reason: ReasonToolExit},
		{Failure: FailureWorkerCrash, Reason: "raw stderr"},
		{Failure: FailureWorkerCrash, Reason: ReasonToolExit, Params: map[string]int64{"pid": 42}},
		{Failure: FailureWorkerCrash, Reason: ReasonToolExit, Params: map[string]int64{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5}},
	}
	for index, value := range invalid {
		if err := value.Validate(); !errors.Is(err, ErrInvalidContract) {
			t.Fatalf("invalid diagnostic %d error=%v", index, err)
		}
	}
}

func TestPositiveMalwareFindingIsSuccessfulOutcome(t *testing.T) {
	result := MalwareResult{
		SchemaVersion:              1,
		EngineFamily:               "clamav",
		SignatureBundleFingerprint: strings.Repeat("a", 64),
		Result:                     ScanFinding,
		FindingCategory:            "test_signature",
		ScannedBytes:               128,
		Completeness:               CoverageComplete,
		ScannedAt:                  "2026-07-20T00:00:00Z",
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("positive finding must be valid evidence: %v", err)
	}
	if result.ProcessingOutcome() != OutcomeSucceeded {
		t.Fatalf("finding outcome=%q, want succeeded", result.ProcessingOutcome())
	}
}

func TestDeclaredAndSniffedMediaMustMatchClosedRules(t *testing.T) {
	profile, ok := Lookup(CapabilityImageThumbnail, ProfileRasterThumbnailV1, false)
	if !ok {
		t.Fatal("image profile missing")
	}
	if err := profile.ValidateMedia("image/png", "image/png"); err != nil {
		t.Fatalf("matching safe raster rejected: %v", err)
	}
	for _, pair := range [][2]string{
		{"image/png", "text/html"},
		{"image/svg+xml", "image/svg+xml"},
		{"text/html", "text/html"},
		{"application/octet-stream", "image/png"},
	} {
		if err := profile.ValidateMedia(pair[0], pair[1]); !errors.Is(err, ErrUnsupportedMedia) {
			t.Fatalf("media pair %q/%q error=%v", pair[0], pair[1], err)
		}
	}
}
