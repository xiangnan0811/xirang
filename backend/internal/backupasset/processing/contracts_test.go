package processing

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUpdaterMetadataAcceptsOnlyClosedLifecycleFacts(t *testing.T) {
	verified := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	activated := verified.Add(time.Minute)
	base := UpdaterMetadataV1{
		SchemaVersion: 1, SourceKind: UpdaterSourceBuiltin, SourceID: "builtin.noop",
		Version: "1.2.3-rc.1+build.7", ManifestDigest: strings.Repeat("a", 64),
		SigningKeyFingerprint: strings.Repeat("b", 64), BundleFingerprint: strings.Repeat("c", 64),
	}
	valid := []UpdaterMetadataV1{
		base,
		func() UpdaterMetadataV1 {
			value := base
			value.State = UpdaterMetadataVerified
			value.VerifiedAt = &verified
			return value
		}(),
		func() UpdaterMetadataV1 {
			value := base
			value.State, value.VerifiedAt, value.ActivatedAt = UpdaterMetadataActive, &verified, &activated
			return value
		}(),
		func() UpdaterMetadataV1 {
			value := base
			value.State, value.VerifiedAt = UpdaterMetadataSuperseded, &verified
			return value
		}(),
		func() UpdaterMetadataV1 {
			value := base
			value.State, value.FailureCode = UpdaterMetadataFailed, UpdaterFailureInvalidSignature
			return value
		}(),
	}
	valid[0].State = UpdaterMetadataRegistered
	for _, value := range valid {
		if err := ValidateUpdaterMetadataV1(value); err != nil {
			t.Fatalf("valid updater metadata state=%q: %v", value.State, err)
		}
	}
}

func TestUpdaterMetadataRejectsSecretsRawOutputAndOpenEndedFacts(t *testing.T) {
	verified := time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC)
	base := UpdaterMetadataV1{
		SchemaVersion: 1, SourceKind: UpdaterSourceAdminRegistered, SourceID: "admin.fixture-1",
		Version: "1.2.3", ManifestDigest: strings.Repeat("a", 64),
		SigningKeyFingerprint: strings.Repeat("b", 64), BundleFingerprint: strings.Repeat("c", 64),
		State: UpdaterMetadataVerified, VerifiedAt: &verified,
	}
	invalid := map[string]UpdaterMetadataV1{
		"unknown source": func() UpdaterMetadataV1 { value := base; value.SourceKind = "https"; return value }(),
		"URL source": func() UpdaterMetadataV1 {
			value := base
			value.SourceID = "https://example.invalid/bundle"
			return value
		}(),
		"non semantic version": func() UpdaterMetadataV1 { value := base; value.Version = "latest"; return value }(),
		"uppercase digest":     func() UpdaterMetadataV1 { value := base; value.ManifestDigest = strings.Repeat("A", 64); return value }(),
		"activation before verify": func() UpdaterMetadataV1 {
			value := base
			before := verified.Add(-time.Second)
			value.State, value.ActivatedAt = UpdaterMetadataActive, &before
			return value
		}(),
		"raw failure": func() UpdaterMetadataV1 {
			value := base
			value.State, value.FailureCode = UpdaterMetadataFailed, "curl: token=secret"
			return value
		}(),
	}
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := ValidateUpdaterMetadataV1(value); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("ValidateUpdaterMetadataV1 error=%v, want invalid contract", err)
			}
		})
	}

	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string][]byte{
		"unknown URL":    append(encoded[:len(encoded)-1], []byte(`,"url":"https://example.invalid"}`)...),
		"credential":     append(encoded[:len(encoded)-1], []byte(`,"credential":"secret"}`)...),
		"raw output":     append(encoded[:len(encoded)-1], []byte(`,"raw_output":"tool secret"}`)...),
		"duplicate":      []byte(strings.Replace(string(encoded), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)),
		"trailing value": append(append([]byte(nil), encoded...), []byte(` {}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeUpdaterMetadataV1(payload); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("DecodeUpdaterMetadataV1 error=%v, want invalid contract", err)
			}
		})
	}
}
