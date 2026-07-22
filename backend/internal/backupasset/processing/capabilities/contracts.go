package capabilities

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidInvocation          = errors.New("invalid closed tool invocation")
	ErrSecureWorkspaceUnavailable = errors.New("secure tool workspace unavailable")
	ErrToolFailed                 = errors.New("sandboxed tool failed")
	ErrToolTimeout                = errors.New("sandboxed tool deadline exceeded")
	ErrInvalidToolOutput          = errors.New("invalid sandboxed tool output")
	ErrInputLimit                 = errors.New("capability input limit exceeded")
	ErrArchiveMember              = errors.New("invalid archive member")
	ErrArchiveEncrypted           = errors.New("encrypted archive unsupported")
)

type ExecutableID string

const (
	ExecutableVips           ExecutableID = "vips"
	ExecutableBuiltinText    ExecutableID = "builtin_text"
	ExecutableTesseract      ExecutableID = "tesseract"
	ExecutablePDFToCairo     ExecutableID = "pdftocairo"
	ExecutablePDFToText      ExecutableID = "pdftotext"
	ExecutableLibreOffice    ExecutableID = "libreoffice"
	ExecutableClamScan       ExecutableID = "clamscan"
	ExecutableFFProbe        ExecutableID = "ffprobe"
	ExecutableFFmpeg         ExecutableID = "ffmpeg"
	ExecutableGzip           ExecutableID = "gzip"
	ExecutableXZ             ExecutableID = "xz"
	ExecutableZstd           ExecutableID = "zstd"
	ExecutableBuiltinArchive ExecutableID = "builtin_archive"
	ExecutableBuiltinSecret  ExecutableID = "builtin_secret"
)

func (value ExecutableID) valid() bool {
	switch value {
	case ExecutableVips, ExecutableBuiltinText, ExecutableTesseract, ExecutablePDFToCairo,
		ExecutablePDFToText, ExecutableLibreOffice, ExecutableClamScan, ExecutableFFProbe,
		ExecutableFFmpeg, ExecutableGzip, ExecutableXZ, ExecutableZstd, ExecutableBuiltinArchive,
		ExecutableBuiltinSecret:
		return true
	default:
		return false
	}
}

type ToolArgProfile string

const (
	ArgsVipsThumbnail  ToolArgProfile = "vips_thumbnail_v1"
	ArgsVipsNormalize  ToolArgProfile = "vips_raster_normalize_v1"
	ArgsBuiltinText    ToolArgProfile = "builtin_text_v1"
	ArgsTesseractOCR   ToolArgProfile = "tesseract_ocr_v1"
	ArgsPDFPages       ToolArgProfile = "pdf_pages_v1"
	ArgsPDFText        ToolArgProfile = "pdf_text_v1"
	ArgsOfficePDF      ToolArgProfile = "office_pdf_v1"
	ArgsClamScan       ToolArgProfile = "clam_scan_v1"
	ArgsMediaProbe     ToolArgProfile = "media_probe_v1"
	ArgsMediaPreview   ToolArgProfile = "media_preview_v1"
	ArgsGzipDecompress ToolArgProfile = "gzip_decompress_v1"
	ArgsXZDecompress   ToolArgProfile = "xz_decompress_v1"
	ArgsZstdDecompress ToolArgProfile = "zstd_decompress_v1"
	ArgsArchive        ToolArgProfile = "archive_v1"
	ArgsSecret         ToolArgProfile = "secret_v1"
)

func (value ToolArgProfile) valid() bool {
	switch value {
	case ArgsVipsThumbnail, ArgsVipsNormalize, ArgsBuiltinText, ArgsTesseractOCR, ArgsPDFPages, ArgsPDFText,
		ArgsOfficePDF, ArgsClamScan, ArgsMediaProbe, ArgsMediaPreview, ArgsGzipDecompress,
		ArgsXZDecompress, ArgsZstdDecompress, ArgsArchive, ArgsSecret:
		return true
	default:
		return false
	}
}

type ToolInputMode string

const (
	ToolInputPipe ToolInputMode = "pipe"
	ToolInputPath ToolInputMode = "path"
)

type ClosedOutputSpec struct {
	MaximumFiles int
	AllowedNames []string
}

type ToolLimits struct {
	WallTime       time.Duration
	CPUTime        time.Duration
	MaxInputBytes  int64
	MaxOutputBytes int64
	MaxMemoryBytes int64
	MaxFileBytes   int64
	MaxProcesses   int
}

type SandboxResourceLimits struct {
	CPUTime        time.Duration
	MaxMemoryBytes int64
	MaxFileBytes   int64
	MaxProcesses   int
}

