package search

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type searchGenerationDiagnosticAnalysis struct {
	ownerHelpers         int
	typedBindings        int
	formatCalls          int
	safeGenerationFields int
	fullValueViolations  []int
}

type searchGenerationExpressionKind uint8

const (
	searchGenerationExpressionNone searchGenerationExpressionKind = iota
	searchGenerationExpressionWhole
	searchGenerationExpressionPrivate
)

type searchGenerationParameterSet map[int]struct{}

type searchGenerationFunctionSummary struct {
	resultKinds      []searchGenerationExpressionKind
	resultParameters []searchGenerationParameterSet
	parameterKinds   []searchGenerationExpressionKind
}

type searchDiagnosticBinding struct {
	object types.Object
	name   string
}

type searchGenerationValueSet struct {
	bindings map[searchDiagnosticBinding]searchGenerationExpressionKind
	names    map[string]searchGenerationExpressionKind
	typeInfo *types.Info
}

func newSearchGenerationValueSet(typeInfo *types.Info) *searchGenerationValueSet {
	return &searchGenerationValueSet{
		bindings: make(map[searchDiagnosticBinding]searchGenerationExpressionKind),
		names:    make(map[string]searchGenerationExpressionKind),
		typeInfo: typeInfo,
	}
}

func (values *searchGenerationValueSet) identifier(identifier *ast.Ident) searchGenerationExpressionKind {
	binding := searchDiagnosticIdentifierBinding(identifier, values.typeInfo)
	if kind, exists := values.bindings[binding]; exists {
		return kind
	}
	return values.names[identifier.Name]
}

func (values *searchGenerationValueSet) setIdentifier(identifier *ast.Ident, kind searchGenerationExpressionKind) {
	values.bindings[searchDiagnosticIdentifierBinding(identifier, values.typeInfo)] = kind
}

func (values *searchGenerationValueSet) mergeIdentifier(
	identifier *ast.Ident,
	kind searchGenerationExpressionKind,
) bool {
	binding := searchDiagnosticIdentifierBinding(identifier, values.typeInfo)
	current, exists := values.bindings[binding]
	if !exists {
		values.bindings[binding] = kind
		return kind != searchGenerationExpressionNone
	}
	merged := mergeSearchGenerationExpression(current, kind)
	if merged == current {
		return false
	}
	values.bindings[binding] = merged
	return true
}

func TestSearchSourceLifecycleDiagnosticsDoNotFormatFullGeneration(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Search source lifecycle test file")
	}
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, filename, nil, 0)
	if err != nil {
		t.Fatalf("parse Search source lifecycle tests: %v", err)
	}
	analysis := analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.ownerHelpers == 0 {
		t.Fatal("Search diagnostic privacy analyzer scanned no test or helper functions")
	}
	if analysis.typedBindings == 0 {
		t.Fatal("Search diagnostic privacy analyzer found no BackupAssetSearchGeneration bindings")
	}
	if analysis.formatCalls == 0 || analysis.safeGenerationFields == 0 {
		t.Fatalf("Search diagnostic format_calls=%d safe_generation_fields=%d, want non-vacuous safe field diagnostics",
			analysis.formatCalls, analysis.safeGenerationFields)
	}
	for _, line := range analysis.fullValueViolations {
		t.Errorf("source_lifecycle_test.go:%d formats BackupAssetSearchGeneration as a full struct; report only safe state and boolean facts", line)
	}
}

