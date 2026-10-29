package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/backupasset/repository"
	backupruntime "xirang/backend/internal/backupasset/runtime"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/task/executor"
	"xirang/backend/internal/task/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The application clock can advance independently of wall time; historical
// read deadlines are nevertheless bounded using the real command clock.
type rcloneHTTPClock struct {
	mu     sync.RWMutex
	offset time.Duration
}

func (clock *rcloneHTTPClock) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return time.Now().UTC().Add(clock.offset)
}

func (clock *rcloneHTTPClock) Advance(delta time.Duration) {
	clock.mu.Lock()
	clock.offset += delta
	clock.mu.Unlock()
}

type rcloneHTTPReadiness struct{}

func (rcloneHTTPReadiness) ReconcileInterruptedRuns(context.Context, int) (bool, error) {
	return false, nil
}

type rcloneHTTPDependencies func(*gorm.DB, *rcloneHTTPClock) (provider.CommandTransport, provider.CommandStreamTransport, provider.StagedPayloadTransport, repository.RcloneNativePreflightFactoryBuilder, repository.RcloneNativeFactoryBuilder)

type rcloneHTTPFixture struct {
	t        *testing.T
	db       *gorm.DB
	setting  *settings.Service
	runtime  *backupruntime.Runtime
	router   *gin.Engine
	jwt      *auth.JWTManager
	admin    model.User
	token    string
	clock    *rcloneHTTPClock
	binary   string
	external rcloneHTTPDependencies
	trace    *rcloneHTTPTraceTransport
	private  []string
}

func newRcloneHTTPFixture(t *testing.T, binary string, external rcloneHTTPDependencies) *rcloneHTTPFixture {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_RCLONE_RUNTIME_HTTP_KEY_FOR_TEST_ONLY")
	t.Setenv("DATA_ENCRYPTION_LEGACY_KEY", "")
	for _, definition := range settings.NewService(nil).Registry() {
		if strings.HasPrefix(definition.Key, "backup_assets.") && definition.EnvVar != "" {
			t.Setenv(definition.EnvVar, "")
		}
	}
	t.Setenv("BACKUP_ASSETS_CONTENT_CACHE_ENABLED", "false")
	t.Setenv("BACKUP_ASSETS_EXPORT_ROOT", filepath.Join(t.TempDir(), "export"))
	t.Setenv("SSH_STRICT_HOST_KEY_CHECKING", "true")
	t.Setenv("SSH_AUTO_ACCEPT_NEW_HOSTS", "true")
	t.Setenv("SSH_KNOWN_HOSTS_PATH", filepath.Join(t.TempDir(), "known_hosts"))
	sshutil.SetAutoAcceptNewHostsSource(nil)
	t.Cleanup(func() { sshutil.SetAutoAcceptNewHostsSource(nil) })
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	gin.SetMode(gin.TestMode)

	dsn := filepath.Join(t.TempDir(), "runtime.sqlite") + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&_synchronous=NORMAL&_txlock=immediate&_loc=UTC"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open isolated runtime database: %v", err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := database.RunMigrations(db, "sqlite"); err != nil {
		t.Fatalf("migrate runtime database: %v", err)
	}
	setting := settings.NewService(db)
	jwt := auth.NewJWTManager("FAKE_RCLONE_RUNTIME_HTTP_JWT_SECRET_FOR_TEST_ONLY", 24*time.Hour)
	jwt.SetDB(db)
	hash, err := auth.HashPassword("RcloneHttpFixture#2026")
	if err != nil {
		t.Fatal(err)
	}
	admin := model.User{Username: "rclone-fixture-admin", PasswordHash: hash, Role: "admin", Onboarded: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	token, err := jwt.GenerateToken(admin)
	if err != nil {
		t.Fatal(err)
	}
	clock := &rcloneHTTPClock{}
	dependencies := backupruntime.Dependencies{
		DB: db, Settings: setting, SessionRevocations: jwt, Now: clock.Now,
		ToolBinaries: provider.ToolBinaries{Rclone: binary}, Metrics: publication.NoopMetrics{},
	}
	var trace *rcloneHTTPTraceTransport
	if external != nil {
		dependencies.Transport, dependencies.StreamTransport, dependencies.StagedPayload, dependencies.RcloneNativePreflightFactory, dependencies.RcloneNativeFactoryBuilder = external(db, clock)
	} else if directory := os.Getenv("XIRANG_RCLONE_HTTP_DIAGNOSTIC_DIR"); directory != "" {
		trace = newRcloneHTTPTraceTransport(t, db, setting, dependencies.ToolBinaries, directory)
		dependencies.Transport, dependencies.StreamTransport = trace, trace
	}
	runtime, err := backupruntime.New(dependencies)
	if err != nil {
		t.Fatalf("construct production runtime: %v", err)
	}
	if err := runtime.SetInterruptedRunReadiness(rcloneHTTPReadiness{}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunInventory(context.Background()); err != nil {
		t.Fatalf("run real empty-installation inventory: %v", err)
	}
	if err := runtime.StartupPass(context.Background()); err != nil {
		t.Fatalf("start production runtime: %v", err)
	}
	if err := runtime.TransitionFeature(context.Background(), true, func() error {
		return setting.Update("backup_assets.enabled", "true")
	}); err != nil {
		t.Fatalf("enable production runtime: %v", err)
	}
	if live, err := runtime.FeatureLive(); err != nil || !live {
		t.Fatalf("production readiness did not enable feature: live=%t err=%v", live, err)
	}
	fixture := &rcloneHTTPFixture{t: t, db: db, setting: setting, runtime: runtime, jwt: jwt, admin: admin, token: token, clock: clock, binary: binary, external: external, trace: trace}
	fixture.router = NewRouter(Dependencies{DB: db, JWTManager: jwt, SettingsService: setting, BackupAssets: runtime})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := fixture.runtime.Shutdown(ctx); err != nil {
			t.Error("production runtime did not shut down cleanly")
		}
	})
	return fixture
}

func (fixture *rcloneHTTPFixture) restart() {
	fixture.t.Helper()
	if err := fixture.runtime.Shutdown(context.Background()); err != nil {
		fixture.t.Fatalf("stop first production runtime: %v", err)
	}
	dependencies := backupruntime.Dependencies{
		DB: fixture.db, Settings: fixture.setting, SessionRevocations: fixture.jwt, Now: fixture.clock.Now,
		ToolBinaries: provider.ToolBinaries{Rclone: fixture.binary}, Metrics: publication.NoopMetrics{},
	}
	if fixture.external != nil {
		dependencies.Transport, dependencies.StreamTransport, dependencies.StagedPayload, dependencies.RcloneNativePreflightFactory, dependencies.RcloneNativeFactoryBuilder = fixture.external(fixture.db, fixture.clock)
	}
	runtime, err := backupruntime.New(dependencies)
	if err != nil {
		fixture.t.Fatalf("construct restarted production runtime: %v", err)
	}
	if err := runtime.SetInterruptedRunReadiness(rcloneHTTPReadiness{}); err != nil {
		fixture.t.Fatal(err)
	}
	if err := runtime.StartupPass(context.Background()); err != nil {
		fixture.t.Fatalf("start restarted production runtime: %v", err)
	}
	fixture.runtime = runtime
	fixture.router = NewRouter(Dependencies{DB: fixture.db, JWTManager: fixture.jwt, SettingsService: fixture.setting, BackupAssets: runtime})
}

