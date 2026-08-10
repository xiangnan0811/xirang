package recovery

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

type recoveryOrdinaryClaimExecutor interface {
	ExecuteClaim(
		context.Context,
		RecoveryWorkerClaim,
		provider.RsyncRestoreSource,
		string,
	) error
}

func recoveryExecutionPayload(size int64) string {
	return strings.Repeat("s", int(size))
}

func recoveryExecutionPayloadDigest(size int64) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(recoveryExecutionPayload(size))))
}

func newRecoveryExecutionFixture(t *testing.T) *authorizationReceiptServiceFixture {
	t.Helper()
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)

	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var preflight model.BackupAssetRecoveryPreflight
	if err := fixture.db.Where("id = ?", fixture.request.PreflightID).Take(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	operations, err := decodeRecoveryOperationRows(preflight.EncryptedOperationRows)
	if err != nil {
		t.Fatalf("decode recovery execution operation snapshot: %v", err)
	}
	for index := range operations {
		operation := &operations[index]
		switch operation.Kind {
		case RecoveryOperationCreate, RecoveryOperationOverwrite, RecoveryOperationSkip:
			if operation.Source.AssetRef == nil {
				t.Fatalf("ordinary recovery execution operation %d has no source", index)
			}
			var entry model.CatalogEntry
			if err := fixture.db.Where(
				"generation_id = ? AND recovery_point_id = ? AND entry_id = ?",
				plan.CatalogGenerationID,
				operation.Source.AssetRef.RecoveryPointID,
				operation.Source.AssetRef.EntryID,
			).Take(&entry).Error; err != nil {
				t.Fatal(err)
			}
			if operation.Kind == RecoveryOperationSkip {
				sourceDigest := recoveryExecutionPayloadDigest(entry.Size)
				if operation.ExpectedPrior.Digest == sourceDigest {
					t.Fatalf("skip fixture source and target digests unexpectedly match: %q", sourceDigest)
				}
				operation.ExpectedPriorBytes = entry.Size + 100
				operation.ExpectedPostIdentityDigest = operation.ExpectedPrior.Digest
				operation.ExpectedPostBytes = -1
				continue
			}
			if operation.ExpectedPostBytes != entry.Size {
				t.Fatalf("mutating recovery execution operation %d bytes=%d catalog=%d",
					index, operation.ExpectedPostBytes, entry.Size)
			}
			operation.ExpectedPostIdentityDigest = recoveryExecutionPayloadDigest(entry.Size)
		default:
			t.Fatalf("unexpected recovery execution fixture operation %q", operation.Kind)
		}
	}
	products, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetMode(plan.TargetMode), ConflictPolicy: ConflictPolicy(plan.ConflictPolicy),
		Operations: operations,
		Limits: RecoveryOperationLimits{
			MaxRows: len(operations), MaxItems: len(operations),
			MaxBytes: plan.EstimatedBytes, MaxImpactRows: len(operations),
		},
	})
	if err != nil {
		t.Fatalf("rebuild recovery execution operation products: %v", err)
	}
	encodedOperations, err := encodeRecoveryOperationRows(products.Rows)
	if err != nil {
		t.Fatalf("encode recovery execution operation snapshot: %v", err)
	}
	updatedPlan := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).Where("id = ?", plan.ID).Updates(map[string]any{
		"operation_set_digest": products.OperationSetDigest,
		"delete_set_digest":    products.DeleteSetDigest,
		"estimated_items":      products.Impact.EstimatedItems,
		"estimated_bytes":      products.Impact.EstimatedBytes,
	})
	if updatedPlan.Error != nil {
		t.Fatal(updatedPlan.Error)
	}
	if updatedPlan.RowsAffected != 1 {
		t.Fatalf("updated recovery execution plans=%d, want 1", updatedPlan.RowsAffected)
	}
	preflight.OperationSetDigest = products.OperationSetDigest
	preflight.DeleteSetDigest = products.DeleteSetDigest
	preflight.EncryptedOperationRows = encodedOperations
	preflight.EstimatedItems = products.Impact.EstimatedItems
	preflight.EstimatedBytes = products.Impact.EstimatedBytes
	if err := fixture.db.Save(&preflight).Error; err != nil {
		t.Fatal(err)
	}

	write := fixture.request
	write.IdempotencyKey = "authorization-receipt-prerequisite-write-key"
	write.Proof.JTI = "FAKE_RECOVERY_PREREQUISITE_WRITE_PROOF_JTI"
	write.Reason = "FAKE_RECOVERY_PREREQUISITE_WRITE_REASON"
	writeResult, err := fixture.service.Authorize(context.Background(), write)
	if err != nil {
		t.Fatalf("prepare recovery execution write authority: %v", err)
	}
	fixture.request.Operation = AuthorizationReceiptExecute
	fixture.request.Category = AuthorizationReceiptCategoryExecute
	fixture.request.Endpoint = recoveryExecuteEndpoint
	fixture.request.ExpectedPlanRevision = writeResult.PlanTransitionRevision
	fixture.request.GrantID = writeResult.GrantID
	fixture.request.GrantSecret = write.GrantSecret
	fixture.request.Proof.JTI = "FAKE_RECOVERY_OPERATION_PROOF_JTI"
	fixture.request.IdempotencyKey = "authorization-receipt-operation-key-0001"
	fixture.request.Reason = ""
	return fixture
}

func TestRecoveryOrdinaryVerifyIssuanceUsesExactLockedTargetSessionBinding(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "ordinary-verify-issuance")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary recovery: claim=%+v found=%t err=%v", claim, found, err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	if err := coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
		t.Fatalf("execute ordinary recovery: %v", err)
	}
	if len(target.verifyPermits) == 0 || len(target.verifyPermits) != len(target.verifyObjects) ||
		len(target.verifyPermits) != len(target.verifies) {
		t.Fatalf("ordinary verify permits/objects/expectations=%d/%d/%d, want equal non-zero counts",
			len(target.verifyPermits), len(target.verifyObjects), len(target.verifies))
	}
	var job model.BackupAssetRecoveryJob
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	want, err := newRecoveryTargetSessionBinding(plan)
	if err != nil {
		t.Fatalf("derive ordinary verify session binding: %v", err)
	}
	for index := range target.verifyPermits {
		assertRecoveryTargetVerifyPermitProof(
			t, target.verifyPermits[index], target.verifyObjects[index], want,
			claim.JobID, TargetMode(job.TargetMode), fixture.now,
		)
	}
}

func TestRecoveryOrdinaryItemWriteIssuanceUsesExactLockedTargetSessionBinding(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "ordinary-item-write-issuance")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary recovery: claim=%+v found=%t err=%v", claim, found, err)
	}
	base, err := coordinator.PrepareFirstWrite(context.Background(), claim)
	if err != nil {
		t.Fatalf("prepare ordinary first write: %v", err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ? AND outcome = ''", claim.JobID).
		Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	handoff, err := coordinator.loadOrdinaryOperationHandoff(context.Background(), claim, item.ID)
	if err != nil {
		t.Fatalf("load exact ordinary handoff: %v", err)
	}
	permit, err := coordinator.ordinaryItemWritePermit(
		claim, base, handoff, handoff.job.TargetChainRevision,
	)
	if err != nil {
		t.Fatalf("issue exact ordinary item permit: %v", err)
	}
	proof := permit.itemProof
	if proof == nil || proof.sessionBinding != handoff.targetSessionBinding ||
		proof.jobID != handoff.job.ID || proof.targetMode != TargetMode(handoff.job.TargetMode) ||
		proof.jobItemID != handoff.item.ID || proof.operationDigest != handoff.operationDigest ||
		proof.object != handoff.object || proof.operation != handoff.operation.Kind ||
		proof.expectedPrior != handoff.operation.ExpectedPrior ||
		proof.expectedPriorBytes != handoff.operation.ExpectedPriorBytes ||
		proof.expectedDigest != handoff.operation.ExpectedPostIdentityDigest ||
		proof.expectedBytes != handoff.operation.ExpectedPostBytes ||
		proof.artifacts != handoff.overwriteArtifacts ||
		proof.bindingDigest != targetItemWritePermitProofDigest(permit, proof) {
		t.Fatalf("ordinary item proof=%+v, want exact locked handoff", proof)
	}
	request := TargetWriteAtomicRequest{
		Object:         handoff.object,
		ExpectedBytes:  handoff.operation.ExpectedPostBytes,
		ExpectedDigest: handoff.operation.ExpectedPostIdentityDigest,
		Content:        strings.NewReader(recoveryExecutionPayload(handoff.operation.ExpectedPostBytes)),
	}
	if _, err := permit.validateItemWriteAt(fixture.now, request); err != nil {
		t.Fatalf("validate exact ordinary item permit: %v", err)
	}

	mutations := []struct {
		name             string
		expectedRevision string
		mutate           func(*interruptedOperationHandoff)
	}{
		{name: "session binding", mutate: func(value *interruptedOperationHandoff) {
			value.targetSessionBinding.NodeRevision = "node-revision-substituted"
			value.targetSessionBinding.bindingDigest = value.targetSessionBinding.digest()
		}},
		{name: "job", mutate: func(value *interruptedOperationHandoff) {
			value.job.ID = strings.Repeat("7", 32)
		}},
		{name: "mode", mutate: func(value *interruptedOperationHandoff) {
			value.job.TargetMode = string(TargetModeInPlace)
		}},
		{name: "object", mutate: func(value *interruptedOperationHandoff) {
			value.object.PrivateRelativeLocator += ".substituted"
		}},
		{name: "operation", mutate: func(value *interruptedOperationHandoff) {
			value.operation.Kind = RecoveryOperationOverwrite
		}},
		{name: "expected post digest", mutate: func(value *interruptedOperationHandoff) {
			value.operation.ExpectedPostIdentityDigest = strings.Repeat("f", sha256DigestLength)
		}},
		{name: "expected revision", expectedRevision: "target-revision-substituted"},
	}
	for _, testCase := range mutations {
		t.Run(testCase.name, func(t *testing.T) {
			mutated := handoff
			if testCase.mutate != nil {
				testCase.mutate(&mutated)
			}
			expectedRevision := handoff.job.TargetChainRevision
			if testCase.expectedRevision != "" {
				expectedRevision = testCase.expectedRevision
			}
			if _, err := coordinator.ordinaryItemWritePermit(
				claim, base, mutated, expectedRevision,
			); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
				t.Fatalf("substituted ordinary item issuance error=%v, want ErrRecoveryWorkerFenceLost", err)
			}
		})
	}
}

type recoveryExecutionSourceFake struct {
	mu            sync.Mutex
	materialized  [][]provider.RestoreEntry
	opened        []provider.RestoreEntry
	revalidateErr error
	revalidates   int
	closes        int
}

func (fake *recoveryExecutionSourceFake) OpenDeclaredRegular(
	_ context.Context,
	entry provider.RestoreEntry,
) (provider.RsyncRestoreSourceStream, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.opened = append(fake.opened, entry)
	return io.NopCloser(strings.NewReader(recoveryExecutionPayload(entry.ExpectedSize))), nil
}

func (fake *recoveryExecutionSourceFake) MaterializeDeclaredEntries(
	_ context.Context,
	entries []provider.RestoreEntry,
) ([]provider.RestoreEntry, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.materialized = append(fake.materialized, append([]provider.RestoreEntry(nil), entries...))
	materialized := append([]provider.RestoreEntry(nil), entries...)
	for index := range materialized {
		if materialized[index].ExpectedDigest != "" {
			return nil, provider.ErrRsyncRestoreSourceDrift
		}
		materialized[index].ExpectedDigest = recoveryExecutionPayloadDigest(materialized[index].ExpectedSize)
	}
	return materialized, nil
}

func (fake *recoveryExecutionSourceFake) Revalidate(context.Context) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.revalidates++
	return fake.revalidateErr
}

func (fake *recoveryExecutionSourceFake) Close() error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.closes++
	return nil
}

// recoveryRepositoryContractSource enforces the public declaration contract
// implemented by repositoryRsyncRestoreSource.MaterializeDeclaredEntries. In
// particular, a caller must declare the complete durable plan-item set with an
// empty digest; the source returns the pinned content digest after matching the
// remaining exact binding fields.
type recoveryRepositoryContractSource struct {
	ordered        []provider.RestoreEntry
	byBinding      map[recoveryRepositoryDeclarationBinding]provider.RestoreEntry
	materialized   [][]provider.RestoreEntry
	opened         []provider.RestoreEntry
	openAttempts   int
	open           func(int) error
	streamClose    func(int) error
	materializeErr error
	materialize    func([]provider.RestoreEntry) []provider.RestoreEntry
	revalidate     func(int) error
	revalidates    int
	closes         int
	contractError  string
}

type recoverySourceStreamFake struct {
	io.Reader
	closeErr error
}

func (stream *recoverySourceStreamFake) Close() error {
	return stream.closeErr
}

type recoveryRepositoryDeclarationBinding struct {
	AssetRef           backupasset.AssetRef
	Type               backupasset.CatalogEntryType
	ExpectedSize       int64
	TargetObjectDigest string
}

func newRecoveryRepositoryContractSource(
	t *testing.T,
	db *gorm.DB,
	jobID string,
) *recoveryRepositoryContractSource {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var planItems []model.BackupAssetRecoveryPlanItem
	if err := db.Where("plan_id = ?", job.PlanID).Order("ordinal ASC, id ASC").Find(&planItems).Error; err != nil {
		t.Fatal(err)
	}
	if len(planItems) == 0 {
		t.Fatal("Repository declaration fixture has no plan items")
	}
	var jobItems []model.BackupAssetRecoveryJobItem
	if err := db.Where("job_id = ?", jobID).Order("ordinal ASC").Find(&jobItems).Error; err != nil {
		t.Fatal(err)
	}
	entryIDs := make([]string, 0, len(planItems))
	for _, planItem := range planItems {
		entryIDs = append(entryIDs, planItem.EntryID)
	}
	var catalogEntries []model.CatalogEntry
	if err := db.Where(
		"generation_id = ? AND recovery_point_id = ? AND entry_id IN ?",
		planItems[0].CatalogGenerationID, planItems[0].RecoveryPointID, entryIDs,
	).Find(&catalogEntries).Error; err != nil {
		t.Fatal(err)
	}
	catalogEntryByID := make(map[string]model.CatalogEntry, len(catalogEntries))
	for _, entry := range catalogEntries {
		if _, duplicate := catalogEntryByID[entry.EntryID]; duplicate {
			t.Fatalf("duplicate Catalog declaration for entry %q", entry.EntryID)
		}
		catalogEntryByID[entry.EntryID] = entry
	}
	if len(catalogEntryByID) != len(planItems) {
		t.Fatalf("Catalog declarations=%d want=%d", len(catalogEntryByID), len(planItems))
	}
	jobItemByPlanItem := make(map[string]model.BackupAssetRecoveryJobItem, len(jobItems))
	for _, item := range jobItems {
		if item.PlanItemID == nil {
			continue
		}
		if _, duplicate := jobItemByPlanItem[*item.PlanItemID]; duplicate {
			t.Fatalf("duplicate job-item declaration for plan item %q", *item.PlanItemID)
		}
		jobItemByPlanItem[*item.PlanItemID] = item
	}
	source := &recoveryRepositoryContractSource{
		ordered:   make([]provider.RestoreEntry, 0, len(planItems)),
		byBinding: make(map[recoveryRepositoryDeclarationBinding]provider.RestoreEntry, len(planItems)),
	}
	for _, planItem := range planItems {
		jobItem, found := jobItemByPlanItem[planItem.ID]
		if !found {
			t.Fatalf("plan item %q has no exact job-item declaration", planItem.ID)
		}
		catalogEntry, found := catalogEntryByID[planItem.EntryID]
		if !found || catalogEntry.EntryType != planItem.EntryType || catalogEntry.Size < 0 {
			t.Fatalf("plan item %q has no exact Catalog size authority", planItem.ID)
		}
		sourceDigest := jobItem.ExpectedPostIdentityDigest
		if RecoveryOperationKind(jobItem.OperationKind) == RecoveryOperationSkip {
			sourceDigest = recoveryExecutionPayloadDigest(catalogEntry.Size)
		}
		entry := provider.RestoreEntry{
			AssetRef: backupasset.AssetRef{
				RecoveryPointID: planItem.RecoveryPointID,
				EntryID:         planItem.EntryID,
			},
			Type:               backupasset.CatalogEntryType(planItem.EntryType),
			ExpectedSize:       catalogEntry.Size,
			ExpectedDigest:     sourceDigest,
			TargetObjectDigest: planItem.RelativePathDigest,
		}
		if err := entry.Validate(planItem.RecoveryPointID); err != nil {
			t.Fatalf("invalid strict Repository declaration: %v", err)
		}
		binding := recoveryRepositoryDeclarationFor(entry)
		if _, duplicate := source.byBinding[binding]; duplicate {
			t.Fatalf("duplicate strict Repository declaration: %+v", binding)
		}
		source.byBinding[binding] = entry
		source.ordered = append(source.ordered, entry)
	}
	if len(source.ordered) == 0 || len(source.ordered) != len(source.byBinding) {
		t.Fatal("strict Repository source has an incomplete declaration set")
	}
	return source
}

func recoveryRepositoryDeclarationFor(entry provider.RestoreEntry) recoveryRepositoryDeclarationBinding {
	return recoveryRepositoryDeclarationBinding{
		AssetRef: entry.AssetRef, Type: entry.Type, ExpectedSize: entry.ExpectedSize,
		TargetObjectDigest: entry.TargetObjectDigest,
	}
}

func (source *recoveryRepositoryContractSource) OpenDeclaredRegular(
	_ context.Context,
	entry provider.RestoreEntry,
) (provider.RsyncRestoreSourceStream, error) {
	source.openAttempts++
	strict, declared := source.byBinding[recoveryRepositoryDeclarationFor(entry)]
	if !declared || strict != entry {
		return nil, provider.ErrRsyncRestoreSourceDrift
	}
	if source.open != nil {
		if err := source.open(source.openAttempts); err != nil {
			return nil, err
		}
	}
	source.opened = append(source.opened, entry)
	var closeErr error
	if source.streamClose != nil {
		closeErr = source.streamClose(source.openAttempts)
	}
	return &recoverySourceStreamFake{
		Reader: strings.NewReader(recoveryExecutionPayload(entry.ExpectedSize)), closeErr: closeErr,
	}, nil
}

func (source *recoveryRepositoryContractSource) MaterializeDeclaredEntries(
	_ context.Context,
	entries []provider.RestoreEntry,
) ([]provider.RestoreEntry, error) {
	source.materialized = append(source.materialized, append([]provider.RestoreEntry(nil), entries...))
	violations := make([]string, 0)
	if len(entries) != len(source.byBinding) {
		violations = append(violations, fmt.Sprintf("declaration count=%d want=%d", len(entries), len(source.byBinding)))
	}
	seen := make(map[recoveryRepositoryDeclarationBinding]struct{}, len(entries))
	for index, entry := range entries {
		if entry.ExpectedDigest != "" {
			violations = append(violations, fmt.Sprintf("declaration %d caller digest is nonempty", index))
		}
		binding := recoveryRepositoryDeclarationFor(entry)
		if _, duplicate := seen[binding]; duplicate {
			violations = append(violations, fmt.Sprintf("declaration %d is duplicated", index))
		}
		seen[binding] = struct{}{}
		if _, declared := source.byBinding[binding]; !declared {
			violations = append(violations, fmt.Sprintf("declaration %d binding is not in the durable plan set", index))
		}
	}
	if len(seen) != len(source.byBinding) {
		violations = append(violations, "durable plan declaration is omitted")
	}
	if len(violations) != 0 {
		source.contractError = strings.Join(violations, "; ")
		return nil, fmt.Errorf("%w: %s", provider.ErrRsyncRestoreSourceDrift, source.contractError)
	}
	if source.materializeErr != nil {
		return nil, source.materializeErr
	}
	materialized := append([]provider.RestoreEntry(nil), source.ordered...)
	if source.materialize != nil {
		materialized = source.materialize(materialized)
	}
	return materialized, nil
}

func (source *recoveryRepositoryContractSource) Revalidate(context.Context) error {
	source.revalidates++
	if source.revalidate != nil {
		return source.revalidate(source.revalidates)
	}
	return nil
}

func (source *recoveryRepositoryContractSource) Close() error {
	source.closes++
	return nil
}

func TestRecoveryReviewF4WorkspaceDeadlineAndPublication(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}

	var initial model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", executed.JobID).Take(&initial).Error; err != nil {
		t.Fatal(err)
	}
	wantWorkspace := recoveryWorkspaceLocatorDirectory + "/" + executed.JobID
	if initial.State != string(JobStateQueued) || initial.TargetMode != string(TargetModeIsolated) ||
		initial.WorkspacePhase != string(WorkspacePhaseNone) ||
		initial.EncryptedWorkspaceRelativeLocator != wantWorkspace || !validDigest(initial.WorkspaceBindingDigest) ||
		initial.WorkspaceMarkerBindingDigest != "" || initial.WorkspaceOwner != "" ||
		initial.WorkspaceFence != 0 || initial.PlaintextDeadline != nil {
		t.Fatalf("execute did not commit the exact unreserved workspace identity: %+v", initial)
	}
	var storedWorkspace string
	if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).
		Select("encrypted_workspace_relative_locator").Where("id = ?", executed.JobID).
		Scan(&storedWorkspace).Error; err != nil {
		t.Fatal(err)
	}
	if !secure.IsEncrypted(storedWorkspace) || storedWorkspace == wantWorkspace ||
		strings.Contains(storedWorkspace, wantWorkspace) {
		t.Fatalf("workspace locator is not encrypted at rest: %q", storedWorkspace)
	}
	assertRecoveryF4NoPublishedResults(t, fixture.db, executed.JobID)

	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-f4-workspace-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim F4 workspace job: claim=%+v found=%t err=%v", claim, found, err)
	}
	var reservedAtTarget model.BackupAssetRecoveryJob
	var reservationObserved bool
	target := &recoveryExecutionTargetFake{
		db: fixture.db, now: func() time.Time { return fixture.now },
		beforeWorkspaceCreate: func(ctx context.Context, request CreateOwnedJobDirRequest) error {
			if err := fixture.db.WithContext(ctx).Where("id = ?", claim.JobID).Take(&reservedAtTarget).Error; err != nil {
				return err
			}
			wantDeadline := fixture.now.Add(recoveryWorkspacePlaintextTTL)
			if reservedAtTarget.WorkspacePhase != string(WorkspacePhaseReserved) ||
				reservedAtTarget.EncryptedWorkspaceRelativeLocator != initial.EncryptedWorkspaceRelativeLocator ||
				reservedAtTarget.WorkspaceBindingDigest != initial.WorkspaceBindingDigest ||
				!validDigest(reservedAtTarget.WorkspaceMarkerBindingDigest) ||
				reservedAtTarget.WorkspaceOwner != claim.WorkerID ||
				reservedAtTarget.WorkspaceFence != claim.AttemptFence ||
				reservedAtTarget.PlaintextDeadline == nil ||
				!reservedAtTarget.PlaintextDeadline.Equal(wantDeadline) {
				return fmt.Errorf("owned workspace target call preceded durable reservation: %+v", reservedAtTarget)
			}
			if request.Object.PrivateRelativeLocator != wantWorkspace ||
				request.MarkerBindingDigest != reservedAtTarget.WorkspaceMarkerBindingDigest {
				return fmt.Errorf("owned workspace target request does not reuse the durable identity: %+v", request)
			}
			var checkpointCount, latchCount int64
			if err := fixture.db.WithContext(ctx).Model(&model.BackupAssetRecoveryCheckpoint{}).
				Where("job_id = ? AND sequence = ? AND phase = ?", claim.JobID, 0, CheckpointPhaseWorkspaceReserved).
				Count(&checkpointCount).Error; err != nil {
				return err
			}
			if err := fixture.db.WithContext(ctx).Model(&model.BackupAssetRecoveryEvidence{}).
				Where("id = ? AND kind = ?", recoverySchemaUseLatchRowID, RecoverySchemaUseLatchID).
				Count(&latchCount).Error; err != nil {
				return err
			}
			if checkpointCount != 1 || latchCount != 1 {
				return fmt.Errorf("owned workspace target call preceded checkpoint/latch commit: checkpoints=%d latch=%d", checkpointCount, latchCount)
			}
			reservationObserved = true
			return nil
		},
	}
	coordinator.target = target
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	if err := coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
		t.Fatalf("execute F4 isolated recovery: %v", err)
	}
	if !reservationObserved || len(target.workspaceCalls) != 1 || len(target.writes) == 0 {
		t.Fatalf("F4 target ordering observed=%t workspace_calls=%d writes=%d",
			reservationObserved, len(target.workspaceCalls), len(target.writes))
	}

	var terminal model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&terminal).Error; err != nil {
		t.Fatal(err)
	}
	if terminal.State != string(JobStateSucceeded) || terminal.FailureCategory != "" ||
		terminal.WorkspacePhase != string(WorkspacePhaseSealed) ||
		terminal.EncryptedWorkspaceRelativeLocator != initial.EncryptedWorkspaceRelativeLocator ||
		terminal.WorkspaceBindingDigest != initial.WorkspaceBindingDigest ||
		terminal.WorkspaceMarkerBindingDigest != reservedAtTarget.WorkspaceMarkerBindingDigest ||
		terminal.WorkspaceOwner != reservedAtTarget.WorkspaceOwner ||
		terminal.WorkspaceFence != reservedAtTarget.WorkspaceFence ||
		terminal.PlaintextDeadline == nil || reservedAtTarget.PlaintextDeadline == nil ||
		!terminal.PlaintextDeadline.Equal(*reservedAtTarget.PlaintextDeadline) {
		t.Fatalf("successful Task 6 workspace was not sealed and left unpublished: %+v", terminal)
	}
	assertRecoveryF4NoPublishedResults(t, fixture.db, claim.JobID)

	t.Run("unexpected remote directory fails closed without reallocation", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-f4-workspace-conflict")
		if err != nil || !found {
			t.Fatalf("claim workspace-conflict job: found=%t err=%v", found, err)
		}
		target := &recoveryExecutionTargetFake{
			db: fixture.db, now: func() time.Time { return fixture.now },
			workspaceCreateErr: errors.New("unexpected remote recovery directory"),
		}
		coordinator.target = target
		if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoveryWorkerUnavailable) {
			t.Fatalf("unexpected remote directory error=%v, want ErrRecoveryWorkerUnavailable", err)
		}
		reserved := loadRecoveryF4Job(t, fixture.db, claim.JobID)
		if reserved.WorkspacePhase != string(WorkspacePhaseReserved) ||
			reserved.EncryptedWorkspaceRelativeLocator != recoveryWorkspaceLocatorDirectory+"/"+claim.JobID ||
			!validDigest(reserved.WorkspaceBindingDigest) || !validDigest(reserved.WorkspaceMarkerBindingDigest) ||
			reserved.PlaintextDeadline == nil || len(target.workspaceCalls) != 1 {
			t.Fatalf("failed workspace creation lost its durable reservation: job=%+v calls=%d", reserved, len(target.workspaceCalls))
		}

		target.workspaceCreateErr = nil
		if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
			t.Fatalf("retry exact workspace reservation: %v", err)
		}
		retried := loadRecoveryF4Job(t, fixture.db, claim.JobID)
		if retried.EncryptedWorkspaceRelativeLocator != reserved.EncryptedWorkspaceRelativeLocator ||
			retried.WorkspaceBindingDigest != reserved.WorkspaceBindingDigest ||
			retried.WorkspaceMarkerBindingDigest != reserved.WorkspaceMarkerBindingDigest ||
			retried.PlaintextDeadline == nil || reserved.PlaintextDeadline == nil ||
			!retried.PlaintextDeadline.Equal(*reserved.PlaintextDeadline) || len(target.workspaceCalls) != 2 ||
			target.workspaceCalls[0] != target.workspaceCalls[1] {
			t.Fatalf("workspace retry reallocated durable identity: before=%+v after=%+v calls=%+v",
				reserved, retried, target.workspaceCalls)
		}
		var checkpointCount int64
		if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
			Where("job_id = ? AND phase = ?", claim.JobID, CheckpointPhaseWorkspaceReserved).
			Count(&checkpointCount).Error; err != nil {
			t.Fatal(err)
		}
		if checkpointCount != 1 {
			t.Fatalf("workspace retry duplicated reservation checkpoints=%d", checkpointCount)
		}
		assertRecoveryF4NoPublishedResults(t, fixture.db, executed.JobID)
	})
}

func TestRecoveryReviewF4PartialWorkspaceCleanupOnly(t *testing.T) {
	t.Run("failed before reservation remains unreserved", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-f4-pre-arm-failure")
		if err != nil || !found {
			t.Fatalf("claim pre-arm failure job: found=%t err=%v", found, err)
		}
		coordinator.liveRevalidator = &authorizationReceiptLiveRevalidatorSpy{err: ErrAuthorizationDenied}
		if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoverySourceChanged) {
			t.Fatalf("pre-arm drift error=%v, want ErrRecoverySourceChanged", err)
		}
		job := loadRecoveryF4Job(t, fixture.db, claim.JobID)
		if job.State != string(JobStateFailed) || job.WorkspacePhase != string(WorkspacePhaseNone) ||
			job.WorkspaceMarkerBindingDigest != "" || job.WorkspaceOwner != "" ||
			job.WorkspaceFence != 0 || job.PlaintextDeadline != nil {
			t.Fatalf("zero-mutation failure created partial workspace state: %+v", job)
		}
		assertRecoveryF4NoPublishedResults(t, fixture.db, executed.JobID)
	})

	t.Run("queued cancellation remains unreserved", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		if err := coordinator.CancelJob(context.Background(), executed.JobID); err != nil {
			t.Fatalf("cancel queued recovery job: %v", err)
		}
		job := loadRecoveryF4Job(t, fixture.db, executed.JobID)
		if job.State != string(JobStateCanceled) || job.WorkspacePhase != string(WorkspacePhaseNone) ||
			job.WorkspaceMarkerBindingDigest != "" || job.WorkspaceOwner != "" ||
			job.WorkspaceFence != 0 || job.PlaintextDeadline != nil {
			t.Fatalf("queued cancellation created partial workspace state: %+v", job)
		}
		assertRecoveryF4NoPublishedResults(t, fixture.db, executed.JobID)
	})

	t.Run("armed cancellation becomes cleanup only", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-f4-armed-cancel")
		if err != nil || !found {
			t.Fatalf("claim armed-cancel job: found=%t err=%v", found, err)
		}
		if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
			t.Fatalf("reserve workspace before cancellation: %v", err)
		}
		reserved := loadRecoveryF4Job(t, fixture.db, claim.JobID)
		if err := coordinator.CancelJob(context.Background(), claim.JobID); err != nil {
			t.Fatalf("cancel armed recovery job: %v", err)
		}
		job := loadRecoveryF4Job(t, fixture.db, claim.JobID)
		if job.State != string(JobStateNeedsAttention) ||
			job.FailureCategory != recoveryCancellationAfterMutationArmFailureCategory ||
			job.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
			job.EncryptedWorkspaceRelativeLocator != reserved.EncryptedWorkspaceRelativeLocator ||
			job.WorkspaceMarkerBindingDigest != reserved.WorkspaceMarkerBindingDigest ||
			job.PlaintextDeadline == nil || reserved.PlaintextDeadline == nil ||
			!job.PlaintextDeadline.Equal(*reserved.PlaintextDeadline) {
			t.Fatalf("armed cancellation did not preserve a cleanup-only workspace: %+v", job)
		}
		assertRecoveryF4NoPublishedResults(t, fixture.db, executed.JobID)
	})

	t.Run("unresolved remote outcome becomes cleanup only", func(t *testing.T) {
		fixture := newRecoveryExecutionFixture(t)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		target := &recoveryExecutionTargetFake{
			db: fixture.db, now: func() time.Time { return fixture.now },
			verifyObservation: func(call int, observation TargetVerifyObservation) TargetVerifyObservation {
				if call == 1 && observation.Present != nil {
					observation.Present.IdentityDigest = strings.Repeat("9", 64)
				}
				return observation
			},
		}
		coordinator.target = target
		claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-f4-unresolved")
		if err != nil || !found {
			t.Fatalf("claim unresolved-outcome job: found=%t err=%v", found, err)
		}
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		if err := coordinator.ExecuteClaim(context.Background(), claim, source, ""); !errors.Is(err, ErrInvalidTargetVerification) {
			t.Fatalf("unresolved remote outcome error=%v, want ErrInvalidTargetVerification", err)
		}
		job := loadRecoveryF4Job(t, fixture.db, claim.JobID)
		if job.State != string(JobStateNeedsAttention) ||
			job.FailureCategory != recoveryRemoteOutcomeUnresolvedFailureCategory ||
			job.WorkspacePhase != string(WorkspacePhaseCleanupDue) || job.PlaintextDeadline == nil {
			t.Fatalf("unresolved remote outcome did not preserve a cleanup-only workspace: %+v", job)
		}
		assertRecoveryF4NoPublishedResults(t, fixture.db, executed.JobID)
	})
}

func loadRecoveryF4Job(t *testing.T, db *gorm.DB, jobID string) model.BackupAssetRecoveryJob {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

func assertRecoveryF4NoPublishedResults(t *testing.T, db *gorm.DB, jobID string) {
	t.Helper()
	var resultSets, results int64
	if err := db.Model(&model.BackupAssetRecoveryResultSet{}).Where("job_id = ?", jobID).Count(&resultSets).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryResult{}).Where("job_id = ?", jobID).Count(&results).Error; err != nil {
		t.Fatal(err)
	}
	if resultSets != 0 || results != 0 {
		t.Fatalf("unpublished F4 workspace exposed result rows: result_sets=%d results=%d", resultSets, results)
	}
}

func TestRecoveryExecuteClaimUsesRepositoryCompatibleRsyncDeclarations(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "repository-declaration-contract-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	if err := coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
		t.Fatalf("execute with Repository-compatible source: %v (contract=%s)", err, source.contractError)
	}
	if source.contractError != "" || len(source.materialized) != 1 ||
		len(source.materialized[0]) != len(source.ordered) {
		t.Fatalf("Repository declaration calls=%d entries=%d want=%d contract=%q",
			len(source.materialized), len(source.materialized[0]), len(source.ordered), source.contractError)
	}
	for index, declaration := range source.materialized[0] {
		if declaration.ExpectedDigest != "" {
			t.Fatalf("caller declaration %d digest=%q, want empty", index, declaration.ExpectedDigest)
		}
		if _, declared := source.byBinding[recoveryRepositoryDeclarationFor(declaration)]; !declared {
			t.Fatalf("caller declaration %d is outside the complete durable set: %+v", index, declaration)
		}
	}
	if len(source.opened) != 2 || source.revalidates != 7 || source.closes != 1 {
		t.Fatalf("Repository source streams=%d revalidates=%d closes=%d, want 2/7/1",
			len(source.opened), source.revalidates, source.closes)
	}
}

