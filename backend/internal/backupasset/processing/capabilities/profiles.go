package capabilities

import (
	"fmt"
	"strconv"
	"time"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

type ToolParameters struct {
	Width     int
	Height    int
	Quality   int
	Language  string
	MediaType string
}

func BuildInvocation(profile capabilityspec.Profile, parameters ToolParameters) (ToolInvocation, error) {
	if profile.Validate() != nil {
		return ToolInvocation{}, ErrInvalidInvocation
	}
	invocation := ToolInvocation{
		Environment:      []string{"HOME=workspace/home", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC"},
		SuccessExitCodes: []int{0},
		Limits: ToolLimits{
			WallTime: profile.Limits.WallTime, MaxInputBytes: profile.Limits.MaxInputBytes,
			MaxOutputBytes: profile.Limits.MaxOutputBytes,
		},
	}
	switch profile.Capability {
	case capabilityspec.CapabilityImageThumbnail:
		if parameters.Width <= 0 || parameters.Height <= 0 || parameters.Width > 4096 || parameters.Height > 4096 || parameters.Quality <= 0 || parameters.Quality > 100 {
			return ToolInvocation{}, ErrInvalidInvocation
		}
		invocation.ExecutableID = ExecutableVips
		invocation.ArgProfile = ArgsVipsThumbnail
		invocation.InputMode = ToolInputPath
		invocation.Args = []string{"--width=" + strconv.Itoa(parameters.Width), "--height=" + strconv.Itoa(parameters.Height), "--quality=" + strconv.Itoa(parameters.Quality)}
		invocation.OutputSpec = ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"thumbnail.png"}}
	case capabilityspec.CapabilityTextExtract:
		invocation.ExecutableID = ExecutableBuiltinText
		invocation.ArgProfile = ArgsBuiltinText
		invocation.InputMode = ToolInputPipe
		invocation.OutputSpec = ClosedOutputSpec{MaximumFiles: 2, AllowedNames: []string{"content.txt", "metadata.json"}}
	case capabilityspec.CapabilityImageOCR:
		if _, err := PlanOCR(parameters.MediaType, parameters.Language); err != nil {
			return ToolInvocation{}, ErrInvalidInvocation
		}
		invocation.ExecutableID = ExecutableTesseract
		invocation.ArgProfile = ArgsTesseractOCR
		invocation.InputMode = ToolInputPath
		invocation.Args = []string{"--language=" + parameters.Language}
		invocation.OutputSpec = ClosedOutputSpec{MaximumFiles: 2, AllowedNames: []string{"ocr.txt", "metadata.json"}}
	case capabilityspec.CapabilityDocumentConvert:
		if profile.ValidateMedia(parameters.MediaType, parameters.MediaType) != nil {
			return ToolInvocation{}, ErrInvalidInvocation
		}
		invocation.InputMode = ToolInputPath
		if parameters.MediaType == "application/pdf" {
			invocation.ExecutableID = ExecutablePDFToCairo
			invocation.ArgProfile = ArgsPDFPages
			invocation.OutputSpec = ClosedOutputSpec{MaximumFiles: 30, AllowedNames: documentOutputNames()}
		} else {
			invocation.ExecutableID = ExecutableLibreOffice
			invocation.ArgProfile = ArgsOfficePDF
			invocation.OutputSpec = ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"input.pdf"}}
		}
	case capabilityspec.CapabilityMalwareScan:
		if profile.ValidateMedia(parameters.MediaType, parameters.MediaType) != nil {
			return ToolInvocation{}, ErrInvalidInvocation
		}
		invocation.ExecutableID = ExecutableClamScan
		invocation.ArgProfile = ArgsClamScan
		invocation.InputMode = ToolInputPath
		invocation.OutputSpec = ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"metadata.json"}}
		invocation.SuccessExitCodes = []int{0, 1}
	case capabilityspec.CapabilityMediaProbe:
		if profile.ValidateMedia(parameters.MediaType, parameters.MediaType) != nil {
			return ToolInvocation{}, ErrInvalidInvocation
		}
		invocation.ExecutableID = ExecutableFFProbe
		invocation.ArgProfile = ArgsMediaProbe
		invocation.InputMode = ToolInputPath
		invocation.OutputSpec = ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"metadata.json"}}
	case capabilityspec.CapabilityMediaTranscode:
		if profile.ValidateMedia(parameters.MediaType, parameters.MediaType) != nil {
			return ToolInvocation{}, ErrInvalidInvocation
		}
		invocation.ExecutableID = ExecutableFFmpeg
		invocation.ArgProfile = ArgsMediaPreview
		invocation.InputMode = ToolInputPath
		invocation.OutputSpec = ClosedOutputSpec{MaximumFiles: 3, AllowedNames: []string{"preview.mp4", "poster.png", "metadata.json"}}
	default:
		return ToolInvocation{}, fmt.Errorf("%w: capability has no closed invocation", ErrInvalidInvocation)
	}
	if err := applyClosedSandboxResources(&invocation, profile); err != nil {
		return ToolInvocation{}, err
	}
	if err := invocation.Validate(); err != nil {
		return ToolInvocation{}, err
	}
	return invocation, nil
}

