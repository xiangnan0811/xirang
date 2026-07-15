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

const (
	bindingDocumentVersion             = 1
	managedRsyncBindingDocumentVersion = 2
	managedRsyncLayoutRevisionV1       = provider.RsyncManagedTreeLayoutRevisionV1
)

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

// managedRsyncBindingDocumentV2 is stored only inside the encrypted repository
// access binding. It deliberately has no legacy Task.RsyncTarget locator: the
// mutable target remains on TaskRepositoryLink as the rollback locator.
type managedRsyncBindingDocumentV2 struct {
	Version                   int                             `json:"version"`
	Provider                  backupasset.ProviderKind        `json:"provider"`
	IdentityClass             provider.IdentityClass          `json:"identity_class"`
	TaskID                    uint                            `json:"task_id"`
	NodeID                    uint                            `json:"node_id"`
	RepositoryID              string                          `json:"repository_id"`
	TaskRepositoryLinkID      string                          `json:"task_repository_link_id"`
	LayoutRevision            string                          `json:"layout_revision"`
	ManagedRootLocator        string                          `json:"managed_root_locator"`
	RootMarkerDigest          string                          `json:"root_marker_digest"`
	ManagedRootIdentityDigest string                          `json:"managed_root_identity_digest"`
	PublicationMode           backupasset.TaskPublicationMode `json:"publication_mode"`
	PreflightID               string                          `json:"preflight_id"`
	PreflightDigest           string                          `json:"preflight_digest"`
	SeedFullCopyRequired      bool                            `json:"seed_full_copy_required"`
	RollbackPrepared          bool                            `json:"rollback_prepared"`
	IdentitySalt              string                          `json:"identity_salt"`
}

// managedRsyncBindingAssociation is deliberately internal. Activation and
// later point-read paths must bind an encrypted V2 document to the current
// Task/link/marker facts before a managed root is used.
type managedRsyncBindingAssociation struct {
	Task             model.Task
	Link             model.TaskRepositoryLink
	RootMarkerDigest string
}

// storedBindingDocument is an internal closed union. New call sites must
// inspect the exact decoded version instead of treating an encrypted V2
// document as a mutable V1 locator.
type storedBindingDocument struct {
	V1             *bindingDocument
	ManagedRsyncV2 *managedRsyncBindingDocumentV2
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
	decoder, err := strictBindingDocumentDecoder(payload)
	if err != nil {
		return bindingDocument{}, fmt.Errorf("%w: invalid binding document", backupasset.ErrInvalidState)
	}
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

func decodeStoredBindingDocument(payload string) (storedBindingDocument, error) {
	version, err := decodeBindingDocumentVersion(payload)
	if err != nil {
		return storedBindingDocument{}, fmt.Errorf("%w: invalid binding document", backupasset.ErrInvalidState)
	}
	switch version {
	case bindingDocumentVersion:
		document, err := decodeBindingDocument(payload)
		if err != nil {
			return storedBindingDocument{}, err
		}
		return storedBindingDocument{V1: &document}, nil
	case managedRsyncBindingDocumentVersion:
		document, err := decodeManagedRsyncBindingDocumentV2(payload)
		if err != nil {
			return storedBindingDocument{}, err
		}
		return storedBindingDocument{ManagedRsyncV2: &document}, nil
	default:
		return storedBindingDocument{}, fmt.Errorf("%w: unsupported binding document version", backupasset.ErrInvalidState)
	}
}

func encodeManagedRsyncBindingDocumentV2(document managedRsyncBindingDocumentV2) (string, error) {
	if err := validateManagedRsyncBindingDocumentV2(document); err != nil {
		return "", err
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("%w: encode managed Rsync binding document", backupasset.ErrInvalidState)
	}
	return string(payload), nil
}

func decodeManagedRsyncBindingDocumentV2(payload string) (managedRsyncBindingDocumentV2, error) {
	var document managedRsyncBindingDocumentV2
	decoder, err := strictBindingDocumentDecoder(payload)
	if err != nil {
		return managedRsyncBindingDocumentV2{}, fmt.Errorf("%w: invalid managed Rsync binding document", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&document); err != nil {
		return managedRsyncBindingDocumentV2{}, fmt.Errorf("%w: invalid managed Rsync binding document", backupasset.ErrInvalidState)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return managedRsyncBindingDocumentV2{}, fmt.Errorf("%w: trailing managed Rsync binding data", backupasset.ErrInvalidState)
	}
	if err := validateManagedRsyncBindingDocumentV2(document); err != nil {
		return managedRsyncBindingDocumentV2{}, err
	}
	return document, nil
}

func decodeBindingDocumentVersion(payload string) (int, error) {
	if err := rejectDuplicateBindingDocumentMembers(payload); err != nil {
		return 0, err
	}
	var header struct {
		Version int `json:"version"`
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	if err := decoder.Decode(&header); err != nil {
		return 0, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return 0, fmt.Errorf("trailing binding document data")
		}
		return 0, err
	}
	return header.Version, nil
}

func strictBindingDocumentDecoder(payload string) (*json.Decoder, error) {
	if err := rejectDuplicateBindingDocumentMembers(payload); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	return decoder, nil
}

func rejectDuplicateBindingDocumentMembers(payload string) error {
	decoder := json.NewDecoder(strings.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		if err != nil {
			return err
		}
		return fmt.Errorf("binding document must be a JSON object")
	}
	members := make(map[string]struct{})
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := nameToken.(string)
		if !ok {
			return fmt.Errorf("binding document member is not a string")
		}
		if _, exists := members[name]; exists {
			return fmt.Errorf("duplicate binding document member")
		}
		members[name] = struct{}{}
		var discard json.RawMessage
		if err := decoder.Decode(&discard); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		if err != nil {
			return err
		}
		return fmt.Errorf("invalid binding document terminator")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing binding document data")
		}
		return err
	}
	return nil
}