func primeRecoveryOrdinaryCompletedItem(
	t *testing.T,
	coordinator *WorkerCoordinator,
	claim RecoveryWorkerClaim,
	jobItemID string,
) {
	t.Helper()
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("prepare completed-item history: %v", err)
	}
	handoff, err := coordinator.loadOrdinaryOperationHandoff(context.Background(), claim, jobItemID)
	if err != nil {
		t.Fatalf("load completed-item handoff: %v", err)
	}
	observation := TargetVerifyObservation{
		Kind: handoff.expectation.Kind, ObservedRevision: "target-revision-primed-completed-item",
	}
	if handoff.expectation.Present != nil {
		observation.Present = &PresentObservation{
			IdentityDigest: handoff.expectation.Present.IdentityDigest,
			Bytes:          handoff.expectation.Present.Bytes,
		}
	}
	if handoff.expectation.Absent != nil {
		observation.Absent = &AbsentObservation{Evidence: TargetAbsenceEvidenceExact}
	}
	if _, err := coordinator.projectOrdinaryOperation(
		context.Background(), claim, handoff, handoff.job.TargetChainRevision,
		ordinaryOperationResult{observation: observation, observationReturned: true},
		SourceRevalidationMatched,
	); err != nil {
		t.Fatalf("project completed-item history: %v", err)
	}
}

func TestRecoveryExecuteClaimDeclaresCompletedItemWithExactCatalogSize(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	var completed model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ? AND ordinal = ?", executed.JobID, 0).Take(&completed).Error; err != nil {
		t.Fatal(err)
	}
	if completed.PlanItemID == nil || RecoveryOperationKind(completed.OperationKind) != RecoveryOperationCreate {
		t.Fatalf("completed declaration fixture item=%+v", completed)
	}
	var planItem model.BackupAssetRecoveryPlanItem
	if err := fixture.db.Where("id = ?", *completed.PlanItemID).Take(&planItem).Error; err != nil {
		t.Fatal(err)
	}
	exactSourceSize := completed.EstimatedBytes + 100
	if err := fixture.db.Model(&model.CatalogEntry{}).Where(
		"generation_id = ? AND recovery_point_id = ? AND entry_id = ?",
		planItem.CatalogGenerationID, planItem.RecoveryPointID, planItem.EntryID,
	).Update("size", exactSourceSize).Error; err != nil {
		t.Fatal(err)
	}

	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "completed-declaration-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	primeRecoveryOrdinaryCompletedItem(t, coordinator, claim, completed.ID)
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	if err := coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
		t.Fatalf("execute with completed-item declaration: %v (contract=%s)", err, source.contractError)
	}
	if len(source.materialized) != 1 {
		t.Fatalf("completed-item materialization calls=%d, want 1", len(source.materialized))
	}
	declaredCompleted := false
	for _, declaration := range source.materialized[0] {
		if declaration.AssetRef.EntryID != planItem.EntryID {
			continue
		}
		declaredCompleted = true
		if declaration.ExpectedSize != exactSourceSize || declaration.ExpectedSize == completed.EstimatedBytes {
			t.Fatalf("completed-item declaration size=%d estimate=%d exact=%d",
				declaration.ExpectedSize, completed.EstimatedBytes, exactSourceSize)
		}
	}
	if !declaredCompleted {
		t.Fatal("completed item was omitted from the full Repository declaration set")
	}
	if len(source.opened) != 1 || len(target.writes) != 1 {
		t.Fatalf("completed-item execution source streams=%d writes=%d, want 1/1", len(source.opened), len(target.writes))
	}
}

func TestRecoveryExecuteClaimRejectsCompletedMaterializedDigestMismatchBeforeFirstWrite(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	var completed model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ? AND ordinal = ?", executed.JobID, 0).Take(&completed).Error; err != nil {
		t.Fatal(err)
	}
	if completed.PlanItemID == nil || RecoveryOperationKind(completed.OperationKind) != RecoveryOperationCreate {
		t.Fatalf("completed digest fixture item=%+v", completed)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryJobItem{}).Where("id = ?", completed.ID).Updates(map[string]any{
		"outcome":         "succeeded",
		"bytes_written":   completed.ExpectedPostBytes,
		"verified_size":   completed.ExpectedPostBytes,
		"verified_digest": completed.ExpectedPostIdentityDigest,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var completedPlanItem model.BackupAssetRecoveryPlanItem
	if err := fixture.db.Where("id = ?", *completed.PlanItemID).Take(&completedPlanItem).Error; err != nil {
		t.Fatal(err)
	}

	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "completed-materialized-digest-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim completed digest execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	source.materialize = func(entries []provider.RestoreEntry) []provider.RestoreEntry {
		for index := range entries {
			if entries[index].AssetRef.EntryID == completedPlanItem.EntryID {
				entries[index].ExpectedDigest = strings.Repeat("f", 64)
				return entries
			}
		}
		t.Fatalf("completed materialized declaration %q not found", completedPlanItem.EntryID)
		return nil
	}
	executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
	if !errors.Is(executeErr, ErrRecoverySourceChanged) {
		t.Errorf("completed materialized digest mismatch error=%v, want ErrRecoverySourceChanged", executeErr)
	}
	if errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
		t.Errorf("completed materialized digest mismatch was misclassified as fence loss: %v", executeErr)
	}
	assertRecoveryRejectedBeforeFirstWrite(t, fixture.db, before, target, source)
}

func testRecoveryB1E1FreshCreateOverwriteSource(t *testing.T) {
	for _, kind := range []RecoveryOperationKind{RecoveryOperationCreate, RecoveryOperationOverwrite} {
		t.Run(string(kind), func(t *testing.T) {
			fixture := newRecoveryExecutionFixture(t)
			executed, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			var item model.BackupAssetRecoveryJobItem
			if err := fixture.db.Where(
				"job_id = ? AND operation_kind = ?", executed.JobID, kind,
			).Take(&item).Error; err != nil {
				t.Fatal(err)
			}
			if item.PlanItemID == nil {
				t.Fatalf("%s source fixture item=%+v", kind, item)
			}
			var planItem model.BackupAssetRecoveryPlanItem
			if err := fixture.db.Where("id = ?", *item.PlanItemID).Take(&planItem).Error; err != nil {
				t.Fatal(err)
			}

			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
			coordinator.target = target
			claim, found, err := coordinator.ClaimNext(context.Background(), "b1-e1-fresh-source-"+string(kind))
			if err != nil || !found || claim.JobID != executed.JobID {
				t.Fatalf("claim %s execution: claim=%+v found=%t err=%v", kind, claim, found, err)
			}
			before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
			source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
			source.materialize = func(entries []provider.RestoreEntry) []provider.RestoreEntry {
				for index := range entries {
					if entries[index].AssetRef.EntryID == planItem.EntryID {
						entries[index].ExpectedDigest = strings.Repeat("f", sha256DigestLength)
						return entries
					}
				}
				t.Fatalf("%s materialized declaration %q not found", kind, planItem.EntryID)
				return nil
			}
			executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
			if !errors.Is(executeErr, ErrRecoverySourceChanged) || errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
				t.Fatalf("fresh %s source mismatch error=%v, want ErrRecoverySourceChanged", kind, executeErr)
			}
			assertRecoveryRejectedBeforeFirstWrite(t, fixture.db, before, target, source)
			if source.openAttempts != 0 {
				t.Fatalf("fresh %s source mismatch reached stream open %d times", kind, source.openAttempts)
			}
		})
	}
}

func testRecoveryB1E1SkipSourceTargetSeparation(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	assertRecoveryB1E1PersistedOrdinaryOperationProducts(t, fixture.db, executed.JobID)
	var skipped model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where(
		"job_id = ? AND operation_kind = ?", executed.JobID, RecoveryOperationSkip,
	).Take(&skipped).Error; err != nil {
		t.Fatal(err)
	}
	if skipped.PlanItemID == nil {
		t.Fatalf("skip separation fixture item=%+v", skipped)
	}
	var skippedPlanItem model.BackupAssetRecoveryPlanItem
	if err := fixture.db.Where("id = ?", *skipped.PlanItemID).Take(&skippedPlanItem).Error; err != nil {
		t.Fatal(err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, executed.JobID)
	var sourceEntry provider.RestoreEntry
	foundSourceEntry := false
	for _, entry := range source.ordered {
		if entry.AssetRef.EntryID == skippedPlanItem.EntryID {
			sourceEntry = entry
			foundSourceEntry = true
			break
		}
	}
	if !foundSourceEntry || sourceEntry.ExpectedSize == skipped.ExpectedPriorBytes ||
		sourceEntry.ExpectedDigest == skipped.ExpectedPriorDigest ||
		sourceEntry.ExpectedDigest == skipped.ExpectedPostIdentityDigest {
		t.Fatalf("skip source=%+v target prior=%q/%d post=%q/%d",
			sourceEntry, skipped.ExpectedPriorDigest, skipped.ExpectedPriorBytes,
			skipped.ExpectedPostIdentityDigest, skipped.ExpectedPostBytes)
	}

	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "skip-source-target-separation-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim skip separation execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	if err := coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
		t.Fatalf("execute separated skip source/target product: %v", err)
	}
	for _, opened := range source.opened {
		if opened.AssetRef == sourceEntry.AssetRef {
			t.Fatalf("skip source was opened for mutation: %+v", opened)
		}
	}
	if len(source.opened) != 2 || source.openAttempts != 2 || source.revalidates != 7 ||
		len(target.writes) != 2 || len(target.verifies) != 3 || skipped.Ordinal >= len(target.verifies) {
		t.Fatalf("skip separation streams=%d attempts=%d revalidates=%d writes=%d verifies=%d ordinal=%d",
			len(source.opened), source.openAttempts, source.revalidates, len(target.writes), len(target.verifies), skipped.Ordinal)
	}
	expectation := target.verifies[skipped.Ordinal]
	if expectation.Kind != TargetPresencePresent || expectation.Present == nil || expectation.Absent != nil ||
		expectation.Present.IdentityDigest != skipped.ExpectedPriorDigest ||
		expectation.Present.Bytes != skipped.ExpectedPriorBytes {
		t.Fatalf("skip target expectation=%+v, want frozen prior %q/%d",
			expectation, skipped.ExpectedPriorDigest, skipped.ExpectedPriorBytes)
	}
	var after model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("id = ?", skipped.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Outcome != "skipped" || after.BytesWritten != 0 ||
		after.VerifiedDigest != skipped.ExpectedPriorDigest || after.VerifiedSize != skipped.ExpectedPriorBytes {
		t.Fatalf("skip terminal projection=%+v", after)
	}
}

func assertRecoveryB1E1PersistedOrdinaryOperationProducts(t *testing.T, db *gorm.DB, jobID string) {
	t.Helper()
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var preflight model.BackupAssetRecoveryPreflight
	if err := db.Where("id = ?", job.PreflightID).Take(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	operations, err := decodeRecoveryOperationRows(preflight.EncryptedOperationRows)
	if err != nil {
		t.Fatalf("decode persisted ordinary operations: %v", err)
	}
	var items []model.BackupAssetRecoveryJobItem
	if err := db.Where("job_id = ?", jobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != len(operations) || len(items) != 3 {
		t.Fatalf("persisted ordinary operations/items=%d/%d, want 3/3", len(operations), len(items))
	}
	for index := range operations {
		operation := operations[index]
		item := items[index]
		if item.OperationKind != string(operation.Kind) || item.ExpectedPriorKind != string(operation.ExpectedPrior.Kind) ||
			item.ExpectedPriorDigest != operation.ExpectedPrior.Digest ||
			item.ExpectedPostIdentityDigest != operation.ExpectedPostIdentityDigest ||
			item.ExpectedPostBytes != operation.ExpectedPostBytes || item.ExpectedPriorBytes != operation.ExpectedPriorBytes {
			t.Fatalf("persisted ordinary product %d operation=%+v item=%+v", index, operation, item)
		}
		switch operation.Kind {
		case RecoveryOperationCreate:
			if item.ExpectedPriorKind != string(ExpectedTargetAbsent) || item.ExpectedPriorDigest != "" ||
				item.ExpectedPriorBytes != -1 || !validDigest(item.ExpectedPostIdentityDigest) || item.ExpectedPostBytes < 0 {
				t.Fatalf("persisted create product=%+v", item)
			}
		case RecoveryOperationOverwrite:
			if item.ExpectedPriorKind != string(ExpectedTargetPresent) || !validDigest(item.ExpectedPriorDigest) ||
				item.ExpectedPriorBytes < 0 || !validDigest(item.ExpectedPostIdentityDigest) || item.ExpectedPostBytes < 0 {
				t.Fatalf("persisted overwrite product=%+v", item)
			}
		case RecoveryOperationSkip:
			if item.ExpectedPriorKind != string(ExpectedTargetPresent) || !validDigest(item.ExpectedPriorDigest) ||
				item.ExpectedPostIdentityDigest != item.ExpectedPriorDigest || item.ExpectedPostBytes != -1 ||
				item.ExpectedPriorBytes < 0 {
				t.Fatalf("persisted skip target product=%+v", item)
			}
		default:
			t.Fatalf("unexpected ordinary operation %q", operation.Kind)
		}
	}
}

func TestRecoveryExecuteClaimRejectsMaterializedSourceDigestMismatchAsSourceChanged(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "materialized-digest-mismatch-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	var before model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	source.materialize = func(entries []provider.RestoreEntry) []provider.RestoreEntry {
		entries[0].ExpectedDigest = strings.Repeat("f", 64)
		return entries
	}
	executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
	if !errors.Is(executeErr, ErrRecoverySourceChanged) {
		t.Fatalf("materialized digest mismatch error=%v, want ErrRecoverySourceChanged", executeErr)
	}
	if errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("materialized digest mismatch was misclassified as fence loss: %v", executeErr)
	}
	assertRecoveryRevalidationProjection(t, fixture.db, before, 0, 0, false, executeErr)
	if len(source.opened) != 0 || len(target.writes) != 0 || len(target.verifies) != 0 {
		t.Fatalf("materialized digest mismatch source_open=%d writes=%d verifies=%d, want zero operation I/O",
			len(source.opened), len(target.writes), len(target.verifies))
	}
}

func TestRecoveryExecuteClaimRejectsInvalidMaterializedSourceResponsesAsSourceChanged(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]provider.RestoreEntry) []provider.RestoreEntry
	}{
		{
			name: "duplicate",
			mutate: func(entries []provider.RestoreEntry) []provider.RestoreEntry {
				entries[1] = entries[0]
				return entries
			},
		},
		{
			name: "omitted",
			mutate: func(entries []provider.RestoreEntry) []provider.RestoreEntry {
				return entries[:len(entries)-1]
			},
		},
		{
			name: "substituted",
			mutate: func(entries []provider.RestoreEntry) []provider.RestoreEntry {
				entries[0].AssetRef.EntryID = strings.Repeat("9", 64)
				return entries
			},
		},
		{
			name: "wrong digest",
			mutate: func(entries []provider.RestoreEntry) []provider.RestoreEntry {
				entries[0].ExpectedDigest = strings.Repeat("f", 64)
				return entries
			},
		},
		{
			name: "wrong size",
			mutate: func(entries []provider.RestoreEntry) []provider.RestoreEntry {
				entries[0].ExpectedSize++
				return entries
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryExecutionFixture(t)
			executed, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
			coordinator.target = target
			claim, found, err := coordinator.ClaimNext(context.Background(), "invalid-materialized-source-worker")
			if err != nil || !found || claim.JobID != executed.JobID {
				t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
			}
			before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
			source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
			source.materialize = test.mutate
			executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
			if !errors.Is(executeErr, ErrRecoverySourceChanged) {
				t.Fatalf("invalid materialized source error=%v, want ErrRecoverySourceChanged", executeErr)
			}
			if errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
				t.Fatalf("invalid materialized source was misclassified as fence loss: %v", executeErr)
			}
			assertRecoveryRejectedBeforeFirstWrite(t, fixture.db, before, target, source)
		})
	}
}

func TestRecoveryExecuteClaimRejectsPostSnapshotCatalogSizeDriftBeforeExecution(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "post-snapshot-catalog-drift-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	source.materialize = func(entries []provider.RestoreEntry) []provider.RestoreEntry {
		entry := entries[0]
		updated := fixture.db.Model(&model.CatalogEntry{}).Where(
			"generation_id = ? AND recovery_point_id = ? AND entry_id = ?",
			plan.CatalogGenerationID, entry.AssetRef.RecoveryPointID, entry.AssetRef.EntryID,
		).Update("size", entry.ExpectedSize+1)
		if updated.Error != nil || updated.RowsAffected != 1 {
			t.Fatalf("introduce post-snapshot Catalog size drift: rows=%d err=%v", updated.RowsAffected, updated.Error)
		}
		return entries
	}
	executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
	if !errors.Is(executeErr, ErrRecoverySourceChanged) {
		t.Fatalf("post-snapshot Catalog size drift error=%v, want ErrRecoverySourceChanged", executeErr)
	}
	if errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("post-snapshot Catalog size drift was misclassified as fence loss: %v", executeErr)
	}
	assertRecoverySupersededBeforeFirstWrite(t, fixture.db, claim, before, target, source)
}

func TestRecoveryExecuteClaimSupersedesStablePreArmCatalogDrift(t *testing.T) {
	tests := []struct {
		name        string
		kind        RecoveryOperationKind
		frozenBytes func(model.BackupAssetRecoveryJobItem) int64
	}{
		{
			name: "create", kind: RecoveryOperationCreate,
			frozenBytes: func(item model.BackupAssetRecoveryJobItem) int64 { return item.ExpectedPostBytes },
		},
		{
			name: "overwrite", kind: RecoveryOperationOverwrite,
			frozenBytes: func(item model.BackupAssetRecoveryJobItem) int64 { return item.ExpectedPostBytes },
		},
		{
			name: "skip", kind: RecoveryOperationSkip,
			frozenBytes: func(item model.BackupAssetRecoveryJobItem) int64 { return item.ExpectedPriorBytes },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryExecutionFixture(t)
			executed, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			var item model.BackupAssetRecoveryJobItem
			if err := fixture.db.Where(
				"job_id = ? AND operation_kind = ?", executed.JobID, test.kind,
			).Take(&item).Error; err != nil {
				t.Fatal(err)
			}
			if item.PlanItemID == nil || item.Outcome != "" || item.FailureCategory != "" {
				t.Fatalf("stable size mismatch fixture item=%+v", item)
			}
			frozenBytes := test.frozenBytes(item)
			if frozenBytes < 0 {
				t.Fatalf("stable size mismatch frozen bytes=%d for item=%+v", frozenBytes, item)
			}
			var planItem model.BackupAssetRecoveryPlanItem
			if err := fixture.db.Where("id = ?", *item.PlanItemID).Take(&planItem).Error; err != nil {
				t.Fatal(err)
			}
			updated := fixture.db.Model(&model.CatalogEntry{}).Where(
				"generation_id = ? AND recovery_point_id = ? AND entry_id = ?",
				planItem.CatalogGenerationID, planItem.RecoveryPointID, planItem.EntryID,
			).Update("size", frozenBytes+100)
			if updated.Error != nil || updated.RowsAffected != 1 {
				t.Fatalf("establish stable Catalog size mismatch: rows=%d err=%v", updated.RowsAffected, updated.Error)
			}

			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
			coordinator.target = target
			claim, found, err := coordinator.ClaimNext(context.Background(), "stable-size-mismatch-"+test.name+"-worker")
			if err != nil || !found || claim.JobID != executed.JobID {
				t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
			}
			before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
			source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
			executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
			if !errors.Is(executeErr, ErrRecoverySourceChanged) {
				t.Errorf("stable pending %s Catalog size mismatch error=%v, want ErrRecoverySourceChanged", test.kind, executeErr)
			}
			if errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
				t.Errorf("stable pending %s Catalog size mismatch was misclassified as fence loss: %v", test.kind, executeErr)
			}
			assertRecoverySupersededBeforeFirstWrite(t, fixture.db, claim, before, target, source)
		})
	}
}

func TestRecoveryExecuteClaimSupersedesProviderDriftBeforeFirstWrite(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "provider-pre-arm-drift-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	before := captureRecoveryBeforeFirstWriteProjection(t, fixture.db, claim)
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	source.revalidate = func(call int) error {
		if call == 1 {
			return provider.ErrRsyncRestoreSourceDrift
		}
		return nil
	}

	executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
	if !errors.Is(executeErr, ErrRecoverySourceChanged) {
		t.Fatalf("Provider pre-arm drift error=%v, want ErrRecoverySourceChanged", executeErr)
	}
	if errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("Provider pre-arm drift was misclassified as fence loss: %v", executeErr)
	}
	assertRecoverySupersededBeforeFirstWrite(t, fixture.db, claim, before, target, source)
}

type recoveryBeforeFirstWriteProjection struct {
	job           model.BackupAssetRecoveryJob
	attempt       model.BackupAssetRecoveryAttempt
	checkpoints   int64
	terminalItems int64
	latchRows     int64
}

func captureRecoveryBeforeFirstWriteProjection(
	t *testing.T,
	db *gorm.DB,
	claim RecoveryWorkerClaim,
) recoveryBeforeFirstWriteProjection {
	t.Helper()
	projection := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, db, claim.JobID, claim.AttemptID)
	if projection.job.WorkspacePhase != string(WorkspacePhaseNone) || projection.attempt.MutationArmed ||
		projection.latchRows != 0 {
		t.Fatalf("fixture crossed first-write boundary before execution: job=%+v attempt=%+v latch=%d",
			projection.job, projection.attempt, projection.latchRows)
	}
	return projection
}

func assertRecoveryRejectedBeforeFirstWrite(
	t *testing.T,
	db *gorm.DB,
	before recoveryBeforeFirstWriteProjection,
	target *recoveryExecutionTargetFake,
	source *recoveryRepositoryContractSource,
) {
	t.Helper()
	after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, db, before.job.ID, before.attempt.ID)
	workspaceChanged := after.job.WorkspacePhase != before.job.WorkspacePhase ||
		after.job.WorkspaceBindingDigest != before.job.WorkspaceBindingDigest ||
		after.job.WorkspaceMarkerBindingDigest != before.job.WorkspaceMarkerBindingDigest ||
		after.job.WorkspaceOwner != before.job.WorkspaceOwner || after.job.WorkspaceFence != before.job.WorkspaceFence ||
		(after.job.PlaintextDeadline == nil) != (before.job.PlaintextDeadline == nil)
	if after.job.State != before.job.State || after.job.TransitionRevision != before.job.TransitionRevision ||
		after.job.TargetChainRevision != before.job.TargetChainRevision || workspaceChanged ||
		after.attempt.State != before.attempt.State || after.attempt.MutationArmed != before.attempt.MutationArmed ||
		after.checkpoints != before.checkpoints || after.terminalItems != before.terminalItems ||
		after.latchRows != before.latchRows || len(target.workspaceCalls) != 0 || len(target.writes) != 0 ||
		len(target.deletes) != 0 || len(target.verifies) != 0 || len(source.opened) != 0 {
		t.Fatalf("pre-first-write rejection changed durable or I/O effects: state=%q/%q revision=%d/%d "+
			"chain_changed=%t workspace_changed=%t attempt=%q/%q armed=%t/%t checkpoints=%d/%d "+
			"terminal_items=%d/%d latch=%d/%d workspace_calls=%d writes=%d deletes=%d verifies=%d source_open=%d",
			after.job.State, before.job.State, after.job.TransitionRevision, before.job.TransitionRevision,
			after.job.TargetChainRevision != before.job.TargetChainRevision, workspaceChanged,
			after.attempt.State, before.attempt.State, after.attempt.MutationArmed, before.attempt.MutationArmed,
			after.checkpoints, before.checkpoints, after.terminalItems, before.terminalItems,
			after.latchRows, before.latchRows, len(target.workspaceCalls), len(target.writes), len(target.deletes),
			len(target.verifies), len(source.opened))
	}
}

func assertRecoverySupersededBeforeFirstWrite(
	t *testing.T,
	db *gorm.DB,
	claim RecoveryWorkerClaim,
	before recoveryBeforeFirstWriteProjection,
	target *recoveryExecutionTargetFake,
	source *recoveryRepositoryContractSource,
) {
	t.Helper()
	after := captureRecoveryBeforeFirstWriteProjectionUnchecked(t, db, before.job.ID, before.attempt.ID)
	var plan model.BackupAssetRecoveryPlan
	if err := db.Where("id = ?", before.job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var sourceLease model.RecoveryPointLease
	if err := db.Where("id = ?", claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatal(err)
	}
	workspaceChanged := after.job.WorkspacePhase != before.job.WorkspacePhase ||
		after.job.WorkspaceBindingDigest != before.job.WorkspaceBindingDigest ||
		after.job.WorkspaceMarkerBindingDigest != before.job.WorkspaceMarkerBindingDigest ||
		after.job.WorkspaceOwner != before.job.WorkspaceOwner || after.job.WorkspaceFence != before.job.WorkspaceFence ||
		(after.job.PlaintextDeadline == nil) != (before.job.PlaintextDeadline == nil)
	if PlanState(plan.State) != PlanStateSuperseded || after.job.State != string(JobStateFailed) ||
		after.job.FailureCategory != recoveryPreWriteDriftFailureCategory ||
		after.job.TargetChainRevision != before.job.TargetChainRevision || workspaceChanged ||
		after.attempt.State != string(AttemptStateSuperseded) || after.attempt.MutationArmed ||
		after.attempt.ClosedAt == nil || after.checkpoints != before.checkpoints ||
		after.terminalItems != before.terminalItems || after.latchRows != before.latchRows ||
		sourceLease.Status != string(backupasset.LeaseReleased) || sourceLease.ReleasedAt == nil ||
		nodeLease.State != "released" || nodeLease.ReleasedAt == nil ||
		len(target.workspaceCalls) != 0 || len(target.writes) != 0 || len(target.deletes) != 0 ||
		len(target.verifies) != 0 || len(source.opened) != 0 || source.closes != 1 {
		t.Fatalf("pre-first-write drift effects plan=%q job=%q/%q chain_changed=%t workspace_changed=%t "+
			"attempt=%q armed=%t closed=%t checkpoints=%d/%d terminal_items=%d/%d latch=%d/%d "+
			"source_lease=%q node_lease=%q workspace_calls=%d writes=%d deletes=%d verifies=%d source_open=%d closes=%d",
			plan.State, after.job.State, after.job.FailureCategory,
			after.job.TargetChainRevision != before.job.TargetChainRevision, workspaceChanged,
			after.attempt.State, after.attempt.MutationArmed, after.attempt.ClosedAt != nil,
			after.checkpoints, before.checkpoints, after.terminalItems, before.terminalItems,
			after.latchRows, before.latchRows, sourceLease.Status, nodeLease.State,
			len(target.workspaceCalls), len(target.writes), len(target.deletes), len(target.verifies),
			len(source.opened), source.closes)
	}
}

func captureRecoveryBeforeFirstWriteProjectionUnchecked(
	t *testing.T,
	db *gorm.DB,
	jobID string,
	attemptID string,
) recoveryBeforeFirstWriteProjection {
	t.Helper()
	projection := recoveryBeforeFirstWriteProjection{}
	if err := db.Where("id = ?", jobID).Take(&projection.job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", attemptID).Take(&projection.attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND sequence >= ?", jobID, 1).Count(&projection.checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND outcome IN ?", jobID, []string{"succeeded", "skipped"}).
		Count(&projection.terminalItems).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ? AND kind = ?", recoverySchemaUseLatchRowID, RecoverySchemaUseLatchID).
		Count(&projection.latchRows).Error; err != nil {
		t.Fatal(err)
	}
	return projection
}

func TestRecoveryExecuteClaimRevalidatesPinnedSourcePerOperation(t *testing.T) {
	t.Run("drift between operations stops before the later write", func(t *testing.T) {
		fixture := newRecoveryExecutionFixture(t)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
		coordinator.target = target
		claim, found, err := coordinator.ClaimNext(context.Background(), "between-operation-source-drift-worker")
		if err != nil || !found || claim.JobID != executed.JobID {
			t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
		}
		var before model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
			t.Fatal(err)
		}
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		drifted := false
		source.revalidate = func(call int) error {
			if drifted {
				return provider.ErrRsyncRestoreSourceDrift
			}
			if call == 3 {
				drifted = true
			}
			return nil
		}
		executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
		if !errors.Is(executeErr, ErrRecoverySourceChanged) {
			t.Fatalf("between-operation source drift error=%v, want ErrRecoverySourceChanged", executeErr)
		}
		if source.revalidates != 4 || len(target.writes) != 1 || len(target.verifies) != 1 || len(source.opened) != 1 {
			t.Fatalf("between-operation boundary revalidates=%d writes=%d verifies=%d streams=%d, want 4/1/1/1",
				source.revalidates, len(target.writes), len(target.verifies), len(source.opened))
		}
		assertRecoverySourceRevalidationTerminalProjection(
			t, fixture.db, claim, before, executeErr,
		)
	})

	t.Run("drift after operation retains success checkpoint and chain projection", func(t *testing.T) {
		fixture := newRecoveryExecutionFixture(t)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
		coordinator.target = target
		claim, found, err := coordinator.ClaimNext(context.Background(), "post-operation-source-drift-worker")
		if err != nil || !found || claim.JobID != executed.JobID {
			t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
		}
		var before model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
			t.Fatal(err)
		}
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		drifted := false
		source.revalidate = func(int) error {
			if drifted {
				return provider.ErrRsyncRestoreSourceDrift
			}
			return nil
		}
		target.afterVerify = func(call int) {
			if call == 1 {
				drifted = true
			}
		}
		executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
		if !errors.Is(executeErr, ErrRecoverySourceChanged) {
			t.Fatalf("post-operation source drift error=%v, want ErrRecoverySourceChanged", executeErr)
		}
		if source.revalidates != 3 || len(target.writes) != 1 || len(target.verifies) != 1 || len(source.opened) != 1 {
			t.Fatalf("post-operation boundary revalidates=%d writes=%d verifies=%d streams=%d, want 3/1/1/1",
				source.revalidates, len(target.writes), len(target.verifies), len(source.opened))
		}
		assertRecoverySourceRevalidationTerminalProjection(
			t, fixture.db, claim, before, executeErr,
		)
	})
}

func TestRecoveryAdoptsLaterIsolatedOperationAfterPriorCheckpoint(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "later-isolated-adoption-before")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}

	const checkpointCallback = "test:later-isolated-operation-projection-crash"
	projectionErr := errors.New("simulated crash before later isolated operation checkpoint commit")
	if err := fixture.db.Callback().Create().Before("gorm:create").Register(checkpointCallback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
			len(target.writes) == 2 {
			_ = tx.AddError(projectionErr)
		}
	}); err != nil {
		t.Fatalf("register later isolated projection crash: %v", err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
	if err := fixture.db.Callback().Create().Remove(checkpointCallback); err != nil {
		t.Fatalf("remove later isolated projection crash: %v", err)
	}
	if !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("later isolated projection crash error=%v, want ErrRecoveryWorkerUnavailable", executeErr)
	}

	var items []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	var beforeCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("sequence ASC").Find(&beforeCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	var beforeJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&beforeJob).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) < 3 || len(beforeCheckpoints) != 2 || len(target.writes) != 2 ||
		beforeCheckpoints[0].Phase != string(CheckpointPhaseWorkspaceReserved) ||
		beforeCheckpoints[1].Phase != string(CheckpointPhaseOperation) ||
		beforeCheckpoints[1].JobItemID != items[0].ID || items[0].Outcome != "succeeded" ||
		items[1].Outcome != "" || beforeJob.WorkspacePhase != string(WorkspacePhaseWriting) {
		t.Fatalf("later isolated crash fixture job=%+v items=%+v checkpoints=%+v writes=%d",
			beforeJob, items, beforeCheckpoints, len(target.writes))
	}

	providerSource := &recoveryExecutionSourceFake{}
	resolver := &recoveryAdoptionSourceResolverFake{source: providerSource}
	restarted := newRecoveryWorkerCoordinatorWithSourceResolver(t, fixture, resolver)
	restarted.target = target
	fixture.now = claim.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := restarted.TakeoverExpired(context.Background(), "later-isolated-adoption-after")
	if err != nil || !found || takeover.JobID != claim.JobID {
		t.Fatalf("take over later isolated operation: claim=%+v found=%t err=%v", takeover, found, err)
	}
	blindReplaySource := newRecoveryRepositoryContractSource(t, fixture.db, takeover.JobID)
	if err := restarted.ExecuteClaim(context.Background(), takeover, blindReplaySource, ""); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("pre-adoption continuation error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	if len(target.writes) != 2 {
		t.Fatalf("pre-adoption continuation replayed target writes=%d, want 2", len(target.writes))
	}

	checkpoint, err := restarted.AdoptInterruptedOperation(context.Background(), takeover, items[1].ID)
	if err != nil {
		t.Fatalf("adopt later isolated operation after prior checkpoint: %v", err)
	}
	if checkpoint.Sequence != 2 || checkpoint.Phase != string(CheckpointPhaseOperation) ||
		checkpoint.JobItemID != items[1].ID || checkpoint.PriorTargetRevision != beforeJob.TargetChainRevision ||
		checkpoint.NextTargetRevision == checkpoint.PriorTargetRevision {
		t.Fatalf("later isolated adopted checkpoint=%+v", checkpoint)
	}
	var afterItems []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&afterItems).Error; err != nil {
		t.Fatal(err)
	}
	var afterCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("sequence ASC").Find(&afterCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	var afterJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&afterJob).Error; err != nil {
		t.Fatal(err)
	}
	var afterAttempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&afterAttempt).Error; err != nil {
		t.Fatal(err)
	}
	var afterSource model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", takeover.SourceFence.LeaseID).Take(&afterSource).Error; err != nil {
		t.Fatal(err)
	}
	var afterNode model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", takeover.NodeLeaseID).Take(&afterNode).Error; err != nil {
		t.Fatal(err)
	}
	if len(afterCheckpoints) != 3 || afterCheckpoints[1] != beforeCheckpoints[1] ||
		afterItems[0].Outcome != "succeeded" || afterItems[1].Outcome != "succeeded" ||
		afterItems[2].Outcome != "" || afterJob.WorkspaceOwner != beforeJob.WorkspaceOwner ||
		afterJob.WorkspaceFence != beforeJob.WorkspaceFence ||
		afterJob.WorkspaceMarkerBindingDigest != beforeJob.WorkspaceMarkerBindingDigest ||
		afterAttempt.State != string(AttemptStateRunning) || afterAttempt.ClosedAt != nil ||
		afterSource.Status != string(backupasset.LeaseActive) || afterNode.State != "active" ||
		len(resolver.refs) != 1 || providerSource.revalidates != 1 ||
		providerSource.closes != 1 || len(target.writes) != 2 {
		t.Fatalf("later isolated adoption job=%+v attempt=%+v source=%+v node=%+v items=%+v checkpoints=%+v refs=%d source_calls=%d/%d writes=%d",
			afterJob, afterAttempt, afterSource, afterNode, afterItems, afterCheckpoints,
			len(resolver.refs), providerSource.revalidates, providerSource.closes, len(target.writes))
	}

	continuationSource := newRecoveryRepositoryContractSource(t, fixture.db, takeover.JobID)
	if err := restarted.ExecuteClaim(context.Background(), takeover, continuationSource, ""); err != nil {
		t.Fatalf("continue after later isolated adoption: %v", err)
	}
	var finalItems []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&finalItems).Error; err != nil {
		t.Fatal(err)
	}
	var finalCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("sequence ASC").Find(&finalCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	var finalJob model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&finalJob).Error; err != nil {
		t.Fatal(err)
	}
	var finalAttempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&finalAttempt).Error; err != nil {
		t.Fatal(err)
	}
	var finalSource model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", takeover.SourceFence.LeaseID).Take(&finalSource).Error; err != nil {
		t.Fatal(err)
	}
	var finalNode model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", takeover.NodeLeaseID).Take(&finalNode).Error; err != nil {
		t.Fatal(err)
	}
	if len(finalItems) != 3 || finalItems[0].Outcome != "succeeded" || finalItems[1].Outcome != "succeeded" ||
		finalItems[2].Outcome != "skipped" || len(finalCheckpoints) != 4 ||
		finalCheckpoints[1] != afterCheckpoints[1] || finalCheckpoints[2] != afterCheckpoints[2] ||
		finalCheckpoints[3].JobItemID != finalItems[2].ID ||
		finalJob.State != string(JobStateSucceeded) || finalJob.WorkspacePhase != string(WorkspacePhaseSealed) ||
		finalJob.WorkspaceOwner != beforeJob.WorkspaceOwner || finalJob.WorkspaceFence != beforeJob.WorkspaceFence ||
		finalJob.WorkspaceMarkerBindingDigest != beforeJob.WorkspaceMarkerBindingDigest ||
		finalAttempt.State != string(AttemptStateCompleted) || finalAttempt.ClosedAt == nil ||
		finalSource.Status != string(backupasset.LeaseReleased) || finalNode.State != "released" || len(target.writes) != 2 {
		t.Fatalf("later isolated continuation job=%+v attempt=%+v source=%+v node=%+v items=%+v checkpoints=%+v writes=%d",
			finalJob, finalAttempt, finalSource, finalNode, finalItems, finalCheckpoints, len(target.writes))
	}
}

