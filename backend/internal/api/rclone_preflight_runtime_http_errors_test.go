package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/api/handlers"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/repository"
	applogger "xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

func rcloneHTTPNativeTestTask(fixture *rcloneHTTPFixture) uint {
	fixture.t.Helper()
	node := model.Node{Host: "127.0.0.1", Port: 22, Username: "fixture", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
	return fixture.addTask(node, "native-error-case", "/fixture/source", "legacy:fixture")
}

func TestRclonePreflightRuntimeHTTPDisabledAndInternalFailure(t *testing.T) {
	boundary := newRcloneHTTPNativeBoundary(provider.RcloneNativeSSES3V1)
	fixture := newRcloneHTTPFixture(t, "rclone", boundary.dependencies(t, "rclone", false))
	id := rcloneHTTPNativeTestTask(fixture)
	revision := fixture.nativeCandidate(id, false)
	path := fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/preflights", id)
	body := map[string]any{"expected_task_revision": revision, "requested_mode": "native_object_versions"}

	const privateCanary = "FAKE_PRIVATE_PROVIDER_CANARY_DO_NOT_LOG"
	var logs bytes.Buffer
	previousLogger := applogger.Log
	applogger.Log = zerolog.New(&logs)
	t.Cleanup(func() { applogger.Log = previousLogger })
	boundary.mu.Lock()
	boundary.factoryFailure = errors.New(privateCanary)
	boundary.mu.Unlock()
	fixture.private = append(fixture.private, privateCanary)
	fixture.request(http.MethodPost, path, body, http.StatusInternalServerError, nil)
	applogger.Log = previousLogger
	if !strings.Contains(logs.String(), "服务器内部错误") || strings.Contains(logs.String(), privateCanary) {
		t.Fatal("internal provider failure was not safely logged")
	}
	if err := fixture.runtime.TransitionFeature(context.Background(), false, func() error {
		return fixture.setting.Update("backup_assets.enabled", "false")
	}); err != nil {
		t.Fatalf("disable runtime feature: %v", err)
	}
	var keysBefore, bindingsBefore int64
	if err := fixture.db.Model(&model.WrappedDomainKey{}).Count(&keysBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RepositoryAccessBinding{}).Count(&bindingsBefore).Error; err != nil {
		t.Fatal(err)
	}
	boundary.mu.Lock()
	callsBefore, commandsBefore, stagesBefore, writesBefore := boundary.factoryCalls, boundary.commandCalls, boundary.stageCalls, boundary.canaryWrites
	boundary.mu.Unlock()
	var response struct {
		Reason struct {
			Code string `json:"code"`
		} `json:"reason"`
	}
	fixture.request(http.MethodPost, path, body, http.StatusServiceUnavailable, &response)
	if response.Reason.Code != string(backupasset.CapabilityFeatureDisabled) {
		t.Fatal("disabled feature did not return its typed capability reason")
	}
	var keysAfter, bindingsAfter int64
	if err := fixture.db.Model(&model.WrappedDomainKey{}).Count(&keysAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Model(&model.RepositoryAccessBinding{}).Count(&bindingsAfter).Error; err != nil {
		t.Fatal(err)
	}
	boundary.mu.Lock()
	callsAfter, commandsAfter, stagesAfter, writesAfter := boundary.factoryCalls, boundary.commandCalls, boundary.stageCalls, boundary.canaryWrites
	boundary.mu.Unlock()
	if keysBefore != keysAfter || bindingsBefore != bindingsAfter || callsBefore != callsAfter || commandsBefore != commandsAfter || stagesBefore != stagesAfter || writesBefore != writesAfter {
		t.Fatal("disabled preflight reached key, binding, or provider mutation")
	}
}

// The production runtime always installs its preflighter. Exercise the real
// repository and authenticated handler separately for this deliberately absent
// dependency, without adding a production fallback or replacing the service.
func TestRclonePreflightHTTPMissingPreflighter(t *testing.T) {
	boundary := newRcloneHTTPNativeBoundary(provider.RcloneNativeSSES3V1)
	fixture := newRcloneHTTPFixture(t, "rclone", boundary.dependencies(t, "rclone", false))
	id := rcloneHTTPNativeTestTask(fixture)
	revision := fixture.task(id).RclonePublication.TaskRevision
	service, err := repository.NewService(repository.Dependencies{DB: fixture.db, Foundation: fixture.runtime.FoundationService(), Now: fixture.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	group := router.Group("/api/v1")
	group.Use(middleware.AuthMiddleware(fixture.jwt, fixture.db))
	group.POST("/tasks/:id/rclone-versioning/preflights", middleware.RBAC(backupasset.PermissionBackupRepositoriesManage), middleware.RequireRole("admin"), middleware.OwnershipTaskCheck(fixture.db), handlers.NewTaskRcloneVersioningHandler(service).CreatePreflight)
	fixture.router = router
	var response struct {
		Reason struct {
			Code string `json:"code"`
		} `json:"reason"`
	}
	fixture.request(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/preflights", id),
		map[string]any{"expected_task_revision": revision, "requested_mode": "native_object_versions"}, http.StatusServiceUnavailable, &response)
	if response.Reason.Code != string(backupasset.CapabilityProviderUnavailable) {
		t.Fatal("missing real preflighter was not reported as provider unavailable")
	}
}

func TestRclonePreflightRuntimeHTTPRejectsUnsupportedVersions(t *testing.T) {
	for _, version := range []string{"rclone v1.74.3\n", "rclone v1.74.5\n", "rclone v1.74.4-beta.1\n"} {
		t.Run(version, func(t *testing.T) {
			boundary := newRcloneHTTPNativeBoundary(provider.RcloneNativeSSES3V1)
			boundary.managedVersion = version
			fixture := newRcloneHTTPFixture(t, "rclone", boundary.dependencies(t, "rclone", false))
			id := rcloneHTTPNativeTestTask(fixture)
			revision := fixture.nativeCandidate(id, false)
			settling := fixture.preflight(id, revision, backupasset.PublicationNativeObjectVersions)
			if settling.Summary.State != backupasset.RcloneStateCapabilitySettling {
				t.Fatal("native observation did not start")
			}
			fixture.clock.Advance(15 * time.Minute)
			fixture.request(http.MethodPost, fmt.Sprintf("/api/v1/tasks/%d/rclone-versioning/preflights", id),
				map[string]any{"expected_task_revision": revision, "requested_mode": "native_object_versions"}, http.StatusNotImplemented, nil)
			if writes, deletes := boundary.counts(); writes != 0 || deletes != 0 {
				t.Fatal("unsupported Rclone wrote a canary")
			}
		})
	}
}
