package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/ga"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

const backupGATestDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type backupGAServiceFake struct {
	report       ga.AdminReport
	err          error
	actorID      uint
	digest       string
	inventory    int
	readiness    int
	acknowledge  int
	importCalls  int
	rebuildCalls int
	purgeCalls   int
}

func (service *backupGAServiceFake) RunInventory(context.Context) (ga.AdminReport, error) {
	service.inventory++
	return service.report, service.err
}

func (service *backupGAServiceFake) Readiness(context.Context) (ga.AdminReport, error) {
	service.readiness++
	return service.report, service.err
}

func (service *backupGAServiceFake) Acknowledge(_ context.Context, actorID uint, digest string) (ga.AdminReport, error) {
	service.acknowledge++
	service.actorID = actorID
	service.digest = digest
	return service.report, service.err
}

func (service *backupGAServiceFake) DiscoverImport(context.Context) error {
	service.importCalls++
	return nil
}

func (service *backupGAServiceFake) Rebuild(context.Context) error {
	service.rebuildCalls++
	return nil
}

func (service *backupGAServiceFake) Purge(context.Context) error {
	service.purgeCalls++
	return nil
}

func TestBackupGAHandlerInventoryReturnsOpaquePublicDocument(t *testing.T) {
	service := &backupGAServiceFake{report: existingGAReport()}
	response := performBackupGAHandlerRequest(t, service, "admin", http.MethodPost, "/api/v1/settings/backup-assets/ga/inventory", "")
	if response.Code != http.StatusOK || service.inventory != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.inventory, response.Body.String())
	}
	payload := decodeBackupGAEnvelope(t, response)
	if payload["schema_version"] != float64(1) || payload["class"] != "existing" || payload["inventory_digest"] != backupGATestDigest {
		t.Fatalf("inventory payload=%v", payload)
	}
	counts, _ := payload["counts"].(map[string]any)
	if counts["candidates"] != float64(1) || counts["conflicts"] != float64(1) {
		t.Fatalf("counts=%v", counts)
	}
	conflicts, _ := payload["conflicts"].([]any)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts=%v", conflicts)
	}
	conflict, _ := conflicts[0].(map[string]any)
	if conflict["kind"] != "command_unsupported" || conflict["stable_reason_code"] != ga.ReasonCommandUnsupported {
		t.Fatalf("conflict=%v", conflict)
	}
	if service.importCalls != 0 || service.rebuildCalls != 0 || service.purgeCalls != 0 {
		t.Fatalf("inventory invoked Child 14 commands import=%d rebuild=%d purge=%d", service.importCalls, service.rebuildCalls, service.purgeCalls)
	}
}

func TestGaInventoryHandlerStripsLocatorProofAndSnapshotIndex(t *testing.T) {
	service := &backupGAServiceFake{report: existingGAReport()}
	response := performBackupGAHandlerRequest(t, service, "admin", http.MethodPost, "/api/v1/settings/backup-assets/ga/inventory", "")
	body := response.Body.String()
	for _, leaked := range []string{
		"/PRIVATE/REPOSITORY/PATH",
		"identity_key",
		"SnapshotFileIndex",
		"snapshot_path",
		"grant_secret",
		"cookie_secret",
		"proof",
		"ticket",
		"trusted_snapshot_index",
	} {
		if strings.Contains(body, leaked) {
			t.Fatalf("inventory leaked %q body=%s", leaked, body)
		}
	}
	payload := decodeBackupGAEnvelope(t, response)
	if _, ok := payload["candidates"]; ok {
		t.Fatalf("public inventory exposed candidates=%v", payload["candidates"])
	}
}

func TestGaReadinessHandlerReturnsSnapshotWithoutSecrets(t *testing.T) {
	service := &backupGAServiceFake{report: existingGAReport()}
	response := performBackupGAHandlerRequest(t, service, "admin", http.MethodGet, "/api/v1/settings/backup-assets/ga/readiness", "")
	if response.Code != http.StatusOK || service.readiness != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.readiness, response.Body.String())
	}
	payload := decodeBackupGAEnvelope(t, response)
	if payload["status"] != "ready" || payload["inventory_complete"] != true || payload["worker_optional"] != true {
		t.Fatalf("readiness payload=%v", payload)
	}
	if payload["export_root_valid"] != true || payload["key_domains_ready"] != true {
		t.Fatalf("readiness probes=%v", payload)
	}
	body := response.Body.String()
	if strings.Contains(body, "/PRIVATE/REPOSITORY/PATH") || strings.Contains(body, "SnapshotFileIndex") {
		t.Fatalf("readiness leaked private material body=%s", body)
	}
}

