package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
)

var errRcloneManifestReadAhead = errors.New("manifest reader consumed the next record too early")

type gatedRcloneManifestReader struct {
	prefix  *bytes.Reader
	suffix  *bytes.Reader
	release func() bool
}

func (reader *gatedRcloneManifestReader) Read(buffer []byte) (int, error) {
	if reader.prefix.Len() > 0 {
		return reader.prefix.Read(buffer)
	}
	if !reader.release() {
		return 0, errRcloneManifestReadAhead
	}
	return reader.suffix.Read(buffer)
}

func rcloneManifestOptionsForTest() RcloneManifestBuildOptions {
	return RcloneManifestBuildOptions{
		Limits:        ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 100, MaxRecordBytes: 64 << 10, MaxDepth: 32},
		ChunkMaxBytes: 64 << 10, ChunkMaxEntries: 2, SpoolMaxBytes: 2 << 20,
		SymlinkTargetReader: func(_ context.Context, physicalPath string, maxBytes int64) ([]byte, error) {
			if physicalPath != "link.rclonelink" || maxBytes < 12 {
				return nil, errors.New("unexpected symlink target request")
			}
			return []byte("dir/file.txt"), nil
		},
	}
}

func TestRcloneManifestCanonicalOrderingChunkingAndEmptyDirectories(t *testing.T) {
	payload, err := os.ReadFile("testdata/rclone/lsjson-tree.json")
	if err != nil {
		t.Fatal(err)
	}
	options := rcloneManifestOptionsForTest()
	manifest, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(payload), options)
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}
	if manifest.EntryCount != 3 || manifest.LogicalBytes != 5 || len(manifest.Chunks) != 2 || manifest.IndexDigest == "" || manifest.ObservationDigest == "" {
		t.Fatalf("manifest facts=%+v", manifest)
	}
	var paths []string
	for _, chunk := range manifest.Chunks {
		if chunk.Digest == "" || chunk.EntryCount == 0 || len(chunk.Encoded) == 0 || chunk.Size != int64(len(chunk.Encoded)) {
			t.Fatalf("invalid chunk: %+v", chunk)
		}
		for _, line := range bytes.Split(bytes.TrimSuffix(chunk.Encoded, []byte("\n")), []byte("\n")) {
			var entry struct {
				Path string `json:"path"`
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(line, &entry); err != nil {
				t.Fatalf("decode canonical entry: %v", err)
			}
			paths = append(paths, entry.Path+":"+entry.Kind)
		}
	}
	if want := []string{"dir:directory", "dir/file.txt:file", "z-empty:directory"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("canonical paths=%v want=%v", paths, want)
	}
	second, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(payload), options)
	if err != nil || !bytes.Equal(second.IndexEncoded, manifest.IndexEncoded) || second.IndexDigest != manifest.IndexDigest {
		t.Fatalf("manifest is not deterministic: err=%v first=%s second=%s", err, manifest.IndexEncoded, second.IndexEncoded)
	}
}

func TestRcloneManifestProcessesRecordsBeforeReadingTheWholeListing(t *testing.T) {
	processedFirst := false
	prefix := `[{"Path":"link.rclonelink","Name":"link.rclonelink","Size":12,"ModTime":"2026-07-16T01:00:00Z","IsDir":false,"Metadata":{"mode":"120000"}},`
	suffix := `{"Path":"z.txt","Name":"z.txt","Size":1,"ModTime":"2026-07-16T01:00:00Z","IsDir":false,"Hashes":{"sha256":"` + strings.Repeat("a", 64) + `"},"Metadata":{"mode":"100600"}}]`
	reader := &gatedRcloneManifestReader{
		prefix: bytes.NewReader([]byte(prefix)), suffix: bytes.NewReader([]byte(suffix)),
		release: func() bool { return processedFirst },
	}
	options := rcloneManifestOptionsForTest()
	options.SymlinkTargetReader = func(_ context.Context, physicalPath string, maxBytes int64) ([]byte, error) {
		if physicalPath != "link.rclonelink" || maxBytes != 12 {
			return nil, errors.New("unexpected symlink target request")
		}
		processedFirst = true
		return []byte("dir/file.txt"), nil
	}
	manifest, err := BuildRcloneManifestV1(context.Background(), reader, options)
	if err != nil {
		t.Fatalf("streaming manifest: %v", err)
	}
	if !processedFirst || manifest.EntryCount != 2 {
		t.Fatalf("streaming manifest did not process both records: processed=%v manifest=%+v", processedFirst, manifest)
	}
}

