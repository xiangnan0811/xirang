package handlers

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xirang/backend/internal/model"
	nodePkg "xirang/backend/internal/node"
	"xirang/backend/internal/secure"
	"xirang/backend/internal/sshutil"
)

// importedNodeSSHKeyNameCandidate preserves the distinction between a missing
// field, an explicit clear, an unresolved null, and an invalid value. The
// import path uses these states to retain the legacy numeric-ID compatibility
// rules without treating source IDs as cross-database identity.
type importedNodeSSHKeyNameCandidate struct {
	present        bool
	name           string
	explicitNull   bool
	explicitEmpty  bool
	invalid        bool
	legacyConflict bool
}

// parseImportedNodeSSHKeyName parses the optional node-side name reference.
// A non-empty value is intentionally only normalized for surrounding
// whitespace; names remain exact and may contain commas.
func parseImportedNodeSSHKeyName(data map[string]interface{}) (importedNodeSSHKeyNameCandidate, string) {
	var candidate importedNodeSSHKeyNameCandidate
	raw, ok := data["ssh_key_name"]
	if !ok {
		return candidate, ""
	}
	candidate.present = true
	if raw == nil {
		candidate.explicitNull = true
		return candidate, configImportWarningUnresolvedSSHKey
	}
	value, ok := raw.(string)
	if !ok {
		candidate.invalid = true
		return candidate, configImportWarningInvalidReference
	}
	candidate.name = strings.TrimSpace(value)
	if candidate.name == "" {
		candidate.explicitEmpty = true
		candidate.legacyConflict = importedLegacyConfigReferencePresent(data, "ssh_key_id")
		if candidate.legacyConflict {
			return candidate, configImportWarningReferenceConflict
		}
		return candidate, ""
	}
	return candidate, ""
}

type importedSSHKeyAllowedNodeNamesCandidate struct {
	present        bool
	names          []string
	explicitNull   bool
	explicitEmpty  bool
	invalid        bool
	legacyConflict bool
}

// parseImportedSSHKeyAllowedNodeNames parses the optional name-based node
// scope. It never treats a comma as a separator: commas are valid in names.
func parseImportedSSHKeyAllowedNodeNames(data map[string]interface{}) (importedSSHKeyAllowedNodeNamesCandidate, string) {
	var candidate importedSSHKeyAllowedNodeNamesCandidate
	raw, ok := data["allowed_node_names"]
	if !ok {
		return candidate, ""
	}
	candidate.present = true
	if raw == nil {
		candidate.explicitNull = true
		return candidate, configImportWarningUnresolvedNodeScope
	}

	var values []string
	switch typed := raw.(type) {
	case []interface{}:
		values = make([]string, len(typed))
		for i, item := range typed {
			value, ok := item.(string)
			if !ok {
				candidate.invalid = true
				return candidate, configImportWarningInvalidReference
			}
			values[i] = value
		}
	case []string:
		values = typed
	default:
		candidate.invalid = true
		return candidate, configImportWarningInvalidReference
	}

	candidate.names = make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" {
			candidate.invalid = true
			candidate.names = nil
			return candidate, configImportWarningInvalidReference
		}
		if _, duplicate := seen[name]; duplicate {
			candidate.invalid = true
			candidate.names = nil
			return candidate, configImportWarningInvalidReference
		}
		seen[name] = struct{}{}
		candidate.names = append(candidate.names, name)
	}
	if len(candidate.names) == 0 {
		candidate.explicitEmpty = true
		candidate.legacyConflict = importedLegacyConfigReferencePresent(data, "allowed_node_ids")
		if candidate.legacyConflict {
			return candidate, configImportWarningReferenceConflict
		}
	}
	return candidate, ""
}

// importedLegacyConfigReferencePresent reports whether an old reference field
// carries a non-empty value. Invalid legacy types remain present so the old
// field parser can emit its own invalid_scope warning instead of being hidden
// by the name parser.
func importedLegacyConfigReferencePresent(data map[string]interface{}, field string) bool {
	raw, ok := data[field]
	if !ok || raw == nil {
		return false
	}
	if field == "allowed_node_ids" {
		value, valid := raw.(string)
		if !valid {
			return true
		}
		normalized, err := sshutil.NormalizeNodeIDList(strings.TrimSpace(value))
		if err != nil {
			return true
		}
		return strings.TrimSpace(normalized) != ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case []interface{}:
		return len(value) > 0
	case []string:
		return len(value) > 0
	default:
		return true
	}
}

