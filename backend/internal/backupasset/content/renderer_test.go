package content

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestRendererEscapedTextAndHexFreezeDerivedRepresentation(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	textSource := []byte("<script>\x00alert & text\n")
	plan, err := policy.Prepare(RenderRequest{
		Action: DeliveryPreview, Renderer: RendererEscapedText, Profile: ProfileTextV1, Range: RangeNone,
		SourceSize: int64(len(textSource) + 10), Prefix: textSource, ProviderMediaType: "text/plain", Filename: "config.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.MediaType != "text/plain; charset=utf-8" || plan.Range != RangeNone || !plan.Truncated ||
		plan.SourceBytes != int64(len(textSource)) || plan.Size != int64(len(plan.Bytes)) ||
		strings.Contains(string(plan.Bytes), "<script>") || !strings.Contains(string(plan.Bytes), "&lt;script&gt;") ||
		!strings.Contains(string(plan.Bytes), `\x00`) {
		t.Fatalf("text plan=%+v bytes=%q", plan, plan.Bytes)
	}

	hexSource := []byte{0x00, 0x01, 0x41, 0xff}
	plan, err = policy.Prepare(RenderRequest{
		Action: DeliveryPreview, Renderer: RendererMetadataHex, Profile: ProfileHexV1, Range: RangeNone,
		SourceSize: 10, Prefix: hexSource, Filename: "blob.bin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.MediaType != "text/plain; charset=utf-8" || !plan.Truncated || plan.SourceBytes != 4 ||
		!strings.Contains(string(plan.Bytes), "00000000") || !strings.Contains(string(plan.Bytes), "00 01 41 ff") {
		t.Fatalf("hex plan=%+v bytes=%q", plan, plan.Bytes)
	}
}

func TestRendererTextSupportsUTFBOMAndRejectsInvalidEncoding(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	utf16 := []byte{0xff, 0xfe}
	for _, value := range []uint16{'你', '好', '\n'} {
		var encoded [2]byte
		binary.LittleEndian.PutUint16(encoded[:], value)
		utf16 = append(utf16, encoded[:]...)
	}
	plan, err := policy.Prepare(RenderRequest{
		Action: DeliveryPreview, Renderer: RendererEscapedText, Profile: ProfileTextV1, Range: RangeNone,
		SourceSize: int64(len(utf16)), Prefix: utf16, Filename: "utf16.txt",
	})
	if err != nil || string(plan.Bytes) != "你好\n" || plan.Truncated {
		t.Fatalf("UTF-16 plan=%+v bytes=%q err=%v", plan, plan.Bytes, err)
	}
	if _, err := policy.Prepare(RenderRequest{
		Action: DeliveryPreview, Renderer: RendererEscapedText, Profile: ProfileTextV1, Range: RangeNone,
		SourceSize: 2, Prefix: []byte{0xff, 0xff}, Filename: "bad.txt",
	}); !errors.Is(err, ErrRendererUnsupported) {
		t.Fatalf("invalid text encoding error=%v", err)
	}
}

func TestRendererSafePreviewResolvesReadableTextFamiliesWithExactFidelity(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	utf16 := []byte{0xff, 0xfe}
	for _, value := range []uint16{'名', '=', '值', '\r', '\n'} {
		var encoded [2]byte
		binary.LittleEndian.PutUint16(encoded[:], value)
		utf16 = append(utf16, encoded[:]...)
	}
	tests := []struct {
		name     string
		filename string
		mime     string
		payload  []byte
		want     string
	}{
		{name: "generic utf8 config", filename: "service.conf", mime: "application/octet-stream", payload: []byte("enabled=true\r\n"), want: "enabled=true\r\n"},
		{name: "generic utf16 config", filename: "service.ini", mime: "application/octet-stream", payload: utf16, want: "名=值\r\n"},
		{name: "json", filename: "record.json", mime: "application/json", payload: []byte("{\"message\":\"你好\"}\n"), want: "{\"message\":\"你好\"}\n"},
		{name: "yaml", filename: "record.yaml", mime: "application/yaml", payload: []byte("name: value\n"), want: "name: value\n"},
		{name: "toml", filename: "record.toml", mime: "application/toml", payload: []byte("name = \"value\"\n"), want: "name = \"value\"\n"},
		{name: "log", filename: "service.log", mime: "text/plain", payload: []byte("INFO\tready\r\n"), want: "INFO\tready\r\n"},
		{name: "source", filename: "main.go", mime: "text/x-go", payload: []byte("if a < b && b > 0 {\n\tprintln(\"ok\")\n}\n"), want: "if a < b && b > 0 {\n\tprintln(\"ok\")\n}\n"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			renderer, profile, err := policy.SelectSafePreview(SafePreviewSelectionRequest{
				SourceSize: int64(len(testCase.payload)), Prefix: testCase.payload,
				ProviderMediaType: testCase.mime, Filename: testCase.filename,
			})
			if err != nil || renderer != RendererPlainText || profile != ProfileTextV2 {
				t.Fatalf("selection=%s/%s err=%v", renderer, profile, err)
			}
			plan, err := policy.Prepare(RenderRequest{
				Action: DeliveryPreview, Renderer: renderer, Profile: profile, Range: RangeNone,
				SourceSize: int64(len(testCase.payload)), Prefix: testCase.payload,
				ProviderMediaType: testCase.mime, Filename: testCase.filename,
			})
			if err != nil || string(plan.Bytes) != testCase.want || plan.MediaType != "text/plain; charset=utf-8" || plan.Truncated {
				t.Fatalf("plan=%+v bytes=%q want=%q err=%v", plan, plan.Bytes, testCase.want, err)
			}
			for _, encoded := range []string{"&lt;", "&gt;", "&amp;", "&#34;", "00000000  "} {
				if strings.Contains(string(plan.Bytes), encoded) {
					t.Fatalf("faithful text contains transform %q: %q", encoded, plan.Bytes)
				}
			}
		})
	}
}

func TestRendererSafePreviewZeroLengthAndTruncationAreExact(t *testing.T) {
	policy, err := NewRendererPolicy(RendererConfig{
		TextBytes: 8, HexBytes: 8, RasterMaxPixels: 1 << 20, PDFMaxBytes: 1 << 20, MediaMaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name       string
		payload    []byte
		sourceSize int64
		want       string
		truncated  bool
	}{
		{name: "zero", payload: []byte{}, sourceSize: 0, want: ""},
		{name: "bounded", payload: []byte("12345678"), sourceSize: 12, want: "12345678", truncated: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			renderer, profile, selectErr := policy.SelectSafePreview(SafePreviewSelectionRequest{
				SourceSize: testCase.sourceSize, Prefix: testCase.payload, ProviderMediaType: "application/octet-stream", Filename: "record.cfg",
			})
			if selectErr != nil || renderer != RendererPlainText || profile != ProfileTextV2 {
				t.Fatalf("selection=%s/%s err=%v", renderer, profile, selectErr)
			}
			plan, prepareErr := policy.Prepare(RenderRequest{
				Action: DeliveryPreview, Renderer: renderer, Profile: profile, Range: RangeNone,
				SourceSize: testCase.sourceSize, Prefix: testCase.payload, ProviderMediaType: "application/octet-stream", Filename: "record.cfg",
			})
			if prepareErr != nil || string(plan.Bytes) != testCase.want || plan.Truncated != testCase.truncated || plan.SourceBytes != int64(len(testCase.payload)) {
				t.Fatalf("plan=%+v bytes=%q err=%v", plan, plan.Bytes, prepareErr)
			}
		})
	}
}

func TestRendererSafePreviewBinaryBytesCannotBeOverriddenByTextHints(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	payload := []byte{'n', 'a', 'm', 'e', '=', 0, 1, 2, 'x'}
	renderer, profile, err := policy.SelectSafePreview(SafePreviewSelectionRequest{
		SourceSize: int64(len(payload)), Prefix: payload, ProviderMediaType: "text/plain", Filename: "deceptive.yaml",
	})
	if err != nil || renderer != RendererMetadataHex || profile != ProfileHexV1 {
		t.Fatalf("selection=%s/%s err=%v", renderer, profile, err)
	}
}

func TestRendererSafePreviewRejectsMalformedEncodingAtTruncationBoundary(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "arbitrary invalid UTF-8 suffix", payload: []byte{'o', 'k', 0xff}},
		{name: "overlong UTF-8 suffix", payload: []byte{'o', 'k', 0xc0, 0xaf}},
		{name: "UTF-16 isolated high surrogate", payload: []byte{0xff, 0xfe, 0x00, 0xd8, 'x', 0}},
		{name: "UTF-16 isolated low surrogate", payload: []byte{0xff, 0xfe, 0x00, 0xdc}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			renderer, profile, err := policy.SelectSafePreview(SafePreviewSelectionRequest{
				SourceSize: int64(len(testCase.payload) + 1), Prefix: testCase.payload,
				ProviderMediaType: "application/octet-stream", Filename: "record.cfg",
			})
			if err != nil || renderer != RendererMetadataHex || profile != ProfileHexV1 {
				t.Fatalf("selection=%s/%s err=%v", renderer, profile, err)
			}
		})
	}
}

