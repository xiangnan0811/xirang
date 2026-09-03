package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/settings"
)

func TestRuntimeRegistersRcloneDeletionMuxForNativeAccess(t *testing.T) {
	db := openRuntimeTestDB(t)
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB: db, Settings: settings.NewService(db), Transport: transport, StreamTransport: transport,
		StagedPayload: &runtimeStagedPayloadFake{},
		Metrics:       publication.NoopMetrics{}, ContentMetrics: content.NoopMetrics{}, SessionRevocations: &runtimeSessionRevocationsFake{},
		Now: func() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("construct runtime: %v", err)
	}
	deleter, err := runtime.RepositoryService().PointDeleter(backupasset.ProviderRclone)
	if err != nil {
		t.Fatalf("rclone PointDeleter: %v", err)
	}
	versions := []provider.RcloneNativeExactVersion{{
		PhysicalKey: "managed/v1/control/commit.json", VersionID: "v-owned-1",
	}}
	native := &runtimeRcloneNativeDeletionFake{present: map[string]bool{
		"managed/v1/control/commit.json\x00v-owned-1": true,
	}}
	repositoryID := strings.Repeat("1", 32)
	request := provider.DeletePointRequest{
		Snapshot: provider.ReadSnapshot{
			RepositoryID: repositoryID, CapabilityRevision: 1, SourceRevision: strings.Repeat("b", 64),
			Access: provider.AccessBinding{
				Provider: backupasset.ProviderRclone, RepositoryID: repositoryID,
				AdapterData: provider.RcloneNativeDeletionAccess{
					Versions: versions, AuthorityDigest: strings.Repeat("f", 64), Client: native,
				},
			},
		},
		Point:                  provider.PointLocator{Native: strings.Repeat("a", 32)},
		ExpectedSourceRevision: strings.Repeat("b", 64),
		OperationID:            strings.Repeat("e", 32),
	}
	result, err := deleter.DeletePoint(context.Background(), request)
	if err != nil {
		t.Fatalf("runtime rclone native DeletePoint: %v", err)
	}
	if result.Outcome != provider.DeletePointDeleted {
		t.Fatalf("runtime rclone native outcome=%s, want deleted", result.Outcome)
	}
	if native.deleteCalls != 1 {
		t.Fatalf("native exact-version deletes=%d, want 1 (mux must not stay prefix-only)", native.deleteCalls)
	}
}

type runtimeRcloneNativeDeletionFake struct {
	present     map[string]bool
	deleteCalls int
}

func (fake *runtimeRcloneNativeDeletionFake) ProbeExactVersion(_ context.Context, version provider.RcloneNativeExactVersion) (provider.RcloneNativeVersionProbe, error) {
	return provider.RcloneNativeVersionProbe{Present: fake.present[version.PhysicalKey+"\x00"+version.VersionID]}, nil
}

func (fake *runtimeRcloneNativeDeletionFake) DeleteExactVersion(_ context.Context, version provider.RcloneNativeExactVersion) error {
	if version.VersionID == "" {
		return errors.New("unversioned delete")
	}
	fake.deleteCalls++
	delete(fake.present, version.PhysicalKey+"\x00"+version.VersionID)
	return nil
}
