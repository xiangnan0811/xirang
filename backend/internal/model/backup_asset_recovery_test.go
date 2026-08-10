package model

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/secure"

	"gorm.io/gorm"
)

func TestBackupAssetRecoveryModelsUseExactTwelveTables(t *testing.T) {
	models := []interface{ TableName() string }{
		BackupAssetRecoveryPlan{},
		BackupAssetRecoveryPlanItem{},
		BackupAssetRecoveryPreflight{},
		BackupAssetRecoveryGrant{},
		BackupAssetRecoveryJob{},
		BackupAssetRecoveryJobItem{},
		BackupAssetRecoveryAttempt{},
		BackupAssetRecoveryCheckpoint{},
		BackupAssetRecoveryEvidence{},
		BackupAssetRecoveryResultSet{},
		BackupAssetRecoveryResult{},
		BackupAssetRecoveryNodeLease{},
	}
	want := []string{
		"backup_asset_recovery_plans",
		"backup_asset_recovery_plan_items",
		"backup_asset_recovery_preflights",
		"backup_asset_recovery_grants",
		"backup_asset_recovery_jobs",
		"backup_asset_recovery_job_items",
		"backup_asset_recovery_attempts",
		"backup_asset_recovery_checkpoints",
		"backup_asset_recovery_evidence",
		"backup_asset_recovery_result_sets",
		"backup_asset_recovery_results",
		"backup_asset_recovery_node_leases",
	}

	if len(models) != len(want) {
		t.Fatalf("recovery model count=%d want=%d", len(models), len(want))
	}
	seen := make(map[string]struct{}, len(models))
	for index, persistentModel := range models {
		got := persistentModel.TableName()
		if got != want[index] {
			t.Fatalf("recovery model %d table=%q want=%q", index, got, want[index])
		}
		if _, duplicate := seen[got]; duplicate {
			t.Fatalf("duplicate recovery model table %q", got)
		}
		seen[got] = struct{}{}
	}
}

func TestBackupAssetRecoveryModelsCarryClosedProductsAndUniquePlanJob(t *testing.T) {
	testCases := []struct {
		model     any
		field     string
		gormParts []string
	}{
		{model: BackupAssetRecoveryPlan{}, field: "SourceRevisionKind", gormParts: []string{"size:16", "not null"}},
		{model: BackupAssetRecoveryPlan{}, field: "TargetMode", gormParts: []string{"size:16", "not null"}},
		{model: BackupAssetRecoveryPlan{}, field: "SecurityDecision", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryPlan{}, field: "State", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryPlan{}, field: "SelectionDigest", gormParts: []string{"size:64", "not null"}},
		{model: BackupAssetRecoveryPlan{}, field: "BindingDigest", gormParts: []string{"size:64", "not null"}},
		{model: BackupAssetRecoveryPlan{}, field: "OperationSetDigest", gormParts: []string{"size:64", "not null"}},
		{model: BackupAssetRecoveryPlan{}, field: "DeleteSetDigest", gormParts: []string{"size:64", "not null"}},
		{model: BackupAssetRecoveryGrant{}, field: "AuthorityCategory", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryJob{}, field: "PlanID", gormParts: []string{"size:32", "not null", "uniqueIndex:idx_backup_asset_recovery_jobs_plan"}},
		{model: BackupAssetRecoveryJob{}, field: "State", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryJob{}, field: "WorkspacePhase", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryAttempt{}, field: "State", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryCheckpoint{}, field: "Phase", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryResultSet{}, field: "State", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryResultSet{}, field: "CleanupPhase", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryNodeLease{}, field: "State", gormParts: []string{"size:32", "not null"}},
	}

	for _, testCase := range testCases {
		modelType := reflect.TypeOf(testCase.model)
		t.Run(modelType.Name()+"/"+testCase.field, func(t *testing.T) {
			field, found := modelType.FieldByName(testCase.field)
			if !found {
				t.Fatalf("%s is missing closed-product field %s", modelType.Name(), testCase.field)
			}
			if field.Type.Kind() != reflect.String {
				t.Fatalf("%s.%s kind=%s want string-backed closed product", modelType.Name(), testCase.field, field.Type.Kind())
			}
			gormTag := field.Tag.Get("gorm")
			for _, fragment := range testCase.gormParts {
				if !strings.Contains(gormTag, fragment) {
					t.Fatalf("%s.%s gorm tag %q omits %q", modelType.Name(), testCase.field, gormTag, fragment)
				}
			}
			if field.Tag.Get("json") != "-" {
				t.Fatalf("%s.%s must remain private with json:\"-\"", modelType.Name(), testCase.field)
			}
		})
	}
}

