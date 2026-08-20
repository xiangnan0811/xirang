package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
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

func TestRecoveryPointSourceLifecycleTestDiagnosticsDoNotFormatRecoveryPointLease(t *testing.T) {
	privateTypes := map[string]struct{}{
		"RecoveryPointLease":           {},
		"BackupAssetExportJob":         {},
		"BackupAssetExportKey":         {},
		"BackupAssetExportArtifact":    {},
		"BackupAssetExportItem":        {},
		"BackupAssetExportSourceLease": {},
		"PathError":                    {},
		"exportSourceLifecyclePayload": {},
	}

	type localBinding struct {
		name        string
		privateType string
		format      string
		formatKnown bool
		formatter   string
		start, end  token.Pos
	}
	formatters := map[string]struct{}{
		"Errorf":  {},
		"Fatalf":  {},
		"Logf":    {},
		"Printf":  {},
		"Sprintf": {},
	}
	parseArgumentIndex := func(format string, start int) (int, int, bool, bool) {
		if start >= len(format) || format[start] != '[' {
			return 0, start, false, true
		}
		endOffset := strings.IndexByte(format[start:], ']')
		if endOffset < 0 {
			return 0, start, true, false
		}
		end := start + endOffset
		argument, err := strconv.Atoi(format[start+1 : end])
		if err != nil || argument <= 0 {
			return 0, start, true, false
		}
		return argument, end + 1, true, true
	}
	unsafeFormatArguments := func(format string) (map[int]struct{}, map[int]struct{}, bool, bool) {
		unsafeArguments := make(map[int]struct{})
		consumedArguments := make(map[int]struct{})
		indexedArguments := false
		nextArgument := 1
		for index := 0; index < len(format); {
			if format[index] != '%' {
				index++
				continue
			}
			index++
			if index >= len(format) {
				return nil, nil, false, false
			}
			if format[index] == '%' {
				index++
				continue
			}
			selectedArgument := 0
			hasSelectedArgument := false
			readArgumentIndex := func() bool {
				explicit, next, present, ok := parseArgumentIndex(format, index)
				if !ok || (present && hasSelectedArgument) {
					return false
				}
				if present {
					indexedArguments = true
					selectedArgument = explicit
					hasSelectedArgument = true
					index = next
				}
				return true
			}
			consumeArgument := func(unsafe bool) int {
				argument := nextArgument
				if hasSelectedArgument {
					argument = selectedArgument
				}
				hasSelectedArgument = false
				consumedArguments[argument] = struct{}{}
				if unsafe {
					unsafeArguments[argument] = struct{}{}
				}
				nextArgument = argument + 1
				return argument
			}
			if !readArgumentIndex() {
				return nil, nil, false, false
			}
			for index < len(format) && strings.ContainsRune("+#- 0", rune(format[index])) {
				index++
			}
			if !hasSelectedArgument && !readArgumentIndex() {
				return nil, nil, false, false
			}
			for index < len(format) && format[index] >= '0' && format[index] <= '9' {
				index++
			}
			if index < len(format) && format[index] == '*' {
				consumeArgument(true)
				index++
			}
			if index < len(format) && format[index] == '.' {
				index++
				if !readArgumentIndex() {
					return nil, nil, false, false
				}
				for index < len(format) && format[index] >= '0' && format[index] <= '9' {
					index++
				}
				if index < len(format) && format[index] == '*' {
					consumeArgument(true)
					index++
				}
			}
			if !readArgumentIndex() {
				return nil, nil, false, false
			}
			if index >= len(format) {
				return nil, nil, false, false
			}
			character := format[index]
			if character < 'A' || (character > 'Z' && character < 'a') || character > 'z' {
				return nil, nil, false, false
			}
			verb := format[index]
			index++
			consumeArgument(verb != 'T')
		}
		return unsafeArguments, consumedArguments, indexedArguments, true
	}

	type diagnosticAnalysis struct {
		loaders           map[string]string
		loaderResultTypes map[string][]string
		bindings          map[string]int
		violations        []string
	}
	analyze := func(parsed *ast.File, fileSet *token.FileSet) diagnosticAnalysis {
		privateTypeName := func(expression ast.Expr) string {
			for {
				switch typed := expression.(type) {
				case *ast.ArrayType:
					expression = typed.Elt
				case *ast.StarExpr:
					expression = typed.X
				case *ast.SelectorExpr:
					if _, private := privateTypes[typed.Sel.Name]; private {
						return typed.Sel.Name
					}
					return ""
				case *ast.Ident:
					if _, private := privateTypes[typed.Name]; private {
						return typed.Name
					}
					return ""
				default:
					return ""
				}
			}
		}
		calleeName := func(expression ast.Expr) string {
			for {
				switch typed := expression.(type) {
				case *ast.Ident:
					return typed.Name
				case *ast.IndexExpr:
					expression = typed.X
				case *ast.IndexListExpr:
					expression = typed.X
				case *ast.ParenExpr:
					expression = typed.X
				case *ast.SelectorExpr:
					qualifier, ok := typed.X.(*ast.Ident)
					if !ok {
						return ""
					}
					return qualifier.Name + "." + typed.Sel.Name
				default:
					return ""
				}
			}
		}
		analysis := diagnosticAnalysis{
			loaders:           make(map[string]string),
			loaderResultTypes: make(map[string][]string),
			bindings:          make(map[string]int),
		}
		analysis.loaders["os.ReadFile"] = "PathError"
		analysis.loaderResultTypes["os.ReadFile"] = []string{"", "PathError"}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Type.Results == nil {
				continue
			}
			var resultTypes []string
			for _, result := range function.Type.Results.List {
				privateType := privateTypeName(result.Type)
				resultCount := len(result.Names)
				if resultCount == 0 {
					resultCount = 1
				}
				for index := 0; index < resultCount; index++ {
					resultTypes = append(resultTypes, privateType)
				}
				if privateType != "" && analysis.loaders[function.Name.Name] == "" {
					analysis.loaders[function.Name.Name] = privateType
				}
			}
			analysis.loaderResultTypes[function.Name.Name] = resultTypes
		}
		identityParameters := make(map[string]int)
		for changed := true; changed; {
			changed = false
			for _, declaration := range parsed.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil || function.Type.Params == nil || len(function.Body.List) != 1 {
					continue
				}
				if _, summarized := identityParameters[function.Name.Name]; summarized {
					continue
				}
				returned, ok := function.Body.List[0].(*ast.ReturnStmt)
				if !ok || len(returned.Results) != 1 {
					continue
				}
				parameterIndexes := make(map[string]int)
				parameterIndex := 0
				for _, parameter := range function.Type.Params.List {
					for _, name := range parameter.Names {
						parameterIndexes[name.Name] = parameterIndex
						parameterIndex++
					}
				}
				var sourceParameter func(ast.Expr) (int, bool)
				sourceParameter = func(expression ast.Expr) (int, bool) {
					switch typed := expression.(type) {
					case *ast.Ident:
						index, found := parameterIndexes[typed.Name]
						return index, found
					case *ast.ParenExpr:
						return sourceParameter(typed.X)
					case *ast.CallExpr:
						index, identity := identityParameters[calleeName(typed.Fun)]
						if !identity || index >= len(typed.Args) {
							return 0, false
						}
						return sourceParameter(typed.Args[index])
					default:
						return 0, false
					}
				}
				if index, found := sourceParameter(returned.Results[0]); found {
					identityParameters[function.Name.Name] = index
					changed = true
				}
			}
		}
		resultParameterSources := make(map[string][][]int)
		for changed := true; changed; {
			changed = false
			for _, declaration := range parsed.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil || function.Type.Params == nil {
					continue
				}
				resultCount := len(analysis.loaderResultTypes[function.Name.Name])
				if resultCount == 0 {
					continue
				}
				if _, summarized := resultParameterSources[function.Name.Name]; summarized {
					continue
				}
				parameterIndexes := make(map[string]int)
				parameterIndex := 0
				for _, parameter := range function.Type.Params.List {
					for _, name := range parameter.Names {
						parameterIndexes[name.Name] = parameterIndex
						parameterIndex++
					}
				}
				unionSources := func(destination map[int]struct{}, sources map[int]struct{}) {
					for source := range sources {
						destination[source] = struct{}{}
					}
				}
				var sourceParameters func(ast.Expr) (map[int]struct{}, bool)
				sourceParameters = func(expression ast.Expr) (map[int]struct{}, bool) {
					sources := make(map[int]struct{})
					switch typed := expression.(type) {
					case *ast.Ident:
						if index, found := parameterIndexes[typed.Name]; found {
							sources[index] = struct{}{}
						}
						return sources, true
					case *ast.ParenExpr:
						return sourceParameters(typed.X)
					case *ast.SelectorExpr:
						return sourceParameters(typed.X)
					case *ast.IndexExpr:
						return sourceParameters(typed.X)
					case *ast.IndexListExpr:
						return sourceParameters(typed.X)
					case *ast.StarExpr:
						return sourceParameters(typed.X)
					case *ast.UnaryExpr:
						return sourceParameters(typed.X)
					case *ast.SliceExpr:
						return sourceParameters(typed.X)
					case *ast.TypeAssertExpr:
						return sourceParameters(typed.X)
					case *ast.BinaryExpr:
						left, leftResolved := sourceParameters(typed.X)
						right, rightResolved := sourceParameters(typed.Y)
						if !leftResolved || !rightResolved {
							return nil, false
						}
						unionSources(sources, left)
						unionSources(sources, right)
						return sources, true
					case *ast.KeyValueExpr:
						key, keyResolved := sourceParameters(typed.Key)
						value, valueResolved := sourceParameters(typed.Value)
						if !keyResolved || !valueResolved {
							return nil, false
						}
						unionSources(sources, key)
						unionSources(sources, value)
						return sources, true
					case *ast.CompositeLit:
						for _, element := range typed.Elts {
							elementSources, resolved := sourceParameters(element)
							if !resolved {
								return nil, false
							}
							unionSources(sources, elementSources)
						}
						return sources, true
					case *ast.CallExpr:
						name := calleeName(typed.Fun)
						if index, identity := identityParameters[name]; identity {
							if index >= len(typed.Args) {
								return nil, false
							}
							return sourceParameters(typed.Args[index])
						}
						if name == "any" && len(typed.Args) == 1 {
							return sourceParameters(typed.Args[0])
						}
						if _, local := analysis.loaderResultTypes[name]; local {
							calleeSources, summarized := resultParameterSources[name]
							if !summarized || len(calleeSources) != 1 {
								return nil, false
							}
							for _, source := range calleeSources[0] {
								if source >= len(typed.Args) {
									return nil, false
								}
								argumentSources, resolved := sourceParameters(typed.Args[source])
								if !resolved {
									return nil, false
								}
								unionSources(sources, argumentSources)
							}
							return sources, true
						}
						return sources, true
					default:
						return sources, true
					}
				}
				resultSources := make([]map[int]struct{}, resultCount)
				for index := range resultSources {
					resultSources[index] = make(map[int]struct{})
				}
				var returns []*ast.ReturnStmt
				ast.Inspect(function.Body, func(node ast.Node) bool {
					if literal, nested := node.(*ast.FuncLit); nested && literal.Body != function.Body {
						return false
					}
					if returned, ok := node.(*ast.ReturnStmt); ok {
						returns = append(returns, returned)
						return false
					}
					return true
				})
				if len(returns) == 0 {
					continue
				}
				resolved := true
				for _, returned := range returns {
					if len(returned.Results) == resultCount {
						for index, result := range returned.Results {
							sources, resultResolved := sourceParameters(result)
							if !resultResolved {
								resolved = false
								break
							}
							unionSources(resultSources[index], sources)
						}
					} else if len(returned.Results) == 1 && resultCount > 1 {
						call, callOK := returned.Results[0].(*ast.CallExpr)
						if !callOK {
							resolved = false
						} else {
							calleeSources, summarized := resultParameterSources[calleeName(call.Fun)]
							if !summarized || len(calleeSources) != resultCount {
								resolved = false
								break
							}
							for index, calleeResultSources := range calleeSources {
								for _, calleeSource := range calleeResultSources {
									if calleeSource >= len(call.Args) {
										resolved = false
										break
									}
									argumentSources, resultResolved := sourceParameters(call.Args[calleeSource])
									if !resultResolved {
										resolved = false
										break
									}
									unionSources(resultSources[index], argumentSources)
								}
								if !resolved {
									break
								}
							}
						}
					} else {
						resolved = false
					}
					if !resolved {
						break
					}
				}
				if resolved {
					sources := make([][]int, resultCount)
					for resultIndex, sourceSet := range resultSources {
						for sourceIndex := 0; sourceIndex < parameterIndex; sourceIndex++ {
							if _, found := sourceSet[sourceIndex]; found {
								sources[resultIndex] = append(sources[resultIndex], sourceIndex)
							}
						}
					}
					resultParameterSources[function.Name.Name] = sources
					changed = true
				}
			}
		}
		type formatterSinkSummary struct {
			formatterParameter int
			valueParameters    []int
		}
		formatterSinkSummaries := make(map[string]formatterSinkSummary)
		for changed := true; changed; {
			changed = false
			for _, declaration := range parsed.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Body == nil || function.Type.Params == nil || len(function.Body.List) != 1 {
					continue
				}
				if _, summarized := formatterSinkSummaries[function.Name.Name]; summarized {
					continue
				}
				expressionStatement, ok := function.Body.List[0].(*ast.ExprStmt)
				if !ok {
					continue
				}
				call, ok := expressionStatement.X.(*ast.CallExpr)
				if !ok {
					continue
				}
				parameterIndexes := make(map[string]int)
				parameterIndex := 0
				for _, parameter := range function.Type.Params.List {
					for _, name := range parameter.Names {
						parameterIndexes[name.Name] = parameterIndex
						parameterIndex++
					}
				}
				var sourceParameter func(ast.Expr) (int, bool)
				sourceParameter = func(expression ast.Expr) (int, bool) {
					switch typed := expression.(type) {
					case *ast.Ident:
						index, found := parameterIndexes[typed.Name]
						return index, found
					case *ast.ParenExpr:
						return sourceParameter(typed.X)
					case *ast.CallExpr:
						index, identity := identityParameters[calleeName(typed.Fun)]
						if !identity || index >= len(typed.Args) {
							return 0, false
						}
						return sourceParameter(typed.Args[index])
					default:
						return 0, false
					}
				}
				if calleeSummary, relay := formatterSinkSummaries[calleeName(call.Fun)]; relay {
					if calleeSummary.formatterParameter >= len(call.Args) {
						continue
					}
					formatterParameter, found := sourceParameter(call.Args[calleeSummary.formatterParameter])
					if !found {
						continue
					}
					valueParameters := make([]int, 0, len(calleeSummary.valueParameters))
					valid := true
					for _, calleeParameter := range calleeSummary.valueParameters {
						if calleeParameter >= len(call.Args) {
							valid = false
							break
						}
						valueParameter, found := sourceParameter(call.Args[calleeParameter])
						if !found {
							valid = false
							break
						}
						valueParameters = append(valueParameters, valueParameter)
					}
					if valid {
						formatterSinkSummaries[function.Name.Name] = formatterSinkSummary{
							formatterParameter: formatterParameter, valueParameters: valueParameters,
						}
						changed = true
					}
					continue
				}
				formatterParameter, directFormatter := sourceParameter(call.Fun)
				if !directFormatter || len(call.Args) < 2 {
					continue
				}
				literal, ok := call.Args[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				format, err := strconv.Unquote(literal.Value)
				if err != nil {
					continue
				}
				unsafeArguments, _, _, parsedFormat := unsafeFormatArguments(format)
				if !parsedFormat {
					continue
				}
				var valueParameters []int
				for argumentIndex, argument := range call.Args[1:] {
					if _, unsafe := unsafeArguments[argumentIndex+1]; !unsafe {
						continue
					}
					if valueParameter, found := sourceParameter(argument); found {
						valueParameters = append(valueParameters, valueParameter)
					}
				}
				if len(valueParameters) != 0 {
					formatterSinkSummaries[function.Name.Name] = formatterSinkSummary{
						formatterParameter: formatterParameter, valueParameters: valueParameters,
					}
					changed = true
				}
			}
		}

		parents := make(map[ast.Node]ast.Node)
		var nodeStack []ast.Node
		ast.Inspect(parsed, func(node ast.Node) bool {
			if node == nil {
				nodeStack = nodeStack[:len(nodeStack)-1]
				return true
			}
			if len(nodeStack) != 0 {
				parents[node] = nodeStack[len(nodeStack)-1]
			}
			nodeStack = append(nodeStack, node)
			return true
		})
		scopeEnd := func(node ast.Node) token.Pos {
			child := node
			for parent := parents[child]; parent != nil; parent = parents[parent] {
				switch typed := parent.(type) {
				case *ast.IfStmt:
					if typed.Init == child {
						return typed.End()
					}
				case *ast.ForStmt:
					if typed.Init == child {
						return typed.End()
					}
				case *ast.SwitchStmt:
					if typed.Init == child {
						return typed.End()
					}
				case *ast.TypeSwitchStmt:
					if typed.Init == child {
						return typed.End()
					}
				case *ast.BlockStmt:
					return typed.End()
				}
				child = parent
			}
			return token.NoPos
		}
		assignmentMayNotExecute := func(node ast.Node) bool {
			child := node
			for parent := parents[child]; parent != nil; parent = parents[parent] {
				switch typed := parent.(type) {
				case *ast.FuncDecl, *ast.FuncLit:
					return false
				case *ast.IfStmt:
					if typed.Init != child {
						return true
					}
				case *ast.ForStmt:
					if typed.Init != child {
						return true
					}
				case *ast.RangeStmt, *ast.CaseClause, *ast.CommClause, *ast.SelectStmt:
					return true
				case *ast.SwitchStmt:
					if typed.Init != child {
						return true
					}
				case *ast.TypeSwitchStmt:
					if typed.Init != child && typed.Assign != child {
						return true
					}
				}
				child = parent
			}
			return false
		}

		var bindings []localBinding
		bindingAt := func(name string, position token.Pos) *localBinding {
			var selected *localBinding
			for index := range bindings {
				binding := &bindings[index]
				if binding.name != name || position <= binding.start || position >= binding.end {
					continue
				}
				if selected == nil || binding.start > selected.start ||
					(binding.start == selected.start && binding.end < selected.end) {
					selected = binding
				}
			}
			return selected
		}
		privateValueType := func(name string, position token.Pos) string {
			if binding := bindingAt(name, position); binding != nil {
				return binding.privateType
			}
			return ""
		}
		formatValue := func(name string, position token.Pos) string {
			if binding := bindingAt(name, position); binding != nil {
				return binding.format
			}
			return ""
		}
		formatKnown := func(name string, position token.Pos) bool {
			if binding := bindingAt(name, position); binding != nil {
				return binding.formatKnown
			}
			return false
		}
		formatterValue := func(name string, position token.Pos) string {
			if binding := bindingAt(name, position); binding != nil {
				return binding.formatter
			}
			return ""
		}
		safePrivateField := func(name string) bool {
			switch name {
			case "ID", "State", "Status", "ExecutionState", "CleanupState", "ErrorCategory":
				return true
			default:
				return strings.HasSuffix(name, "Revision") || strings.HasSuffix(name, "Count")
			}
		}
		valueConversions := map[string]struct{}{
			"any": {}, "bool": {}, "byte": {}, "complex64": {}, "complex128": {},
			"float32": {}, "float64": {}, "int": {}, "int8": {}, "int16": {}, "int32": {},
			"int64": {}, "rune": {}, "string": {}, "uint": {}, "uint8": {}, "uint16": {},
			"uint32": {}, "uint64": {}, "uintptr": {},
		}
		var expressionPrivateType func(ast.Expr) string
		expressionPrivateType = func(expression ast.Expr) string {
			switch typed := expression.(type) {
			case *ast.Ident:
				return privateValueType(typed.Name, typed.Pos())
			case *ast.SelectorExpr:
				if safePrivateField(typed.Sel.Name) {
					return ""
				}
				return expressionPrivateType(typed.X)
			case *ast.IndexExpr:
				return expressionPrivateType(typed.X)
			case *ast.IndexListExpr:
				return expressionPrivateType(typed.X)
			case *ast.ParenExpr:
				return expressionPrivateType(typed.X)
			case *ast.StarExpr:
				return expressionPrivateType(typed.X)
			case *ast.UnaryExpr:
				return expressionPrivateType(typed.X)
			case *ast.SliceExpr:
				return expressionPrivateType(typed.X)
			case *ast.TypeAssertExpr:
				return expressionPrivateType(typed.X)
			case *ast.BinaryExpr:
				switch typed.Op {
				case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ, token.LAND, token.LOR:
					return ""
				}
				if privateType := expressionPrivateType(typed.X); privateType != "" {
					return privateType
				}
				return expressionPrivateType(typed.Y)
			case *ast.KeyValueExpr:
				if privateType := expressionPrivateType(typed.Key); privateType != "" {
					return privateType
				}
				return expressionPrivateType(typed.Value)
			case *ast.CompositeLit:
				if privateType := privateTypeName(typed.Type); privateType != "" {
					return privateType
				}
				for _, element := range typed.Elts {
					if privateType := expressionPrivateType(element); privateType != "" {
						return privateType
					}
				}
				return ""
			case *ast.CallExpr:
				name := calleeName(typed.Fun)
				if name != "" {
					if resultTypes := analysis.loaderResultTypes[name]; len(resultTypes) == 1 && resultTypes[0] != "" {
						return resultTypes[0]
					}
					if sources := resultParameterSources[name]; len(sources) == 1 {
						for _, source := range sources[0] {
							if source < len(typed.Args) {
								if privateType := expressionPrivateType(typed.Args[source]); privateType != "" {
									return privateType
								}
							}
						}
					}
					if parameterIndex, identity := identityParameters[name]; identity && parameterIndex < len(typed.Args) {
						return expressionPrivateType(typed.Args[parameterIndex])
					}
					if _, conversion := valueConversions[name]; conversion && len(typed.Args) == 1 {
						return expressionPrivateType(typed.Args[0])
					}
				}
				if privateType := privateTypeName(typed.Fun); privateType != "" {
					return privateType
				}
				if _, conversion := typed.Fun.(*ast.ArrayType); conversion && len(typed.Args) == 1 {
					return expressionPrivateType(typed.Args[0])
				}
				return ""
			default:
				return ""
			}
		}
		var expressionFormat func(ast.Expr) (string, bool)
		expressionFormat = func(expression ast.Expr) (string, bool) {
			switch typed := expression.(type) {
			case *ast.BasicLit:
				if typed.Kind != token.STRING {
					return "", false
				}
				format, err := strconv.Unquote(typed.Value)
				return format, err == nil
			case *ast.Ident:
				return formatValue(typed.Name, typed.Pos()), formatKnown(typed.Name, typed.Pos())
			case *ast.ParenExpr:
				return expressionFormat(typed.X)
			default:
				return "", false
			}
		}
		var expressionFormatter func(ast.Expr) string
		expressionFormatter = func(expression ast.Expr) string {
			switch typed := expression.(type) {
			case *ast.Ident:
				return formatterValue(typed.Name, typed.Pos())
			case *ast.SelectorExpr:
				if _, guarded := formatters[typed.Sel.Name]; guarded {
					return typed.Sel.Name
				}
				return ""
			case *ast.ParenExpr:
				return expressionFormatter(typed.X)
			case *ast.CallExpr:
				parameterIndex, identity := identityParameters[calleeName(typed.Fun)]
				if !identity || parameterIndex >= len(typed.Args) {
					return ""
				}
				return expressionFormatter(typed.Args[parameterIndex])
			default:
				return ""
			}
		}
		assignmentPrivateTypes := func(values []ast.Expr, targetCount int) []string {
			privateTypes := make([]string, targetCount)
			if len(values) == 1 && targetCount > 1 {
				call, ok := values[0].(*ast.CallExpr)
				if ok {
					name := calleeName(call.Fun)
					if resultTypes := analysis.loaderResultTypes[name]; len(resultTypes) == targetCount {
						copy(privateTypes, resultTypes)
						if sources := resultParameterSources[name]; len(sources) == targetCount {
							for index, resultSources := range sources {
								if privateTypes[index] != "" {
									continue
								}
								for _, source := range resultSources {
									if source < len(call.Args) {
										if privateType := expressionPrivateType(call.Args[source]); privateType != "" {
											privateTypes[index] = privateType
											break
										}
									}
								}
							}
						}
						return privateTypes
					}
				}
			}
			for index := range privateTypes {
				if index < len(values) {
					privateTypes[index] = expressionPrivateType(values[index])
				}
			}
			return privateTypes
		}
		rangePrivateTypes := func(expression ast.Expr) (string, string) {
			literal, literalOK := expression.(*ast.CompositeLit)
			if literalOK {
				switch typed := literal.Type.(type) {
				case *ast.ArrayType:
					valuePrivateType := privateTypeName(typed.Elt)
					for _, element := range literal.Elts {
						if valuePrivateType == "" {
							valuePrivateType = expressionPrivateType(element)
						}
					}
					return "", valuePrivateType
				case *ast.MapType:
					keyPrivateType := privateTypeName(typed.Key)
					valuePrivateType := privateTypeName(typed.Value)
					for _, element := range literal.Elts {
						keyValue, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if keyPrivateType == "" {
							keyPrivateType = expressionPrivateType(keyValue.Key)
						}
						if valuePrivateType == "" {
							valuePrivateType = expressionPrivateType(keyValue.Value)
						}
					}
					return keyPrivateType, valuePrivateType
				}
			}
			privateType := expressionPrivateType(expression)
			return privateType, privateType
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || function.Type.Params == nil {
				continue
			}
			for _, parameter := range function.Type.Params.List {
				privateType := privateTypeName(parameter.Type)
				for _, name := range parameter.Names {
					bindings = append(bindings, localBinding{
						name: name.Name, privateType: privateType, start: function.Body.Pos(), end: function.Body.End(),
					})
				}
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.ValueSpec:
				end := scopeEnd(typed)
				if end == token.NoPos {
					return true
				}
				explicitPrivateType := privateTypeName(typed.Type)
				inferredPrivateTypes := assignmentPrivateTypes(typed.Values, len(typed.Names))
				for index, name := range typed.Names {
					privateType := explicitPrivateType
					format := ""
					knownFormat := false
					formatter := ""
					if inferredPrivateTypes[index] != "" {
						privateType = inferredPrivateTypes[index]
					}
					if index < len(typed.Values) {
						format, knownFormat = expressionFormat(typed.Values[index])
						formatter = expressionFormatter(typed.Values[index])
					}
					bindings = append(bindings, localBinding{
						name: name.Name, privateType: privateType, format: format, formatKnown: knownFormat,
						formatter: formatter, start: typed.End(), end: end,
					})
				}
			case *ast.AssignStmt:
				if typed.Tok != token.DEFINE && typed.Tok != token.ASSIGN {
					return true
				}
				end := scopeEnd(typed)
				inferredPrivateTypes := assignmentPrivateTypes(typed.Rhs, len(typed.Lhs))
				for index, left := range typed.Lhs {
					name, ok := left.(*ast.Ident)
					if !ok {
						continue
					}
					bindingEnd := end
					var existing *localBinding
					if typed.Tok == token.ASSIGN {
						existing = bindingAt(name.Name, typed.Pos())
						if existing != nil {
							bindingEnd = existing.end
						}
					}
					privateType := inferredPrivateTypes[index]
					format := ""
					knownFormat := false
					formatter := ""
					if index < len(typed.Rhs) {
						format, knownFormat = expressionFormat(typed.Rhs[index])
						formatter = expressionFormatter(typed.Rhs[index])
					}
					if existing != nil && assignmentMayNotExecute(typed) {
						if existing.privateType != "" {
							privateType = existing.privateType
						}
						if !existing.formatKnown || !knownFormat || existing.format != format {
							format = ""
							knownFormat = false
						}
						if existing.formatter != "" {
							formatter = existing.formatter
						}
					}
					bindings = append(bindings, localBinding{
						name: name.Name, privateType: privateType, format: format, formatKnown: knownFormat,
						formatter: formatter, start: typed.End(), end: bindingEnd,
					})
				}
			case *ast.RangeStmt:
				if typed.Tok != token.DEFINE && typed.Tok != token.ASSIGN {
					return true
				}
				keyPrivateType, valuePrivateType := rangePrivateTypes(typed.X)
				appendRangeBinding := func(expression ast.Expr, privateType string) {
					name, ok := expression.(*ast.Ident)
					if !ok || name.Name == "_" {
						return
					}
					bindingEnd := typed.Body.End()
					if typed.Tok == token.ASSIGN {
						if existing := bindingAt(name.Name, typed.Pos()); existing != nil {
							bindingEnd = existing.end
							if existing.privateType != "" {
								privateType = existing.privateType
							}
						}
					}
					bindings = append(bindings, localBinding{
						name: name.Name, privateType: privateType, start: typed.Body.Pos(), end: bindingEnd,
					})
				}
				if typed.Key != nil {
					appendRangeBinding(typed.Key, keyPrivateType)
				}
				if typed.Value != nil {
					appendRangeBinding(typed.Value, valuePrivateType)
				}
			}
			return true
		})
		for _, binding := range bindings {
			if binding.privateType != "" {
				analysis.bindings[binding.privateType]++
			}
		}

		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			if sink, summarized := formatterSinkSummaries[calleeName(call.Fun)]; summarized {
				if sink.formatterParameter >= len(call.Args) {
					return true
				}
				formatter := expressionFormatter(call.Args[sink.formatterParameter])
				if formatter == "" {
					return true
				}
				for _, valueParameter := range sink.valueParameters {
					if valueParameter >= len(call.Args) {
						continue
					}
					if privateType := expressionPrivateType(call.Args[valueParameter]); privateType != "" {
						analysis.violations = append(analysis.violations, fmt.Sprintf(
							"%d:%s:%s", fileSet.Position(call.Pos()).Line, formatter, privateType,
						))
					}
				}
				return true
			}
			formatter := expressionFormatter(call.Fun)
			if formatter == "" {
				return true
			}
			format, knownFormat := expressionFormat(call.Args[0])
			unsafeArguments, consumedArguments, indexedArguments, parsedFormat := unsafeFormatArguments(format)
			for index, argument := range call.Args[1:] {
				if privateType := expressionPrivateType(argument); privateType != "" {
					_, unsafeArgument := unsafeArguments[index+1]
					_, consumedArgument := consumedArguments[index+1]
					if knownFormat && parsedFormat && !unsafeArgument && (consumedArgument || indexedArguments) {
						continue
					}
					analysis.violations = append(analysis.violations, fmt.Sprintf(
						"%d:%s:%s", fileSet.Position(call.Pos()).Line, formatter, privateType,
					))
				}
			}
			return true
		})
		return analysis
	}

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Export source lifecycle test artifact")
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, filename, nil, 0)
	if err != nil {
		t.Fatalf("parse Export source lifecycle test artifact: %v", err)
	}
	actual := analyze(parsed, fileSet)
	requiredLoaders := map[string]string{
		"loadExportSourceLifecycleJob":         "BackupAssetExportJob",
		"loadExportSourceLifecyclePayload":     "exportSourceLifecyclePayload",
		"loadExportSourceLifecycleSourceLease": "BackupAssetExportSourceLease",
		"loadExportSourceLifecyclePointLease":  "RecoveryPointLease",
	}
	for loader, wantType := range requiredLoaders {
		if gotType := actual.loaders[loader]; gotType != wantType {
			t.Fatalf("Export private diagnostic guard inferred loader %s as %q, want %q", loader, gotType, wantType)
		}
	}
	for privateType := range privateTypes {
		if actual.bindings[privateType] == 0 {
			t.Fatalf("Export private diagnostic guard inferred no %s binding", privateType)
		}
	}
	if len(actual.violations) != 0 {
		t.Errorf("unsafe full Export private-authority/payload diagnostics at %v; use explicit ID/state/revision/count/closed fields", actual.violations)
	}

	const mutationSource = `package export

func loadExportSourceLifecycleSourceLease() BackupAssetExportSourceLease { return BackupAssetExportSourceLease{} }
func privateIdentity(value any) any { return value }
func makeFormat() string { return "%v" }

func canary(t *testing.T, payload exportSourceLifecyclePayload, sources []BackupAssetExportSourceLease) {
	alias := sources[0]
	format := "%v"
	formatAlias := format
	t.Errorf("unsafe private alias %v", alias)
	t.Fatalf("unsafe private selector %+v", payload.key)
	t.Logf("unsafe private index %#v", sources[0])
	fmt.Printf("unsafe private loader call %v", loadExportSourceLifecycleSourceLease())
	_ = fmt.Sprintf("unsafe private alias %v", alias)
	t.Errorf(formatAlias, privateIdentity(alias))
	_ = fmt.Sprintf("unsafe explicit index %[1]v", payload.key)
	t.Errorf(makeFormat(), alias)
	reassignedFormat := "%s"
	reassignedFormat = "%#v"
	t.Errorf(reassignedFormat, alias)
	emit := t.Errorf
	emit("%v", alias)
	emit = t.Logf
	emit("%+v", sources[0])
	t.Errorf("unsafe private string %s", alias)
	t.Errorf("unsafe private quote %q", payload.key)
	t.Errorf("unsafe private hex %x", sources[0])
	t.Errorf("unsafe private binary %s", alias.FenceHash+"")
	t.Errorf("unsafe private composite %v", []any{alias}[0])
	t.Errorf("unsafe private conversion %v", any(alias))
	t.Errorf("unsafe private generic %v", valueIdentity[BackupAssetExportSourceLease](alias))
	t.Errorf("safe id=%s state=%s revision=%d count=%d type=%T literal=%%",
		alias.ID, alias.State, payload.key.KeyRevision, len(sources), alias)
	secondLabel, assignedSecond := loadSecondPrivate()
	_ = secondLabel
	t.Errorf("unsafe assigned second %v", assignedSecond)
	var declaredLabel, declaredCount, declaredThird = loadThirdPrivate()
	t.Errorf("safe declared label=%s count=%d", declaredLabel, declaredCount)
	t.Errorf("unsafe declared third %v", declaredThird)
	t.Errorf("safe private width type %*T", 4, alias)
	t.Errorf("safe private precision type %.*T", 2, alias)
	t.Errorf("unsafe private star value %*s", 4, alias)
	t.Errorf("unsafe private precision value %.*q", 2, alias)
	t.Errorf("unsafe private star width %*T", alias, alias)
	t.Errorf("safe indexed private width type %[2]*[1]T", alias, 4)
	var outside any
	if true {
		_, outside = loadSecondPrivate()
	}
	t.Errorf("unsafe outer assignment %v", outside)
	twoLayerEmit := formatterPass2(t.Errorf)
	twoLayerEmit("%v", valuePass2(alias))
	t.Errorf("unsafe private map key %v", map[any]string{alias: "safe"})
	privateOuter := any(alias)
	if false {
		privateOuter = any("safe")
	}
	t.Errorf("unsafe conditional sanitizer %v", privateOuter)
	sinkRelay2(t.Errorf, alias)
	_, pairKey := pairKeyRelay2(alias)
	t.Errorf("unsafe pair map key relay %v", pairKey)
	_, pairValue := pairValueRelay2(alias)
	t.Errorf("unsafe pair map value relay %v", pairValue)
	t.Errorf("safe unused indexed private %[2]T", alias, 4)
	extraStatement := extraStatementPrivate(alias)
	t.Errorf("unsafe extra statement helper %v", extraStatement)
	branched := branchedPrivate(false, alias)
	t.Errorf("unsafe multi-return branch %v", branched)
	joined := joinRelay2("safe", alias)
	t.Errorf("unsafe multi-source relay %v", joined)
	safeFirst, joinedPrivate := safeFirstAndJoined("safe", alias)
	t.Errorf("safe first result=%d", safeFirst)
	t.Errorf("unsafe second multi-source result %v", joinedPrivate)
	ordinaryErr, safeZero := ordinaryErrorAndSafeZero()
	t.Errorf("safe ordinary error_present=%t safe_zero=%d", ordinaryErr != nil, safeZero)
	for rangeIndex, rangeValue := range []any{alias} {
		t.Errorf("safe range index=%d", rangeIndex)
		t.Errorf("unsafe range slice value %v", rangeValue)
	}
	for rangeKey, rangeValue := range map[any]any{alias: alias} {
		t.Errorf("unsafe range map key %v", rangeKey)
		t.Errorf("unsafe range map value %v", rangeValue)
	}
	conditionalFormat := makeFormat()
	if false {
		conditionalFormat = "%T"
	}
	t.Errorf(conditionalFormat, alias)
	nonFormatter := func(string, ...any) {}
	conditionalEmit := t.Errorf
	if false {
		conditionalEmit = nonFormatter
	}
	conditionalEmit("%v", alias)
	safeConditionalFormat := "%T"
	if false {
		safeConditionalFormat = "%T"
	}
	t.Errorf(safeConditionalFormat, alias)
	safeConditionalEmit := t.Errorf
	if false {
		safeConditionalEmit = nonFormatter
	}
	safeConditionalEmit("%T", alias)
}

func valueIdentity[T any](value T) T { return value }
func loadSecondPrivate() (string, BackupAssetExportSourceLease) { return "", BackupAssetExportSourceLease{} }
func loadThirdPrivate() (string, int, BackupAssetExportSourceLease) { return "", 0, BackupAssetExportSourceLease{} }
func valuePass1(value any) any { return privateIdentity(value) }
func valuePass2(value any) any { return valuePass1(value) }
func formatterPass1(formatter func(string, ...any)) func(string, ...any) { return formatter }
func formatterPass2(formatter func(string, ...any)) func(string, ...any) { return formatterPass1(formatter) }
func sinkLeaf[T any](formatter func(string, ...any), value T) { formatter("%v", value) }
func sinkRelay1[T any](formatter func(string, ...any), value T) { sinkLeaf(formatter, value) }
func sinkRelay2[T any](formatter func(string, ...any), value T) { sinkRelay1(formatter, value) }
func pairKeyLeaf(value any) (int, any) { return 1, map[any]string{value: "safe"} }
func pairKeyRelay1(value any) (int, any) { return pairKeyLeaf(value) }
func pairKeyRelay2(value any) (int, any) { return pairKeyRelay1(value) }
func pairValueLeaf(value any) (int, any) { return 1, map[string]any{"safe": value} }
func pairValueRelay1(value any) (int, any) { return pairValueLeaf(value) }
func pairValueRelay2(value any) (int, any) { return pairValueRelay1(value) }
func extraStatementPrivate(value any) any {
	marker := 1
	_ = marker
	return value
}
func branchedPrivate(private bool, value any) any {
	if private {
		return value
	}
	return "safe"
}
func joinValues(a, b any) any { return []any{a, b} }
func joinRelay1(a, b any) any { return joinValues(a, b) }
func joinRelay2(a, b any) any { return joinRelay1(a, b) }
func safeFirstAndJoined(a, b any) (int, any) { return 0, joinRelay2(a, b) }
func ordinaryErrorAndSafeZero() (error, int) { return nil, 0 }
`
	mutationFileSet := token.NewFileSet()
	mutation, err := parser.ParseFile(mutationFileSet, "export_source_lifecycle_privacy_canary.go", mutationSource, 0)
	if err != nil {
		t.Fatalf("parse Export private diagnostic mutation canary: %v", err)
	}
	wantMutationViolations := []string{
		"11:Errorf:BackupAssetExportSourceLease",
		"12:Fatalf:exportSourceLifecyclePayload",
		"13:Logf:BackupAssetExportSourceLease",
		"14:Printf:BackupAssetExportSourceLease",
		"15:Sprintf:BackupAssetExportSourceLease",
		"16:Errorf:BackupAssetExportSourceLease",
		"17:Sprintf:exportSourceLifecyclePayload",
		"18:Errorf:BackupAssetExportSourceLease",
		"21:Errorf:BackupAssetExportSourceLease",
		"23:Errorf:BackupAssetExportSourceLease",
		"25:Logf:BackupAssetExportSourceLease",
		"26:Errorf:BackupAssetExportSourceLease",
		"27:Errorf:exportSourceLifecyclePayload",
		"28:Errorf:BackupAssetExportSourceLease",
		"29:Errorf:BackupAssetExportSourceLease",
		"30:Errorf:BackupAssetExportSourceLease",
		"31:Errorf:BackupAssetExportSourceLease",
		"32:Errorf:BackupAssetExportSourceLease",
		"37:Errorf:BackupAssetExportSourceLease",
		"40:Errorf:BackupAssetExportSourceLease",
		"43:Errorf:BackupAssetExportSourceLease",
		"44:Errorf:BackupAssetExportSourceLease",
		"45:Errorf:BackupAssetExportSourceLease",
		"51:Errorf:BackupAssetExportSourceLease",
		"53:Errorf:BackupAssetExportSourceLease",
		"54:Errorf:BackupAssetExportSourceLease",
		"59:Errorf:BackupAssetExportSourceLease",
		"60:Errorf:BackupAssetExportSourceLease",
		"62:Errorf:BackupAssetExportSourceLease",
		"64:Errorf:BackupAssetExportSourceLease",
		"67:Errorf:BackupAssetExportSourceLease",
		"69:Errorf:BackupAssetExportSourceLease",
		"71:Errorf:BackupAssetExportSourceLease",
		"74:Errorf:BackupAssetExportSourceLease",
		"79:Errorf:BackupAssetExportSourceLease",
		"82:Errorf:BackupAssetExportSourceLease",
		"83:Errorf:BackupAssetExportSourceLease",
		"89:Errorf:BackupAssetExportSourceLease",
		"95:Errorf:BackupAssetExportSourceLease",
	}
	if got := analyze(mutation, mutationFileSet).violations; !reflect.DeepEqual(got, wantMutationViolations) {
		t.Errorf("Export private diagnostic mutation violations=%v want=%v", got, wantMutationViolations)
	}
}

