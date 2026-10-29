package requiredpostgres

import (
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
printf '%s\n' '{"Action":"run","Package":"xirang/backend/internal/fixture","Test":"TestFixture"}' '{"Action":"run","Package":"xirang/backend/internal/fixture","Test":"TestFixture/postgres"}' '{"Action":"pass","Package":"xirang/backend/internal/fixture","Test":"TestFixture/postgres"}' '{"Action":"pass","Package":"xirang/backend/internal/fixture","Test":"TestFixture"}' '{"Action":"pass","Package":"xirang/backend/internal/fixture"}'
if [ "$FAKE_EXIT" = 1 ]; then echo "diagnostic $TEST_POSTGRES_DSN" >&2; exit 42; fi
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
	evidence := filepath.Join(t.TempDir(), "evidence")
	if err := Run(inv, evidence); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(evidence, "001.summary.json"))
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
	if !strings.Contains(string(summary), `"exit": 42`) {
		t.Fatalf("missing failure evidence: %s", summary)
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