func TestRcloneManifestExternalSortMergesRunsRejectsDuplicatesAndCleansSpool(t *testing.T) {
	spoolDirectory := t.TempDir()
	t.Setenv("TMPDIR", spoolDirectory)
	entries := make([]rcloneLSJSONEntry, 40)
	for index := range entries {
		name := fmt.Sprintf("file-%03d.txt", len(entries)-index)
		entries[index] = rcloneLSJSONEntry{
			Path: name, Name: name, Size: 1, ModTime: "2026-07-16T01:00:00Z",
			Hashes: map[string]string{"sha256": strings.Repeat("a", 64)}, Metadata: map[string]string{"mode": "100600"},
		}
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	options := rcloneManifestOptionsForTest()
	options.ChunkMaxEntries = 1
	options.ChunkMaxBytes = 512
	manifest, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(payload), options)
	if err != nil {
		t.Fatalf("external sort manifest: %v", err)
	}
	paths := make([]string, 0, len(entries))
	for _, chunk := range manifest.Chunks {
		var entry struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(chunk.Encoded), &entry); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, entry.Path)
	}
	if len(paths) != len(entries) || !sort.StringsAreSorted(paths) {
		t.Fatalf("external sort paths=%v", paths)
	}
	assertNoRcloneManifestSpoolFiles(t, spoolDirectory)

	duplicate := append([]rcloneLSJSONEntry(nil), entries[:20]...)
	duplicate = append(duplicate, entries[0])
	duplicatePayload, err := json.Marshal(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(duplicatePayload), options); !errors.Is(err, ErrRcloneManifestPathUnsafe) {
		t.Fatalf("cross-run duplicate error=%v", err)
	}
	assertNoRcloneManifestSpoolFiles(t, spoolDirectory)
}

func assertNoRcloneManifestSpoolFiles(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, "xirang-rclone-manifest-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("manifest spool files were not cleaned: %v", matches)
	}
}

func TestRcloneManifestAcceptsProvenEmptySource(t *testing.T) {
	payload, err := os.ReadFile("testdata/rclone/lsjson-empty.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(payload), rcloneManifestOptionsForTest())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.EntryCount != 0 || manifest.LogicalBytes != 0 || len(manifest.Chunks) != 0 || manifest.IndexDigest == "" ||
		manifest.Fidelity.HashFidelity != backupasset.RcloneHashNotEvaluated {
		t.Fatalf("empty manifest=%+v", manifest)
	}
}

func TestRcloneManifestPreservesExactSymlinkTargetAndRejectsWireCollision(t *testing.T) {
	payload, err := os.ReadFile("testdata/rclone/lsjson-symlink.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(payload), rcloneManifestOptionsForTest())
	if err != nil {
		t.Fatal(err)
	}
	var entry struct {
		Path         string `json:"path"`
		Kind         string `json:"kind"`
		TargetBase64 string `json:"symlink_target_base64"`
		TargetDigest string `json:"symlink_target_digest"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(manifest.Chunks[0].Encoded), &entry); err != nil {
		t.Fatal(err)
	}
	target, err := base64.StdEncoding.DecodeString(entry.TargetBase64)
	if err != nil || entry.Path != "link" || entry.Kind != "symlink" || string(target) != "dir/file.txt" || entry.TargetDigest == "" {
		t.Fatalf("canonical symlink=%+v target=%q err=%v", entry, target, err)
	}
	collision, err := os.ReadFile("testdata/rclone/lsjson-collision.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(collision), rcloneManifestOptionsForTest()); !errors.Is(err, ErrRcloneManifestPathUnsafe) {
		t.Fatalf("literal .rclonelink collision error=%v", err)
	}
}

func TestRcloneManifestRejectsSpecialInvalidAndCollidingPaths(t *testing.T) {
	base := `[{"Path":"item","Name":"item","Size":1,"ModTime":"2026-07-16T01:00:00Z","IsDir":false,"Hashes":{"sha256":"` + strings.Repeat("a", 64) + `"},"Metadata":{"mode":"%s"}}]`
	for _, mode := range []string{"010600", "020600", "060600", "140600"} {
		if _, err := BuildRcloneManifestV1(context.Background(), strings.NewReader(strings.Replace(base, "%s", mode, 1)), rcloneManifestOptionsForTest()); !errors.Is(err, ErrRcloneManifestSpecialFile) {
			t.Fatalf("special mode %s error=%v", mode, err)
		}
	}

	invalidUTF8 := append([]byte(`[{"Path":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`","Name":"x","Size":1,"ModTime":"2026-07-16T01:00:00Z","IsDir":false}]`)...)
	if utf8.Valid(invalidUTF8) {
		t.Fatal("invalid UTF-8 fixture became valid")
	}
	if _, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(invalidUTF8), rcloneManifestOptionsForTest()); !errors.Is(err, ErrRcloneManifestPathUnsafe) {
		t.Fatalf("invalid UTF-8 path error=%v", err)
	}

	decomposed := "e\u0301.txt"
	payload := `[{"Path":"` + decomposed + `","Name":"` + decomposed + `","Size":1,"ModTime":"2026-07-16T01:00:00Z","IsDir":false,"Hashes":{"sha256":"` + strings.Repeat("a", 64) + `"},"Metadata":{"mode":"100600"}}]`
	if _, err := BuildRcloneManifestV1(context.Background(), strings.NewReader(payload), rcloneManifestOptionsForTest()); !errors.Is(err, ErrRcloneManifestPathUnsafe) {
		t.Fatalf("non-NFC path error=%v", err)
	}
}

func TestRcloneManifestEnforcesEveryResourceLimit(t *testing.T) {
	payload, err := os.ReadFile("testdata/rclone/lsjson-tree.json")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*RcloneManifestBuildOptions){
		"entries": func(value *RcloneManifestBuildOptions) { value.Limits.MaxEntries = 1 },
		"depth":   func(value *RcloneManifestBuildOptions) { value.Limits.MaxDepth = 1 },
		"record":  func(value *RcloneManifestBuildOptions) { value.Limits.MaxRecordBytes = 32 },
		"output":  func(value *RcloneManifestBuildOptions) { value.Limits.MaxBytes = 32 },
		"spool":   func(value *RcloneManifestBuildOptions) { value.SpoolMaxBytes = 32 },
		"chunk":   func(value *RcloneManifestBuildOptions) { value.ChunkMaxBytes = 32 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			options := rcloneManifestOptionsForTest()
			mutate(&options)
			if _, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(payload), options); !errors.Is(err, ErrRcloneManifestLimitExceeded) {
				t.Fatalf("limit error=%v", err)
			}
		})
	}
}

