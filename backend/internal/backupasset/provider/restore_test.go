package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestRsyncRestoreSourceRefHasNoPrivateSourceFields(t *testing.T) {
	refType := reflect.TypeOf(RsyncRestoreSourceRef{})
	want := map[string]string{
		"PlanID":               "plan_id",
		"PlanBindingDigest":    "plan_binding_digest",
		"RepositoryID":         "repository_id",
		"RecoveryPointID":      "recovery_point_id",
		"CatalogGenerationID":  "catalog_generation_id",
		"SelectionDigest":      "selection_digest",
		"SourceRevisionDigest": "source_revision_digest",
		"ManifestDigest":       "manifest_digest",
	}
	if refType.NumField() != len(want) {
		t.Fatalf("RsyncRestoreSourceRef fields = %d, want %d", refType.NumField(), len(want))
	}
	for index := 0; index < refType.NumField(); index++ {
		field := refType.Field(index)
		jsonName := field.Tag.Get("json")
		if field.PkgPath != "" || field.Type.Kind() != reflect.String || want[field.Name] != jsonName {
			t.Fatalf("RsyncRestoreSourceRef field = %#v, want exported scalar safe field", field)
		}
		delete(want, field.Name)
	}
	if len(want) != 0 {
		t.Fatalf("RsyncRestoreSourceRef missing fields: %v", want)
	}
	source, err := os.ReadFile("restore.go")
	if err != nil {
		t.Fatalf("read Provider restore source: %v", err)
	}
	for _, forbidden := range []string{
		"type rsyncRestoreSourceCapability struct",
		"type RsyncRestoreSourceValidation struct",
		"type RsyncRestoreSourceIssuer func",
		"func IssueRsyncRestoreSource(",
	} {
		if bytes.Contains(source, []byte(forbidden)) {
			t.Fatalf("Provider still exposes superseded Rsync source issuer %q", forbidden)
		}
	}
}

func TestRsyncResolutionIntentAcceptsOnlyDigestlessDurableEntryFacts(t *testing.T) {
	request := validRsyncRestoreRequest(t)
	request.Entries[0].ExpectedDigest = ""
	if err := request.ValidateRsyncResolutionIntent(); err != nil {
		t.Fatalf("digestless Rsync resolution intent: %v", err)
	}
	if err := request.ValidateIntent(); !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("unmaterialized request strict validation error=%v, want invalid restore request", err)
	}

	request.Entries[0].ExpectedDigest = strings.Repeat("9", 64)
	if err := request.ValidateRsyncResolutionIntent(); !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("caller-supplied Rsync digest error=%v, want invalid restore request", err)
	}
}

