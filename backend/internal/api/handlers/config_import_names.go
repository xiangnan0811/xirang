package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	duplicates, _ := configImportDuplicateNameIndexesContext(context.Background(), records)
	return duplicates
}

func configImportDuplicateNameIndexesContext(ctx context.Context, records []map[string]interface{}) (map[int]bool, error) {
	groups := make(map[string][]int)
	for index, record := range records {
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
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
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			if err := configImportContextErr(ctx); err != nil {
				return nil, err
			}
			duplicates[index] = true
		}
	}
	return duplicates, nil
}

type configImportCredentialInventory struct {
	nodes                 []model.Node
	keys                  []model.SSHKey
	nodesByName           map[string][]model.Node
	keysByName            map[string][]model.SSHKey
	nodesByID             map[uint]int
	keysByID              map[uint]int
	nodeNameBucketPos     map[uint]int
	keyNameBucketPos      map[uint]int
	nodePasswordPresent   map[uint]bool
	nodePrivateKeyPresent map[uint]bool
	keyPrivateKeyPresent  map[uint]bool
}

type configImportNodeInventoryRow struct {
	ID                uint   `gorm:"column:id"`
	Name              string `gorm:"column:name"`
	Host              string `gorm:"column:host"`
	Port              int    `gorm:"column:port"`
	Username          string `gorm:"column:username"`
	AuthType          string `gorm:"column:auth_type"`
	SSHKeyID          *uint  `gorm:"column:ssh_key_id"`
	Tags              string `gorm:"column:tags"`
	BasePath          string `gorm:"column:base_path"`
	PasswordPresent   bool   `gorm:"column:password_present"`
	PrivateKeyPresent bool   `gorm:"column:private_key_present"`
}

type configImportSSHKeyInventoryRow struct {
	ID                uint       `gorm:"column:id"`
	Name              string     `gorm:"column:name"`
	Username          string     `gorm:"column:username"`
	KeyType           string     `gorm:"column:key_type"`
	Fingerprint       string     `gorm:"column:fingerprint"`
	Disabled          bool       `gorm:"column:disabled"`
	ExpiresAt         *time.Time `gorm:"column:expires_at"`
	AllowedPurposes   string     `gorm:"column:allowed_purposes"`
	AllowedNodeIDs    string     `gorm:"column:allowed_node_ids"`
	AllowedNodeTags   string     `gorm:"column:allowed_node_tags"`
	PrivateKeyPresent bool       `gorm:"column:private_key_present"`
}

