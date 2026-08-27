package content

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

var (
	ErrInvalidRendererRequest = errors.New("invalid content renderer request")
	ErrRendererUnsupported    = errors.New("content renderer unsupported")
	ErrMIMEConfusion          = errors.New("content MIME confusion")
	ErrRasterLimit            = errors.New("content raster limit exceeded")
)

type RendererConfig struct {
	TextBytes       int64
	HexBytes        int64
	RasterMaxPixels int64
	PDFMaxBytes     int64
	MediaMaxBytes   int64
}

type RenderRequest struct {
	Action            DeliveryAction
	Renderer          Renderer
	Profile           RendererProfile
	Range             RangePolicy
	SourceSize        int64
	Prefix            []byte
	ProviderMediaType string
	Filename          string
}

type SafePreviewSelectionRequest struct {
	SourceSize        int64
	Prefix            []byte
	ProviderMediaType string
	Filename          string
}

type RenderPlan struct {
	MediaType          string
	ContentDisposition string
	Range              RangePolicy
	SourceBytes        int64
	Size               int64
	Truncated          bool
	Bytes              []byte
}

type RendererPolicy struct {
	config RendererConfig
}

func NewRendererPolicy(config RendererConfig) (*RendererPolicy, error) {
	if config.TextBytes <= 0 || config.HexBytes <= 0 || config.RasterMaxPixels <= 0 ||
		config.PDFMaxBytes <= 0 || config.MediaMaxBytes <= 0 {
		return nil, ErrInvalidRendererRequest
	}
	return &RendererPolicy{config: config}, nil
}

func (policy *RendererPolicy) Prepare(request RenderRequest) (RenderPlan, error) {
	if policy == nil || !validRenderRequest(request) {
		return RenderPlan{}, ErrInvalidRendererRequest
	}
	switch request.Renderer {
	case RendererEscapedText:
		return policy.prepareText(request)
	case RendererPlainText:
		return policy.preparePlainText(request)
	case RendererMetadataHex:
		return policy.prepareHex(request), nil
	case RendererSafeRaster:
		return policy.prepareRaster(request)
	case RendererSameOriginPDF:
		return policy.preparePDF(request)
	case RendererNativeAudio, RendererNativeVideo:
		return policy.prepareMedia(request)
	case RendererAttachment:
		return rawRenderPlan(request, "application/octet-stream", "attachment"), nil
	default:
		return RenderPlan{}, ErrInvalidRendererRequest
	}
}

func (policy *RendererPolicy) SelectSafePreview(request SafePreviewSelectionRequest) (Renderer, RendererProfile, error) {
	if policy == nil || request.SourceSize < 0 || int64(len(request.Prefix)) > request.SourceSize ||
		len(request.ProviderMediaType) > 256 || len(request.Filename) > 4096 || request.Filename == "" {
		return "", "", ErrInvalidRendererRequest
	}
	if strings.TrimSpace(request.ProviderMediaType) != "" && normalizedMediaType(request.ProviderMediaType) == "" {
		return "", "", ErrMIMEConfusion
	}
	base := RenderRequest{
		Action: DeliveryPreview, Range: RangeSingle, SourceSize: request.SourceSize, Prefix: request.Prefix,
		ProviderMediaType: request.ProviderMediaType, Filename: request.Filename,
	}
	var renderer Renderer
	var profile RendererProfile
	switch closedSafeBinaryMedia(request.Prefix) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		renderer, profile = RendererSafeRaster, ProfileRasterV1
	case "application/pdf":
		renderer, profile = RendererSameOriginPDF, ProfilePDFV1
	case "audio/wav", "audio/flac", "audio/mpeg":
		renderer, profile = RendererNativeAudio, ProfileAudioV1
	case "video/mp4", "video/webm":
		renderer, profile = RendererNativeVideo, ProfileVideoV1
	case "application/ogg":
		switch detectOggCodecMedia(request.Prefix) {
		case "audio/ogg":
			renderer, profile = RendererNativeAudio, ProfileAudioV1
		case "video/ogg":
			renderer, profile = RendererNativeVideo, ProfileVideoV1
		default:
			return "", "", ErrRendererUnsupported
		}
	}
	if renderer != "" {
		base.Renderer, base.Profile = renderer, profile
		if _, err := policy.Prepare(base); err != nil {
			return "", "", err
		}
		return renderer, profile, nil
	}
	if providerClaimsNativePreview(request.ProviderMediaType) {
		return "", "", ErrMIMEConfusion
	}
	limit := min(int64(len(request.Prefix)), min(request.SourceSize, policy.config.TextBytes))
	if _, _, ok := decodePlainTextPrefix(request.Prefix[:limit], limit < request.SourceSize); ok {
		return RendererPlainText, ProfileTextV2, nil
	}
	return RendererMetadataHex, ProfileHexV1, nil
}

