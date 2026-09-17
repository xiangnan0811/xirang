package repository

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	if len(request.Snapshot.Access.IdentitySalt) != provider.IdentitySaltBytes ||
		request.Snapshot.Access.TaskID != fixture.task.ID || request.Snapshot.Access.NodeID != fixture.task.NodeID ||
		access.Command == nil || access.Command.Node.ID != fixture.task.NodeID ||
		len(request.Snapshot.Access.EndpointFacts) == 0 {
		t.Fatalf("rsync lifecycle identity authority=%+v access=%+v", request.Snapshot.Access, access)
	}
	if _, err := provider.DeletionTargetIdentityDigest(provider.DeletionTargetIdentityInput{
		RecoveryPointID: point.ID, AttemptID: request.OperationID, Operation: backupasset.LifecycleRetentionExpire,
		RepositoryIdentity: lifecycleRepositoryIdentity(fixture.repository), Request: request,
	}); err != nil {
		t.Fatalf("rsync lifecycle target identity: %v", err)
	}
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
	if len(request.Snapshot.Access.IdentitySalt) != provider.IdentitySaltBytes ||
		request.Snapshot.Access.TaskID != fixture.task.ID || request.Snapshot.Access.NodeID != fixture.task.NodeID ||
		len(request.Snapshot.Access.EndpointFacts) == 0 || len(request.Snapshot.Access.Config) == 0 ||
		request.Snapshot.Access.Locator != locator.PortableAttemptRoot {
		t.Fatalf("rclone prefix lifecycle identity authority=%+v", request.Snapshot.Access)
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
	if len(request.Snapshot.Access.IdentitySalt) != provider.IdentitySaltBytes ||
		request.Snapshot.Access.TaskID != fixture.task.ID || request.Snapshot.Access.NodeID != fixture.task.NodeID ||
		len(request.Snapshot.Access.EndpointFacts) == 0 || access.Command == nil ||
		access.Command.Node.ID != fixture.task.NodeID {
		t.Fatalf("native lifecycle identity authority=%+v access=%+v", request.Snapshot.Access, access)
	}
	if _, err := provider.DeletionTargetIdentityDigest(provider.DeletionTargetIdentityInput{
		RecoveryPointID: point.ID, AttemptID: request.OperationID, Operation: backupasset.LifecycleRetentionExpire,
		RepositoryIdentity: lifecycleRepositoryIdentity(fixture.repository), Request: request,
	}); err != nil {
		t.Fatalf("native lifecycle target identity: %v", err)
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
	if len(request.Snapshot.Access.IdentitySalt) != provider.IdentitySaltBytes ||
		len(request.Snapshot.Access.EndpointFacts) == 0 {
		t.Fatalf("Restic lifecycle identity authority=%+v", request.Snapshot.Access)
	}
	if _, err := provider.DeletionTargetIdentityDigest(provider.DeletionTargetIdentityInput{
		RecoveryPointID: point.ID, AttemptID: request.OperationID, Operation: backupasset.LifecycleRetentionExpire,
		RepositoryIdentity: lifecycleRepositoryIdentity(fixture.repository), Request: request,
	}); err != nil {
		t.Fatalf("Restic lifecycle target identity: %v", err)
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

// newCommittedRcloneLifecycleDeleteIdentityFixture uses the normal persisted
// fixture but supplies provider-valid marker-key child identities for the
// resolved prefix request; the broader publication fixture intentionally uses
// placeholder child identities because its tests do not execute deletion.
func newCommittedRcloneLifecycleDeleteIdentityFixture(t *testing.T) (*rclonePublicationFixture, *Service, model.RecoveryPoint) {
	t.Helper()
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
	markerKey, err := fixture.service.rcloneMarkerKey(context.Background(), fixture.repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	attemptRoot := managedRclonePortableAttemptRoot(fixture.binding, attempt)
	commit.Portable.ControlIdentityDigest = lifecycleDeleteKeyedPrivateLocatorDigest(markerKey, strings.TrimSuffix(attemptRoot, "/")+"/control")
	commit.Portable.DataIdentityDigest = lifecycleDeleteKeyedPrivateLocatorDigest(markerKey, strings.TrimSuffix(attemptRoot, "/")+"/data")
	if _, err := execution.RecordProviderCommit(context.Background(), provider.NewRcloneProviderCommit(commit)); err != nil {
		t.Fatal(err)
	}
	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", attempt.RecoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	return fixture, newLifecycleDeleteService(t, fixture.service, fixture.now), point
}

func lifecycleDeleteKeyedPrivateLocatorDigest(key []byte, locator string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("xirang-rclone-private-locator-v1\n"))
	_, _ = mac.Write([]byte(locator))
	return hex.EncodeToString(mac.Sum(nil))
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
func TestResolveLifecycleDeletePointResticAuthorityVectors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *gorm.DB, uint, model.SSHKey, model.SSHKey)
	}{
		{
			name: "host",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"host": "changed-restic-host.example.invalid"})
			},
		},
		{
			name: "port",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"port": 2201})
			},
		},
		{
			name: "username",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"username": "changed-restic-user"})
			},
		},
		{
			name: "auth_type",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"auth_type": "password"})
			},
		},
		{
			name: "password",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"password": "FAKE_CHANGED_RESTIC_NODE_PASSWORD_FOR_TEST_ONLY"})
			},
		},
		{
			name: "private_key",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"private_key": "FAKE_CHANGED_RESTIC_NODE_PRIVATE_KEY_FOR_TEST_ONLY"})
			},
		},
		{
			name: "ssh_key_lineage",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, alternate model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"ssh_key_id": alternate.ID})
			},
		},
		{
			name: "base_path",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"base_path": "/changed/restic/base"})
			},
		},
		{
			name: "backup_dir",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"backup_dir": "changed-restic-backup-dir"})
			},
		},
		{
			name: "sudo",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"use_sudo": false})
			},
		},
		{
			name: "tags",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"tags": "prod,changed-restic-tag"})
			},
		},
		{
			name: "ssh_key_username",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"username": "changed-key-user"})
			},
		},
		{
			name: "ssh_key_type",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"key_type": "rsa"})
			},
		},
		{
			name: "ssh_key_private_key",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"private_key": "FAKE_CHANGED_RESTIC_SSH_PRIVATE_KEY_FOR_TEST_ONLY"})
			},
		},
		{
			name: "ssh_key_fingerprint",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"fingerprint": "SHA256:changed-restic-key"})
			},
		},
		{
			name: "ssh_key_disabled",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"disabled": true})
			},
		},
		{
			name: "ssh_key_expiry",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"expires_at": time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)})
			},
		},
		{
			name: "ssh_key_allowed_purposes",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"allowed_purposes": "retention,probe"})
			},
		},
		{
			name: "ssh_key_allowed_node_ids",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"allowed_node_ids": "999999"})
			},
		},
		{
			name: "ssh_key_allowed_node_tags",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"allowed_node_tags": "prod,changed-key-tag"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, service, point := newResticLifecycleDeleteFixture(t)
			key, alternate := configureLifecycleDeleteRemoteAuthority(t, fixture.db, fixture.node.ID, fixture.now)
			baseRequest := resolveResticLifecycleDeleteIdentityRequest(t, fixture, service, point)
			baseInput := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, baseRequest)
			test.mutate(t, fixture.db, fixture.node.ID, key, alternate)
			mutatedRequest := resolveResticLifecycleDeleteIdentityRequest(t, fixture, service, point)
			mutatedInput := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, mutatedRequest)
			assertLifecycleDeleteAuthorityMismatchBeforeProvider(t, baseInput, mutatedInput, mutatedRequest)
		})
	}
}