func TestBackupAssetRecoveryJobCarriesPrivateMarkerValidationProduct(t *testing.T) {
	testCases := []struct {
		field     string
		kind      reflect.Kind
		gormParts []string
	}{
		{
			field: "WorkspaceMarkerValidationAttemptID", kind: reflect.String,
			gormParts: []string{"size:32", "not null", "default:''"},
		},
		{
			field: "WorkspaceMarkerValidationAttemptFence", kind: reflect.Uint64,
			gormParts: []string{"not null", "default:0"},
		},
		{
			field: "WorkspaceMarkerValidationNodeFence", kind: reflect.Uint64,
			gormParts: []string{"not null", "default:0"},
		},
	}

	modelType := reflect.TypeOf(BackupAssetRecoveryJob{})
	for _, testCase := range testCases {
		t.Run(testCase.field, func(t *testing.T) {
			field, found := modelType.FieldByName(testCase.field)
			if !found {
				t.Fatalf("BackupAssetRecoveryJob is missing marker-validation field %s", testCase.field)
			}
			if field.Type.Kind() != testCase.kind {
				t.Fatalf("BackupAssetRecoveryJob.%s kind=%s want %s", testCase.field, field.Type.Kind(), testCase.kind)
			}
			if field.Tag.Get("json") != "-" {
				t.Fatalf("BackupAssetRecoveryJob.%s must remain private with json:\"-\"", testCase.field)
			}
			for _, fragment := range testCase.gormParts {
				if !strings.Contains(field.Tag.Get("gorm"), fragment) {
					t.Fatalf("BackupAssetRecoveryJob.%s gorm tag %q omits %q",
						testCase.field, field.Tag.Get("gorm"), fragment)
				}
			}
		})
	}
}

func TestRecoveryAuthorizationReceiptModelContract(t *testing.T) {
	modelType := reflect.TypeOf(BackupAssetRecoveryEvidence{})
	for _, testCase := range []struct {
		field     string
		gormParts []string
	}{
		{field: "PlanID", gormParts: []string{"size:32", "index"}},
		{field: "CheckpointID", gormParts: []string{"size:32"}},
		{field: "GrantID", gormParts: []string{"size:32"}},
		{field: "AttemptID", gormParts: []string{"size:32"}},
		{field: "SourceLeaseID", gormParts: []string{"size:32"}},
		{field: "NodeLeaseID", gormParts: []string{"size:32"}},
		{field: "RequesterID", gormParts: []string{"index"}},
		{field: "Operation", gormParts: []string{"size:48", "not null", "default:''"}},
		{field: "Category", gormParts: []string{"size:32", "not null", "default:''"}},
		{field: "Endpoint", gormParts: []string{"size:96", "not null", "default:''"}},
		{field: "IdempotencyKeyDigest", gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "IntentDigest", gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "StepUpJTIDigest", gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "PresentingSessionDigest", gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "PresentingSessionRole", gormParts: []string{"size:32", "not null", "default:''"}},
		{field: "GrantBindingDigest", gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "SourceLeaseBindingDigest", gormParts: []string{"size:64", "not null", "default:''"}},
	} {
		field, found := modelType.FieldByName(testCase.field)
		if !found {
			t.Fatalf("%s is missing private authorization-receipt field %s", modelType.Name(), testCase.field)
		}
		if field.Tag.Get("json") != "-" {
			t.Fatalf("%s.%s must remain private with json:\"-\"", modelType.Name(), testCase.field)
		}
		for _, fragment := range testCase.gormParts {
			if !strings.Contains(field.Tag.Get("gorm"), fragment) {
				t.Fatalf("%s.%s gorm tag %q omits %q", modelType.Name(), testCase.field, field.Tag.Get("gorm"), fragment)
			}
		}
	}

	for _, fieldName := range []string{
		"PresentingSessionUserID",
		"PresentingSessionTokenVersion",
		"ProofExpiresAt",
		"PresentingSessionExpiresAt",
		"ReplayExpiresAt",
		"ExpectedPlanTransitionRevision",
		"ResultPlanTransitionRevision",
		"NodeLeaseFence",
	} {
		field, found := modelType.FieldByName(fieldName)
		if !found {
			t.Fatalf("%s is missing authorization-receipt effect/deadline field %s", modelType.Name(), fieldName)
		}
		if field.Tag.Get("json") != "-" {
			t.Fatalf("%s.%s must remain private with json:\"-\"", modelType.Name(), fieldName)
		}
	}
}

