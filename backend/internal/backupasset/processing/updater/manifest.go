package updater

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidSignature   = errors.New("updater manifest signature invalid")
	ErrUnsupportedVersion = errors.New("updater manifest version unsupported")
	ErrPolicyRejected     = errors.New("updater manifest policy rejected")
	ErrActivationFailed   = errors.New("updater activation failed")
)

const (
	maximumBundleBytes = int64(1 << 30)
	maximumBundleFiles = 100_000
)

var semanticVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

type Manifest struct {
	SchemaVersion      int                  `json:"schema_version"`
	SourceKind         string               `json:"source_kind"`
	SourceID           string               `json:"source_id"`
	Version            string               `json:"version"`
	CreatedAt          time.Time            `json:"created_at"`
	ExpiresAt          time.Time            `json:"expires_at"`
	Capabilities       []ManifestCapability `json:"capabilities"`
	Files              []ManifestFile       `json:"files"`
	BundleSHA256       string               `json:"bundle_sha256"`
	SigningKeyID       string               `json:"signing_key_id"`
	SignatureAlgorithm string               `json:"signature_algorithm"`
	Signature          string               `json:"signature"`
}

type ManifestCapability struct {
	Capability    string   `json:"capability"`
	Schema        string   `json:"schema"`
	Profiles      []string `json:"profiles"`
	ToolRevision  string   `json:"tool_revision"`
	ModelRevision string   `json:"model_revision"`
	DataRevision  string   `json:"data_revision"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	Mode   int64  `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type BundleFilePayload struct {
	Path    string
	Mode    int64
	Content []byte
}

type TrustedKey struct {
	ID          string
	PublicKey   ed25519.PublicKey
	ActiveFrom  time.Time
	RetireAfter time.Time
}

type TrustStore struct {
	Keys []TrustedKey
}

type trustStoreDocument struct {
	SchemaVersion int                  `json:"schema_version"`
	Keys          []trustedKeyDocument `json:"keys"`
}

type trustedKeyDocument struct {
	ID          string `json:"id"`
	PublicKey   string `json:"public_key"`
	ActiveFrom  string `json:"active_from"`
	RetireAfter string `json:"retire_after"`
}

type VerifiedBundle struct {
	Manifest              Manifest
	ManifestDigest        string
	SigningKeyFingerprint string
	BundleFingerprint     string
	Files                 []BundleFilePayload
}

func SHA256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func DecodeTrustStore(payload []byte) (TrustStore, error) {
	if len(payload) == 0 || len(payload) > 64<<10 || !utf8.Valid(payload) || !json.Valid(payload) ||
		rejectDuplicateJSONMembers(payload) != nil {
		return TrustStore{}, ErrPolicyRejected
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var document trustStoreDocument
	if decoder.Decode(&document) != nil || ensureJSONEOF(decoder) != nil || document.SchemaVersion != 1 ||
		len(document.Keys) == 0 || len(document.Keys) > 64 {
		return TrustStore{}, ErrPolicyRejected
	}
	result := TrustStore{Keys: make([]TrustedKey, 0, len(document.Keys))}
	lastID := ""
	fingerprints := make(map[string]bool, len(document.Keys))
	for _, key := range document.Keys {
		publicKey, err := base64.StdEncoding.DecodeString(key.PublicKey)
		activeFrom, activeErr := time.Parse(time.RFC3339, key.ActiveFrom)
		retireAfter, retireErr := time.Parse(time.RFC3339, key.RetireAfter)
		fingerprint := SHA256Hex(publicKey)
		if err != nil || len(publicKey) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(publicKey) != key.PublicKey ||
			activeErr != nil || retireErr != nil || activeFrom.Location() != time.UTC || retireAfter.Location() != time.UTC ||
			activeFrom.Format(time.RFC3339) != key.ActiveFrom || retireAfter.Format(time.RFC3339) != key.RetireAfter ||
			!retireAfter.After(activeFrom) || !validIdentifier(key.ID, 128) || key.ID <= lastID || fingerprints[fingerprint] {
			return TrustStore{}, ErrPolicyRejected
		}
		lastID = key.ID
		fingerprints[fingerprint] = true
		result.Keys = append(result.Keys, TrustedKey{
			ID: key.ID, PublicKey: ed25519.PublicKey(append([]byte(nil), publicKey...)),
			ActiveFrom: activeFrom, RetireAfter: retireAfter,
		})
	}
	return result, nil
}

func BuildCanonicalTar(files []BundleFilePayload) ([]byte, []ManifestFile, error) {
	if len(files) == 0 || len(files) > maximumBundleFiles {
		return nil, nil, ErrPolicyRejected
	}
	ordered := make([]BundleFilePayload, len(files))
	for index, file := range files {
		ordered[index] = BundleFilePayload{Path: file.Path, Mode: file.Mode, Content: append([]byte(nil), file.Content...)}
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Path < ordered[right].Path })
	manifestFiles := make([]ManifestFile, 0, len(ordered))
	seenFolded := make(map[string]bool, len(ordered))
	var total int64
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, file := range ordered {
		if !validBundlePath(file.Path) || file.Mode != 0o444 || seenFolded[strings.ToLower(file.Path)] ||
			int64(len(file.Content)) > maximumBundleBytes-total {
			_ = writer.Close()
			return nil, nil, ErrPolicyRejected
		}
		seenFolded[strings.ToLower(file.Path)] = true
		total += int64(len(file.Content))
		header := &tar.Header{
			Name: file.Path, Mode: file.Mode, Size: int64(len(file.Content)), Typeflag: tar.TypeReg,
			ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
		}
		if err := writer.WriteHeader(header); err != nil {
			_ = writer.Close()
			return nil, nil, ErrPolicyRejected
		}
		if _, err := writer.Write(file.Content); err != nil {
			_ = writer.Close()
			return nil, nil, ErrPolicyRejected
		}
		manifestFiles = append(manifestFiles, ManifestFile{
			Path: file.Path, Mode: file.Mode, Size: int64(len(file.Content)), SHA256: SHA256Hex(file.Content),
		})
	}
	if err := writer.Close(); err != nil || int64(buffer.Len()) > maximumBundleBytes {
		return nil, nil, ErrPolicyRejected
	}
	return buffer.Bytes(), manifestFiles, nil
}

func SignManifest(manifest *Manifest, privateKey ed25519.PrivateKey) error {
	if manifest == nil || len(privateKey) != ed25519.PrivateKeySize {
		return ErrInvalidSignature
	}
	manifest.Signature = ""
	if err := validateManifest(*manifest, manifest.CreatedAt, false); err != nil {
		return err
	}
	payload, err := manifestSigningPayload(*manifest)
	if err != nil {
		return err
	}
	manifest.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return nil
}

func VerifyPackage(manifestJSON, bundle []byte, trust TrustStore, now time.Time) (VerifiedBundle, error) {
	verified, err := VerifyManifest(manifestJSON, trust, now)
	if err != nil {
		return VerifiedBundle{}, err
	}
	return verifyPackageBundle(verified, bundle)
}

func VerifyManifest(manifestJSON []byte, trust TrustStore, now time.Time) (VerifiedBundle, error) {
	if len(manifestJSON) == 0 || len(manifestJSON) > 1<<20 || !utf8.Valid(manifestJSON) || !json.Valid(manifestJSON) ||
		rejectDuplicateJSONMembers(manifestJSON) != nil {
		return VerifiedBundle{}, ErrPolicyRejected
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestJSON))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil || ensureJSONEOF(decoder) != nil {
		return VerifiedBundle{}, ErrPolicyRejected
	}
	canonical, err := json.Marshal(manifest)
	if err != nil || !bytes.Equal(canonical, manifestJSON) {
		return VerifiedBundle{}, ErrPolicyRejected
	}
	now = now.UTC()
	if err := validateManifest(manifest, now, true); err != nil {
		return VerifiedBundle{}, err
	}
	trusted, ok := trust.lookup(manifest.SigningKeyID, now)
	if !ok {
		return VerifiedBundle{}, ErrInvalidSignature
	}
	signature, err := base64.RawStdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return VerifiedBundle{}, ErrInvalidSignature
	}
	signed, err := manifestSigningPayload(manifest)
	if err != nil || !ed25519.Verify(trusted.PublicKey, signed, signature) {
		return VerifiedBundle{}, ErrInvalidSignature
	}
	fingerprint, err := bundleFingerprint(manifest)
	if err != nil {
		return VerifiedBundle{}, ErrPolicyRejected
	}
	return VerifiedBundle{
		Manifest: manifest, ManifestDigest: SHA256Hex(manifestJSON),
		SigningKeyFingerprint: SHA256Hex(trusted.PublicKey), BundleFingerprint: fingerprint,
	}, nil
}

