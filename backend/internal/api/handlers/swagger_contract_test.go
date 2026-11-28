package handlers

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"xirang/backend/internal/api/docs"
)

type generatedSwaggerContractSchema struct {
	Ref         string                                    `json:"$ref"`
	Type        string                                    `json:"type"`
	Format      string                                    `json:"format"`
	Description string                                    `json:"description"`
	Enum        []string                                  `json:"enum"`
	Required    []string                                  `json:"required"`
	Properties  map[string]generatedSwaggerContractSchema `json:"properties"`
	AllOf       []generatedSwaggerContractSchema          `json:"allOf"`
	Nullable    bool                                      `json:"x-nullable"`
}

type generatedSwaggerContractDocument struct {
	Paths map[string]map[string]struct {
		Description string `json:"description"`
		Parameters  []struct {
			Name     string `json:"name"`
			In       string `json:"in"`
			Type     string `json:"type"`
			Required bool   `json:"required"`
		} `json:"parameters"`
		Responses map[string]struct {
			Schema generatedSwaggerContractSchema `json:"schema"`
		} `json:"responses"`
	} `json:"paths"`
	Definitions map[string]generatedSwaggerContractSchema `json:"definitions"`
}

func TestGeneratedSwaggerPublishesNodeUpdateEnvelopeAndDrillUnavailable(t *testing.T) {
	var document generatedSwaggerContractDocument
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("decode generated Swagger: %v", err)
	}

	t.Run("node update envelope", func(t *testing.T) {
		nodeUpdate, ok := document.Paths["/nodes/{id}"]["put"]
		if !ok {
			t.Fatal("generated Swagger missing PUT /nodes/{id}")
		}
		nodeUpdateOK, ok := nodeUpdate.Responses["200"]
		if !ok {
			t.Fatal("generated Swagger missing PUT /nodes/{id} 200 response")
		}
		wantNodeUpdateRef := "#/definitions/internal_api_handlers.nodeUpdateResponse"
		if got := generatedSwaggerDataRef(nodeUpdateOK.Schema); got != wantNodeUpdateRef {
			t.Fatalf("generated Swagger PUT /nodes/{id} 200 data ref=%q, want %q", got, wantNodeUpdateRef)
		}
		nodeUpdateSchema, ok := document.Definitions["internal_api_handlers.nodeUpdateResponse"]
		if !ok {
			t.Fatal("generated Swagger missing internal_api_handlers.nodeUpdateResponse definition")
		}
		node, ok := nodeUpdateSchema.Properties["node"]
		if !ok {
			t.Error("generated nodeUpdateResponse missing node property")
		} else if node.Ref != "#/definitions/xirang_backend_internal_model.Node" {
			t.Errorf("generated nodeUpdateResponse node ref=%q, want model.Node", node.Ref)
		}
		warning, ok := nodeUpdateSchema.Properties["warning"]
		if !ok {
			t.Error("generated nodeUpdateResponse missing optional warning property")
		} else if warning.Type != "string" {
			t.Errorf("generated nodeUpdateResponse warning type=%q, want string", warning.Type)
		}
		if slices.Contains(nodeUpdateSchema.Required, "warning") {
			t.Errorf("generated nodeUpdateResponse warning must remain optional: required=%v", nodeUpdateSchema.Required)
		}
	})

	t.Run("drill unavailable", func(t *testing.T) {
		drillTrigger, ok := document.Paths["/policies/{id}/drill-trigger"]["post"]
		if !ok {
			t.Fatal("generated Swagger missing POST /policies/{id}/drill-trigger")
		}
		if _, ok := drillTrigger.Responses["503"]; !ok {
			t.Error("generated Swagger POST /policies/{id}/drill-trigger missing 503 response")
		}
	})
}

func TestGeneratedSwaggerPublishesNodeTimestampWrites(t *testing.T) {
	var document generatedSwaggerContractDocument
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("decode generated Swagger: %v", err)
	}
	schema, ok := document.Definitions["internal_api_handlers.nodeRequest"]
	if !ok {
		t.Fatal("generated Swagger missing nodeRequest")
	}
	for _, field := range []string{"maintenance_start", "maintenance_end", "expiry_date"} {
		property, ok := schema.Properties[field]
		if !ok || property.Type != "string" || !property.Nullable || property.Format != "" || slices.Contains(schema.Required, field) {
			t.Errorf("%s must be an optional nullable string without a format constraint: %+v", field, property)
		}
	}
	for _, operation := range []struct{ path, method string }{{"/nodes", "post"}, {"/nodes/{id}", "put"}} {
		entry, ok := document.Paths[operation.path][operation.method]
		if !ok {
			t.Errorf("missing %s %s", operation.method, operation.path)
			continue
		}
		for _, term := range []string{"maintenance_start", "maintenance_end", "expiry_date", "null", "空字符串", "RFC3339", "同时", "晚于", "微秒", "400"} {
			if !strings.Contains(entry.Description, term) {
				t.Errorf("%s %s description missing %q", operation.method, operation.path, term)
			}
		}
		if _, ok := entry.Responses["400"]; !ok {
			t.Errorf("%s %s missing 400 response", operation.method, operation.path)
		}
	}
}

