package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/catalog"
	"xirang/backend/internal/backupasset/content"
	"xirang/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

type previewSourcePreparationFunc func(context.Context, content.DeliveryActor, backupasset.AssetRef) (catalog.StatusDTO, error)

func (prepare previewSourcePreparationFunc) PreparePreviewSource(ctx context.Context, actor content.DeliveryActor, ref backupasset.AssetRef) (catalog.StatusDTO, error) {
	return prepare(ctx, actor, ref)
}

func previewSourceHandlerRouter(preparer BackupPreviewSourcePreparer, session bool, sessionUser uint) *gin.Engine {
	handler := NewBackupContentHandler(nil, nil, nil, func(context.Context) (BackupContentHandlerConfig, error) {
		return BackupContentHandlerConfig{TicketTimeout: time.Second}, nil
	}).WithPreviewSourcePreparer(preparer)
	router := gin.New()
	router.POST("/api/v1/recovery-points/:id/entries/:entryId/preview-source", func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(42))
		c.Set(middleware.CtxUsername, "operator")
		c.Set(middleware.CtxRole, "operator")
		if session {
			c.Set(middleware.CtxSessionBinding, middleware.SessionBinding{
				JTI: strings.Repeat("f", 32), UserID: sessionUser, Role: "operator", TokenVersion: 1,
				ExpiresAt: time.Now().Add(time.Hour),
			})
		}
		c.Next()
	}, handler.PreparePreviewSource)
	return router
}

func TestPreviewSourcePreparationRejectsInvalidInputBeforeSourceAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	endpoint := "https://xirang.example/api/v1/recovery-points/" + strings.Repeat("a", 32) + "/entries/" + strings.Repeat("b", 64) + "/preview-source"
	for _, test := range []struct {
		name        string
		body        string
		query       string
		session     bool
		sessionUser uint
		want        int
	}{
		{name: "untrusted locator", body: `{"schema_version":1,"locator":"/private/source"}`, session: true, sessionUser: 42, want: http.StatusBadRequest},
		{name: "duplicate schema", body: `{"schema_version":1,"schema_version":1}`, session: true, sessionUser: 42, want: http.StatusBadRequest},
		{name: "unsupported schema", body: `{"schema_version":2}`, session: true, sessionUser: 42, want: http.StatusBadRequest},
		{name: "query override", body: `{"schema_version":1}`, query: "?entry=other", session: true, sessionUser: 42, want: http.StatusBadRequest},
		{name: "missing session", body: `{"schema_version":1}`, want: http.StatusUnauthorized},
		{name: "wrong session owner", body: `{"schema_version":1}`, session: true, sessionUser: 9, want: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			preparer := previewSourcePreparationFunc(func(context.Context, content.DeliveryActor, backupasset.AssetRef) (catalog.StatusDTO, error) {
				t.Fatal("invalid request reached the source preparation boundary")
				return catalog.StatusDTO{}, nil
			})
			router := previewSourceHandlerRouter(preparer, test.session, test.sessionUser)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, endpoint+test.query, strings.NewReader(test.body)))
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if len(response.Result().Cookies()) != 0 {
				t.Fatal("source preparation issued a content capability cookie")
			}
		})
	}
}

func TestPreviewSourcePreparationPendingIsNotFileNotFoundOrATicket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	preparer := previewSourcePreparationFunc(func(ctx context.Context, _ content.DeliveryActor, _ backupasset.AssetRef) (catalog.StatusDTO, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
			t.Fatal("source preparation is not bounded by the handler budget")
		}
		return catalog.StatusDTO{
			Coverage:            catalog.CoverageDTO{Status: catalog.CoverageBuilding},
			Staleness:           catalog.StalenessDTO{Status: catalog.StalenessUnknown},
			ContentAvailability: catalog.ContentAvailabilityDTO{Available: true},
			Permissions:         catalog.PermissionsDTO{List: true},
		}, nil
	})
	router := previewSourceHandlerRouter(preparer, true, 42)
	endpoint := "https://xirang.example/api/v1/recovery-points/" + strings.Repeat("a", 32) + "/entries/" + strings.Repeat("b", 64) + "/preview-source"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"schema_version":1}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("pending source must remain a successful readiness operation: status=%d body=%s", response.Code, response.Body.String())
	}
	if len(response.Result().Cookies()) != 0 || strings.Contains(response.Body.String(), "content_url") {
		t.Fatalf("preparation minted a content capability: headers=%v body=%s", response.Header(), response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("readiness response may be cached across source changes")
	}
}
