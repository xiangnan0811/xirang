package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xirang/backend/internal/api/docs"
	"xirang/backend/internal/auth"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/content"
	assetexport "xirang/backend/internal/backupasset/export"
	"xirang/backend/internal/backupasset/processing"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var backupArchiveHandlerReplayDBSequence atomic.Uint64

func TestBackupArchiveHandlerListCreateStatusAndCancel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC().Truncate(time.Second)
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("2", 64)
	memberID, requestID := strings.Repeat("3", 32), strings.Repeat("4", 32)
	service := &backupArchiveServiceFake{
		index: processing.ArchiveMemberIndexView{
			SchemaVersion: 1, IndexRevision: strings.Repeat("5", 64), ExpiresAt: now.Add(time.Hour),
			Entries: []processing.ArchiveMemberIndexViewEntry{{
				ID: memberID, DisplayName: "report.txt", Type: "file", Size: 7, MediaType: "text/plain",
			}},
		},
		created: processing.ArchiveMemberCreateResult{
			SchemaVersion: 1, RequestID: requestID,
			AssetRef:      backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID},
			IndexRevision: strings.Repeat("5", 64), State: "queued",
		},
		status: processing.ArchiveMemberStatusResult{
			SchemaVersion: 1, RequestID: requestID,
			AssetRef:      backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID},
			IndexRevision: strings.Repeat("5", 64), State: processing.ArchiveMemberQueued,
			Fallback: processing.ArchiveFallbackProduct{},
		},
	}
	audit := &backupArchiveAuditFake{}
	handler := NewBackupArchiveHandler(service, nil, nil, nil, audit, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(7))
		c.Set(middleware.CtxUsername, "archive-operator")
		c.Set(middleware.CtxRole, "operator")
		c.Next()
	})
	base := "/api/v1/recovery-points/:id/entries/:entryId"
	router.GET(base+"/archive-members", handler.List)
	router.POST(base+"/archive-member-jobs", handler.Create)
	router.GET(base+"/archive-member-jobs/:jobId", handler.Status)
	router.POST(base+"/archive-member-jobs/:jobId/cancel", handler.Cancel)
	path := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID

	listed := httptest.NewRecorder()
	router.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, path+"/archive-members", nil))
	if listed.Code != http.StatusOK || len(service.indexLookups) != 1 {
		t.Fatalf("list status=%d lookups=%+v body=%s", listed.Code, service.indexLookups, listed.Body.String())
	}

	created := httptest.NewRecorder()
	create := httptest.NewRequest(http.MethodPost, path+"/archive-member-jobs", strings.NewReader(
		`{"schema_version":1,"index_revision":"`+service.index.IndexRevision+`","member_chain":["`+memberID+`"]}`,
	))
	create.Header.Set("Idempotency-Key", "0123456789abcdef")
	router.ServeHTTP(created, create)
	if created.Code != http.StatusAccepted || created.Header().Get("Location") != path+"/archive-member-jobs/"+requestID ||
		len(service.createRequests) != 1 || service.createRequests[0].Ref != (backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}) {
		t.Fatalf("create status=%d location=%q requests=%+v body=%s", created.Code, created.Header().Get("Location"), service.createRequests, created.Body.String())
	}

	status := httptest.NewRecorder()
	router.ServeHTTP(status, httptest.NewRequest(http.MethodGet,
		path+"/archive-member-jobs/"+requestID+"?index_revision="+service.index.IndexRevision, nil))
	if status.Code != http.StatusOK || len(service.pollLookups) != 1 ||
		service.pollLookups[0].IndexRevision != service.index.IndexRevision {
		t.Fatalf("status=%d lookups=%+v body=%s", status.Code, service.pollLookups, status.Body.String())
	}

	canceled := httptest.NewRecorder()
	router.ServeHTTP(canceled, httptest.NewRequest(http.MethodPost, path+"/archive-member-jobs/"+requestID+"/cancel",
		strings.NewReader(`{"schema_version":1,"index_revision":"`+service.index.IndexRevision+`"}`)))
	if canceled.Code != http.StatusAccepted || len(service.cancelLookups) != 1 ||
		service.cancelLookups[0].IndexRevision != service.index.IndexRevision {
		t.Fatalf("cancel status=%d lookups=%+v body=%s", canceled.Code, service.cancelLookups, canceled.Body.String())
	}
	if len(audit.inputs) != 3 || audit.inputs[0].Action != backupasset.AuditActionArchiveInspect ||
		audit.inputs[1].Action != backupasset.AuditActionArchiveMember || audit.inputs[2].Action != backupasset.AuditActionArchiveMember {
		t.Fatalf("archive audit=%+v", audit.inputs)
	}
}

