package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestServiceMonitorListFailureReturnsSingleErrorEnvelope(t *testing.T) {
	db := openServiceMonitorTestDB(t)
	const callback = "test:monitor-list-query-failure"
	if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) { _ = tx.AddError(errors.New("injected monitor query failure")) }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	router := gin.New()
	router.GET("/service-monitors", NewServiceMonitorHandler(db, nil).List)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/service-monitors", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", response.Code)
	}
	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("not a single JSON error response: %v", err)
	}
	if body.Code != http.StatusInternalServerError {
		t.Fatalf("error code=%d", body.Code)
	}
}