func TestGeneratedSwaggerPublishesNodeStatusWrites(t *testing.T) {
	var document generatedSwaggerContractDocument
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("decode generated Swagger: %v", err)
	}
	schema, ok := document.Definitions["internal_api_handlers.nodeRequest"]
	if !ok {
		t.Fatal("generated Swagger missing nodeRequest")
	}
	status, ok := schema.Properties["status"]
	if !ok || status.Type != "string" || !status.Nullable || slices.Contains(schema.Required, "status") || len(status.Enum) != 0 {
		t.Fatalf("status must be an optional nullable string without a strict enum: %+v", status)
	}
	for _, term := range []string{"online", "warning", "offline", "nonempty", "400", "Omitted", "null", "empty", "create", "update", "preserve the stored status"} {
		if !strings.Contains(status.Description, term) {
			t.Errorf("status description missing %q: %q", term, status.Description)
		}
	}
}

func TestGeneratedSwaggerPublishesNodeBasePathWrites(t *testing.T) {
	var document generatedSwaggerContractDocument
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("decode generated Swagger: %v", err)
	}
	schema := document.Definitions["internal_api_handlers.nodeRequest"]
	field, ok := schema.Properties["base_path"]
	if !ok || field.Type != "string" || !field.Nullable || slices.Contains(schema.Required, "base_path") {
		t.Fatalf("base_path must be an optional nullable string: %+v", field)
	}
}

func TestGeneratedSwaggerPublishesNodeRevisionAndProbeEvidence(t *testing.T) {
	var document generatedSwaggerContractDocument
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("decode generated Swagger: %v", err)
	}
	request := document.Definitions["internal_api_handlers.nodeRequest"]
	revision, ok := request.Properties["expected_revision"]
	if !ok || revision.Type != "string" || !revision.Nullable || slices.Contains(request.Required, "expected_revision") {
		t.Errorf("expected_revision must be an optional nullable string: %+v", revision)
	}
	if _, ok := document.Paths["/nodes/{id}"]["put"].Responses["409"]; !ok {
		t.Error("PUT /nodes/{id} missing 409 response")
	}
	probe := document.Paths["/nodes/{id}/test-connection"]["post"]
	found := false
	for _, parameter := range probe.Parameters {
		if parameter.Name == "expected_revision" && parameter.In == "query" && parameter.Type == "string" && !parameter.Required {
			found = true
		}
	}
	if !found {
		t.Error("POST test-connection missing optional expected_revision query parameter")
	}
	if _, ok := probe.Responses["400"]; !ok {
		t.Error("POST test-connection missing 400 response")
	}
	node := document.Definitions["xirang_backend_internal_model.Node"]
	if property, ok := node.Properties["revision"]; !ok || property.Type != "string" {
		t.Errorf("node revision missing or invalid: %+v", property)
	}
	if property, ok := node.Properties["connection_test"]; !ok || property.Ref != "#/definitions/xirang_backend_internal_model.NodeConnectionTest" {
		t.Errorf("node connection_test missing or invalid: %+v", property)
	}
	evidence := document.Definitions["xirang_backend_internal_model.NodeConnectionTest"]
	if evidence.Properties["status"].Type != "string" || evidence.Properties["current"].Type != "boolean" {
		t.Errorf("invalid connection test schema: %+v", evidence)
	}
	detail := document.Definitions["internal_api_handlers.nodeGetResponse"]
	if detail.Properties["probe_token"].Type != "string" || detail.Properties["revision"].Type != "string" {
		t.Errorf("node detail must include probe_token and revision: %+v", detail)
	}
}

func TestGeneratedSwaggerPublishesOverviewResponseCoverage(t *testing.T) {
	var document generatedSwaggerContractDocument
	if err := json.Unmarshal([]byte(docs.SwaggerInfo.ReadDoc()), &document); err != nil {
		t.Fatalf("decode generated Swagger: %v", err)
	}
	for _, test := range []struct {
		path       string
		definition string
	}{
		{"/overview/backup-health", "backupHealthData"},
		{"/overview/storage-usage", "storageUsageResponse"},
		{"/overview/traffic", "overviewTrafficResponse"},
	} {
		want := "#/definitions/internal_api_handlers." + test.definition
		if got := generatedSwaggerDataRef(document.Paths[test.path]["get"].Responses["200"].Schema); got != want {
			t.Errorf("GET %s schema data ref=%q, want %q", test.path, got, want)
		}
	}
	property := func(definition, name, wantType string) generatedSwaggerContractSchema {
		t.Helper()
		item := document.Definitions["internal_api_handlers."+definition].Properties[name]
		if item.Type != wantType {
			t.Errorf("%s.%s type=%q, want %q", definition, name, item.Type, wantType)
		}
		return item
	}
	if !property("backupHealthData", "latest_verified_backup_at", "string").Nullable ||
		!property("backupHealthStaleNode", "last_backup_at", "string").Nullable {
		t.Error("backup health timestamp fields must allow null")
	}
	property("backupHealthData", "stale_threshold_hours", "integer")
	property("storageUsageResponse", "mount_points", "array")
	property("storageUsageResponse", "per_node", "array")
	for _, field := range []struct{ definition, name string }{
		{"storageUsageResponse", "incomplete"},
		{"storageUsageResponse", "mount_points_incomplete"},
		{"perNodeUsage", "incomplete"},
		{"overviewTrafficPoint", "incomplete"},
	} {
		property(field.definition, field.name, "boolean")
		if slices.Contains(document.Definitions["internal_api_handlers."+field.definition].Required, field.name) {
			t.Errorf("%s.%s must remain optional", field.definition, field.name)
		}
	}
}

func generatedSwaggerDataRef(schema generatedSwaggerContractSchema) string {
	for _, item := range schema.AllOf {
		if data, ok := item.Properties["data"]; ok {
			return data.Ref
		}
	}
	return ""
}
