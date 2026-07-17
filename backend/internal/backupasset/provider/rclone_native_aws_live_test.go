//go:build aws_live

package provider

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	rcloneNativeAWSLiveSettleDuration = 15 * time.Minute
	rcloneNativeAWSLiveMultipartBytes = int64(201 << 20)
)

type rcloneNativeAWSLiveConfig struct {
	region         string
	bucket         string
	roleARN        string
	externalID     string
	activeKeyARN   string
	retainedKeyARN string
	adminProfile   string
	rcloneBinary   string
	runPrefix      string
	accountID      string
}

type rcloneNativeAWSLiveSession struct {
	result     RcloneNativeSessionResult
	adapter    S3Native
	evidence   RcloneNativeEncryptionEvidence
	bindings   []RcloneNativeKMSKeyDigestBinding
	encryption RcloneNativeEncryptionSelection
}

func TestRcloneAWSLiveConformance(t *testing.T) {
	config := loadRcloneNativeAWSLiveConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
	defer cancel()

	factory, err := NewRcloneNativeAWSFactory(ctx, RcloneNativeBootstrap{Kind: RcloneNativeBootstrapWorkloadChain}, config.region, 3)
	if err != nil {
		t.Fatalf("create official AWS production factory: %v", err)
	}
	adminConfig, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(config.region), awsconfig.WithSharedConfigProfile(config.adminProfile), awsconfig.WithRetryMaxAttempts(3),
	)
	if err != nil {
		t.Fatalf("load separately authorized cleanup fixture: %v", err)
	}
	bootstrapCredentials, err := factory.baseConfig.Credentials.Retrieve(ctx)
	if err != nil {
		t.Fatalf("resolve bootstrap identity: %v", err)
	}
	adminCredentials, err := adminConfig.Credentials.Retrieve(ctx)
	if err != nil {
		t.Fatalf("resolve cleanup identity: %v", err)
	}
	if bootstrapCredentials.AccessKeyID == "" || adminCredentials.AccessKeyID == "" ||
		bootstrapCredentials.AccessKeyID == adminCredentials.AccessKeyID {
		t.Fatal("live fixture must use distinct bootstrap and cleanup-admin identities")
	}
	admin := s3.NewFromConfig(adminConfig, func(options *s3.Options) { options.UsePathStyle = false })
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cleanupCancel()
		if err := cleanupRcloneNativeAWSLivePrefix(cleanupContext, admin, config); err != nil {
			t.Errorf("clean exact randomized AWS live prefix: %v", err)
		}
	})

	sseProfile := rcloneNativeAWSLiveProfile(config, "sse-s3/")
	sseSelection := RcloneNativeEncryptionSelection{Profile: RcloneNativeSSES3V1}
	sseSession := establishRcloneNativeAWSLiveSession(t, ctx, factory, config, sseProfile, sseSelection, false, nil)
	firstObservedAt := time.Now().UTC()
	firstVersioning, err := sseSession.adapter.GetVersioning(ctx, sseProfile)
	if err != nil {
		t.Fatalf("read initial live versioning: %v", err)
	}
	firstVersioningDigest, err := CanonicalRcloneNativeVersioningDigest(firstVersioning)
	if err != nil {
		t.Fatalf("canonicalize initial live versioning: %v", err)
	}
	firstLifecycle, err := sseSession.adapter.GetLifecycle(ctx, sseProfile)
	if err != nil {
		t.Fatalf("read initial live lifecycle: %v", err)
	}
	firstLifecycleDigest, err := CanonicalRcloneNativeLifecycleDigest(firstLifecycle)
	if err != nil {
		t.Fatalf("canonicalize initial live lifecycle: %v", err)
	}
	if _, err := sseSession.adapter.GetEncryption(ctx, sseProfile); err != nil {
		t.Fatalf("read initial live bucket encryption: %v", err)
	}

	t.Log("waiting for the mandatory 15-minute AWS capability stability window")
	timer := time.NewTimer(rcloneNativeAWSLiveSettleDuration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatalf("AWS capability stability window canceled: %v", ctx.Err())
	case <-timer.C:
	}
	secondObservedAt := time.Now().UTC()
	secondVersioning, err := sseSession.adapter.GetVersioning(ctx, sseProfile)
	if err != nil {
		t.Fatalf("read settled live versioning: %v", err)
	}
	if err := ValidateRcloneNativeVersioning(secondVersioning, firstVersioningDigest, firstObservedAt, secondObservedAt); err != nil {
		t.Fatalf("live versioning did not remain stable: %v", err)
	}
	secondLifecycle, err := sseSession.adapter.GetLifecycle(ctx, sseProfile)
	if err != nil {
		t.Fatalf("read settled live lifecycle: %v", err)
	}
	if err := ValidateRcloneNativeLifecycle(secondLifecycle, sseProfile.ManagedPrefix, firstLifecycleDigest, firstObservedAt, secondObservedAt); err != nil {
		t.Fatalf("live lifecycle did not remain safe and stable: %v", err)
	}
	bucketEncryption, err := sseSession.adapter.GetEncryption(ctx, sseProfile)
	if err != nil {
		t.Fatalf("read settled live bucket encryption: %v", err)
	}
	if _, err := ValidateRcloneNativeEncryption(sseSelection, bucketEncryption, nil, RcloneNativeKMSLimits{}); err != nil {
		t.Fatalf("live SSE-S3 profile rejected: %v", err)
	}

	t.Run("sse-s3", func(t *testing.T) {
		runRcloneNativeAWSLiveMutationSequence(t, ctx, factory, config, sseProfile, sseSession, sseSession)
	})

	t.Run("sse-kms-cmk-rotation", func(t *testing.T) {
		kmsProfile := rcloneNativeAWSLiveProfile(config, "sse-kms/")
		if err := ValidateRcloneNativeLifecycle(secondLifecycle, kmsProfile.ManagedPrefix, firstLifecycleDigest, firstObservedAt, secondObservedAt); err != nil {
			t.Fatalf("live KMS-prefix lifecycle did not remain safe and stable: %v", err)
		}
		retainedSelection := RcloneNativeEncryptionSelection{Profile: RcloneNativeSSEKMSV1, ActiveKeyARN: config.retainedKeyARN}
		retainedKeys := inspectRcloneNativeAWSLiveKeys(t, ctx, factory, config, kmsProfile, retainedSelection, bucketEncryption.BucketKeyEnabled)
		retainedSession := establishRcloneNativeAWSLiveSession(
			t, ctx, factory, config, kmsProfile, retainedSelection, bucketEncryption.BucketKeyEnabled, retainedKeys,
		)
		rotatedSelection := RcloneNativeEncryptionSelection{
			Profile: RcloneNativeSSEKMSV1, ActiveKeyARN: config.activeKeyARN,
			RetainedReadKeyARNs: []string{config.retainedKeyARN},
		}
		rotatedKeys := inspectRcloneNativeAWSLiveKeys(t, ctx, factory, config, kmsProfile, rotatedSelection, bucketEncryption.BucketKeyEnabled)
		rotatedSession := establishRcloneNativeAWSLiveSession(
			t, ctx, factory, config, kmsProfile, rotatedSelection, bucketEncryption.BucketKeyEnabled, rotatedKeys,
		)
		runRcloneNativeAWSLiveMutationSequence(t, ctx, factory, config, kmsProfile, retainedSession, rotatedSession)

		retainedDigest := retainedSession.evidence.ActiveKeyDigest
		_, err := rotatedSession.adapter.PutControlVersion(ctx, RcloneNativeControlWriteRequest{
			PhysicalKey: kmsProfile.ManagedPrefix + "control/retained-key-write-must-fail.json",
			Payload:     []byte("retained key is read-only\n"), MaxBytes: 1024,
			EncryptionProfile: RcloneNativeSSEKMSV1, KMSKeyARN: config.retainedKeyARN,
			KMSKeyDigest: retainedDigest, BucketKeyEnabled: bucketEncryption.BucketKeyEnabled,
		})
		if err == nil {
			t.Fatal("rotated session unexpectedly generated data under retained decrypt-only KMS key")
		}
	})
}