func (fixture *rcloneHTTPFixture) request(method, path string, body any, status int, destination any) {
	fixture.t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			fixture.t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(data))
	request.Header.Set("Authorization", "Bearer "+fixture.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	if response.Code != status {
		// Only print a validated, closed reason code, never provider output.
		var envelope struct {
			Data struct {
				Reason backupasset.CapabilityReason `json:"reason"`
			} `json:"data"`
		}
		reason := "unclassified"
		if json.Unmarshal(response.Body.Bytes(), &envelope) == nil && backupasset.ValidateCapabilityReason(envelope.Data.Reason) == nil {
			reason = string(envelope.Data.Reason.Code)
		}
		fixture.t.Fatalf("%s %s status=%d want=%d reason=%s", method, path, response.Code, status, reason)
	}
	if strings.Contains(path, "/rclone-versioning/") && !strings.HasSuffix(path, "native-binding-setups") {
		for _, value := range fixture.private {
			if value != "" && strings.Contains(response.Body.String(), value) {
				fixture.t.Fatalf("%s returned private fixture material", path)
			}
		}
	}
	if destination != nil {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			fixture.t.Fatalf("decode response envelope: %v", err)
		}
		if err := json.Unmarshal(envelope.Data, destination); err != nil {
			fixture.t.Fatalf("decode response data: %v", err)
		}
	}
}

type rcloneHTTPTask struct {
	ID                uint                                 `json:"id"`
	Enabled           bool                                 `json:"enabled"`
	RclonePublication backupasset.RclonePublicationSummary `json:"rclone_publication"`
}

func (fixture *rcloneHTTPFixture) task(id uint) rcloneHTTPTask {
	fixture.t.Helper()
	var result rcloneHTTPTask
	fixture.request(http.MethodGet, fmt.Sprintf("/api/v1/tasks/%d", id), nil, http.StatusOK, &result)
	return result
}

func (fixture *rcloneHTTPFixture) addTask(node model.Node, name, source, legacy string) uint {
	fixture.t.Helper()
	node.Name = name + "-node"
	node.BasePath = source
	node.BackupDir = name + "-backup"
	if err := fixture.db.Create(&node).Error; err != nil {
		fixture.t.Fatalf("create isolated SSH node: %v", err)
	}
	task := model.Task{Name: name, NodeID: node.ID, ExecutorType: "rclone", ExecutorConfig: `{"bandwidth_limit":"10M","transfers":1}`,
		RsyncSource: source, RsyncTarget: legacy, Status: "pending"}
	if err := fixture.db.Create(&task).Error; err != nil {
		fixture.t.Fatalf("create legacy task: %v", err)
	}
	// GORM applies the schema default to a zero bool on Create.
	if err := fixture.db.Model(&model.Task{}).Where("id = ?", task.ID).Update("enabled", false).Error; err != nil {
		fixture.t.Fatal(err)
	}
	fixture.request(http.MethodPost, "/api/v1/backup-repositories/connect", map[string]any{"task_id": task.ID}, http.StatusOK, nil)
	return task.ID
}

func (fixture *rcloneHTTPFixture) portableCandidate(id uint, config, managed string) string {
	fixture.t.Helper()
	base := fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/", id)
	revision := fixture.task(id).RclonePublication.TaskRevision
	var setup backupasset.RcloneBindingSetupResult
	fixture.request(http.MethodPost, base+"portable-binding-setups", map[string]any{"expected_task_revision": revision}, http.StatusOK, &setup)
	fixture.private = append(fixture.private, managed, config, "-----BEGIN RSA PRIVATE KEY-----")
	fixture.request(http.MethodPut, base+"portable-binding", map[string]any{
		"expected_task_revision": revision, "expected_binding_revision": setup.BaseBindingRevision, "setup_id": setup.SetupID,
		"target_remote": "archive", "managed_root_locator": managed, "bound_config": config,
	}, http.StatusOK, nil)
	return revision
}

func (fixture *rcloneHTTPFixture) preflight(id uint, revision string, mode backupasset.TaskPublicationMode) backupasset.RcloneVersioningPreflightResult {
	fixture.t.Helper()
	var result backupasset.RcloneVersioningPreflightResult
	fixture.request(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/preflights", id),
		map[string]any{"expected_task_revision": revision, "requested_mode": mode}, http.StatusOK, &result)
	return result
}

func (fixture *rcloneHTTPFixture) activate(id uint, revision, ticket string, status int) backupasset.RcloneVersioningActivationResult {
	fixture.t.Helper()
	var result backupasset.RcloneVersioningActivationResult
	var destination any
	if status == http.StatusOK {
		destination = &result
	}
	fixture.request(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/activate", id), map[string]any{
		"expected_task_revision": revision, "preflight_id": ticket, "migration_choice": "first_new_point",
	}, status, destination)
	return result
}

func requireRealRclone(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("RCLONE_TEST_BINARY")
	if binary == "" {
		run := flag.Lookup("test.run")
		if run != nil && (strings.Contains(run.Value.String(), "TestRclonePreflightRealSSHPortable") ||
			strings.Contains(run.Value.String(), "TestRclonePortableHistoricalV1RealCommand")) {
			t.Fatal("RCLONE_TEST_BINARY is required for the explicitly selected real SSH test")
		}
		t.Skip("RCLONE_TEST_BINARY is required for real SSH acceptance")
	}
	absolute, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(absolute, "version").Output()
	if err != nil || provider.ValidateManagedRcloneVersion(output) != nil {
		t.Fatal("RCLONE_TEST_BINARY must execute and report exactly rclone v1.74.4")
	}
	return absolute
}

func startRcloneHTTPSSH(t *testing.T) model.Node {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		t.Fatal("sshd is required for the real SSH fixture")
	}
	return testutil.StartRsyncSSHServer(t, sshd)
}

func rcloneSFTPConfig(node model.Node) string {
	return fmt.Sprintf("[archive]\ntype = sftp\nhost = %s\nport = %d\nuser = %s\nkey_pem = %s\n", node.Host, node.Port, node.Username, strings.ReplaceAll(node.PrivateKey, "\n", `\n`))
}

