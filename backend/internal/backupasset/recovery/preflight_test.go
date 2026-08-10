package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

type readOnlyPreflightTargetFake struct {
	facts      TargetRootProbeFacts
	probeCalls int
	onProbe    func()
}

func (fake *readOnlyPreflightTargetFake) ProbeRoot(_ context.Context, _ TargetPreflightPermit, _ TargetProbeRequest) (TargetRootProbeFacts, error) {
	fake.probeCalls++
	if fake.onProbe != nil {
		fake.onProbe()
	}
	return fake.facts, nil
}

func (*readOnlyPreflightTargetFake) CreateOwnedJobDir(context.Context, TargetWritePermit, CreateOwnedJobDirRequest) (OwnedJobDir, error) {
	panic("read-only preflight called CreateOwnedJobDir")
}

func (*readOnlyPreflightTargetFake) Lstat(context.Context, TargetVerifyPermit, TargetLstatRequest) (TargetLstatResult, error) {
	panic("read-only preflight called Lstat")
}

func (*readOnlyPreflightTargetFake) CreateDirectory(context.Context, TargetWritePermit, CreateTargetDirectoryRequest) error {
	panic("read-only preflight called CreateDirectory")
}

func (*readOnlyPreflightTargetFake) WriteAtomic(context.Context, TargetWritePermit, TargetWriteAtomicRequest) (TargetWriteResult, error) {
	panic("read-only preflight called WriteAtomic")
}

func (*readOnlyPreflightTargetFake) FinalizeOverwrite(
	context.Context,
	TargetFinalizeOverwritePermit,
	TargetFinalizeOverwriteRequest,
) (TargetWriteResult, error) {
	panic("read-only preflight called FinalizeOverwrite")
}

func (*readOnlyPreflightTargetFake) Delete(
	context.Context,
	TargetDeletePermit,
	TargetDeleteRequest,
) (TargetWriteResult, error) {
	panic("read-only preflight called Delete")
}

func (*readOnlyPreflightTargetFake) Verify(
	context.Context,
	TargetVerifyPermit,
	TargetObjectRef,
	TargetVerifyExpectation,
) (TargetVerifyObservation, error) {
	panic("read-only preflight called Verify")
}

func (*readOnlyPreflightTargetFake) ValidateOwnedJobDir(
	context.Context,
	TargetCleanupPermit,
	ValidateOwnedJobDirRequest,
) (OwnedJobDirValidation, error) {
	panic("read-only preflight called ValidateOwnedJobDir")
}

func (*readOnlyPreflightTargetFake) RemoveOwnedJobDir(
	context.Context,
	TargetCleanupPermit,
	RemoveOwnedJobDirRequest,
) (OwnedJobDirRemoval, error) {
	panic("read-only preflight called RemoveOwnedJobDir")
}

func (*readOnlyPreflightTargetFake) ValidateOwnedJobDirRemoved(
	context.Context,
	TargetCleanupPermit,
	RemoveOwnedJobDirRequest,
) (OwnedJobDirRemovalValidation, error) {
	panic("read-only preflight called ValidateOwnedJobDirRemoved")
}

func (*readOnlyPreflightTargetFake) OpenOwnedResult(context.Context, TargetResultReadPermit, OpenOwnedResultRequest) (io.ReadCloser, error) {
	panic("read-only preflight called OpenOwnedResult")
}

var _ TargetPort = (*readOnlyPreflightTargetFake)(nil)

type preflightExternalEvidenceFake struct {
	evidence     PreflightExternalEvidence
	issue        bool
	observeCalls int
	requests     []PreflightExternalEvidenceRequest
}

func (fake *preflightExternalEvidenceFake) ObservePreflightEvidence(
	_ context.Context,
	request PreflightExternalEvidenceRequest,
) (PreflightExternalEvidence, error) {
	fake.observeCalls++
	fake.requests = append(fake.requests, request)
	if fake.issue {
		return issuePreflightExternalEvidenceForTest(request, fake.evidence), nil
	}
	return fake.evidence, nil
}

func issuePreflightExternalEvidenceForTest(
	request PreflightExternalEvidenceRequest,
	evidence PreflightExternalEvidence,
) PreflightExternalEvidence {
	evidence.proof = &preflightExternalEvidenceProof{
		requestDigest: preflightExternalEvidenceRequestDigest(request),
	}
	evidence.proof.bindingDigest = preflightExternalEvidenceProofDigest(evidence)
	return evidence
}

type preflightExternalEvidenceAuthorityFake struct {
	observation PreflightExternalEvidenceObservation
	err         error
	calls       int
	requests    []PreflightExternalEvidenceRequest
	onObserve   func()
	respond     func(PreflightExternalEvidenceRequest) (PreflightExternalEvidenceObservation, error)
}

func (fake *preflightExternalEvidenceAuthorityFake) ObserveRecoveryPreflightEvidence(
	_ context.Context,
	request PreflightExternalEvidenceRequest,
) (PreflightExternalEvidenceObservation, error) {
	fake.calls++
	fake.requests = append(fake.requests, request)
	if fake.onObserve != nil {
		fake.onObserve()
	}
	if fake.respond != nil {
		return fake.respond(request)
	}
	return fake.observation, fake.err
}

