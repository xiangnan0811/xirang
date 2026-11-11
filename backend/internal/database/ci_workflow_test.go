package database

import (
	"fmt"
	"maps"
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
	Name           string             `yaml:"name"`
	Needs          []string           `yaml:"needs"`
	Defaults       ciWorkflowDefaults `yaml:"defaults"`
	If             string             `yaml:"if"`
	Env            map[string]string  `yaml:"env"`
	Permissions    map[string]string  `yaml:"permissions"`
	Outputs        map[string]string  `yaml:"outputs"`
	TimeoutMinutes int                `yaml:"timeout-minutes"`
	Services       map[string]struct {
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
	Env              map[string]string `yaml:"env"`
}

func loadCIWorkflow(t *testing.T) ciWorkflow {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	var workflow ciWorkflow
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatalf("parse CI workflow YAML: %v", err)
	}
	return workflow
}

func TestBackendCIRaceGateIncludesConcurrentStatePackages(t *testing.T) {
	workflow := loadCIWorkflow(t)
	if err := validateProtectedSourceWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	base := workflow
	for _, protected := range []struct{ id, name string }{
		{"backend", "Backend Test & Build"},
		{"frontend", "Frontend Test & Build"},
		{"doc-freshness", "Doc Freshness Check"},
	} {
		t.Run(protected.id, func(t *testing.T) {
			checkout := slices.IndexFunc(base.Jobs[protected.id].Steps, func(step ciWorkflowStep) bool {
				return strings.HasPrefix(step.Uses, "actions/checkout@")
			})
			if checkout < 0 {
				t.Fatal("base workflow has no checkout")
			}
			ordinary := slices.IndexFunc(base.Jobs[protected.id].Steps, func(step ciWorkflowStep) bool {
				return step.If == protectedStepGate
			})
			if ordinary < 0 {
				t.Fatal("base workflow has no ordinary source step")
			}
			metadataCheckout := slices.IndexFunc(base.Jobs[protected.id].Steps, func(step ciWorkflowStep) bool { return step.Name == "Checkout metadata verifier" })
			verifier := slices.IndexFunc(base.Jobs[protected.id].Steps, func(step ciWorkflowStep) bool { return step.Name == "Verify source check" })
			if metadataCheckout < 0 || verifier < 0 {
				t.Fatal("metadata verifier steps missing")
			}
			tests := []struct {
				name   string
				mutate func(*ciWorkflowJob)
			}{
				{"job-if-missing", func(job *ciWorkflowJob) { job.If = "" }},
				{"job-if-false", func(job *ciWorkflowJob) { job.If = "false" }},
				{"old-source-only-gate", func(job *ciWorkflowJob) { job.If = requiredPostgresSourceGate }},
				{"missing-classify", func(job *ciWorkflowJob) { job.Needs = nil }},
				{"extra-need", func(job *ciWorkflowJob) { job.Needs = []string{"classify", "other"} }},
				{"wrong-source-name", func(job *ciWorkflowJob) { job.Name = "wrong" }},
				{"mode-only-name", func(job *ciWorkflowJob) {
					job.Name = "${{ needs.classify.outputs.mode == 'metadata' && 'Metadata only / " + protected.name + "' || '" + protected.name + "' }}"
				}},
				{"empty-steps", func(job *ciWorkflowJob) { job.Steps = nil }},
				{"step-if-missing", func(job *ciWorkflowJob) { job.Steps[ordinary].If = "" }},
				{"step-if-false", func(job *ciWorkflowJob) { job.Steps[ordinary].If = "false" }},
				{"step-mode-only", func(job *ciWorkflowJob) { job.Steps[ordinary].If = "${{ needs.classify.outputs.mode != 'metadata' }}" }},
				{"step-reversed", func(job *ciWorkflowJob) {
					job.Steps[ordinary].If = "${{ needs.classify.result == 'success' && needs.classify.outputs.mode == 'metadata' }}"
				}},
				{"step-unconditional-always", func(job *ciWorkflowJob) { job.Steps[ordinary].If = "${{ always() }}" }},
				{"step-extra-always", func(job *ciWorkflowJob) { job.Steps[ordinary].If = protectedAlwaysStepGate }},
				{"checkout-head-ref", func(job *ciWorkflowJob) {
					job.Steps[checkout].With["ref"] = "${{ github.event.pull_request.head.sha }}"
				}},
				{"checkout-moving-ref", func(job *ciWorkflowJob) { job.Steps[checkout].With["ref"] = "main" }},
				{"checkout-wrong-pin", func(job *ciWorkflowJob) { job.Steps[checkout].Uses = "actions/checkout@main" }},
				{"checkout-missing", func(job *ciWorkflowJob) { job.Steps[checkout].Uses = "" }},
				{"checkout-duplicate", func(job *ciWorkflowJob) { job.Steps = append(job.Steps, job.Steps[checkout]) }},
				{"source-sparse", func(job *ciWorkflowJob) { job.Steps[checkout].With["sparse-checkout"] = "scripts" }},
				{"timeout-missing", func(job *ciWorkflowJob) { job.TimeoutMinutes = 0 }},
				{"write-permission", func(job *ciWorkflowJob) { job.Permissions["checks"] = "write" }},
				{"missing-permission", func(job *ciWorkflowJob) { delete(job.Permissions, "actions") }},
				{"missing-verifier", func(job *ciWorkflowJob) { job.Steps = slices.Delete(job.Steps, verifier, verifier+1) }},
				{"duplicate-verifier", func(job *ciWorkflowJob) { job.Steps = append(job.Steps, job.Steps[verifier]) }},
				{"verifier-gate", func(job *ciWorkflowJob) { job.Steps[verifier].If = protectedStepGate }},
				{"verifier-mode-only", func(job *ciWorkflowJob) { job.Steps[verifier].If = "${{ needs.classify.outputs.mode == 'metadata' }}" }},
				{"metadata-checkout-mode-only", func(job *ciWorkflowJob) {
					job.Steps[metadataCheckout].If = "${{ needs.classify.outputs.mode == 'metadata' }}"
				}},
				{"verifier-directory", func(job *ciWorkflowJob) { job.Steps[verifier].WorkingDirectory = "." }},
				{"verifier-record", func(job *ciWorkflowJob) { delete(job.Steps[verifier].Env, "CI_METADATA_RECORD") }},
				{"verifier-token", func(job *ciWorkflowJob) { delete(job.Steps[verifier].Env, "GH_TOKEN") }},
				{"verifier-command", func(job *ciWorkflowJob) { job.Steps[verifier].Run = "true" }},
				{"metadata-ref", func(job *ciWorkflowJob) { job.Steps[metadataCheckout].With["ref"] = "main" }},
				{"metadata-pin", func(job *ciWorkflowJob) { job.Steps[metadataCheckout].Uses = "actions/checkout@main" }},
				{"metadata-sparse", func(job *ciWorkflowJob) { delete(job.Steps[metadataCheckout].With, "sparse-checkout") }},
				{"metadata-checkout-missing", func(job *ciWorkflowJob) { job.Steps = slices.Delete(job.Steps, metadataCheckout, metadataCheckout+1) }},
				{"metadata-checkout-duplicate", func(job *ciWorkflowJob) { job.Steps = append(job.Steps, job.Steps[metadataCheckout]) }},
			}
			if protected.id == "doc-freshness" {
				tests = append(tests, struct {
					name   string
					mutate func(*ciWorkflowJob)
				}{"checkout-shallow", func(job *ciWorkflowJob) { delete(job.Steps[checkout].With, "fetch-depth") }})
			}
			if protected.id == "frontend" {
				for _, stepName := range []string{"Upload browser execution evidence", "Check frontend coverage"} {
					stepIndex := slices.IndexFunc(base.Jobs[protected.id].Steps, func(step ciWorkflowStep) bool {
						return step.Name == stepName
					})
					if stepIndex < 0 {
						t.Fatalf("frontend step %q missing from base", stepName)
					}
					for _, mutation := range []struct {
						name   string
						mutate func(*ciWorkflowJob, int)
					}{
						{"missing-always", func(job *ciWorkflowJob, i int) { job.Steps[i].If = protectedStepGate }},
						{"missing-step", func(job *ciWorkflowJob, i int) { job.Steps = append(job.Steps[:i], job.Steps[i+1:]...) }},
						{"duplicate-step", func(job *ciWorkflowJob, i int) { job.Steps = append(job.Steps, job.Steps[i]) }},
					} {
						tests = append(tests, struct {
							name   string
							mutate func(*ciWorkflowJob)
						}{stepName + "/" + mutation.name, func(job *ciWorkflowJob) {
							mutation.mutate(job, stepIndex)
						}})
					}
				}
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					workflow := ciWorkflow{Jobs: maps.Clone(base.Jobs)}
					job := workflow.Jobs[protected.id]
					job.Steps = slices.Clone(job.Steps)
					job.Permissions = maps.Clone(job.Permissions)
					for i := range job.Steps {
						job.Steps[i].With = maps.Clone(job.Steps[i].With)
						job.Steps[i].Env = maps.Clone(job.Steps[i].Env)
					}
					tc.mutate(&job)
					workflow.Jobs[protected.id] = job
					if err := validateProtectedSourceWorkflow(workflow); err == nil {
						t.Fatal("invalid protected source workflow accepted")
					}
				})
			}
			t.Run("missing-job", func(t *testing.T) {
				workflow := ciWorkflow{Jobs: maps.Clone(base.Jobs)}
				delete(workflow.Jobs, protected.id)
				if err := validateProtectedSourceWorkflow(workflow); err == nil {
					t.Fatal("missing protected job accepted")
				}
			})
		})
	}
	t.Run("missing-classify-record-output", func(t *testing.T) {
		changed := ciWorkflow{Jobs: maps.Clone(base.Jobs)}
		job := changed.Jobs["classify"]
		job.Outputs = maps.Clone(job.Outputs)
		delete(job.Outputs, "record")
		changed.Jobs["classify"] = job
		if err := validateProtectedSourceWorkflow(changed); err == nil {
			t.Fatal("missing record output accepted")
		}
	})
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