func TestRecoveryAuthorizationReceiptOperationSnapshotModelContract(t *testing.T) {
	preflightType := reflect.TypeOf(BackupAssetRecoveryPreflight{})
	snapshot, found := preflightType.FieldByName("EncryptedOperationRows")
	if !found {
		t.Fatal("BackupAssetRecoveryPreflight is missing encrypted operation rows")
	}
	if snapshot.Type.Kind() != reflect.String || snapshot.Tag.Get("json") != "-" {
		t.Fatalf("EncryptedOperationRows type/json=%s/%q, want private string", snapshot.Type.Kind(), snapshot.Tag.Get("json"))
	}
	for _, fragment := range []string{"type:text", "not null"} {
		if !strings.Contains(snapshot.Tag.Get("gorm"), fragment) {
			t.Fatalf("EncryptedOperationRows gorm tag %q omits %q", snapshot.Tag.Get("gorm"), fragment)
		}
	}

	jobItemType := reflect.TypeOf(BackupAssetRecoveryJobItem{})
	planItem, found := jobItemType.FieldByName("PlanItemID")
	if !found || planItem.Type.Kind() != reflect.Pointer || planItem.Type.Elem().Kind() != reflect.String || planItem.Tag.Get("json") != "-" {
		t.Fatalf("PlanItemID field=%+v found=%v, want private nullable string", planItem, found)
	}
	for _, fieldName := range []string{
		"TargetPathDigest", "ExpectedPriorKind", "ExpectedPriorDigest", "DisplayClass", "EstimatedBytes",
	} {
		field, exists := jobItemType.FieldByName(fieldName)
		if !exists {
			t.Fatalf("BackupAssetRecoveryJobItem is missing %s", fieldName)
		}
		if field.Tag.Get("json") != "-" {
			t.Fatalf("BackupAssetRecoveryJobItem.%s must remain private", fieldName)
		}
	}
}

