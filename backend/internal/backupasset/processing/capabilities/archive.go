package capabilities

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"xirang/backend/internal/backupasset/processing/capabilityspec"
)

type ArchiveLimits struct {
	MaxEntries          int
	MaxDepth            int
	MaxExpandedBytes    int64
	MaxCompressionRatio int64
	MaxMemberBytes      int64
}

type ArchiveEntry struct {
	ID          string
	ParentID    string
	DisplayName string
	Size        int64
	Ordinal     int
	MediaType   string
}

type ArchiveIndex struct {
	Entries       []ArchiveEntry
	ExpandedBytes int64
	Complete      bool
}

func InspectArchive(input []byte, mediaType string, limits ArchiveLimits) (ArchiveIndex, error) {
	if !validArchiveLimits(limits) || int64(len(input)) > 2<<30 {
		return ArchiveIndex{}, ErrInputLimit
	}
	switch mediaType {
	case "application/zip":
		if !zipMagic(input) {
			return ArchiveIndex{}, ErrInvalidToolOutput
		}
		return inspectZIP(input, limits)
	case "application/x-tar":
		return inspectTAR(input, limits)
	default:
		return ArchiveIndex{}, capabilityspec.ErrUnsupportedMedia
	}
}

func ExtractArchiveEntry(input []byte, mediaType, memberID string, limits ArchiveLimits) ([]byte, string, error) {
	if len(memberID) != 32 {
		return nil, "", ErrArchiveMember
	}
	index, err := InspectArchive(input, mediaType, limits)
	if err != nil {
		return nil, "", err
	}
	ordinal := -1
	for _, entry := range index.Entries {
		if entry.ID == memberID {
			ordinal = entry.Ordinal
			break
		}
	}
	if ordinal < 0 {
		return nil, "", ErrArchiveMember
	}
	switch mediaType {
	case "application/zip":
		reader, err := zip.NewReader(bytes.NewReader(input), int64(len(input)))
		if err != nil || ordinal >= len(reader.File) {
			return nil, "", ErrInvalidToolOutput
		}
		part, err := reader.File[ordinal].Open()
		if err != nil {
			return nil, "", ErrInvalidToolOutput
		}
		content, readErr := io.ReadAll(io.LimitReader(part, limits.MaxMemberBytes+1))
		closeErr := part.Close()
		if readErr != nil || closeErr != nil || int64(len(content)) > limits.MaxMemberBytes {
			return nil, "", ErrInputLimit
		}
		return content, http.DetectContentType(content), nil
	case "application/x-tar":
		content, mediaType, err := ExtractTARStream(bytes.NewReader(input), 0, memberID, limits)
		return content, mediaType, err
	default:
		return nil, "", capabilityspec.ErrUnsupportedMedia
	}
}

func inspectZIP(input []byte, limits ArchiveLimits) (ArchiveIndex, error) {
	if !canonicalZIPEnd(input) {
		return ArchiveIndex{}, ErrInvalidToolOutput
	}
	if len(input) >= 8 && bytes.Equal(input[:4], []byte{'P', 'K', 3, 4}) && binary.LittleEndian.Uint16(input[6:8])&1 != 0 {
		return ArchiveIndex{}, ErrArchiveEncrypted
	}
	reader, err := zip.NewReader(bytes.NewReader(input), int64(len(input)))
	if err != nil || len(reader.File) > limits.MaxEntries {
		return ArchiveIndex{}, ErrInvalidToolOutput
	}
	result := ArchiveIndex{Entries: make([]ArchiveEntry, 0, len(reader.File)), Complete: true}
	seen := make(map[string]bool, len(reader.File))
	for ordinal, file := range reader.File {
		if file.Flags&1 != 0 {
			return ArchiveIndex{}, ErrArchiveEncrypted
		}
		clean, err := validateArchivePath(file.Name, limits.MaxDepth)
		mode := file.FileInfo().Mode()
		collisionKey := strings.ToLower(clean)
		if err != nil || seen[collisionKey] || mode.IsDir() || !mode.IsRegular() ||
			mode&(os.ModeSymlink|os.ModeDevice|os.ModeNamedPipe|os.ModeSocket) != 0 {
			return ArchiveIndex{}, ErrInvalidToolOutput
		}
		seen[collisionKey] = true
		size := int64(file.UncompressedSize64)
		compressed := int64(file.CompressedSize64)
		if size < 0 || size > limits.MaxMemberBytes || size > limits.MaxExpandedBytes-result.ExpandedBytes ||
			(compressed == 0 && size > 0) || exceedsCompressionRatio(size, compressed, limits.MaxCompressionRatio) {
			return ArchiveIndex{}, ErrInputLimit
		}
		part, err := file.Open()
		if err != nil {
			return ArchiveIndex{}, ErrInvalidToolOutput
		}
		read, readErr := io.Copy(io.Discard, io.LimitReader(part, size+1))
		closeErr := part.Close()
		if readErr != nil || closeErr != nil || read != size {
			return ArchiveIndex{}, ErrInvalidToolOutput
		}
		result.ExpandedBytes += size
		result.Entries = append(result.Entries, archiveEntry(clean, size, ordinal, file.CRC32))
	}
	return result, nil
}

