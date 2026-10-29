package api

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
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
)

const (
	rcloneNativeHTTPBucket = "xirang-http-private-bucket"
	rcloneNativeHTTPPrefix = "managed/http/"
	rcloneNativeHTTPRole   = "arn:aws:iam::123456789012:role/xirang-http-test"
	rcloneNativeHTTPKMS    = "arn:aws:kms:us-east-1:123456789012:key/http-test-key"
)

type rcloneHTTPNativeObject struct {
	record provider.RcloneNativeVersionRecord
	body   []byte
}

// Only the external AWS and remote-command boundaries are controlled. The
// runtime, repository, preflighter, publication strategy and router are real.
type rcloneHTTPNativeBoundary struct {
	mu              sync.Mutex
	clock           *rcloneHTTPClock
	profile         provider.RcloneNativeProfile
	encryption      provider.RcloneNativeEncryptionProfileCode
	activeDigest    string
	stagedID        string
	staged          []byte
	source          []byte
	objects         []rcloneHTTPNativeObject
	sequence        int
	canaryWrites    int
	deletes         int
	factoryCalls    int
	commandCalls    int
	stageCalls      int
	factoryFailure  error
	managedVersion  string
	delegate        *provider.SSHCommandTransport
	stagingDelegate provider.StagedPayloadTransport
}

func newRcloneHTTPNativeBoundary(encryption provider.RcloneNativeEncryptionProfileCode) *rcloneHTTPNativeBoundary {
	return &rcloneHTTPNativeBoundary{encryption: encryption, source: []byte("native fixture source\n")}
}

func (boundary *rcloneHTTPNativeBoundary) dependencies(t *testing.T, binary string, hybrid bool) rcloneHTTPDependencies {
	return func(db *gorm.DB, clock *rcloneHTTPClock) (provider.CommandTransport, provider.CommandStreamTransport, provider.StagedPayloadTransport, repository.RcloneNativePreflightFactoryBuilder, repository.RcloneNativeFactoryBuilder) {
		boundary.clock = clock
		if hybrid {
			dialer := sshutil.NewNodeDialer(db)
			transport, err := provider.NewSSHCommandTransport(dialer, 4, provider.ToolBinaries{Rclone: binary})
			if err != nil {
				t.Fatalf("construct real SSH transport: %v", err)
			}
			ownership, err := backupasset.NewKeyring(db, clock.Now).Ensure(context.Background(), backupasset.KeyDomainRecoveryCleanupOwnership)
			if err != nil {
				t.Fatalf("initialize SSH staging ownership: %v", err)
			}
			staging, err := provider.NewRcloneStagedPayloadTransport(dialer, ownership.Key, clock.Now)
			if err != nil {
				t.Fatalf("construct real SSH staging: %v", err)
			}
			boundary.delegate, boundary.stagingDelegate = transport, staging
		}
		preflight := func(context.Context, provider.RcloneNativeBootstrap, string, int) (repository.RcloneNativePreflightFactory, error) {
			boundary.mu.Lock()
			defer boundary.mu.Unlock()
			boundary.factoryCalls++
			if boundary.factoryFailure != nil {
				return nil, boundary.factoryFailure
			}
			return boundary, nil
		}
		publication := func(context.Context, provider.RcloneNativeBootstrap, string, int) (repository.RcloneNativeFactory, error) {
			return boundary, nil
		}
		return boundary, boundary, boundary, preflight, publication
	}
}

func (boundary *rcloneHTTPNativeBoundary) nativeNode(access *provider.RemoteCommandAccess) bool {
	return boundary.delegate == nil || (access != nil && strings.HasPrefix(access.Node.Name, "native-"))
}

