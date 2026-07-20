package capabilityspec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const (
	CapabilityImageThumbnail      = "image.thumbnail"
	CapabilityTextExtract         = "text.extract"
	CapabilityImageOCR            = "image.ocr"
	CapabilityDocumentConvert     = "document.convert"
	CapabilityMalwareScan         = "malware.scan"
	CapabilityMediaProbe          = "media.probe"
	CapabilityMediaTranscode      = "media.transcode"
	CapabilityArchiveInspect      = "archive.inspect"
	CapabilityArchiveExtractEntry = "archive.extract_entry"
	CapabilitySecretClassify      = "secret.classify"

	ProfileRasterThumbnailV1 = "raster_thumbnail_v1"
	ProfileBoundedTextV1     = "bounded_text_v1"
	ProfileTesseractTextV1   = "tesseract_text_v1"
	ProfileStaticPagesV1     = "static_pages_v1"
	ProfileSignatureScanV1   = "signature_scan_v1"
	ProfileMediaProbeV1      = "media_probe_v1"
	ProfileBrowserPreviewV1  = "browser_preview_v1"
	ProfileArchiveIndexV1    = "archive_index_v1"
	ProfileArchiveMemberV1   = "archive_member_v1"
	ProfileBoundedSecretV1   = "bounded_secret_v1"
)

type InputMode string

const (
	InputStat       InputMode = "stat"
	InputSequential InputMode = "sequential"
	InputRange      InputMode = "range"
)

type OutputRule struct {
	Role       string   `json:"role"`
	MediaTypes []string `json:"media_types"`
	Maximum    int      `json:"maximum"`
}

type Limits struct {
	MaxInputBytes       int64         `json:"max_input_bytes"`
	MaxOutputBytes      int64         `json:"max_output_bytes"`
	MaxOutputCount      int           `json:"max_output_count"`
	MaxPages            int64         `json:"max_pages"`
	MaxRenderedPages    int64         `json:"max_rendered_pages"`
	MaxPixels           int64         `json:"max_pixels"`
	MaxFrames           int64         `json:"max_frames"`
	MaxDurationMillis   int64         `json:"max_duration_millis"`
	MaxExpandedBytes    int64         `json:"max_expanded_bytes"`
	MaxArchiveEntries   int64         `json:"max_archive_entries"`
	MaxArchiveDepth     int64         `json:"max_archive_depth"`
	MaxCompressionRatio int64         `json:"max_compression_ratio"`
	MaxMemberBytes      int64         `json:"max_member_bytes"`
	MaxStreams          int64         `json:"max_streams"`
	MaxRunes            int64         `json:"max_runes"`
	MaxLines            int64         `json:"max_lines"`
	WallTime            time.Duration `json:"wall_time"`
}

type Identity struct {
	Capability    string
	OutputProfile string
}

type Profile struct {
	SchemaVersion           int          `json:"schema_version"`
	Capability              string       `json:"capability"`
	CapabilitySchema        string       `json:"capability_schema"`
	OutputProfile           string       `json:"output_profile"`
	ExecutableID            string       `json:"executable_id"`
	InputModes              []InputMode  `json:"input_modes"`
	InputMIMEs              []string     `json:"input_mimes"`
	Outputs                 []OutputRule `json:"outputs"`
	Limits                  Limits       `json:"limits"`
	RequiresMaterialization bool         `json:"requires_materialization"`
	EnabledByDefault        bool         `json:"enabled_by_default"`
}

func (value Profile) Identity() Identity {
	return Identity{Capability: value.Capability, OutputProfile: value.OutputProfile}
}

