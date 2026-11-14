package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/backupasset/processing/updater"
)

type receipt struct {
	SchemaVersion       int               `json:"schema_version"`
	Kind                string            `json:"kind"`
	Repository          string            `json:"repository"`
	RunID               uint64            `json:"run_id"`
	RunAttempt          uint64            `json:"run_attempt"`
	CheckoutSHA         string            `json:"checkout_sha"`
	ObservedAt          string            `json:"observed_at"`
	Closures            map[string]string `json:"closures"`
	BundleFingerprint   string            `json:"bundle_fingerprint"`
	PipelineFingerprint string            `json:"pipeline_fingerprint"`
	PostRestartBundle   string            `json:"post_restart_bundle_fingerprint"`
	PostRestartPipeline string            `json:"post_restart_pipeline_fingerprint"`
}

type attestation struct {
	Platform              string `json:"platform"`
	RuntimeManifestSHA256 string `json:"runtime_manifest_sha256"`
}

type attestationDocument struct {
	SchemaVersion int           `json:"schema_version"`
	Attestations  []attestation `json:"attestations"`
}

// scanJSON rejects duplicate object keys at every depth and checks that the
// input contains a single complete JSON value.
func scanJSON(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("invalid or duplicate JSON member")
			}
			seen[key] = true
			if err := scanJSON(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for dec.More() {
			if err := scanJSON(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func strictJSON(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := scanJSON(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

func exactKeys(data []byte, names ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || len(object) != len(names) {
		return errors.New("unexpected JSON object fields")
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return errors.New("unexpected JSON object fields")
		}
	}
	return nil
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func readEvidence(dir string) (map[string][]byte, error) {
	names := map[string]bool{
		"manifest.json": true, "bundle.tar": true, "trust.json": true,
		"runtime-closure-amd64.v1.json": true, "runtime-closure-arm64.v1.json": true,
		"attestations.v1.json": true, "receipt.json": true,
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) != len(names) {
		return nil, errors.New("evidence directory must contain exactly seven files")
	}
	result := make(map[string][]byte, len(names))
	for _, entry := range entries {
		if !names[entry.Name()] || entry.Type()&os.ModeSymlink != 0 {
			return nil, errors.New("unexpected or symlink evidence entry")
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("evidence entry is not a regular file")
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		result[entry.Name()] = data
	}
	return result, nil
}

func verify(args []string) error {
	if len(args) != 7 {
		return errors.New("expected seven arguments; use --help for usage")
	}
	expectedRun, err := strconv.ParseUint(args[4], 10, 64)
	if err != nil || expectedRun == 0 {
		return errors.New("expected run ID must be a positive integer")
	}
	expectedAttempt, err := strconv.ParseUint(args[5], 10, 64)
	if err != nil || expectedAttempt == 0 {
		return errors.New("expected attempt must be a positive integer")
	}
	if args[3] == "" || !lowerHex(args[6], 40) {
		return errors.New("invalid expected repository or checkout SHA")
	}

	files, err := readEvidence(args[0])
	if err != nil {
		return fmt.Errorf("read evidence: %w", err)
	}
	var r receipt
	if err := strictJSON(files["receipt.json"], &r); err != nil {
		return fmt.Errorf("decode receipt: %w", err)
	}
	if err := exactKeys(files["receipt.json"], "schema_version", "kind", "repository", "run_id", "run_attempt", "checkout_sha", "observed_at", "closures", "bundle_fingerprint", "pipeline_fingerprint", "post_restart_bundle_fingerprint", "post_restart_pipeline_fingerprint"); err != nil {
		return fmt.Errorf("receipt: %w", err)
	}
	if r.SchemaVersion != 1 || r.Kind != "worker-profile-smoke" || r.Repository != args[3] || r.RunID != expectedRun || r.RunAttempt != expectedAttempt || r.CheckoutSHA != args[6] {
		return errors.New("receipt identity does not match expected run")
	}
	if !strings.HasSuffix(r.ObservedAt, "Z") {
		return errors.New("receipt time must be RFC3339 UTC")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, r.ObservedAt)
	if err != nil {
		return errors.New("receipt time must be RFC3339 UTC")
	}
	if len(r.Closures) != 2 || !lowerHex(r.Closures["linux/amd64"], 64) || !lowerHex(r.Closures["linux/arm64"], 64) {
		return errors.New("receipt must contain exactly two closure digests")
	}
	if !lowerHex(r.BundleFingerprint, 64) || !lowerHex(r.PipelineFingerprint, 64) || r.BundleFingerprint != r.PostRestartBundle || r.PipelineFingerprint != r.PostRestartPipeline {
		return errors.New("receipt fingerprints are invalid or differ after restart")
	}

	trust, err := updater.DecodeTrustStore(files["trust.json"])
	if err != nil {
		return fmt.Errorf("decode trust: %w", err)
	}
	verified, err := updater.VerifyPackage(files["manifest.json"], files["bundle.tar"], trust, observedAt)
	if err != nil {
		return fmt.Errorf("verify signed package: %w", err)
	}
	if verified.BundleFingerprint != r.BundleFingerprint {
		return errors.New("verified bundle fingerprint differs from receipt")
	}
	var attestationBytes []byte
	count := 0
	for _, file := range verified.Files {
		if file.Path == "toolchain/attestations.v1.json" {
			count++
			attestationBytes = file.Content
		}
	}
	if count != 1 || !bytes.Equal(attestationBytes, files["attestations.v1.json"]) {
		return errors.New("signed and exported attestations differ or are missing")
	}
	var doc attestationDocument
	if err := strictJSON(attestationBytes, &doc); err != nil {
		return fmt.Errorf("decode attestations: %w", err)
	}
	if err := exactKeys(attestationBytes, "schema_version", "attestations"); err != nil {
		return fmt.Errorf("attestations: %w", err)
	}
	var raw struct {
		Attestations []json.RawMessage `json:"attestations"`
	}
	if err := json.Unmarshal(attestationBytes, &raw); err != nil {
		return err
	}
	if doc.SchemaVersion != 1 || len(doc.Attestations) != 2 || len(raw.Attestations) != 2 {
		return errors.New("expected schema 1 and two attestations")
	}
	platforms := []string{"linux/amd64", "linux/arm64"}
	closureNames := []string{"runtime-closure-amd64.v1.json", "runtime-closure-arm64.v1.json"}
	for i, platform := range platforms {
		if err := exactKeys(raw.Attestations[i], "platform", "runtime_manifest_sha256"); err != nil {
			return fmt.Errorf("attestation: %w", err)
		}
		native, err := os.ReadFile(args[i+1])
		if err != nil {
			return fmt.Errorf("read native closure %s: %w", platform, err)
		}
		digest := updater.SHA256Hex(files[closureNames[i]])
		if doc.Attestations[i].Platform != platform || doc.Attestations[i].RuntimeManifestSHA256 != digest || r.Closures[platform] != digest || updater.SHA256Hex(native) != digest {
			return fmt.Errorf("closure or attestation mismatch for %s", platform)
		}
	}
	bundles := make(processing.CapabilityBundleFingerprints)
	for _, profile := range capabilityspec.WorkerProfiles() {
		bundles[profile.Capability] = []string{verified.BundleFingerprint}
	}
	capabilities, err := processing.NewProductionWorkerCapabilitySetWithBundles(bundles)
	if err != nil {
		return fmt.Errorf("construct worker capabilities: %w", err)
	}
	pipeline := ""
	count = 0
	for _, ad := range capabilities.Advertisements() {
		if ad.Capability == capabilityspec.CapabilityImageOCR && ad.OutputProfile == capabilityspec.ProfileTesseractTextV1 {
			count++
			pipeline = ad.PipelineFingerprint
		}
	}
	if count != 1 || pipeline != r.PipelineFingerprint {
		return errors.New("verified OCR pipeline fingerprint differs from receipt")
	}
	result := struct {
		Repository          string            `json:"repository"`
		RunID               uint64            `json:"run_id"`
		RunAttempt          uint64            `json:"run_attempt"`
		CheckoutSHA         string            `json:"checkout_sha"`
		Closures            map[string]string `json:"closure_sha256"`
		ClosuresVerified    bool              `json:"closures_verified"`
		ManifestSHA256      string            `json:"manifest_sha256"`
		BundleSHA256        string            `json:"bundle_sha256"`
		AttestationsSHA256  string            `json:"attestations_sha256"`
		BundleFingerprint   string            `json:"bundle_fingerprint"`
		PipelineFingerprint string            `json:"pipeline_fingerprint"`
	}{r.Repository, r.RunID, r.RunAttempt, r.CheckoutSHA, r.Closures, true, updater.SHA256Hex(files["manifest.json"]), updater.SHA256Hex(files["bundle.tar"]), updater.SHA256Hex(attestationBytes), verified.BundleFingerprint, pipeline}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		fmt.Println("Usage: verify-ci-profile EVIDENCE_DIR NATIVE_AMD64_CLOSURE NATIVE_ARM64_CLOSURE EXPECTED_REPOSITORY EXPECTED_RUN_ID EXPECTED_ATTEMPT EXPECTED_CHECKOUT_SHA")
		return
	}
	if err := verify(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "profile proof verification:", err)
		os.Exit(1)
	}
}