func TestRecoveryPreflightExternalEvidenceAdapterIssuesOnlyObservedEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	input, targetFacts, externalFacts := validTargetPreflightInput(t, now)
	request := preflightExternalEvidenceRequestForTest(input, targetFacts)
	observation := preflightExternalEvidenceObservationForTest(request, externalFacts)

	if _, err := NewRecoveryPreflightExternalEvidenceAdapter(nil); !errors.Is(err, ErrTargetPreflightUnavailable) {
		t.Fatalf("nil authority error = %v, want ErrTargetPreflightUnavailable", err)
	}
	for _, product := range []reflect.Type{
		reflect.TypeOf(PreflightExternalEvidenceRequest{}),
		reflect.TypeOf(PreflightExternalEvidenceObservation{}),
	} {
		for index := 0; index < product.NumField(); index++ {
			name := product.Field(index).Name
			if strings.Contains(name, "Locator") || strings.Contains(name, "Credential") || strings.Contains(name, "Command") {
				t.Fatalf("external evidence product %s carries private field %q", product.Name(), name)
			}
		}
	}

	authority := &preflightExternalEvidenceAuthorityFake{observation: observation}
	adapter, err := NewRecoveryPreflightExternalEvidenceAdapter(authority)
	if err != nil {
		t.Fatalf("NewRecoveryPreflightExternalEvidenceAdapter() error = %v", err)
	}
	evidence, err := adapter.ObservePreflightEvidence(context.Background(), request)
	if err != nil {
		t.Fatalf("ObservePreflightEvidence() error = %v", err)
	}
	if authority.calls != 1 || len(authority.requests) != 1 || authority.requests[0] != request {
		t.Fatalf("authority calls/requests = %d/%#v, want one exact scalar request", authority.calls, authority.requests)
	}
	if err := evidence.ValidateFor(now, request); err != nil {
		t.Fatalf("production evidence ValidateFor() error = %v", err)
	}
	if evidence.proof == nil || !evidence.proof.production {
		t.Fatal("production adapter did not issue a production-bound private proof")
	}

	bindingSubstitutions := []struct {
		name   string
		mutate func(*PreflightExternalEvidenceObservation)
	}{
		{name: "plan", mutate: func(value *PreflightExternalEvidenceObservation) { value.PlanID = strings.Repeat("8", 32) }},
		{name: "plan binding", mutate: func(value *PreflightExternalEvidenceObservation) { value.PlanBindingDigest = strings.Repeat("8", 64) }},
		{name: "plan revision", mutate: func(value *PreflightExternalEvidenceObservation) { value.PlanTransitionRevision++ }},
		{name: "source revision", mutate: func(value *PreflightExternalEvidenceObservation) {
			value.SourceRevisionDigest = strings.Repeat("8", 64)
		}},
		{name: "capability revision", mutate: func(value *PreflightExternalEvidenceObservation) {
			value.CapabilityRevision = "capability-revision-substituted"
		}},
		{name: "policy revision", mutate: func(value *PreflightExternalEvidenceObservation) {
			value.PolicyRevision = "policy-revision-substituted"
		}},
		{name: "finding set", mutate: func(value *PreflightExternalEvidenceObservation) { value.FindingSetDigest = strings.Repeat("9", 64) }},
		{name: "target root", mutate: func(value *PreflightExternalEvidenceObservation) {
			value.TargetRootRevision = "root-revision-substituted"
		}},
		{name: "target filesystem", mutate: func(value *PreflightExternalEvidenceObservation) {
			value.TargetFilesystemRevision = "filesystem-revision-substituted"
		}},
		{name: "target", mutate: func(value *PreflightExternalEvidenceObservation) {
			value.TargetRevision = "target-revision-substituted"
		}},
		{name: "required bytes", mutate: func(value *PreflightExternalEvidenceObservation) { value.RequiredBytes++ }},
		{name: "required inodes", mutate: func(value *PreflightExternalEvidenceObservation) { value.RequiredInodes++ }},
	}
	for _, testCase := range bindingSubstitutions {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := observation
			testCase.mutate(&candidate)
			candidateAuthority := &preflightExternalEvidenceAuthorityFake{observation: candidate}
			candidateAdapter, candidateErr := NewRecoveryPreflightExternalEvidenceAdapter(candidateAuthority)
			if candidateErr != nil {
				t.Fatalf("constructor error = %v", candidateErr)
			}
			if _, candidateErr = candidateAdapter.ObservePreflightEvidence(context.Background(), request); !errors.Is(candidateErr, ErrRecoveryPreflightConflict) {
				t.Fatalf("substituted observation error = %v, want ErrRecoveryPreflightConflict", candidateErr)
			}
		})
	}

	tamperCases := []struct {
		name   string
		mutate func(*PreflightExternalEvidence)
	}{
		{name: "source access", mutate: func(value *PreflightExternalEvidence) { value.SourceAccessible = !value.SourceAccessible }},
		{name: "finding disposition", mutate: func(value *PreflightExternalEvidence) { value.FindingDisposition = SecurityFindingDispositionBlocked }},
		{name: "xirang overlap", mutate: func(value *PreflightExternalEvidence) { value.OverlapsXirangRoot = !value.OverlapsXirangRoot }},
		{name: "source overlap", mutate: func(value *PreflightExternalEvidence) { value.OverlapsSourceRoot = !value.OverlapsSourceRoot }},
		{name: "reserved bytes", mutate: func(value *PreflightExternalEvidence) { value.ReservedBytes++ }},
		{name: "reserved inodes", mutate: func(value *PreflightExternalEvidence) { value.ReservedInodes++ }},
	}
	for _, testCase := range tamperCases {
		t.Run("tamper "+testCase.name, func(t *testing.T) {
			candidate := evidence
			testCase.mutate(&candidate)
			if err := candidate.ValidateFor(now, request); !errors.Is(err, ErrInvalidTargetPreflight) {
				t.Fatalf("tampered production evidence error = %v, want ErrInvalidTargetPreflight", err)
			}
		})
	}

	testIssued := issuePreflightExternalEvidenceForTest(request, externalFacts)
	if testIssued.proof == nil || testIssued.proof.production {
		t.Fatal("test evidence unexpectedly carries production authority")
	}
	privateFailure := "recognizable-private-provider-repository-failure"
	failingAdapter, err := NewRecoveryPreflightExternalEvidenceAdapter(
		&preflightExternalEvidenceAuthorityFake{observation: observation, err: errors.New(privateFailure)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = failingAdapter.ObservePreflightEvidence(context.Background(), request); !errors.Is(err, ErrTargetPreflightUnavailable) || strings.Contains(err.Error(), privateFailure) {
		t.Fatalf("private authority error = %v, want sanitized unavailable", err)
	}
}

func preflightExternalEvidenceRequestForTest(
	input TargetPreflightInput,
	target TargetRootProbeFacts,
) PreflightExternalEvidenceRequest {
	binding := input.targetPermit.proof.sessionBinding
	return PreflightExternalEvidenceRequest{
		PlanID: binding.planID, PlanBindingDigest: binding.planBindingDigest,
		PlanTransitionRevision: binding.planTransitionRevision,
		SourceRevisionDigest:   input.Frozen.SourceRevisionDigest,
		CapabilityRevision:     input.Frozen.CapabilityRevision, PolicyRevision: input.Frozen.PolicyRevision,
		FindingSetDigest: input.Frozen.FindingSetDigest, TargetRootRevision: target.RootRevision,
		TargetFilesystemRevision: target.FilesystemRevision, TargetRevision: target.TargetRevision,
		RequiredBytes: input.ProbeRequest.RequiredBytes, RequiredInodes: input.ProbeRequest.RequiredInodes,
	}
}

func preflightExternalEvidenceObservationForTest(
	request PreflightExternalEvidenceRequest,
	evidence PreflightExternalEvidence,
) PreflightExternalEvidenceObservation {
	return PreflightExternalEvidenceObservation{
		PlanID: request.PlanID, PlanBindingDigest: request.PlanBindingDigest,
		PlanTransitionRevision: request.PlanTransitionRevision,
		SourceRevisionDigest:   request.SourceRevisionDigest, CapabilityRevision: request.CapabilityRevision,
		PolicyRevision: request.PolicyRevision, FindingSetDigest: request.FindingSetDigest,
		TargetRootRevision:       request.TargetRootRevision,
		TargetFilesystemRevision: request.TargetFilesystemRevision, TargetRevision: request.TargetRevision,
		RequiredBytes: request.RequiredBytes, RequiredInodes: request.RequiredInodes,
		ObservedAt: evidence.ObservedAt, ExpiresAt: evidence.ExpiresAt,
		FindingDisposition: evidence.FindingDisposition, SourceAccessible: evidence.SourceAccessible,
		OverlapsXirangRoot: evidence.OverlapsXirangRoot, OverlapsSourceRoot: evidence.OverlapsSourceRoot,
		ReservedBytes: evidence.ReservedBytes, ReservedInodes: evidence.ReservedInodes,
	}
}

func TestTargetPreflightEvaluatorRequiresIndependentExternalEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	input, targetFacts, externalFacts := validTargetPreflightInput(t, now)
	target := &readOnlyPreflightTargetFake{facts: targetFacts}

	if _, err := NewTargetPreflightEvaluator(target, nil); !errors.Is(err, ErrInvalidTargetPreflight) {
		t.Fatalf("nil external-evidence port error = %v, want ErrInvalidTargetPreflight", err)
	}
	targetType := reflect.TypeOf(TargetRootProbeFacts{})
	for _, forbidden := range []string{
		"SourceRevisionDigest", "CapabilityRevision", "PolicyRevision", "FindingSetDigest",
		"FindingDisposition", "SourceAccessible", "OverlapsXirangRoot", "OverlapsSourceRoot",
		"ReservedBytes", "ReservedInodes",
	} {
		if _, found := targetType.FieldByName(forbidden); found {
			t.Fatalf("target-owned facts can forge external field %q", forbidden)
		}
	}

	unproved := &preflightExternalEvidenceFake{evidence: externalFacts}
	evaluator, err := NewTargetPreflightEvaluator(target, unproved)
	if err != nil {
		t.Fatalf("construct evaluator with unproved fake: %v", err)
	}
	if _, err := evaluator.Evaluate(context.Background(), now, input); !errors.Is(err, ErrInvalidTargetPreflight) {
		t.Fatalf("unproved external evidence error = %v, want ErrInvalidTargetPreflight", err)
	}

	external := &preflightExternalEvidenceFake{evidence: externalFacts, issue: true}
	evaluator, err = NewTargetPreflightEvaluator(target, external)
	if err != nil {
		t.Fatalf("construct evaluator with independent evidence: %v", err)
	}
	result, err := evaluator.Evaluate(context.Background(), now, input)
	if err != nil {
		t.Fatalf("evaluate independently proved evidence: %v", err)
	}
	if !result.Eligible || external.observeCalls != 1 || len(external.requests) != 1 {
		t.Fatalf("independent evidence result=%#v calls=%d requests=%d", result, external.observeCalls, len(external.requests))
	}

	request := external.requests[0]
	proved := issuePreflightExternalEvidenceForTest(request, externalFacts)
	mutations := []struct {
		name   string
		mutate func(*PreflightExternalEvidence)
	}{
		{name: "request proof", mutate: func(value *PreflightExternalEvidence) {
			value.proof.requestDigest = strings.Repeat("9", sha256DigestLength)
		}},
		{name: "result proof", mutate: func(value *PreflightExternalEvidence) {
			value.proof.bindingDigest = strings.Repeat("9", sha256DigestLength)
		}},
		{name: "source revision", mutate: func(value *PreflightExternalEvidence) {
			value.SourceRevisionDigest = strings.Repeat("9", sha256DigestLength)
		}},
		{name: "policy revision", mutate: func(value *PreflightExternalEvidence) {
			value.PolicyRevision = "policy-revision-substituted"
		}},
		{name: "reserve", mutate: func(value *PreflightExternalEvidence) { value.ReservedBytes++ }},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := proved
			proof := *proved.proof
			mutated.proof = &proof
			testCase.mutate(&mutated)
			if err := mutated.ValidateFor(now, request); !errors.Is(err, ErrInvalidTargetPreflight) {
				t.Fatalf("substituted evidence error = %v, want ErrInvalidTargetPreflight", err)
			}
		})
	}
}

