package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

type rclonePreflightCommandFake struct {
	invocations []CommandInvocation
	payload     []byte
	listPayload []byte
}

func (fake *rclonePreflightCommandFake) Run(_ context.Context, invocation CommandInvocation, _ OperationLimits) (CommandOutput, error) {
	fake.invocations = append(fake.invocations, invocation)
	if err := invocation.Validate(); err != nil {
		return CommandOutput{}, err
	}
	if invocation.Operation == OperationRcloneManagedVersion {
		return CommandOutput{Stdout: []byte("rclone v1.74.4\n")}, nil
	}
	return CommandOutput{}, nil
}

func (fake *rclonePreflightCommandFake) Open(_ context.Context, invocation CommandInvocation, _ OperationLimits, _ int64) (ReadHandle, error) {
	fake.invocations = append(fake.invocations, invocation)
	if err := invocation.Validate(); err != nil {
		return nil, err
	}
	if invocation.Operation == OperationRcloneManagedRecursiveList {
		return io.NopCloser(bytes.NewReader(fake.listPayload)), nil
	}
	return io.NopCloser(bytes.NewReader(fake.payload)), nil
}

func (fake *rclonePreflightCommandFake) OpenExecution(_ context.Context, invocation CommandInvocation, _ OperationLimits, _ int64) (CommandExecution, error) {
	fake.invocations = append(fake.invocations, invocation)
	if err := invocation.Validate(); err != nil {
		return nil, err
	}
	return &rclonePreflightExecutionFake{completion: CommandCompletion{ExitCodeKnown: true, ExitCode: 3}}, nil
}

type rclonePreflightExecutionFake struct {
	completion CommandCompletion
}

func (*rclonePreflightExecutionFake) Read([]byte) (int, error) { return 0, io.EOF }
func (fake *rclonePreflightExecutionFake) Join() (CommandCompletion, error) {
	return fake.completion, nil
}
func (*rclonePreflightExecutionFake) Cancel() error { return nil }

type rclonePreflightStagingFake struct {
	staged  int
	cleaned int
}

func (fake *rclonePreflightStagingFake) Stage(_ context.Context, _ RemoteCommandAccess, request StagedPayloadRequest) (StagedPayloadRef, error) {
	fake.staged++
	digest := sha256.Sum256(request.Payload)
	return StagedPayloadRef{
		attemptID:       request.AttemptID,
		name:            request.Name,
		path:            "/tmp/xirang-preflight/" + request.AttemptID + "/" + request.Name,
		ownerMarkerPath: "/tmp/xirang-preflight/" + request.AttemptID + "/" + stagedPayloadOwnerMarkerName,
		size:            int64(len(request.Payload)), digest: hex.EncodeToString(digest[:]), ownerDigest: strings.Repeat("a", 64),
		lease: &stagedPayloadLease{},
	}, nil
}

func (fake *rclonePreflightStagingFake) Cleanup(_ context.Context, _ RemoteCommandAccess, _ StagedPayloadRef) error {
	fake.cleaned++
	return nil
}

func (*rclonePreflightStagingFake) CleanupAged(context.Context, RemoteCommandAccess, time.Duration, int) error {
	return nil
}

