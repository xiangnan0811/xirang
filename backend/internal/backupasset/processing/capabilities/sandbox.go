package capabilities

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

const productionSandboxBinary = "/usr/local/bin/asset-tool-sandbox"

const productionAPKDatabase = "/lib/apk/db/installed"

const ProductionRuntimeClosureManifestPath = "/usr/local/share/xirang/runtime-closure.v1.json"

const StoredBundleReceiptPath = ".xirang-bundle-receipt.v1.json"

const ToolchainAttestationsBundlePath = "toolchain/attestations.v1.json"

const (
	maximumStoredBundleReceiptBytes = 32 << 20
	maximumStoredBundleFiles        = 100_000
	maximumStoredBundleBytes        = int64(1 << 30)
	maximumRuntimeClosureBytes      = 32 << 20
	maximumRuntimeClosureFiles      = 100_000
	maximumRuntimeClosureFileBytes  = int64(4 << 30)
)

// ToolchainPreflight is the immutable runtime evidence used to decide which
// closed profiles may be advertised by a Worker. The bundle fingerprint is
// deliberately kept separate: it is supplied by the updater contract.
type ToolchainPreflight struct {
	Fingerprint                   string
	AvailableCapabilities         map[string]bool
	UngatedAvailableCount         int
	RuntimeClosureReady           bool
	RuntimeClosureFailureCategory RuntimeClosureFailureCategory
	// RuntimeClosureFailureFileIndex is the zero-based canonical files[] index,
	// or RuntimeClosureNoFileIndex when no declared file caused the failure.
	RuntimeClosureFailureFileIndex int
}

type RuntimeClosureFailureCategory string

const (
	RuntimeClosureFailureNotChecked RuntimeClosureFailureCategory = "not_checked"
	RuntimeClosureFailureNone       RuntimeClosureFailureCategory = "none"
	RuntimeClosureFailureEvidence   RuntimeClosureFailureCategory = "evidence"
	RuntimeClosureFailureMetadata   RuntimeClosureFailureCategory = "metadata"
	RuntimeClosureFailureRead       RuntimeClosureFailureCategory = "read"
	RuntimeClosureFailureDigest     RuntimeClosureFailureCategory = "digest"
	RuntimeClosureFailureSymlink    RuntimeClosureFailureCategory = "symlink"
	RuntimeClosureFailureRace       RuntimeClosureFailureCategory = "race"
)

const RuntimeClosureNoFileIndex = -1

func (preflight ToolchainPreflight) RuntimeClosureDiagnostic(checked bool) (bool, RuntimeClosureFailureCategory, int) {
	if !checked {
		return false, RuntimeClosureFailureNotChecked, RuntimeClosureNoFileIndex
	}
	if preflight.RuntimeClosureReady {
		if preflight.RuntimeClosureFailureCategory == RuntimeClosureFailureNone &&
			preflight.RuntimeClosureFailureFileIndex == RuntimeClosureNoFileIndex {
			return true, RuntimeClosureFailureNone, RuntimeClosureNoFileIndex
		}
		return false, RuntimeClosureFailureEvidence, RuntimeClosureNoFileIndex
	}
	switch preflight.RuntimeClosureFailureCategory {
	case RuntimeClosureFailureEvidence:
		return false, RuntimeClosureFailureEvidence, RuntimeClosureNoFileIndex
	case RuntimeClosureFailureMetadata,
		RuntimeClosureFailureRead,
		RuntimeClosureFailureDigest,
		RuntimeClosureFailureSymlink:
		if preflight.RuntimeClosureFailureFileIndex >= 0 &&
			preflight.RuntimeClosureFailureFileIndex < maximumRuntimeClosureFiles {
			return false, preflight.RuntimeClosureFailureCategory, preflight.RuntimeClosureFailureFileIndex
		}
	case RuntimeClosureFailureRace:
		if preflight.RuntimeClosureFailureFileIndex >= RuntimeClosureNoFileIndex &&
			preflight.RuntimeClosureFailureFileIndex < maximumRuntimeClosureFiles {
			return false, RuntimeClosureFailureRace, preflight.RuntimeClosureFailureFileIndex
		}
	}
	return false, RuntimeClosureFailureEvidence, RuntimeClosureNoFileIndex
}

type runtimeClosureVerificationError struct {
	category  RuntimeClosureFailureCategory
	fileIndex int
}

func (err *runtimeClosureVerificationError) Error() string { return ErrInvalidInvocation.Error() }

func (err *runtimeClosureVerificationError) Unwrap() error { return ErrInvalidInvocation }

func (err *runtimeClosureVerificationError) RuntimeClosureFailure() (string, int) {
	return string(err.category), err.fileIndex
}

func newRuntimeClosureVerificationError(category RuntimeClosureFailureCategory, fileIndex int) error {
	return &runtimeClosureVerificationError{category: category, fileIndex: fileIndex}
}

func runtimeClosureFailureDiagnostic(err error) (RuntimeClosureFailureCategory, int) {
	if err == nil {
		return RuntimeClosureFailureNone, RuntimeClosureNoFileIndex
	}
	var diagnostic *runtimeClosureVerificationError
	if errors.As(err, &diagnostic) {
		return diagnostic.category, diagnostic.fileIndex
	}
	return RuntimeClosureFailureEvidence, RuntimeClosureNoFileIndex
}

func runtimeClosureFailureAtFileIndex(err error, fileIndex int) error {
	category, _ := runtimeClosureFailureDiagnostic(err)
	return newRuntimeClosureVerificationError(category, fileIndex)
}

type StoredBundleReceipt struct {
	SchemaVersion         int                      `json:"schema_version"`
	BundleFingerprint     string                   `json:"bundle_fingerprint"`
	ManifestSchemaVersion int                      `json:"manifest_schema_version"`
	Capabilities          []StoredBundleCapability `json:"capabilities"`
	Files                 []StoredBundleFile       `json:"files"`
	BundleSHA256          string                   `json:"bundle_sha256"`
}

type StoredBundleCapability struct {
	Capability    string   `json:"capability"`
	Schema        string   `json:"schema"`
	Profiles      []string `json:"profiles"`
	ToolRevision  string   `json:"tool_revision"`
	ModelRevision string   `json:"model_revision"`
	DataRevision  string   `json:"data_revision"`
}