func TestRecoveryAdoptsLaterInPlaceOperationAfterPriorCheckpointAndTakeover(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionFixture(t)
	coordinator := fixture.coordinator
	target := fixture.target
	claim := fixture.claim

	const checkpointCallback = "test:later-in-place-operation-projection-crash"
	projectionErr := errors.New("simulated crash before later in-place operation checkpoint commit")
	if err := fixture.serviceFixture.db.Callback().Create().Before("gorm:create").Register(checkpointCallback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
			len(target.writes) == 2 {
			_ = tx.AddError(projectionErr)
		}
	}); err != nil {
		t.Fatalf("register later in-place projection crash: %v", err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, claim.JobID)
	executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
	if err := fixture.serviceFixture.db.Callback().Create().Remove(checkpointCallback); err != nil {
		t.Fatalf("remove later in-place projection crash: %v", err)
	}
	if !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("later in-place projection crash error=%v, want ErrRecoveryWorkerUnavailable", executeErr)
	}

	var items []model.BackupAssetRecoveryJobItem
	if err := fixture.serviceFixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	var beforeCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.serviceFixture.db.Where("job_id = ?", claim.JobID).
		Order("sequence ASC").Find(&beforeCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	var beforeJob model.BackupAssetRecoveryJob
	if err := fixture.serviceFixture.db.Where("id = ?", claim.JobID).Take(&beforeJob).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) < 4 || len(beforeCheckpoints) != 1 || len(target.writes) != 2 ||
		target.finalizeOverwriteCalls != 0 ||
		beforeCheckpoints[0].Phase != string(CheckpointPhaseOperation) ||
		beforeCheckpoints[0].JobItemID != items[0].ID || items[0].Outcome != "succeeded" ||
		items[1].Outcome != "" || beforeJob.WorkspacePhase != string(WorkspacePhaseNone) {
		t.Fatalf("later in-place crash fixture job=%+v items=%+v checkpoints=%+v writes=%d",
			beforeJob, items, beforeCheckpoints, len(target.writes))
	}

	providerSource := &recoveryExecutionSourceFake{}
	resolver := &recoveryAdoptionSourceResolverFake{source: providerSource}
	restarted := newRecoveryWorkerCoordinatorWithSourceResolver(t, fixture.serviceFixture, resolver)
	restarted.target = target
	fixture.serviceFixture.now = claim.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := restarted.TakeoverExpired(context.Background(), "later-in-place-adoption-after")
	if err != nil || !found || takeover.JobID != claim.JobID || takeover.AttemptID == claim.AttemptID {
		t.Fatalf("take over later in-place operation: claim=%+v found=%t err=%v", takeover, found, err)
	}
	blindReplaySource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, takeover.JobID)
	if err := restarted.ExecuteClaim(context.Background(), takeover, blindReplaySource, ""); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("pre-adoption in-place continuation error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	if len(target.writes) != 2 || target.finalizeOverwriteCalls != 0 {
		t.Fatalf("pre-adoption in-place continuation writes/finalizes=%d/%d, want 2/0",
			len(target.writes), target.finalizeOverwriteCalls)
	}

	checkpoint, err := restarted.AdoptInterruptedOperation(context.Background(), takeover, items[1].ID)
	if err != nil {
		t.Fatalf("adopt later in-place operation after prior checkpoint: %v", err)
	}
	if checkpoint.Sequence != 1 || checkpoint.Phase != string(CheckpointPhaseOperation) ||
		checkpoint.JobItemID != items[1].ID || checkpoint.AttemptID != takeover.AttemptID ||
		checkpoint.PriorTargetRevision != beforeJob.TargetChainRevision ||
		checkpoint.NextTargetRevision == checkpoint.PriorTargetRevision {
		t.Fatalf("later in-place adopted checkpoint=%+v", checkpoint)
	}

	continuationSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, takeover.JobID)
	if err := restarted.ExecuteClaim(context.Background(), takeover, continuationSource, ""); err != nil {
		t.Fatalf("continue in-place execution after adoption: %v", err)
	}
	var finalItems []model.BackupAssetRecoveryJobItem
	if err := fixture.serviceFixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&finalItems).Error; err != nil {
		t.Fatal(err)
	}
	var finalCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.serviceFixture.db.Where("job_id = ?", claim.JobID).
		Order("sequence ASC").Find(&finalCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	var finalJob model.BackupAssetRecoveryJob
	if err := fixture.serviceFixture.db.Where("id = ?", claim.JobID).Take(&finalJob).Error; err != nil {
		t.Fatal(err)
	}
	var finalAttempt model.BackupAssetRecoveryAttempt
	if err := fixture.serviceFixture.db.Where("id = ?", takeover.AttemptID).Take(&finalAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if len(finalItems) != 4 || finalItems[0].Outcome != "succeeded" || finalItems[1].Outcome != "succeeded" ||
		finalItems[2].Outcome != "skipped" || finalItems[3].Outcome != "" || len(finalCheckpoints) != 4 ||
		finalCheckpoints[0] != beforeCheckpoints[0] || finalCheckpoints[1] != checkpoint ||
		finalCheckpoints[2].JobItemID != finalItems[2].ID ||
		finalCheckpoints[3].Phase != string(CheckpointPhaseDeleteAuthorityRequired) ||
		finalJob.State != string(JobStateRunning) || finalJob.WorkspacePhase != string(WorkspacePhaseNone) ||
		finalAttempt.State != string(AttemptStateRunning) || finalAttempt.ClosedAt != nil || len(target.writes) != 2 ||
		target.finalizeOverwriteCalls != 1 ||
		len(resolver.refs) != 1 || providerSource.revalidates != 1 || providerSource.closes != 1 {
		t.Fatalf("later in-place continuation job=%+v attempt=%+v items=%+v checkpoints=%+v refs=%d source_calls=%d/%d writes=%d",
			finalJob, finalAttempt, finalItems, finalCheckpoints, len(resolver.refs),
			providerSource.revalidates, providerSource.closes, len(target.writes))
	}
}

func TestRecoveryInPlaceTakeoverRequiresAdoptionBeforeFirstOperationReplay(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionFixture(t)
	if _, err := fixture.coordinator.PrepareFirstWrite(context.Background(), fixture.claim); err != nil {
		t.Fatalf("arm initial in-place execution: %v", err)
	}
	if len(fixture.target.writes) != 0 {
		t.Fatalf("arming initial in-place execution wrote target %d time(s)", len(fixture.target.writes))
	}

	fixture.serviceFixture.now = fixture.claim.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := fixture.coordinator.TakeoverExpired(
		context.Background(), "first-in-place-adoption-after",
	)
	if err != nil || !found || takeover.JobID != fixture.claim.JobID || takeover.AttemptID == fixture.claim.AttemptID {
		t.Fatalf("take over first in-place operation: claim=%+v found=%t err=%v", takeover, found, err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, takeover.JobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), takeover, source, ""); !errors.Is(err, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("pre-adoption first in-place replay error=%v, want ErrRecoveryWorkerFenceLost", err)
	}
	if len(fixture.target.writes) != 0 || len(fixture.target.deletes) != 0 {
		t.Fatalf("pre-adoption first in-place replay mutated target writes/deletes=%d/%d, want 0/0",
			len(fixture.target.writes), len(fixture.target.deletes))
	}
}

func TestRecoveryAdoptedContinuationSourceDriftTerminalizesCurrentAttempt(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "adopted-continuation-source-before")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	var items []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) < 3 {
		t.Fatalf("adopted continuation fixture items=%d, want at least three", len(items))
	}
	primeRecoveryOrdinaryCompletedItem(t, coordinator, claim, items[0].ID)

	fixture.now = claim.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(context.Background(), "adopted-continuation-source-after")
	if err != nil || !found || takeover.JobID != claim.JobID {
		t.Fatalf("take over adopted continuation: claim=%+v found=%t err=%v", takeover, found, err)
	}
	if _, err := coordinator.AdoptInterruptedOperation(context.Background(), takeover, items[1].ID); err != nil {
		t.Fatalf("adopt current-attempt continuation checkpoint: %v", err)
	}

	var before model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	var beforeCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.db.Where("job_id = ?", takeover.JobID).Order("sequence ASC").Find(&beforeCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(beforeCheckpoints) != 3 || beforeCheckpoints[2].AttemptID != takeover.AttemptID ||
		beforeCheckpoints[2].NextTargetRevision != before.TargetChainRevision {
		t.Fatalf("adopted continuation history job=%+v checkpoints=%+v", before, beforeCheckpoints)
	}
	target, ok := coordinator.target.(*recoveryRestartTargetFake)
	if !ok {
		t.Fatalf("adopted continuation target=%T, want recoveryRestartTargetFake", coordinator.target)
	}
	targetCallsBefore := target.calls

	source := newRecoveryRepositoryContractSource(t, fixture.db, takeover.JobID)
	source.revalidate = func(int) error { return provider.ErrRsyncRestoreSourceDrift }
	executeErr := coordinator.ExecuteClaim(context.Background(), takeover, source, "")
	if !errors.Is(executeErr, ErrRecoverySourceChanged) {
		t.Fatalf("adopted continuation source drift error=%v, want ErrRecoverySourceChanged", executeErr)
	}
	if target.calls != targetCallsBefore || len(source.materialized) != 0 || len(source.opened) != 0 {
		t.Fatalf("adopted continuation source drift reached target/source I/O calls=%d/%d materialized=%d opened=%d",
			targetCallsBefore, target.calls, len(source.materialized), len(source.opened))
	}

	var after model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	var afterCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.db.Where("job_id = ?", takeover.JobID).Order("sequence ASC").Find(&afterCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterCheckpoints, beforeCheckpoints) ||
		after.State != string(JobStateNeedsAttention) || after.FailureCategory != "source_revalidation_failed" ||
		after.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
		after.TargetChainRevision != before.TargetChainRevision ||
		after.WorkspaceOwner != before.WorkspaceOwner || after.WorkspaceFence != before.WorkspaceFence {
		t.Fatalf("adopted continuation source drift job before=%+v after=%+v checkpoints=%+v/%+v",
			before, after, beforeCheckpoints, afterCheckpoints)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := fixture.db.Where("id = ?", takeover.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	var sourceLease model.RecoveryPointLease
	if err := fixture.db.Where("id = ?", takeover.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := fixture.db.Where("id = ?", takeover.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateFailed) || attempt.ClosedAt == nil ||
		sourceLease.Status != string(backupasset.LeaseReleased) || sourceLease.ReleasedAt == nil ||
		nodeLease.State != "released" || nodeLease.ReleasedAt == nil {
		t.Fatalf("adopted continuation source drift retained authority attempt/source/node=%+v/%+v/%+v",
			attempt, sourceLease, nodeLease)
	}
	var evidence []model.BackupAssetRecoveryEvidence
	if err := fixture.db.Where("job_id = ? AND kind = ?", takeover.JobID, "failure").Find(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].CheckpointID == nil ||
		*evidence[0].CheckpointID != beforeCheckpoints[2].ID || evidence[0].Outcome != "needs_attention" {
		t.Fatalf("adopted continuation source drift evidence=%+v", evidence)
	}
}

type recoveryAdoptedContinuationState struct {
	fixture     *authorizationReceiptServiceFixture
	coordinator *WorkerCoordinator
	claim       RecoveryWorkerClaim
	job         model.BackupAssetRecoveryJob
	items       []model.BackupAssetRecoveryJobItem
	checkpoints []model.BackupAssetRecoveryCheckpoint
	targetCalls int
}

func newRecoveryAdoptedContinuationState(t *testing.T) recoveryAdoptedContinuationState {
	t.Helper()
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute adopted continuation fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "adopted-continuation-failure-before")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim adopted continuation fixture: claim=%+v found=%t err=%v", claim, found, err)
	}
	var items []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) < 3 {
		t.Fatalf("adopted continuation fixture items=%d, want at least three", len(items))
	}
	primeRecoveryOrdinaryCompletedItem(t, coordinator, claim, items[0].ID)

	fixture.now = claim.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := coordinator.TakeoverExpired(
		context.Background(), "adopted-continuation-failure-after",
	)
	if err != nil || !found || takeover.JobID != claim.JobID {
		t.Fatalf("take over adopted continuation fixture: claim=%+v found=%t err=%v", takeover, found, err)
	}
	if _, err := coordinator.AdoptInterruptedOperation(context.Background(), takeover, items[1].ID); err != nil {
		t.Fatalf("adopt continuation fixture operation: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", takeover.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.db.Where("job_id = ?", takeover.JobID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 3 || checkpoints[2].AttemptID != takeover.AttemptID ||
		checkpoints[2].NextTargetRevision != job.TargetChainRevision {
		t.Fatalf("adopted continuation fixture job=%+v checkpoints=%+v", job, checkpoints)
	}
	if err := fixture.db.Where("job_id = ?", takeover.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	target, ok := coordinator.target.(*recoveryRestartTargetFake)
	if !ok {
		t.Fatalf("adopted continuation target=%T, want recoveryRestartTargetFake", coordinator.target)
	}
	return recoveryAdoptedContinuationState{
		fixture: fixture, coordinator: coordinator, claim: takeover, job: job,
		items: items, checkpoints: checkpoints, targetCalls: target.calls,
	}
}

func assertRecoveryAdoptedContinuationSourceFailure(
	t *testing.T,
	state recoveryAdoptedContinuationState,
	executeErr error,
) {
	t.Helper()
	if !errors.Is(executeErr, ErrRecoverySourceChanged) {
		t.Fatalf("adopted continuation source failure error=%v, want ErrRecoverySourceChanged", executeErr)
	}
	var after model.BackupAssetRecoveryJob
	if err := state.fixture.db.Where("id = ?", state.claim.JobID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := state.fixture.db.Where("job_id = ?", state.claim.JobID).
		Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	var items []model.BackupAssetRecoveryJobItem
	if err := state.fixture.db.Where("job_id = ?", state.claim.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(checkpoints, state.checkpoints) ||
		!reflect.DeepEqual(items, state.items) ||
		after.State != string(JobStateNeedsAttention) || after.FailureCategory != "source_revalidation_failed" ||
		after.TransitionRevision != state.job.TransitionRevision+1 ||
		after.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
		after.TargetChainRevision != state.job.TargetChainRevision ||
		after.WorkspaceOwner != state.job.WorkspaceOwner || after.WorkspaceFence != state.job.WorkspaceFence {
		t.Fatalf("adopted continuation source failure job before=%+v after=%+v items=%+v/%+v checkpoints=%+v/%+v",
			state.job, after, state.items, items, state.checkpoints, checkpoints)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := state.fixture.db.Where("id = ?", state.claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	var sourceLease model.RecoveryPointLease
	if err := state.fixture.db.Where("id = ?", state.claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := state.fixture.db.Where("id = ?", state.claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateFailed) || attempt.ClosedAt == nil ||
		sourceLease.Status != string(backupasset.LeaseReleased) || sourceLease.ReleasedAt == nil ||
		nodeLease.State != "released" || nodeLease.ReleasedAt == nil {
		t.Fatalf("adopted continuation source failure retained authority attempt/source/node=%+v/%+v/%+v",
			attempt, sourceLease, nodeLease)
	}
	var evidence []model.BackupAssetRecoveryEvidence
	if err := state.fixture.db.Where("job_id = ? AND kind = ?", state.claim.JobID, "failure").Find(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	last := state.checkpoints[len(state.checkpoints)-1]
	if len(evidence) != 1 || evidence[0].CheckpointID == nil ||
		*evidence[0].CheckpointID != last.ID || evidence[0].Outcome != "needs_attention" ||
		evidence[0].AttemptID == nil || *evidence[0].AttemptID != state.claim.AttemptID ||
		evidence[0].SourceLeaseID == nil || *evidence[0].SourceLeaseID != state.claim.SourceFence.LeaseID ||
		evidence[0].NodeLeaseID == nil || *evidence[0].NodeLeaseID != state.claim.NodeLeaseID ||
		evidence[0].NodeLeaseFence != state.claim.NodeFence || evidence[0].VerifiedAt == nil {
		t.Fatalf("adopted continuation source failure evidence=%+v", evidence)
	}
}

func TestRecoveryAdoptedContinuationMaterializationFailureTerminalizesCurrentAttempt(t *testing.T) {
	state := newRecoveryAdoptedContinuationState(t)
	source := newRecoveryRepositoryContractSource(t, state.fixture.db, state.claim.JobID)
	source.materializeErr = provider.ErrRsyncRestoreSourceDrift
	executeErr := state.coordinator.ExecuteClaim(context.Background(), state.claim, source, "")
	if source.revalidates != 1 || len(source.materialized) != 1 || len(source.opened) != 0 {
		t.Fatalf("materialization source calls revalidate/materialize/open=%d/%d/%d, want 1/1/0",
			source.revalidates, len(source.materialized), len(source.opened))
	}
	target := state.coordinator.target.(*recoveryRestartTargetFake)
	if target.calls != state.targetCalls {
		t.Fatalf("materialization source failure reached target calls=%d/%d", state.targetCalls, target.calls)
	}
	assertRecoveryAdoptedContinuationSourceFailure(t, state, executeErr)
}

func TestRecoveryAdoptedContinuationPreOperationSourceDriftTerminalizesCurrentAttempt(t *testing.T) {
	state := newRecoveryAdoptedContinuationState(t)
	source := newRecoveryRepositoryContractSource(t, state.fixture.db, state.claim.JobID)
	source.revalidate = func(call int) error {
		if call == 2 {
			return provider.ErrRsyncRestoreSourceDrift
		}
		return nil
	}
	executeErr := state.coordinator.ExecuteClaim(context.Background(), state.claim, source, "")
	if source.revalidates != 2 || len(source.materialized) != 1 || len(source.opened) != 0 {
		t.Fatalf("pre-operation source calls revalidate/materialize/open=%d/%d/%d, want 2/1/0",
			source.revalidates, len(source.materialized), len(source.opened))
	}
	target := state.coordinator.target.(*recoveryRestartTargetFake)
	if target.calls != state.targetCalls {
		t.Fatalf("pre-operation source drift reached target calls=%d/%d", state.targetCalls, target.calls)
	}
	assertRecoveryAdoptedContinuationSourceFailure(t, state, executeErr)
}

func TestRecoveryExecuteClaimOpenSourceFailureAfterCompletedOperationTerminalizesAttempt(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "open-source-failure-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	var before model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	source.open = func(call int) error {
		if call == 2 {
			return provider.ErrRsyncRestoreSourceDrift
		}
		return nil
	}
	executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
	if source.revalidates != 5 || len(source.materialized) != 1 ||
		source.openAttempts != 2 || len(source.opened) != 1 || len(target.writes) != 1 || len(target.verifies) != 1 {
		t.Fatalf("open source failure calls revalidate/materialize/open_attempt/opened/write/verify=%d/%d/%d/%d/%d/%d, want 5/1/2/1/1/1",
			source.revalidates, len(source.materialized), source.openAttempts, len(source.opened),
			len(target.writes), len(target.verifies))
	}
	assertRecoverySourceRevalidationTerminalProjection(t, fixture.db, claim, before, executeErr)
}

func TestRecoveryExecuteClaimStreamCloseSourceFailureRetainsVerifiedOperation(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "stream-close-source-failure-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	var before model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	source.streamClose = func(call int) error {
		if call == 1 {
			return provider.ErrRsyncRestoreSourceDrift
		}
		return nil
	}
	executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
	if source.revalidates != 3 || len(source.materialized) != 1 ||
		source.openAttempts != 1 || len(source.opened) != 1 || len(target.writes) != 1 || len(target.verifies) != 1 {
		t.Fatalf("stream-close source failure calls revalidate/materialize/open/write/verify=%d/%d/%d/%d/%d, want 3/1/1/1/1",
			source.revalidates, len(source.materialized), source.openAttempts,
			len(target.writes), len(target.verifies))
	}
	assertRecoverySourceRevalidationTerminalProjection(t, fixture.db, claim, before, executeErr)
}

func TestRecoveryExecuteClaimStreamCloseDoesNotMisclassifyPostWriteFenceFailure(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "stream-close-post-write-fence-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	source.streamClose = func(call int) error {
		if call == 2 {
			return provider.ErrRsyncRestoreSourceDrift
		}
		return nil
	}
	target.afterWrite = func(call int) {
		if call == 2 {
			fixture.now = claim.LeaseExpiresAt.Add(time.Second)
		}
	}
	executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
	if !errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("post-write fence failure error=%v, want ErrRecoveryWorkerFenceLost", executeErr)
	}
	if len(target.writes) != 2 || len(target.verifies) != 1 {
		t.Fatalf("post-write fence target writes/verifies=%d/%d, want 2/1", len(target.writes), len(target.verifies))
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var evidence int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("job_id = ? AND kind = ?", claim.JobID, "failure").Count(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	if job.FailureCategory == "source_revalidation_failed" || evidence != 0 {
		t.Fatalf("post-write fence was misclassified from prior checkpoint job=%+v failure_evidence=%d", job, evidence)
	}
}

func assertRecoverySourceRevalidationTerminalProjection(
	t *testing.T,
	db *gorm.DB,
	claim RecoveryWorkerClaim,
	before model.BackupAssetRecoveryJob,
	executeErr error,
) {
	t.Helper()
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ? AND phase = ?", before.ID, CheckpointPhaseOperation).
		Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	var after model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", before.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 1 {
		t.Fatalf("source-drift operation checkpoints=%+v, want exactly one retained operation", checkpoints)
	}
	checkpoint := checkpoints[0]
	if checkpoint.JobItemID == "" || checkpoint.AttemptID != claim.AttemptID ||
		checkpoint.AttemptFence != claim.AttemptFence || checkpoint.NodeFence != claim.NodeFence ||
		checkpoint.PriorTargetRevision != before.TargetChainRevision || checkpoint.NextTargetRevision == "" ||
		checkpoint.NextTargetRevision == checkpoint.PriorTargetRevision ||
		checkpoint.UnresolvedCategory != "" || checkpoint.WriteResultDigest != "" ||
		checkpoint.ObservationDigest != "" || checkpoint.SourceRevalidationOutcome != "" {
		t.Fatalf("source-drift retained operation checkpoint=%+v", checkpoint)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := db.Where("id = ? AND job_id = ?", checkpoint.JobItemID, claim.JobID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Outcome != "succeeded" || item.FailureCategory != "" ||
		item.VerifiedDigest != item.ExpectedPostIdentityDigest || item.VerifiedSize != item.ExpectedPostBytes {
		t.Fatalf("source-drift retained item=%+v", item)
	}
	if after.State != string(JobStateNeedsAttention) ||
		after.FailureCategory != "source_revalidation_failed" ||
		after.TransitionRevision != before.TransitionRevision+1 ||
		after.WorkspacePhase != string(WorkspacePhaseCleanupDue) ||
		after.TargetChainRevision != checkpoint.NextTargetRevision {
		t.Fatalf("source-drift terminal job=%+v checkpoint=%+v", after, checkpoint)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	var source model.RecoveryPointLease
	if err := db.Where("id = ?", claim.SourceFence.LeaseID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	var node model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", claim.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateFailed) || attempt.ClosedAt == nil ||
		source.Status != string(backupasset.LeaseReleased) || source.ReleasedAt == nil ||
		node.State != "released" || node.ReleasedAt == nil {
		t.Fatalf("source-drift retained authority attempt/source/node=%+v/%+v/%+v", attempt, source, node)
	}
	var evidence []model.BackupAssetRecoveryEvidence
	if err := db.Where("job_id = ? AND kind = ?", claim.JobID, "failure").Find(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Outcome != "needs_attention" ||
		evidence[0].CheckpointID == nil || *evidence[0].CheckpointID != checkpoint.ID ||
		evidence[0].AttemptID == nil || *evidence[0].AttemptID != claim.AttemptID ||
		evidence[0].SourceLeaseID == nil || *evidence[0].SourceLeaseID != claim.SourceFence.LeaseID ||
		evidence[0].NodeLeaseID == nil || *evidence[0].NodeLeaseID != claim.NodeLeaseID ||
		evidence[0].NodeLeaseFence != claim.NodeFence || evidence[0].DifferenceCount != 0 ||
		evidence[0].VerifiedAt == nil {
		t.Fatalf("source-drift failure evidence=%+v", evidence)
	}
	for _, raw := range []string{"items/item-0000", before.EncryptedWorkspaceRelativeLocator} {
		if raw != "" && strings.Contains(executeErr.Error(), raw) {
			t.Fatalf("source-drift execution leaked raw locator %q: %v", raw, executeErr)
		}
	}
}

func assertRecoveryRevalidationProjection(
	t *testing.T,
	db *gorm.DB,
	before model.BackupAssetRecoveryJob,
	wantCheckpoints int64,
	wantTerminalItems int64,
	wantChainAdvance bool,
	executeErr error,
) {
	t.Helper()
	var checkpoints int64
	if err := db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND phase = ?", before.ID, CheckpointPhaseOperation).
		Count(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	var terminalItems int64
	if err := db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND outcome IN ?", before.ID, []string{"succeeded", "skipped"}).
		Count(&terminalItems).Error; err != nil {
		t.Fatal(err)
	}
	var after model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", before.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	chainAdvanced := after.TargetChainRevision != before.TargetChainRevision
	if checkpoints != wantCheckpoints || terminalItems != wantTerminalItems ||
		after.State == string(JobStateSucceeded) || chainAdvanced != wantChainAdvance {
		t.Fatalf("source revalidation projection checkpoints=%d items=%d job=%q chain_advanced=%t, want %d/%d/non-success/%t",
			checkpoints, terminalItems, after.State, chainAdvanced,
			wantCheckpoints, wantTerminalItems, wantChainAdvance)
	}
	for _, raw := range []string{"items/item-0000", before.EncryptedWorkspaceRelativeLocator} {
		if raw != "" && strings.Contains(executeErr.Error(), raw) {
			t.Fatalf("source revalidation execution leaked raw locator %q: %v", raw, executeErr)
		}
	}
}

func TestRecoveryExecuteClaimClosesSourceOnPreCanceledEntry(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "pre-canceled-source-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	var before model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	source := &recoveryExecutionSourceFake{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	executeErr := coordinator.ExecuteClaim(ctx, claim, source, "")
	if !errors.Is(executeErr, context.Canceled) {
		t.Fatalf("pre-canceled execution error=%v, want context.Canceled", executeErr)
	}
	if source.revalidates != 0 || source.closes != 1 {
		t.Fatalf("pre-canceled source lifecycle revalidates=%d closes=%d, want 0/1", source.revalidates, source.closes)
	}
	assertRecoveryExecutionZeroEffects(t, fixture.db, before, target, source, executeErr)
}

func TestRecoveryOrdinaryExecutionLocksPlanBeforeJob(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	coordinator.target = &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	claim, found, err := coordinator.ClaimNext(context.Background(), "ordinary-lock-order-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("prepare ordinary first write: %v", err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ? AND outcome = ''", claim.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	handoff, err := coordinator.loadOrdinaryOperationHandoff(context.Background(), claim, item.ID)
	if err != nil {
		t.Fatalf("load ordinary operation handoff: %v", err)
	}

	var mu sync.Mutex
	lockedTables := make([]string, 0, 2)
	callbackName := "recovery:ordinary-plan-before-job:" + t.Name()
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, locked := tx.Statement.Clauses["FOR"]; !locked || tx.Statement.Schema == nil {
			return
		}
		table := tx.Statement.Schema.Table
		if table != (model.BackupAssetRecoveryPlan{}).TableName() &&
			table != (model.BackupAssetRecoveryJob{}).TableName() {
			return
		}
		mu.Lock()
		lockedTables = append(lockedTables, table)
		mu.Unlock()
	}); err != nil {
		t.Fatalf("register lock-order observer: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove lock-order observer: %v", err)
		}
	})

	err = fixture.db.Transaction(func(tx *gorm.DB) error {
		_, lockErr := coordinator.lockOrdinaryExecutionTx(
			context.Background(), tx, claim, handoff, handoff.job.TargetChainRevision, fixture.now,
		)
		return lockErr
	})
	if err != nil {
		t.Fatalf("lock ordinary execution: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), lockedTables...)
	mu.Unlock()
	want := []string{
		(model.BackupAssetRecoveryPlan{}).TableName(),
		(model.BackupAssetRecoveryJob{}).TableName(),
	}
	if len(got) < len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ordinary execution plan/job lock order=%v, want prefix %v", got, want)
	}
}

func TestRecoveryExecuteClaimSourceRevalidationFailureHasZeroTargetMutation(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "source-revalidation-failure-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	var before model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	if TargetMode(before.TargetMode) != TargetModeIsolated {
		t.Fatalf("source revalidation fixture target mode=%q, want isolated", before.TargetMode)
	}
	executor, ok := any(coordinator).(recoveryOrdinaryClaimExecutor)
	if !ok {
		t.Fatal("WorkerCoordinator has no ordinary fenced ExecuteClaim boundary")
	}
	source := &recoveryExecutionSourceFake{revalidateErr: errors.New("source revalidation failed")}
	executeErr := executor.ExecuteClaim(context.Background(), claim, source, "")
	if executeErr == nil {
		t.Fatal("source revalidation failure returned nil")
	}
	if source.revalidates != 1 || source.closes != 1 {
		t.Fatalf("source lifecycle revalidates=%d closes=%d, want 1/1", source.revalidates, source.closes)
	}
	assertRecoveryExecutionZeroEffects(t, fixture.db, before, target, source, executeErr)
}

type recoveryExecutionWriteCall struct {
	permit    TargetWritePermit
	authority targetItemWriteAuthority
	request   TargetWriteAtomicRequest
	payload   string
}

type recoveryExecutionTargetState struct {
	kind           TargetPresenceKind
	identityDigest string
	bytes          int64
	revision       string
}

type recoveryExecutionTargetFake struct {
	closedTargetPortFake
	db                      *gorm.DB
	now                     func() time.Time
	beforeWorkspaceCreate   func(context.Context, CreateOwnedJobDirRequest) error
	workspaceCreateErr      error
	beforeWriteAuthority    func(context.Context, int, TargetWritePermit, TargetWriteAtomicRequest) error
	beforeFinalizeOverwrite func(context.Context) error
	mutateWritePermit       func(int, TargetWritePermit) TargetWritePermit
	beforeWrite             func(context.Context, int, TargetWriteAtomicRequest) error
	afterWrite              func(int)
	afterVerify             func(int)
	writeErr                func(int) error
	writeResult             func(int, TargetWriteResult) TargetWriteResult
	verifyObservation       func(int, TargetVerifyObservation) TargetVerifyObservation
	verifyErr               func(int) error
	lstatResult             func(int, TargetLstatResult) TargetLstatResult
	lstatErr                func(int) error
	deleteErr               func(int) error
	states                  map[TargetObjectRef]recoveryExecutionTargetState
	workspaceCalls          []CreateOwnedJobDirRequest
	writeAttempts           int
	finalizeOverwriteCalls  int
	lstatPermits            []TargetVerifyPermit
	lstats                  []TargetLstatRequest
	writes                  []recoveryExecutionWriteCall
	deletes                 []TargetObjectRef
	verifyPermits           []TargetVerifyPermit
	verifyObjects           []TargetObjectRef
	verifies                []TargetVerifyExpectation
}

func (fake *recoveryExecutionTargetFake) CreateOwnedJobDir(
	ctx context.Context,
	permit TargetWritePermit,
	request CreateOwnedJobDirRequest,
) (OwnedJobDir, error) {
	if fake.now == nil || permit.ValidateObjectAt(fake.now().UTC(), request.Object) != nil {
		return OwnedJobDir{}, ErrInvalidTargetPermit
	}
	if fake.db != nil {
		var count int64
		result := fake.db.WithContext(ctx).Model(&model.BackupAssetRecoveryEvidence{}).
			Where("id = ? AND kind = ?", recoverySchemaUseLatchRowID, RecoverySchemaUseLatchID).
			Count(&count)
		if result.Error != nil || count != 1 {
			return OwnedJobDir{}, errors.New("recovery mutation preceded durable schema-use latch")
		}
	}
	if fake.beforeWorkspaceCreate != nil {
		if err := fake.beforeWorkspaceCreate(ctx, request); err != nil {
			return OwnedJobDir{}, err
		}
	}
	fake.workspaceCalls = append(fake.workspaceCalls, request)
	if fake.workspaceCreateErr != nil {
		return OwnedJobDir{}, fake.workspaceCreateErr
	}
	return OwnedJobDir{
		Object: request.Object, MarkerBindingDigest: request.MarkerBindingDigest,
		TargetRevision: "target-revision-workspace-created",
	}, nil
}

func (fake *recoveryExecutionTargetFake) Lstat(
	ctx context.Context,
	permit TargetVerifyPermit,
	request TargetLstatRequest,
) (TargetLstatResult, error) {
	if fake.now == nil || permit.ValidateObjectAt(fake.now().UTC(), request.Object) != nil || fake.db == nil {
		return TargetLstatResult{}, ErrInvalidTargetPermit
	}
	var items []model.BackupAssetRecoveryJobItem
	loaded := fake.db.WithContext(ctx).Where("target_object_digest = ?", request.Object.TargetPathDigest).
		Limit(2).Find(&items)
	if loaded.Error != nil {
		return TargetLstatResult{}, loaded.Error
	}
	if len(items) != 1 {
		return TargetLstatResult{}, errors.New("target lstat binding is not unique")
	}
	item := items[0]
	state, found := fake.states[request.Object]
	if !found {
		state = recoveryExecutionTargetState{
			kind: TargetPresenceKind(item.ExpectedPriorKind), identityDigest: item.ExpectedPriorDigest,
			revision: "target-revision-initial",
		}
	}
	result := TargetLstatResult{TargetRevision: state.revision}
	switch state.kind {
	case TargetPresenceAbsent:
		result.Kind = TargetEntryMissing
	case TargetPresencePresent:
		result.IdentityDigest = state.identityDigest
		switch RecoveryDisplayClass(item.DisplayClass) {
		case RecoveryDisplayClassRegular:
			result.Kind = TargetEntryRegular
		case RecoveryDisplayClassDirectory:
			result.Kind = TargetEntryDirectory
		case RecoveryDisplayClassLink:
			result.Kind = TargetEntrySymlink
		case RecoveryDisplayClassSpecial:
			result.Kind = TargetEntrySpecial
		default:
			return TargetLstatResult{}, errors.New("target lstat display class is invalid")
		}
	default:
		return TargetLstatResult{}, errors.New("target lstat presence is invalid")
	}
	fake.lstatPermits = append(fake.lstatPermits, permit)
	fake.lstats = append(fake.lstats, request)
	if fake.lstatErr != nil {
		if err := fake.lstatErr(len(fake.lstats)); err != nil {
			return TargetLstatResult{}, err
		}
	}
	if fake.lstatResult != nil {
		result = fake.lstatResult(len(fake.lstats), result)
	}
	return result, nil
}

func (fake *recoveryExecutionTargetFake) WriteAtomic(
	ctx context.Context,
	permit TargetWritePermit,
	request TargetWriteAtomicRequest,
) (TargetWriteResult, error) {
	fake.writeAttempts++
	if fake.mutateWritePermit != nil {
		permit = fake.mutateWritePermit(fake.writeAttempts, permit)
	}
	if fake.now == nil {
		return TargetWriteResult{}, ErrInvalidTargetPermit
	}
	authority, err := permit.validateItemWriteAt(fake.now().UTC(), request)
	if err != nil {
		return TargetWriteResult{}, ErrInvalidTargetPermit
	}
	if fake.beforeWriteAuthority != nil {
		if err := fake.beforeWriteAuthority(ctx, fake.writeAttempts, permit, request); err != nil {
			return TargetWriteResult{}, err
		}
	}
	payload, err := io.ReadAll(request.Content)
	if err != nil {
		return TargetWriteResult{}, err
	}
	fake.writes = append(fake.writes, recoveryExecutionWriteCall{
		permit: permit, authority: authority, request: request, payload: string(payload),
	})
	if fake.beforeWrite != nil {
		if err := fake.beforeWrite(ctx, len(fake.writes), request); err != nil {
			return TargetWriteResult{}, err
		}
	}
	identityDigest := fmt.Sprintf("%x", sha256.Sum256(payload))
	revision := "target-revision-write-" + string(rune('a'+len(fake.writes)))
	fake.rememberTargetState(request.Object, recoveryExecutionTargetState{
		kind: TargetPresencePresent, identityDigest: identityDigest, bytes: int64(len(payload)), revision: revision,
	})
	if fake.afterWrite != nil {
		fake.afterWrite(len(fake.writes))
	}
	if fake.writeErr != nil {
		if err := fake.writeErr(len(fake.writes)); err != nil {
			return TargetWriteResult{}, err
		}
	}
	result := TargetWriteResult{
		BytesWritten: int64(len(payload)), IdentityDigest: identityDigest,
		TargetRevision: revision,
	}
	if fake.writeResult != nil {
		result = fake.writeResult(len(fake.writes), result)
	}
	return result, nil
}

func (fake *recoveryExecutionTargetFake) FinalizeOverwrite(
	ctx context.Context,
	permit TargetFinalizeOverwritePermit,
	request TargetFinalizeOverwriteRequest,
) (TargetWriteResult, error) {
	if fake.now == nil {
		return TargetWriteResult{}, ErrInvalidTargetPermit
	}
	authority, err := permit.authorityAt(fake.now().UTC(), request)
	if err != nil {
		return TargetWriteResult{}, ErrInvalidTargetPermit
	}
	fake.finalizeOverwriteCalls++
	if fake.beforeFinalizeOverwrite != nil {
		if err := fake.beforeFinalizeOverwrite(ctx); err != nil {
			return TargetWriteResult{}, err
		}
	}
	state, err := fake.observeTargetState(request.Object)
	if err != nil {
		return TargetWriteResult{}, err
	}
	if authority.object != request.Object || state.kind != TargetPresencePresent ||
		state.identityDigest != request.ExpectedDigest || state.bytes != request.ExpectedBytes ||
		!validOpaqueRevision(state.revision) {
		return TargetWriteResult{}, ErrRecoveryTargetChanged
	}
	return TargetWriteResult{
		BytesWritten: request.ExpectedBytes, IdentityDigest: request.ExpectedDigest,
		TargetRevision: state.revision,
	}, nil
}

func TestRecoveryExecuteClaimCreateCarriesExactItemWriteProof(t *testing.T) {
	t.Run("exact create proof reaches target after source open", func(t *testing.T) {
		fixture := newRecoveryExecutionFixture(t)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
		coordinator.target = target
		claim, found, err := coordinator.ClaimNext(context.Background(), "exact-create-item-proof")
		if err != nil || !found || claim.JobID != executed.JobID {
			t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
		}
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		target.beforeWriteAuthority = func(
			ctx context.Context,
			attempt int,
			permit TargetWritePermit,
			request TargetWriteAtomicRequest,
		) error {
			if len(source.opened) != attempt {
				return fmt.Errorf("source streams=%d, want %d before target write", len(source.opened), attempt)
			}
			if _, err := permit.validateItemWriteAt(fixture.now, request); err != nil {
				return err
			}
			var count int64
			loaded := fixture.db.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
				Where("job_id = ? AND target_object_digest = ?", claim.JobID, request.Object.TargetPathDigest).
				Count(&count)
			if loaded.Error != nil || count != 1 {
				return fmt.Errorf("durable target item count=%d error=%v", count, loaded.Error)
			}
			return nil
		}
		if err := coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
			t.Fatalf("execute exact create proof: %v", err)
		}
		if len(target.writes) != 2 || len(target.verifies) != 3 || len(source.opened) != 2 {
			t.Fatalf("exact execution writes/verifies/source=%d/%d/%d, want 2/3/2",
				len(target.writes), len(target.verifies), len(source.opened))
		}
		create := target.writes[0]
		authority := create.authority
		if authority.jobID != claim.JobID || authority.targetMode != TargetModeIsolated ||
			authority.operation != RecoveryOperationCreate ||
			authority.expectedPrior != (ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}) ||
			create.permit.itemProof == nil || create.permit.itemProof.object != create.request.Object ||
			create.permit.itemProof.expectedDigest != create.request.ExpectedDigest ||
			create.permit.itemProof.expectedBytes != create.request.ExpectedBytes {
			t.Fatalf("recorded create authority=%+v proof=%+v request=%+v",
				authority, create.permit.itemProof, create.request)
		}
		if target.verifyObjects[0] != create.request.Object || target.verifies[0].Present == nil ||
			target.verifies[0].Present.IdentityDigest != create.request.ExpectedDigest ||
			target.verifies[0].Present.Bytes != create.request.ExpectedBytes ||
			source.opened[0].ExpectedDigest != create.request.ExpectedDigest ||
			source.opened[0].ExpectedSize != create.request.ExpectedBytes {
			t.Fatalf("create source/write/verify mismatch source=%+v write=%+v verify=%+v",
				source.opened[0], create, target.verifies[0])
		}
	})

	t.Run("proof removal inside target becomes unresolved before remote mutation", func(t *testing.T) {
		fixture := newRecoveryExecutionFixture(t)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
		target.mutateWritePermit = func(attempt int, permit TargetWritePermit) TargetWritePermit {
			if attempt == 1 {
				permit.itemProof = nil
			}
			return permit
		}
		coordinator.target = target
		claim, found, err := coordinator.ClaimNext(context.Background(), "removed-create-item-proof")
		if err != nil || !found || claim.JobID != executed.JobID {
			t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
		}
		var before model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
			t.Fatal(err)
		}
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
		if !errors.Is(executeErr, ErrInvalidTargetVerification) ||
			errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("removed proof execution error=%v, want unresolved target verification", executeErr)
		}
		if target.writeAttempts != 1 || len(target.writes) != 0 || len(target.states) != 0 ||
			len(target.verifies) != 0 || len(source.opened) != 1 {
			t.Fatalf("removed proof attempts/writes/states/verifies/source=%d/%d/%d/%d/%d, want 1/0/0/0/1",
				target.writeAttempts, len(target.writes), len(target.states), len(target.verifies), len(source.opened))
		}
		assertRecoveryUnresolvedOutcomeProjection(
			t, fixture.db, claim, before, UnresolvedOperationWriteResultInvalid,
			SourceRevalidationMatched, 0,
		)
	})
}

func TestRecoveryOverwriteCheckpointPrecedesPublishedMarkerFinalize(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionFixture(t)
	db := fixture.serviceFixture.db
	claim := fixture.claim
	target := fixture.target

	var overwriteItem model.BackupAssetRecoveryJobItem
	if err := db.Where(
		"job_id = ? AND operation_kind = ?", claim.JobID, RecoveryOperationOverwrite,
	).Take(&overwriteItem).Error; err != nil {
		t.Fatal(err)
	}
	overwriteWriteObserved := false
	target.beforeWriteAuthority = func(
		ctx context.Context,
		_ int,
		permit TargetWritePermit,
		request TargetWriteAtomicRequest,
	) error {
		authority, err := permit.validateItemWriteAt(fixture.serviceFixture.now, request)
		if err != nil || authority.operation != RecoveryOperationOverwrite {
			return err
		}
		overwriteWriteObserved = true
		if authority.jobItemID != overwriteItem.ID {
			return fmt.Errorf("overwrite write item=%q, want %q", authority.jobItemID, overwriteItem.ID)
		}
		var checkpointCount int64
		if err := db.WithContext(ctx).Model(&model.BackupAssetRecoveryCheckpoint{}).
			Where("job_id = ? AND job_item_id = ? AND phase = ?",
				claim.JobID, overwriteItem.ID, CheckpointPhaseOperation).
			Count(&checkpointCount).Error; err != nil {
			return err
		}
		var current model.BackupAssetRecoveryJobItem
		if err := db.WithContext(ctx).Where("id = ?", overwriteItem.ID).Take(&current).Error; err != nil {
			return err
		}
		if checkpointCount != 0 || current.Outcome != "" || current.FailureCategory != "" {
			return fmt.Errorf(
				"overwrite publish preceded durable projection: checkpoints=%d item=%+v",
				checkpointCount, current,
			)
		}
		return nil
	}
	target.beforeFinalizeOverwrite = func(ctx context.Context) error {
		var checkpoints []model.BackupAssetRecoveryCheckpoint
		if err := db.WithContext(ctx).Where(
			"job_id = ? AND job_item_id = ? AND phase = ?",
			claim.JobID, overwriteItem.ID, CheckpointPhaseOperation,
		).Find(&checkpoints).Error; err != nil {
			return err
		}
		var item model.BackupAssetRecoveryJobItem
		if err := db.WithContext(ctx).Where("id = ?", overwriteItem.ID).Take(&item).Error; err != nil {
			return err
		}
		var job model.BackupAssetRecoveryJob
		if err := db.WithContext(ctx).Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
			return err
		}
		var attempt model.BackupAssetRecoveryAttempt
		if err := db.WithContext(ctx).Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
			return err
		}
		var sourceLease model.RecoveryPointLease
		if err := db.WithContext(ctx).Where("id = ?", claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
			return err
		}
		var nodeLease model.BackupAssetRecoveryNodeLease
		if err := db.WithContext(ctx).Where("id = ?", claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
			return err
		}
		if len(checkpoints) != 1 || checkpoints[0].OperationDigest != recoveryJobItemOperationDigest(item) ||
			checkpoints[0].NextTargetRevision == checkpoints[0].PriorTargetRevision ||
			job.TargetChainRevision != checkpoints[0].NextTargetRevision ||
			item.Outcome != "succeeded" || item.FailureCategory != "" ||
			item.VerifiedDigest != item.ExpectedPostIdentityDigest ||
			item.VerifiedSize != item.ExpectedPostBytes || job.State != string(JobStateRunning) ||
			attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil ||
			sourceLease.Status != string(backupasset.LeaseActive) ||
			nodeLease.State != "active" || len(target.writes) != 2 {
			return fmt.Errorf(
				"overwrite finalize preceded durable checkpoint or retained authority: checkpoints=%+v item=%+v job=%+v attempt=%+v source=%+v node=%+v writes=%d",
				checkpoints, item, job, attempt, sourceLease, nodeLease, len(target.writes),
			)
		}
		return nil
	}

	source := newRecoveryRepositoryContractSource(t, db, claim.JobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
		t.Fatalf("execute overwrite checkpoint/finalize ordering: %v", err)
	}
	if !overwriteWriteObserved || target.finalizeOverwriteCalls != 1 {
		t.Fatalf(
			"overwrite write observed=%t finalize calls=%d, want true/1",
			overwriteWriteObserved, target.finalizeOverwriteCalls,
		)
	}
}

func TestRecoveryOverwriteCheckpointPrecedesPublishedMarkerFinalizePostgres069(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionPostgresMigrationFixture(t)
	db := fixture.serviceFixture.db
	claim := fixture.claim
	target := fixture.target
	var overwriteItem model.BackupAssetRecoveryJobItem
	if err := db.Where(
		"job_id = ? AND operation_kind = ?", claim.JobID, RecoveryOperationOverwrite,
	).Take(&overwriteItem).Error; err != nil {
		t.Fatal(err)
	}
	target.beforeFinalizeOverwrite = func(ctx context.Context) error {
		var checkpoint model.BackupAssetRecoveryCheckpoint
		if err := db.WithContext(ctx).Where(
			"job_id = ? AND job_item_id = ? AND phase = ?",
			claim.JobID, overwriteItem.ID, CheckpointPhaseOperation,
		).Take(&checkpoint).Error; err != nil {
			return err
		}
		var job model.BackupAssetRecoveryJob
		if err := db.WithContext(ctx).Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
			return err
		}
		var attempt model.BackupAssetRecoveryAttempt
		if err := db.WithContext(ctx).Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
			return err
		}
		var sourceLease model.RecoveryPointLease
		if err := db.WithContext(ctx).Where("id = ?", claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
			return err
		}
		var nodeLease model.BackupAssetRecoveryNodeLease
		if err := db.WithContext(ctx).Where("id = ?", claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
			return err
		}
		if checkpoint.NextTargetRevision == checkpoint.PriorTargetRevision ||
			job.TargetChainRevision != checkpoint.NextTargetRevision ||
			attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil ||
			sourceLease.Status != string(backupasset.LeaseActive) || nodeLease.State != "active" {
			return fmt.Errorf("PostgreSQL overwrite finalize ordering checkpoint=%+v job=%+v attempt=%+v source=%+v node=%+v",
				checkpoint, job, attempt, sourceLease, nodeLease)
		}
		return nil
	}
	source := newRecoveryRepositoryContractSource(t, db, claim.JobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
		t.Fatalf("execute PostgreSQL overwrite checkpoint/finalize ordering: %v", err)
	}
	if target.finalizeOverwriteCalls != 1 {
		t.Fatalf("PostgreSQL overwrite finalize calls=%d, want 1", target.finalizeOverwriteCalls)
	}
}

func TestRecoveryCompletedOverwriteReconcilesBeforeNextItem(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionFixture(t)
	db := fixture.serviceFixture.db
	claim := fixture.claim
	target := fixture.target

	var overwriteItem model.BackupAssetRecoveryJobItem
	if err := db.Where(
		"job_id = ? AND operation_kind = ?", claim.JobID, RecoveryOperationOverwrite,
	).Take(&overwriteItem).Error; err != nil {
		t.Fatal(err)
	}
	finalizeFailure := errors.New("simulated overwrite finalize interruption")
	target.beforeFinalizeOverwrite = func(context.Context) error { return finalizeFailure }
	firstSource := newRecoveryRepositoryContractSource(t, db, claim.JobID)
	firstErr := fixture.coordinator.ExecuteClaim(context.Background(), claim, firstSource, "")
	if firstErr == nil || target.finalizeOverwriteCalls != 1 {
		t.Fatalf("interrupted overwrite finalize err=%v calls=%d, want interruption/1", firstErr, target.finalizeOverwriteCalls)
	}

	var checkpoint model.BackupAssetRecoveryCheckpoint
	if err := db.Where(
		"job_id = ? AND job_item_id = ? AND phase = ?",
		claim.JobID, overwriteItem.ID, CheckpointPhaseOperation,
	).Take(&checkpoint).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	var sourceLease model.RecoveryPointLease
	if err := db.Where("id = ?", claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatal(err)
	}
	if checkpoint.NextTargetRevision == checkpoint.PriorTargetRevision ||
		attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil ||
		sourceLease.Status != string(backupasset.LeaseActive) || nodeLease.State != "active" {
		t.Fatalf("interrupted overwrite released authority checkpoint=%+v attempt=%+v source=%+v node=%+v",
			checkpoint, attempt, sourceLease, nodeLease)
	}

	target.beforeFinalizeOverwrite = func(ctx context.Context) error {
		var next model.BackupAssetRecoveryJobItem
		if err := db.WithContext(ctx).Where(
			"job_id = ? AND ordinal > ?", claim.JobID, overwriteItem.Ordinal,
		).Order("ordinal ASC").Take(&next).Error; err != nil {
			return err
		}
		if next.Outcome != "" || next.FailureCategory != "" {
			return fmt.Errorf("next item advanced before overwrite finalize: %+v", next)
		}
		return nil
	}
	resumeSource := newRecoveryRepositoryContractSource(t, db, claim.JobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), claim, resumeSource, ""); err != nil {
		t.Fatalf("resume checkpointed overwrite: %v", err)
	}
	if target.finalizeOverwriteCalls != 2 {
		t.Fatalf("checkpointed overwrite finalize calls=%d, want exactly 2", target.finalizeOverwriteCalls)
	}
}

func TestRecoveryCheckpointedOverwriteTakeoverRevalidatesAndFinalizesBeforeContinuation(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionFixture(t)
	db := fixture.serviceFixture.db
	original := fixture.claim
	target := fixture.target

	target.beforeFinalizeOverwrite = func(context.Context) error {
		return errors.New("simulated checkpoint-to-finalize crash")
	}
	firstSource := newRecoveryRepositoryContractSource(t, db, original.JobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), original, firstSource, ""); err == nil {
		t.Fatal("checkpoint-to-finalize crash unexpectedly completed")
	}
	if target.finalizeOverwriteCalls != 1 {
		t.Fatalf("initial overwrite finalize calls=%d, want 1", target.finalizeOverwriteCalls)
	}

	fixture.serviceFixture.now = original.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := fixture.coordinator.TakeoverExpired(
		context.Background(), "overwrite-finalize-takeover",
	)
	if err != nil || !found || takeover.JobID != original.JobID ||
		takeover.AttemptID == original.AttemptID {
		t.Fatalf("take over checkpointed overwrite: claim=%+v found=%t err=%v", takeover, found, err)
	}
	resumeSource := newRecoveryRepositoryContractSource(t, db, takeover.JobID)
	target.beforeFinalizeOverwrite = func(ctx context.Context) error {
		if resumeSource.revalidates == 0 {
			return errors.New("overwrite takeover finalized before fresh source revalidation")
		}
		var next model.BackupAssetRecoveryJobItem
		if err := db.WithContext(ctx).Where(
			"job_id = ? AND outcome = ''", takeover.JobID,
		).Order("ordinal ASC").Take(&next).Error; err != nil {
			return err
		}
		if next.OperationKind != string(RecoveryOperationSkip) {
			return fmt.Errorf("takeover next item=%+v, want pending skip", next)
		}
		return nil
	}
	if err := fixture.coordinator.ExecuteClaim(
		context.Background(), takeover, resumeSource, "",
	); err != nil {
		t.Fatalf("continue checkpointed overwrite takeover: %v", err)
	}
	if target.finalizeOverwriteCalls != 2 || resumeSource.revalidates == 0 {
		t.Fatalf("takeover finalize/revalidates=%d/%d, want 2/non-zero",
			target.finalizeOverwriteCalls, resumeSource.revalidates)
	}
}

