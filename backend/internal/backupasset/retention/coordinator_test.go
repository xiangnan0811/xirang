package retention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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
			point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-96*time.Hour), 3)
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
				RepositoryID: repositoryID, CapabilityRevision: 3, SourceRevision: strings.Repeat("c", 64),
				Access: provider.AccessBinding{Provider: backupasset.ProviderRestic, RepositoryID: repositoryID},
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
					Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 20, CapabilityRevision: 3}},
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
	point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-96*time.Hour), 3)
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
		RepositoryID: repositoryID, CapabilityRevision: 3, SourceRevision: strings.Repeat("c", 64),
		Access: provider.AccessBinding{Provider: backupasset.ProviderRestic, RepositoryID: repositoryID},
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
			Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 8, CapabilityRevision: 3}},
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
		blocked.BlockedReason != backupasset.LifecycleBlockedProviderWORM {
		t.Fatalf("WORM blocked attempt=%+v err=%v", blocked, err)
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
	point := newSelectionPoint(pointID, repositoryID, nil, now.Add(-96*time.Hour), 3)
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
		RepositoryID: repositoryID, CapabilityRevision: 3, SourceRevision: strings.Repeat("c", 64),
		Access: provider.AccessBinding{Provider: backupasset.ProviderRestic, RepositoryID: repositoryID},
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
			Points: []SelectedPoint{{RecoveryPointID: pointID, PointRevision: 5, CapabilityRevision: 3}},
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

func TestLifecycleProviderFailuresRemainPurgeBlockedAndRetryFenced(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason backupasset.LifecycleBlockedReason
	}{
		{name: "worm", err: ErrPointDeletionWORM, reason: backupasset.LifecycleBlockedProviderWORM},
		{name: "unavailable", err: backupasset.ErrProviderUnavailable, reason: backupasset.LifecycleBlockedProviderUnavailable},
		{name: "identity", err: ErrPointDeletionIdentityConflict, reason: backupasset.LifecycleBlockedProviderIdentityConflict},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newClaimedExpiryFixture(t, uint64(700+index*10))
			fixture.deleter.err = test.err
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
				t.Fatalf("provider block attempt=%+v error=%v, want %q", attempt, err, test.reason)
			}
			assertLifecyclePointState(t, fixture.db, fixture.pointID, backupasset.RecoveryPointPurgeBlocked)

			fixture.clock = fixture.clock.Add(6 * time.Minute)
			fixture.deleter.err = nil
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
			blocked, _ := blockTerminalEventAtAbsoluteDeadline(t, fixture, test.base+10)
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
	blocked, _ := blockTerminalEventAtAbsoluteDeadline(t, fixture, 1230)
	if err := fixture.db.Where("recovery_point_id = ? AND terminal_operation = ?",
		fixture.pointID, backupasset.LifecycleRetentionExpire).
		Delete(&model.RecoveryPointLifecycleTombstone{}).Error; err != nil {
		t.Fatalf("remove terminal event for missing-event recovery: %v", err)
	}
	fixture.clock = blocked.RetryAt.UTC().Add(time.Second)
	cleanupBefore, deleteBefore := fixture.cleanup.calls, fixture.deleter.calls
	retried, err := restartTerminalEventCoordinator(t, fixture, 1231).Advance(context.Background(), blocked.ID)
	if err != nil || retried.Phase != backupasset.LifecyclePhaseRevoking ||
		fixture.cleanup.calls != cleanupBefore || fixture.deleter.calls != deleteBefore {
		t.Fatalf("missing event retry phase/effect deltas=%q/%d/%d error=%v, want revoking/0/0",
			retried.Phase, fixture.cleanup.calls-cleanupBefore, fixture.deleter.calls-deleteBefore, err)
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
	fixture.deleter.err = backupasset.ErrProviderUnavailable
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
	fixture.deleter.err = nil
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
	if err != nil || adopted.Phase != backupasset.LifecyclePhaseComplete || fixture.deleter.calls != 2 {
		t.Fatalf("complete adopted lifecycle attempt=%+v delete_calls=%d error=%v", adopted, fixture.deleter.calls, err)
	}
}

func TestLifecycleBlockedAttemptHasExactlyOnePostgresFenceAdopter(t *testing.T) {
	fixture := newClaimedExpiryFixtureWithDB(t, newLifecycleCoordinatorPostgresTestDB(t), 900)
	fixture.deleter.err = backupasset.ErrProviderUnavailable
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
			if err := fixture.db.Model(&model.RecoveryPoint{}).Where("id = ?", fixture.pointID).
				Update("state", backupasset.RecoveryPointPurgeBlocked).Error; err != nil {
				t.Fatalf("make point otherwise hold-eligible during forced race: %v", err)
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

			blocked, err := fixture.coordinator.Advance(context.Background(), attempt.ID)
			if err != nil || blocked.Phase != backupasset.LifecyclePhaseBlocked ||
				blocked.BlockedReason != backupasset.LifecycleBlockedFenceLost {
				t.Fatalf("stale effect attempt=%+v error=%v, want blocked/fence_lost", blocked, err)
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
	calls     int
	completed int
	pointID   string
	attemptID string
	result    PointDeletionResult
	err       error
	entered   chan struct{}
	release   chan struct{}
	operation backupasset.LifecycleOperation
}

type registryDeletePointResolver struct {
	snapshot provider.ReadSnapshot
}

func (resolver registryDeletePointResolver) ResolveDeletePoint(
	_ context.Context,
	request LifecyclePointRequest,
	point model.RecoveryPoint,
	repository model.BackupRepository,
) (provider.DeletePointRequest, error) {
	if point.EncryptedProviderLocator == "" || repository.ID != resolver.snapshot.RepositoryID {
		return provider.DeletePointRequest{}, provider.ErrDeletePointIdentityConflict
	}
	return provider.DeletePointRequest{
		Snapshot:               resolver.snapshot,
		Point:                  provider.PointLocator{Native: point.EncryptedProviderLocator},
		ExpectedSourceRevision: resolver.snapshot.SourceRevision,
		OperationID:            request.AttemptID,
	}, nil
}

type registryPointDeleterFake struct {
	kind    backupasset.ProviderKind
	result  provider.DeletePointResult
	err     error
	calls   int
	request provider.DeletePointRequest
}

func (fake *registryPointDeleterFake) ProviderKind() backupasset.ProviderKind {
	return fake.kind
}

func (fake *registryPointDeleterFake) DeletePoint(_ context.Context, request provider.DeletePointRequest) (provider.DeletePointResult, error) {
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}

type retentionProviderProberFake struct{}

func (*retentionProviderProberFake) Probe(context.Context, provider.AccessBinding, provider.OperationLimits) (provider.RepositoryObservation, error) {
	return provider.RepositoryObservation{}, nil
}

func (fake *lifecycleDeletionFake) DeleteRecoveryPoint(_ context.Context, request LifecyclePointRequest) (PointDeletionResult, error) {
	fake.calls++
	fake.operation = request.Operation
	fake.pointID = request.RecoveryPointID
	fake.attemptID = request.AttemptID
	if fake.entered != nil {
		close(fake.entered)
		<-fake.release
	}
	if fake.err == nil {
		fake.completed++
	}
	return fake.result, fake.err
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

func (audit *recordingSettledAudit) HasSettledDeletion(recoveryPointID, attemptID, status string) bool {
	for _, event := range audit.events {
		if event.Action != backupasset.AuditActionRepositoryPurge || event.RecoveryPointID != recoveryPointID {
			continue
		}
		if event.Fields[backupasset.AuditFieldStage] != "settled" || event.Fields[backupasset.AuditFieldStatus] != status {
			continue
		}
		if source, _ := event.Fields[backupasset.AuditFieldSource].(string); source != "" && source != attemptID {
			continue
		}
		return true
	}
	return false
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

func (audit *flakySettledAudit) HasSettledDeletion(recoveryPointID, attemptID, status string) bool {
	for _, event := range audit.events {
		if event.RecoveryPointID != recoveryPointID || event.Fields[backupasset.AuditFieldStatus] != status {
			continue
		}
		if source, _ := event.Fields[backupasset.AuditFieldSource].(string); source != "" && source != attemptID {
			continue
		}
		return true
	}
	return false
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

func TestLifecycleSettledDeletionAuditFailureStaysOnProviderDelete(t *testing.T) {
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
	if attempt.Phase != backupasset.LifecyclePhaseProviderDelete {
		t.Fatalf("phase after failed audit = %q, want %q", attempt.Phase, backupasset.LifecyclePhaseProviderDelete)
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
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil {
		t.Fatalf("retry after healthy audit: %v", err)
	}
	if attempt.Phase != backupasset.LifecyclePhaseTombstoning {
		t.Fatalf("phase after healthy audit = %q, want %q", attempt.Phase, backupasset.LifecyclePhaseTombstoning)
	}
	if fixture.deleter.calls != 1 {
		t.Fatalf("deleter calls after audit retry = %d, want 1", fixture.deleter.calls)
	}
	if audit.settledCalls != 1 {
		t.Fatalf("settled audit writes = %d, want 1", audit.settledCalls)
	}

	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil {
		t.Fatalf("complete after tombstone: %v", err)
	}
	if attempt.Phase != backupasset.LifecyclePhaseComplete {
		t.Fatalf("phase = %q, want complete", attempt.Phase)
	}
}

func TestLifecycleBlockedProviderAuditFailureRetriesBeforeLeavingBlocked(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 1420)
	fixture.deleter.err = ErrPointDeletionWORM
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
		t.Fatal("expected blocked provider audit failure")
	}
	if attempt.Phase != backupasset.LifecyclePhaseBlocked {
		t.Fatalf("phase after failed blocked audit = %q, want %q", attempt.Phase, backupasset.LifecyclePhaseBlocked)
	}
	if attempt.Phase == backupasset.LifecyclePhaseComplete {
		t.Fatal("attempt must not complete without blocked provider audit")
	}
	if audit.settledCalls != 0 {
		t.Fatalf("settled audit writes = %d, want 0", audit.settledCalls)
	}
	if attempt.RetryAt == nil {
		t.Fatal("retry_at must be set after blocked audit failure")
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
	if attempt.Phase == backupasset.LifecyclePhaseComplete {
		t.Fatal("attempt completed without proving the blocked audit first")
	}
}

func TestLifecycleHealthyBlockedTickDoesNotRewriteSettledAudit(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 1520)
	fixture.deleter.err = ErrPointDeletionWORM
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
	if err != nil {
		t.Fatalf("block with healthy settled audit: %v", err)
	}
	if attempt.Phase != backupasset.LifecyclePhaseBlocked {
		t.Fatalf("phase=%q, want blocked", attempt.Phase)
	}
	if len(audit.events) != 1 {
		t.Fatalf("settled audit writes=%d, want 1", len(audit.events))
	}
	attempt, err = fixture.coordinator.Advance(context.Background(), attempt.ID)
	if err != nil {
		t.Fatalf("healthy blocked retry before RetryAt: %v", err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("healthy blocked tick rewrote settled audit writes=%d, want 1", len(audit.events))
	}
}

func TestLifecycleBlockedAuditRetriesAfterReasonChangesToHold(t *testing.T) {
	fixture := newClaimedExpiryFixture(t, 1620)
	fixture.deleter.err = ErrPointDeletionWORM
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
		t.Fatal("expected blocked provider audit failure")
	}
	if audit.settledCalls != 0 {
		t.Fatalf("settled audit writes=%d, want 0", audit.settledCalls)
	}
	if err := fixture.db.Model(&model.RecoveryPointLifecycleAttempt{}).
		Where("id = ?", attempt.ID).
		Update("blocked_reason", backupasset.LifecycleBlockedActiveHold).Error; err != nil {
		t.Fatalf("change blocked reason: %v", err)
	}
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

func sequentialOpaqueIDs(start uint64) func() (string, error) {
	next := start
	return func() (string, error) {
		id := testOpaqueID(next)
		next++
		return id, nil
	}
}
