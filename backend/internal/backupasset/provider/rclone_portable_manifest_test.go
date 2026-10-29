package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
)

const portableV2HelloSHA256 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func portableV2TestObservation(t *testing.T, listing string, options RcloneManifestBuildOptions, destination bool) RcloneManifestBundle {
	t.Helper()
	var (
		manifest RcloneManifestBundle
		err      error
	)
	if destination {
		manifest, err = buildRcloneManifestV1(context.Background(), strings.NewReader(listing), options, true)
	} else {
		manifest, err = BuildRcloneManifestV1(context.Background(), strings.NewReader(listing), options)
	}
	if err != nil {
		t.Fatalf("build observation: %v", err)
	}
	return manifest
}

func portableV2TestFileListing(modTime, digest, metadata string) string {
	hashes := ""
	if digest != "" {
		hashes = `,"Hashes":{"sha256":"` + digest + `"}`
	}
	if metadata != "" {
		metadata = `,"Metadata":` + metadata
	}
	return `[{"Path":"file.txt","Name":"file.txt","Size":5,"ModTime":"` + modTime + `","IsDir":false` + hashes + metadata + `}]`
}

func portableV2TestComplete(t *testing.T, source, destination RcloneManifestBundle, options RcloneManifestBuildOptions) RclonePortableManifestV2 {
	t.Helper()
	manifest, err := buildRclonePortableManifestV2(context.Background(), source, destination, options)
	if err != nil {
		t.Fatalf("merge Portable observations: %v", err)
	}
	verification := RclonePortableByteVerification{}
	if manifest.Fidelity.RequiresFullByteVerification {
		verification = RclonePortableByteVerification{Complete: true, VerifiedBytes: manifest.LogicalBytes}
	}
	manifest, err = completeRclonePortableManifestV2(manifest, verification, options)
	if err != nil {
		t.Fatalf("complete Portable manifest: %v", err)
	}
	return manifest
}

func portableV2TestCommit(t *testing.T, manifest RclonePortableManifestV2) RcloneCommitV1 {
	t.Helper()
	commit := validRcloneCommitForTest(backupasset.PublicationVersionedPrefix)
	commit.MinimumRuntimeRevision = 2
	commit.ManifestIndexDigest = manifest.IndexDigest
	commit.ManifestEntryCount = manifest.EntryCount
	commit.LogicalBytes = manifest.LogicalBytes
	commit.SourceObservationDigest = manifest.SourceObservationDigest
	commit.DestinationObservationDigest = manifest.DestinationObservationDigest
	commit.ManifestChunkDigests = make([]string, len(manifest.Chunks))
	for index, chunk := range manifest.Chunks {
		commit.ManifestChunkDigests[index] = chunk.Digest
	}
	fidelity, err := json.Marshal(manifest.Fidelity)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := json.Marshal(manifest.ContentProof)
	if err != nil {
		t.Fatal(err)
	}
	commit.FidelityEvidenceDigest = sha256Hex(fidelity)
	commit.ContentProofDigest = sha256Hex(proof)
	commit.Portable.HashEvidenceDigest = commit.ContentProofDigest
	commit.Portable.DownloadVerifiedBytes = manifest.ContentProof.VerifiedBytes
	return commit
}

func portableV2TestRead(
	ctx context.Context,
	manifest RclonePortableManifestV2,
	commit RcloneCommitV1,
	options RcloneManifestBuildOptions,
	visit func(rcloneCanonicalManifestEntry, rcloneCanonicalManifestEntry) error,
) (rclonePortableManifestIndexV2, error) {
	return readRclonePortableManifestV2(ctx, manifest.IndexEncoded, func(_ context.Context, ordinal int, size int64) ([]byte, error) {
		if ordinal < 0 || ordinal >= len(manifest.Chunks) || size != manifest.Chunks[ordinal].Size {
			return nil, fmt.Errorf("unexpected manifest chunk request")
		}
		return manifest.Chunks[ordinal].Encoded, nil
	}, options, commit, visit)
}

