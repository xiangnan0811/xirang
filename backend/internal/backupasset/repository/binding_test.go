package repository

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestManagedRsyncBindingV2RoundTripAndV1DecoderRefusesIt(t *testing.T) {
	document := managedRsyncBindingDocumentV2{
		Version:                   managedRsyncBindingDocumentVersion,
		Provider:                  backupasset.ProviderRsync,
		IdentityClass:             provider.IdentityXirangManagedRepository,
		TaskID:                    7,
		NodeID:                    9,
		RepositoryID:              strings.Repeat("a", 32),
		TaskRepositoryLinkID:      strings.Repeat("b", 32),
		LayoutRevision:            managedRsyncLayoutRevisionV1,
		ManagedRootLocator:        "/srv/xirang-managed/7",
		RootMarkerDigest:          strings.Repeat("c", 64),
		ManagedRootIdentityDigest: strings.Repeat("d", 64),
		PublicationMode:           backupasset.PublicationVersionedHardlink,
		PreflightID:               strings.Repeat("e", 32),
		PreflightDigest:           strings.Repeat("f", 64),
		IdentitySalt:              strings.Repeat("42", provider.IdentitySaltBytes),
	}
	payload, err := encodeManagedRsyncBindingDocumentV2(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeManagedRsyncBindingDocumentV2(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != document {
		t.Fatalf("managed binding round trip=%+v, want %+v", decoded, document)
	}
	if _, err := decodeBindingDocument(payload); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("v1 decoder accepted v2 managed binding: %v", err)
	}
	stored, err := decodeStoredBindingDocument(payload)
	if err != nil || stored.ManagedRsyncV2 == nil || stored.V1 != nil || *stored.ManagedRsyncV2 != document {
		t.Fatalf("stored v2 binding=%+v err=%v", stored, err)
	}
	if _, err := decodeManagedRsyncBindingDocumentV2(payload[:len(payload)-1] + `,"future":true}`); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("v2 decoder accepted unknown field: %v", err)
	}
	if _, err := decodeStoredBindingDocument(strings.Replace(payload, `"version":2`, `"version":2,"version":2`, 1)); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("stored decoder accepted duplicate version: %v", err)
	}
	if _, err := decodeStoredBindingDocument(strings.Replace(payload, `"version":2`, `"version":3`, 1)); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("stored decoder accepted unsupported version: %v", err)
	}

	serialized, err := json.Marshal(model.RepositoryAccessBinding{EncryptedConfig: payload})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), document.ManagedRootLocator) {
		t.Fatalf("managed root locator leaked through public binding JSON: %s", serialized)
	}
}

func TestManagedRcloneBindingV3RoundTripIsClosedAndV1V2RefuseIt(t *testing.T) {
	document := validManagedRclonePortableBindingForTest(t)
	payload, err := encodeManagedRcloneBindingDocumentV3(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeManagedRcloneBindingDocumentV3(payload)
	if err != nil || !reflect.DeepEqual(decoded, document) {
		t.Fatalf("managed Rclone V3 round trip=%+v err=%v want=%+v", decoded, err, document)
	}
	if _, err := decodeBindingDocument(payload); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("V1 decoder accepted managed Rclone V3: %v", err)
	}
	if _, err := decodeManagedRsyncBindingDocumentV2(payload); !errors.Is(err, backupasset.ErrInvalidState) {
		t.Fatalf("V2 decoder accepted managed Rclone V3: %v", err)
	}
	stored, err := decodeStoredBindingDocument(payload)
	if err != nil || stored.ManagedRcloneV3 == nil || stored.ManagedRsyncV2 != nil || stored.V1 != nil ||
		!reflect.DeepEqual(*stored.ManagedRcloneV3, document) {
		t.Fatalf("stored V3 binding=%+v err=%v", stored, err)
	}
	for _, invalid := range []string{
		strings.Replace(payload, `"publication_mode":"versioned_prefix"`, `"publication_mode":"native_object_versions","native":{"profile_code":"aws_s3_general_purpose_v1"}`, 1),
		strings.TrimSuffix(payload, "}") + `,"future":true}`,
		strings.Replace(payload, `"version":3`, `"version":3,"version":3`, 1),
	} {
		if _, err := decodeManagedRcloneBindingDocumentV3(invalid); !errors.Is(err, backupasset.ErrInvalidState) {
			t.Fatalf("invalid V3 binding accepted: %v payload=%s", err, invalid)
		}
	}
}

