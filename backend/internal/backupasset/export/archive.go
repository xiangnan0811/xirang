package export

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
	workerCapabilities "xirang/backend/internal/backupasset/processing/capabilities"

	"golang.org/x/text/unicode/norm"
)

type ArchiveFormat string

const (
	ArchiveZIP ArchiveFormat = "zip"
	ArchiveTAR ArchiveFormat = "tar"
)

const (
	ArchiveProfileZIPDeflateV1 = "zip_deflate_v1"
	ArchiveProfileTARNoneV1    = "tar_none_v1"
	ArchiveProfileTARGzipV1    = "tar_gzip_v1"
)

func ValidArchiveProfilePair(format ArchiveFormat, profile string) bool {
	switch format {
	case ArchiveZIP:
		return profile == ArchiveProfileZIPDeflateV1
	case ArchiveTAR:
		return profile == ArchiveProfileTARNoneV1 || profile == ArchiveProfileTARGzipV1
	default:
		return false
	}
}

type ResultKind string

const (
	ResultComplete ResultKind = "complete"
	ResultPartial  ResultKind = "partial"
)

const (
	ItemErrorLinkMetadataUnavailable = "link_metadata_unavailable"
	ItemErrorSpecialFileSkipped      = "special_file_skipped"
)

type ArchiveEntry struct {
	ItemID               string
	Components           []string
	RecoveryPointID      string
	EntryID              string
	SelectionRootOrdinal int
	Type                 backupasset.CatalogEntryType
	Size                 int64
	ProviderBytes        int64
	ProviderEvidence     bool
	ModifiedAt           time.Time
	Open                 func(context.Context) (io.ReadCloser, error)
	// PreHeaderFailure records a durable spool-stage failure. It is valid only
	// for regular files and prevents the writer from emitting a file header.
	PreHeaderFailure string
}

type ArchiveLimits struct {
	MaxItems         int
	MaxLogicalBytes  int64
	MaxProviderBytes int64
}

type ArchiveItemReport struct {
	ItemID                  string    `json:"item_id"`
	MemberPath              string    `json:"member_path,omitempty"`
	State                   ItemState `json:"state"`
	LogicalBytes            int64     `json:"logical_bytes"`
	ProviderBytes           int64     `json:"provider_bytes"`
	ErrorCategory           string    `json:"error_category,omitempty"`
	preHeaderSpoolRecovered bool
}

type ArchiveReport struct {
	SchemaVersion   int                 `json:"schema_version"`
	SelectionDigest string              `json:"selection_digest"`
	ResultKind      ResultKind          `json:"result_kind"`
	Packed          int64               `json:"packed"`
	Skipped         int64               `json:"skipped"`
	Failed          int64               `json:"failed"`
	LogicalBytes    int64               `json:"logical_bytes"`
	ProviderBytes   int64               `json:"provider_bytes"`
	Items           []ArchiveItemReport `json:"items"`
}

func closeArchiveLayers(first io.Closer, second io.Closer) error {
	var firstErr error
	if first != nil {
		firstErr = first.Close()
	}
	var secondErr error
	if second != nil {
		secondErr = second.Close()
	}
	return errors.Join(firstErr, secondErr)
}