func TestRclonePortableManifestV2DifferentObservationsRequireDownloadAndKeepSourceMetadata(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	sourceListing := portableV2TestFileListing("2026-07-16T01:00:01.123456789Z", portableV2HelloSHA256,
		`{"mode":"100640","uid":"1000","gid":"1000","mtime":"2026-07-16T01:00:01.123456789Z"}`)
	destinationListing := portableV2TestFileListing("2026-07-16T01:00:01Z", "", "")
	source := portableV2TestObservation(t, sourceListing, options, false)
	destination := portableV2TestObservation(t, destinationListing, options, true)
	pending, err := buildRclonePortableManifestV2(context.Background(), source, destination, options)
	if err != nil {
		t.Fatal(err)
	}
	if pending.SourceObservationDigest != source.ObservationDigest || pending.DestinationObservationDigest != destination.ObservationDigest ||
		pending.SourceObservationDigest == pending.DestinationObservationDigest || !pending.Fidelity.RequiresFullByteVerification ||
		pending.Fidelity.HashFidelity != backupasset.RcloneHashDownloadVerifiedBytes || pending.Fidelity.ModTimePreserved ||
		!reflect.DeepEqual(pending.Fidelity.SourceMetadataFields, []string{"gid", "mode", "mtime", "uid"}) ||
		!reflect.DeepEqual(pending.Fidelity.DestinationMetadataFields, []string{}) ||
		!reflect.DeepEqual(pending.Fidelity.PreservedMetadataFields, []string{}) {
		t.Fatalf("unexpected cross-backend fidelity: %+v", pending.Fidelity)
	}
	for _, proof := range []RclonePortableByteVerification{{}, {Complete: true, VerifiedBytes: 4}, {VerifiedBytes: 5}} {
		if _, err := completeRclonePortableManifestV2(pending, proof, options); !errors.Is(err, ErrRcloneManifestFullByteProofRequired) {
			t.Fatalf("partial download proof accepted: %+v err=%v", proof, err)
		}
	}
	manifest, err := completeRclonePortableManifestV2(pending, RclonePortableByteVerification{Complete: true, VerifiedBytes: 5}, options)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.ContentProof.Complete || manifest.ContentProof.VerifiedBytes != 5 || manifest.IndexDigest == source.IndexDigest {
		t.Fatalf("unexpected V2 proof or index: %+v", manifest.ContentProof)
	}
	commit := portableV2TestCommit(t, manifest)
	var gotSource, gotDestination rcloneCanonicalManifestEntry
	index, err := portableV2TestRead(context.Background(), manifest, commit, options, func(source, destination rcloneCanonicalManifestEntry) error {
		gotSource, gotDestination = source, destination
		return nil
	})
	if err != nil {
		t.Fatalf("read authenticated V2 manifest: %v", err)
	}
	if index.GeneratorVersion != "v2" || gotSource.ModTime != "2026-07-16T01:00:01.123456789Z" ||
		gotSource.SHA256 != portableV2HelloSHA256 || gotSource.Metadata["mode"] != "100640" ||
		gotDestination.ModTime != "2026-07-16T01:00:01Z" || gotDestination.SHA256 != "" || len(gotDestination.Metadata) != 0 {
		t.Fatalf("wrong V2 projections: source=%+v destination=%+v", gotSource, gotDestination)
	}
	records, err := rcloneCatalogRecords([]rcloneCanonicalManifestEntry{gotSource}, nil)
	if err != nil || len(records) != 1 || records[0].ModifiedAt == nil ||
		records[0].ModifiedAt.Format("2006-01-02T15:04:05.999999999Z07:00") != "2026-07-16T01:00:01.123456789Z" ||
		records[0].Mode != "100640" || records[0].Owner != "1000:1000" || records[0].Fingerprint != portableV2HelloSHA256 {
		t.Fatalf("wrong source Catalog projection: records=%+v err=%v", records, err)
	}
	// The rule is symmetric: a missing source hash cannot be repaired by a
	// destination hash, even when the destination has a strong checksum.
	reversed, err := buildRclonePortableManifestV2(context.Background(), destination, source, options)
	if err != nil || !reversed.Fidelity.RequiresFullByteVerification {
		t.Fatalf("missing source hash did not require download: fidelity=%+v err=%v", reversed.Fidelity, err)
	}
}