func (value Profile) Validate() error {
	if value.SchemaVersion != 1 || validateClosedIdentifier(value.Capability, 64) != nil ||
		validateClosedIdentifier(value.CapabilitySchema, 64) != nil || validateClosedIdentifier(value.OutputProfile, 64) != nil ||
		!allowedExecutable(value.ExecutableID) || len(value.InputModes) == 0 || len(value.InputMIMEs) == 0 || len(value.Outputs) == 0 {
		return ErrInvalidContract
	}
	if value.Limits.MaxInputBytes < 64<<10 || value.Limits.MaxInputBytes > 16<<30 ||
		value.Limits.MaxOutputBytes <= 0 || value.Limits.MaxOutputBytes > value.Limits.MaxInputBytes ||
		value.Limits.MaxOutputCount <= 0 || value.Limits.MaxOutputCount > 256 ||
		value.Limits.MaxPages <= 0 || value.Limits.MaxPixels <= 0 || value.Limits.MaxDurationMillis <= 0 ||
		value.Limits.MaxExpandedBytes <= 0 || value.Limits.WallTime <= 0 || value.Limits.WallTime > 2*time.Hour {
		return ErrInvalidContract
	}
	modes := make(map[InputMode]bool, len(value.InputModes))
	for _, mode := range value.InputModes {
		if mode != InputStat && mode != InputSequential && mode != InputRange || modes[mode] {
			return ErrInvalidContract
		}
		modes[mode] = true
	}
	if !modes[InputStat] {
		return ErrInvalidContract
	}
	mimes := make(map[string]bool, len(value.InputMIMEs))
	for _, mediaType := range value.InputMIMEs {
		if !validMediaType(mediaType) || activeMediaType(mediaType) || mimes[mediaType] {
			return ErrInvalidContract
		}
		mimes[mediaType] = true
	}
	for _, output := range value.Outputs {
		if output.Role != "content" && output.Role != "ocr" && output.Role != "thumbnail" && output.Role != "metadata" ||
			output.Maximum <= 0 || output.Maximum > value.Limits.MaxOutputCount || len(output.MediaTypes) == 0 {
			return ErrInvalidContract
		}
		seen := make(map[string]bool, len(output.MediaTypes))
		for _, mediaType := range output.MediaTypes {
			if !validMediaType(mediaType) || activeMediaType(mediaType) || seen[mediaType] {
				return ErrInvalidContract
			}
			seen[mediaType] = true
		}
	}
	return nil
}

func (value Profile) ValidateMedia(declared, sniffed string) error {
	if value.Validate() != nil || declared == "" || declared != strings.TrimSpace(declared) || declared != sniffed || activeMediaType(declared) {
		return ErrUnsupportedMedia
	}
	for _, allowed := range value.InputMIMEs {
		if declared == allowed {
			return nil
		}
	}
	return ErrUnsupportedMedia
}

