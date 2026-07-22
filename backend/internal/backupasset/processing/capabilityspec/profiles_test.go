package capabilityspec

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProductionProfilesAreClosedAndHaveFrozenCeilings(t *testing.T) {
	profiles := RequiredProfiles()
	want := []Identity{
		{CapabilityImageThumbnail, ProfileRasterThumbnailV1},
		{CapabilityTextExtract, ProfileBoundedTextV1},
		{CapabilityImageOCR, ProfileTesseractTextV1},
		{CapabilityDocumentConvert, ProfileStaticPagesV1},
		{CapabilityMalwareScan, ProfileSignatureScanV1},
		{CapabilityMediaProbe, ProfileMediaProbeV1},
		{CapabilityMediaTranscode, ProfileBrowserPreviewV1},
		{CapabilityArchiveInspect, ProfileArchiveIndexV1},
		{CapabilityArchiveExtractEntry, ProfileArchiveMemberV1},
	}
	got := make([]Identity, 0, len(profiles))
	for _, profile := range profiles {
		if err := profile.Validate(); err != nil {
			t.Fatalf("profile %s/%s invalid: %v", profile.Capability, profile.OutputProfile, err)
		}
		got = append(got, profile.Identity())
		if profile.Capability == CapabilitySecretClassify {
			t.Fatal("optional secret profile advertised by default")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("production identities=%v, want %v", got, want)
	}

	assertLimit := func(capability, profile string, check func(Limits) bool) {
		t.Helper()
		value, ok := Lookup(capability, profile, false)
		if !ok || !check(value.Limits) {
			t.Fatalf("unexpected limits for %s/%s: %+v", capability, profile, value.Limits)
		}
	}
	assertLimit(CapabilityImageThumbnail, ProfileRasterThumbnailV1, func(v Limits) bool {
		return v.MaxInputBytes == 256<<20 && v.MaxPixels == 50_000_000 && v.MaxFrames == 8 && v.MaxOutputBytes == 8<<20 && v.WallTime == 90*time.Second
	})
	assertLimit(CapabilityDocumentConvert, ProfileStaticPagesV1, func(v Limits) bool {
		return v.MaxPages == 100 && v.MaxRenderedPages == 30 && v.MaxOutputCount == 32 && v.MaxOutputBytes == 64<<20
	})
	assertLimit(CapabilityArchiveInspect, ProfileArchiveIndexV1, func(v Limits) bool {
		return v.MaxArchiveEntries == 100_000 && v.MaxArchiveDepth == 16 && v.MaxExpandedBytes == 8<<30 && v.MaxCompressionRatio == 100
	})
	if _, ok := Lookup(CapabilitySecretClassify, ProfileBoundedSecretV1, false); ok {
		t.Fatal("secret profile available without explicit opt-in")
	}
	if profile, ok := Lookup(CapabilitySecretClassify, ProfileBoundedSecretV1, true); !ok || profile.EnabledByDefault {
		t.Fatalf("optional secret profile=%+v ok=%v", profile, ok)
	}
}

func TestProfilesRejectOpenEndedOrActiveConfiguration(t *testing.T) {
	base, ok := Lookup(CapabilityImageThumbnail, ProfileRasterThumbnailV1, false)
	if !ok {
		t.Fatal("image profile missing")
	}
	mutations := []func(*Profile){
		func(value *Profile) { value.ExecutableID = "/tmp/caller-selected" },
		func(value *Profile) { value.InputMIMEs = append(value.InputMIMEs, "image/svg+xml") },
		func(value *Profile) { value.InputMIMEs = append(value.InputMIMEs, value.InputMIMEs[0]) },
		func(value *Profile) { value.OutputProfile = "../../profile" },
		func(value *Profile) { value.Limits.MaxOutputBytes = value.Limits.MaxInputBytes + 1 },
		func(value *Profile) { value.Limits.MaxOutputCount = 257 },
	}
	for index, mutate := range mutations {
		value := base
		value.InputMIMEs = append([]string(nil), base.InputMIMEs...)
		mutate(&value)
		if err := value.Validate(); !errors.Is(err, ErrInvalidContract) {
			t.Fatalf("mutation %d error=%v", index, err)
		}
	}
}

func TestCanonicalProfileAndPipelineFingerprintAreDeterministic(t *testing.T) {
	profile, ok := Lookup(CapabilityImageOCR, ProfileTesseractTextV1, false)
	if !ok {
		t.Fatal("OCR profile missing")
	}
	first, err := profile.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	second, err := profile.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !json.Valid(first) {
		t.Fatalf("canonical profile unstable: %q / %q", first, second)
	}
	base, err := profile.PipelineFingerprint("toolchain-v1", []string{strings.Repeat("a", 64)}, "policy-v1")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := profile.PipelineFingerprint("toolchain-v1", []string{strings.Repeat("b", 64)}, "policy-v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 64 || base == changed {
		t.Fatalf("fingerprints base=%q changed=%q", base, changed)
	}
}