func loadConfigImportCredentialInventory(
	ctx context.Context,
	db *gorm.DB,
	data configImportData,
	forUpdate bool,
) (*configImportCredentialInventory, error) {
	inventory := &configImportCredentialInventory{
		nodesByName:           make(map[string][]model.Node),
		keysByName:            make(map[string][]model.SSHKey),
		nodePasswordPresent:   make(map[uint]bool),
		nodePrivateKeyPresent: make(map[uint]bool),
		keyPrivateKeyPresent:  make(map[uint]bool),
	}
	if err := configImportContextErr(ctx); err != nil {
		return nil, err
	}
	if db == nil {
		return nil, fmt.Errorf("config import credential inventory database is unavailable")
	}
	if len(data.Nodes) == 0 && len(data.SSHKeys) == 0 && len(data.Tasks) == 0 {
		return inventory, nil
	}
	nodeQuery := db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&model.Node{})
	keyQuery := db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Model(&model.SSHKey{})
	if forUpdate && !strings.EqualFold(db.Name(), "sqlite") {
		locking := clause.Locking{Strength: "UPDATE"}
		nodeQuery = nodeQuery.Clauses(locking)
		keyQuery = keyQuery.Clauses(locking)
	}
	if len(data.Nodes) > 0 || len(data.SSHKeys) > 0 || len(data.Tasks) > 0 {
		var nodeRows []configImportNodeInventoryRow
		if err := nodeQuery.Select(
			"nodes.id", "nodes.name", "nodes.host", "nodes.port", "nodes.username", "nodes.auth_type",
			"nodes.ssh_key_id", "nodes.tags", "nodes.base_path",
			"CASE WHEN nodes.password <> '' THEN TRUE ELSE FALSE END AS password_present",
			"CASE WHEN nodes.private_key <> '' THEN TRUE ELSE FALSE END AS private_key_present",
		).Order("nodes.id").Find(&nodeRows).Error; err != nil {
			return nil, fmt.Errorf("读取导入节点库存失败: %w", err)
		}
		for _, row := range nodeRows {
			if err := configImportContextErr(ctx); err != nil {
				return nil, err
			}
			inventory.nodes = append(inventory.nodes, model.Node{
				ID: row.ID, Name: row.Name, Host: row.Host, Port: row.Port,
				Username: row.Username, AuthType: row.AuthType, SSHKeyID: row.SSHKeyID,
				Tags: row.Tags, BasePath: row.BasePath,
			})
			inventory.nodePasswordPresent[row.ID] = row.PasswordPresent
			inventory.nodePrivateKeyPresent[row.ID] = row.PrivateKeyPresent
		}
	}
	if err := configImportContextErr(ctx); err != nil {
		return nil, err
	}
	if len(data.SSHKeys) > 0 || len(data.Nodes) > 0 {
		var keyRows []configImportSSHKeyInventoryRow
		if err := keyQuery.Select(
			"ssh_keys.id", "ssh_keys.name", "ssh_keys.username", "ssh_keys.key_type", "ssh_keys.fingerprint", "ssh_keys.disabled",
			"ssh_keys.expires_at", "ssh_keys.allowed_purposes", "ssh_keys.allowed_node_ids", "ssh_keys.allowed_node_tags",
			"CASE WHEN ssh_keys.private_key <> '' THEN TRUE ELSE FALSE END AS private_key_present",
		).Order("ssh_keys.id").Find(&keyRows).Error; err != nil {
			return nil, fmt.Errorf("读取导入 SSH 密钥库存失败: %w", err)
		}
		for _, row := range keyRows {
			if err := configImportContextErr(ctx); err != nil {
				return nil, err
			}
			inventory.keys = append(inventory.keys, model.SSHKey{
				ID: row.ID, Name: row.Name, Username: row.Username, KeyType: row.KeyType,
				Fingerprint: row.Fingerprint, Disabled: row.Disabled, ExpiresAt: row.ExpiresAt,
				AllowedPurposes: row.AllowedPurposes, AllowedNodeIDs: row.AllowedNodeIDs,
				AllowedNodeTags: row.AllowedNodeTags,
			})
			inventory.keyPrivateKeyPresent[row.ID] = row.PrivateKeyPresent
		}
	}
	if err := configImportContextErr(ctx); err != nil {
		return nil, err
	}
	if err := inventory.rebuildIndexesContext(ctx); err != nil {
		return nil, err
	}
	return inventory, nil
}

func lockConfigImportCredentialInventory(tx *gorm.DB, data configImportData) (*configImportCredentialInventory, error) {
	if tx == nil {
		return nil, fmt.Errorf("config import credential inventory transaction is unavailable")
	}
	if len(data.Nodes) == 0 && len(data.SSHKeys) == 0 {
		return loadConfigImportCredentialInventory(tx.Statement.Context, tx, data, true)
	}
	if strings.EqualFold(tx.Name(), "sqlite") {
		if err := tx.Exec("UPDATE nodes SET id = id WHERE 1 = 0").Error; err != nil {
			return nil, fmt.Errorf("保留导入节点写锁失败: %w", err)
		}
	}
	return loadConfigImportCredentialInventory(tx.Statement.Context, tx, data, true)
}

func (inventory *configImportCredentialInventory) rebuildIndexes() {
	_ = inventory.rebuildIndexesContext(context.Background())
}

