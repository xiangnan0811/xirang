package requiredpostgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRunnerFakeProcess(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$GOFLAGS" != "" ] || [ "$REQUIRE_POSTGRES_LEASE_TEST" != 1 ] || [ "$REQUIRE_POSTGRES_REPOSITORY_TEST" != 1 ] || [ "$REQUIRE_POSTGRES_PRODUCER_RETENTION_TEST" != 1 ]; then exit 7; fi
case "$*" in *' -list '*) printf '%s\n' TestFixture; exit 0;; esac
case "$*" in *' -timeout=37m'*) : ;; *) exit 8;; esac
if [ "$FAKE_EXIT" = 1 ]; then
  printf '%s\n' '{"Action":"run","Package":"xirang/backend/internal/fixture","Test":"TestFixture"}' '{"Action":"run","Package":"xirang/backend/internal/fixture","Test":"TestFixture/postgres"}' '{"Action":"fail","Package":"xirang/backend/internal/fixture","Test":"TestFixture/postgres"}' '{"Action":"fail","Package":"xirang/backend/internal/fixture","Test":"TestFixture"}' '{"Action":"fail","Package":"xirang/backend/internal/fixture"}'
  echo "diagnostic $TEST_POSTGRES_DSN" >&2
  exit 42
fi
printf '%s\n' '{"Action":"run","Package":"xirang/backend/internal/fixture","Test":"TestFixture"}' '{"Action":"run","Package":"xirang/backend/internal/fixture","Test":"TestFixture/postgres"}' '{"Action":"pass","Package":"xirang/backend/internal/fixture","Test":"TestFixture/postgres"}' '{"Action":"pass","Package":"xirang/backend/internal/fixture","Test":"TestFixture"}' '{"Action":"pass","Package":"xirang/backend/internal/fixture"}'
`
	goPath := filepath.Join(bin, "go")
	if err := os.WriteFile(goPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOFLAGS", "-run=wrong")
	t.Setenv("TEST_POSTGRES_DSN", "postgres://fake.invalid/example")
	t.Setenv("POSTGRES_TEST_TIMEOUT", "37m")
	t.Setenv("REQUIRE_POSTGRES_LEASE_TEST", "wrong")
	t.Setenv("FAKE_EXIT", "0")
	inv := Inventory{Version: 1, Groups: []Group{{Package: "./internal/fixture", Timeout: "20m", Tests: []Test{{Name: "TestFixture", RequiredPaths: []string{"TestFixture/postgres"}}}}}}
	if err := ValidateNative(inv); err != nil {
		t.Fatal(err)
	}
	evidenceDir := filepath.Join(t.TempDir(), "evidence")
	if err := Run(inv, evidenceDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(evidenceDir, "001.summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"exit": 0`) {
		t.Fatalf("unexpected summary: %s", data)
	}
	t.Setenv("FAKE_EXIT", "1")
	failedEvidence := filepath.Join(t.TempDir(), "failed")
	if err := Run(inv, failedEvidence); err == nil {
		t.Fatal("nonzero process exit accepted")
	}
	summary, err := os.ReadFile(filepath.Join(failedEvidence, "001.summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var failedResult evidence
	if err := json.Unmarshal(summary, &failedResult); err != nil {
		t.Fatal(err)
	}
	if failedResult.Exit != 42 || failedResult.RequiredFailed != 1 || failedResult.PackagePass || len(failedResult.Errors) == 0 {
		t.Fatalf("incomplete failure summary: %s", summary)
	}
	stderr, err := os.ReadFile(filepath.Join(failedEvidence, "001.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stderr), os.Getenv("TEST_POSTGRES_DSN")) {
		t.Fatal("DSN leaked to stderr evidence")
	}
}

func TestEventWriterSanitizesOutput(t *testing.T) {
	var output strings.Builder
	w := &eventWriter{writer: &output}
	for _, chunk := range []string{`{"Action":"output","Package":"xirang/backend/internal/fixture","Output":"postgres://sec`, `ret"}`, "\n"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.finish(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret") || !strings.Contains(output.String(), "[redacted]") {
		t.Fatalf("unsanitized output: %s", output.String())
	}
}

func TestEventWriterSourceLocations(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "fixture")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte("one\ntwo\nthree"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "fixture_test.go"), filepath.Join(dir, "link_test.go")); err != nil {
		t.Fatal(err)
	}
	lines := sourceLines(root, "./internal/fixture")
	const canary = "FAKE_OUTPUT_SECRET_FOR_TEST_ONLY"
	tests := []struct {
		name    string
		output  any
		want    string
		secrets []string
	}{
		{"valid with secret", "    fixture_test.go:2: " + canary + "\n", "fixture_test.go:2: [redacted]\n", []string{canary}},
		{"mixed first valid", "unknown.go:1: ignored\n\tfixture_test.go:3: " + canary + "\nfixture_test.go:1: later", "fixture_test.go:3: [redacted]\n", []string{canary}},
		{"unknown", "unknown.go:1: " + canary, "[redacted]", nil},
		{"traversal", "../fixture_test.go:1: " + canary, "[redacted]", nil},
		{"absolute", "/tmp/fixture_test.go:1: " + canary, "[redacted]", nil},
		{"windows path", `C:\fixture_test.go:1: ` + canary, "[redacted]", nil},
		{"symlink", "link_test.go:1: " + canary, "[redacted]", nil},
		{"missing source", "fixture_test.go:1: " + canary, "[redacted]", nil},
		{"negative", "fixture_test.go:-1: " + canary, "[redacted]", nil},
		{"zero", "fixture_test.go:0: " + canary, "[redacted]", nil},
		{"noninteger", "fixture_test.go:x: " + canary, "[redacted]", nil},
		{"overflow", "fixture_test.go:999999999999999999999999999999: " + canary, "[redacted]", nil},
		{"overlong leading zeros", "fixture_test.go:000000000000000000001: " + canary, "[redacted]", nil},
		{"out of bounds", "fixture_test.go:4: " + canary, "[redacted]", nil},
		{"not at line start", "prefix fixture_test.go:1: " + canary, "[redacted]", nil},
		{"number output", 123, "[redacted]", nil},
		{"null output", nil, "[redacted]", nil},
		{"object output", map[string]string{"secret": canary}, "[redacted]", nil},
		{"secret in location", "fixture_test.go:2: message", "[redacted]_test.go:[redacted]: [redacted]\n", []string{"fixture", "2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			w := &eventWriter{writer: file, lines: lines, secrets: tc.secrets}
			if tc.name == "missing source" {
				w.lines = sourceLines(root, "./internal/missing")
			}
			encoded, err := json.Marshal(map[string]any{"Action": "output", "Output": tc.output, "Extra": canary})
			if err != nil {
				t.Fatal(err)
			}
			encoded = append(encoded, '\n')
			// Split an Output canary across writes when present.
			split := strings.LastIndex(string(encoded), canary)
			if split >= 0 {
				split += len(canary) / 2
			} else {
				split = len(encoded) / 2
			}
			for _, chunk := range [][]byte{encoded[:split], encoded[split:]} {
				if _, err := w.Write(chunk); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.finish(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var event map[string]any
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			if event["Output"] != tc.want || len(event) != 2 || strings.Contains(string(data), canary) {
				t.Fatalf("unexpected persisted event: %s", data)
			}
		})
	}
	if got := sourceLines(root, "./internal/missing"); len(got) != 0 {
		t.Fatalf("missing source admitted: %v", got)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.go"), []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "internal", "outside")); err != nil {
		t.Fatal(err)
	}
	if got := sourceLines(root, "./internal/outside"); len(got) != 0 {
		t.Fatalf("outside source admitted: %v", got)
	}
}

func TestEventWriterRejectsInvalidEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := &eventWriter{writer: file}
	if _, err := w.Write([]byte("{\"Output\":\"FAKE_SECRET_FOR_TEST_ONLY\"\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(strings.Repeat("x", 4*1024*1024+1))); err == nil {
		t.Fatal("oversized event accepted")
	}
	if err := w.finish(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "invalid event\n" || !w.failed {
		t.Fatalf("unexpected invalid event evidence: %q", data)
	}
	if result := parseEvents(strings.NewReader(string(data)), Group{Package: "./internal/fixture"}); len(result.Errors) == 0 {
		t.Fatal("malformed evidence accepted")
	}
}

func TestEvidenceRedactionAndOversize(t *testing.T) {
	var output strings.Builder
	w := &stderrWriter{writer: &output, secrets: []string{"postgres://user:secret@host/db", "secret"}}
	for _, chunk := range []string{"message postgres://user:se", "cret@host/db and secret\n"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "postgres://") {
		t.Fatalf("credential leaked: %q", output.String())
	}
	events := &eventWriter{writer: &output}
	if _, err := events.Write([]byte(strings.Repeat("x", 4*1024*1024+1))); err == nil {
		t.Fatal("oversized event accepted")
	}
}

func TestSelectorIsExact(t *testing.T) {
	re, err := regexp.Compile(selector(Group{Tests: []Test{{Name: "TestOne"}, {Name: "TestOther"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("TestOne") || !re.MatchString("TestOther") || re.MatchString("TestOneExtra") || re.MatchString("OtherTestOne") {
		t.Fatal("selector is not an exact union")
	}
}