func (boundary *rcloneHTTPNativeBoundary) Run(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits) (provider.CommandOutput, error) {
	if !boundary.nativeNode(invocation.Runtime) || (boundary.delegate != nil && invocation.Operation == provider.OperationRcloneVersion) ||
		(boundary.delegate != nil && invocation.Operation == provider.OperationRcloneFeatures) || (boundary.delegate != nil && invocation.Operation == provider.OperationRcloneList) ||
		(boundary.delegate != nil && invocation.Operation == provider.OperationRcloneStat) || (boundary.delegate != nil && invocation.Operation == provider.OperationRcloneCat) {
		return boundary.delegate.Run(ctx, invocation, limits)
	}
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	boundary.commandCalls++
	switch invocation.Operation {
	case provider.OperationRcloneVersion:
		return provider.CommandOutput{Stdout: []byte("rclone v1.74.4\n")}, nil
	case provider.OperationRcloneManagedVersion:
		version := boundary.managedVersion
		if version == "" {
			version = "rclone v1.74.4\n"
		}
		return provider.CommandOutput{Stdout: []byte(version)}, nil
	case provider.OperationRcloneFeatures:
		return provider.CommandOutput{Stdout: []byte(`{"Name":"local","Features":{}}`)}, nil
	case provider.OperationRcloneList:
		return provider.CommandOutput{Stdout: []byte(`[]`)}, nil
	case provider.OperationRcloneManagedCopyTo:
		if boundary.stagedID == "" || len(boundary.staged) != 64 {
			return provider.CommandOutput{}, errors.New("missing staged canary")
		}
		boundary.putLocked(rcloneNativeHTTPPrefix+"control/preflight/"+boundary.stagedID+"/canary.bin", boundary.staged, boundary.encryption, boundary.activeDigest)
		boundary.canaryWrites++
		return provider.CommandOutput{}, nil
	case provider.OperationRcloneManagedNativeSync:
		boundary.putLocked(rcloneNativeHTTPPrefix+"data/source.txt", boundary.source, boundary.encryption, boundary.activeDigest)
		return provider.CommandOutput{}, nil
	case provider.OperationRcloneManagedCheckDownload:
		return provider.CommandOutput{}, nil
	default:
		return provider.CommandOutput{}, errors.New("unexpected controlled Rclone operation")
	}
}

func (boundary *rcloneHTTPNativeBoundary) Open(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits, max int64) (provider.ReadHandle, error) {
	if !boundary.nativeNode(invocation.Runtime) {
		return boundary.delegate.Open(ctx, invocation, limits, max)
	}
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	switch invocation.Operation {
	case provider.OperationRcloneManagedCat:
		return io.NopCloser(bytes.NewReader(append([]byte(nil), boundary.staged...))), nil
	case provider.OperationRcloneManagedRecursiveList:
		digest := sha256.Sum256(boundary.source)
		payload, err := json.Marshal([]map[string]any{{"Path": "source.txt", "Name": "source.txt", "Size": len(boundary.source), "ModTime": "2026-07-16T01:00:00Z", "IsDir": false, "Hashes": map[string]string{"sha256": hex.EncodeToString(digest[:])}, "Metadata": map[string]string{"mode": "100600"}}})
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(payload)), nil
	default:
		return nil, errors.New("unexpected controlled Rclone stream")
	}
}

func (boundary *rcloneHTTPNativeBoundary) OpenExecution(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits, max int64) (provider.CommandExecution, error) {
	if boundary.delegate != nil {
		return boundary.delegate.OpenExecution(ctx, invocation, limits, max)
	}
	return nil, errors.New("unexpected controlled Rclone execution")
}

func (boundary *rcloneHTTPNativeBoundary) Stage(ctx context.Context, access provider.RemoteCommandAccess, request provider.StagedPayloadRequest) (provider.StagedPayloadRef, error) {
	if !boundary.nativeNode(&access) {
		return boundary.stagingDelegate.Stage(ctx, access, request)
	}
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	boundary.stageCalls++
	boundary.stagedID = request.AttemptID
	boundary.staged = append([]byte(nil), request.Payload...)
	return provider.StagedPayloadRef{}, nil
}

func (boundary *rcloneHTTPNativeBoundary) Cleanup(ctx context.Context, access provider.RemoteCommandAccess, ref provider.StagedPayloadRef) error {
	if !boundary.nativeNode(&access) {
		return boundary.stagingDelegate.Cleanup(ctx, access, ref)
	}
	return nil
}

func (boundary *rcloneHTTPNativeBoundary) CleanupAged(ctx context.Context, access provider.RemoteCommandAccess, age time.Duration, limit int) error {
	if !boundary.nativeNode(&access) {
		return boundary.stagingDelegate.CleanupAged(ctx, access, age, limit)
	}
	return nil
}

func (*rcloneHTTPNativeBoundary) BootstrapCredentialsExpire(context.Context) (bool, error) {
	return false, nil
}