func TestRendererSafePreviewAcceptsOnlyWellFormedIncompleteEncodingSuffix(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	for _, testCase := range []struct {
		name       string
		payload    []byte
		want       string
		wantSource int64
	}{
		{name: "UTF-8 partial rune", payload: []byte{'o', 'k', 0xe4, 0xbd}, want: "ok", wantSource: 2},
		{name: "UTF-16 terminal high surrogate", payload: []byte{0xff, 0xfe, 'o', 0, 'k', 0, 0x00, 0xd8}, want: "ok", wantSource: 6},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			renderer, profile, err := policy.SelectSafePreview(SafePreviewSelectionRequest{
				SourceSize: int64(len(testCase.payload) + 2), Prefix: testCase.payload,
				ProviderMediaType: "application/octet-stream", Filename: "record.cfg",
			})
			if err != nil || renderer != RendererPlainText || profile != ProfileTextV2 {
				t.Fatalf("selection=%s/%s err=%v", renderer, profile, err)
			}
			plan, err := policy.Prepare(RenderRequest{
				Action: DeliveryPreview, Renderer: renderer, Profile: profile, Range: RangeNone,
				SourceSize: int64(len(testCase.payload) + 2), Prefix: testCase.payload,
				ProviderMediaType: "application/octet-stream", Filename: "record.cfg",
			})
			if err != nil || string(plan.Bytes) != testCase.want || plan.SourceBytes != testCase.wantSource || !plan.Truncated {
				t.Fatalf("plan=%+v bytes=%q err=%v", plan, plan.Bytes, err)
			}
		})
	}
}

