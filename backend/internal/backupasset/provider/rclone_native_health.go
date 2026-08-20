package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"xirang/backend/internal/backupasset"
)

type RcloneNativeHealthS3 interface {
	BucketIdentity(context.Context, RcloneNativeProfile) (RcloneNativeBucketIdentity, error)
	GetVersioning(context.Context, RcloneNativeProfile) (RcloneNativeVersioningObservation, error)
	GetLifecycle(context.Context, RcloneNativeProfile) (RcloneNativeLifecycleObservation, error)
	GetEncryption(context.Context, RcloneNativeProfile) (RcloneNativeBucketEncryption, error)
	RcloneNativeExactReader
}

type RcloneNativeHealthDependencies struct {
	S3  RcloneNativeHealthS3
	KMS KMSKeyInspector
}

type RcloneNativeHealthReference struct {
	KMSKeyDigest string
	Entry        RcloneNativePointViewEntry
}

type RcloneNativeHealthRequest struct {
	Profile                RcloneNativeProfile
	ExpectedAccountID      string
	StableObservedAt       time.Time
	CheckedAt              time.Time
	VersioningDigest       string
	LifecycleDigest        string
	BucketEncryptionDigest string
	Encryption             RcloneNativeEncryptionSelection
	ExpectedEncryption     RcloneNativeEncryptionEvidence
	KMSLimits              RcloneNativeKMSLimits
	References             []RcloneNativeHealthReference
	MaxVerifyBytes         uint64
}

type RcloneNativeHealthResult struct {
	Reason                 backupasset.RcloneVersioningReasonCode
	EvidenceDigest         string
	VerifiedReferenceCount int
	VerifiedBytes          uint64
}