const protectedJobGate = "${{ !cancelled() }}"
const protectedStepGate = "${{ needs.classify.result != 'success' || needs.classify.outputs.mode != 'metadata' }}"
const protectedAlwaysStepGate = "${{ always() && (needs.classify.result != 'success' || needs.classify.outputs.mode != 'metadata') }}"
const metadataStepGate = "${{ needs.classify.result == 'success' && needs.classify.outputs.mode == 'metadata' }}"

func validateProtectedSourceWorkflow(workflow ciWorkflow) error {
	classify, ok := workflow.Jobs["classify"]
	if !ok || classify.Outputs["record"] != "${{ steps.event.outputs.record }}" {
		return fmt.Errorf("missing classifier record output")
	}
	for _, protected := range []struct{ id, name string }{
		{"backend", "Backend Test & Build"},
		{"frontend", "Frontend Test & Build"},
		{"doc-freshness", "Doc Freshness Check"},
	} {
		job, ok := workflow.Jobs[protected.id]
		if !ok {
			return fmt.Errorf("%s job is missing", protected.id)
		}
		if job.If != protectedJobGate || !slices.Equal(job.Needs, []string{"classify"}) || job.Name != protected.name {
			return fmt.Errorf("%s job has an invalid admission gate, dependency, or name", protected.id)
		}
		if job.TimeoutMinutes != 360 || !maps.Equal(job.Permissions, map[string]string{
			"contents": "read", "actions": "read", "pull-requests": "read", "checks": "read",
		}) {
			return fmt.Errorf("%s invalid timeout or permissions", protected.id)
		}
		if len(job.Steps) == 0 {
			return fmt.Errorf("%s job has no steps", protected.id)
		}
		checkouts := 0
		metadataCheckout, verifier := 0, 0
		alwaysSteps := map[string]int{"Upload browser execution evidence": 0, "Check frontend coverage": 0}
		for index, step := range job.Steps {
			if step.Name == "Checkout metadata verifier" {
				metadataCheckout++
				if step.If != metadataStepGate || step.Uses != requiredPostgresCheckout ||
					!maps.Equal(step.With, map[string]string{
						"ref": "${{ github.sha }}", "fetch-depth": "2", "persist-credentials": "false",
						"sparse-checkout-cone-mode": "false", "sparse-checkout": "scripts/classify-ci-event.py",
					}) || step.Run != "" || step.WorkingDirectory != "" || len(step.Env) != 0 {
					return fmt.Errorf("%s invalid metadata checkout", protected.id)
				}
				if checkouts != 1 {
					return fmt.Errorf("%s metadata checkout position", protected.id)
				}
				continue
			}
			if step.Name == "Verify source check" {
				verifier++
				if step.If != metadataStepGate || step.WorkingDirectory != "${{ github.workspace }}" ||
					step.Uses != "" || len(step.With) != 0 ||
					step.Run != "python3 scripts/classify-ci-event.py --source-check '"+protected.name+"'" ||
					!maps.Equal(step.Env, map[string]string{
						"GH_TOKEN": "${{ github.token }}", "CI_WORKFLOW_SHA": "${{ github.workflow_sha }}",
						"CI_METADATA_RECORD": "${{ needs.classify.outputs.record }}",
					}) || index == 0 || job.Steps[index-1].Name != "Checkout metadata verifier" {
					return fmt.Errorf("%s invalid source verifier", protected.id)
				}
				continue
			}
			gate := protectedStepGate
			if _, isAlways := alwaysSteps[step.Name]; protected.id == "frontend" && isAlways {
				alwaysSteps[step.Name]++
				gate = protectedAlwaysStepGate
			}
			if step.If != gate {
				return fmt.Errorf("%s step %d %q has invalid gate %q", protected.id, index, step.Name, step.If)
			}
			if strings.HasPrefix(step.Uses, "actions/checkout@") {
				checkouts++
				if step.Uses != requiredPostgresCheckout || step.With["ref"] != "${{ github.sha }}" ||
					step.With["sparse-checkout"] != "" || step.With["sparse-checkout-cone-mode"] != "" ||
					(protected.id == "doc-freshness" && step.With["fetch-depth"] != "0") {
					return fmt.Errorf("%s step %d %q has invalid checkout pin, ref, or history", protected.id, index, step.Name)
				}
			}
		}
		if metadataCheckout != 1 || verifier != 1 {
			return fmt.Errorf("%s requires one metadata checkout and verifier", protected.id)
		}
		if checkouts != 1 {
			return fmt.Errorf("%s job requires one checkout, got %d", protected.id, checkouts)
		}
		if protected.id == "frontend" {
			for name, count := range alwaysSteps {
				if count != 1 {
					return fmt.Errorf("%s job requires one %q step, got %d", protected.id, name, count)
				}
			}
		}
	}
	return nil
}

