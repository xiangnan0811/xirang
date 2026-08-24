package runtime

import (
	"errors"
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestFoundationTransitionConfigsFromValuesBuildsPriorAndProspectiveBeforeRuntimeWork(t *testing.T) {
	priorValues := runtimeFoundationSettings(false)
	prospectiveValues := make(map[string]string, len(priorValues))
	for key, value := range priorValues {
		prospectiveValues[key] = value
	}
	prospectiveValues["backup_assets.enabled"] = "true"
	prospectiveValues["backup_assets.content_preview_ttl"] = "3m"
	prospectiveValues["backup_assets.search_candidate_limit"] = "20000"

	configs, err := foundationTransitionConfigsFromValues(priorValues, prospectiveValues)
	if err != nil {
		t.Fatalf("foundationTransitionConfigsFromValues: %v", err)
	}
	if configs.Prior.Enabled || !configs.Prospective.Enabled {
		t.Fatalf("transition enabled states: prior=%t prospective=%t", configs.Prior.Enabled, configs.Prospective.Enabled)
	}
	if configs.Prospective.Content.PreviewTTL.String() != "3m0s" || configs.Prospective.Search.CandidateLimit != 20000 {
		t.Fatalf("prospective transition config=%+v", configs.Prospective)
	}
	if configs.Prior.Content.PreviewTTL == configs.Prospective.Content.PreviewTTL ||
		configs.Prior.Search.CandidateLimit == configs.Prospective.Search.CandidateLimit {
		t.Fatal("prior transition config was overlaid with prospective values")
	}

	delete(priorValues, "backup_assets.content_ticket_timeout")
	if _, err := foundationTransitionConfigsFromValues(priorValues, prospectiveValues); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("incomplete prior config error=%v, want ErrInvalidState", err)
	}
}
