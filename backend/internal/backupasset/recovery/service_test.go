package recovery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRecoveryExecutePreparedAggregateGrantFirstMatrix(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	errGrantNotFirst := errors.New("execute grant CAS was not the first effect mutation")
	callbackName := "task6:require_grant_first"
	if err := fixture.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		switch tx.Statement.Table {
		case (model.BackupAssetRecoveryJob{}).TableName(),
			(model.BackupAssetRecoveryJobItem{}).TableName(),
			(model.BackupAssetRecoveryAttempt{}).TableName(),
			(model.BackupAssetRecoveryNodeLease{}).TableName(),
			(model.RecoveryPointLease{}).TableName():
		default:
			return
		}
		var consumedAt *time.Time
		query := tx.Session(&gorm.Session{NewDB: true}).Table((model.BackupAssetRecoveryGrant{}).TableName()).
			Select("consumed_at").Where("id = ?", fixture.request.GrantID).Scan(&consumedAt)
		if query.Error != nil || consumedAt == nil {
			_ = tx.AddError(errGrantNotFirst)
		}
	}); err != nil {
		t.Fatalf("register grant-first assertion: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Create().Remove(callbackName) })

	result, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute did not consume grant before aggregate effects: %v", err)
	}

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", result.JobID).Take(&job).Error; err != nil {
		t.Fatalf("load prepared job: %v", err)
	}
	jobValue := reflect.ValueOf(job)
	workspaceBinding := jobValue.FieldByName("WorkspaceBindingDigest")
	if TargetMode(job.TargetMode) == TargetModeIsolated &&
		(job.WorkspacePhase != string(WorkspacePhaseNone) || job.EncryptedWorkspaceRelativeLocator == "" ||
			!workspaceBinding.IsValid() || !validDigest(workspaceBinding.String()) ||
			job.WorkspaceMarkerBindingDigest != "" || job.WorkspaceOwner != "" || job.WorkspaceFence != 0 || job.PlaintextDeadline != nil) {
		t.Fatalf("isolated execute did not persist a complete unreserved workspace identity: %+v", job)
	}

	var items []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", result.JobID).Order("ordinal ASC").Find(&items).Error; err != nil {
		t.Fatalf("load prepared job items: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("execute committed no prepared job items")
	}
	for _, item := range items {
		value := reflect.ValueOf(item)
		semantic := value.FieldByName("SemanticTargetDigest")
		object := value.FieldByName("TargetObjectDigest")
		if !semantic.IsValid() || !object.IsValid() || !validDigest(semantic.String()) || !validDigest(object.String()) ||
			semantic.String() == object.String() || item.EncryptedTargetRelativeLocator == "" ||
			item.TargetLocatorKeyVersion <= 0 || item.TargetLocatorCipherVersion <= 0 {
			t.Fatalf("execute committed incomplete prepared item product: %+v", item)
		}
	}

	replay, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil || !replay.Replay || replay.JobID != result.JobID || replay.AttemptID != result.AttemptID {
		t.Fatalf("execute prepared-aggregate replay=%+v error=%v, want exact committed aggregate", replay, err)
	}
	var replayItems []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", replay.JobID).Order("ordinal ASC").Find(&replayItems).Error; err != nil {
		t.Fatalf("load replayed prepared items: %v", err)
	}
	if !reflect.DeepEqual(replayItems, items) {
		t.Fatalf("execute replay replaced prepared item identity:\nfirst=%+v\nreplay=%+v", items, replayItems)
	}

	t.Run("preparation failure leaves no durable effect", func(t *testing.T) {
		failureFixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
		before := failureFixture.effectCounts(t)
		keys := newAuthorizationReceiptLocatorKeys()
		keyErr := fmt.Errorf("%w: injected prepared aggregate key failure", backupasset.ErrKeyUnavailable)
		dependencies := failureFixture.dependencies
		dependencies.LocatorKeys = &recoveryPreparedExecuteLocatorKeys{active: keys.material, activeErr: keyErr}
		service, serviceErr := NewAuthorizationService(dependencies)
		if serviceErr != nil {
			t.Fatalf("build preparation-failure authorization service: %v", serviceErr)
		}
		failureFixture.service = service

		if _, authorizeErr := failureFixture.service.Authorize(context.Background(), failureFixture.request); !errors.Is(authorizeErr, ErrAuthorizationUnavailable) {
			t.Fatalf("prepared aggregate key failure error=%v, want ErrAuthorizationUnavailable", authorizeErr)
		}
		if after := failureFixture.effectCounts(t); after != before {
			t.Fatalf("prepared aggregate failure changed durable effects: before=%+v after=%+v", before, after)
		}
		assertRecoveryExecuteGrantAndPlanUnchanged(t, failureFixture)
	})

	t.Run("transaction locks the exact prepared key before grant CAS", func(t *testing.T) {
		mismatchFixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
		before := mismatchFixture.effectCounts(t)
		keys := newAuthorizationReceiptLocatorKeys()
		locked := cloneDomainKeyMaterial(keys.material)
		locked.ID = strings.Repeat("e", 32)
		locatorKeys := &recoveryPreparedExecuteLocatorKeys{active: keys.material, locked: locked}
		dependencies := mismatchFixture.dependencies
		dependencies.LocatorKeys = locatorKeys
		service, serviceErr := NewAuthorizationService(dependencies)
		if serviceErr != nil {
			t.Fatalf("build key-mismatch authorization service: %v", serviceErr)
		}
		mismatchFixture.service = service

		if _, authorizeErr := mismatchFixture.service.Authorize(context.Background(), mismatchFixture.request); !errors.Is(authorizeErr, ErrAuthorizationUnavailable) {
			t.Fatalf("prepared aggregate locked-key mismatch error=%v, want ErrAuthorizationUnavailable", authorizeErr)
		}
		if locatorKeys.activeCalls.Load() != 1 || locatorKeys.lockCalls.Load() != 1 {
			t.Fatalf("prepared aggregate key calls active=%d lock=%d, want one each",
				locatorKeys.activeCalls.Load(), locatorKeys.lockCalls.Load())
		}
		if after := mismatchFixture.effectCounts(t); after != before {
			t.Fatalf("locked-key mismatch changed durable effects: before=%+v after=%+v", before, after)
		}
		assertRecoveryExecuteGrantAndPlanUnchanged(t, mismatchFixture)
	})

	t.Run("post-grant transaction failure rolls back grant and aggregate", func(t *testing.T) {
		rollbackFixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
		before := rollbackFixture.effectCounts(t)
		injected := errors.New("injected prepared aggregate transaction failure")
		rollbackFixture.service.beforePersist = func(stage authorizationPersistStage) error {
			if stage == authorizationPersistBeforeCommit {
				return injected
			}
			return nil
		}
		if _, authorizeErr := rollbackFixture.service.Authorize(context.Background(), rollbackFixture.request); !errors.Is(authorizeErr, injected) {
			t.Fatalf("prepared aggregate rollback error=%v, want injected failure", authorizeErr)
		}
		if after := rollbackFixture.effectCounts(t); after != before {
			t.Fatalf("prepared aggregate rollback residue: before=%+v after=%+v", before, after)
		}
		assertRecoveryExecuteGrantAndPlanUnchanged(t, rollbackFixture)
	})
}

type recoveryPreparedExecuteLocatorKeys struct {
	active      backupasset.DomainKeyMaterial
	locked      backupasset.DomainKeyMaterial
	activeErr   error
	lockErr     error
	activeCalls atomic.Int32
	lockCalls   atomic.Int32
}

func (keys *recoveryPreparedExecuteLocatorKeys) Active(
	_ context.Context,
	domain backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	if keys == nil {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	keys.activeCalls.Add(1)
	if domain != backupasset.KeyDomainRecoveryCleanupOwnership {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	if keys.activeErr != nil {
		return backupasset.DomainKeyMaterial{}, keys.activeErr
	}
	return cloneDomainKeyMaterial(keys.active), nil
}

func (keys *recoveryPreparedExecuteLocatorKeys) LockActiveTx(
	_ context.Context,
	tx *gorm.DB,
	_ backupasset.DomainKeyMaterial,
) (backupasset.DomainKeyMaterial, error) {
	if keys == nil {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	keys.lockCalls.Add(1)
	if tx == nil {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	if keys.lockErr != nil {
		return backupasset.DomainKeyMaterial{}, keys.lockErr
	}
	return cloneDomainKeyMaterial(keys.locked), nil
}

func assertRecoveryExecuteGrantAndPlanUnchanged(
	t *testing.T,
	fixture *authorizationReceiptServiceFixture,
) {
	t.Helper()
	var grant model.BackupAssetRecoveryGrant
	if err := fixture.db.Where("id = ?", fixture.request.GrantID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.ConsumedAt != nil || grant.JobID != nil {
		t.Fatalf("failed execute changed grant=%+v", grant)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if PlanState(plan.State) != PlanStateAuthorized || plan.TransitionRevision != fixture.request.ExpectedPlanRevision {
		t.Fatalf("failed execute changed plan=%+v", plan)
	}
}

func TestRecoveryAuthorizationReceiptSecurityOverrideReplayAndConflict(t *testing.T) {
	testRecoveryAuthorizationReceiptReplayAndConflict(t, AuthorizationReceiptSecurityOverride)
}

func TestRecoveryAuthorizationReceiptWriteAuthorizeReplayAndConflict(t *testing.T) {
	testRecoveryAuthorizationReceiptReplayAndConflict(t, AuthorizationReceiptWriteAuthorize)
}

func TestRecoveryAuthorizationReceiptDeleteAuthorizeReplayAndConflict(t *testing.T) {
	testRecoveryAuthorizationReceiptReplayAndConflict(t, AuthorizationReceiptDeleteAuthorize)
}

func TestRecoveryAuthorizationReceiptExecuteReplayAndConflict(t *testing.T) {
	testRecoveryAuthorizationReceiptReplayAndConflict(t, AuthorizationReceiptExecute)
}

func TestRecoveryAuthorizationReceiptReturnsStableSafeGrantMetadata(t *testing.T) {
	testCases := []struct {
		operation     AuthorizationReceiptOperation
		grantCategory AuthorityCategory
		grantStatus   string
	}{
		{operation: AuthorizationReceiptSecurityOverride},
		{operation: AuthorizationReceiptWriteAuthorize, grantCategory: AuthorityWrite, grantStatus: "issued"},
		{operation: AuthorizationReceiptDeleteAuthorize, grantCategory: AuthorityExactMirrorDelete, grantStatus: "issued"},
		{operation: AuthorizationReceiptExecute, grantCategory: AuthorityWrite, grantStatus: "consumed"},
	}

	for _, testCase := range testCases {
		t.Run(string(testCase.operation), func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, testCase.operation)
			first, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatal(err)
			}

			replayRequest := fixture.request
			replayRequest.Proof = RecoveryAuthorizationProof{}
			replay, found, err := fixture.service.ReplayAuthorization(context.Background(), replayRequest)
			if err != nil || !found || !replay.Replay {
				t.Fatalf("replay result=%+v found=%t error=%v", replay, found, err)
			}

			firstMetadata, firstJSON := authorizationResultGrantMetadata(t, first)
			replayMetadata, replayJSON := authorizationResultGrantMetadata(t, replay)
			if firstMetadata != replayMetadata {
				t.Fatalf("initial/replay grant metadata differ: initial=%+v replay=%+v", firstMetadata, replayMetadata)
			}

			if testCase.grantCategory == "" {
				if firstMetadata != (authorizationResultGrantMetadataJSON{}) {
					t.Fatalf("security override grant metadata=%+v, want empty", firstMetadata)
				}
				return
			}

			var grant model.BackupAssetRecoveryGrant
			if err := fixture.db.Where("id = ?", first.GrantID).Take(&grant).Error; err != nil {
				t.Fatal(err)
			}
			if firstMetadata.Category != string(testCase.grantCategory) ||
				firstMetadata.BindingDigest != grant.BindingDigest ||
				firstMetadata.ExpiresAt != grant.ExpiresAt.UTC().Format(time.RFC3339Nano) ||
				firstMetadata.Status != testCase.grantStatus {
				t.Fatalf("grant metadata=%+v, want category=%q binding=%q expiry=%q status=%q",
					firstMetadata, testCase.grantCategory, grant.BindingDigest,
					grant.ExpiresAt.UTC().Format(time.RFC3339Nano), testCase.grantStatus)
			}

			var stored struct {
				GrantHash       string
				EncryptedReason string
			}
			if err := fixture.db.Table(grant.TableName()).Select("grant_hash", "encrypted_reason").
				Where("id = ?", grant.ID).Take(&stored).Error; err != nil {
				t.Fatal(err)
			}
			encoded := string(firstJSON) + string(replayJSON)
			for _, forbidden := range []string{
				fixture.request.GrantSecret,
				fixture.request.Proof.JTI,
				fixture.request.Session.JTI,
				fixture.request.Reason,
				stored.GrantHash,
				stored.EncryptedReason,
			} {
				if forbidden != "" && strings.Contains(encoded, forbidden) {
					t.Fatalf("authorization result leaked %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

type authorizationResultGrantMetadataJSON struct {
	Category      string `json:"grant_category"`
	BindingDigest string `json:"grant_binding_digest"`
	ExpiresAt     string `json:"grant_expires_at"`
	Status        string `json:"grant_status"`
}

func authorizationResultGrantMetadata(
	t *testing.T,
	result RecoveryAuthorizationResult,
) (authorizationResultGrantMetadataJSON, []byte) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var metadata authorizationResultGrantMetadataJSON
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata, encoded
}

func TestRecoveryAuthorizationReceiptSecurityOverrideRequiresPersistedAllowlistedCandidate(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		categories string
		finding    SecurityFindingCategory
		wantAllow  bool
	}{
		{name: "missing candidate denies known finding", finding: SecurityFindingSuspicious},
		{name: "candidate excludes requested known finding", categories: "malware", finding: SecurityFindingSuspicious},
		{name: "candidate admits exact known finding", categories: "malware", finding: SecurityFindingMalware, wantAllow: true},
		{name: "unknown finding remains denied", categories: "malware", finding: SecurityFindingUnknown},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptSecurityOverride)
			fixture.request.FindingCategory = testCase.finding
			before := fixture.effectCounts(t)

			if testCase.categories == "" {
				if err := fixture.db.Exec(`UPDATE backup_asset_recovery_preflights
					SET security_override_candidate_digest = '', security_override_categories = ''
					WHERE id = ?`, fixture.request.PreflightID).Error; err != nil {
					t.Fatalf("clear security override candidate: %v", err)
				}
			} else {
				var preflight model.BackupAssetRecoveryPreflight
				if err := fixture.db.Where("id = ?", fixture.request.PreflightID).Take(&preflight).Error; err != nil {
					t.Fatal(err)
				}
				candidateDigest := framedDigest(
					securityOverrideCandidateDomain,
					preflight.FindingSetDigest,
					preflight.PolicyRevision,
					testCase.categories,
				)
				if err := fixture.db.Exec(`UPDATE backup_asset_recovery_preflights
					SET security_override_candidate_digest = ?, security_override_categories = ?
					WHERE id = ?`, candidateDigest, testCase.categories, fixture.request.PreflightID).Error; err != nil {
					t.Fatalf("persist security override candidate: %v", err)
				}
			}

			result, err := fixture.service.Authorize(context.Background(), fixture.request)
			if testCase.wantAllow {
				if err != nil {
					t.Fatalf("allowlisted security override error: %v", err)
				}
				if result.ReceiptID == "" {
					t.Fatal("allowlisted security override did not persist a receipt")
				}
				return
			}
			if err == nil {
				t.Fatalf("security override unexpectedly admitted finding category %q", testCase.finding)
			}
			if got := fixture.effectCounts(t); got != before {
				t.Fatalf("denied security override left durable effects: got=%+v want=%+v", got, before)
			}
		})
	}
}

func TestRecoveryAuthorizationReceiptSecurityOverrideRebindsDownstreamAuthority(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptSecurityOverride)
	var before model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&before).Error; err != nil {
		t.Fatal(err)
	}

	override, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("security override: %v", err)
	}
	var overridden model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&overridden).Error; err != nil {
		t.Fatal(err)
	}
	if overridden.SecurityDecision != string(SecurityDecisionAdminOverride) ||
		overridden.SecurityDecisionDigest == before.SecurityDecisionDigest ||
		overridden.BindingDigest == before.BindingDigest ||
		!validDigest(overridden.SecurityDecisionDigest) || !validDigest(overridden.BindingDigest) {
		t.Fatalf("override retained a stale authenticated binding: before=%+v after=%+v", before, overridden)
	}

	write := fixture.request
	write.Operation = AuthorizationReceiptWriteAuthorize
	write.Category = AuthorizationReceiptCategoryWrite
	write.Endpoint = recoveryWriteAuthorizationEndpoint
	write.ExpectedPlanRevision = override.PlanTransitionRevision
	write.IdempotencyKey = "authorization-receipt-rebound-write-key"
	write.Proof.JTI = "FAKE_RECOVERY_REBOUND_WRITE_PROOF_JTI"
	write.Reason = "FAKE_RECOVERY_REBOUND_WRITE_REASON"
	write.GrantSecret = mustAuthorizationReceiptSecret(t)
	writeResult, err := fixture.service.Authorize(context.Background(), write)
	if err != nil {
		t.Fatalf("write authorization after override: %v", err)
	}
	var grant model.BackupAssetRecoveryGrant
	if err := fixture.db.Where("id = ?", writeResult.GrantID).Take(&grant).Error; err != nil {
		t.Fatal(err)
	}
	wantGrantBinding := framedDigest(
		recoveryAuthorizationGrantBindingDomain,
		string(AuthorizationReceiptCategoryWrite),
		overridden.ID,
		overridden.BindingDigest,
		authorizationGrantSecretHash(AuthorizationReceiptCategoryWrite, overridden.ID, "", "", write.GrantSecret),
		grant.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	if grant.BindingDigest != wantGrantBinding {
		t.Fatalf("write grant binding=%q, want rebound plan binding %q", grant.BindingDigest, wantGrantBinding)
	}

	execute := write
	execute.Operation = AuthorizationReceiptExecute
	execute.Category = AuthorizationReceiptCategoryExecute
	execute.Endpoint = recoveryExecuteEndpoint
	execute.ExpectedPlanRevision = writeResult.PlanTransitionRevision
	execute.IdempotencyKey = "authorization-receipt-rebound-execute-key"
	execute.Proof.JTI = "FAKE_RECOVERY_REBOUND_EXECUTE_PROOF_JTI"
	execute.Reason = ""
	execute.GrantID = writeResult.GrantID
	executeResult, err := fixture.service.Authorize(context.Background(), execute)
	if err != nil {
		t.Fatalf("execute after override: %v", err)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", executeResult.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.PlanBindingDigest != overridden.BindingDigest ||
		job.SecurityDecisionDigest != overridden.SecurityDecisionDigest ||
		job.SecurityDecision != string(SecurityDecisionAdminOverride) {
		t.Fatalf("execute job did not bind the authenticated override: job=%+v plan=%+v", job, overridden)
	}
}

func TestAuthorizationServiceRequiresLiveAuthorityRevalidator(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	service, err := NewAuthorizationService(AuthorizationServiceDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now }, SourceLeases: fixture.dependencies.SourceLeases,
		NodeAdmission: fixture.dependencies.NodeAdmission, AuditWriter: fixture.audit,
		ReceiptReplayTTL: fixture.dependencies.ReceiptReplayTTL, WriteGrantTTL: fixture.dependencies.WriteGrantTTL,
		DeleteGrantTTL: fixture.dependencies.DeleteGrantTTL, NodeLeaseTTL: fixture.dependencies.NodeLeaseTTL,
	})
	if !errors.Is(err, ErrAuthorizationUnavailable) || service != nil {
		t.Fatalf("authorization service without a live authority revalidator = (%v, %v), want (nil, ErrAuthorizationUnavailable)", service, err)
	}
}

func TestRecoveryAuthorizationReceiptRevalidatesLiveAuthorityBeforeWriteOrExecute(t *testing.T) {
	for _, operation := range []AuthorizationReceiptOperation{
		AuthorizationReceiptWriteAuthorize,
		AuthorizationReceiptExecute,
	} {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, operation)
			revalidator := &authorizationReceiptLiveRevalidatorSpy{err: ErrAuthorizationDenied}
			fixture.dependencies.LiveRevalidator = revalidator
			service, err := NewAuthorizationService(fixture.dependencies)
			if err != nil {
				t.Fatal(err)
			}
			before := fixture.effectCounts(t)

			if _, err := service.Authorize(context.Background(), fixture.request); !errors.Is(err, ErrAuthorizationDenied) {
				t.Fatalf("live authority rejection error=%v, want ErrAuthorizationDenied", err)
			}
			if got := fixture.effectCounts(t); got != before {
				t.Fatalf("live authority rejection left durable effects: got=%+v want=%+v", got, before)
			}
			if len(revalidator.calls) != 1 {
				t.Fatalf("live authority revalidator calls=%d, want 1", len(revalidator.calls))
			}

			var plan model.BackupAssetRecoveryPlan
			var preflight model.BackupAssetRecoveryPreflight
			if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Where("id = ?", fixture.request.PreflightID).Take(&preflight).Error; err != nil {
				t.Fatal(err)
			}
			binding := revalidator.calls[0]
			if binding.Operation != operation || binding.PlanID != plan.ID ||
				binding.RepositoryID != plan.RepositoryID || binding.RecoveryPointID != plan.RecoveryPointID ||
				binding.SelectionDigest != plan.SelectionDigest || binding.SourceRevisionDigest != plan.SourceRevisionDigest ||
				binding.TargetNodeID != plan.TargetNodeID || binding.TargetRootID != plan.TargetRootID ||
				binding.RootLocatorDigest != plan.RootLocatorDigest || binding.PathDigest != plan.PathDigest ||
				binding.TargetBaseRevision != plan.TargetBaseRevision ||
				binding.CredentialScopeRevision != plan.CredentialScopeRevision ||
				binding.RootRevision != plan.RootRevision || binding.FilesystemRevision != plan.FilesystemRevision ||
				binding.CapabilityRevision != plan.CapabilityRevision ||
				binding.SecurityPolicyRevision != plan.SecurityPolicyRevision ||
				binding.SecurityFindingSetDigest != plan.SecurityFindingSetDigest ||
				binding.OperationSetDigest != plan.OperationSetDigest || binding.DeleteSetDigest != plan.DeleteSetDigest ||
				binding.PreflightID != preflight.ID || binding.PreflightRevision != preflight.Revision ||
				binding.PreflightTargetRevision != preflight.TargetRevision || binding.PreflightNodeRevision != preflight.NodeRevision {
				t.Fatalf("live authority revalidator received incomplete binding: %+v", binding)
			}
		})
	}
}

func TestRecoveryAuthorizationReceiptRevalidatesLiveAuthorityBeforeSecurityOverride(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptSecurityOverride)
	revalidator := &authorizationReceiptLiveRevalidatorSpy{err: ErrAuthorizationDenied}
	fixture.dependencies.LiveRevalidator = revalidator
	service, err := NewAuthorizationService(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	before := fixture.effectCounts(t)

	if _, err := service.Authorize(context.Background(), fixture.request); !errors.Is(err, ErrAuthorizationDenied) {
		t.Fatalf("live security override rejection error=%v, want ErrAuthorizationDenied", err)
	}
	if got := fixture.effectCounts(t); got != before {
		t.Fatalf("live security override rejection left durable effects: got=%+v want=%+v", got, before)
	}
	if len(revalidator.calls) != 1 {
		t.Fatalf("live authority revalidator calls=%d, want 1", len(revalidator.calls))
	}
	if binding := revalidator.calls[0]; binding.Operation != AuthorizationReceiptSecurityOverride ||
		binding.PlanID != fixture.request.PlanID || binding.PreflightID != fixture.request.PreflightID ||
		binding.SecurityDecision != SecurityDecisionBlock ||
		binding.SecurityFindingSetDigest == "" || binding.SecurityPolicyRevision == "" {
		t.Fatalf("live authority revalidator received incomplete security override binding: %+v", binding)
	}
}

type authorizationReceiptLiveRevalidatorSpy struct {
	calls []RecoveryAuthorityBinding
	err   error
}

func (spy *authorizationReceiptLiveRevalidatorSpy) RevalidateRecoveryAuthorityTx(
	_ context.Context,
	_ *gorm.DB,
	binding RecoveryAuthorityBinding,
) error {
	spy.calls = append(spy.calls, binding)
	return spy.err
}

func TestRecoveryAuthorizationReceiptExecutePersistsExactOperationRows(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptDeleteAuthorize)

	var ciphertext string
	if err := fixture.db.Raw(`SELECT encrypted_operation_rows FROM backup_asset_recovery_preflights
		WHERE id = ?`, fixture.request.PreflightID).Scan(&ciphertext).Error; err != nil {
		t.Fatal(err)
	}
	if !secure.IsEncrypted(ciphertext) || strings.Contains(ciphertext, "schema_version") {
		t.Fatalf("operation snapshot is not opaque ciphertext at rest: %q", ciphertext)
	}

	var preflight model.BackupAssetRecoveryPreflight
	if err := fixture.db.Where("id = ?", fixture.request.PreflightID).Take(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := decodeRecoveryOperationRows(preflight.EncryptedOperationRows)
	if err != nil {
		t.Fatalf("decode persisted operation snapshot: %v", err)
	}
	products, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode: TargetModeInPlace, ConflictPolicy: ConflictExactMirror, Operations: rows,
		Limits: RecoveryOperationLimits{
			MaxRows: len(rows), MaxItems: len(rows),
			MaxBytes: preflight.EstimatedBytes, MaxImpactRows: len(rows),
		},
	})
	if err != nil {
		t.Fatalf("rebuild persisted operation snapshot: %v", err)
	}
	if products.OperationSetDigest != preflight.OperationSetDigest ||
		products.DeleteSetDigest != preflight.DeleteSetDigest ||
		products.Impact.EstimatedItems != preflight.EstimatedItems ||
		products.Impact.EstimatedBytes != preflight.EstimatedBytes {
		t.Fatalf("rebuilt operation products=%+v do not match preflight=%+v", products.Impact, preflight)
	}

	var planItems []model.BackupAssetRecoveryPlanItem
	if err := fixture.db.Where("plan_id = ?", fixture.request.PlanID).Find(&planItems).Error; err != nil {
		t.Fatal(err)
	}
	planItemBySource := make(map[string]string, len(planItems))
	for _, item := range planItems {
		planItemBySource[item.RecoveryPointID+"\x00"+item.EntryID] = item.ID
	}
	var jobItems []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ?", fixture.request.JobID).Order("ordinal ASC").Find(&jobItems).Error; err != nil {
		t.Fatal(err)
	}
	if len(jobItems) != len(products.Rows) {
		t.Fatalf("persisted job items=%d, want exact operation rows=%d", len(jobItems), len(products.Rows))
	}
	for index, operation := range products.Rows {
		item := jobItems[index]
		if item.Ordinal != index || item.OperationKind != string(operation.Kind) ||
			item.TargetPathDigest != operation.TargetPathDigest ||
			item.ExpectedPriorKind != string(operation.ExpectedPrior.Kind) ||
			item.ExpectedPriorDigest != operation.ExpectedPrior.Digest ||
			item.DisplayClass != string(operation.DisplayClass) || item.EstimatedBytes != operation.EstimatedBytes {
			t.Fatalf("job item %d=%+v, want operation=%+v", index, item, operation)
		}
		if operation.Kind == RecoveryOperationDelete {
			if item.PlanItemID != nil {
				t.Fatalf("delete job item has plan item %q", *item.PlanItemID)
			}
			continue
		}
		key := operation.Source.AssetRef.RecoveryPointID + "\x00" + operation.Source.AssetRef.EntryID
		wantPlanItemID, found := planItemBySource[key]
		if !found || item.PlanItemID == nil || *item.PlanItemID != wantPlanItemID {
			t.Fatalf("job item %d plan item=%v, want exact source mapping %q", index, item.PlanItemID, wantPlanItemID)
		}
	}
}

func TestRecoveryAuthorizationReceiptExecuteRejectsMissingOrTamperedOperationSnapshot(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*testing.T, *authorizationReceiptServiceFixture)
	}{
		{
			name: "missing snapshot",
			mutate: func(t *testing.T, fixture *authorizationReceiptServiceFixture) {
				t.Helper()
				if err := fixture.db.Exec(`UPDATE backup_asset_recovery_preflights
					SET encrypted_operation_rows = '' WHERE id = ?`, fixture.request.PreflightID).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed snapshot",
			mutate: func(t *testing.T, fixture *authorizationReceiptServiceFixture) {
				t.Helper()
				ciphertext, err := secure.EncryptString("{")
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Exec(`UPDATE backup_asset_recovery_preflights
					SET encrypted_operation_rows = ? WHERE id = ?`, ciphertext, fixture.request.PreflightID).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "substituted valid source digest",
			mutate: func(t *testing.T, fixture *authorizationReceiptServiceFixture) {
				t.Helper()
				var preflight model.BackupAssetRecoveryPreflight
				if err := fixture.db.Where("id = ?", fixture.request.PreflightID).Take(&preflight).Error; err != nil {
					t.Fatal(err)
				}
				var planItem model.BackupAssetRecoveryPlanItem
				if err := fixture.db.Where("plan_id = ?", fixture.request.PlanID).Order("ordinal ASC").Take(&planItem).Error; err != nil {
					t.Fatal(err)
				}
				tampered := strings.Replace(preflight.EncryptedOperationRows, planItem.EntryID, strings.Repeat("f", 64), 1)
				if tampered == preflight.EncryptedOperationRows {
					t.Fatal("fixture snapshot did not contain selected source")
				}
				ciphertext, err := secure.EncryptString(tampered)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Exec(`UPDATE backup_asset_recovery_preflights
					SET encrypted_operation_rows = ? WHERE id = ?`, ciphertext, fixture.request.PreflightID).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
			before := fixture.effectCounts(t)
			testCase.mutate(t, fixture)

			if _, err := fixture.service.Authorize(context.Background(), fixture.request); err == nil {
				t.Fatal("execute accepted a missing or tampered operation snapshot")
			}
			if got := fixture.effectCounts(t); got != before {
				t.Fatalf("rejected execute left transaction residue: got=%+v want=%+v", got, before)
			}
			var plan model.BackupAssetRecoveryPlan
			if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
				t.Fatal(err)
			}
			if plan.State != string(PlanStateAuthorized) || plan.TransitionRevision != fixture.request.ExpectedPlanRevision {
				t.Fatalf("rejected execute changed plan state/revision=%s/%d", plan.State, plan.TransitionRevision)
			}
			var grant model.BackupAssetRecoveryGrant
			if err := fixture.db.Where("id = ?", fixture.request.GrantID).Take(&grant).Error; err != nil {
				t.Fatal(err)
			}
			if grant.ConsumedAt != nil {
				t.Fatal("rejected execute consumed its write grant")
			}
		})
	}
}

func TestRecoveryAuthorizationReceiptExecuteDeleteRowHasNoPlanItem(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptDeleteAuthorize)
	var deleteItems []model.BackupAssetRecoveryJobItem
	if err := fixture.db.Where("job_id = ? AND operation_kind = ?", fixture.request.JobID, RecoveryOperationDelete).
		Find(&deleteItems).Error; err != nil {
		t.Fatal(err)
	}
	if len(deleteItems) != 1 {
		t.Fatalf("delete job items=%d, want exactly one", len(deleteItems))
	}
	if deleteItems[0].PlanItemID != nil {
		t.Fatalf("delete job item has plan item %q", *deleteItems[0].PlanItemID)
	}
}

func TestRecoveryAuthorizationReceiptAuthorityTimeBindingDrift(t *testing.T) {
	testCases := []struct {
		name      string
		operation AuthorizationReceiptOperation
		column    string
		value     string
	}{
		{name: "security override finding", operation: AuthorizationReceiptSecurityOverride, column: "finding_set_digest", value: strings.Repeat("f", 64)},
		{name: "security override policy", operation: AuthorizationReceiptSecurityOverride, column: "policy_revision", value: "policy-revision-drift"},
		{name: "write source", operation: AuthorizationReceiptWriteAuthorize, column: "source_revision_digest", value: strings.Repeat("f", 64)},
		{name: "write node", operation: AuthorizationReceiptWriteAuthorize, column: "node_revision", value: "node-revision-drift"},
		{name: "write root", operation: AuthorizationReceiptWriteAuthorize, column: "root_locator_digest", value: strings.Repeat("f", 64)},
		{name: "write path", operation: AuthorizationReceiptWriteAuthorize, column: "path_digest", value: strings.Repeat("f", 64)},
		{name: "write capability", operation: AuthorizationReceiptWriteAuthorize, column: "capability_revision", value: "capability-revision-drift"},
		{name: "write policy", operation: AuthorizationReceiptWriteAuthorize, column: "policy_revision", value: "policy-revision-drift"},
		{name: "write finding", operation: AuthorizationReceiptWriteAuthorize, column: "finding_set_digest", value: strings.Repeat("f", 64)},
		{name: "write operation set", operation: AuthorizationReceiptWriteAuthorize, column: "operation_set_digest", value: strings.Repeat("f", 64)},
		{name: "write delete set", operation: AuthorizationReceiptWriteAuthorize, column: "delete_set_digest", value: strings.Repeat("f", 64)},
		{name: "execute preflight revision", operation: AuthorizationReceiptExecute, column: "revision", value: "preflight-revision-drift"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, testCase.operation)
			before := fixture.effectCounts(t)
			var beforePlan model.BackupAssetRecoveryPlan
			if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&beforePlan).Error; err != nil {
				t.Fatal(err)
			}
			query := fmt.Sprintf("UPDATE backup_asset_recovery_preflights SET %s = ? WHERE id = ?", testCase.column)
			if err := fixture.db.Exec(query, testCase.value, fixture.request.PreflightID).Error; err != nil {
				t.Fatal(err)
			}

			if _, err := fixture.service.Authorize(context.Background(), fixture.request); !errors.Is(err, ErrAuthorizationDenied) {
				t.Fatalf("authority-time drift error=%v, want ErrAuthorizationDenied", err)
			}
			if got := fixture.effectCounts(t); got != before {
				t.Fatalf("authority-time drift left effect residue: got=%+v want=%+v", got, before)
			}
			var afterPlan model.BackupAssetRecoveryPlan
			if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&afterPlan).Error; err != nil {
				t.Fatal(err)
			}
			if afterPlan.State != beforePlan.State || afterPlan.TransitionRevision != beforePlan.TransitionRevision {
				t.Fatalf("authority-time drift changed plan state/revision=%s/%d, want %s/%d",
					afterPlan.State, afterPlan.TransitionRevision, beforePlan.State, beforePlan.TransitionRevision)
			}
			if fixture.request.GrantID != "" {
				var grant model.BackupAssetRecoveryGrant
				if err := fixture.db.Where("id = ?", fixture.request.GrantID).Take(&grant).Error; err != nil {
					t.Fatal(err)
				}
				if grant.ConsumedAt != nil {
					t.Fatal("authority-time drift consumed its write grant")
				}
			}
		})
	}
}