func TestRendererSafePreviewSelectsSignatureProvenNativeProducts(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	pngPayload := encodeRasterForTest(t, "png", 2, 2)
	tests := []struct {
		name     string
		payload  []byte
		renderer Renderer
		profile  RendererProfile
	}{
		{name: "raster", payload: pngPayload, renderer: RendererSafeRaster, profile: ProfileRasterV1},
		{name: "pdf", payload: []byte("%PDF-1.7\nbody"), renderer: RendererSameOriginPDF, profile: ProfilePDFV1},
		{name: "audio", payload: []byte("RIFFxxxxWAVEfmt "), renderer: RendererNativeAudio, profile: ProfileAudioV1},
		{name: "ID3 audio", payload: []byte("ID3\x04\x00\x00\x00\x00\x00\x00"), renderer: RendererNativeAudio, profile: ProfileAudioV1},
		{name: "video", payload: []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, renderer: RendererNativeVideo, profile: ProfileVideoV1},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			renderer, profile, err := policy.SelectSafePreview(SafePreviewSelectionRequest{
				SourceSize: int64(len(testCase.payload)), Prefix: testCase.payload,
				ProviderMediaType: "application/octet-stream", Filename: "asset.bin",
			})
			if err != nil || renderer != testCase.renderer || profile != testCase.profile {
				t.Fatalf("selection=%s/%s err=%v", renderer, profile, err)
			}
		})
	}
}

func TestRendererSafePreviewResolvesOggFromCodecSignatureNotProviderHint(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	for _, testCase := range []struct {
		name     string
		payload  []byte
		mime     string
		renderer Renderer
		profile  RendererProfile
		wantErr  error
	}{
		{
			name: "container MIME Opus is audio", payload: oggPacketForTest([]byte("OpusHead\x01\x02")),
			mime: "application/ogg", renderer: RendererNativeAudio, profile: ProfileAudioV1,
		},
		{
			name: "generic Theora is video", payload: oggPacketForTest(append([]byte{0x80}, []byte("theora\x01")...)),
			mime: "application/octet-stream", renderer: RendererNativeVideo, profile: ProfileVideoV1,
		},
		{
			name: "provider cannot relabel Theora as audio", payload: oggPacketForTest(append([]byte{0x80}, []byte("theora\x01")...)),
			mime: "audio/ogg", wantErr: ErrMIMEConfusion,
		},
		{
			name: "ambiguous Ogg container is unsupported", payload: oggPacketForTest([]byte("future-codec")),
			mime: "application/ogg", wantErr: ErrRendererUnsupported,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			renderer, profile, err := policy.SelectSafePreview(SafePreviewSelectionRequest{
				SourceSize: int64(len(testCase.payload)), Prefix: testCase.payload,
				ProviderMediaType: testCase.mime, Filename: "asset.ogg",
			})
			if !errors.Is(err, testCase.wantErr) || renderer != testCase.renderer || profile != testCase.profile {
				t.Fatalf("selection=%s/%s err=%v want=%s/%s err=%v", renderer, profile, err,
					testCase.renderer, testCase.profile, testCase.wantErr)
			}
		})
	}
}

