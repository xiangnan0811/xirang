package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
)

type recoveryContractsTargetPortStub struct {
	observation TargetVerifyObservation
}

type recoveryContractsTargetVerifier interface {
	Verify(context.Context, TargetVerifyExpectation) (TargetVerifyObservation, error)
}

func (stub recoveryContractsTargetPortStub) Verify(
	_ context.Context,
	_ TargetVerifyExpectation,
) (TargetVerifyObservation, error) {
	return stub.observation, nil
}

var _ recoveryContractsTargetVerifier = recoveryContractsTargetPortStub{}

func TestRecoveryOperationSnapshotV2CanonicalLocatorMatrix(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	pointID := strings.Repeat("1", 32)
	rootID := "root-canonical-locator"
	rootLocatorDigest := digest("9")
	operations := []RecoveryOperation{
		{
			Kind: RecoveryOperationCreate, TargetPathDigest: digest("1"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetAbsent},
			ExpectedPostIdentityDigest: digest("a"), ExpectedPostBytes: 0, ExpectedPriorBytes: -1,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("a"),
			}},
			DisplayClass: RecoveryDisplayClassRegular,
		},
		{
			Kind: RecoveryOperationOverwrite, TargetPathDigest: digest("2"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("b")},
			ExpectedPostIdentityDigest: digest("c"), ExpectedPostBytes: 2, ExpectedPriorBytes: 1,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("b"),
			}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: 2,
		},
		{
			Kind: RecoveryOperationSkip, TargetPathDigest: digest("3"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("d")},
			ExpectedPostIdentityDigest: digest("d"), ExpectedPostBytes: -1, ExpectedPriorBytes: 3,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("c"),
			}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: 3,
		},
		{
			Kind: RecoveryOperationDelete, TargetPathDigest: digest("4"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("e")},
			ExpectedPostIdentityDigest: "", ExpectedPostBytes: -1, ExpectedPriorBytes: -1,
			Source:       RecoveryOperationSource{Kind: RecoveryOperationSourceNone},
			DisplayClass: RecoveryDisplayClassDirectory,
		},
	}
	locators := []string{"items/create", "items/overwrite", "items/skip", "items/delete"}
	semanticDigests := make([]string, len(locators))
	for index := range operations {
		var err error
		semanticDigests[index], err = SemanticTargetDigest(
			TargetModeInPlace, rootID, rootLocatorDigest, locators[index],
		)
		if err != nil {
			t.Fatalf("derive row %d semantic locator digest: %v", index, err)
		}
		setRecoveryOperationLocatorProduct(t, &operations[index], locators[index], semanticDigests[index])
	}
	isolatedDigest, err := SemanticTargetDigest(TargetModeIsolated, rootID, rootLocatorDigest, locators[0])
	if err != nil {
		t.Fatalf("derive isolated semantic locator digest: %v", err)
	}
	rootChangedDigest, err := SemanticTargetDigest(TargetModeInPlace, rootID+"-other", rootLocatorDigest, locators[0])
	if err != nil {
		t.Fatalf("derive alternate-root semantic locator digest: %v", err)
	}
	objectDigest, err := TargetObjectDigest(rootID, rootLocatorDigest, locators[0])
	if err != nil {
		t.Fatalf("derive final object digest: %v", err)
	}
	if semanticDigests[0] == isolatedDigest || semanticDigests[0] == rootChangedDigest ||
		semanticDigests[0] == objectDigest {
		t.Fatal("semantic locator digest did not separate mode, root product, and final-object domains")
	}

	products, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetModeInPlace, ConflictPolicy: ConflictExactMirror, Operations: operations,
		Limits: RecoveryOperationLimits{MaxRows: 4, MaxItems: 4, MaxBytes: 5, MaxImpactRows: 4},
	})
	if err != nil {
		t.Fatalf("canonical locator product rejected: %v", err)
	}
	encoded, err := encodeRecoveryOperationRows(products.Rows)
	if err != nil {
		t.Fatalf("encode canonical locator product: %v", err)
	}
	if !strings.Contains(encoded, `"target_relative_locator"`) || !strings.Contains(encoded, `"semantic_target_digest"`) {
		t.Fatalf("schema-v2 snapshot omitted locator product: %s", encoded)
	}
	decoded, err := decodeRecoveryOperationRows(encoded)
	if err != nil {
		t.Fatalf("decode canonical locator product: %v", err)
	}
	for index := range decoded {
		locator, semanticDigest := recoveryOperationLocatorProduct(t, decoded[index])
		if locator != locators[index] || semanticDigest != semanticDigests[index] {
			t.Fatalf("row %d locator product=%q/%q, want %q/%q", index, locator, semanticDigest, locators[index], semanticDigests[index])
		}
	}

	for _, invalidLocator := range []string{"", "/items/create", "items//create", "items/./create", "items/../create", "items/create/", `items\create`} {
		t.Run("reject "+strconv.Quote(invalidLocator), func(t *testing.T) {
			candidate := cloneRecoveryOperation(operations[0])
			setRecoveryOperationLocatorProduct(t, &candidate, invalidLocator, digest("9"))
			if _, err := NewOperationProducts(RecoveryOperationProductsInput{
				TargetMode: TargetModeInPlace, ConflictPolicy: ConflictOverwriteSelected,
				Operations: []RecoveryOperation{candidate},
				Limits:     RecoveryOperationLimits{MaxRows: 1, MaxItems: 1, MaxBytes: 0, MaxImpactRows: 1},
			}); !errors.Is(err, ErrInvalidRecoveryOperation) {
				t.Fatalf("locator %q error=%v, want ErrInvalidRecoveryOperation", invalidLocator, err)
			}
		})
	}

	duplicateLocator := []RecoveryOperation{cloneRecoveryOperation(operations[0]), cloneRecoveryOperation(operations[1])}
	setRecoveryOperationLocatorProduct(t, &duplicateLocator[1], locators[0], digest("9"))
	semanticCollision := []RecoveryOperation{cloneRecoveryOperation(operations[0]), cloneRecoveryOperation(operations[1])}
	setRecoveryOperationLocatorProduct(t, &semanticCollision[1], locators[1], semanticDigests[0])
	for name, rows := range map[string][]RecoveryOperation{
		"duplicate locator":  duplicateLocator,
		"semantic collision": semanticCollision,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewOperationProducts(RecoveryOperationProductsInput{
				TargetMode: TargetModeInPlace, ConflictPolicy: ConflictOverwriteSelected, Operations: rows,
				Limits: RecoveryOperationLimits{MaxRows: 2, MaxItems: 2, MaxBytes: 2, MaxImpactRows: 2},
			}); !errors.Is(err, ErrInvalidRecoveryOperation) {
				t.Fatalf("duplicate/collision product error=%v, want ErrInvalidRecoveryOperation", err)
			}
		})
	}
}

func TestRecoveryOperationSnapshotV2WholeProductTamperMatrix(t *testing.T) {
	t.Run("canonical versioned snapshot", TestRecoveryAuthorizationReceiptOperationSnapshotCodec)
	t.Run("invalid whole products", TestRecoveryOperationSnapshotDecodeRejectsInvalidWholeProducts)
	t.Run("operation-specific facts", TestRecoveryOperationAmendmentExpectedPostFactsAndEnvelope)
	t.Run("delete operation facts", TestRecoveryOperationAmendmentRejectsIncorrectExpectedPostFacts)
	t.Run("delete policy", TestOperationProductsCanonicalGoldenAndDeletePolicy)
	t.Run("persisted encrypted aggregate", TestRecoveryAuthorizationReceiptExecutePersistsExactOperationRows)

	operationType := reflect.TypeOf(RecoveryOperation{})
	for _, fieldName := range []string{"TargetRelativeLocator", "SemanticTargetDigest"} {
		field, found := operationType.FieldByName(fieldName)
		if !found || field.Type.Kind() != reflect.String {
			t.Fatalf("RecoveryOperation.%s is required for whole-product validation", fieldName)
		}
	}
	snapshotRowType := reflect.TypeOf(recoveryOperationSnapshotRow{})
	for fieldName, jsonName := range map[string]string{
		"TargetRelativeLocator": "target_relative_locator",
		"SemanticTargetDigest":  "semantic_target_digest",
	} {
		field, found := snapshotRowType.FieldByName(fieldName)
		if !found || field.Tag.Get("json") != jsonName {
			t.Fatalf("schema-v2 row %s json contract=%q found=%t, want %q", fieldName, field.Tag.Get("json"), found, jsonName)
		}
	}
}

func TestRecoveryTargetLocatorCiphertextBindingMatrix(t *testing.T) {
	bindingType := reflect.TypeOf(TargetLocatorEnvelopeBinding{})
	for _, fieldName := range []string{
		"CodecVersion", "PlanItemID", "SourceRecoveryPointID", "SourceEntryID",
		"SemanticTargetDigest", "TargetObjectDigest",
	} {
		if _, found := bindingType.FieldByName(fieldName); !found {
			t.Fatalf("TargetLocatorEnvelopeBinding is missing authenticated field %s", fieldName)
		}
	}

	itemType := reflect.TypeOf(&model.BackupAssetRecoveryJobItem{})
	if _, found := itemType.MethodByName("BeforeSave"); found {
		t.Fatal("job-item locator must bypass generic BeforeSave encryption hooks")
	}
	if _, found := itemType.MethodByName("AfterFind"); found {
		t.Fatal("job-item locator must bypass generic AfterFind decryption hooks")
	}
	for _, fieldName := range []string{"SemanticTargetDigest", "TargetObjectDigest", "EncryptedTargetRelativeLocator", "TargetLocatorKeyVersion", "TargetLocatorCipherVersion"} {
		field, found := itemType.Elem().FieldByName(fieldName)
		if !found || field.Tag.Get("json") != "-" {
			t.Fatalf("job-item authenticated locator field %s found/json=%t/%q", fieldName, found, field.Tag.Get("json"))
		}
	}

	t.Run("canonical envelope", TestTargetLocatorEnvelopeIsCanonicalVersionedAndRowBound)
	t.Run("isolated workspace suffix", TestTargetLocatorEnvelopeAcceptsIsolatedWorkspaceRelativeLocator)
	t.Run("operation fact tamper", TestTargetLocatorEnvelopeRejectsOperationFactTampering)
	t.Run("recovery-local AEAD", testRecoveryTargetLocatorAEADBinding)
}

func testRecoveryTargetLocatorAEADBinding(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	locator := "items/item-a"
	workspaceLocator := "jobs/" + strings.Repeat("1", 32)
	rootID := "root-aead-binding"
	rootLocatorDigest := digest("a")
	targetObjectDigest, err := TargetObjectDigest(
		rootID, rootLocatorDigest, workspaceLocator+"/"+locator,
	)
	if err != nil {
		t.Fatalf("derive AEAD final-object digest: %v", err)
	}
	binding := completeTargetLocatorBindingForTest(t, TargetLocatorEnvelopeBinding{
		JobID: strings.Repeat("1", 32), JobItemID: strings.Repeat("2", 32),
		PlanDigest: digest("b"), PlanItemID: strings.Repeat("3", 32),
		SourceRecoveryPointID: strings.Repeat("4", 32), SourceEntryID: digest("c"),
		TargetMode: TargetModeIsolated, NodeID: 7, RootID: rootID,
		RootLocatorDigest: rootLocatorDigest, TargetObjectDigest: targetObjectDigest,
		Operation: RecoveryOperationOverwrite, WorkspaceBindingDigest: digest("d"),
		WorkspaceRelativeLocator: workspaceLocator,
		ExpectedPriorKind:        ExpectedTargetPresent, ExpectedPriorDigest: digest("e"),
		ExpectedPostIdentityDigest: digest("f"), ExpectedPostBytes: 17, ExpectedPriorBytes: 13,
		TargetLocatorKeyVersion: 3, TargetLocatorCipherVersion: targetLocatorCipherVersion,
	}, locator)
	material := backupasset.DomainKeyMaterial{
		ID: strings.Repeat("5", 32), Domain: backupasset.KeyDomainRecoveryCleanupOwnership,
		Version: binding.TargetLocatorKeyVersion, State: backupasset.DomainKeyActive,
		Key: []byte(strings.Repeat("k", 32)),
	}

	sealed, err := SealTargetLocatorEnvelope(material, binding, locator)
	if err != nil {
		t.Fatalf("seal target locator: %v", err)
	}
	if !strings.HasPrefix(sealed, targetLocatorCiphertextPrefix) ||
		strings.HasPrefix(sealed, "enc:v2:") || strings.Contains(sealed, locator) ||
		strings.Contains(sealed, workspaceLocator) {
		t.Fatalf("recovery-local locator ciphertext has the wrong format or leaked plaintext: %q", sealed)
	}
	opened, err := OpenTargetLocatorEnvelope(material, binding, sealed)
	if err != nil || opened != locator {
		t.Fatalf("open target locator = %q, error = %v, want %q", opened, err, locator)
	}

	rebindTarget := func(candidate *TargetLocatorEnvelopeBinding) {
		t.Helper()
		semanticDigest, digestErr := SemanticTargetDigest(
			candidate.TargetMode, candidate.RootID, candidate.RootLocatorDigest, locator,
		)
		if digestErr != nil {
			t.Fatalf("derive mutated semantic digest: %v", digestErr)
		}
		finalLocator := locator
		if candidate.TargetMode == TargetModeIsolated {
			finalLocator = candidate.WorkspaceRelativeLocator + "/" + locator
		}
		objectDigest, digestErr := TargetObjectDigest(candidate.RootID, candidate.RootLocatorDigest, finalLocator)
		if digestErr != nil {
			t.Fatalf("derive mutated final-object digest: %v", digestErr)
		}
		candidate.SemanticTargetDigest = semanticDigest
		candidate.TargetObjectDigest = objectDigest
	}
	tests := []struct {
		name            string
		mutate          func(*TargetLocatorEnvelopeBinding)
		materialVersion int
	}{
		{name: "codec version", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.CodecVersion++ }},
		{name: "job id", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.JobID = strings.Repeat("6", 32) }},
		{name: "job item id", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.JobItemID = strings.Repeat("7", 32) }},
		{name: "plan digest", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.PlanDigest = digest("0") }},
		{name: "plan item id", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.PlanItemID = strings.Repeat("8", 32) }},
		{name: "source recovery point id", mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.SourceRecoveryPointID = strings.Repeat("9", 32)
		}},
		{name: "source entry id", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.SourceEntryID = digest("1") }},
		{name: "target mode", mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.TargetMode = TargetModeInPlace
			candidate.WorkspaceBindingDigest = ""
			candidate.WorkspaceRelativeLocator = ""
			rebindTarget(candidate)
		}},
		{name: "node id", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.NodeID++ }},
		{name: "root id", mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.RootID = "root-aead-binding-other"
			rebindTarget(candidate)
		}},
		{name: "root locator digest", mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.RootLocatorDigest = digest("2")
			rebindTarget(candidate)
		}},
		{name: "semantic target digest", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.SemanticTargetDigest = digest("3") }},
		{name: "target object digest", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.TargetObjectDigest = digest("4") }},
		{name: "operation", mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.Operation = RecoveryOperationCreate
			candidate.ExpectedPriorKind = ExpectedTargetAbsent
			candidate.ExpectedPriorDigest = ""
			candidate.ExpectedPriorBytes = -1
		}},
		{name: "workspace binding digest", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.WorkspaceBindingDigest = digest("5") }},
		{name: "workspace relative locator", mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.WorkspaceRelativeLocator = "jobs/" + strings.Repeat("6", 32)
			rebindTarget(candidate)
		}},
		{name: "expected prior kind", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.ExpectedPriorKind = ExpectedTargetAbsent }},
		{name: "expected prior digest", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.ExpectedPriorDigest = digest("6") }},
		{name: "expected post digest", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.ExpectedPostIdentityDigest = digest("7") }},
		{name: "expected post bytes", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.ExpectedPostBytes++ }},
		{name: "expected prior bytes", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.ExpectedPriorBytes++ }},
		{name: "key version", materialVersion: 4, mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.TargetLocatorKeyVersion = 4 }},
		{name: "cipher version", mutate: func(candidate *TargetLocatorEnvelopeBinding) { candidate.TargetLocatorCipherVersion++ }},
	}
	baseBindingDigest := targetLocatorEnvelopeBindingDigest(binding)
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := binding
			testCase.mutate(&candidate)
			if targetLocatorEnvelopeBindingDigest(candidate) == baseBindingDigest {
				t.Fatalf("authenticated digest did not bind %s", testCase.name)
			}
			candidateMaterial := cloneDomainKeyMaterial(material)
			if testCase.materialVersion > 0 {
				candidateMaterial.Version = testCase.materialVersion
			}
			if _, err := OpenTargetLocatorEnvelope(candidateMaterial, candidate, sealed); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
				t.Fatalf("binding substitution error = %v, want ErrInvalidTargetLocatorEnvelope", err)
			}
		})
	}

	tamperedMaterial := cloneDomainKeyMaterial(material)
	tamperedMaterial.Key[0] ^= 0xff
	if _, err := OpenTargetLocatorEnvelope(tamperedMaterial, binding, sealed); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("wrong key material error = %v, want ErrInvalidTargetLocatorEnvelope", err)
	}
	tamperedMaterial = cloneDomainKeyMaterial(material)
	tamperedMaterial.State = backupasset.DomainKeyVerifyOnly
	if _, err := OpenTargetLocatorEnvelope(tamperedMaterial, binding, sealed); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("non-active key material error = %v, want ErrInvalidTargetLocatorEnvelope", err)
	}

	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, targetLocatorCiphertextPrefix))
	if err != nil || len(payload) == 0 {
		t.Fatalf("decode target locator ciphertext: %v", err)
	}
	payload[len(payload)-1] ^= 0xff
	tamperedCiphertext := targetLocatorCiphertextPrefix + base64.RawURLEncoding.EncodeToString(payload)
	if _, err := OpenTargetLocatorEnvelope(material, binding, tamperedCiphertext); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("ciphertext tamper error = %v, want ErrInvalidTargetLocatorEnvelope", err)
	}
}

