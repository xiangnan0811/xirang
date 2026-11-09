package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/task"
	"xirang/backend/internal/ws"
)

const (
	generalSurfaceAdminUsername      = "admin"
	generalSurfaceOperatorUsername   = "general-operator"
	generalSurfacePostgresRequireEnv = "REQUIRE_POSTGRES_GENERAL_TEST"
	generalSurfacePostgresPassword   = "FAKE_GeneralPostgresPass2026!_FOR_TEST_ONLY"
	generalSurfaceOperatorPassword   = "FAKE_GeneralOperatorPass2026!_FOR_TEST_ONLY"
	generalSurfaceViewerUsername     = "general-viewer"
	generalSurfaceViewerPassword     = "FAKE_GeneralViewerPass2026!_FOR_TEST_ONLY"
	generalSurfacePostgresJWTSecret  = "FAKE_GENERAL_POSTGRES_JWT_SECRET_2026_FOR_TEST_ONLY"
)

type generalSurfaceLoginResponse struct {
	Code int `json:"code"`
	Data struct {
		Token string `json:"token"`
		User  struct {
			ID          uint   `json:"id"`
			Username    string `json:"username"`
			Role        string `json:"role"`
			TOTPEnabled bool   `json:"totp_enabled"`
		} `json:"user"`
	} `json:"data"`
}