func loadRcloneNativeAWSLiveConfig(t *testing.T) rcloneNativeAWSLiveConfig {
	t.Helper()
	for _, name := range []string{"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_S3"} {
		if os.Getenv(name) != "" {
			t.Fatalf("official AWS live suite forbids endpoint override %s", name)
		}
	}
	required := map[string]string{
		"XIRANG_AWS_LIVE_REGION":        os.Getenv("XIRANG_AWS_LIVE_REGION"),
		"XIRANG_AWS_LIVE_BUCKET":        os.Getenv("XIRANG_AWS_LIVE_BUCKET"),
		"XIRANG_AWS_LIVE_ROLE_ARN":      os.Getenv("XIRANG_AWS_LIVE_ROLE_ARN"),
		"XIRANG_AWS_LIVE_EXTERNAL_ID":   os.Getenv("XIRANG_AWS_LIVE_EXTERNAL_ID"),
		"XIRANG_AWS_LIVE_KMS_KEY_ARN_A": os.Getenv("XIRANG_AWS_LIVE_KMS_KEY_ARN_A"),
		"XIRANG_AWS_LIVE_KMS_KEY_ARN_B": os.Getenv("XIRANG_AWS_LIVE_KMS_KEY_ARN_B"),
		"XIRANG_AWS_LIVE_ADMIN_PROFILE": os.Getenv("XIRANG_AWS_LIVE_ADMIN_PROFILE"),
		"XIRANG_AWS_LIVE_RCLONE_BINARY": os.Getenv("XIRANG_AWS_LIVE_RCLONE_BINARY"),
		"XIRANG_AWS_LIVE_PREFIX":        os.Getenv("XIRANG_AWS_LIVE_PREFIX"),
	}
	missing := make([]string, 0)
	for name, value := range required {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Skipf("official AWS live fixture is not configured; missing %s (skip is not a completion pass)", strings.Join(missing, ", "))
	}
	accountID, ok := parseRcloneNativeRoleARN(required["XIRANG_AWS_LIVE_ROLE_ARN"])
	if !ok || !validRcloneNativeExternalID(required["XIRANG_AWS_LIVE_EXTERNAL_ID"]) ||
		!validRcloneNativeRegion(required["XIRANG_AWS_LIVE_REGION"]) || !validRcloneNativeBucket(required["XIRANG_AWS_LIVE_BUCKET"]) {
		t.Fatal("official AWS live identity/profile configuration is invalid")
	}
	for _, arn := range []string{required["XIRANG_AWS_LIVE_KMS_KEY_ARN_A"], required["XIRANG_AWS_LIVE_KMS_KEY_ARN_B"]} {
		region, keyAccountID, valid := parseRcloneNativeKMSKeyARN(arn)
		if !valid || region != required["XIRANG_AWS_LIVE_REGION"] || keyAccountID != accountID {
			t.Fatal("official AWS live KMS key configuration is invalid")
		}
	}
	if required["XIRANG_AWS_LIVE_KMS_KEY_ARN_A"] == required["XIRANG_AWS_LIVE_KMS_KEY_ARN_B"] {
		t.Fatal("official AWS live active and retained KMS keys must be distinct")
	}
	if info, err := os.Stat(required["XIRANG_AWS_LIVE_RCLONE_BINARY"]); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatal("official AWS live Rclone binary is not executable")
	}
	versionCommand := exec.Command(required["XIRANG_AWS_LIVE_RCLONE_BINARY"], "version")
	versionCommand.Env = sanitizedRcloneNativeAWSLiveEnvironment(os.Environ())
	versionOutput, err := versionCommand.Output()
	if err != nil || len(bytes.SplitN(versionOutput, []byte{'\n'}, 2)) == 0 ||
		string(bytes.SplitN(versionOutput, []byte{'\n'}, 2)[0]) != "rclone v1.74.4" {
		t.Fatal("official AWS live suite requires exact Rclone v1.74.4 binary")
	}
	prefixRoot := strings.TrimSuffix(required["XIRANG_AWS_LIVE_PREFIX"], "/") + "/"
	randomBytes := make([]byte, 12)
	if _, err := io.ReadFull(cryptorand.Reader, randomBytes); err != nil {
		t.Fatalf("create randomized AWS live prefix: %v", err)
	}
	runPrefix := prefixRoot + "run-" + hex.EncodeToString(randomBytes) + "/"
	if !validRcloneNativePrefix(runPrefix) {
		t.Fatal("official AWS live prefix root is invalid")
	}
	return rcloneNativeAWSLiveConfig{
		region: required["XIRANG_AWS_LIVE_REGION"], bucket: required["XIRANG_AWS_LIVE_BUCKET"],
		roleARN: required["XIRANG_AWS_LIVE_ROLE_ARN"], externalID: required["XIRANG_AWS_LIVE_EXTERNAL_ID"],
		activeKeyARN: required["XIRANG_AWS_LIVE_KMS_KEY_ARN_B"], retainedKeyARN: required["XIRANG_AWS_LIVE_KMS_KEY_ARN_A"],
		adminProfile: required["XIRANG_AWS_LIVE_ADMIN_PROFILE"], rcloneBinary: required["XIRANG_AWS_LIVE_RCLONE_BINARY"],
		runPrefix: runPrefix, accountID: accountID,
	}
}

