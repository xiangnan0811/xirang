package processing

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"xirang/backend/internal/backupasset"
)

var processingTestDBSequence atomic.Uint64

func processingTestSQLiteDSN(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(
		"file:%s-%d?mode=memory&cache=shared&_busy_timeout=5000&_txlock=immediate&_loc=UTC",
		strings.ReplaceAll(t.Name(), "/", "_"),
		processingTestDBSequence.Add(1),
	)
}

func validWorkDescriptor() WorkDescriptorV1 {
	return WorkDescriptorV1{
		SchemaVersion:              1,
		Source:                     backupasset.AssetRef{RecoveryPointID: strings.Repeat("a", 32), EntryID: strings.Repeat("b", 64)},
		CatalogGenerationID:        strings.Repeat("c", 32),
		SourceFingerprint:          "source-fingerprint-v1",
		EntryFingerprint:           "entry-fingerprint-v1",
		ProviderCapabilityRevision: 7,
		Capability:                 "noop",
		CapabilitySchema:           "noop.v1",
		PipelineFingerprint:        "pipeline-fingerprint-v1",
		OutputProfile:              "noop.v1",
		SecurityPolicyRevision:     "security-policy-v1",
		Parameters: CanonicalParametersV1{
			SchemaVersion:           1,
			Width:                   1280,
			Height:                  720,
			Codec:                   "png",
			PageStart:               1,
			PageEnd:                 3,
			Quality:                 80,
			Language:                "zh-CN",
			Model:                   "noop-model-v1",
			FontProfile:             "bundled-v1",
			MemberStart:             0,
			MemberEnd:               2,
			FrameStart:              0,
			FrameEnd:                10,
			TimeStartMillis:         0,
			TimeEndMillis:           1000,
			Orientation:             "auto",
			CropX:                   0,
			CropY:                   0,
			CropWidth:               1280,
			CropHeight:              720,
			MaxPages:                100,
			MaxPixels:               100_000_000,
			MaxDurationMillis:       60_000,
			MaxExpandedBytes:        1 << 30,
			MaxOutputBytes:          1 << 28,
			MaxOutputCount:          32,
			TruncationPolicy:        "reject",
			RequiresMaterialization: false,
		},
	}
}
