package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"xirang/backend/internal/backupasset"
)

// readRclonePortablePublishedManifest is the single version-dispatched reader
// used after the marker and the expected attempt have been authenticated.
// The visitor is provisional until the entire manifest has been verified.
func readRclonePortablePublishedManifest(
	ctx context.Context,
	remote RclonePortableRemote,
	request RclonePortablePublicationRequest,
	commit RcloneCommitV1,
	visit func(rcloneCanonicalManifestEntry, rcloneCanonicalManifestEntry) error,
) (int, error) {
	if remote == nil || request.ControlPayloadMaxBytes <= 0 || request.Attempt.PublicationMode != backupasset.PublicationVersionedPrefix ||
		ValidateRcloneRuntimeCompatibility(request.Attempt, commit) != nil {
		return 0, ErrRcloneManifestObservationMismatch
	}
	if err := validateRclonePortableManifestOptions(request.ManifestOptions); err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, request.ManifestOptions.Limits.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	maxIndex := min(request.ControlPayloadMaxBytes, request.ManifestOptions.Limits.MaxBytes, request.ManifestOptions.SpoolMaxBytes)
	indexPayload, err := remote.ReadControl(ctx, request, "manifest-index.json", maxIndex)
	if err != nil {
		return 0, err
	}
	if !rclonePortableWithinBudget(int64(len(indexPayload)), request.ManifestOptions) ||
		int64(len(indexPayload)) > request.ControlPayloadMaxBytes {
		return 0, ErrRcloneManifestLimitExceeded
	}
	if sha256Hex(indexPayload) != commit.ManifestIndexDigest {
		return 0, ErrRcloneManifestObservationMismatch
	}
	readChunk := func(ctx context.Context, ordinal int, size int64) ([]byte, error) {
		if size <= 0 || size > request.ControlPayloadMaxBytes {
			return nil, ErrRcloneManifestLimitExceeded
		}
		return remote.ReadControl(ctx, request, fmt.Sprintf("manifest-%06d.jsonl", ordinal), size)
	}
	switch request.Attempt.ManifestSchemaRevision {
	case 1:
		if err := readRclonePortableManifestV1(ctx, indexPayload, readChunk, request.ManifestOptions, commit, visit); err != nil {
			return 0, err
		}
		return 1, nil
	case 2:
		if _, err := readRclonePortableManifestV2(ctx, indexPayload, readChunk, request.ManifestOptions, commit, visit); err != nil {
			return 0, err
		}
		return 2, nil
	default:
		return 0, ErrRcloneManifestObservationMismatch
	}
}

// readRclonePortableManifestV1 retains the historical V1 entry projection and
// observation framing, while enforcing the same bounded line/chunk traversal.
// It intentionally does not apply V2's two-sided hash or fidelity rules.
func readRclonePortableManifestV1(
	ctx context.Context,
	indexPayload []byte,
	readChunk func(context.Context, int, int64) ([]byte, error),
	options RcloneManifestBuildOptions,
	commit RcloneCommitV1,
	visit func(rcloneCanonicalManifestEntry, rcloneCanonicalManifestEntry) error,
) error {
	var index rcloneCatalogManifestIndexV1
	if err := decodeRclonePortableCanonicalJSON(indexPayload, &index); err != nil || index.Version != 1 ||
		index.Generator != "xirang-rclone-manifest" || index.GeneratorVersion != "v1" || index.Chunks == nil ||
		index.Fidelity.Version != 1 || index.Fidelity.MetadataFields == nil ||
		index.EntryCount != commit.ManifestEntryCount || index.LogicalBytes != commit.LogicalBytes ||
		index.EntryCount > uint64(options.Limits.MaxEntries) || len(index.Chunks) != len(commit.ManifestChunkDigests) {
		return ErrRcloneManifestObservationMismatch
	}
	fidelity, err := json.Marshal(index.Fidelity)
	if err != nil || sha256Hex(fidelity) != commit.FidelityEvidenceDigest ||
		commit.Portable == nil || commit.ContentProofDigest != commit.Portable.HashEvidenceDigest {
		return ErrRcloneManifestObservationMismatch
	}
	observation := sha256.New()
	total := int64(len(indexPayload))
	var entryCount, logicalBytes uint64
	var previous string
	var directories []string
	for ordinal, reference := range index.Chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if reference.Ordinal != ordinal || reference.Size <= 0 || reference.Size > int64(options.ChunkMaxBytes) ||
			reference.EntryCount == 0 || reference.EntryCount > uint64(options.ChunkMaxEntries) ||
			reference.Digest != commit.ManifestChunkDigests[ordinal] || !lowerHex(reference.Digest, 64) ||
			reference.Size > options.Limits.MaxBytes-total || reference.Size > options.SpoolMaxBytes-total {
			return ErrRcloneManifestLimitExceeded
		}
		total += reference.Size
		payload, err := readChunk(ctx, ordinal, reference.Size)
		if err != nil {
			return err
		}
		if int64(len(payload)) != reference.Size || sha256Hex(payload) != reference.Digest {
			return ErrRcloneManifestObservationMismatch
		}
		_, _ = observation.Write(payload)
		cursor := rclonePortableLineCursor{payload: payload, maxRecordBytes: options.Limits.MaxRecordBytes}
		var chunkCount uint64
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			line, present, err := cursor.next()
			if err != nil {
				return err
			}
			if !present {
				break
			}
			chunkCount++
			entryCount, err = checkedAddUint64(entryCount, 1)
			if err != nil || chunkCount > reference.EntryCount || entryCount > index.EntryCount {
				return ErrRcloneManifestLimitExceeded
			}
			var entry rcloneCanonicalManifestEntry
			if err := decodeRclonePortableCanonicalJSON(line, &entry); err != nil || entry.Version != 1 ||
				!validRcloneLogicalPath(entry.Path, options.Limits.MaxDepth) ||
				!validRcloneLogicalPath(entry.PhysicalPath, options.Limits.MaxDepth) || entry.Size > uint64(^uint64(0)>>1) {
				return ErrRcloneManifestPathUnsafe
			}
			if _, err := time.Parse(time.RFC3339Nano, entry.ModTime); err != nil {
				return ErrRcloneManifestPathUnsafe
			}
			switch entry.Kind {
			case "file", "symlink":
				logicalBytes, err = checkedAddUint64(logicalBytes, entry.Size)
				if err != nil {
					return ErrRcloneManifestLimitExceeded
				}
			case "directory":
			default:
				return ErrRcloneManifestPathUnsafe
			}
			if err := trackRclonePortableManifestPath(&directories, &previous, entry); err != nil {
				return err
			}
			if visit != nil {
				if err := visit(entry, entry); err != nil {
					return err
				}
			}
		}
		if chunkCount != reference.EntryCount {
			return ErrRcloneManifestObservationMismatch
		}
	}
	if entryCount != index.EntryCount || logicalBytes != index.LogicalBytes {
		return ErrRcloneManifestObservationMismatch
	}
	_, _ = io.WriteString(observation, fmt.Sprintf("entries:%d\nbytes:%d\n", entryCount, logicalBytes))
	digest := hex.EncodeToString(observation.Sum(nil))
	if digest != commit.SourceObservationDigest || digest != commit.DestinationObservationDigest {
		return ErrRcloneManifestObservationMismatch
	}
	return nil
}
