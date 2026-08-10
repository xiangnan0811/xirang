package handlers

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"xirang/backend/internal/backupasset"
	"xirang/backend/internal/backupasset/publication"
	"xirang/backend/internal/config"
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
	db           *gorm.DB
	svc          *settings.Service
	transitioner publication.FeatureTransitioner
}

// backupAssetRuntimeSettingsTransitioner is an optional extension of the
// established admission transitioner. Keeping it separate preserves the
// FeatureTransitioner contract used by legacy callers and fixtures.
type backupAssetRuntimeSettingsTransitioner interface {
	TransitionBackupAssetSettings(
		context.Context,
		map[string]string,
		map[string]string,
		map[string]string,
		backupasset.ExportConfig,
		func() error,
	) error
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
	maxSecurityRiskExamples            = 3
	staleSSHKeyAge                     = 90 * 24 * time.Hour
	credentialAuditRiskAge             = 7 * 24 * time.Hour
	weakSecurityDefaultJWTTTLThreshold = 24 * time.Hour
)

// NewSettingsHandler 创建设置处理器
func NewSettingsHandler(db *gorm.DB, svc *settings.Service) *SettingsHandler {
	return &SettingsHandler{db: db, svc: svc}
}

// WithBackupAssetTransitioner installs the process-wide admission controller
// for the small set of foundation settings that can change Restic command
// eligibility. It remains optional for focused legacy handler tests.
func (h *SettingsHandler) WithBackupAssetTransitioner(transitioner publication.FeatureTransitioner) *SettingsHandler {
	if h != nil {
		h.transitioner = transitioner
	}
	return h
}

func transitionBackupAssetSettingsMutation(
	ctx context.Context,
	svc *settings.Service,
	transitioner publication.FeatureTransitioner,
	current map[string]string,
	overlay map[string]string,
	persist func() error,
) error {
	if err := svc.ValidateBackupAssetEffectiveUpdate(current, overlay); err != nil {
		return err
	}
	effective := copyBackupAssetSettings(current)
	for key, value := range overlay {
		effective[key] = value
	}
	if runtimeTransitioner, ok := transitioner.(backupAssetRuntimeSettingsTransitioner); ok && hasLiveBackupAssetRuntimeSettings(overlay) {
		config, err := backupasset.ExportConfigFromValues(effective)
		if err != nil {
			return err
		}
		if _, changesRoot := overlay["backup_assets.export.root"]; changesRoot {
			config.Root = strings.TrimSpace(current["backup_assets.export.root"])
		}
		return runtimeTransitioner.TransitionBackupAssetSettings(ctx, current, overlay, effective, config, persist)
	}
	if value, changesEnabled := overlay["backup_assets.enabled"]; changesEnabled {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		if transitioner == nil {
			return fmt.Errorf("backup asset feature transitioner is unavailable")
		}
		return transitioner.TransitionFeature(ctx, enabled, persist)
	}
	return persist()
}

func hasLiveBackupAssetRuntimeSettings(overlay map[string]string) bool {
	for key := range overlay {
		if key == "backup_assets.enabled" {
			return true
		}
		if key == "backup_assets.export.root" {
			continue
		}
		if strings.HasPrefix(key, "backup_assets.export.") || strings.HasPrefix(key, "backup_assets.archive.") ||
			strings.HasPrefix(key, "backup_assets.recovery.") || key == "backup_assets.idempotency_ttl" ||
			key == "backup_assets.idempotency_key_max_bytes" {
			return true
		}
	}
	return false
}

