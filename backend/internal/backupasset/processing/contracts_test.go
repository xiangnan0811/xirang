package processing

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
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

func TestProductionWorkDescriptorBindsClosedProfileAndCeilings(t *testing.T) {
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityImageThumbnail,
		capabilityspec.ProfileRasterThumbnailV1,
		false,
	)
	if !ok {
		t.Fatal("thumbnail profile missing")
	}
	descriptor := validWorkDescriptor()
	descriptor.Capability = profile.Capability
	descriptor.CapabilitySchema = profile.CapabilitySchema
	descriptor.OutputProfile = profile.OutputProfile
	descriptor.Parameters = canonicalPreviewParameters(profile)
	if err := ValidateProductionWorkDescriptorV1(descriptor, false); err != nil {
		t.Fatalf("valid production descriptor: %v", err)
	}

	mutations := []func(*WorkDescriptorV1){
		func(value *WorkDescriptorV1) { value.CapabilitySchema = "future.schema" },
		func(value *WorkDescriptorV1) { value.OutputProfile = "caller-selected" },
		func(value *WorkDescriptorV1) { value.Parameters.MaxOutputBytes = profile.Limits.MaxOutputBytes + 1 },
		func(value *WorkDescriptorV1) { value.Parameters.MaxPages = profile.Limits.MaxPages + 1 },
		func(value *WorkDescriptorV1) {
			value.Parameters.RequiresMaterialization = !profile.RequiresMaterialization
		},
		func(value *WorkDescriptorV1) { value.Parameters.Codec = "mp4" },
	}
	for index, mutate := range mutations {
		candidate := descriptor
		mutate(&candidate)
		if err := ValidateProductionWorkDescriptorV1(candidate, false); !errors.Is(err, ErrInvalidContract) {
			t.Fatalf("unsafe production descriptor %d error=%v", index, err)
		}
	}
}

func TestMediaPreviewDescriptorUsesClosedMP4Codec(t *testing.T) {
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilityMediaTranscode,
		capabilityspec.ProfileBrowserPreviewV1,
		false,
	)
	if !ok {
		t.Fatal("media profile missing")
	}
	parameters := canonicalPreviewParameters(profile)
	if parameters.Codec != "mp4" {
		t.Fatalf("media preview codec=%q, want mp4", parameters.Codec)
	}
}

func TestSecretClassificationDescriptorUsesClosedTextCodec(t *testing.T) {
	profile, ok := capabilityspec.Lookup(
		capabilityspec.CapabilitySecretClassify,
		capabilityspec.ProfileBoundedSecretV1,
		true,
	)
	if !ok {
		t.Fatal("secret classification profile missing")
	}
	parameters := canonicalPreviewParameters(profile)
	if parameters.Codec != "text" {
		t.Fatalf("secret classification codec=%q, want text", parameters.Codec)
	}
}