func TestRcloneManifestFidelityRequiresFullBytesForWeakHashes(t *testing.T) {
	strongPayload, err := os.ReadFile("testdata/rclone/lsjson-tree.json")
	if err != nil {
		t.Fatal(err)
	}
	strong, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(strongPayload), rcloneManifestOptionsForTest())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveRcloneManifestHashFidelity(strong, RcloneFullByteProof{}); err != nil || got != backupasset.RcloneHashProviderStrongChecksum {
		t.Fatalf("strong fidelity=%q err=%v", got, err)
	}

	weakPayload, err := os.ReadFile("testdata/rclone/lsjson-weak-hash.json")
	if err != nil {
		t.Fatal(err)
	}
	weak, err := BuildRcloneManifestV1(context.Background(), bytes.NewReader(weakPayload), rcloneManifestOptionsForTest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveRcloneManifestHashFidelity(weak, RcloneFullByteProof{}); !errors.Is(err, ErrRcloneManifestFullByteProofRequired) {
		t.Fatalf("weak manifest committed without full-byte proof: %v", err)
	}
	proof := RcloneFullByteProof{SourceDigest: strings.Repeat("1", 64), DestinationDigest: strings.Repeat("1", 64), VerifiedBytes: 1048576, Complete: true}
	if got, err := ResolveRcloneManifestHashFidelity(weak, proof); err != nil || got != backupasset.RcloneHashDownloadVerifiedBytes {
		t.Fatalf("download fidelity=%q err=%v", got, err)
	}
	proof.VerifiedBytes--
	if _, err := ResolveRcloneManifestHashFidelity(weak, proof); !errors.Is(err, ErrRcloneManifestFullByteProofRequired) {
		t.Fatalf("partial full-byte proof accepted: %v", err)
	}
}

func TestRclonePortableObservationsRequireStableSourceAndDestination(t *testing.T) {
	base := RcloneObservationV1{Digest: strings.Repeat("a", 64), EntryCount: 3, LogicalBytes: 5}
	if err := ValidateRclonePortableObservations(base, base, base, base); err != nil {
		t.Fatalf("stable observations rejected: %v", err)
	}
	changed := base
	changed.Digest = strings.Repeat("b", 64)
	if err := ValidateRclonePortableObservations(base, changed, base, base); !errors.Is(err, ErrRcloneSourceDrift) {
		t.Fatalf("source drift error=%v", err)
	}
	if err := ValidateRclonePortableObservations(base, base, base, changed); !errors.Is(err, ErrRcloneDestinationUnstable) {
		t.Fatalf("destination drift error=%v", err)
	}
}
