package processing

import (
	"errors"
	"testing"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

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
