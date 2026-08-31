package handlers

import (
	"encoding/json"
	"slices"
	"testing"

	"xirang/backend/internal/api/docs"
)

type generatedSwaggerContractSchema struct {
	Ref        string                                    `json:"$ref"`
	Type       string                                    `json:"type"`
	Required   []string                                  `json:"required"`
	Properties map[string]generatedSwaggerContractSchema `json:"properties"`
	AllOf      []generatedSwaggerContractSchema          `json:"allOf"`
}

type generatedSwaggerContractDocument struct {
	Paths map[string]map[string]struct {
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

func generatedSwaggerDataRef(schema generatedSwaggerContractSchema) string {
	for _, item := range schema.AllOf {
		if data, ok := item.Properties["data"]; ok {
			return data.Ref
		}
	}
	return ""
}
