package provider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"xirang/backend/internal/backupasset"
)

const (
	rcloneNativeMaximumPhysicalKeyBytes = 1024
	rcloneNativeMaximumVersionIDBytes   = 4096
)

type RcloneNativeVersionKind string

const (
	RcloneNativeObjectVersion RcloneNativeVersionKind = "object_version"
	RcloneNativeDeleteMarker  RcloneNativeVersionKind = "delete_marker"
)

type RcloneNativeVersionRecord struct {
	PhysicalKey       string
	VersionID         string
	Kind              RcloneNativeVersionKind
	IsLatest          bool
	Size              uint64
	LastModified      time.Time
	ContentDigest     string
	EncryptionProfile RcloneNativeEncryptionProfileCode
	KMSKeyDigest      string
	BucketKeyEnabled  bool
}

type RcloneNativeFullObservation struct {
	Records                 []RcloneNativeVersionRecord
	PageCount               int
	TerminalKeyMarker       string
	TerminalVersionIDMarker string
}

type RcloneNativeVersionPageRequest struct {
	Prefix          string
	KeyMarker       string
	VersionIDMarker string
	MaxKeys         int
}

type RcloneNativeVersionPage struct {
	Records             []RcloneNativeVersionRecord
	Truncated           bool
	NextKeyMarker       string
	NextVersionIDMarker string
}

type RcloneNativeVersionEnumerator interface {
	ListVersionPage(context.Context, RcloneNativeVersionPageRequest) (RcloneNativeVersionPage, error)
}

type RcloneNativeObservationLimits struct {
	PageSize   int
	MaxPages   int
	MaxRecords int
}

type RcloneNativeStableGraph struct {
	Records           []RcloneNativeVersionRecord
	Digest            string
	RecordCount       uint64
	ObjectCount       uint64
	DeleteMarkerCount uint64
	PageCount         int
}

func NewRcloneNativeStableGraph(first, second RcloneNativeFullObservation) (RcloneNativeStableGraph, error) {
	firstGraph, err := normalizeRcloneNativeObservation(first)
	if err != nil {
		return RcloneNativeStableGraph{}, err
	}
	secondGraph, err := normalizeRcloneNativeObservation(second)
	if err != nil {
		return RcloneNativeStableGraph{}, err
	}
	if !equalRcloneNativeStableGraphs(firstGraph, secondGraph) {
		return RcloneNativeStableGraph{}, rcloneNativeError(backupasset.RcloneReasonExternalWriterDetected, nil)
	}
	return firstGraph, nil
}

