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
		"taskManager.SetNodeWriteAdmission(assetRuntime.NodeWriteCoordinator())",
		"assetRuntime.SetCommitObserver(taskManager)",
		"assetRuntime.StartupPass(context.Background())",
		"taskManager.LoadSchedules(context.Background())",
	}
	if strings.Contains(source, "backupruntime.NewNodeWriteCoordinator(db)") {
		t.Fatal("main.go constructs a second Task/Recovery node-write coordinator")
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
		if mainSourceIndexIgnoringHorizontalWhitespace(source, required) < 0 {
			t.Fatalf("main.go does not pass shared backup runtime port %q to Router", required)
		}
	}
}

func TestRecoveryRuntimeMainDefersRecoveryAuthorizationRoutingToTaskNine(t *testing.T) {
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if strings.Contains(string(sourceBytes), "RecoveryAuthorization: assetRuntime.RecoveryAuthorization()") {
		t.Fatal("Task 8 main wires the Task 9 Recovery authorization route dependency")
	}
}

func TestMainConstructsCanonicalAlertDispatcherBeforeRecoveryRuntime(t *testing.T) {
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(sourceBytes)
	if count := strings.Count(source, "alerting.NewDispatcher("); count != 1 {
		t.Fatalf("main.go constructs %d canonical alert dispatchers, want one", count)
	}
	requiredInOrder := []string{
		"alertDispatcher := alerting.NewDispatcher(",
		"alerting.SetDispatcher(alertDispatcher)",
		"assetRuntime, err := backupruntime.New(",
		"AlertDispatcher: alertDispatcher",
	}
	previous := -1
	for _, required := range requiredInOrder {
		index := strings.Index(source, required)
		if index < 0 {
			t.Fatalf("main.go is missing canonical Recovery alert wiring %q", required)
		}
		if index <= previous {
			t.Fatalf("main.go wires %q out of startup order", required)
		}
		previous = index
	}
	if strings.Contains(source, "RecoveryTargetRoots()") {
		t.Fatal("main.go exposes the Task 9 target-root facade before its handlers exist")
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
		"alertDispatcher.BackupAssetSLORules()",
		"assetRuntime, err := backupruntime.New(",
		"SessionRevocations: jwtManager",
		"authService := auth.NewService(db, jwtManager, settingsSvc",
		"JWTManager:            jwtManager",
		"BackupContent:         assetRuntime.ContentService()",
		"BackupContentConfig:",
	}
	previous := -1
	for _, required := range requiredInOrder {
		index := mainSourceIndexIgnoringHorizontalWhitespace(source, required)
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

func mainSourceIndexIgnoringHorizontalWhitespace(source, fragment string) int {
	compact := strings.NewReplacer(" ", "", "\t", "").Replace
	return strings.Index(compact(source), compact(fragment))
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

func TestMainUsesDedicatedAuthenticatedUpdaterServerBeforePublicServing(t *testing.T) {
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(sourceBytes)
	for _, required := range []string{
		"startUpdaterHTTPServer(assetRuntime)",
		"api.NewWorkerUpdaterRouter(",
		"processingupdater.ListenLocalUpdater(",
		"ConnContext:       api.UpdaterConnContext",
		"updaterServer.StopAccepting()",
		"updaterServer.Shutdown(shutdownCtx)",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("main.go omitted dedicated updater server boundary %q", required)
		}
	}
	startup := strings.Index(source, "assetRuntime.StartupPass(context.Background())")
	updater := strings.Index(source, "startUpdaterHTTPServer(assetRuntime)")
	public := strings.Index(source, "router := api.NewRouter(")
	if startup < 0 || updater <= startup || public <= updater {
		t.Fatalf("updater listener startup order invalid: runtime=%d updater=%d public=%d", startup, updater, public)
	}
}

func TestNewUpdaterHTTPServerHasIndependentBoundsAndConnContext(t *testing.T) {
	server := newUpdaterHTTPServer(http.NotFoundHandler())
	if server == nil || server.Handler == nil || server.ConnContext == nil {
		t.Fatal("dedicated updater HTTP server omitted handler or authenticated ConnContext")
	}
	for name, value := range map[string]time.Duration{
		"read header": server.ReadHeaderTimeout,
		"read":        server.ReadTimeout,
		"write":       server.WriteTimeout,
		"idle":        server.IdleTimeout,
	} {
		if value <= 0 || value > 15*time.Minute {
			t.Fatalf("updater %s timeout=%s is unbounded", name, value)
		}
	}
}

func TestMainGatesDrillSchedulerOnTransportAvailability(t *testing.T) {
	sourceBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(sourceBytes)
	availability := strings.Index(source, "if taskManager.DrillAvailable()")
	start := strings.Index(source, "taskManager.StartDrillLoop()")
	if availability < 0 || start < 0 {
		t.Fatal("main.go must check drill transport availability before starting the drill scheduler")
	}
	if start <= availability {
		t.Fatal("main.go starts the drill scheduler before checking transport availability")
	}
	if strings.Count(source, "taskManager.StartDrillLoop()") != 1 {
		t.Fatal("main.go must have exactly one availability-gated drill scheduler start")
	}
	gatedBlock := source[availability:]
	blockEnd := strings.Index(gatedBlock, "\n\t}\n")
	if blockEnd < 0 || !strings.Contains(gatedBlock[:blockEnd], "taskManager.StartDrillLoop()") {
		t.Fatal("main.go drill scheduler start is not inside the availability guard")
	}
	if !strings.Contains(gatedBlock[:blockEnd], "taskManager.DrillAvailable()") {
		t.Fatal("main.go drill scheduler guard does not use Manager.DrillAvailable")
	}
}