func TestSearchGenerationDiagnosticPrivacyAnalyzerRejectsAliasesAndFullValueVerbs(t *testing.T) {
	mutations := []struct {
		name string
		body string
	}{
		{name: "Errorf plain alias", body: `alias := generation; t.Errorf("%v", alias)`},
		{name: "Fatalf plus address", body: `t.Fatalf("%+v", &generation)`},
		{name: "Logf sharp conversion", body: `t.Logf("%#v", any(generation))`},
		{name: "Printf minus pointer alias", body: `alias := &generation; fmt.Printf("%-v", *alias)`},
		{name: "Sprintf zero alias format", body: `alias := generation; format := "%0v"; fmt.Sprintf(format, alias)`},
		{name: "Sprintf space indirect", body: `alias := any(generation); fmt.Sprintf("% v", alias)`},
		{name: "Errorf private selector", body: `t.Errorf("%v", generation.SourceFingerprint)`},
		{name: "Fatalf private selector alias", body: `alias := generation; t.Fatalf("%+v", alias.LeaseID)`},
		{name: "Logf indexed generation", body: `values := []any{generation}; t.Logf("%v", values[0])`},
		{name: "Printf index list generation", body: `values := []any{generation}; fmt.Printf("%#v", values[int, string])`},
		{name: "Errorf map private key", body: `values := map[any]bool{generation.SourceFingerprint: true}; t.Errorf("%v", values)`},
		{name: "Errorf ranged private key", body: `values := map[any]bool{generation.SourceFingerprint: true}; for key := range values { t.Errorf("%v", key) }`},
		{name: "Errorf ranged private value", body: `values := map[string]any{"safe": generation.LeaseID}; for _, value := range values { t.Errorf("%v", value) }`},
		{name: "Sprintf explicit argument index", body: `fmt.Sprintf("%[1]v", generation)`},
		{name: "Errorf whole string", body: `t.Errorf("%s", generation)`},
		{name: "Fatalf flagged quoted private selector", body: `t.Fatalf("%+q", generation.SourceFingerprint)`},
		{name: "Logf flagged explicit index hex private selector", body: `t.Logf("%#[1]x", generation.LeaseID)`},
		{name: "Errorf formatter alias", body: `format := "%v"; emit := t.Errorf; emit(format, generation)`},
		{name: "Errorf dynamic format call", body: `format := makeFormat(); t.Errorf(format, generation)`},
		{name: "Fatalf dynamic format reassignment", body: `format := "state=%q"; format = makeFormat(); t.Fatalf(format, generation)`},
		{name: "Logf formatter alias dynamic format", body: `emit := t.Logf; format := makeFormat(); emit(format, generation)`},
		{name: "Printf formatter reassignment dynamic format", body: `emit := fmt.Printf; emit = makeFormatter(); format := makeFormat(); emit(format, generation)`},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			source := "package search\nfunc assertSearchGeneration(t *testing.T) { var generation model.BackupAssetSearchGeneration; " + mutation.body + " }"
			files := token.NewFileSet()
			parsed, err := parser.ParseFile(files, "mutation.go", source, 0)
			if err != nil {
				t.Fatalf("parse diagnostic mutation: %v", err)
			}
			analysis := analyzeSearchGenerationDiagnostics(files, parsed)
			if analysis.ownerHelpers != 1 || analysis.typedBindings != 1 || analysis.formatCalls != 1 || len(analysis.fullValueViolations) != 1 {
				t.Fatalf("mutation analysis owners=%d bindings=%d format_calls=%d violations=%d, want 1/1/1/1",
					analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls, len(analysis.fullValueViolations))
			}
		})
	}

	const safeSource = `package search
func assertSearchGeneration(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	t.Fatalf("id=%v state=%+v active=%#v expected=%v written=%v",
		generation.ID, generation.State, generation.IsActive,
		generation.ExpectedDocumentCount, generation.WrittenDocumentCount)
}`
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, "safe.go", safeSource, 0)
	if err != nil {
		t.Fatalf("parse safe diagnostic: %v", err)
	}
	analysis := analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.safeGenerationFields != 5 || len(analysis.fullValueViolations) != 0 {
		t.Fatalf("safe diagnostic fields=%d violations=%d, want 5/0", analysis.safeGenerationFields, len(analysis.fullValueViolations))
	}

	const nonConsumingSafeSource = `package search
func assertSearchGeneration(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	t.Logf("type=%T literal=%% state=%s active=%q id=%x", generation,
		generation.State, generation.IsActive, generation.ID)
}`
	files = token.NewFileSet()
	parsed, err = parser.ParseFile(files, "non_consuming_safe.go", nonConsumingSafeSource, 0)
	if err != nil {
		t.Fatalf("parse non-consuming safe diagnostic: %v", err)
	}
	analysis = analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.formatCalls != 1 || analysis.safeGenerationFields != 3 || len(analysis.fullValueViolations) != 0 {
		t.Fatalf("non-consuming safe diagnostic format_calls=%d fields=%d violations=%d, want 1/3/0",
			analysis.formatCalls, analysis.safeGenerationFields, len(analysis.fullValueViolations))
	}

	const dynamicSafeSource = `package search
func assertSearchGeneration(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	format := makeFormat()
	t.Errorf(format, generation.State, generation.IsActive)
}`
	files = token.NewFileSet()
	parsed, err = parser.ParseFile(files, "dynamic_safe.go", dynamicSafeSource, 0)
	if err != nil {
		t.Fatalf("parse dynamic safe diagnostic: %v", err)
	}
	analysis = analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.formatCalls != 1 || analysis.safeGenerationFields != 2 || len(analysis.fullValueViolations) != 0 {
		t.Fatalf("dynamic safe diagnostic format_calls=%d fields=%d violations=%d, want 1/2/0",
			analysis.formatCalls, analysis.safeGenerationFields, len(analysis.fullValueViolations))
	}

	const ordinaryHelperSource = `package search
func loadSearchGeneration() model.BackupAssetSearchGeneration {
	return model.BackupAssetSearchGeneration{}
}
func TestOrdinarySearchGenerationDiagnostic(t *testing.T) {
	generation := loadSearchGeneration()
	privateAlias := generation.SourceFingerprint
	t.Errorf("%q", privateAlias)
}`
	files = token.NewFileSet()
	parsed, err = parser.ParseFile(files, "ordinary_helper.go", ordinaryHelperSource, 0)
	if err != nil {
		t.Fatalf("parse ordinary helper diagnostic: %v", err)
	}
	analysis = analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.ownerHelpers != 2 || analysis.typedBindings != 1 || analysis.formatCalls != 1 || len(analysis.fullValueViolations) != 1 {
		t.Fatalf("ordinary helper analysis owners=%d bindings=%d format_calls=%d violations=%d, want 2/1/1/1",
			analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls, len(analysis.fullValueViolations))
	}

	multiResultSources := []struct {
		name            string
		source          string
		wantBindings    int
		wantFormatCalls int
		wantViolations  int
	}{
		{
			name: "value declaration maps nonzero private result",
			source: `package search
func loadErrorThenGeneration() (error, model.BackupAssetSearchGeneration) {
	return nil, model.BackupAssetSearchGeneration{}
}
func TestMultiResultGenerationDiagnostic(t *testing.T) {
	_, generation := loadErrorThenGeneration()
	privateAlias := generation.SourceFingerprint
	t.Errorf("%q", privateAlias)
}`,
			wantBindings: 1, wantFormatCalls: 1, wantViolations: 1,
		},
		{
			name: "pointer assignment maps later private result",
			source: `package search
func loadMixedSearchGeneration() (error, string, *model.BackupAssetSearchGeneration) {
	return nil, "", &model.BackupAssetSearchGeneration{}
}
func TestMultiResultGenerationDiagnostic(t *testing.T) {
	var errValue error
	var safeValue string
	var generationValue any
	errValue, safeValue, generationValue = loadMixedSearchGeneration()
	privateAlias := generationValue
	t.Logf("error_type=%T safe=%s", errValue, safeValue)
	t.Errorf("%x", privateAlias)
}`,
			wantBindings: 1, wantFormatCalls: 2, wantViolations: 1,
		},
		{
			name: "safe result position is not contaminated",
			source: `package search
func loadErrorThenGeneration() (error, model.BackupAssetSearchGeneration) {
	return nil, model.BackupAssetSearchGeneration{}
}
func TestMultiResultGenerationDiagnostic(t *testing.T) {
	errValue, _ := loadErrorThenGeneration()
	t.Errorf("%v", errValue)
}`,
			wantBindings: 0, wantFormatCalls: 1, wantViolations: 0,
		},
	}
	for _, sourceCase := range multiResultSources {
		t.Run(sourceCase.name, func(t *testing.T) {
			files := token.NewFileSet()
			parsed, err := parser.ParseFile(files, "multi_result.go", sourceCase.source, 0)
			if err != nil {
				t.Fatalf("parse multi-result diagnostic: %v", err)
			}
			analysis := analyzeSearchGenerationDiagnostics(files, parsed)
			if analysis.ownerHelpers != 2 || analysis.typedBindings != sourceCase.wantBindings ||
				analysis.formatCalls != sourceCase.wantFormatCalls || len(analysis.fullValueViolations) != sourceCase.wantViolations {
				t.Fatalf("multi-result analysis owners=%d bindings=%d format_calls=%d violations=%d, want 2/%d/%d/%d",
					analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls, len(analysis.fullValueViolations),
					sourceCase.wantBindings, sourceCase.wantFormatCalls, sourceCase.wantViolations)
			}
		})
	}

	helperPropagationSources := []struct {
		name       string
		source     string
		wantOwners int
	}{
		{
			name: "generic identity index call",
			source: `package search
func identity[T any](value T) T { return value }
func TestHelperGenerationDiagnostic(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	privateAlias := identity[model.BackupAssetSearchGeneration](generation)
	t.Errorf("%v", privateAlias)
}`,
			wantOwners: 2,
		},
		{
			name: "generic identity index list call",
			source: `package search
func identityWithMarker[T, Marker any](value T) T { return value }
func TestHelperGenerationDiagnostic(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	privateAlias := identityWithMarker[model.BackupAssetSearchGeneration, struct{}](generation)
	t.Errorf("%v", privateAlias)
}`,
			wantOwners: 2,
		},
		{
			name: "private value crosses two ordinary helpers",
			source: `package search
func keepPrivate(value any) any { return value }
func relayPrivate(value any) any { return keepPrivate(value) }
func TestHelperGenerationDiagnostic(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	privateAlias := relayPrivate(generation.SourceFingerprint)
	t.Errorf("%q", privateAlias)
}`,
			wantOwners: 3,
		},
	}
	for _, sourceCase := range helperPropagationSources {
		t.Run(sourceCase.name, func(t *testing.T) {
			files := token.NewFileSet()
			parsed, err := parser.ParseFile(files, "helper_propagation.go", sourceCase.source, 0)
			if err != nil {
				t.Fatalf("parse helper propagation diagnostic: %v", err)
			}
			analysis := analyzeSearchGenerationDiagnostics(files, parsed)
			if analysis.ownerHelpers != sourceCase.wantOwners || analysis.typedBindings != 1 ||
				analysis.formatCalls != 1 || len(analysis.fullValueViolations) != 1 {
				t.Fatalf("helper propagation analysis owners=%d bindings=%d format_calls=%d violations=%d, want %d/1/1/1",
					analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls, len(analysis.fullValueViolations),
					sourceCase.wantOwners)
			}
		})
	}

	const binarySafeSource = `package search
func TestBinarySearchGenerationDiagnostic(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	present := generation.SourceFingerprint != ""
	equal := generation.LeaseID == generation.BuildAttemptID
	available := present && equal
	t.Logf("present=%t equal=%t available=%t", present, equal, available)
}`
	files = token.NewFileSet()
	parsed, err = parser.ParseFile(files, "binary_safe.go", binarySafeSource, 0)
	if err != nil {
		t.Fatalf("parse binary safe diagnostic: %v", err)
	}
	analysis = analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.ownerHelpers != 1 || analysis.typedBindings != 1 || analysis.formatCalls != 1 || len(analysis.fullValueViolations) != 0 {
		t.Fatalf("binary safe analysis owners=%d bindings=%d format_calls=%d violations=%d, want 1/1/1/0",
			analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls, len(analysis.fullValueViolations))
	}

	const rangeSafeSource = `package search
func TestRangeSafeSearchGenerationDiagnostic(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	values := []any{generation.State}
	for _, value := range values {
		t.Logf("%v", value)
	}
}`
	files = token.NewFileSet()
	parsed, err = parser.ParseFile(files, "range_safe.go", rangeSafeSource, 0)
	if err != nil {
		t.Fatalf("parse range safe diagnostic: %v", err)
	}
	analysis = analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.ownerHelpers != 1 || analysis.typedBindings != 1 || analysis.formatCalls != 1 || len(analysis.fullValueViolations) != 0 {
		t.Fatalf("range safe analysis owners=%d bindings=%d format_calls=%d violations=%d, want 1/1/1/0",
			analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls, len(analysis.fullValueViolations))
	}

	const lexicalShadowSource = `package search
func TestLexicalSearchGenerationDiagnostic(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	format := makeFormat()
	{
		format := "%T"
		_ = format
	}
	t.Errorf(format, generation)
}`
	files = token.NewFileSet()
	parsed, err = parser.ParseFile(files, "lexical_shadow.go", lexicalShadowSource, 0)
	if err != nil {
		t.Fatalf("parse lexical shadow diagnostic: %v", err)
	}
	analysis = analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.ownerHelpers != 1 || analysis.typedBindings != 1 || analysis.formatCalls != 1 || len(analysis.fullValueViolations) != 1 {
		t.Errorf("lexical shadow analysis owners=%d bindings=%d format_calls=%d violations=%d, want 1/1/1/1",
			analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls, len(analysis.fullValueViolations))
	}

	const voidHelperSource = `package search
func genericVoidSink[T any](t *testing.T, value T) { relayVoidSink(t, value) }
func relayVoidSink(t *testing.T, value any) { finalVoidSink(t, value) }
func finalVoidSink(t *testing.T, value any) { t.Errorf("%v", value) }
func TestVoidHelperSearchGenerationDiagnostic(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	genericVoidSink(t, generation)
}`
	files = token.NewFileSet()
	parsed, err = parser.ParseFile(files, "void_helper.go", voidHelperSource, 0)
	if err != nil {
		t.Fatalf("parse void helper diagnostic: %v", err)
	}
	analysis = analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.ownerHelpers != 4 || analysis.typedBindings != 1 || analysis.formatCalls != 1 || len(analysis.fullValueViolations) != 1 {
		t.Errorf("void helper analysis owners=%d bindings=%d format_calls=%d violations=%d, want 4/1/1/1",
			analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls, len(analysis.fullValueViolations))
	}

	const explicitReorderSafeSource = `package search
func TestExplicitReorderSearchGenerationDiagnostic(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	t.Logf("%[2]T", generation, generation.State)
}`
	files = token.NewFileSet()
	parsed, err = parser.ParseFile(files, "explicit_reorder_safe.go", explicitReorderSafeSource, 0)
	if err != nil {
		t.Fatalf("parse explicit reorder safe diagnostic: %v", err)
	}
	analysis = analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.ownerHelpers != 1 || analysis.typedBindings != 1 || analysis.formatCalls != 1 ||
		analysis.safeGenerationFields != 1 || len(analysis.fullValueViolations) != 0 {
		t.Errorf("explicit reorder safe analysis owners=%d bindings=%d format_calls=%d fields=%d violations=%d, want 1/1/1/1/0",
			analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls,
			analysis.safeGenerationFields, len(analysis.fullValueViolations))
	}

	const conditionalFormatJoinSource = `package search
func TestConditionalFormatJoinSearchGenerationDiagnostic(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	format := makeFormat()
	if condition() {
		format = "%T"
	}
	t.Errorf(format, generation)
}`
	files = token.NewFileSet()
	parsed, err = parser.ParseFile(files, "conditional_format_join.go", conditionalFormatJoinSource, 0)
	if err != nil {
		t.Fatalf("parse conditional format join diagnostic: %v", err)
	}
	analysis = analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.ownerHelpers != 1 || analysis.typedBindings != 1 || analysis.formatCalls != 1 || len(analysis.fullValueViolations) != 1 {
		t.Errorf("conditional format join analysis owners=%d bindings=%d format_calls=%d violations=%d, want 1/1/1/1",
			analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls, len(analysis.fullValueViolations))
	}

	const lexicalPrivateShadowSource = `package search
func TestLexicalPrivateShadowSearchGenerationDiagnostic(t *testing.T) {
	var generation model.BackupAssetSearchGeneration
	privateAlias := generation.SourceFingerprint
	{
		privateAlias := generation.State
		t.Logf("%v", privateAlias)
	}
	t.Errorf("%v", privateAlias)
}`
	files = token.NewFileSet()
	parsed, err = parser.ParseFile(files, "lexical_private_shadow.go", lexicalPrivateShadowSource, 0)
	if err != nil {
		t.Fatalf("parse lexical private shadow diagnostic: %v", err)
	}
	analysis = analyzeSearchGenerationDiagnostics(files, parsed)
	if analysis.ownerHelpers != 1 || analysis.typedBindings != 1 || analysis.formatCalls != 2 || len(analysis.fullValueViolations) != 1 {
		t.Errorf("lexical private shadow analysis owners=%d bindings=%d format_calls=%d violations=%d, want 1/1/2/1",
			analysis.ownerHelpers, analysis.typedBindings, analysis.formatCalls, len(analysis.fullValueViolations))
	}
}

