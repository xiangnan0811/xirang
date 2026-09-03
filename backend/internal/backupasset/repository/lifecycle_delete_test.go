package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	taskservice "xirang/backend/internal/task"

	"gorm.io/gorm"
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
	wantAttempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil {
		t.Fatal(err)
	}
	wantCommit, err := provider.DecodeRcloneCommitV1(locator.TaggedCommit)
	if err != nil {
		t.Fatal(err)
	}
	markerKey, err := fixture.service.rcloneMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	if access.Prefix != wantPrefix || access.Command == nil || access.Command.Node.ID != fixture.task.NodeID ||
		access.MarkerDigest != point.SourceFingerprint || request.Point.Native != locator.PortableAttemptRoot ||
		string(access.MarkerKey) != string(markerKey) || !reflect.DeepEqual(access.Attempt, wantAttempt) ||
		!reflect.DeepEqual(access.Commit, wantCommit) || access.ExpectedAttemptRoot != locator.PortableAttemptRoot ||
		access.ExpectedRootIdentity != attempt.ManagedRootIdentityDigest ||
		access.ConfigDigest != fixture.binding.Portable.ConfigDigest {
		t.Fatalf("rclone prefix snapshot=%+v native=%q locator=%+v attempt_identity=%q", access, request.Point.Native, locator, attempt.ManagedRootIdentityDigest)
	}
	assertLifecycleDeleteRequestOmitsSecrets(t, request, []byte(fixture.binding.Portable.BoundConfig))
}

