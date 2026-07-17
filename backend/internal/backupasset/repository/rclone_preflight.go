package repository

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
)

const (
	rcloneNativePreflightSettleWindow  = 15 * time.Minute
	rcloneNativePreflightSessionMargin = 2 * time.Minute
	rcloneNativePreflightKMSBytes      = 16 << 10
)

type RcloneNativePreflightFactory interface {
	provider.STSAssumer
	provider.BootstrapDenyProbe
	provider.RcloneNativeClientFactory
	BootstrapCredentialsExpire(context.Context) (bool, error)
}

type RcloneNativePreflightFactoryBuilder func(
	context.Context,
	provider.RcloneNativeBootstrap,
	string,
	int,
) (RcloneNativePreflightFactory, error)

type RcloneProductionPreflightDependencies struct {
	CommandPlane  provider.RclonePreflightCommandPlane
	IdentityKey   []byte `json:"-"`
	Now           func() time.Time
	Random        io.Reader
	NativeFactory RcloneNativePreflightFactoryBuilder
}

type rcloneNativeSettleObservation struct {
	versioningDigest string
	lifecycleDigest  string
	encryptionDigest string
	firstObservedAt  time.Time
}

type productionRcloneVersioningPreflighter struct {
	commands      provider.RclonePreflightCommandPlane
	identityKey   []byte
	now           func() time.Time
	random        io.Reader
	nativeFactory RcloneNativePreflightFactoryBuilder

	mu           sync.Mutex
	observations map[string]rcloneNativeSettleObservation
}

func NewProductionRcloneVersioningPreflighter(
	dependencies RcloneProductionPreflightDependencies,
) (RcloneVersioningPreflighter, error) {
	if dependencies.CommandPlane == nil || len(dependencies.IdentityKey) < 32 {
		return nil, fmt.Errorf("%w: incomplete production Rclone preflight dependencies", backupasset.ErrInvalidState)
	}
	if dependencies.Now == nil {
		dependencies.Now = func() time.Time { return time.Now().UTC() }
	}
	if dependencies.Random == nil {
		dependencies.Random = rand.Reader
	}
	if dependencies.NativeFactory == nil {
		dependencies.NativeFactory = func(
			ctx context.Context,
			bootstrap provider.RcloneNativeBootstrap,
			region string,
			maxAttempts int,
		) (RcloneNativePreflightFactory, error) {
			return provider.NewRcloneNativeAWSFactory(ctx, bootstrap, region, maxAttempts)
		}
	}
	return &productionRcloneVersioningPreflighter{
		commands: dependencies.CommandPlane, identityKey: append([]byte(nil), dependencies.IdentityKey...),
		now: dependencies.Now, random: dependencies.Random, nativeFactory: dependencies.NativeFactory,
		observations: make(map[string]rcloneNativeSettleObservation),
	}, nil
}

func (preflight *productionRcloneVersioningPreflighter) PreflightPortable(
	ctx context.Context,
	input RclonePortablePreflightInput,
) (RcloneVersioningPreflightEvidence, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if preflight == nil || backupasset.ValidateOpaqueID(input.PreflightID) != nil || input.BindingRevision == math.MaxUint64 {
		return RcloneVersioningPreflightEvidence{}, fmt.Errorf("%w: invalid portable Rclone preflight input", backupasset.ErrInvalidState)
	}
	root, err := provider.NewRclonePrivateLocator(input.ManagedRootLocator)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	result, err := preflight.commands.PreflightPortable(ctx, provider.RclonePortableCommandPreflightRequest{
		PreflightID: input.PreflightID, BoundConfig: input.BoundConfig, ManagedRoot: root, Runtime: input.Runtime,
		AbsoluteDeadline: input.AbsoluteDeadline, LowLevelRetries: input.LowLevelRetries,
		ControlPayloadMaxBytes: input.ControlPayloadMaxBytes, FullVerifyMaxBytes: input.FullVerifyMaxBytes,
		ManifestOptions: input.ManifestOptions,
	})
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	evidence := RcloneVersioningPreflightEvidence{
		Mode: backupasset.PublicationVersionedPrefix, CapabilityRevision: input.BindingRevision + 1,
		ConsistencyClass: backupasset.RcloneConsistencyObservationallyStable,
		HashFidelity:     backupasset.RcloneHashDownloadVerifiedBytes, EstimatedReadBytes: result.VerifiedBytes,
		APICostClass: backupasset.RcloneCostLow, StorageCostClass: backupasset.RcloneCostLow, EgressCostClass: backupasset.RcloneCostLow,
		EncryptionProfile: backupasset.RcloneEncryptionNone, KMSKeyStatus: backupasset.RcloneKMSNotApplicable,
		ManagedRootIdentityDigest: result.ManagedRootIdentityDigest, RepositoryMarkerDigest: result.RepositoryMarkerDigest,
		EvidenceDigest: result.EvidenceDigest,
	}
	if err := validateRcloneVersioningPreflightEvidence(evidence); err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	return evidence, nil
}

