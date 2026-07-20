package processing

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestDerivedReferencesAreSourceLocalAndSharedAcrossRecoveryPoints(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := bytes.Repeat([]byte("cross-rp-shared"), 6000)
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	first := harness.seedReference(t, blob.BlobID, "1", "a")
	second := harness.seedReference(t, blob.BlobID, "2", "b")

	var plaintext bytes.Buffer
	if err := harness.lifecycle.ReadAuthorized(context.Background(), first.authorization, &plaintext); err != nil {
		t.Fatalf("ReadAuthorized(first): %v", err)
	}
	if !bytes.Equal(plaintext.Bytes(), payload) {
		t.Fatal("authorized read differs")
	}
	wrong := first.authorization
	wrong.RecoveryPointID = second.authorization.RecoveryPointID
	if err := harness.lifecycle.ReadAuthorized(context.Background(), wrong, &bytes.Buffer{}); !errors.Is(err, ErrDerivedUnauthorized) {
		t.Fatalf("cross-source artifact access got %v", err)
	}

	if err := harness.lifecycle.RevokeSetFenced(context.Background(), first.setID, DerivedRevokeExpired,
		derivedLifecycleFence("1", first.authorization.RecoveryPointID)); err != nil {
		t.Fatalf("RevokeSet(first): %v", err)
	}
	var row model.BackupAssetDerivedBlob
	if err := harness.db.First(&row, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if row.RefCount != 1 || row.State != "active" || len(row.WrappedDEK) == 0 {
		t.Fatalf("first revoke destroyed shared blob: %+v", row)
	}
	plaintext.Reset()
	if err := harness.lifecycle.ReadAuthorized(context.Background(), second.authorization, &plaintext); err != nil {
		t.Fatalf("second reference lost after first revoke: %v", err)
	}
}

func TestDerivedReadRejectsProjectionRequiredSetUntilSearchPublicationCommits(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := []byte("not-visible-before-search-commit")
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "6", "8")
	if err := harness.db.Model(&model.BackupAssetDerivedArtifactSet{}).Where("id = ?", reference.setID).
		Updates(map[string]any{"projection_published": false, "projection_revision": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.lifecycle.ReadAuthorized(context.Background(), reference.authorization, &bytes.Buffer{}); !errors.Is(err, ErrDerivedUnauthorized) {
		t.Fatalf("unpublished Search projection read error=%v", err)
	}
}

func TestProjectionBearingRevokeRequiresFence(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := []byte("fence-required")
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "9", "a")
	if err := harness.lifecycle.RevokeSet(context.Background(), reference.setID, DerivedRevokeExpired); !errors.Is(err, ErrDerivedFenceRequired) {
		t.Fatalf("unfenced projection revoke error=%v", err)
	}
	var set model.BackupAssetDerivedArtifactSet
	if err := harness.db.First(&set, "id = ?", reference.setID).Error; err != nil {
		t.Fatal(err)
	}
	if set.State != "active" || harness.projection.revocations != 0 {
		t.Fatalf("unfenced revoke mutated state: set=%+v revocations=%d", set, harness.projection.revocations)
	}
}

func TestFencedPipelineInvalidationMarksSetStaleAndKeepsDerivedBytes(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := []byte("stale-fallback")
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "a", "b")
	fence := derivedLifecycleFence("a", reference.authorization.RecoveryPointID)
	harness.projection.onRevoke = func(_ *gorm.DB, request DerivedProjectionRevoke) error {
		if request.RecoveryPointFence != fence || request.Reason != DerivedRevokeRollback {
			return errors.New("invalid invalidation fence")
		}
		return nil
	}
	if err := harness.lifecycle.MarkSetStaleFenced(context.Background(), reference.setID, fence); err != nil {
		t.Fatalf("MarkSetStaleFenced: %v", err)
	}
	var set model.BackupAssetDerivedArtifactSet
	var persistedReference model.BackupAssetDerivedBlobReference
	var persistedBlob model.BackupAssetDerivedBlob
	if err := harness.db.First(&set, "id = ?", reference.setID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&persistedReference, "artifact_id = ?", reference.authorization.ArtifactID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&persistedBlob, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if set.State != "stale" || set.ProjectionPublished || persistedReference.State != "active" ||
		persistedBlob.State != "active" || persistedBlob.RefCount != 1 || len(persistedBlob.WrappedDEK) == 0 {
		t.Fatalf("pipeline stale product invalid: set=%+v reference=%+v blob=%+v", set, persistedReference, persistedBlob)
	}
}

func TestFencedPipelineInvalidationRollsBackWhenSearchRevokeFails(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := []byte("stale-rollback-fallback")
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "2", "9")
	harness.projection.onRevoke = func(*gorm.DB, DerivedProjectionRevoke) error {
		return errors.New("search revoke failed")
	}
	if err := harness.lifecycle.MarkSetStaleFenced(
		context.Background(), reference.setID, derivedLifecycleFence("2", reference.authorization.RecoveryPointID),
	); err == nil {
		t.Fatal("Search revoke failure did not abort pipeline invalidation")
	}
	var set model.BackupAssetDerivedArtifactSet
	if err := harness.db.First(&set, "id = ?", reference.setID).Error; err != nil {
		t.Fatal(err)
	}
	var plaintext bytes.Buffer
	if set.State != "active" || !set.ProjectionPublished || set.ProjectionRevision != 1 || harness.projection.revocations != 1 {
		t.Fatalf("failed stale transaction escaped rollback: set=%+v revocations=%d", set, harness.projection.revocations)
	}
	if err := harness.lifecycle.ReadAuthorized(context.Background(), reference.authorization, &plaintext); err != nil ||
		!bytes.Equal(plaintext.Bytes(), payload) {
		t.Fatalf("failed stale transaction destroyed fallback: bytes=%d err=%v", plaintext.Len(), err)
	}
}

