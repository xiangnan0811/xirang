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

func TestResticRestoreRequestValidatesExactSnapshotAndIncludes(t *testing.T) {
	valid := validResticRestoreRequest(t).Restic
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Restic restore request: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*ResticRestoreRequest)
	}{
		{name: "latest snapshot", mutate: func(request *ResticRestoreRequest) { request.SnapshotID = "latest" }},
		{name: "abbreviated snapshot", mutate: func(request *ResticRestoreRequest) { request.SnapshotID = "abcdef" }},
		{name: "missing include", mutate: func(request *ResticRestoreRequest) { request.Includes = nil }},
		{name: "relative include", mutate: func(request *ResticRestoreRequest) { request.Includes = []string{"var/lib/xirang"} }},
		{name: "traversal include", mutate: func(request *ResticRestoreRequest) { request.Includes = []string{"/var/lib/../other"} }},
		{name: "shell fragment include", mutate: func(request *ResticRestoreRequest) { request.Includes = []string{"/var/lib/xirang; rm -rf /"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := cloneResticRestoreRequest(valid)
			test.mutate(request)
			if err := request.Validate(); !errors.Is(err, ErrInvalidRestoreRequest) {
				t.Fatalf("Restic request error = %v, want ErrInvalidRestoreRequest", err)
			}
		})
	}
}

func TestRcloneRestoreDTOValidatesClosedArmAndDigests(t *testing.T) {
	request := validResticRestoreRequest(t)
	source, err := NewValidatedRestoreSource(backupasset.ProviderRclone, request.Source.RepositoryID, request.Source.RecoveryPointID, request.Source.Locator)
	if err != nil {
		t.Fatalf("validated Rclone source: %v", err)
	}
	request.Provider = backupasset.ProviderRclone
	request.Source = source
	request.Restic = nil
	request.Rclone = &RcloneRestoreRequest{ManifestDigest: strings.Repeat("f", 64), CommittedPrefixDigest: strings.Repeat("e", 64)}
	if err := request.ValidateAt(time.Now().UTC()); err != nil {
		t.Fatalf("valid Rclone restore DTO: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*RestoreRequest)
	}{
		{name: "missing manifest", mutate: func(candidate *RestoreRequest) { candidate.Rclone.ManifestDigest = "" }},
		{name: "missing committed prefix", mutate: func(candidate *RestoreRequest) { candidate.Rclone.CommittedPrefixDigest = "" }},
		{name: "invalid manifest", mutate: func(candidate *RestoreRequest) { candidate.Rclone.ManifestDigest = "not-a-digest" }},
		{name: "dual provider arms", mutate: func(candidate *RestoreRequest) { candidate.Restic = &ResticRestoreRequest{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneRestoreRequest(request)
			test.mutate(&candidate)
			if err := candidate.ValidateAt(time.Now().UTC()); !errors.Is(err, ErrInvalidRestoreRequest) {
				t.Fatalf("invalid Rclone restore DTO: %v", err)
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

func TestRestoreSourceRejectsForgedOrSubstitutedCapability(t *testing.T) {
	request := validResticRestoreRequest(t)
	if err := request.ValidateAt(time.Now().UTC()); err != nil {
		t.Fatalf("validated source request: %v", err)
	}
	if source, err := NewRestoreSource(request.Source.Provider, request.Source.RepositoryID, request.Source.RecoveryPointID, request.Source.Locator); !errors.Is(err, ErrInvalidRestoreRequest) || source != (RestoreSource{}) {
		t.Fatalf("raw source constructor = %#v, %v; want invalid restore request", source, err)
	}
	if source, err := NewValidatedRestoreSource(backupasset.ProviderRsync, request.Source.RepositoryID, request.Source.RecoveryPointID, request.Source.Locator); !errors.Is(err, ErrInvalidRestoreRequest) || source != (RestoreSource{}) {
		t.Fatalf("generic Rsync source constructor = %#v, %v; want invalid restore request", source, err)
	}

	forged := RestoreSource{
		Provider: request.Source.Provider, RepositoryID: request.Source.RepositoryID,
		RecoveryPointID: request.Source.RecoveryPointID, Locator: request.Source.Locator,
		LocatorDigest: request.Source.LocatorDigest,
	}
	if err := forged.Validate(); !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("unsealed source with matching digest = %v, want invalid restore request", err)
	}
	request.Source = forged
	if err := request.ValidateAt(time.Now().UTC()); !errors.Is(err, ErrInvalidRestoreRequest) {
		t.Fatalf("request with unsealed source = %v, want invalid restore request", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*RestoreSource)
	}{
		{name: "provider", mutate: func(source *RestoreSource) { source.Provider = backupasset.ProviderRclone }},
		{name: "repository", mutate: func(source *RestoreSource) { source.RepositoryID = strings.Repeat("f", 32) }},
		{name: "recovery point", mutate: func(source *RestoreSource) { source.RecoveryPointID = strings.Repeat("f", 32) }},
		{name: "locator", mutate: func(source *RestoreSource) { source.Locator = "FAKE_SWAPPED_SOURCE_LOCATOR_FOR_TEST_ONLY" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := validResticRestoreRequest(t).Source
			test.mutate(&source)
			digest, err := restoreSourceLocatorDigest(source)
			if err != nil {
				t.Fatal(err)
			}
			source.LocatorDigest = digest
			if err := source.Validate(); !errors.Is(err, ErrInvalidRestoreRequest) {
				t.Fatalf("substituted sealed source with matching digest = %v, want invalid restore request", err)
			}
		})
	}
}

func TestRestoreObservationPermitConstructorsRejectWrongPurpose(t *testing.T) {
	request := validResticRestoreRequest(t)
	now := time.Now().UTC()
	for _, test := range []struct {
		name      string
		purpose   RestoreTargetPurpose
		construct func(TargetObservationPermit) error
	}{
		{name: "preflight", purpose: TargetPurposeVerify, construct: func(permit TargetObservationPermit) error {
			_, err := NewTargetPreflightPermit(permit, request.Target, now)
			return err
		}},
		{name: "verify", purpose: TargetPurposeReconcile, construct: func(permit TargetObservationPermit) error {
			_, err := NewTargetVerifyPermit(permit, request.Target, now)
			return err
		}},
		{name: "reconcile", purpose: TargetPurposePreflight, construct: func(permit TargetObservationPermit) error {
			_, err := NewTargetReconcilePermit(permit, request.Target, now)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.construct(validObservationPermit(request, test.purpose, now)); !errors.Is(err, ErrInvalidRestoreRequest) {
				t.Fatalf("wrong-purpose observation permit error = %v, want ErrInvalidRestoreRequest", err)
			}
		})
	}
}

func TestNewRestoreEvidenceDoesNotExposeRawProcessOutput(t *testing.T) {
	evidence, err := NewRestoreEvidence(RestoreProcessEvidenceInput{
		Stdout: []byte("FAKE_PROVIDER_STDOUT_FOR_TEST_ONLY"),
		Stderr: []byte("FAKE_PROVIDER_STDERR_FOR_TEST_ONLY"),
	})
	if err != nil {
		t.Fatalf("NewRestoreEvidence: %v", err)
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("sanitized restore evidence: %v", err)
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