func TestManagedRsyncBindingV2RequiresExecutionProof(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"version":                      managedRsyncBindingDocumentVersion,
		"provider":                     backupasset.ProviderRsync,
		"identity_class":               provider.IdentityXirangManagedRepository,
		"task_id":                      7,
		"node_id":                      9,
		"repository_id":                strings.Repeat("a", 32),
		"task_repository_link_id":      strings.Repeat("b", 32),
		"layout_revision":              managedRsyncLayoutRevisionV1,
		"managed_root_locator":         "/srv/xirang-managed/7",
		"root_marker_digest":           strings.Repeat("c", 64),
		"managed_root_identity_digest": strings.Repeat("d", 64),
		"publication_mode":             backupasset.PublicationVersionedFullCopy,
		"preflight_id":                 strings.Repeat("e", 32),
		"preflight_digest":             strings.Repeat("f", 64),
		"identity_salt":                strings.Repeat("42", provider.IdentitySaltBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeManagedRsyncBindingDocumentV2(string(payload)); err != nil {
		t.Fatalf("complete managed binding execution proof rejected: %v", err)
	}
	for _, field := range []string{"managed_root_identity_digest", "preflight_id", "preflight_digest"} {
		t.Run("missing "+field, func(t *testing.T) {
			var candidate map[string]any
			if err := json.Unmarshal(payload, &candidate); err != nil {
				t.Fatal(err)
			}
			delete(candidate, field)
			encoded, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeManagedRsyncBindingDocumentV2(string(encoded)); !errors.Is(err, backupasset.ErrInvalidState) {
				t.Fatalf("missing execution proof field error=%v, want invalid state", err)
			}
		})
	}
}

func TestManagedRsyncBindingV2RejectsRootTaskAndLinkDrift(t *testing.T) {
	task := model.Task{ID: 7, NodeID: 9, RsyncTarget: "/srv/legacy-rsync"}
	linkTaskID := task.ID
	link := model.TaskRepositoryLink{
		ID:              strings.Repeat("b", 32),
		TaskID:          &linkTaskID,
		RepositoryID:    strings.Repeat("a", 32),
		PublicationMode: string(backupasset.PublicationVersionedFullCopy),
	}
	document := managedRsyncBindingDocumentV2{
		Version:                   managedRsyncBindingDocumentVersion,
		Provider:                  backupasset.ProviderRsync,
		IdentityClass:             provider.IdentityXirangManagedRepository,
		TaskID:                    task.ID,
		NodeID:                    task.NodeID,
		RepositoryID:              link.RepositoryID,
		TaskRepositoryLinkID:      link.ID,
		LayoutRevision:            managedRsyncLayoutRevisionV1,
		ManagedRootLocator:        "/srv/xirang-managed/7",
		RootMarkerDigest:          strings.Repeat("c", 64),
		ManagedRootIdentityDigest: strings.Repeat("d", 64),
		PublicationMode:           backupasset.PublicationVersionedFullCopy,
		PreflightID:               strings.Repeat("e", 32),
		PreflightDigest:           strings.Repeat("f", 64),
		IdentitySalt:              strings.Repeat("42", provider.IdentitySaltBytes),
	}
	association := managedRsyncBindingAssociation{Task: task, Link: link, RootMarkerDigest: document.RootMarkerDigest}
	if err := validateManagedRsyncBindingAssociation(document, association); err != nil {
		t.Fatalf("valid managed binding association: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*managedRsyncBindingAssociation)
	}{
		{"root marker", func(value *managedRsyncBindingAssociation) { value.RootMarkerDigest = strings.Repeat("d", 64) }},
		{"task", func(value *managedRsyncBindingAssociation) { value.Task.ID++ }},
		{"link", func(value *managedRsyncBindingAssociation) { value.Link.ID = strings.Repeat("e", 32) }},
		{"mode", func(value *managedRsyncBindingAssociation) {
			value.Link.PublicationMode = string(backupasset.PublicationLegacyMutable)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := association
			tt.mutate(&candidate)
			if err := validateManagedRsyncBindingAssociation(document, candidate); !errors.Is(err, backupasset.ErrConflict) {
				t.Fatalf("association drift error=%v, want conflict", err)
			}
		})
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
