package handlers

import (
	"encoding/json"
	"testing"

	"github.com/gin-gonic/gin"
	"xirang/backend/internal/model"
)

func TestConfigImportNameReferenceProtocol(t *testing.T) {
	for _, tc := range []struct {
		name      string
		data      map[string]interface{}
		code      string
		present   bool
		nameValue string
	}{
		{"missing", map[string]interface{}{}, "", false, ""},
		{"empty", map[string]interface{}{"ssh_key_name": ""}, "", true, ""},
		{"trimmed comma", map[string]interface{}{"ssh_key_name": " Key,One ", "ssh_key_id": float64(17)}, "", true, "Key,One"},
		{"null", map[string]interface{}{"ssh_key_name": nil}, configImportWarningUnresolvedSSHKey, true, ""},
		{"invalid", map[string]interface{}{"ssh_key_name": float64(3)}, configImportWarningInvalidReference, true, ""},
		{"conflict", map[string]interface{}{"ssh_key_name": "", "ssh_key_id": float64(17)}, configImportWarningReferenceConflict, true, ""},
	} {
		t.Run("node/"+tc.name, func(t *testing.T) {
			got, code := parseImportedNodeSSHKeyName(tc.data)
			if code != tc.code || got.present != tc.present || got.name != tc.nameValue {
				t.Fatalf("candidate=%+v code=%q; want present=%v name=%q code=%q", got, code, tc.present, tc.nameValue, tc.code)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		data    map[string]interface{}
		code    string
		present bool
		want    []string
	}{
		{"missing", map[string]interface{}{}, "", false, nil},
		{"empty", map[string]interface{}{"allowed_node_names": []interface{}{}}, "", true, []string{}},
		{"commas and case", map[string]interface{}{"allowed_node_names": []interface{}{" Node,A ", "node,a"}, "allowed_node_ids": "900,901"}, "", true, []string{"Node,A", "node,a"}},
		{"null", map[string]interface{}{"allowed_node_names": nil}, configImportWarningUnresolvedNodeScope, true, nil},
		{"wrong type", map[string]interface{}{"allowed_node_names": "a,b"}, configImportWarningInvalidReference, true, nil},
		{"blank member", map[string]interface{}{"allowed_node_names": []interface{}{"a", " "}}, configImportWarningInvalidReference, true, nil},
		{"nonstring member", map[string]interface{}{"allowed_node_names": []interface{}{"a", float64(7)}}, configImportWarningInvalidReference, true, nil},
		{"duplicate member", map[string]interface{}{"allowed_node_names": []interface{}{"a", " a "}}, configImportWarningInvalidReference, true, nil},
		{"conflict", map[string]interface{}{"allowed_node_names": []interface{}{}, "allowed_node_ids": "7"}, configImportWarningReferenceConflict, true, []string{}},
	} {
		t.Run("scope/"+tc.name, func(t *testing.T) {
			got, code := parseImportedSSHKeyAllowedNodeNames(tc.data)
			if code != tc.code || got.present != tc.present || len(got.names) != len(tc.want) {
				t.Fatalf("candidate=%+v code=%q; want present=%v names=%v code=%q", got, code, tc.present, tc.want, tc.code)
			}
			for i := range tc.want {
				if got.names[i] != tc.want[i] {
					t.Fatalf("names=%v want=%v", got.names, tc.want)
				}
			}
		})
	}
	duplicates := configImportDuplicateNameIndexes([]map[string]interface{}{{"name": " A "}, {"name": "A"}, {"name": "a"}, {"name": ""}, {"name": ""}})
	if len(duplicates) != 2 || !duplicates[0] || !duplicates[1] {
		t.Fatalf("duplicate group=%v", duplicates)
	}
}

func TestConfigImportNameWarningDeduplication(t *testing.T) {
	a := newConfigImportAccumulator()
	for i := range 102 {
		a.warning(configImportEntityNodes, i, "node", configImportWarningInvalidReference)
		a.warning(configImportEntityNodes, i, "node", configImportWarningInvalidReference)
	}
	a.warning(configImportEntityNodes, 0, "node", configImportWarningUnresolvedSSHKey)
	a.warning(configImportEntityNodes, 0, "node", configImportWarningUnresolvedSSHKey)
	result := a.finalize()
	if len(result.Warnings) != 100 || result.WarningsTruncated != 3 {
		t.Fatalf("warnings=%d truncated=%d", len(result.Warnings), result.WarningsTruncated)
	}
}

func TestConfigExportNameReferenceBoundaries(t *testing.T) {
	keyID, missingID, lossyID := uint(9), uint(99), uint(10)
	nodes := []model.Node{
		{ID: 7, Name: "Node,One", SSHKeyID: &keyID},
		{ID: 8, Name: "Node Two"},
		{ID: 11, Name: "Missing", SSHKeyID: &missingID},
		{ID: 12, Name: "Lossy", SSHKeyID: &lossyID},
		{ID: 13, Name: " unrepresentable "},
	}
	keys := []model.SSHKey{
		{ID: keyID, Name: "Key,One", AllowedNodeIDs: "8,7"},
		{ID: lossyID, Name: " padded "},
		{ID: 14, Name: "Dangling", AllowedNodeIDs: "7,99"},
		{ID: 15, Name: "Invalid", AllowedNodeIDs: "bad"},
		{ID: 16, Name: "Lossy Scope", AllowedNodeIDs: "13"},
	}
	exportNodes := make([]gin.H, len(nodes))
	for i := range nodes {
		exportNodes[i] = gin.H{"ssh_key_id": nodes[i].SSHKeyID}
	}
	exportKeys := make([]gin.H, len(keys))
	for i := range keys {
		exportKeys[i] = gin.H{"allowed_node_ids": keys[i].AllowedNodeIDs}
	}
	addConfigExportNameReferences(nodes, keys, exportNodes, exportKeys)
	for i, want := range []interface{}{"Key,One", "", nil, nil, ""} {
		if got, present := exportNodes[i]["ssh_key_name"]; !present || got != want {
			t.Fatalf("node %d reference=%v present=%v want=%v", i, got, present, want)
		}
		if exportNodes[i]["ssh_key_id"] != nodes[i].SSHKeyID {
			t.Fatal("legacy node ID changed")
		}
	}
	for i, want := range []string{`["Node Two","Node,One"]`, `[]`, `null`, `null`, `null`} {
		got, err := json.Marshal(exportKeys[i]["allowed_node_names"])
		if err != nil || string(got) != want {
			t.Fatalf("key %d names=%s error=%v want=%s", i, got, err, want)
		}
		if exportKeys[i]["allowed_node_ids"] != keys[i].AllowedNodeIDs {
			t.Fatal("legacy node scope changed")
		}
	}
}