func (policy *RendererPolicy) prepareText(request RenderRequest) (RenderPlan, error) {
	limit := min(int64(len(request.Prefix)), min(request.SourceSize, policy.config.TextBytes))
	decoded, ok := decodeTextBytes(request.Prefix[:limit])
	if !ok {
		return RenderPlan{}, ErrRendererUnsupported
	}
	escaped := escapeRenderedText(decoded)
	payload := []byte(escaped)
	return RenderPlan{
		MediaType: "text/plain; charset=utf-8", ContentDisposition: safeContentDisposition("inline", request.Filename),
		Range: RangeNone, SourceBytes: limit, Size: int64(len(payload)),
		Truncated: limit < request.SourceSize, Bytes: payload,
	}, nil
}

func (policy *RendererPolicy) preparePlainText(request RenderRequest) (RenderPlan, error) {
	limit := min(int64(len(request.Prefix)), min(request.SourceSize, policy.config.TextBytes))
	decoded, consumed, ok := decodePlainTextPrefix(request.Prefix[:limit], limit < request.SourceSize)
	if !ok {
		return RenderPlan{}, ErrRendererUnsupported
	}
	payload := []byte(decoded)
	return RenderPlan{
		MediaType: "text/plain; charset=utf-8", ContentDisposition: safeContentDisposition("inline", request.Filename),
		Range: RangeNone, SourceBytes: consumed, Size: int64(len(payload)),
		Truncated: consumed < request.SourceSize, Bytes: payload,
	}, nil
}

func (policy *RendererPolicy) prepareHex(request RenderRequest) RenderPlan {
	limit := min(int64(len(request.Prefix)), min(request.SourceSize, policy.config.HexBytes))
	payload := renderHex(request.Prefix[:limit])
	return RenderPlan{
		MediaType: "text/plain; charset=utf-8", ContentDisposition: safeContentDisposition("inline", request.Filename),
		Range: RangeNone, SourceBytes: limit, Size: int64(len(payload)),
		Truncated: limit < request.SourceSize, Bytes: payload,
	}
}

func (policy *RendererPolicy) prepareRaster(request RenderRequest) (RenderPlan, error) {
	if activeContent(request.Prefix, request.ProviderMediaType) {
		return RenderPlan{}, ErrRendererUnsupported
	}
	mediaType := closedSafeBinaryMedia(request.Prefix)
	if mediaType != "image/png" && mediaType != "image/jpeg" && mediaType != "image/gif" && mediaType != "image/webp" {
		return RenderPlan{}, ErrRendererUnsupported
	}
	if !providerMediaCompatible(request.ProviderMediaType, mediaType) {
		return RenderPlan{}, ErrMIMEConfusion
	}
	width, height, err := rasterDimensions(mediaType, request.Prefix)
	if err != nil || width <= 0 || height <= 0 {
		return RenderPlan{}, ErrRendererUnsupported
	}
	if int64(width) > policy.config.RasterMaxPixels/int64(height) {
		return RenderPlan{}, ErrRasterLimit
	}
	return rawRenderPlan(request, mediaType, "inline"), nil
}

