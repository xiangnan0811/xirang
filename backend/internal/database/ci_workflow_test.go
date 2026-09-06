package database

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type ciWorkflow struct {
	Jobs map[string]ciWorkflowJob `yaml:"jobs"`
}

type ciWorkflowJob struct {
	Defaults ciWorkflowDefaults `yaml:"defaults"`
	Env      map[string]string  `yaml:"env"`
	Steps    []ciWorkflowStep   `yaml:"steps"`
}

type ciWorkflowDefaults struct {
	Run ciWorkflowRunDefaults `yaml:"run"`
}

type ciWorkflowRunDefaults struct {
	WorkingDirectory string `yaml:"working-directory"`
}

type ciWorkflowStep struct {
	Name             string `yaml:"name"`
	Run              string `yaml:"run"`
	WorkingDirectory string `yaml:"working-directory"`
}

func TestBackendCIRaceGateIncludesConcurrentStatePackages(t *testing.T) {
	workflowPath := filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml")
	contents, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}

	var workflow ciWorkflow
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatalf("parse CI workflow YAML: %v", err)
	}
	backendJob, ok := workflow.Jobs["backend"]
	if !ok {
		t.Fatal("CI workflow has no active backend job")
	}

	requiredPackages := []string{
		"./internal/backupasset/...",
		"./internal/api/handlers/",
		"./internal/task",
		"./internal/ws",
	}
	var raceStep *ciWorkflowStep
	var raceArgs []string
	for index := range backendJob.Steps {
		step := &backendJob.Steps[index]
		args, ok := activeGoRaceTestArgs(step.Run)
		if !ok {
			continue
		}
		if raceStep != nil {
			t.Fatalf("backend job has multiple active go test -race steps: %q and %q", raceStep.Name, step.Name)
		}
		raceStep = step
		raceArgs = args
	}
	if raceStep == nil {
		t.Fatal("backend job has no active go test -race step")
	}

	workingDirectory := raceStep.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = backendJob.Defaults.Run.WorkingDirectory
	}
	if filepath.Clean(workingDirectory) != "backend" {
		t.Fatalf("backend race step working-directory=%q, want backend", workingDirectory)
	}

	packageSet := make(map[string]struct{})
	for _, arg := range raceArgs {
		if strings.HasPrefix(arg, "./") {
			packageSet[arg] = struct{}{}
		}
	}
	if len(packageSet) == 0 {
		t.Fatal("backend go test -race command has an empty package selection")
	}
	for _, packagePath := range requiredPackages {
		if _, ok := packageSet[packagePath]; !ok {
			t.Errorf("backend go test -race command omits %s", packagePath)
		}
	}
}

const requiredPostgresTestDSN = "postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:5432/xirang_test?sslmode=disable"

type requiredPostgresWorkflowInvocation struct {
	suiteLabel       string
	packagePath      string
	selector         string
	workingDirectory string
}

func TestPostgresMigrationCIHasRequiredLifecycleSlices(t *testing.T) {
	workflowPath := filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml")
	contents, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}

	var workflow ciWorkflow
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatalf("parse CI workflow YAML: %v", err)
	}
	job, ok := workflow.Jobs["postgres-migration"]
	if !ok {
		t.Fatal("CI workflow has no postgres-migration job")
	}
	if got := job.Env["TEST_POSTGRES_DSN"]; got != requiredPostgresTestDSN {
		t.Fatalf("postgres-migration TEST_POSTGRES_DSN=%q, want %q", got, requiredPostgresTestDSN)
	}

	want := []requiredPostgresWorkflowInvocation{
		{
			suiteLabel:  "PostgreSQL migration parity",
			packagePath: "./internal/database",
			selector:    "^(Test.*Migration.*Postgres.*|TestPostgresTimestamptzScanUsesConfiguredUTC)$",
		},
		{
			suiteLabel:  "PostgreSQL lifecycle migration contract",
			packagePath: "./internal/database",
			selector:    "^TestLifecycleEffectClaimAuditSlotMigrationPostgres(PristineDown|ClaimUsedDown|SlotUsedDown|Constraints|ClaimTransitionRebinding|UpgradeCutover)$",
		},
		{
			suiteLabel:  "PostgreSQL lifecycle retention contract",
			packagePath: "./internal/backupasset/retention",
		},
		{
			suiteLabel:  "PostgreSQL lifecycle runtime contract",
			packagePath: "./internal/backupasset/runtime",
			selector:    "^TestLifecycleSettledAuditDetailPurgePostgres$",
		},
	}
	got := activeRequiredPostgresInvocations(job)
	if len(got) != len(want) {
		t.Fatalf("postgres-migration has %d reusable required-test invocations, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].suiteLabel != want[index].suiteLabel ||
			got[index].packagePath != want[index].packagePath {
			t.Errorf("invocation %d = {%q %q %q}, want {%q %q %q}",
				index+1,
				got[index].suiteLabel, got[index].packagePath, got[index].selector,
				want[index].suiteLabel, want[index].packagePath, want[index].selector)
		}
		if want[index].selector != "" && got[index].selector != want[index].selector {
			t.Errorf("invocation %d selector = %q, want %q", index+1, got[index].selector, want[index].selector)
		}
		workingDirectory := got[index].workingDirectory
		if workingDirectory == "" {
			workingDirectory = job.Defaults.Run.WorkingDirectory
		}
		if filepath.Clean(workingDirectory) != "backend" {
			t.Errorf("invocation %d working-directory=%q, want backend", index+1, workingDirectory)
		}
	}

	assertLifecycleRetentionPostgresCoverage(t, got[2].selector)
	assertRequiredPostgresRunnerRejectsZeroMatches(t)
}

