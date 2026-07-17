package provider

// This file implements only the path-name behavior required by Rclone's S3
// backend default encoder in v1.74.4:
//
//   encoder.EncodeInvalidUtf8 | encoder.EncodeSlash | encoder.EncodeDot
//
// Derived from Rclone v1.74.4 (commit
// 5bc93a2a7ab0ebd0a11352bc4968eabeffb18027),
// lib/encoder/encoder.go and backend/s3/s3.go. Rclone is MIT licensed.
// This is an independently reduced implementation, not a copy of the general
// MultiEncoder. Xirang additionally rejects invalid UTF-8 and any physical
// spelling that is not an exact encode/decode/encode bijection.

import (
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
)

const (
	rcloneV1744PathCodecRevision = "rclone-s3-path-codec:v1.74.4@5bc93a2a7ab0"
	rcloneV1744QuoteRune         = '‛'
	rcloneV1744FullwidthSlash    = '／'
	rcloneV1744FullwidthDot      = '．'
	rcloneV1744SymbolForNull     = '␀'
	rcloneV1744MaximumPathBytes  = 16 << 10
)

func EncodeRcloneV1744S3Path(logical string) (string, error) {
	physical, err := encodeRcloneV1744S3PathUnchecked(logical)
	if err != nil {
		return "", err
	}
	decoded, err := decodeRcloneV1744S3PathUnchecked(physical)
	if err != nil || decoded != logical {
		return "", invalidRcloneV1744Path()
	}
	return physical, nil
}

func DecodeRcloneV1744S3Path(physical string) (string, error) {
	if !validRcloneV1744PathShape(physical) {
		return "", invalidRcloneV1744Path()
	}
	logical, err := decodeRcloneV1744S3PathUnchecked(physical)
	if err != nil || !validRcloneV1744PathShape(logical) {
		return "", invalidRcloneV1744Path()
	}
	reencoded, err := encodeRcloneV1744S3PathUnchecked(logical)
	if err != nil || reencoded != physical {
		return "", invalidRcloneV1744Path()
	}
	return logical, nil
}

func ValidateRcloneV1744S3PathSet(logicalPaths []string) (map[string]string, error) {
	if len(logicalPaths) == 0 {
		return map[string]string{}, nil
	}
	mapping := make(map[string]string, len(logicalPaths))
	physicalOwners := make(map[string]string, len(logicalPaths))
	for _, logical := range logicalPaths {
		if _, exists := mapping[logical]; exists {
			return nil, invalidRcloneV1744Path()
		}
		physical, err := EncodeRcloneV1744S3Path(logical)
		if err != nil {
			return nil, err
		}
		if owner, exists := physicalOwners[physical]; exists && owner != logical {
			return nil, invalidRcloneV1744Path()
		}
		mapping[logical] = physical
		physicalOwners[physical] = logical
	}
	return mapping, nil
}

func encodeRcloneV1744S3PathUnchecked(logical string) (string, error) {
	if !validRcloneV1744PathShape(logical) {
		return "", invalidRcloneV1744Path()
	}
	parts := strings.Split(logical, "/")
	for index := range parts {
		raw, err := decodeRcloneV1744StandardName(parts[index])
		if err != nil || strings.ContainsRune(raw, 0) {
			return "", invalidRcloneV1744Path()
		}
		parts[index] = encodeRcloneV1744S3Name(raw)
	}
	return strings.Join(parts, "/"), nil
}

func decodeRcloneV1744S3PathUnchecked(physical string) (string, error) {
	if !validRcloneV1744PathShape(physical) {
		return "", invalidRcloneV1744Path()
	}
	parts := strings.Split(physical, "/")
	for index := range parts {
		raw, err := decodeRcloneV1744S3Name(parts[index])
		if err != nil {
			return "", err
		}
		if strings.ContainsRune(raw, 0) {
			return "", invalidRcloneV1744Path()
		}
		parts[index] = encodeRcloneV1744StandardName(raw)
	}
	return strings.Join(parts, "/"), nil
}