func inspectTAR(input []byte, limits ArchiveLimits) (ArchiveIndex, error) {
	return InspectTARStream(bytes.NewReader(input), 0, limits)
}

func InspectTARStream(input io.Reader, compressedBytes int64, limits ArchiveLimits) (ArchiveIndex, error) {
	index, _, _, err := processTARStream(input, compressedBytes, "", limits)
	return index, err
}

func ExtractTARStream(
	input io.Reader,
	compressedBytes int64,
	memberID string,
	limits ArchiveLimits,
) ([]byte, string, error) {
	if len(memberID) != 32 {
		return nil, "", ErrArchiveMember
	}
	_, content, mediaType, err := processTARStream(input, compressedBytes, memberID, limits)
	return content, mediaType, err
}

func processTARStream(
	input io.Reader,
	compressedBytes int64,
	memberID string,
	limits ArchiveLimits,
) (ArchiveIndex, []byte, string, error) {
	if input == nil || !validArchiveLimits(limits) || compressedBytes < 0 {
		return ArchiveIndex{}, nil, "", ErrInputLimit
	}
	maximumStreamBytes := limits.MaxExpandedBytes + int64(limits.MaxEntries)*1024 + 1024
	tracked := &archiveStreamReader{source: input, maximum: maximumStreamBytes}
	reader := tar.NewReader(tracked)
	result := ArchiveIndex{Entries: make([]ArchiveEntry, 0), Complete: true}
	seen := make(map[string]bool)
	expectedRawBytes := int64(0)
	var (
		content   []byte
		mediaType string
		found     bool
	)
	for ordinal := 0; ; ordinal++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			if tracked.total != expectedRawBytes+1024 {
				return ArchiveIndex{}, nil, "", ErrInvalidToolOutput
			}
			var trailing [1]byte
			count, trailingErr := tracked.Read(trailing[:])
			if count != 0 || !errors.Is(trailingErr, io.EOF) {
				return ArchiveIndex{}, nil, "", ErrInvalidToolOutput
			}
			if memberID != "" && !found {
				return ArchiveIndex{}, nil, "", ErrArchiveMember
			}
			return result, content, mediaType, nil
		}
		if err != nil {
			if errors.Is(err, ErrInputLimit) {
				return ArchiveIndex{}, nil, "", ErrInputLimit
			}
			return ArchiveIndex{}, nil, "", ErrInvalidToolOutput
		}
		if ordinal >= limits.MaxEntries || !isRegularTARType(header.Typeflag) || header.Linkname != "" ||
			header.Format != tar.FormatUSTAR || len(header.PAXRecords) != 0 {
			return ArchiveIndex{}, nil, "", ErrInvalidToolOutput
		}
		clean, pathErr := validateArchivePath(header.Name, limits.MaxDepth)
		collisionKey := strings.ToLower(clean)
		if pathErr != nil || seen[collisionKey] || header.Size < 0 || header.Size > limits.MaxMemberBytes ||
			header.Size > limits.MaxExpandedBytes-result.ExpandedBytes {
			return ArchiveIndex{}, nil, "", ErrInputLimit
		}
		seen[collisionKey] = true
		result.ExpandedBytes += header.Size
		if compressedBytes > 0 && exceedsCompressionRatio(result.ExpandedBytes, compressedBytes, limits.MaxCompressionRatio) {
			return ArchiveIndex{}, nil, "", ErrInputLimit
		}
		entry := archiveEntry(clean, header.Size, ordinal, uint32(header.Size))
		result.Entries = append(result.Entries, entry)
		expectedRawBytes += 512 + paddedTARSize(header.Size)
		if memberID == entry.ID {
			payload, readErr := io.ReadAll(io.LimitReader(reader, header.Size+1))
			if readErr != nil || int64(len(payload)) != header.Size {
				return ArchiveIndex{}, nil, "", ErrInvalidToolOutput
			}
			content = payload
			mediaType = http.DetectContentType(payload)
			found = true
			continue
		}
		read, readErr := io.CopyN(io.Discard, reader, header.Size)
		if readErr != nil || read != header.Size {
			return ArchiveIndex{}, nil, "", ErrInvalidToolOutput
		}
	}
}

