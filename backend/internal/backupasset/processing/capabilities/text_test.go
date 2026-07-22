package capabilities

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestExtractTextSupportsBOMAndReportsPartialCoverage(t *testing.T) {
	words := utf16.Encode([]rune("息壤 backup\nsecond line"))
	input := []byte{0xff, 0xfe}
	for _, word := range words {
		input = append(input, byte(word), byte(word>>8))
	}
	result, err := ExtractText(input, "text/plain", TextLimits{MaxBytes: int64(len(input)), MaxRunes: 9, MaxLines: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage != CoveragePartial || !result.Truncated || len([]rune(result.Text)) != 9 || !strings.HasPrefix(result.Text, "息壤 backup") {
		t.Fatalf("unexpected text result: %+v", result)
	}
	if !bytes.Equal(input[:2], []byte{0xff, 0xfe}) {
		t.Fatal("text extraction mutated input")
	}
}

func TestExtractTextRejectsInvalidUTF8BinaryAndUnboundedLine(t *testing.T) {
	for index, input := range [][]byte{{0xff, 0xfe, 0x00}, {0xff, 0xff}, bytes.Repeat([]byte{0}, 128)} {
		if _, err := ExtractText(input, "text/plain", TextLimits{MaxBytes: 1024, MaxRunes: 100, MaxLines: 10}); !errors.Is(err, ErrInvalidToolOutput) {
			t.Fatalf("input %d error=%v", index, err)
		}
	}
	line := bytes.Repeat([]byte("a"), 101)
	if _, err := ExtractText(line, "text/plain", TextLimits{MaxBytes: 1024, MaxRunes: 100, MaxLines: 10}); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("long line error=%v", err)
	}
}
