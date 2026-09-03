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

// NormalizerVersion identifies the persisted token output. Bump it whenever
// normalization changes so older generations are not treated as complete.
const (
	NormalizerVersion = 3

	// Prefixes are optional index entries. Keep their expansion bounded so a
	// deeply nested or unusually long path cannot consume the mandatory token
	// budget.
	minIndexedPrefixRunes  = 3
	maxIndexedPrefixTokens = 64
)

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
	// Prefix marks a generated search-as-you-type token. It is local
	// normalization metadata and is not part of the HMAC posting identity.
	Prefix bool
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
	prefixes := make(map[tokenKey]bool)
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
	prefixCount := 0
	addPrefix := func(value string, kind TokenKind) error {
		runeCount := utf8.RuneCountInString(value)
		if value == "" || runeCount < minIndexedPrefixRunes || runeCount > limits.MaxTokenRunes {
			return nil
		}
		key := tokenKey{value: value, kind: kind}
		if frequencies[key] != 0 || prefixCount >= maxIndexedPrefixTokens || len(ordered) >= limits.MaxTokens {
			return nil
		}
		ordered = append(ordered, key)
		frequencies[key] = 1
		prefixes[key] = true
		prefixCount++
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

	// Prefixes are indexed as bounded segment/exact tokens so candidate
	// planning can remain equality-only over opaque HMAC postings. Generate
	// prefixes for the filename segment first; this keeps useful filename
	// prefixes available when a long path reaches the token limit.
	if field == SearchFieldName || field == SearchFieldPath {
		for segmentIndex := len(segments) - 1; segmentIndex >= 0; segmentIndex-- {
			runes := []rune(segments[segmentIndex])
			runEnds := make(map[int]struct{})
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
				runEnds[end] = struct{}{}
				if !han {
					for prefixLength := 1; prefixLength < end-start; prefixLength++ {
						if err := addPrefix(string(runes[start:start+prefixLength]), TokenKindExact); err != nil {
							return nil, err
						}
					}
				}
				start = end
			}
			for prefixLength := 1; prefixLength < len(runes); prefixLength++ {
				if _, isRunEnd := runEnds[prefixLength]; isRunEnd {
					continue
				}
				if err := addPrefix(string(runes[:prefixLength]), TokenKindSegment); err != nil {
					return nil, err
				}
			}
		}
	}

	tokens := make([]NormalizedToken, 0, len(ordered))
	for _, key := range ordered {
		tokens = append(tokens, NormalizedToken{Value: key.value, Kind: key.kind, Frequency: frequencies[key], Prefix: prefixes[key]})
	}
	return tokens, nil
}