func WriteArchive(
	ctx context.Context,
	destination io.Writer,
	format ArchiveFormat,
	profile string,
	selectionDigest string,
	entries []ArchiveEntry,
	limits ArchiveLimits,
) (ArchiveReport, error) {
	if destination == nil || !ValidArchiveProfilePair(format, profile) || limits.MaxItems <= 0 ||
		limits.MaxLogicalBytes <= 0 || limits.MaxProviderBytes <= 0 || !lowerHex(selectionDigest, 64) ||
		len(entries) == 0 || len(entries) > limits.MaxItems {
		return ArchiveReport{}, ErrArchiveLimit
	}
	prepared, err := prepareArchiveEntries(entries)
	if err != nil {
		return ArchiveReport{}, err
	}
	report := ArchiveReport{
		SchemaVersion: 1, SelectionDigest: selectionDigest, ResultKind: ResultComplete,
		Items: make([]ArchiveItemReport, 0, len(entries)),
	}
	var zipWriter *zip.Writer
	var tarWriter *tar.Writer
	var gzipWriter *gzip.Writer
	if format == ArchiveZIP {
		zipWriter = zip.NewWriter(destination)
	} else {
		tarDestination := destination
		if profile == ArchiveProfileTARGzipV1 {
			gzipWriter, err = gzip.NewWriterLevel(destination, 6)
			if err != nil {
				return ArchiveReport{}, err
			}
			gzipWriter.ModTime = time.Time{}
			gzipWriter.OS = 255
			tarDestination = gzipWriter
		}
		tarWriter = tar.NewWriter(tarDestination)
	}
	closed := false
	closeArchive := func() error {
		if closed {
			return nil
		}
		closed = true
		if zipWriter != nil {
			return zipWriter.Close()
		}
		var compressionCloser io.Closer
		if gzipWriter != nil {
			compressionCloser = gzipWriter
		}
		return closeArchiveLayers(tarWriter, compressionCloser)
	}
	failed := true
	defer func() {
		if failed {
			_ = closeArchive()
		}
	}()

	for _, preparedEntry := range prepared {
		if err := ctx.Err(); err != nil {
			return ArchiveReport{}, err
		}
		entry := preparedEntry.entry
		if backupasset.ValidateOpaqueID(entry.ItemID) != nil || entry.Size < 0 {
			return ArchiveReport{}, ErrArchiveSource
		}
		archivePath := preparedEntry.path
		item := ArchiveItemReport{ItemID: entry.ItemID, MemberPath: archivePath}
		switch entry.Type {
		case backupasset.CatalogEntryDirectory:
			item.MemberPath += "/"
			if err := writeDirectory(zipWriter, tarWriter, item.MemberPath, entry.ModifiedAt); err != nil {
				return ArchiveReport{}, err
			}
			item.State = ItemPacked
			report.Packed++
		case backupasset.CatalogEntrySymlink, backupasset.CatalogEntryHardlink:
			item.State = ItemSkipped
			item.ErrorCategory = ItemErrorLinkMetadataUnavailable
			report.Skipped++
			report.ResultKind = ResultPartial
		case backupasset.CatalogEntrySpecial, backupasset.CatalogEntryUnknown:
			item.State = ItemSkipped
			item.ErrorCategory = ItemErrorSpecialFileSkipped
			report.Skipped++
			report.ResultKind = ResultPartial
		case backupasset.CatalogEntryFile:
			if entry.PreHeaderFailure != "" {
				if !validPreHeaderFailureCategory(entry.PreHeaderFailure) ||
					entry.ProviderBytes < 0 || entry.ProviderBytes > limits.MaxProviderBytes-report.ProviderBytes {
					return ArchiveReport{}, ErrArchiveSource
				}
				item.MemberPath = ""
				item.State = ItemFailed
				item.ProviderBytes = entry.ProviderBytes
				item.ErrorCategory = entry.PreHeaderFailure
				report.Failed++
				report.ProviderBytes += item.ProviderBytes
				report.ResultKind = ResultPartial
				break
			}
			if entry.Open == nil || entry.Size > limits.MaxLogicalBytes-report.LogicalBytes ||
				entry.ProviderBytes < 0 || (entry.ProviderEvidence && entry.ProviderBytes > limits.MaxProviderBytes-report.ProviderBytes) ||
				(!entry.ProviderEvidence && entry.Size > limits.MaxProviderBytes-report.ProviderBytes) {
				return ArchiveReport{}, ErrArchiveLimit
			}
			source, err := entry.Open(ctx)
			if err != nil {
				var failure *PreHeaderSpoolFailure
				if errors.As(err, &failure) && failure.ItemID() == entry.ItemID &&
					failure.ProviderBytes() == entry.ProviderBytes &&
					recoverablePreHeaderAuthenticatedSpoolError(failure) &&
					entry.ProviderBytes <= limits.MaxProviderBytes-report.ProviderBytes {
					item.MemberPath = ""
					item.State = ItemFailed
					item.ProviderBytes = entry.ProviderBytes
					item.ErrorCategory = "internal_failure"
					item.preHeaderSpoolRecovered = true
					report.Failed++
					report.ProviderBytes += item.ProviderBytes
					report.ResultKind = ResultPartial
					break
				}
				return ArchiveReport{}, errors.Join(ErrArchiveSource, err)
			}
			writer, err := writeFileHeader(zipWriter, tarWriter, archivePath, entry.Size, entry.ModifiedAt)
			if err != nil {
				_ = source.Close()
				return ArchiveReport{}, err
			}
			written, copyErr := copyExact(ctx, writer, source, entry.Size)
			closeErr := source.Close()
			if copyErr != nil || closeErr != nil || written != entry.Size {
				return ArchiveReport{}, errors.Join(ErrArchiveSource, copyErr, closeErr)
			}
			item.State = ItemPacked
			item.LogicalBytes = written
			providerBytes := written
			if entry.ProviderEvidence {
				providerBytes = entry.ProviderBytes
			}
			item.ProviderBytes = providerBytes
			report.Packed++
			report.LogicalBytes += written
			report.ProviderBytes += providerBytes
		default:
			return ArchiveReport{}, ErrArchiveSource
		}
		report.Items = append(report.Items, item)
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return ArchiveReport{}, err
	}
	if err := writeReport(zipWriter, tarWriter, reportJSON); err != nil {
		return ArchiveReport{}, err
	}
	if err := closeArchive(); err != nil {
		return ArchiveReport{}, err
	}
	failed = false
	return report, nil
}

func validPreHeaderFailureCategory(category string) bool {
	return category == "source_changed" || category == "internal_failure"
}

const (
	maxArchivePathDepth     = 16
	maxArchiveComponentSize = 255
	maxArchiveMemberSize    = 4096
	archiveReportName       = "xirang-export-report.v1.json"
)

type preparedArchiveEntry struct {
	entry                   ArchiveEntry
	path                    string
	baseCollision           string
	compositeKey            string
	rootScope               string
	rootIdentity            string
	scopeCrossRootCollision bool
}

// archivePathAllocationStats is a per-call test seam for bounding allocator
// work without relying on timing-sensitive performance assertions.
type archivePathAllocationStats struct {
	visits  int
	probes  int
	maxWork int
}

var errArchivePathAllocationWorkLimit = errors.New("archive path allocation work limit exceeded")