func TestRecoveryCheckpointedOverwriteFinalizesBeforeSourceFailureProjection(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionFixture(t)
	db := fixture.serviceFixture.db
	claim := fixture.claim
	target := fixture.target

	target.beforeFinalizeOverwrite = func(context.Context) error {
		return errors.New("simulated overwrite finalize crash before source failure")
	}
	firstSource := newRecoveryRepositoryContractSource(t, db, claim.JobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), claim, firstSource, ""); err == nil {
		t.Fatal("initial overwrite finalize crash unexpectedly completed")
	}
	if target.finalizeOverwriteCalls != 1 {
		t.Fatalf("initial overwrite finalize calls=%d, want 1", target.finalizeOverwriteCalls)
	}

	target.beforeFinalizeOverwrite = func(ctx context.Context) error {
		var attempt model.BackupAssetRecoveryAttempt
		if err := db.WithContext(ctx).Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
			return err
		}
		var sourceLease model.RecoveryPointLease
		if err := db.WithContext(ctx).Where("id = ?", claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
			return err
		}
		var nodeLease model.BackupAssetRecoveryNodeLease
		if err := db.WithContext(ctx).Where("id = ?", claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
			return err
		}
		if attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil ||
			sourceLease.Status != string(backupasset.LeaseActive) || nodeLease.State != "active" {
			return fmt.Errorf("source failure released authority before finalize attempt=%+v source=%+v node=%+v",
				attempt, sourceLease, nodeLease)
		}
		return nil
	}
	driftedSource := newRecoveryRepositoryContractSource(t, db, claim.JobID)
	driftedSource.revalidate = func(int) error { return provider.ErrRsyncRestoreSourceDrift }
	executeErr := fixture.coordinator.ExecuteClaim(context.Background(), claim, driftedSource, "")
	if !errors.Is(executeErr, ErrRecoverySourceChanged) {
		t.Fatalf("checkpointed overwrite source drift err=%v, want ErrRecoverySourceChanged", executeErr)
	}
	if target.finalizeOverwriteCalls != 2 {
		t.Fatalf("checkpointed overwrite finalize calls=%d, want 2 before source failure", target.finalizeOverwriteCalls)
	}
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateNeedsAttention) || attempt.State != string(AttemptStateFailed) ||
		attempt.ClosedAt == nil {
		t.Fatalf("checkpointed overwrite source failure job=%+v attempt=%+v", job, attempt)
	}
}

func TestRecoveryCheckpointedOverwriteTakeoverFinalizesBeforeSourceFailureProjection(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionFixture(t)
	db := fixture.serviceFixture.db
	original := fixture.claim
	target := fixture.target

	target.beforeFinalizeOverwrite = func(context.Context) error {
		return errors.New("simulated overwrite finalize crash before takeover source failure")
	}
	firstSource := newRecoveryRepositoryContractSource(t, db, original.JobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), original, firstSource, ""); err == nil {
		t.Fatal("initial overwrite finalize crash unexpectedly completed")
	}
	fixture.serviceFixture.now = original.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := fixture.coordinator.TakeoverExpired(
		context.Background(), "overwrite-source-failure-takeover",
	)
	if err != nil || !found || takeover.AttemptID == original.AttemptID {
		t.Fatalf("take over checkpointed overwrite source failure: claim=%+v found=%t err=%v",
			takeover, found, err)
	}

	target.beforeFinalizeOverwrite = nil
	driftedSource := newRecoveryRepositoryContractSource(t, db, takeover.JobID)
	driftedSource.revalidate = func(int) error { return provider.ErrRsyncRestoreSourceDrift }
	executeErr := fixture.coordinator.ExecuteClaim(context.Background(), takeover, driftedSource, "")
	if !errors.Is(executeErr, ErrRecoverySourceChanged) {
		t.Fatalf("takeover checkpointed overwrite source drift err=%v, want ErrRecoverySourceChanged", executeErr)
	}
	if target.finalizeOverwriteCalls != 2 || driftedSource.revalidates == 0 {
		t.Fatalf("takeover source failure finalize/revalidates=%d/%d, want 2/non-zero",
			target.finalizeOverwriteCalls, driftedSource.revalidates)
	}
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", takeover.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", takeover.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateNeedsAttention) || attempt.State != string(AttemptStateFailed) ||
		attempt.ClosedAt == nil {
		t.Fatalf("takeover checkpointed overwrite source failure job=%+v attempt=%+v", job, attempt)
	}
}

func TestRecoveryMultipleOverwritesFinalizeBeforeFollowingItem(t *testing.T) {
	fixture := newInPlaceOrdinaryExecutionFixtureWithKinds(t, []RecoveryOperationKind{
		RecoveryOperationOverwrite, RecoveryOperationOverwrite, RecoveryOperationOverwrite,
	}, true)
	db := fixture.serviceFixture.db
	target := fixture.target
	claim := fixture.claim
	target.beforeFinalizeOverwrite = func(ctx context.Context) error {
		var completed int64
		if err := db.WithContext(ctx).Model(&model.BackupAssetRecoveryJobItem{}).
			Where("job_id = ? AND operation_kind = ? AND outcome = ?",
				claim.JobID, RecoveryOperationOverwrite, "succeeded").
			Count(&completed).Error; err != nil {
			return err
		}
		if completed != int64(target.finalizeOverwriteCalls) {
			return fmt.Errorf("completed overwrites=%d finalize call=%d",
				completed, target.finalizeOverwriteCalls)
		}
		return nil
	}
	source := newRecoveryRepositoryContractSource(t, db, claim.JobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
		t.Fatalf("execute multiple overwrite ordering: %v", err)
	}
	if len(target.writes) != 3 || target.finalizeOverwriteCalls != 3 {
		t.Fatalf("multiple overwrite writes/finalizes=%d/%d, want 3/3",
			len(target.writes), target.finalizeOverwriteCalls)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", claim.JobID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 4 || checkpoints[3].Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
		t.Fatalf("multiple overwrite checkpoints=%+v, want three operations then delete pause", checkpoints)
	}
}

func TestRecoveryLastOverwriteFinalizesBeforeJobCompletion(t *testing.T) {
	fixture := newInPlaceOrdinaryExecutionFixtureWithKinds(t, []RecoveryOperationKind{
		RecoveryOperationCreate, RecoveryOperationSkip, RecoveryOperationOverwrite,
	}, false)
	db := fixture.serviceFixture.db
	target := fixture.target
	claim := fixture.claim
	target.beforeFinalizeOverwrite = func(ctx context.Context) error {
		var job model.BackupAssetRecoveryJob
		if err := db.WithContext(ctx).Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
			return err
		}
		var attempt model.BackupAssetRecoveryAttempt
		if err := db.WithContext(ctx).Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
			return err
		}
		if job.State != string(JobStateRunning) || attempt.State != string(AttemptStateRunning) ||
			attempt.ClosedAt != nil {
			return fmt.Errorf("last overwrite terminalized before finalize job=%+v attempt=%+v", job, attempt)
		}
		return nil
	}
	source := newRecoveryRepositoryContractSource(t, db, claim.JobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
		t.Fatalf("execute last overwrite ordering: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if target.finalizeOverwriteCalls != 1 || job.State != string(JobStateSucceeded) ||
		attempt.State != string(AttemptStateCompleted) || attempt.ClosedAt == nil {
		t.Fatalf("last overwrite finalize/job/attempt=%d/%+v/%+v",
			target.finalizeOverwriteCalls, job, attempt)
	}
}

func TestRecoveryOverwriteCrashBeforeCompletionReplaysFinalizeThenCompletes(t *testing.T) {
	fixture := newInPlaceOrdinaryExecutionFixtureWithKinds(t, []RecoveryOperationKind{
		RecoveryOperationCreate, RecoveryOperationSkip, RecoveryOperationOverwrite,
	}, false)
	db := fixture.serviceFixture.db
	target := fixture.target
	claim := fixture.claim
	completionErr := errors.New("simulated crash after overwrite finalize before completion")
	const callbackName = "test:overwrite-finalize-before-completion-crash"
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil &&
			tx.Statement.Table == (model.BackupAssetRecoveryAttempt{}).TableName() &&
			target.finalizeOverwriteCalls == 1 {
			_ = tx.AddError(completionErr)
		}
	}); err != nil {
		t.Fatalf("register overwrite completion crash: %v", err)
	}
	firstSource := newRecoveryRepositoryContractSource(t, db, claim.JobID)
	firstErr := fixture.coordinator.ExecuteClaim(context.Background(), claim, firstSource, "")
	if err := db.Callback().Update().Remove(callbackName); err != nil {
		t.Fatalf("remove overwrite completion crash: %v", err)
	}
	if firstErr == nil || target.finalizeOverwriteCalls != 1 {
		t.Fatalf("overwrite completion crash err=%v finalize=%d, want failure/1",
			firstErr, target.finalizeOverwriteCalls)
	}
	var pending int64
	if err := db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND outcome = ''", claim.JobID).Count(&pending).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if pending != 0 || job.State != string(JobStateRunning) ||
		attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil {
		t.Fatalf("post-finalize completion crash pending=%d job=%+v attempt=%+v", pending, job, attempt)
	}

	resumeSource := newRecoveryRepositoryContractSource(t, db, claim.JobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), claim, resumeSource, ""); err != nil {
		t.Fatalf("resume overwrite before completion crash: %v", err)
	}
	if err := db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if target.finalizeOverwriteCalls != 2 || job.State != string(JobStateSucceeded) ||
		attempt.State != string(AttemptStateCompleted) || attempt.ClosedAt == nil ||
		len(resumeSource.materialized) != 0 {
		t.Fatalf("resumed overwrite completion finalize=%d job=%+v attempt=%+v materialized=%d",
			target.finalizeOverwriteCalls, job, attempt, len(resumeSource.materialized))
	}
}

// Delete is the missing concrete mutation arm under test. Until the A3b worker
// issuer lands, this fake admits the outer object-bound mutation permit only.
func (fake *recoveryExecutionTargetFake) Delete(
	_ context.Context,
	permit TargetDeletePermit,
	request TargetDeleteRequest,
) (TargetWriteResult, error) {
	object := request.Object
	if fake.now == nil || permit.permit.ValidateObjectAt(fake.now().UTC(), object) != nil {
		return TargetWriteResult{}, ErrInvalidTargetPermit
	}
	fake.deletes = append(fake.deletes, object)
	revision := "target-revision-delete"
	fake.rememberTargetState(object, recoveryExecutionTargetState{
		kind: TargetPresenceAbsent, revision: revision,
	})
	if fake.deleteErr != nil {
		if err := fake.deleteErr(len(fake.deletes)); err != nil {
			return TargetWriteResult{}, err
		}
	}
	return TargetWriteResult{TargetRevision: revision}, nil
}

func (fake *recoveryExecutionTargetFake) Verify(
	_ context.Context,
	permit TargetVerifyPermit,
	object TargetObjectRef,
	expectation TargetVerifyExpectation,
) (TargetVerifyObservation, error) {
	if fake.now == nil || permit.ValidateObjectAt(fake.now().UTC(), object) != nil {
		return TargetVerifyObservation{}, ErrInvalidTargetPermit
	}
	fake.verifyPermits = append(fake.verifyPermits, permit)
	fake.verifyObjects = append(fake.verifyObjects, object)
	fake.verifies = append(fake.verifies, cloneTargetVerifyExpectation(expectation))
	if fake.afterVerify != nil {
		fake.afterVerify(len(fake.verifies))
	}
	if fake.verifyErr != nil {
		if err := fake.verifyErr(len(fake.verifies)); err != nil {
			return TargetVerifyObservation{}, err
		}
	}
	state, err := fake.observeTargetState(object)
	if err != nil {
		return TargetVerifyObservation{}, err
	}
	observation := TargetVerifyObservation{
		Kind: state.kind, ObservedRevision: state.revision,
	}
	if state.kind == TargetPresencePresent {
		observation.Present = &PresentObservation{
			IdentityDigest: state.identityDigest,
			Bytes:          state.bytes,
		}
	}
	if state.kind == TargetPresenceAbsent {
		observation.Absent = &AbsentObservation{Evidence: TargetAbsenceEvidenceExact}
	}
	if fake.verifyObservation != nil {
		observation = fake.verifyObservation(len(fake.verifies), observation)
	}
	return observation, nil
}

func (fake *recoveryExecutionTargetFake) rememberTargetState(
	object TargetObjectRef,
	state recoveryExecutionTargetState,
) {
	if fake.states == nil {
		fake.states = make(map[TargetObjectRef]recoveryExecutionTargetState)
	}
	fake.states[object] = state
}

func (fake *recoveryExecutionTargetFake) observeTargetState(
	object TargetObjectRef,
) (recoveryExecutionTargetState, error) {
	if state, found := fake.states[object]; found {
		return state, nil
	}
	if fake.db == nil {
		return recoveryExecutionTargetState{}, errors.New("target state is unavailable")
	}
	var items []model.BackupAssetRecoveryJobItem
	loaded := fake.db.Where("target_object_digest = ?", object.TargetPathDigest).Limit(2).Find(&items)
	if loaded.Error != nil {
		return recoveryExecutionTargetState{}, loaded.Error
	}
	if len(items) != 1 {
		return recoveryExecutionTargetState{}, errors.New("target state binding is not unique")
	}
	item := items[0]
	state := recoveryExecutionTargetState{
		kind: TargetPresenceKind(item.ExpectedPriorKind), revision: "target-revision-initial",
	}
	switch state.kind {
	case TargetPresencePresent:
		if !validDigest(item.ExpectedPriorDigest) || item.ExpectedPriorBytes < 0 {
			return recoveryExecutionTargetState{}, errors.New("initial present target state is invalid")
		}
		state.identityDigest = item.ExpectedPriorDigest
		state.bytes = item.ExpectedPriorBytes
	case TargetPresenceAbsent:
	default:
		return recoveryExecutionTargetState{}, errors.New("initial target presence is invalid")
	}
	fake.rememberTargetState(object, state)
	return state, nil
}

func TestRecoveryExecuteClaimProjectsPostArmTargetMismatch(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*recoveryExecutionTargetFake)
	}{
		{
			name: "write verify mismatch",
			configure: func(target *recoveryExecutionTargetFake) {
				target.afterWrite = func(int) {
					corruptRecoveryExecutionTargetState(target)
				}
			},
		},
		{
			name: "intervening target change",
			configure: func(target *recoveryExecutionTargetFake) {
				target.afterVerify = func(int) {
					corruptRecoveryExecutionTargetState(target)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryExecutionFixture(t)
			executed, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
			test.configure(target)
			coordinator.target = target
			claim, found, err := coordinator.ClaimNext(context.Background(), "target-state-mismatch-"+test.name+"-worker")
			if err != nil || !found || claim.JobID != executed.JobID {
				t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
			}
			var item model.BackupAssetRecoveryJobItem
			if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Take(&item).Error; err != nil {
				t.Fatal(err)
			}
			before := loadRecoveryPendingOperationMismatchState(t, fixture.db, claim, item.ID)
			source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)

			executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
			if !errors.Is(executeErr, ErrInvalidTargetVerification) {
				t.Fatalf("target state mismatch error=%v, want ErrInvalidTargetVerification", executeErr)
			}
			if errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
				t.Fatalf("target state mismatch was misclassified as fence loss: %v", executeErr)
			}
			if len(target.workspaceCalls) != 1 || len(target.writes) != 1 || len(target.verifies) != 1 ||
				len(source.opened) != 1 || source.closes != 1 {
				t.Fatalf("target state mismatch workspace=%d writes=%d verifies=%d source_open=%d closes=%d, want 1/1/1/1/1",
					len(target.workspaceCalls), len(target.writes), len(target.verifies), len(source.opened), source.closes)
			}
			assertRecoveryPostArmMismatchProjection(t, fixture.db, claim, before)
		})
	}
}