type StoredBundleFile struct {
	Path   string `json:"path"`
	Mode   int64  `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type runtimeClosureManifest struct {
	SchemaVersion int                  `json:"schema_version"`
	Platform      string               `json:"platform"`
	Files         []runtimeClosureFile `json:"files"`
}

type runtimeClosureFile struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Mode   int64  `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type runtimeClosureAttestations struct {
	SchemaVersion int                         `json:"schema_version"`
	Attestations  []runtimeClosureAttestation `json:"attestations"`
}

type runtimeClosureAttestation struct {
	Platform              string `json:"platform"`
	RuntimeManifestSHA256 string `json:"runtime_manifest_sha256"`
}

func SHA256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func buildRuntimeClosureManifest(platform string, roots, excluded []string) ([]byte, error) {
	if platform != "linux/amd64" && platform != "linux/arm64" || len(roots) == 0 || len(roots) > 16 {
		return nil, ErrInvalidInvocation
	}
	canonicalRoots, err := canonicalRuntimeClosurePaths(roots, false)
	if err != nil {
		return nil, err
	}
	canonicalExcluded, err := canonicalRuntimeClosurePaths(excluded, true)
	if err != nil {
		return nil, err
	}
	manifest := runtimeClosureManifest{SchemaVersion: 1, Platform: platform}
	seen := make(map[string]bool)
	var total int64
	for _, root := range canonicalRoots {
		rootInfo, statErr := os.Lstat(root)
		if statErr != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalidInvocation
		}
		walkErr := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return ErrInvalidInvocation
			}
			if runtimeClosurePathExcluded(current, canonicalExcluded) {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if seen[current] || len(manifest.Files) >= maximumRuntimeClosureFiles {
				return ErrInvalidInvocation
			}
			info, infoErr := os.Lstat(current)
			if infoErr != nil || info.Size() < 0 || info.Size() > maximumRuntimeClosureFileBytes-total {
				return ErrInvalidInvocation
			}
			declaration := runtimeClosureFile{Path: current, Mode: int64(info.Mode().Perm()), Size: info.Size()}
			switch {
			case info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0:
				declaration.Kind = "regular"
				declaration.SHA256, infoErr = runtimeClosureRegularDigest(current, info)
			case info.Mode()&os.ModeSymlink != 0:
				declaration.Kind = "symlink"
				declaration.SHA256, infoErr = runtimeClosureSymlinkDigest(current, info)
			default:
				return ErrInvalidInvocation
			}
			if infoErr != nil {
				return infoErr
			}
			seen[current] = true
			total += info.Size()
			manifest.Files = append(manifest.Files, declaration)
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	if len(manifest.Files) == 0 {
		return nil, ErrInvalidInvocation
	}
	sort.Slice(manifest.Files, func(left, right int) bool { return manifest.Files[left].Path < manifest.Files[right].Path })
	payload, err := json.Marshal(manifest)
	if err != nil || len(payload) > maximumRuntimeClosureBytes {
		return nil, ErrInvalidInvocation
	}
	return payload, nil
}

func canonicalRuntimeClosurePaths(values []string, allowEmpty bool) ([]string, error) {
	if !allowEmpty && len(values) == 0 {
		return nil, ErrInvalidInvocation
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\x00\r\n") ||
			index > 0 && value == result[index-1] {
			return nil, ErrInvalidInvocation
		}
	}
	return result, nil
}

func runtimeClosurePathExcluded(value string, excluded []string) bool {
	for _, prefix := range excluded {
		if value == prefix || prefix != string(os.PathSeparator) && strings.HasPrefix(value, prefix+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func runtimeClosureRegularDigest(filePath string, before os.FileInfo) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", ErrInvalidInvocation
	}
	digest := sha256.New()
	written, copyErr := io.Copy(digest, io.LimitReader(file, before.Size()+1))
	closeErr := file.Close()
	after, statErr := os.Lstat(filePath)
	if copyErr != nil || closeErr != nil || statErr != nil || written != before.Size() || !os.SameFile(before, after) {
		return "", ErrInvalidInvocation
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func runtimeClosureSymlinkDigest(filePath string, before os.FileInfo) (string, error) {
	target, err := os.Readlink(filePath)
	after, statErr := os.Lstat(filePath)
	if err != nil || statErr != nil || strings.ContainsAny(target, "\x00\r\n") || int64(len(target)) != before.Size() ||
		!os.SameFile(before, after) {
		return "", ErrInvalidInvocation
	}
	return SHA256Hex([]byte(target)), nil
}

func writeRuntimeClosureManifest(outputPath, platform string, roots, excluded []string) error {
	if !filepath.IsAbs(outputPath) || filepath.Clean(outputPath) != outputPath || outputPath == string(os.PathSeparator) ||
		strings.ContainsAny(outputPath, "\x00\r\n") {
		return ErrInvalidInvocation
	}
	parent := filepath.Dir(outputPath)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidInvocation
	}
	if _, err := os.Lstat(outputPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return ErrInvalidInvocation
	}
	payload, err := buildRuntimeClosureManifest(platform, roots, excluded)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(parent, ".runtime-closure-")
	if err != nil {
		return ErrInvalidInvocation
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	written, writeErr := temporary.Write(payload)
	syncErr := temporary.Sync()
	chmodErr := temporary.Chmod(0o444)
	closeErr := temporary.Close()
	if writeErr != nil || syncErr != nil || chmodErr != nil || closeErr != nil || written != len(payload) {
		return ErrInvalidInvocation
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return ErrInvalidInvocation
	}
	cleanup = false
	directory, err := os.Open(parent)
	if err != nil {
		return ErrInvalidInvocation
	}
	syncErr = directory.Sync()
	closeErr = directory.Close()
	if syncErr != nil || closeErr != nil {
		return ErrInvalidInvocation
	}
	return nil
}

// WriteProductionRuntimeClosureManifest records every immutable regular file
// and symlink in the Worker image after installation. Runtime mounts and the
// active signed bundle are verified by their dedicated contracts instead.
func productionRuntimeClosureExcludedPaths() []string {
	return []string{
		ProductionRuntimeClosureManifestPath,
		"/dev", "/proc", "/run", "/sys", "/tmp", "/var/tmp",
		"/etc/hostname", "/etc/hosts", "/etc/mtab", "/etc/resolv.conf", "/etc/shadow",
		"/var/lib/xirang/asset-worker-bundles", "/var/lib/xirang/asset-worker-inbox",
		"/var/lib/xirang-asset-runtime",
	}
}

func WriteProductionRuntimeClosureManifest() error {
	return writeRuntimeClosureManifest(
		ProductionRuntimeClosureManifestPath, runtime.GOOS+"/"+runtime.GOARCH,
		[]string{string(os.PathSeparator)}, productionRuntimeClosureExcludedPaths(),
	)
}

func EncodeStoredBundleReceipt(value StoredBundleReceipt) ([]byte, error) {
	canonical, err := canonicalStoredBundleReceipt(value)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return nil, ErrInvalidInvocation
	}
	return payload, nil
}

func DecodeStoredBundleReceipt(payload []byte) (StoredBundleReceipt, error) {
	if len(payload) == 0 || len(payload) > maximumStoredBundleReceiptBytes || !json.Valid(payload) {
		return StoredBundleReceipt{}, ErrInvalidInvocation
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value StoredBundleReceipt
	if decoder.Decode(&value) != nil || ensureToolchainJSONEOF(decoder) != nil {
		return StoredBundleReceipt{}, ErrInvalidInvocation
	}
	canonical, err := canonicalStoredBundleReceipt(value)
	if err != nil {
		return StoredBundleReceipt{}, err
	}
	canonicalPayload, err := json.Marshal(canonical)
	if err != nil || !bytes.Equal(canonicalPayload, payload) {
		return StoredBundleReceipt{}, ErrInvalidInvocation
	}
	return canonical, nil
}

func canonicalStoredBundleReceipt(value StoredBundleReceipt) (StoredBundleReceipt, error) {
	if value.SchemaVersion != 1 || value.ManifestSchemaVersion != 1 || !lowerHexToolchain(value.BundleFingerprint, 64) ||
		!lowerHexToolchain(value.BundleSHA256, 64) || len(value.Capabilities) == 0 || len(value.Capabilities) > 32 ||
		len(value.Files) == 0 || len(value.Files) > maximumStoredBundleFiles {
		return StoredBundleReceipt{}, ErrInvalidInvocation
	}
	result := StoredBundleReceipt{
		SchemaVersion: value.SchemaVersion, BundleFingerprint: value.BundleFingerprint,
		ManifestSchemaVersion: value.ManifestSchemaVersion, BundleSHA256: value.BundleSHA256,
		Capabilities: append([]StoredBundleCapability(nil), value.Capabilities...),
		Files:        append([]StoredBundleFile(nil), value.Files...),
	}
	for index := range result.Capabilities {
		result.Capabilities[index].Profiles = append([]string(nil), result.Capabilities[index].Profiles...)
		sort.Strings(result.Capabilities[index].Profiles)
	}
	sort.Slice(result.Capabilities, func(left, right int) bool {
		return result.Capabilities[left].Capability+"\x00"+result.Capabilities[left].Schema <
			result.Capabilities[right].Capability+"\x00"+result.Capabilities[right].Schema
	})
	lastCapability := ""
	for _, capability := range result.Capabilities {
		identity := capability.Capability + "\x00" + capability.Schema
		if !validToolchainIdentifier(capability.Capability, 64) || !validToolchainIdentifier(capability.Schema, 64) ||
			identity <= lastCapability || !validToolchainIdentifier(capability.ToolRevision, 128) ||
			!validToolchainIdentifier(capability.ModelRevision, 128) || !validToolchainIdentifier(capability.DataRevision, 128) ||
			len(capability.Profiles) == 0 || len(capability.Profiles) > 16 {
			return StoredBundleReceipt{}, ErrInvalidInvocation
		}
		lastCapability = identity
		lastProfile := ""
		for _, profile := range capability.Profiles {
			if !validToolchainIdentifier(profile, 64) || profile <= lastProfile {
				return StoredBundleReceipt{}, ErrInvalidInvocation
			}
			lastProfile = profile
		}
	}
	sort.Slice(result.Files, func(left, right int) bool { return result.Files[left].Path < result.Files[right].Path })
	seenFolded := make(map[string]bool, len(result.Files))
	var total int64
	for index, file := range result.Files {
		folded := strings.ToLower(file.Path)
		if !validStoredBundlePath(file.Path) || file.Path == StoredBundleReceiptPath || seenFolded[folded] ||
			file.Mode != 0o444 || file.Size < 0 || file.Size > maximumStoredBundleBytes-total ||
			!lowerHexToolchain(file.SHA256, 64) || index > 0 && file.Path == result.Files[index-1].Path {
			return StoredBundleReceipt{}, ErrInvalidInvocation
		}
		seenFolded[folded] = true
		total += file.Size
	}
	fingerprint, err := StoredBundleFingerprint(result)
	if err != nil || fingerprint != result.BundleFingerprint {
		return StoredBundleReceipt{}, ErrInvalidInvocation
	}
	return result, nil
}

func StoredBundleFingerprint(value StoredBundleReceipt) (string, error) {
	if value.ManifestSchemaVersion != 1 || len(value.Capabilities) == 0 || len(value.Files) == 0 ||
		!lowerHexToolchain(value.BundleSHA256, 64) {
		return "", ErrInvalidInvocation
	}
	content := struct {
		SchemaVersion int                      `json:"schema_version"`
		Capabilities  []StoredBundleCapability `json:"capabilities"`
		Files         []StoredBundleFile       `json:"files"`
		BundleSHA256  string                   `json:"bundle_sha256"`
	}{value.ManifestSchemaVersion, value.Capabilities, value.Files, value.BundleSHA256}
	canonical, err := json.Marshal(content)
	if err != nil {
		return "", ErrInvalidInvocation
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("xirang.asset.bundle.fingerprint.v1\x00"))
	_, _ = digest.Write(canonical)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type StoredBundleEvidence struct {
	BundleFingerprint          string
	ToolchainAttestationSHA256 string
}

func VerifyStoredBundleTree(root, expectedFingerprint string) error {
	_, err := InspectStoredBundleTree(root, expectedFingerprint)
	return err
}

func InspectStoredBundleTree(root, expectedFingerprint string) (StoredBundleEvidence, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(os.PathSeparator) ||
		!lowerHexToolchain(expectedFingerprint, 64) {
		return StoredBundleEvidence{}, ErrInvalidInvocation
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm() != 0o555 {
		return StoredBundleEvidence{}, ErrInvalidInvocation
	}
	receiptPath := filepath.Join(root, StoredBundleReceiptPath)
	receiptInfo, err := os.Lstat(receiptPath)
	if err != nil || !receiptInfo.Mode().IsRegular() || receiptInfo.Mode()&os.ModeSymlink != 0 || receiptInfo.Mode().Perm() != 0o444 {
		return StoredBundleEvidence{}, ErrInvalidInvocation
	}
	receiptPayload, err := readBoundedToolchainFile(receiptPath, maximumStoredBundleReceiptBytes)
	if err != nil {
		return StoredBundleEvidence{}, ErrInvalidInvocation
	}
	receipt, err := DecodeStoredBundleReceipt(receiptPayload)
	if err != nil || receipt.BundleFingerprint != expectedFingerprint {
		return StoredBundleEvidence{}, ErrInvalidInvocation
	}
	expectedFiles := make(map[string]StoredBundleFile, len(receipt.Files))
	expectedDirs := map[string]bool{".": true}
	attestationSHA256 := ""
	for _, file := range receipt.Files {
		relative := filepath.FromSlash(file.Path)
		expectedFiles[relative] = file
		if file.Path == ToolchainAttestationsBundlePath {
			attestationSHA256 = file.SHA256
		}
		for directory := filepath.Dir(relative); directory != "."; directory = filepath.Dir(directory) {
			expectedDirs[directory] = true
		}
	}
	seenFiles := make(map[string]bool, len(receipt.Files))
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrInvalidInvocation
		}
		relative, relativeErr := filepath.Rel(root, current)
		if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return ErrInvalidInvocation
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidInvocation
		}
		if entry.IsDir() {
			if !expectedDirs[relative] || info.Mode().Perm() != 0o555 {
				return ErrInvalidInvocation
			}
			return nil
		}
		if relative == StoredBundleReceiptPath {
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0o444 || info.Size() != int64(len(receiptPayload)) {
				return ErrInvalidInvocation
			}
			return nil
		}
		expected, ok := expectedFiles[relative]
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != os.FileMode(expected.Mode) || info.Size() != expected.Size ||
			verifyRuntimeFileDigest(current, info, expected.SHA256) != nil {
			return ErrInvalidInvocation
		}
		seenFiles[relative] = true
		return nil
	})
	rootAfter, rootErr := os.Lstat(root)
	if err != nil || rootErr != nil || !os.SameFile(rootInfo, rootAfter) || len(seenFiles) != len(expectedFiles) {
		return StoredBundleEvidence{}, ErrInvalidInvocation
	}
	return StoredBundleEvidence{
		BundleFingerprint: expectedFingerprint, ToolchainAttestationSHA256: attestationSHA256,
	}, nil
}

func verifyRuntimeClosureAttestationPayloads(manifestPayload, attestationPayload []byte, goos, goarch string) (string, error) {
	if goos == "" || goarch == "" || strings.ContainsAny(goos+goarch, "\x00\r\n/\\ ") {
		return "", ErrInvalidInvocation
	}
	var manifest runtimeClosureManifest
	if err := decodeCanonicalToolchainJSON(manifestPayload, maximumRuntimeClosureBytes, &manifest); err != nil {
		return "", err
	}
	platform := goos + "/" + goarch
	if manifest.SchemaVersion != 1 || manifest.Platform != platform || len(manifest.Files) == 0 || len(manifest.Files) > maximumRuntimeClosureFiles {
		return "", ErrInvalidInvocation
	}
	seen := make(map[string]bool, len(manifest.Files))
	var total int64
	for index, file := range manifest.Files {
		if !filepath.IsAbs(file.Path) || filepath.Clean(file.Path) != file.Path || file.Path == string(os.PathSeparator) ||
			strings.ContainsAny(file.Path, "\x00\r\n") || seen[file.Path] || index > 0 && file.Path <= manifest.Files[index-1].Path ||
			(file.Kind != "regular" && file.Kind != "symlink") || file.Mode < 0 || file.Mode > 0o777 ||
			file.Kind == "regular" && file.Mode&0o022 != 0 || file.Kind == "symlink" && file.Mode != 0o777 || file.Size < 0 ||
			file.Size > maximumRuntimeClosureFileBytes-total || !lowerHexToolchain(file.SHA256, 64) {
			return "", ErrInvalidInvocation
		}
		seen[file.Path] = true
		total += file.Size
	}
	var attestations runtimeClosureAttestations
	if err := decodeCanonicalToolchainJSON(attestationPayload, 64<<10, &attestations); err != nil ||
		attestations.SchemaVersion != 1 || len(attestations.Attestations) != 2 {
		return "", ErrInvalidInvocation
	}
	expectedPlatforms := []string{"linux/amd64", "linux/arm64"}
	expectedDigest := ""
	for index, attestation := range attestations.Attestations {
		if attestation.Platform != expectedPlatforms[index] || !lowerHexToolchain(attestation.RuntimeManifestSHA256, 64) {
			return "", ErrInvalidInvocation
		}
		if attestation.Platform == platform {
			expectedDigest = attestation.RuntimeManifestSHA256
		}
	}
	manifestDigest := SHA256Hex(manifestPayload)
	if expectedDigest == "" || expectedDigest != manifestDigest {
		return "", ErrInvalidInvocation
	}
	for index, file := range manifest.Files {
		if err := verifyRuntimeClosureDeclaredFile(index, file); err != nil {
			return "", err
		}
	}
	return manifestDigest, nil
}

func verifyRuntimeClosureDeclaredFile(index int, file runtimeClosureFile) error {
	info, err := os.Lstat(file.Path)
	if err != nil {
		return newRuntimeClosureVerificationError(RuntimeClosureFailureMetadata, index)
	}
	return verifyRuntimeClosureDeclaredFileInfo(index, file, info)
}

func verifyRuntimeClosureDeclaredFileInfo(index int, file runtimeClosureFile, info os.FileInfo) error {
	if info.Mode().Perm() != os.FileMode(file.Mode) || info.Size() != file.Size {
		return newRuntimeClosureVerificationError(RuntimeClosureFailureMetadata, index)
	}
	switch file.Kind {
	case "regular":
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return newRuntimeClosureVerificationError(RuntimeClosureFailureMetadata, index)
		}
		if err := verifyRuntimeFileDigest(file.Path, info, file.SHA256); err != nil {
			return runtimeClosureFailureAtFileIndex(err, index)
		}
	case "symlink":
		if info.Mode()&os.ModeSymlink == 0 {
			return newRuntimeClosureVerificationError(RuntimeClosureFailureMetadata, index)
		}
		if err := verifyRuntimeSymlinkDigest(file.Path, info, file.SHA256); err != nil {
			return runtimeClosureFailureAtFileIndex(err, index)
		}
	default:
		return newRuntimeClosureVerificationError(RuntimeClosureFailureEvidence, RuntimeClosureNoFileIndex)
	}
	return nil
}

func verifyBoundRuntimeClosureAttestationPayloads(
	manifestPayload, attestationPayload []byte,
	expectedAttestationSHA256, goos, goarch string,
) (string, error) {
	if !lowerHexToolchain(expectedAttestationSHA256, 64) || SHA256Hex(attestationPayload) != expectedAttestationSHA256 {
		return "", ErrInvalidInvocation
	}
	return verifyRuntimeClosureAttestationPayloads(manifestPayload, attestationPayload, goos, goarch)
}

func VerifyProductionRuntimeClosure(runtimeManifestPath, activeBundleRoot, goos, goarch, expectedAttestationSHA256 string) error {
	if !filepath.IsAbs(runtimeManifestPath) || filepath.Clean(runtimeManifestPath) != runtimeManifestPath ||
		!filepath.IsAbs(activeBundleRoot) || filepath.Clean(activeBundleRoot) != activeBundleRoot ||
		!lowerHexToolchain(expectedAttestationSHA256, 64) {
		return ErrInvalidInvocation
	}
	manifestInfo, err := os.Lstat(runtimeManifestPath)
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 ||
		manifestInfo.Mode().Perm() != 0o444 || !runtimeClosureManifestOwnerOK(manifestInfo) {
		return ErrInvalidInvocation
	}
	manifestPayload, err := readBoundedToolchainFile(runtimeManifestPath, maximumRuntimeClosureBytes)
	if err != nil {
		return ErrInvalidInvocation
	}
	attestationPath := filepath.Join(activeBundleRoot, filepath.FromSlash(ToolchainAttestationsBundlePath))
	attestationInfo, err := os.Lstat(attestationPath)
	if err != nil || !attestationInfo.Mode().IsRegular() || attestationInfo.Mode()&os.ModeSymlink != 0 || attestationInfo.Mode().Perm() != 0o444 {
		return ErrInvalidInvocation
	}
	attestationPayload, err := readBoundedToolchainFile(attestationPath, 64<<10)
	if err != nil {
		return ErrInvalidInvocation
	}
	if _, err := verifyBoundRuntimeClosureAttestationPayloads(
		manifestPayload, attestationPayload, expectedAttestationSHA256, goos, goarch,
	); err != nil {
		return err
	}
	manifestAfter, manifestErr := os.Lstat(runtimeManifestPath)
	attestationAfter, attestationErr := os.Lstat(attestationPath)
	if manifestErr != nil || attestationErr != nil || !os.SameFile(manifestInfo, manifestAfter) || !os.SameFile(attestationInfo, attestationAfter) {
		return newRuntimeClosureVerificationError(RuntimeClosureFailureRace, RuntimeClosureNoFileIndex)
	}
	return nil
}

func gateCapabilitiesByRuntimeClosure(ready map[string]bool, closureErr error) map[string]bool {
	result := make(map[string]bool, len(ready))
	for capability, available := range ready {
		result[capability] = available && closureErr == nil
	}
	return result
}

func decodeCanonicalToolchainJSON(payload []byte, maximum int, target any) error {
	if len(payload) == 0 || len(payload) > maximum || !utf8.Valid(payload) || !json.Valid(payload) {
		return ErrInvalidInvocation
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || ensureToolchainJSONEOF(decoder) != nil {
		return ErrInvalidInvocation
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(canonical, payload) {
		return ErrInvalidInvocation
	}
	return nil
}

func ensureToolchainJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrInvalidInvocation
	}
	return nil
}

func readBoundedToolchainFile(filePath string, maximum int) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(payload) == 0 || len(payload) > maximum {
		return nil, ErrInvalidInvocation
	}
	return payload, nil
}