func TestBackupArchiveHandlerCreateReplayAfterSynchronousReconcileKeepsAcceptedResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC().Truncate(time.Second)
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("2", 64)
	memberID := strings.Repeat("3", 32)
	indexRevision := strings.Repeat("4", 64)
	db := backupArchiveHandlerReplayDB(t, now)
	service, err := processing.NewArchiveMemberService(processing.ArchiveMemberServiceDependencies{
		DB: db,
		Coordinator: &backupArchiveReplayCoordinator{result: processing.WorkResult{
			JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
		}},
		Authorize: backupArchiveReplayAuthorizer{asset: content.AuthorizedAsset{
			Ref:                 backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID},
			CatalogGenerationID: strings.Repeat("9", 32), Provider: backupasset.ProviderRestic,
			ProviderCapabilityRevision: 9, SourceFingerprint: "source-fingerprint-v1",
			EntryFingerprint: "entry-fingerprint-v1", FingerprintStrength: "strong", Size: 1024, MediaType: "application/zip",
		}},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (processing.ArchiveMemberIndexBinding, error) {
			return backupArchiveReplayIndex(now, indexRevision, memberID), nil
		},
		RevalidateIndex: func(context.Context, model.BackupAssetArchiveMemberRequest) (processing.ArchiveMemberIndexBinding, error) {
			return backupArchiveReplayIndex(now, indexRevision, memberID), nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (processing.ArchiveMemberProcessingAuthority, error) {
			return processing.ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (processing.CapabilityAdvertisement, error) {
			return processing.CapabilityAdvertisement{
				SchemaVersion: 1, Capability: "archive.extract_entry", CapabilitySchema: "archive.extract_entry.v1",
				PipelineFingerprint: "archive-extract-pipeline-v1", OutputProfile: "archive_member_v1",
			}, nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewBackupArchiveHandler(service, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxUsername, "archive-admin")
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	})
	path := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID + "/archive-member-jobs"
	router.POST("/api/v1/recovery-points/:id/entries/:entryId/archive-member-jobs", handler.Create)
	body := `{"schema_version":1,"index_revision":"` + indexRevision + `","member_chain":["` + memberID + `"]}`

	first := httptest.NewRecorder()
	firstRequest := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	firstRequest.Header.Set("Idempotency-Key", "0123456789abcdef")
	router.ServeHTTP(first, firstRequest)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first create status=%d body=%s", first.Code, first.Body.String())
	}
	firstResult := decodeBackupArchiveCreateResponse(t, first)
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("id = ?", firstResult.RequestID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(processing.ArchiveMemberRunning) {
		t.Fatalf("synchronous reconcile did not advance durable state: %+v", persisted)
	}

	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	replayRequest.Header.Set("Idempotency-Key", "0123456789abcdef")
	router.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusAccepted || replay.Header().Get("Location") != path+"/"+firstResult.RequestID {
		t.Fatalf("replay status=%d location=%q body=%s", replay.Code, replay.Header().Get("Location"), replay.Body.String())
	}
	replayResult := decodeBackupArchiveCreateResponse(t, replay)
	if replayResult.RequestID != firstResult.RequestID || replayResult.State != string(processing.ArchiveMemberQueued) {
		t.Fatalf("replay result first=%+v replay=%+v", firstResult, replayResult)
	}
}

func TestBackupArchiveHandlerRejectsMismatchedArchiveMemberResponseBindings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("2", 64)
	requestID, memberID := strings.Repeat("3", 32), strings.Repeat("4", 32)
	indexRevision := strings.Repeat("5", 64)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}
	otherRef := backupasset.AssetRef{RecoveryPointID: strings.Repeat("6", 32), EntryID: entryID}

	tests := []struct {
		name    string
		method  string
		suffix  string
		body    string
		prepare func(*backupArchiveServiceFake)
	}{
		{
			name: "create outer ref", method: http.MethodPost, suffix: "/archive-member-jobs",
			body:    `{"schema_version":1,"index_revision":"` + indexRevision + `","member_chain":["` + memberID + `"]}`,
			prepare: func(service *backupArchiveServiceFake) { service.created.AssetRef = otherRef },
		},
		{
			name: "create index revision", method: http.MethodPost, suffix: "/archive-member-jobs",
			body:    `{"schema_version":1,"index_revision":"` + indexRevision + `","member_chain":["` + memberID + `"]}`,
			prepare: func(service *backupArchiveServiceFake) { service.created.IndexRevision = strings.Repeat("7", 64) },
		},
		{
			name: "status outer ref", method: http.MethodGet,
			suffix:  "/archive-member-jobs/" + requestID + "?index_revision=" + indexRevision,
			prepare: func(service *backupArchiveServiceFake) { service.status.AssetRef = otherRef },
		},
		{
			name: "status invalid index revision", method: http.MethodGet,
			suffix:  "/archive-member-jobs/" + requestID + "?index_revision=" + indexRevision,
			prepare: func(service *backupArchiveServiceFake) { service.status.IndexRevision = "not-a-digest" },
		},
		{
			name: "cancel outer ref", method: http.MethodPost, suffix: "/archive-member-jobs/" + requestID + "/cancel",
			body:    `{"schema_version":1,"index_revision":"` + indexRevision + `"}`,
			prepare: func(service *backupArchiveServiceFake) { service.status.AssetRef = otherRef },
		},
		{
			name: "cancel invalid index revision", method: http.MethodPost, suffix: "/archive-member-jobs/" + requestID + "/cancel",
			body:    `{"schema_version":1,"index_revision":"` + indexRevision + `"}`,
			prepare: func(service *backupArchiveServiceFake) { service.status.IndexRevision = "not-a-digest" },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &backupArchiveServiceFake{
				created: processing.ArchiveMemberCreateResult{
					SchemaVersion: 1, RequestID: requestID, AssetRef: ref,
					IndexRevision: indexRevision, State: string(processing.ArchiveMemberQueued),
				},
				status: processing.ArchiveMemberStatusResult{
					SchemaVersion: 1, RequestID: requestID, AssetRef: ref,
					IndexRevision: indexRevision, State: processing.ArchiveMemberQueued,
				},
			}
			test.prepare(service)
			handler := NewBackupArchiveHandler(service, nil, nil, nil, nil, nil)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(middleware.CtxUserID, uint(7))
				c.Set(middleware.CtxRole, "operator")
				c.Next()
			})
			base := "/api/v1/recovery-points/:id/entries/:entryId"
			router.POST(base+"/archive-member-jobs", handler.Create)
			router.GET(base+"/archive-member-jobs/:jobId", handler.Status)
			router.POST(base+"/archive-member-jobs/:jobId/cancel", handler.Cancel)
			request := httptest.NewRequest(test.method,
				"/api/v1/recovery-points/"+pointID+"/entries/"+entryID+test.suffix,
				strings.NewReader(test.body),
			)
			if test.suffix == "/archive-member-jobs" {
				request.Header.Set("Idempotency-Key", "0123456789abcdef")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBackupArchiveHandlerCreateRejectsUndeployedProcessingWithoutQueuedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC().Truncate(time.Second)
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("2", 64)
	memberID, indexRevision := strings.Repeat("3", 32), strings.Repeat("4", 64)
	db := backupArchiveHandlerReplayDB(t, now)
	service, err := processing.NewArchiveMemberService(processing.ArchiveMemberServiceDependencies{
		DB: db,
		Coordinator: &backupArchiveReplayCoordinator{result: processing.WorkResult{
			JobID: strings.Repeat("6", 32), InterestID: strings.Repeat("7", 32), WorkKey: strings.Repeat("8", 64), Created: true,
		}},
		Authorize: backupArchiveReplayAuthorizer{asset: content.AuthorizedAsset{
			Ref:                 backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID},
			CatalogGenerationID: strings.Repeat("9", 32), Provider: backupasset.ProviderRestic,
			ProviderCapabilityRevision: 9, SourceFingerprint: "source-fingerprint-v1",
			EntryFingerprint: "entry-fingerprint-v1", FingerprintStrength: "strong", Size: 1024, MediaType: "application/zip",
		}},
		ResolveIndex: func(context.Context, content.AuthorizedAsset, string) (processing.ArchiveMemberIndexBinding, error) {
			return backupArchiveReplayIndex(now, indexRevision, memberID), nil
		},
		RevalidateIndex: func(context.Context, model.BackupAssetArchiveMemberRequest) (processing.ArchiveMemberIndexBinding, error) {
			return backupArchiveReplayIndex(now, indexRevision, memberID), nil
		},
		ResolveAuthority: func(context.Context, model.BackupAssetArchiveMemberRequest) (processing.ArchiveMemberProcessingAuthority, error) {
			return processing.ArchiveMemberProcessingAuthority{ProviderCapabilityRevision: 9, SecurityPolicyRevision: "security-policy-v1"}, nil
		},
		ResolveExtractCapability: func(context.Context) (processing.CapabilityAdvertisement, error) {
			return processing.CapabilityAdvertisement{}, processing.ErrNotDeployed
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	audit := &backupArchiveAuditFake{}
	handler := NewBackupArchiveHandler(service, nil, db, nil, audit, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxUsername, "archive-admin")
		c.Set(middleware.CtxRole, "admin")
		c.Next()
	})
	router.POST("/api/v1/recovery-points/:id/entries/:entryId/archive-member-jobs", handler.Create)
	path := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID + "/archive-member-jobs"
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(
		`{"schema_version":1,"index_revision":"`+indexRevision+`","member_chain":["`+memberID+`"]}`,
	))
	request.Header.Set("Idempotency-Key", "0123456789abcdef")
	router.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var persisted model.BackupAssetArchiveMemberRequest
	if err := db.Where("recovery_point_id = ? AND entry_id = ?", pointID, entryID).Take(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.State != string(processing.ArchiveMemberCanceled) {
		t.Fatalf("undeployed request remained queued: %+v", persisted)
	}
	if len(audit.inputs) != 1 || audit.inputs[0].FailureCode != "unavailable" ||
		audit.inputs[0].Fields[backupasset.AuditFieldSource] != content.ArchiveMemberChainDigest(
			backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}, indexRevision, memberID,
		) {
		t.Fatalf("undeployed audit=%+v", audit.inputs)
	}
}

