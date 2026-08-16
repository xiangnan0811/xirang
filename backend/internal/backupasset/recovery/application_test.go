package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
)

func TestProductionApplicationMaterializerBuildsAllTargetModesFromFrozenEvidence(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	now := fixture.now
	originalSelection := fixture.request.Selection
	selectionAuthorityFixture, err := NewSourceValidator(fixture.service.db)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := selectionAuthorityFixture.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: originalSelection.RepositoryID, RecoveryPointID: originalSelection.RecoveryPointID,
		CatalogGenerationID: originalSelection.CatalogGenerationID,
		AssetRefs:           []backupasset.AssetRef{originalSelection.AssetRefs[0]}, MaxItems: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	rootDigest := fixture.resolver.resolution.LocatorDigest
	item := RecoveryPlanSourceItemEvidence{
		AssetRef: selection.AssetRefs[0], TargetRelativeLocator: "docs/report.txt",
		ContentDigest: strings.Repeat("a", 64), Bytes: 3,
		DisplayClass: RecoveryDisplayClassRegular,
	}
	securityDecision, err := NewPreflightSecurityDecision(PreflightSecurityDecisionInput{
		FindingSetDigest: strings.Repeat("b", 64), PolicyRevision: "security-policy-revision-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		mode      TargetMode
		policy    ConflictPolicy
		operation RecoveryOperationKind
		deletes   int
	}{
		{name: "isolated create", mode: TargetModeIsolated, policy: ConflictFailOnConflict, operation: RecoveryOperationCreate},
		{name: "in-place overwrite", mode: TargetModeInPlace, policy: ConflictOverwriteSelected, operation: RecoveryOperationOverwrite},
		{name: "in-place skip", mode: TargetModeInPlace, policy: ConflictSkipExisting, operation: RecoveryOperationSkip},
		{name: "in-place exact mirror", mode: TargetModeInPlace, policy: ConflictExactMirror, operation: RecoveryOperationOverwrite, deletes: 1},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			// Production clocks advance while Processing reads its current
			// evidence. The materializer must validate the evidence against a
			// post-observation clock sample, not the sample taken before I/O.
			observedAt := now.Add(time.Nanosecond)
			nowCalls := 0
			selectionAuthority := &recoveryApplicationSelectionFreezerFake{selection: selection}
			securityAuthority := &recoveryPlanSecurityAuthorityFake{evidence: RecoveryPlanSecurityEvidence{
				SelectionDigest: selection.SelectionDigest, Provider: backupasset.ProviderRsync,
				CapabilityRevision: "capability-revision-1", Security: securityDecision,
				Items: []RecoveryPlanSourceItemEvidence{item}, ObservedAt: observedAt,
			}}
			targetRelative := item.TargetRelativeLocator
			pathDigest := mustTargetPathDigest(t, "recovery-root", rootDigest, targetRelative)
			semanticDigest, digestErr := SemanticTargetDigest(testCase.mode, "recovery-root", rootDigest, targetRelative)
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			prior := ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}
			priorBytes := int64(-1)
			postBytes := item.Bytes
			postDigest := item.ContentDigest
			if testCase.operation != RecoveryOperationCreate {
				prior = ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: strings.Repeat("c", 64)}
				priorBytes = 7
			}
			if testCase.operation == RecoveryOperationSkip {
				postDigest = prior.Digest
				postBytes = -1
			}
			ref := item.AssetRef
			operations := []RecoveryOperation{{
				Kind: testCase.operation, TargetPathDigest: pathDigest,
				TargetRelativeLocator: targetRelative, SemanticTargetDigest: semanticDigest,
				ExpectedPrior: prior, ExpectedPostIdentityDigest: postDigest,
				ExpectedPostBytes: postBytes, ExpectedPriorBytes: priorBytes,
				Source:       RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &ref},
				DisplayClass: item.DisplayClass, EstimatedBytes: item.Bytes,
			}}
			if testCase.deletes > 0 {
				deleteLocator := "obsolete.txt"
				deletePath := mustTargetPathDigest(t, "recovery-root", rootDigest, deleteLocator)
				deleteSemantic, semanticErr := SemanticTargetDigest(testCase.mode, "recovery-root", rootDigest, deleteLocator)
				if semanticErr != nil {
					t.Fatal(semanticErr)
				}
				operations = append(operations, RecoveryOperation{
					Kind: RecoveryOperationDelete, TargetPathDigest: deletePath,
					TargetRelativeLocator: deleteLocator, SemanticTargetDigest: deleteSemantic,
					ExpectedPrior:     ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: strings.Repeat("d", 64)},
					ExpectedPostBytes: -1, ExpectedPriorBytes: -1,
					Source:       RecoveryOperationSource{Kind: RecoveryOperationSourceNone},
					DisplayClass: RecoveryDisplayClassRegular,
				})
			}
			products, productErr := NewOperationProducts(RecoveryOperationProductsInput{
				TargetMode: testCase.mode, ConflictPolicy: testCase.policy, Operations: operations,
				Limits: RecoveryOperationLimits{MaxRows: 4, MaxItems: 4, MaxBytes: 10, MaxImpactRows: 4},
			})
			if productErr != nil {
				t.Fatal(productErr)
			}
			targetAuthority := &recoveryPlanTargetEnumerationFake{result: RecoveryPlanTargetEnumeration{
				Target: TargetBinding{
					Mode: testCase.mode, NodeID: 27, RootID: "recovery-root",
					EncryptedRelativePath: targetRelative, RootLocatorDigest: rootDigest,
					PathDigest: pathDigest, BaseNodeRevision: "node-revision-1",
					CredentialScopeRevision: "credential-revision-1", RootRevision: "root-revision-1",
					FilesystemRevision: "filesystem-revision-1",
				},
				Operations: products,
			}}
			materializer, constructorErr := NewProductionApplicationMaterializer(
				ProductionApplicationMaterializerDependencies{
					Selections: selectionAuthority, Plans: &recoveryApplicationPlanAuthorityFake{},
					Security: securityAuthority, Targets: targetAuthority,
					Policy: RecoveryApplicationMaterializationPolicy{
						MaxSelectionItems: 4, MaxLogicalBytes: 10, MaxTargetRows: 4,
						MaxTargetBytes: 10, ObservationTimeout: time.Second, PreflightTTL: time.Hour,
					},
					Now: func() time.Time {
						nowCalls++
						if nowCalls == 1 {
							return now
						}
						return observedAt.Add(time.Nanosecond)
					},
					NewRevision: func() (string, error) { return "preflight-revision-1", nil },
				},
			)
			if constructorErr != nil {
				t.Fatal(constructorErr)
			}
			intent := CreatePlanIntentRequest{
				RequesterID: 31, Endpoint: "/api/v1/recovery-plans",
				IdempotencyKey: "recovery-materializer-key-" + strings.ReplaceAll(testCase.name, " ", "-"),
				RepositoryID:   selection.RepositoryID, RecoveryPointID: selection.RecoveryPointID,
				CatalogGenerationID: selection.CatalogGenerationID, EntryIDs: []string{selection.AssetRefs[0].EntryID},
				TargetMode: testCase.mode, TargetNodeID: 27, TargetRootID: "recovery-root", ConflictPolicy: testCase.policy,
			}
			if validationErr := validateRecoveryPlanSecurityEvidence(selection, securityAuthority.evidence, observedAt, materializer.policy); validationErr != nil {
				t.Fatalf("security fixture invalid: %v", validationErr)
			}
			if _, validationErr := validateRecoveryPlanTargetEnumeration(intent, selection, securityAuthority.evidence, targetAuthority.result, materializer.policy); validationErr != nil {
				t.Fatalf("target fixture invalid: %v", validationErr)
			}
			request, materializeErr := materializer.MaterializeCreatePlan(context.Background(), intent)
			if materializeErr != nil {
				t.Fatalf("MaterializeCreatePlan() error = %v", materializeErr)
			}
			if selectionAuthority.calls != 1 || securityAuthority.calls != 1 || targetAuthority.calls != 1 ||
				request.Selection.SelectionDigest != selection.SelectionDigest || request.Plan.Binding.Target != targetAuthority.result.Target ||
				request.Plan.Binding.OperationSetDigest != products.OperationSetDigest ||
				request.Plan.Binding.DeleteSetDigest != products.DeleteSetDigest ||
				request.Plan.Binding.SecurityDecision != securityDecision.Decision ||
				request.Plan.Binding.PreflightRevision != "preflight-revision-1" ||
				request.EstimatedItems != products.Impact.EstimatedItems || request.EstimatedBytes != products.Impact.EstimatedBytes {
				t.Fatalf("materialized request = %+v; calls selection/security/target=%d/%d/%d", request,
					selectionAuthority.calls, securityAuthority.calls, targetAuthority.calls)
			}
			if got := targetAuthority.request; got.TargetMode != testCase.mode || got.ConflictPolicy != testCase.policy ||
				got.TargetNodeID != intent.TargetNodeID || got.TargetRootID != intent.TargetRootID ||
				got.SelectionDigest != selection.SelectionDigest || len(got.Items) != 1 {
				t.Fatalf("target enumeration request = %+v", got)
			}
			created, persistErr := fixture.service.CreatePlan(context.Background(), request)
			if persistErr != nil || !validOpaqueID(created.PlanID) || created.State != PlanStateDraft {
				t.Fatalf("persist materialized public intent result=%#v error=%v", created, persistErr)
			}
		})
	}
}

