package processing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

var (
	ErrInvalidContract   = errors.New("invalid processing contract")
	ErrInvalidTransition = errors.New("invalid processing transition")
	ErrRevisionConflict  = errors.New("processing transition revision conflict")
)

type PriorityClass string

const (
	PriorityInteractive PriorityClass = "interactive"
	PriorityBackground  PriorityClass = "background"
)

type UpdaterSourceKind string

const (
	UpdaterSourceBuiltin         UpdaterSourceKind = "builtin"
	UpdaterSourceAdminRegistered UpdaterSourceKind = "admin_registered"
)

type UpdaterMetadataState string

const (
	UpdaterMetadataRegistered UpdaterMetadataState = "registered"
	UpdaterMetadataVerified   UpdaterMetadataState = "verified"
	UpdaterMetadataActive     UpdaterMetadataState = "active"
	UpdaterMetadataSuperseded UpdaterMetadataState = "superseded"
	UpdaterMetadataFailed     UpdaterMetadataState = "failed"
)

type UpdaterFailureCode string

const (
	UpdaterFailureInvalidSignature   UpdaterFailureCode = "invalid_signature"
	UpdaterFailureUnsupportedVersion UpdaterFailureCode = "unsupported_version"
	UpdaterFailurePolicyRejected     UpdaterFailureCode = "policy_rejected"
	UpdaterFailureActivationFailed   UpdaterFailureCode = "activation_failed"
)

// UpdaterMetadataV1 is a metadata-only seam for Child 11. It deliberately has
// no URL, credential, private key, manifest body, bundle bytes, or raw output.
type UpdaterMetadataV1 struct {
	SchemaVersion         int                  `json:"schema_version"`
	SourceKind            UpdaterSourceKind    `json:"source_kind"`
	SourceID              string               `json:"source_id"`
	Version               string               `json:"version"`
	ManifestDigest        string               `json:"manifest_digest"`
	SigningKeyFingerprint string               `json:"signing_key_fingerprint"`
	BundleFingerprint     string               `json:"bundle_fingerprint"`
	State                 UpdaterMetadataState `json:"state"`
	FailureCode           UpdaterFailureCode   `json:"failure_code"`
	VerifiedAt            *time.Time           `json:"verified_at"`
	ActivatedAt           *time.Time           `json:"activated_at"`
}

var updaterSemanticVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