func (value SandboxResourceLimits) ValidateFor(profile ToolArgProfile) error {
	ceiling, ok := closedSandboxResourceCeiling(profile)
	if !ok || value.CPUTime <= 0 || value.CPUTime%time.Second != 0 || value.CPUTime > ceiling.CPUTime ||
		value.MaxMemoryBytes < 64<<20 || value.MaxMemoryBytes > ceiling.MaxMemoryBytes ||
		value.MaxFileBytes <= 0 || value.MaxFileBytes > ceiling.MaxFileBytes ||
		value.MaxProcesses <= 0 || value.MaxProcesses > ceiling.MaxProcesses {
		return ErrInvalidInvocation
	}
	return nil
}

func (value ToolLimits) sandboxResources() SandboxResourceLimits {
	return SandboxResourceLimits{
		CPUTime: value.CPUTime, MaxMemoryBytes: value.MaxMemoryBytes,
		MaxFileBytes: value.MaxFileBytes, MaxProcesses: value.MaxProcesses,
	}
}

type ToolInvocation struct {
	ExecutableID     ExecutableID
	ArgProfile       ToolArgProfile
	InputMode        ToolInputMode
	Args             []string
	Environment      []string
	OutputSpec       ClosedOutputSpec
	Limits           ToolLimits
	SuccessExitCodes []int
}

func (value ToolInvocation) Validate() error {
	if !value.ExecutableID.valid() || !value.ArgProfile.valid() ||
		(value.InputMode != ToolInputPipe && value.InputMode != ToolInputPath) ||
		value.OutputSpec.MaximumFiles <= 0 || value.OutputSpec.MaximumFiles > 32 ||
		len(value.OutputSpec.AllowedNames) == 0 || len(value.OutputSpec.AllowedNames) > value.OutputSpec.MaximumFiles ||
		value.Limits.WallTime <= 0 || value.Limits.WallTime > 2*time.Hour ||
		value.Limits.CPUTime > value.Limits.WallTime ||
		value.Limits.MaxInputBytes <= 0 || value.Limits.MaxInputBytes > 16<<30 ||
		value.Limits.MaxOutputBytes <= 0 || value.Limits.MaxOutputBytes > 8<<30 ||
		value.Limits.MaxFileBytes > value.Limits.MaxOutputBytes && value.ArgProfile != ArgsClamScan ||
		value.Limits.sandboxResources().ValidateFor(value.ArgProfile) != nil {
		return ErrInvalidInvocation
	}
	seen := make(map[string]bool, len(value.OutputSpec.AllowedNames))
	for _, name := range value.OutputSpec.AllowedNames {
		if name == "" || len(name) > 64 || seen[name] {
			return ErrInvalidInvocation
		}
		for _, character := range name {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
				continue
			}
			return ErrInvalidInvocation
		}
		seen[name] = true
	}
	if !validExecutableProfile(value.ExecutableID, value.ArgProfile) {
		return fmt.Errorf("%w: executable/profile mismatch", ErrInvalidInvocation)
	}
	seenExitCodes := make(map[int]bool, len(value.SuccessExitCodes))
	for _, code := range value.SuccessExitCodes {
		if code < 0 || code > 1 || seenExitCodes[code] || code == 1 && value.ExecutableID != ExecutableClamScan {
			return ErrInvalidInvocation
		}
		seenExitCodes[code] = true
	}
	return nil
}

func validExecutableProfile(executable ExecutableID, profile ToolArgProfile) bool {
	switch executable {
	case ExecutableVips:
		return profile == ArgsVipsThumbnail || profile == ArgsVipsNormalize
	case ExecutableBuiltinText:
		return profile == ArgsBuiltinText
	case ExecutableTesseract:
		return profile == ArgsTesseractOCR
	case ExecutablePDFToCairo:
		return profile == ArgsPDFPages
	case ExecutablePDFToText:
		return profile == ArgsPDFText
	case ExecutableLibreOffice:
		return profile == ArgsOfficePDF
	case ExecutableClamScan:
		return profile == ArgsClamScan
	case ExecutableFFProbe:
		return profile == ArgsMediaProbe
	case ExecutableFFmpeg:
		return profile == ArgsMediaPreview
	case ExecutableGzip:
		return profile == ArgsGzipDecompress
	case ExecutableXZ:
		return profile == ArgsXZDecompress
	case ExecutableZstd:
		return profile == ArgsZstdDecompress
	case ExecutableBuiltinArchive:
		return profile == ArgsArchive
	case ExecutableBuiltinSecret:
		return profile == ArgsSecret
	default:
		return false
	}
}

type ToolResult struct {
	ExitCode        int
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
	Outputs         map[string][]byte
}