func TestBackupArchiveHandlerAuditsOnlyIndexAndMemberDigests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now().UTC().Truncate(time.Second)
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("2", 64)
	memberID, requestID, indexRevision := strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 64)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}
	memberDigest := content.ArchiveMemberChainDigest(ref, indexRevision, memberID)
	db := backupArchiveHandlerReplayDB(t, now)
	if err := db.Create(&model.BackupAssetArchiveMemberRequest{
		ID: requestID, OwnerUserID: 7, Endpoint: "archive_member_create",
		KeyDigest: strings.Repeat("6", 64), RequestIntentDigest: strings.Repeat("7", 64),
		RecoveryPointID: pointID, EntryID: entryID, CatalogGenerationID: strings.Repeat("8", 32),
		SourceFingerprint: "source-fingerprint-v1", EntryFingerprint: "entry-fingerprint-v1",
		IndexArtifactID: strings.Repeat("9", 32), IndexRevision: indexRevision, MemberChainDigest: memberDigest,
		ResolvedOrdinal: 7, State: string(processing.ArchiveMemberQueued),
		IdempotencyExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	service := &backupArchiveServiceFake{
		index: processing.ArchiveMemberIndexView{SchemaVersion: 1, IndexRevision: indexRevision, ExpiresAt: now.Add(time.Hour)},
		created: processing.ArchiveMemberCreateResult{
			SchemaVersion: 1, RequestID: requestID, AssetRef: ref, IndexRevision: indexRevision, State: "queued",
		},
		status: processing.ArchiveMemberStatusResult{
			SchemaVersion: 1, RequestID: requestID, AssetRef: ref, IndexRevision: indexRevision, State: processing.ArchiveMemberQueued,
		},
	}
	audit := &backupArchiveAuditFake{}
	handler := NewBackupArchiveHandler(service, nil, db, nil, audit, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(7))
		c.Set(middleware.CtxUsername, "archive-operator")
		c.Set(middleware.CtxRole, "operator")
		c.Next()
	})
	base := "/api/v1/recovery-points/:id/entries/:entryId"
	router.GET(base+"/archive-members", handler.List)
	router.POST(base+"/archive-member-jobs", handler.Create)
	router.POST(base+"/archive-member-jobs/:jobId/cancel", handler.Cancel)
	path := "/api/v1/recovery-points/" + pointID + "/entries/" + entryID

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, path+"/archive-members", nil),
		func() *http.Request {
			request := httptest.NewRequest(http.MethodPost, path+"/archive-member-jobs", strings.NewReader(
				`{"schema_version":1,"index_revision":"`+indexRevision+`","member_chain":["`+memberID+`"]}`,
			))
			request.Header.Set("Idempotency-Key", "0123456789abcdef")
			return request
		}(),
		httptest.NewRequest(http.MethodPost, path+"/archive-member-jobs/"+requestID+"/cancel",
			strings.NewReader(`{"schema_version":1,"index_revision":"`+indexRevision+`"}`)),
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK && response.Code != http.StatusAccepted {
			t.Fatalf("%s %s status=%d body=%s", request.Method, request.URL.Path, response.Code, response.Body.String())
		}
	}

	if len(audit.inputs) != 3 || audit.inputs[0].Fields[backupasset.AuditFieldSource] != indexRevision ||
		audit.inputs[1].Fields[backupasset.AuditFieldSource] != memberDigest ||
		audit.inputs[2].Fields[backupasset.AuditFieldSource] != memberDigest {
		t.Fatalf("audit=%+v", audit.inputs)
	}
	for _, input := range audit.inputs {
		if input.Actor.UserID != 7 || input.Actor.Username != "archive-operator" || input.Actor.Role != "operator" {
			t.Fatalf("archive audit lost the shared actor envelope: %+v", input.Actor)
		}
	}
	encoded, err := json.Marshal(audit.inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{memberID, "report.txt"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("archive audit leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestBackupArchiveHandlerListAuditsUndeployedFailureAsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID, entryID := strings.Repeat("1", 32), strings.Repeat("2", 64)
	audit := &backupArchiveAuditFake{}
	handler := NewBackupArchiveHandler(&backupArchiveServiceFake{
		indexErr: errors.Join(processing.ErrArchiveMemberUnavailable, processing.ErrNotDeployed),
	}, nil, nil, nil, audit, nil)
	router := gin.New()
	router.GET("/api/v1/recovery-points/:id/entries/:entryId/archive-members", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(7))
		c.Set(middleware.CtxRole, "operator")
		c.Next()
	}, handler.List)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/archive-members", nil))
	if response.Code != http.StatusServiceUnavailable || len(audit.inputs) != 1 ||
		audit.inputs[0].Outcome != backupasset.AuditOutcomeBlocked || audit.inputs[0].FailureCode != "unavailable" {
		t.Fatalf("status=%d audit=%+v body=%s", response.Code, audit.inputs, response.Body.String())
	}
}