func (boundary *rcloneHTTPNativeBoundary) AssumeRole(_ context.Context, request provider.RcloneNativeAssumeRoleRequest) (provider.RcloneNativeAssumeRoleResult, error) {
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	if request.ExternalID == nil {
		return provider.RcloneNativeAssumeRoleResult{}, provider.ErrRcloneNativeAssumeRoleDenied
	}
	if !strings.HasPrefix(*request.ExternalID, "xirang-") || strings.HasPrefix(*request.ExternalID, "xirang-wrong-") {
		return provider.RcloneNativeAssumeRoleResult{}, provider.ErrRcloneNativeAssumeRoleDenied
	}
	session, err := provider.NewRcloneNativeSession("FAKE_HTTP_ACCESS_KEY", "FAKE_HTTP_SECRET_KEY", "FAKE_HTTP_SESSION_TOKEN", "123456789012", strings.Repeat("1", 64), boundary.clock.Now().Add(request.Duration))
	return provider.RcloneNativeAssumeRoleResult{Session: session}, err
}

func (*rcloneHTTPNativeBoundary) Probe(context.Context, provider.RcloneNativeDenyProbeRequest) (provider.RcloneNativeDenyProbeResult, error) {
	return provider.RcloneNativeDenyProbeResult{Denied: true}, nil
}

func (boundary *rcloneHTTPNativeBoundary) S3(_ provider.RcloneNativeSession, profile provider.RcloneNativeProfile, bindings []provider.RcloneNativeKMSKeyDigestBinding) (provider.S3Native, error) {
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	boundary.profile = profile
	if len(bindings) > 0 {
		boundary.activeDigest = bindings[0].Digest
	}
	return boundary, nil
}

func (boundary *rcloneHTTPNativeBoundary) KMS(provider.RcloneNativeSession, string) (provider.KMSKeyInspector, error) {
	return boundary, nil
}

func (*rcloneHTTPNativeBoundary) BaselineS3(provider.RcloneNativeSession, provider.RcloneNativeProfile, []string) (provider.RcloneNativeBaselineS3, error) {
	return nil, errors.New("baseline import is outside this fixture")
}

func (*rcloneHTTPNativeBoundary) DescribeKey(_ context.Context, arn string) (provider.RcloneNativeKMSKey, error) {
	return provider.RcloneNativeKMSKey{ARN: arn, AccountID: "123456789012", Region: "us-east-1", Manager: "CUSTOMER", Spec: "SYMMETRIC_DEFAULT", Usage: "ENCRYPT_DECRYPT", State: "Enabled", Origin: "AWS_KMS"}, nil
}

func (*rcloneHTTPNativeBoundary) BucketIdentity(_ context.Context, profile provider.RcloneNativeProfile) (provider.RcloneNativeBucketIdentity, error) {
	return provider.RcloneNativeBucketIdentity{AccountID: "123456789012", Region: profile.Region, Kind: provider.RcloneNativeBucketGeneralPurpose}, nil
}
func (*rcloneHTTPNativeBoundary) GetVersioning(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeVersioningObservation, error) {
	return provider.RcloneNativeVersioningObservation{Status: "Enabled", MFADelete: "Disabled"}, nil
}
func (*rcloneHTTPNativeBoundary) GetLifecycle(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeLifecycleObservation, error) {
	return provider.RcloneNativeLifecycleObservation{Rules: []provider.RcloneNativeLifecycleRule{}}, nil
}
func (*rcloneHTTPNativeBoundary) GetEncryption(context.Context, provider.RcloneNativeProfile) (provider.RcloneNativeBucketEncryption, error) {
	return provider.RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true}, nil
}

func (boundary *rcloneHTTPNativeBoundary) putLocked(key string, body []byte, profile provider.RcloneNativeEncryptionProfileCode, keyDigest string) string {
	for i := range boundary.objects {
		if boundary.objects[i].record.PhysicalKey == key {
			boundary.objects[i].record.IsLatest = false
		}
	}
	boundary.sequence++
	version := fmt.Sprintf("fixture-version-%d", boundary.sequence)
	digest := sha256.Sum256(body)
	boundary.objects = append(boundary.objects, rcloneHTTPNativeObject{record: provider.RcloneNativeVersionRecord{
		PhysicalKey: key, VersionID: version, Kind: provider.RcloneNativeObjectVersion, IsLatest: true,
		Size: uint64(len(body)), LastModified: boundary.clock.Now(), ContentDigest: hex.EncodeToString(digest[:]),
		EncryptionProfile: profile, KMSKeyDigest: keyDigest,
	}, body: append([]byte(nil), body...)})
	return version
}

