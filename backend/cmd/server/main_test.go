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
		"BackupAssets:      assetRuntime",
		"LegacyResticSnapshots: legacyRestic",
		"SnapshotDiffRunner:    legacyRestic",
		"SnapshotIndexer:       snapshotIndexer",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("main.go does not pass shared backup runtime port %q to Router", required)
		}
	}
}

func TestMainConstructsOneJWTManagerBeforeContentRuntimeAndReusesIt(t *testing.T) {
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(sourceBytes)
	if count := strings.Count(source, "auth.NewJWTManager("); count != 1 {
		t.Fatalf("main.go constructs %d JWT managers, want exactly one", count)
	}
	requiredInOrder := []string{
		"settingsSvc := settings.NewService(db)",
		"jwtManager := auth.NewJWTManager(cfg.JWTSecret, cfg.JWTTTL)",
		"jwtManager.SetDB(db)",
		"assetRuntime, err := backupruntime.New(",
		"SessionRevocations: jwtManager",
		"authService := auth.NewService(db, jwtManager, settingsSvc",
		"JWTManager:        jwtManager",
		"BackupContent:     assetRuntime.ContentBroker()",
		"BackupContentConfig:",
	}
	previous := -1
	for _, required := range requiredInOrder {
		index := strings.Index(source, required)
		if index < 0 {
			t.Fatalf("main.go is missing shared JWT/Content wiring %q", required)
		}
		if index <= previous {
			t.Fatalf("main.go wires %q out of startup order", required)
		}
		previous = index
	}
	for _, timeout := range []string{
		"ReadHeaderTimeout: 10 * time.Second",
		"ReadTimeout:       30 * time.Second",
		"WriteTimeout:      30 * time.Second",
		"IdleTimeout:       60 * time.Second",
	} {
		if !strings.Contains(source, timeout) {
			t.Fatalf("main.go changed the global HTTP timeout contract %q", timeout)
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