func TestResolveLifecycleDeletePointRclonePrefixAuthorityVectors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *gorm.DB, uint, model.SSHKey, model.SSHKey)
	}{
		{
			name: "host",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"host": "changed-rclone-host.example.invalid"})
			},
		},
		{
			name: "port",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"port": 2202})
			},
		},
		{
			name: "username",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"username": "changed-rclone-user"})
			},
		},
		{
			name: "auth_type",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"auth_type": "password"})
			},
		},
		{
			name: "password",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"password": "FAKE_CHANGED_RCLONE_NODE_PASSWORD_FOR_TEST_ONLY"})
			},
		},
		{
			name: "private_key",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"private_key": "FAKE_CHANGED_RCLONE_NODE_PRIVATE_KEY_FOR_TEST_ONLY"})
			},
		},
		{
			name: "ssh_key_lineage",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, alternate model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"ssh_key_id": alternate.ID})
			},
		},
		{
			name: "base_path",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"base_path": "/changed/rclone/base"})
			},
		},
		{
			name: "backup_dir",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"backup_dir": "changed-rclone-backup-dir"})
			},
		},
		{
			name: "sudo",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"use_sudo": false})
			},
		},
		{
			name: "tags",
			mutate: func(t *testing.T, db *gorm.DB, nodeID uint, _ model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteNode(t, db, nodeID, map[string]any{"tags": "prod,changed-rclone-tag"})
			},
		},
		{
			name: "ssh_key_username",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"username": "changed-key-user"})
			},
		},
		{
			name: "ssh_key_type",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"key_type": "rsa"})
			},
		},
		{
			name: "ssh_key_private_key",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"private_key": "FAKE_CHANGED_RCLONE_SSH_PRIVATE_KEY_FOR_TEST_ONLY"})
			},
		},
		{
			name: "ssh_key_fingerprint",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"fingerprint": "SHA256:changed-rclone-key"})
			},
		},
		{
			name: "ssh_key_disabled",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"disabled": true})
			},
		},
		{
			name: "ssh_key_expiry",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"expires_at": time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)})
			},
		},
		{
			name: "ssh_key_allowed_purposes",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"allowed_purposes": "retention,probe"})
			},
		},
		{
			name: "ssh_key_allowed_node_ids",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"allowed_node_ids": "999999"})
			},
		},
		{
			name: "ssh_key_allowed_node_tags",
			mutate: func(t *testing.T, db *gorm.DB, _ uint, key model.SSHKey, _ model.SSHKey) {
				mutateLifecycleDeleteSSHKey(t, db, key.ID, map[string]any{"allowed_node_tags": "prod,changed-key-tag"})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, service, point := newCommittedRcloneLifecycleDeleteIdentityFixture(t)
			key, alternate := configureLifecycleDeleteRemoteAuthority(t, fixture.db, fixture.task.NodeID, fixture.now)
			baseRequest := resolveRclonePrefixLifecycleDeleteIdentityRequest(t, fixture, service, point)
			baseInput := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, baseRequest)
			test.mutate(t, fixture.db, fixture.task.NodeID, key, alternate)
			mutatedRequest := resolveRclonePrefixLifecycleDeleteIdentityRequest(t, fixture, service, point)
			mutatedInput := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, mutatedRequest)
			assertLifecycleDeleteAuthorityMismatchBeforeProvider(t, baseInput, mutatedInput, mutatedRequest)
		})
	}
}

