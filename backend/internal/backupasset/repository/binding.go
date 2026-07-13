package repository

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/fileaccess"
	"xirang/backend/internal/model"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"
)

const bindingDocumentVersion = 1

type bindingDocument struct {
	Version            int                         `json:"version"`
	Provider           backupasset.ProviderKind    `json:"provider"`
	IdentityClass      provider.IdentityClass      `json:"identity_class"`
	TaskID             uint                        `json:"task_id"`
	NodeID             uint                        `json:"node_id"`
	IdentitySalt       string                      `json:"identity_salt"`
	Locator            string                      `json:"locator"`
	Secret             string                      `json:"secret,omitempty"`
	EndpointFacts      []string                    `json:"endpoint_facts"`
	Backend            string                      `json:"backend,omitempty"`
	RangeProven        bool                        `json:"range_proven"`
	ConfigSource       provider.RcloneConfigSource `json:"config_source,omitempty"`
	NativeRepositoryID string                      `json:"native_repository_id,omitempty"`
	AdapterRevision    string                      `json:"adapter_revision,omitempty"`
}

func generateBindingSalt() ([]byte, error) {
	salt := make([]byte, provider.IdentitySaltBytes)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate repository binding salt: %w", err)
	}
	return salt, nil
}

func bindingFromTask(taskEntity model.Task, node model.Node, salt []byte) (bindingDocument, provider.AccessBinding, error) {
	if taskEntity.ID == 0 || node.ID == 0 || taskEntity.NodeID != node.ID || len(salt) != provider.IdentitySaltBytes {
		return bindingDocument{}, provider.AccessBinding{}, fmt.Errorf("%w: invalid Task binding input", backupasset.ErrInvalidState)
	}
	kind := bindingProviderForTask(taskEntity)
	switch kind {
	case backupasset.ProviderCommand:
		return bindingDocument{}, provider.AccessBinding{}, fmt.Errorf("%w: command Task has no artifact contract", backupasset.ErrCapabilityUnavailable)
	case backupasset.ProviderRsync, backupasset.ProviderRestic, backupasset.ProviderRclone:
	default:
		return bindingDocument{}, provider.AccessBinding{}, fmt.Errorf("%w: unsupported Task provider", backupasset.ErrCapabilityUnavailable)
	}
	locator := strings.TrimSpace(taskEntity.RsyncTarget)
	if locator == "" {
		return bindingDocument{}, provider.AccessBinding{}, fmt.Errorf("%w: Task repository locator missing", backupasset.ErrInvalidState)
	}
	document := bindingDocument{
		Version: bindingDocumentVersion, Provider: kind, TaskID: taskEntity.ID, NodeID: node.ID,
		IdentitySalt: hex.EncodeToString(salt), Locator: locator,
	}
	access := provider.AccessBinding{
		Provider: kind, TaskID: taskEntity.ID, NodeID: node.ID, IdentitySalt: append([]byte(nil), salt...),
		Locator: locator, RepositoryID: "", Config: []byte(taskEntity.ExecutorConfig),
	}
	command := &provider.RemoteCommandAccess{Node: node}
	switch kind {
	case backupasset.ProviderRsync:
		if util.IsRemotePathSpec(locator) {
			return bindingDocument{}, provider.AccessBinding{}, fmt.Errorf("%w: remote Rsync target has no target credential binding", backupasset.ErrCapabilityUnavailable)
		}
		if !filepath.IsAbs(filepath.Clean(locator)) {
			return bindingDocument{}, provider.AccessBinding{}, fmt.Errorf("%w: Rsync target must be an absolute local path", backupasset.ErrInvalidState)
		}
		document.IdentityClass = provider.IdentityTaskScopedEndpoint
		document.EndpointFacts = []string{fmt.Sprintf("task:%d", taskEntity.ID), fmt.Sprintf("node:%d", node.ID), "transport:local", "root:" + filepath.Clean(locator)}
		access.EndpointFacts = append([]string(nil), document.EndpointFacts...)
		access.AdapterData = provider.RsyncRuntimeAccess{Tree: fileaccess.NewLocalTree(), Root: fileaccess.Root{Path: filepath.Clean(locator)}}
	case backupasset.ProviderRestic:
		var config struct {
			RepositoryPassword string `json:"repository_password"`
		}
		if err := decodeTaskConfig(taskEntity.ExecutorConfig, &config); err != nil || strings.TrimSpace(config.RepositoryPassword) == "" {
			return bindingDocument{}, provider.AccessBinding{}, fmt.Errorf("%w: invalid Restic Task config", backupasset.ErrInvalidState)
		}
		document.IdentityClass = provider.IdentityNativeRepository
		document.Secret = config.RepositoryPassword
		document.EndpointFacts = []string{fmt.Sprintf("task:%d", taskEntity.ID), fmt.Sprintf("node:%d", node.ID)}
		access.Secret = []byte(config.RepositoryPassword)
		access.EndpointFacts = append([]string(nil), document.EndpointFacts...)
		access.AdapterData = provider.ResticRuntimeAccess{Command: command}
	case backupasset.ProviderRclone:
		var config struct {
			BandwidthLimit string `json:"bandwidth_limit"`
			Transfers      int    `json:"transfers"`
		}
		if err := decodeTaskConfig(taskEntity.ExecutorConfig, &config); err != nil {
			return bindingDocument{}, provider.AccessBinding{}, fmt.Errorf("%w: invalid Rclone Task config", backupasset.ErrInvalidState)
		}
		document.IdentityClass = provider.IdentityTaskScopedEndpoint
		document.ConfigSource = provider.RcloneConfigNodeDefault
		document.EndpointFacts = []string{fmt.Sprintf("task:%d", taskEntity.ID), fmt.Sprintf("node:%d", node.ID), "remote:" + locator}
		access.EndpointFacts = append([]string(nil), document.EndpointFacts...)
		access.AdapterData = provider.RcloneRuntimeAccess{ConfigSource: provider.RcloneConfigNodeDefault, Command: command}
	}
	return document, access, nil
}

