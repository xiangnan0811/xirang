package provider

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"

	"golang.org/x/text/unicode/norm"
)

var (
	ErrRcloneManifestLimitExceeded         = errors.New("rclone manifest limit exceeded")
	ErrRcloneManifestPathUnsafe            = errors.New("rclone manifest path unsafe")
	ErrRcloneManifestSpecialFile           = errors.New("rclone manifest special file")
	ErrRcloneManifestFullByteProofRequired = errors.New("rclone manifest full-byte proof required")
	ErrRcloneSourceDrift                   = errors.New("rclone source drift")
	ErrRcloneDestinationUnstable           = errors.New("rclone destination unstable")
	ErrRcloneManifestObservationMismatch   = errors.New("rclone manifest observation mismatch")
)

type RcloneSymlinkTargetReader func(context.Context, string, int64) ([]byte, error)

type RcloneManifestBuildOptions struct {
	Limits              ManifestLimits
	ChunkMaxBytes       int
	ChunkMaxEntries     int
	SpoolMaxBytes       int64
	SymlinkTargetReader RcloneSymlinkTargetReader
}

type RcloneManifestFidelityV1 struct {
	Version                      int                            `json:"version"`
	HashFidelity                 backupasset.RcloneHashFidelity `json:"hash_fidelity"`
	RequiresFullByteVerification bool                           `json:"requires_full_byte_verification"`
	EmptyDirectoriesPreserved    bool                           `json:"empty_directories_preserved"`
	SymlinkTargetsPreserved      bool                           `json:"symlink_targets_preserved"`
	HardlinkTopologyPreserved    bool                           `json:"hardlink_topology_preserved"`
	MetadataFields               []string                       `json:"metadata_fields"`
}

type RcloneManifestChunk struct {
	Ordinal    int
	Encoded    []byte `json:"-"`
	Digest     string
	Size       int64
	EntryCount uint64
}

type RcloneManifestBundle struct {
	Version           int
	Chunks            []RcloneManifestChunk
	IndexEncoded      []byte `json:"-"`
	IndexDigest       string
	EntryCount        uint64
	LogicalBytes      uint64
	ObservationDigest string
	Fidelity          RcloneManifestFidelityV1
}

type rcloneLSJSONEntry struct {
	Path     string            `json:"Path"`
	Name     string            `json:"Name"`
	Size     int64             `json:"Size"`
	MimeType string            `json:"MimeType,omitempty"`
	ModTime  string            `json:"ModTime"`
	IsDir    bool              `json:"IsDir"`
	Hashes   map[string]string `json:"Hashes,omitempty"`
	Metadata map[string]string `json:"Metadata,omitempty"`
}