func TestRecoveryExecuteClaimProjectsUnresolvedRemoteOutcomeMatrix(t *testing.T) {
	tests := []struct {
		name             string
		configure        func(*recoveryExecutionTargetFake, *recoveryRepositoryContractSource)
		category         UnresolvedOperationCategory
		sourceOutcome    SourceRevalidationOutcome
		wantWriteCalls   int
		wantVerifyCalls  int
		wantSuccessItems int64
	}{
		{
			name: "malformed nil-error write result",
			configure: func(target *recoveryExecutionTargetFake, _ *recoveryRepositoryContractSource) {
				target.writeResult = func(call int, result TargetWriteResult) TargetWriteResult {
					if call == 1 {
						return TargetWriteResult{BytesWritten: -1, IdentityDigest: "invalid", TargetRevision: ""}
					}
					return result
				}
			},
			category:        UnresolvedOperationWriteResultInvalid,
			sourceOutcome:   SourceRevalidationMatched,
			wantWriteCalls:  1,
			wantVerifyCalls: 0,
		},
		{
			name: "invalid observation",
			configure: func(target *recoveryExecutionTargetFake, _ *recoveryRepositoryContractSource) {
				target.verifyObservation = func(call int, observation TargetVerifyObservation) TargetVerifyObservation {
					if call == 1 {
						return TargetVerifyObservation{Kind: TargetPresencePresent}
					}
					return observation
				}
			},
			category:        UnresolvedOperationObservationInvalid,
			sourceOutcome:   SourceRevalidationMatched,
			wantWriteCalls:  1,
			wantVerifyCalls: 1,
		},
		{
			name: "write and observation revisions disagree",
			configure: func(target *recoveryExecutionTargetFake, _ *recoveryRepositoryContractSource) {
				target.writeResult = func(call int, result TargetWriteResult) TargetWriteResult {
					if call == 1 {
						result.TargetRevision = "target-revision-reported-write"
					}
					return result
				}
			},
			category:        UnresolvedOperationRevisionDisagreement,
			sourceOutcome:   SourceRevalidationMatched,
			wantWriteCalls:  1,
			wantVerifyCalls: 1,
		},
		{
			name: "matching revision but mismatching identity",
			configure: func(target *recoveryExecutionTargetFake, _ *recoveryRepositoryContractSource) {
				target.verifyObservation = func(call int, observation TargetVerifyObservation) TargetVerifyObservation {
					if call == 1 && observation.Present != nil {
						observation.Present.IdentityDigest = strings.Repeat("9", 64)
					}
					return observation
				}
			},
			category:        UnresolvedOperationVerificationMismatch,
			sourceOutcome:   SourceRevalidationMatched,
			wantWriteCalls:  1,
			wantVerifyCalls: 1,
		},
		{
			name: "later overwrite mismatch after prior checkpoint",
			configure: func(target *recoveryExecutionTargetFake, _ *recoveryRepositoryContractSource) {
				target.verifyObservation = func(call int, observation TargetVerifyObservation) TargetVerifyObservation {
					if call == 2 && observation.Present != nil {
						observation.Present.IdentityDigest = strings.Repeat("8", 64)
					}
					return observation
				}
			},
			category:         UnresolvedOperationVerificationMismatch,
			sourceOutcome:    SourceRevalidationMatched,
			wantWriteCalls:   2,
			wantVerifyCalls:  2,
			wantSuccessItems: 1,
		},
		{
			name: "skip unchanged-target mismatch",
			configure: func(target *recoveryExecutionTargetFake, _ *recoveryRepositoryContractSource) {
				target.verifyObservation = func(call int, observation TargetVerifyObservation) TargetVerifyObservation {
					if call == 3 && observation.Present != nil {
						observation.Present.IdentityDigest = strings.Repeat("7", 64)
					}
					return observation
				}
			},
			category:         UnresolvedOperationVerificationMismatch,
			sourceOutcome:    SourceRevalidationMatched,
			wantWriteCalls:   2,
			wantVerifyCalls:  3,
			wantSuccessItems: 2,
		},
		{
			name: "target mismatch wins over source drift",
			configure: func(target *recoveryExecutionTargetFake, source *recoveryRepositoryContractSource) {
				target.verifyObservation = func(call int, observation TargetVerifyObservation) TargetVerifyObservation {
					if call == 1 && observation.Present != nil {
						observation.Present.IdentityDigest = strings.Repeat("6", 64)
					}
					return observation
				}
				source.revalidate = func(call int) error {
					if call == 3 {
						return provider.ErrRsyncRestoreSourceDrift
					}
					return nil
				}
			},
			category:        UnresolvedOperationVerificationMismatch,
			sourceOutcome:   SourceRevalidationDrifted,
			wantWriteCalls:  1,
			wantVerifyCalls: 1,
		},
		{
			name: "target mismatch wins over source failure",
			configure: func(target *recoveryExecutionTargetFake, source *recoveryRepositoryContractSource) {
				target.verifyObservation = func(call int, observation TargetVerifyObservation) TargetVerifyObservation {
					if call == 1 && observation.Present != nil {
						observation.Present.IdentityDigest = strings.Repeat("5", 64)
					}
					return observation
				}
				source.revalidate = func(call int) error {
					if call == 3 {
						return errors.New("source unavailable after target observation")
					}
					return nil
				}
			},
			category:        UnresolvedOperationVerificationMismatch,
			sourceOutcome:   SourceRevalidationFailed,
			wantWriteCalls:  1,
			wantVerifyCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryExecutionFixture(t)
			executed, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
			coordinator.target = target
			claim, found, err := coordinator.ClaimNext(context.Background(), "unresolved-"+strings.ReplaceAll(test.name, " ", "-"))
			if err != nil || !found || claim.JobID != executed.JobID {
				t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
			}
			var before model.BackupAssetRecoveryJob
			if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
				t.Fatal(err)
			}
			source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
			test.configure(target, source)

			executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
			if !errors.Is(executeErr, ErrInvalidTargetVerification) ||
				errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
				t.Fatalf("unresolved outcome error=%v, want target verification without fence loss", executeErr)
			}
			if len(target.writes) != test.wantWriteCalls || len(target.verifies) != test.wantVerifyCalls {
				t.Fatalf("target calls writes/verifies=%d/%d, want %d/%d",
					len(target.writes), len(target.verifies), test.wantWriteCalls, test.wantVerifyCalls)
			}
			assertRecoveryUnresolvedOutcomeProjection(
				t, fixture.db, claim, before, test.category, test.sourceOutcome, test.wantSuccessItems,
			)
		})
	}
}

func TestRecoveryExecuteClaimPostArmCallErrorsBecomeUnresolved(t *testing.T) {
	const privateFailure = "private-target-call-failure: items/private-target-locator"
	privateErr := errors.New(privateFailure)

	ordinaryCases := []struct {
		name             string
		configure        func(*recoveryExecutionTargetFake)
		category         UnresolvedOperationCategory
		wantWriteCalls   int
		wantVerifyCalls  int
		wantSuccessItems int64
		wantWriteProduct bool
	}{
		{
			name: "create write error",
			configure: func(target *recoveryExecutionTargetFake) {
				target.writeErr = func(call int) error {
					if call == 1 {
						return privateErr
					}
					return nil
				}
			},
			category: UnresolvedOperationWriteResultInvalid, wantWriteCalls: 1,
		},
		{
			name: "overwrite write error",
			configure: func(target *recoveryExecutionTargetFake) {
				target.writeErr = func(call int) error {
					if call == 2 {
						return privateErr
					}
					return nil
				}
			},
			category: UnresolvedOperationWriteResultInvalid, wantWriteCalls: 2,
			wantVerifyCalls: 1, wantSuccessItems: 1,
		},
		{
			name: "create verify error",
			configure: func(target *recoveryExecutionTargetFake) {
				target.verifyErr = func(call int) error {
					if call == 1 {
						return privateErr
					}
					return nil
				}
			},
			category: UnresolvedOperationObservationInvalid, wantWriteCalls: 1,
			wantVerifyCalls: 1, wantWriteProduct: true,
		},
		{
			name: "overwrite verify error",
			configure: func(target *recoveryExecutionTargetFake) {
				target.verifyErr = func(call int) error {
					if call == 2 {
						return privateErr
					}
					return nil
				}
			},
			category: UnresolvedOperationObservationInvalid, wantWriteCalls: 2,
			wantVerifyCalls: 2, wantSuccessItems: 1, wantWriteProduct: true,
		},
		{
			name: "skip verify error",
			configure: func(target *recoveryExecutionTargetFake) {
				target.verifyErr = func(call int) error {
					if call == 3 {
						return privateErr
					}
					return nil
				}
			},
			category: UnresolvedOperationObservationInvalid, wantWriteCalls: 2,
			wantVerifyCalls: 3, wantSuccessItems: 2,
		},
	}

	for _, test := range ordinaryCases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryExecutionFixture(t)
			executed, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
			test.configure(target)
			coordinator.target = target
			claim, found, err := coordinator.ClaimNext(
				context.Background(), "post-arm-call-error-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			if err != nil || !found || claim.JobID != executed.JobID {
				t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
			}
			var before model.BackupAssetRecoveryJob
			if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
				t.Fatal(err)
			}
			source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)

			executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
			assertRecoveryPostArmCallError(
				t, fixture.db, claim, before, executeErr, privateFailure, test.category,
				test.wantSuccessItems, test.wantWriteProduct,
			)
			if len(target.writes) != test.wantWriteCalls || len(target.verifies) != test.wantVerifyCalls {
				t.Fatalf("post-arm target calls writes/verifies=%d/%d, want %d/%d",
					len(target.writes), len(target.verifies), test.wantWriteCalls, test.wantVerifyCalls)
			}
		})
	}

	deleteCases := []struct {
		name             string
		configure        func(*recoveryExecutionTargetFake, int)
		category         UnresolvedOperationCategory
		wantWriteProduct bool
		wantVerifyDelta  int
	}{
		{
			name: "exact mirror delete error",
			configure: func(target *recoveryExecutionTargetFake, _ int) {
				target.deleteErr = func(call int) error {
					if call == 1 {
						return privateErr
					}
					return nil
				}
			},
			category: UnresolvedOperationWriteResultInvalid,
		},
		{
			name: "exact mirror delete verify error",
			configure: func(target *recoveryExecutionTargetFake, verifiesBefore int) {
				target.verifyErr = func(call int) error {
					if call == verifiesBefore+1 {
						return privateErr
					}
					return nil
				}
			},
			category: UnresolvedOperationObservationInvalid, wantWriteProduct: true, wantVerifyDelta: 1,
		},
	}

	for _, test := range deleteCases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPausedAuthorizedExactMirrorDelete(t, strings.ToUpper(strings.ReplaceAll(test.name, " ", "_")))
			db := fixture.execution.serviceFixture.db
			var before model.BackupAssetRecoveryJob
			if err := db.Where("id = ?", fixture.execution.jobID).Take(&before).Error; err != nil {
				t.Fatal(err)
			}
			verifiesBefore := len(fixture.execution.target.verifies)
			test.configure(fixture.execution.target, verifiesBefore)
			source := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)

			executeErr := fixture.execution.coordinator.ExecuteClaim(
				context.Background(), fixture.execution.claim, source, fixture.request.GrantSecret,
			)
			assertRecoveryPostArmCallError(
				t, db, fixture.execution.claim, before, executeErr, privateFailure, test.category,
				3, test.wantWriteProduct,
			)
			if len(fixture.execution.target.deletes) != 1 ||
				len(fixture.execution.target.verifies)-verifiesBefore != test.wantVerifyDelta {
				t.Fatalf("delete call deltas deletes/verifies=%d/%d, want 1/%d",
					len(fixture.execution.target.deletes),
					len(fixture.execution.target.verifies)-verifiesBefore, test.wantVerifyDelta)
			}
		})
	}
}

func assertRecoveryPostArmCallError(
	t *testing.T,
	db *gorm.DB,
	claim RecoveryWorkerClaim,
	before model.BackupAssetRecoveryJob,
	executeErr error,
	privateFailure string,
	category UnresolvedOperationCategory,
	wantSuccessItems int64,
	wantWriteProduct bool,
) {
	t.Helper()
	if !errors.Is(executeErr, ErrInvalidTargetVerification) ||
		errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("post-arm call error=%v, want ErrInvalidTargetVerification without fence loss", executeErr)
	}
	if strings.Contains(executeErr.Error(), privateFailure) ||
		strings.Contains(executeErr.Error(), "items/private-target-locator") {
		t.Fatalf("post-arm call error leaked private target failure: %v", executeErr)
	}
	assertRecoveryUnresolvedOutcomeProjection(
		t, db, claim, before, category, SourceRevalidationMatched, wantSuccessItems,
	)
	var checkpoint model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", claim.JobID).Order("sequence DESC").Take(&checkpoint).Error; err != nil {
		t.Fatal(err)
	}
	if (checkpoint.WriteResultDigest != "") != wantWriteProduct ||
		(checkpoint.WriteTargetRevision != "") != wantWriteProduct ||
		checkpoint.ObservationDigest != "" || checkpoint.ObservedTargetRevision != "" ||
		checkpoint.ObservedPresence != "" {
		t.Fatalf("post-arm no-observation checkpoint facts=%+v want_write_product=%t", checkpoint, wantWriteProduct)
	}
}

func TestRecoveryExecuteClaimPreservesUnresolvedItemProjectionFacts(t *testing.T) {
	tests := []struct {
		name      string
		category  UnresolvedOperationCategory
		configure func(*recoveryExecutionTargetFake)
	}{
		{
			name:     "write result invalid",
			category: UnresolvedOperationWriteResultInvalid,
			configure: func(target *recoveryExecutionTargetFake) {
				target.writeResult = func(_ int, _ TargetWriteResult) TargetWriteResult {
					return TargetWriteResult{BytesWritten: -1, IdentityDigest: "invalid"}
				}
			},
		},
		{
			name:     "observation invalid",
			category: UnresolvedOperationObservationInvalid,
			configure: func(target *recoveryExecutionTargetFake) {
				target.verifyObservation = func(_ int, _ TargetVerifyObservation) TargetVerifyObservation {
					return TargetVerifyObservation{Kind: TargetPresencePresent}
				}
			},
		},
		{
			name:     "revision disagreement",
			category: UnresolvedOperationRevisionDisagreement,
			configure: func(target *recoveryExecutionTargetFake) {
				target.writeResult = func(_ int, result TargetWriteResult) TargetWriteResult {
					result.TargetRevision = "target-revision-disagreement"
					return result
				}
			},
		},
		{
			name:     "verification mismatch",
			category: UnresolvedOperationVerificationMismatch,
			configure: func(target *recoveryExecutionTargetFake) {
				target.verifyObservation = func(_ int, observation TargetVerifyObservation) TargetVerifyObservation {
					observation.Present.IdentityDigest = strings.Repeat("4", 64)
					return observation
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryExecutionFixture(t)
			executed, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("execute recovery fixture: %v", err)
			}
			var completed model.BackupAssetRecoveryJobItem
			if err := fixture.db.Where("job_id = ? AND ordinal = ?", executed.JobID, 0).Take(&completed).Error; err != nil {
				t.Fatal(err)
			}
			coordinator := newRecoveryWorkerCoordinator(t, fixture)
			target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
			test.configure(target)
			coordinator.target = target
			claim, found, err := coordinator.ClaimNext(context.Background(), "unresolved-neutrality-"+strings.ReplaceAll(test.name, " ", "-"))
			if err != nil || !found || claim.JobID != executed.JobID {
				t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
			}
			primeRecoveryOrdinaryCompletedItem(t, coordinator, claim, completed.ID)
			var before []model.BackupAssetRecoveryJobItem
			if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&before).Error; err != nil {
				t.Fatal(err)
			}
			source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
			executeErr := coordinator.ExecuteClaim(context.Background(), claim, source, "")
			if !errors.Is(executeErr, ErrInvalidTargetVerification) {
				t.Fatalf("unresolved execution error=%v, want ErrInvalidTargetVerification", executeErr)
			}

			var checkpoints []model.BackupAssetRecoveryCheckpoint
			if err := fixture.db.Where("job_id = ?", claim.JobID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
				t.Fatal(err)
			}
			if len(checkpoints) == 0 {
				t.Fatal("unresolved execution did not append a checkpoint")
			}
			unresolved := checkpoints[len(checkpoints)-1]
			if unresolved.Phase != string(CheckpointPhaseOperationUnresolved) ||
				unresolved.UnresolvedCategory != string(test.category) {
				t.Fatalf("unresolved checkpoint=%+v", unresolved)
			}
			var after []model.BackupAssetRecoveryJobItem
			if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&after).Error; err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("job item count changed from %d to %d", len(before), len(after))
			}
			for index := range before {
				if after[index].ID != before[index].ID ||
					after[index].BytesWritten != before[index].BytesWritten ||
					after[index].VerifiedSize != before[index].VerifiedSize ||
					after[index].VerifiedDigest != before[index].VerifiedDigest {
					t.Fatalf("unresolved projection rewrote item facts before=%+v after=%+v", before[index], after[index])
				}
				if before[index].Outcome != "" &&
					(after[index].Outcome != before[index].Outcome || after[index].FailureCategory != before[index].FailureCategory) {
					t.Fatalf("unresolved projection rewrote completed item before=%+v after=%+v", before[index], after[index])
				}
			}
		})
	}
}

func TestRecoveryExecuteClaimPersistsUnresolvedDispositionAfterCallerCancellation(t *testing.T) {
	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	ctx, cancel := context.WithCancel(context.Background())
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	target.verifyObservation = func(_ int, observation TargetVerifyObservation) TargetVerifyObservation {
		observation.Present.IdentityDigest = strings.Repeat("3", 64)
		return observation
	}
	target.afterVerify = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "unresolved-caller-cancellation-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	var before model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	executeErr := coordinator.ExecuteClaim(ctx, claim, source, "")
	if !errors.Is(ctx.Err(), context.Canceled) || !errors.Is(executeErr, ErrInvalidTargetVerification) {
		t.Fatalf("canceled unresolved execution ctx=%v err=%v, want canceled caller and durable target-verification result", ctx.Err(), executeErr)
	}
	if len(target.writes) != 1 || len(target.verifies) != 1 {
		t.Fatalf("canceled unresolved execution target writes/verifies=%d/%d, want 1/1", len(target.writes), len(target.verifies))
	}
	assertRecoveryUnresolvedOutcomeProjection(
		t, fixture.db, claim, before, UnresolvedOperationVerificationMismatch,
		SourceRevalidationMatched, 0,
	)
}

func TestRecoveryExecuteClaimBoundsUnresolvedDispositionAfterCallerCancellation(t *testing.T) {
	const exactProductionBound = 5 * time.Second
	if recoveryUnresolvedProjectionTimeout != exactProductionBound {
		t.Fatalf("unresolved projection timeout=%s, want exact production bound %s",
			recoveryUnresolvedProjectionTimeout, exactProductionBound)
	}

	fixture := newRecoveryExecutionFixture(t)
	executed, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	ctx, cancel := context.WithCancel(context.Background())
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	target.verifyObservation = func(_ int, observation TargetVerifyObservation) TargetVerifyObservation {
		observation.Present.IdentityDigest = strings.Repeat("3", 64)
		return observation
	}
	target.afterVerify = func(call int) {
		if call == 1 {
			cancel()
		}
	}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "bounded-unresolved-cancellation-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim bounded unresolved execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); err != nil {
		t.Fatalf("prepare bounded unresolved first write: %v", err)
	}
	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatalf("open bounded unresolved SQL pool: %v", err)
	}
	anchor, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin bounded unresolved shared-memory database: %v", err)
	}
	t.Cleanup(func() {
		if err := anchor.Close(); err != nil {
			t.Errorf("close bounded unresolved database anchor: %v", err)
		}
	})

	type durableProjection struct {
		job         model.BackupAssetRecoveryJob
		items       []model.BackupAssetRecoveryJobItem
		attempt     model.BackupAssetRecoveryAttempt
		sourceLease model.RecoveryPointLease
		nodeLease   model.BackupAssetRecoveryNodeLease
		checkpoints []model.BackupAssetRecoveryCheckpoint
		evidence    []model.BackupAssetRecoveryEvidence
	}
	captureProjection := func(t *testing.T) durableProjection {
		t.Helper()
		var projection durableProjection
		if err := fixture.db.Where("id = ?", claim.JobID).Take(&projection.job).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&projection.items).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Where("id = ?", claim.AttemptID).Take(&projection.attempt).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Where("id = ?", claim.SourceFence.LeaseID).Take(&projection.sourceLease).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Where("id = ?", claim.NodeLeaseID).Take(&projection.nodeLease).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Where("job_id = ?", claim.JobID).Order("sequence ASC").
			Find(&projection.checkpoints).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Where("job_id = ?", claim.JobID).Order("created_at ASC, id ASC").
			Find(&projection.evidence).Error; err != nil {
			t.Fatal(err)
		}
		return projection
	}
	before := captureProjection(t)
	writesBefore, verifiesBefore := len(target.writes), len(target.verifies)

	type deadlineObservation struct {
		deadline    time.Time
		observedAt  time.Time
		hasDeadline bool
	}
	type expirationObservation struct {
		err       error
		expiredAt time.Time
	}
	deadlineObserved := make(chan deadlineObservation, 1)
	contextExpired := make(chan expirationObservation, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCallback := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(releaseCallback)

	errMissingDeadline := errors.New("unresolved projection transaction has no deadline")
	errReleasedBeforeDeadline := errors.New("unresolved projection callback released before its deadline")
	callbackName := "recovery:bounded-unresolved-projection:" + t.Name()
	if err := fixture.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != (model.BackupAssetRecoveryCheckpoint{}).TableName() {
			return
		}
		checkpoint, ok := tx.Statement.Dest.(*model.BackupAssetRecoveryCheckpoint)
		if !ok || checkpoint.Phase != string(CheckpointPhaseOperationUnresolved) {
			return
		}
		observedAt := time.Now()
		deadline, hasDeadline := tx.Statement.Context.Deadline()
		deadlineObserved <- deadlineObservation{
			deadline: deadline, observedAt: observedAt, hasDeadline: hasDeadline,
		}
		if !hasDeadline {
			<-release
			_ = tx.AddError(errMissingDeadline)
			return
		}
		select {
		case <-tx.Statement.Context.Done():
			contextExpired <- expirationObservation{err: tx.Statement.Context.Err(), expiredAt: time.Now()}
			_ = tx.AddError(tx.Statement.Context.Err())
		case <-release:
			_ = tx.AddError(errReleasedBeforeDeadline)
		}
	}); err != nil {
		t.Fatalf("register bounded unresolved projection callback: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Create().Remove(callbackName); err != nil {
			t.Errorf("remove bounded unresolved projection callback: %v", err)
		}
	})

	source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
	type executionResult struct {
		err        error
		returnedAt time.Time
	}
	result := make(chan executionResult, 1)
	go func() {
		executeErr := coordinator.ExecuteClaim(ctx, claim, source, "")
		result <- executionResult{err: executeErr, returnedAt: time.Now()}
	}()

	var observed deadlineObservation
	select {
	case observed = <-deadlineObserved:
	case completed := <-result:
		t.Fatalf("unresolved execution returned before the transaction callback: %v", completed.err)
	case <-time.After(exactProductionBound):
		releaseCallback()
		t.Fatal("unresolved execution did not reach the transaction callback within the production bound")
	}
	if !observed.hasDeadline {
		releaseCallback()
		completed := <-result
		t.Fatalf("unresolved transaction used an unbounded context: %v", completed.err)
	}
	remaining := observed.deadline.Sub(observed.observedAt)
	if remaining <= 0 || remaining > exactProductionBound {
		releaseCallback()
		completed := <-result
		t.Fatalf("unresolved transaction remaining deadline=%s, want (0, %s]: %v",
			remaining, exactProductionBound, completed.err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		releaseCallback()
		completed := <-result
		t.Fatalf("unresolved transaction callback observed caller context=%v, want canceled: %v",
			ctx.Err(), completed.err)
	}

	returnMargin := 2 * time.Second
	returnTimer := time.NewTimer(time.Until(observed.deadline) + returnMargin)
	defer returnTimer.Stop()
	var completed executionResult
	select {
	case completed = <-result:
	case <-returnTimer.C:
		releaseCallback()
		t.Fatalf("unresolved execution did not return by transaction deadline plus %s", returnMargin)
	}
	if !errors.Is(completed.err, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("expired unresolved projection error=%v, want ErrRecoveryWorkerUnavailable", completed.err)
	}
	expired := <-contextExpired
	if !errors.Is(expired.err, context.DeadlineExceeded) || expired.expiredAt.Before(observed.deadline) ||
		completed.returnedAt.Before(expired.expiredAt) {
		t.Fatalf("unresolved transaction expiration=%v at %s deadline=%s return=%s",
			expired.err, expired.expiredAt, observed.deadline, completed.returnedAt)
	}
	if len(target.writes)-writesBefore != 1 || len(target.verifies)-verifiesBefore != 1 {
		t.Fatalf("bounded unresolved execution target write/verify deltas=%d/%d, want 1/1",
			len(target.writes)-writesBefore, len(target.verifies)-verifiesBefore)
	}

	after := captureProjection(t)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("expired unresolved projection did not roll back complete durable state:\nbefore=%+v\nafter=%+v",
			before, after)
	}
	var unresolvedCount, failureEvidenceCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND phase = ?", claim.JobID, CheckpointPhaseOperationUnresolved).
		Count(&unresolvedCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("job_id = ? AND kind = ?", claim.JobID, "failure").
		Count(&failureEvidenceCount).Error; err != nil {
		t.Fatal(err)
	}
	if unresolvedCount != 0 || failureEvidenceCount != 0 {
		t.Fatalf("expired unresolved projection persisted checkpoints=%d failure_evidence=%d",
			unresolvedCount, failureEvidenceCount)
	}
}

func corruptRecoveryExecutionTargetState(target *recoveryExecutionTargetFake) {
	for object := range target.states {
		target.states[object] = recoveryExecutionTargetState{
			kind: TargetPresencePresent, identityDigest: strings.Repeat("e", 64), bytes: 1,
			revision: "target-revision-external-change",
		}
	}
}

func assertRecoveryPostArmMismatchProjection(
	t *testing.T,
	db *gorm.DB,
	claim RecoveryWorkerClaim,
	before recoveryPendingOperationMismatchState,
) {
	t.Helper()
	assertRecoveryUnresolvedOutcomeProjection(
		t, db, claim, before.job, UnresolvedOperationRevisionDisagreement,
		SourceRevalidationMatched, 0,
	)
}