func (preflight *productionRcloneVersioningPreflighter) PreflightNative(
	ctx context.Context,
	input RcloneNativePreflightInput,
) (RcloneVersioningPreflightEvidence, error) {
	if preflight == nil || preflight.now == nil {
		return RcloneVersioningPreflightEvidence{}, fmt.Errorf("%w: invalid native Rclone preflight input", backupasset.ErrInvalidState)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := preflight.now().UTC()
	if backupasset.ValidateOpaqueID(input.PreflightID) != nil || input.BindingRevision == math.MaxUint64 ||
		input.Request.Validate() != nil || !input.AbsoluteDeadline.After(now) || input.KMSReadKeyMaxCount <= 0 ||
		input.ObservationLimits.PageSize <= 0 || input.ObservationLimits.MaxPages <= 0 || input.ObservationLimits.MaxRecords <= 0 {
		return RcloneVersioningPreflightEvidence{}, fmt.Errorf("%w: invalid native Rclone preflight input", backupasset.ErrInvalidState)
	}
	profile := provider.RcloneNativeProfile{
		Code: provider.RcloneNativeAWSS3GeneralPurposeV1, Region: input.Request.Region, Bucket: input.Request.Bucket,
		ManagedPrefix: input.Request.ManagedPrefix, EndpointMode: provider.RcloneNativeEndpointAWSRegional,
		AddressingMode: provider.RcloneNativeAddressingDNS, BucketKind: provider.RcloneNativeBucketGeneralPurpose,
	}
	if err := provider.ValidateRcloneNativeProfile(profile); err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	bootstrap, err := rcloneNativeBootstrap(input.Request.Bootstrap)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	factory, err := preflight.nativeFactory(ctx, bootstrap, profile.Region, input.AWSSDKMaxAttempts)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	bootstrapTemporary, err := factory.BootstrapCredentialsExpire(ctx)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	selection, err := rcloneNativeEncryptionSelection(input)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	initialSession, err := provider.EstablishRcloneNativeSession(ctx, factory, factory, provider.RcloneNativeSessionRequest{
		Profile: profile, RoleARN: input.Request.RoleARN, ExternalID: input.ExternalID,
		PointDeadlineAt: input.AbsoluteDeadline, SessionMargin: rcloneNativePreflightSessionMargin,
		BootstrapTemporary: bootstrapTemporary, Encryption: selection,
	}, now, preflight.random)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	initialS3, err := factory.S3(initialSession.Session, profile, nil)
	if err != nil || initialS3 == nil {
		return RcloneVersioningPreflightEvidence{}, fmt.Errorf("%w: native Rclone S3 admission unavailable", backupasset.ErrInvalidState)
	}
	identity, err := initialS3.BucketIdentity(ctx, profile)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	accountID, roleOK := managedRcloneAWSRoleAccount(input.Request.RoleARN)
	if !roleOK || identity.AccountID != accountID || identity.Region != profile.Region || identity.Kind != provider.RcloneNativeBucketGeneralPurpose {
		return RcloneVersioningPreflightEvidence{}, fmt.Errorf("%w: native Rclone bucket identity mismatch", backupasset.ErrConflict)
	}
	versioning, lifecycle, bucketEncryption, err := observeRcloneNativeAdmission(ctx, initialS3, profile)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	versioningDigest, err := provider.CanonicalRcloneNativeVersioningDigest(versioning)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	lifecycleDigest, err := provider.CanonicalRcloneNativeLifecycleDigest(lifecycle)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	encryptionDigest, err := preflight.digest("bucket-encryption-v1", bucketEncryption)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	observationKey, err := preflight.digest("settle-key-v1", struct {
		TaskID          uint
		BindingRevision uint64
		Profile         provider.RcloneNativeProfile
	}{input.TaskID, input.BindingRevision, profile})
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	firstObservedAt, settled := preflight.observeNativeSettle(
		observationKey, versioningDigest, lifecycleDigest, encryptionDigest, now,
	)
	if !settled {
		return rcloneNativeSettlingEvidence(input, initialSession.Session.ExpiresAt(), firstObservedAt), nil
	}
	if err := provider.ValidateRcloneNativeVersioning(versioning, versioningDigest, firstObservedAt, now); err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	if err := provider.ValidateRcloneNativeLifecycle(lifecycle, profile.ManagedPrefix, lifecycleDigest, firstObservedAt, now); err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}

	keys, bindings, encryptionEvidence, err := inspectRcloneNativeKeys(
		ctx, factory, initialSession.Session, profile, selection, bucketEncryption,
		input.KMSReadKeyMaxCount,
	)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	_ = keys
	finalSession := initialSession
	if encryptionEvidence.BucketKeyEnabled {
		finalSession, err = provider.EstablishRcloneNativeSession(ctx, factory, factory, provider.RcloneNativeSessionRequest{
			Profile: profile, RoleARN: input.Request.RoleARN, ExternalID: input.ExternalID,
			PointDeadlineAt: input.AbsoluteDeadline, SessionMargin: rcloneNativePreflightSessionMargin,
			BootstrapTemporary: bootstrapTemporary, Encryption: selection, BucketKeyEnabled: true,
		}, now, preflight.random)
		if err != nil {
			return RcloneVersioningPreflightEvidence{}, err
		}
	}
	nativeS3, err := factory.S3(finalSession.Session, profile, bindings)
	if err != nil || nativeS3 == nil {
		return RcloneVersioningPreflightEvidence{}, fmt.Errorf("%w: native Rclone S3 client unavailable", backupasset.ErrInvalidState)
	}
	canaryS3, ok := nativeS3.(provider.RcloneNativeCanaryS3)
	if !ok {
		return RcloneVersioningPreflightEvidence{}, fmt.Errorf("%w: native Rclone canary capability unavailable", backupasset.ErrInvalidState)
	}
	controlPrefix := profile.ManagedPrefix + "control/preflight/" + input.PreflightID + "/"
	b0, err := provider.CaptureRcloneNativeStableGraph(ctx, canaryS3, controlPrefix, input.ObservationLimits)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	physicalKey := controlPrefix + "canary.bin"
	destination, err := provider.NewRclonePrivateLocator("xirang_native:" + profile.Bucket + "/" + physicalKey)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	commandEvidence, err := preflight.commands.WriteNativeCanary(ctx, provider.RcloneNativeCommandPreflightRequest{
		PreflightID: input.PreflightID, RcloneConfig: finalSession.RcloneConfig, Destination: destination,
		Runtime: input.Runtime, AbsoluteDeadline: input.AbsoluteDeadline, LowLevelRetries: input.LowLevelRetries,
		ControlPayloadMaxBytes: input.ControlPayloadMaxBytes,
	})
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	b1, err := provider.CaptureRcloneNativeStableGraph(ctx, canaryS3, controlPrefix, input.ObservationLimits)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	object, err := exactRcloneNativeCanaryVersion(b0, b1, physicalKey, commandEvidence, encryptionEvidence)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	entry := provider.RcloneNativePointViewEntry{
		LogicalPath: "canary.bin", PhysicalKey: object.PhysicalKey, VersionID: object.VersionID,
		Kind: object.Kind, Size: object.Size, LastModified: object.LastModified, ContentDigest: object.ContentDigest,
		EncryptionProfile: object.EncryptionProfile, KMSKeyDigest: object.KMSKeyDigest, BucketKeyEnabled: object.BucketKeyEnabled,
	}
	fullProof, err := provider.VerifyRcloneNativeExactObject(ctx, canaryS3, entry, input.FullVerifyMaxBytes)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	rangeProof, err := provider.VerifyRcloneNativeExactRange(
		ctx, canaryS3, entry, 0, commandEvidence.RangeBytes, commandEvidence.RangeDigest, input.FullVerifyMaxBytes,
	)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	deleted, err := canaryS3.DeleteCurrentCanary(ctx, provider.RcloneNativeCurrentDeleteRequest{Profile: profile, PhysicalKey: physicalKey})
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	b2, err := provider.CaptureRcloneNativeStableGraph(ctx, canaryS3, controlPrefix, input.ObservationLimits)
	if err != nil || !rcloneNativeDeleteMarkerPresent(b2, physicalKey, deleted.VersionID) {
		if err != nil {
			return RcloneVersioningPreflightEvidence{}, err
		}
		return RcloneVersioningPreflightEvidence{}, fmt.Errorf("%w: native Rclone delete marker missing", backupasset.ErrInvalidState)
	}
	postVersioning, postLifecycle, postEncryption, err := observeRcloneNativeAdmission(ctx, canaryS3, profile)
	if err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	postVersioningDigest, _ := provider.CanonicalRcloneNativeVersioningDigest(postVersioning)
	postLifecycleDigest, _ := provider.CanonicalRcloneNativeLifecycleDigest(postLifecycle)
	postEncryptionDigest, _ := preflight.digest("bucket-encryption-v1", postEncryption)
	if postVersioningDigest != versioningDigest || postLifecycleDigest != lifecycleDigest || postEncryptionDigest != encryptionDigest {
		preflight.resetNativeSettle(observationKey, postVersioningDigest, postLifecycleDigest, postEncryptionDigest, now)
		return rcloneNativeSettlingEvidence(input, finalSession.Session.ExpiresAt(), now), nil
	}

	managedIdentity, _ := preflight.digest("native-managed-root-v1", profile)
	markerDigest, _ := preflight.digest("native-marker-v1", struct {
		PhysicalKey   string
		ObjectVersion string
		DeleteVersion string
	}{physicalKey, object.VersionID, deleted.VersionID})
	canaryDigest, _ := preflight.digest("native-canary-v1", struct {
		B0, B1, B2 string
		Full       string
		Range      string
		Payload    string
	}{b0.Digest, b1.Digest, b2.Digest, fullProof.Digest, rangeProof.Digest, commandEvidence.PayloadDigest})
	evidenceDigest, _ := preflight.digest("native-evidence-v1", struct {
		Versioning, Lifecycle, Encryption, Canary string
	}{versioningDigest, lifecycleDigest, encryptionDigest, canaryDigest})
	publicEncryption := backupasset.RcloneEncryptionSSES3
	kmsStatus := backupasset.RcloneKMSNotApplicable
	if selection.Profile == provider.RcloneNativeSSEKMSV1 {
		publicEncryption = backupasset.RcloneEncryptionSSEKMS
		kmsStatus = backupasset.RcloneKMSReady
	}
	retained := make([]managedRcloneKMSReadKeyV3, 0, len(bindings))
	for _, binding := range bindings {
		if binding.KeyARN != selection.ActiveKeyARN {
			retained = append(retained, managedRcloneKMSReadKeyV3{KeyARN: binding.KeyARN, KeyDigest: binding.Digest})
		}
	}
	expiresAt := finalSession.Session.ExpiresAt()
	evidence := RcloneVersioningPreflightEvidence{
		Mode: backupasset.PublicationNativeObjectVersions, CapabilityRevision: input.BindingRevision + 1,
		ConsistencyClass: backupasset.RcloneConsistencyProviderStrong, HashFidelity: backupasset.RcloneHashDownloadVerifiedBytes,
		EstimatedReadBytes: commandEvidence.PayloadBytes + commandEvidence.RangeBytes,
		APICostClass:       backupasset.RcloneCostModerate, StorageCostClass: backupasset.RcloneCostLow, EgressCostClass: backupasset.RcloneCostLow,
		CredentialExpiresAt: &expiresAt, EncryptionProfile: publicEncryption, KMSKeyStatus: kmsStatus,
		KMSReadKeyCount: uint32(encryptionEvidence.RetainedReadKeyCount), ManagedRootIdentityDigest: managedIdentity,
		RepositoryMarkerDigest: markerDigest, EvidenceDigest: evidenceDigest,
		Native: &rcloneNativePreflightEvidence{
			VersioningDigest: versioningDigest, LifecycleDigest: lifecycleDigest,
			CapabilityStableObservedAt: firstObservedAt, BucketEncryptionDigest: encryptionDigest,
			BucketKeyEnabled: encryptionEvidence.BucketKeyEnabled, CanaryEncryptionEvidenceDigest: canaryDigest,
			ActiveKMSKeyDigest: encryptionEvidence.ActiveKeyDigest,
			KMSCapabilityRevision: func() uint64 {
				if selection.Profile == provider.RcloneNativeSSEKMSV1 {
					return input.BindingRevision + 1
				}
				return 0
			}(),
			RetainedReadKeys: retained,
		},
	}
	if err := validateRcloneVersioningPreflightEvidence(evidence); err != nil {
		return RcloneVersioningPreflightEvidence{}, err
	}
	return evidence, nil
}