func TestRecoveryPointSourceLifecycleExportRealPortPreparesQueuedRunningSealingReady(t *testing.T) {
	for _, state := range []ExecutionState{
		ExecutionQueued,
		ExecutionRunning,
		ExecutionSealing,
		ExecutionReady,
	} {
		state := state
		t.Run(string(state), func(t *testing.T) {
			fixture := newExportSourceLifecycleRealPortFixture(t, state)
			beforeJob := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
			beforePayload := loadExportSourceLifecyclePayload(t, fixture.harness.db, fixture.store, fixture.jobID)
			beforeUnrelatedSource := loadExportSourceLifecycleSourceLease(t, fixture.harness.db, fixture.unrelatedSourceID)
			beforeUnrelatedPoint := loadExportSourceLifecyclePointLease(t, fixture.harness.db, fixture.unrelatedLeaseID)

			port := newExportSourceLifecycleRecordingPersistentPort(t, fixture)
			lifecycle, err := NewLifecycle(LifecycleDependencies{
				DB: fixture.harness.db, Port: port, Now: fixture.harness.service.now,
			})
			if err != nil {
				t.Fatal(err)
			}
			owner, err := NewSourceLifecycle(fixture.harness.db, lifecycle, fixture.harness.service.now, 1)
			if err != nil {
				t.Fatal(err)
			}
			request := backupasset.SourceLifecycleRequest{
				RecoveryPointID: fixture.pointID, LifecycleAttemptID: fixture.lifecycleAttemptID,
				Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
			}

			if err := owner.ExpireRecoveryPoint(context.Background(), request); err != nil {
				t.Fatalf("prepare %s Export through persistent lifecycle port: %v", state, err)
			}

			wantState := ExecutionSourceExpired
			if state == ExecutionReady {
				wantState = ExecutionExpiring
			}
			afterFirstJob := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
			if ExecutionState(afterFirstJob.ExecutionState) != wantState || afterFirstJob.ErrorCategory != "source_expired" ||
				afterFirstJob.CurrentAttemptID != nil || afterFirstJob.TransitionRevision != beforeJob.TransitionRevision+1 ||
				afterFirstJob.CurrentFenceRevision != beforeJob.CurrentFenceRevision+1 || afterFirstJob.CleanupState != string(CleanupNone) {
				t.Fatalf("prepared %s Export job before={%s} after={%s} want state=%s with one state/fence revision",
					state, exportSourceLifecycleJobDiagnostic(beforeJob), exportSourceLifecycleJobDiagnostic(afterFirstJob), wantState)
			}
			wantCalls := []string{
				"fence_attempts:" + fixture.jobID,
				"revoke_deliveries:" + fixture.jobID,
				"drain_streams:" + fixture.jobID,
				"release_source:" + fixture.jobID,
			}
			if !reflect.DeepEqual(port.calls, wantCalls) {
				t.Fatalf("prepare %s Export calls=%v want=%v", state, port.calls, wantCalls)
			}
			assertExportSourceLifecyclePayloadUnchanged(t, fixture.harness.db, fixture.store, fixture.jobID, beforePayload)
			assertExportSourceLifecycleTargetReleased(t, fixture)
			if got := loadExportSourceLifecycleSourceLease(t, fixture.harness.db, fixture.unrelatedSourceID); !reflect.DeepEqual(got, beforeUnrelatedSource) {
				t.Fatalf("prepare %s Export changed unrelated source lease: before_id=%s before_state=%s before_released=%t after_id=%s after_state=%s after_released=%t",
					state, beforeUnrelatedSource.ID, beforeUnrelatedSource.State, beforeUnrelatedSource.ReleasedAt != nil,
					got.ID, got.State, got.ReleasedAt != nil)
			}
			if got := loadExportSourceLifecyclePointLease(t, fixture.harness.db, fixture.unrelatedLeaseID); !reflect.DeepEqual(got, beforeUnrelatedPoint) {
				t.Fatalf("prepare %s Export changed unrelated RecoveryPoint lease: before_id=%s before_status=%s before_released=%t after_id=%s after_status=%s after_released=%t",
					state, beforeUnrelatedPoint.ID, beforeUnrelatedPoint.Status, beforeUnrelatedPoint.ReleasedAt != nil,
					got.ID, got.Status, got.ReleasedAt != nil)
			}

			firstSource := loadExportSourceLifecycleSourceLease(t, fixture.harness.db, fixture.sourceLeaseID)
			firstPoint := loadExportSourceLifecyclePointLease(t, fixture.harness.db, fixture.pointLeaseID)
			if err := owner.ExpireRecoveryPoint(context.Background(), request); err != nil {
				t.Fatalf("retry prepared %s Export: %v", state, err)
			}
			if got := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID); !reflect.DeepEqual(got, afterFirstJob) {
				t.Fatalf("idempotent %s prepare changed job: first={%s} retry={%s}",
					state, exportSourceLifecycleJobDiagnostic(afterFirstJob), exportSourceLifecycleJobDiagnostic(got))
			}
			if !reflect.DeepEqual(port.calls, wantCalls) {
				t.Fatalf("idempotent %s prepare repeated persistent effects: calls=%v want=%v", state, port.calls, wantCalls)
			}
			if got := loadExportSourceLifecycleSourceLease(t, fixture.harness.db, fixture.sourceLeaseID); !reflect.DeepEqual(got, firstSource) {
				t.Fatalf("idempotent %s prepare changed released source lease: first_id=%s first_state=%s first_released=%t retry_id=%s retry_state=%s retry_released=%t",
					state, firstSource.ID, firstSource.State, firstSource.ReleasedAt != nil,
					got.ID, got.State, got.ReleasedAt != nil)
			}
			if got := loadExportSourceLifecyclePointLease(t, fixture.harness.db, fixture.pointLeaseID); !reflect.DeepEqual(got, firstPoint) {
				t.Fatalf("idempotent %s prepare changed released RecoveryPoint lease: first_id=%s first_status=%s first_released=%t retry_id=%s retry_status=%s retry_released=%t",
					state, firstPoint.ID, firstPoint.Status, firstPoint.ReleasedAt != nil,
					got.ID, got.Status, got.ReleasedAt != nil)
			}
			assertExportSourceLifecyclePayloadUnchanged(t, fixture.harness.db, fixture.store, fixture.jobID, beforePayload)
		})
	}
}

