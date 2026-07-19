package content

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
)

var ErrInvalidClassificationRequest = errors.New("invalid content classification request")

type ClassificationReason string

const (
	ClassificationReasonClosedText      ClassificationReason = "closed_text"
	ClassificationReasonPathSecret      ClassificationReason = "path_secret"
	ClassificationReasonContentSecret   ClassificationReason = "content_secret"
	ClassificationReasonSearchSecret    ClassificationReason = "search_secret"
	ClassificationReasonTruncated       ClassificationReason = "scan_truncated"
	ClassificationReasonBinaryUnknown   ClassificationReason = "binary_unknown"
	ClassificationReasonActiveContent   ClassificationReason = "active_content"
	ClassificationReasonMIMEConfusion   ClassificationReason = "mime_confusion"
	ClassificationReasonScanUnavailable ClassificationReason = "scan_unavailable"
)

type ClassificationConfig struct {
	ScanBytes int64
}

type SearchClassificationEvidence struct {
	Classification      Classification
	CatalogGenerationID string
	SourceFingerprint   string
	Revision            int64
}

type ClassificationRequest struct {
	Path                string
	Name                string
	SourceSize          int64
	ProviderMediaType   string
	CatalogGenerationID string
	SourceFingerprint   string
	Search              *SearchClassificationEvidence
}

type ClassificationResult struct {
	Classification    Classification
	Reason            ClassificationReason
	DetectedMediaType string
	BytesScanned      int64
	PolicyRevision    int64
	SourceRevision    int64
}

type Classifier struct {
	config ClassificationConfig
}

func NewClassifier(config ClassificationConfig) (*Classifier, error) {
	if config.ScanBytes <= 0 || config.ScanBytes > 4<<20 {
		return nil, ErrInvalidClassificationRequest
	}
	return &Classifier{config: config}, nil
}

func (classifier *Classifier) Classify(
	ctx context.Context,
	request ClassificationRequest,
	reader io.Reader,
) (ClassificationResult, error) {
	if classifier == nil || reader == nil || !validClassificationRequest(request) {
		return ClassificationResult{}, ErrInvalidClassificationRequest
	}
	result := ClassificationResult{
		Classification: ClassificationUnknown,
		PolicyRevision: 1,
		SourceRevision: classificationSourceRevision(request),
	}
	if secretPath(request.Path, request.Name) {
		result.Classification = ClassificationSecret
		result.Reason = ClassificationReasonPathSecret
		return elevateSearchClassification(request, result), nil
	}
	ctx = nonNilContext(ctx)
	if ctx.Err() != nil {
		result.Reason = ClassificationReasonScanUnavailable
		return elevateSearchClassification(request, result), nil
	}
	readLimit := min(request.SourceSize, classifier.config.ScanBytes+1)
	if readLimit > int64(maxInt()) {
		result.Reason = ClassificationReasonScanUnavailable
		return elevateSearchClassification(request, result), nil
	}
	payload := make([]byte, int(readLimit))
	if len(payload) > 0 {
		count, err := io.ReadFull(reader, payload)
		payload = payload[:count]
		if err != nil {
			result.BytesScanned = int64(min(count, int(classifier.config.ScanBytes)))
			result.Reason = ClassificationReasonScanUnavailable
			zeroBytes(payload)
			return elevateSearchClassification(request, result), nil
		}
	}
	scanned := payload
	if int64(len(scanned)) > classifier.config.ScanBytes {
		scanned = scanned[:classifier.config.ScanBytes]
	}
	result.BytesScanned = int64(len(scanned))
	result.DetectedMediaType = detectCoreMediaType(scanned)
	if secretContent(scanned) {
		result.Classification = ClassificationSecret
		result.Reason = ClassificationReasonContentSecret
		zeroBytes(payload)
		return elevateSearchClassification(request, result), nil
	}
	if activeContent(scanned, request.ProviderMediaType) {
		result.Reason = ClassificationReasonActiveContent
		zeroBytes(payload)
		return elevateSearchClassification(request, result), nil
	}
	if !providerMediaCompatible(request.ProviderMediaType, result.DetectedMediaType) {
		result.Reason = ClassificationReasonMIMEConfusion
		zeroBytes(payload)
		return elevateSearchClassification(request, result), nil
	}
	if request.SourceSize > classifier.config.ScanBytes {
		result.Reason = ClassificationReasonTruncated
		zeroBytes(payload)
		return elevateSearchClassification(request, result), nil
	}
	if _, ok := decodeTextBytes(scanned); ok && !containsUnsafeBinaryControl(scanned) {
		result.Classification = ClassificationNonSecret
		result.Reason = ClassificationReasonClosedText
		result.DetectedMediaType = "text/plain; charset=utf-8"
		zeroBytes(payload)
		return elevateSearchClassification(request, result), nil
	}
	if closedSafeBinaryMedia(scanned) != "" {
		result.Classification = ClassificationNonSecret
		result.Reason = ClassificationReasonClosedText
		zeroBytes(payload)
		return elevateSearchClassification(request, result), nil
	}
	result.Reason = ClassificationReasonBinaryUnknown
	zeroBytes(payload)
	return elevateSearchClassification(request, result), nil
}

