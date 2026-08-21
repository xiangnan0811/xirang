package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset/catalog"
	assetsearch "xirang/backend/internal/backupasset/search"

	"github.com/gin-gonic/gin"
)

func TestRequestedTrueFeatureLiveFalseClosesHandlerHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pointID := strings.Repeat("3", 32)
	requestedTrueLiveFalse := func() (BackupAssetHandlerConfig, error) {
		return BackupAssetHandlerConfig{
			Enabled:                false,
			QueryLimits:            assetsearch.DefaultQueryLimits(),
			IdempotencyKeyMaxBytes: 128,
		}, nil
	}

	searchSpy := &backupAssetSearchServiceSpy{}
	searchHandler := NewBackupAssetSearchHandler(searchSpy, &backupAssetSavedSearchUseSpy{}, &backupAssetAuditSpy{}, requestedTrueLiveFalse, nil)
	searchRouter := backupAssetHandlerTestRouterWithRole("admin")
	searchRouter.POST("/asset-search", searchHandler.Search)
	searchBody := `{"query":{"schema_version":1,"root":{"op":"term","field":"name","text":"safe"},"scope":{"mode":"exact_points","recovery_point_ids":["` + pointID + `"]},"sort":"relevance","limit":10}}`
	searchRec := httptest.NewRecorder()
	searchReq := httptest.NewRequest(http.MethodPost, "/asset-search", strings.NewReader(searchBody))
	searchReq.Header.Set("Content-Type", "application/json")
	searchRouter.ServeHTTP(searchRec, searchReq)
	if searchRec.Code != http.StatusServiceUnavailable || searchSpy.calls != 0 {
		t.Fatalf("search status=%d calls=%d body=%s", searchRec.Code, searchSpy.calls, searchRec.Body.String())
	}

	overlaySpy := &backupAssetOverlayServiceSpy{}
	overlayHandler := NewBackupAssetOverlayHandler(overlaySpy, &backupAssetAuditSpy{}, requestedTrueLiveFalse)
	overlayRouter := backupAssetHandlerTestRouterWithRole("admin")
	overlayRouter.GET("/asset-saved-searches", overlayHandler.ListSavedSearches)
	overlayRec := httptest.NewRecorder()
	overlayRouter.ServeHTTP(overlayRec, httptest.NewRequest(http.MethodGet, "/asset-saved-searches", nil))
	if overlayRec.Code != http.StatusServiceUnavailable || overlaySpy.calls != 0 {
		t.Fatalf("overlay status=%d calls=%d body=%s", overlayRec.Code, overlaySpy.calls, overlayRec.Body.String())
	}

	catalogSpy := &backupAssetServiceSpy{err: catalog.ErrFeatureDisabled}
	catalogHandler := NewBackupAssetHandler(catalogSpy, nil)
	catalogRouter := backupAssetHandlerTestRouterWithRole("admin")
	catalogRouter.GET("/recovery-points/:id/entries", catalogHandler.ListEntries)
	catalogRec := httptest.NewRecorder()
	catalogRouter.ServeHTTP(catalogRec, httptest.NewRequest(http.MethodGet, "/recovery-points/"+pointID+"/entries", nil))
	if catalogRec.Code != http.StatusServiceUnavailable || catalogSpy.calls == 0 {
		t.Fatalf("catalog status=%d calls=%d body=%s", catalogRec.Code, catalogSpy.calls, catalogRec.Body.String())
	}
}
