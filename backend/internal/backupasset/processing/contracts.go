package processing

import (
	"errors"
	"fmt"
	"strings"

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
