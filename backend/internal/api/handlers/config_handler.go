package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/cronutil"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/node"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
	taskPkg "xirang/backend/internal/task"
	"xirang/backend/internal/util"
)

// ConfigHandler 处理配置导出/导入
type ConfigHandler struct {
	db           *gorm.DB
	settingsSvc  *settings.Service
	transitioner publication.FeatureTransitioner
	assetProbe   func(operation string)
}

var errConfigImportTargetConflict = errors.New("config import target ownership conflict")

type configImportData struct {
	Nodes          []map[string]interface{} `json:"nodes"`
	SSHKeys        []map[string]interface{} `json:"ssh_keys"`
	Policies       []map[string]interface{} `json:"policies"`
	Tasks          []map[string]interface{} `json:"tasks"`
	SystemSettings []map[string]interface{} `json:"system_settings"`
}

type importTaskKey struct {
	name   string
	nodeID uint
}

type configImportTaskCandidate struct {
	task                    model.Task
	provisional             uint
	req                     taskPkg.CreateTaskInput
	dependencyKey           importTaskKey
	hasDependency           bool
	dependencyID            *uint
	taskIndex               int
	name                    string
	existing                bool
	rejected                bool
	action                  configImportAction
	pendingCode             string
	targetSource            configImportTargetSource
	hasImportedCronOverride bool
	importedCronOverride    bool
	managedRsync            bool
	managedRclone           bool
	hasExplicitEnabled      bool
	requestedEnabled        bool
	previousCronSpec        string
}

type configImportSetting struct {
	key   string
	value string
}

const (
	configImportWarningLimit = 100

	configImportEntityNodes          = "nodes"
	configImportEntitySSHKeys        = "ssh_keys"
	configImportEntityPolicies       = "policies"
	configImportEntityTasks          = "tasks"
	configImportEntitySystemSettings = "system_settings"

	configImportWarningInvalidInput        = "invalid_input"
	configImportWarningInvalidScope        = "invalid_scope"
	configImportWarningUnresolvedNodeScope = "unresolved_node_scope"
	configImportWarningInvalidPrivateKey   = "invalid_private_key"
	configImportWarningMissingPrivateKey   = "missing_private_key"
	configImportWarningMissingPassword     = "missing_password"
	configImportWarningMissingInlineKey    = "missing_inline_private_key"
	configImportWarningUnresolvedSSHKey    = "unresolved_ssh_key"
	configImportWarningDuplicateName       = "duplicate_name"
	configImportWarningInvalidReference    = "invalid_reference"
	configImportWarningReferenceConflict   = "reference_conflict"
)

// configImportWarning is the intentionally small, stable public warning
// contract. It never contains parser, SQL, path, or payload details.
type configImportWarning struct {
	Entity string `json:"entity"`
	Index  int    `json:"index"`
	Name   string `json:"name,omitempty"`
	Code   string `json:"code"`
}

// configImportResult reports only successful writes for the five classic
// entity classes. The asset graph is an atomic side effect and is not included
// in these counts.
type configImportResult struct {
	Nodes             int                   `json:"nodes"`
	SSHKeys           int                   `json:"ssh_keys"`
	Policies          int                   `json:"policies"`
	Tasks             int                   `json:"tasks"`
	SystemSettings    int                   `json:"system_settings"`
	Imported          int                   `json:"imported"`
	Skipped           int                   `json:"skipped"`
	Created           int                   `json:"created"`
	Updated           int                   `json:"updated"`
	Rejected          int                   `json:"rejected"`
	DisabledImported  int                   `json:"disabled_imported"`
	Warnings          []configImportWarning `json:"warnings"`
	WarningsTruncated int                   `json:"warnings_truncated"`
}
type configImportWarningKey struct {
	entity string
	index  int
	code   string
}

type configImportAccumulator struct {
	result           configImportResult
	warningKeys      map[configImportWarningKey]struct{}
	createdNodeIDs   map[uint]struct{}
	createdSSHKeyIDs map[uint]struct{}
}

func newConfigImportAccumulator() *configImportAccumulator {
	return &configImportAccumulator{
		result: configImportResult{
			Warnings: make([]configImportWarning, 0, configImportWarningLimit),
		},
		warningKeys:      make(map[configImportWarningKey]struct{}),
		createdNodeIDs:   make(map[uint]struct{}),
		createdSSHKeyIDs: make(map[uint]struct{}),
	}
}

func (a *configImportAccumulator) recordCreatedID(entity string, id uint) {
	if a == nil || id == 0 {
		return
	}
	switch entity {
	case configImportEntityNodes:
		a.createdNodeIDs[id] = struct{}{}
	case configImportEntitySSHKeys:
		a.createdSSHKeyIDs[id] = struct{}{}
	}
}