func TestRclonePortableManifestV2StrongEmptyAndDirectoryFidelity(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	tests := []struct {
		name             string
		source           string
		destination      string
		wantHash         backupasset.RcloneHashFidelity
		wantDirectories  bool
		wantTimes        bool
		wantSourceFields []string
		wantDestFields   []string
		wantPreserved    []string
	}{
		{
			name: "strong",
			source: portableV2TestFileListing("2026-07-16T01:00:01Z", portableV2HelloSHA256,
				`{"gid":"1000","mode":"100640","mtime":"source","uid":"1000"}`),
			destination: portableV2TestFileListing("2026-07-16T01:00:01Z", portableV2HelloSHA256,
				`{"gid":"1000","mode":"100640","uid":"2000"}`),
			wantHash: backupasset.RcloneHashProviderStrongChecksum, wantTimes: true,
			wantSourceFields: []string{"gid", "mode", "mtime", "uid"}, wantDestFields: []string{"gid", "mode", "uid"},
			wantPreserved: []string{"gid", "mode"},
		},
		{name: "empty", source: `[]`, destination: `[]`, wantHash: backupasset.RcloneHashNotEvaluated,
			wantSourceFields: []string{}, wantDestFields: []string{}, wantPreserved: []string{}},
		{name: "directory", source: `[{"Path":"empty","Name":"empty","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true}]`,
			destination: `[{"Path":"empty","Name":"empty","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true}]`,
			wantHash:    backupasset.RcloneHashNotEvaluated, wantDirectories: true, wantTimes: true,
			wantSourceFields: []string{}, wantDestFields: []string{}, wantPreserved: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := portableV2TestObservation(t, test.source, options, false)
			destination := portableV2TestObservation(t, test.destination, options, true)
			manifest := portableV2TestComplete(t, source, destination, options)
			fidelity := manifest.Fidelity
			if fidelity.HashFidelity != test.wantHash || fidelity.RequiresFullByteVerification ||
				fidelity.EmptyDirectoriesPreserved != test.wantDirectories || fidelity.ModTimePreserved != test.wantTimes ||
				fidelity.SymlinkTargetsPreserved || fidelity.HardlinkTopologyPreserved ||
				!reflect.DeepEqual(fidelity.SourceMetadataFields, test.wantSourceFields) ||
				!reflect.DeepEqual(fidelity.DestinationMetadataFields, test.wantDestFields) ||
				!reflect.DeepEqual(fidelity.PreservedMetadataFields, test.wantPreserved) ||
				!manifest.ContentProof.Complete || manifest.ContentProof.VerifiedBytes != 0 {
				t.Fatalf("unexpected fidelity=%+v proof=%+v", fidelity, manifest.ContentProof)
			}
			if _, err := portableV2TestRead(context.Background(), manifest, portableV2TestCommit(t, manifest), options, nil); err != nil {
				t.Fatalf("read V2 manifest: %v", err)
			}
		})
	}
}

func TestRclonePortableManifestV2ReducesMetadataAcrossChunks(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	options.ChunkMaxEntries = 1
	sourceListing := `[{"Path":"dir","Name":"dir","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true,"Metadata":{"gid":"1000","uid":"1000"}},{"Path":"dir/file","Name":"file","Size":5,"ModTime":"2026-07-16T01:00:01Z","IsDir":false,"Hashes":{"sha256":"` + portableV2HelloSHA256 + `"}}]`
	destinationListing := strings.Replace(sourceListing, `","IsDir":false,"Hashes":`, `","IsDir":false,"Metadata":{"gid":"1000"},"Hashes":`, 1)
	source := portableV2TestObservation(t, sourceListing, options, false)
	destination := portableV2TestObservation(t, destinationListing, options, true)
	manifest := portableV2TestComplete(t, source, destination, options)
	if len(manifest.Chunks) != 2 || !manifest.Fidelity.EmptyDirectoriesPreserved ||
		!reflect.DeepEqual(manifest.Fidelity.SourceMetadataFields, []string{"gid", "uid"}) ||
		!reflect.DeepEqual(manifest.Fidelity.DestinationMetadataFields, []string{"gid", "uid"}) ||
		!reflect.DeepEqual(manifest.Fidelity.PreservedMetadataFields, []string{"uid"}) {
		t.Fatalf("cross-chunk metadata fidelity=%+v", manifest.Fidelity)
	}
	var paths []string
	_, err := portableV2TestRead(context.Background(), manifest, portableV2TestCommit(t, manifest), options,
		func(source, _ rcloneCanonicalManifestEntry) error {
			paths = append(paths, source.Path)
			return nil
		})
	if err != nil || !reflect.DeepEqual(paths, []string{"dir", "dir/file"}) {
		t.Fatalf("cross-chunk projections=%v err=%v", paths, err)
	}
}