func rcloneNativeBootstrap(input backupasset.RcloneNativeBootstrapInput) (provider.RcloneNativeBootstrap, error) {
	if err := input.Validate(); err != nil {
		return provider.RcloneNativeBootstrap{}, err
	}
	switch input.Mode {
	case backupasset.RcloneBootstrapWorkloadChain:
		return provider.RcloneNativeBootstrap{Kind: provider.RcloneNativeBootstrapWorkloadChain}, nil
	case backupasset.RcloneBootstrapStaticSTS:
		return provider.RcloneNativeBootstrap{
			Kind: provider.RcloneNativeBootstrapStaticSTS, AccessKeyID: input.AccessKeyID, SecretAccessKey: input.SecretAccessKey,
		}, nil
	default:
		return provider.RcloneNativeBootstrap{}, fmt.Errorf("%w: unsupported Rclone bootstrap", backupasset.ErrInvalidState)
	}
}

func rcloneNativeEncryptionSelection(input RcloneNativePreflightInput) (provider.RcloneNativeEncryptionSelection, error) {
	selection := provider.RcloneNativeEncryptionSelection{}
	switch input.Request.EncryptionProfile {
	case backupasset.RcloneEncryptionSSES3:
		selection.Profile = provider.RcloneNativeSSES3V1
	case backupasset.RcloneEncryptionSSEKMS:
		selection.Profile = provider.RcloneNativeSSEKMSV1
		selection.ActiveKeyARN = input.Request.KMSKeyARN
		for _, key := range input.RetainedReadKeys {
			selection.RetainedReadKeyARNs = append(selection.RetainedReadKeyARNs, key.KeyARN)
		}
	default:
		return provider.RcloneNativeEncryptionSelection{}, fmt.Errorf("%w: unsupported Rclone encryption", backupasset.ErrInvalidState)
	}
	return selection, nil
}