func TestRecoveryPointSourceLifecycleExportFreshOwnerResumesClosedOutcomeCleanup(t *testing.T) {
	for _, outcome := range []struct {
		name          string
		prepareState  ExecutionState
		closedState   ExecutionState
		errorCategory string
	}{
		{name: "failed", prepareState: ExecutionFailed, closedState: ExecutionFailed, errorCategory: "internal_failure"},
		{name: "canceled", prepareState: ExecutionCancelRequested, closedState: ExecutionCanceled},
	} {
		outcome := outcome
		for _, restart := range []struct {
			name      string
			failAt    string
			wantState CleanupState
			wantCalls []string
		}{
			{
				name: "revoking", failAt: "destroy_key_and_selection", wantState: CleanupRevoking,
				wantCalls: []string{
					"fence_attempts", "revoke_deliveries", "drain_streams", "destroy_key_and_selection",
					"release_sources", "purge_ciphertext", "release_store",
				},
			},
			{
				name: "purge_failed", failAt: "purge_ciphertext", wantState: CleanupPurgeFailed,
				wantCalls: []string{"purge_ciphertext", "release_store"},
			},
			{name: "purged", wantState: CleanupPurged},
		} {
			restart := restart
			t.Run(outcome.name+"_"+restart.name, func(t *testing.T) {
				fixture := newExportSourceLifecycleRealPortFixture(t, ExecutionQueued)
				crashErr := errors.New("injected Export cleanup restart boundary")
				beforeRestartPort := &exportSourceLifecycleInterruptingPersistentPort{
					exportSourceLifecycleRecordingPersistentPort: newExportSourceLifecycleRecordingPersistentPort(t, fixture),
					failAt: restart.failAt, failure: crashErr,
				}
				beforeRestartLifecycle, err := NewLifecycle(LifecycleDependencies{
					DB: fixture.harness.db, Port: beforeRestartPort, Now: fixture.harness.service.now,
				})
				if err != nil {
					t.Fatal(err)
				}
				beforeRestartOwner, err := NewSourceLifecycle(
					fixture.harness.db, beforeRestartLifecycle, fixture.harness.service.now, 1,
				)
				if err != nil {
					t.Fatal(err)
				}

				job := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
				if outcome.errorCategory != "" {
					if err := fixture.harness.db.Model(&model.BackupAssetExportJob{}).
						Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
						Update("error_category", outcome.errorCategory).Error; err != nil {
						t.Fatalf("seed authoritative Export failure category: %v", err)
					}
				}
				if err := beforeRestartLifecycle.transitionExecution(
					context.Background(), &job, ExecutionQueued, outcome.prepareState,
				); err != nil {
					t.Fatalf("seed authoritative Export execution outcome: %v", err)
				}
				prepareRequest := backupasset.SourceLifecycleRequest{
					RecoveryPointID: fixture.pointID, LifecycleAttemptID: fixture.lifecycleAttemptID,
					Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
				}
				if err := beforeRestartOwner.ExpireRecoveryPoint(context.Background(), prepareRequest); err != nil {
					t.Fatalf("release exact Export source before restart: %v", err)
				}
				if outcome.closedState == ExecutionCanceled {
					job = loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
					if err := beforeRestartLifecycle.transitionExecution(
						context.Background(), &job, ExecutionCancelRequested, ExecutionCanceled,
					); err != nil {
						t.Fatalf("close authoritative Export cancellation: %v", err)
					}
				}

				cleanupState, cleanupErr := beforeRestartLifecycle.Cleanup(context.Background(), fixture.jobID)
				if restart.failAt == "" {
					if cleanupErr != nil || cleanupState != CleanupPurged {
						t.Fatalf("complete pre-restart Export cleanup state=%s err=%v", cleanupState, cleanupErr)
					}
				} else if !errors.Is(cleanupErr, crashErr) || cleanupState != restart.wantState {
					t.Fatalf("persist Export cleanup restart boundary state=%s err=%v want=%s/%v", cleanupState, cleanupErr, restart.wantState, crashErr)
				}
				assertExportSourceLifecycleTargetReleased(t, fixture)
				assertExportSourceLifecycleRetainedRepresentations(t, fixture.harness.db, fixture.jobID)
				beforeRestartJob := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
				if ExecutionState(beforeRestartJob.ExecutionState) != outcome.closedState ||
					beforeRestartJob.ErrorCategory != outcome.errorCategory ||
					CleanupState(beforeRestartJob.CleanupState) != restart.wantState {
					t.Fatalf("pre-restart authoritative Export outcome={%s} want execution=%s category=%q cleanup=%s",
						exportSourceLifecycleJobDiagnostic(beforeRestartJob), outcome.closedState, outcome.errorCategory, restart.wantState)
				}
				if err := fixture.harness.db.Model(&model.RecoveryPointLifecycleAttempt{}).
					Where("id = ?", fixture.lifecycleAttemptID).
					Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
					t.Fatalf("advance Export owner request to cleanup: %v", err)
				}

				restartPort := newExportSourceLifecycleRecordingPersistentPort(t, fixture)
				restartedLifecycle, err := NewLifecycle(LifecycleDependencies{
					DB: fixture.harness.db, Port: restartPort, Now: fixture.harness.service.now,
				})
				if err != nil {
					t.Fatal(err)
				}
				restartedOwner, err := NewSourceLifecycle(
					fixture.harness.db, restartedLifecycle, fixture.harness.service.now, 1,
				)
				if err != nil {
					t.Fatal(err)
				}
				cleanupRequest := prepareRequest
				cleanupRequest.Stage = backupasset.SourceLifecycleCleanup
				if err := restartedOwner.ExpireRecoveryPoint(context.Background(), cleanupRequest); err != nil {
					t.Fatalf("fresh Export owner did not converge retained %s/%s facts: %v", outcome.name, restart.name, err)
				}
				afterFirst := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
				if ExecutionState(afterFirst.ExecutionState) != outcome.closedState ||
					afterFirst.ErrorCategory != outcome.errorCategory || CleanupState(afterFirst.CleanupState) != CleanupPurged {
					t.Fatalf("fresh Export owner reclassified authoritative outcome: before={%s} after={%s}",
						exportSourceLifecycleJobDiagnostic(beforeRestartJob), exportSourceLifecycleJobDiagnostic(afterFirst))
				}
				var wantCalls []string
				for _, call := range restart.wantCalls {
					wantCalls = append(wantCalls, call+":"+fixture.jobID)
				}
				if !reflect.DeepEqual(restartPort.calls, wantCalls) {
					t.Fatalf("fresh Export owner cleanup calls=%v want=%v", restartPort.calls, wantCalls)
				}

				if err := restartedOwner.ExpireRecoveryPoint(context.Background(), cleanupRequest); err != nil {
					t.Fatalf("idempotent fresh Export owner cleanup: %v", err)
				}
				afterRetry := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
				if !reflect.DeepEqual(afterRetry, afterFirst) || !reflect.DeepEqual(restartPort.calls, wantCalls) {
					t.Fatalf("idempotent Export owner changed closed state/effects: first={%s} retry={%s} calls=%v want=%v",
						exportSourceLifecycleJobDiagnostic(afterFirst), exportSourceLifecycleJobDiagnostic(afterRetry), restartPort.calls, wantCalls)
				}
				assertExportSourceLifecycleTargetReleased(t, fixture)
				assertExportSourceLifecycleRetainedRepresentations(t, fixture.harness.db, fixture.jobID)
			})
		}
	}

	t.Run("failed_unreleased_source", func(t *testing.T) {
		fixture := newExportSourceLifecycleRealPortFixture(t, ExecutionQueued)
		port := newExportSourceLifecycleRecordingPersistentPort(t, fixture)
		lifecycle, err := NewLifecycle(LifecycleDependencies{
			DB: fixture.harness.db, Port: port, Now: fixture.harness.service.now,
		})
		if err != nil {
			t.Fatal(err)
		}
		owner, err := NewSourceLifecycle(fixture.harness.db, lifecycle, fixture.harness.service.now, 1)
		if err != nil {
			t.Fatal(err)
		}
		job := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
		if err := fixture.harness.db.Model(&model.BackupAssetExportJob{}).
			Where("id = ? AND transition_revision = ?", job.ID, job.TransitionRevision).
			Update("error_category", "internal_failure").Error; err != nil {
			t.Fatalf("seed authoritative Export failure category: %v", err)
		}
		if err := lifecycle.transitionExecution(context.Background(), &job, ExecutionQueued, ExecutionFailed); err != nil {
			t.Fatalf("seed authoritative Export failure: %v", err)
		}
		if err := fixture.harness.db.Model(&model.RecoveryPointLifecycleAttempt{}).
			Where("id = ?", fixture.lifecycleAttemptID).
			Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
			t.Fatalf("advance Export owner request to cleanup: %v", err)
		}
		beforeJob := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
		err = owner.ExpireRecoveryPoint(context.Background(), backupasset.SourceLifecycleRequest{
			RecoveryPointID: fixture.pointID, LifecycleAttemptID: fixture.lifecycleAttemptID,
			Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecycleCleanup,
		})
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("closed Export cleanup without exact released-source proof error=%v, want ErrConflict", err)
		}
		if got := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID); !reflect.DeepEqual(got, beforeJob) {
			t.Fatalf("unproven closed Export cleanup changed job: before={%s} after={%s}",
				exportSourceLifecycleJobDiagnostic(beforeJob), exportSourceLifecycleJobDiagnostic(got))
		}
		if len(port.calls) != 0 {
			t.Fatalf("unproven closed Export cleanup invoked effects: %v", port.calls)
		}
		source := loadExportSourceLifecycleSourceLease(t, fixture.harness.db, fixture.sourceLeaseID)
		point := loadExportSourceLifecyclePointLease(t, fixture.harness.db, fixture.pointLeaseID)
		if source.State != "active" || point.Status != string(backupasset.LeaseActive) {
			t.Fatalf("unproven closed Export cleanup changed exact source proof: source_id=%s source_state=%s source_released=%t point_id=%s point_status=%s point_released=%t",
				source.ID, source.State, source.ReleasedAt != nil, point.ID, point.Status, point.ReleasedAt != nil)
		}
	})
}

