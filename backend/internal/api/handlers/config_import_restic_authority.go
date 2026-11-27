package handlers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/model"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"

	"gorm.io/gorm"
)

// Only material link facts are retained. Names and update timestamps are not
// access authority, and the locator digest keeps its value out of the journal.
type configImportResticLinkIdentity struct {
	taskID      uint
	nodeID      uint
	repository  string
	publication string
	locator     [32]byte
	linkedAt    string
}

func configImportResticIdentity(link model.TaskRepositoryLink) (configImportResticLinkIdentity, error) {
	if link.TaskID == nil {
		return configImportResticLinkIdentity{}, fmt.Errorf("config import rollback asset link identity is invalid")
	}
	locator, err := secure.DecryptIfNeeded(link.EncryptedLegacyLocator)
	if err != nil {
		return configImportResticLinkIdentity{}, fmt.Errorf("config import rollback asset link locator is invalid")
	}
	return configImportResticLinkIdentity{
		taskID: *link.TaskID, nodeID: link.NodeIDSnapshot, repository: link.RepositoryID,
		publication: link.PublicationMode, locator: sha256.Sum256([]byte(strings.TrimSpace(locator))),
		linkedAt: link.LinkedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}, nil
}

func configImportFieldsDiffer(prior, current map[string]any, fields ...string) bool {
	for _, field := range fields {
		if rawConfigImportString(prior[field]) != rawConfigImportString(current[field]) {
			return true
		}
	}
	return false
}

var configImportResticNodeFields = []string{"host", "port", "username", "auth_type", "password", "private_key", "ssh_key_id", "tags"}
var configImportResticKeyFields = []string{"private_key", "disabled", "expires_at", "allowed_purposes", "allowed_node_ids", "allowed_node_tags"}

// Key-only imports affect precisely the nodes using a restored key; do not
// inspect unrelated nodes or their repository bindings.
func (snapshot *configImportRollbackSnapshot) configImportAssetRollbackScope(ctx context.Context, tx *gorm.DB) ([]any, []any, error) {
	taskIDs := make([]any, 0, len(snapshot.tasks.prior))
	nodeIDs := make([]any, 0, len(snapshot.nodes.prior))
	for _, row := range snapshot.tasks.prior {
		taskIDs = append(taskIDs, row["id"])
	}
	for _, row := range snapshot.nodes.prior {
		nodeIDs = append(nodeIDs, row["id"])
	}
	keyIDs := make([]any, 0, len(snapshot.sshKeys.prior))
	for key, prior := range snapshot.sshKeys.prior {
		if current, ok := snapshot.sshKeys.current[key]; ok && configImportFieldsDiffer(prior, current, configImportResticKeyFields...) {
			keyIDs = append(keyIDs, prior["id"])
		}
	}
	if len(keyIDs) != 0 {
		var nodes []struct{ ID uint }
		query := tx.WithContext(ctx).Table("nodes").Select("id").Where("ssh_key_id IN ? AND LOWER(TRIM(auth_type)) = ?", keyIDs, "key").Order("id")
		if err := lockConfigImportAssetQuery(query).Find(&nodes).Error; err != nil {
			return nil, nil, fmt.Errorf("config import rollback asset node lock: %w", err)
		}
		for _, node := range nodes {
			nodeIDs = append(nodeIDs, node.ID)
		}
	}
	return taskIDs, nodeIDs, nil
}