func TestRecoveryJobItemPersistsPrivateOperationAndLocatorBinding(t *testing.T) {
	jobItemType := reflect.TypeOf(BackupAssetRecoveryJobItem{})
	for _, testCase := range []struct {
		field     string
		kind      reflect.Kind
		gormParts []string
	}{
		{field: "ExpectedPostIdentityDigest", kind: reflect.String, gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "ExpectedPostBytes", kind: reflect.Int64, gormParts: []string{"not null", "default:-1"}},
		{field: "ExpectedPriorBytes", kind: reflect.Int64, gormParts: []string{"not null", "default:-1"}},
		{field: "EncryptedTargetRelativeLocator", kind: reflect.String, gormParts: []string{"type:text", "not null"}},
		{field: "TargetLocatorKeyVersion", kind: reflect.Int, gormParts: []string{"not null", "default:0"}},
		{field: "TargetLocatorCipherVersion", kind: reflect.Int, gormParts: []string{"not null", "default:0"}},
	} {
		field, found := jobItemType.FieldByName(testCase.field)
		if !found {
			t.Fatalf("BackupAssetRecoveryJobItem is missing immutable locator/operation field %s", testCase.field)
		}
		if field.Type.Kind() != testCase.kind {
			t.Fatalf("BackupAssetRecoveryJobItem.%s kind=%s want=%s", testCase.field, field.Type.Kind(), testCase.kind)
		}
		if field.Tag.Get("json") != "-" {
			t.Fatalf("BackupAssetRecoveryJobItem.%s must remain private with json:\"-\"", testCase.field)
		}
		for _, fragment := range testCase.gormParts {
			if !strings.Contains(field.Tag.Get("gorm"), fragment) {
				t.Fatalf("BackupAssetRecoveryJobItem.%s gorm tag %q omits %q", testCase.field, field.Tag.Get("gorm"), fragment)
			}
		}
	}

	rawItem := &BackupAssetRecoveryJobItem{}
	if _, ok := any(rawItem).(recoveryEncryptedModel); ok {
		t.Fatal("BackupAssetRecoveryJobItem must bypass generic model hooks for its recovery-local locator envelope")
	}
}

func TestBackupAssetRecoveryModelsCarryPrivateExactMirrorDeleteGrantBinding(t *testing.T) {
	testCases := []struct {
		model     any
		field     string
		gormParts []string
	}{
		{model: BackupAssetRecoveryGrant{}, field: "DeleteCheckpointID", gormParts: []string{"size:32"}},
		{model: BackupAssetRecoveryGrant{}, field: "DeleteSetDigest", gormParts: []string{"size:64", "default:''"}},
		{model: BackupAssetRecoveryGrant{}, field: "DeleteTargetRevision", gormParts: []string{"size:64", "default:''"}},
		{model: BackupAssetRecoveryGrant{}, field: "DeleteAttemptID", gormParts: []string{"size:32"}},
		{model: BackupAssetRecoveryGrant{}, field: "DeleteAttemptFence", gormParts: []string{"default:0"}},
		{model: BackupAssetRecoveryGrant{}, field: "DeleteNodeFence", gormParts: []string{"default:0"}},
		{model: BackupAssetRecoveryCheckpoint{}, field: "DeleteNodeRevision", gormParts: []string{"size:64", "default:''"}},
		{model: BackupAssetRecoveryCheckpoint{}, field: "DeleteRootRevision", gormParts: []string{"size:64", "default:''"}},
		{model: BackupAssetRecoveryCheckpoint{}, field: "DeleteAuthorityExpiresAt", gormParts: nil},
		{model: BackupAssetRecoveryCheckpoint{}, field: "DeleteGrantID", gormParts: []string{"size:32", "default:''"}},
		{model: BackupAssetRecoveryCheckpoint{}, field: "DeleteGrantBindingDigest", gormParts: []string{"size:64", "default:''"}},
		{model: BackupAssetRecoveryCheckpoint{}, field: "DeleteGrantExpiresAt", gormParts: nil},
		{model: BackupAssetRecoveryCheckpoint{}, field: "DeleteGrantConsumedAt", gormParts: nil},
	}

	for _, testCase := range testCases {
		modelType := reflect.TypeOf(testCase.model)
		t.Run(modelType.Name()+"/"+testCase.field, func(t *testing.T) {
			field, found := modelType.FieldByName(testCase.field)
			if !found {
				t.Fatalf("%s is missing exact-mirror delete binding field %s", modelType.Name(), testCase.field)
			}
			if field.Tag.Get("json") != "-" {
				t.Fatalf("%s.%s must remain private with json:\"-\"", modelType.Name(), testCase.field)
			}
			gormTag := field.Tag.Get("gorm")
			for _, fragment := range testCase.gormParts {
				if !strings.Contains(gormTag, fragment) {
					t.Fatalf("%s.%s gorm tag %q omits %q", modelType.Name(), testCase.field, gormTag, fragment)
				}
			}
		})
	}
}