type exportSourceLifecycleInterruptingPersistentPort struct {
	*exportSourceLifecycleRecordingPersistentPort
	failAt  string
	failure error
}

func (port *exportSourceLifecycleInterruptingPersistentPort) DestroyJobKeyAndSelection(ctx context.Context, jobID string) error {
	return port.record("destroy_key_and_selection", jobID, func() error {
		if port.failAt == "destroy_key_and_selection" {
			return port.failure
		}
		return port.inner.DestroyJobKeyAndSelection(ctx, jobID)
	})
}

func (port *exportSourceLifecycleInterruptingPersistentPort) PurgeCiphertext(ctx context.Context, jobID string) error {
	return port.record("purge_ciphertext", jobID, func() error {
		if port.failAt == "purge_ciphertext" {
			return port.failure
		}
		return port.inner.PurgeCiphertext(ctx, jobID)
	})
}

func TestRecoveryPointSourceLifecycleExportFreshOwnerNoopsPrePurgedClosedOutcome(t *testing.T) {
	for _, outcome := range []struct {
		name         string
		fixtureState ExecutionState
		wantState    ExecutionState
		category     string
	}{
		{name: "failed", fixtureState: ExecutionQueued, wantState: ExecutionFailed, category: "internal_failure"},
		{name: "canceled", fixtureState: ExecutionQueued, wantState: ExecutionCanceled},
		{name: "expired", fixtureState: ExecutionReady, wantState: ExecutionExpired, category: "deadline"},
	} {
		outcome := outcome
		t.Run(outcome.name, func(t *testing.T) {
			fixture := newExportSourceLifecycleRealPortFixture(t, outcome.fixtureState)
			ordinaryPort := newExportSourceLifecycleRecordingPersistentPort(t, fixture)
			ordinaryLifecycle, err := NewLifecycle(LifecycleDependencies{
				DB: fixture.harness.db, Port: ordinaryPort, Now: fixture.harness.service.now,
			})
			if err != nil {
				t.Fatal(err)
			}
			job := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
			if outcome.category != "" {
				if err := fixture.harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
					Update("error_category", outcome.category).Error; err != nil {
					t.Fatalf("seed pre-purged %s Export category: %v", outcome.name, err)
				}
			}
			switch outcome.wantState {
			case ExecutionFailed:
				err = ordinaryLifecycle.transitionExecution(context.Background(), &job, ExecutionQueued, ExecutionFailed)
			case ExecutionCanceled:
				err = ordinaryLifecycle.transitionExecution(context.Background(), &job, ExecutionQueued, ExecutionCancelRequested)
				if err == nil {
					err = ordinaryLifecycle.transitionExecution(
						context.Background(), &job, ExecutionCancelRequested, ExecutionCanceled,
					)
				}
			case ExecutionExpired:
				err = ordinaryLifecycle.transitionExecution(context.Background(), &job, ExecutionReady, ExecutionExpiring)
			default:
				t.Fatalf("unsupported pre-purged Export outcome %s", outcome.wantState)
			}
			if err != nil {
				t.Fatalf("seed pre-purged %s Export outcome: %v", outcome.name, err)
			}
			state, cleanupErr := ordinaryLifecycle.Cleanup(context.Background(), fixture.jobID)
			if cleanupErr != nil || state != CleanupPurged {
				t.Fatalf("ordinary %s Export cleanup state=%s err=%v", outcome.name, state, cleanupErr)
			}
			if err := ordinaryLifecycle.finalizeExecutionAfterCleanup(context.Background(), fixture.jobID, state); err != nil {
				t.Fatalf("finalize ordinary %s Export cleanup: %v", outcome.name, err)
			}
			assertExportSourceLifecycleTargetReleased(t, fixture)
			assertExportSourceLifecycleRetainedRepresentations(t, fixture.harness.db, fixture.jobID)
			beforeOwner := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
			if ExecutionState(beforeOwner.ExecutionState) != outcome.wantState ||
				CleanupState(beforeOwner.CleanupState) != CleanupPurged || beforeOwner.ErrorCategory != outcome.category {
				t.Fatalf("ordinary pre-owner Export cleanup outcome={%s} want execution=%s category=%q cleanup=%s",
					exportSourceLifecycleJobDiagnostic(beforeOwner), outcome.wantState, outcome.category, CleanupPurged)
			}

			freshPort := newExportSourceLifecycleRecordingPersistentPort(t, fixture)
			freshLifecycle, err := NewLifecycle(LifecycleDependencies{
				DB: fixture.harness.db, Port: freshPort, Now: fixture.harness.service.now,
			})
			if err != nil {
				t.Fatal(err)
			}
			freshOwner, err := NewSourceLifecycle(fixture.harness.db, freshLifecycle, fixture.harness.service.now, 1)
			if err != nil {
				t.Fatal(err)
			}
			request := backupasset.SourceLifecycleRequest{
				RecoveryPointID: fixture.pointID, LifecycleAttemptID: fixture.lifecycleAttemptID,
				Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := freshOwner.ExpireRecoveryPoint(context.Background(), request); err != nil {
					t.Fatalf("fresh owner prepare pre-purged %s Export attempt=%d: %v", outcome.name, attempt, err)
				}
			}
			if err := fixture.harness.db.Model(&model.RecoveryPointLifecycleAttempt{}).
				Where("id = ?", fixture.lifecycleAttemptID).
				Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
				t.Fatalf("advance pre-purged %s Export owner to cleanup: %v", outcome.name, err)
			}
			request.Stage = backupasset.SourceLifecycleCleanup
			for attempt := 0; attempt < 2; attempt++ {
				if err := freshOwner.ExpireRecoveryPoint(context.Background(), request); err != nil {
					t.Fatalf("fresh owner cleanup pre-purged %s Export attempt=%d: %v", outcome.name, attempt, err)
				}
			}
			afterOwner := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
			if !reflect.DeepEqual(afterOwner, beforeOwner) || len(freshPort.calls) != 0 {
				t.Fatalf("fresh owner changed pre-purged %s Export: before={%s} after={%s} effects=%v",
					outcome.name, exportSourceLifecycleJobDiagnostic(beforeOwner),
					exportSourceLifecycleJobDiagnostic(afterOwner), freshPort.calls)
			}
			assertExportSourceLifecycleTargetReleased(t, fixture)
			assertExportSourceLifecycleRetainedRepresentations(t, fixture.harness.db, fixture.jobID)
		})
	}

	t.Run("failed_unreleased_source", func(t *testing.T) {
		fixture := newExportSourceLifecycleRealPortFixture(t, ExecutionQueued)
		port := newExportSourceLifecycleRecordingPersistentPort(t, fixture)
		lifecycle, err := NewLifecycle(LifecycleDependencies{
			DB: fixture.harness.db, Port: port, Now: fixture.harness.service.now,
		})
		if err != nil {
			t.Fatal(err)
		}
		job := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
		if err := fixture.harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
			Update("error_category", "internal_failure").Error; err != nil {
			t.Fatalf("seed unproven pre-purged Export category: %v", err)
		}
		if err := lifecycle.transitionExecution(context.Background(), &job, ExecutionQueued, ExecutionFailed); err != nil {
			t.Fatalf("seed unproven pre-purged Export failure: %v", err)
		}
		if err := fixture.harness.db.Model(&model.BackupAssetExportJob{}).Where("id = ?", job.ID).
			Update("cleanup_state", CleanupPurged).Error; err != nil {
			t.Fatalf("seed unproven pre-purged Export cleanup: %v", err)
		}
		owner, err := NewSourceLifecycle(fixture.harness.db, lifecycle, fixture.harness.service.now, 1)
		if err != nil {
			t.Fatal(err)
		}
		before := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
		err = owner.ExpireRecoveryPoint(context.Background(), backupasset.SourceLifecycleRequest{
			RecoveryPointID: fixture.pointID, LifecycleAttemptID: fixture.lifecycleAttemptID,
			Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare,
		})
		if !errors.Is(err, backupasset.ErrConflict) {
			t.Fatalf("unreleased pre-purged Export source error=%v want ErrConflict", err)
		}
		after := loadExportSourceLifecycleJob(t, fixture.harness.db, fixture.jobID)
		if !reflect.DeepEqual(after, before) || len(port.calls) != 0 {
			t.Fatalf("unreleased pre-purged Export source mutated state/effects: before={%s} after={%s} effects=%v",
				exportSourceLifecycleJobDiagnostic(before), exportSourceLifecycleJobDiagnostic(after), port.calls)
		}
	})
}

