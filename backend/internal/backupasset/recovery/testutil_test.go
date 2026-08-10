package recovery

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"

	"gorm.io/gorm"
)

type authorizationReceiptServiceFixture struct {
	db           *gorm.DB
	service      *AuthorizationService
	request      RecoveryAuthorizationRequest
	now          time.Time
	audit        *authorizationReceiptAuditSpy
	dependencies AuthorizationServiceDependencies
}

type authorizationReceiptAuditSpy struct {
	mu     sync.Mutex
	inputs []backupasset.AuditEventInput
	err    error
}

func (spy *authorizationReceiptAuditSpy) Write(
	_ context.Context,
	input backupasset.AuditEventInput,
) (model.BackupAssetAuditEvent, error) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	spy.inputs = append(spy.inputs, input)
	return model.BackupAssetAuditEvent{}, spy.err
}

type authorizationReceiptNodeAdmission struct{}

func (authorizationReceiptNodeAdmission) AdmitRecoveryTx(context.Context, *gorm.DB, uint) error {
	return nil
}

type authorizationReceiptLiveRevalidator struct{}

func (authorizationReceiptLiveRevalidator) RevalidateRecoveryAuthorityTx(
	context.Context,
	*gorm.DB,
	RecoveryAuthorityBinding,
) error {
	return nil
}

type authorizationReceiptLocatorKeys struct {
	material backupasset.DomainKeyMaterial
}

func newAuthorizationReceiptLocatorKeys() *authorizationReceiptLocatorKeys {
	return &authorizationReceiptLocatorKeys{material: backupasset.DomainKeyMaterial{
		ID: strings.Repeat("f", 32), Domain: backupasset.KeyDomainRecoveryCleanupOwnership,
		Version: 1, State: backupasset.DomainKeyActive, Key: []byte(strings.Repeat("k", 32)),
	}}
}

func (keys *authorizationReceiptLocatorKeys) Active(
	_ context.Context,
	domain backupasset.KeyDomain,
) (backupasset.DomainKeyMaterial, error) {
	if keys == nil || domain != backupasset.KeyDomainRecoveryCleanupOwnership {
		return backupasset.DomainKeyMaterial{}, fmt.Errorf("unexpected recovery locator key domain")
	}
	return cloneDomainKeyMaterial(keys.material), nil
}

func (keys *authorizationReceiptLocatorKeys) LockActiveTx(
	_ context.Context,
	tx *gorm.DB,
	expected backupasset.DomainKeyMaterial,
) (backupasset.DomainKeyMaterial, error) {
	if keys == nil || tx == nil || !sameRecoveryLocatorKey(keys.material, expected) {
		return backupasset.DomainKeyMaterial{}, fmt.Errorf("recovery locator key changed")
	}
	return cloneDomainKeyMaterial(keys.material), nil
}

func mustAuthorizationReceiptOperationSnapshot(
	t *testing.T,
	assetRefs []backupasset.AssetRef,
	nonDeleteKinds []RecoveryOperationKind,
	includeDelete bool,
	targetRootID string,
	rootLocatorDigest string,
) (RecoveryOperationProducts, string) {
	t.Helper()
	return mustAuthorizationReceiptOperationSnapshotConfigured(
		t, assetRefs, nonDeleteKinds, includeDelete, targetRootID, rootLocatorDigest, false,
	)
}

func mustAuthorizationReceiptExecutionOperationSnapshot(
	t *testing.T,
	assetRefs []backupasset.AssetRef,
	nonDeleteKinds []RecoveryOperationKind,
	includeDelete bool,
	targetRootID string,
	rootLocatorDigest string,
) (RecoveryOperationProducts, string) {
	t.Helper()
	return mustAuthorizationReceiptOperationSnapshotConfigured(
		t, assetRefs, nonDeleteKinds, includeDelete, targetRootID, rootLocatorDigest, true,
	)
}

func mustAuthorizationReceiptOperationSnapshotConfigured(
	t *testing.T,
	assetRefs []backupasset.AssetRef,
	nonDeleteKinds []RecoveryOperationKind,
	includeDelete bool,
	targetRootID string,
	rootLocatorDigest string,
	executionPayloads bool,
) (RecoveryOperationProducts, string) {
	t.Helper()
	deleteCount := 0
	if includeDelete {
		deleteCount = 1
	}
	return mustAuthorizationReceiptOperationSnapshotConfiguredWithDeleteCount(
		t, assetRefs, nonDeleteKinds, deleteCount, targetRootID, rootLocatorDigest, executionPayloads,
	)
}

