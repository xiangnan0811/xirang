package capabilities

import (
	"errors"
	"testing"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

func TestMediaPlanRejectsMalformedAndNetworkProtocols(t *testing.T) {
	if _, err := PlanMedia(readCapabilityFixture(t, "malformed-media.mp4"), "video/mp4", MediaProbe); !errors.Is(err, ErrInvalidToolOutput) {
		t.Fatalf("malformed media error=%v", err)
	}
	for _, mediaType := range []string{"text/html", "application/vnd.apple.mpegurl", "application/x-rtsp"} {
		if _, err := PlanMedia([]byte("http://FAKE_MEDIA_FOR_TEST_ONLY"), mediaType, MediaPreview); !errors.Is(err, capabilityspec.ErrUnsupportedMedia) {
			t.Fatalf("network media %q error=%v", mediaType, err)
		}
	}
}

func TestMediaPlanUsesClosedLocalProfiles(t *testing.T) {
	input := append([]byte{0, 0, 0, 20}, []byte("ftypisom00000000")...)
	plan, err := PlanMedia(input, "video/mp4", MediaPreview)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ExecutableID != ExecutableFFmpeg || plan.ArgProfile != ArgsMediaPreview || plan.AllowNetwork || plan.MaxDuration != 30*60*1000 {
		t.Fatalf("unsafe media plan: %+v", plan)
	}
}