func copyBackupAssetSettings(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
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
	items, err := h.securityRiskItems(c)
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

	if err := h.persistSettingsMutation(c.Request.Context(), req); err != nil {
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
	if err := h.deleteSettingOverride(c.Request.Context(), key); err != nil {
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

func (h *SettingsHandler) persistSettingsMutation(ctx context.Context, values map[string]string) error {
	if h == nil || h.db == nil || h.svc == nil {
		return fmt.Errorf("settings handler is unavailable")
	}
	containsFoundation := false
	foundationOverlay := make(map[string]string)
	for key, value := range values {
		if settings.IsBackupAssetFoundationSetting(key) {
			containsFoundation = true
			foundationOverlay[key] = value
		}
	}
	persist := func() error {
		return h.db.Transaction(func(tx *gorm.DB) error {
			for key, value := range values {
				if err := h.svc.UpdateWithTx(tx, key, value); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if !containsFoundation {
		return persist()
	}
	return h.svc.WithBackupAssetMutation(ctx, func(current map[string]string) error {
		return transitionBackupAssetSettingsMutation(ctx, h.svc, h.transitioner, current, foundationOverlay, persist)
	})
}

func (h *SettingsHandler) deleteSettingOverride(ctx context.Context, key string) error {
	if h == nil || h.db == nil || h.svc == nil {
		return fmt.Errorf("settings handler is unavailable")
	}
	if !settings.IsBackupAssetFoundationSetting(key) {
		return h.svc.Delete(key)
	}
	return h.svc.WithBackupAssetMutation(ctx, func(current map[string]string) error {
		fallback, err := h.svc.GetFallback(key)
		if err != nil {
			return err
		}
		override := map[string]string{key: fallback}
		persist := func() error {
			return h.db.Transaction(func(tx *gorm.DB) error {
				return h.svc.DeleteWithTx(tx, key)
			})
		}
		return transitionBackupAssetSettingsMutation(ctx, h.svc, h.transitioner, current, override, persist)
	})
}

func (h *SettingsHandler) securityRiskItems(c *gin.Context) ([]securityRiskItem, error) {
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
	privilegedAuthItem, err := h.privilegedUsersWithoutTOTPRiskItem()
	if err != nil {
		return nil, err
	}
	adminRecoveryItem, err := h.adminRecoveryPostureRiskItem()
	if err != nil {
		return nil, err
	}
	auditIntegrityItem, err := h.auditLogIntegrityPostureRiskItem()
	if err != nil {
		return nil, err
	}
	hostKeyTrustItem := h.sshHostKeyTrustPostureRiskItem()
	backupRestoreItem, err := h.backupRestorePostureRiskItem(c)
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
		privilegedAuthItem,
		adminRecoveryItem,
		auditIntegrityItem,
		hostKeyTrustItem,
		deploymentSecretPostureRiskItem(),
		backupRestoreItem,
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

func (h *SettingsHandler) privilegedUsersWithoutTOTPRiskItem() (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "privileged_users_without_totp",
		Severity:    "warning",
		Title:       "高权限用户未启用两步验证",
		Description: "管理员和操作员账号应启用两步验证，以降低账号凭据泄露后的控制面风险。",
	}
	type privilegedUserRow struct {
		ID       uint
		Username string
		Role     string
	}
	privilegedWithoutTOTP := func(db *gorm.DB) *gorm.DB {
		return db.Model(&model.User{}).
			Where("role IN ? AND totp_enabled = ?", []string{"admin", "operator"}, false)
	}
	if err := privilegedWithoutTOTP(h.db).Count(&item.Count).Error; err != nil {
		return item, err
	}
	var rows []privilegedUserRow
	if err := privilegedWithoutTOTP(h.db).
		Select("id", "username", "role").
		Order("id asc").
		Limit(maxSecurityRiskExamples).
		Find(&rows).Error; err != nil {
		return item, err
	}
	for _, row := range rows {
		item.Examples = append(item.Examples, privilegedUserExample(row.ID, row.Username, row.Role))
	}
	if item.Count == 0 {
		item.Severity = "info"
	}
	return item, nil
}

func (h *SettingsHandler) adminRecoveryPostureRiskItem() (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "admin_recovery_posture",
		Severity:    "warning",
		Title:       "管理员恢复姿态",
		Description: "个人或小团队应保留可恢复的管理员访问路径，避免单个账号、两步验证设备或恢复码遗失导致控制面锁死。",
	}
	countUsers := func(scope func(*gorm.DB) *gorm.DB) (int64, error) {
		var count int64
		if err := scope(h.db.Model(&model.User{})).Count(&count).Error; err != nil {
			return 0, err
		}
		return count, nil
	}
	adminCount, err := countUsers(func(db *gorm.DB) *gorm.DB {
		return db.Where("role = ?", "admin")
	})
	if err != nil {
		return item, err
	}
	operatorCount, err := countUsers(func(db *gorm.DB) *gorm.DB {
		return db.Where("role = ?", "operator")
	})
	if err != nil {
		return item, err
	}
	adminWithTOTP, err := countUsers(func(db *gorm.DB) *gorm.DB {
		return db.Where("role = ? AND totp_enabled = ?", "admin", true)
	})
	if err != nil {
		return item, err
	}
	adminWithRecoveryEvidence, err := countUsers(func(db *gorm.DB) *gorm.DB {
		return db.Where("role = ? AND totp_enabled = ? AND TRIM(COALESCE(recovery_codes, '')) <> ''", "admin", true)
	})
	if err != nil {
		return item, err
	}
	totpAdminWithoutRecoveryEvidence, err := countUsers(func(db *gorm.DB) *gorm.DB {
		return db.Where("role = ? AND totp_enabled = ? AND TRIM(COALESCE(recovery_codes, '')) = ''", "admin", true)
	})
	if err != nil {
		return item, err
	}

	addFinding := func(label string) {
		item.Count++
		if len(item.Examples) < maxSecurityRiskExamples {
			item.Examples = append(item.Examples, label)
		}
	}
	critical := false
	if adminCount == 0 {
		addFinding("未发现管理员账号")
		critical = true
	} else {
		if adminCount == 1 {
			addFinding("仅有一个管理员账号")
		}
		if adminWithTOTP == 0 {
			addFinding("没有管理员启用两步验证")
		} else if adminWithTOTP == adminCount && adminWithRecoveryEvidence == 0 {
			addFinding("所有管理员依赖两步验证但缺少恢复码证据")
			critical = true
		} else if totpAdminWithoutRecoveryEvidence > 0 {
			addFinding("存在启用两步验证但缺少恢复码证据的管理员")
		}
	}
	if operatorCount == 0 {
		addFinding("缺少低权限操作员账号作为日常操作后备")
	}
	if item.Count == 0 {
		item.Severity = "info"
	} else if critical {
		item.Severity = "critical"
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
		"config.import",
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
	case "config.import":
		return "配置导入"
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

func (h *SettingsHandler) auditLogIntegrityPostureRiskItem() (item securityRiskItem, err error) {
	item = securityRiskItem{
		Code:        "audit_log_integrity_posture",
		Severity:    "info",
		Title:       "审计日志完整性姿态",
		Description: "审计日志哈希链应保持连续，以便管理员复核关键控制面操作的完整性。",
		Examples:    []string{},
	}
	type auditIntegrityRow struct {
		ID        uint
		PrevHash  string
		EntryHash string
	}
	rows, err := h.db.Model(&model.AuditLog{}).
		Select("id", "prev_hash", "entry_hash").
		Order("id asc").
		Rows()
	if err != nil {
		return item, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	missingEntryHash := int64(0)
	missingPrevHash := int64(0)
	brokenLinks := int64(0)
	rowIndex := int64(0)
	previousEntryHash := ""
	for rows.Next() {
		var row auditIntegrityRow
		if err := h.db.ScanRows(rows, &row); err != nil {
			return item, err
		}
		entryHash := strings.TrimSpace(row.EntryHash)
		if entryHash == "" {
			missingEntryHash++
		}
		if rowIndex > 0 {
			prevHash := strings.TrimSpace(row.PrevHash)
			if prevHash == "" {
				missingPrevHash++
			} else if prevHash != previousEntryHash {
				brokenLinks++
			}
		}
		previousEntryHash = entryHash
		rowIndex++
	}
	if err := rows.Err(); err != nil {
		return item, err
	}
	if rowIndex == 0 {
		return item, nil
	}

	appendFinding := func(count int64, label string) {
		if count == 0 {
			return
		}
		item.Count += count
		if len(item.Examples) < maxSecurityRiskExamples {
			item.Examples = append(item.Examples, label)
		}
	}
	appendFinding(missingEntryHash, "审计日志存在缺失的完整性哈希")
	appendFinding(missingPrevHash, "审计日志存在缺失的前序哈希")
	appendFinding(brokenLinks, "审计日志哈希链存在断点")
	if item.Count > 0 {
		item.Severity = "critical"
	}
	return item, nil
}

func (h *SettingsHandler) sshHostKeyTrustPostureRiskItem() securityRiskItem {
	examples := make([]string, 0, maxSecurityRiskExamples)
	strictHostCheck, strictErr := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
	if strictErr != nil {
		examples = append(examples, "SSH 主机密钥校验配置值无效")
	} else if !strictHostCheck {
		examples = append(examples, "SSH 主机密钥校验已关闭")
	} else {
		autoAccept, autoAcceptErr := util.ReadBoolEnv("SSH_AUTO_ACCEPT_NEW_HOSTS", true)
		if autoAcceptErr != nil {
			examples = append(examples, "SSH 自动接受未知主机密钥配置值无效")
		} else if autoAccept {
			examples = append(examples, "SSH 自动接受首次发现的主机密钥")
		}
	}

	item := securityRiskItem{
		Code:        "ssh_host_key_trust_posture",
		Severity:    "warning",
		Title:       "SSH 主机密钥信任姿态",
		Description: "SSH 主机密钥校验应保持启用，并谨慎评估首次连接自动接受未知主机密钥的部署边界。",
		Count:       int64(len(examples)),
		Examples:    examples,
	}
	if item.Count == 0 {
		item.Severity = "info"
	}
	return item
}

func deploymentSecretPostureRiskItem() securityRiskItem {
	findings := []deploymentSecretPostureFinding{
		{risky: util.IsDevelopmentEnv(), label: "运行环境仍处于开发模式"},
		{risky: config.IsWeakJWTSecret(os.Getenv("JWT_SECRET")), label: "JWT 签名密钥缺失或强度不足"},
		{risky: config.IsWeakDataEncryptionKey(os.Getenv("DATA_ENCRYPTION_KEY")), label: "数据加密密钥缺失或强度不足"},
		{risky: isPlaceholderAdminInitialPassword(os.Getenv("ADMIN_INITIAL_PASSWORD")), label: "初始管理员密码仍为占位值"},
		{risky: strings.TrimSpace(os.Getenv("APP_ENV")) == "", label: "APP_ENV 未明确声明运行环境"},
	}

	examples := make([]string, 0, maxSecurityRiskExamples)
	var count int64
	for _, finding := range findings {
		if !finding.risky {
			continue
		}
		count++
		if len(examples) < maxSecurityRiskExamples {
			examples = append(examples, finding.label)
		}
	}

	item := securityRiskItem{
		Code:        "deployment_secret_posture",
		Severity:    "warning",
		Title:       "部署密钥姿态",
		Description: "个人或小团队部署也应使用明确环境和强随机密钥，避免以开发默认值暴露控制面。",
		Count:       count,
		Examples:    examples,
	}
	if item.Count == 0 {
		item.Severity = "info"
	}
	return item
}

type deploymentSecretPostureFinding struct {
	risky bool
	label string
}

func isPlaceholderAdminInitialPassword(value string) bool {
	switch strings.TrimSpace(value) {
	case "change-me", "change-me-admin-password", "admin", "password", "please-change-me":
		return true
	default:
		return false
	}
}

func (h *SettingsHandler) backupRestorePostureRiskItem(c *gin.Context) (securityRiskItem, error) {
	item := securityRiskItem{
		Code:        "backup_restore_posture",
		Severity:    "warning",
		Title:       "备份恢复姿态",
		Description: "个人或小团队也应定期确认备份新鲜度、校验结果和恢复演练证据，避免只拥有不可恢复的备份。",
	}

	var policies []model.Policy
	if err := h.db.Where("enabled = ? AND is_template = ?", true, false).Preload("Nodes").Order("id asc").Find(&policies).Error; err != nil {
		return item, err
	}
	if len(policies) == 0 {
		item.Count = 1
		item.Examples = []string{"尚未配置启用中的备份策略"}
		return item, nil
	}

	aggregated := make(map[string]int64)
	confidenceHandler := NewBackupConfidenceHandler(h.db)
	now := time.Now().UTC()
	for _, policy := range policies {
		ctx, err := confidenceHandler.loadPolicyContext(c, policy)
		if err != nil {
			return item, err
		}
		if len(ctx.Tasks) > 0 && !backupRestorePostureHasExecutableTask(ctx.Tasks) {
			addBackupRestorePostureFinding(aggregated, "no_task")
		}
		// 仅补齐已有执行但从未成功的场景，避免与 no_successful_backup 重复计数。
		if ctx.LatestBackupRun != nil && ctx.LatestSuccessfulBackupRun == nil {
			addBackupRestorePostureFinding(aggregated, "no_successful_backup")
		}
		confidence := buildBackupConfidenceItem(now, ctx)
		for _, reason := range confidence.Reasons {
			addBackupRestorePostureFinding(aggregated, reason.Code)
		}
	}

	examples := make([]string, 0, maxSecurityRiskExamples)
	for _, label := range backupRestorePostureLabelOrder() {
		count := aggregated[label]
		if count == 0 {
			continue
		}
		item.Count += count
		if len(examples) < maxSecurityRiskExamples {
			examples = append(examples, label)
		}
	}
	item.Examples = examples
	if item.Count == 0 {
		item.Severity = "info"
	} else if backupRestorePostureHasCritical(aggregated) {
		item.Severity = "critical"
	}
	return item, nil
}

type backupRestorePostureLabelSpec struct {
	code     string
	label    string
	critical bool
}

func backupRestorePostureLabelSpecs() []backupRestorePostureLabelSpec {
	return []backupRestorePostureLabelSpec{
		{code: "no_task", label: "存在启用策略尚未关联可执行备份任务", critical: true},
		{code: "no_successful_backup", label: "存在启用策略缺少成功备份证据", critical: true},
		{code: "recent_backup_failed", label: "存在最近备份失败的策略", critical: true},
		{code: "backup_not_completed", label: "存在最近备份尚未完成的策略"},
		{code: "verify_failed", label: "存在备份校验失败证据", critical: true},
		{code: "verify_warning", label: "存在备份校验告警证据"},
		{code: "verify_missing", label: "存在启用校验但缺少校验证据的策略"},
		{code: "recent_run_failed", label: "存在最近任务执行失败的策略", critical: true},
		{code: "rpo_unknown", label: "存在无法证明 RPO 达标的策略"},
		{code: "rpo_exceeded", label: "存在 RPO 已超限的策略", critical: true},
		{code: "drill_missing", label: "存在缺少恢复演练证据的策略"},
		{code: "drill_failed", label: "存在恢复演练失败证据", critical: true},
		{code: "drill_not_confident", label: "存在恢复演练不能作为可信证据的策略"},
		{code: "integrity_alert", label: "存在未解决的完整性校验告警", critical: true},
		{code: "verification_alert", label: "存在未解决的备份校验告警"},
		{code: "drill_alert", label: "存在未解决的恢复演练告警", critical: true},
	}
}

func addBackupRestorePostureFinding(counts map[string]int64, code string) {
	label := backupRestorePostureLabel(code)
	if label == "" {
		return
	}
	counts[label]++
}

func backupRestorePostureLabel(code string) string {
	for _, spec := range backupRestorePostureLabelSpecs() {
		if spec.code == code {
			return spec.label
		}
	}
	return ""
}

func backupRestorePostureHasExecutableTask(tasks []model.Task) bool {
	for _, task := range tasks {
		if task.Enabled {
			return true
		}
	}
	return false
}

func backupRestorePostureLabelOrder() []string {
	specs := backupRestorePostureLabelSpecs()
	labels := make([]string, 0, len(specs))
	for _, spec := range specs {
		labels = append(labels, spec.label)
	}
	return labels
}

func backupRestorePostureHasCritical(counts map[string]int64) bool {
	for _, spec := range backupRestorePostureLabelSpecs() {
		if spec.critical && counts[spec.label] > 0 {
			return true
		}
	}
	return false
}

func (h *SettingsHandler) weakSecurityDefaultsRiskItem() securityRiskItem {
	item := securityRiskItem{
		Code:        "weak_security_defaults",
		Severity:    "warning",
		Title:       "弱安全默认项",
		Description: "这些本地硬化提示不会自动修复；请结合部署环境评估是否需要收紧。",
		Examples:    make([]string, 0, maxSecurityRiskExamples),
	}
	addFinding := func(label string) {
		item.Count++
		if len(item.Examples) < maxSecurityRiskExamples {
			item.Examples = append(item.Examples, label)
		}
	}
	appendEnvBoolRisk := func(key string, defaultValue bool, riskyValue bool, label string) {
		value, err := util.ReadBoolEnv(key, defaultValue)
		if err != nil {
			addFinding(key + " 配置值无效")
			return
		}
		if value == riskyValue {
			addFinding(label)
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
				addFinding(key + " 配置值无效")
				return
			}
			value = parsed
		}
		if value == riskyValue {
			addFinding(label)
		}
	}
	appendCORSRisk := func() {
		raw, configured := os.LookupEnv("CORS_ALLOWED_ORIGINS")
		if !configured || strings.TrimSpace(raw) == "" {
			addFinding("CORS 允许来源未显式声明")
			return
		}
		for _, origin := range strings.Split(raw, ",") {
			if strings.TrimSpace(origin) == "*" {
				addFinding("CORS 允许来源包含通配符")
				return
			}
		}
	}
	appendJWTTTLRisk := func() {
		raw := strings.TrimSpace(os.Getenv("JWT_TTL"))
		if raw == "" {
			raw = "24h"
		}
		jwtTTL, err := time.ParseDuration(raw)
		if err != nil {
			addFinding("JWT 会话有效期配置值无效")
			return
		}
		if jwtTTL > weakSecurityDefaultJWTTTLThreshold {
			addFinding("JWT 会话有效期偏长")
		}
	}

	appendEnvBoolRisk("WS_ALLOW_EMPTY_ORIGIN", false, true, "WebSocket 允许空 Origin")
	appendSettingBoolRisk("login.captcha_enabled", "LOGIN_CAPTCHA_ENABLED", false, false, "登录验证码未启用")
	appendSettingBoolRisk("login.second_captcha_enabled", "LOGIN_SECOND_CAPTCHA_ENABLED", false, false, "登录二次验证码未启用")
	if strings.TrimSpace(os.Getenv("METRICS_TOKEN")) == "" {
		addFinding("Metrics 抓取端点未配置 token 保护")
	}
	appendCORSRisk()
	appendJWTTTLRisk()

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

func privilegedUserExample(userID uint, username string, role string) string {
	name := strings.TrimSpace(username)
	if name == "" {
		name = fmt.Sprintf("User #%d", userID)
	}
	return util.SanitizeMessage(fmt.Sprintf("%s（%s）", name, role))
}