func analyzeSearchGenerationDiagnostics(files *token.FileSet, parsed *ast.File) searchGenerationDiagnosticAnalysis {
	var analysis searchGenerationDiagnosticAnalysis
	typeInfo := &types.Info{
		Defs: make(map[*ast.Ident]types.Object),
		Uses: make(map[*ast.Ident]types.Object),
	}
	typeConfig := types.Config{Error: func(error) {}}
	_, _ = typeConfig.Check(parsed.Name.Name, files, []*ast.File{parsed}, typeInfo)
	generationFunctions := buildSearchGenerationFunctionSummaries(parsed, typeInfo)
	for _, declaration := range parsed.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if !isFunction || function.Body == nil || isSearchDiagnosticAnalyzerFunction(function.Name.Name) {
			continue
		}
		analysis.ownerHelpers++
		generationValues := searchGenerationFunctionValues(
			function, generationFunctions[function.Name.Name], generationFunctions, typeInfo)
		generationBindings := make(map[searchDiagnosticBinding]struct{})
		bindFields := func(fields *ast.FieldList) {
			if fields == nil {
				return
			}
			for _, field := range fields.List {
				if !isBackupAssetSearchGenerationType(field.Type) {
					continue
				}
				for _, name := range field.Names {
					generationValues.setIdentifier(name, searchGenerationExpressionWhole)
					generationBindings[searchDiagnosticIdentifierBinding(name, typeInfo)] = struct{}{}
				}
			}
		}
		bindFields(function.Type.Params)
		bindFields(function.Type.Results)
		ast.Inspect(function.Body, func(node ast.Node) bool {
			value, isValue := node.(*ast.ValueSpec)
			if !isValue || !isBackupAssetSearchGenerationType(value.Type) {
				return true
			}
			for _, name := range value.Names {
				generationValues.setIdentifier(name, searchGenerationExpressionWhole)
				generationBindings[searchDiagnosticIdentifierBinding(name, typeInfo)] = struct{}{}
			}
			return true
		})
		for changed := true; changed; {
			changed = false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch statement := node.(type) {
				case *ast.AssignStmt:
					for index := range statement.Lhs {
						identifier, isIdentifier := statement.Lhs[index].(*ast.Ident)
						if !isIdentifier || identifier.Name == "_" {
							continue
						}
						kind, typedResult := searchGenerationAssignmentKind(
							index, statement.Rhs, generationFunctions, generationValues)
						if typedResult {
							generationBindings[searchDiagnosticIdentifierBinding(identifier, typeInfo)] = struct{}{}
						}
						if generationValues.mergeIdentifier(identifier, kind) {
							changed = true
						}
					}
				case *ast.ValueSpec:
					for index, name := range statement.Names {
						if name.Name == "_" {
							continue
						}
						kind, typedResult := searchGenerationAssignmentKind(
							index, statement.Values, generationFunctions, generationValues)
						if typedResult {
							generationBindings[searchDiagnosticIdentifierBinding(name, typeInfo)] = struct{}{}
						}
						if generationValues.mergeIdentifier(name, kind) {
							changed = true
						}
					}
				}
				return true
			})
		}
		analysis.typedBindings += len(generationBindings)

		formatFunctions := make(map[string]struct{})
		for changed := true; changed; {
			changed = false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch statement := node.(type) {
				case *ast.AssignStmt:
					for index := range statement.Lhs {
						if index >= len(statement.Rhs) || !isSearchDiagnosticFormatCall(statement.Rhs[index], formatFunctions) {
							continue
						}
						identifier, isIdentifier := statement.Lhs[index].(*ast.Ident)
						if !isIdentifier {
							continue
						}
						if _, exists := formatFunctions[identifier.Name]; !exists {
							formatFunctions[identifier.Name] = struct{}{}
							changed = true
						}
					}
				case *ast.ValueSpec:
					for index, name := range statement.Names {
						if index >= len(statement.Values) || !isSearchDiagnosticFormatCall(statement.Values[index], formatFunctions) {
							continue
						}
						if _, exists := formatFunctions[name.Name]; !exists {
							formatFunctions[name.Name] = struct{}{}
							changed = true
						}
					}
				}
				return true
			})
		}

		formatValues := make(map[searchDiagnosticBinding]string)
		conditionalFormatAssignments := searchDiagnosticConditionalAssignments(function.Body, typeInfo)
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch statement := node.(type) {
			case *ast.AssignStmt:
				for index := range statement.Lhs {
					if index >= len(statement.Rhs) {
						continue
					}
					identifier, isIdentifier := statement.Lhs[index].(*ast.Ident)
					if !isIdentifier {
						continue
					}
					format, resolved := searchDiagnosticFormat(statement.Rhs[index], formatValues, typeInfo)
					binding := searchDiagnosticIdentifierBinding(identifier, typeInfo)
					if _, conditional := conditionalFormatAssignments[identifier]; conditional {
						current, currentResolved := formatValues[binding]
						if !resolved || !currentResolved || current != format {
							delete(formatValues, binding)
						}
					} else if resolved {
						formatValues[binding] = format
					} else {
						delete(formatValues, binding)
					}
				}
			case *ast.ValueSpec:
				for index, name := range statement.Names {
					if index >= len(statement.Values) {
						continue
					}
					format, resolved := searchDiagnosticFormat(statement.Values[index], formatValues, typeInfo)
					if resolved {
						formatValues[searchDiagnosticIdentifierBinding(name, typeInfo)] = format
					} else {
						delete(formatValues, searchDiagnosticIdentifierBinding(name, typeInfo))
					}
				}
			case *ast.CallExpr:
				if len(statement.Args) < 2 || !isSearchDiagnosticFormatCall(statement.Fun, formatFunctions) {
					return true
				}
				analysis.formatCalls++
				format, resolved := searchDiagnosticFormat(statement.Args[0], formatValues, typeInfo)
				privateArguments := make([]bool, 0, len(statement.Args)-1)
				for _, argument := range statement.Args[1:] {
					if searchGenerationFieldExpression(argument, generationValues, generationFunctions) {
						analysis.safeGenerationFields++
					}
					privateArguments = append(privateArguments,
						searchGenerationExpression(argument, generationValues, generationFunctions) != searchGenerationExpressionNone)
				}
				if searchDiagnosticHasPrivateArgument(privateArguments) &&
					(!resolved || searchDiagnosticFormatsPrivateValue(format, privateArguments)) {
					analysis.fullValueViolations = append(analysis.fullValueViolations, files.Position(statement.Pos()).Line)
				}
			}
			return true
		})
	}
	return analysis
}

func isSearchDiagnosticAnalyzerFunction(name string) bool {
	switch name {
	case "TestSearchGenerationDiagnosticPrivacyAnalyzerRejectsAliasesAndFullValueVerbs",
		"analyzeSearchGenerationDiagnostics",
		"isSearchDiagnosticAnalyzerFunction",
		"searchGenerationResultKinds",
		"searchGenerationParameterKinds",
		"buildSearchGenerationFunctionSummaries",
		"searchGenerationFunctionValues",
		"searchGenerationParameterAssignment",
		"searchGenerationParameterCallResults",
		"searchGenerationParameterExpression",
		"mergeSearchGenerationParameters",
		"mergeSearchGenerationParameterSet",
		"searchGenerationCalledFunction",
		"searchGenerationCallKinds",
		"searchGenerationAssignmentKind",
		"isBackupAssetSearchGenerationType",
		"mergeSearchGenerationExpression",
		"searchGenerationExpression",
		"isSearchGenerationSafeBinaryOperator",
		"isSearchGenerationTypeConversion",
		"searchGenerationFieldExpression",
		"isSafeSearchGenerationDiagnosticField",
		"searchDiagnosticIdentifierBinding",
		"searchDiagnosticConditionalAssignments",
		"searchDiagnosticFormat",
		"isSearchDiagnosticFormatCall",
		"searchDiagnosticHasPrivateArgument",
		"searchDiagnosticFormatsPrivateValue":
		return true
	default:
		return false
	}
}