func TestResolveLifecycleDeletePointResolvedAuthorityProjectionRedaction(t *testing.T) {
	t.Run("restic", func(t *testing.T) {
		fixture, service, point := newResticLifecycleDeleteFixture(t)
		configureLifecycleDeleteRemoteAuthority(t, fixture.db, fixture.node.ID, fixture.now)
		request := resolveResticLifecycleDeleteIdentityRequest(t, fixture, service, point)
		assertResolvedLifecycleDeleteIdentityIsPersistenceSafe(t, lifecycleDeleteIdentityInputForRequest(point, fixture.repository, request))
	})
	t.Run("rclone-prefix", func(t *testing.T) {
		fixture, service, point := newCommittedRcloneLifecycleDeleteIdentityFixture(t)
		configureLifecycleDeleteRemoteAuthority(t, fixture.db, fixture.task.NodeID, fixture.now)
		request := resolveRclonePrefixLifecycleDeleteIdentityRequest(t, fixture, service, point)
		assertResolvedLifecycleDeleteIdentityIsPersistenceSafe(t, lifecycleDeleteIdentityInputForRequest(point, fixture.repository, request))
	})
}

func TestResolveLifecycleDeletePointResolvedOpaqueRuntimeChangesRemainEqual(t *testing.T) {
	t.Run("restic", func(t *testing.T) {
		fixture, service, point := newResticLifecycleDeleteFixture(t)
		configureLifecycleDeleteRemoteAuthority(t, fixture.db, fixture.node.ID, fixture.now)
		request := resolveResticLifecycleDeleteIdentityRequest(t, fixture, service, point)
		base := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, request)
		mutatedRequest := lifecycleDeleteRequestWithOpaqueRuntimeChanges(request)
		mutated := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, mutatedRequest)
		if err := provider.CompareDeletionTargetAuthority(base, mutated); err != nil {
			t.Fatalf("Restic opaque runtime/telemetry changes changed identity: %v", err)
		}
	})
	t.Run("rclone-prefix", func(t *testing.T) {
		fixture, service, point := newCommittedRcloneLifecycleDeleteIdentityFixture(t)
		configureLifecycleDeleteRemoteAuthority(t, fixture.db, fixture.task.NodeID, fixture.now)
		request := resolveRclonePrefixLifecycleDeleteIdentityRequest(t, fixture, service, point)
		base := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, request)
		mutatedRequest := lifecycleDeleteRequestWithOpaqueRuntimeChanges(request)
		mutated := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, mutatedRequest)
		if err := provider.CompareDeletionTargetAuthority(base, mutated); err != nil {
			t.Fatalf("Rclone prefix opaque runtime/telemetry changes changed identity: %v", err)
		}
	})
}