func rcloneNativeAWSLiveProfile(config rcloneNativeAWSLiveConfig, suffix string) RcloneNativeProfile {
	return RcloneNativeProfile{
		Code: RcloneNativeAWSS3GeneralPurposeV1, Region: config.region, Bucket: config.bucket,
		ManagedPrefix: config.runPrefix + suffix, EndpointMode: RcloneNativeEndpointAWSRegional,
		AddressingMode: RcloneNativeAddressingDNS, BucketKind: RcloneNativeBucketGeneralPurpose,
	}
}

func inspectRcloneNativeAWSLiveKeys(
	t *testing.T,
	ctx context.Context,
	factory *RcloneNativeAWSFactory,
	config rcloneNativeAWSLiveConfig,
	profile RcloneNativeProfile,
	selection RcloneNativeEncryptionSelection,
	bucketKeyEnabled bool,
) []RcloneNativeKMSKey {
	t.Helper()
	preliminary := establishRcloneNativeAWSLiveSession(t, ctx, factory, config, profile, selection, bucketKeyEnabled, nil)
	inspector, err := factory.KMS(preliminary.result.Session, profile.Region)
	if err != nil {
		t.Fatalf("create official AWS KMS inspector: %v", err)
	}
	arns := append([]string{selection.ActiveKeyARN}, selection.RetainedReadKeyARNs...)
	keys := make([]RcloneNativeKMSKey, 0, len(arns))
	for _, arn := range arns {
		key, err := inspector.DescribeKey(ctx, arn)
		if err != nil {
			t.Fatalf("inspect customer-managed AWS KMS key: %v", err)
		}
		keys = append(keys, key)
	}
	return keys
}

