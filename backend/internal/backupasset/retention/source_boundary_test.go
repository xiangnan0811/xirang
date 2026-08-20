package retention

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestLifecycleDependentCleanupSourceBoundaryKeepsOwnerTablesAndRecoveryResultsOutOfRetention(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate retention source boundary test")
	}
	retentionDir := filepath.Dir(filename)
	entries, err := os.ReadDir(retentionDir)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenTables := []string{
		"backup_asset_delivery_", "catalog_generations", "catalog_entries", "backup_asset_search_",
		"backup_asset_processing_", "backup_asset_export_", "backup_asset_recovery_",
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(retentionDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range forbiddenTables {
			if strings.Contains(string(source), forbidden) {
				t.Fatalf("retention production %s directly references owner table family %q", entry.Name(), forbidden)
			}
		}
	}

	recoverySource := filepath.Join(retentionDir, "..", "recovery", "source_lifecycle.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), recoverySource, nil, 0)
	if err != nil {
		t.Fatalf("parse Recovery source lifecycle: %v", err)
	}
	forbiddenSelectors := map[string]struct{}{
		"BackupAssetRecoveryResultSet": {}, "BackupAssetRecoveryResult": {},
		"ResultLifecycleService": {}, "CleanupPhase": {}, "CleanupOwner": {}, "CleanupFence": {},
		"WorkspaceCleanupPhase": {}, "WorkspaceCleanupOwner": {}, "WorkspaceCleanupFence": {},
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.SelectorExpr:
			if _, forbidden := forbiddenSelectors[value.Sel.Name]; forbidden {
				t.Errorf("Recovery source lifecycle imports Child13-owned selector %q", value.Sel.Name)
			}
		case *ast.BasicLit:
			if value.Kind != token.STRING {
				break
			}
			literal, unquoteErr := strconv.Unquote(value.Value)
			if unquoteErr != nil {
				t.Errorf("decode Recovery source lifecycle literal: %v", unquoteErr)
				break
			}
			for _, forbidden := range []string{
				"backup_asset_recovery_result", "cleanup_phase", "cleanup_owner", "cleanup_fence",
				"workspace_cleanup_phase", "workspace_cleanup_owner", "workspace_cleanup_fence",
			} {
				if strings.Contains(literal, forbidden) {
					t.Errorf("Recovery source lifecycle queries Child13-owned field/table %q", forbidden)
				}
			}
		}
		return true
	})
}