func TestPreflightServicePersistsCanonicalEncryptedSnapshotAndCandidate(t *testing.T) {
	fixture := newPreflightPersistenceFixture(t)

	result, err := fixture.service.EvaluateAndPersist(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("EvaluateAndPersist() error = %v", err)
	}
	if !result.Persisted || result.PlanID != fixture.planID || result.PlanTransitionRevision != 2 {
		t.Fatalf("persistence result = %#v", result)
	}
	if fixture.target.probeCalls != 1 {
		t.Fatalf("read-only target probe calls = %d, want 1", fixture.target.probeCalls)
	}

	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.planID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if PlanState(plan.State) != PlanStatePreflightReady || plan.TransitionRevision != 2 {
		t.Fatalf("persisted plan state/revision = %s/%d", plan.State, plan.TransitionRevision)
	}

	var preflight model.BackupAssetRecoveryPreflight
	if err := fixture.db.Where("plan_id = ?", fixture.planID).Take(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	if preflight.EncryptedOperationRows != fixture.encodedOperations ||
		preflight.OperationSetDigest != fixture.operations.OperationSetDigest ||
		preflight.DeleteSetDigest != fixture.operations.DeleteSetDigest ||
		preflight.EstimatedItems != fixture.operations.Impact.EstimatedItems ||
		preflight.EstimatedBytes != fixture.operations.Impact.EstimatedBytes {
		t.Fatalf("persisted operation product = %#v", preflight)
	}
	if fixture.security.OverrideCandidate == nil ||
		preflight.SecurityOverrideCandidateDigest != fixture.security.OverrideCandidate.BindingDigest ||
		preflight.SecurityOverrideCategories != string(SecurityFindingMalware) {
		t.Fatalf("persisted override candidate = %#v", preflight)
	}

	var rawCiphertext string
	if err := fixture.db.Raw(
		"SELECT encrypted_operation_rows FROM backup_asset_recovery_preflights WHERE id = ?",
		preflight.ID,
	).Scan(&rawCiphertext).Error; err != nil {
		t.Fatal(err)
	}
	if rawCiphertext == fixture.encodedOperations || !secure.IsEncrypted(rawCiphertext) ||
		strings.Contains(rawCiphertext, fixture.operations.Rows[0].TargetPathDigest) {
		t.Fatalf("operation snapshot was not encrypted at rest: %q", rawCiphertext)
	}
}

func TestPreflightServiceComposesIndependentEvidenceBeforeLock(t *testing.T) {
	t.Run("production evidence precedes lock and remains bound", func(t *testing.T) {
		fixture := newPreflightPersistenceFixture(t)
		events := make([]string, 0, 3)
		fixture.target.onProbe = func() { events = append(events, "target") }
		fixture.authority.onObserve = func() { events = append(events, "external") }

		callbackName := "task7:r32-preflight-lock-order:" + t.Name()
		lockObserved := false
		if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if _, locking := tx.Statement.Clauses["FOR"]; locking && !lockObserved {
				lockObserved = true
				events = append(events, "lock")
			}
		}); err != nil {
			t.Fatalf("register lock-order callback: %v", err)
		}
		t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callbackName) })

		result, err := fixture.service.EvaluateAndPersist(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("EvaluateAndPersist() error = %v", err)
		}
		if !result.Persisted || !reflect.DeepEqual(events, []string{"target", "external", "lock"}) {
			t.Fatalf("result/events = %#v/%v, want persisted target -> external -> lock", result, events)
		}
		if fixture.authority.calls != 1 || len(fixture.authority.requests) != 1 {
			t.Fatalf("authority calls/requests = %d/%d, want one", fixture.authority.calls, len(fixture.authority.requests))
		}
		var plan model.BackupAssetRecoveryPlan
		if err := fixture.db.Where("id = ?", fixture.planID).Take(&plan).Error; err != nil {
			t.Fatal(err)
		}
		observed := fixture.authority.requests[0]
		if observed.PlanID != plan.ID || observed.PlanBindingDigest != plan.BindingDigest ||
			observed.PlanTransitionRevision+1 != plan.TransitionRevision ||
			observed.SourceRevisionDigest != plan.SourceRevisionDigest ||
			observed.CapabilityRevision != plan.CapabilityRevision ||
			observed.PolicyRevision != plan.SecurityPolicyRevision ||
			observed.FindingSetDigest != plan.SecurityFindingSetDigest {
			t.Fatalf("production evidence request was not bound to committed plan: request=%#v plan=%#v", observed, plan)
		}
	})

	t.Run("test issuer cannot persist", func(t *testing.T) {
		fixture := newPreflightPersistenceFixture(t)
		testIssuer := &preflightExternalEvidenceFake{issue: true, evidence: fixture.externalEvidence}
		evaluator, err := NewTargetPreflightEvaluator(fixture.target, testIssuer)
		if err != nil {
			t.Fatal(err)
		}
		fixture.service.evaluator = evaluator

		_, err = fixture.service.EvaluateAndPersist(context.Background(), fixture.request)
		if !errors.Is(err, ErrInvalidTargetPreflight) {
			t.Fatalf("EvaluateAndPersist(test proof) error = %v, want ErrInvalidTargetPreflight", err)
		}
		assertPreflightPersistenceWriteSet(t, fixture, 0, PlanStateDraft, 1)
	})
}

func TestPreflightServiceRejectsDriftTamperAndRollsBackWrites(t *testing.T) {
	t.Run("operation tamper", func(t *testing.T) {
		fixture := newPreflightPersistenceFixture(t)
		fixture.request.Input.Operations.Rows[0].EstimatedBytes++

		_, err := fixture.service.EvaluateAndPersist(context.Background(), fixture.request)
		if !errors.Is(err, ErrInvalidTargetPreflight) {
			t.Fatalf("EvaluateAndPersist(tampered operations) error = %v", err)
		}
		if fixture.target.probeCalls != 0 {
			t.Fatalf("target probes after local tamper = %d, want 0", fixture.target.probeCalls)
		}
		assertPreflightPersistenceWriteSet(t, fixture, 0, PlanStateDraft, 1)
	})

	t.Run("plan drift after observation", func(t *testing.T) {
		fixture := newPreflightPersistenceFixture(t)
		fixture.target.onProbe = func() {
			if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).
				Where("id = ? AND state = ? AND transition_revision = ?", fixture.planID, PlanStateDraft, 1).
				Updates(map[string]any{"transition_revision": uint64(2), "updated_at": fixture.now.Add(time.Second)}).Error; err != nil {
				t.Fatalf("inject plan drift: %v", err)
			}
		}

		_, err := fixture.service.EvaluateAndPersist(context.Background(), fixture.request)
		if !errors.Is(err, ErrRecoveryPreflightConflict) {
			t.Fatalf("EvaluateAndPersist(plan drift) error = %v", err)
		}
		assertPreflightPersistenceWriteSet(t, fixture, 0, PlanStateDraft, 2)
	})

	t.Run("plan CAS failure rolls back snapshot", func(t *testing.T) {
		fixture := newPreflightPersistenceFixture(t)
		if err := fixture.db.Exec(`CREATE TRIGGER recovery_preflight_test_reject_plan_cas
			BEFORE UPDATE OF state ON backup_asset_recovery_plans
			WHEN NEW.state = 'preflight_ready'
			BEGIN
				SELECT RAISE(ABORT, 'injected recovery preflight CAS failure');
			END`).Error; err != nil {
			t.Fatal(err)
		}

		_, err := fixture.service.EvaluateAndPersist(context.Background(), fixture.request)
		if !errors.Is(err, ErrTargetPreflightUnavailable) {
			t.Fatalf("EvaluateAndPersist(CAS failure) error = %v", err)
		}
		assertPreflightPersistenceWriteSet(t, fixture, 0, PlanStateDraft, 1)
	})
}

