package export

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const selectionDigestDomain = "xirang.backup_asset.export.selection.v1"

func FreezeSelection(items []FrozenItem, binding *SavedSearchCommitBindingV1, limits SelectionLimits) (FrozenSelection, error) {
	if len(items) == 0 || !validSelectionLimits(limits) || validateSavedSearchBinding(binding) != nil {
		return FrozenSelection{}, ErrInvalidSelection
	}
	unique := make(map[string]FrozenItem, len(items))
	for _, item := range items {
		if err := ValidateFrozenItem(item); err != nil {
			return FrozenSelection{}, err
		}
		key := strings.ToLower(item.Ref.RecoveryPointID) + "\x00" + item.Ref.EntryID
		if previous, exists := unique[key]; exists {
			if !frozenItemsEqual(previous, item) {
				return FrozenSelection{}, fmt.Errorf("%w: duplicate composite identity changed", ErrInvalidSelection)
			}
			continue
		}
		unique[key] = cloneFrozenItem(item)
	}
	ordered := make([]FrozenItem, 0, len(unique))
	sources := make(map[string]struct{})
	var logicalBytes int64
	for _, item := range unique {
		ordered = append(ordered, item)
		sources[strings.ToLower(item.Ref.RecoveryPointID)] = struct{}{}
		if item.LogicalSize > limits.MaxLogicalBytes-logicalBytes {
			return FrozenSelection{}, ErrSelectionLimit
		}
		logicalBytes += item.LogicalSize
	}
	if len(ordered) > limits.MaxItems || len(sources) > limits.MaxSourcePoints {
		return FrozenSelection{}, ErrSelectionLimit
	}
	sort.Slice(ordered, func(i, j int) bool {
		leftPoint := strings.ToLower(ordered[i].Ref.RecoveryPointID)
		rightPoint := strings.ToLower(ordered[j].Ref.RecoveryPointID)
		if leftPoint != rightPoint {
			return leftPoint < rightPoint
		}
		return ordered[i].Ref.EntryID < ordered[j].Ref.EntryID
	})
	digest, err := selectionDigest(ordered)
	if err != nil {
		return FrozenSelection{}, err
	}
	result := FrozenSelection{Items: ordered, Digest: digest}
	if binding != nil {
		copyBinding := *binding
		result.SavedSearch = &copyBinding
	}
	return result, nil
}

func selectionDigest(items []FrozenItem) (string, error) {
	var canonical bytes.Buffer
	writeString(&canonical, selectionDigestDomain)
	writeUint64(&canonical, uint64(len(items)))
	for _, item := range items {
		writeUint64(&canonical, uint64(item.SchemaVersion))
		writeString(&canonical, strings.ToLower(item.Ref.RecoveryPointID))
		writeString(&canonical, item.Ref.EntryID)
		writeString(&canonical, item.CatalogGenerationID)
		writeString(&canonical, item.SourceFingerprint)
		writeString(&canonical, item.EntryFingerprint)
		writeString(&canonical, item.FingerprintStrength)
		writeUint64(&canonical, uint64(item.ProviderCapabilityRevision))
		writeString(&canonical, string(item.EntryType))
		writeUint64(&canonical, uint64(item.LogicalSize))
		writeString(&canonical, item.MediaType)
		if item.RetentionUntil == nil {
			canonical.WriteByte(0)
		} else {
			canonical.WriteByte(1)
			writeString(&canonical, item.RetentionUntil.UTC().Format(time.RFC3339Nano))
		}
		writeUint64(&canonical, uint64(item.SelectionRootOrdinal))
		writeUint64(&canonical, uint64(len(item.ArchiveComponents)))
		for _, component := range item.ArchiveComponents {
			writeString(&canonical, component)
		}
	}
	sum := sha256.Sum256(canonical.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func writeString(buffer *bytes.Buffer, value string) {
	writeUint64(buffer, uint64(len(value)))
	buffer.WriteString(value)
}

func writeUint64(buffer *bytes.Buffer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	buffer.Write(encoded[:])
}

func cloneFrozenItem(item FrozenItem) FrozenItem {
	item.ArchiveComponents = append([]string(nil), item.ArchiveComponents...)
	if item.RetentionUntil != nil {
		value := *item.RetentionUntil
		item.RetentionUntil = &value
	}
	return item
}

func frozenItemsEqual(left, right FrozenItem) bool {
	leftDigest, leftErr := selectionDigest([]FrozenItem{left})
	rightDigest, rightErr := selectionDigest([]FrozenItem{right})
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest
}
