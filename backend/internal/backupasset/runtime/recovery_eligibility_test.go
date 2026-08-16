package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/backupasset/processing/capabilityspec"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/recovery"
	"xirang/backend/internal/backupasset/repository"
	"xirang/backend/internal/model"

	"gorm.io/gorm"
)

func TestManagedRecoveryEligibilitySourceAdapterCopiesClosedRepositoryObservation(t *testing.T) {
	source := &managedRecoveryDeclaredSourceFake{}
	request := provider.RecoverySourceAuthorityRequest{
		Provider: backupasset.ProviderRsync,
		RsyncRef: provider.RsyncRestoreSourceRef{
			PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
			RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
			CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
			SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
		},
	}
	repositoryObservation := repository.RecoveryRsyncSourceAuthorityObservation{
		Provider: backupasset.ProviderRsync, RepositoryID: request.RsyncRef.RepositoryID,
		RecoveryPointID: request.RsyncRef.RecoveryPointID, CatalogGenerationID: request.RsyncRef.CatalogGenerationID,
		SourceRevisionDigest: request.RsyncRef.SourceRevisionDigest, ManifestDigest: request.RsyncRef.ManifestDigest,
		RepositoryCapabilityRevision: 9, CapabilityRevision: 10,
		SourceAccessIdentity: "source-access-v1", SourceFingerprint: strings.Repeat("9", 64),
		ManagedRootIdentity: strings.Repeat("a", 64), RepositoryBindingRevision: strings.Repeat("b", 64),
		ProvenanceRevision: strings.Repeat("c", 64),
	}
	observer := &managedRecoveryEligibilityRepositoryObserverFake{
		source: source, observation: repositoryObservation,
	}
	adapter, err := newManagedRecoveryEligibilitySourceAdapter(observer, &managedRecoveryEligibilitySourceDurableFake{})
	if err != nil {
		t.Fatalf("new source adapter: %v", err)
	}

	gotSource, got, err := adapter.ObserveRecoveryEligibilitySource(context.Background(), request)
	if err != nil {
		t.Fatalf("observe source: %v", err)
	}
	if gotSource != source || observer.request != request {
		t.Fatalf("source/request were not transferred exactly")
	}
	want := recovery.RecoveryEligibilitySourceObservation{
		RepositoryCapabilityRevision: 9, CapabilityRevision: 10,
		SourceAccessIdentity: "source-access-v1", SourceFingerprint: strings.Repeat("9", 64),
		ManagedRootIdentity: strings.Repeat("a", 64), RepositoryBindingRevision: strings.Repeat("b", 64),
		ProvenanceRevision: strings.Repeat("c", 64),
	}
	if got != want {
		t.Fatalf("closed source observation=%#v, want exact copied scalar", got)
	}
}

func TestManagedRecoveryEligibilitySourceAdapterRevalidatesInsideCallerTransaction(t *testing.T) {
	db := openRuntimeTestDB(t)
	durable := &managedRecoveryEligibilitySourceDurableFake{}
	adapter, err := newManagedRecoveryEligibilitySourceAdapter(
		&managedRecoveryEligibilityRepositoryObserverFake{}, durable,
	)
	if err != nil {
		t.Fatalf("new source adapter: %v", err)
	}
	binding := recovery.RecoveryAuthorityBinding{
		Provider: backupasset.ProviderRsync, PlanID: strings.Repeat("1", 32),
		PlanBindingDigest: strings.Repeat("2", 64), RepositoryID: strings.Repeat("3", 32),
		RecoveryPointID: strings.Repeat("4", 32), CatalogGenerationID: strings.Repeat("5", 32),
		SelectionDigest: strings.Repeat("6", 64), SourceRevisionDigest: strings.Repeat("7", 64),
		ManifestDigest: strings.Repeat("8", 64),
		SourceRef: provider.RsyncRestoreSourceRef{
			PlanID: strings.Repeat("1", 32), PlanBindingDigest: strings.Repeat("2", 64),
			RepositoryID: strings.Repeat("3", 32), RecoveryPointID: strings.Repeat("4", 32),
			CatalogGenerationID: strings.Repeat("5", 32), SelectionDigest: strings.Repeat("6", 64),
			SourceRevisionDigest: strings.Repeat("7", 64), ManifestDigest: strings.Repeat("8", 64),
		},
	}
	observation := recovery.RecoveryEligibilitySourceObservation{
		RepositoryCapabilityRevision: 9, CapabilityRevision: 10,
		SourceAccessIdentity: "source-access-v1", SourceFingerprint: strings.Repeat("9", 64),
		ManagedRootIdentity: strings.Repeat("a", 64), RepositoryBindingRevision: strings.Repeat("b", 64),
		ProvenanceRevision: strings.Repeat("c", 64),
	}
	var callerTx *gorm.DB
	err = db.Transaction(func(tx *gorm.DB) error {
		callerTx = tx
		return adapter.RevalidateRecoveryEligibilitySourceTx(context.Background(), tx, binding, observation)
	})
	if err != nil {
		t.Fatalf("revalidate source: %v", err)
	}
	if durable.tx != callerTx || durable.calls != 1 || durable.request.Provider != binding.Provider ||
		durable.request.RsyncRef != binding.SourceRef {
		t.Fatalf("durable revalidation escaped or changed caller transaction: %+v", durable)
	}
	if durable.expected.RepositoryBindingRevision != observation.RepositoryBindingRevision ||
		durable.expected.ProvenanceRevision != observation.ProvenanceRevision ||
		durable.expected.Provider != binding.Provider || durable.expected.RepositoryID != binding.RepositoryID {
		t.Fatalf("durable expected source product=%#v", durable.expected)
	}
}