func (policy *RendererPolicy) preparePDF(request RenderRequest) (RenderPlan, error) {
	if request.SourceSize > policy.config.PDFMaxBytes || closedSafeBinaryMedia(request.Prefix) != "application/pdf" {
		return RenderPlan{}, ErrRendererUnsupported
	}
	if !providerMediaCompatible(request.ProviderMediaType, "application/pdf") {
		return RenderPlan{}, ErrMIMEConfusion
	}
	return rawRenderPlan(request, "application/pdf", "inline"), nil
}

func (policy *RendererPolicy) prepareMedia(request RenderRequest) (RenderPlan, error) {
	if request.SourceSize > policy.config.MediaMaxBytes {
		return RenderPlan{}, ErrRendererUnsupported
	}
	mediaType := detectNativeMedia(request.Prefix, request.Renderer)
	if mediaType == "" {
		return RenderPlan{}, ErrRendererUnsupported
	}
	if !nativeProviderMediaCompatible(request.ProviderMediaType, mediaType, request.Renderer) {
		return RenderPlan{}, ErrMIMEConfusion
	}
	return rawRenderPlan(request, mediaType, "inline"), nil
}

func validRenderRequest(request RenderRequest) bool {
	if request.SourceSize < 0 || int64(len(request.Prefix)) > request.SourceSize || len(request.ProviderMediaType) > 256 ||
		len(request.Filename) > 4096 || request.Filename == "" ||
		(request.Range != RangeNone && request.Range != RangeSingle) {
		return false
	}
	product := DeliveryProduct{Action: request.Action, Renderer: request.Renderer, Profile: request.Profile, Range: request.Range}
	if !validRendererProduct(product) {
		return false
	}
	if request.Action == DeliveryDownload {
		return request.Renderer == RendererAttachment && request.Profile == ProfileOriginalV1
	}
	return request.Action == DeliveryPreview && request.Renderer != RendererAttachment
}

func providerClaimsNativePreview(value string) bool {
	mediaType := normalizedMediaType(value)
	if mediaType == "image/svg+xml" {
		return false
	}
	return mediaType == "application/pdf" || mediaType == "application/ogg" ||
		strings.HasPrefix(mediaType, "image/") || strings.HasPrefix(mediaType, "audio/") ||
		strings.HasPrefix(mediaType, "video/")
}

func decodePlainTextPrefix(payload []byte, truncated bool) (string, int64, bool) {
	originalLength := len(payload)
	if len(payload) >= 3 && bytes.Equal(payload[:3], []byte{0xef, 0xbb, 0xbf}) {
		decoded, consumed, ok := decodeUTF8TextPrefix(payload[3:], truncated)
		return decoded, int64(consumed + 3), ok
	}
	if len(payload) >= 2 && (bytes.Equal(payload[:2], []byte{0xff, 0xfe}) || bytes.Equal(payload[:2], []byte{0xfe, 0xff})) {
		little := payload[0] == 0xff
		body := payload[2:]
		if len(body)%2 != 0 {
			if !truncated {
				return "", 0, false
			}
			body = body[:len(body)-1]
		}
		units := make([]uint16, len(body)/2)
		for index := range units {
			if little {
				units[index] = binary.LittleEndian.Uint16(body[index*2:])
			} else {
				units[index] = binary.BigEndian.Uint16(body[index*2:])
			}
		}
		if truncated && len(units) > 0 && units[len(units)-1] >= 0xd800 && units[len(units)-1] <= 0xdbff {
			units = units[:len(units)-1]
			body = body[:len(body)-2]
		}
		if !validUTF16Units(units) {
			return "", 0, false
		}
		decoded := utf16.Decode(units)
		if !validPlainTextRunes(decoded) {
			return "", 0, false
		}
		return string(decoded), int64(2 + len(body)), true
	}
	decoded, consumed, ok := decodeUTF8TextPrefix(payload, truncated)
	if !ok {
		return "", 0, false
	}
	if consumed > originalLength {
		return "", 0, false
	}
	return decoded, int64(consumed), true
}

func decodeUTF8TextPrefix(payload []byte, truncated bool) (string, int, bool) {
	consumed := len(payload)
	if !utf8.Valid(payload) && truncated {
		if complete, ok := trimIncompleteUTF8Suffix(payload); ok {
			payload = complete
			consumed = len(payload)
		}
	}
	if !utf8.Valid(payload) {
		return "", 0, false
	}
	runes := []rune(string(payload))
	if !validPlainTextRunes(runes) {
		return "", 0, false
	}
	return string(runes), consumed, true
}

