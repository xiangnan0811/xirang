package database

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"xirang/backend/internal/requiredpostgres"

	"gopkg.in/yaml.v3"
)

type ciWorkflow struct {
	Jobs map[string]ciWorkflowJob `yaml:"jobs"`
}

type ciWorkflowJob struct {
	Defaults ciWorkflowDefaults `yaml:"defaults"`
	If       string             `yaml:"if"`
	Env      map[string]string  `yaml:"env"`
	Services map[string]struct {
		Image string `yaml:"image"`
	} `yaml:"services"`
	Steps []ciWorkflowStep `yaml:"steps"`
}

type ciWorkflowDefaults struct {
	Run ciWorkflowRunDefaults `yaml:"run"`
}

type ciWorkflowRunDefaults struct {
	WorkingDirectory string `yaml:"working-directory"`
}

type ciWorkflowStep struct {
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	WorkingDirectory string            `yaml:"working-directory"`
	If               string            `yaml:"if"`
	Uses             string            `yaml:"uses"`
	With             map[string]string `yaml:"with"`
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

const requiredPostgresRunnerCommand = `bash ../scripts/run-required-postgres-tests.sh --evidence-dir "${{ runner.temp }}/required-postgres-evidence"`

var requiredPostgresEnvironment = []string{
	"REQUIRE_POSTGRES_MIGRATION_TEST", "REQUIRE_POSTGRES_RECOVERY_TEST",
	"REQUIRE_POSTGRES_CATALOG_TEST", "REQUIRE_POSTGRES_SEARCH_TEST",
	"REQUIRE_POSTGRES_OVERLAY_TEST", "REQUIRE_POSTGRES_CONTENT_TEST",
	"REQUIRE_POSTGRES_PROCESSING_TEST", "REQUIRE_POSTGRES_EXPORT_TEST",
	"REQUIRE_POSTGRES_GENERAL_TEST", "REQUIRE_POSTGRES_TASK_TERMINAL_TEST",
	"REQUIRE_POSTGRES_ESCALATION_PREVIEW_TEST", "REQUIRE_POSTGRES_LEASE_TEST",
	"REQUIRE_POSTGRES_REPOSITORY_TEST", "REQUIRE_POSTGRES_PRODUCER_RETENTION_TEST",
}

func loadPostgresWorkflowJob(t *testing.T) ciWorkflowJob {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	var workflow ciWorkflow
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	job, ok := workflow.Jobs["postgres-migration"]
	if !ok {
		t.Fatal("CI workflow has no postgres-migration job")
	}
	return job
}

func validateRequiredPostgresWorkflow(job ciWorkflowJob) error {
	if job.If != "" || job.Env["TEST_POSTGRES_DSN"] != requiredPostgresTestDSN {
		return fmt.Errorf("postgres job is conditional or has an unexpected test DSN")
	}
	if job.Services["postgres"].Image == "" {
		return fmt.Errorf("postgres service is missing")
	}
	for _, key := range requiredPostgresEnvironment {
		if job.Env[key] != "1" {
			return fmt.Errorf("required environment %s is not enabled", key)
		}
	}
	goTest := regexp.MustCompile(`\bgo[[:space:]]+test\b`)
	runners, selftests, uploads, backups := 0, 0, 0, 0
	for _, step := range job.Steps {
		command := strings.TrimSpace(step.Run)
		activeLines := make([]string, 0)
		for _, line := range strings.Split(command, "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				activeLines = append(activeLines, line)
			}
		}
		active := strings.Join(activeLines, "\n")
		if goTest.MatchString(active) || strings.Contains(active, "cmd/required-postgres-tests") {
			return fmt.Errorf("unrecognized direct Go test entry in step %q", step.Name)
		}
		if strings.Contains(active, "run-required-postgres-tests.sh") {
			if active != requiredPostgresRunnerCommand || step.If != "" {
				return fmt.Errorf("unrecognized or conditional PostgreSQL runner in step %q", step.Name)
			}
			dir := step.WorkingDirectory
			if dir == "" {
				dir = job.Defaults.Run.WorkingDirectory
			}
			if filepath.Clean(dir) != "backend" {
				return fmt.Errorf("PostgreSQL runner working-directory is %q", dir)
			}
			runners++
		}
		if strings.Contains(active, "run-required-postgres-tests.test.sh") {
			if active != "bash ../scripts/run-required-postgres-tests.test.sh" || step.If != "" {
				return fmt.Errorf("unrecognized or conditional runner selftest in step %q", step.Name)
			}
			selftests++
		}
		if strings.Contains(active, "postgres-backup-restore.integration.test.sh") {
			if step.If != "" {
				return fmt.Errorf("backup/restore step is conditional")
			}
			backups++
		}
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") &&
			step.With["path"] == "${{ runner.temp }}/required-postgres-evidence" {
			if step.If != "${{ always() }}" || step.With["if-no-files-found"] != "error" {
				return fmt.Errorf("PostgreSQL evidence upload is not fail-closed")
			}
			uploads++
		}
	}
	if runners != 1 || selftests != 1 || uploads != 1 || backups != 1 {
		return fmt.Errorf("postgres job requires one inventory runner, protocol selftest, evidence upload, and backup/restore step; got %d/%d/%d/%d", runners, selftests, uploads, backups)
	}
	return nil
}