func TestRecoveryVerifyOperationProductMatrix(t *testing.T) {
	itemType := reflect.TypeOf(model.BackupAssetRecoveryJobItem{})
	for _, forbidden := range []string{"VerifiedModifiedAt", "FidelityState"} {
		if _, found := itemType.FieldByName(forbidden); found {
			t.Fatalf("exact content verification product retains unsupported field %s", forbidden)
		}
	}
	t.Run("ordinary operation identity and bytes", testRecoveryB1E1OrdinaryOperationIdentityAndBytes)
	t.Run("fresh create and overwrite source", testRecoveryB1E1FreshCreateOverwriteSource)
	t.Run("per-operation source revalidation", TestRecoveryExecuteClaimRevalidatesPinnedSourcePerOperation)
	t.Run("skip source and target separation", testRecoveryB1E1SkipSourceTargetSeparation)
	t.Run("closed product and opaque revision", TestTargetVerifyClosedProductAndOpaqueRevision)
	t.Run("closed presence arms", TestTargetVerifyContractsClosePresenceArms)
	t.Run("uncertain observations fail closed", TestWorkerRestartAdoptionFailsClosedOnInvalidOrUncertainTargetObservation)
	t.Run("exact-mirror delete requires durable authority", TestRecoveryExactMirrorDeletePauseRequiresOrdinaryExecutionHistory)
	t.Run("exact-mirror delete commits absence chain", TestRecoveryExactMirrorSuccessfulDeleteProjectsAbsenceCheckpointAndChain)
	t.Run("exact-mirror multi-delete orders absence chain", TestRecoveryExactMirrorMultipleDeletesReuseConsumedSetAuthorityInSameExecution)
	t.Run("exact-mirror multi-delete reuses consumed authority after restart", TestRecoveryExactMirrorMultipleDeletesConsumeSetAuthorityOnceAcrossRestart)
	t.Run("exact-mirror consumed delete reconciles exact absence", TestRecoveryExactMirrorConsumedDeleteAuthorityReloadReconcilesAbsence)
	t.Run("exact-mirror multi-delete honors production PostgreSQL 000069", testRecoveryExactMirrorMultipleDeletesProductionPostgres069)
}

func testRecoveryB1E1OrdinaryOperationIdentityAndBytes(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	pointID := strings.Repeat("1", 32)
	operations := []RecoveryOperation{
		{
			Kind: RecoveryOperationCreate, TargetPathDigest: digest("1"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetAbsent},
			ExpectedPostIdentityDigest: digest("a"), ExpectedPostBytes: 11, ExpectedPriorBytes: -1,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("4"),
			}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: 11,
		},
		{
			Kind: RecoveryOperationOverwrite, TargetPathDigest: digest("2"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("b")},
			ExpectedPostIdentityDigest: digest("c"), ExpectedPostBytes: 13, ExpectedPriorBytes: 7,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("5"),
			}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: 13,
		},
		{
			Kind: RecoveryOperationSkip, TargetPathDigest: digest("3"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("d")},
			ExpectedPostIdentityDigest: digest("d"), ExpectedPostBytes: -1, ExpectedPriorBytes: 41,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("6"),
			}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: 17,
		},
	}
	for index := range operations {
		bindRecoveryOperationTargetForTest(
			t, TargetModeInPlace, "root-b1-e1", digest("9"),
			fmt.Sprintf("items/ordinary-%d", index), &operations[index],
		)
	}
	input := func() RecoveryOperationProductsInput {
		rows := make([]RecoveryOperation, len(operations))
		for index := range operations {
			rows[index] = cloneRecoveryOperation(operations[index])
		}
		return RecoveryOperationProductsInput{
			TargetMode: TargetModeInPlace, ConflictPolicy: ConflictOverwriteSelected,
			Operations: rows,
			Limits: RecoveryOperationLimits{
				MaxRows: 3, MaxItems: 3, MaxBytes: 41, MaxImpactRows: 3,
			},
		}
	}

	products, err := NewOperationProducts(input())
	if err != nil {
		t.Fatalf("ordinary operation product rejected: %v", err)
	}
	encoded, err := encodeRecoveryOperationRows(products.Rows)
	if err != nil {
		t.Fatalf("encode ordinary operation product: %v", err)
	}
	decoded, err := decodeRecoveryOperationRows(encoded)
	if err != nil {
		t.Fatalf("decode ordinary operation product: %v", err)
	}
	byKind := make(map[RecoveryOperationKind]RecoveryOperation, len(decoded))
	for _, operation := range decoded {
		byKind[operation.Kind] = operation
	}
	for _, want := range operations {
		got, found := byKind[want.Kind]
		if !found || got.ExpectedPrior != want.ExpectedPrior ||
			got.ExpectedPostIdentityDigest != want.ExpectedPostIdentityDigest ||
			got.ExpectedPostBytes != want.ExpectedPostBytes || got.ExpectedPriorBytes != want.ExpectedPriorBytes {
			t.Fatalf("round-trip %s identity/bytes=%+v, want %+v", want.Kind, got, want)
		}
	}

	invalid := []struct {
		name   string
		mutate func([]RecoveryOperation)
	}{
		{name: "create post digest", mutate: func(rows []RecoveryOperation) {
			rows[0].ExpectedPostIdentityDigest = strings.ToUpper(rows[0].ExpectedPostIdentityDigest)
		}},
		{name: "create post bytes", mutate: func(rows []RecoveryOperation) { rows[0].ExpectedPostBytes = -1 }},
		{name: "create prior-byte sentinel", mutate: func(rows []RecoveryOperation) { rows[0].ExpectedPriorBytes = 0 }},
		{name: "overwrite post digest", mutate: func(rows []RecoveryOperation) { rows[1].ExpectedPostIdentityDigest = "short" }},
		{name: "overwrite post bytes", mutate: func(rows []RecoveryOperation) { rows[1].ExpectedPostBytes = -1 }},
		{name: "overwrite prior bytes", mutate: func(rows []RecoveryOperation) { rows[1].ExpectedPriorBytes = -1 }},
		{name: "skip target identity", mutate: func(rows []RecoveryOperation) { rows[2].ExpectedPostIdentityDigest = digest("e") }},
		{name: "skip post-byte sentinel", mutate: func(rows []RecoveryOperation) { rows[2].ExpectedPostBytes = 17 }},
		{name: "skip prior-target bytes", mutate: func(rows []RecoveryOperation) { rows[2].ExpectedPriorBytes = -1 }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			candidate := input()
			test.mutate(candidate.Operations)
			if _, err := NewOperationProducts(candidate); !errors.Is(err, ErrInvalidRecoveryOperation) {
				t.Fatalf("invalid ordinary operation error=%v, want ErrInvalidRecoveryOperation", err)
			}
		})
	}
}

func TestRecoveryLocatorProductNoPlaintextLeak(t *testing.T) {
	const rawLocator = "recognizable/task-6/private-target-locator"
	item := model.BackupAssetRecoveryJobItem{EncryptedTargetRelativeLocator: rawLocator}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal job item: %v", err)
	}
	if strings.Contains(string(encoded), rawLocator) {
		t.Fatalf("job-item JSON leaked raw locator: %s", encoded)
	}

	bindingType := reflect.TypeOf(TargetLocatorEnvelopeBinding{})
	if _, found := bindingType.FieldByName("TargetObjectDigest"); !found {
		t.Fatal("no-leak product requires a distinct opaque TargetObjectDigest")
	}
	_, decodeErr := DecodeTargetLocatorEnvelope(rawLocator, TargetLocatorEnvelopeBinding{})
	if decodeErr == nil || strings.Contains(decodeErr.Error(), rawLocator) {
		t.Fatalf("locator decode error leaked plaintext: %v", decodeErr)
	}
}

func setRecoveryOperationLocatorProduct(t *testing.T, operation *RecoveryOperation, locator, semanticDigest string) {
	t.Helper()
	value := reflect.ValueOf(operation).Elem()
	locatorField := value.FieldByName("TargetRelativeLocator")
	semanticField := value.FieldByName("SemanticTargetDigest")
	if !locatorField.IsValid() || !semanticField.IsValid() {
		t.Fatal("RecoveryOperation is missing schema-v2 locator product fields")
	}
	locatorField.SetString(locator)
	semanticField.SetString(semanticDigest)
}

func recoveryOperationLocatorProduct(t *testing.T, operation RecoveryOperation) (string, string) {
	t.Helper()
	value := reflect.ValueOf(operation)
	locatorField := value.FieldByName("TargetRelativeLocator")
	semanticField := value.FieldByName("SemanticTargetDigest")
	if !locatorField.IsValid() || !semanticField.IsValid() {
		t.Fatal("RecoveryOperation is missing schema-v2 locator product fields")
	}
	return locatorField.String(), semanticField.String()
}