func (boundary *rcloneHTTPNativeBoundary) ListVersionPage(_ context.Context, request provider.RcloneNativeVersionPageRequest) (provider.RcloneNativeVersionPage, error) {
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	var records []provider.RcloneNativeVersionRecord
	for _, object := range boundary.objects {
		if strings.HasPrefix(object.record.PhysicalKey, request.Prefix) {
			records = append(records, object.record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].PhysicalKey != records[j].PhysicalKey {
			return records[i].PhysicalKey < records[j].PhysicalKey
		}
		return records[i].VersionID < records[j].VersionID
	})
	if len(records) > request.MaxKeys {
		return provider.RcloneNativeVersionPage{}, errors.New("controlled graph exceeds one page")
	}
	return provider.RcloneNativeVersionPage{Records: records}, nil
}

func (boundary *rcloneHTTPNativeBoundary) findLocked(request provider.RcloneNativeExactReadRequest) (rcloneHTTPNativeObject, error) {
	for _, object := range boundary.objects {
		if object.record.PhysicalKey == request.PhysicalKey && object.record.VersionID == request.VersionID && object.record.Kind == provider.RcloneNativeObjectVersion {
			return object, nil
		}
	}
	return rcloneHTTPNativeObject{}, io.EOF
}

func (boundary *rcloneHTTPNativeBoundary) HeadVersion(_ context.Context, request provider.RcloneNativeExactReadRequest) (provider.RcloneNativeExactObjectHead, error) {
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	object, err := boundary.findLocked(request)
	if err != nil {
		return provider.RcloneNativeExactObjectHead{}, err
	}
	return provider.RcloneNativeExactObjectHead{PhysicalKey: object.record.PhysicalKey, VersionID: object.record.VersionID, Size: object.record.Size, EncryptionProfile: object.record.EncryptionProfile, KMSKeyDigest: object.record.KMSKeyDigest, BucketKeyEnabled: object.record.BucketKeyEnabled}, nil
}
func (boundary *rcloneHTTPNativeBoundary) OpenVersion(_ context.Context, request provider.RcloneNativeExactReadRequest) (io.ReadCloser, error) {
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	object, err := boundary.findLocked(request)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), object.body...))), nil
}
func (boundary *rcloneHTTPNativeBoundary) OpenVersionRange(_ context.Context, request provider.RcloneNativeExactRangeRequest) (io.ReadCloser, error) {
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	object, err := boundary.findLocked(provider.RcloneNativeExactReadRequest{PhysicalKey: request.PhysicalKey, VersionID: request.VersionID})
	if err != nil || request.Offset > uint64(len(object.body)) || request.Length > uint64(len(object.body))-request.Offset {
		return nil, io.EOF
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), object.body[request.Offset:request.Offset+request.Length]...))), nil
}
func (boundary *rcloneHTTPNativeBoundary) PutControlVersion(_ context.Context, request provider.RcloneNativeControlWriteRequest) (provider.RcloneNativeControlWriteResult, error) {
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	return provider.RcloneNativeControlWriteResult{VersionID: boundary.putLocked(request.PhysicalKey, request.Payload, request.EncryptionProfile, request.KMSKeyDigest)}, nil
}
func (boundary *rcloneHTTPNativeBoundary) DeleteCurrentCanary(_ context.Context, request provider.RcloneNativeCurrentDeleteRequest) (provider.RcloneNativeCurrentDeleteResult, error) {
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	for i := range boundary.objects {
		if boundary.objects[i].record.PhysicalKey == request.PhysicalKey {
			boundary.objects[i].record.IsLatest = false
		}
	}
	boundary.sequence++
	version := fmt.Sprintf("fixture-delete-%d", boundary.sequence)
	boundary.objects = append(boundary.objects, rcloneHTTPNativeObject{record: provider.RcloneNativeVersionRecord{PhysicalKey: request.PhysicalKey, VersionID: version, Kind: provider.RcloneNativeDeleteMarker, IsLatest: true, LastModified: boundary.clock.Now()}})
	boundary.deletes++
	return provider.RcloneNativeCurrentDeleteResult{VersionID: version}, nil
}

