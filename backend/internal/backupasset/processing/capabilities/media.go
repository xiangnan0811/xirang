package capabilities

import (
	"bytes"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

type MediaOperation string

const (
	MediaProbe   MediaOperation = "probe"
	MediaPreview MediaOperation = "preview"
)

type MediaPlan struct {
	ExecutableID ExecutableID
	ArgProfile   ToolArgProfile
	AllowNetwork bool
	MaxDuration  int64
}

func PlanMedia(input []byte, mediaType string, operation MediaOperation) (MediaPlan, error) {
	if !validMediaHeader(input, mediaType) {
		if mediaType == "video/mp4" || mediaType == "video/webm" || mediaType == "audio/mpeg" || mediaType == "audio/mp4" || mediaType == "audio/ogg" || mediaType == "audio/wav" {
			return MediaPlan{}, ErrInvalidToolOutput
		}
		return MediaPlan{}, capabilityspec.ErrUnsupportedMedia
	}
	switch operation {
	case MediaProbe:
		return MediaPlan{ExecutableID: ExecutableFFProbe, ArgProfile: ArgsMediaProbe, MaxDuration: 7_200_000}, nil
	case MediaPreview:
		return MediaPlan{ExecutableID: ExecutableFFmpeg, ArgProfile: ArgsMediaPreview, MaxDuration: 1_800_000}, nil
	default:
		return MediaPlan{}, ErrInvalidInvocation
	}
}

func validMediaHeader(input []byte, mediaType string) bool {
	switch mediaType {
	case "video/mp4", "audio/mp4":
		return len(input) >= 12 && bytes.Equal(input[4:8], []byte("ftyp"))
	case "video/webm":
		return len(input) >= 4 && bytes.Equal(input[:4], []byte{0x1a, 0x45, 0xdf, 0xa3})
	case "audio/mpeg":
		return len(input) >= 3 && (bytes.Equal(input[:3], []byte("ID3")) || input[0] == 0xff && input[1]&0xe0 == 0xe0)
	case "audio/ogg":
		return len(input) >= 4 && bytes.Equal(input[:4], []byte("OggS"))
	case "audio/wav":
		return len(input) >= 12 && bytes.Equal(input[:4], []byte("RIFF")) && bytes.Equal(input[8:12], []byte("WAVE"))
	default:
		return false
	}
}