func observeRcloneNativeAdmission(
	ctx context.Context,
	s3 provider.S3Native,
	profile provider.RcloneNativeProfile,
) (provider.RcloneNativeVersioningObservation, provider.RcloneNativeLifecycleObservation, provider.RcloneNativeBucketEncryption, error) {
	versioning, err := s3.GetVersioning(ctx, profile)
	if err != nil {
		return provider.RcloneNativeVersioningObservation{}, provider.RcloneNativeLifecycleObservation{}, provider.RcloneNativeBucketEncryption{}, err
	}
	lifecycle, err := s3.GetLifecycle(ctx, profile)
	if err != nil {
		return provider.RcloneNativeVersioningObservation{}, provider.RcloneNativeLifecycleObservation{}, provider.RcloneNativeBucketEncryption{}, err
	}
	encryption, err := s3.GetEncryption(ctx, profile)
	return versioning, lifecycle, encryption, err
}

func inspectRcloneNativeKeys(
	ctx context.Context,
	factory RcloneNativePreflightFactory,
	session provider.RcloneNativeSession,
	profile provider.RcloneNativeProfile,
	selection provider.RcloneNativeEncryptionSelection,
	bucket provider.RcloneNativeBucketEncryption,
	maxReadKeys int,
) ([]provider.RcloneNativeKMSKey, []provider.RcloneNativeKMSKeyDigestBinding, provider.RcloneNativeEncryptionEvidence, error) {
	arns := append([]string{selection.ActiveKeyARN}, selection.RetainedReadKeyARNs...)
	if selection.Profile == provider.RcloneNativeSSES3V1 {
		arns = nil
	}
	keys := make([]provider.RcloneNativeKMSKey, 0, len(arns))
	bindings := make([]provider.RcloneNativeKMSKeyDigestBinding, 0, len(arns))
	if len(arns) > 0 {
		inspector, err := factory.KMS(session, profile.Region)
		if err != nil || inspector == nil {
			return nil, nil, provider.RcloneNativeEncryptionEvidence{}, fmt.Errorf("%w: native Rclone KMS unavailable", backupasset.ErrInvalidState)
		}
		for _, arn := range arns {
			key, err := inspector.DescribeKey(ctx, arn)
			if err != nil {
				return nil, nil, provider.RcloneNativeEncryptionEvidence{}, err
			}
			binding, err := provider.RcloneNativeKMSKeyBinding(key)
			if err != nil {
				return nil, nil, provider.RcloneNativeEncryptionEvidence{}, err
			}
			keys = append(keys, key)
			bindings = append(bindings, binding)
		}
	}
	evidence, err := provider.ValidateRcloneNativeEncryption(selection, bucket, keys, provider.RcloneNativeKMSLimits{
		MaxReadKeys: maxReadKeys, MaxSerializedBytes: rcloneNativePreflightKMSBytes,
	})
	return keys, bindings, evidence, err
}

