package capabilities

import (
	"fmt"
	"strconv"

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
			MaxOutputBytes: profile.Limits.MaxOutputBytes, MaxProcesses: 64,
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
			invocation.OutputSpec = ClosedOutputSpec{MaximumFiles: 32, AllowedNames: documentOutputNames()}
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
			MaxOutputBytes: profile.Limits.MaxInputBytes, MaxProcesses: 64,
		},
		SuccessExitCodes: []int{0},
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
			MaxOutputBytes: profile.Limits.MaxExpandedBytes, MaxProcesses: 4,
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
	if err := invocation.Validate(); err != nil {
		return ToolInvocation{}, err
	}
	return invocation, nil
}

func documentOutputNames() []string {
	result := make([]string, 0, 32)
	for page := 1; page <= 30; page++ {
		result = append(result, fmt.Sprintf("page-%02d.png", page))
	}
	return append(result, "content.txt", "metadata.json")
}
