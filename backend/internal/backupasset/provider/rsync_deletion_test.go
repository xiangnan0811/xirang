package provider

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestRsyncExactPointDeletionDeletesCommittedManagedComponent(t *testing.T) {
	fixture := newRsyncDeletionFixture(t)
	other := strings.Repeat("f", 32)
	if err := os.MkdirAll(filepath.Join(fixture.root, "points", other), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(t.TempDir(), "legacy-mutable-target")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "keep"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := fixture.deleter.DeletePoint(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("DeletePoint committed component: %v", err)
	}
	if result.Outcome != DeletePointDeleted || !lowerHex(result.ReceiptDigest, 64) {
		t.Fatalf("committed delete result=%+v", result)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "points", fixture.attempt.FinalComponent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed component still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "points", other)); err != nil {
		t.Fatalf("unrelated managed point was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "keep")); err != nil {
		t.Fatalf("legacy mutable target was deleted: %v", err)
	}
}

func TestRsyncExactPointDeletionRejectsMutableLegacyTarget(t *testing.T) {
	fixture := newRsyncDeletionFixture(t)
	legacy := filepath.Join(t.TempDir(), "legacy-mutable-target")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	request := fixture.request
	request.Point.Native = legacy
	if _, err := fixture.deleter.DeletePoint(context.Background(), request); !errors.Is(err, ErrInvalidDeletePointRequest) {
		t.Fatalf("legacy mutable delete error=%v, want invalid delete request", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy mutable target was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "points", fixture.attempt.FinalComponent)); err != nil {
		t.Fatalf("committed component was removed during rejected legacy delete: %v", err)
	}
}

func TestRsyncExactPointDeletionAlreadyAbsentIsIdempotent(t *testing.T) {
	fixture := newRsyncDeletionFixture(t)
	if _, err := fixture.deleter.DeletePoint(context.Background(), fixture.request); err != nil {
		t.Fatalf("first committed delete: %v", err)
	}
	result, err := fixture.deleter.DeletePoint(context.Background(), fixture.request)
	if err != nil || result.Outcome != DeletePointAlreadyAbsent || !lowerHex(result.ReceiptDigest, 64) {
		t.Fatalf("already-absent result=%+v err=%v", result, err)
	}
}

func TestRsyncExactPointDeletionRejectsUncommittedComponent(t *testing.T) {
	fixture := newRsyncDeletionFixture(t)
	request := fixture.request
	request.Point.Native = strings.Repeat("9", 32)
	if _, err := fixture.deleter.DeletePoint(context.Background(), request); !errors.Is(err, ErrInvalidDeletePointRequest) {
		t.Fatalf("uncommitted component error=%v, want invalid delete request", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "points", fixture.attempt.FinalComponent)); err != nil {
		t.Fatalf("owned committed component was deleted: %v", err)
	}
}

func TestRsyncExactPointDeletionRejectsSourceRevisionDrift(t *testing.T) {
	fixture := newRsyncDeletionFixture(t)
	request := fixture.request
	request.ExpectedSourceRevision = strings.Repeat("0", 64)
	if _, err := fixture.deleter.DeletePoint(context.Background(), request); !errors.Is(err, ErrDeletePointIdentityConflict) {
		t.Fatalf("rsync identity drift error=%v, want identity conflict", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, "points", fixture.attempt.FinalComponent)); err != nil {
		t.Fatalf("identity-conflict delete removed the committed component: %v", err)
	}
}

func TestRsyncExactPointDeletionHonorsCanceledContext(t *testing.T) {
	fixture := newRsyncDeletionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := fixture.deleter.DeletePoint(ctx, fixture.request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled DeletePoint error=%v result=%+v, want context.Canceled", err, result)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.root, "points", fixture.attempt.FinalComponent)); statErr != nil {
		t.Fatalf("canceled delete removed committed component: %v", statErr)
	}
}

func TestRsyncExactPointDeletionHidesManagedRootAndComponent(t *testing.T) {
	fixture := newRsyncDeletionFixture(t)
	payload, err := json.Marshal(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{fixture.root, fixture.attempt.FinalComponent, "FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("private rsync deletion value %q leaked in %s", forbidden, payload)
		}
	}
}

type rsyncDeletionFixture struct {
	root    string
	attempt RsyncTreeAttemptV1
	request DeletePointRequest
	deleter *RsyncPointDeleter
}

func newRsyncDeletionFixture(t *testing.T) *rsyncDeletionFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "repository.json"), []byte(`{"layout_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tree := newRsyncManagedTreeForTest(t, root)
	attempt := rsyncTreeAttemptForMarkerTest()
	key := []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES")
	if err := tree.CreateFreshStaging(attempt.StagingComponent); err != nil {
		t.Fatalf("CreateFreshStaging: %v", err)
	}
	attemptMarker, err := encodeRsyncTreeAttemptMarkerV1(attempt, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.WriteStagingMetadata(attempt.StagingComponent, "attempt.json", attemptMarker); err != nil {
		t.Fatal(err)
	}
	signed, commitMarker, err := encodeRsyncTreeCommitMarkerV1(rsyncTreeCommitForMarkerTest(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.WriteStagingMetadata(attempt.StagingComponent, "commit.json", commitMarker); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staging", attempt.StagingComponent, "payload"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tree.CommitStaging(attempt.StagingComponent, attempt.FinalComponent); err != nil {
		t.Fatalf("CommitStaging: %v", err)
	}
	_ = tree.Close()

	fixture := &rsyncDeletionFixture{root: root, attempt: attempt}
	deleter, err := NewRsyncPointDeleter(func() time.Time { return time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("NewRsyncPointDeleter: %v", err)
	}
	fixture.deleter = deleter
	access := RsyncPointDeletionAccess{
		ManagedRoot:        root,
		MarkerKey:          key,
		Attempt:            attempt,
		CommitMarkerDigest: signed.CommitMarkerDigest,
		SourceFingerprint:  signed.SourceFingerprint,
	}
	binding := AccessBinding{
		Provider: backupasset.ProviderRsync, RepositoryID: attempt.RepositoryID,
		Locator: root, AdapterData: access,
	}
	fixture.request = DeletePointRequest{
		Snapshot: ReadSnapshot{
			RepositoryID: attempt.RepositoryID, CapabilityRevision: 1,
			SourceRevision: signed.SourceFingerprint, Access: binding,
		},
		Point:                  PointLocator{Native: attempt.FinalComponent},
		ExpectedSourceRevision: signed.SourceFingerprint,
		OperationID:            strings.Repeat("e", 32),
	}
	return fixture
}