func validateManagedRsyncBindingDocumentV2(document managedRsyncBindingDocumentV2) error {
	if document.Version != managedRsyncBindingDocumentVersion ||
		document.Provider != backupasset.ProviderRsync ||
		document.IdentityClass != provider.IdentityXirangManagedRepository ||
		document.TaskID == 0 || document.NodeID == 0 ||
		backupasset.ValidateOpaqueID(document.RepositoryID) != nil ||
		backupasset.ValidateOpaqueID(document.TaskRepositoryLinkID) != nil ||
		document.LayoutRevision != managedRsyncLayoutRevisionV1 ||
		strings.TrimSpace(document.ManagedRootLocator) == "" ||
		!isLowerHex64(document.RootMarkerDigest) || !isLowerHex64(document.ManagedRootIdentityDigest) ||
		backupasset.ValidateOpaqueID(document.PreflightID) != nil || !isLowerHex64(document.PreflightDigest) {
		return fmt.Errorf("%w: invalid managed Rsync binding document", backupasset.ErrInvalidState)
	}
	if !filepath.IsAbs(filepath.Clean(document.ManagedRootLocator)) {
		return fmt.Errorf("%w: managed Rsync root must be an absolute local path", backupasset.ErrInvalidState)
	}
	if _, err := hexDecodeSalt(document.IdentitySalt); err != nil {
		return err
	}
	switch document.PublicationMode {
	case backupasset.PublicationVersionedHardlink:
		return nil
	case backupasset.PublicationVersionedFullCopy:
		if document.SeedFullCopyRequired {
			return fmt.Errorf("%w: full-copy managed Rsync binding cannot require a seed", backupasset.ErrInvalidState)
		}
		return nil
	default:
		return fmt.Errorf("%w: invalid managed Rsync publication mode", backupasset.ErrInvalidState)
	}
}

func managedRsyncRepositoryIdentity(document managedRsyncBindingDocumentV2) (string, error) {
	if err := validateManagedRsyncBindingDocumentV2(document); err != nil {
		return "", err
	}
	salt, err := hexDecodeSalt(document.IdentitySalt)
	if err != nil {
		return "", err
	}
	return provider.DeriveScopedIdentity(salt, provider.ScopedIdentityDocument{
		Provider: backupasset.ProviderRsync, TaskID: document.TaskID, NodeID: document.NodeID,
		EndpointFacts: []string{
			"identity_class:xirang_managed_repository",
			"layout:" + document.LayoutRevision,
			"managed_root_identity:" + document.ManagedRootIdentityDigest,
			"repository:" + document.RepositoryID,
		},
	})
}

func validateManagedRsyncBindingAssociation(document managedRsyncBindingDocumentV2, association managedRsyncBindingAssociation) error {
	if err := validateManagedRsyncBindingDocumentV2(document); err != nil {
		return err
	}
	if association.Task.ID != document.TaskID || association.Task.NodeID != document.NodeID ||
		association.Link.TaskID == nil || *association.Link.TaskID != association.Task.ID ||
		association.Link.ID != document.TaskRepositoryLinkID || association.Link.RepositoryID != document.RepositoryID ||
		association.Link.PublicationMode != string(document.PublicationMode) ||
		association.RootMarkerDigest != document.RootMarkerDigest || !isLowerHex64(association.RootMarkerDigest) {
		return fmt.Errorf("%w: managed Rsync binding identity drift", backupasset.ErrConflict)
	}
	if strings.TrimSpace(association.Task.RsyncTarget) != "" &&
		filepath.Clean(association.Task.RsyncTarget) == filepath.Clean(document.ManagedRootLocator) {
		return fmt.Errorf("%w: managed Rsync root must not use Task legacy target", backupasset.ErrConflict)
	}
	return nil
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