func TestPostgresMigrationCIHasRequiredLifecycleSlices(t *testing.T) {
	job := loadPostgresWorkflowJob(t)
	if err := validateRequiredPostgresWorkflow(job); err != nil {
		t.Fatal(err)
	}
	const retentionPackage = "./internal/backupasset/retention"
	intended := listGoTestNames(t, retentionPackage, `^TestLifecycle.*Postgres$`)
	if len(intended) == 0 {
		t.Fatal("lifecycle retention package has no PostgreSQL tests")
	}
	assertRequiredInventoryTests(t, retentionPackage, intended...)
	assertRequiredInventoryTests(t, "./internal/backupasset/runtime", "TestLifecycleSettledAuditDetailPurgePostgres")
}

func TestRequiredPostgresWorkflowRejectsBypasses(t *testing.T) {
	base := loadPostgresWorkflowJob(t)
	for _, tc := range []struct {
		name   string
		mutate func(*ciWorkflowJob)
	}{
		{"direct-go-test", func(job *ciWorkflowJob) {
			job.Steps = append(job.Steps, ciWorkflowStep{Run: "env GOFLAGS='' go test ./internal/database"})
		}},
		{"second-runner", func(job *ciWorkflowJob) {
			job.Steps = append(job.Steps, ciWorkflowStep{Run: requiredPostgresRunnerCommand})
		}},
		{"positional-runner", func(job *ciWorkflowJob) {
			job.Steps = append(job.Steps, ciWorkflowStep{Run: "bash ../scripts/run-required-postgres-tests.sh suite ./internal/database TestOne"})
		}},
		{"disabled-require", func(job *ciWorkflowJob) { job.Env["REQUIRE_POSTGRES_LEASE_TEST"] = "0" }},
		{"conditional-runner", func(job *ciWorkflowJob) {
			for i := range job.Steps {
				if job.Steps[i].Run == requiredPostgresRunnerCommand {
					job.Steps[i].If = "false"
				}
			}
		}},
		{"missing-runner", func(job *ciWorkflowJob) {
			for i := range job.Steps {
				if job.Steps[i].Run == requiredPostgresRunnerCommand {
					job.Steps[i].Run = "# disabled"
				}
			}
		}},
		{"missing-evidence", func(job *ciWorkflowJob) {
			for i := range job.Steps {
				if job.Steps[i].With["path"] == "${{ runner.temp }}/required-postgres-evidence" {
					job.Steps[i].Uses = ""
				}
			}
		}},
		{"conditional-backup", func(job *ciWorkflowJob) {
			for i := range job.Steps {
				if strings.Contains(job.Steps[i].Run, "postgres-backup-restore.integration.test.sh") {
					job.Steps[i].If = "false"
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := base
			job.Steps = append([]ciWorkflowStep(nil), base.Steps...)
			job.Env = make(map[string]string, len(base.Env))
			for key, value := range base.Env {
				job.Env[key] = value
			}
			tc.mutate(&job)
			if err := validateRequiredPostgresWorkflow(job); err == nil {
				t.Fatal("invalid PostgreSQL workflow accepted")
			}
		})
	}
}

func assertRequiredInventoryTests(t *testing.T, packagePath string, names ...string) {
	t.Helper()
	inventoryPath := filepath.Join("..", "..", "..", "scripts", "required-postgres-tests.json")
	inventory, err := requiredpostgres.Load(inventoryPath)
	if err != nil {
		t.Fatalf("load required PostgreSQL inventory: %v", err)
	}
	selected := map[string]requiredpostgres.Test{}
	for _, group := range inventory.Groups {
		if group.Package == packagePath {
			for _, test := range group.Tests {
				selected[test.Name] = test
			}
		}
	}
	native := map[string]bool{}
	for _, name := range listGoTestNames(t, packagePath, "^Test") {
		native[name] = true
	}
	for _, name := range names {
		test, ok := selected[name]
		if !ok || !slices.Contains(test.RequiredPaths, name) {
			t.Errorf("required PostgreSQL inventory omits direct required test %s/%s", packagePath, name)
		}
		if !native[name] {
			t.Errorf("native Go listing omits %s/%s", packagePath, name)
		}
	}
}

func listGoTestNames(t *testing.T, packagePath, selector string) []string {
	t.Helper()
	cmd := exec.Command("go", "test", packagePath, "-list", selector, "-count=1") //nolint:gosec // package and selector come from fixed test declarations
	cmd.Dir = filepath.Join("..", "..")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GOFLAGS=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list Go tests for %s: %v\n%s", packagePath, err, output)
	}
	pattern := regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)
	var names []string
	for _, line := range strings.Split(string(output), "\n") {
		if pattern.MatchString(line) {
			names = append(names, line)
		}
	}
	return names
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
