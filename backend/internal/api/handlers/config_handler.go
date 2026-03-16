package handlers

import (
	"net/http"
	"time"

	"xirang/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ConfigHandler 处理配置导出/导入
type ConfigHandler struct {
	db *gorm.DB
}

func NewConfigHandler(db *gorm.DB) *ConfigHandler {
	return &ConfigHandler{db: db}
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
			"bwlimit":        p.BwLimit,
			"retention_days": p.RetentionDays,
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

	c.JSON(http.StatusOK, gin.H{
		"version":     "1.0",
		"exported_at": time.Now().Format(time.RFC3339),
		"data": gin.H{
			"nodes":    exportNodes,
			"ssh_keys": exportKeys,
			"policies": exportPolicies,
			"tasks":    exportTasks,
		},
	})
}

// Import 从 JSON 导入配置。
// conflict 参数控制冲突策略：skip（默认）跳过已存在项，overwrite 覆盖。
func (h *ConfigHandler) Import(c *gin.Context) {
	conflict := c.DefaultQuery("conflict", "skip")

	var payload struct {
		Data struct {
			Nodes    []map[string]interface{} `json:"nodes"`
			SSHKeys  []map[string]interface{} `json:"ssh_keys"`
			Policies []map[string]interface{} `json:"policies"`
		} `json:"data"`
	}

	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的导入数据"})
		return
	}

	importedNodes := 0
	importedKeys := 0
	importedPolicies := 0

	// 导入 SSH 密钥
	for _, keyData := range payload.Data.SSHKeys {
		name, _ := keyData["name"].(string)
		if name == "" {
			continue
		}
		var existing model.SSHKey
		err := h.db.Where("name = ?", name).First(&existing).Error
		if err == nil {
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
			h.db.Save(&existing)
			importedKeys++
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
			if err := h.db.Create(&newKey).Error; err == nil {
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
		err := h.db.Where("name = ?", name).First(&existing).Error
		if err == nil {
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
			h.db.Save(&existing)
			importedNodes++
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
			if err := h.db.Create(&newNode).Error; err == nil {
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
		err := h.db.Where("name = ?", name).First(&existing).Error
		if err == nil {
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
				existing.CronSpec = cron
			}
			if excl, ok := policyData["exclude_rules"].(string); ok {
				existing.ExcludeRules = excl
			}
			if ret, ok := policyData["retention_days"].(float64); ok {
				existing.RetentionDays = int(ret)
			}
			h.db.Save(&existing)
			importedPolicies++
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
			if err := h.db.Create(&newPolicy).Error; err == nil {
				importedPolicies++
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"nodes":    importedNodes,
			"ssh_keys": importedKeys,
			"policies": importedPolicies,
		},
	})
}