type archiveStreamReader struct {
	source  io.Reader
	maximum int64
	total   int64
}

func (reader *archiveStreamReader) Read(payload []byte) (int, error) {
	if reader.total > reader.maximum {
		return 0, ErrInputLimit
	}
	remaining := reader.maximum - reader.total
	limit := int64(len(payload))
	if limit > remaining+1 {
		limit = remaining + 1
	}
	count, err := reader.source.Read(payload[:limit])
	reader.total += int64(count)
	if reader.total > reader.maximum {
		return count, ErrInputLimit
	}
	return count, err
}

func paddedTARSize(size int64) int64 {
	return (size + 511) &^ 511
}

func exceedsCompressionRatio(expanded, compressed, ratio int64) bool {
	if expanded <= 0 {
		return false
	}
	if compressed <= 0 || ratio <= 0 {
		return true
	}
	return expanded/compressed > ratio || expanded%compressed != 0 && expanded/compressed == ratio
}

func zipMagic(input []byte) bool {
	return len(input) >= 4 && bytes.Equal(input[:2], []byte{'P', 'K'}) &&
		(bytes.Equal(input[2:4], []byte{3, 4}) || bytes.Equal(input[2:4], []byte{5, 6}))
}

func canonicalZIPEnd(input []byte) bool {
	if len(input) < 22 {
		return false
	}
	start := len(input) - 22 - 65535
	if start < 0 {
		start = 0
	}
	for offset := len(input) - 22; offset >= start; offset-- {
		if !bytes.Equal(input[offset:offset+4], []byte{'P', 'K', 5, 6}) {
			continue
		}
		commentBytes := int(binary.LittleEndian.Uint16(input[offset+20 : offset+22]))
		if offset+22+commentBytes != len(input) || binary.LittleEndian.Uint16(input[offset+4:offset+6]) != 0 ||
			binary.LittleEndian.Uint16(input[offset+6:offset+8]) != 0 {
			continue
		}
		return true
	}
	return false
}

func isRegularTARType(typeflag byte) bool {
	return typeflag == tar.TypeReg || typeflag == 0
}

func archiveEntry(clean string, size int64, ordinal int, checksum uint32) ArchiveEntry {
	digest := sha256.Sum256([]byte(fmt.Sprintf("xirang.archive.member.v1\x00%d\x00%d", ordinal, checksum)))
	return ArchiveEntry{ID: hex.EncodeToString(digest[:16]), DisplayName: path.Base(clean), Size: size, Ordinal: ordinal}
}

func validateArchivePath(value string, maxDepth int) (string, error) {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n\\") || strings.HasPrefix(value, "/") {
		return "", ErrInvalidToolOutput
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value || len(strings.Split(clean, "/")) > maxDepth {
		return "", ErrInvalidToolOutput
	}
	for _, character := range clean {
		if character < 0x20 || character == 0x7f {
			return "", ErrInvalidToolOutput
		}
	}
	return clean, nil
}

func validArchiveLimits(value ArchiveLimits) bool {
	return value.MaxEntries > 0 && value.MaxEntries <= 100_000 && value.MaxDepth > 0 && value.MaxDepth <= 16 &&
		value.MaxExpandedBytes > 0 && value.MaxExpandedBytes <= 8<<30 && value.MaxCompressionRatio > 0 && value.MaxCompressionRatio <= 100 &&
		value.MaxMemberBytes > 0 && value.MaxMemberBytes <= 256<<20
}