func verifyRuntimeFileDigest(filePath string, before os.FileInfo, expected string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return newRuntimeClosureVerificationError(RuntimeClosureFailureRead, RuntimeClosureNoFileIndex)
	}
	digest := sha256.New()
	written, copyErr := io.Copy(digest, io.LimitReader(file, before.Size()+1))
	closeErr := file.Close()
	after, statErr := os.Lstat(filePath)
	if copyErr != nil || closeErr != nil {
		return newRuntimeClosureVerificationError(RuntimeClosureFailureRead, RuntimeClosureNoFileIndex)
	}
	if statErr != nil || written != before.Size() || !os.SameFile(before, after) {
		return newRuntimeClosureVerificationError(RuntimeClosureFailureRace, RuntimeClosureNoFileIndex)
	}
	if hex.EncodeToString(digest.Sum(nil)) != expected {
		return newRuntimeClosureVerificationError(RuntimeClosureFailureDigest, RuntimeClosureNoFileIndex)
	}
	return nil
}

func verifyRuntimeSymlinkDigest(filePath string, before os.FileInfo, expected string) error {
	target, err := os.Readlink(filePath)
	after, statErr := os.Lstat(filePath)
	if err != nil || statErr != nil {
		return newRuntimeClosureVerificationError(RuntimeClosureFailureRead, RuntimeClosureNoFileIndex)
	}
	if int64(len(target)) != before.Size() || !os.SameFile(before, after) {
		return newRuntimeClosureVerificationError(RuntimeClosureFailureRace, RuntimeClosureNoFileIndex)
	}
	if strings.ContainsAny(target, "\x00\r\n") || SHA256Hex([]byte(target)) != expected {
		return newRuntimeClosureVerificationError(RuntimeClosureFailureSymlink, RuntimeClosureNoFileIndex)
	}
	return nil
}