func derivedLifecycleFence(seed, recoveryPointID string) backupasset.LeaseFence {
	return backupasset.LeaseFence{
		LeaseID: strings.Repeat(seed, 32), RecoveryPointID: recoveryPointID,
		HolderType: backupasset.LeaseHolderProcessingJob, OwnerID: strings.Repeat(seed, 31) + "1",
		AttemptID: strings.Repeat(seed, 31) + "2", FenceToken: strings.Repeat(seed, 64),
	}
}

func TestDerivedRevokeCommitsProjectionBeforeReferenceKeyAndBlobDestruction(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := bytes.Repeat([]byte("revoke-order"), 6000)
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "3", "c")
	harness.projection.onRevoke = func(tx *gorm.DB, request DerivedProjectionRevoke) error {
		var row model.BackupAssetDerivedBlobReference
		if err := tx.First(&row, "artifact_id = ?", reference.authorization.ArtifactID).Error; err != nil {
			return err
		}
		if row.State != "active" {
			return errors.New("reference changed before projection revoke")
		}
		return nil
	}
	removedAfterKeyErasure := false
	harness.store.removeFile = func(path string) error {
		var row model.BackupAssetDerivedBlob
		if err := harness.db.First(&row, "id = ?", blob.BlobID).Error; err != nil {
			return err
		}
		removedAfterKeyErasure = row.State == "unavailable" && len(row.WrappedDEK) == 0 && row.RefCount == 0
		return os.Remove(path)
	}
	if err := harness.lifecycle.RevokeSetFenced(context.Background(), reference.setID, DerivedRevokeExpired,
		derivedLifecycleFence("3", reference.authorization.RecoveryPointID)); err != nil {
		t.Fatalf("RevokeSet: %v", err)
	}
	if harness.projection.revocations != 1 || !removedAfterKeyErasure {
		t.Fatalf("revoke order invalid: projection=%d key-before-file=%t", harness.projection.revocations, removedAfterKeyErasure)
	}
	if _, err := os.Stat(filepath.Join(harness.root, blob.OpaqueLocator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ciphertext remains after last-reference revoke: %v", err)
	}
	if err := harness.lifecycle.ReadAuthorized(context.Background(), reference.authorization, &bytes.Buffer{}); !errors.Is(err, ErrDerivedUnauthorized) {
		t.Fatalf("revoked reference remained readable: %v", err)
	}
}