func trimIncompleteUTF8Suffix(payload []byte) ([]byte, bool) {
	start := max(0, len(payload)-(utf8.UTFMax-1))
	for ; start < len(payload); start++ {
		if utf8.Valid(payload[:start]) && !utf8.FullRune(payload[start:]) {
			return payload[:start], true
		}
	}
	return nil, false
}

func validUTF16Units(units []uint16) bool {
	for index := 0; index < len(units); index++ {
		value := units[index]
		switch {
		case value >= 0xd800 && value <= 0xdbff:
			if index+1 >= len(units) || units[index+1] < 0xdc00 || units[index+1] > 0xdfff {
				return false
			}
			index++
		case value >= 0xdc00 && value <= 0xdfff:
			return false
		}
	}
	return true
}

func validPlainTextRunes(values []rune) bool {
	for _, value := range values {
		if value == '\t' || value == '\n' || value == '\r' {
			continue
		}
		if unicode.IsControl(value) {
			return false
		}
	}
	return true
}

func rawRenderPlan(request RenderRequest, mediaType, disposition string) RenderPlan {
	return RenderPlan{
		MediaType: mediaType, ContentDisposition: safeContentDisposition(disposition, request.Filename),
		Range: request.Range, SourceBytes: request.SourceSize, Size: request.SourceSize,
	}
}

func escapeRenderedText(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character == '\n' || character == '\r' || character == '\t':
			builder.WriteRune(character)
		case character < 0x20 || character == 0x7f:
			_, _ = fmt.Fprintf(&builder, `\x%02x`, character)
		default:
			builder.WriteRune(character)
		}
	}
	return html.EscapeString(builder.String())
}

func renderHex(payload []byte) []byte {
	var output bytes.Buffer
	for offset := 0; offset < len(payload); offset += 16 {
		end := min(offset+16, len(payload))
		_, _ = fmt.Fprintf(&output, "%08x  ", offset)
		for index := offset; index < offset+16; index++ {
			if index < end {
				_, _ = fmt.Fprintf(&output, "%02x ", payload[index])
			} else {
				_, _ = output.WriteString("   ")
			}
		}
		_, _ = output.WriteString(" |")
		for _, value := range payload[offset:end] {
			if value >= 0x20 && value <= 0x7e {
				_ = output.WriteByte(value)
			} else {
				_ = output.WriteByte('.')
			}
		}
		_, _ = output.WriteString("|\n")
	}
	return output.Bytes()
}

func rasterDimensions(mediaType string, payload []byte) (int, int, error) {
	if mediaType == "image/webp" {
		if len(payload) < 30 || string(payload[12:16]) != "VP8X" {
			return 0, 0, ErrRendererUnsupported
		}
		width := int(payload[24]) | int(payload[25])<<8 | int(payload[26])<<16
		height := int(payload[27]) | int(payload[28])<<8 | int(payload[29])<<16
		return width + 1, height + 1, nil
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(payload))
	return config.Width, config.Height, err
}