func (value Profile) CanonicalJSON() ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (value Profile) PipelineFingerprint(toolchain string, bundleFingerprints []string, securityPolicyRevision string) (string, error) {
	canonical, err := value.CanonicalJSON()
	if err != nil || validateClosedIdentifier(toolchain, 128) != nil || validateClosedIdentifier(securityPolicyRevision, 128) != nil {
		return "", ErrInvalidContract
	}
	for _, fingerprint := range bundleFingerprints {
		if !lowerHex(fingerprint, 64) {
			return "", ErrInvalidContract
		}
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("xirang.asset.pipeline.v1\x00"))
	_, _ = digest.Write(canonical)
	_, _ = digest.Write([]byte("\x00" + toolchain + "\x00"))
	for _, fingerprint := range bundleFingerprints {
		_, _ = digest.Write([]byte(fingerprint + "\x00"))
	}
	_, _ = digest.Write([]byte(securityPolicyRevision))
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func RequiredProfiles() []Profile {
	all := productionProfiles()
	result := make([]Profile, 0, len(all)-1)
	for _, profile := range all {
		if profile.Capability != CapabilitySecretClassify {
			result = append(result, cloneProfile(profile))
		}
	}
	return result
}

func AllProfiles(secretEnabled bool) []Profile {
	result := RequiredProfiles()
	if secretEnabled {
		for _, profile := range productionProfiles() {
			if profile.Capability == CapabilitySecretClassify {
				result = append(result, cloneProfile(profile))
			}
		}
	}
	return result
}

func WorkerProfiles() []Profile {
	return AllProfiles(true)
}

func Lookup(capability, outputProfile string, secretEnabled bool) (Profile, bool) {
	for _, profile := range AllProfiles(secretEnabled) {
		if profile.Capability == capability && profile.OutputProfile == outputProfile {
			return profile, true
		}
	}
	return Profile{}, false
}

func cloneProfile(value Profile) Profile {
	value.InputModes = append([]InputMode(nil), value.InputModes...)
	value.InputMIMEs = append([]string(nil), value.InputMIMEs...)
	value.Outputs = append([]OutputRule(nil), value.Outputs...)
	for index := range value.Outputs {
		value.Outputs[index].MediaTypes = append([]string(nil), value.Outputs[index].MediaTypes...)
	}
	return value
}

func allowedExecutable(value string) bool {
	switch value {
	case "libvips.thumbnail", "builtin.text", "tesseract.ocr", "poppler.document", "clamav.scan", "ffmpeg.probe", "ffmpeg.transcode", "builtin.archive", "builtin.secret":
		return true
	default:
		return false
	}
}

func validMediaType(value string) bool {
	if value == "" || len(value) > 96 || strings.TrimSpace(value) != value || strings.ToLower(value) != value || strings.Count(value, "/") != 1 {
		return false
	}
	return !strings.ContainsAny(value, " \t\r\n\x00;\\")
}

func activeMediaType(value string) bool {
	return value == "image/svg+xml" || value == "text/html" || value == "application/xhtml+xml"
}

func productionProfiles() []Profile {
	statStream := []InputMode{InputStat, InputSequential}
	statRange := []InputMode{InputStat, InputSequential, InputRange}
	profile := func(capability, schema, output, executable string, input []string, outputs []OutputRule, limits Limits, materialized, enabled bool, modes []InputMode) Profile {
		return Profile{SchemaVersion: 1, Capability: capability, CapabilitySchema: schema, OutputProfile: output,
			ExecutableID: executable, InputModes: modes, InputMIMEs: input, Outputs: outputs, Limits: limits,
			RequiresMaterialization: materialized, EnabledByDefault: enabled}
	}
	common := func(input, output int64, count int, pages, pixels, duration, expanded int64, wall time.Duration) Limits {
		return Limits{MaxInputBytes: input, MaxOutputBytes: output, MaxOutputCount: count, MaxPages: pages,
			MaxPixels: pixels, MaxDurationMillis: duration, MaxExpandedBytes: expanded, WallTime: wall}
	}
	values := []Profile{
		profile(CapabilityImageThumbnail, "image.thumbnail.v1", ProfileRasterThumbnailV1, "libvips.thumbnail",
			[]string{"image/jpeg", "image/png", "image/webp", "image/gif", "image/tiff", "image/bmp"},
			[]OutputRule{{Role: "thumbnail", MediaTypes: []string{"image/png", "image/webp"}, Maximum: 1}, {Role: "metadata", MediaTypes: []string{"application/json"}, Maximum: 1}},
			func() Limits {
				v := common(256<<20, 8<<20, 2, 1, 50_000_000, 90_000, 256<<20, 90*time.Second)
				v.MaxFrames = 8
				return v
			}(), true, true, statRange),
		profile(CapabilityTextExtract, "text.extract.v1", ProfileBoundedTextV1, "builtin.text",
			[]string{"text/plain", "text/csv", "text/markdown", "application/json", "application/xml"},
			[]OutputRule{{Role: "content", MediaTypes: []string{"text/plain"}, Maximum: 1}, {Role: "metadata", MediaTypes: []string{"application/json"}, Maximum: 1}},
			func() Limits {
				v := common(64<<20, 4<<20, 2, 1, 1, 60_000, 64<<20, time.Minute)
				v.MaxRunes = 1_000_000
				v.MaxLines = 100_000
				return v
			}(), false, true, statStream),
		profile(CapabilityImageOCR, "image.ocr.v1", ProfileTesseractTextV1, "tesseract.ocr",
			[]string{"image/jpeg", "image/png", "image/webp", "image/tiff"},
			[]OutputRule{{Role: "ocr", MediaTypes: []string{"text/plain"}, Maximum: 1}, {Role: "metadata", MediaTypes: []string{"application/json"}, Maximum: 1}},
			common(128<<20, 8<<20, 2, 8, 20_000_000, 300_000, 128<<20, 5*time.Minute), true, true, statRange),
		profile(CapabilityDocumentConvert, "document.convert.v1", ProfileStaticPagesV1, "poppler.document",
			[]string{"application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "application/vnd.oasis.opendocument.text", "application/vnd.oasis.opendocument.spreadsheet", "application/vnd.oasis.opendocument.presentation"},
			[]OutputRule{{Role: "thumbnail", MediaTypes: []string{"image/png"}, Maximum: 30}, {Role: "content", MediaTypes: []string{"application/pdf", "text/plain"}, Maximum: 1}, {Role: "metadata", MediaTypes: []string{"application/json"}, Maximum: 1}},
			func() Limits {
				v := common(256<<20, 64<<20, 32, 100, 50_000_000, 600_000, 256<<20, 10*time.Minute)
				v.MaxRenderedPages = 30
				return v
			}(), true, true, statRange),
		profile(CapabilityMalwareScan, "malware.scan.v1", ProfileSignatureScanV1, "clamav.scan",
			[]string{"application/octet-stream", "application/pdf", "image/jpeg", "image/png", "text/plain", "application/zip"},
			[]OutputRule{{Role: "metadata", MediaTypes: []string{"application/json"}, Maximum: 1}}, common(1<<30, 64<<10, 1, 1, 1, 600_000, 1<<30, 10*time.Minute), true, true, statStream),
		profile(CapabilityMediaProbe, "media.probe.v1", ProfileMediaProbeV1, "ffmpeg.probe",
			[]string{"video/mp4", "video/webm", "audio/mpeg", "audio/mp4", "audio/ogg", "audio/wav"},
			[]OutputRule{{Role: "metadata", MediaTypes: []string{"application/json"}, Maximum: 1}},
			func() Limits {
				v := common(512<<20, 1<<20, 1, 1, 3840*2160, 7_200_000, 512<<20, 2*time.Minute)
				v.MaxStreams = 32
				return v
			}(), true, true, statRange),
		profile(CapabilityMediaTranscode, "media.transcode.v1", ProfileBrowserPreviewV1, "ffmpeg.transcode",
			[]string{"video/mp4", "video/webm", "audio/mpeg", "audio/mp4", "audio/ogg", "audio/wav"},
			[]OutputRule{{Role: "content", MediaTypes: []string{"video/mp4", "video/webm", "audio/mpeg", "audio/mp4", "audio/ogg"}, Maximum: 1}, {Role: "thumbnail", MediaTypes: []string{"image/png", "image/jpeg"}, Maximum: 1}, {Role: "metadata", MediaTypes: []string{"application/json"}, Maximum: 1}},
			func() Limits {
				v := common(2<<30, 512<<20, 3, 1, 3840*2160, 1_800_000, 2<<30, 30*time.Minute)
				v.MaxStreams = 32
				return v
			}(), true, true, statRange),
		profile(CapabilityArchiveInspect, "archive.inspect.v1", ProfileArchiveIndexV1, "builtin.archive",
			[]string{"application/zip", "application/x-tar", "application/gzip", "application/x-xz", "application/zstd"},
			[]OutputRule{{Role: "metadata", MediaTypes: []string{"application/json"}, Maximum: 1}},
			func() Limits {
				v := common(2<<30, 16<<20, 1, 1, 1, 600_000, 8<<30, 10*time.Minute)
				v.MaxArchiveEntries = 100_000
				v.MaxArchiveDepth = 16
				v.MaxCompressionRatio = 100
				return v
			}(), false, true, statStream),
		profile(CapabilityArchiveExtractEntry, "archive.extract_entry.v1", ProfileArchiveMemberV1, "builtin.archive",
			[]string{"application/zip", "application/x-tar", "application/gzip", "application/x-xz", "application/zstd"},
			[]OutputRule{{Role: "content", MediaTypes: []string{"application/octet-stream", "text/plain", "image/png", "image/jpeg", "application/pdf"}, Maximum: 1}, {Role: "metadata", MediaTypes: []string{"application/json"}, Maximum: 1}},
			func() Limits {
				v := common(2<<30, 256<<20, 2, 1, 1, 600_000, 8<<30, 10*time.Minute)
				v.MaxArchiveEntries = 100_000
				v.MaxArchiveDepth = 16
				v.MaxCompressionRatio = 100
				v.MaxMemberBytes = 256 << 20
				return v
			}(), false, true, statStream),
		profile(CapabilitySecretClassify, "secret.classify.v1", ProfileBoundedSecretV1, "builtin.secret",
			[]string{"text/plain"}, []OutputRule{{Role: "metadata", MediaTypes: []string{"application/json"}, Maximum: 1}},
			common(16<<20, 256<<10, 1, 1, 1, 60_000, 16<<20, time.Minute), false, false, statStream),
	}
	for index := range values {
		sort.Strings(values[index].InputMIMEs)
		for output := range values[index].Outputs {
			sort.Strings(values[index].Outputs[output].MediaTypes)
		}
	}
	return values
}