func TestCommandRclonePreflightPlaneProvesPortableCanaryWithoutDeletingEvidence(t *testing.T) {
	configBytes := []byte("[archive]\ntype = s3\nprovider = AWS\naccess_key_id = FAKE_ACCESS_KEY_FOR_TEST_ONLY\nsecret_access_key = FAKE_SECRET_ACCESS_KEY_FOR_TEST_ONLY\n")
	bound, err := ValidateRcloneBoundConfigV1744(configBytes, "archive", []byte("FAKE_IDENTITY_KEY_FOR_TEST_ONLY_32_BYTES"), int64(len(configBytes)))
	if err != nil {
		t.Fatalf("validate bound config: %v", err)
	}
	listPayload := []byte(`[{"Path":"canary.bin","Name":"canary.bin","Size":64,"ModTime":"2026-07-16T00:00:00Z","IsDir":false,"Hashes":{"sha256":"` + strings.Repeat("b", 64) + `"}}]`)
	commands := &rclonePreflightCommandFake{payload: bytes.Repeat([]byte{0x2a}, 64), listPayload: listPayload}
	staging := &rclonePreflightStagingFake{}
	plane, err := NewCommandRclonePreflightPlane(commands, commands, staging, func() (OperationLimits, error) {
		return NewMetadataOperationLimits(time.Minute, 1<<20)
	}, bytes.NewReader(bytes.Repeat([]byte{0x2a}, 64)))
	if err != nil {
		t.Fatalf("new preflight plane: %v", err)
	}
	root, err := NewRclonePrivateLocator("archive:managed")
	if err != nil {
		t.Fatal(err)
	}
	result, err := plane.PreflightPortable(context.Background(), RclonePortableCommandPreflightRequest{
		PreflightID: strings.Repeat("a", 32), BoundConfig: bound, ManagedRoot: root,
		AbsoluteDeadline: time.Now().UTC().Add(time.Hour), LowLevelRetries: 3,
		ControlPayloadMaxBytes: 1 << 20, FullVerifyMaxBytes: 1 << 20,
		ManifestOptions: RcloneManifestBuildOptions{
			Limits:        ManifestLimits{Timeout: time.Minute, MaxBytes: 1 << 20, MaxEntries: 10, MaxRecordBytes: 1 << 16, MaxDepth: 16},
			ChunkMaxBytes: 1 << 16, ChunkMaxEntries: 10, SpoolMaxBytes: 1 << 20,
		},
	})
	if err != nil {
		t.Fatalf("portable command preflight: %v", err)
	}
	if result.VerifiedBytes != 64 || !lowerHex(result.ManagedRootIdentityDigest, 64) ||
		!lowerHex(result.RepositoryMarkerDigest, 64) || !lowerHex(result.EvidenceDigest, 64) {
		t.Fatalf("portable command evidence=%+v", result)
	}
	if staging.staged != 1 || staging.cleaned != 1 {
		t.Fatalf("staging lifecycle staged=%d cleaned=%d", staging.staged, staging.cleaned)
	}
	assertRclonePreflightOperations(t, commands.invocations, []CommandOperation{
		OperationRcloneManagedVersion, OperationRcloneManagedExactStat, OperationRcloneManagedCopyTo,
		OperationRcloneManagedCopy, OperationRcloneManagedFeatures, OperationRcloneManagedRecursiveList,
		OperationRcloneManagedRecursiveList, OperationRcloneManagedCheckDownload, OperationRcloneManagedCat,
		OperationRcloneManagedCat,
	})
}

func TestCommandRclonePreflightPlaneWritesNativeCanaryWithExactGeneratedConfig(t *testing.T) {
	commands := &rclonePreflightCommandFake{payload: bytes.Repeat([]byte{0x5c}, 64)}
	staging := &rclonePreflightStagingFake{}
	plane, err := NewCommandRclonePreflightPlane(commands, commands, staging, func() (OperationLimits, error) {
		return NewMetadataOperationLimits(time.Minute, 1<<20)
	}, bytes.NewReader(bytes.Repeat([]byte{0x5c}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	destination, err := NewRclonePrivateLocator("xirang_native:bucket/managed/control/preflight/" + strings.Repeat("b", 32) + "/canary.bin")
	if err != nil {
		t.Fatal(err)
	}
	result, err := plane.WriteNativeCanary(context.Background(), RcloneNativeCommandPreflightRequest{
		PreflightID: strings.Repeat("b", 32), RcloneConfig: []byte("[xirang_native]\ntype = s3\n"), Destination: destination,
		AbsoluteDeadline: time.Now().UTC().Add(45 * time.Minute), LowLevelRetries: 3, ControlPayloadMaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("native command preflight: %v", err)
	}
	if result.PayloadBytes != 64 || result.RangeBytes != 16 || !lowerHex(result.PayloadDigest, 64) || !lowerHex(result.RangeDigest, 64) {
		t.Fatalf("native command evidence=%+v", result)
	}
	if staging.staged != 1 || staging.cleaned != 1 {
		t.Fatalf("native staging lifecycle staged=%d cleaned=%d", staging.staged, staging.cleaned)
	}
	assertRclonePreflightOperations(t, commands.invocations, []CommandOperation{
		OperationRcloneManagedVersion, OperationRcloneManagedCopyTo, OperationRcloneManagedCat,
	})
}

func assertRclonePreflightOperations(t *testing.T, invocations []CommandInvocation, expected []CommandOperation) {
	t.Helper()
	if len(invocations) != len(expected) {
		t.Fatalf("operations=%v want=%v", rclonePreflightOperations(invocations), expected)
	}
	for index, operation := range expected {
		if invocations[index].Operation != operation {
			t.Fatalf("operation[%d]=%s want=%s", index, invocations[index].Operation, operation)
		}
		if bytes.Contains(invocations[index].SecretStdin, []byte("provider_secret")) {
			t.Fatalf("unexpected foreign provider secret in invocation %d", index)
		}
	}
}

func rclonePreflightOperations(invocations []CommandInvocation) []CommandOperation {
	result := make([]CommandOperation, len(invocations))
	for index := range invocations {
		result[index] = invocations[index].Operation
	}
	return result
}

var _ CommandTransport = (*rclonePreflightCommandFake)(nil)
var _ CommandStreamTransport = (*rclonePreflightCommandFake)(nil)
var _ StagedPayloadTransport = (*rclonePreflightStagingFake)(nil)
var _ = backupasset.ProviderRclone