func assertRecoveryUnresolvedOutcomeProjection(
	t *testing.T,
	db *gorm.DB,
	claim RecoveryWorkerClaim,
	before model.BackupAssetRecoveryJob,
	category UnresolvedOperationCategory,
	sourceOutcome SourceRevalidationOutcome,
	wantSuccessItems int64,
) {
	t.Helper()
	var plan model.BackupAssetRecoveryPlan
	if err := db.Where("id = ?", before.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", claim.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	var source model.RecoveryPointLease
	if err := db.Where("id = ?", claim.SourceFence.LeaseID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	var node model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", claim.NodeLeaseID).Take(&node).Error; err != nil {
		t.Fatal(err)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", claim.JobID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) < 2 {
		t.Fatalf("unresolved outcome checkpoints=%+v", checkpoints)
	}
	unresolved := checkpoints[len(checkpoints)-1]
	priorRevision := before.TargetChainRevision
	for _, checkpoint := range checkpoints[:len(checkpoints)-1] {
		if checkpoint.Phase == string(CheckpointPhaseOperation) {
			priorRevision = checkpoint.NextTargetRevision
		}
	}
	if unresolved.Phase != string(CheckpointPhaseOperationUnresolved) ||
		unresolved.JobItemID == "" || unresolved.AttemptID != claim.AttemptID ||
		unresolved.OperationDigest == "" || unresolved.PriorTargetRevision != priorRevision ||
		unresolved.NextTargetRevision != "" || unresolved.UnresolvedCategory != string(category) ||
		unresolved.SourceRevalidationOutcome != string(sourceOutcome) ||
		unresolved.AttemptFence != claim.AttemptFence || unresolved.NodeFence != claim.NodeFence {
		t.Fatalf("unresolved checkpoint=%+v prior=%q", unresolved, priorRevision)
	}
	if unresolved.WriteResultDigest == "" && unresolved.WriteTargetRevision != "" {
		t.Fatalf("unresolved checkpoint fabricated a write revision without a result: %+v", unresolved)
	}
	if unresolved.ObservationDigest == "" &&
		(unresolved.ObservedTargetRevision != "" || unresolved.ObservedPresence != "") {
		t.Fatalf("unresolved checkpoint fabricated observation facts without a result: %+v", unresolved)
	}
	if category == UnresolvedOperationWriteResultInvalid && unresolved.ObservationDigest != "" {
		t.Fatalf("write-result-invalid checkpoint fabricated an observation: %+v", unresolved)
	}
	if category == UnresolvedOperationRevisionDisagreement &&
		(unresolved.WriteResultDigest == "" || unresolved.ObservationDigest == "") {
		t.Fatalf("revision-disagreement checkpoint omitted a returned product: %+v", unresolved)
	}
	if category == UnresolvedOperationVerificationMismatch && unresolved.ObservationDigest == "" {
		t.Fatalf("verification-mismatch checkpoint omitted a returned observation: %+v", unresolved)
	}

	var item model.BackupAssetRecoveryJobItem
	if err := db.Where("id = ? AND job_id = ?", unresolved.JobItemID, claim.JobID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	wantWorkspacePhase := string(WorkspacePhaseNone)
	if TargetMode(before.TargetMode) == TargetModeIsolated {
		wantWorkspacePhase = string(WorkspacePhaseCleanupDue)
	}
	if PlanState(plan.State) != PlanStateExecuted || job.State != string(JobStateNeedsAttention) ||
		job.FailureCategory != "remote_outcome_unresolved" ||
		job.TransitionRevision != before.TransitionRevision+1 ||
		job.WorkspacePhase != wantWorkspacePhase || job.TargetChainRevision != priorRevision ||
		item.Outcome != "failed" || item.FailureCategory != "remote_outcome_unresolved" {
		t.Fatalf("unresolved projection plan/job/item=%+v/%+v/%+v", plan, job, item)
	}
	if attempt.State != string(AttemptStateFailed) || !attempt.MutationArmed || attempt.ClosedAt == nil ||
		source.Status != string(backupasset.LeaseReleased) || source.ReleasedAt == nil ||
		node.State != "released" || node.ReleasedAt == nil {
		t.Fatalf("unresolved projection retained authority attempt/source/node=%+v/%+v/%+v", attempt, source, node)
	}
	var evidence []model.BackupAssetRecoveryEvidence
	if err := db.Where("job_id = ? AND kind = ?", claim.JobID, "failure").Find(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Outcome != "needs_attention" ||
		evidence[0].CheckpointID == nil || *evidence[0].CheckpointID != unresolved.ID ||
		evidence[0].DifferenceCount != 0 || evidence[0].VerifiedAt == nil {
		t.Fatalf("unresolved failure evidence=%+v", evidence)
	}
	var succeeded int64
	if err := db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND outcome IN ?", claim.JobID, []string{"succeeded", "skipped"}).
		Count(&succeeded).Error; err != nil {
		t.Fatal(err)
	}
	if succeeded != wantSuccessItems {
		t.Fatalf("unresolved projection successful/skipped items=%d, want %d", succeeded, wantSuccessItems)
	}
}

type exactMirrorOrdinaryExecutionFixture struct {
	serviceFixture *authorizationReceiptServiceFixture
	coordinator    *WorkerCoordinator
	claim          RecoveryWorkerClaim
	target         *recoveryExecutionTargetFake
	jobID          string
}

func TestRecoveryDeleteObservationIssuanceUsesExactLockedTargetSessionBinding(t *testing.T) {
	paused := newPausedAuthorizedExactMirrorDelete(t, "verify-issuance")
	fixture := paused.execution
	if len(fixture.target.lstatPermits) != 1 || len(fixture.target.lstats) != 1 {
		t.Fatalf("delete-pause lstat permits/requests=%d/%d, want 1/1",
			len(fixture.target.lstatPermits), len(fixture.target.lstats))
	}

	resumeSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
	if err := fixture.coordinator.ExecuteClaim(
		context.Background(), fixture.claim, resumeSource, paused.request.GrantSecret,
	); err != nil {
		t.Fatalf("execute authorized exact-mirror delete: %v", err)
	}
	if len(fixture.target.lstatPermits) < 2 ||
		len(fixture.target.lstatPermits) != len(fixture.target.lstats) {
		t.Fatalf("delete observation lstat permits/requests=%d/%d, want both issuance paths",
			len(fixture.target.lstatPermits), len(fixture.target.lstats))
	}
	var job model.BackupAssetRecoveryJob
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.serviceFixture.db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	want, err := newRecoveryTargetSessionBinding(plan)
	if err != nil {
		t.Fatalf("derive delete observation session binding: %v", err)
	}
	for index := range fixture.target.lstatPermits {
		assertRecoveryTargetVerifyPermitProof(
			t, fixture.target.lstatPermits[index], fixture.target.lstats[index].Object,
			want, fixture.jobID, TargetMode(job.TargetMode), fixture.serviceFixture.now,
		)
		proof := fixture.target.lstatPermits[index].permit.proof
		if proof.operation != RecoveryOperationDelete || proof.expectedPrior.Kind != ExpectedTargetPresent ||
			!validDigest(proof.expectedPrior.Digest) {
			t.Fatalf("delete observation proof operation=%q prior=%+v, want exact delete/present authority",
				proof.operation, proof.expectedPrior)
		}
	}
}

func newExactMirrorOrdinaryExecutionFixture(t *testing.T) exactMirrorOrdinaryExecutionFixture {
	t.Helper()
	return newExactMirrorOrdinaryExecutionFixtureFromService(t, newExactMirrorExecutionServiceFixture(t))
}

func newInPlaceOrdinaryExecutionFixtureWithKinds(
	t *testing.T,
	kinds []RecoveryOperationKind,
	includeDelete bool,
) exactMirrorOrdinaryExecutionFixture {
	t.Helper()
	return newInPlaceOrdinaryExecutionFixtureFromServiceWithKinds(
		t, newExactMirrorExecutionServiceFixture(t), kinds, includeDelete,
	)
}

func newInPlaceOrdinaryExecutionFixtureFromServiceWithKinds(
	t *testing.T,
	fixture *authorizationReceiptServiceFixture,
	kinds []RecoveryOperationKind,
	includeDelete bool,
) exactMirrorOrdinaryExecutionFixture {
	t.Helper()
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var preflight model.BackupAssetRecoveryPreflight
	if err := fixture.db.Where("id = ?", fixture.request.PreflightID).Take(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	operations, err := decodeRecoveryOperationRows(preflight.EncryptedOperationRows)
	if err != nil {
		t.Fatalf("decode configurable in-place operations: %v", err)
	}
	nonDelete := make([]RecoveryOperation, 0, len(operations))
	deleteOperations := make([]RecoveryOperation, 0, 1)
	for _, operation := range operations {
		if operation.Kind == RecoveryOperationDelete {
			deleteOperations = append(deleteOperations, operation)
			continue
		}
		nonDelete = append(nonDelete, operation)
	}
	if len(nonDelete) != len(kinds) || len(deleteOperations) != 1 {
		t.Fatalf("configurable in-place operations non-delete/delete=%d/%d, want %d/1",
			len(nonDelete), len(deleteOperations), len(kinds))
	}
	for index := range nonDelete {
		operation := &nonDelete[index]
		if operation.Source.AssetRef == nil {
			t.Fatalf("configurable operation %d has no source", index)
		}
		var entry model.CatalogEntry
		if err := fixture.db.Where(
			"generation_id = ? AND recovery_point_id = ? AND entry_id = ?",
			plan.CatalogGenerationID, operation.Source.AssetRef.RecoveryPointID,
			operation.Source.AssetRef.EntryID,
		).Take(&entry).Error; err != nil {
			t.Fatal(err)
		}
		operation.Kind = kinds[index]
		postDigest := recoveryExecutionPayloadDigest(entry.Size)
		switch operation.Kind {
		case RecoveryOperationCreate:
			operation.ExpectedPrior = ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}
			operation.ExpectedPriorBytes = -1
			operation.ExpectedPostIdentityDigest = postDigest
			operation.ExpectedPostBytes = entry.Size
		case RecoveryOperationOverwrite:
			operation.ExpectedPrior = ExpectedTargetIdentity{
				Kind: ExpectedTargetPresent,
				Digest: framedDigest(
					"xirang/recovery/test-configured-overwrite-prior/v1",
					operation.Source.AssetRef.RecoveryPointID,
					operation.Source.AssetRef.EntryID,
				),
			}
			operation.ExpectedPriorBytes = entry.Size
			operation.ExpectedPostIdentityDigest = postDigest
			operation.ExpectedPostBytes = entry.Size
		case RecoveryOperationSkip:
			operation.ExpectedPrior = ExpectedTargetIdentity{
				Kind: ExpectedTargetPresent,
				Digest: framedDigest(
					"xirang/recovery/test-configured-skip-prior/v1",
					operation.Source.AssetRef.RecoveryPointID,
					operation.Source.AssetRef.EntryID,
				),
			}
			operation.ExpectedPriorBytes = entry.Size
			operation.ExpectedPostIdentityDigest = operation.ExpectedPrior.Digest
			operation.ExpectedPostBytes = -1
		default:
			t.Fatalf("unsupported configurable operation kind %q", operation.Kind)
		}
	}
	operations = nonDelete
	conflictPolicy := ConflictOverwriteSelected
	if includeDelete {
		operations = append(operations, deleteOperations...)
		conflictPolicy = ConflictExactMirror
	}
	for index := range operations {
		operations[index].snapshotTargetMode = ""
		operations[index].snapshotConflictPolicy = ""
	}
	var maxBytes int64
	for _, operation := range operations {
		maxBytes += operation.EstimatedBytes
	}
	products, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetModeInPlace, ConflictPolicy: conflictPolicy, Operations: operations,
		Limits: RecoveryOperationLimits{
			MaxRows: len(operations), MaxItems: len(operations), MaxBytes: maxBytes,
			MaxImpactRows: len(operations),
		},
	})
	if err != nil {
		t.Fatalf("rebuild configurable in-place products: %v", err)
	}
	encoded, err := encodeRecoveryOperationRows(products.Rows)
	if err != nil {
		t.Fatalf("encode configurable in-place products: %v", err)
	}
	updated := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).Where("id = ?", plan.ID).
		Updates(map[string]any{
			"conflict_policy": conflictPolicy, "operation_set_digest": products.OperationSetDigest,
			"delete_set_digest": products.DeleteSetDigest,
			"estimated_items":   products.Impact.EstimatedItems,
			"estimated_bytes":   products.Impact.EstimatedBytes,
		})
	if updated.Error != nil || updated.RowsAffected != 1 {
		t.Fatalf("update configurable in-place plan err=%v rows=%d", updated.Error, updated.RowsAffected)
	}
	preflight.OperationSetDigest = products.OperationSetDigest
	preflight.DeleteSetDigest = products.DeleteSetDigest
	preflight.EncryptedOperationRows = encoded
	preflight.EstimatedItems = products.Impact.EstimatedItems
	preflight.EstimatedBytes = products.Impact.EstimatedBytes
	if err := fixture.db.Save(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	return newExactMirrorOrdinaryExecutionFixtureFromService(t, fixture)
}

func newExactMirrorOrdinaryExecutionMigrationFixture(t *testing.T) exactMirrorOrdinaryExecutionFixture {
	t.Helper()
	fixture := newAuthorizationReceiptSQLiteMigrationServiceFixture(
		t, AuthorizationReceiptWriteAuthorize, true, false,
	)
	return newExactMirrorOrdinaryExecutionFixtureFromService(t, fixture)
}

func newExactMirrorOrdinaryExecutionMultiDeleteMigrationFixture(
	t *testing.T,
) exactMirrorOrdinaryExecutionFixture {
	t.Helper()
	fixture := newAuthorizationReceiptSQLiteMigrationServiceFixtureWithDeleteCount(
		t, AuthorizationReceiptWriteAuthorize, true, false, 2,
	)
	return newExactMirrorOrdinaryExecutionFixtureFromService(t, fixture)
}

func newExactMirrorOrdinaryExecutionMultiDeletePostgresMigrationFixture(
	t *testing.T,
) exactMirrorOrdinaryExecutionFixture {
	t.Helper()
	fixture := newAuthorizationReceiptPostgresMigrationServiceFixtureWithDeleteCount(
		t, AuthorizationReceiptWriteAuthorize, true, false, 2,
	)
	return newExactMirrorOrdinaryExecutionFixtureFromService(t, fixture)
}

func newExactMirrorOrdinaryExecutionPostgresMigrationFixture(
	t *testing.T,
) exactMirrorOrdinaryExecutionFixture {
	t.Helper()
	fixture := newAuthorizationReceiptPostgresMigrationServiceFixtureWithDeleteCountAndKinds(
		t, AuthorizationReceiptWriteAuthorize, true, false, 1,
		[]RecoveryOperationKind{RecoveryOperationOverwrite},
	)
	return newExactMirrorOrdinaryExecutionFixtureFromService(t, fixture)
}

func newExactMirrorOrdinaryExecutionFixtureFromService(
	t *testing.T,
	fixture *authorizationReceiptServiceFixture,
) exactMirrorOrdinaryExecutionFixture {
	t.Helper()
	write := fixture.request
	writeResult, err := fixture.service.Authorize(context.Background(), write)
	if err != nil {
		t.Fatalf("authorize exact-mirror write: %v", err)
	}

	execute := write
	execute.Operation = AuthorizationReceiptExecute
	execute.Category = AuthorizationReceiptCategoryExecute
	execute.Endpoint = recoveryExecuteEndpoint
	execute.IdempotencyKey = "exact-mirror-ordinary-execute-key"
	execute.Proof.JTI = "FAKE_RECOVERY_EXACT_MIRROR_EXECUTE_PROOF_JTI"
	execute.Reason = ""
	execute.ExpectedPlanRevision = writeResult.PlanTransitionRevision
	execute.GrantID = writeResult.GrantID
	executed, err := fixture.service.Authorize(context.Background(), execute)
	if err != nil {
		t.Fatalf("execute exact-mirror plan: %v", err)
	}

	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
	coordinator.target = target
	claim, found, err := coordinator.ClaimNext(context.Background(), "exact-mirror-ordinary-worker")
	if err != nil || !found || claim.JobID != executed.JobID {
		t.Fatalf("claim exact-mirror execution: claim=%+v found=%t err=%v", claim, found, err)
	}
	return exactMirrorOrdinaryExecutionFixture{
		serviceFixture: fixture,
		coordinator:    coordinator,
		claim:          claim,
		target:         target,
		jobID:          executed.JobID,
	}
}

type pausedAuthorizedExactMirrorDelete struct {
	execution   exactMirrorOrdinaryExecutionFixture
	request     RecoveryAuthorizationRequest
	result      RecoveryAuthorizationResult
	job         model.BackupAssetRecoveryJob
	checkpoints []model.BackupAssetRecoveryCheckpoint
}

func newPausedAuthorizedExactMirrorDelete(
	t *testing.T,
	suffix string,
) pausedAuthorizedExactMirrorDelete {
	t.Helper()
	return newPausedAuthorizedExactMirrorDeleteFromExecution(
		t, suffix, newExactMirrorOrdinaryExecutionFixture(t),
	)
}

func newPausedAuthorizedExactMirrorDeleteMigration(
	t *testing.T,
	suffix string,
) pausedAuthorizedExactMirrorDelete {
	t.Helper()
	return newPausedAuthorizedExactMirrorDeleteFromExecution(
		t, suffix, newExactMirrorOrdinaryExecutionMigrationFixture(t),
	)
}

func newPausedAuthorizedExactMirrorDeleteFromExecution(
	t *testing.T,
	suffix string,
	execution exactMirrorOrdinaryExecutionFixture,
) pausedAuthorizedExactMirrorDelete {
	t.Helper()
	pauseSource := newRecoveryRepositoryContractSource(t, execution.serviceFixture.db, execution.jobID)
	if err := execution.coordinator.ExecuteClaim(context.Background(), execution.claim, pauseSource, ""); err != nil {
		t.Fatalf("pause exact-mirror execution: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	if err := execution.serviceFixture.db.Where("id = ?", execution.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := execution.serviceFixture.db.Where("job_id = ?", execution.jobID).
		Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) == 0 ||
		checkpoints[len(checkpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
		t.Fatalf("delete authority was not durably paused: %+v", checkpoints)
	}
	required := checkpoints[len(checkpoints)-1]
	request := execution.serviceFixture.request
	request.Operation = AuthorizationReceiptDeleteAuthorize
	request.Category = AuthorizationReceiptCategoryExactMirrorDelete
	request.Endpoint = recoveryDeleteAuthorizationEndpoint
	request.IdempotencyKey = "exact-mirror-paused-delete-" + suffix
	request.Proof.JTI = "FAKE_RECOVERY_PAUSED_DELETE_PROOF_" + suffix
	request.ExpectedPlanRevision = jobPlanTransitionRevision(t, execution.serviceFixture.db, job.PlanID)
	request.PreflightID = ""
	request.JobID = execution.jobID
	request.CheckpointID = required.ID
	request.AttemptID = execution.claim.AttemptID
	request.GrantID = ""
	request.Reason = "FAKE_RECOVERY_PAUSED_DELETE_REASON_" + suffix
	request.GrantSecret = mustAuthorizationReceiptSecretForFixture()
	result, err := execution.serviceFixture.service.Authorize(context.Background(), request)
	if err != nil {
		t.Fatalf("authorize exact-mirror delete: %v", err)
	}
	return pausedAuthorizedExactMirrorDelete{
		execution: execution, request: request, result: result, job: job, checkpoints: checkpoints,
	}
}

func TestRecoveryExactMirrorDeletePauseRequiresOrdinaryExecutionHistory(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionFixture(t)
	source := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, source, ""); err != nil {
		t.Fatalf("pause exact-mirror execution: %v", err)
	}

	var plan model.BackupAssetRecoveryPlan
	var job model.BackupAssetRecoveryJob
	if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.serviceFixture.db.Where("id = ?", job.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != 4 || checkpoints[0].Phase != string(CheckpointPhaseOperation) ||
		checkpoints[1].Phase != string(CheckpointPhaseOperation) ||
		checkpoints[2].Phase != string(CheckpointPhaseOperation) ||
		checkpoints[2].NextTargetRevision != checkpoints[2].PriorTargetRevision ||
		checkpoints[3].Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
		t.Fatalf("exact-mirror pause checkpoints=%+v, want three item-bound operations then delete authority", checkpoints)
	}
	required := checkpoints[3]
	if required.Sequence != 3 || required.AttemptID != fixture.claim.AttemptID ||
		required.OperationDigest != job.DeleteSetDigest || required.PriorTargetRevision != job.TargetChainRevision ||
		required.NextTargetRevision != "" || required.DeleteNodeRevision != job.PreflightNodeRevision ||
		required.DeleteRootRevision != plan.RootRevision || required.AttemptFence != fixture.claim.AttemptFence ||
		required.NodeFence != fixture.claim.NodeFence || required.DeleteAuthorityExpiresAt == nil ||
		!required.DeleteAuthorityExpiresAt.After(fixture.serviceFixture.now) {
		t.Fatalf("exact-mirror delete pause=%+v job=%+v plan=%+v", required, job, plan)
	}

	var attempt model.BackupAssetRecoveryAttempt
	var sourceLease model.RecoveryPointLease
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := fixture.serviceFixture.db.Where("id = ?", fixture.claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.serviceFixture.db.Where("id = ?", fixture.claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.serviceFixture.db.Where("id = ?", fixture.claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateRunning) || attempt.State != string(AttemptStateRunning) ||
		sourceLease.Status != string(backupasset.LeaseActive) || nodeLease.State != "active" ||
		len(fixture.target.writes) != 2 || len(fixture.target.deletes) != 0 {
		t.Fatalf("paused exact-mirror state job/attempt/source/node=%s/%s/%s/%s writes/deletes=%d/%d",
			job.State, attempt.State, sourceLease.Status, nodeLease.State,
			len(fixture.target.writes), len(fixture.target.deletes))
	}

	var items []model.BackupAssetRecoveryJobItem
	if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if RecoveryOperationKind(item.OperationKind) == RecoveryOperationDelete {
			if item.Outcome != "" || item.FailureCategory != "" {
				t.Fatalf("paused delete item was terminalized: %+v", item)
			}
			continue
		}
		if item.Outcome != "succeeded" && item.Outcome != "skipped" {
			t.Fatalf("ordinary exact-mirror item did not complete before pause: %+v", item)
		}
	}
}

func TestRecoveryExactMirrorSuccessfulDeleteProjectsAbsenceCheckpointAndChain(t *testing.T) {
	assertRecoveryExactMirrorSuccessfulDeleteProjectsAbsenceCheckpointAndChain(
		t, newExactMirrorOrdinaryExecutionFixture(t),
	)
}

func TestRecoveryExactMirrorSuccessfulDeleteProjectionHonorsProductionSQLite069(t *testing.T) {
	assertRecoveryExactMirrorSuccessfulDeleteProjectsAbsenceCheckpointAndChain(
		t, newExactMirrorOrdinaryExecutionMigrationFixture(t),
	)
}

func assertRecoveryExactMirrorSuccessfulDeleteProjectsAbsenceCheckpointAndChain(
	t *testing.T,
	fixture exactMirrorOrdinaryExecutionFixture,
) {
	t.Helper()
	pauseSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, pauseSource, ""); err != nil {
		t.Fatalf("pause exact-mirror execution: %v", err)
	}

	var pausedJob model.BackupAssetRecoveryJob
	if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&pausedJob).Error; err != nil {
		t.Fatal(err)
	}
	var pausedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&pausedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(pausedCheckpoints) == 0 ||
		pausedCheckpoints[len(pausedCheckpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
		t.Fatalf("delete authority was not durably paused: %+v", pausedCheckpoints)
	}
	required := pausedCheckpoints[len(pausedCheckpoints)-1]
	priorRevision := pausedJob.TargetChainRevision

	deleteRequest := fixture.serviceFixture.request
	deleteRequest.Operation = AuthorizationReceiptDeleteAuthorize
	deleteRequest.Category = AuthorizationReceiptCategoryExactMirrorDelete
	deleteRequest.Endpoint = recoveryDeleteAuthorizationEndpoint
	deleteRequest.IdempotencyKey = "exact-mirror-successful-delete-projection-key"
	deleteRequest.Proof.JTI = "FAKE_RECOVERY_SUCCESSFUL_DELETE_PROJECTION_PROOF_JTI"
	deleteRequest.ExpectedPlanRevision = jobPlanTransitionRevision(
		t, fixture.serviceFixture.db, pausedJob.PlanID,
	)
	deleteRequest.PreflightID = ""
	deleteRequest.JobID = fixture.jobID
	deleteRequest.CheckpointID = required.ID
	deleteRequest.AttemptID = fixture.claim.AttemptID
	deleteRequest.GrantID = ""
	deleteRequest.Reason = "FAKE_RECOVERY_SUCCESSFUL_DELETE_PROJECTION_REASON"
	deleteRequest.GrantSecret = mustAuthorizationReceiptSecretForFixture()
	if _, err := fixture.serviceFixture.service.Authorize(context.Background(), deleteRequest); err != nil {
		t.Fatalf("authorize exact-mirror delete: %v", err)
	}

	resumeSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
	if err := fixture.coordinator.ExecuteClaim(
		context.Background(), fixture.claim, resumeSource, deleteRequest.GrantSecret,
	); err != nil {
		t.Fatalf("execute exact-mirror delete after %d destructive call(s): %v", len(fixture.target.deletes), err)
	}
	if len(fixture.target.deletes) != 1 {
		t.Fatalf("successful exact-mirror delete calls=%d, want exactly 1", len(fixture.target.deletes))
	}

	var deleted model.BackupAssetRecoveryJobItem
	if err := fixture.serviceFixture.db.Where(
		"job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationDelete,
	).Take(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != len(pausedCheckpoints)+2 {
		t.Fatalf("successful delete checkpoints=%+v, want consumed authority and operation", checkpoints)
	}
	consumed := checkpoints[len(checkpoints)-2]
	operation := checkpoints[len(checkpoints)-1]
	if consumed.Phase != string(CheckpointPhaseDeleteAuthorityConsumed) ||
		operation.Phase != string(CheckpointPhaseOperation) || operation.Sequence != consumed.Sequence+1 {
		t.Fatalf("successful delete checkpoint tail consumed=%+v operation=%+v", consumed, operation)
	}

	var completedJob model.BackupAssetRecoveryJob
	if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&completedJob).Error; err != nil {
		t.Fatal(err)
	}
	wantTargetRevision := "target-revision-delete"
	wantChainRevision := framedDigest(
		"xirang/recovery/target-absence-chain/v1",
		priorRevision,
		recoveryJobItemOperationDigest(deleted),
		deleted.ID,
		completedJob.SourceRevisionDigest,
		fixture.claim.AttemptID,
		strconv.FormatUint(fixture.claim.AttemptFence, 10),
		strconv.FormatUint(fixture.claim.NodeFence, 10),
		string(TargetAbsenceEvidenceExact),
		wantTargetRevision,
	)
	if operation.JobItemID != deleted.ID || operation.UnresolvedCategory != "" ||
		operation.WriteResultDigest != "" || operation.WriteTargetRevision != "" ||
		operation.ObservationDigest != "" || operation.ObservedTargetRevision != "" ||
		operation.ObservedPresence != "" || operation.SourceRevalidationOutcome != "" ||
		operation.AuthorityCategory != string(AuthorityWrite) ||
		operation.OperationDigest != recoveryJobItemOperationDigest(deleted) ||
		operation.PriorTargetRevision != priorRevision || operation.NextTargetRevision != wantChainRevision ||
		operation.AttemptID != fixture.claim.AttemptID || operation.AttemptFence != fixture.claim.AttemptFence ||
		operation.NodeFence != fixture.claim.NodeFence || completedJob.TargetChainRevision != wantChainRevision ||
		completedJob.TargetChainRevision == priorRevision {
		t.Fatalf("successful delete operation=%+v deleted=%+v job=%+v prior_revision=%q want_chain=%q",
			operation, deleted, completedJob, priorRevision, wantChainRevision)
	}
}

func TestRecoveryExactMirrorMultipleDeletesReuseConsumedSetAuthorityInSameExecution(t *testing.T) {
	assertRecoveryExactMirrorMultipleDeletesReuseConsumedSetAuthorityInSameExecution(
		t, newExactMirrorOrdinaryExecutionMultiDeleteMigrationFixture(t),
	)
}

func testRecoveryExactMirrorMultipleDeletesProductionPostgres069(t *testing.T) {
	assertRecoveryExactMirrorMultipleDeletesReuseConsumedSetAuthorityInSameExecution(
		t, newExactMirrorOrdinaryExecutionMultiDeletePostgresMigrationFixture(t),
	)
}

func assertRecoveryExactMirrorMultipleDeletesReuseConsumedSetAuthorityInSameExecution(
	t *testing.T,
	fixture exactMirrorOrdinaryExecutionFixture,
) {
	t.Helper()
	db := fixture.serviceFixture.db
	pauseSource := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, pauseSource, ""); err != nil {
		t.Fatalf("pause same-execution multi-delete: %v", err)
	}

	var pausedJob model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.jobID).Take(&pausedJob).Error; err != nil {
		t.Fatal(err)
	}
	var pausedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&pausedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(pausedCheckpoints) == 0 ||
		pausedCheckpoints[len(pausedCheckpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
		t.Fatalf("same-execution delete set was not durably paused: %+v", pausedCheckpoints)
	}
	required := pausedCheckpoints[len(pausedCheckpoints)-1]
	deleteRequest := fixture.serviceFixture.request
	deleteRequest.Operation = AuthorizationReceiptDeleteAuthorize
	deleteRequest.Category = AuthorizationReceiptCategoryExactMirrorDelete
	deleteRequest.Endpoint = recoveryDeleteAuthorizationEndpoint
	deleteRequest.IdempotencyKey = "exact-mirror-same-execution-delete-set-key"
	deleteRequest.Proof.JTI = "FAKE_RECOVERY_SAME_EXECUTION_DELETE_SET_PROOF_JTI"
	deleteRequest.ExpectedPlanRevision = jobPlanTransitionRevision(t, db, pausedJob.PlanID)
	deleteRequest.PreflightID = ""
	deleteRequest.JobID = fixture.jobID
	deleteRequest.CheckpointID = required.ID
	deleteRequest.AttemptID = fixture.claim.AttemptID
	deleteRequest.GrantID = ""
	deleteRequest.Reason = "FAKE_RECOVERY_SAME_EXECUTION_DELETE_SET_REASON"
	deleteRequest.GrantSecret = mustAuthorizationReceiptSecretForFixture()
	issued, err := fixture.serviceFixture.service.Authorize(context.Background(), deleteRequest)
	if err != nil {
		t.Fatalf("authorize same-execution delete set: %v", err)
	}

	resumeSource := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
	if err := fixture.coordinator.ExecuteClaim(
		context.Background(), fixture.claim, resumeSource, deleteRequest.GrantSecret,
	); err != nil {
		t.Fatalf("execute two deletes under one consumed set grant after %d destructive call(s): %v",
			len(fixture.target.deletes), err)
	}

	var completedJob model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.jobID).Take(&completedJob).Error; err != nil {
		t.Fatal(err)
	}
	var deleteItems []model.BackupAssetRecoveryJobItem
	if err := db.Where(
		"job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationDelete,
	).Order("ordinal ASC").Find(&deleteItems).Error; err != nil {
		t.Fatal(err)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.jobID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	deleteCalls := make(map[string]int, len(fixture.target.deletes))
	for _, call := range fixture.target.deletes {
		deleteCalls[call.TargetPathDigest]++
	}
	if len(deleteItems) != 2 || len(fixture.target.deletes) != 2 {
		t.Fatalf("same-execution multi-delete items/calls=%d/%d, want 2/2",
			len(deleteItems), len(fixture.target.deletes))
	}
	if len(checkpoints) != len(pausedCheckpoints)+3 {
		t.Fatalf("same-execution multi-delete checkpoints=%d, want paused history plus consumption and two operations (%d)",
			len(checkpoints), len(pausedCheckpoints)+3)
	}
	consumed := checkpoints[len(pausedCheckpoints)]
	chainRevision := pausedJob.TargetChainRevision
	deleteDigests := make(map[string]struct{}, len(deleteItems))
	for index, item := range deleteItems {
		operationDigest := recoveryJobItemOperationDigest(item)
		checkpoint := checkpoints[len(pausedCheckpoints)+1+index]
		wantNextRevision := framedDigest(
			"xirang/recovery/target-absence-chain/v1",
			chainRevision,
			operationDigest,
			item.ID,
			completedJob.SourceRevisionDigest,
			fixture.claim.AttemptID,
			strconv.FormatUint(fixture.claim.AttemptFence, 10),
			strconv.FormatUint(fixture.claim.NodeFence, 10),
			string(TargetAbsenceEvidenceExact),
			"target-revision-delete",
		)
		if item.Outcome != "succeeded" || item.FailureCategory != "" ||
			deleteCalls[item.TargetObjectDigest] != 1 ||
			fixture.target.deletes[index].TargetPathDigest != item.TargetObjectDigest ||
			checkpoint.Phase != string(CheckpointPhaseOperation) ||
			checkpoint.Sequence != consumed.Sequence+1+index ||
			checkpoint.JobItemID != item.ID || checkpoint.UnresolvedCategory != "" ||
			checkpoint.ObservationDigest != "" || checkpoint.ObservedTargetRevision != "" ||
			checkpoint.ObservedPresence != "" || checkpoint.SourceRevalidationOutcome != "" ||
			checkpoint.OperationDigest != operationDigest ||
			checkpoint.PriorTargetRevision != chainRevision ||
			checkpoint.NextTargetRevision != wantNextRevision {
			t.Fatalf("same-execution delete %d item=%+v checkpoint=%+v call=%+v want_next=%q",
				index, item, checkpoint, fixture.target.deletes[index], wantNextRevision)
		}
		deleteDigests[operationDigest] = struct{}{}
		chainRevision = wantNextRevision
	}
	consumedCount := 0
	deleteCheckpointCount := 0
	for _, checkpoint := range checkpoints {
		if CheckpointPhase(checkpoint.Phase) == CheckpointPhaseDeleteAuthorityConsumed {
			consumedCount++
		}
		if CheckpointPhase(checkpoint.Phase) == CheckpointPhaseOperation {
			if _, isDelete := deleteDigests[checkpoint.OperationDigest]; isDelete {
				deleteCheckpointCount++
			}
		}
	}
	var consumedGrant model.BackupAssetRecoveryGrant
	if err := db.Where("id = ?", issued.GrantID).Take(&consumedGrant).Error; err != nil {
		t.Fatal(err)
	}
	if len(deleteItems) != 2 || len(deleteDigests) != 2 || len(fixture.target.deletes) != 2 || len(deleteCalls) != 2 ||
		consumed.Phase != string(CheckpointPhaseDeleteAuthorityConsumed) ||
		consumedCount != 1 || deleteCheckpointCount != 2 || consumedGrant.ConsumedAt == nil ||
		completedJob.TargetChainRevision != chainRevision || completedJob.State != string(JobStateSucceeded) {
		t.Fatalf("same-execution multi-delete job=%+v grant=%+v items=%d calls=%+v consumed/checkpoints=%d/%d",
			completedJob, consumedGrant, len(deleteItems), deleteCalls, consumedCount, deleteCheckpointCount)
	}
	assertRecoveryExecutionAttemptCompleted(t, db, fixture.claim.AttemptID)
}

func TestRecoveryExactMirrorMultipleDeletesConsumeSetAuthorityOnceAcrossRestart(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionMultiDeleteMigrationFixture(t)
	db := fixture.serviceFixture.db
	pauseSource := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, pauseSource, ""); err != nil {
		t.Fatalf("pause multi-delete exact-mirror execution: %v", err)
	}

	var pausedJob model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.jobID).Take(&pausedJob).Error; err != nil {
		t.Fatal(err)
	}
	var pausedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&pausedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(pausedCheckpoints) == 0 ||
		pausedCheckpoints[len(pausedCheckpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
		t.Fatalf("multi-delete authority was not durably paused: %+v", pausedCheckpoints)
	}
	required := pausedCheckpoints[len(pausedCheckpoints)-1]
	var deleteItems []model.BackupAssetRecoveryJobItem
	if err := db.Where(
		"job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationDelete,
	).Order("ordinal ASC").Find(&deleteItems).Error; err != nil {
		t.Fatal(err)
	}
	if len(deleteItems) != 2 || deleteItems[0].Outcome != "" || deleteItems[1].Outcome != "" {
		t.Fatalf("multi-delete fixture rows=%+v, want two pending deletes", deleteItems)
	}

	deleteRequest := fixture.serviceFixture.request
	deleteRequest.Operation = AuthorizationReceiptDeleteAuthorize
	deleteRequest.Category = AuthorizationReceiptCategoryExactMirrorDelete
	deleteRequest.Endpoint = recoveryDeleteAuthorizationEndpoint
	deleteRequest.IdempotencyKey = "exact-mirror-multi-delete-set-key"
	deleteRequest.Proof.JTI = "FAKE_RECOVERY_MULTI_DELETE_SET_PROOF_JTI"
	deleteRequest.ExpectedPlanRevision = jobPlanTransitionRevision(t, db, pausedJob.PlanID)
	deleteRequest.PreflightID = ""
	deleteRequest.JobID = fixture.jobID
	deleteRequest.CheckpointID = required.ID
	deleteRequest.AttemptID = fixture.claim.AttemptID
	deleteRequest.GrantID = ""
	deleteRequest.Reason = "FAKE_RECOVERY_MULTI_DELETE_SET_REASON"
	deleteRequest.GrantSecret = mustAuthorizationReceiptSecretForFixture()
	issued, err := fixture.serviceFixture.service.Authorize(context.Background(), deleteRequest)
	if err != nil {
		t.Fatalf("authorize exact-mirror multi-delete set: %v", err)
	}

	const checkpointCallback = "test:multi-delete-second-operation-projection-crash"
	projectionErr := errors.New("simulated crash before second delete checkpoint commit")
	if err := db.Callback().Create().Before("gorm:create").Register(checkpointCallback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
			len(fixture.target.deletes) == 2 {
			_ = tx.AddError(projectionErr)
		}
	}); err != nil {
		t.Fatalf("register second delete projection crash: %v", err)
	}
	interruptedSource := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
	interruptedErr := fixture.coordinator.ExecuteClaim(
		context.Background(), fixture.claim, interruptedSource, deleteRequest.GrantSecret,
	)
	if err := db.Callback().Create().Remove(checkpointCallback); err != nil {
		t.Fatalf("remove second delete projection crash: %v", err)
	}
	if !errors.Is(interruptedErr, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("interrupt after second persistent delete error=%v, want worker unavailable; deletes=%d",
			interruptedErr, len(fixture.target.deletes))
	}

	var interruptedJob model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.jobID).Take(&interruptedJob).Error; err != nil {
		t.Fatal(err)
	}
	var interruptedItems []model.BackupAssetRecoveryJobItem
	if err := db.Where(
		"job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationDelete,
	).Order("ordinal ASC").Find(&interruptedItems).Error; err != nil {
		t.Fatal(err)
	}
	var interruptedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&interruptedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(interruptedCheckpoints) != len(pausedCheckpoints)+2 {
		t.Fatalf("interrupted multi-delete checkpoints=%+v, want one consumption and one delete operation",
			interruptedCheckpoints)
	}
	consumed := interruptedCheckpoints[len(interruptedCheckpoints)-2]
	firstOperation := interruptedCheckpoints[len(interruptedCheckpoints)-1]
	if consumed.Phase != string(CheckpointPhaseDeleteAuthorityConsumed) ||
		firstOperation.Phase != string(CheckpointPhaseOperation) ||
		firstOperation.JobItemID != interruptedItems[0].ID || firstOperation.UnresolvedCategory != "" ||
		firstOperation.ObservationDigest != "" || firstOperation.ObservedTargetRevision != "" ||
		firstOperation.ObservedPresence != "" || firstOperation.SourceRevalidationOutcome != "" {
		t.Fatalf("interrupted multi-delete checkpoint tail consumed=%+v operation=%+v", consumed, firstOperation)
	}
	if len(interruptedItems) != 2 || interruptedItems[0].Outcome != "succeeded" ||
		interruptedItems[1].Outcome != "" || interruptedItems[0].FailureCategory != "" ||
		interruptedItems[1].FailureCategory != "" || len(fixture.target.deletes) != 2 ||
		fixture.target.deletes[0].TargetPathDigest != interruptedItems[0].TargetObjectDigest ||
		fixture.target.deletes[1].TargetPathDigest != interruptedItems[1].TargetObjectDigest ||
		firstOperation.OperationDigest != recoveryJobItemOperationDigest(interruptedItems[0]) ||
		firstOperation.PriorTargetRevision != pausedJob.TargetChainRevision ||
		firstOperation.NextTargetRevision != interruptedJob.TargetChainRevision ||
		interruptedJob.TargetChainRevision == pausedJob.TargetChainRevision {
		t.Fatalf("interrupted multi-delete items=%+v operation=%+v job=%+v deletes=%d",
			interruptedItems, firstOperation, interruptedJob, len(fixture.target.deletes))
	}

	var interruptedAttempt model.BackupAssetRecoveryAttempt
	var interruptedSourceLease model.RecoveryPointLease
	var interruptedNodeLease model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", fixture.claim.AttemptID).Take(&interruptedAttempt).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", fixture.claim.SourceFence.LeaseID).Take(&interruptedSourceLease).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", fixture.claim.NodeLeaseID).Take(&interruptedNodeLease).Error; err != nil {
		t.Fatal(err)
	}
	if interruptedJob.State != string(JobStateRunning) ||
		interruptedAttempt.State != string(AttemptStateRunning) || !interruptedAttempt.MutationArmed ||
		interruptedAttempt.ClosedAt != nil || interruptedSourceLease.Status != string(backupasset.LeaseActive) ||
		interruptedSourceLease.ReleasedAt != nil || interruptedNodeLease.State != "active" ||
		interruptedNodeLease.ReleasedAt != nil {
		t.Fatalf("interrupted multi-delete retained state job/attempt/source/node=%+v/%+v/%+v/%+v",
			interruptedJob, interruptedAttempt, interruptedSourceLease, interruptedNodeLease)
	}

	restarted := newRecoveryWorkerCoordinator(t, fixture.serviceFixture)
	restarted.target = fixture.target
	reloadSource := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
	if err := restarted.ExecuteClaim(context.Background(), fixture.claim, reloadSource, ""); err != nil {
		t.Fatalf("reload remaining delete without consumed bearer after %d destructive call(s): %v",
			len(fixture.target.deletes), err)
	}

	var completedJob model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.jobID).Take(&completedJob).Error; err != nil {
		t.Fatal(err)
	}
	var completedItems []model.BackupAssetRecoveryJobItem
	if err := db.Where(
		"job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationDelete,
	).Order("ordinal ASC").Find(&completedItems).Error; err != nil {
		t.Fatal(err)
	}
	var completedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&completedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(completedItems) != 2 || len(completedCheckpoints) != len(pausedCheckpoints)+3 {
		t.Fatalf("completed multi-delete items/checkpoints=%d/%d, want 2/%d",
			len(completedItems), len(completedCheckpoints), len(pausedCheckpoints)+3)
	}

	deleteCalls := make(map[string]int, len(fixture.target.deletes))
	for _, call := range fixture.target.deletes {
		deleteCalls[call.TargetPathDigest]++
	}
	chainRevision := pausedJob.TargetChainRevision
	for index, item := range completedItems {
		checkpoint := completedCheckpoints[len(pausedCheckpoints)+1+index]
		wantDeleteCalls := 1
		if index == 1 {
			// The second delete crossed the remote mutation boundary before the
			// injected projection crash. R52 reconciles that tuple with one
			// additional Delete call during restart.
			wantDeleteCalls = 2
		}
		advance := TargetAbsenceChainAdvance{
			PriorRevision: chainRevision, OperationDigest: recoveryJobItemOperationDigest(item),
			JobItemID: item.ID, SourceRevisionDigest: completedJob.SourceRevisionDigest,
			AttemptID: fixture.claim.AttemptID, AttemptFence: fixture.claim.AttemptFence,
			NodeFence: fixture.claim.NodeFence, AbsenceEvidence: TargetAbsenceEvidenceExact,
			TargetRevision: "target-revision-delete",
		}
		wantNextRevision, advanceErr := advance.NextRevision()
		if advanceErr != nil {
			t.Fatalf("derive delete %d absence-chain revision: %v", index, advanceErr)
		}
		if item.Outcome != "succeeded" || item.FailureCategory != "" ||
			deleteCalls[item.TargetObjectDigest] != wantDeleteCalls || checkpoint.Phase != string(CheckpointPhaseOperation) ||
			checkpoint.JobItemID != item.ID || checkpoint.UnresolvedCategory != "" ||
			checkpoint.ObservationDigest != "" || checkpoint.ObservedTargetRevision != "" ||
			checkpoint.ObservedPresence != "" || checkpoint.SourceRevalidationOutcome != "" ||
			checkpoint.OperationDigest != recoveryJobItemOperationDigest(item) ||
			checkpoint.PriorTargetRevision != chainRevision || checkpoint.NextTargetRevision != wantNextRevision {
			t.Fatalf("completed delete %d item=%+v checkpoint=%+v calls=%d want_next=%q",
				index, item, checkpoint, deleteCalls[item.TargetObjectDigest], wantNextRevision)
		}
		chainRevision = wantNextRevision
	}
	var consumedCount int64
	if err := db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND phase = ?", fixture.jobID, CheckpointPhaseDeleteAuthorityConsumed).
		Count(&consumedCount).Error; err != nil {
		t.Fatal(err)
	}
	var consumedGrant model.BackupAssetRecoveryGrant
	if err := db.Where("id = ?", issued.GrantID).Take(&consumedGrant).Error; err != nil {
		t.Fatal(err)
	}
	if len(fixture.target.deletes) != 3 || len(deleteCalls) != 2 || consumedCount != 1 ||
		consumedGrant.ConsumedAt == nil || completedJob.TargetChainRevision != chainRevision ||
		completedJob.State != string(JobStateSucceeded) {
		t.Fatalf("completed multi-delete job=%+v grant=%+v calls=%+v consumed=%d want_chain=%q",
			completedJob, consumedGrant, deleteCalls, consumedCount, chainRevision)
	}
	assertRecoveryExecutionAttemptCompleted(t, db, fixture.claim.AttemptID)
	var completedSourceLease model.RecoveryPointLease
	var completedNodeLease model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", fixture.claim.SourceFence.LeaseID).Take(&completedSourceLease).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id = ?", fixture.claim.NodeLeaseID).Take(&completedNodeLease).Error; err != nil {
		t.Fatal(err)
	}
	if completedSourceLease.Status != string(backupasset.LeaseReleased) || completedSourceLease.ReleasedAt == nil ||
		completedNodeLease.State != "released" || completedNodeLease.ReleasedAt == nil {
		t.Fatalf("completed multi-delete leases source/node=%+v/%+v", completedSourceLease, completedNodeLease)
	}
}

