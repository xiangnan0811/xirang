package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"xirang/backend/internal/model"
	nodepkg "xirang/backend/internal/node"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
)

func TestNodeStatusWriteValidation(t *testing.T) {
	key := sha256.Sum256([]byte("FAKE_NODE_STATUS_ENCRYPTION_KEY_FOR_TEST_ONLY"))
	t.Setenv("DATA_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key[:]))
	secure.ResetForTesting()
	t.Cleanup(secure.ResetForTesting)

	const legacyStatus = "Historical Invalid Status"
	const validationMessage = "status must be online, warning, or offline"
	cases := []struct {
		name       string
		include    bool
		status     any
		wantCreate string
		wantUpdate string
		invalid    bool
	}{
		{name: "online", include: true, status: "online", wantCreate: "online", wantUpdate: "online"},
		{name: "warning", include: true, status: "warning", wantCreate: "warning", wantUpdate: "warning"},
		{name: "offline", include: true, status: "offline", wantCreate: "offline", wantUpdate: "offline"},
		{name: "omitted", wantCreate: "offline", wantUpdate: legacyStatus},
		{name: "null", include: true, status: nil, wantCreate: "offline", wantUpdate: legacyStatus},
		{name: "empty", include: true, status: "", wantCreate: "offline", wantUpdate: legacyStatus},
		{name: "uppercase", include: true, status: "ONLINE", invalid: true},
		{name: "mixed case", include: true, status: "Warning", invalid: true},
		{name: "space", include: true, status: " ", invalid: true},
		{name: "unicode whitespace", include: true, status: "\t\u3000", invalid: true},
		{name: "leading padding", include: true, status: " online", invalid: true},
		{name: "trailing padding", include: true, status: "offline ", invalid: true},
		{name: "arbitrary", include: true, status: "maintenance-secret-value", invalid: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openNodeHandlerTestDB(t)
			if err := db.AutoMigrate(&model.Node{}, &model.SSHKey{}); err != nil {
				t.Fatalf("migrate node tables: %v", err)
			}
			handler := NewNodeHandler(db, nil, nodepkg.NewNodeService(gormrepo.NewNodeRepository(db)))
			router := gin.New()
			router.POST("/nodes", handler.Create)
			router.PUT("/nodes/:id", handler.Update)

			payload := map[string]any{
				"name": "node-create", "host": "192.0.2.20", "port": 2222,
				"username": "new-user", "auth_type": "password", "password": "FAKE_NODE_PASSWORD_FOR_TEST_ONLY",
				"tags": "changed",
			}
			if tc.include {
				payload["status"] = tc.status
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			request := func(method, path string) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(method, path, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				resp := httptest.NewRecorder()
				router.ServeHTTP(resp, req)
				return resp
			}
			assertInvalid := func(resp *httptest.ResponseRecorder) {
				t.Helper()
				if resp.Code != http.StatusBadRequest {
					t.Fatalf("HTTP status=%d, want 400; body=%s", resp.Code, resp.Body.String())
				}
				var envelope Response
				if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("decode error envelope: %v", err)
				}
				if envelope.Code != http.StatusBadRequest || envelope.Message != validationMessage || envelope.Data != nil {
					t.Fatalf("unexpected error envelope: %+v", envelope)
				}
			}

			missing := request(http.MethodPut, "/nodes/999999")
			if tc.invalid {
				assertInvalid(missing)
			} else if missing.Code != http.StatusNotFound {
				t.Fatalf("missing node status=%d, want 404; body=%s", missing.Code, missing.Body.String())
			}

			create := request(http.MethodPost, "/nodes")
			if tc.invalid {
				assertInvalid(create)
				var count int64
				if err := db.Model(&model.Node{}).Count(&count).Error; err != nil {
					t.Fatalf("count nodes: %v", err)
				}
				if count != 0 {
					t.Fatalf("invalid create persisted %d nodes", count)
				}
			} else {
				if create.Code != http.StatusCreated {
					t.Fatalf("create status=%d, want 201; body=%s", create.Code, create.Body.String())
				}
				var envelope struct {
					Code int        `json:"code"`
					Data model.Node `json:"data"`
				}
				if err := json.Unmarshal(create.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("decode create response: %v", err)
				}
				if envelope.Code != http.StatusCreated || envelope.Data.ID == 0 || envelope.Data.Status != tc.wantCreate {
					t.Fatalf("unexpected create response: %+v", envelope)
				}
				var stored model.Node
				if err := db.First(&stored, envelope.Data.ID).Error; err != nil {
					t.Fatalf("load created node: %v", err)
				}
				if stored.Status != tc.wantCreate {
					t.Fatalf("created status=%q, want %q", stored.Status, tc.wantCreate)
				}
			}

			legacy := model.Node{
				Name: "node-before", Host: "192.0.2.10", Port: 22, Username: "old-user",
				AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY", BackupDir: "node-before",
				Status: legacyStatus, Tags: "original",
			}
			payload["name"] = "node-after"
			body, err = json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal update request: %v", err)
			}

			if err := db.Create(&legacy).Error; err != nil {
				t.Fatalf("create historical node: %v", err)
			}
			var before model.Node
			if err := db.First(&before, legacy.ID).Error; err != nil {
				t.Fatalf("load historical node: %v", err)
			}
			update := request(http.MethodPut, fmt.Sprintf("/nodes/%d", legacy.ID))
			var after model.Node
			if err := db.First(&after, legacy.ID).Error; err != nil {
				t.Fatalf("load updated node: %v", err)
			}
			if tc.invalid {
				assertInvalid(update)
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("invalid update mutated node: before=%+v after=%+v", before, after)
				}
				return
			}
			if update.Code != http.StatusOK {
				t.Fatalf("update status=%d, want 200; body=%s", update.Code, update.Body.String())
			}
			var envelope struct {
				Code int `json:"code"`
				Data struct {
					Node model.Node `json:"node"`
				} `json:"data"`
			}
			if err := json.Unmarshal(update.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode update response: %v", err)
			}
			if envelope.Code != http.StatusOK || envelope.Data.Node.ID != legacy.ID || envelope.Data.Node.Status != tc.wantUpdate {
				t.Fatalf("unexpected update response: %+v", envelope)
			}
			if after.Status != tc.wantUpdate || after.Name != "node-after" || after.Host != "192.0.2.20" || after.Tags != "changed" {
				t.Fatalf("unexpected updated node: %+v", after)
			}
		})
	}
}
