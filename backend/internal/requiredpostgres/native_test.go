package requiredpostgres

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeDeclarationsArePackageQualified(t *testing.T) {
	bin := t.TempDir()
	fake := `#!/bin/sh
case "$2" in
 ./internal/one) printf '%s\n' TestSame TestExcluded ;;
 ./internal/two) printf '%s\n' TestDifferent ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOFLAGS", "-run=hostile")
	inv := Inventory{Groups: []Group{{Package: "./internal/one", Tests: []Test{{Name: "TestSame"}}}, {Package: "./internal/two", Tests: []Test{{Name: "TestSame"}}}}, Exclusions: []Exclusion{{Package: "./internal/one", Name: "TestExcluded", Reason: "fixture"}}}
	if err := ValidateNative(inv); err == nil {
		t.Fatal("same name in a different package satisfied missing declaration")
	}
	inv.Groups[1].Tests[0].Name = "TestDifferent"
	if err := ValidateNative(inv); err != nil {
		t.Fatal(err)
	}
	inv.Exclusions[0].Name = "TestMissing"
	if err := ValidateNative(inv); err == nil {
		t.Fatal("missing excluded declaration accepted")
	}
}
