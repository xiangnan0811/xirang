package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/settings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRecoveryEligibilityAuthorityContractIsSealedAndOwnsAllPorts(t *testing.T) {
	source := recoveryEligibilitySourcePortFake{}
	security := recoveryEligibilitySecurityPortFake{}
	targetRoot := recoveryEligibilityTargetRootPortFake{}
	targetObservation := recoveryEligibilityTargetObservationPortFake{}
	authority, err := NewRecoveryEligibilityAuthority(RecoveryEligibilityAuthorityDependencies{
		DB:                &gorm.DB{},
		Source:            source,
		Security:          security,
		TargetRoot:        targetRoot,
		TargetObservation: targetObservation,
		Now:               time.Now,
	})
	if err != nil {
		t.Fatalf("construct sealed Recovery eligibility authority: %v", err)
	}
	if authority == nil || authority.source == nil || authority.security == nil ||
		authority.targetRoot == nil || authority.targetObservation == nil {
		t.Fatal("Recovery eligibility authority did not retain all four owning ports")
	}
	var _ RecoveryAuthorityRevalidator = authority
	var _ RecoveryPreflightExternalEvidenceAuthority = authority
	var _ RecoveryReconciliationRevisionSource = authority

	observationType := reflect.TypeOf(RecoveryAuthorityObservation{})
	for index := 0; index < observationType.NumField(); index++ {
		if field := observationType.Field(index); field.PkgPath == "" {
			t.Fatalf("sealed Recovery authority observation exposes field %q", field.Name)
		}
	}
	if _, err := authority.ObserveRecoveryAuthority(context.Background(), RecoveryAuthorityBinding{}); !errors.Is(err, ErrRecoveryTargetUnavailable) {
		t.Fatalf("B4 contract must remain fail closed before B5/B6 implementation: %v", err)
	}
}

func TestRecoveryEligibilityAuthorityPostgres(t *testing.T) {
	db := newAuthorizationReceiptPostgresScopedDB(t)
	harness := newRecoveryEligibilityHarnessOnDB(t, db)

	observed, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
	if err != nil {
		t.Fatalf("observe PostgreSQL eligibility authority: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return harness.authority.RevalidateRecoveryAuthorityTx(
			context.Background(), tx, harness.binding, observed,
		)
	}); err != nil {
		t.Fatalf("revalidate PostgreSQL eligibility authority: %v", err)
	}

	observed, err = harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
	if err != nil {
		t.Fatalf("observe PostgreSQL eligibility authority before drift: %v", err)
	}
	harness.root.current.AuthorityRevision = "root-authority-v2"
	err = db.Transaction(func(tx *gorm.DB) error {
		return harness.authority.RevalidateRecoveryAuthorityTx(
			context.Background(), tx, harness.binding, observed,
		)
	})
	if !errors.Is(err, ErrRecoveryTargetChanged) {
		t.Fatalf("PostgreSQL post-observation drift error=%v, want target changed", err)
	}
}

func TestRecoveryEligibilityAuthorityConstructorRejectsMissingOwningPort(t *testing.T) {
	complete := RecoveryEligibilityAuthorityDependencies{
		DB:                &gorm.DB{},
		Source:            recoveryEligibilitySourcePortFake{},
		Security:          recoveryEligibilitySecurityPortFake{},
		TargetRoot:        recoveryEligibilityTargetRootPortFake{},
		TargetObservation: recoveryEligibilityTargetObservationPortFake{},
		Now:               time.Now,
	}
	tests := []struct {
		name   string
		mutate func(*RecoveryEligibilityAuthorityDependencies)
	}{
		{name: "database", mutate: func(value *RecoveryEligibilityAuthorityDependencies) { value.DB = nil }},
		{name: "source", mutate: func(value *RecoveryEligibilityAuthorityDependencies) { value.Source = nil }},
		{name: "security", mutate: func(value *RecoveryEligibilityAuthorityDependencies) { value.Security = nil }},
		{name: "target root", mutate: func(value *RecoveryEligibilityAuthorityDependencies) { value.TargetRoot = nil }},
		{name: "target observation", mutate: func(value *RecoveryEligibilityAuthorityDependencies) { value.TargetObservation = nil }},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			dependencies := complete
			testCase.mutate(&dependencies)
			if authority, err := NewRecoveryEligibilityAuthority(dependencies); authority != nil || !errors.Is(err, ErrRecoveryTargetUnavailable) {
				t.Fatalf("missing %s got authority=%v err=%v", testCase.name, authority, err)
			}
		})
	}
}

func TestRecoveryEligibilityPrivateProductsAreRedacted(t *testing.T) {
	const privateCanary = "PRIVATE_ELIGIBILITY_CANARY"
	products := []any{
		RecoveryAuthorityBinding{PlanID: privateCanary},
		RecoveryEligibilitySourceObservation{RepositoryBindingRevision: privateCanary},
		RecoveryEligibilitySecurityObservation{FindingSetDigest: privateCanary},
		RecoveryEligibilityTargetRootSnapshot{Locator: privateCanary},
		RecoveryEligibilityTargetObservation{CanonicalRoot: privateCanary},
		RecoveryAuthorityObservation{
			binding: recoveryEligibilityBinding{authority: RecoveryAuthorityBinding{PlanID: privateCanary}},
			proof:   &recoveryEligibilityProof{bindingDigest: privateCanary},
		},
	}
	for _, product := range products {
		encoded, err := json.Marshal(product)
		if err != nil {
			t.Fatalf("marshal private eligibility product %T: %v", product, err)
		}
		for _, formatted := range []string{
			fmt.Sprint(product), fmt.Sprintf("%+v", product), fmt.Sprintf("%#v", product), string(encoded),
		} {
			if strings.Contains(formatted, privateCanary) {
				t.Fatalf("private eligibility product %T leaked through %q", product, formatted)
			}
		}
	}
}

