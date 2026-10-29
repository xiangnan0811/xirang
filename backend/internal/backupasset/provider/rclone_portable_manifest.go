package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
)

// Portable V2 persists the two independently observed metadata sets while the
// observation digests remain the original V1 canonical projections.
type rclonePortableEntryMetadataV2 struct {
	ModTime  string            `json:"mod_time"`
	SHA256   string            `json:"sha256,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type rclonePortableManifestEntryV2 struct {
	Version             int                           `json:"version"`
	Path                string                        `json:"path"`
	PhysicalPath        string                        `json:"physical_path"`
	Kind                string                        `json:"kind"`
	Size                uint64                        `json:"size"`
	SymlinkTargetBase64 string                        `json:"symlink_target_base64,omitempty"`
	SymlinkTargetDigest string                        `json:"symlink_target_digest,omitempty"`
	Source              rclonePortableEntryMetadataV2 `json:"source"`
	Destination         rclonePortableEntryMetadataV2 `json:"destination"`
}

type RcloneManifestFidelityV2 struct {
	Version                      int                            `json:"version"`
	HashFidelity                 backupasset.RcloneHashFidelity `json:"hash_fidelity"`
	RequiresFullByteVerification bool                           `json:"requires_full_byte_verification"`
	EmptyDirectoriesPreserved    bool                           `json:"empty_directories_preserved"`
	SymlinkTargetsPreserved      bool                           `json:"symlink_targets_preserved"`
	HardlinkTopologyPreserved    bool                           `json:"hardlink_topology_preserved"`
	SourceMetadataFields         []string                       `json:"source_metadata_fields"`
	DestinationMetadataFields    []string                       `json:"destination_metadata_fields"`
	PreservedMetadataFields      []string                       `json:"preserved_metadata_fields"`
	ModTimePreserved             bool                           `json:"mod_time_preserved"`
}

type rclonePortableContentProofV2 struct {
	Version                      int                            `json:"version"`
	HashFidelity                 backupasset.RcloneHashFidelity `json:"hash_fidelity"`
	SourceObservationDigest      string                         `json:"source_observation_digest"`
	DestinationObservationDigest string                         `json:"destination_observation_digest"`
	VerifiedBytes                uint64                         `json:"verified_bytes"`
	Complete                     bool                           `json:"complete"`
}

// RclonePortableByteVerification is returned only after the Portable download
// check succeeds. Native continues to use its distinct RcloneFullByteProof.
type RclonePortableByteVerification struct {
	VerifiedBytes uint64
	Complete      bool
}

type RclonePortableManifestV2 struct {
	Version                      int
	Chunks                       []RcloneManifestChunk
	IndexEncoded                 []byte `json:"-"`
	IndexDigest                  string
	EntryCount                   uint64
	LogicalBytes                 uint64
	SourceObservationDigest      string
	DestinationObservationDigest string
	Fidelity                     RcloneManifestFidelityV2
	ContentProof                 rclonePortableContentProofV2
}

type rclonePortableManifestIndexV2 struct {
	Version                      int                          `json:"version"`
	Generator                    string                       `json:"generator"`
	GeneratorVersion             string                       `json:"generator_version"`
	EntryCount                   uint64                       `json:"entry_count"`
	LogicalBytes                 uint64                       `json:"logical_bytes"`
	Chunks                       []rcloneCatalogChunkV1       `json:"chunks"`
	SourceObservationDigest      string                       `json:"source_observation_digest"`
	DestinationObservationDigest string                       `json:"destination_observation_digest"`
	Fidelity                     RcloneManifestFidelityV2     `json:"fidelity"`
	ContentProof                 rclonePortableContentProofV2 `json:"content_proof"`
}

// rclonePortableLineCursor is shared by V1 input merging and V2 reading. A
// chunk must contain complete newline-terminated records; no tree-sized split
// or second map of the entries is created.
type rclonePortableLineCursor struct {
	payload        []byte
	offset         int
	maxRecordBytes int
}

func (cursor *rclonePortableLineCursor) next() ([]byte, bool, error) {
	if cursor.offset == len(cursor.payload) {
		return nil, false, nil
	}
	remaining := cursor.payload[cursor.offset:]
	end := bytes.IndexByte(remaining, '\n')
	if end < 0 || end == 0 {
		return nil, false, fmt.Errorf("%w: invalid manifest record framing", ErrRcloneManifestPathUnsafe)
	}
	if end+1 > cursor.maxRecordBytes {
		return nil, false, ErrRcloneManifestLimitExceeded
	}
	cursor.offset += end + 1
	return remaining[:end], true, nil
}

// The writer emits canonical JSON, so comparing the decoded value with its
// canonical encoding also rejects omitted required fields, null optionals,
// noncanonical ordering and ambiguous alternate representations.
func decodeRclonePortableCanonicalJSON(payload []byte, value any) error {
	if len(payload) == 0 || !utf8.Valid(payload) || rejectDuplicateJSONMembers(string(payload)) != nil {
		return fmt.Errorf("%w: invalid portable manifest JSON", ErrRcloneManifestPathUnsafe)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: invalid portable manifest JSON", ErrRcloneManifestPathUnsafe)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing portable manifest JSON", ErrRcloneManifestPathUnsafe)
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, payload) {
		return fmt.Errorf("%w: noncanonical portable manifest JSON", ErrRcloneManifestPathUnsafe)
	}
	return nil
}

func validateRclonePortableManifestOptions(options RcloneManifestBuildOptions) error {
	if options.Limits.Timeout <= 0 || options.Limits.MaxBytes <= 0 || options.Limits.MaxEntries <= 0 ||
		options.Limits.MaxRecordBytes <= 0 || options.Limits.MaxDepth <= 0 || options.ChunkMaxBytes <= 0 ||
		options.ChunkMaxEntries <= 0 || options.SpoolMaxBytes < 2 {
		return ErrRcloneManifestLimitExceeded
	}
	return nil
}

func rclonePortableWithinBudget(total int64, options RcloneManifestBuildOptions) bool {
	return total >= 0 && total <= options.Limits.MaxBytes && total <= options.SpoolMaxBytes
}

type rclonePortableV1Cursor struct {
	manifest     RcloneManifestBundle
	options      RcloneManifestBuildOptions
	chunk        int
	line         rclonePortableLineCursor
	chunkEntries uint64
	previousPath string
}

func newRclonePortableV1Cursor(ctx context.Context, manifest RcloneManifestBundle, options RcloneManifestBuildOptions) (*rclonePortableV1Cursor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if manifest.EntryCount > uint64(options.Limits.MaxEntries) || !rclonePortableWithinBudget(int64(len(manifest.IndexEncoded)), options) {
		return nil, ErrRcloneManifestLimitExceeded
	}
	if manifest.Version != 1 || !lowerHex(manifest.ObservationDigest, 64) || sha256Hex(manifest.IndexEncoded) != manifest.IndexDigest {
		return nil, ErrRcloneManifestObservationMismatch
	}
	var index rcloneCatalogManifestIndexV1
	if err := decodeRclonePortableCanonicalJSON(manifest.IndexEncoded, &index); err != nil || index.Version != 1 ||
		index.Generator != "xirang-rclone-manifest" || index.GeneratorVersion != "v1" || index.Chunks == nil ||
		index.EntryCount != manifest.EntryCount || index.LogicalBytes != manifest.LogicalBytes ||
		len(index.Chunks) != len(manifest.Chunks) || !reflect.DeepEqual(index.Fidelity, manifest.Fidelity) {
		return nil, ErrRcloneManifestObservationMismatch
	}
	total := int64(len(manifest.IndexEncoded))
	var entries uint64
	for ordinal, chunk := range manifest.Chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reference := index.Chunks[ordinal]
		if chunk.Ordinal != ordinal || reference.Ordinal != ordinal || chunk.Size <= 0 ||
			chunk.Size != int64(len(chunk.Encoded)) || chunk.Size != reference.Size ||
			chunk.Size > int64(options.ChunkMaxBytes) || chunk.EntryCount == 0 ||
			chunk.EntryCount > uint64(options.ChunkMaxEntries) || chunk.EntryCount != reference.EntryCount ||
			chunk.Digest != reference.Digest || sha256Hex(chunk.Encoded) != chunk.Digest {
			return nil, ErrRcloneManifestObservationMismatch
		}
		if chunk.Size > options.Limits.MaxBytes-total || chunk.Size > options.SpoolMaxBytes-total {
			return nil, ErrRcloneManifestLimitExceeded
		}
		total += chunk.Size
		var err error
		entries, err = checkedAddUint64(entries, chunk.EntryCount)
		if err != nil || entries > manifest.EntryCount {
			return nil, ErrRcloneManifestLimitExceeded
		}
	}
	if entries != manifest.EntryCount {
		return nil, ErrRcloneManifestObservationMismatch
	}
	return &rclonePortableV1Cursor{manifest: manifest, options: options}, nil
}

func (cursor *rclonePortableV1Cursor) next(ctx context.Context) (rcloneCanonicalManifestEntry, bool, error) {
	for cursor.chunk < len(cursor.manifest.Chunks) {
		if err := ctx.Err(); err != nil {
			return rcloneCanonicalManifestEntry{}, false, err
		}
		chunk := cursor.manifest.Chunks[cursor.chunk]
		if cursor.line.payload == nil {
			cursor.line = rclonePortableLineCursor{payload: chunk.Encoded, maxRecordBytes: cursor.options.Limits.MaxRecordBytes}
		}
		line, present, err := cursor.line.next()
		if err != nil {
			return rcloneCanonicalManifestEntry{}, false, err
		}
		if !present {
			if cursor.chunkEntries != chunk.EntryCount {
				return rcloneCanonicalManifestEntry{}, false, ErrRcloneManifestObservationMismatch
			}
			cursor.chunk++
			cursor.chunkEntries = 0
			cursor.line = rclonePortableLineCursor{}
			continue
		}
		cursor.chunkEntries++
		if cursor.chunkEntries > chunk.EntryCount {
			return rcloneCanonicalManifestEntry{}, false, ErrRcloneManifestObservationMismatch
		}
		var entry rcloneCanonicalManifestEntry
		if err := decodeRclonePortableCanonicalJSON(line, &entry); err != nil {
			return rcloneCanonicalManifestEntry{}, false, err
		}
		if cursor.previousPath != "" && cursor.previousPath >= entry.Path {
			return rcloneCanonicalManifestEntry{}, false, ErrRcloneManifestPathUnsafe
		}
		cursor.previousPath = entry.Path
		return entry, true, nil
	}
	return rcloneCanonicalManifestEntry{}, false, nil
}

func validateRclonePortableProjection(entry rcloneCanonicalManifestEntry, maxDepth int, destination, verifyTarget bool) error {
	if entry.Version != 1 || !validRcloneLogicalPath(entry.Path, maxDepth) ||
		!validRcloneLogicalPath(entry.PhysicalPath, maxDepth) || entry.Size > uint64(^uint64(0)>>1) {
		return ErrRcloneManifestPathUnsafe
	}
	parsed, err := time.Parse(time.RFC3339Nano, entry.ModTime)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != entry.ModTime {
		return ErrRcloneManifestPathUnsafe
	}
	if entry.SHA256 != "" && !lowerHex(entry.SHA256, 64) {
		return ErrRcloneManifestPathUnsafe
	}
	for key, value := range entry.Metadata {
		if value == "" || (key != "gid" && key != "mode" && key != "mtime" && key != "uid") {
			return ErrRcloneManifestPathUnsafe
		}
	}
	modeType, modeKnown, err := rcloneManifestModeType(entry.Metadata["mode"])
	if err != nil {
		return err
	}
	switch entry.Kind {
	case "directory":
		if entry.PhysicalPath != entry.Path || entry.Size != 0 || entry.SHA256 != "" ||
			entry.SymlinkTargetBase64 != "" || entry.SymlinkTargetDigest != "" || (modeKnown && modeType != 0o040000) {
			return ErrRcloneManifestPathUnsafe
		}
	case "file":
		if entry.PhysicalPath != entry.Path || strings.HasSuffix(entry.Path, ".rclonelink") ||
			entry.SymlinkTargetBase64 != "" || entry.SymlinkTargetDigest != "" || (modeKnown && modeType != 0o100000) {
			return ErrRcloneManifestPathUnsafe
		}
	case "symlink":
		if entry.PhysicalPath != entry.Path+".rclonelink" || !lowerHex(entry.SymlinkTargetDigest, 64) ||
			(!destination && (!modeKnown || modeType != 0o120000)) ||
			(destination && modeKnown && modeType != 0o120000 && modeType != 0o100000) {
			return ErrRcloneManifestPathUnsafe
		}
		if entry.SHA256 != "" && entry.SHA256 != entry.SymlinkTargetDigest {
			return ErrRcloneManifestObservationMismatch
		}
		if verifyTarget {
			target, err := base64.StdEncoding.Strict().DecodeString(entry.SymlinkTargetBase64)
			if err != nil || base64.StdEncoding.EncodeToString(target) != entry.SymlinkTargetBase64 ||
				uint64(len(target)) != entry.Size || sha256Hex(target) != entry.SymlinkTargetDigest {
				return ErrRcloneManifestObservationMismatch
			}
		}
	default:
		return ErrRcloneManifestPathUnsafe
	}
	return nil
}

func rclonePortableV2Entry(source, destination rcloneCanonicalManifestEntry) rclonePortableManifestEntryV2 {
	return rclonePortableManifestEntryV2{
		Version: 2, Path: source.Path, PhysicalPath: source.PhysicalPath, Kind: source.Kind, Size: source.Size,
		SymlinkTargetBase64: source.SymlinkTargetBase64, SymlinkTargetDigest: source.SymlinkTargetDigest,
		Source:      rclonePortableEntryMetadataV2{ModTime: source.ModTime, SHA256: source.SHA256, Metadata: source.Metadata},
		Destination: rclonePortableEntryMetadataV2{ModTime: destination.ModTime, SHA256: destination.SHA256, Metadata: destination.Metadata},
	}
}

func rclonePortableV2Projection(entry rclonePortableManifestEntryV2, source bool) rcloneCanonicalManifestEntry {
	metadata := entry.Destination
	if source {
		metadata = entry.Source
	}
	return rcloneCanonicalManifestEntry{
		Version: 1, Path: entry.Path, PhysicalPath: entry.PhysicalPath, Kind: entry.Kind, Size: entry.Size,
		ModTime: metadata.ModTime, SHA256: metadata.SHA256, SymlinkTargetBase64: entry.SymlinkTargetBase64,
		SymlinkTargetDigest: entry.SymlinkTargetDigest, Metadata: metadata.Metadata,
	}
}

var rclonePortableMetadataFields = [...]string{"gid", "mode", "mtime", "uid"}

type rclonePortableManifestAccumulator struct {
	options         RcloneManifestBuildOptions
	sourceHash      hash.Hash
	destinationHash hash.Hash
	entryCount      uint64
	logicalBytes    uint64
	contentEntries  uint64
	allStrong       bool
	hasDirectory    bool
	hasSymlink      bool
	allTimesEqual   bool
	sourceFields    uint8
	destFields      uint8
	preservedFields uint8
	previousPath    string
	directories     []string
}

func newRclonePortableManifestAccumulator(options RcloneManifestBuildOptions) *rclonePortableManifestAccumulator {
	return &rclonePortableManifestAccumulator{
		options: options, sourceHash: sha256.New(), destinationHash: sha256.New(),
		allStrong: true, allTimesEqual: true, preservedFields: 0x0f,
	}
}

func (state *rclonePortableManifestAccumulator) add(source, destination rcloneCanonicalManifestEntry) error {
	if source.Path != destination.Path || source.PhysicalPath != destination.PhysicalPath || source.Kind != destination.Kind ||
		source.Size != destination.Size || source.SymlinkTargetBase64 != destination.SymlinkTargetBase64 ||
		source.SymlinkTargetDigest != destination.SymlinkTargetDigest ||
		(source.SHA256 != "" && destination.SHA256 != "" && source.SHA256 != destination.SHA256) {
		return ErrRcloneManifestObservationMismatch
	}
	if err := validateRclonePortableProjection(source, state.options.Limits.MaxDepth, false, true); err != nil {
		return err
	}
	if err := validateRclonePortableProjection(destination, state.options.Limits.MaxDepth, true, false); err != nil {
		return err
	}
	if state.entryCount >= uint64(state.options.Limits.MaxEntries) {
		return ErrRcloneManifestLimitExceeded
	}
	if err := trackRclonePortableManifestPath(&state.directories, &state.previousPath, source); err != nil {
		return err
	}
	state.hasDirectory = state.hasDirectory || source.Kind == "directory"
	state.entryCount++
	if source.Kind == "file" || source.Kind == "symlink" {
		var err error
		state.logicalBytes, err = checkedAddUint64(state.logicalBytes, source.Size)
		if err != nil {
			return ErrRcloneManifestLimitExceeded
		}
		state.contentEntries++
		state.allStrong = state.allStrong && source.SHA256 != "" && destination.SHA256 != ""
	}
	state.hasSymlink = state.hasSymlink || source.Kind == "symlink"
	state.allTimesEqual = state.allTimesEqual && source.ModTime == destination.ModTime
	for index, field := range rclonePortableMetadataFields {
		bit := uint8(1 << index)
		sourceValue, sourcePresent := source.Metadata[field]
		destValue, destPresent := destination.Metadata[field]
		if sourcePresent {
			state.sourceFields |= bit
		}
		if destPresent {
			state.destFields |= bit
		}
		if sourcePresent != destPresent || (sourcePresent && sourceValue != destValue) {
			state.preservedFields &^= bit
		}
	}
	for _, item := range []struct {
		entry rcloneCanonicalManifestEntry
		hash  hash.Hash
	}{{source, state.sourceHash}, {destination, state.destinationHash}} {
		encoded, err := json.Marshal(item.entry)
		if err != nil {
			return err
		}
		_, _ = item.hash.Write(encoded)
		_, _ = item.hash.Write([]byte{'\n'})
	}
	return nil
}

// Pending directories are kept only until their lexically ordered child range
// has passed. Each retained path belongs to an already budgeted record.
func trackRclonePortableManifestPath(directories *[]string, previous *string, entry rcloneCanonicalManifestEntry) error {
	if *previous != "" && *previous >= entry.Path {
		return ErrRcloneManifestPathUnsafe
	}
	parent := path.Dir(entry.Path)
	if parent == "." {
		parent = ""
	}
	for len(*directories) > 0 {
		last := len(*directories) - 1
		directory := (*directories)[last]
		if len(entry.Path) > len(directory) && strings.HasPrefix(entry.Path, directory) && entry.Path[len(directory)] <= '/' {
			break
		}
		(*directories)[last] = ""
		*directories = (*directories)[:last]
	}
	if parent != "" {
		position := sort.SearchStrings(*directories, parent)
		if position == len(*directories) || (*directories)[position] != parent {
			return ErrRcloneManifestPathUnsafe
		}
	}
	if entry.Kind == "directory" {
		*directories = append(*directories, entry.Path)
	}
	*previous = entry.Path
	return nil
}

func rclonePortableMetadataFieldSet(mask uint8) []string {
	fields := make([]string, 0, len(rclonePortableMetadataFields))
	for index, field := range rclonePortableMetadataFields {
		if mask&(1<<index) != 0 {
			fields = append(fields, field)
		}
	}
	return fields
}

func (state *rclonePortableManifestAccumulator) finish() (string, string, RcloneManifestFidelityV2) {
	frame := fmt.Sprintf("entries:%d\nbytes:%d\n", state.entryCount, state.logicalBytes)
	_, _ = io.WriteString(state.sourceHash, frame)
	_, _ = io.WriteString(state.destinationHash, frame)
	fidelity := RcloneManifestFidelityV2{
		Version: 2, EmptyDirectoriesPreserved: state.hasDirectory, SymlinkTargetsPreserved: state.hasSymlink,
		HardlinkTopologyPreserved: false, SourceMetadataFields: rclonePortableMetadataFieldSet(state.sourceFields),
		DestinationMetadataFields: rclonePortableMetadataFieldSet(state.destFields),
		PreservedMetadataFields:   rclonePortableMetadataFieldSet(state.preservedFields & state.sourceFields),
		ModTimePreserved:          state.entryCount > 0 && state.allTimesEqual,
	}
	switch {
	case state.contentEntries == 0:
		fidelity.HashFidelity = backupasset.RcloneHashNotEvaluated
	case state.allStrong:
		fidelity.HashFidelity = backupasset.RcloneHashProviderStrongChecksum
	default:
		fidelity.HashFidelity = backupasset.RcloneHashDownloadVerifiedBytes
		fidelity.RequiresFullByteVerification = true
	}
	return hex.EncodeToString(state.sourceHash.Sum(nil)), hex.EncodeToString(state.destinationHash.Sum(nil)), fidelity
}

// buildRclonePortableManifestV2 performs a sorted lockstep merge of the two V1
// observations. The index is deliberately not finalized until any required
// download proof has succeeded.
func buildRclonePortableManifestV2(ctx context.Context, source, destination RcloneManifestBundle, options RcloneManifestBuildOptions) (RclonePortableManifestV2, error) {
	if err := validateRclonePortableManifestOptions(options); err != nil {
		return RclonePortableManifestV2{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, options.Limits.Timeout)
	defer cancel()
	sourceCursor, err := newRclonePortableV1Cursor(ctx, source, options)
	if err != nil {
		return RclonePortableManifestV2{}, err
	}
	destinationCursor, err := newRclonePortableV1Cursor(ctx, destination, options)
	if err != nil {
		return RclonePortableManifestV2{}, err
	}
	state := newRclonePortableManifestAccumulator(options)
	result := RclonePortableManifestV2{Version: 2, Chunks: make([]RcloneManifestChunk, 0)}
	var current bytes.Buffer
	var chunkEntries uint64
	var total int64
	flush := func() {
		if chunkEntries == 0 {
			return
		}
		encoded := current.Bytes()
		result.Chunks = append(result.Chunks, RcloneManifestChunk{
			Ordinal: len(result.Chunks), Encoded: encoded, Digest: sha256Hex(encoded),
			Size: int64(len(encoded)), EntryCount: chunkEntries,
		})
		current = bytes.Buffer{}
		chunkEntries = 0
	}
	for {
		sourceEntry, sourcePresent, err := sourceCursor.next(ctx)
		if err != nil {
			return RclonePortableManifestV2{}, err
		}
		destinationEntry, destinationPresent, err := destinationCursor.next(ctx)
		if err != nil {
			return RclonePortableManifestV2{}, err
		}
		if sourcePresent != destinationPresent {
			return RclonePortableManifestV2{}, ErrRcloneManifestObservationMismatch
		}
		if !sourcePresent {
			break
		}
		if err := state.add(sourceEntry, destinationEntry); err != nil {
			return RclonePortableManifestV2{}, err
		}
		encoded, err := json.Marshal(rclonePortableV2Entry(sourceEntry, destinationEntry))
		if err != nil {
			return RclonePortableManifestV2{}, err
		}
		if len(encoded)+1 > options.Limits.MaxRecordBytes || len(encoded)+1 > options.ChunkMaxBytes ||
			int64(len(encoded)+1) > options.Limits.MaxBytes-total || int64(len(encoded)+1) > options.SpoolMaxBytes-total {
			return RclonePortableManifestV2{}, ErrRcloneManifestLimitExceeded
		}
		if chunkEntries > 0 && (current.Len()+len(encoded)+1 > options.ChunkMaxBytes || chunkEntries >= uint64(options.ChunkMaxEntries)) {
			flush()
		}
		_, _ = current.Write(encoded)
		_ = current.WriteByte('\n')
		chunkEntries++
		total += int64(len(encoded) + 1)
	}
	flush()
	result.EntryCount, result.LogicalBytes = state.entryCount, state.logicalBytes
	result.SourceObservationDigest, result.DestinationObservationDigest, result.Fidelity = state.finish()
	if result.EntryCount != source.EntryCount || result.EntryCount != destination.EntryCount ||
		result.LogicalBytes != source.LogicalBytes || result.LogicalBytes != destination.LogicalBytes ||
		result.SourceObservationDigest != source.ObservationDigest || result.DestinationObservationDigest != destination.ObservationDigest {
		return RclonePortableManifestV2{}, ErrRcloneManifestObservationMismatch
	}
	return result, nil
}

// completeRclonePortableManifestV2 binds the successful download proof, if one
// was required, and only then produces the persistent index bytes.
func completeRclonePortableManifestV2(manifest RclonePortableManifestV2, verification RclonePortableByteVerification, options RcloneManifestBuildOptions) (RclonePortableManifestV2, error) {
	if err := validateRclonePortableManifestOptions(options); err != nil {
		return RclonePortableManifestV2{}, err
	}
	if manifest.Version != 2 || manifest.IndexEncoded != nil || manifest.IndexDigest != "" ||
		manifest.Fidelity.Version != 2 || manifest.Fidelity.HardlinkTopologyPreserved ||
		manifest.Fidelity.SourceMetadataFields == nil || manifest.Fidelity.DestinationMetadataFields == nil ||
		manifest.Fidelity.PreservedMetadataFields == nil ||
		!lowerHex(manifest.SourceObservationDigest, 64) || !lowerHex(manifest.DestinationObservationDigest, 64) {
		return RclonePortableManifestV2{}, ErrRcloneManifestObservationMismatch
	}
	if manifest.EntryCount > uint64(options.Limits.MaxEntries) {
		return RclonePortableManifestV2{}, ErrRcloneManifestLimitExceeded
	}
	proof := rclonePortableContentProofV2{
		Version: 2, HashFidelity: manifest.Fidelity.HashFidelity,
		SourceObservationDigest: manifest.SourceObservationDigest, DestinationObservationDigest: manifest.DestinationObservationDigest,
		Complete: true,
	}
	if manifest.Fidelity.RequiresFullByteVerification {
		if manifest.Fidelity.HashFidelity != backupasset.RcloneHashDownloadVerifiedBytes || !verification.Complete ||
			verification.VerifiedBytes != manifest.LogicalBytes {
			return RclonePortableManifestV2{}, ErrRcloneManifestFullByteProofRequired
		}
		proof.VerifiedBytes = verification.VerifiedBytes
	} else if verification.VerifiedBytes != 0 ||
		(manifest.Fidelity.HashFidelity != backupasset.RcloneHashProviderStrongChecksum && manifest.Fidelity.HashFidelity != backupasset.RcloneHashNotEvaluated) {
		return RclonePortableManifestV2{}, ErrRcloneManifestObservationMismatch
	}
	index := rclonePortableManifestIndexV2{
		Version: 2, Generator: "xirang-rclone-manifest", GeneratorVersion: "v2", EntryCount: manifest.EntryCount,
		LogicalBytes: manifest.LogicalBytes, Chunks: make([]rcloneCatalogChunkV1, 0, len(manifest.Chunks)),
		SourceObservationDigest: manifest.SourceObservationDigest, DestinationObservationDigest: manifest.DestinationObservationDigest,
		Fidelity: manifest.Fidelity, ContentProof: proof,
	}
	var total int64
	var count uint64
	for ordinal, chunk := range manifest.Chunks {
		if chunk.Ordinal != ordinal || chunk.Size <= 0 || chunk.Size != int64(len(chunk.Encoded)) ||
			chunk.Size > int64(options.ChunkMaxBytes) || chunk.EntryCount == 0 ||
			chunk.EntryCount > uint64(options.ChunkMaxEntries) || chunk.Digest != sha256Hex(chunk.Encoded) ||
			chunk.Size > options.Limits.MaxBytes-total || chunk.Size > options.SpoolMaxBytes-total {
			return RclonePortableManifestV2{}, ErrRcloneManifestLimitExceeded
		}
		total += chunk.Size
		var err error
		count, err = checkedAddUint64(count, chunk.EntryCount)
		if err != nil || count > manifest.EntryCount {
			return RclonePortableManifestV2{}, ErrRcloneManifestObservationMismatch
		}
		index.Chunks = append(index.Chunks, rcloneCatalogChunkV1{
			Ordinal: ordinal, Digest: chunk.Digest, Size: chunk.Size, EntryCount: chunk.EntryCount,
		})
	}
	if count != manifest.EntryCount {
		return RclonePortableManifestV2{}, ErrRcloneManifestObservationMismatch
	}
	encoded, err := json.Marshal(index)
	if err != nil {
		return RclonePortableManifestV2{}, err
	}
	if int64(len(encoded)) > options.Limits.MaxBytes-total || int64(len(encoded)) > options.SpoolMaxBytes-total {
		return RclonePortableManifestV2{}, ErrRcloneManifestLimitExceeded
	}
	manifest.ContentProof = proof
	manifest.IndexEncoded = encoded
	manifest.IndexDigest = sha256Hex(encoded)
	return manifest, nil
}

// readRclonePortableManifestV2 validates the authenticated index/chunks and
// reduces both projections and fidelity again. visit sees one pair at a time;
// callers must discard its results if this function subsequently returns an
// error. The function does not retain the chunk payloads.
func readRclonePortableManifestV2(
	ctx context.Context,
	indexPayload []byte,
	readChunk func(context.Context, int, int64) ([]byte, error),
	options RcloneManifestBuildOptions,
	expected RcloneCommitV1,
	visit func(rcloneCanonicalManifestEntry, rcloneCanonicalManifestEntry) error,
) (rclonePortableManifestIndexV2, error) {
	if err := validateRclonePortableManifestOptions(options); err != nil {
		return rclonePortableManifestIndexV2{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, options.Limits.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return rclonePortableManifestIndexV2{}, err
	}
	if !rclonePortableWithinBudget(int64(len(indexPayload)), options) {
		return rclonePortableManifestIndexV2{}, ErrRcloneManifestLimitExceeded
	}
	if readChunk == nil || sha256Hex(indexPayload) != expected.ManifestIndexDigest || expected.Portable == nil {
		return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
	}
	var index rclonePortableManifestIndexV2
	if err := decodeRclonePortableCanonicalJSON(indexPayload, &index); err != nil {
		return rclonePortableManifestIndexV2{}, err
	}
	if index.Version != 2 || index.Generator != "xirang-rclone-manifest" || index.GeneratorVersion != "v2" ||
		index.Chunks == nil || index.EntryCount > uint64(options.Limits.MaxEntries) ||
		len(index.Chunks) != len(expected.ManifestChunkDigests) || index.EntryCount != expected.ManifestEntryCount ||
		index.LogicalBytes != expected.LogicalBytes || index.SourceObservationDigest != expected.SourceObservationDigest ||
		index.DestinationObservationDigest != expected.DestinationObservationDigest ||
		!lowerHex(index.SourceObservationDigest, 64) || !lowerHex(index.DestinationObservationDigest, 64) {
		return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
	}
	fidelityJSON, err := json.Marshal(index.Fidelity)
	if err != nil || sha256Hex(fidelityJSON) != expected.FidelityEvidenceDigest {
		return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
	}
	proofJSON, err := json.Marshal(index.ContentProof)
	if err != nil {
		return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
	}
	proofDigest := sha256Hex(proofJSON)
	if proofDigest != expected.ContentProofDigest || proofDigest != expected.Portable.HashEvidenceDigest {
		return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
	}
	state := newRclonePortableManifestAccumulator(options)
	total := int64(len(indexPayload))
	var totalEntries uint64
	for ordinal, reference := range index.Chunks {
		if err := ctx.Err(); err != nil {
			return rclonePortableManifestIndexV2{}, err
		}
		if reference.Ordinal != ordinal || reference.Size <= 0 || reference.Size > int64(options.ChunkMaxBytes) ||
			reference.EntryCount == 0 || reference.EntryCount > uint64(options.ChunkMaxEntries) ||
			reference.Digest != expected.ManifestChunkDigests[ordinal] || !lowerHex(reference.Digest, 64) ||
			reference.Size > options.Limits.MaxBytes-total || reference.Size > options.SpoolMaxBytes-total {
			return rclonePortableManifestIndexV2{}, ErrRcloneManifestLimitExceeded
		}
		total += reference.Size
		totalEntries, err = checkedAddUint64(totalEntries, reference.EntryCount)
		if err != nil || totalEntries > index.EntryCount {
			return rclonePortableManifestIndexV2{}, ErrRcloneManifestLimitExceeded
		}
		payload, err := readChunk(ctx, ordinal, reference.Size)
		if err != nil {
			return rclonePortableManifestIndexV2{}, err
		}
		if int64(len(payload)) != reference.Size || sha256Hex(payload) != reference.Digest {
			return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
		}
		cursor := rclonePortableLineCursor{payload: payload, maxRecordBytes: options.Limits.MaxRecordBytes}
		var chunkEntries uint64
		for {
			if err := ctx.Err(); err != nil {
				return rclonePortableManifestIndexV2{}, err
			}
			line, present, err := cursor.next()
			if err != nil {
				return rclonePortableManifestIndexV2{}, err
			}
			if !present {
				break
			}
			chunkEntries++
			if chunkEntries > reference.EntryCount {
				return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
			}
			var entry rclonePortableManifestEntryV2
			if err := decodeRclonePortableCanonicalJSON(line, &entry); err != nil || entry.Version != 2 {
				return rclonePortableManifestIndexV2{}, ErrRcloneManifestPathUnsafe
			}
			source := rclonePortableV2Projection(entry, true)
			destination := rclonePortableV2Projection(entry, false)
			if err := state.add(source, destination); err != nil {
				return rclonePortableManifestIndexV2{}, err
			}
			if visit != nil {
				if err := visit(source, destination); err != nil {
					return rclonePortableManifestIndexV2{}, err
				}
			}
		}
		if chunkEntries != reference.EntryCount {
			return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
		}
	}
	if totalEntries != index.EntryCount || state.entryCount != index.EntryCount || state.logicalBytes != index.LogicalBytes {
		return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
	}
	sourceDigest, destinationDigest, fidelity := state.finish()
	if sourceDigest != index.SourceObservationDigest || destinationDigest != index.DestinationObservationDigest ||
		!reflect.DeepEqual(fidelity, index.Fidelity) {
		return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
	}
	proof := index.ContentProof
	if proof.Version != 2 || !proof.Complete || proof.HashFidelity != fidelity.HashFidelity ||
		proof.SourceObservationDigest != sourceDigest || proof.DestinationObservationDigest != destinationDigest ||
		(fidelity.RequiresFullByteVerification && proof.VerifiedBytes != index.LogicalBytes) ||
		(!fidelity.RequiresFullByteVerification && proof.VerifiedBytes != 0) ||
		expected.Portable.DownloadVerifiedBytes != proof.VerifiedBytes {
		return rclonePortableManifestIndexV2{}, ErrRcloneManifestObservationMismatch
	}
	return index, nil
}