func TestBackupAssetRecoveryCheckpointCarriesPrivateUnresolvedOutcomeProduct(t *testing.T) {
	checkpointType := reflect.TypeOf(BackupAssetRecoveryCheckpoint{})
	for _, testCase := range []struct {
		field     string
		gormParts []string
	}{
		{field: "JobItemID", gormParts: []string{"size:32", "not null", "default:''"}},
		{field: "UnresolvedCategory", gormParts: []string{"size:32", "not null", "default:''"}},
		{field: "WriteResultDigest", gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "WriteTargetRevision", gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "ObservationDigest", gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "ObservedTargetRevision", gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "ObservedPresence", gormParts: []string{"size:16", "not null", "default:''"}},
		{field: "SourceRevalidationOutcome", gormParts: []string{"size:16", "not null", "default:''"}},
	} {
		t.Run(testCase.field, func(t *testing.T) {
			field, found := checkpointType.FieldByName(testCase.field)
			if !found {
				t.Fatalf("BackupAssetRecoveryCheckpoint is missing unresolved-outcome field %s", testCase.field)
			}
			if field.Type.Kind() != reflect.String || field.Tag.Get("json") != "-" {
				t.Fatalf("BackupAssetRecoveryCheckpoint.%s type/json=%s/%q, want private string",
					testCase.field, field.Type.Kind(), field.Tag.Get("json"))
			}
			for _, fragment := range testCase.gormParts {
				if !strings.Contains(field.Tag.Get("gorm"), fragment) {
					t.Fatalf("BackupAssetRecoveryCheckpoint.%s gorm tag %q omits %q",
						testCase.field, field.Tag.Get("gorm"), fragment)
				}
			}
		})
	}
}

func TestBackupAssetRecoveryModelsFreezePrivateTargetBinding(t *testing.T) {
	testCases := []struct {
		model     any
		field     string
		gormParts []string
	}{
		{model: BackupAssetRecoveryPreflight{}, field: "TargetRootID", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryPreflight{}, field: "RootLocatorDigest", gormParts: []string{"size:64", "not null"}},
		{model: BackupAssetRecoveryPreflight{}, field: "PathDigest", gormParts: []string{"size:64", "not null"}},
		{model: BackupAssetRecoveryJob{}, field: "TargetRootID", gormParts: []string{"size:32", "not null"}},
		{model: BackupAssetRecoveryJob{}, field: "RootLocatorDigest", gormParts: []string{"size:64", "not null"}},
		{model: BackupAssetRecoveryJob{}, field: "PathDigest", gormParts: []string{"size:64", "not null"}},
	}

	for _, testCase := range testCases {
		modelType := reflect.TypeOf(testCase.model)
		t.Run(modelType.Name()+"/"+testCase.field, func(t *testing.T) {
			field, found := modelType.FieldByName(testCase.field)
			if !found {
				t.Fatalf("%s is missing frozen target-binding field %s", modelType.Name(), testCase.field)
			}
			if field.Tag.Get("json") != "-" {
				t.Fatalf("%s.%s must remain private with json:\"-\"", modelType.Name(), testCase.field)
			}
			for _, fragment := range testCase.gormParts {
				if !strings.Contains(field.Tag.Get("gorm"), fragment) {
					t.Fatalf("%s.%s gorm tag %q omits %q", modelType.Name(), testCase.field, field.Tag.Get("gorm"), fragment)
				}
			}
		})
	}
}