// configImportDuplicateNameIndexes returns all indexes belonging to a
// repeated, non-empty normalized entity name. Every member of an ambiguous
// input group is rejected by the import phase; no first-match guessing is
// allowed.
func configImportDuplicateNameIndexes(records []map[string]interface{}) map[int]bool {
	groups := make(map[string][]int)
	for index, record := range records {
		name, ok := record["name"].(string)
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		groups[name] = append(groups[name], index)
	}
	duplicates := make(map[int]bool)
	for _, indexes := range groups {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			duplicates[index] = true
		}
	}
	return duplicates
}

type configImportCredentialInventory struct {
	nodes       []model.Node
	keys        []model.SSHKey
	nodesByName map[string][]model.Node
	keysByName  map[string][]model.SSHKey
}

func lockConfigImportCredentialInventory(tx *gorm.DB, data configImportData) (*configImportCredentialInventory, error) {
	inventory := &configImportCredentialInventory{
		nodesByName: make(map[string][]model.Node),
		keysByName:  make(map[string][]model.SSHKey),
	}
	if tx == nil {
		return nil, fmt.Errorf("config import credential inventory transaction is unavailable")
	}
	if len(data.Nodes) == 0 && len(data.SSHKeys) == 0 {
		return inventory, nil
	}
	if strings.EqualFold(tx.Name(), "sqlite") {
		if err := tx.Exec("UPDATE nodes SET id = id WHERE 1 = 0").Error; err != nil {
			return nil, fmt.Errorf("保留导入节点写锁失败: %w", err)
		}
	}

	nodeQuery := tx.Session(&gorm.Session{SkipHooks: true})
	keyQuery := tx.Session(&gorm.Session{SkipHooks: true})
	if !strings.EqualFold(tx.Name(), "sqlite") {
		locking := clause.Locking{Strength: "UPDATE"}
		nodeQuery = nodeQuery.Clauses(locking)
		keyQuery = keyQuery.Clauses(locking)
	}
	if err := nodeQuery.Select("id", "name", "ssh_key_id").Order("id").Find(&inventory.nodes).Error; err != nil {
		return nil, fmt.Errorf("读取导入节点库存失败: %w", err)
	}
	if err := keyQuery.Order("id").Find(&inventory.keys).Error; err != nil {
		return nil, fmt.Errorf("读取导入 SSH 密钥库存失败: %w", err)
	}
	inventory.rebuildIndexes()
	return inventory, nil
}

func (inventory *configImportCredentialInventory) rebuildIndexes() {
	if inventory == nil {
		return
	}
	inventory.nodesByName = make(map[string][]model.Node, len(inventory.nodes))
	for _, node := range inventory.nodes {
		if !configExportNameRepresentable(node.Name) {
			continue
		}
		inventory.nodesByName[node.Name] = append(inventory.nodesByName[node.Name], node)
	}
	inventory.keysByName = make(map[string][]model.SSHKey, len(inventory.keys))
	for _, key := range inventory.keys {
		if !configExportNameRepresentable(key.Name) {
			continue
		}
		inventory.keysByName[key.Name] = append(inventory.keysByName[key.Name], key)
	}
}

func (inventory *configImportCredentialInventory) replaceNode(value model.Node) {
	if inventory == nil {
		return
	}
	for index, node := range inventory.nodes {
		if node.ID == value.ID {
			inventory.nodes[index] = value
			inventory.rebuildIndexes()
			return
		}
	}
	inventory.nodes = append(inventory.nodes, value)
	inventory.rebuildIndexes()
}

func (inventory *configImportCredentialInventory) replaceKey(value model.SSHKey) {
	if inventory == nil {
		return
	}
	for index, key := range inventory.keys {
		if key.ID == value.ID {
			inventory.keys[index] = value
			inventory.rebuildIndexes()
			return
		}
	}
	inventory.keys = append(inventory.keys, value)
	inventory.rebuildIndexes()
}

func (inventory *configImportCredentialInventory) nodeByName(name string) (model.Node, bool) {
	if inventory == nil {
		return model.Node{}, false
	}
	rows := inventory.nodesByName[strings.TrimSpace(name)]
	if len(rows) != 1 {
		return model.Node{}, false
	}
	return rows[0], true
}

func loadImportedNodeForOverwrite(tx *gorm.DB, id uint) (model.Node, error) {
	if tx == nil || id == 0 {
		return model.Node{}, fmt.Errorf("导入节点库存行不可用")
	}
	var node model.Node
	if err := tx.Session(&gorm.Session{SkipHooks: true}).Where("id = ?", id).First(&node).Error; err != nil {
		return model.Node{}, err
	}
	return node, nil
}

