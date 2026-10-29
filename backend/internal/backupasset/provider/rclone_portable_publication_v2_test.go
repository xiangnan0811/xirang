package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func portableV2PublicationFixture(t *testing.T) (RclonePortablePublicationRequest, RcloneManifestBundle) {
	t.Helper()
	request := portablePublicationRequestForTest(t, false)
	request.Manifest = portableV2TestObservation(t,
		portableV2TestFileListing("2026-07-16T01:00:01.123456789Z", portableV2HelloSHA256,
			`{"mode":"100640","uid":"1000","gid":"1000","mtime":"2026-07-16T01:00:01.123456789Z"}`),
		request.ManifestOptions, false)
	destination := portableV2TestObservation(t,
		portableV2TestFileListing("2026-07-16T01:00:01Z", "", ""), request.ManifestOptions, true)
	return request, destination
}

func TestRclonePortableManifestV2PublicationCatalogUsesSourceMetadata(t *testing.T) {
	request, destination := portableV2PublicationFixture(t)
	remote := &fakeRclonePortableRemote{
		presence:      RcloneAttemptAbsent,
		observations:  []RcloneManifestBundle{request.Manifest, request.Manifest, destination, destination, request.Manifest, destination},
		fullByteProof: RclonePortableByteVerification{Complete: true, VerifiedBytes: 5},
	}
	strategy, err := NewRclonePublicationStrategy(
		NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest),
		NewRcloneNativePublisher(&rcloneNativeDataPlaneFake{}, portableNowForTest),
	)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := strategy.portable.Publish(context.Background(), request)
	if err != nil {
		t.Fatalf("publish differing stable observations: %v", err)
	}
	if remote.fullByteCalls != 1 || remote.observationAt != 6 || remote.mutations[len(remote.mutations)-1] != "commit.json" ||
		commit.MinimumRuntimeRevision != 2 || commit.SourceObservationDigest != request.Manifest.ObservationDigest ||
		commit.DestinationObservationDigest != destination.ObservationDigest ||
		commit.SourceObservationDigest == commit.DestinationObservationDigest || commit.ManifestIndexDigest == request.Manifest.IndexDigest ||
		commit.Portable.DownloadVerifiedBytes != 5 {
		t.Fatalf("unexpected publication evidence commit=%+v checks=%d observations=%d", commit, remote.fullByteCalls, remote.observationAt)
	}
	restarted := request
	restarted.Manifest = RcloneManifestBundle{}
	readRequest := CatalogReadRequest{
		Provider: backupasset.ProviderRclone, RecoveryPointID: request.Attempt.RecoveryPointID,
		Snapshot: ReadSnapshot{RepositoryID: request.Attempt.RepositoryID, CapabilityRevision: int(request.Attempt.CapabilityRevision),
			SourceRevision: strings.Repeat("f", 64), Access: AccessBinding{Provider: backupasset.ProviderRclone, RepositoryID: request.Attempt.RepositoryID}},
		Point: PointLocator{Native: "FAKE_EXACT_PORTABLE_POINT_FOR_TEST_ONLY"}, Mode: CatalogProofPublicationManifest,
		Manifest: CatalogManifestProof{ManifestID: strings.Repeat("9", 32), Revision: 1, DigestAlgorithm: "sha256",
			Digest: commit.ManifestIndexDigest, EntryCount: int64(commit.ManifestEntryCount),
			Completeness: backupasset.ManifestComplete, SourceRevision: strings.Repeat("f", 64)},
		RcloneProof: &RcloneCatalogProofInput{Reconcile: RcloneReconcileInput{ManifestLimits: request.ManifestOptions.Limits, PortableRequest: &restarted}, Commit: commit},
		MaxItems:    int(commit.ManifestEntryCount) + 1,
	}
	remote.reads = nil
	session, err := strategy.OpenCatalogRead(context.Background(), readRequest)
	if err != nil {
		t.Fatalf("open V2 Catalog: %v", err)
	}
	page, err := session.ListCanonical(context.Background(), PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ModifiedAt == nil ||
		page.Items[0].ModifiedAt.Format(time.RFC3339Nano) != "2026-07-16T01:00:01.123456789Z" ||
		page.Items[0].Mode != "100640" || page.Items[0].Owner != "1000:1000" || page.Items[0].Fingerprint != portableV2HelloSHA256 {
		t.Fatalf("source Catalog projection=%+v err=%v", page.Items, err)
	}
	if _, err := session.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize V2 Catalog: %v", err)
	}
	if remote.reads["commit.json"] != 1 || remote.reads["manifest-index.json"] != 1 || remote.reads["manifest-000000.jsonl"] != 1 {
		t.Fatalf("V2 controls reread during Catalog: %v", remote.reads)
	}
}