func (inventory *configImportCredentialInventory) rebuildIndexesContext(ctx context.Context) error {
	if inventory == nil {
		return nil
	}
	if err := configImportContextErr(ctx); err != nil {
		return err
	}
	inventory.nodesByName = make(map[string][]model.Node, len(inventory.nodes))
	inventory.keysByName = make(map[string][]model.SSHKey, len(inventory.keys))
	inventory.nodesByID = make(map[uint]int, len(inventory.nodes))
	inventory.keysByID = make(map[uint]int, len(inventory.keys))
	inventory.nodeNameBucketPos = make(map[uint]int, len(inventory.nodes))
	inventory.keyNameBucketPos = make(map[uint]int, len(inventory.keys))
	for index, node := range inventory.nodes {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		inventory.nodesByID[node.ID] = index
		inventory.addNodeName(node)
	}
	for index, key := range inventory.keys {
		if err := configImportContextErr(ctx); err != nil {
			return err
		}
		inventory.keysByID[key.ID] = index
		inventory.addKeyName(key)
	}
	return nil
}

func (inventory *configImportCredentialInventory) addNodeName(value model.Node) {
	if inventory == nil || !configExportNameRepresentable(value.Name) {
		return
	}
	rows := append(inventory.nodesByName[value.Name], value)
	inventory.nodesByName[value.Name] = rows
}

func (inventory *configImportCredentialInventory) removeNodeName(value model.Node) {
	if inventory == nil || !configExportNameRepresentable(value.Name) {
		return
	}
	name := value.Name
	rows := inventory.nodesByName[name]
	position, ok := inventory.nodeNameBucketPos[value.ID]
	if !ok || position < 0 || position >= len(rows) || rows[position].ID != value.ID {
		ok = false
		for index, row := range rows {
			if row.ID == value.ID {
				position = index
				ok = true
				break
			}
		}
	}
	if !ok || position < 0 || position >= len(rows) {
		return
	}
	last := len(rows) - 1
	if position != last {
		moved := rows[last]
		rows[position] = moved
		inventory.nodeNameBucketPos[moved.ID] = position
	}
	rows = rows[:last]
	if len(rows) == 0 {
		delete(inventory.nodesByName, name)
	} else {
		inventory.nodesByName[name] = rows
	}
	delete(inventory.nodeNameBucketPos, value.ID)
}

func (inventory *configImportCredentialInventory) addKeyName(value model.SSHKey) {
	if inventory == nil || !configExportNameRepresentable(value.Name) {
		return
	}
	rows := append(inventory.keysByName[value.Name], value)
	inventory.keysByName[value.Name] = rows
	inventory.keyNameBucketPos[value.ID] = len(rows) - 1
}

func (inventory *configImportCredentialInventory) removeKeyName(value model.SSHKey) {
	if inventory == nil || !configExportNameRepresentable(value.Name) {
		return
	}
	name := value.Name
	rows := inventory.keysByName[name]
	position, ok := inventory.keyNameBucketPos[value.ID]
	if !ok || position < 0 || position >= len(rows) || rows[position].ID != value.ID {
		ok = false
		for index, row := range rows {
			if row.ID == value.ID {
				position = index
				ok = true
				break
			}
		}
	}
	if !ok || position < 0 || position >= len(rows) {
		delete(inventory.keyNameBucketPos, value.ID)
		return
	}
	last := len(rows) - 1
	if position != last {
		moved := rows[last]
		rows[position] = moved
		inventory.keyNameBucketPos[moved.ID] = position
	}
	rows = rows[:last]
	if len(rows) == 0 {
		delete(inventory.keysByName, name)
	} else {
		inventory.keysByName[name] = rows
	}
	delete(inventory.keyNameBucketPos, value.ID)
}

func (inventory *configImportCredentialInventory) replaceNode(value model.Node) {
	if inventory == nil {
		return
	}
	if inventory.nodesByID == nil {
		inventory.rebuildIndexes()
	}
	if index, exists := inventory.nodesByID[value.ID]; exists {
		old := inventory.nodes[index]
		inventory.removeNodeName(old)
		inventory.nodes[index] = value
		inventory.addNodeName(value)
		return
	}
	inventory.nodesByID[value.ID] = len(inventory.nodes)
	inventory.nodes = append(inventory.nodes, value)
	inventory.addNodeName(value)
}

