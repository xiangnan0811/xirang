package catalog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestCatalogContractsClosedEnumsFailClosed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		value string
		parse func(string) error
	}{
		{"generation", "complete", func(value string) error { _, err := ParseGenerationState(value); return err }},
		{"coverage", "complete", func(value string) error { _, err := ParseCoverageStatus(value); return err }},
		{"staleness", "fresh", func(value string) error { _, err := ParseStalenessStatus(value); return err }},
		{"fingerprint", "strong", func(value string) error { _, err := ParseFingerprintStrength(value); return err }},
		{"evidence", "recorded", func(value string) error { _, err := ParseEvidenceLayerStatus(value); return err }},
		{"diff", "type_changed", func(value string) error { _, err := ParseDiffChangeKind(value); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.parse(test.value); err != nil {
				t.Fatalf("parse valid %s: %v", test.name, err)
			}
			if err := test.parse("future_internal_value"); !errors.Is(err, ErrUnknownInternalState) {
				t.Fatalf("unknown %s error=%v", test.name, err)
			}
		})
	}
}

func TestCatalogContractsStatusKeepsDimensionsAndKnownZeroSeparate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	zero := int64(0)
	status := StatusDTO{
		Generation: &GenerationDTO{
			ID: "11111111111111111111111111111111", Sequence: 4,
			State: GenerationComplete, StartedAt: now, FinishedAt: &now,
		},
		Coverage: CoverageDTO{
			Status: CoverageComplete, IndexedEntries: 0, ExpectedEntries: &zero,
			ManifestDigest: strings.Repeat("a", 64), ObservedAt: now,
		},
		Staleness: StalenessDTO{Status: StalenessFresh, ObservedAt: &now},
		ContentAvailability: ContentAvailabilityDTO{
			Available: false,
			Reason:    &backupasset.CapabilityReason{Code: backupasset.CapabilityRepositoryOffline},
		},
		Permissions: PermissionsDTO{List: true},
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("validate status: %v", err)
	}
	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	text := string(payload)
	for _, expected := range []string{`"state":"complete"`, `"status":"complete"`, `"status":"fresh"`, `"expected_entries":0`, `"available":false`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("payload missing %s: %s", expected, text)
		}
	}
	if strings.Contains(text, `"coverage":{"state"`) || strings.Contains(text, `"staleness":{"available"`) {
		t.Fatalf("orthogonal dimensions were merged: %s", text)
	}

	status.Coverage.ExpectedEntries = nil
	payload, err = json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal unknown expected count: %v", err)
	}
	if !strings.Contains(string(payload), `"expected_entries":null`) {
		t.Fatalf("unknown expected count must be null: %s", payload)
	}
}

func TestCatalogContractsEntrySerializationUsesCompositeIdentityAndHidesPrivateFacts(t *testing.T) {
	t.Parallel()

	entry := EntryDTO{
		RecoveryPointID:     "11111111111111111111111111111111",
		EntryID:             strings.Repeat("a", 64),
		Name:                "report.txt",
		EntryType:           backupasset.CatalogEntryFile,
		Size:                42,
		FingerprintStrength: FingerprintStrong,
		Breadcrumb: []BreadcrumbDTO{{
			RecoveryPointID: "11111111111111111111111111111111",
			EntryID:         strings.Repeat("b", 64),
			Name:            "docs",
		}},
	}
	if err := entry.Validate(); err != nil {
		t.Fatalf("validate entry: %v", err)
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	text := string(payload)
	for _, expected := range []string{`"recovery_point_id"`, `"entry_id"`, `"breadcrumb"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("payload missing %s: %s", expected, text)
		}
	}
	for _, forbidden := range []string{"normalized_path", "fingerprint\"", "provider_locator", "source_fingerprint"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("payload exposed %s: %s", forbidden, text)
		}
	}
}
