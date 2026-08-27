package catalog

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestBackupFileSourceProjectionHasNoProviderCommandDependency(t *testing.T) {
	const path = "file_source.go"
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, payload, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range file.Imports {
		importPath := strings.Trim(spec.Path.Value, `"`)
		for _, forbidden := range []string{"/provider", "/runner", "/sshutil", "os/exec", "os"} {
			if importPath == forbidden || strings.Contains(importPath, forbidden) {
				t.Fatalf("projection imports forbidden command boundary %q", importPath)
			}
		}
	}
	for _, forbidden := range [][]byte{[]byte("exec.Command"), []byte("ProviderLocator"), []byte("EncryptedProviderLocator")} {
		if bytes.Contains(payload, forbidden) {
			t.Fatalf("projection source contains forbidden %q", forbidden)
		}
	}
}