type backupAssetSwaggerSchema struct {
	Type       string                              `json:"type"`
	Format     string                              `json:"format"`
	Required   []string                            `json:"required"`
	Properties map[string]backupAssetSwaggerSchema `json:"properties"`
	Items      *backupAssetSwaggerSchema           `json:"items"`
	Enum       []string                            `json:"enum"`
	Minimum    *float64                            `json:"minimum"`
	Maximum    *float64                            `json:"maximum"`
	MinLength  *int64                              `json:"minLength"`
	MaxLength  *int64                              `json:"maxLength"`
	MinItems   *int64                              `json:"minItems"`
	MaxItems   *int64                              `json:"maxItems"`
	XPattern   string                              `json:"x-pattern"`
}

type backupAssetSwaggerDocument struct {
	Paths map[string]map[string]struct {
		Responses map[string]json.RawMessage `json:"responses"`
	} `json:"paths"`
	Definitions map[string]backupAssetSwaggerSchema `json:"definitions"`
}

func readBackupAssetSwagger(t *testing.T) backupAssetSwaggerDocument {
	t.Helper()
	var document backupAssetSwaggerDocument
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("decode generated Swagger: %v", err)
	}
	return document
}

func TestBackupAssetProtectedSwaggerDocumentsAuthFailures(t *testing.T) {
	document := readBackupAssetSwagger(t)
	operations := []struct {
		method string
		path   string
	}{
		{"post", "/asset-exports"},
		{"get", "/asset-exports/{id}"},
		{"post", "/asset-exports/{id}/cancel"},
		{"post", "/asset-exports/{id}/download-ticket"},
		{"get", "/recovery-points/{id}/entries/{entryId}/archive-members"},
		{"post", "/recovery-points/{id}/entries/{entryId}/archive-member-jobs"},
		{"get", "/recovery-points/{id}/entries/{entryId}/archive-member-jobs/{jobId}"},
		{"post", "/recovery-points/{id}/entries/{entryId}/archive-member-jobs/{jobId}/cancel"},
		{"post", "/recovery-points/{id}/entries/{entryId}/archive-member-jobs/{jobId}/delivery-ticket"},
	}
	for _, expected := range operations {
		operation, ok := document.Paths[expected.path][expected.method]
		if !ok {
			t.Fatalf("generated Swagger missing %s %s", strings.ToUpper(expected.method), expected.path)
		}
		for _, status := range []string{"401", "403"} {
			if _, ok := operation.Responses[status]; !ok {
				t.Errorf("generated Swagger missing %s for %s %s", status, strings.ToUpper(expected.method), expected.path)
			}
		}
	}
}

