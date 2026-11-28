package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
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

func TestConfigImportRetainedNodeSecretsMigrated(t *testing.T) {
	retryTransactionForEachEngine(t, func(t *testing.T, engine string) {
		setConfigHandlerTestEncryption(t)
		db := openMigratedRetryTransactionDB(t, engine)
		service := settings.NewService(db)

		type storedNode struct {
			host, username, authType, tags, basePath string
			port                                     int
			password, privateKey                     string
			generation, revision                     string
		}
		readStored := func(t *testing.T, id uint) storedNode {
			t.Helper()
			var got storedNode
			err := db.Raw(`SELECT host, username, auth_type, tags, base_path, port, password, private_key, probe_generation, config_revision FROM nodes WHERE id = ?`, id).Row().Scan(
				&got.host, &got.username, &got.authType, &got.tags, &got.basePath, &got.port,
				&got.password, &got.privateKey, &got.generation, &got.revision,
			)
			if err != nil {
				t.Fatalf("read stored node: %v", err)
			}
			return got
		}
		const password = "FAKE_RETAINED_NODE_PASSWORD"
		const privateKey = "FAKE_RETAINED_NODE_INLINE_KEY"
		for _, tc := range []struct {
			name, password, privateKey string
		}{
			{"password", password, ""},
			{"inline_key", "", privateKey},
			{"both", password, privateKey},
		} {
			t.Run(tc.name, func(t *testing.T) {
				node := model.Node{Name: "retained-" + tc.name, Host: "host.example", Port: 22, Username: "root", AuthType: "key", BackupDir: "retained-" + tc.name, Password: tc.password, PrivateKey: tc.privateKey}
				if err := db.Create(&node).Error; err != nil {
					t.Fatal(err)
				}
				before := readStored(t, node.ID)
				if before.generation == "" || before.revision == "" ||
					(tc.password != "" && !strings.HasPrefix(before.password, "enc:v2:")) ||
					(tc.privateKey != "" && !strings.HasPrefix(before.privateKey, "enc:v2:")) {
					t.Fatal("fixture lacks migrated revision or encrypted credentials")
				}
				request := map[string]any{"nodes": []any{map[string]any{
					"name": node.Name, "host": node.Host, "password": "FAKE_IGNORED_PASSWORD", "private_key": "FAKE_IGNORED_KEY",
				}}}
				response, result := serveConfigNameMappingImport(t, db, service, "overwrite", request)
				if response.Code != http.StatusOK || result.Updated != 1 || result.Rejected != 0 {
					t.Fatalf("no-op overwrite: status=%d updated=%d rejected=%d", response.Code, result.Updated, result.Rejected)
				}
				if strings.Contains(response.Body.String(), password) || strings.Contains(response.Body.String(), privateKey) || strings.Contains(response.Body.String(), "FAKE_IGNORED_") {
					t.Fatal("import response exposed a credential")
				}
				if got := readStored(t, node.ID); got != before {
					t.Fatal("no-op overwrite changed stored configuration, revisions, or credential bytes")
				}
				var readback model.Node
				if err := db.First(&readback, node.ID).Error; err != nil {
					t.Fatalf("read back node: %v", err)
				}
				if readback.Password != tc.password || readback.PrivateKey != tc.privateKey {
					t.Fatal("no-op overwrite changed decrypted credentials")
				}

				request = map[string]any{"nodes": []any{map[string]any{
					"name": node.Name, "host": "new.example", "port": 2222, "username": "other", "auth_type": "password", "tags": "new-tag", "base_path": "/new",
					"password": "FAKE_IGNORED_PASSWORD", "private_key": "FAKE_IGNORED_KEY",
				}}}
				response, result = serveConfigNameMappingImport(t, db, service, "overwrite", request)
				if response.Code != http.StatusOK || result.Updated != 1 || result.Rejected != 0 {
					t.Fatalf("changed overwrite: status=%d updated=%d rejected=%d", response.Code, result.Updated, result.Rejected)
				}
				after := readStored(t, node.ID)
				if after.host != "new.example" || after.port != 2222 || after.username != "other" || after.authType != "password" || after.tags != "new-tag" || after.basePath != "/new" {
					t.Fatal("overwrite did not persist imported node fields")
				}
				if after.generation == before.generation || after.revision == before.revision || after.password != before.password || after.privateKey != before.privateKey {
					t.Fatal("connection change did not advance both revisions while preserving credential bytes")
				}
				if err := db.First(&readback, node.ID).Error; err != nil || readback.Password != tc.password || readback.PrivateKey != tc.privateKey {
					t.Fatal("changed overwrite failed credential readback")
				}
			})
		}
		for _, column := range []string{"password", "private_key"} {
			for _, prefix := range []string{"enc:v1:", "enc:v2:"} {
				t.Run("corrupt_"+column+"_"+strings.Trim(prefix, ":"), func(t *testing.T) {
					name := "corrupt-" + column + "-" + strings.ReplaceAll(strings.Trim(prefix, ":"), ":", "-")
					node := model.Node{Name: name, Host: "host.example", Port: 22, Username: "root", AuthType: "key", BackupDir: name, Password: password, PrivateKey: privateKey}
					if err := db.Create(&node).Error; err != nil {
						t.Fatal(err)
					}
					if err := db.Exec("UPDATE nodes SET "+column+" = ? WHERE id = ?", prefix+"corrupt", node.ID).Error; err != nil {
						t.Fatal(err)
					}
					before := readStored(t, node.ID)
					response, result := serveConfigNameMappingImport(t, db, service, "overwrite", map[string]any{"nodes": []any{map[string]any{"name": name, "host": "new.example"}}})
					if response.Code != http.StatusOK || result.Rejected != 1 || result.Updated != 0 {
						t.Fatalf("corrupted secret overwrite: status=%d updated=%d rejected=%d", response.Code, result.Updated, result.Rejected)
					}
					requireConfigNameMappingWarning(t, result, configImportEntityNodes, 0, configImportWarningInvalidInput)
					if got := readStored(t, node.ID); got != before {
						t.Fatal("rejected overwrite changed the stored node")
					}
					if strings.Contains(response.Body.String(), password) || strings.Contains(response.Body.String(), privateKey) || strings.Contains(response.Body.String(), prefix+"corrupt") {
						t.Fatal("rejection response exposed a credential")
					}
				})
			}
		}
	})
}
