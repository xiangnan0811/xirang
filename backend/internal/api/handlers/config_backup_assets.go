package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/provider"
	"xirang/backend/internal/backupasset/retention"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	configExportVersion1             = "1.0"
	configExportVersion2             = "2.0"
	configImportedIdentityRefPrefix  = backupasset.ImportedRepositoryIdentityRefPrefix
	configAssetEntityRepository      = "repository"
	configAssetEntityTaskLink        = "task_link"
	configAssetEntityRetentionPolicy = "retention_policy"
	configAssetBindingStatusRevoked  = "revoked"
	configAssetBindingStatusActive   = "active"
	configAssetTaskRefPrefix         = "task:"
	configAssetTaskRefSeparator      = "@"
)

var (
	errConfigAssetGraphConflict = errors.New("backup asset graph identity conflict")
	errConfigAssetGraphInvalid  = errors.New("backup asset graph is invalid")
)

type configAssetExportCounts struct {
	Repositories int
	Links        int
	Policies     int
	Holds        int
}

type configAssetGraph struct {
	BackupRepositories      []configAssetRepositoryExport `json:"backup_repositories"`
	TaskRepositoryLinks     []configAssetLinkExport       `json:"task_repository_links"`
	BackupRetentionPolicies []configAssetPolicyExport     `json:"backup_retention_policies"`
	RecoveryPointHolds      []configAssetHoldExport       `json:"recovery_point_holds"`
}

type configAssetRepositoryExport struct {
	RepositoryRef     string                      `json:"repository_ref"`
	ProviderKind      string                      `json:"provider_kind"`
	DisplayName       string                      `json:"display_name"`
	Description       string                      `json:"description,omitempty"`
	VersionMode       string                      `json:"version_mode"`
	Status            string                      `json:"status"`
	ImmutabilityLevel string                      `json:"immutability_level"`
	IdentityRef       string                      `json:"identity_ref,omitempty"`
	AccessBinding     *configAssetBindingEnvelope `json:"access_binding,omitempty"`
}

type configAssetBindingEnvelope struct {
	BindingKind       string `json:"binding_kind"`
	ConfigFingerprint string `json:"config_fingerprint"`
	Envelope          string `json:"envelope"`
}

type configAssetLinkExport struct {
	LinkRef          string `json:"link_ref"`
	RepositoryRef    string `json:"repository_ref"`
	TaskRef          string `json:"task_ref"`
	TaskNameSnapshot string `json:"task_name_snapshot"`
	NodeNameSnapshot string `json:"node_name_snapshot"`
	PublicationMode  string `json:"publication_mode"`
}

type configAssetPolicyExport struct {
	PolicyRef     string `json:"policy_ref"`
	ScopeKind     string `json:"scope_kind"`
	RepositoryRef string `json:"repository_ref,omitempty"`
	LinkRef       string `json:"link_ref,omitempty"`
	Revision      int64  `json:"revision"`
	RulesJSON     string `json:"rules_json"`
	Status        string `json:"status"`
}