func TestContractSourceRevisionRequiresExactlyOneArm(t *testing.T) {
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	catalogGenerationID := strings.Repeat("1", 32)

	tests := []struct {
		name     string
		revision SourceRevision
		wantErr  bool
	}{
		{
			name: "immutable locator and manifest digests",
			revision: SourceRevision{
				Kind: SourceRevisionImmutable,
				Immutable: &ImmutableSourceRevision{
					LocatorDigest:  digest,
					ManifestDigest: digest,
				},
			},
		},
		{
			name: "mutable observation tuple",
			revision: SourceRevision{
				Kind: SourceRevisionObservation,
				MutableObservation: &ObservationRevision{
					SourceFingerprint:   digest,
					CatalogGenerationID: catalogGenerationID,
					ObservedAt:          now.Add(-time.Minute),
				},
			},
		},
		{
			name:     "no arm",
			revision: SourceRevision{Kind: SourceRevisionImmutable},
			wantErr:  true,
		},
		{
			name: "dual arms",
			revision: SourceRevision{
				Kind: SourceRevisionImmutable,
				Immutable: &ImmutableSourceRevision{
					LocatorDigest:  digest,
					ManifestDigest: digest,
				},
				MutableObservation: &ObservationRevision{
					SourceFingerprint:   digest,
					CatalogGenerationID: catalogGenerationID,
					ObservedAt:          now.Add(-time.Minute),
				},
			},
			wantErr: true,
		},
		{
			name: "unknown kind",
			revision: SourceRevision{
				Kind: SourceRevisionKind("latest"),
				Immutable: &ImmutableSourceRevision{
					LocatorDigest:  digest,
					ManifestDigest: digest,
				},
			},
			wantErr: true,
		},
		{
			name: "short immutable digest",
			revision: SourceRevision{
				Kind: SourceRevisionImmutable,
				Immutable: &ImmutableSourceRevision{
					LocatorDigest:  "short",
					ManifestDigest: digest,
				},
			},
			wantErr: true,
		},
		{
			name: "incomplete observation",
			revision: SourceRevision{
				Kind: SourceRevisionObservation,
				MutableObservation: &ObservationRevision{
					SourceFingerprint:   digest,
					CatalogGenerationID: catalogGenerationID,
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.revision.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestSourceLocatorDigestMatchesDesignGoldenVectors(t *testing.T) {
	tests := []struct {
		name            string
		repositoryID    string
		provider        backupasset.ProviderKind
		recoveryPointID string
		locator         string
		want            string
	}{
		{
			name:            "restic exact locator",
			repositoryID:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			provider:        backupasset.ProviderRestic,
			recoveryPointID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			locator:         "FAKE_LOCATOR_FOR_DIGEST_VECTOR_A",
			want:            "da078fcf22aa14d8b8119dfb57c133139dbf47fe009aad5fd8798b45a61ac0ad",
		},
		{
			name:            "rsync literal locator bytes",
			repositoryID:    "0123456789abcdef0123456789abcdef",
			provider:        backupasset.ProviderRsync,
			recoveryPointID: "fedcba9876543210fedcba9876543210",
			locator:         "FAKE_LOCATOR_FOR_DIGEST_VECTOR_B/with-%_\\-literal",
			want:            "4ac95bf188ec11e546e16ed9c970685b3a184dcded57cc3073fb1c060bda71f8",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := SourceLocatorDigest(
				testCase.repositoryID,
				testCase.provider,
				testCase.recoveryPointID,
				testCase.locator,
			)
			if err != nil {
				t.Fatalf("SourceLocatorDigest() error = %v", err)
			}
			if got != testCase.want {
				t.Fatalf("SourceLocatorDigest() = %s, want %s", got, testCase.want)
			}
		})
	}
}

func TestContractTargetBindingRequiresCanonicalServerDerivedPath(t *testing.T) {
	rootLocatorDigest := strings.Repeat("a", sha256DigestLength)
	validRelativePath := "jobs/job-1"
	pathDigest := mustTargetPathDigest(t, "root-a", rootLocatorDigest, validRelativePath)
	validBinding := func() TargetBinding {
		return TargetBinding{
			Mode: TargetModeIsolated, NodeID: 7, RootID: "root-a",
			EncryptedRelativePath: validRelativePath,
			RootLocatorDigest:     rootLocatorDigest, PathDigest: pathDigest,
			BaseNodeRevision: "node-revision-1", CredentialScopeRevision: "credential-revision-1",
			RootRevision: "root-revision-1", FilesystemRevision: "filesystem-revision-1",
		}
	}

	if err := validBinding().Validate(); err != nil {
		t.Fatalf("valid target binding rejected: %v", err)
	}

	invalidPaths := []string{
		"", ".", "..", "../escape", "a/../b", "a/./b", "/absolute", "a//b", "a/",
		`a\b`, `C:/windows`, `C:\windows`, `\\server\share`, "nul\x00byte", string([]byte{0xff}),
	}
	for index, relativePath := range invalidPaths {
		t.Run("invalid_path_"+strconv.Itoa(index), func(t *testing.T) {
			if _, err := TargetPathDigest("root-a", rootLocatorDigest, relativePath); !errors.Is(err, ErrInvalidTargetBinding) {
				t.Fatalf("TargetPathDigest(%q) error = %v, want ErrInvalidTargetBinding", relativePath, err)
			}
			binding := validBinding()
			binding.EncryptedRelativePath = relativePath
			if err := binding.Validate(); !errors.Is(err, ErrInvalidTargetBinding) {
				t.Fatalf("TargetBinding.Validate() path %q error = %v, want ErrInvalidTargetBinding", relativePath, err)
			}
		})
	}

	for name, mutate := range map[string]func(*TargetBinding){
		"caller path digest": func(binding *TargetBinding) { binding.PathDigest = strings.Repeat("b", sha256DigestLength) },
		"root id":            func(binding *TargetBinding) { binding.RootID = "root-b" },
		"root locator":       func(binding *TargetBinding) { binding.RootLocatorDigest = strings.Repeat("c", sha256DigestLength) },
	} {
		t.Run(name+" substitution", func(t *testing.T) {
			binding := validBinding()
			mutate(&binding)
			if err := binding.Validate(); !errors.Is(err, ErrInvalidTargetBinding) {
				t.Fatalf("TargetBinding.Validate() error = %v, want digest-bound rejection", err)
			}
		})
	}
}

func TestContractFrozenJobRejectsTargetSubstitution(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rootDigestA := strings.Repeat("a", sha256DigestLength)
	jobA := validFrozenJobBindingForTarget(t, now, "root-a", rootDigestA, "jobs/job-a")
	if err := jobA.ValidateAt(now); err != nil {
		t.Fatalf("valid frozen job binding rejected: %v", err)
	}

	for name, mutate := range map[string]func(*PreflightBinding){
		"node":                func(binding *PreflightBinding) { binding.TargetNodeID++ },
		"node revision":       func(binding *PreflightBinding) { binding.NodeRevision = "node-revision-2" },
		"root id":             func(binding *PreflightBinding) { binding.RootID = "root-b" },
		"root locator digest": func(binding *PreflightBinding) { binding.RootLocatorDigest = strings.Repeat("b", sha256DigestLength) },
		"path digest":         func(binding *PreflightBinding) { binding.PathDigest = strings.Repeat("c", sha256DigestLength) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := jobA
			mutate(&candidate.Preflight)
			if err := candidate.ValidateAt(now); !errors.Is(err, ErrInvalidFrozenJobBinding) {
				t.Fatalf("FrozenJobBinding.ValidateAt() error = %v, want target-substitution rejection", err)
			}
		})
	}

	jobB := validFrozenJobBindingForTarget(
		t, now, "root-b", strings.Repeat("b", sha256DigestLength), "jobs/job-b",
	)
	transplanted := jobA
	transplanted.Preflight = jobB.Preflight
	if err := transplanted.ValidateAt(now); !errors.Is(err, ErrInvalidFrozenJobBinding) {
		t.Fatalf("transplanted target-B preflight error = %v, want target-substitution rejection", err)
	}
}

func TestContractOpaqueAndTemporalProductsAreBounded(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	digest := strings.Repeat("a", 64)
	opaqueID := func(character string) string { return strings.Repeat(character, 32) }

	validSource := func(observedAt time.Time) SourceRevision {
		return SourceRevision{
			Kind: SourceRevisionObservation,
			MutableObservation: &ObservationRevision{
				SourceFingerprint:   digest,
				CatalogGenerationID: opaqueID("1"),
				ObservedAt:          observedAt,
			},
		}
	}
	validTarget := func() TargetBinding {
		relativePath := "jobs/job-1"
		return TargetBinding{
			Mode:                    TargetModeIsolated,
			NodeID:                  1,
			RootID:                  "root-1",
			EncryptedRelativePath:   relativePath,
			RootLocatorDigest:       digest,
			PathDigest:              mustTargetPathDigest(t, "root-1", digest, relativePath),
			BaseNodeRevision:        "target-v1",
			CredentialScopeRevision: "credential-v1",
			RootRevision:            "root-v1",
			FilesystemRevision:      "filesystem-v1",
		}
	}
	validPlan := func(observedAt time.Time) PlanBinding {
		return PlanBinding{
			SchemaVersion:        1,
			PlanDigest:           digest,
			SelectionDigest:      digest,
			RepositoryID:         opaqueID("2"),
			RecoveryPointID:      opaqueID("3"),
			SourceRevisionDigest: digest,
			SourceRevision:       validSource(observedAt),
			Target:               validTarget(),
			ConflictPolicy:       ConflictFailOnConflict,
			OperationSetDigest:   digest,
			DeleteSetDigest:      EmptyDeleteSetDigest,
			CapabilityRevision:   "capability-v1",
			SecurityDecision: SecurityDecision{
				Kind:             SecurityDecisionAllowClean,
				DecisionDigest:   digest,
				FindingSetDigest: digest,
				PolicyRevision:   "policy-v1",
			},
			PreflightRevision: "preflight-v1",
		}
	}
	validPreflight := func() PreflightBinding {
		target := validTarget()
		return PreflightBinding{
			ID:                   opaqueID("4"),
			Revision:             "preflight-v1",
			SourceRevisionDigest: digest,
			TargetNodeID:         target.NodeID,
			NodeRevision:         target.BaseNodeRevision,
			RootID:               target.RootID,
			RootLocatorDigest:    target.RootLocatorDigest,
			PathDigest:           target.PathDigest,
			TargetRevision:       "target-v1",
			CapabilityRevision:   "capability-v1",
			PolicyRevision:       "policy-v1",
			FindingSetDigest:     digest,
			OperationSetDigest:   digest,
			DeleteSetDigest:      EmptyDeleteSetDigest,
			EstimatedItems:       1,
			EstimatedBytes:       1,
			ExpiresAt:            now.Add(time.Hour),
		}
	}
	validAuthority := func() AuthorityBinding {
		return AuthorityBinding{
			GrantID:       opaqueID("5"),
			Category:      AuthorityWrite,
			BindingDigest: digest,
			ExpiresAt:     now.Add(30 * time.Minute),
			ConsumedAt:    now.Add(-time.Minute),
		}
	}

	t.Run("exact expiry equality fails closed at the injected clock", func(t *testing.T) {
		preflight := validPreflight()
		preflight.ExpiresAt = now
		if err := preflight.ValidateAt(now); err == nil {
			t.Fatal("preflight accepted its exact expiry timestamp")
		}

		authority := validAuthority()
		authority.ExpiresAt = now
		if err := authority.ValidateAt(now); err == nil {
			t.Fatal("authority accepted its exact expiry timestamp")
		}

		authority = validAuthority()
		authority.ExpiresAt = now.Add(time.Minute)
		authority.ConsumedAt = authority.ExpiresAt
		if err := authority.ValidateAt(now); err == nil {
			t.Fatal("authority accepted consumption at its exact expiry timestamp")
		}
	})

	t.Run("source observation requires opaque catalog generation ID", func(t *testing.T) {
		revision := validSource(now.Add(-time.Minute))
		revision.MutableObservation.CatalogGenerationID = "generation-1"
		if err := revision.Validate(); err == nil {
			t.Fatal("source observation accepted an unbounded non-opaque catalog generation ID")
		}
	})

	t.Run("target identifiers and revisions are bounded", func(t *testing.T) {
		for _, mutate := range []func(*TargetBinding){
			func(binding *TargetBinding) { binding.RootID = strings.Repeat("r", 33) },
			func(binding *TargetBinding) { binding.BaseNodeRevision = strings.Repeat("r", 65) },
			func(binding *TargetBinding) { binding.CredentialScopeRevision = strings.Repeat("r", 65) },
			func(binding *TargetBinding) { binding.RootRevision = strings.Repeat("r", 65) },
			func(binding *TargetBinding) { binding.FilesystemRevision = strings.Repeat("r", 65) },
		} {
			candidate := validTarget()
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("target accepted an overlong opaque identifier or revision")
			}
		}
	})

	t.Run("plan IDs and revisions are bounded", func(t *testing.T) {
		mutations := []func(*PlanBinding){
			func(binding *PlanBinding) { binding.RepositoryID = strings.Repeat("G", 32) },
			func(binding *PlanBinding) { binding.RecoveryPointID = "point-1" },
			func(binding *PlanBinding) { binding.CapabilityRevision = strings.Repeat("r", 65) },
			func(binding *PlanBinding) { binding.PreflightRevision = strings.Repeat("r", 65) },
			func(binding *PlanBinding) { binding.SecurityDecision.PolicyRevision = strings.Repeat("r", 65) },
		}
		for _, mutate := range mutations {
			candidate := validPlan(now.Add(-time.Minute))
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("plan accepted an invalid opaque ID or overlong revision")
			}
		}
	})

	t.Run("preflight accepts bounded opaque revisions", func(t *testing.T) {
		if err := validPreflight().ValidateAt(now); err != nil {
			t.Fatalf("bounded opaque preflight revisions were rejected: %v", err)
		}
	})

	t.Run("preflight ID and revision bounds are closed", func(t *testing.T) {
		for _, mutate := range []func(*PreflightBinding){
			func(binding *PreflightBinding) { binding.ID = "preflight-1" },
			func(binding *PreflightBinding) { binding.Revision = strings.Repeat("r", 65) },
			func(binding *PreflightBinding) { binding.TargetRevision = strings.Repeat("r", 65) },
			func(binding *PreflightBinding) { binding.CapabilityRevision = strings.Repeat("r", 65) },
			func(binding *PreflightBinding) { binding.PolicyRevision = strings.Repeat("r", 65) },
		} {
			candidate := validPreflight()
			mutate(&candidate)
			if err := candidate.ValidateAt(now); err == nil {
				t.Fatal("preflight accepted an invalid opaque ID or overlong revision")
			}
		}
	})

	t.Run("authority grant ID is opaque", func(t *testing.T) {
		candidate := validAuthority()
		candidate.GrantID = "grant-1"
		if err := candidate.ValidateAt(now); err == nil {
			t.Fatal("authority accepted a non-opaque grant ID")
		}
	})

	t.Run("future observation is rejected at plan clock boundary", func(t *testing.T) {
		plan := RecoveryPlan{
			Binding:            validPlan(now.Add(time.Minute)),
			PreflightExpiresAt: now.Add(time.Hour),
		}
		if err := plan.ValidateAt(now); err == nil {
			t.Fatal("recovery plan accepted a future-dated source observation")
		}
	})

	t.Run("future observation is rejected at frozen job boundary", func(t *testing.T) {
		plan := validPlan(now.Add(time.Minute))
		preflight := validPreflight()
		preflight.SourceRevisionDigest = plan.SourceRevisionDigest
		preflight.TargetRevision = plan.Target.BaseNodeRevision
		preflight.CapabilityRevision = plan.CapabilityRevision
		preflight.PolicyRevision = plan.SecurityDecision.PolicyRevision
		preflight.FindingSetDigest = plan.SecurityDecision.FindingSetDigest
		preflight.OperationSetDigest = plan.OperationSetDigest
		preflight.DeleteSetDigest = plan.DeleteSetDigest
		job := FrozenJobBinding{Plan: plan, Preflight: preflight, Authority: validAuthority()}
		if err := job.ValidateAt(now); err == nil {
			t.Fatal("frozen job accepted a future-dated source observation")
		}
	})

	t.Run("checkpoint copied IDs and revisions are bounded", func(t *testing.T) {
		job := FrozenJobBinding{
			Plan:      validPlan(now.Add(-time.Minute)),
			Preflight: validPreflight(),
			Authority: validAuthority(),
		}
		checkpoint := FrozenCheckpointBinding{
			PlanBindingDigest:        job.Plan.PlanDigest,
			SourceRevisionDigest:     job.Plan.SourceRevisionDigest,
			PreflightID:              job.Preflight.ID,
			PreflightRevision:        job.Preflight.Revision,
			PreflightExpiresAt:       job.Preflight.ExpiresAt,
			SecurityDecision:         job.Plan.SecurityDecision.Kind,
			SecurityDecisionDigest:   job.Plan.SecurityDecision.DecisionDigest,
			SecurityFindingSetDigest: job.Plan.SecurityDecision.FindingSetDigest,
			SecurityPolicyRevision:   job.Plan.SecurityDecision.PolicyRevision,
			AuthorityGrantID:         job.Authority.GrantID,
			JobAuthorityCategory:     job.Authority.Category,
			AuthorityBindingDigest:   job.Authority.BindingDigest,
			AuthorityExpiresAt:       job.Authority.ExpiresAt,
		}
		checkpoint.PreflightID = "preflight-1"
		if err := checkpoint.ValidateAgainst(job); err == nil {
			t.Fatal("checkpoint accepted a non-opaque preflight ID")
		}
	})
}

func TestContractPlanRejectsUnknownAndContradictoryProducts(t *testing.T) {
	now := time.Now().UTC()
	digest := strings.Repeat("b", 64)
	opaqueID := func(character string) string { return strings.Repeat(character, 32) }

	validPlan := func() RecoveryPlan {
		relativePath := "jobs/job-1"
		return RecoveryPlan{
			Binding: PlanBinding{
				SchemaVersion:        1,
				PlanDigest:           digest,
				SelectionDigest:      digest,
				RepositoryID:         opaqueID("1"),
				RecoveryPointID:      opaqueID("2"),
				SourceRevisionDigest: digest,
				SourceRevision: SourceRevision{
					Kind: SourceRevisionObservation,
					MutableObservation: &ObservationRevision{
						SourceFingerprint:   digest,
						CatalogGenerationID: opaqueID("3"),
						ObservedAt:          now.Add(-time.Minute),
					},
				},
				Target: TargetBinding{
					Mode:                    TargetModeIsolated,
					NodeID:                  1,
					RootID:                  "root-1",
					EncryptedRelativePath:   relativePath,
					RootLocatorDigest:       digest,
					PathDigest:              mustTargetPathDigest(t, "root-1", digest, relativePath),
					BaseNodeRevision:        "node-revision-1",
					CredentialScopeRevision: "credential-revision-1",
					RootRevision:            "root-revision-1",
					FilesystemRevision:      "filesystem-revision-1",
				},
				ConflictPolicy:     ConflictFailOnConflict,
				OperationSetDigest: digest,
				DeleteSetDigest:    EmptyDeleteSetDigest,
				CapabilityRevision: "capability-revision-1",
				SecurityDecision: SecurityDecision{
					Kind:             SecurityDecisionAllowClean,
					DecisionDigest:   digest,
					FindingSetDigest: digest,
					PolicyRevision:   "policy-revision-1",
				},
				PreflightRevision: "preflight-revision-1",
			},
			PreflightExpiresAt: now.Add(time.Minute),
		}
	}

	tests := []struct {
		name    string
		plan    RecoveryPlan
		wantErr bool
	}{
		{name: "isolated fail on conflict", plan: validPlan()},
		{
			name: "in place exact mirror with delete set",
			plan: func() RecoveryPlan {
				plan := validPlan()
				plan.Binding.Target.Mode = TargetModeInPlace
				plan.Binding.ConflictPolicy = ConflictExactMirror
				plan.Binding.DeleteSetDigest = digest
				return plan
			}(),
		},
		{
			name: "unknown target mode",
			plan: func() RecoveryPlan {
				plan := validPlan()
				plan.Binding.Target.Mode = TargetMode("replace_everything")
				return plan
			}(),
			wantErr: true,
		},
		{
			name: "unknown conflict policy",
			plan: func() RecoveryPlan {
				plan := validPlan()
				plan.Binding.ConflictPolicy = ConflictPolicy("merge")
				return plan
			}(),
			wantErr: true,
		},
		{
			name: "isolated exact mirror",
			plan: func() RecoveryPlan {
				plan := validPlan()
				plan.Binding.ConflictPolicy = ConflictExactMirror
				plan.Binding.DeleteSetDigest = digest
				return plan
			}(),
			wantErr: true,
		},
		{
			name: "delete set outside exact mirror",
			plan: func() RecoveryPlan {
				plan := validPlan()
				plan.Binding.DeleteSetDigest = digest
				return plan
			}(),
			wantErr: true,
		},
		{
			name: "short operation digest",
			plan: func() RecoveryPlan {
				plan := validPlan()
				plan.Binding.OperationSetDigest = "short"
				return plan
			}(),
			wantErr: true,
		},
		{
			name: "expired preflight",
			plan: func() RecoveryPlan {
				plan := validPlan()
				plan.PreflightExpiresAt = now
				return plan
			}(),
			wantErr: true,
		},
		{
			name: "observation after preflight expiry",
			plan: func() RecoveryPlan {
				plan := validPlan()
				plan.Binding.SourceRevision.MutableObservation.ObservedAt = now.Add(2 * time.Minute)
				return plan
			}(),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.plan.ValidateAt(now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateAt() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

// Post-GREEN regression coverage: the original Task 1 contract RED was not observed.
func TestRecoveryReviewF1TargetModeAndOperationDigests(t *testing.T) {
	TestContractPlanRejectsUnknownAndContradictoryProducts(t)
}

func TestContractAuthorityAndSecurityDecisionAreClosed(t *testing.T) {
	digest := strings.Repeat("c", 64)

	for _, category := range []AuthorityCategory{AuthorityWrite, AuthorityExactMirrorDelete} {
		if err := category.Validate(); err != nil {
			t.Fatalf("authority category %q rejected: %v", category, err)
		}
	}
	if err := AuthorityCategory("delete").Validate(); err == nil {
		t.Fatal("unknown authority category was accepted")
	}

	tests := []struct {
		name     string
		decision SecurityDecision
		wantErr  bool
	}{
		{
			name: "allow clean",
			decision: SecurityDecision{
				Kind:             SecurityDecisionAllowClean,
				DecisionDigest:   digest,
				FindingSetDigest: digest,
				PolicyRevision:   "policy-revision-1",
			},
		},
		{
			name: "block",
			decision: SecurityDecision{
				Kind:             SecurityDecisionBlock,
				DecisionDigest:   digest,
				FindingSetDigest: digest,
				PolicyRevision:   "policy-revision-1",
			},
		},
		{
			name: "admin override",
			decision: SecurityDecision{
				Kind:                  SecurityDecisionAdminOverride,
				DecisionDigest:        digest,
				FindingSetDigest:      digest,
				PolicyRevision:        "policy-revision-1",
				OverrideBindingDigest: digest,
			},
		},
		{
			name: "unknown decision",
			decision: SecurityDecision{
				Kind:             SecurityDecisionKind("ignore"),
				DecisionDigest:   digest,
				FindingSetDigest: digest,
				PolicyRevision:   "policy-revision-1",
			},
			wantErr: true,
		},
		{
			name: "override without binding",
			decision: SecurityDecision{
				Kind:             SecurityDecisionAdminOverride,
				DecisionDigest:   digest,
				FindingSetDigest: digest,
				PolicyRevision:   "policy-revision-1",
			},
			wantErr: true,
		},
		{
			name: "clean with override binding",
			decision: SecurityDecision{
				Kind:                  SecurityDecisionAllowClean,
				DecisionDigest:        digest,
				FindingSetDigest:      digest,
				PolicyRevision:        "policy-revision-1",
				OverrideBindingDigest: digest,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.decision.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestContractFrozenJobAndCheckpointAuthorityParity(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	digest := strings.Repeat("d", 64)
	relativePath := "jobs/job-1"
	pathDigest := mustTargetPathDigest(t, "root-1", digest, relativePath)
	plan := PlanBinding{
		SchemaVersion:        1,
		PlanDigest:           digest,
		SelectionDigest:      digest,
		RepositoryID:         strings.Repeat("1", 32),
		RecoveryPointID:      strings.Repeat("2", 32),
		SourceRevisionDigest: digest,
		SourceRevision: SourceRevision{
			Kind: SourceRevisionImmutable,
			Immutable: &ImmutableSourceRevision{
				LocatorDigest:  digest,
				ManifestDigest: digest,
			},
		},
		Target: TargetBinding{
			Mode:                    TargetModeIsolated,
			NodeID:                  1,
			RootID:                  "root-1",
			EncryptedRelativePath:   relativePath,
			RootLocatorDigest:       digest,
			PathDigest:              pathDigest,
			BaseNodeRevision:        digest,
			CredentialScopeRevision: "credential-revision-1",
			RootRevision:            "root-revision-1",
			FilesystemRevision:      "filesystem-revision-1",
		},
		ConflictPolicy:     ConflictFailOnConflict,
		OperationSetDigest: digest,
		DeleteSetDigest:    EmptyDeleteSetDigest,
		CapabilityRevision: digest,
		SecurityDecision: SecurityDecision{
			Kind:             SecurityDecisionAllowClean,
			DecisionDigest:   digest,
			FindingSetDigest: digest,
			PolicyRevision:   digest,
		},
		PreflightRevision: "preflight-revision-1",
	}
	job := FrozenJobBinding{
		Plan: plan,
		Preflight: PreflightBinding{
			ID:                   strings.Repeat("3", 32),
			Revision:             plan.PreflightRevision,
			SourceRevisionDigest: plan.SourceRevisionDigest,
			TargetNodeID:         plan.Target.NodeID,
			NodeRevision:         plan.Target.BaseNodeRevision,
			RootID:               plan.Target.RootID,
			RootLocatorDigest:    plan.Target.RootLocatorDigest,
			PathDigest:           plan.Target.PathDigest,
			TargetRevision:       "target-revision-1",
			CapabilityRevision:   plan.CapabilityRevision,
			PolicyRevision:       plan.SecurityDecision.PolicyRevision,
			FindingSetDigest:     plan.SecurityDecision.FindingSetDigest,
			OperationSetDigest:   plan.OperationSetDigest,
			DeleteSetDigest:      plan.DeleteSetDigest,
			EstimatedItems:       1,
			EstimatedBytes:       1,
			ExpiresAt:            now.Add(time.Hour),
		},
		Authority: AuthorityBinding{
			GrantID:       strings.Repeat("4", 32),
			Category:      AuthorityWrite,
			BindingDigest: digest,
			ExpiresAt:     now.Add(30 * time.Minute),
			ConsumedAt:    now.Add(-time.Minute),
		},
	}
	if err := job.ValidateAt(now); err != nil {
		t.Fatalf("valid frozen job binding rejected: %v", err)
	}
	blockedJob := job
	blockedJob.Plan.SecurityDecision.Kind = SecurityDecisionBlock
	if err := blockedJob.ValidateAt(now); err == nil {
		t.Fatal("frozen job accepted a blocked security decision")
	}

	checkpoint := FrozenCheckpointBinding{
		PlanBindingDigest:        job.Plan.PlanDigest,
		SourceRevisionDigest:     job.Plan.SourceRevisionDigest,
		PreflightID:              job.Preflight.ID,
		PreflightRevision:        job.Preflight.Revision,
		PreflightExpiresAt:       job.Preflight.ExpiresAt,
		SecurityDecision:         job.Plan.SecurityDecision.Kind,
		SecurityDecisionDigest:   job.Plan.SecurityDecision.DecisionDigest,
		SecurityFindingSetDigest: job.Plan.SecurityDecision.FindingSetDigest,
		SecurityPolicyRevision:   job.Plan.SecurityDecision.PolicyRevision,
		AuthorityGrantID:         job.Authority.GrantID,
		JobAuthorityCategory:     job.Authority.Category,
		AuthorityBindingDigest:   job.Authority.BindingDigest,
		AuthorityExpiresAt:       job.Authority.ExpiresAt,
	}
	if err := checkpoint.ValidateAgainst(job); err != nil {
		t.Fatalf("valid frozen checkpoint binding rejected: %v", err)
	}

	checkpoint.SecurityDecisionDigest = strings.Repeat("e", 64)
	if err := checkpoint.ValidateAgainst(job); err == nil {
		t.Fatal("checkpoint accepted a rewritten security-decision digest")
	}

	job.Preflight.SourceRevisionDigest = strings.Repeat("f", 64)
	if err := job.ValidateAt(now); err == nil {
		t.Fatal("job accepted source binding that diverges from its frozen plan")
	}
}

func mustTargetPathDigest(t *testing.T, rootID, rootLocatorDigest, relativePath string) string {
	t.Helper()
	digest, err := TargetPathDigest(rootID, rootLocatorDigest, relativePath)
	if err != nil {
		t.Fatalf("TargetPathDigest(%q, %q) error = %v", rootID, relativePath, err)
	}
	return digest
}

func bindRecoveryOperationTargetForTest(
	t *testing.T,
	mode TargetMode,
	rootID,
	rootLocatorDigest,
	relativeLocator string,
	operation *RecoveryOperation,
) {
	t.Helper()
	semanticDigest, err := SemanticTargetDigest(mode, rootID, rootLocatorDigest, relativeLocator)
	if err != nil {
		t.Fatalf("derive semantic target digest: %v", err)
	}
	operation.TargetRelativeLocator = relativeLocator
	operation.SemanticTargetDigest = semanticDigest
}

func completeTargetLocatorBindingForTest(
	t *testing.T,
	binding TargetLocatorEnvelopeBinding,
	relativeLocator string,
) TargetLocatorEnvelopeBinding {
	t.Helper()
	binding.CodecVersion = targetLocatorEnvelopeSchemaVersion
	semanticDigest, err := SemanticTargetDigest(
		binding.TargetMode,
		binding.RootID,
		binding.RootLocatorDigest,
		relativeLocator,
	)
	if err != nil {
		t.Fatalf("derive target locator semantic digest: %v", err)
	}
	binding.SemanticTargetDigest = semanticDigest
	if binding.Operation != RecoveryOperationDelete {
		if binding.PlanItemID == "" {
			binding.PlanItemID = strings.Repeat("4", 32)
		}
		if binding.SourceRecoveryPointID == "" {
			binding.SourceRecoveryPointID = strings.Repeat("5", 32)
		}
		if binding.SourceEntryID == "" {
			binding.SourceEntryID = strings.Repeat("6", sha256DigestLength)
		}
	}
	return binding
}

func validFrozenJobBindingForTarget(
	t *testing.T,
	now time.Time,
	rootID, rootLocatorDigest, relativePath string,
) FrozenJobBinding {
	t.Helper()
	digest := strings.Repeat("d", sha256DigestLength)
	pathDigest := mustTargetPathDigest(t, rootID, rootLocatorDigest, relativePath)
	plan := PlanBinding{
		SchemaVersion: 1, PlanDigest: digest, SelectionDigest: digest,
		RepositoryID: strings.Repeat("1", 32), RecoveryPointID: strings.Repeat("2", 32),
		SourceRevisionDigest: digest,
		SourceRevision: SourceRevision{
			Kind:      SourceRevisionImmutable,
			Immutable: &ImmutableSourceRevision{LocatorDigest: digest, ManifestDigest: digest},
		},
		Target: TargetBinding{
			Mode: TargetModeIsolated, NodeID: 7, RootID: rootID,
			EncryptedRelativePath: relativePath,
			RootLocatorDigest:     rootLocatorDigest, PathDigest: pathDigest,
			BaseNodeRevision: "node-revision-1", CredentialScopeRevision: "credential-revision-1",
			RootRevision: "root-revision-1", FilesystemRevision: "filesystem-revision-1",
		},
		ConflictPolicy: ConflictFailOnConflict, OperationSetDigest: digest,
		DeleteSetDigest: EmptyDeleteSetDigest, CapabilityRevision: "capability-revision-1",
		SecurityDecision: SecurityDecision{
			Kind: SecurityDecisionAllowClean, DecisionDigest: digest,
			FindingSetDigest: digest, PolicyRevision: "policy-revision-1",
		},
		PreflightRevision: "preflight-revision-1",
	}
	return FrozenJobBinding{
		Plan: plan,
		Preflight: PreflightBinding{
			ID: strings.Repeat("3", 32), Revision: plan.PreflightRevision,
			SourceRevisionDigest: plan.SourceRevisionDigest,
			TargetNodeID:         plan.Target.NodeID, NodeRevision: plan.Target.BaseNodeRevision,
			RootID: rootID, RootLocatorDigest: rootLocatorDigest, PathDigest: pathDigest,
			TargetRevision: "target-revision-1", CapabilityRevision: plan.CapabilityRevision,
			PolicyRevision:     plan.SecurityDecision.PolicyRevision,
			FindingSetDigest:   plan.SecurityDecision.FindingSetDigest,
			OperationSetDigest: plan.OperationSetDigest, DeleteSetDigest: plan.DeleteSetDigest,
			EstimatedItems: 1, EstimatedBytes: 1, ExpiresAt: now.Add(time.Hour),
		},
		Authority: AuthorityBinding{
			GrantID: strings.Repeat("4", 32), Category: AuthorityWrite,
			BindingDigest: digest, ExpiresAt: now.Add(30 * time.Minute), ConsumedAt: now.Add(-time.Minute),
		},
	}
}

func TestContractExactMirrorDeleteAuthorityConsumption(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	digest := strings.Repeat("a", 64)
	required := DeleteAuthorityCheckpointBinding{
		CheckpointID:           strings.Repeat("1", 32),
		JobID:                  strings.Repeat("2", 32),
		AttemptID:              strings.Repeat("3", 32),
		DeleteSetDigest:        digest,
		TargetRevision:         "target-chain-revision-1",
		NodeRevision:           "node-revision-1",
		RootRevision:           "root-revision-1",
		AttemptFence:           7,
		NodeFence:              11,
		AuthorizationExpiresAt: now.Add(10 * time.Minute),
	}
	grant := ExactMirrorDeleteGrantBinding{
		GrantID:         strings.Repeat("4", 32),
		Category:        AuthorityExactMirrorDelete,
		BindingDigest:   digest,
		JobID:           required.JobID,
		CheckpointID:    required.CheckpointID,
		AttemptID:       required.AttemptID,
		DeleteSetDigest: required.DeleteSetDigest,
		TargetRevision:  required.TargetRevision,
		AttemptFence:    required.AttemptFence,
		NodeFence:       required.NodeFence,
		ExpiresAt:       now.Add(5 * time.Minute),
		ConsumedAt:      now.Add(-time.Second),
	}
	valid := ConsumedDeleteAuthorityBinding{
		CheckpointID: strings.Repeat("5", 32),
		Required:     required,
		Grant:        grant,
	}
	if err := valid.ValidateAt(now); err != nil {
		t.Fatalf("valid exact-mirror delete authority consumption rejected: %v", err)
	}

	invalid := []struct {
		name   string
		mutate func(*ConsumedDeleteAuthorityBinding)
	}{
		{name: "missing grant", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant = ExactMirrorDeleteGrantBinding{}
		}},
		{name: "wrong category", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.Category = AuthorityWrite
		}},
		{name: "wrong job", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.JobID = strings.Repeat("6", 32)
		}},
		{name: "wrong required checkpoint", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.CheckpointID = strings.Repeat("6", 32)
		}},
		{name: "wrong delete set", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.DeleteSetDigest = strings.Repeat("b", 64)
		}},
		{name: "wrong attempt", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.AttemptID = strings.Repeat("6", 32)
		}},
		{name: "wrong attempt fence", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.AttemptFence++
		}},
		{name: "wrong node fence", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.NodeFence++
		}},
		{name: "wrong current target", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.TargetRevision = "target-chain-revision-2"
		}},
		{name: "expired grant", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.ExpiresAt = now
		}},
		{name: "revoked grant", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			revokedAt := now.Add(-2 * time.Second)
			binding.Grant.RevokedAt = &revokedAt
		}},
		{name: "unconsumed grant", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.ConsumedAt = time.Time{}
		}},
		{name: "future consumption", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.ConsumedAt = now.Add(time.Second)
		}},
		{name: "expired checkpoint deadline", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Required.AuthorizationExpiresAt = now
		}},
		{name: "missing node revision", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Required.NodeRevision = ""
		}},
		{name: "missing root revision", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Required.RootRevision = ""
		}},
		{name: "same checkpoint reused as consumed", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.CheckpointID = binding.Required.CheckpointID
		}},
		{name: "oversized target revision", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Required.TargetRevision = strings.Repeat("r", opaqueRevisionMax+1)
			binding.Grant.TargetRevision = binding.Required.TargetRevision
		}},
		{name: "malformed binding digest", mutate: func(binding *ConsumedDeleteAuthorityBinding) {
			binding.Grant.BindingDigest = strings.Repeat("A", 64)
		}},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := valid
			testCase.mutate(&candidate)
			if err := candidate.ValidateAt(now); err == nil {
				t.Fatal("invalid exact-mirror delete authority consumption was accepted")
			}
		})
	}
}