func (inventory *configImportCredentialInventory) replaceKey(value model.SSHKey) {
	if inventory == nil {
		return
	}
	if inventory.keysByID == nil {
		inventory.rebuildIndexes()
	}
	if index, exists := inventory.keysByID[value.ID]; exists {
		old := inventory.keys[index]
		inventory.removeKeyName(old)
		inventory.keys[index] = value
		inventory.addKeyName(value)
		return
	}
	inventory.keysByID[value.ID] = len(inventory.keys)
	inventory.keys = append(inventory.keys, value)
	inventory.addKeyName(value)
}

func (inventory *configImportCredentialInventory) remapNodeIdentity(provisional uint, value model.Node) {
	if inventory == nil {
		return
	}
	if inventory.nodesByID == nil {
		inventory.rebuildIndexes()
	}
	index, exists := inventory.nodesByID[provisional]
	if !exists {
		inventory.replaceNode(value)
		return
	}
	old := inventory.nodes[index]
	inventory.removeNodeName(old)
	delete(inventory.nodesByID, provisional)
	inventory.nodes[index] = value
	inventory.nodesByID[value.ID] = index
	inventory.addNodeName(value)
	if present, ok := inventory.nodePasswordPresent[provisional]; ok {
		delete(inventory.nodePasswordPresent, provisional)
		inventory.nodePasswordPresent[value.ID] = present
	}
	if present, ok := inventory.nodePrivateKeyPresent[provisional]; ok {
		delete(inventory.nodePrivateKeyPresent, provisional)
		inventory.nodePrivateKeyPresent[value.ID] = present
	}
}