// Capture active native Restic links under the same import transaction as the
// classic seal. Only tasks whose access could be restored need a baseline.
func (snapshot *configImportRollbackSnapshot) captureSealedResticLinks(ctx context.Context, tx *gorm.DB) error {
	snapshot.sealedResticLinks = make(map[string]configImportResticLinkIdentity)
	taskIDs := make([]any, 0, len(snapshot.tasks.prior))
	for key, prior := range snapshot.tasks.prior {
		current, ok := snapshot.tasks.current[key]
		if !ok {
			continue
		}
		priorProvider := strings.ToLower(strings.TrimSpace(rawConfigImportString(prior["executor_type"])))
		currentProvider := strings.ToLower(strings.TrimSpace(rawConfigImportString(current["executor_type"])))
		if (priorProvider == "restic" || currentProvider == "restic") && configImportFieldsDiffer(prior, current, "node_id", "executor_type", "rsync_target", "executor_config") {
			taskIDs = append(taskIDs, prior["id"])
		}
	}
	nodeIDs := make([]any, 0, len(snapshot.nodes.prior))
	for key, prior := range snapshot.nodes.prior {
		if current, ok := snapshot.nodes.current[key]; ok && configImportFieldsDiffer(prior, current, configImportResticNodeFields...) {
			nodeIDs = append(nodeIDs, prior["id"])
		}
	}
	keyIDs := make([]any, 0, len(snapshot.sshKeys.prior))
	for key, prior := range snapshot.sshKeys.prior {
		if current, ok := snapshot.sshKeys.current[key]; ok && configImportFieldsDiffer(prior, current, configImportResticKeyFields...) {
			keyIDs = append(keyIDs, prior["id"])
		}
	}
	if len(keyIDs) != 0 {
		var nodes []struct{ ID uint }
		if err := tx.WithContext(ctx).Table("nodes").Select("id").Where("ssh_key_id IN ? AND LOWER(TRIM(auth_type)) = ?", keyIDs, "key").Find(&nodes).Error; err != nil {
			return fmt.Errorf("config import rollback asset node query: %w", err)
		}
		for _, node := range nodes {
			nodeIDs = append(nodeIDs, node.ID)
		}
	}
	if len(taskIDs) == 0 && len(nodeIDs) == 0 {
		return nil
	}
	// Avoid touching asset tables for changed nodes with no Restic tasks.
	taskQuery := tx.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&model.Task{}).Select("id")
	switch {
	case len(taskIDs) != 0 && len(nodeIDs) != 0:
		taskQuery = taskQuery.Where("id IN ? OR (node_id IN ? AND LOWER(TRIM(executor_type)) = ?)", taskIDs, nodeIDs, "restic")
	case len(taskIDs) != 0:
		taskQuery = taskQuery.Where("id IN ?", taskIDs)
	default:
		taskQuery = taskQuery.Where("node_id IN ? AND LOWER(TRIM(executor_type)) = ?", nodeIDs, "restic")
	}
	var affectedTasks []model.Task
	if err := taskQuery.Find(&affectedTasks).Error; err != nil {
		return fmt.Errorf("config import rollback asset task query: %w", err)
	}
	if len(affectedTasks) == 0 {
		return nil
	}
	affectedIDs := make([]uint, 0, len(affectedTasks))
	for _, task := range affectedTasks {
		affectedIDs = append(affectedIDs, task.ID)
	}
	query := tx.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&model.TaskRepositoryLink{}).
		Select("task_repository_links.*").Joins("JOIN backup_repositories ON backup_repositories.id = task_repository_links.repository_id").
		Where("task_repository_links.task_id IN ? AND task_repository_links.unlinked_at IS NULL AND task_repository_links.publication_mode = ? AND backup_repositories.provider_kind = ?", affectedIDs, string(backupasset.PublicationNativeSnapshot), string(backupasset.ProviderRestic))
	var links []model.TaskRepositoryLink
	if err := query.Order("task_repository_links.id").Find(&links).Error; err != nil {
		return fmt.Errorf("config import rollback asset link query: %w", err)
	}
	for _, link := range links {
		identity, err := configImportResticIdentity(link)
		if err != nil {
			return err
		}
		snapshot.sealedResticLinks[link.ID] = identity
	}
	return nil
}

func (snapshot *configImportRollbackSnapshot) validateNewResticLinkAccess(ctx context.Context, tx *gorm.DB, link model.TaskRepositoryLink, task model.Task) error {
	identity, err := configImportResticIdentity(link)
	if err != nil {
		return err
	}
	if sealed, ok := snapshot.sealedResticLinks[link.ID]; ok && sealed == identity {
		return nil
	}
	if prior, ok := snapshot.tasks.prior[rawConfigImportKey(task.ID)]; ok {
		if current, ok := snapshot.tasks.current[rawConfigImportKey(task.ID)]; ok {
			beforeLocator := strings.TrimSpace(rawConfigImportString(prior["rsync_target"]))
			afterLocator := strings.TrimSpace(rawConfigImportString(current["rsync_target"]))
			if beforeLocator != afterLocator && (beforeLocator == "" || identity.locator != sha256.Sum256([]byte(beforeLocator))) {
				return fmt.Errorf("config import rollback asset link locator is incompatible")
			}
			before, after := rawConfigImportString(prior["executor_config"]), rawConfigImportString(current["executor_config"])
			if before != after {
				before, err = secure.DecryptIfNeeded(before)
				if err != nil {
					return fmt.Errorf("config import rollback asset task access is invalid")
				}
				after, err = secure.DecryptIfNeeded(after)
				if err != nil {
					return fmt.Errorf("config import rollback asset task access is invalid")
				}
				beforeSecret, beforeErr := configImportResticBindingSecret(before)
				afterSecret, afterErr := configImportResticBindingSecret(after)
				if beforeErr != nil || afterErr != nil {
					return fmt.Errorf("config import rollback asset task access is invalid")
				}
				if beforeSecret != afterSecret {
					return fmt.Errorf("config import rollback asset link access is incompatible")
				}
			}
		}
	}
	before, after, err := snapshot.configImportResticNodeAccess(ctx, tx, task.NodeID)
	if err != nil {
		return err
	}
	beforeCredential, afterCredential := before.credential, after.credential
	before.credential, after.credential = "", ""
	sameCredential := beforeCredential == afterCredential
	if !sameCredential && before.auth == "key" && after.auth == "key" {
		sameCredential = sshutil.SamePrivateKeyMaterial(beforeCredential, afterCredential)
	}
	if before != after || !sameCredential {
		return fmt.Errorf("config import rollback asset link access is incompatible")
	}
	return nil
}

type configImportResticSSHAccess struct {
	host, port, username, auth, credential string
	disabled, expires, purposes            string
	nodeAllowed                            bool
}