func DecodeUpdaterMetadataV1(payload []byte) (UpdaterMetadataV1, error) {
	if len(payload) == 0 || len(payload) > 64*1024 || !utf8.Valid(payload) || !json.Valid(payload) {
		return UpdaterMetadataV1{}, ErrInvalidContract
	}
	if err := rejectDuplicateJSONMembers(payload); err != nil {
		return UpdaterMetadataV1{}, ErrInvalidContract
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var value UpdaterMetadataV1
	if err := decoder.Decode(&value); err != nil || ensureJSONEOF(decoder) != nil {
		return UpdaterMetadataV1{}, ErrInvalidContract
	}
	if err := ValidateUpdaterMetadataV1(value); err != nil {
		return UpdaterMetadataV1{}, err
	}
	return value, nil
}

func ValidateUpdaterMetadataV1(value UpdaterMetadataV1) error {
	if value.SchemaVersion != 1 || !validUpdaterSourceKind(value.SourceKind) ||
		!validUpdaterSourceID(value.SourceID) || len(value.Version) > 64 || !updaterSemanticVersion.MatchString(value.Version) ||
		!lowerHex(value.ManifestDigest, 64) || !lowerHex(value.SigningKeyFingerprint, 64) || !lowerHex(value.BundleFingerprint, 64) {
		return ErrInvalidContract
	}
	if !utcUpdaterTime(value.VerifiedAt) || !utcUpdaterTime(value.ActivatedAt) ||
		(value.ActivatedAt != nil && (value.VerifiedAt == nil || value.ActivatedAt.Before(*value.VerifiedAt))) {
		return ErrInvalidContract
	}
	switch value.State {
	case UpdaterMetadataRegistered:
		if value.VerifiedAt != nil || value.ActivatedAt != nil || value.FailureCode != "" {
			return ErrInvalidContract
		}
	case UpdaterMetadataVerified:
		if value.VerifiedAt == nil || value.ActivatedAt != nil || value.FailureCode != "" {
			return ErrInvalidContract
		}
	case UpdaterMetadataActive:
		if value.VerifiedAt == nil || value.ActivatedAt == nil || value.FailureCode != "" {
			return ErrInvalidContract
		}
	case UpdaterMetadataSuperseded:
		if value.VerifiedAt == nil || value.FailureCode != "" {
			return ErrInvalidContract
		}
	case UpdaterMetadataFailed:
		if !validUpdaterFailureCode(value.FailureCode) {
			return ErrInvalidContract
		}
	default:
		return ErrInvalidContract
	}
	return nil
}

func validUpdaterSourceKind(value UpdaterSourceKind) bool {
	return value == UpdaterSourceBuiltin || value == UpdaterSourceAdminRegistered
}

func validUpdaterSourceID(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validUpdaterFailureCode(value UpdaterFailureCode) bool {
	switch value {
	case UpdaterFailureInvalidSignature, UpdaterFailureUnsupportedVersion, UpdaterFailurePolicyRejected, UpdaterFailureActivationFailed:
		return true
	default:
		return false
	}
}

func utcUpdaterTime(value *time.Time) bool {
	return value == nil || value.Location() == time.UTC
}

type CanonicalParametersV1 struct {
	SchemaVersion           int    `json:"schema_version"`
	Width                   int    `json:"width"`
	Height                  int    `json:"height"`
	Codec                   string `json:"codec"`
	PageStart               int    `json:"page_start"`
	PageEnd                 int    `json:"page_end"`
	Quality                 int    `json:"quality"`
	Language                string `json:"language"`
	Model                   string `json:"model"`
	FontProfile             string `json:"font_profile"`
	MemberStart             int    `json:"member_start"`
	MemberEnd               int    `json:"member_end"`
	FrameStart              int64  `json:"frame_start"`
	FrameEnd                int64  `json:"frame_end"`
	TimeStartMillis         int64  `json:"time_start_millis"`
	TimeEndMillis           int64  `json:"time_end_millis"`
	Orientation             string `json:"orientation"`
	CropX                   int    `json:"crop_x"`
	CropY                   int    `json:"crop_y"`
	CropWidth               int    `json:"crop_width"`
	CropHeight              int    `json:"crop_height"`
	MaxPages                int64  `json:"max_pages"`
	MaxPixels               int64  `json:"max_pixels"`
	MaxDurationMillis       int64  `json:"max_duration_millis"`
	MaxExpandedBytes        int64  `json:"max_expanded_bytes"`
	MaxOutputBytes          int64  `json:"max_output_bytes"`
	MaxOutputCount          int    `json:"max_output_count"`
	TruncationPolicy        string `json:"truncation_policy"`
	RequiresMaterialization bool   `json:"requires_materialization"`
}

type WorkDescriptorV1 struct {
	SchemaVersion              int                   `json:"schema_version"`
	Source                     backupasset.AssetRef  `json:"source"`
	CatalogGenerationID        string                `json:"catalog_generation_id"`
	SourceFingerprint          string                `json:"source_fingerprint"`
	EntryFingerprint           string                `json:"entry_fingerprint"`
	ProviderCapabilityRevision int64                 `json:"provider_capability_revision"`
	Capability                 string                `json:"capability"`
	CapabilitySchema           string                `json:"capability_schema"`
	PipelineFingerprint        string                `json:"pipeline_fingerprint"`
	OutputProfile              string                `json:"output_profile"`
	SecurityPolicyRevision     string                `json:"security_policy_revision"`
	Parameters                 CanonicalParametersV1 `json:"parameters"`
}

func ValidateWorkDescriptorV1(value WorkDescriptorV1) error {
	if value.SchemaVersion != 1 || backupasset.ValidateAssetRef(value.Source) != nil ||
		backupasset.ValidateOpaqueID(value.CatalogGenerationID) != nil || value.ProviderCapabilityRevision <= 0 {
		return fmt.Errorf("%w: invalid descriptor identity", ErrInvalidContract)
	}
	for name, field := range map[string]string{
		"source_fingerprint":       value.SourceFingerprint,
		"capability":               value.Capability,
		"capability_schema":        value.CapabilitySchema,
		"pipeline_fingerprint":     value.PipelineFingerprint,
		"output_profile":           value.OutputProfile,
		"security_policy_revision": value.SecurityPolicyRevision,
	} {
		if strings.TrimSpace(field) == "" || len(field) > 128 {
			return fmt.Errorf("%w: invalid %s", ErrInvalidContract, name)
		}
	}
	if len(value.EntryFingerprint) > 128 {
		return fmt.Errorf("%w: invalid entry_fingerprint", ErrInvalidContract)
	}
	return ValidateCanonicalParametersV1(value.Parameters)
}

func ValidateProductionWorkDescriptorV1(value WorkDescriptorV1, secretEnabled bool) error {
	if err := ValidateWorkDescriptorV1(value); err != nil {
		return err
	}
	profile, ok := capabilityspec.Lookup(value.Capability, value.OutputProfile, secretEnabled)
	if !ok || value.CapabilitySchema != profile.CapabilitySchema ||
		value.Parameters.RequiresMaterialization != profile.RequiresMaterialization ||
		value.Parameters.Codec != productionCodec(profile.Capability) {
		return fmt.Errorf("%w: descriptor does not match a closed production profile", ErrInvalidContract)
	}
	limits := profile.Limits
	parameters := value.Parameters
	if parameters.MaxPages > limits.MaxPages || parameters.MaxPixels > limits.MaxPixels ||
		parameters.MaxDurationMillis > limits.MaxDurationMillis || parameters.MaxExpandedBytes > limits.MaxExpandedBytes ||
		parameters.MaxOutputBytes > limits.MaxOutputBytes || parameters.MaxOutputCount > limits.MaxOutputCount {
		return fmt.Errorf("%w: descriptor exceeds production profile ceilings", ErrInvalidContract)
	}
	return nil
}

func productionCodec(capability string) string {
	switch capability {
	case capabilityspec.CapabilityImageThumbnail:
		return "webp"
	case capabilityspec.CapabilityTextExtract, capabilityspec.CapabilityImageOCR,
		capabilityspec.CapabilityArchiveInspect, capabilityspec.CapabilitySecretClassify:
		return "text"
	case capabilityspec.CapabilityDocumentConvert:
		return "pdf"
	case capabilityspec.CapabilityMediaTranscode:
		return "mp4"
	default:
		return "noop"
	}
}

func ValidateCanonicalParametersV1(value CanonicalParametersV1) error {
	if value.SchemaVersion != 1 || value.Width <= 0 || value.Width > 65535 ||
		value.Height <= 0 || value.Height > 65535 || value.Quality <= 0 || value.Quality > 100 ||
		value.PageStart <= 0 || value.PageEnd < value.PageStart || value.PageEnd > 100000 ||
		value.MemberStart < 0 || value.MemberEnd < value.MemberStart ||
		value.FrameStart < 0 || value.FrameEnd < value.FrameStart ||
		value.TimeStartMillis < 0 || value.TimeEndMillis < value.TimeStartMillis ||
		value.CropX < 0 || value.CropY < 0 || value.CropWidth <= 0 || value.CropHeight <= 0 ||
		value.CropX+value.CropWidth > value.Width || value.CropY+value.CropHeight > value.Height ||
		value.MaxPages <= 0 || value.MaxPages > 100000 || value.MaxPixels <= 0 ||
		value.MaxDurationMillis <= 0 || value.MaxExpandedBytes <= 0 || value.MaxOutputBytes <= 0 ||
		value.MaxOutputCount <= 0 || value.MaxOutputCount > 256 {
		return fmt.Errorf("%w: output-affecting numeric parameter is out of bounds", ErrInvalidContract)
	}
	if !oneOf(value.Codec, "noop", "png", "jpeg", "webp", "pdf", "text", "mp4") ||
		!oneOf(value.Orientation, "auto", "none", "rotate90", "rotate180", "rotate270") ||
		!oneOf(value.TruncationPolicy, "reject", "partial") {
		return fmt.Errorf("%w: output-affecting enum parameter is invalid", ErrInvalidContract)
	}
	for _, field := range []string{value.Language, value.Model, value.FontProfile} {
		if strings.TrimSpace(field) == "" || len(field) > 128 {
			return fmt.Errorf("%w: output-affecting string parameter is invalid", ErrInvalidContract)
		}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