func TestManagedRecoveryEligibilitySourceAdapterFailsClosedWithoutDurableSeam(t *testing.T) {
	_, err := newManagedRecoveryEligibilitySourceAdapter(
		&managedRecoveryEligibilityRepositoryObserverFake{}, nil,
	)
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) || strings.Contains(err.Error(), "secret-source") {
		t.Fatalf("missing durable seam error=%v, want stable redacted unavailable", err)
	}
}

func TestManagedRecoveryEligibilitySecurityAdapterAggregatesEveryPlanItemAndRevalidatesDurably(t *testing.T) {
	now := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryPlanItem{}); err != nil {
		t.Fatal(err)
	}
	bundleFingerprint := strings.Repeat("1", 64)
	if err := db.Create(&model.BackupAssetUpdaterMetadata{
		ID: strings.Repeat("2", 32), SourceKind: "admin_registered", SourceID: "security-test",
		Version: "1.0.0", ManifestDigest: strings.Repeat("3", 64),
		SigningKeyFingerprint: strings.Repeat("4", 64), BundleFingerprint: bundleFingerprint,
		State: "active", ActivatedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	pipeline, err := (&managedProcessingRuntime{db: db}).activePipelineFingerprint(
		context.Background(), capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1,
	)
	if err != nil || pipeline == "" {
		t.Fatalf("active malware pipeline=%q err=%v", pipeline, err)
	}
	plan := managedRecoveryEligibilitySecurityPlan(now)
	items := managedRecoveryEligibilitySecurityItems(now, plan)
	assets := []content.AuthorizedAsset{
		managedRecoveryEligibilitySecurityAsset(items[0], plan, strings.Repeat("5", 64), 4096),
		managedRecoveryEligibilitySecurityAsset(items[1], plan, strings.Repeat("6", 64), 2048),
	}
	payloads := make(map[string][]byte, len(items))
	evidence := make([]managedRecoveryEligibilitySecurityEvidenceRow, 0, len(items))
	states := []capabilityspec.ScanState{capabilityspec.ScanNoFinding, capabilityspec.ScanFinding}
	for index := range items {
		payload, row := seedManagedRecoveryEligibilitySecurityEvidence(
			t, db, now, items[index], assets[index], pipeline, bundleFingerprint, states[index], index,
		)
		payloads[row.ArtifactID] = payload
		evidence = append(evidence, row)
	}
	binding := managedRecoveryEligibilitySecurityBinding(plan)
	binding.SecurityFindingSetDigest = managedRecoveryEligibilitySecurityDigestForTest(
		binding, bundleFingerprint, evidence,
	)
	plan.SecurityFindingSetDigest = binding.SecurityFindingSetDigest
	if err := db.Create(&model.User{
		ID: plan.RequesterID, Username: "recovery-owner", PasswordHash: "unused", Role: "admin",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	// Reverse insert order: authority order must come from durable Ordinal.
	if err := db.Create(&items[1]).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&items[0]).Error; err != nil {
		t.Fatal(err)
	}
	reader := &managedRecoveryEligibilitySecurityReaderFake{payloads: payloads}
	runtime := &managedProcessingRuntime{
		db: db, now: func() time.Time { return now },
		authorize: managedRecoveryEligibilitySecurityAuthorizerFake{assets: map[backupasset.AssetRef]content.AuthorizedAsset{
			assets[0].Ref: assets[0], assets[1].Ref: assets[1],
		}},
		malwareEvidence: reader,
	}
	adapter, err := newManagedRecoveryEligibilitySecurityAdapter(runtime)
	if err != nil {
		t.Fatalf("new security adapter: %v", err)
	}

	observed, err := adapter.ObserveRecoveryEligibilitySecurity(context.Background(), binding)
	if err != nil {
		t.Fatalf("observe plan security: %v (artifact reads=%d)", err, reader.calls)
	}
	if observed.PolicyRevision != processingSecurityPolicyRevision ||
		observed.FindingSetDigest != binding.SecurityFindingSetDigest ||
		observed.Disposition != recovery.SecurityFindingDispositionBlocked || !observed.Complete ||
		!observed.ObservedAt.Equal(now) || reader.calls != len(items) {
		t.Fatalf("plan security observation=%#v reads=%d", observed, reader.calls)
	}
	readsBeforeRevalidation := reader.calls
	if err := db.Transaction(func(tx *gorm.DB) error {
		return adapter.RevalidateRecoveryEligibilitySecurityTx(context.Background(), tx, binding, observed)
	}); err != nil {
		t.Fatalf("durable security revalidation: %v", err)
	}
	if reader.calls != readsBeforeRevalidation {
		t.Fatalf("tx revalidation performed external artifact reads: before=%d after=%d", readsBeforeRevalidation, reader.calls)
	}

	if err := db.Model(&model.BackupAssetDerivedArtifact{}).Where("id = ?", evidence[1].ArtifactID).
		Update("plaintext_digest", strings.Repeat("f", 64)).Error; err != nil {
		t.Fatal(err)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		return adapter.RevalidateRecoveryEligibilitySecurityTx(context.Background(), tx, binding, observed)
	})
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("one-item durable drift error=%v, want conflict", err)
	}
}