type preflightPersistenceFixture struct {
	db                *gorm.DB
	service           *PreflightService
	target            *readOnlyPreflightTargetFake
	authority         *preflightExternalEvidenceAuthorityFake
	request           PreflightPersistenceRequest
	planID            string
	now               time.Time
	operations        RecoveryOperationProducts
	encodedOperations string
	security          PreflightSecurityDecision
	externalEvidence  PreflightExternalEvidence
}

func newPreflightPersistenceFixture(t *testing.T) *preflightPersistenceFixture {
	t.Helper()
	base := newPlanServiceTestFixture(t, false)
	ensureRecoveryPlanRollbackTables(t, base.db)

	operations, encodedOperations := mustAuthorizationReceiptOperationSnapshot(
		t,
		base.request.Selection.AssetRefs,
		nil,
		false,
		base.request.Plan.Binding.Target.RootID,
		base.request.Plan.Binding.Target.RootLocatorDigest,
	)
	security, err := NewPreflightSecurityDecision(PreflightSecurityDecisionInput{
		FindingSetDigest:      strings.Repeat("3", 64),
		PolicyRevision:        "security-policy-revision-1",
		Findings:              []SecurityFinding{{Category: SecurityFindingMalware}},
		OverridableCategories: []SecurityFindingCategory{SecurityFindingMalware},
	})
	if err != nil {
		t.Fatal(err)
	}
	base.request.Plan.Binding.OperationSetDigest = operations.OperationSetDigest
	base.request.Plan.Binding.DeleteSetDigest = operations.DeleteSetDigest
	base.request.Plan.Binding.SecurityDecision = security.Decision
	base.request.EstimatedItems = operations.Impact.EstimatedItems
	base.request.EstimatedBytes = operations.Impact.EstimatedBytes
	created, err := base.service.CreatePlan(context.Background(), base.request)
	if err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}

	var plan model.BackupAssetRecoveryPlan
	if err := base.db.Where("id = ?", created.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	input := TargetPreflightInput{
		SnapshotID:       strings.Repeat("8", 32),
		SnapshotRevision: plan.PreflightRevision,
		SnapshotTTL:      plan.PreflightExpiresAt.Sub(base.now),
		Node: NodeEligibilityFacts{
			NodeID: plan.TargetNodeID, Registered: true, Online: true, Authorized: true,
			CredentialPurpose: TargetPurposePreflight, NodeRevision: plan.TargetBaseRevision,
		},
		Permit: TargetObservationPermit{
			SchemaVersion: 1, NodeID: plan.TargetNodeID, Purpose: TargetPurposePreflight,
			RootID: plan.TargetRootID, RootLocatorDigest: plan.RootLocatorDigest,
			TargetPathDigest: plan.PathDigest, RootRevision: plan.RootRevision,
			ExpiresAt: plan.PreflightExpiresAt,
		},
		ProbeRequest: TargetProbeRequest{
			Object: TargetObjectRef{
				RootID: plan.TargetRootID, RootLocatorDigest: plan.RootLocatorDigest,
				TargetPathDigest: plan.PathDigest, PrivateRelativeLocator: plan.EncryptedTargetRelativePath,
			},
			SourceRevisionDigest: plan.SourceRevisionDigest,
			CapabilityRevision:   plan.CapabilityRevision,
			PolicyRevision:       plan.SecurityPolicyRevision,
			RequiredBytes:        plan.EstimatedBytes,
			RequiredInodes:       plan.EstimatedItems,
		},
		Frozen: FrozenPreflightRevisions{
			NodeRevision: plan.TargetBaseRevision, SourceRevisionDigest: plan.SourceRevisionDigest,
			TargetRevision: plan.TargetBaseRevision, CapabilityRevision: plan.CapabilityRevision,
			PolicyRevision: plan.SecurityPolicyRevision, FindingSetDigest: plan.SecurityFindingSetDigest,
		},
		TargetMode: TargetMode(plan.TargetMode), ConflictPolicy: ConflictPolicy(plan.ConflictPolicy),
		Operations: operations, Security: security,
	}
	target := &readOnlyPreflightTargetFake{facts: TargetRootProbeFacts{
		ObservedAt: base.now, ExpiresAt: plan.PreflightExpiresAt.Add(time.Minute),
		RootRevision: plan.RootRevision, FilesystemRevision: plan.FilesystemRevision,
		TargetRevision: plan.TargetBaseRevision, CredentialRevision: plan.CredentialScopeRevision,
		RequiredToolsAvailable: true, RootReal: true, RootCanonical: true,
		DeviceValid: true, MountValid: true, OwnerValid: true, ModeValid: true,
		FreeBytes: plan.EstimatedBytes + 1_000, FreeInodes: plan.EstimatedItems + 100,
	}}
	externalEvidence := PreflightExternalEvidence{
		ObservedAt: base.now, ExpiresAt: plan.PreflightExpiresAt.Add(time.Minute),
		SourceRevisionDigest: plan.SourceRevisionDigest, CapabilityRevision: plan.CapabilityRevision,
		PolicyRevision: plan.SecurityPolicyRevision, FindingSetDigest: plan.SecurityFindingSetDigest,
		FindingDisposition: SecurityFindingDispositionBlocked, SourceAccessible: true,
	}
	authority := &preflightExternalEvidenceAuthorityFake{
		respond: func(request PreflightExternalEvidenceRequest) (PreflightExternalEvidenceObservation, error) {
			return preflightExternalEvidenceObservationForTest(request, externalEvidence), nil
		},
	}
	external, err := NewRecoveryPreflightExternalEvidenceAdapter(authority)
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := NewTargetPreflightEvaluator(target, external)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPreflightService(PreflightServiceDependencies{
		DB: base.db, Now: func() time.Time { return base.now }, Evaluator: evaluator,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &preflightPersistenceFixture{
		db: base.db, service: service, target: target, authority: authority,
		request: PreflightPersistenceRequest{
			RequesterID: base.request.RequesterID, PlanID: created.PlanID,
			ExpectedPlanRevision: 1, Input: input,
		},
		planID: created.PlanID, now: base.now, operations: operations,
		encodedOperations: encodedOperations, security: security, externalEvidence: externalEvidence,
	}
}

func assertPreflightPersistenceWriteSet(
	t *testing.T,
	fixture *preflightPersistenceFixture,
	wantPreflights int64,
	wantState PlanState,
	wantRevision uint64,
) {
	t.Helper()
	var preflights int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryPreflight{}).
		Where("plan_id = ?", fixture.planID).Count(&preflights).Error; err != nil {
		t.Fatal(err)
	}
	if preflights != wantPreflights {
		t.Fatalf("preflight rows = %d, want %d", preflights, wantPreflights)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.planID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if PlanState(plan.State) != wantState || plan.TransitionRevision != wantRevision {
		t.Fatalf("plan state/revision = %s/%d, want %s/%d", plan.State, plan.TransitionRevision, wantState, wantRevision)
	}
}

func TestTargetPreflightIsReadOnlyAndUsesEligibilityPolicyA(t *testing.T) {
	now := time.Now().UTC()
	input, targetFacts, externalFacts := validTargetPreflightInput(t, now)
	fake := &readOnlyPreflightTargetFake{facts: targetFacts}
	external := &preflightExternalEvidenceFake{evidence: externalFacts, issue: true}
	evaluator, err := NewTargetPreflightEvaluator(fake, external)
	if err != nil {
		t.Fatalf("NewTargetPreflightEvaluator() error = %v", err)
	}

	result, err := evaluator.Evaluate(context.Background(), now, input)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !result.Eligible || result.Preferred || len(result.Reasons) != 0 {
		t.Fatalf("non-producing eligible node result = %#v", result)
	}
	if fake.probeCalls != 1 {
		t.Fatalf("ProbeRoot calls = %d, want 1", fake.probeCalls)
	}
	if result.Snapshot.NodeID != input.Node.NodeID || result.Snapshot.SchemaVersion != 1 ||
		result.Snapshot.RootID != input.ProbeRequest.Object.RootID ||
		result.Snapshot.RootLocatorDigest != input.ProbeRequest.Object.RootLocatorDigest ||
		result.Snapshot.PathDigest != input.ProbeRequest.Object.TargetPathDigest ||
		result.Snapshot.OperationSetDigest != input.Operations.OperationSetDigest ||
		result.Snapshot.DeleteSetDigest != input.Operations.DeleteSetDigest {
		t.Fatalf("snapshot binding incomplete: %#v", result.Snapshot)
	}
	if err := result.Snapshot.ValidateAt(now); err != nil {
		t.Fatalf("snapshot ValidateAt(now) error = %v", err)
	}
	if err := result.Snapshot.ValidateAt(result.Snapshot.ExpiresAt); !errors.Is(err, ErrRecoveryPreflightConflict) {
		t.Fatalf("snapshot expiry equality error = %v, want conflict", err)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, forbidden := range []string{
		input.ProbeRequest.Object.PrivateRelativeLocator,
		input.ProbeRequest.Object.RootLocatorDigest,
		input.ProbeRequest.Object.TargetPathDigest,
		"recognizable-command-output",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("preflight result leaked %q: %s", forbidden, encoded)
		}
	}

	input.Node.ProducingNode = true
	fake.facts = targetFacts
	preferred, err := evaluator.Evaluate(context.Background(), now, input)
	if err != nil || !preferred.Eligible || !preferred.Preferred {
		t.Fatalf("eligible producing node result = %#v error = %v", preferred, err)
	}
}

func TestTargetPreflightRejectsTargetSubstitutionBeforeProbe(t *testing.T) {
	now := time.Now().UTC()
	base, targetFacts, externalFacts := validTargetPreflightInput(t, now)

	tests := []struct {
		name   string
		mutate func(*TargetPreflightInput)
	}{
		{name: "permit root id", mutate: func(input *TargetPreflightInput) { input.Permit.RootID = "root-b" }},
		{name: "permit root locator digest", mutate: func(input *TargetPreflightInput) {
			input.Permit.RootLocatorDigest = strings.Repeat("9", sha256DigestLength)
		}},
		{name: "permit path digest", mutate: func(input *TargetPreflightInput) {
			input.Permit.TargetPathDigest = strings.Repeat("9", sha256DigestLength)
		}},
		{name: "request root id", mutate: func(input *TargetPreflightInput) {
			input.ProbeRequest.Object.RootID = "root-b"
			input.ProbeRequest.Object.TargetPathDigest = mustTargetPathDigest(
				t, input.ProbeRequest.Object.RootID, input.ProbeRequest.Object.RootLocatorDigest,
				input.ProbeRequest.Object.PrivateRelativeLocator,
			)
		}},
		{name: "request root locator digest", mutate: func(input *TargetPreflightInput) {
			input.ProbeRequest.Object.RootLocatorDigest = strings.Repeat("9", sha256DigestLength)
			input.ProbeRequest.Object.TargetPathDigest = mustTargetPathDigest(
				t, input.ProbeRequest.Object.RootID, input.ProbeRequest.Object.RootLocatorDigest,
				input.ProbeRequest.Object.PrivateRelativeLocator,
			)
		}},
		{name: "request relative path", mutate: func(input *TargetPreflightInput) {
			input.ProbeRequest.Object.PrivateRelativeLocator = "recognizable-private-relative-locator-b"
			input.ProbeRequest.Object.TargetPathDigest = mustTargetPathDigest(
				t, input.ProbeRequest.Object.RootID, input.ProbeRequest.Object.RootLocatorDigest,
				input.ProbeRequest.Object.PrivateRelativeLocator,
			)
		}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			input := cloneTargetPreflightInput(base)
			testCase.mutate(&input)
			fake := &readOnlyPreflightTargetFake{facts: targetFacts}
			external := &preflightExternalEvidenceFake{evidence: externalFacts, issue: true}
			evaluator, err := NewTargetPreflightEvaluator(fake, external)
			if err != nil {
				t.Fatalf("NewTargetPreflightEvaluator() error = %v", err)
			}
			_, err = evaluator.Evaluate(context.Background(), now, input)
			if !errors.Is(err, ErrRecoveryPreflightConflict) {
				t.Fatalf("Evaluate() error = %v, want ErrRecoveryPreflightConflict", err)
			}
			if fake.probeCalls != 0 {
				t.Fatalf("ProbeRoot calls = %d, want zero before target-binding rejection", fake.probeCalls)
			}
		})
	}
}

func TestTargetPreflightClosedRejectionMatrix(t *testing.T) {
	now := time.Now().UTC()
	base, baseTargetFacts, baseExternalFacts := validTargetPreflightInput(t, now)

	tests := []struct {
		name   string
		mutate func(*TargetPreflightInput, *TargetRootProbeFacts, *PreflightExternalEvidence)
		want   TargetPreflightReason
	}{
		{name: "unregistered", mutate: func(input *TargetPreflightInput, _ *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			input.Node.Registered = false
		}, want: TargetPreflightNodeUnregistered},
		{name: "archived", mutate: func(input *TargetPreflightInput, _ *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			input.Node.Archived = true
		}, want: TargetPreflightNodeArchived},
		{name: "offline", mutate: func(input *TargetPreflightInput, _ *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			input.Node.Online = false
		}, want: TargetPreflightNodeOffline},
		{name: "unauthorized", mutate: func(input *TargetPreflightInput, _ *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			input.Node.Authorized = false
		}, want: TargetPreflightNodeUnauthorized},
		{name: "wrong credential purpose", mutate: func(input *TargetPreflightInput, _ *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			input.Node.CredentialPurpose = TargetPurposeWrite
		}, want: TargetPreflightCredentialPurpose},
		{name: "missing tool", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.RequiredToolsAvailable = false
		}, want: TargetPreflightToolUnavailable},
		{name: "source inaccessible", mutate: func(_ *TargetPreflightInput, _ *TargetRootProbeFacts, facts *PreflightExternalEvidence) {
			facts.SourceAccessible = false
		}, want: TargetPreflightSourceUnavailable},
		{name: "root not real", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.RootReal = false
		}, want: TargetPreflightRootNotReal},
		{name: "root noncanonical", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.RootCanonical = false
		}, want: TargetPreflightRootNoncanonical},
		{name: "device invalid", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.DeviceValid = false
		}, want: TargetPreflightDeviceInvalid},
		{name: "mount invalid", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.MountValid = false
		}, want: TargetPreflightMountInvalid},
		{name: "owner invalid", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.OwnerValid = false
		}, want: TargetPreflightOwnerInvalid},
		{name: "mode invalid", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.ModeValid = false
		}, want: TargetPreflightModeInvalid},
		{name: "symlink component", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.HasSymlinkComponent = true
		}, want: TargetPreflightSymlinkComponent},
		{name: "xirang overlap", mutate: func(_ *TargetPreflightInput, _ *TargetRootProbeFacts, facts *PreflightExternalEvidence) {
			facts.OverlapsXirangRoot = true
		}, want: TargetPreflightXirangOverlap},
		{name: "source overlap", mutate: func(_ *TargetPreflightInput, _ *TargetRootProbeFacts, facts *PreflightExternalEvidence) {
			facts.OverlapsSourceRoot = true
		}, want: TargetPreflightSourceOverlap},
		{name: "insufficient bytes after reserve", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.FreeBytes = 149
		}, want: TargetPreflightInsufficientBytes},
		{name: "insufficient inodes after reserve", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.FreeInodes = 10
		}, want: TargetPreflightInsufficientInodes},
		{name: "active writer", mutate: func(input *TargetPreflightInput, _ *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			input.Node.ActiveWriter = true
		}, want: TargetPreflightActiveWriter},
		{name: "fail-on-conflict existing target", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.TargetExists = true
		}, want: TargetPreflightTargetConflict},
		{name: "security finding", mutate: func(input *TargetPreflightInput, _ *TargetRootProbeFacts, facts *PreflightExternalEvidence) {
			decision, decisionErr := NewPreflightSecurityDecision(PreflightSecurityDecisionInput{
				FindingSetDigest: input.Frozen.FindingSetDigest, PolicyRevision: input.Frozen.PolicyRevision,
				Findings: []SecurityFinding{{Category: SecurityFindingMalware}},
			})
			if decisionErr != nil {
				panic(decisionErr)
			}
			input.Security = decision
			facts.FindingDisposition = SecurityFindingDispositionBlocked
		}, want: TargetPreflightSecurityBlocked},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			input := cloneTargetPreflightInput(base)
			targetFacts := baseTargetFacts
			externalFacts := baseExternalFacts
			testCase.mutate(&input, &targetFacts, &externalFacts)
			fake := &readOnlyPreflightTargetFake{facts: targetFacts}
			external := &preflightExternalEvidenceFake{evidence: externalFacts, issue: true}
			evaluator, err := NewTargetPreflightEvaluator(fake, external)
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			result, err := evaluator.Evaluate(context.Background(), now, input)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result.Eligible || !containsTargetPreflightReason(result.Reasons, testCase.want) {
				t.Fatalf("result = %#v, want ineligible reason %q", result, testCase.want)
			}
			if result.Preferred {
				t.Fatalf("ineligible node was preferred: %#v", result)
			}
		})
	}
}