func (a *configImportAccumulator) warning(entity string, index int, name, code string) {
	if a == nil {
		return
	}
	if a.warningKeys == nil {
		a.warningKeys = make(map[configImportWarningKey]struct{})
	}
	key := configImportWarningKey{entity: entity, index: index, code: code}
	if _, exists := a.warningKeys[key]; exists {
		return
	}
	a.warningKeys[key] = struct{}{}
	warning := configImportWarning{
		Entity: entity,
		Index:  index,
		Name:   sanitizeConfigImportWarningName(name),
		Code:   code,
	}
	if len(a.result.Warnings) < configImportWarningLimit {
		a.result.Warnings = append(a.result.Warnings, warning)
		return
	}
	a.result.WarningsTruncated++
}

func (a *configImportAccumulator) rejected(entity string, index int, name, code string) {
	if a == nil {
		return
	}
	a.result.Rejected++
	a.warning(entity, index, name, code)
}

func (a *configImportAccumulator) skipped() {
	if a != nil {
		a.result.Skipped++
	}
}

func (a *configImportAccumulator) created(entity string, disabled bool) {
	if a == nil {
		return
	}
	a.result.Created++
	a.classCount(entity)
	if entity == configImportEntitySSHKeys && disabled {
		a.result.DisabledImported++
	}
}

func (a *configImportAccumulator) updated(entity string) {
	if a == nil {
		return
	}
	a.result.Updated++
	a.classCount(entity)
}

func (a *configImportAccumulator) classCount(entity string) {
	switch entity {
	case configImportEntityNodes:
		a.result.Nodes++
	case configImportEntitySSHKeys:
		a.result.SSHKeys++
	case configImportEntityPolicies:
		a.result.Policies++
	case configImportEntityTasks:
		a.result.Tasks++
	case configImportEntitySystemSettings:
		a.result.SystemSettings++
	}
}

func (a *configImportAccumulator) finalize() configImportResult {
	if a == nil {
		return configImportResult{}
	}
	a.result.Imported = a.result.Created + a.result.Updated
	return a.result
}

func sanitizeConfigImportWarningName(name string) string {
	clean := strings.TrimSpace(util.SanitizeMessage(name))
	clean = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, clean)
	runes := []rune(clean)
	if len(runes) > 120 {
		return string(runes[:120])
	}
	return clean
}

func NewConfigHandler(db *gorm.DB, settingsSvc *settings.Service) *ConfigHandler {
	return &ConfigHandler{db: db, settingsSvc: settingsSvc}
}

// WithBackupAssetTransitioner connects imports that alter backup-asset
// foundation settings to the same admission drain used by SettingsHandler.
func (h *ConfigHandler) WithBackupAssetTransitioner(transitioner publication.FeatureTransitioner) *ConfigHandler {
	if h != nil {
		h.transitioner = transitioner
	}
	return h
}

func (h *ConfigHandler) normalizeImportSettings(records []map[string]interface{}) ([]configImportSetting, map[string]string, error) {
	if len(records) == 0 {
		return nil, map[string]string{}, nil
	}
	if h == nil || h.settingsSvc == nil {
		return nil, nil, fmt.Errorf("settings service is unavailable for system-setting import")
	}
	plan := make([]configImportSetting, 0, len(records))
	foundation := make(map[string]string)
	seen := make(map[string]struct{}, len(records))
	for index, record := range records {
		key, ok := record["key"].(string)
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, nil, fmt.Errorf("system_settings[%d].key is required", index)
		}
		value, ok := record["value"].(string)
		if !ok {
			return nil, nil, fmt.Errorf("system_settings[%d].value must be a string", index)
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, nil, fmt.Errorf("system_settings contains duplicate key: %s", key)
		}
		if err := h.settingsSvc.Validate(key, value); err != nil {
			return nil, nil, err
		}
		seen[key] = struct{}{}
		plan = append(plan, configImportSetting{key: key, value: value})
		if settings.IsBackupAssetFoundationSetting(key) {
			foundation[key] = value
		}
	}
	return plan, foundation, nil
}

func (h *ConfigHandler) persistConfigImport(
	ctx context.Context,
	foundation map[string]string,
	persist func(context.Context) error,
	restore func(context.Context) error,
) error {
	if persist == nil {
		return fmt.Errorf("config import persistence callback is required")
	}
	if len(foundation) == 0 {
		return persist(ctx)
	}
	if h == nil || h.settingsSvc == nil {
		return fmt.Errorf("settings service is unavailable for backup asset import")
	}
	return h.settingsSvc.WithBackupAssetMutation(ctx, func(current map[string]string) error {
		return transitionBackupAssetSettingsMutationWithRestore(
			ctx, h.settingsSvc, h.transitioner, current, foundation, persist, restore,
		)
	})
}

