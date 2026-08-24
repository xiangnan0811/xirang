package backupasset

import (
	"errors"
	"reflect"
	"testing"
)

func TestContentConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot(t *testing.T) {
	values := cloneFoundationTestValues(staticFoundationDefaults)
	values["backup_assets.enabled"] = "true"
	values["backup_assets.content_preview_ttl"] = "3m"
	values["backup_assets.content_allow_insecure_loopback"] = "true"
	wantValues := cloneFoundationTestValues(values)
	reader := &snapshotSettingsReader{values: values}

	current, err := NewFoundationService(reader).ContentConfig()
	if err != nil {
		t.Fatalf("ContentConfig: %v", err)
	}
	prospective, err := ContentConfigFromValues(values)
	if err != nil {
		t.Fatalf("ContentConfigFromValues: %v", err)
	}
	if !reflect.DeepEqual(current, prospective) {
		t.Fatalf("current Content config differs from prospective parser:\ncurrent=%+v\nprospective=%+v", current, prospective)
	}
	if reader.effectiveReads != 0 || reader.snapshotReads != 1 {
		t.Fatalf("ContentConfig reads: effective=%d snapshot=%d, want one atomic snapshot", reader.effectiveReads, reader.snapshotReads)
	}
	if !reflect.DeepEqual(values, wantValues) {
		t.Fatal("ContentConfigFromValues mutated its input")
	}
}

func TestSearchOverlayConfigFromValuesMatchesCurrentGetterFromOneAtomicSnapshot(t *testing.T) {
	values := cloneFoundationTestValues(staticFoundationDefaults)
	values["backup_assets.enabled"] = "true"
	values["backup_assets.search_candidate_limit"] = "20000"
	values["backup_assets.search_page_size_max"] = "300"
	values["backup_assets.favorite_quota"] = "6000"
	wantValues := cloneFoundationTestValues(values)
	reader := &snapshotSettingsReader{values: values}

	currentSearch, currentOverlay, err := NewFoundationService(reader).SearchOverlayConfig()
	if err != nil {
		t.Fatalf("SearchOverlayConfig: %v", err)
	}
	prospectiveSearch, prospectiveOverlay, err := SearchOverlayConfigFromValues(values)
	if err != nil {
		t.Fatalf("SearchOverlayConfigFromValues: %v", err)
	}
	if !reflect.DeepEqual(currentSearch, prospectiveSearch) || !reflect.DeepEqual(currentOverlay, prospectiveOverlay) {
		t.Fatalf(
			"current Search/Overlay config differs from prospective parser:\ncurrent=%+v %+v\nprospective=%+v %+v",
			currentSearch, currentOverlay, prospectiveSearch, prospectiveOverlay,
		)
	}
	if reader.effectiveReads != 0 || reader.snapshotReads != 1 {
		t.Fatalf("SearchOverlayConfig reads: effective=%d snapshot=%d, want one atomic snapshot", reader.effectiveReads, reader.snapshotReads)
	}
	if !reflect.DeepEqual(values, wantValues) {
		t.Fatal("SearchOverlayConfigFromValues mutated its input")
	}
}

func TestProspectiveConfigParsersRequireCompleteValidatedFoundationValues(t *testing.T) {
	t.Run("Content missing key", func(t *testing.T) {
		values := cloneFoundationTestValues(staticFoundationDefaults)
		delete(values, "backup_assets.search_candidate_limit")
		if _, err := ContentConfigFromValues(values); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("ContentConfigFromValues error=%v, want ErrInvalidState", err)
		}
	})

	t.Run("Content rejects invalid cross-domain combination", func(t *testing.T) {
		values := cloneFoundationTestValues(staticFoundationDefaults)
		values["backup_assets.search_ast_max_depth"] = "9"
		values["backup_assets.search_ast_max_nodes"] = "8"
		if _, err := ContentConfigFromValues(values); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("ContentConfigFromValues error=%v, want ErrInvalidState", err)
		}
	})

	t.Run("Search and Overlay missing key", func(t *testing.T) {
		values := cloneFoundationTestValues(staticFoundationDefaults)
		delete(values, "backup_assets.content_ticket_timeout")
		if _, _, err := SearchOverlayConfigFromValues(values); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("SearchOverlayConfigFromValues error=%v, want ErrInvalidState", err)
		}
	})

	t.Run("Search and Overlay reject invalid cross-domain combination", func(t *testing.T) {
		values := cloneFoundationTestValues(staticFoundationDefaults)
		values["backup_assets.content_request_max_bytes"] = "1073741824"
		values["backup_assets.content_cumulative_max_bytes"] = "536870912"
		if _, _, err := SearchOverlayConfigFromValues(values); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("SearchOverlayConfigFromValues error=%v, want ErrInvalidState", err)
		}
	})
}

func TestFoundationTransitionConfigFromValuesBuildsCompleteTypedBundle(t *testing.T) {
	values := cloneFoundationTestValues(staticFoundationDefaults)
	values["backup_assets.enabled"] = "true"
	values["backup_assets.content_preview_ttl"] = "3m"
	values["backup_assets.search_candidate_limit"] = "20000"
	values["backup_assets.export.enabled"] = "true"
	values["backup_assets.recovery.enabled"] = "true"

	bundle, err := FoundationTransitionConfigFromValues(values)
	if err != nil {
		t.Fatalf("FoundationTransitionConfigFromValues: %v", err)
	}
	if !bundle.Enabled || !bundle.Content.Enabled || !bundle.Search.Enabled || !bundle.Overlay.Enabled ||
		!bundle.Export.Enabled || !bundle.Recovery.Enabled {
		t.Fatalf("incomplete prospective transition bundle: %+v", bundle)
	}
	if bundle.Content.PreviewTTL.String() != "3m0s" || bundle.Search.CandidateLimit != 20000 {
		t.Fatalf("prospective values missing from transition bundle: %+v", bundle)
	}

	delete(values, "backup_assets.recovery.scan_limit")
	if _, err := FoundationTransitionConfigFromValues(values); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("incomplete transition bundle error=%v, want ErrInvalidState", err)
	}
}