func TestManagedRcloneNativeLocatorPersistsBoundedVersionEvidenceReference(t *testing.T) {
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
	dataVersion := provider.RcloneNativeExactVersion{PhysicalKey: "managed/v1/data/file.bin", VersionID: "v-owned-1"}
	commit.Native.FrozenNativeVersions = []provider.RcloneNativeExactVersion{
		dataVersion,
		{PhysicalKey: commit.Native.CommitKey, VersionID: commit.Native.CommitVersionID},
	}
	commit.Native.FrozenNativeReferences = []provider.RcloneNativeExactVersion{dataVersion}
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
	if locator.FrozenNativeVersionCount != 2 || locator.FrozenNativeReferenceCount != 1 ||
		!isLowerHex64(locator.FrozenNativeVersionsDigest) || !isLowerHex64(locator.FrozenNativeReferencesDigest) {
		t.Fatalf("native locator evidence reference=%+v", locator)
	}
	if strings.Contains(point.EncryptedProviderLocator, dataVersion.PhysicalKey) ||
		strings.Contains(point.EncryptedProviderLocator, dataVersion.VersionID) ||
		strings.Contains(point.EncryptedProviderLocator, commit.Native.CommitKey) ||
		strings.Contains(point.EncryptedProviderLocator, commit.Native.CommitVersionID) {
		t.Fatalf("native locator embedded exact version identities: %s", point.EncryptedProviderLocator)
	}
	var rows []model.RecoveryPointRcloneNativeVersion
	if err := fixture.db.Where("recovery_point_id = ?", point.ID).
		Order("evidence_role, ordinal").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("native version evidence rows=%d, want 3", len(rows))
	}
	service := newLifecycleDeleteService(t, fixture.service, fixture.now)
	request, err := service.ResolveLifecycleDeletePoint(lifecycleNativeResolveContext(t, fixture.now), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("bounded evidence ResolveLifecycleDeletePoint: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.RcloneNativeDeletionAccess)
	if !ok || len(access.Versions) != 2 {
		t.Fatalf("bounded evidence AdapterData=%T versions=%d, want 2 owned versions", request.Snapshot.Access.AdapterData, len(access.Versions))
	}
}

func TestResolveLifecycleDeletePointDefersRcloneNativeMaterializationUntilProbe(t *testing.T) {
	fixture, service, point := newCommittedRcloneLifecycleDeleteFixture(t, backupasset.PublicationNativeObjectVersions)
	var factoryCalls int
	originalBuilder := service.publication.rcloneNativeFactoryBuilder
	service.publication.rcloneNativeFactoryBuilder = func(
		ctx context.Context,
		bootstrap provider.RcloneNativeBootstrap,
		region string,
		attempts int,
	) (RcloneNativeFactory, error) {
		factoryCalls++
		return originalBuilder(ctx, bootstrap, region, attempts)
	}
	beforeAssumeCalls := fixture.nativeFactory.assumeCalls
	ctx := lifecycleNativeResolveContext(t, fixture.now)
	var request provider.DeletePointRequest
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		var err error
		request, err = service.ResolveLifecycleDeletePointTx(
			ctx, tx, strings.Repeat("e", 32), point, fixture.repository,
		)
		return err
	}); err != nil {
		t.Fatalf("ResolveLifecycleDeletePointTx: %v", err)
	}
	if factoryCalls != 0 || fixture.nativeFactory.assumeCalls != beforeAssumeCalls {
		t.Fatalf("native materialization ran in resolver transaction: factory=%d assume=%d/%d", factoryCalls, fixture.nativeFactory.assumeCalls, beforeAssumeCalls)
	}
	if err := fixture.db.Model(&model.BackupRepository{}).
		Where("id = ?", fixture.repository.ID).Update("updated_at", fixture.now).Error; err != nil {
		t.Fatalf("SQLite write after lifecycle resolution: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.RcloneNativeDeletionAccess)
	if !ok || access.Client == nil || len(access.Versions) == 0 || !isLowerHex64(access.AuthorityDigest) {
		t.Fatalf("lazy native deletion access=%T %+v", request.Snapshot.Access.AdapterData, request.Snapshot.Access.AdapterData)
	}
	if _, err := access.Client.ProbeExactVersion(ctx, access.Versions[0]); err != nil {
		t.Fatalf("first exact-version probe: %v", err)
	}
	if factoryCalls != 1 || fixture.nativeFactory.assumeCalls == beforeAssumeCalls {
		t.Fatalf("first exact-version probe did not materialize native client: factory=%d assume=%d/%d", factoryCalls, fixture.nativeFactory.assumeCalls, beforeAssumeCalls)
	}
	if _, err := access.Client.ProbeExactVersion(ctx, access.Versions[0]); err != nil {
		t.Fatalf("second exact-version probe: %v", err)
	}
	if factoryCalls != 1 {
		t.Fatalf("native materialization repeated after success: factory=%d", factoryCalls)
	}
}

func TestRcloneNativeLifecycleDeleteAuthorityDigestBindsCredentials(t *testing.T) {
	fixture, _, point := newCommittedRcloneLifecycleDeleteFixture(t, backupasset.PublicationNativeObjectVersions)
	locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	if err != nil {
		t.Fatal(err)
	}
	markerKey, err := fixture.service.rcloneMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := managedRclonePublicationRuntime{
		repository: fixture.repository,
		task:       fixture.task,
		link:       fixture.link,
		binding:    fixture.binding,
	}
	before, err := rcloneNativeLifecycleDeleteAuthorityDigest(markerKey, runtime, attempt, locator)
	if err != nil {
		t.Fatal(err)
	}
	runtime.binding = cloneManagedRcloneBindingDocumentForLifecycle(runtime.binding)
	runtime.binding.Native.ExternalID += "-rotated"
	after, err := rcloneNativeLifecycleDeleteAuthorityDigest(markerKey, runtime, attempt, locator)
	if err != nil {
		t.Fatal(err)
	}
	if !isLowerHex64(before) || !isLowerHex64(after) || before == after {
		t.Fatalf("native deletion authority credential drift digests before=%q after=%q", before, after)
	}
}

func TestRecordRcloneNativeProviderCommitKeepsLargeExactEvidenceOutsideLocator(t *testing.T) {
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
	const versionCount = 1200
	commit.Native.FrozenNativeVersions = make([]provider.RcloneNativeExactVersion, 0, versionCount+1)
	commit.Native.FrozenNativeReferences = make([]provider.RcloneNativeExactVersion, 0, versionCount)
	for ordinal := range versionCount {
		version := provider.RcloneNativeExactVersion{
			PhysicalKey: fmt.Sprintf("managed/v1/data/tree/%06d/file-with-long-name.bin", ordinal),
			VersionID:   fmt.Sprintf("opaque-native-version-%06d", ordinal),
		}
		commit.Native.FrozenNativeVersions = append(commit.Native.FrozenNativeVersions, version)
		commit.Native.FrozenNativeReferences = append(commit.Native.FrozenNativeReferences, version)
	}
	commit.Native.FrozenNativeVersions = append(commit.Native.FrozenNativeVersions, provider.RcloneNativeExactVersion{
		PhysicalKey: commit.Native.CommitKey, VersionID: commit.Native.CommitVersionID,
	})
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if len(point.EncryptedProviderLocator) >= maxManagedRclonePreparedAttemptRecordBytes {
		t.Fatalf("large native locator bytes=%d, limit=%d", len(point.EncryptedProviderLocator), maxManagedRclonePreparedAttemptRecordBytes)
	}
	if strings.Contains(point.EncryptedProviderLocator, "file-with-long-name.bin") ||
		strings.Contains(point.EncryptedProviderLocator, "opaque-native-version-") {
		t.Fatal("large native locator embedded exact version identities")
	}
	var rowCount int64
	if err := fixture.db.Model(&model.RecoveryPointRcloneNativeVersion{}).
		Where("recovery_point_id = ?", point.ID).Count(&rowCount).Error; err != nil {
		t.Fatal(err)
	}
	if rowCount != 2*versionCount+1 {
		t.Fatalf("large native evidence rows=%d, want %d", rowCount, 2*versionCount+1)
	}
	locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	if locator.FrozenNativeVersionCount != versionCount+1 || locator.FrozenNativeReferenceCount != versionCount {
		t.Fatalf("large native locator counts owned=%d references=%d", locator.FrozenNativeVersionCount, locator.FrozenNativeReferenceCount)
	}
}

func TestRecordRcloneNativeProviderCommitRequiresFrozenVersionSet(t *testing.T) {
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
	var before model.RecoveryPoint
	if err := fixture.db.First(&before, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	commit.Native.FrozenNativeVersions = nil
	commit.Native.FrozenNativeReferences = nil
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("empty native frozen version record error=%v, want ErrInvalidState", err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if point.EncryptedProviderLocator != before.EncryptedProviderLocator ||
		point.State != before.State || point.ConsistencyJSON != before.ConsistencyJSON {
		t.Fatalf("invalid native commit mutated point: before=%+v after=%+v", before, point)
	}
}

func TestResolveLifecycleDeletePointMissingNativeOwnedEvidenceFailsClosed(t *testing.T) {
	fixture, service, point := newCommittedRcloneLifecycleDeleteFixture(t, backupasset.PublicationNativeObjectVersions)
	if err := fixture.db.Where("recovery_point_id = ? AND evidence_role = ?",
		point.ID, model.RecoveryPointRcloneNativeEvidenceRoleOwned).
		Delete(&model.RecoveryPointRcloneNativeVersion{}).Error; err != nil {
		t.Fatal(err)
	}
	factoryCalls := 0
	originalBuilder := service.publication.rcloneNativeFactoryBuilder
	service.publication.rcloneNativeFactoryBuilder = func(ctx context.Context, bootstrap provider.RcloneNativeBootstrap, region string, attempts int) (RcloneNativeFactory, error) {
		factoryCalls++
		return originalBuilder(ctx, bootstrap, region, attempts)
	}
	request, err := service.ResolveLifecycleDeletePoint(lifecycleNativeResolveContext(t, fixture.now), strings.Repeat("e", 32), point, fixture.repository)
	if !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("missing native owned evidence error=%v request=%+v, want identity conflict", err, request)
	}
	if factoryCalls != 0 {
		t.Fatalf("missing native owned evidence touched native factory %d times", factoryCalls)
	}
}

func TestResolveLifecycleDeletePointRejectsMutatedNativeEvidenceRow(t *testing.T) {
	fixture, service, point := newCommittedRcloneLifecycleDeleteFixture(t, backupasset.PublicationNativeObjectVersions)
	if err := fixture.db.Model(&model.RecoveryPointRcloneNativeVersion{}).
		Where("recovery_point_id = ? AND evidence_role = ? AND ordinal = ?",
			point.ID, model.RecoveryPointRcloneNativeEvidenceRoleOwned, 0).
		Update("identity_digest", strings.Repeat("f", 64)).Error; err != nil {
		t.Fatal(err)
	}
	factoryCalls := 0
	originalBuilder := service.publication.rcloneNativeFactoryBuilder
	service.publication.rcloneNativeFactoryBuilder = func(ctx context.Context, bootstrap provider.RcloneNativeBootstrap, region string, attempts int) (RcloneNativeFactory, error) {
		factoryCalls++
		return originalBuilder(ctx, bootstrap, region, attempts)
	}
	_, err := service.ResolveLifecycleDeletePoint(lifecycleNativeResolveContext(t, fixture.now), strings.Repeat("e", 32), point, fixture.repository)
	if !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("mutated native evidence row error=%v, want identity conflict", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("mutated native evidence row touched native factory %d times", factoryCalls)
	}
}
func TestResolveLifecycleDeletePointRejectsRcloneTaskRunAuthorityBeforeProviderConstruction(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix, backupasset.PublicationNativeObjectVersions,
	} {
		for _, test := range []struct {
			name   string
			mutate func(*testing.T, *rclonePublicationFixture)
		}{
			{
				name: "TaskRun deleted",
				mutate: func(t *testing.T, fixture *rclonePublicationFixture) {
					if err := fixture.db.Delete(&model.TaskRun{}, fixture.taskRun.ID).Error; err != nil {
						t.Fatalf("delete TaskRun: %v", err)
					}
				},
			},
			{
				name: "TaskRun reassigned",
				mutate: func(t *testing.T, fixture *rclonePublicationFixture) {
					otherTask := seedTask(t, fixture.db, "rclone", "backup:other", `{}`)
					if err := fixture.db.Model(&model.TaskRun{}).Where("id = ?", fixture.taskRun.ID).
						UpdateColumn("task_id", otherTask.ID).Error; err != nil {
						t.Fatalf("reassign TaskRun: %v", err)
					}
				},
			},
			{
				name: "TaskRun node snapshot drift",
				mutate: func(t *testing.T, fixture *rclonePublicationFixture) {
					if err := fixture.db.Model(&model.TaskRun{}).Where("id = ?", fixture.taskRun.ID).
						UpdateColumn("node_id_snapshot", fixture.task.NodeID+1).Error; err != nil {
						t.Fatalf("mutate TaskRun node snapshot: %v", err)
					}
				},
			},
		} {
			t.Run(string(mode)+"/"+test.name, func(t *testing.T) {
				fixture, service, point := newCommittedRcloneLifecycleDeleteFixture(t, mode)
				test.mutate(t, fixture)
				factoryCalls := 0
				if mode == backupasset.PublicationNativeObjectVersions {
					service.publication.rcloneNativeFactoryBuilder = func(context.Context, provider.RcloneNativeBootstrap, string, int) (RcloneNativeFactory, error) {
						factoryCalls++
						return nil, errors.New("native factory must not run before TaskRun authority")
					}
				}
				_, err := service.ResolveLifecycleDeletePoint(
					lifecycleNativeResolveContext(t, fixture.now), strings.Repeat("e", 32), point, fixture.repository,
				)
				if !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
					t.Fatalf("TaskRun %s error=%v, want identity conflict", test.name, err)
				}
				if factoryCalls != 0 {
					t.Fatalf("TaskRun %s touched native factory %d times", test.name, factoryCalls)
				}
			})
		}
	}
}