func ObserveRcloneNativeFullVersions(
	ctx context.Context,
	enumerator RcloneNativeVersionEnumerator,
	prefix string,
	limits RcloneNativeObservationLimits,
) (RcloneNativeFullObservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if enumerator == nil || !validRcloneNativePrefix(prefix) || limits.PageSize <= 0 || limits.PageSize > 1000 ||
		limits.MaxPages <= 0 || limits.MaxRecords <= 0 {
		return RcloneNativeFullObservation{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
	}
	observation := RcloneNativeFullObservation{Records: make([]RcloneNativeVersionRecord, 0)}
	request := RcloneNativeVersionPageRequest{Prefix: prefix, MaxKeys: limits.PageSize}
	for {
		if err := ctx.Err(); err != nil {
			return RcloneNativeFullObservation{}, rcloneNativeError(backupasset.RcloneReasonProviderTimeout, err)
		}
		if observation.PageCount >= limits.MaxPages {
			return RcloneNativeFullObservation{}, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, nil)
		}
		page, err := enumerator.ListVersionPage(ctx, request)
		if err != nil {
			return RcloneNativeFullObservation{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
		}
		observation.PageCount++
		if len(page.Records) > limits.PageSize || len(page.Records) > limits.MaxRecords-len(observation.Records) {
			return RcloneNativeFullObservation{}, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, nil)
		}
		for _, record := range page.Records {
			if !validRcloneNativeVersionRecord(record) || !strings.HasPrefix(record.PhysicalKey, prefix) {
				return RcloneNativeFullObservation{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
			}
		}
		observation.Records = append(observation.Records, page.Records...)
		if !page.Truncated {
			if page.NextKeyMarker != "" || page.NextVersionIDMarker != "" {
				return RcloneNativeFullObservation{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
			}
			return observation, nil
		}
		if len(page.Records) == 0 || !validRcloneNativePhysicalKey(page.NextKeyMarker) ||
			!validRcloneNativeVersionID(page.NextVersionIDMarker) || !strings.HasPrefix(page.NextKeyMarker, prefix) ||
			(page.NextKeyMarker == request.KeyMarker && page.NextVersionIDMarker == request.VersionIDMarker) {
			return RcloneNativeFullObservation{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
		}
		request.KeyMarker = page.NextKeyMarker
		request.VersionIDMarker = page.NextVersionIDMarker
	}
}

func CaptureRcloneNativeStableGraph(
	ctx context.Context,
	enumerator RcloneNativeVersionEnumerator,
	prefix string,
	limits RcloneNativeObservationLimits,
) (RcloneNativeStableGraph, error) {
	first, err := ObserveRcloneNativeFullVersions(ctx, enumerator, prefix, limits)
	if err != nil {
		return RcloneNativeStableGraph{}, err
	}
	second, err := ObserveRcloneNativeFullVersions(ctx, enumerator, prefix, limits)
	if err != nil {
		return RcloneNativeStableGraph{}, err
	}
	return NewRcloneNativeStableGraph(first, second)
}

type RcloneNativeOwnedMutation struct {
	PhysicalKey string
	VersionID   string
	Kind        RcloneNativeVersionKind
}

type RcloneNativePointViewEntry struct {
	LogicalPath       string
	PhysicalKey       string
	VersionID         string
	Kind              RcloneNativeVersionKind
	Size              uint64
	LastModified      time.Time
	ContentDigest     string
	EncryptionProfile RcloneNativeEncryptionProfileCode
	KMSKeyDigest      string
	BucketKeyEnabled  bool
}

type RcloneNativeMutationDisposition string

const (
	RcloneNativeMutationReferenced RcloneNativeMutationDisposition = "referenced"
	RcloneNativeMutationSuperseded RcloneNativeMutationDisposition = "superseded"
)

type RcloneNativeMutationLedgerEntry struct {
	LogicalPath       string
	PhysicalKey       string
	VersionID         string
	Kind              RcloneNativeVersionKind
	Size              uint64
	LastModified      time.Time
	Disposition       RcloneNativeMutationDisposition
	EncryptionProfile RcloneNativeEncryptionProfileCode
	KMSKeyDigest      string
	BucketKeyEnabled  bool
}

type RcloneNativePointGraph struct {
	View         []RcloneNativePointViewEntry
	Ledger       []RcloneNativeMutationLedgerEntry
	ViewDigest   string
	LedgerDigest string
}

func BuildRcloneNativePointGraph(b0, b1 RcloneNativeStableGraph, dataPrefix string, owned []RcloneNativeOwnedMutation) (RcloneNativePointGraph, error) {
	if !validRcloneNativePrefix(dataPrefix) || validateRcloneNativeStableGraph(b0) != nil || validateRcloneNativeStableGraph(b1) != nil {
		return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
	}

	b0ByIdentity := make(map[string]RcloneNativeVersionRecord, len(b0.Records))
	for _, record := range b0.Records {
		b0ByIdentity[rcloneNativeVersionIdentity(record.PhysicalKey, record.VersionID)] = record
	}
	b1ByIdentity := make(map[string]RcloneNativeVersionRecord, len(b1.Records))
	for _, record := range b1.Records {
		identity := rcloneNativeVersionIdentity(record.PhysicalKey, record.VersionID)
		b1ByIdentity[identity] = record
	}
	for identity, before := range b0ByIdentity {
		after, exists := b1ByIdentity[identity]
		if !exists || !equalRcloneNativeImmutableVersion(before, after) {
			return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
		}
	}

	ownedByIdentity := make(map[string]RcloneNativeOwnedMutation, len(owned))
	for _, mutation := range owned {
		if !validRcloneNativeVersionIdentity(mutation.PhysicalKey, mutation.VersionID, mutation.Kind) {
			return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
		}
		identity := rcloneNativeVersionIdentity(mutation.PhysicalKey, mutation.VersionID)
		if _, exists := ownedByIdentity[identity]; exists {
			return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
		}
		ownedByIdentity[identity] = mutation
	}

	delta := make([]RcloneNativeVersionRecord, 0, len(b1.Records))
	for _, record := range b1.Records {
		identity := rcloneNativeVersionIdentity(record.PhysicalKey, record.VersionID)
		if _, existed := b0ByIdentity[identity]; existed {
			continue
		}
		mutation, explained := ownedByIdentity[identity]
		if !explained || mutation.Kind != record.Kind {
			return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonExternalWriterDetected, nil)
		}
		delta = append(delta, record)
		delete(ownedByIdentity, identity)
	}
	if len(ownedByIdentity) != 0 {
		return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonExternalWriterDetected, nil)
	}

	point := RcloneNativePointGraph{
		View:   make([]RcloneNativePointViewEntry, 0),
		Ledger: make([]RcloneNativeMutationLedgerEntry, 0, len(delta)),
	}
	referenced := make(map[string]struct{})
	for _, record := range b1.Records {
		if !record.IsLatest || !strings.HasPrefix(record.PhysicalKey, dataPrefix) {
			continue
		}
		logicalPath, err := DecodeRcloneV1744S3Path(strings.TrimPrefix(record.PhysicalKey, dataPrefix))
		if err != nil {
			return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, err)
		}
		point.View = append(point.View, rcloneNativePointViewFromRecord(logicalPath, record))
		referenced[rcloneNativeVersionIdentity(record.PhysicalKey, record.VersionID)] = struct{}{}
	}
	sort.Slice(point.View, func(left, right int) bool {
		if point.View[left].LogicalPath != point.View[right].LogicalPath {
			return point.View[left].LogicalPath < point.View[right].LogicalPath
		}
		return point.View[left].VersionID < point.View[right].VersionID
	})
	for index := 1; index < len(point.View); index++ {
		if point.View[index-1].LogicalPath == point.View[index].LogicalPath {
			return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
		}
	}

	for _, record := range delta {
		logicalPath := ""
		if strings.HasPrefix(record.PhysicalKey, dataPrefix) {
			var err error
			logicalPath, err = DecodeRcloneV1744S3Path(strings.TrimPrefix(record.PhysicalKey, dataPrefix))
			if err != nil {
				return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, err)
			}
		}
		disposition := RcloneNativeMutationSuperseded
		if _, exists := referenced[rcloneNativeVersionIdentity(record.PhysicalKey, record.VersionID)]; exists {
			disposition = RcloneNativeMutationReferenced
		}
		point.Ledger = append(point.Ledger, RcloneNativeMutationLedgerEntry{
			LogicalPath: logicalPath, PhysicalKey: record.PhysicalKey, VersionID: record.VersionID, Kind: record.Kind,
			Size: record.Size, LastModified: record.LastModified, Disposition: disposition,
			EncryptionProfile: record.EncryptionProfile, KMSKeyDigest: record.KMSKeyDigest, BucketKeyEnabled: record.BucketKeyEnabled,
		})
	}
	sort.Slice(point.Ledger, func(left, right int) bool {
		if point.Ledger[left].PhysicalKey != point.Ledger[right].PhysicalKey {
			return point.Ledger[left].PhysicalKey < point.Ledger[right].PhysicalKey
		}
		if !point.Ledger[left].LastModified.Equal(point.Ledger[right].LastModified) {
			return point.Ledger[left].LastModified.Before(point.Ledger[right].LastModified)
		}
		return point.Ledger[left].VersionID < point.Ledger[right].VersionID
	})

	var err error
	point.ViewDigest, err = digestRcloneNativePointView(point.View)
	if err != nil {
		return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	point.LedgerDigest, err = digestRcloneNativeMutationLedger(point.Ledger)
	if err != nil {
		return RcloneNativePointGraph{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	return point, nil
}

type RcloneNativeExactReadRequest struct {
	PhysicalKey string
	VersionID   string
}

type RcloneNativeExactObjectHead struct {
	PhysicalKey       string
	VersionID         string
	Size              uint64
	EncryptionProfile RcloneNativeEncryptionProfileCode
	KMSKeyDigest      string
	BucketKeyEnabled  bool
}

type RcloneNativeBaselineObjectHead struct {
	PhysicalKey       string
	VersionID         string
	Size              uint64
	EncryptionProfile RcloneNativeEncryptionProfileCode
	KMSKeyARN         string `json:"-"`
	BucketKeyEnabled  bool
}

type RcloneNativeBaselineS3 interface {
	RcloneNativeVersionEnumerator
	HeadBaselineVersion(context.Context, RcloneNativeExactReadRequest) (RcloneNativeBaselineObjectHead, error)
	OpenBaselineVersion(context.Context, RcloneNativeExactReadRequest) (io.ReadCloser, error)
}

type RcloneNativeBaselineInventoryRequest struct {
	SourcePrefix      string `json:"-"`
	ObservationLimits RcloneNativeObservationLimits
	MaxReadBytes      uint64
}

type RcloneNativeBaselineInventoryObject struct {
	PhysicalKey       string                            `json:"-"`
	VersionID         string                            `json:"-"`
	Size              uint64                            `json:"-"`
	EncryptionProfile RcloneNativeEncryptionProfileCode `json:"-"`
	KMSKeyARN         string                            `json:"-"`
	BucketKeyEnabled  bool                              `json:"-"`
}

type RcloneNativeBaselineInventory struct {
	Digest           string                                `json:"digest"`
	ObjectCount      uint64                                `json:"object_count"`
	LogicalBytes     uint64                                `json:"logical_bytes"`
	SourcePrefix     string                                `json:"-"`
	SourceKMSKeyARNs []string                              `json:"-"`
	Objects          []RcloneNativeBaselineInventoryObject `json:"-"`
}

type RcloneNativeBaselineDiscovery struct {
	Digest           string                                `json:"digest"`
	ObjectCount      uint64                                `json:"object_count"`
	LogicalBytes     uint64                                `json:"logical_bytes"`
	SourcePrefix     string                                `json:"-"`
	SourceKMSKeyARNs []string                              `json:"-"`
	Objects          []RcloneNativeBaselineInventoryObject `json:"-"`
	graphDigest      string
}

type RcloneNativeBaselineSource struct {
	SourcePrefix      string               `json:"-"`
	PublicationSource RclonePrivateLocator `json:"-"`
}

func ResolveRcloneNativeBaselineSource(legacyLocator string, profile RcloneNativeProfile) (RcloneNativeBaselineSource, error) {
	if err := ValidateRcloneNativeProfile(profile); err != nil {
		return RcloneNativeBaselineSource{}, err
	}
	remote, target, hasRemote := strings.Cut(legacyLocator, ":")
	bucket, sourcePrefix, hasPrefix := strings.Cut(target, "/")
	if !hasRemote || !hasPrefix || !validRcloneRemoteName(remote) || bucket != profile.Bucket ||
		!validRcloneNativePrefix(sourcePrefix) || rcloneNativePrefixesOverlap(profile.ManagedPrefix, sourcePrefix) {
		return RcloneNativeBaselineSource{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
	}
	publicationSource, err := NewRclonePrivateLocator(rcloneNativeConfigRemote + ":" + bucket + "/" + sourcePrefix)
	if err != nil {
		return RcloneNativeBaselineSource{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, err)
	}
	return RcloneNativeBaselineSource{SourcePrefix: sourcePrefix, PublicationSource: publicationSource}, nil
}

func DiscoverRcloneNativeBaselineSource(
	ctx context.Context,
	client RcloneNativeBaselineS3,
	request RcloneNativeBaselineInventoryRequest,
) (RcloneNativeBaselineDiscovery, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil || !validRcloneNativePrefix(request.SourcePrefix) || request.MaxReadBytes == 0 ||
		request.ObservationLimits.PageSize <= 0 || request.ObservationLimits.MaxPages <= 0 || request.ObservationLimits.MaxRecords <= 0 {
		return RcloneNativeBaselineDiscovery{}, rcloneNativeError(backupasset.RcloneReasonUnsupportedProfile, nil)
	}
	graph, err := CaptureRcloneNativeStableGraph(ctx, client, request.SourcePrefix, request.ObservationLimits)
	if err != nil {
		return RcloneNativeBaselineDiscovery{}, err
	}
	discovery := RcloneNativeBaselineDiscovery{
		SourcePrefix: request.SourcePrefix, graphDigest: graph.Digest,
		Objects: make([]RcloneNativeBaselineInventoryObject, 0, graph.ObjectCount),
	}
	keySet := make(map[string]struct{})
	for _, record := range graph.Records {
		if !record.IsLatest || record.Kind == RcloneNativeDeleteMarker {
			continue
		}
		if record.Kind != RcloneNativeObjectVersion || record.Size > math.MaxInt64 ||
			record.Size > request.MaxReadBytes-discovery.LogicalBytes {
			return RcloneNativeBaselineDiscovery{}, rcloneNativeError(backupasset.RcloneReasonVerificationCostLimit, nil)
		}
		exact := RcloneNativeExactReadRequest{PhysicalKey: record.PhysicalKey, VersionID: record.VersionID}
		head, err := client.HeadBaselineVersion(ctx, exact)
		if err != nil {
			if rcloneNativeReason(err) != "" {
				return RcloneNativeBaselineDiscovery{}, err
			}
			return RcloneNativeBaselineDiscovery{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
		}
		if head.PhysicalKey != record.PhysicalKey || head.VersionID != record.VersionID || head.Size != record.Size {
			return RcloneNativeBaselineDiscovery{}, rcloneNativeError(backupasset.RcloneReasonSourceDrift, nil)
		}
		switch head.EncryptionProfile {
		case RcloneNativeSSES3V1:
			if head.KMSKeyARN != "" || head.BucketKeyEnabled {
				return RcloneNativeBaselineDiscovery{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
			}
		case RcloneNativeSSEKMSV1:
			if _, _, ok := parseRcloneNativeKMSKeyARN(head.KMSKeyARN); !ok {
				return RcloneNativeBaselineDiscovery{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
			}
			keySet[head.KMSKeyARN] = struct{}{}
		default:
			return RcloneNativeBaselineDiscovery{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		discovery.ObjectCount++
		discovery.LogicalBytes += record.Size
		discovery.Objects = append(discovery.Objects, RcloneNativeBaselineInventoryObject{
			PhysicalKey: record.PhysicalKey, VersionID: record.VersionID, Size: record.Size,
			EncryptionProfile: head.EncryptionProfile, KMSKeyARN: head.KMSKeyARN, BucketKeyEnabled: head.BucketKeyEnabled,
		})
	}
	for keyARN := range keySet {
		discovery.SourceKMSKeyARNs = append(discovery.SourceKMSKeyARNs, keyARN)
	}
	sort.Strings(discovery.SourceKMSKeyARNs)
	discovery.Digest, err = digestRcloneNativeBaselineMetadata(
		"baseline-source-discovery-v1", discovery.SourcePrefix, discovery.graphDigest,
		discovery.Objects, discovery.SourceKMSKeyARNs,
	)
	if err != nil {
		return RcloneNativeBaselineDiscovery{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	return discovery, nil
}

func InspectRcloneNativeBaselineSource(
	ctx context.Context,
	client RcloneNativeBaselineS3,
	request RcloneNativeBaselineInventoryRequest,
) (RcloneNativeBaselineInventory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	discovery, err := DiscoverRcloneNativeBaselineSource(ctx, client, request)
	if err != nil {
		return RcloneNativeBaselineInventory{}, err
	}
	inventory := RcloneNativeBaselineInventory{
		ObjectCount: discovery.ObjectCount, LogicalBytes: discovery.LogicalBytes,
		SourcePrefix: discovery.SourcePrefix, SourceKMSKeyARNs: append([]string(nil), discovery.SourceKMSKeyARNs...),
		Objects: append([]RcloneNativeBaselineInventoryObject(nil), discovery.Objects...),
	}
	for _, object := range inventory.Objects {
		exact := RcloneNativeExactReadRequest{PhysicalKey: object.PhysicalKey, VersionID: object.VersionID}
		body, err := client.OpenBaselineVersion(ctx, exact)
		if err != nil || body == nil {
			return RcloneNativeBaselineInventory{}, rcloneNativeError(backupasset.RcloneReasonKMSPermissionDenied, err)
		}
		readBytes, readErr := io.Copy(io.Discard, io.LimitReader(body, int64(object.Size)+1))
		closeErr := body.Close()
		if readErr != nil || closeErr != nil || readBytes != int64(object.Size) {
			return RcloneNativeBaselineInventory{}, rcloneNativeError(backupasset.RcloneReasonKMSPermissionDenied, errors.Join(readErr, closeErr))
		}
	}
	inventory.Digest, err = digestRcloneNativeBaselineMetadata(
		"baseline-source-inventory-v1", inventory.SourcePrefix, discovery.graphDigest,
		inventory.Objects, inventory.SourceKMSKeyARNs,
	)
	if err != nil {
		return RcloneNativeBaselineInventory{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	return inventory, nil
}

func digestRcloneNativeBaselineMetadata(
	domain, sourcePrefix, graphDigest string,
	objects []RcloneNativeBaselineInventoryObject,
	sourceKMSKeyARNs []string,
) (string, error) {
	type digestObject struct {
		PhysicalKey       string
		VersionID         string
		Size              uint64
		EncryptionProfile RcloneNativeEncryptionProfileCode
		KMSKeyARN         string
		BucketKeyEnabled  bool
	}
	digestObjects := make([]digestObject, 0, len(objects))
	for _, object := range objects {
		digestObjects = append(digestObjects, digestObject(object))
	}
	return canonicalRcloneNativeDigest(domain, struct {
		SourcePrefix     string
		GraphDigest      string
		Objects          []digestObject
		SourceKMSKeyARNs []string
	}{
		SourcePrefix: sourcePrefix, GraphDigest: graphDigest,
		Objects: digestObjects, SourceKMSKeyARNs: sourceKMSKeyARNs,
	})
}

type RcloneNativeKMSKeyDigestBinding struct {
	KeyARN string `json:"-"`
	Digest string
}

type RcloneNativeControlWriteRequest struct {
	PhysicalKey       string
	Payload           []byte `json:"-"`
	MaxBytes          uint64
	EncryptionProfile RcloneNativeEncryptionProfileCode
	KMSKeyARN         string `json:"-"`
	KMSKeyDigest      string
	BucketKeyEnabled  bool
}

type RcloneNativeControlWriteResult struct {
	VersionID string
}

type RcloneNativeControlWriter interface {
	PutControlVersion(context.Context, RcloneNativeControlWriteRequest) (RcloneNativeControlWriteResult, error)
}

type RcloneNativeControlStore interface {
	RcloneNativeControlWriter
	RcloneNativeExactReader
}

type RcloneNativeControlPayload struct {
	PhysicalKey string
	Payload     []byte `json:"-"`
}

type RcloneNativeControlCommitRequest struct {
	ManifestChunks    []RcloneNativeControlPayload
	ManifestIndex     RcloneNativeControlPayload
	Commit            RcloneNativeControlPayload
	EncryptionProfile RcloneNativeEncryptionProfileCode
	KMSKeyARN         string `json:"-"`
	KMSKeyDigest      string
	BucketKeyEnabled  bool
	MaxObjectBytes    uint64
}

type RcloneNativeControlObjectVersion struct {
	PhysicalKey       string
	VersionID         string
	Size              uint64
	ContentDigest     string
	EncryptionProfile RcloneNativeEncryptionProfileCode
	KMSKeyDigest      string
	BucketKeyEnabled  bool
	EvidenceDigest    string
}

type RcloneNativeControlCommitGraph struct {
	ManifestVersions []RcloneNativeControlObjectVersion
	IndexVersion     RcloneNativeControlObjectVersion
	CommitVersion    RcloneNativeControlObjectVersion
	Digest           string
}

type RcloneNativeDataPlane interface {
	ObserveSource(context.Context, RcloneNativePublicationRequest) (RcloneManifestBundle, error)
	Sync(context.Context, RcloneNativePublicationRequest) error
	VerifyFullBytes(context.Context, RcloneNativePublicationRequest, uint64) (RcloneFullByteProof, error)
}

type CommandRcloneNativeDataPlane struct {
	commands     CommandTransport
	limitsSource OperationLimitsSource
}

func NewCommandRcloneNativeDataPlane(commands CommandTransport, limitsSource OperationLimitsSource) (*CommandRcloneNativeDataPlane, error) {
	if commands == nil {
		return nil, fmt.Errorf("%w: native Rclone data plane unavailable", backupasset.ErrInvalidState)
	}
	if _, err := resolveOperationLimits(limitsSource); err != nil {
		return nil, err
	}
	return &CommandRcloneNativeDataPlane{commands: commands, limitsSource: limitsSource}, nil
}

func (dataPlane *CommandRcloneNativeDataPlane) ObserveSource(ctx context.Context, request RcloneNativePublicationRequest) (RcloneManifestBundle, error) {
	if dataPlane == nil || dataPlane.commands == nil {
		return RcloneManifestBundle{}, fmt.Errorf("%w: native Rclone data plane unavailable", backupasset.ErrInvalidState)
	}
	limits, err := resolveOperationLimits(dataPlane.limitsSource)
	if err != nil {
		return RcloneManifestBundle{}, err
	}
	handle, err := dataPlane.commands.Open(
		ctx,
		dataPlane.invocation(request, OperationRcloneManagedRecursiveList, &request.Source, nil),
		limits,
		request.ManifestOptions.SpoolMaxBytes,
	)
	if err != nil {
		return RcloneManifestBundle{}, err
	}
	options := request.ManifestOptions
	options.SymlinkTargetReader = func(readContext context.Context, physicalPath string, maxBytes int64) ([]byte, error) {
		locator, err := joinRclonePrivateLocator(request.Source, physicalPath)
		if err != nil {
			return nil, err
		}
		return dataPlane.readSourceObject(readContext, request, locator, maxBytes)
	}
	manifest, buildErr := BuildRcloneManifestV1(ctx, handle, options)
	closeErr := handle.Close()
	if buildErr != nil {
		return RcloneManifestBundle{}, buildErr
	}
	if closeErr != nil {
		return RcloneManifestBundle{}, closeErr
	}
	return manifest, nil
}

func (dataPlane *CommandRcloneNativeDataPlane) Sync(ctx context.Context, request RcloneNativePublicationRequest) error {
	if dataPlane == nil || dataPlane.commands == nil {
		return fmt.Errorf("%w: native Rclone data plane unavailable", backupasset.ErrInvalidState)
	}
	destination, err := rcloneNativeManagedDataLocator(request.Profile)
	if err != nil {
		return err
	}
	limits, err := resolveOperationLimits(dataPlane.limitsSource)
	if err != nil {
		return err
	}
	_, err = dataPlane.commands.Run(ctx, dataPlane.invocation(request, OperationRcloneManagedNativeSync, &request.Source, &destination), limits)
	return err
}

func (dataPlane *CommandRcloneNativeDataPlane) VerifyFullBytes(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	expectedBytes uint64,
) (RcloneFullByteProof, error) {
	if dataPlane == nil || dataPlane.commands == nil || expectedBytes > request.MaxVerifyBytes {
		return RcloneFullByteProof{}, rcloneNativeError(backupasset.RcloneReasonVerificationCostLimit, nil)
	}
	destination, err := rcloneNativeManagedDataLocator(request.Profile)
	if err != nil {
		return RcloneFullByteProof{}, err
	}
	limits, err := resolveOperationLimits(dataPlane.limitsSource)
	if err != nil {
		return RcloneFullByteProof{}, err
	}
	_, err = dataPlane.commands.Run(ctx, dataPlane.invocation(request, OperationRcloneManagedCheckDownload, &request.Source, &destination), limits)
	if err != nil {
		return RcloneFullByteProof{}, err
	}
	return RcloneFullByteProof{
		SourceDigest: request.Manifest.ObservationDigest, DestinationDigest: request.Manifest.ObservationDigest,
		VerifiedBytes: expectedBytes, Complete: true,
	}, nil
}

func (dataPlane *CommandRcloneNativeDataPlane) readSourceObject(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	locator RclonePrivateLocator,
	maxBytes int64,
) ([]byte, error) {
	limits, err := resolveOperationLimits(dataPlane.limitsSource)
	if err != nil {
		return nil, err
	}
	handle, err := dataPlane.commands.Open(ctx, dataPlane.invocation(request, OperationRcloneManagedCat, &locator, nil), limits, maxBytes)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(handle, maxBytes+1))
	closeErr := handle.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) > maxBytes {
		return nil, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, nil)
	}
	return payload, nil
}

func (*CommandRcloneNativeDataPlane) invocation(
	request RcloneNativePublicationRequest,
	operation CommandOperation,
	source, destination *RclonePrivateLocator,
) CommandInvocation {
	return CommandInvocation{
		Tool: ToolRclone, Operation: operation, Purpose: CommandPurposePublish,
		SecretStdin: append([]byte(nil), request.RcloneConfig...), Runtime: &request.Runtime,
		RcloneSource: source, RcloneDestination: destination, RcloneLowLevelRetries: request.LowLevelRetries,
		AbsoluteDeadline: request.Attempt.PointDeadlineAt,
	}
}

func rcloneNativeManagedDataLocator(profile RcloneNativeProfile) (RclonePrivateLocator, error) {
	if err := ValidateRcloneNativeProfile(profile); err != nil {
		return RclonePrivateLocator{}, err
	}
	value := rcloneNativeConfigRemote + ":" + profile.Bucket + "/" + strings.TrimSuffix(profile.ManagedPrefix, "/") + "/data"
	return NewRclonePrivateLocator(value)
}

type RcloneNativePublicationRequest struct {
	Attempt                  RcloneAttemptV1
	Profile                  RcloneNativeProfile
	Session                  RcloneNativeSession       `json:"-"`
	ClientFactory            RcloneNativeClientFactory `json:"-"`
	s3                       S3Native
	Source                   RclonePrivateLocator `json:"-"`
	RcloneConfig             []byte               `json:"-"`
	Runtime                  RemoteCommandAccess  `json:"-"`
	Manifest                 RcloneManifestBundle
	ManifestOptions          RcloneManifestBuildOptions
	ObservationLimits        RcloneNativeObservationLimits
	Encryption               RcloneNativeEncryptionSelection
	EncryptionEvidence       RcloneNativeEncryptionEvidence
	KMSKeyBindings           []RcloneNativeKMSKeyDigestBinding `json:"-"`
	MarkerKey                []byte                            `json:"-"`
	ExactCommitKey           string                            `json:"-"`
	ExactCommitVersionID     string                            `json:"-"`
	CapabilityEvidenceDigest string
	CostEvidenceDigest       string
	MaxVerifyBytes           uint64
	ControlPayloadMaxBytes   uint64
	LowLevelRetries          int
}

type RcloneNativePublisher struct {
	dataPlane RcloneNativeDataPlane
	now       func() time.Time
}

func NewRcloneNativePublisher(dataPlane RcloneNativeDataPlane, now func() time.Time) *RcloneNativePublisher {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RcloneNativePublisher{dataPlane: dataPlane, now: now}
}

func (publisher *RcloneNativePublisher) Publish(ctx context.Context, request RcloneNativePublicationRequest) (RcloneCommitV1, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if publisher == nil || publisher.dataPlane == nil || publisher.now == nil {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, nil)
	}
	now := publisher.now().UTC()
	if err := request.validate(now); err != nil {
		return RcloneCommitV1{}, err
	}
	s3, err := request.ClientFactory.S3(request.Session, request.Profile, request.KMSKeyBindings)
	if err != nil || s3 == nil {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, err)
	}
	request.s3 = s3

	sourceBefore, err := publisher.dataPlane.ObserveSource(ctx, request)
	if err != nil {
		return RcloneCommitV1{}, rcloneNativeDataPlaneError(ctx, err)
	}
	if !equalRcloneManifestBundleIdentity(sourceBefore, request.Manifest) {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonSourceDrift, nil)
	}

	b0, err := CaptureRcloneNativeStableGraph(ctx, request.s3, request.Profile.ManagedPrefix, request.ObservationLimits)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	if b0.Digest != request.Attempt.Native.B0VersionGraphDigest {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonExternalWriterDetected, nil)
	}
	controlPrefix := rcloneNativeAttemptControlPrefix(request)
	startVersion, err := publisher.writeAttemptMarker(ctx, request, controlPrefix+"start.json", "start")
	if err != nil {
		return RcloneCommitV1{}, err
	}
	if err := publisher.dataPlane.Sync(ctx, request); err != nil {
		return RcloneCommitV1{}, rcloneNativeDataPlaneError(ctx, err)
	}
	sourceAfter, err := publisher.dataPlane.ObserveSource(ctx, request)
	if err != nil {
		return RcloneCommitV1{}, rcloneNativeDataPlaneError(ctx, err)
	}
	if !equalRcloneManifestBundleIdentity(sourceBefore, sourceAfter) {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonSourceDrift, nil)
	}
	fullByteProof := RcloneFullByteProof{}
	if request.Manifest.Fidelity.RequiresFullByteVerification {
		if request.Manifest.LogicalBytes > request.MaxVerifyBytes {
			return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonVerificationCostLimit, nil)
		}
		fullByteProof, err = publisher.dataPlane.VerifyFullBytes(ctx, request, request.Manifest.LogicalBytes)
		if err != nil {
			return RcloneCommitV1{}, rcloneNativeDataPlaneError(ctx, err)
		}
		if !fullByteProof.Complete || fullByteProof.VerifiedBytes != request.Manifest.LogicalBytes {
			return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
		}
	}
	endVersion, err := publisher.writeAttemptMarker(ctx, request, controlPrefix+"end.json", "end")
	if err != nil {
		return RcloneCommitV1{}, err
	}
	b1, err := CaptureRcloneNativeStableGraph(ctx, request.s3, request.Profile.ManagedPrefix, request.ObservationLimits)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	owned, err := attributeRcloneNativeAttemptMutations(request, b0, b1, startVersion, endVersion)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	point, err := BuildRcloneNativePointGraph(b0, b1, request.Profile.ManagedPrefix+"data/", owned)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	sourceEntries, err := decodeRcloneNativeSourceManifest(request.Manifest)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	exactProofDigest, err := enrichAndVerifyRcloneNativePoint(ctx, request, sourceEntries, &point, fullByteProof)
	if err != nil {
		return RcloneCommitV1{}, err
	}

	committedAt := publisher.now().UTC()
	controlRequest, chunkDigests, manifestIndexDigest, commitMarker, err := buildRcloneNativeControlCommitRequest(
		request, controlPrefix, sourceEntries, point, exactProofDigest, b0, b1, committedAt,
	)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	controlGraph, err := publishRcloneNativeBoundControlCommit(ctx, request.s3, controlRequest, commitMarker, request.MarkerKey)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	if err := verifyUniqueRcloneNativeCommitVersion(ctx, request, controlPrefix, controlGraph.CommitVersion); err != nil {
		return RcloneCommitV1{}, err
	}
	commit, err := buildRcloneNativeProviderCommit(
		request, point, exactProofDigest, b0, b1, controlGraph, chunkDigests, manifestIndexDigest, committedAt,
	)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	return commit, nil
}

func (publisher *RcloneNativePublisher) Reconcile(ctx context.Context, request RcloneNativePublicationRequest) (RcloneCommitV1, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if publisher == nil || publisher.dataPlane == nil || publisher.now == nil {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, nil)
	}
	if err := request.validateForReconcile(publisher.now().UTC()); err != nil {
		return RcloneCommitV1{}, err
	}
	s3, err := request.ClientFactory.S3(request.Session, request.Profile, request.KMSKeyBindings)
	if err != nil || s3 == nil {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, err)
	}
	request.s3 = s3
	controlPrefix := rcloneNativeAttemptControlPrefix(request)
	commitKey := controlPrefix + "commit.json"
	var candidate RcloneNativeVersionRecord
	if request.ExactCommitKey != "" {
		head, headErr := request.s3.HeadVersion(ctx, RcloneNativeExactReadRequest{
			PhysicalKey: request.ExactCommitKey, VersionID: request.ExactCommitVersionID,
		})
		if headErr != nil || head.PhysicalKey != request.ExactCommitKey || head.VersionID != request.ExactCommitVersionID ||
			head.Size == 0 || head.Size > request.ControlPayloadMaxBytes {
			return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, headErr)
		}
		candidate = RcloneNativeVersionRecord{
			PhysicalKey: head.PhysicalKey, VersionID: head.VersionID, Kind: RcloneNativeObjectVersion, Size: head.Size,
			EncryptionProfile: head.EncryptionProfile, KMSKeyDigest: head.KMSKeyDigest, BucketKeyEnabled: head.BucketKeyEnabled,
		}
	} else {
		graph, captureErr := CaptureRcloneNativeStableGraph(ctx, request.s3, controlPrefix, request.ObservationLimits)
		if captureErr != nil {
			return RcloneCommitV1{}, captureErr
		}
		candidateCount := 0
		for _, record := range graph.Records {
			if record.PhysicalKey != commitKey {
				continue
			}
			candidate = record
			candidateCount++
		}
		if candidateCount != 1 || candidate.Kind != RcloneNativeObjectVersion || !candidate.IsLatest {
			return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
		}
	}
	commitPayload, commitVersion, err := readRcloneNativeCommitCandidate(ctx, request, candidate)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	marker, err := decodeRcloneNativeCommitMarker(commitPayload, request.MarkerKey)
	if err != nil || validateRcloneNativeCommitMarker(request, controlPrefix, marker) != nil {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	controlGraph, index, chunkDigests, err := reopenRcloneNativeControlGraph(ctx, request, marker, commitVersion)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	if emptyRcloneManifestBundle(request.Manifest) {
		request.Manifest = RcloneManifestBundle{
			Version: 1, IndexDigest: index.SourceManifestIndexDigest, ObservationDigest: index.SourceObservationDigest,
			EntryCount: index.EntryCount, LogicalBytes: index.LogicalBytes,
		}
	}
	point := RcloneNativePointGraph{ViewDigest: marker.PointViewDigest, LedgerDigest: marker.MutationLedgerDigest}
	b0 := RcloneNativeStableGraph{Digest: marker.B0VersionGraphDigest}
	b1 := RcloneNativeStableGraph{Digest: marker.B1VersionGraphDigest}
	commit, err := buildRcloneNativeProviderCommit(
		request, point, marker.ExactReadProofDigest, b0, b1, controlGraph, chunkDigests,
		marker.ManifestIndexDigest, marker.ProviderCommittedAt,
	)
	if err != nil {
		return RcloneCommitV1{}, err
	}
	if commit.Native == nil || commit.Native.EncryptionEvidenceDigest != marker.EncryptionEvidenceDigest {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	commit.FidelityEvidenceDigest = marker.FidelityEvidenceDigest
	commit.CostEvidenceDigest = marker.CostEvidenceDigest
	commit.CapabilityEvidenceDigest = marker.CapabilityEvidenceDigest
	commit.Native.EncryptionEvidenceDigest = marker.EncryptionEvidenceDigest
	if err := commit.Validate(); err != nil {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	return commit, nil
}

func readRcloneNativeCommitCandidate(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	candidate RcloneNativeVersionRecord,
) ([]byte, RcloneNativeControlObjectVersion, error) {
	readRequest := RcloneNativeExactReadRequest{PhysicalKey: candidate.PhysicalKey, VersionID: candidate.VersionID}
	head, err := request.s3.HeadVersion(ctx, readRequest)
	if err != nil || head.PhysicalKey != candidate.PhysicalKey || head.VersionID != candidate.VersionID || head.Size != candidate.Size ||
		head.Size > request.ControlPayloadMaxBytes {
		return nil, RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	if err := validateRcloneNativeHeadEncryption(request, head); err != nil {
		return nil, RcloneNativeControlObjectVersion{}, err
	}
	body, err := request.s3.OpenVersion(ctx, readRequest)
	if err != nil || body == nil {
		return nil, RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(body, int64(request.ControlPayloadMaxBytes)+1))
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || uint64(len(payload)) != head.Size || uint64(len(payload)) > request.ControlPayloadMaxBytes {
		if readErr == nil {
			readErr = closeErr
		}
		return nil, RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, readErr)
	}
	entry := RcloneNativePointViewEntry{
		PhysicalKey: candidate.PhysicalKey, VersionID: candidate.VersionID, Kind: RcloneNativeObjectVersion,
		Size: head.Size, ContentDigest: sha256Hex(payload), EncryptionProfile: head.EncryptionProfile,
		KMSKeyDigest: head.KMSKeyDigest, BucketKeyEnabled: head.BucketKeyEnabled,
	}
	proofDigest, err := digestRcloneNativeExactProof(entry, entry.ContentDigest)
	if err != nil {
		return nil, RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	return payload, RcloneNativeControlObjectVersion{
		PhysicalKey: entry.PhysicalKey, VersionID: entry.VersionID, Size: entry.Size, ContentDigest: entry.ContentDigest,
		EncryptionProfile: entry.EncryptionProfile, KMSKeyDigest: entry.KMSKeyDigest,
		BucketKeyEnabled: entry.BucketKeyEnabled, EvidenceDigest: proofDigest,
	}, nil
}

func decodeRcloneNativeCommitMarker(payload, markerKey []byte) (rcloneNativeCommitMarkerV1, error) {
	if len(payload) == 0 || rejectDuplicateJSONMembers(string(payload)) != nil || len(markerKey) < 32 {
		return rcloneNativeCommitMarkerV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var envelope rcloneAuthenticatedControlV1
	if err := decoder.Decode(&envelope); err != nil || envelope.Version != 1 || envelope.Kind != "commit" || !lowerHex(envelope.Authentication, 64) {
		return rcloneNativeCommitMarkerV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return rcloneNativeCommitMarkerV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	_, _, authentication, err := encodeRcloneAuthenticatedControl("commit", envelope.Document, markerKey)
	if err != nil || !hmac.Equal([]byte(authentication), []byte(envelope.Authentication)) || rejectDuplicateJSONMembers(string(envelope.Document)) != nil {
		return rcloneNativeCommitMarkerV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	documentDecoder := json.NewDecoder(bytes.NewReader(envelope.Document))
	documentDecoder.DisallowUnknownFields()
	var marker rcloneNativeCommitMarkerV1
	if err := documentDecoder.Decode(&marker); err != nil {
		return rcloneNativeCommitMarkerV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	if err := documentDecoder.Decode(&struct{}{}); err != io.EOF {
		return rcloneNativeCommitMarkerV1{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	return marker, nil
}

func validateRcloneNativeCommitMarker(
	request RcloneNativePublicationRequest,
	controlPrefix string,
	marker rcloneNativeCommitMarkerV1,
) error {
	if marker.Version != 1 || marker.RepositoryID != request.Attempt.RepositoryID ||
		marker.TaskRepositoryLinkID != request.Attempt.TaskRepositoryLinkID || marker.RecoveryPointID != request.Attempt.RecoveryPointID ||
		marker.AttemptID != request.Attempt.AttemptID || !marker.PointDeadlineAt.Equal(request.Attempt.PointDeadlineAt) ||
		!validRcloneNativeUTCTime(marker.ProviderCommittedAt) || marker.ProviderCommittedAt.After(marker.PointDeadlineAt) ||
		marker.ChildFenceDigest != request.Attempt.ChildFenceDigest || marker.B0VersionGraphDigest != request.Attempt.Native.B0VersionGraphDigest ||
		!validRcloneNativeDigest(marker.ManifestIndexDigest) || !validRcloneNativeDigest(marker.SourceManifestIndexDigest) ||
		!validRcloneNativeDigest(marker.SourceObservationDigest) || !validRcloneNativeDigest(marker.FidelityEvidenceDigest) ||
		!validRcloneNativeDigest(marker.CostEvidenceDigest) || !validRcloneNativeDigest(marker.CapabilityEvidenceDigest) ||
		!validRcloneNativeDigest(marker.EncryptionEvidenceDigest) || marker.CapabilityEvidenceDigest != request.CapabilityEvidenceDigest ||
		marker.CostEvidenceDigest != request.CostEvidenceDigest || !validRcloneNativeDigest(marker.PointViewDigest) ||
		!validRcloneNativeDigest(marker.MutationLedgerDigest) || !validRcloneNativeDigest(marker.B1VersionGraphDigest) ||
		!validRcloneNativeDigest(marker.ExactReadProofDigest) || !validRcloneNativeDigest(marker.PrecommitGraphDigest) || len(marker.ManifestVersions) == 0 {
		return rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	manifest := make([]RcloneNativeControlObjectVersion, len(marker.ManifestVersions))
	for index, reference := range marker.ManifestVersions {
		version, err := rcloneNativeControlVersionFromReference(reference)
		if err != nil || path.Dir(version.PhysicalKey)+"/" != controlPrefix || path.Base(version.PhysicalKey) != fmt.Sprintf("manifest-%06d.jsonl", index) {
			return rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
		}
		manifest[index] = version
	}
	indexVersion, err := rcloneNativeControlVersionFromReference(marker.IndexVersion)
	if err != nil || indexVersion.PhysicalKey != controlPrefix+"manifest-index.json" || indexVersion.ContentDigest != marker.ManifestIndexDigest {
		return rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	digest, err := digestRcloneNativeControlPrecommit(manifest, indexVersion)
	if err != nil || digest != marker.PrecommitGraphDigest {
		return rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	return nil
}

func rcloneNativeControlVersionFromReference(reference rcloneNativeControlVersionReferenceV1) (RcloneNativeControlObjectVersion, error) {
	value := RcloneNativeControlObjectVersion(reference)
	if !validRcloneNativePhysicalKey(value.PhysicalKey) || !validRcloneNativeVersionID(value.VersionID) ||
		!validRcloneNativeDigest(value.ContentDigest) || !validRcloneNativeDigest(value.EvidenceDigest) {
		return RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	return value, nil
}

func reopenRcloneNativeControlGraph(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	marker rcloneNativeCommitMarkerV1,
	commitVersion RcloneNativeControlObjectVersion,
) (RcloneNativeControlCommitGraph, rcloneNativeManifestIndexV1, []string, error) {
	graph := RcloneNativeControlCommitGraph{ManifestVersions: make([]RcloneNativeControlObjectVersion, len(marker.ManifestVersions)), CommitVersion: commitVersion}
	manifestPayloads := make([][]byte, len(marker.ManifestVersions))
	for index, reference := range marker.ManifestVersions {
		payload, version, err := reopenRcloneNativeControlVersion(ctx, request, reference)
		if err != nil {
			return RcloneNativeControlCommitGraph{}, rcloneNativeManifestIndexV1{}, nil, err
		}
		manifestPayloads[index] = payload
		graph.ManifestVersions[index] = version
	}
	indexPayload, indexVersion, err := reopenRcloneNativeControlVersion(ctx, request, marker.IndexVersion)
	if err != nil {
		return RcloneNativeControlCommitGraph{}, rcloneNativeManifestIndexV1{}, nil, err
	}
	graph.IndexVersion = indexVersion
	graph.Digest, err = digestRcloneNativeControlGraph(graph)
	if err != nil {
		return RcloneNativeControlCommitGraph{}, rcloneNativeManifestIndexV1{}, nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	index, chunkDigests, err := decodeAndValidateRcloneNativeManifestIndex(request, marker, indexPayload, manifestPayloads)
	if err != nil {
		return RcloneNativeControlCommitGraph{}, rcloneNativeManifestIndexV1{}, nil, err
	}
	return graph, index, chunkDigests, nil
}

func reopenRcloneNativeControlVersion(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	reference rcloneNativeControlVersionReferenceV1,
) ([]byte, RcloneNativeControlObjectVersion, error) {
	version, err := rcloneNativeControlVersionFromReference(reference)
	if err != nil || version.Size > request.ControlPayloadMaxBytes {
		return nil, RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	entry := RcloneNativePointViewEntry{
		PhysicalKey: version.PhysicalKey, VersionID: version.VersionID, Kind: RcloneNativeObjectVersion,
		Size: version.Size, ContentDigest: version.ContentDigest, EncryptionProfile: version.EncryptionProfile,
		KMSKeyDigest: version.KMSKeyDigest, BucketKeyEnabled: version.BucketKeyEnabled,
	}
	proof, err := VerifyRcloneNativeExactObject(ctx, request.s3, entry, request.ControlPayloadMaxBytes)
	if err != nil || proof.Digest != version.EvidenceDigest {
		return nil, RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	body, err := request.s3.OpenVersion(ctx, RcloneNativeExactReadRequest{PhysicalKey: version.PhysicalKey, VersionID: version.VersionID})
	if err != nil || body == nil {
		return nil, RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(body, int64(version.Size)+1))
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || uint64(len(payload)) != version.Size || sha256Hex(payload) != version.ContentDigest {
		if readErr == nil {
			readErr = closeErr
		}
		return nil, RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, readErr)
	}
	return payload, version, nil
}

func decodeAndValidateRcloneNativeManifestIndex(
	request RcloneNativePublicationRequest,
	marker rcloneNativeCommitMarkerV1,
	payload []byte,
	manifestPayloads [][]byte,
) (rcloneNativeManifestIndexV1, []string, error) {
	if sha256Hex(payload) != marker.ManifestIndexDigest || rejectDuplicateJSONMembers(string(payload)) != nil {
		return rcloneNativeManifestIndexV1{}, nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var index rcloneNativeManifestIndexV1
	if err := decoder.Decode(&index); err != nil {
		return rcloneNativeManifestIndexV1{}, nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return rcloneNativeManifestIndexV1{}, nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	if index.Version != 1 || index.ManifestSchema != "rclone-native-manifest-v1" ||
		index.SourceManifestIndexDigest != marker.SourceManifestIndexDigest || index.SourceObservationDigest != marker.SourceObservationDigest ||
		index.EntryCount != marker.ManifestEntryCount || index.LogicalBytes != marker.LogicalBytes ||
		index.PointViewDigest != marker.PointViewDigest || index.MutationLedgerDigest != marker.MutationLedgerDigest ||
		index.B0VersionGraphDigest != marker.B0VersionGraphDigest || index.B1VersionGraphDigest != marker.B1VersionGraphDigest ||
		index.ExactReadProofDigest != marker.ExactReadProofDigest || len(index.Chunks) != len(manifestPayloads) {
		return rcloneNativeManifestIndexV1{}, nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	if !emptyRcloneManifestBundle(request.Manifest) &&
		(index.SourceManifestIndexDigest != request.Manifest.IndexDigest || index.SourceObservationDigest != request.Manifest.ObservationDigest ||
			index.EntryCount != request.Manifest.EntryCount || index.LogicalBytes != request.Manifest.LogicalBytes) {
		return rcloneNativeManifestIndexV1{}, nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	digests := make([]string, len(index.Chunks))
	for position, chunk := range index.Chunks {
		if chunk.Ordinal != position || chunk.RecordCount == 0 || chunk.Size != uint64(len(manifestPayloads[position])) ||
			chunk.Digest != sha256Hex(manifestPayloads[position]) || chunk.Digest != marker.ManifestVersions[position].ContentDigest {
			return rcloneNativeManifestIndexV1{}, nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
		}
		digests[position] = chunk.Digest
	}
	return index, digests, nil
}

type rcloneNativeAttemptMarkerV1 struct {
	Version              int       `json:"version"`
	Phase                string    `json:"phase"`
	RepositoryID         string    `json:"repository_id"`
	TaskRepositoryLinkID string    `json:"task_repository_link_id"`
	RecoveryPointID      string    `json:"recovery_point_id"`
	AttemptID            string    `json:"attempt_id"`
	PointDeadlineAt      time.Time `json:"point_deadline_at"`
	ChildFenceDigest     string    `json:"child_fence_digest"`
}

func (publisher *RcloneNativePublisher) writeAttemptMarker(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	physicalKey string,
	phase string,
) (RcloneNativeControlObjectVersion, error) {
	document, err := json.Marshal(rcloneNativeAttemptMarkerV1{
		Version: 1, Phase: phase, RepositoryID: request.Attempt.RepositoryID,
		TaskRepositoryLinkID: request.Attempt.TaskRepositoryLinkID, RecoveryPointID: request.Attempt.RecoveryPointID,
		AttemptID: request.Attempt.AttemptID, PointDeadlineAt: request.Attempt.PointDeadlineAt,
		ChildFenceDigest: request.Attempt.ChildFenceDigest,
	})
	if err != nil {
		return RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	payload, _, _, err := encodeRcloneAuthenticatedControl("attempt", document, request.MarkerKey)
	if err != nil {
		return RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	commitRequest := RcloneNativeControlCommitRequest{
		EncryptionProfile: request.Encryption.Profile, KMSKeyARN: request.Encryption.ActiveKeyARN,
		KMSKeyDigest: request.EncryptionEvidence.ActiveKeyDigest, BucketKeyEnabled: request.EncryptionEvidence.BucketKeyEnabled,
		MaxObjectBytes: request.ControlPayloadMaxBytes,
	}
	return writeAndVerifyRcloneNativeControlObject(ctx, request.s3, commitRequest, RcloneNativeControlPayload{PhysicalKey: physicalKey, Payload: payload})
}

func (request RcloneNativePublicationRequest) validate(now time.Time) error {
	return request.validateAt(now, true)
}

func (request RcloneNativePublicationRequest) validateForReconcile(now time.Time) error {
	return request.validateAt(now, false)
}

func (request RcloneNativePublicationRequest) validateAt(now time.Time, requireFutureDeadline bool) error {
	if request.Attempt.Validate() != nil || request.Attempt.PublicationMode != backupasset.PublicationNativeObjectVersions || request.Attempt.Native == nil ||
		ValidateRcloneNativeProfile(request.Profile) != nil || request.Profile.Code != request.Attempt.Native.ProfileCode || request.ClientFactory == nil ||
		!request.Session.valid() || !request.Session.ExpiresAt().After(now) ||
		request.ObservationLimits.PageSize <= 0 || request.ObservationLimits.MaxPages <= 0 ||
		request.ObservationLimits.MaxRecords <= 0 || len(request.MarkerKey) < 32 || !validRcloneNativeDigest(request.CapabilityEvidenceDigest) ||
		!validRcloneNativeDigest(request.CostEvidenceDigest) || request.ControlPayloadMaxBytes == 0 ||
		request.ControlPayloadMaxBytes > math.MaxInt64 || !validRcloneNativeUTCTime(now) {
		return rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, nil)
	}
	if requireFutureDeadline {
		if request.ExactCommitKey != "" || request.ExactCommitVersionID != "" {
			return rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, nil)
		}
		if request.Manifest.Version != 1 || !validRcloneNativeDigest(request.Manifest.IndexDigest) ||
			!validRcloneNativeDigest(request.Manifest.ObservationDigest) || !request.Attempt.PointDeadlineAt.After(now) ||
			!request.Source.valid() || len(request.RcloneConfig) == 0 ||
			len(request.RcloneConfig) > 64<<10 || sha256Hex(request.RcloneConfig) != request.Attempt.ConfigDigest ||
			request.Runtime.Node.ID == 0 || request.MaxVerifyBytes == 0 || request.LowLevelRetries < 1 || request.LowLevelRetries > 10 {
			return rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, nil)
		}
		expectedConfig, err := BuildRcloneNativeRcloneConfig(request.Profile, request.Encryption, request.Session)
		if err != nil || !bytes.Equal(expectedConfig, request.RcloneConfig) ||
			request.Session.IdentityDigest() != request.Attempt.Native.RoleSessionIdentityDigest ||
			!request.Session.ExpiresAt().Equal(request.Attempt.Native.SessionExpiresAt) {
			return rcloneNativeError(backupasset.RcloneReasonCredentialInvalid, err)
		}
	} else {
		exactEmpty := request.ExactCommitKey == "" && request.ExactCommitVersionID == ""
		exactFull := request.ExactCommitKey == rcloneNativeAttemptControlPrefix(request)+"commit.json" &&
			validRcloneNativeVersionID(request.ExactCommitVersionID)
		if !exactEmpty && !exactFull {
			return rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, nil)
		}
		if !emptyRcloneManifestBundle(request.Manifest) &&
			(request.Manifest.Version != 1 || !validRcloneNativeDigest(request.Manifest.IndexDigest) ||
				!validRcloneNativeDigest(request.Manifest.ObservationDigest)) {
			return rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, nil)
		}
	}
	if request.Encryption.Profile != request.Attempt.Native.EncryptionProfile || request.EncryptionEvidence.Profile != request.Encryption.Profile {
		return rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	if request.Encryption.Profile == RcloneNativeSSES3V1 {
		if request.Encryption.ActiveKeyARN != "" || len(request.Encryption.RetainedReadKeyARNs) != 0 ||
			request.EncryptionEvidence.ActiveKeyDigest != "" || request.EncryptionEvidence.ReadKeySetDigest != "" ||
			request.EncryptionEvidence.RetainedReadKeyCount != 0 || request.EncryptionEvidence.BucketKeyEnabled || len(request.KMSKeyBindings) != 0 {
			return rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		return nil
	}
	return validateRcloneNativeKMSBindings(request)
}

func validateRcloneNativeKMSBindings(request RcloneNativePublicationRequest) error {
	if request.Encryption.Profile != RcloneNativeSSEKMSV1 ||
		!validRcloneNativeDigest(request.EncryptionEvidence.ActiveKeyDigest) ||
		!validRcloneNativeDigest(request.EncryptionEvidence.ReadKeySetDigest) ||
		request.EncryptionEvidence.ActiveKeyDigest != request.Attempt.Native.ActiveKeyDigest ||
		request.EncryptionEvidence.ReadKeySetDigest != request.Attempt.Native.RetainedReadKeySetDigest ||
		request.EncryptionEvidence.RetainedReadKeyCount != len(request.Encryption.RetainedReadKeyARNs) ||
		len(request.KMSKeyBindings) != 1+len(request.Encryption.RetainedReadKeyARNs) {
		return rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	wantARNs := append([]string{request.Encryption.ActiveKeyARN}, request.Encryption.RetainedReadKeyARNs...)
	want := make(map[string]string, len(wantARNs))
	for index, arn := range wantARNs {
		region, accountID, valid := parseRcloneNativeKMSKeyARN(arn)
		if !valid || region != request.Profile.Region || accountID != request.Session.AccountID() {
			return rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		if _, duplicate := want[arn]; duplicate {
			return rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		if index == 0 {
			want[arn] = request.EncryptionEvidence.ActiveKeyDigest
		} else {
			want[arn] = ""
		}
	}
	readDigests := make([]string, 0, len(request.Encryption.RetainedReadKeyARNs))
	seen := make(map[string]struct{}, len(request.KMSKeyBindings))
	for _, binding := range request.KMSKeyBindings {
		expectedDigest, exists := want[binding.KeyARN]
		if !exists || !validRcloneNativeDigest(binding.Digest) {
			return rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		if _, duplicate := seen[binding.KeyARN]; duplicate {
			return rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		seen[binding.KeyARN] = struct{}{}
		if expectedDigest != "" {
			if binding.Digest != expectedDigest {
				return rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
			}
			continue
		}
		readDigests = append(readDigests, binding.Digest)
	}
	if len(seen) != len(want) {
		return rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	sort.Strings(readDigests)
	readSetDigest, err := canonicalRcloneNativeDigest("kms-read-key-set-v1", readDigests)
	if err != nil || readSetDigest != request.EncryptionEvidence.ReadKeySetDigest {
		return rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, err)
	}
	return nil
}

func rcloneNativeAttemptControlPrefix(request RcloneNativePublicationRequest) string {
	return request.Profile.ManagedPrefix + "control/points/" + request.Attempt.RecoveryPointID + "/attempts/" + request.Attempt.AttemptID + "/"
}

func equalRcloneManifestBundleIdentity(left, right RcloneManifestBundle) bool {
	if left.Version != right.Version || left.IndexDigest != right.IndexDigest || left.EntryCount != right.EntryCount ||
		left.LogicalBytes != right.LogicalBytes || left.ObservationDigest != right.ObservationDigest || len(left.Chunks) != len(right.Chunks) {
		return false
	}
	for index := range left.Chunks {
		if left.Chunks[index].Ordinal != right.Chunks[index].Ordinal || left.Chunks[index].Digest != right.Chunks[index].Digest ||
			left.Chunks[index].EntryCount != right.Chunks[index].EntryCount || !bytes.Equal(left.Chunks[index].Encoded, right.Chunks[index].Encoded) {
			return false
		}
	}
	return bytes.Equal(left.IndexEncoded, right.IndexEncoded)
}

func attributeRcloneNativeAttemptMutations(
	request RcloneNativePublicationRequest,
	b0, b1 RcloneNativeStableGraph,
	start, end RcloneNativeControlObjectVersion,
) ([]RcloneNativeOwnedMutation, error) {
	before := make(map[string]struct{}, len(b0.Records))
	for _, record := range b0.Records {
		before[rcloneNativeVersionIdentity(record.PhysicalKey, record.VersionID)] = struct{}{}
	}
	startRecord, startFound := findRcloneNativeVersion(b1.Records, start.PhysicalKey, start.VersionID)
	endRecord, endFound := findRcloneNativeVersion(b1.Records, end.PhysicalKey, end.VersionID)
	if !startFound || !endFound || startRecord.Kind != RcloneNativeObjectVersion || endRecord.Kind != RcloneNativeObjectVersion ||
		endRecord.LastModified.Before(startRecord.LastModified) {
		return nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	dataPrefix := request.Profile.ManagedPrefix + "data/"
	owned := make([]RcloneNativeOwnedMutation, 0)
	for _, record := range b1.Records {
		identity := rcloneNativeVersionIdentity(record.PhysicalKey, record.VersionID)
		if _, exists := before[identity]; exists {
			continue
		}
		isMarker := identity == rcloneNativeVersionIdentity(start.PhysicalKey, start.VersionID) || identity == rcloneNativeVersionIdentity(end.PhysicalKey, end.VersionID)
		if !isMarker {
			if !strings.HasPrefix(record.PhysicalKey, dataPrefix) || record.LastModified.Before(startRecord.LastModified) || record.LastModified.After(endRecord.LastModified) {
				return nil, rcloneNativeError(backupasset.RcloneReasonExternalWriterDetected, nil)
			}
			if _, err := DecodeRcloneV1744S3Path(strings.TrimPrefix(record.PhysicalKey, dataPrefix)); err != nil {
				return nil, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, err)
			}
		}
		owned = append(owned, RcloneNativeOwnedMutation{PhysicalKey: record.PhysicalKey, VersionID: record.VersionID, Kind: record.Kind})
	}
	return owned, nil
}

func findRcloneNativeVersion(records []RcloneNativeVersionRecord, key, versionID string) (RcloneNativeVersionRecord, bool) {
	for _, record := range records {
		if record.PhysicalKey == key && record.VersionID == versionID {
			return record, true
		}
	}
	return RcloneNativeVersionRecord{}, false
}

func decodeRcloneNativeSourceManifest(manifest RcloneManifestBundle) ([]rcloneCanonicalManifestEntry, error) {
	entries := make([]rcloneCanonicalManifestEntry, 0, manifest.EntryCount)
	var logicalBytes uint64
	for index, chunk := range manifest.Chunks {
		if chunk.Ordinal != index || sha256Hex(chunk.Encoded) != chunk.Digest {
			return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
		}
		for _, line := range bytes.Split(chunk.Encoded, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			if rejectDuplicateJSONMembers(string(line)) != nil {
				return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
			}
			decoder := json.NewDecoder(bytes.NewReader(line))
			decoder.DisallowUnknownFields()
			var entry rcloneCanonicalManifestEntry
			if err := decoder.Decode(&entry); err != nil {
				return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
			}
			if err := decoder.Decode(&struct{}{}); err != io.EOF || entry.Version != 1 || !validRcloneLogicalPath(entry.Path, manifestDepthFloor(manifest)) {
				return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
			}
			if entry.Kind == "file" || entry.Kind == "symlink" {
				var err error
				logicalBytes, err = checkedAddUint64(logicalBytes, entry.Size)
				if err != nil {
					return nil, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, err)
				}
			}
			entries = append(entries, entry)
		}
	}
	if uint64(len(entries)) != manifest.EntryCount || logicalBytes != manifest.LogicalBytes {
		return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	return entries, nil
}

func manifestDepthFloor(RcloneManifestBundle) int { return 4096 }

func enrichAndVerifyRcloneNativePoint(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	sourceEntries []rcloneCanonicalManifestEntry,
	point *RcloneNativePointGraph,
	fullByteProof RcloneFullByteProof,
) (string, error) {
	expected := make(map[string]rcloneCanonicalManifestEntry)
	for _, entry := range sourceEntries {
		if entry.Kind == "directory" {
			continue
		}
		encoded, err := EncodeRcloneV1744S3Path(entry.PhysicalPath)
		if err != nil {
			return "", rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, err)
		}
		key := request.Profile.ManagedPrefix + "data/" + encoded
		if _, exists := expected[key]; exists {
			return "", rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
		}
		expected[key] = entry
	}
	proofDigests := make([]string, 0, len(expected))
	var verifiedBytes uint64
	seen := make(map[string]struct{}, len(expected))
	for index := range point.View {
		view := &point.View[index]
		source, exists := expected[view.PhysicalKey]
		if view.Kind == RcloneNativeDeleteMarker {
			if exists {
				return "", rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
			}
			continue
		}
		if !exists || source.Size != view.Size || verifiedBytes > request.MaxVerifyBytes || view.Size > request.MaxVerifyBytes-verifiedBytes {
			return "", rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
		}
		head, digest, err := readRcloneNativeExactObject(ctx, request, *view, source.SHA256)
		if err != nil {
			return "", err
		}
		view.LogicalPath = source.Path
		view.ContentDigest = digest
		view.EncryptionProfile = head.EncryptionProfile
		view.KMSKeyDigest = head.KMSKeyDigest
		view.BucketKeyEnabled = head.BucketKeyEnabled
		proof, err := digestRcloneNativeExactProof(*view, digest)
		if err != nil {
			return "", rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
		}
		proofDigests = append(proofDigests, proof)
		verifiedBytes += view.Size
		seen[view.PhysicalKey] = struct{}{}
	}
	if len(seen) != len(expected) || request.Manifest.Fidelity.RequiresFullByteVerification && !fullByteProof.Complete {
		return "", rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	for index := range point.Ledger {
		entry := &point.Ledger[index]
		if entry.Kind == RcloneNativeDeleteMarker {
			continue
		}
		head, err := request.s3.HeadVersion(ctx, RcloneNativeExactReadRequest{PhysicalKey: entry.PhysicalKey, VersionID: entry.VersionID})
		if err != nil || head.PhysicalKey != entry.PhysicalKey || head.VersionID != entry.VersionID || head.Size != entry.Size || validateRcloneNativeHeadEncryption(request, head) != nil {
			return "", rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, err)
		}
		entry.EncryptionProfile = head.EncryptionProfile
		entry.KMSKeyDigest = head.KMSKeyDigest
		entry.BucketKeyEnabled = head.BucketKeyEnabled
	}
	var err error
	point.ViewDigest, err = digestRcloneNativePointView(point.View)
	if err != nil {
		return "", rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	point.LedgerDigest, err = digestRcloneNativeMutationLedger(point.Ledger)
	if err != nil {
		return "", rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	sort.Strings(proofDigests)
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-native-exact-proof-set-v1")
	writer.Uint64(verifiedBytes)
	for _, digest := range proofDigests {
		writer.String(digest)
	}
	return writer.HexDigest()
}

func readRcloneNativeExactObject(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	entry RcloneNativePointViewEntry,
	expectedDigest string,
) (RcloneNativeExactObjectHead, string, error) {
	head, err := request.s3.HeadVersion(ctx, RcloneNativeExactReadRequest{PhysicalKey: entry.PhysicalKey, VersionID: entry.VersionID})
	if err != nil || head.PhysicalKey != entry.PhysicalKey || head.VersionID != entry.VersionID || head.Size != entry.Size {
		return RcloneNativeExactObjectHead{}, "", rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, err)
	}
	if err := validateRcloneNativeHeadEncryption(request, head); err != nil {
		return RcloneNativeExactObjectHead{}, "", err
	}
	body, err := request.s3.OpenVersion(ctx, RcloneNativeExactReadRequest{PhysicalKey: entry.PhysicalKey, VersionID: entry.VersionID})
	if err != nil || body == nil {
		return RcloneNativeExactObjectHead{}, "", rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	hasher := sha256.New()
	read, readErr := io.Copy(hasher, io.LimitReader(body, int64(entry.Size)+1))
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || read < 0 || uint64(read) != entry.Size {
		if readErr == nil {
			readErr = closeErr
		}
		return RcloneNativeExactObjectHead{}, "", rcloneNativeError(backupasset.RcloneReasonManifestMismatch, readErr)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if expectedDigest != "" && digest != expectedDigest {
		return RcloneNativeExactObjectHead{}, "", rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	return head, digest, nil
}

func validateRcloneNativeHeadEncryption(request RcloneNativePublicationRequest, head RcloneNativeExactObjectHead) error {
	if head.EncryptionProfile != request.Encryption.Profile || head.BucketKeyEnabled != request.EncryptionEvidence.BucketKeyEnabled {
		return rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
	}
	if head.EncryptionProfile == RcloneNativeSSES3V1 {
		if head.KMSKeyDigest != "" || head.BucketKeyEnabled {
			return rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
		}
		return nil
	}
	for _, binding := range request.KMSKeyBindings {
		if binding.Digest == head.KMSKeyDigest && validRcloneNativeDigest(binding.Digest) {
			return nil
		}
	}
	return rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
}

type rcloneNativeCommitMarkerV1 struct {
	Version                   int                                     `json:"version"`
	RepositoryID              string                                  `json:"repository_id"`
	TaskRepositoryLinkID      string                                  `json:"task_repository_link_id"`
	RecoveryPointID           string                                  `json:"recovery_point_id"`
	AttemptID                 string                                  `json:"attempt_id"`
	PointDeadlineAt           time.Time                               `json:"point_deadline_at"`
	ProviderCommittedAt       time.Time                               `json:"provider_committed_at"`
	ManifestIndexDigest       string                                  `json:"manifest_index_digest"`
	SourceManifestIndexDigest string                                  `json:"source_manifest_index_digest"`
	SourceObservationDigest   string                                  `json:"source_observation_digest"`
	ManifestEntryCount        uint64                                  `json:"manifest_entry_count"`
	LogicalBytes              uint64                                  `json:"logical_bytes"`
	FidelityEvidenceDigest    string                                  `json:"fidelity_evidence_digest"`
	CostEvidenceDigest        string                                  `json:"cost_evidence_digest"`
	CapabilityEvidenceDigest  string                                  `json:"capability_evidence_digest"`
	EncryptionEvidenceDigest  string                                  `json:"encryption_evidence_digest"`
	PointViewDigest           string                                  `json:"point_view_digest"`
	MutationLedgerDigest      string                                  `json:"mutation_ledger_digest"`
	B0VersionGraphDigest      string                                  `json:"b0_version_graph_digest"`
	B1VersionGraphDigest      string                                  `json:"b1_version_graph_digest"`
	ExactReadProofDigest      string                                  `json:"exact_read_proof_digest"`
	ChildFenceDigest          string                                  `json:"child_fence_digest"`
	ManifestVersions          []rcloneNativeControlVersionReferenceV1 `json:"manifest_versions"`
	IndexVersion              rcloneNativeControlVersionReferenceV1   `json:"index_version"`
	PrecommitGraphDigest      string                                  `json:"precommit_graph_digest"`
}

type rcloneNativeControlVersionReferenceV1 struct {
	PhysicalKey       string                            `json:"physical_key"`
	VersionID         string                            `json:"version_id"`
	Size              uint64                            `json:"size"`
	ContentDigest     string                            `json:"content_digest"`
	EncryptionProfile RcloneNativeEncryptionProfileCode `json:"encryption_profile"`
	KMSKeyDigest      string                            `json:"kms_key_digest,omitempty"`
	BucketKeyEnabled  bool                              `json:"bucket_key_enabled,omitempty"`
	EvidenceDigest    string                            `json:"evidence_digest"`
}

type rcloneNativeManifestVersionStateV1 struct {
	PhysicalKey       string                            `json:"physical_key"`
	VersionID         string                            `json:"version_id"`
	Kind              RcloneNativeVersionKind           `json:"kind"`
	Size              uint64                            `json:"size"`
	ContentDigest     string                            `json:"content_digest,omitempty"`
	EncryptionProfile RcloneNativeEncryptionProfileCode `json:"encryption_profile,omitempty"`
	KMSKeyDigest      string                            `json:"kms_key_digest,omitempty"`
	BucketKeyEnabled  bool                              `json:"bucket_key_enabled,omitempty"`
	Disposition       RcloneNativeMutationDisposition   `json:"disposition,omitempty"`
	LastModified      time.Time                         `json:"last_modified,omitempty"`
}

type rcloneNativeManifestHeaderV1 struct {
	SourceManifestIndexDigest string `json:"source_manifest_index_digest"`
	SourceObservationDigest   string `json:"source_observation_digest"`
	PointViewDigest           string `json:"point_view_digest"`
	MutationLedgerDigest      string `json:"mutation_ledger_digest"`
	B0VersionGraphDigest      string `json:"b0_version_graph_digest"`
	B1VersionGraphDigest      string `json:"b1_version_graph_digest"`
	ExactReadProofDigest      string `json:"exact_read_proof_digest"`
}

type rcloneNativeManifestRecordV1 struct {
	Version    int                                 `json:"version"`
	RecordKind string                              `json:"record_kind"`
	Header     *rcloneNativeManifestHeaderV1       `json:"header,omitempty"`
	Source     *rcloneCanonicalManifestEntry       `json:"source,omitempty"`
	State      *rcloneNativeManifestVersionStateV1 `json:"state,omitempty"`
}

type rcloneNativeManifestChunkReferenceV1 struct {
	Ordinal     int    `json:"ordinal"`
	Digest      string `json:"digest"`
	Size        uint64 `json:"size"`
	RecordCount uint64 `json:"record_count"`
}

type rcloneNativeManifestIndexV1 struct {
	Version                   int                                    `json:"version"`
	ManifestSchema            string                                 `json:"manifest_schema"`
	SourceManifestIndexDigest string                                 `json:"source_manifest_index_digest"`
	SourceObservationDigest   string                                 `json:"source_observation_digest"`
	Chunks                    []rcloneNativeManifestChunkReferenceV1 `json:"chunks"`
	EntryCount                uint64                                 `json:"entry_count"`
	LogicalBytes              uint64                                 `json:"logical_bytes"`
	PointViewDigest           string                                 `json:"point_view_digest"`
	MutationLedgerDigest      string                                 `json:"mutation_ledger_digest"`
	B0VersionGraphDigest      string                                 `json:"b0_version_graph_digest"`
	B1VersionGraphDigest      string                                 `json:"b1_version_graph_digest"`
	ExactReadProofDigest      string                                 `json:"exact_read_proof_digest"`
}

func buildRcloneNativeControlCommitRequest(
	request RcloneNativePublicationRequest,
	controlPrefix string,
	sourceEntries []rcloneCanonicalManifestEntry,
	point RcloneNativePointGraph,
	exactProofDigest string,
	b0, b1 RcloneNativeStableGraph,
	committedAt time.Time,
) (RcloneNativeControlCommitRequest, []string, string, rcloneNativeCommitMarkerV1, error) {
	records, err := buildRcloneNativeManifestRecords(request, sourceEntries, point, exactProofDigest, b0, b1)
	if err != nil {
		return RcloneNativeControlCommitRequest{}, nil, "", rcloneNativeCommitMarkerV1{}, err
	}
	chunkPayloads, chunkReferences, err := chunkRcloneNativeManifestRecords(records, request.ControlPayloadMaxBytes)
	if err != nil {
		return RcloneNativeControlCommitRequest{}, nil, "", rcloneNativeCommitMarkerV1{}, err
	}
	chunks := make([]RcloneNativeControlPayload, len(chunkPayloads))
	digests := make([]string, len(chunkPayloads))
	for index, payload := range chunkPayloads {
		chunks[index] = RcloneNativeControlPayload{
			PhysicalKey: controlPrefix + fmt.Sprintf("manifest-%06d.jsonl", index),
			Payload:     payload,
		}
		digests[index] = chunkReferences[index].Digest
	}
	manifestIndex := rcloneNativeManifestIndexV1{
		Version: 1, ManifestSchema: "rclone-native-manifest-v1",
		SourceManifestIndexDigest: request.Manifest.IndexDigest, SourceObservationDigest: request.Manifest.ObservationDigest,
		Chunks: chunkReferences, EntryCount: request.Manifest.EntryCount, LogicalBytes: request.Manifest.LogicalBytes,
		PointViewDigest: point.ViewDigest, MutationLedgerDigest: point.LedgerDigest,
		B0VersionGraphDigest: b0.Digest, B1VersionGraphDigest: b1.Digest, ExactReadProofDigest: exactProofDigest,
	}
	manifestIndexPayload, err := json.Marshal(manifestIndex)
	if err != nil || uint64(len(manifestIndexPayload)) > request.ControlPayloadMaxBytes {
		return RcloneNativeControlCommitRequest{}, nil, "", rcloneNativeCommitMarkerV1{}, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, err)
	}
	manifestIndexDigest := sha256Hex(manifestIndexPayload)
	fidelityEvidenceDigest, err := canonicalRcloneNativeDigest("fidelity-evidence-v1", request.Manifest.Fidelity)
	if err != nil {
		return RcloneNativeControlCommitRequest{}, nil, "", rcloneNativeCommitMarkerV1{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	encryptionEvidenceDigest, err := canonicalRcloneNativeDigest("encryption-evidence-v1", request.EncryptionEvidence)
	if err != nil {
		return RcloneNativeControlCommitRequest{}, nil, "", rcloneNativeCommitMarkerV1{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	marker := rcloneNativeCommitMarkerV1{
		Version: 1, RepositoryID: request.Attempt.RepositoryID, TaskRepositoryLinkID: request.Attempt.TaskRepositoryLinkID,
		RecoveryPointID: request.Attempt.RecoveryPointID, AttemptID: request.Attempt.AttemptID,
		PointDeadlineAt: request.Attempt.PointDeadlineAt, ProviderCommittedAt: committedAt, ManifestIndexDigest: manifestIndexDigest,
		SourceManifestIndexDigest: request.Manifest.IndexDigest, SourceObservationDigest: request.Manifest.ObservationDigest,
		ManifestEntryCount: request.Manifest.EntryCount, LogicalBytes: request.Manifest.LogicalBytes,
		FidelityEvidenceDigest: fidelityEvidenceDigest, CostEvidenceDigest: request.CostEvidenceDigest,
		CapabilityEvidenceDigest: request.CapabilityEvidenceDigest, EncryptionEvidenceDigest: encryptionEvidenceDigest,
		PointViewDigest: point.ViewDigest, MutationLedgerDigest: point.LedgerDigest,
		B0VersionGraphDigest: b0.Digest, B1VersionGraphDigest: b1.Digest,
		ExactReadProofDigest: exactProofDigest, ChildFenceDigest: request.Attempt.ChildFenceDigest,
	}
	return RcloneNativeControlCommitRequest{
		ManifestChunks:    chunks,
		ManifestIndex:     RcloneNativeControlPayload{PhysicalKey: controlPrefix + "manifest-index.json", Payload: manifestIndexPayload},
		Commit:            RcloneNativeControlPayload{PhysicalKey: controlPrefix + "commit.json"},
		EncryptionProfile: request.Encryption.Profile, KMSKeyARN: request.Encryption.ActiveKeyARN,
		KMSKeyDigest: request.EncryptionEvidence.ActiveKeyDigest, BucketKeyEnabled: request.EncryptionEvidence.BucketKeyEnabled,
		MaxObjectBytes: request.ControlPayloadMaxBytes,
	}, digests, manifestIndexDigest, marker, nil
}

func publishRcloneNativeBoundControlCommit(
	ctx context.Context,
	store RcloneNativeControlStore,
	request RcloneNativeControlCommitRequest,
	marker rcloneNativeCommitMarkerV1,
	markerKey []byte,
) (RcloneNativeControlCommitGraph, error) {
	if store == nil || len(markerKey) < 32 || request.Commit.Payload != nil {
		return RcloneNativeControlCommitGraph{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	validation := request
	validation.Commit.Payload = []byte{1}
	ordered, err := validateRcloneNativeControlCommitRequest(validation)
	if err != nil {
		return RcloneNativeControlCommitGraph{}, err
	}
	graph := RcloneNativeControlCommitGraph{ManifestVersions: make([]RcloneNativeControlObjectVersion, 0, len(request.ManifestChunks))}
	for index, payload := range ordered[:len(ordered)-1] {
		version, writeErr := writeAndVerifyRcloneNativeControlObject(ctx, store, request, payload)
		if writeErr != nil {
			return RcloneNativeControlCommitGraph{}, writeErr
		}
		if index < len(request.ManifestChunks) {
			graph.ManifestVersions = append(graph.ManifestVersions, version)
		} else {
			graph.IndexVersion = version
		}
	}
	marker.ManifestVersions = make([]rcloneNativeControlVersionReferenceV1, len(graph.ManifestVersions))
	for index, version := range graph.ManifestVersions {
		marker.ManifestVersions[index] = rcloneNativeControlVersionReference(version)
	}
	marker.IndexVersion = rcloneNativeControlVersionReference(graph.IndexVersion)
	marker.PrecommitGraphDigest, err = digestRcloneNativeControlPrecommit(graph.ManifestVersions, graph.IndexVersion)
	if err != nil {
		return RcloneNativeControlCommitGraph{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	document, err := json.Marshal(marker)
	if err != nil {
		return RcloneNativeControlCommitGraph{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	commitPayload, _, _, err := encodeRcloneAuthenticatedControl("commit", document, markerKey)
	if err != nil || uint64(len(commitPayload)) > request.MaxObjectBytes {
		return RcloneNativeControlCommitGraph{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	request.Commit.Payload = commitPayload
	graph.CommitVersion, err = writeAndVerifyRcloneNativeControlObject(ctx, store, request, request.Commit)
	if err != nil {
		return RcloneNativeControlCommitGraph{}, err
	}
	graph.Digest, err = digestRcloneNativeControlGraph(graph)
	if err != nil {
		return RcloneNativeControlCommitGraph{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	return graph, nil
}

func rcloneNativeControlVersionReference(value RcloneNativeControlObjectVersion) rcloneNativeControlVersionReferenceV1 {
	return rcloneNativeControlVersionReferenceV1(value)
}

func digestRcloneNativeControlPrecommit(
	manifest []RcloneNativeControlObjectVersion,
	index RcloneNativeControlObjectVersion,
) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-native-control-precommit-v1")
	writer.Uint64(uint64(len(manifest)))
	for _, version := range manifest {
		writeRcloneNativeControlVersion(writer, version)
	}
	writeRcloneNativeControlVersion(writer, index)
	return writer.HexDigest()
}

func buildRcloneNativeManifestRecords(
	request RcloneNativePublicationRequest,
	sourceEntries []rcloneCanonicalManifestEntry,
	point RcloneNativePointGraph,
	exactProofDigest string,
	b0, b1 RcloneNativeStableGraph,
) ([]rcloneNativeManifestRecordV1, error) {
	header := &rcloneNativeManifestHeaderV1{
		SourceManifestIndexDigest: request.Manifest.IndexDigest, SourceObservationDigest: request.Manifest.ObservationDigest,
		PointViewDigest: point.ViewDigest, MutationLedgerDigest: point.LedgerDigest,
		B0VersionGraphDigest: b0.Digest, B1VersionGraphDigest: b1.Digest, ExactReadProofDigest: exactProofDigest,
	}
	records := []rcloneNativeManifestRecordV1{{Version: 1, RecordKind: "header", Header: header}}
	viewByKey := make(map[string]RcloneNativePointViewEntry, len(point.View))
	deletes := make([]RcloneNativePointViewEntry, 0)
	for _, view := range point.View {
		viewByKey[view.PhysicalKey] = view
		if view.Kind == RcloneNativeDeleteMarker {
			deletes = append(deletes, view)
		}
	}
	sort.Slice(sourceEntries, func(left, right int) bool { return sourceEntries[left].Path < sourceEntries[right].Path })
	for index := range sourceEntries {
		source := sourceEntries[index]
		record := rcloneNativeManifestRecordV1{Version: 1, RecordKind: "entry", Source: &source}
		if source.Kind != "directory" {
			encoded, err := EncodeRcloneV1744S3Path(source.PhysicalPath)
			if err != nil {
				return nil, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, err)
			}
			view, exists := viewByKey[request.Profile.ManagedPrefix+"data/"+encoded]
			if !exists || view.Kind != RcloneNativeObjectVersion {
				return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
			}
			state := rcloneNativeVersionStateFromView(view)
			record.State = &state
		}
		records = append(records, record)
	}
	sort.Slice(deletes, func(left, right int) bool { return deletes[left].PhysicalKey < deletes[right].PhysicalKey })
	for _, view := range deletes {
		state := rcloneNativeVersionStateFromView(view)
		records = append(records, rcloneNativeManifestRecordV1{Version: 1, RecordKind: "delete_state", State: &state})
	}
	for _, mutation := range point.Ledger {
		state := rcloneNativeVersionStateFromMutation(mutation)
		records = append(records, rcloneNativeManifestRecordV1{Version: 1, RecordKind: "mutation", State: &state})
	}
	return records, nil
}

func rcloneNativeVersionStateFromView(value RcloneNativePointViewEntry) rcloneNativeManifestVersionStateV1 {
	return rcloneNativeManifestVersionStateV1{
		PhysicalKey: value.PhysicalKey, VersionID: value.VersionID, Kind: value.Kind, Size: value.Size,
		ContentDigest: value.ContentDigest, EncryptionProfile: value.EncryptionProfile,
		KMSKeyDigest: value.KMSKeyDigest, BucketKeyEnabled: value.BucketKeyEnabled,
	}
}

func rcloneNativeVersionStateFromMutation(value RcloneNativeMutationLedgerEntry) rcloneNativeManifestVersionStateV1 {
	return rcloneNativeManifestVersionStateV1{
		PhysicalKey: value.PhysicalKey, VersionID: value.VersionID, Kind: value.Kind, Size: value.Size,
		EncryptionProfile: value.EncryptionProfile, KMSKeyDigest: value.KMSKeyDigest,
		BucketKeyEnabled: value.BucketKeyEnabled, Disposition: value.Disposition, LastModified: value.LastModified,
	}
}

func chunkRcloneNativeManifestRecords(
	records []rcloneNativeManifestRecordV1,
	maxBytes uint64,
) ([][]byte, []rcloneNativeManifestChunkReferenceV1, error) {
	if len(records) == 0 || maxBytes == 0 || maxBytes > math.MaxInt64 {
		return nil, nil, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, nil)
	}
	payloads := make([][]byte, 0)
	references := make([]rcloneNativeManifestChunkReferenceV1, 0)
	current := bytes.Buffer{}
	var recordCount uint64
	flush := func() {
		payload := append([]byte(nil), current.Bytes()...)
		ordinal := len(payloads)
		payloads = append(payloads, payload)
		references = append(references, rcloneNativeManifestChunkReferenceV1{
			Ordinal: ordinal, Digest: sha256Hex(payload), Size: uint64(len(payload)), RecordCount: recordCount,
		})
		current.Reset()
		recordCount = 0
	}
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			return nil, nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
		}
		encoded = append(encoded, '\n')
		if uint64(len(encoded)) > maxBytes {
			return nil, nil, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, nil)
		}
		if current.Len() > 0 && uint64(current.Len()+len(encoded)) > maxBytes {
			flush()
		}
		_, _ = current.Write(encoded)
		recordCount++
	}
	if current.Len() > 0 {
		flush()
	}
	return payloads, references, nil
}

func verifyUniqueRcloneNativeCommitVersion(
	ctx context.Context,
	request RcloneNativePublicationRequest,
	controlPrefix string,
	commit RcloneNativeControlObjectVersion,
) error {
	graph, err := CaptureRcloneNativeStableGraph(ctx, request.s3, controlPrefix, request.ObservationLimits)
	if err != nil {
		return err
	}
	count := 0
	for _, record := range graph.Records {
		if record.PhysicalKey != commit.PhysicalKey {
			continue
		}
		count++
		if record.VersionID != commit.VersionID || record.Kind != RcloneNativeObjectVersion || !record.IsLatest {
			return rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
		}
	}
	if count != 1 {
		return rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	return nil
}

func buildRcloneNativeProviderCommit(
	request RcloneNativePublicationRequest,
	point RcloneNativePointGraph,
	exactProofDigest string,
	b0, b1 RcloneNativeStableGraph,
	control RcloneNativeControlCommitGraph,
	chunkDigests []string,
	manifestIndexDigest string,
	committedAt time.Time,
) (RcloneCommitV1, error) {
	fidelityDigest, err := canonicalRcloneNativeDigest("fidelity-evidence-v1", request.Manifest.Fidelity)
	if err != nil {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	encryptionDigest, err := canonicalRcloneNativeDigest("encryption-evidence-v1", request.EncryptionEvidence)
	if err != nil {
		return RcloneCommitV1{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	native := request.Attempt.Native
	commit := RcloneCommitV1{
		SchemaVersion: 1, LayoutVersion: request.Attempt.LayoutVersion, MinimumRuntimeRevision: request.Attempt.MinimumRuntimeRevision,
		RepositoryID: request.Attempt.RepositoryID, TaskRepositoryLinkID: request.Attempt.TaskRepositoryLinkID,
		RecoveryPointID: request.Attempt.RecoveryPointID, AttemptID: request.Attempt.AttemptID,
		PublicationMode: request.Attempt.PublicationMode, PointDeadlineAt: request.Attempt.PointDeadlineAt, ProviderCommittedAt: committedAt,
		ManifestIndexDigest: manifestIndexDigest, ManifestChunkDigests: append([]string(nil), chunkDigests...),
		ManifestEntryCount: request.Manifest.EntryCount, LogicalBytes: request.Manifest.LogicalBytes,
		SourceObservationDigest: request.Manifest.ObservationDigest, DestinationObservationDigest: b1.Digest,
		ContentProofDigest: exactProofDigest, FidelityEvidenceDigest: fidelityDigest,
		CostEvidenceDigest: request.CostEvidenceDigest, CapabilityEvidenceDigest: request.CapabilityEvidenceDigest,
		ChildFenceDigest: request.Attempt.ChildFenceDigest,
		Native: &RcloneNativeCommitV1{
			CommitKey: control.CommitVersion.PhysicalKey, CommitVersionID: control.CommitVersion.VersionID,
			CommitContentDigest: control.CommitVersion.ContentDigest, ManifestControlGraphDigest: control.Digest,
			PointViewDigest: point.ViewDigest, MutationLedgerDigest: point.LedgerDigest,
			B0VersionGraphDigest: b0.Digest, B1VersionGraphDigest: b1.Digest, ExactReadProofDigest: exactProofDigest,
			VersioningDigest: native.VersioningDigest, LifecycleDigest: native.LifecycleDigest,
			BucketEncryptionDigest: native.BucketEncryptionDigest, EncryptionEvidenceDigest: encryptionDigest,
			ActiveKeyDigest: native.ActiveKeyDigest, RetainedReadKeySetDigest: native.RetainedReadKeySetDigest,
			RoleSessionIdentityDigest: native.RoleSessionIdentityDigest, CapabilityRevision: request.Attempt.CapabilityRevision,
			CredentialRevision: request.Attempt.CredentialRevision, KMSCapabilityRevision: native.KMSCapabilityRevision,
			SessionExpiresAt: native.SessionExpiresAt,
		},
	}
	if err := commit.Validate(); err != nil {
		return RcloneCommitV1{}, err
	}
	return commit, nil
}

func rcloneNativeDataPlaneError(ctx context.Context, err error) error {
	if rcloneNativeReason(err) != "" {
		return err
	}
	if ctx.Err() != nil {
		return rcloneNativeError(backupasset.RcloneReasonProviderTimeout, err)
	}
	return rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
}

type RcloneNativeExactReader interface {
	HeadVersion(context.Context, RcloneNativeExactReadRequest) (RcloneNativeExactObjectHead, error)
	OpenVersion(context.Context, RcloneNativeExactReadRequest) (io.ReadCloser, error)
}

type RcloneNativeExactRangeRequest struct {
	PhysicalKey string
	VersionID   string
	Offset      uint64
	Length      uint64
}

type RcloneNativeExactRangeReader interface {
	HeadVersion(context.Context, RcloneNativeExactReadRequest) (RcloneNativeExactObjectHead, error)
	OpenVersionRange(context.Context, RcloneNativeExactRangeRequest) (io.ReadCloser, error)
}

type RcloneNativeExactObjectProof struct {
	Digest        string
	VerifiedBytes uint64
}

type RcloneNativeExactRangeProof struct {
	Digest        string
	Offset        uint64
	VerifiedBytes uint64
}

func VerifyRcloneNativeExactObject(ctx context.Context, reader RcloneNativeExactReader, entry RcloneNativePointViewEntry, maxBytes uint64) (RcloneNativeExactObjectProof, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reader == nil || entry.Kind != RcloneNativeObjectVersion || !validRcloneNativeVersionIdentity(entry.PhysicalKey, entry.VersionID, entry.Kind) ||
		!validRcloneNativeContentEvidence(entry) || maxBytes > math.MaxInt64 || entry.Size > maxBytes || entry.Size >= math.MaxInt64 {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	request := RcloneNativeExactReadRequest{PhysicalKey: entry.PhysicalKey, VersionID: entry.VersionID}
	head, err := reader.HeadVersion(ctx, request)
	if err != nil {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	if head.PhysicalKey != entry.PhysicalKey || head.VersionID != entry.VersionID || head.Size != entry.Size {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
	}
	if head.EncryptionProfile != entry.EncryptionProfile || head.KMSKeyDigest != entry.KMSKeyDigest || head.BucketKeyEnabled != entry.BucketKeyEnabled {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
	}
	body, err := reader.OpenVersion(ctx, request)
	if err != nil {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	if body == nil {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, nil)
	}
	hasher := sha256.New()
	read, readErr := io.Copy(hasher, io.LimitReader(body, int64(entry.Size)+1))
	closeErr := body.Close()
	if readErr != nil {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, readErr)
	}
	if closeErr != nil {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, closeErr)
	}
	if read < 0 || uint64(read) != entry.Size {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	contentDigest := hex.EncodeToString(hasher.Sum(nil))
	if contentDigest != entry.ContentDigest {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	proofDigest, err := digestRcloneNativeExactProof(entry, contentDigest)
	if err != nil {
		return RcloneNativeExactObjectProof{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	return RcloneNativeExactObjectProof{Digest: proofDigest, VerifiedBytes: uint64(read)}, nil
}

func VerifyRcloneNativeExactRange(
	ctx context.Context,
	reader RcloneNativeExactRangeReader,
	entry RcloneNativePointViewEntry,
	offset uint64,
	length uint64,
	expectedDigest string,
	maxBytes uint64,
) (RcloneNativeExactRangeProof, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reader == nil || entry.Kind != RcloneNativeObjectVersion || !validRcloneNativeVersionIdentity(entry.PhysicalKey, entry.VersionID, entry.Kind) ||
		!validRcloneNativeContentEvidence(entry) || length == 0 || length > maxBytes || length >= math.MaxInt64 ||
		offset > entry.Size || length > entry.Size-offset || !validRcloneNativeDigest(expectedDigest) {
		return RcloneNativeExactRangeProof{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	readRequest := RcloneNativeExactReadRequest{PhysicalKey: entry.PhysicalKey, VersionID: entry.VersionID}
	head, err := reader.HeadVersion(ctx, readRequest)
	if err != nil {
		return RcloneNativeExactRangeProof{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	if head.PhysicalKey != entry.PhysicalKey || head.VersionID != entry.VersionID || head.Size != entry.Size {
		return RcloneNativeExactRangeProof{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
	}
	if head.EncryptionProfile != entry.EncryptionProfile || head.KMSKeyDigest != entry.KMSKeyDigest || head.BucketKeyEnabled != entry.BucketKeyEnabled {
		return RcloneNativeExactRangeProof{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
	}
	rangeRequest := RcloneNativeExactRangeRequest{
		PhysicalKey: entry.PhysicalKey,
		VersionID:   entry.VersionID,
		Offset:      offset,
		Length:      length,
	}
	body, err := reader.OpenVersionRange(ctx, rangeRequest)
	if err != nil {
		return RcloneNativeExactRangeProof{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	if body == nil {
		return RcloneNativeExactRangeProof{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, nil)
	}
	hasher := sha256.New()
	read, readErr := io.Copy(hasher, io.LimitReader(body, int64(length)+1))
	closeErr := body.Close()
	if readErr != nil {
		return RcloneNativeExactRangeProof{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, readErr)
	}
	if closeErr != nil {
		return RcloneNativeExactRangeProof{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, closeErr)
	}
	if read < 0 || uint64(read) != length || hex.EncodeToString(hasher.Sum(nil)) != expectedDigest {
		return RcloneNativeExactRangeProof{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
	}
	proofDigest, err := digestRcloneNativeExactRangeProof(entry, offset, length, expectedDigest)
	if err != nil {
		return RcloneNativeExactRangeProof{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	return RcloneNativeExactRangeProof{Digest: proofDigest, Offset: offset, VerifiedBytes: uint64(read)}, nil
}

func PublishRcloneNativeControlCommit(
	ctx context.Context,
	store RcloneNativeControlStore,
	request RcloneNativeControlCommitRequest,
) (RcloneNativeControlCommitGraph, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil {
		return RcloneNativeControlCommitGraph{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	ordered, err := validateRcloneNativeControlCommitRequest(request)
	if err != nil {
		return RcloneNativeControlCommitGraph{}, err
	}
	graph := RcloneNativeControlCommitGraph{ManifestVersions: make([]RcloneNativeControlObjectVersion, 0, len(request.ManifestChunks))}
	for index, payload := range ordered {
		version, writeErr := writeAndVerifyRcloneNativeControlObject(ctx, store, request, payload)
		if writeErr != nil {
			return RcloneNativeControlCommitGraph{}, writeErr
		}
		switch {
		case index < len(request.ManifestChunks):
			graph.ManifestVersions = append(graph.ManifestVersions, version)
		case index == len(request.ManifestChunks):
			graph.IndexVersion = version
		default:
			graph.CommitVersion = version
		}
	}
	graph.Digest, err = digestRcloneNativeControlGraph(graph)
	if err != nil {
		return RcloneNativeControlCommitGraph{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	return graph, nil
}

func validateRcloneNativeControlCommitRequest(request RcloneNativeControlCommitRequest) ([]RcloneNativeControlPayload, error) {
	if request.MaxObjectBytes == 0 || request.MaxObjectBytes > math.MaxInt64 || len(request.ManifestChunks) == 0 {
		return nil, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, nil)
	}
	switch request.EncryptionProfile {
	case RcloneNativeSSES3V1:
		if request.KMSKeyARN != "" || request.KMSKeyDigest != "" || request.BucketKeyEnabled {
			return nil, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
	case RcloneNativeSSEKMSV1:
		if _, _, ok := parseRcloneNativeKMSKeyARN(request.KMSKeyARN); !ok || !validRcloneNativeDigest(request.KMSKeyDigest) {
			return nil, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
	default:
		return nil, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
	}
	ordered := make([]RcloneNativeControlPayload, 0, len(request.ManifestChunks)+2)
	ordered = append(ordered, request.ManifestChunks...)
	ordered = append(ordered, request.ManifestIndex, request.Commit)
	seen := make(map[string]struct{}, len(ordered))
	controlDirectory := path.Dir(request.Commit.PhysicalKey)
	if path.Base(request.Commit.PhysicalKey) != "commit.json" || path.Base(request.ManifestIndex.PhysicalKey) != "manifest-index.json" ||
		controlDirectory == "." || path.Dir(request.ManifestIndex.PhysicalKey) != controlDirectory {
		return nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	for index, payload := range ordered {
		if !validRcloneNativePhysicalKey(payload.PhysicalKey) || path.Dir(payload.PhysicalKey) != controlDirectory || len(payload.Payload) == 0 {
			return nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
		}
		if uint64(len(payload.Payload)) > request.MaxObjectBytes {
			return nil, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, nil)
		}
		if _, exists := seen[payload.PhysicalKey]; exists {
			return nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
		}
		seen[payload.PhysicalKey] = struct{}{}
		if index < len(request.ManifestChunks) && !strings.HasPrefix(path.Base(payload.PhysicalKey), "manifest-") {
			return nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
		}
	}
	return ordered, nil
}

func writeAndVerifyRcloneNativeControlObject(
	ctx context.Context,
	store RcloneNativeControlStore,
	commitRequest RcloneNativeControlCommitRequest,
	payload RcloneNativeControlPayload,
) (RcloneNativeControlObjectVersion, error) {
	writeRequest := RcloneNativeControlWriteRequest{
		PhysicalKey: payload.PhysicalKey, Payload: append([]byte(nil), payload.Payload...), MaxBytes: commitRequest.MaxObjectBytes,
		EncryptionProfile: commitRequest.EncryptionProfile, KMSKeyARN: commitRequest.KMSKeyARN,
		KMSKeyDigest: commitRequest.KMSKeyDigest, BucketKeyEnabled: commitRequest.BucketKeyEnabled,
	}
	result, err := store.PutControlVersion(ctx, writeRequest)
	if err != nil {
		return RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
	}
	if !validRcloneNativeVersionID(result.VersionID) {
		return RcloneNativeControlObjectVersion{}, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	entry := RcloneNativePointViewEntry{
		PhysicalKey: payload.PhysicalKey, VersionID: result.VersionID, Kind: RcloneNativeObjectVersion,
		Size: uint64(len(payload.Payload)), ContentDigest: sha256Hex(payload.Payload), EncryptionProfile: commitRequest.EncryptionProfile,
		KMSKeyDigest: commitRequest.KMSKeyDigest, BucketKeyEnabled: commitRequest.BucketKeyEnabled,
	}
	proof, err := VerifyRcloneNativeExactObject(ctx, store, entry, commitRequest.MaxObjectBytes)
	if err != nil {
		return RcloneNativeControlObjectVersion{}, err
	}
	return RcloneNativeControlObjectVersion{
		PhysicalKey: entry.PhysicalKey, VersionID: entry.VersionID, Size: entry.Size, ContentDigest: entry.ContentDigest,
		EncryptionProfile: entry.EncryptionProfile, KMSKeyDigest: entry.KMSKeyDigest, BucketKeyEnabled: entry.BucketKeyEnabled,
		EvidenceDigest: proof.Digest,
	}, nil
}

func normalizeRcloneNativeObservation(value RcloneNativeFullObservation) (RcloneNativeStableGraph, error) {
	if value.PageCount <= 0 || value.TerminalKeyMarker != "" || value.TerminalVersionIDMarker != "" {
		return RcloneNativeStableGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
	}
	records := append([]RcloneNativeVersionRecord(nil), value.Records...)
	sort.Slice(records, func(left, right int) bool {
		if records[left].PhysicalKey != records[right].PhysicalKey {
			return records[left].PhysicalKey < records[right].PhysicalKey
		}
		if records[left].VersionID != records[right].VersionID {
			return records[left].VersionID < records[right].VersionID
		}
		return records[left].Kind < records[right].Kind
	})
	graph := RcloneNativeStableGraph{Records: records, RecordCount: uint64(len(records)), PageCount: value.PageCount}
	latestByKey := make(map[string]struct{})
	for index, record := range records {
		if !validRcloneNativeVersionRecord(record) {
			return RcloneNativeStableGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
		}
		if index > 0 && records[index-1].PhysicalKey == record.PhysicalKey && records[index-1].VersionID == record.VersionID {
			return RcloneNativeStableGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
		}
		if record.IsLatest {
			if _, exists := latestByKey[record.PhysicalKey]; exists {
				return RcloneNativeStableGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
			}
			latestByKey[record.PhysicalKey] = struct{}{}
		}
		switch record.Kind {
		case RcloneNativeObjectVersion:
			graph.ObjectCount++
		case RcloneNativeDeleteMarker:
			graph.DeleteMarkerCount++
		}
	}
	keys := make(map[string]struct{})
	for _, record := range records {
		keys[record.PhysicalKey] = struct{}{}
	}
	if len(keys) != len(latestByKey) {
		return RcloneNativeStableGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, nil)
	}
	digest, err := digestRcloneNativeStableGraph(graph)
	if err != nil {
		return RcloneNativeStableGraph{}, rcloneNativeError(backupasset.RcloneReasonUnexpectedVersion, err)
	}
	graph.Digest = digest
	return graph, nil
}

func validateRcloneNativeStableGraph(value RcloneNativeStableGraph) error {
	normalized, err := normalizeRcloneNativeObservation(RcloneNativeFullObservation{Records: value.Records, PageCount: value.PageCount})
	if err != nil {
		return err
	}
	if normalized.Digest != value.Digest || normalized.RecordCount != value.RecordCount || normalized.ObjectCount != value.ObjectCount ||
		normalized.DeleteMarkerCount != value.DeleteMarkerCount || !equalRcloneNativeRecords(normalized.Records, value.Records, true) {
		return fmt.Errorf("invalid stable native version graph")
	}
	return nil
}

func equalRcloneNativeStableGraphs(left, right RcloneNativeStableGraph) bool {
	return left.Digest == right.Digest && left.RecordCount == right.RecordCount && left.ObjectCount == right.ObjectCount &&
		left.DeleteMarkerCount == right.DeleteMarkerCount && left.PageCount == right.PageCount && equalRcloneNativeRecords(left.Records, right.Records, true)
}

func equalRcloneNativeRecords(left, right []RcloneNativeVersionRecord, includeLatest bool) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !equalRcloneNativeImmutableVersion(left[index], right[index]) || includeLatest && left[index].IsLatest != right[index].IsLatest {
			return false
		}
	}
	return true
}

func equalRcloneNativeImmutableVersion(left, right RcloneNativeVersionRecord) bool {
	return left.PhysicalKey == right.PhysicalKey && left.VersionID == right.VersionID && left.Kind == right.Kind && left.Size == right.Size &&
		left.LastModified.Equal(right.LastModified) && left.ContentDigest == right.ContentDigest && left.EncryptionProfile == right.EncryptionProfile &&
		left.KMSKeyDigest == right.KMSKeyDigest && left.BucketKeyEnabled == right.BucketKeyEnabled
}

func validRcloneNativeVersionRecord(value RcloneNativeVersionRecord) bool {
	if !validRcloneNativeVersionIdentity(value.PhysicalKey, value.VersionID, value.Kind) || !validRcloneNativeUTCTime(value.LastModified) {
		return false
	}
	if value.Kind == RcloneNativeDeleteMarker {
		return value.Size == 0 && value.ContentDigest == "" && value.EncryptionProfile == "" && value.KMSKeyDigest == "" && !value.BucketKeyEnabled
	}
	if value.ContentDigest != "" && !validRcloneNativeDigest(value.ContentDigest) {
		return false
	}
	switch value.EncryptionProfile {
	case "":
		return value.KMSKeyDigest == "" && !value.BucketKeyEnabled
	case RcloneNativeSSES3V1:
		return value.KMSKeyDigest == "" && !value.BucketKeyEnabled
	case RcloneNativeSSEKMSV1:
		return validRcloneNativeDigest(value.KMSKeyDigest)
	default:
		return false
	}
}

func validRcloneNativeVersionIdentity(physicalKey, versionID string, kind RcloneNativeVersionKind) bool {
	return (kind == RcloneNativeObjectVersion || kind == RcloneNativeDeleteMarker) && validRcloneNativePhysicalKey(physicalKey) &&
		validRcloneNativeVersionID(versionID)
}

func validRcloneNativePhysicalKey(value string) bool {
	return value != "" && len(value) <= rcloneNativeMaximumPhysicalKeyBytes && utf8.ValidString(value) &&
		!strings.HasPrefix(value, "/") && !strings.ContainsRune(value, '\x00')
}

func validRcloneNativeVersionID(value string) bool {
	return value != "" && len(value) <= rcloneNativeMaximumVersionIDBytes && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validRcloneNativeContentEvidence(value RcloneNativePointViewEntry) bool {
	if !validRcloneNativeDigest(value.ContentDigest) {
		return false
	}
	switch value.EncryptionProfile {
	case RcloneNativeSSES3V1:
		return value.KMSKeyDigest == "" && !value.BucketKeyEnabled
	case RcloneNativeSSEKMSV1:
		return validRcloneNativeDigest(value.KMSKeyDigest)
	default:
		return false
	}
}

func rcloneNativeVersionIdentity(physicalKey, versionID string) string {
	return physicalKey + "\x00" + versionID
}

func rcloneNativePointViewFromRecord(logicalPath string, record RcloneNativeVersionRecord) RcloneNativePointViewEntry {
	return RcloneNativePointViewEntry{
		LogicalPath: logicalPath, PhysicalKey: record.PhysicalKey, VersionID: record.VersionID, Kind: record.Kind,
		Size: record.Size, LastModified: record.LastModified, ContentDigest: record.ContentDigest,
		EncryptionProfile: record.EncryptionProfile, KMSKeyDigest: record.KMSKeyDigest, BucketKeyEnabled: record.BucketKeyEnabled,
	}
}

func digestRcloneNativeStableGraph(value RcloneNativeStableGraph) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-native-version-graph-v1")
	writer.Uint64(uint64(value.PageCount))
	writer.Uint64(uint64(len(value.Records)))
	for _, record := range value.Records {
		writeRcloneNativeVersionRecord(writer, record, true)
	}
	return writer.HexDigest()
}

func digestRcloneNativePointView(entries []RcloneNativePointViewEntry) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-native-point-view-v1")
	writer.Uint64(uint64(len(entries)))
	for _, entry := range entries {
		writer.String(entry.LogicalPath)
		writeRcloneNativePointEntry(writer, entry)
	}
	return writer.HexDigest()
}

func digestRcloneNativeMutationLedger(entries []RcloneNativeMutationLedgerEntry) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-native-mutation-ledger-v1")
	writer.Uint64(uint64(len(entries)))
	for _, entry := range entries {
		writer.String(entry.LogicalPath)
		writer.String(entry.PhysicalKey)
		writer.String(entry.VersionID)
		writer.String(string(entry.Kind))
		writer.Uint64(entry.Size)
		writer.Int64(entry.LastModified.UnixNano())
		writer.String(string(entry.Disposition))
		writer.String(string(entry.EncryptionProfile))
		writer.String(entry.KMSKeyDigest)
		if entry.BucketKeyEnabled {
			writer.Uint8(1)
		} else {
			writer.Uint8(0)
		}
	}
	return writer.HexDigest()
}

func digestRcloneNativeExactProof(entry RcloneNativePointViewEntry, contentDigest string) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-native-exact-object-proof-v1")
	writeRcloneNativePointEntry(writer, entry)
	writer.String(contentDigest)
	return writer.HexDigest()
}

func digestRcloneNativeExactRangeProof(entry RcloneNativePointViewEntry, offset, length uint64, contentDigest string) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-native-exact-range-proof-v1")
	writeRcloneNativePointEntry(writer, entry)
	writer.Uint64(offset)
	writer.Uint64(length)
	writer.String(contentDigest)
	return writer.HexDigest()
}

func digestRcloneNativeControlGraph(value RcloneNativeControlCommitGraph) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-native-control-graph-v1")
	writer.Uint64(uint64(len(value.ManifestVersions)))
	for _, version := range value.ManifestVersions {
		writeRcloneNativeControlVersion(writer, version)
	}
	writeRcloneNativeControlVersion(writer, value.IndexVersion)
	writeRcloneNativeControlVersion(writer, value.CommitVersion)
	return writer.HexDigest()
}

func writeRcloneNativeControlVersion(writer *backupasset.CanonicalSHA256, value RcloneNativeControlObjectVersion) {
	writer.String(value.PhysicalKey)
	writer.String(value.VersionID)
	writer.Uint64(value.Size)
	writer.String(value.ContentDigest)
	writer.String(string(value.EncryptionProfile))
	writer.String(value.KMSKeyDigest)
	if value.BucketKeyEnabled {
		writer.Uint8(1)
	} else {
		writer.Uint8(0)
	}
	writer.String(value.EvidenceDigest)
}

func writeRcloneNativeVersionRecord(writer *backupasset.CanonicalSHA256, record RcloneNativeVersionRecord, includeLatest bool) {
	writer.String(record.PhysicalKey)
	writer.String(record.VersionID)
	writer.String(string(record.Kind))
	if includeLatest && record.IsLatest {
		writer.Uint8(1)
	} else {
		writer.Uint8(0)
	}
	writer.Uint64(record.Size)
	writer.Int64(record.LastModified.UnixNano())
	writer.String(record.ContentDigest)
	writer.String(string(record.EncryptionProfile))
	writer.String(record.KMSKeyDigest)
	if record.BucketKeyEnabled {
		writer.Uint8(1)
	} else {
		writer.Uint8(0)
	}
}

func writeRcloneNativePointEntry(writer *backupasset.CanonicalSHA256, entry RcloneNativePointViewEntry) {
	writer.String(entry.PhysicalKey)
	writer.String(entry.VersionID)
	writer.String(string(entry.Kind))
	writer.Uint64(entry.Size)
	writer.Int64(entry.LastModified.UnixNano())
	writer.String(entry.ContentDigest)
	writer.String(string(entry.EncryptionProfile))
	writer.String(entry.KMSKeyDigest)
	if entry.BucketKeyEnabled {
		writer.Uint8(1)
	} else {
		writer.Uint8(0)
	}
}