func TestDerivedRevokeStopsBeforeMutationWhenProjectionRevokeFails(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := []byte("projection-failure")
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "4", "d")
	harness.projection.onRevoke = func(*gorm.DB, DerivedProjectionRevoke) error { return errors.New("search unavailable") }
	if err := harness.lifecycle.RevokeSetFenced(context.Background(), reference.setID, DerivedRevokeExpired,
		derivedLifecycleFence("4", reference.authorization.RecoveryPointID)); err == nil {
		t.Fatal("projection failure did not stop revoke")
	}
	var row model.BackupAssetDerivedBlob
	if err := harness.db.First(&row, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "active" || row.RefCount != 1 || len(row.WrappedDEK) == 0 {
		t.Fatalf("projection failure mutated blob: %+v", row)
	}
	if err := harness.lifecycle.ReadAuthorized(context.Background(), reference.authorization, &bytes.Buffer{}); err != nil {
		t.Fatalf("projection failure destroyed readable derivative: %v", err)
	}
}

func TestDerivedKeyLossRevokesProjectionBeforeMarkingKeyLost(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := bytes.Repeat([]byte("derived-key-loss"), 4000)
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "7", "e")
	active, err := harness.keyring.Active(context.Background(), backupasset.KeyDomainDerivedStore)
	if err != nil {
		t.Fatal(err)
	}
	observedKeyAndReferenceActive := false
	harness.projection.onRevoke = func(tx *gorm.DB, _ DerivedProjectionRevoke) error {
		var key model.WrappedDomainKey
		if err := tx.First(&key, "domain = ? AND version = ?", backupasset.KeyDomainDerivedStore, active.Version).Error; err != nil {
			return err
		}
		var persisted model.BackupAssetDerivedBlobReference
		if err := tx.First(&persisted, "artifact_id = ?", reference.authorization.ArtifactID).Error; err != nil {
			return err
		}
		observedKeyAndReferenceActive = key.State == string(backupasset.DomainKeyActive) && persisted.State == "active"
		return nil
	}

	if err := harness.lifecycle.MarkActiveKeyLost(context.Background(), active.Version, 32); err != nil {
		t.Fatalf("MarkActiveKeyLost: %v", err)
	}
	if !observedKeyAndReferenceActive || harness.projection.revocations != 1 {
		t.Fatalf("key-loss revoke order invalid: observed=%t revocations=%d", observedKeyAndReferenceActive, harness.projection.revocations)
	}
	var key model.WrappedDomainKey
	var set model.BackupAssetDerivedArtifactSet
	var persistedReference model.BackupAssetDerivedBlobReference
	var persistedBlob model.BackupAssetDerivedBlob
	if err := harness.db.First(&key, "domain = ? AND version = ?", backupasset.KeyDomainDerivedStore, active.Version).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&set, "id = ?", reference.setID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&persistedReference, "artifact_id = ?", reference.authorization.ArtifactID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&persistedBlob, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if key.State != string(backupasset.DomainKeyLost) || set.State != "unavailable" || set.RevocationReason != string(DerivedRevokeKeyLoss) || set.ProjectionPublished ||
		persistedReference.State != "unavailable" || persistedBlob.State != "unavailable" || len(persistedBlob.WrappedDEK) != 0 {
		t.Fatalf("key-loss product invalid: key=%+v set=%+v reference=%+v blob=%+v", key, set, persistedReference, persistedBlob)
	}
	if _, err := os.Stat(filepath.Join(harness.root, blob.OpaqueLocator)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("key-loss ciphertext remains: %v", err)
	}
}