func TestRecoveryApplicationServiceMaterializesOnlyPrivatePlanAndPreflightProducts(t *testing.T) {
	intent := CreatePlanIntentRequest{
		RequesterID: 31, Endpoint: "/api/v1/recovery-plans", IdempotencyKey: "recovery-plan-application-key",
		RepositoryID: strings.Repeat("a", 32), RecoveryPointID: strings.Repeat("b", 32),
		CatalogGenerationID: strings.Repeat("c", 32), EntryIDs: []string{strings.Repeat("d", 64)},
		TargetMode: TargetModeIsolated, TargetNodeID: 9, TargetRootID: "recovery-root",
		ConflictPolicy: ConflictFailOnConflict,
	}
	preflightRequest := RecoveryPreflightRequest{
		RequesterID: 31, PlanID: strings.Repeat("e", 32), ExpectedPlanRevision: 1,
	}
	privatePlan := CreatePlanRequest{
		RequesterID: intent.RequesterID, Endpoint: intent.Endpoint, IdempotencyKey: intent.IdempotencyKey,
		Selection: ExactSelection{
			RepositoryID: intent.RepositoryID, RecoveryPointID: intent.RecoveryPointID,
			CatalogGenerationID: intent.CatalogGenerationID,
		},
		Plan: RecoveryPlan{Binding: PlanBinding{
			Target: TargetBinding{
				Mode: intent.TargetMode, NodeID: intent.TargetNodeID, RootID: intent.TargetRootID,
			},
			ConflictPolicy: intent.ConflictPolicy,
		}},
	}
	privatePreflight := PreflightPersistenceRequest{
		RequesterID: preflightRequest.RequesterID, PlanID: preflightRequest.PlanID,
		ExpectedPlanRevision: preflightRequest.ExpectedPlanRevision,
		Input:                TargetPreflightInput{SnapshotRevision: "FAKE_PRIVATE_PREFLIGHT_REVISION_FOR_TEST_ONLY"},
	}
	materializer := &recoveryApplicationMaterializerFake{plan: privatePlan, preflight: privatePreflight}
	plans := &recoveryApplicationPlanOwnerFake{result: CreatePlanResult{PlanID: strings.Repeat("f", 32), State: PlanStateDraft}}
	preflights := &recoveryApplicationPreflightOwnerFake{result: PreflightPersistenceResult{
		PlanID: preflightRequest.PlanID, Persisted: true, PlanTransitionRevision: 2,
		Evaluation: TargetPreflightResult{
			Eligible: true, Reasons: []TargetPreflightReason{},
			Snapshot: TargetPreflightSnapshot{
				ID: strings.Repeat("1", 32), Revision: "preflight-revision-1",
				TargetMode: TargetModeIsolated, ConflictPolicy: ConflictFailOnConflict,
				OperationSetDigest: strings.Repeat("2", 64), DeleteSetDigest: strings.Repeat("3", 64),
				ObservedAt: time.Date(2026, 8, 16, 13, 0, 0, 0, time.UTC),
				ExpiresAt:  time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC),
			},
			Security: PreflightSecurityDecision{Decision: SecurityDecision{Kind: SecurityDecisionAllowClean}},
		},
	}}
	service, err := NewApplicationService(RecoveryApplicationServiceDependencies{
		Materializer: materializer, Plans: plans, Preflights: preflights,
	})
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.CreatePlan(context.Background(), intent)
	if err != nil || created.PlanID != plans.result.PlanID || materializer.planCalls != 1 ||
		plans.calls != 1 || plans.request.RequesterID != intent.RequesterID {
		t.Fatalf("CreatePlan() result=%+v error=%v materializer=%d owner=%d request=%+v",
			created, err, materializer.planCalls, plans.calls, plans.request)
	}
	preflight, err := service.Preflight(context.Background(), preflightRequest)
	if err != nil || !preflight.Persisted || materializer.preflightCalls != 1 ||
		preflights.calls != 1 || preflights.request.Input.SnapshotRevision != privatePreflight.Input.SnapshotRevision {
		t.Fatalf("Preflight() result=%+v error=%v materializer=%d owner=%d request=%+v",
			preflight, err, materializer.preflightCalls, preflights.calls, preflights.request)
	}
}