func TestRestoreRequestRejectsClosedUnionBeforeExecute(t *testing.T) {
	valid := validResticRestoreRequest(t)
	cases := []struct {
		name    string
		mutate  func(*RestoreRequest)
		request RestoreRequest
	}{
		{name: "empty", request: RestoreRequest{}},
		{
			name: "dual provider arms",
			mutate: func(request *RestoreRequest) {
				request.Rsync = &RsyncRestoreRequest{ManifestDigest: strings.Repeat("a", 64)}
			},
		},
		{
			name: "unknown provider",
			mutate: func(request *RestoreRequest) {
				request.Provider = backupasset.ProviderCommand
			},
		},
		{
			name: "latest snapshot",
			mutate: func(request *RestoreRequest) {
				request.Restic.SnapshotID = "latest"
			},
		},
		{
			name: "shell fragment include",
			mutate: func(request *RestoreRequest) {
				request.Restic.Includes = []string{"/var/lib/xirang; rm -rf /"}
			},
		},
		{
			name: "swapped source locator",
			mutate: func(request *RestoreRequest) {
				request.Source.Locator = "FAKE_SWAPPED_SOURCE_LOCATOR"
			},
		},
		{
			name: "swapped recovery point binding",
			mutate: func(request *RestoreRequest) {
				request.Source.RecoveryPointID = strings.Repeat("f", 32)
			},
		},
		{
			name: "swapped target root binding",
			mutate: func(request *RestoreRequest) {
				request.Target.RootID = "other-approved-root"
			},
		},
		{
			name: "swapped target root digest",
			mutate: func(request *RestoreRequest) {
				request.Target.RootLocatorDigest = strings.Repeat("f", 64)
			},
		},
		{
			name: "missing permanent use latch",
			mutate: func(request *RestoreRequest) {
				request.MutationPermit.UseLatchID = ""
			},
		},
		{
			name: "stale credential session",
			mutate: func(request *RestoreRequest) {
				request.MutationPermit.Session.ExpiresAt = time.Now().UTC().Add(-time.Second)
			},
		},
		{
			name: "mismatched target chain revision",
			mutate: func(request *RestoreRequest) {
				request.MutationPermit.ExpectedTargetRevision = "target-revision-other"
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := test.request
			if test.mutate != nil {
				request = cloneRestoreRequest(valid)
				test.mutate(&request)
			}
			port := &countingRestorePort{}
			_, err := ExecuteRestore(context.Background(), port, request, RestoreProgress{})
			if !errors.Is(err, ErrInvalidRestoreRequest) {
				t.Fatalf("ExecuteRestore error = %v, want ErrInvalidRestoreRequest", err)
			}
			if port.executeCalls != 0 {
				t.Fatalf("invalid request reached RestorePort.Execute %d time(s)", port.executeCalls)
			}
		})
	}
}

func TestRestoreRequestHidesPrivateLocatorAndSessionValuesFromJSON(t *testing.T) {
	request := validResticRestoreRequest(t)
	request.Restic.Includes = []string{"/FAKE_PRIVATE_RESTIC_INCLUDE_FOR_TEST_ONLY"}
	request.Target.RootLocatorDigest = "FAKE_PRIVATE_TARGET_ROOT_FOR_TEST_ONLY"
	request.MutationPermit.Session.ID = "FAKE_PRIVATE_CREDENTIAL_SESSION_FOR_TEST_ONLY"

	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal restore request: %v", err)
	}
	for _, forbidden := range []string{
		"FAKE_EXACT_SOURCE_LOCATOR_FOR_TEST_ONLY",
		"FAKE_PRIVATE_RESTIC_INCLUDE_FOR_TEST_ONLY",
		"FAKE_PRIVATE_TARGET_ROOT_FOR_TEST_ONLY",
		"FAKE_PRIVATE_CREDENTIAL_SESSION_FOR_TEST_ONLY",
	} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("private restore value %q leaked in %s", forbidden, payload)
		}
	}
}

func TestRestoreRequestExecutesExactTypedRequest(t *testing.T) {
	port := &countingRestorePort{}
	request := validResticRestoreRequest(t)
	port.result = validRestoreResult(request)
	if _, err := ExecuteRestore(context.Background(), port, request, RestoreProgress{}); err != nil {
		t.Fatalf("ExecuteRestore exact request: %v", err)
	}
	if port.executeCalls != 1 {
		t.Fatalf("exact request execute calls = %d, want 1", port.executeCalls)
	}
}

func TestRestoreRequestRejectsDirectSourceConstructionBeforePortCall(t *testing.T) {
	request := validResticRestoreRequest(t)
	forged := RestoreSource{
		Provider:        request.Source.Provider,
		RepositoryID:    request.Source.RepositoryID,
		RecoveryPointID: request.Source.RecoveryPointID,
		Locator:         request.Source.Locator,
	}
	digest, err := restoreSourceLocatorDigest(forged)
	if err != nil {
		t.Fatalf("restoreSourceLocatorDigest: %v", err)
	}
	forged.LocatorDigest = digest
	request.Source = forged
	port := &countingRestorePort{result: validRestoreResult(request)}

	_, err = ExecuteRestore(context.Background(), port, request, RestoreProgress{})
	if !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("ExecuteRestore direct source error = %v, want ErrInvalidRestoreRequest", err)
	}
	if port.executeCalls != 0 {
		t.Fatalf("direct source reached RestorePort.Execute %d time(s)", port.executeCalls)
	}
}

