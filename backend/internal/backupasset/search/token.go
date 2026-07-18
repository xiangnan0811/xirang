package search

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ErrInvalidTokenInput = errors.New("invalid backup asset search token input")

func TokenHMAC(
	key []byte,
	keyVersion int,
	normalizerVersion int,
	field SearchField,
	kind TokenKind,
	normalized string,
) (string, error) {
	if len(key) != 32 || keyVersion <= 0 || normalizerVersion <= 0 ||
		!validPostingFields[field] || !validTokenKinds[kind] || normalized == "" ||
		!utf8.ValidString(normalized) || strings.ContainsRune(normalized, 0) {
		return "", ErrInvalidTokenInput
	}
	return keyedDigest(key,
		"xirang/search/v1", strconv.Itoa(keyVersion), strconv.Itoa(normalizerVersion),
		string(field), string(kind), normalized,
	), nil
}

func PortableSortKey(canonical string) (string, error) {
	if canonical == "" || !utf8.ValidString(canonical) || strings.ContainsRune(canonical, 0) {
		return "", ErrInvalidTokenInput
	}
	return hex.EncodeToString([]byte(canonical)), nil
}

func PathGroupToken(key []byte, keyVersion int, lineage, canonicalPath string) (string, error) {
	if !validPrivateTokenInput(key, keyVersion, lineage) || !validCanonicalCatalogPath(canonicalPath) {
		return "", ErrInvalidTokenInput
	}
	return keyedDigest(key, "xirang/search/group/path/v1", strconv.Itoa(keyVersion), lineage, canonicalPath), nil
}

func LineageToken(key []byte, keyVersion int, lineage string) (string, error) {
	if !validPrivateTokenInput(key, keyVersion, lineage) {
		return "", ErrInvalidTokenInput
	}
	return keyedDigest(key, "xirang/search/group/lineage/v1", strconv.Itoa(keyVersion), lineage), nil
}

func validPrivateTokenInput(key []byte, keyVersion int, lineage string) bool {
	return len(key) == 32 && keyVersion > 0 && lineage != "" && len(lineage) <= 1024 &&
		utf8.ValidString(lineage) && !strings.ContainsRune(lineage, 0)
}

func validCanonicalCatalogPath(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") || strings.Contains(value, "\\") || strings.ContainsRune(value, 0) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || hasUnsafeControl(segment) {
			return false
		}
	}
	return true
}

func keyedDigest(key []byte, values ...string) string {
	mac := hmac.New(sha256.New, key)
	for index, value := range values {
		if index > 0 {
			_, _ = mac.Write([]byte{0})
		}
		_, _ = mac.Write([]byte(value))
	}
	return hex.EncodeToString(mac.Sum(nil))
}