func establishRcloneNativeAWSLiveSession(
	t *testing.T,
	ctx context.Context,
	factory *RcloneNativeAWSFactory,
	config rcloneNativeAWSLiveConfig,
	profile RcloneNativeProfile,
	selection RcloneNativeEncryptionSelection,
	bucketKeyEnabled bool,
	keys []RcloneNativeKMSKey,
) rcloneNativeAWSLiveSession {
	t.Helper()
	now := time.Now().UTC()
	result, err := EstablishRcloneNativeSession(ctx, factory, factory, RcloneNativeSessionRequest{
		Profile: profile, RoleARN: config.roleARN, ExternalID: config.externalID,
		PointDeadlineAt: now.Add(40 * time.Minute), SessionMargin: 5 * time.Minute,
		Encryption: selection, BucketKeyEnabled: bucketKeyEnabled,
	}, now, cryptorand.Reader)
	if err != nil {
		t.Fatalf("establish official AWS STS-only session: %v", err)
	}
	if result.Session.AccountID() != config.accountID || !result.Session.ExpiresAt().After(now.Add(44*time.Minute)) {
		t.Fatal("official AWS STS session identity or expiry is outside the live contract")
	}
	bucketEncryption, err := func() (RcloneNativeBucketEncryption, error) {
		adapter, createErr := factory.S3(result.Session, profile, nil)
		if createErr != nil {
			return RcloneNativeBucketEncryption{}, createErr
		}
		return adapter.GetEncryption(ctx, profile)
	}()
	if err != nil {
		t.Fatalf("read live encryption admission evidence: %v", err)
	}
	evidence, err := ValidateRcloneNativeEncryption(selection, bucketEncryption, keys, RcloneNativeKMSLimits{MaxReadKeys: 8, MaxSerializedBytes: 16 << 10})
	if err != nil {
		if selection.Profile == RcloneNativeSSEKMSV1 && len(keys) == 0 {
			return rcloneNativeAWSLiveSession{result: result, encryption: selection}
		}
		t.Fatalf("validate live encryption admission evidence: %v", err)
	}
	bindings := make([]RcloneNativeKMSKeyDigestBinding, 0, len(keys))
	for _, key := range keys {
		bindings = append(bindings, RcloneNativeKMSKeyDigestBinding{KeyARN: key.ARN, Digest: rcloneNativeKMSKeyDigest(key)})
	}
	adapter, err := factory.S3(result.Session, profile, bindings)
	if err != nil {
		t.Fatalf("create official AWS S3 functional client: %v", err)
	}
	identity, err := adapter.BucketIdentity(ctx, profile)
	if err != nil || identity.AccountID != config.accountID || identity.Region != config.region || identity.Kind != RcloneNativeBucketGeneralPurpose {
		t.Fatalf("official AWS bucket identity mismatch: identity=%+v err=%v", identity, err)
	}
	return rcloneNativeAWSLiveSession{
		result: result, adapter: adapter, evidence: evidence, bindings: bindings, encryption: selection,
	}
}