func mustAuthorizationReceiptOperationSnapshotConfiguredWithDeleteCount(
	t *testing.T,
	assetRefs []backupasset.AssetRef,
	nonDeleteKinds []RecoveryOperationKind,
	deleteCount int,
	targetRootID string,
	rootLocatorDigest string,
	executionPayloads bool,
) (RecoveryOperationProducts, string) {
	t.Helper()
	if len(nonDeleteKinds) == 0 {
		nonDeleteKinds = make([]RecoveryOperationKind, len(assetRefs))
		for index := range nonDeleteKinds {
			nonDeleteKinds[index] = RecoveryOperationCreate
		}
	}
	if len(assetRefs) == 0 || len(nonDeleteKinds) != len(assetRefs) || deleteCount < 0 {
		t.Fatal("authorization operation fixture requires one kind per selected asset")
	}

	targetMode := TargetModeIsolated
	conflictPolicy := ConflictFailOnConflict
	if deleteCount > 0 {
		targetMode = TargetModeInPlace
		conflictPolicy = ConflictExactMirror
	}
	operations := make([]RecoveryOperation, 0, len(assetRefs)+deleteCount)
	var estimatedBytes int64
	for index, assetRef := range assetRefs {
		kind := nonDeleteKinds[index]
		expectedPrior := ExpectedTargetIdentity{Kind: ExpectedTargetAbsent}
		expectedPriorBytes := int64(-1)
		expectedPostIdentityDigest := framedDigest(
			"xirang/recovery/test-expected-post/v1",
			assetRef.RecoveryPointID,
			assetRef.EntryID,
		)
		expectedPostBytes := int64(index + 1)
		if executionPayloads {
			expectedPostIdentityDigest = recoveryExecutionPayloadDigest(expectedPostBytes)
		}
		if kind != RecoveryOperationCreate {
			expectedPrior = ExpectedTargetIdentity{
				Kind: ExpectedTargetPresent,
				Digest: framedDigest(
					"xirang/recovery/test-expected-prior/v1",
					assetRef.RecoveryPointID,
					assetRef.EntryID,
				),
			}
			expectedPriorBytes = expectedPostBytes
		}
		if kind == RecoveryOperationSkip {
			expectedPostIdentityDigest = expectedPrior.Digest
			expectedPostBytes = -1
		}
		estimated := int64(index + 1)
		estimatedBytes += estimated
		assetRefCopy := assetRef
		targetRelativeLocator := fmt.Sprintf("items/item-%04d", index)
		semanticTargetDigest, err := SemanticTargetDigest(
			targetMode, targetRootID, rootLocatorDigest, targetRelativeLocator,
		)
		if err != nil {
			t.Fatalf("build authorization semantic target digest: %v", err)
		}
		operations = append(operations, RecoveryOperation{
			Kind: kind, TargetRelativeLocator: targetRelativeLocator,
			SemanticTargetDigest:       semanticTargetDigest,
			TargetPathDigest:           strings.Repeat(strconv.Itoa(index+1), sha256DigestLength),
			ExpectedPrior:              expectedPrior,
			ExpectedPostIdentityDigest: expectedPostIdentityDigest,
			ExpectedPostBytes:          expectedPostBytes,
			ExpectedPriorBytes:         expectedPriorBytes,
			Source: RecoveryOperationSource{
				Kind:     RecoveryOperationSourceAssetRef,
				AssetRef: &assetRefCopy,
			},
			DisplayClass:   RecoveryDisplayClassRegular,
			EstimatedBytes: estimated,
		})
	}

	for deleteIndex := 0; deleteIndex < deleteCount; deleteIndex++ {
		deleteTargetRelativeLocator := "items/stale-directory"
		deleteTargetPathDigest := strings.Repeat("4", sha256DigestLength)
		deletePriorDigest := framedDigest("xirang/recovery/test-delete-prior/v1", assetRefs[0].RecoveryPointID)
		if deleteIndex > 0 {
			ordinal := strconv.Itoa(deleteIndex)
			deleteTargetRelativeLocator = fmt.Sprintf("items/stale-directory-%04d", deleteIndex)
			deleteTargetPathDigest = framedDigest("xirang/recovery/test-delete-path/v1", ordinal)
			deletePriorDigest = framedDigest(
				"xirang/recovery/test-delete-prior/v1", assetRefs[0].RecoveryPointID, ordinal,
			)
		}
		semanticTargetDigest, err := SemanticTargetDigest(
			targetMode, targetRootID, rootLocatorDigest, deleteTargetRelativeLocator,
		)
		if err != nil {
			t.Fatalf("build authorization delete semantic target digest: %v", err)
		}
		operations = append(operations, RecoveryOperation{
			Kind: RecoveryOperationDelete, TargetRelativeLocator: deleteTargetRelativeLocator,
			SemanticTargetDigest: semanticTargetDigest,
			TargetPathDigest:     deleteTargetPathDigest,
			ExpectedPrior: ExpectedTargetIdentity{
				Kind:   ExpectedTargetPresent,
				Digest: deletePriorDigest,
			},
			ExpectedPostIdentityDigest: "",
			ExpectedPostBytes:          -1,
			ExpectedPriorBytes:         -1,
			Source:                     RecoveryOperationSource{Kind: RecoveryOperationSourceNone},
			DisplayClass:               RecoveryDisplayClassDirectory,
			EstimatedBytes:             0,
		})
	}

	products, err := NewOperationProducts(RecoveryOperationProductsInput{
		TargetMode:     targetMode,
		ConflictPolicy: conflictPolicy,
		Operations:     operations,
		Limits: RecoveryOperationLimits{
			MaxRows: len(operations), MaxItems: len(operations),
			MaxBytes: estimatedBytes, MaxImpactRows: len(operations),
		},
	})
	if err != nil {
		t.Fatalf("build authorization operation products: %v", err)
	}
	encoded, err := encodeRecoveryOperationRows(products.Rows)
	if err != nil {
		t.Fatalf("encode authorization operation snapshot: %v", err)
	}
	return products, encoded
}

func newAuthorizationReceiptServiceFixture(
	t *testing.T,
	operation AuthorizationReceiptOperation,
) *authorizationReceiptServiceFixture {
	return newAuthorizationReceiptServiceFixtureConfigured(
		t, operation, operation == AuthorizationReceiptDeleteAuthorize, true,
	)
}

func newExactMirrorExecutionServiceFixture(t *testing.T) *authorizationReceiptServiceFixture {
	t.Helper()
	return newAuthorizationReceiptServiceFixtureConfigured(
		t, AuthorizationReceiptWriteAuthorize, true, false,
	)
}