func validStoredBundlePath(value string) bool {
	if value == "" || len(value) > 240 || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\x00\r\n") || strings.HasPrefix(value, "/") {
		return false
	}
	clean := path.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || len(strings.Split(clean, "/")) > 16 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func lowerHexToolchain(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validToolchainIdentifier(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}

type toolchainPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Runtime bool   `json:"runtime"`
}

type toolchainAsset struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Executable bool   `json:"executable"`
}

type toolchainProbe struct {
	ID         string   `json:"id"`
	Executable string   `json:"executable"`
	Arguments  []string `json:"arguments"`
	Required   []string `json:"required"`
}

type toolchainComponent struct {
	ID       string           `json:"id"`
	Revision string           `json:"revision"`
	Packages []string         `json:"packages,omitempty"`
	Assets   []toolchainAsset `json:"assets,omitempty"`
	Probes   []toolchainProbe `json:"probes,omitempty"`
}

type toolchainProfile struct {
	Capability string   `json:"capability"`
	Components []string `json:"components"`
}

type toolchainInventory struct {
	SchemaVersion string               `json:"schema_version"`
	BuilderBase   string               `json:"builder_base"`
	RuntimeBase   string               `json:"runtime_base"`
	Packages      []toolchainPackage   `json:"packages"`
	Components    []toolchainComponent `json:"components"`
	Profiles      []toolchainProfile   `json:"profiles"`
	Policies      []string             `json:"policies"`
}