func TestBackupAssetSwaggerDocumentsStrictRequestDTOs(t *testing.T) {
	document := readBackupAssetSwagger(t)
	const prefix = "internal_api_handlers."

	selection := requireBackupAssetSwaggerDefinition(t, document, prefix+"backupAssetExportSelectionPayload")
	requireBackupAssetSwaggerFields(t, selection, "kind", "schema_version")
	requireBackupAssetSwaggerVersion(t, selection)
	requireBackupAssetSwaggerEnum(t, selection.Properties["kind"], "explicit", "saved_search")
	requireBackupAssetSwaggerBounds(t, selection.Properties["refs"], nil, nil, int64Pointer(1), int64Pointer(100000))
	requireBackupAssetSwaggerOpaqueID(t, selection.Properties["saved_search_id"], 32)
	requireBackupAssetSwaggerBounds(t, selection.Properties["saved_search_version"], float64Pointer(1), nil, nil, nil)

	create := requireBackupAssetSwaggerDefinition(t, document, prefix+"backupAssetExportCreatePayload")
	requireBackupAssetSwaggerFields(t, create, "archive_format", "archive_profile", "schema_version", "selection")
	requireBackupAssetSwaggerVersion(t, create)
	requireBackupAssetSwaggerEnum(t, create.Properties["archive_format"], "zip", "tar")
	requireBackupAssetSwaggerEnum(t, create.Properties["archive_profile"], "zip_deflate_v1", "tar_none_v1", "tar_gzip_v1")

	version := requireBackupAssetSwaggerDefinition(t, document, prefix+"backupAssetExportVersionPayload")
	requireBackupAssetSwaggerFields(t, version, "schema_version")
	requireBackupAssetSwaggerVersion(t, version)

	archiveCreate := requireBackupAssetSwaggerDefinition(t, document, prefix+"backupArchiveMemberCreatePayload")
	requireBackupAssetSwaggerFields(t, archiveCreate, "index_revision", "member_chain", "schema_version")
	requireBackupAssetSwaggerVersion(t, archiveCreate)
	requireBackupAssetSwaggerOpaqueID(t, archiveCreate.Properties["index_revision"], 64)
	memberChain := archiveCreate.Properties["member_chain"]
	requireBackupAssetSwaggerBounds(t, memberChain, nil, nil, int64Pointer(1), int64Pointer(1))
	if memberChain.Items == nil {
		t.Fatal("member_chain Swagger schema is missing its item schema")
	}
	requireBackupAssetSwaggerBounds(t, *memberChain.Items, nil, nil, int64Pointer(32), int64Pointer(32))
	if memberChain.XPattern != "" {
		t.Errorf("member_chain array Swagger x-pattern=%q want empty", memberChain.XPattern)
	}
	if memberChain.Items.Format != "lowercase-hex-32" {
		t.Errorf("member_chain item Swagger format=%q want lowercase-hex-32", memberChain.Items.Format)
	}

	archiveBound := requireBackupAssetSwaggerDefinition(t, document, prefix+"backupArchiveMemberBoundPayload")
	requireBackupAssetSwaggerFields(t, archiveBound, "index_revision", "schema_version")
	requireBackupAssetSwaggerVersion(t, archiveBound)
	requireBackupAssetSwaggerOpaqueID(t, archiveBound.Properties["index_revision"], 64)
}

func requireBackupAssetSwaggerDefinition(
	t *testing.T,
	document backupAssetSwaggerDocument,
	name string,
) backupAssetSwaggerSchema {
	t.Helper()
	schema, ok := document.Definitions[name]
	if !ok {
		t.Fatalf("generated Swagger missing definition %s", name)
	}
	return schema
}

func requireBackupAssetSwaggerFields(t *testing.T, schema backupAssetSwaggerSchema, expected ...string) {
	t.Helper()
	required := make(map[string]bool, len(schema.Required))
	for _, field := range schema.Required {
		required[field] = true
	}
	if len(required) != len(expected) {
		t.Errorf("Swagger required fields=%v want=%v", schema.Required, expected)
	}
	for _, field := range expected {
		if !required[field] {
			t.Errorf("Swagger required fields missing %q: %v", field, schema.Required)
		}
	}
}

func requireBackupAssetSwaggerVersion(t *testing.T, schema backupAssetSwaggerSchema) {
	t.Helper()
	version, ok := schema.Properties["schema_version"]
	if !ok {
		t.Fatal("Swagger schema is missing schema_version")
	}
	requireBackupAssetSwaggerBounds(t, version, float64Pointer(1), float64Pointer(1), nil, nil)
}

func requireBackupAssetSwaggerEnum(t *testing.T, schema backupAssetSwaggerSchema, expected ...string) {
	t.Helper()
	actual := make(map[string]bool, len(schema.Enum))
	for _, value := range schema.Enum {
		actual[value] = true
	}
	if len(actual) != len(expected) {
		t.Errorf("Swagger enum=%v want=%v", schema.Enum, expected)
	}
	for _, value := range expected {
		if !actual[value] {
			t.Errorf("Swagger enum missing %q: %v", value, schema.Enum)
		}
	}
}

func requireBackupAssetSwaggerOpaqueID(t *testing.T, schema backupAssetSwaggerSchema, length int64) {
	t.Helper()
	requireBackupAssetSwaggerBounds(t, schema, nil, nil, &length, &length)
	wantPattern := fmt.Sprintf("^[0-9a-f]{%d}$", length)
	if schema.XPattern != wantPattern {
		t.Errorf("Swagger x-pattern=%q want=%q", schema.XPattern, wantPattern)
	}
}