func exactRcloneNativeCanaryVersion(
	b0, b1 provider.RcloneNativeStableGraph,
	physicalKey string,
	command provider.RcloneNativeCommandPreflightResult,
	encryption provider.RcloneNativeEncryptionEvidence,
) (provider.RcloneNativeVersionRecord, error) {
	if b0.RecordCount != 0 {
		return provider.RcloneNativeVersionRecord{}, fmt.Errorf("%w: native Rclone canary prefix was not fresh", backupasset.ErrConflict)
	}
	var found *provider.RcloneNativeVersionRecord
	for index := range b1.Records {
		record := b1.Records[index]
		if record.PhysicalKey != physicalKey {
			continue
		}
		if found != nil || record.Kind != provider.RcloneNativeObjectVersion || !record.IsLatest || record.Size != command.PayloadBytes ||
			record.ContentDigest != command.PayloadDigest || record.EncryptionProfile != encryption.Profile ||
			record.KMSKeyDigest != encryption.ActiveKeyDigest || record.BucketKeyEnabled != encryption.BucketKeyEnabled {
			return provider.RcloneNativeVersionRecord{}, fmt.Errorf("%w: invalid native Rclone canary version", backupasset.ErrInvalidState)
		}
		copy := record
		found = &copy
	}
	if found == nil {
		return provider.RcloneNativeVersionRecord{}, fmt.Errorf("%w: native Rclone canary version missing", backupasset.ErrInvalidState)
	}
	return *found, nil
}