func TestRecoveryExactMirrorConsumedAuthorityFreshTakeoverRequiresAdoption(t *testing.T) {
	paused := newPausedAuthorizedExactMirrorDeleteFromExecution(
		t,
		"CONSUMED_AUTHORITY_FRESH_TAKEOVER",
		newExactMirrorOrdinaryExecutionMultiDeleteMigrationFixture(t),
	)
	fixture := paused.execution
	db := fixture.serviceFixture.db

	var deleteItems []model.BackupAssetRecoveryJobItem
	if err := db.Where(
		"job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationDelete,
	).Order("ordinal ASC").Find(&deleteItems).Error; err != nil {
		t.Fatal(err)
	}
	if len(deleteItems) != 2 || deleteItems[0].Outcome != "" || deleteItems[1].Outcome != "" {
		t.Fatalf("fresh-takeover fixture delete rows=%+v, want two pending deletes", deleteItems)
	}

	const checkpointCallback = "test:consumed-authority-fresh-takeover-first-delete-projection-crash"
	projectionErr := errors.New("simulated crash before first delete operation checkpoint commit")
	if err := db.Callback().Create().Before("gorm:create").Register(checkpointCallback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
			len(fixture.target.deletes) == 1 {
			_ = tx.AddError(projectionErr)
		}
	}); err != nil {
		t.Fatalf("register first delete projection crash: %v", err)
	}
	interruptedSource := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
	interruptedErr := fixture.coordinator.ExecuteClaim(
		context.Background(), fixture.claim, interruptedSource, paused.request.GrantSecret,
	)
	if err := db.Callback().Create().Remove(checkpointCallback); err != nil {
		t.Fatalf("remove first delete projection crash: %v", err)
	}
	if !errors.Is(interruptedErr, ErrRecoveryWorkerUnavailable) || len(fixture.target.deletes) != 1 {
		t.Fatalf("first delete interruption error=%v deletes=%d, want worker unavailable after one delete",
			interruptedErr, len(fixture.target.deletes))
	}

	var interruptedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&interruptedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(interruptedCheckpoints) != len(paused.checkpoints)+1 {
		t.Fatalf("interrupted checkpoints=%+v, want only one consumed-authority row after pause",
			interruptedCheckpoints)
	}
	required := interruptedCheckpoints[len(interruptedCheckpoints)-2]
	consumed := interruptedCheckpoints[len(interruptedCheckpoints)-1]
	if required.ID != paused.checkpoints[len(paused.checkpoints)-1].ID ||
		required.Phase != string(CheckpointPhaseDeleteAuthorityRequired) ||
		consumed.Phase != string(CheckpointPhaseDeleteAuthorityConsumed) ||
		required.AttemptID != fixture.claim.AttemptID || consumed.AttemptID != fixture.claim.AttemptID ||
		required.AttemptFence != fixture.claim.AttemptFence || consumed.AttemptFence != fixture.claim.AttemptFence ||
		required.NodeFence != fixture.claim.NodeFence || consumed.NodeFence != fixture.claim.NodeFence {
		t.Fatalf("interrupted historical delete authority required/consumed=%+v/%+v", required, consumed)
	}
	if err := db.Where(
		"job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationDelete,
	).Order("ordinal ASC").Find(&deleteItems).Error; err != nil {
		t.Fatal(err)
	}
	if deleteItems[0].Outcome != "" || deleteItems[1].Outcome != "" ||
		fixture.target.deletes[0].TargetPathDigest != deleteItems[0].TargetObjectDigest {
		t.Fatalf("interrupted delete rows=%+v calls=%+v, want first remote delete unprojected", deleteItems, fixture.target.deletes)
	}

	var historicalGrant model.BackupAssetRecoveryGrant
	if err := db.Where("id = ?", paused.result.GrantID).Take(&historicalGrant).Error; err != nil {
		t.Fatal(err)
	}
	if historicalGrant.ConsumedAt == nil || historicalGrant.DeleteAttemptID == nil ||
		*historicalGrant.DeleteAttemptID != fixture.claim.AttemptID ||
		historicalGrant.DeleteAttemptFence != fixture.claim.AttemptFence ||
		historicalGrant.DeleteNodeFence != fixture.claim.NodeFence {
		t.Fatalf("consumed historical grant=%+v, want original claim provenance", historicalGrant)
	}
	historicalConsumedAt := *historicalGrant.ConsumedAt

	fixture.serviceFixture.now = fixture.claim.LeaseExpiresAt.Add(time.Second)
	takeover, found, err := fixture.coordinator.TakeoverExpired(
		context.Background(), "exact-mirror-consumed-authority-takeover-worker",
	)
	if err != nil || !found {
		t.Fatalf("take over consumed exact-mirror execution: found=%t err=%v", found, err)
	}
	if takeover.AttemptID == fixture.claim.AttemptID || takeover.AttemptFence == fixture.claim.AttemptFence ||
		takeover.NodeFence == fixture.claim.NodeFence {
		t.Fatalf("fresh takeover reused historical claim: old=%+v new=%+v", fixture.claim, takeover)
	}

	writesBeforeReplay := len(fixture.target.writes)
	deletesBeforeReplay := len(fixture.target.deletes)
	verifiesBeforeReplay := len(fixture.target.verifies)
	workspaceCallsBeforeReplay := len(fixture.target.workspaceCalls)
	replaySource := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
	replayErr := fixture.coordinator.ExecuteClaim(context.Background(), takeover, replaySource, "")
	if !errors.Is(replayErr, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("fresh takeover ordinary execution error=%v, want fence loss before adoption", replayErr)
	}
	if len(fixture.target.writes) != writesBeforeReplay || len(fixture.target.deletes) != deletesBeforeReplay ||
		len(fixture.target.verifies) != verifiesBeforeReplay ||
		len(fixture.target.workspaceCalls) != workspaceCallsBeforeReplay {
		t.Fatalf("fresh takeover crossed target boundary before adoption: workspace/writes/deletes/verifies=%d/%d/%d/%d",
			len(fixture.target.workspaceCalls), len(fixture.target.writes),
			len(fixture.target.deletes), len(fixture.target.verifies))
	}

	var retainedConsumed model.BackupAssetRecoveryCheckpoint
	if err := db.Where("id = ?", consumed.ID).Take(&retainedConsumed).Error; err != nil {
		t.Fatal(err)
	}
	var retainedGrant model.BackupAssetRecoveryGrant
	if err := db.Where("id = ?", paused.result.GrantID).Take(&retainedGrant).Error; err != nil {
		t.Fatal(err)
	}
	if retainedConsumed.AttemptID != consumed.AttemptID ||
		retainedConsumed.AttemptFence != consumed.AttemptFence || retainedConsumed.NodeFence != consumed.NodeFence ||
		retainedGrant.DeleteAttemptID == nil || *retainedGrant.DeleteAttemptID != fixture.claim.AttemptID ||
		retainedGrant.DeleteAttemptFence != fixture.claim.AttemptFence ||
		retainedGrant.DeleteNodeFence != fixture.claim.NodeFence || retainedGrant.ConsumedAt == nil ||
		!retainedGrant.ConsumedAt.Equal(historicalConsumedAt) {
		t.Fatalf("fresh takeover rewrote historical authority checkpoint/grant=%+v/%+v",
			retainedConsumed, retainedGrant)
	}

	adopted, err := fixture.coordinator.AdoptInterruptedOperation(
		context.Background(), takeover, deleteItems[0].ID,
	)
	if err != nil {
		t.Fatalf("adopt consumed exact-mirror delete under fresh claim: %v", err)
	}
	if len(fixture.target.deletes) != deletesBeforeReplay || len(fixture.target.verifies) != verifiesBeforeReplay+1 ||
		adopted.Phase != string(CheckpointPhaseOperation) || adopted.JobItemID != deleteItems[0].ID ||
		adopted.AttemptID != takeover.AttemptID || adopted.AttemptFence != takeover.AttemptFence ||
		adopted.NodeFence != takeover.NodeFence || adopted.Sequence != consumed.Sequence+1 {
		t.Fatalf("fresh takeover adoption checkpoint=%+v deletes/verifies=%d/%d",
			adopted, len(fixture.target.deletes), len(fixture.target.verifies))
	}
	var adoptedItems []model.BackupAssetRecoveryJobItem
	if err := db.Where(
		"job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationDelete,
	).Order("ordinal ASC").Find(&adoptedItems).Error; err != nil {
		t.Fatal(err)
	}
	var activeTakeover model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", takeover.AttemptID).Take(&activeTakeover).Error; err != nil {
		t.Fatal(err)
	}
	if adoptedItems[0].Outcome != "succeeded" || adoptedItems[1].Outcome != "" ||
		activeTakeover.State != string(AttemptStateRunning) || activeTakeover.ClosedAt != nil {
		t.Fatalf("post-adoption continuation state items/attempt=%+v/%+v", adoptedItems, activeTakeover)
	}

	continuationSource := newRecoveryRepositoryContractSource(t, db, fixture.jobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), takeover, continuationSource, ""); err != nil {
		t.Fatalf("continue remaining delete with consumed historical authority: %v", err)
	}
	var completedJob model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.jobID).Take(&completedJob).Error; err != nil {
		t.Fatal(err)
	}
	var completedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&completedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	var completedGrant model.BackupAssetRecoveryGrant
	if err := db.Where("id = ?", paused.result.GrantID).Take(&completedGrant).Error; err != nil {
		t.Fatal(err)
	}
	deleteCalls := make(map[string]int, len(fixture.target.deletes))
	for _, call := range fixture.target.deletes {
		deleteCalls[call.TargetPathDigest]++
	}
	if len(fixture.target.deletes) != 2 || deleteCalls[deleteItems[0].TargetObjectDigest] != 1 ||
		deleteCalls[deleteItems[1].TargetObjectDigest] != 1 ||
		len(completedCheckpoints) != len(paused.checkpoints)+3 ||
		completedCheckpoints[len(completedCheckpoints)-1].AttemptID != takeover.AttemptID ||
		completedJob.State != string(JobStateSucceeded) || completedGrant.ConsumedAt == nil ||
		!completedGrant.ConsumedAt.Equal(historicalConsumedAt) || completedGrant.DeleteAttemptID == nil ||
		*completedGrant.DeleteAttemptID != fixture.claim.AttemptID ||
		completedGrant.DeleteAttemptFence != fixture.claim.AttemptFence ||
		completedGrant.DeleteNodeFence != fixture.claim.NodeFence {
		t.Fatalf("completed fresh-takeover delete set job/grant/checkpoints/calls=%+v/%+v/%d/%+v",
			completedJob, completedGrant, len(completedCheckpoints), deleteCalls)
	}
	assertRecoveryExecutionAttemptCompleted(t, db, takeover.AttemptID)
}

func TestRecoveryExactMirrorConsumedDeleteAuthorityReloadReconcilesAbsence(t *testing.T) {
	fixture := newExactMirrorOrdinaryExecutionFixture(t)
	pauseSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
	if err := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, pauseSource, ""); err != nil {
		t.Fatalf("pause exact-mirror execution: %v", err)
	}

	var pausedJob model.BackupAssetRecoveryJob
	if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&pausedJob).Error; err != nil {
		t.Fatal(err)
	}
	var pausedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&pausedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(pausedCheckpoints) == 0 ||
		pausedCheckpoints[len(pausedCheckpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
		t.Fatalf("delete authority was not durably paused: %+v", pausedCheckpoints)
	}
	required := pausedCheckpoints[len(pausedCheckpoints)-1]

	deleteRequest := fixture.serviceFixture.request
	deleteRequest.Operation = AuthorizationReceiptDeleteAuthorize
	deleteRequest.Category = AuthorizationReceiptCategoryExactMirrorDelete
	deleteRequest.Endpoint = recoveryDeleteAuthorizationEndpoint
	deleteRequest.IdempotencyKey = "exact-mirror-consumed-reload-key"
	deleteRequest.Proof.JTI = "FAKE_RECOVERY_CONSUMED_RELOAD_PROOF_JTI"
	deleteRequest.ExpectedPlanRevision = jobPlanTransitionRevision(
		t, fixture.serviceFixture.db, pausedJob.PlanID,
	)
	deleteRequest.PreflightID = ""
	deleteRequest.JobID = fixture.jobID
	deleteRequest.CheckpointID = required.ID
	deleteRequest.AttemptID = fixture.claim.AttemptID
	deleteRequest.GrantID = ""
	deleteRequest.Reason = "FAKE_RECOVERY_CONSUMED_RELOAD_REASON"
	deleteRequest.GrantSecret = mustAuthorizationReceiptSecretForFixture()
	issued, err := fixture.serviceFixture.service.Authorize(context.Background(), deleteRequest)
	if err != nil {
		t.Fatalf("authorize exact-mirror delete: %v", err)
	}

	const checkpointCallback = "test:consumed-delete-operation-projection-crash"
	projectionErr := errors.New("simulated crash before consumed delete checkpoint commit")
	if err := fixture.serviceFixture.db.Callback().Create().Before("gorm:create").Register(
		checkpointCallback,
		func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
				len(fixture.target.deletes) == 1 {
				_ = tx.AddError(projectionErr)
			}
		},
	); err != nil {
		t.Fatalf("register consumed delete projection crash: %v", err)
	}
	crashSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
	executeErr := fixture.coordinator.ExecuteClaim(
		context.Background(), fixture.claim, crashSource, deleteRequest.GrantSecret,
	)
	if err := fixture.serviceFixture.db.Callback().Create().Remove(checkpointCallback); err != nil {
		t.Fatalf("remove consumed delete projection crash: %v", err)
	}
	if !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("post-consumption crash error=%v, want worker unavailable", executeErr)
	}

	var consumedGrant model.BackupAssetRecoveryGrant
	if err := fixture.serviceFixture.db.Where("id = ?", issued.GrantID).Take(&consumedGrant).Error; err != nil {
		t.Fatal(err)
	}
	var crashedJob model.BackupAssetRecoveryJob
	if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&crashedJob).Error; err != nil {
		t.Fatal(err)
	}
	var crashedItem model.BackupAssetRecoveryJobItem
	if err := fixture.serviceFixture.db.Where(
		"job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationDelete,
	).Take(&crashedItem).Error; err != nil {
		t.Fatal(err)
	}
	var crashedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&crashedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if consumedGrant.ConsumedAt == nil || len(crashedCheckpoints) != len(pausedCheckpoints)+1 ||
		crashedCheckpoints[len(crashedCheckpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityConsumed) ||
		crashedJob.State != string(JobStateRunning) || crashedJob.TargetChainRevision != pausedJob.TargetChainRevision ||
		crashedItem.Outcome != "" || crashedItem.FailureCategory != "" || len(fixture.target.deletes) != 1 {
		t.Fatalf("post-consumption crash grant=%+v checkpoints=%+v job=%+v item=%+v deletes=%d",
			consumedGrant, crashedCheckpoints, crashedJob, crashedItem, len(fixture.target.deletes))
	}

	verifiesBeforeReload := len(fixture.target.verifies)
	reloadSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
	deleteRequest.GrantSecret = ""
	if err := fixture.coordinator.ExecuteClaim(
		context.Background(), fixture.claim, reloadSource, deleteRequest.GrantSecret,
	); err != nil {
		t.Fatalf("reload consumed delete authority: %v", err)
	}

	var completedJob model.BackupAssetRecoveryJob
	if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&completedJob).Error; err != nil {
		t.Fatal(err)
	}
	var completedItem model.BackupAssetRecoveryJobItem
	if err := fixture.serviceFixture.db.Where("id = ?", crashedItem.ID).Take(&completedItem).Error; err != nil {
		t.Fatal(err)
	}
	var completedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).
		Order("sequence ASC").Find(&completedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	var consumedCount int64
	if err := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND phase = ?", fixture.jobID, CheckpointPhaseDeleteAuthorityConsumed).
		Count(&consumedCount).Error; err != nil {
		t.Fatal(err)
	}
	if len(fixture.target.deletes) != 2 || len(fixture.target.verifies) != verifiesBeforeReload+1 ||
		consumedCount != 1 || len(completedCheckpoints) != len(crashedCheckpoints)+1 ||
		completedCheckpoints[len(completedCheckpoints)-1].Phase != string(CheckpointPhaseOperation) ||
		completedJob.State != string(JobStateSucceeded) ||
		completedJob.TargetChainRevision == crashedJob.TargetChainRevision || completedItem.Outcome != "succeeded" ||
		completedItem.FailureCategory != "" {
		t.Fatalf("consumed reload checkpoints=%+v job=%+v item=%+v deletes=%d verifies=%d/%d consumed=%d",
			completedCheckpoints, completedJob, completedItem, len(fixture.target.deletes),
			verifiesBeforeReload, len(fixture.target.verifies), consumedCount)
	}
	assertRecoveryExecutionAttemptCompleted(t, fixture.serviceFixture.db, fixture.claim.AttemptID)
}

func TestRecoveryConsumedDeleteReconcilesTupleBeforeAbsenceAdoption(t *testing.T) {
	fixture := newPausedAuthorizedExactMirrorDelete(t, "CONSUMED_TUPLE_RECONCILIATION")
	db := fixture.execution.serviceFixture.db
	target := fixture.execution.target

	const crashCallback = "test:consumed-delete-tuple-reconciliation-crash"
	projectionErr := errors.New("simulated crash before consumed delete projection")
	if err := db.Callback().Create().Before("gorm:create").Register(crashCallback, func(tx *gorm.DB) {
		if tx.Statement != nil &&
			tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
			len(target.deletes) == 1 {
			_ = tx.AddError(projectionErr)
		}
	}); err != nil {
		t.Fatalf("register consumed delete projection crash: %v", err)
	}
	crashSource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
	executeErr := fixture.execution.coordinator.ExecuteClaim(
		context.Background(), fixture.execution.claim, crashSource, fixture.request.GrantSecret,
	)
	if err := db.Callback().Create().Remove(crashCallback); err != nil {
		t.Fatalf("remove consumed delete projection crash: %v", err)
	}
	if !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("post-delete projection crash error=%v, want worker unavailable", executeErr)
	}

	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.execution.jobID).
		Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) == 0 ||
		checkpoints[len(checkpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityConsumed) ||
		len(target.deletes) != 1 {
		t.Fatalf("crashed consumed delete checkpoints=%+v deletes=%d", checkpoints, len(target.deletes))
	}

	deletesBeforeReload := len(target.deletes)
	verifyObserved := false
	deleteCallsAtVerify := 0
	target.afterVerify = func(int) {
		verifyObserved = true
		deleteCallsAtVerify = len(target.deletes)
	}
	reloadSource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
	if err := fixture.execution.coordinator.ExecuteClaim(
		context.Background(), fixture.execution.claim, reloadSource, "",
	); err != nil {
		t.Fatalf("reload consumed delete authority: %v", err)
	}

	if !verifyObserved || deleteCallsAtVerify != deletesBeforeReload+1 ||
		len(target.deletes) != deletesBeforeReload+1 {
		t.Fatalf(
			"consumed delete reload verify=%t deletes_at_verify=%d deletes=%d, want one tuple reconciliation before verify",
			verifyObserved, deleteCallsAtVerify, len(target.deletes),
		)
	}
}

func TestRecoveryConsumedDeleteUnavailableDoesNotTerminalize(t *testing.T) {
	fixture := newPausedAuthorizedExactMirrorDelete(t, "CONSUMED_DELETE_UNAVAILABLE")
	db := fixture.execution.serviceFixture.db
	target := fixture.execution.target

	const callbackName = "test:consumed-delete-unavailable-projection-crash"
	projectionErr := errors.New("simulated crash before consumed delete projection")
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
			len(target.deletes) == 1 {
			_ = tx.AddError(projectionErr)
		}
	}); err != nil {
		t.Fatalf("register consumed-delete projection crash: %v", err)
	}

	crashSource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
	crashErr := fixture.execution.coordinator.ExecuteClaim(
		context.Background(), fixture.execution.claim, crashSource, fixture.request.GrantSecret,
	)
	if err := db.Callback().Create().Remove(callbackName); err != nil {
		t.Fatalf("remove consumed-delete projection crash: %v", err)
	}
	if !errors.Is(crashErr, ErrRecoveryWorkerUnavailable) || len(target.deletes) != 1 {
		t.Fatalf("consumed-delete crash error=%v deletes=%d, want unavailable/1", crashErr, len(target.deletes))
	}

	var crashedJob model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.execution.jobID).Take(&crashedJob).Error; err != nil {
		t.Fatal(err)
	}
	var crashedItem model.BackupAssetRecoveryJobItem
	if err := db.Where("job_id = ? AND operation_kind = ?", fixture.execution.jobID, RecoveryOperationDelete).
		Take(&crashedItem).Error; err != nil {
		t.Fatal(err)
	}
	var crashedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.execution.jobID).
		Order("sequence ASC").Find(&crashedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}

	target.deleteErr = func(call int) error {
		if call == 2 {
			return ErrRecoveryTargetUnavailable
		}
		return nil
	}

	retrySource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
	retryErr := fixture.execution.coordinator.ExecuteClaim(
		context.Background(), fixture.execution.claim, retrySource, "",
	)
	if !errors.Is(retryErr, ErrRecoveryWorkerUnavailable) || errors.Is(retryErr, ErrInvalidTargetVerification) {
		t.Fatalf("consumed-delete unavailable retry error=%v, want worker unavailable without terminal verification", retryErr)
	}

	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.execution.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := db.Where("id = ?", crashedItem.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.execution.jobID).
		Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	var evidenceCount int64
	if err := db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("job_id = ? AND kind = ?", fixture.execution.jobID, "failure").
		Count(&evidenceCount).Error; err != nil {
		t.Fatal(err)
	}

	if len(checkpoints) != len(crashedCheckpoints) || evidenceCount != 0 ||
		job.State != string(JobStateRunning) || job.FailureCategory != "" ||
		job.TargetChainRevision != crashedJob.TargetChainRevision ||
		item.Outcome != "" || item.FailureCategory != "" || len(target.deletes) != 2 {
		t.Fatalf("consumed-delete unavailable projected job=%+v item=%+v checkpoints=%d/%d evidence=%d deletes=%d",
			job, item, len(checkpoints), len(crashedCheckpoints), evidenceCount, len(target.deletes))
	}

	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", fixture.execution.claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	var sourceLease model.RecoveryPointLease
	if err := db.Where("id = ?", fixture.execution.claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", fixture.execution.claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil ||
		sourceLease.Status != string(backupasset.LeaseActive) || sourceLease.ReleasedAt != nil ||
		nodeLease.State != "active" || nodeLease.ReleasedAt != nil {
		t.Fatalf("consumed-delete unavailable closed owner attempt/source/node=%+v/%+v/%+v",
			attempt, sourceLease, nodeLease)
	}

	target.deleteErr = nil
	replaySource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
	if err := fixture.execution.coordinator.ExecuteClaim(
		context.Background(), fixture.execution.claim, replaySource, "",
	); err != nil {
		t.Fatalf("re-enter consumed-delete tuple after temporary unavailability: %v", err)
	}
	if err := db.Where("id = ?", fixture.execution.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateSucceeded) || len(target.deletes) != 3 {
		t.Fatalf("re-entered consumed-delete job=%+v deletes=%d, want succeeded/3", job, len(target.deletes))
	}
}

func TestRecoveryFreshlyConsumedDeleteUnavailableDoesNotTerminalize(t *testing.T) {
	tests := []struct {
		name   string
		inject func(*recoveryExecutionTargetFake)
	}{
		{
			name: "delete unavailable",
			inject: func(target *recoveryExecutionTargetFake) {
				target.deleteErr = func(int) error { return ErrRecoveryTargetUnavailable }
			},
		},
		{
			name: "verification unavailable",
			inject: func(target *recoveryExecutionTargetFake) {
				target.verifyErr = func(int) error { return ErrRecoveryTargetUnavailable }
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPausedAuthorizedExactMirrorDelete(
				t, "FRESHLY_CONSUMED_DELETE_UNAVAILABLE_"+strings.ToUpper(strings.ReplaceAll(testCase.name, " ", "_")),
			)
			db := fixture.execution.serviceFixture.db
			target := fixture.execution.target
			testCase.inject(target)

			source := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
			executeErr := fixture.execution.coordinator.ExecuteClaim(
				context.Background(), fixture.execution.claim, source, fixture.request.GrantSecret,
			)
			if !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) ||
				errors.Is(executeErr, ErrInvalidTargetVerification) {
				t.Fatalf("freshly consumed delete unavailable error=%v, want retryable worker unavailable", executeErr)
			}

			var grant model.BackupAssetRecoveryGrant
			if err := db.Where("id = ?", fixture.result.GrantID).Take(&grant).Error; err != nil {
				t.Fatal(err)
			}
			var job model.BackupAssetRecoveryJob
			if err := db.Where("id = ?", fixture.execution.jobID).Take(&job).Error; err != nil {
				t.Fatal(err)
			}
			var item model.BackupAssetRecoveryJobItem
			if err := db.Where("job_id = ? AND operation_kind = ?", fixture.execution.jobID, RecoveryOperationDelete).
				Take(&item).Error; err != nil {
				t.Fatal(err)
			}
			var checkpoints []model.BackupAssetRecoveryCheckpoint
			if err := db.Where("job_id = ?", fixture.execution.jobID).
				Order("sequence ASC").Find(&checkpoints).Error; err != nil {
				t.Fatal(err)
			}
			var evidenceCount int64
			if err := db.Model(&model.BackupAssetRecoveryEvidence{}).
				Where("job_id = ? AND kind = ?", fixture.execution.jobID, "failure").
				Count(&evidenceCount).Error; err != nil {
				t.Fatal(err)
			}

			if grant.ConsumedAt == nil || len(checkpoints) != len(fixture.checkpoints)+1 ||
				checkpoints[len(checkpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityConsumed) ||
				evidenceCount != 0 || job.State != string(JobStateRunning) || job.FailureCategory != "" ||
				job.TargetChainRevision != fixture.job.TargetChainRevision || item.Outcome != "" ||
				item.FailureCategory != "" || len(target.deletes) != 1 {
				t.Fatalf(
					"freshly consumed delete unavailable projected grant=%+v job=%+v item=%+v checkpoints=%+v evidence=%d deletes=%d",
					grant, job, item, checkpoints, evidenceCount, len(target.deletes),
				)
			}

			var attempt model.BackupAssetRecoveryAttempt
			if err := db.Where("id = ?", fixture.execution.claim.AttemptID).Take(&attempt).Error; err != nil {
				t.Fatal(err)
			}
			var sourceLease model.RecoveryPointLease
			if err := db.Where("id = ?", fixture.execution.claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
				t.Fatal(err)
			}
			var nodeLease model.BackupAssetRecoveryNodeLease
			if err := db.Where("id = ?", fixture.execution.claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
				t.Fatal(err)
			}
			if attempt.State != string(AttemptStateRunning) || attempt.ClosedAt != nil ||
				sourceLease.Status != string(backupasset.LeaseActive) || sourceLease.ReleasedAt != nil ||
				nodeLease.State != "active" || nodeLease.ReleasedAt != nil {
				t.Fatalf("freshly consumed delete unavailable closed owner attempt/source/node=%+v/%+v/%+v",
					attempt, sourceLease, nodeLease)
			}
		})
	}
}

func TestRecoveryConsumedDeleteContradictionTerminalizesCurrentOwner(t *testing.T) {
	fixture := newPausedAuthorizedExactMirrorDelete(t, "CONSUMED_DELETE_CONTRADICTION")
	db := fixture.execution.serviceFixture.db
	target := fixture.execution.target

	const callbackName = "test:consumed-delete-contradiction-projection-crash"
	projectionErr := errors.New("simulated crash before consumed delete projection")
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
			len(target.deletes) == 1 {
			_ = tx.AddError(projectionErr)
		}
	}); err != nil {
		t.Fatalf("register consumed-delete contradiction crash: %v", err)
	}

	crashSource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
	crashErr := fixture.execution.coordinator.ExecuteClaim(
		context.Background(), fixture.execution.claim, crashSource, fixture.request.GrantSecret,
	)
	if err := db.Callback().Create().Remove(callbackName); err != nil {
		t.Fatalf("remove consumed-delete contradiction crash: %v", err)
	}
	if !errors.Is(crashErr, ErrRecoveryWorkerUnavailable) || len(target.deletes) != 1 {
		t.Fatalf("consumed-delete contradiction crash error=%v deletes=%d, want unavailable/1",
			crashErr, len(target.deletes))
	}

	var crashedJob model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.execution.jobID).Take(&crashedJob).Error; err != nil {
		t.Fatal(err)
	}
	target.deleteErr = func(call int) error {
		if call == 2 {
			return ErrRecoveryTargetChanged
		}
		return nil
	}

	retrySource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
	retryErr := fixture.execution.coordinator.ExecuteClaim(
		context.Background(), fixture.execution.claim, retrySource, "",
	)
	if !errors.Is(retryErr, ErrInvalidTargetVerification) ||
		errors.Is(retryErr, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("consumed-delete contradiction retry error=%v, want terminal unresolved outcome", retryErr)
	}
	assertRecoveryUnresolvedOutcomeProjection(
		t, db, fixture.execution.claim, crashedJob,
		UnresolvedOperationWriteResultInvalid, SourceRevalidationMatched, 3,
	)
	if len(target.deletes) != 2 {
		t.Fatalf("consumed-delete contradiction delete calls=%d, want 2", len(target.deletes))
	}
}

func TestRecoveryOrdinaryDeleteDispositionMatrix(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ordinaryDeleteDisposition
	}{
		{name: "context canceled", err: context.Canceled, want: ordinaryDeleteRetryable},
		{name: "deadline", err: context.DeadlineExceeded, want: ordinaryDeleteRetryable},
		{name: "target unavailable", err: ErrRecoveryTargetUnavailable, want: ordinaryDeleteRetryable},
		{name: "key unavailable", err: backupasset.ErrKeyUnavailable, want: ordinaryDeleteRetryable},
		{name: "target changed", err: ErrRecoveryTargetChanged, want: ordinaryDeleteContradictory},
		{name: "invalid permit", err: ErrInvalidTargetPermit, want: ordinaryDeleteFenceLost},
		{name: "fence lost", err: ErrRecoveryWorkerFenceLost, want: ordinaryDeleteFenceLost},
		{name: "unknown", err: errors.New("unclassified delete failure"), want: ordinaryDeleteContradictory},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyOrdinaryDeleteDisposition(test.err); got != test.want {
				t.Fatalf("delete disposition=%d, want %d", got, test.want)
			}
		})
	}
}