func (boundary *rcloneHTTPNativeBoundary) counts() (int, int) {
	boundary.mu.Lock()
	defer boundary.mu.Unlock()
	return boundary.canaryWrites, boundary.deletes
}

func (fixture *rcloneHTTPFixture) nativeCandidate(id uint, kms bool) string {
	fixture.t.Helper()
	base := fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/", id)
	revision := fixture.task(id).RclonePublication.TaskRevision
	var setup backupasset.RcloneBindingSetupResult
	fixture.request("POST", base+"native-binding-setups", map[string]any{"expected_task_revision": revision}, 200, &setup)
	if err := setup.Validate(true); err != nil || setup.BaseBindingRevision != "0" {
		fixture.t.Fatalf("invalid native setup base revision %q: %v", setup.BaseBindingRevision, err)
	}
	fixture.private = append(fixture.private, setup.ExternalID, rcloneNativeHTTPBucket, rcloneNativeHTTPPrefix, rcloneNativeHTTPRole, rcloneNativeHTTPKMS, "FAKE_HTTP_SESSION_TOKEN")
	body := map[string]any{"expected_task_revision": revision, "expected_binding_revision": setup.BaseBindingRevision, "setup_id": setup.SetupID,
		"region": "us-east-1", "bucket": rcloneNativeHTTPBucket, "managed_prefix": rcloneNativeHTTPPrefix,
		"role_arn": rcloneNativeHTTPRole, "bootstrap": map[string]any{"mode": "workload_chain"}, "encryption_profile": "sse_s3"}
	if kms {
		body["encryption_profile"], body["kms_key_arn"] = "sse_kms_cmk", rcloneNativeHTTPKMS
	}
	var summary backupasset.RclonePublicationSummary
	fixture.request("PUT", base+"native-binding", body, 200, &summary)
	if summary.BindingRevision != "1" {
		fixture.t.Fatalf("native candidate revision=%q, want 1", summary.BindingRevision)
	}
	return revision
}

var _ provider.RcloneNativeCanaryS3 = (*rcloneHTTPNativeBoundary)(nil)
var _ repository.RcloneNativeFactory = (*rcloneHTTPNativeBoundary)(nil)