func TestResolveLifecycleDeletePointResolvedProviderAuthorityVectors(t *testing.T) {
	t.Run("restic", func(t *testing.T) {
		fixture, service, point := newResticLifecycleDeleteFixture(t)
		configureLifecycleDeleteRemoteAuthority(t, fixture.db, fixture.node.ID, fixture.now)
		request := resolveResticLifecycleDeleteIdentityRequest(t, fixture, service, point)
		base := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, request)
		for _, test := range []struct {
			name   string
			mutate func(*provider.DeletePointRequest)
		}{
			{name: "point native", mutate: func(request *provider.DeletePointRequest) {
				request.Point.Native = strings.Repeat("d", 64)
			}},
			{name: "access locator", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.Locator = "changed-restic-locator"
			}},
			{name: "access config", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.Config = []byte("changed-restic-config")
			}},
			{name: "access secret", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.Secret = []byte("changed-restic-secret")
			}},
			{name: "identity salt", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.IdentitySalt = []byte(strings.Repeat("C", provider.IdentitySaltBytes))
			}},
			{name: "endpoint fact", mutate: func(request *provider.DeletePointRequest) {
				facts := append([]string(nil), request.Snapshot.Access.EndpointFacts...)
				facts[0] = "changed-restic-endpoint-fact"
				request.Snapshot.Access.EndpointFacts = facts
			}},
			{name: "access task id", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.TaskID++
			}},
			{name: "access node id", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.NodeID++
			}},
			{name: "native repository authority", mutate: func(request *provider.DeletePointRequest) {
				access := request.Snapshot.Access.AdapterData.(provider.ResticRuntimeAccess)
				access.NativeRepositoryID = strings.Repeat("d", 64)
				request.Snapshot.Access.AdapterData = access
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				mutatedRequest := cloneLifecycleDeleteRequest(request)
				test.mutate(&mutatedRequest)
				mutated := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, mutatedRequest)
				assertLifecycleDeleteAuthorityMismatchBeforeProvider(t, base, mutated, mutatedRequest)
			})
		}
	})
	t.Run("rclone-prefix", func(t *testing.T) {
		fixture, service, point := newCommittedRcloneLifecycleDeleteIdentityFixture(t)
		configureLifecycleDeleteRemoteAuthority(t, fixture.db, fixture.task.NodeID, fixture.now)
		request := resolveRclonePrefixLifecycleDeleteIdentityRequest(t, fixture, service, point)
		base := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, request)
		for _, test := range []struct {
			name   string
			mutate func(*provider.DeletePointRequest)
		}{
			{name: "point native", mutate: func(request *provider.DeletePointRequest) {
				request.Point.Native = "backup:managed/v1/points/changed"
			}},
			{name: "access locator", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.Locator = "changed-rclone-locator"
			}},
			{name: "access config", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.Config = []byte("changed-rclone-config")
			}},
			{name: "access secret", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.Secret = []byte("changed-rclone-secret")
			}},
			{name: "identity salt", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.IdentitySalt = []byte(strings.Repeat("D", provider.IdentitySaltBytes))
			}},
			{name: "endpoint fact", mutate: func(request *provider.DeletePointRequest) {
				facts := append([]string(nil), request.Snapshot.Access.EndpointFacts...)
				facts[0] = "changed-rclone-endpoint-fact"
				request.Snapshot.Access.EndpointFacts = facts
			}},
			{name: "access task id", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.TaskID++
			}},
			{name: "access node id", mutate: func(request *provider.DeletePointRequest) {
				request.Snapshot.Access.NodeID++
			}},
			{name: "prefix", mutate: func(request *provider.DeletePointRequest) {
				access := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
				prefix, err := provider.NewRclonePrivateLocator("backup:managed/v1/points/" + strings.Repeat("d", 32) + "." + strings.Repeat("e", 32))
				if err != nil {
					panic(err)
				}
				access.Prefix = prefix
				request.Snapshot.Access.AdapterData = access
			}},
			{name: "marker digest", mutate: func(request *provider.DeletePointRequest) {
				access := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
				access.MarkerDigest = strings.Repeat("d", 64)
				request.Snapshot.Access.AdapterData = access
			}},
			{name: "expected backend", mutate: func(request *provider.DeletePointRequest) {
				access := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
				access.ExpectedBackend = "changed-backend"
				request.Snapshot.Access.AdapterData = access
			}},
			{name: "expected root identity", mutate: func(request *provider.DeletePointRequest) {
				access := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
				access.ExpectedRootIdentity = strings.Repeat("d", 64)
				request.Snapshot.Access.AdapterData = access
			}},
			{name: "config digest", mutate: func(request *provider.DeletePointRequest) {
				access := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
				access.ConfigDigest = strings.Repeat("d", 64)
				request.Snapshot.Access.AdapterData = access
			}},
			{name: "marker key", mutate: func(request *provider.DeletePointRequest) {
				access := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
				access.MarkerKey = []byte(strings.Repeat("x", len(access.MarkerKey)))
				request.Snapshot.Access.AdapterData = access
			}},
			{name: "attempt root", mutate: func(request *provider.DeletePointRequest) {
				access := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
				access.ExpectedAttemptRoot = "changed-attempt-root"
				request.Snapshot.Access.AdapterData = access
			}},
			{name: "attempt authority", mutate: func(request *provider.DeletePointRequest) {
				access := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
				attempt := access.Attempt
				attempt.ConfigDigest = strings.Repeat("d", 64)
				access.Attempt = attempt
				request.Snapshot.Access.AdapterData = access
			}},
			{name: "commit authority", mutate: func(request *provider.DeletePointRequest) {
				access := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess)
				if access.Commit.Portable == nil {
					t.Fatal("resolved Rclone prefix commit has no portable authority")
				}
				portable := *access.Commit.Portable
				portable.CommitPayloadDigest = strings.Repeat("d", 64)
				access.Commit.Portable = &portable
				request.Snapshot.Access.AdapterData = access
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				mutatedRequest := cloneLifecycleDeleteRequest(request)
				test.mutate(&mutatedRequest)
				mutated := lifecycleDeleteIdentityInputForRequest(point, fixture.repository, mutatedRequest)
				assertLifecycleDeleteAuthorityMismatchBeforeProvider(t, base, mutated, mutatedRequest)
			})
		}
	})
}