func TestProductionApplicationMaterializerReconstructsOwnedPreflightFromSameSealedProducts(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	now := fixture.now
	original := fixture.request.Selection
	validator, err := NewSourceValidator(fixture.service.db)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: original.RepositoryID, RecoveryPointID: original.RecoveryPointID,
		CatalogGenerationID: original.CatalogGenerationID,
		AssetRefs:           []backupasset.AssetRef{original.AssetRefs[0]}, MaxItems: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	item := RecoveryPlanSourceItemEvidence{
		AssetRef: selection.AssetRefs[0], TargetRelativeLocator: "docs/report.txt",
		ContentDigest: strings.Repeat("a", 64), Bytes: 3, DisplayClass: RecoveryDisplayClassRegular,
	}
	security, err := NewPreflightSecurityDecision(PreflightSecurityDecisionInput{
		FindingSetDigest: strings.Repeat("b", 64), PolicyRevision: "security-policy-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	rootDigest := fixture.resolver.resolution.LocatorDigest
	pathDigest := mustTargetPathDigest(t, "recovery-root", rootDigest, item.TargetRelativeLocator)
	semantic, err := SemanticTargetDigest(TargetModeInPlace, "recovery-root", rootDigest, item.TargetRelativeLocator)
	if err != nil {
		t.Fatal(err)
	}
	ref := item.AssetRef
	products, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetModeInPlace, ConflictPolicy: ConflictOverwriteSelected,
		Operations: []RecoveryOperation{{
			Kind: RecoveryOperationOverwrite, TargetPathDigest: pathDigest,
			TargetRelativeLocator: item.TargetRelativeLocator, SemanticTargetDigest: semantic,
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: strings.Repeat("c", 64)},
			ExpectedPostIdentityDigest: item.ContentDigest, ExpectedPostBytes: item.Bytes, ExpectedPriorBytes: 3,
			Source:       RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &ref},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: item.Bytes,
		}},
		Limits: RecoveryOperationLimits{MaxRows: 4, MaxItems: 4, MaxBytes: 16, MaxImpactRows: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := TargetBinding{
		Mode: TargetModeInPlace, NodeID: 27, RootID: "recovery-root",
		EncryptedRelativePath: item.TargetRelativeLocator, RootLocatorDigest: rootDigest, PathDigest: pathDigest,
		BaseNodeRevision: "node-v1", CredentialScopeRevision: "credential-v1",
		RootRevision: "sftpr1:root-v1", FilesystemRevision: "sftpf1:filesystem-v1",
	}
	planID := strings.Repeat("d", 32)
	planAuthority := &recoveryApplicationPlanAuthorityFake{snapshot: RecoveryApplicationPlanSnapshot{
		PlanID: planID, RequesterID: 31, TransitionRevision: 1, BindingDigest: strings.Repeat("e", 64),
		Selection: selection, Target: target, ConflictPolicy: ConflictOverwriteSelected,
		OperationSetDigest: products.OperationSetDigest, DeleteSetDigest: products.DeleteSetDigest,
		CapabilityRevision: "capability-v1", SecurityDecision: security.Decision,
		PreflightRevision: "preflight-v1", PreflightExpiresAt: now.Add(time.Hour),
		EstimatedItems: products.Impact.EstimatedItems, EstimatedBytes: products.Impact.EstimatedBytes,
	}}
	securityAuthority := &recoveryPlanSecurityAuthorityFake{evidence: RecoveryPlanSecurityEvidence{
		SelectionDigest: selection.SelectionDigest, Provider: backupasset.ProviderRsync,
		CapabilityRevision: "capability-v1", Security: security,
		Items: []RecoveryPlanSourceItemEvidence{item}, ObservedAt: now,
	}}
	targetAuthority := &recoveryPlanTargetEnumerationFake{result: RecoveryPlanTargetEnumeration{
		Target: target, TargetRevision: "sftpt1:target-v1",
		Node:       RecoveryPlanTargetNodeEvidence{Registered: true, Online: true, Authorized: true},
		Operations: products,
	}}
	materializer, err := NewProductionApplicationMaterializer(ProductionApplicationMaterializerDependencies{
		Selections: &recoveryApplicationSelectionFreezerFake{}, Plans: planAuthority,
		Security: securityAuthority, Targets: targetAuthority,
		Policy: RecoveryApplicationMaterializationPolicy{
			MaxSelectionItems: 4, MaxLogicalBytes: 16, MaxTargetRows: 4, MaxTargetBytes: 16,
			ObservationTimeout: time.Second, PreflightTTL: time.Hour,
		},
		Now: func() time.Time { return now }, NewID: func() (string, error) { return strings.Repeat("f", 32), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := RecoveryPreflightRequest{RequesterID: 31, PlanID: planID, ExpectedPlanRevision: 1}
	private, err := materializer.MaterializePreflight(context.Background(), request)
	if err != nil {
		t.Fatalf("MaterializePreflight() error=%v", err)
	}
	if planAuthority.calls != 1 || securityAuthority.calls != 1 || targetAuthority.calls != 1 ||
		private.RequesterID != request.RequesterID || private.PlanID != planID ||
		private.Input.SnapshotID != strings.Repeat("f", 32) || private.Input.SnapshotRevision != "preflight-v1" ||
		private.Input.Frozen.TargetRevision != targetAuthority.result.TargetRevision ||
		private.Input.Operations.OperationSetDigest != products.OperationSetDigest ||
		private.Input.Permit.Purpose != TargetPurposePreflight ||
		private.Input.ProbeRequest.Object.PrivateRelativeLocator != item.TargetRelativeLocator {
		t.Fatalf("private preflight=%#v calls plan/security/target=%d/%d/%d", private,
			planAuthority.calls, securityAuthority.calls, targetAuthority.calls)
	}

	targetAuthority.result.Operations.OperationSetDigest = strings.Repeat("0", 64)
	if _, err := materializer.MaterializePreflight(context.Background(), request); !errors.Is(err, ErrRecoveryTargetChanged) {
		t.Fatalf("operation drift error=%v, want ErrRecoveryTargetChanged", err)
	}
	planAuthority.err = ErrRecoveryAPIObjectNotFound
	if _, err := materializer.MaterializePreflight(context.Background(), request); !errors.Is(err, ErrRecoveryAPIObjectNotFound) {
		t.Fatalf("foreign plan error=%v, want hidden not found", err)
	}
}

func TestProductionApplicationMaterializerRebuildsOwnedPreflightFromPersistedPlan(t *testing.T) {
	fixture := newPreflightPersistenceFixture(t)
	planAuthority, err := NewRecoveryApplicationPlanAuthority(fixture.db)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := planAuthority.LoadRecoveryApplicationPlan(context.Background(), RecoveryApplicationPlanRequest{
		RequesterID: fixture.request.RequesterID, PlanID: fixture.planID,
		ExpectedRevision: fixture.request.ExpectedPlanRevision, ObservedAt: fixture.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	items := make([]RecoveryPlanSourceItemEvidence, 0, len(snapshot.Selection.AssetRefs))
	for _, operation := range fixture.operations.Rows {
		if operation.Source.AssetRef == nil {
			continue
		}
		items = append(items, RecoveryPlanSourceItemEvidence{
			AssetRef: *operation.Source.AssetRef, TargetRelativeLocator: operation.TargetRelativeLocator,
			ContentDigest: operation.ExpectedPostIdentityDigest, Bytes: operation.EstimatedBytes,
			DisplayClass: operation.DisplayClass,
		})
	}
	security := &recoveryPlanSecurityAuthorityFake{evidence: RecoveryPlanSecurityEvidence{
		SelectionDigest: snapshot.Selection.SelectionDigest, Provider: backupasset.ProviderRsync,
		CapabilityRevision: snapshot.CapabilityRevision, Security: fixture.security,
		Items: items, ObservedAt: fixture.now,
	}}
	targetRevision := "sftpt1:persisted-plan-target-v1"
	targets := &recoveryPlanTargetEnumerationFake{result: RecoveryPlanTargetEnumeration{
		Target: snapshot.Target, TargetRevision: targetRevision,
		Node:       RecoveryPlanTargetNodeEvidence{Registered: true, Online: true, Authorized: true},
		Operations: fixture.operations,
	}}
	materializer, err := NewProductionApplicationMaterializer(ProductionApplicationMaterializerDependencies{
		Selections: &recoveryApplicationSelectionFreezerFake{}, Plans: planAuthority,
		Security: security, Targets: targets,
		Policy: RecoveryApplicationMaterializationPolicy{
			MaxSelectionItems: 100, MaxLogicalBytes: 1 << 20, MaxTargetRows: 100,
			MaxTargetBytes: 1 << 20, ObservationTimeout: time.Second, PreflightTTL: time.Hour,
		},
		Now:   func() time.Time { return fixture.now },
		NewID: func() (string, error) { return strings.Repeat("9", 32), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	private, err := materializer.MaterializePreflight(context.Background(), RecoveryPreflightRequest{
		RequesterID: fixture.request.RequesterID, PlanID: fixture.planID,
		ExpectedPlanRevision: fixture.request.ExpectedPlanRevision,
	})
	if err != nil {
		t.Fatalf("MaterializePreflight(persisted plan) error=%v", err)
	}
	if private.Input.Frozen.TargetRevision != targetRevision ||
		private.Input.Operations.OperationSetDigest != snapshot.OperationSetDigest ||
		private.Input.Security.Decision != snapshot.SecurityDecision ||
		private.Input.ProbeRequest.Object.PrivateRelativeLocator != snapshot.Target.EncryptedRelativePath {
		t.Fatalf("persisted private preflight=%#v", private)
	}
	fixture.target.facts.TargetRevision = targetRevision
	result, err := fixture.service.EvaluateAndPersist(context.Background(), private)
	if err != nil || !result.Persisted || result.Evaluation.Snapshot.TargetRevision != targetRevision {
		t.Fatalf("EvaluateAndPersist(materialized) result=%#v error=%v", result, err)
	}

	if _, err := materializer.MaterializePreflight(context.Background(), RecoveryPreflightRequest{
		RequesterID: fixture.request.RequesterID + 1, PlanID: fixture.planID,
		ExpectedPlanRevision: fixture.request.ExpectedPlanRevision,
	}); !errors.Is(err, ErrRecoveryAPIObjectNotFound) {
		t.Fatalf("foreign persisted plan error=%v, want hidden not found", err)
	}
}

func TestRecoveryApplicationServiceFailsClosedWithoutConcreteMaterializationAuthority(t *testing.T) {
	service, err := NewApplicationService(RecoveryApplicationServiceDependencies{
		Materializer: UnavailableApplicationMaterializer{},
		Plans:        &recoveryApplicationPlanOwnerFake{},
		Preflights:   &recoveryApplicationPreflightOwnerFake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	intent := CreatePlanIntentRequest{
		RequesterID: 31, Endpoint: "/api/v1/recovery-plans", IdempotencyKey: "recovery-plan-application-key",
		RepositoryID: strings.Repeat("a", 32), RecoveryPointID: strings.Repeat("b", 32),
		CatalogGenerationID: strings.Repeat("c", 32), EntryIDs: []string{strings.Repeat("d", 64)},
		TargetMode: TargetModeInPlace, TargetNodeID: 9, TargetRootID: "recovery-root",
		ConflictPolicy: ConflictOverwriteSelected,
	}
	if _, err := service.CreatePlan(context.Background(), intent); !errors.Is(err, ErrRecoveryPlanUnavailable) {
		t.Fatalf("CreatePlan() error=%v, want unavailable without pre-create target/security authority", err)
	}
	if _, err := service.Preflight(context.Background(), RecoveryPreflightRequest{
		RequesterID: 31, PlanID: strings.Repeat("e", 32), ExpectedPlanRevision: 1,
	}); !errors.Is(err, ErrTargetPreflightUnavailable) {
		t.Fatalf("Preflight() error=%v, want unavailable without private input reconstruction", err)
	}
}

type recoveryApplicationMaterializerFake struct {
	plan           CreatePlanRequest
	preflight      PreflightPersistenceRequest
	planCalls      int
	preflightCalls int
}

type recoveryApplicationSelectionFreezerFake struct {
	selection ExactSelection
	err       error
	request   SourceSelectionRequest
	calls     int
}

func (fake *recoveryApplicationSelectionFreezerFake) FreezeSelection(
	_ context.Context,
	request SourceSelectionRequest,
) (ExactSelection, error) {
	fake.calls++
	fake.request = request
	return fake.selection, fake.err
}

type recoveryPlanSecurityAuthorityFake struct {
	evidence RecoveryPlanSecurityEvidence
	err      error
	request  RecoveryPlanSecurityRequest
	calls    int
}

func (fake *recoveryPlanSecurityAuthorityFake) ObserveRecoveryPlanSecurity(
	_ context.Context,
	request RecoveryPlanSecurityRequest,
) (RecoveryPlanSecurityEvidence, error) {
	fake.calls++
	fake.request = request
	return fake.evidence, fake.err
}

type recoveryPlanTargetEnumerationFake struct {
	result  RecoveryPlanTargetEnumeration
	err     error
	request RecoveryPlanTargetEnumerationRequest
	calls   int
}

type recoveryApplicationPlanAuthorityFake struct {
	snapshot RecoveryApplicationPlanSnapshot
	err      error
	request  RecoveryApplicationPlanRequest
	calls    int
}

func (fake *recoveryApplicationPlanAuthorityFake) LoadRecoveryApplicationPlan(
	_ context.Context,
	request RecoveryApplicationPlanRequest,
) (RecoveryApplicationPlanSnapshot, error) {
	fake.calls++
	fake.request = request
	return fake.snapshot, fake.err
}

func (fake *recoveryPlanTargetEnumerationFake) EnumerateRecoveryPlanTarget(
	_ context.Context,
	request RecoveryPlanTargetEnumerationRequest,
) (RecoveryPlanTargetEnumeration, error) {
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}

func (fake *recoveryApplicationMaterializerFake) MaterializeCreatePlan(
	_ context.Context,
	_ CreatePlanIntentRequest,
) (CreatePlanRequest, error) {
	fake.planCalls++
	return fake.plan, nil
}

func (fake *recoveryApplicationMaterializerFake) MaterializePreflight(
	_ context.Context,
	_ RecoveryPreflightRequest,
) (PreflightPersistenceRequest, error) {
	fake.preflightCalls++
	return fake.preflight, nil
}

type recoveryApplicationPlanOwnerFake struct {
	request CreatePlanRequest
	result  CreatePlanResult
	calls   int
}

func (fake *recoveryApplicationPlanOwnerFake) CreatePlan(
	_ context.Context,
	request CreatePlanRequest,
) (CreatePlanResult, error) {
	fake.calls++
	fake.request = request
	return fake.result, nil
}

type recoveryApplicationPreflightOwnerFake struct {
	request PreflightPersistenceRequest
	result  PreflightPersistenceResult
	calls   int
}

func (fake *recoveryApplicationPreflightOwnerFake) EvaluateAndPersist(
	_ context.Context,
	request PreflightPersistenceRequest,
) (PreflightPersistenceResult, error) {
	fake.calls++
	fake.request = request
	return fake.result, nil
}