func rcloneNativeDeleteMarkerPresent(graph provider.RcloneNativeStableGraph, physicalKey, versionID string) bool {
	count := 0
	for _, record := range graph.Records {
		if record.PhysicalKey == physicalKey && record.VersionID == versionID && record.Kind == provider.RcloneNativeDeleteMarker && record.IsLatest {
			count++
		}
	}
	return count == 1
}

func rcloneNativeSettlingEvidence(input RcloneNativePreflightInput, expiresAt, observedAt time.Time) RcloneVersioningPreflightEvidence {
	encryption := input.Request.EncryptionProfile
	kmsStatus := backupasset.RcloneKMSNotApplicable
	if encryption == backupasset.RcloneEncryptionSSEKMS {
		kmsStatus = backupasset.RcloneKMSReady
	}
	return RcloneVersioningPreflightEvidence{
		Settling: true, SettlingObservedAt: observedAt, Mode: backupasset.PublicationNativeObjectVersions,
		CredentialExpiresAt: &expiresAt, EncryptionProfile: encryption, KMSKeyStatus: kmsStatus,
	}
}

func (preflight *productionRcloneVersioningPreflighter) observeNativeSettle(
	key, versioningDigest, lifecycleDigest, encryptionDigest string,
	now time.Time,
) (time.Time, bool) {
	preflight.mu.Lock()
	defer preflight.mu.Unlock()
	record, exists := preflight.observations[key]
	if !exists || record.versioningDigest != versioningDigest || record.lifecycleDigest != lifecycleDigest || record.encryptionDigest != encryptionDigest {
		record = rcloneNativeSettleObservation{
			versioningDigest: versioningDigest, lifecycleDigest: lifecycleDigest,
			encryptionDigest: encryptionDigest, firstObservedAt: now,
		}
		preflight.observations[key] = record
		return now, false
	}
	return record.firstObservedAt, !now.Before(record.firstObservedAt.Add(rcloneNativePreflightSettleWindow))
}

func (preflight *productionRcloneVersioningPreflighter) resetNativeSettle(
	key, versioningDigest, lifecycleDigest, encryptionDigest string,
	now time.Time,
) {
	preflight.mu.Lock()
	preflight.observations[key] = rcloneNativeSettleObservation{
		versioningDigest: versioningDigest, lifecycleDigest: lifecycleDigest,
		encryptionDigest: encryptionDigest, firstObservedAt: now,
	}
	preflight.mu.Unlock()
}

func (preflight *productionRcloneVersioningPreflighter) digest(domain string, value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, preflight.identityKey)
	_, _ = io.WriteString(mac, "xirang-rclone-production-preflight-"+domain+"\n")
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

var _ RcloneVersioningPreflighter = (*productionRcloneVersioningPreflighter)(nil)