func validClassificationRequest(request ClassificationRequest) bool {
	return strings.TrimSpace(request.Path) != "" && strings.TrimSpace(request.Name) != "" && request.SourceSize >= 0 &&
		len(request.Path) <= 4096 && len(request.Name) <= 1024 && len(request.ProviderMediaType) <= 256
}

func secretPath(path, name string) bool {
	normalized := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	base := strings.ToLower(strings.TrimSpace(name))
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "id_rsa" || base == "id_ed25519" ||
		base == "credentials" || base == "shadow" || base == "secrets.yml" || base == "secrets.yaml" {
		return true
	}
	for _, signal := range []string{
		"/.ssh/", "/.kube/config", "/.aws/credentials", "/.config/gcloud/credentials",
		"/etc/shadow", "/private_key", "/credential_store", "/keyring/",
	} {
		if strings.Contains("/"+strings.TrimPrefix(normalized, "/"), signal) {
			return true
		}
	}
	return false
}

func secretContent(payload []byte) bool {
	lower := bytes.ToLower(payload)
	for _, marker := range [][]byte{
		[]byte("-----begin private key-----"), []byte("-----begin rsa private key-----"),
		[]byte("-----begin openssh private key-----"), []byte("-----begin ec private key-----"),
	} {
		if bytes.Contains(lower, marker) {
			return true
		}
	}
	for _, key := range [][]byte{
		[]byte("password"), []byte("passwd"), []byte("api_token"), []byte("api_key"),
		[]byte("access_token"), []byte("secret_key"), []byte("client_secret"),
	} {
		for offset := 0; ; {
			index := bytes.Index(lower[offset:], key)
			if index < 0 {
				break
			}
			index += offset + len(key)
			for index < len(lower) && (lower[index] == ' ' || lower[index] == '\t') {
				index++
			}
			if index < len(lower) && (lower[index] == '=' || lower[index] == ':') {
				return true
			}
			offset = index
		}
	}
	return false
}

func activeContent(payload []byte, providerMediaType string) bool {
	mediaType := normalizedMediaType(providerMediaType)
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" || mediaType == "application/xml" ||
		mediaType == "text/xml" || mediaType == "image/svg+xml" {
		return true
	}
	decoded, ok := decodeTextBytes(payload)
	if !ok {
		return false
	}
	trimmed := strings.ToLower(strings.TrimSpace(decoded))
	return strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html") ||
		strings.HasPrefix(trimmed, "<?xml") || strings.HasPrefix(trimmed, "<svg") ||
		strings.HasPrefix(trimmed, "<!entity")
}

func elevateSearchClassification(request ClassificationRequest, result ClassificationResult) ClassificationResult {
	evidence, ok := exactSearchClassificationEvidence(request)
	if ok && evidence.Classification == ClassificationSecret {
		result.Classification = ClassificationSecret
		result.Reason = ClassificationReasonSearchSecret
	}
	return result
}