func verifyPackageBundle(verified VerifiedBundle, bundle []byte) (VerifiedBundle, error) {
	files := make([]BundleFilePayload, 0, len(verified.Manifest.Files))
	err := verifyCanonicalTarStream(
		context.Background(), bytes.NewReader(bundle), verified.Manifest.Files, verified.Manifest.BundleSHA256,
		func(_ context.Context, declaration ManifestFile, content io.Reader) error {
			payload, readErr := io.ReadAll(content)
			if readErr != nil {
				return readErr
			}
			files = append(files, BundleFilePayload{Path: declaration.Path, Mode: declaration.Mode, Content: payload})
			return nil
		},
	)
	if err != nil {
		return VerifiedBundle{}, err
	}
	verified.Files = files
	return verified, nil
}

func (store TrustStore) lookup(id string, now time.Time) (TrustedKey, bool) {
	for _, key := range store.Keys {
		if key.ID == id && len(key.PublicKey) == ed25519.PublicKeySize && key.ActiveFrom.Location() == time.UTC &&
			key.RetireAfter.Location() == time.UTC && !key.RetireAfter.Before(key.ActiveFrom) &&
			!now.Before(key.ActiveFrom) && now.Before(key.RetireAfter) {
			return key, true
		}
	}
	return TrustedKey{}, false
}

