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
	t.Run("declared command packages", func(t *testing.T) {
		root := t.TempDir()
		files := map[string]string{
			"internal/fixture/fixture_test.go": `package fixture
import "testing"
func TestInternal(t *testing.T) {}`,
			"cmd/TestTools/helper/helper_test.go": `package main
import "testing"
func TestEvidence(t *testing.T) {}
func TestOther(t *testing.T) {}`,
			"cmd/TestTools/helper/nested/nested_test.go": `package main
import "testing"
func TestNested(t *testing.T) {}`,
			"cmd/unrelated/unrelated_test.go": `package main
import "testing"
func TestUnrelatedPostgres(t *testing.T) {}`,
		}
		for name, source := range files {
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cases := []struct {
			name, inventory, want string
		}{
			{"exclusions", `{"version":1,"groups":[{"package":"./internal/fixture","timeout":"1m","tests":[{"name":"TestInternal","required_paths":[]}]}],"exclusions":[{"package":"./cmd/TestTools/helper","name":"TestEvidence","reason":"non-PG"},{"package":"./cmd/TestTools/helper","name":"TestOther","reason":"non-PG"}]}`, ""},
			{"group and exclusion", `{"version":1,"groups":[{"package":"./cmd/TestTools/helper","timeout":"1m","tests":[{"name":"TestEvidence","required_paths":[]}]}],"exclusions":[{"package":"./cmd/TestTools/helper","name":"TestOther","reason":"non-PG"}]}`, ""},
			{"missing declaration", `{"version":1,"groups":[{"package":"./internal/fixture","timeout":"1m","tests":[{"name":"TestInternal","required_paths":[]}]}],"exclusions":[{"package":"./cmd/TestTools/helper","name":"TestEvidence","reason":"non-PG"}]}`, "./cmd/TestTools/helper/TestOther: unregistered command test"},
			{"extra declaration", `{"version":1,"groups":[{"package":"./cmd/TestTools/helper","timeout":"1m","tests":[{"name":"TestEvidence","required_paths":[]},{"name":"TestMissing","required_paths":[]}]}],"exclusions":[{"package":"./cmd/TestTools/helper","name":"TestOther","reason":"non-PG"}]}`, "./cmd/TestTools/helper/TestMissing: declared command test missing"},
			{"missing package", `{"version":1,"groups":[{"package":"./internal/fixture","timeout":"1m","tests":[{"name":"TestInternal","required_paths":[]}]}],"exclusions":[{"package":"./cmd/missing","name":"TestEvidence","reason":"non-PG"}]}`, "cmd/missing"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "inventory.json")
				if err := os.WriteFile(path, []byte(tc.inventory), 0600); err != nil {
					t.Fatal(err)
				}
				inv, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				err = Census(root, inv)
				if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
					t.Fatalf("Census error = %v, want %q", err, tc.want)
				}
			})
		}
	})
}