type recoveryEligibilitySourcePortFake struct{}

func (recoveryEligibilitySourcePortFake) ObserveRecoveryEligibilitySource(
	context.Context,
	provider.RecoverySourceAuthorityRequest,
) (provider.RsyncRestoreSource, RecoveryEligibilitySourceObservation, error) {
	return nil, RecoveryEligibilitySourceObservation{}, nil
}

func (recoveryEligibilitySourcePortFake) RevalidateRecoveryEligibilitySourceTx(
	context.Context,
	*gorm.DB,
	RecoveryAuthorityBinding,
	RecoveryEligibilitySourceObservation,
) error {
	return nil
}

type recoveryEligibilitySecurityPortFake struct{}

func (recoveryEligibilitySecurityPortFake) ObserveRecoveryEligibilitySecurity(
	context.Context,
	RecoveryAuthorityBinding,
) (RecoveryEligibilitySecurityObservation, error) {
	return RecoveryEligibilitySecurityObservation{}, nil
}

func (recoveryEligibilitySecurityPortFake) RevalidateRecoveryEligibilitySecurityTx(
	context.Context,
	*gorm.DB,
	RecoveryAuthorityBinding,
	RecoveryEligibilitySecurityObservation,
) error {
	return nil
}

type recoveryEligibilityTargetRootPortFake struct{}

func (recoveryEligibilityTargetRootPortFake) CaptureRecoveryEligibilityTargetRootTx(
	context.Context,
	*gorm.DB,
	RecoveryAuthorityBinding,
) (RecoveryEligibilityTargetRootSnapshot, error) {
	return RecoveryEligibilityTargetRootSnapshot{}, nil
}

func (recoveryEligibilityTargetRootPortFake) RevalidateRecoveryEligibilityTargetRootTx(
	context.Context,
	*gorm.DB,
	RecoveryAuthorityBinding,
	RecoveryEligibilityTargetRootSnapshot,
) error {
	return nil
}

func (recoveryEligibilityTargetRootPortFake) ResolveRecoveryReconciliationRevisionsTx(
	context.Context,
	*gorm.DB,
	uint,
	string,
) (RecoveryReconciliationRevisionSnapshot, error) {
	return RecoveryReconciliationRevisionSnapshot{}, nil
}

type recoveryEligibilityTargetObservationPortFake struct{}

func (recoveryEligibilityTargetObservationPortFake) ObserveRecoveryEligibilityTarget(
	context.Context,
	RecoveryEligibilityTargetObservationRequest,
) (RecoveryEligibilityTargetObservation, error) {
	return RecoveryEligibilityTargetObservation{}, nil
}

func TestRecoveryEligibilityAuthorityIssuesCompleteManagedRsyncObservation(t *testing.T) {
	harness := newRecoveryEligibilityHarness(t)

	observation, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
	if err != nil {
		t.Fatalf("observe complete managed Rsync eligibility: %v", err)
	}
	if observation.proof == nil || !observation.proof.production ||
		observation.proof.bindingDigest == "" || observation.observedAt != harness.now ||
		!observation.expiresAt.Equal(harness.now.Add(5*time.Minute)) {
		t.Fatalf("incomplete sealed observation: %#v", observation)
	}
	if observation.binding.reservedBytes != harness.root.current.Policy.ReserveBytes ||
		observation.binding.reservedInodes != harness.root.current.Policy.ReserveInodes ||
		observation.binding.findingDisposition != SecurityFindingDispositionClean ||
		!observation.binding.sourceAccessible || observation.binding.overlapsSourceRoot ||
		observation.binding.overlapsXirangRoot {
		t.Fatalf("incorrect closed facts: %#v", observation.binding)
	}
	if harness.source.observeCalls != 1 || harness.security.observeCalls != 1 ||
		harness.target.observeCalls != 1 || harness.root.captureCalls != 1 ||
		harness.source.revalidateCalls != 1 || harness.security.revalidateCalls != 1 ||
		harness.root.revalidateCalls != 1 {
		t.Fatalf("unexpected authority call counts: source=%+v security=%+v root=%+v target=%+v",
			harness.source, harness.security, harness.root, harness.target)
	}
	if harness.source.lastClose == nil || harness.source.lastClose.closeCalls != 1 {
		t.Fatalf("transferred source close calls=%v, want exactly one", harness.source.lastClose)
	}
}

