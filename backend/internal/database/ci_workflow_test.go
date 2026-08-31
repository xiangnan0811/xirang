package database

import (
	"os"
	"path/filepath"
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