func TestRclonePortableManifestV2LexicalSiblingsKeepPendingDirectories(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	listing := `[
		{"Path":"a","Name":"a","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true},
		{"Path":"a-else","Name":"a-else","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true},
		{"Path":"a-else-keeper","Name":"a-else-keeper","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":false},
		{"Path":"a-else/file","Name":"file","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":false},
		{"Path":"a/b","Name":"b","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true},
		{"Path":"a/b-else","Name":"b-else","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true},
		{"Path":"a/b-else-keeper","Name":"b-else-keeper","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":false},
		{"Path":"a/b-else/file","Name":"file","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":false},
		{"Path":"a/b/file","Name":"file","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":false},
		{"Path":"a/file","Name":"file","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":false}
	]`
	observation := portableV2TestObservation(t, listing, options, false)
	manifest := portableV2TestComplete(t, observation, observation, options)
	var paths []string
	_, err := portableV2TestRead(context.Background(), manifest, portableV2TestCommit(t, manifest), options,
		func(source, _ rcloneCanonicalManifestEntry) error {
			paths = append(paths, source.Path)
			return nil
		})
	want := []string{"a", "a-else", "a-else-keeper", "a-else/file", "a/b", "a/b-else",
		"a/b-else-keeper", "a/b-else/file", "a/b/file", "a/file"}
	if err != nil || !reflect.DeepEqual(paths, want) {
		t.Fatalf("lexically interleaved tree paths=%v err=%v", paths, err)
	}

	missingParent := `[
		{"Path":"a","Name":"a","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true},
		{"Path":"a-else","Name":"a-else","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true},
		{"Path":"a/missing/file","Name":"file","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":false}
	]`
	orphan := portableV2TestObservation(t, missingParent, options, false)
	if _, err := buildRclonePortableManifestV2(context.Background(), orphan, orphan, options); !errors.Is(err, ErrRcloneManifestPathUnsafe) {
		t.Fatalf("missing nested directory accepted: %v", err)
	}
}

