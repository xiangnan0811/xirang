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

func TestProviderPublicationBoundaryKeepsReadAdaptersNonMutating(t *testing.T) {
	for _, file := range []string{"restic_publication.go", "restic_manifest.go", "restic.go", "rclone.go", "rsync.go"} {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(contents)
		if regexp.MustCompile(`(?i)restic[^\n]*(forget|prune|delete)`).MatchString(source) ||
			strings.Contains(source, "OperationResticForgetExact") ||
			strings.Contains(source, "OperationRcloneManagedDeleteExactPrefix") {
			t.Fatalf("read/publication source %s exposes a deletion operation", file)
		}
	}
	deletion, err := os.ReadFile("restic_deletion.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(deletion), "OperationResticForgetExact") {
		t.Fatal("exact Restic deletion does not live in restic_deletion.go")
	}
	if _, ok := any(&ResticAdapter{}).(PointDeleter); ok {
		t.Fatal("read Restic adapter unexpectedly implements PointDeleter")
	}
	if _, ok := any(&RcloneAdapter{}).(PointDeleter); ok {
		t.Fatal("read Rclone adapter unexpectedly implements PointDeleter")
	}
	if _, ok := any(&RsyncAdapter{}).(PointDeleter); ok {
		t.Fatal("read Rsync adapter unexpectedly implements PointDeleter")
	}
}

func TestResticAdapterReadPortRejectsMutation(t *testing.T) {
	TestProviderPublicationBoundaryKeepsReadAdaptersNonMutating(t)
}

func TestRcloneAdapterReadPortRejectsMutation(t *testing.T) {
	source, err := os.ReadFile("rclone.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"purge", "OperationRcloneManagedDeleteExactPrefix", "deletefile"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("read Rclone adapter contains mutation token %q", forbidden)
		}
	}
}

func TestRsyncAdapterReadPortRejectsMutation(t *testing.T) {
	source, err := os.ReadFile("rsync.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "DeleteCommittedPoint") || strings.Contains(string(source), "os.RemoveAll") {
		t.Fatal("read Rsync adapter reached handle-relative or path deletion")
	}
}

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
