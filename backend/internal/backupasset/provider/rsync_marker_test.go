package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestRsyncTreeAttemptMarkerAuthenticatesExactAttempt(t *testing.T) {
	attempt := rsyncTreeAttemptForMarkerTest()
	key := []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES")
	encoded, err := encodeRsyncTreeAttemptMarkerV1(attempt, key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRsyncTreeAttemptMarkerV1(encoded, key)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != attempt {
		t.Fatalf("decoded attempt=%+v, want=%+v", decoded, attempt)
	}
	tampered := strings.Replace(string(encoded), `"publication_mode":"versioned_full_copy"`, `"publication_mode":"versioned_hardlink"`, 1)
	if _, err := decodeRsyncTreeAttemptMarkerV1([]byte(tampered), key); err == nil {
		t.Fatal("tampered attempt marker was accepted")
	}
}

func TestRsyncTreeCommitMarkerRoundTripsAndRejectsTampering(t *testing.T) {
	key := []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES")
	signed, encoded, err := encodeRsyncTreeCommitMarkerV1(rsyncTreeCommitForMarkerTest(), key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRsyncTreeCommitMarkerV1(encoded, key)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != signed {
		t.Fatalf("decoded commit=%+v, want=%+v", decoded, signed)
	}
	tampered := bytes.Replace(encoded, []byte(`"manifest_entry_count":1`), []byte(`"manifest_entry_count":2`), 1)
	if bytes.Equal(tampered, encoded) {
		t.Fatal("test fixture did not alter commit marker")
	}
	if _, err := decodeRsyncTreeCommitMarkerV1(tampered, key); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("tampered commit marker error=%v, want conflict", err)
	}
}

func TestRsyncTreePublicationStrategyPublishesFullCopyWithExactManifest(t *testing.T) {
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("versioned payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	attempt := rsyncTreeAttemptForMarkerTest()
	attempt.RepositoryMarkerDigest = markerDigest
	attempt.ManagedRootIdentityDigest = rootIdentity
	processRunner, err := newLocalRsyncTreeProcessRunner(os.Environ)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	strategy, err := NewRsyncTreePublicationStrategy(processRunner, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	input := RsyncTreePublicationInput{
		ManagedRoot:           root,
		Source:                RsyncTreeCommandSource{LocalPath: source},
		MarkerKey:             []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"),
		SourceFingerprint:     strings.Repeat("3", 64),
		ChildFenceDigest:      strings.Repeat("4", 64),
		ManifestLimits:        ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10},
		MaxCommandOutputBytes: 1 << 20,
	}
	prepared, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{
		Attempt:        NewRsyncTreePublicationAttempt(attempt),
		RsyncTreeInput: &input,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Execute(context.Background(), prepared, PublicationProgress{})
	if err != nil || result.Completion != backupasset.CompletionKnownExitZero || result.ExitCode != 0 || result.ProviderCommit == nil {
		t.Fatalf("strategy execution result=%+v err=%v", result, err)
	}
	commit, err := strategy.RecordCommit(context.Background(), prepared, result)
	if err != nil {
		t.Fatal(err)
	}
	typedCommit, err := commit.RsyncTreeCommit()
	if err != nil {
		t.Fatal(err)
	}
	if !typedCommit.RenameVerified || !typedCommit.DirectoryFsyncVerified || typedCommit.ProviderCommittedAt != now {
		t.Fatalf("typed commit=%+v", typedCommit)
	}
	if content, err := os.ReadFile(filepath.Join(root, "points", attempt.FinalComponent, "tree", "file")); err != nil || string(content) != "versioned payload" {
		t.Fatalf("final tree content=%q err=%v", content, err)
	}
	reconciled, err := strategy.Reconcile(context.Background(), PublicationReconcileRequest{
		Attempt: NewRsyncTreePublicationAttempt(attempt),
		RsyncTreeInput: &RsyncTreeReconcileInput{
			ManagedRoot: root, MarkerKey: input.MarkerKey, SourceFingerprint: input.SourceFingerprint,
			ChildFenceDigest: input.ChildFenceDigest, ManifestLimits: input.ManifestLimits,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reconcileFact, err := reconciled.RsyncTreeResult()
	if err != nil {
		t.Fatal(err)
	}
	if reconcileFact.State != RsyncTreeReconcileFinal || reconcileFact.Commit == nil || *reconcileFact.Commit != typedCommit ||
		reconcileFact.Manifest == nil || reconcileFact.Manifest.Digest != typedCommit.ManifestDigest || reconcileFact.Manifest.EntryCount != typedCommit.ManifestEntryCount {
		t.Fatalf("reconciled managed Rsync fact=%+v", reconcileFact)
	}
	manifest, err := strategy.VerifyOrBuildManifest(context.Background(), prepared, commit, input.ManifestLimits)
	if err != nil {
		t.Fatal(err)
	}
	if typedManifest, err := manifest.RsyncTreeManifest(); err != nil || typedManifest.Digest != typedCommit.ManifestDigest || typedManifest.EntryCount != 1 {
		t.Fatalf("manifest=%+v err=%v", typedManifest, err)
	}
	if err := os.WriteFile(filepath.Join(root, "points", attempt.FinalComponent, "commit.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := strategy.VerifyOrBuildManifest(context.Background(), prepared, commit, input.ManifestLimits); err == nil {
		t.Fatal("verification accepted a replaced final commit marker")
	}
}

func TestRsyncTreePublicationStrategyReconcileClassifiesExactOwnedStaging(t *testing.T) {
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	attempt := rsyncTreeAttemptForMarkerTest()
	attempt.RepositoryMarkerDigest = markerDigest
	attempt.ManagedRootIdentityDigest = rootIdentity
	input := RsyncTreePublicationInput{
		ManagedRoot: root, Source: RsyncTreeCommandSource{LocalPath: source}, MarkerKey: []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"),
		SourceFingerprint: strings.Repeat("3", 64), ChildFenceDigest: strings.Repeat("4", 64),
		ManifestLimits: ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}, MaxCommandOutputBytes: 1 << 20,
	}
	strategy, err := NewRsyncTreePublicationStrategy(fixedRsyncTreeProcessRunner{}, func() time.Time { return time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{Attempt: NewRsyncTreePublicationAttempt(attempt), RsyncTreeInput: &input}); err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Reconcile(context.Background(), PublicationReconcileRequest{
		Attempt: NewRsyncTreePublicationAttempt(attempt),
		RsyncTreeInput: &RsyncTreeReconcileInput{
			ManagedRoot: root, MarkerKey: input.MarkerKey, SourceFingerprint: input.SourceFingerprint,
			ChildFenceDigest: input.ChildFenceDigest, ManifestLimits: input.ManifestLimits,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fact, err := result.RsyncTreeResult()
	if err != nil {
		t.Fatal(err)
	}
	if fact.State != RsyncTreeReconcileStaging || fact.Commit != nil || fact.Manifest != nil {
		t.Fatalf("owned staging reconcile fact=%+v", fact)
	}
}

func TestRsyncTreePublicationStrategyReconcileRejectsFinalDirectoryWithoutAttemptMarker(t *testing.T) {
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("versioned payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := newLocalRsyncTreeProcessRunner(os.Environ)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := NewRsyncTreePublicationStrategy(runner, func() time.Time {
		return time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt := rsyncTreeAttemptForMarkerTest()
	attempt.RepositoryMarkerDigest = markerDigest
	attempt.ManagedRootIdentityDigest = rootIdentity
	input := RsyncTreePublicationInput{
		ManagedRoot: root, Source: RsyncTreeCommandSource{LocalPath: source}, MarkerKey: []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"),
		SourceFingerprint: strings.Repeat("3", 64), ChildFenceDigest: strings.Repeat("4", 64),
		ManifestLimits:        ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10},
		MaxCommandOutputBytes: 1 << 20,
	}
	runRsyncTreePublicationForTest(t, strategy, attempt, input)
	if err := os.Remove(filepath.Join(root, "points", attempt.FinalComponent, "attempt.json")); err != nil {
		t.Fatal(err)
	}

	_, err = strategy.Reconcile(context.Background(), PublicationReconcileRequest{
		Attempt: NewRsyncTreePublicationAttempt(attempt),
		RsyncTreeInput: &RsyncTreeReconcileInput{
			ManagedRoot: root, MarkerKey: input.MarkerKey, SourceFingerprint: input.SourceFingerprint,
			ChildFenceDigest: input.ChildFenceDigest, ManifestLimits: input.ManifestLimits,
		},
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("final directory without attempt marker error=%v, want conflict", err)
	}
}

func TestRsyncTreePublicationStrategyReconcileRejectsFinalAttemptMarkerMismatch(t *testing.T) {
	fixture := newPublishedRsyncTreeReconcileFixture(t)
	foreignAttempt := fixture.attempt
	foreignAttempt.AttemptID = strings.Repeat("e", 32)
	foreignAttempt.StagingComponent = foreignAttempt.RecoveryPointID + "." + foreignAttempt.AttemptID
	marker, err := encodeRsyncTreeAttemptMarkerV1(foreignAttempt, fixture.input.MarkerKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "points", fixture.attempt.FinalComponent, "attempt.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = fixture.strategy.Reconcile(context.Background(), fixture.request())
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("final attempt marker mismatch error=%v, want conflict", err)
	}
}

func TestRsyncTreePublicationStrategyReconcileRejectsStaleChildFence(t *testing.T) {
	fixture := newPublishedRsyncTreeReconcileFixture(t)
	request := fixture.request()
	request.RsyncTreeInput.ChildFenceDigest = strings.Repeat("9", 64)

	_, err := fixture.strategy.Reconcile(context.Background(), request)
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale child fence error=%v, want conflict", err)
	}
}

func TestRsyncTreePublicationStrategyReconcileRejectsManagedRootReplacement(t *testing.T) {
	fixture := newPublishedRsyncTreeReconcileFixture(t)
	if err := os.Rename(fixture.root, fixture.root+"-replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.root, "repository.json"), fixture.marker, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := fixture.strategy.Reconcile(context.Background(), fixture.request())
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("managed root replacement error=%v, want conflict", err)
	}
}

func TestRsyncTreePublicationStrategyHardlinkPreservesParentAndDropsDeletedSourceFiles(t *testing.T) {
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	for name, content := range map[string]string{"same": "same", "changed": "old", "deleted": "old-only"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	processRunner, err := newLocalRsyncTreeProcessRunner(os.Environ)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	strategy, err := NewRsyncTreePublicationStrategy(processRunner, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	input := RsyncTreePublicationInput{
		ManagedRoot: root, Source: RsyncTreeCommandSource{LocalPath: source}, MarkerKey: []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"),
		SourceFingerprint: strings.Repeat("3", 64), ChildFenceDigest: strings.Repeat("4", 64),
		ManifestLimits: ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}, MaxCommandOutputBytes: 1 << 20,
	}
	firstAttempt := rsyncTreeAttemptForMarkerTest()
	firstAttempt.RepositoryMarkerDigest = markerDigest
	firstAttempt.ManagedRootIdentityDigest = rootIdentity
	firstCommit := runRsyncTreePublicationForTest(t, strategy, firstAttempt, input)

	if err := os.WriteFile(filepath.Join(source, "changed"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(source, "deleted")); err != nil {
		t.Fatal(err)
	}
	secondAttempt := firstAttempt
	secondAttempt.RecoveryPointID = strings.Repeat("c", 32)
	secondAttempt.AttemptID = strings.Repeat("d", 32)
	secondAttempt.StagingComponent = secondAttempt.RecoveryPointID + "." + secondAttempt.AttemptID
	secondAttempt.FinalComponent = secondAttempt.RecoveryPointID
	secondAttempt.PublicationMode = backupasset.PublicationVersionedHardlink
	secondAttempt.ParentRecoveryPointID = firstAttempt.RecoveryPointID
	secondAttempt.ParentCommitDigest = firstCommit.CommitMarkerDigest
	secondAttempt.ParentManifestDigest = firstCommit.ManifestDigest
	secondCommit := runRsyncTreePublicationForTest(t, strategy, secondAttempt, input)
	if secondCommit.ParentRecoveryPointID != firstAttempt.RecoveryPointID {
		t.Fatalf("hardlink parent commit=%+v", secondCommit)
	}
	parentTree := filepath.Join(root, "points", firstAttempt.FinalComponent, "tree")
	candidateTree := filepath.Join(root, "points", secondAttempt.FinalComponent, "tree")
	parentSame, err := os.Stat(filepath.Join(parentTree, "same"))
	if err != nil {
		t.Fatal(err)
	}
	candidateSame, err := os.Stat(filepath.Join(candidateTree, "same"))
	if err != nil {
		t.Fatal(err)
	}
	parentChanged, err := os.Stat(filepath.Join(parentTree, "changed"))
	if err != nil {
		t.Fatal(err)
	}
	candidateChanged, err := os.Stat(filepath.Join(candidateTree, "changed"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(parentSame, candidateSame) || os.SameFile(parentChanged, candidateChanged) {
		t.Fatalf("unexpected hardlink inode relationships same=%v changed=%v", os.SameFile(parentSame, candidateSame), os.SameFile(parentChanged, candidateChanged))
	}
	if _, err := os.Stat(filepath.Join(candidateTree, "deleted")); !os.IsNotExist(err) {
		t.Fatalf("deleted source entry remains in candidate: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(parentTree, "changed")); err != nil || string(content) != "old" {
		t.Fatalf("parent changed content=%q err=%v", content, err)
	}
}

type rsyncTreeTopologyTransitionFixture struct {
	parentTree    string
	candidateTree string
	parentA       os.FileInfo
	parentB       os.FileInfo
	strategy      PublicationStrategy
	attempt       RsyncTreeAttemptV1
	input         RsyncTreePublicationInput
	commit        RsyncTreeCommitV1
}

func (fixture rsyncTreeTopologyTransitionFixture) reconcileRequest() PublicationReconcileRequest {
	return PublicationReconcileRequest{
		Attempt: NewRsyncTreePublicationAttempt(fixture.attempt),
		RsyncTreeInput: &RsyncTreeReconcileInput{
			ManagedRoot: fixture.input.ManagedRoot, MarkerKey: fixture.input.MarkerKey, SourceFingerprint: fixture.input.SourceFingerprint,
			ChildFenceDigest: fixture.input.ChildFenceDigest, ManifestLimits: fixture.input.ManifestLimits,
		},
	}
}

func TestRsyncTreePublicationStrategyHardlinkPreservesSourceBreakLinkTopology(t *testing.T) {
	fixture := runRsyncTreeTopologyTransitionForTest(t, true, true)
	candidateA, err := os.Stat(filepath.Join(fixture.candidateTree, "a"))
	if err != nil {
		t.Fatal(err)
	}
	candidateB, err := os.Stat(filepath.Join(fixture.candidateTree, "b"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(candidateA, candidateB) {
		t.Fatal("break-link source topology was merged in candidate")
	}
	currentParentA, err := os.Stat(filepath.Join(fixture.parentTree, "a"))
	if err != nil {
		t.Fatal(err)
	}
	currentParentB, err := os.Stat(filepath.Join(fixture.parentTree, "b"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(currentParentA, fixture.parentA) || !os.SameFile(currentParentB, fixture.parentB) || !os.SameFile(currentParentA, currentParentB) {
		t.Fatal("parent hardlink topology changed during break-link publication")
	}
}

func TestRsyncTreePublicationStrategyHardlinkPreservesSourceCreateLinkTopology(t *testing.T) {
	fixture := runRsyncTreeTopologyTransitionForTest(t, false, false)
	candidateA, err := os.Stat(filepath.Join(fixture.candidateTree, "a"))
	if err != nil {
		t.Fatal(err)
	}
	candidateB, err := os.Stat(filepath.Join(fixture.candidateTree, "b"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(candidateA, candidateB) {
		t.Fatal("create-link source topology was split in candidate")
	}
	currentParentA, err := os.Stat(filepath.Join(fixture.parentTree, "a"))
	if err != nil {
		t.Fatal(err)
	}
	currentParentB, err := os.Stat(filepath.Join(fixture.parentTree, "b"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(currentParentA, currentParentB) || !os.SameFile(currentParentA, fixture.parentA) || !os.SameFile(currentParentB, fixture.parentB) {
		t.Fatal("parent independent topology changed during create-link publication")
	}
}

func runRsyncTreeTopologyTransitionForTest(t *testing.T, initiallyLinked, breakLink bool) rsyncTreeTopologyTransitionFixture {
	t.Helper()
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a"), []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if initiallyLinked {
		if err := os.Link(filepath.Join(source, "a"), filepath.Join(source, "b")); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(filepath.Join(source, "b"), []byte("same"), 0o600); err != nil {
			t.Fatal(err)
		}
		aInfo, err := os.Stat(filepath.Join(source, "a"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(source, "b"), aInfo.ModTime(), aInfo.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	processRunner, err := newLocalRsyncTreeProcessRunner(os.Environ)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	strategy, err := NewRsyncTreePublicationStrategy(processRunner, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	input := RsyncTreePublicationInput{
		ManagedRoot: root, Source: RsyncTreeCommandSource{LocalPath: source}, MarkerKey: []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"),
		SourceFingerprint: strings.Repeat("3", 64), ChildFenceDigest: strings.Repeat("4", 64),
		ManifestLimits: ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}, MaxCommandOutputBytes: 1 << 20,
	}
	firstAttempt := rsyncTreeAttemptForMarkerTest()
	firstAttempt.RepositoryMarkerDigest = markerDigest
	firstAttempt.ManagedRootIdentityDigest = rootIdentity
	firstCommit := runRsyncTreePublicationForTest(t, strategy, firstAttempt, input)
	parentTree := filepath.Join(root, "points", firstAttempt.FinalComponent, "tree")
	parentA, err := os.Stat(filepath.Join(parentTree, "a"))
	if err != nil {
		t.Fatal(err)
	}
	parentB, err := os.Stat(filepath.Join(parentTree, "b"))
	if err != nil {
		t.Fatal(err)
	}
	if breakLink {
		if err := os.Remove(filepath.Join(source, "b")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "b"), []byte("same"), 0o600); err != nil {
			t.Fatal(err)
		}
		aInfo, err := os.Stat(filepath.Join(source, "a"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(source, "b"), aInfo.ModTime(), aInfo.ModTime()); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.Remove(filepath.Join(source, "b")); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(source, "a"), filepath.Join(source, "b")); err != nil {
			t.Fatal(err)
		}
	}
	secondAttempt := firstAttempt
	secondAttempt.RecoveryPointID = strings.Repeat("c", 32)
	secondAttempt.AttemptID = strings.Repeat("d", 32)
	secondAttempt.StagingComponent = secondAttempt.RecoveryPointID + "." + secondAttempt.AttemptID
	secondAttempt.FinalComponent = secondAttempt.RecoveryPointID
	secondAttempt.PublicationMode = backupasset.PublicationVersionedHardlink
	secondAttempt.ParentRecoveryPointID = firstAttempt.RecoveryPointID
	secondAttempt.ParentCommitDigest = firstCommit.CommitMarkerDigest
	secondAttempt.ParentManifestDigest = firstCommit.ManifestDigest
	secondCommit := runRsyncTreePublicationForTest(t, strategy, secondAttempt, input)
	return rsyncTreeTopologyTransitionFixture{
		parentTree: parentTree, candidateTree: filepath.Join(root, "points", secondAttempt.FinalComponent, "tree"),
		parentA: parentA, parentB: parentB, strategy: strategy, attempt: secondAttempt, input: input, commit: secondCommit,
	}
}
func TestRsyncTreePublicationStrategyReconcilePreservesCommittedBreakLinkTopology(t *testing.T) {
	assertRsyncTreeTopologyTransitionReconciles(t, runRsyncTreeTopologyTransitionForTest(t, true, true))
}

func TestRsyncTreePublicationStrategyReconcilePreservesCommittedCreateLinkTopology(t *testing.T) {
	assertRsyncTreeTopologyTransitionReconciles(t, runRsyncTreeTopologyTransitionForTest(t, false, false))
}

func assertRsyncTreeTopologyTransitionReconciles(t *testing.T, fixture rsyncTreeTopologyTransitionFixture) {
	t.Helper()
	reconciled, err := fixture.strategy.Reconcile(context.Background(), fixture.reconcileRequest())
	if err != nil {
		t.Fatal(err)
	}
	fact, err := reconciled.RsyncTreeResult()
	if err != nil {
		t.Fatal(err)
	}
	if fact.State != RsyncTreeReconcileFinal || fact.Commit == nil || fact.Commit.CommitMarkerDigest != fixture.commit.CommitMarkerDigest ||
		fact.Manifest == nil || fact.Manifest.Digest != fixture.commit.ManifestDigest {
		t.Fatalf("reconciled topology fixture=%+v", fact)
	}
}

func TestRsyncTreePublicationStrategyReconcileRejectsCommittedTreeTampering(t *testing.T) {
	t.Run("candidate", func(t *testing.T) {
		fixture := runRsyncTreeTopologyTransitionForTest(t, true, true)
		if err := os.WriteFile(filepath.Join(fixture.candidateTree, "a"), []byte("tampered"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.strategy.Reconcile(context.Background(), fixture.reconcileRequest()); !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("candidate tamper error=%v, want conflict", err)
		}
	})
	t.Run("parent", func(t *testing.T) {
		fixture := runRsyncTreeTopologyTransitionForTest(t, true, true)
		if err := os.WriteFile(filepath.Join(fixture.parentTree, "a"), []byte("tampered"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.strategy.Reconcile(context.Background(), fixture.reconcileRequest()); !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("parent tamper error=%v, want conflict", err)
		}
	})
}

func TestRsyncTreePublicationStrategyFullCopyPointsDoNotShareInodes(t *testing.T) {
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "same"), []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := newLocalRsyncTreeProcessRunner(os.Environ)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := NewRsyncTreePublicationStrategy(runner, func() time.Time {
		return time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	input := RsyncTreePublicationInput{
		ManagedRoot: root, Source: RsyncTreeCommandSource{LocalPath: source}, MarkerKey: []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"),
		SourceFingerprint: strings.Repeat("3", 64), ChildFenceDigest: strings.Repeat("4", 64),
		ManifestLimits: ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}, MaxCommandOutputBytes: 1 << 20,
	}
	first := rsyncTreeAttemptForMarkerTest()
	first.RepositoryMarkerDigest = markerDigest
	first.ManagedRootIdentityDigest = rootIdentity
	runRsyncTreePublicationForTest(t, strategy, first, input)
	second := first
	second.RecoveryPointID = strings.Repeat("c", 32)
	second.AttemptID = strings.Repeat("d", 32)
	second.StagingComponent = second.RecoveryPointID + "." + second.AttemptID
	second.FinalComponent = second.RecoveryPointID
	runRsyncTreePublicationForTest(t, strategy, second, input)
	firstFile, err := os.Stat(filepath.Join(root, "points", first.FinalComponent, "tree", "same"))
	if err != nil {
		t.Fatal(err)
	}
	secondFile, err := os.Stat(filepath.Join(root, "points", second.FinalComponent, "tree", "same"))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(firstFile, secondFile) {
		t.Fatal("full-copy points shared a regular-file inode")
	}
}

func TestRsyncTreePublicationStrategyRejectsMismatchedHardlinkParentCommit(t *testing.T) {
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := newLocalRsyncTreeProcessRunner(os.Environ)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := NewRsyncTreePublicationStrategy(runner, func() time.Time { return time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	input := RsyncTreePublicationInput{ManagedRoot: root, Source: RsyncTreeCommandSource{LocalPath: source}, MarkerKey: []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"), SourceFingerprint: strings.Repeat("3", 64), ChildFenceDigest: strings.Repeat("4", 64), ManifestLimits: ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}, MaxCommandOutputBytes: 1 << 20}
	first := rsyncTreeAttemptForMarkerTest()
	first.RepositoryMarkerDigest = markerDigest
	first.ManagedRootIdentityDigest = rootIdentity
	firstCommit := runRsyncTreePublicationForTest(t, strategy, first, input)
	second := first
	second.RecoveryPointID = strings.Repeat("c", 32)
	second.AttemptID = strings.Repeat("d", 32)
	second.StagingComponent = second.RecoveryPointID + "." + second.AttemptID
	second.FinalComponent = second.RecoveryPointID
	second.PublicationMode = backupasset.PublicationVersionedHardlink
	second.ParentRecoveryPointID = first.RecoveryPointID
	second.ParentManifestDigest = firstCommit.ManifestDigest
	second.ParentCommitDigest = strings.Repeat("9", 64)
	if _, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{Attempt: NewRsyncTreePublicationAttempt(second), RsyncTreeInput: &input}); !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("mismatched parent commit error=%v, want conflict", err)
	}
}

func TestRsyncTreePublicationStrategyRechecksLocalSourceOverlap(t *testing.T) {
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	runner, err := newLocalRsyncTreeProcessRunner(os.Environ)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := NewRsyncTreePublicationStrategy(runner, func() time.Time { return time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	attempt := rsyncTreeAttemptForMarkerTest()
	attempt.RepositoryMarkerDigest = markerDigest
	attempt.ManagedRootIdentityDigest = rootIdentity
	input := RsyncTreePublicationInput{ManagedRoot: root, Source: RsyncTreeCommandSource{LocalPath: root}, MarkerKey: []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"), SourceFingerprint: strings.Repeat("3", 64), ChildFenceDigest: strings.Repeat("4", 64), ManifestLimits: ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}, MaxCommandOutputBytes: 1 << 20}
	if _, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{Attempt: NewRsyncTreePublicationAttempt(attempt), RsyncTreeInput: &input}); err == nil {
		t.Fatal("strategy accepted a local source overlapping its managed root")
	}
}

func TestRsyncTreePublicationStrategyNeverCommitsPartialExit(t *testing.T) {
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	attempt := rsyncTreeAttemptForMarkerTest()
	attempt.RepositoryMarkerDigest = markerDigest
	attempt.ManagedRootIdentityDigest = rootIdentity
	strategy, err := NewRsyncTreePublicationStrategy(fixedRsyncTreeProcessRunner{result: rsyncTreeProcessResult{ExitCode: 23, ExitCodeKnown: true}}, func() time.Time {
		return time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	input := RsyncTreePublicationInput{ManagedRoot: root, Source: RsyncTreeCommandSource{LocalPath: t.TempDir()}, MarkerKey: []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"), SourceFingerprint: strings.Repeat("3", 64), ChildFenceDigest: strings.Repeat("4", 64), ManifestLimits: ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}, MaxCommandOutputBytes: 1 << 20}
	prepared, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{Attempt: NewRsyncTreePublicationAttempt(attempt), RsyncTreeInput: &input})
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Execute(context.Background(), prepared, PublicationProgress{})
	if err != nil || result.ExitCode != 23 || result.Completion != backupasset.CompletionKnownNonzero || result.ProviderCommit != nil || result.EvidenceCode != backupasset.FailureProviderNonzeroExit {
		t.Fatalf("partial execution result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "points", attempt.FinalComponent)); !os.IsNotExist(err) {
		t.Fatalf("partial execution published final tree: %v", err)
	}
}

func TestRsyncTreePublicationStrategyNeverCommitsUnknownProcessOutcome(t *testing.T) {
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	attempt := rsyncTreeAttemptForMarkerTest()
	attempt.RepositoryMarkerDigest = markerDigest
	attempt.ManagedRootIdentityDigest = rootIdentity
	strategy, err := NewRsyncTreePublicationStrategy(fixedRsyncTreeProcessRunner{
		result: rsyncTreeProcessResult{ExitCode: UnknownProviderExitCode}, err: backupasset.ErrCapabilityUnavailable,
	}, func() time.Time { return time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	input := RsyncTreePublicationInput{ManagedRoot: root, Source: RsyncTreeCommandSource{LocalPath: t.TempDir()}, MarkerKey: []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"), SourceFingerprint: strings.Repeat("3", 64), ChildFenceDigest: strings.Repeat("4", 64), ManifestLimits: ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10}, MaxCommandOutputBytes: 1 << 20}
	prepared, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{Attempt: NewRsyncTreePublicationAttempt(attempt), RsyncTreeInput: &input})
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Execute(context.Background(), prepared, PublicationProgress{})
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) || result.ExitCode != UnknownProviderExitCode || result.Completion != backupasset.CompletionOutcomeUnknown || result.ProviderCommit != nil {
		t.Fatalf("unknown execution result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "points", attempt.FinalComponent)); !os.IsNotExist(err) {
		t.Fatalf("unknown execution published final tree: %v", err)
	}
}

type rsyncTreeReconcileFixture struct {
	root     string
	marker   []byte
	attempt  RsyncTreeAttemptV1
	commit   RsyncTreeCommitV1
	strategy PublicationStrategy
	input    RsyncTreePublicationInput
}

func newPublishedRsyncTreeReconcileFixture(t *testing.T) rsyncTreeReconcileFixture {
	t.Helper()
	root := t.TempDir()
	marker := []byte(`{"layout_version":1,"repository":"opaque"}`)
	if err := os.WriteFile(filepath.Join(root, "repository.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	markerSum := sha256.Sum256(marker)
	markerDigest := hex.EncodeToString(markerSum[:])
	tree, err := openRsyncManagedTree(root)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := tree.identityDigest(markerDigest)
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("versioned payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner, err := newLocalRsyncTreeProcessRunner(os.Environ)
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := NewRsyncTreePublicationStrategy(runner, func() time.Time {
		return time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt := rsyncTreeAttemptForMarkerTest()
	attempt.RepositoryMarkerDigest = markerDigest
	attempt.ManagedRootIdentityDigest = rootIdentity
	input := RsyncTreePublicationInput{
		ManagedRoot: root, Source: RsyncTreeCommandSource{LocalPath: source}, MarkerKey: []byte("FAKE_RSYNC_TREE_MARKER_KEY_32_BYTES"),
		SourceFingerprint: strings.Repeat("3", 64), ChildFenceDigest: strings.Repeat("4", 64),
		ManifestLimits:        ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 4096, MaxDepth: 10},
		MaxCommandOutputBytes: 1 << 20,
	}
	commit := runRsyncTreePublicationForTest(t, strategy, attempt, input)
	return rsyncTreeReconcileFixture{root: root, marker: marker, attempt: attempt, commit: commit, strategy: strategy, input: input}
}

func (fixture rsyncTreeReconcileFixture) request() PublicationReconcileRequest {
	return PublicationReconcileRequest{
		Attempt: NewRsyncTreePublicationAttempt(fixture.attempt),
		RsyncTreeInput: &RsyncTreeReconcileInput{
			ManagedRoot: fixture.root, MarkerKey: fixture.input.MarkerKey, SourceFingerprint: fixture.input.SourceFingerprint,
			ChildFenceDigest: fixture.input.ChildFenceDigest, ManifestLimits: fixture.input.ManifestLimits,
		},
	}
}

type fixedRsyncTreeProcessRunner struct {
	result rsyncTreeProcessResult
	err    error
}

func (runner fixedRsyncTreeProcessRunner) Run(context.Context, RsyncTreeCommand, int64) (rsyncTreeProcessResult, error) {
	return runner.result, runner.err
}

func runRsyncTreePublicationForTest(t *testing.T, strategy PublicationStrategy, attempt RsyncTreeAttemptV1, input RsyncTreePublicationInput) RsyncTreeCommitV1 {
	t.Helper()
	prepared, err := strategy.Prepare(context.Background(), PublicationPrepareRequest{Attempt: NewRsyncTreePublicationAttempt(attempt), RsyncTreeInput: &input})
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Execute(context.Background(), prepared, PublicationProgress{})
	if err != nil || result.ExitCode != 0 || result.Completion != backupasset.CompletionKnownExitZero || result.ProviderCommit == nil {
		t.Fatalf("execute result=%+v err=%v", result, err)
	}
	commit, err := strategy.RecordCommit(context.Background(), prepared, result)
	if err != nil {
		t.Fatal(err)
	}
	typed, err := commit.RsyncTreeCommit()
	if err != nil {
		t.Fatal(err)
	}
	return typed
}

func rsyncTreeAttemptForMarkerTest() RsyncTreeAttemptV1 {
	pointID := strings.Repeat("a", 32)
	attemptID := strings.Repeat("b", 32)
	return RsyncTreeAttemptV1{
		RepositoryID:              strings.Repeat("c", 32),
		TaskRepositoryLinkID:      strings.Repeat("d", 32),
		RecoveryPointID:           pointID,
		AttemptID:                 attemptID,
		TaskID:                    7,
		TaskRunID:                 8,
		PublicationMode:           backupasset.PublicationVersionedFullCopy,
		PointDeadlineAt:           time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		ExpectedTaskRevision:      1,
		RepositoryMarkerDigest:    strings.Repeat("e", 64),
		ManagedRootIdentityDigest: strings.Repeat("f", 64),
		StagingComponent:          pointID + "." + attemptID,
		FinalComponent:            pointID,
		CommandProfileVersion:     rsyncTreeCommandProfileVersion,
		PreflightID:               strings.Repeat("1", 32),
		PreflightDigest:           strings.Repeat("2", 64),
	}
}

func rsyncTreeCommitForMarkerTest() RsyncTreeCommitV1 {
	attempt := rsyncTreeAttemptForMarkerTest()
	return RsyncTreeCommitV1{
		LayoutVersion: taggedPublicationSchemaV1, RepositoryID: attempt.RepositoryID, TaskRepositoryLinkID: attempt.TaskRepositoryLinkID,
		RecoveryPointID: attempt.RecoveryPointID, AttemptID: attempt.AttemptID, PublicationMode: attempt.PublicationMode,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("1", 64), ManifestEntryCount: 1, LogicalBytes: 42,
		FidelityDigest: strings.Repeat("2", 64), SourceFingerprint: strings.Repeat("3", 64),
		ProviderCommittedAt: time.Date(2026, 7, 15, 11, 0, 0, 0, time.UTC), ChildFenceDigest: strings.Repeat("4", 64),
		PointDeadlineAt: attempt.PointDeadlineAt, RenameVerified: true, DirectoryFsyncVerified: true,
	}
}