func runRcloneNativeAWSLiveMutationSequence(
	t *testing.T,
	ctx context.Context,
	factory *RcloneNativeAWSFactory,
	config rcloneNativeAWSLiveConfig,
	profile RcloneNativeProfile,
	first, second rcloneNativeAWSLiveSession,
) {
	t.Helper()
	if first.adapter == nil || second.adapter == nil {
		t.Fatal("live mutation sequence requires complete functional sessions")
	}
	source := t.TempDir()
	writeRcloneNativeAWSLiveFile(t, filepath.Join(source, "stable.txt"), []byte("stable-version-one"))
	writeRcloneNativeAWSLiveFile(t, filepath.Join(source, "deleted.txt"), []byte("delete-me"))
	writeRcloneNativeAWSLiveFile(t, filepath.Join(source, "empty.bin"), nil)
	writeRcloneNativeAWSLiveFile(t, filepath.Join(source, "目录", "文件.txt"), []byte("unicode"))
	writeRcloneNativeAWSLiveFile(t, filepath.Join(source, ".", "dot-file"), []byte("dot"))
	multipartPath := filepath.Join(source, "multipart.bin")
	multipart, err := os.Create(multipartPath)
	if err != nil {
		t.Fatalf("create live multipart fixture: %v", err)
	}
	if err := multipart.Truncate(rcloneNativeAWSLiveMultipartBytes); err != nil {
		_ = multipart.Close()
		t.Fatalf("size live multipart fixture: %v", err)
	}
	if err := multipart.Close(); err != nil {
		t.Fatalf("close live multipart fixture: %v", err)
	}
	if err := runRcloneNativeAWSLiveSync(ctx, config.rcloneBinary, source, profile, first.result.RcloneConfig); err != nil {
		t.Fatalf("run initial pinned Rclone AWS sync: %v", err)
	}
	dataPrefix := profile.ManagedPrefix + "data/"
	firstGraph, err := CaptureRcloneNativeStableGraph(ctx, first.adapter, dataPrefix,
		RcloneNativeObservationLimits{PageSize: 1, MaxPages: 20000, MaxRecords: 10000})
	if err != nil || firstGraph.PageCount <= 1 {
		t.Fatalf("capture first paginated AWS version graph: pages=%d err=%v", firstGraph.PageCount, err)
	}

	writeRcloneNativeAWSLiveFile(t, filepath.Join(source, "stable.txt"), []byte("stable-version-two"))
	if err := os.Remove(filepath.Join(source, "deleted.txt")); err != nil {
		t.Fatalf("remove live delete-marker fixture: %v", err)
	}
	if err := runRcloneNativeAWSLiveSync(ctx, config.rcloneBinary, source, profile, second.result.RcloneConfig); err != nil {
		t.Fatalf("run overwrite/delete pinned Rclone AWS sync: %v", err)
	}
	secondGraph, err := CaptureRcloneNativeStableGraph(ctx, second.adapter, dataPrefix,
		RcloneNativeObservationLimits{PageSize: 1, MaxPages: 20000, MaxRecords: 10000})
	if err != nil || secondGraph.PageCount <= 1 || secondGraph.DeleteMarkerCount == 0 {
		t.Fatalf("capture second paginated AWS version graph: graph=%+v err=%v", secondGraph, err)
	}
	firstPage, err := second.adapter.ListVersionPage(ctx, RcloneNativeVersionPageRequest{Prefix: dataPrefix, MaxKeys: 1})
	if err != nil || !firstPage.Truncated || firstPage.NextKeyMarker == "" || firstPage.NextVersionIDMarker == "" {
		t.Fatalf("AWS dual-marker pagination evidence incomplete: page=%+v err=%v", firstPage, err)
	}

	stableKey := rcloneNativeAWSLivePhysicalKey(t, dataPrefix, "stable.txt")
	deletedKey := rcloneNativeAWSLivePhysicalKey(t, dataPrefix, "deleted.txt")
	multipartKey := rcloneNativeAWSLivePhysicalKey(t, dataPrefix, "multipart.bin")
	stableVersions := make([]RcloneNativeVersionRecord, 0, 2)
	var deletedMarker RcloneNativeVersionRecord
	var multipartVersion RcloneNativeVersionRecord
	for _, record := range secondGraph.Records {
		switch {
		case record.PhysicalKey == stableKey && record.Kind == RcloneNativeObjectVersion:
			stableVersions = append(stableVersions, record)
		case record.PhysicalKey == deletedKey && record.Kind == RcloneNativeDeleteMarker && record.IsLatest:
			deletedMarker = record
		case record.PhysicalKey == multipartKey && record.Kind == RcloneNativeObjectVersion && record.IsLatest:
			multipartVersion = record
		}
	}
	if len(stableVersions) < 2 || deletedMarker.VersionID == "" || multipartVersion.VersionID == "" {
		t.Fatalf("live graph omitted overwrite/delete/multipart evidence: stable=%d delete=%t multipart=%t", len(stableVersions), deletedMarker.VersionID != "", multipartVersion.VersionID != "")
	}
	var latest, previous RcloneNativeVersionRecord
	for _, record := range stableVersions {
		if record.IsLatest {
			latest = record
		} else if previous.VersionID == "" || record.LastModified.After(previous.LastModified) {
			previous = record
		}
	}
	assertRcloneNativeAWSLiveExactBytes(t, ctx, second.adapter, previous, []byte("stable-version-one"), first.evidence)
	assertRcloneNativeAWSLiveExactBytes(t, ctx, second.adapter, latest, []byte("stable-version-two"), second.evidence)
	rangeBody, err := second.adapter.OpenVersionRange(ctx, RcloneNativeExactRangeRequest{
		PhysicalKey: latest.PhysicalKey, VersionID: latest.VersionID, Offset: 7, Length: 7,
	})
	if err != nil {
		t.Fatalf("open exact AWS ranged version: %v", err)
	}
	rangeBytes, readErr := io.ReadAll(rangeBody)
	closeErr := rangeBody.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(rangeBytes, []byte("version")) {
		t.Fatalf("exact AWS range mismatch: read=%v close=%v bytes=%q", readErr, closeErr, rangeBytes)
	}
	if body, err := second.adapter.OpenVersion(ctx, RcloneNativeExactReadRequest{
		PhysicalKey: deletedMarker.PhysicalKey, VersionID: deletedMarker.VersionID,
	}); err == nil {
		if body != nil {
			_ = body.Close()
		}
		t.Fatal("AWS delete marker unexpectedly opened as an object version")
	}

	raw := s3.NewFromConfig(factory.assumedRoleConfig(second.result.Session, profile.Region), func(options *s3.Options) { options.UsePathStyle = false })
	multipartHead, err := raw.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(profile.Bucket), Key: aws.String(multipartVersion.PhysicalKey),
		VersionId: aws.String(multipartVersion.VersionID), ExpectedBucketOwner: aws.String(config.accountID),
	})
	if err != nil || multipartHead == nil || !strings.Contains(strings.Trim(aws.ToString(multipartHead.ETag), `"`), "-") {
		t.Fatalf("live multipart upload evidence missing: err=%v", err)
	}
	controlKey := profile.ManagedPrefix + "control/live-proof.json"
	control, err := second.adapter.PutControlVersion(ctx, RcloneNativeControlWriteRequest{
		PhysicalKey: controlKey, Payload: []byte("official AWS control proof\n"), MaxBytes: 1024,
		EncryptionProfile: second.encryption.Profile, KMSKeyARN: second.encryption.ActiveKeyARN,
		KMSKeyDigest: second.evidence.ActiveKeyDigest, BucketKeyEnabled: second.evidence.BucketKeyEnabled,
	})
	if err != nil {
		t.Fatalf("write official AWS exact control version: %v", err)
	}
	assertRcloneNativeAWSLiveExactBytes(t, ctx, second.adapter, RcloneNativeVersionRecord{
		PhysicalKey: controlKey, VersionID: control.VersionID, Kind: RcloneNativeObjectVersion,
		IsLatest: true, Size: uint64(len("official AWS control proof\n")), LastModified: time.Now().UTC(),
	}, []byte("official AWS control proof\n"), second.evidence)
}

