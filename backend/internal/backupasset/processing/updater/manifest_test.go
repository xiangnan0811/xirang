package updater

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
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

func TestVerifyPackageRejectsMalformedRawCanonicalUSTARFraming(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	manifest, _, bundle, trust, privateKey := signedManifestTestPackage(t, now, []byte("model"))

	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "noncanonical uid header", mutate: func(value []byte) []byte {
			copy(value[108:116], []byte("0000001\x00"))
			rewriteTarHeaderChecksum(value[:512])
			return value
		}},
		{name: "malformed checksum", mutate: func(value []byte) []byte {
			value[0] ^= 1
			return value
		}},
		{name: "nonzero member padding", mutate: func(value []byte) []byte {
			value[512+len("model")] = 1
			return value
		}},
		{name: "one terminal block", mutate: func(value []byte) []byte {
			return value[:len(value)-512]
		}},
		{name: "nonzero terminal block", mutate: func(value []byte) []byte {
			value[len(value)-1024] = 1
			return value
		}},
		{name: "trailing byte", mutate: func(value []byte) []byte {
			return append(value, 0)
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := testCase.mutate(append([]byte(nil), bundle...))
			candidate := manifest
			candidate.BundleSHA256 = SHA256Hex(mutated)
			candidate.Signature = ""
			if err := SignManifest(&candidate, privateKey); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyPackage(payload, mutated, trust, now); !errors.Is(err, ErrPolicyRejected) {
				t.Fatalf("malformed tar error=%v", err)
			}
		})
	}
}

func TestVerifyPackageRejectsMemberAndWholeBundleDigestMismatch(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	manifest, _, bundle, trust, privateKey := signedManifestTestPackage(t, now, []byte("model"))

	memberMismatch := append([]byte(nil), bundle...)
	memberMismatch[512] ^= 1
	memberManifest := manifest
	memberManifest.BundleSHA256 = SHA256Hex(memberMismatch)
	memberManifest.Signature = ""
	if err := SignManifest(&memberManifest, privateKey); err != nil {
		t.Fatal(err)
	}
	memberPayload, _ := json.Marshal(memberManifest)
	if _, err := VerifyPackage(memberPayload, memberMismatch, trust, now); !errors.Is(err, ErrPolicyRejected) {
		t.Fatalf("member digest mismatch error=%v", err)
	}

	bundleManifest := manifest
	bundleManifest.BundleSHA256 = strings.Repeat("0", 64)
	bundleManifest.Signature = ""
	if err := SignManifest(&bundleManifest, privateKey); err != nil {
		t.Fatal(err)
	}
	bundlePayload, _ := json.Marshal(bundleManifest)
	if _, err := VerifyPackage(bundlePayload, bundle, trust, now); !errors.Is(err, ErrPolicyRejected) {
		t.Fatalf("bundle digest mismatch error=%v", err)
	}
}

func signedManifestTestPackage(
	t *testing.T,
	now time.Time,
	content []byte,
) (Manifest, []byte, []byte, TrustStore, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bundle, files, err := BuildCanonicalTar([]BundleFilePayload{{Path: "models/model.dat", Mode: 0o444, Content: content}})
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
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	trust := TrustStore{Keys: []TrustedKey{{
		ID: "key-2026", PublicKey: publicKey, ActiveFrom: now.Add(-time.Hour), RetireAfter: now.Add(time.Hour),
	}}}
	return manifest, payload, bundle, trust, privateKey
}

func rewriteTarHeaderChecksum(header []byte) {
	for index := 148; index < 156; index++ {
		header[index] = ' '
	}
	var checksum int
	for _, value := range header {
		checksum += int(value)
	}
	copy(header[148:156], []byte(fmt.Sprintf("%06o\x00 ", checksum)))
}
