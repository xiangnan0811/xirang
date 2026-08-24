package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"
)

const enableTransitionDeadlockHelperEnv = "XIRANG_TEST_ENABLE_TRANSITION_DEADLOCK_HELPER"

const (
	enableTransitionHelperStartupTimeout    = 15 * time.Second
	enableTransitionHelperCompletionTimeout = 2 * time.Second
)

type enableTransitionHelperOutput struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	marker  []byte
	reached chan struct{}
	once    sync.Once
}

func newEnableTransitionHelperOutput(marker []byte) *enableTransitionHelperOutput {
	return &enableTransitionHelperOutput{
		marker:  append([]byte(nil), marker...),
		reached: make(chan struct{}),
	}
}

func (output *enableTransitionHelperOutput) Write(payload []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	written, err := output.buffer.Write(payload)
	if bytes.Contains(output.buffer.Bytes(), output.marker) {
		output.once.Do(func() { close(output.reached) })
	}
	return written, err
}

func (output *enableTransitionHelperOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.String()
}

func (output *enableTransitionHelperOutput) reachedStage() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return bytes.Contains(output.buffer.Bytes(), output.marker)
}

type productionDeadlockContentManager struct {
	*managedContentRuntime
	configs []backupasset.ContentConfig
}

func (manager *productionDeadlockContentManager) PrepareEnable(ctx context.Context, config backupasset.ContentConfig) error {
	_, _ = fmt.Fprintln(os.Stdout, "deadlock-stage=content-config")
	manager.configs = append(manager.configs, config)
	return manager.managedContentRuntime.PrepareEnable(ctx, config)
}

func TestRuntimeEnableTransitionContentConfigDoesNotReenterSettingsMutation(t *testing.T) {
	if os.Getenv(enableTransitionDeadlockHelperEnv) == "content" {
		runContentConfigDeadlockHelper(t)
		return
	}

	requireEnableTransitionHelperCompletes(t, "content")
}

func TestRuntimeEnableTransitionSearchConfigDoesNotReenterSettingsMutation(t *testing.T) {
	if os.Getenv(enableTransitionDeadlockHelperEnv) == "search" {
		runSearchConfigDeadlockHelper(t)
		return
	}

	requireEnableTransitionHelperCompletes(t, "search")
}

func TestEnableTransitionHelperAllowsSlowStartupBeforeStageDeadline(t *testing.T) {
	const helper = "delayed-stage"
	if os.Getenv(enableTransitionDeadlockHelperEnv) == helper {
		time.Sleep(2500 * time.Millisecond)
		writeEnableTransitionHelperReady(helper)
		return
	}

	requireEnableTransitionHelperCompletes(t, helper)
}

func runContentConfigDeadlockHelper(t *testing.T) {
	t.Helper()
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.Task{}, &model.TaskRepositoryLink{}, &model.RepositoryAccessBinding{}); err != nil {
		t.Fatalf("migrate production Content fixture: %v", err)
	}
	settingsService := settings.NewService(db)
	cacheRoot := filepath.Join(t.TempDir(), "asset-content-cache")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		t.Fatalf("create production Content cache root: %v", err)
	}
	if err := settingsService.Update("backup_assets.content_cache_root", cacheRoot); err != nil {
		t.Fatalf("configure production Content cache root: %v", err)
	}
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB:                 db,
		Settings:           settingsService,
		Transport:          transport,
		StreamTransport:    transport,
		StagedPayload:      &runtimeStagedPayloadFake{},
		Metrics:            publication.NoopMetrics{},
		ContentMetrics:     content.NoopMetrics{},
		SessionRevocations: &runtimeSessionRevocationsFake{},
	})
	if err != nil {
		t.Fatalf("construct production Content runtime: %v", err)
	}
	productionManager, ok := runtime.contentManager.(*managedContentRuntime)
	if !ok {
		t.Fatalf("production Content manager type=%T", runtime.contentManager)
	}
	t.Cleanup(func() { _ = productionManager.Shutdown(context.Background()) })
	if err := runtime.admission.InitializeManaged(context.Background()); err != nil {
		t.Fatalf("initialize managed admission: %v", err)
	}
	events := []string{}
	manager := &productionDeadlockContentManager{managedContentRuntime: productionManager}
	runtime.contentManager = manager
	runtime.exportManager = &runtimeExportSettingsManagerFake{events: &events}
	runtime.recoveryManager = nil
	runtime.inventory = nil
	runtime.keyring = nil
	runtime.searchWorker = nil
	runtime.enablement = readyGAEnablement()

	writeEnableTransitionHelperReady("content")
	if err := runRealEnabledSettingsMutation(settingsService, runtime); err != nil {
		t.Fatalf("run real Content enable transition: %v", err)
	}
	if len(manager.configs) != 1 || !manager.configs[0].Enabled {
		t.Fatalf("Content enable did not receive prospective config: %+v", manager.configs)
	}
	if productionManager.cache == nil || runtime.contentReady == nil || !runtime.contentReady.Load() {
		t.Fatal("production Content runtime did not build its cache and publish readiness")
	}
	if got := settingsService.GetEffective("backup_assets.enabled"); got != "true" {
		t.Fatalf("persisted backup_assets.enabled=%q, want true", got)
	}
}