func searchGenerationResultKinds(results *ast.FieldList) []searchGenerationExpressionKind {
	if results == nil {
		return nil
	}
	kinds := make([]searchGenerationExpressionKind, 0, len(results.List))
	for _, result := range results.List {
		kind := searchGenerationExpressionNone
		if isBackupAssetSearchGenerationType(result.Type) {
			kind = searchGenerationExpressionWhole
		}
		count := len(result.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func searchGenerationParameterKinds(parameters *ast.FieldList) []searchGenerationExpressionKind {
	if parameters == nil {
		return nil
	}
	kinds := make([]searchGenerationExpressionKind, 0, len(parameters.List))
	for _, parameter := range parameters.List {
		kind := searchGenerationExpressionNone
		if isBackupAssetSearchGenerationType(parameter.Type) {
			kind = searchGenerationExpressionWhole
		}
		count := len(parameter.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func buildSearchGenerationFunctionSummaries(
	parsed *ast.File,
	typeInfo *types.Info,
) map[string]*searchGenerationFunctionSummary {
	functions := make(map[string]*searchGenerationFunctionSummary)
	declarations := make(map[string]*ast.FuncDecl)
	for _, declaration := range parsed.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if !isFunction || function.Type == nil || isSearchDiagnosticAnalyzerFunction(function.Name.Name) {
			continue
		}
		resultKinds := searchGenerationResultKinds(function.Type.Results)
		functions[function.Name.Name] = &searchGenerationFunctionSummary{
			resultKinds:      resultKinds,
			resultParameters: make([]searchGenerationParameterSet, len(resultKinds)),
			parameterKinds:   searchGenerationParameterKinds(function.Type.Params),
		}
		declarations[function.Name.Name] = function
	}

	for changed := true; changed; {
		changed = false
		for name, function := range declarations {
			summary := functions[name]
			parameters := make(map[string]searchGenerationParameterSet)
			parameterIndex := 0
			if function.Type.Params != nil {
				for _, field := range function.Type.Params.List {
					count := len(field.Names)
					if count == 0 {
						count = 1
					}
					for _, parameterName := range field.Names {
						parameters[parameterName.Name] = searchGenerationParameterSet{parameterIndex: {}}
						parameterIndex++
					}
					if len(field.Names) == 0 {
						parameterIndex += count
					}
				}
			}
			for localChanged := true; localChanged; {
				localChanged = false
				ast.Inspect(function.Body, func(node ast.Node) bool {
					switch statement := node.(type) {
					case *ast.AssignStmt:
						for resultIndex, left := range statement.Lhs {
							identifier, isIdentifier := left.(*ast.Ident)
							if !isIdentifier || identifier.Name == "_" {
								continue
							}
							flow := searchGenerationParameterAssignment(
								resultIndex, statement.Rhs, functions, parameters)
							if mergeSearchGenerationParameters(parameters, identifier.Name, flow) {
								localChanged = true
							}
						}
					case *ast.ValueSpec:
						for resultIndex, valueName := range statement.Names {
							if valueName.Name == "_" {
								continue
							}
							flow := searchGenerationParameterAssignment(
								resultIndex, statement.Values, functions, parameters)
							if mergeSearchGenerationParameters(parameters, valueName.Name, flow) {
								localChanged = true
							}
						}
					}
					return true
				})
			}

			ast.Inspect(function.Body, func(node ast.Node) bool {
				statement, isReturn := node.(*ast.ReturnStmt)
				if !isReturn {
					return true
				}
				for resultIndex := range summary.resultKinds {
					flow := searchGenerationParameterAssignment(
						resultIndex, statement.Results, functions, parameters)
					if mergeSearchGenerationParameterSet(&summary.resultParameters[resultIndex], flow) {
						changed = true
					}
				}
				return true
			})
		}
	}

	for changed := true; changed; {
		changed = false
		for name, function := range declarations {
			generationValues := searchGenerationFunctionValues(function, functions[name], functions, typeInfo)
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, isCall := node.(*ast.CallExpr)
				if !isCall {
					return true
				}
				resolvedCall, callee, exists := searchGenerationCalledFunction(call, functions)
				if !exists {
					return true
				}
				for parameterIndex := range callee.parameterKinds {
					if parameterIndex >= len(resolvedCall.Args) {
						continue
					}
					kind := searchGenerationExpression(resolvedCall.Args[parameterIndex], generationValues, functions)
					merged := mergeSearchGenerationExpression(callee.parameterKinds[parameterIndex], kind)
					if merged != callee.parameterKinds[parameterIndex] {
						callee.parameterKinds[parameterIndex] = merged
						changed = true
					}
				}
				return true
			})
		}
	}
	return functions
}

func searchGenerationFunctionValues(
	function *ast.FuncDecl,
	summary *searchGenerationFunctionSummary,
	functions map[string]*searchGenerationFunctionSummary,
	typeInfo *types.Info,
) *searchGenerationValueSet {
	values := newSearchGenerationValueSet(typeInfo)
	for name, candidate := range functions {
		if len(candidate.resultKinds) == 1 && candidate.resultKinds[0] != searchGenerationExpressionNone {
			values.names[name] = candidate.resultKinds[0]
		}
	}
	parameterIndex := 0
	if function.Type.Params != nil {
		for _, field := range function.Type.Params.List {
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			for _, name := range field.Names {
				if summary != nil && parameterIndex < len(summary.parameterKinds) {
					values.mergeIdentifier(name, summary.parameterKinds[parameterIndex])
				}
				if isBackupAssetSearchGenerationType(field.Type) {
					values.setIdentifier(name, searchGenerationExpressionWhole)
				}
				parameterIndex++
			}
			if len(field.Names) == 0 {
				parameterIndex += count
			}
		}
	}
	if function.Type.Results != nil {
		for _, field := range function.Type.Results.List {
			if !isBackupAssetSearchGenerationType(field.Type) {
				continue
			}
			for _, name := range field.Names {
				values.setIdentifier(name, searchGenerationExpressionWhole)
			}
		}
	}
	ast.Inspect(function.Body, func(node ast.Node) bool {
		value, isValue := node.(*ast.ValueSpec)
		if !isValue || !isBackupAssetSearchGenerationType(value.Type) {
			return true
		}
		for _, name := range value.Names {
			values.setIdentifier(name, searchGenerationExpressionWhole)
		}
		return true
	})
	for changed := true; changed; {
		changed = false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch statement := node.(type) {
			case *ast.AssignStmt:
				for index, left := range statement.Lhs {
					identifier, isIdentifier := left.(*ast.Ident)
					if !isIdentifier || identifier.Name == "_" {
						continue
					}
					kind, _ := searchGenerationAssignmentKind(index, statement.Rhs, functions, values)
					if values.mergeIdentifier(identifier, kind) {
						changed = true
					}
				}
			case *ast.ValueSpec:
				for index, name := range statement.Names {
					if name.Name == "_" {
						continue
					}
					kind, _ := searchGenerationAssignmentKind(index, statement.Values, functions, values)
					if values.mergeIdentifier(name, kind) {
						changed = true
					}
				}
			case *ast.RangeStmt:
				kind := searchGenerationExpression(statement.X, values, functions)
				for _, expression := range []ast.Expr{statement.Key, statement.Value} {
					identifier, isIdentifier := expression.(*ast.Ident)
					if !isIdentifier || identifier.Name == "_" {
						continue
					}
					if values.mergeIdentifier(identifier, kind) {
						changed = true
					}
				}
			}
			return true
		})
	}
	return values
}

func searchGenerationParameterAssignment(
	resultIndex int,
	right []ast.Expr,
	functions map[string]*searchGenerationFunctionSummary,
	roots map[string]searchGenerationParameterSet,
) searchGenerationParameterSet {
	if len(right) == 1 {
		if results, isFunctionCall := searchGenerationParameterCallResults(right[0], functions, roots); isFunctionCall {
			if resultIndex >= len(results) {
				return nil
			}
			return results[resultIndex]
		}
	}
	if resultIndex >= len(right) {
		return nil
	}
	return searchGenerationParameterExpression(right[resultIndex], roots, functions)
}

func searchGenerationParameterCallResults(
	expression ast.Expr,
	functions map[string]*searchGenerationFunctionSummary,
	roots map[string]searchGenerationParameterSet,
) ([]searchGenerationParameterSet, bool) {
	call, summary, exists := searchGenerationCalledFunction(expression, functions)
	if !exists {
		return nil, false
	}
	results := make([]searchGenerationParameterSet, len(summary.resultKinds))
	for resultIndex, parameters := range summary.resultParameters {
		for parameterIndex := range parameters {
			if parameterIndex >= len(call.Args) {
				continue
			}
			mergeSearchGenerationParameterSet(&results[resultIndex],
				searchGenerationParameterExpression(call.Args[parameterIndex], roots, functions))
		}
	}
	return results, true
}

func searchGenerationParameterExpression(
	expression ast.Expr,
	roots map[string]searchGenerationParameterSet,
	functions map[string]*searchGenerationFunctionSummary,
) searchGenerationParameterSet {
	var flow searchGenerationParameterSet
	merge := func(other searchGenerationParameterSet) {
		mergeSearchGenerationParameterSet(&flow, other)
	}
	switch value := expression.(type) {
	case *ast.Ident:
		merge(roots[value.Name])
	case *ast.ParenExpr:
		merge(searchGenerationParameterExpression(value.X, roots, functions))
	case *ast.UnaryExpr:
		merge(searchGenerationParameterExpression(value.X, roots, functions))
	case *ast.StarExpr:
		merge(searchGenerationParameterExpression(value.X, roots, functions))
	case *ast.TypeAssertExpr:
		merge(searchGenerationParameterExpression(value.X, roots, functions))
	case *ast.SelectorExpr:
		merge(searchGenerationParameterExpression(value.X, roots, functions))
	case *ast.IndexExpr:
		merge(searchGenerationParameterExpression(value.X, roots, functions))
	case *ast.IndexListExpr:
		merge(searchGenerationParameterExpression(value.X, roots, functions))
	case *ast.SliceExpr:
		merge(searchGenerationParameterExpression(value.X, roots, functions))
	case *ast.CallExpr:
		if results, isFunctionCall := searchGenerationParameterCallResults(value, functions, roots); isFunctionCall {
			for _, result := range results {
				merge(result)
			}
			break
		}
		if isSearchGenerationTypeConversion(value.Fun) {
			for _, argument := range value.Args {
				merge(searchGenerationParameterExpression(argument, roots, functions))
			}
		}
	case *ast.BinaryExpr:
		if !isSearchGenerationSafeBinaryOperator(value.Op) {
			merge(searchGenerationParameterExpression(value.X, roots, functions))
			merge(searchGenerationParameterExpression(value.Y, roots, functions))
		}
	case *ast.CompositeLit:
		for _, element := range value.Elts {
			switch item := element.(type) {
			case *ast.KeyValueExpr:
				merge(searchGenerationParameterExpression(item.Key, roots, functions))
				merge(searchGenerationParameterExpression(item.Value, roots, functions))
			case ast.Expr:
				merge(searchGenerationParameterExpression(item, roots, functions))
			}
		}
	}
	return flow
}

func mergeSearchGenerationParameters(
	values map[string]searchGenerationParameterSet,
	name string,
	flow searchGenerationParameterSet,
) bool {
	current := values[name]
	changed := mergeSearchGenerationParameterSet(&current, flow)
	if changed {
		values[name] = current
	}
	return changed
}

func mergeSearchGenerationParameterSet(target *searchGenerationParameterSet, source searchGenerationParameterSet) bool {
	if len(source) == 0 {
		return false
	}
	if *target == nil {
		*target = make(searchGenerationParameterSet, len(source))
	}
	changed := false
	for parameterIndex := range source {
		if _, exists := (*target)[parameterIndex]; exists {
			continue
		}
		(*target)[parameterIndex] = struct{}{}
		changed = true
	}
	return changed
}

func searchGenerationCalledFunction(
	expression ast.Expr,
	functions map[string]*searchGenerationFunctionSummary,
) (*ast.CallExpr, *searchGenerationFunctionSummary, bool) {
	for {
		parenthesized, isParenthesized := expression.(*ast.ParenExpr)
		if !isParenthesized {
			break
		}
		expression = parenthesized.X
	}
	call, isCall := expression.(*ast.CallExpr)
	if !isCall {
		return nil, nil, false
	}
	function := call.Fun
	for {
		switch value := function.(type) {
		case *ast.ParenExpr:
			function = value.X
		case *ast.IndexExpr:
			function = value.X
		case *ast.IndexListExpr:
			function = value.X
		default:
			goto resolved
		}
	}
resolved:
	identifier, isIdentifier := function.(*ast.Ident)
	if !isIdentifier {
		return nil, nil, false
	}
	summary, exists := functions[identifier.Name]
	return call, summary, exists
}

func searchGenerationCallKinds(
	expression ast.Expr,
	functions map[string]*searchGenerationFunctionSummary,
	roots *searchGenerationValueSet,
) ([]searchGenerationExpressionKind, *searchGenerationFunctionSummary, bool) {
	call, summary, exists := searchGenerationCalledFunction(expression, functions)
	if !exists {
		return nil, nil, false
	}
	kinds := append([]searchGenerationExpressionKind(nil), summary.resultKinds...)
	for resultIndex, parameters := range summary.resultParameters {
		for parameterIndex := range parameters {
			if parameterIndex >= len(call.Args) {
				continue
			}
			kinds[resultIndex] = mergeSearchGenerationExpression(kinds[resultIndex],
				searchGenerationExpression(call.Args[parameterIndex], roots, functions))
		}
	}
	return kinds, summary, true
}

func searchGenerationAssignmentKind(
	resultIndex int,
	right []ast.Expr,
	functions map[string]*searchGenerationFunctionSummary,
	roots *searchGenerationValueSet,
) (searchGenerationExpressionKind, bool) {
	if len(right) == 1 {
		if results, summary, isFunctionCall := searchGenerationCallKinds(right[0], functions, roots); isFunctionCall {
			if resultIndex >= len(results) {
				return searchGenerationExpressionNone, false
			}
			return results[resultIndex], summary.resultKinds[resultIndex] == searchGenerationExpressionWhole
		}
	}
	if resultIndex >= len(right) {
		return searchGenerationExpressionNone, false
	}
	kind := searchGenerationExpression(right[resultIndex], roots, functions)
	_, summary, isFunctionCall := searchGenerationCallKinds(right[resultIndex], functions, roots)
	return kind, isFunctionCall && len(summary.resultKinds) == 1 &&
		summary.resultKinds[0] == searchGenerationExpressionWhole
}

func isBackupAssetSearchGenerationType(expression ast.Expr) bool {
	if pointer, isPointer := expression.(*ast.StarExpr); isPointer {
		return isBackupAssetSearchGenerationType(pointer.X)
	}
	selector, isSelector := expression.(*ast.SelectorExpr)
	if !isSelector || selector.Sel.Name != "BackupAssetSearchGeneration" {
		return false
	}
	identifier, isIdentifier := selector.X.(*ast.Ident)
	return isIdentifier && identifier.Name == "model"
}

func mergeSearchGenerationExpression(
	current searchGenerationExpressionKind,
	next searchGenerationExpressionKind,
) searchGenerationExpressionKind {
	if next == searchGenerationExpressionNone || current == next {
		return current
	}
	if current == searchGenerationExpressionNone {
		return next
	}
	return searchGenerationExpressionPrivate
}

func searchGenerationExpression(
	expression ast.Expr,
	roots *searchGenerationValueSet,
	functions map[string]*searchGenerationFunctionSummary,
) searchGenerationExpressionKind {
	switch value := expression.(type) {
	case *ast.Ident:
		return roots.identifier(value)
	case *ast.ParenExpr:
		return searchGenerationExpression(value.X, roots, functions)
	case *ast.UnaryExpr:
		return searchGenerationExpression(value.X, roots, functions)
	case *ast.StarExpr:
		return searchGenerationExpression(value.X, roots, functions)
	case *ast.TypeAssertExpr:
		kind := searchGenerationExpression(value.X, roots, functions)
		if isBackupAssetSearchGenerationType(value.Type) && kind != searchGenerationExpressionNone {
			return searchGenerationExpressionWhole
		}
		return kind
	case *ast.SelectorExpr:
		receiver := searchGenerationExpression(value.X, roots, functions)
		if receiver == searchGenerationExpressionNone {
			return searchGenerationExpressionNone
		}
		if receiver == searchGenerationExpressionWhole && isSafeSearchGenerationDiagnosticField(value.Sel.Name) {
			return searchGenerationExpressionNone
		}
		return searchGenerationExpressionPrivate
	case *ast.IndexExpr:
		if searchGenerationExpression(value.X, roots, functions) != searchGenerationExpressionNone {
			return searchGenerationExpressionPrivate
		}
	case *ast.IndexListExpr:
		if searchGenerationExpression(value.X, roots, functions) != searchGenerationExpressionNone {
			return searchGenerationExpressionPrivate
		}
	case *ast.SliceExpr:
		if searchGenerationExpression(value.X, roots, functions) != searchGenerationExpressionNone {
			return searchGenerationExpressionPrivate
		}
	case *ast.CallExpr:
		if results, _, isFunctionCall := searchGenerationCallKinds(value, functions, roots); isFunctionCall {
			kind := searchGenerationExpressionNone
			for _, result := range results {
				kind = mergeSearchGenerationExpression(kind, result)
			}
			return kind
		}
		functionKind := searchGenerationExpression(value.Fun, roots, functions)
		if functionKind == searchGenerationExpressionWhole {
			return searchGenerationExpressionWhole
		}
		if functionKind != searchGenerationExpressionNone {
			return searchGenerationExpressionPrivate
		}
		if isSearchGenerationTypeConversion(value.Fun) {
			kind := searchGenerationExpressionNone
			for _, argument := range value.Args {
				kind = mergeSearchGenerationExpression(kind, searchGenerationExpression(argument, roots, functions))
			}
			return kind
		}
	case *ast.BinaryExpr:
		if !isSearchGenerationSafeBinaryOperator(value.Op) &&
			(searchGenerationExpression(value.X, roots, functions) != searchGenerationExpressionNone ||
				searchGenerationExpression(value.Y, roots, functions) != searchGenerationExpressionNone) {
			return searchGenerationExpressionPrivate
		}
	case *ast.CompositeLit:
		if isBackupAssetSearchGenerationType(value.Type) {
			return searchGenerationExpressionWhole
		}
		for _, element := range value.Elts {
			switch item := element.(type) {
			case *ast.KeyValueExpr:
				if searchGenerationExpression(item.Key, roots, functions) != searchGenerationExpressionNone ||
					searchGenerationExpression(item.Value, roots, functions) != searchGenerationExpressionNone {
					return searchGenerationExpressionPrivate
				}
			case ast.Expr:
				if searchGenerationExpression(item, roots, functions) != searchGenerationExpressionNone {
					return searchGenerationExpressionPrivate
				}
			}
		}
	}
	return searchGenerationExpressionNone
}

func isSearchGenerationSafeBinaryOperator(operator token.Token) bool {
	switch operator {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ, token.LAND, token.LOR:
		return true
	default:
		return false
	}
}

func isSearchGenerationTypeConversion(expression ast.Expr) bool {
	switch value := expression.(type) {
	case *ast.Ident:
		switch value.Name {
		case "any", "string", "bool", "byte", "rune",
			"int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
			"float32", "float64", "complex64", "complex128":
			return true
		default:
			return false
		}
	case *ast.ArrayType, *ast.InterfaceType:
		return true
	case *ast.ParenExpr:
		return isSearchGenerationTypeConversion(value.X)
	default:
		return false
	}
}

func searchGenerationFieldExpression(
	expression ast.Expr,
	roots *searchGenerationValueSet,
	functions map[string]*searchGenerationFunctionSummary,
) bool {
	selector, isSelector := expression.(*ast.SelectorExpr)
	return isSelector && searchGenerationExpression(selector.X, roots, functions) == searchGenerationExpressionWhole &&
		isSafeSearchGenerationDiagnosticField(selector.Sel.Name)
}

func isSafeSearchGenerationDiagnosticField(name string) bool {
	switch name {
	case "ID", "State", "IsActive", "ExpectedDocumentCount", "WrittenDocumentCount":
		return true
	default:
		return false
	}
}

func searchDiagnosticIdentifierBinding(identifier *ast.Ident, typeInfo *types.Info) searchDiagnosticBinding {
	object := typeInfo.Defs[identifier]
	if object == nil {
		object = typeInfo.Uses[identifier]
	}
	return searchDiagnosticBinding{object: object, name: identifier.Name}
}

func searchDiagnosticConditionalAssignments(
	body *ast.BlockStmt,
	typeInfo *types.Info,
) map[*ast.Ident]struct{} {
	assignments := make(map[*ast.Ident]struct{})
	collect := func(branch ast.Node) {
		if branch == nil {
			return
		}
		ast.Inspect(branch, func(node ast.Node) bool {
			statement, isAssignment := node.(*ast.AssignStmt)
			if !isAssignment {
				return true
			}
			for _, left := range statement.Lhs {
				identifier, isIdentifier := left.(*ast.Ident)
				if isIdentifier && typeInfo.Uses[identifier] != nil {
					assignments[identifier] = struct{}{}
				}
			}
			return true
		})
	}
	ast.Inspect(body, func(node ast.Node) bool {
		switch statement := node.(type) {
		case *ast.IfStmt:
			collect(statement.Body)
			collect(statement.Else)
			return false
		case *ast.ForStmt:
			collect(statement.Body)
			return false
		case *ast.RangeStmt:
			collect(statement.Body)
			return false
		case *ast.SwitchStmt:
			collect(statement.Body)
			return false
		case *ast.TypeSwitchStmt:
			collect(statement.Body)
			return false
		case *ast.SelectStmt:
			collect(statement.Body)
			return false
		default:
			return true
		}
	})
	return assignments
}

func searchDiagnosticFormat(
	expression ast.Expr,
	values map[searchDiagnosticBinding]string,
	typeInfo *types.Info,
) (string, bool) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		format, err := strconv.Unquote(value.Value)
		return format, err == nil
	case *ast.Ident:
		format, exists := values[searchDiagnosticIdentifierBinding(value, typeInfo)]
		return format, exists
	case *ast.ParenExpr:
		return searchDiagnosticFormat(value.X, values, typeInfo)
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return "", false
		}
		left, leftOK := searchDiagnosticFormat(value.X, values, typeInfo)
		right, rightOK := searchDiagnosticFormat(value.Y, values, typeInfo)
		return left + right, leftOK && rightOK
	default:
		return "", false
	}
}

