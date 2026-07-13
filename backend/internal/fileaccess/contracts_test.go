package fileaccess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContractsRequireBoundedPageAndRange(t *testing.T) {
	if _, err := (PageRequest{}).Normalize(); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("unbounded page error=%v", err)
	}
	page, err := (PageRequest{Limit: 5, MaxItems: 100, MaxBytes: 4096}).Normalize()
	if err != nil || page.Limit != 5 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	for _, value := range []ByteRange{{Offset: -1, Length: 1}, {Offset: 0, Length: 0}} {
		if err := value.Validate(); err == nil {
			t.Fatalf("invalid range accepted: %+v", value)
		}
	}
}

func TestContractsTreeShape(t *testing.T) {
	var _ Tree = NewLocalTree()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewLocalTree().List(ctx, Root{}, RootLocator(), ProviderPolicy, PageRequest{Limit: 1, MaxItems: 1, MaxBytes: 1}); err == nil {
		t.Fatal("canceled operation unexpectedly succeeded")
	}
}

func TestFileAccessProductionHasNoDatabaseDomainOrLegacyBypassDependency(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		source := string(content)
		for _, forbidden := range []string{
			"gorm.io/gorm",
			"/internal/model",
			"/internal/backupasset",
			"/internal/task",
			"FILE_BROWSER_ALLOW_ALL",
		} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("fileaccess production file %s contains forbidden dependency %q", file, forbidden)
			}
		}
	}
}