func TestContractPublicationAndDeadlineIntegrity(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	markerDigest := strings.Repeat("a", 64)
	publication := ResultPublicationBinding{
		TargetMode:                        TargetModeIsolated,
		JobState:                          JobStateSucceeded,
		WorkspacePhase:                    WorkspacePhasePublished,
		WorkspaceMarkerBindingDigest:      markerDigest,
		ResultSetMarkerBindingDigest:      markerDigest,
		WorkspacePlaintextDeadline:        now.Add(2 * time.Hour),
		InitialResultPlaintextDeadline:    now.Add(2 * time.Hour),
		ResultPlaintextRetentionHardLimit: now.Add(3 * time.Hour),
	}
	if err := publication.ValidateAt(now); err != nil {
		t.Fatalf("valid result publication rejected: %v", err)
	}
	if !publication.CanRetainUntil(now.Add(150 * time.Minute)) {
		t.Fatal("valid plaintext retention extension within the hard limit was rejected")
	}

	invalid := []struct {
		name   string
		mutate func(*ResultPublicationBinding)
	}{
		{name: "in place target", mutate: func(binding *ResultPublicationBinding) {
			binding.TargetMode = TargetModeInPlace
		}},
		{name: "nonterminal job", mutate: func(binding *ResultPublicationBinding) {
			binding.JobState = JobStateVerifying
		}},
		{name: "unpublished workspace", mutate: func(binding *ResultPublicationBinding) {
			binding.WorkspacePhase = WorkspacePhaseSealed
		}},
		{name: "active attempt", mutate: func(binding *ResultPublicationBinding) {
			binding.HasActiveAttempt = true
		}},
		{name: "marker mismatch", mutate: func(binding *ResultPublicationBinding) {
			binding.ResultSetMarkerBindingDigest = strings.Repeat("b", 64)
		}},
		{name: "deadline mismatch", mutate: func(binding *ResultPublicationBinding) {
			binding.InitialResultPlaintextDeadline = binding.InitialResultPlaintextDeadline.Add(time.Second)
		}},
		{name: "expired workspace", mutate: func(binding *ResultPublicationBinding) {
			binding.WorkspacePlaintextDeadline = now
			binding.InitialResultPlaintextDeadline = now
		}},
		{name: "hard limit before plaintext deadline", mutate: func(binding *ResultPublicationBinding) {
			binding.ResultPlaintextRetentionHardLimit = binding.InitialResultPlaintextDeadline.Add(-time.Second)
		}},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := publication
			testCase.mutate(&candidate)
			if err := candidate.ValidateAt(now); err == nil {
				t.Fatal("invalid result publication was accepted")
			}
		})
	}

	for _, deadline := range []time.Time{
		publication.InitialResultPlaintextDeadline,
		publication.InitialResultPlaintextDeadline.Add(-time.Second),
		publication.ResultPlaintextRetentionHardLimit.Add(time.Second),
	} {
		if publication.CanRetainUntil(deadline) {
			t.Fatalf("invalid plaintext retention deadline %s was accepted", deadline)
		}
	}
}

func TestContractRecoveryResultContentClassificationAndAuthority(t *testing.T) {
	classification := RecoveryResultClassificationBinding{
		Kind:           RecoveryResultClassificationNonSecret,
		Revision:       1,
		SourceRevision: 1,
	}
	valid := RecoveryResultContentBinding{
		SessionRole:          "admin",
		OwnerMatches:         true,
		StepUpAction:         "recovery.result_download",
		HasStepUpProof:       true,
		TargetMode:           TargetModeIsolated,
		JobState:             JobStateSucceeded,
		WorkspacePhase:       WorkspacePhasePublished,
		ResultSetState:       ResultSetStateReady,
		SecurityDecision:     SecurityDecisionAllowClean,
		AuthorityCategory:    AuthorityWrite,
		ResultClassification: classification,
		GrantClassification:  classification,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid RecoveryResult Content binding rejected: %v", err)
	}
	for _, kind := range []RecoveryResultClassification{
		RecoveryResultClassificationNonSecret,
		RecoveryResultClassificationSecret,
		RecoveryResultClassificationUnknown,
	} {
		candidate := valid
		candidate.ResultClassification.Kind = kind
		candidate.GrantClassification.Kind = kind
		if err := candidate.Validate(); err != nil {
			t.Fatalf("valid RecoveryResult classification %q rejected: %v", kind, err)
		}
	}

	invalid := []struct {
		name   string
		mutate func(*RecoveryResultContentBinding)
	}{
		{name: "operator session", mutate: func(binding *RecoveryResultContentBinding) {
			binding.SessionRole = "operator"
		}},
		{name: "wrong owner", mutate: func(binding *RecoveryResultContentBinding) {
			binding.OwnerMatches = false
		}},
		{name: "wrong step up", mutate: func(binding *RecoveryResultContentBinding) {
			binding.StepUpAction = "asset.download"
		}},
		{name: "missing step up proof", mutate: func(binding *RecoveryResultContentBinding) {
			binding.HasStepUpProof = false
		}},
		{name: "in place job", mutate: func(binding *RecoveryResultContentBinding) {
			binding.TargetMode = TargetModeInPlace
		}},
		{name: "nonterminal job", mutate: func(binding *RecoveryResultContentBinding) {
			binding.JobState = JobStateVerifying
		}},
		{name: "unpublished job", mutate: func(binding *RecoveryResultContentBinding) {
			binding.WorkspacePhase = WorkspacePhaseSealed
		}},
		{name: "nonready result set", mutate: func(binding *RecoveryResultContentBinding) {
			binding.ResultSetState = ResultSetStateRevoking
		}},
		{name: "blocked security decision", mutate: func(binding *RecoveryResultContentBinding) {
			binding.SecurityDecision = SecurityDecisionBlock
		}},
		{name: "delete authority", mutate: func(binding *RecoveryResultContentBinding) {
			binding.AuthorityCategory = AuthorityExactMirrorDelete
		}},
		{name: "classification kind mismatch", mutate: func(binding *RecoveryResultContentBinding) {
			binding.GrantClassification.Kind = RecoveryResultClassificationSecret
		}},
		{name: "classification revision mismatch", mutate: func(binding *RecoveryResultContentBinding) {
			binding.GrantClassification.Revision++
		}},
		{name: "classification source revision mismatch", mutate: func(binding *RecoveryResultContentBinding) {
			binding.GrantClassification.SourceRevision++
		}},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := valid
			testCase.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid RecoveryResult Content binding was accepted")
			}
		})
	}
}