func configureLifecycleDeleteRemoteAuthority(t *testing.T, db *gorm.DB, nodeID uint, now time.Time) (model.SSHKey, model.SSHKey) {
	t.Helper()
	key := model.SSHKey{
		Name: "lifecycle-delete-identity-key", Username: "resolved-key-user", KeyType: "ed25519",
		PrivateKey: "FAKE_RESOLVED_SSH_PRIVATE_KEY_FOR_TEST_ONLY", Fingerprint: "SHA256:resolved-lifecycle-key",
		AllowedPurposes: "retention", AllowedNodeIDs: fmt.Sprintf("%d", nodeID), AllowedNodeTags: "prod,archive",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&key).Error; err != nil {
		t.Fatalf("create lifecycle identity SSH key: %v", err)
	}
	alternate := model.SSHKey{
		Name: "lifecycle-delete-identity-key-alternate", Username: key.Username, KeyType: key.KeyType,
		PrivateKey: key.PrivateKey, Fingerprint: key.Fingerprint, AllowedPurposes: key.AllowedPurposes,
		AllowedNodeIDs: key.AllowedNodeIDs, AllowedNodeTags: key.AllowedNodeTags, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&alternate).Error; err != nil {
		t.Fatalf("create alternate lifecycle identity SSH key: %v", err)
	}
	if err := db.Model(&model.Node{}).Where("id = ?", nodeID).Updates(map[string]any{
		"host": "resolved-authority.example.invalid", "port": 2200, "username": "resolved-authority-user",
		"auth_type": "key", "password": "FAKE_RESOLVED_NODE_PASSWORD_FOR_TEST_ONLY",
		"private_key": "FAKE_RESOLVED_NODE_PRIVATE_KEY_FOR_TEST_ONLY", "ssh_key_id": key.ID,
		"base_path": "/resolved/authority/base", "backup_dir": "resolved-authority-backup-dir",
		"use_sudo": true, "tags": "prod,archive",
	}).Error; err != nil {
		t.Fatalf("configure lifecycle identity Node: %v", err)
	}
	return key, alternate
}