// LoadRcloneNativeHealthReferences reopens only the encrypted-locator commit
// key/version selected by the repository. It authenticates the commit marker
// and exact manifest graph before returning bounded exact object references;
// it never lists a current/latest control prefix.
func LoadRcloneNativeHealthReferences(
	ctx context.Context,
	reader S3Native,
	request RcloneNativePublicationRequest,
	commit RcloneCommitV1,
	maxReferences int,
) ([]RcloneNativeHealthReference, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reader == nil || maxReferences <= 0 || request.Attempt.Native == nil || request.Attempt.Portable != nil ||
		request.Attempt.PublicationMode != backupasset.PublicationNativeObjectVersions || commit.Native == nil || commit.Portable != nil ||
		commit.PublicationMode != backupasset.PublicationNativeObjectVersions || len(request.MarkerKey) < 32 ||
		request.ControlPayloadMaxBytes == 0 || request.ControlPayloadMaxBytes > math.MaxInt64 ||
		request.Attempt.Validate() != nil || commit.Validate() != nil || ValidateRcloneNativeProfile(request.Profile) != nil {
		return nil, rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, nil)
	}
	controlPrefix := rcloneNativeAttemptControlPrefix(request)
	if commit.Native.CommitKey != controlPrefix+"commit.json" || !validRcloneNativeVersionID(commit.Native.CommitVersionID) ||
		commit.RepositoryID != request.Attempt.RepositoryID || commit.TaskRepositoryLinkID != request.Attempt.TaskRepositoryLinkID ||
		commit.RecoveryPointID != request.Attempt.RecoveryPointID || commit.AttemptID != request.Attempt.AttemptID {
		return nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	request.s3 = reader
	readRequest := RcloneNativeExactReadRequest{PhysicalKey: commit.Native.CommitKey, VersionID: commit.Native.CommitVersionID}
	head, err := reader.HeadVersion(ctx, readRequest)
	if err != nil || head.PhysicalKey != readRequest.PhysicalKey || head.VersionID != readRequest.VersionID ||
		head.Size == 0 || head.Size > request.ControlPayloadMaxBytes {
		return nil, rcloneNativeHealthCallError(ctx, err)
	}
	candidate := RcloneNativeVersionRecord{
		PhysicalKey: head.PhysicalKey, VersionID: head.VersionID, Kind: RcloneNativeObjectVersion, Size: head.Size,
		EncryptionProfile: head.EncryptionProfile, KMSKeyDigest: head.KMSKeyDigest, BucketKeyEnabled: head.BucketKeyEnabled,
	}
	payload, commitVersion, err := readRcloneNativeCommitCandidate(ctx, request, candidate)
	if err != nil {
		return nil, err
	}
	if commitVersion.ContentDigest != commit.Native.CommitContentDigest {
		return nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}
	marker, err := decodeRcloneNativeCommitMarker(payload, request.MarkerKey)
	if err != nil {
		return nil, err
	}
	if err := validateRcloneNativeCommitMarker(request, controlPrefix, marker); err != nil {
		return nil, err
	}
	controlGraph, index, chunkDigests, payloads, err := reopenRcloneNativeControlGraph(ctx, request, marker, commitVersion)
	if err != nil {
		return nil, err
	}
	if emptyRcloneManifestBundle(request.Manifest) {
		request.Manifest = RcloneManifestBundle{
			Version: 1, IndexDigest: index.SourceManifestIndexDigest, ObservationDigest: index.SourceObservationDigest,
			EntryCount: index.EntryCount, LogicalBytes: index.LogicalBytes,
		}
	}
	point := RcloneNativePointGraph{ViewDigest: marker.PointViewDigest, LedgerDigest: marker.MutationLedgerDigest}
	rebuilt, err := buildRcloneNativeProviderCommit(
		request,
		point,
		marker.ExactReadProofDigest,
		RcloneNativeStableGraph{Digest: marker.B0VersionGraphDigest},
		RcloneNativeStableGraph{Digest: marker.B1VersionGraphDigest},
		controlGraph,
		chunkDigests,
		marker.ManifestIndexDigest,
		marker.ProviderCommittedAt,
	)
	if err != nil || rebuilt.Native == nil {
		return nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, err)
	}
	if err := attachRcloneNativeFrozenDeletionVersions(&rebuilt, point, controlGraph, payloads); err != nil {
		return nil, err
	}
	rebuilt.FidelityEvidenceDigest = marker.FidelityEvidenceDigest
	rebuilt.CostEvidenceDigest = marker.CostEvidenceDigest
	rebuilt.CapabilityEvidenceDigest = marker.CapabilityEvidenceDigest
	rebuilt.Native.EncryptionEvidenceDigest = marker.EncryptionEvidenceDigest
	if !rcloneNativeCommitsEqualIgnoringFrozenVersions(rebuilt, commit) {
		return nil, rcloneNativeError(backupasset.RcloneReasonMarkerMismatch, nil)
	}

	references := []RcloneNativeHealthReference{{
		KMSKeyDigest: commitVersion.KMSKeyDigest,
		Entry: RcloneNativePointViewEntry{
			PhysicalKey: commitVersion.PhysicalKey, VersionID: commitVersion.VersionID, Kind: RcloneNativeObjectVersion,
			Size: commitVersion.Size, ContentDigest: commitVersion.ContentDigest, EncryptionProfile: commitVersion.EncryptionProfile,
			KMSKeyDigest: commitVersion.KMSKeyDigest, BucketKeyEnabled: commitVersion.BucketKeyEnabled,
		},
	}}
	seenKeyDigests := map[string]struct{}{commitVersion.KMSKeyDigest: {}}
	seenSSES3Data := false
	for _, encodedReference := range marker.ManifestVersions {
		manifestPayload, _, openErr := reopenRcloneNativeControlVersion(ctx, request, encodedReference)
		if openErr != nil {
			return nil, openErr
		}
		entries, parseErr := rcloneNativeHealthEntries(manifestPayload)
		if parseErr != nil {
			return nil, parseErr
		}
		for _, entry := range entries {
			if entry.Kind != RcloneNativeObjectVersion || !validRcloneNativeContentEvidence(entry) {
				continue
			}
			if entry.EncryptionProfile == RcloneNativeSSES3V1 {
				if seenSSES3Data {
					continue
				}
				seenSSES3Data = true
			} else {
				if _, exists := seenKeyDigests[entry.KMSKeyDigest]; exists {
					continue
				}
				seenKeyDigests[entry.KMSKeyDigest] = struct{}{}
			}
			if len(references) >= maxReferences {
				return nil, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, nil)
			}
			references = append(references, RcloneNativeHealthReference{KMSKeyDigest: entry.KMSKeyDigest, Entry: entry})
		}
	}
	sort.Slice(references, func(left, right int) bool {
		if references[left].Entry.PhysicalKey != references[right].Entry.PhysicalKey {
			return references[left].Entry.PhysicalKey < references[right].Entry.PhysicalKey
		}
		return references[left].Entry.VersionID < references[right].Entry.VersionID
	})
	return references, nil
}