func TestResolveLifecycleDeletePointProtectsVersionReferencedByNewerNativePoint(t *testing.T) {
	fixture, service, ownerPoint := newCommittedRcloneLifecycleDeleteFixture(t, backupasset.PublicationNativeObjectVersions)
	ownerLocator, err := decodeManagedRclonePointLocator(ownerPoint.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	ownerAttempt, err := provider.DecodeRcloneAttemptV1(ownerLocator.TaggedAttempt)
	if err != nil {
		t.Fatal(err)
	}
	markerKey, err := fixture.service.rcloneMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	sharedVersion := provider.RcloneNativeExactVersion{PhysicalKey: "managed/v1/data/file.bin", VersionID: "v-owned-1"}
	newRun := model.TaskRun{
		TaskID: fixture.task.ID, NodeIDSnapshot: fixture.task.NodeID, TriggerType: "manual", Status: "success",
		StartedAt: timePointer(fixture.now), FinishedAt: timePointer(fixture.now.Add(time.Minute)),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&newRun).Error; err != nil {
		t.Fatal(err)
	}
	newAttempt := ownerAttempt
	newAttempt.RecoveryPointID = strings.Repeat("6", 32)
	newAttempt.AttemptID = strings.Repeat("7", 32)
	newAttempt.TaskRunID = newRun.ID
	newCommit := validRcloneNativeRepositoryCommit(newAttempt, strings.Repeat("e", 64), fixture.now.Add(2*time.Minute))
	newDataVersion := provider.RcloneNativeExactVersion{PhysicalKey: "managed/v1/data/new.bin", VersionID: "v-new-1"}
	newCommit.Native.FrozenNativeVersions = []provider.RcloneNativeExactVersion{
		newDataVersion,
		{PhysicalKey: newCommit.Native.CommitKey, VersionID: newCommit.Native.CommitVersionID},
	}
	newCommit.Native.FrozenNativeReferences = []provider.RcloneNativeExactVersion{sharedVersion, newDataVersion}
	locatorPayload, newLocator, err := encodeManagedRclonePointLocator(newAttempt, fixture.binding, markerKey, newCommit)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: newAttempt.TaskRepositoryLinkID,
		TaskID: newAttempt.TaskID, TaskRunID: newAttempt.TaskRunID, Trigger: newAttempt.Trigger,
		PublicationMode: string(newAttempt.PublicationMode), PointCodecVersion: 1, TagCodecVersion: 0,
		StartedAt: newAttempt.CaptureStartedAt, PreparedAt: newAttempt.PreparedAt, PointDeadlineAt: newAttempt.PointDeadlineAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	consistency, err := backupasset.EncodePublicationConsistency(backupasset.PublicationConsistencyV1{
		Version: 1, Provider: backupasset.ProviderRclone,
		RepositoryIdentityDigest: newAttempt.RepositoryIdentityDigest,
		ProviderCommitDigest:     newLocator.ProviderCommitDigest,
		CapabilityRevision:       fixture.repository.CapabilityRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	capturedAt := newCommit.ProviderCommittedAt.UTC()
	newPoint := model.RecoveryPoint{
		ID: newAttempt.RecoveryPointID, RepositoryID: fixture.repository.ID,
		ProducingTaskID: &fixture.task.ID, ProducingTaskRunID: &newRun.ID,
		ProducingTaskNameSnapshot: fixture.task.Name, ProducingNodeIDSnapshot: fixture.task.NodeID,
		ProducingNodeNameSnapshot: fixture.task.Node.Name, LineageJSON: lineage,
		EncryptedProviderLocator: locatorPayload, Semantics: string(backupasset.PointXirangManifest),
		State: string(backupasset.RecoveryPointCommitted), CapturedAt: &capturedAt, CommittedAt: &capturedAt,
		SourceFingerprint: newLocator.PhysicalIdentityDigest, ManifestDigestAlgorithm: "sha256",
		ManifestDigest: newCommit.ManifestIndexDigest, EntryCount: int64(newCommit.ManifestEntryCount),
		LogicalBytes: int64(newCommit.LogicalBytes), ConsistencyJSON: consistency, FidelityJSON: "{}",
		CapabilityRevision: fixture.repository.CapabilityRevision, CapabilitiesJSON: fixture.repository.CapabilitiesJSON,
		ImmutabilityLevel: fixture.repository.ImmutabilityLevel, PhysicalAvailability: string(backupasset.PhysicalOnline),
		HoldState: string(backupasset.HoldNone), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&newPoint).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := persistManagedRcloneNativeVersionEvidenceTx(
		context.Background(), fixture.db, fixture.repository.ID, newPoint.ID, markerKey, newCommit.Native, fixture.now,
	); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&newPoint, "id = ?", newPoint.ID).Error; err != nil {
		t.Fatal(err)
	}

	factoryCalls := 0
	originalBuilder := service.publication.rcloneNativeFactoryBuilder
	service.publication.rcloneNativeFactoryBuilder = func(ctx context.Context, bootstrap provider.RcloneNativeBootstrap, region string, attempts int) (RcloneNativeFactory, error) {
		factoryCalls++
		return originalBuilder(ctx, bootstrap, region, attempts)
	}
	var ownerDeletionErr error
	_, ownerDeletionErr = service.ResolveLifecycleDeletePoint(
		lifecycleNativeResolveContext(t, fixture.now), strings.Repeat("e", 32), ownerPoint, fixture.repository,
	)
	if !errors.Is(ownerDeletionErr, provider.ErrDeletePointNativeVersionReferenced) {
		t.Fatalf("owner deletion with live reference error=%v, want native-version reference dependency", ownerDeletionErr)
	}
	if errors.Is(ownerDeletionErr, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("owner deletion with live reference aliased identity conflict: %v", ownerDeletionErr)
	}
	if factoryCalls != 0 {
		t.Fatalf("owner deletion touched native factory %d times despite live reference", factoryCalls)
	}

	request, err := service.ResolveLifecycleDeletePoint(
		lifecycleNativeResolveContext(t, fixture.now), strings.Repeat("f", 32), newPoint, fixture.repository,
	)
	if err != nil {
		t.Fatalf("newer point deletion resolution: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.RcloneNativeDeletionAccess)
	if !ok {
		t.Fatalf("newer point deletion access=%T", request.Snapshot.Access.AdapterData)
	}
	for _, version := range access.Versions {
		if version == sharedVersion {
			t.Fatalf("newer point owned deletion set contains shared prior version: %+v", access.Versions)
		}
	}
}

func TestResolveLifecycleDeletePointNativePublicationPreparingAllowsCommit(t *testing.T) {
	fixture, service, ownerPoint := newCommittedRcloneLifecycleDeleteFixture(t, backupasset.PublicationNativeObjectVersions)
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", ownerPoint.ID).Updates(map[string]any{
		"state": backupasset.RecoveryPointCommitted, "committed_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.First(&ownerPoint, "id = ?", ownerPoint.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.TaskRun{}).Where("id = ?", fixture.taskRun.ID).Updates(map[string]any{
		"status": "success", "finished_at": fixture.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	run := model.TaskRun{
		TaskID: fixture.task.ID, NodeIDSnapshot: fixture.task.NodeID, TriggerType: "manual", Status: "running",
		StartedAt: timePointer(fixture.now), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	execution, err := fixture.service.Prepare(context.Background(), publication.Run{
		Task: fixture.task, TaskRunID: run.ID, Trigger: run.TriggerType, StartedAt: *run.StartedAt,
		Audit: backupasset.PublicationAuditContext{
			Actor:         backupasset.AuditActor{UserID: 9, Username: "operator", Role: "operator"},
			CorrelationID: "paused-native-publication",
		},
	})
	if err != nil {
		t.Fatalf("prepare paused native publication: %v", err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
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
	factoryCalls := 0
	service.publication.rcloneNativeFactoryBuilder = func(context.Context, provider.RcloneNativeBootstrap, string, int) (RcloneNativeFactory, error) {
		factoryCalls++
		return nil, errors.New("native factory must not run while publication is preparing")
	}
	_, ownerDeletionErr := service.ResolveLifecycleDeletePoint(
		lifecycleNativeResolveContext(t, fixture.now), strings.Repeat("9", 32), ownerPoint, fixture.repository,
	)
	if !errors.Is(ownerDeletionErr, provider.ErrDeletePointNativeVersionReferenced) {
		t.Fatalf("delete during preparing publication error=%v, want native-version reference dependency", ownerDeletionErr)
	}
	if errors.Is(ownerDeletionErr, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("delete during preparing publication aliased native-version reference dependency: %v", ownerDeletionErr)
	}
	if factoryCalls != 0 {
		t.Fatalf("delete during preparing publication touched native factory %d times", factoryCalls)
	}

	blockedAttempt := model.RecoveryPointLifecycleAttempt{
		ID: strings.Repeat("a", 32), RecoveryPointID: ownerPoint.ID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseBlocked),
		TransitionRevision: 1, BlockedReason: string(backupasset.LifecycleBlockedProviderNativeVersionReferenced),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&blockedAttempt).Error; err != nil {
		t.Fatal(err)
	}
	commit := validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	outcome, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit))
	if err != nil {
		t.Fatalf("record native publication B provider commit: %v", err)
	}
	if outcome.RecoveryPointID != attempt.RecoveryPointID || outcome.State != backupasset.RecoveryPointVerifying ||
		!outcome.ProviderCommitRecorded {
		t.Fatalf("native publication B commit outcome=%+v, want verifying provider commit recorded", outcome)
	}

	var publicationPointB model.RecoveryPoint
	if err := fixture.db.First(&publicationPointB, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if publicationPointB.State != string(backupasset.RecoveryPointVerifying) {
		t.Fatalf("native publication B state=%q, want verifying", publicationPointB.State)
	}
	markerKey, err := fixture.service.rcloneMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := decodeManagedRclonePointLocator(publicationPointB.EncryptedProviderLocator)
	if err != nil {
		t.Fatal(err)
	}
	owned, references, err := loadManagedRcloneNativeVersionEvidenceTx(
		context.Background(), fixture.db, fixture.repository.ID, publicationPointB.ID,
		markerKey, locator, managedRcloneNativeControlCommitKey(fixture.binding, attempt),
	)
	if err != nil {
		t.Fatal(err)
	}
	wantOwned, err := orderedManagedRcloneNativeVersions(
		commit.Native.FrozenNativeVersions, model.RecoveryPointRcloneNativeEvidenceRoleOwned,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantReferences, err := orderedManagedRcloneNativeVersions(
		commit.Native.FrozenNativeReferences, model.RecoveryPointRcloneNativeEvidenceRoleReference,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(owned, wantOwned) || !reflect.DeepEqual(references, wantReferences) {
		t.Fatalf("native publication B durable evidence owned=%+v references=%+v, want owned=%+v references=%+v",
			owned, references, wantOwned, wantReferences)
	}

	var activePublicationLeases int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ? AND owner_id = ? AND status = ?",
			publicationPointB.ID, backupasset.LeaseHolderPointPublication, publicationLeaseOwner, backupasset.LeaseActive).
		Count(&activePublicationLeases).Error; err != nil {
		t.Fatal(err)
	}
	if activePublicationLeases != 0 {
		t.Fatalf("native publication B active publication leases=%d, want zero", activePublicationLeases)
	}
	var publicationLease model.RecoveryPointLease
	if err := fixture.db.Where(
		"recovery_point_id = ? AND holder_type = ? AND owner_id = ?",
		publicationPointB.ID, backupasset.LeaseHolderPointPublication, publicationLeaseOwner,
	).First(&publicationLease).Error; err != nil {
		t.Fatal(err)
	}
	if publicationLease.Status != string(backupasset.LeaseReleased) || publicationLease.ReleasedAt == nil {
		t.Fatalf("native publication B publication lease=%+v, want released", publicationLease)
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
	commit.Native.FrozenNativeVersions = []provider.RcloneNativeExactVersion{
		{PhysicalKey: "managed/v1/data/file.bin", VersionID: "v-owned-1"},
		{PhysicalKey: commit.Native.CommitKey, VersionID: commit.Native.CommitVersionID},
	}
	commit.Native.FrozenNativeReferences = []provider.RcloneNativeExactVersion{
		{PhysicalKey: "managed/v1/data/file.bin", VersionID: "v-owned-1"},
	}
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
func TestResolveLifecycleDeletePointReconstructsResticProductionLineage(t *testing.T) {
	fixture, service, point := newResticLifecycleDeleteFixture(t)
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("ResolveLifecycleDeletePoint Restic: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.ResticRuntimeAccess)
	if !ok || access.Command == nil || access.Command.Node.ID != fixture.node.ID {
		t.Fatalf("Restic deletion access=%T %+v", request.Snapshot.Access.AdapterData, request.Snapshot.Access.AdapterData)
	}
	if request.Point.Native != strings.Repeat("c", 64) ||
		request.Snapshot.Access.RepositoryID != fixture.repository.ID ||
		request.Snapshot.Access.TaskID != fixture.task.ID ||
		request.Snapshot.Access.NodeID != fixture.node.ID {
		t.Fatalf("Restic deletion request=%+v", request)
	}
}
func TestResolveLifecycleDeletePointSharedResticProducerUsesDurableBindingOwnerProof(t *testing.T) {
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	secondPassword := "FAKE_SECOND_RESTIC_PASSWORD_FOR_LIFECYCLE_TEST_ONLY"
	secondTask := seedTask(t, fixture.db, "restic", "sftp:second@example.invalid:/repository", fmt.Sprintf(`{"repository_password":%q}`, secondPassword))
	var secondNode model.Node
	if err := fixture.db.First(&secondNode, secondTask.NodeID).Error; err != nil {
		t.Fatal(err)
	}
	secondTask.Node = secondNode
	secondRun := model.TaskRun{
		TaskID: secondTask.ID, TriggerType: "manual", Status: "running",
		StartedAt: timePointer(fixture.now.Add(-time.Second)), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&secondRun).Error; err != nil {
		t.Fatal(err)
	}
	secondLink := model.TaskRepositoryLink{
		ID: strings.Repeat("4", 32), TaskID: &secondTask.ID, RepositoryID: fixture.repository.ID,
		TaskNameSnapshot: secondTask.Name, NodeIDSnapshot: secondNode.ID, NodeNameSnapshot: secondNode.Name,
		PublicationMode: string(backupasset.PublicationNativeSnapshot), LinkedAt: fixture.now, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&secondLink).Error; err != nil {
		t.Fatal(err)
	}
	execution, err := fixture.service.Prepare(context.Background(), publication.Run{
		Task: secondTask, TaskRunID: secondRun.ID, Trigger: secondRun.TriggerType, StartedAt: *secondRun.StartedAt,
		Audit: backupasset.PublicationAuditContext{
			Actor:         backupasset.AuditActor{UserID: 9, Username: "operator", Role: "operator"},
			CorrelationID: "lifecycle-shared-restic-2",
		},
	})
	if err != nil {
		t.Fatalf("prepare second shared Restic producer: %v", err)
	}
	attempt := resticAttemptForExecution(t, execution)
	if attempt.Access.TaskID != secondTask.ID || attempt.Access.NodeID != secondNode.ID {
		t.Fatalf("second publication access=%+v, want producer Task/Node", attempt.Access)
	}
	evidence := fixture.commitEvidence()
	evidence.NativePointID = strings.Repeat("d", 64)
	if _, err := execution.RecordProviderCommit(context.Background(), resticProviderCommit(evidence)); err != nil {
		t.Fatalf("commit second shared Restic producer: %v", err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&point).Updates(map[string]any{
		"state": string(backupasset.RecoveryPointCommitted), "committed_at": fixture.now,
	}).Error; err != nil {
		t.Fatalf("mark second shared Restic point committed: %v", err)
	}
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	service := newLifecycleDeleteService(t, fixture.service, fixture.now)
	request, err := service.ResolveLifecycleDeletePoint(
		context.Background(), strings.Repeat("e", 32), point, fixture.repository,
	)
	if err != nil {
		t.Fatalf("resolve second shared Restic lifecycle deletion: %v", err)
	}
	access, ok := request.Snapshot.Access.AdapterData.(provider.ResticRuntimeAccess)
	if !ok || access.Command == nil || access.Command.Node.ID != secondNode.ID ||
		request.Point.Native != evidence.NativePointID ||
		request.Snapshot.Access.RepositoryID != fixture.repository.ID ||
		request.Snapshot.Access.TaskID != secondTask.ID ||
		request.Snapshot.Access.NodeID != secondNode.ID ||
		request.Snapshot.Access.Locator != secondTask.RsyncTarget ||
		string(request.Snapshot.Access.Secret) != secondPassword ||
		access.NativeRepositoryID != strings.Repeat("a", 64) {
		t.Fatalf("second shared Restic lifecycle request=%+v runtime=%+v", request, access)
	}
	ownerUnlinkedAt := fixture.now.Add(time.Minute)
	archiveService := taskservice.NewArchiveService(taskservice.ArchiveDependencies{
		DB:             fixture.db,
		RemoveSchedule: func(uint) error { return nil },
		Now:            func() time.Time { return ownerUnlinkedAt },
	})
	archiveResult, err := archiveService.Archive(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("archive Restic binding owner task: %v", err)
	}
	if !archiveResult.Archived || !archiveResult.Unlinked || archiveResult.ProviderBytesDeleted {
		t.Fatalf("archive Restic binding owner result=%+v, want archived/unlinked and provider_bytes_deleted=false", archiveResult)
	}
	var ownerLink model.TaskRepositoryLink
	if err := fixture.db.First(&ownerLink, "id = ?", fixture.link.ID).Error; err != nil {
		t.Fatalf("reload Restic binding owner link: %v", err)
	}
	if ownerLink.TaskID == nil || *ownerLink.TaskID != fixture.task.ID || ownerLink.UnlinkedAt == nil || !ownerLink.UnlinkedAt.Equal(ownerUnlinkedAt) {
		t.Fatalf("Restic owner link lost durable membership: %+v", ownerLink)
	}
	request, err = service.ResolveLifecycleDeletePoint(
		context.Background(), strings.Repeat("e", 32), point, fixture.repository,
	)
	if err != nil {
		t.Fatalf("resolve second shared Restic lifecycle deletion after owner unlink: %v", err)
	}
	access, ok = request.Snapshot.Access.AdapterData.(provider.ResticRuntimeAccess)
	if !ok || access.Command == nil || access.Command.Node.ID != secondNode.ID ||
		request.Snapshot.Access.TaskID != secondTask.ID ||
		string(request.Snapshot.Access.Secret) != secondPassword {
		t.Fatalf("second shared Restic lifecycle after owner unlink request=%+v runtime=%+v", request, access)
	}

	originalSource := point.SourceFingerprint
	point.SourceFingerprint = strings.Repeat("f", 64)
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
		Update("source_fingerprint", point.SourceFingerprint).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveLifecycleDeletePoint(
		context.Background(), strings.Repeat("e", 32), point, fixture.repository,
	); !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("second shared Restic source drift error=%v, want identity conflict", err)
	}
	point.SourceFingerprint = originalSource
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
		Update("source_fingerprint", point.SourceFingerprint).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", secondLink.ID).
		Update("publication_mode", string(backupasset.PublicationVersionedPrefix)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveLifecycleDeletePoint(
		context.Background(), strings.Repeat("e", 32), point, fixture.repository,
	); !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("second shared Restic link drift error=%v, want identity conflict", err)
	}
	if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", secondLink.ID).
		Update("publication_mode", string(backupasset.PublicationNativeSnapshot)).Error; err != nil {
		t.Fatal(err)
	}

	reboundTask := seedTask(t, fixture.db, "restic", fixture.task.RsyncTarget, `{"repository_password":"ROTATED_TEST_SECRET"}`)
	var reboundNode model.Node
	if err := fixture.db.First(&reboundNode, reboundTask.NodeID).Error; err != nil {
		t.Fatal(err)
	}
	reboundDocument := bindingDocument{
		Version: bindingDocumentVersion, Provider: backupasset.ProviderRestic, IdentityClass: provider.IdentityNativeRepository,
		TaskID: reboundTask.ID, NodeID: reboundNode.ID, IdentitySalt: strings.Repeat("07", provider.IdentitySaltBytes),
		Locator: reboundTask.RsyncTarget, Secret: "ROTATED_TEST_SECRET",
		EndpointFacts:      []string{"task:" + uintString(reboundTask.ID), "node:" + uintString(reboundNode.ID)},
		NativeRepositoryID: strings.Repeat("a", 64), AdapterRevision: "test-reader:v1",
	}
	payload, err := encodeBindingDocument(reboundDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RepositoryAccessBinding{}).
		Where("repository_id = ? AND status = ?", fixture.repository.ID, bindingStatusActive).
		Update("encrypted_config", payload).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveLifecycleDeletePoint(
		context.Background(), strings.Repeat("e", 32), point, fixture.repository,
	); !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("second shared Restic binding rebind error=%v, want identity conflict", err)
	}
}

func TestResolveLifecycleDeletePointSurvivesArchivedUnlinkedResticLineage(t *testing.T) {
	fixture, service, point := newResticLifecycleDeleteFixture(t)
	var binding model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ? AND status = ?", fixture.repository.ID, bindingStatusActive).
		First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	document, err := decodeBindingDocument(binding.EncryptedConfig)
	if err != nil {
		t.Fatal(err)
	}
	archivedAt := fixture.now.Add(time.Minute)
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Updates(map[string]any{
		"archived_at": archivedAt, "name": "restic-task-renamed",
		"rsync_target":    "sftp:user@example.invalid:/renamed-repository",
		"executor_config": `{"repository_password":"MUTATED_RESTIC_PASSWORD_FOR_TEST_ONLY"}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.Node{}).Where("id = ?", fixture.node.ID).
		Update("name", "restic-node-renamed").Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
		Updates(map[string]any{"task_id": nil, "unlinked_at": archivedAt}).Error; err != nil {
		t.Fatal(err)
	}
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("archived/unlinked/renamed Restic lineage: %v", err)
	}
	runtimeAccess, ok := request.Snapshot.Access.AdapterData.(provider.ResticRuntimeAccess)
	if !ok || runtimeAccess.Command == nil || runtimeAccess.Command.Node.ID != fixture.node.ID {
		t.Fatalf("archived/unlinked/renamed Restic access=%+v", request.Snapshot.Access.AdapterData)
	}
	if request.Snapshot.Access.Locator != document.Locator ||
		string(request.Snapshot.Access.Secret) != document.Secret {
		t.Fatalf(
			"archived/unlinked/renamed Restic credentials locator=%q secret=%q, want frozen binding locator=%q secret=%q",
			request.Snapshot.Access.Locator, request.Snapshot.Access.Secret, document.Locator, document.Secret,
		)
	}
}

func TestResolveLifecycleDeletePointSurvivesArchivedUnlinkedRsyncLineage(t *testing.T) {
	fixture, _, point, service := newCommittedRsyncLifecycleDeleteFixture(t)
	archivedAt := fixture.now.Add(time.Minute)
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Updates(map[string]any{
		"archived_at": archivedAt, "name": "rsync-task-renamed", "rsync_target": "/renamed-rsync-target",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.Node{}).Where("id = ?", fixture.task.NodeID).
		Update("name", "rsync-node-renamed").Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
		Updates(map[string]any{"task_id": nil, "unlinked_at": archivedAt}).Error; err != nil {
		t.Fatal(err)
	}
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("archived/unlinked/renamed Rsync lineage: %v", err)
	}
	if _, ok := request.Snapshot.Access.AdapterData.(provider.RsyncPointDeletionAccess); !ok {
		t.Fatalf("archived/unlinked/renamed Rsync access=%T", request.Snapshot.Access.AdapterData)
	}
}

func TestResolveLifecycleDeletePointSurvivesArchivedUnlinkedRcloneLineage(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix, backupasset.PublicationNativeObjectVersions,
	} {
		t.Run(string(mode), func(t *testing.T) {
			fixture, service, point := newCommittedRcloneLifecycleDeleteFixture(t, mode)
			archivedAt := fixture.now.Add(time.Minute)
			if err := fixture.db.Model(&model.Task{}).Where("id = ?", fixture.task.ID).Updates(map[string]any{
				"archived_at": archivedAt, "name": "rclone-task-renamed", "rsync_target": "backup:renamed",
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Model(&model.Node{}).Where("id = ?", fixture.task.NodeID).
				Update("name", "rclone-node-renamed").Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
				Updates(map[string]any{"task_id": nil, "unlinked_at": archivedAt}).Error; err != nil {
				t.Fatal(err)
			}
			request, err := service.ResolveLifecycleDeletePoint(
				lifecycleNativeResolveContext(t, fixture.now), strings.Repeat("e", 32), point, fixture.repository,
			)
			if err != nil {
				t.Fatalf("archived/unlinked/renamed Rclone lineage: %v", err)
			}
			switch mode {
			case backupasset.PublicationVersionedPrefix:
				if _, ok := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess); !ok {
					t.Fatalf("archived/unlinked/renamed Rclone access=%T", request.Snapshot.Access.AdapterData)
				}
			case backupasset.PublicationNativeObjectVersions:
				if _, ok := request.Snapshot.Access.AdapterData.(provider.RcloneNativeDeletionAccess); !ok {
					t.Fatalf("archived/unlinked/renamed Rclone access=%T", request.Snapshot.Access.AdapterData)
				}
			}
		})
	}
}

func TestResolveLifecycleDeletePointRejectsRsyncHistoricalLinkModeDrift(t *testing.T) {
	fixture, _, point, service := newCommittedRsyncLifecycleDeleteFixture(t)
	if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
		Update("publication_mode", string(backupasset.PublicationVersionedHardlink)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository); !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("Rsync historical link mode drift error=%v, want identity conflict", err)
	}
}

func TestResolveLifecycleDeletePointRejectsRsyncBindingLineageDrift(t *testing.T) {
	fixture, _, point, service := newCommittedRsyncLifecycleDeleteFixture(t)
	binding := fixture.binding
	binding.TaskRepositoryLinkID = strings.Repeat("a", 32)
	payload, err := encodeManagedRsyncBindingDocumentV2(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RepositoryAccessBinding{}).
		Where("repository_id = ? AND status = ?", fixture.repository.ID, bindingStatusActive).
		Update("encrypted_config", payload).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository); !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("Rsync binding lineage drift error=%v, want identity conflict", err)
	}
}

func TestResolveLifecycleDeletePointRejectsRcloneHistoricalLinkModeDrift(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix, backupasset.PublicationNativeObjectVersions,
	} {
		t.Run(string(mode), func(t *testing.T) {
			fixture, service, point := newCommittedRcloneLifecycleDeleteFixture(t, mode)
			otherMode := backupasset.PublicationVersionedPrefix
			if mode == otherMode {
				otherMode = backupasset.PublicationNativeObjectVersions
			}
			if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
				Update("publication_mode", string(otherMode)).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := service.ResolveLifecycleDeletePoint(
				lifecycleNativeResolveContext(t, fixture.now), strings.Repeat("e", 32), point, fixture.repository,
			); !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				t.Fatalf("Rclone historical link mode drift error=%v, want identity conflict", err)
			}
		})
	}
}

func TestResolveLifecycleDeletePointRejectsRcloneBindingLineageDrift(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix, backupasset.PublicationNativeObjectVersions,
	} {
		t.Run(string(mode), func(t *testing.T) {
			fixture, service, point := newCommittedRcloneLifecycleDeleteFixture(t, mode)
			binding := fixture.binding
			binding.TaskRepositoryLinkID = strings.Repeat("a", 32)
			payload, err := encodeManagedRcloneBindingDocumentV3(binding)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Model(&model.RepositoryAccessBinding{}).
				Where("repository_id = ? AND status = ?", fixture.repository.ID, bindingStatusActive).
				Update("encrypted_config", payload).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := service.ResolveLifecycleDeletePoint(
				lifecycleNativeResolveContext(t, fixture.now), strings.Repeat("e", 32), point, fixture.repository,
			); !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				t.Fatalf("Rclone binding lineage drift error=%v, want identity conflict", err)
			}
		})
	}
}

func TestResolveLifecycleDeletePointRejectsResticSameRepositoryRebind(t *testing.T) {
	fixture, service, point := newResticLifecycleDeleteFixture(t)
	reboundTask := seedTask(t, fixture.db, "restic", fixture.task.RsyncTarget, `{"repository_password":"ROTATED_TEST_SECRET"}`)
	var reboundNode model.Node
	if err := fixture.db.First(&reboundNode, reboundTask.NodeID).Error; err != nil {
		t.Fatal(err)
	}
	document := bindingDocument{
		Version: bindingDocumentVersion, Provider: backupasset.ProviderRestic, IdentityClass: provider.IdentityNativeRepository,
		TaskID: reboundTask.ID, NodeID: reboundNode.ID, IdentitySalt: strings.Repeat("07", provider.IdentitySaltBytes),
		Locator: fixture.task.RsyncTarget, Secret: "ROTATED_TEST_SECRET",
		EndpointFacts:      []string{"task:" + uintString(reboundTask.ID), "node:" + uintString(reboundNode.ID)},
		NativeRepositoryID: strings.Repeat("a", 64), AdapterRevision: "test-reader:v1",
	}
	payload, err := encodeBindingDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RepositoryAccessBinding{}).
		Where("repository_id = ? AND status = ?", fixture.repository.ID, bindingStatusActive).
		Update("encrypted_config", payload).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository); !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("same-repository Restic rebind error=%v, want identity conflict", err)
	}
}

func TestResolveLifecycleDeletePointResticIdentityClosure(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*publicationFixture, *model.RecoveryPoint)
	}{
		{
			name: "locator swap",
			mutate: func(_ *publicationFixture, point *model.RecoveryPoint) {
				locator, _ := decodeResticPointLocator(point.EncryptedProviderLocator)
				locator.FullSnapshotID = strings.Repeat("d", 64)
				payload, _ := json.Marshal(locator)
				point.EncryptedProviderLocator = string(payload)
			},
		},
		{
			name: "source swap",
			mutate: func(_ *publicationFixture, point *model.RecoveryPoint) {
				point.SourceFingerprint = strings.Repeat("d", 64)
			},
		},
		{
			name: "runtime capability revision drift",
			mutate: func(fixture *publicationFixture, _ *model.RecoveryPoint) {
				if err := fixture.db.Model(&model.BackupRepository{}).
					Where("id = ?", fixture.repository.ID).Update("capability_revision", 2).Error; err != nil {
					t.Fatalf("mutate Restic runtime capability revision: %v", err)
				}
			},
		},
		{
			name: "link mode drift",
			mutate: func(fixture *publicationFixture, _ *model.RecoveryPoint) {
				if err := fixture.db.Model(&model.TaskRepositoryLink{}).
					Where("id = ?", fixture.link.ID).
					Update("publication_mode", string(backupasset.PublicationVersionedPrefix)).Error; err != nil {
					t.Fatalf("mutate Restic link mode: %v", err)
				}
			},
		},
		{
			name: "frozen task snapshot drift",
			mutate: func(_ *publicationFixture, point *model.RecoveryPoint) {
				point.ProducingTaskNameSnapshot = "different-frozen-task"
			},
		},
		{
			name: "frozen node snapshot drift",
			mutate: func(_ *publicationFixture, point *model.RecoveryPoint) {
				point.ProducingNodeNameSnapshot = "different-frozen-node"
			},
		},
		{
			name: "missing point task snapshot",
			mutate: func(_ *publicationFixture, point *model.RecoveryPoint) {
				point.ProducingTaskNameSnapshot = ""
			},
		},
		{
			name: "missing point node snapshot",
			mutate: func(_ *publicationFixture, point *model.RecoveryPoint) {
				point.ProducingNodeNameSnapshot = ""
			},
		},
		{
			name: "missing link task snapshot",
			mutate: func(fixture *publicationFixture, _ *model.RecoveryPoint) {
				if err := fixture.db.Model(&model.TaskRepositoryLink{}).
					Where("id = ?", fixture.link.ID).Update("task_name_snapshot", "").Error; err != nil {
					t.Fatalf("mutate Restic link task snapshot: %v", err)
				}
			},
		},
		{
			name: "missing link node snapshot",
			mutate: func(fixture *publicationFixture, _ *model.RecoveryPoint) {
				if err := fixture.db.Model(&model.TaskRepositoryLink{}).
					Where("id = ?", fixture.link.ID).Update("node_name_snapshot", "").Error; err != nil {
					t.Fatalf("mutate Restic link node snapshot: %v", err)
				}
			},
		},
		{
			name: "link task snapshot drift",
			mutate: func(fixture *publicationFixture, _ *model.RecoveryPoint) {
				if err := fixture.db.Model(&model.TaskRepositoryLink{}).
					Where("id = ?", fixture.link.ID).Update("task_name_snapshot", "different-link-task").Error; err != nil {
					t.Fatalf("mutate Restic link task snapshot drift: %v", err)
				}
			},
		},
		{
			name: "link node snapshot drift",
			mutate: func(fixture *publicationFixture, _ *model.RecoveryPoint) {
				if err := fixture.db.Model(&model.TaskRepositoryLink{}).
					Where("id = ?", fixture.link.ID).Update("node_name_snapshot", "different-link-node").Error; err != nil {
					t.Fatalf("mutate Restic link node snapshot drift: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, service, point := newResticLifecycleDeleteFixture(t)
			test.mutate(fixture, &point)
			if test.name != "runtime capability revision drift" {
				if err := fixture.db.Save(&point).Error; err != nil {
					t.Fatalf("save Restic lifecycle mutation: %v", err)
				}
			}
			if err := fixture.db.First(&point, "id = ?", point.ID).Error; err != nil {
				t.Fatalf("reload Restic lifecycle point: %v", err)
			}
			_, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
			if !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				t.Fatalf("Restic %s error=%v, want identity conflict", test.name, err)
			}
		})
	}
}

func newResticLifecycleDeleteFixture(t *testing.T) (*publicationFixture, *Service, model.RecoveryPoint) {
	t.Helper()
	fixture := newPublicationFixture(t, true, publication.AdmissionManaged)
	fixture.connectExactResticBinding(t)
	native := strings.Repeat("c", 64)
	identity := *fixture.repository.RepositoryIdentity
	pointID := fixture.expectedPointID(t)
	locator, err := json.Marshal(resticPointLocatorV1{
		Version: 1, Provider: string(backupasset.ProviderRestic), FullSnapshotID: native,
	})
	if err != nil {
		t.Fatalf("encode Restic lifecycle locator: %v", err)
	}
	lineage, err := backupasset.EncodePublicationLineage(backupasset.PublicationLineageV1{
		Version: 1, TaskRepositoryLinkID: fixture.link.ID, TaskID: fixture.task.ID, TaskRunID: fixture.taskRun.ID,
		Trigger: "manual", PublicationMode: string(backupasset.PublicationNativeSnapshot),
		PointCodecVersion: 1, TagCodecVersion: 1,
		StartedAt: fixture.now.Add(-time.Minute), PreparedAt: fixture.now, PointDeadlineAt: fixture.now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("encode Restic lifecycle lineage: %v", err)
	}
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: fixture.repository.ID,
		ProducingTaskID: &fixture.task.ID, ProducingTaskRunID: &fixture.taskRun.ID,
		ProducingTaskNameSnapshot: fixture.task.Name, ProducingNodeIDSnapshot: fixture.node.ID,
		ProducingNodeNameSnapshot: fixture.node.Name, LineageJSON: lineage,
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
		CapturedAt: timePointer(fixture.now), CommittedAt: timePointer(fixture.now),
		SourceFingerprint: resticSourceFingerprint(identity, native), CapabilityRevision: 1,
		EncryptedProviderLocator: string(locator), ManifestDigestAlgorithm: "sha256",
		CapabilitiesJSON:     `{"list":true,"open_sequential":true}`,
		ImmutabilityLevel:    string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	if err := fixture.db.Create(&point).Error; err != nil {
		t.Fatalf("seed Restic lifecycle point: %v", err)
	}
	if err := fixture.db.First(&point, "id = ?", point.ID).Error; err != nil {
		t.Fatalf("reload Restic lifecycle point: %v", err)
	}
	return fixture, newLifecycleDeleteService(t, fixture.service, fixture.now), point
}

func TestResolveLifecycleDeletePointRsyncIdentityClosure(t *testing.T) {
	for _, test := range []struct {
		name            string
		mutate          func(*managedRsyncPointLocatorV1, *model.RecoveryPoint, *model.BackupRepository)
		mutateLink      func(*rsyncPublicationFixture)
		wantUnavailable bool
	}{
		{name: "locator repository swap", mutate: func(locator *managedRsyncPointLocatorV1, _ *model.RecoveryPoint, _ *model.BackupRepository) {
			locator.RepositoryID = strings.Repeat("8", 32)
		}, wantUnavailable: true},
		{name: "locator point swap", mutate: func(locator *managedRsyncPointLocatorV1, _ *model.RecoveryPoint, _ *model.BackupRepository) {
			locator.RecoveryPointID = strings.Repeat("9", 32)
		}, wantUnavailable: true},
		{name: "source swap", mutate: func(_ *managedRsyncPointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
			point.SourceFingerprint = strings.Repeat("d", 64)
		}},
		{name: "malformed attempt", mutate: func(locator *managedRsyncPointLocatorV1, _ *model.RecoveryPoint, _ *model.BackupRepository) {
			locator.TaggedAttempt = "{"
		}, wantUnavailable: true},
		{name: "capability revision drift", mutate: func(_ *managedRsyncPointLocatorV1, _ *model.RecoveryPoint, repository *model.BackupRepository) {
			repository.CapabilityRevision = 2
		}},
		{name: "frozen task snapshot drift", mutate: func(_ *managedRsyncPointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
			point.ProducingTaskNameSnapshot = "different-frozen-task"
		}},
		{name: "frozen node snapshot drift", mutate: func(_ *managedRsyncPointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
			point.ProducingNodeNameSnapshot = "different-frozen-node"
		}},
		{name: "missing point task snapshot", mutate: func(_ *managedRsyncPointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
			point.ProducingTaskNameSnapshot = ""
		}},
		{name: "missing point node snapshot", mutate: func(_ *managedRsyncPointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
			point.ProducingNodeNameSnapshot = ""
		}},
		{name: "missing link task snapshot", mutateLink: func(fixture *rsyncPublicationFixture) {
			if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
				Update("task_name_snapshot", "").Error; err != nil {
				fixture.t.Fatalf("mutate Rsync link task snapshot: %v", err)
			}
		}},
		{name: "missing link node snapshot", mutateLink: func(fixture *rsyncPublicationFixture) {
			if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
				Update("node_name_snapshot", "").Error; err != nil {
				fixture.t.Fatalf("mutate Rsync link node snapshot: %v", err)
			}
		}},
		{name: "link task snapshot drift", mutateLink: func(fixture *rsyncPublicationFixture) {
			if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
				Update("task_name_snapshot", "different-link-task").Error; err != nil {
				fixture.t.Fatalf("mutate Rsync link task snapshot drift: %v", err)
			}
		}},
		{name: "link node snapshot drift", mutateLink: func(fixture *rsyncPublicationFixture) {
			if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
				Update("node_name_snapshot", "different-link-node").Error; err != nil {
				fixture.t.Fatalf("mutate Rsync link node snapshot drift: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, _, point, service := newCommittedRsyncLifecycleDeleteFixture(t)
			locator, err := decodeManagedRsyncPointLocator(point.EncryptedProviderLocator)
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(&locator, &point, &fixture.repository)
			}
			if test.mutateLink != nil {
				test.mutateLink(fixture)
			}
			if test.name == "locator repository swap" || test.name == "locator point swap" || test.name == "malformed attempt" {
				payload, marshalErr := json.Marshal(locator)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				point.EncryptedProviderLocator = string(payload)
			}
			if _, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository); err == nil {
				t.Fatalf("Rsync %s unexpectedly resolved", test.name)
			} else if test.wantUnavailable {
				reason, _, ok := CapabilityFromError(err)
				if !ok || reason.Code != backupasset.CapabilityDeletionUnavailable {
					t.Fatalf("Rsync %s error=%v, want deletion_unavailable", test.name, err)
				}
			} else if !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				t.Fatalf("Rsync %s error=%v, want identity conflict", test.name, err)
			}
		})
	}
}

func newCommittedRsyncLifecycleDeleteFixture(t *testing.T) (*rsyncPublicationFixture, *rsyncPublicationExecution, model.RecoveryPoint, *Service) {
	t.Helper()
	fixture := newRsyncPublicationFixture(t)
	execution, err := fixture.service.Prepare(context.Background(), fixture.run())
	if err != nil {
		t.Fatal(err)
	}
	state, ok := execution.(*rsyncPublicationExecution)
	if !ok {
		t.Fatalf("Rsync execution type=%T", execution)
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
	return fixture, state, point, newLifecycleDeleteService(t, fixture.service, fixture.now)
}

func TestResolveLifecycleDeletePointRcloneIdentityClosure(t *testing.T) {
	for _, mode := range []backupasset.TaskPublicationMode{
		backupasset.PublicationVersionedPrefix, backupasset.PublicationNativeObjectVersions,
	} {
		t.Run(string(mode), func(t *testing.T) {
			for _, test := range []struct {
				name            string
				mutate          func(*managedRclonePointLocatorV1, *model.RecoveryPoint, *model.BackupRepository)
				mutateLink      func(*rclonePublicationFixture)
				wantUnavailable bool
			}{
				{name: "locator repository ID swap", mutate: func(locator *managedRclonePointLocatorV1, _ *model.RecoveryPoint, _ *model.BackupRepository) {
					locator.RepositoryID = strings.Repeat("8", 32)
				}, wantUnavailable: true},
				{name: "locator point ID swap", mutate: func(locator *managedRclonePointLocatorV1, _ *model.RecoveryPoint, _ *model.BackupRepository) {
					locator.RecoveryPointID = strings.Repeat("9", 32)
				}, wantUnavailable: true},
				{name: "locator attempt ID swap", mutate: func(locator *managedRclonePointLocatorV1, _ *model.RecoveryPoint, _ *model.BackupRepository) {
					locator.AttemptID = strings.Repeat("a", 32)
				}, wantUnavailable: true},
				{name: "attempt capability drift", mutate: func(locator *managedRclonePointLocatorV1, _ *model.RecoveryPoint, _ *model.BackupRepository) {
					tagged, err := provider.DecodePublicationAttempt(locator.TaggedAttempt)
					if err != nil {
						t.Fatalf("decode Rclone attempt: %v", err)
					}
					attempt, err := tagged.RcloneAttempt()
					if err != nil {
						t.Fatalf("decode Rclone attempt branch: %v", err)
					}
					attempt.CapabilityRevision++
					locator.TaggedAttempt, err = provider.EncodePublicationAttempt(provider.NewRclonePublicationAttempt(attempt))
					if err != nil {
						t.Fatalf("encode Rclone attempt: %v", err)
					}
				}},
				{name: "commit ID drift", mutate: func(locator *managedRclonePointLocatorV1, _ *model.RecoveryPoint, _ *model.BackupRepository) {
					tagged, err := provider.DecodeProviderCommit(locator.TaggedCommit)
					if err != nil {
						t.Fatalf("decode Rclone commit: %v", err)
					}
					commit, err := tagged.RcloneCommit()
					if err != nil {
						t.Fatalf("decode Rclone commit branch: %v", err)
					}
					commit.AttemptID = strings.Repeat("b", 32)
					locator.TaggedCommit, err = provider.EncodeProviderCommit(provider.NewRcloneProviderCommit(commit))
					if err != nil {
						t.Fatalf("encode Rclone commit: %v", err)
					}
				}, wantUnavailable: true},
				{name: "source fingerprint drift", mutate: func(_ *managedRclonePointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
					point.SourceFingerprint = strings.Repeat("d", 64)
				}},
				{name: "repository capability drift", mutate: func(_ *managedRclonePointLocatorV1, _ *model.RecoveryPoint, repository *model.BackupRepository) {
					repository.CapabilityRevision++
				}},
				{name: "durable manifest evidence drift", mutate: func(_ *managedRclonePointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
					point.ManifestDigest = strings.Repeat("d", 64)
				}},
				{name: "frozen task snapshot drift", mutate: func(_ *managedRclonePointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
					point.ProducingTaskNameSnapshot = "different-frozen-task"
				}},
				{name: "frozen node snapshot drift", mutate: func(_ *managedRclonePointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
					point.ProducingNodeNameSnapshot = "different-frozen-node"
				}},
				{name: "missing point task snapshot", mutate: func(_ *managedRclonePointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
					point.ProducingTaskNameSnapshot = ""
				}},
				{name: "missing point node snapshot", mutate: func(_ *managedRclonePointLocatorV1, point *model.RecoveryPoint, _ *model.BackupRepository) {
					point.ProducingNodeNameSnapshot = ""
				}},
				{name: "missing link task snapshot", mutateLink: func(fixture *rclonePublicationFixture) {
					if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
						Update("task_name_snapshot", "").Error; err != nil {
						fixture.t.Fatalf("mutate Rclone link task snapshot: %v", err)
					}
				}},
				{name: "missing link node snapshot", mutateLink: func(fixture *rclonePublicationFixture) {
					if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
						Update("node_name_snapshot", "").Error; err != nil {
						fixture.t.Fatalf("mutate Rclone link node snapshot: %v", err)
					}
				}},
				{name: "link task snapshot drift", mutateLink: func(fixture *rclonePublicationFixture) {
					if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
						Update("task_name_snapshot", "different-link-task").Error; err != nil {
						fixture.t.Fatalf("mutate Rclone link task snapshot drift: %v", err)
					}
				}},
				{name: "link node snapshot drift", mutateLink: func(fixture *rclonePublicationFixture) {
					if err := fixture.db.Model(&model.TaskRepositoryLink{}).Where("id = ?", fixture.link.ID).
						Update("node_name_snapshot", "different-link-node").Error; err != nil {
						fixture.t.Fatalf("mutate Rclone link node snapshot drift: %v", err)
					}
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					fixture, service, point := newCommittedRcloneLifecycleDeleteFixture(t, mode)
					locator, err := decodeManagedRclonePointLocator(point.EncryptedProviderLocator)
					if err != nil {
						t.Fatal(err)
					}
					if test.mutate != nil {
						test.mutate(&locator, &point, &fixture.repository)
					}
					if test.mutateLink != nil {
						test.mutateLink(fixture)
					}
					if test.name != "repository capability drift" {
						payload, marshalErr := marshalManagedRclonePointLocatorForTest(locator)
						if marshalErr != nil {
							t.Fatal(marshalErr)
						}
						point.EncryptedProviderLocator = string(payload)
					}
					factoryCalls := 0
					if mode == backupasset.PublicationNativeObjectVersions {
						originalBuilder := service.publication.rcloneNativeFactoryBuilder
						service.publication.rcloneNativeFactoryBuilder = func(ctx context.Context, bootstrap provider.RcloneNativeBootstrap, region string, attempts int) (RcloneNativeFactory, error) {
							factoryCalls++
							return originalBuilder(ctx, bootstrap, region, attempts)
						}
					}
					_, err = service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
					if err == nil {
						t.Fatalf("Rclone %s unexpectedly resolved", test.name)
					}
					if test.wantUnavailable {
						reason, _, ok := CapabilityFromError(err)
						if !ok || reason.Code != backupasset.CapabilityDeletionUnavailable {
							t.Fatalf("Rclone %s error=%v, want deletion_unavailable", test.name, err)
						}
					} else if !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
						t.Fatalf("Rclone %s error=%v, want identity conflict", test.name, err)
					}
					if mode == backupasset.PublicationNativeObjectVersions && factoryCalls != 0 {
						t.Fatalf("Rclone %s touched native factory %d times before closure", test.name, factoryCalls)
					}
				})
			}
		})
	}
}

func newCommittedRcloneLifecycleDeleteFixture(t *testing.T, mode backupasset.TaskPublicationMode) (*rclonePublicationFixture, *Service, model.RecoveryPoint) {
	t.Helper()
	fixture := newRclonePublicationFixture(t, mode)
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
	var commit provider.RcloneCommitV1
	if mode == backupasset.PublicationVersionedPrefix {
		commit = validRcloneRepositoryCommit(attempt, input.PortableRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
	} else {
		commit = validRcloneNativeRepositoryCommit(attempt, input.NativeRequest.CostEvidenceDigest, fixture.now.Add(time.Minute))
		commit.Native.FrozenNativeVersions = []provider.RcloneNativeExactVersion{
			{PhysicalKey: "managed/v1/data/file.bin", VersionID: "v-owned-1"},
			{PhysicalKey: commit.Native.CommitKey, VersionID: commit.Native.CommitVersionID},
		}
		commit.Native.FrozenNativeReferences = []provider.RcloneNativeExactVersion{
			{PhysicalKey: "managed/v1/data/file.bin", VersionID: "v-owned-1"},
		}
	}
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	return fixture, newLifecycleDeleteService(t, fixture.service, fixture.now), point
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

func lifecycleNativeResolveContext(t *testing.T, now time.Time) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(20*time.Minute))
	t.Cleanup(cancel)
	return ctx
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