func TestBackupAssetRecoveryModelsFreezeJobAndCheckpointAuthority(t *testing.T) {
	testCases := []struct {
		model  any
		fields []string
	}{
		{
			model: BackupAssetRecoveryPlan{},
			fields: []string{
				"SourceRevisionDigest",
				"SecurityDecisionDigest",
			},
		},
		{
			model: BackupAssetRecoveryJob{},
			fields: []string{
				"PlanBindingDigest",
				"SelectionDigest",
				"SourceRevisionDigest",
				"PreflightID",
				"PreflightRevision",
				"PreflightExpiresAt",
				"PreflightTargetRevision",
				"CapabilityRevision",
				"OperationSetDigest",
				"DeleteSetDigest",
				"SecurityDecision",
				"SecurityDecisionDigest",
				"SecurityFindingSetDigest",
				"SecurityPolicyRevision",
				"SecurityOverrideBindingDigest",
				"EstimatedItems",
				"EstimatedBytes",
				"AuthorityGrantID",
				"AuthorityCategory",
				"AuthorityBindingDigest",
				"AuthorityExpiresAt",
				"AuthorityConsumedAt",
			},
		},
		{
			model: BackupAssetRecoveryCheckpoint{},
			fields: []string{
				"PlanBindingDigest",
				"SourceRevisionDigest",
				"PreflightID",
				"PreflightRevision",
				"PreflightExpiresAt",
				"SecurityDecision",
				"SecurityDecisionDigest",
				"SecurityFindingSetDigest",
				"SecurityPolicyRevision",
				"AuthorityGrantID",
				"JobAuthorityCategory",
				"AuthorityBindingDigest",
				"AuthorityExpiresAt",
			},
		},
	}

	for _, testCase := range testCases {
		modelType := reflect.TypeOf(testCase.model)
		for _, fieldName := range testCase.fields {
			field, found := modelType.FieldByName(fieldName)
			if !found {
				t.Fatalf("%s is missing frozen authority field %s", modelType.Name(), fieldName)
			}
			if field.Tag.Get("json") != "-" {
				t.Fatalf("%s.%s must remain private with json:\"-\"", modelType.Name(), fieldName)
			}
		}
	}
}

func TestBackupAssetRecoveryResultCarriesFrozenClassification(t *testing.T) {
	modelType := reflect.TypeOf(BackupAssetRecoveryResult{})
	for _, testCase := range []struct {
		field     string
		gormParts []string
	}{
		{field: "Classification", gormParts: []string{"size:16", "not null"}},
		{field: "ClassificationRevision", gormParts: []string{"not null"}},
		{field: "ClassificationSourceRevision", gormParts: []string{"not null"}},
	} {
		field, found := modelType.FieldByName(testCase.field)
		if !found {
			t.Fatalf("%s is missing frozen classification field %s", modelType.Name(), testCase.field)
		}
		for _, fragment := range testCase.gormParts {
			if !strings.Contains(field.Tag.Get("gorm"), fragment) {
				t.Fatalf("%s.%s gorm tag %q omits %q", modelType.Name(), testCase.field, field.Tag.Get("gorm"), fragment)
			}
		}
		if field.Tag.Get("json") != "-" {
			t.Fatalf("%s.%s must remain private with json:\"-\"", modelType.Name(), testCase.field)
		}
	}
}

func TestBackupAssetRecoveryJobCarriesPrivateWorkspaceCleanupTuple(t *testing.T) {
	modelType := reflect.TypeOf(BackupAssetRecoveryJob{})
	timePointerType := reflect.TypeOf((*time.Time)(nil))
	stringPointerType := reflect.TypeOf((*string)(nil))
	testCases := []struct {
		field     string
		fieldType reflect.Type
		gormParts []string
	}{
		{field: "WorkspaceCleanupPhase", fieldType: reflect.TypeOf(""), gormParts: []string{"size:32", "not null", "default:claimed"}},
		{field: "WorkspaceCleanupOwner", fieldType: reflect.TypeOf(""), gormParts: []string{"size:64", "not null", "default:''"}},
		{field: "WorkspaceCleanupLeaseExpiresAt", fieldType: timePointerType},
		{field: "WorkspaceCleanupFence", fieldType: reflect.TypeOf(uint64(0)), gormParts: []string{"not null", "default:0"}},
		{field: "WorkspaceCleanupNodeLeaseID", fieldType: stringPointerType, gormParts: []string{"size:32"}},
		{field: "WorkspaceCleanupNodeFence", fieldType: reflect.TypeOf(uint64(0)), gormParts: []string{"not null", "default:0"}},
		{field: "WorkspaceCleanupAttempt", fieldType: reflect.TypeOf(uint64(0)), gormParts: []string{"not null", "default:0"}},
	}

	for _, testCase := range testCases {
		field, found := modelType.FieldByName(testCase.field)
		if !found {
			t.Fatalf("%s is missing workspace cleanup field %s", modelType.Name(), testCase.field)
		}
		if field.Type != testCase.fieldType {
			t.Fatalf("%s.%s type = %s, want %s", modelType.Name(), testCase.field, field.Type, testCase.fieldType)
		}
		if field.Tag.Get("json") != "-" {
			t.Fatalf("%s.%s must remain private with json:\"-\"", modelType.Name(), testCase.field)
		}
		for _, fragment := range testCase.gormParts {
			if !strings.Contains(field.Tag.Get("gorm"), fragment) {
				t.Fatalf("%s.%s gorm tag %q omits %q", modelType.Name(), testCase.field, field.Tag.Get("gorm"), fragment)
			}
		}
	}
}

