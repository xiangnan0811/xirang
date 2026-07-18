package search

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func TestTokenHMACSeparatesFieldKindNormalizerAndKeyVersion(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	base, err := TokenHMAC(key, 1, NormalizerVersion, SearchFieldName, TokenKindExact, "report")
	if err != nil {
		t.Fatalf("TokenHMAC: %v", err)
	}
	if len(base) != 64 || base != strings.ToLower(base) {
		t.Fatalf("token HMAC is not fixed lowercase hex: %q", base)
	}
	variants := []struct {
		keyVersion        int
		normalizerVersion int
		field             SearchField
		kind              TokenKind
	}{
		{keyVersion: 2, normalizerVersion: NormalizerVersion, field: SearchFieldName, kind: TokenKindExact},
		{keyVersion: 1, normalizerVersion: NormalizerVersion + 1, field: SearchFieldName, kind: TokenKindExact},
		{keyVersion: 1, normalizerVersion: NormalizerVersion, field: SearchFieldPath, kind: TokenKindExact},
		{keyVersion: 1, normalizerVersion: NormalizerVersion, field: SearchFieldName, kind: TokenKindSegment},
	}
	seen := map[string]bool{base: true}
	for _, variant := range variants {
		value, err := TokenHMAC(key, variant.keyVersion, variant.normalizerVersion, variant.field, variant.kind, "report")
		if err != nil {
			t.Fatalf("TokenHMAC variant: %v", err)
		}
		if seen[value] {
			t.Fatalf("domain-separated variant collided with %q", value)
		}
		seen[value] = true
	}
}

func TestPortableSortKeyUsesASCIIHexByteOrder(t *testing.T) {
	first, err := PortableSortKey("a/é")
	if err != nil {
		t.Fatalf("PortableSortKey first: %v", err)
	}
	second, err := PortableSortKey("b/é")
	if err != nil {
		t.Fatalf("PortableSortKey second: %v", err)
	}
	if first >= second || first != strings.ToLower(first) {
		t.Fatalf("sort keys do not preserve byte order: first=%q second=%q", first, second)
	}
	decoded, err := hex.DecodeString(first)
	if err != nil || string(decoded) != "a/é" {
		t.Fatalf("sort key is not ASCII hex of canonical bytes: decoded=%q err=%v", decoded, err)
	}
	if _, err := PortableSortKey(string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 sort input was accepted")
	}
}

func TestPathGroupTokenPreservesCanonicalCaseAndLineage(t *testing.T) {
	key := bytes.Repeat([]byte{0x24}, 32)
	upper, err := PathGroupToken(key, 1, "lineage-a", "Docs/Report.txt")
	if err != nil {
		t.Fatalf("PathGroupToken upper: %v", err)
	}
	lower, err := PathGroupToken(key, 1, "lineage-a", "docs/report.txt")
	if err != nil {
		t.Fatalf("PathGroupToken lower: %v", err)
	}
	otherLineage, err := PathGroupToken(key, 1, "lineage-b", "Docs/Report.txt")
	if err != nil {
		t.Fatalf("PathGroupToken lineage: %v", err)
	}
	if upper == lower || upper == otherLineage || lower == otherLineage {
		t.Fatalf("path group token merged case or lineage: upper=%q lower=%q other=%q", upper, lower, otherLineage)
	}
	lineageA, err := LineageToken(key, 1, "lineage-a")
	if err != nil {
		t.Fatalf("LineageToken: %v", err)
	}
	lineageB, err := LineageToken(key, 1, "lineage-b")
	if err != nil {
		t.Fatalf("LineageToken: %v", err)
	}
	if lineageA == lineageB || lineageA == upper {
		t.Fatal("lineage and path-group domains are not separated")
	}
}