func TestTargetPreflightCompositeEvidenceMatrix(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	baseInput, baseTarget, baseExternal := validTargetPreflightInput(t, now)
	type matrixCase struct {
		name              string
		mutateInput       func(*TargetPreflightInput)
		mutateTarget      func(*TargetRootProbeFacts)
		mutateEvidence    func(*PreflightExternalEvidence)
		mutateObservation func(*PreflightExternalEvidenceObservation)
		wantReason        TargetPreflightReason
		wantError         error
	}
	cases := []matrixCase{
		{name: "source inaccessible", mutateEvidence: func(value *PreflightExternalEvidence) {
			value.SourceAccessible = false
		}, wantReason: TargetPreflightSourceUnavailable},
		{name: "capability drift", mutateObservation: func(value *PreflightExternalEvidenceObservation) {
			value.CapabilityRevision = "capability-revision-drift"
		}, wantError: ErrRecoveryPreflightConflict},
		{name: "policy drift", mutateObservation: func(value *PreflightExternalEvidenceObservation) {
			value.PolicyRevision = "policy-revision-drift"
		}, wantError: ErrRecoveryPreflightConflict},
		{name: "finding drift", mutateObservation: func(value *PreflightExternalEvidenceObservation) {
			value.FindingSetDigest = strings.Repeat("f", sha256DigestLength)
		}, wantError: ErrRecoveryPreflightConflict},
		{name: "protected root overlap", mutateEvidence: func(value *PreflightExternalEvidence) {
			value.OverlapsXirangRoot = true
		}, wantReason: TargetPreflightXirangOverlap},
		{name: "source root overlap", mutateEvidence: func(value *PreflightExternalEvidence) {
			value.OverlapsSourceRoot = true
		}, wantReason: TargetPreflightSourceOverlap},
		{name: "reserved bytes", mutateEvidence: func(value *PreflightExternalEvidence) {
			value.ReservedBytes = baseTarget.FreeBytes - baseInput.ProbeRequest.RequiredBytes + 1
		}, wantReason: TargetPreflightInsufficientBytes},
		{name: "reserved inodes", mutateEvidence: func(value *PreflightExternalEvidence) {
			value.ReservedInodes = baseTarget.FreeInodes - baseInput.ProbeRequest.RequiredInodes + 1
		}, wantReason: TargetPreflightInsufficientInodes},
		{name: "target tool fact", mutateTarget: func(value *TargetRootProbeFacts) {
			value.RequiredToolsAvailable = false
		}, wantReason: TargetPreflightToolUnavailable},
		{name: "target canonical fact", mutateTarget: func(value *TargetRootProbeFacts) {
			value.RootCanonical = false
		}, wantReason: TargetPreflightRootNoncanonical},
		{name: "security disposition", mutateInput: func(value *TargetPreflightInput) {
			decision, err := NewPreflightSecurityDecision(PreflightSecurityDecisionInput{
				FindingSetDigest: value.Frozen.FindingSetDigest,
				PolicyRevision:   value.Frozen.PolicyRevision,
				Findings:         []SecurityFinding{{Category: SecurityFindingMalware}},
			})
			if err != nil {
				panic(err)
			}
			value.Security = decision
		}, mutateEvidence: func(value *PreflightExternalEvidence) {
			value.FindingDisposition = SecurityFindingDispositionBlocked
		}, wantReason: TargetPreflightSecurityBlocked},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := cloneTargetPreflightInput(baseInput)
			targetFacts := baseTarget
			externalFacts := baseExternal
			if testCase.mutateInput != nil {
				testCase.mutateInput(&input)
			}
			if testCase.mutateTarget != nil {
				testCase.mutateTarget(&targetFacts)
			}
			if testCase.mutateEvidence != nil {
				testCase.mutateEvidence(&externalFacts)
			}
			target := &readOnlyPreflightTargetFake{facts: targetFacts}
			authority := &preflightExternalEvidenceAuthorityFake{
				respond: func(request PreflightExternalEvidenceRequest) (PreflightExternalEvidenceObservation, error) {
					observation := preflightExternalEvidenceObservationForTest(request, externalFacts)
					if testCase.mutateObservation != nil {
						testCase.mutateObservation(&observation)
					}
					return observation, nil
				},
			}
			adapter, err := NewRecoveryPreflightExternalEvidenceAdapter(authority)
			if err != nil {
				t.Fatal(err)
			}
			evaluator, err := NewTargetPreflightEvaluator(target, adapter)
			if err != nil {
				t.Fatal(err)
			}
			result, err := evaluator.Evaluate(context.Background(), now, input)
			if testCase.wantError != nil {
				if !errors.Is(err, testCase.wantError) {
					t.Fatalf("Evaluate() error = %v, want %v", err, testCase.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result.Eligible || !reflect.DeepEqual(result.Reasons, []TargetPreflightReason{testCase.wantReason}) {
				t.Fatalf("composite result = %#v, want only %q", result, testCase.wantReason)
			}
		})
	}

	t.Run("snapshot intersects observations and preserves bindings", func(t *testing.T) {
		input := cloneTargetPreflightInput(baseInput)
		targetFacts := baseTarget
		externalFacts := baseExternal
		targetFacts.ObservedAt = now.Add(-2 * time.Minute)
		targetFacts.ExpiresAt = now.Add(4 * time.Minute)
		externalFacts.ObservedAt = now.Add(-time.Minute)
		externalFacts.ExpiresAt = now.Add(3 * time.Minute)
		target := &readOnlyPreflightTargetFake{facts: targetFacts}
		authority := &preflightExternalEvidenceAuthorityFake{
			respond: func(request PreflightExternalEvidenceRequest) (PreflightExternalEvidenceObservation, error) {
				return preflightExternalEvidenceObservationForTest(request, externalFacts), nil
			},
		}
		adapter, err := NewRecoveryPreflightExternalEvidenceAdapter(authority)
		if err != nil {
			t.Fatal(err)
		}
		evaluator, err := NewTargetPreflightEvaluator(target, adapter)
		if err != nil {
			t.Fatal(err)
		}
		result, err := evaluator.Evaluate(context.Background(), now, input)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := result.Snapshot
		if !result.Eligible || !snapshot.ObservedAt.Equal(externalFacts.ObservedAt) ||
			!snapshot.ExpiresAt.Equal(externalFacts.ExpiresAt) ||
			snapshot.NodeRevision != input.Frozen.NodeRevision ||
			snapshot.SourceRevisionDigest != input.Frozen.SourceRevisionDigest ||
			snapshot.TargetRevision != targetFacts.TargetRevision ||
			snapshot.RootRevision != targetFacts.RootRevision ||
			snapshot.FilesystemRevision != targetFacts.FilesystemRevision ||
			snapshot.CredentialRevision != targetFacts.CredentialRevision ||
			snapshot.CapabilityRevision != externalFacts.CapabilityRevision ||
			snapshot.PolicyRevision != externalFacts.PolicyRevision ||
			snapshot.FindingSetDigest != externalFacts.FindingSetDigest {
			t.Fatalf("composite snapshot = %#v", snapshot)
		}
	})

	t.Run("persistence admits only security-only rejection", func(t *testing.T) {
		ineligible := newPreflightPersistenceFixture(t)
		ineligible.target.facts.RequiredToolsAvailable = false
		result, err := ineligible.service.EvaluateAndPersist(context.Background(), ineligible.request)
		if err != nil {
			t.Fatalf("persist target-ineligible observation: %v", err)
		}
		if result.Persisted || !reflect.DeepEqual(result.Evaluation.Reasons, []TargetPreflightReason{TargetPreflightToolUnavailable, TargetPreflightSecurityBlocked}) {
			t.Fatalf("target-ineligible persistence result = %#v", result)
		}
		assertPreflightPersistenceWriteSet(t, ineligible, 0, PlanStateDraft, 1)

		securityOnly := newPreflightPersistenceFixture(t)
		result, err = securityOnly.service.EvaluateAndPersist(context.Background(), securityOnly.request)
		if err != nil {
			t.Fatalf("persist security-only observation: %v", err)
		}
		if !result.Persisted || !reflect.DeepEqual(result.Evaluation.Reasons, []TargetPreflightReason{TargetPreflightSecurityBlocked}) {
			t.Fatalf("security-only persistence result = %#v", result)
		}
		assertPreflightPersistenceWriteSet(t, securityOnly, 1, PlanStatePreflightReady, 2)
	})
}

func TestTargetPreflightRejectsBlockedFindingsRewrittenAsRehashedClean(t *testing.T) {
	now := time.Now().UTC()
	input, targetFacts, externalFacts := validTargetPreflightInput(t, now)
	blocked, err := NewPreflightSecurityDecision(PreflightSecurityDecisionInput{
		FindingSetDigest: input.Frozen.FindingSetDigest,
		PolicyRevision:   input.Frozen.PolicyRevision,
		Findings:         []SecurityFinding{{Category: SecurityFindingSuspicious}},
	})
	if err != nil {
		t.Fatalf("NewPreflightSecurityDecision() error = %v", err)
	}
	input.Security = blocked
	externalFacts.FindingDisposition = SecurityFindingDispositionBlocked

	input.Security.Decision.Kind = SecurityDecisionAllowClean
	input.Security.FindingCount = 0
	input.Security.OverrideCandidate = nil
	input.Security.Decision.DecisionDigest = framedDigest(
		securityDecisionDigestDomain,
		string(input.Security.Decision.Kind),
		input.Security.Decision.FindingSetDigest,
		input.Security.Decision.PolicyRevision,
		strconv.Itoa(input.Security.FindingCount),
	)

	fake := &readOnlyPreflightTargetFake{facts: targetFacts}
	external := &preflightExternalEvidenceFake{evidence: externalFacts, issue: true}
	evaluator, err := NewTargetPreflightEvaluator(fake, external)
	if err != nil {
		t.Fatalf("NewTargetPreflightEvaluator() error = %v", err)
	}
	_, err = evaluator.Evaluate(context.Background(), now, input)
	if !errors.Is(err, ErrInvalidTargetPreflight) {
		t.Fatalf("Evaluate(rehashed blocked-to-clean rewrite) error = %v, want ErrInvalidTargetPreflight", err)
	}
	if fake.probeCalls != 1 {
		t.Fatalf("authoritative finding probe calls = %d, want 1", fake.probeCalls)
	}
}

func TestTargetPreflightExpiryAndRevisionDriftFailClosed(t *testing.T) {
	now := time.Now().UTC()
	base, baseTargetFacts, baseExternalFacts := validTargetPreflightInput(t, now)

	tests := []struct {
		name   string
		mutate func(*TargetPreflightInput, *TargetRootProbeFacts, *PreflightExternalEvidence)
	}{
		{name: "target probe expired at equality", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.ExpiresAt = now
		}},
		{name: "external evidence expired at equality", mutate: func(_ *TargetPreflightInput, _ *TargetRootProbeFacts, facts *PreflightExternalEvidence) {
			facts.ExpiresAt = now
		}},
		{name: "permit expired at equality", mutate: func(input *TargetPreflightInput, _ *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			input.Permit.ExpiresAt = now
		}},
		{name: "node revision drift", mutate: func(input *TargetPreflightInput, _ *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			input.Node.NodeRevision = "node-revision-drift"
		}},
		{name: "source revision drift", mutate: func(_ *TargetPreflightInput, _ *TargetRootProbeFacts, facts *PreflightExternalEvidence) {
			facts.SourceRevisionDigest = strings.Repeat("b", sha256DigestLength)
		}},
		{name: "target revision drift", mutate: func(_ *TargetPreflightInput, facts *TargetRootProbeFacts, _ *PreflightExternalEvidence) {
			facts.TargetRevision = "target-revision-drift"
		}},
		{name: "capability revision drift", mutate: func(_ *TargetPreflightInput, _ *TargetRootProbeFacts, facts *PreflightExternalEvidence) {
			facts.CapabilityRevision = "capability-revision-drift"
		}},
		{name: "policy revision drift", mutate: func(_ *TargetPreflightInput, _ *TargetRootProbeFacts, facts *PreflightExternalEvidence) {
			facts.PolicyRevision = "policy-revision-drift"
		}},
		{name: "finding revision drift", mutate: func(_ *TargetPreflightInput, _ *TargetRootProbeFacts, facts *PreflightExternalEvidence) {
			facts.FindingSetDigest = strings.Repeat("c", sha256DigestLength)
		}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			input := cloneTargetPreflightInput(base)
			targetFacts := baseTargetFacts
			externalFacts := baseExternalFacts
			testCase.mutate(&input, &targetFacts, &externalFacts)
			fake := &readOnlyPreflightTargetFake{facts: targetFacts}
			external := &preflightExternalEvidenceFake{evidence: externalFacts, issue: true}
			evaluator, err := NewTargetPreflightEvaluator(fake, external)
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			_, err = evaluator.Evaluate(context.Background(), now, input)
			if !errors.Is(err, ErrRecoveryPreflightConflict) {
				t.Fatalf("Evaluate() error = %v, want ErrRecoveryPreflightConflict", err)
			}
			if fake.probeCalls > 1 {
				t.Fatalf("preflight auto-refreshed after drift: calls=%d", fake.probeCalls)
			}
		})
	}
}