func (stats *archivePathAllocationStats) recordVisit() error {
	if stats == nil {
		return nil
	}
	stats.visits++
	if stats.maxWork > 0 && stats.visits+stats.probes > stats.maxWork {
		return errArchivePathAllocationWorkLimit
	}
	return nil
}

func (stats *archivePathAllocationStats) recordProbe() error {
	if stats == nil {
		return nil
	}
	stats.probes++
	if stats.maxWork > 0 && stats.visits+stats.probes > stats.maxWork {
		return errArchivePathAllocationWorkLimit
	}
	return nil
}

func prepareArchiveEntries(entries []ArchiveEntry) ([]preparedArchiveEntry, error) {
	return prepareArchiveEntriesWithStats(entries, nil)
}

func prepareArchiveEntriesWithStats(
	entries []ArchiveEntry,
	stats *archivePathAllocationStats,
) ([]preparedArchiveEntry, error) {
	prepared := make([]preparedArchiveEntry, 0, len(entries))
	for _, entry := range entries {
		if backupasset.ValidateAssetRef(backupasset.AssetRef{
			RecoveryPointID: entry.RecoveryPointID,
			EntryID:         entry.EntryID,
		}) != nil || entry.SelectionRootOrdinal < 0 {
			return nil, ErrArchiveSource
		}
		archivePath, err := SanitizeArchiveComponents(entry.Components)
		if err != nil {
			return nil, err
		}
		collision := workerCapabilities.CanonicalNFKCCasefold(archivePath)
		if collision == "" {
			return nil, ErrArchiveSource
		}
		prepared = append(prepared, preparedArchiveEntry{
			entry: entry, path: archivePath, baseCollision: collision, compositeKey: archiveCompositeKey(entry),
			rootScope: archiveRootScope(entry), rootIdentity: archiveRootScopeIdentity(entry),
		})
	}
	disambiguateArchiveRootScopes(prepared)
	scopeCrossRootArchivePathCollisions(prepared)

	for index := range prepared {
		if prepared[index].scopeCrossRootCollision {
			if prepared[index].rootScope == "" {
				return nil, ErrArchiveSource
			}
			scopedPath, scopeErr := prependArchiveScope(prepared[index].rootScope, prepared[index].path)
			if scopeErr != nil {
				return nil, scopeErr
			}
			prepared[index].path = scopedPath
		}
		if err := validateFinalArchiveMember(prepared[index].path); err != nil {
			return nil, err
		}
	}
	sort.Slice(prepared, func(left, right int) bool {
		if prepared[left].baseCollision != prepared[right].baseCollision {
			return prepared[left].baseCollision < prepared[right].baseCollision
		}
		if prepared[left].compositeKey != prepared[right].compositeKey {
			return prepared[left].compositeKey < prepared[right].compositeKey
		}
		return prepared[left].entry.ItemID < prepared[right].entry.ItemID
	})

	allocator := newArchivePathAllocator(stats, len(entries)+1)
	if err := allocator.insert(archiveReportName, backupasset.CatalogEntryFile); err != nil {
		return nil, err
	}
	for index := range prepared {
		candidate, allocationErr := allocator.allocate(prepared[index].path, prepared[index].entry.Type)
		if allocationErr != nil {
			return nil, allocationErr
		}
		prepared[index].path = candidate
	}

	sort.Slice(prepared, func(left, right int) bool {
		leftCollision := workerCapabilities.CanonicalNFKCCasefold(prepared[left].path)
		rightCollision := workerCapabilities.CanonicalNFKCCasefold(prepared[right].path)
		if leftCollision != rightCollision {
			return leftCollision < rightCollision
		}
		if prepared[left].compositeKey != prepared[right].compositeKey {
			return prepared[left].compositeKey < prepared[right].compositeKey
		}
		return prepared[left].entry.ItemID < prepared[right].entry.ItemID
	})
	return prepared, nil
}

type archivePathScopeGroup struct {
	entriesByScope             map[string][]int
	nonDirectoryEntriesByScope map[string][]int
	scopeCount                 int
	nonDirectoryScopeCount     int
	singleScope                string
	singleNonDirectoryScope    string
	decision                   archivePathScopeDecision
	nonDirectoryDecision       archivePathScopeDecision
}

func (group *archivePathScopeGroup) addEntry(scope string, index int, entryType backupasset.CatalogEntryType) {
	if _, exists := group.entriesByScope[scope]; !exists {
		group.scopeCount++
		if group.scopeCount == 1 {
			group.singleScope = scope
		} else {
			group.singleScope = ""
		}
	}
	group.entriesByScope[scope] = append(group.entriesByScope[scope], index)
	if entryType == backupasset.CatalogEntryDirectory {
		return
	}
	if _, exists := group.nonDirectoryEntriesByScope[scope]; !exists {
		group.nonDirectoryScopeCount++
		if group.nonDirectoryScopeCount == 1 {
			group.singleNonDirectoryScope = scope
		} else {
			group.singleNonDirectoryScope = ""
		}
	}
	group.nonDirectoryEntriesByScope[scope] = append(group.nonDirectoryEntriesByScope[scope], index)
}

type archivePathScopeDecision struct {
	scopeAll      bool
	hasOtherScope bool
	otherScope    string
}

