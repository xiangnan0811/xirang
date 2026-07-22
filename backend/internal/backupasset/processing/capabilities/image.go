package capabilities

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	_ "image/jpeg"
	"image/png"
	"math"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

type ImageOptions struct {
	Width  int
	Height int
}

type ImageResult struct {
	MediaType string
	Width     int
	Height    int
	Content   []byte
}

type RasterInfo struct {
	MediaType string
	Width     int
	Height    int
	Frames    int
	Pixels    int64
}

func Thumbnail(ctx context.Context, source []byte, declaredMediaType string, options ImageOptions) (ImageResult, error) {
	if ctx == nil || options.Width <= 0 || options.Height <= 0 || options.Width > 4096 || options.Height > 4096 {
		return ImageResult{}, ErrInvalidInvocation
	}
	profile, ok := capabilityspec.Lookup(capabilityspec.CapabilityImageThumbnail, capabilityspec.ProfileRasterThumbnailV1, false)
	if !ok || int64(len(source)) > profile.Limits.MaxInputBytes {
		return ImageResult{}, ErrInputLimit
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(source))
	pixels, pixelsOK := rasterPixels(config.Width, config.Height)
	if err != nil || config.Width <= 0 || config.Height <= 0 || !pixelsOK || pixels > profile.Limits.MaxPixels {
		return ImageResult{}, ErrInvalidToolOutput
	}
	if err := profile.ValidateMedia(declaredMediaType, imageFormatMediaType(format)); err != nil {
		return ImageResult{}, err
	}
	decoded, actualFormat, err := image.Decode(bytes.NewReader(source))
	if err != nil || actualFormat != format {
		return ImageResult{}, ErrInvalidToolOutput
	}
	if err := ctx.Err(); err != nil {
		return ImageResult{}, err
	}
	scale := math.Min(float64(options.Width)/float64(config.Width), float64(options.Height)/float64(config.Height))
	if scale > 1 {
		scale = 1
	}
	width := max(1, int(math.Round(float64(config.Width)*scale)))
	height := max(1, int(math.Round(float64(config.Height)*scale)))
	output := image.NewRGBA(image.Rect(0, 0, width, height))
	bounds := decoded.Bounds()
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return ImageResult{}, err
		}
		sourceY := bounds.Min.Y + y*bounds.Dy()/height
		for x := 0; x < width; x++ {
			sourceX := bounds.Min.X + x*bounds.Dx()/width
			output.Set(x, y, color.NRGBAModel.Convert(decoded.At(sourceX, sourceY)))
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, output); err != nil || int64(encoded.Len()) > profile.Limits.MaxOutputBytes {
		return ImageResult{}, ErrInvalidToolOutput
	}
	return ImageResult{MediaType: "image/png", Width: width, Height: height, Content: encoded.Bytes()}, nil
}

func imageFormatMediaType(format string) string {
	switch format {
	case "jpeg":
		return "image/jpeg"
	case "png":
		return "image/png"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	case "tiff":
		return "image/tiff"
	case "bmp":
		return "image/bmp"
	default:
		return ""
	}
}

