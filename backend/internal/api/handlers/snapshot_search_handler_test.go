package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSnapshotSearchHandlerReturnsRetiredResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewSnapshotSearchHandler()
	router.GET("/tasks/:id/snapshots/search", handler.Search)

	const privateQuery = "FAKE_PRIVATE_SEARCH_TERM_FOR_TEST_ONLY"
	const privateTaskID = "FAKE_PRIVATE_TASK_ID_FOR_TEST_ONLY"
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "empty query", path: "/tasks/1/snapshots/search?q="},
		{name: "missing query", path: "/tasks/1/snapshots/search"},
		{name: "arbitrary task ID", path: "/tasks/99999/snapshots/search?q=test"},
		{name: "invalid task ID", path: "/tasks/abc/snapshots/search?q=test"},
		{name: "private input", path: "/tasks/" + privateTaskID + "/snapshots/search?q=" + privateQuery},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusGone {
				t.Fatalf("retired search status=%d body=%s", response.Code, response.Body.String())
			}

			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode retired search response: %v", err)
			}
			if len(envelope) != 3 || string(envelope["code"]) != "410" || string(envelope["data"]) != "null" {
				t.Fatalf("unexpected retired search envelope: %s", response.Body.String())
			}
			if strings.Contains(response.Body.String(), privateQuery) || strings.Contains(response.Body.String(), privateTaskID) {
				t.Fatal("retired search echoed private request data")
			}
		})
	}
}
