package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/cronutil"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/node"
	policyPkg "xirang/backend/internal/policy"
	"xirang/backend/internal/repository"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/secure"
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
	req                     taskPkg.CreateTaskInput
	dependencyKey           importTaskKey
	hasDependency           bool
	dependencyID            *uint
	taskIndex               int
	name                    string
	existing                bool
	rejected                bool
	hasImportedCronOverride bool
	importedCronOverride    bool
	managedRsync            bool
	managedRclone           bool
	hasExplicitEnabled      bool
	requestedEnabled        bool
	previousCronSpec        string
}

// configImportTaskRefRepository overlays the not-yet-published task
// candidates on the transaction repository. ValidateTaskRefs can therefore
// validate a complete imported graph, including forward references, without
// requiring a write-and-compensate pass.
type configImportTaskRefRepository struct {
	repository.TaskRepository
	candidates map[uint]model.Task
}

func (r *configImportTaskRefRepository) ExistsLiveByID(ctx context.Context, id uint) (bool, error) {
	if r != nil {
		if candidate, ok := r.candidates[id]; ok {
			return candidate.ArchivedAt == nil, nil
		}
	}
	return r.TaskRepository.ExistsLiveByID(ctx, id)
}

func (r *configImportTaskRefRepository) FindByIDFields(ctx context.Context, id uint, fields ...string) (*model.Task, error) {
	if r != nil {
		if candidate, ok := r.candidates[id]; ok {
			copy := candidate
			return &copy, nil
		}
	}
	return r.TaskRepository.FindByIDFields(ctx, id, fields...)
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

type configImportAccumulator struct {
	result configImportResult
}

func newConfigImportAccumulator() *configImportAccumulator {
	return &configImportAccumulator{
		result: configImportResult{
			Warnings: make([]configImportWarning, 0, configImportWarningLimit),
		},
	}
}

func (a *configImportAccumulator) warning(entity string, index int, name, code string) {
	if a == nil {
		return
	}
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

// Export godoc
// @Summary      导出配置
// @Description  导出节点、SSH 密钥、策略、任务配置为 JSON；默认不含敏感字段，include_secrets=true 且 admin 权限时可导出
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
		// H3: 审计日志 — 记录敏感数据导出
		userID, _ := c.Get("user_id")
		username, _ := c.Get("username")
		logger.Module("audit").Warn().
			Interface("user_id", userID).
			Interface("username", username).
			Msg("管理员导出了包含敏感数据的配置")
	}

	var nodes []model.Node
	if err := h.db.Find(&nodes).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	var sshKeys []model.SSHKey
	if err := h.db.Find(&sshKeys).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	var policies []model.Policy
	if err := h.db.Preload("Nodes").Find(&policies).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	var tasks []model.Task
	if err := h.db.Preload("Node").Preload("Policy").Find(&tasks).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	taskLookup := make(map[uint]model.Task, len(tasks))
	for _, task := range tasks {
		taskLookup[task.ID] = task
	}

	// 构建节点导出数据
	exportNodes := make([]gin.H, 0, len(nodes))
	for _, n := range nodes {
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
		exportNodes = append(exportNodes, item)
	}

	// 构建密钥导出数据
	exportKeys := make([]gin.H, 0, len(sshKeys))
	for _, k := range sshKeys {
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
		exportKeys = append(exportKeys, item)
	}

	// 构建策略导出数据
	exportPolicies := make([]gin.H, 0, len(policies))
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
		exportPolicies = append(exportPolicies, item)
	}

	// 构建任务导出数据
	exportTasks := make([]gin.H, 0, len(tasks))
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
		exportTasks = append(exportTasks, item)
	}

	// 导出系统设置（仅 DB 覆盖值）
	var dbSettings []model.SystemSetting
	if err := h.db.Find(&dbSettings).Error; err != nil {
		respondInternalError(c, err)
		return
	}
	exportSettings := make([]gin.H, 0, len(dbSettings))
	for _, s := range dbSettings {
		if settings.IsInternalSettingKey(s.Key) {
			continue
		}
		if !includeSecrets && configExportSettingLooksSensitive(s) {
			continue
		}
		exportSettings = append(exportSettings, gin.H{
			"key":   s.Key,
			"value": s.Value,
		})
	}

	documentID, err := backupasset.NewOpaqueID()
	if err != nil {
		respondInternalError(c, err)
		return
	}
	assetGraph, assetCounts, err := h.buildConfigAssetExportGraph(includeSecrets)
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
			"node_count":             len(exportNodes),
			"key_count":              len(exportKeys),
			"policy_count":           len(exportPolicies),
			"task_count":             len(exportTasks),
			"setting_count":          len(exportSettings),
			"repository_count":       assetCounts.Repositories,
			"link_count":             assetCounts.Links,
			"retention_policy_count": assetCounts.Policies,
			"hold_count":             assetCounts.Holds,
		},
	})

	respondOK(c, gin.H{
		"document_id": documentID,
		"version":     configExportVersion2,
		"exported_at": time.Now().Format(time.RFC3339),
		"data": gin.H{
			"nodes":                     exportNodes,
			"ssh_keys":                  exportKeys,
			"policies":                  exportPolicies,
			"tasks":                     exportTasks,
			"system_settings":           exportSettings,
			"backup_repositories":       assetGraph.BackupRepositories,
			"task_repository_links":     assetGraph.TaskRepositoryLinks,
			"backup_retention_policies": assetGraph.BackupRetentionPolicies,
			"recovery_point_holds":      assetGraph.RecoveryPointHolds,
		},
	})
}

