package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateRuntimeRootRejectsSymlinkSpecialAndForbiddenPaths(t *testing.T) {
	service := &Service{db: newRepositoryTestDB(t)}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(base, "regular")
	if err := os.WriteFile(regular, []byte("not-a-root"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]string{
		"relative":           "derived",
		"unclean":            base + "/x/../derived",
		"filesystem root":    string(filepath.Separator),
		"forbidden ancestor": "/data/private-derived",
		"symlink component":  filepath.Join(link, "derived"),
		"regular file":       regular,
	} {
		t.Run(name, func(t *testing.T) {
			if err := service.ValidatePrivateRuntimeRoot(context.Background(), candidate); !errors.Is(err, ErrUnsafePrivateRuntimeRoot) {
				t.Fatalf("ValidatePrivateRuntimeRoot(%q)=%v", candidate, err)
			}
		})
	}
}

func TestPrivateRuntimeRootAcceptsDedicatedNonexistentDirectory(t *testing.T) {
	service := &Service{db: newRepositoryTestDB(t)}
	candidate := filepath.Join(t.TempDir(), "asset-runtime", "derived")
	if err := service.ValidatePrivateRuntimeRoot(context.Background(), candidate); err != nil {
		t.Fatalf("safe private runtime root rejected: %v", err)
	}
}
