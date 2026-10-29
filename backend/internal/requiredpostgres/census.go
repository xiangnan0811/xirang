package requiredpostgres

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type censusFunc struct {
	name, source string
	refs         map[string]bool
	seed         string
	test         bool
}

func mixedEngineLiteral(node ast.Node) bool {
	sqlite, postgres := false, false
	ast.Inspect(node, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			value, _ := strconv.Unquote(lit.Value)
			sqlite = sqlite || value == "sqlite"
			postgres = postgres || value == "postgres"
		}
		return true
	})
	return sqlite && postgres
}

// Census inspects active test files only. It follows same-package function identifiers,
// including references in closures and function values, but not dynamic dispatch.
func Census(root string, inv Inventory) error {
	packages := map[string]map[string]*censusFunc{}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, ent fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ent.IsDir() {
			name := ent.Name()
			if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		active, err := build.Default.MatchFile(filepath.Dir(path), filepath.Base(path))
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		aliases := map[string]string{}
		dotImports := map[string]bool{}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			alias := filepath.Base(p)
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			if alias == "." {
				dotImports[p] = true
			} else {
				aliases[alias] = p
			}
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := "./" + filepath.ToSlash(rel) + "\x00" + f.Name.Name
		if packages[pkg] == nil {
			packages[pkg] = map[string]*censusFunc{}
		}
		declarations := append([]ast.Decl(nil), f.Decls...)
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range value.Names {
					if i < len(value.Values) {
						declarations = append(declarations, &ast.FuncDecl{Name: name, Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: value.Values[i]}}}})
					}
				}
			}
		}
		for _, decl := range declarations {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			c := &censusFunc{name: fn.Name.Name, source: filepath.ToSlash(path) + ":" + strconv.Itoa(fset.Position(fn.Name.Pos()).Line), refs: map[string]bool{}, test: fn.Type != nil && testName.MatchString(fn.Name.Name)}
			if c.test && strings.Contains(c.name, "Postgres") {
				c.seed = "Postgres test name"
			}
			if fn.Type == nil && mixedEngineLiteral(fn.Body) {
				c.seed = "mixed engine variable"
			}
			selectorNames := map[*ast.Ident]bool{}
			engineVars := map[string]bool{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					selectorNames[x.Sel] = true
				case *ast.AssignStmt:
					for i, value := range x.Rhs {
						if i < len(x.Lhs) && mixedEngineLiteral(value) {
							if id, ok := x.Lhs[i].(*ast.Ident); ok {
								engineVars[id.Name] = true
							}
						}
					}
				case *ast.ValueSpec:
					for i, value := range x.Values {
						if i < len(x.Names) && mixedEngineLiteral(value) {
							engineVars[x.Names[i].Name] = true
						}
					}
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					if !selectorNames[x] {
						if x.Obj == nil || x.Obj.Kind == ast.Fun {
							c.refs[x.Name] = true
						} else if x.Obj.Kind == ast.Var {
							if _, ok := x.Obj.Decl.(*ast.ValueSpec); ok {
								c.refs[x.Name] = true
							}
						}
					}
				case *ast.CallExpr:
					path, name := "", ""
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok {
							path, name = aliases[id.Name], sel.Sel.Name
						}
					} else if id, ok := x.Fun.(*ast.Ident); ok {
						name = id.Name
						if dotImports["os"] && (name == "Getenv" || name == "LookupEnv") {
							path = "os"
						}
						if dotImports["gorm.io/driver/postgres"] && name == "Open" {
							path = "gorm.io/driver/postgres"
						}
					}
					if path == "os" && (name == "Getenv" || name == "LookupEnv") && len(x.Args) > 0 {
						if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							value, _ := strconv.Unquote(lit.Value)
							if value == "TEST_POSTGRES_DSN" {
								c.seed = "TEST_POSTGRES_DSN"
							}
						}
					}
					if path == "gorm.io/driver/postgres" && name == "Open" {
						c.seed = "postgres.Open"
					}
				case *ast.RangeStmt:
					mixed := mixedEngineLiteral(x.X)
					if id, ok := x.X.(*ast.Ident); ok && engineVars[id.Name] {
						mixed = true
					}
					if mixed {
						c.seed = "mixed engine loop"
					}
				}
				return true
			})
			packages[pkg][c.name] = c
		}
		return nil
	})
	if err != nil {
		return err
	}
	registered := map[string]bool{}
	for _, g := range inv.Groups {
		for _, t := range g.Tests {
			registered[g.Package+"/"+t.Name] = true
		}
	}
	for _, e := range inv.Exclusions {
		registered[e.Package+"/"+e.Name] = true
	}
	missing := []string{}
	for scope, funcs := range packages {
		pkg, _, _ := strings.Cut(scope, "\x00")
		sources := map[string]string{}
		names := make([]string, 0, len(funcs))
		for name, f := range funcs {
			names = append(names, name)
			if f.seed != "" {
				sources[name] = f.seed + " (" + f.source + ")"
			}
		}
		sort.Strings(names)
		for changed := true; changed; {
			changed = false
			for _, name := range names {
				f := funcs[name]
				if sources[name] != "" {
					continue
				}
				refs := make([]string, 0, len(f.refs))
				for ref := range f.refs {
					refs = append(refs, ref)
				}
				sort.Strings(refs)
				for _, ref := range refs {
					if sources[ref] != "" {
						sources[name] = ref + " -> " + sources[ref]
						changed = true
						break
					}
				}
			}
		}
		for name, source := range sources {
			if funcs[name].test && !registered[pkg+"/"+name] {
				missing = append(missing, pkg+"/"+name+": "+source)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("unregistered PostgreSQL census candidates:\n%s", strings.Join(missing, "\n"))
	}
	return nil
}