type configExportSnapshot struct {
	nodes          []model.Node
	sshKeys        []model.SSHKey
	exportNodes    []gin.H
	exportKeys     []gin.H
	exportPolicies []gin.H
	exportTasks    []gin.H
	exportSettings []gin.H
	assetGraph     configAssetGraph
	assetCounts    configAssetExportCounts
}

func (h *ConfigHandler) buildConfigExportSnapshot(tx *gorm.DB, includeSecrets bool) (configExportSnapshot, error) {
	var snapshot configExportSnapshot
	if h == nil || tx == nil {
		return snapshot, fmt.Errorf("config export transaction is unavailable")
	}

	if err := tx.Find(&snapshot.nodes).Error; err != nil {
		return configExportSnapshot{}, err
	}
	if err := tx.Find(&snapshot.sshKeys).Error; err != nil {
		return configExportSnapshot{}, err
	}
	var policies []model.Policy
	if err := tx.Preload("Nodes").Find(&policies).Error; err != nil {
		return configExportSnapshot{}, err
	}
	var tasks []model.Task
	if err := tx.Preload("Node").Preload("Policy").Find(&tasks).Error; err != nil {
		return configExportSnapshot{}, err
	}

	taskLookup := make(map[uint]model.Task, len(tasks))
	for _, task := range tasks {
		taskLookup[task.ID] = task
	}

	snapshot.exportNodes = make([]gin.H, 0, len(snapshot.nodes))
	for _, n := range snapshot.nodes {
		item := gin.H{
			"name":       n.Name,
			"host":       n.Host,
			"port":       n.Port,
			"username":   n.Username,
			"auth_type":  n.AuthType,
			"tags":       n.Tags,
			"base_path":  n.BasePath,
			"ssh_key_id": n.SSHKeyID,
		}
		if includeSecrets {
			item["password"] = n.Password
			item["private_key"] = n.PrivateKey
		}
		snapshot.exportNodes = append(snapshot.exportNodes, item)
	}

	snapshot.exportKeys = make([]gin.H, 0, len(snapshot.sshKeys))
	for _, k := range snapshot.sshKeys {
		item := gin.H{
			"name":              k.Name,
			"username":          k.Username,
			"key_type":          k.KeyType,
			"fingerprint":       k.Fingerprint,
			"disabled":          k.Disabled,
			"expires_at":        k.ExpiresAt,
			"allowed_purposes":  k.AllowedPurposes,
			"allowed_node_ids":  k.AllowedNodeIDs,
			"allowed_node_tags": k.AllowedNodeTags,
		}
		if includeSecrets {
			item["private_key"] = k.PrivateKey
		}
		snapshot.exportKeys = append(snapshot.exportKeys, item)
	}
	addConfigExportNameReferences(snapshot.nodes, snapshot.sshKeys, snapshot.exportNodes, snapshot.exportKeys)

	snapshot.exportPolicies = make([]gin.H, 0, len(policies))
	for _, p := range policies {
		nodeNames := make([]string, 0, len(p.Nodes))
		for _, n := range p.Nodes {
			nodeNames = append(nodeNames, n.Name)
		}
		item := gin.H{
			"name":                  p.Name,
			"description":           p.Description,
			"source_path":           p.SourcePath,
			"target_path":           p.TargetPath,
			"cron_spec":             p.CronSpec,
			"exclude_rules":         p.ExcludeRules,
			"bwlimit":               p.BwLimit,
			"bandwidth_schedule":    p.BandwidthSchedule,
			"retention_days":        p.RetentionDays,
			"retention_mode":        p.RetentionMode,
			"keep_daily":            p.KeepDaily,
			"keep_weekly":           p.KeepWeekly,
			"keep_monthly":          p.KeepMonthly,
			"keep_yearly":           p.KeepYearly,
			"max_concurrent":        p.MaxConcurrent,
			"enabled":               p.Enabled,
			"verify_enabled":        p.VerifyEnabled,
			"verify_sample_rate":    p.VerifySampleRate,
			"max_execution_seconds": p.MaxExecutionSeconds,
			"max_retries":           p.MaxRetries,
			"retry_base_seconds":    p.RetryBaseSeconds,
			"drill_enabled":         p.DrillEnabled,
			"drill_cron":            p.DrillCron,
			"drill_restore_path":    p.DrillRestorePath,
			"drill_auto_cleanup":    p.DrillAutoCleanup,
			"rpo_minutes":           p.RPOMinutes,
			"rto_minutes":           p.RTOMinutes,
			"is_template":           p.IsTemplate,
			"node_names":            nodeNames,
		}
		if includeSecrets {
			item["pre_hook"] = p.PreHook
			item["post_hook"] = p.PostHook
			item["hook_timeout_seconds"] = p.HookTimeoutSeconds
			item["app_profile"] = p.AppProfile
			item["app_credential_id"] = p.AppCredentialID
			item["escalation_policy_id"] = p.EscalationPolicyID
			item["drill_pre_verify"] = p.DrillPreVerify
			item["drill_verify"] = p.DrillVerify
			item["drill_post_verify"] = p.DrillPostVerify
		}
		snapshot.exportPolicies = append(snapshot.exportPolicies, item)
	}

	snapshot.exportTasks = make([]gin.H, 0, len(tasks))
	for _, t := range tasks {
		item := gin.H{
			"name":          t.Name,
			"node_id":       t.NodeID,
			"node_name":     t.Node.Name,
			"policy_id":     t.PolicyID,
			"policy_name":   "",
			"executor_type": t.ExecutorType,
			"command":       t.Command,
			"rsync_source":  t.RsyncSource,
			"rsync_target":  t.RsyncTarget,
			"cron_spec":     t.CronSpec,
			"cron_override": t.CronOverride,
			"source":        t.Source,
			"enabled":       t.Enabled,
		}
		if t.DependsOnTaskID != nil {
			item["depends_on_task_id"] = *t.DependsOnTaskID
			if depTask, ok := taskLookup[*t.DependsOnTaskID]; ok {
				item["depends_on_task_name"] = depTask.Name
				item["depends_on_task_node_name"] = depTask.Node.Name
				item["depends_on_task_node_id"] = depTask.NodeID
			}
		}
		if t.Policy != nil {
			item["policy_name"] = t.Policy.Name
		}
		if includeSecrets && t.ExecutorConfig != "" {
			item["executor_config"] = t.ExecutorConfig
		}
		snapshot.exportTasks = append(snapshot.exportTasks, item)
	}

	var dbSettings []model.SystemSetting
	if err := tx.Find(&dbSettings).Error; err != nil {
		return configExportSnapshot{}, err
	}
	snapshot.exportSettings = make([]gin.H, 0, len(dbSettings))
	for _, s := range dbSettings {
		if settings.IsInternalSettingKey(s.Key) {
			continue
		}
		if !includeSecrets && configExportSettingLooksSensitive(s) {
			continue
		}
		snapshot.exportSettings = append(snapshot.exportSettings, gin.H{
			"key":   s.Key,
			"value": s.Value,
		})
	}

	assetGraph, assetCounts, err := h.buildConfigAssetExportGraph(tx, includeSecrets)
	if err != nil {
		return configExportSnapshot{}, err
	}
	snapshot.assetGraph = assetGraph
	snapshot.assetCounts = assetCounts
	return snapshot, nil
}