type toolchainInspection struct {
	Packages map[string]string
	Assets   map[string]bool
	Probes   map[string]string
}

// productionToolchainInventory is the single ordered contract shared by Core
// and Worker. Package revisions mirror the exact versions in deploy/worker/
// Dockerfile; data/font/codec identities are checked again at Worker startup.
func productionToolchainInventory() toolchainInventory {
	return toolchainInventory{
		SchemaVersion: "xirang.asset.toolchain.v2",
		BuilderBase:   "golang:1.26.6-alpine@sha256:af8d6740070b8906d12eae1c3e3ea0957fb63f492051ea05e354c38ef9fe88df",
		RuntimeBase:   "alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40",
		Packages: []toolchainPackage{
			{Name: "bash", Version: "5.3.3-r1", Runtime: true},
			{Name: "clamav", Version: "1.4.4-r0", Runtime: true},
			{Name: "ffmpeg", Version: "8.0.1-r1", Runtime: true},
			{Name: "font-noto", Version: "2025.12.01-r0", Runtime: true},
			{Name: "font-noto-cjk", Version: "0_git20220127-r1", Runtime: true},
			{Name: "gcc", Version: "15.2.0-r5"},
			{Name: "gzip", Version: "1.14-r2", Runtime: true},
			{Name: "libreoffice", Version: "25.8.1.1-r5", Runtime: true},
			{Name: "musl-dev", Version: "1.2.6-r2"},
			{Name: "poppler-utils", Version: "25.12.0-r0", Runtime: true},
			{Name: "tesseract-ocr", Version: "5.5.1-r0", Runtime: true},
			{Name: "tesseract-ocr-data-chi_sim", Version: "5.5.1-r0", Runtime: true},
			{Name: "tesseract-ocr-data-eng", Version: "5.5.1-r0", Runtime: true},
			{Name: "vips-tools", Version: "8.17.3-r1", Runtime: true},
			{Name: "xz", Version: "5.8.3-r0", Runtime: true},
			{Name: "zstd", Version: "1.5.7-r2", Runtime: true},
		},
		Components: []toolchainComponent{
			{ID: "sandbox", Revision: "sandbox-v2", Packages: []string{"bash"}, Assets: []toolchainAsset{{ID: "sandbox-helper", Path: productionSandboxBinary, Executable: true}}},
			{ID: "vips", Revision: "vips-8.17.3", Packages: []string{"vips-tools"}, Assets: []toolchainAsset{{ID: "vips", Path: "/usr/bin/vips", Executable: true}}},
			{ID: "tesseract", Revision: "tesseract-5.5.1", Packages: []string{"tesseract-ocr", "tesseract-ocr-data-eng", "tesseract-ocr-data-chi_sim"}, Assets: []toolchainAsset{
				{ID: "tesseract", Path: "/usr/bin/tesseract", Executable: true},
				{ID: "tessdata-eng", Path: "/usr/share/tessdata/eng.traineddata"},
				{ID: "tessdata-chi-sim", Path: "/usr/share/tessdata/chi_sim.traineddata"},
			}, Probes: []toolchainProbe{{ID: "tesseract-languages", Executable: "/usr/bin/tesseract", Arguments: []string{"--list-langs"}, Required: []string{"eng", "chi_sim"}}}},
			{ID: "poppler", Revision: "poppler-25.12.0", Packages: []string{"poppler-utils"}, Assets: []toolchainAsset{
				{ID: "pdftocairo", Path: "/usr/bin/pdftocairo", Executable: true},
				{ID: "pdftotext", Path: "/usr/bin/pdftotext", Executable: true},
			}},
			{ID: "libreoffice", Revision: "libreoffice-25.8.1.1", Packages: []string{"libreoffice"}, Assets: []toolchainAsset{{ID: "soffice", Path: "/usr/lib/libreoffice/program/soffice.bin", Executable: true}}},
			{ID: "fonts", Revision: "noto-2025.12.01+noto-cjk-20220127", Packages: []string{"font-noto", "font-noto-cjk"}, Assets: []toolchainAsset{
				{ID: "noto-sans", Path: "/usr/share/fonts/noto/NotoSans-Regular.ttf"},
				{ID: "noto-cjk", Path: "/usr/share/fonts/noto/NotoSansCJK-Regular.ttc"},
			}},
			{ID: "clamav", Revision: "clamav-1.4.4", Packages: []string{"clamav"}, Assets: []toolchainAsset{{ID: "clamscan", Path: "/usr/bin/clamscan", Executable: true}}},
			{ID: "ffmpeg", Revision: "ffmpeg-8.0.1", Packages: []string{"ffmpeg"}, Assets: []toolchainAsset{
				{ID: "ffprobe", Path: "/usr/bin/ffprobe", Executable: true},
				{ID: "ffmpeg", Path: "/usr/bin/ffmpeg", Executable: true},
			}, Probes: []toolchainProbe{
				{ID: "ffmpeg-codecs", Executable: "/usr/bin/ffmpeg", Arguments: []string{"-hide_banner", "-codecs"}, Required: []string{"h264", "vp8", "vp9", "aac", "mp3", "vorbis", "pcm_s16le", "png"}},
				{ID: "ffmpeg-encoders", Executable: "/usr/bin/ffmpeg", Arguments: []string{"-hide_banner", "-encoders"}, Required: []string{"libx264", "aac", "png"}},
			}},
			{ID: "gzip", Revision: "gzip-1.14", Packages: []string{"gzip"}, Assets: []toolchainAsset{{ID: "gzip", Path: "/bin/gzip", Executable: true}}},
			{ID: "xz", Revision: "xz-5.8.3", Packages: []string{"xz"}, Assets: []toolchainAsset{{ID: "xz", Path: "/usr/bin/xz", Executable: true}}},
			{ID: "zstd", Revision: "zstd-1.5.7", Packages: []string{"zstd"}, Assets: []toolchainAsset{{ID: "zstd", Path: "/usr/bin/zstd", Executable: true}}},
			{ID: "builtin-text", Revision: "bounded-text-v1"},
			{ID: "builtin-archive", Revision: "archive-parser-v1"},
			{ID: "builtin-secret", Revision: "secret-policy-v1"},
		},
		Profiles: []toolchainProfile{
			{Capability: capabilityspec.CapabilityImageThumbnail, Components: []string{"sandbox", "vips"}},
			{Capability: capabilityspec.CapabilityTextExtract, Components: []string{"sandbox", "builtin-text"}},
			{Capability: capabilityspec.CapabilityImageOCR, Components: []string{"sandbox", "vips", "tesseract"}},
			{Capability: capabilityspec.CapabilityDocumentConvert, Components: []string{"sandbox", "poppler", "libreoffice", "fonts"}},
			{Capability: capabilityspec.CapabilityMalwareScan, Components: []string{"sandbox", "clamav"}},
			{Capability: capabilityspec.CapabilityMediaProbe, Components: []string{"sandbox", "ffmpeg"}},
			{Capability: capabilityspec.CapabilityMediaTranscode, Components: []string{"sandbox", "ffmpeg"}},
			{Capability: capabilityspec.CapabilityArchiveInspect, Components: []string{"sandbox", "builtin-archive", "gzip", "xz", "zstd"}},
			{Capability: capabilityspec.CapabilityArchiveExtractEntry, Components: []string{"sandbox", "builtin-archive", "gzip", "xz", "zstd"}},
			{Capability: capabilityspec.CapabilitySecretClassify, Components: []string{"sandbox", "builtin-secret"}},
		},
		Policies: []string{"image-static-raster-v1", "document-static-preflight-v1", "media-file-pipe-codecs-v1", "archive-single-stream-v1", "sandbox-no-network-v2"},
	}
}