func TestRecoveryAuthorizationReceiptAuthorityTimeSourceRevalidation(t *testing.T) {
	for _, operation := range []AuthorizationReceiptOperation{
		AuthorizationReceiptWriteAuthorize,
		AuthorizationReceiptExecute,
	} {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, operation)
			before := fixture.effectCounts(t)
			if err := fixture.db.Exec(`UPDATE recovery_points SET manifest_digest = ?
				WHERE id = (SELECT recovery_point_id FROM backup_asset_recovery_plans WHERE id = ?)`,
				strings.Repeat("f", 64), fixture.request.PlanID).Error; err != nil {
				t.Fatal(err)
			}

			if _, err := fixture.service.Authorize(context.Background(), fixture.request); !errors.Is(err, ErrAuthorizationDenied) {
				t.Fatalf("source drift error=%v, want ErrAuthorizationDenied", err)
			}
			if got := fixture.effectCounts(t); got != before {
				t.Fatalf("source drift left effect residue: got=%+v want=%+v", got, before)
			}
			if fixture.request.GrantID != "" {
				var grant model.BackupAssetRecoveryGrant
				if err := fixture.db.Where("id = ?", fixture.request.GrantID).Take(&grant).Error; err != nil {
					t.Fatal(err)
				}
				if grant.ConsumedAt != nil {
					t.Fatal("source drift consumed its write grant")
				}
			}
		})
	}
}

func TestRecoveryAuthorizationReceiptAuthorityTimeSourceRevalidationPostgres(t *testing.T) {
	fixture := newAuthorizationReceiptPostgresServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	revalidate := func() error {
		return fixture.db.Transaction(func(tx *gorm.DB) error {
			var plan model.BackupAssetRecoveryPlan
			if err := tx.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
				return err
			}
			return fixture.service.sourceValidator.RevalidatePlanTx(context.Background(), tx, plan)
		})
	}
	if err := revalidate(); err != nil {
		t.Fatalf("valid PostgreSQL source revalidation: %v", err)
	}
	if err := fixture.db.Exec(`UPDATE recovery_points SET manifest_digest = ?
		WHERE id = (SELECT recovery_point_id FROM backup_asset_recovery_plans WHERE id = ?)`,
		strings.Repeat("f", 64), fixture.request.PlanID).Error; err != nil {
		t.Fatal(err)
	}
	if err := revalidate(); err == nil {
		t.Fatal("PostgreSQL source revalidation accepted manifest drift")
	}
}

func TestSourceValidatorRevalidatePlanTxRejectsSameKeyCatalogEntrySubstitution(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		updates map[string]any
	}{
		{
			name: "canonical path",
			updates: map[string]any{
				"normalized_path": "/substituted",
				"name":            "substituted",
			},
		},
		{
			name: "non-directory type",
			updates: map[string]any{
				"entry_type": string(backupasset.CatalogEntrySymlink),
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
			var plan model.BackupAssetRecoveryPlan
			if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&plan).Error; err != nil {
				t.Fatal(err)
			}
			var item model.BackupAssetRecoveryPlanItem
			if err := fixture.db.Where("plan_id = ?", plan.ID).Order("ordinal ASC").First(&item).Error; err != nil {
				t.Fatal(err)
			}
			revalidate := func() error {
				return fixture.db.Transaction(func(tx *gorm.DB) error {
					var lockedPlan model.BackupAssetRecoveryPlan
					if err := tx.Where("id = ?", fixture.request.PlanID).Take(&lockedPlan).Error; err != nil {
						return err
					}
					return fixture.service.sourceValidator.RevalidatePlanTx(context.Background(), tx, lockedPlan)
				})
			}

			if err := revalidate(); err != nil {
				t.Fatalf("valid plan source revalidation: %v", err)
			}
			if err := fixture.db.Model(&model.CatalogEntry{}).
				Where("generation_id = ? AND recovery_point_id = ? AND entry_id = ?",
					plan.CatalogGenerationID, plan.RecoveryPointID, item.EntryID).
				Updates(testCase.updates).Error; err != nil {
				t.Fatal(err)
			}

			err := revalidate()
			if !errors.Is(err, ErrRecoverySourceChanged) {
				t.Fatalf("same-key Catalog substitution error=%v, want ErrRecoverySourceChanged", err)
			}
			if strings.Contains(err.Error(), "/substituted") {
				t.Fatalf("same-key Catalog substitution leaked a private path: %v", err)
			}
		})
	}
}

func TestRecoveryAuthorizationReceiptIntentBindsAuthorityInputs(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		column string
		value  string
	}{
		{name: "plan binding", column: "binding_digest", value: strings.Repeat("e", 64)},
		{name: "credential scope", column: "credential_scope_revision", value: "credential-revision-2"},
		{name: "root revision", column: "root_revision", value: "root-revision-2"},
		{name: "filesystem revision", column: "filesystem_revision", value: "filesystem-revision-2"},
		{name: "conflict policy", column: "conflict_policy", value: string(ConflictSkipExisting)},
		{name: "security decision", column: "security_decision_digest", value: strings.Repeat("f", 64)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
			query := fmt.Sprintf("UPDATE backup_asset_recovery_plans SET %s = ? WHERE id = ?", testCase.column)
			if err := fixture.db.Exec(query, testCase.value, fixture.request.PlanID).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
				t.Fatalf("authorize write receipt after %s drift: %v", testCase.name, err)
			}
			if digest := authorizationReceiptIntentDigest(t, fixture); digest == legacyAuthorizationReceiptIntentDigest(fixture.request) {
				t.Fatalf("authorization intent ignored %s drift", testCase.name)
			}
		})
	}

	t.Run("source revision replay", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
		first, err := fixture.service.Authorize(context.Background(), fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		before := fixture.effectCounts(t)
		if err := fixture.db.Exec(`UPDATE recovery_points SET manifest_digest = ?
			WHERE id = (SELECT recovery_point_id FROM backup_asset_recovery_plans WHERE id = ?)`,
			strings.Repeat("f", 64), fixture.request.PlanID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.service.Authorize(context.Background(), fixture.request); !errors.Is(err, ErrAuthorizationIdempotencyConflict) {
			t.Fatalf("source-drift replay error=%v, want ErrAuthorizationIdempotencyConflict", err)
		}
		if got := fixture.effectCounts(t); got != before {
			t.Fatalf("source-drift replay changed effects: got=%+v want=%+v", got, before)
		}
		if first.ReceiptID == "" {
			t.Fatal("source-drift replay fixture did not create a receipt")
		}
	})
}

func authorizationReceiptIntentDigest(t *testing.T, fixture *authorizationReceiptServiceFixture) string {
	t.Helper()
	var digest string
	if err := fixture.db.Table("backup_asset_recovery_evidence").
		Select("intent_digest").Where("kind = ?", "authorization_receipt").Limit(1).Scan(&digest).Error; err != nil {
		t.Fatal(err)
	}
	if !validDigest(digest) {
		t.Fatalf("authorization receipt intent digest=%q, want canonical digest", digest)
	}
	return digest
}

func TestRecoveryReviewF3ExecuteReplayAfterDrift(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	first, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("execute recovery fixture: %v", err)
	}
	coordinator := newRecoveryWorkerCoordinator(t, fixture)
	claim, found, err := coordinator.ClaimNext(context.Background(), "recovery-f3-replay-worker")
	if err != nil || !found {
		t.Fatalf("claim recovery job before source drift: found=%t err=%v", found, err)
	}
	var planItem model.BackupAssetRecoveryPlanItem
	if err := fixture.db.Where("plan_id = ?", fixture.request.PlanID).Order("ordinal ASC").Take(&planItem).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.CatalogEntry{}).
		Where("generation_id = ? AND recovery_point_id = ? AND entry_id = ?",
			planItem.CatalogGenerationID, planItem.RecoveryPointID, planItem.EntryID).
		Update("normalized_path", "/f3-replay-source-drift").Error; err != nil {
		t.Fatalf("introduce Catalog drift after execute receipt: %v", err)
	}
	if _, err := coordinator.PrepareFirstWrite(context.Background(), claim); !errors.Is(err, ErrRecoverySourceChanged) {
		t.Fatalf("terminalize pre-write source drift error=%v, want ErrRecoverySourceChanged", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).
		Where("id = (SELECT recovery_point_id FROM backup_asset_recovery_plans WHERE id = ?)", fixture.request.PlanID).
		Updates(map[string]any{
			"source_fingerprint": strings.Repeat("e", 64),
			"manifest_digest":    strings.Repeat("f", 64),
			"updated_at":         fixture.now.Add(time.Second),
		}).Error; err != nil {
		t.Fatalf("advance mutable RecoveryPoint facts before replay: %v", err)
	}

	before := fixture.effectCounts(t)
	replay, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("same-key execute replay after drift: %v", err)
	}
	if !replay.Replay || !replay.sameDurableEffect(first) {
		t.Fatalf("execute replay=%+v, want original durable effect %+v", replay, first)
	}
	if got := fixture.effectCounts(t); got != before {
		t.Fatalf("execute replay after drift changed effects: got=%+v want=%+v", got, before)
	}
	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", first.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if job.State != string(JobStateFailed) || job.FailureCategory != recoveryPreWriteDriftFailureCategory {
		t.Fatalf("execute replay did not retain failed pre-write-drift job: %+v", job)
	}
}

func legacyAuthorizationReceiptIntentDigest(request RecoveryAuthorizationRequest) string {
	reasonDigest := framedDigest(recoveryAuthorizationReasonDomain, request.Reason)
	secretHash := authorizationGrantSecretHash(
		AuthorizationReceiptCategoryWrite,
		request.PlanID,
		request.JobID,
		request.CheckpointID,
		request.GrantSecret,
	)
	return framedDigest(recoveryAuthorizationIntentDomain,
		string(request.Operation), string(request.Category), request.Endpoint,
		strconv.FormatUint(uint64(request.RequesterID), 10), request.PlanID, request.JobID,
		request.CheckpointID, request.GrantID, request.AttemptID,
		strconv.FormatUint(request.ExpectedPlanRevision, 10),
		request.PreflightID, string(request.FindingCategory), reasonDigest, secretHash)
}

func testRecoveryAuthorizationReceiptReplayAndConflict(t *testing.T, operation AuthorizationReceiptOperation) {
	t.Helper()
	fixture := newAuthorizationReceiptServiceFixture(t, operation)
	before := fixture.effectCounts(t)
	first, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("first %s authorization: %v", operation, err)
	}
	if first.Replay || first.ReceiptID == "" {
		t.Fatalf("first %s result replay/receipt=%t/%q, want false/nonempty", operation, first.Replay, first.ReceiptID)
	}
	afterFirst := fixture.effectCounts(t)
	if !afterFirst.addedExactlyOneEffect(operation, before) {
		t.Fatalf("first %s effect counts=%+v before=%+v", operation, afterFirst, before)
	}

	replay, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("same-intent %s replay: %v", operation, err)
	}
	if !replay.Replay || !replay.sameDurableEffect(first) {
		t.Fatalf("same-intent %s replay=%+v, want same durable effect as %+v", operation, replay, first)
	}
	if got := fixture.effectCounts(t); got != afterFirst {
		t.Fatalf("same-intent %s replay changed effects: got=%+v want=%+v", operation, got, afterFirst)
	}

	changed := fixture.request
	changed.Reason = "FAKE_RECOVERY_REASON_CHANGED_FOR_IDEMPOTENCY_CONFLICT"
	if operation == AuthorizationReceiptExecute {
		changed.GrantSecret = mustAuthorizationReceiptSecret(t)
	}
	if _, err := fixture.service.Authorize(context.Background(), changed); !errors.Is(err, ErrAuthorizationIdempotencyConflict) {
		t.Fatalf("changed-intent %s error=%v, want ErrAuthorizationIdempotencyConflict", operation, err)
	}
	if got := fixture.effectCounts(t); got != afterFirst {
		t.Fatalf("changed-intent %s collision changed effects: got=%+v want=%+v", operation, got, afterFirst)
	}
}

func TestRecoveryAuthorizationReceiptProofReuseAcrossPlanAndCategory(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptSecurityOverride)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	before := fixture.effectCounts(t)

	for _, testCase := range []struct {
		name   string
		mutate func(*RecoveryAuthorizationRequest)
	}{
		{name: "category", mutate: func(request *RecoveryAuthorizationRequest) {
			request.Operation = AuthorizationReceiptWriteAuthorize
			request.Category = AuthorizationReceiptCategoryWrite
			request.Endpoint = recoveryWriteAuthorizationEndpoint
			request.IdempotencyKey = "authorization-receipt-other-category-key"
			request.GrantSecret = mustAuthorizationReceiptSecret(t)
		}},
		{name: "plan", mutate: func(request *RecoveryAuthorizationRequest) {
			request.PlanID = fixture.clonePlan(t)
			request.IdempotencyKey = "authorization-receipt-other-plan-key"
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := fixture.request
			testCase.mutate(&request)
			if _, err := fixture.service.Authorize(context.Background(), request); !errors.Is(err, ErrAuthorizationProofUsed) {
				t.Fatalf("cross-%s proof reuse error=%v, want ErrAuthorizationProofUsed", testCase.name, err)
			}
			if got := fixture.effectCounts(t); got != before {
				t.Fatalf("cross-%s proof reuse changed effects: got=%+v want=%+v", testCase.name, got, before)
			}
		})
	}
}

func TestRecoveryAuthorizationReceiptReplayAfterProofExpiryInSameLoginSession(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	first, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.advanceTo(fixture.request.Proof.ExpiresAt.Add(time.Second))
	replay, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("same-session lost-response replay after proof expiry: %v", err)
	}
	if !replay.Replay || !replay.sameDurableEffect(first) {
		t.Fatalf("replay after proof expiry=%+v, want %+v with Replay", replay, first)
	}
}

func TestRecoveryAuthorizationReceiptReplayExpiryConflicts(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	var receipt model.BackupAssetRecoveryEvidence
	if err := fixture.db.Where("kind = ?", "authorization_receipt").Take(&receipt).Error; err != nil {
		t.Fatal(err)
	}
	if receipt.ReplayExpiresAt == nil || !receipt.ReplayExpiresAt.Before(fixture.request.Session.ExpiresAt) {
		t.Fatalf("receipt replay/session expiry = %v/%v", receipt.ReplayExpiresAt, fixture.request.Session.ExpiresAt)
	}
	fixture.advanceTo(receipt.ReplayExpiresAt.Add(time.Nanosecond))
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); !errors.Is(err, ErrAuthorizationIdempotencyConflict) {
		t.Fatalf("expired receipt replay error = %v, want idempotency conflict", err)
	}
}

func TestRecoveryAuthorizationReceiptRejectsDifferentPresentingSession(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	before := fixture.effectCounts(t)
	request := fixture.request
	request.Session.JTI = "FAKE_RECOVERY_DIFFERENT_PRESENTING_SESSION_JTI"
	if _, err := fixture.service.Authorize(context.Background(), request); !errors.Is(err, ErrAuthorizationSessionMismatch) {
		t.Fatalf("different presenting-session replay error=%v, want ErrAuthorizationSessionMismatch", err)
	}
	if got := fixture.effectCounts(t); got != before {
		t.Fatalf("different presenting session changed effects: got=%+v want=%+v", got, before)
	}
}

func TestRecoveryAuthorizationReceiptDoesNotAssertProofJTIEqualsSessionJTI(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	if fixture.request.Proof.JTI == fixture.request.Session.JTI {
		t.Fatal("fixture must use independent proof and login-session JTIs")
	}
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatalf("independent proof/session JTIs were rejected: %v", err)
	}
}

func TestRecoveryAuthorizationReceiptRejectsUncoverableProofLifetime(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	request := fixture.request
	request.Proof.ExpiresAt = request.Session.ExpiresAt.Add(time.Second)
	before := fixture.effectCounts(t)
	if _, err := fixture.service.Authorize(context.Background(), request); !errors.Is(err, ErrAuthorizationProofLifetime) {
		t.Fatalf("uncoverable proof lifetime error=%v, want ErrAuthorizationProofLifetime", err)
	}
	if got := fixture.effectCounts(t); got != before {
		t.Fatalf("uncoverable proof lifetime changed effects: got=%+v want=%+v", got, before)
	}
}

func TestRecoveryAuthorizationReceiptReaperNeverReopensLiveProof(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	databaseFuture := time.Now().UTC().Add(time.Hour)
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("kind = ?", "authorization_receipt").
		Updates(map[string]any{
			"proof_expires_at":              databaseFuture,
			"replay_expires_at":             databaseFuture,
			"presenting_session_expires_at": databaseFuture,
		}).Error; err != nil {
		t.Fatal(err)
	}
	fixture.advanceTo(fixture.request.Proof.ExpiresAt.Add(-time.Second))
	if removed, err := fixture.service.ReapAuthorizationReceipts(context.Background(), 1); err != nil || removed != 0 {
		t.Fatalf("reaper before proof expiry removed/error=%d/%v, want 0/nil", removed, err)
	}
	request := fixture.request
	request.Endpoint = recoveryExecuteEndpoint
	request.Operation = AuthorizationReceiptExecute
	request.Category = AuthorizationReceiptCategoryExecute
	request.IdempotencyKey = "authorization-receipt-live-proof-other-key"
	if _, err := fixture.service.Authorize(context.Background(), request); !errors.Is(err, ErrAuthorizationProofUsed) {
		t.Fatalf("live proof after reaper error=%v, want ErrAuthorizationProofUsed", err)
	}
}

func TestRecoveryAuthorizationReceiptReaperUsesDatabaseClock(t *testing.T) {
	t.Run("application clock ahead cannot reap early", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
		if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
			t.Fatal(err)
		}
		databaseFuture := time.Now().UTC().Add(time.Hour)
		if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
			Where("kind = ?", "authorization_receipt").
			Updates(map[string]any{
				"proof_expires_at":              databaseFuture,
				"replay_expires_at":             databaseFuture,
				"presenting_session_expires_at": databaseFuture,
			}).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Exec(`CREATE TRIGGER authorization_receipt_database_clock_guard
			BEFORE DELETE ON backup_asset_recovery_evidence
			FOR EACH ROW
			WHEN OLD.kind = 'authorization_receipt' AND OLD.replay_expires_at > CURRENT_TIMESTAMP
			BEGIN
				SELECT RAISE(ABORT, 'authorization receipt is still protected');
			END`).Error; err != nil {
			t.Fatal(err)
		}
		fixture.advanceTo(databaseFuture.Add(time.Hour))
		if removed, err := fixture.service.ReapAuthorizationReceipts(context.Background(), 1); err != nil || removed != 0 {
			t.Fatalf("application-ahead reaper removed/error=%d/%v, want 0/nil", removed, err)
		}
	})

	t.Run("application clock behind cannot retain expired receipt", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
		if _, err := fixture.service.Authorize(context.Background(), fixture.request); err != nil {
			t.Fatal(err)
		}
		databasePast := time.Now().UTC().Add(-time.Hour)
		if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
			Where("kind = ?", "authorization_receipt").
			Updates(map[string]any{
				"proof_expires_at":              databasePast,
				"replay_expires_at":             databasePast,
				"presenting_session_expires_at": databasePast,
			}).Error; err != nil {
			t.Fatal(err)
		}
		fixture.advanceTo(databasePast.Add(-time.Hour))
		if removed, err := fixture.service.ReapAuthorizationReceipts(context.Background(), 1); err != nil || removed != 1 {
			t.Fatalf("application-behind reaper removed/error=%d/%v, want 1/nil", removed, err)
		}
	})
}

func TestRecoveryGrantSecretCanonicalShape(t *testing.T) {
	valid := mustAuthorizationReceiptSecret(t)
	if len(valid) != 43 || strings.Contains(valid, "=") {
		t.Fatalf("canonical secret shape length/padding=%d/%t, want 43/false", len(valid), strings.Contains(valid, "="))
	}
	for _, invalid := range []string{
		"", " " + valid, valid + " ", valid + "=", valid[:42], valid + "A",
		strings.Repeat("!", 43), base64.StdEncoding.EncodeToString(make([]byte, 32)),
	} {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
		request := fixture.request
		request.GrantSecret = invalid
		if _, err := fixture.service.Authorize(context.Background(), request); !errors.Is(err, ErrInvalidAuthorizationGrantSecret) {
			t.Fatalf("invalid grant secret %q error=%v, want ErrInvalidAuthorizationGrantSecret", invalid, err)
		}
		if got := fixture.effectCounts(t).Receipts; got != 0 {
			t.Fatalf("invalid grant secret %q created %d receipts", invalid, got)
		}
	}
}

func TestRecoveryWriteGrantSecretLostResponseReplay(t *testing.T) {
	testRecoveryGrantSecretLostResponseReplay(t, AuthorizationReceiptWriteAuthorize)
}

func TestRecoveryDeleteGrantSecretLostResponseReplay(t *testing.T) {
	testRecoveryGrantSecretLostResponseReplay(t, AuthorizationReceiptDeleteAuthorize)
}

func testRecoveryGrantSecretLostResponseReplay(t *testing.T, operation AuthorizationReceiptOperation) {
	t.Helper()
	fixture := newAuthorizationReceiptServiceFixture(t, operation)
	first, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil || !replay.Replay || !replay.sameDurableEffect(first) {
		t.Fatalf("lost-response %s replay=%+v error=%v, want same effect", operation, replay, err)
	}
	var persisted model.BackupAssetRecoveryGrant
	if err := fixture.db.Where("id = ?", first.GrantID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.GrantHash == fixture.request.GrantSecret || !validDigest(persisted.GrantHash) {
		t.Fatalf("%s persisted raw/noncanonical grant hash %q", operation, persisted.GrantHash)
	}

	changed := fixture.request
	changed.GrantSecret = mustAuthorizationReceiptSecret(t)
	if _, err := fixture.service.Authorize(context.Background(), changed); !errors.Is(err, ErrAuthorizationIdempotencyConflict) {
		t.Fatalf("same-key changed-secret %s error=%v, want idempotency conflict", operation, err)
	}
	fixture.assertRawSecretsAbsent(t, fixture.request.GrantSecret)
}

func TestRecoveryExecuteRejectsMismatchedGrantSecret(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptExecute)
	request := fixture.request
	request.GrantSecret = mustAuthorizationReceiptSecret(t)
	before := fixture.effectCounts(t)
	if _, err := fixture.service.Authorize(context.Background(), request); !errors.Is(err, ErrAuthorizationDenied) {
		t.Fatalf("mismatched execute secret error=%v, want ErrAuthorizationDenied", err)
	}
	if got := fixture.effectCounts(t); got != before {
		t.Fatalf("mismatched execute secret changed effects: got=%+v want=%+v", got, before)
	}
}

func TestRecoveryAuthorizationReceiptRollbackBeforeCommit(t *testing.T) {
	for _, operation := range []AuthorizationReceiptOperation{
		AuthorizationReceiptSecurityOverride,
		AuthorizationReceiptWriteAuthorize,
		AuthorizationReceiptDeleteAuthorize,
		AuthorizationReceiptExecute,
	} {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newAuthorizationReceiptServiceFixture(t, operation)
			before := fixture.effectCounts(t)
			injected := errors.New("injected authorization receipt before-commit failure")
			fixture.service.beforePersist = func(stage authorizationPersistStage) error {
				if stage == authorizationPersistBeforeCommit {
					return injected
				}
				return nil
			}
			if _, err := fixture.service.Authorize(context.Background(), fixture.request); !errors.Is(err, injected) {
				t.Fatalf("%s rollback error=%v, want injected", operation, err)
			}
			if got := fixture.effectCounts(t); got != before {
				t.Fatalf("%s rollback residue=%+v before=%+v", operation, got, before)
			}
		})
	}
}

func TestRecoveryAuthorizationReceiptAuditFailureAfterCommit(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	fixture.audit.err = errors.New("FAKE_RECOVERY_AUDIT_SINK_FAILURE")
	result, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("post-commit audit failure changed response: %v", err)
	}
	if result.ReceiptID == "" || result.GrantID == "" {
		t.Fatalf("post-commit audit failure lost durable effect: %+v", result)
	}
	before := fixture.effectCounts(t)
	replay, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil || !replay.Replay || !replay.sameDurableEffect(result) {
		t.Fatalf("post-audit-failure replay=%+v error=%v", replay, err)
	}
	if got := fixture.effectCounts(t); got != before {
		t.Fatalf("post-audit-failure replay duplicated effect: got=%+v want=%+v", got, before)
	}
}

func TestRecoveryAuthorizationReceiptAuditUsesBoundedDetachedContext(t *testing.T) {
	const maxAuditTimeout = 5 * time.Second
	spy := &authorizationReceiptAuditContextSpy{}
	service := &AuthorizationService{auditWriter: spy}
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	service.writeAuthorizationAudit(parent, RecoveryAuthorizationRequest{
		RequesterID: 7,
		Operation:   AuthorizationReceiptWriteAuthorize,
		Session:     RecoveryAuthorizationSession{Role: "admin"},
	}, RecoveryAuthorizationResult{})
	if spy.contextErr != nil {
		t.Fatalf("detached audit inherited request cancellation: %v", spy.contextErr)
	}
	if !spy.hasDeadline || spy.deadlineRemaining <= 0 || spy.deadlineRemaining > maxAuditTimeout {
		t.Fatalf("audit deadline present/remaining=%v/%s, want positive and <= %s",
			spy.hasDeadline, spy.deadlineRemaining, maxAuditTimeout)
	}
}

type authorizationReceiptAuditContextSpy struct {
	hasDeadline       bool
	deadlineRemaining time.Duration
	contextErr        error
}

func (spy *authorizationReceiptAuditContextSpy) Write(
	ctx context.Context,
	_ backupasset.AuditEventInput,
) (model.BackupAssetAuditEvent, error) {
	deadline, ok := ctx.Deadline()
	spy.hasDeadline = ok
	spy.deadlineRemaining = time.Until(deadline)
	spy.contextErr = ctx.Err()
	return model.BackupAssetAuditEvent{}, nil
}

func TestRecoveryAuthorizationReceiptConcurrentSQLiteWinner(t *testing.T) {
	t.Run("SameIntentReplay", func(t *testing.T) {
		fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
		first, second := raceAuthorizationReceiptRequests(t, fixture.service, fixture.request, fixture.request)
		if first.err != nil || second.err != nil {
			t.Fatalf("concurrent same-intent errors=%v/%v", first.err, second.err)
		}
		if !first.result.sameDurableEffect(second.result) || first.result.Replay == second.result.Replay {
			t.Fatalf("concurrent same-intent results=%+v/%+v, want one winner and one replay", first.result, second.result)
		}
		if got := fixture.effectCounts(t); got.Receipts != 1 {
			t.Fatalf("concurrent same-intent receipts=%d, want 1", got.Receipts)
		}
	})
	t.Run("CrossPlanProofReuse", func(t *testing.T) {
		testRecoveryAuthorizationReceiptConcurrentCrossPlanProofReuse(t, newAuthorizationReceiptServiceFixture)
	})
	t.Run("CrossCategoryProofReuse", func(t *testing.T) {
		testRecoveryAuthorizationReceiptConcurrentCrossCategoryProofReuse(t, newAuthorizationReceiptServiceFixture)
	})
}

func TestRecoveryAuthorizationReceiptConcurrentPostgresWinner(t *testing.T) {
	t.Run("SameIntentReplay", func(t *testing.T) {
		fixture := newAuthorizationReceiptPostgresServiceFixture(t, AuthorizationReceiptWriteAuthorize)
		first, second := raceAuthorizationReceiptRequests(t, fixture.service, fixture.request, fixture.request)
		if first.err != nil || second.err != nil {
			t.Fatalf("PostgreSQL concurrent same-intent errors=%v/%v", first.err, second.err)
		}
		if !first.result.sameDurableEffect(second.result) || first.result.Replay == second.result.Replay {
			t.Fatalf("PostgreSQL concurrent results=%+v/%+v, want one winner and one replay", first.result, second.result)
		}
		if got := fixture.effectCounts(t); got.Receipts != 1 || got.Grants != 1 {
			t.Fatalf("PostgreSQL concurrent durable receipt/grant counts=%d/%d, want 1/1", got.Receipts, got.Grants)
		}
	})
	t.Run("CrossPlanProofReuse", func(t *testing.T) {
		testRecoveryAuthorizationReceiptConcurrentCrossPlanProofReuse(t, newAuthorizationReceiptPostgresServiceFixture)
	})
	t.Run("CrossCategoryProofReuse", func(t *testing.T) {
		testRecoveryAuthorizationReceiptConcurrentCrossCategoryProofReuse(t, newAuthorizationReceiptPostgresServiceFixture)
	})
}

type authorizationReceiptFixtureFactory func(
	*testing.T,
	AuthorizationReceiptOperation,
) *authorizationReceiptServiceFixture

func raceAuthorizationReceiptRequests(
	t *testing.T,
	service *AuthorizationService,
	requests ...RecoveryAuthorizationRequest,
) (authorizationReceiptConcurrentResult, authorizationReceiptConcurrentResult) {
	t.Helper()
	if len(requests) != 2 {
		t.Fatalf("authorization receipt race requires exactly two requests, got %d", len(requests))
	}
	start := make(chan struct{})
	results := make(chan authorizationReceiptConcurrentResult, 2)
	for _, request := range requests {
		request := request
		go func() {
			<-start
			result, err := service.Authorize(context.Background(), request)
			results <- authorizationReceiptConcurrentResult{result: result, err: err}
		}()
	}
	close(start)
	return <-results, <-results
}

func testRecoveryAuthorizationReceiptConcurrentCrossPlanProofReuse(
	t *testing.T,
	newFixture authorizationReceiptFixtureFactory,
) {
	t.Helper()
	fixture := newFixture(t, AuthorizationReceiptWriteAuthorize)
	planID, preflightID := fixture.cloneAuthorizationPlan(t)
	secondRequest := fixture.request
	secondRequest.PlanID = planID
	secondRequest.PreflightID = preflightID
	secondRequest.IdempotencyKey = "authorization-receipt-cross-plan-key-0002"

	first, second := raceAuthorizationReceiptRequests(t, fixture.service, fixture.request, secondRequest)
	assertAuthorizationReceiptProofRace(t, first, second)
	if got := fixture.effectCounts(t); got.Receipts != 1 || got.Grants != 1 {
		t.Fatalf("cross-plan proof race durable receipt/grant counts=%d/%d, want 1/1", got.Receipts, got.Grants)
	}
}

func testRecoveryAuthorizationReceiptConcurrentCrossCategoryProofReuse(
	t *testing.T,
	newFixture authorizationReceiptFixtureFactory,
) {
	t.Helper()
	fixture := newFixture(t, AuthorizationReceiptWriteAuthorize)
	planID, preflightID := fixture.cloneAuthorizationPlan(t)

	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", planID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	plan.SecurityDecision = string(SecurityDecisionBlock)
	plan.SecurityDecisionDigest = strings.Repeat("f", 64)
	if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).Where("id = ?", plan.ID).
		Updates(map[string]any{
			"security_decision":        plan.SecurityDecision,
			"security_decision_digest": plan.SecurityDecisionDigest,
		}).Error; err != nil {
		t.Fatal(err)
	}
	candidateDigest, candidateCategories := authorizationReceiptOverrideCandidate(
		plan,
		AuthorizationReceiptSecurityOverride,
	)
	if err := fixture.db.Model(&model.BackupAssetRecoveryPreflight{}).
		Where("id = ? AND plan_id = ?", preflightID, plan.ID).
		Updates(map[string]any{
			"security_override_candidate_digest": candidateDigest,
			"security_override_categories":       candidateCategories,
		}).Error; err != nil {
		t.Fatal(err)
	}
	var preflight model.BackupAssetRecoveryPreflight
	if err := fixture.db.Where("id = ? AND plan_id = ?", preflightID, plan.ID).
		Take(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	securityRequest := fixture.newRequest(AuthorizationReceiptSecurityOverride, plan, preflight)
	securityRequest.IdempotencyKey = "authorization-receipt-cross-category-key-0002"

	first, second := raceAuthorizationReceiptRequests(t, fixture.service, fixture.request, securityRequest)
	assertAuthorizationReceiptProofRace(t, first, second)
	counts := fixture.effectCounts(t)
	var overrideCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).
		Where("id = ? AND security_decision = ?", plan.ID, SecurityDecisionAdminOverride).
		Count(&overrideCount).Error; err != nil {
		t.Fatal(err)
	}
	if counts.Receipts != 1 || counts.Grants+overrideCount != 1 {
		t.Fatalf("cross-category proof race receipts/grants/overrides=%d/%d/%d, want 1 and one effect",
			counts.Receipts, counts.Grants, overrideCount)
	}
}