// Export godoc
// @Summary      导出配置
// @Description  导出节点、SSH 密钥、策略、任务配置为 JSON；默认不含敏感字段，include_secrets=true 且 admin 权限时可导出。节点保留 ssh_key_id 并附带 ssh_key_name，密钥保留 allowed_node_ids 并附带 allowed_node_names；名称字段缺失/空/null 语义与导入兼容，导出始终使用 v2 格式。
// @Tags         config
// @Security     Bearer
// @Produce      json
// @Param        include_secrets  query     bool    false  "是否包含敏感字段（仅 admin）"
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Failure      403  {object}  handlers.Response
// @Router       /config/export [get]
func (h *ConfigHandler) Export(c *gin.Context) {
	includeSecrets := c.Query("include_secrets") == "true"

	if includeSecrets {
		role, _ := c.Get("role")
		if role != "admin" {
			writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
				Action:           "config.export",
				Purpose:          "config_export",
				CredentialKind:   "config_export",
				CredentialSource: "config.export",
				Outcome:          credentialaudit.OutcomeBlocked,
				Metadata: map[string]any{
					"stage":          "authorization",
					"with_sensitive": true,
				},
			})
			respondForbidden(c, "仅管理员可导出敏感数据")
			return
		}
		userID, _ := c.Get("user_id")
		username, _ := c.Get("username")
		logger.Module("audit").Warn().
			Interface("user_id", userID).
			Interface("username", username).
			Msg("管理员导出了包含敏感数据的配置")
	}

	var snapshot configExportSnapshot
	transaction := func(tx *gorm.DB) error {
		var err error
		snapshot, err = h.buildConfigExportSnapshot(tx, includeSecrets)
		return err
	}
	var err error
	if strings.EqualFold(h.db.Name(), "postgres") {
		err = h.db.WithContext(c.Request.Context()).Transaction(transaction, &sql.TxOptions{
			Isolation: sql.LevelRepeatableRead,
			ReadOnly:  true,
		})
	} else {
		err = h.db.WithContext(c.Request.Context()).Transaction(transaction)
	}
	if err != nil {
		respondInternalError(c, err)
		return
	}

	documentID, err := backupasset.NewOpaqueID()
	if err != nil {
		respondInternalError(c, err)
		return
	}

	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:           "config.export",
		Purpose:          "config_export",
		CredentialKind:   "config_export",
		CredentialSource: "config.export",
		Outcome:          credentialaudit.OutcomeSuccess,
		Metadata: map[string]any{
			"stage":                  "success",
			"with_sensitive":         includeSecrets,
			"node_count":             len(snapshot.exportNodes),
			"key_count":              len(snapshot.exportKeys),
			"policy_count":           len(snapshot.exportPolicies),
			"task_count":             len(snapshot.exportTasks),
			"setting_count":          len(snapshot.exportSettings),
			"repository_count":       snapshot.assetCounts.Repositories,
			"link_count":             snapshot.assetCounts.Links,
			"retention_policy_count": snapshot.assetCounts.Policies,
			"hold_count":             snapshot.assetCounts.Holds,
		},
	})

	respondOK(c, gin.H{
		"document_id": documentID,
		"version":     configExportVersion2,
		"exported_at": time.Now().Format(time.RFC3339),
		"data": gin.H{
			"nodes":                     snapshot.exportNodes,
			"ssh_keys":                  snapshot.exportKeys,
			"policies":                  snapshot.exportPolicies,
			"tasks":                     snapshot.exportTasks,
			"system_settings":           snapshot.exportSettings,
			"backup_repositories":       snapshot.assetGraph.BackupRepositories,
			"task_repository_links":     snapshot.assetGraph.TaskRepositoryLinks,
			"backup_retention_policies": snapshot.assetGraph.BackupRetentionPolicies,
			"recovery_point_holds":      snapshot.assetGraph.RecoveryPointHolds,
		},
	})
}

