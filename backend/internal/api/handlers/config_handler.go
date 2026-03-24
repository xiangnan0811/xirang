package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/config"
	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	policyPkg "xirang/backend/internal/policy"
	"xirang/backend/internal/settings"

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

// Export 导出节点、密钥、策略、任务配置为 JSON。
// 默认不导出敏感字段（私钥、密码），需 include_secrets=true 且 admin 权限。
func (h *ConfigHandler) Export(c *gin.Context) {
	includeSecrets := c.Query("include_secrets") == "true"

	if includeSecrets {
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "仅管理员可导出敏感数据"})
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
	h.db.Find(&nodes)
	var sshKeys []model.SSHKey
	h.db.Find(&sshKeys)
	var policies []model.Policy
	h.db.Preload("Nodes").Find(&policies)
	var tasks []model.Task
	h.db.Preload("Node").Preload("Policy").Find(&tasks)
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
			"name":        k.Name,
			"username":    k.Username,
			"key_type":    k.KeyType,
			"fingerprint": k.Fingerprint,
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
	h.db.Find(&dbSettings)
	exportSettings := make([]gin.H, 0, len(dbSettings))
	for _, s := range dbSettings {
		exportSettings = append(exportSettings, gin.H{
			"key":   s.Key,
			"value": s.Value,
		})
	}

	c.JSON(http.StatusOK, gin.H{
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

// Import 从 JSON 导入配置。
// conflict 参数控制冲突策略：skip（默认）跳过已存在项，overwrite 覆盖。
func (h *ConfigHandler) Import(c *gin.Context) {
	conflict := c.DefaultQuery("conflict", "skip")
	if conflict != "skip" && conflict != "overwrite" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conflict 参数仅支持 skip 或 overwrite"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20) // 10MB

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "导入文件超过 10MB 限制"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "读取导入数据失败"})
		return
	}

	data, err := decodeConfigImportData(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的导入数据"})
		return
	}

	var importedNodes, importedKeys, importedPolicies, importedTasks, importedSettings int

	importErr := h.db.Transaction(func(tx *gorm.DB) error {
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
				if err := validateNodeHostPort(existing.Host, existing.Port); err != nil {
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
					continue
				}
				if newNode.AuthType != "" && newNode.AuthType != "password" && newNode.AuthType != "key" && newNode.AuthType != "ssh_key" {
					continue
				}
				if err := validateNodeHostPort(newNode.Host, newNode.Port); err != nil {
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

			req := taskRequest{
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
			hydrateTaskDefaultsFromPolicy(tx, &req)
			trimTaskRequest(&req)
			inferTaskExecutor(&req, "")
			ensureNodeTargetPrefix(tx, &req)
			if hasDependency && strings.TrimSpace(explicitCronSpec) == "" {
				req.CronSpec = ""
			}
			if (req.ExecutorType == "rsync" || req.ExecutorType == "restic") && req.RsyncTarget == "" {
				var node model.Node
				if err := tx.First(&node, req.NodeID).Error; err == nil && node.BackupDir != "" {
					req.RsyncTarget = policyPkg.NodeTargetPath(config.BackupRoot, node.BackupDir)
				}
			}
			if err := validateTaskRequest(req); err != nil {
				continue
			}
			if err := validateTaskRefsWithDB(tx, req, 0); err != nil {
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
			req := taskRequest{
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
			if err := validateTaskRefsWithDB(tx, req, current.ID); err != nil {
				continue
			}
			if err := tx.Model(&current).Update("depends_on_task_id", dependencyID).Error; err != nil {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": "导入失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"nodes":           importedNodes,
			"ssh_keys":        importedKeys,
			"policies":        importedPolicies,
			"tasks":           importedTasks,
			"system_settings": importedSettings,
			"imported":        importedNodes + importedKeys + importedPolicies + importedTasks + importedSettings,
			"skipped":         0,
		},
	})
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

func readStringField(values map[string]interface{}, key string) string {
	raw, _ := values[key].(string)
	return strings.TrimSpace(raw)
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