func classificationSourceRevision(request ClassificationRequest) int64 {
	if evidence, ok := exactSearchClassificationEvidence(request); ok {
		return evidence.Revision
	}
	return 1
}

func exactSearchClassificationEvidence(request ClassificationRequest) (*SearchClassificationEvidence, bool) {
	evidence := request.Search
	if evidence == nil || evidence.Revision <= 0 || evidence.CatalogGenerationID != request.CatalogGenerationID ||
		evidence.SourceFingerprint != request.SourceFingerprint || backupasset.ValidateOpaqueID(evidence.CatalogGenerationID) != nil ||
		evidence.SourceFingerprint == "" {
		return nil, false
	}
	switch evidence.Classification {
	case ClassificationNonSecret, ClassificationSecret, ClassificationUnknown:
		return evidence, true
	default:
		return nil, false
	}
}

func providerMediaCompatible(providerValue, detectedValue string) bool {
	providerType := normalizedMediaType(providerValue)
	if providerType == "" || providerType == "application/octet-stream" {
		return true
	}
	detectedType := normalizedMediaType(detectedValue)
	if strings.HasPrefix(providerType, "text/") && strings.HasPrefix(detectedType, "text/") {
		return true
	}
	return providerType == detectedType
}

func normalizedMediaType(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parsed, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed)
}

func detectCoreMediaType(payload []byte) string {
	if len(payload) == 0 {
		return "text/plain; charset=utf-8"
	}
	if mediaType := closedSafeBinaryMedia(payload); mediaType != "" {
		return mediaType
	}
	return http.DetectContentType(payload)
}

func closedSafeBinaryMedia(payload []byte) string {
	switch {
	case len(payload) >= 8 && bytes.Equal(payload[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(payload) >= 3 && payload[0] == 0xff && payload[1] == 0xd8 && payload[2] == 0xff:
		return "image/jpeg"
	case len(payload) >= 6 && (string(payload[:6]) == "GIF87a" || string(payload[:6]) == "GIF89a"):
		return "image/gif"
	case len(payload) >= 12 && string(payload[:4]) == "RIFF" && string(payload[8:12]) == "WEBP":
		return "image/webp"
	case len(payload) >= 5 && string(payload[:5]) == "%PDF-":
		return "application/pdf"
	case len(payload) >= 12 && string(payload[:4]) == "RIFF" && string(payload[8:12]) == "WAVE":
		return "audio/wav"
	case len(payload) >= 4 && string(payload[:4]) == "fLaC":
		return "audio/flac"
	case len(payload) >= 4 && string(payload[:4]) == "OggS":
		return "application/ogg"
	case len(payload) >= 8 && string(payload[4:8]) == "ftyp":
		return "video/mp4"
	case len(payload) >= 4 && bytes.Equal(payload[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
		return "video/webm"
	default:
		return ""
	}
}

func decodeTextBytes(payload []byte) (string, bool) {
	if len(payload) >= 3 && bytes.Equal(payload[:3], []byte{0xef, 0xbb, 0xbf}) {
		payload = payload[3:]
	}
	if len(payload) >= 2 && (bytes.Equal(payload[:2], []byte{0xff, 0xfe}) || bytes.Equal(payload[:2], []byte{0xfe, 0xff})) {
		little := payload[0] == 0xff
		payload = payload[2:]
		if len(payload)%2 != 0 {
			return "", false
		}
		units := make([]uint16, len(payload)/2)
		for index := range units {
			if little {
				units[index] = binary.LittleEndian.Uint16(payload[index*2:])
			} else {
				units[index] = binary.BigEndian.Uint16(payload[index*2:])
			}
		}
		decoded := utf16.Decode(units)
		for _, value := range decoded {
			if value == utf8.RuneError {
				return "", false
			}
		}
		return string(decoded), true
	}
	if !utf8.Valid(payload) {
		return "", false
	}
	return string(payload), true
}

func containsUnsafeBinaryControl(payload []byte) bool {
	for _, value := range payload {
		if value == 0 || value < 0x08 || value == 0x0b || value == 0x0c || value == 0x7f {
			return true
		}
	}
	return false
}