func validateManifest(value Manifest, now time.Time, requireSignature bool) error {
	if value.SchemaVersion != 1 {
		return ErrUnsupportedVersion
	}
	if (value.SourceKind != "builtin" && value.SourceKind != "admin_registered") || !validIdentifier(value.SourceID, 128) ||
		!semanticVersion.MatchString(value.Version) || value.CreatedAt.Location() != time.UTC || value.ExpiresAt.Location() != time.UTC ||
		!value.ExpiresAt.After(value.CreatedAt) || now.Before(value.CreatedAt) || !now.Before(value.ExpiresAt) ||
		!lowerHex(value.BundleSHA256, 64) || !validIdentifier(value.SigningKeyID, 128) || value.SignatureAlgorithm != "ed25519" ||
		len(value.Capabilities) == 0 || len(value.Capabilities) > 32 || len(value.Files) == 0 || len(value.Files) > maximumBundleFiles {
		return ErrPolicyRejected
	}
	if requireSignature && value.Signature == "" {
		return ErrInvalidSignature
	}
	lastCapability := ""
	for _, capability := range value.Capabilities {
		identity := capability.Capability + "\x00" + capability.Schema
		if !validIdentifier(capability.Capability, 64) || !validIdentifier(capability.Schema, 64) || identity <= lastCapability ||
			!validIdentifier(capability.ToolRevision, 128) || !validIdentifier(capability.ModelRevision, 128) ||
			!validIdentifier(capability.DataRevision, 128) || len(capability.Profiles) == 0 || len(capability.Profiles) > 16 {
			return ErrPolicyRejected
		}
		lastCapability = identity
		lastProfile := ""
		for _, profile := range capability.Profiles {
			if !validIdentifier(profile, 64) || profile <= lastProfile {
				return ErrPolicyRejected
			}
			lastProfile = profile
		}
	}
	lastPath := ""
	seenFolded := make(map[string]bool, len(value.Files))
	var total int64
	for _, file := range value.Files {
		folded := strings.ToLower(file.Path)
		if !validBundlePath(file.Path) || file.Path <= lastPath || seenFolded[folded] || file.Mode != 0o444 || file.Size < 0 ||
			file.Size > maximumBundleBytes-total || !lowerHex(file.SHA256, 64) {
			return ErrPolicyRejected
		}
		lastPath = file.Path
		seenFolded[folded] = true
		total += file.Size
	}
	return nil
}

func manifestSigningPayload(value Manifest) ([]byte, error) {
	unsigned := struct {
		SchemaVersion      int                  `json:"schema_version"`
		SourceKind         string               `json:"source_kind"`
		SourceID           string               `json:"source_id"`
		Version            string               `json:"version"`
		CreatedAt          time.Time            `json:"created_at"`
		ExpiresAt          time.Time            `json:"expires_at"`
		Capabilities       []ManifestCapability `json:"capabilities"`
		Files              []ManifestFile       `json:"files"`
		BundleSHA256       string               `json:"bundle_sha256"`
		SigningKeyID       string               `json:"signing_key_id"`
		SignatureAlgorithm string               `json:"signature_algorithm"`
	}{
		value.SchemaVersion, value.SourceKind, value.SourceID, value.Version, value.CreatedAt, value.ExpiresAt,
		value.Capabilities, value.Files, value.BundleSHA256, value.SigningKeyID, value.SignatureAlgorithm,
	}
	canonical, err := json.Marshal(unsigned)
	if err != nil {
		return nil, ErrPolicyRejected
	}
	return append([]byte("xirang.asset.bundle.manifest.v1\x00"), canonical...), nil
}

func bundleFingerprint(value Manifest) (string, error) {
	content := struct {
		SchemaVersion int                  `json:"schema_version"`
		Capabilities  []ManifestCapability `json:"capabilities"`
		Files         []ManifestFile       `json:"files"`
		BundleSHA256  string               `json:"bundle_sha256"`
	}{value.SchemaVersion, value.Capabilities, value.Files, value.BundleSHA256}
	canonical, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("xirang.asset.bundle.fingerprint.v1\x00"))
	_, _ = digest.Write(canonical)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type canonicalTarMemberConsumer func(context.Context, ManifestFile, io.Reader) error