type exportSourceLifecycleRealPortFixture struct {
	harness            serviceHarness
	store              *Store
	jobID              string
	pointID            string
	lifecycleAttemptID string
	sourceLeaseID      string
	pointLeaseID       string
	unrelatedSourceID  string
	unrelatedLeaseID   string
}

func newExportSourceLifecycleRealPortFixture(
	t *testing.T,
	state ExecutionState,
) exportSourceLifecycleRealPortFixture {
	t.Helper()
	var harness serviceHarness
	var store *Store
	var jobID string
	switch state {
	case ExecutionQueued:
		harness = newServiceHarness(t)
		jobID = commitFenceAttemptsTestJob(t, harness, "source-lifecycle-queued").ID
		store = openExportSourceLifecycleStore(t)
	case ExecutionRunning:
		harness = newServiceHarness(t)
		job, _, _ := claimFenceAttemptsTestJob(t, harness, "source-lifecycle-running")
		jobID = job.ID
		store = openExportSourceLifecycleStore(t)
	case ExecutionSealing:
		sealed := createPersistentSealedFixture(t)
		harness, store, jobID = sealed.harness, sealed.store, sealed.jobID
	case ExecutionReady:
		ready := createPersistentReadyFixture(t)
		harness, store, jobID = ready.harness, ready.store, ready.jobID
	default:
		t.Fatalf("unsupported real-port Export state %q", state)
	}
	job := loadExportSourceLifecycleJob(t, harness.db, jobID)
	if ExecutionState(job.ExecutionState) != state {
		t.Fatalf("real-port Export fixture state=%s want=%s job={%s}",
			job.ExecutionState, state, exportSourceLifecycleJobDiagnostic(job))
	}
	if err := harness.db.AutoMigrate(&model.RecoveryPointLifecycleAttempt{}); err != nil {
		t.Fatalf("migrate lifecycle authority for Export fixture: %v", err)
	}
	var source model.BackupAssetExportSourceLease
	if err := harness.db.Where("job_id = ?", jobID).Order("recovery_point_id ASC").Take(&source).Error; err != nil {
		t.Fatalf("load target Export source lease: %v", err)
	}
	lifecycleAttemptID, err := backupasset.NewOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.db.Create(&model.RecoveryPointLifecycleAttempt{
		ID: lifecycleAttemptID, RecoveryPointID: source.RecoveryPointID,
		Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking),
	}).Error; err != nil {
		t.Fatalf("seed Export lifecycle attempt: %v", err)
	}
	unrelatedSourceID, unrelatedLeaseID := seedExportSourceLifecycleUnrelatedLease(t, harness, jobID)
	return exportSourceLifecycleRealPortFixture{
		harness: harness, store: store, jobID: jobID, pointID: source.RecoveryPointID,
		lifecycleAttemptID: lifecycleAttemptID, sourceLeaseID: source.ID, pointLeaseID: source.LeaseID,
		unrelatedSourceID: unrelatedSourceID, unrelatedLeaseID: unrelatedLeaseID,
	}
}

func openExportSourceLifecycleStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(StoreConfig{Root: filepath.Join(t.TempDir(), "source-lifecycle-real-port")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close source lifecycle real-port store: %v", err)
		}
	})
	return store
}

func seedExportSourceLifecycleUnrelatedLease(t *testing.T, harness serviceHarness, targetJobID string) (string, string) {
	t.Helper()
	now := harness.service.now().UTC()
	pointID := strings.Repeat("e", 32)
	jobID := strings.Repeat("d", 32)
	if jobID == targetJobID {
		t.Fatal("unrelated Export job ID collided with target")
	}
	retentionUntil := now.Add(24 * time.Hour)
	if err := harness.db.Create(&model.RecoveryPoint{
		ID: pointID, RepositoryID: strings.Repeat("f", 32), State: string(backupasset.RecoveryPointCommitted),
		Semantics: string(backupasset.PointNativeSnapshot), SourceFingerprint: "unrelated-source-fingerprint-v1",
		CapabilityRevision: 1, PhysicalAvailability: string(backupasset.PhysicalOnline),
		ImmutabilityLevel: string(backupasset.ImmutabilityBackendVersioned), HoldState: string(backupasset.HoldNone),
		RetentionUntil: &retentionUntil, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed unrelated Export point: %v", err)
	}
	if err := harness.db.Create(&model.BackupAssetExportJob{
		ID: jobID, OwnerUserID: 777, LifecycleEnqueueSequence: 999999,
		ExecutionState: string(ExecutionQueued), CleanupState: string(CleanupNone),
		TransitionRevision: 1, CurrentFenceRevision: 1, AbsoluteDeadline: now.Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed unrelated Export job: %v", err)
	}
	lease, err := harness.lease.Acquire(context.Background(), backupasset.AcquireLeaseRequest{
		RecoveryPointID: pointID, HolderType: backupasset.LeaseHolderExportJob,
		OwnerID: jobID, AbsoluteDeadline: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("acquire unrelated Export RecoveryPoint lease: %v", err)
	}
	fenceHash := sha256.Sum256([]byte(lease.Fence.FenceToken))
	sourceID := strings.Repeat("c", 32)
	if err := harness.db.Create(&model.BackupAssetExportSourceLease{
		ID: sourceID, JobID: jobID, RecoveryPointID: pointID, LeaseID: lease.ID,
		LeaseAttemptID: lease.Fence.AttemptID, FenceHash: hex.EncodeToString(fenceHash[:]),
		AbsoluteDeadline: lease.AbsoluteDeadline, RetentionUntil: &retentionUntil,
		State: "active", AcquiredAt: lease.LastHeartbeatAt, RenewedAt: lease.LastHeartbeatAt,
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed unrelated Export source lease: %v", err)
	}
	return sourceID, lease.ID
}

type exportSourceLifecycleRecordingPersistentPort struct {
	inner *PersistentLifecyclePort
	calls []string
}

func newExportSourceLifecycleRecordingPersistentPort(
	t *testing.T,
	fixture exportSourceLifecycleRealPortFixture,
) *exportSourceLifecycleRecordingPersistentPort {
	t.Helper()
	quota, err := NewQuotaService(fixture.harness.db, fixture.harness.service.now, fixture.harness.config.Quota)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := NewDeliveryGateway(DeliveryGatewayDependencies{
		DB: fixture.harness.db, Now: fixture.harness.service.now, Session: &deliverySessionValidatorStub{},
		Store: fixture.store, Keys: backupasset.NewKeyring(fixture.harness.db, fixture.harness.service.now),
		Audit: mustDeliveryAudit(t),
		Config: DeliveryGatewayConfig{
			TicketTTL: 5 * time.Minute, MaxRequests: 4, MaxCumulativeBytes: 1 << 20, MaxInFlight: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	persistent, err := NewPersistentLifecyclePort(PersistentLifecyclePortDependencies{
		DB: fixture.harness.db, Delivery: delivery, Sources: fixture.harness.lease,
		Quota: quota, Store: fixture.store, Now: fixture.harness.service.now,
		AttemptWork: NewAttemptWorkRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &exportSourceLifecycleRecordingPersistentPort{inner: persistent}
}

func (port *exportSourceLifecycleRecordingPersistentPort) record(name, jobID string, run func() error) error {
	port.calls = append(port.calls, name+":"+jobID)
	return run()
}

func (port *exportSourceLifecycleRecordingPersistentPort) FenceAttempts(ctx context.Context, jobID string) error {
	return port.record("fence_attempts", jobID, func() error { return port.inner.FenceAttempts(ctx, jobID) })
}

func (port *exportSourceLifecycleRecordingPersistentPort) RevokeDeliveries(ctx context.Context, jobID string) error {
	return port.record("revoke_deliveries", jobID, func() error { return port.inner.RevokeDeliveries(ctx, jobID) })
}

func (port *exportSourceLifecycleRecordingPersistentPort) DrainStreams(ctx context.Context, jobID string) error {
	return port.record("drain_streams", jobID, func() error { return port.inner.DrainStreams(ctx, jobID) })
}

func (port *exportSourceLifecycleRecordingPersistentPort) ReleaseRecoveryPointSource(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
	jobID string,
) error {
	return port.record("release_source", jobID, func() error {
		return port.inner.ReleaseRecoveryPointSource(ctx, request, jobID)
	})
}

func (port *exportSourceLifecycleRecordingPersistentPort) DestroyJobKeyAndSelection(ctx context.Context, jobID string) error {
	return port.record("destroy_key_and_selection", jobID, func() error { return port.inner.DestroyJobKeyAndSelection(ctx, jobID) })
}

func (port *exportSourceLifecycleRecordingPersistentPort) ReleaseSourcesAndNonStore(ctx context.Context, jobID string) error {
	return port.record("release_sources", jobID, func() error { return port.inner.ReleaseSourcesAndNonStore(ctx, jobID) })
}

func (port *exportSourceLifecycleRecordingPersistentPort) PurgeCiphertext(ctx context.Context, jobID string) error {
	return port.record("purge_ciphertext", jobID, func() error { return port.inner.PurgeCiphertext(ctx, jobID) })
}

func (port *exportSourceLifecycleRecordingPersistentPort) ReleaseStoreBytes(ctx context.Context, jobID string) error {
	return port.record("release_store", jobID, func() error { return port.inner.ReleaseStoreBytes(ctx, jobID) })
}

type exportSourceLifecyclePayload struct {
	selection    []persistentLifecycleSelectionMetadata
	key          model.BackupAssetExportKey
	artifacts    []model.BackupAssetExportArtifact
	reservations []model.BackupAssetExportReservation
	ciphertext   map[string][]byte
}

func loadExportSourceLifecyclePayload(t *testing.T, db *gorm.DB, store *Store, jobID string) exportSourceLifecyclePayload {
	t.Helper()
	payload := exportSourceLifecyclePayload{
		selection:  loadPersistentLifecycleSelectionMetadata(t, db, jobID),
		ciphertext: make(map[string][]byte),
	}
	if err := db.Where("job_id = ?", jobID).Take(&payload.key).Error; err != nil {
		t.Fatalf("load Export job key: %v", err)
	}
	if err := db.Where("job_id = ?", jobID).Order("id ASC").Find(&payload.artifacts).Error; err != nil {
		t.Fatalf("load Export artifacts: %v", err)
	}
	if err := db.Where("job_id = ? AND kind = ?", jobID, "store").Order("id ASC").Find(&payload.reservations).Error; err != nil {
		t.Fatalf("load Export store reservations: %v", err)
	}
	if len(payload.reservations) != 2 {
		t.Fatalf("Export store reservations=%d want exact global/user pair", len(payload.reservations))
	}
	for _, artifact := range payload.artifacts {
		bytes, err := os.ReadFile(filepath.Join(store.root, artifact.Locator))
		if err != nil {
			t.Fatalf("read Export ciphertext %s: category=read_failed error_present=true", artifact.ID)
		}
		payload.ciphertext[artifact.ID] = bytes
	}
	return payload
}

func assertExportSourceLifecyclePayloadUnchanged(
	t *testing.T,
	db *gorm.DB,
	store *Store,
	jobID string,
	want exportSourceLifecyclePayload,
) {
	t.Helper()
	got := loadExportSourceLifecyclePayload(t, db, store, jobID)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Export prepare changed selection/key/artifact/ciphertext/store reservation: want_selection=%d got_selection=%d want_key_id=%s got_key_id=%s want_key_state=%s got_key_state=%s want_artifacts=%d got_artifacts=%d want_reservations=%d got_reservations=%d want_ciphertext_objects=%d got_ciphertext_objects=%d",
			len(want.selection), len(got.selection), want.key.ID, got.key.ID, want.key.State, got.key.State,
			len(want.artifacts), len(got.artifacts), len(want.reservations), len(got.reservations),
			len(want.ciphertext), len(got.ciphertext))
	}
}

func assertExportSourceLifecycleTargetReleased(t *testing.T, fixture exportSourceLifecycleRealPortFixture) {
	t.Helper()
	source := loadExportSourceLifecycleSourceLease(t, fixture.harness.db, fixture.sourceLeaseID)
	point := loadExportSourceLifecyclePointLease(t, fixture.harness.db, fixture.pointLeaseID)
	if source.State != "released" || source.ReleasedAt == nil ||
		point.Status != string(backupasset.LeaseReleased) || point.ReleasedAt == nil {
		t.Fatalf("Export prepare did not release exact source/point lease: source_id=%s source_state=%s source_released=%t point_id=%s point_status=%s point_released=%t",
			source.ID, source.State, source.ReleasedAt != nil, point.ID, point.Status, point.ReleasedAt != nil)
	}
}

func assertExportSourceLifecycleRetainedRepresentations(t *testing.T, db *gorm.DB, jobID string) {
	t.Helper()
	var itemCount, sourceCount int64
	if err := db.Model(&model.BackupAssetExportItem{}).Where("job_id = ?", jobID).Count(&itemCount).Error; err != nil {
		t.Fatalf("count retained Export items: %v", err)
	}
	if err := db.Model(&model.BackupAssetExportSourceLease{}).Where("job_id = ?", jobID).Count(&sourceCount).Error; err != nil {
		t.Fatalf("count retained Export source leases: %v", err)
	}
	if itemCount == 0 || sourceCount == 0 {
		t.Fatalf("Export cleanup removed owner discovery facts: items=%d sources=%d", itemCount, sourceCount)
	}
}

func loadExportSourceLifecycleJob(t *testing.T, db *gorm.DB, jobID string) model.BackupAssetExportJob {
	t.Helper()
	var job model.BackupAssetExportJob
	if err := db.Where("id = ?", jobID).Take(&job).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

func exportSourceLifecycleJobDiagnostic(job model.BackupAssetExportJob) string {
	return fmt.Sprintf(
		"id=%s execution=%s category=%q cleanup=%s transition_revision=%d fence_revision=%d attempt_present=%t",
		job.ID, job.ExecutionState, job.ErrorCategory, job.CleanupState,
		job.TransitionRevision, job.CurrentFenceRevision, job.CurrentAttemptID != nil,
	)
}

func loadExportSourceLifecycleSourceLease(t *testing.T, db *gorm.DB, sourceID string) model.BackupAssetExportSourceLease {
	t.Helper()
	var source model.BackupAssetExportSourceLease
	if err := db.Where("id = ?", sourceID).Take(&source).Error; err != nil {
		t.Fatal(err)
	}
	return source
}

func loadExportSourceLifecyclePointLease(t *testing.T, db *gorm.DB, leaseID string) model.RecoveryPointLease {
	t.Helper()
	var lease model.RecoveryPointLease
	if err := db.Where("id = ?", leaseID).Take(&lease).Error; err != nil {
		t.Fatal(err)
	}
	return lease
}

func TestRecoveryPointSourceLifecycleExportSeparatesPrepareFromCleanup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/source-lifecycle.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
		&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportSourceLease{},
		&model.BackupAssetExportKey{}, &model.BackupAssetExportArtifact{}, &model.BackupAssetExportReservation{},
	); err != nil {
		t.Fatalf("migrate source lifecycle tables: %v", err)
	}
	now := time.Date(2026, 8, 17, 14, 42, 0, 0, time.UTC)
	pointID, attemptID, jobID := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
	leaseID, sourceLeaseID, keyID, artifactID := strings.Repeat("4", 32), strings.Repeat("5", 32), strings.Repeat("6", 32), strings.Repeat("7", 32)
	if err := db.Create(&model.RecoveryPoint{ID: pointID, RepositoryID: strings.Repeat("8", 32)}).Error; err != nil {
		t.Fatalf("seed point: %v", err)
	}
	if err := db.Create(&model.RecoveryPointLifecycleAttempt{ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(backupasset.LifecyclePhaseRevoking)}).Error; err != nil {
		t.Fatalf("seed lifecycle attempt: %v", err)
	}
	job := model.BackupAssetExportJob{ID: jobID, LifecycleEnqueueSequence: 1, ExecutionState: string(ExecutionRunning), CleanupState: string(CleanupNone), TransitionRevision: 1, AbsoluteDeadline: now.Add(time.Hour)}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("seed Export job: %v", err)
	}
	item := model.BackupAssetExportItem{ID: strings.Repeat("9", 32), JobID: jobID, RecoveryPointID: pointID, EntryID: strings.Repeat("a", 64), CatalogGenerationID: strings.Repeat("b", 32), PathNonce: []byte{1}, PathCiphertext: []byte("private-selection"), State: string(ItemPending)}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("seed Export item: %v", err)
	}
	leaseAttemptID := strings.Repeat("c", 32)
	fenceToken := strings.Repeat("d", 64)
	fenceHash := sha256.Sum256([]byte(fenceToken))
	if err := db.Create(&model.BackupAssetExportSourceLease{
		ID: sourceLeaseID, JobID: jobID, RecoveryPointID: pointID, LeaseID: leaseID,
		LeaseAttemptID: leaseAttemptID, FenceHash: hex.EncodeToString(fenceHash[:]),
		State: "active", AcquiredAt: now, RenewedAt: now, AbsoluteDeadline: now.Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed Export source lease: %v", err)
	}
	if err := db.Create(&model.RecoveryPointLease{ID: leaseID, RecoveryPointID: pointID, HolderType: string(backupasset.LeaseHolderExportJob), OwnerID: jobID, AttemptID: leaseAttemptID, FenceToken: fenceToken, Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(time.Hour), AbsoluteDeadline: now.Add(time.Hour), LastHeartbeatAt: now}).Error; err != nil {
		t.Fatalf("seed Export RecoveryPoint lease: %v", err)
	}
	if err := db.Create(&model.BackupAssetExportKey{ID: keyID, JobID: jobID, State: "active", WrappedDEK: []byte("wrapped-export-key"), EnvelopeNonce: []byte{1}}).Error; err != nil {
		t.Fatalf("seed Export key: %v", err)
	}
	if err := db.Create(&model.BackupAssetExportArtifact{ID: artifactID, JobID: jobID, JobKeyID: keyID, State: "ready", Locator: "artifact.xre", NoncePrefix: []byte{1}}).Error; err != nil {
		t.Fatalf("seed Export artifact: %v", err)
	}
	jobRef := jobID
	for index, bucket := range []string{strings.Repeat("e", 32), strings.Repeat("f", 32)} {
		reservation := model.BackupAssetExportReservation{ID: strings.Repeat(string(rune('a'+index)), 32), BucketID: bucket, JobID: &jobRef, Kind: "store", State: "active", LeaseExpiresAt: now.Add(time.Hour)}
		if err := db.Create(&reservation).Error; err != nil {
			t.Fatalf("seed Export reservation: %v", err)
		}
	}

	port := &sourceLifecycleExportPort{db: db, now: now}
	lifecycle := &Lifecycle{db: db, port: port, now: func() time.Time { return now }}
	owner, err := NewSourceLifecycle(db, lifecycle, func() time.Time { return now }, 16)
	if err != nil {
		t.Fatalf("NewSourceLifecycle: %v", err)
	}
	request := backupasset.SourceLifecycleRequest{RecoveryPointID: pointID, LifecycleAttemptID: attemptID, Operation: backupasset.LifecycleRetentionExpire, Stage: backupasset.SourceLifecyclePrepare}
	if err := owner.ExpireRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("prepare Export lifecycle: %v", err)
	}
	assertExportPayloadState(t, db, jobID, keyID, artifactID, true)
	var sourceLease model.BackupAssetExportSourceLease
	var pointLease model.RecoveryPointLease
	db.First(&sourceLease, "id = ?", sourceLeaseID)
	db.First(&pointLease, "id = ?", leaseID)
	if sourceLease.State != "released" || pointLease.Status != string(backupasset.LeaseReleased) {
		t.Fatalf("Export prepare leases not released: source_id=%s source_state=%s source_released=%t point_id=%s point_status=%s point_released=%t",
			sourceLease.ID, sourceLease.State, sourceLease.ReleasedAt != nil,
			pointLease.ID, pointLease.Status, pointLease.ReleasedAt != nil)
	}

	if err := db.Model(&model.RecoveryPointLifecycleAttempt{}).Where("id = ?", attemptID).Update("phase", backupasset.LifecyclePhaseCleaning).Error; err != nil {
		t.Fatalf("advance lifecycle: %v", err)
	}
	request.Stage = backupasset.SourceLifecycleCleanup
	if err := owner.ExpireRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("cleanup Export lifecycle: %v", err)
	}
	if err := owner.ExpireRecoveryPoint(context.Background(), request); err != nil {
		t.Fatalf("idempotent Export cleanup: %v", err)
	}
	assertExportPayloadState(t, db, jobID, keyID, artifactID, false)
}

func TestRecoveryPointSourceLifecycleExportRejectsDivergentItemSourceRepresentations(t *testing.T) {
	for _, fixtureName := range []string{"item_only", "source_only", "mismatched_points", "split_jobs"} {
		for _, stage := range []backupasset.SourceLifecycleStage{backupasset.SourceLifecyclePrepare, backupasset.SourceLifecycleCleanup} {
			t.Run(fixtureName+"_"+string(stage), func(t *testing.T) {
				db, err := gorm.Open(sqlite.Open(t.TempDir()+"/source-representation.db?_busy_timeout=5000&_loc=UTC"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				if err != nil {
					t.Fatal(err)
				}
				if err := db.AutoMigrate(
					&model.RecoveryPoint{}, &model.RecoveryPointLifecycleAttempt{}, &model.RecoveryPointLease{},
					&model.BackupAssetExportJob{}, &model.BackupAssetExportItem{}, &model.BackupAssetExportSourceLease{},
					&model.BackupAssetExportKey{}, &model.BackupAssetExportArtifact{}, &model.BackupAssetExportReservation{},
				); err != nil {
					t.Fatal(err)
				}
				now := time.Date(2026, 8, 17, 16, 8, 0, 0, time.UTC)
				pointID, otherPointID, attemptID := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32)
				for _, point := range []model.RecoveryPoint{{ID: pointID, RepositoryID: strings.Repeat("4", 32)}, {ID: otherPointID, RepositoryID: strings.Repeat("4", 32)}} {
					if err := db.Create(&point).Error; err != nil {
						t.Fatal(err)
					}
				}
				phase := backupasset.LifecyclePhaseRevoking
				if stage == backupasset.SourceLifecycleCleanup {
					phase = backupasset.LifecyclePhaseCleaning
				}
				if err := db.Create(&model.RecoveryPointLifecycleAttempt{
					ID: attemptID, RecoveryPointID: pointID, Operation: string(backupasset.LifecycleRetentionExpire), Phase: string(phase),
				}).Error; err != nil {
					t.Fatal(err)
				}

				jobIDs := []string{strings.Repeat("5", 32)}
				if fixtureName == "split_jobs" {
					jobIDs = append(jobIDs, strings.Repeat("6", 32))
				}
				for index, jobID := range jobIDs {
					if err := db.Create(&model.BackupAssetExportJob{
						ID: jobID, LifecycleEnqueueSequence: int64(index + 1), ExecutionState: string(ExecutionRunning),
						CleanupState: string(CleanupNone), TransitionRevision: 1, AbsoluteDeadline: now.Add(time.Hour),
					}).Error; err != nil {
						t.Fatal(err)
					}
				}
				seedItem := func(jobID, itemPointID string, index int) {
					t.Helper()
					if err := db.Create(&model.BackupAssetExportItem{
						ID: strings.Repeat(string(rune('7'+index)), 32), JobID: jobID, RecoveryPointID: itemPointID,
						EntryID: strings.Repeat(string(rune('a'+index)), 64), CatalogGenerationID: strings.Repeat(string(rune('c'+index)), 32),
						PathNonce: []byte{1}, PathCiphertext: []byte("private-selection"), State: string(ItemPending),
					}).Error; err != nil {
						t.Fatal(err)
					}
				}
				seedSource := func(jobID, sourcePointID string, index int) {
					t.Helper()
					leaseID := strings.Repeat(string(rune('d'+index)), 32)
					if err := db.Create(&model.BackupAssetExportSourceLease{
						ID: strings.Repeat(string(rune('f'+index)), 32), JobID: jobID, RecoveryPointID: sourcePointID,
						LeaseID: leaseID, State: "active", AcquiredAt: now, RenewedAt: now, AbsoluteDeadline: now.Add(time.Hour),
					}).Error; err != nil {
						t.Fatal(err)
					}
					if err := db.Create(&model.RecoveryPointLease{
						ID: leaseID, RecoveryPointID: sourcePointID, HolderType: string(backupasset.LeaseHolderExportJob), OwnerID: jobID,
						AttemptID: strings.Repeat(string(rune('1'+index)), 32), FenceToken: strings.Repeat(string(rune('a'+index)), 64),
						Status: string(backupasset.LeaseActive), LeaseExpiresAt: now.Add(time.Hour), AbsoluteDeadline: now.Add(2 * time.Hour), LastHeartbeatAt: now,
					}).Error; err != nil {
						t.Fatal(err)
					}
				}

				switch fixtureName {
				case "item_only":
					seedItem(jobIDs[0], pointID, 0)
					seedSource(jobIDs[0], otherPointID, 0)
				case "source_only":
					seedItem(jobIDs[0], otherPointID, 0)
					seedSource(jobIDs[0], pointID, 0)
				case "mismatched_points":
					seedItem(jobIDs[0], pointID, 0)
					seedItem(jobIDs[0], otherPointID, 1)
					seedSource(jobIDs[0], pointID, 0)
				case "split_jobs":
					seedItem(jobIDs[0], pointID, 0)
					seedSource(jobIDs[0], otherPointID, 0)
					seedItem(jobIDs[1], otherPointID, 1)
					seedSource(jobIDs[1], pointID, 1)
				default:
					t.Fatalf("unknown fixture %q", fixtureName)
				}

				port := &representationGuardExportPort{}
				lifecycle := &Lifecycle{db: db, port: port, now: func() time.Time { return now }}
				owner, err := NewSourceLifecycle(db, lifecycle, func() time.Time { return now }, 1)
				if err != nil {
					t.Fatal(err)
				}
				err = owner.ExpireRecoveryPoint(context.Background(), backupasset.SourceLifecycleRequest{
					RecoveryPointID: pointID, LifecycleAttemptID: attemptID,
					Operation: backupasset.LifecycleRetentionExpire, Stage: stage,
				})
				if !errors.Is(err, backupasset.ErrConflict) {
					t.Fatalf("divergent representation error=%v, want ErrConflict", err)
				}
				if port.calls != 0 {
					t.Fatalf("divergent representation invoked %d lifecycle effects", port.calls)
				}
				for _, jobID := range jobIDs {
					var job model.BackupAssetExportJob
					if err := db.First(&job, "id = ?", jobID).Error; err != nil || job.ExecutionState != string(ExecutionRunning) || job.TransitionRevision != 1 {
						t.Fatalf("divergent representation mutated job={%s} err=%v",
							exportSourceLifecycleJobDiagnostic(job), err)
					}
				}
				var sourceChanged, pointLeaseChanged int64
				if err := db.Model(&model.BackupAssetExportSourceLease{}).Where("state <> ?", "active").Count(&sourceChanged).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Model(&model.RecoveryPointLease{}).Where("status <> ?", backupasset.LeaseActive).Count(&pointLeaseChanged).Error; err != nil {
					t.Fatal(err)
				}
				if sourceChanged != 0 || pointLeaseChanged != 0 {
					t.Fatalf("divergent representation changed leases source=%d point=%d", sourceChanged, pointLeaseChanged)
				}
			})
		}
	}
}

type representationGuardExportPort struct{ calls int }

func (port *representationGuardExportPort) effect() error { port.calls++; return nil }
func (port *representationGuardExportPort) FenceAttempts(context.Context, string) error {
	return port.effect()
}
func (port *representationGuardExportPort) RevokeDeliveries(context.Context, string) error {
	return port.effect()
}
func (port *representationGuardExportPort) DrainStreams(context.Context, string) error {
	return port.effect()
}
func (port *representationGuardExportPort) ReleaseRecoveryPointSource(context.Context, backupasset.SourceLifecycleRequest, string) error {
	return port.effect()
}
func (port *representationGuardExportPort) DestroyJobKeyAndSelection(context.Context, string) error {
	return port.effect()
}
func (port *representationGuardExportPort) ReleaseSourcesAndNonStore(context.Context, string) error {
	return port.effect()
}
func (port *representationGuardExportPort) PurgeCiphertext(context.Context, string) error {
	return port.effect()
}
func (port *representationGuardExportPort) ReleaseStoreBytes(context.Context, string) error {
	return port.effect()
}

type sourceLifecycleExportPort struct {
	db  *gorm.DB
	now time.Time
}

func (*sourceLifecycleExportPort) FenceAttempts(context.Context, string) error    { return nil }
func (*sourceLifecycleExportPort) RevokeDeliveries(context.Context, string) error { return nil }
func (*sourceLifecycleExportPort) DrainStreams(context.Context, string) error     { return nil }
func (port *sourceLifecycleExportPort) ReleaseRecoveryPointSource(
	ctx context.Context,
	request backupasset.SourceLifecycleRequest,
	jobID string,
) error {
	return port.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := backupasset.ValidateSourceLifecycleAttemptTx(ctx, tx, request); err != nil {
			return err
		}
		if err := tx.Model(&model.BackupAssetExportSourceLease{}).
			Where("job_id = ? AND recovery_point_id = ? AND state = ?", jobID, request.RecoveryPointID, "active").
			Updates(map[string]any{"state": "released", "released_at": port.now, "updated_at": port.now}).Error; err != nil {
			return err
		}
		return tx.Model(&model.RecoveryPointLease{}).
			Where("recovery_point_id = ? AND owner_id = ? AND holder_type = ? AND status = ?", request.RecoveryPointID, jobID, backupasset.LeaseHolderExportJob, backupasset.LeaseActive).
			Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": port.now, "updated_at": port.now}).Error
	})
}
func (port *sourceLifecycleExportPort) DestroyJobKeyAndSelection(_ context.Context, jobID string) error {
	if err := port.db.Model(&model.BackupAssetExportKey{}).Where("job_id = ?", jobID).Updates(map[string]any{"state": "destroyed", "wrapped_dek": []byte{}, "destroyed_at": port.now}).Error; err != nil {
		return err
	}
	return port.db.Model(&model.BackupAssetExportItem{}).Where("job_id = ?", jobID).Updates(map[string]any{"path_nonce": []byte{}, "path_ciphertext": []byte{}}).Error
}
func (port *sourceLifecycleExportPort) ReleaseSourcesAndNonStore(_ context.Context, jobID string) error {
	return port.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.BackupAssetExportSourceLease{}).Where("job_id = ? AND state = ?", jobID, "active").
			Updates(map[string]any{"state": "released", "released_at": port.now, "updated_at": port.now}).Error; err != nil {
			return err
		}
		return tx.Model(&model.RecoveryPointLease{}).
			Where("owner_id = ? AND holder_type = ? AND status = ?", jobID, backupasset.LeaseHolderExportJob, backupasset.LeaseActive).
			Updates(map[string]any{"status": backupasset.LeaseReleased, "released_at": port.now, "updated_at": port.now}).Error
	})
}
func (port *sourceLifecycleExportPort) PurgeCiphertext(_ context.Context, jobID string) error {
	return port.db.Model(&model.BackupAssetExportArtifact{}).Where("job_id = ?", jobID).Updates(map[string]any{"state": "purged", "purged_at": port.now}).Error
}
func (*sourceLifecycleExportPort) ReleaseStoreBytes(context.Context, string) error { return nil }