func (inventory *configImportCredentialInventory) remapKeyIdentity(provisional uint, value model.SSHKey) {
	if inventory == nil {
		return
	}
	if inventory.keysByID == nil {
		inventory.rebuildIndexes()
	}
	index, exists := inventory.keysByID[provisional]
	if !exists {
		inventory.replaceKey(value)
		return
	}
	old := inventory.keys[index]
	inventory.removeKeyName(old)
	delete(inventory.keysByID, provisional)
	inventory.keys[index] = value
	inventory.keysByID[value.ID] = index
	inventory.addKeyName(value)
	if present, ok := inventory.keyPrivateKeyPresent[provisional]; ok {
		delete(inventory.keyPrivateKeyPresent, provisional)
		inventory.keyPrivateKeyPresent[value.ID] = present
	}
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

func configImportDuplicateNameSetContext(ctx context.Context, records []map[string]interface{}) (map[string]struct{}, error) {
	duplicates, err := configImportDuplicateNameIndexesContext(ctx, records)
	if err != nil {
		return nil, err
	}
	names := make(map[string]struct{})
	for index := range duplicates {
		if err := configImportContextErr(ctx); err != nil {
			return nil, err
		}
		if name, ok := records[index]["name"].(string); ok {
			name = strings.TrimSpace(name)
			if name != "" {
				names[name] = struct{}{}
			}
		}
	}
	return names, nil
}

type configImportNodeCandidate struct {
	index         int
	name          string
	source        map[string]interface{}
	row           model.Node
	provisional   uint
	existing      bool
	baseCreated   bool
	skipped       bool
	action        configImportAction
	pendingCode   string
	reference     importedNodeSSHKeyNameCandidate
	referenceCode string
	rejected      bool
}

type configImportSSHKeyCandidate struct {
	index             int
	name              string
	source            map[string]interface{}
	row               model.SSHKey
	provisional       uint
	existing          bool
	action            configImportAction
	pendingCode       string
	reference         importedSSHKeyAllowedNodeNamesCandidate
	referenceCode     string
	legacyScope       importedSSHKeyScopeCandidate
	legacyCode        string
	nameScopeResolved bool
	rejected          bool
}

// prepareConfigImportSSHKeyCandidateRow is the single field/secret
// preparation boundary used by preview evaluation and apply. Preview passes
// allowRetained=false and never supplies a stored secret; apply passes true
// only after the locked row has been read and decrypted.
func prepareConfigImportSSHKeyCandidateRow(
	candidate *configImportSSHKeyCandidate,
	retainedPresent bool,
	allowRetained bool,
) (bool, error) {
	if candidate == nil {
		return false, nil
	}
	selectedType, err := importedSSHKeySelectedType(candidate.source, candidate.row.KeyType)
	if err != nil {
		return false, err
	}
	supplied, invalid := importedConfigSecretString(candidate.source, "private_key")
	if invalid {
		return false, fmt.Errorf("imported private_key must be a string")
	}
	if supplied == "" && candidate.existing && retainedPresent && !allowRetained {
		return true, nil
	}
	base := candidate.row
	preparedKey, storedType, err := prepareImportedSSHKeyPrivateKey(candidate.source, &base, selectedType)
	if err != nil {
		return false, err
	}
	if !candidate.existing {
		base.Name = candidate.name
	}
	if username, ok := candidate.source["username"].(string); ok {
		base.Username = strings.TrimSpace(username)
	}
	base.KeyType = storedType
	base.PrivateKey = preparedKey
	if strings.TrimSpace(preparedKey) == "" {
		base.PrivateKey = ""
		base.Fingerprint = ""
		base.Disabled = true
	} else {
		base.Fingerprint = generateFingerprint(preparedKey)
	}
	applyImportedSSHKeyScopeCandidate(&base, candidate.legacyScope)
	if candidate.reference.present && candidate.existing && candidate.row.Disabled {
		base.Disabled = true
	}
	if candidate.referenceCode != "" || candidate.legacyCode != "" {
		base.Disabled = true
		if !candidate.nameScopeResolved {
			base.AllowedNodeIDs = ""
		}
	}
	if !candidate.existing && strings.TrimSpace(base.PrivateKey) == "" {
		base.Disabled = true
	}
	candidate.row = base
	return false, nil
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
	supplied, suppliedInvalid := importedConfigSecretString(candidate.source, "private_key")
	if suppliedInvalid {
		accumulator.rejectedWithWarnings(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningInvalidPrivateKey)
		candidate.rejected = true
		candidate.action = configImportActionReject
		return nil
	}
	if supplied == "" && strings.TrimSpace(candidate.row.PrivateKey) != "" {
		decrypted, err := secure.DecryptIfNeeded(candidate.row.PrivateKey)
		if err != nil {
			accumulator.rejectedWithWarnings(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningInvalidPrivateKey)
			candidate.rejected = true
			candidate.action = configImportActionReject
			return nil
		}
		candidate.row.PrivateKey = decrypted
	}
	pending, err := prepareConfigImportSSHKeyCandidateRow(candidate, supplied == "" && strings.TrimSpace(candidate.row.PrivateKey) != "", true)
	if err != nil || pending {
		accumulator.rejectedWithWarnings(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningInvalidPrivateKey)
		candidate.rejected = true
		candidate.action = configImportActionReject
		return nil
	}
	if strings.TrimSpace(candidate.row.PrivateKey) == "" {
		accumulator.warning(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningMissingPrivateKey)
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
	candidate *configImportSSHKeyCandidate,
	accumulator *configImportAccumulator,
) error {
	if candidate == nil {
		return nil
	}
	candidate.existing = false
	pending, err := prepareConfigImportSSHKeyCandidateRow(candidate, false, true)
	if err != nil || pending {
		accumulator.rejectedWithWarnings(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningInvalidPrivateKey)
		candidate.rejected = true
		candidate.action = configImportActionReject
		return nil
	}
	if strings.TrimSpace(candidate.row.PrivateKey) == "" {
		accumulator.warning(configImportEntitySSHKeys, candidate.index, candidate.name, configImportWarningMissingPrivateKey)
	}
	// Candidate IDs are only valid inside the in-memory decision.  Never pass
	// one through to the database: the real identity is assigned by INSERT and
	// is mapped back by persistConfigImportSSHKeys.
	candidate.row.ID = 0
	if err := tx.Create(&candidate.row).Error; err != nil {
		return fmt.Errorf("创建导入 SSH 密钥失败: %w", err)
	}
	accumulator.recordCreatedID(configImportEntitySSHKeys, candidate.row.ID)
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