func assertAuthorizationReceiptProofRace(
	t *testing.T,
	first authorizationReceiptConcurrentResult,
	second authorizationReceiptConcurrentResult,
) {
	t.Helper()
	results := []authorizationReceiptConcurrentResult{first, second}
	var successCount, proofUsedCount int
	for _, result := range results {
		switch {
		case result.err == nil:
			successCount++
			if result.result.Replay {
				t.Fatalf("cross-boundary proof race returned replay instead of a new winner: %+v", result.result)
			}
		case errors.Is(result.err, ErrAuthorizationProofUsed):
			proofUsedCount++
		default:
			t.Fatalf("cross-boundary proof race returned unexpected error: %v", result.err)
		}
	}
	if successCount != 1 || proofUsedCount != 1 {
		t.Fatalf("cross-boundary proof race success/proof-used=%d/%d, want 1/1", successCount, proofUsedCount)
	}
}

func TestRecoveryAuthorizationReceiptRollbackBeforeCommitPostgres(t *testing.T) {
	for _, operation := range []AuthorizationReceiptOperation{
		AuthorizationReceiptSecurityOverride,
		AuthorizationReceiptWriteAuthorize,
		AuthorizationReceiptDeleteAuthorize,
		AuthorizationReceiptExecute,
	} {
		t.Run(string(operation), func(t *testing.T) {
			fixture := newAuthorizationReceiptPostgresServiceFixture(t, operation)
			before := fixture.effectCounts(t)
			injected := errors.New("injected PostgreSQL authorization receipt rollback failure")
			fixture.service.beforePersist = func(stage authorizationPersistStage) error {
				if stage == authorizationPersistBeforeCommit {
					return injected
				}
				return nil
			}
			if _, err := fixture.service.Authorize(context.Background(), fixture.request); !errors.Is(err, injected) {
				t.Fatalf("PostgreSQL %s rollback error=%v, want injected", operation, err)
			}
			if got := fixture.effectCounts(t); got != before {
				t.Fatalf("PostgreSQL %s rollback residue=%+v before=%+v", operation, got, before)
			}
		})
	}
}

func TestRecoveryAuthorizationReceiptReaperProgressAndRestart(t *testing.T) {
	fixture := newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize)
	type authorizationTarget struct{ planID, preflightID string }
	targets := []authorizationTarget{{fixture.request.PlanID, fixture.request.PreflightID}}
	for range 2 {
		planID, preflightID := fixture.cloneAuthorizationPlan(t)
		targets = append(targets, authorizationTarget{planID: planID, preflightID: preflightID})
	}
	for index := 0; index < 3; index++ {
		request := fixture.request
		request.PlanID = targets[index].planID
		request.PreflightID = targets[index].preflightID
		request.IdempotencyKey = fmt.Sprintf("authorization-reaper-key-%d", index)
		request.Proof.JTI = fmt.Sprintf("FAKE_RECOVERY_REAPER_PROOF_JTI_%02d", index)
		if _, err := fixture.service.Authorize(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	fixture.insertProtectedEvidenceRows(t)
	fixture.advanceTo(fixture.request.Session.ExpiresAt.Add(time.Second))
	if removed, err := fixture.service.ReapAuthorizationReceipts(context.Background(), 1); err != nil || removed != 1 {
		t.Fatalf("first bounded reaper removed/error=%d/%v, want 1/nil", removed, err)
	}
	restarted := fixture.restartService(t)
	removed, err := restarted.ReapAuthorizationReceipts(context.Background(), 2)
	if err != nil || removed != 2 {
		t.Fatalf("restart bounded reaper removed/error=%d/%v, want 2/nil", removed, err)
	}
	fixture.assertProtectedEvidenceRows(t)
}

func TestRecoveryAuthorizationReceiptReaperSkipsLiveGrantBeforeLimit(t *testing.T) {
	testRecoveryAuthorizationReceiptReaperSkipsLiveGrantBeforeLimit(
		t,
		newAuthorizationReceiptServiceFixture(t, AuthorizationReceiptWriteAuthorize),
	)
}

func TestRecoveryAuthorizationReceiptReaperSkipsLiveGrantBeforeLimitPostgres(t *testing.T) {
	testRecoveryAuthorizationReceiptReaperSkipsLiveGrantBeforeLimit(
		t,
		newAuthorizationReceiptPostgresServiceFixture(t, AuthorizationReceiptWriteAuthorize),
	)
}

func TestRecoveryAuthorizationReceiptReaperHandlesAllEffectKinds(t *testing.T) {
	testRecoveryAuthorizationReceiptReaperHandlesAllEffectKinds(t, false)
}

func TestRecoveryAuthorizationReceiptReaperHandlesAllEffectKindsPostgres(t *testing.T) {
	testRecoveryAuthorizationReceiptReaperHandlesAllEffectKinds(t, true)
}

func testRecoveryAuthorizationReceiptReaperHandlesAllEffectKinds(t *testing.T, postgres bool) {
	t.Helper()
	operations := []AuthorizationReceiptOperation{
		AuthorizationReceiptSecurityOverride,
		AuthorizationReceiptWriteAuthorize,
		AuthorizationReceiptExecute,
		AuthorizationReceiptDeleteAuthorize,
	}
	for _, operation := range operations {
		t.Run(string(operation), func(t *testing.T) {
			var fixture *authorizationReceiptServiceFixture
			if postgres {
				fixture = newAuthorizationReceiptPostgresServiceFixture(t, operation)
			} else {
				fixture = newAuthorizationReceiptServiceFixture(t, operation)
			}
			result, err := fixture.service.Authorize(context.Background(), fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			expired := time.Now().UTC().Add(-time.Hour)
			if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
				Where("id = ?", result.ReceiptID).
				Updates(map[string]any{
					"proof_expires_at":              expired.Add(-2 * time.Hour),
					"replay_expires_at":             expired,
					"presenting_session_expires_at": expired.Add(time.Hour),
				}).Error; err != nil {
				t.Fatal(err)
			}
			if result.GrantID != "" {
				if err := fixture.db.Model(&model.BackupAssetRecoveryGrant{}).
					Where("id = ?", result.GrantID).
					Update("expires_at", expired).Error; err != nil {
					t.Fatal(err)
				}
			}
			removed, err := fixture.service.ReapAuthorizationReceipts(context.Background(), 1000)
			if err != nil {
				t.Fatal(err)
			}
			if removed < 1 {
				t.Fatalf("reaper removed %d rows, want target %s receipt", removed, operation)
			}
			var count int64
			if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
				Where("id = ?", result.ReceiptID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("expired %s receipt remains after reaper", operation)
			}
		})
	}
}

func testRecoveryAuthorizationReceiptReaperSkipsLiveGrantBeforeLimit(
	t *testing.T,
	fixture *authorizationReceiptServiceFixture,
) {
	t.Helper()
	planID, preflightID := fixture.cloneAuthorizationPlan(t)
	protected, err := fixture.service.Authorize(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	eligibleRequest := fixture.request
	eligibleRequest.PlanID = planID
	eligibleRequest.PreflightID = preflightID
	eligibleRequest.ExpectedPlanRevision = fixture.request.ExpectedPlanRevision
	eligibleRequest.IdempotencyKey = "authorization-reaper-eligible-key"
	eligibleRequest.Proof.JTI = "FAKE_RECOVERY_REAPER_ELIGIBLE_PROOF_JTI"
	eligible, err := fixture.service.Authorize(context.Background(), eligibleRequest)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a stale receipt whose grant is still live. This is not
	// constructible through the paired constraints, but the reaper must fail
	// closed if it encounters historical or manually-corrupted state.
	databaseNow := time.Now().UTC()
	protectedProofExpiry := databaseNow.Add(-3 * time.Hour)
	protectedReplayExpiry := databaseNow.Add(-2 * time.Hour)
	protectedSessionExpiry := databaseNow.Add(-time.Hour)
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", protected.ReceiptID).
		Updates(map[string]any{
			"proof_expires_at":              protectedProofExpiry,
			"replay_expires_at":             protectedReplayExpiry,
			"presenting_session_expires_at": protectedSessionExpiry,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryGrant{}).
		Where("id = ?", protected.GrantID).
		Update("expires_at", databaseNow.Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}

	eligibleProofExpiry := databaseNow.Add(-3 * time.Hour)
	eligibleReplayExpiry := databaseNow.Add(-time.Hour)
	eligibleSessionExpiry := databaseNow.Add(-30 * time.Minute)
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", eligible.ReceiptID).
		Updates(map[string]any{
			"proof_expires_at":              eligibleProofExpiry,
			"replay_expires_at":             eligibleReplayExpiry,
			"presenting_session_expires_at": eligibleSessionExpiry,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryGrant{}).
		Where("id = ?", eligible.GrantID).
		Update("expires_at", databaseNow.Add(-2*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}

	removed, err := fixture.service.ReapAuthorizationReceipts(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("reaper removed %d receipts, want exactly the eligible row", removed)
	}

	var protectedCount, eligibleCount int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", protected.ReceiptID).Count(&protectedCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).
		Where("id = ?", eligible.ReceiptID).Count(&eligibleCount).Error; err != nil {
		t.Fatal(err)
	}
	if protectedCount != 1 || eligibleCount != 0 {
		t.Fatalf("reaper counts protected/eligible=%d/%d, want 1/0", protectedCount, eligibleCount)
	}
}

type authorizationReceiptConcurrentResult struct {
	result RecoveryAuthorizationResult
	err    error
}

func mustAuthorizationReceiptSecret(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("generate authorization receipt test secret: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

var recoverySourceValidatorDBSequence atomic.Uint64

type recoverySourceFixture struct {
	db                  *gorm.DB
	repositoryID        string
	recoveryPointID     string
	catalogGenerationID string
	directoryRef        backupasset.AssetRef
	nestedDirectoryRef  backupasset.AssetRef
	fileRefs            []backupasset.AssetRef
	foreignRef          backupasset.AssetRef
	fakeLocator         string
	manifestDigest      string
	sourceFingerprint   string
	observedAt          time.Time
	provider            backupasset.ProviderKind
}

type exactRecoverySourceConsumerSpy struct {
	calls   int
	sources []publication.ExactRecoverySource
	err     error
}

type exactRecoverySourceConsumerFunc func(context.Context, publication.ExactRecoverySource) error

func (consume exactRecoverySourceConsumerFunc) ConsumeExactRecoverySource(
	ctx context.Context,
	source publication.ExactRecoverySource,
) error {
	return consume(ctx, source)
}

func (spy *exactRecoverySourceConsumerSpy) ConsumeExactRecoverySource(_ context.Context, source publication.ExactRecoverySource) error {
	spy.calls++
	spy.sources = append(spy.sources, source)
	return spy.err
}

func TestSourceValidatorFreezesDirectorySelectionWithExactRevision(t *testing.T) {
	tests := []struct {
		name      string
		semantics backupasset.PointVersionSemantics
	}{
		{name: "immutable point binds locator and manifest", semantics: backupasset.PointNativeSnapshot},
		{name: "mutable head binds fingerprint generation and observation", semantics: backupasset.PointMutableHead},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := seedRecoverySourceFixture(t, testCase.semantics)
			validator, err := NewSourceValidator(fixture.db)
			if err != nil {
				t.Fatalf("NewSourceValidator() error = %v", err)
			}

			selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
				RepositoryID:        fixture.repositoryID,
				RecoveryPointID:     fixture.recoveryPointID,
				CatalogGenerationID: fixture.catalogGenerationID,
				AssetRefs:           []backupasset.AssetRef{fixture.directoryRef},
				MaxItems:            len(fixture.fileRefs),
			})
			if err != nil {
				t.Fatalf("FreezeSelection() error = %v", err)
			}
			if got, want := fmt.Sprint(selection.AssetRefs), fmt.Sprint(fixture.fileRefs); got != want {
				t.Fatalf("expanded refs = %s, want %s", got, want)
			}
			if selection.SelectionDigest == "" || selection.SourceRevisionDigest == "" {
				t.Fatalf("selection did not freeze both digests: %#v", selection)
			}

			switch testCase.semantics {
			case backupasset.PointNativeSnapshot:
				wantLocatorDigest, digestErr := SourceLocatorDigest(
					fixture.repositoryID, fixture.provider, fixture.recoveryPointID, fixture.fakeLocator,
				)
				if digestErr != nil {
					t.Fatal(digestErr)
				}
				if selection.SourceRevision.Kind != SourceRevisionImmutable || selection.SourceRevision.Immutable == nil ||
					selection.SourceRevision.Immutable.LocatorDigest != wantLocatorDigest ||
					selection.SourceRevision.Immutable.ManifestDigest != fixture.manifestDigest {
					t.Fatalf("immutable revision = %#v, want locator+manifest binding", selection.SourceRevision)
				}
			case backupasset.PointMutableHead:
				if selection.SourceRevision.Kind != SourceRevisionObservation || selection.SourceRevision.MutableObservation == nil ||
					selection.SourceRevision.MutableObservation.SourceFingerprint != fixture.sourceFingerprint ||
					selection.SourceRevision.MutableObservation.CatalogGenerationID != fixture.catalogGenerationID ||
					!selection.SourceRevision.MutableObservation.ObservedAt.Equal(fixture.observedAt) {
					t.Fatalf("mutable revision = %#v, want fingerprint+generation+observed-at", selection.SourceRevision)
				}
			}
		})
	}
}

func TestSourceValidatorBoundsDirectoryExpansionAndRejectsCrossGeneration(t *testing.T) {
	tests := []struct {
		name       string
		maxItems   int
		foreignRef bool
		wantErr    bool
	}{
		{name: "bounded exact directory expansion", maxItems: 2},
		{name: "directory expansion over bound", maxItems: 1, wantErr: true},
		{name: "entry from another generation", maxItems: 2, foreignRef: true, wantErr: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := seedRecoverySourceFixture(t, backupasset.PointNativeSnapshot)
			validator, err := NewSourceValidator(fixture.db)
			if err != nil {
				t.Fatalf("NewSourceValidator() error = %v", err)
			}
			refs := []backupasset.AssetRef{fixture.directoryRef}
			if testCase.foreignRef {
				refs = []backupasset.AssetRef{fixture.foreignRef}
			}

			selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
				RepositoryID:        fixture.repositoryID,
				RecoveryPointID:     fixture.recoveryPointID,
				CatalogGenerationID: fixture.catalogGenerationID,
				AssetRefs:           refs,
				MaxItems:            testCase.maxItems,
			})
			if (err != nil) != testCase.wantErr {
				t.Fatalf("FreezeSelection() error = %v, wantErr %t", err, testCase.wantErr)
			}
			if !testCase.wantErr && fmt.Sprint(selection.AssetRefs) != fmt.Sprint(fixture.fileRefs) {
				t.Fatalf("expanded refs = %#v, want %#v", selection.AssetRefs, fixture.fileRefs)
			}
		})
	}
}

func TestSourceValidatorDirectoryExpansionIsByteExact(t *testing.T) {
	fixture := seedRecoverySourceFixture(t, backupasset.PointNativeSnapshot)
	rootID := fixture.directoryRef.EntryID
	entryID := func(value string) string { return strings.Repeat(value, 64/len(value)) }

	type directoryCase struct {
		name        string
		directoryID string
		path        string
		fileID      string
		filePath    string
		lookalikeID string
		lookalike   string
	}
	cases := []directoryCase{
		{
			name:        "case-sensitive descendant",
			directoryID: entryID("6"),
			path:        "/case",
			fileID:      entryID("7"),
			filePath:    "/case/file",
			lookalikeID: entryID("8"),
			lookalike:   "/Case/file",
		},
		{
			name:        "literal percent",
			directoryID: entryID("9"),
			path:        "/literal%",
			fileID:      entryID("a"),
			filePath:    "/literal%/file",
			lookalikeID: entryID("b"),
			lookalike:   "/literalX/file",
		},
		{
			name:        "literal underscore",
			directoryID: entryID("c"),
			path:        "/literal_",
			fileID:      entryID("e"),
			filePath:    "/literal_/file",
			lookalikeID: entryID("f"),
			lookalike:   "/literalX/file",
		},
		{
			name:        "literal backslash",
			directoryID: entryID("0"),
			path:        "/literal\\segment",
			fileID:      entryID("01"),
			filePath:    "/literal\\segment/file",
			lookalikeID: entryID("02"),
			lookalike:   "/literalssegment/file",
		},
	}

	entries := make([]model.CatalogEntry, 0, len(cases)*3)
	for _, testCase := range cases {
		directoryID := testCase.directoryID
		entries = append(entries,
			model.CatalogEntry{
				GenerationID: fixture.catalogGenerationID, EntryID: directoryID, RecoveryPointID: fixture.recoveryPointID,
				ParentEntryID: &rootID, NormalizedPath: testCase.path, Name: testCase.name,
				EntryType: string(backupasset.CatalogEntryDirectory),
			},
			model.CatalogEntry{
				GenerationID: fixture.catalogGenerationID, EntryID: testCase.fileID, RecoveryPointID: fixture.recoveryPointID,
				ParentEntryID: &directoryID, NormalizedPath: testCase.filePath, Name: "file",
				EntryType: string(backupasset.CatalogEntryFile), Size: 1,
			},
			model.CatalogEntry{
				GenerationID: fixture.catalogGenerationID, EntryID: testCase.lookalikeID, RecoveryPointID: fixture.recoveryPointID,
				ParentEntryID: &rootID, NormalizedPath: testCase.lookalike, Name: "lookalike",
				EntryType: string(backupasset.CatalogEntryFile), Size: 1,
			},
		)
	}
	if err := fixture.db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}

	validator, err := NewSourceValidator(fixture.db)
	if err != nil {
		t.Fatalf("NewSourceValidator() error = %v", err)
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
				RepositoryID: fixture.repositoryID, RecoveryPointID: fixture.recoveryPointID,
				CatalogGenerationID: fixture.catalogGenerationID,
				AssetRefs: []backupasset.AssetRef{{
					RecoveryPointID: fixture.recoveryPointID,
					EntryID:         testCase.directoryID,
				}},
				MaxItems: 2,
			})
			if err != nil {
				t.Fatalf("FreezeSelection() error = %v", err)
			}
			want := []backupasset.AssetRef{{RecoveryPointID: fixture.recoveryPointID, EntryID: testCase.fileID}}
			if got := fmt.Sprint(selection.AssetRefs); got != fmt.Sprint(want) {
				t.Fatalf("expanded refs = %s, want only byte-exact descendant %s", got, fmt.Sprint(want))
			}
		})
	}
}

func TestSourceValidatorRejectsDuplicateAndOverlappingAssetRefs(t *testing.T) {
	tests := []struct {
		name string
		refs func(recoverySourceFixture) []backupasset.AssetRef
	}{
		{
			name: "duplicate explicit asset ref",
			refs: func(fixture recoverySourceFixture) []backupasset.AssetRef {
				return []backupasset.AssetRef{fixture.fileRefs[0], fixture.fileRefs[0]}
			},
		},
		{
			name: "directory and direct item overlap",
			refs: func(fixture recoverySourceFixture) []backupasset.AssetRef {
				return []backupasset.AssetRef{fixture.directoryRef, fixture.fileRefs[0]}
			},
		},
		{
			name: "two directory expansions overlap",
			refs: func(fixture recoverySourceFixture) []backupasset.AssetRef {
				return []backupasset.AssetRef{fixture.directoryRef, fixture.nestedDirectoryRef}
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := seedRecoverySourceFixture(t, backupasset.PointNativeSnapshot)
			validator, err := NewSourceValidator(fixture.db)
			if err != nil {
				t.Fatalf("NewSourceValidator() error = %v", err)
			}
			_, err = validator.FreezeSelection(context.Background(), SourceSelectionRequest{
				RepositoryID: fixture.repositoryID, RecoveryPointID: fixture.recoveryPointID,
				CatalogGenerationID: fixture.catalogGenerationID, AssetRefs: testCase.refs(fixture), MaxItems: 3,
			})
			if !errors.Is(err, ErrInvalidExactSelection) {
				t.Fatalf("FreezeSelection() error = %v, want ErrInvalidExactSelection", err)
			}
		})
	}
}

func TestSourceValidatorRevalidatesExactSourceBeforeProviderIO(t *testing.T) {
	fixture := seedRecoverySourceFixture(t, backupasset.PointNativeSnapshot)
	validator, err := NewSourceValidator(fixture.db)
	if err != nil {
		t.Fatalf("NewSourceValidator() error = %v", err)
	}
	selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: fixture.repositoryID, RecoveryPointID: fixture.recoveryPointID,
		CatalogGenerationID: fixture.catalogGenerationID, AssetRefs: []backupasset.AssetRef{fixture.directoryRef},
		MaxItems: len(fixture.fileRefs),
	})
	if err != nil {
		t.Fatalf("FreezeSelection() error = %v", err)
	}

	t.Run("current tuple passes the exact locator only to the provider consumer", func(t *testing.T) {
		consumer := &exactRecoverySourceConsumerSpy{}
		if err := validator.Revalidate(context.Background(), selection, consumer); err != nil {
			t.Fatalf("Revalidate() error = %v", err)
		}
		if consumer.calls != 1 || len(consumer.sources) != 1 {
			t.Fatalf("provider calls = %d, sources = %#v", consumer.calls, consumer.sources)
		}
		wantLocatorDigest, digestErr := SourceLocatorDigest(
			fixture.repositoryID, fixture.provider, fixture.recoveryPointID, fixture.fakeLocator,
		)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		if source := consumer.sources[0]; source.Locator != fixture.fakeLocator || source.LocatorDigest != wantLocatorDigest {
			t.Fatalf("provider source = %#v, want exact private locator binding", source)
		}
	})

	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture recoverySourceFixture)
	}{
		{
			name: "ciphertext substitution",
			mutate: func(t *testing.T, fixture recoverySourceFixture) {
				t.Helper()
				ciphertext, encryptErr := secure.EncryptString("FAKE_SUBSTITUTED_RECOVERY_LOCATOR")
				if encryptErr != nil {
					t.Fatal(encryptErr)
				}
				if err := fixture.db.Exec(
					"UPDATE recovery_points SET encrypted_provider_locator = ? WHERE id = ?", ciphertext, fixture.recoveryPointID,
				).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "catalog row substitution",
			mutate: func(t *testing.T, fixture recoverySourceFixture) {
				t.Helper()
				if err := fixture.db.Model(&model.CatalogEntry{}).
					Where("generation_id = ? AND entry_id = ?", fixture.catalogGenerationID, fixture.fileRefs[0].EntryID).
					Update("generation_id", strings.Repeat("9", 32)).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "recovery point substitution",
			mutate: func(t *testing.T, fixture recoverySourceFixture) {
				t.Helper()
				replacement := model.BackupRepository{
					ID: strings.Repeat("8", 32), ProviderKind: string(fixture.provider), DisplayName: "replacement",
					VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
					ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CapabilityRevision: 1,
				}
				if err := fixture.db.Create(&replacement).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RecoveryPoint{}).
					Where("id = ?", fixture.recoveryPointID).Update("repository_id", replacement.ID).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name+" fails before provider I/O", func(t *testing.T) {
			fixture := seedRecoverySourceFixture(t, backupasset.PointNativeSnapshot)
			validator, err := NewSourceValidator(fixture.db)
			if err != nil {
				t.Fatalf("NewSourceValidator() error = %v", err)
			}
			selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
				RepositoryID: fixture.repositoryID, RecoveryPointID: fixture.recoveryPointID,
				CatalogGenerationID: fixture.catalogGenerationID, AssetRefs: []backupasset.AssetRef{fixture.directoryRef},
				MaxItems: len(fixture.fileRefs),
			})
			if err != nil {
				t.Fatalf("FreezeSelection() error = %v", err)
			}
			testCase.mutate(t, fixture)
			consumer := &exactRecoverySourceConsumerSpy{}
			if err := validator.Revalidate(context.Background(), selection, consumer); err == nil {
				t.Fatal("Revalidate() accepted a substituted frozen source")
			}
			if consumer.calls != 0 {
				t.Fatalf("provider received %d call(s) after source substitution", consumer.calls)
			}
		})
	}
}

func TestSourceValidatorRefusesRawRsyncHandoffBeforeProviderConsumer(t *testing.T) {
	fixture := seedRecoverySourceFixture(t, backupasset.PointMutableHead)
	validator, err := NewSourceValidator(fixture.db)
	if err != nil {
		t.Fatalf("NewSourceValidator() error = %v", err)
	}
	selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: fixture.repositoryID, RecoveryPointID: fixture.recoveryPointID,
		CatalogGenerationID: fixture.catalogGenerationID, AssetRefs: []backupasset.AssetRef{fixture.directoryRef},
		MaxItems: len(fixture.fileRefs),
	})
	if err != nil {
		t.Fatalf("FreezeSelection() error = %v", err)
	}
	consumer := &exactRecoverySourceConsumerSpy{}

	err = validator.Revalidate(context.Background(), selection, consumer)
	if err == nil {
		t.Fatal("Revalidate() handed an Rsync locator directly to a provider consumer")
	}
	if consumer.calls != 0 {
		t.Fatalf("raw Rsync source reached provider consumer %d time(s)", consumer.calls)
	}
	tx := fixture.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer func() { _ = tx.Rollback().Error }()
	raw, err := validator.RevalidateTx(context.Background(), tx, selection)
	if err == nil {
		t.Fatal("RevalidateTx() returned a raw Rsync source handoff")
	}
	if raw.Locator != "" || raw.LocatorDigest != "" {
		t.Fatal("RevalidateTx() returned private Rsync source data")
	}
}

func TestRecoveryCreatesRsyncScalarRefWithoutCallerRoot(t *testing.T) {
	plan := model.BackupAssetRecoveryPlan{
		ID:                      strings.Repeat("1", 32),
		BindingDigest:           strings.Repeat("2", 64),
		RepositoryID:            strings.Repeat("3", 32),
		RecoveryPointID:         strings.Repeat("4", 32),
		CatalogGenerationID:     strings.Repeat("5", 32),
		SelectionDigest:         strings.Repeat("6", 64),
		SourceRevisionDigest:    strings.Repeat("7", 64),
		ImmutableManifestDigest: strings.Repeat("8", 64),
	}

	ref, err := NewRsyncRestoreSourceRef(plan)
	if err != nil {
		t.Fatalf("NewRsyncRestoreSourceRef: %v", err)
	}
	want := provider.RsyncRestoreSourceRef{
		PlanID:               plan.ID,
		PlanBindingDigest:    plan.BindingDigest,
		RepositoryID:         plan.RepositoryID,
		RecoveryPointID:      plan.RecoveryPointID,
		CatalogGenerationID:  plan.CatalogGenerationID,
		SelectionDigest:      plan.SelectionDigest,
		SourceRevisionDigest: plan.SourceRevisionDigest,
		ManifestDigest:       plan.ImmutableManifestDigest,
	}
	if ref != want {
		t.Fatalf("Rsync scalar ref = %#v, want %#v", ref, want)
	}
	assertRecoveryPackageLacksTopLevelIdentifiers(t,
		"RsyncManagedSourceRoot",
		"NewRsyncManagedSourceRoot",
		"RevalidateRsyncRestoreSource",
	)
}

func assertRecoveryPackageLacksTopLevelIdentifiers(t *testing.T, forbidden ...string) {
	t.Helper()
	forbiddenSet := make(map[string]struct{}, len(forbidden))
	for _, name := range forbidden {
		forbiddenSet[name] = struct{}{}
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read Recovery package: %v", err)
	}
	fileSet := token.NewFileSet()
	files := make([]*ast.File, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fileSet, entry.Name(), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse Recovery file %s: %v", entry.Name(), parseErr)
		}
		if file.Name.Name == "recovery" {
			files = append(files, file)
		}
	}
	if len(files) == 0 {
		t.Fatal("parsed Recovery package is missing")
	}
	for _, file := range files {
		for _, declaration := range file.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if _, exists := forbiddenSet[typed.Name.Name]; exists {
					t.Fatalf("Recovery still exports superseded identifier %q", typed.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range typed.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, exists := forbiddenSet[typeSpec.Name.Name]; exists {
						t.Fatalf("Recovery still exports superseded identifier %q", typeSpec.Name.Name)
					}
				}
			}
		}
	}
}

func TestSourceValidatorCallsConsumerAfterValidationTransactionCommits(t *testing.T) {
	fixture := seedRecoverySourceFixture(t, backupasset.PointNativeSnapshot)
	validator, err := NewSourceValidator(fixture.db)
	if err != nil {
		t.Fatalf("NewSourceValidator() error = %v", err)
	}
	selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: fixture.repositoryID, RecoveryPointID: fixture.recoveryPointID,
		CatalogGenerationID: fixture.catalogGenerationID, AssetRefs: []backupasset.AssetRef{fixture.directoryRef},
		MaxItems: len(fixture.fileRefs),
	})
	if err != nil {
		t.Fatalf("FreezeSelection() error = %v", err)
	}

	consumer := exactRecoverySourceConsumerFunc(func(_ context.Context, _ publication.ExactRecoverySource) error {
		return fixture.db.Model(&model.BackupRepository{}).
			Where("id = ?", fixture.repositoryID).
			Update("display_name", "consumer-ran-after-validation-commit").Error
	})
	if err := validator.Revalidate(context.Background(), selection, consumer); err != nil {
		t.Fatalf("Revalidate() consumer ran before validation commit: %v", err)
	}

	var repository model.BackupRepository
	if err := fixture.db.Where("id = ?", fixture.repositoryID).Take(&repository).Error; err != nil {
		t.Fatal(err)
	}
	if repository.DisplayName != "consumer-ran-after-validation-commit" {
		t.Fatalf("consumer update = %q, want committed post-validation update", repository.DisplayName)
	}
}

func TestSourceValidatorSanitizesConsumerErrorsAndPreservesContextSemantics(t *testing.T) {
	fixture := seedRecoverySourceFixture(t, backupasset.PointNativeSnapshot)
	validator, err := NewSourceValidator(fixture.db)
	if err != nil {
		t.Fatalf("NewSourceValidator() error = %v", err)
	}
	selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: fixture.repositoryID, RecoveryPointID: fixture.recoveryPointID,
		CatalogGenerationID: fixture.catalogGenerationID, AssetRefs: []backupasset.AssetRef{fixture.directoryRef},
		MaxItems: len(fixture.fileRefs),
	})
	if err != nil {
		t.Fatalf("FreezeSelection() error = %v", err)
	}
	privateDigest := selection.privateSourceBinding.LocatorDigest

	tests := []struct {
		name        string
		consumerErr error
		want        error
	}{
		{
			name:        "provider failure is closed",
			consumerErr: fmt.Errorf("provider failed for locator %s digest %s", fixture.fakeLocator, privateDigest),
			want:        ErrRecoverySourceUnavailable,
		},
		{
			name:        "provider cancellation preserves context semantics",
			consumerErr: fmt.Errorf("provider canceled locator %s: %w", fixture.fakeLocator, context.Canceled),
			want:        context.Canceled,
		},
		{
			name:        "provider deadline preserves context semantics",
			consumerErr: fmt.Errorf("provider timed out digest %s: %w", privateDigest, context.DeadlineExceeded),
			want:        context.DeadlineExceeded,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			consumer := &exactRecoverySourceConsumerSpy{err: testCase.consumerErr}
			got := validator.Revalidate(context.Background(), selection, consumer)
			if !errors.Is(got, testCase.want) {
				t.Fatalf("Revalidate() error = %v, want %v", got, testCase.want)
			}
			for _, forbidden := range []string{fixture.fakeLocator, privateDigest} {
				if strings.Contains(got.Error(), forbidden) {
					t.Fatalf("Revalidate() error leaked %q: %v", forbidden, got)
				}
			}
			if consumer.calls != 1 {
				t.Fatalf("provider calls = %d, want 1", consumer.calls)
			}
		})
	}
}

func TestSourceValidatorRevalidatesInsideCallerTransaction(t *testing.T) {
	fixture := seedRecoverySourceFixture(t, backupasset.PointNativeSnapshot)
	validator, err := NewSourceValidator(fixture.db)
	if err != nil {
		t.Fatalf("NewSourceValidator() error = %v", err)
	}
	selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: fixture.repositoryID, RecoveryPointID: fixture.recoveryPointID,
		CatalogGenerationID: fixture.catalogGenerationID, AssetRefs: []backupasset.AssetRef{fixture.directoryRef},
		MaxItems: len(fixture.fileRefs),
	})
	if err != nil {
		t.Fatalf("FreezeSelection() error = %v", err)
	}

	tx := fixture.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	ciphertext, err := secure.EncryptString("FAKE_TX_SUBSTITUTED_RECOVERY_LOCATOR")
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec("UPDATE recovery_points SET encrypted_provider_locator = ? WHERE id = ?", ciphertext, fixture.recoveryPointID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := validator.RevalidateTx(context.Background(), tx, selection); err == nil {
		t.Fatal("RevalidateTx() ignored an uncommitted source substitution")
	}
}