type rcloneCanonicalManifestEntry struct {
	Version             int               `json:"version"`
	Path                string            `json:"path"`
	PhysicalPath        string            `json:"physical_path"`
	Kind                string            `json:"kind"`
	Size                uint64            `json:"size"`
	ModTime             string            `json:"mod_time"`
	SHA256              string            `json:"sha256,omitempty"`
	SymlinkTargetBase64 string            `json:"symlink_target_base64,omitempty"`
	SymlinkTargetDigest string            `json:"symlink_target_digest,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
	strongHash          bool              `json:"-"`
}

func BuildRcloneManifestV1(ctx context.Context, reader io.Reader, options RcloneManifestBuildOptions) (RcloneManifestBundle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reader == nil || options.Limits.Timeout <= 0 || options.Limits.MaxBytes <= 0 || options.Limits.MaxEntries <= 0 ||
		options.Limits.MaxRecordBytes <= 0 || options.Limits.MaxDepth <= 0 || options.ChunkMaxBytes <= 0 ||
		options.ChunkMaxEntries <= 0 || options.SpoolMaxBytes < 2 {
		return RcloneManifestBundle{}, fmt.Errorf("%w: invalid manifest limits", ErrRcloneManifestLimitExceeded)
	}
	boundedContext, cancel := context.WithTimeout(ctx, options.Limits.Timeout)
	defer cancel()
	spool := newRcloneManifestSpool(options.SpoolMaxBytes)
	defer spool.cleanup() //nolint:errcheck // explicit cleanup below reports the primary cleanup failure
	stream := newRcloneJSONArrayStream(reader, options.SpoolMaxBytes, options.Limits.MaxRecordBytes)
	state := rcloneManifestBuildState{options: options, spool: spool, allStrong: true}
	for {
		if err := boundedContext.Err(); err != nil {
			return RcloneManifestBundle{}, err
		}
		raw, present, err := stream.Next()
		if err != nil {
			if errors.Is(err, ErrRcloneManifestLimitExceeded) {
				return RcloneManifestBundle{}, err
			}
			return RcloneManifestBundle{}, fmt.Errorf("read Rclone manifest input: %w", err)
		}
		if !present {
			break
		}
		if !utf8.Valid(raw) || rejectDuplicateJSONMembers(string(raw)) != nil {
			return RcloneManifestBundle{}, fmt.Errorf("%w: ambiguous lsjson", ErrRcloneManifestPathUnsafe)
		}
		strict := json.NewDecoder(bytes.NewReader(raw))
		strict.DisallowUnknownFields()
		var source rcloneLSJSONEntry
		if err := strict.Decode(&source); err != nil {
			return RcloneManifestBundle{}, fmt.Errorf("%w: unknown lsjson field", ErrRcloneManifestPathUnsafe)
		}
		if _, err := strict.Token(); !errors.Is(err, io.EOF) {
			return RcloneManifestBundle{}, fmt.Errorf("%w: trailing lsjson record data", ErrRcloneManifestPathUnsafe)
		}
		entry, err := canonicalRcloneManifestEntry(boundedContext, source, options)
		if err != nil {
			return RcloneManifestBundle{}, err
		}
		if state.entryCount >= uint64(options.Limits.MaxEntries) {
			return RcloneManifestBundle{}, ErrRcloneManifestLimitExceeded
		}
		if err := state.Add(entry); err != nil {
			return RcloneManifestBundle{}, err
		}
	}
	if err := state.Flush(); err != nil {
		return RcloneManifestBundle{}, err
	}
	run, err := mergeRcloneManifestRuns(boundedContext, spool, state.runs, options.Limits.MaxRecordBytes)
	if err != nil {
		return RcloneManifestBundle{}, err
	}
	fidelity := RcloneManifestFidelityV1{
		Version: 1, EmptyDirectoriesPreserved: state.hasDirectory, SymlinkTargetsPreserved: state.hasSymlink,
		HardlinkTopologyPreserved: false, MetadataFields: []string{"mode", "uid", "gid", "mtime"},
	}
	switch {
	case state.contentEntries == 0:
		fidelity.HashFidelity = backupasset.RcloneHashNotEvaluated
	case state.allStrong:
		fidelity.HashFidelity = backupasset.RcloneHashProviderStrongChecksum
	default:
		fidelity.HashFidelity = backupasset.RcloneHashNotEvaluated
		fidelity.RequiresFullByteVerification = true
	}
	manifest, err := encodeRcloneManifestRun(boundedContext, run, state.entryCount, state.logicalBytes, fidelity, options)
	if err != nil {
		return RcloneManifestBundle{}, err
	}
	if err := spool.cleanup(); err != nil {
		return RcloneManifestBundle{}, fmt.Errorf("cleanup Rclone manifest spool: %w", err)
	}
	return manifest, nil
}

type rcloneJSONArrayStream struct {
	reader         *bufio.Reader
	maxBytes       int64
	maxRecordBytes int
	bytesRead      int64
	started        bool
	afterRecord    bool
	finished       bool
}

func newRcloneJSONArrayStream(reader io.Reader, maxBytes int64, maxRecordBytes int) *rcloneJSONArrayStream {
	return &rcloneJSONArrayStream{reader: bufio.NewReaderSize(reader, 4096), maxBytes: maxBytes, maxRecordBytes: maxRecordBytes}
}

func (stream *rcloneJSONArrayStream) Next() ([]byte, bool, error) {
	if stream.finished {
		return nil, false, nil
	}
	if !stream.started {
		opening, err := stream.nextNonWhitespace()
		if err != nil {
			return nil, false, err
		}
		if opening != '[' {
			return nil, false, fmt.Errorf("%w: lsjson must be an array", ErrRcloneManifestPathUnsafe)
		}
		stream.started = true
	}
	first, err := stream.nextNonWhitespace()
	if err != nil {
		return nil, false, err
	}
	if stream.afterRecord {
		if first == ']' {
			return stream.finish()
		}
		if first != ',' {
			return nil, false, fmt.Errorf("%w: missing lsjson record separator", ErrRcloneManifestPathUnsafe)
		}
		first, err = stream.nextNonWhitespace()
		if err != nil {
			return nil, false, err
		}
		if first == ']' {
			return nil, false, fmt.Errorf("%w: trailing lsjson comma", ErrRcloneManifestPathUnsafe)
		}
	} else if first == ']' {
		return stream.finish()
	}
	if first != '{' {
		return nil, false, fmt.Errorf("%w: lsjson record must be an object", ErrRcloneManifestPathUnsafe)
	}
	record := []byte{'{'}
	stack := []byte{'{'}
	inString := false
	escaped := false
	for len(stack) > 0 {
		value, err := stream.readByte()
		if err != nil {
			return nil, false, err
		}
		record = append(record, value)
		if len(record) > stream.maxRecordBytes {
			return nil, false, ErrRcloneManifestLimitExceeded
		}
		if inString {
			switch {
			case escaped:
				escaped = false
			case value == '\\':
				escaped = true
			case value == '"':
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, value)
		case '}', ']':
			opening := stack[len(stack)-1]
			if (opening == '{' && value != '}') || (opening == '[' && value != ']') {
				return nil, false, fmt.Errorf("%w: mismatched lsjson delimiter", ErrRcloneManifestPathUnsafe)
			}
			stack = stack[:len(stack)-1]
		}
	}
	stream.afterRecord = true
	return record, true, nil
}

func (stream *rcloneJSONArrayStream) finish() ([]byte, bool, error) {
	for {
		value, err := stream.readByte()
		if errors.Is(err, io.EOF) {
			stream.finished = true
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if !rcloneJSONWhitespace(value) {
			return nil, false, fmt.Errorf("%w: trailing lsjson data", ErrRcloneManifestPathUnsafe)
		}
	}
}

func (stream *rcloneJSONArrayStream) nextNonWhitespace() (byte, error) {
	for {
		value, err := stream.readByte()
		if err != nil {
			return 0, err
		}
		if !rcloneJSONWhitespace(value) {
			return value, nil
		}
	}
}

func (stream *rcloneJSONArrayStream) readByte() (byte, error) {
	value, err := stream.reader.ReadByte()
	if err != nil {
		return 0, err
	}
	stream.bytesRead++
	if stream.bytesRead > stream.maxBytes {
		return 0, ErrRcloneManifestLimitExceeded
	}
	return value, nil
}

func rcloneJSONWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

type rcloneManifestSortRecord struct {
	path    string
	encoded []byte
}

type rcloneManifestRun struct {
	path string
	size int64
}

type rcloneManifestSpool struct {
	maxBytes int64
	used     int64
	runs     map[string]int64
}

func newRcloneManifestSpool(maxBytes int64) *rcloneManifestSpool {
	return &rcloneManifestSpool{maxBytes: maxBytes, runs: make(map[string]int64)}
}

func (spool *rcloneManifestSpool) create() (*os.File, *rcloneManifestRun, error) {
	file, err := os.CreateTemp("", "xirang-rclone-manifest-*")
	if err != nil {
		return nil, nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return nil, nil, err
	}
	run := &rcloneManifestRun{path: file.Name()}
	spool.runs[run.path] = 0
	return file, run, nil
}

func (spool *rcloneManifestSpool) reserve(run *rcloneManifestRun, count int) error {
	if count < 0 || spool.used+int64(count) > spool.maxBytes {
		return ErrRcloneManifestLimitExceeded
	}
	spool.used += int64(count)
	run.size += int64(count)
	spool.runs[run.path] = run.size
	return nil
}

func (spool *rcloneManifestSpool) remove(run rcloneManifestRun) error {
	size, tracked := spool.runs[run.path]
	if !tracked {
		return nil
	}
	if err := os.Remove(run.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	delete(spool.runs, run.path)
	spool.used -= size
	return nil
}

func (spool *rcloneManifestSpool) cleanup() error {
	var first error
	for runPath, size := range spool.runs {
		if err := os.Remove(runPath); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
		delete(spool.runs, runPath)
		spool.used -= size
	}
	return first
}

type rcloneManifestBuildState struct {
	options        RcloneManifestBuildOptions
	spool          *rcloneManifestSpool
	records        []rcloneManifestSortRecord
	recordBytes    int64
	canonicalBytes int64
	runs           []rcloneManifestRun
	entryCount     uint64
	logicalBytes   uint64
	contentEntries uint64
	allStrong      bool
	hasDirectory   bool
	hasSymlink     bool
}

func (state *rcloneManifestBuildState) Add(entry rcloneCanonicalManifestEntry) error {
	strongHash := entry.strongHash
	entry.strongHash = false
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode Rclone manifest entry: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > state.options.Limits.MaxRecordBytes || len(encoded) > state.options.ChunkMaxBytes {
		return ErrRcloneManifestLimitExceeded
	}
	if len(state.records) > 0 && (state.recordBytes+int64(len(encoded)) > int64(state.options.ChunkMaxBytes) ||
		len(state.records) >= state.options.ChunkMaxEntries) {
		if err := state.Flush(); err != nil {
			return err
		}
	}
	state.canonicalBytes += int64(len(encoded))
	if state.canonicalBytes > state.options.Limits.MaxBytes || state.canonicalBytes > state.options.SpoolMaxBytes/2 {
		return ErrRcloneManifestLimitExceeded
	}
	state.records = append(state.records, rcloneManifestSortRecord{path: entry.Path, encoded: encoded})
	state.recordBytes += int64(len(encoded))
	state.entryCount++
	switch entry.Kind {
	case "file", "symlink":
		state.contentEntries++
		state.logicalBytes, err = checkedAddUint64(state.logicalBytes, entry.Size)
		if err != nil {
			return ErrRcloneManifestLimitExceeded
		}
		state.allStrong = state.allStrong && strongHash
	case "directory":
		state.hasDirectory = true
	}
	state.hasSymlink = state.hasSymlink || entry.Kind == "symlink"
	if state.recordBytes >= int64(state.options.ChunkMaxBytes) || len(state.records) >= state.options.ChunkMaxEntries {
		return state.Flush()
	}
	return nil
}

func (state *rcloneManifestBuildState) Flush() error {
	if len(state.records) == 0 {
		return nil
	}
	sort.Slice(state.records, func(left, right int) bool { return state.records[left].path < state.records[right].path })
	for index := 1; index < len(state.records); index++ {
		if state.records[index-1].path == state.records[index].path {
			return ErrRcloneManifestPathUnsafe
		}
	}
	run, err := writeRcloneManifestRun(state.spool, state.records)
	if err != nil {
		return err
	}
	state.runs = append(state.runs, run)
	state.records = nil
	state.recordBytes = 0
	return nil
}

func writeRcloneManifestRun(spool *rcloneManifestSpool, records []rcloneManifestSortRecord) (rcloneManifestRun, error) {
	file, run, err := spool.create()
	if err != nil {
		return rcloneManifestRun{}, err
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = spool.remove(*run)
		}
	}()
	writer := bufio.NewWriterSize(file, 64<<10)
	for _, record := range records {
		if err := spool.reserve(run, len(record.encoded)); err != nil {
			return rcloneManifestRun{}, err
		}
		if _, err := writer.Write(record.encoded); err != nil {
			return rcloneManifestRun{}, err
		}
	}
	if err := writer.Flush(); err != nil {
		return rcloneManifestRun{}, err
	}
	if err := file.Close(); err != nil {
		return rcloneManifestRun{}, err
	}
	success = true
	return *run, nil
}

func canonicalRcloneManifestEntry(ctx context.Context, source rcloneLSJSONEntry, options RcloneManifestBuildOptions) (rcloneCanonicalManifestEntry, error) {
	if len(strings.Split(source.Path, "/")) > options.Limits.MaxDepth {
		return rcloneCanonicalManifestEntry{}, ErrRcloneManifestLimitExceeded
	}
	if source.Name != path.Base(source.Path) || !validRcloneLogicalPath(source.Path, options.Limits.MaxDepth) || source.Size < 0 {
		return rcloneCanonicalManifestEntry{}, ErrRcloneManifestPathUnsafe
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, source.ModTime)
	if err != nil {
		return rcloneCanonicalManifestEntry{}, ErrRcloneManifestPathUnsafe
	}
	entry := rcloneCanonicalManifestEntry{
		Version: 1, Path: source.Path, PhysicalPath: source.Path, ModTime: parsedTime.UTC().Format(time.RFC3339Nano),
		Metadata: canonicalRcloneMetadata(source.Metadata),
	}
	modeType, modeKnown, err := rcloneManifestModeType(source.Metadata["mode"])
	if err != nil {
		return rcloneCanonicalManifestEntry{}, err
	}
	switch {
	case source.IsDir:
		if modeKnown && modeType != 0o040000 {
			return rcloneCanonicalManifestEntry{}, ErrRcloneManifestSpecialFile
		}
		entry.Kind = "directory"
	case modeKnown && modeType == 0o120000:
		if !strings.HasSuffix(source.Path, ".rclonelink") || options.SymlinkTargetReader == nil {
			return rcloneCanonicalManifestEntry{}, ErrRcloneManifestPathUnsafe
		}
		entry.Path = strings.TrimSuffix(source.Path, ".rclonelink")
		if !validRcloneLogicalPath(entry.Path, options.Limits.MaxDepth) {
			return rcloneCanonicalManifestEntry{}, ErrRcloneManifestPathUnsafe
		}
		target, err := options.SymlinkTargetReader(ctx, source.Path, source.Size)
		if err != nil || int64(len(target)) != source.Size {
			return rcloneCanonicalManifestEntry{}, ErrRcloneManifestPathUnsafe
		}
		entry.Kind = "symlink"
		entry.Size = uint64(len(target))
		entry.SymlinkTargetBase64 = base64.StdEncoding.EncodeToString(target)
		entry.SymlinkTargetDigest = sha256Hex(target)
		entry.SHA256 = normalizedRcloneSHA256(source.Hashes)
		entry.strongHash = entry.SHA256 == entry.SymlinkTargetDigest
	case modeKnown && modeType != 0o100000:
		return rcloneCanonicalManifestEntry{}, ErrRcloneManifestSpecialFile
	default:
		if strings.HasSuffix(source.Path, ".rclonelink") {
			return rcloneCanonicalManifestEntry{}, ErrRcloneManifestPathUnsafe
		}
		entry.Kind = "file"
		entry.Size = uint64(source.Size)
		entry.SHA256 = normalizedRcloneSHA256(source.Hashes)
		entry.strongHash = entry.SHA256 != ""
	}
	return entry, nil
}

func validRcloneLogicalPath(value string, maxDepth int) bool {
	if value == "" || strings.HasPrefix(value, "/") || path.Clean(value) != value || norm.NFC.String(value) != value || !utf8.ValidString(value) {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) > maxDepth {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, character := range part {
			if character == '\x00' || unicode.IsControl(character) || (character >= '\uF000' && character <= '\uF0FF') {
				return false
			}
		}
	}
	return true
}

func rcloneManifestModeType(value string) (uint64, bool, error) {
	if value == "" {
		return 0, false, nil
	}
	mode, err := strconv.ParseUint(value, 8, 32)
	if err != nil {
		return 0, false, ErrRcloneManifestSpecialFile
	}
	return mode & 0o170000, true, nil
}

func canonicalRcloneMetadata(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string)
	for _, key := range []string{"gid", "mode", "mtime", "uid"} {
		if value := source[key]; value != "" {
			result[key] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func normalizedRcloneSHA256(hashes map[string]string) string {
	if len(hashes) == 0 {
		return ""
	}
	value, ok := hashes["sha256"]
	if !ok || !lowerHex(value, 64) {
		return ""
	}
	return value
}

const rcloneManifestMergeFanIn = 16

type rcloneManifestRunCursor struct {
	file    *os.File
	scanner *bufio.Scanner
	path    string
	encoded []byte
}

func openRcloneManifestRunCursor(run rcloneManifestRun, maxRecordBytes int) (*rcloneManifestRunCursor, error) {
	file, err := os.Open(run.path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != run.size {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: invalid manifest spool file", ErrRcloneManifestLimitExceeded)
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, min(maxRecordBytes, 64<<10)), maxRecordBytes+1)
	return &rcloneManifestRunCursor{file: file, scanner: scanner}, nil
}

func (cursor *rcloneManifestRunCursor) advance() (bool, error) {
	if !cursor.scanner.Scan() {
		if err := cursor.scanner.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	line := cursor.scanner.Bytes()
	if len(line) == 0 {
		return false, fmt.Errorf("%w: empty manifest spool record", ErrRcloneManifestPathUnsafe)
	}
	var entry struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(line, &entry); err != nil || entry.Path == "" {
		return false, fmt.Errorf("%w: invalid manifest spool record", ErrRcloneManifestPathUnsafe)
	}
	cursor.path = entry.Path
	cursor.encoded = append(append(cursor.encoded[:0], line...), '\n')
	return true, nil
}

type rcloneManifestMergeHeap []*rcloneManifestRunCursor

func (items rcloneManifestMergeHeap) Len() int { return len(items) }
func (items rcloneManifestMergeHeap) Less(left, right int) bool {
	return items[left].path < items[right].path
}
func (items rcloneManifestMergeHeap) Swap(left, right int) {
	items[left], items[right] = items[right], items[left]
}
func (items *rcloneManifestMergeHeap) Push(value any) {
	*items = append(*items, value.(*rcloneManifestRunCursor))
}
func (items *rcloneManifestMergeHeap) Pop() any {
	values := *items
	last := len(values) - 1
	value := values[last]
	values[last] = nil
	*items = values[:last]
	return value
}

func mergeRcloneManifestRuns(ctx context.Context, spool *rcloneManifestSpool, runs []rcloneManifestRun, maxRecordBytes int) (*rcloneManifestRun, error) {
	if len(runs) == 0 {
		return nil, nil
	}
	current := append([]rcloneManifestRun(nil), runs...)
	for len(current) > 1 {
		next := make([]rcloneManifestRun, 0, (len(current)+rcloneManifestMergeFanIn-1)/rcloneManifestMergeFanIn)
		for start := 0; start < len(current); start += rcloneManifestMergeFanIn {
			end := min(start+rcloneManifestMergeFanIn, len(current))
			if end-start == 1 {
				next = append(next, current[start])
				continue
			}
			merged, err := mergeRcloneManifestRunBatch(ctx, spool, current[start:end], maxRecordBytes)
			if err != nil {
				return nil, err
			}
			next = append(next, merged)
		}
		current = next
	}
	return &current[0], nil
}

func mergeRcloneManifestRunBatch(ctx context.Context, spool *rcloneManifestSpool, runs []rcloneManifestRun, maxRecordBytes int) (rcloneManifestRun, error) {
	file, output, err := spool.create()
	if err != nil {
		return rcloneManifestRun{}, err
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = spool.remove(*output)
		}
	}()
	cursors := make([]*rcloneManifestRunCursor, 0, len(runs))
	closeCursors := func() error {
		var first error
		for _, cursor := range cursors {
			if err := cursor.file.Close(); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	items := make(rcloneManifestMergeHeap, 0, len(runs))
	for _, run := range runs {
		cursor, err := openRcloneManifestRunCursor(run, maxRecordBytes)
		if err != nil {
			_ = closeCursors()
			return rcloneManifestRun{}, err
		}
		cursors = append(cursors, cursor)
		present, err := cursor.advance()
		if err != nil || !present {
			_ = closeCursors()
			if err != nil {
				return rcloneManifestRun{}, err
			}
			return rcloneManifestRun{}, fmt.Errorf("%w: empty manifest spool run", ErrRcloneManifestPathUnsafe)
		}
		items = append(items, cursor)
	}
	heap.Init(&items)
	writer := bufio.NewWriterSize(file, 64<<10)
	lastPath := ""
	for items.Len() > 0 {
		if err := ctx.Err(); err != nil {
			_ = closeCursors()
			return rcloneManifestRun{}, err
		}
		cursor := heap.Pop(&items).(*rcloneManifestRunCursor)
		if cursor.path == lastPath {
			_ = closeCursors()
			return rcloneManifestRun{}, ErrRcloneManifestPathUnsafe
		}
		lastPath = cursor.path
		if err := spool.reserve(output, len(cursor.encoded)); err != nil {
			_ = closeCursors()
			return rcloneManifestRun{}, err
		}
		if _, err := writer.Write(cursor.encoded); err != nil {
			_ = closeCursors()
			return rcloneManifestRun{}, err
		}
		present, err := cursor.advance()
		if err != nil {
			_ = closeCursors()
			return rcloneManifestRun{}, err
		}
		if present {
			heap.Push(&items, cursor)
		}
	}
	if err := writer.Flush(); err != nil {
		_ = closeCursors()
		return rcloneManifestRun{}, err
	}
	if err := file.Close(); err != nil {
		_ = closeCursors()
		return rcloneManifestRun{}, err
	}
	if err := closeCursors(); err != nil {
		return rcloneManifestRun{}, err
	}
	for _, run := range runs {
		if err := spool.remove(run); err != nil {
			return rcloneManifestRun{}, err
		}
	}
	success = true
	return *output, nil
}

func encodeRcloneManifestRun(ctx context.Context, run *rcloneManifestRun, entryCount, logicalBytes uint64, fidelity RcloneManifestFidelityV1, options RcloneManifestBuildOptions) (RcloneManifestBundle, error) {
	chunks := make([]RcloneManifestChunk, 0)
	current := bytes.NewBuffer(nil)
	currentEntries := uint64(0)
	manifestBytes := int64(0)
	flush := func() {
		if currentEntries == 0 {
			return
		}
		encoded := append([]byte(nil), current.Bytes()...)
		chunks = append(chunks, RcloneManifestChunk{
			Ordinal: len(chunks), Encoded: encoded, Digest: sha256Hex(encoded), Size: int64(len(encoded)), EntryCount: currentEntries,
		})
		current.Reset()
		currentEntries = 0
	}
	observationHash := sha256.New()
	observedEntries := uint64(0)
	if run != nil {
		cursor, err := openRcloneManifestRunCursor(*run, options.Limits.MaxRecordBytes)
		if err != nil {
			return RcloneManifestBundle{}, err
		}
		defer cursor.file.Close() //nolint:errcheck // explicit read errors are returned below
		for {
			if err := ctx.Err(); err != nil {
				return RcloneManifestBundle{}, err
			}
			present, err := cursor.advance()
			if err != nil {
				return RcloneManifestBundle{}, err
			}
			if !present {
				break
			}
			encoded := cursor.encoded
			if len(encoded) > options.Limits.MaxRecordBytes || len(encoded) > options.ChunkMaxBytes {
				return RcloneManifestBundle{}, ErrRcloneManifestLimitExceeded
			}
			if (current.Len()+len(encoded) > options.ChunkMaxBytes || int(currentEntries) >= options.ChunkMaxEntries) && currentEntries > 0 {
				flush()
			}
			_, _ = current.Write(encoded)
			_, _ = observationHash.Write(encoded)
			currentEntries++
			observedEntries++
			manifestBytes += int64(len(encoded))
			if manifestBytes > options.Limits.MaxBytes || manifestBytes > options.SpoolMaxBytes/2 {
				return RcloneManifestBundle{}, ErrRcloneManifestLimitExceeded
			}
		}
	}
	flush()
	if observedEntries != entryCount {
		return RcloneManifestBundle{}, ErrRcloneManifestObservationMismatch
	}

	type chunkIndex struct {
		Ordinal    int    `json:"ordinal"`
		Digest     string `json:"digest"`
		Size       int64  `json:"size"`
		EntryCount uint64 `json:"entry_count"`
	}
	indexChunks := make([]chunkIndex, len(chunks))
	for index, chunk := range chunks {
		indexChunks[index] = chunkIndex{Ordinal: chunk.Ordinal, Digest: chunk.Digest, Size: chunk.Size, EntryCount: chunk.EntryCount}
	}
	index := struct {
		Version          int                      `json:"version"`
		Generator        string                   `json:"generator"`
		GeneratorVersion string                   `json:"generator_version"`
		EntryCount       uint64                   `json:"entry_count"`
		LogicalBytes     uint64                   `json:"logical_bytes"`
		Chunks           []chunkIndex             `json:"chunks"`
		Fidelity         RcloneManifestFidelityV1 `json:"fidelity"`
	}{
		Version: 1, Generator: "xirang-rclone-manifest", GeneratorVersion: "v1", EntryCount: entryCount,
		LogicalBytes: logicalBytes, Chunks: indexChunks, Fidelity: fidelity,
	}
	indexEncoded, err := json.Marshal(index)
	if err != nil {
		return RcloneManifestBundle{}, fmt.Errorf("encode Rclone manifest index: %w", err)
	}
	if manifestBytes+int64(len(indexEncoded)) > options.Limits.MaxBytes {
		return RcloneManifestBundle{}, ErrRcloneManifestLimitExceeded
	}
	observationFrame := fmt.Sprintf("entries:%d\nbytes:%d\n", entryCount, logicalBytes)
	_, _ = observationHash.Write([]byte(observationFrame))
	return RcloneManifestBundle{
		Version: 1, Chunks: chunks, IndexEncoded: indexEncoded, IndexDigest: sha256Hex(indexEncoded),
		EntryCount: entryCount, LogicalBytes: logicalBytes,
		ObservationDigest: hex.EncodeToString(observationHash.Sum(nil)), Fidelity: fidelity,
	}, nil
}

type RcloneFullByteProof struct {
	SourceDigest      string
	DestinationDigest string
	VerifiedBytes     uint64
	Complete          bool
}

func ResolveRcloneManifestHashFidelity(manifest RcloneManifestBundle, proof RcloneFullByteProof) (backupasset.RcloneHashFidelity, error) {
	if manifest.Fidelity.HashFidelity == backupasset.RcloneHashProviderStrongChecksum && !manifest.Fidelity.RequiresFullByteVerification {
		return backupasset.RcloneHashProviderStrongChecksum, nil
	}
	if manifest.EntryCount == 0 && manifest.LogicalBytes == 0 {
		return backupasset.RcloneHashNotEvaluated, nil
	}
	if !manifest.Fidelity.RequiresFullByteVerification || !proof.Complete || !lowerHex(proof.SourceDigest, 64) ||
		proof.SourceDigest != proof.DestinationDigest || proof.VerifiedBytes != manifest.LogicalBytes {
		return "", ErrRcloneManifestFullByteProofRequired
	}
	return backupasset.RcloneHashDownloadVerifiedBytes, nil
}

type RcloneObservationV1 struct {
	Digest       string
	EntryCount   uint64
	LogicalBytes uint64
}

func (value RcloneObservationV1) valid() bool { return lowerHex(value.Digest, 64) }

func ValidateRclonePortableObservations(sourceBefore, sourceAfter, destinationFirst, destinationSecond RcloneObservationV1) error {
	if !sourceBefore.valid() || !sourceAfter.valid() || !destinationFirst.valid() || !destinationSecond.valid() {
		return ErrRcloneManifestObservationMismatch
	}
	if sourceBefore != sourceAfter {
		return ErrRcloneSourceDrift
	}
	if destinationFirst != destinationSecond {
		return ErrRcloneDestinationUnstable
	}
	if sourceAfter != destinationFirst {
		return ErrRcloneManifestObservationMismatch
	}
	return nil
}

func checkedAddUint64(left, right uint64) (uint64, error) {
	result := left + right
	if result < left {
		return 0, ErrRcloneManifestLimitExceeded
	}
	return result, nil
}