func bindingProviderForTask(taskEntity model.Task) backupasset.ProviderKind {
	return backupasset.ProviderKind(strings.ToLower(strings.TrimSpace(taskEntity.ExecutorType)))
}

func encodeBindingDocument(document bindingDocument) (string, error) {
	if err := validateBindingDocument(document); err != nil {
		return "", err
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("%w: encode binding document", backupasset.ErrInvalidState)
	}
	return string(payload), nil
}

func decodeBindingDocument(payload string) (bindingDocument, error) {
	var document bindingDocument
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return bindingDocument{}, fmt.Errorf("%w: invalid binding document", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return bindingDocument{}, fmt.Errorf("%w: trailing binding data", backupasset.ErrInvalidState)
	}
	if err := validateBindingDocument(document); err != nil {
		return bindingDocument{}, err
	}
	return document, nil
}

func validateBindingDocument(document bindingDocument) error {
	if document.Version != bindingDocumentVersion || document.TaskID == 0 || document.NodeID == 0 || strings.TrimSpace(document.Locator) == "" || len(document.EndpointFacts) == 0 {
		return fmt.Errorf("%w: incomplete binding document", backupasset.ErrInvalidState)
	}
	salt, err := hex.DecodeString(document.IdentitySalt)
	if err != nil || len(salt) != provider.IdentitySaltBytes {
		return fmt.Errorf("%w: invalid binding identity salt", backupasset.ErrInvalidState)
	}
	switch document.Provider {
	case backupasset.ProviderRestic:
		if document.IdentityClass != provider.IdentityNativeRepository || document.Secret == "" || document.AdapterRevision == "" {
			return fmt.Errorf("%w: invalid Restic binding document", backupasset.ErrInvalidState)
		}
		if _, err := provider.NativeRepositoryIdentity(backupasset.ProviderRestic, document.NativeRepositoryID); err != nil {
			return fmt.Errorf("%w: invalid Restic native repository ID", backupasset.ErrInvalidState)
		}
	case backupasset.ProviderRsync:
		if document.IdentityClass != provider.IdentityTaskScopedEndpoint {
			return fmt.Errorf("%w: invalid scoped binding document", backupasset.ErrInvalidState)
		}
	case backupasset.ProviderRclone:
		if document.IdentityClass != provider.IdentityTaskScopedEndpoint {
			return fmt.Errorf("%w: invalid scoped binding document", backupasset.ErrInvalidState)
		}
		switch document.ConfigSource {
		case provider.RcloneConfigNodeDefault:
			if document.Secret != "" {
				return fmt.Errorf("%w: node-default Rclone binding contains config", backupasset.ErrInvalidState)
			}
		case provider.RcloneConfigBound:
			if document.Secret == "" {
				return fmt.Errorf("%w: bound Rclone config is missing", backupasset.ErrInvalidState)
			}
		default:
			return fmt.Errorf("%w: invalid Rclone config source", backupasset.ErrInvalidState)
		}
	default:
		return fmt.Errorf("%w: unsupported binding provider", backupasset.ErrCapabilityUnavailable)
	}
	return nil
}

