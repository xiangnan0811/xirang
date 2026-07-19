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