func TestRclonePortableManifestV2RejectsMismatchedTreesAndHashes(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	sourceListing := portableV2TestFileListing("2026-07-16T01:00:01Z", portableV2HelloSHA256, "")
	source := portableV2TestObservation(t, sourceListing, options, false)
	tests := []struct {
		name    string
		listing string
	}{
		{"missing", `[]`},
		{"different_path", strings.ReplaceAll(sourceListing, "file.txt", "other.txt")},
		{"size", strings.Replace(sourceListing, `"Size":5`, `"Size":6`, 1)},
		{"kind", `[{"Path":"file.txt","Name":"file.txt","Size":0,"ModTime":"2026-07-16T01:00:01Z","IsDir":true}]`},
		{"strong_hash", portableV2TestFileListing("2026-07-16T01:00:01Z", strings.Repeat("a", 64), "")},
		{"extra", strings.TrimSuffix(sourceListing, "]") + `,{"Path":"other.txt","Name":"other.txt","Size":0,"ModTime":"2026-07-16T01:00:01Z","IsDir":false}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := portableV2TestObservation(t, test.listing, options, true)
			if _, err := buildRclonePortableManifestV2(context.Background(), source, destination, options); err == nil {
				t.Fatal("different trees or content hashes accepted")
			}
		})
	}
	orphan := `[{"Path":"missing/file","Name":"file","Size":0,"ModTime":"2026-07-16T01:00:01Z","IsDir":false}]`
	incomplete := portableV2TestObservation(t, orphan, options, false)
	if _, err := buildRclonePortableManifestV2(context.Background(), incomplete, incomplete, options); err == nil {
		t.Fatal("manifest without its parent directory accepted")
	}
}

func TestRclonePortableManifestV2SymlinkRequiresExactTargetAndRejectsWrongStrongHash(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	options.SymlinkTargetReader = func(_ context.Context, name string, size int64) ([]byte, error) {
		if name != "link.rclonelink" || size != 12 {
			return nil, errors.New("unexpected target request")
		}
		return []byte("dir/file.txt"), nil
	}
	sourceListing := `[{"Path":"link.rclonelink","Name":"link.rclonelink","Size":12,"ModTime":"2026-07-16T01:00:01Z","IsDir":false,"Metadata":{"mode":"120000"}}]`
	destinationListing := strings.Replace(sourceListing, `"mode":"120000"`, `"mode":"100644"`, 1)
	source := portableV2TestObservation(t, sourceListing, options, false)
	destination := portableV2TestObservation(t, destinationListing, options, true)
	manifest := portableV2TestComplete(t, source, destination, options)
	if !manifest.Fidelity.SymlinkTargetsPreserved || !manifest.Fidelity.RequiresFullByteVerification ||
		manifest.ContentProof.VerifiedBytes != 12 {
		t.Fatalf("symlink fidelity=%+v proof=%+v", manifest.Fidelity, manifest.ContentProof)
	}
	if _, err := portableV2TestRead(context.Background(), manifest, portableV2TestCommit(t, manifest), options, nil); err != nil {
		t.Fatalf("read symlink manifest: %v", err)
	}
	otherOptions := options
	otherOptions.SymlinkTargetReader = func(context.Context, string, int64) ([]byte, error) { return []byte("other-target"), nil }
	changed := portableV2TestObservation(t, destinationListing, otherOptions, true)
	if _, err := buildRclonePortableManifestV2(context.Background(), source, changed, options); err == nil {
		t.Fatal("different symlink targets accepted")
	}
	wrongHash := strings.Replace(sourceListing, `,"Metadata"`, `,"Hashes":{"sha256":"`+strings.Repeat("a", 64)+`"},"Metadata"`, 1)
	wrongSource := portableV2TestObservation(t, wrongHash, options, false)
	wrongDestination := portableV2TestObservation(t, strings.Replace(wrongHash, `"mode":"120000"`, `"mode":"100644"`, 1), options, true)
	if _, err := buildRclonePortableManifestV2(context.Background(), wrongSource, wrongDestination, options); err == nil {
		t.Fatal("matching incorrect symlink hashes accepted")
	}
}

func TestRclonePortableManifestV2LimitsAndCancellation(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	source := portableV2TestObservation(t, portableV2TestFileListing("2026-07-16T01:00:01Z", portableV2HelloSHA256, ""), options, false)
	destination := portableV2TestObservation(t, portableV2TestFileListing("2026-07-16T01:00:01Z", "", ""), options, true)
	pending, err := buildRclonePortableManifestV2(context.Background(), source, destination, options)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := completeRclonePortableManifestV2(pending, RclonePortableByteVerification{Complete: true, VerifiedBytes: 5}, options)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(len(manifest.IndexEncoded))
	for _, chunk := range manifest.Chunks {
		total += chunk.Size
	}
	for _, limit := range []string{"bytes", "spool"} {
		t.Run(limit, func(t *testing.T) {
			bounded := options
			if limit == "bytes" {
				bounded.Limits.MaxBytes = total
			} else {
				bounded.SpoolMaxBytes = total
			}
			if _, err := completeRclonePortableManifestV2(pending, RclonePortableByteVerification{Complete: true, VerifiedBytes: 5}, bounded); err != nil {
				t.Fatalf("exact %s budget rejected: %v", limit, err)
			}
			if _, err := portableV2TestRead(context.Background(), manifest, portableV2TestCommit(t, manifest), bounded, nil); err != nil {
				t.Fatalf("reader rejected exact %s budget: %v", limit, err)
			}
			if limit == "bytes" {
				bounded.Limits.MaxBytes = total - 1
			} else {
				bounded.SpoolMaxBytes = total - 1
			}
			if _, err := completeRclonePortableManifestV2(pending, RclonePortableByteVerification{Complete: true, VerifiedBytes: 5}, bounded); !errors.Is(err, ErrRcloneManifestLimitExceeded) {
				t.Fatalf("index was not counted in %s budget: %v", limit, err)
			}
			if _, err := portableV2TestRead(context.Background(), manifest, portableV2TestCommit(t, manifest), bounded, nil); !errors.Is(err, ErrRcloneManifestLimitExceeded) {
				t.Fatalf("reader accepted %s budget: %v", limit, err)
			}
		})
	}
	bounded := options
	bounded.Limits.MaxRecordBytes = len(source.Chunks[0].Encoded)
	if _, err := buildRclonePortableManifestV2(context.Background(), source, destination, bounded); !errors.Is(err, ErrRcloneManifestLimitExceeded) {
		t.Fatalf("V2 record limit: %v", err)
	}
	bounded = options
	bounded.Limits.MaxEntries = 1
	twoFiles := `[{"Path":"a","Name":"a","Size":0,"ModTime":"2026-07-16T01:00:01Z","IsDir":false},{"Path":"b","Name":"b","Size":0,"ModTime":"2026-07-16T01:00:01Z","IsDir":false}]`
	two := portableV2TestObservation(t, twoFiles, options, false)
	if _, err := buildRclonePortableManifestV2(context.Background(), two, two, bounded); !errors.Is(err, ErrRcloneManifestLimitExceeded) {
		t.Fatalf("entry limit: %v", err)
	}
	bounded = options
	bounded.Limits.MaxDepth = 1
	nested := `[{"Path":"dir","Name":"dir","Size":0,"ModTime":"2026-07-16T01:00:01Z","IsDir":true},{"Path":"dir/file","Name":"file","Size":0,"ModTime":"2026-07-16T01:00:01Z","IsDir":false}]`
	deep := portableV2TestObservation(t, nested, options, false)
	if _, err := buildRclonePortableManifestV2(context.Background(), deep, deep, bounded); err == nil {
		t.Fatal("depth limit was not enforced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buildRclonePortableManifestV2(ctx, source, destination, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled merge: %v", err)
	}
	if _, err := portableV2TestRead(ctx, manifest, portableV2TestCommit(t, manifest), options, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reader: %v", err)
	}
	expired, cancelDeadline := context.WithTimeout(context.Background(), 0)
	defer cancelDeadline()
	if _, err := buildRclonePortableManifestV2(expired, source, destination, options); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired merge: %v", err)
	}
	if _, err := portableV2TestRead(expired, manifest, portableV2TestCommit(t, manifest), options, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired reader: %v", err)
	}
}

func portableV2TestReplaceChunk(t *testing.T, manifest RclonePortableManifestV2, payload []byte) RclonePortableManifestV2 {
	t.Helper()
	manifest.Chunks = append([]RcloneManifestChunk(nil), manifest.Chunks...)
	manifest.Chunks[0].Encoded = payload
	manifest.Chunks[0].Size = int64(len(payload))
	manifest.Chunks[0].Digest = sha256Hex(payload)
	var index rclonePortableManifestIndexV2
	if err := json.Unmarshal(manifest.IndexEncoded, &index); err != nil {
		t.Fatal(err)
	}
	index.Chunks[0].Size = int64(len(payload))
	index.Chunks[0].Digest = manifest.Chunks[0].Digest
	var err error
	manifest.IndexEncoded, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	manifest.IndexDigest = sha256Hex(manifest.IndexEncoded)
	return manifest
}

func TestRclonePortableManifestV2RejectsMalformedRecordsEvenWhenChunkIsResigned(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	listing := portableV2TestFileListing("2026-07-16T01:00:01Z", portableV2HelloSHA256, "")
	observation := portableV2TestObservation(t, listing, options, false)
	manifest := portableV2TestComplete(t, observation, observation, options)
	line := strings.TrimSuffix(string(manifest.Chunks[0].Encoded), "\n")
	sourceAt := strings.Index(line, `,"source":`)
	destinationAt := strings.Index(line, `,"destination":`)
	if sourceAt < 0 || destinationAt < 0 {
		t.Fatal("fixture has no source or destination")
	}
	tests := map[string]string{
		"missing_source":      line[:sourceAt] + line[destinationAt:],
		"null_source":         line[:sourceAt] + `,"source":null` + line[destinationAt:],
		"missing_destination": line[:destinationAt] + `}`,
		"null_destination":    line[:destinationAt] + `,"destination":null}`,
		"unknown":             strings.TrimSuffix(line, "}") + `,"extra":true}`,
		"duplicate":           strings.Replace(line, `"version":2`, `"version":2,"version":2`, 1),
		"wrong_version":       strings.Replace(line, `"version":2`, `"version":1`, 1),
		"null_size":           strings.Replace(line, `"size":5`, `"size":null`, 1),
		"missing_time":        strings.Replace(line, `"mod_time":"2026-07-16T01:00:01Z",`, "", 1),
		"null_metadata":       strings.Replace(line, `},"destination":`, `,"metadata":null},"destination":`, 1),
		"null_optional":       strings.Replace(line, `,"source":`, `,"symlink_target_digest":null,"source":`, 1),
		"wrong_physical_path": strings.Replace(line, `"physical_path":"file.txt"`, `"physical_path":"other.txt"`, 1),
		"wrong_hash":          strings.Replace(line, portableV2HelloSHA256, strings.Repeat("a", 64), 1),
		"trailing":            line + ` {}`,
	}
	for name, altered := range tests {
		t.Run(name, func(t *testing.T) {
			changed := portableV2TestReplaceChunk(t, manifest, []byte(altered+"\n"))
			if _, err := portableV2TestRead(context.Background(), changed, portableV2TestCommit(t, changed), options, nil); err == nil {
				t.Fatal("malformed or mismatched record accepted")
			}
		})
	}
}

func TestRclonePortableManifestV2RejectsForgedIndexAndEvidence(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	listing := portableV2TestFileListing("2026-07-16T01:00:01Z", portableV2HelloSHA256, "")
	observation := portableV2TestObservation(t, listing, options, false)
	manifest := portableV2TestComplete(t, observation, observation, options)
	for _, test := range []struct {
		name   string
		mutate func(*rclonePortableManifestIndexV2)
	}{
		{"wrong_generator", func(index *rclonePortableManifestIndexV2) { index.GeneratorVersion = "v1" }},
		{"wrong_version", func(index *rclonePortableManifestIndexV2) { index.Version = 1 }},
		{"chunk_ordinal", func(index *rclonePortableManifestIndexV2) { index.Chunks[0].Ordinal = 1 }},
		{"chunk_count", func(index *rclonePortableManifestIndexV2) { index.Chunks[0].EntryCount++ }},
		{"null_chunks", func(index *rclonePortableManifestIndexV2) { index.Chunks = nil }},
		{"entry_count", func(index *rclonePortableManifestIndexV2) { index.EntryCount++ }},
		{"wrong_fidelity", func(index *rclonePortableManifestIndexV2) { index.Fidelity.ModTimePreserved = false }},
		{"null_fidelity_fields", func(index *rclonePortableManifestIndexV2) { index.Fidelity.SourceMetadataFields = nil }},
		{"wrong_proof", func(index *rclonePortableManifestIndexV2) { index.ContentProof.VerifiedBytes = 5 }},
		{"incomplete_proof", func(index *rclonePortableManifestIndexV2) { index.ContentProof.Complete = false }},
		{"wrong_projection", func(index *rclonePortableManifestIndexV2) { index.SourceObservationDigest = strings.Repeat("f", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var index rclonePortableManifestIndexV2
			if err := json.Unmarshal(manifest.IndexEncoded, &index); err != nil {
				t.Fatal(err)
			}
			test.mutate(&index)
			changed := manifest
			var err error
			changed.IndexEncoded, err = json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			changed.IndexDigest = sha256Hex(changed.IndexEncoded)
			changed.Fidelity = index.Fidelity
			changed.ContentProof = index.ContentProof
			changed.SourceObservationDigest = index.SourceObservationDigest
			changed.EntryCount = index.EntryCount
			commit := portableV2TestCommit(t, changed)
			if _, err := portableV2TestRead(context.Background(), changed, commit, options, nil); err == nil {
				t.Fatal("forged index or evidence accepted")
			}
		})
	}
	indexText := string(manifest.IndexEncoded)
	for name, altered := range map[string]string{
		"missing":   strings.Replace(indexText, `,"generator_version":"v2"`, "", 1),
		"null":      strings.Replace(indexText, `"entry_count":1`, `"entry_count":null`, 1),
		"duplicate": strings.Replace(indexText, `"version":2`, `"version":2,"version":2`, 1),
		"unknown":   strings.Replace(indexText, `"version":2`, `"version":2,"future":true`, 1),
		"trailing":  indexText + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			changed := manifest
			changed.IndexEncoded = []byte(altered)
			changed.IndexDigest = sha256Hex(changed.IndexEncoded)
			if _, err := portableV2TestRead(context.Background(), changed, portableV2TestCommit(t, changed), options, nil); err == nil {
				t.Fatal("malformed index accepted")
			}
		})
	}
}

func TestRclonePortableManifestV2RejectsCommitEvidenceMismatch(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	listing := portableV2TestFileListing("2026-07-16T01:00:01Z", portableV2HelloSHA256, "")
	observation := portableV2TestObservation(t, listing, options, false)
	manifest := portableV2TestComplete(t, observation, observation, options)
	for _, test := range []struct {
		name   string
		mutate func(*RcloneCommitV1)
	}{
		{"index", func(commit *RcloneCommitV1) { commit.ManifestIndexDigest = strings.Repeat("f", 64) }},
		{"chunk", func(commit *RcloneCommitV1) { commit.ManifestChunkDigests[0] = strings.Repeat("f", 64) }},
		{"fidelity", func(commit *RcloneCommitV1) { commit.FidelityEvidenceDigest = strings.Repeat("f", 64) }},
		{"content_proof", func(commit *RcloneCommitV1) { commit.ContentProofDigest = strings.Repeat("f", 64) }},
		{"hash_evidence", func(commit *RcloneCommitV1) { commit.Portable.HashEvidenceDigest = strings.Repeat("f", 64) }},
		{"destination", func(commit *RcloneCommitV1) { commit.DestinationObservationDigest = strings.Repeat("f", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			commit := portableV2TestCommit(t, manifest)
			test.mutate(&commit)
			if _, err := portableV2TestRead(context.Background(), manifest, commit, options, nil); err == nil {
				t.Fatal("mismatched commit evidence accepted")
			}
		})
	}
}

func TestRclonePortableManifestV2DestinationObservationAcceptsOnlyTargetLinkEncoding(t *testing.T) {
	request := portablePublicationRequestForTest(t, false)
	listing := `[{"Path":"link.rclonelink","Name":"link.rclonelink","Size":12,"ModTime":"2026-07-16T01:00:01Z","IsDir":false}]`
	for _, metadata := range []string{"", `,"Metadata":{"mode":"100644"}`} {
		t.Run(fmt.Sprintf("metadata=%s", metadata), func(t *testing.T) {
			payload := strings.Replace(listing, `}]`, metadata+`}]`, 1)
			commands := &rclonePreflightCommandFake{listPayload: []byte(payload), payload: []byte("dir/file.txt")}
			remote, err := NewCommandRclonePortableRemote(commands, &fakeRcloneStagedPayloadTransport{}, func() (OperationLimits, error) {
				return testOperationLimits(), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := remote.Observe(context.Background(), request, request.DataRoot)
			if err != nil || !manifest.Fidelity.SymlinkTargetsPreserved || manifest.EntryCount != 1 {
				t.Fatalf("destination link observation: %+v err=%v", manifest.Fidelity, err)
			}
			if _, err := remote.Observe(context.Background(), request, request.Source); !errors.Is(err, ErrRcloneManifestPathUnsafe) {
				t.Fatalf("source reserved suffix accepted: %v", err)
			}
			if _, err := BuildRcloneManifestV1(context.Background(), strings.NewReader(payload), request.ManifestOptions); !errors.Is(err, ErrRcloneManifestPathUnsafe) {
				t.Fatalf("Native/source builder accepted reserved suffix: %v", err)
			}
			same := request
			same.Source = request.DataRoot
			if _, err := remote.Observe(context.Background(), same, same.Source); !errors.Is(err, ErrRcloneManifestPathUnsafe) {
				t.Fatalf("source/target collision accepted reserved suffix: %v", err)
			}
		})
	}
	special := strings.Replace(listing, `}]`, `,"Metadata":{"mode":"010600"}}]`, 1)
	if _, err := buildRcloneManifestV1(context.Background(), strings.NewReader(special), request.ManifestOptions, true); !errors.Is(err, ErrRcloneManifestSpecialFile) {
		t.Fatalf("special destination object accepted as link: %v", err)
	}
}

func TestRclonePortableManifestV2NormalizesOnlyNegativeDirectorySizes(t *testing.T) {
	options := rcloneManifestOptionsForTest()
	zero := `[{"Path":"empty","Name":"empty","Size":0,"ModTime":"2026-07-16T01:00:00Z","IsDir":true}]`
	negative := strings.Replace(zero, `"Size":0`, `"Size":-1`, 1)
	one := portableV2TestObservation(t, zero, options, false)
	two := portableV2TestObservation(t, negative, options, false)
	if one.IndexDigest != two.IndexDigest || one.ObservationDigest != two.ObservationDigest ||
		!bytes.Equal(one.IndexEncoded, two.IndexEncoded) || !bytes.Equal(one.Chunks[0].Encoded, two.Chunks[0].Encoded) {
		t.Fatal("directory normalization changed canonical V1 bytes")
	}
	file := strings.Replace(negative, `"IsDir":true`, `"IsDir":false`, 1)
	if _, err := BuildRcloneManifestV1(context.Background(), strings.NewReader(file), options); !errors.Is(err, ErrRcloneManifestPathUnsafe) {
		t.Fatalf("negative regular file size accepted: %v", err)
	}
}

func TestRclonePortableManifestV2PreservesFixedHistoricalV1WriterBytes(t *testing.T) {
	listing, err := os.ReadFile("testdata/rclone/portable-v1/source-lsjson.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(listing), rcloneManifestOptionsForTest())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.IndexDigest != "c4f5248c188eae6186949636b9a4f244cf0b57556b6085cc3e9e58ec015b504d" ||
		manifest.ObservationDigest != "98e2f08631870cd7ca018da195b24308bf3cb52d11bfb4d4793fdbcdaa3d3724" {
		t.Fatalf("historical V1 digest changed: index=%s observation=%s", manifest.IndexDigest, manifest.ObservationDigest)
	}
	for index, chunk := range manifest.Chunks {
		fixed, err := os.ReadFile(fmt.Sprintf("testdata/rclone/portable-v1/control/manifest-%06d.jsonl", index))
		if err != nil || !bytes.Equal(chunk.Encoded, fixed) {
			t.Fatalf("historical V1 chunk %d changed: %v", index, err)
		}
	}
	fixedIndex, err := os.ReadFile("testdata/rclone/portable-v1/control/manifest-index.json")
	if err != nil || !bytes.Equal(manifest.IndexEncoded, fixedIndex) {
		t.Fatalf("historical V1 index changed: %v", err)
	}
}
