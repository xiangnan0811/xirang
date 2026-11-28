package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"xirang/backend/internal/model"
	nodepkg "xirang/backend/internal/node"
	gormrepo "xirang/backend/internal/repository/gorm"
)

func TestNodeBasePathHTTP(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	migrateP1ConfigImport(t, db)
	handler := NewNodeHandler(db, nil, nodepkg.NewNodeService(gormrepo.NewNodeRepository(db)))
	router := gin.New()
	router.POST("/nodes", handler.Create)
	router.PUT("/nodes/:id", handler.Update)
	request := func(method, path, name string, fields map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		body := map[string]any{"name": name, "host": "192.0.2.10", "username": "root", "auth_type": "password", "password": "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
		for key, value := range fields {
			body[key] = value
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, req)
		return response
	}
	load := func(id uint) model.Node {
		t.Helper()
		var stored model.Node
		if err := db.First(&stored, id).Error; err != nil {
			t.Fatal(err)
		}
		return stored
	}
	for i, value := range []string{"", "/", "/data//./.hidden/", "/data/..archive/internal space/"} {
		name := fmt.Sprintf("base-valid-%d", i)
		response := request(http.MethodPost, "/nodes", name, map[string]any{"base_path": value})
		if response.Code != http.StatusCreated {
			t.Fatalf("create %q: status=%d body=%s", value, response.Code, response.Body.String())
		}
		var envelope struct {
			Data model.Node `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if got := load(envelope.Data.ID).BasePath; got != value {
			t.Fatalf("created base_path=%q want %q", got, value)
		}
	}
	name := "base-update"
	response := request(http.MethodPost, "/nodes", name, map[string]any{"base_path": "/old"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create update fixture: %s", response.Body.String())
	}
	var envelope struct {
		Data model.Node `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	id := envelope.Data.ID
	updateURL := fmt.Sprintf("/nodes/%d", id)
	for _, invalid := range []any{"relative", "/data/../other", " /data", "/data\u00a0", "/data/\x00x", 123} {
		for _, target := range []struct{ method, path string }{{http.MethodPost, "/nodes"}, {http.MethodPut, updateURL}} {
			response := request(target.method, target.path, name, map[string]any{"base_path": invalid})
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s invalid %v: status=%d body=%s", target.method, invalid, response.Code, response.Body.String())
			}
			if got := load(id).BasePath; got != "/old" {
				t.Fatalf("rejected update changed base_path to %q", got)
			}
		}
	}
	const legacy = " /historical/../root "
	if err := db.Model(&model.Node{}).Where("id = ?", id).UpdateColumn("base_path", legacy).Error; err != nil {
		t.Fatal(err)
	}
	for _, fields := range []map[string]any{{}, {"base_path": nil}, {"base_path": ""}, {"base_path": legacy}} {
		response := request(http.MethodPut, updateURL, name, fields)
		if response.Code != http.StatusOK || load(id).BasePath != legacy {
			t.Fatalf("legacy replay %v: status=%d body=%s stored=%q", fields, response.Code, response.Body.String(), load(id).BasePath)
		}
	}
	const changed = "/new//./..archive/"
	response = request(http.MethodPut, updateURL, name, map[string]any{"base_path": changed})
	if response.Code != http.StatusOK || load(id).BasePath != changed {
		t.Fatalf("valid change: status=%d body=%s stored=%q", response.Code, response.Body.String(), load(id).BasePath)
	}
}

func TestConfigImportNodeBasePathPreviewApplyAndExport(t *testing.T) {
	setConfigHandlerTestEncryption(t)
	db := openConfigHandlerTestDB(t)
	migrateConfigAssetGraphDB(t, db)
	const legacy = " /old/../legacy "
	existing := model.Node{Name: "import-base", Host: "192.0.2.11", Port: 22, Username: "root", AuthType: "key", BackupDir: "import-base", BasePath: legacy}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		value    any
		present  bool
		rejected bool
		want     string
	}{
		{"omitted", nil, false, false, legacy},
		{"null", nil, true, false, legacy},
		{"clear", "", true, false, ""},
		{"valid", "/data//./..archive/internal space/", true, false, "/data//./..archive/internal space/"},
		{"root", "/", true, false, "/"},
		{"legacy echo", legacy, true, true, legacy},
		{"relative", "relative", true, true, legacy},
		{"traversal", "/data/../other", true, true, legacy},
		{"edge whitespace", "/data\u00a0", true, true, legacy},
		{"nul", "/data/\x00x", true, true, legacy},
		{"number", 42, true, true, legacy},
		{"object", map[string]any{}, true, true, legacy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.Model(&model.Node{}).Where("id = ?", existing.ID).UpdateColumn("base_path", legacy).Error; err != nil {
				t.Fatal(err)
			}
			node := map[string]any{"name": existing.Name, "host": existing.Host, "username": existing.Username, "auth_type": "key"}
			if tc.present {
				node["base_path"] = tc.value
			}
			body := map[string]any{"nodes": []any{node}}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			preview := serveConfigImportPreviewHTTP(t, db, nil, "overwrite", encoded, nil)
			if preview.Code != http.StatusOK {
				t.Fatalf("preview status=%d body=%s", preview.Code, preview.Body.String())
			}
			entity := previewHTTPEntity(t, decodeConfigImportPreviewHTTP(t, preview), "nodes")
			if entity.Input != 1 || (tc.rejected && (entity.Rejected != 1 || entity.Updated != 0)) || (!tc.rejected && (entity.Rejected != 0 || entity.Updated != 1)) {
				t.Fatalf("preview entity=%+v", entity)
			}
			var stored model.Node
			if err := db.First(&stored, existing.ID).Error; err != nil || stored.BasePath != legacy {
				t.Fatalf("preview changed stored base_path=%q err=%v", stored.BasePath, err)
			}
			response, result := serveP1ConfigImport(t, db, nil, "overwrite", body)
			if response.Code != http.StatusOK || (tc.rejected && (result.Rejected != 1 || result.Updated != 0)) || (!tc.rejected && (result.Rejected != 0 || result.Updated != 1)) {
				t.Fatalf("apply status=%d result=%+v body=%s", response.Code, result, response.Body.String())
			}
			if err := db.First(&stored, existing.ID).Error; err != nil || stored.BasePath != tc.want {
				t.Fatalf("apply stored base_path=%q want=%q err=%v", stored.BasePath, tc.want, err)
			}
		})
	}
	skipBody := map[string]any{"nodes": []any{map[string]any{
		"name": existing.Name, "host": existing.Host, "username": existing.Username,
		"auth_type": "key", "base_path": legacy,
	}}}
	skipBytes, err := json.Marshal(skipBody)
	if err != nil {
		t.Fatal(err)
	}
	skipPreview := serveConfigImportPreviewHTTP(t, db, nil, "skip", skipBytes, nil)
	if skipPreview.Code != http.StatusOK {
		t.Fatalf("skip preview status=%d body=%s", skipPreview.Code, skipPreview.Body.String())
	}
	skipEntity := previewHTTPEntity(t, decodeConfigImportPreviewHTTP(t, skipPreview), "nodes")
	if skipEntity.Skipped != 1 || skipEntity.Rejected != 0 {
		t.Fatalf("skip preview entity=%+v", skipEntity)
	}
	skipResponse, skipResult := serveP1ConfigImport(t, db, nil, "skip", skipBody)
	if skipResponse.Code != http.StatusOK || skipResult.Skipped != 1 || skipResult.Rejected != 0 {
		t.Fatalf("skip apply status=%d result=%+v body=%s", skipResponse.Code, skipResult, skipResponse.Body.String())
	}
	var afterSkip model.Node
	if err := db.First(&afterSkip, existing.ID).Error; err != nil || afterSkip.BasePath != legacy {
		t.Fatalf("skip changed base_path=%q err=%v", afterSkip.BasePath, err)
	}
	legacyPayload := parseConfigExportPayload(t, serveConfigExport(t, db, false))
	legacyData := legacyPayload["data"].(map[string]any)
	legacyNodes := legacyData["nodes"].([]any)
	if len(legacyNodes) != 1 || legacyNodes[0].(map[string]any)["base_path"] != legacy {
		t.Fatalf("export did not preserve historical path: %v", legacyNodes)
	}
	const raw = "/data//./..archive/internal space/"
	if err := db.Model(&model.Node{}).Where("id = ?", existing.ID).UpdateColumn("base_path", raw).Error; err != nil {
		t.Fatal(err)
	}
	payload := parseConfigExportPayload(t, serveConfigExport(t, db, false))
	data := payload["data"].(map[string]any)
	nodes := data["nodes"].([]any)
	if len(nodes) != 1 || nodes[0].(map[string]any)["base_path"] != raw {
		t.Fatalf("export did not preserve raw path: %v", nodes)
	}
	for i, tc := range []struct {
		present bool
		value   any
		want    string
	}{
		{false, nil, ""},
		{true, nil, ""},
		{true, "", ""},
		{true, raw, raw},
	} {
		name := fmt.Sprintf("import-new-base-%d", i)
		node := map[string]any{"name": name, "host": "192.0.2.12", "username": "root", "auth_type": "key"}
		if tc.present {
			node["base_path"] = tc.value
		}
		body := map[string]any{"nodes": []any{node}}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		preview := serveConfigImportPreviewHTTP(t, db, nil, "overwrite", encoded, nil)
		if preview.Code != http.StatusOK {
			t.Fatalf("new node preview status=%d body=%s", preview.Code, preview.Body.String())
		}
		entity := previewHTTPEntity(t, decodeConfigImportPreviewHTTP(t, preview), "nodes")
		if entity.Created != 1 || entity.Rejected != 0 {
			t.Fatalf("new node preview entity=%+v", entity)
		}
		response, result := serveP1ConfigImport(t, db, nil, "overwrite", body)
		if response.Code != http.StatusOK || result.Created != 1 || result.Rejected != 0 {
			t.Fatalf("new node apply status=%d result=%+v body=%s", response.Code, result, response.Body.String())
		}
		var stored model.Node
		if err := db.Where("name = ?", name).First(&stored).Error; err != nil || stored.BasePath != tc.want {
			t.Fatalf("new node stored base_path=%q want=%q err=%v", stored.BasePath, tc.want, err)
		}
	}
}
