package capabilities

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

func TestThumbnailReencodesStaticRasterWithoutMutatingSource(t *testing.T) {
	inputImage := image.NewRGBA(image.Rect(0, 0, 4, 2))
	inputImage.Set(0, 0, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, inputImage); err != nil {
		t.Fatal(err)
	}
	source := append([]byte(nil), encoded.Bytes()...)
	before := append([]byte(nil), source...)
	result, err := Thumbnail(context.Background(), source, "image/png", ImageOptions{Width: 2, Height: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, before) {
		t.Fatal("thumbnail mutated source bytes")
	}
	if result.MediaType != "image/png" || result.Width != 2 || result.Height != 1 || len(result.Content) == 0 {
		t.Fatalf("unexpected thumbnail: %+v", result)
	}
	decoded, format, err := image.Decode(bytes.NewReader(result.Content))
	if err != nil || format != "png" || decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 1 {
		t.Fatalf("unsafe thumbnail format=%q bounds=%v err=%v", format, decoded.Bounds(), err)
	}
}

func TestThumbnailRejectsMalformedAndActiveContent(t *testing.T) {
	fixtures := []struct {
		name      string
		mediaType string
	}{
		{"malformed-image-truncated.png", "image/png"},
		{"active-content.svg", "image/svg+xml"},
		{"active-content.html", "text/html"},
	}
	for _, testCase := range fixtures {
		payload := readCapabilityFixture(t, testCase.name)
		if _, err := Thumbnail(context.Background(), payload, testCase.mediaType, ImageOptions{Width: 64, Height: 64}); !errors.Is(err, capabilityspec.ErrUnsupportedMedia) && !errors.Is(err, ErrInvalidToolOutput) {
			t.Fatalf("fixture %s error=%v", testCase.name, err)
		}
	}
}

func TestImageFormatMediaTypeCoversEveryAdvertisedRaster(t *testing.T) {
	tests := map[string]string{
		"jpeg": "image/jpeg",
		"png":  "image/png",
		"webp": "image/webp",
		"gif":  "image/gif",
		"tiff": "image/tiff",
		"bmp":  "image/bmp",
	}
	for format, mediaType := range tests {
		if got := imageFormatMediaType(format); got != mediaType {
			t.Fatalf("format %q maps to %q, want %q", format, got, mediaType)
		}
	}
}

func readCapabilityFixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
