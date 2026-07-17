package task

import (
	"strconv"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
)

func TestParseRcloneTaskConfigV1PreservesLegacyCompatibility(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		bandwidth string
		transfers int
	}{
		{name: "empty"},
		{name: "empty object", raw: `{}`},
		{name: "explicit legacy", raw: `{"version":1,"publication_mode":"legacy_mutable"}`},
		{name: "old unversioned", raw: `{"bandwidth_limit":"10M","transfers":4}`, bandwidth: "10M", transfers: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := ParseRcloneTaskConfigV1(test.raw)
			if err != nil {
				t.Fatalf("parse %q: %v", test.raw, err)
			}
			if config.Version != RcloneTaskConfigV1Version || config.PublicationMode != backupasset.PublicationLegacyMutable ||
				config.BandwidthLimit != test.bandwidth || config.Transfers != test.transfers {
				t.Fatalf("parse %q = %+v", test.raw, config)
			}
		})
	}
}

func TestRcloneTaskConfigV1ExactManagedRoundTrip(t *testing.T) {
	want := RcloneTaskConfigV1{
		Version: RcloneTaskConfigV1Version, PublicationMode: backupasset.PublicationVersionedPrefix,
		BandwidthLimit: "10M", Transfers: 4,
	}
	encoded, err := EncodeRcloneTaskConfigV1(want)
	if err != nil {
		t.Fatalf("encode portable config: %v", err)
	}
	if encoded != `{"version":1,"publication_mode":"versioned_prefix","bandwidth_limit":"10M","transfers":4}` {
		t.Fatalf("portable encoding=%s", encoded)
	}
	got, err := ParseRcloneTaskConfigV1(encoded)
	if err != nil || got != want {
		t.Fatalf("round trip got=%+v err=%v want=%+v", got, err, want)
	}

	want.PublicationMode = backupasset.PublicationNativeObjectVersions
	encoded, err = EncodeRcloneTaskConfigV1(want)
	if err != nil {
		t.Fatalf("encode native config: %v", err)
	}
	got, err = ParseRcloneTaskConfigV1(encoded)
	if err != nil || got != want {
		t.Fatalf("native round trip got=%+v err=%v want=%+v", got, err, want)
	}
}

func TestParseRcloneTaskConfigV1RejectsAmbiguousOrUnboundedInput(t *testing.T) {
	for _, raw := range []string{
		`{"version":2,"publication_mode":"legacy_mutable"}`,
		`{"version":1,"publication_mode":"native_snapshot"}`,
		`{"version":1,"publication_mode":"legacy_mutable","remote":"secret:root"}`,
		`{"version":1,"version":1,"publication_mode":"legacy_mutable"}`,
		`{"version":null,"publication_mode":"legacy_mutable"}`,
		`{"version":1,"publication_mode":null}`,
		`{"version":1,"publication_mode":"legacy_mutable"} trailing`,
		`[]`,
		`{"bandwidth_limit":"` + strings.Repeat("a", MaxRcloneBandwidthLimitBytes+1) + `"}`,
		`{"bandwidth_limit":"10M\u0000future"}`,
		`{"transfers":-1}`,
		`{"transfers":` + strings.Repeat("9", 64) + `}`,
		`{"transfers":` + strconv.Itoa(MaxRcloneTransfers+1) + `}`,
	} {
		if _, err := ParseRcloneTaskConfigV1(raw); err == nil {
			t.Fatalf("parse %q unexpectedly succeeded", raw)
		}
	}
	if _, err := ParseRcloneTaskConfigV1(`{"transfers":0}`); err != nil {
		t.Fatalf("omitted-value transfers rejected: %v", err)
	}
}

func TestValidateTaskInputRejectsDirectManagedRcloneActivation(t *testing.T) {
	input := CreateTaskInput{
		Name: "versioned-rclone", NodeID: 1, ExecutorType: "rclone",
		RsyncSource: "/data/source", RsyncTarget: "s3:bucket/legacy",
	}
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix,
		backupasset.PublicationNativeObjectVersions,
	} {
		input.ExecutorConfig = `{"version":1,"publication_mode":"` + string(mode) + `"}`
		if err := ValidateTaskInput(input); err == nil {
			t.Fatalf("generic task validation accepted managed Rclone mode %q", mode)
		}
	}
	input.ExecutorConfig = `{"version":1,"publication_mode":"legacy_mutable","bandwidth_limit":"10M","transfers":4}`
	if err := ValidateTaskInput(input); err != nil {
		t.Fatalf("legacy Rclone policy rejected: %v", err)
	}
}

func TestValidateDisconnectedImportedRcloneTask(t *testing.T) {
	valid := CreateTaskInput{
		Name:           "foreign-managed-rclone",
		NodeID:         1,
		ExecutorType:   "rclone",
		RsyncSource:    "/srv/trusted-source",
		ExecutorConfig: `{"version":1,"publication_mode":"legacy_mutable"}`,
		CronSpec:       "*/5 * * * *",
	}
	if err := ValidateDisconnectedImportedRcloneTask(valid); err != nil {
		t.Fatalf("valid disconnected Rclone import rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*CreateTaskInput)
	}{
		{name: "wrong executor", mutate: func(input *CreateTaskInput) { input.ExecutorType = "rsync" }},
		{name: "missing source", mutate: func(input *CreateTaskInput) { input.RsyncSource = "" }},
		{name: "remote source", mutate: func(input *CreateTaskInput) { input.RsyncSource = "foreign:source" }},
		{name: "unsafe source", mutate: func(input *CreateTaskInput) { input.RsyncSource = "/srv/source\nnext" }},
		{name: "foreign target", mutate: func(input *CreateTaskInput) { input.RsyncTarget = "foreign:bucket/private" }},
		{name: "managed mode", mutate: func(input *CreateTaskInput) {
			input.ExecutorConfig = `{"version":1,"publication_mode":"versioned_prefix"}`
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if err := ValidateDisconnectedImportedRcloneTask(input); err == nil {
				t.Fatalf("unsafe disconnected Rclone import unexpectedly passed: %+v", input)
			}
		})
	}
}