func (decision *archivePathScopeDecision) addCounterpart(scopeCount int, singleScope string) {
	if decision.scopeAll || scopeCount == 0 {
		return
	}
	if scopeCount > 1 {
		decision.scopeAll = true
		return
	}
	if !decision.hasOtherScope {
		decision.hasOtherScope = true
		decision.otherScope = singleScope
		return
	}
	if decision.otherScope != singleScope {
		decision.scopeAll = true
	}
}

func (decision archivePathScopeDecision) appliesTo(scope string) bool {
	return decision.scopeAll || (decision.hasOtherScope && scope != decision.otherScope)
}

type archivePathScopeClassifier struct {
	entryFlagApplications int
}

// scopeCrossRootArchivePathCollisions derives scoped root decisions for exact
// and component-boundary ancestor collisions, then applies each decision once.
// Paths have a bounded depth, so the prefix index needs at most one lookup per
// canonical ancestor rather than pairwise comparisons across all entries.
func scopeCrossRootArchivePathCollisions(prepared []preparedArchiveEntry) {
	classifier := archivePathScopeClassifier{}
	classifier.classify(prepared)
}

func (classifier *archivePathScopeClassifier) classify(prepared []preparedArchiveEntry) {
	classifier.entryFlagApplications = 0
	groups := make(map[string]*archivePathScopeGroup, len(prepared))
	for index := range prepared {
		group := groups[prepared[index].baseCollision]
		if group == nil {
			group = &archivePathScopeGroup{
				entriesByScope:             make(map[string][]int),
				nonDirectoryEntriesByScope: make(map[string][]int),
			}
			groups[prepared[index].baseCollision] = group
		}
		group.addEntry(preparedArchiveRootIdentity(prepared[index]), index, prepared[index].entry.Type)
	}

	for _, group := range groups {
		if group.scopeCount > 1 {
			group.decision.scopeAll = true
		}
	}
	for collision, group := range groups {
		for boundary := strings.IndexByte(collision, '/'); boundary >= 0; {
			if ancestor := groups[collision[:boundary]]; ancestor != nil {
				group.decision.addCounterpart(ancestor.nonDirectoryScopeCount, ancestor.singleNonDirectoryScope)
				ancestor.nonDirectoryDecision.addCounterpart(group.scopeCount, group.singleScope)
			}
			next := strings.IndexByte(collision[boundary+1:], '/')
			if next < 0 {
				break
			}
			boundary += next + 1
		}
	}

	for _, group := range groups {
		for identity, indexes := range group.entriesByScope {
			if group.decision.appliesTo(identity) {
				classifier.markEntries(prepared, indexes)
			}
		}
		for identity, indexes := range group.nonDirectoryEntriesByScope {
			if group.nonDirectoryDecision.appliesTo(identity) {
				classifier.markEntries(prepared, indexes)
			}
		}
	}
}

func (classifier *archivePathScopeClassifier) markEntries(prepared []preparedArchiveEntry, indexes []int) {
	for _, index := range indexes {
		if prepared[index].scopeCrossRootCollision {
			continue
		}
		classifier.entryFlagApplications++
		prepared[index].scopeCrossRootCollision = true
	}
}

func SanitizeArchiveComponents(components []string) (string, error) {
	if len(components) == 0 || len(components) > maxArchivePathDepth {
		return "", ErrArchiveSource
	}
	sanitized := make([]string, 0, len(components))
	for _, component := range components {
		value, err := sanitizeArchiveComponent(component)
		if err != nil {
			return "", err
		}
		sanitized = append(sanitized, value)
	}
	joined := strings.Join(sanitized, "/")
	if joined == "" || len(joined) > maxArchiveMemberSize || strings.HasPrefix(joined, "/") ||
		strings.HasPrefix(joined, "../") || workerCapabilities.CanonicalNFKCCasefold(joined) == "" {
		return "", ErrArchiveSource
	}
	return joined, nil
}

func prependArchiveScope(scope, archivePath string) (string, error) {
	scopeComponents := strings.Split(scope, "/")
	sourceComponents := strings.Split(archivePath, "/")
	remainingDepth := maxArchivePathDepth - len(scopeComponents)
	if remainingDepth <= 0 {
		return "", ErrArchiveSource
	}
	if len(sourceComponents) > remainingDepth {
		sourceComponents = compactArchiveComponents(sourceComponents, remainingDepth)
	}
	components := append(append(make([]string, 0, len(scopeComponents)+len(sourceComponents)), scopeComponents...), sourceComponents...)
	member := strings.Join(components, "/")
	if err := validateFinalArchiveMember(member); err != nil {
		return "", err
	}
	return member, nil
}

func compactArchiveComponents(components []string, limit int) []string {
	if len(components) <= limit {
		return append([]string(nil), components...)
	}
	sum := sha256.Sum256([]byte("xirang.archive.path-components.v1\x00" + strings.Join(components, "\x00")))
	marker := "path-" + hex.EncodeToString(sum[:8])
	if limit == 1 {
		return []string{marker}
	}
	if limit == 2 {
		return []string{marker, components[len(components)-1]}
	}
	compacted := make([]string, 0, limit)
	compacted = append(compacted, components[:limit-2]...)
	compacted = append(compacted, marker, components[len(components)-1])
	return compacted
}