func TestRclonePortableManifestV2PublicationRejectsDriftAndIncompleteProof(t *testing.T) {
	request, destination := portableV2PublicationFixture(t)
	changedSource := request.Manifest
	changedSource.ObservationDigest = strings.Repeat("e", 64)
	changedDestination := destination
	changedDestination.ObservationDigest = strings.Repeat("d", 64)
	wrongSize := portableV2TestObservation(t, strings.Replace(
		portableV2TestFileListing("2026-07-16T01:00:01Z", "", ""), `"Size":5`, `"Size":6`, 1), request.ManifestOptions, true)
	wrongHash := portableV2TestObservation(t,
		portableV2TestFileListing("2026-07-16T01:00:01Z", strings.Repeat("a", 64), ""), request.ManifestOptions, true)
	extra := portableV2TestObservation(t, strings.TrimSuffix(
		portableV2TestFileListing("2026-07-16T01:00:01Z", "", ""), "]")+
		`,{"Path":"other","Name":"other","Size":0,"ModTime":"2026-07-16T01:00:01Z","IsDir":false}]`, request.ManifestOptions, true)
	missing := portableV2TestObservation(t, `[]`, request.ManifestOptions, true)
	wrongKind := portableV2TestObservation(t, `[{"Path":"file.txt","Name":"file.txt","Size":0,"ModTime":"2026-07-16T01:00:01Z","IsDir":true}]`, request.ManifestOptions, true)
	proof := RclonePortableByteVerification{Complete: true, VerifiedBytes: 5}
	checkFailure := errors.New("FAKE_RCLONE_CHECK_FAILURE_FOR_TEST_ONLY")
	tests := []struct {
		name         string
		observations []RcloneManifestBundle
		proof        RclonePortableByteVerification
		checkError   error
		limit        int64
		want         error
	}{
		{"source_drift", []RcloneManifestBundle{request.Manifest, changedSource, destination, destination}, proof, nil, 0, ErrRcloneSourceDrift},
		{"destination_drift", []RcloneManifestBundle{request.Manifest, request.Manifest, destination, changedDestination}, proof, nil, 0, ErrRcloneDestinationUnstable},
		{"postcheck_source_drift", []RcloneManifestBundle{request.Manifest, request.Manifest, destination, destination, changedSource, destination}, proof, nil, 0, ErrRcloneSourceDrift},
		{"postcheck_destination_drift", []RcloneManifestBundle{request.Manifest, request.Manifest, destination, destination, request.Manifest, changedDestination}, proof, nil, 0, ErrRcloneDestinationUnstable},
		{"partial_proof", []RcloneManifestBundle{request.Manifest, request.Manifest, destination, destination}, RclonePortableByteVerification{Complete: true, VerifiedBytes: 4}, nil, 0, ErrRcloneManifestFullByteProofRequired},
		{"incomplete_proof", []RcloneManifestBundle{request.Manifest, request.Manifest, destination, destination}, RclonePortableByteVerification{VerifiedBytes: 5}, nil, 0, ErrRcloneManifestFullByteProofRequired},
		{"check_error", []RcloneManifestBundle{request.Manifest, request.Manifest, destination, destination}, proof, checkFailure, 0, checkFailure},
		{"budget", []RcloneManifestBundle{request.Manifest, request.Manifest, destination, destination}, proof, nil, 4, ErrRcloneManifestFullByteProofRequired},
		{"size", []RcloneManifestBundle{request.Manifest, request.Manifest, wrongSize, wrongSize}, proof, nil, 0, ErrRcloneManifestObservationMismatch},
		{"strong_hash", []RcloneManifestBundle{request.Manifest, request.Manifest, wrongHash, wrongHash}, proof, nil, 0, ErrRcloneManifestObservationMismatch},
		{"extra", []RcloneManifestBundle{request.Manifest, request.Manifest, extra, extra}, proof, nil, 0, ErrRcloneManifestObservationMismatch},
		{"missing", []RcloneManifestBundle{request.Manifest, request.Manifest, missing, missing}, proof, nil, 0, ErrRcloneManifestObservationMismatch},
		{"kind", []RcloneManifestBundle{request.Manifest, request.Manifest, wrongKind, wrongKind}, proof, nil, 0, ErrRcloneManifestObservationMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := request
			if test.limit != 0 {
				input.FullVerifyMaxBytes = test.limit
			}
			remote := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, observations: test.observations,
				fullByteProof: test.proof, fullByteError: test.checkError}
			publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
			if _, err := publisher.Publish(context.Background(), input); !errors.Is(err, test.want) {
				t.Fatalf("publish error=%v want=%v", err, test.want)
			}
			if containsString(remote.mutations, "commit.json") {
				t.Fatalf("invalid publication wrote commit: %v", remote.mutations)
			}
			if (test.name == "strong_hash" || test.name == "size" || test.name == "extra" || test.name == "missing" || test.name == "kind" || test.name == "budget") && remote.fullByteCalls != 0 {
				t.Fatal("incompatible observations or over-budget check reached download")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	remote := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent, observations: []RcloneManifestBundle{request.Manifest}}
	if _, err := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest).Publish(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publication error=%v", err)
	}
	if containsString(remote.mutations, "commit.json") {
		t.Fatal("cancelled publication wrote commit")
	}
}

