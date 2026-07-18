package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/overlay"
	assetsearch "xirang/backend/internal/backupasset/search"

	"github.com/gin-gonic/gin"
)

type backupAssetOverlayServiceSpy struct {
	calls          int
	idempotencyKey string
	ownerID        uint
	id             string
	ref            backupasset.AssetRef
}

func (spy *backupAssetOverlayServiceSpy) ListSavedSearches(context.Context, uint, overlay.OverlayListRequest) (overlay.SavedSearchPage, error) {
	spy.calls++
	return overlay.SavedSearchPage{Items: []overlay.SavedSearch{}}, nil
}
func (spy *backupAssetOverlayServiceSpy) CreateSavedSearch(_ context.Context, actor overlay.Actor, request overlay.CreateSavedSearchRequest) (overlay.SavedSearch, error) {
	spy.calls++
	spy.ownerID, spy.idempotencyKey = actor.UserID, request.IdempotencyKey
	return overlay.SavedSearch{ID: strings.Repeat("a", 32), OwnerUserID: actor.UserID, Version: 1, State: overlay.SavedSearchActive}, nil
}
func (spy *backupAssetOverlayServiceSpy) GetSavedSearch(_ context.Context, ownerID uint, id string) (overlay.SavedSearch, error) {
	spy.calls++
	spy.ownerID, spy.id = ownerID, id
	return overlay.SavedSearch{ID: id, OwnerUserID: ownerID, Version: 1, State: overlay.SavedSearchActive}, nil
}
func (spy *backupAssetOverlayServiceSpy) UpdateSavedSearch(_ context.Context, actor overlay.Actor, id string, request overlay.UpdateSavedSearchRequest) (overlay.SavedSearch, error) {
	spy.calls++
	spy.ownerID, spy.id, spy.idempotencyKey = actor.UserID, id, request.IdempotencyKey
	return overlay.SavedSearch{ID: id, OwnerUserID: actor.UserID, Version: request.ExpectedVersion + 1, State: overlay.SavedSearchActive}, nil
}
func (spy *backupAssetOverlayServiceSpy) DeleteSavedSearch(_ context.Context, ownerID uint, id string, _ int, key string) error {
	spy.calls++
	spy.ownerID, spy.id, spy.idempotencyKey = ownerID, id, key
	return nil
}
func (spy *backupAssetOverlayServiceSpy) ListFavorites(context.Context, uint, overlay.OverlayListRequest) (overlay.FavoritePage, error) {
	spy.calls++
	return overlay.FavoritePage{Items: []overlay.Favorite{}}, nil
}
func (spy *backupAssetOverlayServiceSpy) AddFavorite(_ context.Context, actor overlay.Actor, request overlay.AddFavoriteRequest) (overlay.Favorite, error) {
	spy.calls++
	spy.ownerID, spy.ref, spy.idempotencyKey = actor.UserID, request.Ref, request.IdempotencyKey
	return overlay.Favorite{ID: strings.Repeat("b", 32), OwnerUserID: actor.UserID, Ref: request.Ref, Version: 1, State: overlay.OverlayActive}, nil
}
func (spy *backupAssetOverlayServiceSpy) RemoveFavorite(_ context.Context, ownerID uint, ref backupasset.AssetRef, key string) error {
	spy.calls++
	spy.ownerID, spy.ref, spy.idempotencyKey = ownerID, ref, key
	return nil
}
func (spy *backupAssetOverlayServiceSpy) ListTags(context.Context, uint, overlay.OverlayListRequest) (overlay.TagPage, error) {
	spy.calls++
	return overlay.TagPage{Items: []overlay.Tag{}}, nil
}
func (spy *backupAssetOverlayServiceSpy) CreateTag(_ context.Context, ownerID uint, _ string, key string) (overlay.Tag, error) {
	spy.calls++
	spy.ownerID, spy.idempotencyKey = ownerID, key
	return overlay.Tag{ID: strings.Repeat("c", 32), OwnerUserID: ownerID, Version: 1}, nil
}
func (spy *backupAssetOverlayServiceSpy) UpdateTag(_ context.Context, ownerID uint, id string, request overlay.UpdateTagRequest) (overlay.Tag, error) {
	spy.calls++
	spy.ownerID, spy.id, spy.idempotencyKey = ownerID, id, request.IdempotencyKey
	return overlay.Tag{ID: id, OwnerUserID: ownerID, Version: request.ExpectedVersion + 1}, nil
}
func (spy *backupAssetOverlayServiceSpy) DeleteTag(_ context.Context, ownerID uint, id string, _ int, key string) error {
	spy.calls++
	spy.ownerID, spy.id, spy.idempotencyKey = ownerID, id, key
	return nil
}
func (spy *backupAssetOverlayServiceSpy) AssignTag(_ context.Context, actor overlay.Actor, id string, ref backupasset.AssetRef, key string) (overlay.TagAssignment, error) {
	spy.calls++
	spy.ownerID, spy.id, spy.ref, spy.idempotencyKey = actor.UserID, id, ref, key
	return overlay.TagAssignment{ID: strings.Repeat("d", 32), OwnerUserID: actor.UserID, TagID: id, Ref: ref, Version: 1, State: overlay.OverlayActive}, nil
}
func (spy *backupAssetOverlayServiceSpy) UnassignTag(_ context.Context, ownerID uint, id string, ref backupasset.AssetRef, key string) error {
	spy.calls++
	spy.ownerID, spy.id, spy.ref, spy.idempotencyKey = ownerID, id, ref, key
	return nil
}
func (spy *backupAssetOverlayServiceSpy) ListRecent(context.Context, uint, overlay.OverlayListRequest) (overlay.RecentAccessPage, error) {
	spy.calls++
	return overlay.RecentAccessPage{Items: []overlay.RecentAccess{}}, nil
}
func (spy *backupAssetOverlayServiceSpy) ClearRecent(_ context.Context, ownerID uint, key string) (int64, error) {
	spy.calls++
	spy.ownerID, spy.idempotencyKey = ownerID, key
	return 2, nil
}