func validateFinalArchiveMember(member string) error {
	if member == "" || !utf8.ValidString(member) || len(member) > maxArchiveMemberSize || strings.HasPrefix(member, "/") ||
		strings.HasPrefix(member, "../") || workerCapabilities.CanonicalNFKCCasefold(member) == "" {
		return ErrArchiveSource
	}
	components := strings.Split(member, "/")
	if len(components) == 0 || len(components) > maxArchivePathDepth {
		return ErrArchiveSource
	}
	for _, component := range components {
		if len(component) > maxArchiveComponentSize {
			return ErrArchiveSource
		}
		sanitized, err := sanitizeArchiveComponent(component)
		if err != nil || sanitized != component {
			return ErrArchiveSource
		}
	}
	return nil
}

func sanitizeArchiveComponent(component string) (string, error) {
	if component == "" || !utf8.ValidString(component) || component == "." || component == ".." ||
		strings.ContainsAny(component, "\x00/\\:") {
		return "", ErrArchiveSource
	}
	for _, character := range component {
		if unicode.IsControl(character) || unicode.In(
			character, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector,
		) {
			return "", ErrArchiveSource
		}
	}
	value := strings.TrimRight(norm.NFKC.String(component), " .")
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "\x00/\\:") || isWindowsDriveRoot(value) {
		return "", ErrArchiveSource
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(
			character, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector,
		) {
			return "", ErrArchiveSource
		}
	}
	if isWindowsReservedName(value) {
		if extensionIndex := strings.IndexByte(value, '.'); extensionIndex >= 0 {
			value = value[:extensionIndex] + "_" + value[extensionIndex:]
		} else {
			value += "_"
		}
	}
	return boundArchiveComponent(value), nil
}

