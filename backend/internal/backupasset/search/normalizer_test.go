package search

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"
	"unicode/utf8"
)

func TestNormalizerV1CanonicalEquivalence(t *testing.T) {
	limits := DefaultNormalizerLimits()
	testCases := []struct {
		left  string
		right string
		want  string
	}{
		{left: "Ｆｏｏ", right: "foo", want: "foo"},
		{left: "Straße", right: "STRASSE", want: "strasse"},
		{left: "e\u0301", right: "é", want: "é"},
		{left: "Kelvin", right: "kelvin", want: "kelvin"},
	}
	for _, testCase := range testCases {
		left, err := NormalizeFieldV1(SearchFieldName, testCase.left, limits)
		if err != nil {
			t.Fatalf("normalize %q: %v", testCase.left, err)
		}
		right, err := NormalizeFieldV1(SearchFieldName, testCase.right, limits)
		if err != nil {
			t.Fatalf("normalize %q: %v", testCase.right, err)
		}
		if left.Canonical != right.Canonical || left.Canonical != testCase.want {
			t.Fatalf("canonical mismatch for %q/%q: left=%q right=%q want=%q", testCase.left, testCase.right, left.Canonical, right.Canonical, testCase.want)
		}
	}

	config := &quick.Config{MaxCount: 200, Rand: rand.New(rand.NewSource(7))}
	if err := quick.Check(func(value normalizerQuickString) bool {
		first, err := NormalizeFieldV1(SearchFieldName, string(value), limits)
		if err != nil || !utf8.ValidString(first.Canonical) {
			return false
		}
		second, err := NormalizeFieldV1(SearchFieldName, first.Canonical, limits)
		return err == nil && first.Canonical == second.Canonical
	}, config); err != nil {
		t.Fatalf("normalization idempotence property: %v", err)
	}
}

func TestNormalizerV1HanBigramsLatinExtensionAndUTCDate(t *testing.T) {
	value, err := NormalizeFieldV1(SearchFieldName, "年度报告 Report_2026.TXT", DefaultNormalizerLimits())
	if err != nil {
		t.Fatalf("NormalizeFieldV1: %v", err)
	}
	for _, expected := range []NormalizedToken{
		{Value: "年度", Kind: TokenKindBigram},
		{Value: "度报", Kind: TokenKindBigram},
		{Value: "报告", Kind: TokenKindBigram},
		{Value: "report", Kind: TokenKindExact},
		{Value: "2026", Kind: TokenKindExact},
		{Value: "txt", Kind: TokenKindExact},
	} {
		if !containsNormalizedToken(value.Tokens, expected) {
			t.Fatalf("tokens %#v omit %#v", value.Tokens, expected)
		}
	}
	if value.Extension != "txt" {
		t.Fatalf("extension=%q, want txt", value.Extension)
	}

	dateTokens := NormalizeModifiedTimeV1(time.Date(2026, 7, 18, 23, 30, 0, 0, time.FixedZone("west", -7*60*60)))
	for _, expected := range []string{"2026", "2026-07", "2026-07-19"} {
		if !containsNormalizedToken(dateTokens, NormalizedToken{Value: expected, Kind: TokenKindDate}) {
			t.Fatalf("date tokens %#v omit %q", dateTokens, expected)
		}
	}
}

func TestNormalizerV1RejectsTraversalControlsAndLimits(t *testing.T) {
	limits := DefaultNormalizerLimits()
	for _, value := range []string{"../secret", "/etc/passwd", "C:\\secret", "docs/../../secret", "docs/\x00secret", "docs/\nsecret", string([]byte{0xff})} {
		if _, err := NormalizeFieldV1(SearchFieldPath, value, limits); err == nil {
			t.Fatalf("unsafe path %q was accepted", value)
		}
	}
	normalized, err := NormalizeFieldV1(SearchFieldPath, "Docs\\./Reports//Q1.TXT", limits)
	if err != nil {
		t.Fatalf("normalize safe slash path: %v", err)
	}
	if normalized.Canonical != "docs/reports/q1.txt" {
		t.Fatalf("canonical safe path=%q", normalized.Canonical)
	}

	limits.MaxInputBytes = 8
	if _, err := NormalizeFieldV1(SearchFieldName, strings.Repeat("a", 9), limits); err == nil {
		t.Fatal("input byte limit was not enforced")
	}
	limits = DefaultNormalizerLimits()
	limits.MaxTokens = 2
	if _, err := NormalizeFieldV1(SearchFieldName, "one two three", limits); err == nil {
		t.Fatal("token count limit was not enforced")
	}
}

type normalizerQuickString string

func (normalizerQuickString) Generate(random *rand.Rand, size int) reflect.Value {
	alphabet := []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789éßK报告資料＿-")
	length := random.Intn(min(size+1, 48)) + 1
	value := make([]rune, length)
	for index := range value {
		value[index] = alphabet[random.Intn(len(alphabet))]
	}
	return reflect.ValueOf(normalizerQuickString(value))
}

func containsNormalizedToken(tokens []NormalizedToken, expected NormalizedToken) bool {
	for _, token := range tokens {
		if token.Value == expected.Value && token.Kind == expected.Kind {
			return true
		}
	}
	return false
}