// ProductionToolchainFingerprint returns the digest of the canonical closed
// inventory. Both Core and Worker call this function, so they cannot drift by
// carrying separate hand-maintained fingerprint literals.
func ProductionToolchainFingerprint() string {
	fingerprint, err := toolchainFingerprint(productionToolchainInventory())
	if err != nil {
		return ""
	}
	return fingerprint
}

// PreflightProductionToolchain checks the immutable package database, fixed
// binaries, language/font assets and codec/language probes. A failed component
// only removes the profiles that depend on it; no profile is advertised with a
// partially verified runtime.
func PreflightProductionToolchain(ctx context.Context, activeBundleRoot, expectedAttestationSHA256 string) ToolchainPreflight {
	inventory := productionToolchainInventory()
	fingerprint, _ := toolchainFingerprint(inventory)
	inspection := inspectProductionToolchain(ctx, inventory)
	ready := evaluateToolchainPreflight(ctx, inventory, inspection)
	closureErr := VerifyProductionRuntimeClosure(
		ProductionRuntimeClosureManifestPath, activeBundleRoot, runtime.GOOS, runtime.GOARCH, expectedAttestationSHA256,
	)
	return newToolchainPreflight(fingerprint, ready, closureErr)
}

func newToolchainPreflight(fingerprint string, ready map[string]bool, closureErr error) ToolchainPreflight {
	ungatedAvailable := 0
	for _, available := range ready {
		if available {
			ungatedAvailable++
		}
	}
	failureCategory, failureFileIndex := runtimeClosureFailureDiagnostic(closureErr)
	return ToolchainPreflight{
		Fingerprint:                    fingerprint,
		AvailableCapabilities:          gateCapabilitiesByRuntimeClosure(ready, closureErr),
		UngatedAvailableCount:          ungatedAvailable,
		RuntimeClosureReady:            closureErr == nil,
		RuntimeClosureFailureCategory:  failureCategory,
		RuntimeClosureFailureFileIndex: failureFileIndex,
	}
}