func accessFromBindingDocument(document bindingDocument, node model.Node) (provider.AccessBinding, error) {
	salt, err := hex.DecodeString(document.IdentitySalt)
	if err != nil {
		return provider.AccessBinding{}, fmt.Errorf("%w: invalid binding salt", backupasset.ErrInvalidState)
	}
	taskEntity := model.Task{ID: document.TaskID, NodeID: document.NodeID, ExecutorType: string(document.Provider), RsyncTarget: document.Locator}
	if document.Provider == backupasset.ProviderRestic {
		taskEntity.ExecutorConfig = `{"repository_password":` + strconvQuote(document.Secret) + `}`
	}
	doc, access, err := bindingFromTask(taskEntity, node, salt)
	if err != nil {
		return provider.AccessBinding{}, err
	}
	_ = doc
	switch runtimeAccess := access.AdapterData.(type) {
	case provider.ResticRuntimeAccess:
		runtimeAccess.NativeRepositoryID = document.NativeRepositoryID
		access.AdapterData = runtimeAccess
	case provider.RcloneRuntimeAccess:
		runtimeAccess.Backend = document.Backend
		runtimeAccess.RangeProven = document.RangeProven
		runtimeAccess.ConfigSource = document.ConfigSource
		if document.ConfigSource == provider.RcloneConfigBound {
			access.Secret = []byte(document.Secret)
		} else {
			access.Secret = nil
		}
		access.AdapterData = runtimeAccess
	case provider.RsyncRuntimeAccess:
		runtimeAccess.RangeProven = document.RangeProven
		access.AdapterData = runtimeAccess
	}
	return access, nil
}

func decodeTaskConfig(payload string, target any) error {
	if strings.TrimSpace(payload) == "" {
		payload = "{}"
	}
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing Task config")
	}
	return nil
}

func strconvQuote(value string) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}

func withRemoteAuditContext(access provider.AccessBinding, requestContext RequestContext, taskID uint) provider.AccessBinding {
	audit := sshutil.DialAuditContext{
		CorrelationID: requestContext.CorrelationID,
		UserID:        requestContext.Actor.UserID, Username: requestContext.Actor.Username, Role: requestContext.Actor.Role,
		TaskID: &taskID,
	}
	switch runtimeAccess := access.AdapterData.(type) {
	case provider.ResticRuntimeAccess:
		if runtimeAccess.Command != nil {
			runtimeAccess.Command.Audit = audit
		}
		access.AdapterData = runtimeAccess
	case provider.RcloneRuntimeAccess:
		if runtimeAccess.Command != nil {
			runtimeAccess.Command.Audit = audit
		}
		access.AdapterData = runtimeAccess
	}
	return access
}