func TestSelectionCanonicalizesDistinctRefsAndKeepsSourceAuthorityOpaque(t *testing.T) {
	repositoryID := strings.Repeat("a", 32)
	recoveryPointID := strings.Repeat("b", 32)
	catalogGenerationID := strings.Repeat("c", 32)
	firstEntryID := strings.Repeat("1", 64)
	secondEntryID := strings.Repeat("2", 64)
	fakeLocator := "FAKE_RECOVERY_LOCATOR_FOR_TEST_ONLY"
	manifestDigest := strings.Repeat("d", 64)

	locatorDigest, err := SourceLocatorDigest(repositoryID, backupasset.ProviderRestic, recoveryPointID, fakeLocator)
	if err != nil {
		t.Fatalf("SourceLocatorDigest() error = %v", err)
	}
	plain := sha256.Sum256([]byte(fakeLocator))
	if locatorDigest == hex.EncodeToString(plain[:]) {
		t.Fatal("source locator digest was not domain-separated")
	}

	selection, err := NewExactSelection(ExactSelectionInput{
		RepositoryID:        repositoryID,
		RecoveryPointID:     recoveryPointID,
		CatalogGenerationID: catalogGenerationID,
		AssetRefs: []backupasset.AssetRef{
			{RecoveryPointID: recoveryPointID, EntryID: secondEntryID},
			{RecoveryPointID: recoveryPointID, EntryID: firstEntryID},
		},
		SourceRevision: SourceRevision{
			Kind: SourceRevisionImmutable,
			Immutable: &ImmutableSourceRevision{
				LocatorDigest:  locatorDigest,
				ManifestDigest: manifestDigest,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewExactSelection() error = %v", err)
	}
	if got, want := selection.AssetRefs, []backupasset.AssetRef{
		{RecoveryPointID: recoveryPointID, EntryID: firstEntryID},
		{RecoveryPointID: recoveryPointID, EntryID: secondEntryID},
	}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("canonical refs = %#v, want %#v", got, want)
	}
	if selection.SourceRevision.Kind != SourceRevisionImmutable || selection.SourceRevision.Immutable == nil ||
		selection.SourceRevision.Immutable.LocatorDigest != locatorDigest ||
		selection.SourceRevision.Immutable.ManifestDigest != manifestDigest {
		t.Fatalf("immutable source revision was not frozen exactly: %#v", selection.SourceRevision)
	}
	if selection.SelectionDigest == "" || selection.SourceRevisionDigest == "" {
		t.Fatalf("selection did not carry stable digests: %#v", selection)
	}

	authority := selection.Authority()
	encoded, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{fakeLocator, locatorDigest} {
		if strings.Contains(fmt.Sprintf("%+v", authority), forbidden) || strings.Contains(string(encoded), forbidden) {
			t.Fatalf("selection authority leaked %q: struct=%+v json=%s", forbidden, authority, encoded)
		}
	}
}

func TestSelectionRejectsDuplicateExplicitAssetRefs(t *testing.T) {
	repositoryID := strings.Repeat("a", 32)
	recoveryPointID := strings.Repeat("b", 32)
	catalogGenerationID := strings.Repeat("c", 32)
	entryID := strings.Repeat("1", 64)
	digest := strings.Repeat("d", 64)

	_, err := NewExactSelection(ExactSelectionInput{
		RepositoryID:        repositoryID,
		RecoveryPointID:     recoveryPointID,
		CatalogGenerationID: catalogGenerationID,
		AssetRefs: []backupasset.AssetRef{
			{RecoveryPointID: recoveryPointID, EntryID: entryID},
			{RecoveryPointID: recoveryPointID, EntryID: entryID},
		},
		SourceRevision: SourceRevision{
			Kind: SourceRevisionImmutable,
			Immutable: &ImmutableSourceRevision{
				LocatorDigest:  digest,
				ManifestDigest: digest,
			},
		},
	})
	if !errors.Is(err, ErrInvalidExactSelection) {
		t.Fatalf("NewExactSelection() error = %v, want ErrInvalidExactSelection", err)
	}
}

func TestSelectionSerializationNeverExposesLocatorBoundDigests(t *testing.T) {
	repositoryID := strings.Repeat("a", 32)
	recoveryPointID := strings.Repeat("b", 32)
	catalogGenerationID := strings.Repeat("c", 32)
	entryID := strings.Repeat("1", 64)
	manifestDigest := strings.Repeat("d", 64)

	buildSelection := func(locator string) ExactSelection {
		t.Helper()
		locatorDigest, err := SourceLocatorDigest(repositoryID, backupasset.ProviderRestic, recoveryPointID, locator)
		if err != nil {
			t.Fatal(err)
		}
		selection, err := NewExactSelection(ExactSelectionInput{
			RepositoryID:        repositoryID,
			RecoveryPointID:     recoveryPointID,
			CatalogGenerationID: catalogGenerationID,
			AssetRefs: []backupasset.AssetRef{
				{RecoveryPointID: recoveryPointID, EntryID: entryID},
			},
			SourceRevision: SourceRevision{
				Kind: SourceRevisionImmutable,
				Immutable: &ImmutableSourceRevision{
					LocatorDigest:  locatorDigest,
					ManifestDigest: manifestDigest,
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return selection
	}

	first := buildSelection("FAKE_LOCATOR_A_FOR_SERIALIZATION_TEST")
	second := buildSelection("FAKE_LOCATOR_B_FOR_SERIALIZATION_TEST")
	if first.SourceRevisionDigest == second.SourceRevisionDigest {
		t.Fatal("source revision digest did not bind the private locator revision")
	}
	if first.SelectionDigest != second.SelectionDigest {
		t.Fatal("selection digest was derived from a private locator binding")
	}

	firstSelectionJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondSelectionJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	firstAuthorityJSON, err := json.Marshal(first.Authority())
	if err != nil {
		t.Fatal(err)
	}
	secondAuthorityJSON, err := json.Marshal(second.Authority())
	if err != nil {
		t.Fatal(err)
	}

	for _, encoded := range [][]byte{firstSelectionJSON, secondSelectionJSON, firstAuthorityJSON, secondAuthorityJSON} {
		for _, forbidden := range []string{
			"FAKE_LOCATOR_A_FOR_SERIALIZATION_TEST",
			"FAKE_LOCATOR_B_FOR_SERIALIZATION_TEST",
			first.SourceRevision.Immutable.LocatorDigest,
			second.SourceRevision.Immutable.LocatorDigest,
			first.SourceRevisionDigest,
			second.SourceRevisionDigest,
		} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("serialized selection authority leaked %q: %s", forbidden, encoded)
			}
		}
	}
	if string(firstSelectionJSON) != string(secondSelectionJSON) || string(firstAuthorityJSON) != string(secondAuthorityJSON) {
		t.Fatalf("locator-only source changes altered JSON: selection %s / %s authority %s / %s", firstSelectionJSON, secondSelectionJSON, firstAuthorityJSON, secondAuthorityJSON)
	}
}

func TestSelectionRejectsUnknownDualAndCrossPointSourceProducts(t *testing.T) {
	repositoryID := strings.Repeat("a", 32)
	recoveryPointID := strings.Repeat("b", 32)
	catalogGenerationID := strings.Repeat("c", 32)
	entryID := strings.Repeat("1", 64)
	digest := strings.Repeat("d", 64)
	now := time.Now().UTC().Add(-time.Minute)

	tests := []struct {
		name    string
		input   ExactSelectionInput
		wantErr bool
	}{
		{
			name: "unknown revision has no latest arm",
			input: ExactSelectionInput{
				RepositoryID: repositoryID, RecoveryPointID: recoveryPointID, CatalogGenerationID: catalogGenerationID,
				AssetRefs: []backupasset.AssetRef{{RecoveryPointID: recoveryPointID, EntryID: entryID}},
				SourceRevision: SourceRevision{Kind: SourceRevisionKind("latest"), Immutable: &ImmutableSourceRevision{
					LocatorDigest: digest, ManifestDigest: digest,
				}},
			},
			wantErr: true,
		},
		{
			name: "dual revision arms",
			input: ExactSelectionInput{
				RepositoryID: repositoryID, RecoveryPointID: recoveryPointID, CatalogGenerationID: catalogGenerationID,
				AssetRefs: []backupasset.AssetRef{{RecoveryPointID: recoveryPointID, EntryID: entryID}},
				SourceRevision: SourceRevision{
					Kind:      SourceRevisionImmutable,
					Immutable: &ImmutableSourceRevision{LocatorDigest: digest, ManifestDigest: digest},
					MutableObservation: &ObservationRevision{
						SourceFingerprint: digest, CatalogGenerationID: catalogGenerationID, ObservedAt: now,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "cross recovery point asset ref",
			input: ExactSelectionInput{
				RepositoryID: repositoryID, RecoveryPointID: recoveryPointID, CatalogGenerationID: catalogGenerationID,
				AssetRefs: []backupasset.AssetRef{{RecoveryPointID: strings.Repeat("e", 32), EntryID: entryID}},
				SourceRevision: SourceRevision{Kind: SourceRevisionObservation, MutableObservation: &ObservationRevision{
					SourceFingerprint: digest, CatalogGenerationID: catalogGenerationID, ObservedAt: now,
				}},
			},
			wantErr: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewExactSelection(testCase.input)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("NewExactSelection() error = %v, wantErr %t", err, testCase.wantErr)
			}
		})
	}
}

func TestEmptyDeleteSetDigestMatchesCanonicalFramingGolden(t *testing.T) {
	const want = "3f5a5d5213612b170da6ce2f2f90775a31d4e40269bb785042589af64011b7cf"
	if EmptyDeleteSetDigest != want {
		t.Fatalf("EmptyDeleteSetDigest = %q, want %q", EmptyDeleteSetDigest, want)
	}
}

func TestRecoveryAuthorizationReceiptOperationSnapshotCodec(t *testing.T) {
	pointID := strings.Repeat("a", 32)
	digest := func(character string) string { return strings.Repeat(character, 64) }
	operations := []RecoveryOperation{
		{Kind: RecoveryOperationCreate, TargetPathDigest: digest("1"), ExpectedPrior: ExpectedTargetIdentity{Kind: ExpectedTargetAbsent},
			ExpectedPostIdentityDigest: digest("a"), ExpectedPostBytes: 1, ExpectedPriorBytes: -1,
			Source:       RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{RecoveryPointID: pointID, EntryID: digest("a")}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: 1},
		{Kind: RecoveryOperationOverwrite, TargetPathDigest: digest("2"), ExpectedPrior: ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("b")},
			ExpectedPostIdentityDigest: digest("b"), ExpectedPostBytes: 2, ExpectedPriorBytes: 2,
			Source:       RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{RecoveryPointID: pointID, EntryID: digest("b")}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: 2},
		{Kind: RecoveryOperationSkip, TargetPathDigest: digest("3"), ExpectedPrior: ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("c")},
			ExpectedPostIdentityDigest: digest("c"), ExpectedPostBytes: -1, ExpectedPriorBytes: 3,
			Source:       RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{RecoveryPointID: pointID, EntryID: digest("c")}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: 3},
		{Kind: RecoveryOperationDelete, TargetPathDigest: digest("4"), ExpectedPrior: ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("d")},
			ExpectedPostIdentityDigest: "", ExpectedPostBytes: -1, ExpectedPriorBytes: -1,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceNone}, DisplayClass: RecoveryDisplayClassDirectory},
	}
	for index := range operations {
		bindRecoveryOperationTargetForTest(
			t, TargetModeInPlace, "root-a", digest("f"), fmt.Sprintf("items/item-%d", index), &operations[index],
		)
	}
	products, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetModeInPlace, ConflictPolicy: ConflictExactMirror,
		Operations: operations,
		Limits:     RecoveryOperationLimits{MaxRows: 4, MaxItems: 4, MaxBytes: 6, MaxImpactRows: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeRecoveryOperationRows(products.Rows)
	if err != nil {
		t.Fatalf("encode operation snapshot: %v", err)
	}
	decoded, err := decodeRecoveryOperationRows(encoded)
	if err != nil {
		t.Fatalf("decode operation snapshot: %v", err)
	}
	rebuilt, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetModeInPlace, ConflictPolicy: ConflictExactMirror, Operations: decoded,
		Limits: RecoveryOperationLimits{MaxRows: 4, MaxItems: 4, MaxBytes: 6, MaxImpactRows: 4},
	})
	if err != nil || rebuilt.OperationSetDigest != products.OperationSetDigest || rebuilt.DeleteSetDigest != products.DeleteSetDigest {
		t.Fatalf("round-trip products=%+v error=%v, want digests %s/%s", rebuilt, err, products.OperationSetDigest, products.DeleteSetDigest)
	}
	for _, invalid := range []string{
		" " + encoded,
		strings.Replace(encoded, `"schema_version":2`, `"schema_version":1`, 1),
		strings.Replace(encoded, `"rows":`, `"unknown":true,"rows":`, 1),
		strings.Replace(encoded, digest("1"), "z"+strings.Repeat("f", 63), 1),
		strings.Replace(encoded, `"expected_post_bytes":1`, `"expected_post_bytes":2`, 1),
	} {
		if _, err := decodeRecoveryOperationRows(invalid); err == nil {
			t.Fatalf("invalid operation snapshot was accepted: %q", invalid)
		}
	}
}

func TestRecoveryOperationSnapshotDecodeRejectsInvalidWholeProducts(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	pointID := strings.Repeat("1", 32)
	create := func(target, entry string) RecoveryOperation {
		return RecoveryOperation{
			Kind:                       RecoveryOperationCreate,
			TargetPathDigest:           digest(target),
			TargetRelativeLocator:      "items/" + target,
			SemanticTargetDigest:       digest(target),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetAbsent},
			ExpectedPostIdentityDigest: digest(entry),
			ExpectedPostBytes:          1,
			ExpectedPriorBytes:         -1,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID,
				EntryID:         digest(entry),
			}},
			DisplayClass:   RecoveryDisplayClassRegular,
			EstimatedBytes: 1,
		}
	}
	deleteOperation := RecoveryOperation{
		Kind:                       RecoveryOperationDelete,
		TargetPathDigest:           digest("e"),
		TargetRelativeLocator:      "items/delete",
		SemanticTargetDigest:       digest("9"),
		ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("f")},
		ExpectedPostIdentityDigest: "",
		ExpectedPostBytes:          -1,
		ExpectedPriorBytes:         -1,
		Source:                     RecoveryOperationSource{Kind: RecoveryOperationSourceNone},
		DisplayClass:               RecoveryDisplayClassRegular,
	}
	canonicalSnapshot := func(
		t *testing.T,
		mode TargetMode,
		policy ConflictPolicy,
		rows []RecoveryOperation,
		deleteSetDigest string,
	) string {
		t.Helper()
		snapshotRows, err := recoveryOperationSnapshotRows(rows)
		if err != nil {
			t.Fatalf("build canonical snapshot rows: %v", err)
		}
		if deleteSetDigest == "" {
			deleteRows := make([]RecoveryOperation, 0, len(rows))
			for _, row := range rows {
				if row.Kind == RecoveryOperationDelete {
					deleteRows = append(deleteRows, row)
				}
			}
			deleteSetDigest = recoveryOperationSetDigest(deleteSetDigestDomain, deleteRows)
		}
		encoded, err := json.Marshal(recoveryOperationRowsSnapshot{
			SchemaVersion:      recoveryOperationSnapshotSchemaVersion,
			TargetMode:         mode,
			ConflictPolicy:     policy,
			OperationSetDigest: recoveryOperationSetDigest(operationSetDigestDomain, rows),
			DeleteSetDigest:    deleteSetDigest,
			Rows:               snapshotRows,
		})
		if err != nil {
			t.Fatalf("marshal canonical operation snapshot: %v", err)
		}
		return string(encoded)
	}

	duplicateSource := []RecoveryOperation{create("a", "b"), create("c", "d")}
	duplicateSource[1].Source.AssetRef.EntryID = duplicateSource[0].Source.AssetRef.EntryID
	invalid := map[string]string{
		"duplicate source": canonicalSnapshot(
			t, TargetModeInPlace, ConflictOverwriteSelected, duplicateSource, "",
		),
		"policy invalid": canonicalSnapshot(
			t, TargetModeInPlace, ConflictSkipExisting, []RecoveryOperation{deleteOperation}, "",
		),
		"self consistent wrong delete set": canonicalSnapshot(
			t, TargetModeInPlace, ConflictExactMirror, []RecoveryOperation{deleteOperation}, EmptyDeleteSetDigest,
		),
	}
	for name, encoded := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRecoveryOperationRows(encoded); !errors.Is(err, ErrInvalidRecoveryOperation) {
				t.Fatalf("decodeRecoveryOperationRows() error = %v, want ErrInvalidRecoveryOperation", err)
			}
		})
	}
}

func TestRecoveryOperationAmendmentExpectedPostFactsAndEnvelope(t *testing.T) {
	operationType := reflect.TypeOf(RecoveryOperation{})
	for field, want := range map[string]reflect.Type{
		"ExpectedPostIdentityDigest": reflect.TypeOf(""),
		"ExpectedPostBytes":          reflect.TypeOf(int64(0)),
		"ExpectedPriorBytes":         reflect.TypeOf(int64(0)),
	} {
		fieldInfo, ok := operationType.FieldByName(field)
		if !ok {
			t.Fatalf("RecoveryOperation is missing amendment field %s", field)
		}
		if fieldInfo.Type != want {
			t.Fatalf("RecoveryOperation.%s type=%s, want %s", field, fieldInfo.Type, want)
		}
	}

	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	pointID := strings.Repeat("1", 32)
	makeOperation := func(kind RecoveryOperationKind, target, prior string) RecoveryOperation {
		operation := RecoveryOperation{
			Kind: kind, TargetPathDigest: digest(target),
			ExpectedPrior: ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest(prior)},
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest(target),
			}},
			DisplayClass: RecoveryDisplayClassRegular,
		}
		if kind == RecoveryOperationCreate {
			operation.ExpectedPrior = ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}
		}
		if kind == RecoveryOperationDelete {
			operation.Source = RecoveryOperationSource{Kind: RecoveryOperationSourceNone}
			operation.DisplayClass = RecoveryDisplayClassDirectory
		}
		bindRecoveryOperationTargetForTest(
			t, TargetModeInPlace, "root-a", digest("9"), "items/item-"+target, &operation,
		)
		return operation
	}
	setFacts := func(operation *RecoveryOperation, postDigest string, postBytes, priorBytes int64) {
		value := reflect.ValueOf(operation).Elem()
		value.FieldByName("ExpectedPostIdentityDigest").SetString(postDigest)
		value.FieldByName("ExpectedPostBytes").SetInt(postBytes)
		value.FieldByName("ExpectedPriorBytes").SetInt(priorBytes)
	}

	create := makeOperation(RecoveryOperationCreate, "a", "b")
	setFacts(&create, digest("c"), 11, -1)
	overwrite := makeOperation(RecoveryOperationOverwrite, "d", "e")
	setFacts(&overwrite, digest("f"), 13, 7)
	skip := makeOperation(RecoveryOperationSkip, "0", "1")
	setFacts(&skip, digest("1"), -1, 17)
	delete := makeOperation(RecoveryOperationDelete, "2", "3")
	setFacts(&delete, "", -1, -1)

	products, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetModeInPlace, ConflictPolicy: ConflictExactMirror,
		Operations: []RecoveryOperation{create, overwrite, skip, delete},
		Limits:     RecoveryOperationLimits{MaxRows: 4, MaxItems: 4, MaxBytes: 41, MaxImpactRows: 4},
	})
	if err != nil {
		t.Fatalf("amended operation products rejected: %v", err)
	}
	encoded, err := encodeRecoveryOperationRows(products.Rows)
	if err != nil {
		t.Fatalf("encode amended operation envelope: %v", err)
	}
	if !strings.Contains(encoded, `"schema_version":2`) {
		t.Fatalf("operation envelope=%s, want schema_version 2", encoded)
	}
	for _, forbidden := range []string{digest("c"), digest("f"), digest("1")} {
		if !strings.Contains(encoded, forbidden) {
			t.Fatalf("operation envelope omitted expected-post digest %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(encoded, `"expected_post_identity_digest":""`) {
		t.Fatalf("delete envelope omitted length-framed empty expected-post identity: %s", encoded)
	}
	decoded, err := decodeRecoveryOperationRows(encoded)
	if err != nil {
		t.Fatalf("decode amended operation envelope: %v", err)
	}
	if len(decoded) != 4 {
		t.Fatalf("decoded rows=%d, want 4", len(decoded))
	}
	for _, tampered := range []string{
		strings.Replace(encoded, `"schema_version":2`, `"schema_version":1`, 1),
		strings.Replace(encoded, digest("c"), digest("z"), 1),
		strings.Replace(encoded, `"expected_post_bytes":11`, `"expected_post_bytes":12`, 1),
	} {
		if _, err := decodeRecoveryOperationRows(tampered); err == nil {
			t.Fatalf("tampered amended envelope was accepted: %s", tampered)
		}
	}
}

func TestRecoveryOverwritePriorBytesRoundTripAndTamperBinding(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	pointID := strings.Repeat("1", 32)
	rootDigest := digest("a")
	locator := "items/item-a"
	overwrite := RecoveryOperation{
		Kind:                       RecoveryOperationOverwrite,
		TargetPathDigest:           mustTargetPathDigest(t, "root-a", rootDigest, locator),
		ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("b")},
		ExpectedPostIdentityDigest: digest("c"),
		ExpectedPostBytes:          13,
		ExpectedPriorBytes:         7,
		Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
			RecoveryPointID: pointID,
			EntryID:         digest("d"),
		}},
		DisplayClass:   RecoveryDisplayClassRegular,
		EstimatedBytes: 13,
	}
	bindRecoveryOperationTargetForTest(t, TargetModeInPlace, "root-a", rootDigest, locator, &overwrite)
	input := RecoveryOperationProductsInput{
		TargetMode:     TargetModeInPlace,
		ConflictPolicy: ConflictOverwriteSelected,
		Operations:     []RecoveryOperation{overwrite},
		Limits:         RecoveryOperationLimits{MaxRows: 1, MaxItems: 1, MaxBytes: 13, MaxImpactRows: 1},
	}

	products, err := NewOperationProducts(input)
	if err != nil {
		t.Fatalf("valid overwrite prior bytes rejected: %v", err)
	}
	if len(products.Rows) != 1 || products.Rows[0].ExpectedPriorBytes != overwrite.ExpectedPriorBytes {
		t.Fatalf("operation product prior bytes = %#v, want %d", products.Rows, overwrite.ExpectedPriorBytes)
	}

	encodedRows, err := encodeRecoveryOperationRows(products.Rows)
	if err != nil {
		t.Fatalf("encode overwrite operation snapshot: %v", err)
	}
	if !strings.Contains(encodedRows, `"schema_version":2`) ||
		!strings.Contains(encodedRows, `"expected_prior_bytes":7`) {
		t.Fatalf("schema-v2 snapshot omitted overwrite prior bytes: %s", encodedRows)
	}
	decodedRows, err := decodeRecoveryOperationRows(encodedRows)
	if err != nil {
		t.Fatalf("decode overwrite operation snapshot: %v", err)
	}
	if len(decodedRows) != 1 || decodedRows[0].ExpectedPriorBytes != overwrite.ExpectedPriorBytes {
		t.Fatalf("decoded overwrite prior bytes = %#v, want %d", decodedRows, overwrite.ExpectedPriorBytes)
	}
	rebuilt, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode:     TargetModeInPlace,
		ConflictPolicy: ConflictOverwriteSelected,
		Operations:     decodedRows,
		Limits:         input.Limits,
	})
	if err != nil || rebuilt.OperationSetDigest != products.OperationSetDigest {
		t.Fatalf("rebuilt overwrite product digest = %q, error = %v, want %q", rebuilt.OperationSetDigest, err, products.OperationSetDigest)
	}
	changed := cloneRecoveryOperation(decodedRows[0])
	changed.ExpectedPriorBytes++
	changedProducts, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode:     TargetModeInPlace,
		ConflictPolicy: ConflictOverwriteSelected,
		Operations:     []RecoveryOperation{changed},
		Limits:         input.Limits,
	})
	if err != nil {
		t.Fatalf("build changed overwrite product: %v", err)
	}
	if changedProducts.OperationSetDigest == products.OperationSetDigest {
		t.Fatal("operation-set digest did not bind exact overwrite prior bytes")
	}

	binding := completeTargetLocatorBindingForTest(t, TargetLocatorEnvelopeBinding{
		JobID:                      strings.Repeat("2", 32),
		JobItemID:                  strings.Repeat("3", 32),
		PlanDigest:                 digest("e"),
		TargetMode:                 TargetModeInPlace,
		NodeID:                     7,
		RootID:                     "root-a",
		RootLocatorDigest:          rootDigest,
		TargetObjectDigest:         mustTargetPathDigest(t, "root-a", rootDigest, locator),
		Operation:                  overwrite.Kind,
		ExpectedPriorKind:          overwrite.ExpectedPrior.Kind,
		ExpectedPriorDigest:        overwrite.ExpectedPrior.Digest,
		ExpectedPostIdentityDigest: overwrite.ExpectedPostIdentityDigest,
		ExpectedPostBytes:          overwrite.ExpectedPostBytes,
		ExpectedPriorBytes:         overwrite.ExpectedPriorBytes,
		TargetLocatorKeyVersion:    1,
		TargetLocatorCipherVersion: 1,
	}, locator)
	encodedLocator, err := EncodeTargetLocatorEnvelope(binding, locator)
	if err != nil {
		t.Fatalf("encode overwrite locator envelope: %v", err)
	}
	if !strings.Contains(encodedLocator, `"expected_prior_bytes":7`) {
		t.Fatalf("locator envelope omitted overwrite prior bytes: %s", encodedLocator)
	}
	decodedLocator, err := DecodeTargetLocatorEnvelope(encodedLocator, binding)
	if err != nil || decodedLocator != locator {
		t.Fatalf("decode overwrite locator = %q, error = %v, want %q", decodedLocator, err, locator)
	}

	missingPriorBytes := overwrite
	missingPriorBytes.ExpectedPriorBytes = -1
	invalidInput := input
	invalidInput.Operations = []RecoveryOperation{missingPriorBytes}
	if _, err := NewOperationProducts(invalidInput); !errors.Is(err, ErrInvalidRecoveryOperation) {
		t.Fatalf("overwrite prior bytes -1 error = %v, want ErrInvalidRecoveryOperation", err)
	}
	invalidBinding := binding
	invalidBinding.ExpectedPriorBytes = -1
	if _, err := EncodeTargetLocatorEnvelope(invalidBinding, locator); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("overwrite locator prior bytes -1 error = %v, want ErrInvalidTargetLocatorEnvelope", err)
	}

	var tampered targetLocatorEnvelope
	if err := json.Unmarshal([]byte(encodedLocator), &tampered); err != nil {
		t.Fatalf("decode overwrite locator fixture: %v", err)
	}
	tampered.ExpectedPriorBytes++
	tampered.BindingDigest = targetLocatorEnvelopeDigest(tampered)
	reDigested, err := json.Marshal(tampered)
	if err != nil {
		t.Fatalf("marshal re-digested overwrite locator: %v", err)
	}
	if _, err := DecodeTargetLocatorEnvelope(string(reDigested), binding); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("re-digested overwrite prior-byte tamper error = %v, want ErrInvalidTargetLocatorEnvelope", err)
	}
}