func decryptImportedNodeSecrets(node *model.Node) error {
	if node == nil {
		return fmt.Errorf("导入节点不可用")
	}
	if node.Password != "" {
		decrypted, err := secure.DecryptIfNeeded(node.Password)
		if err != nil {
			return err
		}
		node.Password = decrypted
	}
	if node.PrivateKey != "" {
		decrypted, err := secure.DecryptIfNeeded(node.PrivateKey)
		if err != nil {
			return err
		}
		node.PrivateKey = decrypted
	}
	return nil
}

func (inventory *configImportCredentialInventory) keyByName(name string) (model.SSHKey, bool) {
	if inventory == nil {
		return model.SSHKey{}, false
	}
	rows := inventory.keysByName[strings.TrimSpace(name)]
	if len(rows) != 1 {
		return model.SSHKey{}, false
	}
	return rows[0], true
}

func configImportDuplicateNameSet(records []map[string]interface{}) map[string]struct{} {
	duplicates := configImportDuplicateNameIndexes(records)
	names := make(map[string]struct{})
	for index := range duplicates {
		if name, ok := records[index]["name"].(string); ok {
			name = strings.TrimSpace(name)
			if name != "" {
				names[name] = struct{}{}
			}
		}
	}
	return names
}

type configImportNodeCandidate struct {
	index         int
	name          string
	source        map[string]interface{}
	row           model.Node
	existing      bool
	baseCreated   bool
	skipped       bool
	reference     importedNodeSSHKeyNameCandidate
	referenceCode string
	rejected      bool
}

type configImportSSHKeyCandidate struct {
	index             int
	name              string
	source            map[string]interface{}
	row               model.SSHKey
	existing          bool
	reference         importedSSHKeyAllowedNodeNamesCandidate
	referenceCode     string
	legacyScope       importedSSHKeyScopeCandidate
	legacyCode        string
	nameScopeResolved bool
	rejected          bool
}

func (a *configImportAccumulator) rejectedWithWarnings(entity string, index int, name string, codes ...string) {
	if a == nil {
		return
	}
	a.result.Rejected++
	seen := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		a.warning(entity, index, name, code)
	}
}