func isSearchDiagnosticFormatCall(expression ast.Expr, aliases map[string]struct{}) bool {
	name := ""
	switch function := expression.(type) {
	case *ast.SelectorExpr:
		name = function.Sel.Name
	case *ast.Ident:
		name = function.Name
		if _, exists := aliases[name]; exists {
			return true
		}
	case *ast.ParenExpr:
		return isSearchDiagnosticFormatCall(function.X, aliases)
	}
	switch name {
	case "Errorf", "Fatalf", "Logf", "Printf", "Sprintf":
		return true
	default:
		return false
	}
}

func searchDiagnosticHasPrivateArgument(arguments []bool) bool {
	for _, private := range arguments {
		if private {
			return true
		}
	}
	return false
}

func searchDiagnosticFormatsPrivateValue(format string, privateArguments []bool) bool {
	const decorations = "#+- 0.0123456789"
	consumed := make([]bool, len(privateArguments))
	nextArgument := 0
	for cursor := 0; cursor < len(format); {
		if format[cursor] != '%' {
			cursor++
			continue
		}
		cursor++
		if cursor >= len(format) {
			break
		}
		if format[cursor] == '%' {
			cursor++
			continue
		}

		for cursor < len(format) {
			switch {
			case format[cursor] == '[':
				closing := strings.IndexByte(format[cursor:], ']')
				if closing < 0 {
					cursor = len(format)
					continue
				}
				argument, err := strconv.Atoi(format[cursor+1 : cursor+closing])
				if err != nil || argument <= 0 {
					cursor += closing + 1
					continue
				}
				nextArgument = argument - 1
				for skipped := 0; skipped < nextArgument && skipped < len(consumed); skipped++ {
					consumed[skipped] = true
				}
				cursor += closing + 1
			case format[cursor] == '*':
				if nextArgument >= 0 && nextArgument < len(privateArguments) {
					consumed[nextArgument] = true
					if privateArguments[nextArgument] {
						return true
					}
				}
				nextArgument++
				cursor++
			case strings.ContainsRune(decorations, rune(format[cursor])):
				cursor++
			default:
				verb := format[cursor]
				if verb != '%' && nextArgument >= 0 && nextArgument < len(privateArguments) {
					consumed[nextArgument] = true
					if privateArguments[nextArgument] && verb != 'T' {
						return true
					}
				}
				if verb != '%' {
					nextArgument++
				}
				cursor++
				goto nextDirective
			}
		}
	nextDirective:
	}
	for index, private := range privateArguments {
		if private && !consumed[index] {
			return true
		}
	}
	return false
}