func TestTargetPreflightFixtureEnumeratesClosedMatrix(t *testing.T) {
	encoded, err := os.ReadFile("testdata/target_preflight.json")
	if err != nil {
		t.Fatalf("read target preflight fixture: %v", err)
	}
	var fixture struct {
		SchemaVersion int                     `json:"schema_version"`
		Reasons       []TargetPreflightReason `json:"reasons"`
	}
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatalf("decode target preflight fixture: %v", err)
	}
	want := []TargetPreflightReason{
		TargetPreflightNodeUnregistered, TargetPreflightNodeArchived, TargetPreflightNodeOffline,
		TargetPreflightNodeUnauthorized, TargetPreflightCredentialPurpose, TargetPreflightToolUnavailable,
		TargetPreflightSourceUnavailable, TargetPreflightRootNotReal, TargetPreflightRootNoncanonical,
		TargetPreflightDeviceInvalid, TargetPreflightMountInvalid, TargetPreflightOwnerInvalid,
		TargetPreflightModeInvalid, TargetPreflightSymlinkComponent, TargetPreflightXirangOverlap,
		TargetPreflightSourceOverlap, TargetPreflightInsufficientBytes, TargetPreflightInsufficientInodes,
		TargetPreflightActiveWriter, TargetPreflightTargetConflict, TargetPreflightSecurityBlocked,
	}
	if fixture.SchemaVersion != 1 || len(fixture.Reasons) != len(want) {
		t.Fatalf("fixture = %#v, want schema 1 and %d reasons", fixture, len(want))
	}
	seen := make(map[TargetPreflightReason]struct{}, len(fixture.Reasons))
	for _, reason := range fixture.Reasons {
		if _, duplicate := seen[reason]; duplicate {
			t.Fatalf("duplicate fixture reason %q", reason)
		}
		seen[reason] = struct{}{}
	}
	for _, reason := range want {
		if _, ok := seen[reason]; !ok {
			t.Fatalf("fixture missing closed reason %q", reason)
		}
	}
}