func importConfigNodesAndSSHKeys(
	tx *gorm.DB,
	data configImportData,
	conflict string,
	preRejected map[string]map[int]bool,
	accumulator *configImportAccumulator,
) error {
	if tx == nil {
		return fmt.Errorf("config import credential transaction is unavailable")
	}
	if accumulator == nil {
		return fmt.Errorf("config import accumulator is unavailable")
	}
	inventory := accumulator.credentialInventory
	if inventory == nil {
		var err error
		inventory, err = lockConfigImportCredentialInventory(tx, data)
		if err != nil {
			return err
		}
		accumulator.credentialInventory = inventory
	}

	duplicateNodeIndexes := configImportDuplicateNameIndexes(data.Nodes)
	duplicateKeyIndexes := configImportDuplicateNameIndexes(data.SSHKeys)
	duplicateNodeNames := configImportDuplicateNameSet(data.Nodes)
	duplicateKeyNames := configImportDuplicateNameSet(data.SSHKeys)
	nodeCandidates := make([]*configImportNodeCandidate, 0, len(data.Nodes))

	for nodeIndex, nodeData := range data.Nodes {
		name, _ := nodeData["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
			continue
		}
		if duplicateNodeIndexes[nodeIndex] {
			accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningDuplicateName)
			continue
		}

		reference, referenceCode := parseImportedNodeSSHKeyName(nodeData)
		targetRows := inventory.nodesByName[name]
		if len(targetRows) > 1 {
			accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningDuplicateName)
			continue
		}
		if len(targetRows) == 1 && conflict != "overwrite" {
			accumulator.skipped()
			nodeCandidates = append(nodeCandidates, &configImportNodeCandidate{
				index:         nodeIndex,
				name:          name,
				source:        nodeData,
				row:           targetRows[0],
				existing:      true,
				skipped:       true,
				reference:     reference,
				referenceCode: referenceCode,
			})
			continue
		}
		if preRejected[configImportEntityNodes][nodeIndex] {
			accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
			continue
		}
		if len(targetRows) == 1 {
			if referenceCode != "" && referenceCode != configImportWarningUnresolvedSSHKey {
				codes := importedNodeReferenceWarningCodes(reference, referenceCode, nodeData, false)
				accumulator.rejectedWithWarnings(configImportEntityNodes, nodeIndex, name, codes...)
				continue
			}
			if reference.explicitNull {
				accumulator.rejectedWithWarnings(configImportEntityNodes, nodeIndex, name, configImportWarningUnresolvedSSHKey)
				continue
			}
			candidate, err := loadImportedNodeForOverwrite(tx, targetRows[0].ID)
			if err != nil {
				return fmt.Errorf("读取导入节点失败: %w", err)
			}
			if err := decryptImportedNodeSecrets(&candidate); err != nil {
				accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
				continue
			}
			applyImportedNodeFields(&candidate, nodeData)
			if err := validateImportedNodeCandidate(candidate); err != nil {
				accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
				continue
			}
			nodeCandidates = append(nodeCandidates, &configImportNodeCandidate{
				index:         nodeIndex,
				name:          name,
				source:        nodeData,
				row:           candidate,
				existing:      true,
				reference:     reference,
				referenceCode: referenceCode,
			})
			continue
		}

		newNode := model.Node{
			Name:     name,
			Status:   "offline",
			Port:     22,
			AuthType: "key",
		}
		applyImportedNodeFields(&newNode, nodeData)
		if strings.TrimSpace(newNode.AuthType) == "" {
			newNode.AuthType = "key"
		}
		if password, ok := nodeData["password"].(string); ok {
			newNode.Password = password
		}
		if privateKey, ok := nodeData["private_key"].(string); ok {
			newNode.PrivateKey = privateKey
		}
		// The classic export intentionally omits backup_dir. Preserve P1's
		// empty-value behavior when a valid name has no canonical identifier.
		if strings.TrimSpace(newNode.BackupDir) == "" {
			newNode.BackupDir = nodePkg.SanitizeBackupDir(name)
		}
		if err := validateImportedNodeCandidate(newNode); err != nil {
			accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
			continue
		}
		// New nodes are staged unbound. The name reference is resolved only
		// after all keys have been parsed and written.
		newNode.SSHKeyID = nil
		if err := tx.Create(&newNode).Error; err != nil {
			return fmt.Errorf("创建导入节点失败: %w", err)
		}
		accumulator.recordCreatedID(configImportEntityNodes, newNode.ID)
		inventory.replaceNode(newNode)
		accumulator.created(configImportEntityNodes, false)
		nodeCandidates = append(nodeCandidates, &configImportNodeCandidate{
			index:         nodeIndex,
			name:          name,
			source:        nodeData,
			row:           newNode,
			baseCreated:   true,
			reference:     reference,
			referenceCode: referenceCode,
		})
	}

	for keyIndex, keyData := range data.SSHKeys {
		name, _ := keyData["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			accumulator.rejected(configImportEntitySSHKeys, keyIndex, name, configImportWarningInvalidInput)
			continue
		}
		if duplicateKeyIndexes[keyIndex] {
			accumulator.rejected(configImportEntitySSHKeys, keyIndex, name, configImportWarningDuplicateName)
			continue
		}

		reference, referenceCode := parseImportedSSHKeyAllowedNodeNames(keyData)
		legacyScope, legacyCode := parseImportedSSHKeyScope(keyData)
		if reference.present && referenceCode == "" && !reference.explicitNull && !reference.invalid {
			if legacyCode == configImportWarningUnresolvedNodeScope {
				legacyCode = ""
			}
			legacyScope.allowedNodeIDs = nil
		}
		targetRows := inventory.keysByName[name]
		if len(targetRows) > 1 {
			accumulator.rejected(configImportEntitySSHKeys, keyIndex, name, configImportWarningDuplicateName)
			continue
		}
		if len(targetRows) == 1 && conflict != "overwrite" {
			accumulator.skipped()
			addImportedSSHKeyReferenceWarnings(accumulator, keyIndex, name, reference, referenceCode, legacyCode, inventory, duplicateNodeNames)
			continue
		}

		keyCandidate := &configImportSSHKeyCandidate{
			index:         keyIndex,
			name:          name,
			source:        keyData,
			reference:     reference,
			referenceCode: referenceCode,
			legacyScope:   legacyScope,
			legacyCode:    legacyCode,
		}
		if len(targetRows) == 1 {
			keyCandidate.existing = true
			keyCandidate.row = targetRows[0]
		}
		if keyCandidate.existing {
			if referenceCode != "" || legacyCode != "" {
				keyCandidate.rejected = true
				accumulator.rejectedWithWarnings(
					configImportEntitySSHKeys, keyIndex, name,
					importedSSHKeyReferenceWarningCodes(reference, referenceCode, legacyCode)...,
				)
				continue
			}
			if reference.present && len(reference.names) > 0 {
				ids, ok := resolveImportedNodeNameScope(reference.names, inventory, duplicateNodeNames)
				if !ok {
					keyCandidate.rejected = true
					accumulator.rejectedWithWarnings(configImportEntitySSHKeys, keyIndex, name, configImportWarningUnresolvedNodeScope)
					continue
				}
				keyCandidate.legacyScope.allowedNodeIDs = &ids
				keyCandidate.nameScopeResolved = true
			} else if reference.present && reference.explicitEmpty {
				empty := ""
				keyCandidate.legacyScope.allowedNodeIDs = &empty
			}

			if err := applyImportedSSHKeyCandidate(tx, inventory, keyCandidate, accumulator); err != nil {
				return err
			}
			continue
		}

		if referenceCode == "" && reference.present && len(reference.names) > 0 {
			ids, ok := resolveImportedNodeNameScope(reference.names, inventory, duplicateNodeNames)
			if ok {
				keyCandidate.legacyScope.allowedNodeIDs = &ids
				keyCandidate.nameScopeResolved = true
			} else {
				keyCandidate.referenceCode = configImportWarningUnresolvedNodeScope
			}
		}
		if reference.present && reference.explicitEmpty && referenceCode == "" {
			empty := ""
			keyCandidate.legacyScope.allowedNodeIDs = &empty
		}
		if err := createImportedSSHKey(tx, inventory, keyCandidate, accumulator); err != nil {
			return err
		}
	}

	for _, candidate := range nodeCandidates {
		if candidate == nil || candidate.rejected {
			continue
		}
		if candidate.skipped {
			_, resolved := resolveImportedNodeKeyReference(
				candidate.reference, candidate.referenceCode,
				inventory, duplicateKeyNames,
			)
			addImportedNodeReferenceWarnings(
				accumulator, candidate.index, candidate.name, candidate.source,
				candidate.reference, candidate.referenceCode, resolved,
			)
			continue
		}
		resolvedKey, resolved := resolveImportedNodeKeyReference(
			candidate.reference, candidate.referenceCode,
			inventory, duplicateKeyNames,
		)
		if candidate.existing && !resolved && nodeReferenceRequiresExistingResolution(candidate.reference, candidate.referenceCode) {
			codes := importedNodeReferenceWarningCodes(candidate.reference, candidate.referenceCode, candidate.source, false)
			candidate.rejected = true
			accumulator.rejectedWithWarnings(configImportEntityNodes, candidate.index, candidate.name, codes...)
			continue
		}
		if resolved {
			candidate.row.SSHKeyID = &resolvedKey.ID
		} else if candidate.reference.present && candidate.reference.explicitEmpty {
			candidate.row.SSHKeyID = nil
		}
		if candidate.existing {
			if err := tx.Save(&candidate.row).Error; err != nil {
				return fmt.Errorf("保存导入节点失败: %w", err)
			}
			inventory.replaceNode(candidate.row)
			accumulator.updated(configImportEntityNodes)
		} else if resolved {
			if err := tx.Model(&model.Node{}).Where("id = ?", candidate.row.ID).
				Update("ssh_key_id", resolvedKey.ID).Error; err != nil {
				return fmt.Errorf("回填导入节点 SSH 密钥失败: %w", err)
			}
			inventory.replaceNode(candidate.row)
		}
		addImportedNodeCredentialWarnings(accumulator, candidate.index, candidate.name, candidate.source, candidate.row)
		if !resolved && !candidate.existing {
			addImportedNodeReferenceWarnings(accumulator, candidate.index, candidate.name, candidate.source, candidate.reference, candidate.referenceCode, false)
		}
	}
	return nil
}
func applyImportedNodeFields(target *model.Node, data map[string]interface{}) {
	if target == nil {
		return
	}
	if host, ok := data["host"].(string); ok {
		target.Host = strings.TrimSpace(host)
	}
	if port, ok := data["port"].(float64); ok {
		target.Port = int(port)
	}
	if username, ok := data["username"].(string); ok {
		target.Username = strings.TrimSpace(username)
	}
	if authType, ok := data["auth_type"].(string); ok {
		target.AuthType = strings.ToLower(strings.TrimSpace(authType))
	}
	if tags, ok := data["tags"].(string); ok {
		target.Tags = tags
	}
	if basePath, ok := data["base_path"].(string); ok {
		target.BasePath = strings.TrimSpace(basePath)
	}
}

