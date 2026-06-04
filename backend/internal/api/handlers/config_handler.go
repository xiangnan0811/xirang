package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/credentialaudit"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/node"
	gormrepo "xirang/backend/internal/repository/gorm"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
	taskPkg "xirang/backend/internal/task"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ConfigHandler 处理配置导出/导入
type ConfigHandler struct {
	db          *gorm.DB
	settingsSvc *settings.Service
}

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

func NewConfigHandler(db *gorm.DB, settingsSvc *settings.Service) *ConfigHandler {
	return &ConfigHandler{db: db, settingsSvc: settingsSvc}
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
		exportPolicies = append(exportPolicies, gin.H{
			"name":               p.Name,
			"description":        p.Description,
			"source_path":        p.SourcePath,
			"target_path":        p.TargetPath,
			"cron_spec":          p.CronSpec,
			"exclude_rules":      p.ExcludeRules,
			"bwlimit":            p.BwLimit,
			"bandwidth_schedule": p.BandwidthSchedule,
			"retention_days":     p.RetentionDays,
			"max_concurrent":     p.MaxConcurrent,
			"enabled":            p.Enabled,
			"is_template":        p.IsTemplate,
			"node_names":         nodeNames,
		})
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
		if !includeSecrets && configExportSettingLooksSensitive(s) {
			continue
		}
		exportSettings = append(exportSettings, gin.H{
			"key":   s.Key,
			"value": s.Value,
		})
	}

	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:           "config.export",
		Purpose:          "config_export",
		CredentialKind:   "config_export",
		CredentialSource: "config.export",
		Outcome:          credentialaudit.OutcomeSuccess,
		Metadata: map[string]any{
			"stage":          "success",
			"with_sensitive": includeSecrets,
			"node_count":     len(exportNodes),
			"key_count":      len(exportKeys),
			"policy_count":   len(exportPolicies),
			"task_count":     len(exportTasks),
			"setting_count":  len(exportSettings),
		},
	})

	respondOK(c, gin.H{
		"version":     "1.0",
		"exported_at": time.Now().Format(time.RFC3339),
		"data": gin.H{
			"nodes":           exportNodes,
			"ssh_keys":        exportKeys,
			"policies":        exportPolicies,
			"tasks":           exportTasks,
			"system_settings": exportSettings,
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
// @Success      200  {object}  handlers.Response
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

	data, err := decodeConfigImportData(body)
	if err != nil {
		respondBadRequest(c, "无效的导入数据")
		return
	}

	// 导入前校验节点数据：host 合法性、base_path 绝对路径
	var importErrList []importValidationError
	for i, nodeData := range data.Nodes {
		if errs := validateNodeImportData(nodeData, i); len(errs) > 0 {
			importErrList = append(importErrList, errs...)
		}
	}
	// 校验策略路径
	for i, policyData := range data.Policies {
		name, _ := policyData["name"].(string)
		if src, ok := policyData["source_path"].(string); ok && src != "" {
			if err := validateImportPath(src); err != nil {
				importErrList = append(importErrList, importValidationError{
					Resource: "policies",
					Index:    i,
					Name:     name,
					Field:    "source_path",
					Message:  err.Error(),
				})
			}
		}
		if tgt, ok := policyData["target_path"].(string); ok && tgt != "" {
			if err := validateImportPath(tgt); err != nil {
				importErrList = append(importErrList, importValidationError{
					Resource: "policies",
					Index:    i,
					Name:     name,
					Field:    "target_path",
					Message:  err.Error(),
				})
			}
		}
	}
	// 校验任务路径
	for i, taskData := range data.Tasks {
		name, _ := taskData["name"].(string)
		if src, ok := taskData["rsync_source"].(string); ok && src != "" {
			if err := validateImportPath(src); err != nil {
				importErrList = append(importErrList, importValidationError{
					Resource: "tasks",
					Index:    i,
					Name:     name,
					Field:    "rsync_source",
					Message:  err.Error(),
				})
			}
		}
		if tgt, ok := taskData["rsync_target"].(string); ok && tgt != "" {
			if err := validateImportPath(tgt); err != nil {
				importErrList = append(importErrList, importValidationError{
					Resource: "tasks",
					Index:    i,
					Name:     name,
					Field:    "rsync_target",
					Message:  err.Error(),
				})
			}
		}
	}

	if len(importErrList) > 0 {
		for _, ve := range importErrList {
			logger.Module("config").Warn().
				Str("resource", ve.Resource).
				Int("index", ve.Index).
				Str("name", ve.Name).
				Str("field", ve.Field).
				Str("message", ve.Message).
				Msg("配置导入校验失败")
		}
		// 构建人类可读的错误消息
		errDetail := make([]string, 0, len(importErrList))
		for _, ve := range importErrList {
			label := fmt.Sprintf("#%d", ve.Index+1)
			if ve.Name != "" {
				label = ve.Name
			}
			errDetail = append(errDetail, fmt.Sprintf("%s.%s: %s", label, ve.Field, ve.Message))
		}
		c.JSON(http.StatusBadRequest, Response{
			Code:    http.StatusBadRequest,
			Message: fmt.Sprintf("导入数据校验失败（共 %d 项），请修复后重试", len(importErrList)),
			Data:    gin.H{"detail": errDetail, "items": importErrList},
		})
		return
	}

	var importedNodes, importedKeys, importedPolicies, importedTasks, importedSettings int

	importErr := h.db.Transaction(func(tx *gorm.DB) error {
	// Create repos from tx for task helper functions.
	importNodeRepo := gormrepo.NewNodeRepository(tx)
	importPolicyRepo := gormrepo.NewPolicyRepository(tx)
	importTaskRepo := gormrepo.NewTaskRepository(tx)

		resolvedTaskIDs := make(map[importTaskKey]uint)
		type taskDependencyUpdate struct {
			taskID        uint
			dependencyKey importTaskKey
			hasDependency bool
		}
		var taskDependencyUpdates []taskDependencyUpdate

		// 导入 SSH 密钥
		for _, keyData := range data.SSHKeys {
			name, _ := keyData["name"].(string)
			if name == "" {
				continue
			}
			var existing model.SSHKey
			found := tx.Where("name = ?", name).Limit(1).Find(&existing).RowsAffected > 0
			if found {
				if conflict != "overwrite" {
					continue
				}
				// overwrite: 更新已有记录
				if username, ok := keyData["username"].(string); ok {
					existing.Username = username
				}
				if keyType, ok := keyData["key_type"].(string); ok {
					existing.KeyType = keyType
				}
				if privateKey, ok := keyData["private_key"].(string); ok && privateKey != "" {
					existing.PrivateKey = privateKey
				}
				applyImportedSSHKeyScope(&existing, keyData)
				if err := tx.Save(&existing).Error; err == nil {
					importedKeys++
				}
			} else {
				newKey := model.SSHKey{Name: name}
				if username, ok := keyData["username"].(string); ok {
					newKey.Username = username
				}
				if keyType, ok := keyData["key_type"].(string); ok {
					newKey.KeyType = keyType
				}
				if privateKey, ok := keyData["private_key"].(string); ok {
					newKey.PrivateKey = privateKey
				}
				if fingerprint, ok := keyData["fingerprint"].(string); ok {
					newKey.Fingerprint = fingerprint
				}
				applyImportedSSHKeyScope(&newKey, keyData)
				if err := tx.Create(&newKey).Error; err == nil {
					importedKeys++
				}
			}
		}

		// 导入节点
		for _, nodeData := range data.Nodes {
			name, _ := nodeData["name"].(string)
			if name == "" {
				continue
			}
			var existing model.Node
			found := tx.Where("name = ?", name).Limit(1).Find(&existing).RowsAffected > 0
			if found {
				if conflict != "overwrite" {
					continue
				}
				if host, ok := nodeData["host"].(string); ok {
					existing.Host = host
				}
				if port, ok := nodeData["port"].(float64); ok {
					existing.Port = int(port)
				}
				if username, ok := nodeData["username"].(string); ok {
					existing.Username = username
				}
				if authType, ok := nodeData["auth_type"].(string); ok {
					existing.AuthType = authType
				}
				if tags, ok := nodeData["tags"].(string); ok {
					existing.Tags = tags
				}
				if basePath, ok := nodeData["base_path"].(string); ok {
					existing.BasePath = basePath
				}
				if err := node.ValidateNodeHostPort(existing.Host, existing.Port); err != nil {
					logger.Module("config").Warn().
						Str("node", name).
						Str("host", existing.Host).
						Err(err).
						Msg("导入节点覆盖时 host 校验失败，跳过")
					continue
				}
				if err := tx.Save(&existing).Error; err == nil {
					importedNodes++
				}
			} else {
				newNode := model.Node{
					Name:   name,
					Status: "offline",
					Port:   22,
				}
				if host, ok := nodeData["host"].(string); ok {
					newNode.Host = host
				}
				if port, ok := nodeData["port"].(float64); ok {
					newNode.Port = int(port)
				}
				if username, ok := nodeData["username"].(string); ok {
					newNode.Username = username
				}
				if authType, ok := nodeData["auth_type"].(string); ok {
					newNode.AuthType = authType
				}
				if tags, ok := nodeData["tags"].(string); ok {
					newNode.Tags = tags
				}
				if basePath, ok := nodeData["base_path"].(string); ok {
					newNode.BasePath = basePath
				}
				if password, ok := nodeData["password"].(string); ok {
					newNode.Password = password
				}
				if privateKey, ok := nodeData["private_key"].(string); ok {
					newNode.PrivateKey = privateKey
				}
				if newNode.Username == "" {
					logger.Module("config").Warn().
						Str("node", name).
						Msg("导入节点缺少用户名，跳过")
					continue
				}
				if newNode.AuthType != "" && newNode.AuthType != "password" && newNode.AuthType != "key" && newNode.AuthType != "ssh_key" {
					logger.Module("config").Warn().
						Str("node", name).
						Str("auth_type", newNode.AuthType).
						Msg("导入节点认证类型无效，跳过")
					continue
				}
				if err := node.ValidateNodeHostPort(newNode.Host, newNode.Port); err != nil {
					logger.Module("config").Warn().
						Str("node", name).
						Str("host", newNode.Host).
						Err(err).
						Msg("导入新节点时 host 校验失败，跳过")
					continue
				}
				if err := tx.Create(&newNode).Error; err == nil {
					importedNodes++
				}
			}
		}

		// 导入策略
		for _, policyData := range data.Policies {
			name, _ := policyData["name"].(string)
			if name == "" {
				continue
			}
			var existing model.Policy
			found := tx.Where("name = ?", name).Limit(1).Find(&existing).RowsAffected > 0
			if found {
				if conflict != "overwrite" {
					continue
				}
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
						logger.Module("config").Warn().
							Str("policy", name).
							Str("cron_spec", cron).
							Err(err).
							Msg("导入策略覆盖时 cron spec 校验失败，跳过")
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
				if err := tx.Save(&existing).Error; err == nil {
					importedPolicies++
				}
			} else {
				newPolicy := model.Policy{
					Name:          name,
					MaxConcurrent: 1,
					RetentionDays: 7,
					Enabled:       false,
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
						logger.Module("config").Warn().
							Str("policy", name).
							Str("cron_spec", cron).
							Err(err).
							Msg("导入新策略时 cron spec 校验失败，跳过")
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
				if isTmpl, ok := policyData["is_template"].(bool); ok {
					newPolicy.IsTemplate = isTmpl
				}
				if err := tx.Create(&newPolicy).Error; err == nil {
					importedPolicies++
				}
			}
		}

		// 导入任务
		for _, taskData := range data.Tasks {
			name, _ := taskData["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}

			nodeID, ok := resolveImportNodeID(tx, taskData)
			if !ok {
				continue
			}

			var policyID *uint
			if id, ok := resolveImportPolicyID(tx, taskData); ok {
				policyID = &id
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
			dependencyKey, hasDependency := resolveImportedDependencyKey(tx, taskData)
			explicitCronSpec := req.CronSpec
			taskPkg.HydrateTaskDefaultsFromPolicy(c.Request.Context(), importPolicyRepo, importNodeRepo, &req)
			taskPkg.TrimTaskInput(&req)
			taskPkg.InferTaskExecutor(&req, "")
			taskPkg.EnsureNodeTargetPrefix(c.Request.Context(), importNodeRepo, &req)
			if hasDependency && strings.TrimSpace(explicitCronSpec) == "" {
				req.CronSpec = ""
			}
			taskPkg.AutoGenerateTarget(c.Request.Context(), importNodeRepo, &req)
			if err := taskPkg.ValidateTaskInput(req); err != nil {
				logger.Module("config").Warn().
					Str("task", req.Name).
					Err(err).
					Msg("导入任务校验失败，跳过")
				continue
			}
			if err := taskPkg.ValidateTaskRefs(c.Request.Context(), importNodeRepo, importPolicyRepo, importTaskRepo, req, 0); err != nil {
				logger.Module("config").Warn().
					Str("task", req.Name).
					Err(err).
					Msg("导入任务引用校验失败，跳过")
				continue
			}
			taskKey := buildImportTaskKey(req.Name, req.NodeID)

			var existing model.Task
			found := tx.Where("name = ? AND node_id = ?", req.Name, req.NodeID).Limit(1).Find(&existing).RowsAffected > 0
			if found {
				resolvedTaskIDs[taskKey] = existing.ID
				if conflict != "overwrite" {
					continue
				}
				existing.PolicyID = req.PolicyID
				existing.DependsOnTaskID = nil
				existing.Command = req.Command
				existing.RsyncSource = req.RsyncSource
				existing.RsyncTarget = req.RsyncTarget
				existing.ExecutorType = req.ExecutorType
				existing.ExecutorConfig = req.ExecutorConfig
				existing.CronSpec = req.CronSpec
				existing.Source = readStringField(taskData, "source")
				// overwrite 仅在导入数据显式携带 enabled 字段时才覆盖，避免意外改写已有任务启停状态。
				if enabled, ok := taskData["enabled"].(bool); ok {
					existing.Enabled = enabled
				}
				if err := tx.Save(&existing).Error; err == nil {
					importedTasks++
					taskDependencyUpdates = append(taskDependencyUpdates, taskDependencyUpdate{taskID: existing.ID, dependencyKey: dependencyKey, hasDependency: hasDependency})
				}
				continue
			}

			newTask := model.Task{
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
				Enabled:        true,
			}
			if newTask.Source == "" {
				newTask.Source = "manual"
			}
			if enabled, ok := taskData["enabled"].(bool); ok {
				newTask.Enabled = enabled
			}
			if err := tx.Create(&newTask).Error; err == nil {
				importedTasks++
				resolvedTaskIDs[taskKey] = newTask.ID
				taskDependencyUpdates = append(taskDependencyUpdates, taskDependencyUpdate{taskID: newTask.ID, dependencyKey: dependencyKey, hasDependency: hasDependency})
			}
		}

		for _, update := range taskDependencyUpdates {
			var dependencyID *uint
			if update.hasDependency {
				resolvedID, ok := resolvedTaskIDs[update.dependencyKey]
				if !ok || resolvedID == 0 || resolvedID == update.taskID {
					continue
				}
				dependencyID = &resolvedID
			}

			var current model.Task
			if err := tx.First(&current, update.taskID).Error; err != nil {
				continue
			}
			req := taskPkg.CreateTaskInput{
				Name:            current.Name,
				NodeID:          current.NodeID,
				PolicyID:        current.PolicyID,
				DependsOnTaskID: dependencyID,
				Command:         current.Command,
				RsyncSource:     current.RsyncSource,
				RsyncTarget:     current.RsyncTarget,
				ExecutorType:    current.ExecutorType,
				ExecutorConfig:  current.ExecutorConfig,
				CronSpec:        current.CronSpec,
			}
			if err := taskPkg.ValidateTaskRefs(c.Request.Context(), importNodeRepo, importPolicyRepo, importTaskRepo, req, current.ID); err != nil {
				logger.Module("config").Warn().
					Str("task", current.Name).
					Err(err).
					Msg("导入任务依赖更新校验失败，跳过")
				continue
			}
			if err := tx.Model(&current).Update("depends_on_task_id", dependencyID).Error; err != nil {
				logger.Module("config").Warn().
					Str("task", current.Name).
					Err(err).
					Msg("导入任务依赖关系更新失败，跳过")
				continue
			}
		}

		// 导入系统设置（使用事务 handle 确保原子性）
		if h.settingsSvc != nil {
			for _, sd := range data.SystemSettings {
				key, _ := sd["key"].(string)
				value, _ := sd["value"].(string)
				if key == "" {
					continue
				}
				if err := h.settingsSvc.UpdateWithTx(tx, key, value); err == nil {
					importedSettings++
				}
			}
		}

		return nil
	})
	if importErr != nil {
		respondInternalError(c, importErr)
		return
	}

	writeCredentialAuditFromGin(c, h.db, credentialaudit.Event{
		Action:           "config.import",
		Purpose:          "config_import",
		CredentialKind:   "system_import",
		CredentialSource: "settings.import",
		Outcome:          credentialaudit.OutcomeSuccess,
		Metadata: map[string]any{
			"stage":          "success",
			"node_count":     importedNodes,
			"key_count":      importedKeys,
			"policy_count":   importedPolicies,
			"task_count":     importedTasks,
			"settings_count": importedSettings,
		},
	})

	respondOK(c, gin.H{
		"nodes":           importedNodes,
		"ssh_keys":        importedKeys,
		"policies":        importedPolicies,
		"tasks":           importedTasks,
		"system_settings": importedSettings,
		"imported":        importedNodes + importedKeys + importedPolicies + importedTasks + importedSettings,
		"skipped":         0,
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

func decodeConfigImportData(body []byte) (configImportData, error) {
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(body, &topLevel); err == nil {
		if rawData, ok := topLevel["data"]; ok {
			var wrapped configImportData
			if err := json.Unmarshal(rawData, &wrapped); err != nil {
				return configImportData{}, err
			}
			return wrapped, nil
		}
	}

	var direct configImportData
	if err := json.Unmarshal(body, &direct); err != nil {
		return configImportData{}, err
	}
	return direct, nil
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

func applyImportedSSHKeyScope(key *model.SSHKey, data map[string]interface{}) {
	if key == nil {
		return
	}
	if disabled, ok := data["disabled"].(bool); ok {
		key.Disabled = disabled
	}
	if raw, ok := data["expires_at"]; ok {
		key.ExpiresAt = parseImportedTimePtr(raw)
	}
	if raw, ok := data["allowed_purposes"]; ok {
		if normalized, err := sshutil.NormalizePurposeList(importStringValue(raw)); err == nil {
			key.AllowedPurposes = normalized
		}
	}
	if raw, ok := data["allowed_node_ids"]; ok {
		if normalized, err := sshutil.NormalizeNodeIDList(importStringValue(raw)); err == nil {
			key.AllowedNodeIDs = normalized
		}
	}
	if raw, ok := data["allowed_node_tags"]; ok {
		key.AllowedNodeTags = sshutil.NormalizeTagList(importStringValue(raw))
	}
}

func importStringValue(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		if v == float64(uint64(v)) {
			return strconv.FormatUint(uint64(v), 10)
		}
		return strings.TrimSpace(fmt.Sprint(v))
	default:
		return ""
	}
}

func parseImportedTimePtr(raw interface{}) *time.Time {
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func resolveImportNodeID(tx *gorm.DB, taskData map[string]interface{}) (uint, bool) {
	if name := readStringField(taskData, "node_name"); name != "" {
		var node model.Node
		if err := tx.Select("id").Where("name = ?", name).First(&node).Error; err == nil {
			return node.ID, true
		}
	}

	if rawID, ok := taskData["node_id"]; ok {
		if nodeID, ok := normalizeUintValue(rawID); ok {
			var node model.Node
			if err := tx.Select("id").Where("id = ?", nodeID).First(&node).Error; err == nil {
				return node.ID, true
			}
		}
	}

	return 0, false
}

func resolveImportPolicyID(tx *gorm.DB, taskData map[string]interface{}) (uint, bool) {
	if name := readStringField(taskData, "policy_name"); name != "" {
		var policy model.Policy
		if err := tx.Select("id").Where("name = ?", name).First(&policy).Error; err == nil {
			return policy.ID, true
		}
		return 0, false
	}

	if rawID, ok := taskData["policy_id"]; ok {
		if policyID, ok := normalizeUintValue(rawID); ok {
			var policy model.Policy
			if err := tx.Select("id").Where("id = ?", policyID).First(&policy).Error; err == nil {
				return policy.ID, true
			}
		}
	}

	return 0, false
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

func resolveImportedDependencyKey(tx *gorm.DB, taskData map[string]interface{}) (importTaskKey, bool) {
	dependencyName := readStringField(taskData, "depends_on_task_name")
	if dependencyName == "" {
		return importTaskKey{}, false
	}

	nodeName := readStringField(taskData, "depends_on_task_node_name")
	if nodeName != "" {
		var node model.Node
		if err := tx.Select("id").Where("name = ?", nodeName).First(&node).Error; err == nil {
			return buildImportTaskKey(dependencyName, node.ID), true
		}
	}

	if rawNodeID, ok := taskData["depends_on_task_node_id"]; ok {
		if nodeID, ok := normalizeUintValue(rawNodeID); ok {
			return buildImportTaskKey(dependencyName, nodeID), true
		}
	}

	return importTaskKey{}, false
}