func cloneToolchainInventory(value toolchainInventory) toolchainInventory {
	result := value
	result.Packages = append([]toolchainPackage(nil), value.Packages...)
	result.Components = append([]toolchainComponent(nil), value.Components...)
	for index := range result.Components {
		result.Components[index].Assets = append([]toolchainAsset(nil), value.Components[index].Assets...)
		result.Components[index].Probes = append([]toolchainProbe(nil), value.Components[index].Probes...)
		for probe := range result.Components[index].Probes {
			result.Components[index].Probes[probe].Arguments = append([]string(nil), value.Components[index].Probes[probe].Arguments...)
			result.Components[index].Probes[probe].Required = append([]string(nil), value.Components[index].Probes[probe].Required...)
		}
		result.Components[index].Packages = append([]string(nil), value.Components[index].Packages...)
	}
	result.Profiles = append([]toolchainProfile(nil), value.Profiles...)
	for index := range result.Profiles {
		result.Profiles[index].Components = append([]string(nil), value.Profiles[index].Components...)
	}
	result.Policies = append([]string(nil), value.Policies...)
	return result
}

func toolchainFingerprint(value toolchainInventory) (string, error) {
	canonical, err := canonicalToolchainInventory(value)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("xirang.asset.toolchain.v2\x00"))
	_, _ = digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func canonicalToolchainInventory(value toolchainInventory) (toolchainInventory, error) {
	result := cloneToolchainInventory(value)
	if result.SchemaVersion == "" || result.BuilderBase == "" || result.RuntimeBase == "" || len(result.Components) == 0 || len(result.Profiles) == 0 {
		return toolchainInventory{}, ErrInvalidInvocation
	}
	for index := range result.Packages {
		if result.Packages[index].Name == "" || result.Packages[index].Version == "" {
			return toolchainInventory{}, ErrInvalidInvocation
		}
	}
	sort.Slice(result.Packages, func(left, right int) bool { return result.Packages[left].Name < result.Packages[right].Name })
	for index := 1; index < len(result.Packages); index++ {
		if result.Packages[index-1].Name == result.Packages[index].Name {
			return toolchainInventory{}, ErrInvalidInvocation
		}
	}
	packageNames := make(map[string]bool, len(result.Packages))
	for _, packageValue := range result.Packages {
		packageNames[packageValue.Name] = true
	}
	sort.Slice(result.Components, func(left, right int) bool { return result.Components[left].ID < result.Components[right].ID })
	sort.Slice(result.Profiles, func(left, right int) bool {
		return result.Profiles[left].Capability < result.Profiles[right].Capability
	})
	sort.Strings(result.Policies)
	seenComponents := make(map[string]bool, len(result.Components))
	seenAssets := make(map[string]bool)
	seenProbes := make(map[string]bool)
	for index := range result.Components {
		component := &result.Components[index]
		if component.ID == "" || component.Revision == "" || seenComponents[component.ID] {
			return toolchainInventory{}, ErrInvalidInvocation
		}
		seenComponents[component.ID] = true
		sort.Strings(component.Packages)
		for _, packageName := range component.Packages {
			if !packageNames[packageName] {
				return toolchainInventory{}, ErrInvalidInvocation
			}
		}
		sort.Slice(component.Assets, func(left, right int) bool { return component.Assets[left].ID < component.Assets[right].ID })
		for _, asset := range component.Assets {
			if asset.ID == "" || !filepath.IsAbs(asset.Path) || filepath.Clean(asset.Path) != asset.Path ||
				strings.ContainsAny(asset.Path, "\x00\r\n") || seenAssets[asset.Path] {
				return toolchainInventory{}, ErrInvalidInvocation
			}
			seenAssets[asset.Path] = true
		}
		sort.Slice(component.Probes, func(left, right int) bool { return component.Probes[left].ID < component.Probes[right].ID })
		for probe := range component.Probes {
			if component.Probes[probe].ID == "" || !filepath.IsAbs(component.Probes[probe].Executable) ||
				len(component.Probes[probe].Arguments) == 0 || len(component.Probes[probe].Required) == 0 ||
				seenProbes[component.Probes[probe].ID] {
				return toolchainInventory{}, ErrInvalidInvocation
			}
			seenProbes[component.Probes[probe].ID] = true
			sort.Strings(component.Probes[probe].Required)
		}
	}
	seenProfiles := make(map[string]bool, len(result.Profiles))
	for index := range result.Profiles {
		if result.Profiles[index].Capability == "" || seenProfiles[result.Profiles[index].Capability] {
			return toolchainInventory{}, ErrInvalidInvocation
		}
		seenProfiles[result.Profiles[index].Capability] = true
		sort.Strings(result.Profiles[index].Components)
		for _, component := range result.Profiles[index].Components {
			if !seenComponents[component] {
				return toolchainInventory{}, ErrInvalidInvocation
			}
		}
	}
	expectedProfiles := capabilityspec.WorkerProfiles()
	if len(expectedProfiles) != len(seenProfiles) {
		return toolchainInventory{}, ErrInvalidInvocation
	}
	for _, profile := range expectedProfiles {
		if !seenProfiles[profile.Capability] {
			return toolchainInventory{}, ErrInvalidInvocation
		}
	}
	for index, policy := range result.Policies {
		if policy == "" || strings.ContainsAny(policy, "\x00\r\n/\\ ") || index > 0 && policy == result.Policies[index-1] {
			return toolchainInventory{}, ErrInvalidInvocation
		}
	}
	return result, nil
}

func inspectProductionToolchain(ctx context.Context, inventory toolchainInventory) toolchainInspection {
	inspection := toolchainInspection{Packages: readAPKPackageVersions(), Assets: make(map[string]bool), Probes: make(map[string]string)}
	for _, component := range inventory.Components {
		for _, asset := range component.Assets {
			inspection.Assets[asset.Path] = inspectToolchainAsset(asset)
		}
		for _, probe := range component.Probes {
			inspection.Probes[probe.ID] = runToolchainProbe(ctx, probe)
		}
	}
	return inspection
}