func validateImportedNodeCandidate(candidate model.Node) error {
	if candidate.Username == "" ||
		(candidate.AuthType != "" && candidate.AuthType != "password" && candidate.AuthType != "key" && candidate.AuthType != "ssh_key") {
		return fmt.Errorf("imported node credentials are invalid")
	}
	return nodePkg.ValidateNodeHostPort(candidate.Host, candidate.Port)
}

func addImportedNodeReferenceWarnings(
	accumulator *configImportAccumulator,
	index int,
	name string,
	source map[string]interface{},
	reference importedNodeSSHKeyNameCandidate,
	referenceCode string,
	resolved bool,
) {
	for _, code := range importedNodeReferenceWarningCodes(reference, referenceCode, source, resolved) {
		accumulator.warning(configImportEntityNodes, index, name, code)
	}
}

func importedNodeReferenceWarningCodes(
	reference importedNodeSSHKeyNameCandidate,
	referenceCode string,
	source map[string]interface{},
	resolved bool,
) []string {
	codes := make([]string, 0, 2)
	if reference.invalid || referenceCode == configImportWarningInvalidReference {
		codes = append(codes, configImportWarningInvalidReference)
	}
	if reference.legacyConflict || referenceCode == configImportWarningReferenceConflict {
		codes = append(codes, configImportWarningReferenceConflict)
	}
	if !resolved {
		switch {
		case reference.explicitNull,
			referenceCode == configImportWarningUnresolvedSSHKey,
			reference.present && !reference.explicitEmpty,
			!reference.present && importedNodeSourceSSHKeyIDPresent(source):
			codes = append(codes, configImportWarningUnresolvedSSHKey)
		}
	}
	return uniqueConfigImportWarningCodes(codes)
}

