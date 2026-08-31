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
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/auth"
	"xirang/backend/internal/bootstrap"
	"xirang/backend/internal/config"
	"xirang/backend/internal/database"
	"xirang/backend/internal/model"
	"xirang/backend/internal/task"
	"xirang/backend/internal/ws"
)

const (
	generalSurfaceAdminUsername      = "admin"
	generalSurfaceOperatorUsername   = "general-operator"
	generalSurfacePostgresRequireEnv = "REQUIRE_POSTGRES_GENERAL_TEST"
	generalSurfacePostgresPassword   = "FAKE_GeneralPostgresPass2026!_FOR_TEST_ONLY"
	generalSurfaceOperatorPassword   = "FAKE_GeneralOperatorPass2026!_FOR_TEST_ONLY"
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
	if created.Code != http.StatusCreated || created.Data.ID == 0 || created.Data.Name != taskName || created.Data.NodeID != nodeEntity.ID || created.Data.ExecutorType != "command" {
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
			"name":          updatedTaskName,
			"node_id":       nodeEntity.ID,
			"executor_type": "command",
			"command":       "printf postgres-general-surface-updated",
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

	deleteStatus, deleteBody := generalSurfaceRequest(t, client, server.URL, http.MethodDelete,
		fmt.Sprintf("/api/v1/tasks/%d", created.Data.ID), login.Data.Token, nil)
	if deleteStatus != http.StatusOK {
		t.Fatalf("PostgreSQL task archive status=%d body=%s", deleteStatus, deleteBody)
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
	if err := json.Unmarshal(deleteBody, &archived); err != nil {
		t.Fatalf("decode PostgreSQL task archive response: %v; body=%s", err, deleteBody)
	}
	if archived.Code != http.StatusOK || !archived.Data.Archived || !archived.Data.ScheduleRemoved ||
		archived.Data.Unlinked || archived.Data.ProviderBytesDeleted {
		t.Fatalf("unexpected PostgreSQL task archive envelope: %+v", archived)
	}
	if err := db.First(&persisted, created.Data.ID).Error; err != nil {
		t.Fatalf("read PostgreSQL archived task: %v", err)
	}
	if persisted.ArchivedAt == nil || persisted.Enabled {
		t.Fatalf("PostgreSQL task archive did not persist terminal metadata: %+v", persisted)
	}
}

func generalSurfaceRequest(t *testing.T, client *http.Client, baseURL, method, path, token string, payload any) (int, []byte) {
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
	return resp.StatusCode, responseBody
}