func makeRcloneHTTPPaths(t *testing.T) (source, legacy, managed string) {
	t.Helper()
	root := t.TempDir()
	source, legacy, managed = filepath.Join(root, "source"), filepath.Join(root, "legacy"), filepath.Join(root, "managed")
	for _, directory := range []string{source, legacy, managed} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte("first managed publication\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "legacy.txt"), []byte("unchanged legacy target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return
}

// rcloneHTTPAcceptanceTransport records only operation metadata and the names
// of newly created controls. All commands still run through the real SSH
// transport; neither locators nor credentials are retained in the trace.
type rcloneHTTPAcceptanceTransport struct {
	delegate *provider.SSHCommandTransport
	mu       sync.Mutex
	root     string
	known    map[string]bool
	events   []rcloneHTTPCommandEvent
}

type rcloneHTTPCommandEvent struct {
	operation provider.CommandOperation
	purpose   provider.CommandPurpose
	deadline  time.Time
	started   time.Time
	elapsed   time.Duration
	succeeded bool
	created   []string
	scanErr   error
}

func (trace *rcloneHTTPAcceptanceTransport) reset(root string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.root = root
	trace.known = make(map[string]bool)
	trace.events = nil
}

func (trace *rcloneHTTPAcceptanceTransport) record(invocation provider.CommandInvocation, started time.Time, success bool) {
	event := rcloneHTTPCommandEvent{
		operation: invocation.Operation, purpose: invocation.Purpose,
		deadline: invocation.AbsoluteDeadline, started: started, elapsed: time.Since(started), succeeded: success,
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if success && trace.root != "" && invocation.Operation == provider.OperationRcloneManagedCopyTo {
		var paths []string
		paths, event.scanErr = filepath.Glob(filepath.Join(trace.root, "points", "*", "control", "*"))
		for _, name := range paths {
			if !trace.known[name] {
				trace.known[name] = true
				event.created = append(event.created, filepath.Base(name))
			}
		}
		sort.Strings(event.created)
	}
	trace.events = append(trace.events, event)
}

func (trace *rcloneHTTPAcceptanceTransport) snapshot() []rcloneHTTPCommandEvent {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return append([]rcloneHTTPCommandEvent(nil), trace.events...)
}

func (trace *rcloneHTTPAcceptanceTransport) Run(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits) (provider.CommandOutput, error) {
	started := time.Now().UTC()
	output, err := trace.delegate.Run(ctx, invocation, limits)
	trace.record(invocation, started, err == nil)
	return output, err
}

func (trace *rcloneHTTPAcceptanceTransport) Open(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits, maxBytes int64) (provider.ReadHandle, error) {
	started := time.Now().UTC()
	handle, err := trace.delegate.Open(ctx, invocation, limits, maxBytes)
	trace.record(invocation, started, err == nil)
	return handle, err
}

func (trace *rcloneHTTPAcceptanceTransport) OpenExecution(ctx context.Context, invocation provider.CommandInvocation, limits provider.OperationLimits, maxBytes int64) (provider.CommandExecution, error) {
	started := time.Now().UTC()
	execution, err := trace.delegate.OpenExecution(ctx, invocation, limits, maxBytes)
	trace.record(invocation, started, err == nil)
	return execution, err
}

func rcloneHTTPAcceptanceDependencies(t *testing.T, binary string, trace **rcloneHTTPAcceptanceTransport) rcloneHTTPDependencies {
	return func(db *gorm.DB, _ *rcloneHTTPClock) (provider.CommandTransport, provider.CommandStreamTransport, provider.StagedPayloadTransport, repository.RcloneNativePreflightFactoryBuilder, repository.RcloneNativeFactoryBuilder) {
		if *trace == nil {
			foundation := backupasset.NewFoundationService(settings.NewService(db))
			delegate, err := provider.NewSSHCommandTransportWithConcurrencySource(sshutil.NewNodeDialer(db), func() (int, error) {
				config, err := foundation.ProviderConfig()
				return config.MaxConcurrency, err
			}, provider.ToolBinaries{Rclone: binary})
			if err != nil {
				t.Fatal("construct real SSH acceptance transport")
			}
			*trace = &rcloneHTTPAcceptanceTransport{delegate: delegate}
		}
		return *trace, *trace, nil, nil, nil
	}
}

func TestRclonePreflightRealSSHPortable(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 18*time.Minute)
	defer cancel()
	binary := requireRealRclone(t)
	node := startRcloneHTTPSSH(t)
	var trace *rcloneHTTPAcceptanceTransport
	fixture := newRcloneHTTPFixture(t, binary, rcloneHTTPAcceptanceDependencies(t, binary, &trace))
	if err := fixture.setting.Update("backup_assets.rclone_portable_deadline", "5m"); err != nil {
		t.Fatal("set the isolated portable publication deadline")
	}
	source, legacy, managed := makeRcloneHTTPPaths(t)
	sourceTime := time.Date(2024, 4, 5, 6, 7, 8, 123456789, time.UTC)
	sourceBytes := []byte("first managed publication\n")
	sourceFile := filepath.Join(source, "source.txt")
	if err := os.Chmod(sourceFile, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(sourceFile, sourceTime, sourceTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "child.txt"), []byte("nested publication\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested/child.txt", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}

	id := fixture.addTask(node, "portable-real-ssh", source, legacy)
	config, target := rcloneSFTPConfig(node), "archive:"+managed
	revision := fixture.portableCandidate(id, config, target)
	firstReady := fixture.preflight(id, revision, backupasset.PublicationVersionedPrefix)
	if firstReady.Purpose != backupasset.RclonePreflightPurposeActivation || firstReady.Summary.State != backupasset.RcloneStateReady ||
		firstReady.Summary.BindingRevision != "1" || firstReady.PreflightID == "" {
		t.Fatal("first real portable candidate did not become ready at proposed revision 1")
	}
	base := fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/", id)
	var replacementSetup backupasset.RcloneBindingSetupResult
	fixture.request(http.MethodPost, base+"portable-binding-setups", map[string]any{"expected_task_revision": revision}, http.StatusOK, &replacementSetup)
	if err := replacementSetup.Validate(false); err != nil || replacementSetup.BaseBindingRevision != "0" {
		t.Fatal("second portable setup did not return persisted base revision 0")
	}
	var replacement backupasset.RclonePublicationSummary
	fixture.request(http.MethodPut, base+"portable-binding", map[string]any{
		"expected_task_revision": revision, "expected_binding_revision": replacementSetup.BaseBindingRevision, "setup_id": replacementSetup.SetupID,
		"target_remote": "archive", "managed_root_locator": target, "bound_config": config,
	}, http.StatusOK, &replacement)
	if replacement.BindingRevision != "1" {
		t.Fatal("successful replacement changed the proposed binding revision")
	}
	fixture.activate(id, revision, firstReady.PreflightID, http.StatusConflict)
	ready := fixture.preflight(id, revision, backupasset.PublicationVersionedPrefix)
	if ready.Purpose != backupasset.RclonePreflightPurposeActivation || ready.Summary.State != backupasset.RcloneStateReady ||
		ready.Summary.BindingRevision != "1" || ready.PreflightID == "" {
		t.Fatal("replacement portable preflight did not produce a fresh activation ticket")
	}
	var rejectedSetup backupasset.RcloneBindingSetupResult
	fixture.request(http.MethodPost, base+"portable-binding-setups", map[string]any{"expected_task_revision": revision}, http.StatusOK, &rejectedSetup)
	if err := rejectedSetup.Validate(false); err != nil || rejectedSetup.BaseBindingRevision != "0" {
		t.Fatal("portable replacement setup did not return persisted base revision 0")
	}
	badReplacement := map[string]any{
		"expected_task_revision": revision, "expected_binding_revision": ready.Summary.BindingRevision, "setup_id": rejectedSetup.SetupID,
		"target_remote": "archive", "managed_root_locator": target, "bound_config": config,
	}
	beforeRejected := fixture.task(id).RclonePublication
	fixture.request(http.MethodPut, base+"portable-binding", badReplacement, http.StatusConflict, nil)
	badReplacement["expected_binding_revision"] = rejectedSetup.BaseBindingRevision
	fixture.request(http.MethodPut, base+"portable-binding", badReplacement, http.StatusConflict, nil)
	if afterRejected := fixture.task(id).RclonePublication; !reflect.DeepEqual(beforeRejected, afterRejected) {
		t.Fatal("rejected or replayed replacement changed the ready portable candidate")
	}
	activated := fixture.activate(id, revision, ready.PreflightID, http.StatusOK)
	if activated.Summary.TaskRevision == revision || fixture.task(id).Enabled {
		t.Fatal("activation did not advance the paused task revision")
	}
	var link model.TaskRepositoryLink
	if err := fixture.db.Where("task_id = ? AND unlinked_at IS NULL", id).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.PublicationMode != string(backupasset.PublicationVersionedPrefix) || link.EncryptedLegacyLocator != legacy {
		t.Fatal("activation did not retain the original legacy locator")
	}
	before := rcloneHTTPBindingDocument(t, fixture.db, link.RepositoryID)
	trace.reset(managed)
	outcome := fixture.publishRclone(id)
	point, manifest, attempt, commit := fixture.portableHTTPPoint(outcome.RecoveryPointID)
	lineage, err := backupasset.DecodePublicationLineage(point.LineageJSON)
	if err != nil || !lineage.PointDeadlineAt.Equal(attempt.PointDeadlineAt) || !attempt.PointDeadlineAt.After(time.Now().UTC()) {
		t.Fatal("new point did not retain a future, matching publication deadline")
	}
	oldExpiry, oldOK := before["preflight_expires_at"].(string)
	oldTime, oldErr := time.Parse(time.RFC3339Nano, oldExpiry)
	if !oldOK || oldErr != nil || lineage.PointDeadlineAt.After(oldTime) {
		t.Fatal("point deadline exceeded the original binding authorization")
	}
	dataRoot := filepath.Join(managed, "points", attempt.Portable.AttemptComponent, "data")
	controlRoot := filepath.Join(managed, "points", attempt.Portable.AttemptComponent, "control")
	fixture.assertPortableHTTPPublication(trace.snapshot(), dataRoot, controlRoot, sourceBytes, sourceTime, commit, manifest)
	if got, err := os.ReadFile(filepath.Join(legacy, "legacy.txt")); err != nil || string(got) != "unchanged legacy target\n" {
		t.Fatal("managed publication changed the original target")
	}
	firstGeneration, firstEntry := fixture.buildPortableHTTPCatalog(ctx, outcome, sourceTime)
	contentServer, served := fixture.newPortableHTTPContentServer()
	defer contentServer.Close()
	fixture.readPortableHTTPContent(ctx, point, firstGeneration, firstEntry, sourceBytes, sourceTime, false)
	fixture.assertPortableHTTPBrokerContent(ctx, contentServer, served, trace, "initial", point, firstGeneration, firstEntry, sourceBytes)

	timer := time.NewTimer(time.Until(lineage.PointDeadlineAt) + time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatalf("waiting for the real publication deadline: %v", ctx.Err())
	}
	if !time.Now().UTC().After(lineage.PointDeadlineAt) {
		t.Fatal("wall clock did not cross the original point deadline")
	}
	trace.reset("")
	generation, entry := fixture.buildPortableHTTPCatalog(ctx, outcome, sourceTime)
	if entry.EntryID != firstEntry.EntryID || entry.EncryptedProviderLocator != firstEntry.EncryptedProviderLocator {
		t.Fatal("historical Catalog rebuild changed the canonical entry identity or locator")
	}
	fixture.readPortableHTTPContent(ctx, point, generation, entry, sourceBytes, sourceTime, true)
	fixture.assertPortableHTTPBrokerContent(ctx, contentServer, served, trace, "historical", point, generation, entry, sourceBytes)
	publicationConfig, err := fixture.runtime.FoundationService().PublicationConfig()
	if err != nil {
		t.Fatal(err)
	}
	assertRcloneHTTPHistoricalReadTrace(t, trace.snapshot(), lineage.PointDeadlineAt, publicationConfig.ManifestTimeout)

	// Restart before advancing the application clock so the Broker's default
	// ticket deadline remains a real 20-second wall-clock budget.
	contentServer.Close()
	// This is now an existing installation. Complete its real admin inventory
	// acknowledgment before restart; startup must retain the production gate.
	var readiness struct {
		Class              string `json:"class"`
		Status             string `json:"status"`
		InventoryComplete  bool   `json:"inventory_complete"`
		InventoryDigest    string `json:"inventory_digest"`
		AcknowledgedDigest string `json:"acknowledged_digest"`
	}
	fixture.request(http.MethodPost, "/api/v1/settings/backup-assets/ga/inventory", nil, http.StatusOK, &readiness)
	if readiness.Class != "existing" || readiness.Status != "ready" || !readiness.InventoryComplete || len(readiness.InventoryDigest) != 64 || readiness.AcknowledgedDigest != "" {
		t.Fatal("historical installation did not require current inventory acknowledgment")
	}
	digest := readiness.InventoryDigest
	fixture.request(http.MethodPost, "/api/v1/settings/backup-assets/ga/acknowledge", map[string]any{"digest": digest}, http.StatusOK, &readiness)
	if readiness.Status != "acknowledged" || readiness.InventoryDigest != digest || readiness.AcknowledgedDigest != digest {
		t.Fatal("admin acknowledgment did not bind the current installation inventory")
	}
	fixture.restart()
	restartedContentServer, restartedServed := fixture.newPortableHTTPContentServer()
	defer restartedContentServer.Close()
	fixture.assertPortableHTTPBrokerContent(ctx, restartedContentServer, restartedServed, trace, "historical after restart", point, generation, entry, sourceBytes)
	restartedContentServer.Close()

	fixture.clock.Advance(31 * time.Minute)
	expired := fixture.task(id).RclonePublication
	if expired.State != backupasset.RcloneStatePreflightRequired || expired.ReasonCode != backupasset.RcloneReasonPreflightExpired {
		t.Fatal("expired managed binding did not require a new preflight")
	}
	fixture.assertExpiredPublicationDenied(id)
	renewal := fixture.preflight(id, expired.TaskRevision, backupasset.PublicationVersionedPrefix)
	if renewal.Purpose != backupasset.RclonePreflightPurposeRenewal || renewal.Summary.State != backupasset.RcloneStateReady {
		t.Fatal("real portable renewal was not ready")
	}
	renewed := fixture.activate(id, expired.TaskRevision, renewal.PreflightID, http.StatusOK)
	if renewed.Summary.TaskRevision == expired.TaskRevision || fixture.task(id).Enabled {
		t.Fatal("renewal did not advance the paused task revision")
	}
	after := rcloneHTTPBindingDocument(t, fixture.db, link.RepositoryID)
	newExpiry, newOK := after["preflight_expires_at"].(string)
	newTime, newErr := time.Parse(time.RFC3339Nano, newExpiry)
	delete(before, "preflight_expires_at")
	delete(after, "preflight_expires_at")
	if !newOK || newErr != nil || !newTime.After(oldTime) || !reflect.DeepEqual(before, after) {
		t.Fatal("renewal changed binding identity or did not extend the authorization")
	}
	fixture.restart()
	reopened := fixture.task(id).RclonePublication
	if reopened.TaskRevision != renewed.Summary.TaskRevision || reopened.ReasonCode == backupasset.RcloneReasonPreflightExpired {
		t.Fatal("restarted runtime lost the renewed authorization or task revision")
	}
	reopenedPoint, reopenedManifest, reopenedAttempt, _ := fixture.portableHTTPPoint(outcome.RecoveryPointID)
	if reopenedPoint.ID != point.ID || reopenedPoint.LineageJSON != point.LineageJSON ||
		reopenedPoint.EncryptedProviderLocator != point.EncryptedProviderLocator || reopenedPoint.SourceFingerprint != point.SourceFingerprint ||
		reopenedManifest.ID != manifest.ID || reopenedManifest.Digest != manifest.Digest ||
		reopenedManifest.EncryptedCommitEvidence != manifest.EncryptedCommitEvidence || !reopenedAttempt.PointDeadlineAt.Equal(lineage.PointDeadlineAt) {
		t.Fatal("renewal or restart rewrote the historical point, manifest, locator, or deadline")
	}
	var reopenedEntry model.CatalogEntry
	if err := fixture.db.Where("generation_id = ? AND entry_id = ?", generation.ID, entry.EntryID).First(&reopenedEntry).Error; err != nil ||
		reopenedEntry.EncryptedProviderLocator != entry.EncryptedProviderLocator {
		t.Fatal("renewal or restart rewrote the historical Catalog locator")
	}
	trace.reset("")
	restartedGeneration, restartedEntry := fixture.buildPortableHTTPCatalog(ctx, outcome, sourceTime)
	if restartedEntry.EntryID != entry.EntryID || restartedEntry.EncryptedProviderLocator != entry.EncryptedProviderLocator {
		t.Fatal("restarted historical Catalog changed the entry identity or locator")
	}
	fixture.readPortableHTTPContent(ctx, reopenedPoint, restartedGeneration, restartedEntry, sourceBytes, sourceTime, false)
	assertRcloneHTTPHistoricalReadTrace(t, trace.snapshot(), lineage.PointDeadlineAt, publicationConfig.ManifestTimeout)
	fixture.activate(id, expired.TaskRevision, renewal.PreflightID, http.StatusConflict)

	physicalFile := filepath.Join(dataRoot, "source.txt")
	physical, err := os.Stat(physicalFile)
	if err != nil {
		t.Fatal(err)
	}
	drifted := physical.ModTime().Add(10 * time.Second)
	if err := os.Chtimes(physicalFile, drifted, drifted); err != nil {
		t.Fatal(err)
	}
	request := portableHTTPContentRequest(reopenedPoint, restartedGeneration, restartedEntry)
	session, err := fixture.runtime.RepositoryService().OpenContentSource(ctx, request)
	if err == nil {
		if session != nil {
			_ = session.Close()
		}
		t.Fatal("same-size destination mtime drift did not reject content")
	}
}

func (fixture *rcloneHTTPFixture) portableHTTPPoint(pointID string) (model.RecoveryPoint, model.RecoveryPointManifest, provider.RcloneAttemptV1, provider.RcloneCommitV1) {
	fixture.t.Helper()
	var point model.RecoveryPoint
	var manifest model.RecoveryPointManifest
	if err := fixture.db.First(&point, "id = ?", pointID).Error; err != nil {
		fixture.t.Fatal(err)
	}
	if err := fixture.db.Where("recovery_point_id = ? AND is_active = ?", pointID, true).First(&manifest).Error; err != nil {
		fixture.t.Fatal(err)
	}
	var locator struct {
		TaggedAttempt string `json:"tagged_attempt"`
		TaggedCommit  string `json:"tagged_commit"`
	}
	if err := json.Unmarshal([]byte(point.EncryptedProviderLocator), &locator); err != nil {
		fixture.t.Fatal("decode managed point locator")
	}
	attempt, attemptErr := provider.DecodeRcloneAttemptV1(locator.TaggedAttempt)
	commit, commitErr := provider.DecodeRcloneCommitV1(locator.TaggedCommit)
	if attemptErr != nil || commitErr != nil || provider.ValidateRcloneRuntimeCompatibility(attempt, commit) != nil ||
		attempt.MinimumRuntimeRevision != 2 || attempt.ManifestSchemaRevision != 2 || attempt.Portable == nil || commit.Portable == nil ||
		manifest.GeneratorVersion != "2" || manifest.Digest != commit.ManifestIndexDigest {
		fixture.t.Fatal("persisted point does not contain compatible Portable V2 evidence")
	}
	return point, manifest, attempt, commit
}

func rcloneHTTPSHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (fixture *rcloneHTTPFixture) assertPortableHTTPPublication(
	events []rcloneHTTPCommandEvent, dataRoot, controlRoot string, sourceBytes []byte, sourceTime time.Time,
	commit provider.RcloneCommitV1, manifest model.RecoveryPointManifest,
) {
	fixture.t.Helper()
	read := func(name string) []byte {
		payload, err := os.ReadFile(name)
		if err != nil {
			fixture.t.Fatal("expected remote publication object is missing")
		}
		return payload
	}
	if !bytes.Equal(read(filepath.Join(dataRoot, "source.txt")), sourceBytes) ||
		string(read(filepath.Join(dataRoot, "nested", "child.txt"))) != "nested publication\n" ||
		string(read(filepath.Join(dataRoot, "link.rclonelink"))) != "nested/child.txt" {
		fixture.t.Fatal("remote SFTP data, nested file, or symlink target differs from the source")
	}
	if info, err := os.Stat(filepath.Join(dataRoot, "empty")); err != nil || !info.IsDir() {
		fixture.t.Fatal("remote empty directory was not preserved")
	}
	indexBytes := read(filepath.Join(controlRoot, "manifest-index.json"))
	if rcloneHTTPSHA256(indexBytes) != commit.ManifestIndexDigest {
		fixture.t.Fatal("remote V2 manifest index does not match the committed digest")
	}
	var index struct {
		Version                      int    `json:"version"`
		Generator                    string `json:"generator"`
		GeneratorVersion             string `json:"generator_version"`
		EntryCount                   uint64 `json:"entry_count"`
		LogicalBytes                 uint64 `json:"logical_bytes"`
		SourceObservationDigest      string `json:"source_observation_digest"`
		DestinationObservationDigest string `json:"destination_observation_digest"`
		Chunks                       []struct {
			Ordinal int    `json:"ordinal"`
			Digest  string `json:"digest"`
			Size    int64  `json:"size"`
		} `json:"chunks"`
		Fidelity struct {
			HashFidelity                 backupasset.RcloneHashFidelity `json:"hash_fidelity"`
			RequiresFullByteVerification bool                           `json:"requires_full_byte_verification"`
			EmptyDirectoriesPreserved    bool                           `json:"empty_directories_preserved"`
			SymlinkTargetsPreserved      bool                           `json:"symlink_targets_preserved"`
		} `json:"fidelity"`
		ContentProof struct {
			HashFidelity  backupasset.RcloneHashFidelity `json:"hash_fidelity"`
			VerifiedBytes uint64                         `json:"verified_bytes"`
			Complete      bool                           `json:"complete"`
		} `json:"content_proof"`
	}
	if err := json.Unmarshal(indexBytes, &index); err != nil || index.Version != 2 || index.Generator != "xirang-rclone-manifest" ||
		index.GeneratorVersion != "v2" || index.EntryCount != 5 || index.EntryCount != commit.ManifestEntryCount ||
		index.LogicalBytes != commit.LogicalBytes || index.SourceObservationDigest != commit.SourceObservationDigest ||
		index.DestinationObservationDigest != commit.DestinationObservationDigest || index.SourceObservationDigest == index.DestinationObservationDigest ||
		index.Fidelity.HashFidelity != backupasset.RcloneHashDownloadVerifiedBytes || !index.Fidelity.RequiresFullByteVerification ||
		!index.Fidelity.EmptyDirectoriesPreserved || !index.Fidelity.SymlinkTargetsPreserved ||
		index.ContentProof.HashFidelity != backupasset.RcloneHashDownloadVerifiedBytes || !index.ContentProof.Complete ||
		index.ContentProof.VerifiedBytes != index.LogicalBytes || manifest.GeneratorVersion != "2" {
		fixture.t.Fatal("remote V2 index did not prove the distinct observations and download verification")
	}
	entries := make(map[string]struct {
		Version int    `json:"version"`
		Path    string `json:"path"`
		Kind    string `json:"kind"`
		Source  struct {
			ModTime string `json:"mod_time"`
			SHA256  string `json:"sha256"`
		} `json:"source"`
		Destination struct {
			ModTime string `json:"mod_time"`
			SHA256  string `json:"sha256"`
		} `json:"destination"`
	})
	for ordinal, chunk := range index.Chunks {
		payload := read(filepath.Join(controlRoot, fmt.Sprintf("manifest-%06d.jsonl", ordinal)))
		if chunk.Ordinal != ordinal || chunk.Size != int64(len(payload)) || rcloneHTTPSHA256(payload) != chunk.Digest ||
			ordinal >= len(commit.ManifestChunkDigests) || chunk.Digest != commit.ManifestChunkDigests[ordinal] {
			fixture.t.Fatal("remote V2 manifest chunk does not match the committed index")
		}
		for _, line := range bytes.Split(bytes.TrimSuffix(payload, []byte("\n")), []byte("\n")) {
			var entry struct {
				Version int    `json:"version"`
				Path    string `json:"path"`
				Kind    string `json:"kind"`
				Source  struct {
					ModTime string `json:"mod_time"`
					SHA256  string `json:"sha256"`
				} `json:"source"`
				Destination struct {
					ModTime string `json:"mod_time"`
					SHA256  string `json:"sha256"`
				} `json:"destination"`
			}
			if json.Unmarshal(line, &entry) != nil || entry.Version != 2 || entry.Path == "" {
				fixture.t.Fatal("invalid remote V2 manifest entry")
			}
			if _, exists := entries[entry.Path]; exists {
				fixture.t.Fatal("duplicate remote V2 manifest entry")
			}
			entries[entry.Path] = entry
		}
	}
	if len(index.Chunks) == 0 || len(index.Chunks) != len(commit.ManifestChunkDigests) || len(entries) != 5 ||
		entries["source.txt"].Kind != "file" || entries["nested"].Kind != "directory" || entries["nested/child.txt"].Kind != "file" ||
		entries["empty"].Kind != "directory" || entries["link"].Kind != "symlink" {
		fixture.t.Fatal("remote V2 manifest did not contain the exact source tree")
	}
	fileEntry := entries["source.txt"]
	physical, err := os.Stat(filepath.Join(dataRoot, "source.txt"))
	destinationTime, timeErr := time.Parse(time.RFC3339Nano, fileEntry.Destination.ModTime)
	if err != nil || timeErr != nil || fileEntry.Source.ModTime != sourceTime.Format(time.RFC3339Nano) ||
		fileEntry.Source.SHA256 != rcloneHTTPSHA256(sourceBytes) || fileEntry.Destination.SHA256 != "" ||
		!destinationTime.Equal(physical.ModTime()) || destinationTime.Equal(sourceTime) {
		fixture.t.Fatal("remote V2 file did not preserve the logical and physical metadata separately")
	}
	var marker struct {
		Version  int    `json:"version"`
		Kind     string `json:"kind"`
		Document struct {
			ManifestIndexDigest string `json:"manifest_index_digest"`
		} `json:"document"`
	}
	if json.Unmarshal(read(filepath.Join(controlRoot, "commit.json")), &marker) != nil ||
		marker.Version != 1 || marker.Kind != "commit" || marker.Document.ManifestIndexDigest != commit.ManifestIndexDigest {
		fixture.t.Fatal("remote commit marker does not name the verified V2 index")
	}
	expected := []string{"attempt.json"}
	for ordinal := range index.Chunks {
		expected = append(expected, fmt.Sprintf("manifest-%06d.jsonl", ordinal))
	}
	expected = append(expected, "manifest-index.json", "commit.json")
	var written []string
	copied, downloaded, committed := false, false, false
	for position, event := range events {
		if committed && (event.operation == provider.OperationRcloneManagedCopy ||
			event.operation == provider.OperationRcloneManagedCopyTo || event.operation == provider.OperationRcloneManagedDeleteExactPrefix) {
			fixture.t.Fatal("a data or control mutation was attempted after the commit marker")
		}
		if event.operation == provider.OperationRcloneManagedCopy && event.succeeded {
			copied = true
		}
		if event.operation == provider.OperationRcloneManagedCheckDownload && event.succeeded {
			downloaded = copied
		}
		if event.operation != provider.OperationRcloneManagedCopyTo || !event.succeeded {
			continue
		}
		if event.scanErr != nil || len(event.created) != 1 || position+1 >= len(events) ||
			events[position+1].operation != provider.OperationRcloneManagedCat || !events[position+1].succeeded {
			fixture.t.Fatal("control write was not followed by a successful real readback")
		}
		if event.created[0] != "attempt.json" && !downloaded {
			fixture.t.Fatal("manifest or commit was written before the real download check")
		}
		written = append(written, event.created[0])
		committed = event.created[0] == "commit.json"
	}
	if !downloaded || !reflect.DeepEqual(written, expected) {
		fixture.t.Fatal("real remote controls were not written and read back with commit last")
	}
}

func (fixture *rcloneHTTPFixture) buildPortableHTTPCatalog(ctx context.Context, outcome publication.Outcome, sourceTime time.Time) (model.CatalogGeneration, model.CatalogEntry) {
	fixture.t.Helper()
	foundation := fixture.runtime.FoundationService()
	leaseConfig, err := foundation.LeaseConfig()
	if err != nil {
		fixture.t.Fatal(err)
	}
	catalogConfig, err := foundation.CatalogConfig()
	if err != nil {
		fixture.t.Fatal(err)
	}
	lease, err := backupasset.NewLeaseService(fixture.db, fixture.clock.Now, leaseConfig)
	if err != nil {
		fixture.t.Fatal(err)
	}
	indexer, err := catalog.NewIndexer(catalog.IndexerDependencies{
		DB: fixture.db, Factory: fixture.runtime.RepositoryService(), Lease: lease,
		IdentityKeys: backupasset.NewKeyring(fixture.db, fixture.clock.Now), Now: fixture.clock.Now,
		Config: catalog.IndexerConfig{
			BatchSize: catalogConfig.BatchSize, BuildTimeout: catalogConfig.BuildTimeout, MaxEntries: catalogConfig.MaxEntries,
			HeartbeatInterval: catalogConfig.Lease.Heartbeat,
		},
	})
	if err != nil {
		fixture.t.Fatal(err)
	}
	generation, err := indexer.Build(ctx, catalog.BuildRequest{
		RepositoryID: outcome.RepositoryID, RecoveryPointID: outcome.RecoveryPointID, CorrelationID: "rclone-v2-http",
	})
	if err != nil || generation.State != string(catalog.GenerationComplete) || !generation.IsActive {
		fixture.t.Fatalf("build real Portable Catalog failed: class=%s", rcloneHTTPSafeErrorClass(err))
	}
	var entries []model.CatalogEntry
	if err := fixture.db.Where("generation_id = ?", generation.ID).Find(&entries).Error; err != nil {
		fixture.t.Fatal(err)
	}
	var file model.CatalogEntry
	kinds := make(map[string]string)
	for _, entry := range entries {
		kinds[entry.NormalizedPath] = entry.EntryType
		if entry.NormalizedPath == "source.txt" {
			file = entry
		}
	}
	if len(entries) != 5 || kinds["source.txt"] != string(backupasset.CatalogEntryFile) ||
		kinds["nested"] != string(backupasset.CatalogEntryDirectory) || kinds["nested/child.txt"] != string(backupasset.CatalogEntryFile) ||
		kinds["empty"] != string(backupasset.CatalogEntryDirectory) || kinds["link"] != string(backupasset.CatalogEntrySymlink) ||
		file.ModifiedAt == nil || !file.ModifiedAt.Equal(sourceTime) || file.Mode != "100640" || file.SecurityState != "sealed" {
		fixture.t.Fatal("real Catalog did not retain the complete source tree and logical metadata")
	}
	return generation, file
}

func portableHTTPContentRequest(point model.RecoveryPoint, generation model.CatalogGeneration, entry model.CatalogEntry) content.SourceRequest {
	return content.SourceRequest{
		Ref: backupasset.AssetRef{RecoveryPointID: point.ID, EntryID: entry.EntryID}, CatalogGenerationID: generation.ID,
		ExpectedSource: point.SourceFingerprint, ExpectedEntry: entry.Fingerprint,
		Mode: content.SourceModeSequential, MaxBytes: entry.Size,
	}
}

func (fixture *rcloneHTTPFixture) readPortableHTTPContent(
	ctx context.Context, point model.RecoveryPoint, generation model.CatalogGeneration, entry model.CatalogEntry, expected []byte, sourceTime time.Time, verifyRange bool,
) {
	fixture.t.Helper()
	request := portableHTTPContentRequest(point, generation, entry)
	session, err := fixture.runtime.RepositoryService().OpenContentSource(ctx, request)
	if err != nil || session == nil {
		fixture.t.Fatalf("open real Portable content failed: class=%s", rcloneHTTPSafeErrorClass(err))
	}
	stat, capabilities := session.Stat(), session.Capabilities()
	if stat.ModifiedAt == nil || !stat.ModifiedAt.Equal(sourceTime) || stat.Size != int64(len(expected)) ||
		!capabilities.Sequential || session.Reader() == nil {
		_ = session.Close()
		fixture.t.Fatal("real content stat did not retain source metadata and sequential capability")
	}
	payload, readErr := io.ReadAll(session.Reader())
	closeErr := session.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(payload, expected) {
		fixture.t.Fatalf("read and close real Portable content failed: read=%s close=%s",
			rcloneHTTPSafeErrorClass(readErr), rcloneHTTPSafeErrorClass(closeErr))
	}
	if !verifyRange {
		return
	}
	request.Mode = content.SourceModeRange
	request.MaxBytes = 5
	request.Range = &content.ResolvedRange{Offset: 2, Length: 5}
	ranged, rangeErr := fixture.runtime.RepositoryService().OpenContentSource(ctx, request)
	if !capabilities.Range {
		reason, _, ok := repository.CapabilityFromError(rangeErr)
		if rangeErr == nil || ranged != nil || !ok || reason.Code != backupasset.CapabilityRangeUnavailable ||
			capabilities.Reason == nil || capabilities.Reason.Code != backupasset.CapabilityRangeUnavailable {
			if ranged != nil {
				_ = ranged.Close()
			}
			fixture.t.Fatal("unproven range did not report the existing capability reason")
		}
		return
	}
	if rangeErr != nil || ranged == nil || ranged.Reader() == nil {
		fixture.t.Fatalf("open proven Portable range failed: class=%s", rcloneHTTPSafeErrorClass(rangeErr))
	}
	rangeBytes, readErr := io.ReadAll(ranged.Reader())
	closeErr = ranged.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(rangeBytes, expected[2:7]) {
		fixture.t.Fatal("proven Portable range did not preserve its exact boundaries")
	}
}

func assertRcloneHTTPHistoricalReadTrace(t *testing.T, events []rcloneHTTPCommandEvent, pointDeadline time.Time, timeout time.Duration) {
	t.Helper()
	cat, recursive := false, false
	for _, event := range events {
		if event.operation != provider.OperationRcloneManagedCat && event.operation != provider.OperationRcloneManagedRecursiveList {
			continue
		}
		if !event.succeeded || event.purpose != provider.CommandPurposeRead || !event.started.After(pointDeadline) ||
			!event.deadline.After(event.started) || !event.deadline.After(pointDeadline) || event.deadline.After(event.started.Add(timeout+time.Second)) {
			t.Fatal("historical control or recursive observation did not use a new bounded read deadline")
		}
		cat = cat || event.operation == provider.OperationRcloneManagedCat
		recursive = recursive || event.operation == provider.OperationRcloneManagedRecursiveList
	}
	if !cat || !recursive {
		t.Fatal("historical content did not execute real control and recursive observation reads")
	}
}

func rcloneHTTPBindingDocument(t *testing.T, db *gorm.DB, repositoryID string) map[string]any {
	t.Helper()
	var binding model.RepositoryAccessBinding
	if err := db.Where("repository_id = ? AND status = ?", repositoryID, "active").First(&binding).Error; err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(binding.EncryptedConfig), &document); err != nil {
		t.Fatal("active binding is not a managed document")
	}
	return document
}

func (fixture *rcloneHTTPFixture) publishRclone(id uint) publication.Outcome {
	fixture.t.Helper()
	var task model.Task
	if err := fixture.db.Preload("Node").First(&task, id).Error; err != nil {
		fixture.t.Fatal(err)
	}
	started := fixture.clock.Now()
	run := model.TaskRun{TaskID: id, NodeIDSnapshot: task.NodeID, TriggerType: "manual", Status: model.TaskRunStatusRunning, StartedAt: &started}
	if err := fixture.db.Create(&run).Error; err != nil {
		fixture.t.Fatal(err)
	}
	audit, err := backupasset.NewSystemPublicationAuditContext("rclone-runtime-http-publication")
	if err != nil {
		fixture.t.Fatal(err)
	}
	execution, err := fixture.runtime.PublicationCoordinator().Prepare(context.Background(), publication.Run{
		Task: task, TaskRunID: run.ID, Trigger: "manual", StartedAt: started, Audit: audit,
	})
	if err != nil {
		fixture.t.Fatalf("prepare real publication failed: class=%s error_type=%T", rcloneHTTPSafeErrorClass(err), err)
	}
	defer func() { _ = execution.Abandon(backupasset.ErrPublicationSessionAbandoned) }()
	inputSource, ok := execution.(interface {
		RclonePublicationInput() (provider.RclonePublicationInput, error)
	})
	if !ok || execution.Mode() != publication.ModeEvidence || execution.Attempt() == nil {
		fixture.t.Fatal("real runtime did not provide a managed Rclone execution")
	}
	input, err := inputSource.RclonePublicationInput()
	if err != nil {
		fixture.t.Fatalf("obtain managed publication input failed: class=%s error_type=%T", rcloneHTTPSafeErrorClass(err), err)
	}
	factory := executor.NewFactoryWithPublicationStrategies("rsync", nil, nil, fixture.runtime.RclonePublicationStrategy())
	worker, ok := factory.Resolve("rclone").(executor.PublicationExecutor)
	if !ok {
		fixture.t.Fatal("real Rclone publication executor unavailable")
	}
	phase := "strategy_prepare"
	if fixture.trace != nil {
		fixture.trace.setPhase(phase)
	}
	logf := func(level, message string) {
		switch {
		case level == "info" && message == "开始受管 Rclone 恢复点发布":
			phase = "strategy_execute"
		case level == "info" && message == "受管 Rclone 传输与证据提交已结束":
			phase = "strategy_record_commit"
		}
		if fixture.trace != nil {
			fixture.trace.setPhase(phase)
		}
	}
	result, err := worker.RunWithPublication(execution.Context(), executor.PublicationExecutionRequest{
		Task: task, TaskRunID: run.ID, Attempt: *execution.Attempt(), RcloneInput: &input,
	}, logf, nil)
	if fixture.trace != nil {
		fixture.trace.setPhase("")
	}
	if err != nil || result.Completion != backupasset.CompletionKnownExitZero || result.ProviderCommit == nil {
		fixture.writePrivatePublicationDiagnostic(phase, err)
		fixture.t.Fatalf("real managed publication did not complete: phase=%s class=%s error_type=%T exit_zero=%t commit_present=%t",
			phase, rcloneHTTPSafeErrorClass(err), err, result.Completion == backupasset.CompletionKnownExitZero, result.ProviderCommit != nil)
	}
	outcome, err := execution.RecordProviderCommit(context.Background(), *result.ProviderCommit)
	if err != nil || !outcome.ProviderCommitRecorded || outcome.State != backupasset.RecoveryPointVerifying {
		fixture.t.Fatalf("record real managed publication failed: class=%s error_type=%T recorded=%t verifying=%t",
			rcloneHTTPSafeErrorClass(err), err, outcome.ProviderCommitRecorded, outcome.State == backupasset.RecoveryPointVerifying)
	}
	finished := fixture.clock.Now()
	if err := fixture.db.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{"status": model.TaskRunStatusSuccess, "finished_at": finished}).Error; err != nil {
		fixture.t.Fatal(err)
	}
	outcome, err = fixture.runtime.PublicationReconciler().ProcessPoint(context.Background(), outcome.RecoveryPointID)
	if err != nil || outcome.State != backupasset.RecoveryPointCommitted {
		fixture.t.Fatalf("verify real managed recovery point failed: class=%s error_type=%T committed=%t",
			rcloneHTTPSafeErrorClass(err), err, outcome.State == backupasset.RecoveryPointCommitted)
	}
	return outcome
}