func TestRclonePreflightRuntimeHTTPNative(t *testing.T) {
	for _, test := range []struct {
		name       string
		encryption provider.RcloneNativeEncryptionProfileCode
		kms        bool
	}{
		{name: "sse-s3", encryption: provider.RcloneNativeSSES3V1},
		{name: "sse-kms-retained-key", encryption: provider.RcloneNativeSSEKMSV1, kms: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			boundary := newRcloneHTTPNativeBoundary(test.encryption)
			fixture := newRcloneHTTPFixture(t, "rclone", boundary.dependencies(t, "rclone", false))
			node := model.Node{Host: "127.0.0.1", Port: 22, Username: "fixture", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
			id := fixture.addTask(node, "native-"+test.name, "/fixture/source", "legacy:fixture")
			revision := fixture.nativeCandidate(id, test.kms)
			first := fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
			if first.Purpose != backupasset.RclonePreflightPurposeActivation || first.Summary.State != backupasset.RcloneStateCapabilitySettling {
				t.Fatal("initial native preflight did not settle")
			}
			if summary := fixture.task(id).RclonePublication; summary.State != backupasset.RcloneStateCapabilitySettling || summary.ConsistencyClass != backupasset.RcloneConsistencyNotEvaluated {
				t.Fatal("reopened task did not project the settling candidate")
			}
			fixture.activate(id, revision, first.PreflightID, 409)
			fixture.clock.Advance(14 * time.Minute)
			second := fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
			if second.Summary.State != backupasset.RcloneStateCapabilitySettling {
				t.Fatal("native candidate became ready before 15 minutes")
			}
			if writes, deletes := boundary.counts(); writes != 0 || deletes != 0 {
				t.Fatal("native preflight wrote a canary during settling")
			}

			fixture.restart()
			fixture.activate(id, revision, second.PreflightID, 409)
			revision = fixture.nativeCandidate(id, test.kms)
			restarted := fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
			if restarted.Summary.State != backupasset.RcloneStateCapabilitySettling {
				t.Fatal("restart reused an earlier observation window")
			}
			fixture.clock.Advance(14 * time.Minute)
			restarted = fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
			if restarted.Summary.State != backupasset.RcloneStateCapabilitySettling {
				t.Fatal("restarted candidate became ready too early")
			}
			fixture.clock.Advance(time.Minute)
			ready := fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
			if ready.Purpose != backupasset.RclonePreflightPurposeActivation || ready.Summary.State != backupasset.RcloneStateReady {
				t.Fatal("stable native preflight did not produce a ready activation")
			}
			if writes, deletes := boundary.counts(); writes != 1 || deletes != 1 {
				t.Fatal("stable native preflight did not write and delete exactly one canary")
			}
			activated := fixture.activate(id, revision, ready.PreflightID, 200)
			if activated.Summary.TaskRevision == revision || fixture.task(id).Enabled {
				t.Fatal("native activation did not retain a paused task with a new revision")
			}
			var link model.TaskRepositoryLink
			if err := fixture.db.Where("task_id = ? AND unlinked_at IS NULL", id).First(&link).Error; err != nil {
				t.Fatal(err)
			}
			document := rcloneHTTPBindingDocument(t, fixture.db, link.RepositoryID)
			native, ok := document["native"].(map[string]any)
			canonical, err := provider.CanonicalRcloneNativeBucketEncryptionDigest(provider.RcloneNativeBucketEncryption{Algorithm: "AES256", BlockedEncryptionTypesKnown: true})
			if err != nil || !ok || native["bucket_encryption_digest"] != canonical {
				t.Fatal("activated native binding did not retain the canonical encryption observation")
			}
			outcome := fixture.publishRclone(id)
			health, ok := fixture.runtime.PublicationReconciler().(interface {
				CheckRcloneNativeHealth(context.Context, string) (provider.RcloneNativeHealthResult, error)
			})
			if !ok {
				t.Fatal("runtime did not expose native health checks")
			}
			result, err := health.CheckRcloneNativeHealth(context.Background(), outcome.RepositoryID)
			if err != nil || result.Reason != backupasset.RcloneReasonReady || result.VerifiedReferenceCount == 0 {
				t.Fatal("native health rejected the real preflight and publication evidence")
			}

			// This represents an existing historical read-key ring; the current
			// HTTP API intentionally does not expose key rotation or ring editing.
			if test.kms {
				rcloneHTTPSeedRetainedKey(t, fixture, link.RepositoryID)
			}
			before := rcloneHTTPBindingDocument(t, fixture.db, link.RepositoryID)
			fixture.clock.Advance(31 * time.Minute)
			expired := fixture.task(id).RclonePublication
			if expired.State != backupasset.RcloneStatePreflightRequired || expired.ReasonCode != backupasset.RcloneReasonPreflightExpired {
				t.Fatal("native authorization did not expire")
			}
			fixture.assertExpiredPublicationDenied(id)
			fixture.assertRenewalRejectsActiveRun(id, expired.TaskRevision)
			settling := fixture.preflight(id, expired.TaskRevision, backupasset.PublicationNativeObjectVersions)
			if settling.Purpose != backupasset.RclonePreflightPurposeRenewal || settling.Summary.State != backupasset.RcloneStateCapabilitySettling {
				t.Fatal("native renewal did not start a new observation window")
			}
			fixture.clock.Advance(15 * time.Minute)
			renewal := fixture.preflight(id, expired.TaskRevision, backupasset.PublicationNativeObjectVersions)
			if renewal.Purpose != backupasset.RclonePreflightPurposeRenewal || renewal.Summary.State != backupasset.RcloneStateReady {
				t.Fatal("native renewal did not complete the observation window")
			}
			if test.kms && renewal.Summary.KMSReadKeyCount != 1 {
				t.Fatal("native renewal omitted the retained read key")
			}
			fixture.request("POST", fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/activate", id), map[string]any{
				"expected_task_revision": expired.TaskRevision, "preflight_id": renewal.PreflightID, "migration_choice": "imported_baseline",
			}, 409, nil)
			fixture.activate(id, expired.TaskRevision, renewal.PreflightID, 409)
			renewal = fixture.preflight(id, expired.TaskRevision, backupasset.PublicationNativeObjectVersions)
			if renewal.Purpose != backupasset.RclonePreflightPurposeRenewal || renewal.Summary.State != backupasset.RcloneStateReady {
				t.Fatal("native renewal did not recover after consuming the rejected ticket")
			}
			renewed := fixture.activate(id, expired.TaskRevision, renewal.PreflightID, 200)
			if renewed.Summary.TaskRevision == expired.TaskRevision || fixture.task(id).Enabled {
				t.Fatal("native renewal did not advance the paused task revision")
			}
			after := rcloneHTTPBindingDocument(t, fixture.db, link.RepositoryID)
			oldExpiry, oldErr := time.Parse(time.RFC3339Nano, before["preflight_expires_at"].(string))
			newExpiry, newErr := time.Parse(time.RFC3339Nano, after["preflight_expires_at"].(string))
			delete(before, "preflight_expires_at")
			delete(after, "preflight_expires_at")
			if oldErr != nil || newErr != nil || !newExpiry.After(oldExpiry) || !reflect.DeepEqual(before, after) {
				t.Fatal("native renewal changed persisted identity or did not extend expiry")
			}
			fixture.activate(id, expired.TaskRevision, renewal.PreflightID, 409)
		})
	}
}