func TestRendererSafePreviewDoesNotTreatID3TextAsAnAudioSignature(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	payload := []byte("ID3 is a readable configuration value\n")
	renderer, profile, err := policy.SelectSafePreview(SafePreviewSelectionRequest{
		SourceSize: int64(len(payload)), Prefix: payload,
		ProviderMediaType: "application/octet-stream", Filename: "record.conf",
	})
	if err != nil || renderer != RendererPlainText || profile != ProfileTextV2 {
		t.Fatalf("selection=%s/%s err=%v", renderer, profile, err)
	}
}

func TestRendererSafePreviewActiveContentIsInertPlainText(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	for _, testCase := range []struct {
		name, mime, source string
	}{
		{name: "html", mime: "text/html", source: "<!doctype html><script>safe & visible</script>"},
		{name: "xml", mime: "application/xml", source: "<?xml version=\"1.0\"?><root/>"},
		{name: "svg", mime: "image/svg+xml", source: "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			renderer, profile, err := policy.SelectSafePreview(SafePreviewSelectionRequest{
				SourceSize: int64(len(testCase.source)), Prefix: []byte(testCase.source),
				ProviderMediaType: testCase.mime, Filename: testCase.name,
			})
			if err != nil || renderer != RendererPlainText || profile != ProfileTextV2 {
				t.Fatalf("selection=%s/%s err=%v", renderer, profile, err)
			}
			plan, err := policy.Prepare(RenderRequest{
				Action: DeliveryPreview, Renderer: renderer, Profile: profile, Range: RangeNone,
				SourceSize: int64(len(testCase.source)), Prefix: []byte(testCase.source),
				ProviderMediaType: testCase.mime, Filename: testCase.name,
			})
			if err != nil || string(plan.Bytes) != testCase.source || plan.MediaType != "text/plain; charset=utf-8" {
				t.Fatalf("inert plan=%+v bytes=%q err=%v", plan, plan.Bytes, err)
			}
		})
	}
}

func TestRendererSafePreviewKnownNativeFailuresDoNotDowngradeToHex(t *testing.T) {
	pngPayload := encodeRasterForTest(t, "png", 4, 4)
	limited, err := NewRendererPolicy(RendererConfig{
		TextBytes: 64, HexBytes: 64, RasterMaxPixels: 10, PDFMaxBytes: 8, MediaMaxBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		payload []byte
		mime    string
		wantErr error
	}{
		{name: "MIME confusion", payload: encodeRasterForTest(t, "jpeg", 2, 2), mime: "image/png", wantErr: ErrMIMEConfusion},
		{name: "pixel limit", payload: pngPayload, mime: "application/octet-stream", wantErr: ErrRasterLimit},
		{name: "PDF size", payload: []byte("%PDF-1.7\nbody"), mime: "application/octet-stream", wantErr: ErrRendererUnsupported},
		{name: "media size", payload: []byte("RIFFxxxxWAVEfmt "), mime: "application/octet-stream", wantErr: ErrRendererUnsupported},
		{name: "claimed native without signature", payload: []byte("plain text"), mime: "image/png", wantErr: ErrMIMEConfusion},
		{name: "malformed Provider MIME", payload: []byte("plain text"), mime: "image/png; charset", wantErr: ErrMIMEConfusion},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			renderer, profile, selectErr := limited.SelectSafePreview(SafePreviewSelectionRequest{
				SourceSize: int64(len(testCase.payload)), Prefix: testCase.payload,
				ProviderMediaType: testCase.mime, Filename: "asset.bin",
			})
			if !errors.Is(selectErr, testCase.wantErr) || renderer != "" || profile != "" {
				t.Fatalf("selection=%s/%s err=%v want=%v", renderer, profile, selectErr, testCase.wantErr)
			}
		})
	}
}