// Classify failures without printing remote output, private locators, or key material.
func rcloneHTTPSafeErrorClass(err error) string {
	if err == nil {
		return "none"
	}
	if reason, _, ok := repository.CapabilityFromError(err); ok {
		return "capability:" + string(reason.Code)
	}
	switch {
	case errors.Is(err, provider.ErrRcloneManifestLimitExceeded):
		return "rclone_manifest_limit"
	case errors.Is(err, provider.ErrRcloneManifestPathUnsafe):
		return "rclone_manifest_path_unsafe"
	case errors.Is(err, provider.ErrRcloneManifestSpecialFile):
		return "rclone_manifest_special_file"
	case errors.Is(err, provider.ErrRcloneManifestFullByteProofRequired):
		return "rclone_full_byte_proof_required"
	case errors.Is(err, provider.ErrRcloneSourceDrift):
		return "rclone_source_drift"
	case errors.Is(err, provider.ErrRcloneDestinationUnstable):
		return "rclone_destination_unstable"
	case errors.Is(err, provider.ErrRcloneManifestObservationMismatch):
		return "rclone_manifest_observation_mismatch"
	case errors.Is(err, provider.ErrRcloneAttemptCollision):
		return "rclone_attempt_collision"
	case errors.Is(err, provider.ErrRcloneAttemptPresenceUnknown):
		return "rclone_attempt_presence_unknown"
	case errors.Is(err, provider.ErrRcloneCommitOutcomeUnknown):
		return "rclone_commit_outcome_unknown"
	case errors.Is(err, provider.ErrRcloneMarkerMismatch):
		return "rclone_marker_mismatch"
	case errors.Is(err, provider.ErrRcloneCopyDestRetryRequired):
		return "rclone_copy_dest_retry_required"
	case errors.Is(err, provider.ErrRcloneRemoteObjectNotFound):
		return "rclone_remote_object_not_found"
	case errors.Is(err, backupasset.ErrConflict):
		return "conflict"
	case errors.Is(err, backupasset.ErrForbidden):
		return "forbidden"
	case errors.Is(err, backupasset.ErrNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		return "not_found"
	case errors.Is(err, backupasset.ErrInvalidState), errors.Is(err, backupasset.ErrInvalidAssetRef):
		return "invalid_state"
	case errors.Is(err, backupasset.ErrCapabilityUnavailable):
		return "capability_unavailable"
	case errors.Is(err, backupasset.ErrKeyLost), errors.Is(err, backupasset.ErrKeyUnavailable):
		return "key_unavailable"
	case errors.Is(err, backupasset.ErrLeaseHeld), errors.Is(err, backupasset.ErrLeaseFenceLost), errors.Is(err, backupasset.ErrLeaseDeadlineExceeded):
		return "lease"
	case errors.Is(err, backupasset.ErrPublicationInProgress), errors.Is(err, backupasset.ErrPublicationUnconfirmed):
		return "publication"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "deadline_or_cancelled"
	default:
		return "other"
	}
}

// Opt-in diagnostics remain in the caller's private scratch directory and are
// never printed: provider error text may include private paths or credentials.
func (fixture *rcloneHTTPFixture) writePrivatePublicationDiagnostic(phase string, failure error) {
	fixture.t.Helper()
	directory := os.Getenv("XIRANG_RCLONE_HTTP_DIAGNOSTIC_DIR")
	if directory == "" {
		return
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		fixture.t.Error("private publication diagnostic directory is unavailable or not private")
		return
	}
	file, err := os.CreateTemp(directory, "rclone-http-publication-*.json")
	if err != nil {
		fixture.t.Error("could not create private publication diagnostic")
		return
	}
	text := ""
	if failure != nil {
		text = failure.Error()
	}
	if len(text) > 16<<10 {
		text = text[:16<<10]
	}
	payload := map[string]string{
		"phase": phase, "class": rcloneHTTPSafeErrorClass(failure),
		"error_type": fmt.Sprintf("%T", failure), "error": text,
	}
	writeErr := json.NewEncoder(file).Encode(payload)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		fixture.t.Error("could not write private publication diagnostic")
		return
	}
	fixture.t.Log("private publication diagnostic recorded")
}