func TestRestoreRequestAcceptsEachExactProviderArm(t *testing.T) {
	t.Run("rsync scalar ref", func(t *testing.T) {
		request := validRsyncRestoreRequest(t)
		port := &countingRestorePort{result: validRestoreResult(request)}
		if _, err := ExecuteRestore(context.Background(), port, request, RestoreProgress{}); err != nil {
			t.Fatalf("ExecuteRestore Rsync request: %v", err)
		}
		if port.executeCalls != 1 {
			t.Fatalf("Rsync execute calls = %d, want 1", port.executeCalls)
		}
	})
	t.Run("rclone", func(t *testing.T) {
		request := cloneRestoreRequest(validResticRestoreRequest(t))
		source, err := NewValidatedRestoreSource(backupasset.ProviderRclone, request.Source.RepositoryID, request.Source.RecoveryPointID, request.Source.Locator)
		if err != nil {
			t.Fatal(err)
		}
		request.Provider = backupasset.ProviderRclone
		request.Source = source
		request.Rclone = &RcloneRestoreRequest{ManifestDigest: strings.Repeat("f", 64), CommittedPrefixDigest: strings.Repeat("e", 64)}
		request.Restic = nil
		port := &countingRestorePort{result: validRestoreResult(request)}
		if _, err := ExecuteRestore(context.Background(), port, request, RestoreProgress{}); err != nil {
			t.Fatalf("ExecuteRestore Rclone request: %v", err)
		}
		if port.executeCalls != 1 {
			t.Fatalf("Rclone execute calls = %d, want 1", port.executeCalls)
		}
	})
}

func TestExecuteRestoreRejectsProcessEvidenceAsSuccessfulCheckpoint(t *testing.T) {
	request := validResticRestoreRequest(t)
	evidence, err := NewRestoreEvidence(RestoreProcessEvidenceInput{
		Stdout: []byte("FAKE_PROVIDER_STDOUT_FOR_TEST_ONLY"),
		Stderr: []byte("FAKE_PROVIDER_STDERR_FOR_TEST_ONLY"),
	})
	if err != nil {
		t.Fatalf("NewRestoreEvidence: %v", err)
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("marshal restore evidence: %v", err)
	}
	for _, forbidden := range []string{"FAKE_PROVIDER_STDOUT_FOR_TEST_ONLY", "FAKE_PROVIDER_STDERR_FOR_TEST_ONLY"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("raw process output %q leaked in %s", forbidden, payload)
		}
	}
	result := validRestoreResult(request)
	result.Checkpoint.VerifiedTargetIdentityDigest = ""
	result.Evidence = []RestoreEvidence{evidence}
	port := &countingRestorePort{result: result}

	if _, err := ExecuteRestore(context.Background(), port, request, RestoreProgress{}); !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("ExecuteRestore unverified checkpoint error = %v, want ErrInvalidRestoreRequest", err)
	}
	if port.executeCalls != 1 {
		t.Fatalf("unverified checkpoint execute calls = %d, want 1", port.executeCalls)
	}
}

func TestRestoreCheckpointRequiresTypedTargetVerificationEvidence(t *testing.T) {
	request := validResticRestoreRequest(t)
	result := validRestoreResult(request)
	result.Checkpoint.VerifiedTargetIdentityDigest = ""
	port := &countingRestorePort{result: result}

	if _, err := ExecuteRestore(context.Background(), port, request, RestoreProgress{}); !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("ExecuteRestore checkpoint without target evidence error = %v, want ErrInvalidRestoreRequest", err)
	}
}