func detectNativeMedia(payload []byte, renderer Renderer) string {
	switch renderer {
	case RendererNativeAudio:
		switch {
		case len(payload) >= 12 && string(payload[:4]) == "RIFF" && string(payload[8:12]) == "WAVE":
			return "audio/wav"
		case len(payload) >= 4 && string(payload[:4]) == "fLaC":
			return "audio/flac"
		case detectOggCodecMedia(payload) == "audio/ogg":
			return "audio/ogg"
		case validID3Header(payload):
			return "audio/mpeg"
		}
	case RendererNativeVideo:
		switch {
		case len(payload) >= 8 && string(payload[4:8]) == "ftyp":
			return "video/mp4"
		case len(payload) >= 4 && bytes.Equal(payload[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
			return "video/webm"
		case detectOggCodecMedia(payload) == "video/ogg":
			return "video/ogg"
		}
	}
	return ""
}

func detectOggCodecMedia(payload []byte) string {
	if len(payload) < 28 || string(payload[:4]) != "OggS" || payload[4] != 0 || payload[5]&0x02 == 0 {
		return ""
	}
	segmentCount := int(payload[26])
	packetOffset := 27 + segmentCount
	if segmentCount == 0 || packetOffset > len(payload) {
		return ""
	}
	packetLength := 0
	packetComplete := false
	for _, segmentLength := range payload[27:packetOffset] {
		packetLength += int(segmentLength)
		if segmentLength < 255 {
			packetComplete = true
			break
		}
	}
	if !packetComplete || packetLength > len(payload)-packetOffset {
		return ""
	}
	packet := payload[packetOffset : packetOffset+packetLength]
	switch {
	case bytes.HasPrefix(packet, []byte("OpusHead")),
		bytes.HasPrefix(packet, []byte{0x01, 'v', 'o', 'r', 'b', 'i', 's'}),
		bytes.HasPrefix(packet, []byte("Speex   ")),
		bytes.HasPrefix(packet, []byte{0x7f, 'F', 'L', 'A', 'C'}),
		bytes.HasPrefix(packet, []byte("fLaC")):
		return "audio/ogg"
	case bytes.HasPrefix(packet, []byte{0x80, 't', 'h', 'e', 'o', 'r', 'a'}):
		return "video/ogg"
	default:
		return ""
	}
}

func validID3Header(payload []byte) bool {
	if len(payload) < 10 || string(payload[:3]) != "ID3" || payload[3] < 2 || payload[3] > 4 || payload[4] == 0xff {
		return false
	}
	for _, value := range payload[6:10] {
		if value&0x80 != 0 {
			return false
		}
	}
	return true
}

func nativeProviderMediaCompatible(providerValue, detected string, renderer Renderer) bool {
	providerType := normalizedMediaType(providerValue)
	if providerType == "" || providerType == "application/octet-stream" {
		return true
	}
	if providerType == "application/ogg" && (detected == "audio/ogg" || detected == "video/ogg") {
		return true
	}
	prefix := "audio/"
	if renderer == RendererNativeVideo {
		prefix = "video/"
	}
	return providerType == detected || strings.HasPrefix(providerType, prefix) && strings.HasPrefix(detected, prefix)
}

func safeContentDisposition(disposition, filename string) string {
	cleaned, safe := sanitizeContentFilename(filename)
	if !safe {
		cleaned = "download.bin"
	}
	ascii := asciiFilename(cleaned)
	encoded := url.PathEscape(cleaned)
	return disposition + `; filename="` + ascii + `"; filename*=UTF-8''` + encoded
}

func sanitizeContentFilename(filename string) (string, bool) {
	dangerous := false
	for _, character := range filename {
		if character == '\r' || character == '\n' || character == 0 || unicode.IsControl(character) ||
			character == '\u202a' || character == '\u202b' || character == '\u202d' || character == '\u202e' || character == '\u2066' || character == '\u2067' || character == '\u2068' || character == '\u2069' {
			dangerous = true
			break
		}
	}
	filename = strings.ReplaceAll(filename, "\\", "/")
	filename = filepath.Base(filename)
	filename = strings.TrimSpace(filename)
	if filename == "" || filename == "." || filename == ".." {
		return "download.bin", false
	}
	if len(filename) > 180 {
		filename = string([]rune(filename)[:min(120, utf8.RuneCountInString(filename))])
	}
	return filename, !dangerous
}

func asciiFilename(filename string) string {
	var builder strings.Builder
	for _, character := range filename {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._-", character) {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
	}
	result := strings.Trim(builder.String(), ".")
	if result == "" {
		return "download.bin"
	}
	return result
}

func CoreSecurityHeaders() map[string]string {
	return map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Referrer-Policy":              "no-referrer",
		"Content-Security-Policy":      "sandbox; default-src 'none'; frame-ancestors 'self'; object-src 'none'",
		"X-Frame-Options":              "SAMEORIGIN",
		"Cache-Control":                "private, no-store",
		"Content-Encoding":             "identity",
	}
}
