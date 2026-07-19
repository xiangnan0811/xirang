package main

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
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
		"workerServers.StopAccepting()",
		"assetRuntime.StopAccepting()",
		"workerServers.Shutdown(shutdownCtx)",
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

func TestMainUsesDedicatedAuthenticatedWorkerServers(t *testing.T) {
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(sourceBytes)
	required := []string{
		"startWorkerHTTPServers(assetRuntime)",
		"api.NewWorkerRouter(",
		"processing.ListenLocalWorker(",
		"processing.ListenRemoteWorker(",
		"ConnContext:       api.WorkerConnContext",
	}
	for _, value := range required {
		if !strings.Contains(source, value) {
			t.Fatalf("main.go omitted dedicated Worker server boundary %q", value)
		}
	}
	startup := strings.Index(source, "assetRuntime.StartupPass(context.Background())")
	worker := strings.Index(source, "startWorkerHTTPServers(assetRuntime)")
	public := strings.Index(source, "router := api.NewRouter(")
	if startup < 0 || worker <= startup || public <= worker {
		t.Fatalf("Worker listener startup order invalid: runtime=%d worker=%d public=%d", startup, worker, public)
	}
}

func TestNewWorkerHTTPServerHasDedicatedBoundsAndConnContext(t *testing.T) {
	server := newWorkerHTTPServer(http.NotFoundHandler())
	if server == nil || server.Handler == nil || server.ConnContext == nil {
		t.Fatal("dedicated Worker HTTP server omitted handler or authenticated ConnContext")
	}
	for name, value := range map[string]time.Duration{
		"read header": server.ReadHeaderTimeout,
		"read":        server.ReadTimeout,
		"write":       server.WriteTimeout,
		"idle":        server.IdleTimeout,
	} {
		if value <= 0 || value > 30*time.Minute {
			t.Fatalf("Worker %s timeout=%s is unbounded", name, value)
		}
	}
}