func TestRestorePhaseWrappersRejectUnvalidatedResults(t *testing.T) {
	request := validResticRestoreRequest(t)
	now := time.Now().UTC()
	preflightPermit, err := NewTargetPreflightPermit(validObservationPermit(request, TargetPurposePreflight, now), request.Target, now)
	if err != nil {
		t.Fatalf("NewTargetPreflightPermit: %v", err)
	}
	verifyPermit, err := NewTargetVerifyPermit(validObservationPermit(request, TargetPurposeVerify, now), request.Target, now)
	if err != nil {
		t.Fatalf("NewTargetVerifyPermit: %v", err)
	}
	reconcilePermit, err := NewTargetReconcilePermit(validObservationPermit(request, TargetPurposeReconcile, now), request.Target, now)
	if err != nil {
		t.Fatalf("NewTargetReconcilePermit: %v", err)
	}

	tests := []struct {
		name string
		call func(*countingRestorePort) error
	}{
		{
			name: "preflight",
			call: func(port *countingRestorePort) error {
				_, err := PreflightRestore(context.Background(), port, RestorePreflightRequest{Request: request, Permit: preflightPermit})
				return err
			},
		},
		{
			name: "verify",
			call: func(port *countingRestorePort) error {
				_, err := VerifyRestore(context.Background(), port, RestoreVerifyRequest{Request: request, Permit: verifyPermit})
				return err
			},
		},
		{
			name: "reconcile",
			call: func(port *countingRestorePort) error {
				_, err := ReconcileRestore(context.Background(), port, RestoreReconcileRequest{Request: request, Permit: reconcilePermit})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(&countingRestorePort{}); !errors.Is(err, ErrInvalidRestoreRequest) {
				t.Fatalf("%s error = %v, want ErrInvalidRestoreRequest", test.name, err)
			}
		})
	}
}

func TestPreflightRestoreRejectsWrongPurposePermitBeforePortCall(t *testing.T) {
	request := validResticRestoreRequest(t)
	now := time.Now().UTC()
	wrongPurpose := validObservationPermit(request, TargetPurposeVerify, now)
	if _, err := NewTargetPreflightPermit(wrongPurpose, request.Target, now); !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("NewTargetPreflightPermit wrong purpose error = %v, want ErrInvalidRestoreRequest", err)
	}
	port := &countingRestorePort{}
	if _, err := PreflightRestore(context.Background(), port, RestorePreflightRequest{Request: request}); !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("PreflightRestore missing permit error = %v, want ErrInvalidRestoreRequest", err)
	}
	if port.preflightCalls != 0 {
		t.Fatalf("invalid preflight reached RestorePort.Preflight %d time(s)", port.preflightCalls)
	}
}

func TestRestorePhaseWrappersReturnValidatedEvidence(t *testing.T) {
	request := validResticRestoreRequest(t)
	now := time.Now().UTC()
	preflightPermit, err := NewTargetPreflightPermit(validObservationPermit(request, TargetPurposePreflight, now), request.Target, now)
	if err != nil {
		t.Fatalf("NewTargetPreflightPermit: %v", err)
	}
	verifyPermit, err := NewTargetVerifyPermit(validObservationPermit(request, TargetPurposeVerify, now), request.Target, now)
	if err != nil {
		t.Fatalf("NewTargetVerifyPermit: %v", err)
	}
	reconcilePermit, err := NewTargetReconcilePermit(validObservationPermit(request, TargetPurposeReconcile, now), request.Target, now)
	if err != nil {
		t.Fatalf("NewTargetReconcilePermit: %v", err)
	}
	result := validRestoreResult(request)
	port := &countingRestorePort{
		preflight: RestorePreflightEvidence{TargetBindingDigest: request.Target.BindingDigest, TargetRevision: request.Target.TargetRevision, Checkpoint: request.Checkpoint},
		verify:    RestoreVerifyResult{Checkpoint: result.Checkpoint},
		reconcile: RestoreReconcileResult{Checkpoint: result.Checkpoint},
	}

	if _, err := PreflightRestore(context.Background(), port, RestorePreflightRequest{Request: request, Permit: preflightPermit}); err != nil {
		t.Fatalf("PreflightRestore: %v", err)
	}
	if _, err := VerifyRestore(context.Background(), port, RestoreVerifyRequest{Request: request, Permit: verifyPermit}); err != nil {
		t.Fatalf("VerifyRestore: %v", err)
	}
	if _, err := ReconcileRestore(context.Background(), port, RestoreReconcileRequest{Request: request, Permit: reconcilePermit}); err != nil {
		t.Fatalf("ReconcileRestore: %v", err)
	}
	if port.preflightCalls != 1 || port.verifyCalls != 1 || port.reconcileCalls != 1 {
		t.Fatalf("phase calls preflight=%d verify=%d reconcile=%d, want one each", port.preflightCalls, port.verifyCalls, port.reconcileCalls)
	}
}