func TestRecoveryOperationAmendmentRejectsIncorrectExpectedPostFacts(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	pointID := strings.Repeat("1", 32)
	base := []RecoveryOperation{
		{
			Kind:                       RecoveryOperationCreate,
			TargetPathDigest:           digest("a"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetAbsent},
			ExpectedPostIdentityDigest: digest("b"),
			ExpectedPostBytes:          11,
			ExpectedPriorBytes:         -1,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("b"),
			}},
			DisplayClass:   RecoveryDisplayClassRegular,
			EstimatedBytes: 11,
		},
		{
			Kind:                       RecoveryOperationOverwrite,
			TargetPathDigest:           digest("c"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("d")},
			ExpectedPostIdentityDigest: digest("e"),
			ExpectedPostBytes:          13,
			ExpectedPriorBytes:         7,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("e"),
			}},
			DisplayClass:   RecoveryDisplayClassRegular,
			EstimatedBytes: 13,
		},
		{
			Kind:                       RecoveryOperationSkip,
			TargetPathDigest:           digest("f"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("0")},
			ExpectedPostIdentityDigest: digest("0"),
			ExpectedPostBytes:          -1,
			ExpectedPriorBytes:         17,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("1"),
			}},
			DisplayClass:   RecoveryDisplayClassRegular,
			EstimatedBytes: 17,
		},
		{
			Kind:                       RecoveryOperationDelete,
			TargetPathDigest:           digest("2"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("3")},
			ExpectedPostIdentityDigest: "",
			ExpectedPostBytes:          -1,
			ExpectedPriorBytes:         -1,
			Source:                     RecoveryOperationSource{Kind: RecoveryOperationSourceNone},
			DisplayClass:               RecoveryDisplayClassRegular,
		},
	}
	for index := range base {
		bindRecoveryOperationTargetForTest(
			t, TargetModeInPlace, "root-a", digest("9"), fmt.Sprintf("items/item-%d", index), &base[index],
		)
	}

	validInput := func() RecoveryOperationProductsInput {
		operations := make([]RecoveryOperation, len(base))
		for index, operation := range base {
			operations[index] = cloneRecoveryOperation(operation)
		}
		return RecoveryOperationProductsInput{
			TargetMode: TargetModeInPlace, ConflictPolicy: ConflictExactMirror,
			Operations: operations,
			Limits:     RecoveryOperationLimits{MaxRows: 4, MaxItems: 4, MaxBytes: 41, MaxImpactRows: 4},
		}
	}
	if _, err := NewOperationProducts(validInput()); err != nil {
		t.Fatalf("valid amended operation facts rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func([]RecoveryOperation)
	}{
		{
			name: "create requires strong lowercase post digest",
			mutate: func(operations []RecoveryOperation) {
				operations[0].ExpectedPostIdentityDigest = strings.ToUpper(operations[0].ExpectedPostIdentityDigest)
			},
		},
		{
			name: "create requires post bytes",
			mutate: func(operations []RecoveryOperation) {
				operations[0].ExpectedPostBytes = -1
			},
		},
		{
			name: "create has no prior target byte fact",
			mutate: func(operations []RecoveryOperation) {
				operations[0].ExpectedPriorBytes = 1
			},
		},
		{
			name: "overwrite requires strong lowercase post digest",
			mutate: func(operations []RecoveryOperation) {
				operations[1].ExpectedPostIdentityDigest = "short"
			},
		},
		{
			name: "overwrite requires post bytes",
			mutate: func(operations []RecoveryOperation) {
				operations[1].ExpectedPostBytes = -1
			},
		},
		{
			name: "overwrite requires prior target bytes",
			mutate: func(operations []RecoveryOperation) {
				operations[1].ExpectedPriorBytes = -1
			},
		},
		{
			name: "skip post digest is the frozen prior digest",
			mutate: func(operations []RecoveryOperation) {
				operations[2].ExpectedPostIdentityDigest = digest("4")
			},
		},
		{
			name: "skip keeps prior target bytes separate from source",
			mutate: func(operations []RecoveryOperation) {
				operations[2].ExpectedPriorBytes = -1
			},
		},
		{
			name: "skip has no post byte fact",
			mutate: func(operations []RecoveryOperation) {
				operations[2].ExpectedPostBytes = 17
			},
		},
		{
			name: "delete has no invented absence digest",
			mutate: func(operations []RecoveryOperation) {
				operations[3].ExpectedPostIdentityDigest = digest("5")
			},
		},
		{
			name: "delete has no post byte fact",
			mutate: func(operations []RecoveryOperation) {
				operations[3].ExpectedPostBytes = 0
			},
		},
		{
			name: "delete has no prior target byte fact",
			mutate: func(operations []RecoveryOperation) {
				operations[3].ExpectedPriorBytes = 0
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			input := validInput()
			testCase.mutate(input.Operations)
			if _, err := NewOperationProducts(input); !errors.Is(err, ErrInvalidRecoveryOperation) {
				t.Fatalf("NewOperationProducts() error = %v, want ErrInvalidRecoveryOperation", err)
			}
		})
	}
}

func TestRecoverySkipIdentityAndLocatorPriorBinding(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	pointID := strings.Repeat("1", 32)
	skip := RecoveryOperation{
		Kind:                       RecoveryOperationSkip,
		TargetPathDigest:           digest("a"),
		ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("b")},
		ExpectedPostIdentityDigest: digest("c"),
		ExpectedPostBytes:          -1,
		ExpectedPriorBytes:         17,
		Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
			RecoveryPointID: pointID,
			EntryID:         digest("d"),
		}},
		DisplayClass:   RecoveryDisplayClassRegular,
		EstimatedBytes: 17,
	}
	bindRecoveryOperationTargetForTest(t, TargetModeInPlace, "root-a", digest("f"), "items/item-skip", &skip)
	if _, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode:     TargetModeInPlace,
		ConflictPolicy: ConflictSkipExisting,
		Operations:     []RecoveryOperation{skip},
		Limits:         RecoveryOperationLimits{MaxRows: 1, MaxItems: 1, MaxBytes: 17, MaxImpactRows: 1},
	}); !errors.Is(err, ErrInvalidRecoveryOperation) {
		t.Fatalf("skip prior/post identity mismatch error = %v, want ErrInvalidRecoveryOperation", err)
	}

	locator := "jobs/item-skip"
	binding := completeTargetLocatorBindingForTest(t, TargetLocatorEnvelopeBinding{
		JobID:                      strings.Repeat("2", 32),
		JobItemID:                  strings.Repeat("3", 32),
		PlanDigest:                 digest("e"),
		TargetMode:                 TargetModeInPlace,
		NodeID:                     7,
		RootID:                     "root-a",
		RootLocatorDigest:          digest("f"),
		TargetObjectDigest:         mustTargetPathDigest(t, "root-a", digest("f"), locator),
		Operation:                  RecoveryOperationSkip,
		ExpectedPriorKind:          ExpectedTargetPresent,
		ExpectedPriorDigest:        digest("b"),
		ExpectedPostIdentityDigest: digest("b"),
		ExpectedPostBytes:          -1,
		ExpectedPriorBytes:         17,
		TargetLocatorKeyVersion:    1,
		TargetLocatorCipherVersion: 1,
	}, locator)
	encoded, err := EncodeTargetLocatorEnvelope(binding, locator)
	if err != nil {
		t.Fatalf("EncodeTargetLocatorEnvelope(skip) error = %v", err)
	}

	for _, testCase := range []struct {
		name   string
		mutate func(*TargetLocatorEnvelopeBinding)
	}{
		{
			name: "prior kind",
			mutate: func(candidate *TargetLocatorEnvelopeBinding) {
				candidate.ExpectedPriorKind = ExpectedTargetAbsent
				candidate.ExpectedPriorDigest = ""
			},
		},
		{
			name: "prior digest",
			mutate: func(candidate *TargetLocatorEnvelopeBinding) {
				candidate.ExpectedPriorDigest = digest("c")
			},
		},
	} {
		t.Run("invalid "+testCase.name, func(t *testing.T) {
			candidate := binding
			testCase.mutate(&candidate)
			if _, err := EncodeTargetLocatorEnvelope(candidate, locator); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
				t.Fatalf("EncodeTargetLocatorEnvelope() error = %v, want ErrInvalidTargetLocatorEnvelope", err)
			}
		})
	}

	var envelope targetLocatorEnvelope
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatalf("decode locator envelope fixture: %v", err)
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*targetLocatorEnvelope)
	}{
		{
			name: "prior kind",
			mutate: func(candidate *targetLocatorEnvelope) {
				candidate.ExpectedPriorKind = ExpectedTargetAbsent
				candidate.ExpectedPriorDigest = ""
			},
		},
		{
			name: "prior and post digest substitution",
			mutate: func(candidate *targetLocatorEnvelope) {
				candidate.ExpectedPriorDigest = digest("c")
				candidate.ExpectedPostIdentityDigest = digest("c")
			},
		},
	} {
		t.Run("tampered "+testCase.name, func(t *testing.T) {
			candidate := envelope
			testCase.mutate(&candidate)
			candidate.BindingDigest = targetLocatorEnvelopeDigest(candidate)
			tampered, err := json.Marshal(candidate)
			if err != nil {
				t.Fatalf("marshal tampered locator envelope: %v", err)
			}
			if _, err := DecodeTargetLocatorEnvelope(string(tampered), binding); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
				t.Fatalf("tampered locator envelope error = %v, want ErrInvalidTargetLocatorEnvelope", err)
			}
		})
	}
}

func TestTargetVerifyClosedProductAndOpaqueRevision(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	expectation := TargetVerifyExpectation{
		Kind:    TargetPresencePresent,
		Present: &PresentExpectation{IdentityDigest: digest("a"), Bytes: 17},
	}
	valid := TargetVerifyObservation{
		Kind:             TargetPresencePresent,
		Present:          &PresentObservation{IdentityDigest: digest("a"), Bytes: 17},
		ObservedRevision: "target:inode-7@generation-42",
	}
	if err := valid.ValidateAgainst(expectation); err != nil {
		t.Fatalf("bounded opaque observed revision rejected: %v", err)
	}

	for _, revision := range []string{
		"",
		strings.Repeat("r", opaqueRevisionMax+1),
		digest("b"),
		strings.ToUpper(digest("b")),
	} {
		observation := valid
		observation.ObservedRevision = revision
		if err := observation.ValidateAgainst(expectation); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("invalid observed revision %q error = %v, want ErrInvalidTargetVerification", revision, err)
		}
	}

	for _, invalid := range []TargetVerifyExpectation{
		{Kind: TargetPresencePresent},
		{Kind: TargetPresencePresent, Present: expectation.Present, Absent: &AbsentExpectation{}},
	} {
		if err := invalid.Validate(); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("invalid expectation %#v error = %v, want ErrInvalidTargetVerification", invalid, err)
		}
	}
	for _, invalid := range []TargetVerifyObservation{
		{Kind: TargetPresencePresent, ObservedRevision: valid.ObservedRevision},
		{
			Kind: TargetPresencePresent, Present: valid.Present,
			Absent: &AbsentObservation{Evidence: TargetAbsenceEvidenceExact}, ObservedRevision: valid.ObservedRevision,
		},
	} {
		if err := invalid.Validate(); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("invalid observation %#v error = %v, want ErrInvalidTargetVerification", invalid, err)
		}
	}

	for _, mismatch := range []PresentObservation{
		{IdentityDigest: digest("c"), Bytes: expectation.Present.Bytes},
		{IdentityDigest: expectation.Present.IdentityDigest, Bytes: expectation.Present.Bytes + 1},
	} {
		observation := valid
		observation.Present = &mismatch
		if err := observation.ValidateAgainst(expectation); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("mismatched present observation %#v error = %v, want ErrInvalidTargetVerification", mismatch, err)
		}
	}
}

func TestTargetVerifyContractsClosePresenceArms(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	presentExpectation := TargetVerifyExpectation{
		Kind:    TargetPresencePresent,
		Present: &PresentExpectation{IdentityDigest: digest("a"), Bytes: 17},
	}
	presentObservation := TargetVerifyObservation{
		Kind:             TargetPresencePresent,
		Present:          &PresentObservation{IdentityDigest: digest("a"), Bytes: 17},
		ObservedRevision: "target-revision-present-1",
	}
	if err := presentExpectation.Validate(); err != nil {
		t.Fatalf("present expectation rejected: %v", err)
	}
	if err := presentObservation.ValidateAgainst(presentExpectation); err != nil {
		t.Fatalf("matching present observation rejected: %v", err)
	}

	var port recoveryContractsTargetVerifier = recoveryContractsTargetPortStub{observation: presentObservation}
	observed, err := port.Verify(context.Background(), presentExpectation)
	if err != nil {
		t.Fatalf("TargetPort.Verify() error = %v", err)
	}
	if err := observed.ValidateAgainst(presentExpectation); err != nil {
		t.Fatalf("TargetPort.Verify() returned an invalid present observation: %v", err)
	}

	absentExpectation := TargetVerifyExpectation{
		Kind:   TargetPresenceAbsent,
		Absent: &AbsentExpectation{},
	}
	absentObservation := TargetVerifyObservation{
		Kind:             TargetPresenceAbsent,
		Absent:           &AbsentObservation{Evidence: TargetAbsenceEvidenceExact},
		ObservedRevision: "target-revision-absent-1",
	}
	if _, hasSyntheticDigest := reflect.TypeOf(AbsentObservation{}).FieldByName("IdentityDigest"); hasSyntheticDigest {
		t.Fatal("AbsentObservation must not invent an object identity digest")
	}
	if err := absentObservation.ValidateAgainst(absentExpectation); err != nil {
		t.Fatalf("explicit exact absence observation rejected: %v", err)
	}
	for _, evidence := range []TargetAbsenceEvidenceKind{
		"permission_denied",
		"timeout",
		"unsupported_stat",
		"transport_failure",
		"ambiguous_missing",
	} {
		observation := absentObservation
		observation.Absent = &AbsentObservation{Evidence: evidence}
		if err := observation.ValidateAgainst(absentExpectation); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("non-exact absence evidence %q error = %v, want ErrInvalidTargetVerification", evidence, err)
		}
	}

	invalidExpectations := []TargetVerifyExpectation{
		{Kind: TargetPresencePresent},
		{Kind: TargetPresenceAbsent, Present: presentExpectation.Present, Absent: &AbsentExpectation{}},
		{Kind: TargetPresenceKind("unknown"), Present: presentExpectation.Present},
		{Kind: TargetPresencePresent, Present: &PresentExpectation{IdentityDigest: digest("d"), Bytes: -1}},
	}
	for index, expectation := range invalidExpectations {
		if err := expectation.Validate(); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("invalid expectation %d error = %v, want ErrInvalidTargetVerification", index, err)
		}
	}

	for _, observation := range []TargetVerifyObservation{
		{Kind: TargetPresenceAbsent, Absent: &AbsentObservation{Evidence: TargetAbsenceEvidenceKind("ambiguous_missing")}, ObservedRevision: digest("d")},
		{Kind: TargetPresenceAbsent, Absent: &AbsentObservation{Evidence: TargetAbsenceEvidenceExact}, ObservedRevision: "target-revision-1"},
		{Kind: TargetPresencePresent, Present: &PresentObservation{IdentityDigest: digest("e"), Bytes: 17}, ObservedRevision: digest("f")},
		{Kind: TargetPresencePresent, Present: presentObservation.Present, Absent: &AbsentObservation{Evidence: TargetAbsenceEvidenceExact}, ObservedRevision: digest("f")},
	} {
		if err := observation.ValidateAgainst(presentExpectation); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("invalid/mismatched observation %#v error = %v, want ErrInvalidTargetVerification", observation, err)
		}
	}
}