func assertExportPayloadState(t *testing.T, db *gorm.DB, jobID, keyID, artifactID string, preserved bool) {
	t.Helper()
	var key model.BackupAssetExportKey
	var artifact model.BackupAssetExportArtifact
	var item model.BackupAssetExportItem
	db.First(&key, "id = ?", keyID)
	db.First(&artifact, "id = ?", artifactID)
	db.First(&item, "job_id = ?", jobID)
	if preserved {
		if key.State != "active" || len(key.WrappedDEK) == 0 || artifact.State != "ready" || len(item.PathCiphertext) == 0 {
			t.Fatalf("Export payload not preserved: key_id=%s key_state=%s key_wrapped_dek_present=%t artifact_id=%s artifact_state=%s item_id=%s item_path_ciphertext_present=%t",
				key.ID, key.State, len(key.WrappedDEK) != 0, artifact.ID, artifact.State, item.ID, len(item.PathCiphertext) != 0)
		}
		return
	}
	if key.State != "destroyed" || len(key.WrappedDEK) != 0 || artifact.State != "purged" || len(item.PathCiphertext) != 0 {
		t.Fatalf("Export payload not cleaned: key_id=%s key_state=%s key_wrapped_dek_present=%t artifact_id=%s artifact_state=%s item_id=%s item_path_ciphertext_present=%t",
			key.ID, key.State, len(key.WrappedDEK) != 0, artifact.ID, artifact.State, item.ID, len(item.PathCiphertext) != 0)
	}
}