func TestRecoveryEligibilityAuthorityRejectsDurableDriftAfterObservation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*recoveryEligibilityHarness)
	}{
		{name: "capability", mutate: func(h *recoveryEligibilityHarness) {
			h.source.afterObserve = func() { h.source.current.CapabilityRevision++ }
		}},
		{name: "source binding", mutate: func(h *recoveryEligibilityHarness) {
			h.source.afterObserve = func() { h.source.current.RepositoryBindingRevision = "repository-binding-drift" }
		}},
		{name: "policy", mutate: func(h *recoveryEligibilityHarness) {
			h.security.afterObserve = func() { h.security.current.PolicyRevision = "policy-drift" }
		}},
		{name: "finding", mutate: func(h *recoveryEligibilityHarness) {
			h.security.afterObserve = func() { h.security.current.FindingSetDigest = strings.Repeat("f", 64) }
		}},
		{name: "root authority", mutate: func(h *recoveryEligibilityHarness) {
			h.target.afterObserve = func() { h.root.current.AuthorityRevision = "root-authority-drift" }
		}},
		{name: "root observation", mutate: func(h *recoveryEligibilityHarness) {
			h.target.afterObserve = func() { h.root.current.RootObservationRevision = "root-observation-drift" }
		}},
		{name: "node", mutate: func(h *recoveryEligibilityHarness) {
			h.target.afterObserve = func() { h.root.current.NodeRevision = "node-drift" }
		}},
		{name: "credential", mutate: func(h *recoveryEligibilityHarness) {
			h.target.afterObserve = func() { h.root.current.CredentialRevision = "credential-drift" }
		}},
		{name: "reserve", mutate: func(h *recoveryEligibilityHarness) {
			h.target.afterObserve = func() { h.root.current.Policy.ReserveBytes++ }
		}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newRecoveryEligibilityHarness(t)
			testCase.mutate(harness)
			if _, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding); !errors.Is(err, ErrRecoveryTargetChanged) {
				t.Fatalf("drift error=%v, want ErrRecoveryTargetChanged", err)
			}
			if harness.source.lastClose == nil || harness.source.lastClose.closeCalls != 1 {
				t.Fatalf("drift source close calls=%v, want exactly one", harness.source.lastClose)
			}
		})
	}
}

func TestRecoveryEligibilityAuthorityRejectsLateSourceNamespaceDrift(t *testing.T) {
	driftCases := []struct {
		name   string
		mutate func(*recoverySourceNamespaceSnapshot)
	}{
		{name: "task source", mutate: func(snapshot *recoverySourceNamespaceSnapshot) {
			snapshot.sourcePath = "/srv/PRIVATE_LATE_SOURCE_DRIFT"
		}},
		{name: "source node", mutate: func(snapshot *recoverySourceNamespaceSnapshot) {
			snapshot.nodeRevision = "PRIVATE_LATE_SOURCE_NODE_DRIFT"
		}},
		{name: "source credential", mutate: func(snapshot *recoverySourceNamespaceSnapshot) {
			snapshot.credentialRevision = "PRIVATE_LATE_SOURCE_CREDENTIAL_DRIFT"
		}},
	}
	phases := []struct {
		name string
		run  func(*testing.T, *recoveryEligibilityHarness, *lateRecoverySourceNamespaceDurable, func(*recoverySourceNamespaceSnapshot)) error
	}{
		{name: "issuance second transaction", run: func(
			_ *testing.T,
			harness *recoveryEligibilityHarness,
			durable *lateRecoverySourceNamespaceDurable,
			mutate func(*recoverySourceNamespaceSnapshot),
		) error {
			harness.target.afterObserve = func() { mutate(&durable.current) }
			_, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
			return err
		}},
		{name: "effect transaction", run: func(
			t *testing.T,
			harness *recoveryEligibilityHarness,
			durable *lateRecoverySourceNamespaceDurable,
			mutate func(*recoverySourceNamespaceSnapshot),
		) error {
			observation, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
			if err != nil {
				t.Fatalf("observe eligibility before late source drift: %v", err)
			}
			mutate(&durable.current)
			return harness.authority.db.Transaction(func(tx *gorm.DB) error {
				return harness.authority.RevalidateRecoveryAuthorityTx(
					context.Background(), tx, harness.binding, observation,
				)
			})
		}},
	}

	for _, phase := range phases {
		for _, driftCase := range driftCases {
			t.Run(phase.name+"/"+driftCase.name, func(t *testing.T) {
				harness := newRecoveryEligibilityHarness(t)
				captured := recoverySourceNamespaceSnapshot{
					sourceRef: harness.binding.SourceRef, producingTaskID: 23,
					taskRevision: "source-task-v1", sourcePath: "/srv/PRIVATE_LATE_SOURCE_NAMESPACE", nodeID: 17,
					nodeRevision: "source-node-v1", credentialRevision: "source-credential-v1",
					repositoryBindingRevision: harness.source.current.RepositoryBindingRevision,
					provenanceRevision:        harness.source.current.ProvenanceRevision,
				}
				durable := &lateRecoverySourceNamespaceDurable{current: captured}
				harness.source.canonicalPath = captured.sourcePath
				harness.source.namespaceDurable = durable
				harness.source.namespaceCaptured = captured

				err := phase.run(t, harness, durable, driftCase.mutate)
				if !errors.Is(err, ErrRecoveryTargetChanged) {
					t.Fatalf("late source namespace drift error=%v, want ErrRecoveryTargetChanged", err)
				}
				if durable.revalidateCalls == 0 {
					t.Fatal("late source namespace drift did not reach caller-owned-tx durable revalidation")
				}
				if harness.source.observeCalls != 1 || harness.security.observeCalls != 1 || harness.target.observeCalls != 1 {
					t.Fatalf("caller-owned tx repeated external observation: source=%d security=%d target=%d",
						harness.source.observeCalls, harness.security.observeCalls, harness.target.observeCalls)
				}
				if harness.source.lastNamespace == nil {
					t.Fatal("source namespace capability was not retained for closed durable revalidation")
				}
				for _, encoded := range []string{
					fmt.Sprint(harness.source.lastNamespace),
					fmt.Sprintf("%+v", harness.source.lastNamespace),
					fmt.Sprintf("%#v", harness.source.lastNamespace),
					string(mustMarshalRecoveryEligibilityJSON(t, harness.source.lastNamespace)),
				} {
					if strings.Contains(encoded, "PRIVATE_LATE_SOURCE_") || strings.Contains(encoded, captured.sourcePath) {
						t.Fatalf("opaque source namespace product leaked through %q", encoded)
					}
				}
			})
		}
	}
}

func mustMarshalRecoveryEligibilityJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal private eligibility product %T: %v", value, err)
	}
	return encoded
}

type lateRecoverySourceNamespaceDurable struct {
	current         recoverySourceNamespaceSnapshot
	revalidateCalls int
}

func (*lateRecoverySourceNamespaceDurable) CaptureRecoverySourceNamespaceTx(
	context.Context,
	*gorm.DB,
	recoverySourceNamespaceRequest,
) (recoverySourceNamespaceSnapshot, error) {
	return recoverySourceNamespaceSnapshot{}, errors.New("unexpected source namespace capture")
}

func (durable *lateRecoverySourceNamespaceDurable) RevalidateRecoverySourceNamespaceTx(
	_ context.Context,
	tx *gorm.DB,
	_ recoverySourceNamespaceRequest,
	_ recoverySourceNamespaceSnapshot,
) (recoverySourceNamespaceSnapshot, error) {
	durable.revalidateCalls++
	if tx == nil {
		return recoverySourceNamespaceSnapshot{}, errors.New("source namespace revalidation missing caller transaction")
	}
	return durable.current, nil
}

func TestRecoveryEligibilityAuthorityRejectsUnsupportedProviderBeforeAccess(t *testing.T) {
	for _, kind := range []backupasset.ProviderKind{backupasset.ProviderRestic, backupasset.ProviderRclone} {
		t.Run(string(kind), func(t *testing.T) {
			harness := newRecoveryEligibilityHarness(t)
			harness.binding.Provider = kind
			if _, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding); !errors.Is(err, ErrRecoveryTargetUnavailable) {
				t.Fatalf("unsupported provider error=%v", err)
			}
			if harness.source.observeCalls != 0 || harness.security.observeCalls != 0 ||
				harness.root.captureCalls != 0 || harness.target.observeCalls != 0 {
				t.Fatalf("unsupported provider reached authority ports: source=%d security=%d root=%d target=%d",
					harness.source.observeCalls, harness.security.observeCalls,
					harness.root.captureCalls, harness.target.observeCalls)
			}
		})
	}
}

func TestRecoveryEligibilityAuthorityRejectsPartialAndSubstitutedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*recoveryEligibilityHarness)
	}{
		{name: "nil source capability", mutate: func(h *recoveryEligibilityHarness) { h.source.nilCapability = true }},
		{name: "wrong source capability type", mutate: func(h *recoveryEligibilityHarness) { h.source.rawCapability = true }},
		{name: "incomplete source proof", mutate: func(h *recoveryEligibilityHarness) { h.source.omitProof = true }},
		{name: "source request echo", mutate: func(h *recoveryEligibilityHarness) {
			h.source.sourceRefOverride = provider.RsyncRestoreSourceRef{PlanID: strings.Repeat("9", 32)}
		}},
		{name: "incomplete security", mutate: func(h *recoveryEligibilityHarness) { h.security.current.Complete = false }},
		{name: "security policy substitution", mutate: func(h *recoveryEligibilityHarness) { h.security.current.PolicyRevision = "policy-substituted" }},
		{name: "target node substitution", mutate: func(h *recoveryEligibilityHarness) { h.target.current.NodeRevision = "node-substituted" }},
		{name: "target credential substitution", mutate: func(h *recoveryEligibilityHarness) { h.target.current.CredentialRevision = "credential-substituted" }},
		{name: "target root observation substitution", mutate: func(h *recoveryEligibilityHarness) {
			h.target.current.RootObservationRevision = "root-observation-substituted"
		}},
		{name: "target locator substitution", mutate: func(h *recoveryEligibilityHarness) { h.target.current.CanonicalRoot = "/srv/other" }},
		{name: "target not read only", mutate: func(h *recoveryEligibilityHarness) { h.target.current.ReadOnly = false }},
		{name: "target incomplete", mutate: func(h *recoveryEligibilityHarness) { h.target.current.Complete = false }},
		{name: "zero reserve bytes", mutate: func(h *recoveryEligibilityHarness) { h.root.current.Policy.ReserveBytes = 0 }},
		{name: "zero reserve inodes", mutate: func(h *recoveryEligibilityHarness) { h.root.current.Policy.ReserveInodes = 0 }},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newRecoveryEligibilityHarness(t)
			testCase.mutate(harness)
			if _, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding); !errors.Is(err, ErrRecoveryTargetUnavailable) && !errors.Is(err, ErrRecoveryTargetChanged) {
				t.Fatalf("partial/substituted evidence error=%v, want closed unavailable/conflict", err)
			}
			if harness.source.lastClose != nil && harness.source.lastClose.closeCalls != 1 {
				t.Fatalf("partial source close calls=%d, want exactly one", harness.source.lastClose.closeCalls)
			}
			if testCase.name == "nil source capability" || testCase.name == "wrong source capability type" ||
				testCase.name == "incomplete source proof" || testCase.name == "source request echo" ||
				testCase.name == "incomplete security" || testCase.name == "security policy substitution" {
				if harness.target.observeCalls != 0 {
					t.Fatalf("partial evidence reached target observer %d times", harness.target.observeCalls)
				}
			}
		})
	}
}