type generalSurfaceMeResponse struct {
	Code int `json:"code"`
	Data struct {
		User struct {
			ID       uint   `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	} `json:"data"`
}

type generalSurfaceTaskResponse struct {
	ID           uint   `json:"id"`
	Name         string `json:"name"`
	NodeID       uint   `json:"node_id"`
	ExecutorType string `json:"executor_type"`
	Status       string `json:"status"`
	Revision     string `json:"revision"`
}

type generalSurfaceCreateTaskResponse struct {
	Code int                        `json:"code"`
	Data generalSurfaceTaskResponse `json:"data"`
}

type generalSurfaceTasksResponse struct {
	Code     int                          `json:"code"`
	Data     []generalSurfaceTaskResponse `json:"data"`
	Total    int64                        `json:"total"`
	Page     int                          `json:"page"`
	PageSize int                          `json:"page_size"`
}

// TestGeneralSurfacePostgres intentionally exercises the complete production
// database opening/migration path and a small authenticated API flow. It uses
// a per-test PostgreSQL schema so it never reads or mutates the service schema
// (or the schemas used by the backup-asset parity tests).
func TestGeneralSurfacePostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv(generalSurfacePostgresRequireEnv)) == "1" {
			t.Fatalf("TEST_POSTGRES_DSN is required when %s=1", generalSurfacePostgresRequireEnv)
		}
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	parsedDSN, err := url.Parse(dsn)
	if err != nil || (parsedDSN.Scheme != "postgres" && parsedDSN.Scheme != "postgresql") {
		t.Fatalf("TEST_POSTGRES_DSN must be a PostgreSQL URL: %v", err)
	}

	baseDB, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: dsn})
	if err != nil {
		t.Fatalf("open PostgreSQL base connection: %v", err)
	}
	baseSQL, err := baseDB.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL base connection: %v", err)
	}
	t.Cleanup(func() { _ = baseSQL.Close() })
	if err := baseSQL.Ping(); err != nil {
		t.Fatalf("ping PostgreSQL base connection: %v", err)
	}

	schema := fmt.Sprintf("xirang_general_%d", time.Now().UTC().UnixNano())
	if _, err := baseSQL.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatalf("create isolated PostgreSQL schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		if _, err := baseSQL.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Errorf("drop isolated PostgreSQL schema %s: %v", schema, err)
		}
	})

	scopedDSN := *parsedDSN
	scopedQuery := scopedDSN.Query()
	scopedQuery.Set("search_path", schema)
	scopedQuery.Set("timezone", "UTC")
	scopedDSN.RawQuery = scopedQuery.Encode()
	db, err := database.Open(config.Config{DBType: "postgres", PostgresDSN: scopedDSN.String()})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL connection: %v", err)
	}
	scopedSQL, err := db.DB()
	if err != nil {
		t.Fatalf("get isolated PostgreSQL connection: %v", err)
	}
	// Registered after schema cleanup so the scoped pool closes before DROP.
	t.Cleanup(func() { _ = scopedSQL.Close() })

	if err := database.RunMigrations(db, "postgres"); err != nil {
		t.Fatalf("run PostgreSQL migrations in isolated schema: %v", err)
	}
	var migrationVersion int64
	if err := db.Raw("SELECT version FROM schema_migrations LIMIT 1").Scan(&migrationVersion).Error; err != nil {
		t.Fatalf("read PostgreSQL migration version: %v", err)
	}
	if migrationVersion < 73 {
		t.Fatalf("migration version=%d, want current schema version >= 73", migrationVersion)
	}

	t.Setenv("APP_ENV", "development")
	t.Setenv("DATA_ENCRYPTION_KEY", "FAKE_GENERAL_SURFACE_DATA_KEY_FOR_TEST_ONLY")
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)
	t.Setenv("ADMIN_INITIAL_PASSWORD", generalSurfacePostgresPassword)
	if err := bootstrap.SeedUsers(db); err != nil {
		t.Fatalf("seed deterministic PostgreSQL admin: %v", err)
	}

	nodeEntity := model.Node{
		Name:      "general-postgres-node",
		Host:      "127.0.0.1",
		Port:      22,
		Username:  "root",
		AuthType:  "key",
		Status:    "offline",
		BackupDir: "/tmp/general-postgres-backup",
	}
	if err := db.Create(&nodeEntity).Error; err != nil {
		t.Fatalf("seed PostgreSQL API node: %v", err)
	}

	taskManager := task.NewManager(db, nil, nil, nil, nil, nil, 8, 90)
	t.Cleanup(func() {
		shutdownContext, shutdown := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutdown()
		_ = taskManager.Shutdown(shutdownContext)
	})

	appContext, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	jwtManager := auth.NewJWTManager(generalSurfacePostgresJWTSecret, time.Hour)
	jwtManager.SetDB(db)
	authService := auth.NewService(db, jwtManager, nil, auth.LoginSecurityConfig{
		FailLockThreshold:       5,
		FailLockDuration:        time.Minute,
		GlobalFailLockThreshold: 50,
		GlobalFailLockDuration:  time.Minute,
	})

	if _, err := authService.CreateUser(generalSurfaceOperatorUsername, generalSurfaceOperatorPassword, "operator"); err != nil {
		t.Fatalf("create deterministic PostgreSQL operator: %v", err)
	}
	if _, err := authService.CreateUser(generalSurfaceViewerUsername, generalSurfaceViewerPassword, "viewer"); err != nil {
		t.Fatalf("create deterministic PostgreSQL viewer: %v", err)
	}
	hub := ws.NewHub(db, nil, false)
	router := NewRouter(Dependencies{
		AppContext:      appContext,
		DB:              db,
		AuthService:     authService,
		JWTManager:      jwtManager,
		TaskManager:     taskManager,
		Hub:             hub,
		AllowedOrigins:  []string{},
		LoginRateLimit:  20,
		LoginRateWindow: time.Minute,
		TrustedProxies:  []string{},
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 10 * time.Second

	readyStatus, readyBody := generalSurfaceRequest(t, client, server.URL, http.MethodGet, "/readyz", "", nil)
	if readyStatus != http.StatusOK {
		t.Fatalf("PostgreSQL readiness status=%d body=%s", readyStatus, readyBody)
	}
	var readiness struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(readyBody, &readiness); err != nil {
		t.Fatalf("decode PostgreSQL readiness response: %v; body=%s", err, readyBody)
	}
	if readiness.Status != "ready" {
		t.Fatalf("unexpected PostgreSQL readiness response: %+v", readiness)
	}

	authFailureStatus, authFailureBody := generalSurfaceRequest(t, client, server.URL, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": generalSurfaceAdminUsername,
		"password": "FAKE_WrongGeneralPostgresPass2026!_FOR_TEST_ONLY",
	})
	if authFailureStatus != http.StatusUnauthorized {
		t.Fatalf("PostgreSQL invalid-credentials status=%d body=%s", authFailureStatus, authFailureBody)
	}
	var authFailure struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(authFailureBody, &authFailure); err != nil {
		t.Fatalf("decode PostgreSQL invalid-credentials response: %v; body=%s", err, authFailureBody)
	}
	if authFailure.Code != http.StatusUnauthorized || strings.TrimSpace(authFailure.Message) == "" {
		t.Fatalf("unexpected PostgreSQL invalid-credentials envelope: %+v", authFailure)
	}

	loginStatus, loginBody := generalSurfaceRequest(t, client, server.URL, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": generalSurfaceAdminUsername,
		"password": generalSurfacePostgresPassword,
	})
	if loginStatus != http.StatusOK {
		t.Fatalf("PostgreSQL login status=%d body=%s", loginStatus, loginBody)
	}
	var login generalSurfaceLoginResponse
	if err := json.Unmarshal(loginBody, &login); err != nil {
		t.Fatalf("decode PostgreSQL login response: %v; body=%s", err, loginBody)
	}
	if login.Code != http.StatusOK || login.Data.Token == "" {
		t.Fatalf("unexpected PostgreSQL login envelope: %+v", login)
	}
	if login.Data.User.Username != generalSurfaceAdminUsername || login.Data.User.Role != "admin" || login.Data.User.ID == 0 {
		t.Fatalf("unexpected PostgreSQL login user: %+v", login.Data.User)
	}

	meStatus, meBody := generalSurfaceRequest(t, client, server.URL, http.MethodGet, "/api/v1/me", login.Data.Token, nil)
	if meStatus != http.StatusOK {
		t.Fatalf("PostgreSQL /me status=%d body=%s", meStatus, meBody)
	}
	var me generalSurfaceMeResponse
	if err := json.Unmarshal(meBody, &me); err != nil {
		t.Fatalf("decode PostgreSQL /me response: %v; body=%s", err, meBody)
	}
	if me.Code != http.StatusOK || me.Data.User.Username != generalSurfaceAdminUsername || me.Data.User.Role != "admin" {
		t.Fatalf("unexpected PostgreSQL /me envelope: %+v", me)
	}

	operatorLoginStatus, operatorLoginBody := generalSurfaceRequest(t, client, server.URL, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": generalSurfaceOperatorUsername,
		"password": generalSurfaceOperatorPassword,
	})
	if operatorLoginStatus != http.StatusOK {
		t.Fatalf("PostgreSQL operator login status=%d body=%s", operatorLoginStatus, operatorLoginBody)
	}
	var operatorLogin generalSurfaceLoginResponse
	if err := json.Unmarshal(operatorLoginBody, &operatorLogin); err != nil {
		t.Fatalf("decode PostgreSQL operator login response: %v; body=%s", err, operatorLoginBody)
	}
	if operatorLogin.Code != http.StatusOK || operatorLogin.Data.Token == "" ||
		operatorLogin.Data.User.Username != generalSurfaceOperatorUsername ||
		operatorLogin.Data.User.Role != "operator" || operatorLogin.Data.User.ID == 0 {
		t.Fatalf("unexpected PostgreSQL operator login user: %+v", operatorLogin)
	}

	viewerLoginStatus, viewerLoginBody := generalSurfaceRequest(t, client, server.URL, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": generalSurfaceViewerUsername,
		"password": generalSurfaceViewerPassword,
	})
	if viewerLoginStatus != http.StatusOK {
		t.Fatalf("PostgreSQL viewer login status=%d body=%s", viewerLoginStatus, viewerLoginBody)
	}
	var viewerLogin generalSurfaceLoginResponse
	if err := json.Unmarshal(viewerLoginBody, &viewerLogin); err != nil {
		t.Fatalf("decode PostgreSQL viewer login response: %v; body=%s", err, viewerLoginBody)
	}
	if viewerLogin.Code != http.StatusOK || viewerLogin.Data.Token == "" ||
		viewerLogin.Data.User.Username != generalSurfaceViewerUsername ||
		viewerLogin.Data.User.Role != "viewer" || viewerLogin.Data.User.ID == 0 {
		t.Fatalf("unexpected PostgreSQL viewer login user: %+v", viewerLogin)
	}

	rbacStatus, rbacBody := generalSurfaceRequest(t, client, server.URL, http.MethodGet, "/api/v1/settings", operatorLogin.Data.Token, nil)
	if rbacStatus != http.StatusForbidden {
		t.Fatalf("PostgreSQL operator admin-only settings status=%d body=%s", rbacStatus, rbacBody)
	}

	const taskName = "postgres-general-surface-task"

	invalidTaskStatus, invalidTaskBody := generalSurfaceRequest(t, client, server.URL, http.MethodPost, "/api/v1/tasks", login.Data.Token, map[string]any{
		"name":          "postgres-general-surface-invalid",
		"node_id":       nodeEntity.ID,
		"executor_type": "command",
	})
	if invalidTaskStatus != http.StatusBadRequest {
		t.Fatalf("PostgreSQL invalid task status=%d body=%s", invalidTaskStatus, invalidTaskBody)
	}
	var invalidTaskCount int64
	if err := db.Model(&model.Task{}).Where("name = ?", "postgres-general-surface-invalid").Count(&invalidTaskCount).Error; err != nil {
		t.Fatalf("count PostgreSQL invalid task rows: %v", err)
	}
	if invalidTaskCount != 0 {
		t.Fatalf("invalid PostgreSQL task should not persist, rows=%d", invalidTaskCount)
	}

	createStatus, createBody := generalSurfaceRequest(t, client, server.URL, http.MethodPost, "/api/v1/tasks", login.Data.Token, map[string]any{
		"name":          taskName,
		"node_id":       nodeEntity.ID,
		"executor_type": "command",
		"command":       "printf postgres-general-surface",
	})
	if createStatus != http.StatusCreated {
		t.Fatalf("PostgreSQL task create status=%d body=%s", createStatus, createBody)
	}
	var created generalSurfaceCreateTaskResponse
	if err := json.Unmarshal(createBody, &created); err != nil {
		t.Fatalf("decode PostgreSQL task create response: %v; body=%s", err, createBody)
	}
	if created.Code != http.StatusCreated || created.Data.ID == 0 || created.Data.Name != taskName || created.Data.NodeID != nodeEntity.ID || created.Data.ExecutorType != "command" || created.Data.Revision == "" {
		t.Fatalf("unexpected PostgreSQL task create envelope: %+v", created)
	}

	var persisted model.Task
	if err := db.First(&persisted, created.Data.ID).Error; err != nil {
		t.Fatalf("read PostgreSQL task persisted by API: %v", err)
	}
	if persisted.Name != taskName || persisted.NodeID != nodeEntity.ID || persisted.ExecutorType != "command" || persisted.Status != "pending" {
		t.Fatalf("unexpected PostgreSQL persisted task: %+v", persisted)
	}

	listStatus, listBody := generalSurfaceRequest(t, client, server.URL, http.MethodGet, "/api/v1/tasks?keyword="+url.QueryEscape(taskName), login.Data.Token, nil)
	if listStatus != http.StatusOK {
		t.Fatalf("PostgreSQL task list status=%d body=%s", listStatus, listBody)
	}
	var listed generalSurfaceTasksResponse
	if err := json.Unmarshal(listBody, &listed); err != nil {
		t.Fatalf("decode PostgreSQL task list response: %v; body=%s", err, listBody)
	}
	if listed.Code != http.StatusOK || listed.Total != 1 || len(listed.Data) != 1 {
		t.Fatalf("unexpected PostgreSQL task list envelope: %+v", listed)
	}
	if listed.Data[0].ID != created.Data.ID || listed.Data[0].Name != taskName || listed.Data[0].NodeID != nodeEntity.ID {
		t.Fatalf("unexpected PostgreSQL listed task: %+v", listed.Data[0])
	}

	const updatedTaskName = "postgres-general-surface-task-updated"
	updateStatus, updateBody := generalSurfaceRequest(t, client, server.URL, http.MethodPut,
		fmt.Sprintf("/api/v1/tasks/%d", created.Data.ID), login.Data.Token, map[string]any{
			"name":              updatedTaskName,
			"node_id":           nodeEntity.ID,
			"executor_type":     "command",
			"command":           "printf postgres-general-surface-updated",
			"expected_revision": created.Data.Revision,
		})
	if updateStatus != http.StatusOK {
		t.Fatalf("PostgreSQL task update status=%d body=%s", updateStatus, updateBody)
	}
	var updated generalSurfaceCreateTaskResponse
	if err := json.Unmarshal(updateBody, &updated); err != nil {
		t.Fatalf("decode PostgreSQL task update response: %v; body=%s", err, updateBody)
	}
	if updated.Code != http.StatusOK || updated.Data.ID != created.Data.ID || updated.Data.Name != updatedTaskName ||
		updated.Data.ExecutorType != "command" {
		t.Fatalf("unexpected PostgreSQL task update envelope: %+v", updated)
	}
	if err := db.First(&persisted, created.Data.ID).Error; err != nil {
		t.Fatalf("read PostgreSQL task after update: %v", err)
	}
	if persisted.Name != updatedTaskName || persisted.Command != "printf postgres-general-surface-updated" ||
		persisted.Status != "pending" {
		t.Fatalf("unexpected PostgreSQL task after update: %+v", persisted)
	}

	// Exercise archive through the real router with linked metadata and history.
	if err := db.Model(&model.Task{}).Where("id = ?", created.Data.ID).Update("enabled", true).Error; err != nil {
		t.Fatalf("enable archive fixture task: %v", err)
	}
	fixtureTime := time.Date(2026, 8, 18, 15, 0, 0, 0, time.UTC)
	repository := model.BackupRepository{
		ID: "dddddddddddddddddddddddddddddddd", ProviderKind: "restic", DisplayName: "general-archive-repo",
		VersionMode: "native_snapshot", Status: "online", CapabilityRevision: 1, CapabilitiesJSON: `{}`,
		ImmutabilityLevel: "backend_versioned",
	}
	if err := db.Create(&repository).Error; err != nil {
		t.Fatalf("create archive repository: %v", err)
	}
	link := model.TaskRepositoryLink{
		ID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", TaskID: &created.Data.ID, RepositoryID: repository.ID,
		TaskNameSnapshot: updatedTaskName, NodeIDSnapshot: nodeEntity.ID, NodeNameSnapshot: nodeEntity.Name,
		PublicationMode: "native_snapshot", EncryptedLegacyLocator: "sftp:general@example.invalid:/protected/legacy",
		LinkedAt: fixtureTime, CreatedAt: fixtureTime, UpdatedAt: fixtureTime,
	}
	if err := db.Create(&link).Error; err != nil {
		t.Fatalf("create archive link: %v", err)
	}
	policy := model.BackupRetentionPolicy{
		ID: "12121212121212121212121212121212", ScopeKind: "task_link", ScopeID: link.ID, Revision: 1,
		RulesJSON: `{"version":1,"age":{"keep_days":7}}`, Status: "active",
		CreatedBy: login.Data.User.ID, UpdatedBy: login.Data.User.ID, CreatedAt: fixtureTime, UpdatedAt: fixtureTime,
	}
	if err := db.Create(&policy).Error; err != nil {
		t.Fatalf("create archive retention policy: %v", err)
	}
	run := model.TaskRun{TaskID: created.Data.ID, NodeIDSnapshot: nodeEntity.ID, TriggerType: "manual", Status: "success"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatalf("create archive task run: %v", err)
	}
	logEntry := model.TaskLog{TaskID: created.Data.ID, TaskRunID: &run.ID, Level: "info", Message: "archive route history"}
	if err := db.Create(&logEntry).Error; err != nil {
		t.Fatalf("create archive task log: %v", err)
	}
	point := model.RecoveryPoint{
		ID: "ffffffffffffffffffffffffffffffff", RepositoryID: repository.ID, ProducingTaskID: &created.Data.ID, ProducingTaskRunID: &run.ID,
		ProducingTaskNameSnapshot: updatedTaskName, ProducingNodeIDSnapshot: nodeEntity.ID, ProducingNodeNameSnapshot: nodeEntity.Name,
		LineageJSON: `{"version":1}`, EncryptedProviderLocator: `{"snapshot":"keep"}`,
		Semantics: "native_snapshot", State: "committed", CapturedAt: &fixtureTime, CommittedAt: &fixtureTime,
		PointRevision: 1, CapabilityRevision: 1, CapabilitiesJSON: `{}`, ImmutabilityLevel: "backend_versioned",
		PhysicalAvailability: "online", HoldState: "none",
	}
	if err := db.Create(&point).Error; err != nil {
		t.Fatalf("create archive recovery point: %v", err)
	}
	unownedNode := model.Node{Name: "general-postgres-unowned", Host: "127.0.0.2", Port: 22, Username: "root", AuthType: "key", Status: "offline", BackupDir: "/tmp/general-postgres-unowned"}
	if err := db.Create(&unownedNode).Error; err != nil {
		t.Fatalf("create unowned dependency node: %v", err)
	}
	dependent := model.Task{Name: "general-archive-dependent", NodeID: unownedNode.ID, DependsOnTaskID: &created.Data.ID, ExecutorType: "command", Command: "true", Status: "pending", Enabled: true}
	if err := db.Create(&dependent).Error; err != nil {
		t.Fatalf("create live archive dependent: %v", err)
	}

	type archiveState struct {
		Task       model.Task
		Link       model.TaskRepositoryLink
		Policy     model.BackupRetentionPolicy
		Repository model.BackupRepository
		Point      model.RecoveryPoint
		Run        model.TaskRun
		Log        model.TaskLog
		Audits     int64
	}
	readArchiveState := func(t *testing.T) archiveState {
		t.Helper()
		var state archiveState
		for _, query := range []struct {
			value any
			id    any
		}{
			{&state.Task, created.Data.ID}, {&state.Link, link.ID}, {&state.Policy, policy.ID},
			{&state.Repository, repository.ID}, {&state.Point, point.ID}, {&state.Run, run.ID}, {&state.Log, logEntry.ID},
		} {
			if err := db.First(query.value, "id = ?", query.id).Error; err != nil {
				t.Fatalf("read archive state %T: %v", query.value, err)
			}
		}
		if err := db.Model(&model.BackupAssetAuditEvent{}).Where("action = ? AND repository_id = ?", "retention_policy_delete", repository.ID).Count(&state.Audits).Error; err != nil {
			t.Fatalf("count archive policy audits: %v", err)
		}
		return state
	}
	before := readArchiveState(t)
	if !before.Task.Enabled || before.Task.ArchivedAt != nil || before.Link.UnlinkedAt != nil || before.Policy.Status != "active" || before.Audits != 0 {
		t.Fatalf("invalid initial archive fixture: %+v", before)
	}
	if before.Link.TaskID == nil || *before.Link.TaskID != created.Data.ID || before.Link.RepositoryID != repository.ID ||
		before.Point.ProducingTaskID == nil || *before.Point.ProducingTaskID != created.Data.ID ||
		before.Point.ProducingTaskRunID == nil || *before.Point.ProducingTaskRunID != run.ID || before.Point.RepositoryID != repository.ID ||
		before.Run.TaskID != created.Data.ID || before.Log.TaskID != created.Data.ID || before.Log.TaskRunID == nil || *before.Log.TaskRunID != run.ID {
		t.Fatalf("archive fixture has incorrect producer references: %+v", before)
	}
	assertUnchanged := func(t *testing.T) {
		t.Helper()
		if after := readArchiveState(t); !reflect.DeepEqual(before, after) {
			t.Fatalf("rejected archive changed persisted state:\nbefore=%+v\nafter=%+v", before, after)
		}
	}
	taskPath := fmt.Sprintf("/api/v1/tasks/%d", created.Data.ID)
	for _, tc := range []struct {
		name   string
		token  string
		status int
	}{
		{"unauthenticated", "", http.StatusUnauthorized},
		{"invalid_token", "FAKE_invalid_token_FOR_TEST_ONLY", http.StatusUnauthorized},
		{"viewer", viewerLogin.Data.Token, http.StatusForbidden},
		{"unowned_operator", operatorLogin.Data.Token, http.StatusForbidden},
	} {
		t.Run("archive_"+tc.name, func(t *testing.T) {
			status, body := generalSurfaceRequest(t, client, server.URL, http.MethodDelete, taskPath, tc.token, nil)
			if status != tc.status {
				t.Fatalf("archive status=%d want=%d body=%s", status, tc.status, body)
			}
			assertUnchanged(t)
		})
	}
	if err := db.Create(&model.NodeOwner{NodeID: nodeEntity.ID, UserID: operatorLogin.Data.User.ID}).Error; err != nil {
		t.Fatalf("assign archive node ownership: %v", err)
	}
	t.Run("archive_live_dependent_conflict", func(t *testing.T) {
		status, body := generalSurfaceRequest(t, client, server.URL, http.MethodDelete, taskPath, operatorLogin.Data.Token, nil)
		if status != http.StatusConflict {
			t.Fatalf("archive with live dependent status=%d body=%s", status, body)
		}
		assertUnchanged(t)
		var storedDependent model.Task
		if err := db.First(&storedDependent, dependent.ID).Error; err != nil || storedDependent.ArchivedAt != nil || !storedDependent.Enabled || storedDependent.DependsOnTaskID == nil || *storedDependent.DependsOnTaskID != created.Data.ID {
			t.Fatalf("live dependent changed: %+v err=%v", storedDependent, err)
		}
	})
	if !t.Run("archive_admin_unowned_dependent", func(t *testing.T) {
		status, body := generalSurfaceRequest(t, client, server.URL, http.MethodDelete, fmt.Sprintf("/api/v1/tasks/%d", dependent.ID), login.Data.Token, nil)
		if status != http.StatusOK {
			t.Fatalf("admin archive dependent status=%d body=%s", status, body)
		}
		var storedDependent model.Task
		if err := db.First(&storedDependent, dependent.ID).Error; err != nil || storedDependent.ArchivedAt == nil || storedDependent.Enabled || storedDependent.DependsOnTaskID == nil || *storedDependent.DependsOnTaskID != created.Data.ID {
			t.Fatalf("admin archive did not retain dependent: %+v err=%v", storedDependent, err)
		}
		assertUnchanged(t)
	}) {
		return
	}
	t.Run("archive_owned_operator", func(t *testing.T) {
		status, body, requestID := generalSurfaceRequestWithID(t, client, server.URL, http.MethodDelete, taskPath, operatorLogin.Data.Token, nil)
		if status != http.StatusOK {
			t.Fatalf("operator archive status=%d body=%s", status, body)
		}
		var archived struct {
			Code int `json:"code"`
			Data struct {
				Archived             bool `json:"archived"`
				Unlinked             bool `json:"unlinked"`
				ScheduleRemoved      bool `json:"schedule_removed"`
				ProviderBytesDeleted bool `json:"provider_bytes_deleted"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &archived); err != nil {
			t.Fatalf("decode archive response: %v; body=%s", err, body)
		}
		if archived.Code != http.StatusOK || !archived.Data.Archived || !archived.Data.Unlinked || !archived.Data.ScheduleRemoved || archived.Data.ProviderBytesDeleted {
			t.Fatalf("unexpected archive envelope: %+v", archived)
		}
		after := readArchiveState(t)
		if after.Task.ArchivedAt == nil || after.Task.Enabled || after.Task.ID != before.Task.ID {
			t.Fatalf("task was not retained and archived: %+v", after.Task)
		}
		if after.Link.TaskID == nil || *after.Link.TaskID != created.Data.ID || after.Link.UnlinkedAt == nil ||
			after.Link.RepositoryID != before.Link.RepositoryID || after.Link.EncryptedLegacyLocator != before.Link.EncryptedLegacyLocator ||
			after.Link.TaskNameSnapshot != before.Link.TaskNameSnapshot || after.Link.NodeIDSnapshot != before.Link.NodeIDSnapshot || after.Link.NodeNameSnapshot != before.Link.NodeNameSnapshot {
			t.Fatalf("native link was not retained and unlinked: %+v", after.Link)
		}
		if after.Policy.Status != "deleted" || after.Policy.DeletedAt == nil || after.Policy.Revision != before.Policy.Revision+1 || after.Policy.ScopeID != link.ID || after.Audits != 1 {
			t.Fatalf("policy was not soft deleted and audited: %+v audits=%d", after.Policy, after.Audits)
		}
		if !reflect.DeepEqual(after.Repository, before.Repository) || !reflect.DeepEqual(after.Point, before.Point) ||
			!reflect.DeepEqual(after.Run, before.Run) || !reflect.DeepEqual(after.Log, before.Log) {
			t.Fatalf("archive changed repository or producer history: before=%+v after=%+v", before, after)
		}
		var audit model.BackupAssetAuditEvent
		if err := db.Where("action = ? AND repository_id = ?", "retention_policy_delete", repository.ID).Take(&audit).Error; err != nil {
			t.Fatalf("read archive policy audit: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(audit.FieldsJSON), &fields); err != nil {
			t.Fatalf("decode archive policy audit fields: %v", err)
		}
		if requestID == "" || fields["correlation_id"] != requestID || fields["policy_id"] != policy.ID ||
			audit.ActorUserID != operatorLogin.Data.User.ID || audit.ActorUsername != generalSurfaceOperatorUsername || audit.ActorRole != "operator" ||
			audit.Outcome != "success" || audit.ItemCount != 1 {
			t.Fatalf("unexpected archive audit: %+v fields=%v request_id=%q", audit, fields, requestID)
		}
	})
}

func generalSurfaceRequest(t *testing.T, client *http.Client, baseURL, method, path, token string, payload any) (int, []byte) {
	t.Helper()
	status, body, _ := generalSurfaceRequestWithID(t, client, baseURL, method, path, token, payload)
	return status, body
}

func generalSurfaceRequestWithID(t *testing.T, client *http.Client, baseURL, method, path, token string, payload any) (int, []byte, string) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %s %s request: %v", method, path, err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, baseURL+path, body)
	if err != nil {
		t.Fatalf("build %s %s request: %v", method, path, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("perform %s %s request: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, path, err)
	}
	return resp.StatusCode, responseBody, resp.Header.Get("X-Request-ID")
}
