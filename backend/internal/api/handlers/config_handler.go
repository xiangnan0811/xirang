package handlers

import (
	"net/http"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ConfigHandler 处理配置导出/导入
type ConfigHandler struct {
	db          *gorm.DB
	settingsSvc *settings.Service
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
	h.db.Find(&tasks)

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
			"name":           p.Name,
			"description":    p.Description,
			"source_path":    p.SourcePath,
			"target_path":    p.TargetPath,
			"cron_spec":      p.CronSpec,
			"exclude_rules":  p.ExcludeRules,
			"bwlimit":             p.BwLimit,
			"bandwidth_schedule":  p.BandwidthSchedule,
			"retention_days":      p.RetentionDays,
			"max_concurrent": p.MaxConcurrent,
			"enabled":        p.Enabled,
			"is_template":    p.IsTemplate,
			"node_names":     nodeNames,
		})
	}

	// 构建任务导出数据
	exportTasks := make([]gin.H, 0, len(tasks))
	for _, t := range tasks {
		exportTasks = append(exportTasks, gin.H{
			"name":          t.Name,
			"node_id":       t.NodeID,
			"executor_type": t.ExecutorType,
			"rsync_source":  t.RsyncSource,
			"rsync_target":  t.RsyncTarget,
			"cron_spec":     t.CronSpec,
			"source":        t.Source,
		})
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

	var payload struct {
		Data struct {
			Nodes          []map[string]interface{} `json:"nodes"`
			SSHKeys        []map[string]interface{} `json:"ssh_keys"`
			Policies       []map[string]interface{} `json:"policies"`
			SystemSettings []map[string]interface{} `json:"system_settings"`
		} `json:"data"`
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20) // 10MB

	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的导入数据"})
		return
	}

	var importedNodes, importedKeys, importedPolicies, importedSettings int

	importErr := h.db.Transaction(func(tx *gorm.DB) error {

	// 导入 SSH 密钥
	for _, keyData := range payload.Data.SSHKeys {
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
	for _, nodeData := range payload.Data.Nodes {
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
	for _, policyData := range payload.Data.Policies {
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

	// 导入系统设置（使用事务 handle 确保原子性）
	if h.settingsSvc != nil {
		for _, sd := range payload.Data.SystemSettings {
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
			"system_settings": importedSettings,
		},
	})
}