func TestRecoveryEligibilityAuthorityUsesAuthenticatedComponentBoundaryOverlap(t *testing.T) {
	tests := []struct {
		name        string
		sourceNode  string
		targetNode  string
		sourcePath  string
		targetPath  string
		wantOverlap bool
	}{
		{name: "same node ancestor", sourceNode: "node-proof", targetNode: "node-proof", sourcePath: "/srv/source", targetPath: "/srv/source/restore", wantOverlap: true},
		{name: "same node descendant", sourceNode: "node-proof", targetNode: "node-proof", sourcePath: "/srv/source/tree", targetPath: "/srv/source", wantOverlap: true},
		{name: "same node sibling prefix", sourceNode: "node-proof", targetNode: "node-proof", sourcePath: "/srv/source", targetPath: "/srv/source-old", wantOverlap: false},
		{name: "different authenticated node", sourceNode: "source-proof", targetNode: "target-proof", sourcePath: "/srv/source", targetPath: "/srv/source/restore", wantOverlap: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newRecoveryEligibilityHarness(t)
			harness.source.authenticatedNodeIdentity = testCase.sourceNode
			harness.source.canonicalPath = testCase.sourcePath
			harness.source.namespaceCaptured.sourcePath = testCase.sourcePath
			harness.source.namespaceDurable.(*lateRecoverySourceNamespaceDurable).current =
				harness.source.namespaceCaptured
			harness.target.current.AuthenticatedNodeIdentity = testCase.targetNode
			harness.target.current.CanonicalRoot = testCase.targetPath
			harness.root.current.Locator = testCase.targetPath
			observation, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
			if err != nil {
				t.Fatalf("observe overlap evidence: %v", err)
			}
			if observation.binding.overlapsSourceRoot != testCase.wantOverlap {
				t.Fatalf("overlap=%t, want %t", observation.binding.overlapsSourceRoot, testCase.wantOverlap)
			}
		})
	}
}

func TestRecoveryEligibilityAuthorityLockedRevalidationRejectsUnsafeOrDriftedProduct(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*recoveryEligibilityHarness, *RecoveryAuthorityObservation)
	}{
		{name: "durable source drift", mutate: func(h *recoveryEligibilityHarness, _ *RecoveryAuthorityObservation) {
			h.source.current.ProvenanceRevision = "post-observation-drift"
		}},
		{name: "durable security drift", mutate: func(h *recoveryEligibilityHarness, _ *RecoveryAuthorityObservation) {
			h.security.current.FindingSetDigest = strings.Repeat("4", 64)
		}},
		{name: "durable root drift", mutate: func(h *recoveryEligibilityHarness, _ *RecoveryAuthorityObservation) {
			h.root.current.AuthorityRevision = "post-observation-root-drift"
		}},
		{name: "overlap", mutate: func(_ *recoveryEligibilityHarness, observation *RecoveryAuthorityObservation) {
			observation.binding.overlapsSourceRoot = true
			observation.proof.bindingDigest = recoveryEligibilityBindingDigest(observation.binding)
		}},
		{name: "insufficient reserve", mutate: func(_ *recoveryEligibilityHarness, observation *RecoveryAuthorityObservation) {
			observation.binding.target.FreeBytes = observation.binding.reservedBytes
			observation.proof.bindingDigest = recoveryEligibilityBindingDigest(observation.binding)
		}},
		{name: "expired", mutate: func(h *recoveryEligibilityHarness, _ *RecoveryAuthorityObservation) {
			h.authority.now = func() time.Time { return h.now.Add(6 * time.Minute) }
		}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newRecoveryEligibilityHarness(t)
			observation, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
			if err != nil {
				t.Fatalf("observe authority: %v", err)
			}
			testCase.mutate(harness, &observation)
			err = harness.authority.db.Transaction(func(tx *gorm.DB) error {
				return harness.authority.RevalidateRecoveryAuthorityTx(
					context.Background(), tx, harness.binding, observation,
				)
			})
			if !errors.Is(err, ErrRecoveryTargetChanged) {
				t.Fatalf("locked revalidation error=%v, want ErrRecoveryTargetChanged", err)
			}
		})
	}
}

func TestRecoveryEligibilityAuthorityLockedRevalidationAcceptsExactCurrentProduct(t *testing.T) {
	harness := newRecoveryEligibilityHarness(t)
	observation, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
	if err != nil {
		t.Fatalf("observe authority: %v", err)
	}
	err = harness.authority.db.Transaction(func(tx *gorm.DB) error {
		return harness.authority.RevalidateRecoveryAuthorityTx(
			context.Background(), tx, harness.binding, observation,
		)
	})
	if err != nil {
		t.Fatalf("revalidate exact current product: %v", err)
	}
}

func TestRecoveryEligibilityAuthorityReconciliationUsesIndependentAuthorityRevision(t *testing.T) {
	harness := newRecoveryEligibilityHarness(t)
	result, err := harness.authority.ResolveRecoveryReconciliationRevisionsTx(
		context.Background(), harness.authority.db, harness.root.current.NodeID, harness.root.current.RootID,
	)
	if err != nil {
		t.Fatalf("resolve reconciliation revisions: %v", err)
	}
	if result.NodeRevision != harness.root.current.NodeRevision ||
		result.CredentialRevision != harness.root.current.CredentialRevision ||
		result.RootRevision != harness.root.current.AuthorityRevision ||
		result.RootRevision == harness.root.current.LocatorDigest ||
		result.RootRevision == harness.root.current.RootObservationRevision {
		t.Fatalf("incorrect reconciliation authority projection: %#v", result)
	}
}