func verifyCanonicalTarStream(
	ctx context.Context,
	source io.Reader,
	expected []ManifestFile,
	expectedBundleSHA256 string,
	consume canonicalTarMemberConsumer,
) error {
	if ctx == nil || source == nil || len(expected) == 0 || len(expected) > maximumBundleFiles ||
		!lowerHex(expectedBundleSHA256, 64) {
		return ErrPolicyRejected
	}
	reader := &canonicalBundleReader{ctx: ctx, source: source, digest: sha256.New()}
	for _, declaration := range expected {
		var rawHeader [tarBlockSize]byte
		if err := readCanonicalTarBytes(reader, rawHeader[:]); err != nil {
			return err
		}
		expectedHeader, err := canonicalUSTARHeader(declaration)
		if err != nil || !bytes.Equal(rawHeader[:], expectedHeader[:]) {
			return ErrPolicyRejected
		}
		member := &canonicalTarMemberReader{reader: reader, remaining: declaration.Size, digest: sha256.New()}
		if consume == nil {
			if err := discardCanonicalTarMember(ctx, member); err != nil {
				return err
			}
		} else if err := consume(ctx, declaration, member); err != nil {
			return err
		}
		if member.remaining != 0 || hex.EncodeToString(member.digest.Sum(nil)) != declaration.SHA256 {
			return ErrPolicyRejected
		}
		padding := (tarBlockSize - declaration.Size%tarBlockSize) % tarBlockSize
		if padding > 0 {
			var rawPadding [tarBlockSize]byte
			if err := readCanonicalTarBytes(reader, rawPadding[:padding]); err != nil || !allZero(rawPadding[:padding]) {
				return ErrPolicyRejected
			}
		}
	}
	var terminal [2 * tarBlockSize]byte
	if err := readCanonicalTarBytes(reader, terminal[:]); err != nil || !allZero(terminal[:]) {
		return ErrPolicyRejected
	}
	var trailing [1]byte
	count, err := reader.Read(trailing[:])
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if count != 0 || !errors.Is(err, io.EOF) || reader.count > maximumBundleBytes ||
		hex.EncodeToString(reader.digest.Sum(nil)) != expectedBundleSHA256 {
		return ErrPolicyRejected
	}
	return nil
}

const tarBlockSize = int64(512)

type canonicalBundleReader struct {
	ctx    context.Context
	source io.Reader
	digest hash.Hash
	count  int64
}

func (reader *canonicalBundleReader) Read(payload []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := reader.source.Read(payload)
	if count > 0 {
		reader.count += int64(count)
		_, _ = reader.digest.Write(payload[:count])
		if reader.count > maximumBundleBytes {
			return count, ErrPolicyRejected
		}
	}
	if err != nil && !errors.Is(err, io.EOF) {
		if ctxErr := reader.ctx.Err(); ctxErr != nil {
			return count, ctxErr
		}
		return count, ErrPolicyRejected
	}
	return count, err
}

type canonicalTarMemberReader struct {
	reader    io.Reader
	remaining int64
	digest    hash.Hash
}

func (reader *canonicalTarMemberReader) Read(payload []byte) (int, error) {
	if reader.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(payload)) > reader.remaining {
		payload = payload[:reader.remaining]
	}
	count, err := reader.reader.Read(payload)
	if count > 0 {
		reader.remaining -= int64(count)
		_, _ = reader.digest.Write(payload[:count])
	}
	return count, err
}

func canonicalUSTARHeader(declaration ManifestFile) ([tarBlockSize]byte, error) {
	var result [tarBlockSize]byte
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	err := writer.WriteHeader(&tar.Header{
		Name: declaration.Path, Mode: declaration.Mode, Size: declaration.Size, Typeflag: tar.TypeReg,
		ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
	})
	if err != nil || buffer.Len() != len(result) {
		return result, ErrPolicyRejected
	}
	copy(result[:], buffer.Bytes())
	return result, nil
}

func readCanonicalTarBytes(reader io.Reader, payload []byte) error {
	if _, err := io.ReadFull(reader, payload); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrPolicyRejected
	}
	return nil
}

func discardCanonicalTarMember(ctx context.Context, reader io.Reader) error {
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := reader.Read(buffer)
		if count == 0 && errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return ErrPolicyRejected
		}
	}
}

func allZero(payload []byte) bool {
	for _, value := range payload {
		if value != 0 {
			return false
		}
	}
	return true
}

func validBundlePath(value string) bool {
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

func validIdentifier(value string, maximum int) bool {
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

func lowerHex(value string, length int) bool {
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

func rejectDuplicateJSONMembers(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]bool)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return ErrPolicyRejected
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return ErrPolicyRejected
		}
	}
	if err := walk(); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON", ErrPolicyRejected)
	}
	return nil
}