func configExportSettingLooksSensitive(setting model.SystemSetting) bool {
	key := strings.ToLower(strings.TrimSpace(setting.Key))
	for _, marker := range []string{"password", "token", "secret", "private", "credential", "bearer", "api_key", "apikey", "proxy"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	value := strings.ToLower(strings.TrimSpace(setting.Value))
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "ws://") || strings.HasPrefix(value, "wss://") {
		return true
	}
	for _, marker := range []string{"-----begin", "private key", "bearer ", "authorization:", "token=", "password=", "secret="} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

// importValidationError 导入数据校验错误
type importValidationError struct {
	Resource string `json:"resource"` // nodes / policies / tasks
	Index    int    `json:"index"`
	Name     string `json:"name"`
	Field    string `json:"field"`
	Message  string `json:"message"`
}

// validateNodeImportData 校验导入的节点数据：host 有效且无可疑地址，base_path 为绝对路径。
func validateNodeImportData(nodeData map[string]interface{}, idx int) []importValidationError {
	var errs []importValidationError
	name, _ := nodeData["name"].(string)

	host, _ := nodeData["host"].(string)
	port := 22
	if p, ok := nodeData["port"].(float64); ok {
		port = int(p)
	}
	if err := node.ValidateNodeHostPort(host, port); err != nil {
		errs = append(errs, importValidationError{
			Resource: "nodes",
			Index:    idx,
			Name:     name,
			Field:    "host",
			Message:  err.Error(),
		})
	}

	if basePath, ok := nodeData["base_path"].(string); ok && basePath != "" {
		if err := validateImportPath(basePath); err != nil {
			errs = append(errs, importValidationError{
				Resource: "nodes",
				Index:    idx,
				Name:     name,
				Field:    "base_path",
				Message:  err.Error(),
			})
		}
	}

	return errs
}

// validateImportPath 校验导入路径必须是绝对路径（以 / 开头），防止路径遍历注入。
func validateImportPath(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil // 空路径视为未设置，不报错
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("路径必须是绝对路径（以 / 开头）: %s", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("路径不能包含 ..（路径遍历检测）: %s", p)
	}
	return nil
}

func readStringField(values map[string]interface{}, key string) string {
	raw, _ := values[key].(string)
	return strings.TrimSpace(raw)
}
func readImportedBoolField(values map[string]interface{}, key string) (bool, bool) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return false, false
	}
	switch value := raw.(type) {
	case bool:
		return value, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return false, false
	}
}