func TestBackupAssetRecoverySensitiveFieldsEncryptAndStayOutOfJSON(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RECOVERY_MODEL_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	testCases := []struct {
		model recoveryEncryptedModel
		field string
	}{
		{model: &BackupAssetRecoveryPlan{}, field: "EncryptedSourceLocator"},
		{model: &BackupAssetRecoveryPlan{}, field: "EncryptedTargetRootLocator"},
		{model: &BackupAssetRecoveryPlan{}, field: "EncryptedTargetRelativePath"},
		{model: &BackupAssetRecoveryPlan{}, field: "EncryptedOverrideReason"},
		{model: &BackupAssetRecoveryGrant{}, field: "EncryptedReason"},
		{model: &BackupAssetRecoveryJob{}, field: "EncryptedWorkspaceRelativeLocator"},
		{model: &BackupAssetRecoveryResult{}, field: "EncryptedRelativeLocator"},
	}

	for _, testCase := range testCases {
		modelType := reflect.TypeOf(testCase.model).Elem()
		t.Run(modelType.Name()+"/"+testCase.field, func(t *testing.T) {
			modelValue := reflect.ValueOf(testCase.model).Elem()
			fieldType, found := modelType.FieldByName(testCase.field)
			if !found {
				t.Fatalf("%s is missing sensitive field %s", modelType.Name(), testCase.field)
			}
			if fieldType.Tag.Get("json") != "-" {
				t.Fatalf("%s.%s must use json:\"-\"", modelType.Name(), testCase.field)
			}
			fieldValue := modelValue.FieldByName(testCase.field)
			if fieldValue.Kind() != reflect.String || !fieldValue.CanSet() {
				t.Fatalf("%s.%s must be a settable encrypted string", modelType.Name(), testCase.field)
			}

			plaintext := "FAKE_RECOVERY_SENSITIVE_VALUE_FOR_TEST_ONLY"
			fieldValue.SetString(plaintext)
			if err := testCase.model.BeforeSave(nil); err != nil {
				t.Fatalf("encrypt %s.%s: %v", modelType.Name(), testCase.field, err)
			}
			ciphertext := fieldValue.String()
			if !strings.HasPrefix(ciphertext, "enc:") || ciphertext == plaintext || strings.Contains(ciphertext, plaintext) {
				t.Fatalf("%s.%s was not encrypted at rest: %q", modelType.Name(), testCase.field, ciphertext)
			}
			if err := testCase.model.AfterFind(nil); err != nil {
				t.Fatalf("decrypt %s.%s: %v", modelType.Name(), testCase.field, err)
			}
			if got := fieldValue.String(); got != plaintext {
				t.Fatalf("%s.%s plaintext=%q want=%q", modelType.Name(), testCase.field, got, plaintext)
			}
		})
	}
}

// Post-GREEN regression coverage: the original Task 1 model RED was not observed.
func TestRecoveryReviewF8LocatorCiphertextAtRest(t *testing.T) {
	TestBackupAssetRecoverySensitiveFieldsEncryptAndStayOutOfJSON(t)
}

type recoveryEncryptedModel interface {
	BeforeSave(*gorm.DB) error
	AfterFind(*gorm.DB) error
}