func TestManagedRecoveryPlanSecurityAuthorityBuildsBoundedPreCreateEvidence(t *testing.T) {
	now := time.Date(2026, 8, 16, 6, 0, 0, 0, time.UTC)
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	bundleFingerprint := strings.Repeat("1", 64)
	if err := db.Create(&model.BackupAssetUpdaterMetadata{
		ID: strings.Repeat("2", 32), SourceKind: "admin_registered", SourceID: "security-precreate-test",
		Version: "1.0.0", ManifestDigest: strings.Repeat("3", 64),
		SigningKeyFingerprint: strings.Repeat("4", 64), BundleFingerprint: bundleFingerprint,
		State: "active", ActivatedAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	pipeline, err := (&managedProcessingRuntime{db: db}).activePipelineFingerprint(
		context.Background(), capabilityspec.CapabilityMalwareScan, capabilityspec.ProfileSignatureScanV1,
	)
	if err != nil || pipeline == "" {
		t.Fatalf("active malware pipeline=%q err=%v", pipeline, err)
	}
	plan := managedRecoveryEligibilitySecurityPlan(now)
	items := managedRecoveryEligibilitySecurityItems(now, plan)
	assets := []content.AuthorizedAsset{
		managedRecoveryEligibilitySecurityAsset(items[0], plan, strings.Repeat("5", 64), 4096),
		managedRecoveryEligibilitySecurityAsset(items[1], plan, strings.Repeat("6", 64), 2048),
	}
	assets[0].Path = "private/docs/report.txt"
	assets[1].Path = "private/docs/summary.txt"
	payloads := make(map[string][]byte, len(items))
	states := []capabilityspec.ScanState{capabilityspec.ScanNoFinding, capabilityspec.ScanFinding}
	for index := range items {
		payload, row := seedManagedRecoveryEligibilitySecurityEvidence(
			t, db, now, items[index], assets[index], pipeline, bundleFingerprint, states[index], index,
		)
		payloads[row.ArtifactID] = payload
	}
	if err := db.Create(&model.User{
		ID: 41, Username: "recovery-owner", PasswordHash: "unused", Role: "admin",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	selection, err := recovery.NewExactSelection(recovery.ExactSelectionInput{
		RepositoryID: plan.RepositoryID, RecoveryPointID: plan.RecoveryPointID,
		CatalogGenerationID: plan.CatalogGenerationID,
		AssetRefs:           []backupasset.AssetRef{assets[1].Ref, assets[0].Ref},
		SourceRevision: recovery.SourceRevision{
			Kind: recovery.SourceRevisionImmutable,
			Immutable: &recovery.ImmutableSourceRevision{
				LocatorDigest: plan.ImmutableLocatorDigest, ManifestDigest: plan.ImmutableManifestDigest,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := &managedRecoveryEligibilitySecurityReaderFake{payloads: payloads}
	adapter, err := newManagedRecoveryEligibilitySecurityAdapter(&managedProcessingRuntime{
		db: db, now: func() time.Time { return now },
		authorize: managedRecoveryEligibilitySecurityAuthorizerFake{assets: map[backupasset.AssetRef]content.AuthorizedAsset{
			assets[0].Ref: assets[0], assets[1].Ref: assets[1],
		}},
		malwareEvidence: reader,
	})
	if err != nil {
		t.Fatal(err)
	}

	evidence, err := adapter.ObserveRecoveryPlanSecurity(context.Background(), recovery.RecoveryPlanSecurityRequest{
		RequesterID: 41, Selection: selection, MaxItems: 2, MaxBytes: 8192,
	})
	if err != nil {
		t.Fatalf("ObserveRecoveryPlanSecurity() error=%v", err)
	}
	if evidence.SelectionDigest != selection.SelectionDigest || evidence.Provider != backupasset.ProviderRsync ||
		evidence.CapabilityRevision == "" || evidence.Security.Decision.Kind != recovery.SecurityDecisionBlock ||
		len(evidence.Items) != 2 || evidence.Items[0].AssetRef != selection.AssetRefs[0] ||
		evidence.Items[0].TargetRelativeLocator != assets[0].Path || evidence.Items[0].ContentDigest != assets[0].EntryFingerprint ||
		!evidence.ObservedAt.Equal(now) || reader.calls != 2 {
		t.Fatalf("pre-create security evidence=%#v reads=%d", evidence, reader.calls)
	}
	encoded, marshalErr := json.Marshal(evidence)
	formatted := fmt.Sprintf("%+v %#v", evidence, evidence)
	for _, private := range []string{assets[0].Path, assets[1].Path, assets[0].EntryFingerprint} {
		if marshalErr != nil || strings.Contains(string(encoded), private) || strings.Contains(formatted, private) {
			t.Fatalf("private Processing evidence escaped JSON/formatting: json=%q formatted=%q err=%v", encoded, formatted, marshalErr)
		}
	}
}

func TestManagedRecoveryEligibilitySecurityAdapterRejectsPartialPlanWithoutLeakingArtifact(t *testing.T) {
	now := time.Date(2026, 8, 15, 4, 0, 0, 0, time.UTC)
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.User{}, &model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryPlanItem{}); err != nil {
		t.Fatal(err)
	}
	plan := managedRecoveryEligibilitySecurityPlan(now)
	plan.SecurityFindingSetDigest = strings.Repeat("a", 64)
	items := managedRecoveryEligibilitySecurityItems(now, plan)
	if err := db.Create(&model.User{ID: plan.RequesterID, Username: "owner", PasswordHash: "unused", Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	runtime := &managedProcessingRuntime{
		db: db, now: func() time.Time { return now },
		authorize:       managedRecoveryEligibilitySecurityAuthorizerFake{err: errors.New("secret-artifact-path")},
		malwareEvidence: &managedRecoveryEligibilitySecurityReaderFake{},
	}
	adapter, err := newManagedRecoveryEligibilitySecurityAdapter(runtime)
	if err != nil {
		t.Fatalf("new security adapter: %v", err)
	}
	_, err = adapter.ObserveRecoveryEligibilitySecurity(
		context.Background(), managedRecoveryEligibilitySecurityBinding(plan),
	)
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) || strings.Contains(err.Error(), "secret-artifact-path") {
		t.Fatalf("partial security error=%v, want redacted unavailable", err)
	}
}

func TestManagedRecoveryEligibilitySecurityPlanAllowsExactMirrorDeleteImpact(t *testing.T) {
	now := time.Date(2026, 8, 16, 8, 0, 0, 0, time.UTC)
	db := openProcessingRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryPlanItem{}); err != nil {
		t.Fatal(err)
	}
	plan := managedRecoveryEligibilitySecurityPlan(now)
	plan.TargetMode = string(recovery.TargetModeInPlace)
	plan.ConflictPolicy = string(recovery.ConflictExactMirror)
	plan.SecurityFindingSetDigest = strings.Repeat("0", 64)
	items := managedRecoveryEligibilitySecurityItems(now, plan)
	plan.EstimatedItems = int64(len(items) + 1)
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot managedRecoveryEligibilitySecurityPlanSnapshot
	err := db.Transaction(func(tx *gorm.DB) error {
		var loadErr error
		snapshot, loadErr = loadManagedRecoveryEligibilitySecurityPlanTx(
			context.Background(), tx, managedRecoveryEligibilitySecurityBinding(plan), false, false,
		)
		return loadErr
	})
	if err != nil || len(snapshot.items) != len(items) {
		t.Fatalf("load exact-mirror security plan items=%d error=%v", len(snapshot.items), err)
	}
}

func managedRecoveryEligibilitySecurityPlan(now time.Time) model.BackupAssetRecoveryPlan {
	return model.BackupAssetRecoveryPlan{
		ID: strings.Repeat("1", 32), RequesterID: 41, Endpoint: "recovery-test",
		IdempotencyKeyDigest: strings.Repeat("2", 64), RepositoryID: strings.Repeat("3", 32),
		RecoveryPointID: strings.Repeat("4", 32), SourceRevisionDigest: strings.Repeat("5", 64),
		SourceRevisionKind: "immutable", ImmutableLocatorDigest: strings.Repeat("6", 64),
		ImmutableManifestDigest: strings.Repeat("7", 64), CatalogGenerationID: strings.Repeat("8", 32),
		TargetMode: string(recovery.TargetModeIsolated), TargetNodeID: 9, TargetRootID: "recovery-root",
		RootLocatorDigest: strings.Repeat("9", 64), PathDigest: strings.Repeat("a", 64),
		TargetBaseRevision: "node-v1", CredentialScopeRevision: "credential-v1",
		RootRevision: "root-v1", FilesystemRevision: "filesystem-v1",
		SelectionDigest: strings.Repeat("b", 64), BindingDigest: strings.Repeat("c", 64),
		CapabilityRevision: "capability-v1", ConflictPolicy: string(recovery.ConflictFailOnConflict),
		OperationSetDigest: strings.Repeat("d", 64), DeleteSetDigest: strings.Repeat("e", 64),
		SecurityDecision:       string(recovery.SecurityDecisionBlock),
		SecurityDecisionDigest: strings.Repeat("f", 64), SecurityPolicyRevision: processingSecurityPolicyRevision,
		PreflightRevision: "preflight-v1", PreflightExpiresAt: now.Add(time.Hour), EstimatedItems: 2,
		EstimatedBytes: 6144, State: "draft", TransitionRevision: 7, CreatedAt: now, UpdatedAt: now,
	}
}

func managedRecoveryEligibilitySecurityItems(
	now time.Time,
	plan model.BackupAssetRecoveryPlan,
) []model.BackupAssetRecoveryPlanItem {
	return []model.BackupAssetRecoveryPlanItem{
		{ID: strings.Repeat("1", 32), PlanID: plan.ID, Ordinal: 0, RecoveryPointID: plan.RecoveryPointID,
			CatalogGenerationID: plan.CatalogGenerationID, EntryID: strings.Repeat("2", 64),
			EntryType: string(backupasset.CatalogEntryFile), RelativePathDigest: strings.Repeat("3", 64), CreatedAt: now},
		{ID: strings.Repeat("4", 32), PlanID: plan.ID, Ordinal: 1, RecoveryPointID: plan.RecoveryPointID,
			CatalogGenerationID: plan.CatalogGenerationID, EntryID: strings.Repeat("5", 64),
			EntryType: string(backupasset.CatalogEntryFile), RelativePathDigest: strings.Repeat("6", 64), CreatedAt: now},
	}
}

func managedRecoveryEligibilitySecurityAsset(
	item model.BackupAssetRecoveryPlanItem,
	plan model.BackupAssetRecoveryPlan,
	entryFingerprint string,
	size int64,
) content.AuthorizedAsset {
	return content.AuthorizedAsset{
		Ref:                 backupasset.AssetRef{RecoveryPointID: item.RecoveryPointID, EntryID: item.EntryID},
		CatalogGenerationID: item.CatalogGenerationID, RepositoryID: plan.RepositoryID,
		Provider: backupasset.ProviderRsync, ProviderCapabilityRevision: 7,
		SourceFingerprint: "current-source-v1", EntryFingerprint: entryFingerprint,
		FingerprintStrength: "strong", Size: size,
	}
}

func managedRecoveryEligibilitySecurityBinding(plan model.BackupAssetRecoveryPlan) recovery.RecoveryAuthorityBinding {
	return recovery.RecoveryAuthorityBinding{
		Provider: backupasset.ProviderRsync, PlanID: plan.ID, PlanBindingDigest: plan.BindingDigest,
		PlanTransitionRevision: plan.TransitionRevision, RepositoryID: plan.RepositoryID,
		RecoveryPointID: plan.RecoveryPointID, CatalogGenerationID: plan.CatalogGenerationID,
		SelectionDigest: plan.SelectionDigest, SourceRevisionDigest: plan.SourceRevisionDigest,
		ManifestDigest: plan.ImmutableManifestDigest, SecurityDecision: recovery.SecurityDecisionKind(plan.SecurityDecision),
		SecurityDecisionDigest: plan.SecurityDecisionDigest, SecurityFindingSetDigest: plan.SecurityFindingSetDigest,
		SecurityPolicyRevision: plan.SecurityPolicyRevision,
	}
}

func managedRecoveryEligibilitySecurityDigestForTest(
	binding recovery.RecoveryAuthorityBinding,
	bundleFingerprint string,
	rows []managedRecoveryEligibilitySecurityEvidenceRow,
) string {
	canonical := backupasset.NewCanonicalSHA256()
	canonical.String("xirang/recovery/security-finding-set/plan/v1")
	canonical.String(processingSecurityPolicyRevision)
	canonical.String(bundleFingerprint)
	canonical.Uint64(uint64(len(rows)))
	for _, row := range rows {
		canonical.Uint64(uint64(row.Ordinal))
		canonical.String(row.RecoveryPointID)
		canonical.String(row.CatalogGenerationID)
		canonical.String(row.EntryID)
		canonical.String(row.SourceFingerprint)
		canonical.String(row.EntryFingerprint)
		canonical.Int64(row.ProviderCapabilityRevision)
		canonical.String(row.PipelineFingerprint)
		canonical.Int64(row.ArtifactPlaintextSize)
		canonical.String(row.ArtifactPlaintextDigest)
	}
	digest, _ := canonical.HexDigest()
	return digest
}

func seedManagedRecoveryEligibilitySecurityEvidence(
	t *testing.T,
	db *gorm.DB,
	now time.Time,
	item model.BackupAssetRecoveryPlanItem,
	asset content.AuthorizedAsset,
	pipeline string,
	bundleFingerprint string,
	state capabilityspec.ScanState,
	index int,
) ([]byte, managedRecoveryEligibilitySecurityEvidenceRow) {
	t.Helper()
	result := processingRuntimeMalwareResult(
		now.Add(time.Duration(index)*time.Second), bundleFingerprint, state,
		capabilityspec.CoverageComplete, asset.Size,
	)
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	digestBytes := sha256.Sum256(payload)
	artifactDigest := hex.EncodeToString(digestBytes[:])
	identity := string(rune('a' + index))
	jobID := strings.Repeat(identity, 32)
	attemptID := strings.Repeat(string(rune('c'+index)), 32)
	setID := strings.Repeat(string(rune('e'+index)), 32)
	artifactID := strings.Repeat(string(rune('7'+index)), 32)
	blobID := strings.Repeat(string(rune('9'+index)), 32)
	finishedAt := now
	job := model.BackupAssetProcessingJob{
		ID: jobID, WorkKey: strings.Repeat(identity, 64), DescriptorSchemaVersion: 1,
		DescriptorCanonical: []byte(`{}`), RecoveryPointID: asset.Ref.RecoveryPointID,
		CatalogGenerationID: asset.CatalogGenerationID, EntryID: asset.Ref.EntryID,
		SourceFingerprint: asset.SourceFingerprint, EntryFingerprint: asset.EntryFingerprint,
		ProviderCapabilityRevision: asset.ProviderCapabilityRevision,
		Capability:                 capabilityspec.CapabilityMalwareScan, CapabilitySchema: "malware.scan.v1",
		PipelineFingerprint: pipeline, OutputProfile: capabilityspec.ProfileSignatureScanV1,
		SecurityPolicyRevision: processingSecurityPolicyRevision, PriorityClass: string(processing.PriorityBackground),
		EffectivePriority: 900, State: string(processing.ProcessingSucceeded), TransitionRevision: int64(index + 2),
		CurrentAttemptID: &attemptID, CurrentArtifactSetID: &setID, IsCurrent: false,
		QueuedAt: now, FinishedAt: &finishedAt, AbsoluteDeadline: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	attempt := model.BackupAssetProcessingAttempt{
		ID: attemptID, JobID: jobID, AttemptNumber: 1, WorkerID: strings.Repeat("1", 32),
		SlotClass: string(processing.PriorityBackground), State: "succeeded",
		WorkerLeaseExpiresAt: now.Add(time.Minute), LastHeartbeatAt: now,
		RecoveryPointLeaseID: strings.Repeat("2", 32), RecoveryPointAttemptID: strings.Repeat("3", 32),
		RecoveryPointFenceHash: strings.Repeat("4", 64), AbsoluteDeadline: now.Add(time.Hour),
		IsCurrent: false, StartedAt: now, FinishedAt: &finishedAt, CreatedAt: now, UpdatedAt: now,
	}
	set := model.BackupAssetDerivedArtifactSet{
		ID: setID, JobID: jobID, AttemptID: attemptID, WorkKey: job.WorkKey,
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID,
		EntryID: asset.Ref.EntryID, SourceFingerprint: asset.SourceFingerprint,
		SecurityPolicyRevision: processingSecurityPolicyRevision,
		ManifestDigest:         strings.Repeat(string(rune('1'+index)), 64), State: "active",
		Completeness: string(processing.ArtifactComplete), ArtifactCount: 1,
		TotalPlaintextBytes: int64(len(payload)), CreatedAt: now, UpdatedAt: now,
	}
	blob := model.BackupAssetDerivedBlob{
		ID: blobID, PlaintextDigest: artifactDigest, PlaintextSize: int64(len(payload)), PhysicalSize: int64(len(payload)),
		CipherFormatVersion: 1, ChunkSize: 64 << 10, ChunkCount: 1, NoncePrefix: []byte{1},
		OpaqueLocator: "opaque-malware", WrappedDEK: []byte{1}, EnvelopeNonce: []byte{1},
		DerivedKEKVersion: 1, State: "active", RefCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	artifact := model.BackupAssetDerivedArtifact{
		ID: artifactID, ArtifactSetID: setID, Ordinal: 0, Role: string(processing.ArtifactRoleMetadata),
		MediaType: "application/json", PlaintextSize: int64(len(payload)), PlaintextDigest: artifactDigest,
		Completeness:      string(processing.ArtifactComplete),
		CoverageCanonical: []byte(`{"schema_version":1,"kind":"all"}`), BlobID: blobID, CreatedAt: now,
	}
	reference := model.BackupAssetDerivedBlobReference{
		ID: strings.Repeat(string(rune('5'+index)), 32), BlobID: blobID, ArtifactID: artifactID,
		RecoveryPointID: asset.Ref.RecoveryPointID, CatalogGenerationID: asset.CatalogGenerationID,
		EntryID: asset.Ref.EntryID, SourceFingerprint: asset.SourceFingerprint,
		State: "active", CreatedAt: now, UpdatedAt: now,
	}
	for _, value := range []any{&job, &attempt, &set, &blob, &artifact, &reference} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("seed security evidence %d: %v", index, err)
		}
	}
	if err := db.Model(&model.BackupAssetProcessingJob{}).Where("id = ?", job.ID).
		Update("is_current", false).Error; err != nil {
		t.Fatalf("seal security job %d: %v", index, err)
	}
	if err := db.Model(&model.BackupAssetProcessingAttempt{}).Where("id = ?", attempt.ID).
		Update("is_current", false).Error; err != nil {
		t.Fatalf("seal security attempt %d: %v", index, err)
	}
	return payload, managedRecoveryEligibilitySecurityEvidenceRow{
		PlanItemID: item.ID, Ordinal: item.Ordinal, RecoveryPointID: item.RecoveryPointID,
		CatalogGenerationID: item.CatalogGenerationID, EntryID: item.EntryID,
		PlanSourceFingerprint: item.SourceFingerprint, RelativePathDigest: item.RelativePathDigest,
		JobID: job.ID, JobTransitionRevision: job.TransitionRevision,
		SourceFingerprint: job.SourceFingerprint, EntryFingerprint: job.EntryFingerprint,
		ProviderCapabilityRevision: job.ProviderCapabilityRevision, PipelineFingerprint: pipeline,
		ArtifactSetID: set.ID, ArtifactSetManifestDigest: set.ManifestDigest,
		ArtifactID: artifact.ID, ArtifactPlaintextSize: artifact.PlaintextSize,
		ArtifactPlaintextDigest: artifact.PlaintextDigest,
	}
}

type managedRecoveryEligibilitySecurityAuthorizerFake struct {
	assets map[backupasset.AssetRef]content.AuthorizedAsset
	err    error
}

func (fake managedRecoveryEligibilitySecurityAuthorizerFake) Authorize(
	_ context.Context,
	_ content.DeliveryActor,
	ref backupasset.AssetRef,
	_ content.DeliveryAction,
) (content.AuthorizedAsset, error) {
	if fake.err != nil {
		return content.AuthorizedAsset{}, fake.err
	}
	asset, ok := fake.assets[ref]
	if !ok {
		return content.AuthorizedAsset{}, backupasset.ErrNotFound
	}
	return asset, nil
}

type managedRecoveryEligibilitySecurityReaderFake struct {
	payloads map[string][]byte
	calls    int
}

func (fake *managedRecoveryEligibilitySecurityReaderFake) ReadAuthorized(
	_ context.Context,
	authorization processing.DerivedArtifactAuthorization,
	destination io.Writer,
) error {
	fake.calls++
	payload, ok := fake.payloads[authorization.ArtifactID]
	if !ok {
		return backupasset.ErrNotFound
	}
	_, err := destination.Write(payload)
	return err
}

type managedRecoveryEligibilityRepositoryObserverFake struct {
	request     provider.RecoverySourceAuthorityRequest
	source      provider.RsyncRestoreSource
	observation repository.RecoveryRsyncSourceAuthorityObservation
	err         error
}

func (fake *managedRecoveryEligibilityRepositoryObserverFake) ObserveRecoverySource(
	_ context.Context,
	request provider.RecoverySourceAuthorityRequest,
) (provider.RsyncRestoreSource, repository.RecoveryRsyncSourceAuthorityObservation, error) {
	fake.request = request
	return fake.source, fake.observation, fake.err
}

type managedRecoveryEligibilitySourceDurableFake struct {
	calls    int
	tx       *gorm.DB
	request  provider.RecoverySourceAuthorityRequest
	expected repository.RecoveryRsyncSourceAuthorityObservation
	err      error
}

func TestManagedRecoveryEligibilityAuthoritiesProjectOneOwner(t *testing.T) {
	db := openRuntimeTestDB(t)
	ports, err := newManagedRecoveryEligibilityAuthorities(recovery.RecoveryEligibilityAuthorityDependencies{
		DB:                db,
		Source:            managedRecoveryEligibilitySourcePortFake{},
		Security:          managedRecoveryEligibilitySecurityPortFake{},
		TargetRoot:        managedRecoveryEligibilityTargetRootPortFake{},
		TargetObservation: managedRecoveryEligibilityTargetObservationPortFake{},
		Now:               func() time.Time { return time.Date(2026, 8, 15, 1, 2, 3, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("compose eligibility authorities: %v", err)
	}
	preflight, preflightOK := ports.preflight.(*recovery.RecoveryEligibilityAuthority)
	live, liveOK := ports.live.(*recovery.RecoveryEligibilityAuthority)
	reconciliation, reconciliationOK := ports.reconciliation.(*recovery.RecoveryEligibilityAuthority)
	if !preflightOK || !liveOK || !reconciliationOK || preflight != live || live != reconciliation {
		t.Fatal("production projections do not share one Recovery eligibility owner")
	}
	if managedRecoveryKnownUnavailableProductionAuthorities(managedRecoveryGraphBuildDependencies{
		PreflightEvidence:       ports.preflight,
		AuthorityRevalidator:    ports.live,
		ReconciliationRevisions: ports.reconciliation,
	}) {
		t.Fatal("composed eligibility owner was classified as a known-unavailable shell")
	}
}

type managedRecoveryEligibilitySourcePortFake struct{}

func (managedRecoveryEligibilitySourcePortFake) ObserveRecoveryEligibilitySource(
	context.Context,
	provider.RecoverySourceAuthorityRequest,
) (provider.RsyncRestoreSource, recovery.RecoveryEligibilitySourceObservation, error) {
	return nil, recovery.RecoveryEligibilitySourceObservation{}, backupasset.ErrCapabilityUnavailable
}

func (managedRecoveryEligibilitySourcePortFake) RevalidateRecoveryEligibilitySourceTx(
	context.Context,
	*gorm.DB,
	recovery.RecoveryAuthorityBinding,
	recovery.RecoveryEligibilitySourceObservation,
) error {
	return nil
}

type managedRecoveryEligibilitySecurityPortFake struct{}

func (managedRecoveryEligibilitySecurityPortFake) ObserveRecoveryEligibilitySecurity(
	context.Context,
	recovery.RecoveryAuthorityBinding,
) (recovery.RecoveryEligibilitySecurityObservation, error) {
	return recovery.RecoveryEligibilitySecurityObservation{}, backupasset.ErrCapabilityUnavailable
}

func (managedRecoveryEligibilitySecurityPortFake) RevalidateRecoveryEligibilitySecurityTx(
	context.Context,
	*gorm.DB,
	recovery.RecoveryAuthorityBinding,
	recovery.RecoveryEligibilitySecurityObservation,
) error {
	return nil
}

type managedRecoveryEligibilityTargetRootPortFake struct{}

func (managedRecoveryEligibilityTargetRootPortFake) CaptureRecoveryEligibilityTargetRootTx(
	context.Context,
	*gorm.DB,
	recovery.RecoveryAuthorityBinding,
) (recovery.RecoveryEligibilityTargetRootSnapshot, error) {
	return recovery.RecoveryEligibilityTargetRootSnapshot{}, backupasset.ErrCapabilityUnavailable
}

func (managedRecoveryEligibilityTargetRootPortFake) RevalidateRecoveryEligibilityTargetRootTx(
	context.Context,
	*gorm.DB,
	recovery.RecoveryAuthorityBinding,
	recovery.RecoveryEligibilityTargetRootSnapshot,
) error {
	return nil
}

func (managedRecoveryEligibilityTargetRootPortFake) ResolveRecoveryReconciliationRevisionsTx(
	context.Context,
	*gorm.DB,
	uint,
	string,
) (recovery.RecoveryReconciliationRevisionSnapshot, error) {
	return recovery.RecoveryReconciliationRevisionSnapshot{}, backupasset.ErrCapabilityUnavailable
}

type managedRecoveryEligibilityTargetObservationPortFake struct{}

func (managedRecoveryEligibilityTargetObservationPortFake) ObserveRecoveryEligibilityTarget(
	context.Context,
	recovery.RecoveryEligibilityTargetObservationRequest,
) (recovery.RecoveryEligibilityTargetObservation, error) {
	return recovery.RecoveryEligibilityTargetObservation{}, backupasset.ErrCapabilityUnavailable
}

func (fake *managedRecoveryEligibilitySourceDurableFake) RevalidateRecoverySourceAuthorityTx(
	_ context.Context,
	tx *gorm.DB,
	request provider.RecoverySourceAuthorityRequest,
	expected repository.RecoveryRsyncSourceAuthorityObservation,
) error {
	fake.calls++
	fake.tx = tx
	fake.request = request
	fake.expected = expected
	return fake.err
}