func TestSavedSearchFavoriteAssetTagRecentHandlerRouteMatrixAndTypedAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	id := strings.Repeat("a", 32)
	pointID := strings.Repeat("1", 32)
	entryID := strings.Repeat("2", 64)
	query := `{"schema_version":1,"root":{"op":"term","field":"name","text":"report"},"scope":{"mode":"current"},"sort":"relevance","limit":10}`
	for _, test := range []struct {
		name       string
		method     string
		path       string
		body       string
		mutation   bool
		wantStatus int
		wantAction backupasset.AuditAction
		register   func(*gin.Engine, *BackupAssetOverlayHandler)
	}{
		{"saved list", http.MethodGet, "/asset-saved-searches?limit=20", "", false, http.StatusOK, "", func(r *gin.Engine, h *BackupAssetOverlayHandler) { r.GET("/asset-saved-searches", h.ListSavedSearches) }},
		{"saved create", http.MethodPost, "/asset-saved-searches", `{"query":` + query + `}`, true, http.StatusCreated, backupasset.AuditActionSavedSearchCreate, func(r *gin.Engine, h *BackupAssetOverlayHandler) {
			r.POST("/asset-saved-searches", h.CreateSavedSearch)
		}},
		{"saved get", http.MethodGet, "/asset-saved-searches/" + id, "", false, http.StatusOK, "", func(r *gin.Engine, h *BackupAssetOverlayHandler) {
			r.GET("/asset-saved-searches/:id", h.GetSavedSearch)
		}},
		{"saved update", http.MethodPatch, "/asset-saved-searches/" + id, `{"query":` + query + `,"expected_version":1}`, true, http.StatusOK, backupasset.AuditActionSavedSearchUpdate, func(r *gin.Engine, h *BackupAssetOverlayHandler) {
			r.PATCH("/asset-saved-searches/:id", h.UpdateSavedSearch)
		}},
		{"saved delete", http.MethodDelete, "/asset-saved-searches/" + id, `{"expected_version":1}`, true, http.StatusOK, backupasset.AuditActionSavedSearchDelete, func(r *gin.Engine, h *BackupAssetOverlayHandler) {
			r.DELETE("/asset-saved-searches/:id", h.DeleteSavedSearch)
		}},
		{"favorite list", http.MethodGet, "/asset-favorites", "", false, http.StatusOK, "", func(r *gin.Engine, h *BackupAssetOverlayHandler) { r.GET("/asset-favorites", h.ListFavorites) }},
		{"favorite add", http.MethodPost, "/asset-favorites", `{"ref":{"recovery_point_id":"` + pointID + `","entry_id":"` + entryID + `"},"label":"mine"}`, true, http.StatusCreated, backupasset.AuditActionFavoriteAdd, func(r *gin.Engine, h *BackupAssetOverlayHandler) { r.POST("/asset-favorites", h.AddFavorite) }},
		{"favorite remove", http.MethodDelete, "/asset-favorites/" + pointID + "/" + entryID, "", true, http.StatusOK, backupasset.AuditActionFavoriteRemove, func(r *gin.Engine, h *BackupAssetOverlayHandler) {
			r.DELETE("/asset-favorites/:recoveryPointId/:entryId", h.RemoveFavorite)
		}},
		{"tag list", http.MethodGet, "/asset-tags", "", false, http.StatusOK, "", func(r *gin.Engine, h *BackupAssetOverlayHandler) { r.GET("/asset-tags", h.ListTags) }},
		{"tag create", http.MethodPost, "/asset-tags", `{"name":"Finance"}`, true, http.StatusCreated, backupasset.AuditActionTagCreate, func(r *gin.Engine, h *BackupAssetOverlayHandler) { r.POST("/asset-tags", h.CreateTag) }},
		{"tag update", http.MethodPatch, "/asset-tags/" + id, `{"name":"Finance 2026","expected_version":1}`, true, http.StatusOK, backupasset.AuditActionTagUpdate, func(r *gin.Engine, h *BackupAssetOverlayHandler) { r.PATCH("/asset-tags/:id", h.UpdateTag) }},
		{"tag delete", http.MethodDelete, "/asset-tags/" + id, `{"expected_version":1}`, true, http.StatusOK, backupasset.AuditActionTagDelete, func(r *gin.Engine, h *BackupAssetOverlayHandler) { r.DELETE("/asset-tags/:id", h.DeleteTag) }},
		{"tag assign", http.MethodPost, "/asset-tags/" + id + "/assignments", `{"ref":{"recovery_point_id":"` + pointID + `","entry_id":"` + entryID + `"}}`, true, http.StatusCreated, backupasset.AuditActionTagAssign, func(r *gin.Engine, h *BackupAssetOverlayHandler) { r.POST("/asset-tags/:id/assignments", h.AssignTag) }},
		{"tag unassign", http.MethodDelete, "/asset-tags/" + id + "/assignments/" + pointID + "/" + entryID, "", true, http.StatusOK, backupasset.AuditActionTagUnassign, func(r *gin.Engine, h *BackupAssetOverlayHandler) {
			r.DELETE("/asset-tags/:id/assignments/:recoveryPointId/:entryId", h.UnassignTag)
		}},
		{"recent list", http.MethodGet, "/asset-recent", "", false, http.StatusOK, "", func(r *gin.Engine, h *BackupAssetOverlayHandler) { r.GET("/asset-recent", h.ListRecent) }},
		{"recent clear", http.MethodPost, "/asset-recent/clear", "", true, http.StatusOK, backupasset.AuditActionRecentClear, func(r *gin.Engine, h *BackupAssetOverlayHandler) { r.POST("/asset-recent/clear", h.ClearRecent) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &backupAssetOverlayServiceSpy{}
			audit := &backupAssetAuditSpy{}
			handler := NewBackupAssetOverlayHandler(service, audit, backupAssetHandlerConfigEnabled)
			router := backupAssetHandlerTestRouter()
			test.register(router, handler)
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.mutation {
				request.Header.Set("Idempotency-Key", "overlay-handler-key-01")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus || service.calls != 1 || service.ownerID != 0 && service.ownerID != 77 {
				t.Fatalf("status=%d want=%d body=%s calls=%d owner=%d", response.Code, test.wantStatus, response.Body.String(), service.calls, service.ownerID)
			}
			for _, forbidden := range []string{`"OwnerUserID"`, `"ID"`, `"Ref"`, `"Version"`, `"CreatedAt"`} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("wire response contains Go/internal field %s: %s", forbidden, response.Body.String())
				}
			}
			if test.mutation && service.idempotencyKey != "overlay-handler-key-01" {
				t.Fatalf("idempotency key not forwarded: %q", service.idempotencyKey)
			}
			if test.wantAction == "" {
				if len(audit.inputs) != 0 {
					t.Fatalf("read unexpectedly audited: %+v", audit.inputs)
				}
			} else if len(audit.inputs) != 1 || audit.inputs[0].Action != test.wantAction || audit.inputs[0].Outcome != backupasset.AuditOutcomeSuccess {
				t.Fatalf("audit=%+v", audit.inputs)
			}
		})
	}
}