const requiredPostgresTestDSN = "postgres://postgres:FAKE_POSTGRES_PASSWORD_FOR_TEST_ONLY@127.0.0.1:5432/xirang_test?sslmode=disable"

const requiredPostgresRunnerCommand = `bash ../scripts/run-required-postgres-tests.sh --evidence-dir "${{ runner.temp }}/required-postgres-evidence"`

const requiredPostgresSourceGate = "${{ !cancelled() && (needs.classify.result != 'success' || needs.classify.outputs.mode != 'metadata') }}"
const requiredPostgresJobName = "${{ needs.classify.result == 'success' && needs.classify.outputs.mode == 'metadata' && 'Metadata only / PostgreSQL Migration Parity' || 'PostgreSQL Migration Parity' }}"
const requiredPostgresCheckout = "actions/checkout@9f698171ed81b15d1823a05fc7211befd50c8ae0"

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
	workflow := loadCIWorkflow(t)
	job, ok := workflow.Jobs["postgres-migration"]
	if !ok {
		t.Fatal("CI workflow has no postgres-migration job")
	}
	return job
}

func validateRequiredPostgresWorkflow(job ciWorkflowJob) error {
	if job.If != requiredPostgresSourceGate || !slices.Equal(job.Needs, []string{"classify"}) || job.Name != requiredPostgresJobName {
		return fmt.Errorf("postgres job does not use the required source gate, dependency, and isolated metadata name")
	}
	if job.Env["TEST_POSTGRES_DSN"] != requiredPostgresTestDSN {
		return fmt.Errorf("postgres job has an unexpected test DSN")
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
	runners, selftests, uploads, backups, checkouts := 0, 0, 0, 0, 0
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			if step.Uses != requiredPostgresCheckout || step.With["ref"] != "${{ github.sha }}" || step.If != "" {
				return fmt.Errorf("postgres checkout must use the pinned action and event SHA")
			}
			checkouts++
		}
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
	if runners != 1 || selftests != 1 || uploads != 1 || backups != 1 || checkouts != 1 {
		return fmt.Errorf("postgres job requires one inventory runner, protocol selftest, evidence upload, backup/restore step, and checkout; got %d/%d/%d/%d/%d", runners, selftests, uploads, backups, checkouts)
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
		{"job-if-false", func(job *ciWorkflowJob) { job.If = "false" }},
		{"mode-only-gate", func(job *ciWorkflowJob) { job.If = "${{ !cancelled() && needs.classify.outputs.mode != 'metadata' }}" }},
		{"missing-classify", func(job *ciWorkflowJob) { job.Needs = nil }},
		{"metadata-source-name", func(job *ciWorkflowJob) { job.Name = "PostgreSQL Migration Parity" }},
		{"checkout-head-ref", func(job *ciWorkflowJob) {
			for i := range job.Steps {
				if strings.HasPrefix(job.Steps[i].Uses, "actions/checkout@") {
					job.Steps[i].With["ref"] = "${{ github.event.pull_request.head.sha }}"
				}
			}
		}},
		{"checkout-moving-ref", func(job *ciWorkflowJob) {
			for i := range job.Steps {
				if strings.HasPrefix(job.Steps[i].Uses, "actions/checkout@") {
					job.Steps[i].With["ref"] = "main"
				}
			}
		}},
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
			for i := range job.Steps {
				if base.Steps[i].With != nil {
					job.Steps[i].With = make(map[string]string, len(base.Steps[i].With))
					for key, value := range base.Steps[i].With {
						job.Steps[i].With[key] = value
					}
				}
			}
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