func (snapshot *configImportRollbackSnapshot) configImportResticNodeAccess(ctx context.Context, tx *gorm.DB, nodeID uint) (configImportResticSSHAccess, configImportResticSSHAccess, error) {
	var before, after configImportResticSSHAccess
	key := rawConfigImportKey(nodeID)
	prior, restored := snapshot.nodes.prior[key]
	current, sealed := snapshot.nodes.current[key]
	if !restored || !sealed {
		// No node restoration and no restored keys means identical access.
		if len(snapshot.sshKeys.prior) == 0 {
			return before, after, nil
		}
		var row map[string]any
		query := tx.WithContext(ctx).Table("nodes").Select("id", "host", "port", "username", "auth_type", "password", "private_key", "ssh_key_id", "tags").Where("id = ?", nodeID)
		if err := lockConfigImportAssetQuery(query).Take(&row).Error; err != nil {
			return before, after, fmt.Errorf("config import rollback asset node is unavailable")
		}
		prior, current = row, row
	}
	if !configImportFieldsDiffer(prior, current, configImportResticNodeFields...) {
		keyID, bound := configImportRollbackUint(current["ssh_key_id"])
		if strings.ToLower(strings.TrimSpace(rawConfigImportString(current["auth_type"]))) != "key" || !bound {
			return before, after, nil
		}
		priorKey, changed := snapshot.sshKeys.prior[rawConfigImportKey(keyID)]
		currentKey, sealedKey := snapshot.sshKeys.current[rawConfigImportKey(keyID)]
		if !changed || !sealedKey || !configImportFieldsDiffer(priorKey, currentKey, configImportResticKeyFields...) {
			return before, after, nil
		}
	}
	before, err := snapshot.configImportResticSSHInputs(ctx, tx, prior, true)
	if err != nil {
		return before, after, err
	}
	after, err = snapshot.configImportResticSSHInputs(ctx, tx, current, false)
	return before, after, err
}

func (snapshot *configImportRollbackSnapshot) configImportResticSSHInputs(ctx context.Context, tx *gorm.DB, node map[string]any, projected bool) (configImportResticSSHAccess, error) {
	access := configImportResticSSHAccess{
		host: rawConfigImportString(node["host"]), port: rawConfigImportString(node["port"]),
		username: strings.TrimSpace(rawConfigImportString(node["username"])),
		auth:     strings.ToLower(strings.TrimSpace(rawConfigImportString(node["auth_type"]))),
	}
	access.nodeAllowed = true
	if port, err := strconv.Atoi(access.port); err == nil && port == 0 {
		access.port = "22"
	}
	if access.username == "" {
		access.username = "root"
	}
	var raw string
	switch access.auth {
	case "password":
		raw = rawConfigImportString(node["password"])
	case "key":
		if node["ssh_key_id"] == nil {
			raw = rawConfigImportString(node["private_key"])
		} else {
			keyID, valid := configImportRollbackUint(node["ssh_key_id"])
			if !valid {
				return access, fmt.Errorf("config import rollback asset node access is invalid")
			}
			key := rawConfigImportKey(keyID)
			var row map[string]any
			var ok bool
			if projected {
				row, ok = snapshot.sshKeys.prior[key]
			} else {
				row, ok = snapshot.sshKeys.current[key]
			}
			if !ok {
				query := tx.WithContext(ctx).Table("ssh_keys").Select("id", "private_key", "disabled", "expires_at", "allowed_purposes", "allowed_node_ids", "allowed_node_tags").Where("id = ?", keyID)
				if err := lockConfigImportAssetQuery(query).Take(&row).Error; err != nil {
					return access, fmt.Errorf("config import rollback asset node access is invalid")
				}
			}
			raw = rawConfigImportString(row["private_key"])
			access.disabled = rawConfigImportString(row["disabled"])
			if access.disabled == "0" || access.disabled == "false" {
				access.disabled = ""
			}
			access.expires = rawConfigImportString(row["expires_at"])
			purposes := sshutil.NormalizeTagList(rawConfigImportString(row["allowed_purposes"]))
			if purposes != "" {
				items := strings.Split(purposes, ",")
				sort.Strings(items)
				access.purposes = strings.Join(items, ",")
			}
			nodeID, _ := configImportRollbackUint(node["id"])
			scopeKey := model.SSHKey{AllowedNodeIDs: rawConfigImportString(row["allowed_node_ids"]), AllowedNodeTags: rawConfigImportString(row["allowed_node_tags"])}
			scopeNode := model.Node{ID: nodeID, Tags: rawConfigImportString(node["tags"])}
			access.nodeAllowed = sshutil.ValidateSSHKeyScope(scopeKey, scopeNode, "") == nil
		}
	default:
		return access, nil
	}
	credential, err := secure.DecryptIfNeeded(raw)
	if err != nil {
		return access, fmt.Errorf("config import rollback asset node access is invalid")
	}
	if access.auth == "key" {
		credential = strings.TrimSpace(credential)
	}
	access.credential = credential
	return access, nil
}