func isWindowsDriveRoot(value string) bool {
	return len(value) >= 2 && value[1] == ':' && (value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z')
}

func boundArchiveComponent(value string) string {
	if len(value) <= maxArchiveComponentSize {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	suffix := "~" + hex.EncodeToString(sum[:8])
	return truncateUTF8(value, maxArchiveComponentSize-len(suffix)) + suffix
}

func archiveCompositeKey(entry ArchiveEntry) string {
	if entry.RecoveryPointID != "" && entry.EntryID != "" {
		return strings.ToLower(entry.RecoveryPointID) + "\x00" + entry.EntryID
	}
	return entry.ItemID
}

func archiveRootScope(entry ArchiveEntry) string {
	recoveryPointID := strings.ToLower(entry.RecoveryPointID)
	if !lowerHex(recoveryPointID, 32) || entry.SelectionRootOrdinal < 0 {
		return ""
	}
	return "rp-" + recoveryPointID[:8] + "/root-" + strconv.Itoa(entry.SelectionRootOrdinal)
}

func archiveRootScopeIdentity(entry ArchiveEntry) string {
	recoveryPointID := strings.ToLower(entry.RecoveryPointID)
	if !lowerHex(recoveryPointID, 32) || entry.SelectionRootOrdinal < 0 {
		return ""
	}
	return recoveryPointID + "\x00" + strconv.Itoa(entry.SelectionRootOrdinal)
}

func archiveExpandedRootScope(entry ArchiveEntry) string {
	recoveryPointID := strings.ToLower(entry.RecoveryPointID)
	if !lowerHex(recoveryPointID, 32) || entry.SelectionRootOrdinal < 0 {
		return ""
	}
	return "rp-" + recoveryPointID + "/root-" + strconv.Itoa(entry.SelectionRootOrdinal)
}

// disambiguateArchiveRootScopes leaves the compact public label untouched when
// it identifies one root, and expands only labels shared by distinct frozen
// recovery-point/root identities.
func disambiguateArchiveRootScopes(prepared []preparedArchiveEntry) {
	identitiesByScope := make(map[string]map[string]struct{}, len(prepared))
	for _, entry := range prepared {
		if entry.rootScope == "" || entry.rootIdentity == "" {
			continue
		}
		identities := identitiesByScope[entry.rootScope]
		if identities == nil {
			identities = make(map[string]struct{})
			identitiesByScope[entry.rootScope] = identities
		}
		identities[entry.rootIdentity] = struct{}{}
	}
	for index := range prepared {
		identities := identitiesByScope[prepared[index].rootScope]
		if len(identities) <= 1 {
			continue
		}
		prepared[index].rootScope = archiveExpandedRootScope(prepared[index].entry)
	}
}

func preparedArchiveRootIdentity(entry preparedArchiveEntry) string {
	if entry.rootIdentity != "" {
		return entry.rootIdentity
	}
	return entry.rootScope
}

type archivePathIndex struct {
	entries     map[string]backupasset.CatalogEntryType
	descendants map[string]int
	stats       *archivePathAllocationStats
}

func newArchivePathIndex(stats *archivePathAllocationStats) *archivePathIndex {
	return &archivePathIndex{
		entries:     make(map[string]backupasset.CatalogEntryType),
		descendants: make(map[string]int),
		stats:       stats,
	}
}

func (index *archivePathIndex) conflict(
	candidate string,
	candidateType backupasset.CatalogEntryType,
) (bool, bool, string, error) {
	if err := index.stats.recordProbe(); err != nil {
		return false, false, "", err
	}
	candidateKey := workerCapabilities.CanonicalNFKCCasefold(candidate)
	if candidateKey == "" {
		return false, false, "", ErrArchiveSource
	}
	if err := index.stats.recordVisit(); err != nil {
		return false, false, "", err
	}
	if _, exists := index.entries[candidateKey]; exists {
		return true, false, "", nil
	}

	components := strings.Split(candidateKey, "/")
	prefix := components[0]
	for componentIndex := 1; componentIndex < len(components); componentIndex++ {
		if err := index.stats.recordVisit(); err != nil {
			return false, false, "", err
		}
		if existingType, exists := index.entries[prefix]; exists && existingType != backupasset.CatalogEntryDirectory {
			return true, true, prefix, nil
		}
		prefix += "/" + components[componentIndex]
	}
	if candidateType != backupasset.CatalogEntryDirectory {
		if err := index.stats.recordVisit(); err != nil {
			return false, false, "", err
		}
		if index.descendants[candidateKey] > 0 {
			return true, false, "", nil
		}
	}
	return false, false, "", nil
}

func (index *archivePathIndex) insert(candidate string, candidateType backupasset.CatalogEntryType) error {
	candidateKey := workerCapabilities.CanonicalNFKCCasefold(candidate)
	if candidateKey == "" {
		return ErrArchiveSource
	}
	components := strings.Split(candidateKey, "/")
	index.entries[candidateKey] = candidateType
	if len(components) == 1 {
		return nil
	}
	prefix := components[0]
	for componentIndex := 1; componentIndex < len(components); componentIndex++ {
		if err := index.stats.recordVisit(); err != nil {
			return err
		}
		index.descendants[prefix]++
		prefix += "/" + components[componentIndex]
	}
	return nil
}

type archiveSuffixSeries struct {
	next map[int]int
}

func (series *archiveSuffixSeries) find(suffix int) int {
	for {
		next, exists := series.next[suffix]
		if !exists {
			return suffix
		}
		if afterNext, hasAfterNext := series.next[next]; hasAfterNext {
			series.next[suffix] = afterNext
		}
		suffix = next
	}
}

func (series *archiveSuffixSeries) reserve(suffix, next int) {
	series.next[suffix] = next
}

type archivePathAllocator struct {
	index                *archivePathIndex
	series               map[string]*archiveSuffixSeries
	firstComponentSeries map[string]*archiveSuffixSeries
	fileAncestorSeries   map[string]*archiveSuffixSeries
	maximumTail          int
}

func newArchivePathAllocator(stats *archivePathAllocationStats, maximumTail int) *archivePathAllocator {
	return &archivePathAllocator{
		index:                newArchivePathIndex(stats),
		series:               make(map[string]*archiveSuffixSeries),
		firstComponentSeries: make(map[string]*archiveSuffixSeries),
		fileAncestorSeries:   make(map[string]*archiveSuffixSeries),
		maximumTail:          maximumTail,
	}
}

func (allocator *archivePathAllocator) insert(path string, entryType backupasset.CatalogEntryType) error {
	if err := validateFinalArchiveMember(path); err != nil {
		return err
	}
	return allocator.index.insert(path, entryType)
}

func (allocator *archivePathAllocator) allocate(base string, entryType backupasset.CatalogEntryType) (string, error) {
	conflict, prefixConflict, _, err := allocator.index.conflict(base, entryType)
	if err != nil {
		return "", err
	}
	if !conflict {
		if err := allocator.insert(base, entryType); err != nil {
			return "", err
		}
		return base, nil
	}
	if prefixConflict {
		return allocator.allocateFirstComponentSuffix(base, entryType, 1)
	}
	return allocator.allocateLastComponentSuffix(base, entryType, 1)
}

func (allocator *archivePathAllocator) allocateLastComponentSuffix(
	base string,
	entryType backupasset.CatalogEntryType,
	start int,
) (string, error) {
	for suffix := start; ; {
		if suffix > allocator.maximumTail {
			return "", ErrArchiveSource
		}
		width := len(strconv.Itoa(suffix))
		series := allocator.suffixSeries(base, entryType, -1, suffix)
		suffix = series.find(suffix)
		if len(strconv.Itoa(suffix)) != width {
			continue
		}
		if suffix > allocator.maximumTail {
			return "", ErrArchiveSource
		}
		candidate := appendArchiveSuffix(base, suffix, -1)
		if err := validateFinalArchiveMember(candidate); err != nil {
			return "", err
		}
		conflict, prefixConflict, _, err := allocator.index.conflict(candidate, entryType)
		if err != nil {
			return "", err
		}
		if !conflict {
			if err := allocator.insert(candidate, entryType); err != nil {
				return "", err
			}
			series.reserve(suffix, suffix+1)
			return candidate, nil
		}
		series.reserve(suffix, suffix+1)
		if prefixConflict {
			return allocator.allocateFirstComponentSuffix(base, entryType, suffix+1)
		}
		suffix++
	}
}

func (allocator *archivePathAllocator) allocateFirstComponentSuffix(
	base string,
	entryType backupasset.CatalogEntryType,
	start int,
) (string, error) {
	for suffix := start; ; {
		if suffix > allocator.maximumTail {
			return "", ErrArchiveSource
		}
		width := len(strconv.Itoa(suffix))
		firstComponentSeries := allocator.firstComponentSuffixSeries(base, suffix)
		suffix = firstComponentSeries.find(suffix)
		if len(strconv.Itoa(suffix)) != width {
			continue
		}
		if fileAncestorSuffix := allocator.firstComponentFileAncestorSuffix(base, suffix); fileAncestorSuffix != suffix {
			suffix = fileAncestorSuffix
			continue
		}
		series := allocator.suffixSeries(base, entryType, 0, suffix)
		suffix = series.find(suffix)
		if len(strconv.Itoa(suffix)) != width {
			continue
		}
		firstComponentSeries = allocator.firstComponentSuffixSeries(base, suffix)
		if firstComponentSuffix := firstComponentSeries.find(suffix); firstComponentSuffix != suffix {
			suffix = firstComponentSuffix
			continue
		}
		if fileAncestorSuffix := allocator.firstComponentFileAncestorSuffix(base, suffix); fileAncestorSuffix != suffix {
			suffix = fileAncestorSuffix
			continue
		}
		if suffix > allocator.maximumTail {
			return "", ErrArchiveSource
		}
		candidate := appendArchiveSuffix(base, suffix, 0)
		if err := validateFinalArchiveMember(candidate); err != nil {
			return "", err
		}
		conflict, prefixConflict, fileAncestor, err := allocator.index.conflict(candidate, entryType)
		if err != nil {
			return "", err
		}
		if !conflict {
			if err := allocator.insert(candidate, entryType); err != nil {
				return "", err
			}
			series.reserve(suffix, suffix+1)
			return candidate, nil
		}
		if prefixConflict {
			series.reserve(suffix, suffix+1)
			if !strings.Contains(fileAncestor, "/") {
				firstComponentSeries.reserve(suffix, suffix+1)
			} else {
				allocator.reserveFirstComponentFileAncestorSuffix(fileAncestor, suffix)
			}
			suffix++
			continue
		}
		series.reserve(suffix, suffix+1)
		suffix++
	}
}

func (allocator *archivePathAllocator) suffixSeries(
	base string,
	entryType backupasset.CatalogEntryType,
	componentIndex int,
	suffix int,
) *archiveSuffixSeries {
	key := archiveSuffixSeriesKey(base, entryType, componentIndex, suffix)
	series := allocator.series[key]
	if series == nil {
		series = &archiveSuffixSeries{next: make(map[int]int)}
		allocator.series[key] = series
	}
	return series
}

// firstComponentSuffixSeries tracks file ancestors such as "root~3" without
// coupling the state to a descendant-specific path suffix.
func (allocator *archivePathAllocator) firstComponentSuffixSeries(base string, suffix int) *archiveSuffixSeries {
	key := archiveFirstComponentSuffixSeriesKey(base, suffix)
	series := allocator.firstComponentSeries[key]
	if series == nil {
		series = &archiveSuffixSeries{next: make(map[int]int)}
		allocator.firstComponentSeries[key] = series
	}
	return series
}

// firstComponentFileAncestorSuffix skips suffixes whose expanded path has an
// already-known file ancestor, while leaving unrelated descendants free to
// share a first-component suffix.
func (allocator *archivePathAllocator) firstComponentFileAncestorSuffix(base string, suffix int) int {
	candidate := appendArchiveSuffix(base, suffix, 0)
	candidateKey := workerCapabilities.CanonicalNFKCCasefold(candidate)
	if candidateKey == "" {
		return suffix
	}
	components := strings.Split(candidateKey, "/")
	if len(components) < 3 {
		return suffix
	}
	nextSuffix := suffix
	prefix := components[0]
	for componentIndex := 1; componentIndex < len(components)-1; componentIndex++ {
		prefix += "/" + components[componentIndex]
		series := allocator.fileAncestorSeries[archiveFirstComponentFileAncestorSeriesKey(prefix, suffix)]
		if series == nil {
			continue
		}
		if candidateSuffix := series.find(suffix); candidateSuffix > nextSuffix {
			nextSuffix = candidateSuffix
		}
	}
	return nextSuffix
}

func (allocator *archivePathAllocator) reserveFirstComponentFileAncestorSuffix(ancestor string, suffix int) {
	key := archiveFirstComponentFileAncestorSeriesKey(ancestor, suffix)
	if key == "" {
		return
	}
	series := allocator.fileAncestorSeries[key]
	if series == nil {
		series = &archiveSuffixSeries{next: make(map[int]int)}
		allocator.fileAncestorSeries[key] = series
	}
	series.reserve(suffix, suffix+1)
}

func archiveSuffixSeriesKey(
	base string,
	entryType backupasset.CatalogEntryType,
	componentIndex int,
	suffix int,
) string {
	program := "first"
	if componentIndex < 0 {
		program = "last"
	}
	marker := "~" + strconv.Itoa(suffix)
	candidate := appendArchiveSuffix(base, suffix, componentIndex)
	components := strings.Split(candidate, "/")
	if componentIndex < 0 {
		componentIndex = len(components) - 1
	}
	if componentIndex < 0 || componentIndex >= len(components) {
		return ""
	}
	markerIndex := strings.LastIndex(components[componentIndex], marker)
	if markerIndex < 0 {
		return ""
	}
	prefixComponents := append([]string(nil), components[:componentIndex]...)
	prefixComponents = append(prefixComponents, components[componentIndex][:markerIndex])
	suffixComponents := append([]string{components[componentIndex][markerIndex+len(marker):]}, components[componentIndex+1:]...)
	directory := "file"
	if entryType == backupasset.CatalogEntryDirectory {
		directory = "directory"
	}
	return strings.Join([]string{
		program,
		strconv.Itoa(componentIndex),
		strconv.Itoa(len(strconv.Itoa(suffix))),
		directory,
		workerCapabilities.CanonicalNFKCCasefold(strings.Join(prefixComponents, "/")),
		workerCapabilities.CanonicalNFKCCasefold(strings.Join(suffixComponents, "/")),
	}, "\x00")
}

func archiveFirstComponentSuffixSeriesKey(base string, suffix int) string {
	candidate := appendArchiveSuffix(base, suffix, 0)
	components := strings.Split(candidate, "/")
	if len(components) == 0 {
		return ""
	}
	marker := "~" + strconv.Itoa(suffix)
	markerIndex := strings.LastIndex(components[0], marker)
	if markerIndex < 0 {
		return ""
	}
	return strings.Join([]string{
		strconv.Itoa(len(strconv.Itoa(suffix))),
		workerCapabilities.CanonicalNFKCCasefold(components[0][:markerIndex]),
		workerCapabilities.CanonicalNFKCCasefold(components[0][markerIndex+len(marker):]),
	}, "\x00")
}

func archiveFirstComponentFileAncestorSeriesKey(ancestor string, suffix int) string {
	components := strings.Split(workerCapabilities.CanonicalNFKCCasefold(ancestor), "/")
	if len(components) < 2 {
		return ""
	}
	marker := "~" + strconv.Itoa(suffix)
	markerIndex := strings.LastIndex(components[0], marker)
	if markerIndex < 0 {
		return ""
	}
	return strings.Join([]string{
		strconv.Itoa(len(strconv.Itoa(suffix))),
		components[0][:markerIndex],
		components[0][markerIndex+len(marker):],
		strings.Join(components[1:], "/"),
	}, "\x00")
}

func appendArchiveSuffix(value string, suffix int, componentIndex int) string {
	components := strings.Split(value, "/")
	if componentIndex < 0 {
		componentIndex = len(components) - 1
	}
	if componentIndex < 0 || componentIndex >= len(components) || suffix <= 0 {
		return ""
	}
	component := components[componentIndex]
	marker := "~" + strconv.Itoa(suffix)
	if len(marker) >= maxArchiveComponentSize {
		return ""
	}
	extension := ""
	if componentIndex == len(components)-1 {
		candidateExtension := path.Ext(component)
		candidateBase := strings.TrimSuffix(component, candidateExtension)
		if candidateExtension != component && candidateBase != "" &&
			len(candidateExtension)+len(marker) < maxArchiveComponentSize {
			truncatedBase := truncateUTF8(candidateBase, maxArchiveComponentSize-len(candidateExtension)-len(marker))
			if truncatedBase != "" {
				component = truncatedBase
				extension = candidateExtension
			}
		}
	}
	if extension == "" {
		component = truncateUTF8(component, maxArchiveComponentSize-len(marker))
	}
	components[componentIndex] = component + marker + extension
	return strings.Join(components, "/")
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func isWindowsReservedName(value string) bool {
	base, _, _ := strings.Cut(strings.ToUpper(value), ".")
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

func writeDirectory(zipWriter *zip.Writer, tarWriter *tar.Writer, name string, modified time.Time) error {
	name = strings.TrimSuffix(name, "/") + "/"
	if zipWriter != nil {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(0o755 | 0o040000)
		if !modified.IsZero() {
			header.Modified = modified.UTC()
		}
		_, err := zipWriter.CreateHeader(header)
		return err
	}
	return tarWriter.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755, ModTime: modified.UTC()})
}

func writeFileHeader(zipWriter *zip.Writer, tarWriter *tar.Writer, name string, size int64, modified time.Time) (io.Writer, error) {
	if zipWriter != nil {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o600)
		if !modified.IsZero() {
			header.Modified = modified.UTC()
		}
		return zipWriter.CreateHeader(header)
	}
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: size, ModTime: modified.UTC()}); err != nil {
		return nil, err
	}
	return tarWriter, nil
}

func writeReport(zipWriter *zip.Writer, tarWriter *tar.Writer, report []byte) error {
	if zipWriter != nil {
		header := &zip.FileHeader{Name: archiveReportName, Method: zip.Deflate}
		header.SetMode(0o600)
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = writer.Write(report)
		return err
	}
	if err := tarWriter.WriteHeader(&tar.Header{Name: archiveReportName, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(report))}); err != nil {
		return err
	}
	_, err := tarWriter.Write(report)
	return err
}

func copyExact(ctx context.Context, destination io.Writer, source io.Reader, size int64) (int64, error) {
	written, err := io.CopyN(destination, &contextReader{ctx: ctx, reader: source}, size)
	if err != nil {
		return written, err
	}
	var probe [1]byte
	count, probeErr := source.Read(probe[:])
	if count != 0 || (probeErr != nil && probeErr != io.EOF) {
		return written, ErrArchiveSource
	}
	return written, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