func TestRendererSafeRasterUsesMagicAndPixelBounds(t *testing.T) {
	pngBytes := encodeRasterForTest(t, "png", 2, 3)
	policy := newRendererPolicyForTest(t)
	plan, err := policy.Prepare(RenderRequest{
		Action: DeliveryPreview, Renderer: RendererSafeRaster, Profile: ProfileRasterV1, Range: RangeSingle,
		SourceSize: int64(len(pngBytes)), Prefix: pngBytes, ProviderMediaType: "image/png", Filename: "image.png",
	})
	if err != nil || plan.MediaType != "image/png" || plan.Size != int64(len(pngBytes)) || plan.Truncated ||
		plan.SourceBytes != int64(len(pngBytes)) || plan.Range != RangeSingle {
		t.Fatalf("raster plan=%+v err=%v", plan, err)
	}

	jpegBytes := encodeRasterForTest(t, "jpeg", 2, 3)
	if _, err := policy.Prepare(RenderRequest{
		Action: DeliveryPreview, Renderer: RendererSafeRaster, Profile: ProfileRasterV1, Range: RangeNone,
		SourceSize: int64(len(jpegBytes)), Prefix: jpegBytes, ProviderMediaType: "image/png", Filename: "confused.png",
	}); !errors.Is(err, ErrMIMEConfusion) {
		t.Fatalf("MIME confusion error=%v", err)
	}

	limited, err := NewRendererPolicy(RendererConfig{TextBytes: 64, HexBytes: 64, RasterMaxPixels: 10, PDFMaxBytes: 1 << 20, MediaMaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	bomb := encodeRasterForTest(t, "png", 4, 4)
	if _, err := limited.Prepare(RenderRequest{
		Action: DeliveryPreview, Renderer: RendererSafeRaster, Profile: ProfileRasterV1, Range: RangeNone,
		SourceSize: int64(len(bomb)), Prefix: bomb, Filename: "bomb.png",
	}); !errors.Is(err, ErrRasterLimit) {
		t.Fatalf("pixel limit error=%v", err)
	}
}

func TestRendererNeverReturnsActiveContentInline(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	active := []struct {
		name, mime, source string
	}{
		{name: "html", mime: "text/html", source: "<!doctype html><script></script>"},
		{name: "xml", mime: "application/xml", source: "<?xml version=\"1.0\"?><root/>"},
		{name: "svg", mime: "image/svg+xml", source: "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"},
	}
	for _, testCase := range active {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := policy.Prepare(RenderRequest{
				Action: DeliveryPreview, Renderer: RendererSafeRaster, Profile: ProfileRasterV1, Range: RangeNone,
				SourceSize: int64(len(testCase.source)), Prefix: []byte(testCase.source), ProviderMediaType: testCase.mime,
				Filename: testCase.name,
			}); !errors.Is(err, ErrRendererUnsupported) {
				t.Fatalf("active inline error=%v", err)
			}
			plan, err := policy.Prepare(RenderRequest{
				Action: DeliveryPreview, Renderer: RendererEscapedText, Profile: ProfileTextV1, Range: RangeNone,
				SourceSize: int64(len(testCase.source)), Prefix: []byte(testCase.source), ProviderMediaType: testCase.mime,
				Filename: testCase.name,
			})
			if err != nil || plan.MediaType != "text/plain; charset=utf-8" || strings.Contains(string(plan.Bytes), "<script>") || strings.Contains(string(plan.Bytes), "<svg") {
				t.Fatalf("safe active fallback plan=%+v bytes=%q err=%v", plan, plan.Bytes, err)
			}
		})
	}
}