func mutateLifecycleDeleteNode(t *testing.T, db *gorm.DB, nodeID uint, values map[string]any) {
	t.Helper()
	if err := db.Model(&model.Node{}).Where("id = ?", nodeID).Updates(values).Error; err != nil {
		t.Fatalf("mutate lifecycle identity Node: %v", err)
	}
}

func mutateLifecycleDeleteSSHKey(t *testing.T, db *gorm.DB, keyID uint, values map[string]any) {
	t.Helper()
	if err := db.Model(&model.SSHKey{}).Where("id = ?", keyID).Updates(values).Error; err != nil {
		t.Fatalf("mutate lifecycle identity SSH key: %v", err)
	}
}

func resolveResticLifecycleDeleteIdentityRequest(t *testing.T, fixture *publicationFixture, service *Service, point model.RecoveryPoint) provider.DeletePointRequest {
	t.Helper()
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("resolve Restic lifecycle delete identity request: %v", err)
	}
	if _, ok := request.Snapshot.Access.AdapterData.(provider.ResticRuntimeAccess); !ok {
		t.Fatalf("resolved Restic lifecycle access=%T", request.Snapshot.Access.AdapterData)
	}
	return request
}

func resolveRclonePrefixLifecycleDeleteIdentityRequest(t *testing.T, fixture *rclonePublicationFixture, service *Service, point model.RecoveryPoint) provider.DeletePointRequest {
	t.Helper()
	request, err := service.ResolveLifecycleDeletePoint(context.Background(), strings.Repeat("e", 32), point, fixture.repository)
	if err != nil {
		t.Fatalf("resolve Rclone prefix lifecycle delete identity request: %v", err)
	}
	if _, ok := request.Snapshot.Access.AdapterData.(provider.RclonePrefixDeletionAccess); !ok {
		t.Fatalf("resolved Rclone prefix lifecycle access=%T", request.Snapshot.Access.AdapterData)
	}
	return request
}