type searchCleanupProbeContextKey struct{}

type searchCleanupDeleteObservation struct {
	table string
	rows  int64
}

func TestNewSourceLifecycleSearchRejectsMismatchedIndexerDatabase(t *testing.T) {
	dbA, err := gorm.Open(sqlite.Open(t.TempDir()+"/search-owner-a.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open Search owner database: %v", err)
	}
	dbB, err := gorm.Open(sqlite.Open(t.TempDir()+"/search-indexer-b.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open Search indexer database: %v", err)
	}
	if owner, err := NewSourceLifecycle(dbA, &Indexer{}, time.Now, 1); owner != nil || !errors.Is(err, backupasset.ErrInvalidState) {
		t.Errorf("nil-db Search indexer owner=%t err=%v, want nil/invalid state", owner != nil, err)
	}
	invalidDB := &gorm.DB{Config: &gorm.Config{}}
	if owner, err := NewSourceLifecycle(invalidDB, &Indexer{db: invalidDB}, time.Now, 1); owner != nil || !errors.Is(err, backupasset.ErrInvalidState) {
		t.Errorf("unresolvable Search database owner=%t err=%v, want nil/invalid state", owner != nil, err)
	}
	for _, sameDatabase := range []struct {
		name string
		db   *gorm.DB
	}{
		{name: "Session", db: dbA.Session(&gorm.Session{})},
		{name: "WithContext", db: dbA.WithContext(context.Background())},
	} {
		t.Run(sameDatabase.name, func(t *testing.T) {
			if sameDatabase.db == dbA {
				t.Fatal("same-database Search fixture reused the owner *gorm.DB pointer")
			}
			owner, err := NewSourceLifecycle(dbA, &Indexer{db: sameDatabase.db}, time.Now, 1)
			if err != nil || owner == nil {
				t.Errorf("same-database Search clone owner=%t err=%v, want accepted", owner != nil, err)
			}
		})
	}

	pointID := strings.Repeat("1", 32)
	buildCtx, cancelBuild := context.WithCancel(context.Background())
	defer cancelBuild()
	indexer := &Indexer{
		db: dbB,
		attempts: map[string]activeSearchBuild{
			pointID: {cancel: cancelBuild, done: make(chan struct{})},
		},
	}
	if owner, err := NewSourceLifecycle(dbA, indexer, time.Now, 1); owner != nil || !errors.Is(err, backupasset.ErrInvalidState) {
		t.Errorf("cross-db Search owner=%t err=%v, want nil/invalid state", owner != nil, err)
	}
	if buildCtx.Err() != nil {
		t.Error("rejected cross-db Search owner canceled the same-point builder")
	}
}

