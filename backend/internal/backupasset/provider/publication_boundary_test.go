package provider

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPublicationSourceBoundaries(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			for _, forbidden := range []string{
				"xirang/backend/internal/api",
				"xirang/backend/internal/task",
				"xirang/backend/internal/backupasset/repository",
				"xirang/backend/internal/backupasset/runtime",
			} {
				if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
					t.Fatalf("provider production source %s imports forbidden upper layer %s", file, path)
				}
			}
		}
	}

	for _, file := range []string{"restic_publication.go", "restic_manifest.go"} {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(contents)
		if regexp.MustCompile(`(?i)restic[^\n]*(forget|prune|delete|restore|init)`).MatchString(source) ||
			strings.Contains(source, "--latest") || strings.Contains(source, "latest 2") {
			t.Fatalf("publication provider source %s exposes a destructive or inferred-snapshot command", file)
		}
		if strings.Contains(source, "exec.Command") {
			t.Fatalf("publication provider source %s constructs a shell command directly", file)
		}
	}

	for _, file := range []string{
		"../repository/publication_execution.go",
		"../repository/publication_reconcile.go",
	} {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(contents), "snapshot.BuildIndex") || strings.Contains(string(contents), "SnapshotFileIndex") {
			t.Fatalf("publication repository source %s depends on the legacy snapshot index", file)
		}
	}
}