func rcloneNativeHealthEntries(payload []byte) ([]RcloneNativePointViewEntry, error) {
	lines := bytes.Split(payload, []byte{'\n'})
	entries := make([]RcloneNativePointViewEntry, 0)
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if rejectDuplicateJSONMembers(string(line)) != nil {
			return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, nil)
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var record rcloneNativeManifestRecordV1
		if err := decoder.Decode(&record); err != nil || record.Version != 1 {
			return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return nil, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
		}
		if record.RecordKind != "entry" || record.State == nil || record.State.Kind != RcloneNativeObjectVersion {
			continue
		}
		entries = append(entries, RcloneNativePointViewEntry{
			PhysicalKey: record.State.PhysicalKey, VersionID: record.State.VersionID, Kind: record.State.Kind,
			Size: record.State.Size, ContentDigest: record.State.ContentDigest, EncryptionProfile: record.State.EncryptionProfile,
			KMSKeyDigest: record.State.KMSKeyDigest, BucketKeyEnabled: record.State.BucketKeyEnabled,
		})
	}
	return entries, nil
}

func CanonicalRcloneNativeBucketEncryptionDigest(value RcloneNativeBucketEncryption) (string, error) {
	if !value.BlockedEncryptionTypesKnown {
		return "", fmt.Errorf("unknown AWS S3 blocked encryption types")
	}
	switch value.Algorithm {
	case "AES256":
		if value.KMSKeyARN != "" || value.BucketKeyEnabled {
			return "", fmt.Errorf("invalid AWS S3 SSE-S3 bucket encryption")
		}
	case "aws:kms":
		if _, _, ok := parseRcloneNativeKMSKeyARN(value.KMSKeyARN); !ok {
			return "", fmt.Errorf("invalid AWS S3 SSE-KMS bucket encryption")
		}
	default:
		return "", fmt.Errorf("unsupported AWS S3 bucket encryption")
	}
	return canonicalRcloneNativeDigest("bucket-encryption-v1", value)
}