func readImportedIntField(values map[string]interface{}, key string) (int, bool) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch value := raw.(type) {
	case float64:
		return int(value), true
	case json.Number:
		parsed, err := strconv.Atoi(value.String())
		return parsed, err == nil
	case int:
		return value, true
	case int64:
		return int(value), true
	case uint:
		return int(value), true
	default:
		return 0, false
	}
}

// applyImportedPolicyFields copies every policy field emitted by config
// export, plus encrypted hook fields accepted by older/manual exports. It
// intentionally leaves omitted fields untouched so model defaults remain
// meaningful, while explicit false/zero values are retained by the shared
// policy create boundary.
func applyImportedPolicyFields(target *model.Policy, data map[string]interface{}) error {
	if target == nil {
		return fmt.Errorf("import policy target is required")
	}
	for key, dst := range map[string]*string{
		"description":        &target.Description,
		"source_path":        &target.SourcePath,
		"target_path":        &target.TargetPath,
		"exclude_rules":      &target.ExcludeRules,
		"bandwidth_schedule": &target.BandwidthSchedule,
		"retention_mode":     &target.RetentionMode,
		"drill_cron":         &target.DrillCron,
		"drill_restore_path": &target.DrillRestorePath,
		"pre_hook":           &target.PreHook,
		"post_hook":          &target.PostHook,
		"drill_pre_verify":   &target.DrillPreVerify,
		"drill_verify":       &target.DrillVerify,
		"drill_post_verify":  &target.DrillPostVerify,
		"app_profile":        &target.AppProfile,
	} {
		if value, ok := data[key].(string); ok {
			*dst = strings.TrimSpace(value)
		}
	}
	if raw, exists := data["cron_spec"]; exists {
		if value, ok := raw.(string); ok {
			value = strings.TrimSpace(value)
			if err := validateCronSpec(value); err != nil {
				return err
			}
			target.CronSpec = value
		}
	}
	for key, dst := range map[string]*int{
		"bwlimit":               &target.BwLimit,
		"retention_days":        &target.RetentionDays,
		"keep_daily":            &target.KeepDaily,
		"keep_weekly":           &target.KeepWeekly,
		"keep_monthly":          &target.KeepMonthly,
		"keep_yearly":           &target.KeepYearly,
		"max_concurrent":        &target.MaxConcurrent,
		"verify_sample_rate":    &target.VerifySampleRate,
		"max_execution_seconds": &target.MaxExecutionSeconds,
		"max_retries":           &target.MaxRetries,
		"retry_base_seconds":    &target.RetryBaseSeconds,
		"rpo_minutes":           &target.RPOMinutes,
		"rto_minutes":           &target.RTOMinutes,
		"hook_timeout_seconds":  &target.HookTimeoutSeconds,
	} {
		if value, ok := readImportedIntField(data, key); ok {
			*dst = value
		}
	}
	for key, dst := range map[string]*bool{
		"enabled":            &target.Enabled,
		"skip_next":          &target.SkipNext,
		"verify_enabled":     &target.VerifyEnabled,
		"is_template":        &target.IsTemplate,
		"drill_enabled":      &target.DrillEnabled,
		"drill_auto_cleanup": &target.DrillAutoCleanup,
	} {
		if value, ok := data[key].(bool); ok {
			*dst = value
		}
	}
	for key, dst := range map[string]**uint{
		"app_credential_id":    &target.AppCredentialID,
		"escalation_policy_id": &target.EscalationPolicyID,
		"drill_target_node_id": &target.DrillTargetNodeID,
	} {
		if _, exists := data[key]; !exists {
			continue
		}
		raw := data[key]
		if raw == nil {
			*dst = nil
			continue
		}
		if value, ok := normalizeUintValue(raw); ok {
			*dst = &value
		} else {
			*dst = nil
		}
	}
	return nil
}

func canonicalLegacyRsyncImportConfig() string {
	return fmt.Sprintf(`{"version":%d,"publication_mode":"legacy_mutable"}`, taskPkg.RsyncPublicationConfigV1Version)
}

func canonicalLegacyRcloneImportConfig() string {
	return fmt.Sprintf(`{"version":%d,"publication_mode":"legacy_mutable"}`, taskPkg.RcloneTaskConfigV1Version)
}