func runRcloneNativeAWSLiveSync(ctx context.Context, binary, source string, profile RcloneNativeProfile, config []byte) error {
	destination := rcloneNativeConfigRemote + ":" + profile.Bucket + "/" + strings.TrimSuffix(profile.ManagedPrefix, "/") + "/data"
	command := exec.CommandContext(ctx, binary,
		"--config", "/dev/stdin", "--retries", "1", "--low-level-retries", "2", "--log-level", "NOTICE",
		"sync", source, destination,
	)
	command.Stdin = bytes.NewReader(append([]byte(nil), config...))
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.Env = sanitizedRcloneNativeAWSLiveEnvironment(os.Environ())
	if err := command.Run(); err != nil {
		return fmt.Errorf("pinned Rclone live sync failed: %w", err)
	}
	return nil
}

func sanitizedRcloneNativeAWSLiveEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name := entry
		if separator := strings.IndexByte(entry, '='); separator >= 0 {
			name = entry[:separator]
		}
		if strings.HasPrefix(name, "AWS_") || strings.HasPrefix(name, "RCLONE_") {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func writeRcloneNativeAWSLiveFile(t *testing.T, name string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatalf("create live source directory: %v", err)
	}
	if err := os.WriteFile(name, payload, 0o600); err != nil {
		t.Fatalf("write live source fixture: %v", err)
	}
}

