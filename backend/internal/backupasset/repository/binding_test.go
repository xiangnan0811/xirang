package repository

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/model"
)

func TestBindingMapsSupportedTasksAndRejectsCommand(t *testing.T) {
	salt := bytes.Repeat([]byte{0x42}, provider.IdentitySaltBytes)
	tests := []struct {
		executor string
		target   string
		config   string
		kind     backupasset.ProviderKind
	}{
		{"rsync", "/tmp/backups", "", backupasset.ProviderRsync},
		{"restic", "sftp:user@example.invalid:/repo", `{"repository_password":"FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY","exclude_patterns":["tmp"]}`, backupasset.ProviderRestic},
		{"rclone", "remote-name:root", `{"bandwidth_limit":"10M","transfers":4}`, backupasset.ProviderRclone},
	}
	for _, tt := range tests {
		t.Run(tt.executor, func(t *testing.T) {
			taskEntity := model.Task{ID: 7, NodeID: 9, ExecutorType: tt.executor, RsyncTarget: tt.target, ExecutorConfig: tt.config}
			node := model.Node{ID: 9, Name: "node", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
			document, access, err := bindingFromTask(taskEntity, node, salt)
			if err != nil || access.Provider != tt.kind || document.Provider != tt.kind || document.Version != bindingDocumentVersion {
				t.Fatalf("document=%+v access=%+v err=%v", document, access, err)
			}
			payload, err := json.Marshal(access)
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range []string{tt.target, "FAKE_RESTIC_PASSWORD_FOR_TEST_ONLY", "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"} {
				if raw != "" && strings.Contains(string(payload), raw) {
					t.Fatalf("access JSON leaked %q: %s", raw, payload)
				}
			}
		})
	}
	if _, _, err := bindingFromTask(model.Task{ID: 1, NodeID: 2, ExecutorType: "command"}, model.Node{ID: 2}, salt); !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("command binding error=%v", err)
	}
}

func TestBindingRejectsRemoteRsyncTargetAsUnsupported(t *testing.T) {
	salt := bytes.Repeat([]byte{0x42}, provider.IdentitySaltBytes)
	taskEntity := model.Task{ID: 7, NodeID: 9, ExecutorType: "rsync", RsyncTarget: "backup@example.invalid:/archive"}
	node := model.Node{ID: 9}
	_, _, err := bindingFromTask(taskEntity, node, salt)
	if !errors.Is(err, backupasset.ErrCapabilityUnavailable) {
		t.Fatalf("remote Rsync target must fail as an unsupported access contract, got %v", err)
	}
}

func TestBindingCodecRejectsUnknownSchemaAndTrailingData(t *testing.T) {
	document := bindingDocument{Version: bindingDocumentVersion, Provider: backupasset.ProviderRsync, IdentityClass: provider.IdentityTaskScopedEndpoint, TaskID: 1, NodeID: 2, IdentitySalt: strings.Repeat("42", provider.IdentitySaltBytes), Locator: "/backup", EndpointFacts: []string{"task:1", "node:2"}}
	payload, err := encodeBindingDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBindingDocument(payload)
	if err != nil || decoded.TaskID != document.TaskID || decoded.Locator != document.Locator {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	for _, invalid := range []string{
		strings.Replace(payload, `"version":1`, `"version":2`, 1),
		payload + `{}`,
		strings.Replace(payload, `"task_id":1`, `"task_id":1,"future":true`, 1),
	} {
		if _, err := decodeBindingDocument(invalid); !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("invalid binding error=%v payload=%s", err, invalid)
		}
	}
}

func TestBindingReconstructsRcloneNodeDefaultConfig(t *testing.T) {
	salt := bytes.Repeat([]byte{0x42}, provider.IdentitySaltBytes)
	node := model.Node{ID: 9, Name: "node", AuthType: "password", Password: "FAKE_NODE_PASSWORD_FOR_TEST_ONLY"}
	taskEntity := model.Task{ID: 7, NodeID: node.ID, ExecutorType: "rclone", RsyncTarget: "remote-name:root", ExecutorConfig: `{}`}
	document, access, err := bindingFromTask(taskEntity, node, salt)
	if err != nil {
		t.Fatal(err)
	}
	runtimeAccess, ok := access.AdapterData.(provider.RcloneRuntimeAccess)
	if !ok || runtimeAccess.ConfigSource != provider.RcloneConfigNodeDefault || len(access.Secret) != 0 || document.ConfigSource != provider.RcloneConfigNodeDefault {
		t.Fatalf("document=%+v access=%+v", document, access)
	}
	payload, err := encodeBindingDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeBindingDocument(payload)
	if err != nil {
		t.Fatal(err)
	}
	reconstructed, err := accessFromBindingDocument(decoded, node)
	if err != nil {
		t.Fatal(err)
	}
	runtimeAccess, ok = reconstructed.AdapterData.(provider.RcloneRuntimeAccess)
	if !ok || runtimeAccess.ConfigSource != provider.RcloneConfigNodeDefault || len(reconstructed.Secret) != 0 {
		t.Fatalf("reconstructed access=%+v", reconstructed)
	}
}

func TestValidateObservationIncludesRcloneBackendIdentityFact(t *testing.T) {
	salt := bytes.Repeat([]byte{0x42}, provider.IdentitySaltBytes)
	access := provider.AccessBinding{
		Provider: backupasset.ProviderRclone, TaskID: 7, NodeID: 9, IdentitySalt: salt,
		EndpointFacts: []string{"task:7", "node:9", "remote:archive:root"},
	}
	identityFacts := append(append([]string(nil), access.EndpointFacts...), "backend:s3")
	identity, err := provider.DeriveScopedIdentity(salt, provider.ScopedIdentityDocument{
		Provider: backupasset.ProviderRclone, TaskID: access.TaskID, NodeID: access.NodeID, EndpointFacts: identityFacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	observation := provider.RepositoryObservation{
		Provider: backupasset.ProviderRclone, IdentityClass: provider.IdentityTaskScopedEndpoint,
		RepositoryIdentity: identity, VersionMode: backupasset.VersionMutableHead, AdapterRevision: "rclone-reader:v1",
		SourceRevision: strings.Repeat("a", 64), Availability: backupasset.PhysicalOnline,
		InternalProviderFacts: map[string]string{"backend": "s3"},
	}
	if err := validateObservation(access, observation); err != nil {
		t.Fatalf("valid Rclone backend-scoped observation rejected: %v", err)
	}
}

func TestRepositoryProductionKeepsExecutorMappingInsideBindingBoundary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		source := string(content)
		for _, forbiddenImport := range []string{"/internal/api/handlers", "/internal/task/executor"} {
			if strings.Contains(source, forbiddenImport) {
				t.Fatalf("repository production file %s crosses forbidden boundary %s", file, forbiddenImport)
			}
		}
		if file != "binding.go" && strings.Contains(source, "ExecutorType") {
			t.Fatalf("repository production file %s branches on Task executor outside binding mapping", file)
		}
	}
}