func lifecycleDeleteIdentityInputForRequest(point model.RecoveryPoint, repository model.BackupRepository, request provider.DeletePointRequest) provider.DeletionTargetIdentityInput {
	return provider.DeletionTargetIdentityInput{
		RecoveryPointID: point.ID, AttemptID: request.OperationID, Operation: backupasset.LifecycleRetentionExpire,
		RepositoryIdentity: lifecycleRepositoryIdentity(repository), Request: request,
	}
}

type lifecycleDeleteIdentityProviderSpy struct {
	calls int
	kind  backupasset.ProviderKind
}

func (spy *lifecycleDeleteIdentityProviderSpy) ProviderKind() backupasset.ProviderKind {
	return spy.kind
}

func (spy *lifecycleDeleteIdentityProviderSpy) DeletePoint(context.Context, provider.DeletePointRequest) (provider.DeletePointResult, error) {
	spy.calls++
	return provider.DeletePointResult{Outcome: provider.DeletePointAlreadyAbsent, ReceiptDigest: strings.Repeat("a", 64)}, nil
}

func assertLifecycleDeleteAuthorityMismatchBeforeProvider(t *testing.T, base, mutated provider.DeletionTargetIdentityInput, request provider.DeletePointRequest) {
	t.Helper()
	spy := &lifecycleDeleteIdentityProviderSpy{kind: request.Snapshot.Access.Provider}
	err := provider.CompareDeletionTargetAuthority(base, mutated)
	if err == nil {
		_, _ = provider.ExecuteDeletePoint(context.Background(), spy, request)
	}
	if spy.calls != 0 {
		t.Fatalf("material lifecycle authority reached provider before rejection: calls=%d", spy.calls)
	}
	if !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
		t.Fatalf("material lifecycle authority returned unexpected error: %v", err)
	}
}

func assertResolvedLifecycleDeleteIdentityIsPersistenceSafe(t *testing.T, input provider.DeletionTargetIdentityInput) {
	t.Helper()
	projection, err := provider.CanonicalDeletionTargetProjection(input)
	if err != nil {
		t.Fatalf("canonicalize resolved lifecycle delete identity: %v", err)
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("marshal resolved lifecycle delete projection: %v", err)
	}
	digest, err := provider.DeletionTargetIdentityDigest(input)
	if err != nil {
		t.Fatalf("digest resolved lifecycle delete identity: %v", err)
	}
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		t.Fatalf("resolved lifecycle delete digest=%q, want lowercase SHA-256", digest)
	}
	access := input.Request.Snapshot.Access
	forbidden := []string{
		input.Request.Point.Native, input.Request.Snapshot.Access.Locator,
		string(access.Secret), string(access.Config), string(access.IdentitySalt),
		fmt.Sprintf("%x", access.IdentitySalt),
	}
	switch runtime := access.AdapterData.(type) {
	case provider.ResticRuntimeAccess:
		if runtime.Command != nil {
			forbidden = append(forbidden, runtime.Command.Node.Password, runtime.Command.Node.PrivateKey)
			if runtime.Command.Node.SSHKey != nil {
				forbidden = append(forbidden, runtime.Command.Node.SSHKey.PrivateKey)
			}
		}
	case provider.RclonePrefixDeletionAccess:
		if runtime.Command != nil {
			forbidden = append(forbidden, runtime.Command.Node.Password, runtime.Command.Node.PrivateKey)
			if runtime.Command.Node.SSHKey != nil {
				forbidden = append(forbidden, runtime.Command.Node.SSHKey.PrivateKey)
			}
		}
	}
	for _, value := range forbidden {
		if value != "" && (strings.Contains(string(encoded), value) || strings.Contains(digest, value)) {
			t.Fatalf("resolved lifecycle delete persistence output exposed private material %q: projection=%s digest=%s", value, encoded, digest)
		}
	}
}