func validTargetPreflightInput(
	t *testing.T,
	now time.Time,
) (TargetPreflightInput, TargetRootProbeFacts, PreflightExternalEvidence) {
	t.Helper()
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	rootLocator := "/srv/FAKE_PREFLIGHT_ROOT_FOR_TEST_ONLY"
	rootLocatorDigest, err := settings.RecoveryTargetRootLocatorDigest(7, "root-a", rootLocator)
	if err != nil {
		t.Fatalf("root locator digest fixture: %v", err)
	}
	relativeLocator := "recognizable-private-relative-locator"
	pathDigest := mustTargetPathDigest(t, "root-a", rootLocatorDigest, relativeLocator)
	pointID := strings.Repeat("1", 32)
	itemLocator := "items/item-0000"
	semanticTargetDigest, err := SemanticTargetDigest(
		TargetModeIsolated, "root-a", rootLocatorDigest, itemLocator,
	)
	if err != nil {
		t.Fatalf("semantic target fixture: %v", err)
	}
	operations, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetModeIsolated, ConflictPolicy: ConflictFailOnConflict,
		Limits: RecoveryOperationLimits{MaxRows: 4, MaxItems: 4, MaxBytes: 1_000, MaxImpactRows: 4},
		Operations: []RecoveryOperation{{
			Kind: RecoveryOperationCreate, TargetPathDigest: digest("2"),
			TargetRelativeLocator: itemLocator, SemanticTargetDigest: semanticTargetDigest,
			ExpectedPrior: ExpectedTargetIdentity{Kind: ExpectedTargetAbsent},
			ExpectedPostIdentityDigest: framedDigest(
				"xirang/recovery/test-expected-post/v1", pointID, digest("3"),
			),
			ExpectedPostBytes: 100, ExpectedPriorBytes: -1,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("3"),
			}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: 100,
		}},
	})
	if err != nil {
		t.Fatalf("operation fixture: %v", err)
	}
	security, err := NewPreflightSecurityDecision(PreflightSecurityDecisionInput{
		FindingSetDigest: digest("4"), PolicyRevision: "policy-revision-1",
	})
	if err != nil {
		t.Fatalf("security fixture: %v", err)
	}
	frozen := FrozenPreflightRevisions{
		NodeRevision: "node-revision-1", SourceRevisionDigest: digest("5"),
		TargetRevision: "target-revision-1", CapabilityRevision: "capability-revision-1",
		PolicyRevision: "policy-revision-1", FindingSetDigest: digest("4"),
	}
	input := TargetPreflightInput{
		SnapshotID: strings.Repeat("6", 32), SnapshotRevision: "snapshot-revision-1", SnapshotTTL: 5 * time.Minute,
		Node: NodeEligibilityFacts{
			NodeID: 7, Registered: true, Online: true, Authorized: true,
			CredentialPurpose: TargetPurposePreflight, NodeRevision: frozen.NodeRevision,
		},
		Permit: TargetObservationPermit{
			SchemaVersion: 1, NodeID: 7, Purpose: TargetPurposePreflight,
			RootID: "root-a", RootLocatorDigest: rootLocatorDigest, TargetPathDigest: pathDigest,
			RootRevision: "root-revision-1", ExpiresAt: now.Add(10 * time.Minute),
		},
		ProbeRequest: TargetProbeRequest{
			Object: TargetObjectRef{
				RootID: "root-a", RootLocatorDigest: rootLocatorDigest, TargetPathDigest: pathDigest,
				PrivateRelativeLocator: relativeLocator,
			},
			SourceRevisionDigest: frozen.SourceRevisionDigest,
			CapabilityRevision:   frozen.CapabilityRevision, PolicyRevision: frozen.PolicyRevision,
			RequiredBytes: 100, RequiredInodes: 1,
		},
		Frozen: frozen, TargetMode: TargetModeIsolated, ConflictPolicy: ConflictFailOnConflict,
		Operations: operations, Security: security,
	}
	binding := recoveryTargetPreflightSessionBinding{
		planID: strings.Repeat("7", 32), planBindingDigest: digest("7"),
		planTransitionRevision: 1, targetMode: TargetModeIsolated,
		nodeID: 7, nodeRevision: frozen.NodeRevision, credentialRevision: "credential-revision-1",
		rootID: "root-a", rootLocator: rootLocator, rootLocatorDigest: rootLocatorDigest,
		rootRevision: "root-revision-1", filesystemRevision: "filesystem-revision-1",
		targetPathDigest: pathDigest, privateRelativeLocator: relativeLocator,
		targetRevision: frozen.TargetRevision, preflightRevision: input.SnapshotRevision,
	}
	binding.bindingDigest = binding.digest()
	input.targetPermit = issueTargetPreflightPermit(input.Permit, binding, input.ProbeRequest)
	if err := input.targetPermit.ValidateRequestAt(now, input.Permit, input.ProbeRequest); err != nil {
		t.Fatalf("sealed target preflight fixture: %v", err)
	}
	targetFacts := TargetRootProbeFacts{
		ObservedAt: now, ExpiresAt: now.Add(10 * time.Minute),
		RootRevision: "root-revision-1", FilesystemRevision: "filesystem-revision-1",
		TargetRevision: frozen.TargetRevision, CredentialRevision: "credential-revision-1",
		RequiredToolsAvailable: true, RootReal: true, RootCanonical: true,
		DeviceValid: true, MountValid: true, OwnerValid: true, ModeValid: true,
		FreeBytes: 1_000, FreeInodes: 100,
	}
	externalFacts := PreflightExternalEvidence{
		ObservedAt: now, ExpiresAt: now.Add(10 * time.Minute),
		SourceRevisionDigest: frozen.SourceRevisionDigest, CapabilityRevision: frozen.CapabilityRevision,
		PolicyRevision: frozen.PolicyRevision, FindingSetDigest: frozen.FindingSetDigest,
		FindingDisposition: SecurityFindingDispositionClean, SourceAccessible: true,
		ReservedBytes: 50, ReservedInodes: 10,
	}
	return input, targetFacts, externalFacts
}

func cloneTargetPreflightInput(input TargetPreflightInput) TargetPreflightInput {
	clone := input
	clone.Operations.Rows = make([]RecoveryOperation, len(input.Operations.Rows))
	for index, operation := range input.Operations.Rows {
		clone.Operations.Rows[index] = cloneRecoveryOperation(operation)
	}
	clone.Operations.Impact.Rows = append([]RecoveryImpactRow(nil), input.Operations.Impact.Rows...)
	if input.Security.OverrideCandidate != nil {
		candidate := *input.Security.OverrideCandidate
		candidate.Categories = append([]SecurityFindingCategory(nil), input.Security.OverrideCandidate.Categories...)
		clone.Security.OverrideCandidate = &candidate
	}
	return clone
}

func containsTargetPreflightReason(reasons []TargetPreflightReason, want TargetPreflightReason) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}