func TestSourceValidatorRevalidateTxReturnsLockedMaterializedHandoff(t *testing.T) {
	fixture := seedRecoverySourceFixture(t, backupasset.PointNativeSnapshot)
	validator, err := NewSourceValidator(fixture.db)
	if err != nil {
		t.Fatalf("NewSourceValidator() error = %v", err)
	}
	selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: fixture.repositoryID, RecoveryPointID: fixture.recoveryPointID,
		CatalogGenerationID: fixture.catalogGenerationID, AssetRefs: []backupasset.AssetRef{fixture.directoryRef},
		MaxItems: len(fixture.fileRefs),
	})
	if err != nil {
		t.Fatalf("FreezeSelection() error = %v", err)
	}

	tx := fixture.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	exactSource, err := validator.RevalidateTx(context.Background(), tx, selection)
	if err != nil {
		t.Fatalf("RevalidateTx() error = %v", err)
	}
	defer clearExactRecoverySource(&exactSource)
	if err := exactSource.Validate(); err != nil || exactSource.Locator != fixture.fakeLocator {
		t.Fatalf("materialized source = %#v, validation error = %v", exactSource, err)
	}

	writeErr := fixture.db.Model(&model.BackupRepository{}).
		Where("id = ?", fixture.repositoryID).
		Update("provider_kind", string(backupasset.ProviderRclone)).Error
	if writeErr == nil {
		t.Fatal("source tuple change committed while caller validation transaction remained open")
	}
	if !isPlanDatabaseBusy(writeErr) {
		t.Fatalf("source tuple barrier error = %v, want database lock", writeErr)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("caller validation transaction commit: %v", err)
	}
	if err := fixture.db.Model(&model.BackupRepository{}).
		Where("id = ?", fixture.repositoryID).
		Update("provider_kind", string(backupasset.ProviderRclone)).Error; err != nil {
		t.Fatalf("source tuple remained locked after caller commit: %v", err)
	}
}

func TestSourceValidatorRejectsMutableLocatorAndProviderSubstitutionBeforeConsumer(t *testing.T) {
	tests := []struct {
		name     string
		callerTx bool
		mutate   func(t *testing.T, db *gorm.DB, fixture recoverySourceFixture)
	}{
		{
			name: "ordinary ciphertext substitution",
			mutate: func(t *testing.T, db *gorm.DB, fixture recoverySourceFixture) {
				t.Helper()
				ciphertext, err := secure.EncryptString("FAKE_MUTABLE_SUBSTITUTED_LOCATOR_FOR_TEST_ONLY")
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(
					"UPDATE recovery_points SET encrypted_provider_locator = ? WHERE id = ?", ciphertext, fixture.recoveryPointID,
				).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "ordinary provider substitution",
			mutate: func(t *testing.T, db *gorm.DB, fixture recoverySourceFixture) {
				t.Helper()
				if err := db.Model(&model.BackupRepository{}).
					Where("id = ?", fixture.repositoryID).
					Update("provider_kind", string(backupasset.ProviderRclone)).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:     "caller transaction ciphertext substitution",
			callerTx: true,
			mutate: func(t *testing.T, db *gorm.DB, fixture recoverySourceFixture) {
				t.Helper()
				ciphertext, err := secure.EncryptString("FAKE_MUTABLE_TX_SUBSTITUTED_LOCATOR_FOR_TEST_ONLY")
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Exec(
					"UPDATE recovery_points SET encrypted_provider_locator = ? WHERE id = ?", ciphertext, fixture.recoveryPointID,
				).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:     "caller transaction provider substitution",
			callerTx: true,
			mutate: func(t *testing.T, db *gorm.DB, fixture recoverySourceFixture) {
				t.Helper()
				if err := db.Model(&model.BackupRepository{}).
					Where("id = ?", fixture.repositoryID).
					Update("provider_kind", string(backupasset.ProviderRclone)).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := seedRecoverySourceFixture(t, backupasset.PointMutableHead)
			validator, err := NewSourceValidator(fixture.db)
			if err != nil {
				t.Fatalf("NewSourceValidator() error = %v", err)
			}
			selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
				RepositoryID: fixture.repositoryID, RecoveryPointID: fixture.recoveryPointID,
				CatalogGenerationID: fixture.catalogGenerationID, AssetRefs: []backupasset.AssetRef{fixture.directoryRef},
				MaxItems: len(fixture.fileRefs),
			})
			if err != nil {
				t.Fatalf("FreezeSelection() error = %v", err)
			}

			if testCase.callerTx {
				tx := fixture.db.Begin()
				if tx.Error != nil {
					t.Fatal(tx.Error)
				}
				t.Cleanup(func() { _ = tx.Rollback().Error })
				testCase.mutate(t, tx, fixture)
				_, err = validator.RevalidateTx(context.Background(), tx, selection)
			} else {
				consumer := &exactRecoverySourceConsumerSpy{}
				testCase.mutate(t, fixture.db, fixture)
				err = validator.Revalidate(context.Background(), selection, consumer)
				if consumer.calls != 0 {
					t.Fatalf("provider received %d call(s) after mutable source substitution", consumer.calls)
				}
			}
			if !errors.Is(err, ErrRecoverySourceChanged) {
				t.Fatalf("source substitution error = %v, want ErrRecoverySourceChanged", err)
			}
		})
	}
}

func TestRecoveryPlanSnapshotsResolvedTargetRootLocator(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	created, err := fixture.service.CreatePlan(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	if created.PlanID == "" || created.Replay || created.State != PlanStateDraft {
		t.Fatalf("created plan = %+v", created)
	}
	calls := fixture.resolver.snapshotCalls()
	if len(calls) != 1 || calls[0].NodeID != fixture.request.Plan.Binding.Target.NodeID ||
		calls[0].RootID != fixture.request.Plan.Binding.Target.RootID ||
		!strings.Contains(calls[0].ConnectionPoolType, "sql.Tx") {
		t.Fatalf("target-root resolver calls = %+v", calls)
	}

	var rawLocator string
	if err := fixture.db.Table((model.BackupAssetRecoveryPlan{}).TableName()).
		Select("encrypted_target_root_locator").Where("id = ?", created.PlanID).Scan(&rawLocator).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rawLocator, "enc:v2:") || strings.Contains(rawLocator, fixture.targetRootLocator) {
		t.Fatalf("raw target-root snapshot is not private v2 ciphertext: %q", rawLocator)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", created.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if plan.EncryptedTargetRootLocator != fixture.targetRootLocator ||
		plan.TargetNodeID != fixture.request.Plan.Binding.Target.NodeID ||
		plan.TargetRootID != fixture.request.Plan.Binding.Target.RootID ||
		plan.RootLocatorDigest != fixture.request.Plan.Binding.Target.RootLocatorDigest {
		t.Fatalf("persisted target-root snapshot drifted: %+v", plan)
	}
	encoded, err := json.Marshal(struct {
		Result CreatePlanResult              `json:"result"`
		Plan   model.BackupAssetRecoveryPlan `json:"plan"`
	}{Result: created, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		fixture.targetRootLocator,
		fixture.request.Plan.Binding.Target.RootLocatorDigest,
		fixture.resolver.resolution.SafeLabel,
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("private target-root material leaked to result/model JSON: %s", encoded)
		}
	}
}

func TestRecoveryPlanTargetRootResolutionFailsClosedBeforeWrites(t *testing.T) {
	for _, name := range []string{
		"nil resolver",
		"missing root",
		"archived root",
		"returned node mismatch",
		"returned root mismatch",
		"noncanonical locator",
		"returned digest mismatch",
		"expected digest drift",
		"private state error",
		"context cancellation",
		"plan hook encryption failure",
		"resolver transaction error",
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newPlanServiceTestFixture(t, false)
			resolution := fixture.resolver.resolution
			resolver := &recoveryTargetRootResolverFake{resolution: resolution}
			dependencies := PlanServiceDependencies{
				DB: fixture.db, Now: func() time.Time { return fixture.now }, TargetRootResolver: resolver,
			}
			ctx := context.Background()
			wantErr := ErrRecoveryPlanUnavailable
			switch name {
			case "nil resolver":
				dependencies.TargetRootResolver = nil
			case "missing root", "archived root":
				resolver.err = settings.ErrRecoveryTargetRootNotFound
				wantErr = ErrRecoveryTargetChanged
			case "returned node mismatch":
				resolution.NodeID++
				resolution.LocatorDigest, _ = settings.RecoveryTargetRootLocatorDigest(
					resolution.NodeID, resolution.RootID, resolution.Locator,
				)
				resolver.resolution = resolution
				wantErr = ErrRecoveryTargetChanged
			case "returned root mismatch":
				resolution.RootID = "other-root"
				resolution.LocatorDigest, _ = settings.RecoveryTargetRootLocatorDigest(
					resolution.NodeID, resolution.RootID, resolution.Locator,
				)
				resolver.resolution = resolution
				wantErr = ErrRecoveryTargetChanged
			case "noncanonical locator":
				resolution.Locator = "/srv/target/../FAKE_NONCANONICAL_FOR_TEST_ONLY"
				resolver.resolution = resolution
			case "returned digest mismatch":
				resolution.LocatorDigest = strings.Repeat("0", 64)
				resolver.resolution = resolution
			case "expected digest drift":
				resolution.Locator = "/srv/FAKE_ROTATED_PLAN_TARGET_ROOT_FOR_TEST_ONLY"
				resolution.LocatorDigest, _ = settings.RecoveryTargetRootLocatorDigest(
					resolution.NodeID, resolution.RootID, resolution.Locator,
				)
				resolver.resolution = resolution
				wantErr = ErrRecoveryTargetChanged
			case "private state error":
				resolver.err = fmt.Errorf("%w: %s", settings.ErrRecoveryTargetRootUnavailable, fixture.targetRootLocator)
			case "context cancellation":
				sqlDB, dbErr := fixture.db.DB()
				if dbErr != nil {
					t.Fatal(dbErr)
				}
				// Keep the named in-memory schema alive if cancellation discards
				// the transaction's SQLite connection.
				keeper, dbErr := sqlDB.Conn(context.Background())
				if dbErr != nil {
					t.Fatal(dbErr)
				}
				t.Cleanup(func() {
					if closeErr := keeper.Close(); closeErr != nil {
						t.Errorf("close SQLite keeper connection: %v", closeErr)
					}
				})
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(context.Background())
				resolver.resolve = func(context.Context, *gorm.DB, uint, string) (settings.RecoveryTargetRootResolution, error) {
					cancel()
					return settings.RecoveryTargetRootResolution{}, context.Canceled
				}
				wantErr = context.Canceled
			case "plan hook encryption failure":
				dependencies.beforePersist = func(stage planCreateStage, _ int) error {
					if stage == planCreateBeforePlanInsert {
						t.Setenv("APP_ENV", "production")
						t.Setenv("DATA_ENCRYPTION_KEY", "")
						secure.ResetForTesting()
					}
					return nil
				}
			case "resolver transaction error":
				resolver.resolve = func(ctx context.Context, tx *gorm.DB, _ uint, _ string) (settings.RecoveryTargetRootResolution, error) {
					err := tx.WithContext(ctx).Exec("SELECT * FROM recovery_target_root_missing_table").Error
					return settings.RecoveryTargetRootResolution{}, fmt.Errorf("FAKE_RESOLVER_DB_ERROR_FOR_TEST_ONLY: %w", err)
				}
			}

			service, constructorErr := NewPlanService(dependencies)
			if name == "nil resolver" {
				if service != nil || !errors.Is(constructorErr, wantErr) || constructorErr.Error() != wantErr.Error() {
					t.Fatalf("nil-resolver constructor service=%v error=%v", service, constructorErr)
				}
				assertRecoveryPlanAndItemCounts(t, fixture.db, 0, 0)
				return
			}
			if constructorErr != nil {
				t.Fatal(constructorErr)
			}
			_, createErr := service.CreatePlan(ctx, fixture.request)
			if !errors.Is(createErr, wantErr) || createErr.Error() != wantErr.Error() {
				t.Fatalf("CreatePlan() error=%v, want safe %v", createErr, wantErr)
			}
			for _, private := range []string{
				fixture.targetRootLocator,
				resolution.SafeLabel,
				"FAKE_RESOLVER_DB_ERROR_FOR_TEST_ONLY",
			} {
				if strings.Contains(createErr.Error(), private) {
					t.Fatalf("private resolver material leaked through error: %v", createErr)
				}
			}
			assertRecoveryPlanAndItemCounts(t, fixture.db, 0, 0)
		})
	}
}

func TestRecoveryPlanIdempotentReplayUsesFrozenTargetRootSnapshot(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	if err := fixture.db.AutoMigrate(&model.SystemSetting{}, &model.Node{}); err != nil {
		t.Fatal(err)
	}
	node := model.Node{
		ID: fixture.request.Plan.Binding.Target.NodeID, Name: "plan-target-node", Host: "plan-target.invalid",
		Port: 22, Username: "tester", AuthType: "password", BackupDir: "plan-target-backup",
	}
	if err := fixture.db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	registry := settings.NewService(fixture.db)
	oldDefinition := settings.RecoveryTargetRootDefinition{
		NodeID: node.ID, RootID: fixture.request.Plan.Binding.Target.RootID,
		SafeLabel: "FAKE_OLD_PLAN_TARGET_ROOT_LABEL_FOR_TEST_ONLY", Locator: fixture.targetRootLocator,
	}
	var oldResolution settings.RecoveryTargetRootResolution
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		var err error
		oldResolution, err = registry.RegisterRecoveryTargetRootTx(context.Background(), tx, oldDefinition)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if oldResolution.LocatorDigest != fixture.request.Plan.Binding.Target.RootLocatorDigest {
		t.Fatalf("fixture/registry old digest mismatch: %+v", oldResolution)
	}
	resolver := &recoveryTargetRootResolverFake{}
	delegate := registry.ResolveRecoveryTargetRootTx
	resolver.configure(settings.RecoveryTargetRootResolution{}, nil, delegate)
	service := newPlanServiceWithResolverForTest(t, fixture, resolver, nil)

	oldCreated, err := service.CreatePlan(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("create old target plan: %v", err)
	}
	if len(resolver.snapshotCalls()) != 1 {
		t.Fatalf("initial resolver calls=%+v", resolver.snapshotCalls())
	}

	newDefinition := oldDefinition
	newDefinition.SafeLabel = "FAKE_NEW_PLAN_TARGET_ROOT_LABEL_FOR_TEST_ONLY"
	newDefinition.Locator = "/srv/FAKE_NEW_PLAN_TARGET_ROOT_FOR_TEST_ONLY"
	var newResolution settings.RecoveryTargetRootResolution
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		var err error
		newResolution, err = registry.RegisterRecoveryTargetRootTx(context.Background(), tx, newDefinition)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	resolverMustNotRun := errors.New("FAKE_REPLAY_RESOLVER_CALL_FOR_TEST_ONLY")
	resolver.configure(settings.RecoveryTargetRootResolution{}, nil,
		func(context.Context, *gorm.DB, uint, string) (settings.RecoveryTargetRootResolution, error) {
			return settings.RecoveryTargetRootResolution{}, resolverMustNotRun
		})
	callsBeforeReplay := len(resolver.snapshotCalls())
	replayedOld, err := service.CreatePlan(context.Background(), fixture.request)
	if err != nil || !replayedOld.Replay || replayedOld.PlanID != oldCreated.PlanID {
		t.Fatalf("old snapshot replay=%+v error=%v", replayedOld, err)
	}
	if len(resolver.snapshotCalls()) != callsBeforeReplay {
		t.Fatal("same-intent replay consulted the rotated registry")
	}

	resolver.configure(settings.RecoveryTargetRootResolution{}, nil, delegate)
	newIntentWithOldDigest := cloneCreatePlanRequest(fixture.request)
	newIntentWithOldDigest.IdempotencyKey = "recovery-plan-idempotency-key-0002"
	if _, err := service.CreatePlan(context.Background(), newIntentWithOldDigest); !errors.Is(err, ErrRecoveryTargetChanged) {
		t.Fatalf("new intent with old digest error=%v, want ErrRecoveryTargetChanged", err)
	}
	assertRecoveryPlanAndItemCounts(t, fixture.db, 1, int64(len(fixture.request.Selection.AssetRefs)))

	newIntent := cloneCreatePlanRequest(fixture.request)
	newIntent.IdempotencyKey = "recovery-plan-idempotency-key-0003"
	newIntent.Plan.Binding.Target.RootLocatorDigest = newResolution.LocatorDigest
	newIntent.Plan.Binding.Target.PathDigest = mustTargetPathDigest(
		t, newIntent.Plan.Binding.Target.RootID, newResolution.LocatorDigest,
		newIntent.Plan.Binding.Target.EncryptedRelativePath,
	)
	newCreated, err := service.CreatePlan(context.Background(), newIntent)
	if err != nil || newCreated.Replay || newCreated.PlanID == "" {
		t.Fatalf("new target plan=%+v error=%v", newCreated, err)
	}
	var newPlan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", newCreated.PlanID).Take(&newPlan).Error; err != nil {
		t.Fatal(err)
	}
	if newPlan.EncryptedTargetRootLocator != newDefinition.Locator || newPlan.RootLocatorDigest != newResolution.LocatorDigest {
		t.Fatalf("new plan did not snapshot rotated root: %+v", newPlan)
	}

	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		return registry.DeleteRecoveryTargetRootTx(context.Background(), tx, node.ID, oldDefinition.RootID)
	}); err != nil {
		t.Fatal(err)
	}
	resolver.configure(settings.RecoveryTargetRootResolution{}, nil,
		func(context.Context, *gorm.DB, uint, string) (settings.RecoveryTargetRootResolution, error) {
			return settings.RecoveryTargetRootResolution{}, resolverMustNotRun
		})
	callsBeforeDeletedReplay := len(resolver.snapshotCalls())
	for name, request := range map[string]CreatePlanRequest{"old": fixture.request, "new": newIntent} {
		replayed, replayErr := service.CreatePlan(context.Background(), request)
		if replayErr != nil || !replayed.Replay {
			t.Fatalf("%s plan replay after registry deletion=%+v error=%v", name, replayed, replayErr)
		}
	}
	if len(resolver.snapshotCalls()) != callsBeforeDeletedReplay {
		t.Fatal("replay after registry deletion consulted the resolver")
	}

	corruptSnapshot, err := secure.EncryptString("/srv/target/../FAKE_CORRUPT_PLAN_ROOT_FOR_TEST_ONLY")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Table((model.BackupAssetRecoveryPlan{}).TableName()).Where("id = ?", oldCreated.PlanID).
		UpdateColumn("encrypted_target_root_locator", corruptSnapshot).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreatePlan(context.Background(), fixture.request); !errors.Is(err, ErrRecoveryPlanUnavailable) {
		t.Fatalf("corrupt frozen snapshot replay error=%v, want ErrRecoveryPlanUnavailable", err)
	}
	if len(resolver.snapshotCalls()) != callsBeforeDeletedReplay {
		t.Fatal("corrupt frozen snapshot fell back to the current registry")
	}
}

func TestRecoveryPlanTargetRootRotationCannotCrossBind(t *testing.T) {
	tests := []struct {
		name             string
		captureBefore    bool
		requestNewDigest bool
		wantErr          error
		wantLocator      string
	}{
		{name: "plan captures old tuple before rotation", captureBefore: true},
		{name: "plan captures new tuple after rotation", requestNewDigest: true},
		{name: "old digest loses to completed rotation", wantErr: ErrRecoveryTargetChanged},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPlanServiceTestFixture(t, false)
			oldResolution := fixture.resolver.resolution
			newResolution := oldResolution
			newResolution.SafeLabel = "FAKE_ROTATED_BARRIER_ROOT_LABEL_FOR_TEST_ONLY"
			newResolution.Locator = "/srv/FAKE_ROTATED_BARRIER_ROOT_FOR_TEST_ONLY"
			newResolution.LocatorDigest, _ = settings.RecoveryTargetRootLocatorDigest(
				newResolution.NodeID, newResolution.RootID, newResolution.Locator,
			)
			entered := make(chan struct{})
			release := make(chan struct{})
			resolver := &recoveryTargetRootResolverFake{resolution: oldResolution}
			if testCase.captureBefore {
				resolver.resolve = func(context.Context, *gorm.DB, uint, string) (settings.RecoveryTargetRootResolution, error) {
					captured := oldResolution
					close(entered)
					<-release
					return captured, nil
				}
				testCase.wantLocator = oldResolution.Locator
			} else {
				resolver.resolve = func(context.Context, *gorm.DB, uint, string) (settings.RecoveryTargetRootResolution, error) {
					close(entered)
					<-release
					resolver.mu.Lock()
					current := resolver.resolution
					resolver.mu.Unlock()
					return current, nil
				}
				if testCase.wantErr == nil {
					testCase.wantLocator = newResolution.Locator
				}
			}
			service := newPlanServiceWithResolverForTest(t, fixture, resolver, nil)
			request := cloneCreatePlanRequest(fixture.request)
			if testCase.requestNewDigest {
				request.Plan.Binding.Target.RootLocatorDigest = newResolution.LocatorDigest
				request.Plan.Binding.Target.PathDigest = mustTargetPathDigest(
					t, request.Plan.Binding.Target.RootID, newResolution.LocatorDigest,
					request.Plan.Binding.Target.EncryptedRelativePath,
				)
			}
			type outcome struct {
				result CreatePlanResult
				err    error
			}
			outcomes := make(chan outcome, 1)
			go func() {
				result, err := service.CreatePlan(context.Background(), request)
				outcomes <- outcome{result: result, err: err}
			}()
			<-entered
			resolver.mu.Lock()
			resolver.resolution = newResolution
			resolver.mu.Unlock()
			close(release)
			got := <-outcomes
			if testCase.wantErr != nil {
				if !errors.Is(got.err, testCase.wantErr) || got.err.Error() != testCase.wantErr.Error() {
					t.Fatalf("rotation outcome error=%v, want %v", got.err, testCase.wantErr)
				}
				assertRecoveryPlanAndItemCounts(t, fixture.db, 0, 0)
				return
			}
			if got.err != nil || got.result.PlanID == "" || got.result.Replay {
				t.Fatalf("rotation outcome=%+v error=%v", got.result, got.err)
			}
			var plan model.BackupAssetRecoveryPlan
			if err := fixture.db.Where("id = ?", got.result.PlanID).Take(&plan).Error; err != nil {
				t.Fatal(err)
			}
			recomputed, err := settings.RecoveryTargetRootLocatorDigest(plan.TargetNodeID, plan.TargetRootID, plan.EncryptedTargetRootLocator)
			if err != nil || recomputed != plan.RootLocatorDigest || plan.EncryptedTargetRootLocator != testCase.wantLocator {
				t.Fatalf("cross-bound target tuple persisted: plan=%+v recomputed=%q error=%v", plan, recomputed, err)
			}
			calls := resolver.snapshotCalls()
			if len(calls) != 1 || !strings.Contains(calls[0].ConnectionPoolType, "sql.Tx") {
				t.Fatalf("resolver calls=%+v", calls)
			}
		})
	}
}

func TestPlanIdempotencyReplayAndOneFieldConflicts(t *testing.T) {
	t.Run("same intent replays the current durable plan state", func(t *testing.T) {
		fixture := newPlanServiceTestFixture(t, false)
		created, err := fixture.service.CreatePlan(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("CreatePlan() error = %v", err)
		}
		if created.PlanID == "" || created.Replay || created.State != PlanStateDraft {
			t.Fatalf("created plan = %#v", created)
		}
		var items []model.BackupAssetRecoveryPlanItem
		if err := fixture.db.Where("plan_id = ?", created.PlanID).Order("ordinal ASC").Find(&items).Error; err != nil {
			t.Fatal(err)
		}
		if len(items) != len(fixture.request.Selection.AssetRefs) {
			t.Fatalf("persisted item count = %d, want %d", len(items), len(fixture.request.Selection.AssetRefs))
		}
		for ordinal, item := range items {
			if item.Ordinal != ordinal || item.EntryID != fixture.request.Selection.AssetRefs[ordinal].EntryID {
				t.Fatalf("persisted item %d = %#v, want canonical ref %#v", ordinal, item, fixture.request.Selection.AssetRefs[ordinal])
			}
		}
		if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).
			Where("id = ?", created.PlanID).
			Updates(map[string]any{"state": string(PlanStateCanceled), "transition_revision": 2, "updated_at": fixture.now.Add(time.Minute)}).Error; err != nil {
			t.Fatal(err)
		}

		replayed, err := fixture.service.CreatePlan(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("CreatePlan() replay error = %v", err)
		}
		if replayed.PlanID != created.PlanID || !replayed.Replay || replayed.State != PlanStateCanceled {
			t.Fatalf("replayed plan = %#v, created = %#v", replayed, created)
		}
	})

	tests := []struct {
		name        string
		exactMirror bool
		prepare     func(t *testing.T, request *CreatePlanRequest)
		mutate      func(*CreatePlanRequest)
	}{
		{
			name: "exact selection",
			mutate: func(request *CreatePlanRequest) {
				replacePlanSelection(t, request, exactSelectionWithRefs(t, request.Selection, request.Selection.AssetRefs[:1]))
			},
		},
		{
			name: "private source revision binding",
			mutate: func(request *CreatePlanRequest) {
				revision := cloneSourceRevision(request.Selection.SourceRevision)
				revision.Immutable.LocatorDigest = strings.Repeat("a", 64)
				replacePlanSelection(t, request, exactSelectionWithRevision(t, request.Selection, revision))
			},
		},
		{
			name: "target mode",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.Target.Mode = TargetModeInPlace
			},
		},
		{
			name: "target node",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.Target.NodeID++
			},
		},
		{
			name: "target root",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.Target.RootID = "alternate-root"
				request.Plan.Binding.Target.PathDigest = mustTargetPathDigest(
					t, request.Plan.Binding.Target.RootID, request.Plan.Binding.Target.RootLocatorDigest,
					request.Plan.Binding.Target.EncryptedRelativePath,
				)
			},
		},
		{
			name: "target root binding",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.Target.RootLocatorDigest = strings.Repeat("a", 64)
				request.Plan.Binding.Target.PathDigest = mustTargetPathDigest(
					t, request.Plan.Binding.Target.RootID, request.Plan.Binding.Target.RootLocatorDigest,
					request.Plan.Binding.Target.EncryptedRelativePath,
				)
			},
		},
		{
			name: "target relative path binding",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.Target.EncryptedRelativePath = "FAKE_ALTERNATE_RECOVERY_TARGET_PATH"
				request.Plan.Binding.Target.PathDigest = mustTargetPathDigest(
					t, request.Plan.Binding.Target.RootID, request.Plan.Binding.Target.RootLocatorDigest,
					request.Plan.Binding.Target.EncryptedRelativePath,
				)
			},
		},
		{
			name: "target base revision",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.Target.BaseNodeRevision = "node-revision-2"
			},
		},
		{
			name: "credential scope revision",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.Target.CredentialScopeRevision = "credential-revision-2"
			},
		},
		{
			name: "root revision",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.Target.RootRevision = "root-revision-2"
			},
		},
		{
			name: "filesystem revision",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.Target.FilesystemRevision = "filesystem-revision-2"
			},
		},
		{
			name: "conflict policy",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.ConflictPolicy = ConflictSkipExisting
			},
		},
		{
			name: "operation digest",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.OperationSetDigest = strings.Repeat("8", 64)
			},
		},
		{
			name:        "delete digest",
			exactMirror: true,
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.DeleteSetDigest = strings.Repeat("7", 64)
			},
		},
		{
			name: "security decision",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.SecurityDecision.Kind = SecurityDecisionBlock
			},
		},
		{
			name: "security decision digest",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.SecurityDecision.DecisionDigest = strings.Repeat("a", 64)
			},
		},
		{
			name: "security finding set",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.SecurityDecision.FindingSetDigest = strings.Repeat("a", 64)
			},
		},
		{
			name: "security policy revision",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.SecurityDecision.PolicyRevision = "security-policy-revision-2"
			},
		},
		{
			name: "capability revision",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.CapabilityRevision = "capability-revision-2"
			},
		},
		{
			name: "preflight revision",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.Binding.PreflightRevision = "preflight-revision-2"
			},
		},
		{
			name: "preflight expiry",
			mutate: func(request *CreatePlanRequest) {
				request.Plan.PreflightExpiresAt = request.Plan.PreflightExpiresAt.Add(time.Minute)
			},
		},
		{
			name: "estimated items",
			mutate: func(request *CreatePlanRequest) {
				request.EstimatedItems++
			},
		},
		{
			name: "estimated bytes",
			mutate: func(request *CreatePlanRequest) {
				request.EstimatedBytes++
			},
		},
		{
			name: "authority category",
			mutate: func(request *CreatePlanRequest) {
				request.AuthorityCategory = AuthorityExactMirrorDelete
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPlanServiceTestFixture(t, testCase.exactMirror)
			if testCase.prepare != nil {
				testCase.prepare(t, &fixture.request)
			}
			created, err := fixture.service.CreatePlan(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("CreatePlan() error = %v", err)
			}
			changed := cloneCreatePlanRequest(fixture.request)
			testCase.mutate(&changed)
			if _, err := fixture.service.CreatePlan(context.Background(), changed); !errors.Is(err, ErrPlanIdempotencyConflict) {
				t.Fatalf("CreatePlan() collision error = %v, want ErrPlanIdempotencyConflict", err)
			}
			var planRows int64
			if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).Count(&planRows).Error; err != nil {
				t.Fatal(err)
			}
			var itemRows int64
			if err := fixture.db.Model(&model.BackupAssetRecoveryPlanItem{}).Count(&itemRows).Error; err != nil {
				t.Fatal(err)
			}
			if planRows != 1 || itemRows != int64(len(fixture.request.Selection.AssetRefs)) {
				t.Fatalf("durable rows after collision: plans=%d items=%d created=%#v", planRows, itemRows, created)
			}
		})
	}
}

func TestPlanIdempotencyConflictsOnMutablePrivateLocatorOrProviderChange(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture recoverySourceFixture)
	}{
		{
			name: "locator change with stable observation tuple",
			mutate: func(t *testing.T, fixture recoverySourceFixture) {
				t.Helper()
				ciphertext, err := secure.EncryptString("FAKE_MUTABLE_PLAN_SUBSTITUTED_LOCATOR_FOR_TEST_ONLY")
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Exec(
					"UPDATE recovery_points SET encrypted_provider_locator = ? WHERE id = ?", ciphertext, fixture.recoveryPointID,
				).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "provider change with stable observation tuple",
			mutate: func(t *testing.T, fixture recoverySourceFixture) {
				t.Helper()
				if err := fixture.db.Model(&model.BackupRepository{}).
					Where("id = ?", fixture.repositoryID).
					Update("provider_kind", string(backupasset.ProviderRclone)).Error; err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPlanServiceTestFixtureForSemantics(t, false, backupasset.PointMutableHead)
			if _, err := fixture.service.CreatePlan(context.Background(), fixture.request); err != nil {
				t.Fatalf("CreatePlan() initial error = %v", err)
			}

			testCase.mutate(t, fixture.source)
			validator, err := NewSourceValidator(fixture.db)
			if err != nil {
				t.Fatalf("NewSourceValidator() error = %v", err)
			}
			selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
				RepositoryID: fixture.source.repositoryID, RecoveryPointID: fixture.source.recoveryPointID,
				CatalogGenerationID: fixture.source.catalogGenerationID,
				AssetRefs:           []backupasset.AssetRef{fixture.source.directoryRef}, MaxItems: len(fixture.source.fileRefs),
			})
			if err != nil {
				t.Fatalf("FreezeSelection() after source change error = %v", err)
			}
			changed := cloneCreatePlanRequest(fixture.request)
			replacePlanSelection(t, &changed, selection)

			if _, err := fixture.service.CreatePlan(context.Background(), changed); !errors.Is(err, ErrPlanIdempotencyConflict) {
				t.Fatalf("CreatePlan() after mutable private source change error = %v, want ErrPlanIdempotencyConflict", err)
			}
			assertPlanCreationRows(t, fixture.db, len(fixture.request.Selection.AssetRefs))
		})
	}
}