func BuildPDFTextInvocation(profile capabilityspec.Profile) (ToolInvocation, error) {
	if profile.Validate() != nil || profile.Capability != capabilityspec.CapabilityDocumentConvert ||
		profile.OutputProfile != capabilityspec.ProfileStaticPagesV1 ||
		profile.ValidateMedia("application/pdf", "application/pdf") != nil {
		return ToolInvocation{}, ErrInvalidInvocation
	}
	invocation := ToolInvocation{
		ExecutableID: ExecutablePDFToText,
		ArgProfile:   ArgsPDFText,
		InputMode:    ToolInputPath,
		Environment:  []string{"HOME=workspace/home", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC"},
		OutputSpec:   ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"content.txt"}},
		Limits: ToolLimits{
			WallTime: profile.Limits.WallTime, MaxInputBytes: profile.Limits.MaxInputBytes,
			MaxOutputBytes: profile.Limits.MaxOutputBytes,
		},
		SuccessExitCodes: []int{0},
	}
	if err := applyClosedSandboxResources(&invocation, profile); err != nil {
		return ToolInvocation{}, err
	}
	if err := invocation.Validate(); err != nil {
		return ToolInvocation{}, err
	}
	return invocation, nil
}

func BuildRasterNormalizeInvocation(profile capabilityspec.Profile, mediaType string) (ToolInvocation, error) {
	if profile.Validate() != nil || profile.Capability != capabilityspec.CapabilityImageOCR ||
		(mediaType != "image/webp" && mediaType != "image/tiff") || profile.ValidateMedia(mediaType, mediaType) != nil {
		return ToolInvocation{}, ErrInvalidInvocation
	}
	invocation := ToolInvocation{
		ExecutableID: ExecutableVips,
		ArgProfile:   ArgsVipsNormalize,
		InputMode:    ToolInputPath,
		Environment:  []string{"HOME=workspace/home", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC"},
		OutputSpec:   ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"normalized.png"}},
		Limits: ToolLimits{
			WallTime: profile.Limits.WallTime, MaxInputBytes: profile.Limits.MaxInputBytes,
			MaxOutputBytes: profile.Limits.MaxOutputBytes,
		},
		SuccessExitCodes: []int{0},
	}
	if err := applyClosedSandboxResources(&invocation, profile); err != nil {
		return ToolInvocation{}, err
	}
	if err := invocation.Validate(); err != nil {
		return ToolInvocation{}, err
	}
	return invocation, nil
}