func inspectToolchainAsset(asset toolchainAsset) bool {
	info, err := os.Lstat(asset.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	if asset.Executable && info.Mode()&0o111 == 0 {
		return false
	}
	return true
}

func readAPKPackageVersions() map[string]string {
	info, err := os.Lstat(productionAPKDatabase)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return nil
	}
	file, err := os.Open(productionAPKDatabase)
	if err != nil {
		return nil
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, 4<<20))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(payload) >= 4<<20 {
		return nil
	}
	result := make(map[string]string)
	var name, version string
	flush := func() {
		if name != "" && version != "" {
			result[name] = version
		}
		name, version = "", ""
	}
	for _, line := range strings.Split(string(payload), "\n") {
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "P:") {
			name = strings.TrimPrefix(line, "P:")
		} else if strings.HasPrefix(line, "V:") {
			version = strings.TrimPrefix(line, "V:")
		}
	}
	flush()
	return result
}

type boundedProbeOutput struct {
	bytes.Buffer
	limited bool
}

func (output *boundedProbeOutput) Write(payload []byte) (int, error) {
	const maximum = 1 << 20
	if output.Len()+len(payload) > maximum {
		remaining := maximum - output.Len()
		if remaining > 0 {
			_, _ = output.Buffer.Write(payload[:remaining])
		}
		output.limited = true
		return len(payload), nil
	}
	return output.Buffer.Write(payload)
}

func runToolchainProbe(ctx context.Context, probe toolchainProbe) string {
	if ctx == nil {
		return ""
	}
	probeContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(probeContext, probe.Executable, probe.Arguments...)
	command.Env = []string{"HOME=/nonexistent", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC", "PATH="}
	stdout, stderr := &boundedProbeOutput{}, &boundedProbeOutput{}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil || stdout.limited || stderr.limited {
		return ""
	}
	return stdout.String() + "\n" + stderr.String()
}

func evaluateToolchainPreflight(_ context.Context, inventory toolchainInventory, inspection toolchainInspection) map[string]bool {
	componentReady := make(map[string]bool, len(inventory.Components))
	for _, component := range inventory.Components {
		ready := true
		for _, packageName := range component.Packages {
			if inspection.Packages[packageName] != packageVersion(inventory, packageName) {
				ready = false
			}
		}
		for _, asset := range component.Assets {
			if !inspection.Assets[asset.Path] {
				ready = false
			}
		}
		for _, probe := range component.Probes {
			output := inspection.Probes[probe.ID]
			for _, required := range probe.Required {
				if !probeOutputContainsIdentity(output, required) {
					ready = false
				}
			}
		}
		componentReady[component.ID] = ready
	}
	result := make(map[string]bool, len(inventory.Profiles))
	for _, profile := range inventory.Profiles {
		ready := true
		for _, component := range profile.Components {
			if !componentReady[component] {
				ready = false
			}
		}
		result[profile.Capability] = ready
	}
	return result
}

func probeOutputContainsIdentity(output, identity string) bool {
	for _, field := range strings.Fields(output) {
		if field == identity {
			return true
		}
	}
	return false
}

func packageVersion(inventory toolchainInventory, name string) string {
	for _, packageValue := range inventory.Packages {
		if packageValue.Name == name {
			return packageValue.Version
		}
	}
	return ""
}

type executableResolver func(ExecutableID) (string, error)

type SandboxExecution struct {
	Executable     string
	Profile        ToolArgProfile
	Args           []string
	Workspace      string
	InputMode      ToolInputMode
	InputPath      string
	OutputDir      string
	HomeDir        string
	CPUTime        time.Duration
	MaxMemoryBytes int64
	MaxFileBytes   int64
	MaxProcesses   int
}

func ExecuteSandbox(request SandboxExecution) error {
	if err := validateSandboxExecution(request); err != nil {
		return err
	}
	return executeSandbox(request)
}

func validateSandboxExecution(value SandboxExecution) error {
	resources := SandboxResourceLimits{
		CPUTime: value.CPUTime, MaxMemoryBytes: value.MaxMemoryBytes,
		MaxFileBytes: value.MaxFileBytes, MaxProcesses: value.MaxProcesses,
	}
	if !allowedSandboxExecutableProfile(value.Executable, value.Profile) || !cleanWorkspaceRoot(value.Workspace) ||
		!strings.HasPrefix(filepath.Base(value.Workspace), "job-") ||
		value.OutputDir != filepath.Join(value.Workspace, "output") || value.HomeDir != filepath.Join(value.Workspace, "home") ||
		resources.ValidateFor(value.Profile) != nil || len(value.Args) == 0 || len(value.Args) > 64 {
		return ErrInvalidInvocation
	}
	switch value.InputMode {
	case ToolInputPipe:
		if value.InputPath != "" {
			return ErrInvalidInvocation
		}
	case ToolInputPath:
		if value.InputPath != filepath.Join(value.Workspace, "input.bin") {
			return ErrInvalidInvocation
		}
	default:
		return ErrInvalidInvocation
	}
	for _, argument := range value.Args {
		lower := strings.ToLower(argument)
		if argument == "" || len(argument) > 4096 || strings.ContainsAny(argument, "\x00\r\n") ||
			strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(lower, "sh -c") {
			return ErrInvalidInvocation
		}
	}
	return nil
}

func allowedSandboxExecutableProfile(executable string, profile ToolArgProfile) bool {
	switch executable {
	case "/usr/bin/vips":
		return profile == ArgsVipsThumbnail || profile == ArgsVipsNormalize
	case "/usr/bin/tesseract":
		return profile == ArgsTesseractOCR
	case "/usr/bin/pdftocairo":
		return profile == ArgsPDFPages
	case "/usr/bin/pdftotext":
		return profile == ArgsPDFText
	case "/usr/lib/libreoffice/program/soffice.bin":
		return profile == ArgsOfficePDF
	case "/usr/bin/clamscan":
		return profile == ArgsClamScan
	case "/usr/bin/ffprobe":
		return profile == ArgsMediaProbe
	case "/usr/bin/ffmpeg":
		return profile == ArgsMediaPreview
	case "/bin/gzip":
		return profile == ArgsGzipDecompress
	case "/usr/bin/xz":
		return profile == ArgsXZDecompress
	case "/usr/bin/zstd":
		return profile == ArgsZstdDecompress
	default:
		return false
	}
}

func productionExecutableResolver(ExecutableID) (string, error) {
	info, err := os.Stat(productionSandboxBinary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return "", ErrSecureWorkspaceUnavailable
	}
	return productionSandboxBinary, nil
}

func cleanWorkspaceRoot(root string) bool {
	return filepath.IsAbs(root) && filepath.Clean(root) == root && root != "/" && !strings.ContainsAny(root, "\x00\r\n")
}

func sanitizeProcessError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrPermission) {
		return ErrSecureWorkspaceUnavailable
	}
	return ErrToolFailed
}