func requireBackupAssetSwaggerBounds(
	t *testing.T,
	schema backupAssetSwaggerSchema,
	minimum *float64,
	maximum *float64,
	minimumCount *int64,
	maximumCount *int64,
) {
	t.Helper()
	if !equalFloat64Pointers(schema.Minimum, minimum) || !equalFloat64Pointers(schema.Maximum, maximum) ||
		!equalInt64Pointers(swaggerMinimumCount(schema), minimumCount) ||
		!equalInt64Pointers(swaggerMaximumCount(schema), maximumCount) {
		t.Errorf("Swagger bounds min=%v max=%v minLength=%v maxLength=%v minItems=%v maxItems=%v",
			schema.Minimum, schema.Maximum, schema.MinLength, schema.MaxLength, schema.MinItems, schema.MaxItems)
	}
}

func swaggerMinimumCount(schema backupAssetSwaggerSchema) *int64 {
	if schema.Type == "array" {
		return schema.MinItems
	}
	return schema.MinLength
}

func swaggerMaximumCount(schema backupAssetSwaggerSchema) *int64 {
	if schema.Type == "array" {
		return schema.MaxItems
	}
	return schema.MaxLength
}

func float64Pointer(value float64) *float64 { return &value }
func int64Pointer(value int64) *int64       { return &value }

func equalFloat64Pointers(left, right *float64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalInt64Pointers(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func TestBackupArchiveHandlerCollapsesForeignAndMalformedBindings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &backupArchiveServiceFake{pollErr: processing.ErrArchiveMemberUnavailable}
	handler := NewBackupArchiveHandler(service, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(7))
		c.Set(middleware.CtxRole, "operator")
		c.Next()
	})
	base := "/api/v1/recovery-points/:id/entries/:entryId"
	router.GET(base+"/archive-member-jobs/:jobId", handler.Status)
	pointID, entryID, requestID := strings.Repeat("1", 32), strings.Repeat("2", 64), strings.Repeat("3", 32)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/recovery-points/"+pointID+"/entries/"+entryID+"/archive-member-jobs/"+requestID+
			"?index_revision="+strings.Repeat("4", 64), nil))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), requestID) {
		t.Fatalf("foreign status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBackupArchiveHandlerPrivateNetworkHTTPDeliveryTicketRequiresExactAssetDownloadPurpose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	policy, err := NewBackupContentSchemePolicy([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	db, jwtManager, user := backupAssetExportHandlerProofFixture(t)
	proofs := make(map[auth.StepUpAction]string)
	for _, action := range []auth.StepUpAction{
		auth.StepUpActionAssetDownload,
		auth.StepUpActionAssetExportCreate,
		auth.StepUpActionAssetExportDownload,
	} {
		proof, _, err := jwtManager.GenerateStepUpToken(user, action)
		if err != nil {
			t.Fatal(err)
		}
		proofs[action] = proof
	}
	pointID, entryID, requestID := strings.Repeat("1", 32), strings.Repeat("2", 64), strings.Repeat("3", 32)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}
	tests := []struct {
		name   string
		proof  string
		wantOK bool
	}{
		{name: "missing proof"},
		{name: "export create proof", proof: proofs[auth.StepUpActionAssetExportCreate]},
		{name: "export download proof", proof: proofs[auth.StepUpActionAssetExportDownload]},
		{name: "exact asset download proof", proof: proofs[auth.StepUpActionAssetDownload], wantOK: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service := &backupArchiveServiceFake{readyAsset: content.AuthorizedAsset{Ref: ref}}
			ticket := backupAssetExportHandlerTicket(t)
			ticket.Descriptor.ContentType = "application/octet-stream"
			ticket.Descriptor.Range = content.RangeNone
			ticket.Cookie.Secure = false
			delivery := &backupArchiveDeliveryFake{ticket: ticket}
			handler := NewBackupArchiveHandler(service, delivery, db, jwtManager, nil, func(context.Context) (BackupContentHandlerConfig, error) {
				return BackupContentHandlerConfig{
					TicketTimeout: 5 * time.Second, AllowInsecurePrivateNetwork: true,
				}, nil
			}).WithSchemePolicy(policy)
			router := gin.New()
			base := "/api/v1/recovery-points/:id/entries/:entryId/archive-member-jobs/:jobId/delivery-ticket"
			router.POST(base, func(c *gin.Context) {
				c.Set(middleware.CtxUserID, user.ID)
				c.Set(middleware.CtxUsername, user.Username)
				c.Set(middleware.CtxRole, user.Role)
				c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
					JTI: strings.Repeat("f", 32), UserID: user.ID, Role: user.Role,
					TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
				})
				c.Next()
			}, handler.DeliveryTicket)
			request := httptest.NewRequest(http.MethodPost,
				"http://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+
					"/archive-member-jobs/"+requestID+"/delivery-ticket",
				strings.NewReader(`{"schema_version":1}`))
			request.RemoteAddr = "127.0.0.1:43210"
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Forwarded-Proto", "http")
			request.Header.Set("X-Forwarded-For", "192.168.20.45")
			if testCase.proof != "" {
				request.Header.Set(StepUpHeaderName, testCase.proof)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if testCase.wantOK {
				if response.Code != http.StatusOK || len(service.readyLookups) != 1 || len(delivery.requests) != 1 ||
					delivery.requests[0].Proof.Action != auth.StepUpActionAssetDownload ||
					delivery.requests[0].MemberRequestID != requestID || delivery.requests[0].Asset.Ref != ref ||
					delivery.requests[0].SecureCookie {
					t.Fatalf("status=%d lookups=%+v requests=%+v body=%s", response.Code, service.readyLookups, delivery.requests, response.Body.String())
				}
			} else if response.Code != http.StatusForbidden || len(service.readyLookups) != 0 || len(delivery.requests) != 0 {
				t.Fatalf("status=%d lookups=%+v requests=%+v body=%s", response.Code, service.readyLookups, delivery.requests, response.Body.String())
			}
		})
	}
	service := &backupArchiveServiceFake{readyAsset: content.AuthorizedAsset{Ref: ref}}
	delivery := &backupArchiveDeliveryFake{ticket: backupAssetExportHandlerTicket(t)}
	handler := NewBackupArchiveHandler(service, delivery, db, jwtManager, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
	})
	router := gin.New()
	base := "/api/v1/recovery-points/:id/entries/:entryId/archive-member-jobs/:jobId/delivery-ticket"
	router.POST(base, func(c *gin.Context) {
		c.Set(middleware.CtxUserID, user.ID)
		c.Set(middleware.CtxUsername, user.Username)
		c.Set(middleware.CtxRole, user.Role)
		c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
			JTI: strings.Repeat("f", 32), UserID: user.ID, Role: user.Role,
			TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
		})
		c.Next()
	}, handler.DeliveryTicket)
	insecure := httptest.NewRequest(http.MethodPost,
		"http://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+
			"/archive-member-jobs/"+requestID+"/delivery-ticket",
		strings.NewReader(`{"schema_version":1}`))
	insecure.RemoteAddr = "203.0.113.5:43210"
	insecure.Header.Set("Content-Type", "application/json")
	insecure.Header.Set(StepUpHeaderName, proofs[auth.StepUpActionAssetDownload])
	insecureResponse := httptest.NewRecorder()
	router.ServeHTTP(insecureResponse, insecure)
	assertSecureTransportRequiredResponse(t, insecureResponse)
	if len(service.readyLookups) != 0 || len(delivery.requests) != 0 {
		t.Fatalf("insecure Archive ticket reached services lookups=%d delivery=%d", len(service.readyLookups), len(delivery.requests))
	}
}

