package task

import (
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestParseRsyncPublicationConfigV1DefaultsToLegacyMutable(t *testing.T) {
	for _, raw := range []string{"", `{}`, `{"version":1}`} {
		config, err := ParseRsyncPublicationConfigV1(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if config.Version != RsyncPublicationConfigV1Version || config.PublicationMode != backupasset.PublicationLegacyMutable {
			t.Fatalf("parse %q = %+v, want legacy v%d", raw, config, RsyncPublicationConfigV1Version)
		}
	}
}

func TestParseRsyncPublicationConfigV1AcceptsApprovedVersionedModes(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedHardlink,
		backupasset.PublicationVersionedFullCopy,
	} {
		config, err := ParseRsyncPublicationConfigV1(`{"version":1,"publication_mode":"` + string(mode) + `"}`)
		if err != nil {
			t.Fatalf("parse %q: %v", mode, err)
		}
		if config.Version != RsyncPublicationConfigV1Version || config.PublicationMode != mode {
			t.Fatalf("parse %q = %+v", mode, config)
		}
	}
}

func TestParseRsyncPublicationConfigV1RejectsUnknownOrUnsafeFields(t *testing.T) {
	for _, raw := range []string{
		`{"version":2,"publication_mode":"legacy_mutable"}`,
		`{"version":1,"publication_mode":"versioned_prefix"}`,
		`{"version":1,"publication_mode":"versioned_hardlink","rsync_args":["--inplace"]}`,
		`{"version":1,"version":1,"publication_mode":"legacy_mutable"}`,
		`{"version":null,"publication_mode":"legacy_mutable"}`,
		`{"version":1,"publication_mode":null}`,
		`{"version":1,"publication_mode":"versioned_full_copy"} trailing`,
		`["--delete"]`,
	} {
		if _, err := ParseRsyncPublicationConfigV1(raw); err == nil {
			t.Fatalf("parse %q unexpectedly succeeded", raw)
		}
	}
}

func TestValidateTaskInputRejectsUnsafeRsyncPublicationConfig(t *testing.T) {
	input := CreateTaskInput{
		Name: "versioned-rsync", NodeID: 1, ExecutorType: "rsync",
		RsyncSource: "/data/source", RsyncTarget: "/backup/legacy",
		ExecutorConfig: `{"version":1,"publication_mode":"versioned_hardlink","rsync_args":["--inplace"]}`,
	}
	if err := ValidateTaskInput(input); err == nil {
		t.Fatal("unsafe Rsync publication config unexpectedly passed validation")
	}

	input.ExecutorConfig = `{"version":1,"publication_mode":"versioned_full_copy"}`
	if err := ValidateTaskInput(input); err == nil {
		t.Fatal("normal Task validation accepted direct versioned Rsync activation")
	}

	input.ExecutorConfig = `{"version":1,"publication_mode":"legacy_mutable"}`
	if err := ValidateTaskInput(input); err != nil {
		t.Fatalf("legacy Rsync publication mode rejected: %v", err)
	}
}
