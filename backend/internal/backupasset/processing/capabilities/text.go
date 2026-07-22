package capabilities

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

type Coverage string

const (
	CoverageComplete Coverage = "complete"
	CoveragePartial  Coverage = "partial"
)

type TextLimits struct {
	MaxBytes int64
	MaxRunes int
	MaxLines int
}

type TextResult struct {
	Text       string
	Coverage   Coverage
	Truncated  bool
	InputBytes int64
	Runes      int
	Lines      int
}

func ExtractText(input []byte, declaredMediaType string, limits TextLimits) (TextResult, error) {
	profile, ok := capabilityspec.Lookup(capabilityspec.CapabilityTextExtract, capabilityspec.ProfileBoundedTextV1, false)
	if !ok || profile.ValidateMedia(declaredMediaType, declaredMediaType) != nil {
		return TextResult{}, capabilityspec.ErrUnsupportedMedia
	}
	if limits.MaxBytes <= 0 || limits.MaxRunes <= 0 || limits.MaxLines <= 0 || int64(len(input)) > limits.MaxBytes || int64(len(input)) > profile.Limits.MaxInputBytes {
		return TextResult{}, ErrInputLimit
	}
	text, err := decodeClosedText(input)
	if err != nil || strings.IndexByte(text, 0) >= 0 {
		return TextResult{}, ErrInvalidToolOutput
	}
	runes := []rune(text)
	lineCount := 1 + strings.Count(text, "\n")
	if lineCount > limits.MaxLines {
		return TextResult{}, ErrInputLimit
	}
	if len(runes) > limits.MaxRunes && !strings.ContainsRune(text, '\n') {
		return TextResult{}, ErrInputLimit
	}
	result := TextResult{Text: text, Coverage: CoverageComplete, InputBytes: int64(len(input)), Runes: len(runes), Lines: lineCount}
	if len(runes) > limits.MaxRunes {
		result.Text = string(runes[:limits.MaxRunes])
		result.Runes = limits.MaxRunes
		result.Lines = 1 + strings.Count(result.Text, "\n")
		result.Coverage = CoveragePartial
		result.Truncated = true
	}
	return result, nil
}

func decodeClosedText(input []byte) (string, error) {
	switch {
	case bytes.HasPrefix(input, []byte{0xef, 0xbb, 0xbf}):
		input = input[3:]
		if !utf8.Valid(input) {
			return "", ErrInvalidToolOutput
		}
		return string(input), nil
	case bytes.HasPrefix(input, []byte{0xff, 0xfe, 0, 0}):
		return decodeUTF32(input[4:], binary.LittleEndian)
	case bytes.HasPrefix(input, []byte{0, 0, 0xfe, 0xff}):
		return decodeUTF32(input[4:], binary.BigEndian)
	case bytes.HasPrefix(input, []byte{0xff, 0xfe}):
		return decodeUTF16(input[2:], binary.LittleEndian)
	case bytes.HasPrefix(input, []byte{0xfe, 0xff}):
		return decodeUTF16(input[2:], binary.BigEndian)
	default:
		if !utf8.Valid(input) {
			return "", ErrInvalidToolOutput
		}
		return string(input), nil
	}
}

func decodeUTF16(input []byte, order binary.ByteOrder) (string, error) {
	if len(input)%2 != 0 {
		return "", ErrInvalidToolOutput
	}
	words := make([]uint16, len(input)/2)
	for index := range words {
		words[index] = order.Uint16(input[index*2:])
	}
	decoded := utf16.Decode(words)
	for _, character := range decoded {
		if character == utf8.RuneError {
			return "", ErrInvalidToolOutput
		}
	}
	return string(decoded), nil
}

func decodeUTF32(input []byte, order binary.ByteOrder) (string, error) {
	if len(input)%4 != 0 {
		return "", ErrInvalidToolOutput
	}
	result := make([]rune, 0, len(input)/4)
	for index := 0; index < len(input); index += 4 {
		value := rune(order.Uint32(input[index:]))
		if !utf8.ValidRune(value) || value >= 0xd800 && value <= 0xdfff {
			return "", ErrInvalidToolOutput
		}
		result = append(result, value)
	}
	return string(result), nil
}