type configAssetHoldExport struct {
	HoldRef       string     `json:"hold_ref"`
	RepositoryRef string     `json:"repository_ref"`
	HoldType      string     `json:"hold_type"`
	State         string     `json:"state"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

type configImportEnvelope struct {
	Version    string
	DocumentID string
	Classic    configImportData
	Graph      configAssetGraph
}

func decodeConfigImportEnvelope(body []byte) (configImportEnvelope, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return configImportEnvelope{}, err
	}
	envelope := configImportEnvelope{}
	if raw, ok := top["version"]; ok {
		if err := json.Unmarshal(raw, &envelope.Version); err != nil {
			return configImportEnvelope{}, err
		}
	}
	if raw, ok := top["document_id"]; ok {
		if err := json.Unmarshal(raw, &envelope.DocumentID); err != nil {
			return configImportEnvelope{}, err
		}
	}
	payload := body
	if raw, ok := top["data"]; ok {
		payload = raw
	}
	var combined struct {
		configImportData
		configAssetGraph
	}
	if err := json.Unmarshal(payload, &combined); err != nil {
		return configImportEnvelope{}, err
	}
	envelope.Classic = combined.configImportData
	envelope.Graph = combined.configAssetGraph
	return envelope, nil
}

func validateConfigImportEnvelope(envelope configImportEnvelope) error {
	switch strings.TrimSpace(envelope.Version) {
	case "", configExportVersion1:
		return nil
	case configExportVersion2:
		if err := backupasset.ValidateOpaqueID(envelope.DocumentID); err != nil {
			return fmt.Errorf("%w: document_id is required", errConfigAssetGraphInvalid)
		}
		return validateConfigAssetGraph(envelope.Graph)
	default:
		return fmt.Errorf("%w: unsupported config version", errConfigAssetGraphInvalid)
	}
}

func validateConfigAssetGraph(graph configAssetGraph) error {
	repositories := make(map[string]configAssetRepositoryExport, len(graph.BackupRepositories))
	for index, repository := range graph.BackupRepositories {
		if !validConfigAssetRef(repository.RepositoryRef) {
			return fmt.Errorf("%w: backup_repositories[%d].repository_ref is invalid", errConfigAssetGraphInvalid, index)
		}
		if _, exists := repositories[repository.RepositoryRef]; exists {
			return fmt.Errorf("%w: duplicate repository_ref", errConfigAssetGraphInvalid)
		}
		if strings.TrimSpace(repository.ProviderKind) == "" || strings.TrimSpace(repository.DisplayName) == "" ||
			strings.TrimSpace(repository.VersionMode) == "" || strings.TrimSpace(repository.ImmutabilityLevel) == "" {
			return fmt.Errorf("%w: backup_repositories[%d] is incomplete", errConfigAssetGraphInvalid, index)
		}
		if !validConfigAssetProviderKind(repository.ProviderKind) ||
			!validConfigAssetVersionMode(repository.VersionMode) ||
			!validConfigAssetImmutability(repository.ImmutabilityLevel) {
			return fmt.Errorf("%w: backup_repositories[%d] identity fields are invalid", errConfigAssetGraphInvalid, index)
		}
		if err := validateConfigAssetRepositoryIdentity(repository); err != nil {
			return fmt.Errorf("%w: backup_repositories[%d] provider identity is incompatible", errConfigAssetGraphInvalid, index)
		}
		if !validConfigAssetIdentityRef(repository.IdentityRef) {
			return fmt.Errorf("%w: backup_repositories[%d].identity_ref is invalid", errConfigAssetGraphInvalid, index)
		}
		if repository.AccessBinding != nil {
			if strings.TrimSpace(repository.AccessBinding.BindingKind) == "" || strings.TrimSpace(repository.AccessBinding.Envelope) == "" {
				return fmt.Errorf("%w: backup_repositories[%d].access_binding is incomplete", errConfigAssetGraphInvalid, index)
			}
			if !validConfigAssetBindingKind(repository.AccessBinding.BindingKind) {
				return fmt.Errorf("%w: backup_repositories[%d].access_binding.binding_kind is invalid", errConfigAssetGraphInvalid, index)
			}
		}
		repositories[repository.RepositoryRef] = repository
	}

	links := make(map[string]configAssetLinkExport, len(graph.TaskRepositoryLinks))
	for index, link := range graph.TaskRepositoryLinks {
		if !validConfigAssetRef(link.LinkRef) || !validConfigAssetRef(link.TaskRef) {
			return fmt.Errorf("%w: task_repository_links[%d] refs are invalid", errConfigAssetGraphInvalid, index)
		}
		if _, exists := links[link.LinkRef]; exists {
			return fmt.Errorf("%w: duplicate link_ref", errConfigAssetGraphInvalid)
		}
		if _, ok := repositories[link.RepositoryRef]; !ok {
			return fmt.Errorf("%w: task_repository_links[%d].repository_ref is unknown", errConfigAssetGraphInvalid, index)
		}
		if _, _, ok := parseConfigAssetTaskRef(link.TaskRef); !ok {
			return fmt.Errorf("%w: task_repository_links[%d].task_ref is invalid", errConfigAssetGraphInvalid, index)
		}
		if strings.TrimSpace(link.PublicationMode) == "" || !validConfigAssetPublicationMode(link.PublicationMode) {
			return fmt.Errorf("%w: task_repository_links[%d].publication_mode is invalid", errConfigAssetGraphInvalid, index)
		}
		repository := repositories[link.RepositoryRef]
		if err := validateConfigAssetCompatibleGraph(repository, link); err != nil {
			return fmt.Errorf("%w: task_repository_links[%d] is incompatible with repository identity", errConfigAssetGraphInvalid, index)
		}
		links[link.LinkRef] = link
	}

	seenPolicies := make(map[string]struct{}, len(graph.BackupRetentionPolicies))
	seenPolicyScopes := make(map[string]struct{}, len(graph.BackupRetentionPolicies))
	for index, policy := range graph.BackupRetentionPolicies {
		if !validConfigAssetRef(policy.PolicyRef) {
			return fmt.Errorf("%w: backup_retention_policies[%d].policy_ref is invalid", errConfigAssetGraphInvalid, index)
		}
		if _, exists := seenPolicies[policy.PolicyRef]; exists {
			return fmt.Errorf("%w: duplicate policy_ref", errConfigAssetGraphInvalid)
		}
		if _, err := retention.ParsePolicyRules(policy.RulesJSON); err != nil {
			return fmt.Errorf("%w: backup_retention_policies[%d].rules_json is invalid", errConfigAssetGraphInvalid, index)
		}
		if policy.Revision < 1 {
			return fmt.Errorf("%w: backup_retention_policies[%d].revision is invalid", errConfigAssetGraphInvalid, index)
		}
		if policy.Status != string(backupasset.RetentionPolicyActive) {
			return fmt.Errorf("%w: backup_retention_policies[%d].status is invalid", errConfigAssetGraphInvalid, index)
		}
		var scopeKey string
		switch policy.ScopeKind {
		case "repository":
			if _, ok := repositories[policy.RepositoryRef]; !ok {
				return fmt.Errorf("%w: backup_retention_policies[%d].repository_ref is unknown", errConfigAssetGraphInvalid, index)
			}
			scopeKey = policy.ScopeKind + ":" + policy.RepositoryRef
		case "task_link":
			if _, ok := links[policy.LinkRef]; !ok {
				return fmt.Errorf("%w: backup_retention_policies[%d].link_ref is unknown", errConfigAssetGraphInvalid, index)
			}
			scopeKey = policy.ScopeKind + ":" + policy.LinkRef
		default:
			return fmt.Errorf("%w: backup_retention_policies[%d].scope_kind is invalid", errConfigAssetGraphInvalid, index)
		}
		if _, exists := seenPolicyScopes[scopeKey]; exists {
			return fmt.Errorf("%w: duplicate retention policy scope", errConfigAssetGraphInvalid)
		}
		seenPolicyScopes[scopeKey] = struct{}{}
		seenPolicies[policy.PolicyRef] = struct{}{}
	}

	if len(graph.RecoveryPointHolds) > 0 {
		return fmt.Errorf("%w: recovery_point_holds cannot be restored from config backup", errConfigAssetGraphInvalid)
	}
	return nil
}

func (h *ConfigHandler) buildConfigAssetExportGraph(includeSecrets bool) (configAssetGraph, configAssetExportCounts, error) {
	graph := configAssetGraph{
		BackupRepositories:      []configAssetRepositoryExport{},
		TaskRepositoryLinks:     []configAssetLinkExport{},
		BackupRetentionPolicies: []configAssetPolicyExport{},
		RecoveryPointHolds:      []configAssetHoldExport{},
	}
	if h == nil || h.db == nil {
		return graph, configAssetExportCounts{}, nil
	}

	var repositories []model.BackupRepository
	if h.db.Migrator().HasTable(&model.BackupRepository{}) {
		if err := h.db.Order("id").Find(&repositories).Error; err != nil {
			return configAssetGraph{}, configAssetExportCounts{}, err
		}
	}
	repoRefByID := make(map[string]string, len(repositories))
	for index, repository := range repositories {
		ref := fmt.Sprintf("repository_%d", index+1)
		repoRefByID[repository.ID] = ref
		item := configAssetRepositoryExport{
			RepositoryRef:     ref,
			ProviderKind:      repository.ProviderKind,
			DisplayName:       repository.DisplayName,
			Description:       repository.Description,
			VersionMode:       repository.VersionMode,
			Status:            repository.Status,
			ImmutabilityLevel: repository.ImmutabilityLevel,
			IdentityRef:       configAssetIdentityRef(repository.RepositoryIdentity),
		}
		if includeSecrets {
			envelope, err := h.exportRepositoryBindingEnvelope(repository.ID)
			if err != nil {
				return configAssetGraph{}, configAssetExportCounts{}, err
			}
			item.AccessBinding = envelope
		}
		graph.BackupRepositories = append(graph.BackupRepositories, item)
	}

	var links []model.TaskRepositoryLink
	if h.db.Migrator().HasTable(&model.TaskRepositoryLink{}) {
		query := h.db.Where("unlinked_at IS NULL")
		if h.db.Migrator().HasTable(&model.Task{}) {
			query = query.Where("task_id IS NULL OR task_id NOT IN (SELECT id FROM tasks WHERE archived_at IS NOT NULL)")
		}
		if err := query.Order("id").Find(&links).Error; err != nil {
			return configAssetGraph{}, configAssetExportCounts{}, err
		}
	}
	linkRefByID := make(map[string]string, len(links))
	for index, link := range links {
		repositoryRef, ok := repoRefByID[link.RepositoryID]
		if !ok {
			continue
		}
		ref := fmt.Sprintf("link_%d", index+1)
		linkRefByID[link.ID] = ref
		graph.TaskRepositoryLinks = append(graph.TaskRepositoryLinks, configAssetLinkExport{
			LinkRef:          ref,
			RepositoryRef:    repositoryRef,
			TaskRef:          configAssetTaskRef(link.TaskNameSnapshot, link.NodeNameSnapshot),
			TaskNameSnapshot: link.TaskNameSnapshot,
			NodeNameSnapshot: link.NodeNameSnapshot,
			PublicationMode:  link.PublicationMode,
		})
	}

	var policies []model.BackupRetentionPolicy
	if h.db.Migrator().HasTable(&model.BackupRetentionPolicy{}) {
		if err := h.db.Where("status = ? AND deleted_at IS NULL", "active").Order("id").Find(&policies).Error; err != nil {
			return configAssetGraph{}, configAssetExportCounts{}, err
		}
	}
	for index, policy := range policies {
		item := configAssetPolicyExport{
			PolicyRef: fmt.Sprintf("policy_%d", index+1),
			ScopeKind: policy.ScopeKind,
			Revision:  policy.Revision,
			RulesJSON: policy.RulesJSON,
			Status:    policy.Status,
		}
		switch policy.ScopeKind {
		case "repository":
			item.RepositoryRef = repoRefByID[policy.ScopeID]
		case "task_link":
			item.LinkRef = linkRefByID[policy.ScopeID]
		}
		if item.RepositoryRef == "" && item.LinkRef == "" {
			continue
		}
		graph.BackupRetentionPolicies = append(graph.BackupRetentionPolicies, item)
	}

	return graph, configAssetExportCounts{
		Repositories: len(graph.BackupRepositories),
		Links:        len(graph.TaskRepositoryLinks),
		Policies:     len(graph.BackupRetentionPolicies),
		Holds:        len(graph.RecoveryPointHolds),
	}, nil
}

func (h *ConfigHandler) exportRepositoryBindingEnvelope(repositoryID string) (*configAssetBindingEnvelope, error) {
	if h == nil || h.db == nil || !h.db.Migrator().HasTable(&model.RepositoryAccessBinding{}) {
		return nil, nil
	}
	var binding model.RepositoryAccessBinding
	err := h.db.Where("repository_id = ? AND status = ?", repositoryID, configAssetBindingStatusActive).
		Order("id").First(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	envelope, err := secure.EncryptIfNeeded(binding.EncryptedConfig)
	if err != nil {
		return nil, err
	}
	return &configAssetBindingEnvelope{
		BindingKind:       binding.BindingKind,
		ConfigFingerprint: binding.ConfigFingerprint,
		Envelope:          envelope,
	}, nil
}

func (h *ConfigHandler) importBackupAssetGraph(tx *gorm.DB, envelope configImportEnvelope, actorID uint) error {
	if tx == nil {
		return fmt.Errorf("%w: import transaction is required", errConfigAssetGraphInvalid)
	}
	if len(envelope.Graph.BackupRetentionPolicies) > 0 && actorID == 0 {
		return fmt.Errorf("%w: import actor is required", errConfigAssetGraphInvalid)
	}
	now := time.Now().UTC()
	repositoryIDs := make(map[string]string, len(envelope.Graph.BackupRepositories))
	for _, repository := range envelope.Graph.BackupRepositories {
		localID, err := h.importConfigAssetRepository(tx, envelope.DocumentID, repository, now)
		if err != nil {
			return err
		}
		repositoryIDs[repository.RepositoryRef] = localID
	}

	linkIDs := make(map[string]string, len(envelope.Graph.TaskRepositoryLinks))
	for _, link := range envelope.Graph.TaskRepositoryLinks {
		localID, err := h.importConfigAssetLink(tx, envelope.DocumentID, link, repositoryIDs[link.RepositoryRef], now)
		if err != nil {
			return err
		}
		linkIDs[link.LinkRef] = localID
	}

	for _, policy := range envelope.Graph.BackupRetentionPolicies {
		scopeID := repositoryIDs[policy.RepositoryRef]
		if policy.ScopeKind == "task_link" {
			scopeID = linkIDs[policy.LinkRef]
		}
		if _, err := h.importConfigAssetPolicy(tx, envelope.DocumentID, policy, scopeID, actorID, now); err != nil {
			return err
		}
	}
	return nil
}

func (h *ConfigHandler) importConfigAssetRepository(tx *gorm.DB, documentID string, repository configAssetRepositoryExport, now time.Time) (string, error) {
	storedIdentity := configAssetStoredIdentity(repository.IdentityRef)
	if localID, found, err := lookupConfigImportRef(tx, documentID, repository.RepositoryRef, configAssetEntityRepository); err != nil {
		return "", err
	} else if found {
		var local model.BackupRepository
		if err := tx.Where("id = ?", localID).First(&local).Error; err != nil {
			return "", fmt.Errorf("%w: mapped repository is missing", errConfigAssetGraphConflict)
		}
		if !configAssetIdentityMatches(local.RepositoryIdentity, repository.IdentityRef) {
			return "", fmt.Errorf("%w: repository identity mapping changed", errConfigAssetGraphConflict)
		}
		if local.ProviderKind != repository.ProviderKind || local.VersionMode != repository.VersionMode ||
			local.ImmutabilityLevel != repository.ImmutabilityLevel {
			return "", fmt.Errorf("%w: repository identity mapping changed", errConfigAssetGraphConflict)
		}
		if repository.AccessBinding != nil {
			if err := importConfigAssetBinding(tx, local.ID, repository.AccessBinding, now); err != nil {
				return "", err
			}
		}
		return local.ID, nil
	}

	if repository.IdentityRef != "" {
		existing, found, err := findRepositoryByImportedIdentity(tx, repository.ProviderKind, repository.IdentityRef)
		if err != nil {
			return "", err
		}
		if found && existing.ID != "" {
			return "", fmt.Errorf("%w: repository identity already exists", errConfigAssetGraphConflict)
		}
	}

	localID, err := backupasset.NewOpaqueID()
	if err != nil {
		return "", err
	}
	record := model.BackupRepository{
		ID:                 localID,
		ProviderKind:       repository.ProviderKind,
		RepositoryIdentity: storedIdentity,
		DisplayName:        repository.DisplayName,
		Description:        repository.Description,
		VersionMode:        repository.VersionMode,
		Status:             string(backupasset.RepositoryDisconnected),
		CapabilityRevision: 1,
		CapabilitiesJSON:   "{}",
		ImmutabilityLevel:  repository.ImmutabilityLevel,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := tx.Create(&record).Error; err != nil {
		return "", err
	}
	if err := persistConfigImportRef(tx, documentID, repository.RepositoryRef, configAssetEntityRepository, localID, now); err != nil {
		return "", err
	}
	if repository.AccessBinding != nil {
		if err := importConfigAssetBinding(tx, localID, repository.AccessBinding, now); err != nil {
			return "", err
		}
	}
	return localID, nil
}

func (h *ConfigHandler) importConfigAssetLink(tx *gorm.DB, documentID string, link configAssetLinkExport, repositoryID string, now time.Time) (string, error) {
	taskName, nodeName, taskID, nodeID, err := resolveConfigAssetLinkTask(tx, link)
	if err != nil {
		return "", err
	}
	if localID, found, err := lookupConfigImportRef(tx, documentID, link.LinkRef, configAssetEntityTaskLink); err != nil {
		return "", err
	} else if found {
		var local model.TaskRepositoryLink
		if err := tx.Where("id = ?", localID).First(&local).Error; err != nil {
			return "", fmt.Errorf("%w: mapped link is missing", errConfigAssetGraphConflict)
		}
		if local.RepositoryID != repositoryID || local.PublicationMode != link.PublicationMode ||
			local.TaskNameSnapshot != taskName || local.NodeNameSnapshot != nodeName {
			return "", fmt.Errorf("%w: link mapping changed", errConfigAssetGraphConflict)
		}
		if local.UnlinkedAt != nil {
			return "", fmt.Errorf("%w: mapped link is no longer active", errConfigAssetGraphInvalid)
		}
		if local.TaskID != nil {
			var mappedTask model.Task
			if err := tx.Where("id = ?", *local.TaskID).First(&mappedTask).Error; err != nil {
				return "", fmt.Errorf("%w: mapped link task is missing", errConfigAssetGraphConflict)
			}
			if mappedTask.ArchivedAt != nil {
				return "", fmt.Errorf("%w: mapped link task is archived", errConfigAssetGraphInvalid)
			}
		}
		return local.ID, nil
	}
	if repositoryID == "" {
		return "", fmt.Errorf("%w: link repository is missing", errConfigAssetGraphInvalid)
	}
	if taskID != nil {
		var task model.Task
		if err := tx.Where("id = ?", *taskID).First(&task).Error; err != nil {
			return "", fmt.Errorf("%w: link task is missing", errConfigAssetGraphInvalid)
		}
		if task.ArchivedAt != nil {
			return "", fmt.Errorf("%w: archived task cannot receive an active repository link", errConfigAssetGraphInvalid)
		}
		var existing model.TaskRepositoryLink
		result := tx.Where("task_id = ? AND unlinked_at IS NULL", *taskID).Limit(1).Find(&existing)
		if result.Error != nil {
			return "", result.Error
		}
		if result.RowsAffected > 0 {
			return "", fmt.Errorf("%w: task already has an active repository link", errConfigAssetGraphConflict)
		}
	}
	localID, err := backupasset.NewOpaqueID()
	if err != nil {
		return "", err
	}
	record := model.TaskRepositoryLink{
		ID:               localID,
		TaskID:           taskID,
		RepositoryID:     repositoryID,
		TaskNameSnapshot: taskName,
		NodeIDSnapshot:   nodeID,
		NodeNameSnapshot: nodeName,
		PublicationMode:  link.PublicationMode,
		LinkedAt:         now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := tx.Create(&record).Error; err != nil {
		return "", err
	}
	if err := persistConfigImportRef(tx, documentID, link.LinkRef, configAssetEntityTaskLink, localID, now); err != nil {
		return "", err
	}
	return localID, nil
}

func (h *ConfigHandler) importConfigAssetPolicy(tx *gorm.DB, documentID string, policy configAssetPolicyExport, scopeID string, actorID uint, now time.Time) (string, error) {
	canonical, err := retention.ParsePolicyRules(policy.RulesJSON)
	if err != nil {
		return "", fmt.Errorf("%w: policy rules are invalid", errConfigAssetGraphInvalid)
	}
	rulesJSON, _, err := retention.CanonicalizePolicyRules(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: policy rules are invalid", errConfigAssetGraphInvalid)
	}
	if localID, found, err := lookupConfigImportRef(tx, documentID, policy.PolicyRef, configAssetEntityRetentionPolicy); err != nil {
		return "", err
	} else if found {
		var local model.BackupRetentionPolicy
		if err := tx.Where("id = ?", localID).First(&local).Error; err != nil {
			return "", fmt.Errorf("%w: mapped policy is missing", errConfigAssetGraphConflict)
		}
		if local.ScopeKind != policy.ScopeKind || local.ScopeID != scopeID || local.RulesJSON != rulesJSON {
			return "", fmt.Errorf("%w: policy mapping changed", errConfigAssetGraphConflict)
		}
		if local.Status != "active" || local.DeletedAt != nil {
			return "", fmt.Errorf("%w: mapped policy is not active", errConfigAssetGraphInvalid)
		}
		if policy.Revision < 1 {
			return "", fmt.Errorf("%w: mapped policy revision is invalid", errConfigAssetGraphInvalid)
		}
		if policy.Status != "" && policy.Status != string(backupasset.RetentionPolicyActive) {
			return "", fmt.Errorf("%w: mapped policy status is invalid", errConfigAssetGraphInvalid)
		}
		expectedRevision := policy.Revision
		if local.Revision != expectedRevision {
			return "", fmt.Errorf("%w: mapped policy revision changed", errConfigAssetGraphConflict)
		}
		return local.ID, nil
	}
	if scopeID == "" || backupasset.ValidateOpaqueID(scopeID) != nil {
		return "", fmt.Errorf("%w: policy scope is missing", errConfigAssetGraphInvalid)
	}
	if actorID == 0 {
		return "", fmt.Errorf("%w: import actor is required", errConfigAssetGraphInvalid)
	}
	var existing model.BackupRetentionPolicy
	result := tx.Where("scope_kind = ? AND scope_id = ? AND status = ? AND deleted_at IS NULL", policy.ScopeKind, scopeID, "active").
		Limit(1).Find(&existing)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected > 0 {
		return "", fmt.Errorf("%w: retention policy scope already exists", errConfigAssetGraphConflict)
	}
	localID, err := backupasset.NewOpaqueID()
	if err != nil {
		return "", err
	}
	if policy.Revision < 1 {
		return "", fmt.Errorf("%w: policy revision is invalid", errConfigAssetGraphInvalid)
	}
	if policy.Status != "" && policy.Status != string(backupasset.RetentionPolicyActive) {
		return "", fmt.Errorf("%w: policy status is invalid", errConfigAssetGraphInvalid)
	}
	record := model.BackupRetentionPolicy{
		ID:        localID,
		ScopeKind: policy.ScopeKind,
		ScopeID:   scopeID,
		Revision:  policy.Revision,
		RulesJSON: rulesJSON,
		Status:    string(backupasset.RetentionPolicyActive),
		CreatedBy: actorID,
		UpdatedBy: actorID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := tx.Create(&record).Error; err != nil {
		return "", err
	}
	if err := persistConfigImportRef(tx, documentID, policy.PolicyRef, configAssetEntityRetentionPolicy, localID, now); err != nil {
		return "", err
	}
	return localID, nil
}

func importConfigAssetBinding(tx *gorm.DB, repositoryID string, envelope *configAssetBindingEnvelope, now time.Time) error {
	plaintext, fingerprint, err := configAssetBindingPlaintextAndFingerprint(envelope)
	if err != nil {
		return err
	}
	var existing int64
	if err := tx.Model(&model.RepositoryAccessBinding{}).Where("repository_id = ?", repositoryID).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		var current model.RepositoryAccessBinding
		if err := tx.Where("repository_id = ?", repositoryID).Order("id").First(&current).Error; err != nil {
			return err
		}
		if current.BindingKind != envelope.BindingKind || current.ConfigFingerprint != fingerprint {
			return fmt.Errorf("%w: binding mapping changed", errConfigAssetGraphConflict)
		}
		return nil
	}
	localID, err := backupasset.NewOpaqueID()
	if err != nil {
		return err
	}
	revokedAt := now
	return tx.Create(&model.RepositoryAccessBinding{
		ID:                localID,
		RepositoryID:      repositoryID,
		BindingKind:       envelope.BindingKind,
		EncryptedConfig:   plaintext,
		ConfigFingerprint: fingerprint,
		Status:            configAssetBindingStatusRevoked,
		RevokedAt:         &revokedAt,
		CreatedAt:         now,
		UpdatedAt:         now,
	}).Error
}

func resolveConfigAssetLinkTask(tx *gorm.DB, link configAssetLinkExport) (string, string, *uint, uint, error) {
	taskName, nodeName, _ := parseConfigAssetTaskRef(link.TaskRef)
	if link.TaskNameSnapshot != "" {
		taskName = link.TaskNameSnapshot
	}
	if link.NodeNameSnapshot != "" {
		nodeName = link.NodeNameSnapshot
	}
	var nodeID uint
	var taskID *uint
	if nodeName == "" {
		return taskName, nodeName, nil, 0, nil
	}
	var node model.Node
	result := tx.Where("name = ?", nodeName).Limit(1).Find(&node)
	if result.Error != nil {
		return "", "", nil, 0, result.Error
	}
	if result.RowsAffected == 0 {
		return taskName, nodeName, nil, 0, nil
	}
	nodeID = node.ID
	if taskName == "" {
		return taskName, nodeName, nil, nodeID, nil
	}
	var task model.Task
	taskResult := tx.Where("name = ? AND node_id = ?", taskName, node.ID).Limit(1).Find(&task)
	if taskResult.Error != nil {
		return "", "", nil, 0, taskResult.Error
	}
	if taskResult.RowsAffected > 0 {
		taskID = &task.ID
	}
	return taskName, nodeName, taskID, nodeID, nil
}

func lookupConfigImportRef(tx *gorm.DB, documentID, sourceRef, entityKind string) (string, bool, error) {
	var record model.BackupAssetConfigImportRef
	result := tx.Where(
		"source_document_id = ? AND source_reference = ? AND entity_kind = ?",
		documentID, sourceRef, entityKind,
	).Limit(1).Find(&record)
	if result.Error != nil {
		return "", false, result.Error
	}
	if result.RowsAffected == 0 {
		return "", false, nil
	}
	return record.LocalEntityID, true, nil
}

func persistConfigImportRef(tx *gorm.DB, documentID, sourceRef, entityKind, localID string, now time.Time) error {
	id, err := backupasset.NewOpaqueID()
	if err != nil {
		return err
	}
	return tx.Create(&model.BackupAssetConfigImportRef{
		ID:               id,
		SourceDocumentID: documentID,
		SourceReference:  sourceRef,
		EntityKind:       entityKind,
		LocalEntityID:    localID,
		CreatedAt:        now,
	}).Error
}

func configAssetIdentityRef(identity *string) string {
	if identity == nil {
		return ""
	}
	value := strings.TrimSpace(*identity)
	if value == "" {
		return ""
	}
	if digest, ok := strings.CutPrefix(value, backupasset.ImportedRepositoryIdentityRefPrefix); ok {
		return digest
	}
	return backupasset.ImportedIdentityRef(value)
}

func configAssetStoredIdentity(identityRef string) *string {
	identityRef = strings.TrimSpace(identityRef)
	if identityRef == "" {
		return nil
	}
	stored := backupasset.FormatImportedRepositoryIdentity(identityRef)
	return &stored
}

func configAssetIdentityMatches(local *string, identityRef string) bool {
	identityRef = strings.TrimSpace(identityRef)
	if identityRef == "" {
		return local == nil || strings.TrimSpace(*local) == ""
	}
	if local == nil || strings.TrimSpace(*local) == "" {
		return false
	}
	current := strings.TrimSpace(*local)
	if current == backupasset.FormatImportedRepositoryIdentity(identityRef) {
		return true
	}
	return backupasset.ImportedIdentityRef(current) == identityRef
}

func findRepositoryByImportedIdentity(tx *gorm.DB, providerKind, identityRef string) (model.BackupRepository, bool, error) {
	placeholder := backupasset.FormatImportedRepositoryIdentity(identityRef)
	var byPlaceholder model.BackupRepository
	result := tx.Where("provider_kind = ? AND repository_identity = ?", providerKind, placeholder).Limit(1).Find(&byPlaceholder)
	if result.Error != nil {
		return model.BackupRepository{}, false, result.Error
	}
	if result.RowsAffected > 0 {
		return byPlaceholder, true, nil
	}
	var candidates []model.BackupRepository
	if err := tx.Where("provider_kind = ? AND repository_identity IS NOT NULL", providerKind).Find(&candidates).Error; err != nil {
		return model.BackupRepository{}, false, err
	}
	for _, candidate := range candidates {
		if configAssetIdentityMatches(candidate.RepositoryIdentity, identityRef) {
			return candidate, true, nil
		}
	}
	return model.BackupRepository{}, false, nil
}

func configImportActorID(c *gin.Context) uint {
	if c == nil {
		return 0
	}
	for _, key := range []string{"user_id", "userID"} {
		raw, ok := c.Get(key)
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case uint:
			if value > 0 {
				return value
			}
		case int:
			if value > 0 {
				return uint(value)
			}
		}
	}
	return 0
}

func configAssetTaskRef(taskName, nodeName string) string {
	return configAssetTaskRefPrefix + strings.TrimSpace(taskName) + configAssetTaskRefSeparator + strings.TrimSpace(nodeName)
}

func parseConfigAssetTaskRef(value string) (string, string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(value), configAssetTaskRefPrefix)
	if !ok {
		return "", "", false
	}
	taskName, nodeName, ok := strings.Cut(rest, configAssetTaskRefSeparator)
	taskName = strings.TrimSpace(taskName)
	nodeName = strings.TrimSpace(nodeName)
	return taskName, nodeName, ok && taskName != "" && nodeName != ""
}

func validConfigAssetRef(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, cursor := range value {
		if cursor < '0' || cursor > '9' {
			return true
		}
	}
	return false
}

func validConfigAssetProviderKind(value string) bool {
	switch backupasset.ProviderKind(value) {
	case backupasset.ProviderRestic, backupasset.ProviderRsync, backupasset.ProviderRclone:
		return true
	default:
		return false
	}
}

func validConfigAssetPublicationMode(value string) bool {
	switch backupasset.TaskPublicationMode(value) {
	case backupasset.PublicationLegacyMutable, backupasset.PublicationVersionedHardlink,
		backupasset.PublicationVersionedFullCopy, backupasset.PublicationVersionedPrefix,
		backupasset.PublicationNativeObjectVersions, backupasset.PublicationNativeSnapshot:
		return true
	default:
		return false
	}
}

func validConfigAssetBindingKind(value string) bool {
	switch strings.TrimSpace(value) {
	case "task_derived_v1", "managed_rsync_v2", "managed_rclone_v3":
		return true
	default:
		return false
	}
}

func configAssetBindingPlaintextAndFingerprint(envelope *configAssetBindingEnvelope) (string, string, error) {
	if envelope == nil {
		return "", "", fmt.Errorf("%w: binding envelope is missing", errConfigAssetGraphInvalid)
	}
	plaintext, err := secure.DecryptIfNeeded(envelope.Envelope)
	if err != nil {
		return "", "", fmt.Errorf("%w: binding envelope is unreadable", errConfigAssetGraphInvalid)
	}
	salt, err := identitySaltFromBindingPlaintext(plaintext)
	if err != nil {
		return "", "", err
	}
	computed, err := provider.DeriveConfigFingerprint(salt, []byte(plaintext))
	if err != nil {
		return "", "", fmt.Errorf("%w: binding fingerprint is unavailable", errConfigAssetGraphInvalid)
	}
	claimed := strings.TrimSpace(envelope.ConfigFingerprint)
	if claimed == "" || claimed != computed {
		return "", "", fmt.Errorf("%w: binding fingerprint does not match envelope", errConfigAssetGraphInvalid)
	}
	return plaintext, computed, nil
}

func identitySaltFromBindingPlaintext(plaintext string) ([]byte, error) {
	var document struct {
		IdentitySalt string `json:"identity_salt"`
	}
	if err := json.Unmarshal([]byte(plaintext), &document); err != nil {
		return nil, fmt.Errorf("%w: binding envelope is not a binding document", errConfigAssetGraphInvalid)
	}
	salt, err := hex.DecodeString(strings.TrimSpace(document.IdentitySalt))
	if err != nil || len(salt) != provider.IdentitySaltBytes {
		return nil, fmt.Errorf("%w: binding identity salt is invalid", errConfigAssetGraphInvalid)
	}
	return salt, nil
}

func validateConfigAssetRepositoryIdentity(repository configAssetRepositoryExport) error {
	providerKind := backupasset.ProviderKind(repository.ProviderKind)
	version := backupasset.VersionMode(repository.VersionMode)
	immutability := backupasset.ImmutabilityLevel(repository.ImmutabilityLevel)
	switch providerKind {
	case backupasset.ProviderRestic:
		if version != backupasset.VersionNativeSnapshot {
			return fmt.Errorf("%w: restic version_mode is incompatible", errConfigAssetGraphInvalid)
		}
	case backupasset.ProviderRclone:
		switch version {
		case backupasset.VersionMutableHead:
			if immutability != backupasset.ImmutabilityMutable {
				return fmt.Errorf("%w: rclone immutability is incompatible", errConfigAssetGraphInvalid)
			}
		case backupasset.VersionVersionedPrefix, backupasset.VersionNativeObjectVersions:
			if immutability != backupasset.ImmutabilityXirangManaged &&
				immutability != backupasset.ImmutabilityBackendVersioned &&
				immutability != backupasset.ImmutabilityStorageWORM {
				return fmt.Errorf("%w: rclone immutability is incompatible", errConfigAssetGraphInvalid)
			}
		default:
			return fmt.Errorf("%w: rclone version_mode is incompatible", errConfigAssetGraphInvalid)
		}
	case backupasset.ProviderRsync:
		switch version {
		case backupasset.VersionMutableHead:
			if immutability != backupasset.ImmutabilityMutable {
				return fmt.Errorf("%w: rsync immutability is incompatible", errConfigAssetGraphInvalid)
			}
		case backupasset.VersionHardlinkTree, backupasset.VersionFullCopyTree:
			if immutability != backupasset.ImmutabilityXirangManaged && immutability != backupasset.ImmutabilityStorageWORM {
				return fmt.Errorf("%w: rsync immutability is incompatible", errConfigAssetGraphInvalid)
			}
		default:
			return fmt.Errorf("%w: rsync version_mode is incompatible", errConfigAssetGraphInvalid)
		}
	}
	return nil
}

func validateConfigAssetCompatibleGraph(repository configAssetRepositoryExport, link configAssetLinkExport) error {
	providerKind := backupasset.ProviderKind(repository.ProviderKind)
	publication := backupasset.TaskPublicationMode(link.PublicationMode)
	switch providerKind {
	case backupasset.ProviderRestic:
		if publication == backupasset.PublicationNativeObjectVersions ||
			publication == backupasset.PublicationVersionedPrefix ||
			publication == backupasset.PublicationVersionedHardlink ||
			publication == backupasset.PublicationVersionedFullCopy {
			return fmt.Errorf("%w: restic publication is incompatible", errConfigAssetGraphInvalid)
		}
	case backupasset.ProviderRclone:
		if publication == backupasset.PublicationNativeSnapshot ||
			publication == backupasset.PublicationVersionedHardlink ||
			publication == backupasset.PublicationVersionedFullCopy {
			return fmt.Errorf("%w: rclone publication is incompatible", errConfigAssetGraphInvalid)
		}
	case backupasset.ProviderRsync:
		if publication == backupasset.PublicationNativeSnapshot ||
			publication == backupasset.PublicationNativeObjectVersions ||
			publication == backupasset.PublicationVersionedPrefix {
			return fmt.Errorf("%w: rsync publication is incompatible", errConfigAssetGraphInvalid)
		}
	}
	if repository.AccessBinding == nil {
		return nil
	}
	switch providerKind {
	case backupasset.ProviderRestic:
		if repository.AccessBinding.BindingKind != "task_derived_v1" {
			return fmt.Errorf("%w: restic binding_kind is incompatible", errConfigAssetGraphInvalid)
		}
	case backupasset.ProviderRclone:
		if repository.AccessBinding.BindingKind != "managed_rclone_v3" {
			return fmt.Errorf("%w: rclone binding_kind is incompatible", errConfigAssetGraphInvalid)
		}
	case backupasset.ProviderRsync:
		if repository.AccessBinding.BindingKind != "managed_rsync_v2" {
			return fmt.Errorf("%w: rsync binding_kind is incompatible", errConfigAssetGraphInvalid)
		}
	default:
		return fmt.Errorf("%w: provider/binding combination is incompatible", errConfigAssetGraphInvalid)
	}
	return nil
}

func validConfigAssetVersionMode(value string) bool {
	switch backupasset.VersionMode(value) {
	case backupasset.VersionNativeSnapshot, backupasset.VersionHardlinkTree, backupasset.VersionFullCopyTree,
		backupasset.VersionVersionedPrefix, backupasset.VersionNativeObjectVersions, backupasset.VersionMutableHead:
		return true
	default:
		return false
	}
}

func validConfigAssetImmutability(value string) bool {
	switch backupasset.ImmutabilityLevel(value) {
	case backupasset.ImmutabilityMutable, backupasset.ImmutabilityXirangManaged,
		backupasset.ImmutabilityBackendVersioned, backupasset.ImmutabilityStorageWORM:
		return true
	default:
		return false
	}
}

func validConfigAssetIdentityRef(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != 64 {
		return false
	}
	for _, cursor := range value {
		if (cursor < '0' || cursor > '9') && (cursor < 'a' || cursor > 'f') {
			return false
		}
	}
	return true
}