func TestRclonePreflightRuntimeHTTPNativeReplacementUsesSetupBaseAndRestartsWindow(t *testing.T) {
	boundary := newRcloneHTTPNativeBoundary(provider.RcloneNativeSSES3V1)
	fixture := newRcloneHTTPFixture(t, "rclone", boundary.dependencies(t, "rclone", false))
	node := model.Node{Host: "127.0.0.1", Port: 22, Username: "fixture", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
	id := fixture.addTask(node, "native-replacement", "/fixture/source", "legacy:fixture")
	revision := fixture.nativeCandidate(id, false)
	initial := fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
	if initial.Summary.State != backupasset.RcloneStateCapabilitySettling {
		t.Fatal("initial native candidate did not begin a settling window")
	}
	fixture.clock.Advance(15 * time.Minute)
	ready := fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
	if ready.Summary.State != backupasset.RcloneStateReady || ready.Summary.BindingRevision != "1" {
		t.Fatal("first native candidate did not become ready with proposed revision 1")
	}

	base := fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/", id)
	var setup backupasset.RcloneBindingSetupResult
	fixture.request("POST", base+"native-binding-setups", map[string]any{"expected_task_revision": revision}, 200, &setup)
	if err := setup.Validate(true); err != nil || setup.BaseBindingRevision != "0" {
		t.Fatalf("replacement setup did not return the persisted base: %q (%v)", setup.BaseBindingRevision, err)
	}
	fixture.private = append(fixture.private, setup.ExternalID)
	body := map[string]any{
		"expected_task_revision": revision, "expected_binding_revision": ready.Summary.BindingRevision, "setup_id": setup.SetupID,
		"region": "us-east-1", "bucket": rcloneNativeHTTPBucket, "managed_prefix": rcloneNativeHTTPPrefix,
		"role_arn": rcloneNativeHTTPRole, "bootstrap": map[string]any{"mode": "workload_chain"}, "encryption_profile": "sse_s3",
	}
	fixture.request("PUT", base+"native-binding", body, 409, nil)
	body["expected_binding_revision"] = setup.BaseBindingRevision
	fixture.request("PUT", base+"native-binding", body, 409, nil)
	if summary := fixture.task(id).RclonePublication; summary.State != backupasset.RcloneStateReady || summary.BindingRevision != "1" {
		t.Fatal("rejected or replayed setup changed the ready candidate")
	}

	fixture.nativeCandidate(id, false)
	fixture.activate(id, revision, ready.PreflightID, 409)
	replacement := fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
	if replacement.Summary.State != backupasset.RcloneStateCapabilitySettling {
		t.Fatal("replacement reused the old native ready window")
	}
	fixture.clock.Advance(14 * time.Minute)
	replacement = fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
	if replacement.Summary.State != backupasset.RcloneStateCapabilitySettling {
		t.Fatal("replacement became ready before its own 15-minute window")
	}
	fixture.clock.Advance(time.Minute)
	replacement = fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
	if replacement.Summary.State != backupasset.RcloneStateReady || replacement.Summary.BindingRevision != "1" {
		t.Fatal("replacement did not become ready after its own 15-minute window")
	}
	if writes, deletes := boundary.counts(); writes != 2 || deletes != 2 {
		t.Fatalf("native canaries after two stable windows: writes=%d deletes=%d", writes, deletes)
	}
}

func rcloneHTTPSeedRetainedKey(t *testing.T, fixture *rcloneHTTPFixture, repositoryID string) {
	t.Helper()
	const retainedARN = "arn:aws:kms:us-east-1:123456789012:key/http-historical-key"
	key := provider.RcloneNativeKMSKey{ARN: retainedARN, AccountID: "123456789012", Region: "us-east-1", Manager: "CUSTOMER", Spec: "SYMMETRIC_DEFAULT", Usage: "ENCRYPT_DECRYPT", State: "Enabled", Origin: "AWS_KMS"}
	retained, err := provider.RcloneNativeKMSKeyBinding(key)
	if err != nil {
		t.Fatal("construct retained historical key fixture")
	}
	var binding model.RepositoryAccessBinding
	if err := fixture.db.Where("repository_id = ? AND status = ?", repositoryID, "active").First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	document := map[string]any{}
	if err := json.Unmarshal([]byte(binding.EncryptedConfig), &document); err != nil {
		t.Fatal("decode native binding fixture")
	}
	native, ok := document["native"].(map[string]any)
	saltString, saltOK := document["identity_salt"].(string)
	salt, err := hex.DecodeString(saltString)
	if !ok || !saltOK || err != nil {
		t.Fatal("native binding fixture has no salt or native identity")
	}
	native["retained_read_keys"] = []map[string]string{{"key_arn": retainedARN, "key_digest": retained.Digest}}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal("encode native binding fixture")
	}
	mac := hmac.New(sha256.New, salt)
	_, _ = io.WriteString(mac, "xirang-managed-rclone-binding-v3\nactive-binding\n")
	_, _ = mac.Write(encoded)
	binding.EncryptedConfig = string(encoded)
	binding.ConfigFingerprint = hex.EncodeToString(mac.Sum(nil))
	if err := fixture.db.Save(&binding).Error; err != nil {
		t.Fatal("persist historical read-key fixture")
	}
	fixture.private = append(fixture.private, retainedARN)
}