func BuildArchiveDecompressInvocation(profile capabilityspec.Profile, mediaType string) (ToolInvocation, error) {
	if profile.Validate() != nil ||
		(profile.Capability != capabilityspec.CapabilityArchiveInspect &&
			profile.Capability != capabilityspec.CapabilityArchiveExtractEntry) ||
		profile.ValidateMedia(mediaType, mediaType) != nil {
		return ToolInvocation{}, ErrInvalidInvocation
	}
	invocation := ToolInvocation{
		InputMode:   ToolInputPipe,
		Environment: []string{"HOME=workspace/home", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC"},
		OutputSpec:  ClosedOutputSpec{MaximumFiles: 1, AllowedNames: []string{"stream.tar"}},
		Limits: ToolLimits{
			WallTime: profile.Limits.WallTime, MaxInputBytes: profile.Limits.MaxInputBytes,
			MaxOutputBytes: profile.Limits.MaxExpandedBytes,
		},
		SuccessExitCodes: []int{0},
	}
	switch mediaType {
	case "application/gzip":
		invocation.ExecutableID = ExecutableGzip
		invocation.ArgProfile = ArgsGzipDecompress
	case "application/x-xz":
		invocation.ExecutableID = ExecutableXZ
		invocation.ArgProfile = ArgsXZDecompress
	case "application/zstd":
		invocation.ExecutableID = ExecutableZstd
		invocation.ArgProfile = ArgsZstdDecompress
	default:
		return ToolInvocation{}, ErrInvalidInvocation
	}
	if err := applyClosedSandboxResources(&invocation, profile); err != nil {
		return ToolInvocation{}, err
	}
	if err := invocation.Validate(); err != nil {
		return ToolInvocation{}, err
	}
	return invocation, nil
}

func applyClosedSandboxResources(invocation *ToolInvocation, profile capabilityspec.Profile) error {
	if invocation == nil || invocation.ArgProfile == "" {
		return ErrInvalidInvocation
	}
	ceiling, ok := closedSandboxResourceCeiling(invocation.ArgProfile)
	if !ok {
		return ErrInvalidInvocation
	}
	resources := SandboxResourceLimits{
		CPUTime: profile.Limits.WallTime, MaxMemoryBytes: ceiling.MaxMemoryBytes,
		MaxFileBytes: min(profile.Limits.MaxOutputBytes, ceiling.MaxFileBytes),
		MaxProcesses: ceiling.MaxProcesses,
	}
	if invocation.ArgProfile == ArgsClamScan {
		resources.MaxFileBytes = min(profile.Limits.MaxInputBytes, ceiling.MaxFileBytes)
	}
	if err := resources.ValidateFor(invocation.ArgProfile); err != nil {
		return err
	}
	invocation.Limits.CPUTime = resources.CPUTime
	invocation.Limits.MaxMemoryBytes = resources.MaxMemoryBytes
	invocation.Limits.MaxFileBytes = resources.MaxFileBytes
	invocation.Limits.MaxProcesses = resources.MaxProcesses
	return nil
}

func closedSandboxResourceCeiling(profile ToolArgProfile) (SandboxResourceLimits, bool) {
	switch profile {
	case ArgsVipsThumbnail:
		return SandboxResourceLimits{CPUTime: 90 * time.Second, MaxMemoryBytes: 1 << 30, MaxFileBytes: 8 << 20, MaxProcesses: 16}, true
	case ArgsVipsNormalize:
		return SandboxResourceLimits{CPUTime: 5 * time.Minute, MaxMemoryBytes: 1 << 30, MaxFileBytes: 8 << 20, MaxProcesses: 16}, true
	case ArgsBuiltinText:
		return SandboxResourceLimits{CPUTime: time.Minute, MaxMemoryBytes: 512 << 20, MaxFileBytes: 4 << 20, MaxProcesses: 4}, true
	case ArgsTesseractOCR:
		return SandboxResourceLimits{CPUTime: 5 * time.Minute, MaxMemoryBytes: 2 << 30, MaxFileBytes: 8 << 20, MaxProcesses: 16}, true
	case ArgsPDFPages, ArgsPDFText, ArgsOfficePDF:
		return SandboxResourceLimits{CPUTime: 10 * time.Minute, MaxMemoryBytes: 2 << 30, MaxFileBytes: 64 << 20, MaxProcesses: 32}, true
	case ArgsClamScan:
		return SandboxResourceLimits{CPUTime: 10 * time.Minute, MaxMemoryBytes: 2 << 30, MaxFileBytes: 1 << 30, MaxProcesses: 16}, true
	case ArgsMediaProbe:
		return SandboxResourceLimits{CPUTime: 2 * time.Minute, MaxMemoryBytes: 1 << 30, MaxFileBytes: 1 << 20, MaxProcesses: 16}, true
	case ArgsMediaPreview:
		return SandboxResourceLimits{CPUTime: 30 * time.Minute, MaxMemoryBytes: 4 << 30, MaxFileBytes: 512 << 20, MaxProcesses: 32}, true
	case ArgsGzipDecompress, ArgsXZDecompress, ArgsZstdDecompress, ArgsArchive:
		return SandboxResourceLimits{CPUTime: 10 * time.Minute, MaxMemoryBytes: 1 << 30, MaxFileBytes: 256 << 20, MaxProcesses: 4}, true
	case ArgsSecret:
		return SandboxResourceLimits{CPUTime: time.Minute, MaxMemoryBytes: 512 << 20, MaxFileBytes: 256 << 10, MaxProcesses: 4}, true
	default:
		return SandboxResourceLimits{}, false
	}
}

func documentOutputNames() []string {
	result := make([]string, 0, 30)
	for page := 1; page <= 30; page++ {
		result = append(result, fmt.Sprintf("page-%d.png", page))
	}
	return result
}