func CheckRcloneNativeHealth(
	ctx context.Context,
	dependencies RcloneNativeHealthDependencies,
	request RcloneNativeHealthRequest,
) (RcloneNativeHealthResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if dependencies.S3 == nil || ValidateRcloneNativeProfile(request.Profile) != nil || !validRcloneNativeAccountID(request.ExpectedAccountID) ||
		!validRcloneNativeUTCTime(request.StableObservedAt) || !validRcloneNativeUTCTime(request.CheckedAt) || request.StableObservedAt.After(request.CheckedAt) ||
		!validRcloneNativeDigest(request.VersioningDigest) || !validRcloneNativeDigest(request.LifecycleDigest) ||
		!validRcloneNativeDigest(request.BucketEncryptionDigest) || request.MaxVerifyBytes == 0 || request.MaxVerifyBytes > math.MaxInt64 ||
		len(request.References) == 0 {
		return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonAdmissionBlocked, nil)
	}
	identity, err := dependencies.S3.BucketIdentity(ctx, request.Profile)
	if err != nil {
		return RcloneNativeHealthResult{}, rcloneNativeHealthCallError(ctx, err)
	}
	if identity.AccountID != request.ExpectedAccountID || identity.Region != request.Profile.Region || identity.Kind != RcloneNativeBucketGeneralPurpose {
		return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
	}
	versioning, err := dependencies.S3.GetVersioning(ctx, request.Profile)
	if err != nil {
		return RcloneNativeHealthResult{}, rcloneNativeHealthCallError(ctx, err)
	}
	if err := ValidateRcloneNativeVersioning(versioning, request.VersioningDigest, request.StableObservedAt, request.CheckedAt); err != nil {
		return RcloneNativeHealthResult{}, err
	}
	lifecycle, err := dependencies.S3.GetLifecycle(ctx, request.Profile)
	if err != nil {
		return RcloneNativeHealthResult{}, rcloneNativeHealthCallError(ctx, err)
	}
	if err := ValidateRcloneNativeLifecycle(lifecycle, request.Profile.ManagedPrefix, request.LifecycleDigest, request.StableObservedAt, request.CheckedAt); err != nil {
		return RcloneNativeHealthResult{}, err
	}
	bucketEncryption, err := dependencies.S3.GetEncryption(ctx, request.Profile)
	if err != nil {
		return RcloneNativeHealthResult{}, rcloneNativeHealthCallError(ctx, err)
	}
	bucketEncryptionDigest, err := CanonicalRcloneNativeBucketEncryptionDigest(bucketEncryption)
	if err != nil {
		return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, err)
	}
	if bucketEncryptionDigest != request.BucketEncryptionDigest {
		return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
	}

	keys, requiredReferenceDigests, err := loadRcloneNativeHealthKeys(ctx, dependencies.KMS, request.Encryption)
	if err != nil {
		return RcloneNativeHealthResult{}, err
	}
	encryptionEvidence, err := ValidateRcloneNativeEncryption(request.Encryption, bucketEncryption, keys, request.KMSLimits)
	if err != nil {
		return RcloneNativeHealthResult{}, err
	}
	if !equalRcloneNativeEncryptionEvidence(encryptionEvidence, request.ExpectedEncryption) {
		return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
	}

	proofDigests := make([]string, 0, len(request.References))
	result := RcloneNativeHealthResult{Reason: backupasset.RcloneReasonReady}
	for _, reference := range request.References {
		if result.VerifiedBytes > request.MaxVerifyBytes || reference.Entry.Size > request.MaxVerifyBytes-result.VerifiedBytes {
			return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonProviderResourceLimit, nil)
		}
		if request.Encryption.Profile == RcloneNativeSSES3V1 {
			if reference.KMSKeyDigest != "" || reference.Entry.KMSKeyDigest != "" || reference.Entry.EncryptionProfile != RcloneNativeSSES3V1 {
				return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
			}
		} else {
			if reference.KMSKeyDigest == "" || reference.KMSKeyDigest != reference.Entry.KMSKeyDigest ||
				reference.Entry.EncryptionProfile != RcloneNativeSSEKMSV1 {
				return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
			}
			if _, required := requiredReferenceDigests[reference.KMSKeyDigest]; !required {
				return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonIdentityMismatch, nil)
			}
			delete(requiredReferenceDigests, reference.KMSKeyDigest)
		}
		proof, verifyErr := VerifyRcloneNativeExactObject(ctx, dependencies.S3, reference.Entry, request.MaxVerifyBytes-result.VerifiedBytes)
		if verifyErr != nil {
			return RcloneNativeHealthResult{}, verifyErr
		}
		proofDigests = append(proofDigests, proof.Digest)
		result.VerifiedReferenceCount++
		result.VerifiedBytes += proof.VerifiedBytes
	}
	if len(requiredReferenceDigests) != 0 {
		return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonKMSKeyUnavailable, nil)
	}
	sort.Strings(proofDigests)
	result.EvidenceDigest, err = digestRcloneNativeHealthResult(request, encryptionEvidence, proofDigests, result)
	if err != nil {
		return RcloneNativeHealthResult{}, rcloneNativeError(backupasset.RcloneReasonManifestMismatch, err)
	}
	return result, nil
}