func (fixture *rcloneHTTPFixture) assertExpiredPublicationDenied(id uint) {
	fixture.t.Helper()
	var task model.Task
	if err := fixture.db.Preload("Node").First(&task, id).Error; err != nil {
		fixture.t.Fatal(err)
	}
	started := fixture.clock.Now()
	run := model.TaskRun{TaskID: id, NodeIDSnapshot: task.NodeID, TriggerType: "manual", Status: model.TaskRunStatusRunning, StartedAt: &started}
	if err := fixture.db.Create(&run).Error; err != nil {
		fixture.t.Fatal(err)
	}
	audit, err := backupasset.NewSystemPublicationAuditContext("rclone-expired-publication")
	if err != nil {
		fixture.t.Fatal(err)
	}
	execution, err := fixture.runtime.PublicationCoordinator().Prepare(context.Background(), publication.Run{Task: task, TaskRunID: run.ID, Trigger: "manual", StartedAt: started, Audit: audit})
	if execution != nil {
		_ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned)
	}
	if !errors.Is(err, backupasset.ErrForbidden) {
		fixture.t.Fatal("expired Rclone authorization was not rejected at the publication gate")
	}
	if err := fixture.db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": model.TaskRunStatusFailed, "finished_at": started}).Error; err != nil {
		fixture.t.Fatal(err)
	}
}

func (fixture *rcloneHTTPFixture) assertRenewalRejectsActiveRun(id uint, revision string) {
	fixture.t.Helper()
	var task model.Task
	if err := fixture.db.First(&task, id).Error; err != nil {
		fixture.t.Fatal(err)
	}
	started := fixture.clock.Now()
	run := model.TaskRun{TaskID: id, NodeIDSnapshot: task.NodeID, TriggerType: "manual", Status: model.TaskRunStatusRunning, StartedAt: &started}
	if err := fixture.db.Create(&run).Error; err != nil {
		fixture.t.Fatal(err)
	}
	fixture.request("POST", fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/preflights", id), map[string]any{
		"expected_task_revision": revision, "requested_mode": "native_object_versions",
	}, 409, nil)
	if err := fixture.db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": model.TaskRunStatusFailed, "finished_at": started}).Error; err != nil {
		fixture.t.Fatal(err)
	}
}
