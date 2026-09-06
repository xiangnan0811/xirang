package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"runtime"
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
	"gorm.io/gorm/clause"
)

type retentionDiagnosticExposure string

const (
	retentionDiagnosticWhole        retentionDiagnosticExposure = "whole"
	retentionDiagnosticPrivateField retentionDiagnosticExposure = "private_field"
	retentionDiagnosticSafeString   retentionDiagnosticExposure = "safe_string"
)

type retentionDiagnosticBinding struct {
	typeName string
	exposure retentionDiagnosticExposure
}

type retentionDiagnosticAnalysis struct {
	foundFunctions map[string]bool
	classified     map[string]int
	formatCalls    int
	violations     []string
}

func TestLifecycleCoordinatorPrivateDiagnosticsUseClosedFields(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate retention coordinator test source")
	}
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, filename, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse retention coordinator test source: %v", err)
	}
	targets := map[string]bool{
		"TestLifecycleClaimMovesCommittedPointToExpiringWithLeaseFence": true,
		"TestLifecycleDependentCleanupDoesNotDrainRecoveryResultLease":  true,
	}
	analysis := analyzeRetentionPrivateDiagnostics(parsed, files, targets)
	for target := range targets {
		if !analysis.foundFunctions[target] {
			t.Fatalf("retention private diagnostic target %s was not found", target)
		}
	}
	for _, privateType := range []string{
		"LifecycleAttempt", "RecoveryPointLease", "BackupAssetDeliveryGrant", "BackupAssetDeliveryRequest",
	} {
		if analysis.classified[privateType] == 0 {
			t.Fatalf("retention private diagnostic guard classified no %s binding", privateType)
		}
	}
	if analysis.formatCalls == 0 {
		t.Fatal("retention private diagnostic guard audited no formatting calls")
	}
	if len(analysis.violations) != 0 {
		t.Errorf("unsafe retention private diagnostics at %s; use closed IDs/state/revision/presence/equality fields",
			strings.Join(analysis.violations, ", "))
	}

	const mutationSource = `package retention

func loadRetentionLease() model.RecoveryPointLease { return model.RecoveryPointLease{} }
func loadRetentionGrant() model.BackupAssetDeliveryGrant { return model.BackupAssetDeliveryGrant{} }
func loadRetentionAttempt() LifecycleAttempt { return LifecycleAttempt{} }
func retentionIdentity[T any](value T) T { return value }
func retentionPairIdentity[T, U any](value T, _ U) T { return value }
func loadRetentionLeaseSecond() (string, model.RecoveryPointLease) { return "", model.RecoveryPointLease{} }
func loadRetentionRequestAndGrant() (model.BackupAssetDeliveryRequest, string, model.BackupAssetDeliveryGrant) { return model.BackupAssetDeliveryRequest{}, "", model.BackupAssetDeliveryGrant{} }

func retentionPrivacyCanary(t *testing.T, leases []model.RecoveryPointLease, grants []model.BackupAssetDeliveryGrant, requests []model.BackupAssetDeliveryRequest, attempts []LifecycleAttempt, unknownFormat string) {
	leaseAlias := leases[0]
	grantAlias := grants[0]
	format := "%v"
	t.Errorf("lease alias=%v", leaseAlias)
	t.Fatalf("grant selector=%+v", grantAlias.CookieSecretHash)
	t.Logf("lease index=%[1]v", leases[0])
	fmt.Printf("grant call=%#v", loadRetentionGrant())
	_ = fmt.Sprintf("lease call selector=%v", loadRetentionLease().FenceToken)
	_ = fmt.Errorf(format, grantAlias)
	t.Skipf(format, leaseAlias)
	_ = fmt.Appendf(nil, "grant alias=%v", grantAlias)
	t.Errorf("attempt private selector=%v", attempts[0].LeaseFenceTokenHash)
	t.Errorf("attempt safe string=%+v", loadRetentionAttempt())
	leakedLease := retentionIdentity(leaseAlias)
	erasedLease := []any{leaseAlias}[0]
	genericLease := retentionIdentity[model.RecoveryPointLease](leaseAlias)
	genericGrant := retentionPairIdentity[model.BackupAssetDeliveryGrant, string](grantAlias, "")
	erasedGrant := []any{retentionIdentity(grantAlias)}[0]
	emit := t.Errorf
	emit("identity alias=%v", leakedLease)
	emit("container index=%v", erasedLease)
	emit = t.Fatalf
	emit("generic index=%v", genericLease)
	emit("generic index list=%v", genericGrant)
	emit("composite call index=%v", erasedGrant)
	emit(unknownFormat, leakedLease)
	emit = t.Logf
	emit(unknownFormat, genericGrant)
	binaryToken := "prefix:" + leaseAlias.FenceToken
	anyLease := any(leaseAlias)
	convertedToken := string(grantAlias.CookieSecretHash)
	emit("binary token=%s", binaryToken)
	emit("any lease=%q", anyLease)
	emit("converted token=%#x", convertedToken)
	emit("explicit flagged safe=%[1]T private=%#[2]x", "safe", genericGrant)
	emit("flagged lease=%+s", leakedLease)
	emit("private width=%*s", erasedLease, "safe")
	emit("private precision=%.*s", erasedGrant, "safe")
	emit("types lease=%T grant=%T percent=%%", leaseAlias, grantAlias)
	emit("safe state=%q version=%d", grantAlias.State, grantAlias.Version)
	requestAlias := requests[0]
	emit("grant id selector=%s", grantAlias.ID)
	emit("request id selectors=%q/%x", requestAlias.ID, requestAlias.GrantID)
	declaredSafe, declaredLease := loadRetentionLeaseSecond()
	var valueSafe, valueLease = loadRetentionLeaseSecond()
	typedRequest, typedSafe, typedGrant := loadRetentionRequestAndGrant()
	var assignedRequest, assignedSafe, assignedGrant any
	assignedRequest, assignedSafe, assignedGrant = loadRetentionRequestAndGrant()
	_ = declaredSafe
	_ = valueSafe
	_ = typedSafe
	_ = assignedSafe
	requestResultAlias := typedRequest
	grantResultAlias := assignedGrant
	emit("typed declaration lease=%v", declaredLease)
	emit("typed value declaration lease=%v", valueLease)
	emit("typed returns request=%v grant=%v", requestResultAlias, typedGrant)
	emit("typed assignment request=%v grant=%v", assignedRequest, grantResultAlias)
	twiceGrant := retentionIdentityOuter(grantAlias)
	twiceRequest := retentionIdentityOuter(requestAlias)
	emitLayer := emit
	emitOuter := emitLayer
	keyedFence := map[any]string{leaseAlias.FenceToken: "safe"}
	keyedHash := map[any]string{grantAlias.LeaseFenceTokenHash: "safe"}
	keyedTicket := map[any]string{requestAlias.GrantID: "safe"}
	emitOuter("two helper grant=%v request=%v", twiceGrant, twiceRequest)
	emitOuter("keyed fence=%v hash=%v ticket=%v", keyedFence, keyedHash, keyedTicket)
	emitOuter("attempt type=%T", attempts[0], grantAlias)
	emitOuter("percent only=%%", leaseAlias)
	emitOuter("request type=%T", requestAlias, grantAlias, leaseAlias)
	emitOuter("star type=%*T", requestAlias, grantAlias)
	emitOuter("precision type=%.*T", leaseAlias, requestAlias)
	retentionVoidOuter(t.Errorf, grantAlias)
	_, relayedMap := retentionMapRelayOuter(leaseAlias.FenceToken)
	emitOuter("relayed map=%v", relayedMap)
	emitOuter("reordered private safe=%[2]T", grantAlias, requestAlias)
	safeAttemptAlias := retentionIdentity(loadRetentionAttempt())
	safeAttemptErased := []any{safeAttemptAlias}[0]
	emit("safe attempt alias=%+v erased=%+v", safeAttemptAlias, safeAttemptErased)
	outerEmit := t.Errorf
	{
		outerEmit := t.Logf
		outerEmit("shadowed formatter lease=%v", leaseAlias)
	}
	outerEmit("outer formatter grant=%v", grantAlias)
	joinedResult := retentionBranchRelay(false, grantAlias.State, any(grantAlias))
	emit("joined branch result=%v", joinedResult)
	for rangedGrant, rangedLease := range map[model.BackupAssetDeliveryGrant]model.RecoveryPointLease{grantAlias: leaseAlias} {
		emit("range key=%v value=%v", rangedGrant, rangedLease)
	}
	for _, rangedAttempt := range []LifecycleAttempt{attempts[0]} {
		emit("range attempt safe=%+v", rangedAttempt)
		emit("range attempt private=%v", rangedAttempt.LeaseFenceTokenHash)
	}
	ifEmit := t.Errorf
	if false {
		ifEmit = func(string, ...any) {}
	}
	ifEmit("if branch private=%v", leaseAlias)
	loopEmit := t.Fatalf
	for false {
		loopEmit = func(string, ...any) {}
	}
	loopEmit("loop branch private=%v", grantAlias)
	switchEmit := t.Logf
	switch 0 {
	case 1:
		switchEmit = func(string, ...any) {}
	}
	switchEmit("switch branch private=%v", requestAlias)
	ifFormat := "%v"
	if false {
		ifFormat = "%T"
	}
	t.Errorf(ifFormat, leaseAlias)
	loopFormat := "%v"
	for false {
		loopFormat = "%T"
	}
	t.Fatalf(loopFormat, grantAlias)
	switchFormat := "%v"
	switch 0 {
	case 1:
		switchFormat = "%T"
	}
	t.Logf(switchFormat, requestAlias)
	safeFormat := "%T"
	if false {
		safeFormat = "%[1]T"
	}
	t.Errorf(safeFormat, leaseAlias)
	safeFormatter := t.Errorf
	if false {
		safeFormatter = t.Logf
	}
	safeFormatter("definitely safe type=%T", grantAlias)
}

func retentionIdentityLayer[T any](value T) T { return retentionIdentity(value) }
func retentionIdentityOuter[T any](value T) T { return retentionIdentityLayer(value) }
func retentionVoidSink[T any](emit func(string, ...any), value T) { emit("void private=%v", value) }
func retentionVoidLayer[T any](emit func(string, ...any), value T) { retentionVoidSink(emit, value) }
func retentionVoidOuter[T any](emit func(string, ...any), value T) { retentionVoidLayer(emit, value) }
func retentionMapPair[T any](value T) (int, any) { return 0, map[any]any{value: value} }
func retentionMapRelay[T any](value T) (int, any) { return retentionMapPair(value) }
func retentionMapRelayOuter[T any](value T) (int, any) { return retentionMapRelay(value) }
func retentionBranchJoin(useSafe bool, safe, private any) any {
	if useSafe { return safe }
	return private
}
func retentionBranchRelay(useSafe bool, safe, private any) any {
	return retentionBranchJoin(useSafe, safe, private)
}`
	mutationFiles := token.NewFileSet()
	mutation, err := parser.ParseFile(mutationFiles, "retention_private_diagnostic_canary.go", mutationSource, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse retention private diagnostic mutation canary: %v", err)
	}
	mutationAnalysis := analyzeRetentionPrivateDiagnostics(
		mutation,
		mutationFiles,
		map[string]bool{"retentionPrivacyCanary": true},
	)
	wantMutationViolations := []string{
		"15:Errorf:RecoveryPointLease:whole",
		"16:Fatalf:BackupAssetDeliveryGrant:private_field",
		"17:Logf:RecoveryPointLease:whole",
		"18:Printf:BackupAssetDeliveryGrant:whole",
		"19:Sprintf:RecoveryPointLease:private_field",
		"20:Errorf:BackupAssetDeliveryGrant:whole",
		"21:Skipf:RecoveryPointLease:whole",
		"22:Appendf:BackupAssetDeliveryGrant:whole",
		"23:Errorf:LifecycleAttempt:private_field",
		"31:Errorf:RecoveryPointLease:whole",
		"32:Errorf:RecoveryPointLease:whole",
		"34:Fatalf:RecoveryPointLease:whole",
		"35:Fatalf:BackupAssetDeliveryGrant:whole",
		"36:Fatalf:BackupAssetDeliveryGrant:whole",
		"37:Fatalf:RecoveryPointLease:whole:dynamic",
		"39:Logf:BackupAssetDeliveryGrant:whole:dynamic",
		"43:Logf:RecoveryPointLease:private_field",
		"44:Logf:RecoveryPointLease:whole",
		"45:Logf:BackupAssetDeliveryGrant:private_field",
		"46:Logf:BackupAssetDeliveryGrant:whole",
		"47:Logf:RecoveryPointLease:whole",
		"48:Logf:RecoveryPointLease:whole",
		"49:Logf:BackupAssetDeliveryGrant:whole",
		"53:Logf:BackupAssetDeliveryGrant:private_field",
		"54:Logf:BackupAssetDeliveryRequest:private_field",
		"54:Logf:BackupAssetDeliveryRequest:private_field",
		"66:Logf:RecoveryPointLease:whole",
		"67:Logf:RecoveryPointLease:whole",
		"68:Logf:BackupAssetDeliveryRequest:whole",
		"68:Logf:BackupAssetDeliveryGrant:whole",
		"69:Logf:BackupAssetDeliveryRequest:whole",
		"69:Logf:BackupAssetDeliveryGrant:whole",
		"77:Logf:BackupAssetDeliveryGrant:whole",
		"77:Logf:BackupAssetDeliveryRequest:whole",
		"78:Logf:RecoveryPointLease:private_field",
		"78:Logf:BackupAssetDeliveryGrant:private_field",
		"78:Logf:BackupAssetDeliveryRequest:private_field",
		"79:Logf:BackupAssetDeliveryGrant:whole",
		"80:Logf:RecoveryPointLease:whole",
		"81:Logf:BackupAssetDeliveryGrant:whole",
		"81:Logf:RecoveryPointLease:whole",
		"82:Logf:BackupAssetDeliveryRequest:whole",
		"83:Logf:RecoveryPointLease:whole",
		"84:Errorf:BackupAssetDeliveryGrant:whole",
		"86:Logf:RecoveryPointLease:private_field",
		"94:Logf:RecoveryPointLease:whole",
		"96:Errorf:BackupAssetDeliveryGrant:whole",
		"98:Logf:BackupAssetDeliveryGrant:whole",
		"100:Logf:BackupAssetDeliveryGrant:whole",
		"100:Logf:RecoveryPointLease:whole",
		"104:Logf:LifecycleAttempt:private_field",
		"110:Errorf:RecoveryPointLease:whole",
		"115:Fatalf:BackupAssetDeliveryGrant:whole",
		"121:Logf:BackupAssetDeliveryRequest:whole",
		"126:Errorf:RecoveryPointLease:whole",
		"131:Fatalf:BackupAssetDeliveryGrant:whole",
		"137:Logf:BackupAssetDeliveryRequest:whole",
	}
	if got, want := strings.Join(mutationAnalysis.violations, "\n"), strings.Join(wantMutationViolations, "\n"); got != want {
		t.Fatalf("retention private diagnostic mutation coverage got=%q want=%q", got, want)
	}
}

func TestProviderDeletePrivateTypesRedactDiagnostics(t *testing.T) {
	const (
		rawLocator       = "RAW_NATIVE_LOCATOR_CANARY"
		rawConfig        = "RAW_CONFIG_CANARY"
		rawSecret        = "RAW_SECRET_CANARY"
		rawSalt          = "RAW_SALT_CANARY"
		rawPassword      = "RAW_PASSWORD_CANARY"
		rawPrivateKey    = "RAW_PRIVATE_KEY_CANARY"
		rawClient        = "RAW_CLIENT_CANARY"
		rawNative        = "RAW_NATIVE_MATERIAL_CANARY"
		rawRemoteCommand = "RAW_REMOTE_COMMAND_CANARY"
		rawFence         = "RAW_FENCE_CANARY"
	)
	access := provider.AccessBinding{
		IdentitySalt:  []byte(rawSalt),
		EndpointFacts: []string{rawClient},
		Locator:       rawLocator,
		Secret:        []byte(rawSecret),
		Config:        []byte(rawConfig),
		AdapterData: map[string]any{
			"password":    rawPassword,
			"private_key": rawPrivateKey,
			"client":      rawClient,
			"native":      rawNative,
			"command":     rawRemoteCommand,
		},
	}
	request := provider.DeletePointRequest{
		Snapshot: provider.ReadSnapshot{
			RepositoryID:       testOpaqueID(8700),
			CapabilityRevision: 1,
			SourceRevision:     rawNative,
			RepositoryIdentity: rawClient,
			Access:             access,
		},
		Point:                  provider.PointLocator{Native: rawLocator},
		ExpectedSourceRevision: rawNative,
		OperationID:            testOpaqueID(8701),
	}
	prepared := PreparedPointDeletion{
		request:        request,
		identity:       provider.DeletionTargetIdentityInput{RepositoryIdentity: rawClient, Request: request},
		identityDigest: rawFence,
	}
	rawLeaseID, rawAttemptID, rawFenceHash := rawLocator, rawNative, rawFence
	repositoryIdentity := rawClient
	rows := LifecycleDeleteRows{
		attempt: model.RecoveryPointLifecycleAttempt{
			ID:                  rawAttemptID,
			RecoveryPointID:     rawLeaseID,
			LeaseID:             &rawLeaseID,
			LeaseAttemptID:      &rawAttemptID,
			LeaseFenceTokenHash: &rawFenceHash,
		},
		point: model.RecoveryPoint{
			ID:                       rawLeaseID,
			RepositoryID:             rawLeaseID,
			EncryptedProviderLocator: rawLocator,
			EncryptedRollbackLocator: rawNative,
			CapabilitiesJSON:         rawConfig,
		},
		lease: model.RecoveryPointLease{
			ID:              rawLeaseID,
			RecoveryPointID: rawLeaseID,
			AttemptID:       rawAttemptID,
			FenceToken:      rawFence,
			OwnerID:         rawClient,
		},
		repository: model.BackupRepository{
			ID:                 rawLeaseID,
			RepositoryIdentity: &repositoryIdentity,
			CapabilitiesJSON:   rawConfig,
		},
	}
	markers := []string{
		rawLocator, rawConfig, rawSecret, rawSalt, rawPassword,
		rawPrivateKey, rawClient, rawNative, rawRemoteCommand, rawFence,
	}
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{name: "prepared value", value: prepared, want: "[prepared provider deletion]"},
		{name: "prepared pointer", value: &prepared, want: "[prepared provider deletion]"},
		{name: "rows value", value: rows, want: "[lifecycle provider-delete rows]"},
		{name: "rows pointer", value: &rows, want: "[lifecycle provider-delete rows]"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for _, format := range []string{"%+v", "%#v"} {
				rendered := fmt.Sprintf(format, testCase.value)
				if rendered != testCase.want {
					t.Fatalf("format %s rendered %q, want closed redaction %q", format, rendered, testCase.want)
				}
				for _, marker := range markers {
					if strings.Contains(rendered, marker) {
						t.Fatalf("format %s leaked private marker %q: %s", format, marker, rendered)
					}
				}
			}
			renderedJSON, err := json.Marshal(testCase.value)
			if err != nil {
				t.Fatalf("marshal %s diagnostics value: %v", testCase.name, err)
			}
			for _, marker := range markers {
				if strings.Contains(string(renderedJSON), marker) {
					t.Fatalf("json leaked private marker %q: %s", marker, renderedJSON)
				}
			}
		})
	}
}

func analyzeRetentionPrivateDiagnostics(
	parsed *ast.File,
	files *token.FileSet,
	targets map[string]bool,
) retentionDiagnosticAnalysis {
	privateTypes := map[string]bool{
		"LifecycleAttempt":           true,
		"RecoveryPointLease":         true,
		"BackupAssetDeliveryGrant":   true,
		"BackupAssetDeliveryRequest": true,
	}
	safeFields := map[string]map[string]bool{
		"LifecycleAttempt": {
			"ID": true, "RecoveryPointID": true, "Operation": true, "Phase": true,
			"TransitionRevision": true, "BlockedReason": true,
		},
		"RecoveryPointLease": {
			"ID": true, "RecoveryPointID": true, "HolderType": true, "Status": true,
		},
		"BackupAssetDeliveryGrant": {
			"ResourceKind": true, "State": true, "InFlight": true, "Version": true,
		},
		"BackupAssetDeliveryRequest": {
			"State": true, "Version": true,
		},
	}
	wholeBinding := func(typeName string) retentionDiagnosticBinding {
		if typeName == "LifecycleAttempt" {
			return retentionDiagnosticBinding{typeName: typeName, exposure: retentionDiagnosticSafeString}
		}
		return retentionDiagnosticBinding{typeName: typeName, exposure: retentionDiagnosticWhole}
	}
	privateTypeName := func(expression ast.Expr) string {
		for expression != nil {
			switch value := expression.(type) {
			case *ast.ArrayType:
				expression = value.Elt
			case *ast.StarExpr:
				expression = value.X
			case *ast.SelectorExpr:
				if privateTypes[value.Sel.Name] {
					return value.Sel.Name
				}
				return ""
			case *ast.Ident:
				if privateTypes[value.Name] {
					return value.Name
				}
				return ""
			default:
				return ""
			}
		}
		return ""
	}
	callableName := func(expression ast.Expr) string {
		for expression != nil {
			switch value := expression.(type) {
			case *ast.Ident:
				return value.Name
			case *ast.SelectorExpr:
				return value.Sel.Name
			case *ast.IndexExpr:
				expression = value.X
			case *ast.IndexListExpr:
				expression = value.X
			case *ast.ParenExpr:
				expression = value.X
			default:
				return ""
			}
		}
		return ""
	}

	functionResults := make(map[string][]string)
	functionParameters := make(map[string]map[string]int)
	functionBodies := make(map[string]*ast.BlockStmt)
	type resultProvenanceCandidate struct {
		name             string
		parameterIndexes map[string]int
		resultCount      int
		returns          [][]ast.Expr
	}
	var resultProvenanceCandidates []resultProvenanceCandidate
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Type.Results != nil {
			var results []string
			for _, result := range function.Type.Results.List {
				count := len(result.Names)
				if count == 0 {
					count = 1
				}
				for range count {
					results = append(results, privateTypeName(result.Type))
				}
			}
			functionResults[function.Name.Name] = results
		}
		parameterIndexes := make(map[string]int)
		parameterIndex := 0
		if function.Type.Params != nil {
			for _, parameter := range function.Type.Params.List {
				for _, name := range parameter.Names {
					parameterIndexes[name.Name] = parameterIndex
					parameterIndex++
				}
			}
		}
		functionParameters[function.Name.Name] = parameterIndexes
		if function.Body == nil {
			continue
		}
		functionBodies[function.Name.Name] = function.Body
		var returnedExpressions [][]ast.Expr
		ast.Inspect(function.Body, func(node ast.Node) bool {
			statement, ok := node.(*ast.ReturnStmt)
			if !ok || len(statement.Results) == 0 {
				return true
			}
			returnedExpressions = append(returnedExpressions, statement.Results)
			return false
		})
		if returnedExpressions != nil {
			resultProvenanceCandidates = append(resultProvenanceCandidates, resultProvenanceCandidate{
				name: function.Name.Name, parameterIndexes: parameterIndexes,
				resultCount: len(functionResults[function.Name.Name]), returns: returnedExpressions,
			})
		}
	}
	functionResultParameters := make(map[string]map[int]map[int]bool)
	unionParameterIndexes := func(target map[int]bool, source map[int]bool) bool {
		changed := false
		for index := range source {
			if !target[index] {
				target[index] = true
				changed = true
			}
		}
		return changed
	}
	var resultExpressionParameters func(ast.Expr, map[string]int) map[int]bool
	callResultParameters := func(call *ast.CallExpr, resultIndex int, parameters map[string]int) map[int]bool {
		result := make(map[int]bool)
		calleeResults := functionResultParameters[callableName(call.Fun)]
		for calleeParameter := range calleeResults[resultIndex] {
			if calleeParameter < len(call.Args) {
				unionParameterIndexes(result, resultExpressionParameters(call.Args[calleeParameter], parameters))
			}
		}
		return result
	}
	resultExpressionParameters = func(expression ast.Expr, parameters map[string]int) map[int]bool {
		result := make(map[int]bool)
		switch value := expression.(type) {
		case *ast.Ident:
			if index, ok := parameters[value.Name]; ok {
				result[index] = true
			}
		case *ast.ParenExpr:
			return resultExpressionParameters(value.X, parameters)
		case *ast.StarExpr:
			return resultExpressionParameters(value.X, parameters)
		case *ast.UnaryExpr:
			return resultExpressionParameters(value.X, parameters)
		case *ast.BinaryExpr:
			unionParameterIndexes(result, resultExpressionParameters(value.X, parameters))
			unionParameterIndexes(result, resultExpressionParameters(value.Y, parameters))
		case *ast.CompositeLit:
			for _, element := range value.Elts {
				if keyed, ok := element.(*ast.KeyValueExpr); ok {
					unionParameterIndexes(result, resultExpressionParameters(keyed.Key, parameters))
					element = keyed.Value
				}
				unionParameterIndexes(result, resultExpressionParameters(element, parameters))
			}
		case *ast.CallExpr:
			return callResultParameters(value, 0, parameters)
		}
		return result
	}
	resultExpressionParameter := func(expression ast.Expr, parameters map[string]int) (int, bool) {
		indexes := resultExpressionParameters(expression, parameters)
		selected, found := 0, false
		for index := range indexes {
			if !found || index < selected {
				selected, found = index, true
			}
		}
		return selected, found
	}
	for changed := true; changed; {
		changed = false
		for _, candidate := range resultProvenanceCandidates {
			if functionResultParameters[candidate.name] == nil {
				functionResultParameters[candidate.name] = make(map[int]map[int]bool)
			}
			for resultIndex := 0; resultIndex < candidate.resultCount; resultIndex++ {
				if functionResultParameters[candidate.name][resultIndex] == nil {
					functionResultParameters[candidate.name][resultIndex] = make(map[int]bool)
				}
				for _, returned := range candidate.returns {
					var indexes map[int]bool
					if len(returned) == 1 {
						if call, isCall := returned[0].(*ast.CallExpr); isCall && candidate.resultCount > 1 {
							indexes = callResultParameters(call, resultIndex, candidate.parameterIndexes)
						} else if resultIndex == 0 {
							indexes = resultExpressionParameters(returned[0], candidate.parameterIndexes)
						}
					} else if resultIndex < len(returned) {
						indexes = resultExpressionParameters(returned[resultIndex], candidate.parameterIndexes)
					}
					if unionParameterIndexes(functionResultParameters[candidate.name][resultIndex], indexes) {
						changed = true
					}
				}
			}
		}
	}
	analysis := retentionDiagnosticAnalysis{
		foundFunctions: make(map[string]bool),
		classified:     make(map[string]int),
	}
	formatters := map[string]int{
		"Errorf": 0, "Fatalf": 0, "Logf": 0, "Skipf": 0,
		"Printf": 0, "Sprintf": 0, "Appendf": 1, "Fprintf": 1,
	}
	type formatterBinding struct {
		name        string
		formatIndex int
		valid       bool
	}
	type formatterAssignment struct {
		identifier     string
		objectPosition token.Pos
		binding        formatterBinding
		position       token.Pos
		conditional    bool
	}
	type formatState struct {
		literals map[string]bool
		unknown  bool
		assigned bool
	}
	type formatAssignment struct {
		identifier     string
		objectPosition token.Pos
		state          formatState
		position       token.Pos
		conditional    bool
	}
	privateFormatArguments := func(format string) (map[int]bool, map[int]bool, bool, bool) {
		parseIndex := func(offset int) (int, int, bool) {
			if offset >= len(format) || format[offset] != '[' {
				return 0, offset, false
			}
			cursor := offset + 1
			index := 0
			start := cursor
			for cursor < len(format) && format[cursor] >= '0' && format[cursor] <= '9' {
				index = index*10 + int(format[cursor]-'0')
				cursor++
			}
			if cursor == start || cursor >= len(format) || format[cursor] != ']' || index == 0 {
				return 0, offset, false
			}
			return index - 1, cursor + 1, true
		}
		parseFlags := func(offset int) int {
			for offset < len(format) && strings.ContainsRune("+#- 0", rune(format[offset])) {
				offset++
			}
			return offset
		}
		unsafeArguments := make(map[int]bool)
		consumedArguments := make(map[int]bool)
		reordered := false
		nextArgument := 0
		for offset := 0; offset < len(format); {
			if format[offset] != '%' {
				offset++
				continue
			}
			offset++
			if offset >= len(format) {
				return nil, nil, false, false
			}
			if format[offset] == '%' {
				offset++
				continue
			}

			offset = parseFlags(offset)
			valueArgument := -1
			if index, next, ok := parseIndex(offset); ok {
				reordered = true
				valueArgument = index
				nextArgument = index
				offset = parseFlags(next)
			} else if offset < len(format) && format[offset] == '[' {
				return nil, nil, false, false
			}

			if offset < len(format) && format[offset] == '*' {
				widthArgument := nextArgument
				if valueArgument >= 0 {
					widthArgument = valueArgument
				}
				consumedArguments[widthArgument] = true
				unsafeArguments[widthArgument] = true
				nextArgument = widthArgument + 1
				valueArgument = -1
				offset++
			} else {
				for offset < len(format) && format[offset] >= '0' && format[offset] <= '9' {
					offset++
				}
			}

			if offset < len(format) && format[offset] == '.' {
				offset++
				precisionArgument := -1
				if index, next, ok := parseIndex(offset); ok {
					reordered = true
					precisionArgument = index
					nextArgument = index
					offset = next
				} else if offset < len(format) && format[offset] == '[' {
					return nil, nil, false, false
				}
				if offset < len(format) && format[offset] == '*' {
					if precisionArgument < 0 {
						precisionArgument = nextArgument
					}
					consumedArguments[precisionArgument] = true
					unsafeArguments[precisionArgument] = true
					nextArgument = precisionArgument + 1
					offset++
				} else {
					for offset < len(format) && format[offset] >= '0' && format[offset] <= '9' {
						offset++
					}
					if precisionArgument >= 0 {
						valueArgument = precisionArgument
					}
				}
			}

			if index, next, ok := parseIndex(offset); ok {
				reordered = true
				valueArgument = index
				nextArgument = index
				offset = parseFlags(next)
			} else if offset < len(format) && format[offset] == '[' {
				return nil, nil, false, false
			}
			if offset >= len(format) {
				return nil, nil, false, false
			}
			verb := format[offset]
			offset++
			if verb == '%' {
				continue
			}
			if valueArgument < 0 {
				valueArgument = nextArgument
			}
			nextArgument = valueArgument + 1
			consumedArguments[valueArgument] = true
			if verb != 'T' {
				unsafeArguments[valueArgument] = true
			}
		}
		return unsafeArguments, consumedArguments, reordered, true
	}
	type sinkParameterSummary struct {
		formatterParameter int
		valueParameter     int
	}
	sinkParameterSummaries := make(map[string][]sinkParameterSummary)
	addSinkParameterSummary := func(name string, summary sinkParameterSummary) bool {
		for _, existing := range sinkParameterSummaries[name] {
			if existing == summary {
				return false
			}
		}
		sinkParameterSummaries[name] = append(sinkParameterSummaries[name], summary)
		return true
	}
	for changed := true; changed; {
		changed = false
		for name, body := range functionBodies {
			parameters := functionParameters[name]
			ast.Inspect(body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if formatterName, ok := call.Fun.(*ast.Ident); ok {
					formatterParameter, isParameter := parameters[formatterName.Name]
					if isParameter && len(call.Args) >= 2 {
						literal, isLiteral := call.Args[0].(*ast.BasicLit)
						if isLiteral && literal.Kind == token.STRING {
							format, err := strconv.Unquote(literal.Value)
							if err == nil {
								unsafeArguments, consumedArguments, reordered, known := privateFormatArguments(format)
								for argumentIndex, argument := range call.Args[1:] {
									valueParameter, isValueParameter := resultExpressionParameter(argument, parameters)
									if !isValueParameter || known && consumedArguments[argumentIndex] && !unsafeArguments[argumentIndex] ||
										known && reordered && !consumedArguments[argumentIndex] {
										continue
									}
									if addSinkParameterSummary(name, sinkParameterSummary{
										formatterParameter: formatterParameter, valueParameter: valueParameter,
									}) {
										changed = true
									}
								}
							}
						}
					}
				}
				for _, calleeSummary := range sinkParameterSummaries[callableName(call.Fun)] {
					if calleeSummary.formatterParameter >= len(call.Args) || calleeSummary.valueParameter >= len(call.Args) {
						continue
					}
					formatterParameter, formatterOK := resultExpressionParameter(
						call.Args[calleeSummary.formatterParameter], parameters,
					)
					valueParameter, valueOK := resultExpressionParameter(
						call.Args[calleeSummary.valueParameter], parameters,
					)
					if formatterOK && valueOK && addSinkParameterSummary(name, sinkParameterSummary{
						formatterParameter: formatterParameter, valueParameter: valueParameter,
					}) {
						changed = true
					}
				}
				return true
			})
		}
	}

	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil || !targets[function.Name.Name] {
			continue
		}
		analysis.foundFunctions[function.Name.Name] = true
		type diagnosticBindingKey struct {
			name           string
			declarationPos token.Pos
		}
		diagnosticKey := func(identifier *ast.Ident) diagnosticBindingKey {
			key := diagnosticBindingKey{name: identifier.Name}
			if identifier.Obj != nil {
				key.declarationPos = identifier.Obj.Pos()
			}
			return key
		}
		bindings := make(map[diagnosticBindingKey]retentionDiagnosticBinding)
		rangeTypes := make(map[diagnosticBindingKey]ast.Expr)
		if function.Type.Params != nil {
			for _, parameter := range function.Type.Params.List {
				typeName := privateTypeName(parameter.Type)
				for _, name := range parameter.Names {
					key := diagnosticKey(name)
					if typeName != "" {
						bindings[key] = wholeBinding(typeName)
					}
					rangeTypes[key] = parameter.Type
				}
			}
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			specification, ok := node.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for _, name := range specification.Names {
				key := diagnosticKey(name)
				if typeName := privateTypeName(specification.Type); typeName != "" {
					bindings[key] = wholeBinding(typeName)
				}
				if specification.Type != nil {
					rangeTypes[key] = specification.Type
				}
			}
			return true
		})

		var expressionBinding func(ast.Expr) retentionDiagnosticBinding
		bindingFromParameterIndexes := func(arguments []ast.Expr, indexes map[int]bool) retentionDiagnosticBinding {
			joined := retentionDiagnosticBinding{}
			for index, argument := range arguments {
				if !indexes[index] {
					continue
				}
				binding := expressionBinding(argument)
				if binding.exposure == retentionDiagnosticWhole || binding.exposure == retentionDiagnosticPrivateField {
					return binding
				}
				if joined.typeName == "" && binding.typeName != "" {
					joined = binding
				}
			}
			return joined
		}
		builtinConversion := map[string]bool{
			"any": true, "string": true, "bool": true, "byte": true, "rune": true,
			"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
			"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
			"float32": true, "float64": true, "complex64": true, "complex128": true,
		}
		var isTypeConversion func(ast.Expr) bool
		isTypeConversion = func(expression ast.Expr) bool {
			switch value := expression.(type) {
			case *ast.Ident:
				return builtinConversion[value.Name]
			case *ast.ArrayType, *ast.MapType, *ast.InterfaceType, *ast.StructType, *ast.ChanType, *ast.FuncType:
				return true
			case *ast.StarExpr:
				return isTypeConversion(value.X)
			case *ast.ParenExpr:
				return isTypeConversion(value.X)
			default:
				return false
			}
		}
		expressionBinding = func(expression ast.Expr) retentionDiagnosticBinding {
			switch value := expression.(type) {
			case *ast.Ident:
				return bindings[diagnosticKey(value)]
			case *ast.ParenExpr:
				return expressionBinding(value.X)
			case *ast.StarExpr:
				return expressionBinding(value.X)
			case *ast.UnaryExpr:
				return expressionBinding(value.X)
			case *ast.BinaryExpr:
				switch value.Op {
				case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ, token.LAND, token.LOR:
					return retentionDiagnosticBinding{}
				}
				if binding := expressionBinding(value.X); binding.typeName != "" {
					return binding
				}
				return expressionBinding(value.Y)
			case *ast.SelectorExpr:
				root := expressionBinding(value.X)
				if root.typeName == "" {
					return retentionDiagnosticBinding{}
				}
				if safeFields[root.typeName][value.Sel.Name] {
					return retentionDiagnosticBinding{}
				}
				return retentionDiagnosticBinding{typeName: root.typeName, exposure: retentionDiagnosticPrivateField}
			case *ast.IndexExpr:
				return expressionBinding(value.X)
			case *ast.IndexListExpr:
				return expressionBinding(value.X)
			case *ast.SliceExpr:
				return expressionBinding(value.X)
			case *ast.TypeAssertExpr:
				return expressionBinding(value.X)
			case *ast.CompositeLit:
				if typeName := privateTypeName(value.Type); typeName != "" {
					return wholeBinding(typeName)
				}
				for _, element := range value.Elts {
					expression := element
					if keyed, ok := element.(*ast.KeyValueExpr); ok {
						if binding := expressionBinding(keyed.Key); binding.typeName != "" {
							return binding
						}
						expression = keyed.Value
					}
					if binding := expressionBinding(expression); binding.typeName != "" {
						return binding
					}
				}
			case *ast.CallExpr:
				if typeName := privateTypeName(value.Fun); typeName != "" {
					return wholeBinding(typeName)
				}
				if len(value.Args) == 1 && isTypeConversion(value.Fun) {
					return expressionBinding(value.Args[0])
				}
				name := callableName(value.Fun)
				if results := functionResults[name]; len(results) != 0 && results[0] != "" {
					return wholeBinding(results[0])
				}
				if binding := bindingFromParameterIndexes(value.Args, functionResultParameters[name][0]); binding.typeName != "" {
					return binding
				}
				if name == "Claim" || name == "Advance" {
					return wholeBinding("LifecycleAttempt")
				}
			}
			return retentionDiagnosticBinding{}
		}
		assignmentBinding := func(expressions []ast.Expr, index int) retentionDiagnosticBinding {
			if len(expressions) == 1 {
				if call, ok := expressions[0].(*ast.CallExpr); ok {
					name := callableName(call.Fun)
					if results := functionResults[name]; index < len(results) {
						if results[index] != "" {
							return wholeBinding(results[index])
						}
					}
					if binding := bindingFromParameterIndexes(call.Args, functionResultParameters[name][index]); binding.typeName != "" {
						return binding
					}
					if index < len(functionResults[name]) && index != 0 {
						return retentionDiagnosticBinding{}
					}
					if (name == "Claim" || name == "Advance") && index == 0 {
						return wholeBinding("LifecycleAttempt")
					}
					if index != 0 {
						return retentionDiagnosticBinding{}
					}
				}
			}
			if index >= len(expressions) {
				return retentionDiagnosticBinding{}
			}
			return expressionBinding(expressions[index])
		}
		bindingForType := func(expression ast.Expr) retentionDiagnosticBinding {
			if typeName := privateTypeName(expression); typeName != "" {
				return wholeBinding(typeName)
			}
			return retentionDiagnosticBinding{}
		}
		var rangeBindings func(ast.Expr) (retentionDiagnosticBinding, retentionDiagnosticBinding)
		rangeBindings = func(expression ast.Expr) (retentionDiagnosticBinding, retentionDiagnosticBinding) {
			switch value := expression.(type) {
			case *ast.ParenExpr:
				return rangeBindings(value.X)
			case *ast.Ident:
				if rangeType := rangeTypes[diagnosticKey(value)]; rangeType != nil {
					return rangeBindings(&ast.CompositeLit{Type: rangeType})
				}
			case *ast.CompositeLit:
				switch rangeType := value.Type.(type) {
				case *ast.MapType:
					return bindingForType(rangeType.Key), bindingForType(rangeType.Value)
				case *ast.ArrayType:
					return retentionDiagnosticBinding{}, bindingForType(rangeType.Elt)
				case *ast.ChanType:
					return bindingForType(rangeType.Value), retentionDiagnosticBinding{}
				}
			}
			return retentionDiagnosticBinding{}, expressionBinding(expression)
		}

		for changed := true; changed; {
			changed = false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch statement := node.(type) {
				case *ast.ValueSpec:
					for index, name := range statement.Names {
						binding := assignmentBinding(statement.Values, index)
						key := diagnosticKey(name)
						if binding.typeName != "" && bindings[key] != binding {
							bindings[key] = binding
							changed = true
						}
					}
				case *ast.AssignStmt:
					for index, left := range statement.Lhs {
						name, ok := left.(*ast.Ident)
						if !ok || name.Name == "_" {
							continue
						}
						binding := assignmentBinding(statement.Rhs, index)
						key := diagnosticKey(name)
						if binding.typeName != "" && bindings[key] != binding {
							bindings[key] = binding
							changed = true
						}
					}
				case *ast.RangeStmt:
					keyBinding, valueBinding := rangeBindings(statement.X)
					for identifier, binding := range map[ast.Expr]retentionDiagnosticBinding{
						statement.Key: keyBinding, statement.Value: valueBinding,
					} {
						name, ok := identifier.(*ast.Ident)
						if !ok || name.Name == "_" || binding.typeName == "" {
							continue
						}
						key := diagnosticKey(name)
						if bindings[key] != binding {
							bindings[key] = binding
							changed = true
						}
					}
				}
				return true
			})
		}
		for _, binding := range bindings {
			if binding.typeName != "" {
				analysis.classified[binding.typeName]++
			}
		}

		type controlRange struct{ start, end token.Pos }
		var conditionalRanges []controlRange
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch node.(type) {
			case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
				conditionalRanges = append(conditionalRanges, controlRange{start: node.Pos(), end: node.End()})
			}
			return true
		})
		isConditionalPosition := func(position token.Pos) bool {
			for _, candidate := range conditionalRanges {
				if candidate.start <= position && position <= candidate.end {
					return true
				}
			}
			return false
		}

		var formatterAssignments []formatterAssignment
		var formatAssignments []formatAssignment
		formatterObjectPosition := func(identifier *ast.Ident) token.Pos {
			if identifier.Obj == nil {
				return token.NoPos
			}
			return identifier.Obj.Pos()
		}
		matchesObject := func(identifier *ast.Ident, assignedName string, assignedPosition token.Pos) bool {
			objectPosition := formatterObjectPosition(identifier)
			if objectPosition != token.NoPos {
				return assignedPosition == objectPosition
			}
			return assignedPosition == token.NoPos && assignedName == identifier.Name
		}
		resolveFormatter := func(identifier *ast.Ident, position token.Pos) formatterBinding {
			resolved := formatterBinding{}
			for _, assignment := range formatterAssignments {
				if assignment.position >= position ||
					!matchesObject(identifier, assignment.identifier, assignment.objectPosition) {
					continue
				}
				if assignment.conditional {
					if !resolved.valid && assignment.binding.valid {
						resolved = assignment.binding
					}
					continue
				}
				resolved = assignment.binding
			}
			return resolved
		}
		var formatterFromExpression func(ast.Expr, token.Pos) formatterBinding
		formatterFromExpression = func(expression ast.Expr, position token.Pos) formatterBinding {
			switch value := expression.(type) {
			case *ast.SelectorExpr:
				formatIndex, ok := formatters[value.Sel.Name]
				return formatterBinding{name: value.Sel.Name, formatIndex: formatIndex, valid: ok}
			case *ast.Ident:
				return resolveFormatter(value, position)
			case *ast.ParenExpr:
				return formatterFromExpression(value.X, position)
			default:
				return formatterBinding{}
			}
		}
		cloneFormatState := func(state formatState) formatState {
			cloned := formatState{unknown: state.unknown, assigned: state.assigned}
			if len(state.literals) != 0 {
				cloned.literals = make(map[string]bool, len(state.literals))
				for literal := range state.literals {
					cloned.literals[literal] = true
				}
			}
			return cloned
		}
		joinFormatState := func(target formatState, source formatState) formatState {
			if !target.assigned {
				return cloneFormatState(source)
			}
			target.unknown = target.unknown || source.unknown
			target.assigned = target.assigned || source.assigned
			if len(source.literals) != 0 && target.literals == nil {
				target.literals = make(map[string]bool)
			}
			for literal := range source.literals {
				target.literals[literal] = true
			}
			return target
		}
		resolveFormat := func(identifier *ast.Ident, position token.Pos) formatState {
			resolved := formatState{}
			for _, assignment := range formatAssignments {
				if assignment.position >= position ||
					!matchesObject(identifier, assignment.identifier, assignment.objectPosition) {
					continue
				}
				if assignment.conditional {
					resolved = joinFormatState(resolved, assignment.state)
					continue
				}
				resolved = cloneFormatState(assignment.state)
			}
			if !resolved.assigned {
				resolved = formatState{unknown: true, assigned: true}
			}
			return resolved
		}
		var formatFromExpression func(ast.Expr, token.Pos) formatState
		formatFromExpression = func(expression ast.Expr, position token.Pos) formatState {
			switch value := expression.(type) {
			case *ast.BasicLit:
				if value.Kind == token.STRING {
					if literal, err := strconv.Unquote(value.Value); err == nil {
						return formatState{literals: map[string]bool{literal: true}, assigned: true}
					}
				}
			case *ast.Ident:
				return resolveFormat(value, position)
			case *ast.ParenExpr:
				return formatFromExpression(value.X, position)
			}
			return formatState{unknown: true, assigned: true}
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch statement := node.(type) {
			case *ast.AssignStmt:
				for index, left := range statement.Lhs {
					identifier, ok := left.(*ast.Ident)
					if !ok || identifier.Name == "_" || index >= len(statement.Rhs) {
						continue
					}
					binding := formatterFromExpression(statement.Rhs[index], statement.Pos())
					if binding.valid || resolveFormatter(identifier, statement.Pos()).valid {
						formatterAssignments = append(formatterAssignments, formatterAssignment{
							identifier: identifier.Name, objectPosition: formatterObjectPosition(identifier),
							binding: binding, position: statement.End(),
							conditional: isConditionalPosition(statement.Pos()),
						})
					}
					formatAssignments = append(formatAssignments, formatAssignment{
						identifier: identifier.Name, objectPosition: formatterObjectPosition(identifier),
						state: formatFromExpression(statement.Rhs[index], statement.Pos()), position: statement.End(),
						conditional: isConditionalPosition(statement.Pos()),
					})
				}
			case *ast.ValueSpec:
				for index, name := range statement.Names {
					if index >= len(statement.Values) {
						continue
					}
					binding := formatterFromExpression(statement.Values[index], statement.Pos())
					if binding.valid {
						formatterAssignments = append(formatterAssignments, formatterAssignment{
							identifier: name.Name, objectPosition: formatterObjectPosition(name),
							binding: binding, position: statement.End(),
							conditional: isConditionalPosition(statement.Pos()),
						})
					}
					formatAssignments = append(formatAssignments, formatAssignment{
						identifier: name.Name, objectPosition: formatterObjectPosition(name),
						state: formatFromExpression(statement.Values[index], statement.Pos()), position: statement.End(),
						conditional: isConditionalPosition(statement.Pos()),
					})
				}
			}
			return true
		})

		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			formatter := formatterFromExpression(call.Fun, call.Pos())
			if !formatter.valid {
				for _, summary := range sinkParameterSummaries[callableName(call.Fun)] {
					if summary.formatterParameter >= len(call.Args) || summary.valueParameter >= len(call.Args) {
						continue
					}
					sinkFormatter := formatterFromExpression(call.Args[summary.formatterParameter], call.Pos())
					binding := expressionBinding(call.Args[summary.valueParameter])
					if !sinkFormatter.valid || binding.exposure == retentionDiagnosticSafeString || binding.typeName == "" {
						continue
					}
					analysis.formatCalls++
					analysis.violations = append(analysis.violations, fmt.Sprintf(
						"%d:%s:%s:%s",
						files.Position(call.Pos()).Line, sinkFormatter.name, binding.typeName, binding.exposure,
					))
				}
				return true
			}
			if len(call.Args) <= formatter.formatIndex {
				return true
			}
			analysis.formatCalls++
			type sensitiveArgument struct {
				index   int
				binding retentionDiagnosticBinding
			}
			var sensitive []sensitiveArgument
			for index, argument := range call.Args[formatter.formatIndex+1:] {
				binding := expressionBinding(argument)
				if binding.exposure == retentionDiagnosticWhole || binding.exposure == retentionDiagnosticPrivateField {
					sensitive = append(sensitive, sensitiveArgument{index: index, binding: binding})
				}
			}
			if len(sensitive) == 0 {
				return true
			}
			formats := formatFromExpression(call.Args[formatter.formatIndex], call.Pos())
			for _, argument := range sensitive {
				violation, dynamic := formats.unknown, formats.unknown
				for format := range formats.literals {
					unsafeArguments, consumedArguments, reordered, knownFormat := privateFormatArguments(format)
					if !knownFormat {
						violation, dynamic = true, true
						continue
					}
					if consumedArguments[argument.index] && !unsafeArguments[argument.index] {
						continue
					}
					if reordered && !consumedArguments[argument.index] {
						continue
					}
					violation = true
				}
				if !violation {
					continue
				}
				suffix := ""
				if dynamic {
					suffix = ":dynamic"
				}
				analysis.violations = append(analysis.violations, fmt.Sprintf(
					"%d:%s:%s:%s%s",
					files.Position(call.Pos()).Line, formatter.name,
					argument.binding.typeName, argument.binding.exposure, suffix,
				))
			}
			return true
		})
	}
	return analysis
}

func TestLifecycleClaimMovesCommittedPointToExpiringWithLeaseFence(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(600)
	pointID := testOpaqueID(601)
	policyID := testOpaqueID(602)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-48*time.Hour), 11)
	point.PointRevision = 7
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed lifecycle point: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 3, RulesJSON: `{"version":1,"count":{"keep_latest":1}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed lifecycle policy: %v", err)
	}

	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }), Now: func() time.Time { return now },
		NewID:        func() (string, error) { return testOpaqueID(603), nil },
		LeaseOwnerID: "retention-worker-test",
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	selection := Selection{
		PolicyID: policyID, PolicyRevision: 3,
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"count":{"keep_latest":1}}`, RuleDigest: strings.Repeat("a", 64), EvaluatedAt: now,
		Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 7, CapabilityRevision: 11}},
	}
	attempt, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID,
		Operation:       backupasset.LifecycleRetentionExpire,
		PolicySelection: &selection,
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if attempt.ID != testOpaqueID(603) || attempt.RecoveryPointID != pointID ||
		attempt.Operation != backupasset.LifecycleRetentionExpire || attempt.Phase != backupasset.LifecyclePhaseSelected ||
		attempt.TransitionRevision != 1 || attempt.LeaseID == "" || attempt.LeaseFenceTokenHash == "" {
		t.Fatalf("claimed lifecycle attempt mismatch: %+v", attempt)
	}
	repeated, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID,
		Operation:       backupasset.LifecycleRetentionExpire,
		PolicySelection: &selection,
	})
	if err != nil || repeated.ID != attempt.ID || repeated.TransitionRevision != attempt.TransitionRevision {
		t.Fatalf("repeat exact claim attempt=%+v error=%v, want idempotent existing attempt", repeated, err)
	}

	var persistedPoint model.RecoveryPoint
	if err := db.First(&persistedPoint, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load claimed point: %v", err)
	}
	if persistedPoint.State != string(backupasset.RecoveryPointExpiring) || persistedPoint.PointRevision != 8 ||
		persistedPoint.CapabilityRevision != 11 {
		t.Fatalf("claimed point state/revisions=%s/%d/%d, want expiring/8/11",
			persistedPoint.State, persistedPoint.PointRevision, persistedPoint.CapabilityRevision)
	}

	var lease model.RecoveryPointLease
	if err := db.First(&lease, "id = ?", attempt.LeaseID).Error; err != nil {
		t.Fatalf("load lifecycle lease: %v", err)
	}
	if lease.RecoveryPointID != pointID || lease.HolderType != string(backupasset.LeaseHolderRetentionWorker) ||
		lease.OwnerID != "retention-worker-test" || lease.Status != string(backupasset.LeaseActive) ||
		attempt.LeaseFenceTokenHash == lease.FenceToken {
		t.Fatalf("lifecycle lease binding mismatch: attempt_id=%s phase=%s revision=%d lease_id=%s point_id=%s holder=%s state=%s owner_match=%t attempt_binding_present=%t fence_hash_matches_raw=%t",
			attempt.ID, attempt.Phase, attempt.TransitionRevision,
			lease.ID, lease.RecoveryPointID, lease.HolderType, lease.Status,
			lease.OwnerID == "retention-worker-test", lease.AttemptID != "", attempt.LeaseFenceTokenHash == lease.FenceToken)
	}
	payload, err := json.Marshal(attempt)
	if err != nil {
		t.Fatalf("marshal lifecycle attempt: %v", err)
	}
	if strings.Contains(string(payload), lease.FenceToken) || strings.Contains(string(payload), "fence_token") {
		t.Fatalf("lifecycle attempt JSON exposed raw fence material: %s", payload)
	}
}

func TestLifecyclePointRequestCarriesOperationToOwnerPorts(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 605)
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("6", 64),
	}
	attempt := fixture.attempt
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete,
		backupasset.LifecyclePhaseTombstoning,
	} {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("advance lifecycle to %q: attempt=%+v err=%v", want, attempt, err)
		}
	}
	admission := fixture.coordinator.admissions.(*lifecycleAdmissionFake)
	if admission.operation != backupasset.LifecycleRetentionExpire ||
		fixture.cleanup.operation != backupasset.LifecycleRetentionExpire ||
		fixture.deleter.operation != backupasset.LifecycleRetentionExpire {
		t.Fatalf("owner operations admission/cleanup/deletion=%q/%q/%q, want retention_expire",
			admission.operation, fixture.cleanup.operation, fixture.deleter.operation)
	}
}

func TestLeaseAdmissionRejectsActiveLifecycleAttempt(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 17, 11, 15, 0, 0, time.UTC)
	repositoryID := testOpaqueID(610)
	pointID := testOpaqueID(611)
	policyID := testOpaqueID(612)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-48*time.Hour), 4)
	point.PointRevision = 9
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed admission-fenced point: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 2, RulesJSON: `{"version":1,"age":{"keep_days":30}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed admission-fence policy: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }), Now: func() time.Time { return now },
		NewID: func() (string, error) { return testOpaqueID(613), nil }, LeaseOwnerID: "retention-worker-admission-test",
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	selection := Selection{
		PolicyID: policyID, PolicyRevision: 2,
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("b", 64), EvaluatedAt: now,
		Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 9, CapabilityRevision: 4}},
	}
	if _, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire, PolicySelection: &selection,
	}); err != nil {
		t.Fatalf("claim admission-fenced lifecycle: %v", err)
	}

	_, err = leaseService.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
		RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderContentSession, OwnerID: "late-content-session",
	})
	if !errors.Is(err, backupasset.ErrLeaseHeld) {
		t.Fatalf("late lifecycle admission error=%v, want ErrLeaseHeld", err)
	}
	var lateCount int64
	if countErr := db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ?", pointID, backupasset.LeaseHolderContentSession).
		Count(&lateCount).Error; countErr != nil {
		t.Fatalf("count late lifecycle admission leases: %v", countErr)
	}
	if lateCount != 0 {
		t.Fatalf("late lifecycle admission persisted %d content leases", lateCount)
	}
}

func TestLifecycleLiveLeaseBlocksDrainingAndRetriesAfterBoundedTakeover(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	clock := time.Date(2026, 8, 17, 11, 30, 0, 0, time.UTC)
	repositoryID := testOpaqueID(620)
	pointID := testOpaqueID(621)
	policyID := testOpaqueID(622)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(pointID, repositoryID, nil, clock.Add(-72*time.Hour), 5)
	point.PointRevision = 12
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed lease-blocked point: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 4, RulesJSON: `{"version":1,"age":{"keep_days":30}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: clock, UpdatedAt: clock,
	}).Error; err != nil {
		t.Fatalf("seed lease-blocked policy: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	readerLease, err := leaseService.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
		RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderPointPublication, OwnerID: "existing-point-publication",
	})
	if err != nil {
		t.Fatalf("seed live content lease: %v", err)
	}
	admission := &lifecycleAdmissionFake{}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return clock }), Now: func() time.Time { return clock },
		NewID: func() (string, error) { return testOpaqueID(623), nil }, LeaseOwnerID: "retention-worker-drain-test",
		Admissions: admission, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	selection := Selection{
		PolicyID: policyID, PolicyRevision: 4,
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("c", 64), EvaluatedAt: clock,
		Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 12, CapabilityRevision: 5}},
	}
	attempt, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire, PolicySelection: &selection,
	})
	if err != nil {
		t.Fatalf("claim lease-blocked lifecycle: %v", err)
	}
	attempt, err = coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseRevoking {
		t.Fatalf("advance selected attempt=%+v error=%v, want revoking", attempt, err)
	}
	attempt, err = coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseDraining || admission.calls != 1 {
		t.Fatalf("advance revoking attempt=%+v calls=%d error=%v, want draining/1", attempt, admission.calls, err)
	}
	attempt, err = coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked ||
		attempt.BlockedReason != backupasset.LifecycleBlockedLeaseLive || attempt.RetryAt == nil {
		t.Fatalf("live lease drain attempt=%+v error=%v, want blocked/lease_live", attempt, err)
	}
	assertLifecyclePointState(t, db, pointID, backupasset.RecoveryPointPurgeBlocked)

	clock = clock.Add(6 * time.Minute)
	retried, err := coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || retried.Phase != backupasset.LifecyclePhaseDraining || retried.BlockedReason != "" ||
		retried.LeaseAttemptID == attempt.LeaseAttemptID || retried.LeaseFenceTokenHash == attempt.LeaseFenceTokenHash {
		t.Fatalf("retry blocked attempt=%+v error=%v, want draining with rotated lifecycle fence", retried, err)
	}
	assertLifecyclePointState(t, db, pointID, backupasset.RecoveryPointExpiring)
	advanced, err := coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || advanced.Phase != backupasset.LifecyclePhaseCleaning {
		t.Fatalf("bounded takeover drain attempt=%+v error=%v, want cleaning", advanced, err)
	}
	if err := leaseService.ValidateFence(context.Background(), readerLease.Fence); !errors.Is(err, backupasset.ErrLeaseFenceLost) {
		t.Fatalf("drained reader fence error=%v, want ErrLeaseFenceLost", err)
	}
	var readerRow model.RecoveryPointLease
	if err := db.First(&readerRow, "id = ?", readerLease.ID).Error; err != nil {
		t.Fatalf("load drained reader lease: %v", err)
	}
	if readerRow.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("drained reader lease status=%q, want released", readerRow.Status)
	}
}

func TestLifecycleDependentCleanupDoesNotDrainRecoveryResultLease(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 625)
	if err := fixture.db.AutoMigrate(&model.BackupAssetDeliveryGrant{}, &model.BackupAssetDeliveryRequest{}); err != nil {
		t.Fatalf("migrate RecoveryResult content boundary: %v", err)
	}
	grantID, leaseID := testOpaqueID(629), testOpaqueID(630)
	recoveryJobID, recoveryResultID := testOpaqueID(631), testOpaqueID(632)
	grant := model.BackupAssetDeliveryGrant{
		ID: grantID, DeliveryID: testOpaqueID(633), ResourceKind: "recovery_result",
		RecoveryJobID: &recoveryJobID, RecoveryResultID: &recoveryResultID,
		State: "active", LeaseID: leaseID, LeaseAttemptID: testOpaqueID(634),
		LeaseFenceTokenHash: strings.Repeat("7", 64), InFlight: 1,
	}
	if err := fixture.db.Create(&grant).Error; err != nil {
		t.Fatalf("seed RecoveryResult delivery grant: %v", err)
	}
	read := model.BackupAssetDeliveryRequest{
		ID: testOpaqueID(635), GrantID: grantID, State: "streaming", StartedAt: fixture.clock,
	}
	if err := fixture.db.Create(&read).Error; err != nil {
		t.Fatalf("seed RecoveryResult delivery read: %v", err)
	}
	lease := model.RecoveryPointLease{
		ID: leaseID, RecoveryPointID: fixture.pointID,
		HolderType: string(backupasset.LeaseHolderContentSession), OwnerID: grantID,
		AttemptID: grant.LeaseAttemptID, FenceToken: strings.Repeat("8", 64),
		Status: string(backupasset.LeaseActive), LeaseExpiresAt: fixture.clock.Add(5 * time.Minute),
		AbsoluteDeadline: fixture.clock.Add(time.Hour), LastHeartbeatAt: fixture.clock,
	}
	if err := fixture.db.Create(&lease).Error; err != nil {
		t.Fatalf("seed RecoveryResult source-point content lease: %v", err)
	}

	attempt := fixture.attempt
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
	} {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("advance with independent RecoveryResult lease want=%q attempt=%+v err=%v", want, attempt, err)
		}
	}
	var persistedGrant model.BackupAssetDeliveryGrant
	if err := fixture.db.First(&persistedGrant, "id = ?", grantID).Error; err != nil {
		t.Fatalf("load RecoveryResult grant after generic drain: %v", err)
	}
	if persistedGrant.ResourceKind != grant.ResourceKind || persistedGrant.State != grant.State ||
		persistedGrant.InFlight != grant.InFlight || persistedGrant.LeaseID != grant.LeaseID {
		t.Fatalf("generic drain changed RecoveryResult grant: got_state=%s want_state=%s got_version=%d want_version=%d id_match=%t resource_kind_match=%t inflight_match=%t lease_id_match=%t",
			persistedGrant.State, grant.State, persistedGrant.Version, grant.Version, persistedGrant.ID == grant.ID,
			persistedGrant.ResourceKind == grant.ResourceKind, persistedGrant.InFlight == grant.InFlight,
			persistedGrant.LeaseID == grant.LeaseID)
	}
	var persistedRead model.BackupAssetDeliveryRequest
	if err := fixture.db.First(&persistedRead, "id = ?", read.ID).Error; err != nil {
		t.Fatalf("load RecoveryResult read after generic drain: %v", err)
	}
	if persistedRead.State != read.State || persistedRead.GrantID != read.GrantID {
		t.Fatalf("generic drain changed RecoveryResult read: got_state=%s want_state=%s got_version=%d want_version=%d id_match=%t grant_id_match=%t",
			persistedRead.State, read.State, persistedRead.Version, read.Version,
			persistedRead.ID == read.ID, persistedRead.GrantID == read.GrantID)
	}
	var persistedLease model.RecoveryPointLease
	if err := fixture.db.First(&persistedLease, "id = ?", leaseID).Error; err != nil {
		t.Fatalf("load RecoveryResult lease after generic drain: %v", err)
	}
	if persistedLease.Status != string(backupasset.LeaseActive) || persistedLease.AttemptID != lease.AttemptID ||
		persistedLease.FenceToken != lease.FenceToken || persistedLease.OwnerID != grantID {
		t.Fatalf("generic drain changed RecoveryResult lease: got_id=%s want_id=%s got_state=%s want_state=%s point_id_match=%t attempt_match=%t fence_match=%t owner_match=%t",
			persistedLease.ID, lease.ID, persistedLease.Status, lease.Status,
			persistedLease.RecoveryPointID == lease.RecoveryPointID, persistedLease.AttemptID == lease.AttemptID,
			persistedLease.FenceToken == lease.FenceToken, persistedLease.OwnerID == grantID)
	}
}

func TestLifecycleImmutableExpiryPersistsDeletionTombstoneBeforeExpired(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(630)
	pointID := testOpaqueID(631)
	policyID := testOpaqueID(632)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-96*time.Hour), 8)
	point.PointRevision = 15
	point.EncryptedProviderLocator = `{"snapshot":"exact-private-provider-id"}`
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed expiring point: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 5, RulesJSON: `{"version":1,"age":{"keep_days":30}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed expiry policy: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	cleanup := &lifecycleCleanupFake{}
	deleter := &lifecycleDeletionFake{result: PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("d", 64),
	}}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }), Now: func() time.Time { return now },
		NewID: func() (string, error) { return testOpaqueID(633), nil }, LeaseOwnerID: "retention-worker-expiry-test",
		Admissions: &lifecycleAdmissionFake{}, Cleanup: cleanup, Deleter: deleter,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	selection := Selection{
		PolicyID: policyID, PolicyRevision: 5,
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("e", 64), EvaluatedAt: now,
		Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 15, CapabilityRevision: 8}},
	}
	attempt, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire, PolicySelection: &selection,
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete,
		backupasset.LifecyclePhaseTombstoning,
		backupasset.LifecyclePhaseComplete,
	} {
		attempt, err = coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("Advance want phase=%q attempt=%+v error=%v", want, attempt, err)
		}
	}
	if cleanup.calls != 1 || deleter.calls != 1 || deleter.pointID != pointID || deleter.attemptID != attempt.ID {
		t.Fatalf("exact cleanup/delete calls=%d/%d point=%q attempt=%q", cleanup.calls, deleter.calls, deleter.pointID, deleter.attemptID)
	}
	var persistedPoint model.RecoveryPoint
	if err := db.First(&persistedPoint, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load expired point: %v", err)
	}
	if persistedPoint.State != string(backupasset.RecoveryPointExpired) ||
		persistedPoint.PhysicalAvailability != string(backupasset.PhysicalMissing) ||
		persistedPoint.EncryptedProviderLocator != "" || persistedPoint.PointRevision != 17 {
		t.Fatalf("expired point projection state/physical/locator/revision=%q/%q/%t/%d",
			persistedPoint.State, persistedPoint.PhysicalAvailability, persistedPoint.EncryptedProviderLocator != "", persistedPoint.PointRevision)
	}
	var tombstone model.RecoveryPointLifecycleTombstone
	if err := db.First(&tombstone, "recovery_point_id = ?", pointID).Error; err != nil {
		t.Fatalf("load expiry tombstone: %v", err)
	}
	if tombstone.TerminalState != string(backupasset.RecoveryPointExpired) || tombstone.ResultCode != string(PointDeletionDeleted) ||
		tombstone.DeletionReceiptDigest == nil || *tombstone.DeletionReceiptDigest != strings.Repeat("d", 64) || tombstone.PurgedAt == nil {
		t.Fatalf("expiry tombstone mismatch: %+v", tombstone)
	}
	var lifecycleLease model.RecoveryPointLease
	if err := db.First(&lifecycleLease, "id = ?", attempt.LeaseID).Error; err != nil {
		t.Fatalf("load completed lifecycle lease: %v", err)
	}
	if lifecycleLease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("completed lifecycle lease status=%q, want released", lifecycleLease.Status)
	}
	repeated, err := coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || repeated.Phase != backupasset.LifecyclePhaseComplete || deleter.calls != 1 || cleanup.calls != 1 {
		t.Fatalf("repeat complete attempt=%+v cleanup/delete=%d/%d error=%v", repeated, cleanup.calls, deleter.calls, err)
	}
}

func TestExpiryClearsPrivateLocatorOnlyAfterRegistryDeletedOrAlreadyAbsent(t *testing.T) {
	for _, outcome := range []struct {
		name     string
		result   provider.DeletePointResult
		wantCode PointDeletionOutcome
	}{
		{name: "deleted", result: provider.DeletePointResult{Outcome: provider.DeletePointDeleted, ReceiptDigest: strings.Repeat("a", 64)}, wantCode: PointDeletionDeleted},
		{name: "already_absent", result: provider.DeletePointResult{Outcome: provider.DeletePointAlreadyAbsent, ReceiptDigest: strings.Repeat("b", 64)}, wantCode: PointDeletionAlreadyAbsent},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			db := newLifecycleCoordinatorTestDB(t)
			now := time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)
			repositoryID := testOpaqueID(710)
			pointID := testOpaqueID(711)
			policyID := testOpaqueID(712)
			seedRetentionUsersAndRepository(t, db, repositoryID)
			privateLocator := `{"snapshot":"FAKE_PRIVATE_REGISTRY_LOCATOR_FOR_TEST_ONLY"}`
			point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-96*time.Hour), 1)
			point.SourceFingerprint = strings.Repeat("c", 64)
			point.PointRevision = 20
			point.EncryptedProviderLocator = privateLocator
			if err := db.Create(&point).Error; err != nil {
				t.Fatalf("seed registry-expiry point: %v", err)
			}
			if err := db.Create(&model.BackupRetentionPolicy{
				ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
				Revision: 1, RulesJSON: `{"version":1,"age":{"keep_days":30}}`,
				Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("seed registry-expiry policy: %v", err)
			}
			leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
				Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
			})
			if err != nil {
				t.Fatalf("NewLeaseService: %v", err)
			}
			port := &registryPointDeleterFake{kind: backupasset.ProviderRestic, result: outcome.result}
			registry := provider.NewRegistry()
			if err := registry.Register(backupasset.ProviderRestic, provider.Registration{
				Prober: &retentionProviderProberFake{}, PointDeleter: port,
			}); err != nil {
				t.Fatalf("Register PointDeleter: %v", err)
			}
			snapshot := provider.ReadSnapshot{
				RepositoryID: repositoryID, CapabilityRevision: 1, SourceRevision: strings.Repeat("c", 64),
			}
			adapter, err := NewRegistryPointDeletion(db, registry, registryDeletePointResolver{snapshot: snapshot})
			if err != nil {
				t.Fatalf("NewRegistryPointDeletion: %v", err)
			}
			coordinator, err := NewCoordinator(CoordinatorDependencies{
				DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }),
				Now: func() time.Time { return now }, NewID: func() (string, error) { return testOpaqueID(713), nil },
				LeaseOwnerID: "retention-worker-registry-delete-test",
				Admissions:   &lifecycleAdmissionFake{}, Cleanup: &lifecycleCleanupFake{}, Deleter: adapter,
			})
			if err != nil {
				t.Fatalf("NewCoordinator: %v", err)
			}
			attempt, err := coordinator.Claim(context.Background(), ClaimRequest{
				RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire,
				PolicySelection: &Selection{
					PolicyID: policyID, PolicyRevision: 1,
					ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
					RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("6", 64), EvaluatedAt: now,
					Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 20, CapabilityRevision: 1}},
				},
			})
			if err != nil {
				t.Fatalf("Claim: %v", err)
			}
			for _, want := range []backupasset.LifecyclePhase{
				backupasset.LifecyclePhaseRevoking, backupasset.LifecyclePhaseDraining, backupasset.LifecyclePhaseCleaning,
				backupasset.LifecyclePhaseProviderDelete, backupasset.LifecyclePhaseTombstoning, backupasset.LifecyclePhaseComplete,
			} {
				attempt, err = coordinator.Advance(context.Background(), attempt.ID)
				if err != nil || attempt.Phase != want {
					t.Fatalf("Advance want phase=%q attempt=%+v error=%v", want, attempt, err)
				}
			}
			if port.calls != 1 || port.request.Point.Native != privateLocator {
				t.Fatalf("registry deleter calls=%d locator=%q", port.calls, port.request.Point.Native)
			}
			var persisted model.RecoveryPoint
			if err := db.First(&persisted, "id = ?", pointID).Error; err != nil {
				t.Fatalf("load expired point: %v", err)
			}
			if persisted.State != string(backupasset.RecoveryPointExpired) || persisted.EncryptedProviderLocator != "" {
				t.Fatalf("expired point state/locator=%q/%q, want expired and cleared", persisted.State, persisted.EncryptedProviderLocator)
			}
			var tombstone model.RecoveryPointLifecycleTombstone
			if err := db.First(&tombstone, "recovery_point_id = ?", pointID).Error; err != nil {
				t.Fatalf("load expiry tombstone: %v", err)
			}
			if tombstone.ResultCode != string(outcome.wantCode) || tombstone.DeletionReceiptDigest == nil ||
				*tombstone.DeletionReceiptDigest != outcome.result.ReceiptDigest {
				t.Fatalf("registry deletion tombstone mismatch: %+v", tombstone)
			}
			replayed, replayErr := coordinator.Advance(context.Background(), attempt.ID)
			if replayErr != nil || replayed.Phase != backupasset.LifecyclePhaseComplete || port.calls != 1 {
				t.Fatalf("replayed registry deletion phase=%q calls=%d error=%v, want complete/1/nil",
					replayed.Phase, port.calls, replayErr)
			}
		})
	}
}

func TestLifecycleRegistryPointDeletionMapsWORMWithoutClearingLocator(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 18, 13, 15, 0, 0, time.UTC)
	repositoryID := testOpaqueID(720)
	pointID := testOpaqueID(721)
	policyID := testOpaqueID(722)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	privateLocator := `{"snapshot":"FAKE_WORM_PRIVATE_LOCATOR_FOR_TEST_ONLY"}`
	point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-96*time.Hour), 1)
	point.SourceFingerprint = strings.Repeat("c", 64)
	point.PointRevision = 8
	point.EncryptedProviderLocator = privateLocator
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed WORM point: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 1, RulesJSON: `{"version":1,"age":{"keep_days":30}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed WORM policy: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	port := &registryPointDeleterFake{kind: backupasset.ProviderRestic, err: provider.ErrDeletePointWORM}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{
		Prober: &retentionProviderProberFake{}, PointDeleter: port,
	}); err != nil {
		t.Fatalf("Register PointDeleter: %v", err)
	}
	snapshot := provider.ReadSnapshot{
		RepositoryID: repositoryID, CapabilityRevision: 1, SourceRevision: strings.Repeat("c", 64),
	}
	adapter, err := NewRegistryPointDeletion(db, registry, registryDeletePointResolver{snapshot: snapshot})
	if err != nil {
		t.Fatalf("NewRegistryPointDeletion: %v", err)
	}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }),
		Now: func() time.Time { return now }, NewID: func() (string, error) { return testOpaqueID(723), nil },
		LeaseOwnerID: "retention-worker-registry-worm-test",
		Admissions:   &lifecycleAdmissionFake{}, Cleanup: &lifecycleCleanupFake{}, Deleter: adapter,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	attempt, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire,
		PolicySelection: &Selection{
			PolicyID: policyID, PolicyRevision: 1,
			ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
			RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("6", 64), EvaluatedAt: now,
			Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 8, CapabilityRevision: 1}},
		},
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking, backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning, backupasset.LifecyclePhaseProviderDelete,
	} {
		attempt, err = coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("Advance want phase=%q attempt=%+v error=%v", want, attempt, err)
		}
	}
	uncertain, err := coordinator.Advance(context.Background(), attempt.ID)
	if err == nil || uncertain.Phase != backupasset.LifecyclePhaseProviderDelete {
		t.Fatalf("WORM provider error attempt=%+v err=%v, want uncertain provider_delete", uncertain, err)
	}
	if port.calls != 1 {
		t.Fatalf("WORM provider calls=%d, want one call", port.calls)
	}
	var claim model.RecoveryPointLifecycleEffectClaim
	if err := db.First(&claim, "attempt_id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load uncertain WORM claim: %v", err)
	}
	if claim.State != "uncertain" {
		t.Fatalf("WORM claim state=%q, want uncertain", claim.State)
	}
	var tombstone model.RecoveryPointLifecycleTombstone
	if err := db.Where("recovery_point_id = ?", pointID).First(&tombstone).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("WORM unexpectedly persisted tombstone: %+v err=%v", tombstone, err)
	}
	var persisted model.RecoveryPoint
	if err := db.First(&persisted, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load WORM-blocked point: %v", err)
	}
	if persisted.EncryptedProviderLocator != privateLocator || persisted.State == string(backupasset.RecoveryPointExpired) {
		t.Fatalf("WORM unexpectedly expired or cleared locator: state=%q locator_cleared=%t",
			persisted.State, persisted.EncryptedProviderLocator == "")
	}
}

func TestLifecycleRegistryPointDeletionMissingCapabilityStaysUnproven(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 18, 13, 30, 0, 0, time.UTC)
	repositoryID := testOpaqueID(730)
	pointID := testOpaqueID(731)
	policyID := testOpaqueID(732)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	privateLocator := `{"snapshot":"FAKE_UNPROVEN_PRIVATE_LOCATOR_FOR_TEST_ONLY"}`
	point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-96*time.Hour), 1)
	point.SourceFingerprint = strings.Repeat("c", 64)
	point.PointRevision = 5
	point.EncryptedProviderLocator = privateLocator
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed unproven point: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 1, RulesJSON: `{"version":1,"age":{"keep_days":30}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed unproven policy: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: &retentionProviderProberFake{}}); err != nil {
		t.Fatalf("Register read-only provider: %v", err)
	}
	snapshot := provider.ReadSnapshot{
		RepositoryID: repositoryID, CapabilityRevision: 1, SourceRevision: strings.Repeat("c", 64),
	}
	adapter, err := NewRegistryPointDeletion(db, registry, registryDeletePointResolver{snapshot: snapshot})
	if err != nil {
		t.Fatalf("NewRegistryPointDeletion: %v", err)
	}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }),
		Now: func() time.Time { return now }, NewID: func() (string, error) { return testOpaqueID(733), nil },
		LeaseOwnerID: "retention-worker-registry-unproven-test",
		Admissions:   &lifecycleAdmissionFake{}, Cleanup: &lifecycleCleanupFake{}, Deleter: adapter,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	attempt, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire,
		PolicySelection: &Selection{
			PolicyID: policyID, PolicyRevision: 1,
			ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
			RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("6", 64), EvaluatedAt: now,
			Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 5, CapabilityRevision: 1}},
		},
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking, backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning, backupasset.LifecyclePhaseProviderDelete,
	} {
		attempt, err = coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("Advance want phase=%q attempt=%+v error=%v", want, attempt, err)
		}
	}
	blocked, err := coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
		blocked.BlockedReason != backupasset.LifecycleBlockedDeletionUnavailable {
		t.Fatalf("missing deleter blocked attempt=%+v err=%v, want typed deletion_unavailable", blocked, err)
	}
	var persisted model.RecoveryPoint
	if err := db.First(&persisted, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load unproven point: %v", err)
	}
	if persisted.EncryptedProviderLocator != privateLocator || persisted.State == string(backupasset.RecoveryPointExpired) {
		t.Fatalf("unproven delete expired or cleared locator: state=%q locator_cleared=%t",
			persisted.State, persisted.EncryptedProviderLocator == "")
	}
}

func TestMutableHeadRetirementStaysObservedUntilCleanupAndNeverDeletesProvider(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC)
	repositoryID := testOpaqueID(640)
	pointID := testOpaqueID(641)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	observedAt := now.Add(-time.Hour)
	privateLocator := `{"mutable":"private-head"}`
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID,
		Semantics: string(backupasset.PointMutableHead), State: string(backupasset.RecoveryPointObserved),
		ObservedAt: &observedAt, PointRevision: 4, CapabilityRevision: 6,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		EncryptedProviderLocator: privateLocator, CreatedAt: observedAt, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed mutable head: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	cleanup := &lifecycleCleanupFake{}
	deleter := &lifecycleDeletionFake{result: PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("f", 64),
	}}
	newCoordinator := func() *Coordinator {
		coordinator, newErr := NewCoordinator(CoordinatorDependencies{
			DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }), Now: func() time.Time { return now },
			NewID: func() (string, error) { return testOpaqueID(642), nil }, LeaseOwnerID: "retention-worker-mutable-test",
			Admissions: &lifecycleAdmissionFake{}, Cleanup: cleanup, Deleter: deleter,
		})
		if newErr != nil {
			t.Fatalf("NewCoordinator: %v", newErr)
		}
		return coordinator
	}
	coordinator := newCoordinator()
	attempt, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleMutableRetire,
		MutablePoint: &MutableRetirementSnapshot{PointRevision: 4, CapabilityRevision: 6},
	})
	if err != nil {
		t.Fatalf("Claim mutable retirement: %v", err)
	}
	assertLifecyclePointState(t, db, pointID, backupasset.RecoveryPointObserved)
	if _, err := leaseService.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
		RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderContentSession, OwnerID: "late-mutable-reader",
	}); !errors.Is(err, backupasset.ErrLeaseHeld) {
		t.Fatalf("late mutable lease error=%v, want ErrLeaseHeld", err)
	}
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseTombstoning,
		backupasset.LifecyclePhaseComplete,
	} {
		coordinator = newCoordinator()
		attempt, err = coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("restart Advance want phase=%q attempt=%+v error=%v", want, attempt, err)
		}
		if want != backupasset.LifecyclePhaseComplete {
			assertLifecyclePointState(t, db, pointID, backupasset.RecoveryPointObserved)
		}
	}
	if cleanup.calls != 1 || deleter.calls != 0 {
		t.Fatalf("mutable cleanup/delete calls=%d/%d, want 1/0", cleanup.calls, deleter.calls)
	}
	var retired model.RecoveryPoint
	if err := db.First(&retired, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load retired mutable head: %v", err)
	}
	if retired.State != string(backupasset.RecoveryPointRetired) || retired.RetirementReason == nil ||
		*retired.RetirementReason != string(backupasset.RetirementWithdrawn) || retired.RetiredAt == nil ||
		retired.EncryptedProviderLocator != "" || retired.EncryptedRollbackLocator != privateLocator ||
		retired.PhysicalAvailability != string(backupasset.PhysicalOnline) {
		t.Fatalf("retired mutable projection mismatch: %+v", retired)
	}
	var tombstone model.RecoveryPointLifecycleTombstone
	if err := db.First(&tombstone, "recovery_point_id = ?", pointID).Error; err != nil {
		t.Fatalf("load mutable retirement tombstone: %v", err)
	}
	if tombstone.TerminalState != string(backupasset.RecoveryPointRetired) || tombstone.ResultCode != "mutable_retired" ||
		tombstone.RetiredAt == nil || tombstone.PurgedAt != nil || tombstone.DeletionReceiptDigest != nil {
		t.Fatalf("mutable retirement tombstone mismatch: %+v", tombstone)
	}
}

func TestPurgeExplicitMutableHeadRequiresExactPlanAndDeletesOnlyExactPoint(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(650)
	pointID := testOpaqueID(651)
	planID := testOpaqueID(652)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	observedAt := now.Add(-2 * time.Hour)
	retiredAt := now.Add(-time.Hour)
	retirementReason := string(backupasset.RetirementWithdrawn)
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID,
		Semantics: string(backupasset.PointMutableHead), State: string(backupasset.RecoveryPointRetired),
		ObservedAt: &observedAt, PointRevision: 9, CapabilityRevision: 3,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		EncryptedRollbackLocator: `{"mutable":"private-retired-head"}`,
		RetirementReason:         &retirementReason, RetiredAt: &retiredAt, CreatedAt: observedAt, UpdatedAt: retiredAt,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed retired mutable head: %v", err)
	}
	actorID := uint(1)
	if err := db.Create(&model.BackupAssetPurgePlan{
		ID: planID, RepositoryID: repositoryID, RequesterID: actorID, Revision: 2, ImpactRevision: 7,
		ExpiresAt: now.Add(time.Hour), Status: string(backupasset.PurgePlanExecuting), ExecuteActorID: &actorID,
		ExecuteProofDigest: strings.Repeat("1", 64), ExecuteReasonDigest: strings.Repeat("2", 64),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed purge plan: %v", err)
	}
	if err := db.Create(&model.BackupAssetPurgePlanItem{
		ID: testOpaqueID(653), PlanID: planID, Ordinal: 0, RecoveryPointID: pointID,
		ExpectedPointRevision: 9, ExpectedCapabilityRevision: 3, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed purge plan item: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	deleter := &lifecycleDeletionFake{result: PointDeletionResult{
		Outcome: PointDeletionAlreadyAbsent, ReceiptDigest: strings.Repeat("3", 64),
	}}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }), Now: func() time.Time { return now },
		NewID: func() (string, error) { return testOpaqueID(654), nil }, LeaseOwnerID: "retention-worker-purge-test",
		Admissions: &lifecycleAdmissionFake{}, Cleanup: &lifecycleCleanupFake{}, Deleter: deleter,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	attempt, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleExplicitPurge,
		PurgePlan: &PurgePlanSnapshot{
			PlanID: planID, Revision: 2, ActorID: actorID, PointRevision: 9, CapabilityRevision: 3,
		},
	})
	if err != nil {
		t.Fatalf("Claim explicit mutable purge: %v", err)
	}
	assertLifecyclePointState(t, db, pointID, backupasset.RecoveryPointExpiring)
	for attempt.Phase != backupasset.LifecyclePhaseComplete {
		attempt, err = coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("Advance explicit mutable purge phase=%q: %v", attempt.Phase, err)
		}
	}
	if deleter.calls != 1 || deleter.pointID != pointID {
		t.Fatalf("explicit mutable purge delete calls/point=%d/%q", deleter.calls, deleter.pointID)
	}
	var expired model.RecoveryPoint
	if err := db.First(&expired, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load explicitly purged mutable head: %v", err)
	}
	if expired.State != string(backupasset.RecoveryPointExpired) || expired.PhysicalAvailability != string(backupasset.PhysicalMissing) ||
		expired.EncryptedProviderLocator != "" || expired.EncryptedRollbackLocator != "" || expired.PointRevision != 11 {
		t.Fatalf("explicit mutable purge projection mismatch: %+v", expired)
	}
	var tombstone model.RecoveryPointLifecycleTombstone
	if err := db.First(&tombstone, "recovery_point_id = ?", pointID).Error; err != nil {
		t.Fatalf("load explicit mutable purge tombstone: %v", err)
	}
	if tombstone.TerminalOperation != string(backupasset.LifecycleExplicitPurge) ||
		tombstone.ResultCode != string(PointDeletionAlreadyAbsent) || tombstone.PurgedAt == nil {
		t.Fatalf("explicit mutable purge tombstone mismatch: %+v", tombstone)
	}
}

func TestMutableRetirementThenExplicitPurgePreservesOperationTombstones(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 17, 13, 10, 0, 0, time.UTC)
	repositoryID := testOpaqueID(970)
	pointID := testOpaqueID(971)
	planID := testOpaqueID(972)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	observedAt := now.Add(-2 * time.Hour)
	privateLocator := `{"mutable":"private-retire-purge-head"}`
	point := model.RecoveryPoint{
		ID: pointID, RepositoryID: repositoryID,
		Semantics: string(backupasset.PointMutableHead), State: string(backupasset.RecoveryPointObserved),
		ObservedAt: &observedAt, PointRevision: 4, CapabilityRevision: 6,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityMutable),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		EncryptedProviderLocator: privateLocator, CreatedAt: observedAt, UpdatedAt: now,
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed mutable retire-purge point: %v", err)
	}
	leases, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	cleanup := &lifecycleCleanupFake{}
	deleter := &lifecycleDeletionFake{result: PointDeletionResult{
		Outcome: PointDeletionAlreadyAbsent, ReceiptDigest: strings.Repeat("7", 64),
	}}
	attemptIDs := []string{testOpaqueID(973), testOpaqueID(974)}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leases, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }),
		Now: func() time.Time { return now },
		NewID: func() (string, error) {
			id := attemptIDs[0]
			attemptIDs = attemptIDs[1:]
			return id, nil
		},
		LeaseOwnerID: "retention-worker-retire-purge-test", Admissions: &lifecycleAdmissionFake{},
		Cleanup: cleanup, Deleter: deleter,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	retirement, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleMutableRetire,
		MutablePoint: &MutableRetirementSnapshot{PointRevision: 4, CapabilityRevision: 6},
	})
	if err != nil {
		t.Fatalf("claim real mutable retirement: %v", err)
	}
	for retirement.Phase != backupasset.LifecyclePhaseComplete {
		retirement, err = coordinator.Advance(context.Background(), retirement.ID)
		if err != nil {
			t.Fatalf("advance real mutable retirement phase=%q: %v", retirement.Phase, err)
		}
	}
	var retired model.RecoveryPoint
	if err := db.First(&retired, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load real retired point: %v", err)
	}
	if retired.State != string(backupasset.RecoveryPointRetired) || retired.PointRevision != 5 ||
		retired.EncryptedProviderLocator != "" || retired.EncryptedRollbackLocator != privateLocator {
		t.Fatalf("real mutable retirement projection mismatch: %+v", retired)
	}
	observeCompletionLookup := true
	completionLookupSeen := false
	completionLookupExact := false
	const callbackName = "task5:require-operation-scoped-tombstone-load"
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if !observeCompletionLookup || query.Statement.Table != "recovery_point_lifecycle_tombstones" {
			return
		}
		observeCompletionLookup = false
		completionLookupSeen = true
		completionLookupExact = strings.Contains(strings.ToLower(query.Statement.SQL.String()), "terminal_operation")
	}); err != nil {
		t.Fatalf("register tombstone lookup observer: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove tombstone lookup observer: %v", err)
		}
	})
	actorID := uint(1)
	if err := db.Create(&model.BackupAssetPurgePlan{
		ID: planID, RepositoryID: repositoryID, RequesterID: actorID, Revision: 1, ImpactRevision: 1,
		ExpiresAt: now.Add(time.Hour), Status: string(backupasset.PurgePlanExecuting), ExecuteActorID: &actorID,
		ExecuteProofDigest: strings.Repeat("1", 64), ExecuteReasonDigest: strings.Repeat("2", 64),
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create explicit purge plan after retirement: %v", err)
	}
	if err := db.Create(&model.BackupAssetPurgePlanItem{
		ID: testOpaqueID(975), PlanID: planID, Ordinal: 0, RecoveryPointID: pointID,
		ExpectedPointRevision: retired.PointRevision, ExpectedCapabilityRevision: retired.CapabilityRevision, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create explicit purge plan item after retirement: %v", err)
	}
	purge, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleExplicitPurge,
		PurgePlan: &PurgePlanSnapshot{
			PlanID: planID, Revision: 1, ActorID: actorID,
			PointRevision: retired.PointRevision, CapabilityRevision: retired.CapabilityRevision,
		},
	})
	if err != nil {
		t.Fatalf("claim explicit purge after real retirement: %v", err)
	}
	for purge.Phase != backupasset.LifecyclePhaseComplete {
		purge, err = coordinator.Advance(context.Background(), purge.ID)
		if err != nil {
			t.Fatalf("advance explicit purge after retirement phase=%q: %v", purge.Phase, err)
		}
	}
	if !completionLookupSeen || !completionLookupExact {
		t.Fatalf("explicit purge completion tombstone lookup seen/exact=%t/%t, want point+terminal_operation binding",
			completionLookupSeen, completionLookupExact)
	}
	var tombstones []model.RecoveryPointLifecycleTombstone
	if err := db.Where("recovery_point_id = ?", pointID).Order("terminal_operation ASC").Find(&tombstones).Error; err != nil {
		t.Fatalf("load retire-purge tombstone history: %v", err)
	}
	if len(tombstones) != 2 {
		t.Fatalf("retire-purge tombstone count=%d, want two immutable operation events", len(tombstones))
	}
	byOperation := make(map[string]model.RecoveryPointLifecycleTombstone, len(tombstones))
	for _, tombstone := range tombstones {
		byOperation[tombstone.TerminalOperation] = tombstone
	}
	mutable := byOperation[string(backupasset.LifecycleMutableRetire)]
	explicit := byOperation[string(backupasset.LifecycleExplicitPurge)]
	if mutable.ResultCode != "mutable_retired" || mutable.RetiredAt == nil || mutable.PurgedAt != nil || mutable.DeletionReceiptDigest != nil {
		t.Fatalf("immutable mutable-retire event mismatch: %+v", mutable)
	}
	if explicit.ResultCode != string(PointDeletionAlreadyAbsent) || explicit.PurgedAt == nil ||
		explicit.DeletionReceiptDigest == nil || *explicit.DeletionReceiptDigest != strings.Repeat("7", 64) {
		t.Fatalf("immutable explicit-purge event mismatch: %+v", explicit)
	}
	replayedRetirement, retirementErr := coordinator.Advance(context.Background(), retirement.ID)
	replayedPurge, purgeErr := coordinator.Advance(context.Background(), purge.ID)
	if retirementErr != nil || purgeErr != nil || replayedRetirement.Phase != backupasset.LifecyclePhaseComplete ||
		replayedPurge.Phase != backupasset.LifecyclePhaseComplete || cleanup.calls != 2 || deleter.calls != 1 {
		t.Fatalf("retire-purge replay phases=%q/%q revisions=%d/%d effects=%d/%d errors=%v/%v",
			replayedRetirement.Phase, replayedPurge.Phase,
			replayedRetirement.TransitionRevision, replayedPurge.TransitionRevision,
			cleanup.calls, deleter.calls, retirementErr, purgeErr)
	}
}

func TestExplicitPurgeWithInitialHoldCreatesBlockedAttemptAndResumesAfterRelease(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	clock := time.Date(2026, 8, 17, 13, 15, 0, 0, time.UTC)
	repositoryID := testOpaqueID(655)
	pointID := testOpaqueID(656)
	planID := testOpaqueID(657)
	holdID := testOpaqueID(658)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(pointID, repositoryID, nil, clock.Add(-48*time.Hour), 3)
	point.PointRevision = 9
	point.HoldState = string(backupasset.HoldActive)
	point.EncryptedProviderLocator = `{"snapshot":"private-held-point"}`
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed held purge point: %v", err)
	}
	if err := db.Create(&model.RecoveryPointHold{
		ID: holdID, RecoveryPointID: pointID, HoldType: string(backupasset.RecoveryPointHoldLegal),
		State: string(backupasset.HoldActive), EncryptedReason: "FAKE_HELD_PURGE_REASON_FOR_TEST_ONLY",
		CreatedBy: 1, CreatedAt: clock, UpdatedAt: clock,
	}).Error; err != nil {
		t.Fatalf("seed held purge hold: %v", err)
	}
	actorID := uint(1)
	if err := db.Create(&model.BackupAssetPurgePlan{
		ID: planID, RepositoryID: repositoryID, RequesterID: actorID, Revision: 1, ImpactRevision: 1,
		ExpiresAt: clock.Add(time.Hour), Status: string(backupasset.PurgePlanExecuting), ExecuteActorID: &actorID,
		ExecuteProofDigest: strings.Repeat("1", 64), ExecuteReasonDigest: strings.Repeat("2", 64),
		CreatedAt: clock, UpdatedAt: clock,
	}).Error; err != nil {
		t.Fatalf("seed held purge plan: %v", err)
	}
	if err := db.Create(&model.BackupAssetPurgePlanItem{
		ID: testOpaqueID(659), PlanID: planID, Ordinal: 0, RecoveryPointID: pointID,
		ExpectedPointRevision: 9, ExpectedCapabilityRevision: 3, CreatedAt: clock,
	}).Error; err != nil {
		t.Fatalf("seed held purge plan item: %v", err)
	}
	leases, err := backupasset.NewLeaseService(db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	cleanup := &lifecycleCleanupFake{}
	deleter := &lifecycleDeletionFake{result: PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("3", 64),
	}}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leases, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return clock }), Now: func() time.Time { return clock },
		NewID: func() (string, error) { return testOpaqueID(660), nil }, LeaseOwnerID: "retention-worker-held-purge-test",
		Admissions: &lifecycleAdmissionFake{}, Cleanup: cleanup, Deleter: deleter, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	claim := ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleExplicitPurge,
		PurgePlan: &PurgePlanSnapshot{
			PlanID: planID, Revision: 1, ActorID: actorID, PointRevision: 9, CapabilityRevision: 3,
		},
	}
	attempt, err := coordinator.Claim(context.Background(), claim)
	if err != nil {
		t.Fatalf("Claim held explicit purge: %v", err)
	}
	if attempt.Phase != backupasset.LifecyclePhaseBlocked ||
		attempt.BlockedReason != backupasset.LifecycleBlockedActiveHold || attempt.TransitionRevision != 2 ||
		attempt.RetryAt == nil || cleanup.calls != 0 || deleter.calls != 0 {
		t.Fatalf("initial held purge attempt=%+v effects=%d/%d, want blocked/active_hold/revision 2/zero", attempt, cleanup.calls, deleter.calls)
	}
	var blockedPoint model.RecoveryPoint
	if err := db.First(&blockedPoint, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load initial blocked purge point: %v", err)
	}
	if blockedPoint.State != string(backupasset.RecoveryPointPurgeBlocked) || blockedPoint.PointRevision != 10 {
		t.Fatalf("initial blocked purge point state/revision=%q/%d, want purge_blocked/10", blockedPoint.State, blockedPoint.PointRevision)
	}
	replayed, err := coordinator.Claim(context.Background(), claim)
	if err != nil || replayed.ID != attempt.ID || replayed.TransitionRevision != 2 {
		t.Fatalf("replay held explicit purge attempt=%+v error=%v", replayed, err)
	}

	clock = clock.Add(2 * time.Minute)
	if err := db.Model(&model.RecoveryPointHold{}).Where("id = ?", holdID).Updates(map[string]any{
		"state": backupasset.HoldReleased, "released_by": actorID, "released_at": clock, "updated_at": clock,
	}).Error; err != nil {
		t.Fatalf("release held purge hold: %v", err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Updates(map[string]any{
		"hold_state": backupasset.HoldReleased, "point_revision": 11, "updated_at": clock,
	}).Error; err != nil {
		t.Fatalf("project held purge release: %v", err)
	}
	attempt, err = coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseRevoking || attempt.TransitionRevision != 3 {
		t.Fatalf("retry released held purge attempt=%+v error=%v, want revoking/revision 3", attempt, err)
	}
	var resumedPoint model.RecoveryPoint
	if err := db.First(&resumedPoint, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load resumed held purge point: %v", err)
	}
	if resumedPoint.State != string(backupasset.RecoveryPointExpiring) || resumedPoint.PointRevision != 12 {
		t.Fatalf("resumed held purge point state/revision=%q/%d, want expiring/12", resumedPoint.State, resumedPoint.PointRevision)
	}
	for attempt.Phase != backupasset.LifecyclePhaseComplete {
		attempt, err = coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("complete held explicit purge from phase %q: %v", attempt.Phase, err)
		}
	}
	if attempt.TransitionRevision != 8 || cleanup.calls != 1 || deleter.calls != 1 {
		t.Fatalf("completed held purge revision/effects=%d/%d/%d, want 8/1/1", attempt.TransitionRevision, cleanup.calls, deleter.calls)
	}
}

func TestLifecycleHoldAfterClaimBlocksAndRetryRestartsAtRevocation(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	clock := time.Date(2026, 8, 17, 13, 30, 0, 0, time.UTC)
	repositoryID := testOpaqueID(660)
	pointID := testOpaqueID(661)
	policyID := testOpaqueID(662)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(pointID, repositoryID, nil, clock.Add(-72*time.Hour), 2)
	point.PointRevision = 20
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed hold-race point: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 1, RulesJSON: `{"version":1,"age":{"keep_days":30}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: clock, UpdatedAt: clock,
	}).Error; err != nil {
		t.Fatalf("seed hold-race policy: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return clock }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return clock }), Now: func() time.Time { return clock },
		NewID: func() (string, error) { return testOpaqueID(663), nil }, LeaseOwnerID: "retention-worker-hold-test",
		Admissions: &lifecycleAdmissionFake{}, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	selection := Selection{
		PolicyID: policyID, PolicyRevision: 1,
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("4", 64), EvaluatedAt: clock,
		Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 20, CapabilityRevision: 2}},
	}
	attempt, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire, PolicySelection: &selection,
	})
	if err != nil {
		t.Fatalf("Claim hold-race lifecycle: %v", err)
	}
	holdID := testOpaqueID(664)
	if err := db.Create(&model.RecoveryPointHold{
		ID: holdID, RecoveryPointID: pointID, HoldType: string(backupasset.RecoveryPointHoldLegal),
		State: string(backupasset.HoldActive), EncryptedReason: "private legal hold reason",
		CreatedBy: 1, CreatedAt: clock, UpdatedAt: clock,
	}).Error; err != nil {
		t.Fatalf("seed active hold after claim: %v", err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Updates(map[string]any{
		"hold_state": backupasset.HoldActive, "point_revision": 22, "updated_at": clock,
	}).Error; err != nil {
		t.Fatalf("project active hold after claim: %v", err)
	}
	attempt, err = coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked ||
		attempt.BlockedReason != backupasset.LifecycleBlockedActiveHold {
		t.Fatalf("hold-race attempt=%+v error=%v, want blocked/active_hold", attempt, err)
	}
	assertLifecyclePointState(t, db, pointID, backupasset.RecoveryPointPurgeBlocked)

	clock = clock.Add(6 * time.Minute)
	releasedAt := clock
	if err := db.Model(&model.RecoveryPointHold{}).Where("id = ?", holdID).Updates(map[string]any{
		"state": backupasset.HoldReleased, "released_by": uint(1), "released_at": releasedAt, "updated_at": releasedAt,
	}).Error; err != nil {
		t.Fatalf("release lifecycle hold: %v", err)
	}
	if err := db.Model(&model.RecoveryPoint{}).Where("id = ?", pointID).Updates(map[string]any{
		"hold_state": backupasset.HoldReleased, "point_revision": 24, "updated_at": releasedAt,
	}).Error; err != nil {
		t.Fatalf("project released lifecycle hold: %v", err)
	}
	retried, err := coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || retried.Phase != backupasset.LifecyclePhaseRevoking || retried.BlockedReason != "" {
		t.Fatalf("released hold retry attempt=%+v error=%v, want safe revocation restart", retried, err)
	}
	assertLifecyclePointState(t, db, pointID, backupasset.RecoveryPointExpiring)
}

func TestLifecycleRevokeFailureResumesAtRevokingNotCleaning(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 880)
	admissions := &lifecycleAdmissionFake{err: errors.New("revoke owners unavailable")}
	fixture.coordinator.admissions = admissions

	attempt, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseRevoking {
		t.Fatalf("Advance to revoking attempt=%+v error=%v", attempt, err)
	}
	blocked, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
		blocked.BlockedReason != backupasset.LifecycleBlockedOwnerCleanupUnproven {
		t.Fatalf("revoke failure attempt=%+v error=%v, want blocked/owner_cleanup_unproven", blocked, err)
	}
	cleanupBefore := fixture.cleanup.calls
	fixture.clock = fixture.clock.Add(2 * time.Minute)
	admissions.err = nil
	retried, err := fixture.coordinator.Advance(context.Background(), blocked.ID)
	if err != nil || retried.Phase != backupasset.LifecyclePhaseRevoking || retried.BlockedReason != "" {
		t.Fatalf("revoke-failure retry attempt=%+v error=%v, want revoking", retried, err)
	}
	if fixture.cleanup.calls != cleanupBefore {
		t.Fatalf("revoke-failure retry entered cleanup calls=%d, want %d", fixture.cleanup.calls, cleanupBefore)
	}
}

func TestLifecycleProviderFailuresRemainUncertainAndRetryFenced(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason backupasset.LifecycleBlockedReason
	}{
		{name: "worm", err: ErrPointDeletionWORM, reason: backupasset.LifecycleBlockedProviderWORM},
		{name: "unavailable", err: backupasset.ErrProviderUnavailable, reason: backupasset.LifecycleBlockedProviderUnavailable},
		{name: "native version referenced", err: provider.ErrDeletePointNativeVersionReferenced, reason: backupasset.LifecycleBlockedProviderNativeVersionReferenced},
		{name: "identity", err: ErrPointDeletionIdentityConflict, reason: backupasset.LifecycleBlockedProviderIdentityConflict},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, uint64(700+index*10))
			fixture.deleter.prepareErr = test.err
			attempt := fixture.attempt
			var err error
			for _, want := range []backupasset.LifecyclePhase{
				backupasset.LifecyclePhaseRevoking,
				backupasset.LifecyclePhaseDraining,
				backupasset.LifecyclePhaseCleaning,
				backupasset.LifecyclePhaseProviderDelete,
			} {
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil || attempt.Phase != want {
					t.Fatalf("Advance to provider deletion want=%q attempt=%+v error=%v", want, attempt, err)
				}
			}
			attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked || attempt.BlockedReason != test.reason {
				t.Fatalf("provider observer block attempt=%+v error=%v, want %q", attempt, err, test.reason)
			}
			assertLifecyclePointState(t, fixture.db, fixture.pointID, backupasset.RecoveryPointPurgeBlocked)
			var claimCount int64
			if err := fixture.db.Model(&model.RecoveryPointLifecycleEffectClaim{}).
				Where("attempt_id = ?", attempt.ID).Count(&claimCount).Error; err != nil {
				t.Fatalf("count pre-provider claims: %v", err)
			}
			if claimCount != 0 {
				t.Fatalf("pre-provider observer block claims=%d, want zero", claimCount)
			}

			fixture.clock = attempt.RetryAt.UTC().Add(time.Second)
			fixture.deleter.prepareErr = nil
			fixture.deleter.result = PointDeletionResult{
				Outcome: PointDeletionAlreadyAbsent, ReceiptDigest: strings.Repeat("5", 64),
			}
			attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil || attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
				t.Fatalf("provider unblock retry attempt=%+v error=%v, want provider_delete", attempt, err)
			}
			assertLifecyclePointState(t, fixture.db, fixture.pointID, backupasset.RecoveryPointExpiring)
			attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil || attempt.Phase != backupasset.LifecyclePhaseTombstoning {
				t.Fatalf("provider retry deletion attempt=%+v error=%v, want tombstoning", attempt, err)
			}
			attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil || attempt.Phase != backupasset.LifecyclePhaseComplete {
				t.Fatalf("provider retry completion attempt=%+v error=%v", attempt, err)
			}
		})
	}
}

func TestLifecycleProviderDeleteAmbiguousClaimCommitNeverBlocks(t *testing.T) {
	t.Run("committed first acquisition with canceled parent", func(t *testing.T) {
		fixture := newClaimedExpiryFixture(t, 2110)
		attempt := fixture.attempt
		for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
			var err error
			attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil {
				t.Fatalf("advance ambiguous-commit fixture to provider_delete: %v", err)
			}
		}

		sqlDB, err := fixture.db.DB()
		if err != nil {
			t.Fatalf("load ambiguous-commit SQL database: %v", err)
		}
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		ambiguousDB := fixture.db.Session(&gorm.Session{NewDB: true})
		ambiguousDB.Statement.ConnPool = &ambiguousCommitConnPool{
			DB: sqlDB, failNext: true, onAmbiguousCommit: cancel,
		}
		coordinator, err := NewCoordinator(CoordinatorDependencies{
			DB: ambiguousDB, Leases: fixture.coordinator.leases, Holds: fixture.holds,
			Now:          func() time.Time { return fixture.clock },
			NewID:        func() (string, error) { return testOpaqueID(2113), nil },
			LeaseOwnerID: fixture.coordinator.leaseOwnerID, Admissions: fixture.coordinator.admissions,
			Cleanup: fixture.cleanup, Deleter: fixture.deleter,
			EffectExecutorID: testOpaqueID(2114), EffectClaimTTL: fixture.coordinator.effectClaimTTL,
			EffectClaimAfter: fixture.coordinator.effectClaimAfter, RetryDelay: fixture.coordinator.retryDelay,
		})
		if err != nil {
			t.Fatalf("construct ambiguous-commit coordinator: %v", err)
		}

		beforeRevision := attempt.TransitionRevision
		got, err := coordinator.Advance(parent, attempt.ID)
		if !errors.Is(err, context.Canceled) ||
			got.Phase != backupasset.LifecyclePhaseProviderDelete ||
			got.TransitionRevision != beforeRevision || got.RetryAt != nil {
			t.Fatalf("ambiguous first acquisition attempt=%+v error=%v, want canceled in-flight observation without block/retry",
				got, err)
		}
		if fixture.deleter.calls != 0 {
			t.Fatalf("ambiguous first acquisition provider calls=%d, want zero", fixture.deleter.calls)
		}
		var claim model.RecoveryPointLifecycleEffectClaim
		if err := fixture.db.First(&claim, "attempt_id = ?", attempt.ID).Error; err != nil {
			t.Fatalf("load durable first-acquisition claim: %v", err)
		}
		if claim.State != "in_flight" || claim.ExecutionID == "" {
			t.Fatalf("ambiguous first-acquisition claim=%+v, want durable in-flight claim", claim)
		}
		var tombstoneCount int64
		if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
			Where("recovery_point_id = ?", fixture.pointID).Count(&tombstoneCount).Error; err != nil {
			t.Fatalf("count first-acquisition tombstones: %v", err)
		}
		if tombstoneCount != 0 {
			t.Fatalf("ambiguous first-acquisition tombstones=%d, want zero", tombstoneCount)
		}
	})

	t.Run("committed takeover with canceled parent", func(t *testing.T) {
		fixture := newClaimedExpiryFixture(t, 2140)
		attempt := fixture.attempt
		for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
			var err error
			attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil {
				t.Fatalf("advance takeover fixture to provider_delete: %v", err)
			}
		}
		fixture.deleter.err = errors.New("seed uncertain provider execution")
		failed, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
		fixture.deleter.err = nil
		if err == nil || failed.Phase != backupasset.LifecyclePhaseProviderDelete || failed.RetryAt == nil {
			t.Fatalf("seed uncertain takeover claim attempt=%+v error=%v", failed, err)
		}
		providerCallsBefore := fixture.deleter.calls
		fixture.clock = failed.RetryAt.UTC().Add(time.Second)
		if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", failed.LeaseID).
			Update("lease_expires_at", fixture.clock.Add(-time.Second)).Error; err != nil {
			t.Fatalf("expire takeover lease: %v", err)
		}
		beforeAttempt, err := fixture.coordinator.loadAttempt(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("load takeover attempt: %v", err)
		}
		beforeClaim := model.RecoveryPointLifecycleEffectClaim{}
		if err := fixture.db.First(&beforeClaim, "attempt_id = ?", attempt.ID).Error; err != nil {
			t.Fatalf("load uncertain takeover claim: %v", err)
		}
		if beforeClaim.State != "uncertain" {
			t.Fatalf("takeover seed claim=%+v, want uncertain", beforeClaim)
		}

		sqlDB, err := fixture.db.DB()
		if err != nil {
			t.Fatalf("load takeover SQL database: %v", err)
		}
		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		ambiguousDB := fixture.db.Session(&gorm.Session{NewDB: true})
		ambiguousDB.Statement.ConnPool = &ambiguousCommitConnPool{
			DB: sqlDB, failNext: true, onAmbiguousCommit: cancel,
		}
		coordinator, err := NewCoordinator(CoordinatorDependencies{
			DB: ambiguousDB, Leases: fixture.coordinator.leases, Holds: fixture.holds,
			Now:          func() time.Time { return fixture.clock },
			NewID:        func() (string, error) { return testOpaqueID(2143), nil },
			LeaseOwnerID: fixture.coordinator.leaseOwnerID, Admissions: fixture.coordinator.admissions,
			Cleanup: fixture.cleanup, Deleter: fixture.deleter,
			EffectExecutorID: testOpaqueID(2144), EffectClaimTTL: fixture.coordinator.effectClaimTTL,
			EffectClaimAfter: fixture.coordinator.effectClaimAfter, RetryDelay: fixture.coordinator.retryDelay,
		})
		if err != nil {
			t.Fatalf("construct takeover ambiguous-commit coordinator: %v", err)
		}

		got, err := coordinator.Advance(parent, attempt.ID)
		if !errors.Is(err, context.Canceled) ||
			got.Phase != backupasset.LifecyclePhaseProviderDelete ||
			got.TransitionRevision <= beforeAttempt.TransitionRevision || got.RetryAt != nil {
			t.Fatalf("ambiguous takeover attempt=%+v error=%v, want canceled in-flight observation after takeover",
				got, err)
		}
		if fixture.deleter.calls != providerCallsBefore {
			t.Fatalf("ambiguous takeover provider calls=%d, want %d", fixture.deleter.calls, providerCallsBefore)
		}
		var claim model.RecoveryPointLifecycleEffectClaim
		if err := fixture.db.First(&claim, "attempt_id = ?", attempt.ID).Error; err != nil {
			t.Fatalf("load durable takeover claim: %v", err)
		}
		if claim.State != "in_flight" || claim.ExecutionID == beforeClaim.ExecutionID ||
			claim.LeaseFenceTokenHash == beforeClaim.LeaseFenceTokenHash {
			t.Fatalf("ambiguous takeover claim=%+v before=%+v, want new durable in-flight binding", claim, beforeClaim)
		}
		if got.BlockedReason != "" {
			t.Fatalf("ambiguous takeover blocked reason=%q, want empty", got.BlockedReason)
		}
	})

	t.Run("pre-claim cancellation remains canceled", func(t *testing.T) {
		fixture := newClaimedExpiryFixture(t, 2170)
		attempt := fixture.attempt
		for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
			var err error
			attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil {
				t.Fatalf("advance pre-claim fixture to provider_delete: %v", err)
			}
		}
		before, err := fixture.coordinator.loadAttempt(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("load pre-claim attempt: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := fixture.coordinator.Advance(ctx, attempt.ID)
		if !errors.Is(err, context.Canceled) || got.ID != "" {
			t.Fatalf("pre-claim canceled attempt=%+v error=%v, want empty canceled result", got, err)
		}
		if fixture.deleter.calls != 0 {
			t.Fatalf("pre-claim canceled provider calls=%d, want zero", fixture.deleter.calls)
		}
		var claimCount int64
		if err := fixture.db.Model(&model.RecoveryPointLifecycleEffectClaim{}).
			Where("attempt_id = ?", attempt.ID).Count(&claimCount).Error; err != nil {
			t.Fatalf("count pre-claim canceled claims: %v", err)
		}
		if claimCount != 0 {
			t.Fatalf("pre-claim canceled claims=%d, want zero", claimCount)
		}
		after, err := fixture.coordinator.loadAttempt(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("reload pre-claim attempt: %v", err)
		}
		if after.Phase != before.Phase || after.TransitionRevision != before.TransitionRevision ||
			after.BlockedReason != before.BlockedReason || after.RetryAt != before.RetryAt {
			t.Fatalf("pre-claim canceled attempt changed before=%+v after=%+v", before, after)
		}
	})
}

func TestLifecycleProviderDeletePreClaimFailureReconcilesConcurrentWinner(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 2150)
	attempt := fixture.attempt
	for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance pre-claim race fixture to provider_delete: %v", err)
		}
	}

	winnerDeleter := &lifecycleDeletionFake{
		result: PointDeletionResult{
			Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("e", 64),
		},
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	winnerCoordinator := acceptanceCloneCoordinator(
		t, fixture, winnerDeleter, testOpaqueID(2155),
	)
	winnerDone := make(chan struct {
		attempt LifecycleAttempt
		err     error
	}, 1)
	sqlDB, err := fixture.db.DB()
	if err != nil {
		t.Fatalf("load pre-claim race SQL database: %v", err)
	}
	racePool := &preClaimWinnerConnPool{DB: sqlDB}
	raceDB := fixture.db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	raceDB.Statement.ConnPool = racePool
	racePool.onFallbackBegin = func() {
		go func() {
			winner, winnerErr := winnerCoordinator.Advance(context.Background(), attempt.ID)
			winnerDone <- struct {
				attempt LifecycleAttempt
				err     error
			}{winner, winnerErr}
		}()
		select {
		case <-winnerDeleter.entered:
		case <-time.After(5 * time.Second):
		}
	}
	failureCoordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: raceDB, Leases: fixture.coordinator.leases, Holds: fixture.holds,
		Now:          func() time.Time { return fixture.clock },
		NewID:        func() (string, error) { return testOpaqueID(2153), nil },
		LeaseOwnerID: fixture.coordinator.leaseOwnerID, Admissions: fixture.coordinator.admissions,
		Cleanup: fixture.cleanup, EffectExecutorID: testOpaqueID(2154),
		EffectClaimTTL: fixture.coordinator.effectClaimTTL, EffectClaimAfter: fixture.coordinator.effectClaimAfter,
		RetryDelay: fixture.coordinator.retryDelay,
	})
	if err != nil {
		t.Fatalf("construct pre-claim failure coordinator: %v", err)
	}

	failureDone := make(chan struct {
		attempt LifecycleAttempt
		err     error
	}, 1)
	go func() {
		failedAttempt, failureErr := failureCoordinator.resolveProviderDeletePreparationFailure(
			context.Background(), attempt.ID,
			backupasset.LifecycleBlockedProviderDeleteUnproven,
			errors.New("pre-claim transaction rolled back"),
		)
		failureDone <- struct {
			attempt LifecycleAttempt
			err     error
		}{failedAttempt, failureErr}
	}()

	var failedResult struct {
		attempt LifecycleAttempt
		err     error
	}
	select {
	case failedResult = <-failureDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pre-claim failure did not reconcile with the concurrent winner")
	}
	if !errors.Is(failedResult.err, ErrEffectClaimInFlight) ||
		failedResult.attempt.Phase != backupasset.LifecyclePhaseProviderDelete ||
		failedResult.attempt.RetryAt != nil {
		t.Fatalf("pre-claim failure result attempt=%+v error=%v, want in-flight observation without block/retry",
			failedResult.attempt, failedResult.err)
	}
	var claim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&claim, "attempt_id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load winner claim before release: %v", err)
	}
	if claim.State != "in_flight" {
		t.Fatalf("winner claim before release state=%q, want in_flight", claim.State)
	}

	close(winnerDeleter.release)
	select {
	case winnerResult := <-winnerDone:
		if winnerResult.err != nil || winnerResult.attempt.Phase != backupasset.LifecyclePhaseTombstoning {
			t.Fatalf("winner result attempt=%+v error=%v, want tombstoning",
				winnerResult.attempt, winnerResult.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent winner did not finish after provider release")
	}
	if winnerDeleter.calls != 1 {
		t.Fatalf("concurrent winner provider calls=%d, want one", winnerDeleter.calls)
	}
	if err := fixture.db.First(&claim, "attempt_id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load winner claim after release: %v", err)
	}
	if claim.State != "proven" {
		t.Fatalf("winner claim after release state=%q, want proven", claim.State)
	}
	var tombstoneCount int64
	if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
		Where("recovery_point_id = ?", fixture.pointID).Count(&tombstoneCount).Error; err != nil {
		t.Fatalf("count winner tombstones: %v", err)
	}
	if tombstoneCount != 1 {
		t.Fatalf("winner tombstones=%d, want one", tombstoneCount)
	}
}

func TestLifecycleNativeVersionReferenceBlockAuditsAsOrdinaryBlocked(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 1740)
	fixture.deleter.prepareErr = provider.ErrDeletePointNativeVersionReferenced
	audit := &recordingSettledAudit{}
	fixture.coordinator.audit = audit

	attempt := fixture.attempt
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete,
	} {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("Advance to %s attempt=%+v error=%v", want, attempt, err)
		}
	}
	attempt, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked ||
		attempt.BlockedReason != backupasset.LifecycleBlockedProviderNativeVersionReferenced {
		t.Fatalf("native reference dependency block attempt=%+v error=%v", attempt, err)
	}
	if len(audit.events) != 0 {
		t.Fatalf("native reference dependency settled audit before RetryAt=%+v, want none", audit.events)
	}

	fixture.clock = attempt.RetryAt.Add(time.Second)
	fixture.deleter.prepareErr = nil
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionAlreadyAbsent, ReceiptDigest: strings.Repeat("5", 64),
	}
	retried, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || retried.Phase != backupasset.LifecyclePhaseProviderDelete ||
		retried.BlockedReason != "" {
		t.Fatalf("native reference dependency retry attempt=%+v error=%v, want provider_delete", retried, err)
	}
	if len(audit.events) != 1 ||
		audit.events[0].Fields[backupasset.AuditFieldStatus] != "blocked" {
		t.Fatalf("native reference dependency settled audit after RetryAt=%+v, want ordinary blocked", audit.events)
	}
}
func TestLifecycleProviderReceiptSurvivesHoldAppearingAfterEffect(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 1640)
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("a", 64),
	}
	holdID := testOpaqueID(1644)
	fixture.deleter.afterEffect = func() {
		if err := fixture.db.Create(&model.RecoveryPointHold{
			ID: holdID, RecoveryPointID: fixture.pointID,
			HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
			EncryptedReason: "hold appeared after provider effect", CreatedBy: 1,
			CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
		}).Error; err != nil {
			t.Errorf("create post-effect active hold: %v", err)
			return
		}
		if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).
			Updates(map[string]any{
				"hold_state": backupasset.HoldActive, "point_revision": 32, "updated_at": fixture.clock,
			}).Error; err != nil {
			t.Errorf("project post-effect active hold: %v", err)
		}
	}
	attempt := fixture.attempt
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete,
	} {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("advance to provider deletion want=%q attempt=%+v error=%v", want, attempt, err)
		}
	}
	blocked, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
		blocked.BlockedReason != backupasset.LifecycleBlockedActiveHold {
		t.Fatalf("post-effect hold attempt=%+v error=%v, want blocked/active_hold", blocked, err)
	}
	if fixture.deleter.calls != 1 || fixture.deleter.completed != 1 {
		t.Fatalf("post-effect hold provider calls/completed=%d/%d, want 1/1", fixture.deleter.calls, fixture.deleter.completed)
	}
	var event model.RecoveryPointLifecycleTombstone
	if err := fixture.db.First(&event, "recovery_point_id = ? AND terminal_operation = ?", fixture.pointID, backupasset.LifecycleRetentionExpire).Error; err != nil {
		t.Fatalf("load post-effect terminal event: %v", err)
	}
	if event.ResultCode != string(PointDeletionDeleted) || event.DeletionReceiptDigest == nil ||
		*event.DeletionReceiptDigest != strings.Repeat("a", 64) || event.PurgedAt == nil {
		t.Fatalf("post-effect terminal event=%+v, want settled provider receipt", event)
	}
	assertLifecyclePointState(t, fixture.db, fixture.pointID, backupasset.RecoveryPointPurgeBlocked)

	fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
	if err := fixture.db.Model(&model.RecoveryPointHold{}).Where("id = ?", holdID).Updates(map[string]any{
		"state": backupasset.HoldReleased, "released_by": uint(1),
		"released_at": fixture.clock, "updated_at": fixture.clock,
	}).Error; err != nil {
		t.Fatalf("release post-effect active hold: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).Updates(map[string]any{
		"hold_state": backupasset.HoldReleased, "point_revision": 34, "updated_at": fixture.clock,
	}).Error; err != nil {
		t.Fatalf("project post-effect hold release: %v", err)
	}
	resumed, err := fixture.coordinator.Advance(context.Background(), blocked.ID)
	if err != nil || resumed.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("post-effect hold retry attempt=%+v error=%v, want tombstoning", resumed, err)
	}
	assertLifecyclePointState(t, fixture.db, fixture.pointID, backupasset.RecoveryPointPurgeBlocked)
	if fixture.deleter.calls != 1 {
		t.Fatalf("post-effect hold retry provider calls=%d, want no replay", fixture.deleter.calls)
	}
	completed, err := fixture.coordinator.Advance(context.Background(), resumed.ID)
	if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete {
		t.Fatalf("post-effect hold completion attempt=%+v error=%v", completed, err)
	}
	if fixture.deleter.calls != 1 {
		t.Fatalf("post-effect hold completion provider calls=%d, want no replay", fixture.deleter.calls)
	}
	assertLifecyclePointState(t, fixture.db, fixture.pointID, backupasset.RecoveryPointExpired)
}

func TestLifecycleLateHoldReceiptSettledAuditUsesProviderResult(t *testing.T) {
	tests := []struct {
		name      string
		base      uint64
		outcome   PointDeletionOutcome
		digest    string
		wantAudit string
	}{
		{
			name: "deleted", base: 1800, outcome: PointDeletionDeleted,
			digest: strings.Repeat("a", 64), wantAudit: "deleted",
		},
		{
			name: "already_absent", base: 1810, outcome: PointDeletionAlreadyAbsent,
			digest: strings.Repeat("b", 64), wantAudit: "already_absent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, test.base)
			audit := &recordingSettledAudit{}
			fixture.coordinator.audit = audit
			fixture.deleter.result = PointDeletionResult{
				Outcome: test.outcome, ReceiptDigest: test.digest,
			}
			holdID := testOpaqueID(test.base + 4)
			fixture.deleter.afterEffect = func() {
				if err := fixture.db.Create(&model.RecoveryPointHold{
					ID: holdID, RecoveryPointID: fixture.pointID,
					HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
					EncryptedReason: "late active hold", CreatedBy: 1,
					CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
				}).Error; err != nil {
					t.Errorf("create late active hold: %v", err)
					return
				}
				if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).
					Updates(map[string]any{
						"hold_state": backupasset.HoldActive, "point_revision": 32, "updated_at": fixture.clock,
					}).Error; err != nil {
					t.Errorf("project late active hold: %v", err)
				}
			}
			attempt := fixture.attempt
			for _, want := range []backupasset.LifecyclePhase{
				backupasset.LifecyclePhaseRevoking,
				backupasset.LifecyclePhaseDraining,
				backupasset.LifecyclePhaseCleaning,
				backupasset.LifecyclePhaseProviderDelete,
			} {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil || attempt.Phase != want {
					t.Fatalf("advance to provider deletion want=%q attempt=%+v error=%v", want, attempt, err)
				}
			}
			blocked, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
				blocked.BlockedReason != backupasset.LifecycleBlockedActiveHold {
				t.Fatalf("late-hold attempt=%+v error=%v, want blocked/active_hold", blocked, err)
			}
			if len(audit.events) != 0 {
				t.Fatalf("late-hold audit before retry writes=%d, want 0", len(audit.events))
			}

			fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
			blocked, err = fixture.coordinator.Advance(context.Background(), blocked.ID)
			if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
				blocked.BlockedReason != backupasset.LifecycleBlockedActiveHold {
				t.Fatalf("late-hold audit tick attempt=%+v error=%v, want blocked/active_hold", blocked, err)
			}
			if len(audit.events) != 1 {
				t.Fatalf("late-hold audit writes=%d, want one", len(audit.events))
			}
			event := audit.events[0]
			if event.Outcome != backupasset.AuditOutcomeSuccess ||
				event.Fields[backupasset.AuditFieldStatus] != test.wantAudit {
				t.Fatalf("late-hold settled audit=%+v fields=%+v, want successful %q", event, event.Fields, test.wantAudit)
			}

			fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
			blocked, err = fixture.coordinator.Advance(context.Background(), blocked.ID)
			if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
				blocked.BlockedReason != backupasset.LifecycleBlockedActiveHold {
				t.Fatalf("repeated late-hold audit tick attempt=%+v error=%v, want blocked/active_hold", blocked, err)
			}
			if len(audit.events) != 1 {
				t.Fatalf("repeated late-hold audit writes=%d, want one successful audit", len(audit.events))
			}
		})
	}
}

func TestLifecyclePreEffectActiveHoldSettledAuditRemainsBlocked(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 1830)
	audit := &recordingSettledAudit{}
	fixture.coordinator.audit = audit
	attempt, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseRevoking {
		t.Fatalf("advance pre-effect hold to revoking attempt=%+v error=%v", attempt, err)
	}
	holdID := testOpaqueID(1834)
	if err := fixture.db.Create(&model.RecoveryPointHold{
		ID: holdID, RecoveryPointID: fixture.pointID,
		HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
		EncryptedReason: "pre-effect active hold", CreatedBy: 1,
		CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
	}).Error; err != nil {
		t.Fatalf("create pre-effect active hold: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).
		Updates(map[string]any{
			"hold_state": backupasset.HoldActive, "point_revision": 31, "updated_at": fixture.clock,
		}).Error; err != nil {
		t.Fatalf("project pre-effect active hold: %v", err)
	}

	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked ||
		attempt.BlockedReason != backupasset.LifecycleBlockedActiveHold {
		t.Fatalf("pre-effect hold attempt=%+v error=%v, want blocked/active_hold", attempt, err)
	}
	if fixture.deleter.calls != 0 {
		t.Fatalf("pre-effect hold provider calls=%d, want zero", fixture.deleter.calls)
	}

	fixture.clock = attempt.RetryAt.UTC().Add(time.Second)
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked ||
		attempt.BlockedReason != backupasset.LifecycleBlockedActiveHold {
		t.Fatalf("pre-effect hold audit tick attempt=%+v error=%v, want blocked/active_hold", attempt, err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("pre-effect hold audit writes=%d, want one blocked audit", len(audit.events))
	}
	if audit.events[0].Outcome != backupasset.AuditOutcomeBlocked ||
		audit.events[0].Fields[backupasset.AuditFieldStatus] != "blocked" {
		t.Fatalf("pre-effect hold settled audit=%+v fields=%+v, want blocked", audit.events[0], audit.events[0].Fields)
	}

	fixture.clock = attempt.RetryAt.UTC().Add(time.Second)
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked ||
		attempt.BlockedReason != backupasset.LifecycleBlockedActiveHold {
		t.Fatalf("repeated pre-effect hold tick attempt=%+v error=%v, want blocked/active_hold", attempt, err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("repeated pre-effect hold audit writes=%d, want one blocked audit", len(audit.events))
	}
	var tombstoneCount int64
	if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
		Where("recovery_point_id = ?", fixture.pointID).Count(&tombstoneCount).Error; err != nil {
		t.Fatalf("count pre-effect hold tombstones: %v", err)
	}
	if tombstoneCount != 0 {
		t.Fatalf("pre-effect hold tombstones=%d, want zero", tombstoneCount)
	}
}

func TestLifecycleMutableRetireActiveHoldAfterTerminalEventResumesTombstoning(t *testing.T) {
	fixture := newTerminalEventRestartFixture(t, backupasset.LifecycleMutableRetire, 1240)
	audit := &recordingSettledAudit{}
	fixture.coordinator.audit = audit

	var persistedEvent model.RecoveryPointLifecycleTombstone
	if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?",
		fixture.pointID, backupasset.LifecycleMutableRetire).Limit(1).Find(&persistedEvent).Error; err != nil {
		t.Fatalf("load persisted mutable-retire terminal event: %v", err)
	}
	if persistedEvent.RecoveryPointID == "" || persistedEvent.ResultCode != "mutable_retired" ||
		persistedEvent.DeletionReceiptDigest != nil {
		t.Fatalf("mutable-retire terminal event=%+v, want valid receipt-free event", persistedEvent)
	}

	holdID := testOpaqueID(1244)
	if err := fixture.db.Create(&model.RecoveryPointHold{
		ID: holdID, RecoveryPointID: fixture.pointID,
		HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
		EncryptedReason: "mutable-retire tombstoning hold", CreatedBy: 1,
		CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
	}).Error; err != nil {
		t.Fatalf("create mutable-retire tombstoning hold: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).
		Updates(map[string]any{
			"hold_state": backupasset.HoldActive, "point_revision": 11, "updated_at": fixture.clock,
		}).Error; err != nil {
		t.Fatalf("project mutable-retire tombstoning hold: %v", err)
	}

	blocked, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
		blocked.BlockedReason != backupasset.LifecycleBlockedActiveHold {
		t.Fatalf("mutable-retire tombstoning hold attempt=%+v error=%v, want blocked/active_hold", blocked, err)
	}
	if len(audit.events) != 0 {
		t.Fatalf("mutable-retire tombstoning hold audit before retry=%d, want zero", len(audit.events))
	}

	fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
	if err := fixture.db.Model(&model.RecoveryPointHold{}).Where("id = ?", holdID).Updates(map[string]any{
		"state": backupasset.HoldReleased, "released_by": uint(1),
		"released_at": fixture.clock, "updated_at": fixture.clock,
	}).Error; err != nil {
		t.Fatalf("release mutable-retire tombstoning hold: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).
		Updates(map[string]any{
			"hold_state": backupasset.HoldReleased, "point_revision": 12, "updated_at": fixture.clock,
		}).Error; err != nil {
		t.Fatalf("project mutable-retire tombstoning hold release: %v", err)
	}

	resumed, err := fixture.coordinator.Advance(context.Background(), blocked.ID)
	if err != nil || resumed.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("mutable-retire tombstoning hold retry attempt=%+v error=%v, want tombstoning", resumed, err)
	}
	if len(audit.events) != 0 {
		t.Fatalf("mutable-retire tombstoning hold emitted settled deletion audit=%d, want zero", len(audit.events))
	}

	completed, err := fixture.coordinator.Advance(context.Background(), resumed.ID)
	if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete {
		t.Fatalf("mutable-retire tombstoning hold completion attempt=%+v error=%v", completed, err)
	}
	if fixture.cleanup.calls != 1 || fixture.deleter.calls != 0 {
		t.Fatalf("mutable-retire tombstoning hold effects=%d/%d, want cleanup 1/provider delete 0",
			fixture.cleanup.calls, fixture.deleter.calls)
	}
	assertLifecyclePointState(t, fixture.db, fixture.pointID, backupasset.RecoveryPointRetired)

	var terminalEvents []model.RecoveryPointLifecycleTombstone
	if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?",
		fixture.pointID, backupasset.LifecycleMutableRetire).Find(&terminalEvents).Error; err != nil {
		t.Fatalf("load mutable-retire terminal event history: %v", err)
	}
	if len(terminalEvents) != 1 || !terminalEvents[0].CreatedAt.Equal(persistedEvent.CreatedAt) {
		t.Fatalf("mutable-retire terminal event history=%+v, want original single event", terminalEvents)
	}
}

func TestLifecycleActiveHoldMalformedProviderEvidenceDoesNotAuditSuccess(t *testing.T) {
	tests := []struct {
		name      string
		operation backupasset.LifecycleOperation
		base      uint64
		corrupt   func(*gorm.DB, string) error
	}{
		{
			name: "retention expiry missing receipt", operation: backupasset.LifecycleRetentionExpire, base: 1260,
			corrupt: func(db *gorm.DB, pointID string) error {
				return db.Model(&model.RecoveryPointLifecycleTombstone{}).
					Where("recovery_point_id = ? AND terminal_operation = ?", pointID, backupasset.LifecycleRetentionExpire).
					UpdateColumn("deletion_receipt_digest", nil).Error
			},
		},
		{
			name: "explicit purge malformed receipt", operation: backupasset.LifecycleExplicitPurge, base: 1280,
			corrupt: func(db *gorm.DB, pointID string) error {
				return db.Model(&model.RecoveryPointLifecycleTombstone{}).
					Where("recovery_point_id = ? AND terminal_operation = ?", pointID, backupasset.LifecycleExplicitPurge).
					UpdateColumn("deletion_receipt_digest", "invalid").Error
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTerminalEventRestartFixture(t, test.operation, test.base)
			audit := &recordingSettledAudit{}
			fixture.coordinator.audit = audit
			holdID := testOpaqueID(test.base + 4)
			if err := fixture.db.Create(&model.RecoveryPointHold{
				ID: holdID, RecoveryPointID: fixture.pointID,
				HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
				EncryptedReason: "malformed provider evidence hold", CreatedBy: 1,
				CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
			}).Error; err != nil {
				t.Fatalf("create malformed provider evidence hold: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).
				Updates(map[string]any{"hold_state": backupasset.HoldActive, "updated_at": fixture.clock}).Error; err != nil {
				t.Fatalf("project malformed provider evidence hold: %v", err)
			}
			blocked, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
			if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
				blocked.BlockedReason != backupasset.LifecycleBlockedActiveHold {
				t.Fatalf("malformed provider evidence block attempt=%+v error=%v, want blocked/active_hold", blocked, err)
			}
			if err := test.corrupt(fixture.db, fixture.pointID); err != nil {
				t.Fatalf("corrupt provider terminal evidence: %v", err)
			}

			fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
			restarted := restartTerminalEventCoordinator(t, fixture, test.base+10)
			restarted.audit = audit
			retried, err := restarted.Advance(context.Background(), blocked.ID)
			if !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("malformed provider evidence retry error=%v, want ErrInvalidState", err)
			}
			if retried.Phase != backupasset.LifecyclePhaseBlocked ||
				retried.BlockedReason != backupasset.LifecycleBlockedActiveHold {
				t.Fatalf("malformed provider evidence retry attempt=%+v, want blocked/active_hold", retried)
			}
			if len(audit.events) != 0 {
				for _, event := range audit.events {
					status, _ := event.Fields[backupasset.AuditFieldStatus].(string)
					if event.Outcome == backupasset.AuditOutcomeSuccess ||
						status == "deleted" || status == "already_absent" {
						t.Fatalf("malformed provider evidence emitted success audit=%+v", event)
					}
				}
				t.Fatalf("malformed provider evidence audit events=%+v, want no audit", audit.events)
			}
		})
	}
}

func TestLifecycleNativeReservationLinearizesPublicationAtRepositoryLock(t *testing.T) {
	fixture := newClaimedExpiryFixtureWithDB(t, newLifecycleCoordinatorPostgresTestDB(t), 1660)
	attempt := fixture.attempt
	var err error
	for attempt.Phase != backupasset.LifecyclePhaseCleaning {
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance native reservation fixture to cleaning: %v", err)
		}
	}

	var point model.RecoveryPoint
	if err := fixture.db.Select("repository_id").First(&point, "id = ?", fixture.pointID).Error; err != nil {
		t.Fatalf("load native reservation repository: %v", err)
	}
	repositoryID := point.RepositoryID
	type lockProbeKey struct{}
	repositoryLocked := make(chan struct{})
	releaseReservation := make(chan struct{})
	var repositoryLockCalls int
	var repositoryLockMu sync.Mutex
	if err := fixture.db.Callback().Query().After("gorm:query").Register("test:native_reservation_lock", func(callbackDB *gorm.DB) {
		label, _ := callbackDB.Statement.Context.Value(lockProbeKey{}).(string)
		if label != "reservation" {
			return
		}
		if _, locked := callbackDB.Statement.Clauses["FOR"]; !locked {
			return
		}
		if _, isRepository := callbackDB.Statement.Dest.(*model.BackupRepository); !isRepository {
			return
		}
		repositoryLockMu.Lock()
		repositoryLockCalls++
		lockCall := repositoryLockCalls
		repositoryLockMu.Unlock()
		if lockCall != 2 {
			return
		}
		close(repositoryLocked)
		<-releaseReservation
	}); err != nil {
		t.Fatalf("register native reservation lock probe: %v", err)
	}
	t.Cleanup(func() { _ = fixture.db.Callback().Query().Remove("test:native_reservation_lock") })

	advanceDone := make(chan struct {
		attempt LifecycleAttempt
		err     error
	}, 1)
	go func() {
		advanced, advanceErr := fixture.coordinator.Advance(
			context.WithValue(context.Background(), lockProbeKey{}, "reservation"), attempt.ID,
		)
		advanceDone <- struct {
			attempt LifecycleAttempt
			err     error
		}{advanced, advanceErr}
	}()
	select {
	case <-repositoryLocked:
	case <-time.After(2 * time.Second):
		t.Fatal("native reservation transition did not lock repository")
	}

	type publicationCheckResult struct {
		reservations int64
		err          error
	}
	publicationDone := make(chan publicationCheckResult, 1)
	go func() {
		var result publicationCheckResult
		result.err = fixture.db.Transaction(func(tx *gorm.DB) error {
			var repository model.BackupRepository
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				First(&repository, "id = ?", repositoryID).Error; err != nil {
				return err
			}
			result.err = tx.Model(&model.RecoveryPointLifecycleAttempt{}).
				Joins("JOIN recovery_points ON recovery_points.id = recovery_point_lifecycle_attempts.recovery_point_id").
				Where("recovery_points.repository_id = ? AND recovery_point_lifecycle_attempts.phase = ?",
					repository.ID, backupasset.LifecyclePhaseProviderDelete).
				Count(&result.reservations).Error
			return result.err
		})
		publicationDone <- result
	}()
	select {
	case result := <-publicationDone:
		close(releaseReservation)
		advanced := <-advanceDone
		t.Fatalf("publication check passed before reservation transition result=%+v advance=%+v", result, advanced)
	case <-time.After(250 * time.Millisecond):
	}
	close(releaseReservation)
	advanced := <-advanceDone
	if advanced.err != nil || advanced.attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		t.Fatalf("native reservation transition attempt=%+v error=%v, want provider_delete", advanced.attempt, advanced.err)
	}
	result := <-publicationDone
	if result.err != nil || result.reservations != 1 {
		t.Fatalf("publication check after reservation result=%+v, want one reservation", result)
	}
}

func TestLifecycleMutableRetireTerminalEventRestartSkipsEffects(t *testing.T) {
	assertTerminalEventRestartSkipsEffects(t, backupasset.LifecycleMutableRetire, 1100)
}

func TestLifecycleRetentionExpireTerminalEventRestartSkipsEffects(t *testing.T) {
	assertTerminalEventRestartSkipsEffects(t, backupasset.LifecycleRetentionExpire, 1120)
}

func TestLifecycleExplicitPurgeTerminalEventRestartSkipsEffects(t *testing.T) {
	assertTerminalEventRestartSkipsEffects(t, backupasset.LifecycleExplicitPurge, 1140)
}

func TestLifecycleTerminalEventMismatchFailsClosed(t *testing.T) {
	tests := []struct {
		name      string
		operation backupasset.LifecycleOperation
		base      uint64
		corrupt   func(*gorm.DB, string) error
	}{
		{
			name: "mutable retirement timestamp", operation: backupasset.LifecycleMutableRetire, base: 1160,
			corrupt: func(db *gorm.DB, pointID string) error {
				return db.Model(&model.RecoveryPointLifecycleTombstone{}).
					Where("recovery_point_id = ? AND terminal_operation = ?", pointID, backupasset.LifecycleMutableRetire).
					UpdateColumn("retired_at", nil).Error
			},
		},
		{
			name: "retention deletion receipt", operation: backupasset.LifecycleRetentionExpire, base: 1180,
			corrupt: func(db *gorm.DB, pointID string) error {
				return db.Model(&model.RecoveryPointLifecycleTombstone{}).
					Where("recovery_point_id = ? AND terminal_operation = ?", pointID, backupasset.LifecycleRetentionExpire).
					UpdateColumn("deletion_receipt_digest", "invalid").Error
			},
		},
		{
			name: "explicit purge terminal state", operation: backupasset.LifecycleExplicitPurge, base: 1200,
			corrupt: func(db *gorm.DB, pointID string) error {
				return db.Model(&model.RecoveryPointLifecycleTombstone{}).
					Where("recovery_point_id = ? AND terminal_operation = ?", pointID, backupasset.LifecycleExplicitPurge).
					UpdateColumn("terminal_state", backupasset.RecoveryPointRetired).Error
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTerminalEventRestartFixture(t, test.operation, test.base)
			blocked := blockTerminalEventForValidation(t, fixture)
			if err := test.corrupt(fixture.db, fixture.pointID); err != nil {
				t.Fatalf("inject terminal event mismatch: %v", err)
			}
			fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
			cleanupBefore, deleteBefore := fixture.cleanup.calls, fixture.deleter.calls
			_, err := restartTerminalEventCoordinator(t, fixture, test.base+11).Advance(context.Background(), blocked.ID)
			if !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("mismatched terminal event retry error=%v, want ErrInvalidState", err)
			}
			var persisted model.RecoveryPointLifecycleAttempt
			if err := fixture.db.First(&persisted, "id = ?", blocked.ID).Error; err != nil {
				t.Fatalf("load mismatched terminal event attempt: %v", err)
			}
			if persisted.Phase != string(backupasset.LifecyclePhaseBlocked) ||
				fixture.cleanup.calls != cleanupBefore || fixture.deleter.calls != deleteBefore {
				t.Fatalf("mismatched event phase/effect deltas=%q/%d/%d, want blocked/0/0",
					persisted.Phase, fixture.cleanup.calls-cleanupBefore, fixture.deleter.calls-deleteBefore)
			}
		})
	}
}

func TestLifecycleMissingTerminalEventRestartsEarliestSafePhase(t *testing.T) {
	fixture := newTerminalEventRestartFixture(t, backupasset.LifecycleRetentionExpire, 1220)
	blocked := blockTerminalEventForValidation(t, fixture)
	if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?",
		fixture.pointID, backupasset.LifecycleRetentionExpire).
		Delete(&model.RecoveryPointLifecycleTombstone{}).Error; err != nil {
		t.Fatalf("remove terminal event for missing-event recovery: %v", err)
	}
	if err := fixture.db.Where("attempt_id = ?", blocked.ID).
		Delete(&model.RecoveryPointLifecycleEffectClaim{}).Error; err != nil {
		t.Fatalf("remove provider claim for missing-event recovery: %v", err)
	}
	fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
	cleanupBefore, deleteBefore := fixture.cleanup.calls, fixture.deleter.calls
	retried, err := restartTerminalEventCoordinator(t, fixture, 1231).Advance(context.Background(), blocked.ID)
	if err != nil || retried.Phase != backupasset.LifecyclePhaseBlocked ||
		retried.BlockedReason != backupasset.LifecycleBlockedFenceLost ||
		fixture.cleanup.calls != cleanupBefore || fixture.deleter.calls != deleteBefore {
		t.Fatalf("missing event retry phase/reason/effect deltas=%q/%q/%d/%d error=%v, want blocked/fence_lost/0/0",
			retried.Phase, retried.BlockedReason, fixture.cleanup.calls-cleanupBefore, fixture.deleter.calls-deleteBefore, err)
	}
}

func TestSelectedAttemptAbsoluteDeadlineDurablyBlocksAndRestartsWithFreshFence(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 980)
	var oldLease model.RecoveryPointLease
	if err := fixture.db.First(&oldLease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
		t.Fatalf("load selected attempt lease: %v", err)
	}
	oldFence := backupasset.LeaseFence{
		LeaseID: oldLease.ID, RecoveryPointID: oldLease.RecoveryPointID,
		HolderType: backupasset.LeaseHolderType(oldLease.HolderType), OwnerID: oldLease.OwnerID,
		AttemptID: oldLease.AttemptID, FenceToken: oldLease.FenceToken,
	}
	fixture.clock = oldLease.AbsoluteDeadline.UTC().Add(time.Second)
	blocked, err := newRestartedExpiryCoordinator(t, fixture, 984).Advance(context.Background(), fixture.attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
		blocked.BlockedReason != backupasset.LifecycleBlockedFenceLost || blocked.TransitionRevision != 2 || blocked.RetryAt == nil {
		t.Fatalf("selected absolute-deadline block phase/reason/revision=%q/%q/%d error=%v, want blocked/fence_lost/2",
			blocked.Phase, blocked.BlockedReason, blocked.TransitionRevision, err)
	}
	var blockedPoint model.RecoveryPoint
	if err := fixture.db.First(&blockedPoint, "id = ?", fixture.pointID).Error; err != nil {
		t.Fatalf("load selected absolute-deadline point: %v", err)
	}
	if blockedPoint.State != string(backupasset.RecoveryPointPurgeBlocked) || blockedPoint.PointRevision != 32 {
		t.Fatalf("selected absolute-deadline point state/revision=%s/%d, want purge_blocked/32",
			blockedPoint.State, blockedPoint.PointRevision)
	}
	if err := fixture.coordinator.leases.ValidateFence(context.Background(), oldFence); err == nil {
		t.Fatal("selected absolute-deadline old fence remained usable")
	}
	replayed, err := newRestartedExpiryCoordinator(t, fixture, 985).Advance(context.Background(), blocked.ID)
	if err != nil || replayed.Phase != backupasset.LifecyclePhaseBlocked || replayed.TransitionRevision != blocked.TransitionRevision ||
		replayed.LeaseID != blocked.LeaseID {
		t.Fatalf("pre-retry selected block replay phase/reason/revision=%q/%q/%d error=%v",
			replayed.Phase, replayed.BlockedReason, replayed.TransitionRevision, err)
	}
	fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
	adopted, err := newRestartedExpiryCoordinator(t, fixture, 986).Advance(context.Background(), blocked.ID)
	if err != nil || adopted.Phase != backupasset.LifecyclePhaseRevoking || adopted.TransitionRevision != 4 ||
		adopted.LeaseID == oldLease.ID || adopted.LeaseAttemptID == oldLease.AttemptID ||
		adopted.LeaseFenceTokenHash == hashFenceToken(oldLease.FenceToken) {
		t.Fatalf("selected absolute-deadline fresh adoption phase/revision=%q/%d error=%v",
			adopted.Phase, adopted.TransitionRevision, err)
	}
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionAlreadyAbsent, ReceiptDigest: strings.Repeat("8", 64),
	}
	for adopted.Phase != backupasset.LifecyclePhaseComplete {
		adopted, err = newRestartedExpiryCoordinator(t, fixture, 987).Advance(context.Background(), adopted.ID)
		if err != nil {
			t.Fatalf("restart selected absolute-deadline attempt phase=%q: %v", adopted.Phase, err)
		}
	}
	if fixture.cleanup.calls != 1 || fixture.deleter.calls != 1 {
		t.Fatalf("selected absolute-deadline completion effects=%d/%d, want 1/1", fixture.cleanup.calls, fixture.deleter.calls)
	}
	payload, err := json.Marshal(struct {
		Blocked LifecycleAttempt `json:"blocked"`
		Adopted LifecycleAttempt `json:"adopted"`
	}{blocked, adopted})
	if err != nil {
		t.Fatalf("marshal selected deadline attempts: %v", err)
	}
	if strings.Contains(string(payload), oldLease.FenceToken) || strings.Contains(string(payload), "fence_token") ||
		strings.Contains(string(payload), "lease_attempt") {
		t.Fatal("selected deadline lifecycle payload exposed private fence material")
	}
	formatted := fmt.Sprintf("%+v", blocked)
	if strings.Contains(formatted, blocked.LeaseFenceTokenHash) || strings.Contains(formatted, oldLease.AttemptID) ||
		strings.Contains(formatted, oldLease.ID) {
		t.Fatal("selected deadline lifecycle formatting exposed private lease or fence material")
	}
}

func TestDrainingAttemptAbsoluteDeadlineDurablyBlocksFenceLost(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 990)
	attempt := fixture.attempt
	var err error
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
	} {
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("advance draining deadline fixture want=%q phase=%q revision=%d error=%v",
				want, attempt.Phase, attempt.TransitionRevision, err)
		}
	}
	var lease model.RecoveryPointLease
	if err := fixture.db.First(&lease, "id = ?", attempt.LeaseID).Error; err != nil {
		t.Fatalf("load draining deadline lease: %v", err)
	}
	fixture.clock = lease.AbsoluteDeadline.UTC().Add(time.Second)
	blocked, err := newRestartedExpiryCoordinator(t, fixture, 994).Advance(context.Background(), attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
		blocked.BlockedReason != backupasset.LifecycleBlockedFenceLost {
		t.Fatalf("draining absolute-deadline phase/reason/revision=%q/%q/%d error=%v, want blocked/fence_lost",
			blocked.Phase, blocked.BlockedReason, blocked.TransitionRevision, err)
	}
}

func TestLifecycleBlockedAttemptAdoptsFreshFenceAfterAbsoluteDeadline(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 740)
	fixture.deleter.prepareErr = backupasset.ErrProviderUnavailable
	attempt := fixture.attempt
	var err error
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete,
		backupasset.LifecyclePhaseBlocked,
	} {
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("advance outage fixture want=%q attempt=%+v error=%v", want, attempt, err)
		}
	}
	if attempt.BlockedReason != backupasset.LifecycleBlockedProviderUnavailable {
		t.Fatalf("outage block reason=%q, want provider_unavailable", attempt.BlockedReason)
	}
	var oldLease model.RecoveryPointLease
	if err := fixture.db.First(&oldLease, "id = ?", attempt.LeaseID).Error; err != nil {
		t.Fatalf("load old lifecycle lease: %v", err)
	}
	oldFence := backupasset.LeaseFence{
		LeaseID: oldLease.ID, RecoveryPointID: oldLease.RecoveryPointID,
		HolderType: backupasset.LeaseHolderType(oldLease.HolderType), OwnerID: oldLease.OwnerID,
		AttemptID: oldLease.AttemptID, FenceToken: oldLease.FenceToken,
	}
	fixture.clock = oldLease.AbsoluteDeadline.UTC().Add(24 * time.Hour)
	fixture.deleter.prepareErr = nil
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionAlreadyAbsent, ReceiptDigest: strings.Repeat("b", 64),
	}
	newCoordinator := func() *Coordinator {
		coordinator, coordinatorErr := NewCoordinator(CoordinatorDependencies{
			DB: fixture.db, Leases: fixture.coordinator.leases, Holds: fixture.holds, Now: func() time.Time { return fixture.clock },
			NewID:        func() (string, error) { return testOpaqueID(749), nil },
			LeaseOwnerID: "retention-worker-provider-test", Admissions: &lifecycleAdmissionFake{},
			Cleanup: fixture.cleanup, Deleter: fixture.deleter, RetryDelay: time.Minute,
		})
		if coordinatorErr != nil {
			t.Fatalf("restart lifecycle coordinator: %v", coordinatorErr)
		}
		return coordinator
	}
	restarted := newCoordinator()
	adopted, err := restarted.Advance(context.Background(), attempt.ID)
	if err != nil || adopted.Phase != backupasset.LifecyclePhaseProviderDelete ||
		adopted.LeaseID == oldLease.ID || adopted.LeaseAttemptID == oldLease.AttemptID ||
		adopted.LeaseFenceTokenHash == hashFenceToken(oldLease.FenceToken) {
		t.Fatalf("fresh fence adoption attempt=%+v error=%v", adopted, err)
	}
	if err := fixture.coordinator.leases.ValidateFence(context.Background(), oldFence); err == nil {
		t.Fatalf("old absolute-deadline fence remained reusable")
	}
	var persistedOldLease model.RecoveryPointLease
	if err := fixture.db.First(&persistedOldLease, "id = ?", oldLease.ID).Error; err != nil {
		t.Fatalf("reload old lifecycle lease: %v", err)
	}
	if persistedOldLease.Status != string(backupasset.LeaseExpired) {
		t.Fatalf("old lifecycle lease status=%q, want expired", persistedOldLease.Status)
	}

	restarted = newCoordinator()
	adopted, err = restarted.Advance(context.Background(), adopted.ID)
	if err != nil || adopted.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("restart after fence adoption attempt=%+v error=%v", adopted, err)
	}
	adopted, err = newCoordinator().Advance(context.Background(), adopted.ID)
	if err != nil || adopted.Phase != backupasset.LifecyclePhaseComplete || fixture.deleter.calls != 1 {
		t.Fatalf("complete adopted lifecycle attempt=%+v delete_calls=%d error=%v", adopted, fixture.deleter.calls, err)
	}
}

func TestLifecycleBlockedAttemptHasExactlyOnePostgresFenceAdopter(t *testing.T) {
	fixture := newClaimedExpiryFixtureWithDB(t, newLifecycleCoordinatorPostgresTestDB(t), 900)
	fixture.deleter.prepareErr = backupasset.ErrProviderUnavailable
	attempt := fixture.attempt
	var err error
	for attempt.Phase != backupasset.LifecyclePhaseBlocked {
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance PostgreSQL adoption fixture: %v", err)
		}
	}
	var oldLease model.RecoveryPointLease
	if err := fixture.db.First(&oldLease, "id = ?", attempt.LeaseID).Error; err != nil {
		t.Fatalf("load PostgreSQL old lifecycle lease: %v", err)
	}
	fixture.clock = oldLease.AbsoluteDeadline.UTC().Add(time.Hour)
	newCoordinator := func() *Coordinator {
		coordinator, coordinatorErr := NewCoordinator(CoordinatorDependencies{
			DB: fixture.db, Leases: fixture.coordinator.leases, Holds: fixture.holds, Now: func() time.Time { return fixture.clock },
			NewID: func() (string, error) { return testOpaqueID(909), nil }, LeaseOwnerID: "retention-worker-provider-test",
			Admissions: &lifecycleAdmissionFake{}, Cleanup: fixture.cleanup, Deleter: fixture.deleter, RetryDelay: time.Minute,
		})
		if coordinatorErr != nil {
			t.Fatalf("restart PostgreSQL lifecycle coordinator: %v", coordinatorErr)
		}
		return coordinator
	}
	coordinators := []*Coordinator{newCoordinator(), newCoordinator()}
	start := make(chan struct{})
	results := make(chan error, len(coordinators))
	for _, coordinator := range coordinators {
		go func(current *Coordinator) {
			<-start
			_, retryErr := current.retryBlocked(context.Background(), attempt.ID)
			results <- retryErr
		}(coordinator)
	}
	close(start)
	successes := 0
	conflicts := 0
	for range coordinators {
		resultErr := <-results
		switch {
		case resultErr == nil:
			successes++
		case errors.Is(resultErr, backupasset.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent adopter error: %v", resultErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("PostgreSQL adopters successes/conflicts=%d/%d, want 1/1", successes, conflicts)
	}
	var activeLeases int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ? AND status = ?",
			fixture.pointID, backupasset.LeaseHolderRetentionWorker, backupasset.LeaseActive).
		Count(&activeLeases).Error; err != nil {
		t.Fatalf("count active adopted lifecycle leases: %v", err)
	}
	if activeLeases != 1 {
		t.Fatalf("active adopted lifecycle leases=%d, want exactly one", activeLeases)
	}
	var persistedAttempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&persistedAttempt, "id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load adopted PostgreSQL lifecycle attempt: %v", err)
	}
	if persistedAttempt.LeaseID == nil || *persistedAttempt.LeaseID == oldLease.ID {
		t.Fatalf("PostgreSQL lifecycle attempt did not adopt exactly one fresh lease")
	}
}

func TestSelectedAbsoluteDeadlineHasExactlyOnePostgresFenceAdopter(t *testing.T) {
	fixture := newClaimedExpiryFixtureWithDB(t, newLifecycleCoordinatorPostgresTestDB(t), 1000)
	var oldLease model.RecoveryPointLease
	if err := fixture.db.First(&oldLease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
		t.Fatalf("load PostgreSQL selected old lease: %v", err)
	}
	oldFence := backupasset.LeaseFence{
		LeaseID: oldLease.ID, RecoveryPointID: oldLease.RecoveryPointID,
		HolderType: backupasset.LeaseHolderType(oldLease.HolderType), OwnerID: oldLease.OwnerID,
		AttemptID: oldLease.AttemptID, FenceToken: oldLease.FenceToken,
	}
	fixture.clock = oldLease.AbsoluteDeadline.UTC().Add(time.Second)
	blocked, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
		blocked.BlockedReason != backupasset.LifecycleBlockedFenceLost || blocked.RetryAt == nil {
		t.Fatalf("PostgreSQL selected deadline block phase/reason=%q/%q error=%v",
			blocked.Phase, blocked.BlockedReason, err)
	}
	fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
	coordinators := []*Coordinator{
		newRestartedExpiryCoordinator(t, fixture, 1004),
		newRestartedExpiryCoordinator(t, fixture, 1005),
	}
	start := make(chan struct{})
	results := make(chan error, len(coordinators))
	for _, coordinator := range coordinators {
		go func(current *Coordinator) {
			<-start
			_, retryErr := current.retryBlocked(context.Background(), blocked.ID)
			results <- retryErr
		}(coordinator)
	}
	close(start)
	successes := 0
	conflicts := 0
	for range coordinators {
		switch resultErr := <-results; {
		case resultErr == nil:
			successes++
		case errors.Is(resultErr, backupasset.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected PostgreSQL selected adopter error: %v", resultErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("PostgreSQL selected adopters successes/conflicts=%d/%d, want 1/1", successes, conflicts)
	}
	var activeLeases int64
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("recovery_point_id = ? AND holder_type = ? AND status = ?",
			fixture.pointID, backupasset.LeaseHolderRetentionWorker, backupasset.LeaseActive).
		Count(&activeLeases).Error; err != nil {
		t.Fatalf("count selected adopted lifecycle leases: %v", err)
	}
	if activeLeases != 1 {
		t.Fatalf("selected adopted active leases=%d, want exactly one", activeLeases)
	}
	var persisted model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&persisted, "id = ?", blocked.ID).Error; err != nil {
		t.Fatalf("load selected adopted lifecycle attempt: %v", err)
	}
	if persisted.Phase != string(backupasset.LifecyclePhaseRevoking) || persisted.TransitionRevision != 4 ||
		persisted.LeaseID == nil || *persisted.LeaseID == oldLease.ID {
		t.Fatalf("selected adopted persisted phase/revision=%q/%d fresh_lease=%t, want revoking/4/true",
			persisted.Phase, persisted.TransitionRevision, persisted.LeaseID != nil && *persisted.LeaseID != oldLease.ID)
	}
	if err := fixture.coordinator.leases.ValidateFence(context.Background(), oldFence); err == nil {
		t.Fatal("PostgreSQL selected old fence remained usable after adoption")
	}
}

func TestLifecycleTerminalEventRestartPostgresSkipsEffects(t *testing.T) {
	fixture := newTerminalEventRestartFixtureWithDB(
		t,
		newLifecycleCoordinatorPostgresTestDB(t),
		backupasset.LifecycleRetentionExpire,
		1240,
	)
	assertTerminalEventRestartFixtureSkipsEffects(t, fixture, 1240)
}

func TestLifecycleFenceLossIsDurableAndNeverExposesFenceMaterial(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 750)
	var lease model.RecoveryPointLease
	if err := fixture.db.First(&lease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
		t.Fatalf("load lifecycle lease for fence-loss injection: %v", err)
	}
	privateReplacement := strings.Repeat("9", 64)
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", lease.ID).Updates(map[string]any{
		"attempt_id": testOpaqueID(759), "fence_token": privateReplacement,
	}).Error; err != nil {
		t.Fatalf("replace lifecycle fence for test: %v", err)
	}
	blocked, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
		blocked.BlockedReason != backupasset.LifecycleBlockedFenceLost {
		t.Fatalf("fence-loss attempt=%+v error=%v, want blocked/fence_lost", blocked, err)
	}
	assertLifecyclePointState(t, fixture.db, fixture.pointID, backupasset.RecoveryPointPurgeBlocked)
	payload, err := json.Marshal(blocked)
	if err != nil {
		t.Fatalf("marshal blocked lifecycle attempt: %v", err)
	}
	if strings.Contains(string(payload), privateReplacement) || strings.Contains(string(payload), "fence_token") ||
		strings.Contains(string(payload), "lease_attempt") || strings.Contains(string(payload), "lease_id") {
		t.Fatalf("blocked lifecycle payload exposed private fence material")
	}
}

func TestLifecycleCoordinatorRequiresHoldServiceAndRejectsLateHoldsBeforeProviderEffect(t *testing.T) {
	t.Run("construction fails closed without exact hold service", func(t *testing.T) {
		db := newLifecycleCoordinatorTestDB(t)
		now := time.Date(2026, 8, 17, 15, 45, 0, 0, time.UTC)
		leases, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
			Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
		})
		if err != nil {
			t.Fatalf("NewLeaseService: %v", err)
		}
		_, err = NewCoordinator(CoordinatorDependencies{
			DB: db, Leases: leases, Now: func() time.Time { return now },
			LeaseOwnerID: "retention-worker-mandatory-hold-test",
		})
		if !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("NewCoordinator without HoldService error=%v, want ErrInvalidState", err)
		}
	})

	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_DATA_ENCRYPTION_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	for index, holdType := range []backupasset.RecoveryPointHoldType{
		backupasset.RecoveryPointHoldLegal,
		backupasset.RecoveryPointHoldOperational,
	} {
		t.Run(string(holdType), func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, uint64(930+index*10))
			holds := mustNewLifecycleHoldService(t, fixture.db, func() time.Time { return fixture.clock })
			coordinator, err := NewCoordinator(CoordinatorDependencies{
				DB: fixture.db, Leases: fixture.coordinator.leases, Holds: holds,
				Now: func() time.Time { return fixture.clock }, NewID: func() (string, error) { return testOpaqueID(uint64(939 + index*10)), nil },
				LeaseOwnerID: "retention-worker-provider-test", Admissions: &lifecycleAdmissionFake{},
				Cleanup: fixture.cleanup, Deleter: fixture.deleter, RetryDelay: time.Minute,
			})
			if err != nil {
				t.Fatalf("restart coordinator with mandatory hold service: %v", err)
			}
			attempt := fixture.attempt
			for _, want := range []backupasset.LifecyclePhase{
				backupasset.LifecyclePhaseRevoking,
				backupasset.LifecyclePhaseDraining,
				backupasset.LifecyclePhaseCleaning,
				backupasset.LifecyclePhaseProviderDelete,
			} {
				attempt, err = coordinator.Advance(context.Background(), attempt.ID)
				if err != nil || attempt.Phase != want {
					t.Fatalf("advance to provider effect want=%q phase=%q revision=%d error=%v",
						want, attempt.Phase, attempt.TransitionRevision, err)
				}
			}
			fixture.deleter.result = PointDeletionResult{
				Outcome: PointDeletionAlreadyAbsent, ReceiptDigest: strings.Repeat("9", 64),
			}
			fixture.deleter.entered = make(chan struct{})
			fixture.deleter.release = make(chan struct{})
			result := make(chan struct {
				attempt LifecycleAttempt
				err     error
			}, 1)
			go func() {
				advanced, advanceErr := coordinator.Advance(context.Background(), attempt.ID)
				result <- struct {
					attempt LifecycleAttempt
					err     error
				}{advanced, advanceErr}
			}()
			select {
			case <-fixture.deleter.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("provider fake did not enter after pre-effect validation")
			}
			expiresAt := fixture.clock.Add(time.Hour)
			create := CreateHoldRequest{
				Actor:           backupasset.AuditActor{UserID: 1, Username: "admin", Role: "admin"},
				RecoveryPointID: fixture.pointID, HoldType: holdType,
				Reason: "FAKE_LATE_HOLD_REASON_FOR_TEST_ONLY",
			}
			if holdType == backupasset.RecoveryPointHoldOperational {
				create.ExpiresAt = &expiresAt
			}
			if _, err := holds.Create(context.Background(), create); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("late %s hold error=%v, want ErrConflict", holdType, err)
			}
			var holdCount int64
			if err := fixture.db.Model(&model.RecoveryPointHold{}).
				Where("recovery_point_id = ? AND state = ?", fixture.pointID, backupasset.HoldActive).
				Count(&holdCount).Error; err != nil {
				t.Fatalf("count late holds: %v", err)
			}
			if holdCount != 0 || fixture.deleter.completed != 0 {
				t.Fatalf("late hold race persisted holds/deletion=%d/%d, want 0/0 before effect release", holdCount, fixture.deleter.completed)
			}
			close(fixture.deleter.release)
			advanced := <-result
			if advanced.err != nil || advanced.attempt.Phase != backupasset.LifecyclePhaseTombstoning || fixture.deleter.completed != 1 {
				t.Fatalf("provider effect after rejected late hold phase=%q revision=%d completed=%d error=%v",
					advanced.attempt.Phase, advanced.attempt.TransitionRevision, fixture.deleter.completed, advanced.err)
			}
		})
	}
}

func TestLifecycleExternalEffectsRequireCurrentFence(t *testing.T) {
	tests := []struct {
		name       string
		base       uint64
		phase      backupasset.LifecyclePhase
		invalidate func(*claimedExpiryFixture, model.RecoveryPointLease)
		calls      func(*claimedExpiryFixture) int
	}{
		{
			name:  "cleanup rejects taken-over fence",
			base:  760,
			phase: backupasset.LifecyclePhaseCleaning,
			invalidate: func(fixture *claimedExpiryFixture, lease model.RecoveryPointLease) {
				if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", lease.ID).Updates(map[string]any{
					"attempt_id": testOpaqueID(769), "fence_token": strings.Repeat("7", 64),
				}).Error; err != nil {
					t.Fatalf("replace cleanup fence: %v", err)
				}
			},
			calls: func(fixture *claimedExpiryFixture) int { return fixture.cleanup.calls },
		},
		{
			name:  "cleanup rejects absolute deadline",
			base:  770,
			phase: backupasset.LifecyclePhaseCleaning,
			invalidate: func(fixture *claimedExpiryFixture, lease model.RecoveryPointLease) {
				fixture.clock = lease.AbsoluteDeadline.UTC()
			},
			calls: func(fixture *claimedExpiryFixture) int { return fixture.cleanup.calls },
		},
		{
			name:  "provider deletion rejects stale fence",
			base:  780,
			phase: backupasset.LifecyclePhaseProviderDelete,
			invalidate: func(fixture *claimedExpiryFixture, lease model.RecoveryPointLease) {
				if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", lease.ID).Updates(map[string]any{
					"attempt_id": testOpaqueID(789), "fence_token": strings.Repeat("8", 64),
				}).Error; err != nil {
					t.Fatalf("replace deletion fence: %v", err)
				}
			},
			calls: func(fixture *claimedExpiryFixture) int { return fixture.deleter.calls },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, test.base)
			attempt := fixture.attempt
			for attempt.Phase != test.phase {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil {
					t.Fatalf("advance to %q: %v", test.phase, err)
				}
			}
			var lease model.RecoveryPointLease
			if err := fixture.db.First(&lease, "id = ?", attempt.LeaseID).Error; err != nil {
				t.Fatalf("load lifecycle lease: %v", err)
			}
			test.invalidate(fixture, lease)

			wantReason := backupasset.LifecycleBlockedFenceLost
			if test.phase == backupasset.LifecyclePhaseProviderDelete {
				wantReason = backupasset.LifecycleBlockedProviderDeleteUnproven
			}
			blocked, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
				blocked.BlockedReason != wantReason {
				t.Fatalf("stale effect attempt=%+v error=%v, want blocked/%s", blocked, err, wantReason)
			}
			if got := test.calls(fixture); got != 0 {
				t.Fatalf("irreversible effect calls=%d, want zero before current authority", got)
			}
		})
	}
}

func TestLifecycleExternalEffectDeadlineCancelsBeforeAuthorityExpires(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 795)
	attempt := fixture.attempt
	for attempt.Phase != backupasset.LifecyclePhaseCleaning {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance deadline fixture: %v", err)
		}
	}
	leaseExpiry := fixture.clock.Add(20 * time.Millisecond)
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.LeaseID).
		Update("lease_expires_at", leaseExpiry).Error; err != nil {
		t.Fatalf("shorten lifecycle effect lease: %v", err)
	}
	fixture.cleanup.waitForCancellation = true
	blocked, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
		blocked.BlockedReason != backupasset.LifecycleBlockedFenceLost {
		t.Fatalf("deadline-bounded effect attempt=%+v error=%v, want blocked/fence_lost", blocked, err)
	}
	if !fixture.cleanup.deadlinePresent || !fixture.cleanup.canceled || fixture.cleanup.completed != 0 {
		t.Fatalf("effect deadline/cancel/completed=%t/%t/%d, want true/true/0",
			fixture.cleanup.deadlinePresent, fixture.cleanup.canceled, fixture.cleanup.completed)
	}
}

func TestLifecycleUncertainEffectDeadlineDurablyBlocksWithoutReplay(t *testing.T) {
	tests := []struct {
		name             string
		base             uint64
		absoluteDeadline bool
	}{
		{name: "short lease expiry", base: 910},
		{name: "absolute deadline", base: 920, absoluteDeadline: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, test.base)
			fixture.clock = time.Now().UTC().Truncate(time.Millisecond)
			initialExpiry := fixture.clock.Add(5 * time.Minute)
			initialAbsolute := fixture.clock.Add(time.Hour)
			if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", fixture.attempt.LeaseID).
				Updates(map[string]any{"lease_expires_at": initialExpiry, "absolute_deadline": initialAbsolute}).Error; err != nil {
				t.Fatalf("align production-time lifecycle lease: %v", err)
			}
			attempt := fixture.attempt
			for attempt.Phase != backupasset.LifecyclePhaseCleaning {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil {
					t.Fatalf("advance uncertain-effect fixture: %v", err)
				}
			}
			deadline := fixture.clock.Add(25 * time.Millisecond)
			leaseUpdates := map[string]any{"lease_expires_at": deadline}
			if test.absoluteDeadline {
				leaseUpdates["absolute_deadline"] = deadline
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.LeaseID).
				Updates(leaseUpdates).Error; err != nil {
				t.Fatalf("set uncertain-effect lifecycle deadline: %v", err)
			}
			var originalLease model.RecoveryPointLease
			if err := fixture.db.First(&originalLease, "id = ?", attempt.LeaseID).Error; err != nil {
				t.Fatalf("load uncertain-effect lease: %v", err)
			}
			fixture.cleanup.waitForCancellation = true
			fixture.cleanup.onCancellation = func() { fixture.clock = deadline }
			blocked, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil {
				var stillDestructive model.RecoveryPointLifecycleAttempt
				if loadErr := fixture.db.First(&stillDestructive, "id = ?", attempt.ID).Error; loadErr != nil {
					t.Fatalf("load failed uncertain-effect attempt: %v", loadErr)
				}
				if stillDestructive.Phase != string(backupasset.LifecyclePhaseCleaning) {
					t.Fatalf("failed uncertain effect phase=%q, want cleaning RED", stillDestructive.Phase)
				}
				if !test.absoluteDeadline {
					replayDeadline := fixture.clock.Add(25 * time.Millisecond)
					if updateErr := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.LeaseID).
						Update("lease_expires_at", replayDeadline).Error; updateErr != nil {
						t.Fatalf("set uncertain-effect replay deadline: %v", updateErr)
					}
					fixture.cleanup.onCancellation = func() { fixture.clock = replayDeadline }
					_, _ = fixture.coordinator.Advance(context.Background(), attempt.ID)
					if fixture.cleanup.calls != 2 {
						t.Fatalf("uncertain effect replay calls=%d, want 2 RED", fixture.cleanup.calls)
					}
				}
				t.Fatalf("uncertain effect remained destructive after deadline: %v", err)
			}
			if blocked.Phase != backupasset.LifecyclePhaseBlocked ||
				blocked.BlockedReason != backupasset.LifecycleBlockedFenceLost {
				t.Fatalf("uncertain effect attempt=%+v error=%v, want durable blocked/fence_lost", blocked, err)
			}
			if fixture.cleanup.calls != 1 || fixture.cleanup.completed != 0 {
				t.Fatalf("uncertain cleanup calls/completed=%d/%d, want 1/0", fixture.cleanup.calls, fixture.cleanup.completed)
			}
			payload, marshalErr := json.Marshal(blocked)
			if marshalErr != nil {
				t.Fatalf("marshal uncertain blocked attempt: %v", marshalErr)
			}
			if strings.Contains(string(payload), originalLease.FenceToken) || strings.Contains(string(payload), "fence_token") {
				t.Fatalf("uncertain blocked attempt exposed fence material")
			}

			restart := newRestartedExpiryCoordinator(t, fixture, test.base+9)
			replayed, err := restart.Advance(context.Background(), blocked.ID)
			if err != nil || replayed.Phase != backupasset.LifecyclePhaseBlocked || fixture.cleanup.calls != 1 {
				t.Fatalf("pre-retry restart attempt=%+v cleanup_calls=%d error=%v, want blocked/no replay",
					replayed, fixture.cleanup.calls, err)
			}
			fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
			fixture.cleanup.waitForCancellation = false
			fixture.cleanup.onCancellation = nil
			fixture.deleter.result = PointDeletionResult{
				Outcome: PointDeletionAlreadyAbsent, ReceiptDigest: strings.Repeat("c", 64),
			}
			resumed, err := newRestartedExpiryCoordinator(t, fixture, test.base+10).Advance(context.Background(), blocked.ID)
			if err != nil || resumed.Phase != backupasset.LifecyclePhaseRevoking {
				t.Fatalf("fresh-fence retry attempt=%+v error=%v, want revoking", resumed, err)
			}
			if test.absoluteDeadline && resumed.LeaseID == originalLease.ID {
				t.Fatalf("absolute-deadline retry reused old lifecycle lease")
			}
			if !test.absoluteDeadline && resumed.LeaseAttemptID == originalLease.AttemptID {
				t.Fatalf("short-expiry retry reused old lifecycle fence attempt")
			}
			for resumed.Phase != backupasset.LifecyclePhaseComplete {
				resumed, err = newRestartedExpiryCoordinator(t, fixture, test.base+11).Advance(context.Background(), resumed.ID)
				if err != nil {
					t.Fatalf("complete uncertain-effect retry phase=%q: %v", resumed.Phase, err)
				}
			}
			if fixture.cleanup.calls != 2 || fixture.cleanup.completed != 1 {
				t.Fatalf("completed retry cleanup calls/completed=%d/%d, want 2/1", fixture.cleanup.calls, fixture.cleanup.completed)
			}
		})
	}
}

func TestLifecycleUncertainEffectDoesNotOverwriteNewerAuthority(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 930)
	fixture.clock = time.Now().UTC().Truncate(time.Millisecond)
	initialExpiry := fixture.clock.Add(5 * time.Minute)
	initialAbsolute := fixture.clock.Add(time.Hour)
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", fixture.attempt.LeaseID).
		Updates(map[string]any{"lease_expires_at": initialExpiry, "absolute_deadline": initialAbsolute}).Error; err != nil {
		t.Fatalf("align newer-authority lifecycle lease: %v", err)
	}
	attempt := fixture.attempt
	for attempt.Phase != backupasset.LifecyclePhaseCleaning {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance newer-authority fixture: %v", err)
		}
	}
	deadline := fixture.clock.Add(25 * time.Millisecond)
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", attempt.LeaseID).
		Update("lease_expires_at", deadline).Error; err != nil {
		t.Fatalf("set newer-authority deadline: %v", err)
	}
	originalRevision := attempt.TransitionRevision
	fixture.cleanup.waitForCancellation = true
	fixture.cleanup.onCancellation = func() {
		fixture.clock = deadline
		fresh, err := fixture.coordinator.leases.Takeover(context.Background(), backupasset.TakeoverLeaseRequest{
			LeaseID: attempt.LeaseID, OwnerID: "retention-worker-provider-test",
		})
		if err != nil {
			t.Errorf("install newer lifecycle authority: %v", err)
			return
		}
		freshHash := hashFenceToken(fresh.Fence.FenceToken)
		result := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ? AND transition_revision = ?", attempt.ID, originalRevision).
			Updates(map[string]any{
				"lease_attempt_id": fresh.Fence.AttemptID, "lease_fence_token_hash": freshHash,
				"transition_revision": originalRevision + 1, "updated_at": fixture.clock,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			t.Errorf("persist newer lifecycle authority: error=%v rows=%d", result.Error, result.RowsAffected)
		}
	}
	_, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if !errors.Is(err, backupasset.ErrConflict) {
		t.Fatalf("stale uncertain worker error=%v, want ErrConflict", err)
	}
	var preserved model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&preserved, "id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load preserved newer lifecycle authority: %v", err)
	}
	if preserved.Phase != string(backupasset.LifecyclePhaseCleaning) ||
		preserved.TransitionRevision != originalRevision+1 || preserved.BlockedReason != "" ||
		preserved.LeaseAttemptID == nil || *preserved.LeaseAttemptID == attempt.LeaseAttemptID {
		t.Fatalf("newer lifecycle authority was overwritten: %+v", preserved)
	}
	assertLifecyclePointState(t, fixture.db, fixture.pointID, backupasset.RecoveryPointExpiring)
}

func TestLifecycleClaimAndAdvanceUseOnePostgresLockOrder(t *testing.T) {
	db := newLifecycleCoordinatorPostgresTestDB(t)
	now := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(860)
	pointID := testOpaqueID(861)
	policyID := testOpaqueID(862)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-72*time.Hour), 3)
	point.PointRevision = 5
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed lock-order point: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 1, RulesJSON: `{"version":1,"age":{"keep_days":30}}`, Status: string(backupasset.RetentionPolicyActive),
		CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed lock-order policy: %v", err)
	}
	leases, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leases, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }), Now: func() time.Time { return now },
		NewID: func() (string, error) { return testOpaqueID(863), nil }, LeaseOwnerID: "retention-worker-lock-order-test",
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	selection := Selection{
		PolicyID: policyID, PolicyRevision: 1, ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("a", 64), EvaluatedAt: now,
		Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 5, CapabilityRevision: 3}},
	}
	request := ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire, PolicySelection: &selection,
	}
	attempt, err := coordinator.Claim(context.Background(), request)
	if err != nil {
		t.Fatalf("seed lock-order lifecycle claim: %v", err)
	}

	type lockProbeKey struct{}
	claimPointLocked := make(chan struct{})
	advanceAttemptLocked := make(chan struct{})
	releaseClaim := make(chan struct{})
	var claimOnce sync.Once
	var advanceOnce sync.Once
	if err := db.Callback().Query().After("gorm:query").Register("test:lifecycle_lock_order", func(callbackDB *gorm.DB) {
		label, _ := callbackDB.Statement.Context.Value(lockProbeKey{}).(string)
		if _, locked := callbackDB.Statement.Clauses["FOR"]; !locked {
			return
		}
		switch callbackDB.Statement.Dest.(type) {
		case *model.RecoveryPoint:
			if label == "claim" {
				claimOnce.Do(func() { close(claimPointLocked) })
				<-releaseClaim
			}
		case *model.RecoveryPointLifecycleAttempt:
			if label == "advance" {
				advanceOnce.Do(func() { close(advanceAttemptLocked) })
			}
		}
	}); err != nil {
		t.Fatalf("register lock-order probe: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("test:lifecycle_lock_order") })

	testCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	claimResult := make(chan error, 1)
	go func() {
		_, claimErr := coordinator.Claim(context.WithValue(testCtx, lockProbeKey{}, "claim"), request)
		claimResult <- claimErr
	}()
	select {
	case <-claimPointLocked:
	case <-testCtx.Done():
		t.Fatalf("idempotent claim did not lock point: %v", testCtx.Err())
	}
	advanceResult := make(chan error, 1)
	go func() {
		_, advanceErr := coordinator.Advance(context.WithValue(testCtx, lockProbeKey{}, "advance"), attempt.ID)
		advanceResult <- advanceErr
	}()
	select {
	case <-advanceAttemptLocked:
	case <-time.After(250 * time.Millisecond):
	}
	close(releaseClaim)
	for name, result := range map[string]<-chan error{"claim": claimResult, "advance": advanceResult} {
		select {
		case resultErr := <-result:
			if resultErr != nil {
				t.Fatalf("concurrent %s failed: %v", name, resultErr)
			}
		case <-testCtx.Done():
			t.Fatalf("concurrent %s did not complete without deadlock: %v", name, testCtx.Err())
		}
	}
}

func TestLifecycleClaimAndAdvanceUseOneSQLiteLockOrder(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 880)
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := fixture.coordinator.Claim(context.Background(), fixture.claim)
		results <- err
	}()
	go func() {
		<-start
		_, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		results <- err
	}()
	close(start)
	for index := 0; index < 2; index++ {
		if err := <-results; err != nil {
			t.Fatalf("concurrent SQLite lifecycle operation %d failed: %v", index, err)
		}
	}
	var attemptCount int64
	if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("recovery_point_id = ?", fixture.pointID).Count(&attemptCount).Error; err != nil {
		t.Fatalf("count SQLite lifecycle attempts: %v", err)
	}
	if attemptCount != 1 {
		t.Fatalf("SQLite concurrent lifecycle attempt count=%d, want one", attemptCount)
	}
}

type claimedExpiryFixture struct {
	db          *gorm.DB
	clock       time.Time
	pointID     string
	coordinator *Coordinator
	holds       *HoldService
	cleanup     *lifecycleCleanupFake
	deleter     *lifecycleDeletionFake
	claim       ClaimRequest
	attempt     LifecycleAttempt
}

type terminalEventRestartFixture struct {
	db          *gorm.DB
	clock       time.Time
	pointID     string
	operation   backupasset.LifecycleOperation
	coordinator *Coordinator
	holds       *HoldService
	cleanup     *lifecycleCleanupFake
	deleter     *lifecycleDeletionFake
	attempt     LifecycleAttempt
}

func newTerminalEventRestartFixture(
	t *testing.T,
	operation backupasset.LifecycleOperation,
	base uint64,
) *terminalEventRestartFixture {
	t.Helper()
	return newTerminalEventRestartFixtureWithDB(t, newLifecycleCoordinatorTestDB(t), operation, base)
}

func newTerminalEventRestartFixtureWithDB(
	t *testing.T,
	db *gorm.DB,
	operation backupasset.LifecycleOperation,
	base uint64,
) *terminalEventRestartFixture {
	t.Helper()
	fixture := &terminalEventRestartFixture{
		db: db, clock: time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC),
		pointID: testOpaqueID(base + 1), operation: operation,
	}
	repositoryID := testOpaqueID(base)
	seedRetentionUsersAndRepository(t, fixture.db, repositoryID)
	observedAt := fixture.clock.Add(-48 * time.Hour)
	point := model.RecoveryPoint{
		ID: fixture.pointID, RepositoryID: repositoryID,
		Semantics: string(backupasset.PointNativeSnapshot), State: string(backupasset.RecoveryPointCommitted),
		ObservedAt: &observedAt, PointRevision: 10, CapabilityRevision: 3,
		CapabilitiesJSON: `{}`, ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned),
		PhysicalAvailability: string(backupasset.PhysicalOnline), HoldState: string(backupasset.HoldNone),
		EncryptedProviderLocator: `{"snapshot":"private-terminal-event"}`,
		CreatedAt:                observedAt, UpdatedAt: fixture.clock,
	}
	if operation == backupasset.LifecycleMutableRetire || operation == backupasset.LifecycleExplicitPurge {
		point.Semantics = string(backupasset.PointMutableHead)
		point.ImmutabilityLevel = string(backupasset.ImmutabilityMutable)
	}
	if operation == backupasset.LifecycleMutableRetire {
		point.State = string(backupasset.RecoveryPointObserved)
	}
	if operation == backupasset.LifecycleExplicitPurge {
		point.State = string(backupasset.RecoveryPointRetired)
		point.EncryptedRollbackLocator = point.EncryptedProviderLocator
		point.EncryptedProviderLocator = ""
		retiredAt := fixture.clock.Add(-time.Hour)
		retirementReason := string(backupasset.RetirementWithdrawn)
		point.RetiredAt = &retiredAt
		point.RetirementReason = &retirementReason
	}
	if err := fixture.db.Create(&point).Error; err != nil {
		t.Fatalf("seed terminal-event restart point: %v", err)
	}
	policyOrPlanID := testOpaqueID(base + 2)
	if operation == backupasset.LifecycleRetentionExpire {
		if err := fixture.db.Create(&model.BackupRetentionPolicy{
			ID: policyOrPlanID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
			Revision: 1, RulesJSON: `{"version":1,"age":{"keep_days":30}}`, Status: string(backupasset.RetentionPolicyActive),
			CreatedBy: 1, UpdatedBy: 1, CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
		}).Error; err != nil {
			t.Fatalf("seed terminal-event restart policy: %v", err)
		}
	}
	actorID := uint(1)
	if operation == backupasset.LifecycleExplicitPurge {
		if err := fixture.db.Create(&model.BackupAssetPurgePlan{
			ID: policyOrPlanID, RepositoryID: repositoryID, RequesterID: actorID,
			Revision: 1, ImpactRevision: 1, ExpiresAt: fixture.clock.Add(time.Hour),
			Status: string(backupasset.PurgePlanExecuting), ExecuteActorID: &actorID,
			ExecuteProofDigest: strings.Repeat("1", 64), ExecuteReasonDigest: strings.Repeat("2", 64),
			CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
		}).Error; err != nil {
			t.Fatalf("seed terminal-event restart purge plan: %v", err)
		}
		if err := fixture.db.Create(&model.BackupAssetPurgePlanItem{
			ID: testOpaqueID(base + 4), PlanID: policyOrPlanID, Ordinal: 0, RecoveryPointID: fixture.pointID,
			ExpectedPointRevision: point.PointRevision, ExpectedCapabilityRevision: point.CapabilityRevision,
			CreatedAt: fixture.clock,
		}).Error; err != nil {
			t.Fatalf("seed terminal-event restart purge item: %v", err)
		}
	}
	leases, err := backupasset.NewLeaseService(fixture.db, func() time.Time { return fixture.clock }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	fixture.holds = mustNewLifecycleHoldService(t, fixture.db, func() time.Time { return fixture.clock })
	fixture.cleanup = &lifecycleCleanupFake{}
	fixture.deleter = &lifecycleDeletionFake{result: PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("d", 64),
	}}
	fixture.coordinator, err = NewCoordinator(CoordinatorDependencies{
		DB: fixture.db, Leases: leases, Holds: fixture.holds, Now: func() time.Time { return fixture.clock },
		NewID:        func() (string, error) { return testOpaqueID(base + 3), nil },
		LeaseOwnerID: "retention-worker-terminal-event-test", Admissions: &lifecycleAdmissionFake{},
		Cleanup: fixture.cleanup, Deleter: fixture.deleter, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	claim := ClaimRequest{RecoveryPointID: fixture.pointID, Operation: operation}
	switch operation {
	case backupasset.LifecycleRetentionExpire:
		claim.PolicySelection = &Selection{
			PolicyID: policyOrPlanID, PolicyRevision: 1,
			ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
			RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("a", 64),
			EvaluatedAt: fixture.clock,
			Points: []SelectedPoint{{
				RecoveryPointID: fixture.pointID, PointRevision: point.PointRevision,
				CapabilityRevision: point.CapabilityRevision,
			}},
		}
	case backupasset.LifecycleMutableRetire:
		claim.MutablePoint = &MutableRetirementSnapshot{
			PointRevision: point.PointRevision, CapabilityRevision: point.CapabilityRevision,
		}
	case backupasset.LifecycleExplicitPurge:
		claim.PurgePlan = &PurgePlanSnapshot{
			PlanID: policyOrPlanID, Revision: 1, ActorID: actorID,
			PointRevision: point.PointRevision, CapabilityRevision: point.CapabilityRevision,
		}
	}
	fixture.attempt, err = fixture.coordinator.Claim(context.Background(), claim)
	if err != nil {
		t.Fatalf("claim terminal-event restart operation=%q: %v", operation, err)
	}
	for transitions := 0; fixture.attempt.Phase != backupasset.LifecyclePhaseTombstoning && transitions < 8; transitions++ {
		fixture.attempt, err = fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		if err != nil {
			t.Fatalf("persist terminal event operation=%q phase=%q: %v", operation, fixture.attempt.Phase, err)
		}
	}
	if fixture.attempt.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("terminal event operation=%q stopped at phase=%q", operation, fixture.attempt.Phase)
	}
	var event model.RecoveryPointLifecycleTombstone
	if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?", fixture.pointID, operation).
		Limit(1).Find(&event).Error; err != nil {
		t.Fatalf("load persisted terminal event: %v", err)
	}
	if event.RecoveryPointID == "" {
		t.Fatalf("terminal event operation=%q was not persisted", operation)
	}
	return fixture
}

func blockTerminalEventAtAbsoluteDeadline(
	t *testing.T,
	fixture *terminalEventRestartFixture,
	restartID uint64,
) (LifecycleAttempt, model.RecoveryPointLease) {
	t.Helper()
	var oldLease model.RecoveryPointLease
	if err := fixture.db.First(&oldLease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
		t.Fatalf("load terminal-event lifecycle lease: %v", err)
	}
	fixture.clock = oldLease.AbsoluteDeadline.UTC().Add(time.Second)
	blocked, err := restartTerminalEventCoordinator(t, fixture, restartID).
		Advance(context.Background(), fixture.attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
		blocked.BlockedReason != backupasset.LifecycleBlockedFenceLost || blocked.RetryAt == nil {
		t.Fatalf("terminal-event deadline block phase/reason=%q/%q error=%v",
			blocked.Phase, blocked.BlockedReason, err)
	}
	return blocked, oldLease
}
func blockTerminalEventForValidation(
	t *testing.T,
	fixture *terminalEventRestartFixture,
) LifecycleAttempt {
	t.Helper()
	blocked, err := fixture.coordinator.block(
		context.Background(), fixture.attempt.ID, backupasset.LifecycleBlockedActiveHold,
	)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked || blocked.RetryAt == nil {
		t.Fatalf("terminal-event validation block phase/reason=%q/%q error=%v",
			blocked.Phase, blocked.BlockedReason, err)
	}
	return blocked
}

func restartTerminalEventCoordinator(
	t *testing.T,
	fixture *terminalEventRestartFixture,
	restartID uint64,
) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: fixture.db, Leases: fixture.coordinator.leases, Holds: fixture.holds,
		Now: func() time.Time { return fixture.clock }, NewID: func() (string, error) { return testOpaqueID(restartID), nil },
		LeaseOwnerID: "retention-worker-terminal-event-test", Admissions: &lifecycleAdmissionFake{},
		Cleanup: fixture.cleanup, Deleter: fixture.deleter, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("restart terminal-event coordinator: %v", err)
	}
	return coordinator
}

func assertTerminalEventRestartSkipsEffects(
	t *testing.T,
	operation backupasset.LifecycleOperation,
	base uint64,
) {
	t.Helper()
	fixture := newTerminalEventRestartFixture(t, operation, base)
	assertTerminalEventRestartFixtureSkipsEffects(t, fixture, base)
}

func assertTerminalEventRestartFixtureSkipsEffects(
	t *testing.T,
	fixture *terminalEventRestartFixture,
	base uint64,
) {
	t.Helper()
	if operation := fixture.operation; operation == backupasset.LifecycleRetentionExpire || operation == backupasset.LifecycleExplicitPurge {
		cleanupBefore, deleteBefore := fixture.cleanup.calls, fixture.deleter.calls
		var oldLease model.RecoveryPointLease
		if fixture.attempt.LeaseID == "" {
			t.Fatal("provider terminal event fixture has no lifecycle lease")
		}
		if err := fixture.db.First(&oldLease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
			t.Fatalf("load provider terminal-event lifecycle lease: %v", err)
		}
		fixture.clock = oldLease.AbsoluteDeadline.UTC().Add(time.Second)
		restarted := restartTerminalEventCoordinator(t, fixture, base+10)
		current, err := restarted.Advance(context.Background(), fixture.attempt.ID)
		if err != nil {
			t.Fatalf("provider terminal-event stale-deadline advance operation=%q: %v", operation, err)
		}
		for transitions := 0; transitions < 8 && current.Phase != backupasset.LifecyclePhaseComplete; transitions++ {
			current, err = restarted.Advance(context.Background(), current.ID)
			if err != nil {
				t.Fatalf("provider terminal-event completion operation=%q phase=%q: %v", operation, current.Phase, err)
			}
		}
		if current.Phase != backupasset.LifecyclePhaseComplete ||
			fixture.cleanup.calls != cleanupBefore || fixture.deleter.calls != deleteBefore {
			t.Fatalf("provider terminal-event stale-deadline operation=%q phase=%q effect_deltas=%d/%d, want complete/0/0",
				operation, current.Phase, fixture.cleanup.calls-cleanupBefore, fixture.deleter.calls-deleteBefore)
		}
		var eventCount int64
		if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
			Where("recovery_point_id = ? AND terminal_operation = ?", fixture.pointID, operation).
			Count(&eventCount).Error; err != nil {
			t.Fatalf("count provider terminal event: %v", err)
		}
		if eventCount != 1 {
			t.Fatalf("provider terminal event operation=%q count=%d, want one", operation, eventCount)
		}
		return
	}
	operation := fixture.operation
	cleanupBefore, deleteBefore := fixture.cleanup.calls, fixture.deleter.calls
	blocked, oldLease := blockTerminalEventAtAbsoluteDeadline(t, fixture, base+10)
	fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
	restarted := restartTerminalEventCoordinator(t, fixture, base+11)
	resumed, err := restarted.Advance(context.Background(), blocked.ID)
	if err != nil {
		t.Fatalf("adopt terminal-event retry operation=%q: %v", operation, err)
	}
	if resumed.Phase != backupasset.LifecyclePhaseTombstoning {
		current := resumed
		var replayErr error
		for transitions := 0; transitions < 8 && current.Phase != backupasset.LifecyclePhaseTombstoning && replayErr == nil; transitions++ {
			current, replayErr = restarted.Advance(context.Background(), current.ID)
		}
		duplicateEvent := replayErr != nil && strings.Contains(strings.ToLower(replayErr.Error()), "unique")
		t.Fatalf("terminal-event retry operation=%q resumed=%q effect_deltas=%d/%d duplicate_event=%t, want tombstoning/0/0/false",
			operation, resumed.Phase, fixture.cleanup.calls-cleanupBefore, fixture.deleter.calls-deleteBefore, duplicateEvent)
	}
	if resumed.LeaseID == oldLease.ID || fixture.cleanup.calls != cleanupBefore || fixture.deleter.calls != deleteBefore {
		t.Fatalf("terminal-event fresh adoption operation=%q fresh=%t effect_deltas=%d/%d, want true/0/0",
			operation, resumed.LeaseID != oldLease.ID,
			fixture.cleanup.calls-cleanupBefore, fixture.deleter.calls-deleteBefore)
	}
	completed, err := restartTerminalEventCoordinator(t, fixture, base+12).Advance(context.Background(), resumed.ID)
	if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete ||
		fixture.cleanup.calls != cleanupBefore || fixture.deleter.calls != deleteBefore {
		t.Fatalf("terminal-event completion operation=%q phase=%q effect_deltas=%d/%d error=%v",
			operation, completed.Phase,
			fixture.cleanup.calls-cleanupBefore, fixture.deleter.calls-deleteBefore, err)
	}
	replayed, err := restartTerminalEventCoordinator(t, fixture, base+13).Advance(context.Background(), completed.ID)
	if err != nil || replayed.Phase != backupasset.LifecyclePhaseComplete ||
		fixture.cleanup.calls != cleanupBefore || fixture.deleter.calls != deleteBefore {
		t.Fatalf("terminal-event completed replay operation=%q phase=%q effect_deltas=%d/%d error=%v",
			operation, replayed.Phase,
			fixture.cleanup.calls-cleanupBefore, fixture.deleter.calls-deleteBefore, err)
	}
	var eventCount int64
	if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
		Where("recovery_point_id = ? AND terminal_operation = ?", fixture.pointID, operation).
		Count(&eventCount).Error; err != nil {
		t.Fatalf("count exact terminal event: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("terminal event operation=%q count=%d, want one", operation, eventCount)
	}
}

func newClaimedExpiryFixture(t *testing.T, base uint64) *claimedExpiryFixture {
	t.Helper()
	return newClaimedExpiryFixtureWithDB(t, newLifecycleCoordinatorTestDB(t), base)
}

func newClaimedExpiryFixtureWithDB(t *testing.T, db *gorm.DB, base uint64) *claimedExpiryFixture {
	t.Helper()
	fixture := &claimedExpiryFixture{db: db, clock: time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)}
	repositoryID := testOpaqueID(base)
	fixture.pointID = testOpaqueID(base + 1)
	policyID := testOpaqueID(base + 2)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(fixture.pointID, repositoryID, nil, fixture.clock.Add(-96*time.Hour), 3)
	point.PointRevision = 30
	point.EncryptedProviderLocator = `{"snapshot":"private-exact-point"}`
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed claimed expiry point: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 1, RulesJSON: `{"version":1,"age":{"keep_days":30}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1,
		CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
	}).Error; err != nil {
		t.Fatalf("seed claimed expiry policy: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return fixture.clock }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	fixture.cleanup = &lifecycleCleanupFake{}
	fixture.deleter = &lifecycleDeletionFake{}
	fixture.holds = mustNewLifecycleHoldService(t, db, func() time.Time { return fixture.clock })
	fixture.coordinator, err = NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: fixture.holds, Now: func() time.Time { return fixture.clock },
		NewID: func() (string, error) { return testOpaqueID(base + 3), nil }, LeaseOwnerID: "retention-worker-provider-test",
		Admissions: &lifecycleAdmissionFake{}, Cleanup: fixture.cleanup, Deleter: fixture.deleter, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	selection := Selection{
		PolicyID: policyID, PolicyRevision: 1,
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("6", 64), EvaluatedAt: fixture.clock,
		Points: []SelectedPoint{{RecoveryPointID: fixture.pointID, PointRevision: 30, CapabilityRevision: 3}},
	}
	fixture.claim = ClaimRequest{
		RecoveryPointID: fixture.pointID, Operation: backupasset.LifecycleRetentionExpire, PolicySelection: &selection,
	}
	fixture.attempt, err = fixture.coordinator.Claim(context.Background(), fixture.claim)
	if err != nil {
		t.Fatalf("Claim expiry fixture: %v", err)
	}
	return fixture
}

func seedProviderDeleteProofFirstFixture(
	t *testing.T,
	base uint64,
	claimState string,
) *claimedExpiryFixture {
	t.Helper()
	return seedProviderDeleteProofFirstFixtureWithDB(t, newLifecycleCoordinatorTestDB(t), base, claimState)
}

func seedProviderDeleteProofFirstFixtureWithDB(
	t *testing.T,
	db *gorm.DB,
	base uint64,
	claimState string,
) *claimedExpiryFixture {
	t.Helper()
	fixture := newClaimedExpiryFixtureWithDB(t, db, base)
	attempt := fixture.attempt
	for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance proof-first fixture to provider_delete: %v", err)
		}
	}
	fixture.attempt = attempt

	var point model.RecoveryPoint
	if err := fixture.db.First(&point, "id = ?", fixture.pointID).Error; err != nil {
		t.Fatalf("load proof-first point: %v", err)
	}
	now := fixture.clock.UTC()
	stale := now.Add(-time.Minute)
	if err := fixture.db.Model(&model.RecoveryPointLease{}).
		Where("id = ?", attempt.LeaseID).
		Updates(map[string]any{
			"status": backupasset.LeaseExpired, "lease_expires_at": stale,
			"absolute_deadline": stale, "updated_at": stale,
		}).Error; err != nil {
		t.Fatalf("expire proof-first lease: %v", err)
	}
	if err := fixture.db.Create(&model.RecoveryPointLifecycleEffectClaim{
		ID:                   testOpaqueID(base + 4),
		AttemptID:            attempt.ID,
		ExecutorID:           testOpaqueID(base + 5),
		ExecutionID:          testOpaqueID(base + 6),
		TransitionRevision:   attempt.TransitionRevision,
		LeaseID:              attempt.LeaseID,
		LeaseAttemptID:       attempt.LeaseAttemptID,
		LeaseFenceTokenHash:  attempt.LeaseFenceTokenHash,
		TargetIdentityDigest: strings.Repeat("a", 64),
		State:                claimState,
		DeadlineAt:           stale,
		HeartbeatAt:          stale,
		CreatedAt:            stale,
		UpdatedAt:            stale,
	}).Error; err != nil {
		t.Fatalf("seed proof-first effect claim: %v", err)
	}
	receiptDigest := strings.Repeat("c", 64)
	if err := fixture.db.Create(&model.RecoveryPointLifecycleTombstone{
		RecoveryPointID:       point.ID,
		RepositoryID:          point.RepositoryID,
		OriginalSemantics:     point.Semantics,
		TerminalOperation:     string(attempt.Operation),
		TerminalState:         string(backupasset.RecoveryPointExpired),
		ManagedHistory:        true,
		DeletionReceiptDigest: &receiptDigest,
		PurgedAt:              &now,
		ResultCode:            string(PointDeletionDeleted),
		CreatedAt:             now,
	}).Error; err != nil {
		t.Fatalf("seed proof-first tombstone: %v", err)
	}
	futureRetry := now.Add(time.Hour)
	if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ?", attempt.ID).
		Update("retry_at", futureRetry).Error; err != nil {
		t.Fatalf("seed proof-first retry gate: %v", err)
	}
	return fixture
}

func newRestartedExpiryCoordinator(t *testing.T, fixture *claimedExpiryFixture, id uint64) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: fixture.db, Leases: fixture.coordinator.leases, Holds: fixture.holds, Now: func() time.Time { return fixture.clock },
		NewID: func() (string, error) { return testOpaqueID(id), nil }, LeaseOwnerID: "retention-worker-provider-test",
		Admissions: &lifecycleAdmissionFake{}, Cleanup: fixture.cleanup, Deleter: fixture.deleter, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatalf("restart expiry coordinator: %v", err)
	}
	return coordinator
}

type lifecycleCleanupFake struct {
	calls               int
	completed           int
	err                 error
	waitForCancellation bool
	deadlinePresent     bool
	canceled            bool
	onCancellation      func()
	operation           backupasset.LifecycleOperation
}

func (fake *lifecycleCleanupFake) CleanupRecoveryPoint(ctx context.Context, request LifecyclePointRequest) error {
	fake.calls++
	fake.operation = request.Operation
	_, fake.deadlinePresent = ctx.Deadline()
	if fake.waitForCancellation {
		<-ctx.Done()
		fake.canceled = true
		if fake.onCancellation != nil {
			fake.onCancellation()
		}
		return ctx.Err()
	}
	if backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil || backupasset.ValidateOpaqueID(request.AttemptID) != nil {
		return errors.New("invalid lifecycle cleanup request")
	}
	if fake.err == nil {
		fake.completed++
	}
	return fake.err
}

type lifecycleDeletionFake struct {
	calls        int
	prepareCalls int
	completed    int
	pointID      string
	attemptID    string
	result       PointDeletionResult
	prepareErr   error
	err          error
	entered      chan struct{}
	release      chan struct{}
	operation    backupasset.LifecycleOperation
	afterEffect  func()
	verify       func(context.Context)
	verifyCalls  int
}

type registryDeletePointResolver struct {
	snapshot provider.ReadSnapshot
}

func (resolver registryDeletePointResolver) ResolveDeletePoint(
	_ context.Context,
	_ *gorm.DB,
	request LifecyclePointRequest,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	if point.EncryptedProviderLocator == "" || repository.ID != resolver.snapshot.RepositoryID {
		return provider.DeletePointRequest{}, provider.ErrDeletePointIdentityConflict
	}
	snapshot := resolver.snapshot
	snapshot.Access.Provider = backupasset.ProviderKind(repository.ProviderKind)
	snapshot.Access.TaskID = 1
	snapshot.Access.NodeID = 1
	snapshot.Access.IdentitySalt = []byte(strings.Repeat("s", 32))
	snapshot.Access.EndpointFacts = []string{"test-endpoint"}
	if snapshot.Access.Provider == backupasset.ProviderRestic {
		snapshot.Access.AdapterData = provider.ResticRuntimeAccess{
			NativeRepositoryID: strings.Repeat("0", 64),
			Command: &provider.RemoteCommandAccess{Node: model.Node{
				ID: 1, Host: "localhost", Port: 22, Username: "root", AuthType: "password",
				BasePath: "/", BackupDir: "/backup", Password: "FAKE_TEST_PASSWORD",
			}},
		}
	}
	if strings.TrimSpace(snapshot.RepositoryIdentity) == "" && snapshot.Access.Provider == backupasset.ProviderRestic {
		snapshot.RepositoryIdentity = provider.NativeResticIdentityPrefix + strings.Repeat("0", 64)
	}
	snapshot.Access.RepositoryID = repository.ID
	if repository.RepositoryIdentity != nil {
		snapshot.RepositoryIdentity = *repository.RepositoryIdentity
	}
	return provider.DeletePointRequest{
		Snapshot:               snapshot,
		Point:                  provider.PointLocator{Native: point.EncryptedProviderLocator},
		ExpectedSourceRevision: snapshot.SourceRevision,
		OperationID:            request.AttemptID,
	}, nil
}

type sequencedDeletePointResolver struct {
	calls  int
	native bool
}

func (resolver *sequencedDeletePointResolver) ResolveDeletePoint(
	_ context.Context,
	_ *gorm.DB,
	request LifecyclePointRequest,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	resolver.calls++
	repositoryIdentity := ""
	if repository.RepositoryIdentity != nil {
		repositoryIdentity = *repository.RepositoryIdentity
	}
	snapshot := provider.ReadSnapshot{
		RepositoryID: repository.ID, RepositoryIdentity: repositoryIdentity,
		CapabilityRevision: repository.CapabilityRevision, SourceRevision: point.SourceFingerprint,
		Access: provider.AccessBinding{
			Provider:      backupasset.ProviderKind(repository.ProviderKind),
			RepositoryID:  repository.ID,
			TaskID:        1,
			NodeID:        1,
			IdentitySalt:  []byte(strings.Repeat("s", provider.IdentitySaltBytes)),
			EndpointFacts: []string{"test-endpoint"},
		},
	}
	command := &provider.RemoteCommandAccess{Node: model.Node{
		ID: 1, Host: "localhost", Port: 22, Username: "root", AuthType: "password",
		BasePath: "/", BackupDir: "/backup", Password: "FAKE_TEST_PASSWORD",
	}}
	nativeRepositoryID := strings.Repeat("0", 64)
	if resolver.calls > 1 && resolver.native {
		nativeRepositoryID = strings.Repeat("1", 64)
	}
	snapshot.Access.AdapterData = provider.ResticRuntimeAccess{
		NativeRepositoryID: nativeRepositoryID,
		Command:            command,
	}
	if !resolver.native && resolver.calls > 1 {
		snapshot.Access.Secret = []byte("post-effect binding authority drift")
	}
	return provider.DeletePointRequest{
		Snapshot: snapshot, Point: provider.PointLocator{Native: point.EncryptedProviderLocator},
		ExpectedSourceRevision: point.SourceFingerprint, OperationID: request.AttemptID,
	}, nil
}

type registryPointDeleterFake struct {
	kind    backupasset.ProviderKind
	result  provider.DeletePointResult
	err     error
	calls   int
	request provider.DeletePointRequest
	entered chan struct{}
	release chan struct{}
}

func (fake *registryPointDeleterFake) ProviderKind() backupasset.ProviderKind {
	return fake.kind
}

func (fake *registryPointDeleterFake) DeletePoint(_ context.Context, request provider.DeletePointRequest) (provider.DeletePointResult, error) {
	fake.calls++
	fake.request = request
	if fake.entered != nil {
		close(fake.entered)
		<-fake.release
	}
	return fake.result, fake.err
}

type retentionProviderProberFake struct{}

func (*retentionProviderProberFake) Probe(context.Context, provider.AccessBinding, provider.OperationLimits) (provider.RepositoryObservation, error) {
	return provider.RepositoryObservation{}, nil
}

func (fake *lifecycleDeletionFake) Prepare(
	_ context.Context,
	_ *gorm.DB,
	_ providerDeletePrepareProfile,
	request LifecyclePointRequest,
	_ lifecycleDeleteRows,
) (preparedPointDeletion, error) {
	fake.prepareCalls++
	fake.operation = request.Operation
	fake.pointID = request.RecoveryPointID
	fake.attemptID = request.AttemptID
	if fake.prepareErr != nil {
		return preparedPointDeletion{}, fake.prepareErr
	}
	return preparedPointDeletion{
		identityDigest: strings.Repeat("a", 64),
		request:        provider.DeletePointRequest{OperationID: request.AttemptID},
	}, nil
}

func (fake *lifecycleDeletionFake) Execute(ctx context.Context, _ preparedPointDeletion) (pointDeletionExecution, error) {
	fake.calls++
	if fake.entered != nil {
		close(fake.entered)
		select {
		case <-fake.release:
		case <-ctx.Done():
			return pointDeletionExecution{ProviderCalled: true, Stage: providerDeleteStageProvider}, ctx.Err()
		}
	}
	if fake.err != nil {
		return pointDeletionExecution{ProviderCalled: true, Stage: providerDeleteStageProvider}, fake.err
	}
	if fake.afterEffect != nil {
		afterEffect := fake.afterEffect
		fake.afterEffect = nil
		afterEffect()
	}
	fake.completed++
	return pointDeletionExecution{Result: fake.result, ProviderCalled: true, Stage: providerDeleteStageProvider}, nil
}

func (fake *lifecycleDeletionFake) Verify(ctx context.Context, _ *gorm.DB, _ LifecyclePointRequest, _ preparedPointDeletion, _ lifecycleDeleteRows) error {
	fake.verifyCalls++
	if fake.verify != nil {
		fake.verify(ctx)
	}
	return nil
}

type preClaimWinnerConnPool struct {
	*sql.DB
	mu              sync.Mutex
	beginCount      int
	onFallbackBegin func()
}

func (pool *preClaimWinnerConnPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	pool.mu.Lock()
	pool.beginCount++
	triggerWinner := pool.beginCount == 1
	onFallbackBegin := pool.onFallbackBegin
	pool.mu.Unlock()
	if triggerWinner && onFallbackBegin != nil {
		onFallbackBegin()
	}
	tx, err := pool.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &ambiguousCommitTx{Tx: tx}, nil
}

type ambiguousCommitConnPool struct {
	*sql.DB
	mu                sync.Mutex
	failNext          bool
	onAmbiguousCommit func()
}

func (pool *ambiguousCommitConnPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := pool.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	pool.mu.Lock()
	failCommit := pool.failNext
	pool.failNext = false
	onAmbiguousCommit := pool.onAmbiguousCommit
	pool.mu.Unlock()
	return &ambiguousCommitTx{Tx: tx, failCommit: failCommit, onCommit: onAmbiguousCommit}, nil
}

type ambiguousCommitTx struct {
	*sql.Tx
	failCommit bool
	onCommit   func()
}

func (tx *ambiguousCommitTx) Commit() error {
	err := tx.Tx.Commit()
	if err == nil && tx.failCommit {
		if tx.onCommit != nil {
			tx.onCommit()
		}
		return errors.New("ambiguous transaction commit")
	}
	return err
}

func runRegistryPointDeletionProtocol(ctx context.Context, adapter *RegistryPointDeletion, request LifecyclePointRequest) (PointDeletionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var prepared preparedPointDeletion
	err := adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockLifecycleDeleteRowsTx(ctx, tx, request, false)
		if err != nil {
			return err
		}
		if err := validateLifecycleDeleteRows(request, rows.attempt, rows.point, rows.lease, rows.repository); err != nil {
			return err
		}
		held, err := lifecycleDeleteHasActiveHoldTx(ctx, tx, rows.point)
		if err != nil {
			return err
		}
		if held {
			return lifecycleDeleteIdentityConflict("lifecycle deletion is blocked by an active hold")
		}
		prepared, err = adapter.Prepare(ctx, tx, providerDeletePrepareObserver, request, rows)
		return err
	})
	if err != nil {
		return PointDeletionResult{}, mapProviderDeletionError(err)
	}
	execution, err := adapter.Execute(ctx, prepared)
	if err != nil {
		return PointDeletionResult{}, mapProviderDeletionError(err)
	}
	if !execution.ProviderCalled || !validPointDeletionResult(execution.Result) {
		return PointDeletionResult{}, fmt.Errorf("%w: provider deletion result is unproven", backupasset.ErrInvalidState)
	}
	err = adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := lockLifecycleDeleteRowsTx(ctx, tx, request, true)
		if err != nil {
			return err
		}
		if err := adapter.Verify(ctx, tx, request, prepared, rows); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return PointDeletionResult{}, mapProviderDeletionError(err)
	}
	return execution.Result, nil
}

func mustNewLifecycleHoldService(t *testing.T, db *gorm.DB, now func() time.Time) *HoldService {
	t.Helper()
	service, err := NewHoldService(HoldServiceDependencies{DB: db, Now: now})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	return service
}

type lifecycleAdmissionFake struct {
	calls     int
	err       error
	operation backupasset.LifecycleOperation
}

func (fake *lifecycleAdmissionFake) RevokeRecoveryPoint(_ context.Context, request LifecyclePointRequest) error {
	fake.calls++
	fake.operation = request.Operation
	if backupasset.ValidateOpaqueID(request.RecoveryPointID) != nil || backupasset.ValidateOpaqueID(request.AttemptID) != nil {
		return errors.New("invalid lifecycle point request")
	}
	return fake.err
}

func assertLifecyclePointState(t *testing.T, db *gorm.DB, pointID string, want backupasset.RecoveryPointState) {
	t.Helper()
	var point model.RecoveryPoint
	if err := db.Select("state").First(&point, "id = ?", pointID).Error; err != nil {
		t.Fatalf("load lifecycle point state: %v", err)
	}
	if point.State != string(want) {
		t.Fatalf("lifecycle point state=%q, want %q", point.State, want)
	}
}

func newLifecycleCoordinatorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newRetentionTestDB(t)
	if err := db.AutoMigrate(
		&model.RecoveryPointLease{},
		&model.RecoveryPointLifecycleAttempt{},
		&model.RecoveryPointLifecycleTombstone{},
		&model.RecoveryPointLifecycleEffectClaim{},
		&model.RecoveryPointLifecycleAuditSlot{},
		&model.BackupAssetPurgePlan{},
		&model.BackupAssetPurgePlanItem{},
	); err != nil {
		t.Fatalf("migrate lifecycle coordinator test database: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_recovery_point_leases_active_owner_slot
		ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`).Error; err != nil {
		t.Fatalf("create lifecycle lease owner index: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_recovery_point_lifecycle_attempts_active
		ON recovery_point_lifecycle_attempts(recovery_point_id) WHERE phase <> 'complete'`).Error; err != nil {
		t.Fatalf("create active lifecycle attempt index: %v", err)
	}
	return db
}

func newLifecycleCoordinatorPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newIsolatedRetentionPostgresTestDB(t)
	if err := db.AutoMigrate(
		&model.RecoveryPointLease{},
		&model.RecoveryPointLifecycleAttempt{},
		&model.RecoveryPointLifecycleTombstone{},
		&model.RecoveryPointLifecycleEffectClaim{},
		&model.RecoveryPointLifecycleAuditSlot{},
		&model.BackupAssetPurgePlan{},
		&model.BackupAssetPurgePlanItem{},
	); err != nil {
		t.Fatalf("migrate PostgreSQL lifecycle coordinator test database: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_point_leases_active_owner_slot
		ON recovery_point_leases(recovery_point_id, holder_type, owner_id) WHERE status = 'active'`).Error; err != nil {
		t.Fatalf("create PostgreSQL lifecycle lease owner index: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_point_lifecycle_attempts_active
		ON recovery_point_lifecycle_attempts(recovery_point_id) WHERE phase <> 'complete'`).Error; err != nil {
		t.Fatalf("create PostgreSQL active lifecycle attempt index: %v", err)
	}
	return db
}

type recordingSettledAudit struct {
	events []backupasset.AuditEventInput
}

func (audit *recordingSettledAudit) Write(_ context.Context, event backupasset.AuditEventInput) error {
	audit.events = append(audit.events, event)
	return nil
}
func (audit *recordingSettledAudit) WriteTx(_ context.Context, _ *gorm.DB, event backupasset.AuditEventInput) error {
	return audit.Write(context.Background(), event)
}

type flakySettledAudit struct {
	failLeft     int
	settledCalls int
	events       []backupasset.AuditEventInput
}

func (audit *flakySettledAudit) Write(_ context.Context, event backupasset.AuditEventInput) error {
	if event.Action != backupasset.AuditActionRepositoryPurge || event.Fields[backupasset.AuditFieldStage] != "settled" {
		return nil
	}
	if audit.failLeft > 0 {
		audit.failLeft--
		return errors.New("settled deletion audit unavailable")
	}
	audit.settledCalls++
	audit.events = append(audit.events, event)
	return nil
}
func (audit *flakySettledAudit) WriteTx(_ context.Context, _ *gorm.DB, event backupasset.AuditEventInput) error {
	return audit.Write(context.Background(), event)
}

func TestListIncompleteAttemptsAfterUsesKeysetCursor(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 19, 13, 0, 0, 0, time.UTC)
	repositoryID := testOpaqueID(700)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	firstPoint := newSelectionPoint(testOpaqueID(701), repositoryID, nil, now.Add(-72*time.Hour), 8)
	secondPoint := newSelectionPoint(testOpaqueID(702), repositoryID, nil, now.Add(-48*time.Hour), 8)
	firstPoint.PointRevision = 15
	secondPoint.PointRevision = 16
	if err := db.Create(&[]model.RecoveryPoint{firstPoint, secondPoint}).Error; err != nil {
		t.Fatalf("seed attempt points: %v", err)
	}
	policyID := testOpaqueID(703)
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 3, RulesJSON: `{"version":1,"age":{"keep_days":1}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed attempt policy: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }),
		Now: func() time.Time { return now }, NewID: sequentialOpaqueIDs(710), LeaseOwnerID: "retention-worker-cursor",
		Admissions: &lifecycleAdmissionFake{}, Cleanup: &lifecycleCleanupFake{},
		Deleter: &lifecycleDeletionFake{result: PointDeletionResult{Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("c", 64)}},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	selection := Selection{
		PolicyID: policyID, PolicyRevision: 3,
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"age":{"keep_days":1}}`, RuleDigest: strings.Repeat("e", 64), EvaluatedAt: now,
	}
	for _, point := range []model.RecoveryPoint{firstPoint, secondPoint} {
		selection.Points = []SelectedPoint{{RecoveryPointID: point.ID, PointRevision: point.PointRevision, CapabilityRevision: 8}}
		if _, err := coordinator.Claim(context.Background(), ClaimRequest{
			RecoveryPointID: point.ID, Operation: backupasset.LifecycleRetentionExpire, PolicySelection: &selection,
		}); err != nil {
			t.Fatalf("Claim %s: %v", point.ID, err)
		}
	}
	firstPage, err := coordinator.ListIncompleteAttemptsAfter(context.Background(), 1, "")
	if err != nil || len(firstPage) != 1 {
		t.Fatalf("first page=%+v err=%v", firstPage, err)
	}
	secondPage, err := coordinator.ListIncompleteAttemptsAfter(context.Background(), 1, firstPage[0].ID)
	if err != nil || len(secondPage) != 1 {
		t.Fatalf("second page=%+v err=%v", secondPage, err)
	}
	if firstPage[0].ID >= secondPage[0].ID {
		t.Fatalf("keyset cursor did not advance: first=%s second=%s", firstPage[0].ID, secondPage[0].ID)
	}
}

func TestSettledProviderDeleteWritesAuditBeyondClaimed(t *testing.T) {
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 19, 13, 30, 0, 0, time.UTC)
	repositoryID := testOpaqueID(720)
	pointID := testOpaqueID(721)
	policyID := testOpaqueID(722)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-96*time.Hour), 8)
	point.PointRevision = 15
	point.EncryptedProviderLocator = `{"snapshot":"exact-private-provider-id"}`
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed expiring point: %v", err)
	}
	if err := db.Create(&model.BackupRetentionPolicy{
		ID: policyID, ScopeKind: string(backupasset.RetentionPolicyScopeRepository), ScopeID: repositoryID,
		Revision: 5, RulesJSON: `{"version":1,"age":{"keep_days":30}}`,
		Status: string(backupasset.RetentionPolicyActive), CreatedBy: 1, UpdatedBy: 1, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed expiry policy: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	audit := &recordingSettledAudit{}
	coordinator, err := NewCoordinator(CoordinatorDependencies{
		DB: db, Leases: leaseService, Holds: mustNewLifecycleHoldService(t, db, func() time.Time { return now }),
		Now: func() time.Time { return now }, NewID: func() (string, error) { return testOpaqueID(723), nil },
		LeaseOwnerID: "retention-worker-settled-audit", Admissions: &lifecycleAdmissionFake{},
		Cleanup: &lifecycleCleanupFake{}, Deleter: &lifecycleDeletionFake{result: PointDeletionResult{
			Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("d", 64),
		}},
		Audit: audit,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	selection := Selection{
		PolicyID: policyID, PolicyRevision: 5,
		ScopeKind: backupasset.RetentionPolicyScopeRepository, ScopeID: repositoryID,
		RulesJSON: `{"version":1,"age":{"keep_days":30}}`, RuleDigest: strings.Repeat("e", 64), EvaluatedAt: now,
		Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 15, CapabilityRevision: 8}},
	}
	attempt, err := coordinator.Claim(context.Background(), ClaimRequest{
		RecoveryPointID: pointID, Operation: backupasset.LifecycleRetentionExpire, PolicySelection: &selection,
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking, backupasset.LifecyclePhaseDraining, backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete, backupasset.LifecyclePhaseTombstoning, backupasset.LifecyclePhaseComplete,
	} {
		attempt, err = coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("Advance want phase=%q attempt=%+v error=%v", want, attempt, err)
		}
	}
	if len(audit.events) == 0 {
		t.Fatal("settled delete wrote no asset audit event")
	}
	event := audit.events[len(audit.events)-1]
	if event.Action != backupasset.AuditActionRepositoryPurge || event.Outcome != backupasset.AuditOutcomeSuccess ||
		event.RecoveryPointID != pointID || event.Fields[backupasset.AuditFieldStage] != "settled" ||
		event.Fields[backupasset.AuditFieldStatus] != "deleted" {
		t.Fatalf("settled audit=%+v fields=%+v, want repository_purge settled deleted", event, event.Fields)
	}
	if event.Fields[backupasset.AuditFieldStatus] == "claimed" {
		t.Fatal("settled delete audit was claim-only")
	}
}

func TestLifecycleSettledDeletionAuditFailureProgressesProofBeforeRetry(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 1320)
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("a", 64),
	}
	audit := &flakySettledAudit{failLeft: 1}
	fixture.coordinator.audit = audit

	attempt := fixture.attempt
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete,
	} {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("Advance to %s attempt=%+v error=%v", want, attempt, err)
		}
	}

	attempt, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err == nil {
		t.Fatal("expected settled deletion audit failure")
	}
	if attempt.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("phase after failed audit = %q, want %q", attempt.Phase, backupasset.LifecyclePhaseTombstoning)
	}
	if attempt.Phase == backupasset.LifecyclePhaseComplete {
		t.Fatal("attempt must not complete without settled deletion audit")
	}
	if fixture.deleter.calls != 1 {
		t.Fatalf("deleter calls after failed audit = %d, want 1", fixture.deleter.calls)
	}
	var tombstone model.RecoveryPointLifecycleTombstone
	if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?", fixture.pointID, backupasset.LifecycleRetentionExpire).
		First(&tombstone).Error; err != nil {
		t.Fatalf("tombstone after provider delete: %v", err)
	}
	if audit.settledCalls != 0 {
		t.Fatalf("settled audit writes = %d, want 0", audit.settledCalls)
	}

	audit.failLeft = 0
	if attempt.RetryAt == nil {
		t.Fatal("retry_at must be set after settled audit failure")
	}
	fixture.clock = attempt.RetryAt.Add(time.Second)
	pending, err := fixture.coordinator.flushDueSettledAuditBeforeHeartbeat(context.Background(), attempt)
	if err != nil || pending {
		t.Fatalf("flush after healthy audit pending=%v error=%v", pending, err)
	}
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil {
		t.Fatalf("complete after tombstone: %v", err)
	}
	if attempt.Phase != backupasset.LifecyclePhaseComplete {
		t.Fatalf("phase = %q, want complete", attempt.Phase)
	}
	if fixture.deleter.calls != 1 {
		t.Fatalf("deleter calls after audit retry = %d, want 1", fixture.deleter.calls)
	}
	if audit.settledCalls != 1 {
		t.Fatalf("settled audit writes = %d, want 1", audit.settledCalls)
	}

}

func TestLifecycleProviderDeleteProofFirstRecoverySQLite(t *testing.T) {
	states := []string{"in_flight", "uncertain"}
	for index, claimState := range states {
		t.Run(claimState, func(t *testing.T) {
			base := uint64(1350 + index*100)
			t.Run("advance", func(t *testing.T) {
				fixture := seedProviderDeleteProofFirstFixture(t, base, claimState)
				var before model.RecoveryPointLifecycleEffectClaim
				if err := fixture.db.First(&before, "attempt_id = ?", fixture.attempt.ID).Error; err != nil {
					t.Fatalf("load proof-first claim before advance: %v", err)
				}
				got, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
				if err != nil || got.Phase != backupasset.LifecyclePhaseTombstoning {
					t.Fatalf("proof-first advance attempt=%+v error=%v, want tombstoning", got, err)
				}
				var after model.RecoveryPointLifecycleEffectClaim
				if err := fixture.db.First(&after, "attempt_id = ?", fixture.attempt.ID).Error; err != nil {
					t.Fatalf("load proof-first claim after advance: %v", err)
				}
				if before.State != after.State || before.ExecutionID != after.ExecutionID ||
					before.LeaseID != after.LeaseID || before.LeaseFenceTokenHash != after.LeaseFenceTokenHash ||
					fixture.deleter.calls != 0 {
					t.Fatalf("proof-first advance mutated claim/provider: before=%+v after=%+v calls=%d",
						before, after, fixture.deleter.calls)
				}
			})

			t.Run("heartbeat", func(t *testing.T) {
				fixture := seedProviderDeleteProofFirstFixture(t, base+10, claimState)
				if err := fixture.db.Model(&model.RecoveryPointLease{}).
					Where("id = ?", fixture.attempt.LeaseID).
					Update("owner_id", "foreign-proof-heartbeat-owner").Error; err != nil {
					t.Fatalf("rebind proof-first heartbeat lease: %v", err)
				}
				var beforeLease model.RecoveryPointLease
				if err := fixture.db.First(&beforeLease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
					t.Fatalf("load proof-first lease before heartbeat: %v", err)
				}
				before, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
				if err != nil {
					t.Fatalf("load proof-first attempt before heartbeat: %v", err)
				}
				got, err := fixture.coordinator.Heartbeat(context.Background(), fixture.attempt.ID)
				if err != nil || got.Phase != backupasset.LifecyclePhaseProviderDelete ||
					got.RetryAt == nil || before.TransitionRevision != got.TransitionRevision {
					t.Fatalf("proof-first heartbeat attempt=%+v before=%+v error=%v", got, before, err)
				}
				var afterLease model.RecoveryPointLease
				if err := fixture.db.First(&afterLease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
					t.Fatalf("load proof-first lease after heartbeat: %v", err)
				}
				if !reflect.DeepEqual(beforeLease, afterLease) {
					t.Fatalf("proof-first heartbeat mutated rebound lease before=%+v after=%+v", beforeLease, afterLease)
				}
				if fixture.deleter.calls != 0 {
					t.Fatalf("proof-first heartbeat provider calls=%d, want 0", fixture.deleter.calls)
				}
			})

			t.Run("progress", func(t *testing.T) {
				fixture := seedProviderDeleteProofFirstFixture(t, base+20, claimState)
				var before model.RecoveryPointLifecycleEffectClaim
				if err := fixture.db.First(&before, "attempt_id = ?", fixture.attempt.ID).Error; err != nil {
					t.Fatalf("load proof-first claim before progress: %v", err)
				}
				got, err := fixture.coordinator.progressProviderProof(context.Background(), fixture.attempt.ID)
				if err != nil || got.Phase != backupasset.LifecyclePhaseTombstoning {
					t.Fatalf("proof-first progress attempt=%+v error=%v, want tombstoning", got, err)
				}
				var after model.RecoveryPointLifecycleEffectClaim
				if err := fixture.db.First(&after, "attempt_id = ?", fixture.attempt.ID).Error; err != nil {
					t.Fatalf("load proof-first claim after progress: %v", err)
				}
				if before.State != after.State || before.ExecutionID != after.ExecutionID ||
					before.LeaseID != after.LeaseID || before.LeaseFenceTokenHash != after.LeaseFenceTokenHash ||
					fixture.deleter.calls != 0 {
					t.Fatalf("proof-first progress mutated claim/provider: before=%+v after=%+v calls=%d",
						before, after, fixture.deleter.calls)
				}
			})

			t.Run("completion", func(t *testing.T) {
				fixture := seedProviderDeleteProofFirstFixture(t, base+30, claimState)
				if _, err := fixture.coordinator.progressProviderProof(context.Background(), fixture.attempt.ID); err != nil {
					t.Fatalf("progress proof before completion: %v", err)
				}
				got, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
				if err != nil || got.Phase != backupasset.LifecyclePhaseComplete {
					t.Fatalf("proof-first completion attempt=%+v error=%v, want complete", got, err)
				}
				var claim model.RecoveryPointLifecycleEffectClaim
				if err := fixture.db.First(&claim, "attempt_id = ?", fixture.attempt.ID).Error; err != nil {
					t.Fatalf("load proof-first claim after completion: %v", err)
				}
				if claim.State != claimState || fixture.deleter.calls != 0 {
					t.Fatalf("proof-first completion claim/provider state=%q/calls=%d, want %q/0",
						claim.State, fixture.deleter.calls, claimState)
				}
			})

			t.Run("settled audit", func(t *testing.T) {
				fixture := seedProviderDeleteProofFirstFixture(t, base+40, claimState)
				audit := &recordingSettledAudit{}
				var gated model.RecoveryPointLifecycleAttempt
				if err := fixture.db.First(&gated, "id = ?", fixture.attempt.ID).Error; err != nil {
					t.Fatalf("load proof-first settled audit retry gate: %v", err)
				}
				if gated.RetryAt == nil {
					t.Fatal("proof-first settled audit retry gate is nil")
				}
				fixture.clock = gated.RetryAt.UTC()
				fixture.coordinator.audit = audit
				got, err := fixture.coordinator.flushDueSettledAuditBeforeHeartbeat(
					context.Background(), fixture.attempt,
				)
				if err != nil || got {
					t.Fatalf("proof-first settled audit pending=%v error=%v, want emitted", got, err)
				}
				attempt, err := fixture.coordinator.loadAttempt(context.Background(), fixture.attempt.ID)
				if err != nil {
					t.Fatalf("load proof-first attempt after settled audit: %v", err)
				}
				var claim model.RecoveryPointLifecycleEffectClaim
				if err := fixture.db.First(&claim, "attempt_id = ?", fixture.attempt.ID).Error; err != nil {
					t.Fatalf("load proof-first claim after settled audit: %v", err)
				}
				var slots int64
				if err := fixture.db.Model(&model.RecoveryPointLifecycleAuditSlot{}).
					Where("attempt_id = ?", fixture.attempt.ID).Count(&slots).Error; err != nil {
					t.Fatalf("count proof-first settled audit slots: %v", err)
				}
				if attempt.Phase != backupasset.LifecyclePhaseTombstoning ||
					attempt.RetryAt != nil || claim.State != claimState ||
					len(audit.events) != 1 || slots != 1 || fixture.deleter.calls != 0 {
					t.Fatalf("proof-first settled audit attempt=%+v claim=%q events=%d slots=%d calls=%d",
						attempt, claim.State, len(audit.events), slots, fixture.deleter.calls)
				}
			})
		})
	}
}

func TestLifecycleProviderDeleteProofValidationFailsClosedSQLite(t *testing.T) {
	t.Run("contradictory tombstone", func(t *testing.T) {
		fixture := seedProviderDeleteProofFirstFixture(t, 1550, "in_flight")
		if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
			Where("recovery_point_id = ?", fixture.pointID).
			Update("terminal_state", "contradictory").Error; err != nil {
			t.Fatalf("corrupt proof-first tombstone: %v", err)
		}
		got, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		if err == nil || !errors.Is(err, backupasset.ErrInvalidState) ||
			got.Phase != backupasset.LifecyclePhaseProviderDelete || fixture.deleter.calls != 0 {
			t.Fatalf("contradictory tombstone attempt=%+v calls=%d error=%v, want fail closed",
				got, fixture.deleter.calls, err)
		}
	})

	t.Run("proven claim without tombstone", func(t *testing.T) {
		fixture := seedProviderDeleteProofFirstFixture(t, 1560, "in_flight")
		if err := fixture.db.Where("recovery_point_id = ?", fixture.pointID).
			Delete(&model.RecoveryPointLifecycleTombstone{}).Error; err != nil {
			t.Fatalf("remove proof-first tombstone: %v", err)
		}
		if err := fixture.db.Model(&model.RecoveryPointLifecycleEffectClaim{}).
			Where("attempt_id = ?", fixture.attempt.ID).
			Update("state", "proven").Error; err != nil {
			t.Fatalf("corrupt proof-first claim state: %v", err)
		}
		got, err := fixture.coordinator.Advance(context.Background(), fixture.attempt.ID)
		if err == nil || !errors.Is(err, backupasset.ErrInvalidState) ||
			got.Phase != backupasset.LifecyclePhaseProviderDelete || fixture.deleter.calls != 0 {
			t.Fatalf("proven claim without tombstone attempt=%+v calls=%d error=%v, want fail closed",
				got, fixture.deleter.calls, err)
		}
	})
}

func TestLifecycleBlockedProviderAuditFailureRetriesBeforeLeavingBlocked(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 1420)
	fixture.deleter.prepareErr = ErrPointDeletionWORM
	audit := &flakySettledAudit{failLeft: 1}
	fixture.coordinator.audit = audit

	attempt := fixture.attempt
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete,
	} {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("Advance to %s attempt=%+v error=%v", want, attempt, err)
		}
	}

	attempt, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked {
		t.Fatalf("provider observer block attempt=%+v error=%v, want blocked", attempt, err)
	}
	if audit.settledCalls != 0 {
		t.Fatalf("settled audit writes before due gate=%d, want 0", audit.settledCalls)
	}
	fixture.clock = attempt.RetryAt.Add(time.Second)
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err == nil {
		t.Fatal("expected due blocked audit failure")
	}
	if attempt.Phase != backupasset.LifecyclePhaseBlocked {
		t.Fatalf("phase after failed blocked audit = %q, want %q", attempt.Phase, backupasset.LifecyclePhaseBlocked)
	}
	if audit.settledCalls != 0 {
		t.Fatalf("settled audit writes = %d, want 0", audit.settledCalls)
	}
	if attempt.RetryAt == nil || !attempt.RetryAt.After(fixture.clock) {
		t.Fatalf("retry_at after blocked audit failure=%v, want future", attempt.RetryAt)
	}

	audit.failLeft = 0
	fixture.clock = attempt.RetryAt.Add(time.Second)
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil {
		t.Fatalf("retry after healthy blocked audit: %v", err)
	}
	if audit.settledCalls != 1 {
		t.Fatalf("settled audit writes = %d, want 1", audit.settledCalls)
	}
	if len(audit.events) == 0 || audit.events[0].Fields[backupasset.AuditFieldStatus] != "blocked" {
		t.Fatalf("blocked audit=%+v, want settled blocked status", audit.events)
	}
	if attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		t.Fatalf("attempt after successful blocked audit=%q, want provider_delete", attempt.Phase)
	}
}

func TestLifecycleHealthyBlockedTickDoesNotRewriteSettledAudit(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 1520)
	fixture.deleter.prepareErr = ErrPointDeletionWORM
	audit := &recordingSettledAudit{}
	fixture.coordinator.audit = audit

	attempt := fixture.attempt
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete,
	} {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("Advance to %s attempt=%+v error=%v", want, attempt, err)
		}
	}
	attempt, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked {
		t.Fatalf("provider observer block attempt=%+v error=%v, want blocked", attempt, err)
	}
	if len(audit.events) != 0 {
		t.Fatalf("settled audit before RetryAt=%d, want 0", len(audit.events))
	}
	fixture.clock = attempt.RetryAt.Add(time.Second)
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		t.Fatalf("healthy blocked retry attempt=%+v error=%v, want provider_delete", attempt, err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("settled audit writes=%d, want 1", len(audit.events))
	}
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked {
		t.Fatalf("healthy blocked revisit attempt=%+v error=%v, want blocked", attempt, err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("healthy blocked tick rewrote settled audit writes=%d, want 1", len(audit.events))
	}
}

func TestLifecycleSettledAuditAllowsSameTimestampObservationAndTerminal(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 7280)
	audit := &recordingSettledAudit{}
	fixture.coordinator.audit = audit
	// Keep the injected clock fixed. A zero retry delay makes the
	// observation eligible immediately without advancing time.
	fixedNow := fixture.clock
	fixture.coordinator.retryDelay = 0

	attempt := fixture.attempt
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete,
	} {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("Advance to %s attempt=%+v error=%v", want, attempt, err)
		}
	}
	preparation, err := fixture.coordinator.prepareProviderDelete(context.Background(), attempt.ID)
	if err != nil || !preparation.acquired {
		t.Fatalf("prepare same-time claim preparation=%+v error=%v", preparation, err)
	}
	claimUpdate := fixture.db.Model(&model.RecoveryPointLifecycleEffectClaim{}).
		Where("attempt_id = ? AND state = ?", attempt.ID, "in_flight").
		Update("state", "uncertain")
	if claimUpdate.Error != nil || claimUpdate.RowsAffected != 1 {
		t.Fatalf("mark same-time claim uncertain rows=%d error=%v", claimUpdate.RowsAffected, claimUpdate.Error)
	}
	retryUpdate := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ?", attempt.ID).Update("retry_at", fixedNow)
	if retryUpdate.Error != nil || retryUpdate.RowsAffected != 1 {
		t.Fatalf("make same-time claim retryable rows=%d error=%v", retryUpdate.RowsAffected, retryUpdate.Error)
	}
	fixture.deleter.prepareErr = provider.ErrDeletePointNativeVersionReferenced
	blocked, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked {
		t.Fatalf("provider observer block attempt=%+v error=%v, want blocked", blocked, err)
	}
	if len(audit.events) != 1 || audit.events[0].Fields[backupasset.AuditFieldStatus] != "blocked" {
		t.Fatalf("same-time observation events=%+v, want one blocked event", audit.events)
	}

	fixture.deleter.prepareErr = nil
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("a", 64),
	}
	tombstoning, err := fixture.coordinator.Advance(context.Background(), blocked.ID)
	if err != nil || tombstoning.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("same-time terminal attempt=%+v error=%v, want tombstoning", tombstoning, err)
	}
	if len(audit.events) != 2 || audit.events[1].Fields[backupasset.AuditFieldStatus] != "deleted" {
		t.Fatalf("same-time terminal events=%+v, want blocked then deleted", audit.events)
	}

	var slots []model.RecoveryPointLifecycleAuditSlot
	if err := fixture.db.Where("attempt_id = ?", blocked.ID).Order("created_at ASC").Find(&slots).Error; err != nil {
		t.Fatalf("load same-time settled audit slots: %v", err)
	}
	if len(slots) != 2 || slots[0].Status != "blocked" || slots[1].Status != "deleted" ||
		!slots[0].EmittedAt.UTC().Equal(fixedNow.UTC()) || !slots[1].EmittedAt.UTC().Equal(fixedNow.UTC()) {
		t.Fatalf("same-time settled audit slots=%+v, want blocked/deleted at %s", slots, fixedNow)
	}

	beforeWrites := len(audit.events)
	if err := fixture.coordinator.writeSettledDeletionAudit(context.Background(), tombstoning); err != nil {
		t.Fatalf("idempotent settled audit reread: %v", err)
	}
	if pending, err := fixture.coordinator.flushDueSettledAuditBeforeHeartbeat(context.Background(), tombstoning); err != nil || pending {
		t.Fatalf("worker pre-heartbeat settled audit flush pending=%v error=%v", pending, err)
	}
	if len(audit.events) != beforeWrites {
		t.Fatalf("idempotent settled audit reread/flush writes=%d, want unchanged %d", len(audit.events), beforeWrites)
	}

	completed, err := fixture.coordinator.Advance(context.Background(), tombstoning.ID)
	if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete {
		t.Fatalf("same-time settled lifecycle completion attempt=%+v error=%v, want complete", completed, err)
	}
	if len(audit.events) != beforeWrites {
		t.Fatalf("normal completion rewrote settled audit writes=%d, want unchanged %d", len(audit.events), beforeWrites)
	}
}

func TestLifecycleBlockedAuditRetriesAfterReasonChangesToHold(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 1620)
	fixture.deleter.prepareErr = ErrPointDeletionWORM
	audit := &flakySettledAudit{failLeft: 1}
	fixture.coordinator.audit = audit

	attempt := fixture.attempt
	for _, want := range []backupasset.LifecyclePhase{
		backupasset.LifecyclePhaseRevoking,
		backupasset.LifecyclePhaseDraining,
		backupasset.LifecyclePhaseCleaning,
		backupasset.LifecyclePhaseProviderDelete,
	} {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil || attempt.Phase != want {
			t.Fatalf("Advance to %s attempt=%+v error=%v", want, attempt, err)
		}
	}
	attempt, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseBlocked {
		t.Fatalf("provider observer block attempt=%+v error=%v, want blocked", attempt, err)
	}
	if audit.settledCalls != 0 {
		t.Fatalf("settled audit writes before due gate=%d, want 0", audit.settledCalls)
	}
	fixture.clock = attempt.RetryAt.Add(time.Second)
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err == nil {
		t.Fatal("expected due blocked audit failure")
	}
	if audit.settledCalls != 0 {
		t.Fatalf("settled audit writes=%d, want 0", audit.settledCalls)
	}
	if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ?", attempt.ID).
		Update("blocked_reason", backupasset.LifecycleBlockedActiveHold).Error; err != nil {
		t.Fatalf("change blocked reason: %v", err)
	}
	audit.failLeft = 0
	fixture.clock = attempt.RetryAt.Add(time.Second)
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil {
		t.Fatalf("retry missed blocked audit after reason change: %v", err)
	}
	if audit.settledCalls != 1 {
		t.Fatalf("settled audit writes=%d, want 1 after reason change", audit.settledCalls)
	}
	if len(audit.events) == 0 || audit.events[0].Fields[backupasset.AuditFieldStatus] != "blocked" {
		t.Fatalf("blocked audit=%+v, want settled blocked status", audit.events)
	}
}

func TestRegistryPointDeletionRejectsActiveHoldCommittedAfterPreparedAuthority(t *testing.T) {
	fixture := newDirectRegistryDeletionFixture(t, registryDeletePointResolver{}, provider.DeletePointDeleted, nil)
	_, authority, blocked, err := fixture.coordinator.prepareExternalEffect(
		context.Background(), fixture.attemptID, backupasset.LifecyclePhaseProviderDelete,
	)
	if err != nil || blocked {
		t.Fatalf("prepareExternalEffect authority=%+v blocked=%t error=%v", authority, blocked, err)
	}
	if err := fixture.db.Create(&model.RecoveryPointHold{
		ID: testOpaqueID(1704), RecoveryPointID: fixture.pointID,
		HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
		EncryptedReason: "active hold after prepared authority", CreatedBy: 1,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}).Error; err != nil {
		t.Fatalf("commit active hold after prepared authority: %v", err)
	}
	request := LifecyclePointRequest{
		RecoveryPointID: fixture.pointID, AttemptID: fixture.attemptID,
		Operation: backupasset.LifecycleRetentionExpire, authority: authority,
	}
	_, err = runRegistryPointDeletionProtocol(context.Background(), fixture.adapter, request)
	if !errors.Is(err, ErrPointDeletionIdentityConflict) {
		t.Fatalf("active hold error=%v, want ErrPointDeletionIdentityConflict", err)
	}
	if fixture.deleter.calls != 0 {
		t.Fatalf("active hold invoked provider deleter %d times", fixture.deleter.calls)
	}
}

func TestRegistryPointDeletionRevalidatesResolverMutationsInsideTransaction(t *testing.T) {
	for _, mutation := range []struct {
		name  string
		point bool
		repo  bool
	}{
		{name: "point", point: true},
		{name: "repository", repo: true},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			fixture := newDirectRegistryDeletionFixture(t, transactionalMutationDeleteResolver{
				mutatePoint: mutation.point, mutateRepository: mutation.repo,
			}, provider.DeletePointDeleted, nil)
			_, err := runRegistryPointDeletionProtocol(context.Background(), fixture.adapter, fixture.request)
			if !errors.Is(err, ErrPointDeletionIdentityConflict) {
				t.Fatalf("resolver %s mutation error=%v, want identity conflict", mutation.name, err)
			}
			if fixture.deleter.calls != 0 {
				t.Fatalf("resolver %s mutation invoked provider deleter %d times", mutation.name, fixture.deleter.calls)
			}
			var point model.RecoveryPoint
			if err := fixture.db.First(&point, "id = ?", fixture.pointID).Error; err != nil {
				t.Fatalf("reload point after rollback: %v", err)
			}
			if point.SourceFingerprint != strings.Repeat("c", 64) {
				t.Fatalf("resolver %s mutation committed point source=%q", mutation.name, point.SourceFingerprint)
			}
		})
	}
}

func TestRegistryPointDeletionPreservesProviderErrorMappings(t *testing.T) {
	providerError := errors.New("provider temporarily unavailable")
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "ordinary provider error", err: providerError, want: providerError},
		{name: "worm", err: provider.ErrDeletePointWORM, want: ErrPointDeletionWORM},
		{name: "native version referenced", err: provider.ErrDeletePointNativeVersionReferenced, want: provider.ErrDeletePointNativeVersionReferenced},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectRegistryDeletionFixture(t, registryDeletePointResolver{}, provider.DeletePointDeleted, test.err)
			_, err := runRegistryPointDeletionProtocol(context.Background(), fixture.adapter, fixture.request)
			if !errors.Is(err, test.want) {
				t.Fatalf("provider error=%v, want errors.Is(%v)", err, test.want)
			}
			if fixture.deleter.calls != 1 {
				t.Fatalf("provider error deleter calls=%d, want 1", fixture.deleter.calls)
			}
		})
	}
}

func TestRegistryPointDeletionSucceedsWithPreparedTransactionAuthority(t *testing.T) {
	fixture := newDirectRegistryDeletionFixture(t, registryDeletePointResolver{}, provider.DeletePointDeleted, nil)
	result, err := runRegistryPointDeletionProtocol(context.Background(), fixture.adapter, fixture.request)
	if err != nil {
		t.Fatalf("registry point deletion: %v", err)
	}
	if result.Outcome != PointDeletionDeleted || result.ReceiptDigest != strings.Repeat("d", 64) {
		t.Fatalf("registry point deletion result=%+v", result)
	}
	if fixture.deleter.calls != 1 {
		t.Fatalf("successful registry deleter calls=%d, want 1", fixture.deleter.calls)
	}
}

func TestRegistryPointDeletionRejectsPostEffectResolvedAccessDrift(t *testing.T) {
	resolver := &sequencedDeletePointResolver{}
	fixture := newDirectRegistryDeletionFixture(t, resolver, provider.DeletePointDeleted, nil)
	_, err := runRegistryPointDeletionProtocol(context.Background(), fixture.adapter, fixture.request)
	if !errors.Is(err, ErrPointDeletionIdentityConflict) {
		t.Fatalf("post-effect resolved access drift error=%v, want ErrPointDeletionIdentityConflict", err)
	}
	if resolver.calls != 2 {
		t.Fatalf("resolver calls=%d, want pre-effect and post-effect resolution", resolver.calls)
	}
	if fixture.deleter.calls != 1 {
		t.Fatalf("provider calls=%d, want one completed effect before drift rejection", fixture.deleter.calls)
	}
}

func TestRegistryPointDeletionRejectsPostEffectNativeAuthorityDigestDrift(t *testing.T) {
	resolver := &sequencedDeletePointResolver{native: true}
	fixture := newDirectRegistryDeletionFixture(t, resolver, provider.DeletePointDeleted, nil)
	_, err := runRegistryPointDeletionProtocol(context.Background(), fixture.adapter, fixture.request)
	if !errors.Is(err, ErrPointDeletionIdentityConflict) {
		t.Fatalf("post-effect native authority drift error=%v, want ErrPointDeletionIdentityConflict", err)
	}
	if resolver.calls != 2 {
		t.Fatalf("native resolver calls=%d, want pre-effect and post-effect resolution", resolver.calls)
	}
	if fixture.deleter.calls != 1 {
		t.Fatalf("native provider calls=%d, want one completed effect before drift rejection", fixture.deleter.calls)
	}
}

func TestRegistryPointDeletionAllowsIndependentRepositoryWriteDuringProviderDelete(t *testing.T) {
	fixture := newDirectRegistryDeletionFixture(t, registryDeletePointResolver{}, provider.DeletePointDeleted, nil)
	var point model.RecoveryPoint
	if err := fixture.db.Select("repository_id").First(&point, "id = ?", fixture.pointID).Error; err != nil {
		t.Fatal(err)
	}
	fixture.deleter.entered = make(chan struct{})
	fixture.deleter.release = make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		_, err := runRegistryPointDeletionProtocol(context.Background(), fixture.adapter, fixture.request)
		deleteDone <- err
	}()
	<-fixture.deleter.entered

	writerDone := make(chan error, 1)
	go func() {
		writerDone <- fixture.db.Model(&model.BackupRepository{}).Where("id = ?", point.RepositoryID).
			Update("description", "independent writer completed").Error
	}()
	select {
	case writerErr := <-writerDone:
		if writerErr != nil {
			close(fixture.deleter.release)
			<-deleteDone
			t.Fatalf("repository writer failed while provider deletion was paused: %v", writerErr)
		}
	case <-time.After(time.Second):
		close(fixture.deleter.release)
		<-deleteDone
		t.Fatal("repository writer did not complete while provider deletion was paused")
	}

	close(fixture.deleter.release)
	if err := <-deleteDone; err != nil {
		t.Fatalf("provider deletion after independent repository write: %v", err)
	}
	var repository model.BackupRepository
	if err := fixture.db.First(&repository, "id = ?", point.RepositoryID).Error; err != nil {
		t.Fatalf("reload repository after provider deletion: %v", err)
	}
	if repository.Description != "independent writer completed" {
		t.Fatalf("repository description=%q, want independent writer update", repository.Description)
	}
}

func TestRegistryPointDeletionReturnsReceiptWhenHoldAppearsAfterProviderEffect(t *testing.T) {
	fixture := newDirectRegistryDeletionFixture(t, registryDeletePointResolver{}, provider.DeletePointDeleted, nil)
	fixture.deleter.entered = make(chan struct{})
	fixture.deleter.release = make(chan struct{})
	type deletionOutcome struct {
		result PointDeletionResult
		err    error
	}
	deleteDone := make(chan deletionOutcome, 1)
	go func() {
		result, err := runRegistryPointDeletionProtocol(context.Background(), fixture.adapter, fixture.request)
		deleteDone <- deletionOutcome{result: result, err: err}
	}()
	<-fixture.deleter.entered

	if err := fixture.db.Create(&model.RecoveryPointHold{
		ID: testOpaqueID(1710), RecoveryPointID: fixture.pointID,
		HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
		EncryptedReason: "active hold after provider effect", CreatedBy: 1,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}).Error; err != nil {
		close(fixture.deleter.release)
		<-deleteDone
		t.Fatalf("commit post-effect active hold: %v", err)
	}
	close(fixture.deleter.release)
	outcome := <-deleteDone
	if outcome.err != nil {
		t.Fatalf("post-effect active hold discarded provider receipt: %v", outcome.err)
	}
	if outcome.result.Outcome != PointDeletionDeleted ||
		outcome.result.ReceiptDigest != strings.Repeat("d", 64) {
		t.Fatalf("post-effect active hold result=%+v, want settled deletion receipt", outcome.result)
	}
	if fixture.deleter.calls != 1 {
		t.Fatalf("post-effect active hold provider calls=%d, want 1", fixture.deleter.calls)
	}
}

func TestRegistryPointDeletionRejectsPostEffectDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*directRegistryDeletionFixture) error
	}{
		{
			name: "authority",
			mutate: func(fixture *directRegistryDeletionFixture) error {
				return fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).
					Where("id = ?", fixture.attemptID).
					Update("transition_revision", 2).Error
			},
		},
		{
			name: "request",
			mutate: func(fixture *directRegistryDeletionFixture) error {
				return fixture.db.Model(&model.RecoveryPoint{}).
					Where("id = ?", fixture.pointID).
					Update("source_fingerprint", strings.Repeat("e", 64)).Error
			},
		},
		{
			name: "repository fence",
			mutate: func(fixture *directRegistryDeletionFixture) error {
				var point model.RecoveryPoint
				if err := fixture.db.Select("repository_id").First(&point, "id = ?", fixture.pointID).Error; err != nil {
					return err
				}
				return fixture.db.Model(&model.BackupRepository{}).
					Where("id = ?", point.RepositoryID).
					Update("capability_revision", 2).Error
			},
		},
		{
			name: "provider locator",
			mutate: func(fixture *directRegistryDeletionFixture) error {
				return fixture.db.Model(&model.RecoveryPoint{}).
					Where("id = ?", fixture.pointID).
					Update("encrypted_provider_locator", `{"snapshot":"drifted-private-locator"}`).Error
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectRegistryDeletionFixture(t, registryDeletePointResolver{}, provider.DeletePointDeleted, nil)
			fixture.deleter.entered = make(chan struct{})
			fixture.deleter.release = make(chan struct{})
			deleteDone := make(chan error, 1)
			go func() {
				_, err := runRegistryPointDeletionProtocol(context.Background(), fixture.adapter, fixture.request)
				deleteDone <- err
			}()
			<-fixture.deleter.entered

			if err := test.mutate(&fixture); err != nil {
				close(fixture.deleter.release)
				<-deleteDone
				t.Fatalf("post-effect %s mutation: %v", test.name, err)
			}
			close(fixture.deleter.release)
			if err := <-deleteDone; !errors.Is(err, ErrPointDeletionIdentityConflict) {
				t.Fatalf("post-effect %s drift error=%v, want ErrPointDeletionIdentityConflict", test.name, err)
			}
			if fixture.deleter.calls != 1 {
				t.Fatalf("post-effect %s drift provider calls=%d, want 1", test.name, fixture.deleter.calls)
			}
		})
	}
}

func TestRegistryPointDeletionRejectsInvalidProviderReceipts(t *testing.T) {
	for _, test := range []struct {
		name   string
		result provider.DeletePointResult
	}{
		{
			name:   "deleted missing receipt",
			result: provider.DeletePointResult{Outcome: provider.DeletePointDeleted},
		},
		{
			name: "already absent malformed receipt",
			result: provider.DeletePointResult{
				Outcome: provider.DeletePointAlreadyAbsent, ReceiptDigest: strings.Repeat("A", 64),
			},
		},
		{
			name: "unknown outcome",
			result: provider.DeletePointResult{
				Outcome: provider.DeletePointOutcome("unknown"), ReceiptDigest: strings.Repeat("a", 64),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectRegistryDeletionFixture(t, registryDeletePointResolver{}, provider.DeletePointDeleted, nil)
			fixture.deleter.result = test.result
			_, err := runRegistryPointDeletionProtocol(context.Background(), fixture.adapter, fixture.request)
			if !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("invalid provider result=%+v error=%v, want ErrInvalidState", test.result, err)
			}
			if fixture.deleter.calls != 1 {
				t.Fatalf("invalid provider result deleter calls=%d, want 1", fixture.deleter.calls)
			}
		})
	}
}

func TestRegistryPointDeletionExecuteStopsBeforeCanceledRegisteredProvider(t *testing.T) {
	fixture := newDirectRegistryDeletionFixture(t, registryDeletePointResolver{}, provider.DeletePointDeleted, nil)
	var prepared preparedPointDeletion
	if err := fixture.db.Transaction(func(tx *gorm.DB) error {
		rows, err := lockLifecycleDeleteRowsTx(context.Background(), tx, fixture.request, false)
		if err != nil {
			return err
		}
		prepared, err = fixture.adapter.Prepare(
			context.Background(), tx, providerDeletePrepareObserver, fixture.request, rows,
		)
		return err
	}); err != nil {
		t.Fatalf("prepare direct registry deletion: %v", err)
	}
	bounded, cancel, err := fixture.coordinator.providerDeleteBoundContext(
		context.Background(), fixture.now.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("bound provider context: %v", err)
	}
	cancel()
	execution, err := fixture.adapter.Execute(bounded, prepared)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled registered provider error=%v, want context.Canceled", err)
	}
	if execution.Stage != providerDeleteStageProvider || execution.ProviderCalled {
		t.Fatalf("canceled registered provider execution=%+v, want provider_invoke/false", execution)
	}
	if fixture.deleter.calls != 0 {
		t.Fatalf("canceled registered provider calls=%d, want zero", fixture.deleter.calls)
	}
}

func TestLifecycleProviderReceiptVerifyUsesLeaseBoundContextAndFreshDeadline(t *testing.T) {
	for index, advanceClock := range []bool{false, true} {
		name := "live"
		if advanceClock {
			name = "absolute_deadline"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, 6000+uint64(index)*10)
			fixture.deleter.result = PointDeletionResult{
				Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("a", 64),
			}
			attempt := fixture.attempt
			for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil {
					t.Fatalf("advance to provider delete: %v", err)
				}
			}
			var lease model.RecoveryPointLease
			if err := fixture.db.First(&lease, "id = ?", attempt.LeaseID).Error; err != nil {
				t.Fatalf("load provider lease: %v", err)
			}
			fixture.deleter.verify = func(ctx context.Context) {
				deadline, ok := ctx.Deadline()
				if !ok || !deadline.Equal(lease.AbsoluteDeadline.UTC()) {
					t.Errorf("verify deadline=%s present=%t, want lease absolute deadline=%s",
						deadline, ok, lease.AbsoluteDeadline.UTC())
				}
				if advanceClock {
					fixture.clock = lease.AbsoluteDeadline.UTC()
				}
			}
			got, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
			if !advanceClock {
				if err != nil || got.Phase != backupasset.LifecyclePhaseTombstoning {
					t.Fatalf("live verify attempt=%+v error=%v, want tombstoning", got, err)
				}
				if fixture.deleter.verifyCalls != 1 {
					t.Fatalf("live verify calls=%d, want one", fixture.deleter.verifyCalls)
				}
				return
			}
			if err == nil || got.Phase != backupasset.LifecyclePhaseProviderDelete {
				t.Fatalf("deadline-crossing verify attempt=%+v error=%v, want uncertain provider delete", got, err)
			}
			var claim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.First(&claim, "attempt_id = ?", attempt.ID).Error; err != nil {
				t.Fatalf("load deadline-crossing claim: %v", err)
			}
			if claim.State != "uncertain" {
				t.Fatalf("deadline-crossing claim state=%q, want uncertain", claim.State)
			}
			var tombstoneCount int64
			if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
				Where("recovery_point_id = ?", fixture.pointID).Count(&tombstoneCount).Error; err != nil {
				t.Fatalf("count deadline-crossing tombstones: %v", err)
			}
			if tombstoneCount != 0 {
				t.Fatalf("deadline-crossing verify tombstones=%d, want zero", tombstoneCount)
			}
		})
	}
}
func TestLifecycleTakeoverRenewsActiveShortLeaseAtHorizon(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 6400)
	attempt := fixture.attempt
	for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance short-live takeover fixture: %v", err)
		}
	}
	fixture.deleter.err = errors.New("seed uncertain provider execution")
	failed, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	fixture.deleter.err = nil
	if err == nil || failed.Phase != backupasset.LifecyclePhaseProviderDelete || failed.RetryAt == nil {
		t.Fatalf("seed short-live takeover claim attempt=%+v error=%v", failed, err)
	}
	fixture.clock = failed.RetryAt.UTC().Add(time.Second)
	var beforeClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&beforeClaim, "attempt_id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load short-live takeover claim: %v", err)
	}
	var beforeLease model.RecoveryPointLease
	if err := fixture.db.First(&beforeLease, "id = ?", beforeClaim.LeaseID).Error; err != nil {
		t.Fatalf("load short-live takeover lease: %v", err)
	}
	nearExpiry := fixture.clock.Add(time.Second)
	if err := fixture.db.Model(&model.RecoveryPointLease{}).Where("id = ?", beforeLease.ID).
		Update("lease_expires_at", nearExpiry).Error; err != nil {
		t.Fatalf("set short-live near-expiry lease: %v", err)
	}
	if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attempt.ID).
		Update("retry_at", fixture.clock.Add(-time.Second)).Error; err != nil {
		t.Fatalf("make short-live takeover retry due: %v", err)
	}
	if err := fixture.db.First(&beforeLease, "id = ?", beforeLease.ID).Error; err != nil {
		t.Fatalf("reload short-live near-expiry lease: %v", err)
	}
	beforeAttempt, err := fixture.coordinator.loadAttempt(context.Background(), attempt.ID)
	if err != nil {
		t.Fatalf("load short-live near-expiry attempt: %v", err)
	}
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("7", 64),
	}
	beforeProviderCalls := fixture.deleter.calls
	got, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || got.Phase != backupasset.LifecyclePhaseTombstoning ||
		fixture.deleter.calls != beforeProviderCalls+1 {
		t.Fatalf("short-live near-expiry takeover attempt=%+v error=%v provider_calls=%d", got, err, fixture.deleter.calls)
	}
	var afterClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&afterClaim, "attempt_id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load refreshed short-live takeover claim: %v", err)
	}
	var afterLease model.RecoveryPointLease
	if err := fixture.db.First(&afterLease, "id = ?", beforeLease.ID).Error; err != nil {
		t.Fatalf("load refreshed short-live takeover lease: %v", err)
	}
	horizon := fixture.clock.UTC().Add(fixture.coordinator.effectClaimTTL)
	if afterClaim.State != "proven" ||
		afterClaim.LeaseID != beforeClaim.LeaseID ||
		afterClaim.LeaseAttemptID != beforeClaim.LeaseAttemptID ||
		afterClaim.LeaseFenceTokenHash != beforeClaim.LeaseFenceTokenHash ||
		!afterClaim.DeadlineAt.UTC().Equal(horizon) ||
		!afterLease.LeaseExpiresAt.UTC().After(horizon) ||
		!afterLease.AbsoluteDeadline.UTC().Equal(beforeLease.AbsoluteDeadline.UTC()) ||
		afterLease.AttemptID != beforeLease.AttemptID ||
		afterLease.FenceToken != beforeLease.FenceToken ||
		got.TransitionRevision != beforeAttempt.TransitionRevision+1 {
		t.Fatalf("short-live near-expiry horizon claim=%+v before_claim=%+v lease=%+v before_lease=%+v attempt=%+v before_attempt=%+v horizon=%s",
			afterClaim, beforeClaim, afterLease, beforeLease, got, beforeAttempt, horizon)
	}
}
func TestLifecycleHeartbeatInitialLoadFailureIsFailClosed(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 6460)
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("8", 64),
	}
	attempt := fixture.attempt
	for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance heartbeat fixture to provider_delete: %v", err)
		}
	}
	attempt, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil || attempt.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("settle heartbeat fixture: attempt=%+v error=%v", attempt, err)
	}

	var beforeAttempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&beforeAttempt, "id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load heartbeat attempt snapshot: %v", err)
	}
	var beforeLease model.RecoveryPointLease
	if err := fixture.db.First(&beforeLease, "id = ?", beforeAttempt.LeaseID).Error; err != nil {
		t.Fatalf("load heartbeat lease snapshot: %v", err)
	}
	var beforeClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&beforeClaim, "attempt_id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load heartbeat claim snapshot: %v", err)
	}
	var beforeTombstone model.RecoveryPointLifecycleTombstone
	if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?", fixture.pointID, attempt.Operation).
		First(&beforeTombstone).Error; err != nil {
		t.Fatalf("load heartbeat tombstone snapshot: %v", err)
	}

	loadErr := errors.New("heartbeat initial attempt load failed")
	var loadCalls int
	const callbackName = "test:heartbeat-initial-load-failure"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement == nil || query.Statement.Table != "recovery_point_lifecycle_attempts" {
			return
		}
		if _, ok := query.Statement.Dest.(*model.RecoveryPointLifecycleAttempt); !ok || loadCalls != 0 {
			return
		}
		loadCalls++
		_ = query.AddError(loadErr)
	}); err != nil {
		t.Fatalf("register heartbeat initial-load failure: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove heartbeat initial-load failure: %v", err)
		}
	})

	got, err := fixture.coordinator.Heartbeat(context.Background(), attempt.ID)
	if !errors.Is(err, loadErr) || got.ID != "" || loadCalls != 1 {
		t.Fatalf("heartbeat initial-load failure attempt=%+v error=%v load_calls=%d, want wrapped failure/no result/one load",
			got, err, loadCalls)
	}

	var afterAttempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&afterAttempt, "id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load heartbeat attempt after failure: %v", err)
	}
	var afterLease model.RecoveryPointLease
	if err := fixture.db.First(&afterLease, "id = ?", beforeLease.ID).Error; err != nil {
		t.Fatalf("load heartbeat lease after failure: %v", err)
	}
	var afterClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&afterClaim, "attempt_id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load heartbeat claim after failure: %v", err)
	}
	var afterTombstone model.RecoveryPointLifecycleTombstone
	if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?", fixture.pointID, attempt.Operation).
		First(&afterTombstone).Error; err != nil {
		t.Fatalf("load heartbeat tombstone after failure: %v", err)
	}
	if !reflect.DeepEqual(beforeAttempt, afterAttempt) ||
		!reflect.DeepEqual(beforeLease, afterLease) ||
		!reflect.DeepEqual(beforeClaim, afterClaim) ||
		!reflect.DeepEqual(beforeTombstone, afterTombstone) {
		t.Fatalf("heartbeat initial-load failure mutated durable state attempt=%t lease=%t claim=%t tombstone=%t",
			!reflect.DeepEqual(beforeAttempt, afterAttempt),
			!reflect.DeepEqual(beforeLease, afterLease),
			!reflect.DeepEqual(beforeClaim, afterClaim),
			!reflect.DeepEqual(beforeTombstone, afterTombstone))
	}
}
func TestLifecycleTakeoverLeaseErrorsPreserveConflictAndCause(t *testing.T) {
	t.Run("renew preserves lease fence sentinel", func(t *testing.T) {
		fixture := newClaimedExpiryFixture(t, 6480)
		tx := fixture.db.Begin()
		if tx.Error != nil {
			t.Fatalf("begin renew error transaction: %v", tx.Error)
		}
		rows, err := lockProviderDeleteRowsByAttemptTx(context.Background(), tx, fixture.attempt.ID)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("lock renew error rows: %v", err)
		}
		originalFence := rows.lease.FenceToken
		rows.lease.FenceToken = strings.Repeat("f", 64)
		if rows.lease.FenceToken == originalFence {
			rows.lease.FenceToken = strings.Repeat("e", 64)
		}
		err = fixture.coordinator.takeoverProviderLeaseTx(context.Background(), tx, &rows)
		_ = tx.Rollback()
		if err == nil || !errors.Is(err, backupasset.ErrConflict) ||
			!errors.Is(err, backupasset.ErrLeaseFenceLost) ||
			!strings.Contains(err.Error(), string(providerDeleteStageClaimAcquire)) {
			t.Fatalf("renew error=%v, want claim_acquire/ErrConflict/ErrLeaseFenceLost", err)
		}
	})

	t.Run("takeover preserves context sentinel", func(t *testing.T) {
		fixture := newClaimedExpiryFixture(t, 6490)
		result := fixture.db.Model(&model.RecoveryPointLease{}).
			Where("id = ?", fixture.attempt.LeaseID).
			Updates(map[string]any{
				"lease_expires_at":  fixture.clock.Add(-time.Second),
				"absolute_deadline": fixture.clock.Add(time.Hour),
			})
		if result.Error != nil || result.RowsAffected != 1 {
			t.Fatalf("expire takeover error lease rows=%d error=%v", result.RowsAffected, result.Error)
		}
		tx := fixture.db.Begin()
		if tx.Error != nil {
			t.Fatalf("begin takeover error transaction: %v", tx.Error)
		}
		rows, err := lockProviderDeleteRowsByAttemptTx(context.Background(), tx, fixture.attempt.ID)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("lock takeover error rows: %v", err)
		}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		err = fixture.coordinator.takeoverProviderLeaseTx(cancelled, tx, &rows)
		_ = tx.Rollback()
		if err == nil || !errors.Is(err, backupasset.ErrConflict) ||
			!errors.Is(err, context.Canceled) ||
			!strings.Contains(err.Error(), string(providerDeleteStageClaimAcquire)) {
			t.Fatalf("takeover error=%v, want claim_acquire/ErrConflict/context.Canceled", err)
		}
	})
	t.Run("absolute expiry update preserves database sentinel", func(t *testing.T) {
		fixture := newClaimedExpiryFixture(t, 7230)
		var lease model.RecoveryPointLease
		if err := fixture.db.First(&lease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
			t.Fatalf("load absolute-expiry update lease: %v", err)
		}
		fixture.clock = lease.AbsoluteDeadline.UTC().Add(time.Second)
		updateErr := errors.New("takeover absolute-expiry update sentinel")
		var updateCalls int
		const callbackName = "test:takeover-absolute-expiry-update-error"
		if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(query *gorm.DB) {
			if query.Statement == nil || query.Statement.Schema == nil ||
				query.Statement.Schema.Table != (model.RecoveryPointLease{}).TableName() || updateCalls != 0 {
				return
			}
			updateCalls++
			_ = query.AddError(updateErr)
		}); err != nil {
			t.Fatalf("register absolute-expiry update error: %v", err)
		}
		t.Cleanup(func() {
			if err := fixture.db.Callback().Update().Remove(callbackName); err != nil {
				t.Errorf("remove absolute-expiry update error: %v", err)
			}
		})
		tx := fixture.db.Begin()
		if tx.Error != nil {
			t.Fatalf("begin absolute-expiry update error transaction: %v", tx.Error)
		}
		rows, err := lockProviderDeleteRowsByAttemptTx(context.Background(), tx, fixture.attempt.ID)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("lock absolute-expiry update error rows: %v", err)
		}
		err = fixture.coordinator.takeoverProviderLeaseTx(context.Background(), tx, &rows)
		_ = tx.Rollback()
		if err == nil || !errors.Is(err, updateErr) || !errors.Is(err, backupasset.ErrConflict) ||
			!strings.Contains(err.Error(), string(providerDeleteStageClaimAcquire)) || updateCalls != 1 {
			t.Fatalf("absolute-expiry update error=%v update_calls=%d, want claim_acquire/ErrConflict/sentinel/one update", err, updateCalls)
		}
	})

	t.Run("absolute expiry update preserves lost CAS conflict", func(t *testing.T) {
		fixture := newClaimedExpiryFixture(t, 7240)
		var lease model.RecoveryPointLease
		if err := fixture.db.First(&lease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
			t.Fatalf("load absolute-expiry CAS lease: %v", err)
		}
		fixture.clock = lease.AbsoluteDeadline.UTC().Add(time.Second)
		var updateCalls int
		const callbackName = "test:takeover-absolute-expiry-cas"
		if err := fixture.db.Callback().Update().After("gorm:update").Register(callbackName, func(query *gorm.DB) {
			if query.Statement == nil || query.Statement.Schema == nil ||
				query.Statement.Schema.Table != (model.RecoveryPointLease{}).TableName() || updateCalls != 0 {
				return
			}
			updateCalls++
			query.RowsAffected = 0
		}); err != nil {
			t.Fatalf("register absolute-expiry CAS: %v", err)
		}
		t.Cleanup(func() {
			if err := fixture.db.Callback().Update().Remove(callbackName); err != nil {
				t.Errorf("remove absolute-expiry CAS: %v", err)
			}
		})
		tx := fixture.db.Begin()
		if tx.Error != nil {
			t.Fatalf("begin absolute-expiry CAS transaction: %v", tx.Error)
		}
		rows, err := lockProviderDeleteRowsByAttemptTx(context.Background(), tx, fixture.attempt.ID)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("lock absolute-expiry CAS rows: %v", err)
		}
		err = fixture.coordinator.takeoverProviderLeaseTx(context.Background(), tx, &rows)
		_ = tx.Rollback()
		if err == nil || !errors.Is(err, backupasset.ErrConflict) ||
			!strings.Contains(err.Error(), string(providerDeleteStageClaimAcquire)) || updateCalls != 1 {
			t.Fatalf("absolute-expiry CAS error=%v update_calls=%d, want claim_acquire/ErrConflict/one update", err, updateCalls)
		}
	})

	t.Run("fresh acquisition after reconciled expiry preserves database sentinel", func(t *testing.T) {
		fixture := newClaimedExpiryFixture(t, 7250)
		attempt := fixture.attempt
		for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
			var err error
			attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil {
				t.Fatalf("advance fresh-acquisition fixture to provider_delete: %v", err)
			}
		}
		fixture.attempt = attempt
		preparation, err := fixture.coordinator.prepareProviderDelete(context.Background(), attempt.ID)
		if err != nil || !preparation.acquired {
			t.Fatalf("prepare fresh-acquisition fixture preparation=%+v error=%v", preparation, err)
		}
		var oldLease model.RecoveryPointLease
		if err := fixture.db.First(&oldLease, "id = ?", attempt.LeaseID).Error; err != nil {
			t.Fatalf("load fresh-acquisition lease: %v", err)
		}
		fixture.clock = oldLease.AbsoluteDeadline.UTC().Add(time.Second)
		reconciled, err := fixture.coordinator.leases.ReconcileExpired(context.Background())
		if err != nil || reconciled != 1 {
			t.Fatalf("reconcile fresh-acquisition lease count=%d error=%v, want one", reconciled, err)
		}
		acquireErr := errors.New("fresh takeover acquisition sentinel")
		var acquireCalls int
		const callbackName = "test:fresh-takeover-acquisition-error"
		if err := fixture.db.Callback().Create().Before("gorm:create").Register(callbackName, func(query *gorm.DB) {
			if query.Statement == nil || query.Statement.Schema == nil ||
				query.Statement.Schema.Table != (model.RecoveryPointLease{}).TableName() || acquireCalls != 0 {
				return
			}
			acquireCalls++
			_ = query.AddError(acquireErr)
		}); err != nil {
			t.Fatalf("register fresh-acquisition error: %v", err)
		}
		t.Cleanup(func() {
			if err := fixture.db.Callback().Create().Remove(callbackName); err != nil {
				t.Errorf("remove fresh-acquisition error: %v", err)
			}
		})
		tx := fixture.db.Begin()
		if tx.Error != nil {
			t.Fatalf("begin fresh-acquisition transaction: %v", tx.Error)
		}
		rows, err := lockProviderDeleteRowsByAttemptTx(context.Background(), tx, attempt.ID)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("lock fresh-acquisition rows: %v", err)
		}
		err = fixture.coordinator.takeoverProviderLeaseTx(context.Background(), tx, &rows)
		_ = tx.Rollback()
		if err == nil || !errors.Is(err, acquireErr) || !errors.Is(err, backupasset.ErrConflict) ||
			!strings.Contains(err.Error(), string(providerDeleteStageClaimAcquire)) || acquireCalls != 1 {
			t.Fatalf("fresh acquisition error=%v acquire_calls=%d, want claim_acquire/ErrConflict/sentinel/one create", err, acquireCalls)
		}
	})

}

func TestLifecycleProviderDeleteReceiptRejectsForeignLeaseAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate map[string]any
	}{
		{
			name:   "owner",
			mutate: map[string]any{"owner_id": "foreign-owner"},
		},
		{
			name:   "holder",
			mutate: map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, 6490)
			fixture.deleter.result = PointDeletionResult{
				Outcome:       PointDeletionDeleted,
				ReceiptDigest: strings.Repeat("b", 64),
			}
			attempt := fixture.attempt
			for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil {
					t.Fatalf("advance to provider_delete: %v", err)
				}
			}
			preparation, err := fixture.coordinator.prepareProviderDelete(context.Background(), attempt.ID)
			if err != nil {
				t.Fatalf("prepare provider delete: %v", err)
			}
			if !preparation.acquired {
				t.Fatal("prepare provider delete did not acquire effect claim")
			}
			execution, err := fixture.coordinator.executeProviderDelete(context.Background(), preparation)
			if err != nil {
				t.Fatalf("execute provider delete: %v", err)
			}
			beforeAttempt, err := fixture.coordinator.loadAttempt(context.Background(), attempt.ID)
			if err != nil {
				t.Fatalf("load attempt before receipt: %v", err)
			}
			var beforeClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.Where("attempt_id = ?", preparation.binding.AttemptID).First(&beforeClaim).Error; err != nil {
				t.Fatalf("load claim before receipt: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", preparation.binding.LeaseID).
				Updates(testCase.mutate).Error; err != nil {
				t.Fatalf("mutate lease authority: %v", err)
			}
			var mutatedLease model.RecoveryPointLease
			if err := fixture.db.Where("id = ?", preparation.binding.LeaseID).First(&mutatedLease).Error; err != nil {
				t.Fatalf("load mutated lease: %v", err)
			}
			if _, err := fixture.coordinator.persistProviderDeleteReceiptWithClaim(context.Background(), preparation, execution); err == nil ||
				!errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				t.Fatalf("persist receipt error=%v, want provider identity conflict", err)
			}
			afterAttempt, err := fixture.coordinator.loadAttempt(context.Background(), attempt.ID)
			if err != nil {
				t.Fatalf("load attempt after receipt: %v", err)
			}
			var afterClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.Where("attempt_id = ?", preparation.binding.AttemptID).First(&afterClaim).Error; err != nil {
				t.Fatalf("load claim after receipt: %v", err)
			}
			var afterLease model.RecoveryPointLease
			if err := fixture.db.Where("id = ?", preparation.binding.LeaseID).First(&afterLease).Error; err != nil {
				t.Fatalf("load lease after receipt: %v", err)
			}
			if !reflect.DeepEqual(beforeAttempt, afterAttempt) {
				t.Fatalf("attempt mutated after foreign lease receipt rejection:\nbefore=%+v\nafter=%+v", beforeAttempt, afterAttempt)
			}
			if !reflect.DeepEqual(beforeClaim, afterClaim) {
				t.Fatalf("claim mutated after foreign lease receipt rejection:\nbefore=%+v\nafter=%+v", beforeClaim, afterClaim)
			}
			if !reflect.DeepEqual(mutatedLease, afterLease) {
				t.Fatalf("lease mutated after foreign lease receipt rejection:\nbefore=%+v\nafter=%+v", mutatedLease, afterLease)
			}
			var tombstoneCount int64
			if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).Count(&tombstoneCount).Error; err != nil {
				t.Fatalf("count tombstones: %v", err)
			}
			if tombstoneCount != 0 {
				t.Fatalf("tombstones=%d after foreign lease receipt rejection, want 0", tombstoneCount)
			}
			if fixture.deleter.verifyCalls != 0 {
				t.Fatalf("verify calls=%d after foreign lease receipt rejection, want 0", fixture.deleter.verifyCalls)
			}
		})
	}
}
func TestLifecycleEffectClaimRenewPreservesDatabaseError(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 7280)
	attempt := fixture.attempt
	for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance renewal error fixture to provider_delete: %v", err)
		}
	}
	preparation, err := fixture.coordinator.prepareProviderDelete(context.Background(), attempt.ID)
	if err != nil || !preparation.acquired {
		t.Fatalf("prepare renewal error fixture preparation=%+v error=%v", preparation, err)
	}
	var beforeLease model.RecoveryPointLease
	if err := fixture.db.First(&beforeLease, "id = ?", preparation.binding.LeaseID).Error; err != nil {
		t.Fatalf("load lease before renewal error: %v", err)
	}
	var beforeClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&beforeClaim, "attempt_id = ?", preparation.binding.AttemptID).Error; err != nil {
		t.Fatalf("load claim before renewal error: %v", err)
	}

	updateErr := errors.New("renew effect claim update sentinel")
	var updateCalls int
	const callbackName = "test:renew-effect-claim-update-error"
	if err := fixture.db.Callback().Update().Before("gorm:update").Register(callbackName, func(query *gorm.DB) {
		if query.Statement == nil || query.Statement.Schema == nil ||
			query.Statement.Schema.Table != (model.RecoveryPointLifecycleEffectClaim{}).TableName() || updateCalls != 0 {
			return
		}
		updateCalls++
		_ = query.AddError(updateErr)
	}); err != nil {
		t.Fatalf("register renewal update error: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Update().Remove(callbackName); err != nil {
			t.Errorf("remove renewal update error: %v", err)
		}
	})

	err = fixture.coordinator.renewEffectClaim(context.Background(), &preparation.binding)
	if err == nil || !errors.Is(err, updateErr) || errors.Is(err, backupasset.ErrConflict) ||
		strings.Count(err.Error(), string(providerDeleteStageClaimRenew)) != 1 || updateCalls != 1 {
		t.Fatalf("renewal update error=%v update_calls=%d, want one claim_renew tag and preserved non-conflict sentinel", err, updateCalls)
	}
	var afterLease model.RecoveryPointLease
	if err := fixture.db.First(&afterLease, "id = ?", preparation.binding.LeaseID).Error; err != nil {
		t.Fatalf("load lease after renewal error: %v", err)
	}
	var afterClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&afterClaim, "attempt_id = ?", preparation.binding.AttemptID).Error; err != nil {
		t.Fatalf("load claim after renewal error: %v", err)
	}
	if !reflect.DeepEqual(beforeLease, afterLease) {
		t.Fatalf("lease mutated after renewal update rollback: before=%+v after=%+v", beforeLease, afterLease)
	}
	if !reflect.DeepEqual(beforeClaim, afterClaim) {
		t.Fatalf("claim mutated after renewal update rollback: before=%+v after=%+v", beforeClaim, afterClaim)
	}
}
func TestLifecycleEffectClaimRenewPreservesLeaseLockDatabaseError(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 7290)
	attempt := fixture.attempt
	for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance renewal lock fixture to provider_delete: %v", err)
		}
	}
	preparation, err := fixture.coordinator.prepareProviderDelete(context.Background(), attempt.ID)
	if err != nil || !preparation.acquired {
		t.Fatalf("prepare renewal lock fixture preparation=%+v error=%v", preparation, err)
	}
	var beforeLease model.RecoveryPointLease
	if err := fixture.db.First(&beforeLease, "id = ?", preparation.binding.LeaseID).Error; err != nil {
		t.Fatalf("load lease before renewal lock error: %v", err)
	}
	var beforeClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&beforeClaim, "attempt_id = ?", preparation.binding.AttemptID).Error; err != nil {
		t.Fatalf("load claim before renewal lock error: %v", err)
	}

	queryErr := errors.New("renew effect claim lease lock sentinel")
	var queryCalls int
	const callbackName = "test:renew-effect-claim-lease-lock-error"
	if err := fixture.db.Callback().Query().Before("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement == nil || query.Statement.Schema == nil ||
			query.Statement.Schema.Table != (model.RecoveryPointLease{}).TableName() || queryCalls != 0 {
			return
		}
		queryCalls++
		_ = query.AddError(queryErr)
	}); err != nil {
		t.Fatalf("register renewal lease lock error: %v", err)
	}
	t.Cleanup(func() {
		if err := fixture.db.Callback().Query().Remove(callbackName); err != nil {
			t.Errorf("remove renewal lease lock error: %v", err)
		}
	})

	err = fixture.coordinator.renewEffectClaim(context.Background(), &preparation.binding)
	if err == nil || !errors.Is(err, queryErr) || errors.Is(err, backupasset.ErrConflict) ||
		errors.Is(err, provider.ErrDeletePointIdentityConflict) ||
		strings.Count(err.Error(), string(providerDeleteStageClaimRenew)) != 1 || queryCalls != 1 {
		t.Fatalf("renewal lease lock error=%v query_calls=%d, want one claim_renew tag and preserved storage sentinel", err, queryCalls)
	}
	var afterLease model.RecoveryPointLease
	if err := fixture.db.First(&afterLease, "id = ?", preparation.binding.LeaseID).Error; err != nil {
		t.Fatalf("load lease after renewal lock error: %v", err)
	}
	var afterClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&afterClaim, "attempt_id = ?", preparation.binding.AttemptID).Error; err != nil {
		t.Fatalf("load claim after renewal lock error: %v", err)
	}
	if !reflect.DeepEqual(beforeLease, afterLease) {
		t.Fatalf("lease mutated after renewal lock error: before=%+v after=%+v", beforeLease, afterLease)
	}
	if !reflect.DeepEqual(beforeClaim, afterClaim) {
		t.Fatalf("claim mutated after renewal lock error: before=%+v after=%+v", beforeClaim, afterClaim)
	}
}

func TestLifecycleEffectClaimRenewRejectsForeignLeaseAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate map[string]any
	}{
		{
			name:   "owner",
			mutate: map[string]any{"owner_id": "foreign-owner"},
		},
		{
			name:   "holder",
			mutate: map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, 6490)
			attempt := fixture.attempt
			for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil {
					t.Fatalf("advance to provider_delete: %v", err)
				}
			}
			preparation, err := fixture.coordinator.prepareProviderDelete(context.Background(), attempt.ID)
			if err != nil {
				t.Fatalf("prepare provider delete: %v", err)
			}
			if !preparation.acquired {
				t.Fatal("prepare provider delete did not acquire effect claim")
			}
			var beforeClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.Where("attempt_id = ?", preparation.binding.AttemptID).First(&beforeClaim).Error; err != nil {
				t.Fatalf("load claim before renewal: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", preparation.binding.LeaseID).
				Updates(testCase.mutate).Error; err != nil {
				t.Fatalf("mutate lease authority: %v", err)
			}
			var mutatedLease model.RecoveryPointLease
			if err := fixture.db.Where("id = ?", preparation.binding.LeaseID).First(&mutatedLease).Error; err != nil {
				t.Fatalf("load mutated lease: %v", err)
			}
			if err := fixture.coordinator.renewEffectClaim(context.Background(), &preparation.binding); err == nil ||
				!errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				t.Fatalf("renew effect claim error=%v, want provider identity conflict", err)
			}
			var afterClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.Where("attempt_id = ?", preparation.binding.AttemptID).First(&afterClaim).Error; err != nil {
				t.Fatalf("load claim after renewal: %v", err)
			}
			var afterLease model.RecoveryPointLease
			if err := fixture.db.Where("id = ?", preparation.binding.LeaseID).First(&afterLease).Error; err != nil {
				t.Fatalf("load lease after renewal: %v", err)
			}
			if !reflect.DeepEqual(beforeClaim, afterClaim) {
				t.Fatalf("claim mutated after foreign lease renewal rejection:\nbefore=%+v\nafter=%+v", beforeClaim, afterClaim)
			}
			if !reflect.DeepEqual(mutatedLease, afterLease) {
				t.Fatalf("lease mutated after foreign lease renewal rejection:\nbefore=%+v\nafter=%+v", mutatedLease, afterLease)
			}
		})
	}
}

func TestLifecycleProviderProofProgressAcceptsForeignLeaseAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate map[string]any
	}{
		{
			name:   "owner",
			mutate: map[string]any{"owner_id": "foreign-owner"},
		},
		{
			name:   "holder",
			mutate: map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)},
		},
		{
			name:   "missing owner",
			mutate: map[string]any{"owner_id": ""},
		},
		{
			name:   "missing holder",
			mutate: map[string]any{"holder_type": ""},
		},
		{
			name:   "rebound attempt",
			mutate: map[string]any{"attempt_id": testOpaqueID(9898)},
		},
		{
			name:   "rebound fence",
			mutate: map[string]any{"fence_token": strings.Repeat("f", 64)},
		},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := seedProviderDeleteProofFirstFixture(t, 6810+uint64(index*10), "in_flight")
			now := fixture.clock.UTC()
			leaseExpiry := now.Add(10 * time.Minute)
			absoluteDeadline := now.Add(time.Hour)
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", fixture.attempt.LeaseID).
				Updates(map[string]any{
					"status": backupasset.LeaseActive, "holder_type": string(backupasset.LeaseHolderRetentionWorker),
					"owner_id": fixture.coordinator.leaseOwnerID, "lease_expires_at": leaseExpiry,
					"absolute_deadline": absoluteDeadline, "updated_at": now,
				}).Error; err != nil {
				t.Fatalf("restore active proof lease: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", fixture.attempt.LeaseID).Updates(testCase.mutate).Error; err != nil {
				t.Fatalf("mutate proven lease authority: %v", err)
			}
			var beforeClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.Where("attempt_id = ?", fixture.attempt.ID).First(&beforeClaim).Error; err != nil {
				t.Fatalf("load proof claim before progress: %v", err)
			}
			var beforeTombstone model.RecoveryPointLifecycleTombstone
			if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?",
				fixture.pointID, backupasset.LifecycleRetentionExpire).First(&beforeTombstone).Error; err != nil {
				t.Fatalf("load proof tombstone before progress: %v", err)
			}
			var beforeLease model.RecoveryPointLease
			if err := fixture.db.Where("id = ?", fixture.attempt.LeaseID).First(&beforeLease).Error; err != nil {
				t.Fatalf("load mutated proof lease: %v", err)
			}

			progressed, err := fixture.coordinator.progressProviderProof(context.Background(), fixture.attempt.ID)
			if err != nil || progressed.Phase != backupasset.LifecyclePhaseTombstoning {
				t.Fatalf("progress provider proof attempt=%+v error=%v, want tombstoning", progressed, err)
			}
			completed, err := fixture.coordinator.tombstoneAndCompleteProviderProof(context.Background(), fixture.attempt.ID)
			if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete {
				t.Fatalf("complete provider proof attempt=%+v error=%v, want complete", completed, err)
			}
			var afterClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.Where("attempt_id = ?", fixture.attempt.ID).First(&afterClaim).Error; err != nil {
				t.Fatalf("load proof claim after completion: %v", err)
			}
			var afterTombstone model.RecoveryPointLifecycleTombstone
			if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?",
				fixture.pointID, backupasset.LifecycleRetentionExpire).First(&afterTombstone).Error; err != nil {
				t.Fatalf("load proof tombstone after completion: %v", err)
			}
			var afterLease model.RecoveryPointLease
			if err := fixture.db.Where("id = ?", fixture.attempt.LeaseID).First(&afterLease).Error; err != nil {
				t.Fatalf("load proof lease after completion: %v", err)
			}
			var point model.RecoveryPoint
			if err := fixture.db.First(&point, "id = ?", fixture.pointID).Error; err != nil {
				t.Fatalf("load proof point after completion: %v", err)
			}
			if !reflect.DeepEqual(beforeClaim, afterClaim) ||
				!reflect.DeepEqual(beforeTombstone, afterTombstone) ||
				!reflect.DeepEqual(beforeLease, afterLease) ||
				point.State != string(backupasset.RecoveryPointExpired) ||
				point.PhysicalAvailability != string(backupasset.PhysicalMissing) {
				t.Fatalf("foreign proven authority changed durable proof/lease or failed completion claim=%t tombstone=%t lease=%t point=%+v",
					!reflect.DeepEqual(beforeClaim, afterClaim),
					!reflect.DeepEqual(beforeTombstone, afterTombstone),
					!reflect.DeepEqual(beforeLease, afterLease), point)
			}
			if fixture.deleter.calls != 0 {
				t.Fatalf("provider calls=%d after foreign proven lease progress, want 0", fixture.deleter.calls)
			}
		})
	}
}

func TestLifecycleProviderDeleteObserverRejectsForeignLeaseAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate map[string]any
	}{
		{
			name:   "owner",
			mutate: map[string]any{"owner_id": "foreign-owner"},
		},
		{
			name:   "holder",
			mutate: map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, 6890)
			attempt := fixture.attempt
			for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil {
					t.Fatalf("advance to provider_delete: %v", err)
				}
			}
			preparation, err := fixture.coordinator.prepareProviderDelete(context.Background(), attempt.ID)
			if err != nil {
				t.Fatalf("prepare provider delete: %v", err)
			}
			if !preparation.acquired {
				t.Fatal("prepare provider delete did not acquire effect claim")
			}
			if err := fixture.db.Model(&model.RecoveryPointLifecycleEffectClaim{}).
				Where("attempt_id = ?", attempt.ID).
				Update("state", "uncertain").Error; err != nil {
				t.Fatalf("seed uncertain observer claim: %v", err)
			}
			var beforeAttempt model.RecoveryPointLifecycleAttempt
			if err := fixture.db.Where("id = ?", attempt.ID).First(&beforeAttempt).Error; err != nil {
				t.Fatalf("load observer attempt before advance: %v", err)
			}
			var beforeClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.Where("attempt_id = ?", attempt.ID).First(&beforeClaim).Error; err != nil {
				t.Fatalf("load observer claim before advance: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", preparation.binding.LeaseID).
				Updates(testCase.mutate).Error; err != nil {
				t.Fatalf("mutate observer lease authority: %v", err)
			}
			var mutatedLease model.RecoveryPointLease
			if err := fixture.db.Where("id = ?", preparation.binding.LeaseID).First(&mutatedLease).Error; err != nil {
				t.Fatalf("load mutated observer lease: %v", err)
			}
			prepareCalls := fixture.deleter.prepareCalls
			if _, err := fixture.coordinator.Advance(context.Background(), attempt.ID); err == nil ||
				!errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				t.Fatalf("foreign observer advance error=%v, want provider identity conflict", err)
			}
			var afterAttempt model.RecoveryPointLifecycleAttempt
			if err := fixture.db.Where("id = ?", attempt.ID).First(&afterAttempt).Error; err != nil {
				t.Fatalf("load observer attempt after advance: %v", err)
			}
			var afterClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.Where("attempt_id = ?", attempt.ID).First(&afterClaim).Error; err != nil {
				t.Fatalf("load observer claim after advance: %v", err)
			}
			var afterLease model.RecoveryPointLease
			if err := fixture.db.Where("id = ?", preparation.binding.LeaseID).First(&afterLease).Error; err != nil {
				t.Fatalf("load observer lease after advance: %v", err)
			}
			if !reflect.DeepEqual(beforeAttempt, afterAttempt) ||
				!reflect.DeepEqual(beforeClaim, afterClaim) ||
				!reflect.DeepEqual(mutatedLease, afterLease) {
				t.Fatalf("foreign observer advance mutated durable state attempt=%t claim=%t lease=%t",
					!reflect.DeepEqual(beforeAttempt, afterAttempt),
					!reflect.DeepEqual(beforeClaim, afterClaim),
					!reflect.DeepEqual(mutatedLease, afterLease))
			}
			if fixture.deleter.prepareCalls != prepareCalls || fixture.deleter.calls != 0 {
				t.Fatalf("foreign observer advance provider calls prepare=%d/%d execute=%d, want %d/0",
					fixture.deleter.prepareCalls, prepareCalls, fixture.deleter.calls, prepareCalls)
			}
		})
	}
}

func TestLifecycleProviderDeleteHeartbeatRejectsForeignLeaseAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate map[string]any
	}{
		{
			name:   "owner",
			mutate: map[string]any{"owner_id": "foreign-owner"},
		},
		{
			name:   "holder",
			mutate: map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, 6990)
			attempt := fixture.attempt
			for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil {
					t.Fatalf("advance to provider_delete: %v", err)
				}
			}
			preparation, err := fixture.coordinator.prepareProviderDelete(context.Background(), attempt.ID)
			if err != nil {
				t.Fatalf("prepare provider delete: %v", err)
			}
			if !preparation.acquired {
				t.Fatal("prepare provider delete did not acquire effect claim")
			}
			var beforeAttempt model.RecoveryPointLifecycleAttempt
			if err := fixture.db.Where("id = ?", attempt.ID).First(&beforeAttempt).Error; err != nil {
				t.Fatalf("load heartbeat attempt before heartbeat: %v", err)
			}
			var beforeClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.Where("attempt_id = ?", attempt.ID).First(&beforeClaim).Error; err != nil {
				t.Fatalf("load heartbeat claim before heartbeat: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", preparation.binding.LeaseID).
				Updates(testCase.mutate).Error; err != nil {
				t.Fatalf("mutate heartbeat lease authority: %v", err)
			}
			var mutatedLease model.RecoveryPointLease
			if err := fixture.db.Where("id = ?", preparation.binding.LeaseID).First(&mutatedLease).Error; err != nil {
				t.Fatalf("load mutated heartbeat lease: %v", err)
			}
			if _, err := fixture.coordinator.heartbeatProviderDelete(context.Background(), attempt.ID); err == nil ||
				!errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				t.Fatalf("foreign heartbeat error=%v, want provider identity conflict", err)
			}
			var afterAttempt model.RecoveryPointLifecycleAttempt
			if err := fixture.db.Where("id = ?", attempt.ID).First(&afterAttempt).Error; err != nil {
				t.Fatalf("load heartbeat attempt after heartbeat: %v", err)
			}
			var afterClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.Where("attempt_id = ?", attempt.ID).First(&afterClaim).Error; err != nil {
				t.Fatalf("load heartbeat claim after heartbeat: %v", err)
			}
			var afterLease model.RecoveryPointLease
			if err := fixture.db.Where("id = ?", preparation.binding.LeaseID).First(&afterLease).Error; err != nil {
				t.Fatalf("load heartbeat lease after heartbeat: %v", err)
			}
			if !reflect.DeepEqual(beforeAttempt, afterAttempt) ||
				!reflect.DeepEqual(beforeClaim, afterClaim) ||
				!reflect.DeepEqual(mutatedLease, afterLease) {
				t.Fatalf("foreign heartbeat mutated durable state attempt=%t claim=%t lease=%t",
					!reflect.DeepEqual(beforeAttempt, afterAttempt),
					!reflect.DeepEqual(beforeClaim, afterClaim),
					!reflect.DeepEqual(mutatedLease, afterLease))
			}
		})
	}
}

func TestLifecycleProviderDeleteNoClaimAdvanceRejectsForeignLeaseAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate map[string]any
	}{
		{name: "owner", mutate: map[string]any{"owner_id": "foreign-owner"}},
		{name: "holder", mutate: map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)}},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, 7100+uint64(index*10))
			attempt := fixture.attempt
			for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil {
					t.Fatalf("advance to provider_delete: %v", err)
				}
			}
			fixture.attempt = attempt
			var beforeAttempt model.RecoveryPointLifecycleAttempt
			if err := fixture.db.First(&beforeAttempt, "id = ?", attempt.ID).Error; err != nil {
				t.Fatalf("load no-claim advance attempt: %v", err)
			}
			var beforePoint model.RecoveryPoint
			if err := fixture.db.First(&beforePoint, "id = ?", fixture.pointID).Error; err != nil {
				t.Fatalf("load no-claim advance point: %v", err)
			}
			var beforeLease model.RecoveryPointLease
			if err := fixture.db.First(&beforeLease, "id = ?", attempt.LeaseID).Error; err != nil {
				t.Fatalf("load no-claim advance lease: %v", err)
			}
			var beforeClaims, beforeTombstones int64
			if err := fixture.db.Model(&model.RecoveryPointLifecycleEffectClaim{}).
				Where("attempt_id = ?", attempt.ID).Count(&beforeClaims).Error; err != nil {
				t.Fatalf("count no-claim advance claims: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
				Where("recovery_point_id = ?", fixture.pointID).Count(&beforeTombstones).Error; err != nil {
				t.Fatalf("count no-claim advance tombstones: %v", err)
			}
			if beforeClaims != 0 || beforeTombstones != 0 {
				t.Fatalf("no-claim advance fixture claims=%d tombstones=%d, want zero", beforeClaims, beforeTombstones)
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", beforeLease.ID).Updates(testCase.mutate).Error; err != nil {
				t.Fatalf("mutate no-claim advance lease authority: %v", err)
			}
			var mutatedLease model.RecoveryPointLease
			if err := fixture.db.First(&mutatedLease, "id = ?", beforeLease.ID).Error; err != nil {
				t.Fatalf("load mutated no-claim advance lease: %v", err)
			}
			got, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err == nil || !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				t.Fatalf("no-claim advance attempt=%+v error=%v, want provider identity conflict", got, err)
			}
			var afterAttempt model.RecoveryPointLifecycleAttempt
			if err := fixture.db.First(&afterAttempt, "id = ?", attempt.ID).Error; err != nil {
				t.Fatalf("load no-claim advance attempt after rejection: %v", err)
			}
			var afterPoint model.RecoveryPoint
			if err := fixture.db.First(&afterPoint, "id = ?", fixture.pointID).Error; err != nil {
				t.Fatalf("load no-claim advance point after rejection: %v", err)
			}
			var afterLease model.RecoveryPointLease
			if err := fixture.db.First(&afterLease, "id = ?", beforeLease.ID).Error; err != nil {
				t.Fatalf("load no-claim advance lease after rejection: %v", err)
			}
			var afterClaims, afterTombstones int64
			if err := fixture.db.Model(&model.RecoveryPointLifecycleEffectClaim{}).
				Where("attempt_id = ?", attempt.ID).Count(&afterClaims).Error; err != nil {
				t.Fatalf("count no-claim advance claims after rejection: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
				Where("recovery_point_id = ?", fixture.pointID).Count(&afterTombstones).Error; err != nil {
				t.Fatalf("count no-claim advance tombstones after rejection: %v", err)
			}
			if !reflect.DeepEqual(beforeAttempt, afterAttempt) ||
				!reflect.DeepEqual(beforePoint, afterPoint) ||
				!reflect.DeepEqual(mutatedLease, afterLease) ||
				afterClaims != beforeClaims || afterTombstones != beforeTombstones {
				t.Fatalf("no-claim advance mutated durable state attempt=%t point=%t lease=%t claims=%d/%d tombstones=%d/%d",
					!reflect.DeepEqual(beforeAttempt, afterAttempt),
					!reflect.DeepEqual(beforePoint, afterPoint),
					!reflect.DeepEqual(mutatedLease, afterLease),
					afterClaims, beforeClaims, afterTombstones, beforeTombstones)
			}
			if fixture.deleter.prepareCalls != 0 || fixture.deleter.calls != 0 {
				t.Fatalf("no-claim advance provider calls prepare=%d execute=%d, want zero",
					fixture.deleter.prepareCalls, fixture.deleter.calls)
			}
		})
	}
}

func TestLifecycleProviderDeleteNoClaimHeartbeatRejectsForeignLeaseAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate map[string]any
	}{
		{name: "owner", mutate: map[string]any{"owner_id": "foreign-owner"}},
		{name: "holder", mutate: map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)}},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, 7120+uint64(index*10))
			attempt := fixture.attempt
			for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil {
					t.Fatalf("advance to provider_delete: %v", err)
				}
			}
			fixture.attempt = attempt
			var beforeAttempt model.RecoveryPointLifecycleAttempt
			if err := fixture.db.First(&beforeAttempt, "id = ?", attempt.ID).Error; err != nil {
				t.Fatalf("load no-claim heartbeat attempt: %v", err)
			}
			var beforePoint model.RecoveryPoint
			if err := fixture.db.First(&beforePoint, "id = ?", fixture.pointID).Error; err != nil {
				t.Fatalf("load no-claim heartbeat point: %v", err)
			}
			var beforeLease model.RecoveryPointLease
			if err := fixture.db.First(&beforeLease, "id = ?", attempt.LeaseID).Error; err != nil {
				t.Fatalf("load no-claim heartbeat lease: %v", err)
			}
			var beforeClaims, beforeTombstones int64
			if err := fixture.db.Model(&model.RecoveryPointLifecycleEffectClaim{}).
				Where("attempt_id = ?", attempt.ID).Count(&beforeClaims).Error; err != nil {
				t.Fatalf("count no-claim heartbeat claims: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
				Where("recovery_point_id = ?", fixture.pointID).Count(&beforeTombstones).Error; err != nil {
				t.Fatalf("count no-claim heartbeat tombstones: %v", err)
			}
			if beforeClaims != 0 || beforeTombstones != 0 {
				t.Fatalf("no-claim heartbeat fixture claims=%d tombstones=%d, want zero", beforeClaims, beforeTombstones)
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", beforeLease.ID).Updates(testCase.mutate).Error; err != nil {
				t.Fatalf("mutate no-claim heartbeat lease authority: %v", err)
			}
			var mutatedLease model.RecoveryPointLease
			if err := fixture.db.First(&mutatedLease, "id = ?", beforeLease.ID).Error; err != nil {
				t.Fatalf("load mutated no-claim heartbeat lease: %v", err)
			}
			got, err := fixture.coordinator.Heartbeat(context.Background(), attempt.ID)
			if err == nil || !errors.Is(err, provider.ErrDeletePointIdentityConflict) {
				t.Fatalf("no-claim heartbeat attempt=%+v error=%v, want provider identity conflict", got, err)
			}
			var afterAttempt model.RecoveryPointLifecycleAttempt
			if err := fixture.db.First(&afterAttempt, "id = ?", attempt.ID).Error; err != nil {
				t.Fatalf("load no-claim heartbeat attempt after rejection: %v", err)
			}
			var afterPoint model.RecoveryPoint
			if err := fixture.db.First(&afterPoint, "id = ?", fixture.pointID).Error; err != nil {
				t.Fatalf("load no-claim heartbeat point after rejection: %v", err)
			}
			var afterLease model.RecoveryPointLease
			if err := fixture.db.First(&afterLease, "id = ?", beforeLease.ID).Error; err != nil {
				t.Fatalf("load no-claim heartbeat lease after rejection: %v", err)
			}
			var afterClaims, afterTombstones int64
			if err := fixture.db.Model(&model.RecoveryPointLifecycleEffectClaim{}).
				Where("attempt_id = ?", attempt.ID).Count(&afterClaims).Error; err != nil {
				t.Fatalf("count no-claim heartbeat claims after rejection: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
				Where("recovery_point_id = ?", fixture.pointID).Count(&afterTombstones).Error; err != nil {
				t.Fatalf("count no-claim heartbeat tombstones after rejection: %v", err)
			}
			if !reflect.DeepEqual(beforeAttempt, afterAttempt) ||
				!reflect.DeepEqual(beforePoint, afterPoint) ||
				!reflect.DeepEqual(mutatedLease, afterLease) ||
				afterClaims != beforeClaims || afterTombstones != beforeTombstones {
				t.Fatalf("no-claim heartbeat mutated durable state attempt=%t point=%t lease=%t claims=%d/%d tombstones=%d/%d",
					!reflect.DeepEqual(beforeAttempt, afterAttempt),
					!reflect.DeepEqual(beforePoint, afterPoint),
					!reflect.DeepEqual(mutatedLease, afterLease),
					afterClaims, beforeClaims, afterTombstones, beforeTombstones)
			}
			if fixture.deleter.prepareCalls != 0 || fixture.deleter.calls != 0 {
				t.Fatalf("no-claim heartbeat provider calls prepare=%d execute=%d, want zero",
					fixture.deleter.prepareCalls, fixture.deleter.calls)
			}
		})
	}
}

func TestLifecycleProviderProofProgressAcceptsForeignLeaseAuthorityAfterSettledStatus(t *testing.T) {
	tests := []struct {
		name   string
		status backupasset.LeaseStatus
		mutate map[string]any
	}{
		{
			name:   "released owner",
			status: backupasset.LeaseReleased,
			mutate: map[string]any{"owner_id": "foreign-owner"},
		},
		{
			name:   "released holder",
			status: backupasset.LeaseReleased,
			mutate: map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)},
		},
		{
			name:   "expired owner",
			status: backupasset.LeaseExpired,
			mutate: map[string]any{"owner_id": "foreign-owner"},
		},
		{
			name:   "expired holder",
			status: backupasset.LeaseExpired,
			mutate: map[string]any{"holder_type": string(backupasset.LeaseHolderContentSession)},
		},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := seedProviderDeleteProofFirstFixture(t, 7140+uint64(index*10), "in_flight")
			now := fixture.clock.UTC()
			leaseUpdates := map[string]any{"status": testCase.status, "updated_at": now}
			if testCase.status == backupasset.LeaseReleased {
				leaseUpdates["released_at"] = now
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", fixture.attempt.LeaseID).Updates(leaseUpdates).Error; err != nil {
				t.Fatalf("set settled proof lease status: %v", err)
			}
			if err := fixture.db.Model(&model.RecoveryPointLease{}).
				Where("id = ?", fixture.attempt.LeaseID).Updates(testCase.mutate).Error; err != nil {
				t.Fatalf("mutate settled proof lease authority: %v", err)
			}
			var beforeClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.First(&beforeClaim, "attempt_id = ?", fixture.attempt.ID).Error; err != nil {
				t.Fatalf("load settled proof claim: %v", err)
			}
			var beforeTombstone model.RecoveryPointLifecycleTombstone
			if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?",
				fixture.pointID, backupasset.LifecycleRetentionExpire).First(&beforeTombstone).Error; err != nil {
				t.Fatalf("load settled proof tombstone: %v", err)
			}
			var beforeLease model.RecoveryPointLease
			if err := fixture.db.First(&beforeLease, "id = ?", fixture.attempt.LeaseID).Error; err != nil {
				t.Fatalf("load settled proof lease: %v", err)
			}

			progressed, err := fixture.coordinator.progressProviderProof(context.Background(), fixture.attempt.ID)
			if err != nil || progressed.Phase != backupasset.LifecyclePhaseTombstoning {
				t.Fatalf("settled proof progress attempt=%+v error=%v, want tombstoning", progressed, err)
			}
			completed, err := fixture.coordinator.tombstoneAndCompleteProviderProof(context.Background(), fixture.attempt.ID)
			if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete {
				t.Fatalf("settled proof completion attempt=%+v error=%v, want complete", completed, err)
			}
			var afterClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.First(&afterClaim, "attempt_id = ?", fixture.attempt.ID).Error; err != nil {
				t.Fatalf("load settled proof claim after completion: %v", err)
			}
			var afterTombstone model.RecoveryPointLifecycleTombstone
			if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?",
				fixture.pointID, backupasset.LifecycleRetentionExpire).First(&afterTombstone).Error; err != nil {
				t.Fatalf("load settled proof tombstone after completion: %v", err)
			}
			var afterLease model.RecoveryPointLease
			if err := fixture.db.First(&afterLease, "id = ?", beforeLease.ID).Error; err != nil {
				t.Fatalf("load settled proof lease after completion: %v", err)
			}
			var point model.RecoveryPoint
			if err := fixture.db.First(&point, "id = ?", fixture.pointID).Error; err != nil {
				t.Fatalf("load settled proof point after completion: %v", err)
			}
			if !reflect.DeepEqual(beforeClaim, afterClaim) ||
				!reflect.DeepEqual(beforeTombstone, afterTombstone) ||
				!reflect.DeepEqual(beforeLease, afterLease) ||
				point.State != string(backupasset.RecoveryPointExpired) ||
				point.PhysicalAvailability != string(backupasset.PhysicalMissing) {
				t.Fatalf("settled proof changed durable proof/lease or failed completion claim=%t tombstone=%t lease=%t point=%+v",
					!reflect.DeepEqual(beforeClaim, afterClaim),
					!reflect.DeepEqual(beforeTombstone, afterTombstone),
					!reflect.DeepEqual(beforeLease, afterLease), point)
			}
			if fixture.deleter.prepareCalls != 0 || fixture.deleter.calls != 0 || fixture.deleter.verifyCalls != 0 {
				t.Fatalf("settled proof provider calls prepare=%d execute=%d verify=%d, want zero",
					fixture.deleter.prepareCalls, fixture.deleter.calls, fixture.deleter.verifyCalls)
			}
		})
	}
}

func TestLifecycleProviderDeleteTakeoverAfterSharedReconcileExpired(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 7180)
	attempt := fixture.attempt
	for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance to provider_delete: %v", err)
		}
	}
	fixture.attempt = attempt
	preparation, err := fixture.coordinator.prepareProviderDelete(context.Background(), attempt.ID)
	if err != nil || !preparation.acquired {
		t.Fatalf("seed durable non-proven claim preparation=%+v error=%v", preparation, err)
	}
	if fixture.deleter.prepareCalls != 1 || fixture.deleter.calls != 0 {
		t.Fatalf("seed durable non-proven claim provider calls prepare=%d execute=%d, want 1/0",
			fixture.deleter.prepareCalls, fixture.deleter.calls)
	}
	var oldAttempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&oldAttempt, "id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load old takeover attempt: %v", err)
	}
	var oldClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&oldClaim, "attempt_id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load old takeover claim: %v", err)
	}
	if oldClaim.State != "in_flight" {
		t.Fatalf("seed claim state=%q, want in_flight", oldClaim.State)
	}
	var oldLease model.RecoveryPointLease
	if err := fixture.db.First(&oldLease, "id = ?", oldAttempt.LeaseID).Error; err != nil {
		t.Fatalf("load old takeover lease: %v", err)
	}
	var beforeTombstoneCount int64
	if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
		Where("recovery_point_id = ?", fixture.pointID).Count(&beforeTombstoneCount).Error; err != nil {
		t.Fatalf("check no pre-existing takeover proof: %v", err)
	}
	if beforeTombstoneCount != 0 {
		t.Fatalf("pre-existing takeover tombstones=%d, want zero", beforeTombstoneCount)
	}
	fixture.clock = oldLease.AbsoluteDeadline.UTC().Add(time.Second)
	reconciled, err := fixture.coordinator.leases.ReconcileExpired(context.Background())
	if err != nil || reconciled != 1 {
		t.Fatalf("shared ReconcileExpired count=%d error=%v, want one", reconciled, err)
	}
	var expiredLease model.RecoveryPointLease
	if err := fixture.db.First(&expiredLease, "id = ?", oldLease.ID).Error; err != nil {
		t.Fatalf("load reconciled expired lease: %v", err)
	}
	if backupasset.LeaseStatus(expiredLease.Status) != backupasset.LeaseExpired {
		t.Fatalf("reconciled lease status=%q, want expired", expiredLease.Status)
	}
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("d", 64),
	}
	takenOver, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil {
		t.Fatalf("advance expired in-flight claim takeover: %v", err)
	}
	if takenOver.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("expired in-flight claim takeover phase=%q, want tombstoning", takenOver.Phase)
	}
	if fixture.deleter.prepareCalls != 3 || fixture.deleter.calls != 1 {
		t.Fatalf("expired in-flight claim takeover provider calls prepare=%d execute=%d, want observer/execution prepares (3 total) and one effect",
			fixture.deleter.prepareCalls, fixture.deleter.calls)
	}
	if takenOver.LeaseID == oldLease.ID || takenOver.LeaseAttemptID == oldLease.AttemptID ||
		takenOver.LeaseFenceTokenHash == hashFenceToken(oldLease.FenceToken) {
		t.Fatalf("expired in-flight claim takeover did not rotate lease authority: attempt=%+v old_lease=%+v",
			takenOver, oldLease)
	}
	var newLease model.RecoveryPointLease
	if err := fixture.db.First(&newLease, "id = ?", takenOver.LeaseID).Error; err != nil {
		t.Fatalf("load fresh takeover lease: %v", err)
	}
	if newLease.ID == oldLease.ID ||
		newLease.RecoveryPointID != oldLease.RecoveryPointID ||
		newLease.HolderType != string(backupasset.LeaseHolderRetentionWorker) ||
		newLease.OwnerID != fixture.coordinator.leaseOwnerID {
		t.Fatalf("fresh takeover lease=%+v, want configured owner/holder and new ID", newLease)
	}
	var oldLeaseAfter model.RecoveryPointLease
	if err := fixture.db.First(&oldLeaseAfter, "id = ?", oldLease.ID).Error; err != nil {
		t.Fatalf("reload historical expired lease: %v", err)
	}
	if !reflect.DeepEqual(expiredLease, oldLeaseAfter) {
		t.Fatalf("historical expired lease mutated after takeover:\nbefore=%+v\nafter=%+v", expiredLease, oldLeaseAfter)
	}
	var claimAfter model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&claimAfter, "attempt_id = ?", attempt.ID).Error; err != nil {
		t.Fatalf("load takeover claim after proof: %v", err)
	}
	if claimAfter.State != "proven" ||
		claimAfter.LeaseID != newLease.ID ||
		claimAfter.LeaseAttemptID != newLease.AttemptID ||
		claimAfter.LeaseFenceTokenHash != hashFenceToken(newLease.FenceToken) ||
		claimAfter.ExecutionID == oldClaim.ExecutionID {
		t.Fatalf("takeover claim=%+v, want proven fresh lease and execution", claimAfter)
	}
	var tombstoneCount int64
	if err := fixture.db.Model(&model.RecoveryPointLifecycleTombstone{}).
		Where("recovery_point_id = ?", fixture.pointID).Count(&tombstoneCount).Error; err != nil {
		t.Fatalf("count takeover tombstones: %v", err)
	}
	if tombstoneCount != 1 {
		t.Fatalf("takeover tombstones=%d, want one", tombstoneCount)
	}
	completed, err := fixture.coordinator.Advance(context.Background(), takenOver.ID)
	if err != nil || completed.Phase != backupasset.LifecyclePhaseComplete {
		t.Fatalf("complete expired in-flight claim takeover attempt=%+v error=%v, want complete", completed, err)
	}
	if fixture.deleter.calls != 1 {
		t.Fatalf("completed takeover execute calls=%d, want no duplicate effect", fixture.deleter.calls)
	}
}

func TestLifecycleUncertainProviderDeleteRetryAtIsDueGated(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 6030)
	fixture.deleter.result = PointDeletionResult{
		Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("b", 64),
	}
	attempt := fixture.attempt
	for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		var err error
		attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
		if err != nil {
			t.Fatalf("advance to provider delete: %v", err)
		}
	}
	fixture.deleter.err = errors.New("provider execution uncertain")
	failed, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err == nil || failed.Phase != backupasset.LifecyclePhaseProviderDelete ||
		failed.RetryAt == nil {
		t.Fatalf("uncertain provider attempt=%+v error=%v, want provider delete retry", failed, err)
	}
	var beforeAttempt model.RecoveryPointLifecycleAttempt
	if err := fixture.db.First(&beforeAttempt, "id = ?", failed.ID).Error; err != nil {
		t.Fatalf("load uncertain attempt: %v", err)
	}
	var beforeClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&beforeClaim, "attempt_id = ?", failed.ID).Error; err != nil {
		t.Fatalf("load uncertain claim: %v", err)
	}
	var beforeLease model.RecoveryPointLease
	if err := fixture.db.First(&beforeLease, "id = ?", failed.LeaseID).Error; err != nil {
		t.Fatalf("load uncertain lease: %v", err)
	}
	prepareCalls := fixture.deleter.prepareCalls
	providerCalls := fixture.deleter.calls
	retryAt := failed.RetryAt.UTC()
	fixture.clock = retryAt.Add(-time.Nanosecond)
	pending, err := fixture.coordinator.Advance(context.Background(), failed.ID)
	if err != nil || pending.Phase != beforeAttemptPhase(beforeAttempt) ||
		pending.TransitionRevision != beforeAttempt.TransitionRevision ||
		!sameLifecycleTimePtr(pending.RetryAt, beforeAttempt.RetryAt) ||
		fixture.deleter.prepareCalls != prepareCalls || fixture.deleter.calls != providerCalls {
		t.Fatalf("not-due uncertain retry attempt=%+v error=%v prepare/calls=%d/%d want unchanged rev=%d retry=%s prepare/calls=%d/%d",
			pending, err, fixture.deleter.prepareCalls, fixture.deleter.calls,
			beforeAttempt.TransitionRevision, retryAt, prepareCalls, providerCalls)
	}
	var afterClaim model.RecoveryPointLifecycleEffectClaim
	if err := fixture.db.First(&afterClaim, "attempt_id = ?", failed.ID).Error; err != nil {
		t.Fatalf("reload not-due claim: %v", err)
	}
	var afterLease model.RecoveryPointLease
	if err := fixture.db.First(&afterLease, "id = ?", failed.LeaseID).Error; err != nil {
		t.Fatalf("reload not-due lease: %v", err)
	}
	if afterClaim.State != beforeClaim.State || afterClaim.ExecutionID != beforeClaim.ExecutionID ||
		!afterClaim.HeartbeatAt.Equal(beforeClaim.HeartbeatAt) || !afterClaim.UpdatedAt.Equal(beforeClaim.UpdatedAt) ||
		afterLease.Status != beforeLease.Status || !afterLease.LeaseExpiresAt.Equal(beforeLease.LeaseExpiresAt) ||
		!afterLease.LastHeartbeatAt.Equal(beforeLease.LastHeartbeatAt) || !afterLease.UpdatedAt.Equal(beforeLease.UpdatedAt) {
		t.Fatalf("not-due uncertain retry mutated claim/lease before=%+v/%+v after=%+v/%+v",
			beforeClaim, beforeLease, afterClaim, afterLease)
	}
	fixture.clock = retryAt
	fixture.deleter.err = nil
	retried, err := fixture.coordinator.Advance(context.Background(), failed.ID)
	if err != nil || retried.Phase != backupasset.LifecyclePhaseTombstoning ||
		fixture.deleter.calls != providerCalls+1 || retried.RetryAt != nil {
		t.Fatalf("due uncertain retry attempt=%+v error=%v provider_calls=%d retry_at=%v, want tombstoning/one retry/cleared",
			retried, err, fixture.deleter.calls, retried.RetryAt)
	}
}

func beforeAttemptPhase(attempt model.RecoveryPointLifecycleAttempt) backupasset.LifecyclePhase {
	return backupasset.LifecyclePhase(attempt.Phase)
}

func TestLifecycleClaimedBlockedProviderDeleteRetryAtIsDueGated(t *testing.T) {
	tests := []struct {
		name        string
		reason      backupasset.LifecycleBlockedReason
		prepareErr  error
		hold        bool
		auditStatus string
		wantPrepare int
	}{
		{
			name: "active_hold", reason: backupasset.LifecycleBlockedActiveHold,
			hold: true, auditStatus: "blocked",
		},
		{
			name: "identity_conflict", reason: backupasset.LifecycleBlockedProviderIdentityConflict,
			prepareErr: provider.ErrDeletePointIdentityConflict, auditStatus: "identity_conflict", wantPrepare: 1,
		},
		{
			name: "native_reference", reason: backupasset.LifecycleBlockedProviderNativeVersionReferenced,
			prepareErr: provider.ErrDeletePointNativeVersionReferenced, auditStatus: "blocked", wantPrepare: 1,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := uint64(6100 + index*20)
			fixture := newClaimedExpiryFixture(t, base)
			fixture.deleter.result = PointDeletionResult{
				Outcome: PointDeletionDeleted, ReceiptDigest: strings.Repeat("c", 64),
			}
			attempt := fixture.attempt
			for attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
				var err error
				attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
				if err != nil {
					t.Fatalf("advance to provider delete: %v", err)
				}
			}
			fixture.deleter.err = errors.New("provider execution uncertain")
			failed, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err == nil || failed.RetryAt == nil {
				t.Fatalf("create uncertain claim attempt=%+v error=%v", failed, err)
			}
			if test.hold {
				holdID := testOpaqueID(base + 9)
				if err := fixture.db.Create(&model.RecoveryPointHold{
					ID: holdID, RecoveryPointID: fixture.pointID,
					HoldType: string(backupasset.RecoveryPointHoldLegal), State: string(backupasset.HoldActive),
					EncryptedReason: "FAKE_CLAIMED_ACTIVE_HOLD_FOR_TEST_ONLY", CreatedBy: 1,
					CreatedAt: fixture.clock, UpdatedAt: fixture.clock,
				}).Error; err != nil {
					t.Fatalf("seed active hold: %v", err)
				}
				result := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).
					Updates(map[string]any{"hold_state": backupasset.HoldActive, "updated_at": fixture.clock})
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("project active hold rows=%d error=%v", result.RowsAffected, result.Error)
				}
			}
			var blocked LifecycleAttempt
			if err := fixture.db.Transaction(func(tx *gorm.DB) error {
				rows, lockErr := lockProviderDeleteRowsByAttemptTx(context.Background(), tx, failed.ID)
				if lockErr != nil {
					return lockErr
				}
				var blockErr error
				blocked, blockErr = fixture.coordinator.blockAttemptTx(
					context.Background(), tx, &rows.attempt, &rows.point, test.reason,
				)
				return blockErr
			}); err != nil {
				t.Fatalf("create claimed blocked state: %v", err)
			}
			fixture.deleter.prepareErr = test.prepareErr
			audit := &recordingSettledAudit{}
			fixture.coordinator.audit = audit
			slotID := testOpaqueID(base + 8)
			if err := fixture.db.Create(&model.RecoveryPointLifecycleAuditSlot{
				ID: slotID, AttemptID: blocked.ID, Status: test.auditStatus,
				EmittedAt: fixture.clock, CreatedAt: fixture.clock,
			}).Error; err != nil {
				t.Fatalf("seed settled audit slot: %v", err)
			}
			var beforeAttempt model.RecoveryPointLifecycleAttempt
			if err := fixture.db.First(&beforeAttempt, "id = ?", blocked.ID).Error; err != nil {
				t.Fatalf("load claimed blocked attempt: %v", err)
			}
			var beforeClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.First(&beforeClaim, "attempt_id = ?", blocked.ID).Error; err != nil {
				t.Fatalf("load claimed blocked claim: %v", err)
			}
			prepareCalls := fixture.deleter.prepareCalls
			providerCalls := fixture.deleter.calls
			retryAt := blocked.RetryAt.UTC()
			fixture.clock = retryAt.Add(-time.Nanosecond)
			pending, err := fixture.coordinator.Advance(context.Background(), blocked.ID)
			if err != nil || pending.Phase != backupasset.LifecyclePhaseBlocked ||
				pending.BlockedReason != test.reason ||
				pending.TransitionRevision != beforeAttempt.TransitionRevision ||
				!sameLifecycleTimePtr(pending.RetryAt, beforeAttempt.RetryAt) ||
				fixture.deleter.prepareCalls != prepareCalls || fixture.deleter.calls != providerCalls ||
				len(audit.events) != 0 {
				t.Fatalf("not-due claimed blocked attempt=%+v error=%v prepare/calls=%d/%d audit=%d, want unchanged rev=%d retry=%s prepare/calls=%d/%d",
					pending, err, fixture.deleter.prepareCalls, fixture.deleter.calls, len(audit.events),
					beforeAttempt.TransitionRevision, retryAt, prepareCalls, providerCalls)
			}
			var afterClaim model.RecoveryPointLifecycleEffectClaim
			if err := fixture.db.First(&afterClaim, "attempt_id = ?", blocked.ID).Error; err != nil {
				t.Fatalf("reload not-due claimed blocked claim: %v", err)
			}
			if afterClaim.State != beforeClaim.State || afterClaim.ExecutionID != beforeClaim.ExecutionID ||
				!afterClaim.HeartbeatAt.Equal(beforeClaim.HeartbeatAt) || !afterClaim.UpdatedAt.Equal(beforeClaim.UpdatedAt) {
				t.Fatalf("not-due claimed blocked claim changed before=%+v after=%+v", beforeClaim, afterClaim)
			}
			fixture.clock = retryAt
			due, err := fixture.coordinator.Advance(context.Background(), blocked.ID)
			if err != nil || due.Phase != backupasset.LifecyclePhaseBlocked ||
				due.BlockedReason != test.reason || due.RetryAt == nil ||
				!due.RetryAt.After(fixture.clock) ||
				due.TransitionRevision != beforeAttempt.TransitionRevision+1 ||
				fixture.deleter.prepareCalls != prepareCalls+test.wantPrepare ||
				fixture.deleter.calls != providerCalls {
				var point model.RecoveryPoint
				_ = fixture.db.First(&point, "id = ?", fixture.pointID).Error
				var activeHolds int64
				_ = fixture.db.Model(&model.RecoveryPointHold{}).
					Where("recovery_point_id = ? AND state = ?", fixture.pointID, backupasset.HoldActive).
					Count(&activeHolds).Error
				t.Fatalf("due claimed blocked attempt=%+v error=%v prepare/calls=%d/%d active_hold_state=%q active_holds=%d, want blocked/retry rev=%d prepare/calls=%d/%d",
					due, err, fixture.deleter.prepareCalls, fixture.deleter.calls, point.HoldState, activeHolds,
					beforeAttempt.TransitionRevision+1, prepareCalls+test.wantPrepare, providerCalls)
			}
			nextBefore := due
			fixture.clock = due.RetryAt.UTC().Add(-time.Nanosecond)
			revisited, err := fixture.coordinator.Advance(context.Background(), due.ID)
			if err != nil || revisited.TransitionRevision != nextBefore.TransitionRevision ||
				!sameLifecycleTimePtr(revisited.RetryAt, nextBefore.RetryAt) ||
				fixture.deleter.prepareCalls != prepareCalls+test.wantPrepare ||
				fixture.deleter.calls != providerCalls {
				t.Fatalf("revisited claimed blocked attempt=%+v error=%v prepare/calls=%d/%d, want unchanged after due retry",
					revisited, err, fixture.deleter.prepareCalls, fixture.deleter.calls)
			}
		})
	}
}

type directRegistryDeletionFixture struct {
	db          *gorm.DB
	adapter     *RegistryPointDeletion
	coordinator *Coordinator
	holds       *HoldService
	deleter     *registryPointDeleterFake
	request     LifecyclePointRequest
	pointID     string
	attemptID   string
	now         time.Time
}

func newDirectRegistryDeletionFixture(
	t *testing.T,
	resolver PointDeletionAccessResolver,
	outcome provider.DeletePointOutcome,
	deleterErr error,
) directRegistryDeletionFixture {
	t.Helper()
	db := newLifecycleCoordinatorTestDB(t)
	now := time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)
	repositoryID, pointID, leaseID, attemptID := testOpaqueID(1700), testOpaqueID(1701), testOpaqueID(1702), testOpaqueID(1703)
	seedRetentionUsersAndRepository(t, db, repositoryID)
	point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-time.Hour), 1)
	point.State = string(backupasset.RecoveryPointExpiring)
	point.SourceFingerprint = strings.Repeat("c", 64)
	point.EncryptedProviderLocator = `{"snapshot":"direct-registry-private-locator"}`
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("seed direct registry point: %v", err)
	}
	owner := "retention-worker-direct-registry"
	fenceToken := strings.Repeat("a", 64)
	fenceHash := hashFenceToken(fenceToken)
	lease := model.RecoveryPointLease{
		ID: leaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderRetentionWorker),
		OwnerID: owner, AttemptID: attemptID, FenceToken: fenceToken, Status: string(backupasset.LeaseActive),
		LeaseExpiresAt: now.Add(5 * time.Minute), AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&lease).Error; err != nil {
		t.Fatalf("seed direct registry lease: %v", err)
	}
	attempt := model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire),
		Phase: string(backupasset.LifecyclePhaseProviderDelete), TransitionRevision: 1,
		LeaseID: &leaseID, LeaseAttemptID: &attemptID, LeaseFenceTokenHash: &fenceHash,
		ClaimedAt: &now, HeartbeatAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatalf("seed direct registry attempt: %v", err)
	}
	leaseService, err := backupasset.NewLeaseService(db, func() time.Time { return now }, backupasset.LeaseConfig{
		Duration: 5 * time.Minute, Heartbeat: time.Minute, AbsoluteDeadline: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewLeaseService: %v", err)
	}
	coordinator := &Coordinator{db: db, leases: leaseService, now: func() time.Time { return now }, leaseOwnerID: owner}
	_, authority, blocked, err := coordinator.prepareExternalEffect(context.Background(), attemptID, backupasset.LifecyclePhaseProviderDelete)
	if err != nil || blocked {
		t.Fatalf("prepare direct registry authority=%+v blocked=%t error=%v", authority, blocked, err)
	}
	snapshot := provider.ReadSnapshot{
		RepositoryID: repositoryID, CapabilityRevision: 1, SourceRevision: strings.Repeat("c", 64),
		Access: provider.AccessBinding{Provider: backupasset.ProviderRestic, RepositoryID: repositoryID},
	}
	if _, ok := resolver.(registryDeletePointResolver); ok {
		resolver = registryDeletePointResolver{snapshot: snapshot}
	}
	deleter := &registryPointDeleterFake{
		kind:   backupasset.ProviderRestic,
		result: provider.DeletePointResult{Outcome: outcome, ReceiptDigest: strings.Repeat("d", 64)},
		err:    deleterErr,
	}
	registry := provider.NewRegistry()
	if err := registry.Register(backupasset.ProviderRestic, provider.Registration{Prober: &retentionProviderProberFake{}, PointDeleter: deleter}); err != nil {
		t.Fatalf("register direct registry deleter: %v", err)
	}
	adapter, err := NewRegistryPointDeletion(db, registry, resolver)
	if err != nil {
		t.Fatalf("NewRegistryPointDeletion: %v", err)
	}
	holds, err := NewHoldService(HoldServiceDependencies{DB: db, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("NewHoldService: %v", err)
	}
	return directRegistryDeletionFixture{
		db: db, adapter: adapter, coordinator: coordinator, holds: holds, deleter: deleter,
		request: LifecyclePointRequest{
			RecoveryPointID: pointID, AttemptID: attemptID,
			Operation: backupasset.LifecycleRetentionExpire, authority: authority,
		},
		pointID: pointID, attemptID: attemptID, now: now,
	}
}

type transactionalMutationDeleteResolver struct {
	mutatePoint      bool
	mutateRepository bool
}

func (resolver transactionalMutationDeleteResolver) ResolveDeletePoint(
	_ context.Context,
	tx *gorm.DB,
	request LifecyclePointRequest,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	if resolver.mutatePoint {
		if err := tx.Model(&model.RecoveryPoint{}).Where("id = ?", point.ID).
			Update("source_fingerprint", strings.Repeat("e", 64)).Error; err != nil {
			return provider.DeletePointRequest{}, err
		}
	}
	if resolver.mutateRepository {
		if err := tx.Model(&model.BackupRepository{}).Where("id = ?", repository.ID).
			Update("capability_revision", 2).Error; err != nil {
			return provider.DeletePointRequest{}, err
		}
	}
	return registryDeletePointResolver{
		snapshot: provider.ReadSnapshot{
			RepositoryID: repository.ID, CapabilityRevision: point.CapabilityRevision,
			SourceRevision: point.SourceFingerprint,
			Access:         provider.AccessBinding{Provider: backupasset.ProviderRestic, RepositoryID: repository.ID},
		},
	}.ResolveDeletePoint(context.Background(), tx, request, point, repository)
}

func sequentialOpaqueIDs(start uint64) func() (string, error) {
	next := start
	return func() (string, error) {
		id := testOpaqueID(next)
		next++
		return id, nil
	}
}
