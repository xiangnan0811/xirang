package main

import (
	"os"
	"strings"
	"testing"
)

func TestMainWiresSharedBackupAssetRuntimeBeforeSchedules(t *testing.T) {
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(sourceBytes)
	requiredInOrder := []string{
		"assetRuntime, err := backupruntime.New(",
		"executor.NewFactoryWithPublicationStrategies(",
		"taskManager.SetPublicationCoordinator(assetRuntime.PublicationCoordinator())",
		"taskManager.SetLineageGuard(assetRuntime.LineageGuard())",
		"assetRuntime.SetCommitObserver(taskManager)",
		"assetRuntime.StartupPass(context.Background())",
		"taskManager.LoadSchedules(context.Background())",
	}
	previous := -1
	for _, required := range requiredInOrder {
		index := strings.Index(source, required)
		if index < 0 {
			t.Fatalf("main.go is missing shared backup runtime wiring %q", required)
		}
		if index <= previous {
			t.Fatalf("main.go wires %q out of startup order", required)
		}
		previous = index
	}
	for _, required := range []string{
		"assetRuntime.RsyncTreePublicationStrategy()",
		"BackupAssets:          assetRuntime",
		"LegacyResticSnapshots: legacyRestic",
		"SnapshotDiffRunner:    legacyRestic",
		"SnapshotIndexer:       snapshotIndexer",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("main.go does not pass shared backup runtime port %q to Router", required)
		}
	}
}

func TestMainShutdownStopsResticAdmissionBeforeHTTPAndCleansUpAfterWorkers(t *testing.T) {
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(sourceBytes)
	requiredInOrder := []string{
		"cronScheduler.Stop()",
		"taskManager.StopAccepting()",
		"assetRuntime.StopAccepting()",
		"server.Shutdown(shutdownCtx)",
		"workers[i].Shutdown(shutdownCtx)",
		"executor.CleanupTempKeyDir()",
	}
	previous := -1
	for _, required := range requiredInOrder {
		index := strings.Index(source, required)
		if index < 0 {
			t.Fatalf("main.go is missing shutdown operation %q", required)
		}
		if index <= previous {
			t.Fatalf("main.go runs shutdown operation %q out of order", required)
		}
		previous = index
	}
	workerRuntime := strings.Index(source, "\t\tassetRuntime,")
	workerManager := strings.Index(source, "\t\ttaskManager,")
	if workerRuntime < 0 || workerManager < 0 || workerRuntime >= workerManager {
		t.Fatal("asset runtime must start before Task Manager so reverse shutdown drains consumers first")
	}
}