func TestTargetLocatorEnvelopeIsCanonicalVersionedAndRowBound(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	locator := "jobs/item-a"
	binding := TargetLocatorEnvelopeBinding{
		JobID:                      strings.Repeat("1", 32),
		JobItemID:                  strings.Repeat("2", 32),
		PlanDigest:                 digest("a"),
		TargetMode:                 TargetModeInPlace,
		NodeID:                     7,
		RootID:                     "root-a",
		RootLocatorDigest:          digest("b"),
		TargetObjectDigest:         mustTargetPathDigest(t, "root-a", digest("b"), locator),
		Operation:                  RecoveryOperationCreate,
		ExpectedPriorKind:          ExpectedTargetAbsent,
		ExpectedPostIdentityDigest: digest("c"),
		ExpectedPostBytes:          17,
		ExpectedPriorBytes:         -1,
		TargetLocatorKeyVersion:    1,
		TargetLocatorCipherVersion: 1,
	}
	binding = completeTargetLocatorBindingForTest(t, binding, locator)
	encoded, err := EncodeTargetLocatorEnvelope(binding, locator)
	if err != nil {
		t.Fatalf("EncodeTargetLocatorEnvelope() error = %v", err)
	}
	if !strings.Contains(encoded, `"schema_version":1`) ||
		!strings.Contains(encoded, `"expected_post_identity_digest":"`+binding.ExpectedPostIdentityDigest+`"`) ||
		!strings.Contains(encoded, `"expected_post_bytes":17`) {
		t.Fatalf("locator envelope omitted its versioned expected-post binding: %s", encoded)
	}
	decoded, err := DecodeTargetLocatorEnvelope(encoded, binding)
	if err != nil {
		t.Fatalf("DecodeTargetLocatorEnvelope() error = %v", err)
	}
	if decoded != locator {
		t.Fatalf("decoded locator = %q, want %q", decoded, locator)
	}

	for _, tampered := range []string{
		" " + encoded,
		strings.Replace(encoded, `"schema_version":1`, `"schema_version":2`, 1),
		strings.Replace(encoded, binding.JobItemID, strings.Repeat("3", 32), 1),
		strings.Replace(encoded, binding.ExpectedPostIdentityDigest, digest("d"), 1),
		strings.Replace(encoded, `"expected_post_bytes":17`, `"expected_post_bytes":18`, 1),
		strings.Replace(encoded, `"expected_prior_bytes":-1`, `"expected_prior_bytes":0`, 1),
		strings.Replace(encoded, `"relative_locator":"jobs/item-a"`, `"relative_locator":"jobs/item-b"`, 1),
		strings.Replace(encoded, `"binding_digest":`, `"unexpected":true,"binding_digest":`, 1),
	} {
		if _, err := DecodeTargetLocatorEnvelope(tampered, binding); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
			t.Fatalf("tampered locator envelope error = %v, want ErrInvalidTargetLocatorEnvelope: %s", err, tampered)
		}
	}

	crossRow := binding
	crossRow.JobItemID = strings.Repeat("3", 32)
	if _, err := DecodeTargetLocatorEnvelope(encoded, crossRow); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("cross-row locator envelope error = %v, want ErrInvalidTargetLocatorEnvelope", err)
	}

	invalidBindings := []struct {
		name    string
		locator string
		mutate  func(*TargetLocatorEnvelopeBinding)
	}{
		{name: "zero key version", locator: locator, mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.TargetLocatorKeyVersion = 0
		}},
		{name: "zero local cipher version", locator: locator, mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.TargetLocatorCipherVersion = 0
		}},
		{name: "in-place workspace binding", locator: locator, mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.WorkspaceBindingDigest = digest("e")
		}},
		{name: "in-place workspace locator", locator: locator, mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.WorkspaceRelativeLocator = "jobs/workspace-a"
		}},
		{name: "create without post bytes", locator: locator, mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.ExpectedPostBytes = -1
		}},
		{name: "create with prior target bytes", locator: locator, mutate: func(candidate *TargetLocatorEnvelopeBinding) {
			candidate.ExpectedPriorBytes = 17
		}},
		{name: "noncanonical locator", locator: "jobs/../item-a", mutate: func(*TargetLocatorEnvelopeBinding) {}},
	}
	for _, testCase := range invalidBindings {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := binding
			testCase.mutate(&candidate)
			if _, err := EncodeTargetLocatorEnvelope(candidate, testCase.locator); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
				t.Fatalf("EncodeTargetLocatorEnvelope() error = %v, want ErrInvalidTargetLocatorEnvelope", err)
			}
		})
	}

	deleteLocator := "jobs/item-delete"
	deleteBinding := binding
	deleteBinding.JobItemID = strings.Repeat("4", 32)
	deleteBinding.Operation = RecoveryOperationDelete
	deleteBinding.PlanItemID = ""
	deleteBinding.SourceRecoveryPointID = ""
	deleteBinding.SourceEntryID = ""
	deleteBinding.ExpectedPriorKind = ExpectedTargetPresent
	deleteBinding.ExpectedPriorDigest = digest("d")
	deleteBinding.ExpectedPostIdentityDigest = ""
	deleteBinding.ExpectedPostBytes = -1
	deleteBinding.ExpectedPriorBytes = -1
	deleteBinding.TargetObjectDigest = mustTargetPathDigest(t, "root-a", digest("b"), deleteLocator)
	deleteBinding = completeTargetLocatorBindingForTest(t, deleteBinding, deleteLocator)
	deleteEnvelope, err := EncodeTargetLocatorEnvelope(deleteBinding, deleteLocator)
	if err != nil {
		t.Fatalf("EncodeTargetLocatorEnvelope(delete) error = %v", err)
	}
	if !strings.Contains(deleteEnvelope, `"expected_post_identity_digest":""`) {
		t.Fatalf("delete locator envelope omitted the length-framed empty post identity: %s", deleteEnvelope)
	}
	if _, err := DecodeTargetLocatorEnvelope(deleteEnvelope, deleteBinding); err != nil {
		t.Fatalf("DecodeTargetLocatorEnvelope(delete) error = %v", err)
	}
	deleteBinding.ExpectedPostIdentityDigest = digest("e")
	if _, err := EncodeTargetLocatorEnvelope(deleteBinding, deleteLocator); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("delete locator envelope accepted invented absence digest: %v", err)
	}
}

func TestTargetLocatorEnvelopeRejectsInvalidUTF8RootIDBeforeJSONReplacement(t *testing.T) {
	rootID := "root-" + string([]byte{0xff})
	rootLocatorDigest := strings.Repeat("b", sha256DigestLength)
	locator := "jobs/item-a"
	binding := TargetLocatorEnvelopeBinding{
		CodecVersion:               targetLocatorEnvelopeSchemaVersion,
		JobID:                      strings.Repeat("1", 32),
		JobItemID:                  strings.Repeat("2", 32),
		PlanDigest:                 strings.Repeat("a", sha256DigestLength),
		PlanItemID:                 strings.Repeat("4", 32),
		SourceRecoveryPointID:      strings.Repeat("5", 32),
		SourceEntryID:              strings.Repeat("6", sha256DigestLength),
		TargetMode:                 TargetModeInPlace,
		NodeID:                     7,
		RootID:                     rootID,
		RootLocatorDigest:          rootLocatorDigest,
		SemanticTargetDigest:       strings.Repeat("d", sha256DigestLength),
		TargetObjectDigest:         framedDigest(targetPathDigestDomain, rootID, rootLocatorDigest, locator),
		Operation:                  RecoveryOperationCreate,
		ExpectedPriorKind:          ExpectedTargetAbsent,
		ExpectedPostIdentityDigest: strings.Repeat("c", sha256DigestLength),
		ExpectedPostBytes:          17,
		ExpectedPriorBytes:         -1,
		TargetLocatorKeyVersion:    1,
		TargetLocatorCipherVersion: 1,
	}

	encoded, err := EncodeTargetLocatorEnvelope(binding, locator)
	if !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("EncodeTargetLocatorEnvelope() error = %v, encoded = %q, want ErrInvalidTargetLocatorEnvelope before JSON replacement", err, encoded)
	}
}

func TestTargetLocatorEnvelopeAcceptsValidUTF8RootIDRoundTrip(t *testing.T) {
	rootID := "\u6062\u590d-root"
	rootLocatorDigest := strings.Repeat("b", sha256DigestLength)
	locator := "jobs/item-a"
	targetPathDigest, err := TargetObjectDigest(rootID, rootLocatorDigest, locator)
	if err != nil {
		t.Fatalf("TargetObjectDigest() error = %v", err)
	}
	binding := TargetLocatorEnvelopeBinding{
		JobID:                      strings.Repeat("1", 32),
		JobItemID:                  strings.Repeat("2", 32),
		PlanDigest:                 strings.Repeat("a", sha256DigestLength),
		TargetMode:                 TargetModeInPlace,
		NodeID:                     7,
		RootID:                     rootID,
		RootLocatorDigest:          rootLocatorDigest,
		TargetObjectDigest:         targetPathDigest,
		Operation:                  RecoveryOperationCreate,
		ExpectedPriorKind:          ExpectedTargetAbsent,
		ExpectedPostIdentityDigest: strings.Repeat("c", sha256DigestLength),
		ExpectedPostBytes:          17,
		ExpectedPriorBytes:         -1,
		TargetLocatorKeyVersion:    1,
		TargetLocatorCipherVersion: 1,
	}
	binding = completeTargetLocatorBindingForTest(t, binding, locator)

	encoded, err := EncodeTargetLocatorEnvelope(binding, locator)
	if err != nil {
		t.Fatalf("EncodeTargetLocatorEnvelope() error = %v", err)
	}
	decoded, err := DecodeTargetLocatorEnvelope(encoded, binding)
	if err != nil {
		t.Fatalf("DecodeTargetLocatorEnvelope() error = %v", err)
	}
	if decoded != locator {
		t.Fatalf("decoded locator = %q, want %q", decoded, locator)
	}
}

func TestTargetLocatorEnvelopeAcceptsIsolatedWorkspaceRelativeLocator(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	workspaceLocator := "jobs/workspace-a"
	itemLocator := "items/item-a"
	fullLocator := workspaceLocator + "/" + itemLocator
	binding := TargetLocatorEnvelopeBinding{
		JobID:                      strings.Repeat("1", 32),
		JobItemID:                  strings.Repeat("2", 32),
		PlanDigest:                 digest("a"),
		TargetMode:                 TargetModeIsolated,
		NodeID:                     7,
		RootID:                     "root-a",
		RootLocatorDigest:          digest("b"),
		TargetObjectDigest:         mustTargetPathDigest(t, "root-a", digest("b"), fullLocator),
		Operation:                  RecoveryOperationCreate,
		WorkspaceBindingDigest:     digest("c"),
		WorkspaceRelativeLocator:   workspaceLocator,
		ExpectedPriorKind:          ExpectedTargetAbsent,
		ExpectedPostIdentityDigest: digest("d"),
		ExpectedPostBytes:          17,
		ExpectedPriorBytes:         -1,
		TargetLocatorKeyVersion:    1,
		TargetLocatorCipherVersion: 1,
	}
	binding = completeTargetLocatorBindingForTest(t, binding, itemLocator)

	encoded, err := EncodeTargetLocatorEnvelope(binding, itemLocator)
	if err != nil {
		t.Fatalf("EncodeTargetLocatorEnvelope(isolated) error = %v", err)
	}
	decoded, err := DecodeTargetLocatorEnvelope(encoded, binding)
	if err != nil {
		t.Fatalf("DecodeTargetLocatorEnvelope(isolated) error = %v", err)
	}
	if decoded != itemLocator {
		t.Fatalf("decoded isolated locator = %q, want workspace-relative %q", decoded, itemLocator)
	}
	if !strings.Contains(encoded, `"workspace_relative_locator":"`+workspaceLocator+`"`) ||
		!strings.Contains(encoded, `"relative_locator":"`+itemLocator+`"`) {
		t.Fatalf("isolated locator envelope omitted its workspace/suffix binding: %s", encoded)
	}

	wrongWorkspace := binding
	wrongWorkspace.WorkspaceBindingDigest = digest("e")
	wrongWorkspace.WorkspaceRelativeLocator = "jobs/workspace-b"
	wrongWorkspace.TargetObjectDigest = mustTargetPathDigest(
		t, wrongWorkspace.RootID, wrongWorkspace.RootLocatorDigest,
		wrongWorkspace.WorkspaceRelativeLocator+"/"+itemLocator,
	)
	if _, err := DecodeTargetLocatorEnvelope(encoded, wrongWorkspace); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("isolated envelope accepted a different workspace binding: %v", err)
	}

	wrongRoot := binding
	wrongRoot.RootID = "root-b"
	wrongRoot.TargetObjectDigest = mustTargetPathDigest(t, wrongRoot.RootID, wrongRoot.RootLocatorDigest, fullLocator)
	if _, err := DecodeTargetLocatorEnvelope(encoded, wrongRoot); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("isolated envelope accepted a different root binding: %v", err)
	}

	wrongNamespace := binding
	wrongNamespace.TargetObjectDigest = mustTargetPathDigest(
		t, wrongNamespace.RootID, wrongNamespace.RootLocatorDigest, itemLocator,
	)
	if _, err := EncodeTargetLocatorEnvelope(wrongNamespace, itemLocator); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("isolated envelope accepted a workspace-relative target digest: %v", err)
	}

	for _, candidate := range []TargetLocatorEnvelopeBinding{
		func() TargetLocatorEnvelopeBinding {
			candidate := binding
			candidate.WorkspaceRelativeLocator = ""
			return candidate
		}(),
		func() TargetLocatorEnvelopeBinding {
			candidate := binding
			candidate.WorkspaceRelativeLocator = "jobs/../workspace-a"
			return candidate
		}(),
	} {
		if _, err := EncodeTargetLocatorEnvelope(candidate, itemLocator); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
			t.Fatalf("isolated envelope accepted invalid workspace locator %q: %v", candidate.WorkspaceRelativeLocator, err)
		}
	}

	tamperedWorkspace := strings.Replace(
		encoded,
		`"workspace_relative_locator":"`+workspaceLocator+`"`,
		`"workspace_relative_locator":"jobs/workspace-b"`,
		1,
	)
	if _, err := DecodeTargetLocatorEnvelope(tamperedWorkspace, binding); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
		t.Fatalf("isolated envelope accepted workspace-byte tampering: %v", err)
	}
}

func TestTargetLocatorEnvelopeRejectsOperationFactTampering(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	locator := "items/item-a"
	baseBinding := func(operation RecoveryOperationKind) TargetLocatorEnvelopeBinding {
		binding := TargetLocatorEnvelopeBinding{
			JobID:                      strings.Repeat("1", 32),
			JobItemID:                  strings.Repeat("2", 32),
			PlanDigest:                 digest("a"),
			TargetMode:                 TargetModeInPlace,
			NodeID:                     7,
			RootID:                     "root-a",
			RootLocatorDigest:          digest("b"),
			TargetObjectDigest:         mustTargetPathDigest(t, "root-a", digest("b"), locator),
			Operation:                  operation,
			ExpectedPriorKind:          ExpectedTargetPresent,
			ExpectedPriorDigest:        digest("c"),
			ExpectedPostIdentityDigest: digest("d"),
			ExpectedPostBytes:          17,
			ExpectedPriorBytes:         13,
			TargetLocatorKeyVersion:    1,
			TargetLocatorCipherVersion: 1,
		}
		switch operation {
		case RecoveryOperationCreate:
			binding.ExpectedPriorKind = ExpectedTargetAbsent
			binding.ExpectedPriorDigest = ""
			binding.ExpectedPriorBytes = -1
		case RecoveryOperationSkip:
			binding.ExpectedPostIdentityDigest = binding.ExpectedPriorDigest
			binding.ExpectedPostBytes = -1
			binding.ExpectedPriorBytes = 17
		case RecoveryOperationDelete:
			binding.ExpectedPostIdentityDigest = ""
			binding.ExpectedPostBytes = -1
			binding.ExpectedPriorBytes = -1
		}
		return completeTargetLocatorBindingForTest(t, binding, locator)
	}
	assertTampered := func(
		t *testing.T,
		binding TargetLocatorEnvelopeBinding,
		relativeLocator string,
		mutate func(*targetLocatorEnvelope),
	) {
		t.Helper()
		encoded, err := EncodeTargetLocatorEnvelope(binding, relativeLocator)
		if err != nil {
			t.Fatalf("EncodeTargetLocatorEnvelope(%s) error = %v", binding.Operation, err)
		}
		if _, err := DecodeTargetLocatorEnvelope(encoded, binding); err != nil {
			t.Fatalf("DecodeTargetLocatorEnvelope(%s) valid fixture error = %v", binding.Operation, err)
		}
		var envelope targetLocatorEnvelope
		if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
			t.Fatalf("decode target locator fixture: %v", err)
		}
		mutate(&envelope)
		envelope.BindingDigest = targetLocatorEnvelopeDigest(envelope)
		tampered, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal tampered target locator envelope: %v", err)
		}
		if _, err := DecodeTargetLocatorEnvelope(string(tampered), binding); !errors.Is(err, ErrInvalidTargetLocatorEnvelope) {
			t.Fatalf("DecodeTargetLocatorEnvelope(%s tamper) error = %v, want ErrInvalidTargetLocatorEnvelope", binding.Operation, err)
		}
	}

	tests := []struct {
		name      string
		operation RecoveryOperationKind
		mutate    func(*targetLocatorEnvelope)
	}{
		{name: "create post digest", operation: RecoveryOperationCreate, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPostIdentityDigest = digest("e")
		}},
		{name: "create post bytes", operation: RecoveryOperationCreate, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPostBytes++
		}},
		{name: "create prior byte sentinel", operation: RecoveryOperationCreate, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPriorBytes = 0
		}},
		{name: "operation and prior arm", operation: RecoveryOperationCreate, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.Operation = RecoveryOperationOverwrite
			envelope.ExpectedPriorKind = ExpectedTargetPresent
			envelope.ExpectedPriorDigest = digest("c")
			envelope.ExpectedPriorBytes = 13
		}},
		{name: "overwrite prior digest", operation: RecoveryOperationOverwrite, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPriorDigest = digest("e")
		}},
		{name: "overwrite post digest", operation: RecoveryOperationOverwrite, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPostIdentityDigest = digest("e")
		}},
		{name: "overwrite post bytes", operation: RecoveryOperationOverwrite, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPostBytes++
		}},
		{name: "overwrite prior bytes", operation: RecoveryOperationOverwrite, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPriorBytes++
		}},
		{name: "skip prior and post digest", operation: RecoveryOperationSkip, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPriorDigest = digest("e")
			envelope.ExpectedPostIdentityDigest = digest("e")
		}},
		{name: "skip prior bytes", operation: RecoveryOperationSkip, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPriorBytes++
		}},
		{name: "skip post byte sentinel", operation: RecoveryOperationSkip, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPostBytes = 0
		}},
		{name: "delete prior digest", operation: RecoveryOperationDelete, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPriorDigest = digest("e")
		}},
		{name: "delete empty post digest", operation: RecoveryOperationDelete, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPostIdentityDigest = digest("e")
		}},
		{name: "delete post byte sentinel", operation: RecoveryOperationDelete, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPostBytes = 0
		}},
		{name: "delete prior byte sentinel", operation: RecoveryOperationDelete, mutate: func(envelope *targetLocatorEnvelope) {
			envelope.ExpectedPriorBytes = 0
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			assertTampered(t, baseBinding(testCase.operation), locator, testCase.mutate)
		})
	}

	workspaceLocator := "jobs/workspace-a"
	isolated := baseBinding(RecoveryOperationCreate)
	isolated.TargetMode = TargetModeIsolated
	isolated.WorkspaceBindingDigest = digest("f")
	isolated.WorkspaceRelativeLocator = workspaceLocator
	isolated.TargetObjectDigest = mustTargetPathDigest(
		t,
		isolated.RootID,
		isolated.RootLocatorDigest,
		workspaceLocator+"/"+locator,
	)
	isolated = completeTargetLocatorBindingForTest(t, isolated, locator)
	for _, testCase := range []struct {
		name   string
		mutate func(*targetLocatorEnvelope)
	}{
		{name: "isolated suffix and full object", mutate: func(envelope *targetLocatorEnvelope) {
			envelope.RelativeLocator = "items/item-b"
			envelope.TargetObjectDigest = mustTargetPathDigest(
				t,
				envelope.RootID,
				envelope.RootLocatorDigest,
				envelope.WorkspaceRelativeLocator+"/"+envelope.RelativeLocator,
			)
		}},
		{name: "isolated workspace and full object", mutate: func(envelope *targetLocatorEnvelope) {
			envelope.WorkspaceRelativeLocator = "jobs/workspace-b"
			envelope.TargetObjectDigest = mustTargetPathDigest(
				t,
				envelope.RootID,
				envelope.RootLocatorDigest,
				envelope.WorkspaceRelativeLocator+"/"+envelope.RelativeLocator,
			)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertTampered(t, isolated, locator, testCase.mutate)
		})
	}
}