func InspectRaster(
	source []byte,
	declaredMediaType string,
	maxInputBytes int64,
	maxPixels int64,
	maxFrames int,
) (RasterInfo, error) {
	if len(source) == 0 || int64(len(source)) > maxInputBytes || maxInputBytes <= 0 || maxPixels <= 0 || maxFrames <= 0 {
		return RasterInfo{}, ErrInputLimit
	}
	var (
		info RasterInfo
		err  error
	)
	switch {
	case len(source) >= 3 && source[0] == 0xff && source[1] == 0xd8 && source[2] == 0xff:
		info, err = inspectStandardRaster(source, "jpeg")
	case len(source) >= 8 && bytes.Equal(source[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		info, err = inspectStandardRaster(source, "png")
	case len(source) >= 6 && (string(source[:6]) == "GIF87a" || string(source[:6]) == "GIF89a"):
		info, err = inspectGIF(source)
	case len(source) >= 12 && string(source[:4]) == "RIFF" && string(source[8:12]) == "WEBP":
		info, err = inspectWebP(source)
	case len(source) >= 8 && (bytes.Equal(source[:4], []byte{'I', 'I', 42, 0}) || bytes.Equal(source[:4], []byte{'M', 'M', 0, 42})):
		info, err = inspectTIFF(source)
	case len(source) >= 26 && string(source[:2]) == "BM":
		info, err = inspectBMP(source)
	default:
		return RasterInfo{}, capabilityspec.ErrUnsupportedMedia
	}
	if err != nil || info.MediaType != declaredMediaType || info.Width <= 0 || info.Height <= 0 ||
		info.Frames <= 0 || info.Frames > maxFrames || info.Pixels <= 0 || info.Pixels > maxPixels {
		return RasterInfo{}, ErrInvalidToolOutput
	}
	return info, nil
}

func ValidateRasterOutput(
	source []byte,
	declaredMediaType string,
	maxOutputBytes int64,
	maxPixels int64,
	maxFrames int,
) (RasterInfo, error) {
	info, err := InspectRaster(source, declaredMediaType, maxOutputBytes, maxPixels, maxFrames)
	if err != nil {
		return RasterInfo{}, err
	}
	switch declaredMediaType {
	case "image/jpeg", "image/png":
		decoded, format, decodeErr := image.Decode(bytes.NewReader(source))
		if decodeErr != nil || imageFormatMediaType(format) != declaredMediaType ||
			decoded.Bounds().Dx() != info.Width || decoded.Bounds().Dy() != info.Height {
			return RasterInfo{}, ErrInvalidToolOutput
		}
	case "image/gif":
		decoded, decodeErr := gif.DecodeAll(bytes.NewReader(source))
		if decodeErr != nil || len(decoded.Image) != info.Frames {
			return RasterInfo{}, ErrInvalidToolOutput
		}
	}
	return info, nil
}

func inspectStandardRaster(source []byte, expectedFormat string) (RasterInfo, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(source))
	if err != nil || format != expectedFormat || config.Width <= 0 || config.Height <= 0 {
		return RasterInfo{}, ErrInvalidToolOutput
	}
	pixels, ok := rasterPixels(config.Width, config.Height)
	if !ok {
		return RasterInfo{}, ErrInputLimit
	}
	return RasterInfo{
		MediaType: imageFormatMediaType(format), Width: config.Width, Height: config.Height, Frames: 1, Pixels: pixels,
	}, nil
}

func inspectGIF(source []byte) (RasterInfo, error) {
	if len(source) < 14 {
		return RasterInfo{}, ErrInvalidToolOutput
	}
	width := int(binary.LittleEndian.Uint16(source[6:8]))
	height := int(binary.LittleEndian.Uint16(source[8:10]))
	position := 13
	if source[10]&0x80 != 0 {
		position += 3 * (1 << ((source[10] & 0x07) + 1))
	}
	frames := 0
	totalPixels := int64(0)
	for position < len(source) {
		switch source[position] {
		case 0x3b:
			position++
			if position != len(source) || frames == 0 {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			return RasterInfo{MediaType: "image/gif", Width: width, Height: height, Frames: frames, Pixels: totalPixels}, nil
		case 0x21:
			if position+2 > len(source) {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			position += 2
			var ok bool
			position, ok = skipGIFSubBlocks(source, position)
			if !ok {
				return RasterInfo{}, ErrInvalidToolOutput
			}
		case 0x2c:
			if position+10 > len(source) {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			frameWidth := int(binary.LittleEndian.Uint16(source[position+5 : position+7]))
			frameHeight := int(binary.LittleEndian.Uint16(source[position+7 : position+9]))
			pixels, ok := rasterPixels(frameWidth, frameHeight)
			if !ok {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			totalPixels += pixels
			frames++
			packed := source[position+9]
			position += 10
			if packed&0x80 != 0 {
				position += 3 * (1 << ((packed & 0x07) + 1))
			}
			if position >= len(source) {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			position++
			position, ok = skipGIFSubBlocks(source, position)
			if !ok {
				return RasterInfo{}, ErrInvalidToolOutput
			}
		default:
			return RasterInfo{}, ErrInvalidToolOutput
		}
	}
	return RasterInfo{}, ErrInvalidToolOutput
}

func skipGIFSubBlocks(source []byte, position int) (int, bool) {
	for position < len(source) {
		size := int(source[position])
		position++
		if size == 0 {
			return position, true
		}
		if size > len(source)-position {
			return 0, false
		}
		position += size
	}
	return 0, false
}

func inspectWebP(source []byte) (RasterInfo, error) {
	if len(source) < 20 || int(binary.LittleEndian.Uint32(source[4:8]))+8 != len(source) {
		return RasterInfo{}, ErrInvalidToolOutput
	}
	position := 12
	width, height, frames := 0, 0, 0
	totalPixels := int64(0)
	animated := false
	for position < len(source) {
		if position+8 > len(source) {
			return RasterInfo{}, ErrInvalidToolOutput
		}
		kind := string(source[position : position+4])
		size := int(binary.LittleEndian.Uint32(source[position+4 : position+8]))
		position += 8
		if size < 0 || size > len(source)-position {
			return RasterInfo{}, ErrInvalidToolOutput
		}
		payload := source[position : position+size]
		switch kind {
		case "VP8X":
			if len(payload) != 10 {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			animated = payload[0]&0x02 != 0
			width = 1 + littleEndian24(payload[4:7])
			height = 1 + littleEndian24(payload[7:10])
		case "VP8 ":
			if len(payload) < 10 || !bytes.Equal(payload[3:6], []byte{0x9d, 0x01, 0x2a}) {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			width = int(binary.LittleEndian.Uint16(payload[6:8]) & 0x3fff)
			height = int(binary.LittleEndian.Uint16(payload[8:10]) & 0x3fff)
		case "VP8L":
			if len(payload) < 5 || payload[0] != 0x2f {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			bits := binary.LittleEndian.Uint32(payload[1:5])
			width = int(bits&0x3fff) + 1
			height = int(bits>>14&0x3fff) + 1
		case "ANMF":
			if len(payload) < 16 {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			frameWidth := 1 + littleEndian24(payload[6:9])
			frameHeight := 1 + littleEndian24(payload[9:12])
			pixels, ok := rasterPixels(frameWidth, frameHeight)
			if !ok {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			totalPixels += pixels
			frames++
		}
		position += size
		if size%2 != 0 {
			position++
		}
		if position > len(source) {
			return RasterInfo{}, ErrInvalidToolOutput
		}
	}
	canvasPixels, ok := rasterPixels(width, height)
	if !ok || animated && frames == 0 {
		return RasterInfo{}, ErrInvalidToolOutput
	}
	if !animated {
		frames = 1
		totalPixels = canvasPixels
	}
	return RasterInfo{MediaType: "image/webp", Width: width, Height: height, Frames: frames, Pixels: totalPixels}, nil
}

func inspectTIFF(source []byte) (RasterInfo, error) {
	var order binary.ByteOrder
	switch string(source[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return RasterInfo{}, ErrInvalidToolOutput
	}
	offset := int(order.Uint32(source[4:8]))
	frames := 0
	totalPixels := int64(0)
	firstWidth, firstHeight := 0, 0
	seen := make(map[int]bool)
	for offset != 0 {
		if offset < 8 || offset+2 > len(source) || seen[offset] || frames >= 8 {
			return RasterInfo{}, ErrInvalidToolOutput
		}
		seen[offset] = true
		entries := int(order.Uint16(source[offset : offset+2]))
		if entries <= 0 || entries > 4096 || offset+2+entries*12+4 > len(source) {
			return RasterInfo{}, ErrInvalidToolOutput
		}
		width, height := 0, 0
		for entryIndex := 0; entryIndex < entries; entryIndex++ {
			entry := source[offset+2+entryIndex*12 : offset+2+(entryIndex+1)*12]
			tag := order.Uint16(entry[:2])
			if tag != 256 && tag != 257 {
				continue
			}
			value, ok := tiffScalar(entry, order)
			if !ok || value == 0 || value > math.MaxInt32 {
				return RasterInfo{}, ErrInvalidToolOutput
			}
			if tag == 256 {
				width = int(value)
			} else {
				height = int(value)
			}
		}
		pixels, ok := rasterPixels(width, height)
		if !ok {
			return RasterInfo{}, ErrInvalidToolOutput
		}
		if frames == 0 {
			firstWidth, firstHeight = width, height
		}
		totalPixels += pixels
		frames++
		next := offset + 2 + entries*12
		offset = int(order.Uint32(source[next : next+4]))
	}
	if frames == 0 {
		return RasterInfo{}, ErrInvalidToolOutput
	}
	return RasterInfo{MediaType: "image/tiff", Width: firstWidth, Height: firstHeight, Frames: frames, Pixels: totalPixels}, nil
}

func tiffScalar(entry []byte, order binary.ByteOrder) (uint32, bool) {
	if len(entry) != 12 || order.Uint32(entry[4:8]) != 1 {
		return 0, false
	}
	switch order.Uint16(entry[2:4]) {
	case 3:
		return uint32(order.Uint16(entry[8:10])), true
	case 4:
		return order.Uint32(entry[8:12]), true
	default:
		return 0, false
	}
}

func inspectBMP(source []byte) (RasterInfo, error) {
	if len(source) < 54 || int(binary.LittleEndian.Uint32(source[2:6])) != len(source) ||
		binary.LittleEndian.Uint32(source[14:18]) < 40 || binary.LittleEndian.Uint16(source[26:28]) != 1 ||
		binary.LittleEndian.Uint32(source[30:34]) != 0 {
		return RasterInfo{}, ErrInvalidToolOutput
	}
	width := int(int32(binary.LittleEndian.Uint32(source[18:22])))
	heightValue := int32(binary.LittleEndian.Uint32(source[22:26]))
	height := int(heightValue)
	if height < 0 {
		height = -height
	}
	bits := int(binary.LittleEndian.Uint16(source[28:30]))
	pixelOffset := int(binary.LittleEndian.Uint32(source[10:14]))
	if width <= 0 || height <= 0 || (bits != 24 && bits != 32) || pixelOffset < 54 || pixelOffset > len(source) {
		return RasterInfo{}, ErrInvalidToolOutput
	}
	pixels, ok := rasterPixels(width, height)
	if !ok {
		return RasterInfo{}, ErrInvalidToolOutput
	}
	rowBytes := ((int64(width)*int64(bits) + 31) / 32) * 4
	if rowBytes <= 0 || int64(height) > math.MaxInt64/rowBytes ||
		rowBytes*int64(height) > int64(len(source)-pixelOffset) {
		return RasterInfo{}, ErrInvalidToolOutput
	}
	return RasterInfo{MediaType: "image/bmp", Width: width, Height: height, Frames: 1, Pixels: pixels}, nil
}

func rasterPixels(width, height int) (int64, bool) {
	if width <= 0 || height <= 0 || int64(width) > math.MaxInt64/int64(height) {
		return 0, false
	}
	return int64(width) * int64(height), true
}

func littleEndian24(value []byte) int {
	return int(value[0]) | int(value[1])<<8 | int(value[2])<<16
}

type OCRPlan struct {
	ExecutableID ExecutableID
	ArgProfile   ToolArgProfile
	Language     string
}

func PlanOCR(mediaType, language string) (OCRPlan, error) {
	profile, ok := capabilityspec.Lookup(capabilityspec.CapabilityImageOCR, capabilityspec.ProfileTesseractTextV1, false)
	if !ok || profile.ValidateMedia(mediaType, mediaType) != nil || (language != "eng" && language != "chi_sim" && language != "eng+chi_sim") {
		return OCRPlan{}, capabilityspec.ErrUnsupportedMedia
	}
	return OCRPlan{ExecutableID: ExecutableTesseract, ArgProfile: ArgsTesseractOCR, Language: language}, nil
}
