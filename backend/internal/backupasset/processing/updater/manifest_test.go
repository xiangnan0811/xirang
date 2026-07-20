package updater

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestVerifyPackageAcceptsCanonicalSignedTarAndDerivesContentIdentity(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	files := []BundleFilePayload{
		{Path: "models/ocr.dat", Mode: 0o444, Content: []byte("model")},
		{Path: "policies/parser.json", Mode: 0o444, Content: []byte(`{"schema_version":1}`)},
	}
	bundle, manifestFiles, err := BuildCanonicalTar(files)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: 1, SourceKind: "admin_registered", SourceID: "offline.default", Version: "1.2.3",
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		Capabilities: []ManifestCapability{{
			Capability: "image.ocr", Schema: "image.ocr.v1", Profiles: []string{"tesseract_text_v1"},
			ToolRevision: "tesseract-5", ModelRevision: "ocr-2026-07", DataRevision: "none",
		}},
		Files: manifestFiles, BundleSHA256: SHA256Hex(bundle), SigningKeyID: "key-2026",
		SignatureAlgorithm: "ed25519",
	}
	if err := SignManifest(&manifest, privateKey); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyPackage(payload, bundle, TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified.BundleFingerprint) != 64 || verified.SigningKeyFingerprint != SHA256Hex(publicKey) ||
		verified.ManifestDigest != SHA256Hex(payload) || len(verified.Files) != len(files) {
		t.Fatalf("invalid verified package: %+v", verified)
	}

	rotatedPublic, rotatedPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rotated := manifest
	rotated.SigningKeyID = "key-2027"
	rotated.Signature = ""
	if err := SignManifest(&rotated, rotatedPrivate); err != nil {
		t.Fatal(err)
	}
	rotatedPayload, _ := json.Marshal(rotated)
	rotatedVerified, err := VerifyPackage(rotatedPayload, bundle, TrustStore{Keys: []TrustedKey{
		{ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour)},
		{ID: "key-2027", PublicKey: rotatedPublic, ActiveFrom: now.Add(-time.Minute), RetireAfter: now.Add(2 * time.Hour)},
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedVerified.BundleFingerprint != verified.BundleFingerprint {
		t.Fatalf("key rotation changed content fingerprint: %s / %s", verified.BundleFingerprint, rotatedVerified.BundleFingerprint)
	}
}

func TestVerifyPackageRejectsSignatureTarAndPathContractViolations(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
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
		Capabilities: []ManifestCapability{{Capability: "image.ocr", Schema: "image.ocr.v1", Profiles: []string{"tesseract_text_v1"}, ToolRevision: "tesseract-5", ModelRevision: "model-v1", DataRevision: "none"}},
		Files:        files, BundleSHA256: SHA256Hex(bundle), SigningKeyID: "key-2026", SignatureAlgorithm: "ed25519",
	}
	if err := SignManifest(&manifest, privateKey); err != nil {
		t.Fatal(err)
	}
	trust := TrustStore{Keys: []TrustedKey{{ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour)}}}

	tests := []struct {
		name     string
		manifest Manifest
		bundle   []byte
		want     error
	}{
		{name: "bad signature", manifest: func() Manifest {
			value := manifest
			value.Signature = strings.Repeat("A", len(value.Signature))
			return value
		}(), bundle: bundle, want: ErrInvalidSignature},
		{name: "trailing tar bytes", manifest: manifest, bundle: append(append([]byte(nil), bundle...), []byte("trailing")...), want: ErrPolicyRejected},
		{name: "path traversal", manifest: func() Manifest {
			value := manifest
			value.Files = append([]ManifestFile(nil), manifest.Files...)
			value.Files[0].Path = "../model.dat"
			return value
		}(), bundle: bundle, want: ErrPolicyRejected},
		{name: "retired key", manifest: manifest, bundle: bundle, want: ErrInvalidSignature},
	}
	for _, testCase := range tests {
		payload, err := json.Marshal(testCase.manifest)
		if err != nil {
			t.Fatal(err)
		}
		caseTrust := trust
		caseNow := now
		if testCase.name == "retired key" {
			caseTrust = TrustStore{Keys: append([]TrustedKey(nil), trust.Keys...)}
			caseTrust.Keys[0].RetireAfter = now.Add(30 * time.Minute)
			caseNow = now.Add(45 * time.Minute)
		}
		_, err = VerifyPackage(payload, testCase.bundle, caseTrust, caseNow)
		if !errors.Is(err, testCase.want) {
			t.Fatalf("%s error=%v, want %v", testCase.name, err, testCase.want)
		}
	}

	validPayload, _ := json.Marshal(manifest)
	duplicate := bytes.Replace(validPayload, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
	if _, err := VerifyPackage(duplicate, bundle, trust, now); !errors.Is(err, ErrPolicyRejected) {
		t.Fatalf("duplicate JSON error=%v", err)
	}
	unknown := bytes.Replace(validPayload, []byte(`"schema_version":1`), []byte(`"schema_version":1,"unknown":true`), 1)
	if _, err := VerifyPackage(unknown, bundle, trust, now); !errors.Is(err, ErrPolicyRejected) {
		t.Fatalf("unknown JSON error=%v", err)
	}
}