func TestDerivedKeyLossStopsBeforeMutationWhenProjectionRevokeFails(t *testing.T) {
	harness := newDerivedLifecycleHarness(t)
	payload := bytes.Repeat([]byte("derived-key-loss-projection-failure"), 2000)
	blob, err := harness.store.PutBlob(context.Background(), derivedDeclaration(payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	reference := harness.seedReference(t, blob.BlobID, "8", "f")
	active, err := harness.keyring.Active(context.Background(), backupasset.KeyDomainDerivedStore)
	if err != nil {
		t.Fatal(err)
	}
	harness.projection.onRevoke = func(*gorm.DB, DerivedProjectionRevoke) error { return errors.New("search unavailable") }

	if err := harness.lifecycle.MarkActiveKeyLost(context.Background(), active.Version, 32); err == nil {
		t.Fatal("projection failure did not stop Derived key loss")
	}
	remaining, err := harness.keyring.Active(context.Background(), backupasset.KeyDomainDerivedStore)
	if err != nil || remaining.Version != active.Version {
		t.Fatalf("projection failure changed active Derived key: material=%+v err=%v", remaining, err)
	}
	var persistedReference model.BackupAssetDerivedBlobReference
	var persistedBlob model.BackupAssetDerivedBlob
	if err := harness.db.First(&persistedReference, "artifact_id = ?", reference.authorization.ArtifactID).Error; err != nil {
		t.Fatal(err)
	}
	if err := harness.db.First(&persistedBlob, "id = ?", blob.BlobID).Error; err != nil {
		t.Fatal(err)
	}
	if persistedReference.State != "active" || persistedBlob.State != "active" || len(persistedBlob.WrappedDEK) == 0 {
		t.Fatalf("projection failure mutated Derived state: reference=%+v blob=%+v", persistedReference, persistedBlob)
	}
	var plaintext bytes.Buffer
	if err := harness.lifecycle.ReadAuthorized(context.Background(), reference.authorization, &plaintext); err != nil || !bytes.Equal(plaintext.Bytes(), payload) {
		t.Fatalf("projection failure destroyed readable Derived artifact: bytes=%d err=%v", plaintext.Len(), err)
	}
}

type derivedReferenceFixture struct {
	setID         string
	authorization DerivedArtifactAuthorization
}

type derivedLifecycleHarness struct {
	*derivedStoreHarness
	lifecycle  *DerivedLifecycle
	projection *derivedProjectionFake
}

func newDerivedLifecycleHarness(t *testing.T) *derivedLifecycleHarness {
	t.Helper()
	storeHarness := newDerivedStoreHarness(t)
	if err := storeHarness.db.AutoMigrate(
		&model.BackupAssetDerivedArtifactSet{}, &model.BackupAssetDerivedArtifact{}, &model.BackupAssetDerivedBlobReference{},
	); err != nil {
		t.Fatal(err)
	}
	projection := &derivedProjectionFake{}
	lifecycle, err := NewDerivedLifecycle(storeHarness.db, storeHarness.store, projection, storeHarness.store.now, derivedFenceLeaseFake{})
	if err != nil {
		t.Fatal(err)
	}
	return &derivedLifecycleHarness{derivedStoreHarness: storeHarness, lifecycle: lifecycle, projection: projection}
}

func (harness *derivedLifecycleHarness) seedReference(t *testing.T, blobID, suffix, sourceSuffix string) derivedReferenceFixture {
	t.Helper()
	now := harness.store.utcNow()
	setID := strings.Repeat(suffix, 32)
	artifactID := strings.Repeat(sourceSuffix, 32)
	recoveryPointID := strings.Repeat(sourceSuffix, 31) + "1"
	catalogID := strings.Repeat(sourceSuffix, 31) + "2"
	entryID := strings.Repeat(sourceSuffix, 64)
	set := model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: strings.Repeat(suffix, 31) + "a", AttemptID: strings.Repeat(suffix, 31) + "b",
		WorkKey: strings.Repeat(sourceSuffix, 64), RecoveryPointID: recoveryPointID, CatalogGenerationID: catalogID,
		EntryID: entryID, SourceFingerprint: "source-" + sourceSuffix, SecurityPolicyRevision: "policy-v1",
		ManifestDigest: strings.Repeat(suffix, 64), State: "active", Completeness: "complete", ArtifactCount: 1,
		TotalPlaintextBytes: 1, ProjectionRequired: true, ProjectionPublished: true, ProjectionRevision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	artifact := model.BackupAssetDerivedArtifact{
		ID: artifactID, ArtifactSetID: setID, Ordinal: 0, Role: "content", MediaType: "text/plain",
		PlaintextSize: 1, PlaintextDigest: strings.Repeat(sourceSuffix, 64), Completeness: "complete",
		CoverageCanonical: []byte(`{"schema_version":1}`), BlobID: blobID, ExcerptRef: "", CreatedAt: now,
	}
	referenceID := strings.Repeat(sourceSuffix, 31) + "3"
	reference := model.BackupAssetDerivedBlobReference{
		ID: referenceID, BlobID: blobID, ArtifactID: artifactID, RecoveryPointID: recoveryPointID,
		CatalogGenerationID: catalogID, EntryID: entryID, SourceFingerprint: set.SourceFingerprint,
		State: "active", CreatedAt: now, UpdatedAt: now,
	}
	if err := harness.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&set).Error; err != nil {
			return err
		}
		if err := tx.Create(&artifact).Error; err != nil {
			return err
		}
		if err := tx.Create(&reference).Error; err != nil {
			return err
		}
		return tx.Model(&model.BackupAssetDerivedBlob{}).Where("id = ?", blobID).
			Update("ref_count", gorm.Expr("ref_count + 1")).Error
	}); err != nil {
		t.Fatal(err)
	}
	return derivedReferenceFixture{
		setID: setID,
		authorization: DerivedArtifactAuthorization{
			ArtifactID: artifactID, RecoveryPointID: recoveryPointID, CatalogGenerationID: catalogID,
			EntryID: entryID, SourceFingerprint: set.SourceFingerprint,
		},
	}
}