func TestRclonePortableManifestV2PublicationRejectsChangedSymlinkTarget(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	request.ManifestOptions.SymlinkTargetReader = func(context.Context, string, int64) ([]byte, error) {
		return []byte("dir/file.txt"), nil
	}
	source := `[{"Path":"link.rclonelink","Name":"link.rclonelink","Size":12,"ModTime":"2026-07-16T01:00:01Z","IsDir":false,"Metadata":{"mode":"120000"}}]`
	target := strings.Replace(source, `"mode":"120000"`, `"mode":"100644"`, 1)
	request.Manifest = portableV2TestObservation(t, source, request.ManifestOptions, false)
	destinationOptions := request.ManifestOptions
	destinationOptions.SymlinkTargetReader = func(context.Context, string, int64) ([]byte, error) {
		return []byte("other-target"), nil
	}
	destination := portableV2TestObservation(t, target, destinationOptions, true)
	remote := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent,
		observations: []RcloneManifestBundle{request.Manifest, request.Manifest, destination, destination}}
	if _, err := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest).Publish(context.Background(), request); !errors.Is(err, ErrRcloneManifestObservationMismatch) {
		t.Fatalf("changed symlink target error=%v", err)
	}
	if remote.fullByteCalls != 0 || containsString(remote.mutations, "commit.json") {
		t.Fatalf("changed symlink reached download or commit: checks=%d mutations=%v", remote.fullByteCalls, remote.mutations)
	}
}

func TestRclonePortableManifestV2StrongPublicationSkipsDownload(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	remote := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent,
		observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest}}
	commit, err := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest).Publish(context.Background(), request)
	if err != nil || remote.fullByteCalls != 0 || remote.observationAt != 4 || commit.Portable == nil || commit.Portable.DownloadVerifiedBytes != 0 {
		t.Fatalf("strong publication commit=%+v checks=%d observations=%d err=%v", commit, remote.fullByteCalls, remote.observationAt, err)
	}
}