func TestPlanIdempotencyConflictsForSourceIdentityAndOverrideBindings(t *testing.T) {
	tests := []struct {
		name      string
		semantics backupasset.PointVersionSemantics
		prepare   func(*CreatePlanRequest)
		mutate    func(t *testing.T, fixture planServiceTestFixture, request *CreatePlanRequest)
	}{
		{
			name:      "immutable manifest digest",
			semantics: backupasset.PointNativeSnapshot,
			mutate: func(t *testing.T, fixture planServiceTestFixture, request *CreatePlanRequest) {
				t.Helper()
				manifestDigest := strings.Repeat("1", 64)
				if err := fixture.db.Model(&model.RecoveryPoint{}).
					Where("id = ?", fixture.source.recoveryPointID).
					Update("manifest_digest", manifestDigest).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.RecoveryPointManifest{}).
					Where("recovery_point_id = ?", fixture.source.recoveryPointID).
					Update("digest", manifestDigest).Error; err != nil {
					t.Fatal(err)
				}
				replacePlanSelection(t, request, freezeFixtureSelection(t, fixture.source, fixture.source.catalogGenerationID))
			},
		},
		{
			name:      "mutable source fingerprint",
			semantics: backupasset.PointMutableHead,
			mutate: func(t *testing.T, fixture planServiceTestFixture, request *CreatePlanRequest) {
				t.Helper()
				fingerprint := strings.Repeat("1", 64)
				if err := fixture.db.Model(&model.RecoveryPoint{}).
					Where("id = ?", fixture.source.recoveryPointID).
					Update("source_fingerprint", fingerprint).Error; err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.Model(&model.CatalogGeneration{}).
					Where("id = ?", fixture.source.catalogGenerationID).
					Update("source_fingerprint", fingerprint).Error; err != nil {
					t.Fatal(err)
				}
				replacePlanSelection(t, request, freezeFixtureSelection(t, fixture.source, fixture.source.catalogGenerationID))
			},
		},
		{
			name:      "mutable catalog generation",
			semantics: backupasset.PointMutableHead,
			mutate: func(t *testing.T, fixture planServiceTestFixture, request *CreatePlanRequest) {
				t.Helper()
				generationID := strings.Repeat("7", 32)
				cloneCatalogGeneration(t, fixture.source, generationID)
				replacePlanSelection(t, request, freezeFixtureSelection(t, fixture.source, generationID))
			},
		},
		{
			name:      "mutable observed at",
			semantics: backupasset.PointMutableHead,
			mutate: func(t *testing.T, fixture planServiceTestFixture, request *CreatePlanRequest) {
				t.Helper()
				observedAt := fixture.source.observedAt.Add(time.Minute)
				if err := fixture.db.Model(&model.RecoveryPoint{}).
					Where("id = ?", fixture.source.recoveryPointID).
					Update("observed_at", observedAt).Error; err != nil {
					t.Fatal(err)
				}
				replacePlanSelection(t, request, freezeFixtureSelection(t, fixture.source, fixture.source.catalogGenerationID))
			},
		},
		{
			name:      "repository identity",
			semantics: backupasset.PointNativeSnapshot,
			mutate: func(t *testing.T, fixture planServiceTestFixture, request *CreatePlanRequest) {
				t.Helper()
				replacePlanSelection(t, request, selectionWithSourceIdentity(
					t, request.Selection, strings.Repeat("1", 32), request.Selection.RecoveryPointID, request.Selection.CatalogGenerationID,
				))
			},
		},
		{
			name:      "recovery point identity",
			semantics: backupasset.PointNativeSnapshot,
			mutate: func(t *testing.T, fixture planServiceTestFixture, request *CreatePlanRequest) {
				t.Helper()
				replacePlanSelection(t, request, selectionWithSourceIdentity(
					t, request.Selection, request.Selection.RepositoryID, strings.Repeat("2", 32), request.Selection.CatalogGenerationID,
				))
			},
		},
		{
			name:      "catalog identity",
			semantics: backupasset.PointNativeSnapshot,
			mutate: func(t *testing.T, fixture planServiceTestFixture, request *CreatePlanRequest) {
				t.Helper()
				replacePlanSelection(t, request, selectionWithSourceIdentity(
					t, request.Selection, request.Selection.RepositoryID, request.Selection.RecoveryPointID, strings.Repeat("3", 32),
				))
			},
		},
		{
			name:      "admin override binding",
			semantics: backupasset.PointNativeSnapshot,
			prepare: func(request *CreatePlanRequest) {
				request.Plan.Binding.SecurityDecision.Kind = SecurityDecisionAdminOverride
				request.Plan.Binding.SecurityDecision.OverrideBindingDigest = strings.Repeat("1", 64)
			},
			mutate: func(t *testing.T, _ planServiceTestFixture, request *CreatePlanRequest) {
				t.Helper()
				request.Plan.Binding.SecurityDecision.OverrideBindingDigest = strings.Repeat("2", 64)
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPlanServiceTestFixtureForSemantics(t, false, testCase.semantics)
			if testCase.prepare != nil {
				testCase.prepare(&fixture.request)
			}
			if _, err := fixture.service.CreatePlan(context.Background(), fixture.request); err != nil {
				t.Fatalf("CreatePlan() initial error = %v", err)
			}

			changed := cloneCreatePlanRequest(fixture.request)
			testCase.mutate(t, fixture, &changed)
			if _, err := fixture.service.CreatePlan(context.Background(), changed); !errors.Is(err, ErrPlanIdempotencyConflict) {
				t.Fatalf("CreatePlan() collision error = %v, want ErrPlanIdempotencyConflict", err)
			}
			assertPlanCreationRows(t, fixture.db, len(fixture.request.Selection.AssetRefs))
		})
	}
}

func TestPlanIdempotencyNamespacesAndDurableReplay(t *testing.T) {
	t.Run("requester and endpoint namespaces create independent durable plans", func(t *testing.T) {
		fixture := newPlanServiceTestFixture(t, false)
		created, err := fixture.service.CreatePlan(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("CreatePlan() initial error = %v", err)
		}

		requesterNamespace := cloneCreatePlanRequest(fixture.request)
		requesterNamespace.RequesterID++
		requesterPlan, err := fixture.service.CreatePlan(context.Background(), requesterNamespace)
		if err != nil {
			t.Fatalf("CreatePlan() requester namespace error = %v", err)
		}

		endpointNamespace := cloneCreatePlanRequest(fixture.request)
		endpointNamespace.Endpoint = "recovery_plan_create_alt"
		endpointPlan, err := fixture.service.CreatePlan(context.Background(), endpointNamespace)
		if err != nil {
			t.Fatalf("CreatePlan() endpoint namespace error = %v", err)
		}

		if requesterPlan.Replay || endpointPlan.Replay || requesterPlan.PlanID == created.PlanID ||
			endpointPlan.PlanID == created.PlanID || endpointPlan.PlanID == requesterPlan.PlanID {
			t.Fatalf("namespace plans must be independent: initial=%#v requester=%#v endpoint=%#v", created, requesterPlan, endpointPlan)
		}
		var planRows int64
		if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).Count(&planRows).Error; err != nil {
			t.Fatal(err)
		}
		if planRows != 3 {
			t.Fatalf("durable plan count = %d, want 3", planRows)
		}
	})

	t.Run("same intent replays after source and catalog state change", func(t *testing.T) {
		fixture := newPlanServiceTestFixture(t, false)
		created, err := fixture.service.CreatePlan(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("CreatePlan() initial error = %v", err)
		}
		if err := fixture.db.Model(&model.RecoveryPoint{}).
			Where("id = ?", fixture.source.recoveryPointID).
			Update("physical_availability", string(backupasset.PhysicalOffline)).Error; err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.Model(&model.CatalogGeneration{}).
			Where("id = ?", fixture.source.catalogGenerationID).
			Update("is_active", false).Error; err != nil {
			t.Fatal(err)
		}

		replayed, err := fixture.service.CreatePlan(context.Background(), fixture.request)
		if err != nil {
			t.Fatalf("CreatePlan() replay after source/catalog state change error = %v", err)
		}
		if !replayed.Replay || replayed.PlanID != created.PlanID {
			t.Fatalf("replayed plan = %#v, created = %#v", replayed, created)
		}
	})
}

func TestPlanIdempotencyReplaysAcrossLocatorKeyAndCiphertextRotation(t *testing.T) {
	tests := []struct {
		name      string
		semantics backupasset.PointVersionSemantics
	}{
		{name: "immutable source", semantics: backupasset.PointNativeSnapshot},
		{name: "mutable source", semantics: backupasset.PointMutableHead},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPlanServiceTestFixtureForSemantics(t, false, testCase.semantics)
			created, err := fixture.service.CreatePlan(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("CreatePlan() initial error = %v", err)
			}

			t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RECOVERY_SOURCE_ROTATED_KEY_FOR_TEST_ONLY")
			secure.ResetForTesting()
			rotatedCiphertext, err := secure.EncryptString(fixture.source.fakeLocator)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Exec(
				"UPDATE recovery_points SET encrypted_provider_locator = ? WHERE id = ?", rotatedCiphertext, fixture.source.recoveryPointID,
			).Error; err != nil {
				t.Fatal(err)
			}
			rotatedTargetRootCiphertext, err := secure.EncryptString(fixture.targetRootLocator)
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Table((model.BackupAssetRecoveryPlan{}).TableName()).Where("id = ?", created.PlanID).
				UpdateColumn("encrypted_target_root_locator", rotatedTargetRootCiphertext).Error; err != nil {
				t.Fatal(err)
			}

			rotatedSelection := freezeFixtureSelection(t, fixture.source, fixture.source.catalogGenerationID)
			if rotatedSelection.SelectionDigest != fixture.request.Selection.SelectionDigest ||
				rotatedSelection.SourceRevisionDigest != fixture.request.Selection.SourceRevisionDigest ||
				!sameSourceRevision(rotatedSelection.SourceRevision, fixture.request.Selection.SourceRevision) ||
				!sameFrozenSourceBinding(rotatedSelection.privateSourceBinding, fixture.request.Selection.privateSourceBinding) {
				t.Fatal("key/ciphertext rotation changed frozen semantic selection")
			}

			rotatedRequest := cloneCreatePlanRequest(fixture.request)
			replacePlanSelection(t, &rotatedRequest, rotatedSelection)
			replayed, err := fixture.service.CreatePlan(context.Background(), rotatedRequest)
			if err != nil {
				t.Fatalf("CreatePlan() replay after key/ciphertext rotation error = %v", err)
			}
			if !replayed.Replay || replayed.PlanID != created.PlanID {
				t.Fatalf("replayed plan = %#v, created = %#v", replayed, created)
			}

			payload, err := json.Marshal(struct {
				Selection ExactSelection          `json:"selection"`
				Authority ExactSelectionAuthority `json:"authority"`
				Replay    CreatePlanResult        `json:"replay"`
			}{
				Selection: rotatedSelection,
				Authority: rotatedSelection.Authority(),
				Replay:    replayed,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, privateValue := range []string{
				fixture.source.fakeLocator,
				rotatedSelection.SourceRevisionDigest,
				rotatedSelection.privateSourceBinding.LocatorDigest,
			} {
				if strings.Contains(string(payload), privateValue) {
					t.Fatal("private locator-derived value leaked into replay payload")
				}
			}
		})
	}
}

func TestPlanCreateConcurrentSameIntent(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	const callers = 12
	timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	start := make(chan struct{})
	type outcome struct {
		result CreatePlanResult
		err    error
	}
	outcomes := make(chan outcome, callers)
	var callersReady sync.WaitGroup
	callersReady.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			callersReady.Done()
			<-start
			result, err := fixture.service.CreatePlan(timeout, cloneCreatePlanRequest(fixture.request))
			outcomes <- outcome{result: result, err: err}
		}()
	}
	callersReady.Wait()
	close(start)

	var planID string
	created := 0
	replayed := 0
	for index := 0; index < callers; index++ {
		select {
		case outcome := <-outcomes:
			if outcome.err != nil {
				t.Fatalf("concurrent CreatePlan() error = %v", outcome.err)
			}
			if planID == "" {
				planID = outcome.result.PlanID
			}
			if outcome.result.PlanID != planID {
				t.Fatalf("concurrent plans diverged: got %s, want %s", outcome.result.PlanID, planID)
			}
			if outcome.result.Replay {
				replayed++
			} else {
				created++
			}
		case <-timeout.Done():
			t.Fatalf("wait for concurrent plan creators: %v", timeout.Err())
		}
	}
	if created != 1 || replayed != callers-1 {
		t.Fatalf("same-intent results: created=%d replayed=%d", created, replayed)
	}
	assertPlanCreationRows(t, fixture.db, len(fixture.request.Selection.AssetRefs))
}

func TestPlanCreateConcurrentDifferentIntentElectsOneWinner(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	changed := cloneCreatePlanRequest(fixture.request)
	changed.Plan.Binding.CapabilityRevision = "capability-revision-concurrent-2"
	const callersPerIntent = 8
	timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	start := make(chan struct{})
	type outcome struct {
		intent int
		result CreatePlanResult
		err    error
	}
	outcomes := make(chan outcome, callersPerIntent*2)
	var callersReady sync.WaitGroup
	callersReady.Add(callersPerIntent * 2)
	for intent, request := range []CreatePlanRequest{fixture.request, changed} {
		for index := 0; index < callersPerIntent; index++ {
			go func(intent int, request CreatePlanRequest) {
				callersReady.Done()
				<-start
				result, err := fixture.service.CreatePlan(timeout, cloneCreatePlanRequest(request))
				outcomes <- outcome{intent: intent, result: result, err: err}
			}(intent, request)
		}
	}
	callersReady.Wait()
	close(start)

	winnerIntent := -1
	winnerPlanID := ""
	winnerResults := 0
	conflicts := 0
	for index := 0; index < callersPerIntent*2; index++ {
		select {
		case outcome := <-outcomes:
			switch {
			case outcome.err == nil:
				if winnerIntent == -1 {
					winnerIntent, winnerPlanID = outcome.intent, outcome.result.PlanID
				}
				if outcome.intent != winnerIntent || outcome.result.PlanID != winnerPlanID {
					t.Fatalf("different-intent winner drift: intent=%d plan=%s winnerIntent=%d winnerPlan=%s", outcome.intent, outcome.result.PlanID, winnerIntent, winnerPlanID)
				}
				winnerResults++
			case errors.Is(outcome.err, ErrPlanIdempotencyConflict):
				conflicts++
			default:
				t.Fatalf("different-intent CreatePlan() error = %v", outcome.err)
			}
		case <-timeout.Done():
			t.Fatalf("wait for different-intent plan creators: %v", timeout.Err())
		}
	}
	if winnerIntent == -1 || winnerResults != callersPerIntent || conflicts != callersPerIntent {
		t.Fatalf("different-intent results: winnerIntent=%d winnerResults=%d conflicts=%d", winnerIntent, winnerResults, conflicts)
	}
	assertPlanCreationRows(t, fixture.db, len(fixture.request.Selection.AssetRefs))
}

func TestPlanCreateConcurrentSQLiteContentionHonorsContext(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	locker := fixture.db.Begin()
	if locker.Error != nil {
		t.Fatal(locker.Error)
	}
	if err := locker.Exec(
		"UPDATE backup_repositories SET display_name = ? WHERE id = ?",
		"sqlite-writer-lock", fixture.request.Selection.RepositoryID,
	).Error; err != nil {
		_ = locker.Rollback().Error
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := fixture.service.CreatePlan(ctx, fixture.request); !errors.Is(err, context.DeadlineExceeded) {
		_ = locker.Rollback().Error
		t.Fatalf("CreatePlan() under SQLite contention error = %v, want context deadline", err)
	}
	if err := locker.Rollback().Error; err != nil {
		t.Fatalf("release SQLite writer lock: %v", err)
	}

	var plans int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryPlan{}).Count(&plans).Error; err != nil {
		t.Fatal(err)
	}
	var items int64
	if err := fixture.db.Model(&model.BackupAssetRecoveryPlanItem{}).Count(&items).Error; err != nil {
		t.Fatal(err)
	}
	if plans != 0 || items != 0 {
		t.Fatalf("canceled contention left residue: plans=%d items=%d", plans, items)
	}

	created, err := fixture.service.CreatePlan(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("CreatePlan() after releasing SQLite contention error = %v", err)
	}
	if created.PlanID == "" || created.Replay || created.State != PlanStateDraft {
		t.Fatalf("CreatePlan() after contention result = %#v", created)
	}
	assertPlanCreationRows(t, fixture.db, len(fixture.request.Selection.AssetRefs))
}

