package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
)

// The frozen V1 controls were emitted by the old writer for a synthetic B2
// point. This test keeps its index, chunks, attempt, and observation unchanged;
// only the commit's private locator bindings are re-signed for a local tree.
// Absolute local locators cause real rclone commands to use the local backend,
// not the inert synthetic B2 section retained in the authenticated attempt.
func TestRclonePortableHistoricalV1RealCommand(t *testing.T) {
	// The V1 writer recorded UTC metadata strings verbatim. Its isolated SSH
	// daemon must use that same timezone; equivalent offset strings change the
	// authenticated historical observation.
	t.Setenv("TZ", "UTC")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	binary := requireRealRclone(t)
	node := startRcloneHTTPSSH(t)
	var trace *rcloneHTTPAcceptanceTransport
	fixture := newRcloneHTTPFixture(t, binary, rcloneHTTPAcceptanceDependencies(t, binary, &trace))
	node.Name = "portable-historical-v1-node"
	node.BasePath = t.TempDir()
	node.BackupDir = "portable-v1-backup"
	if err := fixture.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	// BeforeSave seals the in-memory key; reload the decrypted Node snapshot as
	// the production repository does before handing it to the SSH transport.
	if err := fixture.db.First(&node, node.ID).Error; err != nil {
		t.Fatal(err)
	}

	fixtureRoot := filepath.Join("..", "backupasset", "provider", "testdata", "rclone", "portable-v1")
	readFixture := func(name string) []byte {
		payload, err := os.ReadFile(filepath.Join(fixtureRoot, name))
		if err != nil {
			t.Fatal("read frozen historical V1 fixture")
		}
		return payload
	}
	var metadata struct {
		IdentitySaltHex   string                  `json:"identity_salt_hex"`
		MarkerKeyBase64   string                  `json:"synthetic_marker_key_base64"`
		TargetRemote      string                  `json:"target_remote"`
		SourceFingerprint string                  `json:"source_fingerprint"`
		TaggedAttempt     string                  `json:"tagged_attempt"`
		TaggedCommit      string                  `json:"tagged_commit"`
		ManifestLimits    provider.ManifestLimits `json:"manifest_limits"`
		ChunkMaxBytes     int                     `json:"chunk_max_bytes"`
		ChunkMaxEntries   int                     `json:"chunk_max_entries"`
		SpoolMaxBytes     int64                   `json:"spool_max_bytes"`
		ControlMaxBytes   int64                   `json:"control_payload_max_bytes"`
		LowLevelRetries   int                     `json:"low_level_retries"`
	}
	if err := json.Unmarshal(readFixture("fixture.json"), &metadata); err != nil {
		t.Fatal("decode frozen historical V1 metadata")
	}
	attempt, attemptErr := provider.DecodeRcloneAttemptV1(metadata.TaggedAttempt)
	commit, commitErr := provider.DecodeRcloneCommitV1(metadata.TaggedCommit)
	key, keyErr := base64.StdEncoding.DecodeString(metadata.MarkerKeyBase64)
	salt, saltErr := hex.DecodeString(metadata.IdentitySaltHex)
	config := readFixture("bound-config.conf")
	bound, boundErr := provider.ValidateRcloneBoundConfigV1744(config, metadata.TargetRemote, salt, 64<<10)
	if attemptErr != nil || commitErr != nil || keyErr != nil || saltErr != nil || boundErr != nil ||
		attempt.Portable == nil || commit.Portable == nil || attempt.MinimumRuntimeRevision != 1 || attempt.ManifestSchemaRevision != 1 ||
		provider.ValidateRcloneRuntimeCompatibility(attempt, commit) != nil || !time.Now().UTC().After(attempt.PointDeadlineAt) {
		t.Fatal("frozen V1 evidence is not a valid, already-expired historical point")
	}

	attemptRoot := filepath.Join(t.TempDir(), "points", attempt.Portable.AttemptComponent)
	dataRoot := filepath.Join(attemptRoot, "data")
	controlRoot := filepath.Join(attemptRoot, "control")
	if err := os.MkdirAll(filepath.Join(dataRoot, "dir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dataRoot, "z-empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(controlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dataRoot, "dir", "file.txt")
	if err := os.WriteFile(filePath, readFixture(filepath.Join("data", "dir", "file.txt")), 0o640); err != nil {
		t.Fatal(err)
	}
	// Apply modes after creating children so inherited setgid bits do not alter
	// the frozen canonical modes. The fixture TMPDIR must supply ownership 1000:1000.
	fixedTime := time.Date(2026, 7, 16, 1, 0, 0, 0, time.UTC)
	for _, object := range []struct {
		path string
		mode os.FileMode
		when time.Time
	}{
		{filePath, 0o640, fixedTime.Add(time.Second)},
		{filepath.Join(dataRoot, "dir"), 0o750, fixedTime},
		{filepath.Join(dataRoot, "z-empty"), 0o700, fixedTime},
	} {
		if err := os.Chmod(object.path, object.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(object.path, object.when, object.when); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(object.path)
		if err != nil {
			t.Fatal(err)
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 1000 || owner.Gid != 1000 {
			t.Fatal("frozen V1 requires an isolated setgid TMPDIR owned 1000:1000; refusing to alter its metadata evidence")
		}
	}
	if len(commit.ManifestChunkDigests) != 2 {
		t.Fatal("frozen V1 manifest has an unexpected chunk count")
	}
	for _, name := range []string{"attempt.json", "manifest-index.json", "manifest-000000.jsonl", "manifest-000001.jsonl"} {
		payload := readFixture(filepath.Join("control", name))
		if name == "attempt.json" && rcloneHTTPSHA256(payload) != commit.Portable.AttemptIdentityDigest {
			t.Fatal("frozen V1 attempt marker digest changed")
		}
		if name == "manifest-index.json" && rcloneHTTPSHA256(payload) != commit.ManifestIndexDigest {
			t.Fatal("frozen V1 index digest changed")
		}
		if name == "manifest-000000.jsonl" && rcloneHTTPSHA256(payload) != commit.ManifestChunkDigests[0] ||
			name == "manifest-000001.jsonl" && rcloneHTTPSHA256(payload) != commit.ManifestChunkDigests[1] {
			t.Fatal("frozen V1 chunk digest changed")
		}
		if err := os.WriteFile(filepath.Join(controlRoot, name), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	marker := rebindFrozenV1Marker(t, readFixture(filepath.Join("control", "commit.json")), key, &commit, dataRoot, controlRoot)
	if err := os.WriteFile(filepath.Join(controlRoot, "commit.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	privateLocator := func(value string) provider.RclonePrivateLocator {
		locator, err := provider.NewRclonePrivateLocator(value)
		if err != nil {
			t.Fatal("construct isolated historical V1 locator")
		}
		return locator
	}
	request := provider.RclonePortablePublicationRequest{
		Attempt: attempt, BoundConfig: bound, AttemptRoot: privateLocator(attemptRoot),
		DataRoot: privateLocator(dataRoot), ControlRoot: privateLocator(controlRoot), MarkerKey: key,
		ControlPayloadMaxBytes: metadata.ControlMaxBytes, LowLevelRetries: metadata.LowLevelRetries,
		Runtime: provider.RemoteCommandAccess{Node: node},
		ManifestOptions: provider.RcloneManifestBuildOptions{
			Limits: metadata.ManifestLimits, ChunkMaxBytes: metadata.ChunkMaxBytes,
			ChunkMaxEntries: metadata.ChunkMaxEntries, SpoolMaxBytes: metadata.SpoolMaxBytes,
		},
	}
	strategy := fixture.runtime.RclonePublicationStrategy()
	catalogReader, catalogOK := strategy.(provider.CatalogReader)
	resolver, resolverOK := strategy.(provider.RclonePortableContentResolver)
	if !catalogOK || !resolverOK {
		t.Fatal("production Rclone strategy lacks historical Catalog or content resolution")
	}
	trace.reset("")
	catalogRequest := provider.CatalogReadRequest{
		Provider: backupasset.ProviderRclone, RecoveryPointID: attempt.RecoveryPointID,
		Snapshot: provider.ReadSnapshot{
			RepositoryID: attempt.RepositoryID, CapabilityRevision: int(attempt.CapabilityRevision), SourceRevision: metadata.SourceFingerprint,
			Access: provider.AccessBinding{Provider: backupasset.ProviderRclone, RepositoryID: attempt.RepositoryID},
		},
		Point: provider.PointLocator{Native: "FAKE_EXACT_HISTORICAL_V1_POINT_FOR_TEST_ONLY"}, Mode: provider.CatalogProofPublicationManifest,
		Manifest: provider.CatalogManifestProof{
			ManifestID: strings.Repeat("9", 32), Revision: 1, DigestAlgorithm: "sha256", Digest: commit.ManifestIndexDigest,
			EntryCount: int64(commit.ManifestEntryCount), Completeness: backupasset.ManifestComplete, SourceRevision: metadata.SourceFingerprint,
		},
		RcloneProof: &provider.RcloneCatalogProofInput{
			Reconcile: provider.RcloneReconcileInput{ManifestLimits: metadata.ManifestLimits, PortableRequest: &request}, Commit: commit,
		},
		MaxItems: int(commit.ManifestEntryCount) + 1,
	}
	catalogSession, err := catalogReader.OpenCatalogRead(ctx, catalogRequest)
	if err != nil {
		t.Fatalf("real-command historical V1 Catalog failed: class=%s", rcloneHTTPSafeErrorClass(err))
	}
	page, pageErr := catalogSession.ListCanonical(ctx, provider.PageRequest{Limit: 10})
	proof, proofErr := catalogSession.Finalize(ctx)
	closeErr := catalogSession.Close()
	if pageErr != nil || proofErr != nil || closeErr != nil || len(page.Items) != 3 || !proof.Catalog.Complete ||
		page.Items[1].NormalizedPath != "dir/file.txt" || page.Items[1].ModifiedAt == nil ||
		!page.Items[1].ModifiedAt.Equal(fixedTime.Add(time.Second)) || page.Items[1].Mode != "100640" || page.Items[1].Owner != "1000:1000" {
		t.Fatal("real-command historical V1 Catalog did not retain its frozen projection")
	}
	binding, err := resolver.ResolvePortableContent(ctx, request, commit, "dir/file.txt")
	if err != nil || binding.ManifestVersion != 1 || binding.ValidateSnapshot == nil || binding.Record.NormalizedPath != "dir/file.txt" ||
		binding.Record.ModifiedAt == nil || !binding.PhysicalModTime.Equal(*binding.Record.ModifiedAt) ||
		!binding.PhysicalModTime.Equal(fixedTime.Add(time.Second)) {
		t.Fatalf("real-command historical V1 content binding failed: class=%s", rcloneHTTPSafeErrorClass(err))
	}
	limits, err := provider.NewMetadataOperationLimits(time.Minute, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := provider.NewRcloneAdapter(trace, provider.NewCursorCodec(backupasset.NewKeyring(fixture.db, fixture.clock.Now), fixture.clock.Now, time.Minute), limits, 100, fixture.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	managed := &provider.RcloneManagedPointAccess{
		RecoveryPointID: attempt.RecoveryPointID, AttemptID: attempt.AttemptID, DataLocator: dataRoot,
		ManifestDigest: commit.ManifestIndexDigest, SourceRevision: commit.DestinationObservationDigest,
		Committed: true, ValidateSnapshot: binding.ValidateSnapshot,
	}
	snapshot := provider.ReadSnapshot{
		RepositoryID: attempt.RepositoryID, CapabilityRevision: int(attempt.CapabilityRevision), SourceRevision: commit.DestinationObservationDigest,
		Access: provider.AccessBinding{
			Provider: backupasset.ProviderRclone, RepositoryID: attempt.RepositoryID, TaskID: attempt.TaskID, NodeID: node.ID,
			IdentitySalt: salt, EndpointFacts: []string{"synthetic-v1-local-target"}, Locator: dataRoot, Secret: config,
			AdapterData: provider.RcloneRuntimeAccess{Backend: "local", ConfigSource: provider.RcloneConfigBound, Command: &request.Runtime, ManagedPoint: managed},
		},
	}
	point := provider.PointLocator{Native: "managed:" + attempt.RecoveryPointID + ":" + attempt.AttemptID + ":" + commit.ManifestIndexDigest}
	entry := provider.EntryLocator{Native: "dir/file.txt"}
	stat, err := adapter.StatEntry(ctx, snapshot, point, entry)
	if err != nil || stat.Size != 5 || !stat.ModTime.Equal(binding.PhysicalModTime) {
		t.Fatalf("real-command historical V1 exact stat failed: class=%s", rcloneHTTPSafeErrorClass(err))
	}
	handle, opened, err := adapter.OpenSequential(ctx, snapshot, point, entry, provider.ReadRequest{MaxBytes: 5})
	if err != nil || handle == nil || opened.Size != 5 || !opened.ModTime.Equal(binding.PhysicalModTime) {
		t.Fatalf("real-command historical V1 content open failed: class=%s", rcloneHTTPSafeErrorClass(err))
	}
	payload, readErr := io.ReadAll(handle)
	closeErr = handle.Close()
	if readErr != nil || closeErr != nil || string(payload) != "hello" {
		t.Fatalf("real-command historical V1 content read/close failed: read=%s close=%s", rcloneHTTPSafeErrorClass(readErr), rcloneHTTPSafeErrorClass(closeErr))
	}
	assertRcloneHTTPHistoricalReadTrace(t, trace.snapshot(), attempt.PointDeadlineAt, metadata.ManifestLimits.Timeout)
	if request.Attempt.PointDeadlineAt != attempt.PointDeadlineAt {
		t.Fatal("historical V1 read changed the signed attempt deadline")
	}
}

func rebindFrozenV1Marker(t *testing.T, original, key []byte, commit *provider.RcloneCommitV1, dataRoot, controlRoot string) []byte {
	t.Helper()
	var envelope struct {
		Version        int             `json:"version"`
		Kind           string          `json:"kind"`
		Document       json.RawMessage `json:"document"`
		Authentication string          `json:"authentication"`
	}
	if err := json.Unmarshal(original, &envelope); err != nil || envelope.Version != 1 || envelope.Kind != "commit" || commit.Portable == nil {
		t.Fatal("decode frozen V1 commit envelope")
	}
	originalMAC := hmac.New(sha256.New, key)
	_, _ = io.WriteString(originalMAC, "xirang-rclone-control-v1\ncommit\n")
	_, _ = originalMAC.Write(envelope.Document)
	if rcloneHTTPSHA256(envelope.Document) != commit.Portable.CommitPayloadDigest ||
		!hmac.Equal([]byte(envelope.Authentication), []byte(hex.EncodeToString(originalMAC.Sum(nil)))) ||
		envelope.Authentication != commit.Portable.CommitAuthenticationDigest {
		t.Fatal("frozen V1 commit authentication or payload digest changed")
	}
	locatorDigest := func(locator string) string {
		mac := hmac.New(sha256.New, key)
		_, _ = io.WriteString(mac, "xirang-rclone-private-locator-v1\n"+locator)
		return hex.EncodeToString(mac.Sum(nil))
	}
	for _, replacement := range []struct {
		field string
		old   string
		value string
	}{
		{"control_identity_digest", commit.Portable.ControlIdentityDigest, locatorDigest(controlRoot)},
		{"data_identity_digest", commit.Portable.DataIdentityDigest, locatorDigest(dataRoot)},
	} {
		old := []byte(fmt.Sprintf("\"%s\":\"%s\"", replacement.field, replacement.old))
		value := []byte(fmt.Sprintf("\"%s\":\"%s\"", replacement.field, replacement.value))
		if bytes.Count(envelope.Document, old) != 1 {
			t.Fatal("frozen V1 commit does not contain exactly one expected locator binding")
		}
		envelope.Document = bytes.Replace(envelope.Document, old, value, 1)
		if replacement.field == "control_identity_digest" {
			commit.Portable.ControlIdentityDigest = replacement.value
		} else {
			commit.Portable.DataIdentityDigest = replacement.value
		}
	}
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "xirang-rclone-control-v1\ncommit\n")
	_, _ = mac.Write(envelope.Document)
	envelope.Authentication = hex.EncodeToString(mac.Sum(nil))
	commit.Portable.CommitPayloadDigest = rcloneHTTPSHA256(envelope.Document)
	commit.Portable.CommitAuthenticationDigest = envelope.Authentication
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal("re-sign isolated V1 commit locator binding")
	}
	return payload
}