func TestRecoveryPointSourceLifecycleSearchCleanupBoundsPayloadRowsPerTransactionAndRestarts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/source-bounded-cleanup.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open bounded cleanup database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
		&model.BackupAssetSearchPosting{}, &model.BackupAssetSearchDocumentField{},
	); err != nil {
		t.Fatalf("migrate bounded cleanup tables: %v", err)
	}
	now := time.Date(2026, 8, 17, 17, 40, 0, 0, time.UTC)
	pointID, otherPointID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	attemptID, generationID, otherGenerationID := strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 32)
	for _, point := range []model.RecoveryPoint{
		{ID: pointID, RepositoryID: strings.Repeat("6", 32)},
		{ID: otherPointID, RepositoryID: strings.Repeat("6", 32)},
	} {
		if err := db.Create(&point).Error; err != nil {
			t.Fatalf("seed bounded cleanup point: %v", err)
		}
	}
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire),
		Phase: string(backupasset.LifecyclePhaseCleaning),
	}).Error; err != nil {
		t.Fatalf("seed bounded cleanup attempt: %v", err)
	}
	for _, generation := range []model.BackupAssetSearchGeneration{
		{ID: generationID, RecoveryPointID: pointID, CatalogGenerationID: strings.Repeat("7", 32), Generation: 1, State: string(SearchGenerationComplete), IsActive: true, StartedAt: now},
		{ID: otherGenerationID, RecoveryPointID: otherPointID, CatalogGenerationID: strings.Repeat("8", 32), Generation: 1, State: string(SearchGenerationComplete), IsActive: true, StartedAt: now},
	} {
		if err := db.Create(&generation).Error; err != nil {
			t.Fatalf("seed bounded cleanup generation: %v", err)
		}
	}
	seedPayload := func(generationID, payloadPointID, catalogID string, count int) {
		t.Helper()
		for index := 0; index < count; index++ {
			documentID := strings.Repeat(string(rune('a'+index)), 64)
			if err := db.Create(&model.BackupAssetSearchDocument{
				SearchGenerationID: generationID, DocumentID: documentID, RecoveryPointID: payloadPointID,
				CatalogGenerationID: catalogID, EntryID: documentID, EntryType: "file", CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("seed bounded Search document: %v", err)
			}
			if err := db.Create(&model.BackupAssetSearchPosting{
				SearchGenerationID: generationID, DocumentID: documentID, Field: "name", TokenKind: "exact", TermFrequency: 1,
			}).Error; err != nil {
				t.Fatalf("seed bounded Search posting: %v", err)
			}
			if err := db.Create(&model.BackupAssetSearchDocumentField{
				SearchGenerationID: generationID, DocumentID: documentID, Field: "content", State: string(FieldCoverageComplete), UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("seed bounded Search field: %v", err)
			}
		}
	}
	const payloadRows = 5
	const rowBudget = 2
	seedPayload(generationID, pointID, strings.Repeat("7", 32), payloadRows)
	seedPayload(otherGenerationID, otherPointID, strings.Repeat("8", 32), 1)

	probeContext := context.WithValue(context.Background(), searchCleanupProbeContextKey{}, true)
	interrupted := errors.New("bounded Search cleanup interruption")
	interruptEnabled := true
	injectionAccepted := false
	generationQueries := 0
	currentTransactionDeleted := int64(0)
	var transactionDeleteTotals []int64
	var deleteObservations []searchCleanupDeleteObservation
	const generationQueryProbe = "search:test_bounded_cleanup_generation_query"
	if err := db.Callback().Query().Before("gorm:query").Register(generationQueryProbe, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(searchCleanupProbeContextKey{}) != true ||
			searchLifecycleCallbackTable(tx) != (model.BackupAssetSearchGeneration{}).TableName() {
			return
		}
		if currentTransactionDeleted > 0 {
			transactionDeleteTotals = append(transactionDeleteTotals, currentTransactionDeleted)
			currentTransactionDeleted = 0
		}
		generationQueries++
		if interruptEnabled && generationQueries == 2 {
			if addErr := tx.AddError(interrupted); errors.Is(addErr, interrupted) {
				injectionAccepted = true
			}
		}
	}); err != nil {
		t.Fatalf("register bounded cleanup query probe: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Query().Remove(generationQueryProbe); err != nil {
			t.Errorf("remove bounded cleanup query probe: %v", err)
		}
	})
	const deleteProbe = "search:test_bounded_cleanup_delete"
	if err := db.Callback().Delete().After("gorm:delete").Register(deleteProbe, func(tx *gorm.DB) {
		if tx.Statement.Context.Value(searchCleanupProbeContextKey{}) != true || tx.RowsAffected <= 0 {
			return
		}
		deleteObservations = append(deleteObservations, searchCleanupDeleteObservation{
			table: searchLifecycleCallbackTable(tx), rows: tx.RowsAffected,
		})
		currentTransactionDeleted += tx.RowsAffected
	}); err != nil {
		t.Fatalf("register bounded cleanup delete probe: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Delete().Remove(deleteProbe); err != nil {
			t.Errorf("remove bounded cleanup delete probe: %v", err)
		}
	})

	owner, err := NewSourceLifecycle(db, &Indexer{db: db}, func() time.Time { return now }, rowBudget)
	if err != nil {
		t.Fatalf("NewSourceLifecycle bounded cleanup: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecycleCleanup,
	}
	firstErr := owner.RevokeRecoveryPoint(probeContext, request)
	if !errors.Is(firstErr, interrupted) || !injectionAccepted {
		t.Errorf("first cleanup interrupted=%t injection_accepted=%t, want both true", errors.Is(firstErr, interrupted), injectionAccepted)
	}
	documents, postings, fields := searchGenerationPayloadCounts(t, db, generationID)
	remainingAfterInterruption := documents + postings + fields
	if remainingAfterInterruption <= 0 || remainingAfterInterruption >= payloadRows*3 {
		t.Errorf("interrupted cleanup remaining payload rows=%d, want committed partial progress between 1 and %d",
			remainingAfterInterruption, payloadRows*3-1)
	}
	otherDocuments, otherPostings, otherFields := searchGenerationPayloadCounts(t, db, otherGenerationID)
	if otherDocuments != 1 || otherPostings != 1 || otherFields != 1 {
		t.Errorf("unrelated generation payload documents=%d postings=%d fields=%d, want 1/1/1",
			otherDocuments, otherPostings, otherFields)
	}

	interruptEnabled = false
	restarted, err := NewSourceLifecycle(db, &Indexer{db: db}, func() time.Time { return now }, rowBudget)
	if err != nil {
		t.Fatalf("reconstruct Search source lifecycle: %v", err)
	}
	if err := restarted.RevokeRecoveryPoint(probeContext, request); err != nil {
		t.Fatalf("restart bounded Search cleanup: %v", err)
	}
	deletesAfterConvergence := len(deleteObservations)
	if err := restarted.RevokeRecoveryPoint(probeContext, request); err != nil {
		t.Fatalf("idempotent bounded Search cleanup: %v", err)
	}
	if len(deleteObservations) != deletesAfterConvergence {
		t.Errorf("idempotent cleanup added delete statements=%d, want zero", len(deleteObservations)-deletesAfterConvergence)
	}
	if currentTransactionDeleted > 0 {
		transactionDeleteTotals = append(transactionDeleteTotals, currentTransactionDeleted)
	}
	if len(deleteObservations) == 0 || len(transactionDeleteTotals) == 0 {
		t.Fatalf("bounded cleanup observations statements=%d transactions=%d, want non-zero", len(deleteObservations), len(transactionDeleteTotals))
	}
	for _, observation := range deleteObservations {
		if observation.rows > rowBudget {
			t.Errorf("Search cleanup DELETE table=%s rows=%d, want at most row budget %d", observation.table, observation.rows, rowBudget)
		}
	}
	for _, total := range transactionDeleteTotals {
		if total > rowBudget {
			t.Errorf("Search cleanup transaction deleted rows=%d, want at most row budget %d", total, rowBudget)
		}
	}
	stageByTable := map[string]int{
		(model.BackupAssetSearchDocumentField{}).TableName(): 0,
		(model.BackupAssetSearchPosting{}).TableName():       1,
		(model.BackupAssetSearchDocument{}).TableName():      2,
	}
	lastStage := -1
	for _, observation := range deleteObservations {
		stage, recognized := stageByTable[observation.table]
		if !recognized {
			t.Errorf("Search cleanup deleted unexpected table=%q", observation.table)
			continue
		}
		if stage < lastStage {
			t.Errorf("Search cleanup delete order regressed from stage=%d to stage=%d", lastStage, stage)
		}
		lastStage = stage
	}
	documents, postings, fields = searchGenerationPayloadCounts(t, db, generationID)
	if documents != 0 || postings != 0 || fields != 0 {
		t.Errorf("converged cleanup payload documents=%d postings=%d fields=%d, want 0/0/0", documents, postings, fields)
	}
	assertSearchGeneration(t, db, generationID, SearchGenerationSuperseded, false)
	assertSearchGeneration(t, db, otherGenerationID, SearchGenerationComplete, true)
	otherDocuments, otherPostings, otherFields = searchGenerationPayloadCounts(t, db, otherGenerationID)
	if otherDocuments != 1 || otherPostings != 1 || otherFields != 1 {
		t.Errorf("converged unrelated payload documents=%d postings=%d fields=%d, want 1/1/1",
			otherDocuments, otherPostings, otherFields)
	}
}

func searchLifecycleCallbackTable(tx *gorm.DB) string {
	if tx == nil || tx.Statement == nil {
		return ""
	}
	if tx.Statement.Table != "" {
		return tx.Statement.Table
	}
	if tx.Statement.Schema != nil {
		return tx.Statement.Schema.Table
	}
	return ""
}

func searchGenerationPayloadCounts(t *testing.T, db *gorm.DB, generationID string) (int64, int64, int64) {
	t.Helper()
	var documents, postings, fields int64
	if err := db.Model(&model.BackupAssetSearchDocument{}).Where("search_generation_id = ?", generationID).Count(&documents).Error; err != nil {
		t.Fatalf("count Search generation documents: %v", err)
	}
	if err := db.Model(&model.BackupAssetSearchPosting{}).Where("search_generation_id = ?", generationID).Count(&postings).Error; err != nil {
		t.Fatalf("count Search generation postings: %v", err)
	}
	if err := db.Model(&model.BackupAssetSearchDocumentField{}).Where("search_generation_id = ?", generationID).Count(&fields).Error; err != nil {
		t.Fatalf("count Search generation fields: %v", err)
	}
	return documents, postings, fields
}

func TestRecoveryPointSourceLifecycleSearchSeparatesPrepareFromCleanup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/source-lifecycle.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{}, &model.WrappedDomainKey{}, &model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{}, &model.BackupAssetSearchPosting{}, &model.BackupAssetSearchDocumentField{}); err != nil {
		t.Fatalf("migrate source lifecycle tables: %v", err)
	}
	now := time.Date(2026, 8, 17, 14, 27, 0, 0, time.UTC)
	pointID, attemptID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	buildingID, completeID, leaseID := strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 32)
	if err := db.Create(&model.RecoveryPoint{ID: pointID, RepositoryID: strings.Repeat("6", 32)}).Error; err != nil {
		t.Fatalf("seed point: %v", err)
	}
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking)}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}
	if err := db.Create(&model.WrappedDomainKey{ID: strings.Repeat("7", 32), Domain: string(backupasset.KeyDomainSearchToken), Version: 1, State: string(backupasset.DomainKeyActive), ActivatedAt: now}).Error; err != nil {
		t.Fatalf("seed shared Search key: %v", err)
	}
	generations := []model.BackupAssetSearchGeneration{
		{ID: buildingID, RecoveryPointID: pointID, CatalogGenerationID: strings.Repeat("8", 32), Generation: 2, State: string(SearchGenerationBuilding), LeaseID: leaseID, BuildAttemptID: strings.Repeat("9", 32), StartedAt: now},
		{ID: completeID, RecoveryPointID: pointID, CatalogGenerationID: strings.Repeat("8", 32), Generation: 1, State: string(SearchGenerationComplete), IsActive: true, LeaseID: strings.Repeat("a", 32), BuildAttemptID: strings.Repeat("b", 32), StartedAt: now, FinishedAt: &now},
	}
	if err := db.Create(&generations).Error; err != nil {
		t.Fatalf("seed Search generations: %v", err)
	}
	documentID := strings.Repeat("c", 64)
	if err := db.Create(&model.BackupAssetSearchDocument{SearchGenerationID: completeID, DocumentID: documentID, RecoveryPointID: pointID, CatalogGenerationID: strings.Repeat("8", 32), EntryID: strings.Repeat("d", 64)}).Error; err != nil {
		t.Fatalf("seed Search document: %v", err)
	}
	if err := db.Create(&model.BackupAssetSearchPosting{SearchGenerationID: completeID, DocumentID: documentID, Field: "name", TokenKind: "exact"}).Error; err != nil {
		t.Fatalf("seed Search posting: %v", err)
	}
	if err := db.Create(&model.BackupAssetSearchDocumentField{SearchGenerationID: completeID, DocumentID: documentID, Field: "content", State: string(FieldCoverageComplete), UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed Search field: %v", err)
	}
	lease := model.RecoveryPointLease{ID: leaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderSearchIndex), OwnerID: searchBuildOwnerPrefix + pointID, AttemptID: strings.Repeat("9", 32), FenceToken: strings.Repeat("e", 64), Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(time.Hour), AbsoluteDeadline: now.Add(2 * time.Hour), LastHeartbeatAt: now}
	if err := db.Create(&lease).Error; err != nil {
		t.Fatalf("seed Search lease: %v", err)
	}

	owner, err := NewSourceLifecycle(db, &Indexer{db: db}, func() time.Time { return now }, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{RecoveryPointID: pointID, LifecycleAttemptID: attemptID, Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("prepare Search lifecycle: %v", err)
	}
	assertSearchGeneration(t, db, buildingID, SearchGenerationFailed, false)
	assertSearchGeneration(t, db, completeID, SearchGenerationComplete, true)
	assertSearchPayloadCounts(t, db, pointID, 1, 1, 1)
	assertSearchKeyCount(t, db, 1)

	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attemptID).Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
		t.Fatalf("advance lifecycle: %v", err)
	}
	request.Stage = backupasset.SourceLifecycleCleanup
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("cleanup Search lifecycle: %v", err)
	}
	if err := owner.ProveRecoveryPointRevoked(context.Background(), request); err != nil {
		t.Fatalf("prove Search cleanup: %v", err)
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("idempotent Search cleanup: %v", err)
	}
	assertSearchGeneration(t, db, completeID, SearchGenerationSuperseded, false)
	assertSearchPayloadCounts(t, db, pointID, 0, 0, 0)
	assertSearchKeyCount(t, db, 1)
}

