package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	candidates, err := inbox.Scan(context.Background(), TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Receipt.ManifestDigest != SHA256Hex(manifestPayload) ||
		candidates[0].Receipt.BundleSHA256 != SHA256Hex(bundle) || candidates[0].Receipt.Version != "1.0.0" {
		t.Fatalf("unexpected inbox candidates: %+v", candidates)
	}
	payload, err := json.Marshal(candidates[0].Receipt)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(payload))
	for _, forbidden := range []string{"candidate-one", root, "manifest.json", "bundle.tar", "path", "content", "signature"} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Fatalf("sanitized inbox receipt leaked %q: %s", forbidden, payload)
		}
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
	_, err = inbox.Scan(context.Background(), TrustStore{Keys: []TrustedKey{{
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
			_, err = inbox.Scan(context.Background(), TrustStore{Keys: []TrustedKey{{
				ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
			}}}, now)
			if !errors.Is(err, ErrPolicyRejected) {
				t.Fatalf("unsafe inbox error=%v", err)
			}
		})
	}
}

func writeInboxCandidate(t *testing.T, root, name string, now time.Time) (ed25519.PublicKey, []byte, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bundle, files, err := BuildCanonicalTar([]BundleFilePayload{{Path: "models/model.dat", Mode: 0o444, Content: []byte("model")}})
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "offline.default", Version: "1.0.0",
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
