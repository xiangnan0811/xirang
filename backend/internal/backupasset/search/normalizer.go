package search

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const NormalizerVersion = 1

var ErrInvalidNormalization = errors.New("invalid backup asset search normalization")

type NormalizerLimits struct {
	MaxInputBytes int
	MaxRunes      int
	MaxTokens     int
	MaxTokenRunes int
}

type NormalizedToken struct {
	Value     string
	Kind      TokenKind
	Frequency int
}

type NormalizedValue struct {
	Canonical string
	Extension string
	Tokens    []NormalizedToken
}

func DefaultNormalizerLimits() NormalizerLimits {
	return NormalizerLimits{
		MaxInputBytes: 4096,
		MaxRunes:      2048,
		MaxTokens:     512,
		MaxTokenRunes: 256,
	}
}

func NormalizeFieldV1(field SearchField, raw string, limits NormalizerLimits) (NormalizedValue, error) {
	if !validNormalizableField(field) || !validNormalizerLimits(limits) || raw == "" ||
		!utf8.ValidString(raw) || len(raw) > limits.MaxInputBytes || utf8.RuneCountInString(raw) > limits.MaxRunes {
		return NormalizedValue{}, ErrInvalidNormalization
	}
	canonical := cases.Fold().String(norm.NFKC.String(raw))
	if canonical == "" || hasUnsafeControl(canonical) {
		return NormalizedValue{}, ErrInvalidNormalization
	}
	var segments []string
	if field == SearchFieldPath {
		var err error
		canonical, segments, err = normalizeRelativePath(canonical)
		if err != nil {
			return NormalizedValue{}, err
		}
	} else {
		if strings.ContainsAny(canonical, "/\\") {
			return NormalizedValue{}, ErrInvalidNormalization
		}
		segments = []string{canonical}
	}

	result := NormalizedValue{Canonical: canonical}
	if field == SearchFieldName || field == SearchFieldPath || field == SearchFieldExtension {
		result.Extension = normalizedExtension(segments[len(segments)-1])
	}
	tokens, err := tokenizeSegments(segments, field, limits)
	if err != nil {
		return NormalizedValue{}, err
	}
	result.Tokens = tokens
	return result, nil
}

func NormalizeModifiedTimeV1(value time.Time) []NormalizedToken {
	utc := value.UTC()
	return []NormalizedToken{
		{Value: utc.Format("2006"), Kind: TokenKindDate, Frequency: 1},
		{Value: utc.Format("2006-01"), Kind: TokenKindDate, Frequency: 1},
		{Value: utc.Format("2006-01-02"), Kind: TokenKindDate, Frequency: 1},
	}
}

func validNormalizableField(field SearchField) bool {
	switch field {
	case SearchFieldName, SearchFieldPath, SearchFieldExtension, SearchFieldTag, SearchFieldContent, SearchFieldOCR:
		return true
	default:
		return false
	}
}

func validNormalizerLimits(limits NormalizerLimits) bool {
	return limits.MaxInputBytes > 0 && limits.MaxInputBytes <= 64*1024 &&
		limits.MaxRunes > 0 && limits.MaxRunes <= limits.MaxInputBytes &&
		limits.MaxTokens > 0 && limits.MaxTokens <= 4096 &&
		limits.MaxTokenRunes > 0 && limits.MaxTokenRunes <= limits.MaxRunes
}

func hasUnsafeControl(value string) bool {
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func normalizeRelativePath(value string) (string, []string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, ":") {
		return "", nil, ErrInvalidNormalization
	}
	parts := strings.Split(value, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", nil, ErrInvalidNormalization
		default:
			segments = append(segments, part)
		}
	}
	if len(segments) == 0 {
		return "", nil, ErrInvalidNormalization
	}
	return strings.Join(segments, "/"), segments, nil
}

func normalizedExtension(finalSegment string) string {
	index := strings.LastIndexByte(finalSegment, '.')
	if index <= 0 || index == len(finalSegment)-1 {
		return ""
	}
	return finalSegment[index+1:]
}

func tokenizeSegments(segments []string, field SearchField, limits NormalizerLimits) ([]NormalizedToken, error) {
	type tokenKey struct {
		value string
		kind  TokenKind
	}
	ordered := make([]tokenKey, 0)
	frequencies := make(map[tokenKey]int)
	add := func(value string, kind TokenKind) error {
		if value == "" {
			return nil
		}
		if utf8.RuneCountInString(value) > limits.MaxTokenRunes {
			return ErrInvalidNormalization
		}
		key := tokenKey{value: value, kind: kind}
		if frequencies[key] == 0 {
			ordered = append(ordered, key)
			if len(ordered) > limits.MaxTokens {
				return ErrInvalidNormalization
			}
		}
		frequencies[key]++
		return nil
	}

	for _, segment := range segments {
		if (field == SearchFieldName || field == SearchFieldPath) && segment != "" {
			if err := add(segment, TokenKindSegment); err != nil {
				return nil, err
			}
		}
		runes := []rune(segment)
		for start := 0; start < len(runes); {
			if !unicode.IsLetter(runes[start]) && !unicode.IsDigit(runes[start]) {
				start++
				continue
			}
			han := unicode.Is(unicode.Han, runes[start])
			end := start + 1
			for end < len(runes) && (unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end])) && unicode.Is(unicode.Han, runes[end]) == han {
				end++
			}
			run := runes[start:end]
			if han && len(run) > 1 {
				for index := 0; index+1 < len(run); index++ {
					if err := add(string(run[index:index+2]), TokenKindBigram); err != nil {
						return nil, err
					}
				}
			} else if err := add(string(run), TokenKindExact); err != nil {
				return nil, err
			}
			start = end
		}
	}
	if extension := normalizedExtension(segments[len(segments)-1]); extension != "" {
		if err := add(extension, TokenKindExact); err != nil {
			return nil, err
		}
	}

	tokens := make([]NormalizedToken, 0, len(ordered))
	for _, key := range ordered {
		tokens = append(tokens, NormalizedToken{Value: key.value, Kind: key.kind, Frequency: frequencies[key]})
	}
	return tokens, nil
}
