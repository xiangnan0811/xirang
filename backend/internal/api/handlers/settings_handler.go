package handlers

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/logger"
	"xirang/backend/internal/middleware"
	"xirang/backend/internal/model"
	"xirang/backend/internal/settings"
	"xirang/backend/internal/sshutil"
	"xirang/backend/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SettingsHandler 系统设置接口
type SettingsHandler struct {
	db  *gorm.DB
	svc *settings.Service
}

type securityRiskSummaryResponse struct {
	GeneratedAt time.Time               `json:"generated_at"`
	Summary     securityRiskSummaryStat `json:"summary"`
	Items       []securityRiskItem      `json:"items"`
}

type securityRiskSummaryStat struct {
	TotalRisks int64 `json:"total_risks"`
	Categories int   `json:"categories"`
}

type securityRiskItem struct {
	Code        string   `json:"code"`
	Severity    string   `json:"severity"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Count       int64    `json:"count"`
	Examples    []string `json:"examples"`
}

const (
	maxSecurityRiskExamples = 3
	staleSSHKeyAge          = 90 * 24 * time.Hour
	credentialAuditRiskAge  = 7 * 24 * time.Hour
)

// NewSettingsHandler 创建设置处理器
func NewSettingsHandler(db *gorm.DB, svc *settings.Service) *SettingsHandler {
	return &SettingsHandler{db: db, svc: svc}
}

// GetAll godoc
// @Summary      获取所有系统设置
// @Description  返回设置定义列表和当前有效值
// @Tags         settings
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /settings [get]
func (h *SettingsHandler) GetAll(c *gin.Context) {
	result, err := h.svc.GetAll()
	if err != nil {
		respondInternalError(c, err)
		return
	}
	respondOK(c, gin.H{
		"definitions": h.svc.Registry(),
		"values":      result,
	})
}

// SecurityRiskSummary godoc
// @Summary      获取安全风险摘要
// @Description  返回只读的轻量安全风险提示，不包含任何原始凭据
// @Tags         settings
// @Security     Bearer
// @Produce      json
// @Success      200  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /settings/security-risk-summary [get]
func (h *SettingsHandler) SecurityRiskSummary(c *gin.Context) {
	items, err := h.securityRiskItems()
	if err != nil {
		respondInternalError(c, err)
		return
	}

	var totalRisks int64
	for _, item := range items {
		totalRisks += item.Count
	}

	respondOK(c, securityRiskSummaryResponse{
		GeneratedAt: time.Now().UTC(),
		Summary: securityRiskSummaryStat{
			TotalRisks: totalRisks,
			Categories: len(items),
		},
		Items: items,
	})
}

// BatchUpdate godoc
// @Summary      批量更新系统设置
// @Description  批量更新系统设置（原子操作：先校验全部，再统一写入）
// @Tags         settings
// @Security     Bearer
// @Accept       json
// @Produce      json
// @Param        body  body      object  true  "键值对 map"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /settings [put]
func (h *SettingsHandler) BatchUpdate(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		respondBadRequest(c, "请求参数不合法")
		return
	}

	// 预检：校验全部 key/value，不写入
	for key, value := range req {
		if err := h.svc.Validate(key, value); err != nil {
			respondBadRequest(c, err.Error())
			return
		}
	}

	userID := c.GetUint(middleware.CtxUserID)

	// 记录旧值用于审计
	oldValues := make(map[string]string, len(req))
	for key := range req {
		oldValues[key] = h.svc.GetEffective(key)
	}

	// 原子写入：在事务中更新全部设置
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		for key, value := range req {
			if err := h.svc.UpdateWithTx(tx, key, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		respondInternalError(c, err)
		return
	}

	// 审计日志：仅记录设置键和变更事实，不写入可能包含密码、令牌、端点或代理地址的值。
	for key := range req {
		logger.Module("audit").Info().
			Str("action", "settings_update").
			Str("key", key).
			Bool("changed", oldValues[key] != req[key]).
			Str("source", "db").
			Uint("user_id", userID).
			Msg("系统设置变更")
	}

	respondMessage(c, "设置已更新")
}

// Delete godoc
// @Summary      重置系统设置
// @Description  删除指定 key 的 DB 覆盖值，恢复为环境变量或默认值
// @Tags         settings
// @Security     Bearer
// @Produce      json
// @Param        key  path      string  true  "设置 Key"
// @Success      200  {object}  handlers.Response
// @Failure      400  {object}  handlers.Response
// @Failure      401  {object}  handlers.Response
// @Router       /settings/{key} [delete]
func (h *SettingsHandler) Delete(c *gin.Context) {
	key := c.Param("key")
	oldVal := h.svc.GetEffective(key)
	if err := h.svc.Delete(key); err != nil {
		respondBadRequest(c, err.Error())
		return
	}
	newVal := h.svc.GetEffective(key)
	userID := c.GetUint(middleware.CtxUserID)
	logger.Module("audit").Info().
		Str("action", "settings_reset").
		Str("key", key).
		Bool("changed", oldVal != newVal).
		Uint("user_id", userID).
		Msg("系统设置重置为默认值")
	respondMessage(c, "设置已重置")
}

func (h *SettingsHandler) securityRiskItems() ([]securityRiskItem, error) {
	rootItem, err := h.rootSSHUserRiskItem()
	if err != nil {
		return nil, err
	}
	reusedItem, err := h.reusedSSHKeyRiskItem()
	if err != nil {
		return nil, err
	}
	sudoItem, err := h.sudoEnabledRiskItem()
	if err != nil {
		return nil, err
	}
	broadScopeItem, err := h.broadScopeSSHKeyRiskItem()
	if err != nil {
		return nil, err
	}
	disabledInUseItem, err := h.disabledSSHKeyInUseRiskItem()
	if err != nil {
		return nil, err
	}
	expiredInUseItem, err := h.expiredSSHKeyInUseRiskItem()
	if err != nil {
		return nil, err
	}
	staleItem, err := h.staleSSHKeyRiskItem()
	if err != nil {
		return nil, err
	}
	credentialOpsItem, err := h.recentCredentialOperationRiskItem()
	if err != nil {
		return nil, err
	}
	weakItem := h.weakSecurityDefaultsRiskItem()
	return []securityRiskItem{
		rootItem,
		reusedItem,
		sudoItem,
		broadScopeItem,
		disabledInUseItem,
		expiredInUseItem,
		staleItem,
		credentialOpsItem,
		weakItem,
	}, nil
}

func (h *SettingsHandler) rootSSHUserRiskItem() (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "root_ssh_users",
		Severity:    "warning",
		Title:       "Root SSH 用户",
		Description: "使用 root 账号登录的节点会扩大面板被攻陷后的横向移动影响面。",
	}
	if err := h.db.Model(&model.Node{}).Where("LOWER(username) = ?", "root").Count(&item.Count).Error; err != nil {
		return item, err
	}
	var nodes []model.Node
	if err := h.db.Select("id", "name").Where("LOWER(username) = ?", "root").Order("id asc").Limit(maxSecurityRiskExamples).Find(&nodes).Error; err != nil {
		return item, err
	}
	item.Examples = nodeNameExamples(nodes)
	return item, nil
}

func (h *SettingsHandler) sudoEnabledRiskItem() (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "sudo_enabled_nodes",
		Severity:    "warning",
		Title:       "启用 sudo 的节点",
		Description: "启用 sudo 的节点会让普通 SSH 用户执行高权限运维命令。",
	}
	if err := h.db.Model(&model.Node{}).Where("use_sudo = ?", true).Count(&item.Count).Error; err != nil {
		return item, err
	}
	var nodes []model.Node
	if err := h.db.Select("id", "name").Where("use_sudo = ?", true).Order("id asc").Limit(maxSecurityRiskExamples).Find(&nodes).Error; err != nil {
		return item, err
	}
	item.Examples = nodeNameExamples(nodes)
	return item, nil
}

func (h *SettingsHandler) reusedSSHKeyRiskItem() (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "reused_ssh_keys",
		Severity:    "warning",
		Title:       "复用的 SSH Key",
		Description: "同一 SSH Key 绑定多个节点会扩大单个密钥泄露后的影响范围。",
	}
	type reusedKeyRow struct {
		SSHKeyID  uint
		KeyName   string
		NodeCount int64
	}
	var rows []reusedKeyRow
	if err := h.db.Table("nodes").
		Select("nodes.ssh_key_id AS ssh_key_id, ssh_keys.name AS key_name, COUNT(nodes.id) AS node_count").
		Joins("JOIN ssh_keys ON ssh_keys.id = nodes.ssh_key_id").
		Where("nodes.ssh_key_id IS NOT NULL").
		Group("nodes.ssh_key_id, ssh_keys.name").
		Having("COUNT(nodes.id) > 1").
		Order("node_count desc, nodes.ssh_key_id asc").
		Find(&rows).Error; err != nil {
		return item, err
	}
	item.Count = int64(len(rows))
	limit := len(rows)
	if limit > maxSecurityRiskExamples {
		limit = maxSecurityRiskExamples
	}
	item.Examples = make([]string, 0, limit)
	for _, row := range rows[:limit] {
		name := strings.TrimSpace(row.KeyName)
		if name == "" {
			name = fmt.Sprintf("SSH Key #%d", row.SSHKeyID)
		}
		item.Examples = append(item.Examples, util.SanitizeMessage(fmt.Sprintf("%s（%d 个节点）", name, row.NodeCount)))
	}
	return item, nil
}

func (h *SettingsHandler) broadScopeSSHKeyRiskItem() (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "broad_scope_ssh_keys",
		Severity:    "warning",
		Title:       "范围过宽的 SSH Key",
		Description: "未限制用途或节点范围的 SSH Key 仍可兼容使用，但应逐步收敛到最小权限。",
	}
	var keys []model.SSHKey
	if err := h.db.Select("id", "name", "allowed_purposes", "allowed_node_ids", "allowed_node_tags").Order("id asc").Find(&keys).Error; err != nil {
		return item, err
	}
	for _, key := range keys {
		if !sshutil.IsBroadScope(key) {
			continue
		}
		item.Count++
		if len(item.Examples) < maxSecurityRiskExamples {
			item.Examples = append(item.Examples, sshKeyNameExample(key))
		}
	}
	return item, nil
}

func (h *SettingsHandler) disabledSSHKeyInUseRiskItem() (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "disabled_ssh_keys_in_use",
		Severity:    "critical",
		Title:       "已禁用但仍被引用的 SSH Key",
		Description: "节点仍引用已禁用的 SSH Key，后续连接会被阻断并可能影响任务执行。",
	}
	type keyUsageRow struct {
		SSHKeyID  uint
		KeyName   string
		NodeCount int64
	}
	var rows []keyUsageRow
	if err := h.db.Table("nodes").
		Select("nodes.ssh_key_id AS ssh_key_id, ssh_keys.name AS key_name, COUNT(nodes.id) AS node_count").
		Joins("JOIN ssh_keys ON ssh_keys.id = nodes.ssh_key_id").
		Where("nodes.ssh_key_id IS NOT NULL AND ssh_keys.disabled = ?", true).
		Group("nodes.ssh_key_id, ssh_keys.name").
		Order("node_count desc, nodes.ssh_key_id asc").
		Find(&rows).Error; err != nil {
		return item, err
	}
	item.Count = int64(len(rows))
	for _, row := range rows {
		if len(item.Examples) >= maxSecurityRiskExamples {
			break
		}
		item.Examples = append(item.Examples, keyUsageExample(row.SSHKeyID, row.KeyName, row.NodeCount))
	}
	return item, nil
}

func (h *SettingsHandler) expiredSSHKeyInUseRiskItem() (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "expired_ssh_keys_in_use",
		Severity:    "critical",
		Title:       "已过期但仍被引用的 SSH Key",
		Description: "节点仍引用已过期的 SSH Key，后续连接会被阻断并可能影响任务执行。",
	}
	type keyUsageRow struct {
		SSHKeyID  uint
		KeyName   string
		NodeCount int64
	}
	var rows []keyUsageRow
	if err := h.db.Table("nodes").
		Select("nodes.ssh_key_id AS ssh_key_id, ssh_keys.name AS key_name, COUNT(nodes.id) AS node_count").
		Joins("JOIN ssh_keys ON ssh_keys.id = nodes.ssh_key_id").
		Where("nodes.ssh_key_id IS NOT NULL AND ssh_keys.expires_at IS NOT NULL AND ssh_keys.expires_at <= ?", time.Now().UTC()).
		Group("nodes.ssh_key_id, ssh_keys.name").
		Order("node_count desc, nodes.ssh_key_id asc").
		Find(&rows).Error; err != nil {
		return item, err
	}
	item.Count = int64(len(rows))
	for _, row := range rows {
		if len(item.Examples) >= maxSecurityRiskExamples {
			break
		}
		item.Examples = append(item.Examples, keyUsageExample(row.SSHKeyID, row.KeyName, row.NodeCount))
	}
	return item, nil
}

func (h *SettingsHandler) staleSSHKeyRiskItem() (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "stale_ssh_keys",
		Severity:    "warning",
		Title:       "长期未使用的 SSH Key",
		Description: "长期未使用或从未使用的 SSH Key 应定期复核，降低遗留凭据带来的风险。",
	}
	cutoff := time.Now().UTC().Add(-staleSSHKeyAge)
	var keys []model.SSHKey
	if err := h.db.Select("id", "name", "last_used_at", "created_at").
		Where("(last_used_at IS NULL AND created_at <= ?) OR last_used_at <= ?", cutoff, cutoff).
		Order("id asc").
		Find(&keys).Error; err != nil {
		return item, err
	}
	item.Count = int64(len(keys))
	for _, key := range keys {
		if len(item.Examples) >= maxSecurityRiskExamples {
			break
		}
		item.Examples = append(item.Examples, sshKeyNameExample(key))
	}
	return item, nil
}

func (h *SettingsHandler) recentCredentialOperationRiskItem() (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "recent_credential_operations",
		Severity:    "info",
		Title:       "近期高风险凭据操作",
		Description: "近期存在 SSH Key 导出、终端或批量/命令任务等凭据使用事件；请结合审计记录复核操作者与用途。",
	}
	cutoff := time.Now().UTC().Add(-credentialAuditRiskAge)
	type credentialOperationRow struct {
		Action string
		Count  int64
	}
	var rows []credentialOperationRow
	if err := h.db.Model(&model.CredentialAuditEvent{}).
		Select("action, COUNT(*) AS count").
		Where("created_at >= ? AND action IN ?", cutoff, highRiskCredentialAuditActions()).
		Group("action").
		Order("count desc, action asc").
		Find(&rows).Error; err != nil {
		return item, err
	}
	for _, row := range rows {
		item.Count += row.Count
		if len(item.Examples) < maxSecurityRiskExamples {
			item.Examples = append(item.Examples, util.SanitizeMessage(fmt.Sprintf("%s（%d 次）", credentialActionLabel(row.Action), row.Count)))
		}
	}
	if item.Count > 0 {
		item.Severity = "warning"
	}
	return item, nil
}

func highRiskCredentialAuditActions() []string {
	return []string{
		"ssh_key.export",
		"auth.step_up",
		"file_browser.list",
		"file_browser.preview",
		"docker_volumes.discover",
		"config.export",
		"node.doctor.run",
		"node_migration.preflight",
		"probe.ssh",
		"probe.metrics",
		"node_logs.collect",
		"terminal.open",
		"terminal.failure",
		"task.manual_trigger",
		"task.restore_trigger",
		"task.batch_trigger",
		"snapshot.restore",
		"task.credential.use",
		"batch_command.create",
		"drill.trigger",
		"drill.phase",
	}
}

func credentialActionLabel(action string) string {
	switch action {
	case "ssh_key.export":
		return "SSH Key 导出"
	case "auth.step_up":
		return "二次验证"
	case "file_browser.list":
		return "节点文件列表浏览"
	case "file_browser.preview":
		return "节点文件预览"
	case "docker_volumes.discover":
		return "Docker 卷发现"
	case "config.export":
		return "配置导出"
	case "node.doctor.run":
		return "节点 Doctor 诊断"
	case "node_migration.preflight":
		return "节点迁移预检"
	case "probe.ssh":
		return "后台 SSH 探针"
	case "probe.metrics":
		return "后台指标采集"
	case "node_logs.collect":
		return "节点日志采集"
	case "terminal.open":
		return "终端会话打开"
	case "terminal.failure":
		return "终端会话失败"
	case "task.manual_trigger":
		return "手动任务触发"
	case "task.restore_trigger":
		return "恢复任务触发"
	case "task.batch_trigger":
		return "批量任务触发"
	case "snapshot.restore":
		return "快照恢复"
	case "task.credential.use":
		return "任务运行凭据使用"
	case "batch_command.create":
		return "批量命令创建"
	case "drill.trigger":
		return "恢复演练触发"
	case "drill.phase":
		return "恢复演练阶段凭据使用"
	default:
		return "凭据操作"
	}
}

func (h *SettingsHandler) weakSecurityDefaultsRiskItem() securityRiskItem {
	examples := make([]string, 0, maxSecurityRiskExamples)
	appendEnvBoolRisk := func(key string, defaultValue bool, riskyValue bool, label string) {
		value, err := util.ReadBoolEnv(key, defaultValue)
		if err != nil {
			examples = append(examples, key+" 配置值无效")
			return
		}
		if value == riskyValue {
			examples = append(examples, label)
		}
	}
	appendSettingBoolRisk := func(key string, envVar string, defaultValue bool, riskyValue bool, label string) {
		value := defaultValue
		raw := strings.TrimSpace(os.Getenv(envVar))
		if h.svc != nil {
			raw = strings.TrimSpace(h.svc.GetEffective(key))
		}
		if raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				examples = append(examples, key+" 配置值无效")
				return
			}
			value = parsed
		}
		if value == riskyValue {
			examples = append(examples, label)
		}
	}

	appendEnvBoolRisk("SSH_STRICT_HOST_KEY_CHECKING", true, false, "SSH 主机密钥校验已关闭")
	appendEnvBoolRisk("SSH_AUTO_ACCEPT_NEW_HOSTS", true, true, "SSH 自动接受未知主机密钥")
	appendEnvBoolRisk("WS_ALLOW_EMPTY_ORIGIN", false, true, "WebSocket 允许空 Origin")
	appendSettingBoolRisk("login.captcha_enabled", "LOGIN_CAPTCHA_ENABLED", false, false, "登录验证码未启用")
	appendSettingBoolRisk("login.second_captcha_enabled", "LOGIN_SECOND_CAPTCHA_ENABLED", false, false, "登录二次验证码未启用")

	item := securityRiskItem{
		Code:        "weak_security_defaults",
		Severity:    "warning",
		Title:       "弱安全默认项",
		Description: "这些开关不会自动修复；请结合部署环境评估是否需要收紧。",
		Count:       int64(len(examples)),
		Examples:    examples,
	}
	if len(item.Examples) > maxSecurityRiskExamples {
		item.Examples = item.Examples[:maxSecurityRiskExamples]
	}
	if item.Count == 0 {
		item.Severity = "info"
	}
	return item
}

func nodeNameExamples(nodes []model.Node) []string {
	examples := make([]string, 0, len(nodes))
	for _, node := range nodes {
		name := strings.TrimSpace(node.Name)
		if name == "" {
			name = fmt.Sprintf("Node #%d", node.ID)
		}
		examples = append(examples, util.SanitizeMessage(name))
	}
	return examples
}

func sshKeyNameExample(key model.SSHKey) string {
	name := strings.TrimSpace(key.Name)
	if name == "" {
		name = fmt.Sprintf("SSH Key #%d", key.ID)
	}
	return util.SanitizeMessage(name)
}

func keyUsageExample(keyID uint, keyName string, nodeCount int64) string {
	name := strings.TrimSpace(keyName)
	if name == "" {
		name = fmt.Sprintf("SSH Key #%d", keyID)
	}
	return util.SanitizeMessage(fmt.Sprintf("%s（%d 个节点）", name, nodeCount))
}
