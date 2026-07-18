package provider

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestResticManifestRequiresExactIdentityIDTagsOriginalAndTime(t *testing.T) {
	valid := readResticPublicationFixture(t, "manifest-valid.ndjson")
	for _, test := range []struct {
		name string
		body []byte
	}{
		{"wrong full ID", bytes.Replace(valid, []byte(strings.Repeat("a", 64)), []byte(strings.Repeat("d", 64)), 1)},
		{"extra tag", bytes.Replace(valid, []byte(`"tags":["xirang.link.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","xirang.point.v1.cccccccccccccccccccccccccccccccc"]`), []byte(`"tags":["xirang.link.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","xirang.point.v1.cccccccccccccccccccccccccccccccc","extra"]`), 1)},
		{"rewritten", readResticPublicationFixture(t, "manifest-rewritten.ndjson")},
		{"time mismatch", bytes.Replace(valid, []byte("2026-07-14T03:00:00Z"), []byte("2026-07-14T03:00:01Z"), 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence, err := buildResticManifestForTest(t, test.body, manifestLimitsForTest())
			if err != nil || evidence.Completeness == backupasset.ManifestComplete || evidence.FailureCode != backupasset.FailureProviderSnapshotRewritten {
				t.Fatalf("evidence=%+v err=%v", evidence, err)
			}
		})
	}
}