func TestSavedSearchFavoriteAssetTagRecentHandlerRejectsInvalidIdempotencyAndDisabledBeforeServiceAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name   string
		body   string
		key    string
		config BackupAssetHandlerConfigSource
		want   int
	}{
		{"short key", `{"name":"Finance"}`, "short", backupAssetHandlerConfigEnabled, http.StatusBadRequest},
		{"long key", `{"name":"Finance"}`, strings.Repeat("a", 129), backupAssetHandlerConfigEnabled, http.StatusBadRequest},
		{"unknown body", `{"name":"Finance","path":"private"}`, "overlay-handler-key-01", backupAssetHandlerConfigEnabled, http.StatusBadRequest},
		{"trailing body", `{"name":"Finance"}{}`, "overlay-handler-key-01", backupAssetHandlerConfigEnabled, http.StatusBadRequest},
		{"disabled", `{"name":"Finance"}`, "overlay-handler-key-01", func() (BackupAssetHandlerConfig, error) {
			return BackupAssetHandlerConfig{Enabled: false, QueryLimits: assetsearch.DefaultQueryLimits(), IdempotencyKeyMaxBytes: 128}, nil
		}, http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &backupAssetOverlayServiceSpy{}
			audit := &backupAssetAuditSpy{}
			handler := NewBackupAssetOverlayHandler(service, audit, test.config)
			router := backupAssetHandlerTestRouter()
			router.POST("/asset-tags", handler.CreateTag)
			request := httptest.NewRequest(http.MethodPost, "/asset-tags", strings.NewReader(test.body))
			request.Header.Set("Idempotency-Key", test.key)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want || service.calls != 0 || len(audit.inputs) != 0 {
				t.Fatalf("status=%d want=%d body=%s calls=%d audit=%d", response.Code, test.want, response.Body.String(), service.calls, len(audit.inputs))
			}
		})
	}
}