// Import godoc
// @Summary      导入配置
// @Description  从 JSON 文件导入节点、SSH 密钥、策略、任务配置；conflict 参数控制冲突策略
// @Tags         config
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        conflict  query     string  false  "冲突策略（skip 默认/overwrite）"
// @Param        body      body      object  true   "配置 JSON 数据"
// @Success      200  {object}  handlers.Response{data=configImportResult}
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /config/import [post]
func (h *ConfigHandler) Import(c *gin.Context) {
	conflict := c.DefaultQuery("conflict", "skip")
	if conflict != "skip" && conflict != "overwrite" {
		respondBadRequest(c, "conflict 参数仅支持 skip 或 overwrite")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20) // 10MB

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			respondBadRequest(c, "导入文件超过 10MB 限制")
			return
		}
		respondBadRequest(c, "读取导入数据失败")
		return
	}

	envelope, err := decodeConfigImportEnvelope(body)
	if err != nil {
		respondBadRequest(c, "无效的导入数据")
		return
	}
	if err := validateConfigImportEnvelope(envelope); err != nil {
		logger.Module("config").Warn().Msg("配置导入包络校验失败")
		respondBadRequest(c, "无效的导入数据")
		return
	}
	data := envelope.Classic
	settingsPlan, foundationSettings, err := h.normalizeImportSettings(data.SystemSettings)
	if err != nil {
		logger.Module("config").Warn().Msg("配置导入系统设置校验失败")
		respondBadRequest(c, "导入系统设置无效")
		return
	}

	accumulator := newConfigImportAccumulator()
	preRejected := map[string]map[int]bool{}
	markPreRejected := func(entity string, index int) {
		indexes := preRejected[entity]
		if indexes == nil {
			indexes = make(map[int]bool)
			preRejected[entity] = indexes
		}
		if indexes[index] {
			return
		}
		indexes[index] = true
	}

	// Path and node-address validation remains fail-closed, but invalid
	// individual records are now represented as rejected warnings so valid
	// records in the same import can still be committed.
	for i, nodeData := range data.Nodes {
		if errs := validateNodeImportData(nodeData, i); len(errs) > 0 {
			markPreRejected(configImportEntityNodes, i)
		}
	}
	for i, policyData := range data.Policies {
		invalid := false
		if src, ok := policyData["source_path"].(string); ok && src != "" {
			invalid = invalid || validateImportPath(src) != nil
		}
		if tgt, ok := policyData["target_path"].(string); ok && tgt != "" {
			invalid = invalid || validateImportPath(tgt) != nil
		}
		if invalid {
			markPreRejected(configImportEntityPolicies, i)
		}
	}
	for i, taskData := range data.Tasks {
		executorType := readStringField(taskData, "executor_type")
		executorConfig := readStringField(taskData, "executor_config")
		if importedRsyncConfigRequiresDisconnect(executorType, executorConfig) {
			continue
		}
		managedRcloneImport := importedRcloneConfigRequiresDisconnect(executorType, executorConfig)
		invalid := false
		if src, ok := taskData["rsync_source"].(string); ok && src != "" {
			invalid = invalid || validateImportPath(src) != nil
		}
		if tgt, ok := taskData["rsync_target"].(string); ok && tgt != "" && !managedRcloneImport {
			invalid = invalid || validateImportPath(tgt) != nil
		}
		if invalid {
			markPreRejected(configImportEntityTasks, i)
		}
	}
	rollbackJournal := newConfigImportRollbackJournal(h.db, h.settingsSvc)

	persistImport := func(persistCtx context.Context) error {
		var rollbackSnapshot *configImportRollbackSnapshot
		err := h.db.WithContext(persistCtx).Transaction(func(tx *gorm.DB) error {
			if len(data.Tasks) > 0 {
				if err := policyPkg.LockTargetOwnershipSpace(tx); err != nil {
					return fmt.Errorf("锁定导入任务备份目标失败: %w", err)
				}
			}
			// Create repos from tx for task helper functions.
			importNodeRepo := gormrepo.NewNodeRepository(tx)
			importPolicyRepo := gormrepo.NewPolicyRepository(tx)
			importTaskRepo := gormrepo.NewTaskRepository(tx)

			if len(data.Tasks) > 0 {
				var existingTargetPolicies []model.Policy
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Select("id").
					Order("id").
					Find(&existingTargetPolicies).Error; err != nil {
					return fmt.Errorf("查询现有策略目标归属失败: %w", err)
				}
			}
			targetClaims := make([]policyPkg.TargetOwner, 0)
			if len(data.Tasks) > 0 {
				var existingTargetTasks []model.Task
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Select("id", "node_id", "policy_id", "executor_type", "rsync_target").
					Order("id").Find(&existingTargetTasks).Error; err != nil {
					return fmt.Errorf("查询现有任务目标归属失败: %w", err)
				}
				for _, existingTask := range existingTargetTasks {
					if !policyPkg.IsCoreLocalTarget(existingTask.ExecutorType, existingTask.RsyncTarget) {
						continue
					}
					claim := policyPkg.TargetOwner{
						NodeID: existingTask.NodeID,
						TaskID: existingTask.ID,
						Target: existingTask.RsyncTarget,
					}
					if existingTask.PolicyID != nil {
						claim.PolicyID = *existingTask.PolicyID
					}
					targetClaims = append(targetClaims, claim)
				}
			}
			var captureErr error
			rollbackSnapshot, captureErr = captureConfigImportRollbackSnapshot(
				persistCtx, tx, data, settingsPlan, envelope,
			)
			if captureErr != nil {
				return captureErr
			}
			validateImportedTarget := func(req taskPkg.CreateTaskInput, taskID uint, reserve bool) error {
				target := strings.TrimSpace(req.RsyncTarget)
				if target == "" || !filepath.IsAbs(target) {
					return nil
				}
				owner := policyPkg.TargetOwner{NodeID: req.NodeID, TaskID: taskID, Target: target}
				if req.PolicyID != nil {
					owner.PolicyID = *req.PolicyID
				}
				if _, err := policyPkg.ValidateTargetOwnership(target, owner, targetClaims); err != nil {
					return fmt.Errorf("%w: 导入任务 %q 的备份目标存在重叠或归属不明: %w", errConfigImportTargetConflict, req.Name, err)
				}
				if reserve {
					targetClaims = append(targetClaims, owner)
				}
				return nil
			}

			// 导入 SSH 密钥
			for keyIndex, keyData := range data.SSHKeys {
				name, _ := keyData["name"].(string)
				name = strings.TrimSpace(name)
				if name == "" {
					accumulator.rejected(configImportEntitySSHKeys, keyIndex, name, configImportWarningInvalidInput)
					continue
				}

				var existingSSHKeyPlaceholder model.SSHKey
				result := tx.Session(&gorm.Session{SkipHooks: true}).
					Where("name = ?", name).Limit(1).Find(&existingSSHKeyPlaceholder)
				if result.Error != nil {
					return fmt.Errorf("查询导入 SSH 密钥失败: %w", result.Error)
				}
				found := result.RowsAffected > 0
				if found && conflict != "overwrite" {
					accumulator.skipped()
					continue
				}

				scope, scopeCode := parseImportedSSHKeyScope(keyData)
				if found && scopeCode != "" {
					accumulator.rejected(configImportEntitySSHKeys, keyIndex, name, scopeCode)
					continue
				}

				if found {
					existing := existingSSHKeyPlaceholder
					sourcePrivateKey, sourcePrivateKeyProvided := keyData["private_key"].(string)
					if (!sourcePrivateKeyProvided || strings.TrimSpace(sourcePrivateKey) == "") &&
						strings.TrimSpace(existing.PrivateKey) != "" {
						decrypted, decryptErr := secure.DecryptIfNeeded(existing.PrivateKey)
						if decryptErr != nil {
							accumulator.rejected(configImportEntitySSHKeys, keyIndex, name, configImportWarningInvalidPrivateKey)
							continue
						}
						existing.PrivateKey = decrypted
					}
					selectedType, err := importedSSHKeySelectedType(keyData, existing.KeyType)
					if err != nil {
						accumulator.rejected(configImportEntitySSHKeys, keyIndex, name, configImportWarningInvalidInput)
						continue
					}
					preparedKey, storedType, err := prepareImportedSSHKeyPrivateKey(keyData, &existing, selectedType)
					if err != nil {
						accumulator.rejected(configImportEntitySSHKeys, keyIndex, name, configImportWarningInvalidPrivateKey)
						continue
					}

					candidate := existing
					if username, ok := keyData["username"].(string); ok {
						candidate.Username = strings.TrimSpace(username)
					}
					candidate.KeyType = storedType
					candidate.PrivateKey = preparedKey
					if strings.TrimSpace(preparedKey) == "" {
						candidate.PrivateKey = ""
						candidate.Fingerprint = ""
						candidate.Disabled = true
						accumulator.warning(configImportEntitySSHKeys, keyIndex, name, configImportWarningMissingPrivateKey)
					} else {
						candidate.Fingerprint = generateFingerprint(preparedKey)
					}
					applyImportedSSHKeyScopeCandidate(&candidate, scope)
					if strings.TrimSpace(candidate.PrivateKey) == "" {
						candidate.Fingerprint = ""
						candidate.Disabled = true
					}
					if err := tx.Save(&candidate).Error; err != nil {
						return fmt.Errorf("保存导入 SSH 密钥失败: %w", err)
					}
					accumulator.updated(configImportEntitySSHKeys)
					continue
				}

				selectedType, err := importedSSHKeySelectedType(keyData, "")
				if err != nil {
					accumulator.rejected(configImportEntitySSHKeys, keyIndex, name, configImportWarningInvalidInput)
					continue
				}
				preparedKey, storedType, err := prepareImportedSSHKeyPrivateKey(keyData, nil, selectedType)
				if err != nil {
					accumulator.rejected(configImportEntitySSHKeys, keyIndex, name, configImportWarningInvalidPrivateKey)
					continue
				}
				newKey := model.SSHKey{Name: name, KeyType: storedType}
				if username, ok := keyData["username"].(string); ok {
					newKey.Username = strings.TrimSpace(username)
				}
				newKey.PrivateKey = preparedKey
				if strings.TrimSpace(preparedKey) != "" {
					newKey.Fingerprint = generateFingerprint(preparedKey)
				} else {
					newKey.Fingerprint = ""
					newKey.Disabled = true
					accumulator.warning(configImportEntitySSHKeys, keyIndex, name, configImportWarningMissingPrivateKey)
				}
				applyImportedSSHKeyScopeCandidate(&newKey, scope)
				if scopeCode != "" {
					accumulator.warning(configImportEntitySSHKeys, keyIndex, name, scopeCode)
					newKey.Disabled = true
					newKey.AllowedNodeIDs = ""
				}
				if strings.TrimSpace(newKey.PrivateKey) == "" {
					newKey.Fingerprint = ""
					newKey.Disabled = true
				}
				if err := tx.Create(&newKey).Error; err != nil {
					return fmt.Errorf("创建导入 SSH 密钥失败: %w", err)
				}
				accumulator.created(configImportEntitySSHKeys, newKey.Disabled)
			}

			// 导入节点
			for nodeIndex, nodeData := range data.Nodes {
				name, _ := nodeData["name"].(string)
				name = strings.TrimSpace(name)
				if name == "" {
					accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
					continue
				}

				var existing model.Node
				result := tx.Where("name = ?", name).Limit(1).Find(&existing)
				if result.Error != nil {
					return fmt.Errorf("查询导入节点失败: %w", result.Error)
				}
				found := result.RowsAffected > 0
				if found && conflict != "overwrite" {
					accumulator.skipped()
					continue
				}
				if preRejected[configImportEntityNodes][nodeIndex] {
					accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
					continue
				}

				if found {
					candidate := existing
					if host, ok := nodeData["host"].(string); ok {
						candidate.Host = strings.TrimSpace(host)
					}
					if port, ok := nodeData["port"].(float64); ok {
						candidate.Port = int(port)
					}
					if username, ok := nodeData["username"].(string); ok {
						candidate.Username = strings.TrimSpace(username)
					}
					if authType, ok := nodeData["auth_type"].(string); ok {
						candidate.AuthType = strings.ToLower(strings.TrimSpace(authType))
					}
					if tags, ok := nodeData["tags"].(string); ok {
						candidate.Tags = tags
					}
					if basePath, ok := nodeData["base_path"].(string); ok {
						candidate.BasePath = strings.TrimSpace(basePath)
					}
					if candidate.Username == "" ||
						(candidate.AuthType != "" && candidate.AuthType != "password" && candidate.AuthType != "key" && candidate.AuthType != "ssh_key") {
						accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
						continue
					}
					if err := node.ValidateNodeHostPort(candidate.Host, candidate.Port); err != nil {
						accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
						continue
					}
					if err := tx.Save(&candidate).Error; err != nil {
						return fmt.Errorf("保存导入节点失败: %w", err)
					}
					accumulator.updated(configImportEntityNodes)
					addImportedNodeCredentialWarnings(accumulator, nodeIndex, name, nodeData, candidate)
					continue
				}

				newNode := model.Node{
					Name:     name,
					Status:   "offline",
					Port:     22,
					AuthType: "key",
				}
				if host, ok := nodeData["host"].(string); ok {
					newNode.Host = strings.TrimSpace(host)
				}
				if port, ok := nodeData["port"].(float64); ok {
					newNode.Port = int(port)
				}
				if username, ok := nodeData["username"].(string); ok {
					newNode.Username = strings.TrimSpace(username)
				}
				if authType, ok := nodeData["auth_type"].(string); ok {
					newNode.AuthType = strings.ToLower(strings.TrimSpace(authType))
				}
				if tags, ok := nodeData["tags"].(string); ok {
					newNode.Tags = tags
				}
				if basePath, ok := nodeData["base_path"].(string); ok {
					newNode.BasePath = strings.TrimSpace(basePath)
				}
				if password, ok := nodeData["password"].(string); ok {
					newNode.Password = password
				}
				if privateKey, ok := nodeData["private_key"].(string); ok {
					newNode.PrivateKey = privateKey
				}
				if newNode.Username == "" ||
					(newNode.AuthType != "" && newNode.AuthType != "password" && newNode.AuthType != "key" && newNode.AuthType != "ssh_key") {
					accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
					continue
				}
				if err := node.ValidateNodeHostPort(newNode.Host, newNode.Port); err != nil {
					accumulator.rejected(configImportEntityNodes, nodeIndex, name, configImportWarningInvalidInput)
					continue
				}
				if err := tx.Create(&newNode).Error; err != nil {
					return fmt.Errorf("创建导入节点失败: %w", err)
				}
				accumulator.created(configImportEntityNodes, false)
				addImportedNodeCredentialWarnings(accumulator, nodeIndex, name, nodeData, newNode)
			}

			// 导入策略
			for policyIndex, policyData := range data.Policies {
				name, _ := policyData["name"].(string)
				name = strings.TrimSpace(name)
				if name == "" {
					accumulator.rejected(configImportEntityPolicies, policyIndex, name, configImportWarningInvalidInput)
					continue
				}
				var existing model.Policy
				result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("name = ?", name).Limit(1).Find(&existing)
				if result.Error != nil {
					return fmt.Errorf("查询导入策略失败: %w", result.Error)
				}
				found := result.RowsAffected > 0
				if found && conflict != "overwrite" {
					accumulator.skipped()
					continue
				}
				if preRejected[configImportEntityPolicies][policyIndex] {
					accumulator.rejected(configImportEntityPolicies, policyIndex, name, configImportWarningInvalidInput)
					continue
				}
				if found {
					if desc, ok := policyData["description"].(string); ok {
						existing.Description = desc
					}
					if src, ok := policyData["source_path"].(string); ok {
						existing.SourcePath = src
					}
					if tgt, ok := policyData["target_path"].(string); ok {
						existing.TargetPath = tgt
					}
					if cron, ok := policyData["cron_spec"].(string); ok {
						if err := validateCronSpec(cron); err != nil {
							accumulator.rejected(configImportEntityPolicies, policyIndex, name, configImportWarningInvalidInput)
							continue
						}
						existing.CronSpec = cron
					}
					if excl, ok := policyData["exclude_rules"].(string); ok {
						existing.ExcludeRules = excl
					}
					if ret, ok := policyData["retention_days"].(float64); ok {
						existing.RetentionDays = int(ret)
					}
					if maxC, ok := policyData["max_concurrent"].(float64); ok {
						existing.MaxConcurrent = int(maxC)
					}
					if enabled, ok := policyData["enabled"].(bool); ok {
						existing.Enabled = enabled
					}
					if verify, ok := policyData["verify_enabled"].(bool); ok {
						existing.VerifyEnabled = verify
					}
					if sample, ok := policyData["verify_sample_rate"].(float64); ok {
						existing.VerifySampleRate = int(sample)
					}
					if retries, ok := policyData["max_retries"].(float64); ok {
						existing.MaxRetries = int(retries)
					}
					if retryBase, ok := policyData["retry_base_seconds"].(float64); ok {
						existing.RetryBaseSeconds = int(retryBase)
					}
					if mode, ok := policyData["retention_mode"].(string); ok && mode != "" {
						existing.RetentionMode = mode
					}
					for key, dst := range map[string]*int{
						"keep_daily": &existing.KeepDaily, "keep_weekly": &existing.KeepWeekly,
						"keep_monthly": &existing.KeepMonthly, "keep_yearly": &existing.KeepYearly,
						"rpo_minutes": &existing.RPOMinutes, "rto_minutes": &existing.RTOMinutes,
						"max_execution_seconds": &existing.MaxExecutionSeconds,
					} {
						if value, ok := policyData[key].(float64); ok {
							*dst = int(value)
						}
					}
					if err := applyImportedPolicyFields(&existing, policyData); err != nil {
						accumulator.rejected(configImportEntityPolicies, policyIndex, name, configImportWarningInvalidInput)
						continue
					}
					if err := tx.Save(&existing).Error; err != nil {
						return fmt.Errorf("保存导入策略失败: %w", err)
					}
					accumulator.updated(configImportEntityPolicies)
				} else {
					newPolicy := model.Policy{
						Name:               name,
						MaxConcurrent:      1,
						RetentionDays:      7,
						RetentionMode:      "simple",
						Enabled:            false,
						VerifyEnabled:      true,
						HookTimeoutSeconds: 300,
						MaxRetries:         2,
						RetryBaseSeconds:   30,
						DrillRestorePath:   "/tmp/xirang-drill",
						DrillAutoCleanup:   true,
					}
					if desc, ok := policyData["description"].(string); ok {
						newPolicy.Description = desc
					}
					if src, ok := policyData["source_path"].(string); ok {
						newPolicy.SourcePath = src
					}
					if tgt, ok := policyData["target_path"].(string); ok {
						newPolicy.TargetPath = tgt
					}
					if cron, ok := policyData["cron_spec"].(string); ok {
						if err := validateCronSpec(cron); err != nil {
							accumulator.rejected(configImportEntityPolicies, policyIndex, name, configImportWarningInvalidInput)
							continue
						}
						newPolicy.CronSpec = cron
					}
					if excl, ok := policyData["exclude_rules"].(string); ok {
						newPolicy.ExcludeRules = excl
					}
					if ret, ok := policyData["retention_days"].(float64); ok {
						newPolicy.RetentionDays = int(ret)
					}
					if maxC, ok := policyData["max_concurrent"].(float64); ok {
						newPolicy.MaxConcurrent = int(maxC)
					}
					if enabled, ok := policyData["enabled"].(bool); ok {
						newPolicy.Enabled = enabled
					}
					if verify, ok := policyData["verify_enabled"].(bool); ok {
						newPolicy.VerifyEnabled = verify
					}
					if sample, ok := policyData["verify_sample_rate"].(float64); ok {
						newPolicy.VerifySampleRate = int(sample)
					}
					if retries, ok := policyData["max_retries"].(float64); ok {
						newPolicy.MaxRetries = int(retries)
					}
					if retryBase, ok := policyData["retry_base_seconds"].(float64); ok {
						newPolicy.RetryBaseSeconds = int(retryBase)
					}
					if mode, ok := policyData["retention_mode"].(string); ok && mode != "" {
						newPolicy.RetentionMode = mode
					}
					for key, dst := range map[string]*int{
						"keep_daily": &newPolicy.KeepDaily, "keep_weekly": &newPolicy.KeepWeekly,
						"keep_monthly": &newPolicy.KeepMonthly, "keep_yearly": &newPolicy.KeepYearly,
						"rpo_minutes": &newPolicy.RPOMinutes, "rto_minutes": &newPolicy.RTOMinutes,
						"max_execution_seconds": &newPolicy.MaxExecutionSeconds,
					} {
						if value, ok := policyData[key].(float64); ok {
							*dst = int(value)
						}
					}
					if drillEnabled, ok := policyData["drill_enabled"].(bool); ok {
						newPolicy.DrillEnabled = drillEnabled
					}
					if drillCron, ok := policyData["drill_cron"].(string); ok {
						newPolicy.DrillCron = drillCron
					}
					if drillPath, ok := policyData["drill_restore_path"].(string); ok && drillPath != "" {
						newPolicy.DrillRestorePath = drillPath
					}
					if cleanup, ok := policyData["drill_auto_cleanup"].(bool); ok {
						newPolicy.DrillAutoCleanup = cleanup
					}
					if isTmpl, ok := policyData["is_template"].(bool); ok {
						newPolicy.IsTemplate = isTmpl
					}
					if err := applyImportedPolicyFields(&newPolicy, policyData); err != nil {
						accumulator.rejected(configImportEntityPolicies, policyIndex, name, configImportWarningInvalidInput)
						continue
					}
					if err := importPolicyRepo.CreateWithExplicitValues(persistCtx, &newPolicy, model.PolicyCreateExplicitColumns()...); err != nil {
						return fmt.Errorf("创建导入策略失败: %w", err)
					}
					accumulator.created(configImportEntityPolicies, false)
				}
			}

			// 导入任务采用两阶段边界：先解析并校验完整候选图，再发布
			// 行变更和计数。这样无效依赖不会留下半成品任务或虚假的
			// created/updated 结果，同时仍允许引用后续导入的节点和任务。
			importTaskNodeIDs := make([]uint, len(data.Tasks))
			importTaskNames := make([]string, len(data.Tasks))
			for taskIndex, taskData := range data.Tasks {
				name, _ := taskData["name"].(string)
				name = strings.TrimSpace(name)
				importTaskNames[taskIndex] = name
				if name == "" {
					continue
				}
				nodeID, ok, err := resolveImportNodeID(tx, taskData)
				if err != nil {
					return fmt.Errorf("解析导入任务节点失败: %w", err)
				}
				if ok {
					importTaskNodeIDs[taskIndex] = nodeID
				}
			}

			nextSyntheticTaskID := ^uint(0)
			provisionalTaskIDs := make(map[importTaskKey]uint, len(data.Tasks))
			for taskIndex, name := range importTaskNames {
				nodeID := importTaskNodeIDs[taskIndex]
				if name == "" || nodeID == 0 {
					continue
				}
				key := buildImportTaskKey(name, nodeID)
				if _, exists := provisionalTaskIDs[key]; exists {
					continue
				}
				provisionalTaskIDs[key] = nextSyntheticTaskID
				nextSyntheticTaskID--
			}

			taskCandidates := make([]*configImportTaskCandidate, 0, len(data.Tasks))
			candidateByKey := make(map[importTaskKey]*configImportTaskCandidate, len(data.Tasks))
			for taskIndex, taskData := range data.Tasks {
				name := importTaskNames[taskIndex]
				if name == "" {
					accumulator.rejected(configImportEntityTasks, taskIndex, name, configImportWarningInvalidInput)
					continue
				}
				nodeID := importTaskNodeIDs[taskIndex]
				if nodeID == 0 {
					accumulator.rejected(configImportEntityTasks, taskIndex, name, configImportWarningInvalidInput)
					continue
				}

				var existingTask model.Task
				existingTaskResult := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("name = ? AND node_id = ?", name, nodeID).Limit(1).Find(&existingTask)
				if existingTaskResult.Error != nil {
					return fmt.Errorf("查询导入任务失败: %w", existingTaskResult.Error)
				}
				existingTaskFound := existingTaskResult.RowsAffected > 0
				taskKey := buildImportTaskKey(name, nodeID)
				previousCandidate := candidateByKey[taskKey]
				if (existingTaskFound || previousCandidate != nil) && conflict != "overwrite" {
					accumulator.skipped()
					continue
				}
				if preRejected[configImportEntityTasks][taskIndex] {
					accumulator.rejected(configImportEntityTasks, taskIndex, name, configImportWarningInvalidInput)
					continue
				}

				var policyID *uint
				policySpecified := importTaskFieldSpecified(taskData, "policy_name", "policy_id")
				if id, found, err := resolveImportPolicyID(tx, taskData); err != nil {
					return fmt.Errorf("解析导入任务策略失败: %w", err)
				} else if found {
					policyID = &id
				} else if policySpecified {
					accumulator.rejected(configImportEntityTasks, taskIndex, name, configImportWarningInvalidInput)
					continue
				}

				req := taskPkg.CreateTaskInput{
					Name:            name,
					NodeID:          nodeID,
					PolicyID:        policyID,
					DependsOnTaskID: nil,
					Command:         readStringField(taskData, "command"),
					RsyncSource:     readStringField(taskData, "rsync_source"),
					RsyncTarget:     readStringField(taskData, "rsync_target"),
					ExecutorType:    readStringField(taskData, "executor_type"),
					ExecutorConfig:  readStringField(taskData, "executor_config"),
					CronSpec:        readStringField(taskData, "cron_spec"),
				}
				dependencyKey, hasDependency, err := resolveImportedDependencyKey(tx, taskData, req.NodeID)
				if err != nil {
					return fmt.Errorf("解析导入任务依赖失败: %w", err)
				}
				if importTaskFieldSpecified(taskData, "depends_on_task_name") && !hasDependency {
					accumulator.rejected(configImportEntityTasks, taskIndex, name, configImportWarningInvalidInput)
					continue
				}
				importedCronOverride, hasImportedCronOverride := readImportedBoolField(taskData, "cron_override")
				explicitCronSpec := req.CronSpec
				_, hasExplicitCronSpec := taskData["cron_spec"]
				if policyID != nil {
					var importedPolicy model.Policy
					if err := tx.First(&importedPolicy, *policyID).Error; err != nil {
						return fmt.Errorf("读取导入任务策略失败: %w", err)
					}
					if strings.TrimSpace(req.RsyncSource) == "" {
						req.RsyncSource = importedPolicy.SourcePath
					}
					if strings.TrimSpace(req.RsyncTarget) == "" && req.NodeID != 0 {
						req.RsyncTarget = policyPkg.PolicyNodeTargetPath(importedPolicy.TargetPath, importedPolicy.ID, req.NodeID)
					}
					if strings.TrimSpace(req.CronSpec) == "" {
						req.CronSpec = importedPolicy.CronSpec
					}
				}
				if hasImportedCronOverride && importedCronOverride && hasExplicitCronSpec {
					// An explicitly exported empty cron is a deliberate manual
					// schedule; policy hydration must not fill it back in.
					req.CronSpec = explicitCronSpec
				}
				taskPkg.TrimTaskInput(&req)
				taskPkg.InferTaskExecutor(&req, "")
				// Imported snapshots are an explicit compatibility boundary.
				// Normalize legacy Restic config here (including a ciphertext
				// envelope produced by an external snapshot) before model hooks
				// encrypt the destination row. Runtime reads never alias the
				// removed append_only field.
				if strings.EqualFold(strings.TrimSpace(req.ExecutorType), "restic") {
					migratedConfig, migrateErr := taskPkg.NormalizeImportedResticConfig(req.ExecutorConfig)
					if migrateErr != nil {
						accumulator.rejected(configImportEntityTasks, taskIndex, name, configImportWarningInvalidInput)
						continue
					}
					req.ExecutorConfig = migratedConfig
				}
				managedRsyncImport := importedRsyncConfigRequiresDisconnect(req.ExecutorType, req.ExecutorConfig)
				managedRcloneImport := importedRcloneConfigRequiresDisconnect(req.ExecutorType, req.ExecutorConfig)
				if managedRsyncImport {
					req.RsyncSource = ""
					req.RsyncTarget = ""
					req.ExecutorConfig = canonicalLegacyRsyncImportConfig()
				} else if managedRcloneImport {
					req.RsyncTarget = ""
					req.ExecutorConfig = canonicalLegacyRcloneImportConfig()
				} else {
					taskPkg.EnsureNodeTargetPrefix(persistCtx, importNodeRepo, &req)
				}
				if hasDependency && strings.TrimSpace(explicitCronSpec) == "" {
					req.CronSpec = ""
				}
				if !managedRsyncImport && !managedRcloneImport {
					taskPkg.AutoGenerateTarget(persistCtx, importNodeRepo, &req)
				}
				var validationErr error
				if managedRsyncImport {
					validationErr = taskPkg.ValidateDisconnectedImportedRsyncTask(req)
				} else if managedRcloneImport {
					validationErr = taskPkg.ValidateDisconnectedImportedRcloneTask(req)
				} else {
					validationErr = taskPkg.ValidateTaskInput(req)
				}
				if validationErr != nil {
					if !taskPkg.IsTaskValidationError(validationErr) {
						return fmt.Errorf("校验导入任务失败: %w", validationErr)
					}
					accumulator.rejected(configImportEntityTasks, taskIndex, name, configImportWarningInvalidInput)
					continue
				}

				baseTask := existingTask
				existing := existingTaskFound
				if previousCandidate != nil {
					baseTask = previousCandidate.task
					existing = previousCandidate.existing
				}
				candidateTask := baseTask
				if !existing {
					candidateTask = model.Task{
						ID:             provisionalTaskIDs[taskKey],
						Name:           req.Name,
						NodeID:         req.NodeID,
						PolicyID:       req.PolicyID,
						Command:        req.Command,
						RsyncSource:    req.RsyncSource,
						RsyncTarget:    req.RsyncTarget,
						ExecutorType:   req.ExecutorType,
						ExecutorConfig: req.ExecutorConfig,
						CronSpec:       req.CronSpec,
						Status:         "pending",
						Source:         readStringField(taskData, "source"),
						Enabled:        !managedRsyncImport && !managedRcloneImport,
					}
					if hasImportedCronOverride {
						candidateTask.CronOverride = importedCronOverride
					}
					if candidateTask.Source == "" {
						candidateTask.Source = "manual"
					}
				} else {
					candidateTask.DependsOnTaskID = nil
					candidateTask.Command = req.Command
					candidateTask.RsyncSource = req.RsyncSource
					candidateTask.RsyncTarget = req.RsyncTarget
					candidateTask.ExecutorType = req.ExecutorType
					candidateTask.ExecutorConfig = req.ExecutorConfig
					candidateTask.CronSpec = req.CronSpec
					if hasImportedCronOverride {
						candidateTask.CronOverride = importedCronOverride
					}
					candidateTask.Source = readStringField(taskData, "source")
					// Foreign managed publication configuration is always imported paused.
					if managedRsyncImport || managedRcloneImport {
						candidateTask.Enabled = false
					} else if enabled, ok := readImportedBoolField(taskData, "enabled"); ok {
						candidateTask.Enabled = enabled
					}
				}
				explicitEnabled, hasExplicitEnabled := readImportedBoolField(taskData, "enabled")
				if !existing && hasExplicitEnabled && !managedRsyncImport && !managedRcloneImport {
					candidateTask.Enabled = explicitEnabled
				}
				if !existing && candidateTask.Enabled {
					candidateTask.NextRunAt = cronutil.Next(candidateTask.CronSpec)
				}
				candidate := &configImportTaskCandidate{
					task:                    candidateTask,
					req:                     req,
					dependencyKey:           dependencyKey,
					hasDependency:           hasDependency,
					taskIndex:               taskIndex,
					name:                    name,
					existing:                existing,
					hasImportedCronOverride: hasImportedCronOverride,
					importedCronOverride:    importedCronOverride,
					managedRsync:            managedRsyncImport,
					managedRclone:           managedRcloneImport,
					hasExplicitEnabled:      hasExplicitEnabled,
					requestedEnabled:        candidateTask.Enabled,
					previousCronSpec:        strings.TrimSpace(baseTask.CronSpec),
				}
				if previousCandidate != nil && conflict == "overwrite" {
					*previousCandidate = *candidate
					candidate = previousCandidate
				} else {
					taskCandidates = append(taskCandidates, candidate)
				}
				candidateByKey[taskKey] = candidate
			}

			candidateIDs := make(map[importTaskKey]uint, len(taskCandidates))
			candidateRows := make(map[uint]model.Task, len(taskCandidates))
			for _, candidate := range taskCandidates {
				taskKey := buildImportTaskKey(candidate.task.Name, candidate.task.NodeID)
				candidateIDs[taskKey] = candidate.task.ID
				candidateRows[candidate.task.ID] = candidate.task
			}
			rejectCandidate := func(candidate *configImportTaskCandidate) {
				if candidate == nil || candidate.rejected {
					return
				}
				candidate.rejected = true
				accumulator.rejected(configImportEntityTasks, candidate.taskIndex, candidate.name, configImportWarningInvalidInput)
			}

			for _, candidate := range taskCandidates {
				if !candidate.hasDependency {
					candidate.dependencyID = nil
					continue
				}
				dependencyID, ok := candidateIDs[candidate.dependencyKey]
				if !ok {
					var dependency model.Task
					result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
						Where("name = ? AND node_id = ?", candidate.dependencyKey.name, candidate.dependencyKey.nodeID).
						Limit(1).Find(&dependency)
					if result.Error != nil {
						return fmt.Errorf("查询导入任务依赖失败: %w", result.Error)
					}
					if result.RowsAffected == 0 {
						rejectCandidate(candidate)
						continue
					}
					dependencyID = dependency.ID
				}
				if dependencyID == 0 {
					rejectCandidate(candidate)
					continue
				}
				candidate.dependencyID = &dependencyID
				candidate.req.DependsOnTaskID = &dependencyID
				candidate.task.DependsOnTaskID = &dependencyID
				candidateRows[candidate.task.ID] = candidate.task
			}

			taskRefRepo := &configImportTaskRefRepository{
				TaskRepository: importTaskRepo,
				candidates:     candidateRows,
			}
			// The shared single-task validator has a finite depth bound. Walk
			// the complete staged graph (including referenced existing rows)
			// once so a long cycle cannot be accepted as a valid import.
			const (
				dependencyVisiting = 1
				dependencyValid    = 2
				dependencyInvalid  = 3
			)
			dependencyState := make(map[uint]uint8, len(candidateRows))
			dependencyPath := make([]uint, 0, len(candidateRows))
			for _, candidate := range taskCandidates {
				dependencyPath = dependencyPath[:0]
				currentID := candidate.task.ID
				valid := true
				for currentID != 0 {
					if state := dependencyState[currentID]; state != 0 {
						valid = state == dependencyValid
						break
					}
					dependencyState[currentID] = dependencyVisiting
					dependencyPath = append(dependencyPath, currentID)
					row, err := taskRefRepo.FindByIDFields(persistCtx, currentID, "id", "depends_on_task_id")
					if err != nil {
						if errors.Is(err, gorm.ErrRecordNotFound) {
							valid = false
							break
						}
						return fmt.Errorf("读取导入任务依赖图失败: %w", err)
					}
					if row == nil {
						valid = false
						break
					}
					currentID = 0
					if row.DependsOnTaskID != nil {
						currentID = *row.DependsOnTaskID
					}
				}
				state := uint8(dependencyValid)
				if !valid {
					state = dependencyInvalid
					rejectCandidate(candidate)
				}
				for _, id := range dependencyPath {
					dependencyState[id] = state
				}
			}
			for _, candidate := range taskCandidates {
				if candidate.rejected {
					continue
				}
				if err := taskPkg.ValidateTaskRefs(
					persistCtx, importNodeRepo, importPolicyRepo, taskRefRepo,
					candidate.req, candidate.task.ID,
				); err != nil {
					if !taskPkg.IsTaskValidationError(err) {
						return fmt.Errorf("校验导入任务依赖失败: %w", err)
					}
					rejectCandidate(candidate)
				}
			}
			changed := true
			for changed {
				changed = false
				for _, candidate := range taskCandidates {
					if candidate.rejected || candidate.dependencyID == nil {
						continue
					}
					if dependency, ok := candidateRows[*candidate.dependencyID]; ok {
						for _, dependencyCandidate := range taskCandidates {
							if dependencyCandidate.task.ID == dependency.ID && dependencyCandidate.rejected {
								rejectCandidate(candidate)
								changed = true
								break
							}
						}
					}
				}
			}

			publishedTaskIDs := make(map[uint]uint, len(taskCandidates))
			for _, candidate := range taskCandidates {
				if candidate.rejected {
					continue
				}
				candidate.req.DependsOnTaskID = candidate.dependencyID
				// Dependency IDs for new candidates are provisional overlay IDs.
				// Rows are published without that field, then linked after all
				// inserts have returned their real database IDs.
				candidateSourceID := candidate.task.ID
				candidate.task.DependsOnTaskID = nil
				if candidate.existing {
					if err := applyImportedTaskCronCursor(
						tx, &candidate.task, candidate.previousCronSpec, candidate.task.CronSpec,
					); err != nil {
						return fmt.Errorf("更新导入任务调度游标失败: %w", err)
					}
					if err := tx.Save(&candidate.task).Error; err != nil {
						return fmt.Errorf("保存导入任务失败: %w", err)
					}
					publishedTaskIDs[candidateSourceID] = candidate.task.ID
					accumulator.updated(configImportEntityTasks)
					continue
				}

				if err := validateImportedTarget(candidate.req, 0, true); err != nil {
					return err
				}
				candidate.task.ID = 0
				requestedEnabled := candidate.task.Enabled
				if err := tx.Create(&candidate.task).Error; err != nil {
					return fmt.Errorf("创建导入任务失败: %w", err)
				}
				publishedTaskIDs[candidateSourceID] = candidate.task.ID
				// GORM omits false bools when the model declares default:true.
				// Restore explicit task values in this transaction while keeping
				// foreign managed publication tasks paused.
				if candidate.hasExplicitEnabled || candidate.managedRsync || candidate.managedRclone {
					if err := tx.Model(&model.Task{}).Where("id = ?", candidate.task.ID).
						Update("enabled", requestedEnabled).Error; err != nil {
						return fmt.Errorf("恢复导入任务启用状态失败: %w", err)
					}
				}
				accumulator.created(configImportEntityTasks, false)
			}

			for _, candidate := range taskCandidates {
				if candidate.rejected {
					continue
				}
				var dependencyID *uint
				if candidate.dependencyID != nil {
					resolvedID := *candidate.dependencyID
					if publishedID, ok := publishedTaskIDs[resolvedID]; ok {
						resolvedID = publishedID
					}
					dependencyID = &resolvedID
				}
				if err := tx.Model(&model.Task{}).Where("id = ?", candidate.task.ID).
					Update("depends_on_task_id", dependencyID).Error; err != nil {
					return fmt.Errorf("更新导入任务依赖失败: %w", err)
				}
			}

			// 导入系统设置（使用事务 handle 确保原子性）
			if h.settingsSvc != nil {
				for _, setting := range settingsPlan {
					var existingSetting model.SystemSetting
					result := tx.Where("key = ?", setting.key).Limit(1).Find(&existingSetting)
					if result.Error != nil {
						return fmt.Errorf("查询导入系统设置失败: %w", result.Error)
					}
					if err := h.settingsSvc.UpdateWithTxContext(persistCtx, tx, setting.key, setting.value); err != nil {
						return fmt.Errorf("保存导入系统设置失败: %w", err)
					}
					if result.RowsAffected > 0 {
						accumulator.updated(configImportEntitySystemSettings)
					} else {
						accumulator.created(configImportEntitySystemSettings, false)
					}
				}
			}

			if envelope.Version == configExportVersion2 {
				if err := h.importBackupAssetGraph(tx, envelope, configImportActorID(c)); err != nil {
					return err
				}
			}

			return rollbackSnapshot.seal(persistCtx, tx)
		})
		if err == nil {
			rollbackJournal.install(rollbackSnapshot)
		}
		return err
	}
	importErr := h.persistConfigImport(c.Request.Context(), foundationSettings, persistImport, rollbackJournal.Restore)
	if importErr != nil {
		if errors.Is(importErr, errConfigImportTargetConflict) {
			respondBadRequest(c, "导入任务的备份目标存在重叠或归属不明")
			return
		}
		if errors.Is(importErr, errConfigAssetGraphConflict) {
			respondConflict(c, "导入的备份资产图与本地身份冲突")
			return
		}
		if respondBackupAssetEnablementConflict(c, importErr) {
			return
		}
		if errors.Is(importErr, errConfigAssetGraphInvalid) {
			respondBadRequest(c, "导入数据无效")
			return
		}
		respondInternalError(c, importErr)
		return
	}

	result := accumulator.finalize()
	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:           "config.import",
		Purpose:          "config_import",
		CredentialKind:   "system_import",
		CredentialSource: "settings.import",
		Outcome:          credentialaudit.OutcomeSuccess,
		Metadata: map[string]any{
			"stage":          "success",
			"node_count":     result.Nodes,
			"key_count":      result.SSHKeys,
			"policy_count":   result.Policies,
			"task_count":     result.Tasks,
			"settings_count": result.SystemSettings,
			"created_count":  result.Created,
			"updated_count":  result.Updated,
			"rejected_count": result.Rejected,
			"skipped_count":  result.Skipped,
		},
	})

	respondOK(c, result)
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
	if candidate.allowedNodeIDs != nil && strings.TrimSpace(*candidate.allowedNodeIDs) != "" {
		key.AllowedNodeIDs = ""
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

func addImportedNodeCredentialWarnings(
	accumulator *configImportAccumulator,
	index int,
	name string,
	source map[string]interface{},
	nodeItem model.Node,
) {
	if importedNodeSourceSSHKeyIDPresent(source) {
		accumulator.warning(configImportEntityNodes, index, name, configImportWarningUnresolvedSSHKey)
	}
	switch strings.ToLower(strings.TrimSpace(nodeItem.AuthType)) {
	case "password":
		if strings.TrimSpace(nodeItem.Password) == "" {
			accumulator.warning(configImportEntityNodes, index, name, configImportWarningMissingPassword)
		}
	case "key", "ssh_key":
		if nodeItem.SSHKeyID == nil && strings.TrimSpace(nodeItem.PrivateKey) == "" {
			accumulator.warning(configImportEntityNodes, index, name, configImportWarningMissingInlineKey)
		}
	}
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

func resolveImportNodeID(tx *gorm.DB, taskData map[string]interface{}) (uint, bool, error) {
	name := readStringField(taskData, "node_name")
	if name == "" {
		// Numeric node IDs belong to the source database and are not stable
		// identity evidence for an import. The export format carries the
		// target node name for this mapping.
		return 0, false, nil
	}
	var node model.Node
	result := tx.Select("id").Where("name = ?", name).First(&node)
	if result.Error == nil {
		return node.ID, true, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return 0, false, result.Error
	}
	return 0, false, nil
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

func resolveImportPolicyID(tx *gorm.DB, taskData map[string]interface{}) (uint, bool, error) {
	if name := readStringField(taskData, "policy_name"); name != "" {
		var policy model.Policy
		result := tx.Select("id").Where("name = ?", name).First(&policy)
		if result.Error == nil {
			return policy.ID, true, nil
		}
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return 0, false, nil
		}
		return 0, false, result.Error
	}

	if rawID, ok := taskData["policy_id"]; ok {
		if policyID, ok := normalizeUintValue(rawID); ok {
			var policy model.Policy
			result := tx.Select("id").Where("id = ?", policyID).First(&policy)
			if result.Error == nil {
				return policy.ID, true, nil
			}
			if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
				return 0, false, result.Error
			}
		}
	}

	return 0, false, nil
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

func resolveImportedDependencyKey(tx *gorm.DB, taskData map[string]interface{}, ownNodeIDs ...uint) (importTaskKey, bool, error) {
	dependencyName := readStringField(taskData, "depends_on_task_name")
	if dependencyName == "" {
		return importTaskKey{}, false, nil
	}

	nodeName := readStringField(taskData, "depends_on_task_node_name")
	if nodeName != "" {
		var node model.Node
		result := tx.Select("id").Where("name = ?", nodeName).First(&node)
		if result.Error == nil {
			return buildImportTaskKey(dependencyName, node.ID), true, nil
		}
		if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return importTaskKey{}, false, result.Error
		}
		// A named dependency node that cannot be resolved must not fall back
		// to a source numeric ID or to the dependent task's node.
		return importTaskKey{}, false, nil
	}

	// Older same-node exports may omit the dependency node name. In that
	// established shape, the candidate task's already-resolved local node is
	// the only safe mapping. Numeric source IDs are never interpreted here.
	if len(ownNodeIDs) > 0 && ownNodeIDs[0] != 0 {
		return buildImportTaskKey(dependencyName, ownNodeIDs[0]), true, nil
	}
	return importTaskKey{}, false, nil
}