func nodeReferenceRequiresExistingResolution(
	reference importedNodeSSHKeyNameCandidate,
	referenceCode string,
) bool {
	if reference.explicitEmpty && referenceCode == "" {
		return false
	}
	if reference.present {
		return reference.explicitNull || reference.invalid || reference.name != "" || referenceCode != ""
	}
	return false
}

func resolveImportedNodeKeyReference(
	reference importedNodeSSHKeyNameCandidate,
	referenceCode string,
	inventory *configImportCredentialInventory,
	ambiguousNames map[string]struct{},
) (model.SSHKey, bool) {
	if !reference.present {
		return model.SSHKey{}, false
	}
	if reference.explicitEmpty || reference.explicitNull || reference.invalid || referenceCode != "" || reference.name == "" {
		return model.SSHKey{}, false
	}
	if _, ambiguous := ambiguousNames[reference.name]; ambiguous {
		return model.SSHKey{}, false
	}
	key, ok := inventory.keyByName(reference.name)
	if !ok {
		return model.SSHKey{}, false
	}
	return key, true
}

func resolveImportedNodeNameScope(
	names []string,
	inventory *configImportCredentialInventory,
	ambiguousNames map[string]struct{},
) (string, bool) {
	ids := make([]string, 0, len(names))
	for _, name := range names {
		if _, ambiguous := ambiguousNames[name]; ambiguous {
			return "", false
		}
		node, ok := inventory.nodeByName(name)
		if !ok {
			return "", false
		}
		ids = append(ids, strconv.FormatUint(uint64(node.ID), 10))
	}
	return strings.Join(ids, ","), true
}
func importedNodeKeyReferenceUnresolved(source map[string]interface{}, nodeItem model.Node) bool {
	raw, present := source["ssh_key_name"]
	if present {
		if raw == nil {
			return true
		}
		value, ok := raw.(string)
		if !ok {
			return true
		}
		if strings.TrimSpace(value) == "" {
			return false
		}
		return nodeItem.SSHKeyID == nil
	}
	return importedNodeSourceSSHKeyIDPresent(source) && nodeItem.SSHKeyID == nil
}