func TestOperationProductsCanonicalGoldenAndDeletePolicy(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	pointID := strings.Repeat("1", 32)
	limits := RecoveryOperationLimits{MaxRows: 8, MaxItems: 8, MaxBytes: 1_024, MaxImpactRows: 8}
	withTarget := func(mode TargetMode, locator string, operation RecoveryOperation) RecoveryOperation {
		bindRecoveryOperationTargetForTest(t, mode, "root-a", digest("4"), locator, &operation)
		return operation
	}

	products, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode:     TargetModeInPlace,
		ConflictPolicy: ConflictExactMirror,
		Limits:         limits,
		Operations: []RecoveryOperation{
			withTarget(TargetModeInPlace, "items/delete", RecoveryOperation{
				Kind:                       RecoveryOperationDelete,
				TargetPathDigest:           digest("c"),
				ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("d")},
				ExpectedPostIdentityDigest: "",
				ExpectedPostBytes:          -1,
				ExpectedPriorBytes:         -1,
				Source:                     RecoveryOperationSource{Kind: RecoveryOperationSourceNone},
				DisplayClass:               RecoveryDisplayClassRegular,
				EstimatedBytes:             23,
			}),
			withTarget(TargetModeInPlace, "items/create", RecoveryOperation{
				Kind:                       RecoveryOperationCreate,
				TargetPathDigest:           digest("b"),
				ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetAbsent},
				ExpectedPostIdentityDigest: digest("e"),
				ExpectedPostBytes:          17,
				ExpectedPriorBytes:         -1,
				Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
					RecoveryPointID: pointID, EntryID: digest("e"),
				}},
				DisplayClass:   RecoveryDisplayClassRegular,
				EstimatedBytes: 17,
			}),
			withTarget(TargetModeInPlace, "items/overwrite", RecoveryOperation{
				Kind:                       RecoveryOperationOverwrite,
				TargetPathDigest:           digest("a"),
				ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("f")},
				ExpectedPostIdentityDigest: digest("0"),
				ExpectedPostBytes:          19,
				ExpectedPriorBytes:         29,
				Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
					RecoveryPointID: pointID, EntryID: digest("0"),
				}},
				DisplayClass:   RecoveryDisplayClassRegular,
				EstimatedBytes: 19,
			}),
		},
	})
	if err != nil {
		t.Fatalf("NewOperationProducts() error = %v", err)
	}
	if got, want := products.OperationSetDigest, "b196df47f320cfa51def30b2f55eef710e42807027098e531ec123a46d3ac2f8"; got != want {
		t.Fatalf("operation-set digest = %q, want golden %q", got, want)
	}
	if got, want := products.DeleteSetDigest, "48b4aa4cb36e095e0971eae10e61118dece133ef7055c158a9d1dd4ec8098d8b"; got != want {
		t.Fatalf("delete-set digest = %q, want golden %q", got, want)
	}
	if got, want := products.Impact, (RecoveryImpactSummary{
		CreateCount: 1, OverwriteCount: 1, DeleteCount: 1, EstimatedItems: 3, EstimatedBytes: 59,
	}); got.CreateCount != want.CreateCount || got.OverwriteCount != want.OverwriteCount ||
		got.DeleteCount != want.DeleteCount || got.SkipCount != want.SkipCount ||
		got.EstimatedItems != want.EstimatedItems || got.EstimatedBytes != want.EstimatedBytes {
		t.Fatalf("impact = %#v, want counts/estimates %#v", got, want)
	}
	if got := []RecoveryOperationKind{products.Rows[0].Kind, products.Rows[1].Kind, products.Rows[2].Kind}; fmt.Sprint(got) != fmt.Sprint([]RecoveryOperationKind{RecoveryOperationOverwrite, RecoveryOperationCreate, RecoveryOperationDelete}) {
		t.Fatalf("rows were not canonical byte-order sorted: %v", got)
	}

	withoutDeletes, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetModeIsolated, ConflictPolicy: ConflictSkipExisting, Limits: limits,
		Operations: []RecoveryOperation{withTarget(TargetModeIsolated, "items/create", RecoveryOperation{
			Kind:                       RecoveryOperationCreate,
			TargetPathDigest:           digest("9"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetAbsent},
			ExpectedPostIdentityDigest: digest("8"),
			ExpectedPostBytes:          1,
			ExpectedPriorBytes:         -1,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest("8"),
			}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: 1,
		})},
	})
	if err != nil {
		t.Fatalf("isolated non-delete product error = %v", err)
	}
	if withoutDeletes.DeleteSetDigest != EmptyDeleteSetDigest {
		t.Fatalf("non-exact-mirror delete digest = %q, want canonical empty %q", withoutDeletes.DeleteSetDigest, EmptyDeleteSetDigest)
	}

	_, err = NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetModeIsolated, ConflictPolicy: ConflictExactMirror, Limits: limits,
		Operations: []RecoveryOperation{withTarget(TargetModeIsolated, "items/delete", RecoveryOperation{
			Kind:                       RecoveryOperationDelete,
			TargetPathDigest:           digest("7"),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("6")},
			ExpectedPostIdentityDigest: "",
			ExpectedPostBytes:          -1,
			ExpectedPriorBytes:         -1,
			Source:                     RecoveryOperationSource{Kind: RecoveryOperationSourceNone},
			DisplayClass:               RecoveryDisplayClassRegular,
		})},
	})
	if !errors.Is(err, ErrInvalidRecoveryOperation) {
		t.Fatalf("isolated delete error = %v, want ErrInvalidRecoveryOperation", err)
	}
}

func cloneRecoveryOperationProductsInput(input RecoveryOperationProductsInput) RecoveryOperationProductsInput {
	clone := input
	clone.Operations = make([]RecoveryOperation, len(input.Operations))
	for index, operation := range input.Operations {
		clone.Operations[index] = operation
		if operation.Source.AssetRef != nil {
			assetRef := *operation.Source.AssetRef
			clone.Operations[index].Source.AssetRef = &assetRef
		}
	}
	return clone
}

func TestPreflightSecurityDecisionMatrixAndRevisionDrift(t *testing.T) {
	digest := strings.Repeat("a", sha256DigestLength)
	policyRevision := "policy-revision-1"

	tests := []struct {
		name          string
		findings      []SecurityFinding
		overridable   []SecurityFindingCategory
		wantKind      SecurityDecisionKind
		wantCandidate bool
	}{
		{name: "clean is the only direct allow", wantKind: SecurityDecisionAllowClean},
		{
			name:        "known policy overridable finding remains blocked with candidate",
			findings:    []SecurityFinding{{Category: SecurityFindingMalware}},
			overridable: []SecurityFindingCategory{SecurityFindingMalware},
			wantKind:    SecurityDecisionBlock, wantCandidate: true,
		},
		{
			name:        "known but non-overridable stays blocked",
			findings:    []SecurityFinding{{Category: SecurityFindingSuspicious}},
			overridable: []SecurityFindingCategory{SecurityFindingMalware},
			wantKind:    SecurityDecisionBlock,
		},
		{
			name:        "unknown category stays blocked even if policy lists unknown",
			findings:    []SecurityFinding{{Category: SecurityFindingCategory("recognizable-raw-finding")}},
			overridable: []SecurityFindingCategory{SecurityFindingCategory("recognizable-raw-finding")},
			wantKind:    SecurityDecisionBlock,
		},
		{
			name:        "one non-overridable finding closes a mixed set",
			findings:    []SecurityFinding{{Category: SecurityFindingMalware}, {Category: SecurityFindingTestSignature}},
			overridable: []SecurityFindingCategory{SecurityFindingMalware},
			wantKind:    SecurityDecisionBlock,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			disposition := SecurityFindingDispositionClean
			if len(testCase.findings) > 0 {
				disposition = SecurityFindingDispositionBlocked
			}
			product, err := NewPreflightSecurityDecision(PreflightSecurityDecisionInput{
				FindingSetDigest:      digest,
				PolicyRevision:        policyRevision,
				Findings:              testCase.findings,
				OverridableCategories: testCase.overridable,
			})
			if err != nil {
				t.Fatalf("NewPreflightSecurityDecision() error = %v", err)
			}
			if product.Decision.Kind != testCase.wantKind {
				t.Fatalf("decision kind = %q, want %q", product.Decision.Kind, testCase.wantKind)
			}
			if got := product.OverrideCandidate != nil; got != testCase.wantCandidate {
				t.Fatalf("override candidate present = %t, want %t: %#v", got, testCase.wantCandidate, product)
			}
			if product.Decision.FindingSetDigest != digest || product.Decision.PolicyRevision != policyRevision ||
				!validDigest(product.Decision.DecisionDigest) {
				t.Fatalf("decision binding is incomplete: %#v", product.Decision)
			}
			if product.OverrideCandidate != nil {
				if !validDigest(product.OverrideCandidate.BindingDigest) ||
					product.OverrideCandidate.FindingSetDigest != digest ||
					product.OverrideCandidate.PolicyRevision != policyRevision {
					t.Fatalf("override candidate binding is incomplete: %#v", product.OverrideCandidate)
				}
			}

			if err := product.ValidateBinding(digest, policyRevision, disposition); err != nil {
				t.Fatalf("ValidateBinding(current) error = %v", err)
			}
			if err := product.ValidateBinding(strings.Repeat("b", sha256DigestLength), policyRevision, disposition); !errors.Is(err, ErrRecoveryPreflightConflict) {
				t.Fatalf("finding drift error = %v, want ErrRecoveryPreflightConflict", err)
			}
			if err := product.ValidateBinding(digest, "policy-revision-2", disposition); !errors.Is(err, ErrRecoveryPreflightConflict) {
				t.Fatalf("policy drift error = %v, want ErrRecoveryPreflightConflict", err)
			}

			encoded, err := json.Marshal(product)
			if err != nil {
				t.Fatalf("marshal decision product: %v", err)
			}
			if strings.Contains(string(encoded), "recognizable-raw-finding") {
				t.Fatalf("unknown raw finding category leaked: %s", encoded)
			}
		})
	}
}

func TestPreflightSecurityDecisionRejectsRehashedKindFindingContradiction(t *testing.T) {
	findingSetDigest := strings.Repeat("a", sha256DigestLength)
	policyRevision := "policy-revision-1"
	tests := []struct {
		name     string
		findings []SecurityFinding
		tampered SecurityDecisionKind
	}{
		{
			name:     "blocked finding cannot become clean allow",
			findings: []SecurityFinding{{Category: SecurityFindingSuspicious}},
			tampered: SecurityDecisionAllowClean,
		},
		{
			name:     "clean finding set cannot become blocked",
			tampered: SecurityDecisionBlock,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			disposition := SecurityFindingDispositionClean
			if len(testCase.findings) > 0 {
				disposition = SecurityFindingDispositionBlocked
			}
			product, err := NewPreflightSecurityDecision(PreflightSecurityDecisionInput{
				FindingSetDigest: findingSetDigest,
				PolicyRevision:   policyRevision,
				Findings:         testCase.findings,
			})
			if err != nil {
				t.Fatalf("NewPreflightSecurityDecision() error = %v", err)
			}
			if product.OverrideCandidate != nil {
				t.Fatalf("fixture unexpectedly has override candidate: %#v", product.OverrideCandidate)
			}
			if product.FindingCount != len(testCase.findings) {
				t.Fatalf("finding count = %d, want %d", product.FindingCount, len(testCase.findings))
			}

			product.Decision.Kind = testCase.tampered
			product.Decision.DecisionDigest = framedDigest(
				securityDecisionDigestDomain,
				string(product.Decision.Kind),
				product.Decision.FindingSetDigest,
				product.Decision.PolicyRevision,
				strconv.Itoa(product.FindingCount),
			)
			if err := product.ValidateBinding(findingSetDigest, policyRevision, disposition); !errors.Is(err, ErrInvalidSecurityDecision) {
				t.Fatalf("ValidateBinding(rehashed contradiction) error = %v, want ErrInvalidSecurityDecision", err)
			}
		})
	}
}

func TestPreflightSecurityDecisionRejectsBlockedFindingsRewrittenAsRehashedClean(t *testing.T) {
	findingSetDigest := strings.Repeat("a", sha256DigestLength)
	policyRevision := "policy-revision-1"
	product, err := NewPreflightSecurityDecision(PreflightSecurityDecisionInput{
		FindingSetDigest: findingSetDigest,
		PolicyRevision:   policyRevision,
		Findings:         []SecurityFinding{{Category: SecurityFindingSuspicious}},
	})
	if err != nil {
		t.Fatalf("NewPreflightSecurityDecision() error = %v", err)
	}
	if product.Decision.Kind != SecurityDecisionBlock || product.FindingCount != 1 {
		t.Fatalf("blocked fixture = %#v, want one blocked finding", product)
	}

	product.Decision.Kind = SecurityDecisionAllowClean
	product.FindingCount = 0
	product.OverrideCandidate = nil
	product.Decision.DecisionDigest = framedDigest(
		securityDecisionDigestDomain,
		string(product.Decision.Kind),
		product.Decision.FindingSetDigest,
		product.Decision.PolicyRevision,
		strconv.Itoa(product.FindingCount),
	)

	if err := product.ValidateBinding(
		findingSetDigest,
		policyRevision,
		SecurityFindingDispositionBlocked,
	); !errors.Is(err, ErrInvalidSecurityDecision) {
		t.Fatalf("ValidateBinding(rehashed blocked-to-clean rewrite) error = %v, want ErrInvalidSecurityDecision", err)
	}
}

func TestOperationProductsRejectDuplicatesArmsAndLimits(t *testing.T) {
	digest := func(character string) string { return strings.Repeat(character, sha256DigestLength) }
	pointID := strings.Repeat("2", 32)
	create := func(target, entry string, bytes int64) RecoveryOperation {
		operation := RecoveryOperation{
			Kind:                       RecoveryOperationCreate,
			TargetPathDigest:           digest(target),
			ExpectedPrior:              ExpectedTargetIdentity{Kind: ExpectedTargetAbsent},
			ExpectedPostIdentityDigest: digest(entry),
			ExpectedPostBytes:          bytes,
			ExpectedPriorBytes:         -1,
			Source: RecoveryOperationSource{Kind: RecoveryOperationSourceAssetRef, AssetRef: &backupasset.AssetRef{
				RecoveryPointID: pointID, EntryID: digest(entry),
			}},
			DisplayClass: RecoveryDisplayClassRegular, EstimatedBytes: bytes,
		}
		bindRecoveryOperationTargetForTest(
			t, TargetModeInPlace, "root-a", digest("f"), "items/item-"+target, &operation,
		)
		return operation
	}
	base := RecoveryOperationProductsInput{
		TargetMode: TargetModeInPlace, ConflictPolicy: ConflictOverwriteSelected,
		Limits:     RecoveryOperationLimits{MaxRows: 4, MaxItems: 4, MaxBytes: 100, MaxImpactRows: 4},
		Operations: []RecoveryOperation{create("a", "b", 1), create("c", "d", 2)},
	}

	invalid := []struct {
		name    string
		mutate  func(*RecoveryOperationProductsInput)
		wantErr error
	}{
		{
			name: "duplicate target digest",
			mutate: func(input *RecoveryOperationProductsInput) {
				input.Operations[1].TargetPathDigest = input.Operations[0].TargetPathDigest
			},
			wantErr: ErrInvalidRecoveryOperation,
		},
		{
			name: "duplicate source provenance",
			mutate: func(input *RecoveryOperationProductsInput) {
				input.Operations[1].Source.AssetRef.EntryID = input.Operations[0].Source.AssetRef.EntryID
			},
			wantErr: ErrInvalidRecoveryOperation,
		},
		{
			name: "dual source arm",
			mutate: func(input *RecoveryOperationProductsInput) {
				input.Operations[0].Source.Kind = RecoveryOperationSourceNone
			},
			wantErr: ErrInvalidRecoveryOperation,
		},
		{
			name: "create present prior",
			mutate: func(input *RecoveryOperationProductsInput) {
				input.Operations[0].ExpectedPrior = ExpectedTargetIdentity{Kind: ExpectedTargetPresent, Digest: digest("e")}
			},
			wantErr: ErrInvalidRecoveryOperation,
		},
		{
			name:    "row limit",
			mutate:  func(input *RecoveryOperationProductsInput) { input.Limits.MaxRows = 1 },
			wantErr: ErrRecoveryOperationLimit,
		},
		{
			name:    "item limit",
			mutate:  func(input *RecoveryOperationProductsInput) { input.Limits.MaxItems = 1 },
			wantErr: ErrRecoveryOperationLimit,
		},
		{
			name:    "byte limit",
			mutate:  func(input *RecoveryOperationProductsInput) { input.Limits.MaxBytes = 2 },
			wantErr: ErrRecoveryOperationLimit,
		},
		{
			name:    "impact row limit",
			mutate:  func(input *RecoveryOperationProductsInput) { input.Limits.MaxImpactRows = 1 },
			wantErr: ErrRecoveryImpactLimit,
		},
	}

	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := cloneRecoveryOperationProductsInput(base)
			testCase.mutate(&candidate)
			_, err := NewOperationProducts(candidate)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("NewOperationProducts() error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}