func cloneLifecycleDeleteRequest(request provider.DeletePointRequest) provider.DeletePointRequest {
	cloned := request
	access := request.Snapshot.Access
	access.IdentitySalt = append([]byte(nil), access.IdentitySalt...)
	access.EndpointFacts = append([]string(nil), access.EndpointFacts...)
	access.Secret = append([]byte(nil), access.Secret...)
	access.Config = append([]byte(nil), access.Config...)
	switch runtime := access.AdapterData.(type) {
	case provider.ResticRuntimeAccess:
		clonedRuntime := runtime
		if runtime.Command != nil {
			command := *runtime.Command
			command.Node = cloneLifecycleDeleteNode(runtime.Command.Node)
			clonedRuntime.Command = &command
		}
		access.AdapterData = clonedRuntime
	case provider.RclonePrefixDeletionAccess:
		clonedRuntime := runtime
		clonedRuntime.MarkerKey = append([]byte(nil), runtime.MarkerKey...)
		if runtime.Command != nil {
			command := *runtime.Command
			command.Node = cloneLifecycleDeleteNode(runtime.Command.Node)
			clonedRuntime.Command = &command
		}
		access.AdapterData = clonedRuntime
	}
	cloned.Snapshot.Access = access
	return cloned
}

func cloneLifecycleDeleteNode(node model.Node) model.Node {
	if node.SSHKey != nil {
		key := *node.SSHKey
		node.SSHKey = &key
	}
	return node
}

func lifecycleDeleteRequestWithOpaqueRuntimeChanges(request provider.DeletePointRequest) provider.DeletePointRequest {
	mutated := cloneLifecycleDeleteRequest(request)
	access := mutated.Snapshot.Access
	var command *provider.RemoteCommandAccess
	switch runtime := access.AdapterData.(type) {
	case provider.ResticRuntimeAccess:
		command = runtime.Command
	case provider.RclonePrefixDeletionAccess:
		command = runtime.Command
	default:
		return mutated
	}
	if command == nil {
		return mutated
	}
	commandCopy := *command
	node := commandCopy.Node
	node.Name = "opaque-renamed-node"
	node.Status = "maintenance"
	node.ConnectionLatency = 99
	node.DiskUsedGB = 11
	node.DiskTotalGB = 22
	node.ConsecutiveFailures = 3
	node.LastSeenAt = timePointer(time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	node.LastBackupAt = timePointer(time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC))
	node.LastProbeAt = timePointer(time.Date(2026, 8, 18, 12, 2, 0, 0, time.UTC))
	node.MaintenanceStart = timePointer(time.Date(2026, 8, 18, 12, 3, 0, 0, time.UTC))
	node.MaintenanceEnd = timePointer(time.Date(2026, 8, 18, 12, 4, 0, 0, time.UTC))
	node.ExpiryDate = timePointer(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
	node.Archived = true
	node.LogPaths = `["/opaque/log"]`
	node.LogJournalctlEnabled = false
	node.LogRetentionDays = 2
	node.UpdatedAt = time.Date(2026, 8, 18, 12, 5, 0, 0, time.UTC)
	if node.SSHKey != nil {
		key := *node.SSHKey
		key.Name = "opaque-renamed-key"
		key.LastUsedAt = timePointer(time.Date(2026, 8, 18, 12, 6, 0, 0, time.UTC))
		key.UpdatedAt = time.Date(2026, 8, 18, 12, 7, 0, 0, time.UTC)
		node.SSHKey = &key
	}
	commandCopy.Node = node
	commandCopy.Audit.CorrelationID = "opaque-audit-correlation"
	commandCopy.Audit.UserID = 9001
	commandCopy.Audit.Username = "opaque-audit-user"
	commandCopy.Audit.Role = "opaque-audit-role"
	taskID := uint(9002)
	commandCopy.Audit.TaskID = &taskID
	switch runtime := access.AdapterData.(type) {
	case provider.ResticRuntimeAccess:
		runtime.Command = &commandCopy
		access.AdapterData = runtime
	case provider.RclonePrefixDeletionAccess:
		runtime.Command = &commandCopy
		access.AdapterData = runtime
	}
	mutated.Snapshot.Access = access
	return mutated
}