func TestDecodeRestoreRequestRejectsRawExecutorAndCredentialExtensions(t *testing.T) {
	for name, payload := range map[string]string{
		"executor":    `{"executor":"rsync --delete /source /target"}`,
		"command":     `{"command":"sh -c unsafe"}`,
		"credentials": `{"credentials":"FAKE_PASSWORD_FOR_TEST_ONLY"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeRestoreRequest([]byte(payload))
			if !errors.Is(err, ErrInvalidRestoreRequest) {
				t.Fatalf("DecodeRestoreRequest error = %v, want ErrInvalidRestoreRequest", err)
			}
		})
	}
}

func validResticRestoreRequest(t *testing.T) RestoreRequest {
	t.Helper()
	now := time.Now().UTC()
	repositoryID := strings.Repeat("1", 32)
	recoveryPointID := strings.Repeat("2", 32)
	source, err := NewValidatedRestoreSource(
		backupasset.ProviderRestic,
		repositoryID,
		recoveryPointID,
		"FAKE_EXACT_SOURCE_LOCATOR_FOR_TEST_ONLY",
	)
	if err != nil {
		t.Fatalf("NewRestoreSource: %v", err)
	}
	target, err := NewRestoreTarget(
		7,
		"approved-root",
		strings.Repeat("3", 64),
		strings.Repeat("4", 64),
		"root-revision-1",
		"target-revision-1",
	)
	if err != nil {
		t.Fatalf("NewRestoreTarget: %v", err)
	}
	fence := RestoreFence{
		JobID:                  strings.Repeat("5", 32),
		AttemptID:              strings.Repeat("6", 32),
		NodeLeaseID:            strings.Repeat("7", 32),
		AttemptFence:           11,
		NodeFence:              13,
		ExpectedTargetRevision: target.TargetRevision,
	}
	checkpoint := RestoreCheckpoint{
		ID:                           strings.Repeat("8", 32),
		OperationDigest:              strings.Repeat("9", 64),
		PriorTargetRevision:          target.TargetRevision,
		VerifiedTargetIdentityDigest: strings.Repeat("0", 64),
		VerifiedTargetRevision:       target.TargetRevision,
		VerifiedBytes:                17,
		AttemptFence:                 fence.AttemptFence,
		NodeFence:                    fence.NodeFence,
	}
	permit := TargetMutationPermit{
		TargetBindingDigest:    target.BindingDigest,
		UseLatchID:             RestoreSchemaUseLatchID,
		JobID:                  fence.JobID,
		AttemptID:              fence.AttemptID,
		NodeLeaseID:            fence.NodeLeaseID,
		AttemptFence:           fence.AttemptFence,
		NodeFence:              fence.NodeFence,
		ExpectedTargetRevision: target.TargetRevision,
		Session: TargetSession{
			ID:                 strings.Repeat("a", 32),
			Purpose:            TargetPurposeWrite,
			CredentialRevision: "credential-revision-1",
			ExpiresAt:          now.Add(time.Minute),
		},
	}
	return RestoreRequest{
		Version:        RestoreRequestSchemaV1,
		Provider:       backupasset.ProviderRestic,
		Source:         source,
		Entries:        []RestoreEntry{{AssetRef: backupasset.AssetRef{RecoveryPointID: recoveryPointID, EntryID: strings.Repeat("b", 64)}, Type: backupasset.CatalogEntryFile, ExpectedSize: 17, ExpectedDigest: strings.Repeat("c", 64), TargetObjectDigest: strings.Repeat("e", 64)}},
		Target:         target,
		Limits:         RestoreLimits{MaxEntries: 2, MaxBytes: 1024, MaxEntryBytes: 1024},
		ConflictPolicy: RestoreConflictFailOnConflict,
		Fence:          fence,
		Checkpoint:     checkpoint,
		MutationPermit: permit,
		Restic: &ResticRestoreRequest{
			SnapshotID: strings.Repeat("d", 64),
			Includes:   []string{"/var/lib/xirang"},
		},
	}
}

func cloneRestoreRequest(request RestoreRequest) RestoreRequest {
	request.Entries = append([]RestoreEntry(nil), request.Entries...)
	request.Restic = cloneResticRestoreRequest(request.Restic)
	request.Rsync = cloneRsyncRestoreRequest(request.Rsync)
	request.Rclone = cloneRcloneRestoreRequest(request.Rclone)
	return request
}

func cloneResticRestoreRequest(request *ResticRestoreRequest) *ResticRestoreRequest {
	if request == nil {
		return nil
	}
	clone := *request
	clone.Includes = append([]string(nil), request.Includes...)
	return &clone
}

func cloneRsyncRestoreRequest(request *RsyncRestoreRequest) *RsyncRestoreRequest {
	if request == nil {
		return nil
	}
	clone := *request
	return &clone
}

func cloneRcloneRestoreRequest(request *RcloneRestoreRequest) *RcloneRestoreRequest {
	if request == nil {
		return nil
	}
	clone := *request
	return &clone
}

func validRestoreResult(request RestoreRequest) RestoreResult {
	return RestoreResult{
		Checkpoint: RestoreCheckpoint{
			ID:                           strings.Repeat("f", 32),
			OperationDigest:              strings.Repeat("1", 64),
			PriorTargetRevision:          request.Target.TargetRevision,
			VerifiedTargetIdentityDigest: strings.Repeat("2", 64),
			VerifiedTargetRevision:       "target-revision-2",
			VerifiedBytes:                17,
			AttemptFence:                 request.Fence.AttemptFence,
			NodeFence:                    request.Fence.NodeFence,
		},
	}
}

func validObservationPermit(request RestoreRequest, purpose RestoreTargetPurpose, now time.Time) TargetObservationPermit {
	return TargetObservationPermit{
		TargetBindingDigest: request.Target.BindingDigest,
		Session: TargetSession{
			ID:                 strings.Repeat("e", 32),
			Purpose:            purpose,
			CredentialRevision: "credential-revision-1",
			ExpiresAt:          now.Add(time.Minute),
		},
	}
}

type countingRestorePort struct {
	kind           backupasset.ProviderKind
	preflightCalls int
	executeCalls   int
	verifyCalls    int
	reconcileCalls int
	result         RestoreResult
	preflight      RestorePreflightEvidence
	verify         RestoreVerifyResult
	reconcile      RestoreReconcileResult
}

func (port *countingRestorePort) ProviderKind() backupasset.ProviderKind {
	return port.kind
}

func (port *countingRestorePort) Preflight(context.Context, RestorePreflightRequest) (RestorePreflightEvidence, error) {
	port.preflightCalls++
	return port.preflight, nil
}

func (port *countingRestorePort) Execute(context.Context, RestoreRequest, RestoreProgress) (RestoreResult, error) {
	port.executeCalls++
	return port.result, nil
}

func (port *countingRestorePort) Verify(context.Context, RestoreVerifyRequest) (RestoreVerifyResult, error) {
	port.verifyCalls++
	return port.verify, nil
}

func (port *countingRestorePort) Reconcile(context.Context, RestoreReconcileRequest) (RestoreReconcileResult, error) {
	port.reconcileCalls++
	return port.reconcile, nil
}

var _ RestorePort = (*countingRestorePort)(nil)