func loadRcloneNativeHealthKeys(
	ctx context.Context,
	inspector KMSKeyInspector,
	selection RcloneNativeEncryptionSelection,
) ([]RcloneNativeKMSKey, map[string]struct{}, error) {
	if selection.Profile == RcloneNativeSSES3V1 {
		if selection.ActiveKeyARN != "" || len(selection.RetainedReadKeyARNs) != 0 {
			return nil, nil, rcloneNativeError(backupasset.RcloneReasonEncryptionUnsupported, nil)
		}
		return nil, map[string]struct{}{}, nil
	}
	if selection.Profile != RcloneNativeSSEKMSV1 || inspector == nil {
		return nil, nil, rcloneNativeError(backupasset.RcloneReasonKMSKeyUnavailable, nil)
	}
	arns := append([]string{selection.ActiveKeyARN}, selection.RetainedReadKeyARNs...)
	keys := make([]RcloneNativeKMSKey, 0, len(arns))
	required := make(map[string]struct{}, len(arns))
	for _, arn := range arns {
		key, err := inspector.DescribeKey(ctx, arn)
		if err != nil {
			return nil, nil, rcloneNativeHealthCallError(ctx, err)
		}
		keys = append(keys, key)
		digest := rcloneNativeKMSKeyDigest(key)
		if !validRcloneNativeDigest(digest) {
			return nil, nil, rcloneNativeError(backupasset.RcloneReasonKMSKeyUnavailable, nil)
		}
		required[digest] = struct{}{}
	}
	return keys, required, nil
}

func equalRcloneNativeEncryptionEvidence(left, right RcloneNativeEncryptionEvidence) bool {
	return left.Profile == right.Profile && left.BucketKeyEnabled == right.BucketKeyEnabled &&
		left.ActiveKeyDigest == right.ActiveKeyDigest && left.ReadKeySetDigest == right.ReadKeySetDigest &&
		left.RetainedReadKeyCount == right.RetainedReadKeyCount
}

func digestRcloneNativeHealthResult(
	request RcloneNativeHealthRequest,
	encryption RcloneNativeEncryptionEvidence,
	proofDigests []string,
	result RcloneNativeHealthResult,
) (string, error) {
	writer := backupasset.NewCanonicalSHA256()
	writer.String("xirang-rclone-native-health-v1")
	writer.String(request.VersioningDigest)
	writer.String(request.LifecycleDigest)
	writer.String(request.BucketEncryptionDigest)
	writer.String(string(encryption.Profile))
	writer.String(encryption.ActiveKeyDigest)
	writer.String(encryption.ReadKeySetDigest)
	writer.Uint64(uint64(encryption.RetainedReadKeyCount))
	writer.Int64(request.CheckedAt.UnixNano())
	writer.Uint64(uint64(result.VerifiedReferenceCount))
	writer.Uint64(result.VerifiedBytes)
	for _, digest := range proofDigests {
		writer.String(digest)
	}
	return writer.HexDigest()
}

func rcloneNativeHealthCallError(ctx context.Context, err error) error {
	if rcloneNativeReason(err) != "" {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return rcloneNativeError(backupasset.RcloneReasonProviderTimeout, err)
	}
	return rcloneNativeError(backupasset.RcloneReasonProviderUnavailable, err)
}

// RcloneNativeFailureReason exposes only the closed safe reason code carried
// by native provider failures. Raw SDK/provider causes remain private.
func RcloneNativeFailureReason(err error) backupasset.RcloneVersioningReasonCode {
	if reason := rcloneNativeReason(err); reason != "" {
		return reason
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return backupasset.RcloneReasonProviderTimeout
	}
	if err != nil {
		return backupasset.RcloneReasonProviderUnavailable
	}
	return ""
}