func TestRecoveryExactMirrorConsumedDeleteVerificationErrorProjectsUnresolvedOutcomeSQLite(t *testing.T) {
	fixture := newPausedAuthorizedExactMirrorDeleteMigration(t, "CONSUMED_VERIFY_ERROR")
	db := fixture.execution.serviceFixture.db
	const checkpointCallback = "test:consumed-delete-verification-operation-projection-crash"
	projectionErr := errors.New("simulated crash before consumed delete operation commit")
	if err := db.Callback().Create().Before("gorm:create").Register(checkpointCallback, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryCheckpoint{}).TableName() &&
			len(fixture.execution.target.deletes) == 1 {
			_ = tx.AddError(projectionErr)
		}
	}); err != nil {
		t.Fatalf("register consumed delete verification crash: %v", err)
	}
	crashSource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
	executeErr := fixture.execution.coordinator.ExecuteClaim(
		context.Background(), fixture.execution.claim, crashSource, fixture.request.GrantSecret,
	)
	if err := db.Callback().Create().Remove(checkpointCallback); err != nil {
		t.Fatalf("remove consumed delete verification crash: %v", err)
	}
	if !errors.Is(executeErr, ErrRecoveryWorkerUnavailable) {
		t.Fatalf("post-consumption crash error=%v, want ErrRecoveryWorkerUnavailable", executeErr)
	}

	var crashedJob model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.execution.jobID).Take(&crashedJob).Error; err != nil {
		t.Fatal(err)
	}
	var crashedItem model.BackupAssetRecoveryJobItem
	if err := db.Where("job_id = ? AND operation_kind = ?", fixture.execution.jobID, RecoveryOperationDelete).
		Take(&crashedItem).Error; err != nil {
		t.Fatal(err)
	}
	var crashedCheckpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.execution.jobID).Order("sequence ASC").Find(&crashedCheckpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(crashedCheckpoints) == 0 ||
		crashedCheckpoints[len(crashedCheckpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityConsumed) ||
		crashedJob.State != string(JobStateRunning) || crashedItem.Outcome != "" ||
		len(fixture.execution.target.deletes) != 1 {
		t.Fatalf("consumed-delete crash job=%+v item=%+v checkpoints=%+v deletes=%d",
			crashedJob, crashedItem, crashedCheckpoints, len(fixture.execution.target.deletes))
	}

	verificationErr := errors.New("injected consumed-delete verification error")
	fixture.execution.target.verifyErr = func(int) error { return verificationErr }
	reloadSource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
	executeErr = fixture.execution.coordinator.ExecuteClaim(
		context.Background(), fixture.execution.claim, reloadSource, "",
	)
	if !errors.Is(executeErr, ErrInvalidTargetVerification) || errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
		t.Fatalf("consumed-delete verification error=%v, want durable unresolved target verification", executeErr)
	}

	var job model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", fixture.execution.jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	var item model.BackupAssetRecoveryJobItem
	if err := db.Where("id = ?", crashedItem.ID).Take(&item).Error; err != nil {
		t.Fatal(err)
	}
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", fixture.execution.claim.AttemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	var sourceLease model.RecoveryPointLease
	if err := db.Where("id = ?", fixture.execution.claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
		t.Fatal(err)
	}
	var nodeLease model.BackupAssetRecoveryNodeLease
	if err := db.Where("id = ?", fixture.execution.claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
		t.Fatal(err)
	}
	var checkpoints []model.BackupAssetRecoveryCheckpoint
	if err := db.Where("job_id = ?", fixture.execution.jobID).Order("sequence ASC").Find(&checkpoints).Error; err != nil {
		t.Fatal(err)
	}
	if len(checkpoints) != len(crashedCheckpoints)+1 {
		t.Fatalf("consumed-delete unresolved checkpoints=%+v, prior=%+v", checkpoints, crashedCheckpoints)
	}
	unresolved := checkpoints[len(checkpoints)-1]
	wantObservationDigest := ""
	wantWriteResultDigest := framedDigest(
		"xirang/recovery/unresolved-write-result/v1",
		"0", "", "target-revision-delete",
	)
	if unresolved.Phase != string(CheckpointPhaseOperationUnresolved) ||
		unresolved.JobItemID != crashedItem.ID ||
		unresolved.UnresolvedCategory != string(UnresolvedOperationObservationInvalid) ||
		unresolved.WriteResultDigest != wantWriteResultDigest || unresolved.WriteTargetRevision != "target-revision-delete" ||
		unresolved.ObservationDigest != wantObservationDigest ||
		unresolved.ObservedTargetRevision != "" || unresolved.ObservedPresence != "" ||
		unresolved.SourceRevalidationOutcome != string(SourceRevalidationMatched) ||
		unresolved.PriorTargetRevision != crashedJob.TargetChainRevision || unresolved.NextTargetRevision != "" {
		t.Fatalf("consumed-delete unresolved checkpoint=%+v want_observation_digest=%q", unresolved, wantObservationDigest)
	}
	if job.State != string(JobStateNeedsAttention) ||
		job.FailureCategory != recoveryRemoteOutcomeUnresolvedFailureCategory ||
		job.TargetChainRevision != crashedJob.TargetChainRevision ||
		item.Outcome != "failed" || item.FailureCategory != recoveryRemoteOutcomeUnresolvedFailureCategory ||
		item.BytesWritten != crashedItem.BytesWritten || item.VerifiedSize != crashedItem.VerifiedSize ||
		item.VerifiedDigest != crashedItem.VerifiedDigest ||
		attempt.State != string(AttemptStateFailed) || attempt.ClosedAt == nil ||
		sourceLease.Status != string(backupasset.LeaseReleased) || sourceLease.ReleasedAt == nil ||
		nodeLease.State != "released" || nodeLease.ReleasedAt == nil {
		t.Fatalf("consumed-delete unresolved job/item/attempt/source/node=%+v/%+v/%+v/%+v/%+v",
			job, item, attempt, sourceLease, nodeLease)
	}
	var evidence []model.BackupAssetRecoveryEvidence
	if err := db.Where("job_id = ? AND kind = ?", fixture.execution.jobID, "failure").Find(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	wantSummaryDigest := recoveryUnresolvedOutcomeSummaryDigest(
		fixture.execution.claim, unresolved, crashedItem, fixture.execution.serviceFixture.now,
	)
	if len(evidence) != 1 || evidence[0].Outcome != "needs_attention" ||
		evidence[0].CheckpointID == nil || *evidence[0].CheckpointID != unresolved.ID ||
		evidence[0].SummaryDigest != wantSummaryDigest || evidence[0].DifferenceCount != 0 ||
		evidence[0].VerifiedAt == nil {
		t.Fatalf("consumed-delete unresolved evidence=%+v want_summary_digest=%q", evidence, wantSummaryDigest)
	}
	var legacyJobs, legacyItems int64
	if err := db.Model(&model.BackupAssetRecoveryJob{}).
		Where("id = ? AND failure_category = ?", fixture.execution.jobID, recoveryPostPauseFailureCategory).
		Count(&legacyJobs).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND failure_category = ?", fixture.execution.jobID, recoveryPostPauseFailureCategory).
		Count(&legacyItems).Error; err != nil {
		t.Fatal(err)
	}
	if legacyJobs != 0 || legacyItems != 0 || len(fixture.execution.target.deletes) != 2 {
		t.Fatalf("consumed-delete unresolved legacy job/items=%d/%d deletes=%d",
			legacyJobs, legacyItems, len(fixture.execution.target.deletes))
	}
}

func TestRecoveryExactMirrorPostPauseFailuresEnterTerminalDisposition(t *testing.T) {
	tests := []struct {
		name   string
		suffix string
		inject func(*testing.T, *pausedAuthorizedExactMirrorDelete)
	}{
		{
			name:   "target observation failure",
			suffix: "OBSERVATION_FAILURE",
			inject: func(_ *testing.T, fixture *pausedAuthorizedExactMirrorDelete) {
				fixture.execution.target.lstatErr = func(call int) error {
					if call == 2 {
						return errors.New("injected post-pause target observation failure")
					}
					return nil
				}
			},
		},
		{
			name:   "grant consumption transaction failure",
			suffix: "CONSUMPTION_FAILURE",
			inject: func(t *testing.T, fixture *pausedAuthorizedExactMirrorDelete) {
				t.Helper()
				callbackName := "recovery:post-pause-consumption-failure:" + t.Name()
				if err := fixture.execution.serviceFixture.db.Callback().Update().
					Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
					if tx.Statement != nil &&
						tx.Statement.Table == (model.BackupAssetRecoveryGrant{}).TableName() {
						_ = tx.AddError(errors.New("injected delete authority consumption transaction failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = fixture.execution.serviceFixture.db.Callback().Update().Remove(callbackName)
				})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPausedAuthorizedExactMirrorDelete(t, test.suffix)
			test.inject(t, &fixture)
			db := fixture.execution.serviceFixture.db
			var itemsBefore []model.BackupAssetRecoveryJobItem
			if err := db.Where("job_id = ?", fixture.execution.jobID).Order("ordinal ASC").Find(&itemsBefore).Error; err != nil {
				t.Fatal(err)
			}

			resumeSource := newRecoveryRepositoryContractSource(t, db, fixture.execution.jobID)
			executeErr := fixture.execution.coordinator.ExecuteClaim(
				context.Background(), fixture.execution.claim, resumeSource, fixture.request.GrantSecret,
			)
			if executeErr == nil {
				t.Fatal("post-pause failure returned nil")
			}

			var grant model.BackupAssetRecoveryGrant
			if err := db.Where("id = ?", fixture.result.GrantID).Take(&grant).Error; err != nil {
				t.Fatal(err)
			}
			var job model.BackupAssetRecoveryJob
			if err := db.Where("id = ?", fixture.execution.jobID).Take(&job).Error; err != nil {
				t.Fatal(err)
			}
			var attempt model.BackupAssetRecoveryAttempt
			if err := db.Where("id = ?", fixture.execution.claim.AttemptID).Take(&attempt).Error; err != nil {
				t.Fatal(err)
			}
			var sourceLease model.RecoveryPointLease
			if err := db.Where("id = ?", fixture.execution.claim.SourceFence.LeaseID).Take(&sourceLease).Error; err != nil {
				t.Fatal(err)
			}
			var nodeLease model.BackupAssetRecoveryNodeLease
			if err := db.Where("id = ?", fixture.execution.claim.NodeLeaseID).Take(&nodeLease).Error; err != nil {
				t.Fatal(err)
			}
			var checkpoints []model.BackupAssetRecoveryCheckpoint
			if err := db.Where("job_id = ?", fixture.execution.jobID).
				Order("sequence ASC").Find(&checkpoints).Error; err != nil {
				t.Fatal(err)
			}
			var itemsAfter []model.BackupAssetRecoveryJobItem
			if err := db.Where("job_id = ?", fixture.execution.jobID).Order("ordinal ASC").Find(&itemsAfter).Error; err != nil {
				t.Fatal(err)
			}
			var evidence []model.BackupAssetRecoveryEvidence
			if err := db.Where("job_id = ? AND kind = ?", fixture.execution.jobID, "failure").Find(&evidence).Error; err != nil {
				t.Fatal(err)
			}

			if grant.ConsumedAt != nil || grant.RevokedAt != nil ||
				job.State != string(JobStateNeedsAttention) || job.FailureCategory == "" ||
				job.TargetChainRevision != fixture.job.TargetChainRevision ||
				attempt.State != string(AttemptStateFailed) || attempt.ClosedAt == nil ||
				sourceLease.Status != string(backupasset.LeaseReleased) || sourceLease.ReleasedAt == nil ||
				nodeLease.State != "released" || nodeLease.ReleasedAt == nil ||
				len(fixture.execution.target.deletes) != 0 ||
				!reflect.DeepEqual(checkpoints, fixture.checkpoints) || len(itemsAfter) != len(itemsBefore) {
				t.Fatalf("post-pause disposition err=%v grant=%+v job=%+v attempt=%+v source=%+v node=%+v checkpoints=%+v deletes=%d",
					executeErr, grant, job, attempt, sourceLease, nodeLease, checkpoints,
					len(fixture.execution.target.deletes))
			}
			for index := range itemsBefore {
				before, after := itemsBefore[index], itemsAfter[index]
				if RecoveryOperationKind(before.OperationKind) == RecoveryOperationDelete {
					if after.Outcome != "failed" || after.FailureCategory != job.FailureCategory ||
						after.BytesWritten != 0 || after.VerifiedSize != 0 || after.VerifiedDigest != "" {
						t.Fatalf("post-pause delete item=%+v", after)
					}
					continue
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("post-pause disposition changed completed item before=%+v after=%+v", before, after)
				}
			}
			required := fixture.checkpoints[len(fixture.checkpoints)-1]
			if len(evidence) != 1 || evidence[0].Outcome != "needs_attention" ||
				evidence[0].CheckpointID == nil || *evidence[0].CheckpointID != required.ID ||
				evidence[0].AttemptID == nil || *evidence[0].AttemptID != fixture.execution.claim.AttemptID ||
				evidence[0].VerifiedAt == nil {
				t.Fatalf("post-pause failure evidence=%+v", evidence)
			}
		})
	}
}

func TestRecoveryExactMirrorStaleDeleteAuthorityDoesNotConsumeGrant(t *testing.T) {
	tests := []struct {
		name                 string
		wantFreshObservation bool
		mutate               func(*testing.T, *exactMirrorOrdinaryExecutionFixture, model.BackupAssetRecoveryCheckpoint)
	}{
		{
			name: "delete_node_revision",
			mutate: func(t *testing.T, fixture *exactMirrorOrdinaryExecutionFixture, required model.BackupAssetRecoveryCheckpoint) {
				t.Helper()
				updated := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
					Where("id = ?", required.ID).
					Update("delete_node_revision", "stale-node-revision")
				if updated.Error != nil || updated.RowsAffected != 1 {
					t.Fatalf("stale delete node revision update: %v rows=%d", updated.Error, updated.RowsAffected)
				}
			},
		},
		{
			name: "delete_root_revision",
			mutate: func(t *testing.T, fixture *exactMirrorOrdinaryExecutionFixture, required model.BackupAssetRecoveryCheckpoint) {
				t.Helper()
				updated := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
					Where("id = ?", required.ID).
					Update("delete_root_revision", "stale-root-revision")
				if updated.Error != nil || updated.RowsAffected != 1 {
					t.Fatalf("stale delete root revision update: %v rows=%d", updated.Error, updated.RowsAffected)
				}
			},
		},
		{
			name: "target_chain_revision",
			mutate: func(t *testing.T, fixture *exactMirrorOrdinaryExecutionFixture, _ model.BackupAssetRecoveryCheckpoint) {
				t.Helper()
				updated := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryJob{}).
					Where("id = ?", fixture.jobID).
					Update("target_chain_revision", "stale-target-chain-revision")
				if updated.Error != nil || updated.RowsAffected != 1 {
					t.Fatalf("stale target chain revision update: %v rows=%d", updated.Error, updated.RowsAffected)
				}
			},
		},
		{
			name:                 "fresh_target_observation",
			wantFreshObservation: true,
			mutate: func(_ *testing.T, fixture *exactMirrorOrdinaryExecutionFixture, _ model.BackupAssetRecoveryCheckpoint) {
				fixture.target.lstatResult = func(_ int, result TargetLstatResult) TargetLstatResult {
					result.IdentityDigest = "stale-target-observation"
					return result
				}
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newExactMirrorOrdinaryExecutionFixture(t)
			source := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
			if err := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, source, ""); err != nil {
				t.Fatalf("pause exact-mirror execution: %v", err)
			}
			var job model.BackupAssetRecoveryJob
			if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
				t.Fatal(err)
			}
			var checkpoints []model.BackupAssetRecoveryCheckpoint
			if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).
				Order("sequence ASC").Find(&checkpoints).Error; err != nil {
				t.Fatal(err)
			}
			if len(checkpoints) == 0 || checkpoints[len(checkpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
				t.Fatalf("delete authority was not durably paused: %+v", checkpoints)
			}
			required := checkpoints[len(checkpoints)-1]
			deleteRequest := fixture.serviceFixture.request
			deleteRequest.Operation = AuthorizationReceiptDeleteAuthorize
			deleteRequest.Category = AuthorizationReceiptCategoryExactMirrorDelete
			deleteRequest.Endpoint = recoveryDeleteAuthorizationEndpoint
			deleteRequest.IdempotencyKey = "exact-mirror-stale-delete-" + testCase.name
			deleteRequest.Proof.JTI = "FAKE_RECOVERY_STALE_DELETE_PROOF_" + testCase.name
			deleteRequest.ExpectedPlanRevision = jobPlanTransitionRevision(t, fixture.serviceFixture.db, job.PlanID)
			deleteRequest.PreflightID = ""
			deleteRequest.JobID = fixture.jobID
			deleteRequest.CheckpointID = required.ID
			deleteRequest.AttemptID = fixture.claim.AttemptID
			deleteRequest.GrantID = ""
			deleteRequest.Reason = "FAKE_RECOVERY_STALE_DELETE_REASON_" + testCase.name
			deleteRequest.GrantSecret = mustAuthorizationReceiptSecretForFixture()
			issued, err := fixture.serviceFixture.service.Authorize(context.Background(), deleteRequest)
			if err != nil {
				t.Fatalf("authorize exact-mirror delete through AuthorizationService: %v", err)
			}
			if issued.GrantID == "" || issued.GrantCategory != AuthorityExactMirrorDelete {
				t.Fatalf("delete authorization result=%+v, want exact-mirror grant", issued)
			}

			var grantBefore model.BackupAssetRecoveryGrant
			if err := fixture.serviceFixture.db.Where("id = ?", issued.GrantID).Take(&grantBefore).Error; err != nil {
				t.Fatal(err)
			}
			var itemBefore model.BackupAssetRecoveryJobItem
			if err := fixture.serviceFixture.db.Where("job_id = ? AND operation_kind = ?", fixture.jobID, RecoveryOperationDelete).
				Take(&itemBefore).Error; err != nil {
				t.Fatal(err)
			}
			checkpointCountBefore := int64(len(checkpoints))
			lstatCountBefore := len(fixture.target.lstats)
			testCase.mutate(t, &fixture, required)
			var jobBeforeResume model.BackupAssetRecoveryJob
			if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&jobBeforeResume).Error; err != nil {
				t.Fatal(err)
			}
			chainBefore := jobBeforeResume.TargetChainRevision

			resumeSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
			resumeErr := fixture.coordinator.ExecuteClaim(
				context.Background(), fixture.claim, resumeSource, deleteRequest.GrantSecret,
			)
			if resumeErr == nil {
				t.Fatal("stale exact-mirror delete authority was accepted")
			}
			if testCase.wantFreshObservation && len(fixture.target.lstats) <= lstatCountBefore {
				t.Fatalf("resume did not perform a fresh target observation: before=%d after=%d err=%v",
					lstatCountBefore, len(fixture.target.lstats), resumeErr)
			}

			var grantAfter model.BackupAssetRecoveryGrant
			if err := fixture.serviceFixture.db.Where("id = ?", issued.GrantID).Take(&grantAfter).Error; err != nil {
				t.Fatal(err)
			}
			var consumedCount int64
			if err := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
				Where("job_id = ? AND phase = ?", fixture.jobID, CheckpointPhaseDeleteAuthorityConsumed).
				Count(&consumedCount).Error; err != nil {
				t.Fatal(err)
			}
			var checkpointCountAfter int64
			if err := fixture.serviceFixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
				Where("job_id = ?", fixture.jobID).Count(&checkpointCountAfter).Error; err != nil {
				t.Fatal(err)
			}
			var jobAfter model.BackupAssetRecoveryJob
			if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&jobAfter).Error; err != nil {
				t.Fatal(err)
			}
			var itemAfter model.BackupAssetRecoveryJobItem
			if err := fixture.serviceFixture.db.Where("id = ?", itemBefore.ID).Take(&itemAfter).Error; err != nil {
				t.Fatal(err)
			}
			if grantAfter.ConsumedAt != nil || grantAfter.RevokedAt != nil || consumedCount != 0 ||
				checkpointCountAfter != checkpointCountBefore || len(fixture.target.deletes) != 0 ||
				itemAfter.Outcome != itemBefore.Outcome || itemAfter.FailureCategory != itemBefore.FailureCategory ||
				jobAfter.State != string(JobStateRunning) || jobAfter.TargetChainRevision != chainBefore {
				t.Fatalf("stale delete authority had effects: err=%v grant_before=%+v grant_after=%+v consumed=%d checkpoints=%d/%d deletes=%d item=%+v/%+v job=%+v",
					resumeErr, grantBefore, grantAfter, consumedCount, checkpointCountBefore, checkpointCountAfter,
					len(fixture.target.deletes), itemBefore, itemAfter, jobAfter)
			}
		})
	}
}

func jobPlanTransitionRevision(t *testing.T, db *gorm.DB, planID string) uint64 {
	t.Helper()
	var plan model.BackupAssetRecoveryPlan
	if err := db.Where("id = ?", planID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	return plan.TransitionRevision
}

func TestRecoveryOrdinaryExecutionMutationMatrix(t *testing.T) {
	t.Run("create overwrite skip stream checkpoint and terminate", func(t *testing.T) {
		fixture := newRecoveryExecutionFixture(t)
		executed, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
		coordinator.target = target
		claim, found, err := coordinator.ClaimNext(context.Background(), "ordinary-execution-worker")
		if err != nil || !found || claim.JobID != executed.JobID {
			t.Fatalf("claim ordinary execution: claim=%+v found=%t err=%v", claim, found, err)
		}
		var before model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
			t.Fatal(err)
		}
		executor, ok := any(coordinator).(recoveryOrdinaryClaimExecutor)
		if !ok {
			t.Fatal("WorkerCoordinator has no ordinary fenced ExecuteClaim boundary")
		}
		source := newRecoveryRepositoryContractSource(t, fixture.db, claim.JobID)
		if err := executor.ExecuteClaim(context.Background(), claim, source, ""); err != nil {
			t.Fatalf("execute create/overwrite/skip claim: %v", err)
		}
		if len(target.workspaceCalls) != 1 || len(target.writes) != 2 || len(target.deletes) != 0 || len(source.opened) != 2 {
			t.Fatalf("ordinary I/O workspace=%d writes=%d deletes=%d source_streams=%d, want 1/2/0/2",
				len(target.workspaceCalls), len(target.writes), len(target.deletes), len(source.opened))
		}
		for index, call := range target.writes {
			streamedDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(call.payload)))
			if int64(len(call.payload)) != call.request.ExpectedBytes ||
				call.request.ExpectedDigest != streamedDigest ||
				source.opened[index].ExpectedDigest != call.request.ExpectedDigest {
				t.Fatalf("streamed write %d=%+v source=%+v", index, call, source.opened[index])
			}
		}
		var items []model.BackupAssetRecoveryJobItem
		if err := fixture.db.Where("job_id = ?", claim.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			switch RecoveryOperationKind(item.OperationKind) {
			case RecoveryOperationCreate, RecoveryOperationOverwrite:
				if item.Outcome != "succeeded" || item.BytesWritten != item.ExpectedPostBytes ||
					item.VerifiedSize != item.ExpectedPostBytes || item.VerifiedDigest != item.ExpectedPostIdentityDigest {
					t.Fatalf("terminal mutating item=%+v", item)
				}
			case RecoveryOperationSkip:
				if item.Outcome != "skipped" || item.BytesWritten != 0 ||
					item.VerifiedSize != item.ExpectedPriorBytes || item.VerifiedDigest != item.ExpectedPriorDigest {
					t.Fatalf("terminal skip item=%+v", item)
				}
			default:
				t.Fatalf("unexpected operation=%q", item.OperationKind)
			}
		}
		var operationCheckpoints int64
		if err := fixture.db.Model(&model.BackupAssetRecoveryCheckpoint{}).
			Where("job_id = ? AND phase = ?", claim.JobID, CheckpointPhaseOperation).
			Count(&operationCheckpoints).Error; err != nil {
			t.Fatal(err)
		}
		var after model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", claim.JobID).Take(&after).Error; err != nil {
			t.Fatal(err)
		}
		if operationCheckpoints != 3 || after.State != "succeeded" || after.TargetChainRevision == before.TargetChainRevision {
			t.Fatalf("terminal projection checkpoints=%d job=%+v", operationCheckpoints, after)
		}
		assertRecoveryExecutionAttemptCompleted(t, fixture.db, claim.AttemptID)
	})

	t.Run("stale fence has six zero effects", func(t *testing.T) {
		fixture := newRecoveryExecutionFixture(t)
		if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
			t.Fatalf("execute recovery fixture: %v", err)
		}
		coordinator := newRecoveryWorkerCoordinator(t, fixture)
		target := &recoveryExecutionTargetFake{db: fixture.db, now: func() time.Time { return fixture.now }}
		coordinator.target = target
		claim, found, err := coordinator.ClaimNext(context.Background(), "stale-execution-worker")
		if err != nil || !found {
			t.Fatalf("claim ordinary execution: found=%t err=%v", found, err)
		}
		var before model.BackupAssetRecoveryJob
		if err := fixture.db.Where("id = ?", claim.JobID).Take(&before).Error; err != nil {
			t.Fatal(err)
		}
		executor, ok := any(coordinator).(recoveryOrdinaryClaimExecutor)
		if !ok {
			t.Fatal("WorkerCoordinator has no ordinary fenced ExecuteClaim boundary")
		}
		stale := claim
		stale.AttemptFence++
		source := &recoveryExecutionSourceFake{}
		executeErr := executor.ExecuteClaim(context.Background(), stale, source, "")
		if !errors.Is(executeErr, ErrRecoveryWorkerFenceLost) {
			t.Fatalf("stale ordinary execution error=%v, want fence loss", executeErr)
		}
		assertRecoveryExecutionZeroEffects(t, fixture.db, before, target, source, executeErr)
	})

	t.Run("exact mirror delete consumes authority and terminates", func(t *testing.T) {
		fixture := newExactMirrorOrdinaryExecutionFixture(t)
		pauseSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
		if err := fixture.coordinator.ExecuteClaim(context.Background(), fixture.claim, pauseSource, ""); err != nil {
			t.Fatalf("pause exact-mirror execution: %v", err)
		}
		var job model.BackupAssetRecoveryJob
		if err := fixture.serviceFixture.db.Where("id = ?", fixture.jobID).Take(&job).Error; err != nil {
			t.Fatal(err)
		}
		var checkpoints []model.BackupAssetRecoveryCheckpoint
		if err := fixture.serviceFixture.db.Where("job_id = ?", fixture.jobID).
			Order("sequence ASC").Find(&checkpoints).Error; err != nil {
			t.Fatal(err)
		}
		if len(checkpoints) == 0 || checkpoints[len(checkpoints)-1].Phase != string(CheckpointPhaseDeleteAuthorityRequired) {
			t.Fatalf("delete authority was not durably paused: %+v", checkpoints)
		}
		required := checkpoints[len(checkpoints)-1]
		deleteRequest := fixture.serviceFixture.request
		deleteRequest.Operation = AuthorizationReceiptDeleteAuthorize
		deleteRequest.Category = AuthorizationReceiptCategoryExactMirrorDelete
		deleteRequest.Endpoint = recoveryDeleteAuthorizationEndpoint
		deleteRequest.IdempotencyKey = "exact-mirror-ordinary-delete-key"
		deleteRequest.Proof.JTI = "FAKE_RECOVERY_ORDINARY_DELETE_PROOF_JTI"
		deleteRequest.ExpectedPlanRevision = jobPlanTransitionRevision(
			t, fixture.serviceFixture.db, job.PlanID,
		)
		deleteRequest.PreflightID = ""
		deleteRequest.JobID = fixture.jobID
		deleteRequest.CheckpointID = required.ID
		deleteRequest.AttemptID = fixture.claim.AttemptID
		deleteRequest.GrantID = ""
		deleteRequest.Reason = "FAKE_RECOVERY_ORDINARY_DELETE_REASON"
		deleteRequest.GrantSecret = mustAuthorizationReceiptSecretForFixture()
		issued, err := fixture.serviceFixture.service.Authorize(context.Background(), deleteRequest)
		if err != nil {
			t.Fatalf("authorize exact-mirror delete: %v", err)
		}

		resumeSource := newRecoveryRepositoryContractSource(t, fixture.serviceFixture.db, fixture.jobID)
		if err := fixture.coordinator.ExecuteClaim(
			context.Background(), fixture.claim, resumeSource, deleteRequest.GrantSecret,
		); err != nil {
			t.Fatalf("execute exact-mirror delete: %v", err)
		}
		if len(fixture.target.workspaceCalls) != 0 || len(fixture.target.writes) != 2 ||
			len(fixture.target.deletes) != 1 || len(resumeSource.opened) != 0 {
			t.Fatalf("exact-mirror I/O workspace=%d writes=%d deletes=%d resumed_source=%d, want 0/2/1/0",
				len(fixture.target.workspaceCalls), len(fixture.target.writes),
				len(fixture.target.deletes), len(resumeSource.opened))
		}
		var deleted model.BackupAssetRecoveryJobItem
		if err := fixture.serviceFixture.db.Where(
			"job_id = ? AND operation_kind = ?", job.ID, RecoveryOperationDelete,
		).Take(&deleted).Error; err != nil {
			t.Fatal(err)
		}
		var grant model.BackupAssetRecoveryGrant
		if err := fixture.serviceFixture.db.Where("id = ?", issued.GrantID).Take(&grant).Error; err != nil {
			t.Fatal(err)
		}
		if deleted.Outcome != "succeeded" || deleted.BytesWritten != 0 || deleted.VerifiedSize != 0 ||
			deleted.VerifiedDigest != "" || grant.ConsumedAt == nil {
			t.Fatalf("delete projection=%+v grant=%+v", deleted, grant)
		}
		if err := fixture.serviceFixture.db.Where("id = ?", job.ID).Take(&job).Error; err != nil {
			t.Fatal(err)
		}
		if job.State != "succeeded" {
			t.Fatalf("exact-mirror terminal job=%+v", job)
		}
		assertRecoveryExecutionAttemptCompleted(t, fixture.serviceFixture.db, fixture.claim.AttemptID)
	})
}

func assertRecoveryExecutionAttemptCompleted(t *testing.T, db *gorm.DB, attemptID string) {
	t.Helper()
	var attempt model.BackupAssetRecoveryAttempt
	if err := db.Where("id = ?", attemptID).Take(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt.State != string(AttemptStateCompleted) || attempt.ClosedAt == nil {
		t.Fatalf("ordinary execution attempt=%+v, want completed and closed", attempt)
	}
}

func assertRecoveryExecutionZeroEffects(
	t *testing.T,
	db *gorm.DB,
	before model.BackupAssetRecoveryJob,
	target *recoveryExecutionTargetFake,
	source *recoveryExecutionSourceFake,
	executeErr error,
) {
	t.Helper()
	var sequenceOne, terminalItems int64
	if err := db.Model(&model.BackupAssetRecoveryCheckpoint{}).
		Where("job_id = ? AND sequence >= ?", before.ID, 1).Count(&sequenceOne).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.BackupAssetRecoveryJobItem{}).
		Where("job_id = ? AND outcome IN ?", before.ID, []string{"succeeded", "skipped"}).Count(&terminalItems).Error; err != nil {
		t.Fatal(err)
	}
	var after model.BackupAssetRecoveryJob
	if err := db.Where("id = ?", before.ID).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	targetIO := len(target.workspaceCalls) + len(target.writes) + len(target.deletes)
	if sequenceOne != 0 || terminalItems != 0 || after.State == "succeeded" ||
		after.TargetChainRevision != before.TargetChainRevision || targetIO != 0 || len(source.opened) != 0 {
		t.Fatalf("negative effects checkpoints=%d items=%d job=%q chain_changed=%t target_io=%d source_io=%d",
			sequenceOne, terminalItems, after.State, after.TargetChainRevision != before.TargetChainRevision,
			targetIO, len(source.opened))
	}
	for _, raw := range []string{"items/item-0000", before.EncryptedWorkspaceRelativeLocator} {
		if raw != "" && strings.Contains(executeErr.Error(), raw) {
			t.Fatalf("negative execution leaked raw locator %q: %v", raw, executeErr)
		}
	}
}

func TestTargetChainAdvanceBindsOperationAndCurrentFences(t *testing.T) {
	input := TargetChainAdvance{
		PriorRevision:        strings.Repeat("a", 64),
		OperationDigest:      strings.Repeat("b", 64),
		PlanItemID:           strings.Repeat("c", 32),
		SourceRevisionDigest: strings.Repeat("d", 64),
		AttemptID:            strings.Repeat("e", 32),
		AttemptFence:         7,
		NodeFence:            11,
		VerifiedIdentity:     strings.Repeat("f", 64),
		TargetRevision:       strings.Repeat("1", 64),
	}

	got, err := input.NextRevision()
	if err != nil {
		t.Fatalf("advance target chain: %v", err)
	}
	const want = "1e875d5b9e69b62dac17f9b1b758f453bbcaa18c7193adb38dc3f8171840fc8c"
	if got != want {
		t.Fatalf("target-chain revision = %q, want golden %q", got, want)
	}

	mutations := []func(*TargetChainAdvance){
		func(value *TargetChainAdvance) { value.PriorRevision = strings.Repeat("e", 64) },
		func(value *TargetChainAdvance) { value.OperationDigest = strings.Repeat("e", 64) },
		func(value *TargetChainAdvance) { value.PlanItemID = strings.Repeat("f", 32) },
		func(value *TargetChainAdvance) { value.SourceRevisionDigest = strings.Repeat("e", 64) },
		func(value *TargetChainAdvance) { value.AttemptID = strings.Repeat("f", 32) },
		func(value *TargetChainAdvance) { value.AttemptFence++ },
		func(value *TargetChainAdvance) { value.NodeFence++ },
		func(value *TargetChainAdvance) { value.VerifiedIdentity = strings.Repeat("e", 64) },
		func(value *TargetChainAdvance) { value.TargetRevision = strings.Repeat("e", 64) },
	}
	for index, mutate := range mutations {
		changed := input
		mutate(&changed)
		revision, changedErr := changed.NextRevision()
		if changedErr != nil {
			t.Fatalf("mutation %d became invalid: %v", index, changedErr)
		}
		if revision == got {
			t.Fatalf("mutation %d did not change the target-chain revision", index)
		}
	}
}

func TestTargetChainAdvanceRejectsIncompleteOrStaleAuthority(t *testing.T) {
	valid := TargetChainAdvance{
		PriorRevision:        strings.Repeat("a", 64),
		OperationDigest:      strings.Repeat("b", 64),
		PlanItemID:           strings.Repeat("c", 32),
		SourceRevisionDigest: strings.Repeat("d", 64),
		AttemptID:            strings.Repeat("e", 32),
		AttemptFence:         7,
		NodeFence:            11,
		VerifiedIdentity:     strings.Repeat("f", 64),
		TargetRevision:       strings.Repeat("1", 64),
	}

	invalid := []TargetChainAdvance{
		{},
		func() TargetChainAdvance { value := valid; value.PriorRevision = ""; return value }(),
		func() TargetChainAdvance { value := valid; value.OperationDigest = "bad"; return value }(),
		func() TargetChainAdvance { value := valid; value.PlanItemID = "bad"; return value }(),
		func() TargetChainAdvance { value := valid; value.SourceRevisionDigest = "bad"; return value }(),
		func() TargetChainAdvance { value := valid; value.AttemptID = "bad"; return value }(),
		func() TargetChainAdvance { value := valid; value.AttemptFence = 0; return value }(),
		func() TargetChainAdvance { value := valid; value.NodeFence = 0; return value }(),
		func() TargetChainAdvance { value := valid; value.VerifiedIdentity = "bad"; return value }(),
		func() TargetChainAdvance { value := valid; value.TargetRevision = ""; return value }(),
	}
	for index, input := range invalid {
		if _, err := input.NextRevision(); !errors.Is(err, ErrInvalidTargetChain) {
			t.Fatalf("invalid target-chain input %d error = %v", index, err)
		}
	}
}
