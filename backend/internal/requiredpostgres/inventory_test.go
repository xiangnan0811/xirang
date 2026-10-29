package requiredpostgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInventoryValidation(t *testing.T) {
	cases := []struct {
		name, json string
		valid      bool
	}{
		{"valid", `{"version":1,"groups":[{"package":"./internal/database","timeout":"30m","tests":[{"name":"TestOne","required_paths":["TestOne/postgres"]}]}],"exclusions":[]}`, true},
		{"duplicate key", `{"version":1,"version":1,"groups":[],"exclusions":[]}`, false},
		{"unknown field", `{"version":1,"groups":[],"exclusions":[],"extra":1}`, false},
		{"missing required paths", `{"version":1,"groups":[{"package":"./internal/database","timeout":"30m","tests":[{"name":"TestOne"}]}],"exclusions":[]}`, false},
		{"duplicate path", `{"version":1,"groups":[{"package":"./internal/database","timeout":"30m","tests":[{"name":"TestOne","required_paths":["TestOne/postgres","TestOne/postgres"]}]}],"exclusions":[]}`, false},
		{"invalid timeout", `{"version":1,"groups":[{"package":"./internal/database","timeout":"0","tests":[{"name":"TestOne","required_paths":[]}]}],"exclusions":[]}`, false},
		{"missing exclusions", `{"version":1,"groups":[{"package":"./internal/database","timeout":"20m","tests":[{"name":"TestOne","required_paths":[]}]}]}`, false},
		{"bad path", `{"version":1,"groups":[{"package":"./internal/database","timeout":"20m","tests":[{"name":"TestOne","required_paths":["TestOther/postgres"]}]}],"exclusions":[]}`, false},
		{"duplicate test", `{"version":1,"groups":[{"package":"./internal/database","timeout":"20m","tests":[{"name":"TestOne","required_paths":[]},{"name":"TestOne","required_paths":[]}]}],"exclusions":[]}`, false},
		{"duplicate exclusion", `{"version":1,"groups":[{"package":"./internal/database","timeout":"20m","tests":[{"name":"TestOne","required_paths":[]}]}],"exclusions":[{"package":"./internal/database","name":"TestOne","reason":"no"}]}`, false},
		{"unknown version", `{"version":2,"groups":[{"package":"./internal/database","timeout":"20m","tests":[{"name":"TestOne","required_paths":[]}]}],"exclusions":[]}`, false},
		{"trailing JSON", `{"version":1,"groups":[],"exclusions":[]} {}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "inventory.json")
			if err := os.WriteFile(p, []byte(tc.json), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestEventProtocol(t *testing.T) {
	g := Group{Package: "./internal/database", Tests: []Test{{Name: "TestOne", RequiredPaths: []string{"TestOne/postgres"}}}}
	event := func(action, name string) string {
		return `{"Action":"` + action + `","Package":"xirang/backend/internal/database","Test":"` + name + `"}` + "\n"
	}
	good := event("run", "TestOne") + event("run", "TestOne/postgres") + event("pass", "TestOne/postgres") + event("pass", "TestOne") + event("pass", "")
	cases := []struct {
		name, data string
		valid      bool
	}{
		{"pass", good, true}, {"missing child", event("run", "TestOne") + event("pass", "TestOne") + event("pass", ""), false},
		{"descendant skip", good + event("skip", "TestOne/postgres/scenario"), false}, {"invalid json", good + "not json\n", false},
		{"wrong package", strings.Replace(good, "xirang/backend/internal/database", "xirang/backend/internal/other", 1), false}, {"empty", "", false},
		{"terminal before run", event("pass", "TestOne") + event("run", "TestOne") + event("run", "TestOne/postgres") + event("pass", "TestOne/postgres") + event("pass", ""), false},
		{"optional skip", event("run", "TestOne") + event("run", "TestOne/sqlite") + event("skip", "TestOne/sqlite") + event("run", "TestOne/postgres") + event("pass", "TestOne/postgres") + event("pass", "TestOne") + event("pass", ""), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := parseEvents(strings.NewReader(tc.data), g)
			if (len(result.Errors) == 0) != tc.valid {
				t.Fatalf("errors: %v", result.Errors)
			}
		})
	}
	leafGroup := Group{Package: "./internal/database", Tests: []Test{{Name: "TestOne", RequiredPaths: []string{"TestOne/postgres/scenario"}}}}
	leafEvents := event("run", "TestOne") + event("run", "TestOne/postgres/scenario") + event("pass", "TestOne/postgres/scenario") + event("pass", "TestOne") + event("pass", "")
	if got := parseEvents(strings.NewReader(leafEvents), leafGroup); len(got.Errors) != 0 {
		t.Fatalf("slash-containing subtest must not require synthetic parent: %v", got.Errors)
	}
}