func runSearchConfigDeadlockHelper(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_ENABLE_TRANSITION_SEARCH_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	db := openRuntimeTestDB(t)
	if err := db.AutoMigrate(&model.WrappedDomainKey{}); err != nil {
		t.Fatalf("migrate Search key fixture: %v", err)
	}
	settingsService := settings.NewService(db)
	transport := &runtimeTransportFake{}
	runtime, err := New(Dependencies{
		DB:                 db,
		Settings:           settingsService,
		Transport:          transport,
		StreamTransport:    transport,
		StagedPayload:      &runtimeStagedPayloadFake{},
		Metrics:            publication.NoopMetrics{},
		ContentMetrics:     content.NoopMetrics{},
		SessionRevocations: &runtimeSessionRevocationsFake{},
	})
	if err != nil {
		t.Fatalf("construct production Search runtime: %v", err)
	}
	events := []string{}
	runtime.contentManager = &runtimeContentManagerFake{events: &events}
	runtime.exportManager = &runtimeExportSettingsManagerFake{events: &events}
	runtime.recoveryManager = nil
	runtime.inventory = nil
	runtime.enablement = readyGAEnablement()
	if err := runtime.admission.InitializeManaged(context.Background()); err != nil {
		t.Fatalf("initialize managed admission: %v", err)
	}
	productionConfig := runtime.searchWorker.config
	runtime.searchWorker.config = func() (SearchWorkerConfig, error) {
		_, _ = fmt.Fprintln(os.Stdout, "deadlock-stage=search-config")
		return productionConfig()
	}
	runtime.searchWorker.backend = newSearchWorkerBackendFake()

	writeEnableTransitionHelperReady("search")
	if err := runRealEnabledSettingsMutation(settingsService, runtime); err != nil {
		t.Fatalf("run real Search enable transition: %v", err)
	}
}

func writeEnableTransitionHelperReady(helper string) {
	_, _ = fmt.Fprintln(os.Stdout, "helper-ready="+helper)
}

func runRealEnabledSettingsMutation(settingsService *settings.Service, runtime *Runtime) error {
	return settingsService.WithBackupAssetMutation(context.Background(), func(current map[string]string) error {
		effective := make(map[string]string, len(current))
		for key, value := range current {
			effective[key] = value
		}
		effective["backup_assets.enabled"] = "true"
		config, err := backupasset.ExportConfigFromValues(effective)
		if err != nil {
			return err
		}
		return runtime.TransitionBackupAssetSettings(
			context.Background(),
			current,
			map[string]string{"backup_assets.enabled": "true"},
			effective,
			config,
			func() error { return settingsService.Update("backup_assets.enabled", "true") },
		)
	})
}

func requireEnableTransitionHelperCompletes(t *testing.T, helper string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	cmd := exec.Command(
		executable,
		"-test.run=^"+regexp.QuoteMeta(t.Name())+"$",
		"-test.count=1",
		"-test.v",
	)
	cmd.Env = append(os.Environ(), enableTransitionDeadlockHelperEnv+"="+helper)
	output := newEnableTransitionHelperOutput([]byte("helper-ready=" + helper))
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s enable-transition helper: %v", helper, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	startupTimer := time.NewTimer(enableTransitionHelperStartupTimeout)
	defer startupTimer.Stop()
	select {
	case err := <-done:
		if !output.reachedStage() {
			t.Fatalf("%s enable-transition helper exited before signaling fixture readiness: %v\n%s", helper, err, output.String())
		}
		if err != nil {
			t.Fatalf("%s enable-transition helper failed after signaling fixture readiness: %v\n%s", helper, err, output.String())
		}
		return
	case <-output.reached:
	case <-startupTimer.C:
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("%s enable-transition helper exceeded its startup deadline before signaling fixture readiness\n%s", helper, output.String())
	}

	completionTimer := time.NewTimer(enableTransitionHelperCompletionTimeout)
	defer completionTimer.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s enable-transition helper failed after signaling fixture readiness: %v\n%s", helper, err, output.String())
		}
	case <-completionTimer.C:
		_ = cmd.Process.Kill()
		<-done
		if !bytes.Contains([]byte(output.String()), []byte("deadlock-stage="+helper)) {
			t.Fatalf("%s enable-transition helper exceeded its completion deadline before reaching the expected config-read stage\n%s", helper, output.String())
		}
		t.Fatalf("%s enable-transition helper exceeded its completion deadline; settings mutation re-entered its snapshot gate\n%s", helper, output.String())
	}
}