func TestRecoveryPointSourceLifecycleSearchCancelsAndJoinsActiveBuilder(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/source-builder.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 15, 2, 0, 0, time.UTC)
	pointID, attemptID := strings.Repeat("e", 32), strings.Repeat("f", 32)
	if err := db.Create(&model.RecoveryPoint{ID: pointID, RepositoryID: strings.Repeat("d", 32)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire),
		Phase: string(backupasset.LifecyclePhaseRevoking),
	}).Error; err != nil {
		t.Fatal(err)
	}
	lease := &indexerLeaseFake{now: now}
	indexer := &Indexer{db: db, lease: lease}
	buildCtx, cancel := context.WithCancel(context.Background())
	fence := backupasset.LeaseFence{
		LeaseID: strings.Repeat("a", 32), RecoveryPointID: pointID,
		HolderType: backupasset.LeaseHolderSearchIndex, OwnerID: searchBuildOwnerPrefix + pointID,
		AttemptID: strings.Repeat("b", 32), FenceToken: strings.Repeat("c", 64),
	}
	if err := indexer.registerActiveBuild(pointID, fence, cancel); err != nil {
		t.Fatalf("register active Search build: %v", err)
	}
	joined := make(chan struct{})
	go func() {
		<-buildCtx.Done()
		indexer.unregisterActiveBuild(pointID, fence)
		close(joined)
	}()
	owner, err := NewSourceLifecycle(db, indexer, func() time.Time { return now }, 16)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
	}); err != nil {
		t.Fatalf("prepare Search source with active builder: %v", err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("Search source lifecycle returned before active builder joined")
	}
	if buildCtx.Err() == nil || indexer.activeBuildExists(pointID) {
		t.Fatal("Search source lifecycle left an active in-process builder")
	}
}

func TestRecoveryPointSourceLifecycleSearchRemovesSupersededGenerationPayloadOnRestart(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/source-restart.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{}, &model.WrappedDomainKey{},
		&model.BackupAssetSearchGeneration{}, &model.BackupAssetSearchDocument{}, &model.BackupAssetSearchPosting{}, &model.BackupAssetSearchDocumentField{},
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 16, 4, 0, 0, time.UTC)
	pointID, otherPointID, attemptID := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
	for _, point := range []model.RecoveryPoint{
		{ID: pointID, RepositoryID: strings.Repeat("4", 32)},
		{ID: otherPointID, RepositoryID: strings.Repeat("4", 32)},
	} {
		if err := db.Create(&point).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire),
		Phase: string(backupasset.LifecyclePhaseCleaning),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.WrappedDomainKey{ID: strings.Repeat("5", 32), Domain: string(backupasset.KeyDomainSearchToken), Version: 1, State: string(backupasset.DomainKeyActive), ActivatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	supersededID, currentID, otherID := strings.Repeat("6", 32), strings.Repeat("7", 32), strings.Repeat("8", 32)
	generations := []model.BackupAssetSearchGeneration{
		{ID: supersededID, RecoveryPointID: pointID, CatalogGenerationID: strings.Repeat("9", 32), Generation: 1, State: string(SearchGenerationSuperseded), StartedAt: now, FinishedAt: &now},
		{ID: currentID, RecoveryPointID: pointID, CatalogGenerationID: strings.Repeat("a", 32), Generation: 2, State: string(SearchGenerationComplete), IsActive: true, StartedAt: now, FinishedAt: &now},
		{ID: otherID, RecoveryPointID: otherPointID, CatalogGenerationID: strings.Repeat("b", 32), Generation: 1, State: string(SearchGenerationComplete), IsActive: true, StartedAt: now, FinishedAt: &now},
	}
	if err := db.Create(&generations).Error; err != nil {
		t.Fatal(err)
	}
	for index, fixture := range []struct {
		generationID string
		pointID      string
		catalogID    string
	}{
		{generationID: supersededID, pointID: pointID, catalogID: strings.Repeat("9", 32)},
		{generationID: currentID, pointID: pointID, catalogID: strings.Repeat("a", 32)},
		{generationID: otherID, pointID: otherPointID, catalogID: strings.Repeat("b", 32)},
	} {
		documentID := strings.Repeat(string(rune('c'+index)), 64)
		if err := db.Create(&model.BackupAssetSearchDocument{SearchGenerationID: fixture.generationID, DocumentID: documentID, RecoveryPointID: fixture.pointID, CatalogGenerationID: fixture.catalogID, EntryID: documentID}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.BackupAssetSearchPosting{SearchGenerationID: fixture.generationID, DocumentID: documentID, Field: "name", TokenKind: "exact"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.BackupAssetSearchDocumentField{SearchGenerationID: fixture.generationID, DocumentID: documentID, Field: "content", State: string(FieldCoverageComplete), UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}

	owner, err := NewSourceLifecycle(db, &Indexer{db: db}, func() time.Time { return now }, 1)
	if err != nil {
		t.Fatal(err)
	}
	request := backupasset.SourceLifecycleRequest{
		RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
		Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecycleCleanup,
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("restart Search cleanup: %v", err)
	}
	if err := owner.ProveRecoveryPointRevoked(context.Background(), request); err != nil {
		t.Fatalf("prove restarted Search cleanup: %v", err)
	}
	if err := owner.RevokeRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("idempotent restarted Search cleanup: %v", err)
	}
	assertSearchGeneration(t, db, supersededID, SearchGenerationSuperseded, false)
	assertSearchGeneration(t, db, currentID, SearchGenerationSuperseded, false)
	assertSearchPayloadCounts(t, db, pointID, 0, 0, 0)
	assertSearchPayloadCounts(t, db, otherPointID, 1, 1, 1)
	assertSearchKeyCount(t, db, 1)
	var generationCount int64
	if err := db.Model(&model.BackupAssetSearchGeneration{}).Where("recovery_point_id = ?", pointID).Count(&generationCount).Error; err != nil || generationCount != 2 {
		t.Fatalf("Search generation evidence count=%d err=%v, want 2", generationCount, err)
	}
}

func assertSearchGeneration(t *testing.T, db *gorm.DB, id string, state SearchGenerationState, active bool) {
	t.Helper()
	var generation model.BackupAssetSearchGeneration
	if err := db.First(&generation, "id = ?", id).Error; err != nil || generation.State != string(state) || generation.IsActive != active {
		t.Fatalf("Search generation id=%q state=%q is_active=%t expected_documents=%d written_documents=%d query_error_present=%t, want state=%s active=%t",
			generation.ID, generation.State, generation.IsActive, generation.ExpectedDocumentCount, generation.WrittenDocumentCount,
			err != nil, state, active)
	}
}

func assertSearchPayloadCounts(t *testing.T, db *gorm.DB, pointID string, documents, postings, fields int64) {
	t.Helper()
	var documentCount, postingCount, fieldCount int64
	db.Model(&model.BackupAssetSearchDocument{}).Where("recovery_point_id = ?", pointID).Count(&documentCount)
	db.Table("backup_asset_search_postings AS postings").Joins("JOIN backup_asset_search_generations AS generations ON generations.id = postings.search_generation_id").Where("generations.recovery_point_id = ?", pointID).Count(&postingCount)
	db.Table("backup_asset_search_document_fields AS fields").Joins("JOIN backup_asset_search_generations AS generations ON generations.id = fields.search_generation_id").Where("generations.recovery_point_id = ?", pointID).Count(&fieldCount)
	if documentCount != documents || postingCount != postings || fieldCount != fields {
		t.Fatalf("Search payload counts documents=%d postings=%d fields=%d, want %d/%d/%d", documentCount, postingCount, fieldCount, documents, postings, fields)
	}
}

func assertSearchKeyCount(t *testing.T, db *gorm.DB, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(&model.WrappedDomainKey{}).Where("domain = ?", backupasset.KeyDomainSearchToken).Count(&count).Error; err != nil || count != want {
		t.Fatalf("Search key count=%d err=%v, want %d", count, err, want)
	}
}