func rcloneNativeAWSLivePhysicalKey(t *testing.T, prefix, logicalPath string) string {
	t.Helper()
	encoded, err := EncodeRcloneV1744S3Path(logicalPath)
	if err != nil {
		t.Fatalf("encode pinned Rclone live path: %v", err)
	}
	return prefix + encoded
}

func assertRcloneNativeAWSLiveExactBytes(
	t *testing.T,
	ctx context.Context,
	adapter S3Native,
	record RcloneNativeVersionRecord,
	want []byte,
	evidence RcloneNativeEncryptionEvidence,
) {
	t.Helper()
	request := RcloneNativeExactReadRequest{PhysicalKey: record.PhysicalKey, VersionID: record.VersionID}
	head, err := adapter.HeadVersion(ctx, request)
	if err != nil || head.VersionID != record.VersionID || head.Size != uint64(len(want)) || head.EncryptionProfile != evidence.Profile ||
		head.BucketKeyEnabled != evidence.BucketKeyEnabled || head.KMSKeyDigest != evidence.ActiveKeyDigest {
		t.Fatalf("exact AWS head evidence mismatch: head=%+v err=%v", head, err)
	}
	body, err := adapter.OpenVersion(ctx, request)
	if err != nil {
		t.Fatalf("open exact AWS object version: %v", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(body, int64(len(want))+1))
	closeErr := body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(payload, want) {
		t.Fatalf("exact AWS object bytes mismatch: read=%v close=%v size=%d", readErr, closeErr, len(payload))
	}
}

func cleanupRcloneNativeAWSLivePrefix(ctx context.Context, client *s3.Client, config rcloneNativeAWSLiveConfig) error {
	if ctx == nil || client == nil || !strings.HasPrefix(config.runPrefix, strings.TrimSuffix(os.Getenv("XIRANG_AWS_LIVE_PREFIX"), "/")+"/run-") {
		return fmt.Errorf("invalid bounded AWS live cleanup request")
	}
	uploads := s3.NewListMultipartUploadsPaginator(client, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(config.bucket), Prefix: aws.String(config.runPrefix), ExpectedBucketOwner: aws.String(config.accountID),
	})
	for uploads.HasMorePages() {
		page, err := uploads.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list bounded AWS live multipart uploads: %w", err)
		}
		for _, upload := range page.Uploads {
			if _, err := client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket: aws.String(config.bucket), Key: upload.Key, UploadId: upload.UploadId,
				ExpectedBucketOwner: aws.String(config.accountID),
			}); err != nil {
				return fmt.Errorf("abort bounded AWS live multipart upload: %w", err)
			}
		}
	}
	paginator := s3.NewListObjectVersionsPaginator(client, &s3.ListObjectVersionsInput{
		Bucket: aws.String(config.bucket), Prefix: aws.String(config.runPrefix), ExpectedBucketOwner: aws.String(config.accountID),
	})
	objects := make([]s3types.ObjectIdentifier, 0, 1000)
	flush := func() error {
		if len(objects) == 0 {
			return nil
		}
		output, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(config.bucket), ExpectedBucketOwner: aws.String(config.accountID),
			Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		})
		objects = objects[:0]
		if err != nil {
			return fmt.Errorf("delete bounded AWS live object versions: %w", err)
		}
		if output == nil || len(output.Errors) != 0 {
			return fmt.Errorf("delete bounded AWS live object versions returned item failures")
		}
		return nil
	}
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list bounded AWS live object versions: %w", err)
		}
		for _, version := range page.Versions {
			objects = append(objects, s3types.ObjectIdentifier{Key: version.Key, VersionId: version.VersionId})
			if len(objects) == cap(objects) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		for _, marker := range page.DeleteMarkers {
			objects = append(objects, s3types.ObjectIdentifier{Key: marker.Key, VersionId: marker.VersionId})
			if len(objects) == cap(objects) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	return flush()
}
