package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestInboxScansFixedNoFollowPackageAndReturnsSanitizedReceipt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	publicKey, manifestPayload, bundle := writeInboxCandidate(t, root, "candidate-one", now)
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(root) })
	inbox, err := NewInbox(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := collectInboxCandidates(t, context.Background(), inbox, TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("unexpected inbox candidates: %+v", candidates)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestPayload, &manifest); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := bundleFingerprint(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := InboxReceipt{
		SchemaVersion: 1, SourceKind: manifest.SourceKind, SourceID: manifest.SourceID, Version: manifest.Version,
		ManifestDigest: SHA256Hex(manifestPayload), SigningKeyFingerprint: SHA256Hex(publicKey),
		BundleFingerprint: fingerprint, BundleSHA256: SHA256Hex(bundle), Capabilities: manifest.Capabilities, VerifiedAt: now,
	}
	registration := RegisterCandidateRequest{SchemaVersion: 1, Receipt: candidates[0].Receipt}
	payload, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRegisterCandidateRequest(payload)
	if err != nil || decoded.SchemaVersion != 1 || !reflect.DeepEqual(decoded.Receipt, want) {
		t.Fatalf("registration decoded=%+v want=%+v err=%v", decoded, want, err)
	}
	lower := strings.ToLower(string(payload))
	for _, forbidden := range []string{
		"candidate-one", root, "manifest.json", "bundle.tar", "models/model.dat", manifest.Signature,
		"FAKE_BUNDLE_CONTENT_FOR_TEST_ONLY", `"path"`, `"content"`, `"signature"`,
		`"url"`, `"credential"`, `"raw_output"`, `"bundle_bytes"`, `"worker_id"`, `"attempt_id"`, `"fence"`,
	} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Fatalf("sanitized registration leaked %q: %s", forbidden, payload)
		}
	}
	t.Run("builtin source kind", func(t *testing.T) {
		builtin := registration
		builtin.Receipt.SourceKind = "builtin"
		builtinPayload, err := json.Marshal(builtin)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeRegisterCandidateRequest(builtinPayload)
		if err != nil || !reflect.DeepEqual(decoded, builtin) {
			t.Fatalf("builtin registration decoded=%+v want=%+v err=%v", decoded, builtin, err)
		}
	})

	replaceOnce := func(t *testing.T, old, replacement string) []byte {
		t.Helper()
		if strings.Count(string(payload), old) != 1 {
			t.Fatalf("expected one %q in valid registration", old)
		}
		return []byte(strings.Replace(string(payload), old, replacement, 1))
	}
	verifiedJSON, err := json.Marshal(now)
	if err != nil {
		t.Fatal(err)
	}
	jsonMutations := []struct {
		name, old, replacement string
	}{
		{"unknown top-level", `"receipt":`, `"unexpected":true,"receipt":`},
		{"unknown receipt", `"receipt":{`, `"receipt":{"unexpected":true,`},
		{"unknown capability", `"capability":"image.ocr"`, `"unexpected":true,"capability":"image.ocr"`},
		{"URL", `"receipt":{`, `"receipt":{"url":"https://example.invalid/bundle",`},
		{"credential", `"receipt":{`, `"receipt":{"credential":"FAKE_TOKEN_FOR_TEST_ONLY",`},
		{"raw output", `"receipt":{`, `"receipt":{"raw_output":"FAKE_OUTPUT_FOR_TEST_ONLY",`},
		{"duplicate top-level", `"receipt":`, `"schema_version":1,"receipt":`},
		{"duplicate receipt", `"receipt":{"schema_version":1,`, `"receipt":{"schema_version":1,"schema_version":1,`},
		{"duplicate capability", `"capability":"image.ocr"`, `"capability":"image.ocr","capability":"image.ocr"`},
		{"missing verified time", `,"verified_at":` + string(verifiedJSON), ""},
		{"invalid UTF-8", `"source_id":"offline.default"`, "\"source_id\":\"\xff\""},
	}
	for _, mutation := range jsonMutations {
		t.Run(mutation.name, func(t *testing.T) {
			invalid := replaceOnce(t, mutation.old, mutation.replacement)
			if _, err := DecodeRegisterCandidateRequest(invalid); !errors.Is(err, ErrProtocolInvalid) {
				t.Fatalf("DecodeRegisterCandidateRequest error=%v, want invalid protocol", err)
			}
		})
	}
	t.Run("trailing JSON", func(t *testing.T) {
		invalid := append(append([]byte(nil), payload...), []byte(` {}`)...)
		if _, err := DecodeRegisterCandidateRequest(invalid); !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("DecodeRegisterCandidateRequest error=%v, want invalid protocol", err)
		}
	})
	t.Run("truncated JSON", func(t *testing.T) {
		if _, err := DecodeRegisterCandidateRequest(payload[:len(payload)-1]); !errors.Is(err, ErrProtocolInvalid) {
			t.Fatalf("DecodeRegisterCandidateRequest error=%v, want invalid protocol", err)
		}
	})

	invalidRegistrations := []struct {
		name   string
		mutate func(*RegisterCandidateRequest)
	}{
		{"unsupported request schema", func(value *RegisterCandidateRequest) { value.SchemaVersion = 2 }},
		{"unsupported receipt schema", func(value *RegisterCandidateRequest) { value.Receipt.SchemaVersion = 2 }},
		{"unknown source kind", func(value *RegisterCandidateRequest) { value.Receipt.SourceKind = "https" }},
		{"URL source ID", func(value *RegisterCandidateRequest) { value.Receipt.SourceID = "https://example.invalid/bundle" }},
		{"oversized source ID", func(value *RegisterCandidateRequest) { value.Receipt.SourceID = strings.Repeat("a", 129) }},
		{"non-semantic version", func(value *RegisterCandidateRequest) { value.Receipt.Version = "latest" }},
		{"uppercase manifest digest", func(value *RegisterCandidateRequest) { value.Receipt.ManifestDigest = strings.Repeat("A", 64) }},
		{"uppercase signing key fingerprint", func(value *RegisterCandidateRequest) { value.Receipt.SigningKeyFingerprint = strings.Repeat("B", 64) }},
		{"uppercase bundle fingerprint", func(value *RegisterCandidateRequest) { value.Receipt.BundleFingerprint = strings.Repeat("C", 64) }},
		{"uppercase bundle digest", func(value *RegisterCandidateRequest) { value.Receipt.BundleSHA256 = strings.Repeat("D", 64) }},
		{"zero verified time", func(value *RegisterCandidateRequest) { value.Receipt.VerifiedAt = time.Time{} }},
		{"non-UTC verified time", func(value *RegisterCandidateRequest) {
			value.Receipt.VerifiedAt = now.In(time.FixedZone("non-UTC", 3600))
		}},
	}
	for _, testCase := range invalidRegistrations {
		t.Run(testCase.name, func(t *testing.T) {
			invalid := registration
			testCase.mutate(&invalid)
			invalidPayload, err := json.Marshal(invalid)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeRegisterCandidateRequest(invalidPayload); !errors.Is(err, ErrProtocolInvalid) {
				t.Fatalf("DecodeRegisterCandidateRequest error=%v, want invalid protocol", err)
			}
		})
	}
}

