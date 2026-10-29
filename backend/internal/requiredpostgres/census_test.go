package requiredpostgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCensusFollowsFunctionValuesAndAliases(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source := `package fixture
import (env "os"; pg "gorm.io/driver/postgres"; "testing")
func seed() { _ = env.Getenv("TEST_POSTGRES_DSN"); _ = pg.Open("") }
func helper(f func()) { f() }
func TestIndirect(t *testing.T) { helper(func() { seed() }) }
func TestFunctionValue(t *testing.T) { helper(seed) }
var seedAlias = seed
func TestPackageFunctionValue(t *testing.T) { seedAlias() }
func TestEngineLoop(t *testing.T) { for _, engine := range []string{"sqlite", "postgres"} { _ = engine } }
func TestEngineVariable(t *testing.T) { engines := []string{"sqlite", "postgres"}; for _, engine := range engines { _ = engine } }
func TestSQLiteOnly(t *testing.T) { engineHelper("sqlite") }
func engineHelper(engine string) { if engine == "postgres" { seed() } }
func TestDirectGate(t *testing.T) { _ = env.LookupEnv; _, _ = env.LookupEnv("TEST_POSTGRES_DSN") }
func TestNestedClosure(t *testing.T) { f := func() { func() { seed() }() }; f() }
`
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	dot := "package fixture\nimport (. \"os\"; \"testing\")\nfunc TestDotImport(t *testing.T) { _ = Getenv(`TEST_POSTGRES_DSN`) }\n"
	if err := os.WriteFile(filepath.Join(dir, "dot_test.go"), []byte(dot), 0600); err != nil {
		t.Fatal(err)
	}
	external := "package fixture_test\nimport \"testing\"\nfunc seed() {}\nfunc TestExternal(t *testing.T) { seed() }\n"
	if err := os.WriteFile(filepath.Join(dir, "external_test.go"), []byte(external), 0600); err != nil {
		t.Fatal(err)
	}
	inv := Inventory{Groups: []Group{{Package: "./internal/fixture", Tests: []Test{{Name: "TestIndirect"}, {Name: "TestFunctionValue"}}}}}
	err := Census(root, inv)
	if err == nil || !strings.Contains(err.Error(), "TestEngineLoop") || !strings.Contains(err.Error(), "TestEngineVariable") || !strings.Contains(err.Error(), "TestSQLiteOnly") || !strings.Contains(err.Error(), "TestDirectGate") || !strings.Contains(err.Error(), "TestNestedClosure") || !strings.Contains(err.Error(), "TestDotImport") || !strings.Contains(err.Error(), "TestPackageFunctionValue") || strings.Contains(err.Error(), "TestIndirect:") || strings.Contains(err.Error(), "TestFunctionValue:") || strings.Contains(err.Error(), "TestExternal:") {
		t.Fatalf("unexpected census result: %v", err)
	}
	inv.Groups[0].Tests = append(inv.Groups[0].Tests, Test{Name: "TestEngineLoop"}, Test{Name: "TestEngineVariable"}, Test{Name: "TestDirectGate"}, Test{Name: "TestNestedClosure"}, Test{Name: "TestDotImport"}, Test{Name: "TestPackageFunctionValue"})
	inv.Exclusions = []Exclusion{{Package: "./internal/fixture", Name: "TestSQLiteOnly", Reason: "call passes only sqlite to engineHelper"}}
	if err := Census(root, inv); err != nil {
		t.Fatal(err)
	}
}
