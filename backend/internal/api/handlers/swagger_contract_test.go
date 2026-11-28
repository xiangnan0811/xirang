package handlers

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"xirang/backend/internal/api/docs"
)

type generatedSwaggerContractSchema struct {
	Ref        string                                    `json:"$ref"`
	Type       string                                    `json:"type"`
	Format     string                                    `json:"format"`
	Required   []string                                  `json:"required"`
	Properties map[string]generatedSwaggerContractSchema `json:"properties"`
	AllOf      []generatedSwaggerContractSchema          `json:"allOf"`
	Nullable   bool                                      `json:"x-nullable"`
}

type generatedSwaggerContractDocument struct {
	Paths map[string]map[string]struct {
		Description string `json:"description"`
		Responses   map[string]struct {
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