// importedRsyncConfigRequiresDisconnect reads only the top-level publication
// mode. It does not decode, retain, or trust foreign roots, preflight IDs, or
// any other provider configuration; a versioned mode is enough to force the
// imported task through a fresh local migration.
func importedRsyncConfigRequiresDisconnect(executorType, rawConfig string) bool {
	normalizedExecutor := strings.TrimSpace(strings.ToLower(executorType))
	if normalizedExecutor != "" && normalizedExecutor != "rsync" {
		return false
	}
	return importedConfigHasManagedMode(rawConfig, "versioned_hardlink", "versioned_full_copy")
}

// importedRcloneConfigRequiresDisconnect intentionally inspects only the
// top-level publication mode. Foreign V3 binding, preflight and provider
// fields are never decoded or retained; either managed mode forces a fresh
// local setup before the task can run.
func importedRcloneConfigRequiresDisconnect(executorType, rawConfig string) bool {
	normalizedExecutor := strings.TrimSpace(strings.ToLower(executorType))
	if normalizedExecutor != "" && normalizedExecutor != "rclone" {
		return false
	}
	return importedConfigHasManagedMode(rawConfig, "versioned_prefix", "native_object_versions")
}

func importedConfigHasManagedMode(rawConfig string, managedModes ...string) bool {
	decoder := json.NewDecoder(strings.NewReader(rawConfig))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	for decoder.More() {
		member, err := decoder.Token()
		if err != nil {
			return false
		}
		name, ok := member.(string)
		if !ok {
			return false
		}
		if name == "publication_mode" {
			var mode string
			if err := decoder.Decode(&mode); err != nil {
				return false
			}
			for _, managedMode := range managedModes {
				if mode == managedMode {
					return true
				}
			}
			continue
		}
		if err := discardImportedConfigValue(decoder); err != nil {
			return false
		}
	}
	return false
}

func discardImportedConfigValue(decoder *json.Decoder) error {
	value, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := value.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			member, err := decoder.Token()
			if err != nil {
				return err
			}
			if _, ok := member.(string); !ok {
				return fmt.Errorf("invalid imported config object member")
			}
			if err := discardImportedConfigValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("invalid imported config object")
		}
	case '[':
		for decoder.More() {
			if err := discardImportedConfigValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("invalid imported config array")
		}
	default:
		return fmt.Errorf("invalid imported config delimiter")
	}
	return nil
}

type importedSSHKeyScopeCandidate struct {
	disabled        *bool
	expiresAt       *time.Time
	expiresAtSet    bool
	allowedPurposes *string
	allowedNodeIDs  *string
	allowedNodeTags *string
	unsafeCode      string
}

func parseImportedSSHKeyScope(data map[string]interface{}) (importedSSHKeyScopeCandidate, string) {
	var candidate importedSSHKeyScopeCandidate
	invalid := false

	if raw, ok := data["disabled"]; ok {
		value, valid := raw.(bool)
		if !valid {
			invalid = true
		} else {
			candidate.disabled = &value
		}
	}
	if raw, ok := data["expires_at"]; ok {
		candidate.expiresAtSet = true
		switch value := raw.(type) {
		case nil:
			// Explicit null clears the expiry.
		case string:
			value = strings.TrimSpace(value)
			if value != "" {
				parsed, err := time.Parse(time.RFC3339, value)
				if err != nil {
					invalid = true
				} else {
					parsed = parsed.UTC()
					candidate.expiresAt = &parsed
				}
			}
		default:
			invalid = true
		}
	}
	if raw, ok := data["allowed_purposes"]; ok {
		value, valid := raw.(string)
		if !valid {
			invalid = true
		} else if normalized, err := sshutil.NormalizePurposeList(strings.TrimSpace(value)); err != nil {
			invalid = true
		} else {
			candidate.allowedPurposes = &normalized
		}
	}
	if raw, ok := data["allowed_node_ids"]; ok {
		value, valid := raw.(string)
		if !valid {
			invalid = true
		} else if normalized, err := sshutil.NormalizeNodeIDList(strings.TrimSpace(value)); err != nil {
			invalid = true
		} else {
			candidate.allowedNodeIDs = &normalized
		}
	}
	if raw, ok := data["allowed_node_tags"]; ok {
		value, valid := raw.(string)
		if !valid {
			invalid = true
		} else {
			normalized := sshutil.NormalizeTagList(strings.TrimSpace(value))
			candidate.allowedNodeTags = &normalized
		}
	}
	if invalid {
		candidate.unsafeCode = configImportWarningInvalidScope
	} else if candidate.allowedNodeIDs != nil && strings.TrimSpace(*candidate.allowedNodeIDs) != "" {
		candidate.unsafeCode = configImportWarningUnresolvedNodeScope
	}
	return candidate, candidate.unsafeCode
}