func TestRclonePortableManifestV2DirectoryOnlyPublicationDoesNotDownload(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	listing := `[{"Path":"empty","Name":"empty","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true}]`
	request.Manifest = portableV2TestObservation(t, listing, request.ManifestOptions, false)
	remote := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent,
		observations: []RcloneManifestBundle{request.Manifest, request.Manifest, request.Manifest, request.Manifest}}
	publisher := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest)
	commit, err := publisher.Publish(context.Background(), request)
	if err != nil || remote.fullByteCalls != 0 || commit.ManifestEntryCount != 1 || commit.LogicalBytes != 0 {
		t.Fatalf("directory publication commit=%+v checks=%d err=%v", commit, remote.fullByteCalls, err)
	}
	var index rclonePortableManifestIndexV2
	if err := json.Unmarshal(remote.controls["manifest-index.json"], &index); err != nil ||
		index.Fidelity.HashFidelity != backupasset.RcloneHashNotEvaluated || !index.Fidelity.EmptyDirectoriesPreserved ||
		index.Fidelity.RequiresFullByteVerification || !index.ContentProof.Complete || index.ContentProof.VerifiedBytes != 0 {
		t.Fatalf("directory publication index=%+v err=%v", index, err)
	}
}

func TestRclonePortableManifestV2CommandProofRequiresSuccessfulDownload(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	commands := &rclonePreflightCommandFake{}
	remote, err := NewCommandRclonePortableRemote(commands, &fakeRcloneStagedPayloadTransport{}, func() (OperationLimits, error) {
		return testOperationLimits(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := remote.VerifyFullBytes(context.Background(), request, 5)
	if err != nil || !proof.Complete || proof.VerifiedBytes != 5 || len(commands.invocations) != 1 ||
		commands.invocations[0].Operation != OperationRcloneManagedCheckDownload {
		t.Fatalf("successful download proof=%+v invocations=%d err=%v", proof, len(commands.invocations), err)
	}
	failure := errors.New("FAKE_RCLONE_CHECK_FAILURE_FOR_TEST_ONLY")
	commands.failOperation, commands.failErr = OperationRcloneManagedCheckDownload, failure
	proof, err = remote.VerifyFullBytes(context.Background(), request, 5)
	if !errors.Is(err, failure) || proof.Complete || proof.VerifiedBytes != 0 {
		t.Fatalf("failed download proof=%+v err=%v", proof, err)
	}
	calls := len(commands.invocations)
	request.FullVerifyMaxBytes = 4
	if _, err := remote.VerifyFullBytes(context.Background(), request, 5); !errors.Is(err, ErrRcloneManifestFullByteProofRequired) ||
		len(commands.invocations) != calls {
		t.Fatalf("over-budget download reached command: calls=%d err=%v", len(commands.invocations), err)
	}
}

func TestRclonePortableManifestV2PublicationRejectsManifestBudgetsBeforeCommit(t *testing.T) {
	request, destination := portableV2PublicationFixture(t)
	observationBytes := func(value RcloneManifestBundle) int64 {
		total := int64(len(value.IndexEncoded))
		for _, chunk := range value.Chunks {
			total += chunk.Size
		}
		return total
	}
	inputBudget := max(observationBytes(request.Manifest), observationBytes(destination))
	for _, kind := range []string{"bytes", "spool"} {
		t.Run(kind, func(t *testing.T) {
			bounded := request
			if kind == "bytes" {
				bounded.ManifestOptions.Limits.MaxBytes = inputBudget
			} else {
				bounded.ManifestOptions.SpoolMaxBytes = inputBudget
			}
			remote := &fakeRclonePortableRemote{presence: RcloneAttemptAbsent,
				observations:  []RcloneManifestBundle{request.Manifest, request.Manifest, destination, destination, request.Manifest, destination},
				fullByteProof: RclonePortableByteVerification{Complete: true, VerifiedBytes: 5}}
			_, err := NewRclonePortablePublisher(remote, func(time.Duration) {}, portableNowForTest).Publish(context.Background(), bounded)
			if !errors.Is(err, ErrRcloneManifestLimitExceeded) || containsString(remote.mutations, "commit.json") {
				t.Fatalf("V2 %s budget error=%v mutations=%v", kind, err, remote.mutations)
			}
		})
	}
}
