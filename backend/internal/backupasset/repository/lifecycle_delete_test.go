package repository

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestResolveLifecycleDeletePointReconstructsRsyncDeletionAccess(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	state, ok := execution.(*rsyncPublicationExecution)
	if !ok {
		t.Fatalf("managed Rsync execution type=%T", execution)
	}
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(state.markerKey, fixture.binding, state.attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(state.markerKey, state.childFence),
		PointDeadlineAt: state.attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	service := newLifecycleDeleteService(t, fixture.service, fixture.now)
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("ResolveLifecycleDeletePoint rsync: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.RsyncPointDeletionAccess)
	if !ok {
		t.Fatalf("rsync AdapterData=%T, want RsyncPointDeletionAccess", request.Snapshot.Access.AdapterData)
	}
	locator, err := decodeManagedRsyncPointLocator(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := provider.DecodeRsyncTreeAttemptV1(locator.TaggedAttempt)
	if err != nil {
		t.Fatal(err)
	}
	markerKey, err := fixture.service.rsyncMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	if access.ManagedRoot != filepath.Clean(fixture.binding.ManagedRootLocator) ||
		string(access.MarkerKey) != string(markerKey) ||
		access.Attempt != attempt ||
		access.CommitMarkerDigest != locator.CommitMarkerDigest ||
		access.SourceFingerprint != point.SourceFingerprint ||
		request.Point.Native != attempt.FinalComponent {
		t.Fatalf("rsync deletion snapshot=%+v native=%q locator=%+v", access, request.Point.Native, locator)
	}
	assertLifecycleDeleteRequestOmitsSecrets(t, request, markerKey)
}

func TestResolveLifecycleDeletePointUsesRollbackLocatorForRetiredMutableHead(t *testing.T) {
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	state, ok := execution.(*rsyncPublicationExecution)
	if !ok {
		t.Fatalf("managed Rsync execution type=%T", execution)
	}
	commit := provider.RsyncTreeCommitV1{
		LayoutVersion: 1, RepositoryID: state.attempt.RepositoryID, TaskRepositoryLinkID: state.attempt.TaskRepositoryLinkID,
		RecoveryPointID: state.attempt.RecoveryPointID, AttemptID: state.attempt.AttemptID, PublicationMode: state.attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: managedRsyncSourceFingerprint(state.markerKey, fixture.binding, state.attempt.RecoveryPointID),
		ProviderCommittedAt: fixture.now, CommitMarkerDigest: strings.Repeat("3", 64), ChildFenceDigest: rsyncChildFenceDigest(state.markerKey, state.childFence),
		PointDeadlineAt: state.attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRsyncTreeProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&point).Updates(map[string]any{
		"encrypted_rollback_locator": point.EncryptedProviderLocator,
		"encrypted_provider_locator": "",
		"semantics":                  string(backupasset.PointMutableHead),
		"state":                      string(backupasset.RecoveryPointRetired),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&point, "id = ?", state.attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	service := newLifecycleDeleteService(t, fixture.service, fixture.now)
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("retired rollback ResolveLifecycleDeletePoint: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.RsyncPointDeletionAccess)
	if !ok {
		t.Fatalf("retired rollback AdapterData=%T, want RsyncPointDeletionAccess", request.Snapshot.Access.AdapterData)
	}
	locator, err := decodeManagedRsyncPointLocator(point.EncryptedRollbackLocator)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := provider.DecodeRsyncTreeAttemptV1(locator.TaggedAttempt)
	if err != nil {
		t.Fatal(err)
	}
	if request.Point.Native != attempt.FinalComponent || access.Attempt != attempt {
		t.Fatalf("retired rollback used wrong locator native=%q attempt=%+v", request.Point.Native, access.Attempt)
	}
}

func TestResolveLifecycleDeletePointReconstructsRclonePrefixDeletionAccess(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationVersionedPrefix)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	commit := validRcloneRepositoryCommit(attempt, input.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	service := newLifecycleDeleteService(t, fixture.service, fixture.now)
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("ResolveLifecycleDeletePoint rclone prefix: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
	if !ok {
		t.Fatalf("rclone prefix AdapterData=%T, want RclonePrefixDeletionAccess", request.Snapshot.Access.AdapterData)
	}
	locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix, err := provider.NewRclonePrivateLocator(locator.PortableAttemptRoot)
	if err != nil {
		t.Fatal(err)
	}
	if access.Prefix != wantPrefix || access.Command == nil || access.Command.Node.ID != fixture.task.NodeID ||
		access.MarkerDigest != point.SourceFingerprint || request.Point.Native != locator.PortableAttemptRoot ||
		access.ExpectedRootIdentity != attempt.ManagedRootIdentityDigest ||
		access.ConfigDigest != fixture.binding.Portable.ConfigDigest {
		t.Fatalf("rclone prefix snapshot=%+v native=%q locator=%+v attempt_identity=%q", access, request.Point.Native, locator, attempt.ManagedRootIdentityDigest)
	}
	assertLifecycleDeleteRequestOmitsSecrets(t, request, []byte(fixture.binding.Portable.BoundConfig))
}

func TestEncodeManagedRclonePointLocatorPersistsFrozenNativeVersionsFromNativeCommit(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	commit.Native.FrozenNativeVersions = []provider.RcloneNativeExactVersion{
		{PhysicalKey: "managed/v1/data/file.bin", VersionID: "v-owned-1"},
		{PhysicalKey: commit.Native.CommitKey, VersionID: commit.Native.CommitVersionID},
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	if len(locator.FrozenNativeVersions) < 2 {
		t.Fatalf("encoded native locator frozen versions=%d, want >= 2 including commit", len(locator.FrozenNativeVersions))
	}
	foundCommit, foundData := false, false
	for _, version := range locator.FrozenNativeVersions {
		if version.PhysicalKey == locator.NativeCommitKey && version.VersionID == locator.NativeCommitVersionID {
			foundCommit = true
		}
		if version.PhysicalKey == "managed/v1/data/file.bin" && version.VersionID == "v-owned-1" {
			foundData = true
		}
	}
	if !foundCommit || !foundData {
		t.Fatalf("encoded frozen versions=%+v missing commit or data", locator.FrozenNativeVersions)
	}
	service := newLifecycleDeleteService(t, fixture.service, fixture.now)
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("encoded frozen ResolveLifecycleDeletePoint: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.RcloneNativeDeletionAccess)
	if !ok || len(access.Versions) < 2 {
		t.Fatalf("encoded frozen AdapterData=%T versions=%d, want RcloneNativeDeletionAccess with >= 2", request.Snapshot.Access.AdapterData, len(access.Versions))
	}
}

func TestResolveLifecycleDeletePointReconstructsRcloneNativeDeletionAccess(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	service := newLifecycleDeleteService(t, fixture.service, fixture.now)
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	reason, _, ok := CapabilityFromError(err)
	if err == nil || !ok || reason.Code != backupasset.CapabilityDeletionUnavailable {
		t.Fatalf("commit-only native reconstruction error=%v request=%+v, want deletion_unavailable", err, request)
	}
}

func TestResolveLifecycleDeletePointEmptyFrozenNativeVersionsIsDeletionUnavailable(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	if locator.NativeCommitKey == "" || locator.NativeCommitVersionID == "" {
		t.Fatalf("commit-only locator missing native identity key=%q version=%q", locator.NativeCommitKey, locator.NativeCommitVersionID)
	}
	locator.FrozenNativeVersions = nil
	payload, err := json.Marshal(locator)
	if err != nil {
		t.Fatal(err)
	}
	point.EncryptedProviderLocator = string(payload)
	if err := fixture.db.Save(&point).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	headCallsBefore := fixture.nativeFactory.headVersionCalls
	service := newLifecycleDeleteService(t, fixture.service, fixture.now)
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	reason, _, ok := CapabilityFromError(err)
	if err == nil || !ok || reason.Code != backupasset.CapabilityDeletionUnavailable {
		t.Fatalf("empty frozen versions error=%v request=%+v, want deletion_unavailable", err, request)
	}
	if _, hasLiveSet := request.Snapshot.Access.AdapterData.(provider.RcloneNativeDeletionAccess); hasLiveSet {
		t.Fatalf("empty frozen versions reconstructed live deletion set: %+v", request.Snapshot.Access.AdapterData)
	}
	if fixture.nativeFactory.headVersionCalls != headCallsBefore {
		t.Fatalf("empty frozen versions used live HeadVersion calls=%d, want %d", fixture.nativeFactory.headVersionCalls, headCallsBefore)
	}
}

func TestResolveLifecycleDeletePointReconstructsFrozenRcloneNativeViewSet(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	locator.FrozenNativeVersions = []managedRcloneFrozenNativeVersion{
		{PhysicalKey: "managed/v1/data/file.bin", VersionID: "v-owned-1"},
		{PhysicalKey: locator.NativeCommitKey, VersionID: locator.NativeCommitVersionID},
	}
	payload, err := json.Marshal(locator)
	if err != nil {
		t.Fatal(err)
	}
	point.EncryptedProviderLocator = string(payload)
	if err := fixture.db.Save(&point).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	service := newLifecycleDeleteService(t, fixture.service, fixture.now)
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("frozen native ResolveLifecycleDeletePoint: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.RcloneNativeDeletionAccess)
	if !ok {
		t.Fatalf("frozen native AdapterData=%T, want RcloneNativeDeletionAccess", request.Snapshot.Access.AdapterData)
	}
	if access.Client == nil || len(access.Versions) != 2 {
		t.Fatalf("frozen native versions=%d access=%+v, want 2", len(access.Versions), access)
	}
	foundCommit, foundData := false, false
	for _, version := range access.Versions {
		if version.PhysicalKey == locator.NativeCommitKey && version.VersionID == locator.NativeCommitVersionID {
			foundCommit = true
		}
		if version.PhysicalKey == "managed/v1/data/file.bin" && version.VersionID == "v-owned-1" {
			foundData = true
		}
	}
	if !foundCommit || !foundData {
		t.Fatalf("frozen native versions=%+v missing commit or data", access.Versions)
	}
}

func TestResolveLifecycleDeletePointNativeWithoutLineageFailsClosed(t *testing.T) {
	fixture := newRclonePublicationFixture(t, backupasset.PublicationNativeObjectVersions)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := execution.Attempt().RcloneAttempt()
	if err != nil {
		t.Fatal(err)
	}
	input, err := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	}).RclonePublicationInput()
	if err != nil {
		t.Fatal(err)
	}
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&point).Update("producing_task_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	service := newLifecycleDeleteService(t, fixture.service, fixture.now)
	_, err = service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	reason, _, ok := CapabilityFromError(err)
	if !ok || reason.Code != backupasset.CapabilityDeletionUnavailable {
		t.Fatalf("native without lineage error=%v, want deletion_unavailable", err)
	}
}

func newLifecycleDeleteService(t *testing.T, publication *PublicationService, now time.Time) *Service {
	t.Helper()
	service, err := NewService(Dependencies{
		DB: publication.db, Foundation: publication.foundation, Registry: publication.registry,
		Keyring: publication.keyring, Now: func() time.Time { return now }, Admission: publication.admission,
		Publication: publication,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertLifecycleDeleteRequestOmitsSecrets(t *testing.T, request provider.DeletePointRequest, secrets ...[]byte) {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	body := string(payload)
	for _, secret := range secrets {
		if len(secret) > 0 && strings.Contains(body, string(secret)) {
			t.Fatalf("lifecycle delete request JSON leaked secret %q: %s", secret, body)
		}
	}
	if strings.Contains(body, `"adapter_data"`) || strings.Contains(body, `"secret"`) || strings.Contains(body, `"marker_key"`) {
		t.Fatalf("lifecycle delete request JSON exposed private binding fields: %s", body)
	}
}