func TestInboxAcceptsCandidateDeliveredAfterStartup(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(root) })
	inbox, err := NewInbox(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	publicKey, _, _ := writeInboxCandidate(t, root, "candidate-after-startup", now)
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}

	candidates, err := collectInboxCandidates(t, context.Background(), inbox, TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count=%d want=1", len(candidates))
	}
}

func TestInboxEnumerationIsBatchedAndCandidateBounded(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 6; index++ {
		name := filepath.Join(root, fmt.Sprintf("candidate-%02d", index))
		if err := os.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	handle, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	reader := &recordingDirectoryReader{File: handle}
	entries, err := readBoundedDirectory(reader, 5)
	if !errors.Is(err, ErrPolicyRejected) || entries != nil {
		t.Fatalf("over-limit directory entries=%v error=%v", entries, err)
	}
	if len(reader.batchSizes) == 0 {
		t.Fatal("directory enumeration made no bounded reads")
	}
	for _, size := range reader.batchSizes {
		if size <= 0 || size > inboxReadBatchSize {
			t.Fatalf("unbounded directory read size=%d batches=%v", size, reader.batchSizes)
		}
	}
}

func TestInboxFreezesFifteenMinuteDeadlinePerCandidate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, root, "candidate", now)
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(root) })
	inbox, err := NewInbox(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(newStoreTestRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	visited := false
	err = inbox.Scan(context.Background(), TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now, func(packageCtx context.Context, candidate InboxCandidate) error {
		visited = true
		deadline, ok := packageCtx.Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining <= 14*time.Minute+59*time.Second || remaining > offlinePackageDeadline {
			t.Fatalf("offline package deadline ok=%v remaining=%v", ok, remaining)
		}
		_, err := store.storeInboxCandidate(packageCtx, candidate)
		return err
	})
	if err != nil || !visited {
		t.Fatalf("offline deadline scan visited=%v error=%v", visited, err)
	}
}

func TestInboxRejectsRootReachedThroughSymlinkedAncestor(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	base := t.TempDir()
	realParent := filepath.Join(base, "real-parent")
	root := filepath.Join(realParent, "inbox")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	publicKey, _, _ := writeInboxCandidate(t, root, "candidate", now)
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-parent")
	if err := os.Symlink(realParent, link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(root) })

	inbox, err := NewInbox(filepath.Join(link, "inbox"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	_, err = collectInboxCandidates(t, context.Background(), inbox, TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if !errors.Is(err, ErrPolicyRejected) {
		t.Fatalf("symlinked inbox ancestor error=%v", err)
	}
}

func TestInboxRejectsSymlinkHardlinkAndExtraEntries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, testCase := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "symlink", mutate: func(t *testing.T, candidate string) {
			t.Helper()
			manifest := filepath.Join(candidate, "manifest.json")
			if err := os.Rename(manifest, manifest+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("manifest.json.real", manifest); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, candidate string) {
			t.Helper()
			if err := os.Link(filepath.Join(candidate, "bundle.tar"), filepath.Join(candidate, "bundle-copy.tar")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "extra", mutate: func(t *testing.T, candidate string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(candidate, "extra"), []byte("extra"), 0o444); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			publicKey, _, _ := writeInboxCandidate(t, root, "candidate", now)
			candidate := filepath.Join(root, "candidate")
			if err := os.Chmod(candidate, 0o755); err != nil {
				t.Fatal(err)
			}
			testCase.mutate(t, candidate)
			if err := os.Chmod(candidate, 0o555); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { makeTreeWritable(root) })
			inbox, err := NewInbox(root, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			_, err = collectInboxCandidates(t, context.Background(), inbox, TrustStore{Keys: []TrustedKey{{
				ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
			}}}, now)
			if !errors.Is(err, ErrPolicyRejected) {
				t.Fatalf("unsafe inbox error=%v", err)
			}
		})
	}
}

func TestInboxVerifiesManifestBeforeOpeningBundle(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	publicKey, _, _ := writeInboxCandidate(t, root, "candidate", now)
	candidate := filepath.Join(root, "candidate")
	manifestPath := filepath.Join(candidate, "manifest.json")
	manifestPayload, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestPayload, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Signature = strings.Repeat("A", len(manifest.Signature))
	manifestPayload, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(candidate, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(manifestPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifestPayload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(manifestPath, 0o444); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(candidate, "bundle.tar")
	if err := os.Remove(bundlePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-bundle", bundlePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(candidate, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { makeTreeWritable(root) })
	inbox, err := NewInbox(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	err = inbox.Scan(context.Background(), TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now, func(context.Context, InboxCandidate) error {
		t.Fatal("bundle callback ran before invalid manifest was rejected")
		return nil
	})
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("manifest-first error=%v", err)
	}
}

func TestInboxStreamingStoreRejectsSourceDriftAndReplacementWithoutPublishing(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, testCase := range []struct {
		name   string
		mutate func(*testing.T, string, []byte)
	}{
		{name: "in-place metadata drift", mutate: func(t *testing.T, path string, _ []byte) {
			t.Helper()
			future := now.Add(24 * time.Hour)
			if err := os.Chtimes(path, future, future); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "in-place rewrite with restored mtime", mutate: func(t *testing.T, path string, bundle []byte) {
			t.Helper()
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, bundle, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o444); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "path replacement", mutate: func(t *testing.T, path string, bundle []byte) {
			t.Helper()
			candidate := filepath.Dir(path)
			if err := os.Chmod(candidate, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, bundle, 0o444); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(candidate, 0o555); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			publicKey, _, bundle := writeInboxCandidate(t, root, "candidate", now)
			if err := os.Chmod(root, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { makeTreeWritable(root) })
			inbox, err := NewInbox(root, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			storeRoot := newStoreTestRoot(t)
			store, err := NewStore(storeRoot)
			if err != nil {
				t.Fatal(err)
			}
			bundlePath := filepath.Join(root, "candidate", "bundle.tar")
			err = inbox.Scan(context.Background(), TrustStore{Keys: []TrustedKey{{
				ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
			}}}, now, func(packageCtx context.Context, candidate InboxCandidate) error {
				testCase.mutate(t, bundlePath, bundle)
				_, storeErr := store.storeInboxCandidate(packageCtx, candidate)
				return storeErr
			})
			if !errors.Is(err, ErrPolicyRejected) {
				t.Fatalf("unstable source error=%v", err)
			}
			entries, readErr := os.ReadDir(filepath.Join(storeRoot, "bundles"))
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("unstable source published or left staging: entries=%v err=%v", entries, readErr)
			}
		})
	}
}

func collectInboxCandidates(
	t *testing.T,
	ctx context.Context,
	inbox *Inbox,
	trust TrustStore,
	now time.Time,
) ([]InboxCandidate, error) {
	t.Helper()
	store, err := NewStore(newStoreTestRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	result := make([]InboxCandidate, 0)
	err = inbox.Scan(ctx, trust, now, func(packageCtx context.Context, candidate InboxCandidate) error {
		if _, err := store.storeInboxCandidate(packageCtx, candidate); err != nil {
			return err
		}
		result = append(result, candidate)
		return nil
	})
	return result, err
}

func writeInboxCandidate(t *testing.T, root, name string, now time.Time) (ed25519.PublicKey, []byte, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bundle, files, err := BuildCanonicalTar([]BundleFilePayload{{Path: "models/model.dat", Mode: 0o444, Content: []byte("FAKE_BUNDLE_CONTENT_FOR_TEST_ONLY")}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "offline.default", Version: "1.2.3-rc.1+build.7",
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Capabilities: []ManifestCapability{{
			Capability: "image.ocr", Schema: "image.ocr.v1", Profiles: []string{"tesseract_text_v1"},
			ToolRevision: "tesseract-5", ModelRevision: "model-v1", DataRevision: "none",
		}},
		Files: files, BundleSHA256: SHA256Hex(bundle), SigningKeyID: "key-2026", SignatureAlgorithm: "ed25519",
	}
	if err := SignManifest(&manifest, privateKey); err != nil {
		t.Fatal(err)
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(root, name)
	if err := os.Mkdir(candidate, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "manifest.json"), manifestPayload, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "bundle.tar"), bundle, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(candidate, 0o555); err != nil {
		t.Fatal(err)
	}
	return publicKey, manifestPayload, bundle
}

func makeTreeWritable(root string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
}

type recordingDirectoryReader struct {
	*os.File
	batchSizes []int
}

func (reader *recordingDirectoryReader) ReadDir(count int) ([]os.DirEntry, error) {
	reader.batchSizes = append(reader.batchSizes, count)
	return reader.File.ReadDir(count)
}