func importedSSHKeyReferenceWarningCodes(
	reference importedSSHKeyAllowedNodeNamesCandidate,
	referenceCode string,
	legacyCode string,
) []string {
	codes := make([]string, 0, 3)
	if reference.invalid || referenceCode == configImportWarningInvalidReference {
		codes = append(codes, configImportWarningInvalidReference)
	}
	if reference.legacyConflict || referenceCode == configImportWarningReferenceConflict {
		codes = append(codes, configImportWarningReferenceConflict)
	}
	if reference.explicitNull || referenceCode == configImportWarningUnresolvedNodeScope ||
		(reference.present && len(reference.names) > 0 && referenceCode != "") {
		codes = append(codes, configImportWarningUnresolvedNodeScope)
	}
	if legacyCode == configImportWarningInvalidScope {
		codes = append(codes, configImportWarningInvalidScope)
	}
	if legacyCode == configImportWarningUnresolvedNodeScope {
		codes = append(codes, configImportWarningUnresolvedNodeScope)
	}
	return uniqueConfigImportWarningCodes(codes)
}

func addImportedSSHKeyReferenceWarnings(
	accumulator *configImportAccumulator,
	index int,
	name string,
	reference importedSSHKeyAllowedNodeNamesCandidate,
	referenceCode string,
	legacyCode string,
	inventory *configImportCredentialInventory,
	ambiguousNames map[string]struct{},
) {
	codes := importedSSHKeyReferenceWarningCodes(reference, referenceCode, legacyCode)
	if reference.present && referenceCode == "" && len(reference.names) > 0 {
		if _, ok := resolveImportedNodeNameScope(reference.names, inventory, ambiguousNames); !ok {
			codes = append(codes, configImportWarningUnresolvedNodeScope)
		}
	}
	for _, code := range uniqueConfigImportWarningCodes(codes) {
		accumulator.warning(configImportEntitySSHKeys, index, name, code)
	}
}