func TestBackupArchiveHandlerDeliveryTicketRejectsUnsafeIssuedTicket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, jwtManager, user := backupAssetExportHandlerProofFixture(t)
	proof, _, err := jwtManager.GenerateStepUpToken(user, auth.StepUpActionAssetDownload)
	if err != nil {
		t.Fatal(err)
	}
	pointID, entryID, requestID := strings.Repeat("1", 32), strings.Repeat("2", 64), strings.Repeat("3", 32)
	ref := backupasset.AssetRef{RecoveryPointID: pointID, EntryID: entryID}
	tests := []struct {
		name   string
		mutate func(*assetexport.IssuedDeliveryTicket)
	}{
		{name: "same site lax", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Cookie.SameSite = http.SameSiteLaxMode
		}},
		{name: "cookie domain", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Cookie.Domain = "example.invalid"
		}},
		{name: "expired ticket", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			expired := time.Now().UTC().Add(-time.Minute)
			ticket.Descriptor.ExpiresAt = expired
			ticket.Descriptor.IdleExpiresAt = expired
			ticket.Cookie.Expires = expired
		}},
		{name: "cookie expiry mismatch", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Cookie.Expires = ticket.Cookie.Expires.Add(time.Second)
		}},
		{name: "content url query", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Descriptor.ContentURL += "?ticket=unexpected"
			ticket.Cookie.Path = ticket.Descriptor.ContentURL
		}},
		{name: "content url child path", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Descriptor.ContentURL += "/unexpected"
			ticket.Cookie.Path = ticket.Descriptor.ContentURL
		}},
		{name: "range single", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Descriptor.Range = content.RangeSingle
		}},
		{name: "unsafe content type", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Descriptor.ContentType = "text/html"
		}},
		{name: "weak etag", mutate: func(ticket *assetexport.IssuedDeliveryTicket) {
			ticket.Descriptor.ETag = `W/"` + strings.Repeat("e", 64) + `"`
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			ticket := backupAssetExportHandlerTicket(t)
			ticket.Descriptor.ContentType = "application/octet-stream"
			ticket.Descriptor.Range = content.RangeNone
			testCase.mutate(&ticket)
			service := &backupArchiveServiceFake{readyAsset: content.AuthorizedAsset{Ref: ref}}
			delivery := &backupArchiveDeliveryFake{ticket: ticket}
			handler := NewBackupArchiveHandler(service, delivery, db, jwtManager, nil, func(context.Context) (BackupContentHandlerConfig, error) {
				return BackupContentHandlerConfig{TicketTimeout: 5 * time.Second}, nil
			})
			router := gin.New()
			base := "/api/v1/recovery-points/:id/entries/:entryId/archive-member-jobs/:jobId/delivery-ticket"
			router.POST(base, func(c *gin.Context) {
				c.Set(middleware.CtxUserID, user.ID)
				c.Set(middleware.CtxUsername, user.Username)
				c.Set(middleware.CtxRole, user.Role)
				c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
					JTI: strings.Repeat("f", 32), UserID: user.ID, Role: user.Role,
					TokenVersion: user.TokenVersion, ExpiresAt: time.Now().UTC().Add(time.Hour),
				})
				c.Next()
			}, handler.DeliveryTicket)
			request := httptest.NewRequest(http.MethodPost,
				"https://xirang.example/api/v1/recovery-points/"+pointID+"/entries/"+entryID+
					"/archive-member-jobs/"+requestID+"/delivery-ticket",
				strings.NewReader(`{"schema_version":1}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(StepUpHeaderName, proof)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable || len(service.readyLookups) != 1 || len(delivery.requests) != 1 ||
				len(response.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("status=%d lookups=%+v requests=%+v set_cookie=%v body=%s", response.Code, service.readyLookups,
					delivery.requests, response.Header().Values("Set-Cookie"), response.Body.String())
			}
		})
	}
}

type backupArchiveServiceFake struct {
	index          processing.ArchiveMemberIndexView
	created        processing.ArchiveMemberCreateResult
	status         processing.ArchiveMemberStatusResult
	indexErr       error
	createErr      error
	pollErr        error
	cancelErr      error
	readyAsset     content.AuthorizedAsset
	readyErr       error
	indexLookups   []processing.ArchiveMemberIndexLookup
	createRequests []processing.ArchiveMemberCreateRequest
	pollLookups    []processing.ArchiveMemberLookup
	cancelLookups  []processing.ArchiveMemberLookup
	readyLookups   []processing.ArchiveMemberLookup
}

type backupArchiveReplayCoordinator struct {
	result processing.WorkResult
}

func (fake *backupArchiveReplayCoordinator) RequestWork(context.Context, processing.WorkRequest) (processing.WorkResult, error) {
	return fake.result, nil
}

func (*backupArchiveReplayCoordinator) RemoveInterest(
	context.Context,
	string,
	processing.InterestOwnerKind,
	string,
	processing.InterestRemovedReason,
) error {
	return nil
}

type backupArchiveReplayAuthorizer struct {
	asset content.AuthorizedAsset
}

func (fake backupArchiveReplayAuthorizer) Authorize(
	context.Context,
	content.DeliveryActor,
	backupasset.AssetRef,
	content.DeliveryAction,
) (content.AuthorizedAsset, error) {
	return fake.asset, nil
}

func backupArchiveReplayIndex(now time.Time, revision string, memberID string) processing.ArchiveMemberIndexBinding {
	return processing.ArchiveMemberIndexBinding{
		ArtifactID: strings.Repeat("5", 32), Revision: revision,
		PipelineFingerprint: "archive-inspect-pipeline-v1", SecurityPolicyRevision: "security-policy-v1",
		AbsoluteExpiresAt: now.Add(time.Hour),
		Members: []processing.ArchiveMemberIndexEntry{{
			OpaqueID: memberID, Ordinal: 7, DisplayName: "member.txt", Size: 3, MediaType: "text/plain",
		}},
	}
}

func backupArchiveHandlerReplayDB(t *testing.T, now time.Time) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf(
		"file:%s-%d?mode=memory&cache=shared&_busy_timeout=5000&_foreign_keys=ON&_txlock=immediate&_loc=UTC",
		strings.ReplaceAll(t.Name(), "/", "_"), backupArchiveHandlerReplayDBSequence.Add(1),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		NowFunc: func() time.Time { return now }, DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.BackupAssetArchiveMemberRequest{}, &model.BackupAssetExportQuotaBucket{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func decodeBackupArchiveCreateResponse(t *testing.T, response *httptest.ResponseRecorder) processing.ArchiveMemberCreateResult {
	t.Helper()
	var payload struct {
		Code int                                  `json:"code"`
		Data processing.ArchiveMemberCreateResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode archive create response: %v body=%s", err, response.Body.String())
	}
	if payload.Code != http.StatusAccepted {
		t.Fatalf("archive create envelope=%+v body=%s", payload, response.Body.String())
	}
	return payload.Data
}

func (fake *backupArchiveServiceFake) ListIndex(_ context.Context, lookup processing.ArchiveMemberIndexLookup) (processing.ArchiveMemberIndexView, error) {
	fake.indexLookups = append(fake.indexLookups, lookup)
	return fake.index, fake.indexErr
}

func (fake *backupArchiveServiceFake) Create(_ context.Context, request processing.ArchiveMemberCreateRequest) (processing.ArchiveMemberCreateResult, error) {
	fake.createRequests = append(fake.createRequests, request)
	return fake.created, fake.createErr
}

func (fake *backupArchiveServiceFake) Reconcile(context.Context, string) error { return nil }

func (fake *backupArchiveServiceFake) Poll(_ context.Context, lookup processing.ArchiveMemberLookup) (processing.ArchiveMemberStatusResult, error) {
	fake.pollLookups = append(fake.pollLookups, lookup)
	return fake.status, fake.pollErr
}

func (fake *backupArchiveServiceFake) Cancel(_ context.Context, lookup processing.ArchiveMemberLookup) error {
	fake.cancelLookups = append(fake.cancelLookups, lookup)
	return fake.cancelErr
}

func (fake *backupArchiveServiceFake) AuthorizeReadyDelivery(
	_ context.Context,
	lookup processing.ArchiveMemberLookup,
) (content.AuthorizedAsset, error) {
	fake.readyLookups = append(fake.readyLookups, lookup)
	return fake.readyAsset, fake.readyErr
}

type backupArchiveDeliveryFake struct {
	ticket   assetexport.IssuedDeliveryTicket
	err      error
	requests []assetexport.ArchiveMemberDeliveryIssueRequest
}

func (fake *backupArchiveDeliveryFake) IssueArchiveMember(
	_ context.Context,
	request assetexport.ArchiveMemberDeliveryIssueRequest,
) (assetexport.IssuedDeliveryTicket, error) {
	fake.requests = append(fake.requests, request)
	return fake.ticket, fake.err
}

type backupArchiveAuditFake struct {
	inputs []backupasset.AuditEventInput
}

func (fake *backupArchiveAuditFake) Write(_ context.Context, input backupasset.AuditEventInput) error {
	fake.inputs = append(fake.inputs, input)
	return nil
}