func TestRecoveryEligibilityAuthorityPreflightProjectionUsesSameSealedProduct(t *testing.T) {
	harness := newRecoveryEligibilityHarness(t)
	sealed, err := harness.authority.ObserveRecoveryAuthority(context.Background(), harness.binding)
	if err != nil {
		t.Fatalf("observe eligibility: %v", err)
	}
	request := PreflightExternalEvidenceRequest{
		PlanID: harness.binding.PlanID, PlanBindingDigest: harness.binding.PlanBindingDigest,
		PlanTransitionRevision:   harness.binding.PlanTransitionRevision,
		SourceRevisionDigest:     harness.binding.SourceRevisionDigest,
		CapabilityRevision:       harness.binding.CapabilityRevision,
		PolicyRevision:           harness.binding.SecurityPolicyRevision,
		FindingSetDigest:         harness.binding.SecurityFindingSetDigest,
		TargetRootRevision:       harness.binding.RootRevision,
		TargetFilesystemRevision: harness.binding.FilesystemRevision,
		TargetRevision:           harness.binding.PreflightTargetRevision,
		RequiredBytes:            harness.binding.RequiredBytes, RequiredInodes: harness.binding.RequiredInodes,
	}
	projected, err := recoveryEligibilityPreflightProjection(request, sealed)
	if err != nil {
		t.Fatalf("project sealed eligibility: %v", err)
	}
	if projected.PlanID != request.PlanID || projected.CapabilityRevision != request.CapabilityRevision ||
		projected.PolicyRevision != request.PolicyRevision || projected.FindingSetDigest != request.FindingSetDigest ||
		projected.TargetRootRevision != request.TargetRootRevision ||
		projected.ReservedBytes != harness.root.current.Policy.ReserveBytes ||
		projected.ReservedInodes != harness.root.current.Policy.ReserveInodes ||
		!projected.SourceAccessible || projected.FindingDisposition != SecurityFindingDispositionClean {
		t.Fatalf("incorrect preflight projection: %#v", projected)
	}
	substituted := request
	substituted.CapabilityRevision = "caller-substituted-capability"
	if _, err := recoveryEligibilityPreflightProjection(substituted, sealed); !errors.Is(err, ErrRecoveryPreflightConflict) {
		t.Fatalf("substituted request error=%v, want conflict", err)
	}
}

type recoveryEligibilityHarness struct {
	now       time.Time
	binding   RecoveryAuthorityBinding
	source    *statefulRecoveryEligibilitySource
	security  *statefulRecoveryEligibilitySecurity
	root      *statefulRecoveryEligibilityTargetRoot
	target    *statefulRecoveryEligibilityTarget
	authority *RecoveryEligibilityAuthority
}

func newRecoveryEligibilityHarness(t *testing.T) *recoveryEligibilityHarness {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared&_txlock=immediate"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open eligibility DB: %v", err)
	}
	return newRecoveryEligibilityHarnessOnDB(t, db)
}

