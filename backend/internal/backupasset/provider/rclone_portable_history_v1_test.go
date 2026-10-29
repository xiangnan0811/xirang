package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

type portableV1FixedFixture struct {
	request           RclonePortablePublicationRequest
	commit            RcloneCommitV1
	remote            *fakeRclonePortableRemote
	backend           string
	sourceFingerprint string
}

func portableV1FixedFixtureForTest(t *testing.T) portableV1FixedFixture {
	t.Helper()
	payload, err := os.ReadFile("testdata/rclone/portable-v1/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		IdentitySaltHex   string `json:"identity_salt_hex"`
		MarkerKeyBase64   string `json:"synthetic_marker_key_base64"`
		AttemptRoot       string `json:"attempt_root"`
		DataRoot          string `json:"data_root"`
		ControlRoot       string `json:"control_root"`
		TargetRemote      string `json:"target_remote"`
		Backend           string `json:"backend"`
		SourceFingerprint string `json:"source_fingerprint"`
		TaggedAttempt     string `json:"tagged_attempt"`
		TaggedCommit      string `json:"tagged_commit"`
	}
	if err := json.Unmarshal(payload, &metadata); err != nil {
		t.Fatal(err)
	}
	attempt, err := DecodeRcloneAttemptV1(metadata.TaggedAttempt)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := DecodeRcloneCommitV1(metadata.TaggedCommit)
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(metadata.MarkerKeyBase64)
	if err != nil {
		t.Fatal(err)
	}
	salt, err := hex.DecodeString(metadata.IdentitySaltHex)
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile("testdata/rclone/portable-v1/bound-config.conf")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := ValidateRcloneBoundConfigV1744(config, metadata.TargetRemote, salt, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	request := portablePublicationRequestForTest(t, false)
	request.Attempt = attempt
	request.MarkerKey = key
	request.BoundConfig = bound
	request.AttemptRoot = mustRclonePrivateLocatorForTest(t, metadata.AttemptRoot)
	request.DataRoot = mustRclonePrivateLocatorForTest(t, metadata.DataRoot)
	request.ControlRoot = mustRclonePrivateLocatorForTest(t, metadata.ControlRoot)
	request.Manifest = RcloneManifestBundle{}
	remote := &fakeRclonePortableRemote{presence: RcloneAttemptPresent, controls: make(map[string][]byte)}
	names := []string{"attempt.json", "commit.json", "manifest-index.json"}
	for index := range commit.ManifestChunkDigests {
		names = append(names, fmt.Sprintf("manifest-%06d.jsonl", index))
	}
	for _, name := range names {
		remote.controls[name], err = os.ReadFile("testdata/rclone/portable-v1/control/" + name)
		if err != nil {
			t.Fatal(err)
		}
	}
	return portableV1FixedFixture{request: request, commit: commit, remote: remote,
		backend: metadata.Backend, sourceFingerprint: metadata.SourceFingerprint}
}

func TestRclonePortableHistoricalV1ReadsReconcilesAndDeletesExactPoint(t *testing.T) {
	fixture := portableV1FixedFixtureForTest(t)
	publisher := NewRclonePortablePublisher(fixture.remote, func(time.Duration) {}, portableNowForTest)
	got, err := publisher.Reconcile(context.Background(), fixture.request)
	if err != nil || !reflect.DeepEqual(got, fixture.commit) {
		t.Fatalf("restart V1 reconcile got=%+v err=%v", got, err)
	}
	listing, err := os.ReadFile("testdata/rclone/portable-v1/source-lsjson.json")
	if err != nil {
		t.Fatal(err)
	}
	withSource := fixture.request
	withSource.Manifest, err = BuildRcloneManifestV1(context.Background(), bytes.NewReader(listing), withSource.ManifestOptions)
	if err != nil {
		t.Fatal(err)
	}
	withSource.Manifest.IndexDigest = strings.Repeat("f", 64)
	if _, err := publisher.Reconcile(context.Background(), withSource); err != nil {
		t.Fatalf("reconcile should bind source observation rather than prepared index: %v", err)
	}
	fixture.remote.mutations = nil
	if _, err := publisher.Publish(context.Background(), withSource); err == nil || len(fixture.remote.mutations) != 0 {
		t.Fatalf("historical preparing attempt was retransferred: mutations=%v err=%v", fixture.remote.mutations, err)
	}
	strategy, err := NewRclonePublicationStrategy(publisher, NewRcloneNativePublisher(&rcloneNativeDataPlaneFake{}, portableNowForTest))
	if err != nil {
		t.Fatal(err)
	}
	_, err = strategy.Prepare(context.Background(), PublicationPrepareRequest{
		Attempt:     NewRclonePublicationAttempt(withSource.Attempt),
		RcloneInput: &RclonePublicationInput{ManifestLimits: withSource.ManifestOptions.Limits, PortableRequest: &withSource},
	})
	if err == nil {
		t.Fatal("historical attempt was prepared for a new transfer")
	}
	fixture.remote.reads = nil
	readRequest := CatalogReadRequest{
		Provider: backupasset.ProviderRclone, RecoveryPointID: fixture.request.Attempt.RecoveryPointID,
		Snapshot: ReadSnapshot{RepositoryID: fixture.commit.RepositoryID, CapabilityRevision: int(fixture.request.Attempt.CapabilityRevision),
			SourceRevision: fixture.sourceFingerprint, Access: AccessBinding{Provider: backupasset.ProviderRclone, RepositoryID: fixture.commit.RepositoryID}},
		Point: PointLocator{Native: "FAKE_EXACT_PORTABLE_POINT_FOR_TEST_ONLY"}, Mode: CatalogProofPublicationManifest,
		Manifest: CatalogManifestProof{ManifestID: strings.Repeat("9", 32), Revision: 1, DigestAlgorithm: "sha256", Digest: fixture.commit.ManifestIndexDigest,
			EntryCount: int64(fixture.commit.ManifestEntryCount), Completeness: backupasset.ManifestComplete, SourceRevision: fixture.sourceFingerprint},
		RcloneProof: &RcloneCatalogProofInput{Reconcile: RcloneReconcileInput{ManifestLimits: fixture.request.ManifestOptions.Limits, PortableRequest: &fixture.request}, Commit: fixture.commit},
		MaxItems:    int(fixture.commit.ManifestEntryCount) + 1,
	}
	session, err := strategy.OpenCatalogRead(context.Background(), readRequest)
	if err != nil {
		t.Fatalf("open fixed V1 Catalog: %v", err)
	}
	page, err := session.ListCanonical(context.Background(), PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 3 || page.Items[1].NormalizedPath != "dir/file.txt" ||
		page.Items[1].Fingerprint != portableV2HelloSHA256 || page.Items[1].ModifiedAt == nil ||
		page.Items[1].ModifiedAt.Format(time.RFC3339Nano) != "2026-07-16T01:00:01Z" {
		t.Fatalf("fixed V1 Catalog records=%+v err=%v", page.Items, err)
	}
	if _, err := session.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.remote.reads["commit.json"] != 1 || fixture.remote.reads["manifest-index.json"] != 1 ||
		fixture.remote.reads["manifest-000000.jsonl"] != 1 || fixture.remote.reads["manifest-000001.jsonl"] != 1 {
		t.Fatalf("V1 controls reread during Catalog: %v", fixture.remote.reads)
	}
	deleteTransport := &rcloneDeletionTransport{
		prefixPresent: true, liveBackend: fixture.backend,
		controlPayloads: map[string][]byte{
			fixture.request.ControlRoot.value + "/attempt.json": fixture.remote.controls["attempt.json"],
			fixture.request.ControlRoot.value + "/commit.json":  fixture.remote.controls["commit.json"],
		},
	}
	access := RclonePrefixDeletionAccess{
		Prefix: fixture.request.AttemptRoot, MarkerDigest: fixture.sourceFingerprint, ExpectedBackend: fixture.backend,
		ExpectedRootIdentity: fixture.request.Attempt.ManagedRootIdentityDigest, ConfigDigest: fixture.request.Attempt.ConfigDigest,
		MarkerKey: fixture.request.MarkerKey, Attempt: fixture.request.Attempt, Commit: fixture.commit,
		ExpectedAttemptRoot: fixture.request.AttemptRoot.value, Command: &fixture.request.Runtime,
	}
	deleteRequest := DeletePointRequest{
		Snapshot: ReadSnapshot{RepositoryID: fixture.commit.RepositoryID, CapabilityRevision: int(fixture.request.Attempt.CapabilityRevision),
			SourceRevision: fixture.sourceFingerprint, Access: AccessBinding{Provider: backupasset.ProviderRclone,
				RepositoryID: fixture.commit.RepositoryID, Secret: fixture.request.BoundConfig.ExactBytes(), AdapterData: access}},
		Point: PointLocator{Native: fixture.request.AttemptRoot.value}, ExpectedSourceRevision: fixture.sourceFingerprint,
		OperationID: strings.Repeat("e", 32),
	}
	result, err := newRclonePrefixPointDeleterForTest(t, deleteTransport).DeletePoint(context.Background(), deleteRequest)
	if err != nil || result.Outcome != DeletePointDeleted || deleteTransport.deleteCalls != 1 ||
		deleteTransport.deleted.value != fixture.request.AttemptRoot.value {
		t.Fatalf("fixed V1 exact deletion result=%+v err=%v", result, err)
	}
	access.Commit.MinimumRuntimeRevision = 2
	deleteRequest.Snapshot.Access.AdapterData = access
	blocked := &rcloneDeletionTransport{prefixPresent: true, liveBackend: fixture.backend}
	if _, err := newRclonePrefixPointDeleterForTest(t, blocked).DeletePoint(context.Background(), deleteRequest); err == nil || blocked.deleteCalls != 0 {
		t.Fatalf("deletion accepted mismatched attempt/commit versions: calls=%d err=%v", blocked.deleteCalls, err)
	}
}

func portableV1TestResignCommit(t *testing.T, fixture *portableV1FixedFixture) {
	t.Helper()
	attemptJSON, err := json.Marshal(rcloneAttemptToWire(fixture.request.Attempt))
	if err != nil {
		t.Fatal(err)
	}
	attemptPayload, _, _, err := encodeRcloneAuthenticatedControl("attempt", attemptJSON, fixture.request.MarkerKey)
	if err != nil {
		t.Fatal(err)
	}
	fixture.commit.Portable = cloneRclonePortableCommit(fixture.commit.Portable)
	fixture.commit.Portable.AttemptIdentityDigest = sha256Hex(attemptPayload)
	marker, digest, authentication, err := encodeRcloneCommitMarker(fixture.commit, fixture.request.MarkerKey)
	if err != nil {
		t.Fatal(err)
	}
	fixture.commit.Portable.CommitPayloadDigest = digest
	fixture.commit.Portable.CommitAuthenticationDigest = authentication
	fixture.remote.controls["attempt.json"] = attemptPayload
	fixture.remote.controls["commit.json"] = marker
}

func TestRclonePortableHistoricalV1RejectsSchemaCrossoversAndV2Fields(t *testing.T) {
	t.Run("v2_attempt_v1_index", func(t *testing.T) {
		fixture := portableV1FixedFixtureForTest(t)
		fixture.request.Attempt.MinimumRuntimeRevision = 2
		fixture.request.Attempt.ManifestSchemaRevision = 2
		fixture.commit.MinimumRuntimeRevision = 2
		portableV1TestResignCommit(t, &fixture)
		if _, err := NewRclonePortablePublisher(fixture.remote, nil, portableNowForTest).Reconcile(context.Background(), fixture.request); err == nil {
			t.Fatal("V2 attempt accepted V1 index")
		}
	})
	t.Run("v1_attempt_v2_index", func(t *testing.T) {
		fixture := portableV1FixedFixtureForTest(t)
		listing, err := os.ReadFile("testdata/rclone/portable-v1/source-lsjson.json")
		if err != nil {
			t.Fatal(err)
		}
		observation, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(listing), fixture.request.ManifestOptions)
		if err != nil {
			t.Fatal(err)
		}
		v2 := portableV2TestComplete(t, observation, observation, fixture.request.ManifestOptions)
		fixture.commit.ManifestIndexDigest = v2.IndexDigest
		fixture.commit.ManifestChunkDigests = make([]string, len(v2.Chunks))
		for index, chunk := range v2.Chunks {
			fixture.commit.ManifestChunkDigests[index] = chunk.Digest
			fixture.remote.controls[fmt.Sprintf("manifest-%06d.jsonl", index)] = chunk.Encoded
		}
		fixture.remote.controls["manifest-index.json"] = v2.IndexEncoded
		fidelity, err := json.Marshal(v2.Fidelity)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := json.Marshal(v2.ContentProof)
		if err != nil {
			t.Fatal(err)
		}
		fixture.commit.FidelityEvidenceDigest = sha256Hex(fidelity)
		fixture.commit.ContentProofDigest = sha256Hex(proof)
		fixture.commit.Portable.HashEvidenceDigest = fixture.commit.ContentProofDigest
		portableV1TestResignCommit(t, &fixture)
		if _, err := NewRclonePortablePublisher(fixture.remote, nil, portableNowForTest).Reconcile(context.Background(), fixture.request); err == nil {
			t.Fatal("V1 attempt accepted V2 index")
		}
	})
	t.Run("v1_extra_v2_field", func(t *testing.T) {
		fixture := portableV1FixedFixtureForTest(t)
		index := string(fixture.remote.controls["manifest-index.json"])
		index = strings.TrimSuffix(index, "}") + `,"source_observation_digest":"` + fixture.commit.SourceObservationDigest + `"}`
		fixture.remote.controls["manifest-index.json"] = []byte(index)
		fixture.commit.ManifestIndexDigest = sha256Hex([]byte(index))
		portableV1TestResignCommit(t, &fixture)
		if _, err := NewRclonePortablePublisher(fixture.remote, nil, portableNowForTest).Reconcile(context.Background(), fixture.request); err == nil {
			t.Fatal("V1 index accepted V2 member")
		}
	})
	t.Run("v1_entry_with_v2_field", func(t *testing.T) {
		fixture := portableV1FixedFixtureForTest(t)
		chunk := fixture.remote.controls["manifest-000000.jsonl"]
		end := bytes.IndexByte(chunk, '\n')
		if end < 0 {
			t.Fatal("fixed V1 chunk has no first record")
		}
		first := strings.TrimSuffix(string(chunk[:end]), "}") + `,"destination":{}}` + "\n"
		changed := append([]byte(first), chunk[end+1:]...)
		fixture.remote.controls["manifest-000000.jsonl"] = changed
		var index rcloneCatalogManifestIndexV1
		if err := json.Unmarshal(fixture.remote.controls["manifest-index.json"], &index); err != nil {
			t.Fatal(err)
		}
		index.Chunks[0].Size = int64(len(changed))
		index.Chunks[0].Digest = sha256Hex(changed)
		encoded, err := json.Marshal(index)
		if err != nil {
			t.Fatal(err)
		}
		fixture.remote.controls["manifest-index.json"] = encoded
		fixture.commit.ManifestIndexDigest = sha256Hex(encoded)
		fixture.commit.ManifestChunkDigests[0] = sha256Hex(changed)
		portableV1TestResignCommit(t, &fixture)
		if _, err := NewRclonePortablePublisher(fixture.remote, nil, portableNowForTest).Reconcile(context.Background(), fixture.request); err == nil {
			t.Fatal("V1 entry accepted a V2 member")
		}
	})
}
