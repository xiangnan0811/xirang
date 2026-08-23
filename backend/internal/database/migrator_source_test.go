package database

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMigrationsHasNoDirtyBypassOrForceCall(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read database package: %v", err)
	}
	files := token.NewFileSet()
	foundRunMigrations := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(files, filepath.Clean(entry.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if function.Name.Name == "allowDirtyStartup" {
				t.Fatalf("production dirty bypass helper remains in %s", entry.Name())
			}
			if function.Name.Name != "RunMigrations" || function.Body == nil {
				continue
			}
			foundRunMigrations = true
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch expression := node.(type) {
				case *ast.SelectorExpr:
					if expression.Sel.Name == "Force" {
						t.Errorf("RunMigrations calls Force at %s", files.Position(expression.Pos()))
					}
				case *ast.Ident:
					if expression.Name == "allowDirtyStartup" {
						t.Errorf("RunMigrations retains allowDirtyStartup at %s", files.Position(expression.Pos()))
					}
				}
				return true
			})
		}
	}
	if !foundRunMigrations {
		t.Fatal("RunMigrations production function not found")
	}
}