func TestResticManifestAcceptsDepthFirstSiblingOrdering(t *testing.T) {
	evidence, err := buildResticManifestForTest(t, readResticPublicationFixture(t, "manifest-depth-edge.ndjson"), manifestLimitsForTest())
	if err != nil || evidence.Completeness != backupasset.ManifestComplete || evidence.EntryCount != 4 || evidence.LogicalBytes != 3 || evidence.Digest == "" {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestResticManifestRejectsDuplicateNoncanonicalAndReenteredPaths(t *testing.T) {
	header := manifestHeaderForTest()
	tests := []string{
		header + `{"struct_type":"node","name":"a","path":"/a","type":"file","size":1}` + "\n" + `{"struct_type":"node","name":"a","path":"/a","type":"file","size":1}` + "\n",
		header + `{"struct_type":"node","name":"a","path":"/dir/../a","type":"file","size":1}` + "\n",
		header + `{"struct_type":"node","name":"a","path":"/a","type":"dir"}` + "\n" + `{"struct_type":"node","name":"child","path":"/a/child","type":"file","size":1}` + "\n" + `{"struct_type":"node","name":"a-","path":"/a-","type":"file","size":1}` + "\n" + `{"struct_type":"node","name":"again","path":"/a/again","type":"file","size":1}` + "\n",
	}
	for _, body := range tests {
		evidence, err := buildResticManifestForTest(t, []byte(body), manifestLimitsForTest())
		if err != nil || evidence.Completeness == backupasset.ManifestComplete || evidence.FailureCode != backupasset.FailureManifestPartial {
			t.Fatalf("evidence=%+v err=%v", evidence, err)
		}
	}
}

func TestResticManifestRejectsUnknownRecordAndNodeTypes(t *testing.T) {
	for _, body := range []string{
		manifestHeaderForTest() + `{"struct_type":"future_record"}` + "\n",
		manifestHeaderForTest() + `{"struct_type":"node","name":"future","path":"/future","type":"future","size":1}` + "\n",
	} {
		evidence, err := buildResticManifestForTest(t, []byte(body), manifestLimitsForTest())
		if err != nil || evidence.Completeness == backupasset.ManifestComplete || evidence.FailureCode != backupasset.FailureManifestPartial {
			t.Fatalf("evidence=%+v err=%v", evidence, err)
		}
	}
}

func TestResticManifestKnownHeaderAndNodeRecordsTolerateUnknownFields(t *testing.T) {
	body := bytes.Replace(readResticPublicationFixture(t, "manifest-valid.ndjson"), []byte(`"time":"2026-07-14T03:00:00Z"`), []byte(`"time":"2026-07-14T03:00:00Z","future_header":{"raw":"ignored"}`), 1)
	body = bytes.Replace(body, []byte(`"name":"char"`), []byte(`"name":"char","future_node":["ignored"]`), 1)
	evidence, err := buildResticManifestForTest(t, body, manifestLimitsForTest())
	if err != nil || evidence.Completeness != backupasset.ManifestComplete {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestResticManifestChecksOptionalNumericAndTimeRanges(t *testing.T) {
	valid := readResticPublicationFixture(t, "manifest-valid.ndjson")
	for _, body := range [][]byte{
		bytes.Replace(valid, []byte(`"size":5`), []byte(`"size":-1`), 1),
		bytes.Replace(valid, []byte(`"mtime":"2026-07-14T03:00:00Z"`), []byte(`"mtime":"not-a-time"`), 1),
		bytes.Replace(valid, []byte(`"uid":1`), []byte(`"uid":"wrong"`), 1),
	} {
		evidence, err := buildResticManifestForTest(t, body, manifestLimitsForTest())
		if err != nil || evidence.Completeness == backupasset.ManifestComplete || evidence.FailureCode != backupasset.FailureManifestPartial {
			t.Fatalf("evidence=%+v err=%v", evidence, err)
		}
	}
}

func TestResticManifestCountsOnlyRegularFileLogicalBytes(t *testing.T) {
	evidence, err := buildResticManifestForTest(t, readResticPublicationFixture(t, "manifest-valid.ndjson"), manifestLimitsForTest())
	if err != nil || evidence.EntryCount != 8 || evidence.LogicalBytes != 5 {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestResticManifestRejectsHeaderBeforeCanonicalNodeEncoding(t *testing.T) {
	body := append(readResticPublicationFixture(t, "manifest-rewritten.ndjson"), []byte(`{"struct_type":"node","name":"must-not-hash","path":"/must-not-hash","type":"file","size":99}`+"\n")...)
	evidence, err := buildResticManifestForTest(t, body, manifestLimitsForTest())
	if err != nil || evidence.Completeness == backupasset.ManifestComplete || evidence.EntryCount != 0 || evidence.LogicalBytes != 0 {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestResticManifestFidelityV1DeclaresExactIncludedCommitBoundAndNotExposedFields(t *testing.T) {
	fidelity := ResticManifestFidelityV1()
	if fidelity.Version != 1 || fidelity.Profile != "restic_ls_json_v1" ||
		fidelity.Included != [7]string{"path_name", "native_type", "regular_file_size", "mode", "uid_gid", "mtime_atime_ctime", "inode"} ||
		fidelity.CommitBound != [3]string{"repository_identity", "full_snapshot_id", "required_tags"} ||
		fidelity.NotExposed != [7]string{"link_target", "xattrs", "generic_attributes", "device_link_counts", "content_blob_ids", "subtree_ids", "acl_security_descriptors"} {
		t.Fatalf("fidelity=%+v", fidelity)
	}
	fidelity.Included[0] = "mutated"
	if ResticManifestFidelityV1().Included[0] != "path_name" {
		t.Fatal("fidelity function returned mutable shared state")
	}
}

func TestResticManifestCompleteDigestIsChunkAndJSONFieldOrderIndependent(t *testing.T) {
	valid := readResticPublicationFixture(t, "manifest-depth-edge.ndjson")
	first, err := buildResticManifestForTest(t, valid, manifestLimitsForTest())
	if err != nil || first.Completeness != backupasset.ManifestComplete {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	reordered := bytes.Replace(valid, []byte(`{"struct_type":"node","name":"a-","path":"/a-","type":"file","size":2}`), []byte(`{"size":2,"type":"file","path":"/a-","name":"a-","struct_type":"node"}`), 1)
	second, err := buildResticManifestForTest(t, reordered, manifestLimitsForTest())
	if err != nil || second.Completeness != backupasset.ManifestComplete || first.Digest != second.Digest {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestResticManifestPreservesUTF8WithoutNormalizationOrCaseFolding(t *testing.T) {
	build := func(name string) ResticManifestV1 {
		body := []byte(manifestHeaderForTest() + `{"struct_type":"node","name":"` + name + `","path":"/` + name + `","type":"file","size":1}` + "\n")
		evidence, err := buildResticManifestForTest(t, body, manifestLimitsForTest())
		if err != nil || evidence.Completeness != backupasset.ManifestComplete {
			t.Fatalf("name=%q evidence=%+v err=%v", name, evidence, err)
		}
		return evidence
	}
	if composed, decomposed := build("é"), build("e\u0301"); composed.Digest == decomposed.Digest {
		t.Fatalf("Unicode normalization or case folding changed distinct manifest paths: %q", composed.Digest)
	}
}

func TestResticManifestPartialDomainCannotCollideWithShorterComplete(t *testing.T) {
	prefix := manifestHeaderForTest() + `{"struct_type":"node","name":"file","path":"/file","type":"file","size":1}` + "\n"
	complete, err := buildResticManifestForTest(t, []byte(prefix), manifestLimitsForTest())
	if err != nil || complete.Completeness != backupasset.ManifestComplete {
		t.Fatalf("complete=%+v err=%v", complete, err)
	}
	partial, err := buildResticManifestForTest(t, []byte(prefix+`{"struct_type":"node","name":"broken","path":"/broken","type":"future"}`+"\n"), manifestLimitsForTest())
	if err != nil || partial.Completeness != backupasset.ManifestPartial || partial.Digest == "" || partial.Digest == complete.Digest {
		t.Fatalf("complete=%+v partial=%+v err=%v", complete, partial, err)
	}
	resourceLimits := manifestLimitsForTest()
	resourceLimits.MaxEntries = 1
	resource, err := buildResticManifestForTest(t, []byte(prefix+`{"struct_type":"node","name":"second","path":"/second","type":"file","size":1}`+"\n"), resourceLimits)
	if err != nil || resource.Completeness != backupasset.ManifestPartial || resource.Digest == complete.Digest || resource.Digest == partial.Digest {
		t.Fatalf("resource=%+v complete=%+v partial=%+v err=%v", resource, complete, partial, err)
	}
}

func TestResticManifestUnavailableHasEmptyDigestAndZeroCounts(t *testing.T) {
	evidence, err := buildResticManifestForTest(t, []byte(`{"struct_type":"future"}`+"\n"), manifestLimitsForTest())
	if err != nil || evidence.Completeness != backupasset.ManifestUnavailable || evidence.Digest != "" || evidence.EntryCount != 0 || evidence.LogicalBytes != 0 {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestResticManifestLimitsCancellationTruncationAndNonzeroCloseNeverComplete(t *testing.T) {
	valid := readResticPublicationFixture(t, "manifest-valid.ndjson")
	entryLimits := manifestLimitsForTest()
	entryLimits.MaxEntries = 1
	for _, test := range []struct {
		name       string
		body       []byte
		limits     ManifestLimits
		completion CommandCompletion
		joinErr    error
	}{
		{"entry limit", valid, entryLimits, CommandCompletion{ExitCode: 0, ExitCodeKnown: true}, nil},
		{"truncated", []byte(manifestHeaderForTest() + `{"struct_type":"node","name":"file"`), manifestLimitsForTest(), CommandCompletion{ExitCode: 0, ExitCodeKnown: true}, nil},
		{"nonzero", valid, manifestLimitsForTest(), CommandCompletion{ExitCode: 3, ExitCodeKnown: true}, nil},
		{"close uncertainty", valid, manifestLimitsForTest(), CommandCompletion{}, errors.New("join uncertainty")},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence, err := buildResticManifestWithExecutionForTest(t, test.body, test.limits, test.completion, test.joinErr, nil)
			if err != nil || evidence.Completeness == backupasset.ManifestComplete {
				t.Fatalf("evidence=%+v err=%v", evidence, err)
			}
		})
	}
}

func TestResticManifestReprobesRepositoryIdentityBeforeAndAfterEnumeration(t *testing.T) {
	drift := NativeResticIdentityPrefix + strings.Repeat("e", 64)
	evidence, err := buildResticManifestWithExecutionForTest(t, readResticPublicationFixture(t, "manifest-valid.ndjson"), manifestLimitsForTest(), CommandCompletion{ExitCode: 0, ExitCodeKnown: true}, nil, []string{strings.Repeat("f", 64), strings.Repeat("e", 64)})
	if err != nil || evidence.Completeness == backupasset.ManifestComplete || evidence.FailureCode != backupasset.FailureRepositoryIdentityDrift || strings.Contains(evidence.Digest, drift) {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestResticCatalogProofReusesPublicationCodecWithoutPublicationDeadline(t *testing.T) {
	body := readResticPublicationFixture(t, "manifest-valid.ndjson")
	stream := &fakePublicationExecution{Reader: bytes.NewReader(body), completion: CommandCompletion{ExitCode: 0, ExitCodeKnown: true}}
	transport := &fakeManifestTransport{execution: stream}
	adapter := newManifestResticAdapterForTest(t, transport)
	attempt := manifestAttemptForTest()
	attempt.PointDeadlineAt = attempt.PointDeadlineAt.Add(-48 * time.Hour)
	commit := ResticCommitV1{
		Provider: backupasset.ProviderRestic, RepositoryIdentity: attempt.RepositoryIdentity,
		NativePointID: strings.Repeat("a", 64), CaptureStartedAt: time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC),
		CaptureFinishedAt: time.Date(2026, 7, 14, 3, 0, 2, 0, time.UTC), FilesProcessed: 1, LogicalBytes: 5,
	}
	input := ResticCatalogProofInput{Attempt: attempt, Commit: commit, Limits: manifestLimitsForTest()}
	manifest, err := adapter.BuildCatalogManifest(context.Background(), input)
	if err != nil || manifest.Completeness != backupasset.ManifestComplete || manifest.EntryCount != 8 || manifest.Digest == "" {
		t.Fatalf("Catalog manifest=%+v err=%v", manifest, err)
	}
	request := CatalogReadRequest{
		Provider: backupasset.ProviderRestic, RecoveryPointID: attempt.RecoveryPointID,
		Snapshot: ReadSnapshot{RepositoryID: attempt.RepositoryID, CapabilityRevision: attempt.CapabilityRevision,
			SourceRevision: strings.Repeat("9", 64), Access: attempt.Access},
		Point: PointLocator{Native: commit.NativePointID}, Mode: CatalogProofPublicationManifest,
		Manifest: CatalogManifestProof{ManifestID: strings.Repeat("8", 32), Revision: 1, DigestAlgorithm: "sha256",
			Digest: manifest.Digest, EntryCount: manifest.EntryCount, Completeness: backupasset.ManifestComplete, SourceRevision: strings.Repeat("9", 64)},
		ResticProof: &input, MaxItems: 100,
	}
	stream.Reader = bytes.NewReader(body)
	proved, err := adapter.ProveCatalogManifest(context.Background(), request)
	if err != nil || proved != request.Manifest {
		t.Fatalf("proved manifest=%+v err=%v", proved, err)
	}
}

func buildResticManifestForTest(t *testing.T, body []byte, limits ManifestLimits) (ResticManifestV1, error) {
	return buildResticManifestWithExecutionForTest(t, body, limits, CommandCompletion{ExitCode: 0, ExitCodeKnown: true}, nil, nil)
}

func buildResticManifestWithExecutionForTest(t *testing.T, body []byte, limits ManifestLimits, completion CommandCompletion, joinErr error, probeIDs []string) (ResticManifestV1, error) {
	t.Helper()
	stream := &fakePublicationExecution{Reader: bytes.NewReader(body), completion: completion, joinErr: joinErr}
	transport := &fakeManifestTransport{execution: stream, probeIDs: probeIDs}
	adapter := newManifestResticAdapterForTest(t, transport)
	attempt := manifestAttemptForTest()
	commit := ResticCommitV1{Provider: backupasset.ProviderRestic, RepositoryIdentity: attempt.RepositoryIdentity, NativePointID: strings.Repeat("a", 64), CaptureStartedAt: time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC), CaptureFinishedAt: time.Date(2026, 7, 14, 3, 0, 2, 0, time.UTC), FilesProcessed: 1, LogicalBytes: 1}
	return adapter.BuildManifest(context.Background(), attempt, commit, limits)
}

func newManifestResticAdapterForTest(t *testing.T, transport *fakeManifestTransport) *ResticAdapter {
	t.Helper()
	now := time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)
	material := backupasset.DomainKeyMaterial{Version: 1, Domain: backupasset.KeyDomainCursorSigning, Key: []byte("FAKE_CURSOR_SIGNING_KEY_FOR_TEST_ONLY")}
	keys := staticCursorKeys{active: material, versions: map[int]backupasset.DomainKeyMaterial{1: material}}
	adapter, err := NewResticAdapterWithPublication(transport, transport, NewCursorCodec(keys, func() time.Time { return now }, time.Hour), func() (OperationLimits, error) {
		return testOperationLimits(), nil
	}, func() (backupasset.PublicationConfig, error) {
		return backupasset.PublicationConfig{BackupStreamMaxBytes: 1 << 20}, nil
	}, 100, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func manifestAttemptForTest() ResticAttemptV1 {
	attempt := publicationAttemptForTest()
	attempt.RequiredTags = [2]string{"xirang.link.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "xirang.point.v1.cccccccccccccccccccccccccccccccc"}
	return attempt
}

func manifestLimitsForTest() ManifestLimits {
	return ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 64 << 10, MaxDepth: 64}
}

func manifestHeaderForTest() string {
	return `{"struct_type":"snapshot","id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","time":"2026-07-14T03:00:00Z","tags":["xirang.link.v1.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","xirang.point.v1.cccccccccccccccccccccccccccccccc"]}` + "\n"
}

type fakeManifestTransport struct {
	execution   CommandExecution
	probeIDs    []string
	probeCount  int
	invocations []CommandInvocation
}

func (transport *fakeManifestTransport) Run(_ context.Context, invocation CommandInvocation, _ OperationLimits) (CommandOutput, error) {
	transport.invocations = append(transport.invocations, invocation)
	switch invocation.Operation {
	case OperationResticVersion:
		return CommandOutput{Stdout: []byte("restic 0.19.1 compiled with go1.26\n")}, nil
	case OperationResticConfig:
		id := strings.Repeat("f", 64)
		if len(transport.probeIDs) > 0 {
			id = transport.probeIDs[min(transport.probeCount, len(transport.probeIDs)-1)]
		}
		transport.probeCount++
		return CommandOutput{Stdout: []byte(`{"version":2,"id":"` + id + `"}`)}, nil
	default:
		return CommandOutput{}, errors.New("unexpected manifest transport operation")
	}
}

func (*fakeManifestTransport) Open(context.Context, CommandInvocation, OperationLimits, int64) (ReadHandle, error) {
	return nil, errors.New("unexpected manifest read handle")
}

func (transport *fakeManifestTransport) OpenExecution(_ context.Context, invocation CommandInvocation, _ OperationLimits, _ int64) (CommandExecution, error) {
	transport.invocations = append(transport.invocations, invocation)
	return transport.execution, nil
}