type derivedProjectionFake struct {
	revocations int
	onRevoke    func(*gorm.DB, DerivedProjectionRevoke) error
}

type derivedFenceLeaseFake struct{}

func (derivedFenceLeaseFake) Acquire(_ context.Context, request backupasset.AcquireLeaseRequest) (backupasset.Lease, error) {
	fence := backupasset.LeaseFence{
		LeaseID: strings.Repeat("d", 32), RecoveryPointID: request.RecoveryPointID,
		HolderType: request.HolderType, OwnerID: request.OwnerID,
		AttemptID: strings.Repeat("e", 32), FenceToken: strings.Repeat("f", 64),
	}
	return backupasset.Lease{ID: fence.LeaseID, RecoveryPointID: request.RecoveryPointID, HolderType: request.HolderType, OwnerID: request.OwnerID, Fence: fence}, nil
}

func (derivedFenceLeaseFake) Release(context.Context, backupasset.LeaseFence) error { return nil }

type derivedPreparedPublish struct {
	request DerivedProjectionPublish
}

type derivedPreparedRevoke struct {
	fake    *derivedProjectionFake
	request DerivedProjectionRevoke
}

func (*derivedProjectionFake) PreparePublish(_ context.Context, request DerivedProjectionPublish) (PreparedDerivedProjection, error) {
	return &derivedPreparedPublish{request: request}, nil
}

func (prepared *derivedPreparedPublish) PublishTx(context.Context, *gorm.DB) (DerivedProjectionPublication, error) {
	return DerivedProjectionPublication{ArtifactSetID: prepared.request.ArtifactSetID, Revision: 1}, nil
}

func (fake *derivedProjectionFake) PrepareRevoke(_ context.Context, request DerivedProjectionRevoke) (PreparedDerivedRevocation, error) {
	return &derivedPreparedRevoke{fake: fake, request: request}, nil
}

func (prepared *derivedPreparedRevoke) RevokeTx(_ context.Context, tx *gorm.DB) error {
	fake := prepared.fake
	fake.revocations++
	if fake.onRevoke != nil {
		return fake.onRevoke(tx, prepared.request)
	}
	return nil
}