func assertLifecycleRetentionPostgresCoverage(t *testing.T, selector string) {
	t.Helper()
	if strings.TrimSpace(selector) == "" {
		t.Fatal("lifecycle retention invocation has an empty test selector")
	}

	const packagePath = "./internal/backupasset/retention"
	intended := listGoTestNames(t, packagePath, `^TestLifecycle.*Postgres$`)
	if len(intended) == 0 {
		t.Fatal("lifecycle retention acceptance package has no PostgreSQL tests")
	}
	selected := listGoTestNames(t, packagePath, selector)
	selectedSet := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		selectedSet[name] = struct{}{}
	}
	for _, name := range intended {
		if _, ok := selectedSet[name]; !ok {
			t.Errorf("lifecycle retention selector %q omits acceptance test %s", selector, name)
		}
	}
}

func listGoTestNames(t *testing.T, packagePath, selector string) []string {
	t.Helper()
	cmd := exec.Command("go", "test", packagePath, "-list", selector, "-count=1") //nolint:gosec // test invokes Go with fixed package and selector arguments
	cmd.Dir = filepath.Join("..", "..")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list Go tests for %s with selector %q: %v\n%s", packagePath, selector, err, output)
	}
	testName := regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)
	var names []string
	for _, line := range strings.Split(string(output), "\n") {
		if testName.MatchString(line) {
			names = append(names, line)
		}
	}
	return names
}

func activeRequiredPostgresInvocations(job ciWorkflowJob) []requiredPostgresWorkflowInvocation {
	var invocations []requiredPostgresWorkflowInvocation
	for _, step := range job.Steps {
		suiteLabel, packagePath, selector, ok := parseRequiredPostgresRunnerCommand(step.Run)
		if !ok {
			continue
		}
		invocations = append(invocations, requiredPostgresWorkflowInvocation{
			suiteLabel:       suiteLabel,
			packagePath:      packagePath,
			selector:         selector,
			workingDirectory: step.WorkingDirectory,
		})
	}
	return invocations
}

func parseRequiredPostgresRunnerCommand(run string) (suiteLabel, packagePath, selector string, ok bool) {
	const prefix = "bash ../scripts/run-required-postgres-tests.sh "
	remaining := strings.TrimSpace(run)
	if !strings.HasPrefix(remaining, prefix) {
		return "", "", "", false
	}
	remaining = strings.TrimPrefix(remaining, prefix)
	readQuoted := func(input string) (string, string, bool) {
		if !strings.HasPrefix(input, "'") {
			return "", "", false
		}
		end := strings.IndexByte(input[1:], '\'')
		if end < 0 {
			return "", "", false
		}
		end++
		value := input[1:end]
		rest := input[end+1:]
		if rest == "" {
			return value, rest, true
		}
		if !strings.HasPrefix(rest, " ") {
			return "", "", false
		}
		return value, strings.TrimPrefix(rest, " "), true
	}
	if suiteLabel, remaining, ok = readQuoted(remaining); !ok {
		return "", "", "", false
	}
	space := strings.IndexByte(remaining, ' ')
	if space <= 0 {
		return "", "", "", false
	}
	packagePath = remaining[:space]
	remaining = strings.TrimLeft(remaining[space:], " ")
	if selector, remaining, ok = readQuoted(remaining); !ok || remaining != "" {
		return "", "", "", false
	}
	return suiteLabel, packagePath, selector, true
}

func assertRequiredPostgresRunnerRejectsZeroMatches(t *testing.T) {
	t.Helper()
	fakeBin := t.TempDir()
	fakeGo := filepath.Join(fakeBin, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/usr/bin/env bash\nset -euo pipefail\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake go executable: %v", err)
	}

	runnerPath := filepath.Join("..", "..", "..", "scripts", "run-required-postgres-tests.sh")
	cmd := exec.Command("bash", runnerPath, "--list-only", "zero-match contract", "./internal/database", "^TestRequiredPostgresRunnerNoSuchTest$") //nolint:gosec // test invokes the checked-in runner with fixed arguments
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("required PostgreSQL runner accepted a zero-match selector; output:\n%s", output)
	}
	if !strings.Contains(string(output), "selected zero tests") {
		t.Fatalf("zero-match runner failure was not explicit:\n%s", output)
	}
}

func activeGoRaceTestArgs(run string) ([]string, bool) {
	for _, rawLine := range strings.Split(run, "\n") {
		fields := strings.Fields(strings.TrimSpace(rawLine))
		if len(fields) < 3 || fields[0] != "go" || fields[1] != "test" || !slices.Contains(fields[2:], "-race") {
			continue
		}
		return fields[2:], true
	}
	return nil, false
}