func TestBackupGAHandlerAcknowledgeExistingDigest(t *testing.T) {
	acked := existingGAReport()
	acked.Snapshot.Status = ga.ReadinessAcknowledged
	acked.Snapshot.AcknowledgedDigest = backupGATestDigest
	service := &backupGAServiceFake{report: acked}
	response := performBackupGAHandlerRequest(t, service, "admin", http.MethodPost, "/api/v1/settings/backup-assets/ga/acknowledge", `{"digest":"`+backupGATestDigest+`"}`)
	if response.Code != http.StatusOK || service.acknowledge != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.acknowledge, response.Body.String())
	}
	if service.actorID != 7 || service.digest != backupGATestDigest {
		t.Fatalf("ack actor=%d digest=%s", service.actorID, service.digest)
	}
	payload := decodeBackupGAEnvelope(t, response)
	if payload["status"] != "acknowledged" || payload["acknowledged_digest"] != backupGATestDigest {
		t.Fatalf("ack payload=%v", payload)
	}
}

func TestBackupGAHandlerOperatorAndViewerDeniedWithoutServiceCall(t *testing.T) {
	for _, role := range []string{"operator", "viewer"} {
		service := &backupGAServiceFake{report: existingGAReport()}
		for _, route := range []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodPost, "/api/v1/settings/backup-assets/ga/inventory", ""},
			{http.MethodGet, "/api/v1/settings/backup-assets/ga/readiness", ""},
			{http.MethodPost, "/api/v1/settings/backup-assets/ga/acknowledge", `{"digest":"` + backupGATestDigest + `"}`},
		} {
			response := performBackupGAHandlerRequest(t, service, role, route.method, route.path, route.body)
			if response.Code != http.StatusForbidden {
				t.Fatalf("role=%s %s %s status=%d body=%s", role, route.method, route.path, response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "command_unsupported") || strings.Contains(response.Body.String(), backupGATestDigest) {
				t.Fatalf("role=%s received conflict payload body=%s", role, response.Body.String())
			}
		}
		if service.inventory != 0 || service.readiness != 0 || service.acknowledge != 0 {
			t.Fatalf("role=%s invoked service inventory=%d readiness=%d ack=%d", role, service.inventory, service.readiness, service.acknowledge)
		}
	}
}

func TestGaReadinessHandlerOperatorDenied(t *testing.T) {
	service := &backupGAServiceFake{report: existingGAReport()}
	response := performBackupGAHandlerRequest(t, service, "operator", http.MethodGet, "/api/v1/settings/backup-assets/ga/readiness", "")
	if response.Code != http.StatusForbidden || service.readiness != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.readiness, response.Body.String())
	}
}

func TestBackupGAHandlerStaysThinAndSecretFree(t *testing.T) {
	content, err := os.ReadFile("backup_ga_handler.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	for _, forbidden := range []string{
		"gorm.io/gorm",
		"/backupasset/provider",
		"DiscoverImport",
		"SnapshotFileIndex",
		"exec.Command",
		"c.JSON",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("backup GA handler contains forbidden token %q", forbidden)
		}
	}
}

func existingGAReport() ga.AdminReport {
	return ga.AdminReport{
		Snapshot: ga.ReadinessSnapshot{
			Class:             ga.InstallationExisting,
			Status:            ga.ReadinessReady,
			InventoryComplete: true,
			InventoryDigest:   backupGATestDigest,
			ExportRootValid:   true,
			KeyDomainsReady:   true,
		},
		Inventory: ga.InventoryDocument{
			Class:  ga.InstallationExisting,
			Digest: backupGATestDigest,
			Counts: ga.InventoryCounts{Candidates: 1, Conflicts: 1, Unsupported: 1},
			Conflicts: []ga.InventoryConflict{{
				Kind:             ga.ConflictCommandUnsupported,
				TaskIDs:          []uint{9},
				StableReasonCode: ga.ReasonCommandUnsupported,
			}},
			Candidates: []ga.InventoryCandidate{{
				IdentityKey:      "/PRIVATE/REPOSITORY/PATH",
				RepositoryIDs:    []string{strings.Repeat("a", 32)},
				ProducingTaskIDs: []uint{9},
			}},
			TrustedSnapshotIndex: true,
		},
	}
}

func performBackupGAHandlerRequest(t *testing.T, service *backupGAServiceFake, role, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(7))
		c.Set(middleware.CtxUsername, "ga-admin")
		c.Set(middleware.CtxRole, role)
		c.Next()
	})
	handler := NewBackupGAHandler(service)
	guards := []gin.HandlerFunc{
		middleware.RBAC(backupasset.PermissionBackupRepositoriesManage),
		middleware.RequireRole("admin"),
	}
	router.POST("/api/v1/settings/backup-assets/ga/inventory", append(guards, handler.Inventory)...)
	router.GET("/api/v1/settings/backup-assets/ga/readiness", append(guards, handler.Readiness)...)
	router.POST("/api/v1/settings/backup-assets/ga/acknowledge", append(guards, handler.Acknowledge)...)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeBackupGAEnvelope(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, response.Body.String())
	}
	if envelope.Code != http.StatusOK || envelope.Data == nil {
		t.Fatalf("envelope=%+v body=%s", envelope, response.Body.String())
	}
	return envelope.Data
}