func TestRendererPDFMediaAndAttachmentClosedMatrix(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	tests := []struct {
		name      string
		renderer  Renderer
		profile   RendererProfile
		source    []byte
		mime      string
		wantMedia string
	}{
		{name: "pdf", renderer: RendererSameOriginPDF, profile: ProfilePDFV1, source: []byte("%PDF-1.7\nbody"), mime: "application/pdf", wantMedia: "application/pdf"},
		{name: "wav", renderer: RendererNativeAudio, profile: ProfileAudioV1, source: []byte("RIFFxxxxWAVEfmt "), mime: "audio/wav", wantMedia: "audio/wav"},
		{name: "flac", renderer: RendererNativeAudio, profile: ProfileAudioV1, source: []byte("fLaCxxxx"), mime: "audio/flac", wantMedia: "audio/flac"},
		{name: "mp4", renderer: RendererNativeVideo, profile: ProfileVideoV1, source: []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, mime: "video/mp4", wantMedia: "video/mp4"},
		{name: "webm", renderer: RendererNativeVideo, profile: ProfileVideoV1, source: []byte{0x1a, 0x45, 0xdf, 0xa3, 0x01}, mime: "video/webm", wantMedia: "video/webm"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			plan, err := policy.Prepare(RenderRequest{
				Action: DeliveryPreview, Renderer: testCase.renderer, Profile: testCase.profile, Range: RangeSingle,
				SourceSize: int64(len(testCase.source)), Prefix: testCase.source, ProviderMediaType: testCase.mime,
				Filename: testCase.name,
			})
			if err != nil || plan.MediaType != testCase.wantMedia || plan.Size != int64(len(testCase.source)) || plan.Truncated {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
		})
	}

	plan, err := policy.Prepare(RenderRequest{
		Action: DeliveryDownload, Renderer: RendererAttachment, Profile: ProfileOriginalV1, Range: RangeSingle,
		SourceSize: 123, Filename: "../evil\r\nX-Test: injected\u202e.exe",
	})
	if err != nil || plan.MediaType != "application/octet-stream" || !strings.HasPrefix(plan.ContentDisposition, "attachment;") ||
		strings.ContainsAny(plan.ContentDisposition, "\r\n") || strings.Contains(plan.ContentDisposition, "../") || strings.Contains(plan.ContentDisposition, "X-Test") ||
		plan.Size != 123 || plan.Truncated {
		t.Fatalf("attachment plan=%+v err=%v", plan, err)
	}
}

func TestRendererSecurityHeadersAreFixedAndSameOriginSafe(t *testing.T) {
	headers := CoreSecurityHeaders()
	want := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Referrer-Policy":              "no-referrer",
		"Content-Security-Policy":      "sandbox; default-src 'none'; frame-ancestors 'self'; object-src 'none'",
		"X-Frame-Options":              "SAMEORIGIN",
		"Cache-Control":                "private, no-store",
		"Content-Encoding":             "identity",
	}
	if len(headers) != len(want) {
		t.Fatalf("headers=%+v", headers)
	}
	for key, value := range want {
		if headers[key] != value {
			t.Fatalf("header %s=%q", key, headers[key])
		}
	}
}

func TestRendererRejectsEveryIllegalCoupledProduct(t *testing.T) {
	policy := newRendererPolicyForTest(t)
	base := RenderRequest{
		Action: DeliveryPreview, Renderer: RendererSafeRaster, Profile: ProfileRasterV1, Range: RangeNone,
		SourceSize: 1, Prefix: []byte{0x89}, Filename: "image.png",
	}
	invalid := []RenderRequest{
		{},
		func() RenderRequest { value := base; value.Action = DeliveryDownload; return value }(),
		func() RenderRequest { value := base; value.Profile = ProfilePDFV1; return value }(),
		func() RenderRequest { value := base; value.Range = RangePolicy("future"); return value }(),
		func() RenderRequest { value := base; value.SourceSize = -1; return value }(),
		func() RenderRequest {
			value := base
			value.Renderer = RendererAttachment
			value.Profile = ProfileOriginalV1
			return value
		}(),
	}
	for index, request := range invalid {
		if _, err := policy.Prepare(request); !errors.Is(err, ErrInvalidRendererRequest) {
			t.Fatalf("invalid product %d error=%v", index, err)
		}
	}
}

func newRendererPolicyForTest(t *testing.T) *RendererPolicy {
	t.Helper()
	policy, err := NewRendererPolicy(RendererConfig{
		TextBytes: 1 << 10, HexBytes: 1 << 10, RasterMaxPixels: 1 << 20,
		PDFMaxBytes: 1 << 20, MediaMaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func encodeRasterForTest(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 1, A: 255})
		}
	}
	var output bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&output, canvas)
	case "jpeg":
		err = jpeg.Encode(&output, canvas, nil)
	default:
		t.Fatalf("unsupported test raster format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func oggPacketForTest(packet []byte) []byte {
	if len(packet) == 0 || len(packet) > 254 {
		panic("invalid Ogg test packet")
	}
	payload := make([]byte, 28+len(packet))
	copy(payload, "OggS")
	payload[4] = 0
	payload[5] = 2
	payload[26] = 1
	payload[27] = byte(len(packet))
	copy(payload[28:], packet)
	return payload
}