func TestPlanCreateRollsBackEveryPersistenceBoundary(t *testing.T) {
	tests := []struct {
		name        string
		stage       planCreateStage
		itemOrdinal int
	}{
		{name: "plan insert", stage: planCreateBeforePlanInsert, itemOrdinal: -1},
		{name: "first canonical item", stage: planCreateBeforeItemInsert, itemOrdinal: 0},
		{name: "second canonical item", stage: planCreateBeforeItemInsert, itemOrdinal: 1},
		{name: "commit seam", stage: planCreateBeforeCommit, itemOrdinal: -1},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPlanServiceTestFixture(t, false)
			ensureRecoveryPlanRollbackTables(t, fixture.db)
			injected := errors.New("injected plan transaction boundary failure")
			service, err := NewPlanService(PlanServiceDependencies{
				DB: fixture.db, Now: func() time.Time { return fixture.now },
				TargetRootResolver: fixture.resolver,
				beforePersist: func(stage planCreateStage, itemOrdinal int) error {
					if stage == testCase.stage && itemOrdinal == testCase.itemOrdinal {
						return injected
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.CreatePlan(context.Background(), fixture.request); !errors.Is(err, injected) {
				t.Fatalf("CreatePlan() error = %v, want injected failure", err)
			}
			assertRecoveryPlanWriteSetEmpty(t, fixture.db)
		})
	}
}

func TestPlanCreateRollsBackDatabaseCommitFailure(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	ensureRecoveryPlanRollbackTables(t, fixture.db)

	if err := fixture.db.Exec(`CREATE TABLE recovery_plan_commit_guards (
		id INTEGER PRIMARY KEY,
		plan_id TEXT,
		FOREIGN KEY (plan_id) REFERENCES backup_asset_recovery_plans(id) DEFERRABLE INITIALLY DEFERRED
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Exec(`INSERT INTO recovery_plan_commit_guards (id, plan_id) VALUES (1, NULL)`).Error; err != nil {
		t.Fatal(err)
	}

	var injected atomic.Bool
	const callbackName = "test:fail_recovery_plan_commit"
	if err := fixture.db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil ||
			tx.Statement.Schema.Table != (model.BackupAssetRecoveryPlanItem{}).TableName() ||
			!injected.CompareAndSwap(false, true) {
			return
		}
		if err := tx.Exec(
			`UPDATE recovery_plan_commit_guards SET plan_id = ? WHERE id = 1`,
			strings.Repeat("0", 32),
		).Error; err != nil {
			_ = tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Create().Remove(callbackName); err != nil {
			t.Errorf("remove recovery plan commit failure callback: %v", err)
		}
	})

	if _, err := fixture.service.CreatePlan(context.Background(), fixture.request); !errors.Is(err, ErrRecoveryPlanUnavailable) {
		t.Fatalf("CreatePlan() commit error = %v, want ErrRecoveryPlanUnavailable", err)
	}
	if !injected.Load() {
		t.Fatal("deferred constraint failure was not injected before transaction commit")
	}
	assertRecoveryPlanWriteSetEmpty(t, fixture.db)

	var guardPlanID *string
	if err := fixture.db.Raw(`SELECT plan_id FROM recovery_plan_commit_guards WHERE id = 1`).Scan(&guardPlanID).Error; err != nil {
		t.Fatal(err)
	}
	if guardPlanID != nil {
		t.Fatalf("commit failure guard update survived rollback: %q", *guardPlanID)
	}
}

func TestPlanCreateTxLeavesFinalizationToCaller(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	ensureRecoveryPlanRollbackTables(t, fixture.db)
	injected := errors.New("injected standalone finalization failure")
	var finalizationCalls atomic.Int32
	service, err := NewPlanService(PlanServiceDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
		TargetRootResolver: fixture.resolver,
		beforePersist: func(stage planCreateStage, _ int) error {
			if stage == planCreateBeforeCommit {
				finalizationCalls.Add(1)
				return injected
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tx := fixture.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	result, err := service.CreatePlanTx(context.Background(), tx, fixture.request)
	if err != nil {
		_ = tx.Rollback().Error
		t.Fatalf("CreatePlanTx() ran standalone finalization: %v", err)
	}
	if result.PlanID == "" || result.Replay || result.State != PlanStateDraft {
		_ = tx.Rollback().Error
		t.Fatalf("CreatePlanTx() result = %#v", result)
	}
	if finalizationCalls.Load() != 0 {
		_ = tx.Rollback().Error
		t.Fatalf("CreatePlanTx() invoked %d standalone finalization hook(s)", finalizationCalls.Load())
	}

	var plans int64
	if err := tx.Model(&model.BackupAssetRecoveryPlan{}).Count(&plans).Error; err != nil {
		_ = tx.Rollback().Error
		t.Fatal(err)
	}
	var items int64
	if err := tx.Model(&model.BackupAssetRecoveryPlanItem{}).Count(&items).Error; err != nil {
		_ = tx.Rollback().Error
		t.Fatal(err)
	}
	if plans != 1 || items != int64(len(fixture.request.Selection.AssetRefs)) {
		_ = tx.Rollback().Error
		t.Fatalf("caller transaction rows: plans=%d items=%d", plans, items)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatalf("caller transaction rollback: %v", err)
	}
	assertRecoveryPlanWriteSetEmpty(t, fixture.db)
}

func TestPlanCreateTxLeavesCallerTransactionOwnedOnFailure(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	ensureRecoveryPlanRollbackTables(t, fixture.db)
	injected := errors.New("injected caller transaction failure")
	service, err := NewPlanService(PlanServiceDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
		TargetRootResolver: fixture.resolver,
		beforePersist: func(stage planCreateStage, _ int) error {
			if stage == planCreateBeforePlanInsert {
				return injected
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tx := fixture.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	if _, err := service.CreatePlanTx(context.Background(), tx, fixture.request); !errors.Is(err, injected) {
		t.Fatalf("CreatePlanTx() error = %v, want injected failure", err)
	}
	if err := tx.Model(&model.BackupRepository{}).
		Where("id = ?", fixture.request.Selection.RepositoryID).
		Update("display_name", "caller-transaction-still-owned").Error; err != nil {
		t.Fatalf("caller transaction is no longer usable: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("caller transaction commit: %v", err)
	}
	assertRecoveryPlanWriteSetEmpty(t, fixture.db)
	var repository model.BackupRepository
	if err := fixture.db.Where("id = ?", fixture.request.Selection.RepositoryID).Take(&repository).Error; err != nil {
		t.Fatal(err)
	}
	if repository.DisplayName != "caller-transaction-still-owned" {
		t.Fatalf("caller transaction update was not committed: %q", repository.DisplayName)
	}
}

func TestPlanCreateTxRollsBackItsPartialWriteSetBeforeCallerCommit(t *testing.T) {
	fixture := newPlanServiceTestFixture(t, false)
	ensureRecoveryPlanRollbackTables(t, fixture.db)
	injected := errors.New("injected caller transaction item failure")
	service, err := NewPlanService(PlanServiceDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
		TargetRootResolver: fixture.resolver,
		beforePersist: func(stage planCreateStage, itemOrdinal int) error {
			if stage == planCreateBeforeItemInsert && itemOrdinal == 1 {
				return injected
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tx := fixture.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	if _, err := service.CreatePlanTx(context.Background(), tx, fixture.request); !errors.Is(err, injected) {
		t.Fatalf("CreatePlanTx() error = %v, want injected failure", err)
	}
	if err := tx.Model(&model.BackupRepository{}).
		Where("id = ?", fixture.request.Selection.RepositoryID).
		Update("display_name", "caller-transaction-committed-after-partial-plan").Error; err != nil {
		t.Fatalf("caller transaction is no longer usable: %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("caller transaction commit: %v", err)
	}

	assertRecoveryPlanWriteSetEmpty(t, fixture.db)
	var repository model.BackupRepository
	if err := fixture.db.Where("id = ?", fixture.request.Selection.RepositoryID).Take(&repository).Error; err != nil {
		t.Fatal(err)
	}
	if repository.DisplayName != "caller-transaction-committed-after-partial-plan" {
		t.Fatalf("caller transaction update was not committed: %q", repository.DisplayName)
	}
}

func ensureRecoveryPlanRollbackTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(
		&model.RecoveryPointLease{},
		&model.BackupAssetRecoveryPreflight{},
		&model.BackupAssetRecoveryGrant{},
		&model.BackupAssetRecoveryJob{},
		&model.BackupAssetRecoveryJobItem{},
		&model.BackupAssetRecoveryAttempt{},
		&model.BackupAssetRecoveryCheckpoint{},
		&model.BackupAssetRecoveryEvidence{},
		&model.BackupAssetRecoveryResultSet{},
		&model.BackupAssetRecoveryResult{},
		&model.BackupAssetRecoveryNodeLease{},
	); err != nil {
		t.Fatal(err)
	}
}

func assertRecoveryPlanWriteSetEmpty(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range []any{
		&model.BackupAssetRecoveryPlan{},
		&model.BackupAssetRecoveryPlanItem{},
		&model.BackupAssetRecoveryPreflight{},
		&model.BackupAssetRecoveryGrant{},
		&model.BackupAssetRecoveryJob{},
		&model.BackupAssetRecoveryJobItem{},
		&model.BackupAssetRecoveryAttempt{},
		&model.BackupAssetRecoveryCheckpoint{},
		&model.BackupAssetRecoveryEvidence{},
		&model.BackupAssetRecoveryResultSet{},
		&model.BackupAssetRecoveryResult{},
		&model.RecoveryPointLease{},
		&model.BackupAssetRecoveryNodeLease{},
	} {
		var count int64
		if err := db.Model(table).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%T rows = %d, want 0 after rollback", table, count)
		}
	}
}

func assertPlanCreationRows(t *testing.T, db *gorm.DB, wantItems int) {
	t.Helper()
	var plans int64
	if err := db.Model(&model.BackupAssetRecoveryPlan{}).Count(&plans).Error; err != nil {
		t.Fatal(err)
	}
	var items int64
	if err := db.Model(&model.BackupAssetRecoveryPlanItem{}).Count(&items).Error; err != nil {
		t.Fatal(err)
	}
	if plans != 1 || items != int64(wantItems) {
		t.Fatalf("durable concurrent rows: plans=%d items=%d wantItems=%d", plans, items, wantItems)
	}
}

type planServiceTestFixture struct {
	source            recoverySourceFixture
	db                *gorm.DB
	service           *PlanService
	resolver          *recoveryTargetRootResolverFake
	now               time.Time
	targetRootLocator string
	request           CreatePlanRequest
}

type recoveryTargetRootResolveCall struct {
	NodeID             uint
	RootID             string
	ConnectionPoolType string
}

type recoveryTargetRootResolverFake struct {
	mu         sync.Mutex
	resolution settings.RecoveryTargetRootResolution
	err        error
	resolve    func(context.Context, *gorm.DB, uint, string) (settings.RecoveryTargetRootResolution, error)
	calls      []recoveryTargetRootResolveCall
}

func (fake *recoveryTargetRootResolverFake) ResolveRecoveryTargetRootTx(
	ctx context.Context,
	tx *gorm.DB,
	nodeID uint,
	rootID string,
) (settings.RecoveryTargetRootResolution, error) {
	if fake == nil {
		return settings.RecoveryTargetRootResolution{}, settings.ErrRecoveryTargetRootUnavailable
	}
	fake.mu.Lock()
	fake.calls = append(fake.calls, recoveryTargetRootResolveCall{
		NodeID: nodeID, RootID: rootID, ConnectionPoolType: fmt.Sprintf("%T", tx.Statement.ConnPool),
	})
	resolution, err, resolve := fake.resolution, fake.err, fake.resolve
	fake.mu.Unlock()
	if resolve != nil {
		return resolve(ctx, tx, nodeID, rootID)
	}
	return resolution, err
}

func (fake *recoveryTargetRootResolverFake) configure(
	resolution settings.RecoveryTargetRootResolution,
	err error,
	resolve func(context.Context, *gorm.DB, uint, string) (settings.RecoveryTargetRootResolution, error),
) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.resolution = resolution
	fake.err = err
	fake.resolve = resolve
}

func (fake *recoveryTargetRootResolverFake) snapshotCalls() []recoveryTargetRootResolveCall {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]recoveryTargetRootResolveCall(nil), fake.calls...)
}

func newRecoveryTargetRootResolverFake(
	t *testing.T,
	nodeID uint,
	rootID string,
	locator string,
) *recoveryTargetRootResolverFake {
	t.Helper()
	digest, err := settings.RecoveryTargetRootLocatorDigest(nodeID, rootID, locator)
	if err != nil {
		t.Fatal(err)
	}
	return &recoveryTargetRootResolverFake{resolution: settings.RecoveryTargetRootResolution{
		NodeID: nodeID, RootID: rootID, SafeLabel: "FAKE_PLAN_TARGET_ROOT_LABEL_FOR_TEST_ONLY",
		Locator: locator, LocatorDigest: digest,
	}}
}

func newPlanServiceWithResolverForTest(
	t *testing.T,
	fixture planServiceTestFixture,
	resolver RecoveryTargetRootResolver,
	beforePersist func(planCreateStage, int) error,
) *PlanService {
	t.Helper()
	service, err := NewPlanService(PlanServiceDependencies{
		DB: fixture.db, Now: func() time.Time { return fixture.now },
		TargetRootResolver: resolver, beforePersist: beforePersist,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func assertRecoveryPlanAndItemCounts(t *testing.T, db *gorm.DB, wantPlans, wantItems int64) {
	t.Helper()
	var plans int64
	if err := db.Model(&model.BackupAssetRecoveryPlan{}).Count(&plans).Error; err != nil {
		t.Fatal(err)
	}
	var items int64
	if err := db.Model(&model.BackupAssetRecoveryPlanItem{}).Count(&items).Error; err != nil {
		t.Fatal(err)
	}
	if plans != wantPlans || items != wantItems {
		t.Fatalf("recovery plan write set: plans=%d items=%d, want %d/%d", plans, items, wantPlans, wantItems)
	}
}

func cloneCreatePlanRequest(request CreatePlanRequest) CreatePlanRequest {
	clone := request
	clone.Selection.AssetRefs = append([]backupasset.AssetRef(nil), request.Selection.AssetRefs...)
	clone.Selection.SourceRevision = cloneSourceRevision(request.Selection.SourceRevision)
	clone.Selection.privateSourceBinding = cloneFrozenSourceBinding(request.Selection.privateSourceBinding)
	clone.Plan.Binding.SourceRevision = cloneSourceRevision(request.Plan.Binding.SourceRevision)
	return clone
}

func exactSelectionWithRefs(t *testing.T, selection ExactSelection, refs []backupasset.AssetRef) ExactSelection {
	t.Helper()
	result, err := newExactSelection(ExactSelectionInput{
		RepositoryID: selection.RepositoryID, RecoveryPointID: selection.RecoveryPointID,
		CatalogGenerationID: selection.CatalogGenerationID, AssetRefs: refs, SourceRevision: selection.SourceRevision,
	}, selection.privateSourceBinding)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func exactSelectionWithRevision(t *testing.T, selection ExactSelection, revision SourceRevision) ExactSelection {
	t.Helper()
	result, err := newExactSelection(ExactSelectionInput{
		RepositoryID: selection.RepositoryID, RecoveryPointID: selection.RecoveryPointID,
		CatalogGenerationID: selection.CatalogGenerationID, AssetRefs: selection.AssetRefs, SourceRevision: revision,
	}, selection.privateSourceBinding)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func replacePlanSelection(t *testing.T, request *CreatePlanRequest, selection ExactSelection) {
	t.Helper()
	request.Selection = selection
	request.Plan.Binding.SelectionDigest = selection.SelectionDigest
	request.Plan.Binding.RepositoryID = selection.RepositoryID
	request.Plan.Binding.RecoveryPointID = selection.RecoveryPointID
	request.Plan.Binding.SourceRevisionDigest = selection.SourceRevisionDigest
	request.Plan.Binding.SourceRevision = cloneSourceRevision(selection.SourceRevision)
}

func freezeFixtureSelection(t *testing.T, source recoverySourceFixture, catalogGenerationID string) ExactSelection {
	t.Helper()
	validator, err := NewSourceValidator(source.db)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: source.repositoryID, RecoveryPointID: source.recoveryPointID,
		CatalogGenerationID: catalogGenerationID, AssetRefs: []backupasset.AssetRef{source.directoryRef},
		MaxItems: len(source.fileRefs),
	})
	if err != nil {
		t.Fatal(err)
	}
	return selection
}

func cloneCatalogGeneration(t *testing.T, source recoverySourceFixture, generationID string) {
	t.Helper()
	var generation model.CatalogGeneration
	if err := source.db.Where("id = ?", source.catalogGenerationID).First(&generation).Error; err != nil {
		t.Fatal(err)
	}
	clone := generation
	clone.ID = generationID
	clone.Generation++
	if err := source.db.Create(&clone).Error; err != nil {
		t.Fatal(err)
	}

	var entries []model.CatalogEntry
	if err := source.db.Where("generation_id = ?", source.catalogGenerationID).Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	for index := range entries {
		entries[index].GenerationID = generationID
	}
	if err := source.db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
}

func selectionWithSourceIdentity(
	t *testing.T,
	selection ExactSelection,
	repositoryID, recoveryPointID, catalogGenerationID string,
) ExactSelection {
	t.Helper()
	refs := append([]backupasset.AssetRef(nil), selection.AssetRefs...)
	for index := range refs {
		refs[index].RecoveryPointID = recoveryPointID
	}
	result, err := newExactSelection(ExactSelectionInput{
		RepositoryID: repositoryID, RecoveryPointID: recoveryPointID, CatalogGenerationID: catalogGenerationID,
		AssetRefs: refs, SourceRevision: selection.SourceRevision,
	}, selection.privateSourceBinding)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newPlanServiceTestFixture(t *testing.T, exactMirror bool) planServiceTestFixture {
	return newPlanServiceTestFixtureForSemantics(t, exactMirror, backupasset.PointNativeSnapshot)
}

func newPlanServiceTestFixtureForSemantics(
	t *testing.T,
	exactMirror bool,
	semantics backupasset.PointVersionSemantics,
) planServiceTestFixture {
	t.Helper()
	source := seedRecoverySourceFixture(t, semantics)
	if err := source.db.AutoMigrate(&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryPlanItem{}); err != nil {
		t.Fatal(err)
	}
	if err := source.db.Exec(`CREATE UNIQUE INDEX recovery_plan_test_request_key
		ON backup_asset_recovery_plans(requester_id, endpoint, idempotency_key_digest)`).Error; err != nil {
		t.Fatal(err)
	}
	validator, err := NewSourceValidator(source.db)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := validator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: source.repositoryID, RecoveryPointID: source.recoveryPointID,
		CatalogGenerationID: source.catalogGenerationID, AssetRefs: []backupasset.AssetRef{source.directoryRef},
		MaxItems: len(source.fileRefs),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := source.observedAt.Add(time.Hour)
	targetRootLocator := "/srv/FAKE_PLAN_RECOVERY_TARGET_ROOT_FOR_TEST_ONLY"
	resolver := newRecoveryTargetRootResolverFake(t, 27, "recovery-root", targetRootLocator)
	digest := resolver.resolution.LocatorDigest
	targetRelativePath := "FAKE_SENSITIVE_RECOVERY_TARGET_PATH"
	targetPathDigest := mustTargetPathDigest(t, "recovery-root", digest, targetRelativePath)
	conflictPolicy := ConflictFailOnConflict
	deleteDigest := EmptyDeleteSetDigest
	targetMode := TargetModeIsolated
	if exactMirror {
		conflictPolicy = ConflictExactMirror
		deleteDigest = strings.Repeat("6", 64)
		targetMode = TargetModeInPlace
	}
	request := CreatePlanRequest{
		RequesterID:       101,
		Endpoint:          "recovery_plan_create",
		IdempotencyKey:    "recovery-plan-idempotency-key-0001",
		Selection:         selection,
		AuthorityCategory: AuthorityWrite,
		EstimatedItems:    int64(len(selection.AssetRefs)),
		EstimatedBytes:    3,
		Plan: RecoveryPlan{
			Binding: PlanBinding{
				SchemaVersion: 1, SelectionDigest: selection.SelectionDigest,
				RepositoryID: selection.RepositoryID, RecoveryPointID: selection.RecoveryPointID,
				SourceRevisionDigest: selection.SourceRevisionDigest, SourceRevision: selection.SourceRevision,
				Target: TargetBinding{
					Mode: targetMode, NodeID: 27, RootID: "recovery-root",
					EncryptedRelativePath: targetRelativePath,
					RootLocatorDigest:     digest, PathDigest: targetPathDigest,
					BaseNodeRevision: "node-revision-1", CredentialScopeRevision: "credential-revision-1",
					RootRevision: "root-revision-1", FilesystemRevision: "filesystem-revision-1",
				},
				ConflictPolicy: conflictPolicy, OperationSetDigest: strings.Repeat("5", 64), DeleteSetDigest: deleteDigest,
				CapabilityRevision: "capability-revision-1",
				SecurityDecision: SecurityDecision{
					Kind: SecurityDecisionAllowClean, DecisionDigest: strings.Repeat("4", 64),
					FindingSetDigest: strings.Repeat("3", 64), PolicyRevision: "security-policy-revision-1",
				},
				PreflightRevision: "preflight-revision-1",
			},
			PreflightExpiresAt: now.Add(time.Hour),
		},
	}
	service, err := NewPlanService(PlanServiceDependencies{
		DB: source.db, Now: func() time.Time { return now }, TargetRootResolver: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	return planServiceTestFixture{
		source: source, db: source.db, service: service, resolver: resolver, now: now,
		targetRootLocator: targetRootLocator, request: request,
	}
}

func seedRecoverySourceFixture(t *testing.T, semantics backupasset.PointVersionSemantics) recoverySourceFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RECOVERY_SOURCE_VALIDATOR_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	dsn := fmt.Sprintf(
		"file:%s-%d?mode=memory&cache=shared&_loc=UTC&_foreign_keys=1",
		strings.ReplaceAll(t.Name(), "/", "_"), recoverySourceValidatorDBSequence.Add(1),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.BackupRepository{}, &model.RecoveryPoint{}, &model.RecoveryPointManifest{},
		&model.CatalogGeneration{}, &model.CatalogEntry{},
	); err != nil {
		t.Fatal(err)
	}

	fixture := recoverySourceFixture{
		db:                  db,
		repositoryID:        strings.Repeat("a", 32),
		recoveryPointID:     strings.Repeat("b", 32),
		catalogGenerationID: strings.Repeat("c", 32),
		directoryRef: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("b", 32), EntryID: strings.Repeat("d", 64),
		},
		nestedDirectoryRef: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("b", 32), EntryID: strings.Repeat("5", 64),
		},
		fileRefs: []backupasset.AssetRef{
			{RecoveryPointID: strings.Repeat("b", 32), EntryID: strings.Repeat("1", 64)},
			{RecoveryPointID: strings.Repeat("b", 32), EntryID: strings.Repeat("2", 64)},
		},
		foreignRef: backupasset.AssetRef{
			RecoveryPointID: strings.Repeat("b", 32), EntryID: strings.Repeat("3", 64),
		},
		fakeLocator:       "FAKE_RECOVERY_LOCATOR_FOR_SOURCE_VALIDATOR",
		manifestDigest:    strings.Repeat("e", 64),
		sourceFingerprint: strings.Repeat("f", 64),
		observedAt:        time.Date(2026, 7, 29, 9, 10, 11, 0, time.UTC),
		provider:          backupasset.ProviderRestic,
	}
	if semantics == backupasset.PointMutableHead {
		fixture.provider = backupasset.ProviderRsync
	}
	repository := model.BackupRepository{
		ID: fixture.repositoryID, ProviderKind: string(fixture.provider), DisplayName: "recovery-source",
		VersionMode: string(backupasset.VersionNativeSnapshot), Status: string(backupasset.RepositoryOnline),
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), CapabilityRevision: 1,
	}
	if semantics == backupasset.PointMutableHead {
		repository.VersionMode = string(backupasset.VersionMutableHead)
		repository.ImmutabilityLevel = string(backupasset.ImmutabilityMutable)
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}

	state := backupasset.RecoveryPointCommitted
	if semantics == backupasset.PointMutableHead {
		state = backupasset.RecoveryPointObserved
	}
	point := model.RecoveryPoint{
		ID: fixture.recoveryPointID, RepositoryID: fixture.repositoryID, EncryptedProviderLocator: fixture.fakeLocator,
		Semantics: string(semantics), State: string(state), SourceFingerprint: fixture.sourceFingerprint,
		ManifestDigestAlgorithm: "sha256", ManifestDigest: fixture.manifestDigest, CapabilityRevision: 1,
		ImmutabilityLevel: string(repository.ImmutabilityLevel), PhysicalAvailability: string(backupasset.PhysicalOnline),
		HoldState: string(backupasset.HoldNone), ObservedAt: &fixture.observedAt,
	}
	if semantics != backupasset.PointMutableHead {
		point.CommittedAt = &fixture.observedAt
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	var persistedLocator string
	if err := db.Raw("SELECT encrypted_provider_locator FROM recovery_points WHERE id = ?", fixture.recoveryPointID).Scan(&persistedLocator).Error; err != nil {
		t.Fatal(err)
	}
	if persistedLocator == fixture.fakeLocator || !secure.IsEncrypted(persistedLocator) {
		t.Fatalf("provider locator was not ciphertext at rest: %q", persistedLocator)
	}

	manifestID := strings.Repeat("4", 32)
	manifest := model.RecoveryPointManifest{
		ID: manifestID, RecoveryPointID: fixture.recoveryPointID, Revision: 1, DigestAlgorithm: "sha256",
		Digest: fixture.manifestDigest, Generator: "test", GeneratorVersion: "v1", Completeness: "complete", IsActive: true,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	generation := model.CatalogGeneration{
		ID: fixture.catalogGenerationID, RecoveryPointID: fixture.recoveryPointID, ManifestID: &manifestID,
		Generation: 1, State: "complete", IsActive: true, SourceFingerprint: fixture.sourceFingerprint,
		StartedAt: fixture.observedAt, FinishedAt: &fixture.observedAt,
	}
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}
	secondDirectoryID := fixture.nestedDirectoryRef.EntryID
	entries := []model.CatalogEntry{
		{
			GenerationID: fixture.catalogGenerationID, EntryID: fixture.directoryRef.EntryID, RecoveryPointID: fixture.recoveryPointID,
			NormalizedPath: "/", Name: "root", EntryType: string(backupasset.CatalogEntryDirectory),
		},
		{
			GenerationID: fixture.catalogGenerationID, EntryID: fixture.fileRefs[0].EntryID, RecoveryPointID: fixture.recoveryPointID,
			ParentEntryID: &fixture.directoryRef.EntryID, NormalizedPath: "/one", Name: "one", EntryType: string(backupasset.CatalogEntryFile), Size: 1,
		},
		{
			GenerationID: fixture.catalogGenerationID, EntryID: secondDirectoryID, RecoveryPointID: fixture.recoveryPointID,
			ParentEntryID: &fixture.directoryRef.EntryID, NormalizedPath: "/sub", Name: "sub", EntryType: string(backupasset.CatalogEntryDirectory),
		},
		{
			GenerationID: fixture.catalogGenerationID, EntryID: fixture.fileRefs[1].EntryID, RecoveryPointID: fixture.recoveryPointID,
			ParentEntryID: &secondDirectoryID, NormalizedPath: "/sub/two", Name: "two", EntryType: string(backupasset.CatalogEntryFile), Size: 2,
		},
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	foreignGenerationID := strings.Repeat("6", 32)
	foreignGeneration := model.CatalogGeneration{
		ID: foreignGenerationID, RecoveryPointID: fixture.recoveryPointID, ManifestID: &manifestID,
		Generation: 2, State: "complete", IsActive: true, SourceFingerprint: fixture.sourceFingerprint,
		StartedAt: fixture.observedAt, FinishedAt: &fixture.observedAt,
	}
	if err := db.Create(&foreignGeneration).Error; err != nil {
		t.Fatal(err)
	}
	foreignEntry := model.CatalogEntry{
		GenerationID: foreignGenerationID, EntryID: fixture.foreignRef.EntryID, RecoveryPointID: fixture.recoveryPointID,
		NormalizedPath: "/foreign", Name: "foreign", EntryType: string(backupasset.CatalogEntryFile), Size: 1,
	}
	if err := db.Create(&foreignEntry).Error; err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestRecoveryReconciliationExpectedSetMatrix(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RECOVERY_RECONCILIATION_MATRIX_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	dsn := fmt.Sprintf(
		"file:%s-%d?mode=memory&cache=shared&_loc=UTC&_foreign_keys=1",
		strings.ReplaceAll(t.Name(), "/", "_"), recoverySourceValidatorDBSequence.Add(1),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(
		&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{},
		&model.BackupAssetRecoveryResultSet{}, &model.BackupAssetRecoveryResult{},
	); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 8, 9, 10, 11, 0, time.UTC)
	const (
		nodeID      = uint(91)
		rootID      = "reconcile-root"
		rootLocator = "/srv/FAKE_RECONCILIATION_ROOT_FOR_TEST_ONLY"
	)
	rootDigest, err := settings.RecoveryTargetRootLocatorDigest(nodeID, rootID, rootLocator)
	if err != nil {
		t.Fatal(err)
	}

	type matrixRow struct {
		name             string
		workspacePhase   WorkspacePhase
		workspaceCleanup CleanupPhase
		resultState      ResultSetState
		resultCleanup    CleanupPhase
		remoteState      string
		entryKind        TargetEntryKind
		published        bool
		contributes      bool
	}
	rows := []matrixRow{
		{name: "reserved", workspacePhase: WorkspacePhaseReserved, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "marker-created", workspacePhase: WorkspacePhaseMarkerCreated, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "writing", workspacePhase: WorkspacePhaseWriting, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "sealed", workspacePhase: WorkspacePhaseSealed, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "published-ready", workspacePhase: WorkspacePhasePublished, published: true, resultState: ResultSetStateReady, resultCleanup: CleanupPhaseClaimed, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "published-claimed", workspacePhase: WorkspacePhasePublished, published: true, resultState: ResultSetStateRevoking, resultCleanup: CleanupPhaseClaimed, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "published-revoked", workspacePhase: WorkspacePhasePublished, published: true, resultState: ResultSetStateRevoking, resultCleanup: CleanupPhaseRevoked, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "published-drained", workspacePhase: WorkspacePhasePublished, published: true, resultState: ResultSetStateRevoking, resultCleanup: CleanupPhaseDrained, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "published-validated", workspacePhase: WorkspacePhasePublished, published: true, resultState: ResultSetStateRevoking, resultCleanup: CleanupPhaseValidated, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "published-retryable-drained", workspacePhase: WorkspacePhasePublished, published: true, resultState: ResultSetStateCleanupFailed, resultCleanup: CleanupPhaseDrained, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "published-delete-started", workspacePhase: WorkspacePhasePublished, published: true, resultState: ResultSetStateRevoking, resultCleanup: CleanupPhaseDeleteStarted, remoteState: "delete_started", entryKind: TargetEntryDirectory, contributes: true},
		{name: "published-retryable-delete-started", workspacePhase: WorkspacePhasePublished, published: true, resultState: ResultSetStateCleanupFailed, resultCleanup: CleanupPhaseDeleteStarted, remoteState: "delete_started", entryKind: TargetEntryDirectory, contributes: true},
		{name: "published-deleted", workspacePhase: WorkspacePhasePublished, published: true, resultState: ResultSetStateRevoking, resultCleanup: CleanupPhaseDeleted, remoteState: "absent", entryKind: TargetEntryMissing, contributes: true},
		{name: "published-tombstoned", workspacePhase: WorkspacePhasePublished, published: true, resultState: ResultSetStateCleaned, resultCleanup: CleanupPhaseTombstoned},
		{name: "workspace-cleanup-claimed", workspacePhase: WorkspacePhaseCleanupDue, workspaceCleanup: CleanupPhaseClaimed, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "workspace-cleanup-revoked", workspacePhase: WorkspacePhaseCleanupDue, workspaceCleanup: CleanupPhaseRevoked, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "workspace-cleanup-drained", workspacePhase: WorkspacePhaseCleanupDue, workspaceCleanup: CleanupPhaseDrained, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "workspace-cleanup-validated", workspacePhase: WorkspacePhaseCleanupDue, workspaceCleanup: CleanupPhaseValidated, remoteState: "final", entryKind: TargetEntryDirectory, contributes: true},
		{name: "workspace-cleanup-delete-started", workspacePhase: WorkspacePhaseCleanupDue, workspaceCleanup: CleanupPhaseDeleteStarted, remoteState: "delete_started", entryKind: TargetEntryDirectory, contributes: true},
		{name: "workspace-cleanup-deleted", workspacePhase: WorkspacePhaseCleanupDue, workspaceCleanup: CleanupPhaseDeleted, remoteState: "absent", entryKind: TargetEntryMissing, contributes: true},
		{name: "workspace-cleaned", workspacePhase: WorkspacePhaseCleaned, workspaceCleanup: CleanupPhaseTombstoned},
	}

	type expectedRow struct {
		component     string
		jobID         string
		state         string
		kind          TargetEntryKind
		markerDigest  string
		markerCreator string
		markerFence   uint64
	}
	want := make([]expectedRow, 0, len(rows)*3)
	for index, definition := range rows {
		jobID := fmt.Sprintf("%032x", index+1)
		planID := fmt.Sprintf("%032x", index+1001)
		markerDigest := framedDigest("xirang/recovery/r62-test-marker/v1", jobID)
		markerCreator := "reconcile-worker-" + strconv.Itoa(index+1)
		markerFence := uint64(index + 1)
		plan := model.BackupAssetRecoveryPlan{
			ID: planID, TargetMode: string(TargetModeIsolated), TargetNodeID: nodeID, TargetRootID: rootID,
			EncryptedTargetRootLocator: rootLocator, RootLocatorDigest: rootDigest,
			TargetBaseRevision: "node-revision-r62", CredentialScopeRevision: "credential-revision-r62",
			RootRevision: "root-revision-r62", CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(&plan).Error; err != nil {
			t.Fatalf("create %s plan: %v", definition.name, err)
		}
		jobState := JobStateRunning
		if definition.workspacePhase == WorkspacePhaseSealed || definition.workspacePhase == WorkspacePhasePublished {
			jobState = JobStateSucceeded
		}
		if definition.workspacePhase == WorkspacePhaseCleanupDue || definition.workspacePhase == WorkspacePhaseCleaned {
			jobState = JobStateFailed
		}
		plaintextDeadline := now.Add(24 * time.Hour)
		job := model.BackupAssetRecoveryJob{
			ID: jobID, PlanID: planID, State: string(jobState), TargetMode: string(TargetModeIsolated),
			TargetNodeID: nodeID, TargetRootID: rootID, RootLocatorDigest: rootDigest,
			PathDigest:            framedDigest("xirang/recovery/r62-test-path/v1", jobID),
			PreflightNodeRevision: "node-revision-r62", PreflightTargetRevision: "node-revision-r62",
			WorkspacePhase: string(definition.workspacePhase), WorkspaceCleanupPhase: string(definition.workspaceCleanup),
			EncryptedWorkspaceRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" + jobID,
			WorkspaceBindingDigest:            framedDigest("xirang/recovery/r62-test-workspace/v1", jobID),
			WorkspaceMarkerBindingDigest:      markerDigest, WorkspaceOwner: markerCreator, WorkspaceFence: markerFence,
			PlaintextDeadline: &plaintextDeadline,
			CreatedAt:         now, UpdatedAt: now,
		}
		if definition.workspacePhase != WorkspacePhaseReserved {
			job.WorkspaceMarkerValidationAttemptID = fmt.Sprintf("%032x", index+2001)
			job.WorkspaceMarkerValidationAttemptFence = markerFence
			job.WorkspaceMarkerValidationNodeFence = markerFence
		}
		if definition.workspacePhase == WorkspacePhaseCleanupDue {
			job.WorkspaceCleanupPhase = string(definition.workspaceCleanup)
			if definition.workspaceCleanup == CleanupPhaseDeleteStarted {
				job.WorkspaceCleanupFence = markerFence
				job.WorkspaceCleanupAttempt = 1
			} else {
				job.WorkspaceCleanupOwner = "cleanup-" + markerCreator
				leaseExpiry := now.Add(time.Hour)
				leaseID := fmt.Sprintf("%032x", index+3001)
				job.WorkspaceCleanupLeaseExpiresAt = &leaseExpiry
				job.WorkspaceCleanupFence = markerFence
				job.WorkspaceCleanupNodeLeaseID = &leaseID
				job.WorkspaceCleanupNodeFence = markerFence
				job.WorkspaceCleanupAttempt = 1
			}
		}
		if definition.workspacePhase == WorkspacePhaseCleaned {
			job.WorkspaceCleanupFence = markerFence
			job.WorkspaceCleanupAttempt = 1
		}
		if err := db.Create(&job).Error; err != nil {
			t.Fatalf("create %s job: %v", definition.name, err)
		}

		if definition.published {
			resultSetID := fmt.Sprintf("%032x", index+4001)
			resultSet := model.BackupAssetRecoveryResultSet{
				ID: resultSetID, JobID: jobID, State: string(definition.resultState),
				MarkerBindingDigest: markerDigest, PlaintextDeadline: now.Add(time.Hour),
				HardDeadline: now.Add(2 * time.Hour), CleanupPhase: string(definition.resultCleanup),
				CreatedAt: now, UpdatedAt: now,
			}
			switch definition.resultState {
			case ResultSetStateRevoking:
				leaseExpiry := now.Add(time.Hour)
				leaseID := fmt.Sprintf("%032x", index+5001)
				resultSet.CleanupOwner = "cleanup-" + markerCreator
				resultSet.CleanupLeaseExpiresAt = &leaseExpiry
				resultSet.CleanupFence = markerFence
				resultSet.NodeLeaseID = &leaseID
				resultSet.NodeFence = markerFence
				resultSet.CleanupAttempt = 1
			case ResultSetStateCleanupFailed, ResultSetStateCleaned:
				resultSet.CleanupFence = markerFence
				resultSet.CleanupAttempt = 1
			}
			if err := db.Create(&resultSet).Error; err != nil {
				t.Fatalf("create %s result set: %v", definition.name, err)
			}
			result := model.BackupAssetRecoveryResult{
				ID: fmt.Sprintf("%032x", index+6001), ResultSetID: resultSetID, JobID: jobID,
				ResultKind:             string(RecoveryResultKindRegularFile),
				Classification:         string(RecoveryResultClassificationUnknown),
				ClassificationRevision: 1, ClassificationSourceRevision: 1,
				EncryptedRelativeLocator: "result/" + jobID, LocatorDigest: framedDigest("xirang/recovery/r62-test-result-locator/v1", jobID),
				ContentDigest: framedDigest("xirang/recovery/r62-test-result-content/v1", jobID), CreatedAt: now,
			}
			if err := db.Create(&result).Error; err != nil {
				t.Fatalf("create %s result: %v", definition.name, err)
			}
		}
		if definition.contributes {
			common := []string{
				jobID, rootID, plan.RootRevision, job.PathDigest, markerDigest, markerCreator,
				strconv.FormatUint(markerFence, 10),
			}
			capturedComponent := recoveryOwnedCleanupArtifactPrefix +
				framedDigest(recoveryOwnedCleanupArtifactDomain, common...)
			verifiedComponent := recoveryOwnedCleanupVerifiedPrefix + framedDigest(
				recoveryOwnedCleanupVerifiedDomain, append(common, capturedComponent)...,
			)
			capturedState := recoveryReconciliationRemoteAbsent
			capturedKind := TargetEntryMissing
			verifiedState := recoveryReconciliationRemoteAbsent
			verifiedKind := TargetEntryMissing
			if definition.remoteState == recoveryReconciliationRemoteDeleteStarted {
				capturedState = recoveryReconciliationRemoteDeleteStarted
				capturedKind = TargetEntryDirectory
				verifiedState = recoveryReconciliationRemoteDeleteStarted
				verifiedKind = TargetEntryRegular
			}
			want = append(want, expectedRow{
				component: jobID, jobID: jobID, state: definition.remoteState, kind: definition.entryKind,
				markerDigest: markerDigest, markerCreator: markerCreator, markerFence: markerFence,
			}, expectedRow{
				component: capturedComponent, jobID: jobID, state: capturedState, kind: capturedKind,
				markerDigest: markerDigest, markerCreator: markerCreator, markerFence: markerFence,
			}, expectedRow{
				component: verifiedComponent, jobID: jobID, state: verifiedState, kind: verifiedKind,
				markerDigest: markerDigest, markerCreator: markerCreator, markerFence: markerFence,
			})
		}
	}

	registry := &recoveryReconciliationRootRegistryFake{resolution: settings.RecoveryTargetRootResolution{
		NodeID: nodeID, RootID: rootID, Locator: rootLocator, LocatorDigest: rootDigest,
	}}
	keys := &recoveryReconciliationKeySourceFake{material: backupasset.DomainKeyMaterial{
		ID: strings.Repeat("a", 32), Domain: backupasset.KeyDomainAuditFingerprint,
		Version: 7, State: backupasset.DomainKeyActive, Key: []byte("0123456789abcdef0123456789abcdef"), ActivatedAt: now.Add(-time.Hour),
	}}
	target := &recoveryReconciliationTargetCapture{}
	service, err := NewRecoveryReconciliationService(RecoveryReconciliationServiceDependencies{
		DB: db, Now: func() time.Time { return now }, Roots: registry,
		Revisions: recoveryReconciliationRevisionSourceFake{snapshot: RecoveryReconciliationRevisionSnapshot{
			NodeRevision: "node-revision-r62", CredentialRevision: "credential-revision-r62",
			RootRevision: "root-revision-r62",
		}},
		Keys: keys, Target: target,
		Audit: &authorizationReceiptAuditSpy{}, Findings: &recoveryReconciliationFindingSinkFake{},
	})
	if err != nil {
		t.Fatalf("construct reconciliation service: %v", err)
	}
	request := ReconcileRecoveryRootRequest{NodeID: nodeID, RootID: rootID}
	first, err := service.ReconcileRoot(context.Background(), request)
	if err != nil {
		t.Fatalf("build first reconciliation expected set: %v", err)
	}
	second, err := service.ReconcileRoot(context.Background(), request)
	if err != nil {
		t.Fatalf("build repeated reconciliation expected set: %v", err)
	}
	if first.State != RecoveryReconciliationClear || !first.Complete || second.State != first.State || !second.Complete {
		t.Fatalf("reconciliation products first=%+v second=%+v, want complete clear", first, second)
	}
	if registry.resolveCalls != 2 || !registry.resolvedInTransaction || keys.activeCalls != 2 || len(target.permits) != 2 {
		t.Fatalf("expected-set dependencies roots=%d tx=%t keys=%d permits=%d",
			registry.resolveCalls, registry.resolvedInTransaction, keys.activeCalls, len(target.permits))
	}

	firstPermit, secondPermit := target.permits[0], target.permits[1]
	if firstPermit.proof == nil || secondPermit.proof == nil || !validDigest(firstPermit.ExpectedSetDigest) ||
		firstPermit.ExpectedSetDigest != secondPermit.ExpectedSetDigest ||
		!reflect.DeepEqual(firstPermit.proof.expected, secondPermit.proof.expected) {
		t.Fatalf("expected-set sealing is not deterministic: first=%+v second=%+v", firstPermit, secondPermit)
	}
	if len(firstPermit.proof.expected) != len(want) {
		t.Fatalf("expected component rows=%d, want %d", len(firstPermit.proof.expected), len(want))
	}
	wantByToken := make(map[string]expectedRow, len(want))
	for _, expected := range want {
		row := targetReconciliationExpected{
			jobID: expected.jobID, entryKind: expected.kind, remoteState: expected.state,
			markerBindingDigest: expected.markerDigest, markerCreatorID: expected.markerCreator,
			markerCreatorFence: expected.markerFence,
		}
		token := recoveryReconciliationComponentToken(
			firstPermit.proof.auditTokenKey, firstPermit.proof.auditKeyVersion,
			firstPermit.proof.sessionBinding, expected.component, row,
		)
		if _, duplicate := wantByToken[token]; duplicate {
			t.Fatalf("duplicate expected token for component %q", expected.component)
		}
		wantByToken[token] = expected
	}
	seen := make(map[string]struct{}, len(want))
	for _, actual := range firstPermit.proof.expected {
		expected, ok := wantByToken[actual.componentToken]
		if !ok {
			t.Fatalf("tombstoned or foreign expected component survived: %+v", actual)
		}
		if _, duplicate := seen[actual.componentToken]; duplicate {
			t.Fatalf("duplicate expected component token for job %s", actual.jobID)
		}
		seen[actual.componentToken] = struct{}{}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(actual.componentToken)
		if decodeErr != nil || len(decoded) != 32 || strings.Contains(actual.componentToken, expected.component) ||
			actual.jobID != expected.jobID ||
			actual.entryKind != expected.kind || actual.remoteState != expected.state ||
			actual.markerBindingDigest != expected.markerDigest || actual.markerCreatorID != expected.markerCreator ||
			actual.markerCreatorFence != expected.markerFence {
			t.Fatalf("unexpected sealed expected component: %+v want=%+v", actual, expected)
		}
	}
}

func TestRecoveryReconciliationExpectedSetBindsExactA3cComponents(t *testing.T) {
	tests := []struct {
		name          string
		cleanupDue    bool
		deleteStarted bool
	}{
		{name: "reserved workspace keeps cleanup artifacts absent"},
		{name: "cleanup due before marker validation keeps cleanup artifacts absent", cleanupDue: true},
		{name: "delete started admits final captured and verified", cleanupDue: true, deleteStarted: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryReconciliationServiceTestFixture(t)
			if test.cleanupDue {
				updates := map[string]any{
					"state":           string(JobStateFailed),
					"workspace_phase": string(WorkspacePhaseCleanupDue),
				}
				if test.deleteStarted {
					updates["workspace_marker_validation_attempt_id"] = strings.Repeat("4", 32)
					updates["workspace_marker_validation_attempt_fence"] = uint64(1)
					updates["workspace_marker_validation_node_fence"] = uint64(1)
					updates["workspace_cleanup_phase"] = string(CleanupPhaseDeleteStarted)
					updates["workspace_cleanup_fence"] = uint64(1)
					updates["workspace_cleanup_attempt"] = uint64(1)
				}
				if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).
					Where("id = ?", fixture.job.ID).
					Updates(updates).Error; err != nil {
					t.Fatalf("advance workspace cleanup state: %v", err)
				}
			}

			request := ReconcileRecoveryRootRequest{
				NodeID: fixture.job.TargetNodeID,
				RootID: fixture.job.TargetRootID,
			}
			if _, err := fixture.service.ReconcileRoot(context.Background(), request); err != nil {
				t.Fatalf("build exact A3c expected components: %v", err)
			}
			if len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil {
				t.Fatalf("reconciliation permit=%+v", fixture.target.permits)
			}
			proof := fixture.target.permits[0].proof
			if len(proof.expected) != 3 {
				t.Fatalf("expected component rows=%d, want final, captured and verified", len(proof.expected))
			}

			common := []string{
				fixture.job.ID, fixture.job.TargetRootID, fixture.plan.RootRevision, fixture.job.PathDigest,
				fixture.job.WorkspaceMarkerBindingDigest, fixture.job.WorkspaceOwner,
				strconv.FormatUint(fixture.job.WorkspaceFence, 10),
			}
			capturedComponent := recoveryOwnedCleanupArtifactPrefix +
				framedDigest(recoveryOwnedCleanupArtifactDomain, common...)
			verifiedComponent := recoveryOwnedCleanupVerifiedPrefix + framedDigest(
				recoveryOwnedCleanupVerifiedDomain, append(common, capturedComponent)...,
			)
			type expectedState struct {
				kind  TargetEntryKind
				state string
			}
			want := map[string]expectedState{
				fixture.job.ID:    {kind: TargetEntryDirectory, state: recoveryReconciliationRemoteFinal},
				capturedComponent: {kind: TargetEntryMissing, state: recoveryReconciliationRemoteAbsent},
				verifiedComponent: {kind: TargetEntryMissing, state: recoveryReconciliationRemoteAbsent},
			}
			if test.deleteStarted {
				want[fixture.job.ID] = expectedState{
					kind: TargetEntryDirectory, state: recoveryReconciliationRemoteDeleteStarted,
				}
				want[capturedComponent] = expectedState{
					kind: TargetEntryDirectory, state: recoveryReconciliationRemoteDeleteStarted,
				}
				want[verifiedComponent] = expectedState{
					kind: TargetEntryRegular, state: recoveryReconciliationRemoteDeleteStarted,
				}
			}

			for component, expected := range want {
				buffer := bytes.NewBuffer(nil)
				writeRecoveryDigestString(buffer, recoveryReconciliationComponentDomain)
				writeRecoveryDigestString(buffer, strconv.Itoa(proof.auditKeyVersion))
				writeRecoveryDigestString(buffer, proof.sessionBinding.bindingDigest)
				writeRecoveryDigestString(buffer, component)
				writeRecoveryDigestString(buffer, string(expected.kind))
				writeRecoveryDigestString(buffer, expected.state)
				mac := hmac.New(sha256.New, proof.auditTokenKey[:])
				_, _ = mac.Write(buffer.Bytes())
				token := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

				matched := false
				for _, actual := range proof.expected {
					if actual.componentToken == token {
						matched = actual.jobID == fixture.job.ID && actual.entryKind == expected.kind &&
							actual.remoteState == expected.state
						break
					}
				}
				if !matched {
					t.Fatalf("exact component %q did not match a sealed expected token", component)
				}
			}
		})
	}
}

func TestRecoveryReconciliationExpectedSetDoesNotDependOnHistoricalPlanPopulation(t *testing.T) {
	t.Run("unreserved isolated job has no remote component", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).
			Where("id = ?", fixture.job.ID).
			Updates(map[string]any{
				"workspace_phase":                 string(WorkspacePhaseNone),
				"workspace_marker_binding_digest": "",
				"workspace_owner":                 "",
				"workspace_fence":                 uint64(0),
				"plaintext_deadline":              nil,
			}).Error; err != nil {
			t.Fatalf("return fixture to unreserved state: %v", err)
		}

		result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
			NodeID: fixture.job.TargetNodeID,
			RootID: fixture.job.TargetRootID,
		})
		if err != nil || result.State != RecoveryReconciliationClear || !result.Complete {
			t.Fatalf("unreserved isolated result=%+v error=%v, want a real complete scan", result, err)
		}
		if len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil ||
			len(fixture.target.permits[0].proof.expected) != 0 {
			t.Fatalf("unreserved isolated job unexpectedly contributed a component: %+v", fixture.target.permits)
		}
	})

	t.Run("registered root with no jobs still scans", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		if err := fixture.db.Delete(&model.BackupAssetRecoveryJob{}, "id = ?", fixture.job.ID).Error; err != nil {
			t.Fatalf("remove the only recovery job: %v", err)
		}

		result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
			NodeID: fixture.job.TargetNodeID,
			RootID: fixture.job.TargetRootID,
		})
		if err != nil || result.State != RecoveryReconciliationClear || !result.Complete {
			t.Fatalf("empty registered root result=%+v error=%v, want a real complete scan", result, err)
		}
		if len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil ||
			len(fixture.target.permits[0].proof.expected) != 0 {
			t.Fatalf("empty registered root did not open one zero-expected scan: %+v", fixture.target.permits)
		}
	})

	t.Run("historical plan root revisions share one current scan", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		secondPlan := fixture.plan
		secondPlan.ID = strings.Repeat("5", 32)
		secondPlan.RootRevision = "historical-root-revision-r62"
		if err := fixture.db.Create(&secondPlan).Error; err != nil {
			t.Fatalf("create historical plan revision: %v", err)
		}
		secondJob := fixture.job
		secondJob.ID = strings.Repeat("6", 32)
		secondJob.PlanID = secondPlan.ID
		secondJob.EncryptedWorkspaceRelativeLocator = recoveryWorkspaceLocatorDirectory + "/" + secondJob.ID
		secondJob.PathDigest = framedDigest("xirang/recovery/r62-historical-path/v1", secondJob.ID)
		secondJob.WorkspaceBindingDigest = framedDigest("xirang/recovery/r62-historical-workspace/v1", secondJob.ID)
		secondJob.WorkspaceMarkerBindingDigest = framedDigest("xirang/recovery/r62-historical-marker/v1", secondJob.ID)
		secondJob.WorkspaceOwner = "r62-historical-worker"
		if err := fixture.db.Create(&secondJob).Error; err != nil {
			t.Fatalf("create historical job revision: %v", err)
		}

		result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
			NodeID: fixture.job.TargetNodeID,
			RootID: fixture.job.TargetRootID,
		})
		if err != nil || result.State != RecoveryReconciliationClear || !result.Complete {
			t.Fatalf("historical revisions result=%+v error=%v, want one current complete scan", result, err)
		}
		if len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil ||
			len(fixture.target.permits[0].proof.expected) != 6 {
			t.Fatalf("historical revision expected set=%+v, want six exact components", fixture.target.permits)
		}
	})
}

func TestRecoveryReconciliationExpectedSetBoundsComponentsNotPublishedItems(t *testing.T) {
	fixture := newRecoveryReconciliationServiceTestFixture(t)
	if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).
		Where("id = ?", fixture.job.ID).
		Updates(map[string]any{
			"state":                                  string(JobStateSucceeded),
			"workspace_phase":                        string(WorkspacePhasePublished),
			"workspace_marker_validation_attempt_id": strings.Repeat("7", 32),
			"workspace_marker_validation_attempt_fence": uint64(1),
			"workspace_marker_validation_node_fence":    uint64(1),
		}).Error; err != nil {
		t.Fatalf("publish large result workspace: %v", err)
	}
	resultSetID := strings.Repeat("8", 32)
	if err := fixture.db.Create(&model.BackupAssetRecoveryResultSet{
		ID: resultSetID, JobID: fixture.job.ID, State: string(ResultSetStateReady),
		MarkerBindingDigest: fixture.job.WorkspaceMarkerBindingDigest,
		PlaintextDeadline:   fixture.now.Add(time.Hour), HardDeadline: fixture.now.Add(2 * time.Hour),
		CleanupPhase: string(CleanupPhaseClaimed), CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}).Error; err != nil {
		t.Fatalf("create large published result set: %v", err)
	}
	results := make([]model.BackupAssetRecoveryResult, 0, recoveryReconciliationExpectedLimit+1)
	for index := 0; index <= recoveryReconciliationExpectedLimit; index++ {
		resultID := fmt.Sprintf("%032x", index+70000)
		results = append(results, model.BackupAssetRecoveryResult{
			ID: resultID, ResultSetID: resultSetID, JobID: fixture.job.ID,
			ResultKind: string(RecoveryResultKindRegularFile), Classification: string(RecoveryResultClassificationUnknown),
			ClassificationRevision: 1, ClassificationSourceRevision: 1,
			EncryptedRelativeLocator: "result/" + resultID,
			LocatorDigest:            framedDigest("xirang/recovery/r62-large-result-locator/v1", resultID),
			ContentDigest:            framedDigest("xirang/recovery/r62-large-result-content/v1", resultID),
			CreatedAt:                fixture.now,
		})
	}
	if err := fixture.db.CreateInBatches(results, 128).Error; err != nil {
		t.Fatalf("create large published result rows: %v", err)
	}

	result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
		NodeID: fixture.job.TargetNodeID,
		RootID: fixture.job.TargetRootID,
	})
	if err != nil || result.State != RecoveryReconciliationClear || !result.Complete {
		t.Fatalf("large published result set=%+v error=%v, want component-bounded scan", result, err)
	}
	if len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil ||
		len(fixture.target.permits[0].proof.expected) != 3 {
		t.Fatalf("large published result expected set=%+v, want three remote components", fixture.target.permits)
	}
}

func TestRecoveryReconciliationExpectedSetDoesNotBoundTombstoneHistory(t *testing.T) {
	fixture := newRecoveryReconciliationServiceTestFixture(t)
	tombstone := func(job *model.BackupAssetRecoveryJob) {
		job.State = string(JobStateFailed)
		job.WorkspacePhase = string(WorkspacePhaseCleaned)
		job.WorkspaceCleanupPhase = string(CleanupPhaseTombstoned)
		job.WorkspaceCleanupOwner = ""
		job.WorkspaceCleanupLeaseExpiresAt = nil
		job.WorkspaceCleanupFence = 1
		job.WorkspaceCleanupNodeLeaseID = nil
		job.WorkspaceCleanupNodeFence = 0
		job.WorkspaceCleanupAttempt = 1
	}
	tombstone(&fixture.job)
	if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).
		Where("id = ?", fixture.job.ID).
		Updates(map[string]any{
			"state":                              fixture.job.State,
			"workspace_phase":                    fixture.job.WorkspacePhase,
			"workspace_cleanup_phase":            fixture.job.WorkspaceCleanupPhase,
			"workspace_cleanup_owner":            fixture.job.WorkspaceCleanupOwner,
			"workspace_cleanup_lease_expires_at": nil,
			"workspace_cleanup_fence":            fixture.job.WorkspaceCleanupFence,
			"workspace_cleanup_node_lease_id":    nil,
			"workspace_cleanup_node_fence":       fixture.job.WorkspaceCleanupNodeFence,
			"workspace_cleanup_attempt":          fixture.job.WorkspaceCleanupAttempt,
		}).Error; err != nil {
		t.Fatalf("tombstone fixture workspace: %v", err)
	}

	plans := make([]model.BackupAssetRecoveryPlan, 0, recoveryReconciliationExpectedLimit)
	jobs := make([]model.BackupAssetRecoveryJob, 0, recoveryReconciliationExpectedLimit)
	for index := 0; index < recoveryReconciliationExpectedLimit; index++ {
		jobID := fmt.Sprintf("%032x", index+90000)
		plan := fixture.plan
		plan.ID = fmt.Sprintf("%032x", index+100000)
		plans = append(plans, plan)
		job := fixture.job
		job.ID = jobID
		job.PlanID = plan.ID
		job.EncryptedWorkspaceRelativeLocator = recoveryWorkspaceLocatorDirectory + "/" + jobID
		job.PathDigest = framedDigest("xirang/recovery/r62-tombstone-path/v1", jobID)
		job.WorkspaceBindingDigest = framedDigest("xirang/recovery/r62-tombstone-workspace/v1", jobID)
		job.WorkspaceMarkerBindingDigest = framedDigest("xirang/recovery/r62-tombstone-marker/v1", jobID)
		job.WorkspaceOwner = "r62-tombstone-worker-" + strconv.Itoa(index)
		tombstone(&job)
		jobs = append(jobs, job)
	}
	if err := fixture.db.CreateInBatches(plans, 128).Error; err != nil {
		t.Fatalf("create tombstone history plans: %v", err)
	}
	if err := fixture.db.CreateInBatches(jobs, 128).Error; err != nil {
		t.Fatalf("create tombstone history jobs: %v", err)
	}

	result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
		NodeID: fixture.job.TargetNodeID,
		RootID: fixture.job.TargetRootID,
	})
	if err != nil || result.State != RecoveryReconciliationClear || !result.Complete {
		t.Fatalf("tombstone history result=%+v error=%v, want real zero-expected scan", result, err)
	}
	if len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil ||
		len(fixture.target.permits[0].proof.expected) != 0 {
		t.Fatalf("tombstone history unexpectedly consumed expected components: %+v", fixture.target.permits)
	}
}

func TestRecoveryReconciliationExpectedSetIncompleteFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *recoveryReconciliationServiceTestFixture)
	}{
		{name: "malformed encrypted locator", mutate: func(t *testing.T, fixture *recoveryReconciliationServiceTestFixture) {
			if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).Where("id = ?", fixture.job.ID).
				UpdateColumn("encrypted_workspace_relative_locator", "enc:v2:FAKE_MALFORMED_R62_LOCATOR").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing plan root revision", mutate: func(t *testing.T, fixture *recoveryReconciliationServiceTestFixture) {
			if err := fixture.db.Table((model.BackupAssetRecoveryPlan{}).TableName()).Where("id = ?", fixture.plan.ID).
				UpdateColumn("root_revision", "").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing current revision snapshot", mutate: func(_ *testing.T, fixture *recoveryReconciliationServiceTestFixture) {
			fixture.service.revisions = recoveryReconciliationRevisionSourceFake{}
		}},
		{name: "illegal job state for reserved workspace", mutate: func(t *testing.T, fixture *recoveryReconciliationServiceTestFixture) {
			if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).Where("id = ?", fixture.job.ID).
				UpdateColumn("state", string(JobStateQueued)).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "invalid workspace cleanup ownership shape", mutate: func(t *testing.T, fixture *recoveryReconciliationServiceTestFixture) {
			if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).Where("id = ?", fixture.job.ID).
				Updates(map[string]any{
					"state":                                     string(JobStateFailed),
					"workspace_phase":                           string(WorkspacePhaseCleanupDue),
					"workspace_cleanup_phase":                   string(CleanupPhaseDeleteStarted),
					"workspace_marker_validation_attempt_id":    strings.Repeat("6", 32),
					"workspace_marker_validation_attempt_fence": 1,
					"workspace_marker_validation_node_fence":    1,
				}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "impossible published relation", mutate: func(t *testing.T, fixture *recoveryReconciliationServiceTestFixture) {
			if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).Where("id = ?", fixture.job.ID).
				UpdateColumn("workspace_phase", string(WorkspacePhasePublished)).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "invalid result set cleanup ownership shape", mutate: func(t *testing.T, fixture *recoveryReconciliationServiceTestFixture) {
			if err := fixture.db.Table((model.BackupAssetRecoveryJob{}).TableName()).Where("id = ?", fixture.job.ID).
				Updates(map[string]any{
					"state":                                  string(JobStateSucceeded),
					"workspace_phase":                        string(WorkspacePhasePublished),
					"workspace_marker_validation_attempt_id": strings.Repeat("7", 32),
					"workspace_marker_validation_attempt_fence": 1,
					"workspace_marker_validation_node_fence":    1,
				}).Error; err != nil {
				t.Fatal(err)
			}
			resultSetID := strings.Repeat("8", 32)
			if err := fixture.db.Create(&model.BackupAssetRecoveryResultSet{
				ID: resultSetID, JobID: fixture.job.ID, State: string(ResultSetStateReady),
				MarkerBindingDigest: fixture.job.WorkspaceMarkerBindingDigest,
				PlaintextDeadline:   fixture.now.Add(time.Hour), HardDeadline: fixture.now.Add(2 * time.Hour),
				CleanupPhase: string(CleanupPhaseClaimed), CleanupFence: 1,
				CreatedAt: fixture.now, UpdatedAt: fixture.now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.Create(&model.BackupAssetRecoveryResult{
				ID: strings.Repeat("9", 32), ResultSetID: resultSetID, JobID: fixture.job.ID,
				ResultKind: string(RecoveryResultKindRegularFile), Classification: string(RecoveryResultClassificationUnknown),
				ClassificationRevision: 1, ClassificationSourceRevision: 1,
				EncryptedRelativeLocator: "result/invalid-cleanup-shape",
				LocatorDigest:            framedDigest("xirang/recovery/r62-invalid-result-locator/v1", fixture.job.ID),
				ContentDigest:            framedDigest("xirang/recovery/r62-invalid-result-content/v1", fixture.job.ID),
				CreatedAt:                fixture.now,
			}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "database query failure", mutate: func(t *testing.T, fixture *recoveryReconciliationServiceTestFixture) {
			callbackName := "task7:r62_expected_set_query_failure_" + strings.ReplaceAll(t.Name(), "/", "_")
			if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement != nil && tx.Statement.Table == (model.BackupAssetRecoveryJob{}).TableName() {
					_ = tx.AddError(errors.New("RAW_R62_EXPECTED_SET_QUERY_FAILURE"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callbackName) })
		}},
		{name: "expected component limit plus one", mutate: func(t *testing.T, fixture *recoveryReconciliationServiceTestFixture) {
			const additionalJobs = recoveryReconciliationExpectedLimit/3 + 1 - 1
			plans := make([]model.BackupAssetRecoveryPlan, 0, additionalJobs)
			jobs := make([]model.BackupAssetRecoveryJob, 0, additionalJobs)
			for index := 0; index < additionalJobs; index++ {
				jobID := fmt.Sprintf("%032x", index+10000)
				plan := fixture.plan
				plan.ID = fmt.Sprintf("%032x", index+20000)
				plans = append(plans, plan)
				job := fixture.job
				job.ID = jobID
				job.PlanID = plan.ID
				job.EncryptedWorkspaceRelativeLocator = recoveryWorkspaceLocatorDirectory + "/" + jobID
				job.PathDigest = framedDigest("xirang/recovery/r62-limit-path/v1", jobID)
				job.WorkspaceBindingDigest = framedDigest("xirang/recovery/r62-limit-workspace/v1", jobID)
				job.WorkspaceMarkerBindingDigest = framedDigest("xirang/recovery/r62-limit-marker/v1", jobID)
				job.WorkspaceOwner = "r62-limit-worker-" + strconv.Itoa(index)
				jobs = append(jobs, job)
			}
			if err := fixture.db.CreateInBatches(plans, 128).Error; err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.CreateInBatches(jobs, 128).Error; err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryReconciliationServiceTestFixture(t)
			test.mutate(t, fixture)
			result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
				NodeID: fixture.job.TargetNodeID, RootID: fixture.job.TargetRootID,
			})
			if err != nil {
				t.Fatalf("incomplete expected set returned error: %v", err)
			}
			if result.State != RecoveryReconciliationBlocked || result.Complete || result.NextCursor != "" ||
				result.Counts != (RecoveryReconciliationCounts{ScanIncomplete: 1}) || len(result.Findings) != 1 ||
				result.Findings[0].Category != RecoveryReconciliationScanIncomplete || result.Findings[0].JobID != "" {
				t.Fatalf("incomplete expected-set result=%+v", result)
			}
			fingerprint, decodeErr := base64.RawURLEncoding.DecodeString(result.Findings[0].Fingerprint)
			if decodeErr != nil || len(fingerprint) != 32 {
				t.Fatalf("scan-incomplete fingerprint=%q error=%v", result.Findings[0].Fingerprint, decodeErr)
			}
			if len(fixture.target.permits) != 0 || len(fixture.target.requests) != 0 {
				t.Fatalf("incomplete expected set crossed target: permits=%d requests=%d",
					len(fixture.target.permits), len(fixture.target.requests))
			}
		})
	}
}

func TestRecoveryReconciliationComponentTokenPrivacy(t *testing.T) {
	fixture := newRecoveryReconciliationServiceTestFixture(t)
	keys, ok := fixture.service.keys.(*recoveryReconciliationKeySourceFake)
	if !ok {
		t.Fatalf("unexpected reconciliation key source %T", fixture.service.keys)
	}
	request := ReconcileRecoveryRootRequest{NodeID: fixture.job.TargetNodeID, RootID: fixture.job.TargetRootID}
	if _, err := fixture.service.ReconcileRoot(context.Background(), request); err != nil {
		t.Fatalf("issue privacy-vector reconciliation permit: %v", err)
	}
	if len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil ||
		len(fixture.target.permits[0].proof.expected) != 3 {
		t.Fatalf("privacy-vector permits=%+v", fixture.target.permits)
	}
	permit := fixture.target.permits[0]
	proof := permit.proof
	var expected targetReconciliationExpected
	for _, candidate := range proof.expected {
		if candidate.entryKind == TargetEntryDirectory && candidate.remoteState == recoveryReconciliationRemoteFinal {
			expected = candidate
			break
		}
	}
	if expected.componentToken == "" {
		t.Fatalf("final workspace component missing from sealed proof: %+v", permit)
	}
	buffer := bytes.NewBuffer(nil)
	writeRecoveryDigestString(buffer, recoveryReconciliationComponentDomain)
	writeRecoveryDigestString(buffer, strconv.Itoa(proof.auditKeyVersion))
	writeRecoveryDigestString(buffer, proof.sessionBinding.bindingDigest)
	writeRecoveryDigestString(buffer, fixture.job.ID)
	writeRecoveryDigestString(buffer, string(expected.entryKind))
	writeRecoveryDigestString(buffer, expected.remoteState)
	mac := hmac.New(sha256.New, keys.material.Key)
	_, _ = mac.Write(buffer.Bytes())
	wantToken := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if expected.componentToken != wantToken || expected.jobID != fixture.job.ID ||
		expected.entryKind != TargetEntryDirectory || expected.remoteState != recoveryReconciliationRemoteFinal ||
		proof.auditKeyVersion != keys.material.Version || proof.auditTokenKey != [32]byte(keys.material.Key) {
		t.Fatalf("sealed component token=%+v proof=%+v want_token=%q", expected, proof, wantToken)
	}
	for index, issued := range keys.issuedKeys {
		if !allZeroBytes(issued) {
			t.Fatalf("issued audit key copy %d was not cleared", index)
		}
	}
	encoded, err := json.Marshal(permit)
	if err != nil {
		t.Fatal(err)
	}
	revisionSnapshot := RecoveryReconciliationRevisionSnapshot{
		NodeRevision:       "FAKE_PRIVATE_R62_NODE_REVISION",
		CredentialRevision: "FAKE_PRIVATE_R62_CREDENTIAL_REVISION",
		RootRevision:       "FAKE_PRIVATE_R62_ROOT_REVISION",
	}
	revisionEncoded, err := json.Marshal(revisionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	formatted := strings.Join([]string{
		fmt.Sprintf("%v", permit), fmt.Sprintf("%+v", permit), fmt.Sprintf("%#v", permit), string(encoded),
		fmt.Sprintf("%v", proof), fmt.Sprintf("%+v", proof), fmt.Sprintf("%#v", proof),
		fmt.Sprintf("%v", revisionSnapshot), fmt.Sprintf("%+v", revisionSnapshot),
		fmt.Sprintf("%#v", revisionSnapshot), string(revisionEncoded),
	}, "\n")
	capturedComponent, verifiedComponent := recoveryOwnedCleanupComponents(
		fixture.job.ID, fixture.job.TargetRootID, fixture.plan.RootRevision, fixture.job.PathDigest,
		fixture.job.WorkspaceMarkerBindingDigest, fixture.job.WorkspaceOwner, fixture.job.WorkspaceFence,
	)
	privateValues := []string{
		fixture.plan.EncryptedTargetRootLocator, fixture.job.ID, fixture.job.WorkspaceMarkerBindingDigest,
		fixture.job.WorkspaceOwner, capturedComponent, verifiedComponent, string(keys.material.Key),
		revisionSnapshot.NodeRevision, revisionSnapshot.CredentialRevision, revisionSnapshot.RootRevision,
	}
	for _, row := range proof.expected {
		privateValues = append(privateValues, row.componentToken)
	}
	for _, private := range privateValues {
		if private != "" && strings.Contains(formatted, private) {
			t.Fatalf("reconciliation permit formatting leaked private value %q: %s", private, formatted)
		}
	}

	proof.expected[0].componentToken = "caller-mutated-token"
	proof.auditTokenKey[0] ^= 0xff
	if _, err := fixture.service.ReconcileRoot(context.Background(), request); err != nil {
		t.Fatalf("rebuild reconciliation permit after caller mutation: %v", err)
	}
	if len(fixture.target.permits) != 2 || fixture.target.permits[1].proof == nil ||
		len(fixture.target.permits[1].proof.expected) != 3 ||
		fixture.target.permits[1].proof.auditTokenKey != [32]byte(keys.material.Key) {
		t.Fatalf("caller mutation reached rebuilt sealed proof: %+v", fixture.target.permits[1])
	}
	rebuiltFinal := false
	for _, candidate := range fixture.target.permits[1].proof.expected {
		rebuiltFinal = rebuiltFinal || candidate.componentToken == wantToken
	}
	if !rebuiltFinal {
		t.Fatalf("caller mutation changed rebuilt final component token: %+v", fixture.target.permits[1])
	}
}

func TestRecoveryReconciliationPermitRejectsSubstitutionBeforeTargetDependency(t *testing.T) {
	fixture := newRecoveryReconciliationServiceTestFixture(t)
	request := ReconcileRecoveryRootRequest{NodeID: fixture.job.TargetNodeID, RootID: fixture.job.TargetRootID}
	if _, err := fixture.service.ReconcileRoot(context.Background(), request); err != nil {
		t.Fatalf("issue reconciliation permit fixture: %v", err)
	}
	if len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil {
		t.Fatalf("reconciliation permit fixture=%+v", fixture.target.permits)
	}
	permit := fixture.target.permits[0]
	targetRequest := TargetReconciliationRequest{RootID: request.RootID}
	if err := permit.ValidateRequestAt(fixture.now, targetRequest); err != nil {
		t.Fatalf("valid reconciliation permit rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*TargetReconciliationPermit, *TargetReconciliationRequest)
	}{
		{name: "node", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) { value.NodeID++ }},
		{name: "root id", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) { value.RootID = "other-root" }},
		{name: "request root", mutate: func(_ *TargetReconciliationPermit, value *TargetReconciliationRequest) { value.RootID = "other-root" }},
		{name: "root locator digest", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.RootLocatorDigest = strings.Repeat("1", 64)
		}},
		{name: "root revision", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.RootRevision = "root-revision-substituted"
		}},
		{name: "expected digest", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.ExpectedSetDigest = strings.Repeat("2", 64)
		}},
		{name: "page bound", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) { value.PageLimit-- }},
		{name: "chain bound", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) { value.ChainLimit-- }},
		{name: "finding bound", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) { value.FindingLimit-- }},
		{name: "cursor", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.Cursor = "substituted-cursor"
		}},
		{name: "expiry", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.ExpiresAt = value.ExpiresAt.Add(time.Second)
		}},
		{name: "admission generation", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.AdmissionGeneration = "admission-substituted"
		}},
		{name: "cleanup purpose", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.Purpose = TargetPurposeCleanup
		}},
		{name: "write purpose", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.Purpose = TargetPurposeWrite
		}},
		{name: "operation", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) { value.Operation = "cleanup" }},
		{name: "session node revision", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.proof.sessionBinding.nodeRevision = "node-revision-substituted"
		}},
		{name: "session credential revision", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.proof.sessionBinding.credentialRevision = "credential-revision-substituted"
		}},
		{name: "audit key version", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) { value.proof.auditKeyVersion++ }},
		{name: "audit token key", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.proof.auditTokenKey[0] ^= 0xff
		}},
		{name: "component token", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.proof.expected[0].componentToken = "substituted-token"
		}},
		{name: "proof binding", mutate: func(value *TargetReconciliationPermit, _ *TargetReconciliationRequest) {
			value.proof.bindingDigest = strings.Repeat("3", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneRecoveryReconciliationPermitForTest(permit)
			candidateRequest := targetRequest
			test.mutate(&candidate, &candidateRequest)
			target := &recoveryReconciliationPermitValidationTarget{now: fixture.now}
			if _, err := target.ScanRecoveryRoot(context.Background(), candidate, candidateRequest); !errors.Is(err, ErrInvalidTargetPermit) {
				t.Fatalf("substituted reconciliation permit error=%v, want ErrInvalidTargetPermit", err)
			}
			if target.dependencyCalls != 0 {
				t.Fatalf("substituted reconciliation permit opened %d target dependencies", target.dependencyCalls)
			}
		})
	}

	if err := permit.ValidateRequestAt(permit.ExpiresAt, targetRequest); !errors.Is(err, ErrInvalidTargetPermit) {
		t.Fatalf("expired reconciliation permit error=%v, want ErrInvalidTargetPermit", err)
	}
	if err := (TargetReconciliationPermit{}).ValidateRequestAt(fixture.now, targetRequest); !errors.Is(err, ErrInvalidTargetPermit) {
		t.Fatalf("zero/cross-purpose reconciliation permit error=%v, want ErrInvalidTargetPermit", err)
	}
}

func cloneRecoveryReconciliationPermitForTest(permit TargetReconciliationPermit) TargetReconciliationPermit {
	clone := permit
	if permit.proof != nil {
		proof := *permit.proof
		proof.expected = append([]targetReconciliationExpected(nil), permit.proof.expected...)
		clone.proof = &proof
	}
	return clone
}

func allZeroBytes(value []byte) bool {
	for _, current := range value {
		if current != 0 {
			return false
		}
	}
	return true
}

func TestRecoveryReconciliationExpectedSetConcurrentSQLite(t *testing.T) {
	testRecoveryReconciliationExpectedSetConcurrent(t, newRecoveryReconciliationServiceTestFixture(t))
}

func TestRecoveryReconciliationExpectedSetConcurrentPostgres(t *testing.T) {
	testRecoveryReconciliationExpectedSetConcurrent(
		t,
		newRecoveryReconciliationServiceTestFixtureOnDB(t, newAuthorizationReceiptPostgresScopedDB(t)),
	)
}

func TestRecoveryReconciliationExpectedSetPostgresUsesStableSnapshot(t *testing.T) {
	fixture := newRecoveryReconciliationServiceTestFixtureOnDB(t, newAuthorizationReceiptPostgresScopedDB(t))
	jobsRead := make(chan struct{})
	continueSnapshot := make(chan struct{})
	var jobsReadOnce sync.Once
	callbackName := "task7:r62_postgres_stable_snapshot_" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := fixture.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != (model.BackupAssetRecoveryJob{}).TableName() {
			return
		}
		jobsReadOnce.Do(func() {
			close(jobsRead)
			<-continueSnapshot
		})
	}); err != nil {
		t.Fatalf("register stable-snapshot barrier: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove(callbackName) })

	type outcome struct {
		result RecoveryReconciliationResult
		err    error
	}
	outcomes := make(chan outcome, 1)
	go func() {
		result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
			NodeID: fixture.job.TargetNodeID,
			RootID: fixture.job.TargetRootID,
		})
		outcomes <- outcome{result: result, err: err}
	}()

	select {
	case <-jobsRead:
	case <-time.After(5 * time.Second):
		close(continueSnapshot)
		t.Fatal("reconciliation did not establish the jobs snapshot")
	}
	driftedRevision := "node-revision-r62-after-snapshot"
	if err := fixture.db.Table((model.BackupAssetRecoveryPlan{}).TableName()).
		Where("id = ?", fixture.plan.ID).
		UpdateColumn("target_base_revision", driftedRevision).Error; err != nil {
		close(continueSnapshot)
		t.Fatalf("commit plan revision drift between expected-set queries: %v", err)
	}
	close(continueSnapshot)

	var currentPlan model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.plan.ID).Take(&currentPlan).Error; err != nil {
		t.Fatalf("load drifted plan: %v", err)
	}
	if currentPlan.TargetBaseRevision != driftedRevision {
		t.Fatalf("plan revision=%q, want committed drift %q", currentPlan.TargetBaseRevision, driftedRevision)
	}

	select {
	case current := <-outcomes:
		if current.err != nil || current.result.State != RecoveryReconciliationClear || !current.result.Complete {
			t.Fatalf("stable expected snapshot result=%+v error=%v", current.result, current.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reconciliation did not finish after releasing the snapshot barrier")
	}
	if len(fixture.target.permits) != 1 || fixture.target.permits[0].proof == nil ||
		fixture.target.permits[0].proof.sessionBinding.nodeRevision != fixture.plan.TargetBaseRevision {
		t.Fatalf("permit did not retain the pre-drift snapshot: %+v", fixture.target.permits)
	}
}

func testRecoveryReconciliationExpectedSetConcurrent(
	t *testing.T,
	fixture *recoveryReconciliationServiceTestFixture,
) {
	t.Helper()
	request := ReconcileRecoveryRootRequest{NodeID: fixture.job.TargetNodeID, RootID: fixture.job.TargetRootID}
	type outcome struct {
		result RecoveryReconciliationResult
		err    error
	}
	const callers = 4
	start := make(chan struct{})
	outcomes := make(chan outcome, callers)
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := fixture.service.ReconcileRoot(context.Background(), request)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(outcomes)
	for current := range outcomes {
		if current.err != nil || current.result.State != RecoveryReconciliationClear || !current.result.Complete {
			t.Fatalf("concurrent expected-set result=%+v error=%v", current.result, current.err)
		}
	}
	if len(fixture.target.permits) != callers {
		t.Fatalf("concurrent expected-set permits=%d, want %d", len(fixture.target.permits), callers)
	}
	wantDigest := fixture.target.permits[0].ExpectedSetDigest
	wantExpected := fixture.target.permits[0].proof.expected
	for index, permit := range fixture.target.permits {
		if permit.ExpectedSetDigest != wantDigest || !reflect.DeepEqual(permit.proof.expected, wantExpected) ||
			permit.ValidateRequestAt(fixture.now, TargetReconciliationRequest{RootID: request.RootID}) != nil {
			t.Fatalf("concurrent permit %d drifted: %+v", index, permit)
		}
	}
}

type recoveryReconciliationServiceTestFixture struct {
	db       *gorm.DB
	now      time.Time
	plan     model.BackupAssetRecoveryPlan
	job      model.BackupAssetRecoveryJob
	service  *RecoveryReconciliationService
	target   *recoveryReconciliationTargetCapture
	audit    *authorizationReceiptAuditSpy
	findings *recoveryReconciliationFindingSinkFake
	roots    *recoveryReconciliationRootRegistryFake
}

func TestRecoveryReconciliationWritesOneAggregateAudit(t *testing.T) {
	fixture := newRecoveryReconciliationServiceTestFixture(t)
	request := ReconcileRecoveryRootRequest{
		NodeID: fixture.job.TargetNodeID,
		RootID: fixture.job.TargetRootID,
	}

	result, err := fixture.service.ReconcileRoot(context.Background(), request)
	if err != nil {
		t.Fatalf("reconcile clear root: %v", err)
	}
	if result.State != RecoveryReconciliationClear || !result.Complete ||
		result.NextCursor != "" || len(result.Findings) != 0 {
		t.Fatalf("reconciliation result=%+v, want complete clear", result)
	}

	fixture.audit.mu.Lock()
	auditInputs := append([]backupasset.AuditEventInput(nil), fixture.audit.inputs...)
	fixture.audit.mu.Unlock()
	if len(auditInputs) != 1 {
		t.Fatalf("aggregate audit writes=%d, want exactly one", len(auditInputs))
	}
	audit := auditInputs[0]
	wantFields := map[backupasset.AuditField]any{
		backupasset.AuditFieldOperation: "recovery_reconcile",
		backupasset.AuditFieldStatus:    string(RecoveryReconciliationClear),
	}
	if audit.Action != backupasset.AuditActionRecoveryCleanup ||
		audit.Outcome != backupasset.AuditOutcomeSuccess ||
		audit.ItemCount != int64(result.Counts.Scanned) ||
		audit.ItemCount < 0 || audit.ItemCount > recoveryReconciliationChainLimit ||
		audit.RepositoryID != "" || audit.RecoveryPointID != "" || audit.EntryID != "" ||
		audit.RecoveryJobID != "" || audit.ExportJobID != "" ||
		!reflect.DeepEqual(audit.Fields, wantFields) {
		t.Fatalf("aggregate audit=%+v, want one bounded sanitized clear audit", audit)
	}

	fixture.findings.mu.Lock()
	alerts := append([]RecoveryReconciliationAlert(nil), fixture.findings.alerts...)
	fixture.findings.mu.Unlock()
	if len(alerts) != 1 {
		t.Fatalf("finding sink calls=%d, want exactly one", len(alerts))
	}
	alert := alerts[0]
	if alert.NodeID != request.NodeID || alert.RootID != request.RootID ||
		alert.State != result.State || alert.Counts != result.Counts ||
		len(alert.Findings) != 0 {
		t.Fatalf("finding alert=%+v, want exact zero-finding clear product", alert)
	}
	formatted := fmt.Sprintf("%v|%+v|%#v", alert, alert, alert)
	if strings.Contains(formatted, request.RootID) || formatted !=
		"recovery.RecoveryReconciliationAlert{redacted}|recovery.RecoveryReconciliationAlert{redacted}|recovery.RecoveryReconciliationAlert{redacted}" {
		t.Fatalf("finding alert formatting exposed private fields: %q", formatted)
	}
}

func TestRecoveryReconciliationAuditAndFindingSinkFailurePrecedence(t *testing.T) {
	auditFailure := errors.New("FAKE_RECONCILIATION_AUDIT_FAILURE_FOR_TEST_ONLY")
	alertFailure := errors.New("FAKE_RECONCILIATION_ALERT_FAILURE_FOR_TEST_ONLY")
	tests := []struct {
		name     string
		blocked  bool
		auditErr error
		alertErr error
		wantErr  bool
	}{
		{name: "blocked product succeeds", blocked: true},
		{name: "clear audit failure", auditErr: auditFailure, wantErr: true},
		{name: "clear alert failure", alertErr: alertFailure, wantErr: true},
		{name: "blocked audit failure", blocked: true, auditErr: auditFailure, wantErr: true},
		{name: "blocked alert failure", blocked: true, alertErr: alertFailure, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryReconciliationServiceTestFixture(t)
			request := ReconcileRecoveryRootRequest{
				NodeID: fixture.job.TargetNodeID,
				RootID: fixture.job.TargetRootID,
			}
			wantState := RecoveryReconciliationClear
			wantCounts := RecoveryReconciliationCounts{Scanned: 3, KnownHealthy: 3}
			var wantFindings []RecoveryReconciliationFinding
			if test.blocked {
				wantState = RecoveryReconciliationBlocked
				wantCounts = RecoveryReconciliationCounts{Scanned: 1, KnownDrift: 1}
				wantFindings = []RecoveryReconciliationFinding{{
					Category: RecoveryReconciliationKnownDrift, Fingerprint: strings.Repeat("f", 43),
					EntryKind: TargetEntryDirectory, JobID: fixture.job.ID,
				}}
				fixture.target.page = &TargetReconciliationPage{
					Complete: true, Counts: wantCounts,
					Findings: append([]RecoveryReconciliationFinding(nil), wantFindings...),
				}
			}
			fixture.audit.err = test.auditErr
			fixture.findings.err = test.alertErr

			result, err := fixture.service.ReconcileRoot(context.Background(), request)
			if test.wantErr {
				if !errors.Is(err, ErrRecoveryReconciliationUnavailable) ||
					result.State == RecoveryReconciliationClear {
					t.Fatalf("failed reconciliation result=%+v err=%v, want stable unavailable without clear", result, err)
				}
			} else if err != nil || result.State != wantState || !result.Complete ||
				result.Counts != wantCounts || !reflect.DeepEqual(result.Findings, wantFindings) {
				t.Fatalf("successful reconciliation result=%+v err=%v", result, err)
			}

			fixture.audit.mu.Lock()
			auditInputs := append([]backupasset.AuditEventInput(nil), fixture.audit.inputs...)
			fixture.audit.mu.Unlock()
			fixture.findings.mu.Lock()
			alerts := append([]RecoveryReconciliationAlert(nil), fixture.findings.alerts...)
			fixture.findings.mu.Unlock()
			if len(auditInputs) != 1 || len(alerts) != 1 {
				t.Fatalf("audit writes=%d alert calls=%d, want one each", len(auditInputs), len(alerts))
			}

			wantOutcome := backupasset.AuditOutcomeSuccess
			if wantState == RecoveryReconciliationBlocked {
				wantOutcome = backupasset.AuditOutcomeBlocked
			}
			audit := auditInputs[0]
			if audit.Action != backupasset.AuditActionRecoveryCleanup || audit.Outcome != wantOutcome ||
				audit.ItemCount != int64(wantCounts.Scanned) ||
				audit.Fields[backupasset.AuditFieldOperation] != "recovery_reconcile" ||
				audit.Fields[backupasset.AuditFieldStatus] != string(wantState) {
				t.Fatalf("aggregate audit=%+v, want state %s", audit, wantState)
			}
			alert := alerts[0]
			if alert.NodeID != request.NodeID || alert.RootID != request.RootID ||
				alert.State != wantState || alert.Counts != wantCounts ||
				!reflect.DeepEqual(alert.Findings, wantFindings) {
				t.Fatalf("finding alert=%+v, want state %s", alert, wantState)
			}
		})
	}
}

func TestRecoveryReconcileDowngradeReadinessRequiresFreshAllRootsClear(t *testing.T) {
	t.Run("fresh complete cursor chains for every root without cache", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		firstRoot := fixture.roots.resolution
		secondRoot := settings.RecoveryTargetRootResolution{
			NodeID: firstRoot.NodeID, RootID: "root-r65-b",
			Locator: "/srv/FAKE_R65_SECOND_ROOT_FOR_TEST_ONLY",
		}
		var err error
		secondRoot.LocatorDigest, err = settings.RecoveryTargetRootLocatorDigest(
			secondRoot.NodeID, secondRoot.RootID, secondRoot.Locator,
		)
		if err != nil {
			t.Fatal(err)
		}
		fixture.roots.roots = []settings.RecoveryTargetRootReference{
			{NodeID: firstRoot.NodeID, RootID: firstRoot.RootID},
			{NodeID: secondRoot.NodeID, RootID: secondRoot.RootID},
		}
		fixture.roots.resolutions = map[string]settings.RecoveryTargetRootResolution{
			recoveryReconciliationRootFakeKey(firstRoot.NodeID, firstRoot.RootID):   firstRoot,
			recoveryReconciliationRootFakeKey(secondRoot.NodeID, secondRoot.RootID): secondRoot,
		}
		fixture.target.scan = func(
			permit TargetReconciliationPermit,
			_ TargetReconciliationRequest,
		) (TargetReconciliationPage, error) {
			if permit.RootID == firstRoot.RootID && permit.Cursor == "" {
				cursor := recoveryReconciliationEncodeCursor(
					permit, recoveryReconciliationPageLimit,
					recoveryReconciliationInitialPrefixDigest(permit),
				)
				if cursor == "" {
					t.Fatal("create authenticated R65 continuation cursor")
				}
				return TargetReconciliationPage{
					Complete: false, NextCursor: cursor,
					Counts: RecoveryReconciliationCounts{
						Scanned: recoveryReconciliationPageLimit, KnownHealthy: recoveryReconciliationPageLimit,
					},
				}, nil
			}
			if permit.RootID == firstRoot.RootID {
				return TargetReconciliationPage{
					Complete: true,
					Counts: RecoveryReconciliationCounts{
						Scanned:      recoveryReconciliationPageLimit + 1,
						KnownHealthy: recoveryReconciliationPageLimit + 1,
					},
				}, nil
			}
			return TargetReconciliationPage{Complete: true}, nil
		}

		generations := []string{
			"r65-admission-generation-a", "r65-admission-generation-a", "r65-admission-generation-b",
		}
		for call, generation := range generations {
			result, reconcileErr := fixture.service.ReconcileDowngradeReadiness(
				context.Background(), RecoveryDowngradeReconciliationRequest{AdmissionGeneration: generation},
			)
			if reconcileErr != nil || result.State != RecoveryReconciliationClear ||
				!result.Complete || result.NextCursor != "" || len(result.Findings) != 0 {
				t.Fatalf("fresh downgrade call %d result=%+v err=%v", call+1, result, reconcileErr)
			}
			wantPasses := (call + 1) * 3
			if len(fixture.target.permits) != wantPasses {
				t.Fatalf("fresh downgrade call %d target passes=%d, want %d", call+1, len(fixture.target.permits), wantPasses)
			}
			for _, permit := range fixture.target.permits[wantPasses-3:] {
				if permit.AdmissionGeneration != generation {
					t.Fatalf("permit generation=%q, want sticky %q", permit.AdmissionGeneration, generation)
				}
			}
		}
		if len(fixture.audit.inputs) != 9 || len(fixture.findings.alerts) != 9 {
			t.Fatalf("fresh pass audit=%d alerts=%d, want one per page", len(fixture.audit.inputs), len(fixture.findings.alerts))
		}
	})

	t.Run("pagination orchestration stops at sixteen pages", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		calls := 0
		fixture.target.scan = func(
			permit TargetReconciliationPermit,
			_ TargetReconciliationRequest,
		) (TargetReconciliationPage, error) {
			calls++
			ordinal := ((calls-1)%15 + 1) * recoveryReconciliationPageLimit
			prefix := recoveryReconciliationInitialPrefixDigest(permit)
			prefix[0] = byte(calls)
			cursor := recoveryReconciliationEncodeCursor(permit, ordinal, prefix)
			if cursor == "" {
				t.Fatal("create bounded R65 cursor")
			}
			return TargetReconciliationPage{
				Complete: false, NextCursor: cursor,
				Counts: RecoveryReconciliationCounts{Scanned: ordinal, KnownHealthy: ordinal},
			}, nil
		}

		result, err := fixture.service.ReconcileDowngradeReadiness(
			context.Background(), RecoveryDowngradeReconciliationRequest{
				AdmissionGeneration: "r65-bounded-admission-generation",
			},
		)
		if err != nil || result.State != RecoveryReconciliationBlocked || result.Complete ||
			result.Counts.ScanIncomplete != 1 || calls != 16 ||
			len(fixture.audit.inputs) != 16 || len(fixture.findings.alerts) != 16 {
			t.Fatalf("bounded pagination result=%+v err=%v calls=%d audit=%d alerts=%d",
				result, err, calls, len(fixture.audit.inputs), len(fixture.findings.alerts))
		}
	})

	t.Run("missing and duplicate roots fail closed before scanning", func(t *testing.T) {
		for _, test := range []struct {
			name  string
			roots []settings.RecoveryTargetRootReference
		}{
			{name: "missing", roots: []settings.RecoveryTargetRootReference{}},
			{name: "duplicate", roots: []settings.RecoveryTargetRootReference{
				{NodeID: 92, RootID: "root-r62"}, {NodeID: 92, RootID: "root-r62"},
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := newRecoveryReconciliationServiceTestFixture(t)
				fixture.roots.roots = test.roots
				result, err := fixture.service.ReconcileDowngradeReadiness(
					context.Background(), RecoveryDowngradeReconciliationRequest{
						AdmissionGeneration: "r65-admission-generation",
					},
				)
				if !errors.Is(err, ErrRecoveryReconciliationUnavailable) ||
					result.State == RecoveryReconciliationClear || len(fixture.target.permits) != 0 {
					t.Fatalf("%s roots result=%+v err=%v scans=%d", test.name, result, err, len(fixture.target.permits))
				}
			})
		}
	})

	t.Run("incomplete substantive invalid cursor and unavailable roots block", func(t *testing.T) {
		tests := []struct {
			name string
			page TargetReconciliationPage
			err  error
		}{
			{name: "incomplete without cursor", page: TargetReconciliationPage{
				Complete: false, Counts: RecoveryReconciliationCounts{Scanned: 1, KnownHealthy: 1},
			}},
			{name: "invalid cursor", page: TargetReconciliationPage{
				Complete: false, NextCursor: "invalid-cursor",
				Counts: RecoveryReconciliationCounts{Scanned: 1, KnownHealthy: 1},
			}},
			{name: "substantive finding", page: TargetReconciliationPage{
				Complete: false, NextCursor: "must-not-be-used",
				Counts: RecoveryReconciliationCounts{Scanned: 1, KnownDrift: 1},
				Findings: []RecoveryReconciliationFinding{{
					Category: RecoveryReconciliationKnownDrift, Fingerprint: strings.Repeat("e", 43),
					EntryKind: TargetEntryDirectory, JobID: strings.Repeat("2", 32),
				}},
			}},
			{name: "unavailable dependency", err: errors.New("FAKE_R65_TARGET_FAILURE_FOR_TEST_ONLY")},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				fixture := newRecoveryReconciliationServiceTestFixture(t)
				fixture.target.page = &test.page
				fixture.target.err = test.err
				result, err := fixture.service.ReconcileDowngradeReadiness(
					context.Background(), RecoveryDowngradeReconciliationRequest{
						AdmissionGeneration: "r65-admission-generation",
					},
				)
				if test.err != nil {
					if !errors.Is(err, ErrRecoveryReconciliationUnavailable) || result.State == RecoveryReconciliationClear {
						t.Fatalf("unavailable root result=%+v err=%v", result, err)
					}
				} else if err != nil || result.State != RecoveryReconciliationBlocked {
					t.Fatalf("blocked root result=%+v err=%v", result, err)
				}
				if len(fixture.target.permits) != 1 {
					t.Fatalf("blocked root scans=%d, want immediate stop", len(fixture.target.permits))
				}
			})
		}
	})
}

func TestRecoveryReconcileDowngradeReadinessPostgresRequiresFreshAllRootsClear(t *testing.T) {
	fixture := newRecoveryReconciliationServiceTestFixtureOnDB(
		t, newAuthorizationReceiptPostgresScopedDB(t),
	)
	generation := "r65-postgres-admission-generation"
	result, err := fixture.service.ReconcileDowngradeReadiness(
		context.Background(), RecoveryDowngradeReconciliationRequest{AdmissionGeneration: generation},
	)
	if err != nil || result.State != RecoveryReconciliationClear || !result.Complete ||
		result.NextCursor != "" || len(result.Findings) != 0 {
		t.Fatalf("PostgreSQL fresh downgrade result=%+v err=%v", result, err)
	}
	if len(fixture.target.permits) != 1 || fixture.target.permits[0].AdmissionGeneration != generation ||
		len(fixture.audit.inputs) != 1 || len(fixture.findings.alerts) != 1 {
		t.Fatalf("PostgreSQL fresh downgrade passes=%d audit=%d alerts=%d",
			len(fixture.target.permits), len(fixture.audit.inputs), len(fixture.findings.alerts))
	}
}

func TestRecoveryReconciliationDependencyErrorAndContextMatrix(t *testing.T) {
	rawFailure := errors.New("RAW_R66_RECONCILIATION_DEPENDENCY_FAILURE_FOR_TEST_ONLY")

	t.Run("caller context identity precedes invalid authority", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		result, err := fixture.service.ReconcileRoot(ctx, ReconcileRecoveryRootRequest{})
		if err != context.Canceled || !reflect.DeepEqual(result, RecoveryReconciliationResult{}) {
			t.Fatalf("canceled invalid root result=%+v error=%v, want exact context cancellation", result, err)
		}
		result, err = fixture.service.ReconcileDowngradeReadiness(
			ctx, RecoveryDowngradeReconciliationRequest{},
		)
		if err != context.Canceled || !reflect.DeepEqual(result, RecoveryReconciliationResult{}) {
			t.Fatalf("canceled invalid downgrade result=%+v error=%v, want exact context cancellation", result, err)
		}
		if fixture.roots.resolveCalls != 0 || len(fixture.target.permits) != 0 {
			t.Fatalf("canceled invalid request crossed dependencies: resolves=%d scans=%d",
				fixture.roots.resolveCalls, len(fixture.target.permits))
		}
	})

	t.Run("invalid authority precedes dependencies", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{})
		if !errors.Is(err, ErrInvalidRecoveryReconciliation) ||
			!reflect.DeepEqual(result, RecoveryReconciliationResult{}) ||
			fixture.roots.resolveCalls != 0 || len(fixture.target.permits) != 0 {
			t.Fatalf("invalid authority result=%+v error=%v resolves=%d scans=%d",
				result, err, fixture.roots.resolveCalls, len(fixture.target.permits))
		}
	})

	t.Run("root registry list failure is sanitized unavailable", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		fixture.roots.listErr = rawFailure
		result, err := fixture.service.ReconcileDowngradeReadiness(
			context.Background(), RecoveryDowngradeReconciliationRequest{
				AdmissionGeneration: "r66-registry-list-generation",
			},
		)
		assertRecoveryReconciliationR66Failure(t, result, err, rawFailure, true)
		if len(fixture.target.permits) != 0 || len(fixture.audit.inputs) != 0 || len(fixture.findings.alerts) != 0 {
			t.Fatalf("registry list failure crossed later dependencies")
		}
	})

	t.Run("root resolution failure is a sanitized incomplete blocker", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		fixture.roots.resolveErr = rawFailure
		result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
			NodeID: fixture.job.TargetNodeID, RootID: fixture.job.TargetRootID,
		})
		assertRecoveryReconciliationR66Failure(t, result, err, rawFailure, false)
		if result.State != RecoveryReconciliationBlocked || result.Complete ||
			result.Counts != (RecoveryReconciliationCounts{ScanIncomplete: 1}) ||
			len(result.Findings) != 1 || len(fixture.target.permits) != 0 ||
			len(fixture.audit.inputs) != 1 || len(fixture.findings.alerts) != 1 {
			t.Fatalf("root resolution blocker=%+v scans=%d audit=%d alerts=%d",
				result, len(fixture.target.permits), len(fixture.audit.inputs), len(fixture.findings.alerts))
		}
	})

	t.Run("database transaction failure is sanitized unavailable", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		sqlDB, dbErr := fixture.db.DB()
		if dbErr != nil {
			t.Fatal(dbErr)
		}
		if closeErr := sqlDB.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
			NodeID: fixture.job.TargetNodeID, RootID: fixture.job.TargetRootID,
		})
		assertRecoveryReconciliationR66Failure(t, result, err, rawFailure, true)
		if len(fixture.target.permits) != 0 || len(fixture.audit.inputs) != 0 || len(fixture.findings.alerts) != 0 {
			t.Fatalf("database failure crossed later dependencies")
		}
	})

	t.Run("audit key failure is sanitized unavailable", func(t *testing.T) {
		fixture := newRecoveryReconciliationServiceTestFixture(t)
		keys := fixture.service.keys.(*recoveryReconciliationKeySourceFake)
		keys.activeErr = rawFailure
		result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
			NodeID: fixture.job.TargetNodeID, RootID: fixture.job.TargetRootID,
		})
		assertRecoveryReconciliationR66Failure(t, result, err, rawFailure, true)
		if len(fixture.target.permits) != 0 || len(fixture.audit.inputs) != 0 || len(fixture.findings.alerts) != 0 {
			t.Fatalf("audit-key failure crossed later dependencies")
		}
	})

	for _, sink := range []string{"audit", "alert"} {
		t.Run(sink+" sink failure is sanitized unavailable", func(t *testing.T) {
			fixture := newRecoveryReconciliationServiceTestFixture(t)
			if sink == "audit" {
				fixture.audit.err = rawFailure
			} else {
				fixture.findings.err = rawFailure
			}
			result, err := fixture.service.ReconcileRoot(context.Background(), ReconcileRecoveryRootRequest{
				NodeID: fixture.job.TargetNodeID, RootID: fixture.job.TargetRootID,
			})
			assertRecoveryReconciliationR66Failure(t, result, err, rawFailure, true)
			if len(fixture.target.permits) != 1 || len(fixture.audit.inputs) != 1 || len(fixture.findings.alerts) != 1 {
				t.Fatalf("%s failure calls: scans=%d audit=%d alerts=%d", sink,
					len(fixture.target.permits), len(fixture.audit.inputs), len(fixture.findings.alerts))
			}
		})
	}
}

func TestRecoveryReconciliationPrivacyCanaryMatrix(t *testing.T) {
	fixture := newRecoveryReconciliationServiceTestFixture(t)
	fixture.target.page = &TargetReconciliationPage{
		Complete: true,
		Counts:   RecoveryReconciliationCounts{Scanned: 1, KnownDrift: 1},
		Findings: []RecoveryReconciliationFinding{{
			Category:    RecoveryReconciliationKnownDrift,
			Fingerprint: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x66}, sha256.Size)),
			EntryKind:   TargetEntryDirectory,
			JobID:       fixture.job.ID,
		}},
	}
	request := ReconcileRecoveryRootRequest{NodeID: fixture.job.TargetNodeID, RootID: fixture.job.TargetRootID}
	result, err := fixture.service.ReconcileRoot(context.Background(), request)
	if err != nil || result.State != RecoveryReconciliationBlocked || !result.Complete {
		t.Fatalf("build R66 privacy product result=%+v error=%v", result, err)
	}
	if len(fixture.target.permits) != 1 || len(fixture.target.requests) != 1 ||
		len(fixture.audit.inputs) != 1 || len(fixture.findings.alerts) != 1 {
		t.Fatalf("R66 privacy captures are incomplete")
	}
	permit := fixture.target.permits[0]
	cursor := recoveryReconciliationEncodeCursor(
		permit, recoveryReconciliationPageLimit, recoveryReconciliationInitialPrefixDigest(permit),
	)
	if cursor == "" {
		t.Fatal("build R66 privacy cursor")
	}
	page := *fixture.target.page
	page.Complete = false
	page.NextCursor = cursor
	metricsLabels := map[string]string{
		"state": string(result.State), "category": string(result.Findings[0].Category),
		"entry_kind": string(result.Findings[0].EntryKind),
	}

	values := []any{
		request, permit, permit.proof, fixture.target.requests[0], page, result,
		fixture.findings.alerts[0], fixture.audit.inputs[0], metricsLabels, cursor,
	}
	var corpus strings.Builder
	for _, value := range values {
		_, _ = fmt.Fprintf(&corpus, "|%v|%+v|%#v", value, value, value)
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("marshal R66 privacy product %T: %v", value, marshalErr)
		}
		corpus.Write(encoded)
	}
	rawFailure := errors.New("RAW_R66_PRIVATE_DEPENDENCY_ERROR_FOR_TEST_ONLY")
	fixture.target.err = rawFailure
	failed, failureErr := fixture.service.ReconcileRoot(context.Background(), request)
	if !errors.Is(failureErr, ErrRecoveryReconciliationUnavailable) ||
		!reflect.DeepEqual(failed, RecoveryReconciliationResult{}) {
		t.Fatalf("R66 privacy failure result=%+v error=%v", failed, failureErr)
	}
	_, _ = fmt.Fprintf(&corpus, "|%v|%+v|%#v", failureErr, failureErr, failureErr)

	proof := permit.proof
	if proof == nil || len(proof.expected) == 0 {
		t.Fatal("R66 privacy permit proof is missing")
	}
	canaries := []string{
		fixture.roots.resolution.Locator,
		proof.sessionBinding.nodeRevision,
		proof.sessionBinding.credentialRevision,
		string(proof.auditTokenKey[:]),
		proof.expected[0].componentToken,
		proof.expected[0].markerBindingDigest,
		proof.expected[0].markerCreatorID,
		proof.bindingDigest,
		rawFailure.Error(),
	}
	for _, canary := range canaries {
		if canary != "" && strings.Contains(corpus.String(), canary) {
			t.Fatalf("R66 reconciliation boundary leaked private canary %q", canary)
		}
	}
}

func assertRecoveryReconciliationR66Failure(
	t *testing.T,
	result RecoveryReconciliationResult,
	err error,
	rawFailure error,
	wantUnavailable bool,
) {
	t.Helper()
	if wantUnavailable {
		if !errors.Is(err, ErrRecoveryReconciliationUnavailable) ||
			!reflect.DeepEqual(result, RecoveryReconciliationResult{}) {
			t.Fatalf("R66 dependency result=%+v error=%v, want stable unavailable", result, err)
		}
	} else if err != nil || result.State == RecoveryReconciliationClear {
		t.Fatalf("R66 dependency result=%+v error=%v, want blocked product", result, err)
	}
	corpus := fmt.Sprintf("%v|%+v|%#v|%v|%+v|%#v", result, result, result, err, err, err)
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("marshal R66 dependency result: %v", marshalErr)
	}
	if strings.Contains(corpus+string(encoded), rawFailure.Error()) {
		t.Fatalf("R66 dependency failure leaked raw error")
	}
}

func newRecoveryReconciliationServiceTestFixture(t *testing.T) *recoveryReconciliationServiceTestFixture {
	t.Helper()
	dsn := fmt.Sprintf(
		"file:%s-%d?mode=memory&cache=shared&_loc=UTC&_foreign_keys=1",
		strings.ReplaceAll(t.Name(), "/", "_"), recoverySourceValidatorDBSequence.Add(1),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return newRecoveryReconciliationServiceTestFixtureOnDB(t, db)
}

func newRecoveryReconciliationServiceTestFixtureOnDB(
	t *testing.T,
	db *gorm.DB,
) *recoveryReconciliationServiceTestFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RECOVERY_RECONCILIATION_INCOMPLETE_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	if err := db.AutoMigrate(
		&model.BackupAssetRecoveryPlan{}, &model.BackupAssetRecoveryJob{},
		&model.BackupAssetRecoveryResultSet{}, &model.BackupAssetRecoveryResult{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 8, 10, 11, 12, 0, time.UTC)
	rootLocator := "/srv/FAKE_R62_INCOMPLETE_ROOT_FOR_TEST_ONLY"
	rootDigest, err := settings.RecoveryTargetRootLocatorDigest(92, "root-r62", rootLocator)
	if err != nil {
		t.Fatal(err)
	}
	plan := model.BackupAssetRecoveryPlan{
		ID: strings.Repeat("1", 32), TargetMode: string(TargetModeIsolated), TargetNodeID: 92,
		TargetRootID: "root-r62", EncryptedTargetRootLocator: rootLocator, RootLocatorDigest: rootDigest,
		TargetBaseRevision: "node-revision-r62", CredentialScopeRevision: "credential-revision-r62",
		RootRevision: "root-revision-r62", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	jobID := strings.Repeat("2", 32)
	plaintextDeadline := now.Add(24 * time.Hour)
	job := model.BackupAssetRecoveryJob{
		ID: jobID, PlanID: plan.ID, State: string(JobStateRunning), TargetMode: string(TargetModeIsolated),
		TargetNodeID: plan.TargetNodeID, TargetRootID: plan.TargetRootID, RootLocatorDigest: rootDigest,
		PathDigest:            framedDigest("xirang/recovery/r62-incomplete-path/v1", jobID),
		PreflightNodeRevision: plan.TargetBaseRevision, PreflightTargetRevision: plan.TargetBaseRevision,
		WorkspacePhase:                    string(WorkspacePhaseReserved),
		EncryptedWorkspaceRelativeLocator: recoveryWorkspaceLocatorDirectory + "/" + jobID,
		WorkspaceBindingDigest:            framedDigest("xirang/recovery/r62-incomplete-workspace/v1", jobID),
		WorkspaceMarkerBindingDigest:      framedDigest("xirang/recovery/r62-incomplete-marker/v1", jobID),
		WorkspaceOwner:                    "r62-incomplete-worker", WorkspaceFence: 1,
		PlaintextDeadline: &plaintextDeadline, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	target := &recoveryReconciliationTargetCapture{}
	audit := &authorizationReceiptAuditSpy{}
	findings := &recoveryReconciliationFindingSinkFake{}
	roots := &recoveryReconciliationRootRegistryFake{resolution: settings.RecoveryTargetRootResolution{
		NodeID: plan.TargetNodeID, RootID: plan.TargetRootID, Locator: rootLocator, LocatorDigest: rootDigest,
	}}
	service, err := NewRecoveryReconciliationService(RecoveryReconciliationServiceDependencies{
		DB: db, Now: func() time.Time { return now },
		Roots: roots,
		Revisions: recoveryReconciliationRevisionSourceFake{snapshot: RecoveryReconciliationRevisionSnapshot{
			NodeRevision: plan.TargetBaseRevision, CredentialRevision: plan.CredentialScopeRevision,
			RootRevision: plan.RootRevision,
		}},
		Keys: &recoveryReconciliationKeySourceFake{material: backupasset.DomainKeyMaterial{
			ID: strings.Repeat("3", 32), Domain: backupasset.KeyDomainAuditFingerprint, Version: 3,
			State: backupasset.DomainKeyActive, Key: []byte("abcdef0123456789abcdef0123456789"), ActivatedAt: now.Add(-time.Hour),
		}},
		Target: target, Audit: audit, Findings: findings,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &recoveryReconciliationServiceTestFixture{
		db: db, now: now, plan: plan, job: job, service: service, target: target,
		audit: audit, findings: findings, roots: roots,
	}
}

type recoveryReconciliationRootRegistryFake struct {
	mu                    sync.Mutex
	resolution            settings.RecoveryTargetRootResolution
	resolutions           map[string]settings.RecoveryTargetRootResolution
	roots                 []settings.RecoveryTargetRootReference
	listErr               error
	resolveErr            error
	resolveCalls          int
	resolvedInTransaction bool
}

func recoveryReconciliationRootFakeKey(nodeID uint, rootID string) string {
	return strconv.FormatUint(uint64(nodeID), 10) + "/" + rootID
}

type recoveryReconciliationRevisionSourceFake struct {
	snapshot RecoveryReconciliationRevisionSnapshot
}

func (fake recoveryReconciliationRevisionSourceFake) ResolveRecoveryReconciliationRevisionsTx(
	_ context.Context,
	tx *gorm.DB,
	nodeID uint,
	rootID string,
) (RecoveryReconciliationRevisionSnapshot, error) {
	if tx == nil || nodeID == 0 || rootID == "" {
		return RecoveryReconciliationRevisionSnapshot{}, errors.New("invalid reconciliation revision snapshot request")
	}
	return fake.snapshot, nil
}

func (fake *recoveryReconciliationRootRegistryFake) ListAllRecoveryTargetRoots(
	context.Context,
) ([]settings.RecoveryTargetRootReference, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.listErr != nil {
		return nil, fake.listErr
	}
	if fake.roots != nil {
		return append([]settings.RecoveryTargetRootReference(nil), fake.roots...), nil
	}
	return []settings.RecoveryTargetRootReference{{NodeID: fake.resolution.NodeID, RootID: fake.resolution.RootID}}, nil
}

func (fake *recoveryReconciliationRootRegistryFake) ResolveRecoveryTargetRootTx(
	_ context.Context,
	tx *gorm.DB,
	nodeID uint,
	rootID string,
) (settings.RecoveryTargetRootResolution, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.resolveCalls++
	fake.resolvedInTransaction = fake.resolvedInTransaction || (tx != nil && tx.Statement != nil && tx.Statement.ConnPool != nil)
	if fake.resolveErr != nil {
		return settings.RecoveryTargetRootResolution{}, fake.resolveErr
	}
	resolution := fake.resolution
	if fake.resolutions != nil {
		var ok bool
		resolution, ok = fake.resolutions[recoveryReconciliationRootFakeKey(nodeID, rootID)]
		if !ok {
			return settings.RecoveryTargetRootResolution{}, settings.ErrRecoveryTargetRootNotFound
		}
	}
	if nodeID != resolution.NodeID || rootID != resolution.RootID {
		return settings.RecoveryTargetRootResolution{}, settings.ErrRecoveryTargetRootNotFound
	}
	return resolution, nil
}

type recoveryReconciliationKeySourceFake struct {
	mu             sync.Mutex
	material       backupasset.DomainKeyMaterial
	versions       map[int]backupasset.DomainKeyMaterial
	activeCalls    int
	byVersionCalls []int
	issuedKeys     [][]byte
	activeErr      error
	byVersionErr   error
}

func (fake *recoveryReconciliationKeySourceFake) Active(
	_ context.Context,
	domain backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.activeCalls++
	if fake.activeErr != nil {
		return backupasset.DomainKeyMaterial{}, fake.activeErr
	}
	if domain != backupasset.KeyDomainAuditFingerprint {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	material := cloneDomainKeyMaterial(fake.material)
	fake.issuedKeys = append(fake.issuedKeys, material.Key)
	return material, nil
}

func (fake *recoveryReconciliationKeySourceFake) ByVersion(
	_ context.Context,
	domain backupasset.KeyDomain,
	version int,
) (backupasset.DomainKeyMaterial, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.byVersionCalls = append(fake.byVersionCalls, version)
	if fake.byVersionErr != nil {
		return backupasset.DomainKeyMaterial{}, fake.byVersionErr
	}
	material := fake.material
	if fake.versions != nil {
		var ok bool
		material, ok = fake.versions[version]
		if !ok {
			return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
		}
	}
	if domain != material.Domain || version != material.Version {
		return backupasset.DomainKeyMaterial{}, backupasset.ErrKeyUnavailable
	}
	cloned := cloneDomainKeyMaterial(material)
	fake.issuedKeys = append(fake.issuedKeys, cloned.Key)
	return cloned, nil
}

type recoveryReconciliationTargetCapture struct {
	mu       sync.Mutex
	permits  []TargetReconciliationPermit
	requests []TargetReconciliationRequest
	page     *TargetReconciliationPage
	err      error
	scan     func(TargetReconciliationPermit, TargetReconciliationRequest) (TargetReconciliationPage, error)
}

type recoveryReconciliationPermitValidationTarget struct {
	now             time.Time
	dependencyCalls int
}

func (target *recoveryReconciliationPermitValidationTarget) ScanRecoveryRoot(
	_ context.Context,
	permit TargetReconciliationPermit,
	request TargetReconciliationRequest,
) (TargetReconciliationPage, error) {
	if err := permit.ValidateRequestAt(target.now, request); err != nil {
		return TargetReconciliationPage{}, err
	}
	target.dependencyCalls++
	return TargetReconciliationPage{Complete: true, Findings: []RecoveryReconciliationFinding{}}, nil
}

func (fake *recoveryReconciliationTargetCapture) ScanRecoveryRoot(
	_ context.Context,
	permit TargetReconciliationPermit,
	request TargetReconciliationRequest,
) (TargetReconciliationPage, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	proof := permit.proof
	if proof != nil {
		cloned := *proof
		cloned.expected = append([]targetReconciliationExpected(nil), proof.expected...)
		permit.proof = &cloned
	}
	fake.permits = append(fake.permits, permit)
	fake.requests = append(fake.requests, request)
	if fake.err != nil {
		return TargetReconciliationPage{}, fake.err
	}
	if fake.scan != nil {
		return fake.scan(permit, request)
	}
	if fake.page != nil {
		page := *fake.page
		page.Findings = append([]RecoveryReconciliationFinding(nil), fake.page.Findings...)
		return page, nil
	}
	count := 0
	if proof != nil {
		count = len(proof.expected)
	}
	return TargetReconciliationPage{
		Complete: true,
		Counts:   RecoveryReconciliationCounts{Scanned: count, KnownHealthy: count},
		Findings: []RecoveryReconciliationFinding{},
	}, nil
}

type recoveryReconciliationFindingSinkFake struct {
	mu     sync.Mutex
	alerts []RecoveryReconciliationAlert
	err    error
}

func (fake *recoveryReconciliationFindingSinkFake) NotifyRecoveryReconciliation(
	_ context.Context,
	alert RecoveryReconciliationAlert,
) error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.alerts = append(fake.alerts, alert)
	return fake.err
}