func encodeRcloneV1744StandardName(value string) string {
	switch value {
	case ".":
		return string(rcloneV1744FullwidthDot)
	case "..":
		return strings.Repeat(string(rcloneV1744FullwidthDot), 2)
	case string(rcloneV1744FullwidthDot):
		return string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot)
	case strings.Repeat(string(rcloneV1744FullwidthDot), 2):
		return string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot) + string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot)
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		switch {
		case character == 0:
			builder.WriteRune(rcloneV1744SymbolForNull)
		case character == rcloneV1744SymbolForNull || character == rcloneV1744QuoteRune || character == rcloneV1744FullwidthSlash ||
			(character > rcloneV1744SymbolForNull && character <= rcloneV1744SymbolForNull+0x1f) || character == rcloneV1744SymbolForNull+0x21:
			builder.WriteRune(rcloneV1744QuoteRune)
			builder.WriteRune(character)
		case character == '/':
			builder.WriteRune(rcloneV1744FullwidthSlash)
		case character >= 1 && character <= 0x1f:
			builder.WriteRune(rcloneV1744SymbolForNull + character)
		case character == 0x7f:
			builder.WriteRune(rcloneV1744SymbolForNull + 0x21)
		default:
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func decodeRcloneV1744StandardName(value string) (string, error) {
	switch value {
	case string(rcloneV1744FullwidthDot):
		return ".", nil
	case strings.Repeat(string(rcloneV1744FullwidthDot), 2):
		return "..", nil
	case string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot):
		return string(rcloneV1744FullwidthDot), nil
	case string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot) + string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot):
		return strings.Repeat(string(rcloneV1744FullwidthDot), 2), nil
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); {
		character, size := utf8.DecodeRuneInString(value[index:])
		if character == utf8.RuneError && size == 1 {
			return "", invalidRcloneV1744Path()
		}
		index += size
		if character == rcloneV1744QuoteRune {
			if index >= len(value) {
				builder.WriteRune(character)
				continue
			}
			next, nextSize := utf8.DecodeRuneInString(value[index:])
			if next == utf8.RuneError && nextSize == 1 {
				return "", invalidRcloneV1744Path()
			}
			if next == rcloneV1744QuoteRune || next == rcloneV1744FullwidthSlash || next == rcloneV1744SymbolForNull ||
				(next > rcloneV1744SymbolForNull && next <= rcloneV1744SymbolForNull+0x21) {
				builder.WriteRune(next)
				index += nextSize
				continue
			}
			builder.WriteRune(character)
			continue
		}
		switch {
		case character == rcloneV1744SymbolForNull:
			builder.WriteByte(0)
		case character > rcloneV1744SymbolForNull && character <= rcloneV1744SymbolForNull+0x1f:
			builder.WriteRune(character - rcloneV1744SymbolForNull)
		case character == rcloneV1744SymbolForNull+0x21:
			builder.WriteRune(0x7f)
		case character == rcloneV1744FullwidthSlash:
			builder.WriteRune('/')
		default:
			builder.WriteRune(character)
		}
	}
	return builder.String(), nil
}

func encodeRcloneV1744S3Name(value string) string {
	switch value {
	case ".":
		return string(rcloneV1744FullwidthDot)
	case "..":
		return strings.Repeat(string(rcloneV1744FullwidthDot), 2)
	case string(rcloneV1744FullwidthDot):
		return string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot)
	case strings.Repeat(string(rcloneV1744FullwidthDot), 2):
		return string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot) + string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot)
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for _, character := range value {
		switch character {
		case 0:
			builder.WriteRune(rcloneV1744SymbolForNull)
		case rcloneV1744SymbolForNull, rcloneV1744QuoteRune:
			builder.WriteRune(rcloneV1744QuoteRune)
			builder.WriteRune(character)
		case '/':
			builder.WriteRune(rcloneV1744FullwidthSlash)
		case rcloneV1744FullwidthSlash:
			builder.WriteRune(rcloneV1744QuoteRune)
			builder.WriteRune(character)
		default:
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func decodeRcloneV1744S3Name(value string) (string, error) {
	switch value {
	case string(rcloneV1744FullwidthDot):
		return ".", nil
	case strings.Repeat(string(rcloneV1744FullwidthDot), 2):
		return "..", nil
	case string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot):
		return string(rcloneV1744FullwidthDot), nil
	case string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot) + string(rcloneV1744QuoteRune) + string(rcloneV1744FullwidthDot):
		return strings.Repeat(string(rcloneV1744FullwidthDot), 2), nil
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); {
		character, size := utf8.DecodeRuneInString(value[index:])
		if character == utf8.RuneError && size == 1 {
			return "", invalidRcloneV1744Path()
		}
		index += size
		if character == rcloneV1744QuoteRune {
			if index >= len(value) {
				return "", invalidRcloneV1744Path()
			}
			next, nextSize := utf8.DecodeRuneInString(value[index:])
			if next == utf8.RuneError && nextSize == 1 {
				return "", invalidRcloneV1744Path()
			}
			switch next {
			case rcloneV1744QuoteRune, rcloneV1744SymbolForNull, rcloneV1744FullwidthSlash:
				builder.WriteRune(next)
				index += nextSize
				continue
			}
			if index+2 <= len(value) {
				decoded := make([]byte, 1)
				if _, err := hex.Decode(decoded, []byte(value[index:index+2])); err == nil {
					builder.WriteByte(decoded[0])
					index += 2
					continue
				}
			}
			builder.WriteRune(rcloneV1744QuoteRune)
			continue
		}
		switch character {
		case rcloneV1744SymbolForNull:
			builder.WriteByte(0)
		case rcloneV1744FullwidthSlash:
			builder.WriteRune('/')
		default:
			builder.WriteRune(character)
		}
	}
	decoded := builder.String()
	if !utf8.ValidString(decoded) {
		return "", invalidRcloneV1744Path()
	}
	return decoded, nil
}

func validRcloneV1744PathShape(value string) bool {
	return value != "" && len(value) <= rcloneV1744MaximumPathBytes && utf8.ValidString(value) &&
		!strings.HasPrefix(value, "/") && !strings.HasSuffix(value, "/") && !strings.Contains(value, "//") &&
		!strings.ContainsRune(value, 0)
}

func invalidRcloneV1744Path() error {
	return fmt.Errorf("%w: invalid Rclone v1.74.4 S3 path", backupasset.ErrInvalidState)
}