func applyImportedSSHKeyScopeCandidate(key *model.SSHKey, candidate importedSSHKeyScopeCandidate) {
	if key == nil {
		return
	}
	if candidate.disabled != nil {
		key.Disabled = *candidate.disabled
	}
	if candidate.expiresAtSet {
		key.ExpiresAt = candidate.expiresAt
	}
	if candidate.allowedPurposes != nil {
		key.AllowedPurposes = *candidate.allowedPurposes
	}
	if candidate.allowedNodeIDs != nil {
		key.AllowedNodeIDs = *candidate.allowedNodeIDs
	}
	if candidate.allowedNodeTags != nil {
		key.AllowedNodeTags = *candidate.allowedNodeTags
	}
}

func importedSSHKeySelectedType(data map[string]interface{}, fallback string) (string, error) {
	raw, ok := data["key_type"]
	if !ok || raw == nil {
		return sshutil.NormalizeKeyType(fallback), nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("imported key_type must be a string")
	}
	return sshutil.NormalizeKeyType(value), nil
}

func prepareImportedSSHKeyPrivateKey(
	data map[string]interface{},
	existing *model.SSHKey,
	selectedType string,
) (string, string, error) {
	if raw, ok := data["private_key"]; ok && raw != nil {
		value, valid := raw.(string)
		if !valid {
			return "", "", fmt.Errorf("imported private_key must be a string")
		}
		if strings.TrimSpace(value) != "" {
			return sshutil.ValidateAndPreparePrivateKey(value, selectedType)
		}
	}
	if existing != nil && strings.TrimSpace(existing.PrivateKey) != "" {
		return sshutil.ValidateAndPreparePrivateKey(existing.PrivateKey, selectedType)
	}
	return "", sshutil.NormalizeKeyType(selectedType), nil
}

func importedNodeSourceSSHKeyIDPresent(data map[string]interface{}) bool {
	raw, ok := data["ssh_key_id"]
	if !ok || raw == nil {
		return false
	}
	if value, ok := raw.(string); ok {
		return strings.TrimSpace(value) != ""
	}
	return true
}

func importTaskFieldSpecified(data map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		raw, ok := data[key]
		if !ok || raw == nil {
			continue
		}
		if value, ok := raw.(string); ok {
			if strings.TrimSpace(value) != "" {
				return true
			}
			continue
		}
		return true
	}
	return false
}

// applyImportedTaskCronCursor updates only the cursor owned by this task's
// current retry provenance. The caller must hold the Task row lock in tx.
// Legacy retrying rows retain Task.NextRunAt as their retry deadline, while
// marked rows keep their retry deadline on TaskRunEffect.NextAttemptAt and
// re-anchor the regular cron cursor on a changed schedule.
func applyImportedTaskCronCursor(
	tx *gorm.DB,
	task *model.Task,
	previousCronSpec string,
	nextCronSpec string,
) error {
	if tx == nil || task == nil {
		return fmt.Errorf("导入任务调度归属不可用")
	}
	cronChanged := strings.TrimSpace(previousCronSpec) != strings.TrimSpace(nextCronSpec)
	if strings.EqualFold(strings.TrimSpace(task.Status), model.TaskRunStatusRetrying) {
		mode, err := model.LatestTaskRetryEffectCronCursorModeTx(tx, task.ID)
		if err != nil {
			return fmt.Errorf("读取导入任务重试调度归属失败(task_id=%d): %w", task.ID, err)
		}
		if mode == model.TaskRunCronCursorModeRegularV1 {
			if !task.Enabled {
				task.NextRunAt = nil
			} else if cronChanged {
				task.NextRunAt = cronutil.Next(nextCronSpec)
			}
		}
		return nil
	}
	if !task.Enabled {
		task.NextRunAt = nil
	} else if cronChanged {
		task.NextRunAt = cronutil.Next(nextCronSpec)
	}
	return nil
}

func normalizeUintValue(raw interface{}) (uint, bool) {
	switch value := raw.(type) {
	case json.Number:
		parsed, err := value.Int64()
		if err == nil && parsed > 0 {
			return uint(parsed), true
		}
	case float64:
		if value > 0 {
			return uint(value), true
		}
	case int:
		if value > 0 {
			return uint(value), true
		}
	case string:
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err == nil && parsed > 0 {
			return uint(parsed), true
		}
	}
	return 0, false
}

func buildImportTaskKey(name string, nodeID uint) importTaskKey {
	return importTaskKey{name: strings.TrimSpace(name), nodeID: nodeID}
}
