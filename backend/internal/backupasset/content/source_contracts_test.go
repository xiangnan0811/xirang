package content

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestContentSourceRequestIsClosedAndPrivate(t *testing.T) {
	ref := backupasset.AssetRef{
		RecoveryPointID: strings.Repeat("a", 32),
		EntryID:         strings.Repeat("b", 64),
	}
	valid := []SourceRequest{
		{
			Ref: ref, CatalogGenerationID: strings.Repeat("c", 32),
			ExpectedSource: strings.Repeat("d", 64), ExpectedEntry: strings.Repeat("e", 64),
			Mode: SourceModeStat,
		},
		{
			Ref: ref, CatalogGenerationID: strings.Repeat("c", 32),
			ExpectedSource: strings.Repeat("d", 64), ExpectedEntry: strings.Repeat("e", 64),
			Mode: SourceModeSequential, MaxBytes: 4096,
		},
		{
			Ref: ref, CatalogGenerationID: strings.Repeat("c", 32),
			ExpectedSource: strings.Repeat("d", 64), ExpectedEntry: strings.Repeat("e", 64),
			Mode: SourceModeRange, MaxBytes: 512, Range: &ResolvedRange{Offset: 7, Length: 512},
		},
	}
	for _, request := range valid {
		if err := ValidateSourceRequest(request); err != nil {
			t.Fatalf("valid source request %+v rejected: %v", request, err)
		}
		payload, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("marshal source request: %v", err)
		}
		if string(payload) != "{}" {
			t.Fatalf("private source request escaped to JSON: %s", payload)
		}
	}

	invalid := []SourceRequest{
		{},
		{Ref: ref, CatalogGenerationID: strings.Repeat("c", 32), ExpectedSource: strings.Repeat("d", 64), ExpectedEntry: strings.Repeat("e", 64), Mode: SourceMode("future")},
		{Ref: ref, CatalogGenerationID: strings.Repeat("c", 32), ExpectedSource: strings.Repeat("d", 64), ExpectedEntry: strings.Repeat("e", 64), Mode: SourceModeStat, MaxBytes: 1},
		{Ref: ref, CatalogGenerationID: strings.Repeat("c", 32), ExpectedSource: strings.Repeat("d", 64), ExpectedEntry: strings.Repeat("e", 64), Mode: SourceModeSequential},
		{Ref: ref, CatalogGenerationID: strings.Repeat("c", 32), ExpectedSource: strings.Repeat("d", 64), ExpectedEntry: strings.Repeat("e", 64), Mode: SourceModeSequential, MaxBytes: 1, Range: &ResolvedRange{Length: 1}},
		{Ref: ref, CatalogGenerationID: strings.Repeat("c", 32), ExpectedSource: strings.Repeat("d", 64), ExpectedEntry: strings.Repeat("e", 64), Mode: SourceModeRange, MaxBytes: 1},
		{Ref: ref, CatalogGenerationID: strings.Repeat("c", 32), ExpectedSource: strings.Repeat("d", 64), ExpectedEntry: strings.Repeat("e", 64), Mode: SourceModeRange, MaxBytes: 2, Range: &ResolvedRange{Offset: -1, Length: 2}},
		{Ref: ref, CatalogGenerationID: strings.Repeat("c", 32), ExpectedSource: strings.Repeat("d", 64), ExpectedEntry: strings.Repeat("e", 64), Mode: SourceModeRange, MaxBytes: 2, Range: &ResolvedRange{Offset: 0, Length: 1}},
	}
	for index, request := range invalid {
		if err := ValidateSourceRequest(request); !errors.Is(err, ErrInvalidSourceRequest) {
			t.Fatalf("invalid source request %d got %v", index, err)
		}
	}
}

func TestContentSourceStatAndCapabilitiesDoNotSerializePrivateFacts(t *testing.T) {
	stat := SourceStat{
		Size: 12, MediaType: "text/plain", SourceFingerprint: strings.Repeat("a", 64),
		EntryFingerprint: strings.Repeat("b", 64),
	}
	capabilities := SourceCapabilities{Sequential: true, Range: true}
	for name, value := range map[string]any{"stat": stat, "capabilities": capabilities} {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if string(payload) != "{}" {
			t.Fatalf("private %s escaped to JSON: %s", name, payload)
		}
	}
}