func uniqueConfigImportWarningCodes(codes []string) []string {
	seen := make(map[string]struct{}, len(codes))
	unique := make([]string, 0, len(codes))
	for _, code := range codes {
		if code == "" {
			continue
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		unique = append(unique, code)
	}
	return unique
}

func applyImportedSSHKeyCandidate(
	tx *gorm.DB,
	inventory *configImportCredentialInventory,
	candidate *configImportSSHKeyCandidate,
	accumulator *configImportAccumulator,
) error {
	if candidate == nil {
		return nil
	}
	existing := candidate.row
	sourcePrivateKey, sourcePrivateKeyProvided := candidate.source["private_key"].(string)
	if (!sourcePrivateKeyProvided || strings.TrimSpace(sourcePrivateKey) == "") &&
		strings.TrimSpace(existing.PrivateKey) != "" {
		decrypted, err := secure.DecryptIfNeeded(existing.PrivateKey)
		if err != nil {
			accumulator.rejectedWithWarnings(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningInvalidPrivateKey)
			candidate.rejected = true
			return nil
		}
		existing.PrivateKey = decrypted
	}
	selectedType, err := importedSSHKeySelectedType(candidate.source, existing.KeyType)
	if err != nil {
		accumulator.rejectedWithWarnings(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningInvalidInput)
		candidate.rejected = true
		return nil
	}
	preparedKey, storedType, err := prepareImportedSSHKeyPrivateKey(candidate.source, &existing, selectedType)
	if err != nil {
		accumulator.rejectedWithWarnings(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningInvalidPrivateKey)
		candidate.rejected = true
		return nil
	}
	candidate.row = existing
	if username, ok := candidate.source["username"].(string); ok {
		candidate.row.Username = strings.TrimSpace(username)
	}
	candidate.row.KeyType = storedType
	candidate.row.PrivateKey = preparedKey
	if strings.TrimSpace(preparedKey) == "" {
		candidate.row.PrivateKey = ""
		candidate.row.Fingerprint = ""
		candidate.row.Disabled = true
		accumulator.warning(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningMissingPrivateKey)
	} else {
		candidate.row.Fingerprint = generateFingerprint(preparedKey)
	}
	applyImportedSSHKeyScopeCandidate(&candidate.row, candidate.legacyScope)
	if candidate.reference.present && existing.Disabled {
		candidate.row.Disabled = true
	}
	if strings.TrimSpace(candidate.row.PrivateKey) == "" {
		candidate.row.Fingerprint = ""
		candidate.row.Disabled = true
	}
	if err := tx.Save(&candidate.row).Error; err != nil {
		return fmt.Errorf("保存导入 SSH 密钥失败: %w", err)
	}
	inventory.replaceKey(candidate.row)
	accumulator.updated(configImportEntitySSHKeys)
	return nil
}

func createImportedSSHKey(
	tx *gorm.DB,
	inventory *configImportCredentialInventory,
	candidate *configImportSSHKeyCandidate,
	accumulator *configImportAccumulator,
) error {
	if candidate == nil {
		return nil
	}
	selectedType, err := importedSSHKeySelectedType(candidate.source, "")
	if err != nil {
		accumulator.rejectedWithWarnings(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningInvalidInput)
		candidate.rejected = true
		return nil
	}
	preparedKey, storedType, err := prepareImportedSSHKeyPrivateKey(candidate.source, nil, selectedType)
	if err != nil {
		accumulator.rejectedWithWarnings(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningInvalidPrivateKey)
		candidate.rejected = true
		return nil
	}
	candidate.row = model.SSHKey{Name: candidate.name, KeyType: storedType}
	if username, ok := candidate.source["username"].(string); ok {
		candidate.row.Username = strings.TrimSpace(username)
	}
	candidate.row.PrivateKey = preparedKey
	if strings.TrimSpace(preparedKey) != "" {
		candidate.row.Fingerprint = generateFingerprint(preparedKey)
	} else {
		candidate.row.Fingerprint = ""
		candidate.row.Disabled = true
		accumulator.warning(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningMissingPrivateKey)
	}
	applyImportedSSHKeyScopeCandidate(&candidate.row, candidate.legacyScope)
	if candidate.referenceCode != "" || candidate.legacyCode != "" {
		for _, code := range importedSSHKeyReferenceWarningCodes(candidate.reference, candidate.referenceCode, candidate.legacyCode) {
			accumulator.warning(configImportEntitySSHKeys, candidate.index, candidate.name, code)
		}
		candidate.row.Disabled = true
		if !candidate.nameScopeResolved {
			candidate.row.AllowedNodeIDs = ""
		}
	}
	if strings.TrimSpace(candidate.row.PrivateKey) == "" {
		candidate.row.Fingerprint = ""
		candidate.row.Disabled = true
	}
	if err := tx.Create(&candidate.row).Error; err != nil {
		return fmt.Errorf("创建导入 SSH 密钥失败: %w", err)
	}
	accumulator.recordCreatedID(configImportEntitySSHKeys, candidate.row.ID)
	inventory.replaceKey(candidate.row)
	accumulator.created(configImportEntitySSHKeys, candidate.row.Disabled)
	return nil
}

func addConfigExportNameReferences(nodes []model.Node, keys []model.SSHKey, exportNodes, exportKeys []gin.H) {
	nodeNamesByID := make(map[uint]string, len(nodes))
	for _, node := range nodes {
		nodeNamesByID[node.ID] = node.Name
	}
	keyNamesByID := make(map[uint]string, len(keys))
	for _, key := range keys {
		keyNamesByID[key.ID] = key.Name
	}

	for index, node := range nodes {
		if index >= len(exportNodes) {
			break
		}
		if node.SSHKeyID == nil {
			exportNodes[index]["ssh_key_name"] = ""
			continue
		}
		name, ok := keyNamesByID[*node.SSHKeyID]
		if !ok || !configExportNameRepresentable(name) {
			exportNodes[index]["ssh_key_name"] = nil
			continue
		}
		exportNodes[index]["ssh_key_name"] = name
	}

	for index, key := range keys {
		if index >= len(exportKeys) {
			break
		}
		if strings.TrimSpace(key.AllowedNodeIDs) == "" {
			exportKeys[index]["allowed_node_names"] = []string{}
			continue
		}
		normalized, err := sshutil.NormalizeNodeIDList(key.AllowedNodeIDs)
		if err != nil || strings.TrimSpace(normalized) == "" {
			exportKeys[index]["allowed_node_names"] = nil
			continue
		}
		ids := strings.Split(normalized, ",")
		names := make([]string, 0, len(ids))
		valid := true
		for _, rawID := range ids {
			parsed, parseErr := strconv.ParseUint(strings.TrimSpace(rawID), 10, 64)
			if parseErr != nil || parsed == 0 || uint64(uint(parsed)) != parsed {
				valid = false
				break
			}
			name, ok := nodeNamesByID[uint(parsed)]
			if !ok || !configExportNameRepresentable(name) {
				valid = false
				break
			}
			names = append(names, name)
		}
		if !valid {
			exportKeys[index]["allowed_node_names"] = nil
			continue
		}
		exportKeys[index]["allowed_node_names"] = names
	}
}

func configExportNameRepresentable(name string) bool {
	return name != "" && strings.TrimSpace(name) == name
}