func newAuthorizationReceiptServiceFixtureConfigured(
	t *testing.T,
	operation AuthorizationReceiptOperation,
	exactMirror bool,
	prepareOperation bool,
) *authorizationReceiptServiceFixture {
	t.Helper()
	base := newPlanServiceTestFixture(t, exactMirror)
	ensureRecoveryPlanRollbackTables(t, base.db)
	if err := base.db.AutoMigrate(&model.Node{}); err != nil {
		t.Fatal(err)
	}
	if err := base.db.Create(&model.Node{
		ID: 27, Name: "authorization-receipt-target", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthType: "key", BackupDir: "/tmp/authorization-receipt-target",
	}).Error; err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_point_leases_active_owner_slot
			ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_asset_recovery_node_leases_active_node
			ON backup_asset_recovery_node_leases(node_id) WHERE state = 'active'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_asset_recovery_evidence_authorization_idempotency
			ON backup_asset_recovery_evidence(requester_id, endpoint, idempotency_key_digest)
			WHERE kind = 'authorization_receipt'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_asset_recovery_evidence_authorization_proof
			ON backup_asset_recovery_evidence(step_up_jti_digest)
			WHERE kind = 'authorization_receipt' AND step_up_jti_digest <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_backup_asset_recovery_evidence_authorization_reaper
			ON backup_asset_recovery_evidence(kind, replay_expires_at, id)`,
	} {
		if err := base.db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}

	if operation == AuthorizationReceiptSecurityOverride {
		base.request.Plan.Binding.SecurityDecision = SecurityDecision{
			Kind: SecurityDecisionBlock, DecisionDigest: strings.Repeat("a", 64),
			FindingSetDigest: strings.Repeat("b", 64), PolicyRevision: "security-policy-revision-blocked",
		}
	}
	thirdRef := backupasset.AssetRef{
		RecoveryPointID: base.source.recoveryPointID,
		EntryID:         strings.Repeat("7", 64),
	}
	if err := base.db.Create(&model.CatalogEntry{
		GenerationID: base.source.catalogGenerationID, EntryID: thirdRef.EntryID,
		RecoveryPointID: base.source.recoveryPointID, ParentEntryID: &base.source.directoryRef.EntryID,
		NormalizedPath: "/three", Name: "three", EntryType: string(backupasset.CatalogEntryFile), Size: 3,
	}).Error; err != nil {
		t.Fatal(err)
	}
	selectionRefs := append([]backupasset.AssetRef(nil), base.request.Selection.AssetRefs...)
	selectionRefs = append(selectionRefs, thirdRef)
	replacePlanSelection(t, &base.request, exactSelectionWithRefs(t, base.request.Selection, selectionRefs))
	operationSnapshot := mustAuthorizationReceiptOperationSnapshot
	if exactMirror && !prepareOperation {
		operationSnapshot = mustAuthorizationReceiptExecutionOperationSnapshot
	}
	operationProducts, encodedOperationRows := operationSnapshot(
		t,
		base.request.Selection.AssetRefs,
		[]RecoveryOperationKind{RecoveryOperationCreate, RecoveryOperationOverwrite, RecoveryOperationSkip},
		exactMirror,
		base.request.Plan.Binding.Target.RootID,
		base.request.Plan.Binding.Target.RootLocatorDigest,
	)
	base.request.Plan.Binding.OperationSetDigest = operationProducts.OperationSetDigest
	base.request.Plan.Binding.DeleteSetDigest = operationProducts.DeleteSetDigest
	base.request.EstimatedItems = operationProducts.Impact.EstimatedItems
	base.request.EstimatedBytes = operationProducts.Impact.EstimatedBytes
	created, err := base.service.CreatePlan(context.Background(), base.request)
	if err != nil {
		t.Fatalf("create authorization-receipt fixture plan: %v", err)
	}
	var plan model.BackupAssetRecoveryPlan
	if err := base.db.Where("id = ?", created.PlanID).Take(&plan).Error; err != nil {
		t.Fatal(err)
	}
	if err := base.db.Model(&plan).Updates(map[string]any{
		"state":               string(PlanStatePreflightReady),
		"transition_revision": uint64(1),
		"updated_at":          base.now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	plan.State = string(PlanStatePreflightReady)
	plan.TransitionRevision = 1

	candidateDigest, candidateCategories := authorizationReceiptOverrideCandidate(plan, operation)
	preflight := model.BackupAssetRecoveryPreflight{
		ID: strings.Repeat("7", 32), PlanID: plan.ID, Revision: plan.PreflightRevision,
		SourceRevisionDigest: plan.SourceRevisionDigest, TargetNodeID: plan.TargetNodeID,
		NodeRevision: plan.TargetBaseRevision, TargetRootID: plan.TargetRootID,
		RootLocatorDigest: plan.RootLocatorDigest, PathDigest: plan.PathDigest,
		TargetRevision: plan.TargetBaseRevision, CapabilityRevision: plan.CapabilityRevision,
		PolicyRevision: plan.SecurityPolicyRevision, FindingSetDigest: plan.SecurityFindingSetDigest,
		SecurityOverrideCandidateDigest: candidateDigest, SecurityOverrideCategories: candidateCategories,
		OperationSetDigest: plan.OperationSetDigest, DeleteSetDigest: plan.DeleteSetDigest,
		EncryptedOperationRows: encodedOperationRows,
		EstimatedItems:         plan.EstimatedItems, EstimatedBytes: plan.EstimatedBytes,
		ExpiresAt: plan.PreflightExpiresAt, CreatedAt: base.now,
	}
	if err := base.db.Create(&preflight).Error; err != nil {
		t.Fatal(err)
	}

	leaseService, err := backupasset.NewLeaseService(base.db, func() time.Time { return base.now }, backupasset.LeaseConfig{
		Duration: 10 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	audit := &authorizationReceiptAuditSpy{}
	fixture := &authorizationReceiptServiceFixture{db: base.db, now: base.now, audit: audit}
	fixture.dependencies = AuthorizationServiceDependencies{
		DB: base.db, Now: func() time.Time { return fixture.now }, SourceLeases: leaseService,
		NodeAdmission: authorizationReceiptNodeAdmission{}, LiveRevalidator: authorizationReceiptLiveRevalidator{},
		LocatorKeys: newAuthorizationReceiptLocatorKeys(), AuditWriter: audit,
		ReceiptReplayTTL: 20 * time.Minute, WriteGrantTTL: 15 * time.Minute,
		DeleteGrantTTL: 10 * time.Minute, NodeLeaseTTL: 10 * time.Minute,
	}
	fixture.service, err = NewAuthorizationService(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request = fixture.newRequest(operation, plan, preflight)
	if prepareOperation {
		fixture.prepareOperation(t, operation)
	}
	return fixture
}

func newAuthorizationReceiptPostgresServiceFixture(
	t *testing.T,
	operation AuthorizationReceiptOperation,
) *authorizationReceiptServiceFixture {
	t.Helper()
	db := newAuthorizationReceiptPostgresScopedDB(t)
	return newAuthorizationReceiptServiceFixtureOnDB(t, db, operation)
}

func newAuthorizationReceiptPostgresMigrationServiceFixtureWithDeleteCount(
	t *testing.T,
	operation AuthorizationReceiptOperation,
	exactMirror bool,
	prepareOperation bool,
	deleteCount int,
) *authorizationReceiptServiceFixture {
	t.Helper()
	return newAuthorizationReceiptPostgresMigrationServiceFixtureWithDeleteCountAndKinds(
		t, operation, exactMirror, prepareOperation, deleteCount, nil,
	)
}

func newAuthorizationReceiptPostgresMigrationServiceFixtureWithDeleteCountAndKinds(
	t *testing.T,
	operation AuthorizationReceiptOperation,
	exactMirror bool,
	prepareOperation bool,
	deleteCount int,
	nonDeleteKinds []RecoveryOperationKind,
) *authorizationReceiptServiceFixture {
	t.Helper()
	if (!exactMirror && deleteCount != 0) || (exactMirror && deleteCount < 1) {
		t.Fatal("authorization migration fixture has invalid exact-mirror delete count")
	}
	db := newAuthorizationReceiptPostgresScopedDB(t)
	if err := database.RunMigrations(db, "postgres"); err != nil {
		t.Fatalf("apply paired production PostgreSQL migrations through 000069: %v", err)
	}
	fixture := newAuthorizationReceiptServiceFixtureOnDBConfigured(
		t, db, operation, exactMirror, prepareOperation, false, deleteCount, nonDeleteKinds,
	)
	var scheduler model.BackupAssetRecoveryEvidence
	if err := db.Select("updated_at").Where("id = ?", recoveryClaimSchedulerRowID).Take(&scheduler).Error; err != nil {
		t.Fatalf("load production PostgreSQL recovery scheduler clock floor: %v", err)
	}
	// PostgreSQL preserves the migration's subsecond CURRENT_TIMESTAMP, while
	// the shared fixture clock is second-aligned for deterministic comparisons.
	if !fixture.now.After(scheduler.UpdatedAt.UTC()) {
		fixture.advanceTo(scheduler.UpdatedAt.Add(time.Microsecond))
	}
	return fixture
}

func newAuthorizationReceiptPostgresScopedDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("REQUIRE_POSTGRES_RECOVERY_TEST")) == "1" {
			t.Fatal("TEST_POSTGRES_DSN is required when REQUIRE_POSTGRES_RECOVERY_TEST=1")
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}
	base, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
	if err != nil {
		t.Fatalf("open authorization-receipt PostgreSQL base: %v", err)
	}
	schema := fmt.Sprintf("xirang_recovery_authorization_%d", time.Now().UTC().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		closeAuthorizationReceiptDB(t, base)
		t.Fatalf("create authorization-receipt PostgreSQL schema: %v", err)
	}

	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("timezone", "UTC")
	parsed.RawQuery = query.Encode()
	db, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: parsed.String()})
	if err != nil {
		_ = base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error
		closeAuthorizationReceiptDB(t, base)
		t.Fatalf("open scoped authorization-receipt PostgreSQL DB: %v", err)
	}
	t.Cleanup(func() {
		closeAuthorizationReceiptDB(t, db)
		if err := base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error; err != nil {
			t.Errorf("drop authorization-receipt PostgreSQL schema: %v", err)
		}
		closeAuthorizationReceiptDB(t, base)
	})
	return db
}

func newAuthorizationReceiptSQLiteMigrationServiceFixture(
	t *testing.T,
	operation AuthorizationReceiptOperation,
	exactMirror bool,
	prepareOperation bool,
) *authorizationReceiptServiceFixture {
	t.Helper()
	deleteCount := 0
	if exactMirror {
		deleteCount = 1
	}
	return newAuthorizationReceiptSQLiteMigrationServiceFixtureWithDeleteCount(
		t, operation, exactMirror, prepareOperation, deleteCount,
	)
}

func newAuthorizationReceiptSQLiteMigrationServiceFixtureWithDeleteCount(
	t *testing.T,
	operation AuthorizationReceiptOperation,
	exactMirror bool,
	prepareOperation bool,
	deleteCount int,
) *authorizationReceiptServiceFixture {
	t.Helper()
	if (!exactMirror && deleteCount != 0) || (exactMirror && deleteCount < 1) {
		t.Fatal("authorization migration fixture has invalid exact-mirror delete count")
	}
	db, err := database.Open(config.Config{
		DBType:     "sqlite",
		SQLitePath: filepath.Join(t.TempDir(), "recovery.sqlite"),
	})
	if err != nil {
		t.Fatalf("open authorization-receipt production SQLite DB: %v", err)
	}
	t.Cleanup(func() { closeAuthorizationReceiptDB(t, db) })
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatalf("apply paired production SQLite migrations through 000069: %v", err)
	}
	return newAuthorizationReceiptServiceFixtureOnDBConfigured(
		t, db, operation, exactMirror, prepareOperation, false, deleteCount, nil,
	)
}

func newAuthorizationReceiptServiceFixtureOnDB(
	t *testing.T,
	db *gorm.DB,
	operation AuthorizationReceiptOperation,
) *authorizationReceiptServiceFixture {
	deleteCount := 0
	if operation == AuthorizationReceiptDeleteAuthorize {
		deleteCount = 1
	}
	return newAuthorizationReceiptServiceFixtureOnDBConfigured(
		t, db, operation, operation == AuthorizationReceiptDeleteAuthorize, true, true,
		deleteCount, nil,
	)
}

func newAuthorizationReceiptServiceFixtureOnDBConfigured(
	t *testing.T,
	db *gorm.DB,
	operation AuthorizationReceiptOperation,
	exactMirror bool,
	prepareOperation bool,
	initializeSchema bool,
	deleteCount int,
	nonDeleteKinds []RecoveryOperationKind,
) *authorizationReceiptServiceFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RECOVERY_AUTHORIZATION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	if initializeSchema {
		if err := db.AutoMigrate(
			&model.User{}, &model.Node{}, &model.BackupRepository{}, &model.RecoveryPoint{},
			&model.RecoveryPointManifest{}, &model.CatalogGeneration{}, &model.CatalogEntry{},
			&model.RecoveryPointLease{}, &model.BackupAssetRecoveryPlan{},
			&model.BackupAssetRecoveryPlanItem{}, &model.BackupAssetRecoveryPreflight{},
			&model.BackupAssetRecoveryGrant{}, &model.BackupAssetRecoveryJob{},
			&model.BackupAssetRecoveryJobItem{}, &model.BackupAssetRecoveryAttempt{},
			&model.BackupAssetRecoveryCheckpoint{}, &model.BackupAssetRecoveryEvidence{},
			&model.BackupAssetRecoveryResultSet{}, &model.BackupAssetRecoveryResult{},
			&model.BackupAssetRecoveryNodeLease{},
		); err != nil {
			t.Fatal(err)
		}
		for _, statement := range []string{
			`CREATE UNIQUE INDEX idx_recovery_point_leases_active_owner_slot
			ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`,
			`CREATE UNIQUE INDEX idx_backup_asset_recovery_node_leases_active_node
			ON backup_asset_recovery_node_leases(node_id) WHERE state = 'active'`,
			`CREATE UNIQUE INDEX idx_backup_asset_recovery_evidence_authorization_idempotency
			ON backup_asset_recovery_evidence(requester_id, endpoint, idempotency_key_digest)
			WHERE kind = 'authorization_receipt'`,
			`CREATE UNIQUE INDEX idx_backup_asset_recovery_evidence_authorization_proof
			ON backup_asset_recovery_evidence(step_up_jti_digest)
			WHERE kind = 'authorization_receipt' AND step_up_jti_digest <> ''`,
			`CREATE INDEX idx_backup_asset_recovery_evidence_authorization_reaper
			ON backup_asset_recovery_evidence(kind, replay_expires_at, id)`,
		} {
			if err := db.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	user := model.User{ID: 101, Username: "recovery-authorization-postgres-admin",
		PasswordHash: "FAKE_PASSWORD_HASH_FOR_TEST_ONLY", Role: "admin", TokenVersion: 1, TOTPEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	node := model.Node{ID: 27, Name: "recovery-authorization-postgres-node", Host: "127.0.0.1",
		Port: 22, Username: "root", AuthType: "key", BackupDir: "/tmp/recovery-authorization-postgres"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	repositoryID := strings.Repeat("a", 32)
	pointID := strings.Repeat("b", 32)
	repository := model.BackupRepository{ID: repositoryID, ProviderKind: "restic", DisplayName: "recovery-authorization",
		VersionMode: "native_snapshot", Status: "online", ImmutabilityLevel: "backend_versioned", CapabilityRevision: 1}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatal(err)
	}
	sourceLocator := "FAKE_RECOVERY_LOCATOR_FOR_TEST_ONLY"
	point := model.RecoveryPoint{ID: pointID, RepositoryID: repositoryID, EncryptedProviderLocator: sourceLocator,
		Semantics: "native_snapshot", State: "committed", SourceFingerprint: strings.Repeat("c", 64),
		ManifestDigestAlgorithm: "sha256", ManifestDigest: strings.Repeat("d", 64), CapabilityRevision: 1,
		ImmutabilityLevel: "backend_versioned", PhysicalAvailability: "online", HoldState: "none", ObservedAt: &now, CommittedAt: &now}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	manifestID := strings.Repeat("5", 32)
	generationID := strings.Repeat("6", 32)
	manifest := model.RecoveryPointManifest{
		ID: manifestID, RecoveryPointID: pointID, Revision: 1, DigestAlgorithm: "sha256",
		Digest: point.ManifestDigest, Generator: "recovery-authorization-test",
		GeneratorVersion: "1", Completeness: string(backupasset.ManifestComplete), EntryCount: 1,
		LogicalBytes: 1, FidelityJSON: `{}`, IsActive: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&manifest).Error; err != nil {
		t.Fatal(err)
	}
	generation := model.CatalogGeneration{
		ID: generationID, RecoveryPointID: pointID, ManifestID: &manifestID, Generation: 1,
		State: string(backupasset.CatalogGenerationComplete), IsActive: true,
		ExpectedEntryCount: 1, WrittenEntryCount: 1, ExpectedDigest: point.ManifestDigest,
		WrittenDigest: point.ManifestDigest, StartedAt: now, FinishedAt: timePointer(now),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&generation).Error; err != nil {
		t.Fatal(err)
	}

	targetMode := "isolated"
	conflictPolicy := "fail_on_conflict"
	if exactMirror {
		targetMode = "in_place"
		conflictPolicy = "exact_mirror"
	}
	selectedRef := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: strings.Repeat("e", 64)}
	entry := model.CatalogEntry{
		GenerationID: generationID, EntryID: selectedRef.EntryID, RecoveryPointID: pointID,
		NormalizedPath: "/selected", Name: "selected", EntryType: string(backupasset.CatalogEntryFile),
		Size: 1, CreatedAt: now,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	sourceValidator, err := NewSourceValidator(db)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := sourceValidator.FreezeSelection(context.Background(), SourceSelectionRequest{
		RepositoryID: repositoryID, RecoveryPointID: pointID, CatalogGenerationID: generationID,
		AssetRefs: []backupasset.AssetRef{selectedRef}, MaxItems: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	targetRootLocator := "/srv/FAKE_RECOVERY_TARGET_ROOT_FOR_TEST_ONLY"
	targetRootLocatorDigest, err := settings.RecoveryTargetRootLocatorDigest(
		node.ID, "recovery-root", targetRootLocator,
	)
	if err != nil {
		t.Fatalf("construct migration fixture target-root digest: %v", err)
	}
	targetRelativePath := "FAKE_RECOVERY_TARGET_PATH_FOR_TEST_ONLY"
	targetPathDigest, err := TargetPathDigest("recovery-root", targetRootLocatorDigest, targetRelativePath)
	if err != nil {
		t.Fatalf("construct migration fixture target-path digest: %v", err)
	}
	operationProducts, encodedOperationRows := mustAuthorizationReceiptOperationSnapshotConfiguredWithDeleteCount(
		t, []backupasset.AssetRef{selectedRef}, nonDeleteKinds, deleteCount,
		"recovery-root", targetRootLocatorDigest, exactMirror && !prepareOperation,
	)
	securityDecision := "allow_clean"
	if operation == AuthorizationReceiptSecurityOverride {
		securityDecision = "block"
	}
	plan := model.BackupAssetRecoveryPlan{
		ID: strings.Repeat("1", 32), RequesterID: user.ID, Endpoint: "recovery_plan_create",
		IdempotencyKeyDigest: strings.Repeat("2", 64), RepositoryID: repositoryID,
		RecoveryPointID: pointID, SourceRevisionDigest: selection.SourceRevisionDigest,
		SourceRevisionKind:      string(selection.SourceRevision.Kind),
		ImmutableLocatorDigest:  selection.SourceRevision.Immutable.LocatorDigest,
		ImmutableManifestDigest: selection.SourceRevision.Immutable.ManifestDigest, CatalogGenerationID: generationID,
		EncryptedSourceLocator: sourceLocator, TargetMode: targetMode,
		TargetNodeID: node.ID, TargetRootID: "recovery-root",
		EncryptedTargetRootLocator:  targetRootLocator,
		EncryptedTargetRelativePath: targetRelativePath,
		RootLocatorDigest:           targetRootLocatorDigest, PathDigest: targetPathDigest,
		TargetBaseRevision: "target-revision-1", CredentialScopeRevision: "credential-revision-1",
		RootRevision: "root-revision-1", FilesystemRevision: "filesystem-revision-1",
		SelectionDigest: selection.SelectionDigest, BindingDigest: strings.Repeat("a", 64),
		CapabilityRevision: "capability-revision-1", ConflictPolicy: conflictPolicy,
		OperationSetDigest: operationProducts.OperationSetDigest, DeleteSetDigest: operationProducts.DeleteSetDigest,
		SecurityDecision: securityDecision, SecurityDecisionDigest: strings.Repeat("c", 64),
		SecurityFindingSetDigest: strings.Repeat("d", 64), SecurityPolicyRevision: "security-policy-revision-1",
		PreflightRevision: "preflight-revision-1", PreflightExpiresAt: now.Add(time.Hour),
		EstimatedItems: operationProducts.Impact.EstimatedItems,
		EstimatedBytes: operationProducts.Impact.EstimatedBytes, State: string(PlanStatePreflightReady),
		TransitionRevision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	item := model.BackupAssetRecoveryPlanItem{ID: strings.Repeat("2", 32), PlanID: plan.ID, Ordinal: 0,
		RecoveryPointID: pointID, CatalogGenerationID: plan.CatalogGenerationID,
		EntryID: selectedRef.EntryID, EntryType: "file", RelativePathDigest: publication.RecoveryPlanItemPathDigest(
			repositoryID, pointID, generationID, selectedRef.EntryID, entry.NormalizedPath,
		), CreatedAt: now}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	candidateDigest, candidateCategories := authorizationReceiptOverrideCandidate(plan, operation)
	preflight := model.BackupAssetRecoveryPreflight{ID: strings.Repeat("3", 32), PlanID: plan.ID,
		Revision: plan.PreflightRevision, SourceRevisionDigest: plan.SourceRevisionDigest,
		TargetNodeID: node.ID, NodeRevision: plan.TargetBaseRevision, TargetRootID: plan.TargetRootID,
		RootLocatorDigest: plan.RootLocatorDigest, PathDigest: plan.PathDigest, TargetRevision: plan.TargetBaseRevision,
		CapabilityRevision: plan.CapabilityRevision, PolicyRevision: plan.SecurityPolicyRevision,
		FindingSetDigest: plan.SecurityFindingSetDigest, SecurityOverrideCandidateDigest: candidateDigest,
		SecurityOverrideCategories: candidateCategories, OperationSetDigest: plan.OperationSetDigest,
		DeleteSetDigest: plan.DeleteSetDigest, EncryptedOperationRows: encodedOperationRows,
		EstimatedItems: plan.EstimatedItems, EstimatedBytes: plan.EstimatedBytes,
		ExpiresAt: plan.PreflightExpiresAt, CreatedAt: now}
	if err := db.Create(&preflight).Error; err != nil {
		t.Fatal(err)
	}

	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 10 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	audit := &authorizationReceiptAuditSpy{}
	fixture := &authorizationReceiptServiceFixture{db: db, now: now, audit: audit}
	fixture.dependencies = AuthorizationServiceDependencies{
		DB: db, Now: func() time.Time { return fixture.now }, SourceLeases: leaseService,
		NodeAdmission: authorizationReceiptNodeAdmission{}, LiveRevalidator: authorizationReceiptLiveRevalidator{},
		LocatorKeys: newAuthorizationReceiptLocatorKeys(), AuditWriter: audit,
		ReceiptReplayTTL: 20 * time.Minute, WriteGrantTTL: 15 * time.Minute,
		DeleteGrantTTL: 10 * time.Minute, NodeLeaseTTL: 10 * time.Minute,
	}
	fixture.service, err = NewAuthorizationService(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request = fixture.newRequest(operation, plan, preflight)
	if prepareOperation {
		fixture.prepareOperation(t, operation)
	}
	return fixture
}

func authorizationReceiptOverrideCandidate(
	plan model.BackupAssetRecoveryPlan,
	operation AuthorizationReceiptOperation,
) (string, string) {
	if operation != AuthorizationReceiptSecurityOverride {
		return "", ""
	}
	categories := string(SecurityFindingSuspicious)
	return framedDigest(
		securityOverrideCandidateDomain,
		plan.SecurityFindingSetDigest,
		plan.SecurityPolicyRevision,
		categories,
	), categories
}

func closeAuthorizationReceiptDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Errorf("load authorization-receipt SQL DB: %v", err)
		return
	}
	if err := sqlDB.Close(); err != nil {
		t.Errorf("close authorization-receipt SQL DB: %v", err)
	}
}

func (fixture *authorizationReceiptServiceFixture) newRequest(
	operation AuthorizationReceiptOperation,
	plan model.BackupAssetRecoveryPlan,
	preflight model.BackupAssetRecoveryPreflight,
) RecoveryAuthorizationRequest {
	category := AuthorizationReceiptCategorySecurityOverride
	endpoint := recoverySecurityOverrideEndpoint
	secret := ""
	reason := "FAKE_RECOVERY_REASON_FOR_AUTHORIZATION_RECEIPT"
	switch operation {
	case AuthorizationReceiptWriteAuthorize:
		category = AuthorizationReceiptCategoryWrite
		endpoint = recoveryWriteAuthorizationEndpoint
		secret = mustAuthorizationReceiptSecretForFixture()
	case AuthorizationReceiptDeleteAuthorize:
		category = AuthorizationReceiptCategoryExactMirrorDelete
		endpoint = recoveryDeleteAuthorizationEndpoint
		secret = mustAuthorizationReceiptSecretForFixture()
	case AuthorizationReceiptExecute:
		category = AuthorizationReceiptCategoryExecute
		endpoint = recoveryExecuteEndpoint
		reason = ""
	}
	return RecoveryAuthorizationRequest{
		RequesterID: plan.RequesterID, PlanID: plan.ID, Endpoint: endpoint,
		IdempotencyKey: "authorization-receipt-idempotency-key-0001",
		Operation:      operation, Category: category, ExpectedPlanRevision: plan.TransitionRevision,
		PreflightID: preflight.ID, FindingCategory: SecurityFindingSuspicious,
		Reason: reason, GrantSecret: secret,
		Proof: RecoveryAuthorizationProof{
			JTI: "FAKE_RECOVERY_STEP_UP_PROOF_JTI", Action: "asset.recover",
			UserID: plan.RequesterID, Role: "admin", TokenVersion: 1,
			ExpiresAt: fixture.now.Add(10 * time.Minute),
		},
		Session: RecoveryAuthorizationSession{
			JTI: "FAKE_RECOVERY_PRESENTING_SESSION_JTI", UserID: plan.RequesterID,
			Role: "admin", TokenVersion: 1, ExpiresAt: fixture.now.Add(30 * time.Minute),
		},
	}
}

func (fixture *authorizationReceiptServiceFixture) prepareOperation(
	t *testing.T,
	operation AuthorizationReceiptOperation,
) {
	t.Helper()
	if operation != AuthorizationReceiptExecute && operation != AuthorizationReceiptDeleteAuthorize {
		return
	}
	write := fixture.request
	write.Operation = AuthorizationReceiptWriteAuthorize
	write.Category = AuthorizationReceiptCategoryWrite
	write.Endpoint = recoveryWriteAuthorizationEndpoint
	write.IdempotencyKey = "authorization-receipt-prerequisite-write-key"
	write.Proof.JTI = "FAKE_RECOVERY_PREREQUISITE_WRITE_PROOF_JTI"
	write.Reason = "FAKE_RECOVERY_PREREQUISITE_WRITE_REASON"
	write.GrantSecret = mustAuthorizationReceiptSecretForFixture()
	writeResult, err := fixture.service.Authorize(context.Background(), write)
	if err != nil {
		t.Fatalf("prepare %s write authority: %v", operation, err)
	}
	fixture.request.ExpectedPlanRevision = writeResult.PlanTransitionRevision
	fixture.request.GrantID = writeResult.GrantID
	fixture.request.GrantSecret = write.GrantSecret
	fixture.request.Proof.JTI = "FAKE_RECOVERY_OPERATION_PROOF_JTI"
	fixture.request.IdempotencyKey = "authorization-receipt-operation-key-0001"
	if operation == AuthorizationReceiptExecute {
		return
	}

	execute := fixture.request
	execute.Operation = AuthorizationReceiptExecute
	execute.Category = AuthorizationReceiptCategoryExecute
	execute.Endpoint = recoveryExecuteEndpoint
	execute.IdempotencyKey = "authorization-receipt-prerequisite-execute-key"
	execute.Proof.JTI = "FAKE_RECOVERY_PREREQUISITE_EXECUTE_PROOF_JTI"
	execute.Reason = ""
	executeResult, err := fixture.service.Authorize(context.Background(), execute)
	if err != nil {
		t.Fatalf("prepare delete authority execution: %v", err)
	}
	fixture.request.ExpectedPlanRevision = executeResult.PlanTransitionRevision
	fixture.request.JobID = executeResult.JobID
	fixture.request.AttemptID = executeResult.AttemptID
	fixture.request.GrantID = ""
	fixture.request.Proof.JTI = "FAKE_RECOVERY_DELETE_OPERATION_PROOF_JTI"
	fixture.request.GrantSecret = mustAuthorizationReceiptSecretForFixture()

	var job model.BackupAssetRecoveryJob
	if err := fixture.db.Where("id = ?", executeResult.JobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.BackupAssetRecoveryJob{}).Where("id = ?", job.ID).
		Updates(map[string]any{"state": "running", "updated_at": fixture.now}).Error; err != nil {
		t.Fatal(err)
	}
	job.State = "running"
	checkpoint := model.BackupAssetRecoveryCheckpoint{
		ID: strings.Repeat("8", 32), JobID: job.ID, AttemptID: executeResult.AttemptID,
		Sequence: 1, Phase: "delete_authority_required", AuthorityCategory: string(AuthorityExactMirrorDelete),
		OperationDigest: job.DeleteSetDigest, PriorTargetRevision: job.TargetChainRevision,
		NodeFence: executeResult.NodeLeaseFence, AttemptFence: 1,
		PlanBindingDigest: job.PlanBindingDigest, SourceRevisionDigest: job.SourceRevisionDigest,
		PreflightID: job.PreflightID, PreflightRevision: job.PreflightRevision,
		PreflightExpiresAt: job.PreflightExpiresAt, SecurityDecision: job.SecurityDecision,
		SecurityDecisionDigest:   job.SecurityDecisionDigest,
		SecurityFindingSetDigest: job.SecurityFindingSetDigest,
		SecurityPolicyRevision:   job.SecurityPolicyRevision, AuthorityGrantID: job.AuthorityGrantID,
		JobAuthorityCategory: job.AuthorityCategory, AuthorityBindingDigest: job.AuthorityBindingDigest,
		AuthorityExpiresAt: job.AuthorityExpiresAt, DeleteNodeRevision: "delete-node-revision",
		DeleteRootRevision: "root-revision-1", DeleteAuthorityExpiresAt: timePointer(fixture.now.Add(8 * time.Minute)),
		CreatedAt: fixture.now,
	}
	if err := fixture.db.Create(&checkpoint).Error; err != nil {
		t.Fatal(err)
	}
	fixture.request.CheckpointID = checkpoint.ID
}

func (fixture *authorizationReceiptServiceFixture) advanceTo(now time.Time) {
	fixture.now = now.UTC()
}

func (fixture *authorizationReceiptServiceFixture) restartService(t *testing.T) *AuthorizationService {
	t.Helper()
	service, err := NewAuthorizationService(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func (fixture *authorizationReceiptServiceFixture) clonePlan(t *testing.T) string {
	t.Helper()
	var source model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	clone := source
	clone.ID = strings.Repeat("9", 32)
	clone.IdempotencyKeyDigest = strings.Repeat("9", 64)
	clone.CreatedAt = fixture.now
	clone.UpdatedAt = fixture.now
	if err := fixture.db.Create(&clone).Error; err != nil {
		t.Fatal(err)
	}
	return clone.ID
}

func (fixture *authorizationReceiptServiceFixture) cloneAuthorizationPlan(t *testing.T) (string, string) {
	t.Helper()
	var source model.BackupAssetRecoveryPlan
	if err := fixture.db.Where("id = ?", fixture.request.PlanID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	var sourcePreflight model.BackupAssetRecoveryPreflight
	if err := fixture.db.Where("id = ? AND plan_id = ?", fixture.request.PreflightID, source.ID).
		Take(&sourcePreflight).Error; err != nil {
		t.Fatal(err)
	}
	planID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	preflightID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	clone := source
	clone.ID = planID
	clone.IdempotencyKeyDigest = framedDigest(planIdempotencyDigestDomain, planID)
	clone.CreatedAt = fixture.now
	clone.UpdatedAt = fixture.now
	if err := fixture.db.Create(&clone).Error; err != nil {
		t.Fatal(err)
	}
	var sourceItems []model.BackupAssetRecoveryPlanItem
	if err := fixture.db.Where("plan_id = ?", source.ID).Order("ordinal ASC").Find(&sourceItems).Error; err != nil {
		t.Fatal(err)
	}
	for _, sourceItem := range sourceItems {
		itemID, err := backupasset.NewOpaqueID()
		if err != nil {
			t.Fatal(err)
		}
		item := sourceItem
		item.ID = itemID
		item.PlanID = planID
		if err := fixture.db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	preflight := sourcePreflight
	preflight.ID = preflightID
	preflight.PlanID = planID
	preflight.CreatedAt = fixture.now
	if err := fixture.db.Create(&preflight).Error; err != nil {
		t.Fatal(err)
	}
	return planID, preflightID
}

type authorizationEffectCounts struct {
	Receipts     int64
	Grants       int64
	Jobs         int64
	JobItems     int64
	Attempts     int64
	SourceLeases int64
	NodeLeases   int64
}

func (fixture *authorizationReceiptServiceFixture) effectCounts(t *testing.T) authorizationEffectCounts {
	t.Helper()
	count := func(modelValue any, query string, args ...any) int64 {
		t.Helper()
		var value int64
		if err := fixture.db.Model(modelValue).Where(query, args...).Count(&value).Error; err != nil {
			t.Fatal(err)
		}
		return value
	}
	return authorizationEffectCounts{
		Receipts:     count(&model.BackupAssetRecoveryEvidence{}, "kind = ?", "authorization_receipt"),
		Grants:       count(&model.BackupAssetRecoveryGrant{}, "1 = 1"),
		Jobs:         count(&model.BackupAssetRecoveryJob{}, "1 = 1"),
		JobItems:     count(&model.BackupAssetRecoveryJobItem{}, "1 = 1"),
		Attempts:     count(&model.BackupAssetRecoveryAttempt{}, "1 = 1"),
		SourceLeases: count(&model.RecoveryPointLease{}, "holder_type = ?", "recovery_job"),
		NodeLeases:   count(&model.BackupAssetRecoveryNodeLease{}, "1 = 1"),
	}
}

func (counts authorizationEffectCounts) addedExactlyOneEffect(
	operation AuthorizationReceiptOperation,
	before authorizationEffectCounts,
) bool {
	if counts.Receipts != before.Receipts+1 {
		return false
	}
	switch operation {
	case AuthorizationReceiptSecurityOverride:
		return counts.Grants == before.Grants && counts.Jobs == before.Jobs
	case AuthorizationReceiptWriteAuthorize, AuthorizationReceiptDeleteAuthorize:
		return counts.Grants == before.Grants+1 && counts.Jobs == before.Jobs
	case AuthorizationReceiptExecute:
		return counts.Grants == before.Grants && counts.Jobs == before.Jobs+1 &&
			counts.Attempts == before.Attempts+1 && counts.SourceLeases == before.SourceLeases+1 &&
			counts.NodeLeases == before.NodeLeases+1 && counts.JobItems > before.JobItems
	default:
		return false
	}
}

func (fixture *authorizationReceiptServiceFixture) assertRawSecretsAbsent(t *testing.T, raw string) {
	t.Helper()
	var grants []model.BackupAssetRecoveryGrant
	if err := fixture.db.Find(&grants).Error; err != nil {
		t.Fatal(err)
	}
	var receipts []model.BackupAssetRecoveryEvidence
	if err := fixture.db.Find(&receipts).Error; err != nil {
		t.Fatal(err)
	}
	fixture.audit.mu.Lock()
	auditInputs := append([]backupasset.AuditEventInput(nil), fixture.audit.inputs...)
	fixture.audit.mu.Unlock()
	if strings.Contains(fmt.Sprintf("%+v%+v%+v", grants, receipts, auditInputs), raw) {
		t.Fatal("raw authorization grant secret reached a persisted model or audit input")
	}
}

func (fixture *authorizationReceiptServiceFixture) insertProtectedEvidenceRows(t *testing.T) {
	t.Helper()
	now := fixture.now
	rows := []model.BackupAssetRecoveryEvidence{
		{ID: strings.Repeat("a", 32), Kind: "verification", Outcome: "succeeded", CreatedAt: now, UpdatedAt: now},
		{ID: strings.Repeat("0", 30) + "69", Kind: "schema_use_latch", CreatedAt: now, UpdatedAt: now},
	}
	if err := fixture.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
}

func (fixture *authorizationReceiptServiceFixture) assertProtectedEvidenceRows(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"verification", "schema_use_latch"} {
		var count int64
		if err := fixture.db.Model(&model.BackupAssetRecoveryEvidence{}).Where("kind = ?", kind).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("receipt reaper changed protected %s rows: %d", kind, count)
		}
	}
}

func mustAuthorizationReceiptSecretForFixture() string {
	return "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"
}

func timePointer(value time.Time) *time.Time { return &value }

var _ RecoveryAuthorizationAuditWriter = (*authorizationReceiptAuditSpy)(nil)
var _ RecoveryNodeAdmission = authorizationReceiptNodeAdmission{}