func newRecoveryEligibilityHarnessOnDB(t *testing.T, db *gorm.DB) *recoveryEligibilityHarness {
	t.Helper()
	if db == nil {
		t.Fatal("eligibility DB is required")
	}
	now := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	binding := RecoveryAuthorityBinding{
		Operation: AuthorizationReceiptWriteAuthorize, Provider: backupasset.ProviderRsync,
		PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("a", 64), PlanTransitionRevision: 7,
		RepositoryID: strings.Repeat("2", 32), RecoveryPointID: strings.Repeat("3", 32),
		CatalogGenerationID: strings.Repeat("4", 32), SelectionDigest: strings.Repeat("b", 64),
		SourceRevisionDigest: strings.Repeat("c", 64), ManifestDigest: strings.Repeat("d", 64),
		TargetMode: TargetModeIsolated, TargetNodeID: 41, TargetRootID: "restore-root",
		RootLocatorDigest: strings.Repeat("e", 64), PathDigest: strings.Repeat("f", 64),
		TargetBaseRevision: "target-base-v1", CredentialScopeRevision: "credential-scope-v1",
		RootRevision: "root-remote-v1", FilesystemRevision: "filesystem-v1",
		CapabilityRevision: "capability-v1", ConflictPolicy: ConflictFailOnConflict,
		OperationSetDigest: strings.Repeat("5", 64), DeleteSetDigest: EmptyDeleteSetDigest,
		SecurityDecision: SecurityDecisionAllowClean, SecurityDecisionDigest: strings.Repeat("6", 64),
		SecurityFindingSetDigest: strings.Repeat("7", 64), SecurityPolicyRevision: "policy-v1",
		PreflightID: strings.Repeat("8", 32), PreflightRevision: "preflight-v1",
		PreflightTargetRevision: "target-v1", PreflightNodeRevision: "node-v1",
		RequiredBytes: 100, RequiredInodes: 10,
	}
	binding.SourceRef = provider.RsyncRestoreSourceRef{
		PlanID: binding.PlanID, PlanBindingDigest: binding.PlanBindingDigest,
		RepositoryID: binding.RepositoryID, RecoveryPointID: binding.RecoveryPointID,
		CatalogGenerationID: binding.CatalogGenerationID, SelectionDigest: binding.SelectionDigest,
		SourceRevisionDigest: binding.SourceRevisionDigest, ManifestDigest: binding.ManifestDigest,
	}
	source := &statefulRecoveryEligibilitySource{
		binding: binding,
		current: RecoveryEligibilitySourceObservation{
			RepositoryCapabilityRevision: 3, CapabilityRevision: 5,
			SourceAccessIdentity: "source-access-v1", SourceFingerprint: strings.Repeat("9", 64),
			ManagedRootIdentity: strings.Repeat("0", 64), RepositoryBindingRevision: "repository-binding-v1",
			ProvenanceRevision: "provenance-v1",
		},
		authenticatedNodeIdentity: "source-node-proof", canonicalPath: "/srv/source",
	}
	source.namespaceCaptured = recoverySourceNamespaceSnapshot{
		sourceRef: binding.SourceRef, producingTaskID: 23,
		taskRevision: "source-task-v1", sourcePath: source.canonicalPath, nodeID: 17,
		nodeRevision: "source-node-v1", credentialRevision: "source-credential-v1",
		repositoryBindingRevision: source.current.RepositoryBindingRevision,
		provenanceRevision:        source.current.ProvenanceRevision,
	}
	source.namespaceDurable = &lateRecoverySourceNamespaceDurable{current: source.namespaceCaptured}
	security := &statefulRecoveryEligibilitySecurity{current: RecoveryEligibilitySecurityObservation{
		PolicyRevision: binding.SecurityPolicyRevision, FindingSetDigest: binding.SecurityFindingSetDigest,
		Disposition: SecurityFindingDispositionClean, Complete: true, ObservedAt: now,
	}}
	root := &statefulRecoveryEligibilityTargetRoot{current: RecoveryEligibilityTargetRootSnapshot{
		NodeID: binding.TargetNodeID, RootID: binding.TargetRootID, Locator: "/srv/restore",
		LocatorDigest: binding.RootLocatorDigest, AuthorityRevision: "root-authority-v1",
		RootObservationRevision: "root-observation-v1",
		Policy:                  settings.RecoveryTargetRootPolicy{ReserveBytes: 50, ReserveInodes: 5, OverlapPolicyBinding: "overlap-policy-v1"},
		NodeRevision:            binding.PreflightNodeRevision, CredentialRevision: binding.CredentialScopeRevision,
	}}
	target := &statefulRecoveryEligibilityTarget{current: RecoveryEligibilityTargetObservation{
		AuthenticatedNodeIdentity: "target-node-proof", CanonicalRoot: root.current.Locator,
		NodeRevision: root.current.NodeRevision, CredentialRevision: root.current.CredentialRevision,
		RootRevision: binding.RootRevision, RootObservationRevision: root.current.RootObservationRevision,
		FilesystemRevision: binding.FilesystemRevision, TargetRevision: binding.PreflightTargetRevision,
		FreeBytes: 1000, FreeInodes: 100, ReadOnly: true, Complete: true,
		ObservedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}}
	authority, err := NewRecoveryEligibilityAuthority(RecoveryEligibilityAuthorityDependencies{
		DB: db, Source: source, Security: security, TargetRoot: root,
		TargetObservation: target, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("construct eligibility authority: %v", err)
	}
	return &recoveryEligibilityHarness{now: now, binding: binding, source: source, security: security, root: root, target: target, authority: authority}
}

type eligibilityPinnedCloseSpy struct{ closeCalls int }

func (*eligibilityPinnedCloseSpy) OpenDeclaredRegular(context.Context, provider.RestoreEntry) (provider.RsyncRestoreSourceStream, error) {
	return nil, provider.ErrRsyncRestoreUnavailable
}
func (*eligibilityPinnedCloseSpy) MaterializeDeclaredEntries(context.Context, []provider.RestoreEntry) ([]provider.RestoreEntry, error) {
	return nil, provider.ErrRsyncRestoreUnavailable
}
func (*eligibilityPinnedCloseSpy) Revalidate(context.Context) error { return nil }
func (source *eligibilityPinnedCloseSpy) Close() error {
	source.closeCalls++
	return nil
}

type rawEligibilitySource struct{ closeCalls int }

func (*rawEligibilitySource) OpenDeclaredRegular(context.Context, provider.RestoreEntry) (provider.RsyncRestoreSourceStream, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (*rawEligibilitySource) MaterializeDeclaredEntries(_ context.Context, entries []provider.RestoreEntry) ([]provider.RestoreEntry, error) {
	return entries, nil
}
func (*rawEligibilitySource) Revalidate(context.Context) error { return nil }
func (source *rawEligibilitySource) Close() error              { source.closeCalls++; return nil }

type statefulRecoveryEligibilitySource struct {
	binding                   RecoveryAuthorityBinding
	current                   RecoveryEligibilitySourceObservation
	authenticatedNodeIdentity string
	canonicalPath             string
	afterObserve              func()
	sourceRefOverride         provider.RsyncRestoreSourceRef
	nilCapability             bool
	rawCapability             bool
	omitProof                 bool
	observeCalls              int
	revalidateCalls           int
	lastClose                 *eligibilityPinnedCloseSpy
	namespaceDurable          recoverySourceNamespaceDurable
	namespaceCaptured         recoverySourceNamespaceSnapshot
	lastNamespace             *RecoverySourceNamespaceObservation
}

func (source *statefulRecoveryEligibilitySource) ObserveRecoveryEligibilitySource(
	_ context.Context,
	request provider.RecoverySourceAuthorityRequest,
) (provider.RsyncRestoreSource, RecoveryEligibilitySourceObservation, error) {
	source.observeCalls++
	observed := source.current
	if source.afterObserve != nil {
		source.afterObserve()
	}
	if source.nilCapability {
		return nil, observed, nil
	}
	if source.rawCapability {
		return &rawEligibilitySource{}, observed, nil
	}
	closeSpy := &eligibilityPinnedCloseSpy{}
	source.lastClose = closeSpy
	ref := request.RsyncRef
	if source.sourceRefOverride.PlanID != "" {
		ref = source.sourceRefOverride
	}
	proof := &recoverySourceNamespaceProof{
		authenticatedNodeIdentity: source.authenticatedNodeIdentity, nodeID: 17,
		nodeRevision: "source-node-v1", credentialRevision: "source-credential-v1",
		taskRevision: "source-task-v1", producingTaskID: 23,
		repositoryBindingRevision: observed.RepositoryBindingRevision,
		provenanceRevision:        observed.ProvenanceRevision, sourceRef: ref,
		canonicalPath: source.canonicalPath, observationRevision: "source-observation-v1",
		observedAt: time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC),
	}
	if source.omitProof {
		proof = nil
	}
	namespace := &RecoverySourceNamespaceObservation{observation: &recoverySourceNamespaceObservation{
		proof: proof, pinned: closeSpy, durable: source.namespaceDurable,
		request: recoverySourceNamespaceRequest{
			sourceRef: ref, producingTaskID: source.namespaceCaptured.producingTaskID,
			repositoryBindingRevision: observed.RepositoryBindingRevision,
			provenanceRevision:        observed.ProvenanceRevision,
		},
		captured: source.namespaceCaptured,
	}}
	source.lastNamespace = namespace
	return namespace, observed, nil
}

func (source *statefulRecoveryEligibilitySource) RevalidateRecoveryEligibilitySourceTx(
	_ context.Context, tx *gorm.DB, _ RecoveryAuthorityBinding, observed RecoveryEligibilitySourceObservation,
) error {
	source.revalidateCalls++
	if tx == nil || !reflect.DeepEqual(source.current, observed) {
		return ErrRecoveryTargetChanged
	}
	return nil
}

type statefulRecoveryEligibilitySecurity struct {
	current         RecoveryEligibilitySecurityObservation
	afterObserve    func()
	observeCalls    int
	revalidateCalls int
}

func (security *statefulRecoveryEligibilitySecurity) ObserveRecoveryEligibilitySecurity(
	context.Context, RecoveryAuthorityBinding,
) (RecoveryEligibilitySecurityObservation, error) {
	security.observeCalls++
	observed := security.current
	if security.afterObserve != nil {
		security.afterObserve()
	}
	return observed, nil
}
func (security *statefulRecoveryEligibilitySecurity) RevalidateRecoveryEligibilitySecurityTx(
	_ context.Context, tx *gorm.DB, _ RecoveryAuthorityBinding, observed RecoveryEligibilitySecurityObservation,
) error {
	security.revalidateCalls++
	if tx == nil || !reflect.DeepEqual(security.current, observed) {
		return ErrRecoveryTargetChanged
	}
	return nil
}

type statefulRecoveryEligibilityTargetRoot struct {
	current         RecoveryEligibilityTargetRootSnapshot
	captureCalls    int
	revalidateCalls int
}

func (root *statefulRecoveryEligibilityTargetRoot) CaptureRecoveryEligibilityTargetRootTx(
	_ context.Context, tx *gorm.DB, _ RecoveryAuthorityBinding,
) (RecoveryEligibilityTargetRootSnapshot, error) {
	root.captureCalls++
	if tx == nil {
		return RecoveryEligibilityTargetRootSnapshot{}, ErrRecoveryTargetUnavailable
	}
	return root.current, nil
}
func (root *statefulRecoveryEligibilityTargetRoot) RevalidateRecoveryEligibilityTargetRootTx(
	_ context.Context, tx *gorm.DB, _ RecoveryAuthorityBinding, observed RecoveryEligibilityTargetRootSnapshot,
) error {
	root.revalidateCalls++
	if tx == nil || !reflect.DeepEqual(root.current, observed) {
		return ErrRecoveryTargetChanged
	}
	return nil
}
func (root *statefulRecoveryEligibilityTargetRoot) ResolveRecoveryReconciliationRevisionsTx(
	context.Context, *gorm.DB, uint, string,
) (RecoveryReconciliationRevisionSnapshot, error) {
	return RecoveryReconciliationRevisionSnapshot{
		NodeRevision: root.current.NodeRevision, CredentialRevision: root.current.CredentialRevision,
		RootRevision: root.current.AuthorityRevision,
	}, nil
}

type statefulRecoveryEligibilityTarget struct {
	current      RecoveryEligibilityTargetObservation
	afterObserve func()
	observeCalls int
}

func (target *statefulRecoveryEligibilityTarget) ObserveRecoveryEligibilityTarget(
	_ context.Context, request RecoveryEligibilityTargetObservationRequest,
) (RecoveryEligibilityTargetObservation, error) {
	target.observeCalls++
	observed := target.current
	if request.TargetRoot.Locator != observed.CanonicalRoot {
		return RecoveryEligibilityTargetObservation{}, ErrRecoveryTargetChanged
	}
	if target.afterObserve != nil {
		target.afterObserve()
	}
	return observed, nil
}
